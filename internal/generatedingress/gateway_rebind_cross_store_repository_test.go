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
	f := newGatewayRebindPredecessorFixtureWithClaim(t, false)
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
	proposal := appaccess.GatewayRebindPreclaimProposalV2{
		Spec: inspection.Spec, Roster: inspection.Roster,
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
	// The predecessor fixture stops after switching the serving head, leaving
	// its job running and deployment preparing. Admission must refuse that
	// boundary before this test finishes those exact fixture rows.
	if _, _, err := f.repository.ClaimGatewayRebindV2(ctx, proposal); !errors.Is(err, appaccess.ErrGatewayRebindNotQuiescent) {
		t.Fatalf("nonterminal deployment must refuse admission: %v", err)
	}
	afterBusy, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterBusy) || f.repository.CheckGatewayRebindFence(ctx) != nil {
		t.Fatal("refused nonterminal deployment changed authority or left a fence")
	}
	entry := inspection.Roster[0]
	completed := time.Now().UTC().Format(time.RFC3339Nano)
	jobResult, err := f.db.ExecContext(ctx, `UPDATE jobs SET status='succeeded',phase='completed',updated_at=?,finished_at=?
		WHERE status='running' AND id=(SELECT job_id FROM deployments WHERE id=? AND app_id=?)`,
		completed, completed, entry.ServingDeploymentID, entry.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := jobResult.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("complete exact fixture job: rows=%d err=%v", rows, err)
	}
	deploymentResult, err := f.db.ExecContext(ctx, `UPDATE deployments SET status='succeeded',finished_at=?
		WHERE id=? AND app_id=? AND status='preparing'`, completed, entry.ServingDeploymentID, entry.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := deploymentResult.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("complete exact fixture deployment: rows=%d err=%v", rows, err)
	}
	census, err := f.repository.GatewayRebindQuiescenceCensus(ctx)
	if err != nil || census.Jobs.Total != 1 || census.Jobs.Succeeded != 1 ||
		census.Deployments.Total != 1 || census.Deployments.Succeeded != 1 {
		t.Fatalf("completed fixture must prove real SQL quiescence: %#v, %v", census, err)
	}
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
