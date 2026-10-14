package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindCommitReplayUnresolvedRepository struct {
	*appaccess.Repository
}

func (r gatewayRebindCommitReplayUnresolvedRepository) ApplyGatewayRebindTransition(ctx context.Context,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	if proof.NextState == appaccess.GatewayRebindCommitted {
		proof.NextState = appaccess.GatewayRebindUnresolved
		proof.LocalAttestationDigest = ""
	}
	return r.Repository.ApplyGatewayRebindTransition(ctx, proof)
}

func gatewayRebindCommitActiveReplayFixture(t *testing.T) (appaccess.GatewayRebindRecoverySnapshot,
	GatewayRebindCommitRequest, string, *appaccess.Repository,
) {
	t.Helper()
	fixture, input, _ := newGatewayRebindCoordinatorFixture(t)
	review, err := newGatewayRebindCommitReview(input.Inspection)
	if err != nil {
		t.Fatal(err)
	}
	claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), appaccess.GatewayRebindPreclaimProposalV2{
		Spec: input.Inspection.Spec, RebindApproval: input.RebindApproval,
		ConfigureApproval: input.ConfigureApproval,
		Roster:            input.Inspection.Roster, RuntimeHeads: input.Inspection.RuntimeHeads,
	})
	if err != nil || !created || claim.State != appaccess.GatewayRebindPrepared {
		t.Fatalf("create retained active replay fixture: created=%t state=%s error=%v", created, claim.State, err)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil ||
		gatewayRebindCommitReplayHistoryIndex(t, snapshot, input.Inspection.Spec.OperationID) < 0 {
		t.Fatalf("read retained active replay fixture: %#v error=%v", snapshot, err)
	}
	return snapshot, GatewayRebindCommitRequest{
		Version: gatewayRebindCommitReviewVersion,
		Proposal: GatewayRebindProposalInput{
			OperationID:                    input.Inspection.Spec.OperationID,
			SuccessorProfileRevisionID:     input.Inspection.Spec.SuccessorProfileRevisionID,
			SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
			SuccessorProfileOperationID:    input.Inspection.Spec.SuccessorProfileOperationID,
			SuccessorProfile:               input.Inspection.Spec.SuccessorProfile,
		},
		ProposalDigest: review.ProposalDigest, ConfigureApprovalDigest: review.ConfigureApprovalDigest,
		RebindApprovalDigest: review.RebindApprovalDigest,
	}, gatewayRebindTestAdministrator, fixture.repository
}

func gatewayRebindCommitReplayHistoryIndex(t *testing.T, snapshot appaccess.GatewayRebindRecoverySnapshot,
	operationID string,
) int {
	t.Helper()
	match := -1
	for index, history := range snapshot.History {
		retainedID, err := gatewayRebindHistoryOperationID(history)
		if err != nil {
			t.Fatal(err)
		}
		if retainedID == operationID {
			if match >= 0 {
				t.Fatalf("duplicate fixture history for %s", operationID)
			}
			match = index
		}
	}
	return match
}

func TestGatewayRebindCommitReplayReturnsActiveRecoveryWithoutEffects(t *testing.T) {
	snapshot, request, actorID, repository := gatewayRebindCommitActiveReplayFixture(t)
	before, readErr := repository.GatewayRebindRecoverySnapshot(context.Background())
	if readErr != nil {
		t.Fatal(readErr)
	}
	projection, found, err := readGatewayRebindCommitReplay(context.Background(), snapshot, request, actorID)
	after, readErr := repository.GatewayRebindRecoverySnapshot(context.Background())
	want := GatewayRebindCommitResponse{
		RecoveryRequired: true,
		Operation: GatewayRebindOperationStatus{OperationID: request.Proposal.OperationID,
			StoredState: appaccess.GatewayRebindPrepared, StateSequence: 1},
		TerminalProof: GatewayRebindProofUnknown, CurrentProof: GatewayRebindProofUnknown,
	}
	if err != nil || readErr != nil || !found || !reflect.DeepEqual(projection.Response, want) ||
		projection.TerminalCommand != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("active replay projection=%#v found=%t durableChanged=%t error=%v readError=%v",
			projection, found, !reflect.DeepEqual(before, after), err, readErr)
	}
}

func TestGatewayRebindCommitReplayLeavesAbsentOperationForFreshAdmission(t *testing.T) {
	snapshot, request, actorID, repository := gatewayRebindCommitActiveReplayFixture(t)
	request.Proposal.OperationID = uuid.NewString()
	before, readErr := repository.GatewayRebindRecoverySnapshot(context.Background())
	if readErr != nil {
		t.Fatal(readErr)
	}
	projection, found, err := readGatewayRebindCommitReplay(context.Background(), snapshot, request, actorID)
	after, readErr := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || found || !reflect.DeepEqual(projection, gatewayRebindCommitReplayProjection{}) ||
		readErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("absent replay produced output/effect: projection=%#v found=%t durableChanged=%t error=%v readError=%v",
			projection, found, !reflect.DeepEqual(before, after), err, readErr)
	}
}

func TestGatewayRebindCommitReplayRequiresUniqueExactReviewedHistory(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*appaccess.GatewayRebindRecoverySnapshot, *GatewayRebindCommitRequest, *string)
		wantConflict   bool
		wantValidation bool
	}{
		{name: "request_version", wantValidation: true, mutate: func(_ *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			r.Version++
		}},
		{name: "proposal", wantConflict: true, mutate: func(_ *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			r.Proposal.SuccessorProfileRevisionID = uuid.NewString()
		}},
		{name: "proposal_digest", wantConflict: true, mutate: func(_ *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			r.ProposalDigest = differentGatewayRebindReviewDigest(r.ProposalDigest)
		}},
		{name: "configure_digest", wantConflict: true, mutate: func(_ *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			r.ConfigureApprovalDigest = differentGatewayRebindReviewDigest(r.ConfigureApprovalDigest)
		}},
		{name: "rebind_digest", wantConflict: true, mutate: func(_ *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			r.RebindApprovalDigest = differentGatewayRebindReviewDigest(r.RebindApprovalDigest)
		}},
		{name: "actor", wantConflict: true, mutate: func(_ *appaccess.GatewayRebindRecoverySnapshot,
			_ *GatewayRebindCommitRequest, actor *string) {
			*actor = uuid.NewString()
		}},
		{name: "stored_rebind_actor", wantConflict: true, mutate: func(s *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			index := gatewayRebindCommitReplayHistoryIndex(t, *s, r.Proposal.OperationID)
			s.History[index].Claim.V2.RebindApproval.ActorID = uuid.NewString()
		}},
		{name: "stored_configure_action", wantConflict: true, mutate: func(s *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			index := gatewayRebindCommitReplayHistoryIndex(t, *s, r.Proposal.OperationID)
			s.History[index].Claim.V2.ConfigureApproval.Action = appaccess.ActionRebindGateway
		}},
		{name: "legacy_same_id", wantConflict: true, mutate: func(s *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			index := gatewayRebindCommitReplayHistoryIndex(t, *s, r.Proposal.OperationID)
			legacy := &appaccess.GatewayRebindClaim{Spec: appaccess.GatewayRebindSpec{OperationID: r.Proposal.OperationID}}
			s.History[index].Claim = appaccess.GatewayRebindClaimRecord{SpecVersion: 1, Legacy: legacy}
		}},
		{name: "duplicate", mutate: func(s *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			index := gatewayRebindCommitReplayHistoryIndex(t, *s, r.Proposal.OperationID)
			s.History = append(s.History, s.History[index])
		}},
		{name: "malformed_event", mutate: func(s *appaccess.GatewayRebindRecoverySnapshot,
			r *GatewayRebindCommitRequest, _ *string) {
			index := gatewayRebindCommitReplayHistoryIndex(t, *s, r.Proposal.OperationID)
			s.History[index].Events[0].Sequence++
		}},
		{name: "active_not_exact", mutate: func(s *appaccess.GatewayRebindRecoverySnapshot,
			_ *GatewayRebindCommitRequest, _ *string) {
			changed := *s.Active
			changed.RosterV2 = nil
			s.Active = &changed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, request, actorID, repository := gatewayRebindCommitActiveReplayFixture(t)
			durableBefore, readErr := repository.GatewayRebindRecoverySnapshot(context.Background())
			if readErr != nil {
				t.Fatal(readErr)
			}
			test.mutate(&snapshot, &request, &actorID)
			projection, found, err := readGatewayRebindCommitReplay(context.Background(), snapshot, request, actorID)
			durableAfter, readErr := repository.GatewayRebindRecoverySnapshot(context.Background())
			var domain *Error
			switch {
			case test.wantConflict && !errors.Is(err, appaccess.ErrConflict):
				t.Fatalf("wanted idempotency conflict, got %v", err)
			case test.wantValidation && (!errors.As(err, &domain) || domain.Code != DiagnosticValidationFailed):
				t.Fatalf("wanted validation failure, got %v", err)
			case !test.wantConflict && !test.wantValidation &&
				(!errors.As(err, &domain) || domain.Code != DiagnosticRouteUnresolved):
				t.Fatalf("wanted unresolved retained state, got %v", err)
			}
			if found || !reflect.DeepEqual(projection, gatewayRebindCommitReplayProjection{}) ||
				readErr != nil || !reflect.DeepEqual(durableBefore, durableAfter) {
				t.Fatalf("rejected replay produced output/effect: projection=%#v found=%t durableChanged=%t readError=%v",
					projection, found, !reflect.DeepEqual(durableBefore, durableAfter), readErr)
			}
		})
	}
}

func TestGatewayRebindCommitReplayProjectsTerminalSQLWithoutPhysicalSuccess(t *testing.T) {
	for _, test := range []struct {
		name               string
		rollback           bool
		wantState          appaccess.GatewayRebindState
		wantDisposition    appaccess.GatewayRebindTerminalDisposition
		wantDatabaseCommit bool
	}{
		{name: "committed", wantState: appaccess.GatewayRebindCommitted,
			wantDisposition: appaccess.GatewayRebindDispositionCommit, wantDatabaseCommit: true},
		{name: "rolled_back", rollback: true, wantState: appaccess.GatewayRebindRolledBack,
			wantDisposition: appaccess.GatewayRebindDispositionAbort},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
			driver.rollback = test.rollback
			if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(),
				fixture.repository, input, driver); err != nil {
				t.Fatal(err)
			}
			snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil || snapshot.Active != nil || snapshot.CurrentSource == nil {
				t.Fatalf("read terminal snapshot: %#v error=%v", snapshot, err)
			}
			review, err := newGatewayRebindCommitReview(input.Inspection)
			if err != nil {
				t.Fatal(err)
			}
			request := GatewayRebindCommitRequest{
				Version: gatewayRebindCommitReviewVersion,
				Proposal: GatewayRebindProposalInput{
					OperationID:                    input.Inspection.Spec.OperationID,
					SuccessorProfileRevisionID:     input.Inspection.Spec.SuccessorProfileRevisionID,
					SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
					SuccessorProfileOperationID:    input.Inspection.Spec.SuccessorProfileOperationID,
					SuccessorProfile:               input.Inspection.Spec.SuccessorProfile,
				},
				ProposalDigest: review.ProposalDigest, ConfigureApprovalDigest: review.ConfigureApprovalDigest,
				RebindApprovalDigest: review.RebindApprovalDigest,
			}
			beforeCalls := [4]int{driver.commitCalls, driver.abortCalls, driver.attestCalls, driver.withdrawCalls}
			projection, found, err := readGatewayRebindCommitReplay(context.Background(), snapshot,
				request, gatewayRebindTestAdministrator)
			response := projection.Response
			if err != nil || !found || response.Created || response.RecoveryRequired ||
				response.Operation.OperationID != request.Proposal.OperationID ||
				response.Operation.StoredState != test.wantState ||
				response.Operation.TerminalDisposition != test.wantDisposition ||
				response.Operation.DatabaseCommitObserved != test.wantDatabaseCommit ||
				!validSHA256(response.Operation.TerminalReceiptDigest) || !response.FenceReleased ||
				response.TerminalProof != GatewayRebindProofUnavailable || response.TerminalProofDigest != "" ||
				response.CurrentProof != GatewayRebindProofUnavailable || response.CurrentAttestationDigest != "" ||
				response.SelectedCurrentAuthority == nil || *response.SelectedCurrentAuthority != *snapshot.CurrentSource ||
				projection.TerminalCommand == nil || projection.TerminalCommand.TerminalReceiptDigest != response.Operation.TerminalReceiptDigest ||
				beforeCalls != [4]int{driver.commitCalls, driver.abortCalls, driver.attestCalls, driver.withdrawCalls} {
				t.Fatalf("terminal SQL projection=%#v found=%t calls=%v/%v error=%v",
					projection, found, beforeCalls,
					[4]int{driver.commitCalls, driver.abortCalls, driver.attestCalls, driver.withdrawCalls}, err)
			}
		})
	}
}

func TestGatewayRebindCommitReplayPreservesUnresolvedDatabaseCommitEvidence(t *testing.T) {
	fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
	repository := gatewayRebindCommitReplayUnresolvedRepository{Repository: fixture.repository}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), repository,
		input, driver); err == nil {
		t.Fatal("injected unresolved terminal edge unexpectedly completed")
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindUnresolved ||
		!snapshot.DatabaseCommitObserved || snapshot.DatabaseCommittedEvent == nil {
		t.Fatalf("read real unresolved-after-commit snapshot: %#v error=%v", snapshot, err)
	}
	review, err := newGatewayRebindCommitReview(input.Inspection)
	if err != nil {
		t.Fatal(err)
	}
	request := GatewayRebindCommitRequest{
		Version: gatewayRebindCommitReviewVersion,
		Proposal: GatewayRebindProposalInput{
			OperationID:                    input.Inspection.Spec.OperationID,
			SuccessorProfileRevisionID:     input.Inspection.Spec.SuccessorProfileRevisionID,
			SuccessorProfileRevisionNumber: input.Inspection.Spec.SuccessorProfileRevisionNumber,
			SuccessorProfileOperationID:    input.Inspection.Spec.SuccessorProfileOperationID,
			SuccessorProfile:               input.Inspection.Spec.SuccessorProfile,
		},
		ProposalDigest: review.ProposalDigest, ConfigureApprovalDigest: review.ConfigureApprovalDigest,
		RebindApprovalDigest: review.RebindApprovalDigest,
	}
	beforeCalls := [4]int{driver.commitCalls, driver.abortCalls, driver.attestCalls, driver.withdrawCalls}
	projection, found, err := readGatewayRebindCommitReplay(context.Background(), snapshot,
		request, gatewayRebindTestAdministrator)
	response := projection.Response
	if err != nil || !found || !response.RecoveryRequired || response.FenceReleased ||
		response.Operation.StoredState != appaccess.GatewayRebindUnresolved ||
		!response.Operation.DatabaseCommitObserved || response.Operation.StateSequence != 4 ||
		response.Operation.TerminalDisposition != "" || response.Operation.TerminalReceiptDigest != "" ||
		response.TerminalProof != GatewayRebindProofUnknown || response.CurrentProof != GatewayRebindProofUnknown ||
		projection.TerminalCommand != nil ||
		beforeCalls != [4]int{driver.commitCalls, driver.abortCalls, driver.attestCalls, driver.withdrawCalls} {
		t.Fatalf("unresolved replay projection=%#v found=%t calls=%v/%v error=%v",
			projection, found, beforeCalls,
			[4]int{driver.commitCalls, driver.abortCalls, driver.attestCalls, driver.withdrawCalls}, err)
	}
}
