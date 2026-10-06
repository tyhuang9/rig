package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type gatewayCurrentStateMachineDriver struct {
	t                    *testing.T
	terminal             gatewayRebindAttemptTerminalView
	failApply            bool
	failAttest           bool
	pendingAttestOutcome gatewayCurrentPhysicalOutcome
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

func (d *gatewayCurrentStateMachineDriver) attestGatewayCurrentPhysical(_ context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	d.attestCalls++
	if d.failAttest {
		return gatewayCurrentPhysicalAttestation{}, errors.New("test physical attestation failure")
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
	if err := fixture.manager.Switch(context.Background(), request); err != nil {
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
	if err := fixture.manager.Switch(context.Background(), request); err == nil {
		t.Fatal("replayed current switch accepted failed physical reattestation")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || !ingressErr.CandidateMayBeLive() {
			t.Fatalf("replayed current switch failure did not retain candidate: %v", err)
		}
	}
	driver.failAttest = false
	invalid := routeOperationSwitchRequest(t, appID, effective.Apps[appID].Route)
	invalid.FromSlot = generatedruntime.Slot("unexpected")
	if err := fixture.manager.Switch(context.Background(), invalid); err == nil {
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
	if err := fixture.manager.Switch(context.Background(), nextRequest); err == nil {
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
	if err := fixture.manager.Switch(context.Background(), nextRequest); err == nil {
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
	if err := fixture.manager.Recover(context.Background()); err == nil {
		t.Fatal("pending switch recovery accepted SQL drift immediately before physical restore")
	}
	if retained := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(retained, transition.Pending) ||
		driver.restoreCalls != 0 {
		t.Fatalf("pre-restore SQL drift changed protected or physical state: %#v restoreCalls=%d",
			retained, driver.restoreCalls)
	}
	repository.calls = 0
	repository.snapshots = []appaccess.GatewayRebindRecoverySnapshot{snapshot}
	if err := fixture.manager.Recover(context.Background()); err != nil {
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
	if err := fixture.manager.Switch(context.Background(), request); err == nil {
		t.Fatal("current replay accepted SQL authority drift after physical attestation")
	} else {
		var ingressErr *Error
		if !errors.As(err, &ingressErr) || !ingressErr.CandidateMayBeLive() {
			t.Fatalf("post-attestation SQL drift did not retain candidate: %v", err)
		}
	}
	newAppID := uuid.NewString()
	create, err := gatewayCurrentSwitchTransition(recovered, generatedruntime.RouteSwitchRequest{
		AppID: newAppID, ToSlot: generatedruntime.SlotBlue,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), recovered.Apps[appID].Route.Endpoints...),
	})
	if err != nil || create.Pending.Pending == nil || create.Pending.Pending.Previous != nil {
		t.Fatalf("new-app switch did not retain an explicit absent Before: %#v error=%v", create, err)
	}
	reconstructed, err := gatewayCurrentTransitionFromPending(create.Pending)
	if err != nil || !reflect.DeepEqual(reconstructed, create) {
		t.Fatalf("new-app pending switch cannot be recovered to absence: %#v error=%v", reconstructed, err)
	}
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
	if err := manager.Switch(context.Background(), request); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("native Switch bypassed active rebind fence: %v", err)
	}
	if err := manager.Recover(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("native Recover bypassed active rebind fence: %v", err)
	}
	retained, retainedJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		t.Fatalf("active rebind fence mutated native state: %v", err)
	}
}
