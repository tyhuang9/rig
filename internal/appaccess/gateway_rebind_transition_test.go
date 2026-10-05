package appaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	controldb "github.com/hostd/hostd/internal/database"
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

func TestGatewayRebindTransitionRetainsOneTerminalDecision(t *testing.T) {
	t.Run("SQL rejects changed commit receipt", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		firstReceipt, secondReceipt := strings.Repeat("7", 64), strings.Repeat("9", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, firstReceipt, nil)); err != nil {
			t.Fatal(err)
		}
		transfer := gatewayRebindTransferForFixture(t, fixture, secondReceipt)
		conflict := gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
			GatewayRebindDatabaseCommitted, secondReceipt, []GatewayRebindAllocationTransfer{transfer})
		if err := insertGatewayRebindTransitionDirect(t, fixture, conflict); err == nil ||
			!strings.Contains(err.Error(), "terminal decision is immutable") {
			t.Fatalf("changed receipt direct SQL error=%v", err)
		}
		assertGatewayRebindProjection(t, fixture, GatewayRebindSuccessorReady, 2, false, 0)
		assertGatewayRebindTerminalDecision(t, fixture, GatewayRebindDispositionCommit, firstReceipt, 1)
	})

	t.Run("commit forbids later abort before database commit", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		receipt := strings.Repeat("7", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, receipt, nil)); err != nil {
			t.Fatal(err)
		}
		unresolved := gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
			GatewayRebindUnresolved, receipt, nil)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), unresolved); err != nil {
			t.Fatal(err)
		}
		abort := gatewayRebindProofForFixture(t, fixture, GatewayRebindUnresolved, 3,
			GatewayRebindRolledBack, strings.Repeat("9", 64), nil)
		abort.TerminalDisposition = GatewayRebindDispositionAbort
		abort.ExpectedHeadRevisionID = fixture.claim.Spec.PredecessorProfileRevisionID
		abort.ExpectedHeadRevisionNumber = fixture.claim.Spec.PredecessorProfileRevisionNumber
		abort.ExpectedHeadSpecDigest = fixture.claim.Spec.PredecessorProfileSpecDigest
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), abort); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("abort after retained commit error=%v", err)
		}
		assertGatewayRebindProjection(t, fixture, GatewayRebindUnresolved, 3, false, 0)
		assertGatewayRebindTerminalDecision(t, fixture, GatewayRebindDispositionCommit, receipt, 2)
	})

	t.Run("abort forbids later commit", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		abortReceipt := strings.Repeat("a", 64)
		unresolved := gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
			GatewayRebindUnresolved, abortReceipt, nil)
		unresolved.TerminalDisposition = GatewayRebindDispositionAbort
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), unresolved); err != nil {
			t.Fatal(err)
		}
		commit := gatewayRebindProofForFixture(t, fixture, GatewayRebindUnresolved, 2,
			GatewayRebindSuccessorReady, strings.Repeat("b", 64), nil)
		commit.ExpectedHeadRevisionID = fixture.claim.Spec.PredecessorProfileRevisionID
		commit.ExpectedHeadRevisionNumber = fixture.claim.Spec.PredecessorProfileRevisionNumber
		commit.ExpectedHeadSpecDigest = fixture.claim.Spec.PredecessorProfileSpecDigest
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), commit); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("commit after retained abort error=%v", err)
		}
		assertGatewayRebindProjection(t, fixture, GatewayRebindUnresolved, 2, false, 0)
		assertGatewayRebindTerminalDecision(t, fixture, GatewayRebindDispositionAbort, abortReceipt, 1)
	})

	t.Run("undecided unresolved may later retain commit", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		unresolved := gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
			GatewayRebindUnresolved, "", nil)
		unresolved.TerminalDisposition = GatewayRebindDispositionNone
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), unresolved); err != nil {
			t.Fatal(err)
		}
		receipt := strings.Repeat("c", 64)
		commit := gatewayRebindProofForFixture(t, fixture, GatewayRebindUnresolved, 2,
			GatewayRebindSuccessorReady, receipt, nil)
		commit.ExpectedHeadRevisionID = fixture.claim.Spec.PredecessorProfileRevisionID
		commit.ExpectedHeadRevisionNumber = fixture.claim.Spec.PredecessorProfileRevisionNumber
		commit.ExpectedHeadSpecDigest = fixture.claim.Spec.PredecessorProfileSpecDigest
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), commit); err != nil {
			t.Fatalf("first retained terminal decision: %v", err)
		}
		assertGatewayRebindProjection(t, fixture, GatewayRebindSuccessorReady, 3, false, 0)
		assertGatewayRebindTerminalDecision(t, fixture, GatewayRebindDispositionCommit, receipt, 1)
	})
}

func TestGatewayRebindChainedTransferReadbackUsesDigestValue(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	left := gatewayRebindTransferForFixture(t, fixture, strings.Repeat("7", 64))
	leftDigest := strings.Repeat("d", 64)
	rightDigest := string([]byte(leftDigest))
	left.PredecessorTransferDigest = &leftDigest
	right := left
	right.PredecessorTransferDigest = &rightDigest
	if left.PredecessorTransferDigest == right.PredecessorTransferDigest {
		t.Fatal("test requires distinct pointer identities")
	}
	if !sameGatewayRebindAllocationTransfer(left, right) {
		t.Fatal("equal chained predecessor digest values failed readback comparison")
	}
	wrongDigest := strings.Repeat("e", 64)
	right.PredecessorTransferDigest = &wrongDigest
	if sameGatewayRebindAllocationTransfer(left, right) {
		t.Fatal("different chained predecessor digest passed readback comparison")
	}
	right.PredecessorTransferDigest = nil
	if sameGatewayRebindAllocationTransfer(left, right) {
		t.Fatal("missing chained predecessor digest passed readback comparison")
	}
}

func insertGatewayRebindTransitionDirect(t *testing.T, fixture gatewayRebindFixture,
	proof GatewayRebindTransitionProof,
) error {
	t.Helper()
	insert, revoke := armGatewayRebindTransitionDirect(t, fixture, proof)
	defer revoke()
	return insert()
}

func armGatewayRebindTransitionDirect(t *testing.T, fixture gatewayRebindFixture,
	proof GatewayRebindTransitionProof,
) (func() error, func()) {
	t.Helper()
	payload, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	commandDigest := hex.EncodeToString(sum[:])
	guard := controldb.GatewayRebindTransitionGuard{
		OperationID: proof.OperationID, Sequence: proof.ExpectedSequence + 1,
		PreviousState: string(proof.ExpectedState), PreviousSequence: proof.ExpectedSequence,
		NextState: string(proof.NextState), Purpose: proof.Purpose,
		ProtectedRecordDigest:  proof.ProtectedRecordDigest,
		TerminalReceiptDigest:  proof.TerminalReceiptDigest,
		LocalAttestationDigest: proof.LocalAttestationDigest,
		TerminalDisposition:    string(proof.TerminalDisposition), CommandDigest: commandDigest,
		CanonicalPayload: string(payload),
	}
	nonce, revoke, err := controldb.ArmGatewayRebindTransitionGuard(guard)
	if err != nil {
		t.Fatal(err)
	}
	insert := func() error {
		_, err := fixture.db.Exec(`INSERT INTO lan_gateway_rebind_transition_commands(
		operation_id,sequence,previous_state,previous_sequence,next_state,purpose,
		protected_record_digest,terminal_receipt_digest,local_attestation_digest,
		command_digest,created_at,proof_version,terminal_disposition,protected_generation,
		protected_phase,protected_record_sequence,predecessor_checkpoint_digest,
		source_state_version,source_state_revision,source_state_digest,
		successor_operational_state_version,successor_operational_state_revision,
		successor_operational_state_digest,transfer_manifest_digest,authorization_nonce,canonical_payload
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			proof.OperationID, proof.ExpectedSequence+1, proof.ExpectedState, proof.ExpectedSequence,
			proof.NextState, proof.Purpose, proof.ProtectedRecordDigest,
			nullableDigest(proof.TerminalReceiptDigest), nullableDigest(proof.LocalAttestationDigest),
			commandDigest, formatTime(testNow), proof.Version, proof.TerminalDisposition,
			int64(proof.ProtectedGeneration), proof.ProtectedPhase, int64(proof.ProtectedRecordSequence),
			proof.PredecessorCheckpointDigest, int64(proof.SourceStateVersion), int64(proof.SourceStateRevision),
			proof.SourceStateDigest, int64(proof.SuccessorOperationalStateVersion),
			int64(proof.SuccessorOperationalStateRevision), nullableDigest(proof.SuccessorOperationalStateDigest),
			nullableDigest(proof.TransferManifestDigest), nonce, string(payload))
		return err
	}
	return insert, revoke
}

func assertGatewayRebindTerminalDecision(t *testing.T, fixture gatewayRebindFixture,
	wantDisposition GatewayRebindTerminalDisposition, wantReceipt string, wantCount int,
) {
	t.Helper()
	rows, err := fixture.db.Query(`SELECT terminal_disposition,terminal_receipt_digest
		FROM lan_gateway_rebind_transition_commands
		WHERE operation_id=? AND terminal_disposition<>'none' ORDER BY sequence`, fixture.claim.Spec.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var disposition GatewayRebindTerminalDisposition
		var receipt string
		if err := rows.Scan(&disposition, &receipt); err != nil {
			t.Fatal(err)
		}
		if disposition != wantDisposition || receipt != wantReceipt {
			t.Fatalf("retained decision=%s/%s want=%s/%s", disposition, receipt, wantDisposition, wantReceipt)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != wantCount {
		t.Fatalf("retained decision count=%d want=%d", count, wantCount)
	}
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
