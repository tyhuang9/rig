package generatedingress

import "github.com/hostd/hostd/internal/appaccess"

// GatewayV2LANStartupBindingProjection carries the SQL resolver's effective
// binding without changing the immutable grant or disable request. Current
// and retained projections have separate fields on startup claims: retained
// history cannot authorize serving. The runtime must validate this projection
// against protected lineage before using it.
type GatewayV2LANStartupBindingProjection struct {
	EffectiveProfile       GatewayV2ProfileBinding
	GatewaySource          appaccess.GatewayCurrentAuthorityRef
	TransferChain          []appaccess.GatewayRebindAllocationTransfer
	TransferChainTipDigest string
	TerminalReceiptDigest  string
}

func validGatewayV2LANStartupGrantBindings(claim GatewayV2LANStartupClaim) bool {
	if claim.CurrentBinding != nil && claim.RetainedBinding != nil {
		return false
	}
	if claim.RetainedBinding != nil {
		return claim.State == appaccess.AppAccessGrantCommitted && claim.DisableIntentOperationID != "" &&
			validGatewayV2LANStartupGrantProjection(*claim.RetainedBinding, claim.Request)
	}
	if claim.CurrentBinding == nil {
		return true // Legacy and pre-effect claims can have no SQL authority.
	}
	return claim.State != appaccess.AppAccessGrantPrepared && claim.State != appaccess.AppAccessGrantRolledBack &&
		(claim.State == appaccess.AppAccessGrantCommitted || len(claim.CurrentBinding.TransferChain) == 0) &&
		validGatewayV2LANStartupGrantProjection(*claim.CurrentBinding, claim.Request)
}

func validGatewayV2LANStartupDisableBindings(claim GatewayV2LANDisableStartupClaim) bool {
	if claim.CurrentBinding != nil && claim.RetainedBinding != nil {
		return false
	}
	projection := claim.CurrentBinding
	if claim.RetainedBinding != nil {
		if claim.State != appaccess.AppAccessDisableCommitted || claim.Request.SourceGrant == nil {
			return false
		}
		projection = claim.RetainedBinding
	} else if projection != nil && claim.State == appaccess.AppAccessDisableCommitted {
		return false
	}
	if projection == nil {
		return true
	}
	if claim.Request.SourceGrant != nil {
		return validGatewayV2LANStartupGrantProjection(*projection, *claim.Request.SourceGrant)
	}
	return claim.State != appaccess.AppAccessDisablePrepared && validGatewayV2LANStartupProjection(*projection) &&
		claim.Request.Port >= projection.EffectiveProfile.PortStart && claim.Request.Port <= projection.EffectiveProfile.PortEnd &&
		len(projection.TransferChain) == 0 && projection.TransferChainTipDigest == "" &&
		projection.EffectiveProfile.RevisionID == claim.Request.GatewayProfileRevisionID &&
		projection.EffectiveProfile.RevisionNumber == claim.Request.GatewayProfileRevisionNumber &&
		projection.EffectiveProfile.SpecDigest == claim.Request.GatewayProfileSpecDigest
}

// This validates the SQL projection's internal binding, not its physical
// authority. Callers must additionally compare it to the selected protected
// lineage and attest that generation before allowing startup or mutations.
func validGatewayV2LANStartupProjection(value GatewayV2LANStartupBindingProjection) bool {
	profile, source := value.EffectiveProfile, value.GatewaySource
	if !validGatewayProfileBinding(gatewayProfileBinding(profile)) || !validCanonicalUUID(source.OperationID) ||
		source.ProfileRevisionID != profile.RevisionID || source.ProfileRevisionNumber != profile.RevisionNumber ||
		source.ProfileSpecDigest != profile.SpecDigest || source.TerminalReceiptDigest != value.TerminalReceiptDigest {
		return false
	}
	switch source.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return source.TerminalReceiptDigest == "" && len(value.TransferChain) == 0 && value.TransferChainTipDigest == ""
	case appaccess.GatewayRebindSourceGatewayRebind:
		return validSHA256(source.TerminalReceiptDigest)
	default:
		return false
	}
}

func validGatewayV2LANStartupGrantProjection(value GatewayV2LANStartupBindingProjection,
	raw GatewayV2LANGrantRequest,
) bool {
	if !validGatewayV2LANStartupProjection(value) || raw.Port < value.EffectiveProfile.PortStart || raw.Port > value.EffectiveProfile.PortEnd {
		return false
	}
	profile := value.EffectiveProfile
	if len(value.TransferChain) == 0 {
		return value.TransferChainTipDigest == "" && profile.RevisionID == raw.GatewayProfileRevisionID &&
			profile.RevisionNumber == raw.GatewayProfileRevisionNumber && profile.SpecDigest == raw.GatewayProfileSpecDigest
	}
	previousDigest := ""
	previousNumber := raw.GatewayProfileRevisionNumber
	for index, transfer := range value.TransferChain {
		digest, err := appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if err != nil || digest != transfer.TransferDigest || transfer.AppID != raw.AppID ||
			transfer.AllocationID != raw.AllocationID || transfer.GrantAttemptID != raw.AttemptID ||
			transfer.SourceProfileRevisionID != raw.GatewayProfileRevisionID ||
			transfer.SourceProfileRevisionNumber != raw.GatewayProfileRevisionNumber ||
			transfer.SourceProfileSpecDigest != raw.GatewayProfileSpecDigest ||
			transfer.SuccessorProfileRevisionNumber <= previousNumber ||
			(index == 0 && transfer.PredecessorTransferDigest != nil) ||
			(index > 0 && (transfer.PredecessorTransferDigest == nil || *transfer.PredecessorTransferDigest != previousDigest)) {
			return false
		}
		previousDigest, previousNumber = digest, transfer.SuccessorProfileRevisionNumber
	}
	tip := value.TransferChain[len(value.TransferChain)-1]
	return value.TransferChainTipDigest == previousDigest && tip.OperationID == value.GatewaySource.OperationID &&
		tip.TerminalReceiptDigest == value.TerminalReceiptDigest && tip.SuccessorProfileRevisionID == profile.RevisionID &&
		tip.SuccessorProfileRevisionNumber == profile.RevisionNumber && tip.SuccessorProfileSpecDigest == profile.SpecDigest
}
