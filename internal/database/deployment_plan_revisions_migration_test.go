package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const deploymentPlanRevisionsMigration = "017_deployment_plan_revisions.sql"

func TestDeploymentPlanRevisionsMigration(t *testing.T) {
	t.Run("mirror", func(t *testing.T) {
		embedded, err := migrations.ReadFile("migrations/" + deploymentPlanRevisionsMigration)
		if err != nil {
			t.Fatal(err)
		}
		public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", deploymentPlanRevisionsMigration))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(public) {
			t.Fatal("embedded migration differs from public migration")
		}
		if strings.Contains(strings.ToLower(string(embedded)), "command") {
			t.Fatal("migration retains command-bearing material")
		}
	})

	t.Run("fresh integrity and release provenance", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		seedDeploymentPlanApplication(t, db, "plan-fresh")
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state)
			VALUES('legacy','plan-fresh','materializing','{}',datetime('now'),'github',1,?,'compose.yaml','materializing')`, strings.Repeat("a", 40)); err != nil {
			t.Fatalf("legacy release rejected: %v", err)
		}
		id := "11111111-1111-1111-8111-111111111111"
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at)
			VALUES(?,?,1,?,'compose','compose','1',?,?,0,0,'','owner',datetime('now'),'accepted','owner',datetime('now'))`, id, "plan-fresh", "apps/plan-fresh/deployment-plans/"+id+".secret", strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=1,updated_at=datetime('now') WHERE app_id='plan-fresh'`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state,deployment_plan_revision_id,deployment_plan_revision_number)
			VALUES('pinned','plan-fresh','materializing','{}',datetime('now'),'github',2,?,'compose.yaml','materializing',?,1)`, strings.Repeat("d", 40), id); err != nil {
			t.Fatalf("valid plan provenance rejected: %v", err)
		}
		if _, err := db.Exec(`UPDATE releases SET deployment_plan_revision_number=2 WHERE id='pinned'`); err == nil {
			t.Fatal("invalid revision provenance accepted")
		}
		if _, err := db.Exec(`UPDATE deployment_plan_revisions SET detector='changed' WHERE id=?`, id); err == nil {
			t.Fatal("mutable deployment plan revision accepted")
		}
		if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at)
			VALUES('22222222-2222-2222-8222-222222222222','plan-fresh',2,'apps/plan-fresh/deployment-plans/22222222-2222-2222-8222-222222222222.secret','compose','compose','1',?,?,0,0,'','owner',datetime('now'),'pending','owner',datetime('now'))`, strings.Repeat("d", 64), strings.Repeat("e", 64)); err == nil {
			t.Fatal("unaccepted deployment plan revision accepted")
		}
	})

	t.Run("upgrade preserves legacy releases", func(t *testing.T) {
		db := openMemoryDatabase(t)
		applyMigrationsThrough(t, db, "016_release_workspace_tree_integrity.sql")
		if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for _, name := range migrationNames(t, migrations, "migrations") {
			if name == deploymentPlanRevisionsMigration {
				break
			}
			if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
				t.Fatalf("record %s: %v", name, err)
			}
		}
		seedDeploymentPlanApplication(t, db, "plan-upgrade")
		if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,workspace_state)
			VALUES('upgrade-release','plan-upgrade','materializing','{}',datetime('now'),'github',1,?,'compose.yaml','materializing')`, strings.Repeat("e", 40)); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		var id sql.NullString
		var number sql.NullInt64
		if err := db.QueryRow(`SELECT deployment_plan_revision_id,deployment_plan_revision_number FROM releases WHERE id='upgrade-release'`).Scan(&id, &number); err != nil || id.Valid || number.Valid {
			t.Fatalf("legacy provenance id=%#v number=%#v err=%v", id, number, err)
		}
	})
}

func seedDeploymentPlanApplication(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, id, id, id); err != nil {
		t.Fatal(err)
	}
}
