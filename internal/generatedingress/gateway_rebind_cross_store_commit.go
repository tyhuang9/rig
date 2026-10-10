package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindCrossStoreRepository interface {
	gatewayRebindAdmissionRepository
	gatewayRebindTransitionRepository
}

type gatewayRebindTransitionRepository interface {
	gatewayRebindProposalRepository
	ApplyGatewayRebindTransition(context.Context, appaccess.GatewayRebindTransitionProof) (appaccess.GatewayRebindTransitionCommand, error)
}

type gatewayRebindTypedPhysicalResult struct {
	Disposition appaccess.GatewayRebindTerminalDisposition
	Last        gatewayRebindProgressRecord
	Resources   gatewayRebindFinalHandoverResourceBindings
	Proof       gatewayRebindFinalHandoverTerminalProof
}

type gatewayRebindTypedProgressAppender func(context.Context, gatewayRebindProgressRecord) error

type gatewayRebindPhysicalReconcileMode string

const (
	gatewayRebindPhysicalReconcileUndecided    gatewayRebindPhysicalReconcileMode = "undecided"
	gatewayRebindPhysicalReconcileForwardOnly  gatewayRebindPhysicalReconcileMode = "forward_only"
	gatewayRebindPhysicalReconcileRollbackOnly gatewayRebindPhysicalReconcileMode = "rollback_only"
)

// gatewayRebindPhysicalReconcileRequest makes the SQL recovery direction and
// immutable protected decision available before any physical effect. A commit
// receipt or database-committed SQL phase is always forward-only; a retained
// abort receipt is rollback-only. Undecided is valid only for a prepared
// attempt with no terminal receipt and permits the driver to choose a safely
// proved commit or rollback outcome.
type gatewayRebindPhysicalReconcileRequest struct {
	Attempt                gatewayRebindPreparedAttempt
	Mode                   gatewayRebindPhysicalReconcileMode
	SQLPhase               appaccess.GatewayRebindState
	RollbackAllowed        bool
	DatabaseCommitObserved bool
	Terminal               *gatewayRebindTerminalReceiptV2
}

func validGatewayRebindPhysicalReconcileRequest(value gatewayRebindPhysicalReconcileRequest) bool {
	attempt := value.Attempt
	if !validGatewayRebindPredecessorCheckpoint(attempt.Checkpoint) ||
		!validGatewayRebindProtectedIntentV2(attempt.Intent) ||
		!validGatewayRebindProgressRecord(attempt.Progress) ||
		attempt.Claim.Spec.OperationID != attempt.Intent.OperationID ||
		attempt.Claim.Spec.SuccessorProtectedGeneration != attempt.Intent.Generation ||
		attempt.Checkpoint.sourceRef() != attempt.Claim.Spec.Predecessor ||
		!reflect.DeepEqual(attempt.Intent.Claim, attempt.Claim) ||
		!gatewayRebindProgressMatchesIntentV2(attempt.Progress, attempt.Intent, nil) {
		return false
	}
	if value.Terminal != nil {
		receipt := *value.Terminal
		if !validGatewayRebindTerminalReceiptV2(receipt) || receipt.ProtectedIntentDigest != attempt.Intent.Digest ||
			receipt.OperationID != attempt.Claim.Spec.OperationID || receipt.Generation != attempt.Intent.Generation ||
			receipt.ClaimRequestDigest != attempt.Claim.RequestDigest || receipt.Predecessor != attempt.Claim.Spec.Predecessor {
			return false
		}
	}
	switch value.Mode {
	case gatewayRebindPhysicalReconcileUndecided:
		return value.SQLPhase == appaccess.GatewayRebindPrepared && value.RollbackAllowed &&
			!value.DatabaseCommitObserved && value.Terminal == nil
	case gatewayRebindPhysicalReconcileForwardOnly:
		return value.Terminal != nil && value.Terminal.Disposition == appaccess.GatewayRebindDispositionCommit &&
			(value.SQLPhase == appaccess.GatewayRebindPrepared ||
				value.SQLPhase == appaccess.GatewayRebindSuccessorReady ||
				(value.SQLPhase == appaccess.GatewayRebindDatabaseCommitted && value.DatabaseCommitObserved))
	case gatewayRebindPhysicalReconcileRollbackOnly:
		return value.Terminal != nil && value.Terminal.Disposition == appaccess.GatewayRebindDispositionAbort &&
			value.RollbackAllowed && !value.DatabaseCommitObserved &&
			(value.SQLPhase == appaccess.GatewayRebindPrepared || value.SQLPhase == appaccess.GatewayRebindSuccessorReady)
	default:
		return false
	}
}

// gatewayRebindCrossStoreDriver is private and replaceable only by package
// tests. Production uses the normalized physical adapter; every method runs
// while the effects lease, Manager mutex and gateway OS lock are held.
type gatewayRebindCrossStoreDriver interface {
	reconcileSuccessorLocked(context.Context, gatewayRebindPhysicalReconcileRequest,
		gatewayRebindTypedProgressAppender) (gatewayRebindTypedPhysicalResult, error)
	proveNoSuccessorEffectsLocked(context.Context, appaccess.GatewayRebindClaimV2,
		[]appaccess.GatewayRebindRosterEntryV2, gatewayRebindPredecessorCheckpoint) (gatewayRebindNoEffectAbortProof, error)
	attestCommittedCurrentLocked(context.Context, gatewayCurrentSelection) (string, error)
	confirmRollbackServingLocked(context.Context, gatewayRebindPhysicalReconcileRequest) error
	confirmForwardServingLocked(context.Context, gatewayRebindPhysicalReconcileRequest) error
	withdrawForwardSuccessorLocked(context.Context, gatewayRebindPhysicalReconcileRequest) error
}

type GatewayRebindCommitResult struct {
	OperationID              string
	InitialPhase             appaccess.GatewayRebindState
	FinalPhase               appaccess.GatewayRebindState
	Disposition              appaccess.GatewayRebindTerminalDisposition
	TerminalReceiptDigest    string
	SelectedCurrentAuthority appaccess.GatewayCurrentAuthorityRef
	FenceReleased            bool
}

func (m *Manager) gatewayRebindDriver() gatewayRebindCrossStoreDriver {
	if m != nil && m.gatewayRebindCrossStoreDriver != nil {
		return m.gatewayRebindCrossStoreDriver
	}
	return managerGatewayRebindCrossStoreDriver{manager: m}
}

// commitGatewayRebind is deliberately private. Public administrator actions
// are a later unit; tests and startup recovery exercise the same coordinator.
func (m *Manager) commitGatewayRebind(ctx context.Context, repository gatewayRebindCrossStoreRepository,
	input gatewayRebindCommitInput,
) (GatewayRebindCommitResult, error) {
	return m.commitGatewayRebindWithDriver(ctx, repository, input, m.gatewayRebindDriver())
}

func (m *Manager) commitGatewayRebindWithDriver(ctx context.Context, repository gatewayRebindCrossStoreRepository,
	input gatewayRebindCommitInput, driver gatewayRebindCrossStoreDriver,
) (result GatewayRebindCommitResult, resultErr error) {
	if m == nil || ctx == nil || repository == nil || driver == nil {
		return GatewayRebindCommitResult{}, &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return GatewayRebindCommitResult{}, gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRaw(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			m.gatewayRebindFailStopLatch().Store(true)
		}
		return GatewayRebindCommitResult{}, err
	}
	terminalSQL := false
	defer func() {
		if releaseGateway == nil && releaseEffects == nil {
			return
		}
		if terminalSQL {
			resultErr = m.releaseGatewayRebindTerminalLocks(releaseEffects, releaseGateway, resultErr)
		} else {
			if releaseGateway != nil {
				if releaseErr := releaseGateway(); releaseErr != nil {
					resultErr = &Error{Code: DiagnosticRouteUnresolved}
				}
			}
			if releaseEffects != nil {
				if releaseErr := releaseEffects(); releaseErr != nil {
					resultErr = &Error{Code: DiagnosticRouteUnresolved}
				}
			}
		}
		if resultErr != nil {
			result = GatewayRebindCommitResult{}
		}
	}()

	prepared, err := m.prepareGatewayRebindLocked(ctx, repository, input)
	if err != nil {
		abort, abortErr := m.abortGatewayRebindPreparedWithoutIntentLocked(ctx, repository, driver,
			input.Inspection.Spec.OperationID)
		terminalSQL = m.gatewayRebindCommitBarrierLatch().Load()
		if abortErr != nil {
			return GatewayRebindCommitResult{}, err
		}
		return abort, nil
	}
	result.OperationID, result.InitialPhase = prepared.Claim.Spec.OperationID, appaccess.GatewayRebindPrepared
	appendProgress := func(appendCtx context.Context, record gatewayRebindProgressRecord) error {
		store, storeErr := newGatewayRebindProgressStore(m.options.DataRoot, record.Generation,
			record.OperationID, record.Sequence)
		if storeErr != nil {
			return storeErr
		}
		return store.installExact(appendCtx, record)
	}
	request := gatewayRebindPhysicalReconcileRequest{
		Attempt: prepared, Mode: gatewayRebindPhysicalReconcileUndecided,
		SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true,
	}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	physical, err := driver.reconcileSuccessorLocked(ctx, request, appendProgress)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	if physical.Disposition == appaccess.GatewayRebindDispositionAbort {
		result, err = m.finishGatewayRebindRollbackAfterIntentLocked(ctx, repository, driver, prepared, physical.Last)
		terminalSQL = m.gatewayRebindCommitBarrierLatch().Load()
		return result, err
	}
	if physical.Disposition != appaccess.GatewayRebindDispositionCommit {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	// Completed physical history is a direction fence before receipt creation.
	// Keep ownership-only withdrawal armed through all later failures, while
	// retaining the exact intended receipt across uncertain publication.
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(driver.withdrawForwardSuccessorLocked(ctx, request), resultErr)
		}
	}()
	if physical.Last.Sequence != 17 || physical.Last.Phase != gatewayRebindProgressHandoverCommitted ||
		physical.Last.TypedEffect == nil || physical.Last.TypedEffect.Resources == nil ||
		physical.Last.TypedEffect.PhysicalProof == nil ||
		!reflect.DeepEqual(*physical.Last.TypedEffect.Resources, physical.Resources) ||
		!reflect.DeepEqual(*physical.Last.TypedEffect.PhysicalProof, physical.Proof) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	receipt, err := newGatewayRebindCommitTerminalV2(prepared.Intent, physical.Last, physical.Resources,
		physical.Proof, gatewayRebindTimeStrictlyAfter(m.gatewayRebindProgressTime(), physical.Last.OccurredAt))
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	terminalStore, err := newGatewayRebindTerminalStoreV2(m.options.DataRoot, receipt.Generation, receipt.OperationID)
	if err != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	request.Mode, request.Terminal = gatewayRebindPhysicalReconcileForwardOnly, &receipt
	if installErr := terminalStore.installExact(ctx, receipt); installErr != nil {
		return GatewayRebindCommitResult{}, installErr
	}

	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	proof, err := gatewayRebindTransitionProofV2(snapshot, prepared.Claim, receipt, appaccess.GatewayRebindSuccessorReady,
		gatewayCurrentRouteState{}, nil, "")
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	request = gatewayRebindForwardConfirmationRequest(prepared, receipt, snapshot)
	if err := driver.confirmForwardServingLocked(ctx, request); err != nil {
		return GatewayRebindCommitResult{}, err
	}
	if _, err = applyGatewayRebindTransitionReconciled(ctx, repository, proof); err != nil {
		return GatewayRebindCommitResult{}, err
	}
	transfers, err := newGatewayRebindTransfersV2(prepared.Intent, receipt, prepared.Checkpoint)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	baseline, err := newGatewayCurrentRouteBaselineFromV2Terminal(prepared.Intent, receipt, prepared.Checkpoint, transfers)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	currentStore, err := newGatewayCurrentRouteStateStore(m.options.DataRoot, baseline.Lineage)
	if err != nil || currentStore.installBaseline(baseline) != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}

	snapshot, err = repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	proof, err = gatewayRebindTransitionProofV2(snapshot, prepared.Claim, receipt,
		appaccess.GatewayRebindDatabaseCommitted, baseline, transfers, "")
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	request = gatewayRebindForwardConfirmationRequest(prepared, receipt, snapshot)
	if err := driver.confirmForwardServingLocked(ctx, request); err != nil {
		return GatewayRebindCommitResult{}, err
	}
	if _, err = applyGatewayRebindTransitionReconciled(ctx, repository, proof); err != nil {
		return GatewayRebindCommitResult{}, err
	}

	snapshot, err = repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil || selection.Lineage != baseline.Lineage || selection.State == nil ||
		!reflect.DeepEqual(*selection.State, baseline) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	attestation, err := driver.attestCommittedCurrentLocked(ctx, selection)
	if err != nil || !validSHA256(attestation) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	// Physical proof is only evidence for the exact state that was observed.
	// Re-read both stores before releasing the SQL fence so an ordinary current
	// route write cannot be acknowledged by a stale local attestation.
	freshSnapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, freshSnapshot) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	freshSelection, err := m.selectGatewayCurrentLocked(ctx, freshSnapshot)
	if err != nil || !sameGatewayCurrentSelection(selection, freshSelection) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	snapshot = freshSnapshot
	request = gatewayRebindForwardConfirmationRequest(prepared, receipt, snapshot)
	if err := driver.confirmForwardServingLocked(ctx, request); err != nil {
		return GatewayRebindCommitResult{}, err
	}
	if !m.gatewayRebindCommitBarrierLatch().CompareAndSwap(false, true) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	terminalSQL = true
	proof, err = gatewayRebindTransitionProofV2(snapshot, prepared.Claim, receipt,
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
		*confirmed.CurrentSource != gatewayCurrentAuthority(baseline.Lineage) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	confirmedSelection, selectErr := m.selectGatewayCurrentLocked(ctx, confirmed)
	if selectErr != nil || !sameGatewayCurrentSelection(selection, confirmedSelection) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	result.FinalPhase, result.Disposition = appaccess.GatewayRebindCommitted, appaccess.GatewayRebindDispositionCommit
	result.TerminalReceiptDigest = receipt.Digest
	result.SelectedCurrentAuthority = *confirmed.CurrentSource
	result.FenceReleased = true
	return result, nil
}

func (m *Manager) finishGatewayRebindRollbackAfterIntentLocked(ctx context.Context,
	repository gatewayRebindTransitionRepository, driver gatewayRebindCrossStoreDriver, prepared gatewayRebindPreparedAttempt,
	last gatewayRebindProgressRecord,
) (GatewayRebindCommitResult, error) {
	if last.Phase != gatewayRebindProgressHandoverRolledBack || last.TypedRollback == nil ||
		last.TypedRollback.PhysicalProof == nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		snapshot.Active.Claim.V2.Spec.OperationID != prepared.Claim.Spec.OperationID ||
		(snapshot.Phase != appaccess.GatewayRebindPrepared && snapshot.Phase != appaccess.GatewayRebindSuccessorReady) ||
		!snapshot.RollbackAllowed || snapshot.DatabaseCommitObserved || snapshot.DatabaseCommittedEvent != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: prepared, Mode: gatewayRebindPhysicalReconcileUndecided,
		SQLPhase: snapshot.Phase, RollbackAllowed: true}
	if err := driver.confirmRollbackServingLocked(ctx, request); err != nil {
		return GatewayRebindCommitResult{}, err
	}
	receipt, err := newGatewayRebindRollbackTerminalV2(prepared.Intent, last, prepared.Checkpoint,
		gatewayRebindTimeStrictlyAfter(m.gatewayRebindProgressTime(), last.OccurredAt))
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	store, err := newGatewayRebindTerminalStoreV2(m.options.DataRoot, receipt.Generation, receipt.OperationID)
	if err != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	if installErr := store.installExact(ctx, receipt); installErr != nil {
		// A failed acknowledgement may still have installed the exact immutable
		// receipt. Select its actual form before rechecking serving authority.
		if retained, readErr := store.load(); readErr == nil && reflect.DeepEqual(retained, receipt) {
			request.Mode, request.Terminal = gatewayRebindPhysicalReconcileRollbackOnly, &retained
		}
		if confirmErr := driver.confirmRollbackServingLocked(ctx, request); confirmErr != nil {
			return GatewayRebindCommitResult{}, confirmErr
		}
		return GatewayRebindCommitResult{}, installErr
	}
	transition, err := gatewayRebindTransitionProofV2(snapshot, prepared.Claim, receipt,
		appaccess.GatewayRebindRolledBack, gatewayCurrentRouteState{}, nil, "")
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	request.Mode, request.Terminal = gatewayRebindPhysicalReconcileRollbackOnly, &receipt
	if err := driver.confirmRollbackServingLocked(ctx, request); err != nil {
		return GatewayRebindCommitResult{}, err
	}
	if !m.gatewayRebindCommitBarrierLatch().CompareAndSwap(false, true) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	command, err := applyGatewayRebindTransitionReconciled(ctx, repository, transition)
	if err != nil {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, err
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	expectedAuthority := gatewayCurrentAuthority(receipt.Predecessor.Lineage)
	if err != nil || confirmed.Active != nil || confirmed.CurrentSource == nil ||
		*confirmed.CurrentSource != expectedAuthority || !gatewayRebindSnapshotRetainsCommand(confirmed, command) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	retainedReceipt, err := store.load()
	if err != nil || !reflect.DeepEqual(retainedReceipt, receipt) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	return GatewayRebindCommitResult{
		OperationID: prepared.Claim.Spec.OperationID, InitialPhase: snapshot.Phase,
		FinalPhase: appaccess.GatewayRebindRolledBack, Disposition: appaccess.GatewayRebindDispositionAbort,
		TerminalReceiptDigest: receipt.Digest, SelectedCurrentAuthority: *confirmed.CurrentSource, FenceReleased: true,
	}, nil
}

func (m *Manager) releaseGatewayRebindTerminalLocks(releaseEffects, releaseGateway func() error, prior error) error {
	failed := prior
	if releaseEffects != nil {
		if err := releaseEffects(); err != nil {
			failed = errors.Join(failed, &Error{Code: DiagnosticRouteUnresolved})
			m.gatewayRebindFailStopLatch().Store(true)
		}
	}
	if releaseGateway != nil {
		if err := releaseGateway(); err != nil {
			failed = errors.Join(failed, &Error{Code: DiagnosticRouteUnresolved})
			m.gatewayRebindFailStopLatch().Store(true)
		}
	}
	if failed == nil && !m.gatewayRebindFailStopLatch().Load() {
		m.gatewayRebindCommitBarrierLatch().Store(false)
	}
	return failed
}

func (m *Manager) abortGatewayRebindPreparedWithoutIntentLocked(ctx context.Context,
	repository gatewayRebindTransitionRepository, driver gatewayRebindCrossStoreDriver, operationID string,
) (GatewayRebindCommitResult, error) {
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active == nil || snapshot.Active.Claim.V2 == nil ||
		snapshot.Active.Claim.V2.Spec.OperationID != operationID || snapshot.Phase != appaccess.GatewayRebindPrepared ||
		!snapshot.RollbackAllowed || snapshot.DatabaseCommitObserved {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	claim := *snapshot.Active.Claim.V2
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	var checkpoint *gatewayRebindPredecessorCheckpoint
	for index := range history.Checkpoints {
		candidate := history.Checkpoints[index].Checkpoint
		if candidate.Generation == claim.Spec.SuccessorProtectedGeneration && candidate.OperationID == operationID {
			copy := candidate
			checkpoint = &copy
		}
	}
	for _, intent := range history.IntentsV2 {
		if intent.Intent.OperationID == operationID {
			return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
		}
	}
	for _, record := range history.Progress {
		if record.Record.OperationID == operationID {
			return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
		}
	}
	if checkpoint == nil || checkpoint.sourceRef() != claim.Spec.Predecessor {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	proofValue, err := driver.proveNoSuccessorEffectsLocked(ctx, claim, snapshot.Active.RosterV2, *checkpoint)
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	freshSnapshot, snapshotErr := repository.GatewayRebindRecoverySnapshot(ctx)
	freshHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterFiles, afterFilesErr := readGatewayHistorySnapshotMode(m.store, true)
	freshCheckpoint, checkpointErr := gatewayRebindActiveCheckpoint(freshHistory, claim)
	if snapshotErr != nil || !reflect.DeepEqual(snapshot, freshSnapshot) || historyErr != nil ||
		!sameGatewayRebindCurrentHistory(history, freshHistory) || afterFilesErr != nil ||
		!sameGatewayHistorySnapshot(beforeFiles, afterFiles) || checkpointErr != nil ||
		!reflect.DeepEqual(*checkpoint, freshCheckpoint) || ctx.Err() != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	receipt, err := newGatewayRebindNoEffectAbortTerminalV2(claim, freshSnapshot.Active.RosterV2,
		freshSnapshot.Active.RuntimeHeads, freshCheckpoint,
		proofValue, gatewayRebindTimeStrictlyAfter(m.gatewayRebindProgressTime(), proofValue.CreatedAt))
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	store, err := newGatewayRebindTerminalStoreV2(m.options.DataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(ctx, receipt) != nil {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	confirmedSnapshot, snapshotErr := repository.GatewayRebindRecoverySnapshot(ctx)
	confirmedHistory, historyErr := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	confirmedReceipt, terminalErr := gatewayRebindActiveTerminalV2(confirmedHistory, claim)
	confirmedCheckpoint, checkpointErr := gatewayRebindActiveCheckpoint(confirmedHistory, claim)
	if snapshotErr != nil || !reflect.DeepEqual(freshSnapshot, confirmedSnapshot) || historyErr != nil ||
		terminalErr != nil || confirmedReceipt == nil || !reflect.DeepEqual(receipt, *confirmedReceipt) ||
		checkpointErr != nil || !reflect.DeepEqual(freshCheckpoint, confirmedCheckpoint) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	if !m.gatewayRebindCommitBarrierLatch().CompareAndSwap(false, true) {
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	transition, err := gatewayRebindTransitionProofV2(confirmedSnapshot, claim, receipt, appaccess.GatewayRebindRolledBack,
		gatewayCurrentRouteState{}, nil, "")
	if err != nil {
		return GatewayRebindCommitResult{}, err
	}
	command, err := applyGatewayRebindTransitionReconciled(ctx, repository, transition)
	if err != nil {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, err
	}
	confirmed, err := repository.GatewayRebindRecoverySnapshot(ctx)
	expectedAuthority := gatewayCurrentAuthority(receipt.Predecessor.Lineage)
	if err != nil || confirmed.Active != nil || confirmed.CurrentSource == nil ||
		*confirmed.CurrentSource != expectedAuthority || !gatewayRebindSnapshotRetainsCommand(confirmed, command) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	retainedReceipt, err := store.load()
	if err != nil || !reflect.DeepEqual(retainedReceipt, receipt) {
		m.gatewayRebindFailStopLatch().Store(true)
		return GatewayRebindCommitResult{}, gatewayRebindProposalError(ctx)
	}
	return GatewayRebindCommitResult{OperationID: operationID, InitialPhase: appaccess.GatewayRebindPrepared,
		FinalPhase: appaccess.GatewayRebindRolledBack, Disposition: appaccess.GatewayRebindDispositionAbort,
		TerminalReceiptDigest: receipt.Digest, SelectedCurrentAuthority: *confirmed.CurrentSource, FenceReleased: true}, nil
}

func gatewayRebindSnapshotRetainsCommand(snapshot appaccess.GatewayRebindRecoverySnapshot,
	command appaccess.GatewayRebindTransitionCommand,
) bool {
	matches := 0
	for _, history := range snapshot.History {
		for _, retained := range history.Commands {
			if reflect.DeepEqual(retained, command) {
				matches++
			}
		}
	}
	return matches == 1
}

func gatewayRebindTransitionProofV2(snapshot appaccess.GatewayRebindRecoverySnapshot,
	claim appaccess.GatewayRebindClaimV2, receipt gatewayRebindTerminalReceiptV2,
	next appaccess.GatewayRebindState, baseline gatewayCurrentRouteState,
	transfers []appaccess.GatewayRebindAllocationTransfer, attestation string,
) (appaccess.GatewayRebindTransitionProof, error) {
	if snapshot.Active == nil || snapshot.Active.Claim.V2 == nil || snapshot.CurrentSource == nil ||
		snapshot.Active.Claim.V2.Spec.OperationID != claim.Spec.OperationID || snapshot.Phase == "" {
		return appaccess.GatewayRebindTransitionProof{}, errors.New("invalid active rebind transition snapshot")
	}
	active := snapshot.Active.Claim.V2
	proof := appaccess.GatewayRebindTransitionProof{
		Version: appaccess.GatewayRebindTransitionVersionV1, Purpose: appaccess.GatewayRebindTransitionPurpose,
		OperationID: claim.Spec.OperationID, ClaimRequestDigest: claim.RequestDigest,
		ClaimSpecDigest: claim.RebindApproval.SpecDigest, ExpectedState: snapshot.Phase,
		ExpectedSequence: active.StateSequence, NextState: next,
		ExpectedHeadRevisionID:     snapshot.CurrentSource.ProfileRevisionID,
		ExpectedHeadRevisionNumber: snapshot.CurrentSource.ProfileRevisionNumber,
		ExpectedHeadSpecDigest:     snapshot.CurrentSource.ProfileSpecDigest,
		ProtectedGeneration:        receipt.Generation, ProtectedPhase: receipt.ProtectedPhase,
		ProtectedRecordSequence: receipt.ProtectedRecordSequence, ProtectedRecordDigest: receipt.ProtectedRecordDigest,
		TerminalReceiptDigest: receipt.Digest, TerminalDisposition: receipt.Disposition,
		PredecessorCheckpointDigest: receipt.Predecessor.PredecessorCheckpointDigest,
		SourceStateVersion:          receipt.Predecessor.SourceStateVersion,
		SourceStateRevision:         receipt.Predecessor.SourceStateRevision, SourceStateDigest: receipt.Predecessor.SourceStateDigest,
		LocalAttestationDigest: attestation,
	}
	if next == appaccess.GatewayRebindDatabaseCommitted {
		proof.SuccessorOperationalStateVersion, proof.SuccessorOperationalStateRevision = baseline.Version, baseline.Revision
		proof.SuccessorOperationalStateDigest, proof.TransferManifestDigest = baseline.Digest, baseline.TransferManifestDigest
		proof.Transfers = append([]appaccess.GatewayRebindAllocationTransfer(nil), transfers...)
	}
	return proof, nil
}

func applyGatewayRebindTransitionReconciled(ctx context.Context, repository gatewayRebindTransitionRepository,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	if proof.NextState == appaccess.GatewayRebindDatabaseCommitted && proof.Transfers == nil {
		proof.Transfers = []appaccess.GatewayRebindAllocationTransfer{}
	}
	command, err := repository.ApplyGatewayRebindTransition(ctx, proof)
	if err == nil {
		if gatewayRebindTransitionCommandMatchesProof(command, proof) {
			return command, nil
		}
		return appaccess.GatewayRebindTransitionCommand{}, errors.New("gateway rebind transition command readback mismatch")
	}
	snapshot, readErr := repository.GatewayRebindRecoverySnapshot(ctx)
	if readErr != nil {
		return appaccess.GatewayRebindTransitionCommand{}, err
	}
	for _, history := range snapshot.History {
		operationID, idErr := gatewayRebindHistoryOperationID(history)
		if idErr != nil || operationID != proof.OperationID {
			continue
		}
		for _, retained := range history.Commands {
			if gatewayRebindTransitionCommandMatchesProof(retained, proof) {
				return retained, nil
			}
		}
	}
	return appaccess.GatewayRebindTransitionCommand{}, err
}

func gatewayRebindTransitionCommandMatchesProof(command appaccess.GatewayRebindTransitionCommand,
	proof appaccess.GatewayRebindTransitionProof,
) bool {
	payload, err := json.Marshal(proof)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(payload)
	return command.OperationID == proof.OperationID && command.Sequence == proof.ExpectedSequence+1 &&
		command.PreviousState == proof.ExpectedState && command.PreviousSequence == proof.ExpectedSequence &&
		command.NextState == proof.NextState && command.Purpose == proof.Purpose &&
		command.ProtectedRecordDigest == proof.ProtectedRecordDigest &&
		command.TerminalReceiptDigest == proof.TerminalReceiptDigest &&
		command.LocalAttestationDigest == proof.LocalAttestationDigest &&
		command.TerminalDisposition == proof.TerminalDisposition &&
		command.CanonicalPayload == string(payload) && command.CommandDigest == hex.EncodeToString(sum[:]) &&
		!command.CreatedAt.IsZero()
}
