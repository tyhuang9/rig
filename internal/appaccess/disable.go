package appaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ApproveAppAccessDisable records an immutable administrator intent for the
// exact access head and allocation owner. It freezes allocation transitions,
// but deliberately does not release the port or claim that a route was
// removed.
func (r *Repository) ApproveAppAccessDisable(ctx context.Context, input ApproveAppAccessDisableInput) (AppAccessDisableIntent, bool, error) {
	if r == nil || r.db == nil || !validUUID(input.OperationID) || input.ExpectedRevisionNumber <= 0 ||
		!validUUID(input.Owner.AllocationID) || !validUUID(input.Owner.AppID) ||
		!validUUID(input.Owner.OperationID) || !validUUID(input.Owner.AccessRevisionID) {
		return AppAccessDisableIntent{}, false, ErrInvalidInput
	}
	requestDigest, err := disableApprovalRequestDigest(input)
	if err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	defer tx.Rollback()
	if r.afterDisableIntentLock != nil {
		r.afterDisableIntentLock()
	}
	if existing, lookupErr := readAppAccessDisableIntent(ctx, tx, input.OperationID); lookupErr == nil {
		if existing.RequestDigest != requestDigest {
			return AppAccessDisableIntent{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessDisableIntent{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return AppAccessDisableIntent{}, false, lookupErr
	}

	var revisionID sql.NullString
	var revisionNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number
		FROM lan_app_access_heads h
		JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL
		WHERE h.app_id=?`, input.Owner.AppID).Scan(&revisionID, &revisionNumber); errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableIntent{}, false, ErrNotFound
	} else if err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	if !revisionID.Valid || revisionNumber != input.ExpectedRevisionNumber || revisionID.String != input.Owner.AccessRevisionID {
		return AppAccessDisableIntent{}, false, ErrConflict
	}
	revision, _, err := readAccessRevision(ctx, tx, input.Owner.AppID, revisionID.String, revisionNumber)
	if err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	if revision.OperationID != input.Owner.OperationID || revision.Allocation.ID != input.Owner.AllocationID || revision.Allocation.ReleasedAt != nil {
		return AppAccessDisableIntent{}, false, ErrConflict
	}
	spec := AppAccessDisableSpecFor(revision)
	specDigest, err := AppAccessDisableSpecDigest(spec)
	if err != nil || !validApproval(input.Approval, ActionDisableAppAccess, specDigest) {
		return AppAccessDisableIntent{}, false, ErrApprovalRequired
	}
	if ok, err := administratorExists(ctx, tx, input.Approval.ActorID); err != nil {
		return AppAccessDisableIntent{}, false, err
	} else if !ok {
		return AppAccessDisableIntent{}, false, ErrApprovalRequired
	}
	var allocationClaimed int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lan_app_access_disable_intents WHERE allocation_id=?)`, spec.AllocationID).Scan(&allocationClaimed); err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	if allocationClaimed != 0 {
		return AppAccessDisableIntent{}, false, ErrConflict
	}

	now := r.now().UTC()
	value := AppAccessDisableIntent{
		OperationID:   input.OperationID,
		RequestDigest: requestDigest,
		Spec:          spec,
		SpecDigest:    specDigest,
		ApprovedBy:    input.Approval.ActorID,
		ApprovedAt:    now,
	}
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_app_access_disable_intents(
		operation_id,app_id,request_digest,approval_action,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.OperationID, spec.AppID, requestDigest, ActionDisableAppAccess,
		spec.AllocationID, spec.OwnerOperationID, spec.AccessRevisionID,
		spec.AccessRevisionNumber, spec.Port, spec.GatewayProfileRevisionID,
		spec.GatewayProfileRevisionNumber, specDigest, value.ApprovedBy, stamp); err != nil {
		return AppAccessDisableIntent{}, false, classifyImmediateTransactionError(err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"operationId": value.OperationID, "allocationId": spec.AllocationID,
		"ownerOperationId": spec.OwnerOperationID, "accessRevisionId": spec.AccessRevisionID,
		"accessRevisionNumber": spec.AccessRevisionNumber, "port": spec.Port,
		"gatewayProfileRevisionId":     spec.GatewayProfileRevisionID,
		"gatewayProfileRevisionNumber": spec.GatewayProfileRevisionNumber,
		"specDigest":                   specDigest,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at) VALUES(?,?,?,?,?,?)`, value.ApprovedBy, string(ActionDisableAppAccess), "lan_app_access_disable", value.OperationID, string(metadata), stamp); err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	return value, true, nil
}

func (r *Repository) AppAccessDisableIntent(ctx context.Context, operationID string) (AppAccessDisableIntent, error) {
	if r == nil || r.db == nil || !validUUID(operationID) {
		return AppAccessDisableIntent{}, ErrInvalidInput
	}
	value, err := readAppAccessDisableIntent(ctx, r.db, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableIntent{}, ErrNotFound
	}
	return value, err
}

func readAppAccessDisableIntent(ctx context.Context, query rowQuerier, operationID string) (AppAccessDisableIntent, error) {
	var value AppAccessDisableIntent
	var action, approvedAt string
	value.OperationID = operationID
	err := query.QueryRowContext(ctx, `SELECT app_id,request_digest,approval_action,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,allocated_port,
		gateway_profile_revision_id,gateway_profile_revision_number,spec_digest,approved_by,approved_at
		FROM lan_app_access_disable_intents WHERE operation_id=?`, operationID).Scan(
		&value.Spec.AppID, &value.RequestDigest, &action, &value.Spec.AllocationID,
		&value.Spec.OwnerOperationID, &value.Spec.AccessRevisionID, &value.Spec.AccessRevisionNumber,
		&value.Spec.Port, &value.Spec.GatewayProfileRevisionID, &value.Spec.GatewayProfileRevisionNumber,
		&value.SpecDigest, &value.ApprovedBy, &approvedAt)
	if err != nil {
		return AppAccessDisableIntent{}, err
	}
	parsedApproval, parseErr := parseTime(approvedAt)
	computed, digestErr := AppAccessDisableSpecDigest(value.Spec)
	if parseErr != nil || digestErr != nil || action != string(ActionDisableAppAccess) ||
		computed != value.SpecDigest || !validUUID(value.OperationID) || !validDigest(value.RequestDigest) ||
		!validUUID(value.ApprovedBy) {
		return AppAccessDisableIntent{}, ErrInvalidStoredState
	}
	value.ApprovedAt = parsedApproval
	return value, nil
}

func disableApprovalRequestDigest(input ApproveAppAccessDisableInput) (string, error) {
	return digestJSON(struct {
		Expected int64           `json:"expectedRevisionNumber"`
		Owner    AllocationOwner `json:"owner"`
		Approval Approval        `json:"approval"`
	}{input.ExpectedRevisionNumber, input.Owner, input.Approval})
}
