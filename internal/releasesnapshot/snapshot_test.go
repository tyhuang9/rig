package releasesnapshot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const snapshotSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeSources struct {
	archive []byte
	calls   int
}

func (f *fakeSources) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	return sourceconnections.SourceRepository{ID: 7, Owner: "renamed", Name: "repo"}, sourceconnections.Branch{Name: "main", SHA: snapshotSHA}, nil
}
func (f *fakeSources) ReadTree(context.Context, string, string, int64, string) (githubapp.Tree, error) {
	return githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: snapshotSHA}}}, nil
}
func (f *fakeSources) DownloadArchive(context.Context, string, string, int64, string) (io.ReadCloser, error) {
	f.calls++
	return io.NopCloser(bytes.NewReader(f.archive)), nil
}

func TestMaterializeInstallsImmutableWorkspaceAndReusesReadyRelease(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeArchive(t, "services: {}\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := m.Materialize(context.Background(), "owner", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if release.WorkspaceState != WorkspaceStateReady || source.calls != 1 {
		t.Fatalf("release=%#v calls=%d", release, source.calls)
	}
	if _, err := os.Stat(filepath.Join(release.WorkspacePath, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(release.WorkspacePath) != "workspace" || filepath.Base(filepath.Dir(release.WorkspacePath)) != release.ID {
		t.Fatalf("workspace layout = %q", release.WorkspacePath)
	}
	again, err := m.Materialize(context.Background(), "owner", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != release.ID || source.calls != 1 {
		t.Fatalf("reuse=%#v calls=%d", again, source.calls)
	}
	var owner, name, state, storedPath string
	if err := db.QueryRow(`SELECT repository_owner,repository_name,workspace_state,workspace_path FROM releases WHERE id=?`, release.ID).Scan(&owner, &name, &state, &storedPath); err != nil || owner != "renamed" || name != "repo" || state != "ready" || filepath.IsAbs(storedPath) {
		t.Fatalf("provenance = %q %q %q %q %v", owner, name, state, storedPath, err)
	}
}
func TestMaterializeRejectsWrongOwnerAndUnsafeRoots(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeArchive(t, "services: {}\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Materialize(context.Background(), "other", "11111111-1111-1111-1111-111111111111"); !IsCode(err, "invalid_source") {
		t.Fatalf("wrong owner = %v", err)
	}
	for _, root := range []string{"", "relative", t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "x"} {
		if _, err := New(db, source, root); err == nil {
			t.Fatalf("unsafe root accepted: %q", root)
		}
	}
}
func TestRecoverFailsOnlyValidatedMaterializationStaging(t *testing.T) {
	db := snapshotDB(t)
	m, err := New(db, &fakeSources{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	app := "11111111-1111-1111-1111-111111111111"
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,workspace_state) VALUES(?,?, 'materializing','{}',datetime('now'),'materializing')`, id, app); err != nil {
		t.Fatal(err)
	}
	staging, err := m.stagingPath(app, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging remained: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, id).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("state=%q %v", state, err)
	}
	if err := m.removeStaging(app, "not-an-id"); err == nil {
		t.Fatal("unsafe cleanup target accepted")
	}
}
func snapshotDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	app := "11111111-1111-1111-1111-111111111111"
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES ('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES ('0123456789abcdef0123456789abcdef','owner','github','connected','1','owner',1,datetime('now','+1 hour'),datetime('now','+1 day'),datetime('now'),datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?, 'App','draft',datetime('now'),datetime('now'))`, app, "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?, 'github','0123456789abcdef0123456789abcdef',3,7,'old','repo','main','refs/heads/main','compose.yaml',?,datetime('now'),datetime('now'))`, app, snapshotSHA); err != nil {
		t.Fatal(err)
	}
	return db
}
func composeArchive(t *testing.T, compose string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range []struct {
		name, body string
		typ        byte
	}{{"repo/", "", tar.TypeDir}, {"repo/compose.yaml", compose, tar.TypeReg}} {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o600, Size: int64(len(e.body))}
		if e.typ == tar.TypeDir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
