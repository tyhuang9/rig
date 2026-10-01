package generatedingress

import (
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
)

func TestGatewayV2LANRecoveryProtectedHeadTransitionsAreExactAndSequential(t *testing.T) {
	state := gatewayV2LANRecoveryTransitionTestState(t)
	batchBefore := cloneGatewayV2LANRecoveryBatch(state.LANRecovery)
	first := state.LANRecovery.Items[0]
	second := state.LANRecovery.Items[1]

	if _, err := gatewayV2LANRecoveryAdvanceHeadState(state); err == nil {
		t.Fatal("head advanced before its protected binding was cleared")
	}
	premature := cloneGatewayV2RouteState(state)
	premature.LANRecovery.Head++
	if validGatewayV2RouteState(premature) || validCommittedV2StateTransition(state, premature) {
		t.Fatal("state validation accepted an advanced head with its old binding live")
	}

	cleared, err := gatewayV2LANRecoveryClearedHeadState(state)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.LANRecovery.Head != state.LANRecovery.Head ||
		!reflect.DeepEqual(cleared.LANRecovery, batchBefore) ||
		cleared.Apps[first.AppID].LAN != nil ||
		!reflect.DeepEqual(cleared.Apps[second.AppID].LAN, state.Apps[second.AppID].LAN) {
		t.Fatal("head clear changed the queue, Head, or a non-head binding")
	}
	if state.Apps[first.AppID].LAN == nil {
		t.Fatal("head clear mutated its input state")
	}
	if !validCommittedV2StateTransition(state, cleared) {
		t.Fatal("exact head clear transition was rejected")
	}
	repeated, err := gatewayV2LANRecoveryClearedHeadState(cleared)
	if err != nil || !reflect.DeepEqual(repeated, cleared) || !validCommittedV2StateTransition(cleared, repeated) {
		t.Fatalf("repeated head clear was not idempotent: %v", err)
	}

	crossItem := cloneGatewayV2RouteState(state)
	app := crossItem.Apps[second.AppID]
	app.LAN = nil
	crossItem.Apps[second.AppID] = app
	if !validGatewayV2RouteState(crossItem) || validCommittedV2StateTransition(state, crossItem) {
		t.Fatal("committed transition accepted a non-head binding clear")
	}

	skipped := cloneGatewayV2RouteState(cleared)
	app = skipped.Apps[second.AppID]
	app.LAN = nil
	skipped.Apps[second.AppID] = app
	skipped.LANRecovery.Head += 2
	if !validGatewayV2RouteState(skipped) || validCommittedV2StateTransition(cleared, skipped) {
		t.Fatal("committed transition accepted a head skip")
	}

	rewritten := cloneGatewayV2RouteState(cleared)
	rewritten.LANRecovery.Items[1].Grant.ApprovedBy = uuid.NewString()
	app = rewritten.Apps[second.AppID]
	app.LAN.ApprovedBy = rewritten.LANRecovery.Items[1].Grant.ApprovedBy
	rewritten.Apps[second.AppID] = app
	if !validGatewayV2RouteState(rewritten) || validCommittedV2StateTransition(state, rewritten) {
		t.Fatal("committed transition accepted a rewritten recovery identity")
	}

	mutatedLegacy := cloneGatewayV2RouteState(cleared)
	mutatedLegacy.LANRecovery.LegacyPending = gatewayV2LANRecoveryWithdrawalEvidence(state, second)
	if !validGatewayV2RouteState(mutatedLegacy) || validCommittedV2StateTransition(state, mutatedLegacy) {
		t.Fatal("committed transition accepted mutated legacy evidence")
	}

	advanced, err := gatewayV2LANRecoveryAdvanceHeadState(cleared)
	if err != nil || advanced.LANRecovery.Head != 1 ||
		!reflect.DeepEqual(advanced.LANRecovery.Items, state.LANRecovery.Items) ||
		!reflect.DeepEqual(advanced.LANRecovery.LegacyPending, state.LANRecovery.LegacyPending) ||
		!validCommittedV2StateTransition(cleared, advanced) {
		t.Fatalf("exact one-item head advance was rejected: %v", err)
	}

	effective, unsafePorts, err := gatewayV2LANRecoveryEffectiveProjection(advanced)
	secondPort, _ := gatewayV2LANRecoveryItemIdentity(second)
	if err != nil || !reflect.DeepEqual(unsafePorts, []uint16{secondPort}) ||
		effective.Apps[second.AppID].LAN != nil || advanced.Apps[second.AppID].LAN == nil {
		t.Fatalf("unfinished head projection was not quarantined: ports=%v err=%v", unsafePorts, err)
	}
	earlyRetirement := cloneGatewayV2RouteState(advanced)
	earlyRetirement.LANRecovery = nil
	if validCommittedV2StateTransition(advanced, earlyRetirement) {
		t.Fatal("recovery batch retired before its final head")
	}

	secondCleared, err := gatewayV2LANRecoveryClearedHeadState(advanced)
	if err != nil || !validCommittedV2StateTransition(advanced, secondCleared) {
		t.Fatalf("second exact head clear was rejected: %v", err)
	}
	final, err := gatewayV2LANRecoveryAdvanceHeadState(secondCleared)
	if err != nil || final.LANRecovery.Head != len(final.LANRecovery.Items) ||
		!validCommittedV2StateTransition(secondCleared, final) {
		t.Fatalf("final head advance was rejected: %v", err)
	}
	if _, err := gatewayV2LANRecoveryClearedHeadState(final); err == nil {
		t.Fatal("final head exposed a nonexistent item for clearing")
	}
	if _, err := gatewayV2LANRecoveryAdvanceHeadState(final); err == nil {
		t.Fatal("final head advanced beyond the immutable queue")
	}
	finalProjection, finalPorts, err := gatewayV2LANRecoveryEffectiveProjection(final)
	if err != nil || len(finalPorts) != 0 || finalProjection.Apps[first.AppID].LAN != nil ||
		finalProjection.Apps[second.AppID].LAN != nil {
		t.Fatalf("final head projection = ports %v, err %v", finalPorts, err)
	}
	retired := cloneGatewayV2RouteState(final)
	retired.LANRecovery = nil
	if !validCommittedV2StateTransition(final, retired) {
		t.Fatal("exact completed recovery batch retirement was rejected")
	}
	mutatedRetirement := cloneGatewayV2RouteState(retired)
	delete(mutatedRetirement.Apps, second.AppID)
	if !validGatewayV2RouteState(mutatedRetirement) || validCommittedV2StateTransition(final, mutatedRetirement) {
		t.Fatal("completed recovery batch retirement accepted mutated Apps")
	}
}

func TestGatewayV2LANRecoveryClearsExactDisableHeadBinding(t *testing.T) {
	state := gatewayV2LANRecoveryTestState(t)
	bindLANForTest(t, &state, upgradeTestAppA, 8100)
	request := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(
		upgradeTestAppA, *state.Apps[upgradeTestAppA].LAN,
	))
	state.LANRecovery = &gatewayV2LANRecoveryBatch{Items: []gatewayV2LANRecoveryItem{{
		Kind: gatewayV2PendingLANDisable, AppID: upgradeTestAppA, Disable: &request,
	}}}
	if !validGatewayV2RouteState(state) {
		t.Fatal("disable recovery transition fixture is invalid")
	}

	cleared, err := gatewayV2LANRecoveryClearedHeadState(state)
	if err != nil || cleared.Apps[upgradeTestAppA].LAN != nil ||
		!reflect.DeepEqual(cleared.LANRecovery, state.LANRecovery) ||
		!validCommittedV2StateTransition(state, cleared) {
		t.Fatalf("exact disable head binding was not cleared: %v", err)
	}
	advanced, err := gatewayV2LANRecoveryAdvanceHeadState(cleared)
	if err != nil || advanced.LANRecovery.Head != 1 ||
		!validCommittedV2StateTransition(cleared, advanced) {
		t.Fatalf("cleared disable head did not advance: %v", err)
	}
}

func gatewayV2LANRecoveryTransitionTestState(t *testing.T) gatewayV2RouteState {
	t.Helper()
	state := gatewayV2LANRecoveryTestState(t)
	bindLANForTest(t, &state, upgradeTestAppA, 8100)
	bindLANForTest(t, &state, upgradeTestAppB, 8101)
	items := []gatewayV2LANRecoveryItem{
		{Kind: gatewayV2PendingLANGrant, AppID: upgradeTestAppA, Grant: cloneGatewayV2LANBinding(state.Apps[upgradeTestAppA].LAN)},
		{Kind: gatewayV2PendingLANGrant, AppID: upgradeTestAppB, Grant: cloneGatewayV2LANBinding(state.Apps[upgradeTestAppB].LAN)},
	}
	sort.Slice(items, func(i, j int) bool { return gatewayV2LANRecoveryItemLess(items[i], items[j]) })
	state.LANRecovery = &gatewayV2LANRecoveryBatch{
		Items: items, LegacyPending: gatewayV2LANRecoveryWithdrawalEvidence(state, items[0]),
	}
	if !validGatewayV2RouteState(state) {
		t.Fatal("sequential recovery transition fixture is invalid")
	}
	return state
}

func gatewayV2LANRecoveryWithdrawalEvidence(state gatewayV2RouteState, item gatewayV2LANRecoveryItem) *gatewayV2PendingRoute {
	previous := cloneGatewayV2AppRoute(state.Apps[item.AppID])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = nil
	return &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANWithdrawal, AppID: item.AppID, Previous: &previous,
		Proposed: proposed, ActivationUncertain: true,
	}
}
