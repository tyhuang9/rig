package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const applicationDeploymentLifecycleBindingsMigration = "040_application_deployment_lifecycle_bindings.sql"

func TestApplicationDeploymentLifecycleBindingsMigrationFreshUpgradeAndReopen(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		assertApplicationDeploymentLifecycleBindingSchema(t, db)
		assertApplicationDeploymentLifecycleBindingForeignKeys(t, db)
	})

	t.Run("upgrade seeds only exact legacy jobs and preserves retained history", func(t *testing.T) {
		dataRoot := t.TempDir()
		db, err := sql.Open("sqlite", filepath.Join(dataRoot, "control.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			t.Fatal(err)
		}
		if err := migrateFS(db, migrationSetBefore(t, "040_")); err != nil {
			t.Fatal(err)
		}
		for _, appID := range []string{"seed-app", "delete-app", "positive-app", "other-app"} {
			insertDesiredLifecycleApplication(t, db, appID, "deployed")
		}
		if _, err := db.Exec(`INSERT INTO jobs(
			id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at
		) VALUES
			('seed-job','deploy','application','seed-app','succeeded','succeeded',3,'actor','{"configurationMode":"current"}','2026-10-18T01:02:03Z','2026-10-18T04:05:06Z'),
			('delete-job','deploy','application','delete-app','queued','queued',0,'actor','{}','2026-10-18T01:02:04Z','2026-10-18T01:02:04Z'),
			('positive-deploy','deploy','application','positive-app','queued','queued',0,'actor','{}','2026-10-18T01:02:05Z','2026-10-18T01:02:05Z'),
			('positive-start','start','application','positive-app','succeeded','succeeded',1,'actor','{}','2026-10-18T01:02:06Z','2026-10-18T01:02:06Z'),
			('wrong-type','start','application','other-app','queued','queued',0,'actor','{}','2026-10-18T01:02:07Z','2026-10-18T01:02:07Z'),
			('wrong-resource','deploy','machine','other-app','queued','queued',0,'actor','{}','2026-10-18T01:02:08Z','2026-10-18T01:02:08Z'),
			('missing-app','deploy','application','absent-app','queued','queued',0,'actor','{}','2026-10-18T01:02:09Z','2026-10-18T01:02:09Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_desired_lifecycle_transitions(
			app_id,revision_number,expected_revision,previous_state,desired_state,job_id,action,actor_id,created_at
		) VALUES('positive-app',1,0,NULL,'running','positive-start','start','actor','2026-10-18T01:03:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at)
			VALUES('seed-release','seed-app','ready','{"retained":true}','2026-10-18T01:04:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message)
			VALUES('seed-job',1,3,'2026-10-18T01:05:00Z','info','succeeded','retained_event','retained message')`); err != nil {
			t.Fatal(err)
		}

		body, err := migrations.ReadFile("migrations/" + applicationDeploymentLifecycleBindingsMigration)
		if err != nil {
			t.Fatal(err)
		}
		for pass := 1; pass <= 2; pass++ {
			if err := migrateFS(db, fstest.MapFS{"migrations/" + applicationDeploymentLifecycleBindingsMigration: {Data: body}}); err != nil {
				t.Fatalf("apply migration pass %d: %v", pass, err)
			}
		}
		assertApplicationDeploymentLifecycleBindingSchema(t, db)
		assertApplicationDeploymentLifecycleBindingForeignKeys(t, db)
		var seeded string
		if err := db.QueryRow(`SELECT group_concat(job_id,',') FROM (
			SELECT job_id FROM application_deployment_lifecycle_bindings ORDER BY job_id)`).Scan(&seeded); err != nil {
			t.Fatal(err)
		}
		if seeded != "delete-job,seed-job" {
			t.Fatalf("seeded bindings=%q", seeded)
		}
		var appID, mode, createdAt string
		var revision int64
		var authorizer sql.NullString
		if err := db.QueryRow(`SELECT app_id,desired_revision,mode,authorizing_lifecycle_job_id,created_at
			FROM application_deployment_lifecycle_bindings WHERE job_id='seed-job'`).Scan(
			&appID, &revision, &mode, &authorizer, &createdAt); err != nil {
			t.Fatal(err)
		}
		if appID != "seed-app" || revision != 0 || mode != "legacy_uninitialized" || authorizer.Valid || createdAt != "2026-10-18T01:02:03Z" {
			t.Fatalf("seeded identity app=%q revision=%d mode=%q authorizer=%#v created=%q", appID, revision, mode, authorizer, createdAt)
		}
		var input, jobUpdated, releaseMetadata, eventCode, eventMessage string
		var attempt int
		if err := db.QueryRow(`SELECT input_json,attempt,updated_at FROM jobs WHERE id='seed-job'`).Scan(&input, &attempt, &jobUpdated); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT metadata_json FROM releases WHERE id='seed-release'`).Scan(&releaseMetadata); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT code,message FROM job_events WHERE job_id='seed-job' AND sequence=1`).Scan(&eventCode, &eventMessage); err != nil {
			t.Fatal(err)
		}
		if input != `{"configurationMode":"current"}` || attempt != 3 || jobUpdated != "2026-10-18T04:05:06Z" ||
			releaseMetadata != `{"retained":true}` || eventCode != "retained_event" || eventMessage != "retained message" {
			t.Fatalf("retained data input=%q attempt=%d updated=%q release=%q event=%q/%q", input, attempt, jobUpdated, releaseMetadata, eventCode, eventMessage)
		}

		if _, err := db.Exec(`PRAGMA recursive_triggers = OFF`); err != nil {
			t.Fatal(err)
		}
		assertSQLRejected(t, db, "binding update", `UPDATE application_deployment_lifecycle_bindings SET created_at='changed' WHERE job_id='seed-job'`)
		assertSQLRejected(t, db, "binding delete", `DELETE FROM application_deployment_lifecycle_bindings WHERE job_id='seed-job'`)
		assertSQLRejected(t, db, "binding replace", `INSERT OR REPLACE INTO application_deployment_lifecycle_bindings
			SELECT * FROM application_deployment_lifecycle_bindings WHERE job_id='seed-job'`)
		if _, err := db.Exec(`PRAGMA recursive_triggers = ON`); err != nil {
			t.Fatal(err)
		}

		insertDesiredLifecycleApplication(t, db, "new-app", "draft")
		if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,input_json,created_at,updated_at)
			VALUES('new-job','deploy','application','new-app','queued','queued',0,'{}','2026-10-18T02:00:00Z','2026-10-18T02:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_deployment_lifecycle_bindings(
			job_id,app_id,desired_revision,mode,authorizing_lifecycle_job_id,created_at
		) VALUES('new-job','new-app',0,'legacy_uninitialized',NULL,'2026-10-18T02:00:00Z')`); err != nil {
			t.Fatalf("fresh exact legacy binding: %v", err)
		}
		assertSQLRejected(t, db, "foreign deployment app", `INSERT INTO application_deployment_lifecycle_bindings(
			job_id,app_id,desired_revision,mode,authorizing_lifecycle_job_id,created_at
		) VALUES('wrong-resource','new-app',0,'legacy_uninitialized',NULL,'2026-10-18T02:00:00Z')`)
		assertSQLRejected(t, db, "bare positive transition", `INSERT INTO application_deployment_lifecycle_bindings(
			job_id,app_id,desired_revision,mode,authorizing_lifecycle_job_id,created_at
		) VALUES('positive-deploy','positive-app',1,'running','positive-start','2026-10-18T02:00:00Z')`)

		if _, err := db.Exec(`DELETE FROM applications WHERE id='delete-app'`); err != nil {
			t.Fatalf("legacy app deletion: %v", err)
		}
		var retained int
		if err := db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings
			WHERE job_id='delete-job' AND app_id='delete-app'`).Scan(&retained); err != nil || retained != 1 {
			t.Fatalf("retained deleted-app binding=%d error=%v", retained, err)
		}
		assertNoForeignKeyViolations(t, db)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		assertApplicationDeploymentLifecycleBindingSchema(t, reopened)
		if err := reopened.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings`).Scan(&retained); err != nil || retained != 3 {
			t.Fatalf("reopened bindings=%d error=%v", retained, err)
		}
		assertNoForeignKeyViolations(t, reopened)
	})
}

func TestApplicationDeploymentLifecycleBindingsMigrationIsByteMirrored(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + applicationDeploymentLifecycleBindingsMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", applicationDeploymentLifecycleBindingsMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("application deployment lifecycle bindings migration is not byte mirrored")
	}
}

func assertApplicationDeploymentLifecycleBindingSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name='application_deployment_lifecycle_bindings'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("binding table count=%d error=%v", count, err)
	}
	for _, trigger := range []string{
		"application_deployment_lifecycle_binding_valid_insert",
		"application_deployment_lifecycle_binding_immutable",
		"application_deployment_lifecycle_binding_retain",
	} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("binding trigger %s count=%d error=%v", trigger, count, err)
		}
	}
}

func assertApplicationDeploymentLifecycleBindingForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list(application_deployment_lifecycle_bindings)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	foreignKeys := map[string]string{}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		foreignKeys[from] = table
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if foreignKeys["job_id"] != "jobs" || foreignKeys["authorizing_lifecycle_job_id"] != "application_lifecycle_operations" {
		t.Fatalf("binding foreign keys=%#v", foreignKeys)
	}
	if _, exists := foreignKeys["app_id"]; exists {
		t.Fatalf("app_id unexpectedly restricts application deletion: %#v", foreignKeys)
	}
}
