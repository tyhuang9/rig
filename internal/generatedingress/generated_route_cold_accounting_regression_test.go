//go:build linux || windows

package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestGeneratedDomainRestartAccountingSwitchingRouteRecoversActivationWithoutSecondShare(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingSwitchingRouteFixture(t)
		assertGeneratedRouteColdAccountingSwitchingRouteSnapshot(t, fixture)
		beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
		beforeCommands := fixture.construction.snapshot()
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)
		beforeRestartInstalls := fixture.routes.restartInstalls
		beforeProposedReloads := generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands)
		if beforeProposedReloads != generatedRouteColdAccountingSwitchingRoutePhysicalProposedReloads {
			t.Fatalf("SwitchingRoute physical publication reloads=%d want=%d", beforeProposedReloads,
				generatedRouteColdAccountingSwitchingRoutePhysicalProposedReloads)
		}

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil {
			var diagnostic *generatedDomainConstructionDiagnostic
			typedDiagnostic := errors.As(err, &diagnostic) && diagnostic != nil
			assertGeneratedRouteColdAccountingSwitchingRouteSnapshot(t, fixture)
			assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
			if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
				fixture.predecessor.count(candidateOwnerRemove) != beforeRemove ||
				fixture.routes.restartInstalls != beforeRestartInstalls {
				t.Fatal("SwitchingRoute cold refusal reached route or candidate effects")
			}
			if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
				generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
				t.Fatal("SwitchingRoute cold refusal acquired a fresh capacity share")
			}
			if result != nil || !typedDiagnostic || diagnostic.stage != generatedDomainConstructionStageColdConstruction {
				t.Fatalf("SwitchingRoute construction failed before the qualified cold boundary: result=%#v diagnostic=%#v error=%v",
					result, diagnostic, err)
			}
			t.Fatalf("SwitchingRoute cold construction reached the qualified refusal: stage=%s reason=%s error=%v",
				diagnostic.stage, diagnostic.reason, err)
		}
		if result == nil || result.candidates == nil || result.routes == nil || result.cold == nil {
			t.Fatalf("SwitchingRoute cold construction result=%#v", result)
		}
		if result.cold.pendingActivation == nil ||
			result.cold.pendingGatewayDisposition != generatedDomainColdPendingGatewayProposedExact {
			t.Fatalf("SwitchingRoute cold Pending disposition=%d activation=%#v", result.cold.pendingGatewayDisposition,
				result.cold.pendingActivation)
		}
		assertGeneratedRouteColdAccountingSwitchingRouteSnapshot(t, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
		assertGeneratedRouteColdAccountingSwitchingRouteHandoffSeed(t, result, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("SwitchingRoute cold reconstruction acquired a fresh capacity share")
		}
		fixture.process.setRoutes(fixture.routes)

		var beforeAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
			fixture.migration.claim.JobID()).Scan(&beforeAttempt); err != nil {
			t.Fatalf("read SwitchingRoute worker before recovery: %v", err)
		}
		var originalCreatorJobID string
		var originalCreatorAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT creator_job_id,creator_worker_attempt
			FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt).Scan(
			&originalCreatorJobID, &originalCreatorAttempt); err != nil {
			t.Fatalf("read immutable SwitchingRoute creator: %v", err)
		}
		recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.identity.database,
			time.Now().UTC(), fixture.migration.bindings)
		if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
			t.Fatalf("recover SwitchingRoute worker: result=%+v error=%v", recovered, err)
		}

		var claimed jobs.Job
		var claimedProofErr error
		var recoveredReceipt generatedruntimestate.RouteActivationReceipt
		runGeneratedColdBoundaryWorker(t, fixture.jobs,
			generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job,
				_ jobs.ProgressReporter,
			) (jobs.ExecutionResult, error) {
				claimed = job
				observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
					fixture.process, fixture.identity.options)
				if observeErr != nil {
					claimedProofErr = fmt.Errorf("post-claim SwitchingRoute startup observation: %w", observeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
					observed, *result.cold); attestErr != nil {
					claimedProofErr = fmt.Errorf("post-claim SwitchingRoute cold manifest: %w", attestErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				binding, bindingErr := fixture.migration.bindings.Read(ctx, job.ID)
				if bindingErr != nil {
					claimedProofErr = fmt.Errorf("post-claim SwitchingRoute binding: %w", bindingErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				claim, claimErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
				if claimErr != nil {
					claimedProofErr = fmt.Errorf("post-claim SwitchingRoute claim: %w", claimErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				replacement := GeneratedReplacementRequest{Claim: claim,
					DeploymentID:                 fixture.migration.request.DeploymentID,
					ReleaseID:                    fixture.migration.request.ReleaseID,
					ConfigurationRevisionID:      fixture.migration.request.ConfigurationRevisionID,
					ConfigurationRevisionNumber:  fixture.migration.request.ConfigurationRevisionNumber,
					DeploymentPlanRevisionID:     fixture.migration.request.DeploymentPlanRevisionID,
					DeploymentPlanRevisionNumber: fixture.migration.request.DeploymentPlanRevisionNumber,
					CandidateSlot:                fixture.switchCandidate.Slot}
				reservation, reserveErr := result.candidates.ReserveReplacement(ctx, replacement)
				if reserveErr != nil {
					claimedProofErr = fmt.Errorf("reserve recovered SwitchingRoute candidate: %w", reserveErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				defer reservation.Release()
				execution, pinErr := result.candidates.PinCandidates(ctx,
					GeneratedCandidateRequest(replacement), reservation)
				if pinErr != nil {
					claimedProofErr = fmt.Errorf("pin recovered SwitchingRoute candidate: %w", pinErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				defer execution.Leave()
				candidate, candidateErr := execution.ObserveRouteCandidate(fixture.componentName)
				if candidateErr != nil {
					claimedProofErr = fmt.Errorf("observe recovered SwitchingRoute candidate: %w", candidateErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				request := generatedRouteRequest(fixture.migration.guarded.appID, candidate)
				request.FromSlot = generatedidentity.Slot(fixture.switchPredecessorHead.Slot)
				sealed, routeErr := result.routes.Pin(ctx, execution, request)
				if routeErr != nil {
					claimedProofErr = fmt.Errorf("pin recovered SwitchingRoute activation: %w", routeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				_, retirement, routeErr := sealed.ActivateWithPredecessorRetirement(ctx)
				if routeErr != nil || retirement == nil {
					claimedProofErr = fmt.Errorf("recover SwitchingRoute activation retirement=%#v: %w",
						retirement, routeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				defer retirement.Leave()
				recoveredReceipt = retirement.state.receipt
				if completionErr := retirement.Complete(ctx); completionErr != nil {
					claimedProofErr = fmt.Errorf("complete recovered SwitchingRoute: %w", completionErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			}), fixture.migration.claim.JobID(), jobs.Succeeded,
			fixture.identity.options.WorkingDirectory, func() string { return boundedErrorText(claimedProofErr) })
		if claimedProofErr != nil {
			t.Fatalf("fresh SwitchingRoute worker proof: %v", claimedProofErr)
		}
		if claimed.ID != fixture.migration.claim.JobID() || claimed.ResourceID != fixture.migration.guarded.appID ||
			claimed.Attempt != beforeAttempt+1 {
			t.Fatalf("fresh SwitchingRoute worker=%+v beforeAttempt=%d receipt=%#v", claimed,
				beforeAttempt, recoveredReceipt)
		}
		var relevantActivations, deadReceiptRows int
		var persistedReceiptID, persistedCreatorJobID, persistedWriterJobID string
		var persistedCreatorAttempt, persistedWriterAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
			WHERE app_id=? AND successor_deployment_id=? AND successor_generation=?`,
			fixture.migration.guarded.appID, fixture.migration.request.DeploymentID, 2).Scan(&relevantActivations); err != nil {
			t.Fatalf("count recovered SwitchingRoute activations: %v", err)
		}
		if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`,
			fixture.switchReceiptID).Scan(&deadReceiptRows); err != nil {
			t.Fatalf("count dead SwitchingRoute receipt: %v", err)
		}
		if relevantActivations == 1 {
			if err := fixture.identity.database.QueryRow(`SELECT id,creator_job_id,creator_worker_attempt,
				writer_job_id,writer_worker_attempt FROM generated_route_activations
				WHERE app_id=? AND successor_deployment_id=? AND successor_generation=?`,
				fixture.migration.guarded.appID, fixture.migration.request.DeploymentID, 2).Scan(
				&persistedReceiptID, &persistedCreatorJobID, &persistedCreatorAttempt,
				&persistedWriterJobID, &persistedWriterAttempt); err != nil {
				t.Fatalf("read recovered SwitchingRoute activation: %v", err)
			}
		}
		if relevantActivations != 1 || deadReceiptRows != 0 || recoveredReceipt.ReceiptID == "" ||
			recoveredReceipt.ReceiptID == fixture.switchReceiptID || persistedReceiptID != recoveredReceipt.ReceiptID ||
			persistedCreatorJobID != originalCreatorJobID || persistedCreatorAttempt != originalCreatorAttempt ||
			persistedWriterJobID != claimed.ID || persistedWriterAttempt != claimed.Attempt ||
			recoveredReceipt.CreatorJobID != originalCreatorJobID ||
			recoveredReceipt.CreatorWorkerAttempt != originalCreatorAttempt ||
			recoveredReceipt.WriterJobID != claimed.ID || recoveredReceipt.WriterWorkerAttempt != claimed.Attempt {
			t.Fatalf("recovered SwitchingRoute activation count=%d dead=%d receipt=%#v persisted=%q/%d/%q/%d creator=%q/%d claimed=%q/%d",
				relevantActivations, deadReceiptRows, recoveredReceipt, persistedCreatorJobID, persistedCreatorAttempt,
				persistedWriterJobID, persistedWriterAttempt, originalCreatorJobID, originalCreatorAttempt,
				claimed.ID, claimed.Attempt)
		}
		fixture.firstReceipt = recoveredReceipt
		assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("fresh SwitchingRoute worker acquired a second capacity share")
		}
		if fixture.routes.restartInstalls != beforeRestartInstalls ||
			generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) != beforeProposedReloads ||
			fixture.predecessor.count(candidateOwnerStop) != beforeStop+1 ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove+1 {
			t.Fatalf("fresh SwitchingRoute worker effects restart=%d/%d stop=%d/%d remove=%d/%d",
				beforeRestartInstalls, fixture.routes.restartInstalls, beforeStop+1,
				fixture.predecessor.count(candidateOwnerStop), beforeRemove+1,
				fixture.predecessor.count(candidateOwnerRemove))
		}
	})
}

func TestGeneratedDomainRestartAccountingPreviousExactClearsPendingAndActivatesWithoutSecondShare(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, testGeneratedDomainRestartAccountingPreviousExactActivation)
}

func testGeneratedDomainRestartAccountingPreviousExactActivation(t *testing.T) {
	t.Helper()
	fixture := newGeneratedRouteColdAccountingPreviousExactFixture(t)
	state, err := fixture.switchStateStore.load()
	proposed := routeRecord{Slot: generatedidentity.Slot(fixture.switchCandidate.Slot),
		Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(fixture.switchCandidate)}}
	if err != nil || state.Pending == nil || state.Pending.AppID != fixture.migration.guarded.appID ||
		state.Pending.Previous == nil || !sameRoute(*state.Pending.Previous, fixture.switchPreviousRoute) ||
		!sameRoute(state.Pending.Proposed, proposed) || !reflect.DeepEqual(state, fixture.switchPostMVState) {
		t.Fatalf("PreviousExact producer state=%#v error=%v", state, err)
	}
	previousConfig := expectedConfig(t, fixture.routes.ingressRunner,
		map[string]routeRecord{fixture.migration.guarded.appID: fixture.switchPreviousRoute})
	restartConfig := fixture.routes.files[caddyContainerName+":/config/active.json"]
	if !bytes.Equal(fixture.routes.liveConfig, previousConfig) ||
		!bytes.Equal(restartConfig, previousConfig) {
		t.Fatal("PreviousExact producer did not retain exact live/restart Previous route")
	}
	beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
	beforeCommands := fixture.construction.snapshot()
	beforeStop := fixture.predecessor.count(candidateOwnerStop)
	beforeRemove := fixture.predecessor.count(candidateOwnerRemove)
	beforeRestartInstalls := fixture.routes.restartInstalls
	beforeProposedReloads := generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands)

	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.process, fixture.environment, fixture.identity.options)
	if err != nil || result == nil || result.candidates == nil || result.routes == nil || result.cold == nil {
		t.Fatalf("PreviousExact cold construction result=%#v error=%v", result, err)
	}
	if result.cold.pendingActivation == nil ||
		result.cold.pendingGatewayDisposition != generatedDomainColdPendingGatewayPreviousExact {
		t.Fatalf("PreviousExact cold disposition=%d activation=%#v",
			result.cold.pendingGatewayDisposition, result.cold.pendingActivation)
	}
	assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
	assertGeneratedRouteColdAccountingSwitchingRouteHandoffSeed(t, result, fixture)
	if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
		generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
		t.Fatal("PreviousExact cold reconstruction acquired a fresh capacity share")
	}
	debt := generatedRouteColdAccountingCapturePreviousExactDebt(t, result, fixture)
	fixture.process.setRoutes(fixture.routes)
	beforeRouteCapacityProbes := generatedRouteColdAccountingCapacityProbeCount(fixture.routes.commands)

	var beforeAttempt int
	if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
		fixture.migration.claim.JobID()).Scan(&beforeAttempt); err != nil {
		t.Fatal(err)
	}
	var originalCreatorJobID string
	var originalCreatorAttempt int
	if err := fixture.identity.database.QueryRow(`SELECT creator_job_id,creator_worker_attempt
			FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt).Scan(
		&originalCreatorJobID, &originalCreatorAttempt); err != nil {
		t.Fatal(err)
	}
	recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.identity.database,
		time.Now().UTC(), fixture.migration.bindings)
	if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
		t.Fatalf("recover PreviousExact worker: result=%+v error=%v", recovered, err)
	}

	var claimed jobs.Job
	var workerErr error
	var recoveredReceipt generatedruntimestate.RouteActivationReceipt
	runGeneratedColdBoundaryWorker(t, fixture.jobs,
		generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job,
			_ jobs.ProgressReporter,
		) (jobs.ExecutionResult, error) {
			claimed = job
			observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
				fixture.process, fixture.identity.options)
			if observeErr != nil {
				workerErr = fmt.Errorf("post-claim PreviousExact observation: %w", observeErr)
				return jobs.ExecutionResult{}, workerErr
			}
			if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
				observed, *result.cold); attestErr != nil {
				workerErr = fmt.Errorf("post-claim PreviousExact manifest: %w", attestErr)
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
			replacement := GeneratedReplacementRequest{Claim: claim,
				DeploymentID:                 fixture.migration.request.DeploymentID,
				ReleaseID:                    fixture.migration.request.ReleaseID,
				ConfigurationRevisionID:      fixture.migration.request.ConfigurationRevisionID,
				ConfigurationRevisionNumber:  fixture.migration.request.ConfigurationRevisionNumber,
				DeploymentPlanRevisionID:     fixture.migration.request.DeploymentPlanRevisionID,
				DeploymentPlanRevisionNumber: fixture.migration.request.DeploymentPlanRevisionNumber,
				CandidateSlot:                fixture.switchCandidate.Slot}
			reservation, reserveErr := result.candidates.ReserveReplacement(ctx, replacement)
			if reserveErr != nil {
				workerErr = reserveErr
				return jobs.ExecutionResult{}, workerErr
			}
			defer reservation.Release()
			execution, pinErr := result.candidates.PinCandidates(ctx,
				GeneratedCandidateRequest(replacement), reservation)
			if pinErr != nil {
				workerErr = pinErr
				return jobs.ExecutionResult{}, workerErr
			}
			defer execution.Leave()
			candidate, candidateErr := execution.ObserveRouteCandidate(fixture.componentName)
			if candidateErr != nil {
				workerErr = candidateErr
				return jobs.ExecutionResult{}, workerErr
			}
			request := generatedRouteRequest(fixture.migration.guarded.appID, candidate)
			request.FromSlot = generatedidentity.Slot(fixture.switchPredecessorHead.Slot)
			sealed, routeErr := result.routes.Pin(ctx, execution, request)
			disposition := generatedDomainColdPendingGatewayUnknown
			if sealed != nil {
				disposition = sealed.gatewayDisposition
			}
			if routeErr != nil || sealed == nil ||
				disposition != generatedDomainColdPendingGatewayPreviousExact {
				workerErr = fmt.Errorf("pin PreviousExact disposition=%d: %w",
					disposition, routeErr)
				return jobs.ExecutionResult{}, workerErr
			}
			_, retirement, routeErr := sealed.ActivateWithPredecessorRetirement(ctx)
			if routeErr != nil || retirement == nil {
				workerErr = fmt.Errorf("activate PreviousExact retirement=%#v: %w", retirement, routeErr)
				return jobs.ExecutionResult{}, workerErr
			}
			defer retirement.Leave()
			recoveredReceipt = retirement.state.receipt
			if completionErr := retirement.Complete(ctx); completionErr != nil {
				workerErr = fmt.Errorf("complete PreviousExact retirement: %w", completionErr)
				return jobs.ExecutionResult{}, workerErr
			}
			return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
		}), fixture.migration.claim.JobID(), jobs.Succeeded,
		fixture.identity.options.WorkingDirectory, func() string { return boundedErrorText(workerErr) })
	if workerErr != nil {
		t.Fatalf("fresh PreviousExact worker proof: %v", workerErr)
	}
	if claimed.ID != fixture.migration.claim.JobID() || claimed.Attempt != beforeAttempt+1 {
		t.Fatalf("fresh PreviousExact worker=%+v beforeAttempt=%d", claimed, beforeAttempt)
	}
	var relevantActivations, deadReceiptRows int
	if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
			WHERE app_id=? AND successor_deployment_id=? AND successor_generation=2`,
		fixture.migration.guarded.appID, fixture.migration.request.DeploymentID).Scan(&relevantActivations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`,
		fixture.switchReceiptID).Scan(&deadReceiptRows); err != nil {
		t.Fatal(err)
	}
	if relevantActivations != 1 || deadReceiptRows != 0 || recoveredReceipt.ReceiptID == "" ||
		recoveredReceipt.ReceiptID == fixture.switchReceiptID ||
		recoveredReceipt.CreatorJobID != originalCreatorJobID ||
		recoveredReceipt.CreatorWorkerAttempt != originalCreatorAttempt ||
		recoveredReceipt.WriterJobID != claimed.ID ||
		recoveredReceipt.WriterWorkerAttempt != claimed.Attempt {
		t.Fatalf("PreviousExact activation count=%d dead=%d receipt=%#v creator=%q/%d worker=%q/%d",
			relevantActivations, deadReceiptRows, recoveredReceipt, originalCreatorJobID,
			originalCreatorAttempt, claimed.ID, claimed.Attempt)
	}
	fixture.firstReceipt = recoveredReceipt
	assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
	generatedRouteColdAccountingAssertPreviousExactDebtAfterCompletion(t, result, fixture, debt)
	if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
		generatedRouteColdAccountingCapacityProbeCount(beforeCommands) ||
		generatedRouteColdAccountingCapacityProbeCount(fixture.routes.commands) != beforeRouteCapacityProbes {
		t.Fatal("PreviousExact activation changed the exact restored successor debt/share")
	}
	if fixture.routes.restartInstalls != beforeRestartInstalls+2 ||
		generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) != beforeProposedReloads+1 ||
		fixture.predecessor.count(candidateOwnerStop) != beforeStop+1 ||
		fixture.predecessor.count(candidateOwnerRemove) != beforeRemove+1 {
		t.Fatalf("PreviousExact effects restart=%d/%d reload=%d/%d stop=%d/%d remove=%d/%d",
			fixture.routes.restartInstalls, beforeRestartInstalls+2,
			generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands), beforeProposedReloads+1,
			fixture.predecessor.count(candidateOwnerStop), beforeStop+1,
			fixture.predecessor.count(candidateOwnerRemove), beforeRemove+1)
	}
}

type generatedRouteColdAccountingPreviousExactAuthorityCase struct {
	fixture                   generatedRouteColdAccountingFixture
	result                    *generatedDomainConstruction
	context                   context.Context
	claim                     deploymentexecution.Claim
	debt                      generatedRouteColdAccountingPreviousExactDebt
	beforeCommands            [][]string
	beforeRouteCapacityProbes int
	beforeStop                int
	beforeRemove              int
	beforeRestartInstalls     int
	beforeProposedReloads     int
	originalCreatorJobID      string
	originalCreatorAttempt    int
}

type generatedRouteColdAccountingPreviousExactScope struct {
	reservation GeneratedReplacementReservation
	execution   *GeneratedCandidateExecution
	sealed      *GeneratedRouteExecution
}

func (s *generatedRouteColdAccountingPreviousExactScope) leave() {
	if s == nil {
		return
	}
	if s.execution != nil {
		s.execution.Leave()
		s.execution = nil
	}
	if s.reservation != nil {
		s.reservation.Release()
		s.reservation = nil
	}
}

func generatedRouteColdAccountingPreviousExactAuthorityFixture(t *testing.T) (
	result generatedRouteColdAccountingPreviousExactAuthorityCase,
) {
	t.Helper()
	result.fixture = newGeneratedRouteColdAccountingPreviousExactFixture(t)
	state, err := result.fixture.switchStateStore.load()
	proposed := routeRecord{Slot: generatedidentity.Slot(result.fixture.switchCandidate.Slot),
		Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(result.fixture.switchCandidate)}}
	if err != nil || state.Pending == nil || state.Pending.Previous == nil ||
		state.Pending.AppID != result.fixture.migration.guarded.appID ||
		!sameRoute(*state.Pending.Previous, result.fixture.switchPreviousRoute) ||
		!sameRoute(state.Pending.Proposed, proposed) || !reflect.DeepEqual(state, result.fixture.switchPostMVState) {
		t.Fatalf("PreviousExact authority fixture state=%#v error=%v", state, err)
	}
	previousConfig := expectedConfig(t, result.fixture.routes.ingressRunner,
		map[string]routeRecord{result.fixture.migration.guarded.appID: result.fixture.switchPreviousRoute})
	if !bytes.Equal(result.fixture.routes.liveConfig, previousConfig) ||
		!bytes.Equal(result.fixture.routes.files[caddyContainerName+":/config/active.json"], previousConfig) {
		t.Fatal("PreviousExact authority fixture did not retain exact physical Previous")
	}
	protection := generatedRouteColdAccountingProtection(t, result.fixture)
	result.beforeCommands = result.fixture.construction.snapshot()
	result.beforeStop = result.fixture.predecessor.count(candidateOwnerStop)
	result.beforeRemove = result.fixture.predecessor.count(candidateOwnerRemove)
	result.beforeRestartInstalls = result.fixture.routes.restartInstalls
	result.beforeProposedReloads = generatedRouteColdAccountingProposedReloadCount(result.fixture.routes.commands)
	if err := result.fixture.identity.database.QueryRow(`SELECT creator_job_id,creator_worker_attempt
		FROM generated_candidate_intents WHERE id=?`, result.fixture.componentReceipt).Scan(
		&result.originalCreatorJobID, &result.originalCreatorAttempt); err != nil {
		t.Fatal(err)
	}
	result.result, err = newGeneratedDomainConstruction(context.Background(), result.fixture.identity.stores,
		result.fixture.process, result.fixture.environment, result.fixture.identity.options)
	if err != nil || result.result == nil || result.result.candidates == nil || result.result.routes == nil ||
		result.result.cold == nil || result.result.cold.pendingActivation == nil ||
		result.result.cold.pendingGatewayDisposition != generatedDomainColdPendingGatewayPreviousExact {
		t.Fatalf("PreviousExact authority cold result=%#v error=%v", result.result, err)
	}
	assertGeneratedRouteColdAccountingProtectionUnchanged(t, protection, result.fixture)
	assertGeneratedRouteColdAccountingSwitchingRouteHandoffSeed(t, result.result, result.fixture)
	if generatedRouteColdAccountingCapacityProbeCount(result.fixture.construction.snapshot()) !=
		generatedRouteColdAccountingCapacityProbeCount(result.beforeCommands) {
		t.Fatal("PreviousExact authority cold reconstruction acquired a second capacity share")
	}
	result.debt = generatedRouteColdAccountingCapturePreviousExactDebt(t, result.result, result.fixture)
	result.fixture.process.setRoutes(result.fixture.routes)
	result.beforeRouteCapacityProbes = generatedRouteColdAccountingCapacityProbeCount(result.fixture.routes.commands)
	effectsScope, releaseEffects, err := deploymenteffects.AcquireScoped(context.Background(),
		result.fixture.identity.options.WorkingDirectory)
	if err != nil {
		t.Fatalf("acquire PreviousExact authority effects scope: %v", err)
	}
	t.Cleanup(func() {
		if err := releaseEffects(); err != nil {
			t.Errorf("release PreviousExact authority effects scope: %v", err)
		}
	})
	result.context = deploymenteffects.WithScope(context.Background(), effectsScope)

	recovered, err := generatedrecovery.RecoverRuntime(context.Background(), result.fixture.identity.database,
		time.Now().UTC(), result.fixture.migration.bindings)
	if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
		t.Fatalf("recover PreviousExact authority worker result=%+v error=%v", recovered, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	claimed, err := result.fixture.identity.database.Exec(`UPDATE jobs SET status='running',phase='validate',
		progress_percent=1,checkpoint_json='{"phase":"validate"}',attempt=attempt+1,
		started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status='queued'`,
		now, now, result.fixture.migration.claim.JobID())
	if err != nil {
		t.Fatal(err)
	}
	if rows, rowsErr := claimed.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("claim PreviousExact authority worker rows=%d error=%v", rows, rowsErr)
	}
	binding, err := result.fixture.migration.bindings.Read(context.Background(),
		result.fixture.migration.claim.JobID())
	if err != nil {
		t.Fatal(err)
	}
	var attempt int
	if err := result.fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
		result.fixture.migration.claim.JobID()).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	result.claim, err = deploymentexecution.NewClaim(binding, result.fixture.migration.claim.JobID(),
		result.fixture.migration.guarded.appID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func generatedRouteColdAccountingPreparePreviousExactScope(ctx context.Context,
	test generatedRouteColdAccountingPreviousExactAuthorityCase, claim deploymentexecution.Claim,
) (*generatedRouteColdAccountingPreviousExactScope, error) {
	observed, err := inspectGeneratedDomainStartupObservation(ctx, test.fixture.identity.stores,
		test.fixture.process, test.fixture.identity.options)
	if err != nil {
		return nil, fmt.Errorf("observe PreviousExact scope: %w", err)
	}
	if err := reattestGeneratedDomainCold(ctx, test.result.manager, test.result.candidates,
		observed, *test.result.cold); err != nil {
		return nil, fmt.Errorf("reattest PreviousExact scope: %w", err)
	}
	replacement := GeneratedReplacementRequest{Claim: claim,
		DeploymentID:                 test.fixture.migration.request.DeploymentID,
		ReleaseID:                    test.fixture.migration.request.ReleaseID,
		ConfigurationRevisionID:      test.fixture.migration.request.ConfigurationRevisionID,
		ConfigurationRevisionNumber:  test.fixture.migration.request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID:     test.fixture.migration.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: test.fixture.migration.request.DeploymentPlanRevisionNumber,
		CandidateSlot:                test.fixture.switchCandidate.Slot}
	reservation, err := test.result.candidates.ReserveReplacement(ctx, replacement)
	if err != nil {
		return nil, fmt.Errorf("reserve PreviousExact scope: %w", err)
	}
	scope := &generatedRouteColdAccountingPreviousExactScope{reservation: reservation}
	scope.execution, err = test.result.candidates.PinCandidates(ctx,
		GeneratedCandidateRequest(replacement), reservation)
	if err != nil {
		scope.leave()
		return nil, fmt.Errorf("pin PreviousExact candidates: %w", err)
	}
	candidate, err := scope.execution.ObserveRouteCandidate(test.fixture.componentName)
	if err != nil {
		scope.leave()
		return nil, fmt.Errorf("observe PreviousExact candidate: %w", err)
	}
	request := generatedRouteRequest(test.fixture.migration.guarded.appID, candidate)
	request.FromSlot = generatedidentity.Slot(test.fixture.switchPredecessorHead.Slot)
	scope.sealed, err = test.result.routes.Pin(ctx, scope.execution, request)
	if err != nil || scope.sealed == nil ||
		scope.sealed.gatewayDisposition != generatedDomainColdPendingGatewayPreviousExact {
		scope.leave()
		return nil, fmt.Errorf("pin PreviousExact route disposition=%d: %w",
			func() generatedDomainColdPendingGatewayDisposition {
				if scope.sealed == nil {
					return generatedDomainColdPendingGatewayUnknown
				}
				return scope.sealed.gatewayDisposition
			}(), err)
	}
	return scope, nil
}

type generatedRouteColdAccountingClearLostReplyRunner struct {
	store         *stateStore
	mutate        func()
	failReply     bool
	beforePersist bool
	failureText   string
	fired         bool
}

func (r *generatedRouteColdAccountingClearLostReplyRunner) intercept(
	_ []string, _ *generatedRoutePredecessorRunner,
) (runtimeprocess.CommandResult, bool, error) {
	if r == nil || r.store == nil {
		return runtimeprocess.CommandResult{}, true, errors.New("missing PreviousExact clear lost-reply runner")
	}
	state, err := r.store.load()
	if err != nil {
		return runtimeprocess.CommandResult{}, true, err
	}
	ready := r.beforePersist && state.Pending != nil || !r.beforePersist && state.Pending == nil
	if !r.fired && ready && generatedRouteColdAccountingClearAttestationFrame() {
		r.fired = true
		if r.mutate != nil {
			r.mutate()
		}
		if r.failReply {
			message := r.failureText
			if message == "" {
				message = "fixed PreviousExact clear lost reply"
			}
			return runtimeprocess.CommandResult{}, true, errors.New(message)
		}
	}
	return runtimeprocess.CommandResult{}, false, nil
}

func generatedRouteColdAccountingClearAttestationFrame() bool {
	var callers [32]uintptr
	count := runtime.Callers(2, callers[:])
	frames := runtime.CallersFrames(callers[:count])
	clear, projection := false, false
	for {
		frame, more := frames.Next()
		switch {
		case strings.HasSuffix(frame.Function, ".(*Manager).clearLegacyRouteActivationRollbackPendingLocked"):
			clear = true
		case strings.HasSuffix(frame.Function, ".(*Manager).attestLegacyRouteProjectionLocked"):
			projection = true
		}
		if clear && projection {
			return true
		}
		if !more {
			return false
		}
	}
}

// generatedRouteColdAccountingCheckCrossedPreviousExactAuthorityInput calls
// the production read-only authority gate with an explicit crossed input. It
// does not mutate the sealed execution or bypass immutable SQL triggers.
func generatedRouteColdAccountingCheckCrossedPreviousExactAuthorityInput(
	test generatedRouteColdAccountingPreviousExactAuthorityCase,
	scope *generatedRouteColdAccountingPreviousExactScope,
	claim deploymentexecution.Claim, head generatedruntimestate.ActiveHead,
) error {
	if scope == nil || scope.execution == nil || scope.sealed == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	state, done, err := scope.execution.begin(false)
	if err != nil {
		return err
	}
	defer done()
	crossedState := &generatedCandidateExecutionState{pinned: state.pinned, pins: state.pins}
	crossedState.pins.claim = claim
	crossedExecution := *scope.sealed
	crossedExecution.expectedHead = head
	return crossedExecution.checkRouteActivationAuthority(test.context, crossedState)
}

func generatedRouteColdAccountingAssertPreviousExactAuthorityBoundary(t *testing.T,
	test generatedRouteColdAccountingPreviousExactAuthorityCase, pending bool,
) {
	t.Helper()
	state, err := test.result.manager.store.load()
	want := cloneRouteState(test.fixture.switchPostMVState)
	if !pending {
		want.Pending = nil
	}
	if err != nil || !reflect.DeepEqual(state, want) {
		t.Fatalf("PreviousExact authority protected state=%#v want=%#v error=%v", state, want, err)
	}
	previousConfig := expectedConfig(t, test.fixture.routes.ingressRunner,
		map[string]routeRecord{test.fixture.migration.guarded.appID: test.fixture.switchPreviousRoute})
	if !bytes.Equal(test.fixture.routes.liveConfig, previousConfig) ||
		!bytes.Equal(test.fixture.routes.files[caddyContainerName+":/config/active.json"], previousConfig) {
		t.Fatal("PreviousExact authority refusal changed physical Previous")
	}
	var activations, deadReceipt int
	if err := test.fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
		WHERE app_id=? AND successor_deployment_id=? AND successor_generation=2`,
		test.fixture.migration.guarded.appID, test.fixture.migration.request.DeploymentID).Scan(&activations); err != nil {
		t.Fatal(err)
	}
	if err := test.fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`,
		test.fixture.switchReceiptID).Scan(&deadReceipt); err != nil {
		t.Fatal(err)
	}
	if activations != 0 || deadReceipt != 0 ||
		test.fixture.routes.restartInstalls != test.beforeRestartInstalls ||
		generatedRouteColdAccountingProposedReloadCount(test.fixture.routes.commands) != test.beforeProposedReloads ||
		test.fixture.predecessor.count(candidateOwnerStop) != test.beforeStop ||
		test.fixture.predecessor.count(candidateOwnerRemove) != test.beforeRemove ||
		generatedRouteColdAccountingCapacityProbeCount(test.fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(test.beforeCommands) ||
		generatedRouteColdAccountingCapacityProbeCount(test.fixture.routes.commands) !=
			test.beforeRouteCapacityProbes {
		t.Fatalf("PreviousExact authority refusal activations=%d dead=%d restart=%d/%d reload=%d/%d stop=%d/%d remove=%d/%d",
			activations, deadReceipt, test.fixture.routes.restartInstalls, test.beforeRestartInstalls,
			generatedRouteColdAccountingProposedReloadCount(test.fixture.routes.commands), test.beforeProposedReloads,
			test.fixture.predecessor.count(candidateOwnerStop), test.beforeStop,
			test.fixture.predecessor.count(candidateOwnerRemove), test.beforeRemove)
	}
	afterDebt := generatedRouteColdAccountingCapturePreviousExactDebt(t, test.result, test.fixture)
	if !reflect.DeepEqual(afterDebt, test.debt) {
		t.Fatal("PreviousExact authority refusal changed exact restored debt identities")
	}
}

func generatedRouteColdAccountingAssertPreviousExactAuthorityCompleted(t *testing.T,
	test generatedRouteColdAccountingPreviousExactAuthorityCase,
	retirement *GeneratedPredecessorRetirement, claim deploymentexecution.Claim,
) {
	t.Helper()
	if retirement == nil || retirement.state == nil || retirement.state.receipt.ReceiptID == "" {
		t.Fatal("PreviousExact authority completion omitted retirement receipt")
	}
	test.fixture.firstReceipt = retirement.state.receipt
	receipt := retirement.state.receipt
	var activationCount, deadReceipt int
	if err := test.fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
		WHERE app_id=? AND successor_deployment_id=? AND successor_generation=2`,
		test.fixture.migration.guarded.appID, test.fixture.migration.request.DeploymentID).Scan(&activationCount); err != nil {
		t.Fatal(err)
	}
	if err := test.fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`,
		test.fixture.switchReceiptID).Scan(&deadReceipt); err != nil {
		t.Fatal(err)
	}
	if activationCount != 1 || deadReceipt != 0 || receipt.ReceiptID == test.fixture.switchReceiptID ||
		receipt.CreatorJobID != test.originalCreatorJobID ||
		receipt.CreatorWorkerAttempt != test.originalCreatorAttempt ||
		receipt.WriterJobID != claim.JobID() || receipt.WriterWorkerAttempt != claim.Attempt() {
		t.Fatalf("PreviousExact authority completion activation=%d dead=%d receipt=%#v creator=%q/%d writer=%q/%d",
			activationCount, deadReceipt, receipt, test.originalCreatorJobID, test.originalCreatorAttempt,
			claim.JobID(), claim.Attempt())
	}
	assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, test.fixture)
	generatedRouteColdAccountingAssertPreviousExactDebtAfterCompletion(t, test.result, test.fixture, test.debt)
	if test.fixture.routes.restartInstalls != test.beforeRestartInstalls+2 ||
		generatedRouteColdAccountingProposedReloadCount(test.fixture.routes.commands) != test.beforeProposedReloads+1 ||
		test.fixture.predecessor.count(candidateOwnerStop) != test.beforeStop+1 ||
		test.fixture.predecessor.count(candidateOwnerRemove) != test.beforeRemove+1 ||
		generatedRouteColdAccountingCapacityProbeCount(test.fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(test.beforeCommands) ||
		generatedRouteColdAccountingCapacityProbeCount(test.fixture.routes.commands) !=
			test.beforeRouteCapacityProbes {
		t.Fatalf("PreviousExact authority completion effects restart=%d/%d reload=%d/%d stop=%d/%d remove=%d/%d",
			test.fixture.routes.restartInstalls, test.beforeRestartInstalls+2,
			generatedRouteColdAccountingProposedReloadCount(test.fixture.routes.commands), test.beforeProposedReloads+1,
			test.fixture.predecessor.count(candidateOwnerStop), test.beforeStop+1,
			test.fixture.predecessor.count(candidateOwnerRemove), test.beforeRemove+1)
	}
}

func TestGeneratedDomainRestartAccountingPreviousExactClearAuthorityControls(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, row := range []string{
		"post-clear lost reply retries exact Before",
		"pre-persist clear failure with crossed attempt refuses retry",
		"stale attempt before clear",
		"durable cancellation before clear",
		"crossed deployment binding input refuses authority helper before clear",
		"crossed active head input refuses authority helper before clear",
		"post-clear crossed attempt refuses begin then same construction fresh worker completes",
	} {
		t.Run(row, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				testGeneratedDomainRestartAccountingPreviousExactClearAuthorityControl(t, row)
			})
		})
	}
}

func testGeneratedDomainRestartAccountingPreviousExactClearAuthorityControl(t *testing.T, row string) {
	t.Helper()
	test := generatedRouteColdAccountingPreviousExactAuthorityFixture(t)
	scope, err := generatedRouteColdAccountingPreparePreviousExactScope(test.context, test, test.claim)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.leave()

	switch row {
	case "post-clear lost reply retries exact Before":
		fault := &generatedRouteColdAccountingClearLostReplyRunner{
			store: test.result.manager.store, failReply: true,
		}
		test.fixture.process.setRouteReadHook(fault.intercept)
		_, retirement, activateErr := scope.sealed.ActivateWithPredecessorRetirement(test.context)
		test.fixture.process.setRouteReadHook(nil)
		if activateErr != nil || retirement == nil || !fault.fired {
			t.Fatalf("PreviousExact lost-clear retry retirement=%#v fired=%t error=%v", retirement, fault.fired, activateErr)
		}
		defer retirement.Leave()
		if err := retirement.Complete(test.context); err != nil {
			t.Fatalf("complete PreviousExact lost-clear retry: %v", err)
		}
		generatedRouteColdAccountingAssertPreviousExactAuthorityCompleted(t, test, retirement, test.claim)
		return
	case "pre-persist clear failure with crossed attempt refuses retry":
		var mutationErr error
		var mutationRows int64
		fault := &generatedRouteColdAccountingClearLostReplyRunner{
			store: test.result.manager.store, failReply: true, beforePersist: true,
			failureText: "fixed PreviousExact pre-persist clear failure",
			mutate: func() {
				changed, updateErr := test.fixture.identity.database.Exec(
					`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=? AND attempt=?`,
					time.Now().UTC().Format(time.RFC3339Nano), test.claim.JobID(), test.claim.Attempt())
				if updateErr != nil {
					mutationErr = updateErr
					return
				}
				mutationRows, mutationErr = changed.RowsAffected()
			},
		}
		test.fixture.process.setRouteReadHook(fault.intercept)
		_, retirement, activateErr := scope.sealed.ActivateWithPredecessorRetirement(test.context)
		test.fixture.process.setRouteReadHook(nil)
		if retirement != nil {
			retirement.Leave()
			t.Fatal("crossed lost-clear retry minted retirement authority")
		}
		if mutationErr != nil || mutationRows != 1 || !fault.fired ||
			!errors.Is(activateErr, deploymentexecution.ErrIdentityLost) ||
			!IsCode(activateErr, DiagnosticIngressDrift) {
			t.Fatalf("crossed pre-persist clear retry rows=%d fired=%t mutation=%v activation=%v",
				mutationRows, fault.fired, mutationErr, activateErr)
		}
		generatedRouteColdAccountingAssertPreviousExactAuthorityBoundary(t, test, true)
		return
	case "stale attempt before clear":
		_, err = test.fixture.identity.database.Exec(`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339Nano), test.claim.JobID())
	case "durable cancellation before clear":
		_, err = test.fixture.identity.database.Exec(`UPDATE jobs SET phase='cancelling',updated_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339Nano), test.claim.JobID())
	case "crossed deployment binding input refuses authority helper before clear":
		var crossedJobID string
		var crossedAttempt int
		if err = test.fixture.identity.database.QueryRow(`SELECT deployment.job_id,job.attempt
			FROM deployments deployment JOIN jobs job ON job.id=deployment.job_id
			WHERE deployment.id=?`, test.fixture.lifecycleDeploymentID).Scan(
			&crossedJobID, &crossedAttempt); err != nil {
			t.Fatal(err)
		}
		binding, bindingErr := test.fixture.migration.bindings.Read(test.context, crossedJobID)
		if bindingErr != nil {
			t.Fatal(bindingErr)
		}
		classification, classificationErr := test.fixture.migration.bindings.Check(test.context, binding)
		if classificationErr != nil || classification != deploymentbinding.ClassificationStale {
			t.Fatalf("crossed PreviousExact binding classification=%d error=%v",
				classification, classificationErr)
		}
		crossedClaim, claimErr := deploymentexecution.NewClaim(binding, crossedJobID,
			test.fixture.migration.guarded.appID, crossedAttempt)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		err = generatedRouteColdAccountingCheckCrossedPreviousExactAuthorityInput(test, scope,
			crossedClaim, scope.sealed.expectedHead)
		if !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
			t.Fatalf("crossed PreviousExact binding authority helper error=%v", err)
		}
		generatedRouteColdAccountingAssertPreviousExactAuthorityBoundary(t, test, true)
		return
	case "crossed active head input refuses authority helper before clear":
		crossedHead := scope.sealed.expectedHead
		crossedHead.Generation++
		crossedHead.ActivationReceiptID = uuid.NewString()
		err = generatedRouteColdAccountingCheckCrossedPreviousExactAuthorityInput(test, scope,
			test.claim, crossedHead)
		if !errors.Is(err, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("crossed PreviousExact head authority helper error=%v", err)
		}
		generatedRouteColdAccountingAssertPreviousExactAuthorityBoundary(t, test, true)
		return
	case "post-clear crossed attempt refuses begin then same construction fresh worker completes":
		var mutationErr error
		var mutationRows int64
		fault := &generatedRouteColdAccountingClearLostReplyRunner{store: test.result.manager.store}
		fault.mutate = func() {
			changed, updateErr := test.fixture.identity.database.Exec(
				`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=? AND attempt=?`,
				time.Now().UTC().Format(time.RFC3339Nano), test.claim.JobID(), test.claim.Attempt())
			if updateErr != nil {
				mutationErr = updateErr
				return
			}
			mutationRows, mutationErr = changed.RowsAffected()
		}
		test.fixture.process.setRouteReadHook(fault.intercept)
		_, retirement, activateErr := scope.sealed.ActivateWithPredecessorRetirement(test.context)
		test.fixture.process.setRouteReadHook(nil)
		if retirement != nil {
			retirement.Leave()
			t.Fatal("post-clear crossed attempt minted retirement authority")
		}
		if mutationErr != nil || mutationRows != 1 || !fault.fired ||
			!errors.Is(activateErr, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("post-clear crossed attempt rows=%d fired=%t mutation=%v activation=%v",
				mutationRows, fault.fired, mutationErr, activateErr)
		}
		generatedRouteColdAccountingAssertPreviousExactAuthorityBoundary(t, test, false)
		scope.leave()
		observed, observeErr := inspectGeneratedDomainStartupObservation(test.context,
			test.fixture.identity.stores, test.fixture.process, test.fixture.identity.options)
		if observeErr != nil {
			t.Fatalf("observe cleared Previous markerless prefix: %v", observeErr)
		}
		if groups, markerlessErr := readGeneratedDomainColdGroups(test.context, test.result.candidates,
			observed); markerlessErr == nil || groups != nil {
			t.Fatalf("no-expected cold reader accepted markerless cleared Previous groups=%#v error=%v",
				groups, markerlessErr)
		}
		binding, claimErr := test.fixture.migration.bindings.Read(test.context, test.claim.JobID())
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		freshClaim, claimErr := deploymentexecution.NewClaim(binding, test.claim.JobID(),
			test.claim.ApplicationID(), test.claim.Attempt()+1)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		freshScope, scopeErr := generatedRouteColdAccountingPreparePreviousExactScope(test.context, test, freshClaim)
		if scopeErr != nil {
			t.Fatalf("prepare same-construction worker after cleared Previous: %v", scopeErr)
		}
		defer freshScope.leave()
		_, freshRetirement, freshErr := freshScope.sealed.ActivateWithPredecessorRetirement(test.context)
		if freshErr != nil || freshRetirement == nil {
			t.Fatalf("same-construction worker after cleared Previous retirement=%#v error=%v",
				freshRetirement, freshErr)
		}
		defer freshRetirement.Leave()
		if err := freshRetirement.Complete(test.context); err != nil {
			t.Fatalf("complete same-construction worker after cleared Previous: %v", err)
		}
		generatedRouteColdAccountingAssertPreviousExactAuthorityCompleted(t, test, freshRetirement, freshClaim)
		return
	default:
		t.Fatalf("unknown PreviousExact authority row %q", row)
	}
	if err != nil {
		t.Fatalf("mutate PreviousExact authority row %q: %v", row, err)
	}
	_, retirement, activateErr := scope.sealed.ActivateWithPredecessorRetirement(test.context)
	if retirement != nil {
		retirement.Leave()
		t.Fatalf("PreviousExact authority row %q minted retirement", row)
	}
	if !errors.Is(activateErr, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("PreviousExact authority row %q error=%v", row, activateErr)
	}
	if row == "durable cancellation before clear" {
		var phase string
		if err := test.fixture.identity.database.QueryRow(`SELECT phase FROM jobs WHERE id=?`,
			test.claim.JobID()).Scan(&phase); err != nil || phase != "cancelling" {
			t.Fatalf("PreviousExact durable cancellation phase=%q error=%v", phase, err)
		}
	}
	generatedRouteColdAccountingAssertPreviousExactAuthorityBoundary(t, test, true)
}

type generatedRouteColdAccountingPreviousExactDebt struct {
	predecessorGroup     *candidateCapacityGroup
	predecessorComponent *candidateCapacityComponent
	predecessorCharge    *candidateCapacityCharge
	predecessorReceipt   *candidateReceiptTombstone
	predecessorToken     *generatedDomainCapacityDebtToken
	predecessorLimits    generatedcapacity.Limits
	successorGroup       *candidateCapacityGroup
	successorComponent   *candidateCapacityComponent
	successorCharge      *candidateCapacityCharge
	successorReceipt     *candidateReceiptTombstone
	successorToken       *generatedDomainCapacityDebtToken
	successorLimits      generatedcapacity.Limits
}

func generatedRouteColdAccountingCapturePreviousExactDebt(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
) generatedRouteColdAccountingPreviousExactDebt {
	t.Helper()
	ledger := result.candidates.ledger
	ledger.mu.Lock()
	predecessor := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.lifecycleDeploymentID}]
	successor := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	groupCount := len(ledger.groups)
	ledger.mu.Unlock()
	if predecessor == nil || successor == nil || predecessor == successor || groupCount != 2 {
		t.Fatalf("PreviousExact debt groups=%d predecessor=%#v successor=%#v", groupCount, predecessor, successor)
	}
	read := func(group *candidateCapacityGroup, receiptID string, owner candidateAccountingOwner) (
		*candidateCapacityComponent, *candidateCapacityCharge, *candidateReceiptTombstone,
		*generatedDomainCapacityDebtToken, generatedcapacity.Limits,
	) {
		t.Helper()
		group.mu.Lock()
		defer group.mu.Unlock()
		component := group.byName[fixture.componentName]
		if len(group.components) != 1 || group.components[0] != fixture.componentName || component == nil ||
			component.receipt == nil || component.receipt.id != receiptID || component.tokenHeld ||
			component.charge == nil || component.charge.state != candidateChargeActive ||
			component.charge.owner != owner || component.charge.publicationOwner != 0 ||
			!component.charge.receiptRetained || component.charge.explicitRetained ||
			component.charge.logicallyRetired || component.charge.lease != nil ||
			candidatePhysicalFromWord(component.receipt.state.Load()) != candidatePhysicalHandedToRoute {
			t.Fatalf("PreviousExact debt owner=%d group=%#v component=%#v", owner, group, component)
		}
		token, ok := component.charge.token.(*generatedDomainCapacityDebtToken)
		if !ok || token == nil || !token.configured() || !token.published() {
			t.Fatalf("PreviousExact debt owner=%d token=%#v", owner, component.charge.token)
		}
		return component, component.charge, component.receipt, token, group.limits
	}
	value := generatedRouteColdAccountingPreviousExactDebt{predecessorGroup: predecessor,
		successorGroup: successor}
	value.predecessorComponent, value.predecessorCharge, value.predecessorReceipt,
		value.predecessorToken, value.predecessorLimits = read(predecessor, fixture.predecessorReceipt,
		candidateAccountingRouteActive)
	value.successorComponent, value.successorCharge, value.successorReceipt,
		value.successorToken, value.successorLimits = read(successor, fixture.componentReceipt,
		candidateAccountingCandidate)
	result.capacity.mu.Lock()
	_, predecessorRegistered := result.capacity.tokens[value.predecessorToken]
	_, successorRegistered := result.capacity.tokens[value.successorToken]
	coordinatorExact := result.capacity.phase == generatedDomainCapacityPublished && result.capacity.borrows == 0 &&
		len(result.capacity.tokens) == 2 && predecessorRegistered && successorRegistered &&
		result.capacity.memory == value.predecessorLimits.MemoryBytes+value.successorLimits.MemoryBytes &&
		result.capacity.disk == value.predecessorLimits.DiskBytes+value.successorLimits.DiskBytes
	result.capacity.mu.Unlock()
	if !coordinatorExact || value.predecessorToken == value.successorToken {
		t.Fatal("PreviousExact cold reconstruction omitted exact predecessor/successor debt")
	}
	return value
}

func generatedRouteColdAccountingAssertPreviousExactDebtAfterCompletion(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
	before generatedRouteColdAccountingPreviousExactDebt,
) {
	t.Helper()
	ledger := result.candidates.ledger
	ledger.mu.Lock()
	predecessor := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.lifecycleDeploymentID}]
	successor := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	groupCount := len(ledger.groups)
	ledger.mu.Unlock()
	if predecessor != before.predecessorGroup || successor != before.successorGroup || groupCount != 2 {
		t.Fatal("PreviousExact completion changed exact capacity groups")
	}
	predecessor.mu.Lock()
	predecessorComponent := predecessor.byName[fixture.componentName]
	predecessorExact := predecessorComponent == before.predecessorComponent &&
		predecessorComponent.receipt == before.predecessorReceipt &&
		predecessorComponent.charge == before.predecessorCharge && !predecessorComponent.tokenHeld &&
		before.predecessorCharge.state == candidateChargeReleased &&
		before.predecessorCharge.owner == candidateAccountingPredecessorRetired &&
		before.predecessorCharge.explicitRetained && before.predecessorCharge.logicallyRetired &&
		before.predecessorCharge.token == nil && before.predecessorCharge.lease == nil &&
		candidatePhysicalFromWord(before.predecessorReceipt.state.Load()) == candidatePhysicalConfirmedAbsent
	predecessor.mu.Unlock()
	successor.mu.Lock()
	successorComponent := successor.byName[fixture.componentName]
	successorExact := successorComponent == before.successorComponent &&
		successorComponent.receipt == before.successorReceipt &&
		successorComponent.charge == before.successorCharge && !successorComponent.tokenHeld &&
		before.successorCharge.state == candidateChargeActive &&
		before.successorCharge.owner == candidateAccountingRouteActive &&
		before.successorCharge.publicationOwner == 0 && before.successorCharge.receiptRetained &&
		before.successorCharge.explicitRetained && !before.successorCharge.logicallyRetired &&
		before.successorCharge.token == before.successorToken && before.successorCharge.lease == nil &&
		candidatePhysicalFromWord(before.successorReceipt.state.Load()) == candidatePhysicalHandedToRoute
	successor.mu.Unlock()
	result.capacity.mu.Lock()
	_, predecessorRegistered := result.capacity.tokens[before.predecessorToken]
	_, successorRegistered := result.capacity.tokens[before.successorToken]
	coordinatorExact := result.capacity.phase == generatedDomainCapacityPublished && result.capacity.borrows == 0 &&
		len(result.capacity.tokens) == 1 && !predecessorRegistered && successorRegistered &&
		!before.predecessorToken.active && before.successorToken.active &&
		result.capacity.memory == before.successorLimits.MemoryBytes &&
		result.capacity.disk == before.successorLimits.DiskBytes
	result.capacity.mu.Unlock()
	if !predecessorExact || !successorExact || !coordinatorExact ||
		!before.successorToken.configured() || !before.successorToken.published() {
		t.Fatalf("PreviousExact completion debt predecessor=%t successor=%t coordinator=%t",
			predecessorExact, successorExact, coordinatorExact)
	}
}

func TestGeneratedDomainRestartAccountingInitialSwitchingRouteRecoversActivationWithoutSecondShare(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingInitialSwitchingRouteFixture(t)
		assertGeneratedRouteColdAccountingInitialSwitchingRouteSnapshot(t, fixture)
		beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
		beforeCommands := fixture.construction.snapshot()
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)
		beforeRestartInstalls := fixture.routes.restartInstalls
		beforeProposedReloads := generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands)
		if beforeRestartInstalls != 2 ||
			beforeProposedReloads != generatedRouteColdAccountingInitialSwitchingRoutePhysicalProposedReloads ||
			beforeStop != 0 || beforeRemove != 0 {
			t.Fatalf("initial SwitchingRoute producer effects restart=%d proposed=%d stop=%d remove=%d",
				beforeRestartInstalls, beforeProposedReloads, beforeStop, beforeRemove)
		}

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil || result == nil || result.candidates == nil || result.routes == nil || result.cold == nil {
			t.Fatalf("initial SwitchingRoute cold construction result=%#v error=%v", result, err)
		}
		before, hadBefore, proposed, transitionValid :=
			generatedRouteActivationGatewayRoutes(result.cold.pendingActivation)
		if result.cold.pendingActivation == nil ||
			result.cold.pendingActivationDeploymentID != fixture.migration.request.DeploymentID ||
			result.cold.pendingGatewayDisposition != generatedDomainColdPendingGatewayProposedExact ||
			!transitionValid || hadBefore || !sameRoute(before, routeRecord{}) ||
			!sameRoute(proposed, fixture.switchPostMVState.Pending.Proposed) {
			t.Fatalf("initial SwitchingRoute cold manifest=%#v before=%#v had=%t proposed=%#v valid=%t",
				result.cold, before, hadBefore, proposed, transitionValid)
		}
		assertGeneratedRouteColdAccountingInitialSwitchingRouteSnapshot(t, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
		assertGeneratedRouteColdAccountingSwitchingRouteHandoffSeed(t, result, fixture)
		initialDebt := generatedRouteColdAccountingAssertInitialActivationDebt(t, result, fixture,
			candidateAccountingCandidate, nil)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("initial SwitchingRoute cold reconstruction acquired a second capacity share")
		}
		generatedRouteColdAccountingAssertInitialPendingClassifierGuards(t, result, fixture)
		assertGeneratedRouteColdAccountingInitialSwitchingRouteSnapshot(t, fixture)
		assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
		if fixture.routes.restartInstalls != beforeRestartInstalls ||
			generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) != beforeProposedReloads ||
			fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatal("initial SwitchingRoute classifier guards reached runtime effects")
		}
		fixture.process.setRoutes(fixture.routes)

		var beforeAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
			fixture.migration.claim.JobID()).Scan(&beforeAttempt); err != nil {
			t.Fatal(err)
		}
		var originalCreatorJobID string
		var originalCreatorAttempt int
		if err := fixture.identity.database.QueryRow(`SELECT creator_job_id,creator_worker_attempt
			FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt).Scan(
			&originalCreatorJobID, &originalCreatorAttempt); err != nil {
			t.Fatal(err)
		}
		recovered, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.identity.database,
			time.Now().UTC(), fixture.migration.bindings)
		if err != nil || recovered.Jobs.RequeuedGenerated != 1 {
			t.Fatalf("recover initial SwitchingRoute worker: result=%+v error=%v", recovered, err)
		}

		var claimed jobs.Job
		var claimedProofErr error
		var recoveredReceipt generatedruntimestate.RouteActivationReceipt
		runGeneratedColdBoundaryWorker(t, fixture.jobs,
			generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job,
				_ jobs.ProgressReporter,
			) (jobs.ExecutionResult, error) {
				claimed = job
				observed, observeErr := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
					fixture.process, fixture.identity.options)
				if observeErr != nil {
					claimedProofErr = fmt.Errorf("post-claim initial SwitchingRoute observation: %w", observeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				if attestErr := reattestGeneratedDomainCold(ctx, result.manager, result.candidates,
					observed, *result.cold); attestErr != nil {
					claimedProofErr = fmt.Errorf("post-claim initial SwitchingRoute manifest: %w", attestErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				binding, bindingErr := fixture.migration.bindings.Read(ctx, job.ID)
				if bindingErr != nil {
					claimedProofErr = fmt.Errorf("post-claim initial SwitchingRoute binding: %w", bindingErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				claim, claimErr := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
				if claimErr != nil {
					claimedProofErr = fmt.Errorf("post-claim initial SwitchingRoute claim: %w", claimErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				replacement := GeneratedReplacementRequest{Claim: claim,
					DeploymentID:                 fixture.migration.request.DeploymentID,
					ReleaseID:                    fixture.migration.request.ReleaseID,
					ConfigurationRevisionID:      fixture.migration.request.ConfigurationRevisionID,
					ConfigurationRevisionNumber:  fixture.migration.request.ConfigurationRevisionNumber,
					DeploymentPlanRevisionID:     fixture.migration.request.DeploymentPlanRevisionID,
					DeploymentPlanRevisionNumber: fixture.migration.request.DeploymentPlanRevisionNumber,
					CandidateSlot:                fixture.switchCandidate.Slot}
				reservation, reserveErr := result.candidates.ReserveReplacement(ctx, replacement)
				if reserveErr != nil {
					claimedProofErr = fmt.Errorf("reserve recovered initial candidate: %w", reserveErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				defer reservation.Release()
				execution, pinErr := result.candidates.PinCandidates(ctx,
					GeneratedCandidateRequest(replacement), reservation)
				if pinErr != nil {
					claimedProofErr = fmt.Errorf("pin recovered initial candidate: %w", pinErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				defer execution.Leave()
				candidate, candidateErr := execution.ObserveRouteCandidate(fixture.componentName)
				if candidateErr != nil {
					claimedProofErr = fmt.Errorf("observe recovered initial candidate: %w", candidateErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				request := generatedRouteRequest(fixture.migration.guarded.appID, candidate)
				invalid := request
				invalid.FromSlot = generatedidentity.SlotBlue
				if invalid.FromSlot == invalid.ToSlot {
					invalid.FromSlot = generatedidentity.SlotGreen
				}
				pinProtection := generatedRouteColdAccountingProtection(t, fixture)
				pinCommands := len(fixture.routes.commands)
				if invalidSealed, invalidErr := result.routes.Pin(ctx, execution, invalid); invalidSealed != nil || !errors.Is(invalidErr, deploymentexecution.ErrAuthorityStale) {
					claimedProofErr = fmt.Errorf("initial nonempty FromSlot sealed=%#v error=%w",
						invalidSealed, invalidErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				assertGeneratedRouteColdAccountingProtectionUnchanged(t, pinProtection, fixture)
				if len(fixture.routes.commands) != pinCommands ||
					generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
						generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
					claimedProofErr = errors.New("initial nonempty FromSlot reached route or capacity effects")
					return jobs.ExecutionResult{}, claimedProofErr
				}
				sealed, routeErr := result.routes.Pin(ctx, execution, request)
				if routeErr != nil {
					claimedProofErr = fmt.Errorf("pin recovered initial activation: %w", routeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				_, retirement, routeErr := sealed.ActivateWithPredecessorRetirement(ctx)
				if routeErr != nil || retirement == nil {
					claimedProofErr = fmt.Errorf("activate recovered initial retirement=%#v: %w",
						retirement, routeErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				defer retirement.Leave()
				recoveredReceipt = retirement.state.receipt
				if completionErr := retirement.Complete(ctx); completionErr != nil {
					claimedProofErr = fmt.Errorf("complete recovered initial activation: %w", completionErr)
					return jobs.ExecutionResult{}, claimedProofErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			}), fixture.migration.claim.JobID(), jobs.Succeeded,
			fixture.identity.options.WorkingDirectory, func() string { return boundedErrorText(claimedProofErr) })
		if claimedProofErr != nil {
			t.Fatalf("fresh initial SwitchingRoute worker proof: %v", claimedProofErr)
		}
		if claimed.ID != fixture.migration.claim.JobID() || claimed.ResourceID != fixture.migration.guarded.appID ||
			claimed.Attempt != beforeAttempt+1 {
			t.Fatalf("fresh initial SwitchingRoute worker=%+v beforeAttempt=%d receipt=%#v", claimed,
				beforeAttempt, recoveredReceipt)
		}
		var relevantActivations, deadReceiptRows int
		if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
			WHERE app_id=? AND successor_deployment_id=? AND successor_generation=1`,
			fixture.migration.guarded.appID, fixture.migration.request.DeploymentID).
			Scan(&relevantActivations); err != nil {
			t.Fatal(err)
		}
		if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations WHERE id=?`,
			fixture.switchReceiptID).Scan(&deadReceiptRows); err != nil {
			t.Fatal(err)
		}
		if relevantActivations != 1 || deadReceiptRows != 0 || recoveredReceipt.ReceiptID == "" ||
			recoveredReceipt.ReceiptID == fixture.switchReceiptID || recoveredReceipt.SuccessorGeneration != 1 ||
			recoveredReceipt.PredecessorDeploymentID != "" || recoveredReceipt.PredecessorReleaseID != "" ||
			recoveredReceipt.PredecessorSlot != "" || recoveredReceipt.PredecessorGeneration != 0 ||
			recoveredReceipt.PredecessorActivationReceiptID != "" ||
			recoveredReceipt.CreatorJobID != originalCreatorJobID ||
			recoveredReceipt.CreatorWorkerAttempt != originalCreatorAttempt ||
			recoveredReceipt.WriterJobID != claimed.ID || recoveredReceipt.WriterWorkerAttempt != claimed.Attempt {
			t.Fatalf("recovered initial activation count=%d dead=%d receipt=%#v creator=%q/%d claimed=%q/%d",
				relevantActivations, deadReceiptRows, recoveredReceipt, originalCreatorJobID,
				originalCreatorAttempt, claimed.ID, claimed.Attempt)
		}
		fixture.firstReceipt = recoveredReceipt
		assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
		generatedRouteColdAccountingAssertInitialActivationDebt(t, result, fixture,
			candidateAccountingRouteActive, &initialDebt)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("fresh initial SwitchingRoute worker acquired a second capacity share")
		}
		if fixture.routes.restartInstalls != beforeRestartInstalls ||
			generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) != beforeProposedReloads ||
			fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove {
			t.Fatalf("fresh initial SwitchingRoute effects restart=%d/%d stop=%d/%d remove=%d/%d",
				beforeRestartInstalls, fixture.routes.restartInstalls, beforeStop,
				fixture.predecessor.count(candidateOwnerStop), beforeRemove,
				fixture.predecessor.count(candidateOwnerRemove))
		}
	})
}

// generatedRouteColdAccountingAssertInitialPendingClassifierGuards exercises
// cloned cold evidence only. It proves each noncanonical initial-head shape and
// a crossed Previous record are refused without rewriting immutable SQL.
func generatedRouteColdAccountingAssertInitialPendingClassifierGuards(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	observed, err := inspectGeneratedDomainStartupObservation(context.Background(), fixture.identity.stores,
		fixture.process, fixture.identity.options)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := readGeneratedDomainColdGroups(context.Background(), result.candidates, observed)
	if err != nil {
		t.Fatal(err)
	}
	control, found, err := generatedDomainColdPendingActivation(observed.database, observed.protected, groups)
	if err != nil || !found || !control.beforeActivation ||
		control.successorDeployment != fixture.migration.request.DeploymentID {
		t.Fatalf("initial Pending classifier control=%#v found=%t error=%v", control, found, err)
	}
	cloneGroups := func() []generatedDomainColdGroup {
		return append([]generatedDomainColdGroup(nil), groups...)
	}
	mutateHead := func(values []generatedDomainColdGroup, mutate func(*candidateHeadSnapshot)) {
		t.Helper()
		for index := range values {
			if values[index].pins.deploymentID == fixture.migration.request.DeploymentID {
				mutate(&values[index].snapshot.observedHead)
				return
			}
		}
		t.Fatal("initial Pending classifier omitted successor group")
	}
	for _, test := range []struct {
		name   string
		mutate func(*candidateHeadSnapshot)
	}{
		{name: "nonempty deployment at generation zero", mutate: func(value *candidateHeadSnapshot) {
			value.deploymentID = uuid.NewString()
		}},
		{name: "nonempty release at generation zero", mutate: func(value *candidateHeadSnapshot) {
			value.releaseID = uuid.NewString()
		}},
		{name: "nonempty slot at generation zero", mutate: func(value *candidateHeadSnapshot) {
			value.slot = string(generatedidentity.SlotGreen)
		}},
		{name: "nonempty receipt at generation zero", mutate: func(value *candidateHeadSnapshot) {
			value.activationReceiptID = uuid.NewString()
		}},
		{name: "nonzero generation", mutate: func(value *candidateHeadSnapshot) { value.generation = 1 }},
		{name: "crossed positive-looking head", mutate: func(value *candidateHeadSnapshot) {
			value.deploymentID = uuid.NewString()
			value.releaseID = fixture.migration.request.ReleaseID
			value.slot = fixture.switchCandidate.Slot
			value.generation = 1
			value.activationReceiptID = uuid.NewString()
		}},
	} {
		t.Run("classifier helper "+test.name, func(t *testing.T) {
			invalid := cloneGroups()
			mutateHead(invalid, test.mutate)
			if evidence, found, err := generatedDomainColdPendingActivation(
				observed.database, observed.protected, invalid); found ||
				!errors.Is(err, errGeneratedDomainConstructionRefused) || evidence.transition != nil {
				t.Fatalf("invalid initial head evidence=%#v found=%t error=%v", evidence, found, err)
			}
		})
	}
	protected := observed.protected
	if protected.nativeV1 == nil || protected.nativeV1.Pending == nil {
		t.Fatal("initial Pending classifier lacks native V1 protected state")
	}
	state := cloneRouteState(*protected.nativeV1)
	previous := routeRecord{Slot: generatedidentity.SlotGreen,
		Endpoints: append([]generatedroute.RouteEndpoint(nil), state.Pending.Proposed.Endpoints...)}
	state.Pending.Previous = &previous
	state.Active[state.Pending.AppID] = previous
	protected.nativeV1 = &state
	if evidence, found, err := generatedDomainColdPendingActivation(observed.database, protected, groups); found || !errors.Is(err, errGeneratedDomainConstructionRefused) || evidence.transition != nil {
		t.Fatalf("initial empty head accepted Previous evidence=%#v found=%t error=%v", evidence, found, err)
	}
}

type generatedRouteColdAccountingInitialDebtSnapshot struct {
	group     *candidateCapacityGroup
	component *candidateCapacityComponent
	receipt   *candidateReceiptTombstone
	charge    *candidateCapacityCharge
	token     *generatedDomainCapacityDebtToken
	limits    generatedcapacity.Limits
}

func generatedRouteColdAccountingAssertInitialActivationDebt(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
	expectedOwner candidateAccountingOwner, before *generatedRouteColdAccountingInitialDebtSnapshot,
) generatedRouteColdAccountingInitialDebtSnapshot {
	t.Helper()
	if result == nil || result.candidates == nil || result.candidates.ledger == nil ||
		result.capacity == nil || result.candidates.coordinator != result.capacity {
		t.Fatal("initial activation recovery omitted shared capacity accounting")
	}
	ledger := result.candidates.ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	groupCount := len(ledger.groups)
	ledger.mu.Unlock()
	if group == nil || groupCount != 1 {
		t.Fatalf("initial activation recovery groups=%d target=%#v", groupCount, group)
	}
	group.mu.Lock()
	component := group.byName[fixture.componentName]
	charge := (*candidateCapacityCharge)(nil)
	if component != nil {
		charge = component.charge
	}
	token, debt := (*generatedDomainCapacityDebtToken)(nil), false
	if charge != nil {
		token, debt = charge.token.(*generatedDomainCapacityDebtToken)
	}
	value := generatedRouteColdAccountingInitialDebtSnapshot{group: group, component: component,
		charge: charge, token: token, limits: group.limits}
	if component != nil {
		value.receipt = component.receipt
	}
	valid := group.ledger == ledger && len(group.components) == 1 &&
		group.components[0] == fixture.componentName && component != nil &&
		component.receipt != nil && component.receipt.id == fixture.componentReceipt &&
		!component.tokenHeld && charge != nil && charge.state == candidateChargeActive &&
		charge.owner == expectedOwner && charge.publicationOwner == 0 && charge.receiptRetained &&
		charge.explicitRetained == (expectedOwner == candidateAccountingRouteActive) &&
		!charge.logicallyRetired && charge.lease == nil && debt && token != nil &&
		candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalHandedToRoute
	if before != nil {
		valid = valid && before.group == group && before.component == component &&
			before.receipt == component.receipt && before.charge == charge && before.token == token &&
			before.limits == group.limits
	}
	group.mu.Unlock()
	configured, published := token != nil && token.configured(), token != nil && token.published()
	result.capacity.mu.Lock()
	_, registered := result.capacity.tokens[token]
	coordinatorExact := result.capacity.phase == generatedDomainCapacityPublished && result.capacity.borrows == 0 &&
		len(result.capacity.tokens) == 1 && registered && result.capacity.memory == value.limits.MemoryBytes &&
		result.capacity.disk == value.limits.DiskBytes && token != nil && token.owner == result.capacity &&
		token.active && token.epoch != 0 && token.limits == value.limits
	result.capacity.mu.Unlock()
	if !valid || !configured || !published || !coordinatorExact {
		t.Fatalf("initial activation debt owner=%d valid=%t configured=%t published=%t coordinator=%t snapshot=%#v",
			expectedOwner, valid, configured, published, coordinatorExact, value)
	}
	return value
}

func TestGeneratedDomainRestartAccountingPostActivationPendingCompletesWithoutSecondShare(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedRouteColdAccountingPostActivationPendingFixture(t)
		assertGeneratedRouteColdAccountingActivatedSnapshot(t, fixture)
		store, err := newStateStore(fixture.identity.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := store.load()
		if err != nil || protected.Pending == nil || protected.Pending.AppID != fixture.migration.guarded.appID {
			t.Fatalf("post-activation protected state=%#v error=%v", protected, err)
		}
		activationBefore := generatedRouteColdAccountingPostActivationReceipt(t, fixture)
		beforeCommands := fixture.construction.snapshot()
		beforeRestartInstalls := fixture.routes.restartInstalls
		beforeProposedReloads := generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands)
		beforeStop := fixture.predecessor.count(candidateOwnerStop)
		beforeRemove := fixture.predecessor.count(candidateOwnerRemove)

		result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
			fixture.process, fixture.environment, fixture.identity.options)
		if err != nil || result == nil || result.routes == nil || result.candidates == nil || result.cold == nil {
			t.Fatalf("post-activation Pending cold construction result=%#v error=%v", result, err)
		}
		if result.cold.pendingActivation == nil ||
			result.cold.pendingGatewayDisposition != generatedDomainColdPendingGatewayProposedExact {
			t.Fatalf("post-activation Pending cold manifest=%#v", result.cold)
		}
		assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
		assertGeneratedRouteColdAccountingActivatedSnapshot(t, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("post-activation Pending cold construction acquired a fresh capacity share")
		}
		// CompleteRecovered deliberately observes the predecessor drain boundary
		// before retiring it. Keep the authentic boundary while making this
		// recovery test deterministic: the existing recovered-retirement tests
		// exercise the production timer itself.
		var retirementWaits []time.Duration
		result.routes.waitRecoveredRetirement = func(ctx context.Context, delay time.Duration) error {
			if ctx == nil || delay != maximumDrain {
				return fmt.Errorf("post-activation Pending retirement wait context=%#v delay=%s", ctx, delay)
			}
			retirementWaits = append(retirementWaits, delay)
			return nil
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
			t.Fatalf("requeue post-activation Pending worker result=%+v error=%v", recovered, err)
		}

		var claimed jobs.Job
		var workerErr error
		runGeneratedColdBoundaryWorker(t, fixture.jobs,
			generatedColdBoundaryExecutor(func(ctx context.Context, job jobs.Job,
				_ jobs.ProgressReporter,
			) (jobs.ExecutionResult, error) {
				claimed = job
				binding, err := fixture.migration.bindings.Read(ctx, job.ID)
				if err != nil {
					workerErr = err
					return jobs.ExecutionResult{}, err
				}
				claim, err := deploymentexecution.NewClaim(binding, job.ID, job.ResourceID, job.Attempt)
				if err != nil {
					workerErr = err
					return jobs.ExecutionResult{}, err
				}
				workerErr = result.routes.CompleteRecovered(ctx, claim, fixture.migration.request.DeploymentID)
				if workerErr != nil {
					return jobs.ExecutionResult{}, workerErr
				}
				return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
			}), fixture.migration.claim.JobID(), jobs.Succeeded, fixture.identity.options.WorkingDirectory,
			func() string { return boundedErrorText(workerErr) })
		if workerErr != nil || claimed.ID != fixture.migration.claim.JobID() || claimed.Attempt != beforeAttempt+1 {
			t.Fatalf("post-activation Pending completion worker=%+v beforeAttempt=%d error=%v", claimed, beforeAttempt, workerErr)
		}
		protected, err = store.load()
		if err != nil || protected.Pending != nil {
			t.Fatalf("post-activation Pending completion protected state=%#v error=%v", protected, err)
		}
		activationAfter := generatedRouteColdAccountingPostActivationReceipt(t, fixture)
		if !reflect.DeepEqual(activationAfter, activationBefore) {
			t.Fatalf("post-activation completion rewrote 042 before=%#v after=%#v", activationBefore, activationAfter)
		}
		assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
		if generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
			generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
			t.Fatal("post-activation Pending completion acquired a second capacity share")
		}
		if fixture.routes.restartInstalls != beforeRestartInstalls ||
			generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) != beforeProposedReloads ||
			fixture.predecessor.count(candidateOwnerStop) != beforeStop+1 ||
			fixture.predecessor.count(candidateOwnerRemove) != beforeRemove+1 {
			t.Fatalf("post-activation Pending completion effects restart=%d/%d proposed=%d/%d stop=%d/%d remove=%d/%d",
				fixture.routes.restartInstalls, beforeRestartInstalls,
				generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands), beforeProposedReloads,
				fixture.predecessor.count(candidateOwnerStop), beforeStop+1,
				fixture.predecessor.count(candidateOwnerRemove), beforeRemove+1)
		}
		if !reflect.DeepEqual(retirementWaits, []time.Duration{maximumDrain}) {
			t.Fatalf("post-activation Pending retirement waits=%v want=[%s]", retirementWaits, maximumDrain)
		}
	})
}

func TestGeneratedDomainRecoveredCompletionClaimGateDoesNotFinalizePending(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, generatedRouteColdAccountingFixture, deploymentexecution.Claim) deploymentexecution.Claim
	}{
		{name: "stale attempt", mutate: func(t *testing.T, fixture generatedRouteColdAccountingFixture,
			claim deploymentexecution.Claim,
		) deploymentexecution.Claim {
			if _, err := fixture.identity.database.Exec(`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`,
				time.Now().UTC().Format(time.RFC3339Nano), claim.JobID()); err != nil {
				t.Fatal(err)
			}
			return claim
		}},
		{name: "durable cancellation", mutate: func(t *testing.T, fixture generatedRouteColdAccountingFixture,
			claim deploymentexecution.Claim,
		) deploymentexecution.Claim {
			if _, err := fixture.identity.database.Exec(`UPDATE jobs SET phase='cancelling',updated_at=? WHERE id=?`,
				time.Now().UTC().Format(time.RFC3339Nano), claim.JobID()); err != nil {
				t.Fatal(err)
			}
			return claim
		}},
		{name: "crossed job and binding", mutate: func(t *testing.T, fixture generatedRouteColdAccountingFixture,
			_ deploymentexecution.Claim,
		) deploymentexecution.Claim {
			var jobID string
			var attempt int
			if err := fixture.identity.database.QueryRow(`SELECT job_id FROM deployments WHERE id=?`,
				fixture.lifecycleDeploymentID).Scan(&jobID); err != nil {
				t.Fatal(err)
			}
			binding, err := fixture.migration.bindings.Read(context.Background(), jobID)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`, jobID).Scan(&attempt); err != nil {
				t.Fatal(err)
			}
			crossed, err := deploymentexecution.NewClaim(binding, jobID, fixture.migration.guarded.appID, attempt)
			if err != nil {
				t.Fatal(err)
			}
			return crossed
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, ctx, claim, transition, activation :=
					generatedRouteColdAccountingPendingCompletionClaimFixture(t)
				before := generatedRouteColdAccountingPendingCompletionBoundary(t, fixture)
				claim = test.mutate(t, fixture, claim)
				err := result.routes.finishRecoveredPendingRouteActivation(ctx, claim,
					fixture.migration.request.DeploymentID, transition, activation)
				if !errors.Is(err, deploymentexecution.ErrAuthorityStale) &&
					!errors.Is(err, deploymentexecution.ErrIdentityLost) {
					t.Fatalf("recovered Pending first claim gate error=%v", err)
				}
				generatedRouteColdAccountingAssertPendingCompletionBoundary(t, result, fixture, before, true)
			})
		})
	}

	for _, test := range []struct {
		name          string
		mutate        bool
		cancelCaller  bool
		beforePersist bool
		wantCompleted bool
	}{
		{name: "post-persist lost reply reattests current claim", wantCompleted: true},
		{name: "post-persist retry refuses crossed durable attempt", mutate: true},
		{name: "pre-persist finish failure reattests durable claim before retry", mutate: true,
			beforePersist: true},
		{name: "caller cancellation reconciles Pending before later fresh worker", cancelCaller: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, ctx, claim, _, _ := generatedRouteColdAccountingPendingCompletionClaimFixture(t)
				before := generatedRouteColdAccountingPendingCompletionBoundary(t, fixture)
				var mutationErr error
				wrapper := &generatedRouteColdAccountingFinishLostReplyRunner{
					store: result.manager.store, beforePersist: test.beforePersist,
				}
				if test.mutate {
					wrapper.mutate = func() {
						_, mutationErr = fixture.identity.database.Exec(
							`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`,
							time.Now().UTC().Format(time.RFC3339Nano), claim.JobID())
					}
				}
				callCtx := ctx
				if test.cancelCaller {
					var cancel context.CancelFunc
					callCtx, cancel = context.WithCancel(ctx)
					wrapper.mutate = cancel
				}
				fixture.process.setRouteReadHook(wrapper.intercept)
				result.routes.waitRecoveredRetirement = func(callCtx context.Context, delay time.Duration) error {
					if callCtx == nil || delay != maximumDrain {
						return deploymentexecution.ErrInvalidConfiguration
					}
					return nil
				}
				err := result.routes.CompleteRecovered(callCtx, claim, fixture.migration.request.DeploymentID)
				if mutationErr != nil {
					t.Fatalf("mutate durable claim between finish attempts: %v", mutationErr)
				}
				if !wrapper.fired {
					state, stateErr := result.manager.store.load()
					t.Fatalf("recovered Pending finish did not reach the post-persist lost-reply boundary: error=%v commands=%d state=%#v stateError=%v",
						err, wrapper.commands, state, stateErr)
				}
				if test.wantCompleted {
					if err != nil {
						t.Fatalf("recovered Pending lost-reply retry: %v", err)
					}
					assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
					return
				}
				if test.cancelCaller {
					if callCtx.Err() != context.Canceled || !IsCode(err, DiagnosticCancelled) {
						t.Fatalf("recovered Pending caller cancellation error=%v", err)
					}
					generatedRouteColdAccountingAssertPendingCompletionBoundary(t, result, fixture, before, false)
					fixture.process.setRouteReadHook(nil)
					var beforeAttempt int
					if readErr := fixture.identity.database.QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
						claim.JobID()).Scan(&beforeAttempt); readErr != nil {
						t.Fatal(readErr)
					}
					recovered, recoverErr := generatedrecovery.RecoverRuntime(context.Background(),
						fixture.identity.database, time.Now().UTC(), fixture.migration.bindings)
					if recoverErr != nil || recovered.Jobs.RequeuedGenerated != 1 {
						t.Fatalf("requeue caller-cancelled recovered worker result=%+v error=%v", recovered, recoverErr)
					}
					now := time.Now().UTC().Format(time.RFC3339Nano)
					changed, claimErr := fixture.identity.database.Exec(`UPDATE jobs SET status='running',phase='validate',
						progress_percent=1,checkpoint_json='{"phase":"validate"}',attempt=attempt+1,
						started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status='queued'`,
						now, now, claim.JobID())
					if claimErr != nil {
						t.Fatal(claimErr)
					}
					if rows, rowsErr := changed.RowsAffected(); rowsErr != nil || rows != 1 {
						t.Fatalf("claim later recovered worker changed=%d error=%v", rows, rowsErr)
					}
					binding, claimErr := fixture.migration.bindings.Read(ctx, claim.JobID())
					if claimErr != nil {
						t.Fatal(claimErr)
					}
					fresh, claimErr := deploymentexecution.NewClaim(binding, claim.JobID(), claim.ApplicationID(),
						beforeAttempt+1)
					if claimErr != nil {
						t.Fatal(claimErr)
					}
					if completionErr := result.routes.CompleteRecovered(ctx, fresh,
						fixture.migration.request.DeploymentID); completionErr != nil {
						t.Fatalf("later fresh recovered worker: %v", completionErr)
					}
					assertGeneratedRouteColdAccountingRecoveredHandoffSnapshot(t, fixture)
					return
				}
				if !errors.Is(err, deploymentexecution.ErrAuthorityStale) &&
					!errors.Is(err, deploymentexecution.ErrIdentityLost) {
					t.Fatalf("recovered Pending crossed retry error=%v", err)
				}
				generatedRouteColdAccountingAssertPendingCompletionBoundary(t, result, fixture, before,
					test.beforePersist)
			})
		})
	}
}

func TestGeneratedRouteActivationFinishClaimGateHelperDoesNotFinalizePending(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture, result, ctx, claim, transition, observed :=
			generatedRouteColdAccountingPendingCompletionClaimFixture(t)
		activation, err := result.routes.readRecoveredRouteActivation(ctx, observed)
		if err != nil {
			t.Fatal(err)
		}
		before := generatedRouteColdAccountingPendingCompletionBoundary(t, fixture)
		if _, err := fixture.identity.database.Exec(`UPDATE jobs SET phase='cancelling',updated_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339Nano), claim.JobID()); err != nil {
			t.Fatal(err)
		}
		err = result.routes.finishPendingRouteActivation(ctx, claim, fixture.migration.request.DeploymentID,
			transition, activation)
		if !errors.Is(err, deploymentexecution.ErrAuthorityStale) &&
			!errors.Is(err, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("shared activation finish gate error=%v", err)
		}
		generatedRouteColdAccountingAssertPendingCompletionBoundary(t, result, fixture, before, true)
	})
}

func TestGeneratedRouteActivationFreshFinishClaimGateDoesNotFinalizePending(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name             string
		positive         bool
		mutateBeforeGate bool
	}{
		{name: "unmutated same fixture reaches finish and exact 042", positive: true},
		{name: "durable cancellation before first finish gate", mutateBeforeGate: true},
		{name: "crossed attempt after failed first finish before retry"},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fresh := generatedRouteColdAccountingFreshFinishFixture(t)
				before := captureGeneratedRouteColdAccountingFreshFinishBoundary(t, fresh)
				var mutationErr error
				var mutationRows int64
				gateCtx := &generatedRouteColdAccountingCommittedActivationContext{
					Context: fresh.migration.guarded.context, database: fresh.observerDatabase,
					receiptID: fresh.sealed.receiptID, appID: fresh.migration.guarded.appID,
					deploymentID: fresh.migration.request.DeploymentID,
					jobID:        fresh.migration.claim.JobID(), attempt: fresh.migration.claim.Attempt(),
				}
				if test.mutateBeforeGate {
					gateCtx.mutate = func() error {
						result, err := fresh.observerDatabase.ExecContext(fresh.migration.guarded.context,
							`UPDATE jobs SET phase='cancelling',updated_at=? WHERE id=? AND attempt=?`,
							time.Now().UTC().Format(time.RFC3339Nano), fresh.migration.claim.JobID(),
							fresh.migration.claim.Attempt())
						if err != nil {
							return err
						}
						mutationRows, err = result.RowsAffected()
						return err
					}
				}
				if test.positive {
					_, retirement, err := fresh.sealed.ActivateWithPredecessorRetirement(gateCtx)
					if err != nil || retirement == nil || !gateCtx.ready() {
						t.Fatalf("unmutated fresh finish control retirement=%#v ready=%t error=%v contextError=%v",
							retirement, gateCtx.ready(), err, gateCtx.mutationError())
					}
					defer retirement.Leave()
					if err := retirement.current(); err != nil {
						t.Fatalf("unmutated fresh finish retirement is not current: %v", err)
					}
					state, stateErr := fresh.result.manager.store.load()
					receipt := generatedRouteColdAccountingReadActivation(t, fresh.result.routes.runtime,
						fresh.fixture.identity.database, fresh.sealed.receiptID)
					head, headErr := fresh.result.routes.runtime.Active(context.Background(),
						fresh.migration.guarded.appID)
					if stateErr != nil || state.Pending != nil || receipt.ReceiptID != fresh.sealed.receiptID ||
						headErr != nil || head.ActivationReceiptID != fresh.sealed.receiptID ||
						head.DeploymentID != fresh.migration.request.DeploymentID {
						t.Fatalf("unmutated fresh finish boundary state=%#v stateError=%v receipt=%#v head=%#v headError=%v",
							state, stateErr, receipt, head, headErr)
					}
					return
				}
				wrapper := &generatedRouteColdAccountingFinishLostReplyRunner{
					store: fresh.result.manager.store, beforePersist: true,
					ready: gateCtx.ready,
				}
				if !test.mutateBeforeGate {
					wrapper.mutate = func() {
						result, err := fresh.fixture.identity.database.Exec(
							`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=? AND attempt=?`,
							time.Now().UTC().Format(time.RFC3339Nano), fresh.migration.claim.JobID(),
							fresh.migration.claim.Attempt())
						if err != nil {
							mutationErr = err
							return
						}
						mutationRows, mutationErr = result.RowsAffected()
					}
				}
				fresh.fixture.process.setRouteReadHook(wrapper.intercept)
				_, retirement, err := fresh.sealed.ActivateWithPredecessorRetirement(gateCtx)
				if retirement != nil {
					retirement.Leave()
					t.Fatal("fresh claim refusal minted predecessor retirement authority")
				}
				if mutationErr != nil || gateCtx.mutationError() != nil || mutationRows != 1 {
					t.Fatalf("fresh finish claim mutation rows=%d error=%v contextError=%v activateError=%v ready=%t wrapperFired=%t",
						mutationRows, mutationErr, gateCtx.mutationError(), err, gateCtx.ready(), wrapper.fired)
				}
				if !gateCtx.ready() {
					t.Fatal("fresh finish claim seam never observed the exact committed 042")
				}
				if test.mutateBeforeGate && wrapper.fired {
					t.Fatal("fresh first finish gate reached Manager after durable cancellation")
				}
				if test.mutateBeforeGate && gateCtx.Context.Err() != nil {
					t.Fatalf("fresh first finish gate refusal used a cancelled caller context: %v",
						gateCtx.Context.Err())
				}
				if !test.mutateBeforeGate && !wrapper.fired {
					t.Fatal("fresh retry control did not reach the first Manager finish attestation")
				}
				if !errors.Is(err, deploymentexecution.ErrAuthorityStale) &&
					!errors.Is(err, deploymentexecution.ErrIdentityLost) {
					t.Fatalf("fresh finish claim refusal error=%v", err)
				}
				generatedRouteColdAccountingAssertFreshFinishBoundary(t, fresh, before)
			})
		})
	}
}

type generatedRouteColdAccountingFreshFinishCase struct {
	fixture          generatedRouteColdAccountingFixture
	result           *generatedDomainConstruction
	sealed           *GeneratedRouteExecution
	migration        *generatedMigrationFixture
	candidate        CandidateIdentity
	observerDatabase *sql.DB

	predecessorDeploymentID string
	predecessorReceiptID    string
	predecessorContainerID  string
	historicalDeploymentID  string
	historicalReceiptID     string
}

func generatedRouteColdAccountingFreshFinishFixture(t *testing.T) generatedRouteColdAccountingFreshFinishCase {
	t.Helper()
	fixture := newGeneratedRouteColdAccountingFixture(t)
	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.process, fixture.environment, fixture.identity.options)
	if err != nil || result == nil || result.candidates == nil || result.routes == nil {
		t.Fatalf("fresh activation claim fixture construction=%#v error=%v", result, err)
	}
	assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
	currentPhysical, found := fixture.process.predecessor.physical[fixture.currentContainerID]
	if !found || currentPhysical.health != "unhealthy" {
		t.Fatalf("fresh activation claim fixture current physical=%#v found=%t", currentPhysical, found)
	}
	healthyPhysical := currentPhysical
	healthyPhysical.health = "healthy"
	comparison := healthyPhysical
	comparison.health = currentPhysical.health
	if !reflect.DeepEqual(comparison, currentPhysical) {
		t.Fatal("fresh activation claim fixture health recovery changed authenticated physical identity")
	}
	fixture.process.predecessor.physical[fixture.currentContainerID] = healthyPhysical
	fixture.process.setDelegate(fixture.routes)
	firstHead, err := generatedruntimestate.New(fixture.identity.database).Active(
		context.Background(), fixture.migration.guarded.appID)
	if err != nil || firstHead.DeploymentID != fixture.migration.request.DeploymentID {
		t.Fatalf("fresh activation claim predecessor head=%#v error=%v", firstHead, err)
	}
	predecessorDeploymentID := fixture.migration.request.DeploymentID
	predecessorReceiptID := fixture.componentReceipt
	predecessorContainerID := fixture.currentContainerID

	migration := newGeneratedRouteColdAccountingSuccessorMigration(t, fixture.migration,
		fixture.jobs, fixture.identity.options.WorkingDirectory)
	validation := newGeneratedRouteSuccessorValidation(t, migration)
	base := &candidateOwnerRunner{containerID: strings.Repeat("9", 64),
		absenceErr: generatedMigrationCompletedExitOne(t),
		before:     make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results: make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (
			runtimeprocess.CommandResult, error))}
	fixture.process.setCandidate(base)
	validation.authority = result.candidates
	owner := &candidateOwnerFixture{validation: validation, authority: result.candidates, runner: base,
		environment: &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")},
		budget:      result.budget, options: generatedDomainCandidateOptions(fixture.identity.options)}
	execution, candidate := prepareGeneratedRouteSequentialCandidate(t, owner)
	t.Cleanup(execution.Leave)
	observed, err := result.manager.Observe(context.Background(), fixture.migration.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	request := generatedRouteRequest(fixture.migration.guarded.appID, candidate)
	request.FromSlot = observed.Slot
	sealed, err := result.routes.Pin(migration.guarded.context, execution, request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.routes.network = candidate.NetworkName
	fixture.routes.endpoint = generatedRouteEndpoint(candidate)
	fixture.routes.slot = generatedidentity.Slot(candidate.Slot)
	fixture.routes.endpoints[candidate.ContainerID] = generatedRouteEndpointSnapshot{
		appID: fixture.migration.guarded.appID, endpoint: generatedRouteEndpoint(candidate),
		slot: generatedidentity.Slot(candidate.Slot),
	}
	observerDatabase, err := sql.Open("sqlite", filepath.Join(fixture.identity.dataRoot, "control.db"))
	if err != nil {
		t.Fatalf("open fresh finish auxiliary observer database: %v", err)
	}
	observerDatabase.SetMaxOpenConns(1)
	if _, err := observerDatabase.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = observerDatabase.Close()
		t.Fatalf("configure fresh finish auxiliary observer database: %v", err)
	}
	t.Cleanup(func() { _ = observerDatabase.Close() })
	return generatedRouteColdAccountingFreshFinishCase{fixture: fixture, result: result, sealed: sealed,
		migration: migration, candidate: candidate, observerDatabase: observerDatabase,
		predecessorDeploymentID: predecessorDeploymentID,
		predecessorReceiptID:    predecessorReceiptID, predecessorContainerID: predecessorContainerID,
		historicalDeploymentID: fixture.lifecycleDeploymentID, historicalReceiptID: fixture.predecessorReceipt}
}

type generatedRouteColdAccountingFreshFinishBoundary struct {
	restartInstalls int
	proposedReloads int
	stop, remove    int
	capacityProbes  int
	liveConfig      []byte
	restartConfig   []byte
	capacity        generatedRouteColdAccountingFreshCapacitySnapshot
}

type generatedRouteColdAccountingFreshCapacitySnapshot struct {
	phase                 generatedDomainCapacityPhase
	borrows               int
	memory, disk          uint64
	debtTokens            int
	predecessorDebtExact  bool
	successorRawExact     bool
	predecessorRegistered bool
	ledgerGroups          int
	historical            generatedRouteColdAccountingFreshTombstoneSnapshot
	predecessor           generatedRouteColdAccountingFreshChargeSnapshot
	successor             generatedRouteColdAccountingFreshChargeSnapshot
}

type generatedRouteColdAccountingFreshTombstoneSnapshot struct {
	group       *candidateCapacityGroup
	component   *candidateCapacityComponent
	receipt     *candidateReceiptTombstone
	receiptWord uint64
	tokenHeld   bool
	tokenGen    uint64
}

type generatedRouteColdAccountingFreshChargeSnapshot struct {
	group       *candidateCapacityGroup
	component   *candidateCapacityComponent
	charge      *candidateCapacityCharge
	receipt     *candidateReceiptTombstone
	token       candidateCapacityChargeToken
	lease       *generatedcapacity.Lease
	limits      generatedcapacity.Limits
	epoch       uint64
	refs        int
	state       candidateChargeState
	owner       candidateAccountingOwner
	publication candidateAccountingOwner
	receiptWord uint64
	tokenHeld   bool
	tokenGen    uint64
	retained    bool
	explicit    bool
	retired     bool
}

func captureGeneratedRouteColdAccountingFreshFinishBoundary(t *testing.T,
	fresh generatedRouteColdAccountingFreshFinishCase,
) generatedRouteColdAccountingFreshFinishBoundary {
	t.Helper()
	fixture := fresh.fixture
	liveConfig := append([]byte(nil), fixture.routes.liveConfig...)
	restartConfig := append([]byte(nil),
		fixture.routes.files[caddyContainerName+":/config/active.json"]...)
	if len(liveConfig) == 0 || len(restartConfig) == 0 || !bytes.Equal(liveConfig, restartConfig) {
		t.Fatal("fresh finish boundary lacks an exact pre-042 live/restart baseline")
	}
	return generatedRouteColdAccountingFreshFinishBoundary{
		restartInstalls: fixture.routes.restartInstalls,
		proposedReloads: generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands),
		stop:            fixture.predecessor.count(candidateOwnerStop),
		remove:          fixture.predecessor.count(candidateOwnerRemove),
		capacityProbes:  generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()),
		liveConfig:      liveConfig, restartConfig: restartConfig,
		capacity: generatedRouteColdAccountingFreshCapacity(t, fresh),
	}
}

func generatedRouteColdAccountingFreshCapacity(t *testing.T,
	fresh generatedRouteColdAccountingFreshFinishCase,
) generatedRouteColdAccountingFreshCapacitySnapshot {
	t.Helper()
	result := fresh.result
	ledger := result.candidates.ledger
	ledger.mu.Lock()
	historical := ledger.groups[candidateCapacityGroupKey{appID: fresh.migration.guarded.appID,
		deploymentID: fresh.historicalDeploymentID}]
	predecessor := ledger.groups[candidateCapacityGroupKey{appID: fresh.migration.guarded.appID,
		deploymentID: fresh.predecessorDeploymentID}]
	successor := ledger.groups[candidateCapacityGroupKey{appID: fresh.migration.guarded.appID,
		deploymentID: fresh.migration.request.DeploymentID}]
	groupCount := len(ledger.groups)
	ledger.mu.Unlock()
	read := func(group *candidateCapacityGroup, receiptID string) generatedRouteColdAccountingFreshChargeSnapshot {
		t.Helper()
		if group == nil {
			t.Fatal("fresh finish boundary omitted one exact capacity group")
		}
		group.mu.Lock()
		defer group.mu.Unlock()
		component := group.byName[fresh.fixture.componentName]
		if len(group.components) != 1 || group.components[0] != fresh.fixture.componentName || component == nil ||
			component.receipt == nil || component.receipt.id != receiptID || component.charge == nil {
			t.Fatalf("fresh finish boundary capacity group=%#v component=%#v receipt=%s", group, component, receiptID)
		}
		charge := component.charge
		return generatedRouteColdAccountingFreshChargeSnapshot{group: group, component: component, charge: charge,
			receipt: component.receipt, token: charge.token, lease: charge.lease, limits: group.limits,
			epoch: charge.epoch, refs: charge.refs, state: charge.state, owner: charge.owner,
			publication: charge.publicationOwner, receiptWord: component.receipt.state.Load(),
			tokenHeld: component.tokenHeld, tokenGen: component.tokenGeneration,
			retained: charge.receiptRetained, explicit: charge.explicitRetained,
			retired: charge.logicallyRetired}
	}
	readTombstone := func(group *candidateCapacityGroup, receiptID string,
	) generatedRouteColdAccountingFreshTombstoneSnapshot {
		t.Helper()
		if group == nil || receiptID == "" {
			t.Fatalf("fresh finish boundary omitted historical receipt tombstone group=%#v receipt=%q",
				group, receiptID)
		}
		group.mu.Lock()
		defer group.mu.Unlock()
		component := group.byName[fresh.fixture.componentName]
		if len(group.components) != 1 || len(group.byName) != 1 ||
			group.components[0] != fresh.fixture.componentName || component == nil ||
			component.receipt == nil || component.receipt.id != receiptID ||
			component.charge != nil || component.tokenHeld {
			t.Fatalf("fresh finish boundary historical tombstone group=%#v component=%#v receipt=%q",
				group, component, receiptID)
		}
		return generatedRouteColdAccountingFreshTombstoneSnapshot{group: group, component: component,
			receipt: component.receipt, receiptWord: component.receipt.state.Load(),
			tokenHeld: component.tokenHeld, tokenGen: component.tokenGeneration}
	}
	value := generatedRouteColdAccountingFreshCapacitySnapshot{ledgerGroups: groupCount,
		historical:  readTombstone(historical, fresh.historicalReceiptID),
		predecessor: read(predecessor, fresh.predecessorReceiptID),
		successor:   read(successor, fresh.candidate.ReceiptID)}
	predecessorToken, predecessorDebt := value.predecessor.token.(*generatedDomainCapacityDebtToken)
	successorToken, successorRaw := value.successor.token.(*candidateRawCapacityChargeToken)
	result.capacity.mu.Lock()
	value.phase, value.borrows = result.capacity.phase, result.capacity.borrows
	value.memory, value.disk = result.capacity.memory, result.capacity.disk
	value.debtTokens = len(result.capacity.tokens)
	_, value.predecessorRegistered = result.capacity.tokens[predecessorToken]
	value.predecessorDebtExact = predecessorDebt && predecessorToken != nil &&
		predecessorToken.owner == result.capacity && predecessorToken.active &&
		predecessorToken.limits == value.predecessor.limits && value.predecessor.lease == nil
	result.capacity.mu.Unlock()
	value.successorRawExact = successorRaw && successorToken != nil && successorToken.lease != nil &&
		successorToken.lease == value.successor.lease
	if value.phase != generatedDomainCapacityPublished || value.borrows != 0 || value.debtTokens != 1 ||
		value.memory != value.predecessor.limits.MemoryBytes || value.disk != value.predecessor.limits.DiskBytes ||
		!value.predecessorDebtExact || !value.predecessorRegistered || !value.successorRawExact ||
		value.ledgerGroups != 3 || value.historical.group == nil || value.historical.tokenHeld ||
		value.predecessor.token == nil || value.successor.token == nil ||
		!value.predecessor.token.configured() || !value.predecessor.token.published() ||
		!value.successor.token.configured() || !value.successor.token.published() {
		t.Fatalf("fresh finish boundary did not hold the historical tombstone and two exact capacity charges: %#v", value)
	}
	return value
}

func generatedRouteColdAccountingAssertFreshFinishBoundary(t *testing.T,
	fresh generatedRouteColdAccountingFreshFinishCase, before generatedRouteColdAccountingFreshFinishBoundary,
) {
	t.Helper()
	receipt := generatedRouteColdAccountingReadActivation(t, fresh.result.routes.runtime,
		fresh.fixture.identity.database, fresh.sealed.receiptID)
	fresh.fixture.migration = fresh.migration
	fresh.fixture.firstReceipt = receipt
	fresh.fixture.componentReceipt = fresh.candidate.ReceiptID
	fresh.fixture.lifecycleDeploymentID = fresh.predecessorDeploymentID
	fresh.fixture.predecessorReceipt = fresh.predecessorReceiptID
	fresh.fixture.restoresPredecessor = true
	fresh.fixture.predecessorPhysical = candidatePhysicalHandedToRoute
	fresh.fixture.retiredContainerID = fresh.predecessorContainerID
	fresh.fixture.currentContainerID = fresh.candidate.ContainerID
	after := generatedRouteColdAccountingPendingCompletionBoundary(t, fresh.fixture)
	proposed := routeRecord{Slot: fresh.sealed.request.ToSlot,
		Endpoints: append([]generatedroute.RouteEndpoint(nil), fresh.sealed.request.Endpoints...)}
	protectedPrevious, activeFound := after.state.Active[fresh.sealed.request.AppID]
	if after.state.Pending == nil || after.state.Pending.AppID != fresh.sealed.request.AppID ||
		after.state.Pending.Previous == nil || !sameRoute(*after.state.Pending.Previous, fresh.sealed.previous) ||
		len(after.state.Active) != 1 || !activeFound || !sameRoute(protectedPrevious, fresh.sealed.previous) ||
		!sameRoute(after.state.Pending.Proposed, proposed) || after.activation.receiptID != fresh.sealed.receiptID ||
		after.prefix != ([4]int{}) {
		t.Fatalf("fresh finish refusal Pending/042 boundary state=%#v activation=%#v prefix=%v",
			after.state, after.activation, after.prefix)
	}
	if after.restartInstalls != before.restartInstalls+2 || after.proposedReloads != before.proposedReloads+1 ||
		after.stop != before.stop || after.remove != before.remove || after.capacityProbes != before.capacityProbes {
		t.Fatalf("fresh finish refusal effects before=%#v after=%#v", before, after)
	}
	if bytes.Equal(after.liveConfig, before.liveConfig) || bytes.Equal(after.restartConfig, before.restartConfig) ||
		!bytes.Equal(after.liveConfig, after.restartConfig) {
		t.Fatal("fresh finish refusal did not retain the exact Proposed live/restart bytes")
	}
	if capacity := generatedRouteColdAccountingFreshCapacity(t, fresh); !reflect.DeepEqual(capacity, before.capacity) {
		t.Fatalf("fresh finish refusal changed exact dual capacity holds before=%#v after=%#v",
			before.capacity, capacity)
	}
}

func generatedRouteColdAccountingReadActivation(t *testing.T, runtime *generatedruntimestate.Repository,
	database *sql.DB, receiptID string,
) generatedruntimestate.RouteActivationReceipt {
	t.Helper()
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	receipt, err := runtime.ReadRouteActivationReceiptTx(context.Background(), tx, receiptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return receipt
}

// generatedRouteColdAccountingCommittedActivationContext observes the exact
// committed 042 through a distinct test-only database handle. Context methods
// may be evaluated while database/sql holds the production DB mutex, so this
// observer must never reenter the provenance-bearing DB object.
type generatedRouteColdAccountingCommittedActivationContext struct {
	context.Context
	database                       *sql.DB
	receiptID, appID, deploymentID string
	jobID                          string
	attempt                        int
	mutate                         func() error
	mu                             sync.Mutex
	checking, committed            bool
	err                            error
}

func (c *generatedRouteColdAccountingCommittedActivationContext) observe() {
	if c == nil || c.Context == nil || c.database == nil {
		return
	}
	c.mu.Lock()
	if c.committed || c.checking {
		c.mu.Unlock()
		return
	}
	c.checking = true
	c.mu.Unlock()
	var count int
	err := c.database.QueryRowContext(c.Context, `SELECT COUNT(*) FROM generated_route_activations
		WHERE id=? AND app_id=? AND successor_deployment_id=? AND writer_job_id=? AND writer_worker_attempt=?`,
		c.receiptID, c.appID, c.deploymentID, c.jobID, c.attempt).Scan(&count)
	committed := err == nil && count == 1
	if committed && c.mutate != nil {
		err = c.mutate()
	}
	c.mu.Lock()
	c.checking = false
	if committed {
		c.committed = true
		c.err = err
	} else if err != nil {
		c.err = err
	}
	c.mu.Unlock()
}

func (c *generatedRouteColdAccountingCommittedActivationContext) Deadline() (time.Time, bool) {
	c.observe()
	return c.Context.Deadline()
}

func (c *generatedRouteColdAccountingCommittedActivationContext) Done() <-chan struct{} {
	c.observe()
	return c.Context.Done()
}

func (c *generatedRouteColdAccountingCommittedActivationContext) Err() error {
	c.observe()
	return c.Context.Err()
}

func (c *generatedRouteColdAccountingCommittedActivationContext) Value(key any) any {
	c.observe()
	return c.Context.Value(key)
}

func (c *generatedRouteColdAccountingCommittedActivationContext) ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.committed && c.err == nil
}

func (c *generatedRouteColdAccountingCommittedActivationContext) mutationError() error {
	if c == nil {
		return errors.New("missing committed activation context")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

type generatedRouteColdAccountingPendingCompletionSnapshot struct {
	state           routeState
	activation      generatedRouteColdAccountingActivationRecord
	prefix          [4]int
	commands        int
	restartInstalls int
	proposedReloads int
	stop, remove    int
	capacityProbes  int
	liveConfig      []byte
	restartConfig   []byte
}

func generatedRouteColdAccountingPendingCompletionClaimFixture(t *testing.T) (
	generatedRouteColdAccountingFixture, *generatedDomainConstruction, context.Context,
	deploymentexecution.Claim, *generatedRouteActivationGatewayTransition,
	generatedruntimestate.GeneratedDomainStartupRouteActivation,
) {
	t.Helper()
	fixture := newGeneratedRouteColdAccountingPostActivationPendingFixture(t)
	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.process, fixture.environment, fixture.identity.options)
	if err != nil || result == nil || result.routes == nil || result.cold == nil ||
		result.cold.pendingActivation == nil {
		t.Fatalf("recovered Pending claim fixture result=%#v error=%v", result, err)
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
		t.Fatalf("requeue recovered Pending claim fixture result=%+v error=%v", recovered, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changed, err := fixture.identity.database.Exec(`UPDATE jobs SET status='running',phase='validate',
		progress_percent=1,checkpoint_json='{"phase":"validate"}',attempt=attempt+1,
		started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status='queued'`,
		now, now, fixture.migration.claim.JobID())
	if err != nil {
		t.Fatal(err)
	}
	if rows, rowsErr := changed.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("claim recovered Pending job changed=%d error=%v", rows, rowsErr)
	}
	binding, err := fixture.migration.bindings.Read(context.Background(), fixture.migration.claim.JobID())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, fixture.migration.claim.JobID(),
		fixture.migration.guarded.appID, beforeAttempt+1)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(),
		fixture.identity.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release recovered Pending claim scope: %v", err)
		}
	})
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	observed, err := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
		fixture.process, fixture.identity.options)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := readGeneratedDomainColdGroups(ctx, result.candidates, observed)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := recoveredRouteCompletionEvidence(observed, groups, claim,
		fixture.migration.request.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, result, ctx, claim,
		cloneGeneratedRouteActivationGatewayTransition(result.cold.pendingActivation), evidence.activation
}

func generatedRouteColdAccountingPendingCompletionBoundary(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) generatedRouteColdAccountingPendingCompletionSnapshot {
	t.Helper()
	store, err := newStateStore(fixture.identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := generatedRouteColdAccountingRecoveredPrefixCounts(context.Background(),
		fixture.identity.database, fixture.firstReceipt.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	return generatedRouteColdAccountingPendingCompletionSnapshot{
		state: state, activation: generatedRouteColdAccountingPostActivationReceipt(t, fixture), prefix: prefix,
		commands: len(fixture.routes.commands), restartInstalls: fixture.routes.restartInstalls,
		proposedReloads: generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands),
		stop:            fixture.predecessor.count(candidateOwnerStop), remove: fixture.predecessor.count(candidateOwnerRemove),
		capacityProbes: generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()),
		liveConfig:     append([]byte(nil), fixture.routes.liveConfig...),
		restartConfig: append([]byte(nil),
			fixture.routes.files[caddyContainerName+":/config/active.json"]...),
	}
}

func generatedRouteColdAccountingAssertPendingCompletionBoundary(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
	before generatedRouteColdAccountingPendingCompletionSnapshot, pending bool,
) {
	t.Helper()
	after := generatedRouteColdAccountingPendingCompletionBoundary(t, fixture)
	if pending {
		if !reflect.DeepEqual(after.state, before.state) || after.state.Pending == nil {
			t.Fatalf("recovered claim refusal changed Pending before=%#v after=%#v", before.state, after.state)
		}
	} else if after.state.Pending != nil {
		t.Fatalf("post-persist retry restored Pending=%#v", after.state.Pending)
	}
	if after.activation != before.activation || after.prefix != ([4]int{}) || before.prefix != ([4]int{}) ||
		after.restartInstalls != before.restartInstalls ||
		after.proposedReloads != before.proposedReloads || after.stop != before.stop ||
		after.remove != before.remove || after.capacityProbes != before.capacityProbes {
		t.Fatalf("recovered claim refusal mutated boundary before=%#v after=%#v", before, after)
	}
	if !bytes.Equal(after.liveConfig, before.liveConfig) ||
		!bytes.Equal(after.restartConfig, before.restartConfig) ||
		!bytes.Equal(after.liveConfig, after.restartConfig) {
		t.Fatal("recovered claim refusal changed live or restart route bytes")
	}
	assertGeneratedRouteColdAccountingRestoredCurrent(t, result, fixture)
}

type generatedRouteColdAccountingActivationRecord struct {
	receiptID                     string
	creatorJobID, writerJobID     string
	creatorAttempt, writerAttempt int
	head                          generatedruntimestate.ActiveHead
}

// generatedRouteColdAccountingPostActivationReceipt proves the pre-finish
// boundary has exactly one durable 042 for the successor and that recovery
// preserves its immutable creator/writer provenance rather than synthesizing
// a receipt for the fresh recovery worker.
func generatedRouteColdAccountingPostActivationReceipt(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) generatedRouteColdAccountingActivationRecord {
	t.Helper()
	var count int
	if err := fixture.identity.database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
		WHERE app_id=? AND successor_deployment_id=?`, fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	var record generatedRouteColdAccountingActivationRecord
	if count == 1 {
		if err := fixture.identity.database.QueryRow(`SELECT id,creator_job_id,creator_worker_attempt,
			writer_job_id,writer_worker_attempt FROM generated_route_activations
			WHERE app_id=? AND successor_deployment_id=?`, fixture.migration.guarded.appID,
			fixture.migration.request.DeploymentID).Scan(&record.receiptID, &record.creatorJobID,
			&record.creatorAttempt, &record.writerJobID, &record.writerAttempt); err != nil {
			t.Fatal(err)
		}
	}
	head, err := generatedruntimestate.New(fixture.identity.database).Active(context.Background(),
		fixture.migration.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	record.head = head
	if count != 1 || record.receiptID != fixture.firstReceipt.ReceiptID ||
		record.creatorJobID != fixture.firstReceipt.CreatorJobID ||
		record.creatorAttempt != fixture.firstReceipt.CreatorWorkerAttempt ||
		record.writerJobID != fixture.firstReceipt.WriterJobID ||
		record.writerAttempt != fixture.firstReceipt.WriterWorkerAttempt ||
		head.DeploymentID != fixture.migration.request.DeploymentID ||
		head.ReleaseID != fixture.migration.request.ReleaseID || head.Slot != fixture.firstReceipt.SuccessorSlot ||
		head.ActivationReceiptID != fixture.firstReceipt.ReceiptID {
		t.Fatalf("post-activation durable 042 count=%d record=%#v receipt=%#v", count, record, fixture.firstReceipt)
	}
	return record
}

func generatedRouteColdAccountingProposedReloadCount(commands [][]string) int {
	count := 0
	for _, command := range commands {
		if reflect.DeepEqual(command, []string{
			"container", "exec", caddyContainerName, "caddy", "reload", "--config", "/config/proposed.json",
		}) {
			count++
		}
	}
	return count
}

// The authentic fixture performs exactly its initial publication and the one
// replacement publication before the interruption. Recovery must leave this
// count unchanged, proving it did not replay the already-proposed route.
const generatedRouteColdAccountingSwitchingRoutePhysicalProposedReloads = 2
const generatedRouteColdAccountingInitialSwitchingRoutePhysicalProposedReloads = 1

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
				var changedDiagnostic *generatedDomainConstructionDiagnostic
				if !errors.Is(changedRouteErr, errGeneratedDomainConstructionRefused) ||
					!errors.As(changedRouteErr, &changedDiagnostic) || changedDiagnostic == nil ||
					changedDiagnostic.stage != generatedDomainConstructionStageStartupObservation ||
					changedDiagnostic.reason != generatedDomainConstructionReasonDockerLiveConfig ||
					changedRouteErr.Error() != "generated domain construction was refused: stage=startup_observation reason=docker_live_config" {
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

func assertGeneratedRouteColdAccountingSwitchingRouteSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	if fixture.switchReceiptID == "" || fixture.switchCandidate.ContainerID == "" ||
		fixture.switchCandidate.ReceiptID != fixture.componentReceipt ||
		fixture.switchPredecessorHead.DeploymentID != fixture.lifecycleDeploymentID {
		t.Fatal("SwitchingRoute fixture omitted sealed activation identity")
	}
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read SwitchingRoute durable count: %v", err)
		}
		return value
	}
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations
		WHERE app_id=? AND successor_deployment_id=? AND successor_generation=?`,
		fixture.migration.guarded.appID, fixture.migration.request.DeploymentID, 2)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	state, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || state.Phase != generatedruntimestate.PhaseSwitchingRoute || intents != 1 ||
		activations != 0 || retirementComponents != 0 || retirementReceipts != 0 ||
		authorizations != 0 || handoffs != 0 || completions != 0 {
		t.Fatalf("SwitchingRoute state phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			state.Phase, intents, activations, retirementComponents, retirementReceipts,
			authorizations, handoffs, completions, err)
	}
	var componentState string
	if err := db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.migration.request.DeploymentID,
		fixture.componentName).Scan(&componentState); err != nil ||
		componentState != string(generatedruntimestate.ComponentHealthy) {
		t.Fatalf("SwitchingRoute component state=%q error=%v", componentState, err)
	}
	head, err := generatedruntimestate.New(db).Active(context.Background(), fixture.migration.guarded.appID)
	if err != nil || !reflect.DeepEqual(head, fixture.switchPredecessorHead) {
		t.Fatalf("SwitchingRoute predecessor head=%#v want=%#v error=%v", head,
			fixture.switchPredecessorHead, err)
	}
	protected, err := fixture.switchStateStore.load()
	proposed := routeRecord{Slot: generatedidentity.Slot(fixture.switchCandidate.Slot),
		Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(fixture.switchCandidate)}}
	postMVRoute, postMVFound := fixture.switchPostMVState.Active[fixture.migration.guarded.appID]
	pending := fixture.switchPostMVState.Pending
	if pending == nil || pending.AppID != fixture.migration.guarded.appID || pending.Previous == nil ||
		!sameRoute(*pending.Previous, fixture.switchPreviousRoute) || !sameRoute(pending.Proposed, proposed) ||
		!postMVFound || !sameRoute(postMVRoute, fixture.switchPreviousRoute) {
		t.Fatalf("SwitchingRoute post-mv snapshot route=%#v found=%t pending=%#v",
			postMVRoute, postMVFound, pending)
	}
	protectedRoute, found := protected.Active[fixture.migration.guarded.appID]
	if err != nil || !found || !sameRoute(protectedRoute, fixture.switchPreviousRoute) ||
		!reflect.DeepEqual(protected.Pending, pending) {
		t.Fatalf("SwitchingRoute protected route=%#v found=%t pending=%#v error=%v",
			protectedRoute, found, protected.Pending, err)
	}
	if !reflect.DeepEqual(protected, fixture.switchPostMVState) {
		t.Fatal("SwitchingRoute protected state changed after the sealed invocation left")
	}
	expected := expectedConfig(t, fixture.routes.ingressRunner,
		map[string]routeRecord{fixture.migration.guarded.appID: proposed})
	restart := fixture.routes.files[caddyContainerName+":/config/active.json"]
	if !bytes.Equal(fixture.routes.liveConfig, expected) || !bytes.Equal(restart, expected) ||
		fixture.routes.restartInstalls < 2 ||
		generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) !=
			generatedRouteColdAccountingSwitchingRoutePhysicalProposedReloads {
		t.Fatalf("SwitchingRoute proposed physical route live=%t restart=%t installs=%d",
			bytes.Equal(fixture.routes.liveConfig, expected), bytes.Equal(restart, expected),
			fixture.routes.restartInstalls)
	}
	predecessor, predecessorFound := fixture.process.predecessor.physical[fixture.retiredContainerID]
	successor, successorFound := fixture.process.predecessor.physical[fixture.currentContainerID]
	if !predecessorFound || !predecessor.exists || !predecessor.running || predecessor.health != "healthy" ||
		!successorFound || !successor.exists || !successor.running || successor.health != "healthy" ||
		successor.policy.identity.ReceiptID != fixture.componentReceipt {
		t.Fatalf("SwitchingRoute predecessor=%#v/%t successor=%#v/%t", predecessor,
			predecessorFound, successor, successorFound)
	}
	expectedCapacity := fmt.Sprintf("%d %d\n", fixture.identity.options.MemoryBytes-1,
		fixture.identity.options.ReplacementDiskBytes)
	if string(fixture.construction.capacity) != expectedCapacity {
		t.Fatalf("SwitchingRoute constrained capacity=%q want=%q", fixture.construction.capacity,
			expectedCapacity)
	}
}

func assertGeneratedRouteColdAccountingInitialSwitchingRouteSnapshot(t *testing.T,
	fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	emptyHead := generatedruntimestate.ActiveHead{AppID: fixture.migration.guarded.appID}
	if fixture.switchReceiptID == "" || fixture.switchCandidate.ContainerID == "" ||
		fixture.switchCandidate.ReceiptID != fixture.componentReceipt ||
		fixture.switchPredecessorHead != emptyHead || fixture.lifecycleDeploymentID != "" ||
		fixture.retiredContainerID != "" || fixture.switchPreviousRoute.Slot != "" ||
		len(fixture.switchPreviousRoute.Endpoints) != 0 {
		t.Fatalf("initial SwitchingRoute fixture identity receipt=%q candidate=%#v head=%#v",
			fixture.switchReceiptID, fixture.switchCandidate, fixture.switchPredecessorHead)
	}
	db := fixture.identity.database
	count := func(query string, arguments ...any) int {
		t.Helper()
		var value int
		if err := db.QueryRow(query, arguments...).Scan(&value); err != nil {
			t.Fatalf("read initial SwitchingRoute durable count: %v", err)
		}
		return value
	}
	intents := count(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, fixture.componentReceipt)
	activations := count(`SELECT COUNT(*) FROM generated_route_activations
		WHERE app_id=? AND successor_deployment_id=?`, fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	retirementComponents := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	retirementReceipts := count(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	authorizations := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	handoffs := count(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	completions := count(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, fixture.switchReceiptID)
	runtime, err := generatedruntimestate.New(db).Get(context.Background(), fixture.migration.guarded.appID,
		fixture.migration.request.DeploymentID)
	if err != nil || runtime.Phase != generatedruntimestate.PhaseSwitchingRoute || intents != 1 ||
		activations != 0 || retirementComponents != 0 || retirementReceipts != 0 ||
		authorizations != 0 || handoffs != 0 || completions != 0 {
		t.Fatalf("initial SwitchingRoute phase=%s receipts=%d/%d/%d/%d/%d/%d/%d error=%v",
			runtime.Phase, intents, activations, retirementComponents, retirementReceipts,
			authorizations, handoffs, completions, err)
	}
	var componentState string
	if err := db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.migration.request.DeploymentID,
		fixture.componentName).Scan(&componentState); err != nil ||
		componentState != string(generatedruntimestate.ComponentHealthy) {
		t.Fatalf("initial SwitchingRoute component state=%q error=%v", componentState, err)
	}
	head, err := generatedruntimestate.New(db).Active(context.Background(), fixture.migration.guarded.appID)
	if err != nil || head != emptyHead {
		t.Fatalf("initial SwitchingRoute head=%#v want=%#v error=%v", head, emptyHead, err)
	}
	protected, err := fixture.switchStateStore.load()
	proposed := routeRecord{Slot: generatedidentity.Slot(fixture.switchCandidate.Slot),
		Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(fixture.switchCandidate)}}
	pending := fixture.switchPostMVState.Pending
	_, postMVActive := fixture.switchPostMVState.Active[fixture.migration.guarded.appID]
	if pending == nil || pending.AppID != fixture.migration.guarded.appID || pending.Previous != nil ||
		!sameRoute(pending.Proposed, proposed) || postMVActive || len(fixture.switchPostMVState.Active) != 0 {
		t.Fatalf("initial SwitchingRoute post-mv active=%#v pending=%#v",
			fixture.switchPostMVState.Active, pending)
	}
	_, protectedActive := protected.Active[fixture.migration.guarded.appID]
	if err != nil || protectedActive || len(protected.Active) != 0 || !reflect.DeepEqual(protected.Pending, pending) ||
		!reflect.DeepEqual(protected, fixture.switchPostMVState) {
		t.Fatalf("initial SwitchingRoute protected=%#v error=%v", protected, err)
	}
	expected := expectedConfig(t, fixture.routes.ingressRunner,
		map[string]routeRecord{fixture.migration.guarded.appID: proposed})
	restart := fixture.routes.files[caddyContainerName+":/config/active.json"]
	if !bytes.Equal(fixture.routes.liveConfig, expected) || !bytes.Equal(restart, expected) ||
		fixture.routes.restartInstalls != 2 ||
		generatedRouteColdAccountingProposedReloadCount(fixture.routes.commands) !=
			generatedRouteColdAccountingInitialSwitchingRoutePhysicalProposedReloads {
		t.Fatalf("initial SwitchingRoute physical live=%t restart=%t installs=%d",
			bytes.Equal(fixture.routes.liveConfig, expected), bytes.Equal(restart, expected),
			fixture.routes.restartInstalls)
	}
	successor, successorFound := fixture.process.predecessor.physical[fixture.currentContainerID]
	if !successorFound || !successor.exists || !successor.running || successor.health != "healthy" ||
		successor.policy.identity.ReceiptID != fixture.componentReceipt {
		t.Fatalf("initial SwitchingRoute successor=%#v found=%t", successor, successorFound)
	}
	expectedCapacity := fmt.Sprintf("%d %d\n", fixture.identity.options.MemoryBytes-1,
		fixture.identity.options.ReplacementDiskBytes)
	if string(fixture.construction.capacity) != expectedCapacity {
		t.Fatalf("initial SwitchingRoute constrained capacity=%q want=%q", fixture.construction.capacity,
			expectedCapacity)
	}
}

func assertGeneratedRouteColdAccountingSwitchingRouteHandoffSeed(t *testing.T,
	result *generatedDomainConstruction, fixture generatedRouteColdAccountingFixture,
) {
	t.Helper()
	if result == nil || result.candidates == nil || result.candidates.ledger == nil {
		t.Fatal("SwitchingRoute cold construction omitted candidate ledger")
	}
	ledger := result.candidates.ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("SwitchingRoute cold construction omitted pending successor")
	}
	group.mu.Lock()
	component := group.byName[fixture.componentName]
	valid := component != nil && component.receipt != nil && component.receipt.id == fixture.componentReceipt &&
		component.charge != nil && component.charge.state == candidateChargeActive &&
		component.charge.owner == candidateAccountingCandidate && component.charge.receiptRetained &&
		!component.charge.explicitRetained && !component.charge.logicallyRetired && !component.tokenHeld &&
		candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalHandedToRoute
	group.mu.Unlock()
	if !valid {
		t.Fatalf("SwitchingRoute cold successor did not retain exact Candidate-owned handoff marker: %#v", component)
	}
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
	switchReceiptID       string
	switchCandidate       CandidateIdentity
	switchPredecessorHead generatedruntimestate.ActiveHead
	switchPreviousRoute   routeRecord
	switchStateStore      *stateStore
	switchPostMVState     routeState
}

type generatedRouteColdAccountingRouteCommitExitRunner struct {
	delegate    runtimeprocess.CommandRunner
	store       *stateStore
	reached     chan struct{}
	once        sync.Once
	proposed    bool
	postMVState routeState
	captureErr  error
}

type generatedRouteColdAccountingRoutePrePublishExitRunner struct {
	delegate   runtimeprocess.CommandRunner
	store      *stateStore
	reached    chan struct{}
	once       sync.Once
	state      routeState
	captureErr error
}

// generatedRouteColdAccountingRouteReadRunner preserves the production
// restart-config readback contract while the authentic producer is still
// performing its activation. The shared ingress test runner models writes by
// route name, whereas production reads the committed file by immutable Caddy
// ID through docker cp.
type generatedRouteColdAccountingRouteReadRunner struct {
	routes   *generatedRoutePredecessorRunner
	delegate runtimeprocess.CommandRunner
}

type generatedRouteColdAccountingFinishLostReplyRunner struct {
	store         *stateStore
	mutate        func()
	ready         func() bool
	fired         bool
	commands      int
	beforePersist bool
}

func (r *generatedRouteColdAccountingFinishLostReplyRunner) intercept(
	_ []string, _ *generatedRoutePredecessorRunner,
) (runtimeprocess.CommandResult, bool, error) {
	if r == nil || r.store == nil {
		return runtimeprocess.CommandResult{}, true, errors.New("missing recovered finish lost-reply runner")
	}
	state, err := r.store.load()
	if err != nil {
		return runtimeprocess.CommandResult{}, true, err
	}
	// A pre-persist failure is injected only from the first Manager finish
	// attestation; recovered startup reads may observe the same protected
	// Pending state and must not be mistaken for a finish attempt. The
	// post-persist case fails the first physical read after Pending is cleared,
	// which models a genuine lost protected-write reply without replacing it.
	finishFrame := !r.beforePersist || generatedRouteColdAccountingFinishAttestationFrame()
	ready := r.ready == nil || r.ready()
	if !r.fired && finishFrame && ready &&
		(r.beforePersist && state.Pending != nil || !r.beforePersist && state.Pending == nil) {
		r.fired = true
		if r.mutate != nil {
			r.mutate()
		}
		return runtimeprocess.CommandResult{}, true, errors.New("fixed recovered finish lost reply")
	}
	r.commands++
	return runtimeprocess.CommandResult{}, false, nil
}

func generatedRouteColdAccountingFinishAttestationFrame() bool {
	var callers [32]uintptr
	count := runtime.Callers(2, callers[:])
	frames := runtime.CallersFrames(callers[:count])
	finish, projection := false, false
	for {
		frame, more := frames.Next()
		switch {
		case strings.HasSuffix(frame.Function, ".(*Manager).finishGeneratedDomainRouteActivation"):
			finish = true
		case strings.HasSuffix(frame.Function, ".(*Manager).attestLegacyRouteProjectionLocked"):
			projection = true
		}
		if finish && projection {
			return true
		}
		if !more {
			return false
		}
	}
}

func (r *generatedRouteColdAccountingRouteReadRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if r == nil || r.routes == nil || r.delegate == nil {
		return runtimeprocess.CommandResult{}, errors.New("missing authentic route read runner")
	}
	if result, handled, err := generatedRouteColdAccountingPhysicalRead(request.Args, r.routes); handled {
		return result, err
	}
	return r.delegate.Run(ctx, request)
}

func (r *generatedRouteColdAccountingRouteCommitExitRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	result, err := r.delegate.Run(ctx, request)
	if err == nil && reflect.DeepEqual(request.Args, []string{
		"container", "exec", caddyContainerName, "caddy", "reload", "--config", "/config/proposed.json",
	}) {
		r.proposed = true
	}
	if err == nil && r.proposed && reflect.DeepEqual(request.Args, []string{
		"container", "exec", "--user", "0:0", caddyContainerName,
		"mv", "/config/active.next.json", "/config/active.json",
	}) {
		var captureErr error
		r.postMVState, captureErr = r.store.load()
		r.captureErr = errors.Join(r.captureErr, captureErr)
		r.once.Do(func() { close(r.reached) })
		runtime.Goexit()
	}
	return result, err
}

func (r *generatedRouteColdAccountingRoutePrePublishExitRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if reflect.DeepEqual(request.Args, []string{
		"container", "exec", caddyContainerName, "caddy", "reload", "--config", "/config/proposed.json",
	}) {
		r.state, r.captureErr = r.store.load()
		r.once.Do(func() { close(r.reached) })
		runtime.Goexit()
	}
	return r.delegate.Run(ctx, request)
}

func generatedRouteColdAccountingInterruptBeforeRoutePublish(t *testing.T,
	sealed *GeneratedRouteExecution, ctx context.Context,
	routeRunner *generatedDomainTwoComponentRouteRunner,
	physicalRoutes *generatedRoutePredecessorRunner,
	routeReadRunner *generatedRouteColdAccountingRouteReadRunner, store *stateStore,
) routeState {
	t.Helper()
	if sealed == nil || ctx == nil || routeRunner == nil || physicalRoutes == nil ||
		routeReadRunner == nil || store == nil {
		t.Fatal("route pre-publication interruption requires one exact producer")
	}
	interruption := &generatedRouteColdAccountingRoutePrePublishExitRunner{
		delegate: physicalRoutes, store: store, reached: make(chan struct{}),
	}
	routeRunner.delegate = &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes,
		delegate: interruption}
	defer func() { routeRunner.delegate = routeReadRunner }()
	returned := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, unexpectedRetirement, activateErr := sealed.ActivateWithPredecessorRetirement(ctx)
		if unexpectedRetirement != nil {
			unexpectedRetirement.Leave()
		}
		returned <- fmt.Errorf("sealed activation returned retirement=%t error=%v",
			unexpectedRetirement != nil, activateErr)
	}()
	select {
	case <-interruption.reached:
	case activateErr := <-returned:
		t.Fatalf("sealed activation returned before route pre-publication interruption: %v", activateErr)
	case <-time.After(5 * time.Second):
		t.Fatal("sealed activation did not reach the route pre-publication interruption")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sealed activation did not leave after the route pre-publication interruption")
	}
	select {
	case activateErr := <-returned:
		t.Fatalf("sealed activation returned after route pre-publication interruption: %v", activateErr)
	default:
	}
	if interruption.captureErr != nil || interruption.state.Pending == nil {
		t.Fatalf("capture pre-publication Pending state=%#v error=%v", interruption.state, interruption.captureErr)
	}
	return interruption.state
}

func generatedRouteColdAccountingInterruptAtRouteCommit(t *testing.T,
	sealed *GeneratedRouteExecution, ctx context.Context,
	routeRunner *generatedDomainTwoComponentRouteRunner,
	physicalRoutes *generatedRoutePredecessorRunner,
	routeReadRunner *generatedRouteColdAccountingRouteReadRunner, store *stateStore,
) routeState {
	t.Helper()
	if sealed == nil || ctx == nil || routeRunner == nil || physicalRoutes == nil ||
		routeReadRunner == nil || store == nil {
		t.Fatal("route commit interruption requires one exact producer")
	}
	interruption := &generatedRouteColdAccountingRouteCommitExitRunner{
		delegate: physicalRoutes, store: store, reached: make(chan struct{}),
	}
	routeRunner.delegate = &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes,
		delegate: interruption}
	defer func() { routeRunner.delegate = routeReadRunner }()
	returned := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, unexpectedRetirement, activateErr := sealed.ActivateWithPredecessorRetirement(ctx)
		if unexpectedRetirement != nil {
			unexpectedRetirement.Leave()
		}
		returned <- fmt.Errorf("sealed activation returned retirement=%t error=%v",
			unexpectedRetirement != nil, activateErr)
	}()
	select {
	case <-interruption.reached:
	case activateErr := <-returned:
		t.Fatalf("sealed activation returned before route commit interruption: %v", activateErr)
	case <-time.After(5 * time.Second):
		t.Fatal("sealed activation did not reach the route commit interruption")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sealed activation did not leave after the route commit interruption")
	}
	select {
	case activateErr := <-returned:
		t.Fatalf("sealed activation returned after route commit interruption: %v", activateErr)
	default:
	}
	if interruption.captureErr != nil {
		t.Fatalf("capture sealed activation transition: %v", interruption.captureErr)
	}
	return interruption.postMVState
}

type generatedRouteColdAccountingRunner struct {
	mu                  sync.RWMutex
	cold                runtimeprocess.CommandRunner
	routes              *generatedRoutePredecessorRunner
	routeWriteDirectory string
	beforeRouteRead     func([]string, *generatedRoutePredecessorRunner) (runtimeprocess.CommandResult, bool, error)
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

func (r *generatedRouteColdAccountingRunner) setRouteReadHook(
	hook func([]string, *generatedRoutePredecessorRunner) (runtimeprocess.CommandResult, bool, error),
) {
	r.mu.Lock()
	r.beforeRouteRead = hook
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
	routeWriteDirectory := r.routeWriteDirectory
	beforeRouteRead := r.beforeRouteRead
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
	if beforeRouteRead != nil {
		if result, handled, err := beforeRouteRead(request.Args, routes); handled {
			return result, err
		}
	}
	if result, handled, err := generatedRouteColdAccountingPhysicalRead(request.Args, routes); handled {
		return result, err
	}
	if generatedRouteColdAccountingRouteCommand(routes, request.Args) {
		return routes.Run(ctx, request)
	}
	if generatedRouteColdAccountingRouteWrite(routes, routeWriteDirectory, request.Args) {
		return routes.Run(ctx, request)
	}
	return delegate.Run(ctx, request)
}

// generatedRouteColdAccountingPhysicalRead supplies only the read-only Caddy
// protocols that the authentic publisher already created. It deliberately
// rejects all unrecognized curl shapes: the fixture must not turn a broad
// command matcher into an attestation bypass.
func generatedRouteColdAccountingPhysicalRead(args []string,
	routes *generatedRoutePredecessorRunner,
) (runtimeprocess.CommandResult, bool, error) {
	if archive, handled, err := generatedRouteColdAccountingRestartConfigArchive(args, routes); handled {
		if err != nil {
			return runtimeprocess.CommandResult{}, true, err
		}
		return runtimeprocess.CommandResult{Stdout: archive}, true, nil
	}
	if routes == nil || routes.ingressRunner == nil || len(args) < 4 ||
		args[0] != "container" || args[1] != "exec" ||
		!generatedRouteColdAccountingObservedIngressID(routes, args[2]) {
		return runtimeprocess.CommandResult{}, false, nil
	}
	if reflect.DeepEqual(args[3:], []string{
		"curl", "--silent", "--show-error", "--fail", "--max-time", "10", "http://127.0.0.1:2019/config/",
	}) || reflect.DeepEqual(args[3:], []string{
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/",
	}) {
		if len(routes.liveConfig) == 0 {
			return runtimeprocess.CommandResult{}, true, errors.New("missing authentic live route publication")
		}
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), routes.liveConfig...)}, true, nil
	}
	const staticPrefixLength = 19
	staticPrefix := []string{
		"curl", "--disable", "--silent", "--show-error", "--output", "-", "--write-out", "\n%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
		"--header", "Host: wrong.invalid",
	}
	if len(args) != 3+staticPrefixLength+1 || !reflect.DeepEqual(args[3:3+staticPrefixLength], staticPrefix) ||
		!generatedRouteColdAccountingStaticChallengeURL(args[len(args)-1], routes, args[2]) {
		if generatedRouteColdAccountingReservedCaddyReadTarget(args) {
			return runtimeprocess.CommandResult{}, true, errors.New("unrecognized caddy physical read contract")
		}
		return runtimeprocess.CommandResult{}, false, nil
	}
	if _, err := generatedRouteColdAccountingPublishedLiveConfig(routes); err != nil {
		return runtimeprocess.CommandResult{}, true, err
	}
	return runtimeprocess.CommandResult{Stdout: []byte("\n404")}, true, nil
}

func generatedRouteColdAccountingObservedIngressID(routes *generatedRoutePredecessorRunner, containerID string) bool {
	if routes == nil || routes.ingressRunner == nil {
		return false
	}
	for id := range routes.ingressContainers {
		if normalizeID(containerID) == normalizeID(id) {
			return true
		}
	}
	return false
}

func generatedRouteColdAccountingPublishedLiveConfig(routes *generatedRoutePredecessorRunner) ([]byte, error) {
	if routes == nil || routes.ingressRunner == nil {
		return nil, errors.New("missing authentic route publication")
	}
	restart, found := routes.files[caddyContainerName+":/config/active.json"]
	if !found || len(restart) == 0 || len(routes.liveConfig) == 0 || !bytes.Equal(restart, routes.liveConfig) {
		return nil, errors.New("published route live and restart configurations do not agree")
	}
	return append([]byte(nil), routes.liveConfig...), nil
}

func generatedRouteColdAccountingReservedCaddyReadTarget(args []string) bool {
	if len(args) < 4 {
		return false
	}
	target := args[len(args)-1]
	return target == "http://127.0.0.1:2019/config/" || strings.Contains(target, gatewayV2ChallengePathPrefix)
}

func generatedRouteColdAccountingStaticChallengeURL(value string, routes *generatedRoutePredecessorRunner,
	containerID string,
) bool {
	if routes == nil || routes.ingressRunner == nil {
		return false
	}
	for id, container := range routes.ingressContainers {
		if normalizeID(containerID) != normalizeID(id) {
			continue
		}
		address, _, found := strings.Cut(container.IPv4Address, "/")
		if !found || address == "" {
			return false
		}
		prefix := "http://" + address + ":8080" + gatewayV2ChallengePathPrefix
		return strings.HasPrefix(value, prefix) && validSHA256(strings.TrimPrefix(value, prefix))
	}
	return false
}

// generatedRouteColdAccountingRestartConfigArchive mirrors Docker's stdout
// archive for the production restart-config readback. The authentic fixture
// has already published active.json through the real route runner; cold
// construction must consume the same physical bytes through the caddy ID that
// a fresh Manager observes rather than bypassing that check.
func generatedRouteColdAccountingRestartConfigArchive(args []string,
	routes *generatedRoutePredecessorRunner,
) ([]byte, bool, error) {
	if routes == nil || routes.ingressRunner == nil || len(args) != 4 ||
		args[0] != "container" || args[1] != "cp" || args[3] != "-" ||
		!strings.HasSuffix(args[2], ":/config/active.json") {
		return nil, false, nil
	}
	containerID := strings.TrimSuffix(args[2], ":/config/active.json")
	if !generatedRouteColdAccountingObservedIngressID(routes, containerID) {
		return nil, false, nil
	}
	body, found := routes.files[caddyContainerName+":/config/active.json"]
	if !found || len(body) == 0 {
		return nil, true, errors.New("missing published restart configuration")
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "active.json", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return nil, true, err
	}
	if _, err := writer.Write(body); err != nil {
		return nil, true, err
	}
	if err := writer.Close(); err != nil {
		return nil, true, err
	}
	return archive.Bytes(), true, nil
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

// generatedRouteColdAccountingRouteWrite admits only the complete canonical
// Legacy publication sequence used after cold construction. Startup census,
// candidate hardening, and every unrecognized command remain owned by the
// permanent cold delegate.
func generatedRouteColdAccountingRouteWrite(routes *generatedRoutePredecessorRunner,
	workingDirectory string, args []string,
) bool {
	if routes == nil || routes.ingressRunner == nil || workingDirectory == "" {
		return false
	}
	configPath := func(value string) bool {
		return value == "/config/recovery.json" || value == "/config/proposed.json"
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" &&
		strings.HasPrefix(args[3], caddyContainerName+":") {
		destination := strings.TrimPrefix(args[3], caddyContainerName+":")
		base := filepath.Base(args[2])
		relative, relErr := filepath.Rel(workingDirectory, args[2])
		info, statErr := os.Lstat(args[2])
		return configPath(destination) && relErr == nil && relative == base &&
			strings.HasPrefix(base, ".rig-caddy-") && strings.HasSuffix(base, ".json") &&
			len(strings.TrimSuffix(strings.TrimPrefix(base, ".rig-caddy-"), ".json")) > 0 &&
			statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
			!generatedIngressPathIsReparsePoint(args[2])
	}
	if len(args) == 7 && args[0] == "container" && args[1] == "exec" &&
		args[2] == caddyContainerName && args[3] == "caddy" &&
		(args[4] == "validate" || args[4] == "reload") && args[5] == "--config" && configPath(args[6]) {
		return true
	}
	if len(args) == 8 && reflect.DeepEqual(args[:5], []string{
		"container", "exec", "--user", "0:0", caddyContainerName,
	}) {
		if args[5] == "cp" && configPath(args[6]) && args[7] == "/config/current.json" {
			return true
		}
		return reflect.DeepEqual(args[5:], []string{
			"cp", "/config/current.json", "/config/active.next.json",
		}) || reflect.DeepEqual(args[5:], []string{
			"mv", "/config/active.next.json", "/config/active.json",
		})
	}
	if len(args) == 4 && args[0] == "network" &&
		(args[1] == "connect" || args[1] == "disconnect") &&
		generatedRouteColdAccountingObservedIngressID(routes, args[3]) {
		for _, endpoint := range routes.endpoints {
			if args[2] == endpoint.endpoint.NetworkName {
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
	generatedRouteColdAccountingInitialSwitchingRoute
	generatedRouteColdAccountingLifecycleComplete
	generatedRouteColdAccountingLifecycleDraining
	generatedRouteColdAccountingLifecycleRetired
	generatedRouteColdAccountingLifecycleAuthorized
	generatedRouteColdAccountingLifecycleActivated
	generatedRouteColdAccountingLifecyclePostActivationPending
	generatedRouteColdAccountingLifecycleSwitchingRoute
	generatedRouteColdAccountingLifecyclePreviousExact
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

func newGeneratedRouteColdAccountingPostActivationPendingFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t,
		generatedRouteColdAccountingLifecyclePostActivationPending)
}

func newGeneratedRouteColdAccountingSwitchingRouteFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleSwitchingRoute)
}

func newGeneratedRouteColdAccountingPreviousExactFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecyclePreviousExact)
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

func newGeneratedRouteColdAccountingInitialSwitchingRouteFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t,
		generatedRouteColdAccountingInitialSwitchingRoute)
}

// generatedRouteColdAccountingCommitBeforeFinish builds the authentic
// inter-method crash boundary: the exact physical Pending transition and
// atomic 042 writer complete, but the caller never invokes manager.finish.
// It deliberately reuses the sealed execution's fixed writer input and the
// same read-only claim guard/route proof sequence as activateCore.
func generatedRouteColdAccountingCommitBeforeFinish(t *testing.T, ctx context.Context,
	sealed *GeneratedRouteExecution, execution *GeneratedCandidateExecution,
) generatedruntimestate.RouteActivationReceipt {
	t.Helper()
	if sealed == nil || sealed.authority == nil || execution == nil || ctx == nil {
		t.Fatal("missing sealed post-activation Pending fixture")
	}
	state, done, err := execution.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	current, err := state.observeCandidateState(true)
	expectedCandidateHead := candidateHeadSnapshot{
		appID: sealed.expectedHead.AppID, deploymentID: sealed.expectedHead.DeploymentID,
		releaseID: sealed.expectedHead.ReleaseID, slot: sealed.expectedHead.Slot,
		generation: sealed.expectedHead.Generation, activationReceiptID: sealed.expectedHead.ActivationReceiptID,
	}
	if err != nil || !routeRosterMatchesCandidateSnapshot(sealed.candidateRoster, current) ||
		current.observedHead != expectedCandidateHead {
		t.Fatalf("post-activation Pending candidate snapshot=%#v error=%v", current, err)
	}
	if sealed.hadPrevious && sealed.gatewayTransition == nil {
		observation, observeErr := sealed.authority.manager.Observe(ctx, sealed.request.AppID)
		if observeErr != nil || !sameRoute(sealed.previous,
			routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
			t.Fatalf("post-activation Pending predecessor observation=%#v error=%v", observation, observeErr)
		}
	}
	tx, err := sealed.authority.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.pinned.CheckRouteActivationTx(tx, state.pins.claim, state.pins.deploymentID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	transition, err := sealed.authority.manager.beginGeneratedDomainRouteActivation(ctx, sealed.request,
		sealed.gatewayTransition)
	if err != nil {
		t.Fatal(err)
	}
	before, hadBefore, after, valid := generatedRouteActivationGatewayRoutes(transition)
	proposed := routeRecord{Slot: sealed.request.ToSlot,
		Endpoints: append([]generatedroute.RouteEndpoint(nil), sealed.request.Endpoints...)}
	if !valid || hadBefore != sealed.hadPrevious || hadBefore && !sameRoute(before, sealed.previous) ||
		!sameRoute(after, proposed) {
		t.Fatalf("post-activation Pending transition before=%#v had=%t after=%#v", before, hadBefore, after)
	}
	proof := generatedRouteProofDocument{Version: 1, ReceiptID: sealed.receiptID, After: after}
	if sealed.hadPrevious {
		previous := sealed.previous
		proof.Before = &previous
	}
	body, err := marshalCanonicalGeneratedRouteDocument(proof)
	if err != nil {
		t.Fatal(err)
	}
	input := generatedRouteActivationInput{receiptID: sealed.receiptID, appID: sealed.request.AppID,
		successorDeploymentID: state.pins.deploymentID, successorReleaseID: state.pins.releaseID,
		successorSlot: state.pins.candidateSlot, expectedHead: sealed.expectedHead,
		candidateRoster: sealed.candidateRoster, predecessorRoster: sealed.predecessorRoster,
		routeIntent: sealed.intent, committedRouteProof: body}
	result, err := sealed.authority.activateRoute(ctx, state.pinned, state.pins.claim, input)
	if err != nil || !result.changed || result.receipt.ReceiptID != sealed.receiptID {
		t.Fatalf("commit post-activation Pending result=%#v error=%v", result, err)
	}
	return result.receipt
}

func newGeneratedRouteColdAccountingFixtureAtCompletion(t *testing.T,
	stage generatedRouteColdAccountingCompletionStage,
) generatedRouteColdAccountingFixture {
	t.Helper()
	if stage != generatedRouteColdAccountingInitialComplete &&
		stage != generatedRouteColdAccountingInitialSwitchingRoute &&
		stage != generatedRouteColdAccountingLifecycleComplete &&
		stage != generatedRouteColdAccountingLifecycleDraining &&
		stage != generatedRouteColdAccountingLifecycleRetired &&
		stage != generatedRouteColdAccountingLifecycleAuthorized &&
		stage != generatedRouteColdAccountingLifecycleActivated &&
		stage != generatedRouteColdAccountingLifecyclePostActivationPending &&
		stage != generatedRouteColdAccountingLifecycleSwitchingRoute &&
		stage != generatedRouteColdAccountingLifecyclePreviousExact {
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
	routeReadRunner := &generatedRouteColdAccountingRouteReadRunner{routes: physicalRoutes, delegate: physicalRoutes}
	routeRunner := &generatedDomainTwoComponentRouteRunner{daemonID: "generated-domain-cold-accounting",
		delegate: routeReadRunner}
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
	firstHead := generatedruntimestate.ActiveHead{}
	currentReceipt := generatedruntimestate.RouteActivationReceipt{}
	lifecycleDeploymentID := ""
	predecessorReceipt := ""
	retiredContainerID := ""
	switchReceiptID := ""
	switchCandidate := CandidateIdentity{}
	switchPredecessorHead := generatedruntimestate.ActiveHead{}
	switchPreviousRoute := routeRecord{}
	var switchStateStore *stateStore
	switchPostMVState := routeState{}
	effectsMigration := migration
	var retirement *GeneratedPredecessorRetirement
	if stage == generatedRouteColdAccountingInitialSwitchingRoute {
		firstHead, err = generatedruntimestate.New(identity.database).Active(
			context.Background(), migration.guarded.appID)
		if err != nil || firstHead.AppID != migration.guarded.appID || firstHead.DeploymentID != "" ||
			firstHead.ReleaseID != "" || firstHead.Slot != "" || firstHead.Generation != 0 ||
			firstHead.ActivationReceiptID != "" || !firstHead.UpdatedAt.IsZero() {
			t.Fatalf("read initial generation-zero head=%#v error=%v", firstHead, err)
		}
		switchReceiptID = sealed.receiptID
		switchCandidate = candidateIdentity
		switchPredecessorHead = firstHead
		switchStateStore = store
		switchPostMVState = generatedRouteColdAccountingInterruptAtRouteCommit(t, sealed,
			migration.guarded.context, routeRunner, physicalRoutes, routeReadRunner, store)
	} else {
		_, retirement, err = sealed.ActivateWithPredecessorRetirement(migration.guarded.context)
		if err != nil || retirement == nil {
			t.Fatalf("activate authentic empty-predecessor Current retirement=%#v error=%v", retirement, err)
		}
		t.Cleanup(retirement.Leave)
		if err := retirement.Complete(context.Background()); err != nil {
			t.Fatalf("complete authentic empty-predecessor Current: %v", err)
		}
		firstHead, err = generatedruntimestate.New(identity.database).Active(
			context.Background(), migration.guarded.appID)
		if err != nil || firstHead.DeploymentID != migration.request.DeploymentID {
			t.Fatalf("read first completed Current head=%#v error=%v", firstHead, err)
		}
		currentReceipt = retirement.state.receipt
	}
	if stage != generatedRouteColdAccountingInitialComplete &&
		stage != generatedRouteColdAccountingInitialSwitchingRoute {
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
		if stage == generatedRouteColdAccountingLifecycleSwitchingRoute ||
			stage == generatedRouteColdAccountingLifecyclePreviousExact {
			switchReceiptID = secondSealed.receiptID
			switchCandidate = secondIdentity
			switchPredecessorHead = firstHead
			switchPreviousRoute = routeRecord{Slot: currentRoute.Slot,
				Endpoints: append([]generatedroute.RouteEndpoint(nil), currentRoute.Endpoints...)}
			switchStateStore = store
			if stage == generatedRouteColdAccountingLifecyclePreviousExact {
				switchPostMVState = generatedRouteColdAccountingInterruptBeforeRoutePublish(t, secondSealed,
					secondMigration.guarded.context, routeRunner, physicalRoutes, routeReadRunner, store)
			} else {
				switchPostMVState = generatedRouteColdAccountingInterruptAtRouteCommit(t, secondSealed,
					secondMigration.guarded.context, routeRunner, physicalRoutes, routeReadRunner, store)
			}
		} else if stage == generatedRouteColdAccountingLifecyclePostActivationPending {
			currentReceipt = generatedRouteColdAccountingCommitBeforeFinish(t, secondMigration.guarded.context,
				secondSealed, secondExecution)
		} else {
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
		}
		secondExecution.Leave()
		lifecycleDeploymentID = firstHead.DeploymentID
		migration = secondMigration
		candidateIdentity = secondIdentity
	}
	if retirement != nil {
		retirement.Leave()
	}
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
	if stage == generatedRouteColdAccountingInitialSwitchingRoute {
		// The first candidate remains the authentic Healthy successor at the
		// pre-042 route boundary; there is no predecessor container to recover.
	} else if stage == generatedRouteColdAccountingLifecycleActivated ||
		stage == generatedRouteColdAccountingLifecyclePostActivationPending ||
		stage == generatedRouteColdAccountingLifecycleSwitchingRoute ||
		stage == generatedRouteColdAccountingLifecyclePreviousExact {
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
	if stage == generatedRouteColdAccountingInitialSwitchingRoute ||
		stage == generatedRouteColdAccountingLifecycleDraining ||
		stage == generatedRouteColdAccountingLifecycleRetired ||
		stage == generatedRouteColdAccountingLifecycleAuthorized ||
		stage == generatedRouteColdAccountingLifecycleActivated ||
		stage == generatedRouteColdAccountingLifecyclePostActivationPending ||
		stage == generatedRouteColdAccountingLifecycleSwitchingRoute ||
		stage == generatedRouteColdAccountingLifecyclePreviousExact {
		if identity.options.MemoryBytes <= 1 || identity.options.ReplacementDiskBytes == 0 {
			t.Fatal("interrupted Draining fixture has invalid capacity limits")
		}
		cold.capacity = []byte(fmt.Sprintf("%d %d\n", identity.options.MemoryBytes-1,
			identity.options.ReplacementDiskBytes))
	}
	builder.runner = &generatedDomainTwoComponentRouteRunner{delegate: cold, builder: &builder.physical}
	process := &generatedRouteColdAccountingRunner{cold: builder.runner, routes: physicalRoutes,
		routeWriteDirectory: identity.options.WorkingDirectory,
		predecessor:         candidateRunner, predecessorPhysical: []candidateOwnerPhysical{retirementPhysical}}

	return generatedRouteColdAccountingFixture{identity: identity, migration: migration, jobs: jobService,
		process: process, routes: physicalRoutes, predecessor: owner.runner, construction: cold,
		environment: &candidateFactoryEnvironment{path: t.TempDir()}, firstReceipt: currentReceipt,
		componentName: physicalCandidate.policy.identity.Component, componentReceipt: candidateIdentity.ReceiptID,
		predecessorReceipt: predecessorReceipt, lifecycleDeploymentID: lifecycleDeploymentID,
		restoresPredecessor: stage == generatedRouteColdAccountingLifecycleRetired ||
			stage == generatedRouteColdAccountingLifecycleAuthorized ||
			stage == generatedRouteColdAccountingLifecycleActivated ||
			stage == generatedRouteColdAccountingLifecyclePostActivationPending,
		predecessorPhysical: predecessorState,
		retiredContainerID:  retiredContainerID,
		currentContainerID:  candidateIdentity.ContainerID,
		switchReceiptID:     switchReceiptID, switchCandidate: switchCandidate,
		switchPredecessorHead: switchPredecessorHead, switchPreviousRoute: switchPreviousRoute,
		switchStateStore: switchStateStore, switchPostMVState: switchPostMVState}
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
