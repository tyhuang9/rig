package appaccess

import "context"

// GatewayRebindActiveApprovalAuthority records current roles separately from
// immutable approvals. Revocation must prevent serving, while leaving ownership
// and recovery history readable for withdrawal.
type GatewayRebindActiveApprovalAuthority struct {
	OperationID                      string
	SpecVersion                      int
	RebindActorID                    string
	ConfigureActorID                 string
	RebindApproverIsAdministrator    bool
	ConfigureApproverIsAdministrator bool
}

// ActiveRebindApprovalsAuthorizeServing checks only the active rebind approval
// boundary. Callers must also validate history, runtime and other approvals. An
// absent active claim requires an absent projection; it grants no other authority.
func (s HostingGatewayStartupSnapshot) ActiveRebindApprovalsAuthorizeServing() bool {
	if s.Rebind.Active == nil {
		return s.ActiveRebindApprovalAuthority == nil
	}
	if s.ActiveRebindApprovalAuthority == nil {
		return false
	}
	expected, err := gatewayRebindApprovalBinding(s.Rebind.Active.Claim)
	if err != nil {
		return false
	}
	expected.RebindApproverIsAdministrator = true
	expected.ConfigureApproverIsAdministrator = true
	return expected == *s.ActiveRebindApprovalAuthority
}

func readGatewayRebindActiveApprovalAuthority(ctx context.Context, query rowQuerier,
	snapshot GatewayRebindRecoverySnapshot,
) (*GatewayRebindActiveApprovalAuthority, error) {
	if snapshot.Active == nil {
		return nil, nil
	}
	value, err := gatewayRebindApprovalBinding(snapshot.Active.Claim)
	if err != nil {
		return nil, err
	}
	value.RebindApproverIsAdministrator, err = administratorExists(ctx, query, value.RebindActorID)
	if err != nil {
		return nil, err
	}
	value.ConfigureApproverIsAdministrator, err = administratorExists(ctx, query, value.ConfigureActorID)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// The recovery reader already validates the complete immutable claim. Keep the
// format union strict here so a crossed projection cannot authorize serving.
func gatewayRebindApprovalBinding(record GatewayRebindClaimRecord) (GatewayRebindActiveApprovalAuthority, error) {
	value := GatewayRebindActiveApprovalAuthority{SpecVersion: record.SpecVersion}
	var rebind, configure Approval
	switch record.SpecVersion {
	case 1:
		if record.Legacy == nil || record.V2 != nil {
			return value, ErrInvalidStoredState
		}
		value.OperationID = record.Legacy.Spec.OperationID
		rebind, configure = record.Legacy.RebindApproval, record.Legacy.ConfigureApproval
	case GatewayRebindSpecVersionV2:
		if record.V2 == nil || record.Legacy != nil || record.V2.Spec.Version != GatewayRebindSpecVersionV2 {
			return value, ErrInvalidStoredState
		}
		value.OperationID = record.V2.Spec.OperationID
		rebind, configure = record.V2.RebindApproval, record.V2.ConfigureApproval
	default:
		return value, ErrInvalidStoredState
	}
	value.RebindActorID, value.ConfigureActorID = rebind.ActorID, configure.ActorID
	if !validUUID(value.OperationID) || !validUUID(value.RebindActorID) || !validUUID(value.ConfigureActorID) ||
		rebind.Action != ActionRebindGateway || configure.Action != ActionConfigureGateway {
		return value, ErrInvalidStoredState
	}
	return value, nil
}
