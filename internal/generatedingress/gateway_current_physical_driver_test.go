package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentPhysicalRuntimeFake struct {
	observeFn              func(context.Context, gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
	reconcileFn            func(context.Context, gatewayCurrentPhysicalTarget, func(context.Context) error) error
	stopFn                 func(context.Context, []gatewayCurrentPhysicalTarget, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
	stopPredecessorFn      func(context.Context, gatewayFinalOwnershipFacts, func(context.Context) error) error
	stopNativeFn           func(context.Context, gatewayCurrentNativePredecessorRetirement, func(context.Context) error) error
	profileSafeFn          func(context.Context, gatewayCurrentPredecessorRetirementAction, gatewayProfileBinding, func(context.Context) error) bool
	localSafeFn            func(context.Context, gatewayCurrentPredecessorRetirementAction, uint16, func(context.Context) error) bool
	inventoriesStoppedFn   func(context.Context, gatewayCurrentPredecessorRetirementAction) bool
	observes               int
	reconciles             int
	stops                  int
	retirementObservations int
}

func (f *gatewayCurrentPhysicalRuntimeFake) stopPredecessorOwned(ctx context.Context, facts gatewayFinalOwnershipFacts,
	guard func(context.Context) error,
) error {
	if f.stopPredecessorFn != nil {
		return f.stopPredecessorFn(ctx, facts, guard)
	}
	return guard(ctx)
}
func (f *gatewayCurrentPhysicalRuntimeFake) stopNativePredecessorOwned(ctx context.Context,
	target gatewayCurrentNativePredecessorRetirement, guard func(context.Context) error,
) error {
	if f.stopNativeFn != nil {
		return f.stopNativeFn(ctx, target, guard)
	}
	return guard(ctx)
}
func (f *gatewayCurrentPhysicalRuntimeFake) retiredProfileListenersSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, profile gatewayProfileBinding, guard func(context.Context) error,
) bool {
	if f.profileSafeFn != nil {
		return f.profileSafeFn(ctx, action, profile, guard)
	}
	return validGatewayFinalOwnershipFacts(action.CurrentFacts)
}
func (f *gatewayCurrentPhysicalRuntimeFake) retiredLocalListenerSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, port uint16, guard func(context.Context) error,
) bool {
	if f.localSafeFn != nil {
		return f.localSafeFn(ctx, action, port, guard)
	}
	return validGatewayFinalOwnershipFacts(action.CurrentFacts)
}
func (f *gatewayCurrentPhysicalRuntimeFake) predecessorInventoriesStopped(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction,
) bool {
	f.retirementObservations++
	if f.inventoriesStoppedFn != nil {
		return f.inventoriesStoppedFn(ctx, action)
	}
	return validGatewayFinalOwnershipFacts(action.CurrentFacts) && validGatewayV2RouteState(action.Native.State)
}

func (f *gatewayCurrentPhysicalRuntimeFake) observe(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	f.observes++
	if f.observeFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("physical state unavailable")
	}
	return f.observeFn(ctx, target)
}

func (f *gatewayCurrentPhysicalRuntimeFake) reconcile(ctx context.Context,
	target gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) error {
	f.reconciles++
	if f.reconcileFn == nil {
		return errors.New("physical reconcile unavailable")
	}
	return f.reconcileFn(ctx, target, guard)
}

func (f *gatewayCurrentPhysicalRuntimeFake) stop(ctx context.Context,
	targets []gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	f.stops++
	if f.stopFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("physical stop unavailable")
	}
	return f.stopFn(ctx, targets, guard)
}

type gatewayCurrentSnapshotFunc func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)

func (f gatewayCurrentSnapshotFunc) GatewayRebindRecoverySnapshot(ctx context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	return f(ctx)
}

// gatewayCurrentPredecessorRetirementContractDriver is a narrow coordinator
// contract decorator. It pins the real SQL/protected selection once, leaves
// the concrete current executor in place, and refuses any later substitute.
type gatewayCurrentPredecessorRetirementContractDriver struct {
	managedGatewayCurrentPhysicalDriver
	t        *testing.T
	expected gatewayCurrentPredecessorRetirementAction
	calls    int
	refuse   bool
}

func (d *gatewayCurrentPredecessorRetirementContractDriver) observeGatewayCurrentPredecessorsRetired(
	ctx context.Context, action gatewayCurrentPredecessorRetirementAction,
) error {
	d.calls++
	if ctx == nil || ctx.Err() != nil || d.t == nil || !gatewayCurrentPinnedPredecessorRetirementActionValid(d.expected) ||
		!reflect.DeepEqual(action, d.expected) || d.refuse {
		return errors.New("pinned predecessor retirement observation refused")
	}
	return nil
}

func gatewayCurrentPinnedPredecessorRetirementActionValid(action gatewayCurrentPredecessorRetirementAction) bool {
	if !validGatewayFinalOwnershipFacts(action.CurrentFacts) || !validGatewayV2RouteState(action.Native.State) ||
		action.CurrentFinalID == "" || action.CurrentFacts.Terminal.Resources.FinalContainer == nil ||
		action.CurrentFinalID != action.CurrentFacts.Terminal.Resources.FinalContainer.ID ||
		action.CurrentLineage != action.CurrentState.Lineage || action.CurrentFacts.Lineage != action.CurrentLineage ||
		action.Native.Journal.Resources.FinalContainerID == "" ||
		normalizeID(action.Native.Journal.Resources.FinalContainerID) == normalizeID(action.CurrentFinalID) ||
		len(action.CurrentTargets) == 0 {
		return false
	}
	for _, target := range action.CurrentTargets {
		if !validGatewayCurrentPhysicalTarget(target) || target.Lineage != action.CurrentLineage ||
			!gatewayRebindAttemptTerminalViewSame(target.Terminal, action.CurrentFacts.Terminal) ||
			target.Resources.FinalContainer == nil || target.Resources.FinalContainer.ID != action.CurrentFinalID {
			return false
		}
	}
	for _, predecessor := range action.Predecessors {
		if !validGatewayFinalOwnershipFacts(predecessor.Facts) || predecessor.Facts.Terminal.Resources.FinalContainer == nil ||
			normalizeID(predecessor.Facts.Terminal.Resources.FinalContainer.ID) == normalizeID(action.CurrentFinalID) {
			return false
		}
	}
	return true
}

func installGatewayCurrentPredecessorRetirementContract(t *testing.T, manager *Manager,
	base managedGatewayCurrentPhysicalDriver,
) *gatewayCurrentPredecessorRetirementContractDriver {
	t.Helper()
	if manager == nil || base.manager() != manager {
		t.Fatal("predecessor retirement contract has a different manager")
	}
	selection, _, err := manager.readGatewayCurrentSelectionLocked(context.Background())
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil {
		t.Fatalf("predecessor retirement contract selection=%#v err=%v", selection, err)
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil || !gatewayCurrentPinnedPredecessorRetirementActionValid(action) ||
		action.CurrentLineage != selection.Lineage || !reflect.DeepEqual(action.CurrentState, *selection.State) {
		t.Fatalf("predecessor retirement contract action=%#v err=%v", action, err)
	}
	contract := &gatewayCurrentPredecessorRetirementContractDriver{
		managedGatewayCurrentPhysicalDriver: base,
		t:                                   t,
		expected:                            action,
	}
	manager.gatewayCurrentPhysicalDriver = contract
	return contract
}

func gatewayCurrentPhysicalExecutorDigest(t *testing.T, runner *gatewayCurrentPhysicalExecutor) string {
	t.Helper()
	if runner == nil {
		t.Fatal("missing current physical executor")
	}
	value := struct {
		Container        caddyInspection                   `json:"container"`
		Runtime          gatewayContainerRuntime           `json:"runtime"`
		Ingress          caddyNetworkInspection            `json:"ingress"`
		Networks         map[string]caddyNetworkInspection `json:"networks"`
		NetworkIDs       map[string]string                 `json:"networkIDs"`
		Endpoints        map[string]endpointInspection     `json:"endpoints"`
		Files            map[string][]byte                 `json:"files"`
		Live             []byte                            `json:"live"`
		OwnedContainers  []string                          `json:"ownedContainers"`
		Requests         [][]string                        `json:"requests"`
		Effects          [][]string                        `json:"effects"`
		ContainerPresent bool                              `json:"containerPresent"`
		LostReloadAck    bool                              `json:"lostReloadAck"`
		LostStartAck     bool                              `json:"lostStartAck"`
		LostStopAck      bool                              `json:"lostStopAck"`
		LocalHostPort    uint16                            `json:"localHostPort"`
	}{
		Container: runner.container, Runtime: runner.containerRuntime, Ingress: runner.ingress,
		Networks: runner.networks, NetworkIDs: runner.networkIDs, Endpoints: runner.endpoints,
		Files: runner.files, Live: runner.live, OwnedContainers: runner.ownedContainers,
		Requests: runner.requests, Effects: runner.effects, ContainerPresent: runner.containerPresent,
		LostReloadAck: runner.lostReloadAck, LostStartAck: runner.lostStartAck,
		LostStopAck: runner.lostStopAck, LocalHostPort: runner.localHostPort,
	}
	digest, err := canonicalDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func gatewayCurrentPredecessorRetirementContractFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	*gatewayCurrentPhysicalExecutor, *gatewayCurrentPredecessorRetirementContractDriver,
	gatewayCurrentSelection, appaccess.GatewayRebindRecoverySnapshot,
) {
	t.Helper()
	fixture, _, _ := gatewayCurrentServingRestoreSQLFixture(t)
	selection, snapshot, err := fixture.manager.readGatewayCurrentSelectionLocked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil || !gatewayCurrentPinnedPredecessorRetirementActionValid(action) {
		t.Fatalf("fixture predecessor retirement action=%#v err=%v", action, err)
	}
	port := action.CurrentFacts.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
	base := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: fixture.manager}, runner, port)
	contract := installGatewayCurrentPredecessorRetirementContract(t, fixture.manager, base)
	if !reflect.DeepEqual(contract.expected, action) {
		t.Fatal("contract action changed while installing current executor")
	}
	return fixture, runner, contract, selection, snapshot
}

func TestManagedGatewayCurrentPhysicalDriverReconcilesOnlyExactSelectedMarker(t *testing.T) {
	fixture, transition, snapshot := managedGatewayCurrentPhysicalTransitionFixture(t)
	physical := false
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.observeFn = func(_ context.Context, target gatewayCurrentPhysicalTarget) (
		gatewayCurrentPhysicalAttestation, error,
	) {
		if !physical {
			return gatewayCurrentPhysicalAttestation{}, errors.New("before topology")
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, target,
			gatewayCurrentPhysicalRecoveryEffective), nil
	}
	runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) error {
		if err := guard(ctx); err != nil {
			return err
		}
		physical = true
		return guard(ctx)
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	proof, err := driver.applyGatewayCurrentPhysical(context.Background(), transition)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryEffective ||
		!reflect.DeepEqual(proof.State, transition.Effective) || runtime.reconciles != 1 || runtime.observes != 2 {
		t.Fatalf("exact selected-current apply failed: outcome=%s reconciles=%d observes=%d error=%v",
			proof.Outcome, runtime.reconciles, runtime.observes, err)
	}

	drifted := false
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		value := snapshot
		if drifted {
			value.RollbackAllowed = true
		}
		return value, nil
	})
	physical = false
	runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) error {
		if err := guard(ctx); err != nil {
			return err
		}
		physical, drifted = true, true
		return nil
	}
	if _, err := driver.applyGatewayCurrentPhysical(context.Background(), transition); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("SQL drift after physical proof was accepted: %v", err)
	}
}

func TestGatewayCurrentPredecessorSharedListenerAcceptsExactSelectedMarkerProjection(t *testing.T) {
	fixture, transition, _ := managedGatewayCurrentPhysicalTransitionFixture(t)
	ctx := context.Background()
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: fixture.baseline.Lineage,
		Receipt: &fixture.receipt, State: &transition.Pending}
	targets, err := gatewayCurrentPhysicalTargetsForSelection(selection)
	if err != nil || len(targets) != 2 || targets[1].Pending == nil {
		t.Fatalf("pending marker targets=%#v err=%v", targets, err)
	}
	target := targets[1]
	port := gatewayRebindRetainedHandoverLocalPort(fixture.history.Progress, fixture.receipt.Generation, fixture.receipt.OperationID)
	if port == 0 {
		t.Fatal("missing retained handover local port")
	}
	runner := newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State, port)
	managed := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: fixture.manager}, runner, port)
	fixture.manager.gatewayCurrentPhysicalDriver = managed
	physical, ok := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	if !ok {
		t.Fatalf("managed runtime=%T", managed.runtime)
	}
	fixture.manager.gatewayRebindV2NetworkObserver = func(context.Context, appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{Candidates: []gatewayRebindSuccessorNetworkCandidate{{
			InterfaceID: target.State.Profile.InterfaceID, IPv4: target.State.Profile.SelectedIPv4,
		}}}, nil
	}
	action := gatewayCurrentPredecessorRetirementAction{
		CurrentState: transition.Pending, CurrentTargets: targets,
		CurrentFacts: gatewayCurrentFinalOwnershipFacts(target, port),
	}
	guard := func(context.Context) error { return nil }
	shared := target.State.Profile
	proof, observeErr := physical.observe(ctx, target)
	if observeErr != nil {
		t.Fatalf("exact pending marker observation failed: error=%v valid_target=%t",
			observeErr, validGatewayCurrentPhysicalTarget(target))
	}
	if atTarget, matches := gatewayCurrentPhysicalAttestationAtTarget(proof, target),
		gatewayCurrentPhysicalAttestationMatchesSelection(proof, action.CurrentState); !atTarget || !matches {
		t.Fatalf("exact pending marker observation mismatch: outcome=%s at_target=%t matches_selection=%t state_equal=%t pending_equal=%t",
			proof.Outcome, atTarget, matches, reflect.DeepEqual(proof.State, target.State), reflect.DeepEqual(proof.Pending, target.Pending))
	}
	if !physical.exactCurrentServing(ctx, action, guard) {
		t.Fatal("exact pending marker did not authorize current serving")
	}
	if !physical.retiredProfileListenersSafe(ctx, action, shared, guard) {
		t.Fatal("exact pending marker did not authorize the shared profile listener")
	}
	if !physical.retiredLocalListenerSafe(ctx, action, action.CurrentFacts.LocalHostPort, guard) {
		t.Fatal("exact pending marker did not authorize the shared local listener")
	}
	foreign := action
	foreign.CurrentState = cloneGatewayCurrentRouteState(action.CurrentState)
	foreign.CurrentState.Revision++
	foreign.CurrentState.Digest, _ = gatewayCurrentRouteStateDigest(foreign.CurrentState)
	if physical.exactCurrentServing(ctx, foreign, guard) ||
		physical.retiredProfileListenersSafe(ctx, foreign, shared, guard) ||
		physical.retiredLocalListenerSafe(ctx, foreign, action.CurrentFacts.LocalHostPort, guard) {
		t.Fatal("foreign pending marker authorized a shared retired listener")
	}
	runner.stopAt(runner.files[target.Identity.Rebind.ActiveConfigFilename])
	if physical.exactCurrentServing(ctx, action, guard) {
		t.Fatal("stopped pending marker authorized a shared retired listener")
	}
}

func TestManagerGatewayCurrentPhysicalRuntimeStopsExactRetirementTarget(t *testing.T) {
	f, selection, _ := gatewayCurrentTerminalStopFixture(t)
	ctx := context.Background()
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil {
		t.Fatal(err)
	}
	target := action.CurrentTargets[0]
	var runner *gatewayCurrentPhysicalExecutor
	var physical managerGatewayCurrentPhysicalRuntime
	reset := func() {
		runner = newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, action.CurrentFacts.LocalHostPort)
		managed := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, action.CurrentFacts.LocalHostPort)
		var ok bool
		physical, ok = managed.runtime.(managerGatewayCurrentPhysicalRuntime)
		if !ok {
			t.Fatalf("runtime=%T", managed.runtime)
		}
		f.manager.gatewayCurrentPhysicalDriver = managed
	}
	guard := func(context.Context) error { return nil }
	for _, test := range []struct {
		name            string
		mutate          func()
		wantEffects     int
		wantFailure     bool
		retainAfterStop func()
	}{
		{name: "running", wantEffects: 1},
		{name: "restarting", mutate: func() { runner.container.Running, runner.container.Restarting = false, true }, wantEffects: 1},
		{name: "paused", mutate: func() { runner.container.Running, runner.containerRuntime.Paused = false, true }, wantEffects: 1, wantFailure: true,
			retainAfterStop: func() { runner.containerRuntime.Paused = true }},
		{name: "dead", mutate: func() { runner.container.Running, runner.containerRuntime.Dead = false, true }, wantEffects: 1, wantFailure: true,
			retainAfterStop: func() { runner.containerRuntime.Dead = true }},
		{name: "effective port only", mutate: func() { runner.container.Running = false }, wantEffects: 1},
		{name: "already stopped", mutate: func() {
			runner.container.Running = false
			runner.containerRuntime.EffectivePortBindings = map[string][]map[string]string{}
		}},
		{name: "absent", mutate: func() { runner.containerPresent, runner.ownedContainers, runner.container.Running = false, nil, false }},
		{name: "crossed owner", mutate: func() {
			runner.container.Labels[gatewayRebindIntentDigestLabelKey] = strings.Repeat("f", 64)
		}, wantFailure: true},
		{name: "lost acknowledgement", mutate: func() { runner.lostStopAck = true }, wantEffects: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			reset()
			if test.mutate != nil {
				test.mutate()
			}
			if test.retainAfterStop != nil {
				f.manager.runner = &gatewayCurrentTerminalStopHookExecutor{
					gatewayCurrentPhysicalExecutor: runner,
					hook: func(args []string) {
						if reflect.DeepEqual(args, []string{"container", "stop", "--time", "10", target.Resources.FinalContainer.ID}) {
							test.retainAfterStop()
						}
					},
				}
			}
			err := physical.stopPredecessorOwned(ctx, action.CurrentFacts, guard)
			if (err != nil) != test.wantFailure || len(runner.effects) != test.wantEffects {
				t.Fatalf("stop result: err=%v effects=%v", err, runner.effects)
			}
			if test.wantEffects == 1 && !reflect.DeepEqual(runner.effects[0], []string{"container", "stop", "--time", "10", target.Resources.FinalContainer.ID}) {
				t.Fatalf("unexpected retirement command: %v", runner.effects)
			}
			if !test.wantFailure && runner.containerPresent && (runner.container.Running || runner.container.Restarting ||
				runner.containerRuntime.Paused || runner.containerRuntime.Dead ||
				gatewayV2HasEffectivePortBinding(runner.containerRuntime.EffectivePortBindings)) {
				t.Fatal("exact retirement target remained live")
			}
		})
	}
	reset()
	if err := physical.stopPredecessorOwned(ctx, action.CurrentFacts, func(context.Context) error { return context.Canceled }); err == nil || len(runner.effects) != 0 {
		t.Fatalf("retirement guard reached stop: effects=%v err=%v", runner.effects, err)
	}
}

func TestManagedGatewayCurrentPhysicalDriverReconcilesLostAcknowledgement(t *testing.T) {
	fixture, transition, _ := managedGatewayCurrentPhysicalTransitionFixture(t)
	physical := false
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.observeFn = func(_ context.Context, target gatewayCurrentPhysicalTarget) (
		gatewayCurrentPhysicalAttestation, error,
	) {
		if !physical {
			return gatewayCurrentPhysicalAttestation{}, errors.New("before topology")
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, target,
			gatewayCurrentPhysicalRecoveryEffective), nil
	}
	runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) error {
		if err := guard(ctx); err != nil {
			return err
		}
		physical = true
		return context.Canceled
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	proof, err := driver.applyGatewayCurrentPhysical(context.Background(), transition)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryEffective || runtime.reconciles != 1 ||
		runtime.observes != 2 {
		t.Fatalf("lost physical acknowledgement was not reconciled: proof=%#v error=%v", proof, err)
	}
}

func TestManagedGatewayCurrentPhysicalDriverStopUsesProtectedAuthorityAcrossLatchAndSQLFailure(t *testing.T) {
	fixture, transition, _ := managedGatewayCurrentPhysicalTransitionFixture(t)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	fixture.manager.gatewayRebindFailStop.Store(true)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
	})
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.stopFn = func(ctx context.Context, targets []gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if len(targets) != 2 || targets[0].Resources.FinalContainer == nil ||
			targets[0].Resources.FinalContainer.ID != fixture.receipt.Resources.FinalContainer.ID {
			return gatewayCurrentPhysicalAttestation{}, errors.New("wrong owned-stop target")
		}
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, targets[0],
			gatewayCurrentPhysicalRecoveryStopped), nil
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	proof, err := driver.stopGatewayCurrentPhysical(context.Background(), transition)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped || runtime.stops != 1 {
		t.Fatalf("latched protected-only withdrawal failed: stops=%d proof=%#v error=%v",
			runtime.stops, proof, err)
	}

	changed := transition.Pending
	changed.Pending = cloneGatewayCurrentPendingRoute(changed.Pending)
	changed.Pending.Proposed.Route.Endpoints[0].ContainerID = "sha256:" + changed.Pending.Proposed.Route.Endpoints[0].ContainerID
	changed.Digest = ""
	changed.Digest, _ = gatewayCurrentRouteStateDigest(changed)
	if err := fixture.store.saveNext(transition.Pending, changed); err == nil {
		t.Fatal("invalid replacement state unexpectedly installed")
	}
	foreign := transition
	foreign.Pending.Digest = ""
	foreign.Pending.Revision++
	foreign.Pending.Digest, _ = gatewayCurrentRouteStateDigest(foreign.Pending)
	if _, err := driver.stopGatewayCurrentPhysical(context.Background(), foreign); err == nil || runtime.stops != 1 {
		t.Fatalf("foreign protected marker reached stop: stops=%d error=%v", runtime.stops, err)
	}
}

func TestManagedGatewayCurrentPhysicalDriverStopsStableOwnedTargetWithoutSQLOrLatchAdmission(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.manager.gatewayCurrentOwnedStopTargetForStateLocked(fixture.baseline)
	if err != nil {
		t.Fatal(err)
	}
	sqlReads := 0
	fresh := &Manager{store: fixture.manager.store, options: fixture.manager.options, mu: newContextMutex(),
		gatewayRebindFailStop: &atomic.Bool{}, gatewayRebindCommitBarrier: &atomic.Bool{}}
	fresh.gatewayRebindFailStop.Store(true)
	fresh.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		sqlReads++
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
	})
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.stopFn = func(ctx context.Context, targets []gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if len(targets) != 1 || targets[0].Pending != nil || targets[0].LANRecovery != nil ||
			targets[0].Resources.FinalContainer == nil ||
			targets[0].Resources.FinalContainer.ID != owned.FinalContainer.ID {
			return gatewayCurrentPhysicalAttestation{}, errors.New("wrong stable owned target")
		}
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, targets[0],
			gatewayCurrentPhysicalRecoveryStopped), nil
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fresh},
		runtime:                             runtime,
	}
	if err := driver.stopGatewayCurrentOwnedTarget(context.Background(), owned); err != nil ||
		runtime.stops != 1 || sqlReads != 0 {
		t.Fatalf("stable owned withdrawal: stops=%d SQL reads=%d error=%v", runtime.stops, sqlReads, err)
	}

	replacement := owned
	replacement.FinalContainer.ID = strings.Repeat("8", 64)
	replacement.Digest, _ = gatewayCurrentOwnedStopTargetDigest(replacement)
	if err := driver.stopGatewayCurrentOwnedTarget(context.Background(), replacement); err == nil || runtime.stops != 1 {
		t.Fatalf("replacement owned identity reached stop: stops=%d error=%v", runtime.stops, err)
	}
}

func TestGatewayCurrentPhysicalOwnedStopTargetsUseCanonicalWholeBatchProjection(t *testing.T) {
	fixture, selection := gatewayCurrentLANRecoveryPhysicalSelectionFixture(t)
	owned, err := fixture.manager.gatewayCurrentOwnedStopTargetForStateLocked(*selection.State)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := gatewayCurrentPhysicalTargetsForOwnedStop(owned)
	if err != nil || len(targets) != 2 {
		t.Fatalf("whole-batch stop targets=%#v error=%v", targets, err)
	}
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(*selection.State)
	if err != nil || projection.Withdrawn == nil {
		t.Fatal("missing canonical whole-batch withdrawal projection")
	}
	if !reflect.DeepEqual(targets[0].State, projection.Before) ||
		!reflect.DeepEqual(targets[1].State, *projection.Withdrawn) {
		t.Fatalf("owned batch stop targets did not use canonical endpoints: %#v", targets)
	}
	for _, target := range targets {
		if !reflect.DeepEqual(target.LANRecovery, selection.State.LANRecovery) || target.Pending != nil {
			t.Fatalf("owned batch target lost exact retained queue: %#v", target)
		}
	}
}

func managedGatewayCurrentPhysicalTransitionFixture(t *testing.T) (
	gatewayCurrentStateFixture, gatewayCurrentPhysicalTransition, appaccess.GatewayRebindRecoverySnapshot,
) {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	transition, err := gatewayCurrentSwitchTransition(fixture.baseline,
		routeOperationSwitchRequest(t, appID, app.Route))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.saveNext(transition.Before, transition.Pending); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		return snapshot, nil
	})
	return fixture, transition, snapshot
}

func gatewayCurrentPhysicalDriverTestAttestation(t *testing.T, target gatewayCurrentPhysicalTarget,
	outcome gatewayCurrentPhysicalOutcome,
) gatewayCurrentPhysicalAttestation {
	t.Helper()
	value := gatewayCurrentPhysicalAttestationFixture(t, target.State, target.Terminal, outcome)
	value.Pending = cloneGatewayCurrentPendingRoute(target.Pending)
	value.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(target.LANRecovery)
	if outcome == gatewayCurrentPhysicalRecoveryStopped {
		value.Runtime.ListenerAbsent = true
		var err error
		value.Runtime.Digest, err = gatewayCurrentRuntimeProofDigest(value.Runtime)
		if err != nil {
			t.Fatal(err)
		}
	}
	var err error
	value.Digest, err = gatewayCurrentPhysicalAttestationDigest(value)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) {
		t.Fatalf("build driver attestation: valid=%t error=%v", validGatewayCurrentPhysicalAttestation(value), err)
	}
	return value
}
