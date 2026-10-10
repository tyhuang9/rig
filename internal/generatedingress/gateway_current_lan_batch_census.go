package generatedingress

import (
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// This is a pure comparison, not authority to mutate or serve. The caller must
// validate claims and obtain transfers from the same selected SQL snapshot,
// prove physical withdrawal, and separately attest unrelated retained history.
func gatewayCurrentLANRecoveryCensusMatchesHead(state gatewayCurrentRouteState,
	claims gatewayV2LANAccessStartupClaims, transfers []appaccess.GatewayRebindAllocationTransfer,
) bool {
	manifest, err := appaccess.GatewayRebindTransferManifestDigest(transfers)
	if err != nil || !validGatewayCurrentRouteState(state) || state.LANRecovery == nil ||
		manifest != state.TransferManifestDigest || !gatewayCurrentTransfersMatchState(transfers, state) {
		return false
	}
	batch := state.LANRecovery
	grantItems, disableItems, disableSources := map[string]bool{}, map[string]bool{}, map[string]bool{}
	ports, allocations := map[uint16]bool{}, map[string]bool{}
	for index, item := range batch.Items {
		var request GatewayV2LANGrantRequest
		switch item.Kind {
		case gatewayV2PendingLANGrant:
			request = gatewayV2LANGrantRequestForBinding(item.AppID, item.Grant.Raw)
			claim, exists := claims.grants.byAttempt[request.AttemptID]
			if !exists || claim.Request != request || claim.DisableIntentOperationID != "" || claim.RetainedBinding != nil ||
				!gatewayV2LANRecoveryGrantStateMatchesIndex(claim, index, batch.Head) {
				return false
			}
			if _, exists := claims.byAllocation[request.AllocationID]; exists {
				return false
			}
			if claim.CurrentBinding != nil {
				if !gatewayCurrentLANRecoveryProjectionMatches(state, request, *claim.CurrentBinding, transfers) {
					return false
				}
			} else if claim.State != appaccess.AppAccessGrantPrepared && claim.State != appaccess.AppAccessGrantApplying &&
				claim.State != appaccess.AppAccessGrantRolledBack {
				return false
			}
			grantItems[request.AttemptID] = true
		case gatewayV2PendingLANDisable:
			claim, exists := claims.disables[item.Disable.OperationID]
			if !exists || !reflect.DeepEqual(claim.Request, *item.Disable) ||
				!gatewayV2LANRecoveryDisableStateMatchesIndex(claim, index, batch.Head) ||
				(claim.ClearAcknowledged && state.Apps[item.AppID].LAN != nil) {
				return false
			}
			request = *item.Disable.SourceGrant
			source, exists := claims.grants.byAttempt[request.AttemptID]
			if !exists || source.Request != request || source.State != appaccess.AppAccessGrantCommitted ||
				source.DisableIntentOperationID != claim.Request.OperationID {
				return false
			}
			projection := claim.CurrentBinding
			if claim.State == appaccess.AppAccessDisableCommitted {
				projection = claim.RetainedBinding
			}
			if projection == nil || !gatewayCurrentLANRecoveryProjectionMatches(state, request, *projection, transfers) {
				return false
			}
			disableItems[claim.Request.OperationID], disableSources[request.AttemptID] = true, true
		default:
			return false
		}
		if ports[request.Port] || allocations[request.AllocationID] {
			return false
		}
		ports[request.Port], allocations[request.AllocationID] = true, true
	}
	live := make(map[string]bool)
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		request := gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw)
		claim, exists := claims.grants.byAttempt[request.AttemptID]
		if !exists || claim.Request != request {
			return false
		}
		live[request.AttemptID] = true
		if grantItems[request.AttemptID] || disableSources[request.AttemptID] {
			continue // Batch withdrawal, not this structural census, proves absence.
		}
		if claim.State != appaccess.AppAccessGrantCommitted || claim.RequiresRecovery || claim.DisableIntentOperationID != "" ||
			claim.CurrentBinding == nil || !gatewayCurrentGrantMatchesProjection(state, request, *claim.CurrentBinding) {
			return false
		}
	}
	for attempt, claim := range claims.grants.byAttempt {
		if live[attempt] || grantItems[attempt] || disableSources[attempt] {
			continue
		}
		if claim.State == appaccess.AppAccessGrantRolledBack && claim.DisableIntentOperationID == "" {
			continue
		}
		disable, exists := claims.byAllocation[claim.Request.AllocationID]
		if !exists || claim.State != appaccess.AppAccessGrantCommitted || disable.Request.SourceGrant == nil ||
			*disable.Request.SourceGrant != claim.Request || disable.State != appaccess.AppAccessDisableCommitted || !disable.ClearAcknowledged {
			return false
		}
	}
	for operation, claim := range claims.disables {
		if !disableItems[operation] && (claim.State != appaccess.AppAccessDisableCommitted || !claim.ClearAcknowledged) {
			return false
		}
	}
	return true
}

// A cleared transferred binding is matched against the immutable manifest,
// rather than reconstructed as a live app binding. The manifest digest is
// checked against protected state by the complete census above.
func gatewayCurrentLANRecoveryProjectionMatches(state gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest, projection GatewayV2LANStartupBindingProjection,
	transfers []appaccess.GatewayRebindAllocationTransfer,
) bool {
	if !validGatewayV2LANStartupGrantProjection(projection, request) || !gatewayCurrentStartupProjectionMatches(state, projection) {
		return false
	}
	if app := state.Apps[request.AppID]; app.LAN != nil {
		return gatewayCurrentGrantMatchesProjection(state, request, projection)
	}
	if len(projection.TransferChain) == 0 {
		return request.GatewayProfileRevisionID == state.Profile.RevisionID &&
			request.GatewayProfileRevisionNumber == state.Profile.RevisionNumber && request.GatewayProfileSpecDigest == state.Profile.SpecDigest
	}
	tip := projection.TransferChain[len(projection.TransferChain)-1]
	for _, transfer := range transfers {
		if transfer.AppID == request.AppID && transfer.AllocationID == request.AllocationID && transfer.GrantAttemptID == request.AttemptID {
			return reflect.DeepEqual(tip, transfer)
		}
	}
	return false
}
