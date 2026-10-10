package generatedingress

import (
	"context"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANStartupQuarantineRetainsCommittedHistory(t *testing.T) {
	f, snapshot, upgradeClaims, physical := gatewayRebindCurrentStartupFixture(t)
	driver := &gatewayCurrentStateMachineDriver{t: t, terminal: physical.attest.Terminal,
		pendingAttestOutcome: gatewayCurrentPhysicalRecoveryEffective}
	f.manager.gatewayCurrentPhysicalDriver = driver
	grants := gatewayCurrentStartupGrants(t, f.baseline)
	grants[0].RequiresRecovery = true
	request := grants[0].Request
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	driver.afterApply = cancel
	if err := f.manager.QuarantineGatewayV2LANAccessStartup(ctx, grants, nil); err != nil {
		t.Fatal(err)
	}
	pending := routeOperationLoad(t, f.store)
	if pending.Pending == nil || pending.Pending.Kind != gatewayV2PendingLANWithdrawal ||
		pending.Pending.AppID != request.AppID || pending.Pending.Previous == nil ||
		!reflect.DeepEqual(pending.Pending.Previous.LAN, f.baseline.Apps[request.AppID].LAN) ||
		pending.Pending.Proposed.LAN != nil || !reflect.DeepEqual(pending.Apps, f.baseline.Apps) ||
		pending.Revision != f.baseline.Revision+1 || driver.applyCalls != 1 || driver.restoreCalls != 0 || driver.stopCalls != 0 {
		t.Fatal("quarantine did not retain exact transferred grant and withdrawal marker")
	}
	inspection, err := f.manager.InspectGatewayV2Startup(context.Background(), upgradeClaims)
	if err != nil || inspection.Disposition != GatewayV2StartupRecoveryOnly || inspection.CurrentGatewaySource != *snapshot.CurrentSource {
		t.Fatalf("quarantine admitted normal startup: %+v %v", inspection, err)
	}
	lan, err := f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, nil)
	if err != nil || lan.Disposition != GatewayV2LANStartupRecoveryOnly || lan.OperationID != request.AttemptID {
		t.Fatalf("quarantine lost recovery identity: %+v %v", lan, err)
	}
	if err := f.manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, nil); err != nil {
		t.Fatal(err)
	}
	if got := routeOperationLoad(t, f.store); !reflect.DeepEqual(got, pending) || driver.applyCalls != 2 {
		t.Fatal("quarantine replay advanced or cleared retained evidence")
	}
	driver.failApply = true
	if err := f.manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, nil); err == nil || driver.stopCalls != 1 {
		t.Fatal("failed withdrawal did not stop the exact owned gateway and refuse startup")
	}
	driver.failApply = false
	driver.afterApply = func() {
		changed := snapshot
		changed.RollbackAllowed = true
		f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
			return changed, nil
		})
	}
	if err := f.manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, nil); err == nil || driver.stopCalls != 2 {
		t.Fatal("SQL drift after withdrawal did not retain the fence and stop owned gateway")
	}
	f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		return snapshot, nil
	})
	replacement := cloneGatewayCurrentRouteState(pending)
	replacement.Revision++
	replacement.Digest, _ = gatewayCurrentRouteStateDigest(replacement)
	driver.afterApply = func() {
		if err := f.store.saveNext(pending, replacement); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, nil); err == nil || driver.stopCalls != 3 {
		t.Fatal("protected replacement after withdrawal was accepted")
	}
	if got := routeOperationLoad(t, f.store); !reflect.DeepEqual(got, replacement) {
		t.Fatal("failed quarantine overwrote the changed recovery marker")
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(history, after) {
		t.Fatal("quarantine changed immutable gateway history")
	}
}

func TestGatewayCurrentLANStartupQuarantinePreservesDisableAndGrantMarkers(t *testing.T) {
	for _, kind := range []string{"disable", "prepared grant"} {
		t.Run(kind, func(t *testing.T) {
			f, _, _, physical := gatewayRebindCurrentStartupFixture(t)
			driver := &gatewayCurrentStateMachineDriver{t: t, terminal: physical.attest.Terminal,
				pendingAttestOutcome: gatewayCurrentPhysicalRecoveryBefore}
			f.manager.gatewayCurrentPhysicalDriver = driver
			grants := gatewayCurrentStartupGrants(t, f.baseline)
			var disables []GatewayV2LANDisableStartupClaim
			before := cloneGatewayCurrentRouteState(f.baseline)
			var want gatewayCurrentRouteState
			if kind == "disable" {
				request := disableRequestForGrant(t, grants[0].Request)
				grants[0].DisableIntentOperationID = request.OperationID
				disables = []GatewayV2LANDisableStartupClaim{{Request: request,
					State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: grants[0].CurrentBinding}}
				transition, err := gatewayCurrentDisableTransition(before, request)
				if err != nil {
					t.Fatal(err)
				}
				want = transition.Pending
			} else {
				appID := grants[0].Request.AppID
				app := before.Apps[appID]
				app.LAN = nil
				withoutLAN, err := gatewayCurrentNextState(before, func(next *gatewayCurrentRouteState) { next.Apps[appID] = app })
				if err != nil || f.store.saveNext(before, withoutLAN) != nil {
					t.Fatalf("prepared fixture: %v", err)
				}
				request := routeOperationNativeGrantRequest(t, withoutLAN, appID)
				grants[0] = GatewayV2LANStartupClaim{Request: request, State: appaccess.AppAccessGrantPrepared, StateSequence: 1}
				if err := f.manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, nil); err != nil {
					t.Fatal(err)
				}
				if got := routeOperationLoad(t, f.store); !reflect.DeepEqual(got, withoutLAN) || driver.applyCalls != 0 || driver.restoreCalls != 0 {
					t.Fatal("unpublished prepared grant caused a physical or protected mutation")
				}
				transition, _, err := gatewayCurrentGrantTransition(withoutLAN, request)
				if err != nil || f.store.saveNext(withoutLAN, transition.Pending) != nil {
					t.Fatalf("prepared marker fixture: %v", err)
				}
				want = transition.Pending
			}
			if err := f.manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, disables); err != nil {
				t.Fatal(err)
			}
			if got := routeOperationLoad(t, f.store); !reflect.DeepEqual(got, want) || driver.stopCalls != 0 {
				t.Fatal("startup changed retained operation instead of proving withdrawal")
			}
			if (kind == "disable" && (driver.applyCalls != 1 || driver.restoreCalls != 0)) ||
				(kind == "prepared grant" && (driver.applyCalls != 0 || driver.restoreCalls != 1)) {
				t.Fatal("startup selected the wrong physical withdrawal endpoint")
			}
		})
	}
}
