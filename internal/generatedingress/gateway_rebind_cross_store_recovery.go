package generatedingress

import (
	"context"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindStartupRecoveryRepository is the complete SQL boundary used by
// early startup recovery. RecoverGatewayRebindStartup owns the deployment
// effects lease and both gateway locks; callers must invoke it before ordinary
// worker admission acquires its effects lease.
type GatewayRebindStartupRecoveryRepository interface {
	GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)
	GatewayRebindRuntimeHeads(context.Context) ([]appaccess.GatewayRebindRuntimeHead, error)
	ResolveGatewayBinding(context.Context, appaccess.GatewayBindingRef) (appaccess.GatewayBindingResolution, error)
	ApplyGatewayRebindTransition(context.Context, appaccess.GatewayRebindTransitionProof) (appaccess.GatewayRebindTransitionCommand, error)
	CheckGatewayRebindFence(context.Context) error
}

type GatewayRebindStartupRecoveryResult struct {
	RebindHistoryPresent     bool
	Recovered                bool
	ActiveOperationID        string
	InitialActivePhase       appaccess.GatewayRebindState
	FinalActivePhase         appaccess.GatewayRebindState
	Disposition              appaccess.GatewayRebindTerminalDisposition
	TerminalReceiptDigest    string
	SelectedCurrentAuthority *appaccess.GatewayCurrentAuthorityRef
	CurrentStateVersion      uint64
	CurrentStateRevision     uint64
	CurrentStateDigest       string
	CurrentAttestationDigest string
	FenceReleased            bool
}

func (m *Manager) RecoverGatewayRebindStartup(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository,
) (GatewayRebindStartupRecoveryResult, error) {
	return m.recoverGatewayRebindStartupWithDriver(ctx, repository, m.gatewayRebindDriver())
}

func (m *Manager) recoverGatewayRebindStartupWithDriver(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository, driver gatewayRebindCrossStoreDriver,
) (result GatewayRebindStartupRecoveryResult, resultErr error) {
	if m == nil || ctx == nil || repository == nil || driver == nil {
		return GatewayRebindStartupRecoveryResult{}, &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRaw(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			m.gatewayRebindFailStopLatch().Store(true)
		}
		return GatewayRebindStartupRecoveryResult{}, err
	}
	defer func() {
		// Startup recovery always releases the effects lease before the
		// gateway lock. A failed release latches the process before another
		// Manager can cross the gateway boundary, even on a no-history path.
		resultErr = m.releaseGatewayRebindTerminalLocks(releaseEffects, releaseGateway, resultErr)
		if resultErr != nil {
			result = GatewayRebindStartupRecoveryResult{}
		}
	}()

	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	if len(snapshot.History) == 0 {
		presence, presenceErr := InspectGatewayRebindStartupPresence(ctx, m.options.DataRoot, repository)
		if presenceErr != nil || presence.Present || presence.ProtectedArtifacts ||
			repository.CheckGatewayRebindFence(ctx) != nil {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		return GatewayRebindStartupRecoveryResult{FenceReleased: true}, nil
	}
	result.RebindHistoryPresent = true
	if snapshot.Active == nil {
		return m.recoverGatewayRebindCommittedCurrentLocked(ctx, repository, driver, snapshot, result)
	}
	if snapshot.Active.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		snapshot.Active.Claim.V2 == nil || snapshot.Phase == "" {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	claim := *snapshot.Active.Claim.V2
	result.ActiveOperationID = claim.Spec.OperationID
	result.InitialActivePhase = snapshot.Phase

	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, err
	}
	receipt, err := gatewayRebindActiveTerminalV2(history, claim)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	if receipt != nil && !gatewayRebindTerminalMatchesActiveSnapshot(*receipt, snapshot) {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	if snapshot.Phase == appaccess.GatewayRebindPrepared && receipt != nil &&
		receipt.ProtectedIntentDigest == "" {
		checkpoint, checkpointErr := gatewayRebindActiveCheckpoint(history, claim)
		if checkpointErr != nil || receipt.NoEffectProof == nil {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		beforeFiles, filesErr := readGatewayHistorySnapshotMode(m.store, true)
		if filesErr != nil {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		freshProof, proofErr := driver.proveNoSuccessorEffectsLocked(ctx, claim,
			snapshot.Active.RosterV2, checkpoint)
		if proofErr != nil || !gatewayRebindNoEffectProofReconfirms(*receipt.NoEffectProof,
			freshProof, receipt.CreatedAt) {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		freshSnapshot, snapshotErr := repository.GatewayRebindRecoverySnapshot(ctx)
		freshHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterFiles, afterFilesErr := readGatewayHistorySnapshotMode(m.store, true)
		if snapshotErr != nil || !reflect.DeepEqual(snapshot, freshSnapshot) || historyErr != nil ||
			!sameGatewayRebindCurrentHistory(history, freshHistory) || afterFilesErr != nil ||
			!sameGatewayHistorySnapshot(beforeFiles, afterFiles) || ctx.Err() != nil {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		freshReceipt, terminalErr := gatewayRebindActiveTerminalV2(freshHistory, claim)
		freshCheckpoint, checkpointErr := gatewayRebindActiveCheckpoint(freshHistory, claim)
		if terminalErr != nil || freshReceipt == nil || !reflect.DeepEqual(*receipt, *freshReceipt) ||
			checkpointErr != nil || !reflect.DeepEqual(checkpoint, freshCheckpoint) {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		commitResult, applyErr := m.applyGatewayRebindRetainedRollbackLocked(ctx, repository,
			freshSnapshot, *freshReceipt)
		if applyErr != nil {
			return GatewayRebindStartupRecoveryResult{}, applyErr
		}
		return m.recoverGatewayRebindTerminalCurrentLocked(ctx, repository, driver, commitResult)
	}

	var prepared gatewayRebindPreparedAttempt
	if snapshot.Phase == appaccess.GatewayRebindPrepared {
		prepared, err = m.recoverGatewayRebindPreparedAdmissionLocked(ctx, repository, snapshot)
		if err != nil {
			commitResult, abortErr := m.abortGatewayRebindPreparedWithoutIntentLocked(ctx, repository, driver,
				claim.Spec.OperationID)
			if abortErr != nil {
				return GatewayRebindStartupRecoveryResult{}, err
			}
			return m.recoverGatewayRebindTerminalCurrentLocked(ctx, repository, driver, commitResult)
		}
		history, err = m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	} else {
		prepared, err = gatewayRebindPreparedAttemptFromHistory(snapshot, history)
	}
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	if receipt == nil {
		receipt, err = gatewayRebindActiveTerminalV2(history, claim)
		if err != nil {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
	}

	appendProgress := func(appendCtx context.Context, record gatewayRebindProgressRecord) error {
		store, storeErr := newGatewayRebindProgressStore(m.options.DataRoot, record.Generation,
			record.OperationID, record.Sequence)
		if storeErr != nil {
			return storeErr
		}
		return store.installExact(appendCtx, record)
	}
	mode := gatewayRebindPhysicalReconcileUndecided
	if receipt != nil {
		if receipt.Disposition == appaccess.GatewayRebindDispositionCommit {
			mode = gatewayRebindPhysicalReconcileForwardOnly
		} else if receipt.Disposition == appaccess.GatewayRebindDispositionAbort {
			mode = gatewayRebindPhysicalReconcileRollbackOnly
		}
	}
	if snapshot.DatabaseCommitObserved || snapshot.Phase == appaccess.GatewayRebindDatabaseCommitted {
		mode = gatewayRebindPhysicalReconcileForwardOnly
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: prepared, Mode: mode, SQLPhase: snapshot.Phase,
		RollbackAllowed: snapshot.RollbackAllowed, DatabaseCommitObserved: snapshot.DatabaseCommitObserved,
		Terminal: receipt}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	physical, err := driver.reconcileSuccessorLocked(ctx, request, appendProgress)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, err
	}
	if receipt == nil {
		value, valueErr := m.installGatewayRebindTerminalForPhysicalLocked(ctx, prepared, physical)
		if valueErr != nil {
			return GatewayRebindStartupRecoveryResult{}, valueErr
		}
		receipt = &value
	} else if !gatewayRebindPhysicalMatchesTerminal(physical, *receipt) {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	if receipt.Disposition == appaccess.GatewayRebindDispositionAbort {
		commitResult, applyErr := m.applyGatewayRebindRetainedRollbackLocked(ctx, repository, snapshot, *receipt)
		if applyErr != nil {
			return GatewayRebindStartupRecoveryResult{}, applyErr
		}
		return m.recoverGatewayRebindTerminalCurrentLocked(ctx, repository, driver, commitResult)
	}
	if receipt.Disposition != appaccess.GatewayRebindDispositionCommit {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	commitResult, err := m.recoverGatewayRebindCommitTerminalLocked(ctx, repository, driver, prepared, *receipt)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, err
	}
	return m.recoverGatewayRebindTerminalCurrentLocked(ctx, repository, driver, commitResult)
}

func gatewayRebindActiveCheckpoint(history gatewayRebindProtectedIntentHistory,
	claim appaccess.GatewayRebindClaimV2,
) (gatewayRebindPredecessorCheckpoint, error) {
	var result *gatewayRebindPredecessorCheckpoint
	for _, selected := range history.Checkpoints {
		candidate := selected.Checkpoint
		if candidate.Generation != claim.Spec.SuccessorProtectedGeneration ||
			candidate.OperationID != claim.Spec.OperationID {
			continue
		}
		if result != nil {
			return gatewayRebindPredecessorCheckpoint{}, errors.New("ambiguous retained rebind checkpoint")
		}
		copy := candidate
		result = &copy
	}
	if result == nil || result.sourceRef() != claim.Spec.Predecessor {
		return gatewayRebindPredecessorCheckpoint{}, errors.New("retained rebind checkpoint is missing")
	}
	return *result, nil
}

func gatewayRebindNoEffectProofReconfirms(retained, fresh gatewayRebindNoEffectAbortProof,
	receiptCreatedAt string,
) bool {
	if !validGatewayRebindNoEffectAbortProof(retained) || !validGatewayRebindNoEffectAbortProof(fresh) {
		return false
	}
	receiptAt, err := parseGatewayRebindProgressTime(receiptCreatedAt)
	if err != nil {
		return false
	}
	freshAt, err := parseGatewayRebindProgressTime(fresh.CreatedAt)
	if err != nil || !freshAt.After(receiptAt) {
		return false
	}
	retained.CreatedAt, retained.Digest = "", ""
	fresh.CreatedAt, fresh.Digest = "", ""
	return reflect.DeepEqual(retained, fresh)
}

func gatewayRebindPreparedAttemptFromHistory(snapshot appaccess.GatewayRebindRecoverySnapshot,
	history gatewayRebindProtectedIntentHistory,
) (gatewayRebindPreparedAttempt, error) {
	invalid := errors.New("invalid retained typed rebind attempt")
	if snapshot.Active == nil || snapshot.Active.Claim.V2 == nil {
		return gatewayRebindPreparedAttempt{}, invalid
	}
	claim := *snapshot.Active.Claim.V2
	var checkpoint *gatewayRebindPredecessorCheckpoint
	for _, selected := range history.Checkpoints {
		if selected.Checkpoint.Generation == claim.Spec.SuccessorProtectedGeneration &&
			selected.Checkpoint.OperationID == claim.Spec.OperationID {
			if checkpoint != nil {
				return gatewayRebindPreparedAttempt{}, invalid
			}
			value := selected.Checkpoint
			checkpoint = &value
		}
	}
	var intent *gatewayRebindProtectedIntentV2
	for _, selected := range history.IntentsV2 {
		if selected.Intent.Generation == claim.Spec.SuccessorProtectedGeneration &&
			selected.Intent.OperationID == claim.Spec.OperationID {
			if intent != nil {
				return gatewayRebindPreparedAttempt{}, invalid
			}
			value := selected.Intent
			intent = &value
		}
	}
	var first *gatewayRebindProgressRecord
	for _, selected := range history.Progress {
		if selected.Record.Generation == claim.Spec.SuccessorProtectedGeneration &&
			selected.Record.OperationID == claim.Spec.OperationID && selected.Record.Sequence == 1 {
			if first != nil {
				return gatewayRebindPreparedAttempt{}, invalid
			}
			value := selected.Record
			first = &value
		}
	}
	if checkpoint == nil || intent == nil || first == nil || checkpoint.sourceRef() != claim.Spec.Predecessor ||
		!sameGatewayRebindClaimV2Admission(intent.Claim, claim) || !sameGatewayRebindRosterV2(intent.Roster, snapshot.Active.RosterV2) ||
		!sameGatewayRebindRuntimeHeads(intent.RuntimeHeads, snapshot.Active.RuntimeHeads) ||
		!gatewayRebindProgressMatchesIntentV2(*first, *intent, nil) {
		return gatewayRebindPreparedAttempt{}, invalid
	}
	return gatewayRebindPreparedAttempt{Claim: intent.Claim, Checkpoint: *checkpoint, Intent: *intent, Progress: *first}, nil
}

func sameGatewayRebindClaimV2Admission(left, right appaccess.GatewayRebindClaimV2) bool {
	right.State, right.StateSequence, right.UpdatedAt = left.State, left.StateSequence, left.UpdatedAt
	return reflect.DeepEqual(left, right)
}

func gatewayRebindActiveTerminalV2(history gatewayRebindProtectedIntentHistory,
	claim appaccess.GatewayRebindClaimV2,
) (*gatewayRebindTerminalReceiptV2, error) {
	var result *gatewayRebindTerminalReceiptV2
	for _, selected := range history.TerminalsV2 {
		if selected.Receipt.Generation != claim.Spec.SuccessorProtectedGeneration ||
			selected.Receipt.OperationID != claim.Spec.OperationID {
			continue
		}
		if result != nil {
			return nil, errors.New("ambiguous retained typed rebind terminal")
		}
		value := selected.Receipt
		result = &value
	}
	return result, nil
}

func gatewayRebindTerminalMatchesActiveSnapshot(receipt gatewayRebindTerminalReceiptV2,
	snapshot appaccess.GatewayRebindRecoverySnapshot,
) bool {
	if snapshot.Active == nil || snapshot.Active.Claim.V2 == nil {
		return false
	}
	claim := *snapshot.Active.Claim.V2
	preparedClaim := claim
	preparedClaim.State, preparedClaim.StateSequence, preparedClaim.UpdatedAt =
		appaccess.GatewayRebindPrepared, 1, preparedClaim.CreatedAt
	databaseDigest, err := gatewayRebindPreparedDatabaseDigest(preparedClaim, snapshot.Active.RosterV2,
		snapshot.Active.RuntimeHeads)
	return err == nil && receipt.Generation == claim.Spec.SuccessorProtectedGeneration &&
		receipt.OperationID == claim.Spec.OperationID && receipt.ClaimRequestDigest == claim.RequestDigest &&
		receipt.ClaimSpecDigest == claim.RebindApproval.SpecDigest && receipt.Predecessor == claim.Spec.Predecessor &&
		receipt.PreparedDatabaseDigest == databaseDigest && receipt.RosterDigest == claim.Spec.RosterDigest &&
		receipt.RosterCount == claim.Spec.RosterCount
}

func gatewayRebindPhysicalMatchesTerminal(physical gatewayRebindTypedPhysicalResult,
	receipt gatewayRebindTerminalReceiptV2,
) bool {
	if physical.Disposition != receipt.Disposition || physical.Last.Sequence != receipt.ProtectedRecordSequence ||
		string(physical.Last.Phase) != receipt.ProtectedPhase || physical.Last.Digest != receipt.ProtectedRecordDigest {
		return false
	}
	switch receipt.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		return receipt.Resources != nil && receipt.PhysicalProof != nil && physical.Last.TypedEffect != nil &&
			physical.Last.TypedEffect.Resources != nil && physical.Last.TypedEffect.PhysicalProof != nil &&
			reflect.DeepEqual(physical.Resources, *receipt.Resources) &&
			reflect.DeepEqual(physical.Proof, *receipt.PhysicalProof) &&
			reflect.DeepEqual(*physical.Last.TypedEffect.Resources, *receipt.Resources) &&
			reflect.DeepEqual(*physical.Last.TypedEffect.PhysicalProof, *receipt.PhysicalProof)
	case appaccess.GatewayRebindDispositionAbort:
		return receipt.RollbackProof != nil && physical.Last.TypedRollback != nil &&
			reflect.DeepEqual(*physical.Last.TypedRollback, *receipt.RollbackProof)
	default:
		return false
	}
}

func (m *Manager) installGatewayRebindTerminalForPhysicalLocked(ctx context.Context,
	prepared gatewayRebindPreparedAttempt, physical gatewayRebindTypedPhysicalResult,
) (gatewayRebindTerminalReceiptV2, error) {
	var receipt gatewayRebindTerminalReceiptV2
	var err error
	switch physical.Disposition {
	case appaccess.GatewayRebindDispositionCommit:
		receipt, err = newGatewayRebindCommitTerminalV2(prepared.Intent, physical.Last, physical.Resources,
			physical.Proof, gatewayRebindTimeStrictlyAfter(m.gatewayRebindProgressTime(), physical.Last.OccurredAt))
	case appaccess.GatewayRebindDispositionAbort:
		receipt, err = newGatewayRebindRollbackTerminalV2(prepared.Intent, physical.Last, prepared.Checkpoint,
			gatewayRebindTimeStrictlyAfter(m.gatewayRebindProgressTime(), physical.Last.OccurredAt))
	default:
		err = errors.New("invalid typed physical terminal disposition")
	}
	if err != nil {
		return gatewayRebindTerminalReceiptV2{}, err
	}
	store, err := newGatewayRebindTerminalStoreV2(m.options.DataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(ctx, receipt) != nil {
		return gatewayRebindTerminalReceiptV2{}, gatewayRebindProposalError(ctx)
	}
	return receipt, nil
}

func (m *Manager) applyGatewayRebindRetainedRollbackLocked(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
	receipt gatewayRebindTerminalReceiptV2,
) (GatewayRebindCommitResult, error) {
	if !gatewayRebindTerminalMatchesActiveSnapshot(receipt, snapshot) ||
		receipt.Disposition != appaccess.GatewayRebindDispositionAbort || snapshot.Active == nil ||
		snapshot.Active.Claim.V2 == nil || !snapshot.RollbackAllowed || snapshot.DatabaseCommitObserved ||
		snapshot.DatabaseCommittedEvent != nil ||
		(snapshot.Phase != appaccess.GatewayRebindPrepared && snapshot.Phase != appaccess.GatewayRebindSuccessorReady) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	if !m.gatewayRebindCommitBarrierLatch().CompareAndSwap(false, true) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	claim := *snapshot.Active.Claim.V2
	proof, err := gatewayRebindTransitionProofV2(snapshot, claim, receipt,
		appaccess.GatewayRebindRolledBack, gatewayCurrentRouteState{}, nil, "")
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	command, err := applyGatewayRebindTransitionReconciled(ctx, repository, proof)
	if err != nil {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, err
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	expected := gatewayCurrentAuthority(receipt.Predecessor.Lineage)
	if err != nil || confirmed.Active != nil || confirmed.CurrentSource == nil ||
		*confirmed.CurrentSource != expected || !gatewayRebindSnapshotRetainsCommand(confirmed, command) ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	return GatewayRebindCommitResult{OperationID: claim.Spec.OperationID, InitialPhase: snapshot.Phase,
		FinalPhase: appaccess.GatewayRebindRolledBack, Disposition: appaccess.GatewayRebindDispositionAbort,
		TerminalReceiptDigest: receipt.Digest, SelectedCurrentAuthority: expected, FenceReleased: true}, nil
}

func (m *Manager) recoverGatewayRebindCommitTerminalLocked(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository, driver gatewayRebindCrossStoreDriver,
	prepared gatewayRebindPreparedAttempt, receipt gatewayRebindTerminalReceiptV2,
) (GatewayRebindCommitResult, error) {
	transfers, err := newGatewayRebindTransfersV2(prepared.Intent, receipt, prepared.Checkpoint)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	baseline, err := newGatewayCurrentRouteBaselineFromV2Terminal(prepared.Intent, receipt,
		prepared.Checkpoint, transfers)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	initial := appaccess.GatewayRebindState("")
	for {
		snapshot, readErr := repository.GatewayRebindRecoverySnapshot(ctx)
		if readErr != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
			snapshot.Active.Claim.V2.Spec.OperationID != prepared.Claim.Spec.OperationID {
			return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
		}
		if initial == "" {
			initial = snapshot.Phase
		}
		switch snapshot.Phase {
		case appaccess.GatewayRebindPrepared:
			proof, proofErr := gatewayRebindTransitionProofV2(snapshot, prepared.Claim, receipt,
				appaccess.GatewayRebindSuccessorReady, gatewayCurrentRouteState{}, nil, "")
			if proofErr != nil {
				return GatewayRebindCommitResult{}, proofErr
			}
			if _, applyErr := applyGatewayRebindTransitionReconciled(ctx, repository, proof); applyErr != nil {
				return GatewayRebindCommitResult{}, applyErr
			}
		case appaccess.GatewayRebindSuccessorReady:
			store, storeErr := newGatewayCurrentRouteStateStore(m.options.DataRoot, baseline.Lineage)
			if storeErr != nil || store.installBaseline(baseline) != nil {
				return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
			}
			proof, proofErr := gatewayRebindTransitionProofV2(snapshot, prepared.Claim, receipt,
				appaccess.GatewayRebindDatabaseCommitted, baseline, transfers, "")
			if proofErr != nil {
				return GatewayRebindCommitResult{}, proofErr
			}
			if _, applyErr := applyGatewayRebindTransitionReconciled(ctx, repository, proof); applyErr != nil {
				return GatewayRebindCommitResult{}, applyErr
			}
		case appaccess.GatewayRebindDatabaseCommitted:
			return m.recoverGatewayRebindDatabaseCommittedLocked(ctx, repository, driver, prepared,
				receipt, baseline, initial)
		default:
			return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
		}
	}
}

func (m *Manager) recoverGatewayRebindDatabaseCommittedLocked(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository, driver gatewayRebindCrossStoreDriver,
	prepared gatewayRebindPreparedAttempt, receipt gatewayRebindTerminalReceiptV2,
	baseline gatewayCurrentRouteState, initial appaccess.GatewayRebindState,
) (GatewayRebindCommitResult, error) {
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Phase != appaccess.GatewayRebindDatabaseCommitted {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil || selection.State == nil || selection.Lineage != baseline.Lineage ||
		!reflect.DeepEqual(*selection.State, baseline) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	attestation, err := driver.attestCommittedCurrentLocked(ctx, selection)
	if err != nil || !validSHA256(attestation) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	fresh, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, fresh) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	freshSelection, err := m.selectGatewayCurrentLocked(ctx, fresh)
	if err != nil || !sameGatewayCurrentSelection(selection, freshSelection) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	if !m.gatewayRebindCommitBarrierLatch().CompareAndSwap(false, true) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	proof, err := gatewayRebindTransitionProofV2(fresh, prepared.Claim, receipt,
		appaccess.GatewayRebindCommitted, gatewayCurrentRouteState{}, nil, attestation)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	if _, err = applyGatewayRebindTransitionReconciled(ctx, repository, proof); err != nil {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, err
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || confirmed.Active != nil || confirmed.CurrentSource == nil ||
		*confirmed.CurrentSource != gatewayCurrentAuthority(baseline.Lineage) ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	confirmedSelection, err := m.selectGatewayCurrentLocked(ctx, confirmed)
	if err != nil || !sameGatewayCurrentSelection(selection, confirmedSelection) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	return GatewayRebindCommitResult{OperationID: prepared.Claim.Spec.OperationID, InitialPhase: initial,
		FinalPhase: appaccess.GatewayRebindCommitted, Disposition: appaccess.GatewayRebindDispositionCommit,
		TerminalReceiptDigest: receipt.Digest, SelectedCurrentAuthority: *confirmed.CurrentSource,
		FenceReleased: true}, nil
}

func (m *Manager) recoverGatewayRebindCommittedCurrentLocked(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository, driver gatewayRebindCrossStoreDriver,
	snapshot appaccess.GatewayRebindRecoverySnapshot, result GatewayRebindStartupRecoveryResult,
) (GatewayRebindStartupRecoveryResult, error) {
	if snapshot.CurrentSource == nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		return GatewayRebindStartupRecoveryResult{}, err
	}
	var attestation string
	if selection.Kind == gatewayCurrentSelectionRebind {
		attestation, err = driver.attestCommittedCurrentLocked(ctx, selection)
		if err != nil || !validSHA256(attestation) || selection.State == nil {
			return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
		}
		result.CurrentStateVersion = selection.State.Version
		result.CurrentStateRevision = selection.State.Revision
		result.CurrentStateDigest = selection.State.Digest
		result.CurrentAttestationDigest = attestation
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, confirmed) {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	confirmedSelection, err := m.selectGatewayCurrentLocked(ctx, confirmed)
	if err != nil || !sameGatewayCurrentSelection(selection, confirmedSelection) ||
		repository.CheckGatewayRebindFence(ctx) != nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	authority := *confirmed.CurrentSource
	result.SelectedCurrentAuthority = &authority
	result.FenceReleased = true
	return result, nil
}

func gatewayRebindStartupResultFromCommit(value GatewayRebindCommitResult,
	recovered bool,
) GatewayRebindStartupRecoveryResult {
	authority := value.SelectedCurrentAuthority
	return GatewayRebindStartupRecoveryResult{RebindHistoryPresent: true, Recovered: recovered,
		ActiveOperationID: value.OperationID, InitialActivePhase: value.InitialPhase,
		FinalActivePhase: value.FinalPhase, Disposition: value.Disposition,
		TerminalReceiptDigest: value.TerminalReceiptDigest, SelectedCurrentAuthority: &authority,
		FenceReleased: value.FenceReleased}
}

func (m *Manager) recoverGatewayRebindTerminalCurrentLocked(ctx context.Context,
	repository GatewayRebindStartupRecoveryRepository, driver gatewayRebindCrossStoreDriver,
	commit GatewayRebindCommitResult,
) (GatewayRebindStartupRecoveryResult, error) {
	seed := gatewayRebindStartupResultFromCommit(commit, true)
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active != nil {
		return GatewayRebindStartupRecoveryResult{}, gatewayRebindProposalError(ctx)
	}
	return m.recoverGatewayRebindCommittedCurrentLocked(ctx, repository, driver, snapshot, seed)
}
