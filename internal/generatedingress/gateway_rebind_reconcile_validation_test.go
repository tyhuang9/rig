package generatedingress

import (
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	rebindValidationOperationID         = "11111111-1111-4111-8111-111111111111"
	rebindValidationOtherOperationID    = "22222222-2222-4222-8222-222222222222"
	rebindValidationActorID             = "33333333-3333-4333-8333-333333333333"
	rebindValidationOtherActorID        = "44444444-4444-4444-8444-444444444444"
	rebindValidationPredecessorID       = "55555555-5555-4555-8555-555555555555"
	rebindValidationPredecessorOpID     = "66666666-6666-4666-8666-666666666666"
	rebindValidationPredecessorConfigID = "77777777-7777-4777-8777-777777777777"
	rebindValidationSuccessorID         = "88888888-8888-4888-8888-888888888888"
	rebindValidationSuccessorOpID       = "99999999-9999-4999-8999-999999999999"
	rebindValidationRebindApproverID    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	rebindValidationConfigApproverID    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type gatewayRebindValidationFixture struct {
	policy               appaccess.GatewayRebindRecoveryPolicy
	review               appaccess.GatewayRebindReconcileReview
	activeStatus         GatewayRebindOperationInspection
	predecessorAuthority appaccess.GatewayCurrentAuthorityRef
	successorAuthority   appaccess.GatewayCurrentAuthorityRef
	receipt              string
}

func TestValidateGatewayRebindOperationInspectionCanonicalStates(t *testing.T) {
	fixture := newGatewayRebindValidationFixture(t, appaccess.GatewayRebindRecoveryEvidenceDecides)
	for _, test := range []struct {
		name  string
		value GatewayRebindOperationInspection
	}{
		{name: "validated absent", value: gatewayRebindValidationAbsent(GatewayRebindOperationValidatedAbsent)},
		{name: "unavailable", value: gatewayRebindValidationAbsent(GatewayRebindOperationUnavailable)},
		{name: "active", value: fixture.activeStatus},
		{name: "committed", value: fixture.terminalStatus(t, appaccess.GatewayRebindDispositionCommit)},
		{name: "rolled back", value: fixture.terminalStatus(t, appaccess.GatewayRebindDispositionAbort)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateGatewayRebindOperationInspection(test.value, rebindValidationOperationID); err != nil {
				t.Fatalf("canonical projection rejected: %v", err)
			}
		})
	}
}

func TestValidateGatewayRebindOperationInspectionRejectsContradictions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GatewayRebindOperationInspection)
	}{
		{name: "unknown availability", mutate: func(value *GatewayRebindOperationInspection) {
			*value = gatewayRebindValidationAbsent("future")
		}},
		{name: "absence carries operation", mutate: func(value *GatewayRebindOperationInspection) {
			*value = gatewayRebindValidationAbsent(GatewayRebindOperationValidatedAbsent)
			value.Operation.OperationID = rebindValidationOperationID
		}},
		{name: "cross bound operation", mutate: func(value *GatewayRebindOperationInspection) {
			value.OperationID = rebindValidationOtherOperationID
		}},
		{name: "active marked historical", mutate: func(value *GatewayRebindOperationInspection) {
			value.HistoricalTerminal = true
		}},
		{name: "database state without event", mutate: func(value *GatewayRebindOperationInspection) {
			value.Operation.StoredState = appaccess.GatewayRebindDatabaseCommitted
		}},
		{name: "database event on prepared", mutate: func(value *GatewayRebindOperationInspection) {
			value.Operation.DatabaseCommitObserved = true
			value.DatabaseCommittedEvent = &appaccess.GatewayRebindEvent{
				OperationID: rebindValidationOperationID, Sequence: 1,
				State: appaccess.GatewayRebindDatabaseCommitted,
			}
		}},
		{name: "active terminal proof", mutate: func(value *GatewayRebindOperationInspection) {
			value.TerminalProof = GatewayRebindProofVerified
			value.TerminalProofDigest = rebindValidationDigest("1")
		}},
		{name: "unknown current proof", mutate: func(value *GatewayRebindOperationInspection) {
			value.CurrentProof = "future"
			value.CurrentAttestationDigest = ""
		}},
		{name: "protected absence has phase", mutate: func(value *GatewayRebindOperationInspection) {
			value.Protected = &appaccess.GatewayRebindProtectedRecoveryEvidence{
				Presence: appaccess.GatewayRebindProtectedEvidenceAbsent, OperationID: rebindValidationOperationID,
				Generation: 2, ScanDigest: rebindValidationDigest("2"),
			}
			value.ProtectedPhase = "successor_intent"
		}},
		{name: "commit phase without commit disposition", mutate: func(value *GatewayRebindOperationInspection) {
			value.ProtectedPhase = "handover_committed"
		}},
		{name: "commit disposition before commit phase", mutate: func(value *GatewayRebindOperationInspection) {
			value.Protected.FixedDisposition = appaccess.GatewayRebindDispositionCommit
			value.Protected.TerminalReceiptDigest = rebindValidationDigest("3")
			value.ProtectedPhase = "stage_serving"
		}},
		{name: "action lacks verified current", mutate: func(value *GatewayRebindOperationInspection) {
			value.CurrentProof = GatewayRebindProofUnavailable
			value.CurrentAttestationDigest = ""
		}},
		{name: "unknown action", mutate: func(value *GatewayRebindOperationInspection) {
			value.AllowedActions = []GatewayRebindRecoveryAction{"future"}
		}},
		{name: "terminal held fence", mutate: func(value *GatewayRebindOperationInspection) {
			*value = newGatewayRebindValidationFixture(t,
				appaccess.GatewayRebindRecoveryEvidenceDecides).terminalStatus(t, appaccess.GatewayRebindDispositionCommit)
			value.OperationFence = GatewayRebindOperationFenceHeld
		}},
		{name: "terminal wrong disposition", mutate: func(value *GatewayRebindOperationInspection) {
			*value = newGatewayRebindValidationFixture(t,
				appaccess.GatewayRebindRecoveryEvidenceDecides).terminalStatus(t, appaccess.GatewayRebindDispositionCommit)
			value.Operation.TerminalDisposition = appaccess.GatewayRebindDispositionAbort
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := newGatewayRebindValidationFixture(t,
				appaccess.GatewayRebindRecoveryEvidenceDecides).activeStatus
			test.mutate(&value)
			if ValidateGatewayRebindOperationInspection(value, rebindValidationOperationID) == nil {
				t.Fatal("contradictory operation inspection was accepted")
			}
		})
	}
}

func TestValidateGatewayRebindRecoveryReviewBindsStatusAndConsent(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*gatewayRebindValidationFixture) GatewayRebindRecoveryReview
		valid   bool
	}{
		{name: "executable canonical review", valid: true, prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: f.activeStatus, AuthorizationReview: f.review}
		}},
		{name: "non executable absence has zero consent", valid: true, prepare: func(_ *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: gatewayRebindValidationAbsent(GatewayRebindOperationValidatedAbsent)}
		}},
		{name: "non executable terminal has zero consent", valid: true, prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: f.terminalStatus(t, appaccess.GatewayRebindDispositionCommit)}
		}},
		{name: "non executable status carries consent", prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: gatewayRebindValidationAbsent(GatewayRebindOperationValidatedAbsent), AuthorizationReview: f.review}
		}},
		{name: "executable status omits consent", prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion, Status: f.activeStatus}
		}},
		{name: "status sequence behind review", prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			status := f.activeStatus
			status.Operation.StateSequence++
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: status, AuthorizationReview: f.review}
		}},
		{name: "protected evidence differs from review", prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			status := f.activeStatus
			protected := *status.Protected
			protected.RecordDigest = rebindValidationDigest("4")
			status.Protected = &protected
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: status, AuthorizationReview: f.review}
		}},
		// ProtectedPhase is derived for display after the signed appaccess review;
		// this structural validator only requires the independently valid phase.
		{name: "valid structural display phase is independent of consent", valid: true, prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			status := f.activeStatus
			status.ProtectedPhase = "stage_intent"
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: status, AuthorizationReview: f.review}
		}},
		{name: "selected authority differs from review", prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			status := f.activeStatus
			selected := *status.SelectedCurrentAuthority
			selected.OperationID = rebindValidationOtherOperationID
			status.SelectedCurrentAuthority = &selected
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: status, AuthorizationReview: f.review}
		}},
		{name: "review belongs to another actor", prepare: func(f *gatewayRebindValidationFixture) GatewayRebindRecoveryReview {
			return GatewayRebindRecoveryReview{Version: GatewayRebindRecoveryReviewVersion,
				Status: f.activeStatus, AuthorizationReview: f.review}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindValidationFixture(t, appaccess.GatewayRebindRecoveryEvidenceDecides)
			actorID := rebindValidationActorID
			if test.name == "review belongs to another actor" {
				actorID = rebindValidationOtherActorID
			}
			err := ValidateGatewayRebindRecoveryReview(test.prepare(&fixture),
				rebindValidationOperationID, actorID)
			if test.valid && err != nil {
				t.Fatalf("canonical review rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("cross-bound recovery review was accepted")
			}
		})
	}
}

func TestValidateGatewayRebindReconcileResponseBindsTerminalAuthority(t *testing.T) {
	for _, test := range []struct {
		name        string
		policy      appaccess.GatewayRebindRecoveryPolicy
		disposition appaccess.GatewayRebindTerminalDisposition
		mutate      func(*GatewayRebindReconcileResponse, *GatewayRebindReconcileRequest, *string)
		valid       bool
	}{
		{name: "evidence decides commit", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit, valid: true},
		{name: "evidence decides rollback", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionAbort, valid: true},
		{name: "forward only commit", policy: appaccess.GatewayRebindRecoveryForwardOnly,
			disposition: appaccess.GatewayRebindDispositionCommit, valid: true},
		{name: "rollback only rollback", policy: appaccess.GatewayRebindRecoveryRollbackOnly,
			disposition: appaccess.GatewayRebindDispositionAbort, valid: true},
		{name: "wrong actor", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(_ *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, actor *string) {
				*actor = rebindValidationOtherActorID
			}},
		{name: "wrong request operation", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(_ *GatewayRebindReconcileResponse, request *GatewayRebindReconcileRequest, _ *string) {
				request.OperationID = rebindValidationOtherOperationID
			}},
		{name: "wrong recovery digest", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(_ *GatewayRebindReconcileResponse, request *GatewayRebindReconcileRequest, _ *string) {
				request.RecoveryDigest = rebindValidationDigest("5")
			}},
		{name: "wrong approval digest", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(_ *GatewayRebindReconcileResponse, request *GatewayRebindReconcileRequest, _ *string) {
				request.RecoveryApprovalDigest = rebindValidationDigest("6")
			}},
		{name: "missing audit receipt", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(response *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, _ *string) {
				response.Authorization.AuditEventID = 0
			}},
		{name: "missing terminal proof", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(response *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, _ *string) {
				response.Status.TerminalProof = GatewayRebindProofUnavailable
				response.Status.TerminalProofDigest = ""
			}},
		{name: "missing current proof", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(response *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, _ *string) {
				response.Status.CurrentProof = GatewayRebindProofUnavailable
				response.Status.CurrentAttestationDigest = ""
			}},
		{name: "unreleased fence", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(response *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, _ *string) {
				response.Status.OperationFence = GatewayRebindOperationFenceHeld
			}},
		{name: "protected receipt differs", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(response *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, _ *string) {
				protected := *response.Status.Protected
				protected.TerminalReceiptDigest = rebindValidationDigest("7")
				response.Status.Protected = &protected
			}},
		{name: "selected current differs", policy: appaccess.GatewayRebindRecoveryEvidenceDecides,
			disposition: appaccess.GatewayRebindDispositionCommit,
			mutate: func(response *GatewayRebindReconcileResponse, _ *GatewayRebindReconcileRequest, _ *string) {
				selected := *response.Status.SelectedCurrentAuthority
				selected.OperationID = rebindValidationOtherOperationID
				response.Status.SelectedCurrentAuthority = &selected
			}},
		{name: "forward policy rolls back", policy: appaccess.GatewayRebindRecoveryForwardOnly,
			disposition: appaccess.GatewayRebindDispositionAbort},
		{name: "rollback policy commits", policy: appaccess.GatewayRebindRecoveryRollbackOnly,
			disposition: appaccess.GatewayRebindDispositionCommit},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindValidationFixture(t, test.policy)
			request := GatewayRebindReconcileRequest{
				Version: GatewayRebindReconcileVersion, OperationID: rebindValidationOperationID,
				RecoveryDigest:         fixture.review.RecoveryDigest,
				RecoveryApprovalDigest: fixture.review.RecoveryApprovalDigest,
			}
			response := GatewayRebindReconcileResponse{
				Version: GatewayRebindReconcileVersion,
				Status:  fixture.terminalStatus(t, test.disposition),
				Authorization: appaccess.GatewayRebindReconcileAuthorization{
					Review: fixture.review, ActorID: rebindValidationActorID,
					AuditEventID: 1, AuditedAt: time.Unix(20, 0).UTC(),
				},
			}
			actorID := rebindValidationActorID
			if test.mutate != nil {
				test.mutate(&response, &request, &actorID)
			}
			err := ValidateGatewayRebindReconcileResponse(response, request, actorID)
			if test.valid && err != nil {
				t.Fatalf("canonical reconcile response rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("cross-bound reconcile response was accepted")
			}
		})
	}
}

func newGatewayRebindValidationFixture(t *testing.T,
	policy appaccess.GatewayRebindRecoveryPolicy,
) gatewayRebindValidationFixture {
	t.Helper()
	predecessorSpec := appaccess.GatewayProfileSpec{
		SelectedIPv4: "192.168.50.2", InterfaceID: "test-interface",
		PortStart: appaccess.GatewayPortStart, PortEnd: appaccess.GatewayPortEnd,
	}
	successorSpec := predecessorSpec
	successorSpec.SelectedIPv4 = "192.168.50.3"
	predecessorDigest, err := appaccess.GatewayProfileSpecDigest(predecessorSpec)
	if err != nil {
		t.Fatal(err)
	}
	successorDigest, err := appaccess.GatewayProfileSpecDigest(successorSpec)
	if err != nil {
		t.Fatal(err)
	}
	rosterDigest, err := appaccess.GatewayRebindRosterV2Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDigest, err := appaccess.GatewayRebindRuntimeHeadsV2Digest(rebindValidationOperationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	lineage := appaccess.GatewayCurrentLineageRef{
		Kind: appaccess.GatewayRebindSourceGatewayUpgrade, OperationID: rebindValidationPredecessorOpID,
		ProfileRevisionID: rebindValidationPredecessorID, ProfileRevisionNumber: 1,
		ProfileSpecDigest: predecessorDigest, ProtectedGeneration: 1,
		ProtectedIdentityDigest: rebindValidationDigest("8"),
		ProtectedJournalDigest:  rebindValidationDigest("9"),
	}
	claim := appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{
		Version: appaccess.GatewayRebindSpecVersionV2, OperationID: rebindValidationOperationID,
		Predecessor: appaccess.GatewayRebindSourceRef{
			Lineage: lineage, SourceStateVersion: 2, SourceStateRevision: 0,
			SourceStateDigest:           rebindValidationDigest("a"),
			PredecessorCheckpointDigest: rebindValidationDigest("b"),
		},
		SuccessorProtectedGeneration: 2,
		SuccessorProfileRevisionID:   rebindValidationSuccessorID, SuccessorProfileRevisionNumber: 2,
		SuccessorProfileOperationID: rebindValidationSuccessorOpID, SuccessorProfile: successorSpec,
		RosterVersion: appaccess.GatewayRebindRosterVersionV2, RosterDigest: rosterDigest,
		RuntimeHeadsVersion: appaccess.GatewayRebindRuntimeHeadsVersionV1, RuntimeHeadsDigest: runtimeDigest,
	}, State: appaccess.GatewayRebindPrepared, StateSequence: 1}
	claim.RebindApproval = appaccess.Approval{
		Action: appaccess.ActionRebindGateway, ActorID: rebindValidationRebindApproverID,
	}
	claim.RebindApproval.SpecDigest, err = appaccess.GatewayRebindSpecV2Digest(claim.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim.ConfigureApproval = appaccess.Approval{
		Action: appaccess.ActionConfigureGateway, SpecDigest: successorDigest,
		ActorID: rebindValidationConfigApproverID,
	}
	claim.RequestDigest, err = canonicalDigest(struct {
		Spec      appaccess.GatewayRebindSpecV2 `json:"spec"`
		Rebind    appaccess.Approval            `json:"rebindApproval"`
		Configure appaccess.Approval            `json:"configureApproval"`
	}{claim.Spec, claim.RebindApproval, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
	claim.SuccessorProfileRequestDigest, err = canonicalDigest(struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{1, successorSpec, claim.ConfigureApproval})
	if err != nil {
		t.Fatal(err)
	}
	claim.RebindApprovedAt = time.Unix(1, 0).UTC()
	claim.ConfigureApprovedAt = time.Unix(2, 0).UTC()
	claim.CreatedAt, claim.UpdatedAt = time.Unix(3, 0).UTC(), time.Unix(3, 0).UTC()
	active := appaccess.GatewayRebindHistoryEntry{
		Claim: appaccess.GatewayRebindClaimRecord{
			SpecVersion: appaccess.GatewayRebindSpecVersionV2, V2: &claim,
		},
		Events: []appaccess.GatewayRebindEvent{{
			OperationID: rebindValidationOperationID, Sequence: 1,
			State: appaccess.GatewayRebindPrepared, CreatedAt: claim.CreatedAt,
		}},
	}
	predecessorAuthority := appaccess.GatewayCurrentAuthorityRef{
		Kind: lineage.Kind, OperationID: lineage.OperationID,
		ProfileRevisionID: lineage.ProfileRevisionID, ProfileRevisionNumber: lineage.ProfileRevisionNumber,
		ProfileSpecDigest: lineage.ProfileSpecDigest,
	}
	currentProfile := appaccess.GatewayProfileRevision{
		ID: rebindValidationPredecessorID, RevisionNumber: 1,
		OperationID: rebindValidationPredecessorConfigID, Spec: predecessorSpec,
		SpecDigest: predecessorDigest, ApprovedBy: rebindValidationConfigApproverID,
		ApprovedAt: time.Unix(1, 0).UTC(),
	}
	receipt := rebindValidationDigest("c")
	protected := appaccess.GatewayRebindProtectedRecoveryEvidence{
		Presence: appaccess.GatewayRebindProtectedEvidencePresent, OperationID: rebindValidationOperationID,
		Generation: 2, ScanDigest: rebindValidationDigest("d"), IntentDigest: rebindValidationDigest("e"),
		RecordSequence: 1, RecordDigest: rebindValidationDigest("f"),
		FixedDisposition: appaccess.GatewayRebindDispositionNone,
	}
	phase := "successor_intent"
	switch policy {
	case appaccess.GatewayRebindRecoveryForwardOnly:
		protected.FixedDisposition = appaccess.GatewayRebindDispositionCommit
		protected.TerminalReceiptDigest = receipt
		phase = "handover_committed"
	case appaccess.GatewayRebindRecoveryRollbackOnly:
		protected.FixedDisposition = appaccess.GatewayRebindDispositionAbort
		protected.TerminalReceiptDigest = receipt
		phase = "handover_rolled_back"
	case appaccess.GatewayRebindRecoveryEvidenceDecides:
	default:
		t.Fatalf("unknown test recovery policy %q", policy)
	}
	trusted := appaccess.GatewayRebindTrustedRecoverySnapshot{
		Protected: protected,
		SelectedCurrent: appaccess.GatewayRebindSelectedCurrentEvidence{
			Lineage: lineage, StateVersion: 2, StateRevision: 0,
			StateDigest: claim.Spec.Predecessor.SourceStateDigest,
		},
	}
	snapshot := appaccess.GatewayRebindRecoverySnapshot{
		History: []appaccess.GatewayRebindHistoryEntry{active}, Active: &active,
		CurrentProfile: &currentProfile, CurrentSource: &predecessorAuthority,
		Phase: appaccess.GatewayRebindPrepared, RollbackAllowed: true,
	}
	review, err := appaccess.BuildGatewayRebindReconcileReview(snapshot, trusted, rebindValidationActorID)
	if err != nil {
		t.Fatalf("build canonical recovery review: %v", err)
	}
	protectedCopy, currentCopy := protected, predecessorAuthority
	activeStatus := GatewayRebindOperationInspection{
		Version: GatewayRebindOperationInspectionVersion, OperationID: rebindValidationOperationID,
		Availability: GatewayRebindOperationAvailable,
		Operation: GatewayRebindOperationStatus{
			OperationID: rebindValidationOperationID, StoredState: appaccess.GatewayRebindPrepared,
			StateSequence: 1, TerminalDisposition: appaccess.GatewayRebindDispositionNone,
		},
		Active: true, Protected: &protectedCopy, ProtectedPhase: phase,
		SelectedCurrentAuthority: &currentCopy,
		TerminalProof:            GatewayRebindProofUnavailable, CurrentProof: GatewayRebindProofVerified,
		CurrentAttestationDigest: rebindValidationDigest("1"),
		OperationFence:           GatewayRebindOperationFenceHeld, RecoveryPolicy: policy,
		AllowedActions: []GatewayRebindRecoveryAction{GatewayRebindRecoveryActionReconcile},
	}
	successorAuthority := appaccess.GatewayCurrentAuthorityRef{
		Kind: appaccess.GatewayRebindSourceGatewayRebind, OperationID: rebindValidationOperationID,
		ProfileRevisionID: rebindValidationSuccessorID, ProfileRevisionNumber: 2,
		ProfileSpecDigest: successorDigest, TerminalReceiptDigest: receipt,
	}
	return gatewayRebindValidationFixture{
		policy: policy, review: review, activeStatus: activeStatus,
		predecessorAuthority: predecessorAuthority, successorAuthority: successorAuthority, receipt: receipt,
	}
}

func (f gatewayRebindValidationFixture) terminalStatus(t *testing.T,
	disposition appaccess.GatewayRebindTerminalDisposition,
) GatewayRebindOperationInspection {
	t.Helper()
	status := GatewayRebindOperationInspection{
		Version: GatewayRebindOperationInspectionVersion, OperationID: rebindValidationOperationID,
		Availability: GatewayRebindOperationAvailable, HistoricalTerminal: true,
		TerminalProof: GatewayRebindProofVerified, TerminalProofDigest: rebindValidationDigest("2"),
		CurrentProof: GatewayRebindProofVerified, CurrentAttestationDigest: rebindValidationDigest("3"),
		OperationFence: GatewayRebindOperationFenceReleased,
		AllowedActions: []GatewayRebindRecoveryAction{},
	}
	protected := f.review.Snapshot.Trusted.Protected
	protected.TerminalReceiptDigest = f.receipt
	status.Operation = GatewayRebindOperationStatus{
		OperationID: rebindValidationOperationID, StateSequence: 3,
		TerminalDisposition: disposition, TerminalReceiptDigest: f.receipt,
	}
	switch disposition {
	case appaccess.GatewayRebindDispositionCommit:
		status.Operation.StoredState = appaccess.GatewayRebindCommitted
		status.Operation.DatabaseCommitObserved = true
		status.DatabaseCommittedEvent = &appaccess.GatewayRebindEvent{
			OperationID: rebindValidationOperationID, Sequence: 2,
			State: appaccess.GatewayRebindDatabaseCommitted, CreatedAt: time.Unix(4, 0).UTC(),
		}
		protected.FixedDisposition = appaccess.GatewayRebindDispositionCommit
		status.ProtectedPhase = "handover_committed"
		selected := f.successorAuthority
		status.SelectedCurrentAuthority = &selected
	case appaccess.GatewayRebindDispositionAbort:
		status.Operation.StoredState = appaccess.GatewayRebindRolledBack
		protected.FixedDisposition = appaccess.GatewayRebindDispositionAbort
		status.ProtectedPhase = "handover_rolled_back"
		selected := f.predecessorAuthority
		status.SelectedCurrentAuthority = &selected
	default:
		t.Fatalf("unknown terminal disposition %q", disposition)
	}
	status.Protected = &protected
	return status
}

func gatewayRebindValidationAbsent(availability GatewayRebindOperationAvailability,
) GatewayRebindOperationInspection {
	proof := GatewayRebindProofUnknown
	if availability == GatewayRebindOperationUnavailable {
		proof = GatewayRebindProofUnavailable
	}
	return GatewayRebindOperationInspection{
		Version: GatewayRebindOperationInspectionVersion, OperationID: rebindValidationOperationID,
		Availability: availability, TerminalProof: proof, CurrentProof: proof,
		OperationFence: GatewayRebindOperationFenceUnavailable,
		AllowedActions: []GatewayRebindRecoveryAction{},
	}
}

func rebindValidationDigest(character string) string {
	return strings.Repeat(character, 64)
}
