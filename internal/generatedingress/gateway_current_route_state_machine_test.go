package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
)

type gatewayCurrentStateMachineDriver struct {
	t                    *testing.T
	terminal             gatewayRebindAttemptTerminalView
	failApply            bool
	failAttest           bool
	attestValue          *gatewayCurrentPhysicalAttestation
	afterApply           func()
	afterRestore         func()
	pendingAttestOutcome gatewayCurrentPhysicalOutcome
	attestContext        func(context.Context)
	afterAttest          func()
	applyCalls           int
	restoreCalls         int
	stopCalls            int
	attestCalls          int
}

func (d *gatewayCurrentStateMachineDriver) applyGatewayCurrentPhysical(_ context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	d.applyCalls++
	if d.failApply {
		return gatewayCurrentPhysicalAttestation{}, errors.New("test physical apply failure")
	}
	value := gatewayCurrentPhysicalAttestationFixture(d.t, transition.Effective, d.terminal,
		gatewayCurrentPhysicalRecoveryEffective)
	value.Pending = cloneGatewayCurrentPendingRoute(transition.Pending.Pending)
	value.Digest, _ = gatewayCurrentPhysicalAttestationDigest(value)
	if d.afterApply != nil {
		d.afterApply()
		d.afterApply = nil
	}
	return value, nil
}

func (d *gatewayCurrentStateMachineDriver) restoreGatewayCurrentPhysical(_ context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	d.restoreCalls++
	value := gatewayCurrentPhysicalAttestationFixture(d.t, transition.Before, d.terminal,
		gatewayCurrentPhysicalRecoveryBefore)
	value.Pending = cloneGatewayCurrentPendingRoute(transition.Pending.Pending)
	value.Digest, _ = gatewayCurrentPhysicalAttestationDigest(value)
	if d.afterRestore != nil {
		d.afterRestore()
		d.afterRestore = nil
	}
	return value, nil
}

func (d *gatewayCurrentStateMachineDriver) stopGatewayCurrentPhysical(_ context.Context,
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentPhysicalAttestation, error) {
	d.stopCalls++
	value := gatewayCurrentPhysicalAttestationFixture(d.t, transition.Before, d.terminal,
		gatewayCurrentPhysicalRecoveryStopped)
	value.Runtime.ListenerAbsent = true
	value.Runtime.Digest, _ = gatewayCurrentRuntimeProofDigest(value.Runtime)
	value.Pending = cloneGatewayCurrentPendingRoute(transition.Pending.Pending)
	value.Digest, _ = gatewayCurrentPhysicalAttestationDigest(value)
	return value, nil
}

func (d *gatewayCurrentStateMachineDriver) attestGatewayCurrentPhysical(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	d.attestCalls++
	if d.attestContext != nil {
		d.attestContext(ctx)
	}
	if d.failAttest {
		return gatewayCurrentPhysicalAttestation{}, errors.New("test physical attestation failure")
	}
	if d.attestValue != nil {
		return *d.attestValue, nil
	}
	state := cloneGatewayCurrentOperationState(*selection.State)
	outcome := gatewayCurrentPhysicalStableServing
	attested := cloneGatewayCurrentOperationState(state)
	attested.Pending = nil
	if state.Pending != nil {
		outcome = d.pendingAttestOutcome
		projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(state)
		if err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		switch outcome {
		case gatewayCurrentPhysicalRecoveryBefore:
			attested = projection.Before
		case gatewayCurrentPhysicalRecoveryEffective:
			if projection.Effective == nil {
				return gatewayCurrentPhysicalAttestation{}, errors.New("test pending has no effective projection")
			}
			attested = *projection.Effective
		default:
			return gatewayCurrentPhysicalAttestation{}, errors.New("unsupported test pending outcome")
		}
	}
	value := gatewayCurrentPhysicalAttestationFixture(d.t, attested, d.terminal, outcome)
	value.Pending = cloneGatewayCurrentPendingRoute(state.Pending)
	value.Digest, _ = gatewayCurrentPhysicalAttestationDigest(value)
	if d.afterAttest != nil {
		d.afterAttest()
		d.afterAttest = nil
	}
	return value, nil
}

func (*gatewayCurrentStateMachineDriver) stopGatewayCurrentOwnedPredecessor(context.Context,
	gatewayRebindAttemptTerminalView,
) error {
	return errors.New("not used")
}

func prepareGatewayCurrentPublicManagerTest(manager *Manager) {
	manager.mu = newContextMutex()
	manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	manager.gatewayRebindFailStop = &atomic.Bool{}
	manager.gatewayRebindCommitBarrier = &atomic.Bool{}
}

func TestGatewayCurrentStateMachineSwitchAndRecoveryUseDurablePending(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	repository := &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	fixture.manager.options.RebindCurrentStateRepository = repository
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	request := routeOperationSwitchRequest(t, appID, app.Route)
	if err := exerciseManagerSwitch(fixture.manager, context.Background(), request); err != nil {
		t.Fatal(err)
	}
	effective := routeOperationLoad(t, fixture.store)
	if effective.Revision != fixture.baseline.Revision+2 || effective.Pending != nil ||
		effective.Apps[appID].Route.Slot != request.ToSlot ||
		!reflect.DeepEqual(effective.Apps[appID].LAN, app.LAN) || driver.applyCalls != 1 || driver.attestCalls != 1 {
		t.Fatalf("current switch did not complete exact durable transition: %#v calls=%d/%d",
			effective, driver.applyCalls, driver.attestCalls)
	}
	driver.failAttest = true
	if err := exerciseManagerSwitch(fixture.manager, context.Background(), request); err == nil {
		t.Fatal("replayed current switch accepted failed physical reattestation")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || !ingressErr.CandidateMayBeLive() {
			t.Fatalf("replayed current switch failure did not retain candidate: %v", err)
		}
	}
	driver.failAttest = false
	invalid := routeOperationSwitchRequest(t, appID, effective.Apps[appID].Route)
	invalid.FromSlot = generatedidentity.Slot("unexpected")
	if err := exerciseManagerSwitch(fixture.manager, context.Background(), invalid); err == nil {
		t.Fatal("invalid pre-physical switch accepted")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || ingressErr.CandidateMayBeLive() {
			t.Fatalf("pre-physical validation failure marked candidate live: %v", err)
		}
	}

	nextRequest := routeOperationSwitchRequest(t, appID, effective.Apps[appID].Route)
	transition, err := gatewayCurrentSwitchTransition(effective, nextRequest)
	if err != nil {
		t.Fatal(err)
	}
	driver.failApply = true
	if err := exerciseManagerSwitch(fixture.manager, context.Background(), nextRequest); err == nil {
		t.Fatal("failed physical switch returned success")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || !ingressErr.CandidateMayBeLive() {
			t.Fatalf("physical switch failure did not retain candidate: %v", err)
		}
	}
	driver.failApply = false
	if pending := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(pending, transition.Pending) {
		t.Fatalf("failed physical switch did not retain exact pending: %#v", pending)
	}
	if err := exerciseManagerSwitch(fixture.manager, context.Background(), nextRequest); err == nil {
		t.Fatal("retry accepted unresolved prior switch publication")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || !ingressErr.CandidateMayBeLive() {
			t.Fatalf("retry after unresolved publication did not retain candidate: %v", err)
		}
	}
	changedBeforeRestore := snapshot
	changedBeforeRestore.RollbackAllowed = !snapshot.RollbackAllowed
	repository.calls = 0
	repository.snapshots = []appaccess.GatewayRebindRecoverySnapshot{
		snapshot, snapshot, snapshot, snapshot, snapshot, changedBeforeRestore,
	}
	if err := exerciseGatewayCurrentRecoveryCore(fixture.manager, context.Background()); err == nil {
		t.Fatal("pending switch recovery accepted SQL drift immediately before physical restore")
	}
	if retained := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(retained, transition.Pending) ||
		driver.restoreCalls != 0 {
		t.Fatalf("pre-restore SQL drift changed protected or physical state: %#v restoreCalls=%d",
			retained, driver.restoreCalls)
	}
	repository.calls = 0
	repository.snapshots = []appaccess.GatewayRebindRecoverySnapshot{snapshot}
	if err := exerciseGatewayCurrentRecoveryCore(fixture.manager, context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := routeOperationLoad(t, fixture.store)
	wantRecovered, err := gatewayCurrentNextState(transition.Pending, func(next *gatewayCurrentRouteState) {
		next.Pending = nil
	})
	if err != nil || !reflect.DeepEqual(recovered, wantRecovered) ||
		driver.applyCalls != 2 || driver.restoreCalls != 1 || driver.attestCalls != 3 {
		t.Fatalf("pending switch recovery did not restore exact before state: %#v calls=%d/%d/%d error=%v",
			recovered, driver.applyCalls, driver.restoreCalls, driver.attestCalls, err)
	}
	changed := snapshot
	changed.RollbackAllowed = !snapshot.RollbackAllowed
	driver.afterAttest = func() {
		repository.snapshots = []appaccess.GatewayRebindRecoverySnapshot{changed}
	}
	if err := exerciseManagerSwitch(fixture.manager, context.Background(), request); err == nil {
		t.Fatal("current replay accepted SQL authority drift after physical attestation")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || !ingressErr.CandidateMayBeLive() {
			t.Fatalf("post-attestation SQL drift did not retain candidate: %v", err)
		}
	}
	newAppID := uuid.NewString()
	create, err := gatewayCurrentSwitchTransition(recovered, generatedroute.RouteSwitchRequest{
		AppID: newAppID, ToSlot: generatedidentity.SlotBlue,
		Endpoints: append([]generatedroute.RouteEndpoint(nil), recovered.Apps[appID].Route.Endpoints...),
	})
	if err != nil || create.Pending.Pending == nil || create.Pending.Pending.Previous != nil {
		t.Fatalf("new-app switch did not retain an explicit absent Before: %#v error=%v", create, err)
	}
	reconstructed, err := gatewayCurrentTransitionFromPending(create.Pending)
	if err != nil || !reflect.DeepEqual(reconstructed, create) {
		t.Fatalf("new-app pending switch cannot be recovered to absence: %#v error=%v", reconstructed, err)
	}

	t.Run("expected begin accepts exact pending-revision physical proof", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		assertGatewayCurrentRouteRevisionSplit(t, f.transition)
		f.driver.pendingAttestOutcome = gatewayCurrentPhysicalRecoveryEffective
		sealed, err := f.manager.beginGeneratedDomainRouteActivation(context.Background(), f.request, f.sealed)
		if err != nil || !reflect.DeepEqual(sealed, f.sealed) {
			t.Fatalf("expected current begin did not accept exact pending-revision physical proof: sealed=%#v error=%v",
				sealed, err)
		}
		if state := routeOperationLoad(t, f.fixture.store); !reflect.DeepEqual(state, f.transition.Pending) ||
			f.driver.attestCalls != 1 || f.driver.applyCalls != 0 || f.driver.restoreCalls != 0 {
			t.Fatalf("expected current begin changed protected state or physical effects: state=%#v calls=%d/%d/%d",
				state, f.driver.applyCalls, f.driver.restoreCalls, f.driver.attestCalls)
		}
	})

	t.Run("finish persists the next effective revision after pending-revision proof", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		assertGatewayCurrentRouteRevisionSplit(t, f.transition)
		f.driver.pendingAttestOutcome = gatewayCurrentPhysicalRecoveryEffective
		if err := f.manager.finishGeneratedDomainRouteActivation(context.Background(), f.sealed); err != nil {
			t.Fatal(err)
		}
		if state := routeOperationLoad(t, f.fixture.store); !reflect.DeepEqual(state, f.transition.Effective) ||
			state.Revision != f.transition.Effective.Revision || f.driver.attestCalls != 2 ||
			f.driver.applyCalls != 0 || f.driver.restoreCalls != 0 {
			t.Fatalf("current finish did not persist and attest the exact next effective revision: state=%#v calls=%d/%d/%d",
				state, f.driver.applyCalls, f.driver.restoreCalls, f.driver.attestCalls)
		}
	})

	t.Run("finish refuses a durable next revision as pre-persist physical proof", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		assertGatewayCurrentRouteRevisionSplit(t, f.transition)
		attestation := gatewayCurrentPhysicalAttestationFixture(t, f.transition.Effective, f.terminal,
			gatewayCurrentPhysicalRecoveryEffective)
		attestation.Pending = cloneGatewayCurrentPendingRoute(f.transition.Pending.Pending)
		attestation.Digest, _ = gatewayCurrentPhysicalAttestationDigest(attestation)
		f.driver.attestValue = &attestation
		if err := f.manager.finishGeneratedDomainRouteActivation(context.Background(), f.sealed); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("current finish accepted durable next revision as a pre-persist physical proof: %v", err)
		}
		if state := routeOperationLoad(t, f.fixture.store); !reflect.DeepEqual(state, f.transition.Pending) ||
			f.driver.attestCalls != 1 || f.driver.applyCalls != 0 || f.driver.restoreCalls != 0 {
			t.Fatalf("crossed current physical revision changed protected state or effects: state=%#v calls=%d/%d/%d",
				state, f.driver.applyCalls, f.driver.restoreCalls, f.driver.attestCalls)
		}
	})

	t.Run("rollback accepts pending-revision effective proof before exact restore", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		assertGatewayCurrentRouteRevisionSplit(t, f.transition)
		f.driver.pendingAttestOutcome = gatewayCurrentPhysicalRecoveryEffective
		if err := f.manager.rollbackGeneratedDomainRouteActivation(context.Background(), f.sealed); err != nil {
			t.Fatal(err)
		}
		if state := routeOperationLoad(t, f.fixture.store); !reflect.DeepEqual(state, f.cleared) ||
			f.driver.attestCalls != 2 || f.driver.applyCalls != 0 || f.driver.restoreCalls != 1 {
			t.Fatalf("current rollback did not restore and persist the exact cleared state: state=%#v calls=%d/%d/%d",
				state, f.driver.applyCalls, f.driver.restoreCalls, f.driver.attestCalls)
		}
	})
}

func assertGatewayCurrentRouteRevisionSplit(t *testing.T, transition gatewayCurrentPhysicalTransition) {
	t.Helper()
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(transition.Pending)
	if err != nil || projection.Effective == nil {
		t.Fatalf("current route transition has no effective physical projection: %#v error=%v", transition, err)
	}
	if projection.Effective.Revision != transition.Pending.Revision ||
		projection.Effective.Digest == transition.Effective.Digest ||
		transition.Effective.Revision != transition.Pending.Revision+1 ||
		!reflect.DeepEqual(projection.Effective.Apps, transition.Effective.Apps) {
		t.Fatalf("current route transition collapsed its physical and durable effective revisions: pending=%#v projection=%#v effective=%#v",
			transition.Pending, *projection.Effective, transition.Effective)
	}
}

type generatedRoutePreviousGatewayCurrentClearFixture struct {
	fixture    gatewayCurrentStateFixture
	manager    *Manager
	repository *routeOperationSnapshotRepository
	driver     *gatewayCurrentStateMachineDriver
	terminal   gatewayRebindAttemptTerminalView
	request    generatedroute.RouteSwitchRequest
	transition gatewayCurrentPhysicalTransition
	sealed     *generatedRouteActivationGatewayTransition
	cleared    gatewayCurrentRouteState
}

func newGeneratedRoutePreviousGatewayCurrentClearFixture(t *testing.T) generatedRoutePreviousGatewayCurrentClearFixture {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	repository := &routeOperationSnapshotRepository{snapshots: []appaccess.GatewayRebindRecoverySnapshot{
		routeOperationCommittedSnapshot(fixture),
	}}
	fixture.manager.options.RebindCurrentStateRepository = repository
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal,
		pendingAttestOutcome: gatewayCurrentPhysicalRecoveryBefore}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	request := routeOperationSwitchRequest(t, appID, app.Route)
	transition, err := gatewayCurrentSwitchTransition(fixture.baseline, request)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := gatewayCurrentNextState(transition.Pending, func(next *gatewayCurrentRouteState) {
		next.Pending = nil
	})
	if err != nil || !reflect.DeepEqual(cleared.Apps, transition.Before.Apps) ||
		!sameGatewayCurrentRouteOrigin(cleared, transition.Before) {
		t.Fatalf("current PreviousExact clear fixture has invalid cleared projection: %#v error=%v", cleared, err)
	}
	value := transition
	sealed := &generatedRouteActivationGatewayTransition{kind: generatedDomainGatewayCurrentV1,
		request: canonicalGeneratedRouteRequest(request), currentV1: &value}
	if !validGeneratedRouteActivationGatewayTransition(sealed) {
		t.Fatal("current PreviousExact clear fixture produced an invalid transition")
	}
	return generatedRoutePreviousGatewayCurrentClearFixture{fixture: fixture,
		manager: fixture.manager, repository: repository, driver: driver, terminal: terminal,
		request: request, transition: transition, sealed: sealed, cleared: cleared}
}

func (f generatedRoutePreviousGatewayCurrentClearFixture) installPending(t *testing.T) {
	t.Helper()
	if err := f.fixture.store.saveNext(f.fixture.baseline, f.transition.Pending); err != nil {
		t.Fatal(err)
	}
}

func (f generatedRoutePreviousGatewayCurrentClearFixture) installAfterPending(t *testing.T,
	state gatewayCurrentRouteState,
) {
	t.Helper()
	f.installPending(t)
	if err := f.fixture.store.saveNext(f.transition.Pending, state); err != nil {
		t.Fatal(err)
	}
}

type generatedRoutePreviousGatewayCurrentSnapshot struct {
	protected generatedDomainProtectedStartupSnapshot
	retained  *generatedDomainRetainedV2Projection
	selection gatewayCurrentSelection
}

func generatedRoutePreviousGatewayCurrentSelection(t *testing.T,
	f generatedRoutePreviousGatewayCurrentClearFixture,
) gatewayCurrentSelection {
	t.Helper()
	selection, _, handled, err := f.manager.gatewayCurrentSelectedStateForRecoveryLocked(context.Background())
	if err != nil || !handled || selection.Kind != gatewayCurrentSelectionRebind ||
		selection.State == nil || selection.Store == nil || selection.Terminal == nil || selection.Receipt == nil {
		t.Fatalf("current PreviousExact selection is incomplete: handled=%t selection=%#v error=%v",
			handled, selection, err)
	}
	return selection
}

func generatedRoutePreviousGatewayCurrentSnapshotFor(t *testing.T,
	f generatedRoutePreviousGatewayCurrentClearFixture,
) generatedRoutePreviousGatewayCurrentSnapshot {
	t.Helper()
	selection := generatedRoutePreviousGatewayCurrentSelection(t, f)
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), f.fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	terminal, exists := protected.terminals[selection.State.Lineage.OperationID]
	if !exists {
		t.Fatal("current PreviousExact protected history has no exact terminal")
	}
	retained, ok := generatedDomainCurrentV1RetainedV2Projection(*selection.State, terminal, protected.rebind)
	if !ok || !generatedDomainRetainedV2ProjectionValid(retained) {
		t.Fatalf("current PreviousExact retained NativeV2 companion is invalid: %#v", retained)
	}
	copy := cloneGeneratedDomainRetainedV2Projection(*retained)
	return generatedRoutePreviousGatewayCurrentSnapshot{protected: protected,
		retained: &copy, selection: selection}
}

func generatedRoutePreviousGatewayCurrentMissingSnapshot(t *testing.T,
	f generatedRoutePreviousGatewayCurrentClearFixture,
) generatedDomainProtectedStartupSnapshot {
	t.Helper()
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), f.fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	return protected
}

func generatedRoutePreviousGatewayCurrentSelectionIdentityEqual(left, right gatewayCurrentSelection) bool {
	return left.Kind == right.Kind && left.Lineage == right.Lineage &&
		reflect.DeepEqual(left.Upgrade, right.Upgrade) &&
		reflect.DeepEqual(left.UpgradeSource, right.UpgradeSource) &&
		reflect.DeepEqual(left.Receipt, right.Receipt) &&
		reflect.DeepEqual(left.Terminal, right.Terminal) &&
		left.Store != nil && right.Store != nil &&
		left.Store.dataRoot == right.Store.dataRoot &&
		left.Store.generation == right.Store.generation &&
		left.Store.operationID == right.Store.operationID &&
		left.Store.path == right.Store.path && left.Store.purpose == right.Store.purpose
}

func generatedRoutePreviousGatewayCurrentProtectedAdvanceOnly(before,
	after generatedDomainProtectedStartupSnapshot, currentName string,
) bool {
	if _, ok := before.currentV1[currentName]; !ok {
		return false
	}
	if _, ok := after.currentV1[currentName]; !ok {
		return false
	}
	normalized := before
	normalized.currentV1 = after.currentV1
	normalized.artifacts = append([]generatedDomainProtectedArtifact(nil), before.artifacts...)
	for index := range normalized.artifacts {
		if normalized.artifacts[index].name != currentName {
			continue
		}
		found := false
		for _, artifact := range after.artifacts {
			if artifact.name == currentName {
				normalized.artifacts[index] = artifact
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return generatedRoutePreviousSameProtected(normalized, after)
}

func generatedRoutePreviousAssertGatewayCurrentRefusal(t *testing.T,
	f generatedRoutePreviousGatewayCurrentClearFixture,
	before generatedRoutePreviousGatewayCurrentSnapshot,
	sealed *generatedRouteActivationGatewayTransition, calls [4]int, attestDelta int,
) {
	t.Helper()
	after := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
	if !generatedRoutePreviousSameProtected(before.protected, after.protected) ||
		!reflect.DeepEqual(before.retained, after.retained) ||
		!generatedRoutePreviousGatewayCurrentSelectionIdentityEqual(before.selection, after.selection) ||
		!reflect.DeepEqual(before.selection.State, after.selection.State) {
		t.Fatalf("current PreviousExact refusal changed protected selection or retained companion:\nbefore=%#v\nafter=%#v",
			before, after)
	}
	if f.driver.applyCalls != calls[0] || f.driver.restoreCalls != calls[1] ||
		f.driver.stopCalls != calls[2] || f.driver.attestCalls != calls[3]+attestDelta {
		t.Fatalf("current PreviousExact refusal issued a physical mutation: calls=%d/%d/%d/%d before=%v",
			f.driver.applyCalls, f.driver.restoreCalls, f.driver.stopCalls, f.driver.attestCalls, calls)
	}
	if !reflect.DeepEqual(f.sealed, sealed) {
		t.Fatal("current PreviousExact refusal mutated its sealed transition input")
	}
}

func generatedRoutePreviousAssertGatewayCurrentClear(t *testing.T,
	f generatedRoutePreviousGatewayCurrentClearFixture,
	before generatedRoutePreviousGatewayCurrentSnapshot,
	sealed *generatedRouteActivationGatewayTransition, calls [4]int,
) {
	t.Helper()
	after := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
	state := *after.selection.State
	proof := cloneGatewayCurrentOperationState(state)
	proof.Digest = ""
	digest, err := gatewayCurrentRouteStateDigest(proof)
	if err != nil || !reflect.DeepEqual(state, f.cleared) || state.Revision != f.transition.Pending.Revision+1 ||
		f.transition.Pending.Revision != f.transition.Before.Revision+1 || state.Digest != digest ||
		!sameGatewayCurrentRouteOrigin(state, f.transition.Before) ||
		!reflect.DeepEqual(state.Apps, f.transition.Before.Apps) || state.Pending != nil ||
		!reflect.DeepEqual(state.LANRecovery, f.transition.Pending.LANRecovery) {
		t.Fatalf("current PreviousExact clear readback is not canonical: state=%#v digest=%s error=%v",
			state, digest, err)
	}
	if !generatedRoutePreviousGatewayCurrentSelectionIdentityEqual(before.selection, after.selection) {
		t.Fatalf("current PreviousExact clear changed selection identity: before=%#v after=%#v",
			before.selection, after.selection)
	}
	expectedRetained := cloneGeneratedDomainRetainedV2Projection(*before.retained)
	expectedRetained.current = cloneGatewayCurrentRouteState(state)
	if !reflect.DeepEqual(after.retained, &expectedRetained) ||
		!generatedDomainRetainedV2ProjectionValid(after.retained) {
		t.Fatalf("current PreviousExact clear changed retained NativeV2 companion: want=%#v got=%#v",
			expectedRetained, after.retained)
	}
	if !sameGatewayCurrentV2Census(before.protected.currentV2, after.protected.currentV2) ||
		!generatedRoutePreviousGatewayCurrentProtectedAdvanceOnly(before.protected, after.protected,
			filepathBase(f.fixture.store.path)) {
		t.Fatal("current PreviousExact clear changed protected artifacts outside the selected CurrentV1 state")
	}
	if f.driver.applyCalls != calls[0] || f.driver.restoreCalls != calls[1] ||
		f.driver.stopCalls != calls[2] || f.driver.attestCalls != calls[3]+2 {
		t.Fatalf("current PreviousExact clear physical calls=%d/%d/%d/%d before=%v",
			f.driver.applyCalls, f.driver.restoreCalls, f.driver.stopCalls, f.driver.attestCalls, calls)
	}
	if !reflect.DeepEqual(f.sealed, sealed) {
		t.Fatal("current PreviousExact clear mutated its sealed transition input")
	}
}

func generatedRoutePreviousGatewayCurrentCalls(f generatedRoutePreviousGatewayCurrentClearFixture) [4]int {
	return [4]int{f.driver.applyCalls, f.driver.restoreCalls, f.driver.stopCalls, f.driver.attestCalls}
}

func TestGeneratedRouteActivationPreviousExactGatewayCurrentClearMatrix(t *testing.T) {
	t.Run("exact_previous_clears", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); err != nil {
			t.Fatal(err)
		}
		generatedRoutePreviousAssertGatewayCurrentClear(t, f, before, sealed, calls)
	})

	t.Run("already_cleared_is_idempotent", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installAfterPending(t, f.cleared)
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); err != nil {
			t.Fatal(err)
		}
		after := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		if !generatedRoutePreviousSameProtected(before.protected, after.protected) ||
			!reflect.DeepEqual(before.retained, after.retained) ||
			!reflect.DeepEqual(before.selection.State, after.selection.State) ||
			!generatedRoutePreviousGatewayCurrentSelectionIdentityEqual(before.selection, after.selection) ||
			f.driver.applyCalls != calls[0] || f.driver.restoreCalls != calls[1] ||
			f.driver.stopCalls != calls[2] || f.driver.attestCalls != calls[3]+1 ||
			!reflect.DeepEqual(f.sealed, sealed) {
			t.Fatalf("idempotent current PreviousExact clear changed state: before=%#v after=%#v", before, after)
		}
	})

	t.Run("crossed_protected_state_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installAfterPending(t, f.transition.Effective)
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("crossed current protected state error=%v", err)
		}
		generatedRoutePreviousAssertGatewayCurrentRefusal(t, f, before, sealed, calls, 0)
	})

	t.Run("physical_proposed_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		f.driver.pendingAttestOutcome = gatewayCurrentPhysicalRecoveryEffective
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("current physical Proposed error=%v", err)
		}
		generatedRoutePreviousAssertGatewayCurrentRefusal(t, f, before, sealed, calls, 1)
	})

	t.Run("physical_mixed_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(f.transition.Pending)
		if err != nil {
			t.Fatal(err)
		}
		attestation := gatewayCurrentPhysicalAttestationFixture(t, projection.Before, f.terminal,
			gatewayCurrentPhysicalRecoveryMixed)
		attestation.Pending = cloneGatewayCurrentPendingRoute(f.transition.Pending.Pending)
		attestation.Digest, _ = gatewayCurrentPhysicalAttestationDigest(attestation)
		f.driver.attestValue = &attestation
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("current physical Mixed error=%v", err)
		}
		generatedRoutePreviousAssertGatewayCurrentRefusal(t, f, before, sealed, calls, 1)
	})

	t.Run("physical_unknown_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		f.driver.failAttest = true
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("current unknown physical error=%v", err)
		}
		generatedRoutePreviousAssertGatewayCurrentRefusal(t, f, before, sealed, calls, 1)
	})

	t.Run("protected_state_missing_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		if err := os.Remove(f.fixture.store.path); err != nil {
			t.Fatal(err)
		}
		before := generatedRoutePreviousGatewayCurrentMissingSnapshot(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.sealed); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("missing current protected state error=%v", err)
		}
		after := generatedRoutePreviousGatewayCurrentMissingSnapshot(t, f)
		if !generatedRoutePreviousSameProtected(before, after) ||
			f.driver.applyCalls != calls[0] || f.driver.restoreCalls != calls[1] ||
			f.driver.stopCalls != calls[2] || f.driver.attestCalls != calls[3] ||
			!reflect.DeepEqual(f.sealed, sealed) {
			t.Fatal("missing current protected state refusal mutated protected or physical state")
		}
	})

	t.Run("canceled_context_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousGatewayCurrentClearFixture(t)
		f.installPending(t)
		before := generatedRoutePreviousGatewayCurrentSnapshotFor(t, f)
		sealed := cloneGeneratedRouteActivationGatewayTransition(f.sealed)
		calls := generatedRoutePreviousGatewayCurrentCalls(f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(ctx, f.sealed); !IsCode(err, DiagnosticCancelled) {
			t.Fatalf("canceled current clear error=%v", err)
		}
		generatedRoutePreviousAssertGatewayCurrentRefusal(t, f, before, sealed, calls, 0)
	})
}

func TestGatewayCurrentObservationUsesExactStableRebindState(t *testing.T) {
	type observationFixture struct {
		fixture    gatewayCurrentStateFixture
		driver     *gatewayCurrentStateMachineDriver
		repository *routeOperationSnapshotRepository
		appID      string
		app        gatewayCurrentAppRoute
	}
	newFixture := func(t *testing.T) observationFixture {
		t.Helper()
		fixture := newGatewayCurrentStateFixture(t)
		prepareGatewayCurrentPublicManagerTest(fixture.manager)
		if err := fixture.store.installBaseline(fixture.baseline); err != nil {
			t.Fatal(err)
		}
		snapshot := routeOperationCommittedSnapshot(fixture)
		repository := &routeOperationSnapshotRepository{
			snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
		}
		fixture.manager.options.RebindCurrentStateRepository = repository
		terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
		if err != nil {
			t.Fatal(err)
		}
		driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
		fixture.manager.gatewayCurrentPhysicalDriver = driver
		appID, app := routeOperationTransferredApp(t, fixture.baseline)
		return observationFixture{fixture: fixture, driver: driver, repository: repository,
			appID: appID, app: app}
	}
	assertUnchanged := func(t *testing.T, state observationFixture, before gatewayCurrentRouteState) {
		t.Helper()
		after := routeOperationLoad(t, state.fixture.store)
		if !reflect.DeepEqual(after, before) || state.driver.applyCalls != 0 ||
			state.driver.restoreCalls != 0 || state.driver.stopCalls != 0 {
			t.Fatalf("observation mutated current state: before=%#v after=%#v effects=%d/%d/%d",
				before, after, state.driver.applyCalls, state.driver.restoreCalls, state.driver.stopCalls)
		}
	}

	t.Run("stable exact current rebind", func(t *testing.T) {
		state := newFixture(t)
		before := routeOperationLoad(t, state.fixture.store)
		callbacks := 0
		var attestRemaining, callbackRemaining time.Duration
		state.driver.attestContext = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("current attestation context has no deadline")
			}
			attestRemaining = time.Until(deadline)
		}
		var got Observation
		err := state.fixture.manager.WithObservation(context.Background(), state.appID,
			func(ctx context.Context, observation Observation) error {
				callbacks++
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("current observation callback context has no deadline")
				}
				callbackRemaining = time.Until(deadline)
				got = observation
				return nil
			})
		if err != nil || callbacks != 1 || state.driver.attestCalls != 1 ||
			got.Slot != state.app.Route.Slot || !reflect.DeepEqual(got.Endpoints, state.app.Route.Endpoints) ||
			got.URL == "" || got.ObservedAt.IsZero() || attestRemaining <= observationTimeout ||
			callbackRemaining <= observationTimeout || attestRemaining > v2ObservationTimeout ||
			callbackRemaining > v2ObservationTimeout {
			t.Fatalf("stable current observation=%#v callbacks=%d attest=%d budgets=%s/%s error=%v",
				got, callbacks, state.driver.attestCalls, attestRemaining, callbackRemaining, err)
		}
		assertUnchanged(t, state, before)
	})

	t.Run("caller deadline caps current attestation and callback", func(t *testing.T) {
		state := newFixture(t)
		before := routeOperationLoad(t, state.fixture.store)
		const callerBudget = 45 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
		defer cancel()
		var attestRemaining, callbackRemaining time.Duration
		state.driver.attestContext = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("caller-capped current attestation has no deadline")
			}
			attestRemaining = time.Until(deadline)
		}
		err := state.fixture.manager.WithObservation(ctx, state.appID,
			func(callbackContext context.Context, _ Observation) error {
				deadline, ok := callbackContext.Deadline()
				if !ok {
					t.Fatal("caller-capped current callback has no deadline")
				}
				callbackRemaining = time.Until(deadline)
				return nil
			})
		if err != nil || attestRemaining <= observationTimeout || callbackRemaining <= observationTimeout ||
			attestRemaining > callerBudget || callbackRemaining > callerBudget {
			t.Fatalf("caller-capped current budgets=%s/%s error=%v",
				attestRemaining, callbackRemaining, err)
		}
		assertUnchanged(t, state, before)
	})

	t.Run("caller cancellation during current attestation refuses without fallback", func(t *testing.T) {
		state := newFixture(t)
		before := routeOperationLoad(t, state.fixture.store)
		ctx, cancel := context.WithCancel(context.Background())
		state.driver.attestContext = func(context.Context) { cancel() }
		callbacks := 0
		err := state.fixture.manager.WithObservation(ctx, state.appID,
			func(context.Context, Observation) error {
				callbacks++
				return nil
			})
		if !IsCode(err, DiagnosticCancelled) || callbacks != 0 || state.driver.attestCalls != 1 {
			t.Fatalf("cancelled current observation callbacks=%d attest=%d error=%v",
				callbacks, state.driver.attestCalls, err)
		}
		assertUnchanged(t, state, before)
	})

	t.Run("pending current rebind refuses before physical observation", func(t *testing.T) {
		state := newFixture(t)
		transition, err := gatewayCurrentSwitchTransition(state.fixture.baseline,
			routeOperationSwitchRequest(t, state.appID, state.app.Route))
		if err != nil {
			t.Fatal(err)
		}
		if err := state.fixture.store.saveNext(state.fixture.baseline, transition.Pending); err != nil {
			t.Fatal(err)
		}
		before := routeOperationLoad(t, state.fixture.store)
		callbacks := 0
		err = state.fixture.manager.WithObservation(context.Background(), state.appID,
			func(context.Context, Observation) error {
				callbacks++
				return nil
			})
		if err == nil || callbacks != 0 || state.driver.attestCalls != 0 {
			t.Fatalf("pending current observation callbacks=%d attest=%d error=%v",
				callbacks, state.driver.attestCalls, err)
		}
		assertUnchanged(t, state, before)
	})

	t.Run("crossed SQL protected rebind refuses without fallback", func(t *testing.T) {
		state := newFixture(t)
		crossed := state.repository.snapshots[0]
		crossed.CurrentTransfers = nil
		state.repository.calls = 0
		state.repository.snapshots = []appaccess.GatewayRebindRecoverySnapshot{crossed}
		before := routeOperationLoad(t, state.fixture.store)
		callbacks := 0
		err := state.fixture.manager.WithObservation(context.Background(), state.appID,
			func(context.Context, Observation) error {
				callbacks++
				return nil
			})
		if err == nil || callbacks != 0 || state.driver.attestCalls != 0 {
			t.Fatalf("crossed current observation callbacks=%d attest=%d error=%v",
				callbacks, state.driver.attestCalls, err)
		}
		assertUnchanged(t, state, before)
	})
}

func TestGatewayCurrentStateMachineFencesNativeFallbackDuringActiveRebind(t *testing.T) {
	manager, store, state, journal, _, _ := gatewayV2LANGrantFixture(t)
	prepareGatewayCurrentPublicManagerTest(manager)
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(history.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	profile := gatewayCurrentFixtureProfile(lineage, history.Predecessor.State.Profile)
	authority := gatewayCurrentAuthority(lineage)
	activeOperationID := uuid.NewString()
	active := appaccess.GatewayRebindHistoryEntry{Claim: appaccess.GatewayRebindClaimRecord{
		SpecVersion: appaccess.GatewayRebindSpecVersionV2,
		V2:          &appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{OperationID: activeOperationID}},
	}}
	snapshot := appaccess.GatewayRebindRecoverySnapshot{CurrentProfile: &profile, CurrentSource: &authority,
		Active: &active, Phase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	appID := ""
	var app gatewayV2AppRoute
	for candidate, value := range state.Apps {
		appID, app = candidate, value
		break
	}
	if appID == "" {
		t.Fatal("native fixture has no app")
	}
	request := routeOperationSwitchRequest(t, appID, app.Route)
	if err := exerciseManagerSwitch(manager, context.Background(), request); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("native Switch bypassed active rebind fence: %v", err)
	}
	if err := exerciseGatewayCurrentRecoveryCore(manager, context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("native Recover bypassed active rebind fence: %v", err)
	}
	retained, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		t.Fatalf("active rebind fence mutated native state: %v", err)
	}
}

func TestPersistGatewayCurrentExactRejectsRevisionOnlyNoop(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{routeOperationCommittedSnapshot(fixture)},
	}
	next := cloneGatewayCurrentOperationState(fixture.baseline)
	next.Revision++
	next.Digest = ""
	var err error
	next.Digest, err = gatewayCurrentRouteStateDigest(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.persistGatewayCurrentExactLocked(context.Background(), fixture.baseline, next); err == nil {
		t.Fatal("revision-only exact write was falsely reported as persisted")
	}
	if retained := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(retained, fixture.baseline) {
		t.Fatalf("revision-only exact write changed protected state: %#v", retained)
	}
}
