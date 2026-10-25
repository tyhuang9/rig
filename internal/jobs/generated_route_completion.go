package jobs

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// GeneratedRouteCompletionResult is the exact terminal job and canonical
// event read back by CompleteGeneratedRouteTx.
type GeneratedRouteCompletionResult struct {
	Job   Job
	Event Event
}

const generatedRouteCompletionJobSelectColumns = `job.id,job.type,job.resource_type,job.resource_id,
	job.status,job.phase,job.progress_percent,job.checkpoint_json,COALESCE(job.error_code,''),
	COALESCE(job.error_detail,''),COALESCE(job.requested_by,''),COALESCE(job.pause_disposition,''),
	job.input_json,job.attempt,job.created_at,job.updated_at`

// CompleteGeneratedRouteTx performs the one purpose-specific exact-attempt
// terminal CAS. The caller owns the transaction and authentic route execution.
func CompleteGeneratedRouteTx(ctx context.Context, tx *sql.Tx,
	receipt generatedruntimestate.RouteCompletionReceipt,
) (GeneratedRouteCompletionResult, bool, error) {
	if ctx == nil || tx == nil || !validGeneratedRouteCompletionReceipt(receipt) {
		return GeneratedRouteCompletionResult{}, false, ErrInvalidInput
	}
	job, eventCount, nextSequence, err := generatedRouteCompletionJobTx(ctx, tx, receipt)
	if err != nil {
		return GeneratedRouteCompletionResult{}, false, err
	}
	if job.Status == string(Succeeded) {
		result, readErr := readGeneratedRouteCompletionResultTx(ctx, tx, receipt)
		if readErr != nil {
			return GeneratedRouteCompletionResult{}, false, readErr
		}
		return result, false, nil
	}
	if job.Status != string(Running) || job.Phase == "cancelling" || job.Attempt != receipt.CurrentWorkerAttempt {
		return GeneratedRouteCompletionResult{}, false, ErrJobTerminal
	}
	if err := validateGeneratedRouteCompletionCurrentTx(ctx, tx, receipt); err != nil {
		return GeneratedRouteCompletionResult{}, false, err
	}
	if eventCount >= maxJobEventsPerAttempt {
		return GeneratedRouteCompletionResult{}, false, ErrEventBudget
	}
	stamp := receipt.CompletedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE jobs
		SET status='succeeded',phase='succeeded',progress_percent=100,
		    checkpoint_json='{"phase":"succeeded"}',error_code=NULL,error_detail=NULL,
		    updated_at=?,started_at=COALESCE(started_at,?),finished_at=?
		WHERE id=? AND attempt=? AND status='running' AND phase<>'cancelling'`,
		stamp, stamp, stamp, receipt.CurrentJobID, receipt.CurrentWorkerAttempt)
	if err != nil {
		return GeneratedRouteCompletionResult{}, false, err
	}
	if count, countErr := result.RowsAffected(); countErr != nil || count != 1 {
		return GeneratedRouteCompletionResult{}, false, errors.Join(ErrJobTerminal, countErr)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO job_events(
		job_id,sequence,attempt,timestamp,level,phase,code,message
	) VALUES(?,?,?,?,?,?,?,?)`, receipt.CurrentJobID, nextSequence,
		receipt.CurrentWorkerAttempt, stamp, "info", "succeeded", "job_succeeded", "Deployment completed")
	if err != nil {
		return GeneratedRouteCompletionResult{}, false, err
	}
	value, err := readGeneratedRouteCompletionResultTx(ctx, tx, receipt)
	if err != nil {
		return GeneratedRouteCompletionResult{}, false, err
	}
	return value, true, nil
}

func validateGeneratedRouteCompletionCurrentTx(ctx context.Context, tx *sql.Tx,
	receipt generatedruntimestate.RouteCompletionReceipt,
) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1
		FROM generated_route_completion_receipts completion
		JOIN generated_route_activations activation
		  ON activation.id=completion.activation_receipt_id
		 AND activation.app_id=completion.app_id
		 AND activation.successor_deployment_id=completion.successor_deployment_id
		 AND activation.creator_job_id=completion.creator_job_id
		 AND activation.creator_worker_attempt=completion.creator_worker_attempt
		JOIN generated_route_accounting_handoff_receipts handoff
		  ON handoff.activation_receipt_id=activation.id
		 AND handoff.app_id=activation.app_id
		 AND handoff.successor_deployment_id=activation.successor_deployment_id
		 AND handoff.predecessor_deployment_id=activation.predecessor_deployment_id
		 AND handoff.candidate_roster_digest=activation.candidate_roster_digest
		 AND handoff.predecessor_roster_digest=activation.predecessor_roster_digest
		JOIN generated_runtime_active_heads head
		  ON head.app_id=activation.app_id
		 AND head.deployment_id=activation.successor_deployment_id
		 AND head.release_id=activation.successor_release_id
		 AND head.slot=activation.successor_slot
		 AND head.generation=activation.successor_generation
		 AND head.activation_receipt_id=activation.id
		JOIN generated_runtime_deployments runtime
		  ON runtime.deployment_id=activation.successor_deployment_id
		 AND runtime.app_id=activation.app_id
		 AND runtime.release_id=activation.successor_release_id
		 AND runtime.candidate_slot=activation.successor_slot
		 AND runtime.phase='succeeded'
		WHERE completion.activation_receipt_id=? AND completion.app_id=?
		  AND completion.successor_deployment_id=? AND completion.creator_job_id=?
		  AND completion.creator_worker_attempt=? AND completion.current_job_id=?
		  AND completion.current_worker_attempt=? AND completion.completion_code='deployment_completed'
		  AND completion.completed_at=?
		  AND handoff.successor_component_count=(
		      SELECT COUNT(*) FROM generated_runtime_components component
		      WHERE component.deployment_id=activation.successor_deployment_id
		        AND component.state='active')
		  AND NOT EXISTS (
		      SELECT 1 FROM generated_runtime_components component
		      WHERE component.deployment_id=activation.successor_deployment_id
		        AND (component.state<>'active' OR NOT EXISTS (
		            SELECT 1 FROM generated_candidate_intents intent
		            WHERE intent.deployment_id=component.deployment_id
		              AND intent.component_name=component.component_name)))
	)`, receipt.ActivationReceiptID, receipt.AppID, receipt.SuccessorDeploymentID,
		receipt.CreatorJobID, receipt.CreatorWorkerAttempt, receipt.CurrentJobID,
		receipt.CurrentWorkerAttempt, receipt.CompletedAt.UTC().Format(time.RFC3339Nano)).Scan(&exists)
	if err != nil {
		return err
	}
	if exists != 1 {
		return ErrInvalidInput
	}
	return nil
}

func generatedRouteCompletionJobTx(ctx context.Context, tx *sql.Tx,
	receipt generatedruntimestate.RouteCompletionReceipt,
) (Job, int, int, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+generatedRouteCompletionJobSelectColumns+`,
		(SELECT COUNT(*) FROM job_events e WHERE e.job_id=job.id AND e.attempt=job.attempt),
		(SELECT COALESCE(MAX(sequence),0)+1 FROM job_events e WHERE e.job_id=job.id)
		FROM generated_route_completion_receipts completion
		JOIN generated_route_accounting_handoff_receipts handoff
		  ON handoff.activation_receipt_id=completion.activation_receipt_id
		 AND handoff.app_id=completion.app_id
		 AND handoff.successor_deployment_id=completion.successor_deployment_id
		JOIN generated_route_activations activation
		  ON activation.id=completion.activation_receipt_id
		 AND activation.app_id=completion.app_id
		 AND activation.successor_deployment_id=completion.successor_deployment_id
		 AND activation.creator_job_id=completion.creator_job_id
		 AND activation.creator_worker_attempt=completion.creator_worker_attempt
		JOIN deployments deployment
		  ON deployment.id=completion.successor_deployment_id
		 AND deployment.app_id=completion.app_id
		 AND deployment.job_id=completion.current_job_id
		 AND deployment.status='succeeded'
		JOIN jobs job ON job.id=completion.current_job_id
		WHERE completion.activation_receipt_id=? AND completion.app_id=?
		  AND completion.successor_deployment_id=? AND completion.creator_job_id=?
		  AND completion.creator_worker_attempt=? AND completion.current_job_id=?
		  AND completion.current_worker_attempt=? AND completion.completion_code='deployment_completed'
		  AND completion.completed_at=? AND activation.creator_worker_attempt<=completion.current_worker_attempt`,
		receipt.ActivationReceiptID, receipt.AppID, receipt.SuccessorDeploymentID,
		receipt.CreatorJobID, receipt.CreatorWorkerAttempt, receipt.CurrentJobID,
		receipt.CurrentWorkerAttempt, receipt.CompletedAt.UTC().Format(time.RFC3339Nano))
	var job Job
	var input, created, updated string
	var eventCount, nextSequence int
	err := row.Scan(&job.ID, &job.Type, &job.ResourceType, &job.ResourceID, &job.Status,
		&job.Phase, &job.Progress, &job.Checkpoint, &job.ErrorCode, &job.ErrorDetail,
		&job.RequestedBy, &job.PauseDisposition, &input, &job.Attempt, &created, &updated,
		&eventCount, &nextSequence)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, 0, 0, ErrInvalidInput
	}
	if err != nil {
		return Job{}, 0, 0, err
	}
	job.Input = []byte(input)
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if job.ID != receipt.CurrentJobID || job.Type != "deploy" || job.ResourceType != "application" ||
		job.ResourceID != receipt.AppID || job.Attempt != receipt.CurrentWorkerAttempt {
		return Job{}, 0, 0, ErrInvalidInput
	}
	return job, eventCount, nextSequence, nil
}

func readGeneratedRouteCompletionResultTx(ctx context.Context, tx *sql.Tx,
	receipt generatedruntimestate.RouteCompletionReceipt,
) (GeneratedRouteCompletionResult, error) {
	job, _, _, err := generatedRouteCompletionJobTx(ctx, tx, receipt)
	if err != nil {
		return GeneratedRouteCompletionResult{}, err
	}
	if job.Status != string(Succeeded) || job.Phase != "succeeded" || job.Progress != 100 ||
		job.Checkpoint != `{"phase":"succeeded"}` || job.ErrorCode != "" || job.ErrorDetail != "" {
		return GeneratedRouteCompletionResult{}, ErrJobTerminal
	}
	var event Event
	var stamp string
	err = tx.QueryRowContext(ctx, `SELECT id,job_id,sequence,attempt,timestamp,level,phase,code,message
		FROM job_events WHERE job_id=? AND attempt=? AND level='info' AND phase='succeeded'
		  AND code='job_succeeded' AND message='Deployment completed'`,
		receipt.CurrentJobID, receipt.CurrentWorkerAttempt).Scan(&event.ID, &event.JobID,
		&event.Sequence, &event.Attempt, &stamp, &event.Level, &event.Phase, &event.Code, &event.Message)
	if err != nil {
		return GeneratedRouteCompletionResult{}, errors.Join(ErrJobTerminal, err)
	}
	event.Timestamp, err = time.Parse(time.RFC3339Nano, stamp)
	if err != nil || event.JobID != receipt.CurrentJobID || event.Attempt != receipt.CurrentWorkerAttempt ||
		!event.Timestamp.Equal(receipt.CompletedAt) {
		return GeneratedRouteCompletionResult{}, errors.Join(ErrJobTerminal, err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events
		WHERE job_id=? AND attempt=? AND code='job_succeeded'`, receipt.CurrentJobID,
		receipt.CurrentWorkerAttempt).Scan(&count); err != nil || count != 1 {
		return GeneratedRouteCompletionResult{}, errors.Join(ErrJobTerminal, err)
	}
	return GeneratedRouteCompletionResult{Job: job, Event: event}, nil
}

func validGeneratedRouteCompletionReceipt(receipt generatedruntimestate.RouteCompletionReceipt) bool {
	return uuid.Validate(receipt.ActivationReceiptID) == nil && uuid.Validate(receipt.AppID) == nil &&
		uuid.Validate(receipt.SuccessorDeploymentID) == nil && uuid.Validate(receipt.CreatorJobID) == nil &&
		receipt.CreatorWorkerAttempt > 0 && uuid.Validate(receipt.CurrentJobID) == nil &&
		receipt.CurrentJobID == receipt.CreatorJobID &&
		receipt.CurrentWorkerAttempt >= receipt.CreatorWorkerAttempt &&
		receipt.CompletionCode == "deployment_completed" && !receipt.CompletedAt.IsZero()
}
