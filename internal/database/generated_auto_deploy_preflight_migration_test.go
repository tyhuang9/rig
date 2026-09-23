package database

import (
	"strings"
	"testing"
)

func TestGeneratedAutoDeployPreflightMigrationPreservesHeadsAndAddsPauseCodes(t *testing.T) {
	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "020_generated_runtime_state.sql")
	seedRelaySourceHistory(t, db, 0)
	seedReservationApplication(t, db)
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads
		SET state='paused',pause_code='approval_required',paused_sha='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			last_consumed_generation=3,latest_resolved_generation=3,latest_resolved_sha='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			last_reconciled_at='2026-08-26T12:00:00.000000000Z',next_reconcile_at='2026-08-26T12:01:00.000000000Z'
		WHERE application_id='reservation-app'`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/021_generated_auto_deploy_preflight.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}

	var revision, consumed, resolved int64
	var state, pause, sha, reconciled, next string
	if err = db.QueryRow(`SELECT config_revision,state,pause_code,paused_sha,last_consumed_generation,
		latest_resolved_generation,last_reconciled_at,next_reconcile_at
		FROM github_auto_deploy_heads WHERE application_id='reservation-app'`).Scan(
		&revision, &state, &pause, &sha, &consumed, &resolved, &reconciled, &next,
	); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || state != "paused" || pause != "approval_required" || sha != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || consumed != 3 || resolved != 3 || reconciled != "2026-08-26T12:00:00.000000000Z" || next != "2026-08-26T12:01:00.000000000Z" {
		t.Fatalf("preserved head revision=%d state=%q pause=%q sha=%q consumed=%d resolved=%d reconciled=%q next=%q", revision, state, pause, sha, consumed, resolved, reconciled, next)
	}
	for _, code := range []string{"migration_approval_required", "insufficient_replacement_capacity", "deployment_plan_review_required"} {
		if _, err = db.Exec(`UPDATE github_auto_deploy_heads SET pause_code=? WHERE application_id='reservation-app'`, code); err != nil {
			t.Fatalf("pause code %q rejected: %v", code, err)
		}
	}
	if _, err = db.Exec(`UPDATE github_auto_deploy_heads SET pause_code='raw_build_failure' WHERE application_id='reservation-app'`); err == nil {
		t.Fatal("unknown pause code accepted")
	}
	for _, name := range []string{
		"github_auto_deploy_config_seed_head",
		"github_auto_deploy_config_reset_head",
		"github_auto_deploy_head_config_fence",
		"github_auto_deploy_resolution_reservation_guard",
		"github_auto_deploy_subscription_retire_locked",
	} {
		var count int
		if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("trigger %q count=%d err=%v", name, count, err)
		}
	}
}

func TestGeneratedAutoDeployPreflightMigrationContainsNoCommandOrSecretMaterial(t *testing.T) {
	body, err := migrations.ReadFile("migrations/021_generated_auto_deploy_preflight.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"build_command", "run_command", "migration_command", "access_token", "refresh_token", "private_key", "configuration_value", "secret_value"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("migration contains forbidden material %q", forbidden)
		}
	}
}
