package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentLANBatchObserverDriver struct {
	*gatewayCurrentPhysicalDriverFake
	t       *testing.T
	partial bool
	before  func(gatewayCurrentLANRecoveryPhysicalAction)
	calls   int
	last    gatewayCurrentLANRecoveryPhysicalAction
}

func (d *gatewayCurrentLANBatchObserverDriver) attestGatewayCurrentLANRecoveryBatch(_ context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	d.calls++
	d.last = action
	result := gatewayCurrentLANRecoveryPhysicalResultFixture(d.t, action, true)
	if d.partial {
		if len(action.Selected.LANRecovery.Items) < 2 {
			d.t.Fatal("partial proof requires at least two batch items")
		}
		state := cloneGatewayCurrentRouteState(action.Before)
		appID := action.Selected.LANRecovery.Items[0].AppID
		app := state.Apps[appID]
		app.LAN = nil
		state.Apps[appID] = app
		state.Digest, _ = gatewayCurrentRouteStateDigest(state)
		result.BatchAbsent, result.Attestation.State = false, state
		result.Attestation.Digest, _ = gatewayCurrentPhysicalAttestationDigest(result.Attestation)
		result.Digest, _ = gatewayCurrentLANRecoveryPhysicalResultDigest(result)
	}
	if !validGatewayCurrentLANRecoveryPhysicalResult(action, result) {
		d.t.Fatal("observer fixture must return a valid complete or partial physical result")
	}
	if d.before != nil {
		d.before(action)
	}
	return result, nil
}

func (*gatewayCurrentLANBatchObserverDriver) withdrawGatewayCurrentLANRecoveryBatch(context.Context,
	gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	return gatewayCurrentLANRecoveryPhysicalResult{}, errors.New("observation must not withdraw a batch")
}

func gatewayCurrentLANBatchObservationFixture(t *testing.T) (gatewayCurrentStateFixture,
	appaccess.GatewayRebindRecoverySnapshot, []GatewayV2LANStartupClaim,
	[]GatewayV2LANDisableStartupClaim, gatewayCurrentRouteState,
) {
	t.Helper()
	f, snapshot, _, _ := gatewayRebindCurrentStartupFixture(t)
	state := cloneGatewayCurrentRouteState(f.baseline)
	_, seed := routeOperationTransferredApp(t, state)
	const nativeAppID = "79797979-7979-4797-8797-797979797979"
	request := routeOperationNativeGrantRequest(t, state, nativeAppID)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	native := cloneGatewayCurrentAppRoute(seed)
	native.LAN = &gatewayCurrentLANBinding{Raw: raw}
	state.Apps[nativeAppID] = native
	state.Revision++
	state.Digest, _ = gatewayCurrentRouteStateDigest(state)
	if err := f.store.saveNext(f.baseline, state); err != nil {
		t.Fatal(err)
	}
	f.baseline = state
	grants := gatewayCurrentStartupGrants(t, f.baseline)
	var disables []GatewayV2LANDisableStartupClaim
	for index := range grants {
		request := disableRequestForGrant(t, grants[index].Request)
		request.OperationID = fmt.Sprintf("78787878-7878-4787-8787-%012d", index+1)
		grants[index].DisableIntentOperationID = request.OperationID
		disables = append(disables, GatewayV2LANDisableStartupClaim{Request: request,
			State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: grants[index].CurrentBinding})
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := gatewayCurrentLANRecoveryInstallState(f.baseline, claims)
	if err != nil || !gatewayCurrentLANRecoveryCensusMatchesHead(batch, claims, snapshot.CurrentTransfers) {
		t.Fatalf("batch census fixture: %v", err)
	}
	if err := f.store.saveNext(f.baseline, batch); err != nil {
		t.Fatal(err)
	}
	return f, snapshot, grants, disables, batch
}

func TestGatewayCurrentLANRecoveryHeadRequiresCurrentBatchProof(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchObservationFixture(t)
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	// The existing ordinary attestor deliberately has no whole-batch proof
	// capability. The reader must explicitly identify the selected current batch
	// as present and unresolved instead of selecting its older native owner.
	head, present, err := f.manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
	if err == nil || !present || head != (GatewayV2LANRecoveryHead{}) {
		t.Fatalf("current batch without a proof: head=%+v present=%t error=%v", head, present, err)
	}
	loaded, loadErr := f.store.load()
	after, historyErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if loadErr != nil || historyErr != nil || !reflect.DeepEqual(loaded, batch) || !sameGatewayHistorySnapshot(before, after) {
		t.Fatal("observation refusal changed current state or retained history")
	}
}

func TestGatewayCurrentLANRecoveryHeadObservesWholeBatchAndCompletion(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchObservationFixture(t)
	driver := &gatewayCurrentLANBatchObserverDriver{gatewayCurrentPhysicalDriverFake: &gatewayCurrentPhysicalDriverFake{}, t: t}
	f.manager.gatewayCurrentPhysicalDriver = driver
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	observe := func() {
		t.Helper()
		head, present, err := f.manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
		if err != nil || !present || head.Head != batch.LANRecovery.Head || head.Count != len(batch.LANRecovery.Items) {
			t.Fatalf("current batch head: %+v present=%t error=%v", head, present, err)
		}
		complete := batch.LANRecovery.Head == len(batch.LANRecovery.Items)
		if driver.last.Complete != complete {
			t.Fatal("physical action lost completed-queue disposition")
		}
		if complete {
			if head.Kind != "" || head.OperationID != "" || head.AppID != "" ||
				driver.last.Head != (gatewayCurrentLANRecoveryItem{}) || !reflect.DeepEqual(driver.last.Cleared, batch) {
				t.Fatal("completed queue invented a dispatchable head or clearance")
			}
		} else {
			item := batch.LANRecovery.Items[batch.LANRecovery.Head]
			if head.Kind != GatewayV2LANRecoveryDisable || head.OperationID != item.Disable.OperationID || head.AppID != item.AppID {
				t.Fatal("observation selected a different queue operation")
			}
		}
		if loaded, err := f.store.load(); err != nil || !reflect.DeepEqual(loaded, batch) {
			t.Fatalf("observation changed protected batch: %v", err)
		}
	}
	observe()
	for _, item := range batch.LANRecovery.Items {
		index := -1
		for i := range disables {
			if disables[i].Request.OperationID == item.Disable.OperationID {
				index = i
			}
		}
		if index < 0 {
			t.Fatal("missing queue claim")
		}
		// Advance fixture evidence with the existing pure transitions. The public
		// observer itself performs no SQL completion or protected-state writes.
		disables[index].State, disables[index].StateSequence = appaccess.AppAccessDisableCommitted, 3
		disables[index].RetainedBinding, disables[index].CurrentBinding = disables[index].CurrentBinding, nil
		grants[index].RetainedBinding, grants[index].CurrentBinding = grants[index].CurrentBinding, nil
		cleared, err := gatewayCurrentLANRecoveryClearedHeadState(batch)
		if err != nil || f.store.saveNext(batch, cleared) != nil {
			t.Fatalf("fixture head clearance: %v", err)
		}
		disables[index].ClearAcknowledged = true
		advanced, err := gatewayCurrentLANRecoveryAdvanceHeadState(cleared)
		if err != nil || f.store.saveNext(cleared, advanced) != nil {
			t.Fatalf("fixture head advancement: %v", err)
		}
		batch = advanced
		observe()
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(history, after) || driver.applyCalls+driver.restoreCalls+driver.stopCalls != 0 ||
		driver.calls != len(batch.LANRecovery.Items)+1 {
		t.Fatal("queue observation mutated history or invoked ordinary physical effects")
	}
}

func TestGatewayCurrentLANRecoveryHeadRejectsPartialAndDriftingProof(t *testing.T) {
	f, snapshot, grants, disables, batch := gatewayCurrentLANBatchObservationFixture(t)
	driver := &gatewayCurrentLANBatchObserverDriver{gatewayCurrentPhysicalDriverFake: &gatewayCurrentPhysicalDriverFake{}, t: t}
	f.manager.gatewayCurrentPhysicalDriver = driver
	assertRefused := func(ctx context.Context, g []GatewayV2LANStartupClaim, d []GatewayV2LANDisableStartupClaim) {
		t.Helper()
		head, present, err := f.manager.ObserveGatewayV2LANRecoveryHead(ctx, g, d)
		if err == nil || !present || head != (GatewayV2LANRecoveryHead{}) {
			t.Fatalf("unsafe head escaped observation: %+v present=%t error=%v", head, present, err)
		}
	}
	t.Run("partial withdrawal does not authorize dispatch", func(t *testing.T) {
		driver.partial = true
		assertRefused(context.Background(), grants, disables)
		driver.partial = false
	})
	t.Run("incomplete queue census fails before physical inspection", func(t *testing.T) {
		extra := GatewayV2LANStartupClaim{Request: routeOperationNativeGrantRequest(t, batch,
			"80808080-8080-4808-8808-808080808080"), State: appaccess.AppAccessGrantPrepared, StateSequence: 1}
		changed := append(append([]GatewayV2LANStartupClaim(nil), grants...), extra)
		if _, err := validateGatewayV2LANAccessStartupClaims(changed, disables); err != nil {
			t.Fatal("negative census must remain structurally valid")
		}
		calls := driver.calls
		assertRefused(context.Background(), changed, disables)
		if driver.calls != calls {
			t.Fatal("incomplete census reached the physical driver")
		}
	})
	t.Run("cancellation after proof", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		driver.before = func(gatewayCurrentLANRecoveryPhysicalAction) { cancel() }
		assertRefused(ctx, grants, disables)
		driver.before = nil
	})
	t.Run("SQL authority drift after proof", func(t *testing.T) {
		driver.before = func(gatewayCurrentLANRecoveryPhysicalAction) {
			changed := snapshot
			changed.RollbackAllowed = !changed.RollbackAllowed
			f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
				return changed, nil
			})
		}
		assertRefused(context.Background(), grants, disables)
		driver.before = nil
		f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
			return snapshot, nil
		})
	})
	if loaded, err := f.store.load(); err != nil || !reflect.DeepEqual(loaded, batch) {
		t.Fatal("refusal changed protected state")
	}
	t.Run("protected revision drift after proof", func(t *testing.T) {
		changed := cloneGatewayCurrentRouteState(batch)
		changed.Revision++
		changed.Digest, _ = gatewayCurrentRouteStateDigest(changed)
		driver.before = func(gatewayCurrentLANRecoveryPhysicalAction) {
			if err := f.store.saveNext(batch, changed); err != nil {
				t.Fatal(err)
			}
		}
		assertRefused(context.Background(), grants, disables)
		if loaded, err := f.store.load(); err != nil || !reflect.DeepEqual(loaded, changed) {
			t.Fatal("observation overwrote the changed protected evidence")
		}
		driver.before = nil
	})
	if driver.applyCalls+driver.restoreCalls+driver.stopCalls != 0 {
		t.Fatal("read-only refusal invoked physical mutation")
	}
}
