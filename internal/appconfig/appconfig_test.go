package appconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/secretfile"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES('app','app','App','draft',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	store, err := New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func TestRecoverRemovesRecognizedOrphansAndFailsClosedOnCorruption(t *testing.T) {
	store, root := testStore(t)
	ctx := context.Background()
	configuration, err := store.Replace(ctx, "app", "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "apps", "app", "configuration", "00000000-0000-0000-0000-000000000000.secret")
	if err := secretfile.WriteNew(orphan, "orphan", []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
	referenced := store.bundlePath("app", configuration.RevisionID)
	if err := os.WriteFile(referenced, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err == nil {
		t.Fatal("corrupt referenced bundle was accepted")
	}
}

func TestReplaceRejectsAmbiguousAndInvalidInputs(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	tests := []ReplaceInput{
		{ExpectedRevisionNumber: 0, Secrets: []ValueInput{{Key: "TOKEN", Value: ""}}},
		{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "TOKEN", Value: "public"}}, Remove: []string{"TOKEN"}},
		{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "BAD-KEY", Value: "value"}}},
		{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "MODE", Value: "bad\x00value"}}},
	}
	for _, input := range tests {
		if _, err := store.Replace(ctx, "app", "", input); !IsCode(err, "invalid_configuration") {
			t.Fatalf("input=%+v err=%v", input, err)
		}
	}
}

func TestReplaceKeepsValuesOutOfSQLiteAndMasksSecrets(t *testing.T) {
	store, root := testStore(t)
	ctx := context.Background()
	initial, err := store.Get(ctx, "app")
	if err != nil || initial.RevisionNumber != 0 || initial.RevisionID != "" {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	got, err := store.Replace(ctx, "app", "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "PUBLIC_URL", Value: "https://example.test/sentinel-public"}}, Secrets: []ValueInput{{Key: "API_TOKEN", Value: "sentinel-secret-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.RevisionNumber != 1 || len(got.Entries) != 2 {
		t.Fatalf("configuration=%+v", got)
	}
	for _, entry := range got.Entries {
		if entry.Key == "API_TOKEN" && (!entry.Sensitive || entry.Value != "") {
			t.Fatalf("secret leaked: %+v", entry)
		}
	}
	databaseBytes, err := os.ReadFile(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(databaseBytes), "sentinel-secret-token") || strings.Contains(string(databaseBytes), "sentinel-public") {
		t.Fatal("configuration value found in SQLite")
	}
	if _, err := store.Replace(ctx, "app", "", ReplaceInput{ExpectedRevisionNumber: 0}); !IsCode(err, "configuration_conflict") {
		t.Fatalf("conflict err=%v", err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReplacePreservesStoredSecretAndSupportsExplicitRemoval(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	first, err := store.Replace(ctx, "app", "", ReplaceInput{ExpectedRevisionNumber: 0, Secrets: []ValueInput{{Key: "TOKEN", Value: "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Replace(ctx, "app", "", ReplaceInput{ExpectedRevisionNumber: first.RevisionNumber, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 2 {
		t.Fatalf("preserved entries=%+v", second.Entries)
	}
	third, err := store.Replace(ctx, "app", "", ReplaceInput{ExpectedRevisionNumber: second.RevisionNumber, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}, Remove: []string{"TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Entries) != 1 || third.Entries[0].Key != "MODE" {
		t.Fatalf("removed entries=%+v", third.Entries)
	}
}
