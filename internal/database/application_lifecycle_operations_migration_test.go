package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const applicationLifecycleOperationsMigration = "039_application_lifecycle_operations.sql"

func TestApplicationLifecycleOperationsMigrationFreshUpgradeAndReopen(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		assertLifecycleOperationSchema(t, db)
	})

	t.Run("upgrade preserves prior state without fabricating authority", func(t *testing.T) {
		dataRoot := t.TempDir()
		db, err := sql.Open("sqlite", filepath.Join(dataRoot, "control.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			t.Fatal(err)
		}
		if err := migrateFS(db, migrationSetBefore(t, "039_")); err != nil {
			t.Fatal(err)
		}
		insertDesiredLifecycleApplication(t, db, "operation-upgrade-app", "deployed")
		if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
			VALUES('operation-upgrade-job','start','application','operation-upgrade-app','succeeded','done','actor','{}','2026-10-18T01:00:00Z','2026-10-18T01:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		body, err := migrations.ReadFile("migrations/" + applicationLifecycleOperationsMigration)
		if err != nil {
			t.Fatal(err)
		}
		for pass := 1; pass <= 2; pass++ {
			if err := migrateFS(db, fstest.MapFS{"migrations/" + applicationLifecycleOperationsMigration: {Data: body}}); err != nil {
				t.Fatalf("apply migration pass %d: %v", pass, err)
			}
		}
		assertLifecycleOperationSchema(t, db)
		var applications, jobs, transitions, operations, intents, receipts int
		if err := db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM applications WHERE id='operation-upgrade-app'),
			(SELECT COUNT(*) FROM jobs WHERE id='operation-upgrade-job'),
			(SELECT COUNT(*) FROM application_desired_lifecycle_transitions WHERE app_id='operation-upgrade-app'),
			(SELECT COUNT(*) FROM application_lifecycle_operations),
			(SELECT COUNT(*) FROM application_lifecycle_effect_intents),
			(SELECT COUNT(*) FROM application_lifecycle_effect_receipts)`).Scan(&applications, &jobs, &transitions, &operations, &intents, &receipts); err != nil {
			t.Fatal(err)
		}
		if applications != 1 || jobs != 1 || transitions != 0 || operations != 0 || intents != 0 || receipts != 0 {
			t.Fatalf("upgrade counts applications=%d jobs=%d transitions=%d operations=%d intents=%d receipts=%d", applications, jobs, transitions, operations, intents, receipts)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		assertLifecycleOperationSchema(t, reopened)
		assertNoForeignKeyViolations(t, reopened)
	})
}

func TestApplicationLifecycleOperationsMigrationIsByteMirrored(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + applicationLifecycleOperationsMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", applicationLifecycleOperationsMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("application lifecycle operations migration is not byte mirrored")
	}
}

func assertLifecycleOperationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{
		"application_lifecycle_operations", "application_lifecycle_operation_components",
		"application_lifecycle_effect_intents", "application_lifecycle_effect_receipts",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("schema table %s count=%d error=%v", name, count, err)
		}
	}
}
