package appaccess

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"
)

// GatewayRebindRecoverySnapshot reconstructs every retained claim using its
// stored canonical version. Historical claims are checked against their own
// retained roster, commands, events, and transfers. Only the active claim and
// selected current lineage are compared with live heads.
func (r *Repository) GatewayRebindRecoverySnapshot(ctx context.Context,
) (GatewayRebindRecoverySnapshot, error) {
	if r == nil || r.db == nil || ctx == nil {
		return GatewayRebindRecoverySnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayRebindRecoverySnapshot{}, err
	}
	defer tx.Rollback()
	value, err := readGatewayRebindRecoverySnapshot(ctx, tx)
	if err != nil {
		return GatewayRebindRecoverySnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayRebindRecoverySnapshot{}, err
	}
	return value, nil
}

func readGatewayRebindRecoverySnapshot(ctx context.Context, tx *sql.Tx,
) (GatewayRebindRecoverySnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT operation_id,spec_format_version
		FROM lan_gateway_rebind_claims ORDER BY created_at,operation_id`)
	if err != nil {
		return GatewayRebindRecoverySnapshot{}, err
	}
	type claimKey struct {
		operationID string
		version     int
	}
	var keys []claimKey
	for rows.Next() {
		var key claimKey
		if err := rows.Scan(&key.operationID, &key.version); err != nil {
			_ = rows.Close()
			return GatewayRebindRecoverySnapshot{}, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return GatewayRebindRecoverySnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return GatewayRebindRecoverySnapshot{}, err
	}

	result := GatewayRebindRecoverySnapshot{History: make([]GatewayRebindHistoryEntry, 0, len(keys))}
	if len(keys) == 0 {
		profile, source, err := readGatewayRebindNoHistoryCurrent(ctx, tx)
		if err != nil {
			return GatewayRebindRecoverySnapshot{}, err
		}
		result.CurrentProfile = profile
		result.CurrentSource = source
		return result, nil
	}
	for _, key := range keys {
		history, err := readGatewayRebindHistory(ctx, tx, key.operationID, key.version)
		if err != nil {
			return GatewayRebindRecoverySnapshot{}, err
		}
		state := historyClaimState(history.Claim)
		if isActiveGatewayRebindState(state) {
			if result.Active != nil {
				return GatewayRebindRecoverySnapshot{}, ErrInvalidStoredState
			}
			copy := history
			result.Active = &copy
		}
		result.History = append(result.History, history)
	}

	currentProfile, currentSource, err := readGatewayCurrentAuthority(ctx, tx)
	if err != nil {
		return GatewayRebindRecoverySnapshot{}, err
	}
	result.CurrentProfile = &currentProfile
	result.CurrentSource = &currentSource
	if currentSource.Kind == GatewayRebindSourceGatewayRebind {
		currentHistory := findGatewayRebindHistory(result.History, currentSource.OperationID)
		if currentHistory == nil {
			return GatewayRebindRecoverySnapshot{}, ErrInvalidStoredState
		}
		for index := range currentHistory.Events {
			event := currentHistory.Events[index]
			if event.State == GatewayRebindDatabaseCommitted {
				if result.CurrentDatabaseCommittedEvent != nil {
					return GatewayRebindRecoverySnapshot{}, ErrInvalidStoredState
				}
				copy := event
				result.CurrentDatabaseCommittedEvent = &copy
			}
		}
		if result.CurrentDatabaseCommittedEvent == nil ||
			result.CurrentDatabaseCommittedEvent.OperationID != currentSource.OperationID {
			return GatewayRebindRecoverySnapshot{}, ErrInvalidStoredState
		}
		result.CurrentTransfers = append([]GatewayRebindAllocationTransfer(nil), currentHistory.Transfers...)
	}
	if result.Active != nil {
		state := historyClaimState(result.Active.Claim)
		result.Phase = state
		for index := range result.Active.Events {
			event := result.Active.Events[index]
			if event.State == GatewayRebindDatabaseCommitted {
				if result.DatabaseCommittedEvent != nil {
					return GatewayRebindRecoverySnapshot{}, ErrInvalidStoredState
				}
				copy := event
				result.DatabaseCommittedEvent = &copy
			}
		}
		result.DatabaseCommitObserved = result.DatabaseCommittedEvent != nil
		retainedDisposition := retainedGatewayRebindDisposition(result.Active.Commands)
		result.RollbackAllowed = !result.DatabaseCommitObserved &&
			retainedDisposition != GatewayRebindDispositionCommit
		if err := validateActiveGatewayRebindHeads(*result.Active, currentProfile, currentSource,
			result.DatabaseCommitObserved); err != nil {
			return GatewayRebindRecoverySnapshot{}, err
		}
		if result.Active.Claim.SpecVersion == GatewayRebindSpecVersionV2 {
			liveRuntimeHeads, err := readGatewayRebindRuntimeHeads(ctx, tx)
			if err != nil {
				return GatewayRebindRecoverySnapshot{}, err
			}
			if !sameGatewayRebindRuntimeHeads(liveRuntimeHeads, result.Active.RuntimeHeads) {
				return GatewayRebindRecoverySnapshot{}, ErrInvalidStoredState
			}
		}
	}
	return result, nil
}

func readGatewayRebindNoHistoryCurrent(ctx context.Context, tx *sql.Tx,
) (*GatewayProfileRevision, *GatewayCurrentAuthorityRef, error) {
	var profileID sql.NullString
	var profileNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&profileID, &profileNumber); err != nil {
		return nil, nil, err
	}
	if !profileID.Valid {
		var claims int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_gateway_upgrade_claims`).Scan(&claims); err != nil {
			return nil, nil, err
		}
		if claims != 0 {
			return nil, nil, ErrInvalidStoredState
		}
		return nil, nil, nil
	}
	profile, _, err := readGatewayRevision(ctx, tx, profileID.String, profileNumber)
	if err != nil {
		return nil, nil, invalidRebindStoredState(err)
	}
	var committed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_gateway_upgrade_claims
		WHERE state='committed'`).Scan(&committed); err != nil {
		return nil, nil, err
	}
	if committed == 0 {
		return &profile, nil, nil
	}
	current, source, err := readGatewayCurrentAuthority(ctx, tx)
	if err != nil || current.ID != profile.ID || current.RevisionNumber != profile.RevisionNumber ||
		current.SpecDigest != profile.SpecDigest || source.Kind != GatewayRebindSourceGatewayUpgrade {
		return nil, nil, invalidRebindStoredState(err)
	}
	return &current, &source, nil
}

func readGatewayRebindHistory(ctx context.Context, tx *sql.Tx, operationID string,
	version int,
) (GatewayRebindHistoryEntry, error) {
	value := GatewayRebindHistoryEntry{Claim: GatewayRebindClaimRecord{SpecVersion: version}}
	switch version {
	case 1:
		claim, err := readGatewayRebindClaimRetained(ctx, tx, operationID)
		if err != nil {
			return GatewayRebindHistoryEntry{}, err
		}
		value.Claim.Legacy = &claim
		value.RosterV1, err = readGatewayRebindRoster(ctx, tx, operationID)
		if err != nil {
			return GatewayRebindHistoryEntry{}, err
		}
		if int64(len(value.RosterV1)) != claim.Spec.RosterCount {
			return GatewayRebindHistoryEntry{}, ErrInvalidStoredState
		}
		var retainedRuntimeHeads int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_gateway_rebind_runtime_heads
			WHERE operation_id=?`, operationID).Scan(&retainedRuntimeHeads); err != nil || retainedRuntimeHeads != 0 {
			return GatewayRebindHistoryEntry{}, invalidRebindStoredState(err)
		}
		digest, err := GatewayRebindRosterDigest(value.RosterV1)
		if err != nil || digest != claim.Spec.RosterDigest {
			return GatewayRebindHistoryEntry{}, ErrInvalidStoredState
		}
	case GatewayRebindSpecVersionV2:
		claim, err := readGatewayRebindClaimV2(ctx, tx, operationID)
		if err != nil {
			return GatewayRebindHistoryEntry{}, err
		}
		value.Claim.V2 = &claim
		value.RosterV2, err = readGatewayRebindRosterV2(ctx, tx, operationID)
		if err != nil {
			return GatewayRebindHistoryEntry{}, err
		}
		value.RuntimeHeads, err = readGatewayRebindRetainedRuntimeHeads(ctx, tx, operationID)
		if err != nil {
			return GatewayRebindHistoryEntry{}, err
		}
		if err := validateStoredGatewayRebindClaimV2(claim, value.RosterV2,
			value.RuntimeHeads); err != nil {
			return GatewayRebindHistoryEntry{}, err
		}
	default:
		return GatewayRebindHistoryEntry{}, ErrInvalidStoredState
	}
	var err error
	value.Events, err = readGatewayRebindEvents(ctx, tx, operationID)
	if err != nil {
		return GatewayRebindHistoryEntry{}, err
	}
	value.Commands, err = readGatewayRebindCommands(ctx, tx, operationID)
	if err != nil {
		return GatewayRebindHistoryEntry{}, err
	}
	value.Transfers, err = readGatewayRebindTransfers(ctx, tx, operationID)
	if err != nil {
		return GatewayRebindHistoryEntry{}, err
	}
	if err := validateGatewayRebindRetainedHistory(value); err != nil {
		return GatewayRebindHistoryEntry{}, err
	}
	return value, nil
}

func readGatewayRebindClaimRetained(ctx context.Context, query rowQuerier,
	operationID string,
) (GatewayRebindClaim, error) {
	var claim GatewayRebindClaim
	var rebindAction, configureAction, rebindApprovedAt, configureApprovedAt, createdAt, updatedAt string
	claim.Spec.OperationID = operationID
	err := query.QueryRowContext(ctx, `SELECT request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,predecessor_profile_spec_digest,
		predecessor_upgrade_operation_id,predecessor_protected_identity_digest,
		successor_profile_revision_id,successor_profile_revision_number,successor_profile_operation_id,
		successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		successor_port_start,successor_port_end,successor_profile_spec_digest,
		configure_approval_action,configure_approved_by,configure_approved_at,
		roster_digest,roster_count,state,state_sequence,created_at,updated_at
		FROM lan_gateway_rebind_claims WHERE operation_id=? AND spec_format_version=1`, operationID).Scan(
		&claim.RequestDigest, &rebindAction, &claim.RebindApproval.SpecDigest,
		&claim.RebindApproval.ActorID, &rebindApprovedAt,
		&claim.Spec.PredecessorProfileRevisionID, &claim.Spec.PredecessorProfileRevisionNumber,
		&claim.Spec.PredecessorProfileSpecDigest, &claim.Spec.PredecessorUpgradeOperationID,
		&claim.Spec.PredecessorProtectedIdentityDigest, &claim.Spec.SuccessorProfileRevisionID,
		&claim.Spec.SuccessorProfileRevisionNumber, &claim.Spec.SuccessorProfileOperationID,
		&claim.SuccessorProfileRequestDigest, &claim.Spec.SuccessorProfile.SelectedIPv4,
		&claim.Spec.SuccessorProfile.InterfaceID, &claim.Spec.SuccessorProfile.PortStart,
		&claim.Spec.SuccessorProfile.PortEnd, &claim.ConfigureApproval.SpecDigest,
		&configureAction, &claim.ConfigureApproval.ActorID, &configureApprovedAt,
		&claim.Spec.RosterDigest, &claim.Spec.RosterCount, &claim.State,
		&claim.StateSequence, &createdAt, &updatedAt)
	if err != nil {
		return GatewayRebindClaim{}, err
	}
	claim.RebindApproval.Action = ApprovalAction(rebindAction)
	claim.ConfigureApproval.Action = ApprovalAction(configureAction)
	for _, stamp := range []struct {
		text   string
		target *time.Time
	}{
		{rebindApprovedAt, &claim.RebindApprovedAt}, {configureApprovedAt, &claim.ConfigureApprovedAt},
		{createdAt, &claim.CreatedAt}, {updatedAt, &claim.UpdatedAt},
	} {
		parsed, parseErr := parseTime(stamp.text)
		if parseErr != nil {
			return GatewayRebindClaim{}, ErrInvalidStoredState
		}
		*stamp.target = parsed
	}
	identity := claim
	identity.State, identity.StateSequence, identity.UpdatedAt = GatewayRebindPrepared, 1, identity.CreatedAt
	if err := validateGatewayRebindClaimDigests(identity); err != nil ||
		!validGatewayRebindState(claim.State) || claim.StateSequence < 1 || claim.UpdatedAt.Before(claim.CreatedAt) {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	return claim, nil
}

func validateStoredGatewayRebindClaimV2(claim GatewayRebindClaimV2,
	roster []GatewayRebindRosterEntryV2, runtimeHeads []GatewayRebindRuntimeHead,
) error {
	proposal := GatewayRebindPreclaimProposalV2{
		Spec: claim.Spec, RebindApproval: claim.RebindApproval,
		ConfigureApproval: claim.ConfigureApproval, Roster: roster, RuntimeHeads: runtimeHeads,
	}
	requestDigest, err := gatewayRebindClaimV2RequestDigest(claim.Spec,
		claim.RebindApproval, claim.ConfigureApproval)
	if validateGatewayRebindProposalV2(proposal) != nil || err != nil ||
		requestDigest != claim.RequestDigest || !validGatewayRebindState(claim.State) ||
		claim.StateSequence < 1 || claim.RebindApprovedAt.After(claim.CreatedAt) ||
		claim.ConfigureApprovedAt.After(claim.CreatedAt) || claim.UpdatedAt.Before(claim.CreatedAt) {
		return ErrInvalidStoredState
	}
	canonical, _ := canonicalGatewaySpec(claim.Spec.SuccessorProfile)
	successorRequest, err := gatewayRequestDigest(ConfigureGatewayInput{
		OperationID:            claim.Spec.SuccessorProfileOperationID,
		ExpectedRevisionNumber: claim.Spec.Predecessor.Lineage.ProfileRevisionNumber,
		Spec:                   canonical, Approval: claim.ConfigureApproval,
	}, canonical)
	if err != nil || successorRequest != claim.SuccessorProfileRequestDigest {
		return ErrInvalidStoredState
	}
	return nil
}

func readGatewayRebindEvents(ctx context.Context, query gatewayRebindQuiescenceQuerier,
	operationID string,
) ([]GatewayRebindEvent, error) {
	rows, err := query.QueryContext(ctx, `SELECT sequence,state,created_at
		FROM lan_gateway_rebind_claim_events WHERE operation_id=? ORDER BY sequence`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []GatewayRebindEvent
	for rows.Next() {
		var value GatewayRebindEvent
		var stamp string
		value.OperationID = operationID
		if err := rows.Scan(&value.Sequence, &value.State, &stamp); err != nil {
			return nil, err
		}
		value.CreatedAt, err = parseTime(stamp)
		if err != nil {
			return nil, ErrInvalidStoredState
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func readGatewayRebindCommands(ctx context.Context, query gatewayRebindQuiescenceQuerier,
	operationID string,
) ([]GatewayRebindTransitionCommand, error) {
	rows, err := query.QueryContext(ctx, `SELECT sequence,previous_state,previous_sequence,next_state,
		purpose,protected_record_digest,terminal_receipt_digest,local_attestation_digest,
		terminal_disposition,canonical_payload,command_digest,created_at
		FROM lan_gateway_rebind_transition_commands WHERE operation_id=? ORDER BY sequence`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []GatewayRebindTransitionCommand
	for rows.Next() {
		var value GatewayRebindTransitionCommand
		var receipt, attestation sql.NullString
		var stamp string
		value.OperationID = operationID
		if err := rows.Scan(&value.Sequence, &value.PreviousState, &value.PreviousSequence,
			&value.NextState, &value.Purpose, &value.ProtectedRecordDigest, &receipt,
			&attestation, &value.TerminalDisposition, &value.CanonicalPayload,
			&value.CommandDigest, &stamp); err != nil {
			return nil, err
		}
		value.TerminalReceiptDigest = receipt.String
		value.LocalAttestationDigest = attestation.String
		value.CreatedAt, err = parseTime(stamp)
		if err != nil {
			return nil, ErrInvalidStoredState
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func readGatewayRebindTransfers(ctx context.Context, query gatewayRebindQuiescenceQuerier,
	operationID string,
) ([]GatewayRebindAllocationTransfer, error) {
	rows, err := query.QueryContext(ctx, `SELECT ordinal,app_id,allocation_id,grant_attempt_id,
		source_binding_digest,roster_entry_digest,source_profile_revision_id,
		source_profile_revision_number,source_profile_spec_digest,predecessor_transfer_digest,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_spec_digest,terminal_receipt_digest,transfer_digest
		FROM lan_gateway_rebind_allocation_transfers WHERE operation_id=? ORDER BY ordinal`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []GatewayRebindAllocationTransfer
	for rows.Next() {
		value := GatewayRebindAllocationTransfer{Version: GatewayRebindTransferVersionV1, OperationID: operationID}
		var predecessor sql.NullString
		if err := rows.Scan(&value.Ordinal, &value.AppID, &value.AllocationID,
			&value.GrantAttemptID, &value.SourceBindingDigest, &value.RosterEntryDigest,
			&value.SourceProfileRevisionID, &value.SourceProfileRevisionNumber,
			&value.SourceProfileSpecDigest, &predecessor, &value.SuccessorProfileRevisionID,
			&value.SuccessorProfileRevisionNumber, &value.SuccessorProfileSpecDigest,
			&value.TerminalReceiptDigest, &value.TransferDigest); err != nil {
			return nil, err
		}
		if predecessor.Valid {
			value.PredecessorTransferDigest = &predecessor.String
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func validateGatewayRebindRetainedHistory(history GatewayRebindHistoryEntry) error {
	state := historyClaimState(history.Claim)
	sequence := historyClaimSequence(history.Claim)
	createdAt, updatedAt := historyClaimTimes(history.Claim)
	if len(history.Events) != int(sequence) || sequence < 1 || len(history.Commands) != int(sequence-1) {
		return ErrInvalidStoredState
	}
	retainedDisposition := GatewayRebindDispositionNone
	var retainedReceipt string
	databaseCommitted := false
	for index, event := range history.Events {
		wantSequence := int64(index + 1)
		if event.Sequence != wantSequence || event.OperationID != historyClaimOperationID(history.Claim) {
			return ErrInvalidStoredState
		}
		if index == 0 {
			if event.State != GatewayRebindPrepared || !event.CreatedAt.Equal(createdAt) {
				return ErrInvalidStoredState
			}
			continue
		}
		command := history.Commands[index-1]
		if command.Sequence != event.Sequence || command.PreviousSequence != event.Sequence-1 ||
			command.PreviousState != history.Events[index-1].State || command.NextState != event.State ||
			!command.CreatedAt.Equal(event.CreatedAt) {
			return ErrInvalidStoredState
		}
		var proof GatewayRebindTransitionProof
		if err := json.Unmarshal([]byte(command.CanonicalPayload), &proof); err != nil ||
			validateGatewayRebindTransitionProof(proof) != nil || proof.OperationID != command.OperationID ||
			proof.ClaimRequestDigest != historyClaimRequestDigest(history.Claim) ||
			proof.ClaimSpecDigest != historyClaimSpecDigest(history.Claim) ||
			proof.ExpectedState != command.PreviousState || proof.ExpectedSequence != command.PreviousSequence ||
			proof.NextState != command.NextState || proof.ProtectedRecordDigest != command.ProtectedRecordDigest ||
			proof.TerminalReceiptDigest != command.TerminalReceiptDigest ||
			proof.LocalAttestationDigest != command.LocalAttestationDigest ||
			proof.TerminalDisposition != command.TerminalDisposition {
			return ErrInvalidStoredState
		}
		sum := sha256.Sum256([]byte(command.CanonicalPayload))
		if hex.EncodeToString(sum[:]) != command.CommandDigest {
			return ErrInvalidStoredState
		}
		if command.TerminalDisposition != GatewayRebindDispositionNone {
			if retainedDisposition == GatewayRebindDispositionNone {
				retainedDisposition, retainedReceipt = command.TerminalDisposition, command.TerminalReceiptDigest
			} else if retainedDisposition != command.TerminalDisposition || retainedReceipt != command.TerminalReceiptDigest {
				return ErrInvalidStoredState
			}
		}
		predecessorID, predecessorNumber, predecessorDigest := historyClaimPredecessorProfile(history.Claim)
		successorID, successorNumber, successorDigest := historyClaimSuccessorProfile(history.Claim)
		expectedID, expectedNumber, expectedDigest := predecessorID, predecessorNumber, predecessorDigest
		if databaseCommitted {
			expectedID, expectedNumber, expectedDigest = successorID, successorNumber, successorDigest
		}
		if proof.ExpectedHeadRevisionID != expectedID || proof.ExpectedHeadRevisionNumber != expectedNumber ||
			proof.ExpectedHeadSpecDigest != expectedDigest || !proofMatchesVersionedClaimSource(proof, history.Claim) {
			return ErrInvalidStoredState
		}
		if event.State == GatewayRebindDatabaseCommitted {
			if databaseCommitted {
				return ErrInvalidStoredState
			}
			if len(proof.Transfers) != len(history.Transfers) {
				return ErrInvalidStoredState
			}
			for transferIndex := range proof.Transfers {
				if !sameGatewayRebindAllocationTransfer(proof.Transfers[transferIndex], history.Transfers[transferIndex]) {
					return ErrInvalidStoredState
				}
			}
			databaseCommitted = true
		}
		if databaseCommitted && (event.State == GatewayRebindRolledBack || event.State == GatewayRebindSuccessorReady) {
			return ErrInvalidStoredState
		}
	}
	if history.Events[len(history.Events)-1].State != state ||
		!history.Events[len(history.Events)-1].CreatedAt.Equal(updatedAt) {
		return ErrInvalidStoredState
	}
	rosterCount := len(history.RosterV1) + len(history.RosterV2)
	if databaseCommitted {
		if len(history.Transfers) != rosterCount {
			return ErrInvalidStoredState
		}
		for index, transfer := range history.Transfers {
			if transfer.Ordinal != int64(index+1) {
				return ErrInvalidStoredState
			}
			digest, err := GatewayRebindAllocationTransferDigest(transfer)
			if err != nil || digest != transfer.TransferDigest || transfer.TerminalReceiptDigest != retainedReceipt ||
				!transferMatchesRetainedRoster(history, index, transfer) {
				return ErrInvalidStoredState
			}
		}
	} else if len(history.Transfers) != 0 {
		return ErrInvalidStoredState
	}
	return nil
}

func validateActiveGatewayRebindHeads(history GatewayRebindHistoryEntry,
	current GatewayProfileRevision, source GatewayCurrentAuthorityRef, databaseCommitted bool,
) error {
	predecessorID, predecessorNumber, predecessorDigest := historyClaimPredecessorProfile(history.Claim)
	successorID, successorNumber, successorDigest := historyClaimSuccessorProfile(history.Claim)
	if databaseCommitted {
		if current.ID != successorID || current.RevisionNumber != successorNumber || current.SpecDigest != successorDigest ||
			source.Kind != GatewayRebindSourceGatewayRebind || source.OperationID != historyClaimOperationID(history.Claim) ||
			source.ProfileRevisionID != successorID || source.ProfileRevisionNumber != successorNumber ||
			source.ProfileSpecDigest != successorDigest ||
			source.TerminalReceiptDigest != retainedGatewayRebindReceipt(history.Commands) {
			return ErrInvalidStoredState
		}
		return nil
	}
	expectedSource := historyClaimPredecessorAuthority(history.Claim)
	if current.ID != predecessorID || current.RevisionNumber != predecessorNumber || current.SpecDigest != predecessorDigest ||
		source != expectedSource {
		return ErrInvalidStoredState
	}
	return nil
}

func validGatewayRebindState(state GatewayRebindState) bool {
	switch state {
	case GatewayRebindPrepared, GatewayRebindSuccessorReady, GatewayRebindDatabaseCommitted,
		GatewayRebindCommitted, GatewayRebindRolledBack, GatewayRebindUnresolved:
		return true
	default:
		return false
	}
}

func isActiveGatewayRebindState(state GatewayRebindState) bool {
	return state == GatewayRebindPrepared || state == GatewayRebindSuccessorReady ||
		state == GatewayRebindDatabaseCommitted || state == GatewayRebindUnresolved
}

func historyClaimState(record GatewayRebindClaimRecord) GatewayRebindState {
	if record.SpecVersion == 1 && record.Legacy != nil {
		return record.Legacy.State
	}
	if record.SpecVersion == GatewayRebindSpecVersionV2 && record.V2 != nil {
		return record.V2.State
	}
	return ""
}

func historyClaimSequence(record GatewayRebindClaimRecord) int64 {
	if record.Legacy != nil {
		return record.Legacy.StateSequence
	}
	if record.V2 != nil {
		return record.V2.StateSequence
	}
	return 0
}

func historyClaimOperationID(record GatewayRebindClaimRecord) string {
	if record.Legacy != nil {
		return record.Legacy.Spec.OperationID
	}
	if record.V2 != nil {
		return record.V2.Spec.OperationID
	}
	return ""
}

func historyClaimRequestDigest(record GatewayRebindClaimRecord) string {
	if record.Legacy != nil {
		return record.Legacy.RequestDigest
	}
	if record.V2 != nil {
		return record.V2.RequestDigest
	}
	return ""
}

func historyClaimSpecDigest(record GatewayRebindClaimRecord) string {
	if record.Legacy != nil {
		return record.Legacy.RebindApproval.SpecDigest
	}
	if record.V2 != nil {
		return record.V2.RebindApproval.SpecDigest
	}
	return ""
}

func historyClaimTimes(record GatewayRebindClaimRecord) (time.Time, time.Time) {
	if record.Legacy != nil {
		return record.Legacy.CreatedAt, record.Legacy.UpdatedAt
	}
	if record.V2 != nil {
		return record.V2.CreatedAt, record.V2.UpdatedAt
	}
	return time.Time{}, time.Time{}
}

func historyClaimPredecessorProfile(record GatewayRebindClaimRecord) (string, int64, string) {
	if record.Legacy != nil {
		return record.Legacy.Spec.PredecessorProfileRevisionID,
			record.Legacy.Spec.PredecessorProfileRevisionNumber, record.Legacy.Spec.PredecessorProfileSpecDigest
	}
	if record.V2 != nil {
		lineage := record.V2.Spec.Predecessor.Lineage
		return lineage.ProfileRevisionID, lineage.ProfileRevisionNumber, lineage.ProfileSpecDigest
	}
	return "", 0, ""
}

func historyClaimSuccessorProfile(record GatewayRebindClaimRecord) (string, int64, string) {
	if record.Legacy != nil {
		return record.Legacy.Spec.SuccessorProfileRevisionID,
			record.Legacy.Spec.SuccessorProfileRevisionNumber, record.Legacy.ConfigureApproval.SpecDigest
	}
	if record.V2 != nil {
		return record.V2.Spec.SuccessorProfileRevisionID,
			record.V2.Spec.SuccessorProfileRevisionNumber, record.V2.ConfigureApproval.SpecDigest
	}
	return "", 0, ""
}

func proofMatchesVersionedClaimSource(proof GatewayRebindTransitionProof,
	record GatewayRebindClaimRecord,
) bool {
	if record.V2 == nil {
		return record.Legacy != nil
	}
	source := record.V2.Spec.Predecessor
	return proof.PredecessorCheckpointDigest == source.PredecessorCheckpointDigest &&
		proof.SourceStateVersion == source.SourceStateVersion &&
		proof.SourceStateRevision == source.SourceStateRevision &&
		proof.SourceStateDigest == source.SourceStateDigest &&
		proof.ProtectedGeneration == record.V2.Spec.SuccessorProtectedGeneration
}

func transferMatchesRetainedRoster(history GatewayRebindHistoryEntry, index int,
	transfer GatewayRebindAllocationTransfer,
) bool {
	if transfer.OperationID != historyClaimOperationID(history.Claim) {
		return false
	}
	successorID, successorNumber, successorDigest := historyClaimSuccessorProfile(history.Claim)
	if transfer.SuccessorProfileRevisionID != successorID ||
		transfer.SuccessorProfileRevisionNumber != successorNumber ||
		transfer.SuccessorProfileSpecDigest != successorDigest {
		return false
	}
	if history.Claim.Legacy != nil {
		if index >= len(history.RosterV1) {
			return false
		}
		entry := history.RosterV1[index]
		sourceID, sourceNumber, sourceDigest := historyClaimPredecessorProfile(history.Claim)
		return transfer.Ordinal == entry.Ordinal && transfer.AppID == entry.AppID &&
			transfer.AllocationID == entry.AllocationID && transfer.GrantAttemptID == entry.GrantAttemptID &&
			transfer.RosterEntryDigest == entry.EntryDigest && transfer.SourceProfileRevisionID == sourceID &&
			transfer.SourceProfileRevisionNumber == sourceNumber &&
			transfer.SourceProfileSpecDigest == sourceDigest && transfer.PredecessorTransferDigest == nil
	}
	if history.Claim.V2 != nil && index < len(history.RosterV2) {
		entry := history.RosterV2[index]
		return transfer.Ordinal == entry.Ordinal && transfer.AppID == entry.AppID &&
			transfer.AllocationID == entry.AllocationID && transfer.GrantAttemptID == entry.GrantAttemptID &&
			transfer.RosterEntryDigest == entry.EntryDigest &&
			transfer.SourceProfileRevisionID == entry.SourceProfileRevisionID &&
			transfer.SourceProfileRevisionNumber == entry.SourceProfileRevisionNumber &&
			transfer.SourceProfileSpecDigest == entry.SourceProfileSpecDigest &&
			optionalDigestEqual(transfer.PredecessorTransferDigest, entry.PredecessorTransferDigest)
	}
	return false
}

func retainedGatewayRebindDisposition(commands []GatewayRebindTransitionCommand) GatewayRebindTerminalDisposition {
	for _, command := range commands {
		if command.TerminalDisposition != GatewayRebindDispositionNone {
			return command.TerminalDisposition
		}
	}
	return GatewayRebindDispositionNone
}

func retainedGatewayRebindReceipt(commands []GatewayRebindTransitionCommand) string {
	for _, command := range commands {
		if command.TerminalDisposition != GatewayRebindDispositionNone {
			return command.TerminalReceiptDigest
		}
	}
	return ""
}

func historyClaimPredecessorAuthority(record GatewayRebindClaimRecord) GatewayCurrentAuthorityRef {
	profileID, profileNumber, profileDigest := historyClaimPredecessorProfile(record)
	if record.Legacy != nil {
		return GatewayCurrentAuthorityRef{
			Kind: GatewayRebindSourceGatewayUpgrade, OperationID: record.Legacy.Spec.PredecessorUpgradeOperationID,
			ProfileRevisionID: profileID, ProfileRevisionNumber: profileNumber, ProfileSpecDigest: profileDigest,
		}
	}
	if record.V2 != nil {
		lineage := record.V2.Spec.Predecessor.Lineage
		return GatewayCurrentAuthorityRef{
			Kind: lineage.Kind, OperationID: lineage.OperationID,
			ProfileRevisionID: profileID, ProfileRevisionNumber: profileNumber,
			ProfileSpecDigest: profileDigest, TerminalReceiptDigest: lineage.TerminalReceiptDigest,
		}
	}
	return GatewayCurrentAuthorityRef{}
}

func findGatewayRebindHistory(history []GatewayRebindHistoryEntry,
	operationID string,
) *GatewayRebindHistoryEntry {
	for index := range history {
		if historyClaimOperationID(history[index].Claim) == operationID {
			return &history[index]
		}
	}
	return nil
}
