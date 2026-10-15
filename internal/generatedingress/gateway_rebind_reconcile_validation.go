package generatedingress

import (
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// ValidateGatewayRebindOperationInspection checks only the public projection's
// internal consistency. It does not establish that any SQL, protected, or
// physical read occurred and grants no recovery authority.
func ValidateGatewayRebindOperationInspection(value GatewayRebindOperationInspection,
	operationID string,
) error {
	invalid := func() error { return appaccess.ErrInvalidStoredState }
	if value.Version != GatewayRebindOperationInspectionVersion ||
		!validCanonicalUUID(operationID) || value.OperationID != operationID {
		return invalid()
	}
	if value.Availability != GatewayRebindOperationAvailable {
		wantProof := GatewayRebindProofUnknown
		if value.Availability == GatewayRebindOperationUnavailable {
			wantProof = GatewayRebindProofUnavailable
		} else if value.Availability != GatewayRebindOperationValidatedAbsent {
			return invalid()
		}
		if value.Operation != (GatewayRebindOperationStatus{}) || value.Active ||
			value.DatabaseCommittedEvent != nil || value.Protected != nil || value.ProtectedPhase != "" ||
			value.HistoricalTerminal || value.SelectedCurrentAuthority != nil ||
			value.TerminalProof != wantProof || value.TerminalProofDigest != "" ||
			value.CurrentProof != wantProof || value.CurrentAttestationDigest != "" ||
			value.OperationFence != GatewayRebindOperationFenceUnavailable || value.RecoveryPolicy != "" ||
			len(value.AllowedActions) != 0 {
			return invalid()
		}
		return nil
	}

	operation := value.Operation
	if operation.OperationID != operationID || operation.StateSequence < 1 ||
		operation.DatabaseCommitObserved != (value.DatabaseCommittedEvent != nil) {
		return invalid()
	}
	if value.DatabaseCommittedEvent != nil &&
		(value.DatabaseCommittedEvent.OperationID != operationID ||
			value.DatabaseCommittedEvent.State != appaccess.GatewayRebindDatabaseCommitted ||
			value.DatabaseCommittedEvent.Sequence <= 0 ||
			value.DatabaseCommittedEvent.Sequence > operation.StateSequence) {
		return invalid()
	}
	active := operation.StoredState == appaccess.GatewayRebindPrepared ||
		operation.StoredState == appaccess.GatewayRebindSuccessorReady ||
		operation.StoredState == appaccess.GatewayRebindDatabaseCommitted ||
		operation.StoredState == appaccess.GatewayRebindUnresolved
	terminal := operation.StoredState == appaccess.GatewayRebindCommitted ||
		operation.StoredState == appaccess.GatewayRebindRolledBack
	if value.Active != active || value.HistoricalTerminal != terminal || active == terminal {
		return invalid()
	}
	switch operation.StoredState {
	case appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady,
		appaccess.GatewayRebindRolledBack:
		if operation.DatabaseCommitObserved {
			return invalid()
		}
	case appaccess.GatewayRebindDatabaseCommitted, appaccess.GatewayRebindCommitted:
		if !operation.DatabaseCommitObserved {
			return invalid()
		}
	case appaccess.GatewayRebindUnresolved:
		// Unresolved deliberately preserves either side of the durable database
		// commit boundary. The exact retained event above distinguishes them.
	default:
		return invalid()
	}
	if active {
		if operation.TerminalDisposition != appaccess.GatewayRebindDispositionNone ||
			operation.TerminalReceiptDigest != "" ||
			value.OperationFence != GatewayRebindOperationFenceHeld {
			return invalid()
		}
	} else {
		expected := appaccess.GatewayRebindDispositionCommit
		if operation.StoredState == appaccess.GatewayRebindRolledBack {
			expected = appaccess.GatewayRebindDispositionAbort
		}
		if operation.TerminalDisposition != expected || !validSHA256(operation.TerminalReceiptDigest) ||
			value.OperationFence != GatewayRebindOperationFenceReleased {
			return invalid()
		}
	}

	if value.Protected == nil {
		if value.ProtectedPhase != "" {
			return invalid()
		}
	} else if !validGatewayRebindOperationProtectedProjection(*value.Protected,
		value.ProtectedPhase, operationID) {
		return invalid()
	}
	if value.SelectedCurrentAuthority != nil &&
		!validGatewayRebindOperationCurrentAuthority(*value.SelectedCurrentAuthority) {
		return invalid()
	}
	if !validGatewayRebindOperationProof(value.TerminalProof, value.TerminalProofDigest) ||
		!validGatewayRebindOperationProof(value.CurrentProof, value.CurrentAttestationDigest) ||
		(value.TerminalProof == GatewayRebindProofVerified && !terminal) ||
		(value.CurrentProof == GatewayRebindProofVerified && value.SelectedCurrentAuthority == nil) {
		return invalid()
	}
	if value.RecoveryPolicy != "" {
		switch value.RecoveryPolicy {
		case appaccess.GatewayRebindRecoveryEvidenceDecides,
			appaccess.GatewayRebindRecoveryForwardOnly,
			appaccess.GatewayRebindRecoveryRollbackOnly:
		default:
			return invalid()
		}
	}
	if len(value.AllowedActions) > 1 || len(value.AllowedActions) == 1 &&
		(value.AllowedActions[0] != GatewayRebindRecoveryActionReconcile || !active ||
			value.Protected == nil || value.SelectedCurrentAuthority == nil ||
			value.CurrentProof != GatewayRebindProofVerified || value.RecoveryPolicy == "") {
		return invalid()
	}
	return nil
}

// ValidateGatewayRebindRecoveryReview checks that an executable review binds
// one canonical actor-specific appaccess review to the displayed status. A
// non-executable review must carry no consent digest.
func ValidateGatewayRebindRecoveryReview(value GatewayRebindRecoveryReview,
	operationID, actorID string,
) error {
	if value.Version != GatewayRebindRecoveryReviewVersion || !validCanonicalUUID(actorID) ||
		ValidateGatewayRebindOperationInspection(value.Status, operationID) != nil {
		return appaccess.ErrInvalidStoredState
	}
	if len(value.Status.AllowedActions) == 0 {
		if !reflect.DeepEqual(value.AuthorizationReview, appaccess.GatewayRebindReconcileReview{}) {
			return appaccess.ErrInvalidStoredState
		}
		return nil
	}
	if len(value.Status.AllowedActions) != 1 ||
		appaccess.ValidateGatewayRebindReconcileReview(value.AuthorizationReview, actorID) != nil ||
		!gatewayRebindRecoveryStatusMatchesReview(value.Status, value.AuthorizationReview) {
		return appaccess.ErrInvalidStoredState
	}
	return nil
}

// ValidateGatewayRebindReconcileResponse checks the response, submitted
// digests, actor, and audited authorization as one projection. The audit is
// still neither replayable nor effect authority.
func ValidateGatewayRebindReconcileResponse(value GatewayRebindReconcileResponse,
	request GatewayRebindReconcileRequest, actorID string,
) error {
	if value.Version != GatewayRebindReconcileVersion || request.Version != GatewayRebindReconcileVersion ||
		!validCanonicalUUID(actorID) || !validCanonicalUUID(request.OperationID) ||
		!validSHA256(request.RecoveryDigest) || !validSHA256(request.RecoveryApprovalDigest) ||
		ValidateGatewayRebindOperationInspection(value.Status, request.OperationID) != nil ||
		value.Authorization.ActorID != actorID || value.Authorization.AuditEventID <= 0 ||
		value.Authorization.AuditedAt.IsZero() ||
		value.Authorization.Review.RecoveryDigest != request.RecoveryDigest ||
		value.Authorization.Review.RecoveryApprovalDigest != request.RecoveryApprovalDigest ||
		value.Authorization.Review.Snapshot.Claim.Spec.OperationID != request.OperationID ||
		appaccess.ValidateGatewayRebindReconcileReview(value.Authorization.Review, actorID) != nil {
		return appaccess.ErrInvalidStoredState
	}
	status := value.Status
	if status.Availability != GatewayRebindOperationAvailable || status.Active ||
		!status.HistoricalTerminal || status.OperationFence != GatewayRebindOperationFenceReleased ||
		status.TerminalProof != GatewayRebindProofVerified ||
		status.CurrentProof != GatewayRebindProofVerified || status.SelectedCurrentAuthority == nil ||
		status.RecoveryPolicy != "" || len(status.AllowedActions) != 0 || status.Protected == nil {
		return appaccess.ErrInvalidStoredState
	}
	protected := *status.Protected
	if protected.FixedDisposition != status.Operation.TerminalDisposition ||
		protected.TerminalReceiptDigest != status.Operation.TerminalReceiptDigest ||
		value.Authorization.Review.Snapshot.Policy == "" {
		return appaccess.ErrInvalidStoredState
	}
	switch value.Authorization.Review.Snapshot.Policy {
	case appaccess.GatewayRebindRecoveryForwardOnly:
		if status.Operation.StoredState != appaccess.GatewayRebindCommitted ||
			!status.Operation.DatabaseCommitObserved ||
			status.Operation.TerminalDisposition != appaccess.GatewayRebindDispositionCommit {
			return appaccess.ErrInvalidStoredState
		}
	case appaccess.GatewayRebindRecoveryRollbackOnly:
		if status.Operation.StoredState != appaccess.GatewayRebindRolledBack ||
			status.Operation.DatabaseCommitObserved ||
			status.Operation.TerminalDisposition != appaccess.GatewayRebindDispositionAbort {
			return appaccess.ErrInvalidStoredState
		}
	case appaccess.GatewayRebindRecoveryEvidenceDecides:
		if status.Operation.StoredState == appaccess.GatewayRebindCommitted {
			if !status.Operation.DatabaseCommitObserved ||
				status.Operation.TerminalDisposition != appaccess.GatewayRebindDispositionCommit {
				return appaccess.ErrInvalidStoredState
			}
		} else if status.Operation.StoredState == appaccess.GatewayRebindRolledBack {
			if status.Operation.DatabaseCommitObserved ||
				status.Operation.TerminalDisposition != appaccess.GatewayRebindDispositionAbort {
				return appaccess.ErrInvalidStoredState
			}
		} else {
			return appaccess.ErrInvalidStoredState
		}
	default:
		return appaccess.ErrInvalidStoredState
	}
	selected := *status.SelectedCurrentAuthority
	if status.Operation.TerminalDisposition == appaccess.GatewayRebindDispositionCommit {
		if selected.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
			selected.OperationID != request.OperationID ||
			selected.TerminalReceiptDigest != status.Operation.TerminalReceiptDigest {
			return appaccess.ErrInvalidStoredState
		}
	} else {
		current := value.Authorization.Review.Snapshot.CurrentSource
		if current == nil || selected != *current {
			return appaccess.ErrInvalidStoredState
		}
	}
	return nil
}

func validGatewayRebindOperationProtectedProjection(value appaccess.GatewayRebindProtectedRecoveryEvidence,
	phase, operationID string,
) bool {
	if value.OperationID != operationID || value.Generation == 0 || !validSHA256(value.ScanDigest) {
		return false
	}
	switch value.Presence {
	case appaccess.GatewayRebindProtectedEvidenceAbsent:
		return phase == "" && value.IntentDigest == "" && value.RecordSequence == 0 &&
			value.RecordDigest == "" && value.FixedDisposition == appaccess.GatewayRebindDispositionNone &&
			value.TerminalReceiptDigest == ""
	case appaccess.GatewayRebindProtectedEvidenceCheckpointTerminal:
		return phase == "" && value.IntentDigest == "" && value.RecordSequence == 0 &&
			value.RecordDigest == "" && value.FixedDisposition == appaccess.GatewayRebindDispositionAbort &&
			validSHA256(value.TerminalReceiptDigest)
	case appaccess.GatewayRebindProtectedEvidencePresent:
		if !validSHA256(value.IntentDigest) || value.RecordSequence == 0 || !validSHA256(value.RecordDigest) ||
			!validGatewayRebindOperationProtectedPhase(phase) {
			return false
		}
		return validGatewayRebindOperationProtectedDisposition(value, gatewayRebindProgressPhase(phase))
	default:
		return false
	}
}

func validGatewayRebindOperationProtectedDisposition(value appaccess.GatewayRebindProtectedRecoveryEvidence,
	phase gatewayRebindProgressPhase,
) bool {
	switch value.FixedDisposition {
	case appaccess.GatewayRebindDispositionNone:
		return value.TerminalReceiptDigest == "" &&
			phase != gatewayRebindProgressHandoverCommitted &&
			phase != gatewayRebindProgressRollbackIntent &&
			phase != gatewayRebindProgressHandoverRolledBack
	case appaccess.GatewayRebindDispositionCommit:
		return phase == gatewayRebindProgressHandoverCommitted &&
			(value.TerminalReceiptDigest == "" || validSHA256(value.TerminalReceiptDigest))
	case appaccess.GatewayRebindDispositionAbort:
		return (phase == gatewayRebindProgressRollbackIntent && value.TerminalReceiptDigest == "") ||
			(phase == gatewayRebindProgressHandoverRolledBack &&
				(value.TerminalReceiptDigest == "" || validSHA256(value.TerminalReceiptDigest)))
	default:
		return false
	}
}

func validGatewayRebindOperationProtectedPhase(value string) bool {
	switch gatewayRebindProgressPhase(value) {
	case gatewayRebindProgressSuccessorIntent, gatewayRebindProgressStageIntent,
		gatewayRebindProgressStageConfigIntent, gatewayRebindProgressStageConfigCopied,
		gatewayRebindProgressStageStartIntent, gatewayRebindProgressStageServing,
		gatewayRebindProgressFinalConfigIntent, gatewayRebindProgressFinalConfigCopied,
		gatewayRebindProgressFinalHandoverIntent, gatewayRebindProgressFinalContainerBound,
		gatewayRebindProgressCutoverIntent, gatewayRebindProgressSuccessorServing,
		gatewayRebindProgressHandoverCommitted, gatewayRebindProgressRollbackIntent,
		gatewayRebindProgressHandoverRolledBack:
		return true
	default:
		return false
	}
}

func validGatewayRebindOperationCurrentAuthority(value appaccess.GatewayCurrentAuthorityRef) bool {
	if !validCanonicalUUID(value.OperationID) || !validCanonicalUUID(value.ProfileRevisionID) ||
		value.ProfileRevisionNumber <= 0 || !validSHA256(value.ProfileSpecDigest) {
		return false
	}
	switch value.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return value.TerminalReceiptDigest == ""
	case appaccess.GatewayRebindSourceGatewayRebind:
		return validSHA256(value.TerminalReceiptDigest)
	default:
		return false
	}
}

func validGatewayRebindOperationProof(status GatewayRebindProofStatus, digest string) bool {
	switch status {
	case GatewayRebindProofVerified:
		return validSHA256(digest)
	case GatewayRebindProofUnknown, GatewayRebindProofUnavailable:
		return digest == ""
	default:
		return false
	}
}

func gatewayRebindRecoveryStatusMatchesReview(status GatewayRebindOperationInspection,
	review appaccess.GatewayRebindReconcileReview,
) bool {
	snapshot := review.Snapshot
	return status.Availability == GatewayRebindOperationAvailable && status.Active &&
		status.Operation.OperationID == snapshot.Claim.Spec.OperationID &&
		status.Operation.StoredState == snapshot.Claim.State &&
		status.Operation.StateSequence == snapshot.Claim.StateSequence &&
		status.Operation.DatabaseCommitObserved == (snapshot.DatabaseCommittedEvent != nil) &&
		reflect.DeepEqual(status.DatabaseCommittedEvent, snapshot.DatabaseCommittedEvent) &&
		status.Protected != nil && reflect.DeepEqual(*status.Protected, snapshot.Trusted.Protected) &&
		status.SelectedCurrentAuthority != nil && snapshot.CurrentSource != nil &&
		*status.SelectedCurrentAuthority == *snapshot.CurrentSource &&
		status.RecoveryPolicy == snapshot.Policy &&
		status.OperationFence == GatewayRebindOperationFenceHeld
}
