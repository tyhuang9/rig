package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// The caller holds both gateway locks. SQL selects the generation; raw grant
// identities remain unchanged while canonical operational endpoints provide
// the protected binding against which effective SQL projections are checked.
func (m *Manager) inspectGatewayCurrentLANStartupLocked(ctx context.Context,
	claims gatewayV2LANAccessStartupClaims,
) (GatewayV2LANAccessStartupInspection, bool, error) {
	selection, snapshot, handled, err := m.gatewayCurrentSelectedStateForRecoveryLocked(ctx)
	if err != nil || !handled {
		return GatewayV2LANAccessStartupInspection{}, handled, err
	}
	inspection, err := gatewayCurrentLANStartupCensus(*selection.State, claims)
	if err != nil {
		return GatewayV2LANAccessStartupInspection{}, true, err
	}
	physical, err := m.attestGatewayCurrentPhysicalLocked(ctx, selection)
	if err != nil || (selection.State.Pending == nil && physical.Outcome != gatewayCurrentPhysicalStableServing) {
		return GatewayV2LANAccessStartupInspection{}, true, gatewayV2StartupInspectionError(ctx)
	}
	confirmed, confirmedSQL, present, err := m.readOptionalGatewayCurrentSelectionLocked(ctx)
	if err != nil || !present || !reflect.DeepEqual(snapshot, confirmedSQL) ||
		!sameGatewayCurrentSelection(selection, confirmed) || ctx.Err() != nil {
		return GatewayV2LANAccessStartupInspection{}, true, gatewayV2StartupInspectionError(ctx)
	}
	return inspection, true, nil
}

func gatewayCurrentLANStartupCensus(state gatewayCurrentRouteState,
	claims gatewayV2LANAccessStartupClaims,
) (GatewayV2LANAccessStartupInspection, error) {
	invalid := gatewayV2StartupInspectionError(nil)
	if !validGatewayCurrentRouteState(state) || state.LANRecovery != nil {
		return GatewayV2LANAccessStartupInspection{}, invalid
	}
	before := state
	var effective *gatewayCurrentRouteState
	if state.Pending != nil {
		projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(state)
		if err != nil {
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
		before, effective = projection.Before, projection.Effective
	}
	candidates := make(map[string]gatewayV2LANAccessStartupRecoveryCandidate)
	add := func(kind, operation, appID string, port uint16) bool {
		if !validCanonicalUUID(operation) || !validAppID(appID) || port < state.Profile.PortStart || port > state.Profile.PortEnd {
			return false
		}
		key := kind + "\x00" + operation + "\x00" + appID
		candidate := gatewayV2LANAccessStartupRecoveryCandidate{
			GatewayV2LANAccessStartupRecovery: GatewayV2LANAccessStartupRecovery{Kind: kind, OperationID: operation, AppID: appID}, port: port}
		if previous, exists := candidates[key]; exists && previous != candidate {
			return false
		}
		candidates[key] = candidate
		return true
	}
	live := make(map[string]bool)
	for appID, app := range before.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw)
		grant, exists := claims.grants.byAttempt[request.AttemptID]
		if !exists || grant.Request != request || !gatewayCurrentStartupGrantMatches(before, grant, state.Pending) {
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
		live[request.AttemptID] = true
		if disable, exists := claims.byAllocation[request.AllocationID]; exists {
			if disable.Request.SourceGrant == nil || *disable.Request.SourceGrant != request ||
				(disable.State == appaccess.AppAccessDisableCommitted && !gatewayCurrentStartupPendingDisable(state.Pending, disable)) ||
				!add(GatewayV2LANRecoveryDisable, disable.Request.OperationID, appID, request.Port) {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
		} else if grant.State != appaccess.AppAccessGrantCommitted || grant.RequiresRecovery || grant.DisableIntentOperationID != "" {
			if !add(GatewayV2LANRecoveryGrant, request.AttemptID, appID, request.Port) {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
		}
	}
	if pending := state.Pending; pending != nil {
		switch pending.Kind {
		case "", gatewayV2PendingRouteSwitch:
			// Ordinary route recovery preserves all LAN bindings and precedes workers.
		case gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal:
			endpoint := before
			if pending.Kind == gatewayV2PendingLANGrant {
				if effective == nil {
					return GatewayV2LANAccessStartupInspection{}, invalid
				}
				endpoint = *effective
			}
			app := endpoint.Apps[pending.AppID]
			if app.LAN == nil {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
			request := gatewayV2LANGrantRequestForBinding(pending.AppID, app.LAN.Raw)
			grant, exists := claims.grants.byAttempt[request.AttemptID]
			if !exists || grant.Request != request || !gatewayCurrentStartupGrantMatches(endpoint, grant, pending) ||
				!add(GatewayV2LANRecoveryGrant, request.AttemptID, request.AppID, request.Port) {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
			live[request.AttemptID] = true
		case gatewayV2PendingLANDisable:
			if pending.Disable == nil {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
			disable, exists := claims.disables[pending.Disable.OperationID]
			if !exists || !gatewayCurrentStartupPendingDisable(pending, disable) ||
				!add(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID, disable.Request.Port) {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
		default:
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
	}
	for attempt, grant := range claims.grants.byAttempt {
		if live[attempt] {
			continue
		}
		if disable, exists := claims.byAllocation[grant.Request.AllocationID]; exists && disable.Request.SourceGrant != nil &&
			*disable.Request.SourceGrant == grant.Request {
			continue
		}
		if grant.RetainedBinding != nil || (grant.CurrentBinding != nil && !gatewayCurrentStartupProjectionMatches(state, *grant.CurrentBinding)) {
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
		if grant.State != appaccess.AppAccessGrantRolledBack && !add(GatewayV2LANRecoveryGrant, attempt, grant.Request.AppID, grant.Request.Port) {
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
	}
	for _, disable := range claims.disables {
		pending := gatewayCurrentStartupPendingDisable(state.Pending, disable)
		if disable.State == appaccess.AppAccessDisableCommitted && !pending && disable.ClearAcknowledged {
			// Terminal history may refer to an older generation and reused port.
			// It cannot match a live raw source (checked in the loop above).
			continue
		}
		projection := disable.CurrentBinding
		if pending && disable.State == appaccess.AppAccessDisableCommitted {
			projection = disable.RetainedBinding
		}
		if app := before.Apps[disable.Request.AppID]; app.LAN != nil {
			if projection == nil || !gatewayCurrentDisableMatchesProjection(before, disable.Request, *projection) {
				return GatewayV2LANAccessStartupInspection{}, invalid
			}
		} else if projection != nil && !gatewayCurrentStartupProjectionMatches(state, *projection) {
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
		if !add(GatewayV2LANRecoveryDisable, disable.Request.OperationID, disable.Request.AppID, disable.Request.Port) {
			return GatewayV2LANAccessStartupInspection{}, invalid
		}
	}
	return gatewayV2LANStartupInspectionForCandidates(candidates, claims)
}

func gatewayCurrentStartupPendingDisable(pending *gatewayCurrentPendingRoute, claim GatewayV2LANDisableStartupClaim) bool {
	return pending != nil && pending.Kind == gatewayV2PendingLANDisable && !claim.ClearAcknowledged &&
		pending.Disable != nil && reflect.DeepEqual(*pending.Disable, claim.Request)
}

func gatewayCurrentStartupProjectionMatches(state gatewayCurrentRouteState, projection GatewayV2LANStartupBindingProjection) bool {
	return validGatewayV2LANStartupProjection(projection) &&
		projection.EffectiveProfile == GatewayV2ProfileBinding(state.Profile) &&
		projection.GatewaySource == gatewayCurrentAuthority(state.Lineage)
}

func gatewayCurrentStartupGrantMatches(state gatewayCurrentRouteState, grant GatewayV2LANStartupClaim,
	pending *gatewayCurrentPendingRoute,
) bool {
	if grant.CurrentBinding != nil {
		return gatewayCurrentGrantMatchesProjection(state, grant.Request, *grant.CurrentBinding)
	}
	if grant.RetainedBinding != nil {
		// Retained authority can explain only an exact durable disable marker.
		// The resulting census always requires quarantine before admission.
		return pending != nil && pending.Kind == gatewayV2PendingLANDisable && pending.Disable != nil &&
			pending.Disable.SourceGrant != nil && *pending.Disable.SourceGrant == grant.Request &&
			gatewayCurrentGrantMatchesProjection(state, grant.Request, *grant.RetainedBinding)
	}
	app := state.Apps[grant.Request.AppID]
	if app.LAN == nil || app.LAN.Transfer != nil || gatewayV2LANGrantRequestForBinding(grant.Request.AppID, app.LAN.Raw) != grant.Request {
		return false
	}
	return grant.State == appaccess.AppAccessGrantPrepared || grant.State == appaccess.AppAccessGrantApplying ||
		(grant.State == appaccess.AppAccessGrantRolledBack && pending != nil && pending.Kind == gatewayV2PendingLANWithdrawal)
}
