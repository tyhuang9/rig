package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AppAccessDisableProtectedClearAck records that the exact committed disable
// has had its protected ingress marker cleared and its final state inspected.
// The repository checks the retained identity; the trusted caller must prove
// the external route is absent while holding the gateway lock.
type AppAccessDisableProtectedClearAck struct {
	OperationID               string
	ProofKind                 string
	GatewayOperationID        string
	FinalProtectedStateDigest string
	ObservedAt                time.Time
	AcknowledgedAt            time.Time
}

const AppAccessDisableProofExact404 = "exact_404"

const (
	AppAccessDisableSuccessorSameAppNewPort404 = "same_app_new_port_404"
	AppAccessDisableSuccessorSamePort          = "same_port_successor"
)

// AppAccessDisableSuccessorAck records an exact newer live grant observed
// after a historical disable. The trusted caller attests the final protected
// ingress state under its gateway lock; this repository binds that attestation
// to immutable old and successor database identities.
type AppAccessDisableSuccessorAck struct {
	OperationID               string
	GatewayOperationID        string
	SuccessorAttemptID        string
	SuccessorAllocationID     string
	Relation                  string
	FinalProtectedStateDigest string
	ObservedAt                time.Time
	AcknowledgedAt            time.Time
}

func validDisableSuccessorRelation(relation string) bool {
	return relation == AppAccessDisableSuccessorSameAppNewPort404 ||
		relation == AppAccessDisableSuccessorSamePort
}

// AcknowledgeAppAccessDisableSuccessor releases the admission fence only
// after a newer committed grant has superseded the old committed disable.
// It cannot replace or reinterpret an existing exact-404 acknowledgment.
func (r *Repository) AcknowledgeAppAccessDisableSuccessor(ctx context.Context,
	operationID, gatewayOperationID, successorAttemptID, successorAllocationID,
	relation, finalProtectedStateDigest string, observedAt time.Time,
) (AppAccessDisableSuccessorAck, bool, error) {
	if r == nil || r.db == nil || !validUUID(operationID) || !validUUID(gatewayOperationID) ||
		!validUUID(successorAttemptID) || !validUUID(successorAllocationID) ||
		!validDisableSuccessorRelation(relation) || !validDigest(finalProtectedStateDigest) ||
		observedAt.IsZero() {
		return AppAccessDisableSuccessorAck{}, false, ErrInvalidInput
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessDisableSuccessorAck{}, false, err
	}
	defer tx.Rollback()
	claim, err := readAppAccessDisableClaim(ctx, tx, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableSuccessorAck{}, false, ErrNotFound
	}
	if err != nil {
		return AppAccessDisableSuccessorAck{}, false, err
	}
	if claim.State != AppAccessDisableCommitted || claim.Proof == nil {
		return AppAccessDisableSuccessorAck{}, false, ErrConflict
	}
	if claim.Proof.GatewayOperationID != gatewayOperationID {
		return AppAccessDisableSuccessorAck{}, false, ErrIdempotencyMismatch
	}
	observedAt = observedAt.UTC()
	if observedAt.Before(claim.Proof.ObservedAt) {
		return AppAccessDisableSuccessorAck{}, false, ErrInvalidInput
	}
	if existing, err := readAppAccessDisableSuccessorAck(ctx, tx, operationID); err == nil {
		if existing.GatewayOperationID != gatewayOperationID ||
			existing.SuccessorAttemptID != successorAttemptID ||
			existing.SuccessorAllocationID != successorAllocationID ||
			existing.Relation != relation ||
			existing.FinalProtectedStateDigest != finalProtectedStateDigest {
			return AppAccessDisableSuccessorAck{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessDisableSuccessorAck{}, false, err
		}
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableSuccessorAck{}, false, err
	}
	if _, err := readAppAccessDisableProtectedClearAck(ctx, tx, operationID); err == nil {
		return AppAccessDisableSuccessorAck{}, false, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableSuccessorAck{}, false, err
	}
	successor, err := readAppAccessGrantClaim(ctx, tx, successorAttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableSuccessorAck{}, false, ErrConflict
	}
	if err != nil {
		return AppAccessDisableSuccessorAck{}, false, err
	}
	if successor.State != AppAccessGrantCommitted || successor.Proof == nil ||
		successor.RetiredAt != nil || successor.Spec.AllocationID != successorAllocationID ||
		successor.Spec.AllocationID == claim.Spec.AllocationID ||
		!successor.CreatedAt.After(claim.Proof.ObservedAt) ||
		observedAt.Before(successor.Proof.ObservedAt) ||
		!disableSuccessorRelationMatches(claim, successor, relation) {
		return AppAccessDisableSuccessorAck{}, false, ErrConflict
	}
	acknowledgedAt := r.now().UTC()
	if acknowledgedAt.Before(observedAt) {
		return AppAccessDisableSuccessorAck{}, false, ErrInvalidInput
	}
	value := AppAccessDisableSuccessorAck{
		OperationID: operationID, GatewayOperationID: gatewayOperationID,
		SuccessorAttemptID: successorAttemptID, SuccessorAllocationID: successorAllocationID,
		Relation: relation, FinalProtectedStateDigest: finalProtectedStateDigest,
		ObservedAt: observedAt, AcknowledgedAt: acknowledgedAt,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_app_access_disable_successor_acks(
		operation_id,gateway_operation_id,successor_attempt_id,successor_allocation_id,
		relation,final_protected_state_digest,observed_at,acknowledged_at
	) VALUES(?,?,?,?,?,?,?,?)`, operationID, gatewayOperationID, successorAttemptID,
		successorAllocationID, relation, finalProtectedStateDigest, formatTime(observedAt),
		formatTime(acknowledgedAt)); err != nil {
		if strings.Contains(err.Error(), "LAN disable successor acknowledgment requires exact active successor") {
			return AppAccessDisableSuccessorAck{}, false, ErrConflict
		}
		return AppAccessDisableSuccessorAck{}, false, classifyImmediateTransactionError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessDisableSuccessorAck{}, false, err
	}
	return value, true, nil
}

func disableSuccessorRelationMatches(disable AppAccessDisableClaim, grant AppAccessGrantClaim, relation string) bool {
	switch relation {
	case AppAccessDisableSuccessorSameAppNewPort404:
		return grant.Spec.AppID == disable.Spec.AppID && grant.Spec.Port != disable.Spec.Port
	case AppAccessDisableSuccessorSamePort:
		return grant.Spec.Port == disable.Spec.Port
	default:
		return false
	}
}

func readAppAccessDisableSuccessorAck(ctx context.Context, query rowQuerier,
	operationID string,
) (AppAccessDisableSuccessorAck, error) {
	var value AppAccessDisableSuccessorAck
	var observedAt, acknowledgedAt string
	err := query.QueryRowContext(ctx, `SELECT operation_id,gateway_operation_id,
		successor_attempt_id,successor_allocation_id,relation,
		final_protected_state_digest,observed_at,acknowledged_at
		FROM lan_app_access_disable_successor_acks WHERE operation_id=?`, operationID).Scan(
		&value.OperationID, &value.GatewayOperationID, &value.SuccessorAttemptID,
		&value.SuccessorAllocationID, &value.Relation,
		&value.FinalProtectedStateDigest, &observedAt, &acknowledgedAt)
	if err != nil {
		return AppAccessDisableSuccessorAck{}, err
	}
	value.ObservedAt, err = parseTime(observedAt)
	if err != nil {
		return AppAccessDisableSuccessorAck{}, ErrInvalidStoredState
	}
	value.AcknowledgedAt, err = parseTime(acknowledgedAt)
	if err != nil || value.OperationID != operationID || !validUUID(value.GatewayOperationID) ||
		!validUUID(value.SuccessorAttemptID) || !validUUID(value.SuccessorAllocationID) ||
		!validDisableSuccessorRelation(value.Relation) ||
		!validDigest(value.FinalProtectedStateDigest) || value.AcknowledgedAt.Before(value.ObservedAt) {
		return AppAccessDisableSuccessorAck{}, ErrInvalidStoredState
	}
	return value, nil
}

// AcknowledgeAppAccessDisableProtectedClear is the durable admission boundary
// after the gateway has cleared its pending marker and proved absence. A
// committed claim without this separate fact continues to fence new LAN work.
func (r *Repository) AcknowledgeAppAccessDisableProtectedClear(ctx context.Context,
	operationID, gatewayOperationID, finalProtectedStateDigest string, observedAt time.Time,
) (AppAccessDisableProtectedClearAck, bool, error) {
	if r == nil || r.db == nil || !validUUID(operationID) || !validUUID(gatewayOperationID) ||
		!validDigest(finalProtectedStateDigest) || observedAt.IsZero() {
		return AppAccessDisableProtectedClearAck{}, false, ErrInvalidInput
	}
	tx, err := beginImmediateTransaction(ctx, r.db)
	if err != nil {
		return AppAccessDisableProtectedClearAck{}, false, err
	}
	defer tx.Rollback()
	claim, err := readAppAccessDisableClaim(ctx, tx, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableProtectedClearAck{}, false, ErrNotFound
	}
	if err != nil {
		return AppAccessDisableProtectedClearAck{}, false, err
	}
	if claim.State != AppAccessDisableCommitted || claim.Proof == nil {
		return AppAccessDisableProtectedClearAck{}, false, ErrConflict
	}
	if claim.Proof.GatewayOperationID != gatewayOperationID {
		return AppAccessDisableProtectedClearAck{}, false, ErrIdempotencyMismatch
	}
	observedAt = observedAt.UTC()
	if observedAt.Before(claim.Proof.ObservedAt) {
		return AppAccessDisableProtectedClearAck{}, false, ErrInvalidInput
	}
	existing, err := readAppAccessDisableProtectedClearAck(ctx, tx, operationID)
	if err == nil {
		if existing.GatewayOperationID != gatewayOperationID ||
			existing.FinalProtectedStateDigest != finalProtectedStateDigest {
			return AppAccessDisableProtectedClearAck{}, false, ErrIdempotencyMismatch
		}
		if err := tx.Commit(ctx); err != nil {
			return AppAccessDisableProtectedClearAck{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableProtectedClearAck{}, false, err
	}
	if _, err := readAppAccessDisableSuccessorAck(ctx, tx, operationID); err == nil {
		return AppAccessDisableProtectedClearAck{}, false, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppAccessDisableProtectedClearAck{}, false, err
	}
	value := AppAccessDisableProtectedClearAck{
		OperationID: operationID, GatewayOperationID: gatewayOperationID,
		ProofKind:                 AppAccessDisableProofExact404,
		FinalProtectedStateDigest: finalProtectedStateDigest,
		ObservedAt:                observedAt, AcknowledgedAt: r.now().UTC(),
	}
	if value.AcknowledgedAt.Before(value.ObservedAt) {
		return AppAccessDisableProtectedClearAck{}, false, ErrInvalidInput
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO lan_app_access_disable_clear_acks(
		operation_id,proof_kind,gateway_operation_id,final_protected_state_digest,observed_at,acknowledged_at
	) VALUES(?,?,?,?,?,?)`, value.OperationID, value.ProofKind, value.GatewayOperationID,
		value.FinalProtectedStateDigest, formatTime(value.ObservedAt),
		formatTime(value.AcknowledgedAt)); err != nil {
		return AppAccessDisableProtectedClearAck{}, false, classifyImmediateTransactionError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AppAccessDisableProtectedClearAck{}, false, err
	}
	return value, true, nil
}

func readAppAccessDisableProtectedClearAck(ctx context.Context, query rowQuerier,
	operationID string,
) (AppAccessDisableProtectedClearAck, error) {
	var value AppAccessDisableProtectedClearAck
	var observedAt, acknowledgedAt string
	err := query.QueryRowContext(ctx, `SELECT operation_id,proof_kind,gateway_operation_id,
		final_protected_state_digest,observed_at,acknowledged_at
		FROM lan_app_access_disable_clear_acks WHERE operation_id=?`, operationID).Scan(
		&value.OperationID, &value.ProofKind, &value.GatewayOperationID, &value.FinalProtectedStateDigest,
		&observedAt, &acknowledgedAt)
	if err != nil {
		return AppAccessDisableProtectedClearAck{}, err
	}
	value.ObservedAt, err = parseTime(observedAt)
	if err != nil {
		return AppAccessDisableProtectedClearAck{}, ErrInvalidStoredState
	}
	value.AcknowledgedAt, err = parseTime(acknowledgedAt)
	if err != nil || value.OperationID != operationID || value.ProofKind != AppAccessDisableProofExact404 ||
		!validUUID(value.GatewayOperationID) ||
		!validDigest(value.FinalProtectedStateDigest) || value.AcknowledgedAt.Before(value.ObservedAt) {
		return AppAccessDisableProtectedClearAck{}, ErrInvalidStoredState
	}
	return value, nil
}

func unresolvedAppAccessDisableClearExists(ctx context.Context, query rowQuerier) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM lan_app_access_disable_claims d
		LEFT JOIN lan_app_access_disable_clear_acks a ON a.operation_id=d.operation_id
		LEFT JOIN lan_app_access_disable_successor_acks s ON s.operation_id=d.operation_id
		WHERE d.state<>'committed' OR (a.operation_id IS NULL AND s.operation_id IS NULL)
	)`).Scan(&exists)
	return exists != 0, err
}
