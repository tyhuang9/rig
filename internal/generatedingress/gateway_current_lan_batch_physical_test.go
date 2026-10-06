package generatedingress

import (
	"context"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentLANRecoveryPhysicalDriverFake struct {
	*gatewayCurrentPhysicalDriverFake
	attestResult   gatewayCurrentLANRecoveryPhysicalResult
	withdrawResult gatewayCurrentLANRecoveryPhysicalResult
	attestAction   gatewayCurrentLANRecoveryPhysicalAction
	withdrawAction gatewayCurrentLANRecoveryPhysicalAction
}

func (f *gatewayCurrentLANRecoveryPhysicalDriverFake) attestGatewayCurrentLANRecoveryBatch(_ context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	f.attestAction = action
	return f.attestResult, nil
}

func (f *gatewayCurrentLANRecoveryPhysicalDriverFake) withdrawGatewayCurrentLANRecoveryBatch(_ context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	f.withdrawAction = action
	return f.withdrawResult, nil
}

func gatewayCurrentLANRecoveryPhysicalSelectionFixture(t *testing.T) (gatewayCurrentStateFixture,
	gatewayCurrentSelection,
) {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	_, template := routeOperationTransferredApp(t, state)
	const nativeAppID = "56565656-5656-4565-8565-565656565656"
	nativeRequest := routeOperationNativeGrantRequest(t, state, nativeAppID)
	nativeRaw, err := gatewayV2LANBindingForRequest(nativeRequest)
	if err != nil {
		t.Fatal(err)
	}
	native := cloneGatewayCurrentAppRoute(template)
	native.LAN = &gatewayCurrentLANBinding{Raw: nativeRaw}
	state.Apps[nativeAppID] = native
	state.Revision++
	state.Digest, _ = gatewayCurrentRouteStateDigest(state)
	grants := gatewayCurrentStartupGrants(t, state)
	if len(grants) < 2 {
		t.Fatal("current fixture has fewer than two LAN grants")
	}
	disables := make([]GatewayV2LANDisableStartupClaim, 0, len(grants))
	for index := range grants {
		disable := disableRequestForGrant(t, grants[index].Request)
		if grants[index].Request.AppID == nativeAppID {
			disable.OperationID = "57575757-5757-4575-8575-575757575757"
		}
		grants[index].DisableIntentOperationID = disable.OperationID
		disables = append(disables, GatewayV2LANDisableStartupClaim{
			Request: disable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1,
			CurrentBinding: grants[index].CurrentBinding,
		})
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	state, err = gatewayCurrentLANRecoveryInstallState(state, claims)
	if err != nil || state.LANRecovery == nil || len(state.LANRecovery.Items) < 2 {
		t.Fatalf("install recovery batch: %#v error=%v", state.LANRecovery, err)
	}
	terminal, err := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: state.Lineage,
		Receipt: &fixture.receipt, Terminal: &terminal, State: &state}
}

func gatewayCurrentLANRecoveryPhysicalResultFixture(t *testing.T,
	action gatewayCurrentLANRecoveryPhysicalAction, absent bool,
) gatewayCurrentLANRecoveryPhysicalResult {
	t.Helper()
	state := action.Before
	if absent {
		state = action.Withdrawn
	}
	attestation := gatewayCurrentPhysicalAttestationFixture(t, state, action.Terminal,
		gatewayCurrentPhysicalRecoveryMixed)
	attestation.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(action.Selected.LANRecovery)
	var err error
	attestation.Digest, err = gatewayCurrentPhysicalAttestationDigest(attestation)
	if err != nil {
		t.Fatal(err)
	}
	result := gatewayCurrentLANRecoveryPhysicalResult{ActionDigest: action.Digest,
		BatchAbsent: absent, Attestation: attestation}
	result.Digest, err = gatewayCurrentLANRecoveryPhysicalResultDigest(result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGatewayCurrentLANRecoveryPhysicalActionBindsOrderedHeadAndProjections(t *testing.T) {
	fixture, selection := gatewayCurrentLANRecoveryPhysicalSelectionFixture(t)
	action, err := fixture.manager.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	if err != nil || !validGatewayCurrentLANRecoveryPhysicalAction(action) ||
		action.Head.AppID != selection.State.LANRecovery.Items[0].AppID ||
		action.Before.LANRecovery != nil || action.Withdrawn.LANRecovery != nil ||
		action.Cleared.LANRecovery == nil || action.Cleared.LANRecovery.Head != 0 ||
		action.Cleared.Apps[action.Head.AppID].LAN != nil {
		t.Fatalf("batch physical action=%#v error=%v", action, err)
	}
	if reflect.DeepEqual(action.Before, action.Withdrawn) {
		t.Fatal("published batch head has identical before and withdrawn topology")
	}
	for _, item := range action.Selected.LANRecovery.Items {
		if action.Withdrawn.Apps[item.AppID].LAN != nil {
			t.Fatalf("whole-batch withdrawal retained LAN binding for %s", item.AppID)
		}
	}
	changed := action
	changed.Head.AppID = "56565656-5656-4565-8565-565656565656"
	changed.Digest, _ = gatewayCurrentLANRecoveryPhysicalActionDigest(changed)
	if validGatewayCurrentLANRecoveryPhysicalAction(changed) {
		t.Fatal("batch physical action accepted a changed ordered head")
	}
	changed = action
	changed.Withdrawn.Apps[action.Head.AppID] = action.Before.Apps[action.Head.AppID]
	changed.Withdrawn.Digest, _ = gatewayCurrentRouteStateDigest(changed.Withdrawn)
	changed.Digest, _ = gatewayCurrentLANRecoveryPhysicalActionDigest(changed)
	if validGatewayCurrentLANRecoveryPhysicalAction(changed) {
		t.Fatal("batch physical action accepted a changed withdrawal projection")
	}
}

func TestGatewayCurrentLANRecoveryPhysicalWrappersRequireExactBatchAbsenceProof(t *testing.T) {
	fixture, selection := gatewayCurrentLANRecoveryPhysicalSelectionFixture(t)
	action, err := fixture.manager.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentLANRecoveryPhysicalDriverFake{
		gatewayCurrentPhysicalDriverFake: &gatewayCurrentPhysicalDriverFake{},
		attestResult:                     gatewayCurrentLANRecoveryPhysicalResultFixture(t, action, false),
		withdrawResult:                   gatewayCurrentLANRecoveryPhysicalResultFixture(t, action, true),
	}
	fixture.manager.gatewayCurrentPhysicalDriver = driver
	observed, err := fixture.manager.attestGatewayCurrentLANRecoveryBatchLocked(context.Background(), selection)
	if err != nil || observed.BatchAbsent || !reflect.DeepEqual(driver.attestAction, action) {
		t.Fatalf("batch physical attestation=%#v error=%v", observed, err)
	}
	withdrawn, err := fixture.manager.withdrawGatewayCurrentLANRecoveryBatchLocked(context.Background(), selection)
	if err != nil || !withdrawn.BatchAbsent || !reflect.DeepEqual(driver.withdrawAction, action) ||
		!reflect.DeepEqual(withdrawn.Attestation.State, action.Withdrawn) {
		t.Fatalf("batch physical withdrawal=%#v error=%v", withdrawn, err)
	}
	if !gatewayCurrentPhysicalAttestationMatchesSelection(withdrawn.Attestation, *selection.State) {
		t.Fatal("general current attestation rejected the canonical whole-batch withdrawal")
	}
	partialState := cloneGatewayCurrentRouteState(action.Before)
	partialApp := partialState.Apps[action.Head.AppID]
	partialApp.LAN = nil
	partialState.Apps[action.Head.AppID] = partialApp
	partialState.Digest, _ = gatewayCurrentRouteStateDigest(partialState)
	partialAttestation := gatewayCurrentPhysicalAttestationFixture(t, partialState, action.Terminal,
		gatewayCurrentPhysicalRecoveryMixed)
	partialAttestation.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(action.Selected.LANRecovery)
	partialAttestation.Digest, _ = gatewayCurrentPhysicalAttestationDigest(partialAttestation)
	partial := gatewayCurrentLANRecoveryPhysicalResult{ActionDigest: action.Digest,
		Attestation: partialAttestation}
	partial.Digest, _ = gatewayCurrentLANRecoveryPhysicalResultDigest(partial)
	if !validGatewayCurrentLANRecoveryPhysicalResult(action, partial) {
		t.Fatal("batch-specific attestation rejected exact partial withdrawal")
	}
	if gatewayCurrentPhysicalAttestationMatchesSelection(partialAttestation, *selection.State) {
		t.Fatal("general current attestation treated partial batch withdrawal as complete")
	}
	driver.withdrawResult = gatewayCurrentLANRecoveryPhysicalResultFixture(t, action, false)
	if _, err := fixture.manager.withdrawGatewayCurrentLANRecoveryBatchLocked(context.Background(), selection); err == nil {
		t.Fatal("withdrawal accepted a present batch head")
	}
	tampered := gatewayCurrentLANRecoveryPhysicalResultFixture(t, action, true)
	tampered.Attestation.LANRecovery.Head++
	tampered.Attestation.Digest, _ = gatewayCurrentPhysicalAttestationDigest(tampered.Attestation)
	tampered.Digest, _ = gatewayCurrentLANRecoveryPhysicalResultDigest(tampered)
	driver.withdrawResult = tampered
	if _, err := fixture.manager.withdrawGatewayCurrentLANRecoveryBatchLocked(context.Background(), selection); err == nil {
		t.Fatal("withdrawal accepted changed immutable batch evidence")
	}
}

func TestGatewayCurrentLANRecoveryPhysicalActionTreatsUnpublishedGrantAsAbsent(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentRouteState(fixture.baseline)
	var appID string
	for candidate, app := range state.Apps {
		if app.LAN != nil {
			appID = candidate
			app.LAN = nil
			state.Apps[candidate] = app
			break
		}
	}
	if appID == "" {
		t.Fatal("current fixture has no LAN grant")
	}
	request := routeOperationNativeGrantRequest(t, state, appID)
	raw, err := gatewayV2LANBindingForRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	state.Revision++
	state.LANRecovery = &gatewayCurrentLANRecoveryBatch{Items: []gatewayCurrentLANRecoveryItem{{
		Kind: gatewayV2PendingLANGrant, AppID: appID, Grant: &gatewayCurrentLANBinding{Raw: raw},
	}}}
	state.Digest, _ = gatewayCurrentRouteStateDigest(state)
	if !validGatewayCurrentRouteState(state) {
		t.Fatal("manual unpublished grant batch is invalid")
	}
	terminal, terminalErr := newGatewayRebindAttemptTerminalViewLegacy(fixture.receipt)
	if terminalErr != nil {
		t.Fatalf("install unpublished grant batch: %v", terminalErr)
	}
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: state.Lineage,
		Receipt: &fixture.receipt, Terminal: &terminal, State: &state}
	action, err := fixture.manager.gatewayCurrentLANRecoveryPhysicalActionForSelectionLocked(selection)
	if err != nil || !reflect.DeepEqual(action.Before, action.Withdrawn) ||
		!reflect.DeepEqual(action.Cleared, action.Selected) {
		t.Fatalf("unpublished grant action=%#v error=%v", action, err)
	}
	absent := gatewayCurrentLANRecoveryPhysicalResultFixture(t, action, true)
	if !validGatewayCurrentLANRecoveryPhysicalResult(action, absent) {
		t.Fatal("unpublished grant exact absence proof was rejected")
	}
	present := gatewayCurrentLANRecoveryPhysicalResultFixture(t, action, false)
	if validGatewayCurrentLANRecoveryPhysicalResult(action, present) {
		t.Fatal("unpublished grant was reported as physically present")
	}
}
