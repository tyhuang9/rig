package database

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	autoDeployTestNow          = "2026-08-26T12:00:00Z"
	autoDeployTestController   = "11111111-1111-4111-8111-111111111111"
	autoDeployTestBinding      = "22222222-2222-4222-8222-222222222222"
	autoDeployTestSubscription = "33333333-3333-4333-8333-333333333333"
)

func TestGithubAutoDeployMigrationUpgradesFromTwelveAndBackfillsCompactHeads(t *testing.T) {
	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "012_relay_controller_session_lifecycle.sql")
	seedRelaySourceHistory(t, db, 80)
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('legacy-local','legacy-local','Legacy Local','draft',?,?)`, autoDeployTestNow, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/013_github_auto_deploy_coordination.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var generation, rows int
	var sha, receivedAt string
	if err = db.QueryRow(`SELECT generation,observed_sha,received_at FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription).Scan(&generation, &sha, &receivedAt); err != nil {
		t.Fatal(err)
	}
	if generation != 80 || sha != fmt.Sprintf("%040x", 80) || receivedAt != "2026-08-26T12:00:00.000000000Z" {
		t.Fatalf("backfilled ACK head generation=%d sha=%q received=%q", generation, sha, receivedAt)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 32 {
		t.Fatalf("bounded upgrade history rows=%d", rows)
	}
	var enabled, revision int
	var state string
	if err = db.QueryRow(`SELECT c.enabled,c.revision,h.state FROM github_auto_deploy_configs c JOIN github_auto_deploy_heads h ON h.application_id=c.application_id WHERE c.application_id='legacy-local'`).Scan(&enabled, &revision, &state); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || revision != 0 || state != "disabled" {
		t.Fatalf("legacy default enabled=%d revision=%d state=%q", enabled, revision, state)
	}
}

func TestGithubAutoDeployMigrationNormalizesFractionalACKHeadBackfill(t *testing.T) {
	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "012_relay_controller_session_lifecycle.sql")
	seedRelaySourceHistory(t, db, 0)
	if _, err := db.Exec(`INSERT INTO relay_source_event_inbox(controller_id,delivery_id,subscription_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at,received_at) VALUES(?,'00000001-0000-4000-8000-000000000001',?,1,10,20,'refs/heads/main','0000000000000000000000000000000000000001',?,'2026-08-26T12:00:00.1Z')`, autoDeployTestController, autoDeployTestSubscription, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/013_github_auto_deploy_coordination.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var receivedAt string
	if err = db.QueryRow(`SELECT received_at FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription).Scan(&receivedAt); err != nil {
		t.Fatal(err)
	}
	if receivedAt != "2026-08-26T12:00:00.100000000Z" {
		t.Fatalf("fractional ACK backfill=%q", receivedAt)
	}
	if _, err = db.Exec(`UPDATE relay_source_ack_heads SET generation=generation+1,received_at='2026-08-26T12:00:00.1Z' WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription); err == nil {
		t.Fatal("variable-width ACK timestamp was accepted")
	} else if !strings.Contains(err.Error(), "received_at GLOB") {
		t.Fatalf("variable-width ACK timestamp failed before its CHECK constraint: %v", err)
	}
}

func TestGithubAutoDeployMigrationDefaultsAndGuardsStandWithoutRepository(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('future-local','future-local','Future Local','draft',?,?)`, autoDeployTestNow, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	var configs, heads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_auto_deploy_configs WHERE application_id='future-local' AND enabled=0 AND revision=0`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_auto_deploy_heads WHERE application_id='future-local' AND state='disabled'`).Scan(&heads); err != nil {
		t.Fatal(err)
	}
	if configs != 1 || heads != 1 {
		t.Fatalf("future app defaults configs=%d heads=%d", configs, heads)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_configs SET revision=1,enabled=1,source_owner_user_id='missing',configured_by_user_id='missing',controller_id='bad',binding_id='bad',subscription_id='bad' WHERE application_id='future-local'`); err == nil {
		t.Fatal("local application auto-enabled without an authorized derived scope")
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET state='idle' WHERE application_id='future-local'`); err == nil {
		t.Fatal("disabled work head changed to an enabled state")
	}
	if _, err := db.Exec(`DELETE FROM applications WHERE id='future-local'`); err != nil {
		t.Fatalf("disabled local application delete: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM github_auto_deploy_configs WHERE application_id='future-local'`).Scan(&configs); err != nil || configs != 0 {
		t.Fatalf("configuration cascade rows=%d err=%v", configs, err)
	}
}

func TestGithubAutoDeployMigrationProtectsOnlyCurrentACKRawRow(t *testing.T) {
	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "012_relay_controller_session_lifecycle.sql")
	seedRelaySourceHistory(t, db, 3)
	body, err := migrations.ReadFile("migrations/013_github_auto_deploy_coordination.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=? AND generation=3`, autoDeployTestController, autoDeployTestSubscription); err == nil {
		t.Fatal("current ACK raw row was deletable")
	}
	if _, err = db.Exec(`DELETE FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=? AND generation=1`, autoDeployTestController, autoDeployTestSubscription); err != nil {
		t.Fatalf("non-head audit row could not be pruned: %v", err)
	}
	if _, err = db.Exec(`UPDATE relay_source_ack_heads SET generation=2 WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription); err == nil {
		t.Fatal("ACK head generation moved backwards")
	}
}

func TestGithubAutoDeployMigrationContainsNoSensitiveMaterial(t *testing.T) {
	body, err := migrations.ReadFile("migrations/013_github_auto_deploy_coordination.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"access_token", "refresh_token", "private_key", "provider_body", "raw_webhook", "compose_document", "archive_body", "configuration_value", "secret_value"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("migration contains forbidden material %q", forbidden)
		}
	}
}

func TestGithubAutoDeployMigrationPrunesLargeLegacyHistoryInOneRankedPass(t *testing.T) {
	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "012_relay_controller_session_lifecycle.sql")
	seedRelaySourceHistory(t, db, 4096)
	body, err := migrations.ReadFile("migrations/013_github_auto_deploy_coordination.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "row_number() over") || strings.Contains(lower, "select count(*) from relay_source_event_inbox newer") {
		t.Fatal("legacy pruning is not a single window-ranked pass")
	}
	started := time.Now()
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("ranked migration pruning took %s for 4096 rows", elapsed)
	}
	var rows, generation int
	if err = db.QueryRow(`SELECT COUNT(*) FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT generation FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, autoDeployTestController, autoDeployTestSubscription).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if rows != 32 || generation != 4096 {
		t.Fatalf("large migration rows=%d ACK generation=%d", rows, generation)
	}
}

func TestGithubAutoDeployMigrationRejectsVariableWidthCoordinationTimes(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	seedRelaySourceHistory(t, db, 1)
	const fixed = "2026-08-26T12:00:00.000000000Z"
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('time-github','time-github','Time GitHub','draft',?,?)`, autoDeployTestNow, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES('time-github','github','connection',10,20,'octo','app','main','refs/heads/main','compose.yaml','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?)`, autoDeployTestNow, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_configs SET revision=1,enabled=1,source_owner_user_id='owner',configured_by_user_id='owner',controller_id=?,binding_id=?,subscription_id=?,updated_at=? WHERE application_id='time-github'`, autoDeployTestController, autoDeployTestBinding, autoDeployTestSubscription, fixed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET next_reconcile_at='2026-08-26T12:00:00.1Z' WHERE application_id='time-github'`); err == nil {
		t.Fatal("variable-width reconcile timestamp was accepted")
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET lease_fence=1,lease_token='44444444-4444-4444-8444-444444444444',lease_expires_at='2026-08-26T12:00:01.1Z' WHERE application_id='time-github'`); err == nil {
		t.Fatal("variable-width lease timestamp was accepted")
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET lease_fence=1,lease_token='44444444-4444-4444-8444-444444444444',lease_expires_at='2026-08-26T12:00:01.000000000Z' WHERE application_id='time-github'`); err != nil {
		t.Fatalf("fixed-width lease timestamp rejected: %v", err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET lease_token=NULL,lease_expires_at=NULL WHERE application_id='time-github'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET lease_fence=2 WHERE application_id='time-github'`); err == nil {
		t.Fatal("lease fence advanced without a claim or access-loss invalidation")
	}
}

func applyMigrationsThrough(t *testing.T, db *sql.DB, last string) {
	t.Helper()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, readErr := migrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := db.Exec(string(body)); execErr != nil {
			t.Fatalf("apply %s: %v", entry.Name(), execErr)
		}
		if entry.Name() == last {
			return
		}
	}
	t.Fatalf("migration %s not found", last)
}

func seedRelaySourceHistory(t *testing.T, db *sql.DB, count int) {
	t.Helper()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES('owner','owner','hash','administrator',?,?)`, autoDeployTestNow, autoDeployTestNow)
	mustExec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES('connection','owner','github','connected','42','octocat',1,?,?,?,?,?)`, autoDeployTestNow, autoDeployTestNow, autoDeployTestNow, autoDeployTestNow, autoDeployTestNow)
	mustExec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, autoDeployTestController, autoDeployTestNow, autoDeployTestNow)
	mustExec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,'owner','connection',?,10,20,'authorized',?,?,?)`, autoDeployTestBinding, autoDeployTestController, autoDeployTestNow, autoDeployTestNow, autoDeployTestNow)
	mustExec(`INSERT INTO relay_controller_subscriptions(subscription_id,owner_user_id,binding_id,controller_id,installation_id,repository_id,tracked_ref,state,created_at) VALUES(?,'owner',?,?,10,20,'refs/heads/main','active',?)`, autoDeployTestSubscription, autoDeployTestBinding, autoDeployTestController, autoDeployTestNow)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO relay_source_event_inbox(controller_id,delivery_id,subscription_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at,received_at) VALUES(?,?,?,?,10,20,'refs/heads/main',?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	defer statement.Close()
	for generation := 1; generation <= count; generation++ {
		delivery := fmt.Sprintf("%08x-0000-4000-8000-%012x", generation, generation)
		sha := fmt.Sprintf("%040x", generation)
		if _, err = statement.Exec(autoDeployTestController, delivery, autoDeployTestSubscription, generation, sha, autoDeployTestNow, autoDeployTestNow); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
