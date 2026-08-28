package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollmentCapacityRetentionMigrationIsMirroredAndBounded(t *testing.T) {
	embedded, err := migrationFiles.ReadFile("migrations/007_enrollment_capacity_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "relay-migrations", "007_enrollment_capacity_retention.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, public) {
		t.Fatal("migration 007 mirror differs")
	}
	sql := string(embedded)
	for _, required := range []string{"relay_enrollment_active_capacity", "status IN ('pending','state_claimed')", "relay_enrollment_terminal_retention", "status IN ('authorized','failed','expired')"} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration 007 missing %q", required)
		}
	}
}
