package database

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const applicationDesiredLifecycleMigration = "038_application_desired_lifecycle.sql"

func TestApplicationDesiredLifecycleMigrationFreshUpgradeAndReopen(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		insertDesiredLifecycleApplication(t, db, "fresh-app", "running")
		assertUninitializedDesiredLifecycleHead(t, db, "fresh-app")
	})

	t.Run("upgrade and reopen preserves unrelated state", func(t *testing.T) {
		dataRoot := t.TempDir()
		path := filepath.Join(dataRoot, "control.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			t.Fatal(err)
		}
		if err := migrateFS(db, migrationSetBefore(t, "038_")); err != nil {
			t.Fatalf("migrate through 037: %v", err)
		}
		insertDesiredLifecycleApplication(t, db, "upgrade-app", "deployed")
		if _, err := db.Exec(`INSERT INTO jobs(
			id,type,resource_type,resource_id,status,phase,requested_by,input_json,
			created_at,updated_at
		) VALUES('upgrade-job','start','application','upgrade-app','succeeded','finalize',
			'upgrade-actor','{}','2026-10-18T01:00:00Z','2026-10-18T01:01:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO job_events(
			job_id,sequence,attempt,timestamp,level,phase,code,message,metadata_json
		) VALUES('upgrade-job',1,0,'2026-10-18T01:00:00Z','info','queued','job_queued','Job queued','{}')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at)
			VALUES('upgrade-release','upgrade-app','ready','{"preserved":true}','2026-10-18T01:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		before := desiredLifecycleUnrelatedSnapshot(t, db, "upgrade-app")

		body, err := migrations.ReadFile("migrations/" + applicationDesiredLifecycleMigration)
		if err != nil {
			t.Fatal(err)
		}
		for pass := 1; pass <= 2; pass++ {
			if err := migrateFS(db, fstest.MapFS{
				"migrations/" + applicationDesiredLifecycleMigration: {Data: body},
			}); err != nil {
				t.Fatalf("apply 038 pass %d: %v", pass, err)
			}
		}
		assertUninitializedDesiredLifecycleHead(t, db, "upgrade-app")
		if after := desiredLifecycleUnrelatedSnapshot(t, db, "upgrade-app"); after != before {
			t.Fatalf("migration changed unrelated state:\nbefore=%q\nafter=%q", before, after)
		}
		insertDesiredLifecycleApplication(t, db, "future-app", "draft")
		assertUninitializedDesiredLifecycleHead(t, db, "future-app")
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		reopened, err := Open(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		assertUninitializedDesiredLifecycleHead(t, reopened, "upgrade-app")
		assertUninitializedDesiredLifecycleHead(t, reopened, "future-app")
		if after := desiredLifecycleUnrelatedSnapshot(t, reopened, "upgrade-app"); after != before {
			t.Fatalf("reopen changed unrelated state:\nbefore=%q\nafter=%q", before, after)
		}
		assertNoForeignKeyViolations(t, reopened)
	})
}

func TestApplicationDesiredLifecycleMigrationEnforcesAtomicHistoryAndHead(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertDesiredLifecycleApplication(t, db, "guard-app", "deployed")
	for _, job := range []struct {
		id, action string
	}{
		{"guard-start", "start"},
		{"guard-stop", "stop"},
		{"guard-restart", "restart"},
	} {
		if _, err := db.Exec(`INSERT INTO jobs(
			id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at
		) VALUES(?,?,'application','guard-app','succeeded','finalize','guard-actor',
			'2026-10-18T01:00:00Z','2026-10-18T01:00:00Z')`, job.id, job.action); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO application_desired_lifecycle_transitions(
		app_id,revision_number,expected_revision,previous_state,desired_state,
		job_id,action,actor_id,created_at
	) VALUES('guard-app',1,0,NULL,'running','guard-start','start','guard-actor',
		'2026-10-18T01:02:00Z')`); err != nil {
		t.Fatal(err)
	}
	assertDesiredLifecycleHead(t, db, "guard-app", "running", 1)

	assertSQLRejectedWith(t, db, "direct head delete", "application desired lifecycle head is retained",
		`DELETE FROM application_desired_lifecycle_heads WHERE app_id='guard-app'`)
	assertSQLRejectedWith(t, db, "head replace reset", "application desired lifecycle head must be uninitialized",
		`INSERT OR REPLACE INTO application_desired_lifecycle_heads(app_id)
		 VALUES('guard-app')`)
	assertSQLRejectedWith(t, db, "operation replace", "invalid application desired lifecycle transition",
		`INSERT OR REPLACE INTO application_desired_lifecycle_transitions(
			app_id,revision_number,expected_revision,previous_state,desired_state,
			job_id,action,actor_id,created_at
		) VALUES('guard-app',2,1,'running','running','guard-start','start','guard-actor',
			'2026-10-18T01:03:00Z')`)
	assertDesiredLifecycleHead(t, db, "guard-app", "running", 1)
	var retainedRevision int64
	if err := db.QueryRow(`SELECT revision_number FROM application_desired_lifecycle_transitions
		WHERE job_id='guard-start' AND action='start'`).Scan(&retainedRevision); err != nil || retainedRevision != 1 {
		t.Fatalf("retained operation revision=%d error=%v", retainedRevision, err)
	}

	assertSQLRejectedWith(t, db, "history update", "transitions are immutable",
		`UPDATE application_desired_lifecycle_transitions SET actor_id='changed'
		 WHERE job_id='guard-start' AND action='start'`)
	assertSQLRejectedWith(t, db, "history delete", "transitions are retained",
		`DELETE FROM application_desired_lifecycle_transitions
		 WHERE job_id='guard-start' AND action='start'`)
	assertSQLRejectedWith(t, db, "direct head advance", "invalid application desired lifecycle head advance",
		`UPDATE application_desired_lifecycle_heads
		 SET desired_state='stopped',revision_number=2,updated_at='2026-10-18T01:03:00Z'
		 WHERE app_id='guard-app'`)
	assertSQLRejected(t, db, "malformed action target", `
		INSERT INTO application_desired_lifecycle_transitions(
			app_id,revision_number,expected_revision,previous_state,desired_state,
			job_id,action,actor_id,created_at
		) VALUES('guard-app',2,1,'running','running','guard-stop','stop','guard-actor',
			'2026-10-18T01:03:00Z')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, statementErr := tx.Exec(`INSERT INTO application_desired_lifecycle_transitions(
		app_id,revision_number,expected_revision,previous_state,desired_state,
		job_id,action,actor_id,created_at
	) VALUES('guard-app',3,2,'running','stopped','guard-stop','stop','guard-actor',
		'2026-10-18T01:04:00Z')`)
	if statementErr == nil {
		t.Fatal("stale guarded transition succeeded")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("caller could not commit after caught statement failure: %v", err)
	}
	assertDesiredLifecycleHead(t, db, "guard-app", "running", 1)
	var history int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_transitions
		WHERE app_id='guard-app'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history after caught failure=%d error=%v", history, err)
	}
	if _, err := db.Exec(`INSERT INTO application_desired_lifecycle_transitions(
		app_id,revision_number,expected_revision,previous_state,desired_state,
		job_id,action,actor_id,created_at
	) VALUES('guard-app',2,1,'running','stopped','guard-stop','stop','guard-actor',
		'2026-10-18T01:05:00Z')`); err != nil {
		t.Fatalf("second retained transition: %v", err)
	}
	assertSQLRejectedWith(t, db, "primary key replace", "invalid application desired lifecycle transition",
		`INSERT OR REPLACE INTO application_desired_lifecycle_transitions(
			app_id,revision_number,expected_revision,previous_state,desired_state,
			job_id,action,actor_id,created_at
		) VALUES('guard-app',1,0,NULL,'running','guard-restart','restart','guard-actor',
			'2026-10-18T01:06:00Z')`)
	assertDesiredLifecycleHead(t, db, "guard-app", "stopped", 2)
	var retainedHistory string
	if err := db.QueryRow(`SELECT group_concat(revision_number || ':' || job_id, ',')
		FROM (SELECT revision_number,job_id FROM application_desired_lifecycle_transitions
		      WHERE app_id='guard-app' ORDER BY revision_number)`).Scan(&retainedHistory); err != nil || retainedHistory != "1:guard-start,2:guard-stop" {
		t.Fatalf("retained primary-key history=%q error=%v", retainedHistory, err)
	}
	if _, err := db.Exec(`DELETE FROM applications WHERE id='guard-app'`); err == nil {
		t.Fatal("application deletion cascaded retained lifecycle history")
	}
	insertDesiredLifecycleApplication(t, db, "uninitialized-delete", "draft")
	if _, err := db.Exec(`DELETE FROM applications WHERE id='uninitialized-delete'`); err != nil {
		t.Fatalf("uninitialized application deletion lost compatibility: %v", err)
	}
	var deletedHeads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_desired_lifecycle_heads
		WHERE app_id='uninitialized-delete'`).Scan(&deletedHeads); err != nil || deletedHeads != 0 {
		t.Fatalf("deleted application lifecycle heads=%d error=%v", deletedHeads, err)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestApplicationDesiredLifecycleMigrationIsByteMirrored(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + applicationDesiredLifecycleMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", applicationDesiredLifecycleMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("application desired lifecycle migration is not byte mirrored")
	}
}

func insertDesiredLifecycleApplication(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,?,'2026-10-18T01:00:00Z','2026-10-18T01:00:00Z')`, id, id, id, status); err != nil {
		t.Fatal(err)
	}
}

func assertUninitializedDesiredLifecycleHead(t *testing.T, db *sql.DB, appID string) {
	t.Helper()
	var state, updated sql.NullString
	var revision int64
	if err := db.QueryRow(`SELECT desired_state,revision_number,updated_at
		FROM application_desired_lifecycle_heads WHERE app_id=?`, appID).Scan(&state, &revision, &updated); err != nil {
		t.Fatal(err)
	}
	if state.Valid || revision != 0 || updated.Valid {
		t.Fatalf("%s lifecycle head state=%#v revision=%d updated=%#v", appID, state, revision, updated)
	}
}

func assertDesiredLifecycleHead(t *testing.T, db *sql.DB, appID, state string, revision int64) {
	t.Helper()
	var gotState string
	var gotRevision int64
	if err := db.QueryRow(`SELECT desired_state,revision_number
		FROM application_desired_lifecycle_heads WHERE app_id=?`, appID).Scan(&gotState, &gotRevision); err != nil {
		t.Fatal(err)
	}
	if gotState != state || gotRevision != revision {
		t.Fatalf("%s lifecycle head state=%q revision=%d", appID, gotState, gotRevision)
	}
}

func desiredLifecycleUnrelatedSnapshot(t *testing.T, db *sql.DB, appID string) string {
	t.Helper()
	var snapshot string
	err := db.QueryRow(`SELECT json_object(
		'appStatus',(SELECT status FROM applications WHERE id=?),
		'job',(SELECT type || ':' || status || ':' || phase || ':' || input_json
		       FROM jobs WHERE id='upgrade-job'),
		'events',(SELECT COUNT(*) || ':' || MIN(message) FROM job_events WHERE job_id='upgrade-job'),
		'release',(SELECT status || ':' || metadata_json FROM releases WHERE id='upgrade-release'),
		'runtimeHead',(SELECT printf('%s:%d',COALESCE(deployment_id,''),generation)
		               FROM generated_runtime_active_heads WHERE app_id=?)
	)`, appID, appID).Scan(&snapshot)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return snapshot
}
