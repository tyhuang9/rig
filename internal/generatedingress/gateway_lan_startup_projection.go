package generatedingress

import "github.com/hostd/hostd/internal/appaccess"

// These projections preserve the complete startup SQL evidence for runtime
// validation. They neither authorize serving nor replace missing authority.
func GatewayLANDisableStartupClaims(snapshot appaccess.AppAccessDisableStartupSnapshot) []GatewayV2LANDisableStartupClaim {
	claims := make([]GatewayV2LANDisableStartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		request := GatewayV2LANDisableRequest{
			OperationID: entry.Claim.OperationID, RequestDigest: entry.Claim.RequestDigest,
			SpecDigest: entry.Claim.SpecDigest, AppID: entry.Claim.Spec.AppID,
			AllocationID: entry.Claim.Spec.AllocationID, OwnerOperationID: entry.Claim.Spec.OwnerOperationID,
			Port: entry.Claim.Spec.Port, AccessRevisionID: entry.Claim.Spec.AccessRevisionID,
			AccessRevisionNumber: entry.Claim.Spec.AccessRevisionNumber,
			AccessSpecDigest:     entry.Revision.SpecDigest, ApprovedBy: entry.Claim.ApprovedBy,
			GatewayProfileRevisionID:     entry.Claim.Spec.GatewayProfileRevisionID,
			GatewayProfileRevisionNumber: entry.Claim.Spec.GatewayProfileRevisionNumber,
			GatewayProfileSpecDigest:     entry.Profile.SpecDigest,
		}
		if entry.SourceGrant != nil {
			grant := GatewayLANGrantRequest(*entry.SourceGrant)
			request.SourceGrant = &grant
		}
		claims = append(claims, GatewayV2LANDisableStartupClaim{
			Request: request, State: entry.Claim.State, StateSequence: entry.Claim.StateSequence,
			CurrentBinding: GatewayLANStartupBinding(entry.EffectiveProfile, entry.CurrentGatewaySource,
				entry.TransferChain, entry.TransferChainTipDigest, entry.TerminalReceiptDigest),
			RetainedBinding: GatewayLANStartupBinding(entry.RetainedEffectiveProfile, entry.RetainedGatewaySource,
				entry.RetainedTransferChain, entry.RetainedTransferChainTipDigest, entry.RetainedTerminalReceiptDigest),
			ClearAcknowledged: entry.ProtectedClearAck != nil || entry.SuccessorAck != nil,
			RequiresRecovery: entry.Claim.State != appaccess.AppAccessDisableCommitted &&
				(entry.AppArchived || !entry.AccessHeadCurrent || !entry.ProfileHeadCurrent ||
					!entry.ApproverIsAdministrator),
		})
	}
	return claims
}

func GatewayLANGrantStartupClaims(snapshot appaccess.AppAccessGrantStartupSnapshot) []GatewayV2LANStartupClaim {
	claims := make([]GatewayV2LANStartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		claim := GatewayV2LANStartupClaim{
			Request:       GatewayLANGrantRequest(entry.Claim),
			State:         entry.Claim.State,
			StateSequence: entry.Claim.StateSequence,
			CurrentBinding: GatewayLANStartupBinding(entry.EffectiveProfile, entry.CurrentGatewaySource,
				entry.TransferChain, entry.TransferChainTipDigest, entry.TerminalReceiptDigest),
			RetainedBinding: GatewayLANStartupBinding(entry.RetainedEffectiveProfile, entry.RetainedGatewaySource,
				entry.RetainedTransferChain, entry.RetainedTransferChainTipDigest, entry.RetainedTerminalReceiptDigest),
			RequiresRecovery: entry.AppArchived || !entry.AccessHeadCurrent ||
				!entry.ProfileHeadCurrent || !entry.ApproverIsAdministrator,
		}
		if entry.DisableIntent != nil {
			claim.DisableIntentOperationID = entry.DisableIntent.OperationID
		}
		claims = append(claims, claim)
	}
	return claims
}

func GatewayLANStartupBinding(profile appaccess.GatewayProfileRevision, source appaccess.GatewayCurrentAuthorityRef,
	chain []appaccess.GatewayRebindAllocationTransfer, tip, receipt string,
) *GatewayV2LANStartupBindingProjection {
	// A raw profile on a precommit or legacy claim is not effective authority.
	// Preserve partial authority evidence so runtime validation can reject it;
	// never replace a missing source with the raw request's profile.
	if source == (appaccess.GatewayCurrentAuthorityRef{}) && len(chain) == 0 && tip == "" && receipt == "" {
		return nil
	}
	chainCopy := append([]appaccess.GatewayRebindAllocationTransfer(nil), chain...)
	for index := range chainCopy {
		if previous := chainCopy[index].PredecessorTransferDigest; previous != nil {
			digest := *previous
			chainCopy[index].PredecessorTransferDigest = &digest
		}
	}
	return &GatewayV2LANStartupBindingProjection{
		EffectiveProfile: GatewayV2ProfileBinding{
			RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
			SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
			PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd,
		},
		GatewaySource: source, TransferChain: chainCopy,
		TransferChainTipDigest: tip, TerminalReceiptDigest: receipt,
	}
}

func GatewayLANGrantRequest(claim appaccess.AppAccessGrantClaim) GatewayV2LANGrantRequest {
	return GatewayV2LANGrantRequest{
		AttemptID: claim.AttemptID, ClaimRequestDigest: claim.RequestDigest,
		AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID, Port: claim.Spec.Port,
		AccessRevisionID:             claim.Spec.AccessRevisionID,
		AccessRevisionNumber:         claim.Spec.AccessRevisionNumber,
		AccessSpecDigest:             claim.Spec.AccessSpecDigest,
		ApprovedBy:                   claim.Spec.ApprovedBy,
		GatewayProfileRevisionID:     claim.Spec.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: claim.Spec.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     claim.Spec.GatewayProfileSpecDigest,
	}
}
