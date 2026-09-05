package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const persistentGitHubConnectionMigration = "023_persistent_github_connection.sql"

func TestPersistentGitHubConnectionMigrationMatchesPublicCopy(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + persistentGitHubConnectionMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", persistentGitHubConnectionMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("public and embedded persistent GitHub migrations differ")
	}
}

func TestPersistentGitHubConnectionMigrationBackfillsNewestIdentityWithoutRemovingLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range migrationNames(t, migrations, "migrations") {
		if name == persistentGitHubConnectionMigration {
			break
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES
		('owner','owner','hash',datetime('now'),datetime('now')),
		('other','other','hash',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES
		('11111111111111111111111111111111','owner','github','connected','41','old',1,'2026-09-05T13:00:00Z','2026-09-06T13:00:00Z','2026-09-01T13:00:00Z','2026-09-01T13:00:00Z','2026-09-01T13:00:00Z'),
		('22222222222222222222222222222222','owner','github','connected','42','new',1,'2026-09-05T13:00:00Z','2026-09-06T13:00:00Z','2026-09-02T13:00:00Z','2026-09-02T13:00:00Z','2026-09-02T13:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var connectionID string
	if err := db.QueryRow(`SELECT connection_id FROM github_default_connections WHERE owner_user_id='owner'`).Scan(&connectionID); err != nil || connectionID != "22222222222222222222222222222222" {
		t.Fatalf("backfilled connection=%q err=%v", connectionID, err)
	}
	var legacyRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM source_connections WHERE owner_user_id='owner'`).Scan(&legacyRows); err != nil || legacyRows != 2 {
		t.Fatalf("legacy rows=%d err=%v", legacyRows, err)
	}
	if _, err := db.Exec(`INSERT INTO github_default_connections(owner_user_id,connection_id,created_at,updated_at) VALUES('other','11111111111111111111111111111111',datetime('now'),datetime('now'))`); err == nil {
		t.Fatal("cross-owner default mapping accepted")
	}
	if _, err := db.Exec(`INSERT INTO github_connection_authorizations(id,owner_user_id,connection_id,status,credential_generation,pending_expires_at,poll_interval_seconds,next_poll_at,created_at,updated_at) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','other','22222222222222222222222222222222','pending',1,datetime('now','+10 minutes'),5,datetime('now'),datetime('now'),datetime('now'))`); err == nil {
		t.Fatal("cross-owner authorization accepted")
	}
}
