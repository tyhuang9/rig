package applifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
)

func TestPositiveFenceValidatorRequiresExactRepositoryDatabaseAndTransaction(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	validator, err := NewPositiveFenceValidator(fixture.repo)
	if err != nil || !validator.ConfiguredFor(fixture.db) {
		t.Fatalf("validator=%#v configured=%t error=%v", validator, validator != nil && validator.ConfiguredFor(fixture.db), err)
	}
	foreign := newOperationFixture(t, 1)
	if validator.ConfiguredFor(foreign.db) {
		t.Fatal("validator accepted a foreign database")
	}
	if _, err := NewPositiveFenceValidator(nil); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("nil repository error=%v", err)
	}
	if _, err := validator.PendingStartExistsTx(context.Background(), nil); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("nil transaction error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := validator.PendingStartExistsTx(ctx, tx); !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestPositiveFenceValidatorUsesCallerTransactionForIntentAndReceipt(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	fixture.db.SetMaxOpenConns(2)
	request := fixture.request(Start, BaselineStopped, "positive-fence-intent", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil || !accepted.Created {
		t.Fatalf("accepted=%#v error=%v", accepted, err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
	validator, err := NewPositiveFenceValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	startRequest := effectRequest(accepted.Operation, BoundaryStart, accepted.Operation.Pins.Components[0].Name, EffectCommand)
	intent, created, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, startRequest)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("begin start created=%t error=%v", created, err)
	}
	pending, err := validator.PendingStartExistsTx(context.Background(), tx)
	if err != nil || !pending {
		_ = tx.Rollback()
		t.Fatalf("transaction-local intent pending=%t error=%v", pending, err)
	}
	standalone, err := fixture.repo.PendingStartExists(context.Background())
	if err != nil || standalone {
		_ = tx.Rollback()
		t.Fatalf("standalone saw uncommitted intent pending=%t error=%v", standalone, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	standalone, err = fixture.repo.PendingStartExists(context.Background())
	if err != nil || !standalone {
		t.Fatalf("committed intent pending=%t error=%v", standalone, err)
	}
	tx, err = fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	proof := effectProof(accepted.Operation, intent, OutcomeContainerRunning)
	if _, observed, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err != nil || !observed {
		_ = tx.Rollback()
		t.Fatalf("observe start created=%t error=%v", observed, err)
	}
	pending, err = validator.PendingStartExistsTx(context.Background(), tx)
	if err != nil || pending {
		_ = tx.Rollback()
		t.Fatalf("transaction-local receipt pending=%t error=%v", pending, err)
	}
	standalone, err = fixture.repo.PendingStartExists(context.Background())
	if err != nil || !standalone {
		_ = tx.Rollback()
		t.Fatalf("standalone lost committed pending intent pending=%t error=%v", standalone, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestPositiveFenceValidatorUsesCallerTransactionForOperationAndKeyPage(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	fixture.db.SetMaxOpenConns(2)
	request := fixture.request(Start, BaselineStopped, "positive-fence-operation", 0)
	job := insertOperationJob(t, fixture, request)
	validator, err := NewPositiveFenceValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	operation, created, err := fixture.repo.AcceptOperationTx(context.Background(), tx, job, request, fixture.pins)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("accept operation created=%t error=%v", created, err)
	}
	if _, err := tx.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, job.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	job.Status, job.Phase, job.Attempt = "running", "running", 1
	claim, err := WorkerClaimFor(job)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	withdrawRequest := effectRequest(operation, BoundaryWithdraw, "", EffectObservationOnly)
	withdraw, created, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, operation, withdrawRequest)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("begin withdraw created=%t error=%v", created, err)
	}
	if _, observed, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, claim, withdraw, effectProof(operation, withdraw, OutcomeRoutesAbsent)); err != nil || !observed {
		_ = tx.Rollback()
		t.Fatalf("observe withdraw created=%t error=%v", observed, err)
	}
	component := operation.Pins.Components[0].Name
	if _, created, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, claim, operation, effectRequest(operation, BoundaryStart, component, EffectCommand)); err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("begin start created=%t error=%v", created, err)
	}
	pending, err := validator.PendingStartExistsTx(context.Background(), tx)
	if err != nil || !pending {
		_ = tx.Rollback()
		t.Fatalf("transaction-local operation pending=%t error=%v", pending, err)
	}
	standalone, err := fixture.repo.PendingStartExists(context.Background())
	if err != nil || standalone {
		_ = tx.Rollback()
		t.Fatalf("standalone saw uncommitted operation pending=%t error=%v", standalone, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestPositiveFenceValidatorRejectsMalformedCanonicalHistory(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	request := fixture.request(Start, BaselineStopped, "positive-fence-malformed", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil || !accepted.Created {
		t.Fatalf("accepted=%#v error=%v", accepted, err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	writeEffect(t, fixture, claim, accepted.Operation, BoundaryWithdraw, "", EffectObservationOnly, OutcomeRoutesAbsent)
	intent, created, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryStart, accepted.Operation.Pins.Components[0].Name, EffectCommand)
	if err != nil || !created {
		t.Fatalf("begin start created=%t error=%v", created, err)
	}
	if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_intent_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE application_lifecycle_effect_intents SET request_digest=? WHERE job_id=? AND boundary='start'`, strings.Repeat("0", 64), intent.JobID); err != nil {
		t.Fatal(err)
	}
	validator, err := NewPositiveFenceValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := validator.PendingStartExistsTx(context.Background(), tx); !errors.Is(err, deploymentexecution.ErrAuthorityInvalid) || !errors.Is(err, ErrInvalidEffect) {
		t.Fatalf("malformed history error=%v", err)
	}
}

func TestPositiveFenceValidatorRejectsOrphanStartAuthority(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_intent_valid_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO application_lifecycle_effect_intents(
		job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
	) VALUES('orphan-lifecycle-job','start','api',1,1,'{}',?,?)`, strings.Repeat("0", 64), operationStamp()); err != nil {
		t.Fatal(err)
	}
	validator, err := NewPositiveFenceValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := validator.PendingStartExistsTx(context.Background(), tx); !errors.Is(err, deploymentexecution.ErrAuthorityInvalid) || !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("orphan start error=%v", err)
	}
}
