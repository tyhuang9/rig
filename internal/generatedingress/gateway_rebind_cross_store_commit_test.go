package generatedingress

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindBoundedPhysicalDriver struct {
	t           *testing.T
	template    gatewayCurrentStateFixture
	rollback    bool
	commitCalls int
	abortCalls  int
	attestCalls int
	onAttest    func(gatewayCurrentSelection)
}

type gatewayRebindLostAckRepository struct {
	*appaccess.Repository
	lost   bool
	tamper bool
}

type gatewayRebindRollbackReadbackRepository struct {
	*appaccess.Repository
	rolledBack bool
}

func (r *gatewayRebindRollbackReadbackRepository) ApplyGatewayRebindTransition(ctx context.Context,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	command, err := r.Repository.ApplyGatewayRebindTransition(ctx, proof)
	if err == nil && proof.NextState == appaccess.GatewayRebindRolledBack {
		r.rolledBack = true
	}
	return command, err
}

func (r *gatewayRebindRollbackReadbackRepository) GatewayRebindRecoverySnapshot(ctx context.Context,
) (appaccess.GatewayRebindRecoverySnapshot, error) {
	snapshot, err := r.Repository.GatewayRebindRecoverySnapshot(ctx)
	if err == nil && r.rolledBack && snapshot.CurrentSource != nil {
		changed := *snapshot.CurrentSource
		changed.ProfileSpecDigest = strings.Repeat("f", 64)
		snapshot.CurrentSource = &changed
	}
	return snapshot, err
}

func (r *gatewayRebindLostAckRepository) ApplyGatewayRebindTransition(ctx context.Context,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	command, err := r.Repository.ApplyGatewayRebindTransition(ctx, proof)
	if err == nil && !r.lost {
		r.lost = true
		return appaccess.GatewayRebindTransitionCommand{}, errors.New("injected lost transition acknowledgement")
	}
	return command, err
}

func (r *gatewayRebindLostAckRepository) GatewayRebindRecoverySnapshot(ctx context.Context,
) (appaccess.GatewayRebindRecoverySnapshot, error) {
	snapshot, err := r.Repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !r.lost || !r.tamper {
		return snapshot, err
	}
	for historyIndex := range snapshot.History {
		commands := snapshot.History[historyIndex].Commands
		if len(commands) != 0 {
			commands[len(commands)-1].CanonicalPayload += " "
			snapshot.History[historyIndex].Commands = commands
		}
	}
	return snapshot, nil
}

func (d *gatewayRebindBoundedPhysicalDriver) reconcileSuccessorLocked(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	d.commitCalls++
	records, resources, proof := gatewayRebindTypedCompleteProgressFixture(d.t, d.template, attempt.Intent, attempt.Progress)
	if d.rollback {
		forward := append([]gatewayRebindProgressRecord(nil), records[:13]...)
		for _, record := range forward[1:] {
			if err := appendProgress(ctx, record); err != nil {
				return gatewayRebindTypedPhysicalResult{}, err
			}
		}
		routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(attempt.Checkpoint)
		if err != nil {
			return gatewayRebindTypedPhysicalResult{}, err
		}
		owned := gatewayRebindTypedRollbackOwnedFromEffect(*forward[len(forward)-1].TypedEffect)
		finalCopy := *resources.FinalContainer
		owned.FinalContainer = &finalCopy
		lastAt, err := parseGatewayRebindProgressTime(forward[len(forward)-1].OccurredAt)
		if err != nil {
			return gatewayRebindTypedPhysicalResult{}, err
		}
		rollbackIntent, err := newGatewayRebindTypedRollbackIntentV2(attempt.Intent, forward, owned,
			routesDigest, strings.Repeat("b", 64), lastAt.Add(time.Nanosecond))
		if err != nil || appendProgress(ctx, rollbackIntent) != nil {
			return gatewayRebindTypedPhysicalResult{}, errors.New("bounded typed rollback intent failed")
		}
		withIntent := append(forward, rollbackIntent)
		rollbackProof := gatewayRebindTypedRollbackProofFixture(d.t, d.template, routesDigest, rollbackIntent.Digest)
		rollbackComplete, err := newGatewayRebindTypedRollbackCompleteV2(attempt.Intent, withIntent, rollbackProof,
			lastAt.Add(2*time.Nanosecond))
		if err != nil || appendProgress(ctx, rollbackComplete) != nil {
			return gatewayRebindTypedPhysicalResult{}, errors.New("bounded typed rollback completion failed")
		}
		return gatewayRebindTypedPhysicalResult{Disposition: appaccess.GatewayRebindDispositionAbort,
			Last: rollbackComplete}, nil
	}
	for _, record := range records[1:] {
		if err := appendProgress(ctx, record); err != nil {
			return gatewayRebindTypedPhysicalResult{}, err
		}
	}
	return gatewayRebindTypedPhysicalResult{Disposition: appaccess.GatewayRebindDispositionCommit,
		Last: records[len(records)-1], Resources: resources, Proof: proof}, nil
}

func (d *gatewayRebindBoundedPhysicalDriver) proveNoSuccessorEffectsLocked(_ context.Context,
	claim appaccess.GatewayRebindClaimV2, _ []appaccess.GatewayRebindRosterEntryV2,
	checkpoint gatewayRebindPredecessorCheckpoint,
) (gatewayRebindNoEffectAbortProof, error) {
	d.abortCalls++
	profile := gatewayRebindSuccessorIntentProfile{
		RevisionID: claim.Spec.SuccessorProfileRevisionID, RevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		OperationID: claim.Spec.SuccessorProfileOperationID, RequestDigest: claim.SuccessorProfileRequestDigest,
		SpecDigest: claim.ConfigureApproval.SpecDigest, SelectedIPv4: claim.Spec.SuccessorProfile.SelectedIPv4,
		InterfaceID: claim.Spec.SuccessorProfile.InterfaceID, PortStart: claim.Spec.SuccessorProfile.PortStart,
		PortEnd: claim.Spec.SuccessorProfile.PortEnd, ApprovedBy: claim.ConfigureApproval.ActorID,
	}
	identity, err := newGatewayRebindSuccessorIdentity(checkpoint.Generation, claim.Spec.OperationID,
		GatewayRebindSuccessorProfile(profile))
	if err != nil {
		return gatewayRebindNoEffectAbortProof{}, err
	}
	value := gatewayRebindNoEffectAbortProof{
		Version: gatewayRebindNoEffectProofVersion, Purpose: gatewayRebindNoEffectProofPurpose,
		Generation: checkpoint.Generation, OperationID: checkpoint.OperationID,
		ClaimRequestDigest: claim.RequestDigest, PredecessorCheckpointDigest: checkpoint.Digest,
		SourceStateDigest: checkpoint.SourceStateDigest, SuccessorIdentityDigest: identity.Digest,
		ObservationDigest: strings.Repeat("9", 64), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	value.Digest, err = gatewayRebindNoEffectAbortProofDigest(value)
	return value, err
}

func (d *gatewayRebindBoundedPhysicalDriver) attestCommittedCurrentLocked(_ context.Context,
	selection gatewayCurrentSelection,
) (string, error) {
	d.attestCalls++
	if selection.State == nil || selection.Terminal == nil {
		return "", errors.New("missing selected typed current")
	}
	if d.onAttest != nil {
		d.onAttest(selection)
	}
	return canonicalDigest(struct {
		Purpose string                   `json:"purpose"`
		State   gatewayCurrentRouteState `json:"state"`
		Receipt string                   `json:"receipt"`
	}{"test-only/bounded-current-attestation", *selection.State, selection.Terminal.Digest})
}

func completeGatewayRebindCoordinatorFixture(t *testing.T, f gatewayRebindPredecessorFixture) gatewayRebindCommitInput {
	t.Helper()
	ctx := context.Background()
	input := GatewayRebindProposalInput{
		OperationID:                    f.proposal.Spec.OperationID,
		SuccessorProfileRevisionID:     f.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: f.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    f.proposal.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               f.proposal.Spec.SuccessorProfile,
	}
	inspection, err := f.manager.InspectGatewayRebindProposal(ctx, f.repository, input)
	if err != nil {
		t.Fatal(err)
	}
	entry := inspection.Roster[0]
	completed := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.ExecContext(ctx, `UPDATE jobs SET status='succeeded',phase='completed',updated_at=?,finished_at=?
		WHERE status='running' AND id=(SELECT job_id FROM deployments WHERE id=? AND app_id=?)`,
		completed, completed, entry.ServingDeploymentID, entry.AppID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE deployments SET status='succeeded',finished_at=?
		WHERE id=? AND app_id=? AND status='preparing'`, completed, entry.ServingDeploymentID, entry.AppID); err != nil {
		t.Fatal(err)
	}
	inspection, err = f.manager.InspectGatewayRebindProposal(ctx, f.repository, input)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator},
	}
}

func newGatewayRebindCoordinatorFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindCommitInput, *gatewayRebindBoundedPhysicalDriver) {
	t.Helper()
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	f.manager.options.RebindCurrentStateRepository = f.repository
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	input := completeGatewayRebindCoordinatorFixture(t, f)
	template := newGatewayCurrentStateFixture(t)
	f.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := template.intent.NetworkObservation
		value.OperationID = claim.Spec.OperationID
		value.ClaimRequestDigest = claim.RequestDigest
		value.ProfileSpecDigest = claim.ConfigureApproval.SpecDigest
		return value, nil
	}
	driver := &gatewayRebindBoundedPhysicalDriver{t: t, template: template}
	return f, input, driver
}

func TestGatewayRebindCoordinatorCommitsRealSQLAndProtectedBaseline(t *testing.T) {
	f, input, driver := newGatewayRebindCoordinatorFixture(t)
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
	if err != nil {
		history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		snapshot, snapshotErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
		t.Fatalf("commit error=%v calls=%d/%d/%d history checkpoints=%d intents=%d progress=%d terminals=%d historyErr=%v snapshot=%#v snapshotErr=%v", err, driver.commitCalls, driver.attestCalls, driver.abortCalls, len(history.Checkpoints), len(history.IntentsV2), len(history.Progress), len(history.TerminalsV2), historyErr, snapshot, snapshotErr)
	}
	if result.OperationID != input.Inspection.Spec.OperationID || result.InitialPhase != appaccess.GatewayRebindPrepared ||
		result.FinalPhase != appaccess.GatewayRebindCommitted || result.Disposition != appaccess.GatewayRebindDispositionCommit ||
		!result.FenceReleased || !validSHA256(result.TerminalReceiptDigest) || driver.commitCalls != 1 ||
		driver.attestCalls != 1 || driver.abortCalls != 0 || f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("unexpected coordinator result=%#v calls=%d/%d/%d", result, driver.commitCalls, driver.attestCalls, driver.abortCalls)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.OperationID != result.OperationID || snapshot.CurrentSource.TerminalReceiptDigest != result.TerminalReceiptDigest ||
		len(snapshot.CurrentTransfers) != len(input.Inspection.Roster) || len(snapshot.History) != 1 {
		t.Fatalf("committed SQL snapshot mismatch: %#v error=%v", snapshot, err)
	}
	if err := f.repository.CheckGatewayRebindFence(context.Background()); err != nil {
		t.Fatalf("committed fence: %v", err)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.TerminalsV2) != 1 || len(history.IntentsV2) != 1 {
		t.Fatalf("committed protected history: terminals=%d intents=%d error=%v", len(history.TerminalsV2), len(history.IntentsV2), err)
	}
	selection, err := f.manager.selectGatewayCurrentLocked(context.Background(), snapshot)
	if err != nil || selection.Terminal == nil || selection.Terminal.TypedReceipt == nil || selection.State == nil ||
		selection.State.Revision != 1 || len(selection.State.Apps) < len(input.Inspection.Roster) {
		t.Fatalf("committed typed current selection: %#v error=%v", selection, err)
	}
}

func TestGatewayRebindCoordinatorRejectsCurrentStateDriftAfterAttestation(t *testing.T) {
	f, input, driver := newGatewayRebindCoordinatorFixture(t)
	driver.onAttest = func(selection gatewayCurrentSelection) {
		if selection.State == nil || selection.Store == nil {
			t.Fatal("attestation selection has no current state store")
		}
		next := cloneGatewayCurrentRouteState(*selection.State)
		next.Revision++
		var err error
		next.Digest, err = gatewayCurrentRouteStateDigest(next)
		if err != nil || selection.Store.saveNext(*selection.State, next) != nil {
			t.Fatalf("advance current state after attestation: %v", err)
		}
	}
	if result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver); err == nil {
		t.Fatalf("current state drift released fence: %#v", result)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindDatabaseCommitted {
		t.Fatalf("state drift did not retain database-committed fence: %#v error=%v", snapshot, err)
	}
	if err := f.repository.CheckGatewayRebindFence(context.Background()); err == nil {
		t.Fatal("state drift unexpectedly released SQL fence")
	}
}

func TestGatewayRebindCoordinatorRollsBackPreparedClaimWithNoSuccessorEffects(t *testing.T) {
	f, input, driver := newGatewayRebindCoordinatorFixture(t)
	f.manager.gatewayRebindV2NetworkObserver = func(context.Context, appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("injected successor network unavailable")
	}
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
	if err != nil {
		history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		snapshot, snapshotErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
		t.Fatalf("abort error=%v calls=%d/%d/%d history checkpoints=%d intents=%d progress=%d terminals=%d historyErr=%v snapshot=%#v snapshotErr=%v", err, driver.commitCalls, driver.attestCalls, driver.abortCalls, len(history.Checkpoints), len(history.IntentsV2), len(history.Progress), len(history.TerminalsV2), historyErr, snapshot, snapshotErr)
	}
	if result.FinalPhase != appaccess.GatewayRebindRolledBack || result.Disposition != appaccess.GatewayRebindDispositionAbort ||
		!result.FenceReleased || driver.abortCalls != 1 || driver.commitCalls != 0 || driver.attestCalls != 0 ||
		f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("unexpected no-effect abort result=%#v", result)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active != nil || len(snapshot.History) != 1 || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.OperationID == input.Inspection.Spec.OperationID {
		t.Fatalf("rolled-back SQL snapshot: %#v error=%v", snapshot, err)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.TerminalsV2) != 1 || len(history.IntentsV2) != 0 ||
		history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("no-effect protected history: %#v error=%v", history.TerminalsV2, err)
	}
	// The consumed SQL/protected generation remains in both histories. A later
	// attempt must allocate a new generation and operation identity.
	next := GatewayRebindProposalInput{OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: input.Inspection.Spec.SuccessorProfile}
	inspection, err := f.manager.InspectGatewayRebindProposal(context.Background(), f.repository, next)
	if err != nil || inspection.ProtectedGeneration <= input.Inspection.ProtectedGeneration ||
		inspection.Spec.Predecessor.Lineage != input.Inspection.Spec.Predecessor.Lineage ||
		inspection.Spec.Predecessor.SourceStateVersion != input.Inspection.Spec.Predecessor.SourceStateVersion ||
		inspection.Spec.Predecessor.SourceStateRevision != input.Inspection.Spec.Predecessor.SourceStateRevision ||
		inspection.Spec.Predecessor.SourceStateDigest != input.Inspection.Spec.Predecessor.SourceStateDigest ||
		inspection.Spec.Predecessor.PredecessorCheckpointDigest == input.Inspection.Spec.Predecessor.PredecessorCheckpointDigest {
		t.Fatalf("post-abort generation/source proposal: %#v error=%v", inspection, err)
	}
}

func TestGatewayRebindCoordinatorRollsBackOwnedPostIntentEffects(t *testing.T) {
	f, input, driver := newGatewayRebindCoordinatorFixture(t)
	driver.rollback = true
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalPhase != appaccess.GatewayRebindRolledBack || result.Disposition != appaccess.GatewayRebindDispositionAbort ||
		!result.FenceReleased || driver.commitCalls != 1 || driver.abortCalls != 0 || driver.attestCalls != 0 ||
		f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("unexpected post-intent rollback result=%#v calls=%d/%d/%d",
			result, driver.commitCalls, driver.attestCalls, driver.abortCalls)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.OperationID == input.Inspection.Spec.OperationID || len(snapshot.History) != 1 {
		t.Fatalf("post-intent rollback SQL snapshot=%#v error=%v", snapshot, err)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.TerminalsV2) != 1 || len(history.IntentsV2) != 1 ||
		history.TerminalsV2[0].Receipt.RollbackProof == nil || len(history.Progress) < 4 || len(history.Progress) > 18 {
		t.Fatalf("post-intent rollback protected history: intents=%d progress=%d terminals=%d error=%v",
			len(history.IntentsV2), len(history.Progress), len(history.TerminalsV2), err)
	}
	next := GatewayRebindProposalInput{OperationID: uuid.NewString(), SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: input.Inspection.Spec.SuccessorProfile}
	inspection, err := f.manager.InspectGatewayRebindProposal(context.Background(), f.repository, next)
	if err != nil || inspection.ProtectedGeneration <= input.Inspection.ProtectedGeneration ||
		inspection.Spec.Predecessor.Lineage != input.Inspection.Spec.Predecessor.Lineage {
		t.Fatalf("post-intent rollback next proposal=%#v error=%v", inspection, err)
	}
}

func TestGatewayRebindCoordinatorRejectsChangedPredecessorAfterNoEffectRollback(t *testing.T) {
	f, input, driver := newGatewayRebindCoordinatorFixture(t)
	f.manager.gatewayRebindV2NetworkObserver = func(context.Context, appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("injected successor network unavailable")
	}
	repository := &gatewayRebindRollbackReadbackRepository{Repository: f.repository}
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), repository, input, driver)
	if err == nil || result != (GatewayRebindCommitResult{}) || !repository.rolledBack ||
		!f.manager.gatewayRebindAdmissionBlocked() || driver.abortCalls != 1 {
		t.Fatalf("changed rollback predecessor readback accepted: result=%#v calls=%d error=%v",
			result, driver.abortCalls, err)
	}
	snapshot, snapshotErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if snapshotErr != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.ProfileSpecDigest == strings.Repeat("f", 64) {
		t.Fatalf("underlying rollback result changed unexpectedly: %#v error=%v", snapshot, snapshotErr)
	}
}

func TestGatewayRebindCoordinatorRejectsLostAckWithDifferentCanonicalTransition(t *testing.T) {
	f, input, driver := newGatewayRebindCoordinatorFixture(t)
	repository := &gatewayRebindLostAckRepository{Repository: f.repository, tamper: true}
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), repository, input, driver)
	if err == nil || result != (GatewayRebindCommitResult{}) || !repository.lost || driver.commitCalls != 1 {
		t.Fatalf("tampered lost acknowledgement accepted: result=%#v calls=%d error=%v",
			result, driver.commitCalls, err)
	}
	snapshot, snapshotErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	if snapshotErr != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindSuccessorReady ||
		snapshot.DatabaseCommitObserved {
		t.Fatalf("tampered lost acknowledgement did not retain successor-ready fence: %#v error=%v", snapshot, snapshotErr)
	}
}
