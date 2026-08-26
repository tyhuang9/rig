package database

import (
	"database/sql"
	"strings"
	"testing"
)

func TestRelayOutboundPendingReplayIndexAppliesOnFreshInstallAndUpgrade(t *testing.T) {
	fresh := openMemoryDatabase(t)
	if err := Migrate(fresh); err != nil {
		t.Fatalf("fresh migration: %v", err)
	}
	assertRelayOutboundPendingReplayIndex(t, fresh)
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	upgrade := openMemoryDatabase(t)
	for _, name := range []string{
		"001_foundation.sql",
		"002_github_connections.sql",
		"003_application_sources.sql",
		"004_release_snapshots.sql",
		"005_application_configuration.sql",
		"006_compose_deployment_runtime.sql",
		"007_relay_controller_foundation.sql",
		"008_relay_controller_session.sql",
	} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := upgrade.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := upgrade.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"001_foundation.sql",
		"002_github_connections.sql",
		"003_application_sources.sql",
		"004_release_snapshots.sql",
		"005_application_configuration.sql",
		"006_compose_deployment_runtime.sql",
		"007_relay_controller_foundation.sql",
		"008_relay_controller_session.sql",
	} {
		if _, err := upgrade.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	if err := Migrate(upgrade); err != nil {
		t.Fatalf("upgrade migration: %v", err)
	}
	assertRelayOutboundPendingReplayIndex(t, upgrade)
}

func assertRelayOutboundPendingReplayIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	var statement string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='relay_outbound_pending_replay'`).Scan(&statement); err != nil {
		t.Fatalf("pending replay index: %v", err)
	}
	statement = strings.Join(strings.Fields(strings.ToLower(statement)), " ")
	if statement != "create index relay_outbound_pending_replay on relay_outbound_commands(controller_id,sent_at,message_id) where state='prepared'" {
		t.Fatalf("pending replay index definition = %q", statement)
	}
}
