package generatedingress

import "reflect"

// gatewayCurrentGrantMatchesProjection binds an immutable raw grant to the
// effective SQL projection and the selected protected current generation. It
// never rewrites a transferred grant's raw source profile to the current
// effective profile.
func gatewayCurrentGrantMatchesProjection(state gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest, projection GatewayV2LANStartupBindingProjection,
) bool {
	if !validGatewayV2LANStartupGrantProjection(projection, request) {
		return false
	}
	raw, err := gatewayV2LANBindingForRequest(request)
	app, exists := state.Apps[request.AppID]
	return err == nil && exists && app.LAN != nil && reflect.DeepEqual(app.LAN.Raw, raw) &&
		gatewayCurrentBindingMatchesProjection(state, app.LAN, projection)
}

// gatewayCurrentDisableMatchesProjection matches only the live protected
// binding. Retained historical projections are validated by their startup
// history reader and cannot authorize a current disable.
func gatewayCurrentDisableMatchesProjection(state gatewayCurrentRouteState,
	request GatewayV2LANDisableRequest, projection GatewayV2LANStartupBindingProjection,
) bool {
	if !validGatewayV2LANDisableRequest(request) || !validGatewayV2LANStartupProjection(projection) {
		return false
	}
	app, exists := state.Apps[request.AppID]
	if !exists || app.LAN == nil {
		return false
	}
	if request.SourceGrant != nil {
		return gatewayCurrentGrantMatchesProjection(state, *request.SourceGrant, projection)
	}
	raw := app.LAN.Raw
	return app.LAN.Transfer == nil && len(projection.TransferChain) == 0 &&
		projection.TransferChainTipDigest == "" &&
		raw.AllocationID == request.AllocationID && raw.OwnerOperationID == request.OwnerOperationID &&
		raw.AccessRevisionID == request.AccessRevisionID &&
		raw.AccessRevisionNumber == request.AccessRevisionNumber && raw.AccessSpecDigest == request.AccessSpecDigest &&
		raw.Port == request.Port && raw.ProfileRevisionID == state.Profile.RevisionID &&
		raw.ProfileRevisionNumber == state.Profile.RevisionNumber && raw.ProfileSpecDigest == state.Profile.SpecDigest &&
		request.GatewayProfileRevisionID == state.Profile.RevisionID &&
		request.GatewayProfileRevisionNumber == state.Profile.RevisionNumber &&
		request.GatewayProfileSpecDigest == state.Profile.SpecDigest &&
		gatewayCurrentBindingMatchesProjection(state, app.LAN, projection)
}

func gatewayCurrentBindingMatchesProjection(state gatewayCurrentRouteState, binding *gatewayCurrentLANBinding,
	projection GatewayV2LANStartupBindingProjection,
) bool {
	if !validGatewayV2LANStartupProjection(projection) || binding == nil {
		return false
	}
	proof, err := gatewayCurrentEffectiveProof(state, binding)
	if err != nil || projection.EffectiveProfile != proof.EffectiveProfile ||
		projection.GatewaySource != gatewayCurrentAuthority(proof.ProtectedLineage) ||
		projection.TransferChainTipDigest != proof.TransferChainTipDigest ||
		projection.TerminalReceiptDigest != proof.TerminalReceiptDigest {
		return false
	}
	if binding.Transfer == nil {
		return len(projection.TransferChain) == 0
	}
	return len(projection.TransferChain) != 0 &&
		reflect.DeepEqual(projection.TransferChain[len(projection.TransferChain)-1], *binding.Transfer)
}
