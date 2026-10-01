package generatedingress

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2LANRecoveryStateRequiresBoundedCanonicalExactItems(t *testing.T) {
	state := gatewayV2LANRecoveryTestState(t)
	first := gatewayV2LANRecoveryGrantItem(t, state, upgradeTestAppA, 8100)
	second := gatewayV2LANRecoveryGrantItem(t, state, upgradeTestAppB, 8100)
	state.LANRecovery = &gatewayV2LANRecoveryBatch{Items: []gatewayV2LANRecoveryItem{first, second}}
	if !validGatewayV2RouteState(state) {
		t.Fatal("canonical recovery batch with a shared historical port was rejected")
	}

	completedShape := cloneGatewayV2RouteState(state)
	completedShape.LANRecovery.Head = len(completedShape.LANRecovery.Items)
	if !validGatewayV2RouteState(completedShape) {
		t.Fatal("fully traversed recovery batch shape was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*gatewayV2RouteState)
	}{
		{name: "pending conflicts with batch", mutate: func(candidate *gatewayV2RouteState) {
			candidate.Pending = &gatewayV2PendingRoute{}
		}},
		{name: "reordered", mutate: func(candidate *gatewayV2RouteState) {
			candidate.LANRecovery.Items[0], candidate.LANRecovery.Items[1] = candidate.LANRecovery.Items[1], candidate.LANRecovery.Items[0]
		}},
		{name: "missing exact identity", mutate: func(candidate *gatewayV2RouteState) {
			candidate.LANRecovery.Items[0].Grant = nil
		}},
		{name: "both identity variants", mutate: func(candidate *gatewayV2RouteState) {
			disable := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(
				candidate.LANRecovery.Items[0].AppID, *candidate.LANRecovery.Items[0].Grant,
			))
			candidate.LANRecovery.Items[0].Disable = &disable
		}},
		{name: "forged app relation", mutate: func(candidate *gatewayV2RouteState) {
			candidate.LANRecovery.Items[0].AppID = upgradeTestAppB
		}},
		{name: "duplicate operation", mutate: func(candidate *gatewayV2RouteState) {
			candidate.LANRecovery.Items[1].Grant.GrantAttemptID = candidate.LANRecovery.Items[0].Grant.GrantAttemptID
		}},
		{name: "head before queue", mutate: func(candidate *gatewayV2RouteState) {
			candidate.LANRecovery.Head = -1
		}},
		{name: "head after queue", mutate: func(candidate *gatewayV2RouteState) {
			candidate.LANRecovery.Head = len(candidate.LANRecovery.Items) + 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneGatewayV2RouteState(state)
			test.mutate(&candidate)
			if validGatewayV2RouteState(candidate) {
				t.Fatal("malformed recovery batch was accepted")
			}
		})
	}

	overLimit := cloneGatewayV2RouteState(state)
	overLimit.LANRecovery.Items = make([]gatewayV2LANRecoveryItem, 0, maxStateApps+1)
	for index := 0; index <= maxStateApps; index++ {
		item := first
		binding := *first.Grant
		binding.GrantAttemptID = uuid.NewString()
		item.Grant = &binding
		overLimit.LANRecovery.Items = append(overLimit.LANRecovery.Items, item)
	}
	sort.Slice(overLimit.LANRecovery.Items, func(i, j int) bool {
		return gatewayV2LANRecoveryItemLess(overLimit.LANRecovery.Items[i], overLimit.LANRecovery.Items[j])
	})
	if validGatewayV2RouteState(overLimit) {
		t.Fatal("recovery batch above the strict item limit was accepted")
	}
}

func TestGatewayV2LANRecoveryStatePreservesLegacyPendingAndCreationBoundary(t *testing.T) {
	current := gatewayV2LANRecoveryTestState(t)
	item := gatewayV2LANRecoveryGrantItem(t, current, upgradeTestAppA, 8100)
	previous := cloneGatewayV2AppRoute(current.Apps[upgradeTestAppA])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.LAN = item.Grant
	current.Pending = &gatewayV2PendingRoute{
		Kind: gatewayV2PendingLANGrant, AppID: upgradeTestAppA, Previous: &previous,
		Proposed: proposed, ActivationUncertain: true,
	}
	if !validGatewayV2RouteState(current) {
		t.Fatal("legacy pending grant fixture is invalid")
	}

	next := cloneGatewayV2RouteState(current)
	next.Pending = nil
	next.LANRecovery = &gatewayV2LANRecoveryBatch{
		Items:         []gatewayV2LANRecoveryItem{item},
		LegacyPending: cloneGatewayV2PendingRoute(current.Pending),
	}
	if !validGatewayV2RouteState(next) || !validCommittedV2StateTransition(current, next) {
		t.Fatal("exact legacy pending to recovery batch transition was rejected")
	}
	if !reflect.DeepEqual(current.Apps, next.Apps) {
		t.Fatal("recovery batch creation changed the committed Apps baseline")
	}

	clone := cloneGatewayV2RouteState(next)
	clone.LANRecovery.Items[0].Grant.AllocationID = uuid.NewString()
	clone.LANRecovery.LegacyPending.Proposed.Route.Endpoints[0].NetworkAlias = "mutated"
	if reflect.DeepEqual(clone.LANRecovery.Items[0].Grant, next.LANRecovery.Items[0].Grant) ||
		clone.LANRecovery.LegacyPending.Proposed.Route.Endpoints[0].NetworkAlias ==
			next.LANRecovery.LegacyPending.Proposed.Route.Endpoints[0].NetworkAlias {
		t.Fatal("recovery state clone aliases protected nested identity")
	}

	missingEvidence := cloneGatewayV2RouteState(next)
	missingEvidence.LANRecovery.LegacyPending = nil
	if !validGatewayV2RouteState(missingEvidence) || validCommittedV2StateTransition(current, missingEvidence) {
		t.Fatal("creation accepted a batch that discarded the legacy pending marker")
	}
	changedApps := cloneGatewayV2RouteState(next)
	delete(changedApps.Apps, upgradeTestAppB)
	if !validGatewayV2RouteState(changedApps) || validCommittedV2StateTransition(current, changedApps) {
		t.Fatal("creation accepted a changed committed Apps baseline")
	}
	advanced, err := gatewayV2LANRecoveryAdvanceHeadState(next)
	if err != nil || !validGatewayV2RouteState(advanced) || !validCommittedV2StateTransition(next, advanced) {
		t.Fatalf("exact cleared head advancement was rejected: %v", err)
	}
	cleared := cloneGatewayV2RouteState(advanced)
	cleared.LANRecovery = nil
	if !validCommittedV2StateTransition(advanced, cleared) {
		t.Fatal("exact completed recovery batch retirement was rejected")
	}
}

func TestGatewayV2LANRecoveryStatePreservesLegacyWithdrawalAndDisable(t *testing.T) {
	for _, kind := range []gatewayV2PendingKind{gatewayV2PendingLANWithdrawal, gatewayV2PendingLANDisable} {
		t.Run(string(kind), func(t *testing.T) {
			current := gatewayV2LANRecoveryTestState(t)
			bindLANForTest(t, &current, upgradeTestAppA, 8100)
			previous := cloneGatewayV2AppRoute(current.Apps[upgradeTestAppA])
			proposed := cloneGatewayV2AppRoute(previous)
			proposed.LAN = nil
			pending := &gatewayV2PendingRoute{
				Kind: kind, AppID: upgradeTestAppA, Previous: &previous,
				Proposed: proposed, ActivationUncertain: true,
			}
			item := gatewayV2LANRecoveryItem{AppID: upgradeTestAppA}
			if kind == gatewayV2PendingLANDisable {
				request := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(upgradeTestAppA, *previous.LAN))
				pending.Disable = &request
				item.Kind = gatewayV2PendingLANDisable
				item.Disable = &request
			} else {
				item.Kind = gatewayV2PendingLANGrant
				binding := *previous.LAN
				item.Grant = &binding
			}
			current.Pending = pending
			if !validGatewayV2RouteState(current) {
				t.Fatal("legacy pending fixture is invalid")
			}
			next := cloneGatewayV2RouteState(current)
			next.Pending = nil
			next.LANRecovery = &gatewayV2LANRecoveryBatch{
				Items: []gatewayV2LANRecoveryItem{item}, LegacyPending: cloneGatewayV2PendingRoute(current.Pending),
			}
			if !validGatewayV2RouteState(next) || !validCommittedV2StateTransition(current, next) {
				t.Fatal("exact pending evidence was not preserved")
			}
			altered := cloneGatewayV2RouteState(next)
			altered.LANRecovery.LegacyPending.AppID = upgradeTestAppB
			if validCommittedV2StateTransition(current, altered) {
				t.Fatal("altered legacy pending identity was accepted")
			}
		})
	}
}

func TestGatewayV2LANRecoveryStateAcceptsDBOnlyDisableWithoutLiveBinding(t *testing.T) {
	state := gatewayV2LANRecoveryTestState(t)
	grant := gatewayV2LANRecoveryGrantItem(t, state, upgradeTestAppA, 8100)
	request := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(grant.AppID, *grant.Grant))
	state.LANRecovery = &gatewayV2LANRecoveryBatch{Items: []gatewayV2LANRecoveryItem{{
		Kind: gatewayV2PendingLANDisable, AppID: request.AppID, Disable: &request,
	}}}
	if state.Apps[request.AppID].LAN != nil || !validGatewayV2RouteState(state) {
		t.Fatal("exact DB-only disable item without a live protected binding was rejected")
	}
}

func TestGatewayV2LANRecoveryStateAdvancesExactLegacyGrantDisablePair(t *testing.T) {
	state := gatewayV2LANRecoveryTestState(t)
	grant := gatewayV2LANRecoveryGrantItem(t, state, upgradeTestAppA, 8100)
	app := state.Apps[grant.AppID]
	app.LAN = cloneGatewayV2LANBinding(grant.Grant)
	state.Apps[grant.AppID] = app
	grantRequest := gatewayV2LANGrantRequestForBinding(grant.AppID, *grant.Grant)
	disableRequest := disableRequestForGrant(t, grantRequest)
	disableRequest.SourceGrant = nil
	disable := gatewayV2LANRecoveryItem{
		Kind: gatewayV2PendingLANDisable, AppID: grant.AppID, Disable: &disableRequest,
	}
	state.LANRecovery = &gatewayV2LANRecoveryBatch{Items: []gatewayV2LANRecoveryItem{grant, disable}}
	if !validGatewayV2RouteState(state) {
		t.Fatal("exact legacy pair state was rejected")
	}

	clearedGrant, err := gatewayV2LANRecoveryClearedHeadState(state)
	if err != nil {
		t.Fatal(err)
	}
	afterGrant, err := gatewayV2LANRecoveryAdvanceHeadState(clearedGrant)
	if err != nil || afterGrant.LANRecovery.Head != 1 || !validCommittedV2StateTransition(clearedGrant, afterGrant) {
		t.Fatalf("grant advance state=%#v err=%v", afterGrant.LANRecovery, err)
	}
	clearedDisable, err := gatewayV2LANRecoveryClearedHeadState(afterGrant)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := gatewayV2LANRecoveryAdvanceHeadState(clearedDisable)
	if err != nil || completed.LANRecovery.Head != 2 || !validCommittedV2StateTransition(clearedDisable, completed) {
		t.Fatalf("disable advance state=%#v err=%v", completed.LANRecovery, err)
	}
	retired := cloneGatewayV2RouteState(completed)
	retired.LANRecovery = nil
	if !validCommittedV2StateTransition(completed, retired) {
		t.Fatal("completed exact legacy pair could not retire")
	}

	forged := cloneGatewayV2RouteState(state)
	forged.LANRecovery.Items[1].Disable.SourceGrant = &grantRequest
	if validGatewayV2RouteState(forged) {
		t.Fatal("ordinary same-app same-port collision was accepted as the legacy pair")
	}
}

func TestGatewayV2InitialStateRejectsRecoveryBatch(t *testing.T) {
	source, input := upgradeTestPreparation(t)
	state, _, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	item := gatewayV2LANRecoveryGrantItem(t, state, upgradeTestAppA, 8100)
	state.LANRecovery = &gatewayV2LANRecoveryBatch{Items: []gatewayV2LANRecoveryItem{item}}
	if !validGatewayV2RouteState(state) {
		t.Fatal("recovery schema fixture is invalid")
	}
	if gatewayV2InitialStateMatchesSource(state, source) {
		t.Fatal("migration target accepted a forged recovery batch")
	}
}

func TestGatewayV2LANRecoveryMaximumStateFitsProtectedArtifactLimit(t *testing.T) {
	state := gatewayV2LANRecoveryTestState(t)
	template := cloneGatewayV2AppRoute(state.Apps[upgradeTestAppA])
	state.Apps = make(map[string]gatewayV2AppRoute, maxStateApps)
	items := make([]gatewayV2LANRecoveryItem, 0, maxStateApps)
	var legacyPending *gatewayV2PendingRoute
	for index := 0; index < maxStateApps; index++ {
		appID := gatewayV2LANRecoveryTestUUID(0x1000 + index)
		allocationID := gatewayV2LANRecoveryTestUUID(0x2000 + index)
		accessRevisionID := gatewayV2LANRecoveryTestUUID(0x3000 + index)
		port := state.Profile.PortStart + uint16(index)%((state.Profile.PortEnd-state.Profile.PortStart)+1)
		accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
			AppID: appID, AllocationID: allocationID, Port: port,
			GatewayProfileRevisionID: state.Profile.RevisionID, GatewayProfileRevisionNumber: state.Profile.RevisionNumber,
		})
		if err != nil {
			t.Fatal(err)
		}
		grant := GatewayV2LANGrantRequest{
			AttemptID: gatewayV2LANRecoveryTestUUID(0x4000 + index), ClaimRequestDigest: strings.Repeat("a", 64),
			AppID: appID, AllocationID: allocationID, OwnerOperationID: gatewayV2LANRecoveryTestUUID(0x5000 + index),
			Port: port, AccessRevisionID: accessRevisionID, AccessRevisionNumber: int64(index + 1), AccessSpecDigest: accessDigest,
			ApprovedBy: upgradeTestActor, GatewayProfileRevisionID: state.Profile.RevisionID,
			GatewayProfileRevisionNumber: state.Profile.RevisionNumber, GatewayProfileSpecDigest: state.Profile.SpecDigest,
		}
		disable := disableRequestForGrant(t, grant)
		disable.OperationID = gatewayV2LANRecoveryTestUUID(0x6000 + index)
		disable.RequestDigest = strings.Repeat("b", 64)
		app := cloneGatewayV2AppRoute(template)
		if index == 0 {
			binding, err := gatewayV2LANBindingForRequest(grant)
			if err != nil {
				t.Fatal(err)
			}
			app.LAN = &binding
			previous := cloneGatewayV2AppRoute(app)
			proposed := cloneGatewayV2AppRoute(app)
			proposed.LAN = nil
			legacyPending = &gatewayV2PendingRoute{
				Kind: gatewayV2PendingLANDisable, AppID: appID, Previous: &previous,
				Proposed: proposed, ActivationUncertain: true, Disable: &disable,
			}
		}
		state.Apps[appID] = app
		items = append(items, gatewayV2LANRecoveryItem{
			Kind: gatewayV2PendingLANDisable, AppID: appID, Disable: &disable,
		})
	}
	sort.Slice(items, func(i, j int) bool { return gatewayV2LANRecoveryItemLess(items[i], items[j]) })
	state.LANRecovery = &gatewayV2LANRecoveryBatch{Items: items, LegacyPending: legacyPending}
	if !validGatewayV2RouteState(state) {
		t.Fatal("maximum deterministic recovery state is structurally invalid")
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxV2RouteStateBytes {
		t.Fatalf("maximum recovery state uses %d bytes, protected cap is %d", len(body), maxV2RouteStateBytes)
	}
	source, _ := upgradeTestPreparation(t)
	store := newUpgradeStoreWithV1(t, source)
	if err := store.writeExact(store.v2Path, store.v2Purpose, state, true, maxV2RouteStateBytes); err != nil {
		t.Fatalf("protected maximum recovery state write: %v", err)
	}
	loaded, err := store.loadV2State()
	if err != nil || !reflect.DeepEqual(loaded, state) {
		var decoded gatewayV2RouteState
		readErr := store.readStrict(store.v2Path, store.v2Purpose, maxV2RouteStateBytes, &decoded)
		t.Fatalf("protected maximum recovery state readback: %v (strict=%v valid=%t equal=%t)",
			err, readErr, validGatewayV2RouteState(decoded), reflect.DeepEqual(decoded, state))
	}
	t.Logf("maximum recovery state uses %d of %d protected bytes", len(body), maxV2RouteStateBytes)
}

func gatewayV2LANRecoveryTestState(t *testing.T) gatewayV2RouteState {
	t.Helper()
	source, input := upgradeTestPreparation(t)
	state, _, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func gatewayV2LANRecoveryGrantItem(t *testing.T, state gatewayV2RouteState, appID string, port uint16) gatewayV2LANRecoveryItem {
	t.Helper()
	withBinding := cloneGatewayV2RouteState(state)
	withBinding.Pending = nil
	withBinding.LANRecovery = nil
	bindLANForTest(t, &withBinding, appID, port)
	binding := *withBinding.Apps[appID].LAN
	return gatewayV2LANRecoveryItem{Kind: gatewayV2PendingLANGrant, AppID: appID, Grant: &binding}
}

func gatewayV2LANRecoveryTestUUID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", value)
}
