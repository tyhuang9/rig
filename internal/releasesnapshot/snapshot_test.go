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

func TestMaterializePinsConfigurationRevisionAndDoesNotReuseAfterChange(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeArchive(t, "services: {}\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	initial, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if initial.ConfigurationRevisionID != "" || initial.ConfigurationRevisionNumber != 0 {
		t.Fatalf("initial pin=%+v", initial)
	}
	revisionID := "22222222-2222-2222-2222-222222222222"
	if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_at,variable_count,secret_count) VALUES(?,?,1,?,datetime('now'),1,0)`, revisionID, app, "apps/"+app+"/configuration/"+revisionID+".secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE application_configuration_heads SET revision_id=?,revision_number=1,updated_at=datetime('now') WHERE app_id=?`, revisionID, app); err != nil {
		t.Fatal(err)
	}
	changed, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID == initial.ID || changed.ConfigurationRevisionID != revisionID || changed.ConfigurationRevisionNumber != 1 || source.calls != 2 {
		t.Fatalf("changed=%+v initial=%+v calls=%d", changed, initial, source.calls)
	}
	again, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != changed.ID || source.calls != 2 {
		t.Fatalf("reuse=%+v calls=%d", again, source.calls)
	}
	var pinnedID sql.NullString
	var pinnedNumber int64
	if err := db.QueryRow(`SELECT configuration_revision_id,configuration_revision_number FROM releases WHERE id=?`, initial.ID).Scan(&pinnedID, &pinnedNumber); err != nil || pinnedID.Valid || pinnedNumber != 0 {
		t.Fatalf("initial database pin=%v/%d err=%v", pinnedID, pinnedNumber, err)
	}
}

func TestReadyReleaseReturnsOnlyAppBoundReadyValidatedWorkspace(t *testing.T) {
	db := snapshotDB(t)
	m, err := New(db, &fakeSources{archive: composeArchive(t, "services: {}\n")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := m.ReadyRelease(context.Background(), app, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.ID != release.ID || ready.AppID != app || ready.WorkspaceState != WorkspaceStateReady || ready.WorkspacePath != release.WorkspacePath {
		t.Fatalf("ready release = %#v, materialized = %#v", ready, release)
	}
	for _, request := range []struct{ app, release string }{
		{"22222222-2222-2222-2222-222222222222", release.ID},
		{app, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{app, "not-a-release"},
	} {
		if _, err := m.ReadyRelease(context.Background(), request.app, request.release); !IsCode(err, "release_not_found") {
			t.Fatalf("lookup app=%q release=%q error=%v", request.app, request.release, err)
		}
	}
	if _, err := db.Exec(`UPDATE releases SET status='failed',workspace_state='failed',materialization_error_code='invalid_source' WHERE id=?`, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "release_not_found") {
		t.Fatalf("non-ready release error = %v", err)
	}
}

func TestReadyReleaseInvalidatesTamperedWorkspaceAndStoredPath(t *testing.T) {
	for _, test := range []struct {
		name               string
		workspacePreserved bool
		tamper             func(*testing.T, *sql.DB, *Materializer, Release)
	}{
		{"compose", false, func(t *testing.T, _ *sql.DB, _ *Materializer, release Release) {
			if err := os.WriteFile(filepath.Join(release.WorkspacePath, "compose.yaml"), []byte("invalid: ["), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"stored-path", true, func(t *testing.T, db *sql.DB, _ *Materializer, release Release) {
			if _, err := db.Exec(`UPDATE releases SET workspace_path='../outside' WHERE id=?`, release.ID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := snapshotDB(t)
			m, err := New(db, &fakeSources{archive: composeArchive(t, "services: {}\n")}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			app := "11111111-1111-1111-1111-111111111111"
			release, err := m.Materialize(context.Background(), "owner", app)
			if err != nil {
				t.Fatal(err)
			}
			test.tamper(t, db, m, release)
			if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "invalid_source") {
				t.Fatalf("tampered release error = %v", err)
			}
			var state, code string
			if err := db.QueryRow(`SELECT workspace_state,materialization_error_code FROM releases WHERE id=?`, release.ID).Scan(&state, &code); err != nil || state != WorkspaceStateFailed || code != "invalid_source" {
				t.Fatalf("state=%q code=%q err=%v", state, code, err)
			}
			_, statErr := os.Stat(release.WorkspacePath)
			if test.workspacePreserved && statErr != nil {
				t.Fatalf("unsafe-path workspace was touched: %v", statErr)
			}
			if !test.workspacePreserved && !os.IsNotExist(statErr) {
				t.Fatalf("safely addressable invalid workspace was not removed: %v", statErr)
			}
		})
	}
}

func TestReadyReleaseRejectsSymlinkedWorkspaceWithoutTouchingTarget(t *testing.T) {
	db := snapshotDB(t)
	m, err := New(db, &fakeSources{archive: composeArchive(t, "services: {}\n")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(release.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	sentinel := filepath.Join(external, "compose.yaml")
	if err := os.WriteFile(sentinel, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, release.WorkspacePath); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "invalid_source") {
		t.Fatalf("symlinked release error = %v", err)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "services: {}\n" {
		t.Fatalf("external target changed: %q %v", contents, err)
	}
}

func TestValidateComposeWorkspaceIgnoresUnreferencedSymlink(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(workspace, "node_modules")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := ValidateComposeWorkspace(workspace, "compose.yaml"); err != nil {
		t.Fatalf("unreferenced symlink rejected: %v", err)
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

func TestMaterializeInvalidatesSameSizeDockerfileMutationAndRematerializes(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeAndDockerfileArchive(t, "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile\n", "FROM scratch\n# one\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	first, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.WorkspacePath, "Dockerfile"), []byte("FROM scratch\n# two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || source.calls != 2 {
		t.Fatalf("same-size Dockerfile mutation reused=%+v first=%+v calls=%d", second, first, source.calls)
	}
	if contents, err := os.ReadFile(filepath.Join(second.WorkspacePath, "Dockerfile")); err != nil || string(contents) != "FROM scratch\n# one\n" {
		t.Fatalf("rematerialized Dockerfile=%q err=%v", contents, err)
	}
	var state string
	if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, first.ID).Scan(&state); err != nil || state != WorkspaceStateFailed {
		t.Fatalf("tampered state=%q err=%v", state, err)
	}
}

func TestMaterializeFailsClosedAndRematerializesLegacyGitHubReadyWithoutTreeDigest(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeArchive(t, "services: {}\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	first, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER releases_ready_insert_requires_workspace_tree_digest`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM releases WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,archive_sha256,workspace_path,workspace_state,workspace_size_bytes,configuration_revision_number)
		VALUES(?,?, 'ready','{}',datetime('now'),'github',7,?,'compose.yaml',?,?, 'ready',?,0)`, first.ID, app, snapshotSHA, first.ArchiveSHA256, m.workspaceRelative(app, first.ID), first.WorkspaceSizeBytes); err != nil {
		t.Fatal(err)
	}
	second, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || source.calls != 2 {
		t.Fatalf("legacy digest-less workspace reused=%+v first=%+v calls=%d", second, first, source.calls)
	}
	var state string
	if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, first.ID).Scan(&state); err != nil || state != WorkspaceStateFailed {
		t.Fatalf("legacy state=%q err=%v", state, err)
	}
	if _, err := os.Stat(first.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("legacy managed workspace retained: %v", err)
	}
}

func TestReadyReleaseCancellationPreservesGitHubWorkspaceAndReuse(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeArchive(t, "services: {}\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func(phase string) {
		t.Helper()
		var state string
		if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, release.ID).Scan(&state); err != nil || state != WorkspaceStateReady {
			t.Fatalf("%s state=%q err=%v", phase, state, err)
		}
		if _, err := os.Stat(release.WorkspacePath); err != nil {
			t.Fatalf("%s removed workspace: %v", phase, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ReadyRelease(canceled, app, release.ID); !IsCode(err, "canceled") {
		t.Fatalf("pre-canceled ready lookup error=%v", err)
	}
	assertUnchanged("pre-canceled lookup")

	canonicalHash := m.hashTree
	hashCalls := 0
	m.hashTree = func(context.Context, string) (string, error) {
		hashCalls++
		return "", context.Canceled
	}
	if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "canceled") || hashCalls != 1 {
		t.Fatalf("hash-canceled ready lookup error=%v calls=%d", err, hashCalls)
	}
	assertUnchanged("hash-canceled lookup")

	boundary, cancelBoundary := context.WithCancel(context.Background())
	m.hashTree = func(ctx context.Context, root string) (string, error) {
		digest, err := canonicalHash(ctx, root)
		cancelBoundary()
		return digest, err
	}
	if _, err := m.ReadyRelease(boundary, app, release.ID); !IsCode(err, "canceled") {
		t.Fatalf("post-hash canceled ready lookup error=%v", err)
	}
	assertUnchanged("post-hash canceled lookup")
	m.hashTree = canonicalHash

	if ready, err := m.ReadyRelease(context.Background(), app, release.ID); err != nil || ready.ID != release.ID {
		t.Fatalf("uncanceled ready lookup=%+v err=%v", ready, err)
	}
	if reused, err := m.Materialize(context.Background(), "owner", app); err != nil || reused.ID != release.ID || source.calls != 1 {
		t.Fatalf("uncanceled reuse=%+v err=%v archive calls=%d", reused, err, source.calls)
	}
}

func TestReadyReleaseTerminalizesAfterWorkspaceRemovalCancelsCaller(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeAndDockerfileArchive(t, "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile\n", "FROM scratch\n# one\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release.WorkspacePath, "Dockerfile"), []byte("FROM scratch\n# two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithCancel(context.Background())
	removeAll := m.fs.removeAll
	m.fs.removeAll = func(path string) error {
		err := removeAll(path)
		cancel()
		return err
	}
	if _, err := m.ReadyRelease(caller, app, release.ID); !IsCode(err, "invalid_source") {
		t.Fatalf("post-removal cancellation error=%v", err)
	}
	var state string
	var workspace sql.NullString
	if err := db.QueryRow(`SELECT workspace_state,workspace_path FROM releases WHERE id=?`, release.ID).Scan(&state, &workspace); err != nil || state != WorkspaceStateFailed || workspace.Valid {
		t.Fatalf("post-removal state=%q workspace=%#v err=%v", state, workspace, err)
	}
	if _, err := os.Stat(release.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("post-removal workspace still exists: %v", err)
	}
}

func TestReadyReleaseRemovalFailureLeavesFailedWorkspaceForRetention(t *testing.T) {
	db := snapshotDB(t)
	source := &fakeSources{archive: composeAndDockerfileArchive(t, "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile\n", "FROM scratch\n# one\n")}
	m, err := New(db, source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(release.WorkspacePath, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n# two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalRemoveAll := m.fs.removeAll
	removeCalls := 0
	removedPath := ""
	m.fs.removeAll = func(path string) error {
		removeCalls++
		removedPath = path
		return errors.New("injected workspace removal failure")
	}
	if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "internal_error") {
		t.Fatalf("removal failure error=%v", err)
	}
	if removeCalls != 1 {
		t.Fatalf("remove calls=%d want=1", removeCalls)
	}
	if removedPath != release.WorkspacePath {
		t.Fatalf("removed path=%q want=%q", removedPath, release.WorkspacePath)
	}
	var status, state, code, digest string
	var storedPath sql.NullString
	var size int64
	if err := db.QueryRow(`SELECT status,workspace_state,COALESCE(materialization_error_code,''),workspace_path,workspace_size_bytes,workspace_tree_sha256 FROM releases WHERE id=?`, release.ID).Scan(&status, &state, &code, &storedPath, &size, &digest); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || state != WorkspaceStateFailed || code != "invalid_source" || !storedPath.Valid || storedPath.String != m.workspaceRelative(app, release.ID) || size != release.WorkspaceSizeBytes || digest != release.WorkspaceTreeSHA256 {
		t.Fatalf("removal failure row status=%q state=%q code=%q path=%#v size=%d digest=%q", status, state, code, storedPath, size, digest)
	}
	if body, err := os.ReadFile(dockerfile); err != nil || string(body) != "FROM scratch\n# two\n" {
		t.Fatalf("workspace changed after failed removal: %q %v", body, err)
	}
	if source.calls != 1 {
		t.Fatalf("removal failure refetched archive calls=%d", source.calls)
	}
	m.fs.removeAll = originalRemoveAll
	m.retention = RetentionOptions{PerAppBytes: release.WorkspaceSizeBytes, GlobalBytes: release.WorkspaceSizeBytes}
	if err := m.enforceRetention(context.Background(), app, 1); err != nil {
		t.Fatalf("retention cleanup: %v", err)
	}
	if err := db.QueryRow(`SELECT status,workspace_state,workspace_path,workspace_size_bytes FROM releases WHERE id=?`, release.ID).Scan(&status, &state, &storedPath, &size); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || state != WorkspaceStatePruned || storedPath.Valid || size != 0 {
		t.Fatalf("retention cleanup row status=%q state=%q path=%#v size=%d", status, state, storedPath, size)
	}
	if _, err := os.Stat(release.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("retention cleanup workspace still exists: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("retention cleanup refetched archive calls=%d", source.calls)
	}
}

func TestReadyReleaseChangedRowDoesNotRemoveWorkspace(t *testing.T) {
	db := snapshotDB(t)
	m, err := New(db, &fakeSources{archive: composeArchive(t, "services: {}\n")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := m.hashTree
	invalidDigest := "a" + release.WorkspaceTreeSHA256[1:]
	if invalidDigest == release.WorkspaceTreeSHA256 {
		invalidDigest = "b" + release.WorkspaceTreeSHA256[1:]
	}
	m.hashTree = func(ctx context.Context, root string) (string, error) {
		if _, err := db.Exec(`UPDATE releases SET workspace_path='changed' WHERE id=?`, release.ID); err != nil {
			t.Fatal(err)
		}
		return invalidDigest, nil
	}
	if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "internal_error") {
		t.Fatalf("changed ready row error=%v", err)
	}
	m.hashTree = originalHash
	var state string
	if err := db.QueryRow(`SELECT workspace_state FROM releases WHERE id=?`, release.ID).Scan(&state); err != nil || state != WorkspaceStateReady {
		t.Fatalf("changed row state=%q err=%v", state, err)
	}
	if _, err := os.Stat(release.WorkspacePath); err != nil {
		t.Fatalf("changed row workspace removed: %v", err)
	}
}

func TestReadyReleaseCleanupFailureLeavesRecoverableFailedWorkspace(t *testing.T) {
	db := snapshotDB(t)
	m, err := New(db, &fakeSources{archive: composeAndDockerfileArchive(t, "services:\n  web:\n    build:\n      context: .\n      dockerfile: Dockerfile\n", "FROM scratch\n# one\n")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	release, err := m.Materialize(context.Background(), "owner", app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release.WorkspacePath, "Dockerfile"), []byte("FROM scratch\n# two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_release_workspace_cleanup BEFORE UPDATE OF workspace_path ON releases
		WHEN NEW.workspace_path IS NULL BEGIN SELECT RAISE(ABORT, 'cleanup blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadyRelease(context.Background(), app, release.ID); !IsCode(err, "internal_error") {
		t.Fatalf("cleanup failure error=%v", err)
	}
	var state string
	var storedPath sql.NullString
	var size int64
	if err := db.QueryRow(`SELECT workspace_state,workspace_path,workspace_size_bytes FROM releases WHERE id=?`, release.ID).Scan(&state, &storedPath, &size); err != nil || state != WorkspaceStateFailed || !storedPath.Valid || size <= 0 {
		t.Fatalf("cleanup failure state=%q path=%#v size=%d err=%v", state, storedPath, size, err)
	}
	if _, err := os.Stat(release.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("cleanup failure workspace still exists: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER block_release_workspace_cleanup`); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatalf("recover failed cleanup: %v", err)
	}
	if err := db.QueryRow(`SELECT workspace_state,workspace_path,workspace_size_bytes FROM releases WHERE id=?`, release.ID).Scan(&state, &storedPath, &size); err != nil || state != WorkspaceStateFailed || storedPath.Valid || size != 0 {
		t.Fatalf("recovered cleanup state=%q path=%#v size=%d err=%v", state, storedPath, size, err)
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
	return composeAndDockerfileArchive(t, compose, "")
}

func composeAndDockerfileArchive(t *testing.T, compose, dockerfile string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name, body string
		typ        byte
	}{{"repo/", "", tar.TypeDir}, {"repo/compose.yaml", compose, tar.TypeReg}}
	if dockerfile != "" {
		entries = append(entries, struct {
			name, body string
			typ        byte
		}{"repo/Dockerfile", dockerfile, tar.TypeReg})
	}
	for _, e := range entries {
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
