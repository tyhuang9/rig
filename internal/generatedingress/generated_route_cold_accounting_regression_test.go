//go:build linux || windows

package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedDomainColdOwnershipWithoutChargeRefusesRetirementRoster(t *testing.T) {
	source := &generatedRouteColdAccountingCapacitySource{candidateCapacityTestSource: candidateCapacityTestSource{
		snapshot: generatedcapacity.Snapshot{MemoryAvailableBytes: 100, DiskAvailableBytes: 100},
	}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := newCandidateCapacityLedger(budget)
	if err != nil {
		t.Fatal(err)
	}
	spec := candidateCapacityTestSpec("cold-app", "cold-deployment", "api")
	receiptID := "cold-receipt"
	if err := ledger.seedColdOwnership(spec, map[string]candidateColdReceiptSeed{
		"api": {id: receiptID, physical: candidatePhysicalPresent},
	}); err != nil {
		t.Fatal(err)
	}

	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: spec.appID, deploymentID: spec.deploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("cold ownership group is absent")
	}
	group.mu.Lock()
	component := group.byName["api"]
	if component == nil || component.receipt == nil || component.receipt.id != receiptID ||
		component.charge != nil || component.tokenHeld {
		group.mu.Unlock()
		t.Fatalf("cold ownership component = %#v", component)
	}
	beforeReceipt := component.receipt.state.Load()
	group.mu.Unlock()

	if operation, err := ledger.enterRetirementRoster(spec.appID, spec.deploymentID,
		map[string]string{"api": receiptID}); operation != nil || !errors.Is(err, errCandidateReceiptMismatch) {
		t.Fatalf("charge-free cold retirement operation=%#v error=%v", operation, err)
	}

	group.mu.Lock()
	defer group.mu.Unlock()
	component = group.byName["api"]
	if component == nil || component.charge != nil || component.tokenHeld || component.receipt == nil ||
		component.receipt.id != receiptID || component.receipt.state.Load() != beforeReceipt {
		t.Fatalf("failed retirement changed cold ownership component = %#v", component)
	}
	if source.snapshots.Load() != 0 {
		t.Fatalf("charge-free cold ownership attempted %d capacity acquisitions; want zero acquisitions and releases",
			source.snapshots.Load())
	}
}

type generatedRouteColdAccountingCapacitySource struct {
	candidateCapacityTestSource
	snapshots atomic.Int32
}

func (s *generatedRouteColdAccountingCapacitySource) Snapshot(ctx context.Context) (generatedcapacity.Snapshot, error) {
	s.snapshots.Add(1)
	return s.candidateCapacityTestSource.Snapshot(ctx)
}

func TestGeneratedDomainRestartAccountingCompletedCurrentSupportsDistinctReplacement(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingFixture(t)
		assertGeneratedRouteColdAccountingDurableSnapshot(t, fixture)
		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil || result == nil || result.candidates == nil || result.routes == nil {
			t.Fatalf("fresh cold construction result=%#v error=%v", result, err)
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		fixture.process.setDelegate(fixture.routes)

		firstHead, err := generatedruntimestate.New(fixture.identity.database).Active(
			context.Background(), fixture.migration.guarded.appID)
		if err != nil || firstHead.DeploymentID != fixture.migration.request.DeploymentID ||
			firstHead.ActivationReceiptID == "" {
			t.Fatalf("cold current head=%#v error=%v", firstHead, err)
		}
		assertGeneratedRouteCompletionState(t, fixture.identity.database, fixture.firstReceipt, 1)

		secondMigration := newGeneratedRouteColdAccountingSuccessorMigration(t, fixture.migration,
			fixture.jobs, fixture.identity.options.WorkingDirectory)
		validation := newGeneratedRouteSuccessorValidation(t, secondMigration)
		base := &candidateOwnerRunner{containerID: strings.Repeat("9", 64),
			absenceErr: generatedMigrationCompletedExitOne(t),
			before:     make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
			results:    make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
		fixture.process.setCandidate(base)
		validation.authority = result.candidates
		owner := &candidateOwnerFixture{validation: validation, authority: result.candidates, runner: base,
			environment: &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")},
			budget:      result.budget, options: generatedDomainCandidateOptions(fixture.identity.options)}
		secondExecution, secondIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)

		observed, err := result.manager.Observe(context.Background(), fixture.migration.guarded.appID)
		if err != nil {
			t.Fatal(err)
		}
		request := generatedRouteRequest(fixture.migration.guarded.appID, secondIdentity)
		request.FromSlot = observed.Slot
		sealed, err := result.routes.Pin(secondMigration.guarded.context, secondExecution, request)
		if err != nil {
			t.Fatal(err)
		}
		fixture.routes.network = secondIdentity.NetworkName
		fixture.routes.endpoint = generatedRouteEndpoint(secondIdentity)
		fixture.routes.slot = generatedidentity.Slot(secondIdentity.Slot)
		fixture.routes.endpoints[secondIdentity.ContainerID] = generatedRouteEndpointSnapshot{
			appID: fixture.migration.guarded.appID, endpoint: generatedRouteEndpoint(secondIdentity),
			slot: generatedidentity.Slot(secondIdentity.Slot),
		}
		_, retirement, err := sealed.ActivateWithPredecessorRetirement(secondMigration.guarded.context)
		if err != nil || retirement == nil {
			t.Fatalf("activate distinct replacement retirement=%#v error=%v", retirement, err)
		}
		defer retirement.Leave()
		if err := retirement.current(); err != nil {
			t.Fatalf("distinct replacement retirement is not current: %v", err)
		}

		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)
		completeErr := retirement.Complete(context.Background())
		if completeErr != nil {
			if !errors.Is(completeErr, errCandidateReceiptMismatch) {
				t.Fatalf("distinct replacement completed with non-target error: %v", completeErr)
			}
			assertGeneratedRouteColdAccountingNilCharge(t, result.candidates.ledger,
				fixture.migration.guarded.appID, fixture.migration.request.DeploymentID, fixture.componentReceipt)
			if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
				fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
				t.Fatalf("receipt mismatch reached predecessor effects stop=%d/%d remove=%d/%d",
					beforeStop, fixture.predecessor.count(candidateOwnerStop), beforeRemove,
					fixture.predecessor.count(candidateOwnerRemove))
			}
			assertGeneratedRouteCompletionAbsent(t, fixture.identity.database, retirement.state.receipt)
			t.Fatalf("distinct sealed replacement Complete: %v", completeErr)
		}
		assertGeneratedRouteCompletionState(t, fixture.identity.database, retirement.state.receipt, 1)
	})
}

func TestGeneratedDomainRestartAccountingInitialLegacyCurrentRestoresConfiguredDebt(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingInitialCurrentFixture(t)
		assertGeneratedRouteColdAccountingInitialLegacyCurrentSnapshot(t, fixture)
		beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
		beforeCommands := fixture.construction.snapshot()
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil {
			var diagnostic *generatedDomainConstructionDiagnostic
			if result != nil || !errors.As(err, &diagnostic) || diagnostic == nil ||
				diagnostic.stage != generatedDomainConstructionStageColdConstruction ||
				diagnostic.reason != generatedDomainConstructionReasonColdOwnership {
				t.Fatalf("initial legacy Current cold construction result=%#v diagnostic=%#v error=%v",
					result, diagnostic, err)
			}
			assertGeneratedRouteColdAccountingInitialLegacyCurrentSnapshot(t, fixture)
			assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
			if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
				fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
				t.Fatal("initial legacy Current cold refusal reached candidate effects")
			}
			if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
				generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
				t.Fatal("initial legacy Current cold ownership refusal acquired a fresh capacity share")
			}
			t.Fatalf("initial legacy Current cold construction reached the expected ownership refusal: %v", err)
		}
		if result == nil || result.candidates == nil || result.routes == nil {
			t.Fatalf("initial legacy Current cold construction result=%#v", result)
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		assertGeneratedRouteColdAccountingInitialLegacyCurrentSnapshot(t, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("initial legacy Current cold reconstruction replayed candidate effects")
		}
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("initial legacy Current cold reconstruction acquired a fresh capacity share")
		}
	})
}

func TestGeneratedDomainRestartAccountingInterruptedDrainingRestoresConfiguredDebt(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingDrainingFixture(t)
		assertGeneratedRouteColdAccountingDrainingSnapshot(t, fixture)
		beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
		beforeCommands := fixture.construction.snapshot()
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil {
			var diagnostic *generatedDomainConstructionDiagnostic
			if result != nil || !errors.As(err, &diagnostic) || diagnostic == nil ||
				diagnostic.stage != generatedDomainConstructionStageColdConstruction ||
				diagnostic.reason != generatedDomainConstructionReasonColdReservation {
				t.Fatalf("interrupted Draining cold construction result=%#v diagnostic=%#v error=%v", result,
					diagnostic, err)
			}
			assertGeneratedRouteColdAccountingDrainingSnapshot(t, fixture)
			assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
			if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
				fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
				t.Fatal("interrupted Draining cold refusal reached predecessor effects")
			}
			if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
				generatedRouteColdAccountingCapacityProbeCount(beforeCommands)+1 {
				t.Fatal("interrupted Draining target refusal did not make exactly one fresh capacity acquisition")
			}
			t.Fatalf("interrupted Draining cold construction reached the expected reservation refusal: %v", err)
		}
		if result == nil || result.candidates == nil || result.routes == nil {
			t.Fatalf("interrupted Draining cold construction result=%#v", result)
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		assertGeneratedRouteColdAccountingDrainingSnapshot(t, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("interrupted Draining cold reconstruction acquired a fresh capacity share")
		}
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("interrupted Draining cold reconstruction replayed predecessor effects")
		}
	})
}

func TestGeneratedDomainRestartAccountingRetiredPredecessorRecoversHandoffWithoutSecondShare(t *testing.T) {
	testGeneratedDomainRestartAccountingRecoversHandoff(t,
		newGeneratedRouteColdAccountingRetiredFixture, assertGeneratedRouteColdAccountingRetiredSnapshot,
		false, false, 0, 0)
}

func TestGeneratedDomainRestartAccountingAuthorizedHandoffRecoversWithoutSecondShare(t *testing.T) {
	testGeneratedDomainRestartAccountingRecoversHandoff(t,
		newGeneratedRouteColdAccountingAuthorizedFixture, assertGeneratedRouteColdAccountingAuthorizedSnapshot,
		true, false, 0, 0)
}

func TestGeneratedDomainRestartAccountingActivePredecessorRecoversRetirementWithoutSecondShare(t *testing.T) {
	for _, test := range []struct {
		name               string
		fixture            func(*testing.T) generatedRouteColdAccountingFixture
		assertPrefix       func(*testing.T, generatedRouteColdAccountingFixture)
		stopDelta, rmDelta int
	}{
		{name: "running predecessor", fixture: newGeneratedRouteColdAccountingActivatedFixture,
			assertPrefix: assertGeneratedRouteColdAccountingActivatedSnapshot, stopDelta: 1, rmDelta: 1},
		{name: "stopped predecessor", fixture: newGeneratedRouteColdAccountingActivatedStoppedFixture,
			assertPrefix: assertGeneratedRouteColdAccountingActivatedStoppedSnapshot, rmDelta: 1},
		{name: "absent predecessor", fixture: newGeneratedRouteColdAccountingActivatedAbsentFixture,
			assertPrefix: assertGeneratedRouteColdAccountingActivatedAbsentSnapshot},
	} {
		t.Run(test.name, func(t *testing.T) {
			testGeneratedDomainRestartAccountingRecoversHandoff(t, test.fixture, test.assertPrefix,
				false, true, test.stopDelta, test.rmDelta)
		})
	}
}

func TestGeneratedDomainRecoveredRetirementFailureBoundariesPreserveDebt(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, mode := range []string{
		"wait cancellation releases the exact dual hold",
		"post-wait predecessor drift refuses before effects",
		"stop error retains debt and prevents same-process duplicate",
		"termination failure latches the exact predecessor debt",
	} {
		t.Run(mode, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				testGeneratedDomainRecoveredRetirementFailureBoundary(t, mode)
			})
		})
	}
}

func testGeneratedDomainRecoveredRetirementFailureBoundary(t *testing.T, mode string) {
	t.Helper()
	fixture := newGeneratedRouteColdAccountingActivatedFixture(t)
	assertGeneratedRouteColdAccountingActivatedSnapshot(t, fixture)
	beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
	beforeCommands := fixture.construction.snapshot()
	beforeStop := fixture.predecessor.count(candidateOwnerStop)
	beforeRemove := fixture.predecessor.count(candidateOwnerRemove)
	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.process, fixture.environment, fixture.identity.options)
	if err != nil || result == nil || result.routes == nil || result.cold == nil {
		t.Fatalf("recovered retirement boundary construction=%#v error=%v", result, err)
	}
	assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
	assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
	if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
		generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
		t.Fatal("recovered retirement boundary acquired a fresh capacity share")
	}

	var waits []time.Duration
	var firstErr, secondErr error
	switch mode {
	case "wait cancellation releases the exact dual hold":
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := result.routes.waitRecoveredRetirement(cancelled, maximumDrain); !errors.Is(err, context.Canceled) {
			t.Fatalf("default recovered retirement wait cancellation error=%v", err)
		}
		result.routes.waitRecoveredRetirement = func(ctx context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			if len(waits) == 1 {
				return context.Canceled
			}
			return nil
		}
	case "post-wait predecessor drift refuses before effects":
		result.routes.waitRecoveredRetirement = func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			physical := fixture.process.predecessor.physical[fixture.retiredContainerID]
			physical.policy.identity.ReceiptID = uuid.NewString()
			fixture.process.predecessor.physical[fixture.retiredContainerID] = physical
			return nil
		}
	case "stop error retains debt and prevents same-process duplicate":
		result.routes.waitRecoveredRetirement = func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		}
		fixture.predecessor.setResult(candidateOwnerStop,
			func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				return runtimeprocess.CommandResult{}, errors.New("fixed recovered stop failure")
			})
	case "termination failure latches the exact predecessor debt":
		result.routes.waitRecoveredRetirement = func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		}
		fixture.predecessor.setResult(candidateOwnerStop,
			func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				return runtimeprocess.CommandResult{}, runtimeprocess.ErrTerminationFailed
			})
	default:
		t.Fatalf("unknown recovered retirement boundary %q", mode)
	}
	fixture.process.setRoutes(fixture.routes)

	var beforeAttempt int
	if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
		fixture.migration.claim.JobID()).Scan(&beforeAttempt); err != nil {
		t.Fatal(err)
	}
	recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.identity.database,
		time.Now().UTC(), fixture.migration.bindings)
	if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
		t.Fatalf("recover retirement boundary worker result=%+v error=%v", recovered, err)
	}
	var claimed jobs.Job
	var workerErr error
	expectedStatus := jobs.WaitingUser
	if mode == "wait cancellation releases the exact dual hold" {
		expectedStatus = jobs.Succeeded
	}
	runGeneratedColdBoundaryWorker(t, fixture.jobs,
		generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job,
			_ jobs.ProgressReporter,
		) (jobs.ExecutionResult, error) {
			claimed = job
			observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
				fixture.process, fixture.identity.options)
			if observeErr != nil {
				workerErr = observeErr
				return jobs.ExecutionResult{}, workerErr
			}
			if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
				observed, *result.cold); attestErr != nil {
				workerErr = attestErr
				return jobs.ExecutionResult{}, workerErr
			}
			binding, bindingErr := fixture.migration.bindings.Read(ctx, job.ID)
			if bindingErr != nil {
				workerErr = bindingErr
				return jobs.ExecutionResult{}, workerErr
			}
			claim, claimErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
			if claimErr != nil {
				workerErr = claimErr
				return jobs.ExecutionResult{}, workerErr
			}
			firstErr = result.routes.CompleteRecovered(ctx, claim, fixture.migration.request.DeploymentID)
			counts, countErr := generatedRouteColdAccountingRecoveredPrefixCounts(ctx,
				fixture.identity.database, fixture.firstReceipt.ReceiptID)
			if countErr != nil || counts != ([4]int{}) {
				workerErr = fmt.Errorf("failed recovered retirement durable prefix=%v error=%w", counts, countErr)
				return jobs.ExecutionResult{}, workerErr
			}
			switch mode {
			case "wait cancellation releases the exact dual hold":
				if !errors.Is(firstErr, context.Canceled) {
					workerErr = fmt.Errorf("injected recovered wait error=%w", firstErr)
					return jobs.ExecutionResult{}, workerErr
				}
				secondErr = result.routes.CompleteRecovered(ctx, claim, fixture.migration.request.DeploymentID)
				if secondErr != nil {
					workerErr = fmt.Errorf("retry after recovered wait cancellation: %w", secondErr)
					return jobs.ExecutionResult{}, workerErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			case "post-wait predecessor drift refuses before effects":
				if !errors.Is(firstErr, deploymentexecution.ErrIdentityLost) {
					workerErr = fmt.Errorf("post-wait predecessor drift error=%w", firstErr)
					return jobs.ExecutionResult{}, workerErr
				}
			case "stop error retains debt and prevents same-process duplicate":
				if firstErr == nil || errors.Is(firstErr, runtimeprocess.ErrTerminationFailed) {
					workerErr = fmt.Errorf("recovered stop failure error=%w", firstErr)
					return jobs.ExecutionResult{}, workerErr
				}
				secondErr = result.routes.CompleteRecovered(ctx, claim, fixture.migration.request.DeploymentID)
				if secondErr == nil {
					workerErr = errors.New("same-process recovered stop retry was accepted")
					return jobs.ExecutionResult{}, workerErr
				}
			case "termination failure latches the exact predecessor debt":
				var candidateErr *GeneratedCandidateError
				if !errors.Is(firstErr, runtimeprocess.ErrTerminationFailed) ||
					!errors.As(firstErr, &candidateErr) || candidateErr.Code != "process_termination_failed" {
					workerErr = fmt.Errorf("recovered termination failure error=%w", firstErr)
					return jobs.ExecutionResult{}, workerErr
				}
				secondErr = result.routes.CompleteRecovered(ctx, claim, fixture.migration.request.DeploymentID)
				if secondErr == nil {
					workerErr = errors.New("unquiesced recovered retirement retry was accepted")
					return jobs.ExecutionResult{}, workerErr
				}
			}
			return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser,
				PauseDisposition: jobs.PauseInsufficientReplacementCapacity}, nil
		}), fixture.migration.claim.JobID(), expectedStatus,
		fixture.identity.options.WorkingDirectory, func() string { return boundedErrorText(workerErr) })
	if workerErr != nil {
		t.Fatalf("recovered retirement boundary worker: %v", workerErr)
	}
	if claimed.ID != fixture.migration.claim.JobID() || claimed.ResourceID != fixture.migration.guarded.appID ||
		claimed.Attempt != beforeAttempt+1 {
		t.Fatalf("recovered retirement boundary claim=%+v before attempt=%d", claimed, beforeAttempt)
	}
	if len(waits) == 0 {
		t.Fatal("recovered retirement boundary did not enter the fixed wait")
	}
	for _, delay := range waits {
		if delay != maximumDrain {
			t.Fatalf("recovered retirement boundary delay=%s want=%s", delay, maximumDrain)
		}
	}
	if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
		generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
		t.Fatal("recovered retirement boundary worker acquired a second capacity share")
	}
	afterProtection := generatedRouteColdAccountingProtection(t, fixture)
	if !sameGatewayHistorySnapshot(beforeProtection.history, afterProtection.history) ||
		!sameGeneratedDomainProtectedStartup(beforeProtection.protected, afterProtection.protected) {
		t.Fatal("recovered retirement boundary changed protected history")
	}

	switch mode {
	case "wait cancellation releases the exact dual hold":
		if !reflect.DeepEqual(waits, []time.Duration{maximumDrain, maximumDrain}) {
			t.Fatalf("wait cancellation retry waits=%v", waits)
		}
		assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop+1 ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove+1 {
			t.Fatal("wait cancellation retry did not perform exact retirement effects")
		}
	case "post-wait predecessor drift refuses before effects":
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("post-wait drift performed predecessor effects")
		}
	case "stop error retains debt and prevents same-process duplicate":
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop+1 ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("recovered stop failure retried or removed the predecessor")
		}
	case "termination failure latches the exact predecessor debt":
		assertGeneratedRouteColdAccountingUnquiescedPredecessorDebt(t, result, fixture)
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop+1 ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("termination failure retried or removed the predecessor")
		}
	}
}

func generatedRouteColdAccountingRecoveredPrefixCounts(ctx context.Context, database *sql.DB,
	receiptID string,
) ([4]int, error) {
	var result [4]int
	for index, table := range []string{
		"generated_route_predecessor_retirements",
		"generated_route_predecessor_retirement_receipts",
		"generated_route_accounting_handoff_receipts",
		"generated_route_completion_receipts",
	} {
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+`
			WHERE activation_receipt_id=?`, receiptID).Scan(&result[index]); err != nil {
			return [4]int{}, err
		}
	}
	return result, nil
}

func assertGeneratedRouteColdAccountingUnquiescedPredecessorDebt(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	ledger := result.candidates.ledger
	ledger.mu.Lock()
	predecessor := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.lifecycleDeploymentID}]
	ledger.mu.Unlock()
	if predecessor == nil {
		t.Fatal("unquiesced recovered predecessor debt group is absent")
	}
	predecessor.mu.Lock()
	component := predecessor.byName[fixture.componentName]
	valid := component != nil && component.receipt != nil && component.receipt.id == fixture.predecessorReceipt &&
		component.receipt.state.Load()&candidateReceiptUnquiesced != 0 && !component.tokenHeld &&
		component.charge != nil && component.charge.state == candidateChargeActive &&
		component.charge.owner == candidateAccountingRouteActive && !component.charge.logicallyRetired &&
		candidateCapacityChargeTokenPublished(component.charge.token)
	predecessor.mu.Unlock()
	if !valid {
		t.Fatal("termination failure did not retain exact unquiesced predecessor debt")
	}
	result.capacity.mu.Lock()
	retained := result.capacity.phase == generatedDomainCapacityPublished && len(result.capacity.tokens) == 2
	result.capacity.mu.Unlock()
	if !retained {
		t.Fatal("termination failure changed restored dual debt")
	}
}

func TestGeneratedDomainRestartAccountingPartialPredecessorRetirementRecoversWithoutRepeatedEffects(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingPartialRetirementFixture(t)
		beforeProtection := generatedRouteColdAccountingProtection(t, fixture.base)
		beforeCommands := fixture.base.construction.snapshot()
		beforeEffects := generatedRouteColdAccountingComponentEffectCounts(fixture.runner,
			fixture.predecessor)
		beforeObservation := generatedRouteColdAccountingRetirementObservation(t,
			fixture.base.identity.database, fixture.base.firstReceipt.ReceiptID, fixture.recordedComponent)

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.base.identity.stores,
			fixture.base.process, fixture.base.environment, fixture.base.identity.options)
		if err != nil || result == nil || result.candidates == nil || result.routes == nil || result.cold == nil {
			t.Fatalf("partial predecessor cold construction result=%#v error=%v", result, err)
		}
		var waits []time.Duration
		result.routes.waitRecoveredRetirement = func(ctx context.Context, delay time.Duration) error {
			if ctx == nil || delay != maximumDrain {
				return fmt.Errorf("partial recovered retirement wait context=%#v delay=%s", ctx, delay)
			}
			waits = append(waits, delay)
			return nil
		}
		assertGeneratedRouteColdAccountingPartialDebt(t, result, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture.base)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.base.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("partial predecessor cold reconstruction acquired a fresh capacity share")
		}
		fixture.base.process.setRoutes(fixture.base.routes)

		var claimed jobs.Job
		var workerErr error
		beforeAttempt := 0
		if err := fixture.base.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
			fixture.base.migration.claim.JobID()).Scan(&beforeAttempt); err != nil {
			t.Fatal(err)
		}
		recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.base.identity.database,
			time.Now().UTC(), fixture.base.migration.bindings)
		if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
			t.Fatalf("recover partial predecessor worker: result=%+v error=%v", recovered, err)
		}
		runGeneratedColdBoundaryWorker(t, fixture.base.jobs,
			generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job,
				_ jobs.ProgressReporter,
			) (jobs.ExecutionResult, error) {
				claimed = job
				observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.base.identity.stores,
					fixture.base.process, fixture.base.identity.options)
				if observeErr != nil {
					workerErr = fmt.Errorf("post-claim partial startup observation: %w", observeErr)
					return jobs.ExecutionResult{}, workerErr
				}
				if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
					observed, *result.cold); attestErr != nil {
					workerErr = fmt.Errorf("post-claim partial cold manifest: %w", attestErr)
					return jobs.ExecutionResult{}, workerErr
				}
				binding, bindingErr := fixture.base.migration.bindings.Read(ctx, job.ID)
				if bindingErr != nil {
					workerErr = bindingErr
					return jobs.ExecutionResult{}, workerErr
				}
				claim, claimErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
				if claimErr != nil {
					workerErr = claimErr
					return jobs.ExecutionResult{}, workerErr
				}
				workerErr = result.routes.CompleteRecovered(ctx, claim,
					fixture.base.migration.request.DeploymentID)
				if workerErr != nil {
					return jobs.ExecutionResult{}, workerErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			}), fixture.base.migration.claim.JobID(), jobs.Succeeded,
			fixture.base.identity.options.WorkingDirectory, func() string { return boundedErrorText(workerErr) })
		if workerErr != nil || claimed.ID != fixture.base.migration.claim.JobID() ||
			claimed.ResourceID != fixture.base.migration.guarded.appID || claimed.Attempt != beforeAttempt+1 {
			t.Fatalf("fresh partial worker=%+v before attempt=%d error=%v", claimed, beforeAttempt, workerErr)
		}
		if !reflect.DeepEqual(waits, []time.Duration{maximumDrain}) {
			t.Fatalf("partial predecessor retirement waits=%v want=[%s]", waits, maximumDrain)
		}
		afterObservation := generatedRouteColdAccountingRetirementObservation(t,
			fixture.base.identity.database, fixture.base.firstReceipt.ReceiptID, fixture.recordedComponent)
		if !reflect.DeepEqual(afterObservation, beforeObservation) {
			t.Fatalf("partial predecessor changed retained component observation before=%#v after=%#v",
				beforeObservation, afterObservation)
		}
		afterEffects := generatedRouteColdAccountingComponentEffectCounts(fixture.runner, fixture.predecessor)
		for component, before := range beforeEffects {
			after := afterEffects[component]
			if component == fixture.recordedComponent {
				if after != before {
					t.Fatalf("recorded-absent %s repeated effects before=%+v after=%+v", component, before, after)
				}
				continue
			}
			if after.stop != before.stop+1 || after.remove != before.remove+1 {
				t.Fatalf("unrecorded %s recovery effects before=%+v after=%+v", component, before, after)
			}
		}
		assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture.base)
		assertGeneratedRouteColdAccountingPartialCompletionDelta(t, beforeProtection, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.base.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("fresh partial predecessor worker acquired a second capacity share")
		}
	})
}

type generatedRouteColdAccountingPartialRetirementFixture struct {
	base              generatedRouteColdAccountingFixture
	runner            *generatedRouteCandidateRunner
	predecessor       map[string]CandidateIdentity
	successor         map[string]CandidateIdentity
	recordedComponent string
}

type generatedRouteColdAccountingEffects struct {
	stop   int
	remove int
}

func generatedRouteColdAccountingComponentEffectCounts(runner *generatedRouteCandidateRunner,
	identities map[string]CandidateIdentity,
) map[string]generatedRouteColdAccountingEffects {
	result := make(map[string]generatedRouteColdAccountingEffects, len(identities))
	if runner == nil || runner.delegate == nil {
		return result
	}
	for component, identity := range identities {
		for _, request := range runner.delegate.requests() {
			if len(request.Args) == 0 || request.Args[len(request.Args)-1] != identity.ContainerID {
				continue
			}
			value := result[component]
			switch candidateOwnerKind(request) {
			case candidateOwnerStop:
				value.stop++
			case candidateOwnerRemove:
				value.remove++
			}
			result[component] = value
		}
	}
	return result
}

func generatedRouteColdAccountingRetirementObservation(t *testing.T, database *sql.DB,
	receiptID, component string,
) generatedruntimestate.PredecessorRetirementObservation {
	t.Helper()
	var result generatedruntimestate.PredecessorRetirementObservation
	var observed string
	err := database.QueryRow(`SELECT activation_receipt_id,app_id,successor_deployment_id,
		predecessor_deployment_id,component_name,candidate_receipt_id,container_id,
		candidate_policy_digest,observed_absent_at
		FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=? AND component_name=?`, receiptID, component).
		Scan(&result.ActivationReceiptID, &result.AppID, &result.SuccessorDeploymentID,
			&result.PredecessorDeploymentID, &result.ComponentName, &result.CandidateReceiptID,
			&result.ContainerID, &result.CandidatePolicyDigest, &observed)
	if err != nil {
		t.Fatalf("read %s partial retirement observation: %v", component, err)
	}
	result.ObservedAbsentAt, err = time.Parse(time.RFC3339Nano, observed)
	if err != nil {
		t.Fatalf("parse %s partial retirement timestamp: %v", component, err)
	}
	return result
}

func assertGeneratedRouteColdAccountingPartialCompletionDelta(t *testing.T,
	before generatedRouteColdAccountingProtectionSnapshot,
	fixture generatedRouteColdAccountingPartialRetirementFixture,
) {
	t.Helper()
	after := generatedRouteColdAccountingProtection(t, fixture.base)
	generatedRouteColdAccountingValidateCompletionTerminal(t, fixture.base)
	assertGeneratedRouteColdAccountingPartialRetirementCompleted(t, fixture)
	beforeTables := generatedRouteColdAccountingCompletionTables(t, before.database.Tables, fixture.base, false)
	afterTables := generatedRouteColdAccountingCompletionTables(t, after.database.Tables, fixture.base, true)
	beforeTables = generatedRouteColdAccountingPartialCompletionTables(t, beforeTables, fixture, false)
	afterTables = generatedRouteColdAccountingPartialCompletionTables(t, afterTables, fixture, true)
	databaseSame := reflect.DeepEqual(before.database.Schema, after.database.Schema) &&
		reflect.DeepEqual(beforeTables, afterTables)
	historySame := sameGatewayHistorySnapshot(before.history, after.history)
	protectedSame := sameGeneratedDomainProtectedStartup(before.protected, after.protected)
	if !databaseSame || !historySame || !protectedSame {
		t.Fatalf("fresh partial predecessor completion changed non-target state: database=%t history=%t protected=%t",
			databaseSame, historySame, protectedSame)
	}
}

func generatedRouteColdAccountingPartialCompletionTables(t *testing.T,
	tables map[string][][]gatewayRebindFixtureCell,
	fixture generatedRouteColdAccountingPartialRetirementFixture, after bool,
) map[string][][]gatewayRebindFixtureCell {
	t.Helper()
	result := make(map[string][][]gatewayRebindFixtureCell, len(tables))
	receiptID := fixture.base.firstReceipt.ReceiptID
	predecessorID := fixture.base.lifecycleDeploymentID
	for table, rows := range tables {
		columns, err := gatewayRebindFixtureColumns(context.Background(), fixture.base.identity.database, table)
		if err != nil {
			t.Fatalf("read %s columns for partial completion comparison: %v", table, err)
		}
		column := make(map[string]int, len(columns))
		for index, name := range columns {
			column[name] = index
		}
		for _, source := range rows {
			row := append([]gatewayRebindFixtureCell(nil), source...)
			skip := false
			switch table {
			case "generated_route_predecessor_retirements":
				if generatedRouteColdAccountingRowValue(row, column, "activation_receipt_id") == receiptID &&
					generatedRouteColdAccountingRowValue(row, column, "component_name") == "frontend" {
					if !after {
						t.Fatal("unrecorded partial component existed before recovered completion")
					}
					skip = true
				}
			case "generated_route_predecessor_retirement_receipts",
				"generated_route_accounting_handoff_authorizations",
				"generated_route_accounting_handoff_receipts":
				if generatedRouteColdAccountingRowValue(row, column, "activation_receipt_id") == receiptID {
					if !after {
						t.Fatalf("%s target row existed before recovered completion", table)
					}
					skip = true
				}
			case "generated_runtime_components":
				if generatedRouteColdAccountingRowValue(row, column, "deployment_id") == predecessorID &&
					generatedRouteColdAccountingRowValue(row, column, "component_name") == "frontend" {
					generatedRouteColdAccountingNormalizeColumns(t, row, column, table,
						"state", "diagnostic_code", "updated_at", "finished_at")
				}
			}
			if !skip {
				result[table] = append(result[table], row)
			}
		}
	}
	return result
}

func assertGeneratedRouteColdAccountingPartialRetirementCompleted(t *testing.T,
	fixture generatedRouteColdAccountingPartialRetirementFixture,
) {
	t.Helper()
	database := fixture.base.identity.database
	receiptID := fixture.base.firstReceipt.ReceiptID
	var observations, aggregates, authorizations, handoffs, completions int
	for _, query := range []struct {
		statement string
		into      *int
	}{
		{`SELECT COUNT(*) FROM generated_route_predecessor_retirements WHERE activation_receipt_id=?`, &observations},
		{`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts WHERE activation_receipt_id=?`, &aggregates},
		{`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations WHERE activation_receipt_id=?`, &authorizations},
		{`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts WHERE activation_receipt_id=?`, &handoffs},
		{`SELECT COUNT(*) FROM generated_route_completion_receipts WHERE activation_receipt_id=?`, &completions},
	} {
		if err := database.QueryRow(query.statement, receiptID).Scan(query.into); err != nil {
			t.Fatal(err)
		}
	}
	frontend := generatedRouteColdAccountingRetirementObservation(t, database, receiptID, "frontend")
	if observations != 2 || aggregates != 1 || authorizations != 1 || handoffs != 1 || completions != 1 ||
		frontend.ContainerID != fixture.predecessor["frontend"].ContainerID ||
		frontend.CandidateReceiptID != fixture.predecessor["frontend"].ReceiptID {
		t.Fatalf("partial predecessor terminal receipts=%d/%d/%d/%d/%d frontend=%#v",
			observations, aggregates, authorizations, handoffs, completions, frontend)
	}
}

func testGeneratedDomainRestartAccountingRecoversHandoff(t *testing.T,
	fixtureFactory func(*testing.T) generatedRouteColdAccountingFixture,
	assertPrefix func(*testing.T, generatedRouteColdAccountingFixture), preserveAuthorization, recoverRetirement bool,
	stopDelta, removeDelta int,
) {
	t.Helper()
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := fixtureFactory(t)
		assertPrefix(t, fixture)
		var authorization generatedruntimestate.RouteAccountingHandoffAuthorization
		if preserveAuthorization {
			authorization = generatedRouteColdAccountingAuthorization(t, fixture)
		}
		beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
		beforeCommands := fixture.construction.snapshot()
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil {
			var diagnostic *generatedDomainConstructionDiagnostic
			expectedReason := generatedDomainConstructionReasonColdReservation
			if recoverRetirement {
				expectedReason = generatedDomainConstructionReasonColdGroupReconstruction
			}
			if result != nil || !errors.As(err, &diagnostic) || diagnostic == nil ||
				diagnostic.stage != generatedDomainConstructionStageColdConstruction ||
				diagnostic.reason != expectedReason {
				t.Fatalf("retired predecessor cold construction result=%#v diagnostic=%#v error=%v",
					result, diagnostic, err)
			}
			assertPrefix(t, fixture)
			assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
			if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
				fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
				t.Fatal("retired predecessor cold refusal replayed candidate effects")
			}
			wantCapacityProbes := generatedRouteColdAccountingCapacityProbeCount(beforeCommands) + 1
			if recoverRetirement {
				wantCapacityProbes = generatedRouteColdAccountingCapacityProbeCount(beforeCommands)
			}
			if got := generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()); got != wantCapacityProbes {
				t.Fatalf("retired predecessor target refusal capacity acquisitions=%d want=%d",
					got, wantCapacityProbes)
			}
			t.Fatalf("retired predecessor cold construction reached the expected %s refusal: %v", expectedReason, err)
		}
		if result == nil || result.candidates == nil || result.routes == nil || result.cold == nil {
			t.Fatalf("retired predecessor cold construction result=%#v", result)
		}
		var retirementWaits []time.Duration
		if recoverRetirement {
			result.routes.waitRecoveredRetirement = func(ctx context.Context, delay time.Duration) error {
				if ctx == nil || delay != maximumDrain {
					return fmt.Errorf("recovered retirement wait context=%#v delay=%s", ctx, delay)
				}
				retirementWaits = append(retirementWaits, delay)
				return nil
			}
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		assertPrefix(t, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("retired predecessor cold reconstruction acquired a fresh capacity share")
		}
		fixture.process.setRoutes(fixture.routes)

		var beforeAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
			fixture.migration.claim.JobID()).Scan(&beforeAttempt); err != nil {
			t.Fatalf("read retired predecessor worker before recovery: %v", err)
		}
		recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.identity.database,
			time.Now().UTC(), fixture.migration.bindings)
		if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
			t.Fatalf("recover retired predecessor worker: result=%+v error=%v", recovered, err)
		}
		var claimed jobs.Job
		var claimedProofErr error
		runGeneratedColdBoundaryWorker(t, fixture.jobs,
			generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				claimed = job
				observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
					fixture.process, fixture.identity.options)
				if observeErr != nil {
					claimedProofErr = fmt.Errorf("post-claim retired startup observation: %w", observeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
					observed, *result.cold); attestErr != nil {
					claimedProofErr = fmt.Errorf("post-claim retired cold manifest: %w", attestErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				binding, bindingErr := fixture.migration.bindings.Read(ctx, job.ID)
				if bindingErr != nil {
					claimedProofErr = fmt.Errorf("post-claim retired deployment binding: %w", bindingErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				claim, claimErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
				if claimErr != nil {
					claimedProofErr = fmt.Errorf("post-claim retired execution claim: %w", claimErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if completionErr := result.routes.CompleteRecovered(ctx, claim,
					fixture.migration.request.DeploymentID); completionErr != nil {
					claimedProofErr = fmt.Errorf("recover accounting handoff and complete route: %w", completionErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			}), fixture.migration.claim.JobID(), jobs.Succeeded,
			fixture.identity.options.WorkingDirectory, func() string { return boundedErrorText(claimedProofErr) })
		if claimedProofErr != nil {
			t.Fatalf("fresh retired predecessor worker proof: %v", claimedProofErr)
		}
		if claimed.ID != fixture.migration.claim.JobID() || claimed.ResourceID != fixture.migration.guarded.appID ||
			claimed.Attempt != beforeAttempt+1 {
			t.Fatalf("fresh retired predecessor worker=%+v before attempt=%d", claimed, beforeAttempt)
		}
		assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
		if preserveAuthorization {
			if after := generatedRouteColdAccountingAuthorization(t, fixture); !reflect.DeepEqual(after, authorization) {
				t.Fatalf("recovered accounting handoff changed durable authorization before=%#v after=%#v",
					authorization, after)
			}
		}
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("fresh retired predecessor worker acquired a second capacity share")
		}
		wantStop, wantRemove := beforeStop+stopDelta, beforeRemove+removeDelta
		if fixture.predecessor.count(candidateOwnerStop) != wantStop ||
			fixture.predecessor.count(candidateOwnerRemove) != wantRemove {
			t.Fatalf("fresh retired predecessor worker effects stop=%d want=%d remove=%d want=%d",
				fixture.predecessor.count(candidateOwnerStop), wantStop,
				fixture.predecessor.count(candidateOwnerRemove), wantRemove)
		}
		if recoverRetirement && !reflect.DeepEqual(retirementWaits, []time.Duration{maximumDrain}) {
			t.Fatalf("recovered predecessor retirement waits=%v want=[%s]", retirementWaits, maximumDrain)
		}
	})
}

func TestGeneratedDomainRestartAccountingInterruptedDrainingClaimsFreshWorkerWithoutSecondShare(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingDrainingFixture(t)
		assertGeneratedRouteColdAccountingDrainingSnapshot(t, fixture)
		beforeConstructionCommands := fixture.construction.snapshot()
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil || result == nil || result.candidates == nil || result.routes == nil || result.cold == nil {
			t.Fatalf("interrupted Draining cold construction result=%#v error=%v", result, err)
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		assertGeneratedRouteColdAccountingDrainingSnapshot(t, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeConstructionCommands) {
			t.Fatal("interrupted Draining cold reconstruction acquired a fresh capacity share")
		}
		fixture.process.setRoutes(fixture.routes)

		beforeWorker := generatedRouteColdAccountingProtection(t, fixture)
		var beforeStatus, beforePhase string
		var beforeAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT status,phase,attempt FROM jobs WHERE id=?`,
			fixture.migration.claim.JobID()).Scan(&beforeStatus, &beforePhase, &beforeAttempt); err != nil {
			t.Fatalf("read interrupted Draining worker before recovery: %v", err)
		}
		recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.identity.database,
			time.Now().UTC(), fixture.migration.bindings)
		if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
			t.Fatalf("recover interrupted Draining worker: result=%+v error=%v", recovered, err)
		}

		var claimed jobs.Job
		var claimedProofErr error
		runGeneratedColdBoundaryWorker(t, fixture.jobs,
			generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				claimed = job
				observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
					fixture.process, fixture.identity.options)
				if observeErr != nil {
					claimedProofErr = fmt.Errorf("post-claim startup observation: %w", observeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
					observed, *result.cold); attestErr != nil {
					claimedProofErr = fmt.Errorf("post-claim cold manifest: %w", attestErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				binding, bindingErr := fixture.migration.bindings.Read(ctx, job.ID)
				if bindingErr != nil {
					claimedProofErr = fmt.Errorf("post-claim deployment binding: %w", bindingErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				claim, claimErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
				if claimErr != nil {
					claimedProofErr = fmt.Errorf("post-claim execution claim: %w", claimErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				groups, groupsErr := readGeneratedDomainColdGroups(ctx, result.candidates, observed)
				if groupsErr != nil {
					claimedProofErr = fmt.Errorf("post-claim cold groups: %w", groupsErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				evidence, evidenceErr := recoveredRouteCompletionEvidence(observed, groups, claim,
					fixture.migration.request.DeploymentID)
				if evidenceErr != nil || evidence.replay {
					claimedProofErr = fmt.Errorf("post-claim completion evidence replay=%t: %w",
						evidence.replay, evidenceErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if completionErr := result.routes.CompleteRecovered(ctx, claim,
					fixture.migration.request.DeploymentID); completionErr != nil {
					claimedProofErr = fmt.Errorf("complete recovered route: %w", completionErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				postCompletionContext, cancelPostCompletion := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer cancelPostCompletion()
				if strictErr := result.routes.AttestPublished(postCompletionContext, claim,
					fixture.migration.request.DeploymentID); !errors.Is(strictErr,
					deploymentexecution.ErrAuthorityStale) {
					claimedProofErr = fmt.Errorf("succeeded route passed strict Draining attestation: %v", strictErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				driftProtection := generatedRouteColdAccountingProtection(t, fixture)
				driftStop := fixture.predecessor.count(candidateOwnerStop)
				driftRemove := fixture.predecessor.count(candidateOwnerRemove)
				served := append([]byte(nil), fixture.routes.liveConfig...)
				fixture.routes.liveConfig = []byte(`{}`)
				changedRouteErr := result.routes.CompleteRecovered(postCompletionContext, claim,
					fixture.migration.request.DeploymentID)
				fixture.routes.liveConfig = served
				if !IsCode(changedRouteErr, DiagnosticIngressDrift) {
					claimedProofErr = fmt.Errorf("changed served route replay error=%v", changedRouteErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				assertGeneratedRouteColdAccountingProtectionUnchanged(t, driftProtection, fixture)
				if fixture.predecessor.count(candidateOwnerStop) != driftStop ||
					fixture.predecessor.count(candidateOwnerRemove) != driftRemove {
					claimedProofErr = errors.New("changed served route replay performed predecessor effects")
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if replayErr := result.routes.CompleteRecovered(postCompletionContext, claim,
					fixture.migration.request.DeploymentID); replayErr != nil {
					claimedProofErr = fmt.Errorf("replay recovered route completion: %w", replayErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				crossed, crossedErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt+1)
				if crossedErr != nil {
					claimedProofErr = fmt.Errorf("construct crossed recovered claim: %w", crossedErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if crossedErr := result.routes.CompleteRecovered(postCompletionContext, crossed,
					fixture.migration.request.DeploymentID); !errors.Is(crossedErr, deploymentexecution.ErrIdentityLost) {
					claimedProofErr = fmt.Errorf("crossed recovered claim error=%v", crossedErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if readbackErr := assertGeneratedRouteColdAccountingRecoveredReadback(postCompletionContext, result, claim,
					evidence); readbackErr != nil {
					claimedProofErr = fmt.Errorf("recovered completion readback: %w", readbackErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			}), fixture.migration.claim.JobID(), jobs.Succeeded,
			fixture.identity.options.WorkingDirectory, func() string {
				return boundedErrorText(claimedProofErr)
			})
		if claimedProofErr != nil {
			t.Fatalf("fresh interrupted Draining worker proof: %v", claimedProofErr)
		}
		if claimed.ID != fixture.migration.claim.JobID() || claimed.ResourceID != fixture.migration.guarded.appID ||
			claimed.Attempt != beforeAttempt+1 {
			t.Fatalf("fresh interrupted Draining worker=%+v before=%s/%s/%d", claimed,
				beforeStatus, beforePhase, beforeAttempt)
		}
		var afterStatus, afterPhase, afterPause string
		var afterAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT status,phase,COALESCE(pause_disposition,''),attempt
			FROM jobs WHERE id=?`, fixture.migration.claim.JobID()).
			Scan(&afterStatus, &afterPhase, &afterPause, &afterAttempt); err != nil {
			t.Fatalf("read interrupted Draining worker after execution: %v", err)
		}
		if afterStatus != string(jobs.Succeeded) || afterPhase != string(jobs.Succeeded) ||
			afterPause != "" || afterAttempt != beforeAttempt+1 {
			t.Fatalf("fresh interrupted Draining worker status=%s/%s/%s/%d", afterStatus, afterPhase,
				afterPause, afterAttempt)
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		assertGeneratedRouteCompletionState(t, fixture.identity.database, fixture.firstReceipt, 1)
		assertGeneratedRouteColdAccountingCompletionDelta(t, beforeWorker, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeConstructionCommands) {
			t.Fatal("fresh interrupted Draining worker acquired a second capacity share")
		}
		if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("fresh interrupted Draining worker replayed predecessor effects")
		}
	})
}

func assertGeneratedRouteColdAccountingRecoveredReadback(ctx context.Context,
	result *generatedDomainConstruction, claim deploymentexecution.Claim,
	evidence generatedRecoveredRouteCompletionEvidence,
) error {
	if result == nil || result.candidates == nil || result.routes == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	hold, err := result.candidates.ledger.holdRecoveredCompletionRoster(
		generatedDomainColdGroupSpec(result.candidates, evidence.group), evidence.receipts)
	if err != nil {
		return err
	}
	pinned, err := result.routes.stores.ExecutionGuard().PinScope(ctx)
	if err != nil {
		hold.leave()
		return err
	}
	defer pinned.Leave()
	activation, err := result.routes.readRecoveredRouteActivation(ctx, evidence.activation)
	if err != nil {
		hold.leave()
		return err
	}
	tx, err := result.routes.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		hold.leave()
		return err
	}
	completion, found, err := result.routes.runtime.ReadRouteCompletionReceiptTx(ctx, tx,
		activation.ReceiptID)
	if err != nil || !found {
		_ = tx.Rollback()
		hold.leave()
		return errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	if err := tx.Commit(); err != nil {
		hold.leave()
		return err
	}
	input := generatedruntimestate.RouteCompletionInput{Activation: activation,
		AccountingHandoff: evidence.handoff, CurrentJobID: claim.JobID(),
		CurrentWorkerAttempt: claim.Attempt(), CompletedAt: completion.CompletedAt}
	if err := result.routes.readRecoveredCompletion(ctx, pinned, claim, evidence, hold, input); err != nil {
		hold.leave()
		return err
	}
	crossedInput := input
	crossedInput.Activation.ReceiptID = evidence.activation.PredecessorActivationReceiptID
	if err := result.routes.readRecoveredCompletion(ctx, pinned, claim, evidence, hold,
		crossedInput); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		hold.leave()
		return fmt.Errorf("crossed recovered completion receipt error=%v", err)
	}
	hold.leave()
	if err := result.routes.readRecoveredCompletion(ctx, pinned, claim, evidence, hold,
		input); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		return fmt.Errorf("closed recovered completion hold error=%v", err)
	}
	return nil
}

type generatedRouteColdAccountingProtectionSnapshot struct {
	database  gatewayRebindFixtureDatabaseSnapshot
	history   gatewayHistorySnapshot
	protected generatedDomainProtectedStartupSnapshot
}

func generatedRouteColdAccountingProtection(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) generatedRouteColdAccountingProtectionSnapshot {
	t.Helper()
	database, err := snapshotGatewayRebindFixtureDatabase(context.Background(), fixture.identity.database)
	if err != nil {
		t.Fatalf("read interrupted Draining SQL snapshot: %v", err)
	}
	store, err := newStateStore(fixture.identity.dataRoot)
	if err != nil {
		t.Fatalf("open interrupted Draining protected store: %v", err)
	}
	history, err := readGatewayHistorySnapshotPolicy(store, true, true)
	if err != nil {
		t.Fatalf("read interrupted Draining protected history: %v", err)
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatalf("read interrupted Draining protected startup: %v", err)
	}
	return generatedRouteColdAccountingProtectionSnapshot{database: database, history: history,
		protected: protected}
}

func assertGeneratedRouteColdAccountingProtectionUnchanged(t *testing.T,
	before generatedRouteColdAccountingProtectionSnapshot, fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	after := generatedRouteColdAccountingProtection(t, fixture)
	databaseSame := reflect.DeepEqual(before.database, after.database)
	historySame := sameGatewayHistorySnapshot(before.history, after.history)
	protectedSame := sameGeneratedDomainProtectedStartup(before.protected, after.protected)
	if !databaseSame || !historySame || !protectedSame {
		t.Fatalf("interrupted Draining cold construction changed protected state: database=%t history=%t protected=%t",
			databaseSame, historySame, protectedSame)
	}
}

func assertGeneratedRouteColdAccountingCompletionDelta(t *testing.T,
	before generatedRouteColdAccountingProtectionSnapshot, fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	after := generatedRouteColdAccountingProtection(t, fixture)
	generatedRouteColdAccountingValidateCompletionTerminal(t, fixture)
	beforeTables := generatedRouteColdAccountingCompletionTables(t, before.database.Tables, fixture, false)
	afterTables := generatedRouteColdAccountingCompletionTables(t, after.database.Tables, fixture, true)
	databaseSame := reflect.DeepEqual(before.database.Schema, after.database.Schema) &&
		reflect.DeepEqual(beforeTables, afterTables)
	historySame := sameGatewayHistorySnapshot(before.history, after.history)
	protectedSame := sameGeneratedDomainProtectedStartup(before.protected, after.protected)
	if !databaseSame || !historySame || !protectedSame {
		t.Fatalf("fresh interrupted Draining completion changed non-target state: database=%t history=%t protected=%t",
			databaseSame, historySame, protectedSame)
	}
}

func generatedRouteColdAccountingValidateCompletionTerminal(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	db := fixture.identity.database
	var completionAt string
	if err := db.QueryRow(`SELECT completed_at FROM generated_route_completion_receipts
		WHERE activation_receipt_id=? AND app_id=? AND successor_deployment_id=?
		  AND current_job_id=?`, fixture.firstReceipt.ReceiptID, fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID, fixture.migration.claim.JobID()).Scan(&completionAt); err != nil ||
		completionAt == "" {
		t.Fatalf("read exact recovered completion receipt: completed=%q error=%v", completionAt, err)
	}
	var jobStatus, jobPhase, jobCheckpoint, jobUpdated, jobStarted, jobFinished string
	var jobProgress, jobAttempt int
	var jobErrorCode, jobErrorDetail, jobPause sql.NullString
	if err := db.QueryRow(`SELECT status,phase,progress_percent,attempt,checkpoint_json,error_code,error_detail,
		updated_at,COALESCE(started_at,''),COALESCE(finished_at,''),pause_disposition FROM jobs WHERE id=?`,
		fixture.migration.claim.JobID()).Scan(&jobStatus, &jobPhase, &jobProgress, &jobAttempt,
		&jobCheckpoint, &jobErrorCode, &jobErrorDetail, &jobUpdated, &jobStarted, &jobFinished,
		&jobPause); err != nil || jobStatus != string(jobs.Succeeded) || jobPhase != string(jobs.Succeeded) ||
		jobProgress != 100 || jobAttempt < 2 || jobCheckpoint != `{"phase":"succeeded"}` ||
		jobErrorCode.Valid || jobErrorDetail.Valid || jobPause.Valid || jobUpdated != completionAt ||
		jobStarted == "" || jobFinished != completionAt {
		t.Fatalf("recovered completion job=%s/%s/%d/%d checkpoint=%q errors=%v/%v pause=%v "+
			"timestamps=%q/%q/%q completion=%q error=%v", jobStatus, jobPhase, jobProgress, jobAttempt,
			jobCheckpoint, jobErrorCode, jobErrorDetail, jobPause, jobUpdated, jobStarted, jobFinished,
			completionAt, err)
	}
	var deploymentStatus, deploymentStarted, deploymentFinished string
	var failureCode, failureSummary, diagnosticCode sql.NullString
	if err := db.QueryRow(`SELECT status,COALESCE(started_at,''),COALESCE(finished_at,''),
		failure_code,failure_summary,diagnostic_code FROM deployments WHERE id=? AND app_id=? AND job_id=?`,
		fixture.migration.request.DeploymentID, fixture.migration.guarded.appID,
		fixture.migration.claim.JobID()).Scan(&deploymentStatus, &deploymentStarted, &deploymentFinished,
		&failureCode, &failureSummary, &diagnosticCode); err != nil || deploymentStatus != "succeeded" ||
		deploymentStarted == "" || deploymentFinished != completionAt || failureCode.Valid ||
		failureSummary.Valid || diagnosticCode.Valid {
		t.Fatalf("recovered completion deployment=%s timestamps=%q/%q diagnostics=%v/%v/%v error=%v",
			deploymentStatus, deploymentStarted, deploymentFinished, failureCode, failureSummary,
			diagnosticCode, err)
	}
	var runtimePhase, runtimeUpdated, runtimeFinished string
	var runtimeDiagnostic sql.NullString
	if err := db.QueryRow(`SELECT phase,updated_at,COALESCE(finished_at,''),diagnostic_code
		FROM generated_runtime_deployments WHERE deployment_id=? AND app_id=?`,
		fixture.migration.request.DeploymentID, fixture.migration.guarded.appID).
		Scan(&runtimePhase, &runtimeUpdated, &runtimeFinished, &runtimeDiagnostic); err != nil ||
		runtimePhase != string(generatedruntimestate.PhaseSucceeded) || runtimeUpdated != completionAt ||
		runtimeFinished != completionAt || runtimeDiagnostic.Valid {
		t.Fatalf("recovered completion runtime=%s timestamps=%q/%q diagnostic=%v error=%v",
			runtimePhase, runtimeUpdated, runtimeFinished, runtimeDiagnostic, err)
	}
}

func generatedRouteColdAccountingCompletionTables(t *testing.T,
	tables map[string][][]gatewayRebindFixtureCell, fixture generatedRouteColdAccountingFixture, after bool,
) map[string][][]gatewayRebindFixtureCell {
	t.Helper()
	result := make(map[string][][]gatewayRebindFixtureCell, len(tables))
	for table, rows := range tables {
		columns, err := gatewayRebindFixtureColumns(context.Background(), fixture.identity.database, table)
		if err != nil {
			t.Fatalf("read %s columns for completion comparison: %v", table, err)
		}
		column := make(map[string]int, len(columns))
		for index, name := range columns {
			column[name] = index
		}
		completionEvents := map[int]struct{}{}
		if after && table == "job_events" {
			completionEvents = generatedRouteColdAccountingCompletionEventIndexes(t, rows, column, fixture)
		}
		for rowIndex, source := range rows {
			row := append([]gatewayRebindFixtureCell(nil), source...)
			switch table {
			case "jobs":
				if generatedRouteColdAccountingRowValue(row, column, "id") == fixture.migration.claim.JobID() {
					generatedRouteColdAccountingNormalizeColumns(t, row, column, "jobs",
						"status", "phase", "progress_percent", "attempt", "checkpoint_json", "error_code",
						"error_detail", "updated_at", "started_at", "finished_at", "pause_disposition")
				}
			case "deployments":
				if generatedRouteColdAccountingRowValue(row, column, "id") == fixture.migration.request.DeploymentID {
					generatedRouteColdAccountingNormalizeColumns(t, row, column, "deployments",
						"status", "started_at", "finished_at", "failure_code", "failure_summary", "diagnostic_code")
				}
			case "generated_runtime_deployments":
				if generatedRouteColdAccountingRowValue(row, column, "deployment_id") == fixture.migration.request.DeploymentID {
					generatedRouteColdAccountingNormalizeColumns(t, row, column, "generated_runtime_deployments",
						"phase", "diagnostic_code", "updated_at", "finished_at")
				}
			case "job_events":
				if _, completionEvent := completionEvents[rowIndex]; completionEvent {
					continue
				}
			case "generated_route_completion_receipts":
				if generatedRouteColdAccountingRowValue(row, column, "activation_receipt_id") ==
					fixture.firstReceipt.ReceiptID {
					if after {
						continue
					}
					t.Fatal("completion receipt existed before recovered completion")
				}
			}
			result[table] = append(result[table], row)
		}
	}
	return result
}

func generatedRouteColdAccountingRowValue(row []gatewayRebindFixtureCell,
	columns map[string]int, name string,
) string {
	index, exists := columns[name]
	if !exists || index < 0 || index >= len(row) {
		return ""
	}
	return row[index].Value
}

func generatedRouteColdAccountingNormalizeColumns(t *testing.T, row []gatewayRebindFixtureCell,
	columns map[string]int, table string, names ...string,
) {
	t.Helper()
	for _, name := range names {
		index, exists := columns[name]
		if !exists || index < 0 || index >= len(row) {
			t.Fatalf("completion comparison missing %s.%s", table, name)
		}
		row[index] = gatewayRebindFixtureCell{Type: "normalized", Value: name}
	}
}

func generatedRouteColdAccountingCompletionEventIndexes(t *testing.T,
	rows [][]gatewayRebindFixtureCell, column map[string]int, fixture generatedRouteColdAccountingFixture,
) map[int]struct{} {
	t.Helper()
	type expectedEvent struct {
		level, phase, code, message, metadata string
		attempt                               int
	}
	var finalAttempt int
	if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
		fixture.migration.claim.JobID()).Scan(&finalAttempt); err != nil || finalAttempt < 2 {
		t.Fatalf("read recovered completion attempt: attempt=%d error=%v", finalAttempt, err)
	}
	expected := []expectedEvent{
		{"warn", "queued", "daemon_restarted", "Generated deployment requeued because hostd restarted", `{}`, finalAttempt - 1},
		{"info", "assigned", "job_assigned", "Job assigned", `{}`, finalAttempt},
		{"info", "validate", "phase_started", "Job phase started", `{}`, finalAttempt},
		{"info", "succeeded", "job_succeeded", "Deployment completed", `{}`, finalAttempt},
	}
	type indexedEvent struct {
		index    int
		sequence int
		value    expectedEvent
	}
	var target []indexedEvent
	for index, row := range rows {
		if generatedRouteColdAccountingRowValue(row, column, "job_id") != fixture.migration.claim.JobID() {
			continue
		}
		sequence, sequenceErr := strconv.Atoi(generatedRouteColdAccountingRowValue(row, column, "sequence"))
		attempt, attemptErr := strconv.Atoi(generatedRouteColdAccountingRowValue(row, column, "attempt"))
		if sequenceErr != nil || attemptErr != nil ||
			generatedRouteColdAccountingRowValue(row, column, "timestamp") == "" {
			t.Fatalf("recovered completion event identity index=%d sequenceErr=%v attemptErr=%v", index,
				sequenceErr, attemptErr)
		}
		target = append(target, indexedEvent{index: index, sequence: sequence, value: expectedEvent{
			level:    generatedRouteColdAccountingRowValue(row, column, "level"),
			phase:    generatedRouteColdAccountingRowValue(row, column, "phase"),
			code:     generatedRouteColdAccountingRowValue(row, column, "code"),
			message:  generatedRouteColdAccountingRowValue(row, column, "message"),
			metadata: generatedRouteColdAccountingRowValue(row, column, "metadata_json"), attempt: attempt,
		}})
	}
	if len(target) < len(expected) {
		t.Fatalf("recovered completion target events=%d want at least %d", len(target), len(expected))
	}
	appended := target[len(target)-len(expected):]
	var actual []expectedEvent
	indexes := make(map[int]struct{}, len(appended))
	for index, event := range appended {
		actual = append(actual, event.value)
		indexes[event.index] = struct{}{}
		if index > 0 && event.sequence != appended[index-1].sequence+1 {
			t.Fatalf("recovered completion event sequences=%+v", appended)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("recovered completion events=%+v want=%+v", actual, expected)
	}
	return indexes
}

func generatedRouteColdAccountingCapacityProbeCount(commands [][]string) int {
	count := 0
	for _, command := range commands {
		if len(command) == 6 && command[0] == "container" && command[1] == "exec" &&
			command[3] == "sh" && command[4] == "-c" && command[5] == capacityProbeCommand {
			count++
		}
	}
	return count
}

func assertGeneratedRouteColdAccountingDrainingSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read interrupted Draining durable count: %v", err)
		}
		return value
	}
	receiptID := fixture.firstReceipt.ReceiptID
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`, receiptID)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, receiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseDraining ||
		intents != 1 || activations != 1 || retirementComponents != 1 || retirementReceipts != 1 ||
		authorizations != 1 || handoffs != 1 || completions != 0 {
		t.Fatalf("interrupted Draining state phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			state.Phase, intents, activations, retirementComponents, retirementReceipts, authorizations, handoffs,
			completions, err)
	}
	componentState := ""
	if err := db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.migration.request.DeploymentID,
		fixture.componentName).Scan(&componentState); err != nil ||
		componentState != string(generatedruntimestate.ComponentActive) {
		t.Fatalf("interrupted Draining component state=%q error=%v", componentState, err)
	}
}

func assertGeneratedRouteColdAccountingActivatedSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	assertGeneratedRouteColdAccountingActivatedPhysicalSnapshot(t, fixture, true, true, "healthy")
}

func assertGeneratedRouteColdAccountingActivatedStoppedSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	assertGeneratedRouteColdAccountingActivatedPhysicalSnapshot(t, fixture, true, false, "")
}

func assertGeneratedRouteColdAccountingActivatedAbsentSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	assertGeneratedRouteColdAccountingActivatedPhysicalSnapshot(t, fixture, false, false, "")
}

func assertGeneratedRouteColdAccountingActivatedPhysicalSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture, predecessorExists, predecessorRunning bool,
	predecessorHealth string,
) {
	t.Helper()
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read activated predecessor durable count: %v", err)
		}
		return value
	}
	receiptID := fixture.firstReceipt.ReceiptID
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`, receiptID)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, receiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseDraining || intents != 1 || activations != 1 ||
		retirementComponents != 0 || retirementReceipts != 0 || authorizations != 0 || handoffs != 0 ||
		completions != 0 {
		t.Fatalf("activated predecessor state phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			state.Phase, intents, activations, retirementComponents, retirementReceipts, authorizations, handoffs,
			completions, err)
	}
	predecessor, predecessorFound := fixture.process.predecessor.physical[fixture.retiredContainerID]
	successor, successorFound := fixture.process.predecessor.physical[fixture.currentContainerID]
	if !predecessorFound || predecessor.exists != predecessorExists ||
		predecessor.running != predecessorRunning || predecessor.health != predecessorHealth ||
		!successorFound || !successor.exists || !successor.running || successor.health != "healthy" ||
		successor.policy.identity.ReceiptID != fixture.componentReceipt {
		t.Fatalf("activated predecessor physical=%#v/%t successor=%#v/%t", predecessor, predecessorFound,
			successor, successorFound)
	}
	expectedCapacity := fmt.Sprintf("%d %d\n", fixture.identity.options.MemoryBytes-1,
		fixture.identity.options.ReplacementDiskBytes)
	if string(fixture.construction.capacity) != expectedCapacity {
		t.Fatalf("activated predecessor constrained capacity=%q want=%q", fixture.construction.capacity,
			expectedCapacity)
	}
}

func assertGeneratedRouteColdAccountingRetiredSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read retired predecessor durable count: %v", err)
		}
		return value
	}
	receiptID := fixture.firstReceipt.ReceiptID
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`, receiptID)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, receiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseDraining || intents != 1 || activations != 1 ||
		retirementComponents != 1 || retirementReceipts != 1 || authorizations != 0 || handoffs != 0 ||
		completions != 0 {
		t.Fatalf("retired predecessor state phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			state.Phase, intents, activations, retirementComponents, retirementReceipts, authorizations, handoffs,
			completions, err)
	}
	componentState := ""
	if err := db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.migration.request.DeploymentID,
		fixture.componentName).Scan(&componentState); err != nil ||
		componentState != string(generatedruntimestate.ComponentActive) {
		t.Fatalf("retired predecessor successor component state=%q error=%v", componentState, err)
	}
	predecessor, predecessorFound := fixture.process.predecessor.physical[fixture.retiredContainerID]
	successor, successorFound := fixture.process.predecessor.physical[fixture.currentContainerID]
	if !predecessorFound || predecessor.exists || predecessor.running || !successorFound || !successor.exists ||
		!successor.running || successor.policy.identity.ReceiptID != fixture.componentReceipt {
		t.Fatalf("retired predecessor physical=%#v/%t successor=%#v/%t", predecessor, predecessorFound,
			successor, successorFound)
	}
	expectedCapacity := fmt.Sprintf("%d %d\n", fixture.identity.options.MemoryBytes-1,
		fixture.identity.options.ReplacementDiskBytes)
	if string(fixture.construction.capacity) != expectedCapacity {
		t.Fatalf("retired predecessor constrained capacity=%q want=%q", fixture.construction.capacity,
			expectedCapacity)
	}
}

func assertGeneratedRouteColdAccountingAuthorizedSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read authorized accounting handoff durable count: %v", err)
		}
		return value
	}
	receiptID := fixture.firstReceipt.ReceiptID
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`, receiptID)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, receiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseDraining || intents != 1 || activations != 1 ||
		retirementComponents != 1 || retirementReceipts != 1 || authorizations != 1 || handoffs != 0 ||
		completions != 0 {
		t.Fatalf("authorized accounting handoff state phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			state.Phase, intents, activations, retirementComponents, retirementReceipts, authorizations, handoffs,
			completions, err)
	}
	componentState := ""
	if err := db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.migration.request.DeploymentID,
		fixture.componentName).Scan(&componentState); err != nil ||
		componentState != string(generatedruntimestate.ComponentActive) {
		t.Fatalf("authorized accounting handoff successor component state=%q error=%v", componentState, err)
	}
	predecessor, predecessorFound := fixture.process.predecessor.physical[fixture.retiredContainerID]
	successor, successorFound := fixture.process.predecessor.physical[fixture.currentContainerID]
	if !predecessorFound || predecessor.exists || predecessor.running || !successorFound || !successor.exists ||
		!successor.running || successor.policy.identity.ReceiptID != fixture.componentReceipt {
		t.Fatalf("authorized accounting handoff predecessor=%#v/%t successor=%#v/%t", predecessor,
			predecessorFound, successor, successorFound)
	}
}

func generatedRouteColdAccountingAuthorization(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) generatedruntimestate.RouteAccountingHandoffAuthorization {
	t.Helper()
	var snapshot generatedruntimestate.GeneratedDomainStartupSnapshot
	err := withGeneratedRouteColdAccountingTransaction(context.Background(), fixture.identity.database,
		func(tx *sql.Tx) error {
			var err error
			snapshot, err = generatedruntimestate.New(fixture.identity.database).
				GeneratedDomainStartupSnapshotTx(context.Background(), tx)
			return err
		})
	if err != nil {
		t.Fatalf("read accounting handoff authorization: %v", err)
	}
	var matched []generatedruntimestate.RouteAccountingHandoffAuthorization
	for _, authorization := range snapshot.AccountingHandoffAuthorizations {
		if authorization.ActivationReceiptID == fixture.firstReceipt.ReceiptID {
			matched = append(matched, authorization)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("accounting handoff authorizations=%#v", matched)
	}
	return matched[0]
}

func assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read recovered handoff durable count: %v", err)
		}
		return value
	}
	receiptID := fixture.firstReceipt.ReceiptID
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, receiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseSucceeded || authorizations != 1 || handoffs != 1 ||
		completions != 1 {
		t.Fatalf("recovered handoff state phase=%s receipts=%d/%d/%d error=%v", state.Phase,
			authorizations, handoffs, completions, err)
	}
	assertGeneratedRouteCompletionState(t, db, fixture.firstReceipt, 1)
}

func assertGeneratedRouteColdAccountingInitialLegacyCurrentSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read initial legacy Current durable count: %v", err)
		}
		return value
	}
	receiptID := fixture.firstReceipt.ReceiptID
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`, receiptID)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, receiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, receiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, receiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseSucceeded || intents != 1 || activations != 1 ||
		retirementComponents != 0 || retirementReceipts != 1 || authorizations != 1 || handoffs != 1 ||
		completions != 1 {
		t.Fatalf("initial legacy Current state phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			state.Phase, intents, activations, retirementComponents, retirementReceipts, authorizations, handoffs,
			completions, err)
	}
	binding, err := fixture.migration.bindings.Read(context.Background(), fixture.migration.claim.JobID())
	if err != nil || binding.JobID() != fixture.migration.claim.JobID() ||
		binding.ApplicationID() != fixture.migration.guarded.appID ||
		binding.Mode() != deploymentbinding.ModeLegacyUninitialized || binding.DesiredRevision() != 0 ||
		binding.AuthorizingLifecycleJobID() != "" {
		t.Fatalf("initial legacy Current binding=%#v error=%v", binding, err)
	}
	assertGeneratedRouteCompletionState(t, db, fixture.firstReceipt, 1)
}

func assertGeneratedRouteColdAccountingRestoredCurrent(t *testing.T, result *generatedDomainConstruction,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	if result == nil || result.capacity == nil || result.budget == nil || result.candidates == nil ||
		result.candidates.coordinator != result.capacity || result.candidates.ledger == nil ||
		result.candidates.ledger.budget != result.budget {
		t.Fatal("cold construction did not publish one shared capacity coordinator")
	}
	ledger := result.candidates.ledger
	type debt struct {
		token  *generatedDomainCapacityDebtToken
		limits generatedcapacity.Limits
	}
	readDebt := func(deploymentID, receiptID string, physical candidatePhysicalState) debt {
		t.Helper()
		ledger.mu.Lock()
		group := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
			deploymentID: deploymentID}]
		ledger.mu.Unlock()
		if group == nil {
			t.Fatalf("cold construction omitted restored accounting group %s", deploymentID)
		}
		group.mu.Lock()
		component := group.byName[fixture.componentName]
		if group.ledger != ledger || len(group.components) != 1 || group.components[0] != fixture.componentName ||
			component == nil || component.receipt == nil || component.receipt.id != receiptID ||
			component.tokenHeld || component.charge == nil {
			group.mu.Unlock()
			t.Fatalf("cold construction published incomplete restored accounting component %s", deploymentID)
		}
		receiptWord := component.receipt.state.Load()
		charge := component.charge
		token, restored := charge.token.(*generatedDomainCapacityDebtToken)
		value := debt{token: token, limits: group.limits}
		validCharge := restored && token != nil && charge.state == candidateChargeActive &&
			charge.owner == candidateAccountingRouteActive && charge.lease == nil && !charge.logicallyRetired &&
			candidatePhysicalFromWord(receiptWord) == physical &&
			receiptWord&candidateReceiptAcknowledged != 0 && receiptWord&candidateReceiptObserved != 0 &&
			receiptWord&candidateReceiptUnquiesced == 0
		group.mu.Unlock()
		if !validCharge || value.limits.MemoryBytes == 0 || value.limits.DiskBytes == 0 ||
			!token.configured() || !token.published() {
			t.Fatalf("cold construction did not restore exact accounting debt %s", deploymentID)
		}
		return value
	}
	debts := []debt{readDebt(fixture.migration.request.DeploymentID, fixture.componentReceipt,
		candidatePhysicalHandedToRoute)}
	if fixture.restoresPredecessor {
		if fixture.lifecycleDeploymentID == "" || fixture.predecessorReceipt == "" {
			t.Fatal("cold construction omitted predecessor restored-debt identity")
		}
		debts = append(debts, readDebt(fixture.lifecycleDeploymentID, fixture.predecessorReceipt,
			fixture.predecessorPhysical))
	}
	var memory uint64
	var disk uint64
	for _, value := range debts {
		memory += value.limits.MemoryBytes
		disk += value.limits.DiskBytes
	}
	result.capacity.mu.Lock()
	validDebt := result.capacity.phase == generatedDomainCapacityPublished && result.capacity.borrows == 0 &&
		result.capacity.memory == memory && result.capacity.disk == disk &&
		len(result.capacity.tokens) == len(debts)
	for _, value := range debts {
		_, exactToken := result.capacity.tokens[value.token]
		validDebt = validDebt && exactToken && value.token.owner == result.capacity && value.token.active &&
			value.token.epoch != 0 && value.token.limits == value.limits
	}
	result.capacity.mu.Unlock()
	if !validDebt {
		t.Fatalf("cold construction did not publish exact restored debt tokens count=%d", len(debts))
	}
}

func assertGeneratedRouteColdAccountingDurableSnapshot(t *testing.T, fixture generatedRouteColdAccountingFixture) {
	t.Helper()
	db := fixture.identity.database
	jobStatus, jobPhase, jobAttempt := "", "", 0
	if err := db.QueryRow(`SELECT status,phase,attempt FROM jobs WHERE id=?`,
		fixture.migration.claim.JobID()).Scan(&jobStatus, &jobPhase, &jobAttempt); err != nil {
		t.Fatalf("read completed Current job: %v", err)
	}
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read completed Current durable count: %v", err)
		}
		return value
	}
	canonicalEvents := count(`SELECT COUNT(*) FROM job_events
		WHERE job_id=? AND attempt=? AND level='info' AND phase='succeeded'
		  AND code='job_succeeded' AND message='Deployment completed'`,
		fixture.migration.claim.JobID(), jobAttempt)
	successEvents := count(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND attempt=? AND code='job_succeeded'`,
		fixture.migration.claim.JobID(), jobAttempt)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE successor_deployment_id=?`, fixture.migration.request.DeploymentID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE successor_deployment_id=?`, fixture.migration.request.DeploymentID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE successor_deployment_id=?`, fixture.migration.request.DeploymentID)

	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatalf("construct completed Current lifecycle validator: %v", err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatalf("construct completed Current binding reader: %v", err)
	}
	binding, err := bindings.Read(context.Background(), fixture.migration.claim.JobID())
	if err != nil {
		t.Fatalf("read completed Current binding: %v", err)
	}
	operation := deploymentbinding.OperationRef{ApplicationID: binding.ApplicationID(),
		OperationID: binding.AuthorizingLifecycleJobID(), DesiredRevision: binding.DesiredRevision()}
	completion, err := lifecycleValidatorCurrentOperation(context.Background(), db, validator, operation)
	if err != nil {
		t.Fatalf("validate completed Current lifecycle operation: %v", err)
	}
	unresolved, err := lifecycleValidatorUnresolvedHistory(context.Background(), db, validator,
		fixture.migration.guarded.appID)
	if err != nil {
		t.Fatalf("validate completed Current lifecycle history: %v", err)
	}
	lifecycleMatches := count(`SELECT COUNT(*) FROM application_lifecycle_operations
		WHERE app_id=? AND job_id=? AND deployment_id=? AND desired_revision=?`, operation.ApplicationID,
		operation.OperationID, fixture.lifecycleDeploymentID, operation.DesiredRevision)
	componentState := ""
	if err := db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.migration.request.DeploymentID,
		fixture.componentName).Scan(&componentState); err != nil {
		t.Fatalf("read completed Current component state: %v", err)
	}
	if jobStatus != "succeeded" || jobPhase != "succeeded" || jobAttempt < 1 || canonicalEvents != 1 ||
		successEvents != 1 || authorizations != 1 || handoffs != 1 || completions != 1 ||
		binding.JobID() != fixture.migration.claim.JobID() || binding.ApplicationID() != fixture.migration.guarded.appID ||
		binding.Mode() != deploymentbinding.ModeRunning || binding.DesiredRevision() < 1 ||
		binding.AuthorizingLifecycleJobID() == "" || completion != deploymentbinding.CompletionComplete ||
		unresolved || lifecycleMatches != 1 || componentState != string(generatedruntimestate.ComponentActive) {
		t.Fatalf("completed Current durable state job=%s/%s/%d events=%d/%d "+
			"authorization=%d handoff=%d completion=%d binding=%s/%d lifecycle=%d unresolved=%t component=%s",
			jobStatus, jobPhase, jobAttempt, canonicalEvents, successEvents, authorizations, handoffs, completions,
			binding.Mode(), binding.DesiredRevision(), lifecycleMatches, unresolved, componentState)
	}
}

func lifecycleValidatorCurrentOperation(ctx context.Context, db *sql.DB,
	validator *applifecycle.DeploymentBindingValidator, operation deploymentbinding.OperationRef,
) (deploymentbinding.Completion, error) {
	var result deploymentbinding.Completion
	err := withGeneratedRouteColdAccountingTransaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		result, err = validator.CurrentOperationTx(ctx, tx, operation)
		return err
	})
	return result, err
}

func lifecycleValidatorUnresolvedHistory(ctx context.Context, db *sql.DB,
	validator *applifecycle.DeploymentBindingValidator, appID string,
) (bool, error) {
	var result bool
	err := withGeneratedRouteColdAccountingTransaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		result, err = validator.UnresolvedHistoryTx(ctx, tx, appID)
		return err
	})
	return result, err
}

func withGeneratedRouteColdAccountingTransaction(ctx context.Context, db *sql.DB,
	read func(*sql.Tx) error,
) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return read(tx)
}

func newGeneratedRouteColdAccountingSuccessorMigration(t *testing.T, first *generatedMigrationFixture,
	jobService *jobs.Service, workingDirectory string,
) *generatedMigrationFixture {
	t.Helper()
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), workingDirectory)
	if err != nil {
		t.Fatalf("acquire distinct replacement effects scope: %v", err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release distinct replacement effects scope: %v", err)
		}
	})
	guarded := *first.guarded
	guarded.context = deploymenteffects.WithScope(context.Background(), scope)
	restarted := *first
	restarted.guarded = &guarded
	restarted.releaseEffects = release
	return newGeneratedRouteSuccessorMigration(t, &restarted, jobService)
}

func newGeneratedRouteColdAccountingMultiSuccessorMigration(t *testing.T,
	first *generatedMigrationFixture, componentNames []string,
) *generatedMigrationFixture {
	t.Helper()
	if first == nil || first.jobService == nil || len(componentNames) < 2 {
		t.Fatal("multi-component successor requires a canonical migration and component roster")
	}
	database := first.repository.db
	created, err := first.jobService.CreateDeploymentWithInputTransaction(context.Background(),
		jobs.DeploymentCreateRequest{ApplicationID: first.guarded.appID,
			IdempotencyKey: "route-multi-successor-" + uuid.NewString(), RequestedBy: first.guarded.actorID,
			Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create multi-component successor job: created=%t error=%v", created.Created, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`UPDATE jobs SET status='running',phase='running',attempt=2,started_at=?,updated_at=? WHERE id=?`,
		now, now, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	deployment, _, err := first.repository.GetOrCreateByJob(context.Background(), first.guarded.appID,
		created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = first.repository.InitializeRuntime(context.Background(), first.guarded.appID, deployment.ID,
		first.request.ReleaseID, first.request.ConfigurationRevisionID, first.request.ConfigurationRevisionNumber,
		deployments.RuntimeGeneratedNode, first.request.DeploymentPlanRevisionID,
		first.request.DeploymentPlanRevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, first.guarded.appID, 2)
	if err != nil {
		t.Fatalf("construct multi-component successor claim: %v", err)
	}
	if err := first.repository.Gate(first.guarded.context,
		deployments.GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatalf("gate multi-component successor: %v", err)
	}
	state := generatedruntimestate.New(database)
	runtimeDeployment, wasCreated, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: first.guarded.appID, ReleaseID: first.request.ReleaseID,
		DeploymentPlanRevisionID:     first.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: first.request.DeploymentPlanRevisionNumber,
		ComponentNames:               append([]string(nil), componentNames...),
	})
	if err != nil || !wasCreated {
		t.Fatalf("begin multi-component successor runtime: created=%t error=%v", wasCreated, err)
	}
	runtimeDeployment, err = state.Advance(context.Background(), first.guarded.appID, deployment.ID,
		runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatal(err)
	}
	artifactID := ""
	for _, component := range componentNames {
		var componentArtifactID string
		if err := database.QueryRow(`SELECT id FROM generated_image_artifacts
			WHERE release_id=? AND deployment_plan_revision_id=? AND component_id=? AND state='ready'
			ORDER BY attempt_number DESC,id LIMIT 1`, first.request.ReleaseID,
			first.request.DeploymentPlanRevisionID, component).Scan(&componentArtifactID); err != nil {
			t.Fatalf("read %s successor artifact: %v", component, err)
		}
		if _, err := state.SetImageReady(context.Background(), first.guarded.appID, deployment.ID,
			component, componentArtifactID); err != nil {
			t.Fatalf("record %s successor image: %v", component, err)
		}
		if component == "api" {
			artifactID = componentArtifactID
		}
	}
	if artifactID == "" {
		t.Fatal("multi-component successor lacks the api artifact")
	}
	if _, err := state.Advance(context.Background(), first.guarded.appID, deployment.ID,
		runtimeDeployment.Phase, generatedruntimestate.PhaseMigrating, ""); err != nil {
		t.Fatal(err)
	}
	return &generatedMigrationFixture{repository: first.repository, bindings: first.bindings,
		jobService: first.jobService, plans: first.plans, configuration: first.configuration,
		guarded: first.guarded, releaseEffects: first.releaseEffects, plan: first.plan, claim: claim,
		binding: created.Binding, request: candidateMigrationRequestFixture{DeploymentID: deployment.ID,
			ReleaseID: first.request.ReleaseID, ConfigurationRevisionID: first.request.ConfigurationRevisionID,
			ConfigurationRevisionNumber:  first.request.ConfigurationRevisionNumber,
			DeploymentPlanRevisionID:     first.request.DeploymentPlanRevisionID,
			DeploymentPlanRevisionNumber: first.request.DeploymentPlanRevisionNumber,
			ArtifactID:                   artifactID, ArtifactAttemptNumber: first.request.ArtifactAttemptNumber}}
}

func prepareGeneratedRouteSequentialCandidates(t *testing.T, owner *candidateOwnerFixture,
	componentNames, containerIDs []string,
) (GeneratedReplacementReservation, *GeneratedCandidateExecution, []CandidateIdentity) {
	t.Helper()
	if owner == nil || len(componentNames) < 2 || len(componentNames) != len(containerIDs) {
		t.Fatal("multi-component candidate requires exact component and container rosters")
	}
	reservation := owner.reserve(t)
	execution := owner.pin(t, reservation)
	invocations := make([]GeneratedCandidateInvocation, 0, len(componentNames))
	identities := make([]CandidateIdentity, 0, len(componentNames))
	for index, name := range componentNames {
		invocation, err := execution.BeginCreate(name)
		if err != nil {
			t.Fatalf("begin %s candidate create: %v", name, err)
		}
		permit, ok := invocation.(*generatedCandidateInvocationPermit)
		if !ok || permit == nil || !validContainerID(containerIDs[index]) {
			t.Fatalf("%s candidate invocation=%T container=%q", name, invocation, containerIDs[index])
		}
		owner.runner.mu.Lock()
		owner.runner.containerID = containerIDs[index]
		owner.runner.physical = candidateOwnerPhysical{policy: permit.policy}
		owner.runner.mu.Unlock()
		identity, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatalf("create %s candidate: %v", name, err)
		}
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatalf("start %s candidate: %v", name, err)
		}
		invocations = append(invocations, invocation)
		identities = append(identities, identity)
	}
	state, err := execution.FinishStarting()
	if err != nil || state.Phase != generatedruntimestate.PhaseWaitingHealth {
		t.Fatalf("finish multi-component candidate starting=%#v error=%v", state, err)
	}
	for index, invocation := range invocations {
		if err := execution.WaitHealthyAndRecord(invocation); err != nil {
			t.Fatalf("observe %s candidate health: %v", componentNames[index], err)
		}
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	return reservation, execution, identities
}

func establishGeneratedRoutePredecessorLifecycleRoster(t *testing.T, migration *generatedMigrationFixture,
	head generatedruntimestate.ActiveHead, physical []candidateOwnerPhysical,
) {
	t.Helper()
	if migration == nil || len(physical) < 2 {
		t.Fatal("multi-component predecessor lifecycle requires a canonical migration and roster")
	}
	sort.Slice(physical, func(i, j int) bool {
		return physical[i].policy.identity.Component < physical[j].policy.identity.Component
	})
	configuration, err := migration.configuration.Get(context.Background(), migration.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	components := make([]applifecycle.RetainedComponent, 0, len(physical))
	runtimeComponents := make([]string, 0, len(physical))
	for _, value := range physical {
		policy := value.policy
		if !value.exists || !value.running || value.containerID == "" ||
			policy.identity.DeploymentID != head.DeploymentID || policy.identity.Slot != head.Slot {
			t.Fatalf("multi-component retained physical=%#v head=%#v", value, head)
		}
		technology := policy.identity.Technology
		if technology == "" {
			for _, component := range migration.plan.Components {
				if component.Name == policy.identity.Component && component.NodeVersion != "" {
					technology = "node"
					break
				}
			}
		}
		runtimeComponents = append(runtimeComponents, policy.identity.Component)
		components = append(components, applifecycle.RetainedComponent{
			Name: policy.identity.Component, Role: policy.identity.Role, Technology: technology,
			ContainerID: value.containerID, ContainerName: policy.identity.ContainerName,
			ArtifactID: policy.identity.ArtifactID, ImageContentID: policy.identity.ImageContentID,
			BuildDefinitionDigest: policy.buildDefinitionDigest, NetworkName: policy.identity.NetworkName,
			NetworkAlias: policy.identity.NetworkAlias, InternalPort: policy.internalPort,
			WorkingDirectory: policy.identity.WorkingDirectory,
			RunCommandDigest: policy.identity.RunCommandDigest,
			Limits: applifecycle.RetainedLimits{MemoryBytes: policy.limits.MemoryBytes,
				MemorySwapBytes: policy.limits.MemoryBytes, CPUQuota: policy.limits.MilliCPUs * 100,
				CPUPeriod: 100000, PIDsLimit: policy.limits.PIDs, TmpfsBytes: policy.limits.TmpfsBytes,
				LogDriver: "local", LogMaxSize: policy.limits.LogSize, LogMaxFiles: policy.limits.LogFiles,
				HardeningPolicyVersion: "v1"},
		})
	}
	repository := applifecycle.New(migration.repository.db)
	service := jobs.New(migration.repository.db)
	pins := applifecycle.GeneratedLifecyclePinsV1{Version: 1, DeploymentID: head.DeploymentID,
		ReleaseID: physical[0].policy.identity.ReleaseID, Slot: head.Slot,
		ActiveGeneration: head.Generation,
		PlanRevision: applifecycle.RevisionRef{ID: migration.request.DeploymentPlanRevisionID,
			Number: migration.request.DeploymentPlanRevisionNumber},
		Configuration: applifecycle.ConfigurationRef{RevisionID: migration.request.ConfigurationRevisionID,
			RevisionNumber: migration.request.ConfigurationRevisionNumber, FormatVersion: configuration.FormatVersion,
			PlanRevision: applifecycle.RevisionRef{ID: migration.request.DeploymentPlanRevisionID,
				Number: migration.request.DeploymentPlanRevisionNumber}, RuntimeComponents: runtimeComponents},
		BaselineDisposition: applifecycle.BaselineRunning, Components: components,
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind",
			SelectedLineageGeneration: 1, SelectedLineageOperationID: uuid.NewString(),
			SelectedLineageTerminalDigest: strings.Repeat("f", 64), ProtectedFormatVersion: 2,
			TargetRecordDigest: strings.Repeat("a", 64)}}
	accepted, err := repository.Accept(context.Background(), service, applifecycle.LifecycleRequest{
		ApplicationID: migration.guarded.appID, ActorID: migration.guarded.actorID,
		IdempotencyKey: "route-multi-predecessor-authority-" + uuid.NewString(), Action: applifecycle.Start,
		ExpectedDesiredRevision: 0}, pins)
	if err != nil || !accepted.Created {
		t.Fatalf("accept multi-component predecessor lifecycle: created=%t error=%v", accepted.Created, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := migration.repository.db.Exec(`UPDATE jobs
		SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=? AND status='queued'`,
		now, now, accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("start multi-component predecessor lifecycle changed=%d error=%v", changed, err)
	}
	job, err := service.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(job)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []generatedRouteLifecycleEffectSpec{
		{boundary: applifecycle.BoundaryHealth, mode: applifecycle.EffectObservationOnly,
			outcome: applifecycle.OutcomeHealthy},
		{boundary: applifecycle.BoundaryPublish, mode: applifecycle.EffectCommand,
			outcome: applifecycle.OutcomeServing},
		{boundary: applifecycle.BoundaryComplete, mode: applifecycle.EffectObservationOnly,
			outcome: applifecycle.OutcomeCompleteRunning},
	} {
		tx, err := migration.repository.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		request := applifecycle.EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID,
			ApplicationID: accepted.Operation.ApplicationID, DesiredRevision: accepted.Operation.DesiredRevision,
			Boundary: spec.boundary, ComponentName: spec.component, Mode: spec.mode,
			PinsDigest: accepted.Operation.PinsDigest}
		intent, created, effectErr := repository.BeginBoundaryTx(context.Background(), tx, claim,
			accepted.Operation, request)
		if effectErr == nil && !created {
			effectErr = errors.New("multi-component predecessor lifecycle intent was not newly retained")
		}
		if effectErr == nil {
			proof := applifecycle.EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID,
				ApplicationID:   accepted.Operation.ApplicationID,
				DesiredRevision: accepted.Operation.DesiredRevision, Boundary: spec.boundary,
				ComponentName: spec.component, Outcome: spec.outcome, IntentDigest: intent.RequestDigest,
				EvidenceDigest: strings.Repeat("e", 64), PinsDigest: accepted.Operation.PinsDigest}
			var observed bool
			if spec.boundary == applifecycle.BoundaryComplete {
				_, observed, effectErr = repository.CompleteTx(context.Background(), tx, claim, intent, proof)
			} else {
				_, observed, effectErr = repository.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof)
			}
			if effectErr == nil && !observed {
				effectErr = errors.New("multi-component predecessor lifecycle receipt was not newly retained")
			}
		}
		if effectErr == nil {
			effectErr = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if effectErr != nil {
			t.Fatalf("complete multi-component predecessor lifecycle %s: %v", spec.boundary, effectErr)
		}
	}
	finishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err = migration.repository.db.Exec(`UPDATE jobs
		SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=?
		WHERE id=? AND status='running'`, finishedAt, finishedAt, accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("complete multi-component predecessor lifecycle changed=%d error=%v", changed, err)
	}
}

type generatedRouteColdAccountingFixture struct {
	identity              generatedDomainIdentityFixture
	migration             *generatedMigrationFixture
	jobs                  *jobs.Service
	process               *generatedRouteColdAccountingRunner
	routes                *generatedRoutePredecessorRunner
	predecessor           *candidateOwnerRunner
	construction          *generatedDomainConstructionRunner
	environment           *candidateFactoryEnvironment
	firstReceipt          generatedruntimestate.RouteActivationReceipt
	componentName         string
	componentReceipt      string
	predecessorReceipt    string
	lifecycleDeploymentID string
	restoresPredecessor   bool
	predecessorPhysical   candidatePhysicalState
	retiredContainerID    string
	currentContainerID    string
}

type generatedRouteColdAccountingRunner struct {
	mu                  sync.RWMutex
	cold                runtimeprocess.CommandRunner
	routes              *generatedRoutePredecessorRunner
	predecessor         *generatedRouteCandidateRunner
	predecessorPhysical []candidateOwnerPhysical
	candidate           *candidateOwnerRunner
}

func (r *generatedRouteColdAccountingRunner) setCandidate(candidate *candidateOwnerRunner) {
	r.mu.Lock()
	r.candidate = candidate
	r.mu.Unlock()
}

func (r *generatedRouteColdAccountingRunner) setDelegate(delegate runtimeprocess.CommandRunner) {
	r.mu.Lock()
	r.cold = delegate
	r.mu.Unlock()
}

func (r *generatedRouteColdAccountingRunner) setRoutes(routes *generatedRoutePredecessorRunner) {
	r.mu.Lock()
	r.routes = routes
	r.mu.Unlock()
}

func (r *generatedRouteColdAccountingRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.RLock()
	candidate := r.candidate
	predecessor := r.predecessor
	predecessorPhysical := append([]candidateOwnerPhysical(nil), r.predecessorPhysical...)
	delegate := r.cold
	routes := r.routes
	r.mu.RUnlock()
	kind := candidateOwnerKind(request)
	if kind != "" {
		if candidateOwnerRunnerMatches(candidate, kind, request) {
			return candidate.Run(ctx, request)
		}
		if predecessor != nil {
			for _, physical := range predecessorPhysical {
				if !candidateOwnerPhysicalMatches(physical, kind, request) {
					continue
				}
				if kind == candidateOwnerImageInspect || kind == candidateOwnerNetworkInspect {
					exact := &candidateOwnerRunner{physical: physical}
					return exact.defaultResult(kind, request)
				}
				return predecessor.Run(ctx, request)
			}
		}
	}
	if generatedRouteColdAccountingRouteCommand(routes, request.Args) {
		return routes.Run(ctx, request)
	}
	return delegate.Run(ctx, request)
}

func generatedRouteColdAccountingRouteCommand(routes *generatedRoutePredecessorRunner, args []string) bool {
	if routes == nil || routes.ingressRunner == nil || len(args) < 3 {
		return false
	}
	target := args[len(args)-1]
	switch {
	case len(args) == 5 && args[0] == "image" && args[1] == "inspect" &&
		args[2] == "--format" && args[3] == imageInspectFormat:
		return target == caddyImage
	case len(args) == 5 && args[0] == "volume" && args[1] == "inspect" &&
		args[2] == "--format" && args[3] == volumeInspectFormat:
		return target == caddyVolumeName
	case len(args) == 5 && args[0] == "network" && args[1] == "inspect" && args[2] == "--format":
		if target == caddyNetworkName {
			return args[3] == networkInspectFormat || args[3] == "json"
		}
		if args[3] != networkInspectFormat {
			return false
		}
		for _, endpoint := range routes.endpoints {
			if target == endpoint.endpoint.NetworkName {
				return true
			}
		}
	case len(args) == 5 && args[0] == "container" && args[1] == "inspect" &&
		args[2] == "--format":
		if target == caddyContainerName && args[3] == caddyInspectFormat {
			return true
		}
		_, exists := routes.endpoints[target]
		return exists && args[3] == endpointInspectFormat
	case args[0] == "container" && args[1] == "exec":
		if len(args) < 4 || !generatedRouteColdAccountingRouteExec(args, routes) {
			return false
		}
		for id := range routes.ingressContainers {
			if normalizeID(args[2]) == normalizeID(id) {
				return true
			}
		}
	}
	return false
}

func generatedRouteColdAccountingRouteExec(args []string, routes *generatedRoutePredecessorRunner) bool {
	if reflect.DeepEqual(args[3:], []string{
		"curl", "--silent", "--show-error", "--fail", "--max-time", "10", "http://127.0.0.1:2019/config/",
	}) {
		return true
	}
	if len(args) != 21 || !reflect.DeepEqual(args[3:20], []string{
		"curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
	}) {
		return false
	}
	for _, endpoint := range routes.endpoints {
		address := endpoint.endpoint.NetworkAlias + "." + endpoint.endpoint.NetworkName + ":" +
			strconv.FormatUint(uint64(endpoint.endpoint.InternalPort), 10)
		if args[20] == "http://"+address+"/" {
			return true
		}
	}
	return false
}

func candidateOwnerRunnerMatches(runner *candidateOwnerRunner, kind candidateOwnerCommandKind,
	request runtimeprocess.CommandRequest,
) bool {
	if runner == nil {
		return false
	}
	if kind == candidateOwnerCreate || kind == candidateOwnerStart {
		return true
	}
	return candidateOwnerPhysicalMatches(runner.snapshot(), kind, request)
}

func candidateOwnerPhysicalMatches(physical candidateOwnerPhysical, kind candidateOwnerCommandKind,
	request runtimeprocess.CommandRequest,
) bool {
	if (kind == candidateOwnerImageInspect || kind == candidateOwnerNetworkInspect || kind == candidateOwnerInspect) &&
		!generatedDomainCandidateInspectionCommand(request.Args) {
		return false
	}
	if len(request.Args) == 0 {
		return false
	}
	target := request.Args[len(request.Args)-1]
	switch kind {
	case candidateOwnerImageInspect:
		return target == physical.policy.identity.ImageContentID
	case candidateOwnerNetworkInspect:
		return target == physical.policy.identity.NetworkName
	case candidateOwnerInspect, candidateOwnerStop, candidateOwnerRemove:
		return target == physical.containerID || target == physical.policy.identity.ContainerName
	default:
		return false
	}
}

type generatedRouteColdAccountingCompletionStage uint8

const (
	generatedRouteColdAccountingInitialComplete generatedRouteColdAccountingCompletionStage = iota + 1
	generatedRouteColdAccountingLifecycleComplete
	generatedRouteColdAccountingLifecycleDraining
	generatedRouteColdAccountingLifecycleRetired
	generatedRouteColdAccountingLifecycleAuthorized
	generatedRouteColdAccountingLifecycleActivated
)

func newGeneratedRouteColdAccountingFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleComplete)
}

func newGeneratedRouteColdAccountingDrainingFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleDraining)
}

func newGeneratedRouteColdAccountingRetiredFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleRetired)
}

func newGeneratedRouteColdAccountingAuthorizedFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleAuthorized)
}

func newGeneratedRouteColdAccountingActivatedFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleActivated)
}

func newGeneratedRouteColdAccountingActivatedStoppedFixture(t *testing.T) generatedRouteColdAccountingFixture {
	t.Helper()
	fixture := newGeneratedRouteColdAccountingActivatedFixture(t)
	physical := fixture.process.predecessor.physical[fixture.retiredContainerID]
	physical.running = false
	physical.health = ""
	fixture.process.predecessor.physical[fixture.retiredContainerID] = physical
	return fixture
}

func newGeneratedRouteColdAccountingActivatedAbsentFixture(t *testing.T) generatedRouteColdAccountingFixture {
	t.Helper()
	fixture := newGeneratedRouteColdAccountingActivatedFixture(t)
	physical := fixture.process.predecessor.physical[fixture.retiredContainerID]
	physical.exists = false
	physical.running = false
	physical.health = ""
	fixture.process.predecessor.physical[fixture.retiredContainerID] = physical
	filtered := make([]generatedDomainDockerObject, 0, len(fixture.construction.delegate.objects))
	for _, object := range fixture.construction.delegate.objects {
		if object.kind == "container" && normalizeID(object.id) == normalizeID(fixture.retiredContainerID) {
			continue
		}
		filtered = append(filtered, object)
	}
	fixture.construction.delegate.objects = filtered
	return fixture
}

func newGeneratedRouteColdAccountingInitialCurrentFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingInitialComplete)
}

func newGeneratedRouteColdAccountingFixtureAtCompletion(t *testing.T,
	stage generatedRouteColdAccountingCompletionStage,
) generatedRouteColdAccountingFixture {
	t.Helper()
	if stage != generatedRouteColdAccountingInitialComplete &&
		stage != generatedRouteColdAccountingLifecycleComplete &&
		stage != generatedRouteColdAccountingLifecycleDraining &&
		stage != generatedRouteColdAccountingLifecycleRetired &&
		stage != generatedRouteColdAccountingLifecycleAuthorized &&
		stage != generatedRouteColdAccountingLifecycleActivated {
		t.Fatal("cold accounting fixture requires an exact completion stage")
	}
	identity := newGeneratedDomainIdentityFixture(t)
	migration, jobService := newGeneratedRouteCanonicalMigrationOnDatabase(t, identity.database,
		identity.dataRoot, identity.options.WorkingDirectory)
	identity.stores = migration.repository.stores
	validation := newGeneratedRouteSuccessorValidation(t, migration)
	owner, candidateRunner := newGeneratedRouteColdAccountingSequentialOwner(t, validation,
		generatedDomainCandidateOptions(identity.options))
	execution, candidateIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)

	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, _, ingressIP := ingressNetworkCandidate(0)
	ingress := &ingressRunner{appID: migration.guarded.appID, network: candidateIdentity.NetworkName,
		endpoint: generatedRouteEndpoint(candidateIdentity),
		caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
			GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
		ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
			Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
	physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
		slot: generatedidentity.Slot(candidateIdentity.Slot), endpoints: map[string]generatedRouteEndpointSnapshot{
			candidateIdentity.ContainerID: {appID: migration.guarded.appID,
				endpoint: generatedRouteEndpoint(candidateIdentity), slot: generatedidentity.Slot(candidateIdentity.Slot)},
		}}
	routeRunner := &generatedDomainTwoComponentRouteRunner{daemonID: "generated-domain-cold-accounting",
		delegate: physicalRoutes}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database,
		stores: identity.stores, runner: routeRunner, options: identity.options}, store)
	if err != nil {
		t.Fatal(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores,
		routeRunner, identity.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		t.Fatal(err)
	}
	routes, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := routes.Pin(migration.guarded.context, execution,
		generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
			ToSlot:    generatedidentity.Slot(candidateIdentity.Slot),
			Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(candidateIdentity)}})
	if err != nil {
		t.Fatal(err)
	}
	_, retirement, err := sealed.ActivateWithPredecessorRetirement(migration.guarded.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(retirement.Leave)
	if err := retirement.Complete(context.Background()); err != nil {
		t.Fatalf("complete authentic empty-predecessor Current: %v", err)
	}
	firstHead, err := generatedruntimestate.New(identity.database).Active(context.Background(), migration.guarded.appID)
	if err != nil || firstHead.DeploymentID != migration.request.DeploymentID {
		t.Fatalf("read first completed Current head=%#v error=%v", firstHead, err)
	}
	currentReceipt := retirement.state.receipt
	lifecycleDeploymentID := ""
	predecessorReceipt := ""
	retiredContainerID := ""
	effectsMigration := migration
	if stage != generatedRouteColdAccountingInitialComplete {
		retiredContainerID = candidateIdentity.ContainerID
		predecessorReceipt = candidateIdentity.ReceiptID
		establishGeneratedRoutePredecessorLifecycle(t, owner, firstHead)

		secondMigration := newGeneratedRouteSuccessorMigration(t, migration, jobService)
		owner.validation = newGeneratedRouteSuccessorValidation(t, secondMigration)
		owner.runner.containerID = strings.Repeat("8", 64)
		secondExecution, secondIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)
		currentRoute, err := manager.Observe(context.Background(), migration.guarded.appID)
		if err != nil {
			t.Fatalf("observe first completed Current route: %v", err)
		}
		secondRequest := generatedRouteRequest(migration.guarded.appID, secondIdentity)
		secondRequest.FromSlot = currentRoute.Slot
		secondSealed, err := routes.Pin(secondMigration.guarded.context, secondExecution, secondRequest)
		if err != nil {
			t.Fatalf("pin lifecycle-bound Current: %v", err)
		}
		physicalRoutes.network = secondIdentity.NetworkName
		physicalRoutes.endpoint = generatedRouteEndpoint(secondIdentity)
		physicalRoutes.slot = generatedidentity.Slot(secondIdentity.Slot)
		physicalRoutes.endpoints[secondIdentity.ContainerID] = generatedRouteEndpointSnapshot{
			appID: migration.guarded.appID, endpoint: generatedRouteEndpoint(secondIdentity),
			slot: generatedidentity.Slot(secondIdentity.Slot),
		}
		_, secondRetirement, err := secondSealed.ActivateWithPredecessorRetirement(secondMigration.guarded.context)
		if err != nil || secondRetirement == nil {
			t.Fatalf("activate lifecycle-bound Current retirement=%#v error=%v", secondRetirement, err)
		}
		t.Cleanup(secondRetirement.Leave)
		if stage == generatedRouteColdAccountingLifecycleComplete {
			if err := secondRetirement.Complete(context.Background()); err != nil {
				t.Fatalf("complete lifecycle-bound Current: %v", err)
			}
		} else if stage != generatedRouteColdAccountingLifecycleActivated {
			if retired, err := secondRetirement.Retire(context.Background()); err != nil ||
				retired.ActivationReceiptID != secondRetirement.state.receipt.ReceiptID ||
				retired.SuccessorDeploymentID != secondMigration.request.DeploymentID {
				t.Fatalf("retire interrupted Draining predecessor=%#v error=%v", retired, err)
			}
			if stage == generatedRouteColdAccountingLifecycleDraining {
				handoff, err := secondRetirement.completeAccountingHandoff(context.Background())
				if err != nil || handoff.ActivationReceiptID != secondRetirement.state.receipt.ReceiptID ||
					handoff.SuccessorDeploymentID != secondMigration.request.DeploymentID {
					t.Fatalf("complete interrupted Draining accounting handoff=%#v error=%v", handoff, err)
				}
			} else if stage == generatedRouteColdAccountingLifecycleAuthorized {
				const trigger = "generated_route_cold_authorized_handoff_fault"
				if _, err := identity.database.Exec(`CREATE TRIGGER ` + trigger + `
					BEFORE INSERT ON generated_route_accounting_handoff_receipts
					BEGIN SELECT RAISE(ABORT, 'fixed authorized handoff interruption'); END`); err != nil {
					t.Fatalf("install authorized handoff interruption: %v", err)
				}
				if handoff, err := secondRetirement.completeAccountingHandoff(context.Background()); err == nil ||
					handoff.ActivationReceiptID != "" {
					t.Fatalf("interrupt authorized accounting handoff=%#v error=%v", handoff, err)
				}
				if _, err := identity.database.Exec(`DROP TRIGGER ` + trigger); err != nil {
					t.Fatalf("remove authorized handoff interruption: %v", err)
				}
			}
		}
		currentReceipt = secondRetirement.state.receipt
		secondRetirement.Leave()
		secondExecution.Leave()
		lifecycleDeploymentID = firstHead.DeploymentID
		migration = secondMigration
		candidateIdentity = secondIdentity
	}
	retirement.Leave()
	execution.Leave()
	if err := effectsMigration.releaseEffects(); err != nil {
		t.Fatalf("release completed Current fixture effects before restart: %v", err)
	}

	stateValue, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	_, v2, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, stateValue, v2, journal, gatewayTopologyExactV1Only)
	observation.V1Container.ID = strings.Repeat("d", 64)
	observation.V1NetworkID = strings.Repeat("e", 64)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = observation.V1Container.ID
		case caddyNetworkName:
			objects[index].id = observation.V1NetworkID
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
		}
	}
	physicalCandidate, found := candidateRunner.physical[candidateIdentity.ContainerID]
	physicalReceipt := physicalCandidate.receiptID
	if physicalReceipt == "" {
		physicalReceipt = physicalCandidate.policy.identity.ReceiptID
	}
	if !found || physicalCandidate.containerID != candidateIdentity.ContainerID ||
		physicalReceipt != candidateIdentity.ReceiptID || !physicalCandidate.exists || !physicalCandidate.running ||
		physicalCandidate.health != "healthy" || physicalCandidate.policy.identity.Component == "" {
		t.Fatal("completed Current is absent from the authentic sequential candidate runner")
	}
	applicationPhysical := []candidateOwnerPhysical{physicalCandidate}
	retirementPhysical := physicalCandidate
	predecessorState := candidatePhysicalConfirmedAbsent
	if stage == generatedRouteColdAccountingLifecycleActivated {
		predecessor, predecessorFound := candidateRunner.physical[retiredContainerID]
		predecessorReceiptID := predecessor.receiptID
		if predecessorReceiptID == "" {
			predecessorReceiptID = predecessor.policy.identity.ReceiptID
		}
		if !predecessorFound || predecessor.containerID != retiredContainerID ||
			predecessorReceiptID != predecessorReceipt || !predecessor.exists || !predecessor.running ||
			predecessor.health != "healthy" || predecessor.policy.identity.Component == "" {
			t.Fatal("activated predecessor is absent from the authentic sequential candidate runner")
		}
		applicationPhysical = append([]candidateOwnerPhysical{predecessor}, physicalCandidate)
		retirementPhysical = predecessor
		// Durable Draining ownership is handed to the active route even while
		// its exact predecessor container remains physically running.
		predecessorState = candidatePhysicalHandedToRoute
	} else {
		healthyCandidate := physicalCandidate
		physicalCandidate.health = "unhealthy"
		comparison := physicalCandidate
		comparison.health = healthyCandidate.health
		if !reflect.DeepEqual(comparison, healthyCandidate) {
			t.Fatal("health-only Current degradation changed authenticated physical identity")
		}
		candidateRunner.physical[candidateIdentity.ContainerID] = physicalCandidate
		if !reflect.DeepEqual(candidateRunner.physical[candidateIdentity.ContainerID], physicalCandidate) {
			t.Fatal("authentic sequential runner did not retain the health-only Current observation")
		}
		applicationPhysical[0] = physicalCandidate
	}
	objects = append(objects, generatedDomainA0ApplicationObjects(t, applicationPhysical)...)
	builderObjects, builder := generatedDomainTwoComponentBuilderObjects(t, identity.dataRoot)
	objects = append(objects, builderObjects...)
	identity.options.HostPort = journal.Source.LocalHostPort
	cold := &generatedDomainConstructionRunner{roster: candidateRunner, delegate: &generatedDomainRecoveryCensusRunner{
		daemonID: "generated-domain-cold-accounting", objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	if stage == generatedRouteColdAccountingLifecycleDraining ||
		stage == generatedRouteColdAccountingLifecycleRetired ||
		stage == generatedRouteColdAccountingLifecycleAuthorized ||
		stage == generatedRouteColdAccountingLifecycleActivated {
		if identity.options.MemoryBytes <= 1 || identity.options.ReplacementDiskBytes == 0 {
			t.Fatal("interrupted Draining fixture has invalid capacity limits")
		}
		cold.capacity = []byte(fmt.Sprintf("%d %d\n", identity.options.MemoryBytes-1,
			identity.options.ReplacementDiskBytes))
	}
	builder.runner = &generatedDomainTwoComponentRouteRunner{delegate: cold, builder: &builder.physical}
	process := &generatedRouteColdAccountingRunner{cold: builder.runner,
		predecessor: candidateRunner, predecessorPhysical: []candidateOwnerPhysical{retirementPhysical}}

	return generatedRouteColdAccountingFixture{identity: identity, migration: migration, jobs: jobService,
		process: process, routes: physicalRoutes, predecessor: owner.runner, construction: cold,
		environment: &candidateFactoryEnvironment{path: t.TempDir()}, firstReceipt: currentReceipt,
		componentName: physicalCandidate.policy.identity.Component, componentReceipt: candidateIdentity.ReceiptID,
		predecessorReceipt: predecessorReceipt, lifecycleDeploymentID: lifecycleDeploymentID,
		restoresPredecessor: stage == generatedRouteColdAccountingLifecycleRetired ||
			stage == generatedRouteColdAccountingLifecycleAuthorized ||
			stage == generatedRouteColdAccountingLifecycleActivated,
		predecessorPhysical: predecessorState,
		retiredContainerID:  retiredContainerID,
		currentContainerID:  candidateIdentity.ContainerID}
}

func newGeneratedRouteColdAccountingPartialRetirementFixture(t *testing.T) (
	result generatedRouteColdAccountingPartialRetirementFixture,
) {
	t.Helper()
	components := []string{"api", "frontend"}
	identity := newGeneratedDomainIdentityFixture(t)
	migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity, components, components,
		generatedDomainTwoComponentPlan())
	identity.stores = migration.repository.stores
	validation := newGeneratedRouteSuccessorValidation(t, migration)
	options := generatedDomainCandidateOptions(identity.options)
	owner, candidateRunner := newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t, validation,
		options, 4)
	firstReservation, firstExecution, firstIdentities := prepareGeneratedRouteSequentialCandidates(t, owner,
		components, []string{strings.Repeat("7", 64), strings.Repeat("8", 64)})

	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, _, ingressIP := ingressNetworkCandidate(0)
	ingress := &ingressRunner{appID: migration.guarded.appID, network: firstIdentities[0].NetworkName,
		endpoint: generatedRouteEndpoint(firstIdentities[0]),
		caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
			GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
		ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
			Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
	physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
		slot: generatedidentity.Slot(firstIdentities[0].Slot), endpoints: make(map[string]generatedRouteEndpointSnapshot)}
	for _, value := range firstIdentities {
		physicalRoutes.endpoints[value.ContainerID] = generatedRouteEndpointSnapshot{
			appID: migration.guarded.appID, endpoint: generatedRouteEndpoint(value),
			slot: generatedidentity.Slot(value.Slot)}
	}
	routeRunner := &generatedDomainTwoComponentRouteRunner{daemonID: "generated-domain-cold-partial-retirement",
		delegate: physicalRoutes}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database,
		stores: identity.stores, runner: routeRunner, options: identity.options}, store)
	if err != nil {
		t.Fatal(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores,
		routeRunner, identity.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		t.Fatal(err)
	}
	routes, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	firstEndpoints := make([]generatedroute.RouteEndpoint, 0, len(firstIdentities))
	for _, value := range firstIdentities {
		firstEndpoints = append(firstEndpoints, generatedRouteEndpoint(value))
	}
	firstSealed, err := routes.Pin(migration.guarded.context, firstExecution,
		generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
			ToSlot: generatedidentity.Slot(firstIdentities[0].Slot), Endpoints: firstEndpoints})
	if err != nil {
		t.Fatal(err)
	}
	_, firstRetirement, err := firstSealed.ActivateWithPredecessorRetirement(migration.guarded.context)
	if err != nil || firstRetirement == nil {
		t.Fatalf("activate multi-component initial Current retirement=%#v error=%v", firstRetirement, err)
	}
	if err := firstRetirement.Complete(context.Background()); err != nil {
		t.Fatalf("complete multi-component initial Current: %v", err)
	}
	firstHead, err := generatedruntimestate.New(identity.database).Active(context.Background(),
		migration.guarded.appID)
	if err != nil || firstHead.DeploymentID != migration.request.DeploymentID {
		t.Fatalf("read multi-component initial Current head=%#v error=%v", firstHead, err)
	}
	predecessorPhysical := generatedRouteColdAccountingPhysicalRoster(candidateRunner, firstIdentities)
	establishGeneratedRoutePredecessorLifecycleRoster(t, migration, firstHead,
		append([]candidateOwnerPhysical(nil), predecessorPhysical...))

	successorMigration := newGeneratedRouteColdAccountingMultiSuccessorMigration(t, migration, components)
	owner.validation = newGeneratedRouteSuccessorValidation(t, successorMigration)
	secondReservation, secondExecution, secondIdentities := prepareGeneratedRouteSequentialCandidates(t, owner,
		components, []string{strings.Repeat("9", 64), strings.Repeat("a", 64)})
	currentRoute, err := manager.Observe(context.Background(), migration.guarded.appID)
	if err != nil {
		t.Fatalf("observe multi-component initial Current route: %v", err)
	}
	secondEndpoints := make([]generatedroute.RouteEndpoint, 0, len(secondIdentities))
	for _, value := range secondIdentities {
		endpoint := generatedRouteEndpoint(value)
		secondEndpoints = append(secondEndpoints, endpoint)
		physicalRoutes.endpoints[value.ContainerID] = generatedRouteEndpointSnapshot{
			appID: migration.guarded.appID, endpoint: endpoint, slot: generatedidentity.Slot(value.Slot)}
	}
	secondRequest := generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
		FromSlot: currentRoute.Slot, ToSlot: generatedidentity.Slot(secondIdentities[0].Slot),
		Endpoints: secondEndpoints}
	secondSealed, err := routes.Pin(successorMigration.guarded.context, secondExecution, secondRequest)
	if err != nil {
		t.Fatalf("pin multi-component successor: %v", err)
	}
	physicalRoutes.network = secondIdentities[0].NetworkName
	physicalRoutes.endpoint = generatedRouteEndpoint(secondIdentities[0])
	physicalRoutes.slot = generatedidentity.Slot(secondIdentities[0].Slot)
	_, secondRetirement, err := secondSealed.ActivateWithPredecessorRetirement(successorMigration.guarded.context)
	if err != nil || secondRetirement == nil {
		t.Fatalf("activate multi-component successor retirement=%#v error=%v", secondRetirement, err)
	}

	predecessorByName := generatedRouteColdAccountingIdentityMap(t, components, firstIdentities)
	successorByName := generatedRouteColdAccountingIdentityMap(t, components, secondIdentities)
	fixedInterruption := errors.New("fixed partial predecessor retirement interruption")
	interruptedTarget := predecessorByName["frontend"].ContainerID
	owner.runner.setResult(candidateOwnerStop, func(request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if len(request.Args) != 0 && request.Args[len(request.Args)-1] == interruptedTarget {
			return runtimeprocess.CommandResult{}, fixedInterruption
		}
		return owner.runner.defaultResult(candidateOwnerStop, request)
	})
	if receipt, retireErr := secondRetirement.Retire(context.Background()); !errors.Is(retireErr, fixedInterruption) || receipt.ActivationReceiptID != "" {
		t.Fatalf("interrupt multi-component predecessor retirement receipt=%#v error=%v", receipt, retireErr)
	}
	owner.runner.setResult(candidateOwnerStop, nil)
	var componentRows, aggregateRows int
	if err := identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, secondRetirement.state.receipt.ReceiptID).Scan(&componentRows); err != nil {
		t.Fatal(err)
	}
	if err := identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, secondRetirement.state.receipt.ReceiptID).Scan(&aggregateRows); err != nil {
		t.Fatal(err)
	}
	if componentRows != 1 || aggregateRows != 0 {
		t.Fatalf("partial predecessor durable rows components=%d aggregate=%d", componentRows, aggregateRows)
	}
	recorded := generatedRouteColdAccountingRetirementObservation(t, identity.database,
		secondRetirement.state.receipt.ReceiptID, "api")
	if recorded.ContainerID != predecessorByName["api"].ContainerID ||
		recorded.CandidateReceiptID != predecessorByName["api"].ReceiptID {
		t.Fatalf("partial predecessor observation=%#v", recorded)
	}

	secondRetirement.Leave()
	secondExecution.Leave()
	secondReservation.Release()
	firstRetirement.Leave()
	firstExecution.Leave()
	firstReservation.Release()
	if err := migration.releaseEffects(); err != nil {
		t.Fatalf("release multi-component fixture effects before restart: %v", err)
	}

	process, construction := generatedRouteColdAccountingPartialRestartProcess(t, identity, store,
		physicalRoutes, candidateRunner, append(firstIdentities, secondIdentities...))
	base := generatedRouteColdAccountingFixture{identity: identity, migration: successorMigration,
		jobs: successorMigration.jobService, process: process, routes: physicalRoutes,
		predecessor: owner.runner, construction: construction,
		environment:  &candidateFactoryEnvironment{path: t.TempDir()},
		firstReceipt: secondRetirement.state.receipt, componentName: "api",
		componentReceipt:      successorByName["api"].ReceiptID,
		predecessorReceipt:    predecessorByName["api"].ReceiptID,
		lifecycleDeploymentID: migration.request.DeploymentID, restoresPredecessor: true,
		predecessorPhysical: candidatePhysicalConfirmedAbsent,
		retiredContainerID:  predecessorByName["api"].ContainerID,
		currentContainerID:  successorByName["api"].ContainerID}
	return generatedRouteColdAccountingPartialRetirementFixture{base: base, runner: candidateRunner,
		predecessor: predecessorByName, successor: successorByName, recordedComponent: "api"}
}

func generatedRouteColdAccountingIdentityMap(t *testing.T, names []string,
	identities []CandidateIdentity,
) map[string]CandidateIdentity {
	t.Helper()
	if len(names) != len(identities) {
		t.Fatal("candidate identity roster is incomplete")
	}
	result := make(map[string]CandidateIdentity, len(names))
	for index, name := range names {
		if identities[index].ComponentName != name || identities[index].ContainerID == "" ||
			identities[index].ReceiptID == "" {
			t.Fatalf("candidate identity %s=%#v", name, identities[index])
		}
		result[name] = identities[index]
	}
	return result
}

func generatedRouteColdAccountingPhysicalRoster(runner *generatedRouteCandidateRunner,
	identities []CandidateIdentity,
) []candidateOwnerPhysical {
	result := make([]candidateOwnerPhysical, 0, len(identities))
	for _, identity := range identities {
		result = append(result, runner.physical[identity.ContainerID])
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].policy.identity.Component < result[j].policy.identity.Component
	})
	return result
}

func generatedRouteColdAccountingPartialRestartProcess(t *testing.T, identity generatedDomainIdentityFixture,
	store *stateStore, routes *generatedRoutePredecessorRunner, candidates *generatedRouteCandidateRunner,
	identities []CandidateIdentity,
) (*generatedRouteColdAccountingRunner, *generatedDomainConstructionRunner) {
	t.Helper()
	stateValue, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	_, v2, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, stateValue, v2, journal, gatewayTopologyExactV1Only)
	observation.V1Container.ID = strings.Repeat("d", 64)
	observation.V1NetworkID = strings.Repeat("e", 64)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = observation.V1Container.ID
		case caddyNetworkName:
			objects[index].id = observation.V1NetworkID
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint,
				createdAt:  observation.V1VolumeIdentity.CreatedAt}
		}
	}
	allPhysical := generatedRouteColdAccountingPhysicalRoster(candidates, identities)
	existing := make([]candidateOwnerPhysical, 0, len(allPhysical))
	for _, value := range allPhysical {
		if value.exists {
			existing = append(existing, value)
		}
	}
	objects = append(objects, generatedDomainA0ApplicationObjects(t, existing)...)
	builderObjects, builder := generatedDomainTwoComponentBuilderObjects(t, identity.dataRoot)
	objects = append(objects, builderObjects...)
	identity.options.HostPort = journal.Source.LocalHostPort
	cold := &generatedDomainConstructionRunner{roster: candidates,
		delegate: &generatedDomainRecoveryCensusRunner{daemonID: "generated-domain-cold-partial-retirement",
			objects: objects, imageID: observation.Image.ID, gateway: gateway},
		capacity: []byte(fmt.Sprintf("%d %d\n", identity.options.MemoryBytes-1,
			identity.options.ReplacementDiskBytes))}
	builder.runner = &generatedDomainTwoComponentRouteRunner{delegate: cold, builder: &builder.physical}
	return &generatedRouteColdAccountingRunner{cold: builder.runner, routes: routes,
		predecessor: candidates, predecessorPhysical: allPhysical}, cold
}

func assertGeneratedRouteColdAccountingPartialDebt(t *testing.T, result *generatedDomainConstruction,
	fixture generatedRouteColdAccountingPartialRetirementFixture,
) {
	t.Helper()
	if result == nil || result.capacity == nil || result.candidates == nil || result.candidates.ledger == nil ||
		result.candidates.coordinator != result.capacity {
		t.Fatal("partial cold construction did not publish one shared capacity coordinator")
	}
	ledger := result.candidates.ledger
	type debt struct {
		token  *generatedDomainCapacityDebtToken
		limits generatedcapacity.Limits
	}
	var debts []debt
	for _, deployment := range []struct {
		id       string
		identity map[string]CandidateIdentity
		physical map[string]candidatePhysicalState
	}{
		{fixture.base.lifecycleDeploymentID, fixture.predecessor,
			map[string]candidatePhysicalState{"api": candidatePhysicalConfirmedAbsent,
				"frontend": candidatePhysicalHandedToRoute}},
		{fixture.base.migration.request.DeploymentID, fixture.successor,
			map[string]candidatePhysicalState{"api": candidatePhysicalHandedToRoute,
				"frontend": candidatePhysicalHandedToRoute}},
	} {
		ledger.mu.Lock()
		group := ledger.groups[candidateCapacityGroupKey{appID: fixture.base.migration.guarded.appID,
			deploymentID: deployment.id}]
		ledger.mu.Unlock()
		if group == nil {
			t.Fatalf("partial cold construction omitted accounting group %s", deployment.id)
		}
		group.mu.Lock()
		if group.ledger != ledger || !reflect.DeepEqual(group.components, []string{"api", "frontend"}) {
			group.mu.Unlock()
			t.Fatalf("partial cold accounting roster %s=%v", deployment.id, group.components)
		}
		for _, componentName := range group.components {
			component := group.byName[componentName]
			identity := deployment.identity[componentName]
			word := uint64(0)
			if component != nil && component.receipt != nil {
				word = component.receipt.state.Load()
			}
			charge := (*candidateCapacityCharge)(nil)
			if component != nil {
				charge = component.charge
			}
			token, restored := (*generatedDomainCapacityDebtToken)(nil), false
			if charge != nil {
				token, restored = charge.token.(*generatedDomainCapacityDebtToken)
			}
			if component == nil || component.receipt == nil || component.receipt.id != identity.ReceiptID ||
				component.tokenHeld || charge == nil || charge.state != candidateChargeActive ||
				charge.owner != candidateAccountingRouteActive || charge.lease != nil || charge.logicallyRetired ||
				!restored || token == nil || !token.configured() || !token.published() ||
				candidatePhysicalFromWord(word) != deployment.physical[componentName] ||
				word&candidateReceiptAcknowledged == 0 || word&candidateReceiptObserved == 0 ||
				word&candidateReceiptUnquiesced != 0 {
				group.mu.Unlock()
				t.Fatalf("partial cold accounting component %s/%s is incomplete", deployment.id, componentName)
			}
			debts = append(debts, debt{token: token, limits: group.limits})
		}
		group.mu.Unlock()
	}
	var memory uint64
	var disk uint64
	for _, value := range debts {
		memory += value.limits.MemoryBytes
		disk += value.limits.DiskBytes
	}
	result.capacity.mu.Lock()
	valid := result.capacity.phase == generatedDomainCapacityPublished && result.capacity.borrows == 0 &&
		result.capacity.memory == memory && result.capacity.disk == disk &&
		len(result.capacity.tokens) == len(debts)
	for _, value := range debts {
		_, present := result.capacity.tokens[value.token]
		valid = valid && present && value.token.owner == result.capacity && value.token.active &&
			value.token.epoch != 0 && value.token.limits == value.limits
	}
	result.capacity.mu.Unlock()
	if !valid || len(debts) != 4 {
		t.Fatalf("partial cold construction restored debt count=%d exact=%t", len(debts), valid)
	}
}

func newGeneratedRouteColdAccountingSequentialOwner(t *testing.T, validation *candidateValidationFixture,
	options GeneratedCandidateProcessOptions,
) (*candidateOwnerFixture, *generatedRouteCandidateRunner) {
	return newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t, validation, options, 2)
}

func newGeneratedRouteColdAccountingSequentialOwnerWithCapacity(t *testing.T,
	validation *candidateValidationFixture, options GeneratedCandidateProcessOptions, componentCapacity uint64,
) (*candidateOwnerFixture, *generatedRouteCandidateRunner) {
	t.Helper()
	if validation == nil || validation.migration == nil || componentCapacity < 2 {
		t.Fatal("cold accounting owner requires prepared validation authority")
	}
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: uint64(componentCapacity) * uint64(options.MemoryBytes),
		DiskAvailableBytes:   componentCapacity * options.ReplacementDiskBytes,
	}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatalf("construct cold accounting sequential candidate budget: %v", err)
	}
	base := &candidateOwnerRunner{containerID: strings.Repeat("7", 64),
		absenceErr: generatedMigrationCompletedExitOne(t),
		before:     make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results:    make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
	runner := &generatedRouteCandidateRunner{delegate: base, physical: make(map[string]candidateOwnerPhysical)}
	environment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := validation.migration.repository.GeneratedCandidateAuthority(runner, environment, budget, options)
	if err != nil {
		t.Fatalf("construct cold accounting sequential candidate authority: %v", err)
	}
	validation.authority = authority
	return &candidateOwnerFixture{validation: validation, authority: authority, runner: base,
		environment: environment, source: source, budget: budget, options: options}, runner
}

func assertGeneratedRouteColdAccountingNilCharge(t *testing.T, ledger *candidateCapacityLedger,
	appID, deploymentID, receiptID string,
) {
	t.Helper()
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: appID, deploymentID: deploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("cold predecessor accounting group is absent")
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	component := group.byName["api"]
	if component == nil || component.receipt == nil || component.receipt.id != receiptID ||
		component.charge != nil || component.tokenHeld {
		t.Fatalf("cold predecessor accounting component = %#v", component)
	}
}
