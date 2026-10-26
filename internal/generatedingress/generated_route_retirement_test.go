package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedRouteRecoveredRetirementPhysicalPrefixRefusesForeignAndRecordedReappearance(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("foreign unrecorded configured identity", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedRouteColdAccountingActivatedFixture(t)
			beforeProtection := generatedRouteColdAccountingProtection(t, fixture)
			beforeCommands := fixture.construction.snapshot()
			beforeStop := fixture.predecessor.count(candidateOwnerStop)
			beforeRemove := fixture.predecessor.count(candidateOwnerRemove)
			physical := fixture.process.predecessor.physical[fixture.retiredContainerID]
			physical.policy.identity.ReceiptID = "foreign-recovered-retirement-receipt"
			fixture.process.predecessor.physical[fixture.retiredContainerID] = physical

			result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
				fixture.process, fixture.environment, fixture.identity.options)
			var diagnostic *generatedDomainConstructionDiagnostic
			if result != nil || !errors.As(err, &diagnostic) || diagnostic == nil ||
				diagnostic.stage != generatedDomainConstructionStageColdConstruction ||
				diagnostic.reason != generatedDomainConstructionReasonColdGroupContainerAttestation {
				t.Fatalf("foreign recovered prefix result=%#v diagnostic=%#v error=%v", result, diagnostic, err)
			}
			assertGeneratedRouteColdAccountingActivatedSnapshot(t, fixture)
			assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture)
			if fixture.predecessor.count(candidateOwnerStop) != beforeStop ||
				fixture.predecessor.count(candidateOwnerRemove) != beforeRemove ||
				generatedRouteColdAccountingCapacityProbeCount(fixture.construction.snapshot()) !=
					generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
				t.Fatal("foreign recovered prefix refusal reached effects or capacity")
			}
		})
	})

	t.Run("recorded component reappearance", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedRouteColdAccountingPartialRetirementFixture(t)
			beforeProtection := generatedRouteColdAccountingProtection(t, fixture.base)
			beforeCommands := fixture.base.construction.snapshot()
			beforeEffects := generatedRouteColdAccountingComponentEffectCounts(fixture.runner,
				fixture.predecessor)
			identity := fixture.predecessor[fixture.recordedComponent]
			physical := fixture.runner.physical[identity.ContainerID]
			physical.exists = true
			physical.running = true
			physical.health = "healthy"
			fixture.runner.physical[identity.ContainerID] = physical

			result, err := newGeneratedDomainConstruction(context.Background(), fixture.base.identity.stores,
				fixture.base.process, fixture.base.environment, fixture.base.identity.options)
			var diagnostic *generatedDomainConstructionDiagnostic
			if result != nil || !errors.As(err, &diagnostic) || diagnostic == nil ||
				diagnostic.stage != generatedDomainConstructionStageColdConstruction ||
				diagnostic.reason != generatedDomainConstructionReasonColdGroupComponentAbsence {
				t.Fatalf("recorded reappearance result=%#v diagnostic=%#v error=%v", result, diagnostic, err)
			}
			assertGeneratedRouteColdAccountingProtectionUnchanged(t, beforeProtection, fixture.base)
			if !reflect.DeepEqual(beforeEffects,
				generatedRouteColdAccountingComponentEffectCounts(fixture.runner, fixture.predecessor)) ||
				generatedRouteColdAccountingCapacityProbeCount(fixture.base.construction.snapshot()) !=
					generatedRouteColdAccountingCapacityProbeCount(beforeCommands) {
				t.Fatal("recorded reappearance refusal reached effects or capacity")
			}
		})
	})
}

func TestGeneratedRouteReceiptOwnedPredecessorRetirement(t *testing.T) {
	t.Run("exact activation retires fixed predecessor roster and retains accounting", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		deployment, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		if retirement == nil || deployment.DeploymentID != fixture.successorDeploymentID ||
			deployment.Phase != generatedruntimestate.PhaseDraining {
			t.Fatalf("activation deployment=%#v retirement=%T", deployment, retirement)
		}
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		receipt, err := retirement.Retire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if receipt.AppID != fixture.appID || receipt.SuccessorDeploymentID != fixture.successorDeploymentID ||
			receipt.PredecessorDeploymentID != fixture.predecessorDeploymentID || receipt.ComponentCount != 1 ||
			receipt.ActivationReceiptID == "" || receipt.PredecessorRosterDigest == "" || receipt.RetiredAt.IsZero() {
			t.Fatalf("retirement receipt=%#v", receipt)
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != stopBefore+1 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != removeBefore+1 {
			t.Fatalf("retirement effects stop=%d remove=%d", fixture.candidateRunner.delegate.count(candidateOwnerStop)-stopBefore,
				fixture.candidateRunner.delegate.count(candidateOwnerRemove)-removeBefore)
		}
		assertGeneratedRouteRetirementStored(t, fixture, receipt)
		if replay, err := retirement.Retire(context.Background()); err != nil || replay != receipt {
			t.Fatalf("retirement replay=%#v error=%v", replay, err)
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != stopBefore+1 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != removeBefore+1 {
			t.Fatal("retirement replay repeated an effect")
		}
		retirement.Leave()
		if _, err := retirement.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("closed retirement error=%v", err)
		}
	})

	t.Run("legacy activation cannot later acquire retirement authority", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		if _, err := fixture.successor.Activate(fixture.owner.validation.migration.guarded.context); err != nil {
			t.Fatal(err)
		}
		deployment, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(context.Background())
		if !errors.Is(err, deploymentexecution.ErrScopeUnavailable) || deployment.DeploymentID != "" || retirement != nil {
			t.Fatalf("legacy replay deployment=%#v retirement=%T error=%v", deployment, retirement, err)
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != 0 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != 0 {
			t.Fatal("legacy activation gained predecessor effects")
		}
	})

	t.Run("escaped alias closes with shared retirement lifetime", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		deployment, first, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		replayed, second, err := fixture.successor.ActivateWithPredecessorRetirement(context.Background())
		if err != nil || !reflect.DeepEqual(replayed, deployment) || first == nil || second == nil || first == second || first.state != second.state {
			t.Fatalf("shared activation deployment=%#v/%#v handles=%p/%p error=%v", deployment, replayed, first, second, err)
		}
		first.Leave()
		for _, handle := range []*GeneratedPredecessorRetirement{first, second} {
			if _, err := handle.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
				t.Fatalf("closed alias error=%v", err)
			}
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != 0 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != 0 {
			t.Fatal("closed alias reached predecessor effects")
		}
	})
}

func TestGeneratedRouteAccountingHandoffEmptyPredecessor(t *testing.T) {
	migration, _ := newGeneratedRouteCanonicalMigration(t)
	owner, _ := newGeneratedRouteSequentialCandidateOwnerForMigration(t, migration)
	execution, identity := prepareGeneratedRouteSequentialCandidate(t, owner)

	manager, runner, routeRunner := newGeneratedRoutePredecessorManagerFixture(t)
	runner.appID = migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
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
	if len(sealed.predecessorRoster) != 0 {
		t.Fatalf("initial deployment predecessor roster=%#v", sealed.predecessorRoster)
	}
	_, retirement, err := sealed.ActivateWithPredecessorRetirement(migration.guarded.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(retirement.Leave)
	retired, err := retirement.Retire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if retired.ComponentCount != 0 || retired.PredecessorDeploymentID != "" {
		t.Fatalf("empty predecessor retirement=%#v", retired)
	}
	ledger := owner.authority.ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{
		appID: migration.guarded.appID, deploymentID: migration.request.DeploymentID,
	}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("empty predecessor successor accounting group is absent")
	}
	group.mu.Lock()
	charge := group.byName["api"].charge
	group.mu.Unlock()
	if charge == nil {
		t.Fatal("empty predecessor successor charge is absent")
	}
	lease, epoch := charge.lease, charge.epoch
	receipt, err := retirement.completeAccountingHandoff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PredecessorComponentCount != 0 || receipt.PredecessorDeploymentID != "" ||
		receipt.SuccessorComponentCount != 1 || receipt.SuccessorDeploymentID != migration.request.DeploymentID {
		t.Fatalf("empty predecessor handoff=%#v", receipt)
	}
	if charge.lease != lease || charge.epoch != epoch || charge.owner != candidateAccountingRouteActive ||
		charge.state != candidateChargeActive || charge.logicallyRetired {
		t.Fatalf("empty predecessor successor charge=%+v", charge)
	}
	if replay, err := retirement.completeAccountingHandoff(context.Background()); err != nil || replay != receipt {
		t.Fatalf("empty predecessor replay=%#v error=%v", replay, err)
	}
}

func TestGeneratedRouteReceiptOwnedPredecessorRetirementStickyEntrypoints(t *testing.T) {
	t.Run("retirement activation prevents legacy replay without closing its handle", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil || retirement == nil {
			t.Fatalf("retirement activation handle=%T error=%v", retirement, err)
		}
		if deployment, err := fixture.successor.Activate(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) || deployment.DeploymentID != "" {
			t.Fatalf("legacy replay deployment=%#v error=%v", deployment, err)
		}
		if err := retirement.current(); err != nil {
			t.Fatalf("legacy replay invalidated retirement: %v", err)
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != 0 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != 0 {
			t.Fatal("mixed activation entrypoints reached predecessor effects")
		}
		retirement.Leave()
	})

	t.Run("concurrent retirement entry refuses reentry and preserves one committed handle", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		type activationResult struct {
			deployment generatedruntimestate.Deployment
			retirement *GeneratedPredecessorRetirement
			err        error
		}
		start := make(chan struct{})
		results := make(chan activationResult, 2)
		for index := 0; index < 2; index++ {
			go func() {
				<-start
				deployment, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
					fixture.owner.validation.migration.guarded.context)
				results <- activationResult{deployment: deployment, retirement: retirement, err: err}
			}()
		}
		close(start)
		var committed []*GeneratedPredecessorRetirement
		for index := 0; index < 2; index++ {
			result := <-results
			if result.err == nil {
				if result.retirement == nil || result.deployment.DeploymentID != fixture.successorDeploymentID {
					t.Fatalf("successful activation deployment=%#v retirement=%T", result.deployment, result.retirement)
				}
				committed = append(committed, result.retirement)
				continue
			}
			if !errors.Is(result.err, deploymentexecution.ErrScopeUnavailable) ||
				result.deployment.DeploymentID != "" || result.retirement != nil {
				t.Fatalf("concurrent refusal deployment=%#v retirement=%T error=%v",
					result.deployment, result.retirement, result.err)
			}
		}
		if len(committed) == 0 || len(committed) == 2 && committed[0].state != committed[1].state {
			t.Fatalf("concurrent activation handles=%d", len(committed))
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != 0 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != 0 {
			t.Fatal("concurrent activation entry reached predecessor effects")
		}
		committed[0].Leave()
		for _, handle := range committed {
			if _, err := handle.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
				t.Fatalf("concurrent escaped handle remained live: %v", err)
			}
		}
	})

	t.Run("retirement replay and leave serialize the shared lifetime", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		deployment, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil || retirement == nil {
			t.Fatalf("retirement activation handle=%T error=%v", retirement, err)
		}
		candidateState := retirement.state.execution.candidate.state
		candidateState.mu.Lock()
		candidateLocked := true
		defer func() {
			if candidateLocked {
				candidateState.mu.Unlock()
			}
		}()
		type activationResult struct {
			deployment generatedruntimestate.Deployment
			retirement *GeneratedPredecessorRetirement
			err        error
		}
		replayed := make(chan activationResult, 1)
		go func() {
			got, alias, replayErr := fixture.successor.ActivateWithPredecessorRetirement(context.Background())
			replayed <- activationResult{deployment: got, retirement: alias, err: replayErr}
		}()
		deadline := time.Now().Add(time.Second)
		for {
			retirement.state.mu.Lock()
			active := retirement.state.active
			retirement.state.mu.Unlock()
			if active == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("retirement replay did not borrow shared lifetime")
			}
			runtime.Gosched()
		}
		left := make(chan struct{})
		go func() {
			retirement.Leave()
			close(left)
		}()
		deadline = time.Now().Add(time.Second)
		for {
			retirement.state.mu.Lock()
			closing := retirement.state.closing
			retirement.state.mu.Unlock()
			if closing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("retirement leave did not begin")
			}
			runtime.Gosched()
		}
		select {
		case <-left:
			t.Fatal("retirement leave completed while replay held a lifetime borrow")
		default:
		}
		candidateState.mu.Unlock()
		candidateLocked = false
		result := <-replayed
		<-left
		if result.err != nil || !reflect.DeepEqual(result.deployment, deployment) || result.retirement == nil {
			t.Fatalf("serialized replay deployment=%#v retirement=%T error=%v",
				result.deployment, result.retirement, result.err)
		}
		if _, err := result.retirement.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("replayed alias remained live after leave: %v", err)
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != 0 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != 0 {
			t.Fatal("replay and leave reached predecessor effects")
		}
	})
}

func TestGeneratedRouteReceiptOwnedPredecessorRetirementRefusesPhysicalDrift(t *testing.T) {
	t.Run("successor health drift refuses before predecessor effects", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		successor := fixture.candidateRunner.physical[strings.Repeat("8", 64)]
		successor.health = "unhealthy"
		fixture.candidateRunner.physical[successor.containerID] = successor
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		if _, err := retirement.Retire(context.Background()); err == nil {
			t.Fatal("unhealthy successor authorized retirement")
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != stopBefore ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != removeBefore {
			t.Fatal("unhealthy successor reached predecessor effects")
		}
	})

	t.Run("predecessor reappearance before aggregate refuses receipt", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		predecessorID := fixture.successor.predecessorRoster[0].ContainerID
		predecessorReads := 0
		fixture.candidateRunner.beforeInspect = func(target string) {
			if target != predecessorID {
				return
			}
			predecessorReads++
			if predecessorReads == 4 {
				physical := fixture.candidateRunner.physical[predecessorID]
				physical.exists = true
				physical.running = false
				fixture.candidateRunner.physical[predecessorID] = physical
			}
		}
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		if receipt, err := retirement.Retire(context.Background()); err == nil || receipt.ActivationReceiptID != "" {
			t.Fatalf("reappeared predecessor receipt=%#v error=%v", receipt, err)
		}
		if predecessorReads != 4 || fixture.candidateRunner.delegate.count(candidateOwnerStop) != stopBefore+1 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != removeBefore+1 {
			t.Fatalf("reappearance reads=%d stop=%d remove=%d", predecessorReads,
				fixture.candidateRunner.delegate.count(candidateOwnerStop)-stopBefore,
				fixture.candidateRunner.delegate.count(candidateOwnerRemove)-removeBefore)
		}
		var aggregate int
		if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts`).Scan(&aggregate); err != nil {
			t.Fatal(err)
		}
		if aggregate != 0 {
			t.Fatal("reappeared predecessor retained an aggregate receipt")
		}
	})

	t.Run("predecessor stop refusal retains ownership without remove or receipt", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		refusal := errors.New("retirement stop unavailable")
		fixture.candidateRunner.delegate.setResult(candidateOwnerStop,
			func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				return runtimeprocess.CommandResult{}, refusal
			})
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		_, err = retirement.Retire(context.Background())
		var candidateErr *GeneratedCandidateError
		if !errors.Is(err, refusal) || !errors.As(err, &candidateErr) || candidateErr.Code != "candidate_cleanup_failed" {
			t.Fatalf("stop refusal error=%v", err)
		}
		if fixture.candidateRunner.delegate.count(candidateOwnerStop) != stopBefore+1 ||
			fixture.candidateRunner.delegate.count(candidateOwnerRemove) != removeBefore {
			t.Fatalf("stop refusal effects stop=%d remove=%d",
				fixture.candidateRunner.delegate.count(candidateOwnerStop)-stopBefore,
				fixture.candidateRunner.delegate.count(candidateOwnerRemove)-removeBefore)
		}
		var observations, aggregates int
		if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirements`).Scan(&observations); err != nil {
			t.Fatal(err)
		}
		if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts`).Scan(&aggregates); err != nil {
			t.Fatal(err)
		}
		if observations != 0 || aggregates != 0 {
			t.Fatalf("stop refusal stored observations=%d aggregates=%d", observations, aggregates)
		}
	})
}

func TestGeneratedRouteReceiptOwnedPredecessorRetirementRefusesAuthorityDrift(t *testing.T) {
	t.Run("candidate owner leave invalidates escaped retirement", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		fixture.successor.candidate.Leave()
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		if _, err := retirement.Retire(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("retirement survived candidate owner leave: %v", err)
		}
		assertGeneratedRouteRetirementNoEffects(t, fixture, stopBefore, removeBefore)
	})

	t.Run("pending route graph refuses before predecessor effects", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		state, err := fixture.manager.store.load()
		if err != nil {
			t.Fatal(err)
		}
		previous := state.Active[fixture.appID]
		state.Pending = &pendingRoute{AppID: fixture.appID, Previous: &previous, Proposed: fixture.predecessorRoute}
		if err := fixture.manager.store.save(state); err != nil {
			t.Fatal(err)
		}
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		if _, err := retirement.Retire(context.Background()); err == nil {
			t.Fatal("pending route graph authorized predecessor retirement")
		}
		assertGeneratedRouteRetirementNoEffects(t, fixture, stopBefore, removeBefore)
	})

	t.Run("predecessor configured identity drift refuses before stop", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		predecessorID := fixture.successor.predecessorRoster[0].ContainerID
		physical := fixture.candidateRunner.physical[predecessorID]
		if physical.containerID != predecessorID || physical.policy.identity.Role == "" {
			t.Fatal("predecessor drift control did not select the exact retained physical")
		}
		physical.policy.identity.Role += "-crossed"
		fixture.candidateRunner.physical[predecessorID] = physical
		stopBefore := fixture.candidateRunner.delegate.count(candidateOwnerStop)
		removeBefore := fixture.candidateRunner.delegate.count(candidateOwnerRemove)
		_, err = retirement.Retire(context.Background())
		var candidateErr *GeneratedCandidateError
		if !errors.Is(err, deploymentexecution.ErrIdentityLost) &&
			(!errors.As(err, &candidateErr) || candidateErr.Code != "image_drift_detected") {
			t.Fatalf("crossed predecessor identity error=%v", err)
		}
		assertGeneratedRouteRetirementNoEffects(t, fixture, stopBefore, removeBefore)
	})
}

func assertGeneratedRouteRetirementNoEffects(t *testing.T, fixture *generatedRoutePredecessorFixture,
	stopBefore, removeBefore int,
) {
	t.Helper()
	if fixture.candidateRunner.delegate.count(candidateOwnerStop) != stopBefore ||
		fixture.candidateRunner.delegate.count(candidateOwnerRemove) != removeBefore {
		t.Fatalf("retirement refusal reached predecessor effects: stop=%d remove=%d",
			fixture.candidateRunner.delegate.count(candidateOwnerStop)-stopBefore,
			fixture.candidateRunner.delegate.count(candidateOwnerRemove)-removeBefore)
	}
	var observations, aggregates int
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirements`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts`).Scan(&aggregates); err != nil {
		t.Fatal(err)
	}
	if observations != 0 || aggregates != 0 {
		t.Fatalf("retirement refusal stored observations=%d aggregates=%d", observations, aggregates)
	}
}

func assertGeneratedRouteRetirementStored(t *testing.T, fixture *generatedRoutePredecessorFixture,
	receipt generatedruntimestate.PredecessorRetirementReceipt,
) {
	t.Helper()
	var observations, aggregates int
	var predecessorState generatedruntimestate.ComponentState
	var successorPhase generatedruntimestate.Phase
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receipt.ActivationReceiptID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receipt.ActivationReceiptID).Scan(&aggregates); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database().QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name='api'`, fixture.predecessorDeploymentID).Scan(&predecessorState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database().QueryRow(`SELECT phase FROM generated_runtime_deployments
		WHERE deployment_id=?`, fixture.successorDeploymentID).Scan(&successorPhase); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || aggregates != 1 || predecessorState != generatedruntimestate.ComponentStopped ||
		successorPhase != generatedruntimestate.PhaseDraining {
		t.Fatalf("retirement stored observations=%d aggregates=%d predecessor=%q successor=%q",
			observations, aggregates, predecessorState, successorPhase)
	}
	ledger := fixture.owner.authority.ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.appID, deploymentID: fixture.predecessorDeploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("predecessor accounting group is absent")
	}
	group.mu.Lock()
	component := group.byName["api"]
	retained := component != nil && component.charge != nil && component.charge.state == candidateChargeActive &&
		!component.charge.logicallyRetired && component.receipt != nil &&
		candidatePhysicalFromWord(component.receipt.state.Load()) == candidatePhysicalConfirmedAbsent
	group.mu.Unlock()
	if !retained {
		t.Fatal("predecessor accounting charge was not retained after physical absence")
	}
}
