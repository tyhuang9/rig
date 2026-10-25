package generatedingress

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

type generatedRouteCompletionOperation struct {
	mu sync.Mutex

	state       *generatedPredecessorRetirementState
	handoff     *generatedRouteAccountingHandoffOperation
	handoffFact generatedruntimestate.RouteAccountingHandoffReceipt
	completedAt time.Time
	closed      bool
}

// Complete atomically records route completion through one authentic,
// process-local predecessor-retirement capability. Durable receipts are
// evidence only and cannot construct this authority after process loss.
func (r *GeneratedPredecessorRetirement) Complete(ctx context.Context) error {
	if r == nil || r.state == nil {
		return deploymentexecution.ErrScopeUnavailable
	}
	r.state.completionEntry.Lock()
	defer r.state.completionEntry.Unlock()
	state, candidate, done, err := r.begin()
	if err != nil {
		return err
	}
	defer done()
	if ctx == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	if _, err := state.retire(ctx, candidate); err != nil {
		return err
	}
	handoffOperation, err := state.accountingHandoffOperation(candidate)
	if err != nil {
		return err
	}
	handoff, err := handoffOperation.complete(ctx, candidate)
	if err != nil {
		return err
	}
	operation, err := state.completionOperation(candidate, handoff)
	if err != nil {
		return err
	}
	return operation.complete(ctx, candidate)
}

func (s *generatedPredecessorRetirementState) completionOperation(candidate *generatedCandidateExecutionState,
	handoff generatedruntimestate.RouteAccountingHandoffReceipt,
) (*generatedRouteCompletionOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.left || s.execution == nil || candidate == nil ||
		candidate != s.execution.candidate.state || s.retired == nil || s.accounting == nil {
		return nil, deploymentexecution.ErrScopeUnavailable
	}
	if _, err := s.accounting.completionAccountingProof(handoff); err != nil {
		return nil, err
	}
	if s.completion != nil {
		if s.completion.handoff != s.accounting || s.completion.handoffFact != handoff {
			return nil, deploymentexecution.ErrIdentityLost
		}
		return s.completion, nil
	}
	s.completion = &generatedRouteCompletionOperation{state: s, handoff: s.accounting,
		handoffFact: handoff, completedAt: time.Now().UTC()}
	return s.completion, nil
}

func (o *generatedRouteAccountingHandoffOperation) completionAccountingProof(
	want generatedruntimestate.RouteAccountingHandoffReceipt,
) (*candidateAccountingHandoffOperation, error) {
	if o == nil {
		return nil, deploymentexecution.ErrScopeUnavailable
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.stage != generatedRouteAccountingHandoffComplete || o.receipt != want ||
		o.accounting == nil {
		return nil, deploymentexecution.ErrScopeUnavailable
	}
	return o.accounting, nil
}

func (o *generatedRouteCompletionOperation) complete(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.state == nil || o.state.execution == nil || candidate == nil ||
		candidate != o.state.execution.candidate.state || ctx == nil || o.completedAt.IsZero() {
		return deploymentexecution.ErrScopeUnavailable
	}
	accounting, err := o.handoff.completionAccountingProof(o.handoffFact)
	if err != nil {
		return err
	}
	execution := o.state.execution
	claim := candidate.pins.claim
	input := generatedruntimestate.RouteCompletionInput{
		Activation: o.state.receipt, AccountingHandoff: o.handoffFact,
		CurrentJobID: claim.JobID(), CurrentWorkerAttempt: claim.Attempt(), CompletedAt: o.completedAt,
	}
	tx, err := execution.authority.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()

	existing, found, err := execution.authority.runtime.ReadRouteCompletionReceiptTx(
		ctx, tx, input.Activation.ReceiptID)
	if err != nil {
		return err
	}
	if found {
		if existing != routeCompletionReceipt(input) {
			return deploymentexecution.ErrIdentityLost
		}
		if err := candidate.pinned.CheckGeneratedRouteCompletionReplayTx(
			tx, claim, input.Activation, existing); err != nil {
			return err
		}
		if !routeRetirementRosterMatchesTx(ctx, tx, execution.candidateRoster,
			generatedruntimestate.ComponentActive, false) ||
			!routeRetirementRosterMatchesTx(ctx, tx, execution.predecessorRoster,
				generatedruntimestate.ComponentStopped, false) {
			return deploymentexecution.ErrIdentityLost
		}
	} else if err := o.state.validateRetirementTx(ctx, tx, candidate); err != nil {
		return err
	}
	if err := accounting.validateFinalState(); err != nil {
		return err
	}

	runtimeValue, runtimeChanged, err := execution.authority.runtime.RecordGeneratedRouteSucceededTx(ctx, tx, input)
	if err != nil {
		return err
	}
	mainValue, mainChanged, err := execution.authority.stores.Repository().RecordGeneratedRouteSucceededTx(ctx, tx, input)
	if err != nil {
		return err
	}
	receipt, receiptChanged, err := execution.authority.runtime.RecordRouteCompletionReceiptTx(ctx, tx, input)
	if err != nil {
		return err
	}
	jobResult, jobChanged, err := jobs.CompleteGeneratedRouteTx(ctx, tx, receipt)
	if err != nil {
		return err
	}
	wantChanged := !found
	if runtimeChanged != wantChanged || mainChanged != wantChanged || receiptChanged != wantChanged ||
		jobChanged != wantChanged || !exactGeneratedRouteCompletionResult(input, runtimeValue, mainValue,
		receipt, jobResult) {
		return deploymentexecution.ErrIdentityLost
	}
	if err := tx.Commit(); err != nil {
		commitErr := gateDatabaseError(ctx, err)
		if readErr := o.readCommittedCompletion(ctx, candidate, accounting, input); readErr == nil {
			return nil
		}
		return commitErr
	}
	return nil
}

// readCommittedCompletion resolves only an ambiguous completion transaction
// commit. It is deliberately read-only and is called while the original
// opaque-handle borrow and completion serialization remain live. Earlier
// retirement and accounting stages are never re-entered here.
func (o *generatedRouteCompletionOperation) readCommittedCompletion(ctx context.Context,
	candidate *generatedCandidateExecutionState, accounting *candidateAccountingHandoffOperation,
	input generatedruntimestate.RouteCompletionInput,
) error {
	execution := o.state.execution
	claim := candidate.pins.claim
	tx, err := execution.authority.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	receipt, found, err := execution.authority.runtime.ReadRouteCompletionReceiptTx(
		ctx, tx, input.Activation.ReceiptID)
	if err != nil || !found || receipt != routeCompletionReceipt(input) {
		return deploymentexecution.ErrIdentityLost
	}
	if err := candidate.pinned.CheckGeneratedRouteCompletionReplayTx(
		tx, claim, input.Activation, receipt); err != nil {
		return err
	}
	if !routeRetirementRosterMatchesTx(ctx, tx, execution.candidateRoster,
		generatedruntimestate.ComponentActive, false) ||
		!routeRetirementRosterMatchesTx(ctx, tx, execution.predecessorRoster,
			generatedruntimestate.ComponentStopped, false) {
		return deploymentexecution.ErrIdentityLost
	}
	if err := accounting.validateFinalState(); err != nil {
		return err
	}
	if err := exactGeneratedRouteCompletionEventTx(ctx, tx, input); err != nil {
		return err
	}
	return tx.Commit()
}

func exactGeneratedRouteCompletionEventTx(ctx context.Context, tx *sql.Tx,
	input generatedruntimestate.RouteCompletionInput,
) error {
	var jobID, jobType, resourceType, resourceID, status, phase, checkpoint, errorCode, errorDetail string
	var finishedAt, eventJobID, eventTimestamp, eventLevel, eventPhase, eventCode, eventMessage string
	var progress, attempt, eventAttempt, eventCount int
	err := tx.QueryRowContext(ctx, `SELECT job.id,job.type,job.resource_type,job.resource_id,
		job.status,job.phase,job.progress_percent,job.checkpoint_json,COALESCE(job.error_code,''),
		COALESCE(job.error_detail,''),job.attempt,COALESCE(job.finished_at,''),event.job_id,event.attempt,
		event.timestamp,event.level,event.phase,event.code,event.message,
		(SELECT COUNT(*) FROM job_events counted
		 WHERE counted.job_id=job.id AND counted.attempt=job.attempt AND counted.code='job_succeeded')
		FROM jobs job
		JOIN job_events event ON event.job_id=job.id AND event.attempt=job.attempt
		 AND event.code='job_succeeded'
		WHERE job.id=?`, input.CurrentJobID).Scan(&jobID, &jobType, &resourceType, &resourceID,
		&status, &phase, &progress, &checkpoint, &errorCode, &errorDetail, &attempt, &finishedAt,
		&eventJobID, &eventAttempt, &eventTimestamp, &eventLevel, &eventPhase, &eventCode,
		&eventMessage, &eventCount)
	if err != nil {
		return err
	}
	finished, finishErr := time.Parse(time.RFC3339Nano, finishedAt)
	eventTime, eventErr := time.Parse(time.RFC3339Nano, eventTimestamp)
	if finishErr != nil || eventErr != nil || jobID != input.CurrentJobID || jobType != "deploy" ||
		resourceType != "application" || resourceID != input.Activation.AppID || status != "succeeded" ||
		phase != "succeeded" || progress != 100 || checkpoint != `{"phase":"succeeded"}` ||
		errorCode != "" || errorDetail != "" || attempt != input.CurrentWorkerAttempt ||
		!finished.Equal(input.CompletedAt) || eventJobID != input.CurrentJobID ||
		eventAttempt != input.CurrentWorkerAttempt || !eventTime.Equal(input.CompletedAt) ||
		eventLevel != "info" || eventPhase != "succeeded" || eventCode != "job_succeeded" ||
		eventMessage != "Deployment completed" || eventCount != 1 {
		return deploymentexecution.ErrIdentityLost
	}
	return nil
}

func routeCompletionReceipt(input generatedruntimestate.RouteCompletionInput) generatedruntimestate.RouteCompletionReceipt {
	return generatedruntimestate.RouteCompletionReceipt{
		ActivationReceiptID: input.Activation.ReceiptID, AppID: input.Activation.AppID,
		SuccessorDeploymentID: input.Activation.SuccessorDeploymentID,
		CreatorJobID:          input.Activation.CreatorJobID,
		CreatorWorkerAttempt:  input.Activation.CreatorWorkerAttempt,
		CurrentJobID:          input.CurrentJobID, CurrentWorkerAttempt: input.CurrentWorkerAttempt,
		CompletionCode: "deployment_completed", CompletedAt: input.CompletedAt,
	}
}

func exactGeneratedRouteCompletionResult(input generatedruntimestate.RouteCompletionInput,
	runtime generatedruntimestate.Deployment, main deployments.Deployment,
	receipt generatedruntimestate.RouteCompletionReceipt, job jobs.GeneratedRouteCompletionResult,
) bool {
	return runtime.DeploymentID == input.Activation.SuccessorDeploymentID &&
		runtime.AppID == input.Activation.AppID && runtime.ReleaseID == input.Activation.SuccessorReleaseID &&
		runtime.CandidateSlot == input.Activation.SuccessorSlot && runtime.Phase == generatedruntimestate.PhaseSucceeded &&
		main.ID == input.Activation.SuccessorDeploymentID && main.AppID == input.Activation.AppID &&
		main.JobID == input.CurrentJobID && main.Status == deployments.Succeeded &&
		receipt == routeCompletionReceipt(input) && job.Job.ID == input.CurrentJobID &&
		job.Job.ResourceID == input.Activation.AppID && job.Job.Status == "succeeded" &&
		job.Job.Phase == "succeeded" && job.Job.Attempt == input.CurrentWorkerAttempt &&
		job.Event.JobID == input.CurrentJobID && job.Event.Attempt == input.CurrentWorkerAttempt &&
		job.Event.Level == "info" && job.Event.Phase == "succeeded" && job.Event.Code == "job_succeeded" &&
		job.Event.Message == "Deployment completed" && job.Event.Timestamp.Equal(input.CompletedAt)
}

func (o *generatedRouteCompletionOperation) close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.closed = true
	o.state = nil
	o.handoff = nil
	o.mu.Unlock()
}
