package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RouteAccountingHandoffAuthorization is immutable permission for the
// already-borrowed private route execution to move its fixed accounting
// roster. It is retained evidence, not restart or effect authority.
type RouteAccountingHandoffAuthorization struct {
	ActivationReceiptID       string
	AppID                     string
	SuccessorDeploymentID     string
	PredecessorDeploymentID   string
	CandidateRosterDigest     string
	PredecessorRosterDigest   string
	SuccessorComponentCount   int
	PredecessorComponentCount int
	AuthorizedAt              time.Time
}

// RouteAccountingHandoffReceipt is the immutable durable result of the
// process-local all-or-none accounting transition.
type RouteAccountingHandoffReceipt struct {
	ActivationReceiptID       string
	AppID                     string
	SuccessorDeploymentID     string
	PredecessorDeploymentID   string
	CandidateRosterDigest     string
	PredecessorRosterDigest   string
	SuccessorComponentCount   int
	PredecessorComponentCount int
	HandedOffAt               time.Time
}

// RecordRouteAccountingHandoffAuthorizationTx stores the exact activation,
// completed retirement and fixed rosters before the process-local handoff.
// The caller owns the transaction and the opaque execution authority.
func (r *Repository) RecordRouteAccountingHandoffAuthorizationTx(ctx context.Context, tx *sql.Tx,
	activation RouteActivationReceipt, retirement PredecessorRetirementReceipt,
	candidateRoster, predecessorRoster []RouteActivationComponent, authorizedAt time.Time,
) (RouteAccountingHandoffAuthorization, bool, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !validRouteActivationReceipt(activation) ||
		authorizedAt.IsZero() {
		return RouteAccountingHandoffAuthorization{}, false, ErrInvalidState
	}
	candidate, ok := canonicalPredecessorRetirementRoster(candidateRoster, activation.SuccessorDeploymentID)
	if !ok || len(candidate) == 0 || predecessorRetirementRosterDigest(candidate) != activation.CandidateRosterDigest {
		return RouteAccountingHandoffAuthorization{}, false, ErrConflict
	}
	predecessor, ok := canonicalPredecessorRetirementRoster(predecessorRoster, activation.PredecessorDeploymentID)
	if !ok || predecessorRetirementRosterDigest(predecessor) != activation.PredecessorRosterDigest ||
		!samePredecessorRetirementReceiptIdentity(retirement, activation, len(predecessor)) {
		return RouteAccountingHandoffAuthorization{}, false, ErrConflict
	}
	persistedActivation, err := readRouteActivationReceiptTx(ctx, tx, activation.ReceiptID)
	if err != nil || persistedActivation != activation {
		return RouteAccountingHandoffAuthorization{}, false, errors.Join(ErrConflict, err)
	}
	persistedRetirement, found, err := readPredecessorRetirementReceiptTx(ctx, tx, activation.ReceiptID)
	if err != nil || !found || persistedRetirement != retirement {
		return RouteAccountingHandoffAuthorization{}, false, errors.Join(ErrConflict, err)
	}
	existing, found, err := readRouteAccountingHandoffAuthorizationTx(ctx, tx, activation.ReceiptID)
	if err != nil {
		return RouteAccountingHandoffAuthorization{}, false, err
	}
	if found {
		if !sameRouteAccountingHandoffAuthorizationIdentity(existing, activation, len(candidate), len(predecessor)) {
			return RouteAccountingHandoffAuthorization{}, false, ErrConflict
		}
		return existing, false, nil
	}
	stamp := authorizedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO generated_route_accounting_handoff_authorizations(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		candidate_roster_digest,predecessor_roster_digest,successor_component_count,
		predecessor_component_count,authorized_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, activation.ReceiptID, activation.AppID, activation.SuccessorDeploymentID,
		activation.PredecessorDeploymentID, activation.CandidateRosterDigest, activation.PredecessorRosterDigest,
		len(candidate), len(predecessor), stamp)
	if err != nil {
		return RouteAccountingHandoffAuthorization{}, false, err
	}
	value, found, err := readRouteAccountingHandoffAuthorizationTx(ctx, tx, activation.ReceiptID)
	if err != nil || !found || !sameRouteAccountingHandoffAuthorizationIdentity(value, activation, len(candidate), len(predecessor)) {
		return RouteAccountingHandoffAuthorization{}, false, errors.Join(ErrConflict, err)
	}
	return value, true, nil
}

// RecordRouteAccountingHandoffReceiptTx stores the terminal durable result
// after the private ledger transition and fresh proof have completed.
func (r *Repository) RecordRouteAccountingHandoffReceiptTx(ctx context.Context, tx *sql.Tx,
	authorization RouteAccountingHandoffAuthorization, handedOffAt time.Time,
) (RouteAccountingHandoffReceipt, bool, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil ||
		!validRouteAccountingHandoffAuthorization(authorization) || handedOffAt.IsZero() {
		return RouteAccountingHandoffReceipt{}, false, ErrInvalidState
	}
	persisted, found, err := readRouteAccountingHandoffAuthorizationTx(ctx, tx, authorization.ActivationReceiptID)
	if err != nil || !found || persisted != authorization {
		return RouteAccountingHandoffReceipt{}, false, errors.Join(ErrConflict, err)
	}
	existing, found, err := readRouteAccountingHandoffReceiptTx(ctx, tx, authorization.ActivationReceiptID)
	if err != nil {
		return RouteAccountingHandoffReceipt{}, false, err
	}
	if found {
		if !sameRouteAccountingHandoffReceiptIdentity(existing, authorization) {
			return RouteAccountingHandoffReceipt{}, false, ErrConflict
		}
		return existing, false, nil
	}
	stamp := handedOffAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO generated_route_accounting_handoff_receipts(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		candidate_roster_digest,predecessor_roster_digest,successor_component_count,
		predecessor_component_count,handed_off_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, authorization.ActivationReceiptID, authorization.AppID,
		authorization.SuccessorDeploymentID, authorization.PredecessorDeploymentID,
		authorization.CandidateRosterDigest, authorization.PredecessorRosterDigest,
		authorization.SuccessorComponentCount, authorization.PredecessorComponentCount, stamp)
	if err != nil {
		return RouteAccountingHandoffReceipt{}, false, err
	}
	value, found, err := readRouteAccountingHandoffReceiptTx(ctx, tx, authorization.ActivationReceiptID)
	if err != nil || !found || !sameRouteAccountingHandoffReceiptIdentity(value, authorization) {
		return RouteAccountingHandoffReceipt{}, false, errors.Join(ErrConflict, err)
	}
	return value, true, nil
}

func readRouteAccountingHandoffAuthorizationTx(ctx context.Context, tx *sql.Tx,
	receiptID string,
) (RouteAccountingHandoffAuthorization, bool, error) {
	var value RouteAccountingHandoffAuthorization
	var stamp string
	err := tx.QueryRowContext(ctx, `SELECT activation_receipt_id,app_id,successor_deployment_id,
		predecessor_deployment_id,candidate_roster_digest,predecessor_roster_digest,
		successor_component_count,predecessor_component_count,authorized_at
		FROM generated_route_accounting_handoff_authorizations WHERE activation_receipt_id=?`, receiptID).
		Scan(&value.ActivationReceiptID, &value.AppID, &value.SuccessorDeploymentID,
			&value.PredecessorDeploymentID, &value.CandidateRosterDigest, &value.PredecessorRosterDigest,
			&value.SuccessorComponentCount, &value.PredecessorComponentCount, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	value.AuthorizedAt, err = parseTime(stamp)
	if err != nil || !validRouteAccountingHandoffAuthorization(value) {
		return RouteAccountingHandoffAuthorization{}, false, errors.Join(ErrInvalidState, err)
	}
	return value, true, nil
}

func readRouteAccountingHandoffReceiptTx(ctx context.Context, tx *sql.Tx,
	receiptID string,
) (RouteAccountingHandoffReceipt, bool, error) {
	var value RouteAccountingHandoffReceipt
	var stamp string
	err := tx.QueryRowContext(ctx, `SELECT activation_receipt_id,app_id,successor_deployment_id,
		predecessor_deployment_id,candidate_roster_digest,predecessor_roster_digest,
		successor_component_count,predecessor_component_count,handed_off_at
		FROM generated_route_accounting_handoff_receipts WHERE activation_receipt_id=?`, receiptID).
		Scan(&value.ActivationReceiptID, &value.AppID, &value.SuccessorDeploymentID,
			&value.PredecessorDeploymentID, &value.CandidateRosterDigest, &value.PredecessorRosterDigest,
			&value.SuccessorComponentCount, &value.PredecessorComponentCount, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	value.HandedOffAt, err = parseTime(stamp)
	if err != nil || !validRouteAccountingHandoffReceipt(value) {
		return RouteAccountingHandoffReceipt{}, false, errors.Join(ErrInvalidState, err)
	}
	return value, true, nil
}

func validRouteAccountingHandoffAuthorization(value RouteAccountingHandoffAuthorization) bool {
	return canonicalUUID(value.ActivationReceiptID) && canonicalUUID(value.AppID) &&
		canonicalUUID(value.SuccessorDeploymentID) &&
		(value.PredecessorDeploymentID == "" || canonicalUUID(value.PredecessorDeploymentID)) &&
		lowerHex(value.CandidateRosterDigest, 64) && lowerHex(value.PredecessorRosterDigest, 64) &&
		value.SuccessorComponentCount > 0 && value.PredecessorComponentCount >= 0 &&
		(value.PredecessorDeploymentID == "") == (value.PredecessorComponentCount == 0) &&
		!value.AuthorizedAt.IsZero()
}

func validRouteAccountingHandoffReceipt(value RouteAccountingHandoffReceipt) bool {
	return canonicalUUID(value.ActivationReceiptID) && canonicalUUID(value.AppID) &&
		canonicalUUID(value.SuccessorDeploymentID) &&
		(value.PredecessorDeploymentID == "" || canonicalUUID(value.PredecessorDeploymentID)) &&
		lowerHex(value.CandidateRosterDigest, 64) && lowerHex(value.PredecessorRosterDigest, 64) &&
		value.SuccessorComponentCount > 0 && value.PredecessorComponentCount >= 0 &&
		(value.PredecessorDeploymentID == "") == (value.PredecessorComponentCount == 0) &&
		!value.HandedOffAt.IsZero()
}

func sameRouteAccountingHandoffAuthorizationIdentity(value RouteAccountingHandoffAuthorization,
	activation RouteActivationReceipt, successorCount, predecessorCount int,
) bool {
	return value.ActivationReceiptID == activation.ReceiptID && value.AppID == activation.AppID &&
		value.SuccessorDeploymentID == activation.SuccessorDeploymentID &&
		value.PredecessorDeploymentID == activation.PredecessorDeploymentID &&
		value.CandidateRosterDigest == activation.CandidateRosterDigest &&
		value.PredecessorRosterDigest == activation.PredecessorRosterDigest &&
		value.SuccessorComponentCount == successorCount && value.PredecessorComponentCount == predecessorCount &&
		!value.AuthorizedAt.IsZero()
}

func sameRouteAccountingHandoffReceiptIdentity(value RouteAccountingHandoffReceipt,
	authorization RouteAccountingHandoffAuthorization,
) bool {
	return value.ActivationReceiptID == authorization.ActivationReceiptID && value.AppID == authorization.AppID &&
		value.SuccessorDeploymentID == authorization.SuccessorDeploymentID &&
		value.PredecessorDeploymentID == authorization.PredecessorDeploymentID &&
		value.CandidateRosterDigest == authorization.CandidateRosterDigest &&
		value.PredecessorRosterDigest == authorization.PredecessorRosterDigest &&
		value.SuccessorComponentCount == authorization.SuccessorComponentCount &&
		value.PredecessorComponentCount == authorization.PredecessorComponentCount &&
		!value.HandedOffAt.IsZero()
}
