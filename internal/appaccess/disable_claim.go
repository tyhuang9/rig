package appaccess

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// AppAccessDisableState is the retained state of one monotonic gateway
// withdrawal. A disable has no rollback state: once approved, LAN access can
// only progress toward a proved absence and release.
type AppAccessDisableState string

const (
	AppAccessDisablePrepared    AppAccessDisableState = "prepared"
	AppAccessDisableWithdrawing AppAccessDisableState = "withdrawing"
	AppAccessDisableUncertain   AppAccessDisableState = "uncertain"
	AppAccessDisableCommitted   AppAccessDisableState = "committed"
)

type AppAccessDisableClaimOwner struct {
	OperationID      string
	RequestDigest    string
	AppID            string
	AllocationID     string
	OwnerOperationID string
	AccessRevisionID string
	ActorID          string
}

type AppAccessDisableProof struct {
	GatewayOperationID   string
	ProtectedStateDigest string
	ObservedAt           time.Time
}

type AppAccessDisableClaim struct {
	OperationID          string
	RequestDigest        string
	Spec                 AppAccessDisableSpec
	SpecDigest           string
	ApprovedBy           string
	ApprovedAt           time.Time
	SourceGrantAttemptID string
	CreatedAt            time.Time
	State                AppAccessDisableState
	StateSequence        int64
	UpdatedAt            time.Time
	Proof                *AppAccessDisableProof
}

func AppAccessDisableClaimOwnerFor(claim AppAccessDisableClaim) AppAccessDisableClaimOwner {
	return AppAccessDisableClaimOwner{
		OperationID: claim.OperationID, RequestDigest: claim.RequestDigest,
		AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID,
		AccessRevisionID: claim.Spec.AccessRevisionID, ActorID: claim.ApprovedBy,
	}
}

// ClaimAppAccessDisable atomically records the immutable approval intent and a
// prepared execution claim. Exact operation replays return the retained claim.
func (r *Repository) ClaimAppAccessDisable(ctx context.Context, input ApproveAppAccessDisableInput) (AppAccessDisableClaim, bool, error) {
	if r == nil || r.db == nil || !validUUID(input.OperationID) || input.ExpectedRevisionNumber <= 0 ||
		!validUUID(input.Owner.AllocationID) || !validUUID(input.Owner.AppID) ||
		!validUUID(input.Owner.OperationID) || !validUUID(input.Owner.AccessRevisionID) {
		return AppAccessDisableClaim{}, false, ErrInvalidInput
	}
	requestDigest, err := disableApprovalRequestDigest(input)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	defer tx.Rollback()
	administrator, err := administratorExists(ctx, tx, input.Approval.ActorID)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if !administrator {
		return AppAccessDisableClaim{}, false, ErrApprovalRequired
	}
	if r.afterDisableIntentLock != nil {
		r.afterDisableIntentLock()
	}
	if existing, lookupErr := readAppAccessDisableClaim(ctx, tx, input.OperationID); lookupErr == nil {
		if existing.RequestDigest != requestDigest {
			return AppAccessDisableClaim{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessDisableClaim{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return AppAccessDisableClaim{}, false, lookupErr
	}
	if _, lookupErr := readAppAccessDisableIntent(ctx, tx, input.OperationID); lookupErr == nil {
		return AppAccessDisableClaim{}, false, ErrInvalidStoredState
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return AppAccessDisableClaim{}, false, lookupErr
	}

	var revisionID sql.NullString
	var revisionNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT h.revision_id,h.revision_number
		FROM lan_app_access_heads h
		JOIN applications a ON a.id=h.app_id AND a.archived_at IS NULL
		WHERE h.app_id=?`, input.Owner.AppID).Scan(&revisionID, &revisionNumber); errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableClaim{}, false, ErrNotFound
	} else if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if !revisionID.Valid || revisionNumber != input.ExpectedRevisionNumber || revisionID.String != input.Owner.AccessRevisionID {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	revision, _, err := readAccessRevision(ctx, tx, input.Owner.AppID, revisionID.String, revisionNumber)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if revision.OperationID != input.Owner.OperationID || revision.Allocation.ID != input.Owner.AllocationID || revision.Allocation.ReleasedAt != nil {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	spec := AppAccessDisableSpecFor(revision)
	specDigest, err := AppAccessDisableSpecDigest(spec)
	if err != nil || !validApproval(input.Approval, ActionDisableAppAccess, specDigest) {
		return AppAccessDisableClaim{}, false, ErrApprovalRequired
	}
	var allocationClaimed int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_disable_intents WHERE allocation_id=?
	)`, spec.AllocationID).Scan(&allocationClaimed); err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if allocationClaimed != 0 {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_grant_claims
		WHERE state IN ('prepared','applying','db_active','uncertain') AND retired_at IS NULL
	)`).Scan(&unresolved); err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if unresolved != 0 {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	if blocked, err := unresolvedAppAccessDisableClearExists(ctx, tx); err != nil {
		return AppAccessDisableClaim{}, false, err
	} else if blocked {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	var sourceGrant sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id
		FROM lan_app_access_grant_claims
		WHERE app_id=? AND allocation_id=? AND allocation_owner_operation_id=?
		  AND access_revision_id=? AND state='committed' AND retired_at IS NULL
		ORDER BY updated_at DESC,attempt_id DESC LIMIT 1`, spec.AppID, spec.AllocationID,
		spec.OwnerOperationID, spec.AccessRevisionID).Scan(&sourceGrant); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableClaim{}, false, err
	}

	now := r.now().UTC()
	value := AppAccessDisableClaim{
		OperationID: input.OperationID, RequestDigest: requestDigest,
		Spec: spec, SpecDigest: specDigest, ApprovedBy: input.Approval.ActorID,
		ApprovedAt: now, CreatedAt: now, State: AppAccessDisablePrepared,
		StateSequence: 1, UpdatedAt: now,
	}
	if sourceGrant.Valid {
		value.SourceGrantAttemptID = sourceGrant.String
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
		return AppAccessDisableClaim{}, false, classifyImmediateTransactionError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_app_access_disable_claims(
		operation_id,request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at,source_grant_attempt_id,created_at,
		state,state_sequence,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.OperationID, value.RequestDigest,
		ActionDisableAppAccess, spec.AppID, spec.AllocationID, spec.OwnerOperationID,
		spec.AccessRevisionID, spec.AccessRevisionNumber, spec.Port,
		spec.GatewayProfileRevisionID, spec.GatewayProfileRevisionNumber,
		value.SpecDigest, value.ApprovedBy, stamp, nullableText(value.SourceGrantAttemptID),
		stamp, value.State, value.StateSequence, stamp); err != nil {
		return AppAccessDisableClaim{}, false, classifyImmediateTransactionError(err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"operationId": value.OperationID, "allocationId": spec.AllocationID,
		"ownerOperationId": spec.OwnerOperationID, "accessRevisionId": spec.AccessRevisionID,
		"accessRevisionNumber": spec.AccessRevisionNumber, "port": spec.Port,
		"gatewayProfileRevisionId":     spec.GatewayProfileRevisionID,
		"gatewayProfileRevisionNumber": spec.GatewayProfileRevisionNumber,
		"sourceGrantAttemptId":         value.SourceGrantAttemptID,
		"specDigest":                   value.SpecDigest, "state": value.State,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,?,?,?,?,?)`, value.ApprovedBy, string(ActionDisableAppAccess),
		"lan_app_access_disable", value.OperationID, string(metadata), stamp); err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	return value, true, nil
}

func (r *Repository) AdvanceAppAccessDisableClaim(ctx context.Context, owner AppAccessDisableClaimOwner,
	expected, target AppAccessDisableState,
) (AppAccessDisableClaim, bool, error) {
	if r == nil || r.db == nil || !validAppAccessDisableClaimOwner(owner) ||
		!validAppAccessDisableTransition(expected, target) || target == AppAccessDisableCommitted {
		return AppAccessDisableClaim{}, false, ErrInvalidInput
	}
	return r.transitionAppAccessDisableClaim(ctx, owner, expected, target, nil)
}

func (r *Repository) ResolveAppAccessDisableClaim(ctx context.Context, owner AppAccessDisableClaimOwner,
	expected, target AppAccessDisableState, proof AppAccessDisableProof,
) (AppAccessDisableClaim, bool, error) {
	if r == nil || r.db == nil || !validAppAccessDisableClaimOwner(owner) ||
		target != AppAccessDisableCommitted || !validAppAccessDisableTransition(expected, target) ||
		!validUUID(proof.GatewayOperationID) || !validDigest(proof.ProtectedStateDigest) {
		return AppAccessDisableClaim{}, false, ErrInvalidInput
	}
	proof.ObservedAt = time.Time{}
	return r.transitionAppAccessDisableClaim(ctx, owner, expected, target, &proof)
}

func (r *Repository) transitionAppAccessDisableClaim(ctx context.Context, owner AppAccessDisableClaimOwner,
	expected, target AppAccessDisableState, proof *AppAccessDisableProof,
) (AppAccessDisableClaim, bool, error) {
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	defer tx.Rollback()
	administrator, err := administratorExists(ctx, tx, owner.ActorID)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if !administrator {
		return AppAccessDisableClaim{}, false, ErrApprovalRequired
	}
	value, err := readAppAccessDisableClaim(ctx, tx, owner.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableClaim{}, false, ErrNotFound
	}
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if !appAccessDisableOwnerMatches(value, owner) {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	if value.State == target {
		var predecessor AppAccessDisableState
		if value.StateSequence <= 1 || tx.QueryRowContext(ctx, `SELECT state
			FROM lan_app_access_disable_claim_events WHERE operation_id=? AND sequence=?`,
			value.OperationID, value.StateSequence-1).Scan(&predecessor) != nil || predecessor != expected {
			return AppAccessDisableClaim{}, false, ErrConflict
		}
		if proof != nil && (value.Proof == nil || value.Proof.GatewayOperationID != proof.GatewayOperationID ||
			value.Proof.ProtectedStateDigest != proof.ProtectedStateDigest) {
			return AppAccessDisableClaim{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessDisableClaim{}, false, err
		}
		return value, false, nil
	}
	if value.State != expected {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	if target == AppAccessDisableWithdrawing || target == AppAccessDisableUncertain {
		var blocking int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM lan_app_access_disable_claims
			WHERE operation_id<>? AND state IN ('withdrawing','uncertain')
			UNION ALL
			SELECT 1 FROM lan_app_access_grant_claims
			WHERE state IN ('applying','db_active','uncertain') AND retired_at IS NULL
		)`, value.OperationID).Scan(&blocking); err != nil {
			return AppAccessDisableClaim{}, false, err
		}
		if blocking != 0 {
			return AppAccessDisableClaim{}, false, ErrConflict
		}
	}
	now := r.now().UTC()
	var result sql.Result
	if proof == nil {
		result, err = tx.ExecContext(ctx, `UPDATE lan_app_access_disable_claims
			SET state=?,state_sequence=state_sequence+1,updated_at=?
			WHERE operation_id=? AND request_digest=? AND app_id=? AND allocation_id=?
			  AND allocation_owner_operation_id=? AND access_revision_id=?
			  AND state=? AND state_sequence=?`, target, formatTime(now), owner.OperationID,
			owner.RequestDigest, owner.AppID, owner.AllocationID, owner.OwnerOperationID,
			owner.AccessRevisionID, expected, value.StateSequence)
	} else {
		proof.ObservedAt = now
		result, err = tx.ExecContext(ctx, `UPDATE lan_app_access_disable_claims
			SET state=?,state_sequence=state_sequence+1,updated_at=?,gateway_operation_id=?,
				protected_state_digest=?,resolved_at=?
			WHERE operation_id=? AND request_digest=? AND app_id=? AND allocation_id=?
			  AND allocation_owner_operation_id=? AND access_revision_id=?
			  AND state=? AND state_sequence=?`, target, formatTime(now), proof.GatewayOperationID,
			proof.ProtectedStateDigest, formatTime(now), owner.OperationID, owner.RequestDigest,
			owner.AppID, owner.AllocationID, owner.OwnerOperationID, owner.AccessRevisionID,
			expected, value.StateSequence)
	}
	if err != nil {
		return AppAccessDisableClaim{}, false, classifyImmediateTransactionError(err)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		return AppAccessDisableClaim{}, false, ErrConflict
	}
	updated, err := readAppAccessDisableClaim(ctx, tx, value.OperationID)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	allocation, err := readAllocationByID(ctx, tx, value.Spec.AllocationID)
	if err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if target == AppAccessDisableCommitted {
		if allocation.ReleasedAt == nil || updated.Proof == nil ||
			!allocation.ReleasedAt.Equal(updated.Proof.ObservedAt) {
			return AppAccessDisableClaim{}, false, ErrInvalidStoredState
		}
	} else if allocation.ReleasedAt != nil {
		return AppAccessDisableClaim{}, false, ErrInvalidStoredState
	}
	metadata, _ := json.Marshal(map[string]any{
		"operationId": updated.OperationID, "previousState": value.State,
		"state": updated.State, "stateSequence": updated.StateSequence,
	})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(
		actor_id,action,resource_type,resource_id,metadata_json,created_at
	) VALUES(?,?,?,?,?,?)`, owner.ActorID, "lan_app_access_disable.transition",
		"lan_app_access_disable", updated.OperationID, string(metadata), formatTime(now)); err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessDisableClaim{}, false, err
	}
	return updated, true, nil
}

func (r *Repository) AppAccessDisableClaim(ctx context.Context, operationID string) (AppAccessDisableClaim, error) {
	if r == nil || r.db == nil || !validUUID(operationID) {
		return AppAccessDisableClaim{}, ErrInvalidInput
	}
	value, err := readAppAccessDisableClaim(ctx, r.db, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableClaim{}, ErrNotFound
	}
	return value, err
}

type appAccessDisableQuerier interface {
	rowQuerier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readAppAccessDisableClaim(ctx context.Context, query appAccessDisableQuerier, operationID string) (AppAccessDisableClaim, error) {
	var value AppAccessDisableClaim
	var action, approvedAt, createdAt, updatedAt string
	var sourceGrant, gatewayOperationID, protectedStateDigest, resolvedAt sql.NullString
	value.OperationID = operationID
	err := query.QueryRowContext(ctx, `SELECT request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,allocated_port,
		gateway_profile_revision_id,gateway_profile_revision_number,spec_digest,approved_by,
		approved_at,source_grant_attempt_id,created_at,state,state_sequence,updated_at,
		gateway_operation_id,protected_state_digest,resolved_at
		FROM lan_app_access_disable_claims WHERE operation_id=?`, operationID).Scan(
		&value.RequestDigest, &action, &value.Spec.AppID, &value.Spec.AllocationID,
		&value.Spec.OwnerOperationID, &value.Spec.AccessRevisionID, &value.Spec.AccessRevisionNumber,
		&value.Spec.Port, &value.Spec.GatewayProfileRevisionID, &value.Spec.GatewayProfileRevisionNumber,
		&value.SpecDigest, &value.ApprovedBy, &approvedAt, &sourceGrant, &createdAt,
		&value.State, &value.StateSequence, &updatedAt, &gatewayOperationID,
		&protectedStateDigest, &resolvedAt)
	if err != nil {
		return AppAccessDisableClaim{}, err
	}
	if sourceGrant.Valid {
		value.SourceGrantAttemptID = sourceGrant.String
	}
	value.ApprovedAt, err = parseTime(approvedAt)
	if err != nil {
		return AppAccessDisableClaim{}, ErrInvalidStoredState
	}
	value.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return AppAccessDisableClaim{}, ErrInvalidStoredState
	}
	value.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return AppAccessDisableClaim{}, ErrInvalidStoredState
	}
	if value.State == AppAccessDisableCommitted {
		if !gatewayOperationID.Valid || !protectedStateDigest.Valid || !resolvedAt.Valid {
			return AppAccessDisableClaim{}, ErrInvalidStoredState
		}
		observedAt, parseErr := parseTime(resolvedAt.String)
		if parseErr != nil {
			return AppAccessDisableClaim{}, ErrInvalidStoredState
		}
		value.Proof = &AppAccessDisableProof{GatewayOperationID: gatewayOperationID.String,
			ProtectedStateDigest: protectedStateDigest.String, ObservedAt: observedAt}
		if !validUUID(value.Proof.GatewayOperationID) || !validDigest(value.Proof.ProtectedStateDigest) ||
			!value.Proof.ObservedAt.Equal(value.UpdatedAt) {
			return AppAccessDisableClaim{}, ErrInvalidStoredState
		}
	} else if gatewayOperationID.Valid || protectedStateDigest.Valid || resolvedAt.Valid {
		return AppAccessDisableClaim{}, ErrInvalidStoredState
	}
	computed, digestErr := AppAccessDisableSpecDigest(value.Spec)
	if digestErr != nil || action != string(ActionDisableAppAccess) || computed != value.SpecDigest ||
		!validUUID(value.OperationID) || !validDigest(value.RequestDigest) ||
		!validUUID(value.ApprovedBy) || (value.SourceGrantAttemptID != "" && !validUUID(value.SourceGrantAttemptID)) ||
		!validAppAccessDisableState(value.State) || value.StateSequence <= 0 ||
		value.CreatedAt.Before(value.ApprovedAt) || value.UpdatedAt.Before(value.CreatedAt) {
		return AppAccessDisableClaim{}, ErrInvalidStoredState
	}
	intent, err := readAppAccessDisableIntent(ctx, query, operationID)
	if err != nil || intent != appAccessDisableIntentForClaim(value) {
		if err != nil {
			return AppAccessDisableClaim{}, err
		}
		return AppAccessDisableClaim{}, ErrInvalidStoredState
	}
	if err := validateAppAccessDisableClaimHistory(ctx, query, value); err != nil {
		return AppAccessDisableClaim{}, err
	}
	return value, nil
}

func validateAppAccessDisableClaimHistory(ctx context.Context, query appAccessDisableQuerier, claim AppAccessDisableClaim) error {
	rows, err := query.QueryContext(ctx, `SELECT sequence,state,gateway_operation_id,
		protected_state_digest,created_at FROM lan_app_access_disable_claim_events
		WHERE operation_id=? ORDER BY sequence`, claim.OperationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int64
	var previous, last AppAccessDisableState
	var previousAt, lastAt time.Time
	var lastGateway, lastDigest sql.NullString
	for rows.Next() {
		var sequence int64
		var state AppAccessDisableState
		var gateway, digest sql.NullString
		var created string
		if err := rows.Scan(&sequence, &state, &gateway, &digest, &created); err != nil {
			return err
		}
		count++
		stamp, parseErr := parseTime(created)
		if parseErr != nil || sequence != count || !validAppAccessDisableState(state) {
			return ErrInvalidStoredState
		}
		if sequence == 1 {
			if state != AppAccessDisablePrepared || !stamp.Equal(claim.CreatedAt) {
				return ErrInvalidStoredState
			}
		} else if !validAppAccessDisableTransition(previous, state) || stamp.Before(previousAt) {
			return ErrInvalidStoredState
		}
		if (state == AppAccessDisableCommitted) != (gateway.Valid && digest.Valid) {
			return ErrInvalidStoredState
		}
		previous, previousAt = state, stamp
		last, lastAt, lastGateway, lastDigest = state, stamp, gateway, digest
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != claim.StateSequence || last != claim.State || !lastAt.Equal(claim.UpdatedAt) {
		return ErrInvalidStoredState
	}
	if claim.Proof == nil {
		if lastGateway.Valid || lastDigest.Valid {
			return ErrInvalidStoredState
		}
	} else if !lastGateway.Valid || !lastDigest.Valid ||
		lastGateway.String != claim.Proof.GatewayOperationID ||
		lastDigest.String != claim.Proof.ProtectedStateDigest {
		return ErrInvalidStoredState
	}
	return nil
}

func appAccessDisableIntentForClaim(claim AppAccessDisableClaim) AppAccessDisableIntent {
	return AppAccessDisableIntent{OperationID: claim.OperationID, RequestDigest: claim.RequestDigest,
		Spec: claim.Spec, SpecDigest: claim.SpecDigest, ApprovedBy: claim.ApprovedBy,
		ApprovedAt: claim.ApprovedAt}
}

func validAppAccessDisableClaimOwner(owner AppAccessDisableClaimOwner) bool {
	return validUUID(owner.OperationID) && validDigest(owner.RequestDigest) &&
		validUUID(owner.AppID) && validUUID(owner.AllocationID) &&
		validUUID(owner.OwnerOperationID) && validUUID(owner.AccessRevisionID) && validUUID(owner.ActorID)
}

func appAccessDisableOwnerMatches(claim AppAccessDisableClaim, owner AppAccessDisableClaimOwner) bool {
	return claim.OperationID == owner.OperationID && claim.RequestDigest == owner.RequestDigest &&
		claim.Spec.AppID == owner.AppID && claim.Spec.AllocationID == owner.AllocationID &&
		claim.Spec.OwnerOperationID == owner.OwnerOperationID &&
		claim.Spec.AccessRevisionID == owner.AccessRevisionID
}

func validAppAccessDisableState(state AppAccessDisableState) bool {
	switch state {
	case AppAccessDisablePrepared, AppAccessDisableWithdrawing, AppAccessDisableUncertain, AppAccessDisableCommitted:
		return true
	default:
		return false
	}
}

func validAppAccessDisableTransition(from, to AppAccessDisableState) bool {
	switch from {
	case AppAccessDisablePrepared:
		return to == AppAccessDisableWithdrawing
	case AppAccessDisableWithdrawing:
		return to == AppAccessDisableUncertain || to == AppAccessDisableCommitted
	case AppAccessDisableUncertain:
		return to == AppAccessDisableCommitted
	default:
		return false
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
