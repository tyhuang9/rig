package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scopedApplicationConfigurationMigration = "024_scoped_application_configuration.sql"

func TestScopedApplicationConfigurationMigration(t *testing.T) {
	t.Run("mirror and metadata only", func(t *testing.T) {
		embedded, err := migrations.ReadFile("migrations/" + scopedApplicationConfigurationMigration)
		if err != nil {
			t.Fatal(err)
		}
		public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", scopedApplicationConfigurationMigration))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(public) {
			t.Fatal("embedded migration differs from public migration")
		}
		lower := strings.ToLower(string(embedded))
		for _, forbidden := range []string{"secret_value", "plaintext", "encrypted_value"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("migration contains value-bearing column %q", forbidden)
			}
		}
	})

	t.Run("upgrade preserves v1 and enforces v2 pins and immutability", func(t *testing.T) {
		db := openMemoryDatabase(t)
		applyMigrationsThrough(t, db, "023_persistent_github_connection.sql")
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('scoped-app','scoped-app','Scoped','draft',datetime('now'),datetime('now'))`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_at,variable_count,secret_count) VALUES('legacy','scoped-app',1,'apps/scoped-app/configuration/legacy.secret',datetime('now'),1,0)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_entries(revision_id,key,sensitive) VALUES('legacy','MODE',0)`); err != nil {
			t.Fatal(err)
		}
		body, err := migrations.ReadFile("migrations/" + scopedApplicationConfigurationMigration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		var version int
		var legacyPlanID any
		if err := db.QueryRow(`SELECT bundle_version,deployment_plan_revision_id FROM application_configuration_revisions WHERE id='legacy'`).Scan(&version, &legacyPlanID); err != nil || version != 1 || legacyPlanID != nil {
			t.Fatalf("legacy metadata version=%d plan=%v err=%v", version, legacyPlanID, err)
		}
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',datetime('now'),datetime('now'))`); err != nil {
			t.Fatal(err)
		}
		planID := "11111111-1111-1111-8111-111111111111"
		if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at)
			VALUES(?, 'scoped-app',1,?,'generated_node','test','1',?,'local',0,?,?,1,0,'','owner',datetime('now'),'accepted','owner',datetime('now'))`, planID, "apps/scoped-app/deployment-plans/"+planID+".secret", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_at,variable_count,secret_count,bundle_version,deployment_plan_revision_id,deployment_plan_revision_number) VALUES('bad','scoped-app',2,'apps/scoped-app/configuration/bad.secret',datetime('now'),0,0,2,NULL,NULL)`); err == nil {
			t.Fatal("v2 revision without a deployment plan pin was accepted")
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_at,variable_count,secret_count,bundle_version,deployment_plan_revision_id,deployment_plan_revision_number) VALUES('scoped','scoped-app',2,'apps/scoped-app/configuration/scoped.secret',datetime('now'),1,1,2,?,1)`, planID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_scoped_entries(revision_id,phase,target_component,key,sensitivity) VALUES('scoped','runtime','api','DATABASE_URL','secret'),('scoped','build','web','VITE_LABEL','public')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_entries(revision_id,key,sensitive) VALUES('scoped','LEGACY',0)`); err == nil {
			t.Fatal("legacy entry metadata was accepted for a v2 revision")
		}
		if _, err := db.Exec(`UPDATE application_configuration_scoped_entries SET key='CHANGED' WHERE revision_id='scoped' AND key='DATABASE_URL'`); err == nil {
			t.Fatal("scoped entry update was accepted")
		}
		if _, err := db.Exec(`DELETE FROM application_configuration_scoped_entries WHERE revision_id='scoped' AND key='DATABASE_URL'`); err == nil {
			t.Fatal("scoped entry deletion was accepted")
		}
		if _, err := db.Exec(`UPDATE application_configuration_revisions SET deployment_plan_revision_number=2 WHERE id='scoped'`); err == nil {
			t.Fatal("scoped revision plan pin was mutable")
		}
	})
}
