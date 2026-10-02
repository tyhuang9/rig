package appaccess

import (
	"context"
	"database/sql"
	"errors"
)

// ApproveAppAccessDisable records an immutable administrator intent for the
// exact access head and allocation owner. It freezes allocation transitions,
// but deliberately does not release the port or claim that a route was
// removed.
func (r *Repository) ApproveAppAccessDisable(ctx context.Context, input ApproveAppAccessDisableInput) (AppAccessDisableIntent, bool, error) {
	claim, created, err := r.ClaimAppAccessDisable(ctx, input)
	if err != nil {
		return AppAccessDisableIntent{}, false, err
	}
	return appAccessDisableIntentForClaim(claim), created, nil
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
