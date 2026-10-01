package appaccess

import (
	"context"
	"database/sql"
	"errors"
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
		WHERE d.state<>'committed' OR a.operation_id IS NULL
	)`).Scan(&exists)
	return exists != 0, err
}
