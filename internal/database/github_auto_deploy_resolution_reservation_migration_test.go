package database

import (
	"database/sql"
	"strings"
	"testing"
)

const autoDeployReservationLease = "44444444-4444-4444-8444-444444444444"

func TestGithubAutoDeployResolutionReservationMigrationUpgradesThirteen(t *testing.T) {
	db := openMemoryDatabase(t)
	applyMigrationsThrough(t, db, "012_relay_controller_session_lifecycle.sql")
	seedRelaySourceHistory(t, db, 1)
	coordination, err := migrations.ReadFile("migrations/013_github_auto_deploy_coordination.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(coordination)); err != nil {
		t.Fatal(err)
	}
	seedReservationApplication(t, db)
	body, err := migrations.ReadFile("migrations/014_github_auto_deploy_resolution_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var generation, fence sql.NullInt64
	var revision, enabled int
	if err = db.QueryRow(`SELECT c.revision,c.enabled,h.resolving_generation,h.resolving_lease_fence
		FROM github_auto_deploy_configs c JOIN github_auto_deploy_heads h ON h.application_id=c.application_id
		WHERE c.application_id='reservation-app'`).Scan(&revision, &enabled, &generation, &fence); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || enabled != 1 || generation.Valid || fence.Valid {
		t.Fatalf("upgrade state revision=%d enabled=%d generation=%#v fence=%#v", revision, enabled, generation, fence)
	}
}

func TestGithubAutoDeployResolutionReservationMigrationConstraintsAndClearing(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	seedRelaySourceHistory(t, db, 0)
	if _, err := db.Exec(`INSERT INTO relay_source_ack_heads(controller_id,subscription_id,delivery_id,generation,installation_id,repository_id,tracked_ref,observed_sha,observed_at,received_at)
		VALUES(?,?,'55555555-5555-4555-8555-555555555555',1,10,20,'refs/heads/main','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?)`, autoDeployTestController, autoDeployTestSubscription, autoDeployTestNow, "2026-08-26T12:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	seedReservationApplication(t, db)
	const fixed = "2026-08-26T12:00:00.000000000Z"
	const expires = "2026-08-26T12:01:00.000000000Z"
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET lease_fence=1,lease_token=?,lease_expires_at=? WHERE application_id='reservation-app'`, autoDeployReservationLease, expires); err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"missing fence": `UPDATE github_auto_deploy_heads SET resolving_generation=1 WHERE application_id='reservation-app'`,
		"wrong fence":   `UPDATE github_auto_deploy_heads SET resolving_generation=1,resolving_lease_fence=2 WHERE application_id='reservation-app'`,
		"negative":      `UPDATE github_auto_deploy_heads SET resolving_generation=-1,resolving_lease_fence=1 WHERE application_id='reservation-app'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("%s reservation was accepted", name)
		}
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET resolving_generation=1,resolving_lease_fence=1 WHERE application_id='reservation-app'`); err != nil {
		t.Fatalf("valid reservation: %v", err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET state='paused',pause_code='source_access_lost',paused_sha='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE application_id='reservation-app'`); err != nil {
		t.Fatalf("access-loss transition: %v", err)
	}
	assertNoMigrationReservation(t, db)
	if _, err := db.Exec(`UPDATE github_auto_deploy_heads SET state='idle',pause_code=NULL,paused_sha=NULL,resolving_generation=1,resolving_lease_fence=1 WHERE application_id='reservation-app'`); err != nil {
		t.Fatalf("second reservation: %v", err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_configs SET revision=2,enabled=0,configured_by_user_id='owner',controller_id=NULL,binding_id=NULL,subscription_id=NULL,updated_at=? WHERE application_id='reservation-app'`, fixed); err != nil {
		t.Fatalf("configuration reset: %v", err)
	}
	assertNoMigrationReservation(t, db)
}

func TestGithubAutoDeployResolutionReservationMigrationContainsNoSensitiveMaterial(t *testing.T) {
	body, err := migrations.ReadFile("migrations/014_github_auto_deploy_resolution_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"access_token", "refresh_token", "private_key", "provider_body", "observed_sha", "repository_name", "configuration_value", "secret_value"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("migration contains forbidden material %q", forbidden)
		}
	}
}

func seedReservationApplication(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('reservation-app','reservation-app','Reservation App','draft',?,?)`, autoDeployTestNow, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at)
		VALUES('reservation-app','github','connection',10,20,'octo','app','main','refs/heads/main','compose.yaml','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,?)`, autoDeployTestNow, autoDeployTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE github_auto_deploy_configs SET revision=1,enabled=1,source_owner_user_id='owner',configured_by_user_id='owner',controller_id=?,binding_id=?,subscription_id=?,updated_at='2026-08-26T12:00:00.000000000Z' WHERE application_id='reservation-app'`, autoDeployTestController, autoDeployTestBinding, autoDeployTestSubscription); err != nil {
		t.Fatal(err)
	}
}

func assertNoMigrationReservation(t *testing.T, db *sql.DB) {
	t.Helper()
	var generation, fence sql.NullInt64
	if err := db.QueryRow(`SELECT resolving_generation,resolving_lease_fence FROM github_auto_deploy_heads WHERE application_id='reservation-app'`).Scan(&generation, &fence); err != nil {
		t.Fatal(err)
	}
	if generation.Valid || fence.Valid {
		t.Fatalf("reservation not cleared generation=%#v fence=%#v", generation, fence)
	}
}
