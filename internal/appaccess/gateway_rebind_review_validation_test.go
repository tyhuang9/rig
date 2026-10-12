package appaccess

import (
	"errors"
	"strings"
	"testing"
)

// A matching digest establishes byte identity, not a coherent recovery review.
// Re-sign the malformed snapshots below so validation must reject the actual
// cross-binding or policy contradiction rather than only stale digest bytes.
func TestGatewayRebindReconcileReviewValidationBindsCanonicalSnapshot(t *testing.T) {
	fixture, claim := gatewayRebindDistinctApprovalFixture(t, appAccessDB(t))
	snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
	baseline, err := BuildGatewayRebindReconcileReview(snapshot,
		gatewayRebindA0TrustedSnapshot(claim), testAdministrator)
	if err != nil || ValidateGatewayRebindReconcileReview(baseline, testAdministrator) != nil {
		t.Fatal("canonical A0 review was not valid")
	}
	for _, test := range []struct {
		name   string
		mutate func(*GatewayRebindReconcileSnapshot)
	}{
		{"protected operation", func(v *GatewayRebindReconcileSnapshot) {
			v.Trusted.Protected.OperationID = testViewer
		}},
		{"protected generation", func(v *GatewayRebindReconcileSnapshot) {
			v.Trusted.Protected.Generation++
		}},
		{"selected current version", func(v *GatewayRebindReconcileSnapshot) {
			v.Trusted.SelectedCurrent.StateVersion = 0
		}},
		{"selected current revision", func(v *GatewayRebindReconcileSnapshot) {
			v.Trusted.SelectedCurrent.StateRevision++
		}},
		{"selected predecessor digest", func(v *GatewayRebindReconcileSnapshot) {
			v.Trusted.SelectedCurrent.StateDigest = strings.Repeat("9", 64)
		}},
		{"current profile identity", func(v *GatewayRebindReconcileSnapshot) {
			v.CurrentProfile.ID = testViewer
		}},
		{"terminal claim without commit", func(v *GatewayRebindReconcileSnapshot) {
			v.Claim.State = GatewayRebindCommitted
		}},
		{"forward policy without fixed evidence", func(v *GatewayRebindReconcileSnapshot) {
			v.Policy = GatewayRebindRecoveryForwardOnly
		}},
		{"rollback policy without fixed evidence", func(v *GatewayRebindReconcileSnapshot) {
			v.Policy = GatewayRebindRecoveryRollbackOnly
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			review := baseline
			profile, source := *baseline.Snapshot.CurrentProfile, *baseline.Snapshot.CurrentSource
			review.Snapshot.CurrentProfile, review.Snapshot.CurrentSource = &profile, &source
			test.mutate(&review.Snapshot)
			resignGatewayRebindReviewForValidation(t, &review, testAdministrator)
			if err := ValidateGatewayRebindReconcileReview(review, testAdministrator); !errors.Is(err, ErrInvalidStoredState) {
				t.Fatalf("accepted contradictory re-signed review: %v", err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		actor  string
		mutate func(*GatewayRebindReconcileReview)
	}{
		{"different actor", testViewer, func(*GatewayRebindReconcileReview) {}},
		{"missing actor", "", func(*GatewayRebindReconcileReview) {}},
		{"wrong purpose", testAdministrator, func(v *GatewayRebindReconcileReview) { v.Snapshot.Purpose = "configure_gateway" }},
		{"changed snapshot digest", testAdministrator, func(v *GatewayRebindReconcileReview) { v.RecoveryDigest = strings.Repeat("a", 64) }},
		{"changed consent digest", testAdministrator, func(v *GatewayRebindReconcileReview) { v.RecoveryApprovalDigest = strings.Repeat("b", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			review := baseline
			test.mutate(&review)
			if err := ValidateGatewayRebindReconcileReview(review, test.actor); !errors.Is(err, ErrInvalidStoredState) {
				t.Fatalf("accepted altered review or actor: %v", err)
			}
		})
	}
	assertGatewayRebindReconcileAuditCount(t, fixture, 0)
}

func TestGatewayRebindReconcileReviewValidationPreservesFixedPolicy(t *testing.T) {
	fixture, claim := gatewayRebindDistinctApprovalFixture(t, appAccessDB(t))
	snapshot := gatewayRebindAuthorizationSnapshot(t, fixture)
	trusted := gatewayRebindA0TrustedSnapshot(claim)
	trusted.Protected.Presence = GatewayRebindProtectedEvidenceCheckpointTerminal
	trusted.Protected.FixedDisposition = GatewayRebindDispositionAbort
	trusted.Protected.TerminalReceiptDigest = strings.Repeat("7", 64)
	baseline, err := BuildGatewayRebindReconcileReview(snapshot, trusted, testAdministrator)
	if err != nil || baseline.Snapshot.Policy != GatewayRebindRecoveryRollbackOnly ||
		ValidateGatewayRebindReconcileReview(baseline, testAdministrator) != nil {
		t.Fatal("canonical checkpoint-terminal abort review was not valid")
	}
	for _, policy := range []GatewayRebindRecoveryPolicy{
		GatewayRebindRecoveryEvidenceDecides, GatewayRebindRecoveryForwardOnly,
	} {
		t.Run(string(policy), func(t *testing.T) {
			review := baseline
			review.Snapshot.Policy = policy
			resignGatewayRebindReviewForValidation(t, &review, testAdministrator)
			if err := ValidateGatewayRebindReconcileReview(review, testAdministrator); !errors.Is(err, ErrInvalidStoredState) {
				t.Fatalf("fixed abort review accepted another recovery policy: %v", err)
			}
		})
	}
	assertGatewayRebindReconcileAuditCount(t, fixture, 0)
}

func resignGatewayRebindReviewForValidation(t *testing.T, review *GatewayRebindReconcileReview, actor string) {
	t.Helper()
	var err error
	review.RecoveryDigest, err = digestJSON(review.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	review.RecoveryApprovalDigest, err = gatewayRebindRecoveryApprovalDigest(
		review.Snapshot, review.RecoveryDigest, actor)
	if err != nil {
		t.Fatal(err)
	}
}
