package releasesnapshot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type failingSources struct {
	fakeSources
	err error
}

func (f *failingSources) DownloadArchive(context.Context, string, string, int64, string) (io.ReadCloser, error) {
	return nil, f.err
}

type archiveBodySources struct {
	fakeSources
	body io.ReadCloser
}

func (f *archiveBodySources) DownloadArchive(context.Context, string, string, int64, string) (io.ReadCloser, error) {
	return f.body, nil
}

type zeroReadCloser struct{ remaining int64 }

func (r *zeroReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	clear(p)
	r.remaining -= int64(len(p))
	return len(p), nil
}
func (*zeroReadCloser) Close() error { return nil }

type coordinatedSources struct {
	archive []byte
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (f *coordinatedSources) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	return sourceconnections.SourceRepository{ID: 7, Owner: "renamed", Name: "repo"}, sourceconnections.Branch{Name: "main", SHA: snapshotSHA}, nil
}
func (f *coordinatedSources) ReadTree(context.Context, string, string, int64, string) (githubapp.Tree, error) {
	return githubapp.Tree{Entries: []githubapp.TreeEntry{{Path: "compose.yaml", Type: "blob", SHA: snapshotSHA}}}, nil
}
func (f *coordinatedSources) DownloadArchive(context.Context, string, string, int64, string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	f.entered <- struct{}{}
	<-f.release
	if call > 2 {
		return nil, errors.New("unexpected download")
	}
	return io.NopCloser(bytes.NewReader(f.archive)), nil
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

func TestIndependentMaterializersConvergeAndFinalizeUniqueReadyLoser(t *testing.T) {
	db := snapshotDB(t)
	source := &coordinatedSources{
		archive: composeArchive(t, "services: {}\n"),
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	root := t.TempDir()
	first, err := New(db, source, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(db, source, root)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		release Release
		err     error
	}
	results := make(chan result, 2)
	app := "11111111-1111-1111-1111-111111111111"
	go func() { r, err := first.Materialize(context.Background(), "owner", app); results <- result{r, err} }()
	go func() { r, err := second.Materialize(context.Background(), "owner", app); results <- result{r, err} }()
	<-source.entered
	<-source.entered
	close(source.release)
	firstResult, secondResult := <-results, <-results
	if firstResult.err != nil || secondResult.err != nil || firstResult.release.ID != secondResult.release.ID {
		t.Fatalf("materializations = %#v %#v", firstResult, secondResult)
	}
	var ready, failed, materializing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM releases WHERE workspace_state='ready'`).Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM releases WHERE workspace_state='failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM releases WHERE workspace_state='materializing'`).Scan(&materializing); err != nil {
		t.Fatal(err)
	}
	if ready != 1 || failed != 1 || materializing != 0 {
		t.Fatalf("release states ready=%d failed=%d materializing=%d", ready, failed, materializing)
	}
}

func TestMaterializePersistsOriginalProviderFailureTaxonomyAfterCleanup(t *testing.T) {
	db := snapshotDB(t)
	source := &failingSources{err: &sourceconnections.Error{Code: "provider_unavailable"}}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Materialize(context.Background(), "owner", "11111111-1111-1111-1111-111111111111")
	if !IsCode(err, "provider_unavailable") {
		t.Fatalf("materialize error = %v", err)
	}
	var state, code string
	if err := db.QueryRow(`SELECT workspace_state, materialization_error_code FROM releases`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != WorkspaceStateFailed || code != "provider_unavailable" {
		t.Fatalf("failure persistence state=%q code=%q", state, code)
	}
}

func TestMaterializePersistsSanitizedFailureTaxonomyAfterSuccessfulCleanup(t *testing.T) {
	app := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name   string
		source SourceReader
		code   string
	}{
		{"provider", &failingSources{err: &sourceconnections.Error{Code: "provider_unavailable"}}, "provider_unavailable"},
		{"access-lost", &failingSources{err: &sourceconnections.Error{Code: "source_access_lost"}}, "source_access_lost"},
		{"cancel", &failingSources{err: context.Canceled}, "canceled"},
		{"invalid-archive", &archiveBodySources{body: io.NopCloser(strings.NewReader("not gzip"))}, "invalid_source"},
		{"compressed-limit", &archiveBodySources{body: &zeroReadCloser{remaining: MaxCompressedBytes + 1}}, "source_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := snapshotDB(t)
			m, err := New(db, test.source, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Materialize(context.Background(), "owner", app); !IsCode(err, test.code) {
				t.Fatalf("materialize code = %v, want %s", err, test.code)
			}
			var id, state, code string
			if err := db.QueryRow(`SELECT id, workspace_state, materialization_error_code FROM releases`).Scan(&id, &state, &code); err != nil {
				t.Fatal(err)
			}
			if state != WorkspaceStateFailed || code != test.code {
				t.Fatalf("failure persistence state=%q code=%q, want failed/%q", state, code, test.code)
			}
			staging, err := m.stagingPath(app, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(staging); !os.IsNotExist(err) {
				t.Fatalf("staging remains after cleanup: %v", err)
			}
		})
	}
}

func TestMaterializeCleanupFailureRemainsRecoverableAfterRestart(t *testing.T) {
	db := snapshotDB(t)
	root := t.TempDir()
	m, err := New(db, &failingSources{err: &sourceconnections.Error{Code: "provider_unavailable"}}, root)
	if err != nil {
		t.Fatal(err)
	}
	fs := m.fs
	fs.removeAll = func(string) error { return errors.New("injected cleanup failure") }
	m.fs = fs
	app := "11111111-1111-1111-1111-111111111111"
	if _, err := m.Materialize(context.Background(), "owner", app); !IsCode(err, "internal_error") {
		t.Fatalf("cleanup failure error = %v", err)
	}
	var id, state string
	if err := db.QueryRow(`SELECT id, workspace_state FROM releases`).Scan(&id, &state); err != nil {
		t.Fatal(err)
	}
	if state != WorkspaceStateMaterializing {
		t.Fatalf("cleanup failure state = %q", state)
	}
	staging, err := m.stagingPath(app, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("recoverable staging missing: %v", err)
	}
	restarted, err := New(db, &fakeSources{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("restart did not remove staging: %v", err)
	}
	if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, id).Scan(&state); err != nil || state != WorkspaceStateFailed {
		t.Fatalf("recovered state=%q err=%v", state, err)
	}
}

func TestMaterializeInvalidatesTamperedReadyWorkspaceWithoutPersistingComposeContents(t *testing.T) {
	db := snapshotDB(t)
	compose := "services: {}\n# ghp_snapshot_should_never_reach_sqlite\n"
	source := &fakeSources{archive: composeArchive(t, compose)}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	first, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.WorkspacePath, "compose.yaml"), []byte("invalid: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || source.calls != 2 {
		t.Fatalf("tampered release reused: first=%s second=%s calls=%d", first.ID, second.ID, source.calls)
	}
	var state, stored string
	if err := db.QueryRow(`SELECT workspace_state, metadata_json FROM releases WHERE id=?`, first.ID).Scan(&state, &stored); err != nil {
		t.Fatal(err)
	}
	if state != WorkspaceStateFailed {
		t.Fatalf("tampered ready state=%q", state)
	}
	if strings.Contains(stored, "ghp_snapshot_should_never_reach_sqlite") {
		t.Fatalf("compose content reached sqlite: %q", stored)
	}
	var releasePayload string
	if err := db.QueryRow(`SELECT group_concat(COALESCE(repository_owner,'') || COALESCE(repository_name,'') || COALESCE(resolved_sha,'') || COALESCE(compose_path,'') || COALESCE(archive_sha256,'') || COALESCE(workspace_path,'') || COALESCE(metadata_json,''), '') FROM releases`).Scan(&releasePayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(releasePayload, "ghp_snapshot_should_never_reach_sqlite") {
		t.Fatalf("release row persisted compose content: %q", releasePayload)
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
