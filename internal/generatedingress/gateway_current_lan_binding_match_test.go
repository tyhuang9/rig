package generatedingress

import (
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANBindingMatchersPreserveRawAndEffectiveProfiles(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	state := cloneGatewayCurrentOperationState(fixture.baseline)
	appID, transferred := routeOperationTransferredApp(t, state)
	if transferred.LAN == nil || transferred.LAN.Transfer == nil {
		t.Fatal("fixture has no transferred binding")
	}
	rawRequest := gatewayV2LANGrantRequestForBinding(appID, transferred.LAN.Raw)
	transferredProjection := gatewayCurrentBindingProjection(t, state, transferred.LAN,
		[]appaccess.GatewayRebindAllocationTransfer{*transferred.LAN.Transfer})
	if !gatewayCurrentGrantMatchesProjection(state, rawRequest, transferredProjection) {
		t.Fatal("exact transferred raw grant did not match its effective current projection")
	}
	disable := disableRequestForGrant(t, rawRequest)
	if !gatewayCurrentDisableMatchesProjection(state, disable, transferredProjection) {
		t.Fatal("exact transferred disable did not match its raw grant and effective projection")
	}

	rewritten := transferred.LAN.Raw
	rewritten.ProfileRevisionID = state.Profile.RevisionID
	rewritten.ProfileRevisionNumber = state.Profile.RevisionNumber
	rewritten.ProfileSpecDigest = state.Profile.SpecDigest
	rewritten.AccessSpecDigest = mustGatewayV2LANAccessDigest(t,
		gatewayV2LANGrantRequestForBinding(appID, rewritten))
	rewrittenRequest := gatewayV2LANGrantRequestForBinding(appID, rewritten)
	if gatewayCurrentGrantMatchesProjection(state, rewrittenRequest, transferredProjection) {
		t.Fatal("effective profile rewrite was accepted as the immutable raw grant")
	}

	brokenChain := transferredProjection
	brokenChain.TransferChain = append([]appaccess.GatewayRebindAllocationTransfer(nil),
		transferredProjection.TransferChain...)
	brokenChain.TransferChain[0].TransferDigest = ""
	if gatewayCurrentGrantMatchesProjection(state, rawRequest, brokenChain) {
		t.Fatal("broken transfer chain matched the protected current binding")
	}

	nativeRequest := routeOperationNativeGrantRequest(t, state, appID)
	nativeRaw, err := gatewayV2LANBindingForRequest(nativeRequest)
	if err != nil {
		t.Fatal(err)
	}
	nativeApp := cloneGatewayCurrentOperationApp(state.Apps[appID])
	nativeApp.LAN = &gatewayCurrentLANBinding{Raw: nativeRaw}
	state.Apps[appID] = nativeApp
	state.Digest = ""
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		t.Fatalf("build native current binding: %v", err)
	}
	nativeProjection := gatewayCurrentBindingProjection(t, state, nativeApp.LAN, nil)
	if !gatewayCurrentGrantMatchesProjection(state, nativeRequest, nativeProjection) {
		t.Fatal("native grant on a rebind current profile did not match")
	}
	noSource := disableRequestForGrant(t, nativeRequest)
	noSource.SourceGrant = nil
	if !gatewayCurrentDisableMatchesProjection(state, noSource, nativeProjection) {
		t.Fatal("exact no-source native disable did not match")
	}
	wrongAllocation := disableRequestForGrant(t, routeOperationNativeGrantRequest(t, state, appID))
	wrongAllocation.SourceGrant = nil
	if gatewayCurrentDisableMatchesProjection(state, wrongAllocation, nativeProjection) {
		t.Fatal("no-source disable matched the wrong immutable allocation")
	}
	if gatewayCurrentDisableMatchesProjection(fixture.baseline,
		func() GatewayV2LANDisableRequest {
			value := disableRequestForGrant(t, rawRequest)
			value.SourceGrant = nil
			return value
		}(), transferredProjection) {
		t.Fatal("no-source disable accepted a transferred binding")
	}
}

func gatewayCurrentBindingProjection(t *testing.T, state gatewayCurrentRouteState,
	binding *gatewayCurrentLANBinding, chain []appaccess.GatewayRebindAllocationTransfer,
) GatewayV2LANStartupBindingProjection {
	t.Helper()
	proof, err := gatewayCurrentEffectiveProof(state, binding)
	if err != nil {
		t.Fatal(err)
	}
	return GatewayV2LANStartupBindingProjection{
		EffectiveProfile: proof.EffectiveProfile, GatewaySource: gatewayCurrentAuthority(proof.ProtectedLineage),
		TransferChain: chain, TransferChainTipDigest: proof.TransferChainTipDigest,
		TerminalReceiptDigest: proof.TerminalReceiptDigest,
	}
}
