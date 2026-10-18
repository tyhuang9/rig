package applifecycle

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/jobs"
)

func TestDeploymentBindingValidatorUsesCallerTransactionForCanonicalGraph(t *testing.T) {
	fixture := newOperationFixture(t, 2)
	request := fixture.request(Restart, BaselineRunning, "binding-validator", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil || !accepted.Created {
		t.Fatalf("accepted=%#v error=%v", accepted, err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	validator, err := NewDeploymentBindingValidator(fixture.repo)
	if err != nil || !validator.ConfiguredFor(fixture.db) {
		t.Fatalf("validator=%#v error=%v", validator, err)
	}
	ref := deploymentbinding.OperationRef{ApplicationID: fixture.appID, OperationID: accepted.Operation.JobID, DesiredRevision: 1}

	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	writeCompleteGraphTx(t, fixture.repo, tx, claim, accepted.Operation)
	if err := validator.HistoricalBindingTx(context.Background(), tx, ref); err != nil {
		_ = tx.Rollback()
		t.Fatalf("transaction-local historical validation: %v", err)
	}
	completion, err := validator.CurrentOperationTx(context.Background(), tx, ref)
	if err != nil || completion != deploymentbinding.CompletionComplete {
		_ = tx.Rollback()
		t.Fatalf("transaction-local completion=%d error=%v", completion, err)
	}
	unresolved, err := validator.UnresolvedHistoryTx(context.Background(), tx, fixture.appID)
	if err != nil || unresolved {
		_ = tx.Rollback()
		t.Fatalf("transaction-local unresolved=%t error=%v", unresolved, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx, err = fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	completion, err = validator.CurrentOperationTx(context.Background(), tx, ref)
	if err != nil || completion != deploymentbinding.CompletionIncomplete {
		_ = tx.Rollback()
		t.Fatalf("rolled-back completion=%d error=%v", completion, err)
	}
	if err := validator.HistoricalBindingTx(context.Background(), tx, ref); !errors.Is(err, deploymentbinding.ErrInvalidBinding) {
		_ = tx.Rollback()
		t.Fatalf("incomplete historical error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentBindingValidatorRejectsForeignIdentityAndMissingCompletedPredecessor(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	request := fixture.request(Start, BaselineRunning, "binding-complete", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	runCompleteGraph(t, fixture, claim, accepted.Operation)
	validator, err := NewDeploymentBindingValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	valid := deploymentbinding.OperationRef{ApplicationID: fixture.appID, OperationID: accepted.Operation.JobID, DesiredRevision: 1}
	for name, ref := range map[string]deploymentbinding.OperationRef{
		"foreign app":       {ApplicationID: "foreign", OperationID: valid.OperationID, DesiredRevision: valid.DesiredRevision},
		"wrong revision":    {ApplicationID: valid.ApplicationID, OperationID: valid.OperationID, DesiredRevision: 2},
		"missing operation": {ApplicationID: valid.ApplicationID, OperationID: "missing", DesiredRevision: valid.DesiredRevision},
	} {
		t.Run(name, func(t *testing.T) {
			tx, _ := fixture.db.Begin()
			err := validator.HistoricalBindingTx(context.Background(), tx, ref)
			_ = tx.Rollback()
			if !errors.Is(err, deploymentbinding.ErrInvalidBinding) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_receipt_retain`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM application_lifecycle_effect_receipts
		WHERE job_id=? AND boundary='health' AND component_name=''`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	tx, _ := fixture.db.Begin()
	err = validator.HistoricalBindingTx(context.Background(), tx, valid)
	_ = tx.Rollback()
	if !errors.Is(err, deploymentbinding.ErrInvalidBinding) {
		t.Fatalf("missing predecessor historical error=%v", err)
	}
}

func TestDeploymentBindingValidatorWholeHistoryFindsOlderUnresolvedOperation(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	firstRequest := fixture.request(Start, BaselineRunning, "first-unresolved", 0)
	first, err := fixture.repo.Accept(context.Background(), fixture.jobs, firstRequest, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, first.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	secondRequest := fixture.request(Restart, BaselineRunning, "second-complete", 1)
	second, err := fixture.repo.Accept(context.Background(), fixture.jobs, secondRequest, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, second.Operation.JobID)
	runCompleteGraph(t, fixture, claim, second.Operation)
	validator, _ := NewDeploymentBindingValidator(fixture.repo)
	tx, _ := fixture.db.Begin()
	unresolved, err := validator.UnresolvedHistoryTx(context.Background(), tx, fixture.appID)
	_ = tx.Rollback()
	if err != nil || !unresolved {
		t.Fatalf("unresolved=%t error=%v", unresolved, err)
	}
}

func TestDeploymentBindingValidatorRejectsMalformedPartialGraph(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	request := fixture.request(Start, BaselineRunning, "malformed-partial", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	if _, created, err := beginEffect(t, fixture, claim, accepted.Operation, BoundaryHealth, "", EffectObservationOnly); err != nil || !created {
		t.Fatalf("begin health created=%t error=%v", created, err)
	}
	if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_intent_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE application_lifecycle_effect_intents
		SET request_digest=? WHERE job_id=? AND boundary='health' AND component_name=''`,
		strings.Repeat("0", 64), accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	validator, err := NewDeploymentBindingValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	completion, err := validator.CurrentOperationTx(context.Background(), tx, deploymentbinding.OperationRef{
		ApplicationID: fixture.appID, OperationID: accepted.Operation.JobID, DesiredRevision: 1,
	})
	_ = tx.Rollback()
	if !errors.Is(err, deploymentbinding.ErrInvalidBinding) || completion != 0 {
		t.Fatalf("malformed partial completion=%d error=%v", completion, err)
	}
}

func TestDeploymentBindingValidatorRejectsAlteredPinsAndProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *operationFixture, Operation)
	}{
		{
			name: "pins digest",
			mutate: func(t *testing.T, fixture *operationFixture, operation Operation) {
				if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_operation_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`UPDATE application_lifecycle_operations SET pins_digest=? WHERE job_id=?`,
					strings.Repeat("0", 64), operation.JobID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "proof digest",
			mutate: func(t *testing.T, fixture *operationFixture, operation Operation) {
				if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_receipt_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`UPDATE application_lifecycle_effect_receipts SET proof_digest=?
					WHERE job_id=? AND boundary='complete' AND component_name=''`, strings.Repeat("0", 64), operation.JobID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOperationFixture(t, 1)
			request := fixture.request(Start, BaselineRunning, "altered-"+test.name, 0)
			accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
			if err != nil {
				t.Fatal(err)
			}
			claim := runningClaim(t, fixture, accepted.Operation.JobID)
			runCompleteGraph(t, fixture, claim, accepted.Operation)
			test.mutate(t, fixture, accepted.Operation)
			validator, err := NewDeploymentBindingValidator(fixture.repo)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := fixture.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			err = validator.HistoricalBindingTx(context.Background(), tx, deploymentbinding.OperationRef{
				ApplicationID: fixture.appID, OperationID: accepted.Operation.JobID, DesiredRevision: 1,
			})
			_ = tx.Rollback()
			if !errors.Is(err, deploymentbinding.ErrInvalidBinding) {
				t.Fatalf("historical error=%v", err)
			}
		})
	}
}

func TestDeploymentBindingRepositoryComposesWithConcreteValidator(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	request := fixture.request(Start, BaselineRunning, "binding-authority", 0)
	accepted, err := fixture.repo.Accept(context.Background(), fixture.jobs, request, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, accepted.Operation.JobID)
	runCompleteGraph(t, fixture, claim, accepted.Operation)
	stamp := operationStamp()
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`,
		stamp, stamp, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	validator, err := NewDeploymentBindingValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(fixture.db, validator)
	if err != nil {
		t.Fatal(err)
	}
	var inserted deploymentbinding.Binding
	job, created, err := fixture.jobs.CreateWithInputFinalized(jobs.CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: fixture.appID,
		IdempotencyKey: "ordinary-deployment", RequestedBy: fixture.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, func(tx *sql.Tx, job jobs.Job) error {
		var insertErr error
		inserted, insertErr = bindings.InsertNewTx(context.Background(), tx, job.ID, job.CreatedAt)
		return insertErr
	})
	if err != nil || !created {
		t.Fatalf("job=%#v created=%t error=%v", job, created, err)
	}
	if inserted.JobID() != job.ID || inserted.AuthorizingLifecycleJobID() != accepted.Operation.JobID ||
		inserted.DesiredRevision() != 1 || inserted.Mode() != deploymentbinding.ModeRunning {
		t.Fatalf("inserted binding=%#v", inserted)
	}
	persisted, err := bindings.Read(context.Background(), job.ID)
	if err != nil || !persisted.Equal(inserted) {
		t.Fatalf("persisted=%#v equal=%t error=%v", persisted, persisted.Equal(inserted), err)
	}
	classification, err := bindings.Check(context.Background(), persisted)
	if err != nil || classification != deploymentbinding.ClassificationCurrent {
		t.Fatalf("classification=%d error=%v", classification, err)
	}
}

func writeCompleteGraphTx(t *testing.T, repository *Repository, tx *sql.Tx, claim WorkerClaim, operation Operation) {
	t.Helper()
	specs, err := effectSpecs(operation)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		request := effectRequest(operation, spec.key.Boundary, spec.key.ComponentName, spec.mode)
		intent, created, err := repository.BeginBoundaryTx(context.Background(), tx, claim, operation, request)
		if err != nil || !created {
			t.Fatalf("begin %s/%s created=%t error=%v", spec.key.Boundary, spec.key.ComponentName, created, err)
		}
		proof := effectProof(operation, intent, spec.outcome)
		if _, created, err := repository.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err != nil || !created {
			t.Fatalf("observe %s/%s created=%t error=%v", spec.key.Boundary, spec.key.ComponentName, created, err)
		}
	}
}
