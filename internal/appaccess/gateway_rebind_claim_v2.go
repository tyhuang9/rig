package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"time"
)

// ClaimGatewayRebindV2 repeats the complete approval, current lineage,
// roster, and quiescence checks while holding SQLite's writer reservation,
// then persists one prepared claim and its complete versioned roster.
func (r *Repository) ClaimGatewayRebindV2(ctx context.Context,
	proposal GatewayRebindPreclaimProposalV2,
) (claim GatewayRebindClaimV2, created bool, resultErr error) {
	if r == nil || r.db == nil || ctx == nil || validateGatewayRebindProposalV2(proposal) != nil {
		return GatewayRebindClaimV2{}, false, ErrInvalidInput
	}
	proposal.Roster = append([]GatewayRebindRosterEntryV2(nil), proposal.Roster...)
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayRebindClaimV2{}, false, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			claim, created = GatewayRebindClaimV2{}, false
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()

	requestDigest, err := gatewayRebindClaimV2RequestDigest(proposal.Spec,
		proposal.RebindApproval, proposal.ConfigureApproval)
	if err != nil {
		return GatewayRebindClaimV2{}, false, ErrInvalidInput
	}
	if existing, readErr := readGatewayRebindClaimV2(ctx, tx, proposal.Spec.OperationID); readErr == nil {
		storedRoster, rosterErr := readGatewayRebindRosterV2(ctx, tx, proposal.Spec.OperationID)
		if rosterErr != nil {
			return GatewayRebindClaimV2{}, false, rosterErr
		}
		if existing.RequestDigest != requestDigest || existing.Spec != proposal.Spec ||
			existing.RebindApproval != proposal.RebindApproval ||
			existing.ConfigureApproval != proposal.ConfigureApproval ||
			!reflect.DeepEqual(storedRoster, proposal.Roster) {
			return GatewayRebindClaimV2{}, false, ErrIdempotencyMismatch
		}
		if err := validateStoredGatewayRebindClaimV2(existing, storedRoster); err != nil {
			return GatewayRebindClaimV2{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return GatewayRebindClaimV2{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return GatewayRebindClaimV2{}, false, readErr
	}

	currentProfile, currentSource, err := readGatewayCurrentAuthority(ctx, tx)
	if err != nil {
		return GatewayRebindClaimV2{}, false, err
	}
	lineage := proposal.Spec.Predecessor.Lineage
	if currentProfile.ID != lineage.ProfileRevisionID ||
		currentProfile.RevisionNumber != lineage.ProfileRevisionNumber ||
		currentProfile.SpecDigest != lineage.ProfileSpecDigest ||
		currentSource != (GatewayCurrentAuthorityRef{
			Kind: lineage.Kind, OperationID: lineage.OperationID,
			ProfileRevisionID:     lineage.ProfileRevisionID,
			ProfileRevisionNumber: lineage.ProfileRevisionNumber,
			ProfileSpecDigest:     lineage.ProfileSpecDigest,
			TerminalReceiptDigest: lineage.TerminalReceiptDigest,
		}) {
		return GatewayRebindClaimV2{}, false, ErrInvalidStoredState
	}
	for _, actorID := range []string{proposal.RebindApproval.ActorID, proposal.ConfigureApproval.ActorID} {
		administrator, lookupErr := administratorExists(ctx, tx, actorID)
		if lookupErr != nil {
			return GatewayRebindClaimV2{}, false, lookupErr
		}
		if !administrator {
			return GatewayRebindClaimV2{}, false, ErrApprovalRequired
		}
	}
	var successorExists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lan_gateway_profile_revisions
		WHERE id=? OR revision_number=? OR operation_id=?)`, proposal.Spec.SuccessorProfileRevisionID,
		proposal.Spec.SuccessorProfileRevisionNumber, proposal.Spec.SuccessorProfileOperationID).
		Scan(&successorExists); err != nil {
		return GatewayRebindClaimV2{}, false, err
	}
	if successorExists != 0 {
		return GatewayRebindClaimV2{}, false, ErrConflict
	}
	if err := validateGatewayRebindSettledRosterV2(ctx, tx, proposal.Spec, proposal.Roster); err != nil {
		return GatewayRebindClaimV2{}, false, err
	}
	if _, err := evaluateGatewayRebindQuiescence(ctx, tx, nil); err != nil {
		return GatewayRebindClaimV2{}, false, err
	}

	canonicalSuccessor, _ := canonicalGatewaySpec(proposal.Spec.SuccessorProfile)
	successorRequestDigest, err := gatewayRequestDigest(ConfigureGatewayInput{
		OperationID:            proposal.Spec.SuccessorProfileOperationID,
		ExpectedRevisionNumber: lineage.ProfileRevisionNumber,
		Spec:                   canonicalSuccessor, Approval: proposal.ConfigureApproval,
	}, canonicalSuccessor)
	if err != nil {
		return GatewayRebindClaimV2{}, false, ErrInvalidInput
	}
	now := r.now().UTC()
	claim = GatewayRebindClaimV2{
		Spec: proposal.Spec, RequestDigest: requestDigest,
		RebindApproval: proposal.RebindApproval, RebindApprovedAt: now,
		ConfigureApproval: proposal.ConfigureApproval, ConfigureApprovedAt: now,
		SuccessorProfileRequestDigest: successorRequestDigest,
		State:                         GatewayRebindPrepared, StateSequence: 1, CreatedAt: now, UpdatedAt: now,
	}
	var predecessorUpgrade, predecessorRebind any
	if lineage.Kind == GatewayRebindSourceGatewayUpgrade {
		predecessorUpgrade = lineage.OperationID
	} else {
		predecessorRebind = lineage.OperationID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_gateway_rebind_claims(
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
		predecessor_source_state_digest,predecessor_checkpoint_digest
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		proposal.Spec.OperationID, requestDigest, ActionRebindGateway,
		proposal.RebindApproval.SpecDigest, proposal.RebindApproval.ActorID, formatTime(now),
		lineage.ProfileRevisionID, lineage.ProfileRevisionNumber, lineage.ProfileSpecDigest,
		predecessorUpgrade, lineage.ProtectedIdentityDigest, lineage.Kind, predecessorRebind,
		nullableText(lineage.TerminalReceiptDigest), proposal.Spec.SuccessorProfileRevisionID,
		proposal.Spec.SuccessorProfileRevisionNumber, proposal.Spec.SuccessorProfileOperationID,
		successorRequestDigest, canonicalSuccessor.SelectedIPv4, canonicalSuccessor.InterfaceID,
		canonicalSuccessor.PortStart, canonicalSuccessor.PortEnd, proposal.ConfigureApproval.SpecDigest,
		ActionConfigureGateway, proposal.ConfigureApproval.ActorID, formatTime(now),
		proposal.Spec.RosterDigest, proposal.Spec.RosterCount, claim.State, claim.StateSequence,
		formatTime(now), formatTime(now), GatewayRebindSpecVersionV2, GatewayRebindRosterVersionV2,
		int64(lineage.ProtectedGeneration), nullableText(lineage.ProtectedJournalDigest),
		nullableText(lineage.ProtectedIntentDigest), int64(proposal.Spec.Predecessor.SourceStateVersion),
		int64(proposal.Spec.Predecessor.SourceStateRevision), proposal.Spec.Predecessor.SourceStateDigest,
		proposal.Spec.Predecessor.PredecessorCheckpointDigest); err != nil {
		return GatewayRebindClaimV2{}, false, classifyImmediateTransactionError(err)
	}
	for _, entry := range proposal.Roster {
		if _, err := tx.ExecContext(ctx, `INSERT INTO lan_gateway_rebind_roster_entries(
			operation_id,ordinal,app_id,allocation_id,allocated_port,
			allocation_owner_operation_id,allocation_state,access_revision_id,
			access_revision_number,access_spec_digest,grant_attempt_id,grant_state_sequence,
			grant_protected_state_digest,serving_deployment_id,serving_release_id,
			serving_slot,route_generation,entry_digest,roster_format_version,
			source_profile_revision_id,source_profile_revision_number,
			source_profile_spec_digest,predecessor_transfer_digest
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			entry.OperationID, entry.Ordinal, entry.AppID, entry.AllocationID, entry.Port,
			entry.AllocationOwnerOperationID, entry.AllocationState, entry.AccessRevisionID,
			entry.AccessRevisionNumber, entry.AccessSpecDigest, entry.GrantAttemptID,
			entry.GrantStateSequence, entry.GrantProtectedStateDigest,
			entry.ServingDeploymentID, entry.ServingReleaseID, entry.ServingSlot,
			entry.RouteGeneration, entry.EntryDigest, GatewayRebindRosterVersionV2,
			entry.SourceProfileRevisionID, entry.SourceProfileRevisionNumber,
			entry.SourceProfileSpecDigest, entry.PredecessorTransferDigest); err != nil {
			return GatewayRebindClaimV2{}, false, classifyImmediateTransactionError(err)
		}
	}
	stored, err := readGatewayRebindClaimV2(ctx, tx, proposal.Spec.OperationID)
	if err != nil {
		return GatewayRebindClaimV2{}, false, err
	}
	storedRoster, err := readGatewayRebindRosterV2(ctx, tx, proposal.Spec.OperationID)
	if err != nil || stored.RequestDigest != claim.RequestDigest || stored.Spec != claim.Spec ||
		!reflect.DeepEqual(storedRoster, proposal.Roster) {
		return GatewayRebindClaimV2{}, false, invalidRebindStoredState(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GatewayRebindClaimV2{}, false, err
	}
	return stored, true, nil
}

func validateGatewayRebindProposalV2(proposal GatewayRebindPreclaimProposalV2) error {
	canonical, canonicalErr := canonicalGatewaySpec(proposal.Spec.SuccessorProfile)
	specDigest, err := GatewayRebindSpecV2Digest(proposal.Spec)
	if canonicalErr != nil || canonical != proposal.Spec.SuccessorProfile || err != nil ||
		!validApproval(proposal.RebindApproval, ActionRebindGateway, specDigest) {
		return ErrInvalidInput
	}
	profileDigest, err := GatewayProfileSpecDigest(proposal.Spec.SuccessorProfile)
	if err != nil || !validApproval(proposal.ConfigureApproval, ActionConfigureGateway, profileDigest) ||
		proposal.Spec.RosterCount != int64(len(proposal.Roster)) {
		return ErrInvalidInput
	}
	for index := range proposal.Roster {
		entry := proposal.Roster[index]
		digest, digestErr := GatewayRebindRosterEntryV2Digest(entry)
		if digestErr != nil || entry.EntryDigest != digest || entry.OperationID != proposal.Spec.OperationID ||
			entry.Ordinal != int64(index+1) || entry.Port < proposal.Spec.SuccessorProfile.PortStart ||
			entry.Port > proposal.Spec.SuccessorProfile.PortEnd {
			return ErrInvalidInput
		}
	}
	rosterDigest, err := GatewayRebindRosterV2Digest(proposal.Roster)
	if err != nil || rosterDigest != proposal.Spec.RosterDigest {
		return ErrInvalidInput
	}
	return nil
}

func validateGatewayRebindSettledRosterV2(ctx context.Context, tx gatewayBindingQuerier,
	spec GatewayRebindSpecV2, roster []GatewayRebindRosterEntryV2,
) error {
	var liveCount, unsettled, pendingGrants, pendingDisables int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE
		WHEN state<>'active' OR port<? OR port>? THEN 1 ELSE 0 END),0)
		FROM lan_port_allocations WHERE released_at IS NULL AND disabled_at IS NULL`,
		spec.SuccessorProfile.PortStart, spec.SuccessorProfile.PortEnd).Scan(&liveCount, &unsettled); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_app_access_grant_claims
		WHERE state IN ('prepared','applying','db_active','uncertain') AND retired_at IS NULL`).Scan(&pendingGrants); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_app_access_disable_claims
		WHERE state IN ('prepared','withdrawing','uncertain')`).Scan(&pendingDisables); err != nil {
		return err
	}
	if liveCount != int64(len(roster)) || unsettled != 0 || pendingGrants != 0 || pendingDisables != 0 {
		return ErrInvalidStoredState
	}
	for _, entry := range roster {
		allocation, err := readAllocationByID(ctx, tx, entry.AllocationID)
		if err != nil {
			return invalidRebindStoredState(err)
		}
		revision, _, err := readAccessRevision(ctx, tx, entry.AppID,
			entry.AccessRevisionID, entry.AccessRevisionNumber)
		if err != nil {
			return invalidRebindStoredState(err)
		}
		grant, err := readAppAccessGrantClaim(ctx, tx, entry.GrantAttemptID)
		if err != nil {
			return invalidRebindStoredState(err)
		}
		resolution, err := resolveGatewayBinding(ctx, tx, GatewayBindingRef{
			AppID: entry.AppID, AllocationID: entry.AllocationID,
			AccessRevisionID: entry.AccessRevisionID, GrantAttemptID: entry.GrantAttemptID,
		})
		if err != nil || allocation.AppID != entry.AppID || allocation.Port != entry.Port ||
			allocation.OwnerOperationID != entry.AllocationOwnerOperationID || allocation.State != entry.AllocationState ||
			allocation.ReleasedAt != nil || revision.SpecDigest != entry.AccessSpecDigest ||
			grant.State != AppAccessGrantCommitted || grant.StateSequence != entry.GrantStateSequence || grant.Proof == nil ||
			grant.Proof.ProtectedStateDigest != entry.GrantProtectedStateDigest ||
			resolution.EffectiveProfile.ID != entry.SourceProfileRevisionID ||
			resolution.EffectiveProfile.RevisionNumber != entry.SourceProfileRevisionNumber ||
			resolution.EffectiveProfile.SpecDigest != entry.SourceProfileSpecDigest ||
			!optionalStringMatches(entry.PredecessorTransferDigest, resolution.TransferChainTipDigest) {
			return invalidRebindStoredState(err)
		}
		var deploymentID, releaseID, slot sql.NullString
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT deployment_id,release_id,slot,generation
			FROM generated_runtime_active_heads WHERE app_id=?`, entry.AppID).
			Scan(&deploymentID, &releaseID, &slot, &generation); err != nil ||
			!deploymentID.Valid || !releaseID.Valid || !slot.Valid ||
			deploymentID.String != entry.ServingDeploymentID || releaseID.String != entry.ServingReleaseID ||
			slot.String != entry.ServingSlot || generation != entry.RouteGeneration {
			return invalidRebindStoredState(err)
		}
	}
	return nil
}

func optionalStringMatches(value *string, expected string) bool {
	if expected == "" {
		return value == nil
	}
	return value != nil && *value == expected
}

func gatewayRebindClaimV2RequestDigest(spec GatewayRebindSpecV2,
	rebind, configure Approval,
) (string, error) {
	return digestJSON(struct {
		Spec      GatewayRebindSpecV2 `json:"spec"`
		Rebind    Approval            `json:"rebindApproval"`
		Configure Approval            `json:"configureApproval"`
	}{Spec: spec, Rebind: rebind, Configure: configure})
}

func readGatewayRebindClaimV2(ctx context.Context, query rowQuerier,
	operationID string,
) (GatewayRebindClaimV2, error) {
	var value GatewayRebindClaimV2
	var sourceKind GatewayRebindSourceKind
	var upgradeOperation, rebindOperation, terminalReceipt sql.NullString
	var journalDigest, intentDigest sql.NullString
	var rebindApprovedAt, configureApprovedAt, createdAt, updatedAt string
	value.Spec.OperationID = operationID
	value.Spec.Version = GatewayRebindSpecVersionV2
	value.Spec.RosterVersion = GatewayRebindRosterVersionV2
	err := query.QueryRowContext(ctx, `SELECT request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,predecessor_profile_spec_digest,
		predecessor_upgrade_operation_id,predecessor_protected_identity_digest,predecessor_source_kind,
		predecessor_rebind_operation_id,predecessor_terminal_receipt_digest,
		successor_profile_revision_id,successor_profile_revision_number,successor_profile_operation_id,
		successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		successor_port_start,successor_port_end,successor_profile_spec_digest,
		configure_approval_action,configure_approved_by,configure_approved_at,
		roster_digest,roster_count,state,state_sequence,created_at,updated_at,
		predecessor_protected_generation,predecessor_protected_journal_digest,
		predecessor_protected_intent_digest,predecessor_source_state_version,
		predecessor_source_state_revision,predecessor_source_state_digest,
		predecessor_checkpoint_digest
		FROM lan_gateway_rebind_claims WHERE operation_id=? AND spec_format_version=2
		 AND roster_format_version=2`, operationID).Scan(&value.RequestDigest,
		&value.RebindApproval.Action, &value.RebindApproval.SpecDigest,
		&value.RebindApproval.ActorID, &rebindApprovedAt,
		&value.Spec.Predecessor.Lineage.ProfileRevisionID,
		&value.Spec.Predecessor.Lineage.ProfileRevisionNumber,
		&value.Spec.Predecessor.Lineage.ProfileSpecDigest, &upgradeOperation,
		&value.Spec.Predecessor.Lineage.ProtectedIdentityDigest, &sourceKind, &rebindOperation,
		&terminalReceipt, &value.Spec.SuccessorProfileRevisionID,
		&value.Spec.SuccessorProfileRevisionNumber, &value.Spec.SuccessorProfileOperationID,
		&value.SuccessorProfileRequestDigest, &value.Spec.SuccessorProfile.SelectedIPv4,
		&value.Spec.SuccessorProfile.InterfaceID, &value.Spec.SuccessorProfile.PortStart,
		&value.Spec.SuccessorProfile.PortEnd, &value.ConfigureApproval.SpecDigest,
		&value.ConfigureApproval.Action, &value.ConfigureApproval.ActorID, &configureApprovedAt,
		&value.Spec.RosterDigest, &value.Spec.RosterCount, &value.State, &value.StateSequence,
		&createdAt, &updatedAt, &value.Spec.Predecessor.Lineage.ProtectedGeneration,
		&journalDigest, &intentDigest, &value.Spec.Predecessor.SourceStateVersion,
		&value.Spec.Predecessor.SourceStateRevision, &value.Spec.Predecessor.SourceStateDigest,
		&value.Spec.Predecessor.PredecessorCheckpointDigest)
	if err != nil {
		return GatewayRebindClaimV2{}, err
	}
	value.Spec.Predecessor.Lineage.Kind = sourceKind
	if sourceKind == GatewayRebindSourceGatewayUpgrade {
		value.Spec.Predecessor.Lineage.OperationID = upgradeOperation.String
	} else {
		value.Spec.Predecessor.Lineage.OperationID = rebindOperation.String
	}
	value.Spec.Predecessor.Lineage.ProtectedJournalDigest = journalDigest.String
	value.Spec.Predecessor.Lineage.ProtectedIntentDigest = intentDigest.String
	value.Spec.Predecessor.Lineage.TerminalReceiptDigest = terminalReceipt.String
	for _, stamp := range []struct {
		text   string
		target *time.Time
	}{
		{rebindApprovedAt, &value.RebindApprovedAt},
		{configureApprovedAt, &value.ConfigureApprovedAt},
		{createdAt, &value.CreatedAt},
		{updatedAt, &value.UpdatedAt},
	} {
		parsed, parseErr := parseTime(stamp.text)
		if parseErr != nil {
			return GatewayRebindClaimV2{}, ErrInvalidStoredState
		}
		*stamp.target = parsed
	}
	return value, nil
}

func readGatewayRebindRosterV2(ctx context.Context, query gatewayRebindQuiescenceQuerier,
	operationID string,
) ([]GatewayRebindRosterEntryV2, error) {
	rows, err := query.QueryContext(ctx, `SELECT ordinal,app_id,allocation_id,allocated_port,
		allocation_owner_operation_id,allocation_state,access_revision_id,access_revision_number,
		access_spec_digest,grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		source_profile_revision_id,source_profile_revision_number,source_profile_spec_digest,
		predecessor_transfer_digest,serving_deployment_id,serving_release_id,serving_slot,
		route_generation,entry_digest FROM lan_gateway_rebind_roster_entries
		WHERE operation_id=? AND roster_format_version=2 ORDER BY ordinal`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []GatewayRebindRosterEntryV2
	for rows.Next() {
		value := GatewayRebindRosterEntryV2{Version: GatewayRebindRosterVersionV2, OperationID: operationID}
		var predecessor sql.NullString
		if err := rows.Scan(&value.Ordinal, &value.AppID, &value.AllocationID, &value.Port,
			&value.AllocationOwnerOperationID, &value.AllocationState, &value.AccessRevisionID,
			&value.AccessRevisionNumber, &value.AccessSpecDigest, &value.GrantAttemptID,
			&value.GrantStateSequence, &value.GrantProtectedStateDigest,
			&value.SourceProfileRevisionID, &value.SourceProfileRevisionNumber,
			&value.SourceProfileSpecDigest, &predecessor, &value.ServingDeploymentID,
			&value.ServingReleaseID, &value.ServingSlot, &value.RouteGeneration,
			&value.EntryDigest); err != nil {
			return nil, err
		}
		if predecessor.Valid {
			value.PredecessorTransferDigest = &predecessor.String
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Ordinal < values[j].Ordinal })
	return values, nil
}
