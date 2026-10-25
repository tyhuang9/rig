package deployments

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// RecordGeneratedRouteSucceededTx advances only the main deployment bound to
// the exact retained activation and handoff. The caller owns the transaction.
func (r *Repository) RecordGeneratedRouteSucceededTx(ctx context.Context, tx *sql.Tx,
	input generatedruntimestate.RouteCompletionInput,
) (Deployment, bool, error) {
	if r == nil || r.db == nil || ctx == nil || tx == nil ||
		!validGeneratedRouteCompletionInput(input) {
		return Deployment{}, false, ErrInvalidDeployment
	}
	if err := generatedruntimestate.New(r.db).ValidateRouteCompletionReceiptsTx(ctx, tx, input); err != nil {
		return Deployment{}, false, errors.Join(ErrInvalidTransition, err)
	}
	status, err := generatedRouteCompletionDeploymentStatusTx(ctx, tx, input)
	if err != nil {
		return Deployment{}, false, err
	}
	if status == Succeeded {
		receipt, found, readErr := generatedruntimestate.New(r.db).
			ReadRouteCompletionReceiptTx(ctx, tx, input.Activation.ReceiptID)
		if readErr != nil || !found || !generatedRouteCompletionReceiptMatchesInput(receipt, input) {
			return Deployment{}, false, errors.Join(ErrInvalidTransition, readErr)
		}
		value, getErr := scanDeployment(tx.QueryRowContext(ctx, deploymentSelect+` WHERE d.id=? AND d.app_id=?`,
			input.Activation.SuccessorDeploymentID, input.Activation.AppID))
		return value, false, getErr
	}
	if status != WaitingHealth {
		return Deployment{}, false, ErrInvalidTransition
	}
	stamp := input.CompletedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE deployments
		SET status='succeeded',started_at=COALESCE(started_at,?),finished_at=?,diagnostic_code=NULL,
		    failure_code=NULL,failure_summary=NULL
		WHERE id=? AND app_id=? AND job_id=? AND status='waiting_health'`, stamp, stamp,
		input.Activation.SuccessorDeploymentID, input.Activation.AppID, input.CurrentJobID)
	if err != nil {
		return Deployment{}, false, err
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		return Deployment{}, false, errors.Join(ErrInvalidTransition, countErr)
	}
	value, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelect+` WHERE d.id=? AND d.app_id=?`,
		input.Activation.SuccessorDeploymentID, input.Activation.AppID))
	if err != nil || value.Status != Succeeded || value.JobID != input.CurrentJobID {
		return Deployment{}, false, errors.Join(ErrInvalidTransition, err)
	}
	return value, true, nil
}

func generatedRouteCompletionDeploymentStatusTx(ctx context.Context, tx *sql.Tx,
	input generatedruntimestate.RouteCompletionInput,
) (Status, error) {
	var status Status
	activation := input.Activation
	handoff := input.AccountingHandoff
	err := tx.QueryRowContext(ctx, `SELECT deployment.status
		FROM generated_route_activations a
		JOIN generated_route_accounting_handoff_receipts handoff
		  ON handoff.activation_receipt_id=a.id AND handoff.app_id=a.app_id
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
		 AND runtime.phase='succeeded'
		JOIN deployments deployment
		  ON deployment.id=runtime.deployment_id AND deployment.app_id=runtime.app_id
		 AND deployment.job_id=a.creator_job_id
		JOIN jobs job
		  ON job.id=deployment.job_id AND job.type='deploy'
		 AND job.resource_type='application' AND job.resource_id=a.app_id
		WHERE a.id=? AND a.app_id=? AND a.successor_deployment_id=?
		  AND a.creator_job_id=? AND a.creator_worker_attempt=?
		  AND handoff.successor_component_count=? AND handoff.predecessor_component_count=?
		  AND handoff.handed_off_at=?
		  AND job.id=? AND job.attempt=?
		  AND a.creator_worker_attempt<=job.attempt
		  AND ((deployment.status='waiting_health' AND job.status='running' AND job.phase<>'cancelling')
		       OR (deployment.status='succeeded' AND job.status='succeeded' AND job.phase='succeeded'))
		  AND handoff.successor_component_count=(
		      SELECT COUNT(*) FROM generated_runtime_components c
		      WHERE c.deployment_id=a.successor_deployment_id AND c.state='active')
		  AND NOT EXISTS (
		      SELECT 1 FROM generated_runtime_components c
		      WHERE c.deployment_id=a.successor_deployment_id
		        AND (c.state<>'active' OR NOT EXISTS (
		            SELECT 1 FROM generated_candidate_intents i
		            WHERE i.deployment_id=c.deployment_id AND i.component_name=c.component_name)))`,
		activation.ReceiptID, activation.AppID, activation.SuccessorDeploymentID,
		activation.CreatorJobID, activation.CreatorWorkerAttempt,
		handoff.SuccessorComponentCount, handoff.PredecessorComponentCount,
		handoff.HandedOffAt.UTC().Format(time.RFC3339Nano), input.CurrentJobID,
		input.CurrentWorkerAttempt).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidTransition
	}
	return status, err
}

func validGeneratedRouteCompletionInput(input generatedruntimestate.RouteCompletionInput) bool {
	activation := input.Activation
	handoff := input.AccountingHandoff
	return uuid.Validate(activation.ReceiptID) == nil && uuid.Validate(activation.AppID) == nil &&
		uuid.Validate(activation.SuccessorDeploymentID) == nil && uuid.Validate(activation.CreatorJobID) == nil &&
		activation.CreatorWorkerAttempt > 0 && uuid.Validate(input.CurrentJobID) == nil &&
		input.CurrentJobID == activation.CreatorJobID && input.CurrentWorkerAttempt >= activation.CreatorWorkerAttempt &&
		!input.CompletedAt.IsZero() && handoff.ActivationReceiptID == activation.ReceiptID &&
		handoff.AppID == activation.AppID && handoff.SuccessorDeploymentID == activation.SuccessorDeploymentID &&
		handoff.PredecessorDeploymentID == activation.PredecessorDeploymentID &&
		handoff.CandidateRosterDigest == activation.CandidateRosterDigest &&
		handoff.PredecessorRosterDigest == activation.PredecessorRosterDigest && !handoff.HandedOffAt.IsZero()
}

func generatedRouteCompletionReceiptMatchesInput(receipt generatedruntimestate.RouteCompletionReceipt,
	input generatedruntimestate.RouteCompletionInput,
) bool {
	return receipt.ActivationReceiptID == input.Activation.ReceiptID && receipt.AppID == input.Activation.AppID &&
		receipt.SuccessorDeploymentID == input.Activation.SuccessorDeploymentID &&
		receipt.CreatorJobID == input.Activation.CreatorJobID &&
		receipt.CreatorWorkerAttempt == input.Activation.CreatorWorkerAttempt &&
		receipt.CurrentJobID == input.CurrentJobID &&
		receipt.CurrentWorkerAttempt == input.CurrentWorkerAttempt &&
		receipt.CompletionCode == "deployment_completed" && receipt.CompletedAt.Equal(input.CompletedAt)
}
