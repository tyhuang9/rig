package generatedingress

import (
	"context"
	"errors"
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
