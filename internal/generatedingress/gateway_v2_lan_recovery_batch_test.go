package generatedingress

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2LANRecoveryBatchQuarantinesTwoPreparedDisables(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery == nil || len(state.LANRecovery.Items) != 2 || state.LANRecovery.Head != 0 {
		t.Fatalf("state=%#v err=%v", state.LANRecovery, err)
	}
	for _, claim := range grants {
		if driver.live.Apps[claim.Request.AppID].LAN != nil {
			t.Fatalf("unsafe route remained live for app %s", claim.Request.AppID)
		}
	}
	if driver.gatewayStopped {
		t.Fatal("successful quarantine stopped the owned gateway")
	}
}

func TestGatewayV2LANRecoveryBatchQuarantinesMixedGrantAndDisable(t *testing.T) {
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
	driver.events = nil
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery == nil || len(state.LANRecovery.Items) != 2 ||
		state.LANRecovery.Items[0].Kind != gatewayV2PendingLANGrant ||
		state.LANRecovery.Items[1].Kind != gatewayV2PendingLANDisable ||
		driver.live.Apps[second.AppID].LAN != nil {
		t.Fatalf("state=%#v live=%#v err=%v", state.LANRecovery, driver.live, err)
	}
}

func TestGatewayV2LANRecoveryBatchPreservesLegacyPendingEvidence(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	grantTwoLANForAccessQueue(t, manager, first, second)
	firstDisable := disableRequestForGrant(t, first)
	secondDisable := distinctGatewayV2LANDisableRequest(t, disableRequestForGrant(t, second))
	authorize, _ := allowGatewayV2LANDisable(t, manager, firstDisable)
	if _, err := manager.DisableGatewayV2LAN(context.Background(), firstDisable, authorize); err != nil {
		t.Fatal(err)
	}
	pending, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || pending.Pending == nil {
		t.Fatalf("pending=%#v err=%v", pending.Pending, err)
	}
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANAccessClaimWithDisable(first, firstDisable.OperationID),
		gatewayV2LANAccessClaimWithDisable(second, secondDisable.OperationID),
	}
	disables := []GatewayV2LANDisableStartupClaim{
		{Request: firstDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
		{Request: secondDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
	}
	driver.events = nil
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery == nil ||
		!reflect.DeepEqual(state.LANRecovery.LegacyPending, pending.Pending) || state.Pending != nil ||
		driver.live.Apps[first.AppID].LAN != nil || driver.live.Apps[second.AppID].LAN != nil {
		t.Fatalf("state=%#v live=%#v err=%v", state, driver.live, err)
	}
}

func TestGatewayV2LANRecoveryBatchResumesAfterIntentAndReloadCrashWindows(t *testing.T) {
	t.Run("intent persisted before reload", func(t *testing.T) {
		manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
		batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
		liveBefore := cloneGatewayV2RouteState(driver.live)
		if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
			t.Fatal(err)
		}
		retained, _, err := store.loadBoundUpgrade(journal.OperationID)
		if err != nil || !reflect.DeepEqual(retained, batch) || reflect.DeepEqual(driver.live, liveBefore) {
			t.Fatalf("retained=%#v live=%#v err=%v", retained.LANRecovery, driver.live, err)
		}
	})

	t.Run("simulated reload-only mixed driver", func(t *testing.T) {
		manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
		batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
		effective, _, err := gatewayV2LANRecoveryEffectiveProjection(batch)
		if err != nil {
			t.Fatal(err)
		}
		driver.live = cloneGatewayV2RouteState(effective)
		driver.pendingTopology = gatewayV2PendingReloadOnlyMixed
		driver.events = nil
		if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
			t.Fatal(err)
		}
		if countEvent(driver.events, "apply:lan-recovery-batch.json") != 1 || driver.gatewayStopped {
			t.Fatalf("events=%v stopped=%t", driver.events, driver.gatewayStopped)
		}
	})

	t.Run("disable committed after batch intent", func(t *testing.T) {
		manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
		persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
		disables[0].State = appaccess.AppAccessDisableCommitted
		disables[0].StateSequence = 3
		if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
			t.Fatal(err)
		}
		if driver.gatewayStopped {
			t.Fatal("exact committed disable replay stopped the owned gateway")
		}
	})
}

func TestGatewayV2LANRecoveryBatchRejectsForgedReplayCensusAndStopsOwnedGateway(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
	err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants[:1], disables[:1])
	if !IsCode(err, DiagnosticRouteUnresolved) || !driver.gatewayStopped {
		t.Fatalf("err=%v stopped=%t events=%v", err, driver.gatewayStopped, driver.events)
	}
	retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(retained, batch) {
		t.Fatalf("retained=%#v loadErr=%v", retained.LANRecovery, loadErr)
	}
}

func TestGatewayV2LANRecoveryBatchStopsOwnedGatewayOnInvalidReplayCensus(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
	// The second disable still names its source grant, but that grant row is
	// absent from the database snapshot. The old gateway may still be live.
	err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants[:1], disables)
	if !IsCode(err, DiagnosticRouteUnresolved) || !driver.gatewayStopped {
		t.Fatalf("invalid replay census err=%v stopped=%t", err, driver.gatewayStopped)
	}
	retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(retained, batch) {
		t.Fatalf("protected batch changed after invalid census: err=%v", loadErr)
	}
}

func TestGatewayV2LANRecoveryBatchStopsOwnedGatewayOnInterfaceDriftAfterIntent(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
	driver.failSelectedPreflightAt = driver.selectedPreflightCalls + 1
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("interface drift err=%v", err)
	}
	if !driver.gatewayStopped {
		t.Fatal("persisted batch intent left its pre-quarantine gateway live after interface drift")
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(retained, batch) {
		t.Fatalf("protected batch changed after emergency stop: err=%v", err)
	}
}

func TestGatewayV2LANRecoveryBatchStopsOwnedGatewayWhenAbsenceProofFails(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	driver.failRollbackProof = true
	err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables)
	if !IsCode(err, DiagnosticRouteUnresolved) || !driver.gatewayStopped {
		t.Fatalf("err=%v stopped=%t events=%v", err, driver.gatewayStopped, driver.events)
	}
	state, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || state.LANRecovery == nil {
		t.Fatalf("state=%#v loadErr=%v", state.LANRecovery, loadErr)
	}
}

func TestGatewayV2LANRecoveryBatchRejectsPartialLiveTopology(t *testing.T) {
	manager, store, journal, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	batch := persistGatewayV2LANRecoveryBatchIntent(t, manager, store, journal, grants, disables)
	effective, _, err := gatewayV2LANRecoveryEffectiveProjection(batch)
	if err != nil {
		t.Fatal(err)
	}
	// One unsafe route is still published while the other is absent. This is
	// neither a complete pre-quarantine nor a complete effective state.
	firstApp := grants[0].Request.AppID
	app := effective.Apps[firstApp]
	app.LAN = cloneGatewayV2LANBinding(batch.Apps[firstApp].LAN)
	effective.Apps[firstApp] = app
	driver.live = effective
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); !IsCode(err, DiagnosticRouteUnresolved) || !driver.gatewayStopped {
		t.Fatalf("partial live topology err=%v stopped=%t", err, driver.gatewayStopped)
	}
}

func TestGatewayV2LANRecoveryBatchProvesEveryUnsafePort(t *testing.T) {
	manager, _, _, driver, grants, disables := gatewayV2LANRecoveryTwoDisableFixture(t)
	recording := &recordingGatewayV2LANRecoveryPortDriver{fakeGatewayV2LANGrantDriver: driver}
	manager.gatewayV2LANGrantDriver = recording
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	want := []uint16{disables[0].Request.Port, disables[1].Request.Port}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(recording.probedPorts, want) {
		t.Fatalf("404 probes=%v want=%v", recording.probedPorts, want)
	}
}

func TestGatewayV2LANRecoveryBatchPreservesUnrelatedCommittedRoute(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorize, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorize); err != nil {
		t.Fatal(err)
	}
	secondBinding := cloneGatewayV2LANBinding(driver.live.Apps[second.AppID].LAN)
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantPrepared, 1),
		gatewayV2LANStartupClaim(second, appaccess.AppAccessGrantCommitted, 4),
	}
	driver.events = nil
	if err := manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, nil); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || state.LANRecovery == nil || len(state.LANRecovery.Items) != 1 ||
		!reflect.DeepEqual(driver.live.Apps[second.AppID].LAN, secondBinding) ||
		driver.live.Apps[first.AppID].LAN != nil {
		t.Fatalf("state=%#v live=%#v err=%v", state.LANRecovery, driver.live, err)
	}
}

func gatewayV2LANRecoveryTwoDisableFixture(t *testing.T) (*Manager, *gatewayUpgradeStateStore,
	gatewayMigrationJournal, *fakeGatewayV2LANGrantDriver, []GatewayV2LANStartupClaim,
	[]GatewayV2LANDisableStartupClaim,
) {
	t.Helper()
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	grantTwoLANForAccessQueue(t, manager, first, second)
	firstDisable := disableRequestForGrant(t, first)
	secondDisable := distinctGatewayV2LANDisableRequest(t, disableRequestForGrant(t, second))
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANAccessClaimWithDisable(first, firstDisable.OperationID),
		gatewayV2LANAccessClaimWithDisable(second, secondDisable.OperationID),
	}
	disables := []GatewayV2LANDisableStartupClaim{
		{Request: firstDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
		{Request: secondDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
	}
	driver.events = nil
	return manager, store, journal, driver, grants, disables
}

func persistGatewayV2LANRecoveryBatchIntent(t *testing.T, manager *Manager, store *gatewayUpgradeStateStore,
	journal gatewayMigrationJournal, grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) gatewayV2RouteState {
	t.Helper()
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	inspection, err := inspectGatewayV2LANAccessStartupLocked(context.Background(), state, journal, claims,
		manager.gatewayV2LANDisableDriver())
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	items, err := gatewayV2LANRecoveryItems(inspection, claims)
	if err != nil {
		t.Fatal(err)
	}
	batch := cloneGatewayV2RouteState(state)
	batch.Pending = nil
	batch.LANRecovery = &gatewayV2LANRecoveryBatch{
		Items: items, LegacyPending: cloneGatewayV2PendingRoute(state.Pending),
	}
	if err := store.saveCommittedV2State(batch, journal); err != nil {
		t.Fatal(err)
	}
	return batch
}

func cloneGatewayV2LANBinding(binding *gatewayV2LANBinding) *gatewayV2LANBinding {
	if binding == nil {
		return nil
	}
	clone := *binding
	return &clone
}

func (d *fakeGatewayV2LANGrantDriver) observeLANRecoveryBatch(_ context.Context, state,
	effective gatewayV2RouteState, _ gatewayMigrationJournal,
) gatewayV2LANRecoveryBatchTopology {
	d.checkLocked()
	d.events = append(d.events, "observe_recovery_batch")
	if d.pendingTopology == gatewayV2PendingReloadOnlyMixed && gatewayV2LANRecoveryRuntimeEqual(d.live, effective) {
		return gatewayV2LANRecoveryBatchReloadMixed
	}
	if gatewayV2LANRecoveryRuntimeEqual(d.live, effective) {
		return gatewayV2LANRecoveryBatchEffectiveExact
	}
	before, err := gatewayV2LANRecoveryBeforeProjections(state)
	if err != nil {
		return gatewayV2LANRecoveryBatchUnknown
	}
	for _, candidate := range before {
		if gatewayV2LANRecoveryRuntimeEqual(d.live, candidate) {
			return gatewayV2LANRecoveryBatchBeforeExact
		}
	}
	return gatewayV2LANRecoveryBatchUnknown
}

func gatewayV2LANRecoveryRuntimeEqual(left, right gatewayV2RouteState) bool {
	left = gatewayV2LANRecoveryCommittedProjection(left)
	right = gatewayV2LANRecoveryCommittedProjection(right)
	return reflect.DeepEqual(left, right)
}

var _ gatewayV2LANRecoveryBatchDriver = (*fakeGatewayV2LANGrantDriver)(nil)

type recordingGatewayV2LANRecoveryPortDriver struct {
	*fakeGatewayV2LANGrantDriver
	probedPorts []uint16
}

func (d *recordingGatewayV2LANRecoveryPortDriver) proveRolledBack(ctx context.Context,
	state gatewayV2RouteState, journal gatewayMigrationJournal, request gatewayV2LANGrantRequest,
) bool {
	d.probedPorts = append(d.probedPorts, request.Port)
	for _, app := range state.Apps {
		if app.LAN != nil && app.LAN.Port == request.Port {
			return false
		}
	}
	return d.fakeGatewayV2LANGrantDriver.proveRolledBack(ctx, state, journal, request)
}

var _ gatewayV2LANRecoveryBatchDriver = (*recordingGatewayV2LANRecoveryPortDriver)(nil)
