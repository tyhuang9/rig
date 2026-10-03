package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

// GatewayRebindStartupClaim is read-only recovery input. Migration 033 never
// creates one through Repository and never exposes a state-changing method.
type GatewayRebindStartupClaim struct {
	Claim              GatewayRebindClaim
	PredecessorProfile GatewayProfileRevision
	PredecessorUpgrade GatewayProfileUpgradeClaim
	Roster             []GatewayRebindRosterEntry
	// GrantBindings is in the same order as Roster; each element is validated
	// with its corresponding roster entry in the same SQLite read transaction.
	GrantBindings []GatewayRebindStartupGrantBinding
}

// GatewayRebindStartupGrantBinding projects the controller inputs retained by
// the committed grant for the corresponding roster entry. The projection is
// read and validated from the same SQLite snapshot as the dormant rebind claim.
type GatewayRebindStartupGrantBinding struct {
	AppID              string
	AttemptID          string
	RequestDigest      string
	ApprovedBy         string
	GatewayOperationID string
}

type GatewayRebindStartupSnapshot struct {
	CurrentProfile *GatewayProfileRevision
	Claims         []GatewayRebindStartupClaim
}

// GatewayRebindStartupSnapshot validates the entire dormant lineage from one
// SQLite read snapshot. A malformed, incomplete, or stale prepared claim fails
// closed as ErrInvalidStoredState; it is never treated as authorization.
func (r *Repository) GatewayRebindStartupSnapshot(ctx context.Context) (GatewayRebindStartupSnapshot, error) {
	if r == nil || r.db == nil {
		return GatewayRebindStartupSnapshot{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GatewayRebindStartupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := r.readGatewayRebindStartupSnapshot(ctx, tx)
	if err != nil {
		return GatewayRebindStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayRebindStartupSnapshot{}, err
	}
	return snapshot, nil
}

func (r *Repository) readGatewayRebindStartupSnapshot(ctx context.Context, tx *sql.Tx) (GatewayRebindStartupSnapshot, error) {
	current, err := readGatewayUpgradeStartupHead(ctx, tx)
	if err != nil {
		return GatewayRebindStartupSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM lan_gateway_rebind_claims ORDER BY created_at,operation_id`)
	if err != nil {
		return GatewayRebindStartupSnapshot{}, err
	}
	var operationIDs []string
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			_ = rows.Close()
			return GatewayRebindStartupSnapshot{}, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return GatewayRebindStartupSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return GatewayRebindStartupSnapshot{}, err
	}
	if r.afterRebindStartupClaimsRead != nil {
		r.afterRebindStartupClaimsRead()
	}
	result := GatewayRebindStartupSnapshot{
		CurrentProfile: current,
		Claims:         make([]GatewayRebindStartupClaim, 0, len(operationIDs)),
	}
	for _, operationID := range operationIDs {
		claim, err := readGatewayRebindStartupClaim(ctx, tx, operationID, current)
		if err != nil {
			return GatewayRebindStartupSnapshot{}, err
		}
		result.Claims = append(result.Claims, claim)
	}
	return result, nil
}

func readGatewayRebindStartupClaim(ctx context.Context, tx *sql.Tx, operationID string, current *GatewayProfileRevision) (GatewayRebindStartupClaim, error) {
	claim, err := readGatewayRebindClaim(ctx, tx, operationID)
	if err != nil {
		return GatewayRebindStartupClaim{}, err
	}
	predecessor, profileRequestDigest, err := readGatewayRevision(ctx, tx,
		claim.Spec.PredecessorProfileRevisionID, claim.Spec.PredecessorProfileRevisionNumber)
	if err != nil {
		return GatewayRebindStartupClaim{}, invalidRebindStoredState(err)
	}
	if err := validateGatewayUpgradeStartupProfile(ctx, tx, predecessor, profileRequestDigest); err != nil {
		return GatewayRebindStartupClaim{}, invalidRebindStoredState(err)
	}
	upgrade, err := readGatewayProfileUpgradeClaim(ctx, tx, claim.Spec.PredecessorUpgradeOperationID)
	if err != nil {
		return GatewayRebindStartupClaim{}, invalidRebindStoredState(err)
	}
	if err := validateGatewayProfileUpgradeClaimHistory(ctx, tx, upgrade); err != nil {
		return GatewayRebindStartupClaim{}, invalidRebindStoredState(err)
	}
	if current == nil || predecessor.ID != current.ID || predecessor.RevisionNumber != current.RevisionNumber ||
		predecessor.SpecDigest != current.SpecDigest ||
		predecessor.SpecDigest != claim.Spec.PredecessorProfileSpecDigest ||
		upgrade.State != GatewayProfileUpgradeCommitted ||
		upgrade.ProfileRevisionID != predecessor.ID || upgrade.ProfileRevisionNumber != predecessor.RevisionNumber ||
		upgrade.ProfileSpecDigest != predecessor.SpecDigest {
		return GatewayRebindStartupClaim{}, ErrInvalidStoredState
	}
	for _, actorID := range []string{claim.RebindApproval.ActorID, claim.ConfigureApproval.ActorID} {
		administrator, err := administratorExists(ctx, tx, actorID)
		if err != nil {
			return GatewayRebindStartupClaim{}, err
		}
		if !administrator {
			return GatewayRebindStartupClaim{}, ErrInvalidStoredState
		}
	}
	var successorExists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_gateway_profile_revisions
		WHERE id=? OR revision_number=? OR operation_id=?
	)`, claim.Spec.SuccessorProfileRevisionID, claim.Spec.SuccessorProfileRevisionNumber,
		claim.Spec.SuccessorProfileOperationID).Scan(&successorExists); err != nil {
		return GatewayRebindStartupClaim{}, err
	}
	if successorExists != 0 {
		return GatewayRebindStartupClaim{}, ErrInvalidStoredState
	}
	roster, err := readGatewayRebindRoster(ctx, tx, operationID)
	if err != nil {
		return GatewayRebindStartupClaim{}, err
	}
	if int64(len(roster)) != claim.Spec.RosterCount {
		return GatewayRebindStartupClaim{}, ErrInvalidStoredState
	}
	rosterDigest, err := GatewayRebindRosterDigest(roster)
	if err != nil || rosterDigest != claim.Spec.RosterDigest {
		return GatewayRebindStartupClaim{}, ErrInvalidStoredState
	}
	grantBindings := make([]GatewayRebindStartupGrantBinding, 0, len(roster))
	for _, entry := range roster {
		binding, err := validateGatewayRebindRosterEntry(ctx, tx, claim, entry)
		if err != nil {
			return GatewayRebindStartupClaim{}, err
		}
		grantBindings = append(grantBindings, binding)
	}
	var liveCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_port_allocations
		WHERE released_at IS NULL AND disabled_at IS NULL`).Scan(&liveCount); err != nil {
		return GatewayRebindStartupClaim{}, err
	}
	if liveCount != claim.Spec.RosterCount {
		return GatewayRebindStartupClaim{}, ErrInvalidStoredState
	}
	return GatewayRebindStartupClaim{
		Claim: claim, PredecessorProfile: predecessor, PredecessorUpgrade: upgrade,
		Roster: roster, GrantBindings: grantBindings,
	}, nil
}

func readGatewayRebindClaim(ctx context.Context, query rowQuerier, operationID string) (GatewayRebindClaim, error) {
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
		FROM lan_gateway_rebind_claims WHERE operation_id=?`, operationID).Scan(
		&claim.RequestDigest, &rebindAction, &claim.RebindApproval.SpecDigest,
		&claim.RebindApproval.ActorID, &rebindApprovedAt,
		&claim.Spec.PredecessorProfileRevisionID, &claim.Spec.PredecessorProfileRevisionNumber,
		&claim.Spec.PredecessorProfileSpecDigest, &claim.Spec.PredecessorUpgradeOperationID,
		&claim.Spec.PredecessorProtectedIdentityDigest,
		&claim.Spec.SuccessorProfileRevisionID, &claim.Spec.SuccessorProfileRevisionNumber,
		&claim.Spec.SuccessorProfileOperationID, &claim.SuccessorProfileRequestDigest,
		&claim.Spec.SuccessorProfile.SelectedIPv4, &claim.Spec.SuccessorProfile.InterfaceID,
		&claim.Spec.SuccessorProfile.PortStart, &claim.Spec.SuccessorProfile.PortEnd,
		&claim.ConfigureApproval.SpecDigest, &configureAction, &claim.ConfigureApproval.ActorID,
		&configureApprovedAt, &claim.Spec.RosterDigest, &claim.Spec.RosterCount,
		&claim.State, &claim.StateSequence, &createdAt, &updatedAt,
	)
	if err != nil {
		return GatewayRebindClaim{}, err
	}
	claim.RebindApproval.Action = ApprovalAction(rebindAction)
	claim.ConfigureApproval.Action = ApprovalAction(configureAction)
	claim.RebindApprovedAt, err = parseTime(rebindApprovedAt)
	if err != nil {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	claim.ConfigureApprovedAt, err = parseTime(configureApprovedAt)
	if err != nil {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	claim.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	claim.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	if err := validateGatewayRebindClaimDigests(claim); err != nil {
		return GatewayRebindClaim{}, err
	}
	var eventState GatewayRebindState
	var eventCreatedAt string
	var eventCount int64
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM lan_gateway_rebind_claim_events
		WHERE operation_id=?`, operationID).Scan(&eventCount); err != nil {
		return GatewayRebindClaim{}, err
	}
	if eventCount != 1 {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	eventErr := query.QueryRowContext(ctx, `SELECT state,created_at
		FROM lan_gateway_rebind_claim_events WHERE operation_id=? AND sequence=1`, operationID).
		Scan(&eventState, &eventCreatedAt)
	if eventErr != nil {
		return GatewayRebindClaim{}, invalidRebindStoredState(eventErr)
	}
	if eventState != GatewayRebindPrepared || eventCreatedAt != updatedAt {
		return GatewayRebindClaim{}, ErrInvalidStoredState
	}
	return claim, nil
}

func validateGatewayRebindClaimDigests(claim GatewayRebindClaim) error {
	specDigest, err := GatewayRebindSpecDigest(claim.Spec)
	if err != nil || claim.State != GatewayRebindPrepared || claim.StateSequence != 1 ||
		!claim.CreatedAt.Equal(claim.UpdatedAt) || claim.RebindApprovedAt.After(claim.CreatedAt) ||
		claim.ConfigureApprovedAt.After(claim.CreatedAt) ||
		!validApproval(claim.RebindApproval, ActionRebindGateway, specDigest) {
		return ErrInvalidStoredState
	}
	canonical, err := canonicalGatewaySpec(claim.Spec.SuccessorProfile)
	if err != nil {
		return ErrInvalidStoredState
	}
	successorSpecDigest, err := GatewayProfileSpecDigest(canonical)
	if err != nil || !validApproval(claim.ConfigureApproval, ActionConfigureGateway, successorSpecDigest) {
		return ErrInvalidStoredState
	}
	successorRequestDigest, err := gatewayRequestDigest(ConfigureGatewayInput{
		OperationID: claim.Spec.SuccessorProfileOperationID, ExpectedRevisionNumber: claim.Spec.PredecessorProfileRevisionNumber,
		Spec: canonical, Approval: claim.ConfigureApproval,
	}, canonical)
	if err != nil || successorRequestDigest != claim.SuccessorProfileRequestDigest {
		return ErrInvalidStoredState
	}
	requestDigest, err := gatewayRebindClaimRequestDigest(claim.Spec, claim.RebindApproval, claim.ConfigureApproval)
	if err != nil || requestDigest != claim.RequestDigest {
		return ErrInvalidStoredState
	}
	return nil
}

func gatewayRebindClaimRequestDigest(spec GatewayRebindSpec, rebind, configure Approval) (string, error) {
	return digestJSON(struct {
		Spec      GatewayRebindSpec `json:"spec"`
		Rebind    Approval          `json:"rebindApproval"`
		Configure Approval          `json:"configureApproval"`
	}{Spec: spec, Rebind: rebind, Configure: configure})
}

func readGatewayRebindRoster(ctx context.Context, tx *sql.Tx, operationID string) ([]GatewayRebindRosterEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,app_id,allocation_id,allocated_port,
		allocation_owner_operation_id,allocation_state,access_revision_id,access_revision_number,
		access_spec_digest,grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		serving_deployment_id,serving_release_id,serving_slot,route_generation,entry_digest
		FROM lan_gateway_rebind_roster_entries WHERE operation_id=? ORDER BY ordinal`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []GatewayRebindRosterEntry
	for rows.Next() {
		value := GatewayRebindRosterEntry{OperationID: operationID}
		if err := rows.Scan(&value.Ordinal, &value.AppID, &value.AllocationID, &value.Port,
			&value.AllocationOwnerOperationID, &value.AllocationState, &value.AccessRevisionID,
			&value.AccessRevisionNumber, &value.AccessSpecDigest, &value.GrantAttemptID,
			&value.GrantStateSequence, &value.GrantProtectedStateDigest, &value.ServingDeploymentID,
			&value.ServingReleaseID, &value.ServingSlot, &value.RouteGeneration, &value.EntryDigest); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func validateGatewayRebindRosterEntry(ctx context.Context, tx *sql.Tx, claim GatewayRebindClaim,
	entry GatewayRebindRosterEntry,
) (GatewayRebindStartupGrantBinding, error) {
	entryDigest, err := GatewayRebindRosterEntryDigest(entry)
	if err != nil || entryDigest != entry.EntryDigest || entry.Port < claim.Spec.SuccessorProfile.PortStart ||
		entry.Port > claim.Spec.SuccessorProfile.PortEnd {
		return GatewayRebindStartupGrantBinding{}, ErrInvalidStoredState
	}
	revision, requestDigest, err := readAccessRevision(ctx, tx, entry.AppID, entry.AccessRevisionID, entry.AccessRevisionNumber)
	if err != nil {
		return GatewayRebindStartupGrantBinding{}, invalidRebindStoredState(err)
	}
	if revision.OperationID != entry.AllocationOwnerOperationID ||
		revision.SpecDigest != entry.AccessSpecDigest || revision.Allocation.ID != entry.AllocationID ||
		revision.Allocation.Port != entry.Port || revision.Allocation.State != AllocationActive ||
		revision.Allocation.GatewayProfileRevisionID != claim.Spec.PredecessorProfileRevisionID ||
		revision.Allocation.GatewayProfileRevisionNumber != claim.Spec.PredecessorProfileRevisionNumber {
		return GatewayRebindStartupGrantBinding{}, ErrInvalidStoredState
	}
	if err := validateOperatorCurrentAccessRevision(revision, requestDigest); err != nil {
		return GatewayRebindStartupGrantBinding{}, err
	}
	grant, err := readAppAccessGrantClaim(ctx, tx, entry.GrantAttemptID)
	if err != nil {
		return GatewayRebindStartupGrantBinding{}, invalidRebindStoredState(err)
	}
	if grant.State != AppAccessGrantCommitted || grant.Proof == nil || grant.RetiredAt != nil ||
		grant.StateSequence != entry.GrantStateSequence ||
		grant.Proof.ProtectedStateDigest != entry.GrantProtectedStateDigest ||
		grant.Spec.AppID != entry.AppID || grant.Spec.AllocationID != entry.AllocationID ||
		grant.Spec.OwnerOperationID != entry.AllocationOwnerOperationID ||
		grant.Spec.AccessRevisionID != entry.AccessRevisionID ||
		grant.Spec.AccessRevisionNumber != entry.AccessRevisionNumber ||
		grant.Spec.AccessSpecDigest != entry.AccessSpecDigest || grant.Spec.Port != entry.Port ||
		grant.Spec.GatewayProfileRevisionID != claim.Spec.PredecessorProfileRevisionID ||
		grant.Spec.GatewayProfileRevisionNumber != claim.Spec.PredecessorProfileRevisionNumber ||
		grant.Spec.GatewayProfileSpecDigest != claim.Spec.PredecessorProfileSpecDigest ||
		grant.Proof.GatewayOperationID != claim.Spec.PredecessorUpgradeOperationID {
		return GatewayRebindStartupGrantBinding{}, ErrInvalidStoredState
	}
	var matches int
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1
		FROM lan_port_allocations a
		JOIN applications app ON app.id=a.app_id
		JOIN lan_app_access_heads ah ON ah.app_id=a.app_id
		JOIN lan_app_access_revisions ar
		  ON ar.app_id=ah.app_id AND ar.id=ah.revision_id AND ar.revision_number=ah.revision_number
		JOIN lan_app_access_grant_claims g ON g.attempt_id=? AND g.allocation_id=a.id
		JOIN generated_runtime_active_heads rh ON rh.app_id=a.app_id
		WHERE a.id=? AND a.app_id=? AND app.archived_at IS NULL AND a.port=? AND a.owner_operation_id=?
		  AND a.owner_revision_id=? AND a.state='active'
		  AND a.released_at IS NULL AND a.disabled_at IS NULL
		  AND a.gateway_profile_revision_id=? AND a.gateway_profile_revision_number=?
		  AND ar.id=? AND ar.revision_number=? AND ar.operation_id=?
		  AND ar.allocation_id=a.id AND ar.allocated_port=a.port AND ar.spec_digest=?
		  AND g.access_revision_id=ar.id AND g.access_revision_number=ar.revision_number
		  AND g.allocation_owner_operation_id=a.owner_operation_id
		  AND g.state='committed' AND g.state_sequence=? AND g.retired_at IS NULL
		  AND g.protected_state_digest=?
		  AND rh.deployment_id=? AND rh.release_id=? AND rh.slot=? AND rh.generation=?
		  AND NOT EXISTS (SELECT 1 FROM lan_app_access_disable_intents d WHERE d.allocation_id=a.id)
	)`, entry.GrantAttemptID, entry.AllocationID, entry.AppID, entry.Port,
		entry.AllocationOwnerOperationID, entry.AccessRevisionID,
		claim.Spec.PredecessorProfileRevisionID, claim.Spec.PredecessorProfileRevisionNumber,
		entry.AccessRevisionID, entry.AccessRevisionNumber, entry.AllocationOwnerOperationID,
		entry.AccessSpecDigest, entry.GrantStateSequence, entry.GrantProtectedStateDigest,
		entry.ServingDeploymentID, entry.ServingReleaseID, entry.ServingSlot, entry.RouteGeneration).Scan(&matches)
	if err != nil {
		return GatewayRebindStartupGrantBinding{}, err
	}
	if matches != 1 {
		return GatewayRebindStartupGrantBinding{}, ErrInvalidStoredState
	}
	return GatewayRebindStartupGrantBinding{
		AppID: grant.Spec.AppID, AttemptID: grant.AttemptID, RequestDigest: grant.RequestDigest,
		ApprovedBy: grant.Spec.ApprovedBy, GatewayOperationID: grant.Proof.GatewayOperationID,
	}, nil
}

func invalidRebindStoredState(err error) error {
	if err == nil || errors.Is(err, ErrInvalidStoredState) {
		return ErrInvalidStoredState
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidStoredState
	}
	return err
}
