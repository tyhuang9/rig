package appaccess

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGatewayRebindTransitionAppliesCompleteLifecycleAtomically(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	receipt := strings.Repeat("7", 64)
	transfer := gatewayRebindTransferForFixture(t, fixture, receipt)

	ready := gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
		GatewayRebindSuccessorReady, receipt, nil)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), ready); err != nil {
		t.Fatalf("successor ready: %v", err)
	}

	t.Run("incomplete transfer manifest is atomic", func(t *testing.T) {
		incomplete := gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
			GatewayRebindDatabaseCommitted, receipt, nil)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), incomplete); err == nil {
			t.Fatal("incomplete transfer manifest committed")
		}
		assertGatewayRebindProjection(t, fixture, GatewayRebindSuccessorReady, 2, false, 0)
	})

	databaseCommitted := gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
		GatewayRebindDatabaseCommitted, receipt, []GatewayRebindAllocationTransfer{transfer})
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), databaseCommitted); err != nil {
		t.Fatalf("database committed: %v", err)
	}
	assertGatewayRebindProjection(t, fixture, GatewayRebindDatabaseCommitted, 3, true, 1)

	unresolved := gatewayRebindProofForFixture(t, fixture, GatewayRebindDatabaseCommitted, 3,
		GatewayRebindUnresolved, receipt, nil)
	unresolved.TerminalDisposition = GatewayRebindDispositionCommit
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), unresolved); err != nil {
		t.Fatalf("unresolved after database commit: %v", err)
	}
	rollback := gatewayRebindProofForFixture(t, fixture, GatewayRebindUnresolved, 4,
		GatewayRebindRolledBack, receipt, nil)
	rollback.TerminalDisposition = GatewayRebindDispositionAbort
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), rollback); err == nil {
		t.Fatal("rollback succeeded after durable database_committed event")
	}
	assertGatewayRebindProjection(t, fixture, GatewayRebindUnresolved, 4, true, 1)

	committed := gatewayRebindProofForFixture(t, fixture, GatewayRebindUnresolved, 4,
		GatewayRebindCommitted, receipt, nil)
	committed.LocalAttestationDigest = strings.Repeat("8", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), committed); err != nil {
		t.Fatalf("committed: %v", err)
	}
	assertGatewayRebindProjection(t, fixture, GatewayRebindCommitted, 5, true, 1)

	appID := addApps(t, fixture.db, 1)[0]
	current, _, err := readGatewayRevision(context.Background(), fixture.db,
		fixture.claim.Spec.SuccessorProfileRevisionID, fixture.claim.Spec.SuccessorProfileRevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: current.ID, GatewayProfileRevisionNumber: current.RevisionNumber,
	}); err != nil || !created {
		t.Fatalf("terminal claim did not release ordinary allocation fence: created=%t err=%v", created, err)
	}

	var oldNonce string
	if err := fixture.db.QueryRow(`SELECT authorization_nonce FROM lan_gateway_rebind_transition_commands
		WHERE operation_id=? AND sequence=2`, fixture.claim.Spec.OperationID).Scan(&oldNonce); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO lan_gateway_rebind_transition_commands(
		operation_id,sequence,previous_state,previous_sequence,next_state,purpose,
		protected_record_digest,terminal_receipt_digest,command_digest,created_at,
		proof_version,terminal_disposition,protected_generation,protected_phase,
		protected_record_sequence,predecessor_checkpoint_digest,source_state_version,
		source_state_revision,source_state_digest,successor_operational_state_version,
		successor_operational_state_revision,authorization_nonce,canonical_payload
	) SELECT operation_id,6,'unresolved',5,'committed',purpose,protected_record_digest,
		terminal_receipt_digest,command_digest,created_at,proof_version,terminal_disposition,
		protected_generation,protected_phase,protected_record_sequence,predecessor_checkpoint_digest,
		source_state_version,source_state_revision,source_state_digest,0,0,?,canonical_payload
		FROM lan_gateway_rebind_transition_commands WHERE operation_id=? AND sequence=2`,
		oldNonce, fixture.claim.Spec.OperationID); err == nil ||
		!strings.Contains(err.Error(), "invalid or spent") {
		t.Fatalf("persisted old command authorized later write: %v", err)
	}
}

func TestGatewayRebindTransitionRejectsExtraRosterTransferAtomically(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	receipt := strings.Repeat("7", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
			GatewayRebindSuccessorReady, receipt, nil)); err != nil {
		t.Fatal(err)
	}
	first := gatewayRebindTransferForFixture(t, fixture, receipt)
	extra := first
	extra.Ordinal = 2
	extra.AppID = uuid.NewString()
	extra.AllocationID = uuid.NewString()
	extra.GrantAttemptID = uuid.NewString()
	extra.TransferDigest = ""
	extra.TransferDigest, _ = GatewayRebindAllocationTransferDigest(extra)
	proof := gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
		GatewayRebindDatabaseCommitted, receipt, []GatewayRebindAllocationTransfer{first, extra})
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), proof); err == nil {
		t.Fatal("extra transfer committed")
	}
	assertGatewayRebindProjection(t, fixture, GatewayRebindSuccessorReady, 2, false, 0)
}

func gatewayRebindTransferForFixture(t *testing.T, fixture gatewayRebindFixture,
	receipt string,
) GatewayRebindAllocationTransfer {
	t.Helper()
	transfer := GatewayRebindAllocationTransfer{
		Version: GatewayRebindTransferVersionV1, OperationID: fixture.claim.Spec.OperationID,
		Ordinal: fixture.entry.Ordinal, AppID: fixture.entry.AppID,
		AllocationID: fixture.entry.AllocationID, GrantAttemptID: fixture.entry.GrantAttemptID,
		SourceBindingDigest: strings.Repeat("6", 64), RosterEntryDigest: fixture.entry.EntryDigest,
		SourceProfileRevisionID:        fixture.claim.Spec.PredecessorProfileRevisionID,
		SourceProfileRevisionNumber:    fixture.claim.Spec.PredecessorProfileRevisionNumber,
		SourceProfileSpecDigest:        fixture.claim.Spec.PredecessorProfileSpecDigest,
		SuccessorProfileRevisionID:     fixture.claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: fixture.claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileSpecDigest:     fixture.claim.ConfigureApproval.SpecDigest,
		TerminalReceiptDigest:          receipt,
	}
	var err error
	transfer.TransferDigest, err = GatewayRebindAllocationTransferDigest(transfer)
	if err != nil {
		t.Fatal(err)
	}
	return transfer
}

func gatewayRebindProofForFixture(t *testing.T, fixture gatewayRebindFixture,
	previous GatewayRebindState, sequence int64, next GatewayRebindState, receipt string,
	transfers []GatewayRebindAllocationTransfer,
) GatewayRebindTransitionProof {
	t.Helper()
	expectedID := fixture.claim.Spec.PredecessorProfileRevisionID
	expectedNumber := fixture.claim.Spec.PredecessorProfileRevisionNumber
	expectedDigest := fixture.claim.Spec.PredecessorProfileSpecDigest
	if previous == GatewayRebindDatabaseCommitted || previous == GatewayRebindUnresolved {
		expectedID = fixture.claim.Spec.SuccessorProfileRevisionID
		expectedNumber = fixture.claim.Spec.SuccessorProfileRevisionNumber
		expectedDigest = fixture.claim.ConfigureApproval.SpecDigest
	}
	proof := GatewayRebindTransitionProof{
		Version: GatewayRebindTransitionVersionV1, Purpose: GatewayRebindTransitionPurpose,
		OperationID: fixture.claim.Spec.OperationID, ClaimRequestDigest: fixture.claim.RequestDigest,
		ClaimSpecDigest: fixture.claim.RebindApproval.SpecDigest,
		ExpectedState:   previous, ExpectedSequence: sequence, NextState: next,
		ExpectedHeadRevisionID: expectedID, ExpectedHeadRevisionNumber: expectedNumber,
		ExpectedHeadSpecDigest: expectedDigest, ProtectedGeneration: 1,
		ProtectedPhase: string(next), ProtectedRecordSequence: uint64(sequence),
		ProtectedRecordDigest: strings.Repeat("1", 64), TerminalReceiptDigest: receipt,
		TerminalDisposition:         GatewayRebindDispositionCommit,
		PredecessorCheckpointDigest: strings.Repeat("2", 64), SourceStateVersion: 2,
		SourceStateRevision: 0, SourceStateDigest: strings.Repeat("3", 64), Transfers: transfers,
	}
	if next == GatewayRebindDatabaseCommitted {
		proof.SuccessorOperationalStateVersion = 1
		proof.SuccessorOperationalStateRevision = 1
		proof.SuccessorOperationalStateDigest = strings.Repeat("4", 64)
		var err error
		proof.TransferManifestDigest, err = GatewayRebindTransferManifestDigest(transfers)
		if err != nil {
			t.Fatal(err)
		}
	}
	return proof
}

func assertGatewayRebindProjection(t *testing.T, fixture gatewayRebindFixture,
	wantState GatewayRebindState, wantSequence int64, wantSuccessor bool, wantTransfers int64,
) {
	t.Helper()
	var state GatewayRebindState
	var sequence, transfers, events int64
	if err := fixture.db.QueryRow(`SELECT state,state_sequence FROM lan_gateway_rebind_claims
		WHERE operation_id=?`, fixture.claim.Spec.OperationID).Scan(&state, &sequence); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_allocation_transfers
		WHERE operation_id=?`, fixture.claim.Spec.OperationID).Scan(&transfers); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claim_events
		WHERE operation_id=?`, fixture.claim.Spec.OperationID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var successorExists int
	if err := fixture.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM lan_gateway_profile_revisions WHERE id=?)`,
		fixture.claim.Spec.SuccessorProfileRevisionID).Scan(&successorExists); err != nil {
		t.Fatal(err)
	}
	if state != wantState || sequence != wantSequence || transfers != wantTransfers ||
		successorExists != boolInt(wantSuccessor) || events != wantSequence {
		t.Fatalf("projection state=%s/%d successor=%d transfers=%d events=%d",
			state, sequence, successorExists, transfers, events)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
