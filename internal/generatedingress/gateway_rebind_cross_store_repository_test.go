package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

// This integration test uses the real repository and protected files. The
// fixture's Docker runner is simulated; physical acceptance is a separate gate.
func TestGatewayRebindProposalAndAdmissionUseRealRepository(t *testing.T) {
	f := newGatewayRebindNonterminalPredecessorFixture(t)
	ctx := context.Background()
	f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
	beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || beforeSQL.CurrentSource == nil || beforeSQL.Active != nil || len(beforeSQL.History) != 0 {
		t.Fatal("fixture must have one native source and no retained rebind")
	}
	beforeArtifacts, err := readGatewayHistorySnapshot(f.manager.store)
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(f.runner.commands)
	input := GatewayRebindProposalInput{
		OperationID: f.proposal.Spec.OperationID, SuccessorProfileRevisionID: f.proposal.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: f.proposal.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    f.proposal.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               f.proposal.Spec.SuccessorProfile,
	}
	inspection, err := f.manager.InspectGatewayRebindProposal(ctx, f.repository, input)
	if err != nil {
		t.Fatalf("inspect real SQL/protected native source: %v", err)
	}
	if inspection.Spec.SuccessorProtectedGeneration != inspection.ProtectedGeneration ||
		inspection.ProtectedGeneration != f.store.generation+1 || len(inspection.Roster) != 1 ||
		inspection.Spec.Predecessor.Lineage.OperationID != beforeSQL.CurrentSource.OperationID ||
		inspection.Roster[0].SourceProfileRevisionID != beforeSQL.CurrentSource.ProfileRevisionID ||
		inspection.Roster[0].PredecessorTransferDigest != nil {
		t.Fatal("proposal lost native source, raw grant or exact attempt generation")
	}
	repeated, err := f.manager.InspectGatewayRebindProposal(ctx, f.repository, input)
	if err != nil || !reflect.DeepEqual(inspection, repeated) {
		t.Fatal("real repository changed the read-only proposal")
	}
	runtimeHeadsDigest, err := appaccess.GatewayRebindRuntimeHeadsV2Digest(inspection.Spec.OperationID, inspection.RuntimeHeads)
	if err != nil || len(inspection.RuntimeHeads) == 0 || inspection.Spec.RuntimeHeadsCount != int64(len(inspection.RuntimeHeads)) || inspection.Spec.RuntimeHeadsDigest != runtimeHeadsDigest {
		t.Fatal("proposal must carry the exact inspected runtime-head census")
	}
	proposal := appaccess.GatewayRebindPreclaimProposalV2{
		Spec: inspection.Spec, Roster: inspection.Roster, RuntimeHeads: inspection.RuntimeHeads,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator},
	}
	rejected := proposal
	rejected.RebindApproval.SpecDigest = strings.Repeat("f", 64)
	if _, _, err := f.repository.ClaimGatewayRebindV2(ctx, rejected); !errors.Is(err, appaccess.ErrInvalidInput) {
		t.Fatalf("mismatched exact approval must be refused: %v", err)
	}
	afterRefusal, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterRefusal) {
		t.Fatal("proposal inspection or refused approval changed SQL state")
	}
	if err := f.repository.CheckGatewayRebindFence(ctx); err != nil {
		t.Fatal("refused approval left a rebind fence")
	}
	// This fixture deliberately retains the exact route activation at its
	// nonterminal Draining/Applying/Running boundary. Admission must refuse it
	// before the same sealed execution is completed below.
	if _, _, err := f.repository.ClaimGatewayRebindV2(ctx, proposal); !errors.Is(err, appaccess.ErrGatewayRebindNotQuiescent) {
		t.Fatalf("nonterminal deployment must refuse admission: %v", err)
	}
	afterBusy, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterBusy) || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("refused nonterminal deployment changed authority or left a fence")
	}
	f.seeded.complete(t)
	f.seeded = nil
	completeCrossStoreRepositoryFixture(t, f, inspection.Roster[0])
	claim, created, err := f.repository.ClaimGatewayRebindV2(ctx, proposal)
	if err != nil || !created || claim.State != appaccess.GatewayRebindPrepared || claim.StateSequence != 1 ||
		!reflect.DeepEqual(claim.Spec, inspection.Spec) {
		t.Fatalf("admit exact inspected proposal: created=%v err=%v", created, err)
	}
	replay, created, err := f.repository.ClaimGatewayRebindV2(ctx, proposal)
	if err != nil || created || !reflect.DeepEqual(claim, replay) {
		t.Fatal("real prepared claim was not replayed exactly")
	}
	afterClaim, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || afterClaim.Active == nil || afterClaim.Active.Claim.V2 == nil ||
		afterClaim.Active.Claim.V2.Spec.OperationID != claim.Spec.OperationID ||
		afterClaim.Phase != appaccess.GatewayRebindPrepared || len(afterClaim.History) != 1 ||
		!reflect.DeepEqual(beforeSQL.CurrentSource, afterClaim.CurrentSource) ||
		!reflect.DeepEqual(beforeSQL.CurrentProfile, afterClaim.CurrentProfile) ||
		!afterClaim.RollbackAllowed || afterClaim.DatabaseCommitObserved {
		t.Fatal("prepared SQL claim changed current authority or lost its recovery direction")
	}
	if err := f.repository.CheckGatewayRebindFence(ctx); !errors.Is(err, appaccess.ErrGatewayRebindActive) {
		t.Fatalf("prepared claim must fence ordinary runtime: %v", err)
	}
	if _, err := f.manager.InspectGatewayRebindProposal(ctx, f.repository, input); err == nil {
		t.Fatal("ordinary proposal inspection bypassed the active rebind fence")
	}
	current, err := f.manager.InspectGatewayRebindCurrent(ctx, f.repository)
	if err != nil || current.SelectedCurrentAuthority != *beforeSQL.CurrentSource ||
		current.ActiveOperationID != claim.Spec.OperationID || current.ActivePhase != appaccess.GatewayRebindPrepared ||
		current.FenceReleased || len(current.Retained) != 0 {
		t.Fatalf("current inspection confused prepared attempt with selected authority: %v", err)
	}
	status, err := f.manager.InspectGatewayRebindOperation(ctx, f.repository, claim.Spec.OperationID)
	if err != nil || status.Protected == nil ||
		status.Protected.Presence != appaccess.GatewayRebindProtectedEvidenceAbsent ||
		status.Protected.FixedDisposition != appaccess.GatewayRebindDispositionNone ||
		status.RecoveryPolicy != appaccess.GatewayRebindRecoveryEvidenceDecides ||
		len(status.AllowedActions) != 0 || !status.Active ||
		status.OperationFence != GatewayRebindOperationFenceHeld ||
		ValidateGatewayRebindOperationInspection(status, claim.Spec.OperationID) != nil {
		t.Fatalf("SQL-only prepared status lost its explicit no-disposition evidence or policy: %v", err)
	}
	malformed := status
	protected := *status.Protected
	protected.FixedDisposition = ""
	malformed.Protected = &protected
	if ValidateGatewayRebindOperationInspection(malformed, claim.Spec.OperationID) == nil {
		t.Fatal("empty disposition must remain invalid for absent protected evidence")
	}
	afterArtifacts, err := readGatewayHistorySnapshot(f.manager.store)
	if err != nil || !sameGatewayHistorySnapshot(beforeArtifacts, afterArtifacts) {
		t.Fatal("proposal/admission boundary changed protected files")
	}
	if len(f.runner.commands) != beforeCommands {
		t.Fatal("proposal/admission boundary issued Docker commands")
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 0 || len(history.IntentsV2) != 0 || len(history.Checkpoints) != 0 || len(history.Progress) != 0 || len(history.Terminals) != 0 {
		t.Fatal("SQL-only prepared boundary installed protected rebind evidence")
	}
}

func TestGatewayRebindPreparedAdmissionAndRecoveryUseRealRepository(t *testing.T) {
	for _, boundary := range []string{"prepared", "failure_after_sql_claim"} {
		t.Run(boundary, func(t *testing.T) {
			f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
			ctx := context.Background()
			f.manager.options.RebindFenceCheck = f.repository.CheckGatewayRebindFence
			inspection, err := f.manager.InspectGatewayRebindProposal(ctx, f.repository, GatewayRebindProposalInput{
				OperationID: f.proposal.Spec.OperationID, SuccessorProfileRevisionID: f.proposal.Spec.SuccessorProfileRevisionID,
				SuccessorProfileRevisionNumber: f.proposal.Spec.SuccessorProfileRevisionNumber,
				SuccessorProfileOperationID:    f.proposal.Spec.SuccessorProfileOperationID, SuccessorProfile: f.proposal.Spec.SuccessorProfile,
			})
			if err != nil || len(inspection.Roster) != 1 {
				t.Fatalf("inspect native source: %v", err)
			}
			completeCrossStoreRepositoryFixture(t, f, inspection.Roster[0])
			beforeSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			admissionProof := gatewayRebindAdmissionFixtureProof(t, f.manager, beforeSQL, inspection,
				gatewayRebindNativeFixtureInspector(t, f),
				gatewayRebindCrossStoreAdmissionTestReads(inspection.Spec, f.state.Network.Subnet))
			beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			beforeCommands := len(f.runner.commands)
			input := gatewayRebindCommitInput{Inspection: inspection,
				RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
					SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
				ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
					SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator}}
			installCrossStoreFixtureNetworkObserver(f.manager, f.state.Network.Subnet)
			if boundary == "failure_after_sql_claim" {
				// Return a fault at the real SQL commit boundary before protected
				// writes. This is not the separate child-process crash gate.
				f.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
					return errors.New("injected failure after SQL claim")
				}
			}
			attempt, prepareErr := withCrossStoreFixtureEffectLocks(t, f.manager, func() (gatewayRebindPreparedAttempt, error) {
				return f.manager.prepareGatewayRebindLocked(ctx, f.repository, input)
			})
			if boundary == "prepared" && prepareErr != nil {
				t.Fatalf("real prepared admission: %v", prepareErr)
			}
			if boundary == "failure_after_sql_claim" && prepareErr == nil {
				t.Fatal("post-claim failure was not surfaced")
			}
			preparedSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || preparedSQL.Active == nil || preparedSQL.Active.Claim.V2 == nil ||
				preparedSQL.Phase != appaccess.GatewayRebindPrepared || len(preparedSQL.History) != 1 ||
				!reflect.DeepEqual(beforeSQL.CurrentSource, preparedSQL.CurrentSource) ||
				!reflect.DeepEqual(beforeSQL.CurrentProfile, preparedSQL.CurrentProfile) ||
				!errors.Is(f.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) {
				t.Fatalf("preparation did not preserve fenced native authority: %v", err)
			}
			if boundary == "failure_after_sql_claim" {
				afterFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
				if err != nil || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
					t.Fatal("post-claim fault installed protected evidence prematurely")
				}
			}
			fresh, err := New(f.runner, f.manager.options)
			if err != nil {
				t.Fatal(err)
			}
			installCrossStoreFixtureNetworkObserver(fresh, f.state.Network.Subnet)
			proofReader := gatewayRebindImmutableFixtureProofReader(admissionProof)
			recovered, err := withCrossStoreFixtureEffectLocks(t, fresh, func() (gatewayRebindPreparedAttempt, error) {
				return fresh.recoverGatewayRebindPreparedAdmissionWithProofLocked(ctx, f.repository,
					preparedSQL, proofReader)
			})
			if err != nil {
				t.Fatalf("fresh manager cannot recover real prepared claim: %v", err)
			}
			if boundary == "prepared" && !reflect.DeepEqual(attempt, recovered) {
				t.Fatal("fresh manager replaced immutable prepared evidence")
			}
			occurredAt, err := time.Parse(time.RFC3339Nano, recovered.Progress.OccurredAt)
			if err != nil || !occurredAt.After(preparedSQL.Active.Claim.V2.CreatedAt) ||
				recovered.Progress.Sequence != 1 || recovered.Progress.Phase != gatewayRebindProgressSuccessorIntent ||
				recovered.Checkpoint.Digest != inspection.PredecessorCheckpointDigest ||
				recovered.Intent.Predecessor != inspection.Spec.Predecessor ||
				recovered.Intent.Generation != inspection.ProtectedGeneration ||
				!reflect.DeepEqual(recovered.Claim, *preparedSQL.Active.Claim.V2) {
				t.Fatal("recovery lost admitted generation, claim, predecessor or post-claim time ordering")
			}
			recoveredFiles, err := readGatewayHistorySnapshotMode(fresh.store, true)
			if err != nil {
				t.Fatal(err)
			}
			retainedFiles := gatewayHistorySnapshot{files: make(map[string]gatewayHistoryFileFingerprint)}
			for name := range beforeFiles.files {
				if fingerprint, exists := recoveredFiles.files[name]; exists {
					retainedFiles.files[name] = fingerprint
				}
			}
			if !sameGatewayHistorySnapshot(beforeFiles, retainedFiles) {
				t.Fatal("prepared recovery rewrote predecessor protected history")
			}
			history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.Intents) != 0 || len(history.IntentsV2) != 1 ||
				len(history.Checkpoints) != 1 || len(history.Progress) != 1 || len(history.Terminals) != 0 ||
				history.IntentsV2[0].Intent.Digest != recovered.Intent.Digest ||
				history.Progress[0].Record.Digest != recovered.Progress.Digest {
				t.Fatalf("prepared recovery did not retain exactly one admitted protected attempt: %v", err)
			}
			replayed, err := withCrossStoreFixtureEffectLocks(t, fresh, func() (gatewayRebindPreparedAttempt, error) {
				return fresh.recoverGatewayRebindPreparedAdmissionLocked(ctx, f.repository, preparedSQL)
			})
			if err != nil || !reflect.DeepEqual(recovered, replayed) {
				t.Fatalf("prepared recovery did not replay exact evidence: %v", err)
			}
			finalFiles, err := readGatewayHistorySnapshotMode(fresh.store, true)
			if err != nil || !sameGatewayHistorySnapshot(recoveredFiles, finalFiles) {
				t.Fatal("prepared replay rewrote protected files")
			}
			finalSQL, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || !reflect.DeepEqual(preparedSQL, finalSQL) ||
				!errors.Is(f.repository.CheckGatewayRebindFence(ctx), appaccess.ErrGatewayRebindActive) ||
				len(f.runner.commands) != beforeCommands {
				t.Fatal("prepared recovery changed SQL, released the fence or issued Docker commands")
			}
		})
	}
}

func completeCrossStoreRepositoryFixture(t *testing.T, f gatewayRebindPredecessorFixture, entry appaccess.GatewayRebindRosterEntryV2) {
	t.Helper()
	ctx := context.Background()
	var jobStatus, jobPhase, deploymentStatus string
	var attempt int
	if err := f.db.QueryRowContext(ctx, `SELECT j.status,j.phase,j.attempt,d.status
		FROM deployments d JOIN jobs j ON j.id=d.job_id
		WHERE d.id=? AND d.app_id=?`, entry.ServingDeploymentID, entry.AppID).
		Scan(&jobStatus, &jobPhase, &attempt, &deploymentStatus); err != nil {
		t.Fatalf("read exact completed fixture state: %v", err)
	}
	if jobStatus != "succeeded" || jobPhase != "succeeded" || attempt != 2 || deploymentStatus != "succeeded" {
		t.Fatalf("exact fixture is not terminal: job=%s/%s attempt=%d deployment=%s",
			jobStatus, jobPhase, attempt, deploymentStatus)
	}
	census, err := f.repository.GatewayRebindQuiescenceCensus(ctx)
	if err != nil || census.Jobs.Total != 1 || census.Jobs.Succeeded != 1 ||
		census.Deployments.Total != 1 || census.Deployments.Succeeded != 1 {
		t.Fatalf("completed fixture must prove real SQL quiescence: %#v, %v", census, err)
	}
}

func withCrossStoreFixtureEffectLocks(t *testing.T, manager *Manager,
	operation func() (gatewayRebindPreparedAttempt, error),
) (gatewayRebindPreparedAttempt, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, manager.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := releaseEffects(); err != nil {
			t.Error(err)
		}
	}()
	releaseGateway, err := manager.lockGatewayRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := releaseGateway(); err != nil {
			t.Error(err)
		}
	}()
	return operation()
}

func installCrossStoreFixtureNetworkObserver(manager *Manager, predecessorSubnet string) {
	// Only external inventory is simulated. The production network selector and
	// observation canonicalizer still bind it to the actual admitted SQL claim.
	manager.gatewayRebindV2NetworkObserver = func(ctx context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		reads := gatewayRebindCrossStoreAdmissionTestReads(claim.Spec, predecessorSubnet)
		observed, err := readGatewayRebindV2SuccessorNetwork(ctx, claim, reads)
		if err != nil {
			return gatewayRebindSuccessorNetworkObservation{}, err
		}
		return newGatewayRebindSuccessorNetworkObservation(observed)
	}
}
