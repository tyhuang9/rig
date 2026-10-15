package generatedingress

import "context"

// gatewayStartupDiagnosticCheckpoint is private, finite diagnostic evidence.
// It never changes public error text and never retains an upstream error,
// configuration, path, SQL value, or runtime output.
type gatewayStartupDiagnosticCheckpoint uint8

const (
	gatewayStartupDiagnosticCheckpointUnknown gatewayStartupDiagnosticCheckpoint = iota
	gatewayStartupDiagnosticCheckpointUpgradeClaimsValidation
	gatewayStartupDiagnosticCheckpointUpgradeGatewayLock
	gatewayStartupDiagnosticCheckpointCurrentSelectionPrecondition
	gatewayStartupDiagnosticCheckpointCurrentSQLFirstRead
	gatewayStartupDiagnosticCheckpointCurrentProtectedSelection
	gatewayStartupDiagnosticCheckpointCurrentAbsenceProtectedPresence
	gatewayStartupDiagnosticCheckpointCurrentSQLStability
	gatewayStartupDiagnosticCheckpointCurrentProtectedStability
	gatewayStartupDiagnosticCheckpointUpgradeHistory
	gatewayStartupDiagnosticCheckpointUpgradeSourceState
	gatewayStartupDiagnosticCheckpointUpgradeCurrentBinding
	gatewayStartupDiagnosticCheckpointUpgradeClaimHistoryBinding
	gatewayStartupDiagnosticCheckpointUpgradePendingProof
	gatewayStartupDiagnosticCheckpointUpgradeLANBatchProof
	gatewayStartupDiagnosticCheckpointUpgradeCommittedTopology
	gatewayStartupDiagnosticCheckpointUpgradeCommittedHostRoutes
	gatewayStartupDiagnosticCheckpointUpgradeCommittedSelectedInterface
	gatewayStartupDiagnosticCheckpointUpgradeTerminalReceiptProof
	gatewayStartupDiagnosticCheckpointUpgradeNoArtifactProof
	gatewayStartupDiagnosticCheckpointUpgradeDisposition
	gatewayStartupDiagnosticCheckpointUpgradeSourceStability
	gatewayStartupDiagnosticCheckpointUpgradeHistoryStability
	gatewayStartupDiagnosticCheckpointUpgradeCurrentStability
	gatewayStartupDiagnosticCheckpointUpgradeGatewayUnlock
	gatewayStartupDiagnosticCheckpointLANClaimsValidation
	gatewayStartupDiagnosticCheckpointLANGatewayLock
	gatewayStartupDiagnosticCheckpointLANCurrentGatewaySelection
	gatewayStartupDiagnosticCheckpointLANCommittedState
	gatewayStartupDiagnosticCheckpointLANStartupAuthority
	gatewayStartupDiagnosticCheckpointLANProtectedClaimBinding
	gatewayStartupDiagnosticCheckpointLANPendingStateProof
	gatewayStartupDiagnosticCheckpointLANCommittedTopology
	gatewayStartupDiagnosticCheckpointLANCommittedPublications
	gatewayStartupDiagnosticCheckpointLANOrphanClaimRecovery
	gatewayStartupDiagnosticCheckpointLANDisableBinding
	gatewayStartupDiagnosticCheckpointLANDisableAbsenceProof
	gatewayStartupDiagnosticCheckpointLANSelectedInterface
	gatewayStartupDiagnosticCheckpointLANProtectedStateStability
	gatewayStartupDiagnosticCheckpointLANAuthorityStability
	gatewayStartupDiagnosticCheckpointLANRecoveryCandidateSelection
	gatewayStartupDiagnosticCheckpointLANGatewayUnlock
)

func (checkpoint gatewayStartupDiagnosticCheckpoint) String() string {
	switch checkpoint {
	case gatewayStartupDiagnosticCheckpointUpgradeClaimsValidation:
		return "upgrade_claims_validation"
	case gatewayStartupDiagnosticCheckpointUpgradeGatewayLock:
		return "upgrade_gateway_lock"
	case gatewayStartupDiagnosticCheckpointCurrentSelectionPrecondition:
		return "current_selection_precondition"
	case gatewayStartupDiagnosticCheckpointCurrentSQLFirstRead:
		return "current_sql_first_read"
	case gatewayStartupDiagnosticCheckpointCurrentProtectedSelection:
		return "current_protected_selection"
	case gatewayStartupDiagnosticCheckpointCurrentAbsenceProtectedPresence:
		return "current_absence_protected_presence"
	case gatewayStartupDiagnosticCheckpointCurrentSQLStability:
		return "current_sql_stability"
	case gatewayStartupDiagnosticCheckpointCurrentProtectedStability:
		return "current_protected_stability"
	case gatewayStartupDiagnosticCheckpointUpgradeHistory:
		return "upgrade_history"
	case gatewayStartupDiagnosticCheckpointUpgradeSourceState:
		return "upgrade_source_state"
	case gatewayStartupDiagnosticCheckpointUpgradeCurrentBinding:
		return "upgrade_current_binding"
	case gatewayStartupDiagnosticCheckpointUpgradeClaimHistoryBinding:
		return "upgrade_claim_history_binding"
	case gatewayStartupDiagnosticCheckpointUpgradePendingProof:
		return "upgrade_pending_proof"
	case gatewayStartupDiagnosticCheckpointUpgradeLANBatchProof:
		return "upgrade_lan_batch_proof"
	case gatewayStartupDiagnosticCheckpointUpgradeCommittedTopology:
		return "upgrade_committed_topology"
	case gatewayStartupDiagnosticCheckpointUpgradeCommittedHostRoutes:
		return "upgrade_committed_host_routes"
	case gatewayStartupDiagnosticCheckpointUpgradeCommittedSelectedInterface:
		return "upgrade_committed_selected_interface"
	case gatewayStartupDiagnosticCheckpointUpgradeTerminalReceiptProof:
		return "upgrade_terminal_receipt_proof"
	case gatewayStartupDiagnosticCheckpointUpgradeNoArtifactProof:
		return "upgrade_no_artifact_proof"
	case gatewayStartupDiagnosticCheckpointUpgradeDisposition:
		return "upgrade_disposition"
	case gatewayStartupDiagnosticCheckpointUpgradeSourceStability:
		return "upgrade_source_stability"
	case gatewayStartupDiagnosticCheckpointUpgradeHistoryStability:
		return "upgrade_history_stability"
	case gatewayStartupDiagnosticCheckpointUpgradeCurrentStability:
		return "upgrade_current_stability"
	case gatewayStartupDiagnosticCheckpointUpgradeGatewayUnlock:
		return "upgrade_gateway_unlock"
	case gatewayStartupDiagnosticCheckpointLANClaimsValidation:
		return "lan_claims_validation"
	case gatewayStartupDiagnosticCheckpointLANGatewayLock:
		return "lan_gateway_lock"
	case gatewayStartupDiagnosticCheckpointLANCurrentGatewaySelection:
		return "lan_current_gateway_selection"
	case gatewayStartupDiagnosticCheckpointLANCommittedState:
		return "lan_committed_state"
	case gatewayStartupDiagnosticCheckpointLANStartupAuthority:
		return "lan_startup_authority"
	case gatewayStartupDiagnosticCheckpointLANProtectedClaimBinding:
		return "lan_protected_claim_binding"
	case gatewayStartupDiagnosticCheckpointLANPendingStateProof:
		return "lan_pending_state_proof"
	case gatewayStartupDiagnosticCheckpointLANCommittedTopology:
		return "lan_committed_topology"
	case gatewayStartupDiagnosticCheckpointLANCommittedPublications:
		return "lan_committed_publications"
	case gatewayStartupDiagnosticCheckpointLANOrphanClaimRecovery:
		return "lan_orphan_claim_recovery"
	case gatewayStartupDiagnosticCheckpointLANDisableBinding:
		return "lan_disable_binding"
	case gatewayStartupDiagnosticCheckpointLANDisableAbsenceProof:
		return "lan_disable_absence_proof"
	case gatewayStartupDiagnosticCheckpointLANSelectedInterface:
		return "lan_selected_interface"
	case gatewayStartupDiagnosticCheckpointLANProtectedStateStability:
		return "lan_protected_state_stability"
	case gatewayStartupDiagnosticCheckpointLANAuthorityStability:
		return "lan_authority_stability"
	case gatewayStartupDiagnosticCheckpointLANRecoveryCandidateSelection:
		return "lan_recovery_candidate_selection"
	case gatewayStartupDiagnosticCheckpointLANGatewayUnlock:
		return "lan_gateway_unlock"
	default:
		return "unknown"
	}
}

func (checkpoint gatewayStartupDiagnosticCheckpoint) valid() bool {
	return checkpoint != gatewayStartupDiagnosticCheckpointUnknown && checkpoint.String() != "unknown"
}

func newGatewayStartupDiagnosticError(ctx context.Context,
	checkpoint gatewayStartupDiagnosticCheckpoint,
) *Error {
	code := DiagnosticRouteUnresolved
	if ctx != nil && ctx.Err() != nil {
		code = DiagnosticCancelled
	}
	return newGatewayStartupDiagnosticErrorWithCode(code, checkpoint)
}

func newGatewayStartupDiagnosticErrorWithCode(code DiagnosticCode,
	checkpoint gatewayStartupDiagnosticCheckpoint,
) *Error {
	if !checkpoint.valid() {
		checkpoint = gatewayStartupDiagnosticCheckpointUnknown
	}
	return &Error{Code: code, startupCheckpoint: checkpoint}
}

// gatewayStartupDiagnosticErrorFrom is used only at boundaries that already
// return a sanitized Error. It copies finite state from a direct Error and
// never retains or unwraps an upstream cause.
func gatewayStartupDiagnosticErrorFrom(ctx context.Context, err error,
	fallback gatewayStartupDiagnosticCheckpoint,
) *Error {
	checkpoint := gatewayStartupDiagnosticCheckpointFromError(err)
	if !checkpoint.valid() {
		checkpoint = fallback
	}
	if !checkpoint.valid() {
		checkpoint = gatewayStartupDiagnosticCheckpointUnknown
	}
	value, ok := err.(*Error)
	if !ok || value == nil {
		return newGatewayStartupDiagnosticError(ctx, checkpoint)
	}
	return &Error{
		Code:                         value.Code,
		candidateMayBeLive:           value.candidateMayBeLive,
		currentAttestationCheckpoint: value.currentAttestationCheckpoint,
		startupCheckpoint:            checkpoint,
	}
}

// gatewayStartupDiagnosticErrorWithCheckpoint preserves an unknown upstream
// error unchanged. This is used where callers historically received the
// direct dependency error rather than a sanitized startup error.
func gatewayStartupDiagnosticErrorWithCheckpoint(err error,
	checkpoint gatewayStartupDiagnosticCheckpoint,
) error {
	value, ok := err.(*Error)
	if !ok || value == nil {
		return err
	}
	return gatewayStartupDiagnosticErrorFrom(nil, value, checkpoint)
}

func gatewayStartupDiagnosticCheckpointFromError(err error) gatewayStartupDiagnosticCheckpoint {
	value, ok := err.(*Error)
	if !ok || value == nil || !value.startupCheckpoint.valid() {
		return gatewayStartupDiagnosticCheckpointUnknown
	}
	return value.startupCheckpoint
}

// GatewayStartupDiagnosticCheckpoint returns trusted finite startup evidence.
// Wrapped, forged, invalid, and non-ingress errors are deliberately rejected.
func GatewayStartupDiagnosticCheckpoint(err error) string {
	return gatewayStartupDiagnosticCheckpointFromError(err).String()
}
