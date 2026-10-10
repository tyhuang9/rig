package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayCurrentServingPhysicalRuntimeFake struct {
	*gatewayCurrentPhysicalRuntimeFake
	restoreFn func(context.Context, gatewayCurrentPhysicalTarget, gatewayCurrentPhysicalOutcome,
		func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
	restores int
}

func (f *gatewayCurrentServingPhysicalRuntimeFake) restoreServing(ctx context.Context,
	target gatewayCurrentPhysicalTarget, expected gatewayCurrentPhysicalOutcome,
	guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	f.restores++
	if f.restoreFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("serving restore unavailable")
	}
	return f.restoreFn(ctx, target, expected, guard)
}

func TestManagedGatewayCurrentServingRestoreRequiresFullAndExactAuthorityAtProof(t *testing.T) {
	fixture, _, action, target := managedGatewayCurrentServingStableFixture(t)
	physical := false
	authorizationChecks := 0
	runtime := &gatewayCurrentServingPhysicalRuntimeFake{
		gatewayCurrentPhysicalRuntimeFake: &gatewayCurrentPhysicalRuntimeFake{},
	}
	runtime.observeFn = func(_ context.Context, actual gatewayCurrentPhysicalTarget) (
		gatewayCurrentPhysicalAttestation, error,
	) {
		if !physical || !reflect.DeepEqual(actual, target) {
			return gatewayCurrentPhysicalAttestation{}, errors.New("serving target absent")
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, actual,
			gatewayCurrentPhysicalStableServing), nil
	}
	runtime.restoreFn = func(ctx context.Context, actual gatewayCurrentPhysicalTarget,
		expected gatewayCurrentPhysicalOutcome, guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if !reflect.DeepEqual(actual, target) || expected != gatewayCurrentPhysicalStableServing {
			t.Fatal("managed restore changed stable authority")
		}
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		physical = true
		// Exercise the managed lost-ack proof path.
		return gatewayCurrentPhysicalAttestation{}, context.Canceled
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	contract := installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
	proof, err := driver.restoreGatewayCurrentServing(context.Background(), action,
		func(context.Context) error { authorizationChecks++; return nil })
	if err != nil || proof.Outcome != gatewayCurrentPhysicalStableServing || runtime.restores != 1 ||
		runtime.observes != 1 || authorizationChecks < 4 || !reflect.DeepEqual(proof.State, action.Target) || contract.calls == 0 {
		t.Fatalf("lost-ack stable restore: proof=%#v restores=%d observes=%d guards=%d error=%v",
			proof, runtime.restores, runtime.observes, authorizationChecks, err)
	}

	// An exact physical result cannot supersede a changed full authorization.
	physical, authorizationChecks = false, 0
	authorized := true
	runtime.restoreFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		_ gatewayCurrentPhysicalOutcome, guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		physical, authorized = true, false
		return gatewayCurrentPhysicalDriverTestAttestation(t, target,
			gatewayCurrentPhysicalStableServing), nil
	}
	if _, err := driver.restoreGatewayCurrentServing(context.Background(), action,
		func(context.Context) error {
			authorizationChecks++
			if !authorized {
				return errors.New("SQL/runtime authorization changed")
			}
			return nil
		}); err == nil || runtime.restores != 2 || authorizationChecks < 3 {
		t.Fatalf("post-proof authorization drift accepted: restores=%d guards=%d error=%v",
			runtime.restores, authorizationChecks, err)
	}

	// The full authorization may remain byte-identical while the protected
	// selected route bundle changes. The managed exact-action guard must still
	// reject the old physical proof.
	physical, authorized = false, true
	runtime.restoreFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		_ gatewayCurrentPhysicalOutcome, guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		physical = true
		installed, loadErr := fixture.store.load()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		appID, app := routeOperationTransferredApp(t, installed)
		request := routeOperationSwitchRequest(t, appID, app.Route)
		transition, changeErr := gatewayCurrentSwitchTransition(installed, request)
		if changeErr != nil {
			t.Fatal(changeErr)
		}
		changed := cloneGatewayCurrentRouteState(transition.Effective)
		changed.Revision = installed.Revision + 1
		changed.Digest, changeErr = gatewayCurrentRouteStateDigest(changed)
		if changeErr != nil {
			t.Fatal(changeErr)
		}
		if err := fixture.store.saveNext(installed, changed); err != nil {
			t.Fatalf("install protected selection drift: %v", err)
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, target,
			gatewayCurrentPhysicalStableServing), nil
	}
	if _, err := driver.restoreGatewayCurrentServing(context.Background(), action,
		func(context.Context) error { return nil }); err == nil || runtime.restores != 3 {
		t.Fatalf("post-proof selected action drift accepted: restores=%d error=%v", runtime.restores, err)
	}

	// Changed authorization bytes without their exact action digest never reach
	// the runtime callback.
	tamperFixture, _, tampered, _ := managedGatewayCurrentServingStableFixture(t)
	tampered.AuthorizationDigest = strings.Repeat("b", 64)
	tamperDriver := driver
	tamperDriver.managerGatewayCurrentPhysicalDriver.manager = tamperFixture.manager
	if _, err := tamperDriver.restoreGatewayCurrentServing(context.Background(), tampered,
		func(context.Context) error { return nil }); err == nil || runtime.restores != 3 {
		t.Fatalf("cross-authorization action reached runtime: restores=%d error=%v", runtime.restores, err)
	}
}

func TestManagedGatewayCurrentServingRestoreDriverRefusesPinnedPredecessorRetirementObservation(t *testing.T) {
	_, runner, contract, selection, _ := gatewayCurrentPredecessorRetirementContractFixture(t)
	action, err := gatewayCurrentServingRestoreActionForSelection(selection, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	contract.refuse = true
	before := gatewayCurrentPhysicalExecutorDigest(t, runner)
	if _, err := contract.restoreGatewayCurrentServing(context.Background(), action,
		func(context.Context) error { return nil }); err == nil ||
		contract.calls != 1 || gatewayCurrentPhysicalExecutorDigest(t, runner) != before {
		t.Fatalf("configured retirement observation refusal reached current runtime: calls=%d effects=%v err=%v", contract.calls, runner.effects, err)
	}
	contract.refuse = false
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := contract.restoreGatewayCurrentServing(cancelled, action,
		func(context.Context) error { return nil }); err == nil || contract.calls != 1 ||
		gatewayCurrentPhysicalExecutorDigest(t, runner) != before {
		t.Fatalf("cancelled restore reached retirement or current runtime: calls=%d effects=%v err=%v", contract.calls, runner.effects, err)
	}
	if err := contract.observeGatewayCurrentPredecessorsRetired(cancelled, contract.expected); err == nil || contract.calls != 2 ||
		gatewayCurrentPhysicalExecutorDigest(t, runner) != before {
		t.Fatalf("cancelled retirement callback was accepted: calls=%d err=%v", contract.calls, err)
	}
	if err := contract.observeGatewayCurrentPredecessorsRetired(nil, contract.expected); err == nil || contract.calls != 3 ||
		gatewayCurrentPhysicalExecutorDigest(t, runner) != before {
		t.Fatalf("nil-context retirement callback was accepted: calls=%d err=%v", contract.calls, err)
	}
}

func TestManagedGatewayCurrentServingRestoreRuntimeAcceptsExactRunningOrStoppedOnly(t *testing.T) {
	t.Run("exact running is idempotent", func(t *testing.T) {
		fixture, _, action, target := managedGatewayCurrentServingStableFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		contract := installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		proof, err := driver.restoreGatewayCurrentServing(context.Background(), action,
			func(context.Context) error { return nil })
		if err != nil || proof.Outcome != gatewayCurrentPhysicalStableServing || len(runner.effects) != 0 ||
			!reflect.DeepEqual(proof.State, action.Target) || contract.calls == 0 {
			t.Fatalf("idempotent running restore: proof=%#v effects=%v error=%v", proof, runner.effects, err)
		}
	})

	t.Run("inexact running refuses without effects", func(t *testing.T) {
		fixture, _, action, target := managedGatewayCurrentServingStableFixture(t)
		appID, app := routeOperationTransferredApp(t, action.Target)
		request := routeOperationSwitchRequest(t, appID, app.Route)
		for index := range request.Endpoints {
			request.Endpoints[index].NetworkName = "rig-restore-" + strings.Repeat("d", 12)
			digest := sha256.Sum256([]byte(request.Endpoints[index].ContainerID + "\x00restore-inexact"))
			request.Endpoints[index].ContainerID = hex.EncodeToString(digest[:])
			request.Endpoints[index].NetworkAlias += "-restore"
		}
		transition, err := gatewayCurrentSwitchTransition(action.Target, request)
		if err != nil {
			t.Fatal(err)
		}
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Effective,
			transition.Effective, localPort)
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		if _, err := driver.restoreGatewayCurrentServing(context.Background(), action,
			func(context.Context) error { return nil }); err == nil || len(runner.effects) != 0 {
			t.Fatalf("inexact running topology reached effects: effects=%v error=%v", runner.effects, err)
		}
	})

	t.Run("stopped exact config resolves lost start acknowledgement", func(t *testing.T) {
		fixture, _, action, target := managedGatewayCurrentServingStableFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
		runner.lostStartAck = true
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		contract := installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		proof, err := driver.restoreGatewayCurrentServing(context.Background(), action,
			func(context.Context) error { return nil })
		start := []string{"container", "start", target.Resources.FinalContainer.ID}
		if err != nil || proof.Outcome != gatewayCurrentPhysicalStableServing || !runner.container.Running ||
			!containsGatewayCurrentPhysicalEffect(runner.effects, start) || contract.calls == 0 {
			t.Fatalf("stopped lost-ack restore: proof=%#v effects=%v error=%v", proof, runner.effects, err)
		}
	})
}

func TestManagedGatewayCurrentServingRestoreRuntimeCompletedBatchStartsOnlyUnderFreshAuthority(t *testing.T) {
	t.Run("exact stopped target starts under every guard", func(t *testing.T) {
		fixture, action, target := managedGatewayCurrentServingCompletedBatchFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
		runner.lostStartAck = true
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		contract := installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		guardsByEffects := make(map[int]int)
		proof, err := driver.restoreGatewayCurrentServing(context.Background(), action,
			func(context.Context) error { guardsByEffects[len(runner.effects)]++; return nil })
		start := []string{"container", "start", target.Resources.FinalContainer.ID}
		if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryMixed ||
			!reflect.DeepEqual(proof.LANRecovery, action.Selected.LANRecovery) ||
			!sameCaddyConfig(runner.files[target.Identity.Rebind.ActiveConfigFilename],
				mustGatewayCurrentPhysicalConfig(t, action.Target)) ||
			!containsGatewayCurrentPhysicalEffect(runner.effects, start) ||
			guardsByEffects[0] < 4 || guardsByEffects[len(runner.effects)] < 2 || contract.calls == 0 {
			t.Fatalf("completed-batch restore: proof=%#v effects=%v guards=%v error=%v",
				proof, runner.effects, guardsByEffects, err)
		}
	})

	t.Run("authorization refusal immediately before start prevents effects", func(t *testing.T) {
		fixture, action, target := managedGatewayCurrentServingCompletedBatchFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		checks := 0
		authorize := func(context.Context) error {
			checks++
			if checks == 4 {
				return errors.New("authorization changed before start")
			}
			return nil
		}
		if _, err := driver.restoreGatewayCurrentServing(context.Background(), action, authorize); err == nil ||
			len(runner.effects) != 0 || runner.container.Running {
			t.Fatalf("authorization refusal reached start: checks=%d effects=%v running=%t error=%v",
				checks, runner.effects, runner.container.Running, err)
		}
	})

	t.Run("authorization drift after start rejects exact proof", func(t *testing.T) {
		fixture, action, target := managedGatewayCurrentServingCompletedBatchFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		authorize := func(context.Context) error {
			if runner.container.Running {
				return errors.New("authorization changed after start")
			}
			return nil
		}
		if _, err := driver.restoreGatewayCurrentServing(context.Background(), action, authorize); err == nil ||
			len(runner.effects) != 1 || !runner.container.Running {
			t.Fatalf("post-start authorization drift accepted: effects=%v running=%t error=%v",
				runner.effects, runner.container.Running, err)
		}
	})
}

func TestManagedGatewayCurrentServingRestoreRuntimeRechecksAuthorityAfterFinalStoppedInventory(t *testing.T) {
	t.Run("positive baseline", func(t *testing.T) {
		fixture, _, action, target := managedGatewayCurrentServingStableFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		runner := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		runner.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
		driver := managedGatewayCurrentServingExecutorDriver(fixture, runner, localPort)
		contract := installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		if _, err := driver.restoreGatewayCurrentServing(context.Background(), action,
			func(context.Context) error { return nil }); err != nil || !runner.container.Running || contract.calls == 0 {
			t.Fatalf("positive stopped restore: effects=%v error=%v", runner.effects, err)
		}
	})

	t.Run("late inventory drift prevents start", func(t *testing.T) {
		fixture, _, action, target := managedGatewayCurrentServingStableFixture(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		base := newGatewayCurrentPhysicalExecutor(t, target, action.Target, action.Target, localPort)
		base.stopAt(mustGatewayCurrentPhysicalConfig(t, action.Target))
		authorized, arm := true, false
		runner := &gatewayCurrentServingLateInventoryExecutor{gatewayCurrentPhysicalExecutor: base,
			authorized: &authorized, armed: &arm}
		driver := managedGatewayCurrentServingExecutorDriver(fixture, base, localPort)
		installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, driver)
		fixture.manager.runner = runner
		checks := 0
		authorize := func(context.Context) error {
			checks++
			if checks == 4 {
				arm = true
			}
			if !authorized {
				return errors.New("authorization changed during final stopped inventory")
			}
			return nil
		}
		if _, err := driver.restoreGatewayCurrentServing(context.Background(), action, authorize); err == nil ||
			len(base.effects) != 0 || base.container.Running || authorized || arm {
			t.Fatalf("late inventory drift reached start: checks=%d effects=%v running=%t authorized=%t armed=%t error=%v",
				checks, base.effects, base.container.Running, authorized, arm, err)
		}
	})
}

type gatewayCurrentServingLateInventoryExecutor struct {
	*gatewayCurrentPhysicalExecutor
	authorized *bool
	armed      *bool
}

func (r *gatewayCurrentServingLateInventoryExecutor) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	result, err := r.gatewayCurrentPhysicalExecutor.Run(ctx, request)
	args := request.Args
	if r.authorized != nil && r.armed != nil && *r.armed && len(args) >= 3 &&
		args[0] == "container" && args[1] == "inspect" &&
		normalizeID(args[len(args)-1]) != r.target.Resources.FinalContainer.ID {
		*r.authorized, *r.armed = false, false
	}
	return result, err
}

func managedGatewayCurrentServingStableFixture(t *testing.T) (gatewayCurrentStateFixture,
	gatewayCurrentSelection, gatewayCurrentServingRestoreAction, gatewayCurrentPhysicalTarget,
) {
	t.Helper()
	fixture, _, _ := gatewayCurrentPhysicalExecutorTransition(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		return snapshot, nil
	})
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: state.Lineage,
		Receipt: &fixture.receipt, State: &state}
	action, err := gatewayCurrentServingRestoreActionForSelection(selection, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, action.Target, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, selection, action, target
}

func managedGatewayCurrentServingCompletedBatchFixture(t *testing.T) (gatewayCurrentStateFixture,
	gatewayCurrentServingRestoreAction, gatewayCurrentPhysicalTarget,
) {
	t.Helper()
	fixture, selection, _, _ := managedGatewayCurrentLANRecoveryDriverFixture(t)
	state := cloneGatewayCurrentRouteState(*selection.State)
	for state.LANRecovery.Head < len(state.LANRecovery.Items) {
		cleared, err := gatewayCurrentLANRecoveryClearedHeadState(state)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cleared, state) {
			if err := fixture.store.saveNext(state, cleared); err != nil {
				t.Fatal(err)
			}
			state = cleared
		}
		advanced, err := gatewayCurrentLANRecoveryAdvanceHeadState(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.saveNext(state, advanced); err != nil {
			t.Fatalf("persist completed batch head: %v", err)
		}
		state = advanced
	}
	selection.State = &state
	action, err := gatewayCurrentServingRestoreActionForSelection(selection, strings.Repeat("c", 64))
	if err != nil || action.Mode != gatewayCurrentServingRestoreCompletedBatch {
		t.Fatalf("completed restore action=%#v error=%v", action, err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, action.Target, nil, action.Selected.LANRecovery)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, action, target
}

func managedGatewayCurrentServingExecutorDriver(fixture gatewayCurrentStateFixture,
	runner *gatewayCurrentPhysicalExecutor, localPort uint16,
) managedGatewayCurrentPhysicalDriver {
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtime := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe: func(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
			if !runner.container.Running {
				return gatewayV2HostProbeResult{}
			}
			return gatewayCurrentPhysicalExecutorHostProbe(localPort)(ctx, address, port, host, path)
		},
		containerProbe: func(context.Context, string, string, uint16, string, string) bool {
			return runner.container.Running
		},
	}
	return managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
}
