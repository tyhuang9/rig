package appaccess

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGatewayRebindV2SourceClaimAcceptsUpgradeGenerationAndRejectsCrossKindMixes(t *testing.T) {
	t.Run("upgrade generation after prior abort", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		if err := insertGatewayRebindSourceClaimForMigrationTest(fixture, 2, 2,
			GatewayRebindSourceGatewayUpgrade, 7, strings.Repeat("a", 64), "", nil, nil); err != nil {
			t.Fatalf("insert v2 upgrade generation: %v", err)
		}
		var generation int64
		if err := fixture.db.QueryRow(`SELECT predecessor_protected_generation
			FROM lan_gateway_rebind_claims WHERE operation_id=?`, fixture.claim.Spec.OperationID).
			Scan(&generation); err != nil || generation != 7 {
			t.Fatalf("stored generation=%d error=%v", generation, err)
		}
	})

	t.Run("legacy format cannot encode rebind predecessor", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		rebindOperationID, receipt := uuid.NewString(), strings.Repeat("b", 64)
		if err := insertGatewayRebindSourceClaimForMigrationTest(fixture, 1, 1,
			GatewayRebindSourceGatewayRebind, 0, "", "", &rebindOperationID, &receipt); err == nil {
			t.Fatal("v1 claim accepted a typed rebind predecessor")
		}
	})

	t.Run("upgrade kind rejects rebind evidence", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		if err := insertGatewayRebindSourceClaimForMigrationTest(fixture, 2, 2,
			GatewayRebindSourceGatewayUpgrade, 7, "", strings.Repeat("c", 64), nil, nil); err == nil {
			t.Fatal("upgrade source accepted protected rebind intent evidence")
		}
	})
}

func insertGatewayRebindSourceClaimForMigrationTest(fixture gatewayRebindFixture,
	specVersion, rosterVersion int, sourceKind GatewayRebindSourceKind, generation int64,
	journalDigest, intentDigest string, predecessorRebindOperationID, terminalReceiptDigest *string,
) error {
	claim := fixture.claim
	predecessorUpgradeOperationID := any(claim.Spec.PredecessorUpgradeOperationID)
	if sourceKind == GatewayRebindSourceGatewayRebind {
		predecessorUpgradeOperationID = nil
	}
	successorGeneration := int64(0)
	if specVersion == GatewayRebindSpecVersionV2 {
		successorGeneration = generation + 1
	}
	_, err := fixture.db.Exec(`INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,predecessor_source_kind,
		predecessor_rebind_operation_id,predecessor_terminal_receipt_digest,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_operation_id,successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at,
		spec_format_version,roster_format_version,predecessor_protected_generation,
		predecessor_protected_journal_digest,predecessor_protected_intent_digest,
		predecessor_source_state_version,predecessor_source_state_revision,
		predecessor_source_state_digest,predecessor_checkpoint_digest,
		successor_protected_generation
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		claim.Spec.OperationID, claim.RequestDigest, claim.RebindApproval.Action,
		claim.RebindApproval.SpecDigest, claim.RebindApproval.ActorID, formatTime(claim.RebindApprovedAt),
		claim.Spec.PredecessorProfileRevisionID, claim.Spec.PredecessorProfileRevisionNumber,
		claim.Spec.PredecessorProfileSpecDigest, predecessorUpgradeOperationID,
		claim.Spec.PredecessorProtectedIdentityDigest, sourceKind,
		predecessorRebindOperationID, terminalReceiptDigest,
		claim.Spec.SuccessorProfileRevisionID, claim.Spec.SuccessorProfileRevisionNumber,
		claim.Spec.SuccessorProfileOperationID, claim.SuccessorProfileRequestDigest,
		claim.Spec.SuccessorProfile.SelectedIPv4, claim.Spec.SuccessorProfile.InterfaceID,
		claim.Spec.SuccessorProfile.PortStart, claim.Spec.SuccessorProfile.PortEnd,
		claim.ConfigureApproval.SpecDigest, claim.ConfigureApproval.Action,
		claim.ConfigureApproval.ActorID, formatTime(claim.ConfigureApprovedAt),
		claim.Spec.RosterDigest, claim.Spec.RosterCount, claim.State, claim.StateSequence,
		formatTime(claim.CreatedAt), formatTime(claim.UpdatedAt), specVersion, rosterVersion,
		generation, nullableStringForMigrationTest(journalDigest), nullableStringForMigrationTest(intentDigest),
		2, 0, strings.Repeat("d", 64), strings.Repeat("e", 64), successorGeneration)
	return err
}

func nullableStringForMigrationTest(value string) any {
	if value == "" {
		return nil
	}
	return value
}
