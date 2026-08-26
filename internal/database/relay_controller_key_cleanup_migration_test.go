package database

import (
	"bytes"
	"database/sql"
	"testing"
)

func TestRelayControllerKeyCleanupMigrationAppliesFreshAndFrom009(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T) *sql.DB
	}{
		{name: "fresh", prepare: func(t *testing.T) *sql.DB { return openMemoryDatabase(t) }},
		{name: "upgrade", prepare: openDatabaseAtRelayMigration009},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := test.prepare(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			assertRelayControllerKeyCleanupIntegrity(t, db)
		})
	}
}

func openDatabaseAtRelayMigration009(t *testing.T) *sql.DB {
	t.Helper()
	db := openMemoryDatabase(t)
	names := []string{
		"001_foundation.sql",
		"002_github_connections.sql",
		"003_application_sources.sql",
		"004_release_snapshots.sql",
		"005_application_configuration.sql",
		"006_compose_deployment_runtime.sql",
		"007_relay_controller_foundation.sql",
		"008_relay_controller_session.sql",
		"009_relay_outbound_pending_replay.sql",
	}
	for _, name := range names {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func assertRelayControllerKeyCleanupIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	controllerID := "11111111-1111-4111-8111-111111111111"
	activeKeyID := "22222222-2222-4222-8222-222222222222"
	revokedKeyID := "33333333-3333-4333-8333-333333333333"
	now := "2026-08-25T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?)`, activeKeyID, controllerID, bytes.Repeat([]byte{1}, 32), "relay/controllers/"+controllerID+"/keys/"+activeKeyID+".key", now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,revoked_at) VALUES(?,?,?,'ed25519','revoked',?,?,?,?)`, revokedKeyID, controllerID, bytes.Repeat([]byte{2}, 32), "relay/controllers/"+controllerID+"/keys/"+revokedKeyID+".key", now, now, now); err != nil {
		t.Fatal(err)
	}
	rejectedKeyID := "44444444-4444-4444-8444-444444444444"
	if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,revoked_at,protected_key_cleared_at) VALUES(?,?,?,'ed25519','revoked',?,?,?,?,?)`, rejectedKeyID, controllerID, bytes.Repeat([]byte{3}, 32), "relay/controllers/"+controllerID+"/keys/"+rejectedKeyID+".key", now, now, now, now); err == nil {
		t.Fatal("revoked key inserted with a protected clear marker")
	}
	if _, err := db.Exec(`UPDATE relay_controller_keys SET protected_key_cleared_at=? WHERE key_id=?`, now, activeKeyID); err == nil {
		t.Fatal("active key accepted protected clear marker")
	}
	for _, invalid := range []string{"not-a-time", "2026-08-25 12:00:00Z", "2026-08-25T12:00:00+00:00", "2026-08-25T12:00:00.xZ"} {
		if _, err := db.Exec(`UPDATE relay_controller_keys SET protected_key_cleared_at=? WHERE key_id=?`, invalid, revokedKeyID); err == nil {
			t.Fatalf("invalid clear timestamp accepted: %q", invalid)
		}
	}
	if _, err := db.Exec(`UPDATE relay_controller_keys SET protected_key_cleared_at=? WHERE key_id=?`, "2026-08-25T12:00:00.123456789Z", revokedKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE relay_controller_keys SET protected_key_cleared_at=NULL WHERE key_id=?`, revokedKeyID); err == nil {
		t.Fatal("protected clear marker reverted to null")
	}
	if _, err := db.Exec(`UPDATE relay_controller_keys SET protected_key_cleared_at=? WHERE key_id=?`, "2026-08-25T12:01:00Z", revokedKeyID); err == nil {
		t.Fatal("protected clear marker changed after being set")
	}
}
