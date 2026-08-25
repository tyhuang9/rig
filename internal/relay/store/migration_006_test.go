package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWSSHardeningMigrationIsMirroredAndIndexesBoundedMaintenance(t *testing.T) {
	embedded, err := migrationFiles.ReadFile("migrations/006_wss_hardening.sql")
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "relay-migrations", "006_wss_hardening.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, public) {
		t.Fatal("pending access index migration mirror differs")
	}
	sql := string(embedded)
	for _, required := range []string{
		"CREATE TABLE relay_topology_lock_shards",
		"CHECK (shard_id>=0 AND shard_id<64)",
		"FROM generate_series(0,63)",
		"ON relay_access_events(controller_id, observed_at, event_id)",
		"WHERE decision IS NULL",
		"ADD COLUMN retired_at timestamptz",
		"SET retired_at=CURRENT_TIMESTAMP",
		"retired_generation IS NULL AND retired_at IS NULL",
		"retired_generation IS NOT NULL AND retired_at IS NOT NULL AND retired_at>=created_at",
		"ON relay_subscriptions(retired_at, subscription_id)",
		"ON relay_source_delivery_targets(persisted_at, delivery_id, subscription_id)",
		"ON relay_desired_states(observed_at, subscription_id)",
		"ON relay_desired_states(delivery_id)",
		"ON relay_subscription_set_items(created_at, controller_id, set_generation, subscription_id)",
		"ON relay_subscription_set_items(subscription_id)",
		"ON relay_access_events(decided_at, event_id)",
		"WHERE decision IS NOT NULL",
		"ON relay_ignored_deliveries(persisted_at, delivery_id)",
		"ON relay_github_deliveries(persisted_at, delivery_id)",
		"ON relay_recovery_deliveries(recovered_at, delivery_id)",
		"ON relay_recovery_delivery_attempts(delivery_id, delivery_number)",
		"ON relay_controller_leases(expires_at, controller_id)",
		"ON relay_session_commands(applied_at, session_id, controller_id, message_id)",
		"ON relay_session_messages(seen_at, session_id, message_id)",
		"ON relay_sessions(expires_at, session_id)",
		"ON relay_sessions(revoked_at, session_id)",
		"ON relay_controller_keys(revoked_at, key_id)",
		"ON relay_controller_keys(rotation_session_id)",
		"ON relay_wss_challenges(expires_at, session_id)",
		"ON relay_wss_challenges(consumed_at, session_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("pending access index missing %q", required)
		}
	}
}
