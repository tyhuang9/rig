package appaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ClaimGatewayProfileUpgrade durably binds one generated-ingress upgrade
// operation and administrator approval to the exact current gateway profile.
// An exact replay returns the existing claim. A competing non-rolled-back
// claim fails closed.
func (r *Repository) ClaimGatewayProfileUpgrade(ctx context.Context, input ClaimGatewayProfileUpgradeInput) (GatewayProfileUpgradeClaim, bool, error) {
	upgradeDigest, digestErr := GatewayProfileUpgradeSpecDigest(input.Spec)
	if r == nil || r.db == nil || digestErr != nil || !validUUID(input.OperationID) {
		return GatewayProfileUpgradeClaim{}, false, ErrInvalidInput
	}
	if !validApproval(input.Approval, ActionUpgradeGateway, upgradeDigest) {
		return GatewayProfileUpgradeClaim{}, false, ErrApprovalRequired
	}
	requestDigest, err := gatewayUpgradeClaimRequestDigest(input)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	defer tx.Rollback()
	if r.afterGatewayClaimLock != nil {
		r.afterGatewayClaimLock()
	}

	if existing, lookupErr := readGatewayProfileUpgradeClaim(ctx, tx, input.OperationID); lookupErr == nil {
		if existing.RequestDigest != requestDigest {
			return GatewayProfileUpgradeClaim{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return GatewayProfileUpgradeClaim{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return GatewayProfileUpgradeClaim{}, false, lookupErr
	}

	if ok, err := administratorExists(ctx, tx, input.Approval.ActorID); err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	} else if !ok {
		return GatewayProfileUpgradeClaim{}, false, ErrApprovalRequired
	}
	var currentSpecDigest string
	if err := tx.QueryRowContext(ctx, `SELECT r.spec_digest
		FROM lan_gateway_profile_heads h
		JOIN lan_gateway_profile_revisions r
		  ON r.id=h.revision_id AND r.revision_number=h.revision_number
		WHERE h.singleton=1 AND r.id=? AND r.revision_number=?`, input.Spec.ProfileRevisionID, input.Spec.ProfileRevisionNumber).Scan(&currentSpecDigest); errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileUpgradeClaim{}, false, ErrConflict
	} else if err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	if currentSpecDigest != input.Spec.ProfileSpecDigest {
		return GatewayProfileUpgradeClaim{}, false, ErrConflict
	}
	if blocking, err := blockingGatewayProfileUpgradeClaimExists(ctx, tx); err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	} else if blocking {
		return GatewayProfileUpgradeClaim{}, false, ErrConflict
	}

	now := r.now().UTC()
	value := GatewayProfileUpgradeClaim{
		OperationID:           input.OperationID,
		RequestDigest:         requestDigest,
		ProfileRevisionID:     input.Spec.ProfileRevisionID,
		ProfileRevisionNumber: input.Spec.ProfileRevisionNumber,
		ProfileSpecDigest:     input.Spec.ProfileSpecDigest,
		ApprovedBy:            input.Approval.ActorID,
		ApprovedAt:            now,
		State:                 GatewayProfileUpgradePrepared,
		StateSequence:         1,
		UpdatedAt:             now,
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_gateway_upgrade_claims(
		operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
		profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, value.OperationID, value.RequestDigest, ActionUpgradeGateway,
		value.ProfileRevisionID, value.ProfileRevisionNumber, value.ProfileSpecDigest,
		value.ApprovedBy, stamp, value.State, value.StateSequence, stamp); err != nil {
		return GatewayProfileUpgradeClaim{}, false, classifyImmediateTransactionError(err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"operationId": value.OperationID, "profileRevisionId": value.ProfileRevisionID,
		"profileRevisionNumber": value.ProfileRevisionNumber, "profileSpecDigest": value.ProfileSpecDigest,
		"state": value.State, "stateSequence": value.StateSequence,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`,
		value.ApprovedBy, string(ActionUpgradeGateway), "lan_gateway_upgrade", value.OperationID, string(metadata), stamp); err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	return value, true, nil
}

// AdvanceGatewayProfileUpgradeClaim appends one legal state transition. Exact
// replay of the same transition is idempotent; stale or skipped transitions
// are rejected.
func (r *Repository) AdvanceGatewayProfileUpgradeClaim(ctx context.Context, owner GatewayProfileUpgradeClaimOwner, expected, target GatewayProfileUpgradeState) (GatewayProfileUpgradeClaim, bool, error) {
	if r == nil || r.db == nil || !validUUID(owner.OperationID) || !validUUID(owner.ProfileRevisionID) || owner.ProfileRevisionNumber <= 0 ||
		!validGatewayProfileUpgradeTransition(expected, target) {
		return GatewayProfileUpgradeClaim{}, false, ErrInvalidInput
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	defer tx.Rollback()
	value, err := readGatewayProfileUpgradeClaim(ctx, tx, owner.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileUpgradeClaim{}, false, ErrNotFound
	}
	if err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	if value.ProfileRevisionID != owner.ProfileRevisionID || value.ProfileRevisionNumber != owner.ProfileRevisionNumber {
		return GatewayProfileUpgradeClaim{}, false, ErrConflict
	}
	if value.State == target {
		var predecessor GatewayProfileUpgradeState
		if value.StateSequence <= 1 || tx.QueryRowContext(ctx, `SELECT state FROM lan_gateway_upgrade_claim_events
			WHERE operation_id=? AND sequence=?`, value.OperationID, value.StateSequence-1).Scan(&predecessor) != nil || predecessor != expected {
			return GatewayProfileUpgradeClaim{}, false, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return GatewayProfileUpgradeClaim{}, false, err
		}
		return value, false, nil
	}
	if value.State != expected {
		return GatewayProfileUpgradeClaim{}, false, ErrConflict
	}
	now := r.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE lan_gateway_upgrade_claims
		SET state=?,state_sequence=state_sequence+1,updated_at=?
		WHERE operation_id=? AND profile_revision_id=? AND profile_revision_number=? AND state=? AND state_sequence=?`,
		target, formatTime(now), owner.OperationID, owner.ProfileRevisionID, owner.ProfileRevisionNumber, expected, value.StateSequence)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, false, classifyImmediateTransactionError(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return GatewayProfileUpgradeClaim{}, false, ErrConflict
	}
	previous := value.State
	value.State = target
	value.StateSequence++
	value.UpdatedAt = now
	metadata, _ := json.Marshal(map[string]any{
		"operationId": value.OperationID, "profileRevisionId": value.ProfileRevisionID,
		"profileRevisionNumber": value.ProfileRevisionNumber, "previousState": previous,
		"state": value.State, "stateSequence": value.StateSequence,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`,
		value.ApprovedBy, "lan_gateway_upgrade.transition", "lan_gateway_upgrade", value.OperationID, string(metadata), formatTime(now)); err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return GatewayProfileUpgradeClaim{}, false, err
	}
	return value, true, nil
}

func (r *Repository) GatewayProfileUpgradeClaim(ctx context.Context, operationID string) (GatewayProfileUpgradeClaim, error) {
	if r == nil || r.db == nil || !validUUID(operationID) {
		return GatewayProfileUpgradeClaim{}, ErrInvalidInput
	}
	value, err := readGatewayProfileUpgradeClaim(ctx, r.db, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileUpgradeClaim{}, ErrNotFound
	}
	return value, err
}

// CurrentGatewayProfileUpgradeClaim returns the one claim that still pins the
// gateway profile. A committed claim remains current until a future explicit
// profile migration or disable flow is implemented.
func (r *Repository) CurrentGatewayProfileUpgradeClaim(ctx context.Context) (GatewayProfileUpgradeClaim, error) {
	if r == nil || r.db == nil {
		return GatewayProfileUpgradeClaim{}, ErrInvalidInput
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, err
	}
	defer tx.Rollback()
	var operationID string
	err = tx.QueryRowContext(ctx, `SELECT operation_id FROM lan_gateway_upgrade_claims
		WHERE state IN ('prepared','serving','unresolved','committed')`).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayProfileUpgradeClaim{}, ErrNotFound
	}
	if err != nil {
		return GatewayProfileUpgradeClaim{}, err
	}
	if r.afterCurrentClaimLookup != nil {
		r.afterCurrentClaimLookup()
	}
	value, err := readGatewayProfileUpgradeClaim(ctx, tx, operationID)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return GatewayProfileUpgradeClaim{}, err
	}
	return value, nil
}

func readGatewayProfileUpgradeClaim(ctx context.Context, query rowQuerier, operationID string) (GatewayProfileUpgradeClaim, error) {
	var value GatewayProfileUpgradeClaim
	var action, approvedAt, updatedAt string
	value.OperationID = operationID
	err := query.QueryRowContext(ctx, `SELECT request_digest,approval_action,profile_revision_id,profile_revision_number,
		profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
		FROM lan_gateway_upgrade_claims WHERE operation_id=?`, operationID).Scan(
		&value.RequestDigest, &action, &value.ProfileRevisionID, &value.ProfileRevisionNumber,
		&value.ProfileSpecDigest, &value.ApprovedBy, &approvedAt, &value.State, &value.StateSequence, &updatedAt,
	)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, err
	}
	value.ApprovedAt, err = parseTime(approvedAt)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, ErrInvalidStoredState
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return GatewayProfileUpgradeClaim{}, ErrInvalidStoredState
	}
	spec := GatewayProfileUpgradeSpec{
		ProfileRevisionID: value.ProfileRevisionID, ProfileRevisionNumber: value.ProfileRevisionNumber,
		ProfileSpecDigest: value.ProfileSpecDigest,
	}
	upgradeDigest, digestErr := GatewayProfileUpgradeSpecDigest(spec)
	wantRequestDigest, requestErr := gatewayUpgradeClaimRequestDigest(ClaimGatewayProfileUpgradeInput{
		OperationID: value.OperationID, Spec: spec,
		Approval: Approval{Action: ActionUpgradeGateway, SpecDigest: upgradeDigest, ActorID: value.ApprovedBy},
	})
	var eventState GatewayProfileUpgradeState
	var eventCreatedAt string
	eventErr := query.QueryRowContext(ctx, `SELECT state,created_at FROM lan_gateway_upgrade_claim_events
		WHERE operation_id=? AND sequence=?`, operationID, value.StateSequence).Scan(&eventState, &eventCreatedAt)
	if digestErr != nil || requestErr != nil || eventErr != nil || action != string(ActionUpgradeGateway) ||
		wantRequestDigest != value.RequestDigest || !validGatewayProfileUpgradeState(value.State) || value.StateSequence <= 0 ||
		eventState != value.State || eventCreatedAt != updatedAt || !validUUID(value.OperationID) || !validUUID(value.ApprovedBy) {
		return GatewayProfileUpgradeClaim{}, ErrInvalidStoredState
	}
	return value, nil
}

func blockingGatewayProfileUpgradeClaimExists(ctx context.Context, query rowQuerier) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lan_gateway_upgrade_claims
		WHERE state IN ('prepared','serving','unresolved','committed'))`).Scan(&exists)
	return exists == 1, err
}

func gatewayUpgradeClaimRequestDigest(input ClaimGatewayProfileUpgradeInput) (string, error) {
	return digestJSON(struct {
		Spec     GatewayProfileUpgradeSpec `json:"spec"`
		Approval Approval                  `json:"approval"`
	}{input.Spec, input.Approval})
}
