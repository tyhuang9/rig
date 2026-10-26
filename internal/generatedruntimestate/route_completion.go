package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const routeCompletionCode = "deployment_completed"

// RouteCompletionInput carries exact persisted identities between the
// purpose-specific completion writers. It is not authority: every writer
// independently rejoins these identities to current durable state.
type RouteCompletionInput struct {
	Activation           RouteActivationReceipt
	AccountingHandoff    RouteAccountingHandoffReceipt
	CurrentJobID         string
	CurrentWorkerAttempt int
	CompletedAt          time.Time
}

// RouteCompletionReceipt is immutable evidence that the runtime and main
// deployment reached success under one exact current worker attempt.
type RouteCompletionReceipt struct {
	ActivationReceiptID   string
	AppID                 string
	SuccessorDeploymentID string
	CreatorJobID          string
	CreatorWorkerAttempt  int
	CurrentJobID          string
	CurrentWorkerAttempt  int
	CompletionCode        string
	CompletedAt           time.Time
}

// RecordGeneratedRouteSucceededTx advances only the exact active generated
// successor from Draining to Succeeded. The caller owns the transaction.
func (r *Repository) RecordGeneratedRouteSucceededTx(ctx context.Context, tx *sql.Tx,
	input RouteCompletionInput,
) (Deployment, bool, error) {
	phase, status, jobStatus, jobPhase, err := validateRouteCompletionInputTx(ctx, tx, input)
	if err != nil {
		return Deployment{}, false, err
	}
	if phase == PhaseSucceeded {
		if status != "succeeded" || !((jobStatus == "running" && jobPhase != "cancelling") ||
			(jobStatus == "succeeded" && jobPhase == "succeeded")) {
			return Deployment{}, false, ErrConflict
		}
		receipt, found, readErr := readRouteCompletionReceiptTx(ctx, tx, input.Activation.ReceiptID)
		if readErr != nil || !found || !routeCompletionReceiptMatchesInput(receipt, input) {
			return Deployment{}, false, errors.Join(ErrConflict, readErr)
		}
		value, getErr := getDeployment(ctx, tx, input.Activation.AppID, input.Activation.SuccessorDeploymentID)
		return value, false, getErr
	}
	if phase != PhaseDraining || status != "waiting_health" || jobStatus != "running" || jobPhase == "cancelling" {
		return Deployment{}, false, ErrConflict
	}
	stamp := input.CompletedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE generated_runtime_deployments
		SET phase='succeeded',updated_at=?,finished_at=?
		WHERE deployment_id=? AND app_id=? AND phase='draining'`, stamp, stamp,
		input.Activation.SuccessorDeploymentID, input.Activation.AppID)
	if err != nil {
		return Deployment{}, false, err
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		return Deployment{}, false, errors.Join(ErrConflict, countErr)
	}
	value, err := getDeployment(ctx, tx, input.Activation.AppID, input.Activation.SuccessorDeploymentID)
	if err != nil || value.Phase != PhaseSucceeded {
		return Deployment{}, false, errors.Join(ErrConflict, err)
	}
	return value, true, nil
}

// RecordRouteCompletionReceiptTx stores the fixed completion fact after both
// runtime and main deployment writers have succeeded in the caller's Tx.
func (r *Repository) RecordRouteCompletionReceiptTx(ctx context.Context, tx *sql.Tx,
	input RouteCompletionInput,
) (RouteCompletionReceipt, bool, error) {
	phase, status, jobStatus, jobPhase, err := validateRouteCompletionInputTx(ctx, tx, input)
	if err != nil {
		return RouteCompletionReceipt{}, false, err
	}
	if phase != PhaseSucceeded || status != "succeeded" {
		return RouteCompletionReceipt{}, false, ErrConflict
	}
	existing, found, err := readRouteCompletionReceiptTx(ctx, tx, input.Activation.ReceiptID)
	if err != nil {
		return RouteCompletionReceipt{}, false, err
	}
	if found {
		if !routeCompletionReceiptMatchesInput(existing, input) {
			return RouteCompletionReceipt{}, false, ErrConflict
		}
		return existing, false, nil
	}
	if jobStatus != "running" || jobPhase == "cancelling" {
		return RouteCompletionReceipt{}, false, ErrConflict
	}
	stamp := input.CompletedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO generated_route_completion_receipts(
		activation_receipt_id,app_id,successor_deployment_id,creator_job_id,
		creator_worker_attempt,current_job_id,current_worker_attempt,completion_code,completed_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, input.Activation.ReceiptID, input.Activation.AppID,
		input.Activation.SuccessorDeploymentID, input.Activation.CreatorJobID,
		input.Activation.CreatorWorkerAttempt, input.CurrentJobID, input.CurrentWorkerAttempt,
		routeCompletionCode, stamp)
	if err != nil {
		return RouteCompletionReceipt{}, false, err
	}
	value, found, err := readRouteCompletionReceiptTx(ctx, tx, input.Activation.ReceiptID)
	if err != nil || !found || !routeCompletionReceiptMatchesInput(value, input) {
		return RouteCompletionReceipt{}, false, errors.Join(ErrConflict, err)
	}
	return value, true, nil
}

// ReadRouteCompletionReceiptTx reads retained completion evidence. Possession
// of the returned DTO does not authorize effects or further transitions.
func (r *Repository) ReadRouteCompletionReceiptTx(ctx context.Context, tx *sql.Tx,
	activationReceiptID string,
) (RouteCompletionReceipt, bool, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !canonicalUUID(activationReceiptID) {
		return RouteCompletionReceipt{}, false, ErrInvalidState
	}
	return readRouteCompletionReceiptTx(ctx, tx, activationReceiptID)
}

// ValidateRouteCompletionReceiptsTx proves that the complete retained
// activation and accounting-handoff values equal the caller's typed input.
// It grants no mutation authority; purpose-specific writers must separately
// validate their current state immediately before mutation.
func (r *Repository) ValidateRouteCompletionReceiptsTx(ctx context.Context, tx *sql.Tx,
	input RouteCompletionInput,
) error {
	if r == nil || r.db == nil || ctx == nil || tx == nil || !validRouteCompletionInput(input) {
		return ErrInvalidState
	}
	return validateRouteCompletionReceiptsTx(ctx, tx, input)
}

func validateRouteCompletionReceiptsTx(ctx context.Context, tx *sql.Tx, input RouteCompletionInput) error {
	activation, err := readRouteActivationReceiptTx(ctx, tx, input.Activation.ReceiptID)
	if err != nil || activation != input.Activation {
		return errors.Join(ErrConflict, err)
	}
	handoff, found, err := readRouteAccountingHandoffReceiptTx(ctx, tx, input.Activation.ReceiptID)
	if err != nil || !found || handoff != input.AccountingHandoff {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func validateRouteCompletionInputTx(ctx context.Context, tx *sql.Tx,
	input RouteCompletionInput,
) (Phase, string, string, string, error) {
	if ctx == nil || tx == nil || !validRouteCompletionInput(input) {
		return "", "", "", "", ErrInvalidState
	}
	if err := validateRouteCompletionReceiptsTx(ctx, tx, input); err != nil {
		return "", "", "", "", err
	}
	var phase Phase
	var status string
	var jobStatus, jobPhase string
	err := tx.QueryRowContext(ctx, `SELECT runtime.phase,deployment.status,job.status,job.phase
		FROM generated_route_activations a
		JOIN generated_route_accounting_handoff_receipts handoff
		  ON handoff.activation_receipt_id=a.id
		 AND handoff.app_id=a.app_id
		 AND handoff.successor_deployment_id=a.successor_deployment_id
		 AND handoff.predecessor_deployment_id=a.predecessor_deployment_id
		 AND handoff.candidate_roster_digest=a.candidate_roster_digest
		 AND handoff.predecessor_roster_digest=a.predecessor_roster_digest
		JOIN generated_runtime_active_heads head
		  ON head.app_id=a.app_id AND head.deployment_id=a.successor_deployment_id
		 AND head.release_id=a.successor_release_id AND head.slot=a.successor_slot
		 AND head.generation=a.successor_generation AND head.activation_receipt_id=a.id
		JOIN generated_runtime_deployments runtime
		  ON runtime.deployment_id=a.successor_deployment_id AND runtime.app_id=a.app_id
		 AND runtime.release_id=a.successor_release_id AND runtime.candidate_slot=a.successor_slot
		JOIN deployments deployment
		  ON deployment.id=runtime.deployment_id AND deployment.app_id=runtime.app_id
		 AND deployment.job_id=a.creator_job_id
		JOIN jobs job
		  ON job.id=deployment.job_id AND job.type='deploy'
		 AND job.resource_type='application' AND job.resource_id=a.app_id
		WHERE a.id=? AND a.creator_job_id=? AND a.creator_worker_attempt<=?
		 AND job.id=? AND job.attempt=? AND job.status IN ('running','succeeded')
		  AND handoff.successor_component_count=(
		      SELECT COUNT(*) FROM generated_runtime_components c
		      WHERE c.deployment_id=a.successor_deployment_id AND c.state='active')
		  AND NOT EXISTS (
		      SELECT 1 FROM generated_runtime_components c
		      WHERE c.deployment_id=a.successor_deployment_id
		        AND (c.state<>'active' OR NOT EXISTS (
		            SELECT 1 FROM generated_candidate_intents i
		            WHERE i.deployment_id=c.deployment_id AND i.component_name=c.component_name)))`,
		input.Activation.ReceiptID, input.Activation.CreatorJobID, input.CurrentWorkerAttempt,
		input.CurrentJobID, input.CurrentWorkerAttempt).Scan(&phase, &status, &jobStatus, &jobPhase)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", "", ErrConflict
	}
	return phase, status, jobStatus, jobPhase, err
}

func validRouteCompletionInput(input RouteCompletionInput) bool {
	activation := input.Activation
	handoff := input.AccountingHandoff
	return validRouteActivationReceipt(activation) && validRouteAccountingHandoffReceipt(handoff) &&
		canonicalUUID(input.CurrentJobID) && input.CurrentJobID == activation.CreatorJobID &&
		input.CurrentWorkerAttempt >= activation.CreatorWorkerAttempt && !input.CompletedAt.IsZero() &&
		handoff.ActivationReceiptID == activation.ReceiptID && handoff.AppID == activation.AppID &&
		handoff.SuccessorDeploymentID == activation.SuccessorDeploymentID &&
		handoff.PredecessorDeploymentID == activation.PredecessorDeploymentID &&
		handoff.CandidateRosterDigest == activation.CandidateRosterDigest &&
		handoff.PredecessorRosterDigest == activation.PredecessorRosterDigest
}

func readRouteCompletionReceiptTx(ctx context.Context, tx *sql.Tx,
	activationReceiptID string,
) (RouteCompletionReceipt, bool, error) {
	var value RouteCompletionReceipt
	var completedAt string
	err := tx.QueryRowContext(ctx, `SELECT activation_receipt_id,app_id,successor_deployment_id,
		creator_job_id,creator_worker_attempt,current_job_id,current_worker_attempt,
		completion_code,completed_at FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, activationReceiptID).Scan(&value.ActivationReceiptID,
		&value.AppID, &value.SuccessorDeploymentID, &value.CreatorJobID,
		&value.CreatorWorkerAttempt, &value.CurrentJobID, &value.CurrentWorkerAttempt,
		&value.CompletionCode, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RouteCompletionReceipt{}, false, nil
	}
	if err != nil {
		return RouteCompletionReceipt{}, false, err
	}
	value.CompletedAt, err = parseTime(completedAt)
	if err != nil || !validRouteCompletionReceipt(value) {
		return RouteCompletionReceipt{}, false, errors.Join(ErrInvalidState, err)
	}
	return value, true, nil
}

func readGeneratedDomainStartupRouteCompletionReceipts(ctx context.Context, tx *sql.Tx) (
	[]RouteCompletionReceipt, error,
) {
	rows, err := tx.QueryContext(ctx, `SELECT activation_receipt_id,app_id,successor_deployment_id,
		creator_job_id,creator_worker_attempt,current_job_id,current_worker_attempt,
		completion_code,completed_at FROM generated_route_completion_receipts
		ORDER BY activation_receipt_id`)
	if err != nil {
		return nil, err
	}
	result := []RouteCompletionReceipt{}
	for rows.Next() {
		var value RouteCompletionReceipt
		var completedAt string
		if err := rows.Scan(&value.ActivationReceiptID, &value.AppID, &value.SuccessorDeploymentID,
			&value.CreatorJobID, &value.CreatorWorkerAttempt, &value.CurrentJobID,
			&value.CurrentWorkerAttempt, &value.CompletionCode, &completedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		value.CompletedAt, err = parseTime(completedAt)
		if err != nil || !validRouteCompletionReceipt(value) {
			_ = rows.Close()
			return nil, errors.Join(ErrInvalidState, err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func validRouteCompletionReceipt(value RouteCompletionReceipt) bool {
	return canonicalUUID(value.ActivationReceiptID) && canonicalUUID(value.AppID) &&
		canonicalUUID(value.SuccessorDeploymentID) && canonicalUUID(value.CreatorJobID) &&
		value.CreatorWorkerAttempt > 0 && canonicalUUID(value.CurrentJobID) &&
		value.CurrentJobID == value.CreatorJobID &&
		value.CurrentWorkerAttempt >= value.CreatorWorkerAttempt &&
		value.CompletionCode == routeCompletionCode && !value.CompletedAt.IsZero()
}

func routeCompletionReceiptMatchesInput(value RouteCompletionReceipt, input RouteCompletionInput) bool {
	return validRouteCompletionReceipt(value) && value.ActivationReceiptID == input.Activation.ReceiptID &&
		value.AppID == input.Activation.AppID &&
		value.SuccessorDeploymentID == input.Activation.SuccessorDeploymentID &&
		value.CreatorJobID == input.Activation.CreatorJobID &&
		value.CreatorWorkerAttempt == input.Activation.CreatorWorkerAttempt &&
		value.CurrentJobID == input.CurrentJobID &&
		value.CurrentWorkerAttempt == input.CurrentWorkerAttempt &&
		value.CompletionCode == routeCompletionCode && value.CompletedAt.Equal(input.CompletedAt)
}
