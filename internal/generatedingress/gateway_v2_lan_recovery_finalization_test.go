package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestHasGatewayV2LANRecoveryBatchSelectsIntentBeforeReload(t *testing.T) {
	manager, store, journal, _, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if present, err := manager.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || present {
		t.Fatalf("initial present=%t err=%v", present, err)
	}
	persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
	if present, err := manager.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || !present {
		t.Fatalf("installed present=%t err=%v", present, err)
	}
}

func TestGatewayV2LANRecoveryFinalizesTwoDisablesSequentiallyAndRetires(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	initial, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	items := cloneGatewayV2LANRecoveryBatch(initial.LANRecovery)

	for wantHead := 0; wantHead < len(disables); wantHead++ {
		head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
		if err != nil || !present || head.Head != wantHead || head.Count != len(disables) ||
			head.Kind != GatewayV2LANRecoveryDisable {
			t.Fatalf("head=%#v present=%t err=%v", head, present, err)
		}
		request := disableRequestForRecoveryOperation(t, initial, head.OperationID)
		resolveCalls := 0
		ackCalls := 0
		if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
			func(_ context.Context, observation GatewayV2LANDisableObservation) error {
				resolveCalls++
				assertGatewayV2LANRecoveryLockHeld(t, manager)
				if observation.Request != request || observation.Disposition != GatewayV2LANDisableWithdrawnPending {
					t.Fatalf("resolve observation=%#v", observation)
				}
				commitGatewayV2LANRecoveryDisableClaim(t, disables, request.OperationID)
				return nil
			}, func(_ context.Context, observation GatewayV2LANDisableObservation) error {
				ackCalls++
				assertGatewayV2LANRecoveryLockHeld(t, manager)
				if observation.Request != request || observation.Disposition != GatewayV2LANDisableDisabled {
					t.Fatalf("ack observation=%#v", observation)
				}
				ackGatewayV2LANRecoveryDisableClaim(t, disables, request.OperationID)
				return nil
			}); err != nil {
			t.Fatal(err)
		}
		state, _, err := store.loadBoundUpgrade(journal.OperationID)
		if err != nil || state.LANRecovery == nil || state.LANRecovery.Head != wantHead+1 ||
			resolveCalls != 1 || ackCalls != 1 ||
			!reflect.DeepEqual(state.LANRecovery.Items, items.Items) ||
			!reflect.DeepEqual(state.LANRecovery.LegacyPending, items.LegacyPending) {
			t.Fatalf("state=%#v resolve=%d ack=%d err=%v", state.LANRecovery, resolveCalls, ackCalls, err)
		}
	}

	head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err != nil || !present || head.Head != head.Count || head.Kind != "" || head.OperationID != "" || head.AppID != "" {
		t.Fatalf("completed head=%#v present=%t err=%v", head, present, err)
	}
	if err := manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	retired, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retired.LANRecovery != nil || driver.gatewayStopped {
		t.Fatalf("retired=%#v stopped=%t err=%v", retired.LANRecovery, driver.gatewayStopped, err)
	}
}

type countingGatewayV2LANRecoveryBulkProof struct {
	*fakeGatewayV2LANGrantDriver
	calls    int
	ports    [][]uint16
	failPort uint16
}

func (d *countingGatewayV2LANRecoveryBulkProof) proveLANRecoveryBatch(_ context.Context,
	effective gatewayV2RouteState, _ gatewayMigrationJournal, ports []uint16,
) bool {
	d.checkLocked()
	d.calls++
	d.ports = append(d.ports, append([]uint16(nil), ports...))
	if !reflect.DeepEqual(d.live, effective) || len(ports) == 0 {
		return false
	}
	for _, port := range ports {
		if port == d.failPort {
			return false
		}
		for _, app := range effective.Apps {
			if app.LAN != nil && app.LAN.Port == port {
				return false
			}
		}
	}
	return true
}

func TestGatewayV2LANRecoveryTerminalProofUsesOneBulkPortCheckPerPhase(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for state.LANRecovery.Head < len(state.LANRecovery.Items) {
		state, err = gatewayV2LANRecoveryClearedHeadState(state)
		if err != nil {
			t.Fatal(err)
		}
		state, err = gatewayV2LANRecoveryAdvanceHeadState(state)
		if err != nil {
			t.Fatal(err)
		}
	}
	ports, err := gatewayV2LANRecoveryAllPorts(state)
	if err != nil {
		t.Fatal(err)
	}
	bulk := &countingGatewayV2LANRecoveryBulkProof{fakeGatewayV2LANGrantDriver: driver}
	retired := cloneGatewayV2RouteState(state)
	retired.LANRecovery = nil
	manager.mu.Lock()
	protectedOK := proveGatewayV2LANRecoveryProtected(context.Background(), state, journal, bulk)
	retiredOK := proveGatewayV2LANRecoveryRetired(context.Background(), state, retired, journal, bulk)
	manager.mu.Unlock()
	if !protectedOK || !retiredOK || bulk.calls != 2 ||
		!reflect.DeepEqual(bulk.ports, [][]uint16{ports, ports}) {
		t.Fatalf("terminal proof protected=%t retired=%t bulk calls=%d ports=%v want=%v",
			protectedOK, retiredOK, bulk.calls, bulk.ports, ports)
	}
	bulk.failPort = ports[0]
	manager.mu.Lock()
	protectedOK = proveGatewayV2LANRecoveryProtected(context.Background(), state, journal, bulk)
	retiredOK = proveGatewayV2LANRecoveryRetired(context.Background(), state, retired, journal, bulk)
	manager.mu.Unlock()
	if protectedOK || retiredOK {
		t.Fatal("terminal proof accepted a missing 404 port")
	}
}

func TestGatewayV2LANRecoveryFinalizesMixedGrantDisableWithoutChangingUnrelatedRoute(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorize, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorize); err != nil {
		t.Fatal(err)
	}
	disable := distinctGatewayV2LANDisableRequest(t, disableRequestForGrant(t, second))
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantPrepared, 1),
		gatewayV2LANAccessClaimWithDisable(second, disable.OperationID),
	}
	disables := []GatewayV2LANDisableStartupClaim{{
		Request: disable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1,
	}}
	unrelated := cloneGatewayV2AppRoute(driver.live.Apps[second.AppID])
	driver.events = nil
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	liveQuarantine := cloneGatewayV2RouteState(driver.live)

	head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err != nil || !present || head.Kind != GatewayV2LANRecoveryGrant || head.OperationID != first.AttemptID {
		t.Fatalf("grant head=%#v present=%t err=%v", head, present, err)
	}
	if err := manager.WithGatewayV2LANRecoveryGrantFinalization(context.Background(), first,
		func(_ context.Context, observation GatewayV2LANGrantObservation) error {
			assertGatewayV2LANRecoveryLockHeld(t, manager)
			if observation.Request != first || observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation {
				t.Fatalf("grant observation=%#v", observation)
			}
			grants[0].State = appaccess.AppAccessGrantRolledBack
			grants[0].StateSequence = 2
			grants[0].RequiresRecovery = false
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	afterGrant, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(afterGrant.Apps[second.AppID], unrelated) ||
		!reflect.DeepEqual(driver.live, liveQuarantine) {
		t.Fatalf("unrelated state changed after grant finalization: state=%#v live=%#v err=%v",
			afterGrant.Apps[second.AppID], driver.live, err)
	}

	head, present, err = manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err != nil || !present || head.Kind != GatewayV2LANRecoveryDisable || head.OperationID != disable.OperationID {
		t.Fatalf("disable head=%#v present=%t err=%v", head, present, err)
	}
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), disable,
		func(context.Context, GatewayV2LANDisableObservation) error {
			commitGatewayV2LANRecoveryDisableClaim(t, disables, disable.OperationID)
			return nil
		}, func(context.Context, GatewayV2LANDisableObservation) error {
			ackGatewayV2LANRecoveryDisableClaim(t, disables, disable.OperationID)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if err := manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery != nil || !reflect.DeepEqual(driver.live, liveQuarantine) {
		t.Fatalf("state=%#v live=%#v err=%v", state.LANRecovery, driver.live, err)
	}
}

func TestGatewayV2LANRecoveryFinalizesExactLegacyPairInGrantThenDisableOrder(t *testing.T) {
	manager, store, journal, driver, request, disable, grants, disables := gatewayV2LANRecoveryLegacyPairFixture(t)
	recording := &recordingGatewayV2LANRecoveryPortDriver{fakeGatewayV2LANGrantDriver: driver}
	manager.gatewayV2LANGrantDriver = recording
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recording.probedPorts, []uint16{request.Port}) {
		t.Fatalf("initial 404 probes=%v", recording.probedPorts)
	}
	disables[0].State = appaccess.AppAccessDisableCommitted
	disables[0].StateSequence = 3
	if head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables); err == nil || !present || head != (GatewayV2LANRecoveryHead{}) {
		t.Fatalf("out-of-order disable head=%#v present=%t err=%v", head, present, err)
	}
	disables[0].State = appaccess.AppAccessDisablePrepared
	disables[0].StateSequence = 1

	head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err != nil || !present || head.Kind != GatewayV2LANRecoveryGrant ||
		head.OperationID != request.AttemptID || head.Head != 0 || head.Count != 2 {
		t.Fatalf("grant head=%#v present=%t err=%v", head, present, err)
	}
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), disable,
		func(context.Context, GatewayV2LANDisableObservation) error { return nil },
		func(context.Context, GatewayV2LANDisableObservation) error { return nil }); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("disable crossed grant head: %v", err)
	}
	if err := manager.WithGatewayV2LANRecoveryGrantFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANGrantObservation) error {
			grants[0].State = appaccess.AppAccessGrantRolledBack
			grants[0].StateSequence = 2
			grants[0].RequiresRecovery = false
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery == nil || state.LANRecovery.Head != 1 ||
		state.Apps[request.AppID].LAN != nil {
		t.Fatalf("after grant state=%#v app=%#v err=%v", state.LANRecovery, state.Apps[request.AppID], err)
	}

	recording.probedPorts = nil
	head, present, err = manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err != nil || !present || head.Kind != GatewayV2LANRecoveryDisable ||
		head.OperationID != disable.OperationID || head.Head != 1 ||
		!reflect.DeepEqual(recording.probedPorts, []uint16{request.Port}) {
		t.Fatalf("disable head=%#v present=%t probes=%v err=%v", head, present, recording.probedPorts, err)
	}
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), disable,
		func(context.Context, GatewayV2LANDisableObservation) error {
			commitGatewayV2LANRecoveryDisableClaim(t, disables, disable.OperationID)
			return nil
		}, func(context.Context, GatewayV2LANDisableObservation) error {
			ackGatewayV2LANRecoveryDisableClaim(t, disables, disable.OperationID)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if err := manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	retired, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retired.LANRecovery != nil || retired.Apps[request.AppID].LAN != nil || driver.gatewayStopped {
		t.Fatalf("retired=%#v app=%#v stopped=%t err=%v",
			retired.LANRecovery, retired.Apps[request.AppID], driver.gatewayStopped, err)
	}
}

func TestGatewayV2LANRecoveryGrantTerminalCallbackCrashReplaysCurrentHead(t *testing.T) {
	manager, store, _, journal, request, _ := gatewayV2LANGrantFixture(t)
	grants := []GatewayV2LANStartupClaim{gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantPrepared, 1)}
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the terminal database rollback committed but
	// before the protected head binding could be cleared.
	grants[0].State = appaccess.AppAccessGrantRolledBack
	grants[0].StateSequence = 2
	head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, nil)
	if err != nil || !present || head.OperationID != request.AttemptID || head.Head != 0 {
		t.Fatalf("replay head=%#v present=%t err=%v", head, present, err)
	}
	callbackCalls := 0
	if err := manager.WithGatewayV2LANRecoveryGrantFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANGrantObservation) error {
			callbackCalls++
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery == nil || state.LANRecovery.Head != 1 || callbackCalls != 1 {
		t.Fatalf("state=%#v callbacks=%d err=%v", state.LANRecovery, callbackCalls, err)
	}
}

func TestGatewayV2LANRecoveryInspectorReprovesProcessedPrefixPorts(t *testing.T) {
	manager, _, _, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	head, _, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	request := disables[0].Request
	if request.OperationID != head.OperationID {
		request = disables[1].Request
	}
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANDisableObservation) error {
			commitGatewayV2LANRecoveryDisableClaim(t, disables, request.OperationID)
			return nil
		}, func(context.Context, GatewayV2LANDisableObservation) error {
			ackGatewayV2LANRecoveryDisableClaim(t, disables, request.OperationID)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	recording := &recordingGatewayV2LANRecoveryPortDriver{fakeGatewayV2LANGrantDriver: driver}
	manager.gatewayV2LANGrantDriver = recording
	if _, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables); err != nil || !present {
		t.Fatalf("present=%t err=%v", present, err)
	}
	want := []uint16{disables[0].Request.Port, disables[1].Request.Port}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(recording.probedPorts, want) {
		t.Fatalf("404 probes after head advancement=%v want=%v", recording.probedPorts, want)
	}
}

func TestGatewayV2LANRecoveryFinalizationPreservesUnrelatedCommittedRoute(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorize, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorize); err != nil {
		t.Fatal(err)
	}
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantPrepared, 1),
		gatewayV2LANStartupClaim(second, appaccess.AppAccessGrantCommitted, 4),
	}
	unrelated := cloneGatewayV2AppRoute(driver.live.Apps[second.AppID])
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, nil); err != nil {
		t.Fatal(err)
	}
	quarantinedLive := cloneGatewayV2RouteState(driver.live)
	driver.events = nil
	if err := manager.WithGatewayV2LANRecoveryGrantFinalization(context.Background(), first,
		func(context.Context, GatewayV2LANGrantObservation) error {
			grants[0].State = appaccess.AppAccessGrantRolledBack
			grants[0].StateSequence = 2
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if err := manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, nil); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(state.Apps[second.AppID], unrelated) ||
		!reflect.DeepEqual(driver.live, quarantinedLive) || containsString(driver.events, "apply:lan-recovery-batch.json") {
		t.Fatalf("unrelated state=%#v live=%#v events=%v err=%v",
			state.Apps[second.AppID], driver.live, driver.events, err)
	}
}

func TestGatewayV2LANRecoveryCallbackFailuresRetainExactHead(t *testing.T) {
	t.Run("grant resolution", func(t *testing.T) {
		manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
		grants := []GatewayV2LANStartupClaim{gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantPrepared, 1)}
		if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, nil); err != nil {
			t.Fatal(err)
		}
		before, _, _ := store.loadBoundUpgrade(journal.OperationID)
		callbackErr := errors.New("injected grant DB failure")
		err := manager.WithGatewayV2LANRecoveryGrantFinalization(context.Background(), request,
			func(context.Context, GatewayV2LANGrantObservation) error { return callbackErr })
		after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if !errors.Is(err, callbackErr) || loadErr != nil || !reflect.DeepEqual(after, before) || driver.gatewayStopped {
			t.Fatalf("err=%v loadErr=%v retained=%t stopped=%t", err, loadErr, reflect.DeepEqual(after, before), driver.gatewayStopped)
		}
	})

	t.Run("disable acknowledgment", func(t *testing.T) {
		manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
		if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
			t.Fatal(err)
		}
		state, _, _ := store.loadBoundUpgrade(journal.OperationID)
		request := *state.LANRecovery.Items[0].Disable
		callbackErr := errors.New("injected clear acknowledgment failure")
		err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
			func(context.Context, GatewayV2LANDisableObservation) error { return nil },
			func(context.Context, GatewayV2LANDisableObservation) error { return callbackErr })
		retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if !errors.Is(err, callbackErr) || loadErr != nil || retained.LANRecovery.Head != 0 ||
			retained.Apps[request.AppID].LAN != nil || driver.gatewayStopped {
			t.Fatalf("err=%v state=%#v loadErr=%v stopped=%t", err, retained.LANRecovery, loadErr, driver.gatewayStopped)
		}
	})
}

func TestGatewayV2LANRecoveryDisableAcknowledgmentReplayAdvancesOnce(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, _ := store.loadBoundUpgrade(journal.OperationID)
	request := *state.LANRecovery.Items[0].Disable
	callbackErr := errors.New("injected acknowledgment failure")
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANDisableObservation) error {
			commitGatewayV2LANRecoveryDisableClaim(t, disables, request.OperationID)
			return nil
		}, func(context.Context, GatewayV2LANDisableObservation) error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("first finalization err=%v", err)
	}
	resolveCalls := 0
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANDisableObservation) error {
			resolveCalls++
			return nil
		}, func(context.Context, GatewayV2LANDisableObservation) error {
			ackGatewayV2LANRecoveryDisableClaim(t, disables, request.OperationID)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.LANRecovery.Head != 1 || resolveCalls != 1 || driver.gatewayStopped {
		t.Fatalf("state=%#v resolve=%d stopped=%t err=%v", retained.LANRecovery, resolveCalls, driver.gatewayStopped, err)
	}
}

func TestGatewayV2LANRecoveryRejectsCrossItemAndHeadSkip(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, _ := store.loadBoundUpgrade(journal.OperationID)
	headRequest := *state.LANRecovery.Items[0].Disable
	nextRequest := *state.LANRecovery.Items[1].Disable
	called := false
	if err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), nextRequest,
		func(context.Context, GatewayV2LANDisableObservation) error { called = true; return nil },
		func(context.Context, GatewayV2LANDisableObservation) error { called = true; return nil }); !IsCode(err, DiagnosticRouteUnresolved) || called {
		t.Fatalf("cross-item err=%v called=%t", err, called)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, state) || driver.gatewayStopped {
		t.Fatalf("head changed after cross-item request: err=%v stopped=%t", err, driver.gatewayStopped)
	}

	commitGatewayV2LANRecoveryDisableClaim(t, disables, nextRequest.OperationID)
	ackGatewayV2LANRecoveryDisableClaim(t, disables, nextRequest.OperationID)
	if head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables); err == nil || !present || head != (GatewayV2LANRecoveryHead{}) {
		t.Fatalf("skipped tail head=%#v present=%t err=%v", head, present, err)
	}
	_ = headRequest
}

func TestGatewayV2LANRecoveryAmbiguousProtectedWriteStopsExactGateway(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, _ := store.loadBoundUpgrade(journal.OperationID)
	request := *state.LANRecovery.Items[0].Disable
	originalWrite := upgradeProtectedWrite
	failed := false
	upgradeProtectedWrite = func(path, purpose string, body []byte) error {
		err := originalWrite(path, purpose, body)
		if err == nil && !failed && purpose == v2RouteStatePurpose {
			failed = true
			return errors.New("injected post-install recovery write failure")
		}
		return err
	}
	t.Cleanup(func() { upgradeProtectedWrite = originalWrite })
	err := manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANDisableObservation) error { return nil },
		func(context.Context, GatewayV2LANDisableObservation) error { return nil })
	if !IsCode(err, DiagnosticRouteUnresolved) || !failed || !driver.gatewayStopped {
		t.Fatalf("err=%v failed=%t stopped=%t", err, failed, driver.gatewayStopped)
	}
	retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || retained.LANRecovery == nil || retained.LANRecovery.Head != 0 {
		t.Fatalf("retained=%#v loadErr=%v", retained.LANRecovery, loadErr)
	}
}

func TestGatewayV2LANRecoveryInspectorRejectsCommittedStaleGrant(t *testing.T) {
	manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
	grants := []GatewayV2LANStartupClaim{gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantPrepared, 1)}
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, nil); err != nil {
		t.Fatal(err)
	}
	grants[0].State = appaccess.AppAccessGrantCommitted
	grants[0].StateSequence = 4
	if head, present, err := manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, nil); err == nil || !present || head != (GatewayV2LANRecoveryHead{}) {
		t.Fatalf("stale committed grant head=%#v present=%t err=%v", head, present, err)
	}
}

func disableRequestForRecoveryOperation(t *testing.T, state gatewayV2RouteState, operationID string) GatewayV2LANDisableRequest {
	t.Helper()
	for _, item := range state.LANRecovery.Items {
		if item.Disable != nil && item.Disable.OperationID == operationID {
			return *item.Disable
		}
	}
	t.Fatalf("missing disable recovery operation %s", operationID)
	return GatewayV2LANDisableRequest{}
}

func commitGatewayV2LANRecoveryDisableClaim(t *testing.T, claims []GatewayV2LANDisableStartupClaim, operationID string) {
	t.Helper()
	for index := range claims {
		if claims[index].Request.OperationID == operationID {
			claims[index].State = appaccess.AppAccessDisableCommitted
			claims[index].StateSequence = 3
			claims[index].RequiresRecovery = false
			return
		}
	}
	t.Fatalf("missing disable claim %s", operationID)
}

func ackGatewayV2LANRecoveryDisableClaim(t *testing.T, claims []GatewayV2LANDisableStartupClaim, operationID string) {
	t.Helper()
	for index := range claims {
		if claims[index].Request.OperationID == operationID {
			claims[index].ClearAcknowledged = true
			return
		}
	}
	t.Fatalf("missing disable claim %s", operationID)
}

func assertGatewayV2LANRecoveryLockHeld(t *testing.T, manager *Manager) {
	t.Helper()
	if manager.mu.TryLock() {
		manager.mu.Unlock()
		t.Fatal("LAN recovery callback ran without the Manager lock")
	}
}
