package generatedingress

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedCandidateObservedCreateFailureRequiresConfirmedAbsence(t *testing.T) {
	fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
	completedExit := generatedMigrationCompletedExitOne(t)
	fixture.runner.setResult(candidateOwnerCreate, func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{Stderr: []byte("candidate create failed")}, completedExit
	})
	if _, err := execution.CreateAndRecord(invocation); err == nil {
		t.Fatal("determinate create failure returned nil")
	}
	if permit.observedFailure != generatedruntimestate.DiagnosticStartFailed {
		t.Fatalf("observed failure = %q", permit.observedFailure)
	}
	if _, err := execution.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("failure before absence proof = %v", err)
	}
	if err := execution.RemoveInactive(invocation); err != nil {
		t.Fatal(err)
	}
	state, err := execution.RecordObservedFailure()
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateOwnerFailureState(t, fixture, state, generatedruntimestate.DiagnosticStartFailed, "apply_failed")
}

func TestGeneratedCandidateObservedHealthFailureRequiresConfirmedAbsence(t *testing.T) {
	fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, execution)
	fixture.runner.setHealth("unhealthy")
	if err := execution.WaitHealthyAndRecord(invocation); err == nil {
		t.Fatal("unhealthy observation returned nil")
	}
	if permit.observedFailure != generatedruntimestate.DiagnosticHealthFailed {
		t.Fatalf("observed failure = %q", permit.observedFailure)
	}
	if err := execution.RemoveInactive(invocation); err != nil {
		t.Fatal(err)
	}
	state, err := execution.RecordObservedFailure()
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateOwnerFailureState(t, fixture, state, generatedruntimestate.DiagnosticHealthFailed, "health_failed")
	if fixture.runner.count(candidateOwnerStop) != 1 || fixture.runner.count(candidateOwnerRemove) != 1 {
		t.Fatalf("cleanup stop=%d remove=%d", fixture.runner.count(candidateOwnerStop), fixture.runner.count(candidateOwnerRemove))
	}
}

func TestGeneratedCandidateSuccessfulCandidateCleanupIsNotFailureEvidence(t *testing.T) {
	fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if err := execution.RemoveInactive(invocation); err != nil {
		t.Fatal(err)
	}
	if permit.observedFailure != "" || !permit.absenceConfirmed || !permit.environmentClean {
		t.Fatalf("successful cleanup proof failure=%q absent=%t environmentClean=%t",
			permit.observedFailure, permit.absenceConfirmed, permit.environmentClean)
	}
	if _, err := execution.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("manual cleanup failure record = %v", err)
	}
	current := fixture.validation.read(t)
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate || current.main.status != Applying ||
		len(current.components) != 1 || current.components[0].state != generatedruntimestate.ComponentRunning {
		t.Fatalf("manual cleanup changed durable state = %#v", current)
	}
}

func TestGeneratedCandidateRefusalsNeverMintObservedFailure(t *testing.T) {
	t.Run("staging refusal retains charge", func(t *testing.T) {
		fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
		stageErr := errors.New("test candidate stage refusal")
		fixture.environment.setStageError(stageErr)
		if _, err := execution.CreateAndRecord(invocation); !errors.Is(err, stageErr) {
			t.Fatalf("stage error = %v", err)
		}
		if permit.observedFailure != "" {
			t.Fatalf("stage refusal minted proof %q", permit.observedFailure)
		}
		if _, err := execution.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("stage refusal failure record = %v", err)
		}
		assertCandidateSecurityCapacityRetained(t, fixture)
	})

	t.Run("cancelled create with confirmed absence", func(t *testing.T) {
		fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
		fixture.runner.setResult(candidateOwnerCreate, func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
			return runtimeprocess.CommandResult{}, context.Canceled
		})
		if _, err := execution.CreateAndRecord(invocation); err == nil {
			t.Fatal("cancelled create returned nil")
		}
		if permit.observedFailure != "" {
			t.Fatalf("cancelled create minted proof %q", permit.observedFailure)
		}
		if err := execution.RemoveInactive(invocation); err != nil {
			t.Fatal(err)
		}
		if _, err := execution.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("cancelled create failure record = %v", err)
		}
	})

	t.Run("health timeout with confirmed absence", func(t *testing.T) {
		fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
		if _, err := execution.CreateAndRecord(invocation); err != nil {
			t.Fatal(err)
		}
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatal(err)
		}
		fixture.moveToWaitingHealth(t, execution)
		fixture.runner.setHealth("starting")
		fixture.authority.options.HealthTimeout = 0
		if err := execution.WaitHealthyAndRecord(invocation); err == nil {
			t.Fatal("health timeout returned nil")
		}
		if permit.observedFailure != "" {
			t.Fatalf("health timeout minted proof %q", permit.observedFailure)
		}
		fixture.authority.options.HealthTimeout = fixture.options.HealthTimeout
		if err := execution.RemoveInactive(invocation); err != nil {
			t.Fatal(err)
		}
		if _, err := execution.RecordObservedFailure(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("health timeout failure record = %v", err)
		}
	})
}

func TestGeneratedCandidateFailureRefusesAfterHealthHandoffLatch(t *testing.T) {
	fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	if permit.observedFailure != "" {
		t.Fatalf("successful health minted proof %q", permit.observedFailure)
	}
	if err := execution.RemoveInactive(invocation); !errors.Is(err, errCandidateCleanupClosed) {
		t.Fatalf("cleanup after handoff = %v", err)
	}
	if _, err := execution.RecordObservedFailure(); !errors.Is(err, deploymentexecution.ErrStageInvalid) {
		t.Fatalf("failure after handoff = %v", err)
	}
}

func TestGeneratedCandidateFailureLateWriteErrorRollsBackEveryStateRow(t *testing.T) {
	fixture, execution, invocation, permit := newCandidateFailureOwnerExecution(t)
	completedExit := generatedMigrationCompletedExitOne(t)
	fixture.runner.setResult(candidateOwnerCreate, func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{Stderr: []byte("candidate create failed")}, completedExit
	})
	if _, err := execution.CreateAndRecord(invocation); err == nil {
		t.Fatal("determinate create failure returned nil")
	}
	if permit.observedFailure != generatedruntimestate.DiagnosticStartFailed {
		t.Fatalf("observed failure = %q", permit.observedFailure)
	}
	if err := execution.RemoveInactive(invocation); err != nil {
		t.Fatal(err)
	}
	db := fixture.validation.migration.repository.db
	if _, err := db.Exec(`CREATE TRIGGER candidate_failure_test_late_abort
		BEFORE UPDATE OF status ON deployments WHEN NEW.status='failed'
		BEGIN SELECT RAISE(ABORT,'test late candidate failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordObservedFailure(); err == nil {
		t.Fatal("late helper failure returned nil")
	}

	var phase generatedruntimestate.Phase
	var mainStatus string
	var runtimeDiagnostic, runtimeFinishedNull, componentState, componentDiagnostic, componentFinishedNull string
	if err := db.QueryRow(`SELECT rd.phase,d.status,COALESCE(rd.diagnostic_code,''),
		CASE WHEN rd.finished_at IS NULL THEN 'null' ELSE 'set' END,
		c.state,COALESCE(c.diagnostic_code,''),CASE WHEN c.finished_at IS NULL THEN 'null' ELSE 'set' END
		FROM generated_runtime_deployments rd
		JOIN deployments d ON d.id=rd.deployment_id
		JOIN generated_runtime_components c ON c.deployment_id=rd.deployment_id
		WHERE rd.deployment_id=?`, fixture.validation.request.DeploymentID).Scan(
		&phase, &mainStatus, &runtimeDiagnostic, &runtimeFinishedNull,
		&componentState, &componentDiagnostic, &componentFinishedNull,
	); err != nil {
		t.Fatal(err)
	}
	if phase != generatedruntimestate.PhaseStartingCandidate || mainStatus != string(Applying) ||
		runtimeDiagnostic != "" || runtimeFinishedNull != "null" ||
		componentState != string(generatedruntimestate.ComponentStarting) || componentDiagnostic != "" || componentFinishedNull != "null" {
		t.Fatalf("late failure leaked state phase=%q main=%q runtime=%q/%q component=%q/%q/%q",
			phase, mainStatus, runtimeDiagnostic, runtimeFinishedNull, componentState, componentDiagnostic, componentFinishedNull)
	}
	if _, err := execution.RecordObservedFailure(); !errors.Is(err, errCandidateCapacityBusy) {
		t.Fatalf("late failure reopened winner latch = %v", err)
	}
}

func newCandidateFailureOwnerExecution(
	t *testing.T,
) (*candidateOwnerFixture, *GeneratedCandidateExecution, GeneratedCandidateInvocation, *generatedCandidateInvocationPermit) {
	t.Helper()
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := fixture.beginCreate(t, execution)
	permit := fixture.bindInvocation(t, invocation)
	return fixture, execution, invocation, permit
}

func assertCandidateOwnerFailureState(
	t *testing.T,
	fixture *candidateOwnerFixture,
	state generatedruntimestate.Deployment,
	diagnostic generatedruntimestate.DiagnosticCode,
	mainDiagnostic string,
) {
	t.Helper()
	if state.Phase != generatedruntimestate.PhaseFailed || state.DiagnosticCode != diagnostic ||
		len(state.Components) != 1 || state.Components[0].State != generatedruntimestate.ComponentFailed ||
		state.Components[0].DiagnosticCode != diagnostic {
		t.Fatalf("failed generated state = %#v", state)
	}
	var status, gotDiagnostic, failureCode string
	if err := fixture.validation.migration.repository.db.QueryRow(
		`SELECT status,diagnostic_code,failure_code FROM deployments WHERE id=?`, fixture.validation.request.DeploymentID,
	).Scan(&status, &gotDiagnostic, &failureCode); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || gotDiagnostic != mainDiagnostic || failureCode != mainDiagnostic {
		t.Fatalf("main status=%q diagnostic=%q failure=%q", status, gotDiagnostic, failureCode)
	}
}
