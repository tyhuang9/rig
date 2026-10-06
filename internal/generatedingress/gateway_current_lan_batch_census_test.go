package generatedingress

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANRecoveryCensusBindsClearedHistoryAndOrderedHead(t *testing.T) {
	f := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(f.baseline)
	_, seed := routeOperationTransferredApp(t, state)
	const nativeApp = "56565656-5656-4565-8565-565656565656"
	request := routeOperationNativeGrantRequest(t, state, nativeApp)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	native := cloneGatewayCurrentAppRoute(seed)
	native.LAN = &gatewayCurrentLANBinding{Raw: raw}
	state.Apps[nativeApp] = native
	state.Revision++
	state.Digest, _ = gatewayCurrentRouteStateDigest(state)
	grants := gatewayCurrentStartupGrants(t, state)
	var disables []GatewayV2LANDisableStartupClaim
	for i := range grants {
		request := disableRequestForGrant(t, grants[i].Request)
		if request.AppID == nativeApp {
			request.OperationID = "57575757-5757-4575-8575-575757575757"
		}
		grants[i].DisableIntentOperationID = request.OperationID
		disables = append(disables, GatewayV2LANDisableStartupClaim{Request: request,
			State: appaccess.AppAccessDisablePrepared, StateSequence: 1, CurrentBinding: grants[i].CurrentBinding})
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	state, err = gatewayCurrentLANRecoveryInstallState(state, claims)
	if err != nil {
		t.Fatal(err)
	}
	initial := cloneGatewayCurrentRouteState(state)
	check := func(state gatewayCurrentRouteState, grants []GatewayV2LANStartupClaim,
		disables []GatewayV2LANDisableStartupClaim, transfers []appaccess.GatewayRebindAllocationTransfer,
	) bool {
		claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
		return err == nil && gatewayCurrentLANRecoveryCensusMatchesHead(state, claims, transfers)
	}
	if !check(state, grants, disables, f.transfers) || check(state, grants, disables, nil) ||
		check(state, grants, disables, append(append([]appaccess.GatewayRebindAllocationTransfer(nil), f.transfers...), f.transfers[0])) {
		t.Fatal("complete protected transfer manifest was not required")
	}
	for head, item := range state.LANRecovery.Items {
		index := -1
		for i := range disables {
			if disables[i].Request.OperationID == item.Disable.OperationID {
				index = i
			}
		}
		if index < 0 {
			t.Fatal("missing fixture queue item")
		}
		// SQL may commit the current head before protected clearance; the raw
		// source grant stays committed while its projection becomes retained.
		disables[index].State, disables[index].StateSequence = appaccess.AppAccessDisableCommitted, 3
		disables[index].RetainedBinding, disables[index].CurrentBinding = disables[index].CurrentBinding, nil
		grants[index].RetainedBinding, grants[index].CurrentBinding = grants[index].CurrentBinding, nil
		if !check(state, grants, disables, f.transfers) {
			t.Fatal("terminal current head before protected clear was rejected")
		}
		disables[index].ClearAcknowledged = true
		if check(state, grants, disables, f.transfers) {
			t.Fatal("acknowledgment before protected clearance was accepted")
		}
		disables[index].ClearAcknowledged = false
		state, err = gatewayCurrentLANRecoveryClearedHeadState(state)
		if err != nil || !check(state, grants, disables, f.transfers) {
			t.Fatalf("cleared head lost retained authority: %v", err)
		}
		disables[index].ClearAcknowledged = true
		if !check(state, grants, disables, f.transfers) {
			t.Fatal("acknowledged current head replay was rejected")
		}
		state, err = gatewayCurrentLANRecoveryAdvanceHeadState(state)
		if err != nil || state.LANRecovery.Head != head+1 || !check(state, grants, disables, f.transfers) {
			t.Fatalf("ordered advancement lost terminal history: %v", err)
		}
		disables[index].ClearAcknowledged = false
		if check(state, grants, disables, f.transfers) {
			t.Fatal("completed head without clear acknowledgment was accepted")
		}
		disables[index].ClearAcknowledged = true
	}
	if check(initial, grants, disables, f.transfers) {
		t.Fatal("an acknowledged tail was accepted in the initial queue")
	}
	// A fully rehashed alternate chain tip remains structurally valid, but no
	// longer belongs to the immutable manifest after its live binding is gone.
	for i := range grants {
		projection := grants[i].RetainedBinding
		if len(projection.TransferChain) == 0 {
			continue
		}
		changed := *projection
		changed.TransferChain = append([]appaccess.GatewayRebindAllocationTransfer(nil), projection.TransferChain...)
		last := len(changed.TransferChain) - 1
		changed.TransferChain[last].SourceBindingDigest = strings.Repeat("f", 64)
		changed.TransferChain[last].TransferDigest, err = appaccess.GatewayRebindAllocationTransferDigest(changed.TransferChain[last])
		if err != nil {
			t.Fatal(err)
		}
		changed.TransferChainTipDigest = changed.TransferChain[last].TransferDigest
		grants[i].RetainedBinding, disables[i].RetainedBinding = &changed, &changed
		if check(state, grants, disables, f.transfers) {
			t.Fatal("cleared item accepted a forged but rehashed transfer tip")
		}
		grants[i].RetainedBinding, disables[i].RetainedBinding = projection, projection
	}
	if !reflect.DeepEqual(initial.LANRecovery.Items, state.LANRecovery.Items) || !check(state, grants, disables, f.transfers) {
		t.Fatal("read-only census changed immutable queue or final retained history")
	}

	t.Run("grant rollback cannot skip tail or admit committed history", func(t *testing.T) {
		candidate := cloneGatewayCurrentRouteState(initial)
		candidate.LANRecovery = nil
		for appID, app := range candidate.Apps {
			app.LAN = nil
			candidate.Apps[appID] = app
		}
		var prepared []GatewayV2LANStartupClaim
		var items []gatewayCurrentLANRecoveryItem
		for _, item := range initial.LANRecovery.Items {
			request := routeOperationNativeGrantRequest(t, candidate, item.AppID)
			raw, err := gatewayV2LANBindingForRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			binding := &gatewayCurrentLANBinding{Raw: raw}
			app := candidate.Apps[item.AppID]
			app.LAN = binding // Reserve a distinct port while building the fixture.
			candidate.Apps[item.AppID] = app
			items = append(items, gatewayCurrentLANRecoveryItem{Kind: gatewayV2PendingLANGrant, AppID: item.AppID, Grant: binding})
			prepared = append(prepared, GatewayV2LANStartupClaim{Request: request, State: appaccess.AppAccessGrantPrepared, StateSequence: 1})
		}
		for appID, app := range candidate.Apps {
			app.LAN = nil
			candidate.Apps[appID] = app
		}
		candidate.LANRecovery = &gatewayCurrentLANRecoveryBatch{Items: gatewayCurrentSortedRecoveryItems(items)}
		candidate.Digest, _ = gatewayCurrentRouteStateDigest(candidate)
		if !check(candidate, prepared, nil, f.transfers) {
			t.Fatal("unpublished native grant queue rejected")
		}
		changedClaims := append([]GatewayV2LANStartupClaim(nil), prepared...)
		changedClaims[0].Request.ApprovedBy = "58585858-5858-4585-8585-585858585858"
		if check(candidate, changedClaims, nil, f.transfers) {
			t.Fatal("queue accepted a different immutable raw grant")
		}
		extra := prepared[0]
		extra.Request.AttemptID = "59595959-5959-4595-8595-595959595959"
		if check(candidate, append(append([]GatewayV2LANStartupClaim(nil), prepared...), extra), nil, f.transfers) {
			t.Fatal("incomplete queue omitted an unresolved SQL grant")
		}
		for _, collision := range []string{"port", "allocation"} {
			bad := cloneGatewayCurrentRouteState(candidate)
			changedClaims := append([]GatewayV2LANStartupClaim(nil), prepared...)
			changed := changedClaims[1].Request
			if collision == "port" {
				changed.Port = prepared[0].Request.Port
			} else {
				changed.AllocationID = prepared[0].Request.AllocationID
			}
			changed.AccessSpecDigest, err = appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
				AppID: changed.AppID, AllocationID: changed.AllocationID, Port: changed.Port,
				GatewayProfileRevisionID: changed.GatewayProfileRevisionID, GatewayProfileRevisionNumber: changed.GatewayProfileRevisionNumber})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := gatewayV2LANBindingForRequest(changed)
			if err != nil {
				t.Fatal(err)
			}
			changedClaims[1].Request = changed
			bad.LANRecovery.Items[1].Grant = &gatewayCurrentLANBinding{Raw: raw}
			bad.Digest, _ = gatewayCurrentRouteStateDigest(bad)
			if check(bad, changedClaims, nil, f.transfers) {
				t.Fatalf("queue accepted duplicate %s", collision)
			}
		}
		prepared[1].State, prepared[1].StateSequence = appaccess.AppAccessGrantRolledBack, 2
		if check(candidate, prepared, nil, f.transfers) {
			t.Fatal("tail rollback accepted before head resolution")
		}
		prepared[1].State, prepared[1].StateSequence = appaccess.AppAccessGrantPrepared, 1
		prepared[0].State, prepared[0].StateSequence = appaccess.AppAccessGrantCommitted, 4
		if check(candidate, prepared, nil, f.transfers) {
			t.Fatal("committed grant accepted as automatic rollback item")
		}
		prepared[0].State, prepared[0].StateSequence = appaccess.AppAccessGrantRolledBack, 2
		if !check(candidate, prepared, nil, f.transfers) {
			t.Fatal("current head terminal rollback replay rejected")
		}
		candidate, err = gatewayCurrentLANRecoveryAdvanceHeadState(candidate)
		if err != nil || !check(candidate, prepared, nil, f.transfers) {
			t.Fatalf("terminal grant head could not advance: %v", err)
		}
	})
}
