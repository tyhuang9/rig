package database

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRelayControllerMigrationUpgradesFromSixAndEnforcesPrivacyAndIntegrity(t *testing.T) {
	db := openMemoryDatabase(t)
	for _, name := range []string{"001_foundation.sql", "002_github_connections.sql", "003_application_sources.sql", "004_release_snapshots.sql", "005_application_configuration.sql", "006_compose_deployment_runtime.sql"} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',?,?),('other','other','hash',?,?)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES('connection','owner','github','connected','42','octocat',1,?,?,?,?,?)`, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/007_relay_controller_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var existing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM source_connections WHERE id='connection' AND owner_user_id='owner'`).Scan(&existing); err != nil || existing != 1 {
		t.Fatalf("upgrade preserved source connection=%d err=%v", existing, err)
	}

	var schema strings.Builder
	rows, err := db.Query(`SELECT COALESCE(sql,'') FROM sqlite_master WHERE name LIKE 'relay_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var statement string
		if err := rows.Scan(&statement); err != nil {
			t.Fatal(err)
		}
		schema.WriteString(strings.ToLower(statement))
		schema.WriteByte('\n')
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "access_token", "refresh_token", "oauth", "pkce", "signature", "nonce", "raw_frame", "poll_token", "poll_hash", "provider_body"} {
		if strings.Contains(schema.String(), forbidden) {
			t.Errorf("relay SQLite schema contains forbidden material %q", forbidden)
		}
	}
	for _, required := range []string{"relay_controllers", "relay_controller_keys", "relay_key_rotations", "relay_installation_bindings", "relay_enrollments", "protected_key_ref", "protected_poll_ref"} {
		if !strings.Contains(schema.String(), required) {
			t.Errorf("relay SQLite schema is missing %q", required)
		}
	}

	controllerID := "11111111-1111-4111-8111-111111111111"
	keyID := "22222222-2222-4222-8222-222222222222"
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,'NOT-CANONICAL','active',?,?)`, now, now); err == nil {
		t.Fatal("noncanonical controller ID was accepted")
	}
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, now, now); err != nil {
		t.Fatal(err)
	}
	publicKey := bytes.Repeat([]byte{0x11}, 32)
	keyRef := "relay/controllers/" + controllerID + "/keys/" + keyID + ".key"
	if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?)`, keyID, controllerID, publicKey, keyRef, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE relay_controller_keys SET public_key=? WHERE key_id=?`, bytes.Repeat([]byte{0x22}, 32), keyID); err == nil {
		t.Fatal("public-key identity metadata was mutable")
	}

	enrollmentID := "33333333-3333-4333-8333-333333333333"
	pollRef := "relay/controllers/" + controllerID + "/enrollments/" + enrollmentID + "/poll"
	expires := time.Date(2026, 8, 25, 12, 10, 0, 0, time.UTC).Format(time.RFC3339Nano)
	insertEnrollment := `INSERT INTO relay_enrollments(enrollment_id,owner_user_id,connection_id,controller_id,key_id,installation_id,repository_id,purpose,protected_poll_ref,state,created_at,expires_at,state_changed_at,updated_at) VALUES(?,?,?,?,?,?,?,'controller-relay-enrollment-poll',?,'pending',?,?,?,?)`
	if _, err := db.Exec(insertEnrollment, enrollmentID, "other", "connection", controllerID, keyID, 7, 8, pollRef, now, expires, now, now); err == nil {
		t.Fatal("cross-owner enrollment was accepted")
	}
	if _, err := db.Exec(insertEnrollment, enrollmentID, "owner", "connection", controllerID, keyID, 7, 8, pollRef, now, expires, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE source_connections SET owner_user_id='other' WHERE id='connection'`); err == nil {
		t.Fatal("source connection ownership was changed beneath an enrollment")
	}
	if _, err := db.Exec(`UPDATE relay_controllers SET last_error_code='ghu_secret_material' WHERE controller_id=?`, controllerID); err == nil {
		t.Fatal("noncanonical error material was accepted")
	}
	if _, err := db.Exec(`UPDATE relay_enrollments SET state='denied',completed_at=?,state_changed_at=?,updated_at=? WHERE enrollment_id=?`, now, now, now, enrollmentID); err != nil {
		t.Fatal(err)
	}
	var retainedRef string
	if err := db.QueryRow(`SELECT protected_poll_ref FROM relay_enrollments WHERE enrollment_id=?`, enrollmentID).Scan(&retainedRef); err != nil || retainedRef != pollRef {
		t.Fatalf("terminal enrollment poll ref=%q err=%v", retainedRef, err)
	}
	if _, err := db.Exec(`UPDATE relay_enrollments SET state='failed',last_error_code='enrollment_failed' WHERE enrollment_id=?`, enrollmentID); err == nil {
		t.Fatal("terminal enrollment was transitioned again")
	}
	if _, err := db.Exec(`UPDATE relay_enrollments SET protected_poll_ref=NULL,poll_ref_cleared_at=?,updated_at=? WHERE enrollment_id=?`, now, now, enrollmentID); err != nil {
		t.Fatalf("ordered poll-ref cleanup: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM relay_enrollments WHERE enrollment_id=?`, enrollmentID); err == nil {
		t.Fatal("enrollment audit history was deleted")
	}

	bindingA := "44444444-4444-4444-8444-444444444444"
	bindingB := "55555555-5555-4555-8555-555555555555"
	insertBinding := `INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?,?)`
	if _, err := db.Exec(insertBinding, bindingA, "other", "connection", controllerID, 7, 8, now, now, now); err == nil {
		t.Fatal("cross-owner binding was accepted")
	}
	if _, err := db.Exec(insertBinding, bindingA, "owner", "connection", controllerID, 7, 8, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insertBinding, bindingB, "owner", "connection", controllerID, 7, 8, now, now, now); err == nil {
		t.Fatal("second live binding for the same immutable provider identity was accepted")
	}
	if _, err := db.Exec(`DELETE FROM relay_installation_bindings WHERE binding_id=?`, bindingA); err == nil {
		t.Fatal("binding audit history was deleted")
	}
}
