package generatedingress

import "github.com/hostd/hostd/internal/appaccess"

// These pure transitions do not authorize a write or a physical effect. The
// caller must retain the SQL/effects/gateway guards, prove withdrawal, and run
// the exact SQL terminal/acknowledgment callbacks before clearing or advancing.
func gatewayCurrentLANRecoveryInstallState(state gatewayCurrentRouteState,
	claims gatewayV2LANAccessStartupClaims,
) (gatewayCurrentRouteState, error) {
	inspection, err := gatewayCurrentLANStartupCensus(state, claims)
	if err != nil || inspection.Disposition != GatewayV2LANStartupRecoveryOnly {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	recoveries := append([]GatewayV2LANAccessStartupRecovery(nil), inspection.Recoveries...)
	if len(recoveries) == 0 && inspection.RecoveryKind != "" {
		recoveries = append(recoveries, GatewayV2LANAccessStartupRecovery{
			Kind: inspection.RecoveryKind, OperationID: inspection.OperationID, AppID: inspection.AppID})
	}
	if len(recoveries) == 0 || len(recoveries) > maxStateApps {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	items := make([]gatewayCurrentLANRecoveryItem, 0, len(recoveries))
	for _, recovery := range recoveries {
		item := gatewayCurrentLANRecoveryItem{AppID: recovery.AppID}
		switch recovery.Kind {
		case GatewayV2LANRecoveryGrant:
			claim, exists := claims.grants.byAttempt[recovery.OperationID]
			raw, err := gatewayV2LANBindingForRequest(claim.Request)
			if !exists || err != nil || claim.Request.AppID != recovery.AppID || claim.State == appaccess.AppAccessGrantCommitted {
				return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
			}
			item.Kind, item.Grant = gatewayV2PendingLANGrant, &gatewayCurrentLANBinding{Raw: raw}
		case GatewayV2LANRecoveryDisable:
			claim, exists := claims.disables[recovery.OperationID]
			if !exists || claim.Request.AppID != recovery.AppID || claim.Request.SourceGrant == nil {
				return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
			}
			request := cloneGatewayCurrentDisableRequest(claim.Request)
			item.Kind, item.Disable = gatewayV2PendingLANDisable, &request
		default:
			return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
		}
		items = append(items, item)
	}
	return gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
		next.LANRecovery = &gatewayCurrentLANRecoveryBatch{
			Items: gatewayCurrentSortedRecoveryItems(items), LegacyPending: cloneGatewayCurrentPendingRoute(state.Pending)}
		next.Pending = nil
	})
}

// A replay after protected clearance returns identical bytes and revision.
// Callers must not persist a revision-only no-op as a new state transition.
func gatewayCurrentLANRecoveryClearedHeadState(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
	if !validGatewayCurrentRouteState(state) || state.LANRecovery == nil || state.LANRecovery.Head >= len(state.LANRecovery.Items) {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	item := state.LANRecovery.Items[state.LANRecovery.Head]
	if state.Apps[item.AppID].LAN == nil {
		return cloneGatewayCurrentRouteState(state), nil
	}
	return gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) {
		app := next.Apps[item.AppID]
		app.LAN = nil
		next.Apps[item.AppID] = app
	})
}

// The queue and its original pending evidence remain intact at Head == Count.
// A separate terminal census and complete absent-port proof authorize retirement.
func gatewayCurrentLANRecoveryAdvanceHeadState(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
	if !validGatewayCurrentRouteState(state) || state.LANRecovery == nil || state.LANRecovery.Head >= len(state.LANRecovery.Items) ||
		state.Apps[state.LANRecovery.Items[state.LANRecovery.Head].AppID].LAN != nil {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	return gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) { next.LANRecovery.Head++ })
}

func gatewayCurrentLANRecoveryRetiredState(state gatewayCurrentRouteState) (gatewayCurrentRouteState, error) {
	if !validGatewayCurrentRouteState(state) || state.LANRecovery == nil || state.LANRecovery.Head != len(state.LANRecovery.Items) {
		return gatewayCurrentRouteState{}, gatewayCurrentRouteOperationError(nil)
	}
	return gatewayCurrentNextState(state, func(next *gatewayCurrentRouteState) { next.LANRecovery = nil })
}
