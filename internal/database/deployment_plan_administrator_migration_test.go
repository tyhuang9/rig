package database

import (
	"strings"
	"testing"
)

func TestDeploymentPlanAdministratorApprovalMigration(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	seedDeploymentPlanApplication(t, db, "administrator-plan")
	for _, user := range []struct{ id, role string }{{"plan-admin", "administrator"}, {"plan-operator", "operator"}} {
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?, ?,datetime('now'),datetime('now'))`, user.id, user.id, "hash", user.role); err != nil {
			t.Fatal(err)
		}
	}

	insertRevision := func(id, actor string, number int) error {
		_, err := db.Exec(`INSERT INTO deployment_plan_revisions(
			id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,
			source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,
			analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,
			migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
		) VALUES(?, 'administrator-plan', ?, ?, 'generated_node', 'javascript', '1', ?, 'local', 0, ?, ?, 1, 1, ?, ?, datetime('now'), 'accepted', ?, datetime('now'))`,
			id, number, "apps/administrator-plan/deployment-plans/"+id+".secret",
			strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), actor, actor)
		return err
	}
	if err := insertRevision("11111111-1111-4111-8111-111111111111", "plan-operator", 1); err == nil {
		t.Fatal("non-administrator deployment plan acceptance was persisted")
	}
	validID := "22222222-2222-4222-8222-222222222222"
	if err := insertRevision(validID, "plan-admin", 1); err != nil {
		t.Fatalf("administrator deployment plan acceptance rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?, 'administrator-plan', 1, 'plan-operator', datetime('now'))`, validID); err == nil {
		t.Fatal("non-administrator migration approval was persisted")
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?, 'administrator-plan', 1, 'plan-admin', datetime('now'))`, validID); err != nil {
		t.Fatalf("administrator migration approval rejected: %v", err)
	}
}
