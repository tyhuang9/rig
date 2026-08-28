package releasesnapshot

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

type retentionFixture struct {
	db       *sql.DB
	root     string
	appA     string
	appB     string
	actor    string
	material *Materializer
}

func newRetentionFixture(t *testing.T, perApp, global int64) retentionFixture {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := retentionFixture{db: db, root: root, appA: uuid.NewString(), appB: uuid.NewString(), actor: uuid.NewString()}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'owner','hash',?,?)`, fixture.actor, now, now); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{fixture.appA, fixture.appB} {
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, app, app, app, now, now); err != nil {
			t.Fatal(err)
		}
	}
	fixture.material, err = New(db, &fakeSources{}, root, RetentionOptions{PerAppBytes: perApp, GlobalBytes: global})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f retentionFixture) addWorkspace(t *testing.T, appID, state string, size int, created time.Time) retainedWorkspace {
	t.Helper()
	id, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	path, err := f.material.workspacePath(appID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "content"), bytes.Repeat([]byte{'x'}, size), 0o600); err != nil {
		t.Fatal(err)
	}
	stored := f.material.workspaceRelative(appID, id)
	status := "ready"
	if state == WorkspaceStateFailed {
		status = "failed"
	}
	if _, err := f.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,workspace_path,workspace_state,workspace_size_bytes) VALUES(?,?,?,?,?,'local',?,?,?)`, id, appID, status, `{"retained":"yes"}`, created.UTC().Format(time.RFC3339Nano), stored, state, size); err != nil {
		t.Fatal(err)
	}
	return retainedWorkspace{id: id, appID: appID, storedPath: stored, state: state, size: int64(size), createdAt: created.UTC().Format(time.RFC3339Nano)}
}

func workspaceState(t *testing.T, db *sql.DB, id string) (string, sql.NullString, int64) {
	t.Helper()
	var state string
	var path sql.NullString
	var size int64
	if err := db.QueryRow(`SELECT workspace_state,workspace_path,workspace_size_bytes FROM releases WHERE id=?`, id).Scan(&state, &path, &size); err != nil {
		t.Fatal(err)
	}
	return state, path, size
}

func TestRetentionPrunesStrictlyOldestAndPreservesHistory(t *testing.T) {
	t.Run("per app", func(t *testing.T) {
		fixture := newRetentionFixture(t, 8, 100)
		base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		oldest := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 4, base)
		newer := fixture.addWorkspace(t, fixture.appA, WorkspaceStateFailed, 4, base.Add(time.Hour))
		if err := fixture.material.enforceRetention(context.Background(), fixture.appA, 3); err != nil {
			t.Fatal(err)
		}
		state, path, size := workspaceState(t, fixture.db, oldest.id)
		if state != WorkspaceStatePruned || path.Valid || size != 0 {
			t.Fatalf("oldest state=%q path=%#v size=%d", state, path, size)
		}
		if _, err := os.Stat(filepath.Join(fixture.root, oldest.storedPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("oldest workspace still present: %v", err)
		}
		state, path, size = workspaceState(t, fixture.db, newer.id)
		if state != WorkspaceStateFailed || !path.Valid || size != 4 {
			t.Fatalf("newer state=%q path=%#v size=%d", state, path, size)
		}
		var rows, metadata int
		if err := fixture.db.QueryRow(`SELECT COUNT(*),SUM(metadata_json='{"retained":"yes"}') FROM releases WHERE id IN (?,?)`, oldest.id, newer.id).Scan(&rows, &metadata); err != nil || rows != 2 || metadata != 2 {
			t.Fatalf("rows=%d metadata=%d err=%v", rows, metadata, err)
		}
	})

	t.Run("global", func(t *testing.T) {
		fixture := newRetentionFixture(t, 8, 8)
		base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		oldest := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 4, base)
		newer := fixture.addWorkspace(t, fixture.appB, WorkspaceStateReady, 4, base.Add(time.Hour))
		if err := fixture.material.enforceRetention(context.Background(), fixture.appB, 3); err != nil {
			t.Fatal(err)
		}
		if state, _, _ := workspaceState(t, fixture.db, oldest.id); state != WorkspaceStatePruned {
			t.Fatalf("global oldest state=%q", state)
		}
		if state, _, _ := workspaceState(t, fixture.db, newer.id); state != WorkspaceStateReady {
			t.Fatalf("global newer state=%q", state)
		}
	})
}

func TestProtectedReleaseSetCoversDeploymentAndJobLifecycles(t *testing.T) {
	fixture := newRetentionFixture(t, 1<<20, 1<<20)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	active := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 1, base)
	latest := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 1, base.Add(time.Hour))
	succeededOld := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 1, base.Add(2*time.Hour))
	succeededTwo := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 1, base.Add(3*time.Hour))
	succeededOne := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 1, base.Add(4*time.Hour))
	jobRelease := fixture.addWorkspace(t, fixture.appB, WorkspaceStateReady, 1, base.Add(5*time.Hour))
	unprotected := fixture.addWorkspace(t, fixture.appB, WorkspaceStateReady, 1, base.Add(6*time.Hour))

	insertDeployment := func(release retainedWorkspace, status string, started, finished time.Time) {
		t.Helper()
		var finish any
		if !finished.IsZero() {
			finish = finished.UTC().Format(time.RFC3339Nano)
		}
		if _, err := fixture.db.Exec(`INSERT INTO deployments(id,app_id,release_id,status,configuration_mode,provenance_initialized,started_at,finished_at) VALUES(?,?,?,?,'current',1,?,?)`, uuid.NewString(), release.appID, release.id, status, started.UTC().Format(time.RFC3339Nano), finish); err != nil {
			t.Fatal(err)
		}
	}
	insertDeployment(active, "needs_attention", base, time.Time{})
	insertDeployment(succeededOld, "succeeded", base.Add(-4*time.Hour), base.Add(time.Hour))
	insertDeployment(succeededTwo, "succeeded", base.Add(-3*time.Hour), base.Add(2*time.Hour))
	insertDeployment(succeededOne, "succeeded", base.Add(-2*time.Hour), base.Add(3*time.Hour))
	insertDeployment(latest, "failed", base.Add(10*time.Hour), base.Add(10*time.Hour))
	input := `{"releaseId":"` + jobRelease.id + `","configurationMode":"current"}`
	if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,?,?,?)`, uuid.NewString(), fixture.appB, fixture.actor, input, base.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	tx, err := fixture.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := protectedReleaseIDs(context.Background(), tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	for name, release := range map[string]retainedWorkspace{"active": active, "latest": latest, "succeeded one": succeededOne, "succeeded two": succeededTwo, "job": jobRelease} {
		if !protected[release.id] {
			t.Errorf("%s release not protected", name)
		}
	}
	if protected[succeededOld.id] || protected[unprotected.id] {
		t.Fatalf("unexpected protected set: oldSucceeded=%v unrelated=%v", protected[succeededOld.id], protected[unprotected.id])
	}
}

func TestRetentionFailsClosedWhenOnlyProtectedOrUnknownBytesRemain(t *testing.T) {
	t.Run("latest deployment", func(t *testing.T) {
		fixture := newRetentionFixture(t, 8, 8)
		release := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 8, time.Now())
		if _, err := fixture.db.Exec(`INSERT INTO deployments(id,app_id,release_id,status,configuration_mode,provenance_initialized,started_at,finished_at) VALUES(?,?,?,'failed','current',1,datetime('now'),datetime('now'))`, uuid.NewString(), fixture.appA, release.id); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(fixture.root, release.storedPath)); err != nil {
			t.Fatal(err)
		}
		if err := fixture.material.enforceRetention(context.Background(), fixture.appA, 1); !IsCode(err, "source_storage_full") {
			t.Fatalf("error=%v", err)
		}
		if state, _, _ := workspaceState(t, fixture.db, release.id); state != WorkspaceStateReady {
			t.Fatalf("protected release state=%q", state)
		}
	})

	t.Run("unknown legacy size", func(t *testing.T) {
		fixture := newRetentionFixture(t, 8, 8)
		release := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 8, time.Now())
		if _, err := fixture.db.Exec(`UPDATE releases SET workspace_size_bytes=NULL,workspace_path='../outside' WHERE id=?`, release.id); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(fixture.root, "outside")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.material.enforceRetention(context.Background(), fixture.appA, 1); !IsCode(err, "source_storage_full") {
			t.Fatalf("error=%v", err)
		}
		if body, err := os.ReadFile(outside); err != nil || string(body) != "keep" {
			t.Fatalf("outside file changed: %q %v", body, err)
		}
	})
}

func TestRetentionAccountsForMissingExactManagedWorkspaceAsPruned(t *testing.T) {
	fixture := newRetentionFixture(t, 8, 8)
	release := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 8, time.Now())
	if err := os.RemoveAll(filepath.Join(fixture.root, release.storedPath)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.material.enforceRetention(context.Background(), fixture.appA, 8); err != nil {
		t.Fatalf("missing exact workspace blocked quota admission: %v", err)
	}
	state, path, size := workspaceState(t, fixture.db, release.id)
	if state != WorkspaceStatePruned || path.Valid || size != 0 {
		t.Fatalf("state=%q path=%#v size=%d", state, path, size)
	}
	var metadata string
	if err := fixture.db.QueryRow(`SELECT metadata_json FROM releases WHERE id=?`, release.id).Scan(&metadata); err != nil || metadata != `{"retained":"yes"}` {
		t.Fatalf("metadata=%q err=%v", metadata, err)
	}
}

func TestRetentionPruningRecoveryAndFailureAreDurable(t *testing.T) {
	t.Run("recover present and already missing", func(t *testing.T) {
		fixture := newRetentionFixture(t, 100, 100)
		present := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 3, time.Now())
		missing := fixture.addWorkspace(t, fixture.appA, WorkspaceStateFailed, 2, time.Now().Add(time.Second))
		if err := os.RemoveAll(filepath.Join(fixture.root, missing.storedPath)); err != nil {
			t.Fatal(err)
		}
		for _, release := range []retainedWorkspace{present, missing} {
			if _, err := fixture.db.Exec(`UPDATE releases SET workspace_state='pruning',workspace_prune_from_state=? WHERE id=?`, release.state, release.id); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.material.Recover(); err != nil {
			t.Fatal(err)
		}
		for _, release := range []retainedWorkspace{present, missing} {
			if state, path, size := workspaceState(t, fixture.db, release.id); state != WorkspaceStatePruned || path.Valid || size != 0 {
				t.Fatalf("release %s state=%q path=%#v size=%d", release.id, state, path, size)
			}
		}
	})

	t.Run("remove failure remains recoverable", func(t *testing.T) {
		fixture := newRetentionFixture(t, 100, 100)
		release := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 3, time.Now())
		marked, err := fixture.material.markPruning(context.Background(), release)
		if err != nil || !marked {
			t.Fatalf("marked=%v err=%v", marked, err)
		}
		originalRemove := fixture.material.fs.removeAll
		fixture.material.fs.removeAll = func(string) error { return errors.New("injected remove failure") }
		if err := fixture.material.removePruningWorkspace(context.Background(), release); err == nil {
			t.Fatal("expected removal failure")
		}
		if state, _, _ := workspaceState(t, fixture.db, release.id); state != WorkspaceStatePruning {
			t.Fatalf("state after removal failure=%q", state)
		}
		fixture.material.fs.removeAll = originalRemove
		if err := fixture.material.Recover(); err != nil {
			t.Fatal(err)
		}
		if state, _, _ := workspaceState(t, fixture.db, release.id); state != WorkspaceStatePruned {
			t.Fatalf("recovered state=%q", state)
		}
	})

	t.Run("unsafe interrupted state never removes outside", func(t *testing.T) {
		fixture := newRetentionFixture(t, 100, 100)
		release := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 3, time.Now())
		outside := filepath.Join(fixture.root, "outside")
		if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE releases SET workspace_state='pruning',workspace_prune_from_state='ready',workspace_path='../outside' WHERE id=?`, release.id); err != nil {
			t.Fatal(err)
		}
		if err := fixture.material.Recover(); err == nil {
			t.Fatal("unsafe recovery succeeded")
		}
		if body, err := os.ReadFile(outside); err != nil || string(body) != "keep" {
			t.Fatalf("outside file changed: %q %v", body, err)
		}
	})
}

func TestRetentionBackfillsLegacySizesAndSerializesAdmission(t *testing.T) {
	fixture := newRetentionFixture(t, 100, 100)
	release := fixture.addWorkspace(t, fixture.appA, WorkspaceStateReady, 7, time.Now())
	if _, err := fixture.db.Exec(`UPDATE releases SET workspace_size_bytes=NULL WHERE id=?`, release.id); err != nil {
		t.Fatal(err)
	}
	if err := fixture.material.enforceRetention(context.Background(), fixture.appA, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, size := workspaceState(t, fixture.db, release.id); size != 7 {
		t.Fatalf("backfilled size=%d", size)
	}

	var active, maximum atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := fixture.material.admitRetention(context.Background(), fixture.appA, 0, func() error {
				current := active.Add(1)
				for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
				}
				time.Sleep(25 * time.Millisecond)
				active.Add(-1)
				return nil
			}); err != nil {
				t.Errorf("admission: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent installs=%d", maximum.Load())
	}
}

func TestMaterializersPersistExactWorkspaceSize(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		db := snapshotDB(t)
		root := t.TempDir()
		materializer, err := New(db, &fakeSources{archive: composeArchive(t, "services: {}\n")}, root)
		if err != nil {
			t.Fatal(err)
		}
		release, err := materializer.Materialize(context.Background(), "owner", "11111111-1111-1111-1111-111111111111")
		if err != nil {
			t.Fatal(err)
		}
		want, err := logicalTreeSize(release.WorkspacePath)
		if err != nil {
			t.Fatal(err)
		}
		var stored int64
		if err := db.QueryRow(`SELECT workspace_size_bytes FROM releases WHERE id=?`, release.ID).Scan(&stored); err != nil || release.WorkspaceSizeBytes != want || stored != want {
			t.Fatalf("release=%d stored=%d want=%d err=%v", release.WorkspaceSizeBytes, stored, want, err)
		}
	})

	t.Run("local", func(t *testing.T) {
		materializer, db, _, appID, _, source := localMaterializerFixture(t, false)
		release, err := materializer.MaterializeLocal(context.Background(), appID, source)
		if err != nil {
			t.Fatal(err)
		}
		want, err := logicalTreeSize(release.WorkspacePath)
		if err != nil {
			t.Fatal(err)
		}
		var stored int64
		if err := db.QueryRow(`SELECT workspace_size_bytes FROM releases WHERE id=?`, release.ID).Scan(&stored); err != nil || release.WorkspaceSizeBytes != want || stored != want {
			t.Fatalf("release=%d stored=%d want=%d err=%v", release.WorkspaceSizeBytes, stored, want, err)
		}
	})
}
