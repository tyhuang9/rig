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

func TestExportRevisionForExecutionPinsExactRevision(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	empty, err := store.ExportRevisionForExecution(ctx, configTestApp, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Environment) == 0 || empty.RevisionID != "" || empty.RevisionNumber != 0 {
		t.Fatalf("unexpected empty revision export: %#v", empty)
	}

	first, err := store.Replace(ctx, configTestApp, "", ReplaceInput{
		ExpectedRevisionNumber: 0,
		Variables: []ValueInput{
			{Key: "Z_LAST", Value: "hash# equals= double\" slash\\ carriage\rreturn"},
			{Key: "MODE", Value: "first'$VALUE\nnext"},
		},
		Secrets: []ValueInput{{Key: "TOKEN", Value: "secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Replace(ctx, configTestApp, "", ReplaceInput{
		ExpectedRevisionNumber: first.RevisionNumber,
		Variables:              []ValueInput{{Key: "MODE", Value: "second"}},
		Remove:                 []string{"TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}

	exact, err := store.ExportRevisionForExecution(ctx, configTestApp, first.RevisionID, first.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	wantExact := "# hostd application configuration\nMODE='first\\'$VALUE\nnext'\nTOKEN='secret'\nZ_LAST='hash# equals= double\" slash\\ carriage\rreturn'\n"
	if string(exact.Environment) != wantExact {
		t.Fatalf("exact revision environment=%q want=%q", exact.Environment, wantExact)
	}
	if len(exact.SecretOrigins) != 1 || string(exact.SecretOrigins[0].Key) != "TOKEN" || string(exact.SecretOrigins[0].Value) != "secret" || exact.SecretOrigins[0].RevisionID != first.RevisionID || exact.SecretOrigins[0].RevisionNumber != first.RevisionNumber {
		t.Fatalf("unexpected exact secret origins: %#v", exact.SecretOrigins)
	}
	current, err := store.ExportCurrentForExecution(ctx, configTestApp)
	if err != nil {
		t.Fatal(err)
	}
	if current.RevisionID != second.RevisionID || current.RevisionNumber != second.RevisionNumber || strings.Contains(string(current.Environment), "secret") || !strings.Contains(string(current.Environment), "MODE='second'") {
		t.Fatalf("unexpected current revision export: %#v", current)
	}

	empty.Clear()
	exact.Clear()
	current.Clear()
}

func TestExportRevisionForExecutionReturnsIndependentCallerOwnedBytes(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	revision, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Secrets: []ValueInput{{Key: "TOKEN", Value: "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ExportRevisionForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ExportRevisionForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	first.Clear()
	if strings.ContainsRune(string(second.Environment), '\x00') || !strings.Contains(string(second.Environment), "TOKEN='secret'") {
		t.Fatalf("clearing one export changed another: %q", second.Environment)
	}
	if len(second.SecretOrigins) != 1 || string(second.SecretOrigins[0].Key) != "TOKEN" || string(second.SecretOrigins[0].Value) != "secret" {
		t.Fatalf("unexpected independent secret origins: %#v", second.SecretOrigins)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "Environment") {
		t.Fatalf("execution environment was JSON serialized: %s", encoded)
	}
	originKey := second.SecretOrigins[0].Key
	originValue := second.SecretOrigins[0].Value
	second.Clear()
	if second.Environment != nil || second.SecretOrigins != nil || !allZero(originKey) || !allZero(originValue) {
		t.Fatalf("execution configuration did not clear owned buffers")
	}
}

func TestExportRevisionForExecutionRejectsMismatchedIdentity(t *testing.T) {
	store, _ := testStore(t)
	revision, err := store.Replace(context.Background(), configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "MODE", Value: "prod"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		id     string
		number int64
	}{{"", 1}, {"00000000-0000-0000-0000-000000000000", 0}, {"00000000-0000-0000-0000-000000000000", 1}, {revision.RevisionID, revision.RevisionNumber + 1}} {
		result, err := store.ExportRevisionForExecution(context.Background(), configTestApp, request.id, request.number)
		if !IsCode(err, "configuration_unavailable") {
			result.Clear()
			t.Fatalf("id=%q number=%d error=%v", request.id, request.number, err)
		}
	}
}

func TestExportRevisionForExecutionFailsClosedOnCorruptBundle(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	revision, err := store.Replace(ctx, configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Secrets: []ValueInput{{Key: "TOKEN", Value: "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bundle{Version: 1, ApplicationID: configTestApp, RevisionID: revision.RevisionID, RevisionNumber: revision.RevisionNumber, Entries: map[string]bundleEntry{"TOKEN": {Sensitive: false, Value: "secret"}}}
	plaintext, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatal(err)
	}
	if err := secretfile.Write(store.bundlePath(configTestApp, revision.RevisionID), purpose(configTestApp, revision.RevisionID), plaintext); err != nil {
		t.Fatal(err)
	}
	clear(plaintext)
	result, err := store.ExportRevisionForExecution(ctx, configTestApp, revision.RevisionID, revision.RevisionNumber)
	result.Clear()
	if !IsCode(err, "configuration_unavailable") {
		t.Fatalf("corrupt exact revision error = %v", err)
	}
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
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

func TestIndependentStoresMapUniqueRevisionRaceToConflict(t *testing.T) {
	first, root := testStore(t)
	second, err := New(first.db, root)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	hook := func() { entered <- struct{}{}; <-release }
	first.beforeTransaction = hook
	second.beforeTransaction = hook
	type result struct {
		configuration Configuration
		err           error
	}
	results := make(chan result, 2)
	for index, store := range []*Store{first, second} {
		go func(value string, candidate *Store) {
			configuration, err := candidate.Replace(context.Background(), configTestApp, "", ReplaceInput{ExpectedRevisionNumber: 0, Variables: []ValueInput{{Key: "VALUE", Value: value}}})
			results <- result{configuration, err}
		}(string(rune('a'+index)), store)
	}
	<-entered
	<-entered
	close(release)
	successes, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		result := <-results
		if result.err == nil && result.configuration.RevisionNumber == 1 {
			successes++
		} else if IsCode(result.err, "configuration_conflict") {
			conflicts++
		} else {
			t.Fatalf("unexpected result: %+v err=%v", result.configuration, result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var revisions int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM application_configuration_revisions WHERE app_id=?`, configTestApp).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("revisions=%d err=%v", revisions, err)
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
