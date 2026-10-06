package generatedingress

import (
	"math"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANRecoveryBatchTransitionsPreserveQueue(t *testing.T) {
	f := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(f.baseline)
	_, transferred := routeOperationTransferredApp(t, state)
	const nativeAppID = "56565656-5656-4565-8565-565656565656"
	if _, exists := state.Apps[nativeAppID]; exists {
		t.Fatal("native test application identity is already used")
	}
	nativeRequest := routeOperationNativeGrantRequest(t, state, nativeAppID)
	nativeRaw, err := gatewayV2LANBindingForRequest(nativeRequest)
	if err != nil {
		t.Fatal(err)
	}
	native := cloneGatewayCurrentAppRoute(transferred)
	native.LAN = &gatewayCurrentLANBinding{Raw: nativeRaw}
	state.Apps[nativeAppID] = native
	state.Revision++
	state.Digest, _ = gatewayCurrentRouteStateDigest(state)
	grants := gatewayCurrentStartupGrants(t, state)
	var disables []GatewayV2LANDisableStartupClaim
	for index := range grants {
		request := disableRequestForGrant(t, grants[index].Request)
		if grants[index].Request.AppID == nativeAppID {
			request.OperationID = "57575757-5757-4575-8575-575757575757"
		}
		grants[index].DisableIntentOperationID = request.OperationID
		disables = append(disables, GatewayV2LANDisableStartupClaim{Request: request,
			State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: grants[index].CurrentBinding})
	}
	if len(disables) < 2 {
		t.Fatal("fixture needs distinct recovery applications")
	}
	// A prior single-operation marker survives installation and every head step.
	request := disables[0].Request
	previous := cloneGatewayCurrentAppRoute(state.Apps[request.AppID])
	proposed := cloneGatewayCurrentAppRoute(previous)
	proposed.LAN = nil
	state.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANDisable, AppID: request.AppID,
		Previous: &previous, Proposed: proposed, Disable: &request, ActivationUncertain: true}
	state.Revision++
	state.Digest, _ = gatewayCurrentRouteStateDigest(state)
	before := cloneGatewayCurrentRouteState(state)
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := gatewayCurrentLANRecoveryInstallState(state, claims)
	if err != nil || installed.Pending != nil || installed.LANRecovery == nil || installed.LANRecovery.Head != 0 ||
		installed.Revision != state.Revision+1 || !reflect.DeepEqual(installed.Apps, state.Apps) ||
		!reflect.DeepEqual(installed.LANRecovery.LegacyPending, state.Pending) || !reflect.DeepEqual(state, before) {
		t.Fatalf("batch installation changed source or lost evidence: %+v %v", installed, err)
	}
	if _, err := gatewayCurrentLANRecoveryInstallState(installed, claims); err == nil {
		t.Fatal("existing batch was overwritten")
	}
	queue := cloneGatewayCurrentLANRecoveryBatch(installed.LANRecovery)
	for _, invalidHead := range []int{-1, len(queue.Items) + 1} {
		invalid := cloneGatewayCurrentRouteState(installed)
		invalid.LANRecovery.Head = invalidHead
		invalid.Digest, _ = gatewayCurrentRouteStateDigest(invalid)
		if _, err := gatewayCurrentLANRecoveryClearedHeadState(invalid); err == nil {
			t.Fatal("invalid head accepted for clearance")
		}
		if _, err := gatewayCurrentLANRecoveryAdvanceHeadState(invalid); err == nil {
			t.Fatal("invalid head accepted for advancement")
		}
	}
	current := installed
	for index := range queue.Items {
		if _, err := gatewayCurrentLANRecoveryAdvanceHeadState(current); err == nil {
			t.Fatal("head advanced before protected binding clearance")
		}
		if _, err := gatewayCurrentLANRecoveryRetiredState(current); err == nil {
			t.Fatal("unfinished batch retired")
		}
		prior := cloneGatewayCurrentRouteState(current)
		cleared, err := gatewayCurrentLANRecoveryClearedHeadState(current)
		if err != nil || cleared.Revision != current.Revision+1 || cleared.LANRecovery.Head != index ||
			!reflect.DeepEqual(cleared.LANRecovery, current.LANRecovery) || !reflect.DeepEqual(current, prior) {
			t.Fatalf("head clearance mutated queue/source: %+v %v", cleared, err)
		}
		for appID, app := range current.Apps {
			if appID != queue.Items[index].AppID && !reflect.DeepEqual(app, cleared.Apps[appID]) {
				t.Fatal("head clearance changed unrelated application")
			}
			if !reflect.DeepEqual(app.Route, cleared.Apps[appID].Route) {
				t.Fatal("head clearance changed loopback route")
			}
		}
		replayed, err := gatewayCurrentLANRecoveryClearedHeadState(cleared)
		if err != nil || !reflect.DeepEqual(replayed, cleared) {
			t.Fatal("clear replay changed revision or evidence")
		}
		replayed.LANRecovery.Items[index].Disable.SourceGrant.ApprovedBy = "changed-copy"
		replayed.LANRecovery.LegacyPending.Disable.SourceGrant.ApprovedBy = "changed-pending-copy"
		if !reflect.DeepEqual(cleared.LANRecovery, current.LANRecovery) || !reflect.DeepEqual(state, before) {
			t.Fatal("clear replay aliases immutable queue or source pending evidence")
		}
		advanced, err := gatewayCurrentLANRecoveryAdvanceHeadState(cleared)
		if err != nil || advanced.Revision != cleared.Revision+1 || advanced.LANRecovery.Head != index+1 ||
			!reflect.DeepEqual(advanced.Apps, cleared.Apps) || !reflect.DeepEqual(advanced.LANRecovery.Items, queue.Items) ||
			!reflect.DeepEqual(advanced.LANRecovery.LegacyPending, queue.LegacyPending) || !sameGatewayCurrentRouteOrigin(installed, advanced) {
			t.Fatalf("head advance changed immutable batch: %+v %v", advanced, err)
		}
		current = advanced
	}
	if _, err := gatewayCurrentLANRecoveryAdvanceHeadState(current); err == nil {
		t.Fatal("completed head advanced beyond queue")
	}
	if _, err := gatewayCurrentLANRecoveryClearedHeadState(current); err == nil {
		t.Fatal("completed head was cleared again")
	}
	retired, err := gatewayCurrentLANRecoveryRetiredState(current)
	if err != nil || retired.LANRecovery != nil || retired.Revision != current.Revision+1 ||
		!reflect.DeepEqual(retired.Apps, current.Apps) || !sameGatewayCurrentRouteOrigin(installed, retired) {
		t.Fatalf("retirement changed unrelated state: %+v %v", retired, err)
	}
	if _, err := gatewayCurrentLANRecoveryRetiredState(retired); err == nil {
		t.Fatal("missing batch accepted as a retirement transition")
	}
	// A prepared native grant can require recovery before any binding exists.
	// Installing its batch must retain the approved raw request without making
	// that request a serving publication or a revision-only clear write.
	prepared := cloneGatewayCurrentRouteState(f.baseline)
	withoutLAN := cloneGatewayCurrentAppRoute(native)
	withoutLAN.LAN = nil
	prepared.Apps[nativeAppID] = withoutLAN
	prepared.Revision++
	prepared.Digest, _ = gatewayCurrentRouteStateDigest(prepared)
	preparedGrants := gatewayCurrentStartupGrants(t, prepared)
	preparedGrants = append(preparedGrants, GatewayV2LANStartupClaim{Request: nativeRequest,
		State: appaccess.AppAccessGrantPrepared, StateSequence: 1})
	preparedClaims, err := validateGatewayV2LANAccessStartupClaims(preparedGrants, nil)
	if err != nil {
		t.Fatal(err)
	}
	grantBatch, err := gatewayCurrentLANRecoveryInstallState(prepared, preparedClaims)
	if err != nil || grantBatch.LANRecovery == nil || len(grantBatch.LANRecovery.Items) != 1 ||
		grantBatch.LANRecovery.Items[0].Kind != gatewayV2PendingLANGrant ||
		!reflect.DeepEqual(grantBatch.LANRecovery.Items[0].Grant, &gatewayCurrentLANBinding{Raw: nativeRaw}) ||
		!reflect.DeepEqual(grantBatch.Apps, prepared.Apps) {
		t.Fatalf("prepared grant batch rewrote raw identity or publication: %+v %v", grantBatch, err)
	}
	grantClear, err := gatewayCurrentLANRecoveryClearedHeadState(grantBatch)
	if err != nil || !reflect.DeepEqual(grantClear, grantBatch) {
		t.Fatal("absent prepared grant clear changed protected revision")
	}
	grantAdvanced, err := gatewayCurrentLANRecoveryAdvanceHeadState(grantClear)
	if err != nil || grantAdvanced.LANRecovery.Head != 1 || !reflect.DeepEqual(grantAdvanced.LANRecovery.Items, grantBatch.LANRecovery.Items) {
		t.Fatalf("prepared grant batch did not advance exactly once: %v", err)
	}
	exhausted := cloneGatewayCurrentRouteState(installed)
	exhausted.Revision = math.MaxUint64
	exhausted.Digest, _ = gatewayCurrentRouteStateDigest(exhausted)
	if _, err := gatewayCurrentLANRecoveryClearedHeadState(exhausted); err == nil {
		t.Fatal("revision exhaustion permitted clearance")
	}
}
