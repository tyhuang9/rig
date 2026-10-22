package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindCommitRepository is the complete SQL capability boundary for
// the dormant controller commit facade. Route and factory activation remain
// outside this checkpoint.
type GatewayRebindCommitRepository interface {
	GatewayRebindProposalRepository
	GatewayCurrentServingStartupRepository
	ClaimGatewayRebindV2(context.Context, appaccess.GatewayRebindPreclaimProposalV2) (
		appaccess.GatewayRebindClaimV2, bool, error)
	ApplyGatewayRebindTransition(context.Context, appaccess.GatewayRebindTransitionProof) (
		appaccess.GatewayRebindTransitionCommand, error)
	CheckGatewayRebindFence(context.Context) error
}

type gatewayRebindCommitCurrentProofReader func(context.Context, GatewayRebindCommitRepository,
	appaccess.GatewayRebindRecoverySnapshot, func(context.Context) error) (gatewayRebindCommitCurrentProof, error)

type gatewayRebindCommitTerminalPhysicalDriver interface {
	gatewayCurrentPredecessorRetirementDriver
	gatewayRebindCurrentAttestationDriver
}

// CommitGatewayRebind is the request-bound writer facade. It owns one effects
// lease, Manager mutex and gateway OS lock for classification, strict
// admission, physical reconciliation and terminal proof. Existing operations
// are classified read-only and never re-enter admission.
func (m *Manager) CommitGatewayRebind(ctx context.Context, repository GatewayRebindCommitRepository,
	request GatewayRebindCommitRequest, actorID string, liveSession func(context.Context) error,
) (GatewayRebindCommitResponse, error) {
	if m == nil || ctx == nil || repository == nil || liveSession == nil ||
		request.Version != gatewayRebindCommitReviewVersion || !validCanonicalUUID(actorID) ||
		!validCanonicalUUID(request.Proposal.OperationID) {
		return GatewayRebindCommitResponse{}, &Error{Code: DiagnosticValidationFailed}
	}
	leaveDomain, err := m.enterGeneratedDomainTypedMutation(ctx)
	if err != nil {
		return GatewayRebindCommitResponse{}, err
	}
	defer leaveDomain()
	var response GatewayRebindCommitResponse
	driver, _ := newGatewayRebindRuntimeDrivers(m)
	driver.forwardSessionGuard = liveSession
	err = m.runGatewayRebindCommitLocked(ctx, func() (bool, error) {
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return false, err
		}
		snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
		if err != nil {
			return false, gatewayRebindProposalError(ctx)
		}
		replay, found, err := readGatewayRebindCommitReplay(ctx, snapshot, request, actorID)
		if err != nil {
			return false, err
		}
		if found {
			if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
				return false, err
			}
			if replay.Response.RecoveryRequired {
				response = replay.Response
				return false, nil
			}
			response, err = m.readGatewayRebindTerminalResponseLocked(ctx, repository,
				request, actorID, liveSession, driver, false)
			return false, err
		}
		if snapshot.Active != nil {
			return false, appaccess.ErrGatewayRebindActive
		}

		inspection, err := m.inspectGatewayRebindProposalLocked(ctx, repository, request.Proposal)
		if err != nil {
			return false, err
		}
		review, err := newGatewayRebindCommitReview(inspection)
		if err != nil || !gatewayRebindCommitRequestMatchesReview(request, review) {
			return false, appaccess.ErrConflict
		}
		input := gatewayRebindCommitInput{Inspection: inspection,
			RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
				SpecDigest: review.RebindApprovalDigest, ActorID: actorID},
			ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
				SpecDigest: review.ConfigureApprovalDigest, ActorID: actorID},
		}
		strict, claimErr := m.claimGatewayRebindStrictLocked(ctx, repository, input,
			m.readGatewayRebindAdmissionPhysicalLocked, liveSession)
		if claimErr != nil {
			return m.finishGatewayRebindStrictAdmissionFailureLocked(ctx, repository, driver,
				request, actorID, liveSession, strict, claimErr, &response)
		}
		if !strict.accepted.created {
			return m.finishGatewayRebindCreatedFalseLocked(ctx, repository, driver, request, actorID,
				liveSession, &response)
		}

		prepared, sessionPassed, prepareErr := m.continueGatewayRebindStrictAdmissionLocked(ctx,
			repository, input, strict, m.readGatewayRebindAdmissionPhysicalLocked, liveSession)
		if prepareErr != nil {
			if !sessionPassed || ctx.Err() != nil {
				return false, prepareErr
			}
			abort, handled, abortErr := m.abortAcceptedGatewayRebindPreparedWithoutIntentLocked(ctx,
				repository, driver, strict, liveSession)
			terminalSQL := m.gatewayRebindCommitBarrierLatch().Load()
			if abortErr != nil {
				return terminalSQL, abortErr
			}
			if !handled {
				return terminalSQL, prepareErr
			}
			_ = abort
			response, abortErr = m.readGatewayRebindTerminalResponseLocked(ctx, repository,
				request, actorID, liveSession, driver, true)
			return terminalSQL, abortErr
		}
		_, terminalSQL, commitErr := m.continueGatewayRebindPreparedLocked(ctx, repository, prepared, driver)
		if commitErr != nil {
			return terminalSQL, commitErr
		}
		response, commitErr = m.readGatewayRebindTerminalResponseLocked(ctx, repository,
			request, actorID, liveSession, driver, true)
		return terminalSQL, commitErr
	})
	if err != nil {
		return GatewayRebindCommitResponse{}, err
	}
	return response, nil
}

func gatewayRebindCommitRequestMatchesReview(request GatewayRebindCommitRequest,
	review GatewayRebindCommitReview,
) bool {
	return request.Version == review.Version && request.ProposalDigest == review.ProposalDigest &&
		request.ConfigureApprovalDigest == review.ConfigureApprovalDigest &&
		request.RebindApprovalDigest == review.RebindApprovalDigest
}

func (m *Manager) finishGatewayRebindCreatedFalseLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, driver managerGatewayRebindCrossStoreDriver,
	request GatewayRebindCommitRequest, actorID string,
	liveSession func(context.Context) error, response *GatewayRebindCommitResponse,
) (bool, error) {
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return false, gatewayRebindProposalError(ctx)
	}
	replay, found, err := readGatewayRebindCommitReplay(ctx, snapshot, request, actorID)
	if err != nil {
		return false, err
	}
	if !found || response == nil {
		return false, gatewayRebindProposalError(ctx)
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return false, err
	}
	if replay.Response.RecoveryRequired {
		*response = replay.Response
		return false, nil
	}
	terminal, err := m.readGatewayRebindTerminalResponseLocked(ctx, repository,
		request, actorID, liveSession, driver, false)
	if err == nil {
		*response = terminal
	}
	return false, err
}

func (m *Manager) finishGatewayRebindStrictAdmissionFailureLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, driver managerGatewayRebindCrossStoreDriver,
	request GatewayRebindCommitRequest, actorID string, liveSession func(context.Context) error,
	strict gatewayRebindStrictClaim, claimErr error, response *GatewayRebindCommitResponse,
) (bool, error) {
	if !strict.accepted.created || !validGatewayRebindAcceptedClaim(strict.accepted, strict.candidate) ||
		ctx.Err() != nil {
		return false, claimErr
	}
	abort, handled, abortErr := m.abortAcceptedGatewayRebindPreparedWithoutIntentLocked(ctx,
		repository, driver, strict, liveSession)
	terminalSQL := m.gatewayRebindCommitBarrierLatch().Load()
	if abortErr != nil {
		return terminalSQL, abortErr
	}
	if !handled {
		return terminalSQL, claimErr
	}
	_ = abort
	terminal, err := m.readGatewayRebindTerminalResponseLocked(ctx, repository,
		request, actorID, liveSession, driver, true)
	if err == nil && response != nil {
		*response = terminal
	}
	return terminalSQL, err
}

func (m *Manager) abortAcceptedGatewayRebindPreparedWithoutIntentLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, driver gatewayRebindCrossStoreDriver,
	strict gatewayRebindStrictClaim, liveSession func(context.Context) error,
) (GatewayRebindCommitResult, bool, error) {
	if m == nil || ctx == nil || repository == nil || driver == nil || liveSession == nil ||
		!strict.accepted.created || !validGatewayRebindAcceptedClaim(strict.accepted, strict.candidate) {
		return GatewayRebindCommitResult{}, false, gatewayRebindProposalError(ctx)
	}
	eligible, err := m.gatewayRebindAcceptedAbortEligibleLocked(ctx, repository, strict)
	if err != nil || !eligible {
		return GatewayRebindCommitResult{}, false, err
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return GatewayRebindCommitResult{}, false, err
	}
	evidence, err := m.proveGatewayRebindPreparedWithoutIntentAbortLocked(ctx, repository, driver,
		strict.accepted.claim)
	if err != nil {
		return GatewayRebindCommitResult{}, false, err
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return GatewayRebindCommitResult{}, false, err
	}
	result, err := m.completeGatewayRebindPreparedWithoutIntentAbortLocked(ctx, repository, evidence)
	return result, true, err
}

func (m *Manager) gatewayRebindAcceptedAbortEligibleLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, strict gatewayRebindStrictClaim,
) (bool, error) {
	claim := strict.accepted.claim
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		!reflect.DeepEqual(*snapshot.Active.Claim.V2, claim) || snapshot.Phase != appaccess.GatewayRebindPrepared ||
		!snapshot.RollbackAllowed || snapshot.DatabaseCommitObserved || snapshot.DatabaseCommittedEvent != nil ||
		!sameGatewayRebindRosterV2(snapshot.Active.RosterV2, strict.candidate.Proposal.Roster) ||
		!sameGatewayRebindRuntimeHeads(snapshot.Active.RuntimeHeads, strict.candidate.Proposal.RuntimeHeads) {
		return false, gatewayRebindProposalError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return false, err
	}
	operationID := claim.Spec.OperationID
	for _, intent := range history.Intents {
		if intent.Intent.OperationID == operationID {
			return false, nil
		}
	}
	for _, intent := range history.IntentsV2 {
		if intent.Intent.OperationID == operationID {
			return false, nil
		}
	}
	for _, progress := range history.Progress {
		if progress.Record.OperationID == operationID {
			return false, nil
		}
	}
	for _, terminal := range history.Terminals {
		if terminal.Receipt.OperationID == operationID {
			return false, nil
		}
	}
	for _, terminal := range history.TerminalsV2 {
		if terminal.Receipt.OperationID == operationID {
			return false, nil
		}
	}
	var checkpoint *gatewayRebindPredecessorCheckpoint
	for _, selected := range history.Checkpoints {
		candidate := selected.Checkpoint
		if candidate.Generation != claim.Spec.SuccessorProtectedGeneration || candidate.OperationID != operationID {
			continue
		}
		if checkpoint != nil {
			return false, gatewayRebindProposalError(ctx)
		}
		copy := candidate
		checkpoint = &copy
	}
	if checkpoint == nil {
		return false, nil
	}
	if !reflect.DeepEqual(*checkpoint, strict.candidate.Checkpoint) {
		return false, gatewayRebindProposalError(ctx)
	}
	return ctx.Err() == nil, nil
}

func (m *Manager) readGatewayRebindTerminalResponseLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, request GatewayRebindCommitRequest, actorID string,
	liveSession func(context.Context) error, driver managerGatewayRebindCrossStoreDriver, created bool,
) (GatewayRebindCommitResponse, error) {
	physical, ok := newManagedGatewayCurrentPhysicalDriver(m).(gatewayRebindCommitTerminalPhysicalDriver)
	if !ok {
		return GatewayRebindCommitResponse{}, gatewayRebindProposalError(ctx)
	}
	return m.readGatewayRebindTerminalResponseWithProofLocked(ctx, repository, request, actorID,
		liveSession, driver, created, m.readGatewayRebindCommitCurrentProofLocked, physical)
}

func (m *Manager) readGatewayRebindTerminalResponseWithProofLocked(ctx context.Context,
	repository GatewayRebindCommitRepository, request GatewayRebindCommitRequest, actorID string,
	liveSession func(context.Context) error, driver managerGatewayRebindCrossStoreDriver, created bool,
	readCurrent gatewayRebindCommitCurrentProofReader, physical gatewayRebindCommitTerminalPhysicalDriver,
) (GatewayRebindCommitResponse, error) {
	zero := GatewayRebindCommitResponse{}
	if m == nil || ctx == nil || repository == nil || liveSession == nil || driver.manager == nil ||
		readCurrent == nil || physical == nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	projection, found, err := readGatewayRebindCommitReplay(ctx, snapshot, request, actorID)
	if err != nil || !found || projection.TerminalCommand == nil || projection.Response.RecoveryRequired ||
		!projection.Response.FenceReleased || projection.Response.SelectedCurrentAuthority == nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	receipt, err := gatewayRebindActiveTerminalV2(history, projection.Claim)
	if err != nil || receipt == nil || !validGatewayRebindTerminalReceiptV2(*receipt) ||
		receipt.Digest != projection.TerminalCommand.TerminalReceiptDigest ||
		receipt.Disposition != projection.TerminalCommand.TerminalDisposition {
		return zero, gatewayRebindProposalError(ctx)
	}
	if snapshot.Active != nil {
		response := projection.Response
		response.Created = created
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return zero, err
		}
		if !errors.Is(repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
			return zero, gatewayRebindProposalError(ctx)
		}
		freshSnapshot, snapshotErr := repository.GatewayRebindRecoverySnapshot(ctx)
		freshHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		freshReceipt, terminalErr := gatewayRebindActiveTerminalV2(freshHistory, projection.Claim)
		freshProjection, found, replayErr := readGatewayRebindCommitReplay(ctx, freshSnapshot, request, actorID)
		if snapshotErr != nil || !reflect.DeepEqual(snapshot, freshSnapshot) || historyErr != nil ||
			!sameGatewayRebindCurrentHistory(history, freshHistory) || terminalErr != nil || freshReceipt == nil ||
			!reflect.DeepEqual(*receipt, *freshReceipt) || replayErr != nil || !found ||
			freshProjection.TerminalCommand == nil ||
			!reflect.DeepEqual(*projection.TerminalCommand, *freshProjection.TerminalCommand) ||
			!errors.Is(repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
			return zero, gatewayRebindProposalError(ctx)
		}
		if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
			return zero, err
		}
		return response, nil
	}
	current, err := readCurrent(ctx, repository, snapshot, liveSession)
	if err != nil || !validSHA256(current.Digest) ||
		gatewayCurrentAuthority(current.Selection.Lineage) != *projection.Response.SelectedCurrentAuthority {
		return zero, gatewayRebindProposalError(ctx)
	}
	guard := func(guardCtx context.Context) error {
		if err := checkGatewayRebindAdmissionSession(guardCtx, liveSession); err != nil ||
			repository.CheckGatewayRebindFence(guardCtx) != nil {
			return gatewayRebindProposalError(guardCtx)
		}
		freshSnapshot, snapshotErr := repository.GatewayRebindRecoverySnapshot(guardCtx)
		freshHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		freshReceipt, terminalErr := gatewayRebindActiveTerminalV2(freshHistory, projection.Claim)
		if snapshotErr != nil || !reflect.DeepEqual(snapshot, freshSnapshot) || historyErr != nil ||
			!sameGatewayRebindCurrentHistory(history, freshHistory) || terminalErr != nil || freshReceipt == nil ||
			!reflect.DeepEqual(*receipt, *freshReceipt) {
			return gatewayRebindProposalError(guardCtx)
		}
		return nil
	}
	if err := guard(ctx); err != nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	exactCurrent, err := m.gatewayRebindTerminalSelectsCurrentLocked(projection, history, *receipt,
		current.Selection)
	if err != nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	response := projection.Response
	response.Created = created
	response.CurrentProof = GatewayRebindProofVerified
	response.CurrentAttestationDigest = current.Digest
	if !exactCurrent {
		// A later operation may be selected current. Its fresh physical proof
		// does not establish this historical operation's terminal outcome.
		confirmed, proofErr := readCurrent(ctx, repository, snapshot, liveSession)
		if proofErr != nil || current.Digest != confirmed.Digest ||
			!sameGatewayCurrentSelection(current.Selection, confirmed.Selection) || guard(ctx) != nil {
			return zero, gatewayRebindProposalError(ctx)
		}
		return response, nil
	}
	terminalDigest, err := m.reconfirmGatewayRebindCreatedTerminalProofLocked(ctx, driver,
		current, projection, history, *receipt, guard, physical)
	if err != nil {
		return zero, err
	}
	confirmed, proofErr := readCurrent(ctx, repository, snapshot, liveSession)
	if proofErr != nil || current.Digest != confirmed.Digest ||
		!sameGatewayCurrentSelection(current.Selection, confirmed.Selection) || guard(ctx) != nil {
		return zero, gatewayRebindProposalError(ctx)
	}
	response.TerminalProof = GatewayRebindProofVerified
	response.TerminalProofDigest = terminalDigest
	return response, nil
}

func (m *Manager) reconfirmGatewayRebindCreatedTerminalProofLocked(ctx context.Context,
	driver managerGatewayRebindCrossStoreDriver, current gatewayRebindCommitCurrentProof,
	projection gatewayRebindCommitReplayProjection, history gatewayRebindProtectedIntentHistory,
	receipt gatewayRebindTerminalReceiptV2, guard gatewayRebindTypedEffectGuard,
	physical gatewayRebindCommitTerminalPhysicalDriver,
) (string, error) {
	switch receipt.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		return m.reconfirmGatewayRebindCommitTerminalProofLocked(ctx, current,
			projection, history, receipt, guard, physical)
	case appaccess.GatewayRebindDispositionAbort:
		if receipt.NoEffectProof != nil {
			if guard == nil || guard(ctx) != nil {
				return "", gatewayRebindProposalError(ctx)
			}
			checkpoint, err := gatewayRebindActiveCheckpoint(history, projection.Claim)
			if err != nil || len(projection.History.Events) == 0 {
				return "", gatewayRebindProposalError(ctx)
			}
			prepared := projection.Claim
			prepared.State, prepared.StateSequence, prepared.UpdatedAt = appaccess.GatewayRebindPrepared, 1, prepared.CreatedAt
			fresh, err := driver.proveNoSuccessorEffectsLocked(ctx, prepared,
				projection.History.RosterV2, checkpoint)
			if err != nil || !gatewayRebindNoEffectProofReconfirms(*receipt.NoEffectProof, fresh, receipt.CreatedAt) ||
				guard(ctx) != nil {
				return "", gatewayRebindProposalError(ctx)
			}
			return fresh.Digest, nil
		}
		return m.reconfirmGatewayRebindRollbackTerminalProofLocked(ctx, driver, current,
			projection, history, receipt, guard)
	default:
		return "", gatewayRebindProposalError(ctx)
	}
}

func (m *Manager) reconfirmGatewayRebindCommitTerminalProofLocked(ctx context.Context,
	current gatewayRebindCommitCurrentProof,
	projection gatewayRebindCommitReplayProjection, history gatewayRebindProtectedIntentHistory,
	receipt gatewayRebindTerminalReceiptV2, guard gatewayRebindTypedEffectGuard,
	physical gatewayRebindCommitTerminalPhysicalDriver,
) (string, error) {
	if receipt.Disposition != appaccess.GatewayRebindDispositionCommit || receipt.PhysicalProof == nil ||
		receipt.Resources == nil || current.Selection.State == nil || guard == nil || physical == nil {
		return "", gatewayRebindProposalError(ctx)
	}
	_, err := gatewayRebindRetainedTerminalAttempt(projection, history, receipt)
	if err != nil {
		return "", gatewayRebindProposalError(ctx)
	}
	var terminal *gatewayRebindProgressRecord
	for index := range history.Progress {
		candidate := history.Progress[index].Record
		if candidate.Generation != receipt.Generation || candidate.OperationID != receipt.OperationID ||
			candidate.Sequence != receipt.ProtectedRecordSequence {
			continue
		}
		if terminal != nil {
			return "", gatewayRebindProposalError(ctx)
		}
		copy := candidate
		terminal = &copy
	}
	if terminal == nil || terminal.Digest != receipt.ProtectedRecordDigest ||
		terminal.Phase != gatewayRebindProgressHandoverCommitted || terminal.TypedEffect == nil ||
		terminal.TypedEffect.HandoverIntent == nil || terminal.TypedEffect.FinalContainer == nil {
		return "", gatewayRebindProposalError(ctx)
	}
	retained, ok := gatewayRebindTypedPhysicalResultFromProgress(*terminal)
	if !ok || !gatewayRebindPhysicalMatchesTerminal(retained, receipt) || guard(ctx) != nil {
		return "", gatewayRebindProposalError(ctx)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(current.Selection, history)
	target, targetErr := gatewayCurrentPhysicalTargetFor(current.Selection, *current.Selection.State, nil, nil)
	if err != nil || targetErr != nil ||
		target.Pending != nil || target.LANRecovery != nil || guard(ctx) != nil ||
		physical.observeGatewayCurrentPredecessorsRetired(ctx, action) != nil || guard(ctx) != nil {
		return "", gatewayRebindProposalError(ctx)
	}
	serving, err := physical.attestGatewayRebindCurrent(ctx, target, guard)
	if err != nil || !gatewayCurrentServingRestoreProofMatches(serving, target, gatewayCurrentPhysicalStableServing) ||
		guard(ctx) != nil || physical.observeGatewayCurrentPredecessorsRetired(ctx, action) != nil || guard(ctx) != nil {
		return "", gatewayRebindProposalError(ctx)
	}
	return canonicalDigest(struct {
		Version                     int                                       `json:"version"`
		Purpose                     string                                    `json:"purpose"`
		TerminalReceiptDigest       string                                    `json:"terminalReceiptDigest"`
		ProtectedRecordDigest       string                                    `json:"protectedRecordDigest"`
		RetainedPhysicalProofDigest string                                    `json:"retainedPhysicalProofDigest"`
		Retirement                  gatewayCurrentPredecessorRetirementAction `json:"retirement"`
		FreshServing                gatewayCurrentPhysicalAttestation         `json:"freshServing"`
		CurrentAttestationDigest    string                                    `json:"currentAttestationDigest"`
	}{1, "hostd/generated-ingress/rebind/commit-facade/commit-terminal-proof/v1", receipt.Digest,
		receipt.ProtectedRecordDigest, receipt.PhysicalProof.Digest, action, serving, current.Digest})
}

func (m *Manager) gatewayRebindTerminalSelectsCurrentLocked(projection gatewayRebindCommitReplayProjection,
	history gatewayRebindProtectedIntentHistory, receipt gatewayRebindTerminalReceiptV2,
	current gatewayCurrentSelection,
) (bool, error) {
	invalid := func() (bool, error) {
		return false, gatewayRebindProposalError(context.Background())
	}
	expectedAuthority, err := gatewayRebindTerminalSelectedAuthority(receipt)
	if err != nil {
		return invalid()
	}
	if expectedAuthority != gatewayCurrentAuthority(current.Lineage) {
		return false, nil
	}
	switch receipt.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		attempt, err := gatewayRebindRetainedTerminalAttempt(projection, history, receipt)
		if err != nil {
			return invalid()
		}
		transfers, err := newGatewayRebindTransfersV2(attempt.Intent, receipt, attempt.Checkpoint)
		if err != nil {
			return invalid()
		}
		state, err := newGatewayCurrentRouteBaselineFromV2Terminal(attempt.Intent, receipt,
			attempt.Checkpoint, transfers)
		if err != nil {
			return invalid()
		}
		lineage, err := gatewayRebindCurrentLineageV2(receipt)
		if err != nil {
			return invalid()
		}
		terminal, err := newGatewayRebindAttemptTerminalViewV2(receipt)
		if err != nil {
			return invalid()
		}
		store, err := newGatewayCurrentRouteStateStore(m.options.DataRoot, lineage)
		if err != nil {
			return invalid()
		}
		expected := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: lineage,
			Terminal: &terminal, Store: store, State: &state}
		return sameGatewayCurrentSelection(expected, current), nil
	case appaccess.GatewayRebindDispositionAbort:
		if !validGatewayRebindTerminalReceiptV2(receipt) {
			return invalid()
		}
		checkpoint, err := gatewayRebindCheckpointForSelection(receipt.Generation, receipt.OperationID, current)
		if err != nil {
			return invalid()
		}
		return checkpoint.sourceRef() == receipt.Predecessor, nil
	default:
		return invalid()
	}
}

func gatewayRebindTerminalSelectedAuthority(receipt gatewayRebindTerminalReceiptV2) (
	appaccess.GatewayCurrentAuthorityRef, error,
) {
	switch receipt.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		lineage, err := gatewayRebindCurrentLineageV2(receipt)
		if err != nil {
			return appaccess.GatewayCurrentAuthorityRef{}, err
		}
		return gatewayCurrentAuthority(lineage), nil
	case appaccess.GatewayRebindDispositionAbort:
		if !validGatewayRebindTerminalReceiptV2(receipt) {
			return appaccess.GatewayCurrentAuthorityRef{}, gatewayRebindProposalError(context.Background())
		}
		return gatewayCurrentAuthority(receipt.Predecessor.Lineage), nil
	default:
		return appaccess.GatewayCurrentAuthorityRef{}, gatewayRebindProposalError(context.Background())
	}
}

func (m *Manager) reconfirmGatewayRebindRollbackTerminalProofLocked(ctx context.Context,
	driver managerGatewayRebindCrossStoreDriver, current gatewayRebindCommitCurrentProof,
	projection gatewayRebindCommitReplayProjection, history gatewayRebindProtectedIntentHistory,
	receipt gatewayRebindTerminalReceiptV2, guard gatewayRebindTypedEffectGuard,
) (string, error) {
	runtime, ok := driver.handover.(gatewayRebindTypedHandoverRuntime)
	if !ok || receipt.Disposition != appaccess.GatewayRebindDispositionAbort || receipt.RollbackProof == nil ||
		receipt.RollbackProof.PhysicalProof == nil || guard == nil {
		return "", gatewayRebindProposalError(ctx)
	}
	attempt, err := gatewayRebindRetainedTerminalAttempt(projection, history, receipt)
	if err != nil {
		return "", gatewayRebindProposalError(ctx)
	}
	readPredecessor := func(readCtx context.Context) (gatewayRebindTypedPredecessorObservation, error) {
		if guard(readCtx) != nil {
			return gatewayRebindTypedPredecessorObservation{}, gatewayRebindProposalError(readCtx)
		}
		predecessor, readErr := runtime.predecessor(readCtx, attempt)
		if readErr != nil || !sameGatewayCurrentSelection(current.Selection, predecessor.Selection) ||
			guard(readCtx) != nil {
			return gatewayRebindTypedPredecessorObservation{}, gatewayRebindProposalError(readCtx)
		}
		return predecessor, nil
	}
	physical, err := runtime.observeGatewayRebindRollbackServing(ctx, attempt, readPredecessor, guard)
	if err != nil || physical.Refuse || !validContainerID(physical.Observation.PredecessorContainerID) ||
		!validSHA256(physical.Observation.PredecessorObservationDigest) ||
		!validSHA256(physical.Observation.PredecessorRoutesDigest) ||
		!validSHA256(physical.Observation.StageAbsenceDigest) ||
		physical.Observation.PredecessorRoutesDigest != receipt.RollbackProof.PredecessorRoutesDigest {
		return "", gatewayRebindProposalError(ctx)
	}
	return canonicalDigest(struct {
		Version                      int    `json:"version"`
		Purpose                      string `json:"purpose"`
		TerminalReceiptDigest        string `json:"terminalReceiptDigest"`
		RollbackProgressDigest       string `json:"rollbackProgressDigest"`
		RollbackPhysicalProofDigest  string `json:"rollbackPhysicalProofDigest"`
		CurrentAttestationDigest     string `json:"currentAttestationDigest"`
		PredecessorContainerID       string `json:"predecessorContainerId"`
		PredecessorObservationDigest string `json:"predecessorObservationDigest"`
		PredecessorRoutesDigest      string `json:"predecessorRoutesDigest"`
		StageAbsenceDigest           string `json:"stageAbsenceDigest"`
	}{1, "hostd/generated-ingress/rebind/commit-facade/rollback-terminal-proof/v1", receipt.Digest,
		receipt.RollbackProof.Digest, receipt.RollbackProof.PhysicalProof.Digest, current.Digest,
		physical.Observation.PredecessorContainerID, physical.Observation.PredecessorObservationDigest,
		physical.Observation.PredecessorRoutesDigest, physical.Observation.StageAbsenceDigest})
}

func gatewayRebindRetainedTerminalAttempt(projection gatewayRebindCommitReplayProjection,
	history gatewayRebindProtectedIntentHistory, receipt gatewayRebindTerminalReceiptV2,
) (gatewayRebindPreparedAttempt, error) {
	invalid := func() (gatewayRebindPreparedAttempt, error) {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(context.Background())
	}
	var intent *gatewayRebindProtectedIntentV2
	for index := range history.IntentsV2 {
		candidate := &history.IntentsV2[index].Intent
		if candidate.Generation != receipt.Generation || candidate.OperationID != receipt.OperationID {
			continue
		}
		if intent != nil {
			return invalid()
		}
		intent = candidate
	}
	if intent == nil || intent.Digest != receipt.ProtectedIntentDigest ||
		!sameGatewayRebindClaimV2Admission(intent.Claim, projection.Claim) {
		return invalid()
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(*intent, history)
	if err != nil {
		return invalid()
	}
	progress := make([]gatewayRebindProgressSelection, 0)
	for _, retained := range history.Progress {
		if retained.Generation == receipt.Generation && retained.Record.OperationID == receipt.OperationID {
			progress = append(progress, retained)
		}
	}
	if !gatewayRebindTerminalV2MatchesIntentHistory(receipt, *intent, attempt.Checkpoint, progress) ||
		len(progress) == 0 {
		return invalid()
	}
	physical, ok := gatewayRebindTypedPhysicalResultFromProgress(progress[len(progress)-1].Record)
	if !ok || !gatewayRebindPhysicalMatchesTerminal(physical, receipt) {
		return invalid()
	}
	return attempt, nil
}
