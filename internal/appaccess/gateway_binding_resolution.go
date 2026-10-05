package appaccess

import (
	"context"
	"database/sql"
	"sort"
)

type gatewayBindingQuerier interface {
	appAccessGrantQuerier
}

// ResolveGatewayBinding validates the immutable raw allocation, access
// revision, and grant, then walks every retained transfer to the SQL-selected
// current gateway profile. Historical route facts are checked against their
// own retained roster rather than today's mutable runtime head.
func (r *Repository) ResolveGatewayBinding(ctx context.Context,
	ref GatewayBindingRef,
) (GatewayBindingResolution, error) {
	if r == nil || r.db == nil || ctx == nil || !validGatewayBindingRef(ref) {
		return GatewayBindingResolution{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayBindingResolution{}, err
	}
	defer tx.Rollback()
	value, err := resolveGatewayBinding(ctx, tx, ref)
	if err != nil {
		return GatewayBindingResolution{}, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayBindingResolution{}, err
	}
	return value, nil
}

func validGatewayBindingRef(ref GatewayBindingRef) bool {
	return validUUID(ref.AppID) && validUUID(ref.AllocationID) &&
		validUUID(ref.AccessRevisionID) && validUUID(ref.GrantAttemptID)
}

func resolveGatewayBinding(ctx context.Context, query gatewayBindingQuerier,
	ref GatewayBindingRef,
) (GatewayBindingResolution, error) {
	allocation, err := readAllocationByID(ctx, query, ref.AllocationID)
	if err != nil {
		return GatewayBindingResolution{}, invalidRebindStoredState(err)
	}
	if allocation.AppID != ref.AppID {
		return GatewayBindingResolution{}, ErrInvalidStoredState
	}
	var accessNumber int64
	if err := query.QueryRowContext(ctx, `SELECT revision_number FROM lan_app_access_revisions
		WHERE app_id=? AND id=?`, ref.AppID, ref.AccessRevisionID).Scan(&accessNumber); err != nil {
		return GatewayBindingResolution{}, invalidRebindStoredState(err)
	}
	revision, _, err := readAccessRevision(ctx, query, ref.AppID, ref.AccessRevisionID, accessNumber)
	if err != nil {
		return GatewayBindingResolution{}, invalidRebindStoredState(err)
	}
	grant, err := readAppAccessGrantClaim(ctx, query, ref.GrantAttemptID)
	if err != nil {
		return GatewayBindingResolution{}, invalidRebindStoredState(err)
	}
	if revision.Allocation.ID != allocation.ID || revision.OperationID != allocation.OwnerOperationID ||
		grant.Spec.AppID != ref.AppID || grant.Spec.AllocationID != allocation.ID ||
		grant.Spec.OwnerOperationID != allocation.OwnerOperationID ||
		grant.Spec.AccessRevisionID != revision.ID || grant.Spec.AccessRevisionNumber != revision.RevisionNumber ||
		grant.Spec.AccessSpecDigest != revision.SpecDigest || grant.Spec.Port != allocation.Port ||
		grant.Spec.GatewayProfileRevisionID != allocation.GatewayProfileRevisionID ||
		grant.Spec.GatewayProfileRevisionNumber != allocation.GatewayProfileRevisionNumber ||
		grant.State != AppAccessGrantCommitted || grant.Proof == nil {
		return GatewayBindingResolution{}, ErrInvalidStoredState
	}
	rawProfile, _, err := readGatewayRevision(ctx, query, allocation.GatewayProfileRevisionID,
		allocation.GatewayProfileRevisionNumber)
	if err != nil || rawProfile.SpecDigest != grant.Spec.GatewayProfileSpecDigest {
		return GatewayBindingResolution{}, invalidRebindStoredState(err)
	}
	currentProfile, currentSource, err := readGatewayCurrentAuthority(ctx, query)
	if err != nil {
		return GatewayBindingResolution{}, err
	}
	transferRecords, err := readGatewayBindingTransfers(ctx, query, ref)
	if err != nil {
		return GatewayBindingResolution{}, err
	}

	chain := make([]GatewayRebindAllocationTransfer, 0, len(transferRecords))
	profileID, profileNumber, profileDigest := rawProfile.ID, rawProfile.RevisionNumber, rawProfile.SpecDigest
	var predecessor *string
	used := make(map[int]struct{}, len(transferRecords))
	for profileID != currentProfile.ID || profileNumber != currentProfile.RevisionNumber || profileDigest != currentProfile.SpecDigest {
		matched := -1
		for index := range transferRecords {
			if _, exists := used[index]; exists {
				continue
			}
			record := transferRecords[index]
			transfer := record.Transfer
			if record.PredecessorProfileRevisionID == profileID &&
				record.PredecessorProfileRevisionNumber == profileNumber &&
				record.PredecessorProfileSpecDigest == profileDigest &&
				transfer.SourceProfileRevisionID == rawProfile.ID &&
				transfer.SourceProfileRevisionNumber == rawProfile.RevisionNumber &&
				transfer.SourceProfileSpecDigest == rawProfile.SpecDigest &&
				optionalDigestEqual(transfer.PredecessorTransferDigest, predecessor) {
				if matched != -1 {
					return GatewayBindingResolution{}, ErrInvalidStoredState
				}
				matched = index
			}
		}
		if matched == -1 {
			return GatewayBindingResolution{}, ErrInvalidStoredState
		}
		used[matched] = struct{}{}
		transfer := transferRecords[matched].Transfer
		chain = append(chain, transfer)
		profileID = transfer.SuccessorProfileRevisionID
		profileNumber = transfer.SuccessorProfileRevisionNumber
		profileDigest = transfer.SuccessorProfileSpecDigest
		digest := transfer.TransferDigest
		predecessor = &digest
	}
	if len(used) != len(transferRecords) {
		return GatewayBindingResolution{}, ErrInvalidStoredState
	}
	if len(chain) == 0 && (rawProfile.ID != currentProfile.ID ||
		rawProfile.RevisionNumber != currentProfile.RevisionNumber || rawProfile.SpecDigest != currentProfile.SpecDigest) {
		return GatewayBindingResolution{}, ErrInvalidStoredState
	}
	value := GatewayBindingResolution{
		RawAllocation: allocation, RawAccessRevision: revision, RawGrant: grant,
		RawProfile: rawProfile, EffectiveProfile: currentProfile, CurrentGatewaySource: currentSource,
		TransferChain: chain, TerminalReceiptDigest: currentSource.TerminalReceiptDigest,
	}
	if len(chain) > 0 {
		value.TransferChainTipDigest = chain[len(chain)-1].TransferDigest
	}
	return value, nil
}

// resolveGatewayBindingForConsumer preserves pre-upgrade native histories that
// have no SQL lineage authority at all. Once any authority exists, or the raw
// profile is no longer the head, consumers require the strict resolver.
func resolveGatewayBindingForConsumer(ctx context.Context, query gatewayBindingQuerier,
	ref GatewayBindingRef, rawProfile GatewayProfileRevision,
) (GatewayBindingResolution, error) {
	var headID sql.NullString
	var headNumber int64
	if err := query.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&headID, &headNumber); err != nil {
		return GatewayBindingResolution{}, err
	}
	if !headID.Valid {
		return GatewayBindingResolution{}, ErrInvalidStoredState
	}
	if headID.String != rawProfile.ID || headNumber != rawProfile.RevisionNumber {
		return resolveGatewayBinding(ctx, query, ref)
	}
	hasAuthority, err := hasGatewayCurrentAuthorityCandidate(ctx, query, rawProfile)
	if err != nil {
		return GatewayBindingResolution{}, err
	}
	if hasAuthority {
		return resolveGatewayBinding(ctx, query, ref)
	}
	return GatewayBindingResolution{RawProfile: rawProfile, EffectiveProfile: rawProfile}, nil
}

func readOptionalGatewayCurrentAuthority(ctx context.Context, query gatewayBindingQuerier,
	profile GatewayProfileRevision,
) (GatewayCurrentAuthorityRef, error) {
	var headID sql.NullString
	var headNumber int64
	if err := query.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&headID, &headNumber); err != nil {
		return GatewayCurrentAuthorityRef{}, err
	}
	if !headID.Valid || headID.String != profile.ID || headNumber != profile.RevisionNumber {
		return GatewayCurrentAuthorityRef{}, ErrInvalidStoredState
	}
	hasAuthority, err := hasGatewayCurrentAuthorityCandidate(ctx, query, profile)
	if err != nil || !hasAuthority {
		return GatewayCurrentAuthorityRef{}, err
	}
	current, source, err := readGatewayCurrentAuthority(ctx, query)
	if err != nil {
		return GatewayCurrentAuthorityRef{}, err
	}
	if current.ID != profile.ID || current.RevisionNumber != profile.RevisionNumber ||
		current.SpecDigest != profile.SpecDigest {
		return GatewayCurrentAuthorityRef{}, ErrInvalidStoredState
	}
	return source, nil
}

func hasGatewayCurrentAuthorityCandidate(ctx context.Context, query gatewayBindingQuerier,
	profile GatewayProfileRevision,
) (bool, error) {
	var count int64
	if err := query.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM lan_gateway_rebind_claims c
		 JOIN lan_gateway_rebind_claim_events e ON e.operation_id=c.operation_id
		  AND e.state='database_committed'
		 WHERE c.state IN ('database_committed','unresolved','committed')
		  AND c.successor_profile_revision_id=? AND c.successor_profile_revision_number=?
		  AND c.successor_profile_spec_digest=?)
		+
		(SELECT COUNT(*) FROM lan_gateway_upgrade_claims u
		 WHERE u.state='committed' AND u.profile_revision_id=? AND u.profile_revision_number=?
		  AND u.profile_spec_digest=?)`, profile.ID, profile.RevisionNumber, profile.SpecDigest,
		profile.ID, profile.RevisionNumber, profile.SpecDigest).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func readGatewayCurrentAuthority(ctx context.Context, query gatewayBindingQuerier,
) (GatewayProfileRevision, GatewayCurrentAuthorityRef, error) {
	var profileID sql.NullString
	var profileNumber int64
	if err := query.QueryRowContext(ctx, `SELECT revision_id,revision_number
		FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&profileID, &profileNumber); err != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	if !profileID.Valid {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, ErrInvalidStoredState
	}
	profile, _, err := readGatewayRevision(ctx, query, profileID.String, profileNumber)
	if err != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, invalidRebindStoredState(err)
	}
	rows, err := query.QueryContext(ctx, `SELECT c.operation_id,cmd.terminal_receipt_digest
		FROM lan_gateway_rebind_claims c
		JOIN lan_gateway_rebind_claim_events e ON e.operation_id=c.operation_id
		 AND e.state='database_committed'
		JOIN lan_gateway_rebind_transition_commands cmd ON cmd.operation_id=e.operation_id
		 AND cmd.sequence=e.sequence AND cmd.next_state='database_committed'
		WHERE c.state IN ('database_committed','unresolved','committed')
		 AND c.successor_profile_revision_id=?
		 AND c.successor_profile_revision_number=? AND c.successor_profile_spec_digest=?`,
		profile.ID, profile.RevisionNumber, profile.SpecDigest)
	if err != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	var operationID, receipt string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&operationID, &receipt); err != nil {
			_ = rows.Close()
			return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	if err := rows.Close(); err != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	if count > 1 || (count == 1 && (!validUUID(operationID) || !validDigest(receipt))) {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, ErrInvalidStoredState
	}
	if count == 1 {
		return profile, GatewayCurrentAuthorityRef{
			Kind: GatewayRebindSourceGatewayRebind, OperationID: operationID,
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
			ProfileSpecDigest: profile.SpecDigest, TerminalReceiptDigest: receipt,
		}, nil
	}
	rows, err = query.QueryContext(ctx, `SELECT operation_id FROM lan_gateway_upgrade_claims
		WHERE state='committed' AND profile_revision_id=? AND profile_revision_number=?
		 AND profile_spec_digest=?`, profile.ID, profile.RevisionNumber, profile.SpecDigest)
	if err != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	operationID, count = "", 0
	for rows.Next() {
		if err := rows.Scan(&operationID); err != nil {
			_ = rows.Close()
			return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	if err := rows.Close(); err != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, err
	}
	if count != 1 || !validUUID(operationID) {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, ErrInvalidStoredState
	}
	upgrade, err := readGatewayProfileUpgradeClaim(ctx, query, operationID)
	if err != nil || validateGatewayProfileUpgradeClaimHistory(ctx, query, upgrade) != nil {
		return GatewayProfileRevision{}, GatewayCurrentAuthorityRef{}, invalidRebindStoredState(err)
	}
	return profile, GatewayCurrentAuthorityRef{
		Kind: GatewayRebindSourceGatewayUpgrade, OperationID: operationID,
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
		ProfileSpecDigest: profile.SpecDigest,
	}, nil
}

type gatewayBindingTransferRecord struct {
	Transfer                         GatewayRebindAllocationTransfer
	PredecessorProfileRevisionID     string
	PredecessorProfileRevisionNumber int64
	PredecessorProfileSpecDigest     string
}

func readGatewayBindingTransfers(ctx context.Context, query gatewayBindingQuerier,
	ref GatewayBindingRef,
) ([]gatewayBindingTransferRecord, error) {
	rows, err := query.QueryContext(ctx, `SELECT
		t.operation_id,t.ordinal,t.app_id,t.allocation_id,t.grant_attempt_id,
		t.source_binding_digest,t.roster_entry_digest,t.source_profile_revision_id,
		t.source_profile_revision_number,t.source_profile_spec_digest,t.predecessor_transfer_digest,
		t.successor_profile_revision_id,t.successor_profile_revision_number,
		t.successor_profile_spec_digest,t.terminal_receipt_digest,t.transfer_digest,
		c.state,c.roster_format_version,c.predecessor_profile_revision_id,
		c.predecessor_profile_revision_number,c.predecessor_profile_spec_digest,
		c.successor_profile_revision_id,c.successor_profile_revision_number,c.successor_profile_spec_digest,
		r.ordinal,r.app_id,r.grant_attempt_id,r.entry_digest,r.source_profile_revision_id,
		r.source_profile_revision_number,r.source_profile_spec_digest,r.predecessor_transfer_digest,
		cmd.terminal_receipt_digest
		FROM lan_gateway_rebind_allocation_transfers t
		JOIN lan_gateway_rebind_claims c ON c.operation_id=t.operation_id
		JOIN lan_gateway_rebind_roster_entries r ON r.operation_id=t.operation_id
			 AND r.allocation_id=t.allocation_id
		JOIN lan_gateway_rebind_transition_commands cmd ON cmd.operation_id=c.operation_id
			 AND cmd.next_state='database_committed'
		WHERE t.app_id=? AND t.allocation_id=? AND t.grant_attempt_id=?
		ORDER BY t.successor_profile_revision_number,t.operation_id,t.ordinal`,
		ref.AppID, ref.AllocationID, ref.GrantAttemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transfers []gatewayBindingTransferRecord
	for rows.Next() {
		var record gatewayBindingTransferRecord
		transfer := &record.Transfer
		var transferPredecessor, rosterSourceID, rosterSourceDigest, rosterPredecessor sql.NullString
		var rosterSourceNumber sql.NullInt64
		var claimState GatewayRebindState
		var rosterVersion int
		var claimSourceID, claimSourceDigest, claimSuccessorID, claimSuccessorDigest string
		var claimSourceNumber, claimSuccessorNumber, rosterOrdinal int64
		var rosterAppID, rosterGrantID, rosterDigest, commandReceipt string
		transfer.Version = GatewayRebindTransferVersionV1
		if err := rows.Scan(&transfer.OperationID, &transfer.Ordinal, &transfer.AppID,
			&transfer.AllocationID, &transfer.GrantAttemptID, &transfer.SourceBindingDigest,
			&transfer.RosterEntryDigest, &transfer.SourceProfileRevisionID,
			&transfer.SourceProfileRevisionNumber, &transfer.SourceProfileSpecDigest,
			&transferPredecessor, &transfer.SuccessorProfileRevisionID,
			&transfer.SuccessorProfileRevisionNumber, &transfer.SuccessorProfileSpecDigest,
			&transfer.TerminalReceiptDigest, &transfer.TransferDigest, &claimState, &rosterVersion,
			&claimSourceID, &claimSourceNumber, &claimSourceDigest, &claimSuccessorID,
			&claimSuccessorNumber, &claimSuccessorDigest, &rosterOrdinal, &rosterAppID,
			&rosterGrantID, &rosterDigest, &rosterSourceID, &rosterSourceNumber,
			&rosterSourceDigest, &rosterPredecessor, &commandReceipt); err != nil {
			return nil, err
		}
		if transferPredecessor.Valid {
			transfer.PredecessorTransferDigest = &transferPredecessor.String
		}
		record.PredecessorProfileRevisionID = claimSourceID
		record.PredecessorProfileRevisionNumber = claimSourceNumber
		record.PredecessorProfileSpecDigest = claimSourceDigest
		computed, digestErr := GatewayRebindAllocationTransferDigest(*transfer)
		if digestErr != nil || computed != transfer.TransferDigest ||
			(claimState != GatewayRebindDatabaseCommitted && claimState != GatewayRebindUnresolved &&
				claimState != GatewayRebindCommitted) ||
			claimSuccessorID != transfer.SuccessorProfileRevisionID ||
			claimSuccessorNumber != transfer.SuccessorProfileRevisionNumber ||
			claimSuccessorDigest != transfer.SuccessorProfileSpecDigest ||
			rosterOrdinal != transfer.Ordinal || rosterAppID != transfer.AppID ||
			rosterGrantID != transfer.GrantAttemptID || rosterDigest != transfer.RosterEntryDigest ||
			commandReceipt != transfer.TerminalReceiptDigest {
			return nil, ErrInvalidStoredState
		}
		switch rosterVersion {
		case 1:
			if transfer.SourceProfileRevisionID != claimSourceID ||
				transfer.SourceProfileRevisionNumber != claimSourceNumber ||
				transfer.SourceProfileSpecDigest != claimSourceDigest || transfer.PredecessorTransferDigest != nil {
				return nil, ErrInvalidStoredState
			}
		case GatewayRebindRosterVersionV2:
			if !rosterSourceID.Valid || !rosterSourceNumber.Valid || !rosterSourceDigest.Valid ||
				transfer.SourceProfileRevisionID != rosterSourceID.String ||
				transfer.SourceProfileRevisionNumber != rosterSourceNumber.Int64 ||
				transfer.SourceProfileSpecDigest != rosterSourceDigest.String ||
				!optionalNullDigestEqual(transfer.PredecessorTransferDigest, rosterPredecessor) {
				return nil, ErrInvalidStoredState
			}
		default:
			return nil, ErrInvalidStoredState
		}
		transfers = append(transfers, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(transfers, func(i, j int) bool {
		return transfers[i].Transfer.SuccessorProfileRevisionNumber < transfers[j].Transfer.SuccessorProfileRevisionNumber
	})
	return transfers, nil
}

func optionalDigestEqual(left, right *string) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	return left == nil || *left == *right
}

func optionalNullDigestEqual(value *string, stored sql.NullString) bool {
	if value == nil {
		return !stored.Valid
	}
	return stored.Valid && *value == stored.String
}
