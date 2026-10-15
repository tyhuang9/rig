package main

import "github.com/hostd/hostd/internal/generatedingress"

// gatewayStartupDiagnosticCheckpoint is closed, audit-safe observation data
// for the startup inspection gate. It is never admission authority: the
// wrapped cause remains the sole result used by callers.
type gatewayStartupDiagnosticCheckpoint string

const (
	gatewayStartupDiagnosticStartupDependencies               gatewayStartupDiagnosticCheckpoint = "startup_dependencies"
	gatewayStartupDiagnosticNormalRebindDecision              gatewayStartupDiagnosticCheckpoint = "normal_rebind_decision"
	gatewayStartupDiagnosticPostPrepareFence                  gatewayStartupDiagnosticCheckpoint = "post_prepare_fence"
	gatewayStartupDiagnosticNativeRebindPresenceRecheck       gatewayStartupDiagnosticCheckpoint = "native_rebind_presence_recheck"
	gatewayStartupDiagnosticPreparedRuntimePrecondition       gatewayStartupDiagnosticCheckpoint = "prepared_runtime_precondition"
	gatewayStartupDiagnosticPreparedCurrentRecheck            gatewayStartupDiagnosticCheckpoint = "prepared_current_recheck"
	gatewayStartupDiagnosticOrdinaryFence                     gatewayStartupDiagnosticCheckpoint = "ordinary_fence"
	gatewayStartupDiagnosticStartupSnapshot                   gatewayStartupDiagnosticCheckpoint = "startup_snapshot"
	gatewayStartupDiagnosticDisabledRuntimeHistory            gatewayStartupDiagnosticCheckpoint = "disabled_runtime_history"
	gatewayStartupDiagnosticStartupManager                    gatewayStartupDiagnosticCheckpoint = "startup_manager"
	gatewayStartupDiagnosticUpgradeInspection                 gatewayStartupDiagnosticCheckpoint = "upgrade_inspection"
	gatewayStartupDiagnosticCurrentAuthoritySnapshotRecheck   gatewayStartupDiagnosticCheckpoint = "current_authority_snapshot_recheck"
	gatewayStartupDiagnosticV1WithLANHistory                  gatewayStartupDiagnosticCheckpoint = "v1_with_lan_history"
	gatewayStartupDiagnosticUpgradeRecoveryWithLANHistory     gatewayStartupDiagnosticCheckpoint = "upgrade_recovery_with_lan_history"
	gatewayStartupDiagnosticStartupDisposition                gatewayStartupDiagnosticCheckpoint = "startup_disposition"
	gatewayStartupDiagnosticLANRecoveryBatchPresence          gatewayStartupDiagnosticCheckpoint = "lan_recovery_batch_presence"
	gatewayStartupDiagnosticHistoricalDisableSuccessor        gatewayStartupDiagnosticCheckpoint = "historical_disable_successor"
	gatewayStartupDiagnosticLANAccessInspection               gatewayStartupDiagnosticCheckpoint = "lan_access_inspection"
	gatewayStartupDiagnosticLANRecoverySelection              gatewayStartupDiagnosticCheckpoint = "lan_recovery_selection"
	gatewayStartupDiagnosticUpgradeClaimsValidation           gatewayStartupDiagnosticCheckpoint = "upgrade_claims_validation"
	gatewayStartupDiagnosticUpgradeGatewayLock                gatewayStartupDiagnosticCheckpoint = "upgrade_gateway_lock"
	gatewayStartupDiagnosticCurrentSelectionPrecondition      gatewayStartupDiagnosticCheckpoint = "current_selection_precondition"
	gatewayStartupDiagnosticCurrentSQLFirstRead               gatewayStartupDiagnosticCheckpoint = "current_sql_first_read"
	gatewayStartupDiagnosticCurrentProtectedSelection         gatewayStartupDiagnosticCheckpoint = "current_protected_selection"
	gatewayStartupDiagnosticCurrentAbsenceProtectedPresence   gatewayStartupDiagnosticCheckpoint = "current_absence_protected_presence"
	gatewayStartupDiagnosticCurrentSQLStability               gatewayStartupDiagnosticCheckpoint = "current_sql_stability"
	gatewayStartupDiagnosticCurrentProtectedStability         gatewayStartupDiagnosticCheckpoint = "current_protected_stability"
	gatewayStartupDiagnosticUpgradeHistory                    gatewayStartupDiagnosticCheckpoint = "upgrade_history"
	gatewayStartupDiagnosticUpgradeSourceState                gatewayStartupDiagnosticCheckpoint = "upgrade_source_state"
	gatewayStartupDiagnosticUpgradeCurrentBinding             gatewayStartupDiagnosticCheckpoint = "upgrade_current_binding"
	gatewayStartupDiagnosticUpgradeClaimHistoryBinding        gatewayStartupDiagnosticCheckpoint = "upgrade_claim_history_binding"
	gatewayStartupDiagnosticUpgradePendingProof               gatewayStartupDiagnosticCheckpoint = "upgrade_pending_proof"
	gatewayStartupDiagnosticUpgradeLANBatchProof              gatewayStartupDiagnosticCheckpoint = "upgrade_lan_batch_proof"
	gatewayStartupDiagnosticUpgradeCommittedTopology          gatewayStartupDiagnosticCheckpoint = "upgrade_committed_topology"
	gatewayStartupDiagnosticUpgradeCommittedHostRoutes        gatewayStartupDiagnosticCheckpoint = "upgrade_committed_host_routes"
	gatewayStartupDiagnosticUpgradeCommittedSelectedInterface gatewayStartupDiagnosticCheckpoint = "upgrade_committed_selected_interface"
	gatewayStartupDiagnosticUpgradeTerminalReceiptProof       gatewayStartupDiagnosticCheckpoint = "upgrade_terminal_receipt_proof"
	gatewayStartupDiagnosticUpgradeNoArtifactProof            gatewayStartupDiagnosticCheckpoint = "upgrade_no_artifact_proof"
	gatewayStartupDiagnosticUpgradeDisposition                gatewayStartupDiagnosticCheckpoint = "upgrade_disposition"
	gatewayStartupDiagnosticUpgradeSourceStability            gatewayStartupDiagnosticCheckpoint = "upgrade_source_stability"
	gatewayStartupDiagnosticUpgradeHistoryStability           gatewayStartupDiagnosticCheckpoint = "upgrade_history_stability"
	gatewayStartupDiagnosticUpgradeCurrentStability           gatewayStartupDiagnosticCheckpoint = "upgrade_current_stability"
	gatewayStartupDiagnosticUpgradeGatewayUnlock              gatewayStartupDiagnosticCheckpoint = "upgrade_gateway_unlock"
	gatewayStartupDiagnosticLANClaimsValidation               gatewayStartupDiagnosticCheckpoint = "lan_claims_validation"
	gatewayStartupDiagnosticLANGatewayLock                    gatewayStartupDiagnosticCheckpoint = "lan_gateway_lock"
	gatewayStartupDiagnosticLANCurrentGatewaySelection        gatewayStartupDiagnosticCheckpoint = "lan_current_gateway_selection"
	gatewayStartupDiagnosticLANCommittedState                 gatewayStartupDiagnosticCheckpoint = "lan_committed_state"
	gatewayStartupDiagnosticLANStartupAuthority               gatewayStartupDiagnosticCheckpoint = "lan_startup_authority"
	gatewayStartupDiagnosticLANProtectedClaimBinding          gatewayStartupDiagnosticCheckpoint = "lan_protected_claim_binding"
	gatewayStartupDiagnosticLANPendingStateProof              gatewayStartupDiagnosticCheckpoint = "lan_pending_state_proof"
	gatewayStartupDiagnosticLANCommittedTopology              gatewayStartupDiagnosticCheckpoint = "lan_committed_topology"
	gatewayStartupDiagnosticLANCommittedPublications          gatewayStartupDiagnosticCheckpoint = "lan_committed_publications"
	gatewayStartupDiagnosticLANOrphanClaimRecovery            gatewayStartupDiagnosticCheckpoint = "lan_orphan_claim_recovery"
	gatewayStartupDiagnosticLANDisableBinding                 gatewayStartupDiagnosticCheckpoint = "lan_disable_binding"
	gatewayStartupDiagnosticLANDisableAbsenceProof            gatewayStartupDiagnosticCheckpoint = "lan_disable_absence_proof"
	gatewayStartupDiagnosticLANSelectedInterface              gatewayStartupDiagnosticCheckpoint = "lan_selected_interface"
	gatewayStartupDiagnosticLANProtectedStateStability        gatewayStartupDiagnosticCheckpoint = "lan_protected_state_stability"
	gatewayStartupDiagnosticLANAuthorityStability             gatewayStartupDiagnosticCheckpoint = "lan_authority_stability"
	gatewayStartupDiagnosticLANRecoveryCandidateSelection     gatewayStartupDiagnosticCheckpoint = "lan_recovery_candidate_selection"
	gatewayStartupDiagnosticLANGatewayUnlock                  gatewayStartupDiagnosticCheckpoint = "lan_gateway_unlock"
)

// gatewayStartupDiagnosticCheckpointFromString is the only parser for a
// checkpoint. Its closed switch is shared by the main logger and live stderr
// harness; values are not accepted by prefix, syntax, or caller assertion.
func gatewayStartupDiagnosticCheckpointFromString(value string) (gatewayStartupDiagnosticCheckpoint, bool) {
	checkpoint := gatewayStartupDiagnosticCheckpoint(value)
	switch checkpoint {
	case gatewayStartupDiagnosticStartupDependencies,
		gatewayStartupDiagnosticNormalRebindDecision,
		gatewayStartupDiagnosticPostPrepareFence,
		gatewayStartupDiagnosticNativeRebindPresenceRecheck,
		gatewayStartupDiagnosticPreparedRuntimePrecondition,
		gatewayStartupDiagnosticPreparedCurrentRecheck,
		gatewayStartupDiagnosticOrdinaryFence,
		gatewayStartupDiagnosticStartupSnapshot,
		gatewayStartupDiagnosticDisabledRuntimeHistory,
		gatewayStartupDiagnosticStartupManager,
		gatewayStartupDiagnosticUpgradeInspection,
		gatewayStartupDiagnosticCurrentAuthoritySnapshotRecheck,
		gatewayStartupDiagnosticV1WithLANHistory,
		gatewayStartupDiagnosticUpgradeRecoveryWithLANHistory,
		gatewayStartupDiagnosticStartupDisposition,
		gatewayStartupDiagnosticLANRecoveryBatchPresence,
		gatewayStartupDiagnosticHistoricalDisableSuccessor,
		gatewayStartupDiagnosticLANAccessInspection,
		gatewayStartupDiagnosticLANRecoverySelection,
		gatewayStartupDiagnosticUpgradeClaimsValidation,
		gatewayStartupDiagnosticUpgradeGatewayLock,
		gatewayStartupDiagnosticCurrentSelectionPrecondition,
		gatewayStartupDiagnosticCurrentSQLFirstRead,
		gatewayStartupDiagnosticCurrentProtectedSelection,
		gatewayStartupDiagnosticCurrentAbsenceProtectedPresence,
		gatewayStartupDiagnosticCurrentSQLStability,
		gatewayStartupDiagnosticCurrentProtectedStability,
		gatewayStartupDiagnosticUpgradeHistory,
		gatewayStartupDiagnosticUpgradeSourceState,
		gatewayStartupDiagnosticUpgradeCurrentBinding,
		gatewayStartupDiagnosticUpgradeClaimHistoryBinding,
		gatewayStartupDiagnosticUpgradePendingProof,
		gatewayStartupDiagnosticUpgradeLANBatchProof,
		gatewayStartupDiagnosticUpgradeCommittedTopology,
		gatewayStartupDiagnosticUpgradeCommittedHostRoutes,
		gatewayStartupDiagnosticUpgradeCommittedSelectedInterface,
		gatewayStartupDiagnosticUpgradeTerminalReceiptProof,
		gatewayStartupDiagnosticUpgradeNoArtifactProof,
		gatewayStartupDiagnosticUpgradeDisposition,
		gatewayStartupDiagnosticUpgradeSourceStability,
		gatewayStartupDiagnosticUpgradeHistoryStability,
		gatewayStartupDiagnosticUpgradeCurrentStability,
		gatewayStartupDiagnosticUpgradeGatewayUnlock,
		gatewayStartupDiagnosticLANClaimsValidation,
		gatewayStartupDiagnosticLANGatewayLock,
		gatewayStartupDiagnosticLANCurrentGatewaySelection,
		gatewayStartupDiagnosticLANCommittedState,
		gatewayStartupDiagnosticLANStartupAuthority,
		gatewayStartupDiagnosticLANProtectedClaimBinding,
		gatewayStartupDiagnosticLANPendingStateProof,
		gatewayStartupDiagnosticLANCommittedTopology,
		gatewayStartupDiagnosticLANCommittedPublications,
		gatewayStartupDiagnosticLANOrphanClaimRecovery,
		gatewayStartupDiagnosticLANDisableBinding,
		gatewayStartupDiagnosticLANDisableAbsenceProof,
		gatewayStartupDiagnosticLANSelectedInterface,
		gatewayStartupDiagnosticLANProtectedStateStability,
		gatewayStartupDiagnosticLANAuthorityStability,
		gatewayStartupDiagnosticLANRecoveryCandidateSelection,
		gatewayStartupDiagnosticLANGatewayUnlock:
		return checkpoint, true
	default:
		return "", false
	}
}

// gatewayStartupDiagnosticError keeps the original cause observable to normal
// callers while keeping the diagnostic's Error string fixed and private.
type gatewayStartupDiagnosticError struct {
	checkpoint gatewayStartupDiagnosticCheckpoint
	cause      error
}

func (e *gatewayStartupDiagnosticError) Error() string {
	return "gateway startup diagnostic failed"
}

func (e *gatewayStartupDiagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func gatewayStartupDiagnosticWrap(checkpoint gatewayStartupDiagnosticCheckpoint, cause error) error {
	if cause == nil {
		return nil
	}
	if existing, ok := cause.(*gatewayStartupDiagnosticError); ok && existing != nil {
		return cause
	}
	return &gatewayStartupDiagnosticError{checkpoint: checkpoint, cause: cause}
}

// gatewayStartupDiagnosticWrapIngress reads ingress metadata from the direct
// method return before the caller wraps that return with its existing context.
// The wrapper retains that contextual cause unchanged.
func gatewayStartupDiagnosticWrapIngress(checkpoint gatewayStartupDiagnosticCheckpoint, cause, ingressErr error) error {
	if cause == nil {
		return nil
	}
	if inner, ok := gatewayStartupDiagnosticCheckpointFromString(
		generatedingress.GatewayStartupDiagnosticCheckpoint(ingressErr)); ok {
		checkpoint = inner
	}
	return gatewayStartupDiagnosticWrap(checkpoint, cause)
}

// gatewayStartupDiagnosticCheckpointForError accepts only the private wrapper
// assembled at this gate. It deliberately does not walk error chains, inspect
// text, or trust implementable external types.
func gatewayStartupDiagnosticCheckpointForError(err error) string {
	wrapped, ok := err.(*gatewayStartupDiagnosticError)
	if !ok || wrapped == nil {
		return "unknown"
	}
	checkpoint, ok := gatewayStartupDiagnosticCheckpointFromString(string(wrapped.checkpoint))
	if !ok {
		return "unknown"
	}
	return string(checkpoint)
}
