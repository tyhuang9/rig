package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestGatewayCurrentStateMachineBuildsExactDurableTransitions(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	before := cloneGatewayCurrentOperationState(fixture.baseline)
	transferredAppID, transferred := routeOperationTransferredApp(t, before)

	switchTransition, err := gatewayCurrentSwitchTransition(before,
		routeOperationSwitchRequest(t, transferredAppID, transferred.Route))
	if err != nil || switchTransition.Pending.Revision != before.Revision+1 ||
		switchTransition.Effective.Revision != before.Revision+2 ||
		switchTransition.Pending.Pending == nil ||
		!reflect.DeepEqual(switchTransition.Pending.Apps, before.Apps) ||
		!reflect.DeepEqual(switchTransition.Effective.Apps[transferredAppID].LAN, transferred.LAN) ||
		switchTransition.Effective.TransferManifestDigest != before.TransferManifestDigest {
		t.Fatalf("route switch transition lost durable origin: %#v error=%v", switchTransition, err)
	}
	recoveredSwitch, err := gatewayCurrentTransitionFromPending(switchTransition.Pending)
	if err != nil || !reflect.DeepEqual(recoveredSwitch, switchTransition) {
		t.Fatalf("route switch pending did not reconstruct exact transition: error=%v", err)
	}

	appID := uuid.NewString()
	seed := transferred.Route
	create, err := gatewayCurrentSwitchTransition(before, generatedruntime.RouteSwitchRequest{
		AppID: appID, ToSlot: generatedruntime.SlotBlue,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), seed.Endpoints...),
	})
	if err != nil {
		t.Fatal(err)
	}
	grantRequest := routeOperationNativeGrantRequest(t, create.Effective, appID)
	grant, activationPending, err := gatewayCurrentGrantTransition(create.Effective, grantRequest)
	if err != nil || grant.Pending.Pending == nil || grant.Pending.Pending.ActivationUncertain ||
		activationPending.Pending == nil || !activationPending.Pending.ActivationUncertain ||
		grant.Pending.Revision != create.Effective.Revision+1 ||
		grant.Effective.Revision != grant.Pending.Revision+1 ||
		activationPending.Revision != grant.Pending.Revision+1 ||
		!reflect.DeepEqual(grant.Pending.Apps, create.Effective.Apps) ||
		grant.Effective.Apps[appID].LAN == nil || grant.Effective.Apps[appID].LAN.Transfer != nil {
		t.Fatalf("LAN grant transition does not preserve the two durable pending phases: %#v %#v error=%v",
			grant, activationPending, err)
	}
	activation, err := gatewayCurrentGrantActivationTransition(grant, activationPending)
	if err != nil || activation.Effective.Revision != activationPending.Revision+1 ||
		activation.Effective.Apps[appID].LAN == nil ||
		!reflect.DeepEqual(activation.Before, grant.Before) ||
		!reflect.DeepEqual(activation.Pending, activationPending) {
		t.Fatalf("grant activation transition is not bound to R/R+2/R+3: %#v error=%v", activation, err)
	}

	disableRequest := disableRequestForGrant(t,
		gatewayV2LANGrantRequestForBinding(transferredAppID, transferred.LAN.Raw))
	disable, err := gatewayCurrentDisableTransition(before, disableRequest)
	if err != nil || disable.Pending.Pending == nil || !disable.Pending.Pending.ActivationUncertain ||
		disable.Pending.Pending.Disable == nil || disable.Effective.Apps[transferredAppID].LAN != nil ||
		disable.Effective.TransferManifestDigest != before.TransferManifestDigest {
		t.Fatalf("LAN disable transition lost request or immutable transfer history: %#v error=%v", disable, err)
	}
	recoveredDisable, err := gatewayCurrentTransitionFromPending(disable.Pending)
	if err != nil || !reflect.DeepEqual(recoveredDisable, disable) {
		t.Fatalf("LAN disable pending did not reconstruct exact transition: error=%v", err)
	}
}

func TestGatewayCurrentStateMachineRejectsCrossProfileGrantAndWrongDisableSource(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	before := cloneGatewayCurrentOperationState(fixture.baseline)
	appID, app := routeOperationTransferredApp(t, before)
	request := routeOperationNativeGrantRequest(t, before, appID)
	request.GatewayProfileRevisionID = uuid.NewString()
	if _, _, err := gatewayCurrentGrantTransition(before, request); !IsCode(err, DiagnosticRouteInvalid) {
		t.Fatalf("cross-profile current grant accepted: %v", err)
	}

	disable := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw))
	wrong := *disable.SourceGrant
	wrong.AllocationID = uuid.NewString()
	disable.SourceGrant = &wrong
	if _, err := gatewayCurrentDisableTransition(before, disable); err == nil {
		t.Fatalf("wrong raw disable source accepted: %v", err)
	}
	collisionBinding := app.LAN.Raw
	collisionAppID := uuid.NewString()
	accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: collisionAppID, AllocationID: collisionBinding.AllocationID, Port: collisionBinding.Port,
		GatewayProfileRevisionID:     collisionBinding.ProfileRevisionID,
		GatewayProfileRevisionNumber: collisionBinding.ProfileRevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	collisionBinding.AccessSpecDigest = accessDigest
	collision := disableRequestForGrant(t,
		gatewayV2LANGrantRequestForBinding(collisionAppID, collisionBinding))
	if !validGatewayV2LANDisableRequest(collision) || gatewayCurrentDisableBindingAbsent(before, collision) {
		t.Fatal("foreign app disable identity was reported absent")
	}
}

func TestGatewayCurrentStateMachineReplaysTransferredGrantWithoutRawProfileRewrite(t *testing.T) {
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
	request := gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw)
	authorize, leases := allowGatewayV2LANGrant(t, fixture.manager, request, nil)
	result, err := fixture.manager.GrantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || result.Receipt.Request != request ||
		result.Receipt.ProtectedStateDigest != fixture.baseline.Digest || len(*leases) != 1 ||
		(*leases)[0].revalidateCalls != 1 || (*leases)[0].activateCalls != 1 ||
		(*leases)[0].releaseCalls != 1 || driver.applyCalls != 0 || driver.attestCalls != 2 {
		t.Fatalf("transferred grant replay changed raw/effective authority: result=%#v leases=%#v calls=%d/%d err=%v",
			result, *leases, driver.applyCalls, driver.attestCalls, err)
	}
	retained := routeOperationLoad(t, fixture.store)
	if !reflect.DeepEqual(retained, fixture.baseline) {
		t.Fatal("transferred grant replay mutated protected current state")
	}
	observation, err := fixture.manager.ObserveGatewayV2LAN(context.Background(), request)
	if err != nil || observation.Request != request || observation.Disposition != GatewayV2LANGrantCommitted ||
		observation.EffectiveBinding.ProtectedLineage != fixture.baseline.Lineage ||
		observation.EffectiveBinding.TransferChainTipDigest != app.LAN.Transfer.TransferDigest ||
		observation.EffectiveBinding.TerminalReceiptDigest != fixture.baseline.Lineage.TerminalReceiptDigest {
		t.Fatalf("transferred grant observation lost immutable raw or effective proof: %#v error=%v",
			observation, err)
	}
	changed := snapshot
	changed.RollbackAllowed = !snapshot.RollbackAllowed
	if err := fixture.manager.WithGatewayV2LANObservation(context.Background(), request,
		func(context.Context, GatewayV2LANGrantObservation) error {
			repository.snapshots = []appaccess.GatewayRebindRecoverySnapshot{changed}
			return nil
		}); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("LAN observation accepted SQL authority drift across its callback: %v", err)
	}
	if retained := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(retained, fixture.baseline) {
		t.Fatal("read-only LAN observation drift mutated protected current state")
	}
}

func TestGatewayCurrentStateMachineGrantCommitsAndQuarantinesFailedResolution(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	appID := uuid.NewString()
	transferredAppID, seed := routeOperationTransferredApp(t, fixture.baseline)
	baseline := cloneGatewayCurrentOperationState(fixture.baseline)
	baseline.Apps[appID] = gatewayCurrentAppRoute{Route: routeRecord{Slot: generatedruntime.SlotBlue,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), seed.Route.Endpoints...)}}
	baseline.Digest, _ = gatewayCurrentRouteStateDigest(baseline)
	if !validGatewayCurrentRouteState(baseline) || fixture.store.installBaseline(baseline) != nil {
		t.Fatal("install current grant baseline")
	}
	fixture.baseline = baseline
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	request := routeOperationNativeGrantRequest(t, baseline, appID)
	authorize, leases := allowGatewayV2LANGrant(t, fixture.manager, request, nil)
	result, err := fixture.manager.GrantGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || len(*leases) != 1 || (*leases)[0].revalidateCalls != 2 ||
		(*leases)[0].activateCalls != 1 || (*leases)[0].releaseCalls != 1 ||
		driver.applyCalls != 2 || driver.attestCalls != 1 {
		t.Fatalf("current grant did not execute R/R+1/R+2/R+3 protocol: result=%#v leases=%#v calls=%d/%d err=%v",
			result, *leases, driver.applyCalls, driver.attestCalls, err)
	}
	committed := routeOperationLoad(t, fixture.store)
	if committed.Revision != baseline.Revision+3 || committed.Pending != nil ||
		committed.Apps[appID].LAN == nil || committed.Apps[appID].LAN.Transfer != nil ||
		!reflect.DeepEqual(committed.Apps[transferredAppID].LAN, seed.LAN) ||
		result.Receipt.ProtectedStateDigest != committed.Digest ||
		result.Receipt.GatewayOperationID != committed.Lineage.OperationID {
		t.Fatalf("current grant committed wrong protected state: %#v result=%#v", committed, result)
	}

	callbackErr := errors.New("injected terminal callback failure")
	if err := fixture.manager.WithGatewayV2LANCommitResolution(context.Background(), request,
		func(context.Context, GatewayV2LANGrantReceipt) error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("failed terminal callback was not returned after quarantine: %v", err)
	}
	retained := routeOperationLoad(t, fixture.store)
	if retained.Revision != committed.Revision+1 || retained.Pending == nil ||
		retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
		retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil ||
		!reflect.DeepEqual(retained.Pending.Previous.LAN.Raw, committed.Apps[appID].LAN.Raw) ||
		driver.applyCalls != 3 {
		t.Fatalf("failed terminal callback did not retain exact withdrawn marker: %#v", retained)
	}
	driver.pendingAttestOutcome = gatewayCurrentPhysicalRecoveryEffective
	observation, err := fixture.manager.ObserveGatewayV2LAN(context.Background(), request)
	if err != nil || observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
		!observation.ActivationUncertain || observation.Request != request ||
		observation.EffectiveBinding.ProtectedLineage != committed.Lineage {
		t.Fatalf("withdrawn current observation lost exact authority: %#v error=%v", observation, err)
	}
	rolledBack := false
	if err := fixture.manager.WithGatewayV2LANRollbackResolution(context.Background(), request,
		func(_ context.Context, value GatewayV2LANGrantObservation) error {
			rolledBack = true
			if value.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation || value.Request != request {
				t.Fatalf("unexpected rollback observation: %#v", value)
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	cleared := routeOperationLoad(t, fixture.store)
	if !rolledBack || cleared.Pending != nil || cleared.Apps[appID].LAN != nil ||
		cleared.TransferManifestDigest != baseline.TransferManifestDigest {
		t.Fatalf("grant rollback did not retain exact no-LAN current state: %#v", cleared)
	}
}

func TestGatewayCurrentStateMachineGrantActivationFailureRestoresBeforeAndRetainsMarker(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	appID := uuid.NewString()
	_, seed := routeOperationTransferredApp(t, fixture.baseline)
	baseline := cloneGatewayCurrentOperationState(fixture.baseline)
	baseline.Apps[appID] = gatewayCurrentAppRoute{Route: routeRecord{Slot: generatedruntime.SlotBlue,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), seed.Route.Endpoints...)}}
	baseline.Digest, _ = gatewayCurrentRouteStateDigest(baseline)
	if fixture.store.installBaseline(baseline) != nil {
		t.Fatal("install current grant rollback baseline")
	}
	fixture.baseline = baseline
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	request := routeOperationNativeGrantRequest(t, baseline, appID)
	authorize, _ := allowGatewayV2LANGrant(t, fixture.manager, request, func(lease *fakeGatewayV2LANGrantLease) {
		lease.activateErr = errors.New("ambiguous activate")
	})
	if _, err := fixture.manager.GrantGatewayV2LAN(context.Background(), request, authorize); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("ambiguous activation did not fail closed: %v", err)
	}
	retained := routeOperationLoad(t, fixture.store)
	if retained.Revision != baseline.Revision+2 || retained.Pending == nil ||
		retained.Pending.Kind != gatewayV2PendingLANGrant || !retained.Pending.ActivationUncertain ||
		retained.Apps[appID].LAN != nil || driver.restoreCalls != 1 || driver.stopCalls != 0 {
		t.Fatalf("activation failure did not restore no-LAN before and retain R+2 marker: %#v calls=%d/%d",
			retained, driver.restoreCalls, driver.stopCalls)
	}
	validated := false
	if err := fixture.manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
		func(_ context.Context, value GatewayV2LANGrantObservation) error {
			validated = true
			if value.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
				!value.ActivationUncertain || value.Request != request {
				t.Fatalf("unexpected commit recovery observation: %#v", value)
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	effective := routeOperationLoad(t, fixture.store)
	if !validated || effective.Pending != nil || effective.Apps[appID].LAN == nil ||
		effective.Apps[appID].LAN.Transfer != nil || effective.Revision != baseline.Revision+3 {
		t.Fatalf("commit recovery did not publish exact authorized current grant: %#v", effective)
	}
}

func TestGatewayCurrentStateMachineGrantReconcilesCanceledAndLostMarkerWrites(t *testing.T) {
	t.Run("canceled after physical publication", func(t *testing.T) {
		fixture, driver, request := newGatewayCurrentGrantOperationFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		driver.afterApply = cancel
		authorize, _ := allowGatewayV2LANGrant(t, fixture.manager, request, nil)
		if _, err := fixture.manager.GrantGatewayV2LAN(ctx, request, authorize); err == nil {
			t.Fatal("canceled grant returned success after physical publication")
		}
		retained := routeOperationLoad(t, fixture.store)
		if retained.Revision != fixture.baseline.Revision+1 || retained.Pending == nil ||
			retained.Pending.Kind != gatewayV2PendingLANGrant || retained.Pending.ActivationUncertain ||
			retained.Apps[request.AppID].LAN != nil || driver.applyCalls != 1 ||
			driver.restoreCalls != 1 || driver.stopCalls != 0 {
			t.Fatalf("canceled grant did not restore before with exact R+1 marker: %#v calls=%d/%d/%d",
				retained, driver.applyCalls, driver.restoreCalls, driver.stopCalls)
		}
		if err := fixture.manager.Recover(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("generic recovery cleared request-bound LAN pending state: %v", err)
		}
		if after := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(after, retained) {
			t.Fatal("generic recovery mutated request-bound LAN pending state")
		}
		wrongRequest := request
		wrongRequest.AttemptID = uuid.NewString()
		applyCalls, restoreCalls, stopCalls := driver.applyCalls, driver.restoreCalls, driver.stopCalls
		if _, err := fixture.manager.RecoverGatewayV2LAN(context.Background(), wrongRequest); err == nil {
			t.Fatal("request-mismatched recovery mutated another retained LAN grant")
		}
		if after := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(after, retained) ||
			driver.applyCalls != applyCalls || driver.restoreCalls != restoreCalls || driver.stopCalls != stopCalls {
			t.Fatalf("request mismatch crossed the physical boundary: %#v calls=%d/%d/%d",
				after, driver.applyCalls, driver.restoreCalls, driver.stopCalls)
		}
		recovery, err := fixture.manager.RecoverGatewayV2LAN(context.Background(), request)
		if err != nil || recovery.Observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation ||
			recovery.Observation.ActivationUncertain || recovery.Observation.Request != request ||
			driver.restoreCalls != 2 {
			t.Fatalf("request-bound recovery did not retain exact R+1 rollback evidence: %#v calls=%d error=%v",
				recovery, driver.restoreCalls, err)
		}
		if after := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(after, retained) {
			t.Fatal("request-bound observation mutated retained R+1 marker")
		}
	})

	t.Run("activation marker save acknowledged late", func(t *testing.T) {
		fixture, driver, request := newGatewayCurrentGrantOperationFixture(t)
		repository := fixture.manager.options.RebindCurrentStateRepository.(*routeOperationSnapshotRepository)
		repository.errorAt = make(map[int]error)
		driver.afterApply = func() {
			// persistGatewayCurrentExactLocked performs two complete three-read
			// selections before saveNext, then starts its post-write readback.
			repository.errorAt[repository.calls+7] = errors.New("injected post-write readback failure")
		}
		authorize, _ := allowGatewayV2LANGrant(t, fixture.manager, request, nil)
		if _, err := fixture.manager.GrantGatewayV2LAN(context.Background(), request, authorize); err == nil {
			t.Fatal("lost activation-marker write acknowledgment returned success")
		}
		retained := routeOperationLoad(t, fixture.store)
		if retained.Revision != fixture.baseline.Revision+2 || retained.Pending == nil ||
			retained.Pending.Kind != gatewayV2PendingLANGrant || !retained.Pending.ActivationUncertain ||
			retained.Apps[request.AppID].LAN != nil || driver.applyCalls != 1 ||
			driver.restoreCalls != 1 || driver.stopCalls != 0 {
			t.Fatalf("lost marker acknowledgment did not reconcile exact retained R+2: %#v calls=%d/%d/%d",
				retained, driver.applyCalls, driver.restoreCalls, driver.stopCalls)
		}
	})

	t.Run("rollback callback is fenced by a fresh retained marker read", func(t *testing.T) {
		fixture, driver, request := newGatewayCurrentGrantOperationFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		driver.afterApply = cancel
		authorize, _ := allowGatewayV2LANGrant(t, fixture.manager, request, nil)
		if _, err := fixture.manager.GrantGatewayV2LAN(ctx, request, authorize); err == nil {
			t.Fatal("test grant unexpectedly completed")
		}
		retained := routeOperationLoad(t, fixture.store)
		replaced, err := gatewayCurrentNextState(retained, func(*gatewayCurrentRouteState) {})
		if err != nil {
			t.Fatal(err)
		}
		driver.afterRestore = func() {
			if err := fixture.store.saveNext(retained, replaced); err != nil {
				t.Fatal(err)
			}
		}
		called := false
		if err := fixture.manager.WithGatewayV2LANRollbackResolution(context.Background(), request,
			func(context.Context, GatewayV2LANGrantObservation) error {
				called = true
				return nil
			}); err == nil {
			t.Fatal("rollback accepted protected replacement after physical proof")
		}
		if called {
			t.Fatal("rollback callback ran after protected replacement")
		}
		after := routeOperationLoad(t, fixture.store)
		if after.Revision != replaced.Revision || !reflect.DeepEqual(after.Pending, retained.Pending) {
			t.Fatalf("rollback did not retain request-bound marker after replacement: %#v", after)
		}
	})
}

func TestGatewayCurrentStateMachineQuarantinesLostPostCommitProof(t *testing.T) {
	fixture, driver, request := newGatewayCurrentGrantOperationFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, fixture.manager, request, nil)
	if _, err := fixture.manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.WithGatewayV2LANCommitResolution(context.Background(), request,
		func(context.Context, GatewayV2LANGrantReceipt) error {
			driver.failAttest = true
			return nil
		}); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("lost post-commit proof did not fail closed: %v", err)
	}
	driver.failAttest = false
	retained := routeOperationLoad(t, fixture.store)
	if retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
		retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil ||
		retained.Apps[request.AppID].LAN == nil || driver.applyCalls != 3 {
		t.Fatalf("lost post-commit proof did not quarantine exact binding: %#v applyCalls=%d",
			retained, driver.applyCalls)
	}
}

func TestGatewayCurrentStateMachineDisablesTransferredBindingAndRetainsManifest(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	request := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw))
	authorize, lease := allowGatewayV2LANDisable(t, fixture.manager, request)
	result, err := fixture.manager.DisableGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || lease.revalidations != 1 || lease.withdrawals != 1 || lease.releases != 1 ||
		driver.applyCalls != 1 {
		t.Fatalf("transferred disable did not enter exact pending state: result=%#v lease=%#v calls=%d error=%v",
			result, lease, driver.applyCalls, err)
	}
	retained := routeOperationLoad(t, fixture.store)
	if retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANDisable ||
		retained.Pending.Disable == nil || !reflect.DeepEqual(*retained.Pending.Disable, request) ||
		retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil ||
		retained.Pending.Previous.LAN.Transfer == nil ||
		result.Receipt.ProtectedStateDigest != retained.Digest {
		t.Fatalf("transferred disable lost raw/transfer identity: %#v result=%#v", retained, result)
	}
	resolved, acknowledged := false, false
	authorizeResolution := func(_ context.Context, got GatewayV2LANDisableRequest) error {
		if !reflect.DeepEqual(got, request) {
			t.Fatal("disable resolution request changed")
		}
		return nil
	}
	resolve := func(_ context.Context, observation GatewayV2LANDisableObservation) error {
		resolved = true
		if observation.Disposition != GatewayV2LANDisableWithdrawnPending ||
			!reflect.DeepEqual(observation.Request, request) {
			t.Fatalf("unexpected disable resolution observation: %#v", observation)
		}
		return nil
	}
	acknowledge := func(_ context.Context, observation GatewayV2LANDisableObservation) error {
		acknowledged = true
		if observation.Disposition != GatewayV2LANDisableDisabled ||
			!reflect.DeepEqual(observation.Request, request) {
			t.Fatalf("unexpected disable acknowledgment: %#v", observation)
		}
		return nil
	}
	if err := fixture.manager.WithGatewayV2LANDisableFinalization(context.Background(), request,
		authorizeResolution, resolve, acknowledge); err != nil {
		t.Fatal(err)
	}
	effective := routeOperationLoad(t, fixture.store)
	if !resolved || !acknowledged || effective.Pending != nil || effective.Apps[appID].LAN != nil ||
		effective.TransferManifestDigest != fixture.baseline.TransferManifestDigest ||
		!gatewayCurrentTransfersMatchState(snapshot.CurrentTransfers, effective) ||
		driver.applyCalls != 2 || driver.attestCalls != 1 {
		t.Fatalf("transferred disable finalization changed immutable manifest: %#v resolved=%t/%t calls=%d/%d",
			effective, resolved, acknowledged, driver.applyCalls, driver.attestCalls)
	}
}

func TestGatewayCurrentStateMachineDisableRetainsResolutionAndAcknowledgmentFailures(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	request := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw))
	authorize, _ := allowGatewayV2LANDisable(t, fixture.manager, request)
	if _, err := fixture.manager.DisableGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	authorizeResolution := func(context.Context, GatewayV2LANDisableRequest) error { return nil }
	resolutionErr := errors.New("injected disable resolution failure")
	if err := fixture.manager.WithGatewayV2LANDisableFinalization(context.Background(), request,
		authorizeResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { return resolutionErr },
		func(context.Context, GatewayV2LANDisableObservation) error {
			t.Fatal("acknowledgment ran after failed resolution")
			return nil
		}); !errors.Is(err, resolutionErr) {
		t.Fatalf("disable resolution failure was not returned: %v", err)
	}
	retained := routeOperationLoad(t, fixture.store)
	if retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANDisable ||
		retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil ||
		!reflect.DeepEqual(retained.Pending.Previous.LAN.Raw, app.LAN.Raw) {
		t.Fatalf("failed disable resolution did not retain exact withdrawn marker: %#v", retained)
	}

	acknowledgmentErr := errors.New("injected disable acknowledgment failure")
	if err := fixture.manager.WithGatewayV2LANDisableFinalization(context.Background(), request,
		authorizeResolution,
		func(_ context.Context, observation GatewayV2LANDisableObservation) error {
			if observation.Disposition != GatewayV2LANDisableWithdrawnPending {
				t.Fatalf("unexpected pending resolution observation: %#v", observation)
			}
			return nil
		},
		func(context.Context, GatewayV2LANDisableObservation) error { return acknowledgmentErr },
	); !errors.Is(err, acknowledgmentErr) {
		t.Fatalf("disable acknowledgment failure was not returned: %v", err)
	}
	effective := routeOperationLoad(t, fixture.store)
	if effective.Pending != nil || effective.Apps[appID].LAN != nil ||
		effective.TransferManifestDigest != fixture.baseline.TransferManifestDigest {
		t.Fatalf("failed acknowledgment did not retain exact disabled current state: %#v", effective)
	}

	acknowledged := false
	if err := fixture.manager.WithGatewayV2LANDisableFinalization(context.Background(), request,
		authorizeResolution,
		func(_ context.Context, observation GatewayV2LANDisableObservation) error {
			if observation.Disposition != GatewayV2LANDisableDisabled {
				t.Fatalf("stable retry did not observe disabled state: %#v", observation)
			}
			return nil
		},
		func(_ context.Context, observation GatewayV2LANDisableObservation) error {
			acknowledged = true
			if observation.Disposition != GatewayV2LANDisableDisabled {
				t.Fatalf("unexpected stable acknowledgment: %#v", observation)
			}
			return nil
		}); err != nil || !acknowledged {
		t.Fatalf("stable disabled retry did not complete acknowledgment: acknowledged=%t error=%v", acknowledged, err)
	}
	confirmed := routeOperationLoad(t, fixture.store)
	if !reflect.DeepEqual(confirmed, effective) {
		t.Fatal("stable disabled retry mutated protected current state")
	}
}

func newGatewayCurrentGrantOperationFixture(t *testing.T) (
	gatewayCurrentStateFixture, *gatewayCurrentStateMachineDriver, GatewayV2LANGrantRequest,
) {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentPublicManagerTest(fixture.manager)
	appID := uuid.NewString()
	_, seed := routeOperationTransferredApp(t, fixture.baseline)
	baseline := cloneGatewayCurrentOperationState(fixture.baseline)
	baseline.Apps[appID] = gatewayCurrentAppRoute{Route: routeRecord{Slot: generatedruntime.SlotBlue,
		Endpoints: append([]generatedruntime.RouteEndpoint(nil), seed.Route.Endpoints...)}}
	baseline.Digest = ""
	baseline.Digest, _ = gatewayCurrentRouteStateDigest(baseline)
	if !validGatewayCurrentRouteState(baseline) || fixture.store.installBaseline(baseline) != nil {
		t.Fatal("install current grant operation baseline")
	}
	fixture.baseline = baseline
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = &routeOperationSnapshotRepository{
		snapshots: []appaccess.GatewayRebindRecoverySnapshot{snapshot},
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: terminal}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	return fixture, driver, routeOperationNativeGrantRequest(t, baseline, appID)
}
