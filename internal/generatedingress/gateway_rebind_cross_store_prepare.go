package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindCommitInput struct {
	Inspection        GatewayRebindProposalInspection
	RebindApproval    appaccess.Approval
	ConfigureApproval appaccess.Approval
}

type gatewayRebindAdmissionRepository interface {
	gatewayRebindProposalRepository
	ClaimGatewayRebindV2(context.Context, appaccess.GatewayRebindPreclaimProposalV2) (appaccess.GatewayRebindClaimV2, bool, error)
}

type gatewayRebindPreparedAttempt struct {
	Claim      appaccess.GatewayRebindClaimV2
	Checkpoint gatewayRebindPredecessorCheckpoint
	Intent     gatewayRebindProtectedIntentV2
	Progress   gatewayRebindProgressRecord
}

type gatewayRebindAdmissionCandidate struct {
	Snapshot   appaccess.GatewayRebindRecoverySnapshot
	Selection  gatewayCurrentSelection
	Checkpoint gatewayRebindPredecessorCheckpoint
	Proposal   appaccess.GatewayRebindPreclaimProposalV2
}

// prepareGatewayRebindLocked is the claim-to-protected admission core. Its
// caller holds the deployment-effects lease, Manager mutex, and gateway OS
// lock. ClaimGatewayRebindV2 commits the short SQL prepared transaction before
// this function installs the deterministic checkpoint, typed intent, and
// first progress record. A failure after that SQL commit deliberately leaves
// the fence active for startup recovery.
func (m *Manager) prepareGatewayRebindLocked(ctx context.Context, repository gatewayRebindAdmissionRepository,
	input gatewayRebindCommitInput,
) (gatewayRebindPreparedAttempt, error) {
	if m == nil || ctx == nil || repository == nil {
		return gatewayRebindPreparedAttempt{}, &Error{Code: DiagnosticValidationFailed}
	}
	candidate, err := m.gatewayRebindAdmissionCandidateLocked(ctx, repository, input)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	claim, err := m.claimGatewayRebindAdmissionLocked(ctx, repository, candidate)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	return m.installGatewayRebindPreparedProtectedLocked(ctx, claim, input.Inspection.Roster,
		input.Inspection.RuntimeHeads, candidate.Checkpoint, m.gatewayRebindProgressTime())
}

// prepareGatewayRebindStrictLocked is the request-bound admission path. Its
// caller owns the deployment-effects lease, Manager mutex, and gateway OS
// lock. Both the physical proof reader and live request/session callback are
// mandatory; neither has a permissive default.
func (m *Manager) prepareGatewayRebindStrictLocked(ctx context.Context,
	repository gatewayRebindAdmissionRepository, input gatewayRebindCommitInput,
	proofReader gatewayRebindAdmissionProofReader, liveSession func(context.Context) error,
) (gatewayRebindPreparedAttempt, error) {
	if m == nil || ctx == nil || repository == nil || proofReader == nil || liveSession == nil {
		return gatewayRebindPreparedAttempt{}, &Error{Code: DiagnosticValidationFailed}
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	candidate, err := m.gatewayRebindAdmissionCandidateLocked(ctx, repository, input)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	target := gatewayRebindAdmissionTarget(input.Inspection, candidate.Checkpoint)
	preclaim, err := proofReader(ctx, target, candidate.Selection)
	if err != nil || !gatewayRebindAdmissionProofMatchesTarget(ctx, preclaim, target, candidate.Selection) {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	claim, err := m.claimGatewayRebindAdmissionLocked(ctx, repository, candidate)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	network, err := m.attestGatewayRebindClaimAdmissionLocked(ctx, repository, candidate,
		claim, proofReader, preclaim)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	if err := checkGatewayRebindAdmissionSession(ctx, liveSession); err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	return m.installGatewayRebindPreparedProtectedWithNetworkLocked(ctx, claim,
		input.Inspection.Roster, input.Inspection.RuntimeHeads, candidate.Checkpoint,
		network, m.gatewayRebindProgressTime())
}

func checkGatewayRebindAdmissionSession(ctx context.Context, liveSession func(context.Context) error) error {
	if ctx == nil || ctx.Err() != nil || liveSession == nil {
		return gatewayRebindProposalError(ctx)
	}
	if err := liveSession(ctx); err != nil || ctx.Err() != nil {
		return gatewayRebindProposalError(ctx)
	}
	return nil
}

func (m *Manager) gatewayRebindAdmissionCandidateLocked(ctx context.Context,
	repository gatewayRebindAdmissionRepository, input gatewayRebindCommitInput,
) (gatewayRebindAdmissionCandidate, error) {
	observed, err := m.inspectGatewayRebindProposalLocked(ctx, repository, GatewayRebindProposalInput{
		OperationID:                    input.Inspection.Spec.OperationID,
		SuccessorProfileRevisionID:     input.Inspection.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    input.Inspection.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               input.Inspection.Spec.SuccessorProfile,
	})
	if err != nil || !reflect.DeepEqual(observed, input.Inspection) {
		return gatewayRebindAdmissionCandidate{}, gatewayRebindProposalError(ctx)
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active != nil {
		return gatewayRebindAdmissionCandidate{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		return gatewayRebindAdmissionCandidate{}, gatewayRebindProposalError(ctx)
	}
	checkpoint, err := gatewayRebindCheckpointForSelection(input.Inspection.ProtectedGeneration,
		input.Inspection.Spec.OperationID, selection)
	if err != nil || checkpoint.Digest != input.Inspection.PredecessorCheckpointDigest ||
		checkpoint.sourceRef() != input.Inspection.Spec.Predecessor ||
		input.Inspection.Spec.SuccessorProtectedGeneration != checkpoint.Generation {
		return gatewayRebindAdmissionCandidate{}, gatewayRebindProposalError(ctx)
	}
	return gatewayRebindAdmissionCandidate{Snapshot: snapshot, Selection: selection, Checkpoint: checkpoint,
		Proposal: appaccess.GatewayRebindPreclaimProposalV2{
			Spec: input.Inspection.Spec, RebindApproval: input.RebindApproval,
			ConfigureApproval: input.ConfigureApproval, Roster: input.Inspection.Roster,
			RuntimeHeads: input.Inspection.RuntimeHeads,
		}}, nil
}

func (m *Manager) claimGatewayRebindAdmissionLocked(ctx context.Context,
	repository gatewayRebindAdmissionRepository, candidate gatewayRebindAdmissionCandidate,
) (appaccess.GatewayRebindClaimV2, error) {
	claim, _, err := repository.ClaimGatewayRebindV2(ctx, candidate.Proposal)
	if err != nil {
		return appaccess.GatewayRebindClaimV2{}, gatewayRebindProposalError(ctx)
	}
	if m.gatewayRebindAfterClaim != nil {
		if err := m.gatewayRebindAfterClaim(ctx, claim); err != nil {
			return appaccess.GatewayRebindClaimV2{}, gatewayRebindProposalError(ctx)
		}
	}
	return claim, nil
}

func (m *Manager) recoverGatewayRebindPreparedAdmissionLocked(ctx context.Context,
	repository gatewayRebindProposalRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
) (gatewayRebindPreparedAttempt, error) {
	return m.recoverGatewayRebindPreparedAdmissionWithProofLocked(ctx, repository, snapshot,
		m.readGatewayRebindAdmissionPhysicalLocked)
}

func (m *Manager) recoverGatewayRebindPreparedAdmissionWithProofLocked(ctx context.Context,
	repository gatewayRebindProposalRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
	proofReader gatewayRebindAdmissionProofReader,
) (gatewayRebindPreparedAttempt, error) {
	if m == nil || ctx == nil || repository == nil || snapshot.Active == nil ||
		proofReader == nil ||
		snapshot.Active.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 ||
		snapshot.Active.Claim.V2 == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
		!snapshot.RollbackAllowed || snapshot.DatabaseCommitObserved || snapshot.DatabaseCommittedEvent != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	claim := *snapshot.Active.Claim.V2
	if claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		claim.Spec.SuccessorProtectedGeneration == 0 {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	checkpoint, err := gatewayRebindCheckpointForSelection(claim.Spec.SuccessorProtectedGeneration,
		claim.Spec.OperationID, selection)
	if err != nil || checkpoint.sourceRef() != claim.Spec.Predecessor {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	if replay, complete, replayErr := m.loadGatewayRebindPreparedReplayLocked(ctx, claim,
		snapshot.Active.RosterV2, snapshot.Active.RuntimeHeads, checkpoint); replayErr != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	} else if complete {
		return replay, nil
	}
	network, err := m.attestGatewayRebindPreparedA0Locked(ctx, repository, snapshot,
		claim, selection, checkpoint, proofReader)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	return m.installGatewayRebindPreparedProtectedWithNetworkLocked(ctx, claim,
		snapshot.Active.RosterV2, snapshot.Active.RuntimeHeads, checkpoint,
		network, m.gatewayRebindProgressTime())
}

func (m *Manager) gatewayRebindProgressTime() time.Time {
	if m != nil && m.gatewayRebindClock != nil {
		return m.gatewayRebindClock().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) installGatewayRebindPreparedProtectedLocked(ctx context.Context,
	claim appaccess.GatewayRebindClaimV2, roster []appaccess.GatewayRebindRosterEntryV2,
	runtimeHeads []appaccess.GatewayRebindRuntimeHead,
	checkpoint gatewayRebindPredecessorCheckpoint, occurredAt time.Time,
) (gatewayRebindPreparedAttempt, error) {
	if replay, complete, err := m.loadGatewayRebindPreparedReplayLocked(ctx, claim, roster,
		runtimeHeads, checkpoint); err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	} else if complete {
		return replay, nil
	}
	network, err := m.observeGatewayRebindV2SuccessorNetworkLocked(ctx, claim)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	return m.installGatewayRebindPreparedProtectedWithNetworkLocked(ctx, claim, roster,
		runtimeHeads, checkpoint, network, occurredAt)
}

func (m *Manager) installGatewayRebindPreparedProtectedWithNetworkLocked(ctx context.Context,
	claim appaccess.GatewayRebindClaimV2, roster []appaccess.GatewayRebindRosterEntryV2,
	runtimeHeads []appaccess.GatewayRebindRuntimeHead, checkpoint gatewayRebindPredecessorCheckpoint,
	network gatewayRebindSuccessorNetworkObservation, occurredAt time.Time,
) (gatewayRebindPreparedAttempt, error) {
	if !occurredAt.After(claim.CreatedAt) {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	// Constructing the complete intent validates that the supplied network is
	// claim-bound before the first protected file can be created.
	intent, err := newGatewayRebindProtectedIntentV2(claim, roster, runtimeHeads, checkpoint, network)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	if replay, complete, replayErr := m.loadGatewayRebindPreparedReplayLocked(ctx, claim,
		roster, runtimeHeads, checkpoint); replayErr != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	} else if complete {
		if !reflect.DeepEqual(replay.Intent, intent) {
			return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
		}
		return replay, nil
	}
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(m.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil || ensureGatewayRebindCheckpoint(checkpointStore, checkpoint) != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	if m.gatewayRebindAfterPreparedCheckpoint != nil {
		if err := m.gatewayRebindAfterPreparedCheckpoint(ctx); err != nil {
			return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
		}
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(m.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err == nil {
		if installedIntent, loadErr := intentStore.load(); loadErr == nil {
			if !reflect.DeepEqual(installedIntent, intent) {
				return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
			}
			progressStore, progressStoreErr := newGatewayRebindProgressStore(m.options.DataRoot,
				installedIntent.Generation, installedIntent.OperationID, 1)
			if progressStoreErr == nil {
				if installedProgress, progressErr := progressStore.load(); progressErr == nil &&
					gatewayRebindProgressMatchesIntentV2(installedProgress, installedIntent, nil) {
					return gatewayRebindPreparedAttempt{Claim: claim, Checkpoint: checkpoint,
						Intent: installedIntent, Progress: installedProgress}, nil
				}
			}
		}
	}
	intentStore, err = newGatewayRebindProtectedIntentV2Store(m.options.DataRoot, intent.Generation, intent.OperationID)
	if err != nil || ensureGatewayRebindIntentV2(intentStore, intent) != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	if m.gatewayRebindAfterPreparedIntent != nil {
		if err := m.gatewayRebindAfterPreparedIntent(ctx); err != nil {
			return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
		}
	}
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	progress, loadErr := progressStore.load()
	if loadErr == nil {
		if !gatewayRebindProgressMatchesIntentV2(progress, intent, nil) {
			return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
		}
	} else {
		progress, err = newGatewayRebindSuccessorIntentProgressV2(intent, occurredAt)
		if err != nil || progressStore.installExact(ctx, progress) != nil {
			return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
		}
	}
	return gatewayRebindPreparedAttempt{Claim: claim, Checkpoint: checkpoint, Intent: intent, Progress: progress}, nil
}

func (m *Manager) loadGatewayRebindPreparedReplayLocked(ctx context.Context,
	claim appaccess.GatewayRebindClaimV2, roster []appaccess.GatewayRebindRosterEntryV2,
	runtimeHeads []appaccess.GatewayRebindRuntimeHead, checkpoint gatewayRebindPredecessorCheckpoint,
) (gatewayRebindPreparedAttempt, bool, error) {
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(m.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, false, err
	}
	installedCheckpoint, err := checkpointStore.load()
	if err != nil {
		return gatewayRebindPreparedAttempt{}, false, nil
	}
	if !reflect.DeepEqual(installedCheckpoint, checkpoint) {
		return gatewayRebindPreparedAttempt{}, false, errors.New("generated ingress rebind predecessor checkpoint does not match prepared claim")
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(m.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, false, err
	}
	intent, err := intentStore.load()
	if err != nil {
		return gatewayRebindPreparedAttempt{}, false, nil
	}
	if intent.Claim.RequestDigest != claim.RequestDigest || intent.Predecessor != checkpoint.sourceRef() ||
		!sameGatewayRebindRosterV2(intent.Roster, roster) ||
		!sameGatewayRebindRuntimeHeads(intent.RuntimeHeads, runtimeHeads) {
		return gatewayRebindPreparedAttempt{}, false, errors.New("generated ingress typed rebind intent does not match prepared claim")
	}
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot,
		intent.Generation, intent.OperationID, 1)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, false, err
	}
	progress, err := progressStore.load()
	if err != nil {
		return gatewayRebindPreparedAttempt{}, false, nil
	}
	if !gatewayRebindProgressMatchesIntentV2(progress, intent, nil) {
		return gatewayRebindPreparedAttempt{}, false, errors.New("generated ingress rebind progress does not match prepared intent")
	}
	if ctx == nil || ctx.Err() != nil {
		return gatewayRebindPreparedAttempt{}, false, gatewayRebindProposalError(ctx)
	}
	return gatewayRebindPreparedAttempt{Claim: claim, Checkpoint: checkpoint,
		Intent: intent, Progress: progress}, true, nil
}

func sameGatewayRebindRosterV2(left, right []appaccess.GatewayRebindRosterEntryV2) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !reflect.DeepEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func ensureGatewayRebindCheckpoint(store *gatewayRebindPredecessorCheckpointStore,
	value gatewayRebindPredecessorCheckpoint,
) error {
	if installed, err := store.load(); err == nil {
		if reflect.DeepEqual(installed, value) {
			return nil
		}
		return errors.New("generated ingress rebind predecessor checkpoint does not match prepared claim")
	}
	return store.installExact(value)
}

func ensureGatewayRebindIntentV2(store *gatewayRebindProtectedIntentV2Store,
	value gatewayRebindProtectedIntentV2,
) error {
	if installed, err := store.load(); err == nil {
		if reflect.DeepEqual(installed, value) {
			return nil
		}
		return errors.New("generated ingress typed rebind intent does not match prepared claim")
	}
	return store.installExact(value)
}

func (m *Manager) observeGatewayRebindV2SuccessorNetworkLocked(ctx context.Context,
	claim appaccess.GatewayRebindClaimV2,
) (gatewayRebindSuccessorNetworkObservation, error) {
	if m.gatewayRebindV2NetworkObserver != nil {
		return m.gatewayRebindV2NetworkObserver(ctx, claim)
	}
	inventory := managerGatewayV2DockerInventory{manager: m}
	reads := gatewayRebindSuccessorPreflightReads{
		network: gatewayV2ProductionNetworkPlanReads(m), dockerIDs: inventory.listNetworkIDs,
	}
	first, err := readGatewayRebindV2SuccessorNetwork(ctx, claim, reads)
	if err != nil {
		return gatewayRebindSuccessorNetworkObservation{}, err
	}
	second, err := readGatewayRebindV2SuccessorNetwork(ctx, claim, reads)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindSuccessorNetworkObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	return newGatewayRebindSuccessorNetworkObservation(first)
}

func readGatewayRebindV2SuccessorNetwork(ctx context.Context, claim appaccess.GatewayRebindClaimV2,
	reads gatewayRebindSuccessorPreflightReads,
) (gatewayRebindSuccessorPreflightObservation, error) {
	profile := gatewayProfileBinding{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd,
	}
	if ctx == nil || ctx.Err() != nil || claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	value, err := readGatewayRebindSuccessorPhysical(ctx, gatewayRebindAdmissionSuccessorTarget{
		OperationID: claim.Spec.OperationID, ProfileOperationID: claim.Spec.SuccessorProfileOperationID,
		Profile: profile,
	}, reads)
	if err != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	value.result.ClaimRequestDigest = claim.RequestDigest
	value.result.Profile.RequestDigest = claim.SuccessorProfileRequestDigest
	value.result.Profile.ApprovedBy = claim.ConfigureApproval.ActorID
	return value, nil
}
