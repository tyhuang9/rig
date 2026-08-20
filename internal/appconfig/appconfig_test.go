package appconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/secretfile"
)

const configTestApp = "11111111-1111-1111-1111-111111111111"

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,'app','App','draft',datetime('now'),datetime('now'))`, configTestApp); err != nil {
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
	configuration, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "apps", configTestApp, "configuration", "00000000-0000-0000-0000-000000000000.secret")
	if err := secretfile.WriteNew(orphan, "orphan", []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
	referenced := store.bundlePath(configTestApp, configuration.RevisionID)
	tampered, err := json.Marshal(bundle{Version: 1, ApplicationID: configTestApp, RevisionID: configuration.RevisionID, RevisionNumber: 1, Entries: map[string]bundleEntry{"MODE": {Sensitive: true, Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := secretfile.Write(referenced, purpose(configTestApp, configuration.RevisionID), tampered); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(ctx); err == nil {
		t.Fatal("bundle/SQLite metadata mismatch was accepted")
	}
}

func TestRecoverRejectsSymlinkedOrUnrecognizedCleanupTargets(t *testing.T) {
	store, root := testStore(t)
	external := t.TempDir()
	sentinel := filepath.Join(external, "00000000-0000-0000-0000-000000000000.secret")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(root, "apps", configTestApp)
	if err := os.MkdirAll(appRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(appRoot, "configuration")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := store.Recover(context.Background()); err == nil {
		t.Fatal("symlinked configuration directory was accepted")
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "outside" {
		t.Fatalf("external sentinel changed: %q %v", content, err)
	}
}

func TestRecoverOnlyRemovesExactRecognizedOrphans(t *testing.T) {
	store, root := testStore(t)
	configurationRoot := filepath.Join(root, "apps", configTestApp, "configuration")
	if err := os.MkdirAll(configurationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(configurationRoot, ".hostd-secret-AbCd1234")
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("recognized temp remains: %v", err)
	}
	unrecognized := filepath.Join(configurationRoot, "notes.secret")
	if err := os.WriteFile(unrecognized, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background()); err == nil {
		t.Fatal("unrecognized secret-looking file was removed")
	}
	if content, err := os.ReadFile(unrecognized); err != nil || string(content) != "keep" {
		t.Fatalf("unrecognized file changed: %q %v", content, err)
	}
}

func TestRecoverFailsClosedWhenReferencedBundleIsMissing(t *testing.T) {
	store, _ := testStore(t)
	configuration, err := store.Replace(context.Background(), configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.bundlePath(configTestApp, configuration.RevisionID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background()); err == nil {
		t.Fatal("missing referenced bundle was accepted")
	}
}

func TestRecoverRejectsDuplicateBundleObjectKeys(t *testing.T) {
	store, _ := testStore(t)
	configuration, err := store.Replace(context.Background(), configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"version":1,"version":1,"applicationId":"` + configTestApp + `","revisionId":"` + configuration.RevisionID + `","revisionNumber":1,"entries":{"MODE":{"sensitive":false,"value":"prod"}}}`)
	if err := secretfile.Write(store.bundlePath(configTestApp, configuration.RevisionID), purpose(configTestApp, configuration.RevisionID), document); err != nil {
		t.Fatal(err)
	}
	if err := store.Recover(context.Background()); err == nil {
		t.Fatal("duplicate JSON object keys were accepted")
	}
}

func TestConcurrentReplaceUsesCompareAndSwap(t *testing.T) {
	store, _ := testStore(t)
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(value string) {
			defer wait.Done()
			<-start
			_, err := store.Replace(context.Background(), configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "VALUE", Value: value}}})
			errorsOut <- err
		}(string(rune('a' + i)))
	}
	close(start)
	wait.Wait()
	close(errorsOut)
	successes, conflicts := 0, 0
	for err := range errorsOut {
		if err == nil {
			successes++
		} else if IsCode(err, "configuration_conflict") {
			conflicts++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestDatabaseFailureAfterBundleWriteCleansOnlyNewFile(t *testing.T) {
	store, root := testStore(t)
	_, err := store.Replace(context.Background(), configTestApp, "missing-user", ReplaceInput{ExpectedRevisionNumber: 0, Secrets: []ValueInput{{Key: "TOKEN", Value: "secret"}}})
	if err == nil {
		t.Fatal("invalid actor foreign key was accepted")
	}
	configurationRoot := filepath.Join(root, "apps", configTestApp, "configuration")
	entries, readErr := os.ReadDir(configurationRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("bundle residue=%v", entries)
	}
	configuration, getErr := store.Get(context.Background(), configTestApp)
	if getErr != nil || configuration.RevisionNumber != 0 {
		t.Fatalf("head advanced: %+v %v", configuration, getErr)
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
		if _, err := store.Replace(ctx, configTestApp, "", input); !IsCode(err, "invalid_configuration") {
			t.Fatalf("input=%+v err=%v", input, err)
		}
	}
}

func TestReplaceKeepsValuesOutOfSQLiteAndMasksSecrets(t *testing.T) {
	store, root := testStore(t)
	ctx := context.Background()
	initial, err := store.Get(ctx, configTestApp)
	if err != nil || initial.RevisionNumber != 0 || initial.RevisionID != "" {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	got, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "PUBLIC_URL", Value: "https://example.test/sentinel-public"}}, Secrets: []ValueInput{{Key: "API_TOKEN", Value: "sentinel-secret-token"}}})
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
	databaseFiles, err := filepath.Glob(filepath.Join(root, "control.db*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, databaseFile := range databaseFiles {
		databaseBytes, err := os.ReadFile(databaseFile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(databaseBytes), "sentinel-secret-token") || strings.Contains(string(databaseBytes), "sentinel-public") {
			t.Fatalf("configuration value found in SQLite file %s", filepath.Base(databaseFile))
		}
	}
	for _, query := range []string{
		`SELECT metadata_json FROM audit_events`,
		`SELECT input_json || checkpoint_json || COALESCE(error_detail,'') FROM jobs`,
		`SELECT message || metadata_json FROM job_events`,
	} {
		rows, err := store.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if strings.Contains(text, "sentinel-secret-token") || strings.Contains(text, "sentinel-public") {
				rows.Close()
				t.Fatalf("configuration value found in persisted operational metadata: %s", text)
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0}); !IsCode(err, "configuration_conflict") {
		t.Fatalf("conflict err=%v", err)
	}
	if err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReplacePreservesStoredSecretAndSupportsExplicitRemoval(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	first, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Secrets: []ValueInput{{Key: "TOKEN", Value: "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: first.RevisionNumber, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 2 {
		t.Fatalf("preserved entries=%+v", second.Entries)
	}
	third, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: second.RevisionNumber, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}, Remove: []string{"TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Entries) != 1 || third.Entries[0].Key != "MODE" {
		t.Fatalf("removed entries=%+v", third.Entries)
	}
}
