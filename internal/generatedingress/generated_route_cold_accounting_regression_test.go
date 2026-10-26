//go:build linux || windows

package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
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
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.migration.guarded.appID,
		deploymentID: fixture.migration.request.DeploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("cold construction omitted the completed Current accounting group")
	}
	group.mu.Lock()
	component := group.byName[fixture.componentName]
	if group.ledger != ledger || len(group.components) != 1 || group.components[0] != fixture.componentName ||
		component == nil || component.receipt == nil || component.receipt.id != fixture.componentReceipt ||
		component.tokenHeld || component.charge == nil {
		group.mu.Unlock()
		t.Fatal("cold construction published an incomplete completed Current accounting component")
	}
	receiptWord := component.receipt.state.Load()
	charge := component.charge
	token, restored := charge.token.(*generatedDomainCapacityDebtToken)
	limits, epoch := group.limits, charge.epoch
	validCharge := restored && token != nil && charge.state == candidateChargeActive &&
		charge.owner == candidateAccountingRouteActive && charge.lease == nil && !charge.logicallyRetired &&
		candidatePhysicalFromWord(receiptWord) == candidatePhysicalHandedToRoute &&
		receiptWord&candidateReceiptAcknowledged != 0 && receiptWord&candidateReceiptObserved != 0 &&
		receiptWord&candidateReceiptUnquiesced == 0
	group.mu.Unlock()
	if !validCharge || limits.MemoryBytes == 0 || limits.DiskBytes == 0 {
		t.Fatal("cold construction did not restore the exact route-active Current charge")
	}
	configured, published := token.configured(), token.published()
	result.capacity.mu.Lock()
	_, exactToken := result.capacity.tokens[token]
	validDebt := result.capacity.phase == generatedDomainCapacityPublished && result.capacity.borrows == 0 &&
		result.capacity.memory == limits.MemoryBytes && result.capacity.disk == limits.DiskBytes &&
		len(result.capacity.tokens) == 1 && exactToken && token.owner == result.capacity && token.active &&
		token.epoch == epoch && token.limits == limits
	result.capacity.mu.Unlock()
	if !configured || !published || !validDebt {
		t.Fatal("cold construction did not publish exactly one configured Current debt token")
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
	lifecycleDeploymentID string
}

type generatedRouteColdAccountingRunner struct {
	mu                  sync.RWMutex
	cold                runtimeprocess.CommandRunner
	predecessor         *generatedRouteCandidateRunner
	predecessorPhysical candidateOwnerPhysical
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

func (r *generatedRouteColdAccountingRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.mu.RLock()
	candidate := r.candidate
	predecessor := r.predecessor
	predecessorPhysical := r.predecessorPhysical
	delegate := r.cold
	r.mu.RUnlock()
	kind := candidateOwnerKind(request)
	if kind != "" {
		if candidateOwnerRunnerMatches(candidate, kind, request) {
			return candidate.Run(ctx, request)
		}
		if predecessor != nil && candidateOwnerPhysicalMatches(predecessorPhysical, kind, request) {
			if kind == candidateOwnerImageInspect || kind == candidateOwnerNetworkInspect {
				exact := &candidateOwnerRunner{physical: predecessorPhysical}
				return exact.defaultResult(kind, request)
			}
			return predecessor.Run(ctx, request)
		}
	}
	return delegate.Run(ctx, request)
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
)

func newGeneratedRouteColdAccountingFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleComplete)
}

func newGeneratedRouteColdAccountingDrainingFixture(t *testing.T) generatedRouteColdAccountingFixture {
	return newGeneratedRouteColdAccountingFixtureAtCompletion(t, generatedRouteColdAccountingLifecycleDraining)
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
		stage != generatedRouteColdAccountingLifecycleDraining {
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
	effectsMigration := migration
	if stage != generatedRouteColdAccountingInitialComplete {
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
		} else {
			if retired, err := secondRetirement.Retire(context.Background()); err != nil ||
				retired.ActivationReceiptID != secondRetirement.state.receipt.ReceiptID ||
				retired.SuccessorDeploymentID != secondMigration.request.DeploymentID {
				t.Fatalf("retire interrupted Draining predecessor=%#v error=%v", retired, err)
			}
			handoff, err := secondRetirement.completeAccountingHandoff(context.Background())
			if err != nil || handoff.ActivationReceiptID != secondRetirement.state.receipt.ReceiptID ||
				handoff.SuccessorDeploymentID != secondMigration.request.DeploymentID {
				t.Fatalf("complete interrupted Draining accounting handoff=%#v error=%v", handoff, err)
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
	objects = append(objects, generatedDomainA0ApplicationObjects(t, []candidateOwnerPhysical{physicalCandidate})...)
	builderObjects, builder := generatedDomainTwoComponentBuilderObjects(t, identity.dataRoot)
	objects = append(objects, builderObjects...)
	identity.options.HostPort = journal.Source.LocalHostPort
	cold := &generatedDomainConstructionRunner{roster: candidateRunner, delegate: &generatedDomainRecoveryCensusRunner{
		daemonID: "generated-domain-cold-accounting", objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	if stage == generatedRouteColdAccountingLifecycleDraining {
		if identity.options.MemoryBytes <= 1 || identity.options.ReplacementDiskBytes == 0 {
			t.Fatal("interrupted Draining fixture has invalid capacity limits")
		}
		cold.capacity = []byte(fmt.Sprintf("%d %d\n", identity.options.MemoryBytes-1,
			identity.options.ReplacementDiskBytes))
	}
	builder.runner = &generatedDomainTwoComponentRouteRunner{delegate: cold, builder: &builder.physical}
	process := &generatedRouteColdAccountingRunner{cold: builder.runner,
		predecessor: candidateRunner, predecessorPhysical: physicalCandidate}

	return generatedRouteColdAccountingFixture{identity: identity, migration: migration, jobs: jobService,
		process: process, routes: physicalRoutes, predecessor: owner.runner, construction: cold,
		environment: &candidateFactoryEnvironment{path: t.TempDir()}, firstReceipt: currentReceipt,
		componentName: physicalCandidate.policy.identity.Component, componentReceipt: candidateIdentity.ReceiptID,
		lifecycleDeploymentID: lifecycleDeploymentID}
}

func newGeneratedRouteColdAccountingSequentialOwner(t *testing.T, validation *candidateValidationFixture,
	options GeneratedCandidateProcessOptions,
) (*candidateOwnerFixture, *generatedRouteCandidateRunner) {
	t.Helper()
	if validation == nil || validation.migration == nil {
		t.Fatal("cold accounting owner requires prepared validation authority")
	}
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: 2 * uint64(options.MemoryBytes),
		DiskAvailableBytes:   2 * options.ReplacementDiskBytes,
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
