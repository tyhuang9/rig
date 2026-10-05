package appaccess

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	controldb "github.com/hostd/hostd/internal/database"
)

// ApplyGatewayRebindTransition consumes one protected transition proof and
// applies its complete SQL projection in the command INSERT statement. The
// process-local capability is armed only while a BEGIN IMMEDIATE connection is
// pinned, and is never reusable after an INSERT attempt or transaction exit.
func (r *Repository) ApplyGatewayRebindTransition(ctx context.Context,
	proof GatewayRebindTransitionProof,
) (command GatewayRebindTransitionCommand, resultErr error) {
	if r == nil || r.db == nil || ctx == nil || validateGatewayRebindTransitionProof(proof) != nil {
		return GatewayRebindTransitionCommand{}, ErrInvalidInput
	}
	payload, err := json.Marshal(proof)
	if err != nil {
		return GatewayRebindTransitionCommand{}, ErrInvalidInput
	}
	payloadDigest := sha256.Sum256(payload)
	commandDigest := hex.EncodeToString(payloadDigest[:])

	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayRebindTransitionCommand{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			command = GatewayRebindTransitionCommand{}
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()

	var (
		requestDigest, specDigest      string
		state                          GatewayRebindState
		sequence, specVersion          int64
		checkpointDigest, sourceDigest sql.NullString
		sourceVersion, sourceRevision  sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx, `SELECT request_digest,spec_digest,state,state_sequence,
		spec_format_version,predecessor_checkpoint_digest,predecessor_source_state_version,
		predecessor_source_state_revision,predecessor_source_state_digest
		FROM lan_gateway_rebind_claims WHERE operation_id=?`, proof.OperationID).Scan(
		&requestDigest, &specDigest, &state, &sequence, &specVersion, &checkpointDigest,
		&sourceVersion, &sourceRevision, &sourceDigest); err != nil {
		return GatewayRebindTransitionCommand{}, invalidRebindStoredState(err)
	}
	if requestDigest != proof.ClaimRequestDigest || specDigest != proof.ClaimSpecDigest ||
		state != proof.ExpectedState || sequence != proof.ExpectedSequence {
		return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
	}
	if specVersion == GatewayRebindSpecVersionV2 &&
		(!checkpointDigest.Valid || checkpointDigest.String != proof.PredecessorCheckpointDigest ||
			!sourceVersion.Valid || uint64(sourceVersion.Int64) != proof.SourceStateVersion ||
			!sourceRevision.Valid || uint64(sourceRevision.Int64) != proof.SourceStateRevision ||
			!sourceDigest.Valid || sourceDigest.String != proof.SourceStateDigest) {
		return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
	}
	var retainedDisposition, retainedReceipt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT terminal_disposition,terminal_receipt_digest
		FROM lan_gateway_rebind_transition_commands
		WHERE operation_id=? AND terminal_disposition<>'none'
		ORDER BY sequence LIMIT 1`, proof.OperationID).Scan(&retainedDisposition, &retainedReceipt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GatewayRebindTransitionCommand{}, err
	}
	if err == nil && (!retainedDisposition.Valid || !retainedReceipt.Valid ||
		retainedDisposition.String != string(proof.TerminalDisposition) ||
		retainedReceipt.String != proof.TerminalReceiptDigest) {
		return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
	}

	stamp := r.now().UTC()
	guard := controldb.GatewayRebindTransitionGuard{
		OperationID: proof.OperationID, Sequence: proof.ExpectedSequence + 1,
		PreviousState: string(proof.ExpectedState), PreviousSequence: proof.ExpectedSequence,
		NextState: string(proof.NextState), Purpose: proof.Purpose,
		ProtectedRecordDigest:  proof.ProtectedRecordDigest,
		TerminalReceiptDigest:  proof.TerminalReceiptDigest,
		LocalAttestationDigest: proof.LocalAttestationDigest,
		TerminalDisposition:    string(proof.TerminalDisposition),
		CommandDigest:          commandDigest, CanonicalPayload: string(payload),
	}
	nonce, revoke, err := controldb.ArmGatewayRebindTransitionGuard(guard)
	if err != nil {
		return GatewayRebindTransitionCommand{}, err
	}
	defer revoke()
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_gateway_rebind_transition_commands(
		operation_id,sequence,previous_state,previous_sequence,next_state,purpose,
		protected_record_digest,terminal_receipt_digest,local_attestation_digest,
		command_digest,created_at,proof_version,terminal_disposition,protected_generation,
		protected_phase,protected_record_sequence,predecessor_checkpoint_digest,
		source_state_version,source_state_revision,source_state_digest,
		successor_operational_state_version,successor_operational_state_revision,
		successor_operational_state_digest,transfer_manifest_digest,authorization_nonce,
		canonical_payload
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		proof.OperationID, proof.ExpectedSequence+1, proof.ExpectedState, proof.ExpectedSequence,
		proof.NextState, proof.Purpose, proof.ProtectedRecordDigest,
		nullableDigest(proof.TerminalReceiptDigest), nullableDigest(proof.LocalAttestationDigest),
		commandDigest, formatTime(stamp), proof.Version, proof.TerminalDisposition,
		int64(proof.ProtectedGeneration), proof.ProtectedPhase, int64(proof.ProtectedRecordSequence),
		proof.PredecessorCheckpointDigest, int64(proof.SourceStateVersion),
		int64(proof.SourceStateRevision), proof.SourceStateDigest,
		int64(proof.SuccessorOperationalStateVersion), int64(proof.SuccessorOperationalStateRevision),
		nullableDigest(proof.SuccessorOperationalStateDigest), nullableDigest(proof.TransferManifestDigest),
		nonce, string(payload)); err != nil {
		return GatewayRebindTransitionCommand{}, classifyImmediateTransactionError(err)
	}

	command, err = readBackGatewayRebindTransition(ctx, tx, proof, string(payload), commandDigest)
	if err != nil {
		return GatewayRebindTransitionCommand{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GatewayRebindTransitionCommand{}, err
	}
	return command, nil
}

func validateGatewayRebindTransitionProof(proof GatewayRebindTransitionProof) error {
	if proof.Version != GatewayRebindTransitionVersionV1 || proof.Purpose != GatewayRebindTransitionPurpose ||
		!validUUID(proof.OperationID) || !validDigest(proof.ClaimRequestDigest) ||
		!validDigest(proof.ClaimSpecDigest) || proof.ExpectedSequence < 1 ||
		!validUUID(proof.ExpectedHeadRevisionID) || proof.ExpectedHeadRevisionNumber < 1 ||
		!validDigest(proof.ExpectedHeadSpecDigest) || proof.ProtectedGeneration > math.MaxInt64 ||
		proof.ProtectedPhase == "" || proof.ProtectedRecordSequence > math.MaxInt64 ||
		!validDigest(proof.ProtectedRecordDigest) || !validDigest(proof.PredecessorCheckpointDigest) ||
		proof.SourceStateVersion > math.MaxInt64 || proof.SourceStateRevision > math.MaxInt64 ||
		!validDigest(proof.SourceStateDigest) || proof.SuccessorOperationalStateVersion > math.MaxInt64 ||
		proof.SuccessorOperationalStateRevision > math.MaxInt64 {
		return ErrInvalidInput
	}
	if !validGatewayRebindTransitionEdge(proof.ExpectedState, proof.NextState) {
		return ErrInvalidInput
	}
	switch proof.NextState {
	case GatewayRebindSuccessorReady, GatewayRebindDatabaseCommitted, GatewayRebindCommitted:
		if proof.TerminalDisposition != GatewayRebindDispositionCommit || !validDigest(proof.TerminalReceiptDigest) {
			return ErrInvalidInput
		}
	case GatewayRebindRolledBack:
		if proof.TerminalDisposition != GatewayRebindDispositionAbort || !validDigest(proof.TerminalReceiptDigest) {
			return ErrInvalidInput
		}
	case GatewayRebindUnresolved:
		if proof.TerminalDisposition != GatewayRebindDispositionNone &&
			proof.TerminalDisposition != GatewayRebindDispositionCommit &&
			proof.TerminalDisposition != GatewayRebindDispositionAbort {
			return ErrInvalidInput
		}
		if (proof.TerminalDisposition == GatewayRebindDispositionNone && proof.TerminalReceiptDigest != "") ||
			(proof.TerminalDisposition != GatewayRebindDispositionNone && !validDigest(proof.TerminalReceiptDigest)) {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	if proof.NextState == GatewayRebindCommitted {
		if !validDigest(proof.LocalAttestationDigest) {
			return ErrInvalidInput
		}
	} else if proof.LocalAttestationDigest != "" {
		return ErrInvalidInput
	}
	if proof.NextState == GatewayRebindDatabaseCommitted {
		if proof.SuccessorOperationalStateVersion != 1 || proof.SuccessorOperationalStateRevision != 1 ||
			!validDigest(proof.SuccessorOperationalStateDigest) || !validDigest(proof.TransferManifestDigest) {
			return ErrInvalidInput
		}
		manifestDigest, err := GatewayRebindTransferManifestDigest(proof.Transfers)
		if err != nil || manifestDigest != proof.TransferManifestDigest {
			return ErrInvalidInput
		}
		for index, transfer := range proof.Transfers {
			if transfer.Ordinal != int64(index+1) {
				return ErrInvalidInput
			}
			if transfer.OperationID != proof.OperationID || transfer.TerminalReceiptDigest != proof.TerminalReceiptDigest {
				return ErrInvalidInput
			}
		}
	} else if len(proof.Transfers) != 0 || proof.TransferManifestDigest != "" ||
		proof.SuccessorOperationalStateVersion != 0 || proof.SuccessorOperationalStateRevision != 0 ||
		proof.SuccessorOperationalStateDigest != "" {
		return ErrInvalidInput
	}
	return nil
}

func validGatewayRebindTransitionEdge(previous, next GatewayRebindState) bool {
	switch previous {
	case GatewayRebindPrepared:
		return next == GatewayRebindSuccessorReady || next == GatewayRebindRolledBack || next == GatewayRebindUnresolved
	case GatewayRebindSuccessorReady:
		return next == GatewayRebindDatabaseCommitted || next == GatewayRebindUnresolved
	case GatewayRebindDatabaseCommitted:
		return next == GatewayRebindCommitted || next == GatewayRebindUnresolved
	case GatewayRebindUnresolved:
		return next == GatewayRebindSuccessorReady || next == GatewayRebindDatabaseCommitted ||
			next == GatewayRebindCommitted || next == GatewayRebindRolledBack
	default:
		return false
	}
}

func readBackGatewayRebindTransition(ctx context.Context, tx *immediateTransaction,
	proof GatewayRebindTransitionProof, payload, commandDigest string,
) (GatewayRebindTransitionCommand, error) {
	var (
		state            GatewayRebindState
		stateSequence    int64
		eventState       GatewayRebindState
		storedPayload    string
		storedDigest     string
		terminalReceipt  sql.NullString
		localAttestation sql.NullString
		createdAt        string
	)
	if err := tx.QueryRowContext(ctx, `SELECT state,state_sequence FROM lan_gateway_rebind_claims
		WHERE operation_id=?`, proof.OperationID).Scan(&state, &stateSequence); err != nil {
		return GatewayRebindTransitionCommand{}, err
	}
	if state != proof.NextState || stateSequence != proof.ExpectedSequence+1 {
		return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
	}
	if err := tx.QueryRowContext(ctx, `SELECT state FROM lan_gateway_rebind_claim_events
		WHERE operation_id=? AND sequence=?`, proof.OperationID, proof.ExpectedSequence+1).
		Scan(&eventState); err != nil || eventState != proof.NextState {
		return GatewayRebindTransitionCommand{}, invalidRebindStoredState(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT canonical_payload,command_digest,
		terminal_receipt_digest,local_attestation_digest,created_at
		FROM lan_gateway_rebind_transition_commands WHERE operation_id=? AND sequence=?`,
		proof.OperationID, proof.ExpectedSequence+1).Scan(&storedPayload, &storedDigest,
		&terminalReceipt, &localAttestation, &createdAt); err != nil {
		return GatewayRebindTransitionCommand{}, err
	}
	if storedPayload != payload || storedDigest != commandDigest ||
		terminalReceipt.String != proof.TerminalReceiptDigest ||
		localAttestation.String != proof.LocalAttestationDigest {
		return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
	}
	if proof.NextState == GatewayRebindDatabaseCommitted {
		var profileID, specDigest, expectedProfileID, expectedSpecDigest string
		var revisionNumber, transferCount int64
		var expectedRevisionNumber int64
		if err := tx.QueryRowContext(ctx, `SELECT p.id,p.revision_number,p.spec_digest,
			c.successor_profile_revision_id,c.successor_profile_revision_number,c.successor_profile_spec_digest,
			(SELECT COUNT(*) FROM lan_gateway_rebind_allocation_transfers t WHERE t.operation_id=?)
			FROM lan_gateway_profile_heads h JOIN lan_gateway_profile_revisions p
			ON p.id=h.revision_id AND p.revision_number=h.revision_number
			JOIN lan_gateway_rebind_claims c ON c.operation_id=? WHERE h.singleton=1`,
			proof.OperationID,
			proof.OperationID).Scan(&profileID, &revisionNumber, &specDigest,
			&expectedProfileID, &expectedRevisionNumber, &expectedSpecDigest, &transferCount); err != nil {
			return GatewayRebindTransitionCommand{}, err
		}
		if profileID != expectedProfileID || revisionNumber != expectedRevisionNumber ||
			specDigest != expectedSpecDigest ||
			transferCount != int64(len(proof.Transfers)) {
			return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
		}
		rows, err := tx.QueryContext(ctx, `SELECT ordinal,app_id,allocation_id,grant_attempt_id,
			source_binding_digest,roster_entry_digest,source_profile_revision_id,
			source_profile_revision_number,source_profile_spec_digest,predecessor_transfer_digest,
			successor_profile_revision_id,successor_profile_revision_number,
			successor_profile_spec_digest,terminal_receipt_digest,transfer_digest
			FROM lan_gateway_rebind_allocation_transfers WHERE operation_id=? ORDER BY ordinal`,
			proof.OperationID)
		if err != nil {
			return GatewayRebindTransitionCommand{}, err
		}
		defer rows.Close()
		for index := 0; rows.Next(); index++ {
			if index >= len(proof.Transfers) {
				return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
			}
			var stored GatewayRebindAllocationTransfer
			var predecessor sql.NullString
			stored.Version = GatewayRebindTransferVersionV1
			stored.OperationID = proof.OperationID
			if err := rows.Scan(&stored.Ordinal, &stored.AppID, &stored.AllocationID,
				&stored.GrantAttemptID, &stored.SourceBindingDigest, &stored.RosterEntryDigest,
				&stored.SourceProfileRevisionID, &stored.SourceProfileRevisionNumber,
				&stored.SourceProfileSpecDigest, &predecessor,
				&stored.SuccessorProfileRevisionID, &stored.SuccessorProfileRevisionNumber,
				&stored.SuccessorProfileSpecDigest, &stored.TerminalReceiptDigest,
				&stored.TransferDigest); err != nil {
				return GatewayRebindTransitionCommand{}, err
			}
			if predecessor.Valid {
				stored.PredecessorTransferDigest = &predecessor.String
			}
			expected := proof.Transfers[index]
			if !sameGatewayRebindAllocationTransfer(stored, expected) {
				return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
			}
		}
		if err := rows.Err(); err != nil {
			return GatewayRebindTransitionCommand{}, err
		}
	}
	parsedAt, err := parseTime(createdAt)
	if err != nil {
		return GatewayRebindTransitionCommand{}, ErrInvalidStoredState
	}
	return GatewayRebindTransitionCommand{
		OperationID: proof.OperationID, Sequence: proof.ExpectedSequence + 1,
		PreviousState: proof.ExpectedState, PreviousSequence: proof.ExpectedSequence,
		NextState: proof.NextState, Purpose: proof.Purpose,
		ProtectedRecordDigest:  proof.ProtectedRecordDigest,
		TerminalReceiptDigest:  proof.TerminalReceiptDigest,
		LocalAttestationDigest: proof.LocalAttestationDigest,
		TerminalDisposition:    proof.TerminalDisposition, CanonicalPayload: payload,
		CommandDigest: commandDigest, CreatedAt: parsedAt,
	}, nil
}

func sameGatewayRebindAllocationTransfer(left, right GatewayRebindAllocationTransfer) bool {
	if (left.PredecessorTransferDigest == nil) != (right.PredecessorTransferDigest == nil) {
		return false
	}
	leftPredecessor, rightPredecessor := "", ""
	if left.PredecessorTransferDigest != nil {
		leftPredecessor = *left.PredecessorTransferDigest
	}
	if right.PredecessorTransferDigest != nil {
		rightPredecessor = *right.PredecessorTransferDigest
	}
	left.PredecessorTransferDigest = nil
	right.PredecessorTransferDigest = nil
	return left == right && leftPredecessor == rightPredecessor
}

func nullableDigest(value string) any {
	if value == "" {
		return nil
	}
	return value
}
