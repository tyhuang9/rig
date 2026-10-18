package deploymentbinding

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type validatorStub struct {
	db                *sql.DB
	historicalErr     error
	currentCompletion Completion
	currentErr        error
	unresolved        bool
	unresolvedErr     error
	historicalCalls   int
	currentCalls      int
	unresolvedCalls   int
}

func (v *validatorStub) ConfiguredFor(db *sql.DB) bool { return v != nil && v.db == db }
func (v *validatorStub) HistoricalBindingTx(context.Context, *sql.Tx, OperationRef) error {
	v.historicalCalls++
	return v.historicalErr
}
func (v *validatorStub) CurrentOperationTx(context.Context, *sql.Tx, OperationRef) (Completion, error) {
	v.currentCalls++
	return v.currentCompletion, v.currentErr
}
func (v *validatorStub) UnresolvedHistoryTx(context.Context, *sql.Tx, string) (bool, error) {
	v.unresolvedCalls++
	return v.unresolved, v.unresolvedErr
}

func TestRepositoryConfigurationRejectsNilTypedNilAndForeignValidator(t *testing.T) {
	db := newBindingTestDB(t)
	other := newBindingTestDB(t)
	var typedNil *validatorStub
	for name, validator := range map[string]LifecycleTxValidator{
		"nil":              nil,
		"typed nil":        typedNil,
		"foreign database": &validatorStub{db: other},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(db, validator); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("configuration error=%v", err)
			}
		})
	}
}

func TestRepositoryLegacyBindingIsImmutableCurrentAndThenStale(t *testing.T) {
	db := newBindingTestDB(t)
	validator := &validatorStub{db: db, currentCompletion: CompletionIncomplete}
	repository, err := New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	insertBindingTestAppJob(t, db, "legacy-app", "legacy-job", 0, "", "", "queued")
	createdAt := time.Date(2026, 10, 18, 12, 0, 0, 1, time.UTC)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := repository.InsertNewTx(context.Background(), tx, "legacy-job", createdAt)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if binding.Mode() != ModeLegacyUninitialized || binding.DesiredRevision() != 0 || binding.AuthorizingLifecycleJobID() != "" {
		t.Fatalf("legacy binding=%#v", binding)
	}
	loaded, err := repository.Read(context.Background(), binding.JobID())
	if err != nil || !loaded.Equal(binding) {
		t.Fatalf("loaded=%#v error=%v", loaded, err)
	}
	classification, err := repository.Check(context.Background(), binding)
	if err != nil || classification != ClassificationCurrent {
		t.Fatalf("current classification=%d error=%v", classification, err)
	}
	if validator.historicalCalls != 0 || validator.currentCalls != 0 || validator.unresolvedCalls != 0 {
		t.Fatalf("legacy validator calls historical=%d current=%d unresolved=%d", validator.historicalCalls, validator.currentCalls, validator.unresolvedCalls)
	}

	if _, err := db.Exec(`UPDATE application_desired_lifecycle_heads
		SET desired_state='running',revision_number=1,updated_at=? WHERE app_id='legacy-app'`, bindingTestStamp()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_lifecycle_operations(job_id,app_id,desired_revision)
		VALUES('later-operation','legacy-app',1)`); err != nil {
		t.Fatal(err)
	}
	classification, err = repository.Check(context.Background(), binding)
	if err != nil || classification != ClassificationStale {
		t.Fatalf("later unfinished classification=%d error=%v", classification, err)
	}
	if validator.currentCalls != 1 || validator.unresolvedCalls != 0 {
		t.Fatalf("later operation calls current=%d unresolved=%d", validator.currentCalls, validator.unresolvedCalls)
	}

	if _, err := db.Exec(`DELETE FROM applications WHERE id='legacy-app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Read(context.Background(), binding.JobID()); err != nil {
		t.Fatalf("historical read after app deletion: %v", err)
	}
	classification, err = repository.Check(context.Background(), binding)
	if err != nil || classification != ClassificationStale {
		t.Fatalf("deleted app classification=%d error=%v", classification, err)
	}
}

func TestRepositoryRunningBindingRequiresCompleteCurrentAuthorityAndHistory(t *testing.T) {
	tests := []struct {
		name       string
		completion Completion
		unresolved bool
		want       error
	}{
		{name: "incomplete current operation", completion: CompletionIncomplete, want: ErrLifecycleUnresolved},
		{name: "older unresolved history", completion: CompletionComplete, unresolved: true, want: ErrLifecycleUnresolved},
		{name: "complete authority", completion: CompletionComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newBindingTestDB(t)
			validator := &validatorStub{db: db, currentCompletion: test.completion, unresolved: test.unresolved}
			repository, err := New(db, validator)
			if err != nil {
				t.Fatal(err)
			}
			insertBindingTestAppJob(t, db, "running-app", "deploy-job", 1, "running", "operation-1", "queued")
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			binding, insertErr := repository.InsertNewTx(context.Background(), tx, "deploy-job", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
			if test.want != nil {
				_ = tx.Rollback()
				if !errors.Is(insertErr, test.want) {
					t.Fatalf("insert error=%v want=%v", insertErr, test.want)
				}
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("binding count=%d error=%v", count, err)
				}
				return
			}
			if insertErr != nil {
				_ = tx.Rollback()
				t.Fatal(insertErr)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if binding.Mode() != ModeRunning || binding.DesiredRevision() != 1 || binding.AuthorizingLifecycleJobID() != "operation-1" {
				t.Fatalf("running binding=%#v", binding)
			}
			classification, err := repository.Check(context.Background(), binding)
			if err != nil || classification != ClassificationCurrent {
				t.Fatalf("classification=%d error=%v", classification, err)
			}
			if _, err := db.Exec(`DELETE FROM application_desired_lifecycle_heads WHERE app_id='running-app'`); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.Read(context.Background(), binding.JobID()); err != nil {
				t.Fatalf("historical read unexpectedly required mutable head: %v", err)
			}
			if classification, err := repository.Check(context.Background(), binding); !errors.Is(err, ErrInvalidBinding) || classification != 0 {
				t.Fatalf("present app with missing head classification=%d error=%v", classification, err)
			}
		})
	}
}

func TestRepositoryValidatesHistoricalGraphBeforeNewerHeadStaleness(t *testing.T) {
	db := newBindingTestDB(t)
	validator := &validatorStub{db: db, currentCompletion: CompletionComplete}
	repository, err := New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	insertBindingTestAppJob(t, db, "history-app", "deploy-job", 1, "running", "operation-1", "queued")
	tx, _ := db.Begin()
	binding, err := repository.InsertNewTx(context.Background(), tx, "deploy-job", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE application_desired_lifecycle_heads
		SET revision_number=2,desired_state='running',updated_at=? WHERE app_id='history-app'`, bindingTestStamp()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_lifecycle_operations(job_id,app_id,desired_revision)
		VALUES('operation-2','history-app',2)`); err != nil {
		t.Fatal(err)
	}
	validator.historicalErr = ErrInvalidBinding
	validator.currentCalls = 0
	if classification, err := repository.Check(context.Background(), binding); !errors.Is(err, ErrInvalidBinding) || classification != 0 {
		t.Fatalf("classification=%d error=%v", classification, err)
	}
	if validator.currentCalls != 0 {
		t.Fatalf("current head checked before corrupt historical graph calls=%d", validator.currentCalls)
	}
	validator.historicalErr = nil
	validator.currentCompletion = CompletionIncomplete
	if classification, err := repository.Check(context.Background(), binding); err != nil || classification != ClassificationStale {
		t.Fatalf("newer unfinished operation classification=%d error=%v", classification, err)
	}
	if validator.unresolvedCalls != 1 {
		t.Fatalf("newer unfinished operation unexpectedly checked whole-history fence calls=%d", validator.unresolvedCalls)
	}
}

func TestRepositoryRejectsForeignBindingAndCancellation(t *testing.T) {
	db := newBindingTestDB(t)
	validator := &validatorStub{db: db, currentCompletion: CompletionComplete}
	first, _ := New(db, validator)
	second, _ := New(db, validator)
	insertBindingTestAppJob(t, db, "foreign-app", "foreign-job", 0, "", "", "queued")
	tx, _ := db.Begin()
	binding, err := first.InsertNewTx(context.Background(), tx, "foreign-job", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Check(context.Background(), binding); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("foreign binding error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := first.Read(ctx, binding.JobID()); !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func newBindingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bindings.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE applications(id TEXT PRIMARY KEY);
		CREATE TABLE jobs(
			id TEXT PRIMARY KEY,type TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT NOT NULL,
			status TEXT NOT NULL,phase TEXT NOT NULL,attempt INTEGER NOT NULL DEFAULT 0,
			started_at TEXT,finished_at TEXT
		);
		CREATE TABLE application_desired_lifecycle_heads(
			app_id TEXT PRIMARY KEY,desired_state TEXT,revision_number INTEGER NOT NULL,updated_at TEXT
		);
		CREATE TABLE application_desired_lifecycle_transitions(app_id TEXT,revision_number INTEGER);
		CREATE TABLE application_lifecycle_operations(job_id TEXT PRIMARY KEY,app_id TEXT,desired_revision INTEGER);
		CREATE TABLE application_deployment_lifecycle_bindings(
			job_id TEXT PRIMARY KEY,app_id TEXT NOT NULL,desired_revision INTEGER NOT NULL,mode TEXT NOT NULL,
			authorizing_lifecycle_job_id TEXT,created_at TEXT NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertBindingTestAppJob(t *testing.T, db *sql.DB, appID, jobID string, revision int64, state, operationID, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id) VALUES(?)`, appID); err != nil {
		t.Fatal(err)
	}
	var desired, updated any
	if revision > 0 {
		desired, updated = state, bindingTestStamp()
	}
	if _, err := db.Exec(`INSERT INTO application_desired_lifecycle_heads(app_id,desired_state,revision_number,updated_at)
		VALUES(?,?,?,?)`, appID, desired, revision, updated); err != nil {
		t.Fatal(err)
	}
	if operationID != "" {
		if _, err := db.Exec(`INSERT INTO application_lifecycle_operations(job_id,app_id,desired_revision) VALUES(?,?,?)`, operationID, appID, revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt)
		VALUES(?,'deploy','application',?,?,?,0)`, jobID, appID, status, status); err != nil {
		t.Fatal(err)
	}
}

func bindingTestStamp() string {
	return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
}
