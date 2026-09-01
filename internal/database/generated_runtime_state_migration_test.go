package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const generatedRuntimeStateMigration = "020_generated_runtime_state.sql"

func TestGeneratedRuntimeStateMigrationMirrorAndCompatibility(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedRuntimeStateMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", generatedRuntimeStateMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("embedded migration differs from public migration")
	}

	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	seedDeploymentPlanApplication(t, db, "runtime-state-app")
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,status) VALUES('legacy-compose','runtime-state-app','succeeded')`); err != nil {
		t.Fatalf("legacy Compose deployment rejected: %v", err)
	}
	var strategy string
	if err := db.QueryRow(`SELECT runtime_strategy FROM deployments WHERE id='legacy-compose'`).Scan(&strategy); err != nil || strategy != "compose" {
		t.Fatalf("legacy strategy=%q err=%v", strategy, err)
	}
	var heads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads WHERE app_id='runtime-state-app' AND generation=0`).Scan(&heads); err != nil || heads != 1 {
		t.Fatalf("initial active head count=%d err=%v", heads, err)
	}
	for _, table := range []string{"generated_runtime_deployments", "generated_runtime_components", "generated_runtime_active_heads"} {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			lower := strings.ToLower(name)
			if strings.Contains(lower, "command") || strings.Contains(lower, "environment") || strings.Contains(lower, "output") || strings.Contains(lower, "log") {
				rows.Close()
				t.Fatalf("unsafe runtime metadata column %s.%s", table, name)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
