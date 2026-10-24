package generatedruntimestate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// PredecessorRetirementObservation is immutable evidence that one exact
// receipt-owned predecessor component was freshly observed absent.
type PredecessorRetirementObservation struct {
	ActivationReceiptID     string
	AppID                   string
	SuccessorDeploymentID   string
	PredecessorDeploymentID string
	ComponentName           string
	CandidateReceiptID      string
	ContainerID             string
	CandidatePolicyDigest   string
	ObservedAbsentAt        time.Time
}

// PredecessorRetirementReceipt is the immutable complete-roster result. It is
// not effect authority and is never sufficient to reconstruct a lost private
// retirement capability.
type PredecessorRetirementReceipt struct {
	ActivationReceiptID     string
	AppID                   string
	SuccessorDeploymentID   string
	PredecessorDeploymentID string
	PredecessorRosterDigest string
	ComponentCount          int
	RetiredAt               time.Time
}

// RecordPredecessorRetirementComponentTx atomically marks one exact component
// stopped and stores its immutable fresh-absence observation. The caller owns
// the transaction and must perform the private effect-boundary proof first.
func (r *Repository) RecordPredecessorRetirementComponentTx(ctx context.Context, tx *sql.Tx,
	receipt RouteActivationReceipt, component RouteActivationComponent, observedAt time.Time,
) (PredecessorRetirementObservation, bool, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !validRouteActivationReceipt(receipt) ||
		!validRouteActivationComponent(component) || observedAt.IsZero() ||
		receipt.PredecessorDeploymentID == "" || component.DeploymentID != receipt.PredecessorDeploymentID {
		return PredecessorRetirementObservation{}, false, ErrInvalidState
	}
	persisted, err := readRouteActivationReceiptTx(ctx, tx, receipt.ReceiptID)
	if err != nil || persisted != receipt {
		return PredecessorRetirementObservation{}, false, errors.Join(ErrConflict, err)
	}
	existing, found, err := readPredecessorRetirementObservationTx(ctx, tx, receipt.ReceiptID, component.ComponentName)
	if err != nil {
		return PredecessorRetirementObservation{}, false, err
	}
	if found {
		if !samePredecessorRetirementObservationIdentity(existing, receipt, component) {
			return PredecessorRetirementObservation{}, false, ErrConflict
		}
		return existing, false, nil
	}
	stamp := observedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_components
		SET state='stopped',diagnostic_code=NULL,updated_at=?,finished_at=?
		WHERE deployment_id=? AND component_name=? AND state IN ('active','draining')
		  AND container_name=? AND container_id=? AND image_artifact_id=?`,
		stamp, stamp, component.DeploymentID, component.ComponentName, component.ContainerName,
		component.ContainerID, component.ImageArtifactID)
	if err != nil {
		return PredecessorRetirementObservation{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return PredecessorRetirementObservation{}, false, errors.Join(ErrInvalidTransition, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO generated_route_predecessor_retirements(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		component_name,candidate_receipt_id,container_id,candidate_policy_digest,observed_absent_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, receipt.ReceiptID, receipt.AppID, receipt.SuccessorDeploymentID,
		receipt.PredecessorDeploymentID, component.ComponentName, component.CandidateReceiptID,
		component.ContainerID, component.CandidatePolicyDigest, stamp)
	if err != nil {
		return PredecessorRetirementObservation{}, false, err
	}
	value, found, err := readPredecessorRetirementObservationTx(ctx, tx, receipt.ReceiptID, component.ComponentName)
	if err != nil || !found || !samePredecessorRetirementObservationIdentity(value, receipt, component) {
		return PredecessorRetirementObservation{}, false, errors.Join(ErrConflict, err)
	}
	return value, true, nil
}

// RecordPredecessorRetirementReceiptTx stores the aggregate only after every
// fixed predecessor member has an exact immutable component observation.
func (r *Repository) RecordPredecessorRetirementReceiptTx(ctx context.Context, tx *sql.Tx,
	receipt RouteActivationReceipt, roster []RouteActivationComponent, retiredAt time.Time,
) (PredecessorRetirementReceipt, bool, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !validRouteActivationReceipt(receipt) || retiredAt.IsZero() {
		return PredecessorRetirementReceipt{}, false, ErrInvalidState
	}
	persisted, err := readRouteActivationReceiptTx(ctx, tx, receipt.ReceiptID)
	if err != nil || persisted != receipt {
		return PredecessorRetirementReceipt{}, false, errors.Join(ErrConflict, err)
	}
	canonical, ok := canonicalPredecessorRetirementRoster(roster, receipt.PredecessorDeploymentID)
	if !ok || predecessorRetirementRosterDigest(canonical) != receipt.PredecessorRosterDigest {
		return PredecessorRetirementReceipt{}, false, ErrConflict
	}
	existing, found, err := readPredecessorRetirementReceiptTx(ctx, tx, receipt.ReceiptID)
	if err != nil {
		return PredecessorRetirementReceipt{}, false, err
	}
	if found {
		if !samePredecessorRetirementReceiptIdentity(existing, receipt, len(canonical)) {
			return PredecessorRetirementReceipt{}, false, ErrConflict
		}
		return existing, false, nil
	}
	for _, component := range canonical {
		value, present, readErr := readPredecessorRetirementObservationTx(ctx, tx, receipt.ReceiptID, component.ComponentName)
		if readErr != nil || !present || !samePredecessorRetirementObservationIdentity(value, receipt, component) {
			return PredecessorRetirementReceipt{}, false, errors.Join(ErrConflict, readErr)
		}
	}
	stamp := retiredAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO generated_route_predecessor_retirement_receipts(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		predecessor_roster_digest,component_count,retired_at
	) VALUES(?,?,?,?,?,?,?)`, receipt.ReceiptID, receipt.AppID, receipt.SuccessorDeploymentID,
		receipt.PredecessorDeploymentID, receipt.PredecessorRosterDigest, len(canonical), stamp)
	if err != nil {
		return PredecessorRetirementReceipt{}, false, err
	}
	value, found, err := readPredecessorRetirementReceiptTx(ctx, tx, receipt.ReceiptID)
	if err != nil || !found || !samePredecessorRetirementReceiptIdentity(value, receipt, len(canonical)) {
		return PredecessorRetirementReceipt{}, false, errors.Join(ErrConflict, err)
	}
	return value, true, nil
}

func readPredecessorRetirementObservationTx(ctx context.Context, tx *sql.Tx, receiptID, component string) (PredecessorRetirementObservation, bool, error) {
	var value PredecessorRetirementObservation
	var stamp string
	err := tx.QueryRowContext(ctx, `SELECT activation_receipt_id,app_id,successor_deployment_id,
		predecessor_deployment_id,component_name,candidate_receipt_id,container_id,
		candidate_policy_digest,observed_absent_at
		FROM generated_route_predecessor_retirements WHERE activation_receipt_id=? AND component_name=?`,
		receiptID, component).Scan(&value.ActivationReceiptID, &value.AppID, &value.SuccessorDeploymentID,
		&value.PredecessorDeploymentID, &value.ComponentName, &value.CandidateReceiptID,
		&value.ContainerID, &value.CandidatePolicyDigest, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	value.ObservedAbsentAt, err = parseTime(stamp)
	return value, err == nil, err
}

func readPredecessorRetirementReceiptTx(ctx context.Context, tx *sql.Tx, receiptID string) (PredecessorRetirementReceipt, bool, error) {
	var value PredecessorRetirementReceipt
	var stamp string
	err := tx.QueryRowContext(ctx, `SELECT activation_receipt_id,app_id,successor_deployment_id,
		predecessor_deployment_id,predecessor_roster_digest,component_count,retired_at
		FROM generated_route_predecessor_retirement_receipts WHERE activation_receipt_id=?`, receiptID).
		Scan(&value.ActivationReceiptID, &value.AppID, &value.SuccessorDeploymentID,
			&value.PredecessorDeploymentID, &value.PredecessorRosterDigest, &value.ComponentCount, &stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	value.RetiredAt, err = parseTime(stamp)
	return value, err == nil, err
}

func canonicalPredecessorRetirementRoster(values []RouteActivationComponent, deploymentID string) ([]RouteActivationComponent, bool) {
	result := append([]RouteActivationComponent(nil), values...)
	sortRouteActivationComponents(result)
	if deploymentID == "" {
		return result, len(result) == 0
	}
	for index, value := range result {
		if !validRouteActivationComponent(value) || value.DeploymentID != deploymentID ||
			index > 0 && result[index-1].ComponentName == value.ComponentName {
			return nil, false
		}
	}
	return result, true
}

func sortRouteActivationComponents(values []RouteActivationComponent) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor].ComponentName < values[cursor-1].ComponentName; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func predecessorRetirementRosterDigest(values []RouteActivationComponent) string {
	body, _ := json.Marshal(values)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func samePredecessorRetirementObservationIdentity(value PredecessorRetirementObservation,
	receipt RouteActivationReceipt, component RouteActivationComponent,
) bool {
	return value.ActivationReceiptID == receipt.ReceiptID && value.AppID == receipt.AppID &&
		value.SuccessorDeploymentID == receipt.SuccessorDeploymentID &&
		value.PredecessorDeploymentID == receipt.PredecessorDeploymentID &&
		value.ComponentName == component.ComponentName && value.CandidateReceiptID == component.CandidateReceiptID &&
		value.ContainerID == component.ContainerID && value.CandidatePolicyDigest == component.CandidatePolicyDigest &&
		!value.ObservedAbsentAt.IsZero()
}

func samePredecessorRetirementReceiptIdentity(value PredecessorRetirementReceipt,
	receipt RouteActivationReceipt, count int,
) bool {
	return value.ActivationReceiptID == receipt.ReceiptID && value.AppID == receipt.AppID &&
		value.SuccessorDeploymentID == receipt.SuccessorDeploymentID &&
		value.PredecessorDeploymentID == receipt.PredecessorDeploymentID &&
		value.PredecessorRosterDigest == receipt.PredecessorRosterDigest &&
		value.ComponentCount == count && !value.RetiredAt.IsZero()
}
