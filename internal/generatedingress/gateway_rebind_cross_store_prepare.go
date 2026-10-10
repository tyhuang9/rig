package generatedingress

import (
	"context"
	"errors"
	"net/netip"
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
	observed, err := m.inspectGatewayRebindProposalLocked(ctx, repository, GatewayRebindProposalInput{
		OperationID:                    input.Inspection.Spec.OperationID,
		SuccessorProfileRevisionID:     input.Inspection.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    input.Inspection.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               input.Inspection.Spec.SuccessorProfile,
	})
	if err != nil || !reflect.DeepEqual(observed, input.Inspection) {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || snapshot.Active != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	selection, err := m.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	checkpoint, err := gatewayRebindCheckpointForSelection(input.Inspection.ProtectedGeneration,
		input.Inspection.Spec.OperationID, selection)
	if err != nil || checkpoint.Digest != input.Inspection.PredecessorCheckpointDigest ||
		checkpoint.sourceRef() != input.Inspection.Spec.Predecessor ||
		input.Inspection.Spec.SuccessorProtectedGeneration != checkpoint.Generation {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	claim, _, err := repository.ClaimGatewayRebindV2(ctx, appaccess.GatewayRebindPreclaimProposalV2{
		Spec: input.Inspection.Spec, RebindApproval: input.RebindApproval,
		ConfigureApproval: input.ConfigureApproval, Roster: input.Inspection.Roster,
		RuntimeHeads: input.Inspection.RuntimeHeads,
	})
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	if m.gatewayRebindAfterClaim != nil {
		if err := m.gatewayRebindAfterClaim(ctx, claim); err != nil {
			return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
		}
	}
	return m.installGatewayRebindPreparedProtectedLocked(ctx, claim, input.Inspection.Roster,
		input.Inspection.RuntimeHeads,
		checkpoint, m.gatewayRebindProgressTime())
}

func (m *Manager) recoverGatewayRebindPreparedAdmissionLocked(ctx context.Context,
	repository gatewayRebindProposalRepository, snapshot appaccess.GatewayRebindRecoverySnapshot,
) (gatewayRebindPreparedAttempt, error) {
	if m == nil || ctx == nil || repository == nil || snapshot.Active == nil ||
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
	return m.installGatewayRebindPreparedProtectedLocked(ctx, claim, snapshot.Active.RosterV2,
		snapshot.Active.RuntimeHeads,
		checkpoint, m.gatewayRebindProgressTime())
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
	if !occurredAt.After(claim.CreatedAt) {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(m.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err != nil || ensureGatewayRebindCheckpoint(checkpointStore, checkpoint) != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(m.options.DataRoot,
		checkpoint.Generation, checkpoint.OperationID)
	if err == nil {
		if installedIntent, loadErr := intentStore.load(); loadErr == nil {
			if installedIntent.Claim.RequestDigest != claim.RequestDigest ||
				installedIntent.Predecessor != checkpoint.sourceRef() ||
				!sameGatewayRebindRosterV2(installedIntent.Roster, roster) ||
				!sameGatewayRebindRuntimeHeads(installedIntent.RuntimeHeads, runtimeHeads) {
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
	network, err := m.observeGatewayRebindV2SuccessorNetworkLocked(ctx, claim)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, err
	}
	intent, err := newGatewayRebindProtectedIntentV2(claim, roster, runtimeHeads, checkpoint, network)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindProposalError(ctx)
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
	if ctx.Err() != nil || claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		!validGatewayProfileBinding(profile) || reads.network.candidates == nil || reads.network.host == nil ||
		reads.network.docker == nil || reads.dockerIDs == nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	dockerIDsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsBefore) {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	docker, err := reads.network.docker(ctx)
	if err != nil || ctx.Err() != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	dockerIDsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(dockerIDsAfter) || !equalStrings(dockerIDsBefore, dockerIDsAfter) {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	plan, err := selectGatewayV2NetworkPlan(ctx, profile, gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return append([]hostNetworkCandidate(nil), candidates...), nil },
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{Routes: append([]netip.Prefix(nil), host.Routes...),
				Interfaces: append([]netip.Prefix(nil), host.Interfaces...)}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) { return append([]netip.Prefix(nil), docker...), nil },
	})
	if err != nil {
		return gatewayRebindSuccessorPreflightObservation{}, gatewayRebindSuccessorNetworkError(ctx)
	}
	return gatewayRebindSuccessorPreflightObservation{
		candidates: append([]hostNetworkCandidate(nil), candidates...),
		host: gatewayV2HostNetworkSnapshot{Routes: append([]netip.Prefix(nil), host.Routes...),
			Interfaces: append([]netip.Prefix(nil), host.Interfaces...)},
		dockerIDs: append([]string(nil), dockerIDsBefore...), docker: append([]netip.Prefix(nil), docker...),
		result: GatewayRebindSuccessorPreflight{RebindOperationID: claim.Spec.OperationID,
			ClaimRequestDigest: claim.RequestDigest,
			Profile: GatewayRebindSuccessorProfile{RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber,
				OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
				SpecDigest: profile.SpecDigest, SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID,
				PortStart: profile.PortStart, PortEnd: profile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID},
			Network: GatewayRebindSuccessorNetworkPlan{Subnet: plan.Subnet, GatewayIPv4: plan.GatewayIPv4,
				ContainerIPv4: plan.ContainerIPv4}},
	}, nil
}
