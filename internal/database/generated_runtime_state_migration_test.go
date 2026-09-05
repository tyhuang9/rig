package database

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestGeneratedRuntimeStateMigrationUpgradeEnforcesInitialDeploymentState(t *testing.T) {
	testCases := []struct {
		name              string
		hasEvidence       bool
		hasApproval       bool
		phase             string
		migrationState    string
		migrationStarted  any
		migrationFinished any
		wantRejected      bool
	}{
		{name: "no migration", phase: "preflight", migrationState: "not_required"},
		{name: "approved migration", hasEvidence: true, hasApproval: true, phase: "preflight", migrationState: "pending"},
		{name: "non-preflight phase", phase: "building", migrationState: "not_required", wantRejected: true},
		{name: "running migration", hasEvidence: true, hasApproval: true, phase: "preflight", migrationState: "running", migrationStarted: "2026-08-31T12:00:00Z", wantRejected: true},
		{name: "succeeded migration", hasEvidence: true, hasApproval: true, phase: "preflight", migrationState: "succeeded", migrationStarted: "2026-08-31T12:00:00Z", migrationFinished: "2026-08-31T12:01:00Z", wantRejected: true},
		{name: "failed migration", hasEvidence: true, hasApproval: true, phase: "preflight", migrationState: "failed", migrationStarted: "2026-08-31T12:00:00Z", migrationFinished: "2026-08-31T12:01:00Z", wantRejected: true},
		{name: "unapproved migration", hasEvidence: true, phase: "preflight", migrationState: "pending", wantRejected: true},
		{name: "approval without evidence pending", hasApproval: true, phase: "preflight", migrationState: "pending", wantRejected: true},
		{name: "approval without evidence not required", hasApproval: true, phase: "preflight", migrationState: "not_required", wantRejected: true},
		{name: "approved migration marked not required", hasEvidence: true, hasApproval: true, phase: "preflight", migrationState: "not_required", wantRejected: true},
	}

	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "019_generated_image_artifacts.sql")
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('runtime-owner','runtime-owner','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	for index, testCase := range testCases {
		suffix := strconv.Itoa(index)
		appID, planID, releaseID := "runtime-app-"+suffix, "runtime-plan-"+suffix, "runtime-release-"+suffix
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, appID, appID, appID); err != nil {
			t.Fatal(err)
		}
		evidenceDigest := ""
		if testCase.hasEvidence {
			evidenceDigest = strings.Repeat("e", 64)
		}
		if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
			id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
			analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
			field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
		) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,?,'runtime-owner',datetime('now'),'accepted','runtime-owner',datetime('now'))`,
			planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), evidenceDigest); err != nil {
			t.Fatal(err)
		}
		if testCase.hasApproval {
			if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,'runtime-owner',datetime('now'))`, planID, appID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO releases(
			id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
			workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,'ready','{}',datetime('now'),'local',0,?,'ready',?,?,1)`, releaseID, appID, strings.Repeat("b", 64), strings.Repeat("f", 64), planID); err != nil {
			t.Fatal(err)
		}
	}
	body, err := migrations.ReadFile("migrations/" + generatedRuntimeStateMigration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatalf("upgrade migration failed: %v", err)
	}
	var initialHeads int
	if err = db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads WHERE generation=0`).Scan(&initialHeads); err != nil || initialHeads != len(testCases) {
		t.Fatalf("upgraded active heads=%d want=%d err=%v", initialHeads, len(testCases), err)
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			suffix := strconv.Itoa(index)
			appID, planID := "runtime-app-"+suffix, "runtime-plan-"+suffix
			releaseID, deploymentID := "runtime-release-"+suffix, "runtime-deployment-"+suffix
			if _, err := db.Exec(`INSERT INTO deployments(
				id,app_id,release_id,status,configuration_mode,provenance_initialized,runtime_strategy,
				deployment_plan_revision_id,deployment_plan_revision_number
			) VALUES(?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, appID, releaseID, planID); err != nil {
				t.Fatal(err)
			}
			_, err := db.Exec(`INSERT INTO generated_runtime_deployments(
				deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
				candidate_slot,phase,migration_state,created_at,updated_at,migration_started_at,migration_finished_at
			) VALUES(?,?,?,?,1,'blue',?,?,datetime('now'),datetime('now'),?,?)`, deploymentID, appID, releaseID, planID,
				testCase.phase, testCase.migrationState, testCase.migrationStarted, testCase.migrationFinished)
			if testCase.wantRejected && err == nil {
				t.Fatal("invalid initial generated runtime deployment accepted")
			}
			if !testCase.wantRejected && err != nil {
				t.Fatalf("valid initial generated runtime deployment rejected: %v", err)
			}
		})
	}
}
