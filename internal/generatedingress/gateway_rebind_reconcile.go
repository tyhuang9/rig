package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayRebindReconcileTerminalProofPurpose = "hostd/generated-ingress/rebind/reconcile-terminal-proof/v1"

type gatewayRebindRecoveryCurrentProofReader func(context.Context, GatewayRebindRecoveryReadRepository,
	appaccess.GatewayRebindRecoverySnapshot, func(context.Context) error) (gatewayRebindCommitCurrentProof, error)

// ReconcileGatewayRebind executes one freshly reviewed, actor-bound recovery
// request. It creates no claim and accepts no caller-selected direction. The
// audit records consent only; every effect remains authorized by fresh SQL,
// protected-state, current-state, ownership, and request-session boundaries.
func (m *Manager) ReconcileGatewayRebind(ctx context.Context,
	repository GatewayRebindRecoveryRepository, request GatewayRebindReconcileRequest,
	actorID string, liveSession func(context.Context) error,
) (GatewayRebindReconcileResponse, error) {
	driver, _ := newGatewayRebindRuntimeDrivers(m)
	return m.reconcileGatewayRebindWithRuntime(ctx, repository, request, actorID, liveSession,
		driver, m.readGatewayRebindRecoveryCurrentProofLocked)
}

func (m *Manager) reconcileGatewayRebindWithRuntime(ctx context.Context,
	repository GatewayRebindRecoveryRepository, request GatewayRebindReconcileRequest,
	actorID string, liveSession func(context.Context) error,
	driver managerGatewayRebindCrossStoreDriver, readCurrent gatewayRebindRecoveryCurrentProofReader,
) (GatewayRebindReconcileResponse, error) {
	if m == nil || ctx == nil || repository == nil || liveSession == nil ||
		driver.manager != m || readCurrent == nil ||
		request.Version != GatewayRebindReconcileVersion ||
		!validCanonicalUUID(request.OperationID) || !validCanonicalUUID(actorID) ||
		!validSHA256(request.RecoveryDigest) || !validSHA256(request.RecoveryApprovalDigest) {
		return GatewayRebindReconcileResponse{}, &Error{Code: DiagnosticValidationFailed}
	}
	typedRepository, ok := any(repository).(gatewayRebindTypedBoundaryRepository)
	if !ok {
		return GatewayRebindReconcileResponse{}, &Error{Code: DiagnosticValidationFailed}
	}
	leaveDomain, err := m.enterGeneratedDomainTypedMutation(ctx)
	if err != nil {
		return GatewayRebindReconcileResponse{}, err
	}
	defer leaveDomain()

	var response GatewayRebindReconcileResponse
	driver = driver.withRecoveryRepository(typedRepository)
	err = m.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
		review, err := m.inspectGatewayRebindRecoveryReviewWithCurrentProofLocked(ctx, repository,
			request.OperationID, actorID, liveSession, readCurrent)
		if err != nil {
			return false, err
		}
		if len(review.Status.AllowedActions) != 1 ||
			review.Status.AllowedActions[0] != GatewayRebindRecoveryActionReconcile ||
			review.AuthorizationReview.RecoveryDigest != request.RecoveryDigest {
			return false, appaccess.ErrConflict
		}
		if review.AuthorizationReview.RecoveryApprovalDigest != request.RecoveryApprovalDigest {
			return false, appaccess.ErrApprovalRequired
		}

		authorization, err := repository.AuthorizeAndAuditGatewayRebindReconcile(ctx,
			appaccess.GatewayRebindReconcileAuthorizationInput{
				OperationID: request.OperationID, ActorID: actorID,
				RecoveryDigest:         request.RecoveryDigest,
				RecoveryApprovalDigest: request.RecoveryApprovalDigest,
				Trusted:                review.AuthorizationReview.Snapshot.Trusted,
			})
		if err != nil {
			return false, err
		}
		if !gatewayRebindReconcileAuthorizationMatches(authorization,
			review.AuthorizationReview, actorID) {
			return false, gatewayRebindProposalError(ctx)
		}
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return false, err
		}

		fresh, err := m.inspectGatewayRebindRecoveryReviewWithCurrentProofLocked(ctx, repository,
			request.OperationID, actorID, liveSession, readCurrent)
		if err != nil || !reflect.DeepEqual(review, fresh) {
			if err != nil {
				return false, err
			}
			return false, appaccess.ErrConflict
		}
		policy := fresh.AuthorizationReview.Snapshot.Policy
		driver.forwardSessionGuard = liveSession
		if policy == appaccess.GatewayRebindRecoveryRollbackOnly {
			driver.rollbackStartGuard = liveSession
		}
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return false, err
		}
		recovered, err := m.recoverGatewayRebindStartupExpectedLocked(ctx, repository, driver,
			gatewayRebindRequestedRecovery{OperationID: request.OperationID, Policy: policy})
		terminalSQL := m.gatewayRebindCommitBarrierLatch().Load()
		if err != nil {
			return terminalSQL, err
		}

		status, err := m.reconcileGatewayRebindTerminalStatusLocked(ctx, repository,
			request.OperationID, liveSession, recovered, readCurrent)
		if err != nil {
			return terminalSQL, err
		}
		response = GatewayRebindReconcileResponse{
			Version: GatewayRebindReconcileVersion, Status: status, Authorization: authorization,
		}
		if ValidateGatewayRebindReconcileResponse(response, request, actorID) != nil {
			return terminalSQL, gatewayRebindProposalError(ctx)
		}
		return terminalSQL, nil
	})
	if err != nil {
		return GatewayRebindReconcileResponse{}, err
	}
	return response, nil
}

func gatewayRebindReconcileAuthorizationMatches(value appaccess.GatewayRebindReconcileAuthorization,
	review appaccess.GatewayRebindReconcileReview, actorID string,
) bool {
	return value.ActorID == actorID && value.AuditEventID > 0 && !value.AuditedAt.IsZero() &&
		reflect.DeepEqual(value.Review, review) &&
		appaccess.ValidateGatewayRebindReconcileReview(value.Review, actorID) == nil
}

func (m *Manager) reconcileGatewayRebindTerminalStatusLocked(ctx context.Context,
	repository GatewayRebindRecoveryRepository, operationID string,
	liveSession func(context.Context) error, recovered GatewayRebindStartupRecoveryResult,
	readCurrent gatewayRebindRecoveryCurrentProofReader,
) (GatewayRebindOperationInspection, error) {
	invalid := func() (GatewayRebindOperationInspection, error) {
		return GatewayRebindOperationInspection{}, gatewayRebindProposalError(ctx)
	}
	projection, err := m.projectGatewayRebindRecoveryLocked(ctx, repository, operationID)
	if err != nil {
		return invalid()
	}
	status := projection.status
	if !recovered.RebindHistoryPresent || !recovered.Recovered ||
		recovered.ActiveOperationID != operationID || !recovered.FenceReleased ||
		recovered.SelectedCurrentAuthority == nil || status.Availability != GatewayRebindOperationAvailable ||
		status.Active || !status.HistoricalTerminal || status.OperationFence != GatewayRebindOperationFenceReleased ||
		status.Operation.StoredState != recovered.FinalActivePhase ||
		status.Operation.TerminalDisposition != recovered.Disposition ||
		status.Operation.TerminalReceiptDigest != recovered.TerminalReceiptDigest ||
		status.SelectedCurrentAuthority == nil ||
		*status.SelectedCurrentAuthority != *recovered.SelectedCurrentAuthority ||
		status.Protected == nil || status.RecoveryPolicy != "" || len(status.AllowedActions) != 0 {
		return invalid()
	}
	if readCurrent == nil {
		return invalid()
	}
	current, err := readCurrent(ctx, repository, projection.sql, liveSession)
	if err != nil || !validSHA256(current.Digest) ||
		gatewayCurrentAuthority(current.Selection.Lineage) != *status.SelectedCurrentAuthority {
		return invalid()
	}
	status.CurrentProof = GatewayRebindProofVerified
	status.CurrentAttestationDigest = current.Digest
	terminalDigest, err := gatewayRebindReconcileTerminalProofDigest(status, recovered, current.Digest)
	if err != nil || !validSHA256(terminalDigest) {
		return invalid()
	}
	status.TerminalProof = GatewayRebindProofVerified
	status.TerminalProofDigest = terminalDigest
	if ValidateGatewayRebindOperationInspection(status, operationID) != nil {
		return invalid()
	}
	return status, nil
}

func gatewayRebindReconcileTerminalProofDigest(status GatewayRebindOperationInspection,
	recovered GatewayRebindStartupRecoveryResult, currentProofDigest string,
) (string, error) {
	return canonicalDigest(struct {
		Version                  int                                        `json:"version"`
		Purpose                  string                                     `json:"purpose"`
		Operation                GatewayRebindOperationStatus               `json:"operation"`
		Disposition              appaccess.GatewayRebindTerminalDisposition `json:"disposition"`
		TerminalReceiptDigest    string                                     `json:"terminalReceiptDigest"`
		SelectedCurrentAuthority appaccess.GatewayCurrentAuthorityRef       `json:"selectedCurrentAuthority"`
		CurrentProofDigest       string                                     `json:"currentProofDigest"`
	}{
		Version: 1, Purpose: gatewayRebindReconcileTerminalProofPurpose,
		Operation: status.Operation, Disposition: recovered.Disposition,
		TerminalReceiptDigest:    recovered.TerminalReceiptDigest,
		SelectedCurrentAuthority: *recovered.SelectedCurrentAuthority,
		CurrentProofDigest:       currentProofDigest,
	})
}
