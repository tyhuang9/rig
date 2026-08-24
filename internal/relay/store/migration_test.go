package store

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRelayMigrationsAreByteMirroredAndChecksummed(t *testing.T) {
	embedded := migrationNames(t, migrationFiles, "migrations")
	publicRoot := filepath.Join("..", "..", "..", "db", "relay-migrations")
	public := migrationNames(t, os.DirFS(publicRoot), ".")
	if strings.Join(embedded, "\n") != strings.Join(public, "\n") {
		t.Fatalf("embedded=%v public=%v", embedded, public)
	}
	for _, name := range embedded {
		left, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(publicRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(left, right) {
			t.Fatalf("migration %s differs", name)
		}
		if sha256.Sum256(left) != sha256.Sum256(right) {
			t.Fatalf("migration %s checksum differs", name)
		}
	}
}

func TestRelaySchemaContainsRequiredDurabilityInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/001_relay_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	required := []string{
		"FOREIGN KEY (controller_id, installation_id, repository_id) REFERENCES relay_bindings",
		"CREATE UNIQUE INDEX relay_one_active_key", "CREATE UNIQUE INDEX relay_one_pending_key",
		"UNIQUE (controller_id, key_id, client_nonce)", "FOREIGN KEY (session_id, controller_id, key_id)",
		"FOREIGN KEY (session_id, controller_id) REFERENCES relay_sessions", "claim_fence bigint NOT NULL",
		"decision = 'acked' AND decision_code IS NULL", "decision = 'rejected' AND decision_code IS NOT NULL",
		"subscription_id uuid PRIMARY KEY REFERENCES relay_subscriptions", "CREATE TABLE relay_source_delivery_targets",
	}
	for _, snippet := range required {
		if !strings.Contains(sql, snippet) {
			t.Errorf("missing invariant %q", snippet)
		}
	}
}

func TestRelaySchemaExcludesForbiddenProductAndSecretData(t *testing.T) {
	body, err := migrationFiles.ReadFile("migrations/001_relay_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"app_name", "application_name", "source_body", "archive_body", "compose", "configuration", "variable", "secret", "raw_webhook", "webhook_body", "user_token", "access_token", "refresh_token", "oauth_token"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("schema contains forbidden material %q", forbidden)
		}
	}
}

func TestStoreQueriesContainLockingAndAuthorizationPredicates(t *testing.T) {
	files := []string{"delivery.go", "session.go", "subscriptions.go", "recovery.go", "migrate.go"}
	var source strings.Builder
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source.Write(body)
	}
	for _, required := range []string{"pg_advisory_xact_lock", "FOR UPDATE SKIP LOCKED", "FOR UPDATE OF l", "controller_id=$1", "b.revoked_at IS NULL", "pg_advisory_lock", "checksum mismatch"} {
		if !strings.Contains(source.String(), required) {
			t.Errorf("store queries missing %q", required)
		}
	}
}

func TestMigrationCleanupFailsClosedWhenLockStateIsUncertain(t *testing.T) {
	body, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, required := range []string{
		"Scan(&unlocked)",
		"if unlockErr == nil && unlocked",
		"advisory lock was not held",
		"discard = true",
		"conn.Hijack()",
		"underlying.Close(closeCtx)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("migration cleanup missing fail-closed behavior %q", required)
		}
	}
}

func migrationNames(t *testing.T, filesystem fs.FS, directory string) []string {
	t.Helper()
	entries, err := fs.ReadDir(filesystem, directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}
