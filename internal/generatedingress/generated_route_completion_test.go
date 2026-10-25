package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGeneratedRouteCompletionSuccess(t *testing.T) {
	t.Run("full predecessor and exact replay", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if err := retirement.Complete(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt, 1)
		if err := retirement.Complete(context.Background()); err != nil {
			t.Fatalf("completion replay: %v", err)
		}
		assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt, 1)
	})

	t.Run("empty predecessor", func(t *testing.T) {
		db, receipt, retirement := newGeneratedRouteEmptyPredecessorCompletionFixture(t)
		if err := retirement.Complete(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertGeneratedRouteCompletionState(t, db, receipt, 1)
		if retirement.state.accounting.receipt.PredecessorComponentCount != 0 ||
			retirement.state.accounting.receipt.PredecessorDeploymentID != "" {
			t.Fatalf("empty predecessor handoff=%#v", retirement.state.accounting.receipt)
		}
	})

	t.Run("concurrent aliases serialize one terminal event", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		var wait sync.WaitGroup
		errorsOut := make(chan error, 2)
		for range 2 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				errorsOut <- retirement.Complete(context.Background())
			}()
		}
		wait.Wait()
		close(errorsOut)
		for err := range errorsOut {
			if err != nil {
				t.Fatal(err)
			}
		}
		assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt, 1)
	})
}

func TestGeneratedRouteCompletionReplayAndLostAcknowledgement(t *testing.T) {
	lost := errors.New("test route completion commit acknowledgement lost")
	fault := &generatedRouteAccountingLostAckFault{
		target: "generated_route_completion_receipts", cause: lost,
	}
	db := openGeneratedRouteAccountingLostAckDatabase(t, fault)
	fixture := newGeneratedRouteAccountingFixtureOnDatabase(t, db)
	retirement := activateGeneratedRouteAccountingFixture(t, fixture)
	fault.armed.Store(true)
	if err := retirement.Complete(context.Background()); err != nil || fault.hits.Load() != 1 {
		t.Fatalf("lost acknowledgement hits=%d error=%v", fault.hits.Load(), err)
	}
	assertGeneratedRouteCompletionState(t, db, retirement.state.receipt, 1)
	if fault.hits.Load() != 1 || fault.writes.Load() != 1 {
		t.Fatalf("lost acknowledgement hits=%d writes=%d", fault.hits.Load(), fault.writes.Load())
	}
	assertGeneratedRouteCompletionState(t, db, retirement.state.receipt, 1)
}

func TestGeneratedRouteCompletionCommitReadbackBoundaries(t *testing.T) {
	t.Run("confirmed rollback retains the original commit error", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if _, err := fixture.database().Exec(`CREATE TABLE unit3c_completion_commit_fault(
			id TEXT PRIMARY KEY, missing_app TEXT NOT NULL,
			FOREIGN KEY(missing_app) REFERENCES applications(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.database().Exec(`CREATE TRIGGER unit3c_completion_commit_fault_insert
			AFTER INSERT ON generated_route_completion_receipts
			BEGIN INSERT INTO unit3c_completion_commit_fault(id,missing_app)
			VALUES(NEW.activation_receipt_id,'unit3c-missing-app'); END`); err != nil {
			t.Fatal(err)
		}
		err := retirement.Complete(context.Background())
		if !errors.Is(err, deploymentexecution.ErrUnavailable) ||
			!strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Fatalf("rolled-back completion error=%v", err)
		}
		assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
		var faults int
		if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM unit3c_completion_commit_fault`).
			Scan(&faults); err != nil || faults != 0 {
			t.Fatalf("rolled-back commit fault rows=%d error=%v", faults, err)
		}
	})

	t.Run("cancelled readback retains the original commit error", func(t *testing.T) {
		lost := errors.New("test route completion acknowledgement lost before cancelled readback")
		ctx, cancel := context.WithCancel(context.Background())
		fault := &generatedRouteAccountingLostAckFault{
			target: "generated_route_completion_receipts", cause: lost, afterCommit: cancel,
		}
		db := openGeneratedRouteAccountingLostAckDatabase(t, fault)
		fixture := newGeneratedRouteAccountingFixtureOnDatabase(t, db)
		retirement := activateGeneratedRouteAccountingFixture(t, fixture)
		fault.armed.Store(true)
		err := retirement.Complete(ctx)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, lost) || fault.hits.Load() != 1 {
			t.Fatalf("cancelled readback hits=%d error=%v", fault.hits.Load(), err)
		}
		assertGeneratedRouteCompletionState(t, db, retirement.state.receipt, 1)
	})

	t.Run("live ledger drift retains the original commit error", func(t *testing.T) {
		lost := errors.New("test route completion acknowledgement lost before ledger readback")
		fault := &generatedRouteAccountingLostAckFault{
			target: "generated_route_completion_receipts", cause: lost,
		}
		db := openGeneratedRouteAccountingLostAckDatabase(t, fault)
		fixture := newGeneratedRouteAccountingFixtureOnDatabase(t, db)
		retirement := activateGeneratedRouteAccountingFixture(t, fixture)
		fault.afterCommit = func() {
			mutateGeneratedRouteCompletionAccounting(t, retirement,
				func(operation *candidateAccountingHandoffOperation) {
					operation.successor[0].charge.owner = candidateAccountingCandidate
				})
		}
		defer mutateGeneratedRouteCompletionAccounting(t, retirement,
			func(operation *candidateAccountingHandoffOperation) {
				operation.successor[0].charge.owner = candidateAccountingRouteActive
			})
		fault.armed.Store(true)
		err := retirement.Complete(context.Background())
		if !errors.Is(err, lost) || fault.hits.Load() != 1 {
			t.Fatalf("ledger-drifted readback hits=%d error=%v", fault.hits.Load(), err)
		}
		assertGeneratedRouteCompletionState(t, db, retirement.state.receipt, 1)
	})
}

func TestGeneratedRouteCompletionCommitReadbackEventIntegrity(t *testing.T) {
	for _, test := range []struct {
		name       string
		eventCount int
		mutate     func(*testing.T, *sql.DB, string)
	}{{
		name: "missing canonical event", eventCount: 0,
		mutate: func(t *testing.T, db *sql.DB, jobID string) {
			t.Helper()
			if _, err := db.Exec(`DELETE FROM job_events WHERE job_id=? AND code='job_succeeded'`,
				jobID); err != nil {
				t.Fatal(err)
			}
		},
	}, {
		name: "duplicate canonical event", eventCount: 2,
		mutate: func(t *testing.T, db *sql.DB, jobID string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO job_events(
				job_id,sequence,attempt,timestamp,level,phase,code,message
			) SELECT job_id,(SELECT COALESCE(MAX(sequence),0)+1 FROM job_events WHERE job_id=?),
				attempt,timestamp,level,phase,code,message FROM job_events
				WHERE job_id=? AND code='job_succeeded'`, jobID, jobID); err != nil {
				t.Fatal(err)
			}
		},
	}} {
		t.Run(test.name, func(t *testing.T) {
			fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
			if err := retirement.Complete(context.Background()); err != nil {
				t.Fatal(err)
			}
			operation, candidate, accounting, input := generatedRouteCompletionReadbackProof(t, retirement)
			test.mutate(t, fixture.database(), input.CurrentJobID)
			assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt,
				test.eventCount)
			if err := operation.readCommittedCompletion(context.Background(), candidate, accounting, input); err == nil {
				t.Fatal("noncanonical completion event passed commit readback")
			}
			assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt,
				test.eventCount)
		})
	}
}

func TestGeneratedRouteCompletionAuthorityAndCancellationRefusals(t *testing.T) {
	t.Run("cancelling current attempt", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if _, err := retirement.completeAccountingHandoff(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.database().Exec(`UPDATE jobs SET phase='cancelling' WHERE id=?`,
			retirement.state.receipt.CreatorJobID); err != nil {
			t.Fatal(err)
		}
		if err := retirement.Complete(context.Background()); err == nil {
			t.Fatal("cancelling attempt authorized completion")
		}
		assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
	})

	t.Run("crossed current worker attempt", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if _, err := retirement.completeAccountingHandoff(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.database().Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`,
			retirement.state.receipt.CreatorJobID); err != nil {
			t.Fatal(err)
		}
		if err := retirement.Complete(context.Background()); err == nil {
			t.Fatal("crossed current attempt authorized completion")
		}
		assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
	})

	t.Run("cancelled caller context", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if _, err := retirement.completeAccountingHandoff(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := retirement.Complete(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled completion error=%v", err)
		}
		assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
	})
}

func TestGeneratedRouteCompletionRollbackBoundaries(t *testing.T) {
	for _, test := range []struct {
		name  string
		table string
		when  string
	}{{"completion receipt insert", "generated_route_completion_receipts", "BEFORE INSERT"},
		{"job event insert", "job_events", "BEFORE INSERT"}} {
		t.Run(test.name, func(t *testing.T) {
			fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
			if _, err := retirement.completeAccountingHandoff(context.Background()); err != nil {
				t.Fatal(err)
			}
			trigger := "unit3b_fail_" + strings.ReplaceAll(test.table, "_", "")
			statement := `CREATE TRIGGER ` + trigger + ` ` + test.when + ` ON ` + test.table +
				` BEGIN SELECT RAISE(ABORT,'unit3b injected rollback'); END`
			if _, err := fixture.database().Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err := retirement.Complete(context.Background()); err == nil {
				t.Fatal("injected writer failure authorized completion")
			}
			assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
		})
	}
}

func TestGeneratedRouteCompletionAccountingProofRefusals(t *testing.T) {
	t.Run("successor route-active lease state drift", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if _, err := retirement.completeAccountingHandoff(context.Background()); err != nil {
			t.Fatal(err)
		}
		mutateGeneratedRouteCompletionAccounting(t, retirement, func(operation *candidateAccountingHandoffOperation) {
			operation.successor[0].charge.owner = candidateAccountingCandidate
		})
		if err := retirement.Complete(context.Background()); err == nil {
			t.Fatal("drifted successor authorized completion")
		}
		assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
	})

	t.Run("predecessor released tombstone drift", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if _, err := retirement.completeAccountingHandoff(context.Background()); err != nil {
			t.Fatal(err)
		}
		mutateGeneratedRouteCompletionAccounting(t, retirement, func(operation *candidateAccountingHandoffOperation) {
			operation.predecessor[0].release = candidateAccountingReleasePending
		})
		if err := retirement.Complete(context.Background()); err == nil {
			t.Fatal("drifted predecessor tombstone authorized completion")
		}
		assertGeneratedRouteCompletionAbsent(t, fixture.database(), retirement.state.receipt)
	})

	t.Run("terminal replay revalidates live ledger", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if err := retirement.Complete(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt, 1)
		mutateGeneratedRouteCompletionAccounting(t, retirement, func(operation *candidateAccountingHandoffOperation) {
			operation.successor[0].charge.owner = candidateAccountingCandidate
		})
		if err := retirement.Complete(context.Background()); err == nil {
			t.Fatal("ledger-drifted terminal replay succeeded")
		}
		assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt, 1)
	})
}

func TestGeneratedRouteCompletionReplayGuardRejectsCrossedSucceededJob(t *testing.T) {
	fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
	if err := retirement.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	activation := retirement.state.receipt
	completion := routeCompletionReceipt(generatedruntimestate.RouteCompletionInput{
		Activation: activation, AccountingHandoff: retirement.state.accounting.receipt,
		CurrentJobID:         activation.CreatorJobID,
		CurrentWorkerAttempt: retirement.state.execution.candidate.state.pins.claim.Attempt(),
		CompletedAt:          retirement.state.completion.completedAt,
	})
	jobID := uuid.NewString()
	stamp := completion.CompletedAt.UTC().Format(time.RFC3339Nano)
	if _, err := fixture.database().Exec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,progress_percent,checkpoint_json,
		attempt,requested_by,input_json,created_at,started_at,finished_at,updated_at
	) SELECT ?,'deploy','application',?,'succeeded','succeeded',100,'{"phase":"succeeded"}',
		attempt,requested_by,input_json,?,?,?,? FROM jobs WHERE id=?`, jobID, fixture.appID,
		stamp, stamp, stamp, stamp, activation.CreatorJobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database().Exec(`DROP TRIGGER deployment_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database().Exec(`UPDATE deployments SET job_id=? WHERE id=?`,
		jobID, activation.SuccessorDeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.database().Exec(generatedDomainA0DeploymentIdentityGuardSQL); err != nil {
		t.Fatal(err)
	}
	var persistedJobID, resourceID, status, phase string
	var attempt int
	if err := fixture.database().QueryRow(`SELECT deployment.job_id,job.resource_id,job.status,
		job.phase,job.attempt FROM deployments deployment JOIN jobs job ON job.id=deployment.job_id
		WHERE deployment.id=?`, activation.SuccessorDeploymentID).Scan(&persistedJobID, &resourceID,
		&status, &phase, &attempt); err != nil {
		t.Fatal(err)
	}
	if persistedJobID != jobID || resourceID != activation.AppID || status != "succeeded" ||
		phase != "succeeded" || attempt != completion.CurrentWorkerAttempt {
		t.Fatalf("crossed job precondition job=%s resource=%s status=%s/%s attempt=%d",
			persistedJobID, resourceID, status, phase, attempt)
	}
	tx, err := fixture.database().BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	candidate := retirement.state.execution.candidate.state
	if err := candidate.pinned.CheckGeneratedRouteCompletionReplayTx(
		tx, candidate.pins.claim, activation, completion); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("crossed same-app succeeded job replay guard=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertGeneratedRouteCompletionState(t, fixture.database(), activation, 1)
}

func TestGeneratedRouteCompletionLifetimeAndProofLoss(t *testing.T) {
	t.Run("leave winning before nested start refuses without leaked activity", func(t *testing.T) {
		_, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		_, _, outerDone, err := retirement.begin()
		if err != nil {
			t.Fatal(err)
		}
		var releaseOuter sync.Once
		done := func() { releaseOuter.Do(outerDone) }
		defer done()
		left := make(chan struct{})
		go func() {
			retirement.Leave()
			close(left)
		}()
		for deadline := time.Now().Add(time.Second); ; {
			retirement.state.mu.Lock()
			closing := retirement.state.closing
			retirement.state.mu.Unlock()
			if closing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("leave did not begin")
			}
			time.Sleep(time.Millisecond)
		}
		if _, err := retirement.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("nested stage after closing: %v", err)
		}
		done()
		select {
		case <-left:
		case <-time.After(time.Second):
			t.Fatal("leave remained blocked after outer borrow ended")
		}
		if err := retirement.Complete(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("closed completion: %v", err)
		}
	})

	t.Run("durable rows do not reconstruct a private handle", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		if err := retirement.Complete(context.Background()); err != nil {
			t.Fatal(err)
		}
		zero := &GeneratedPredecessorRetirement{}
		if err := zero.Complete(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("zero handle completion: %v", err)
		}
		retirement.Leave()
		if err := retirement.Complete(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("escaped handle completion: %v", err)
		}
		assertGeneratedRouteCompletionState(t, fixture.database(), retirement.state.receipt, 1)
	})

	t.Run("completion start makes leave wait", func(t *testing.T) {
		reached, release := make(chan struct{}), make(chan struct{})
		lost := errors.New("test completion acknowledgement held")
		fault := &generatedRouteAccountingLostAckFault{
			target: "generated_route_completion_receipts", cause: lost,
			afterCommit: func() {
				close(reached)
				<-release
			},
		}
		db := openGeneratedRouteAccountingLostAckDatabase(t, fault)
		fixture := newGeneratedRouteAccountingFixtureOnDatabase(t, db)
		retirement := activateGeneratedRouteAccountingFixture(t, fixture)
		fault.armed.Store(true)
		var releaseCompletion sync.Once
		releaseCommit := func() { releaseCompletion.Do(func() { close(release) }) }
		defer releaseCommit()
		completed := make(chan error, 1)
		go func() { completed <- retirement.Complete(context.Background()) }()
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			t.Fatal("completion did not reach commit")
		}
		left := make(chan struct{})
		go func() {
			retirement.Leave()
			close(left)
		}()
		for deadline := time.Now().Add(time.Second); ; {
			retirement.state.mu.Lock()
			closing := retirement.state.closing
			retirement.state.mu.Unlock()
			if closing {
				break
			}
			select {
			case <-left:
				t.Fatal("leave completed before entering closing state")
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("leave did not enter closing state")
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-left:
			t.Fatal("leave completed while completion retained its borrow")
		default:
		}
		releaseCommit()
		if err := <-completed; err != nil {
			t.Fatalf("completion result=%v", err)
		}
		select {
		case <-left:
		case <-time.After(time.Second):
			t.Fatal("leave remained blocked after completion returned")
		}
		assertGeneratedRouteCompletionState(t, db, retirement.state.receipt, 1)
		if err := retirement.Complete(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("post-leave completion=%v", err)
		}
	})
}

func generatedRouteCompletionReadbackProof(t *testing.T,
	retirement *GeneratedPredecessorRetirement,
) (*generatedRouteCompletionOperation, *generatedCandidateExecutionState,
	*candidateAccountingHandoffOperation, generatedruntimestate.RouteCompletionInput,
) {
	t.Helper()
	operation := retirement.state.completion
	if operation == nil {
		t.Fatal("completion operation is absent")
	}
	accounting, err := operation.handoff.completionAccountingProof(operation.handoffFact)
	if err != nil {
		t.Fatal(err)
	}
	candidate := retirement.state.execution.candidate.state
	claim := candidate.pins.claim
	return operation, candidate, accounting, generatedruntimestate.RouteCompletionInput{
		Activation: operation.state.receipt, AccountingHandoff: operation.handoffFact,
		CurrentJobID: claim.JobID(), CurrentWorkerAttempt: claim.Attempt(),
		CompletedAt: operation.completedAt,
	}
}

func newGeneratedRouteEmptyPredecessorCompletionFixture(t *testing.T) (
	*sql.DB, generatedruntimestate.RouteActivationReceipt, *GeneratedPredecessorRetirement,
) {
	t.Helper()
	migration, _ := newGeneratedRouteCanonicalMigration(t)
	owner, _ := newGeneratedRouteSequentialCandidateOwnerForMigration(t, migration)
	execution, identity := prepareGeneratedRouteSequentialCandidate(t, owner)
	manager, runner, routeRunner := newGeneratedRoutePredecessorManagerFixture(t)
	runner.appID, runner.network, runner.endpoint = migration.guarded.appID, identity.NetworkName,
		generatedRouteEndpoint(identity)
	routeRunner.endpoints[identity.ContainerID] = generatedRouteEndpointSnapshot{
		appID: migration.guarded.appID, endpoint: generatedRouteEndpoint(identity),
		slot: generatedidentity.Slot(identity.Slot),
	}
	authority, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := authority.Pin(migration.guarded.context, execution,
		generatedRouteRequest(migration.guarded.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	_, retirement, err := sealed.ActivateWithPredecessorRetirement(migration.guarded.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(retirement.Leave)
	return owner.validation.migration.repository.db, retirement.state.receipt, retirement
}

func mutateGeneratedRouteCompletionAccounting(t *testing.T, retirement *GeneratedPredecessorRetirement,
	mutate func(*candidateAccountingHandoffOperation),
) {
	t.Helper()
	operation := retirement.state.accounting
	if operation == nil || operation.accounting == nil {
		t.Fatal("completion accounting operation is absent")
	}
	accounting := operation.accounting
	accounting.mu.Lock()
	unlock := lockCandidateAccountingGroups(accounting.successorGroup, accounting.predecessorGroup)
	mutate(accounting)
	unlock()
	accounting.mu.Unlock()
}

func assertGeneratedRouteCompletionAbsent(t *testing.T, db *sql.DB,
	activation generatedruntimestate.RouteActivationReceipt,
) {
	t.Helper()
	var runtimePhase, mainStatus, jobStatus, jobPhase string
	if err := db.QueryRow(`SELECT runtime.phase,deployment.status,job.status,job.phase
		FROM generated_runtime_deployments runtime JOIN deployments deployment
		ON deployment.id=runtime.deployment_id JOIN jobs job ON job.id=deployment.job_id
		WHERE runtime.deployment_id=?`, activation.SuccessorDeploymentID).
		Scan(&runtimePhase, &mainStatus, &jobStatus, &jobPhase); err != nil {
		t.Fatal(err)
	}
	if runtimePhase != string(generatedruntimestate.PhaseDraining) || mainStatus != "waiting_health" ||
		jobStatus != "running" {
		t.Fatalf("completion state runtime=%s main=%s job=%s/%s",
			runtimePhase, mainStatus, jobStatus, jobPhase)
	}
	assertGeneratedRouteCompletionCounts(t, db, activation, 0, 0)
}

func assertGeneratedRouteCompletionState(t *testing.T, db *sql.DB,
	activation generatedruntimestate.RouteActivationReceipt, eventCount int,
) {
	t.Helper()
	var runtimePhase, mainStatus, jobStatus, jobPhase string
	if err := db.QueryRow(`SELECT runtime.phase,deployment.status,job.status,job.phase
		FROM generated_runtime_deployments runtime JOIN deployments deployment
		ON deployment.id=runtime.deployment_id JOIN jobs job ON job.id=deployment.job_id
		WHERE runtime.deployment_id=?`, activation.SuccessorDeploymentID).
		Scan(&runtimePhase, &mainStatus, &jobStatus, &jobPhase); err != nil {
		t.Fatal(err)
	}
	if runtimePhase != string(generatedruntimestate.PhaseSucceeded) || mainStatus != "succeeded" ||
		jobStatus != "succeeded" || jobPhase != "succeeded" {
		t.Fatalf("completion state runtime=%s main=%s job=%s/%s",
			runtimePhase, mainStatus, jobStatus, jobPhase)
	}
	assertGeneratedRouteCompletionCounts(t, db, activation, 1, eventCount)
}

func assertGeneratedRouteCompletionCounts(t *testing.T, db *sql.DB,
	activation generatedruntimestate.RouteActivationReceipt, receiptCount, eventCount int,
) {
	t.Helper()
	var receipts, events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, activation.ReceiptID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_events
		WHERE job_id=? AND code='job_succeeded'`, activation.CreatorJobID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if receipts != receiptCount || events != eventCount {
		t.Fatalf("completion facts receipts=%d events=%d want=%d/%d",
			receipts, events, receiptCount, eventCount)
	}
}
