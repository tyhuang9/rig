package appaccess

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// These SQL/history fixtures describe serving state written before migration
// 042 introduced receipt-bound activation. Build the real 001–041 schema once,
// copy only that empty schema per test, and migrate each populated history before
// exercising current behavior. Never downgrade a current database or remove its
// activation guards to construct a positive fixture.
var historicalGatewayTemplate = sync.OnceValues(func() ([]byte, error) {
	root, err := os.MkdirTemp("", "rig-appaccess-schema-041-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	db, err := openHistoricalGatewayConnection(root)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	migrationDir := filepath.Join("..", "database", "migrations")
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		return nil, err
	}
	applied := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "042_" {
			continue
		}
		applied++
		if !strings.HasPrefix(entry.Name(), fmt.Sprintf("%03d_", applied)) {
			return nil, fmt.Errorf("historical migration gap at %s", entry.Name())
		}
		body, err := os.ReadFile(filepath.Join(migrationDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(string(body)); err == nil {
			_, err = tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("historical migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	if applied != 41 {
		return nil, fmt.Errorf("historical migration discovery=%d want=41", applied)
	}
	var busy, frames, checkpointed int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil {
		return nil, err
	}
	if busy != 0 {
		return nil, fmt.Errorf("historical template checkpoint busy=%d", busy)
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(filepath.Join(root, "control.db"+suffix)); !os.IsNotExist(err) {
			return nil, fmt.Errorf("historical template sidecar %s remains: %v", suffix, err)
		}
	}
	return os.ReadFile(filepath.Join(root, "control.db"))
})

func openHistoricalGatewayConnection(root string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", filepath.Join(root, "control.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func openHistoricalGatewayDatabase(t *testing.T, root string) *sql.DB {
	t.Helper()
	template, err := historicalGatewayTemplate()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("historical fixture requires an empty private root: entries=%d error=%v", len(entries), err)
	}
	file, err := os.OpenFile(filepath.Join(root, "control.db"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	written, writeErr := file.Write(template)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || written != len(template) || written == 0 {
		t.Fatalf("copy historical schema: written=%d want=%d write=%v close=%v", written, len(template), writeErr, closeErr)
	}
	db, err := openHistoricalGatewayConnection(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertHistoricalGatewaySchema(t, db)
	return db
}

func historicalAppAccessDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openHistoricalGatewayDatabase(t, t.TempDir())
	addUsers(t, db)
	return db
}

func assertHistoricalGatewaySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var count, activationTables int
	var latest string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(version) FROM schema_migrations`).Scan(&count, &latest); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='generated_route_activations'`).Scan(&activationTables); err != nil {
		t.Fatal(err)
	}
	if count != 41 || latest != "041_generated_candidate_intents.sql" || activationTables != 0 {
		t.Fatalf("historical runtime write requires exact schema041: count=%d latest=%q activationTables=%d", count, latest, activationTables)
	}
}

func upgradeHistoricalGatewayDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	assertHistoricalGatewaySchema(t, db)
	if err := database.Migrate(db); err != nil {
		t.Fatalf("upgrade historical gateway fixture: %v", err)
	}
	var receipts, receiptHeads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads WHERE activation_receipt_id IS NOT NULL`).Scan(&receiptHeads); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || receiptHeads != 0 {
		t.Fatalf("historical upgrade minted activation authority: receipts=%d heads=%d", receipts, receiptHeads)
	}
}

func historicalRuntimeHeadForRebindTest(t *testing.T, db *sql.DB, appID string) generatedruntimestate.ActiveHead {
	t.Helper()
	assertHistoricalGatewaySchema(t, db)
	head := generatedruntimestate.ActiveHead{AppID: appID}
	var stamp string
	if err := db.QueryRow(`SELECT deployment_id,release_id,slot,generation,updated_at
		FROM generated_runtime_active_heads WHERE app_id=?`, appID).
		Scan(&head.DeploymentID, &head.ReleaseID, &head.Slot, &head.Generation, &stamp); err != nil {
		t.Fatal(err)
	}
	var err error
	head.UpdatedAt, err = parseTime(stamp)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// Advance only historical SQL rows through their original constrained phases.
// This helper cannot run on a current database and creates no current authority.
func activateHistoricalGatewayComponent(t *testing.T, db *sql.DB, head generatedruntimestate.ActiveHead,
	artifactID, containerName, containerID string,
) generatedruntimestate.ActiveHead {
	t.Helper()
	assertHistoricalGatewaySchema(t, db)
	stamp := formatTime(testNow)
	for index, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO generated_runtime_components(deployment_id,component_name,slot,state,created_at,updated_at)
			VALUES(?,'api',?,'pending',?,?)`, []any{head.DeploymentID, head.Slot, stamp, stamp}},
		{`UPDATE generated_runtime_components SET state='image_ready',image_artifact_id=? WHERE deployment_id=? AND component_name='api'`, []any{artifactID, head.DeploymentID}},
		{`UPDATE generated_runtime_components SET state='starting',container_name=? WHERE deployment_id=? AND component_name='api'`, []any{containerName, head.DeploymentID}},
		{`UPDATE generated_runtime_components SET state='running',container_id=? WHERE deployment_id=? AND component_name='api'`, []any{containerID, head.DeploymentID}},
		{`UPDATE generated_runtime_components SET state='healthy' WHERE deployment_id=? AND component_name='api'`, []any{head.DeploymentID}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("historical component phase %d: %v", index+1, err)
		}
	}
	for _, phase := range []string{"building", "starting_candidate", "waiting_health", "switching_route"} {
		if _, err := db.Exec(`UPDATE generated_runtime_deployments SET phase=?,updated_at=? WHERE deployment_id=?`, phase, stamp, head.DeploymentID); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.Exec(`UPDATE generated_runtime_active_heads SET deployment_id=?,release_id=?,slot=?,generation=?,updated_at=?
		WHERE app_id=? AND generation=?`, head.DeploymentID, head.ReleaseID, head.Slot, head.Generation, stamp, head.AppID, head.Generation-1)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("historical head change=%d error=%v", changed, err)
	}
	if _, err := db.Exec(`UPDATE generated_runtime_components SET state='active' WHERE deployment_id=?`, head.DeploymentID); err != nil {
		t.Fatal(err)
	}
	head.UpdatedAt = testNow
	return head
}
