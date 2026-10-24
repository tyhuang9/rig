package appaccess

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGatewayRebindReconcileAuthorizationA0BindsPreviewAndAudit(t *testing.T) {
	fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
	snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
	trusted := gatewayRebindA0TrustedSnapshot(claim)
	preview, err := BuildGatewayRebindReconcileReview(snapshot, trusted, testAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Snapshot.Policy != GatewayRebindRecoveryEvidenceDecides ||
		preview.Snapshot.Trusted.Protected.Presence != GatewayRebindProtectedEvidenceAbsent ||
		!validDigest(preview.RecoveryDigest) || !validDigest(preview.RecoveryApprovalDigest) {
		t.Fatalf("invalid A0 preview: %#v", preview)
	}
	otherActor, err := BuildGatewayRebindReconcileReview(snapshot, trusted, testViewer)
	if err != nil {
		t.Fatal(err)
	}
	if otherActor.RecoveryDigest != preview.RecoveryDigest ||
		otherActor.RecoveryApprovalDigest == preview.RecoveryApprovalDigest {
		t.Fatal("recovery digest was actor-bound or approval digest was not actor-bound")
	}

	input := gatewayRebindAuthorizationInput(preview, testAdministrator)
	authorization, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(
		context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(authorization.Review, preview) || authorization.ActorID != testAdministrator ||
		authorization.AuditEventID <= 0 || !authorization.AuditedAt.Equal(testNow) {
		t.Fatalf("authorization did not preserve exact review: %#v", authorization)
	}

	var actorID, action, resourceType, resourceID, metadataJSON, createdAt string
	if err := fixture.db.QueryRow(`SELECT actor_id,action,resource_type,resource_id,metadata_json,created_at
		FROM audit_events WHERE id=?`, authorization.AuditEventID).Scan(
		&actorID, &action, &resourceType, &resourceID, &metadataJSON, &createdAt); err != nil {
		t.Fatal(err)
	}
	if actorID != testAdministrator || action != GatewayRebindReconcilePurpose ||
		resourceType != gatewayRebindAuditResourceType || resourceID != claim.Spec.OperationID ||
		createdAt != formatTime(testNow) || strings.Contains(metadataJSON, "protectedPhase") {
		t.Fatalf("wrong audit identity: actor=%q action=%q type=%q resource=%q metadata=%s at=%q",
			actorID, action, resourceType, resourceID, metadataJSON, createdAt)
	}
	var metadata struct {
		RecoveryDigest         string                                 `json:"recoveryDigest"`
		RecoveryApprovalDigest string                                 `json:"recoveryApprovalDigest"`
		RecoveryPolicy         GatewayRebindRecoveryPolicy            `json:"recoveryPolicy"`
		State                  GatewayRebindState                     `json:"state"`
		StateSequence          int64                                  `json:"stateSequence"`
		Protected              GatewayRebindProtectedRecoveryEvidence `json:"protected"`
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.RecoveryDigest != preview.RecoveryDigest ||
		metadata.RecoveryApprovalDigest != preview.RecoveryApprovalDigest ||
		metadata.RecoveryPolicy != GatewayRebindRecoveryEvidenceDecides ||
		metadata.State != GatewayRebindPrepared || metadata.StateSequence != 1 ||
		metadata.Protected != trusted.Protected {
		t.Fatalf("audit did not bind canonical A0 snapshot: %#v", metadata)
	}

	repeated, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(
		context.Background(), input)
	if err != nil || repeated.AuditEventID == authorization.AuditEventID {
		t.Fatalf("repeated attempted consent: %#v error=%v", repeated, err)
	}
	assertGatewayRebindReconcileAuditCount(t, fixture, 2)
}

func TestGatewayRebindCheckpointTerminalRecoveryPolicy(t *testing.T) {
	fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
	snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
	receipt := strings.Repeat("7", 64)
	trusted := gatewayRebindA0TrustedSnapshot(claim)
	trusted.Protected.Presence = GatewayRebindProtectedEvidenceCheckpointTerminal
	trusted.Protected.FixedDisposition = GatewayRebindDispositionAbort
	trusted.Protected.TerminalReceiptDigest = receipt
	review, err := BuildGatewayRebindReconcileReview(snapshot, trusted, testAdministrator)
	if err != nil || review.Snapshot.Policy != GatewayRebindRecoveryRollbackOnly ||
		review.Snapshot.Trusted.Protected != trusted.Protected ||
		ValidateGatewayRebindReconcileReview(review, testAdministrator) != nil {
		t.Fatalf("checkpoint-terminal review=%#v error=%v", review, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*GatewayRebindTrustedRecoverySnapshot)
	}{
		{"intent digest", func(v *GatewayRebindTrustedRecoverySnapshot) { v.Protected.IntentDigest = strings.Repeat("1", 64) }},
		{"record sequence", func(v *GatewayRebindTrustedRecoverySnapshot) { v.Protected.RecordSequence = 1 }},
		{"record digest", func(v *GatewayRebindTrustedRecoverySnapshot) { v.Protected.RecordDigest = strings.Repeat("2", 64) }},
		{"commit disposition", func(v *GatewayRebindTrustedRecoverySnapshot) {
			v.Protected.FixedDisposition = GatewayRebindDispositionCommit
		}},
		{"missing receipt", func(v *GatewayRebindTrustedRecoverySnapshot) { v.Protected.TerminalReceiptDigest = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := trusted
			test.mutate(&invalid)
			if _, err := BuildGatewayRebindReconcileReview(snapshot, invalid,
				testAdministrator); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("malformed checkpoint terminal error=%v", err)
			}
		})
	}

	t.Run("later SQL phase", func(t *testing.T) {
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, receipt, nil)); err != nil {
			t.Fatal(err)
		}
		later := gatewayRebindAuthorizationSnapshot(t, fixture)
		if _, err := BuildGatewayRebindReconcileReview(later, trusted,
			testAdministrator); !errors.Is(err, ErrConflict) {
			t.Fatalf("later SQL phase checkpoint terminal error=%v", err)
		}
	})
}

func TestGatewayRebindReconcileAuthorizationRejectsWithoutAudit(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, fixture gatewayRebindFixture, claim GatewayRebindClaimV2,
			snapshot GatewayRebindRecoverySnapshot, trusted GatewayRebindTrustedRecoverySnapshot,
			preview GatewayRebindReconcileReview) error
		want    error
		wantAny bool
	}{
		{
			name: "wrong operation",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, trusted GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				input := gatewayRebindAuthorizationInput(preview, testAdministrator)
				input.OperationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(), input)
				return err
			},
			want: ErrConflict,
		},
		{
			name: "stale recovery digest",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				input := gatewayRebindAuthorizationInput(preview, testAdministrator)
				input.RecoveryDigest = strings.Repeat("f", 64)
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(), input)
				return err
			},
			want: ErrConflict,
		},
		{
			name: "wrong actor consent",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				input := gatewayRebindAuthorizationInput(preview, testAdministrator)
				input.RecoveryApprovalDigest = strings.Repeat("e", 64)
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(), input)
				return err
			},
			want: ErrApprovalRequired,
		},
		{
			name: "current actor is viewer",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				snapshot GatewayRebindRecoverySnapshot, trusted GatewayRebindTrustedRecoverySnapshot,
				_ GatewayRebindReconcileReview,
			) error {
				viewer, err := BuildGatewayRebindReconcileReview(snapshot, trusted, testViewer)
				if err != nil {
					t.Fatal(err)
				}
				_, err = fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(),
					gatewayRebindAuthorizationInput(viewer, testViewer))
				return err
			},
			want: ErrApprovalRequired,
		},
		{
			name: "original configure approver revoked",
			run: func(t *testing.T, fixture gatewayRebindFixture, claim GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`,
					claim.ConfigureApproval.ActorID); err != nil {
					t.Fatal(err)
				}
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(),
					gatewayRebindAuthorizationInput(preview, testAdministrator))
				return err
			},
			want: ErrApprovalRequired,
		},
		{
			name: "original rebind approver revoked",
			run: func(t *testing.T, fixture gatewayRebindFixture, claim GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`,
					claim.RebindApproval.ActorID); err != nil {
					t.Fatal(err)
				}
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(),
					gatewayRebindAuthorizationInput(preview, testAdministrator))
				return err
			},
			want: ErrApprovalRequired,
		},
		{
			name: "SQL phase changed after preview",
			run: func(t *testing.T, fixture gatewayRebindFixture, claim GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				receipt := strings.Repeat("9", 64)
				if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
					gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
						GatewayRebindSuccessorReady, receipt, nil)); err != nil {
					t.Fatal(err)
				}
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(),
					gatewayRebindAuthorizationInput(preview, testAdministrator))
				return err
			},
			want: ErrConflict,
		},
		{
			name: "validated absence cannot contain progress",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, trusted GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				trusted.Protected.IntentDigest = strings.Repeat("a", 64)
				input := gatewayRebindAuthorizationInput(preview, testAdministrator)
				input.Trusted = trusted
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(), input)
				return err
			},
			want: ErrInvalidInput,
		},
		{
			name: "unknown scan cannot masquerade as absence",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, trusted GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				trusted.Protected.Presence = GatewayRebindProtectedEvidencePresence("unknown")
				input := gatewayRebindAuthorizationInput(preview, testAdministrator)
				input.Trusted = trusted
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(), input)
				return err
			},
			want: ErrInvalidInput,
		},
		{
			name: "selected current changed",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, trusted GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				trusted.SelectedCurrent.StateDigest = strings.Repeat("b", 64)
				input := gatewayRebindAuthorizationInput(preview, testAdministrator)
				input.Trusted = trusted
				_, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(context.Background(), input)
				return err
			},
			want: ErrConflict,
		},
		{
			name: "canceled immediately before audit insert",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				ctx, cancel := context.WithCancel(context.Background())
				fixture.repository.now = func() time.Time {
					cancel()
					return testNow
				}
				authorization, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(ctx,
					gatewayRebindAuthorizationInput(preview, testAdministrator))
				if authorization != (GatewayRebindReconcileAuthorization{}) {
					t.Fatalf("canceled request returned authorization: %#v", authorization)
				}
				return err
			},
			want: context.Canceled,
		},
		{
			name: "audit insert failure",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				if _, err := fixture.db.Exec(`CREATE TRIGGER test_rebind_reconcile_audit_insert_failure
					BEFORE INSERT ON audit_events
					WHEN NEW.resource_type='lan_gateway_rebind'
					BEGIN SELECT RAISE(ABORT, 'test audit insert failure'); END`); err != nil {
					t.Fatal(err)
				}
				authorization, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(
					context.Background(), gatewayRebindAuthorizationInput(preview, testAdministrator))
				if authorization != (GatewayRebindReconcileAuthorization{}) {
					t.Fatalf("audit insert failure returned authorization: %#v", authorization)
				}
				return err
			},
			wantAny: true,
		},
		{
			name: "transaction commit failure",
			run: func(t *testing.T, fixture gatewayRebindFixture, _ GatewayRebindClaimV2,
				_ GatewayRebindRecoverySnapshot, _ GatewayRebindTrustedRecoverySnapshot,
				preview GatewayRebindReconcileReview,
			) error {
				for _, statement := range []string{
					`CREATE TABLE test_rebind_reconcile_audit_parent(id INTEGER PRIMARY KEY)`,
					`CREATE TABLE test_rebind_reconcile_audit_child(
						id INTEGER PRIMARY KEY,
						parent_id INTEGER NOT NULL REFERENCES test_rebind_reconcile_audit_parent(id)
							DEFERRABLE INITIALLY DEFERRED
					)`,
					`CREATE TRIGGER test_rebind_reconcile_audit_commit_failure
						AFTER INSERT ON audit_events
						WHEN NEW.resource_type='lan_gateway_rebind'
						BEGIN
							INSERT INTO test_rebind_reconcile_audit_child(id,parent_id)
							VALUES(NEW.id,NEW.id);
						END`,
				} {
					if _, err := fixture.db.Exec(statement); err != nil {
						t.Fatal(err)
					}
				}
				authorization, err := fixture.repository.AuthorizeAndAuditGatewayRebindReconcile(
					context.Background(), gatewayRebindAuthorizationInput(preview, testAdministrator))
				if authorization != (GatewayRebindReconcileAuthorization{}) {
					t.Fatalf("commit failure returned authorization: %#v", authorization)
				}
				return err
			},
			wantAny: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
			snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
			trusted := gatewayRebindA0TrustedSnapshot(claim)
			preview, err := BuildGatewayRebindReconcileReview(snapshot, trusted, testAdministrator)
			if err != nil {
				t.Fatal(err)
			}
			err = test.run(t, fixture, claim, snapshot, trusted, preview)
			if test.wantAny && err == nil {
				t.Fatal("expected authorization failure")
			}
			if !test.wantAny && !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
			assertGatewayRebindReconcileAuditCount(t, fixture, 0)
		})
	}
}

func TestGatewayRebindReconcileReviewDerivesFixedPolicyAndRejectsContradictions(t *testing.T) {
	t.Run("prepared evidence decides and fixed directions", func(t *testing.T) {
		fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
		snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
		base := gatewayRebindA0TrustedSnapshot(claim)
		for _, test := range []struct {
			name       string
			trusted    GatewayRebindTrustedRecoverySnapshot
			wantPolicy GatewayRebindRecoveryPolicy
		}{
			{name: "validated absence", trusted: base, wantPolicy: GatewayRebindRecoveryEvidenceDecides},
			{name: "progress without fixed direction", trusted: gatewayRebindPresentTrustedSnapshot(
				base, GatewayRebindDispositionNone, ""), wantPolicy: GatewayRebindRecoveryEvidenceDecides},
			{name: "protected commit", trusted: gatewayRebindPresentTrustedSnapshot(
				base, GatewayRebindDispositionCommit, ""), wantPolicy: GatewayRebindRecoveryForwardOnly},
			{name: "protected abort", trusted: gatewayRebindPresentTrustedSnapshot(
				base, GatewayRebindDispositionAbort, ""), wantPolicy: GatewayRebindRecoveryRollbackOnly},
		} {
			t.Run(test.name, func(t *testing.T) {
				review, err := BuildGatewayRebindReconcileReview(snapshot, test.trusted, testAdministrator)
				if err != nil || review.Snapshot.Policy != test.wantPolicy {
					t.Fatalf("review=%#v error=%v want=%q", review, err, test.wantPolicy)
				}
			})
		}
	})

	t.Run("retained commit requires exact protected decision", func(t *testing.T) {
		fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
		receipt := strings.Repeat("9", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, receipt, nil)); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
		base := gatewayRebindA0TrustedSnapshot(claim)
		exact := gatewayRebindPresentTrustedSnapshot(base, GatewayRebindDispositionCommit, receipt)
		review, err := BuildGatewayRebindReconcileReview(snapshot, exact, testAdministrator)
		if err != nil || review.Snapshot.Policy != GatewayRebindRecoveryForwardOnly ||
			review.Snapshot.RetainedCommand == nil ||
			review.Snapshot.RetainedCommand.TerminalReceiptDigest != receipt {
			t.Fatalf("exact retained commit review=%#v error=%v", review, err)
		}
		for name, contradictory := range map[string]GatewayRebindTrustedRecoverySnapshot{
			"absent protected record": base,
			"protected abort": gatewayRebindPresentTrustedSnapshot(
				base, GatewayRebindDispositionAbort, receipt),
			"different receipt": gatewayRebindPresentTrustedSnapshot(
				base, GatewayRebindDispositionCommit, strings.Repeat("8", 64)),
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := BuildGatewayRebindReconcileReview(snapshot, contradictory,
					testAdministrator); !errors.Is(err, ErrConflict) {
					t.Fatalf("contradiction error=%v", err)
				}
			})
		}
	})

	t.Run("retained abort cannot be overridden from unresolved", func(t *testing.T) {
		fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
		receipt := strings.Repeat("7", 64)
		proof := gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindUnresolved, receipt, nil)
		proof.TerminalDisposition = GatewayRebindDispositionAbort
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), proof); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
		base := gatewayRebindA0TrustedSnapshot(claim)
		exact := gatewayRebindPresentTrustedSnapshot(base, GatewayRebindDispositionAbort, receipt)
		review, err := BuildGatewayRebindReconcileReview(snapshot, exact, testAdministrator)
		if err != nil || review.Snapshot.Policy != GatewayRebindRecoveryRollbackOnly {
			t.Fatalf("retained abort review=%#v error=%v", review, err)
		}
		commit := gatewayRebindPresentTrustedSnapshot(base, GatewayRebindDispositionCommit, receipt)
		if _, err := BuildGatewayRebindReconcileReview(snapshot, commit,
			testAdministrator); !errors.Is(err, ErrConflict) {
			t.Fatalf("retained abort override error=%v", err)
		}
	})

	t.Run("database commit is forward only and rejects abort", func(t *testing.T) {
		fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
		receipt := strings.Repeat("6", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, receipt, nil)); err != nil {
			t.Fatal(err)
		}
		beforeCommit := gatewayRebindAuthorizationSnapshot(t, fixture)
		transfer := gatewayRebindV2AuthorizationTransfer(t, beforeCommit, receipt)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForClaimV2(t, claim, GatewayRebindSuccessorReady, 2,
				GatewayRebindDatabaseCommitted, receipt,
				[]GatewayRebindAllocationTransfer{transfer})); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
		base := gatewayRebindA0TrustedSnapshot(claim)
		exact := gatewayRebindPresentTrustedSnapshot(base, GatewayRebindDispositionCommit, receipt)
		exact.SelectedCurrent = GatewayRebindSelectedCurrentEvidence{
			Lineage: GatewayCurrentLineageRef{
				Kind: GatewayRebindSourceGatewayRebind, OperationID: claim.Spec.OperationID,
				ProfileRevisionID:       claim.Spec.SuccessorProfileRevisionID,
				ProfileRevisionNumber:   claim.Spec.SuccessorProfileRevisionNumber,
				ProfileSpecDigest:       claim.ConfigureApproval.SpecDigest,
				ProtectedGeneration:     claim.Spec.SuccessorProtectedGeneration,
				ProtectedIdentityDigest: strings.Repeat("5", 64),
				ProtectedIntentDigest:   exact.Protected.IntentDigest,
				TerminalReceiptDigest:   receipt,
			},
			StateVersion: 1, StateRevision: 1, StateDigest: strings.Repeat("4", 64),
		}
		review, err := BuildGatewayRebindReconcileReview(snapshot, exact, testAdministrator)
		if err != nil || review.Snapshot.Policy != GatewayRebindRecoveryForwardOnly ||
			review.Snapshot.DatabaseCommittedEvent == nil ||
			review.Snapshot.CurrentDatabaseCommittedEvent == nil {
			t.Fatalf("database-committed review=%#v error=%v", review, err)
		}
		for name, invalid := range map[string]GatewayRebindTrustedRecoverySnapshot{
			"wrong rebind state version": func() GatewayRebindTrustedRecoverySnapshot {
				value := exact
				value.SelectedCurrent.StateVersion = 2
				return value
			}(),
			"zero rebind state revision": func() GatewayRebindTrustedRecoverySnapshot {
				value := exact
				value.SelectedCurrent.StateRevision = 0
				return value
			}(),
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := BuildGatewayRebindReconcileReview(snapshot, invalid,
					testAdministrator); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("invalid selected-current tuple error=%v", err)
				}
			})
		}
		for name, contradictory := range map[string]GatewayRebindTrustedRecoverySnapshot{
			"different successor protected generation": func() GatewayRebindTrustedRecoverySnapshot {
				value := exact
				value.SelectedCurrent.Lineage.ProtectedGeneration++
				return value
			}(),
			"different successor protected intent": func() GatewayRebindTrustedRecoverySnapshot {
				value := exact
				value.SelectedCurrent.Lineage.ProtectedIntentDigest = strings.Repeat("8", 64)
				return value
			}(),
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := BuildGatewayRebindReconcileReview(snapshot, contradictory,
					testAdministrator); !errors.Is(err, ErrConflict) {
					t.Fatalf("contradictory active successor error=%v", err)
				}
			})
		}
		abort := exact
		abort.Protected.FixedDisposition = GatewayRebindDispositionAbort
		if _, err := BuildGatewayRebindReconcileReview(snapshot, abort,
			testAdministrator); !errors.Is(err, ErrConflict) {
			t.Fatalf("database commit plus abort error=%v", err)
		}
	})

	t.Run("terminal claim has no active recovery authorization", func(t *testing.T) {
		fixture, claim := gatewayRebindDistinctApprovalFixture(t, historicalAppAccessDB(t))
		receipt := strings.Repeat("3", 64)
		proof := gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindRolledBack, receipt, nil)
		proof.TerminalDisposition = GatewayRebindDispositionAbort
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), proof); err != nil {
			t.Fatal(err)
		}
		snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
		if snapshot.Active != nil {
			t.Fatal("terminal claim remained active")
		}
		trusted := gatewayRebindPresentTrustedSnapshot(gatewayRebindA0TrustedSnapshot(claim),
			GatewayRebindDispositionAbort, receipt)
		if _, err := BuildGatewayRebindReconcileReview(snapshot, trusted,
			testAdministrator); !errors.Is(err, ErrConflict) {
			t.Fatalf("terminal authorization error=%v", err)
		}
	})
}

func gatewayRebindAuthorizationSnapshot(t *testing.T,
	fixture gatewayRebindFixture,
) GatewayRebindRecoverySnapshot {
	t.Helper()
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func gatewayRebindA0TrustedSnapshot(claim GatewayRebindClaimV2) GatewayRebindTrustedRecoverySnapshot {
	return GatewayRebindTrustedRecoverySnapshot{
		Protected: GatewayRebindProtectedRecoveryEvidence{
			Presence: GatewayRebindProtectedEvidenceAbsent, OperationID: claim.Spec.OperationID,
			Generation: claim.Spec.SuccessorProtectedGeneration, ScanDigest: strings.Repeat("1", 64),
			FixedDisposition: GatewayRebindDispositionNone,
		},
		SelectedCurrent: GatewayRebindSelectedCurrentEvidence{
			Lineage:       claim.Spec.Predecessor.Lineage,
			StateVersion:  claim.Spec.Predecessor.SourceStateVersion,
			StateRevision: claim.Spec.Predecessor.SourceStateRevision,
			StateDigest:   claim.Spec.Predecessor.SourceStateDigest,
		},
	}
}

func gatewayRebindPresentTrustedSnapshot(base GatewayRebindTrustedRecoverySnapshot,
	disposition GatewayRebindTerminalDisposition, receipt string,
) GatewayRebindTrustedRecoverySnapshot {
	base.Protected.Presence = GatewayRebindProtectedEvidencePresent
	base.Protected.IntentDigest = strings.Repeat("2", 64)
	base.Protected.RecordSequence = 1
	base.Protected.RecordDigest = strings.Repeat("3", 64)
	base.Protected.FixedDisposition = disposition
	base.Protected.TerminalReceiptDigest = receipt
	return base
}

func gatewayRebindAuthorizationInput(review GatewayRebindReconcileReview,
	actorID string,
) GatewayRebindReconcileAuthorizationInput {
	return GatewayRebindReconcileAuthorizationInput{
		OperationID: review.Snapshot.Claim.Spec.OperationID, ActorID: actorID,
		RecoveryDigest:         review.RecoveryDigest,
		RecoveryApprovalDigest: review.RecoveryApprovalDigest,
		Trusted:                review.Snapshot.Trusted,
	}
}

func assertGatewayRebindReconcileAuditCount(t *testing.T, fixture gatewayRebindFixture, want int) {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE resource_type=? AND action=?`, gatewayRebindAuditResourceType,
		GatewayRebindReconcilePurpose).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("rebind reconcile audit count=%d want=%d", count, want)
	}
}

func gatewayRebindV2AuthorizationTransfer(t *testing.T, snapshot GatewayRebindRecoverySnapshot,
	receipt string,
) GatewayRebindAllocationTransfer {
	t.Helper()
	if snapshot.Active == nil || snapshot.Active.Claim.V2 == nil || len(snapshot.Active.RosterV2) != 1 {
		t.Fatalf("invalid V2 transfer fixture: %#v", snapshot.Active)
	}
	claim, entry := snapshot.Active.Claim.V2, snapshot.Active.RosterV2[0]
	transfer := GatewayRebindAllocationTransfer{
		Version: GatewayRebindTransferVersionV1, OperationID: claim.Spec.OperationID,
		Ordinal: entry.Ordinal, AppID: entry.AppID, AllocationID: entry.AllocationID,
		GrantAttemptID: entry.GrantAttemptID, SourceBindingDigest: strings.Repeat("6", 64),
		RosterEntryDigest:              entry.EntryDigest,
		SourceProfileRevisionID:        entry.SourceProfileRevisionID,
		SourceProfileRevisionNumber:    entry.SourceProfileRevisionNumber,
		SourceProfileSpecDigest:        entry.SourceProfileSpecDigest,
		PredecessorTransferDigest:      entry.PredecessorTransferDigest,
		SuccessorProfileRevisionID:     claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileSpecDigest:     claim.ConfigureApproval.SpecDigest,
		TerminalReceiptDigest:          receipt,
	}
	var err error
	transfer.TransferDigest, err = GatewayRebindAllocationTransferDigest(transfer)
	if err != nil {
		t.Fatal(err)
	}
	return transfer
}
