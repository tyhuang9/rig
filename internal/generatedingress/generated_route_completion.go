package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

type generatedRecoveredRouteCompletionEvidence struct {
	group             generatedDomainColdGroup
	activation        generatedruntimestate.GeneratedDomainStartupRouteActivation
	handoff           generatedruntimestate.RouteAccountingHandoffReceipt
	completion        generatedruntimestate.RouteCompletionReceipt
	candidateRoster   []generatedruntimestate.RouteActivationComponent
	predecessorRoster []generatedruntimestate.RouteActivationComponent
	receipts          map[string]string
	replay            bool
}

// CompleteRecovered completes only the exact interrupted Draining handoff
// reconstructed by this cold GeneratedDomain. Durable receipts remain
// descriptive: the live Claim, sealed cold construction, restored accounting
// roster and fresh physical observation are all required for authority.
func (a *GeneratedRouteAuthority) CompleteRecovered(ctx context.Context,
	claim deploymentexecution.Claim, deploymentID string,
) error {
	if !a.configured() || ctx == nil || claim.ApplicationID() == "" || !validAppID(deploymentID) ||
		a.construction == nil || !a.configuredFor(a.construction) || a.construction.cold == nil ||
		a.construction.manifest == nil ||
		!sameGeneratedDomainColdManifest(a.construction.cold, a.construction.manifest.cold) {
		return deploymentexecution.ErrInvalidConfiguration
	}
	pinned, err := a.stores.ExecutionGuard().PinScope(ctx)
	if err != nil {
		return err
	}
	defer pinned.Leave()

	construction := a.construction
	input := construction.reservation.input
	observed, err := inspectGeneratedDomainStartupObservation(ctx, input.stores, input.runner, input.options)
	if err != nil {
		return err
	}
	if err := reattestGeneratedDomainCold(ctx, construction.manager, construction.candidates,
		observed, *construction.cold); err != nil {
		return err
	}
	groups, err := readGeneratedDomainColdGroups(ctx, construction.candidates, observed)
	if err != nil {
		return err
	}
	evidence, err := recoveredRouteCompletionEvidence(observed, groups, claim, deploymentID)
	if err != nil {
		return err
	}
	hold, err := construction.candidates.ledger.holdRecoveredCompletionRoster(
		generatedDomainColdGroupSpec(construction.candidates, evidence.group), evidence.receipts)
	if err != nil {
		return err
	}
	defer hold.leave()
	if !hold.valid() {
		return deploymentexecution.ErrAuthorityStale
	}
	if evidence.replay {
		activation, err := a.readRecoveredRouteActivation(ctx, evidence.activation)
		if err != nil {
			return err
		}
		if err := a.attestRecoveredRouteCompletion(ctx, pinned, claim, activation,
			evidence.completion); err != nil {
			return err
		}
	} else if err := a.AttestPublished(ctx, claim, deploymentID); err != nil {
		return err
	}
	return a.completeRecovered(ctx, pinned, claim, evidence, hold)
}

func (a *GeneratedRouteAuthority) readRecoveredRouteActivation(ctx context.Context,
	observed generatedruntimestate.GeneratedDomainStartupRouteActivation,
) (generatedruntimestate.RouteActivationReceipt, error) {
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return generatedruntimestate.RouteActivationReceipt{}, err
	}
	defer tx.Rollback()
	activation, err := readGeneratedRouteActivationReceiptTx(ctx, tx, observed.ReceiptID)
	if err != nil || !sameRecoveredRouteActivation(activation, observed) {
		return generatedruntimestate.RouteActivationReceipt{}, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.RouteActivationReceipt{}, err
	}
	return activation, nil
}

func recoveredRouteCompletionEvidence(observed generatedDomainStartupObservation,
	groups []generatedDomainColdGroup, claim deploymentexecution.Claim, deploymentID string,
) (generatedRecoveredRouteCompletionEvidence, error) {
	var result generatedRecoveredRouteCompletionEvidence
	groupCount := 0
	for _, group := range groups {
		if group.pins.applicationID() == claim.ApplicationID() && group.pins.deploymentID == deploymentID {
			result.group, groupCount = group, groupCount+1
		}
	}
	if groupCount != 1 || result.group.pins.jobID() != claim.JobID() ||
		claim.Attempt() < result.group.pins.attempt() {
		return result, deploymentexecution.ErrIdentityLost
	}
	restored, err := generatedDomainColdDrainingDebtEvidence(observed.database, result.group, groups)
	if err != nil {
		return result, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	if !restored {
		completed, completedErr := generatedDomainColdCurrentDebtEvidence(observed.database, result.group, groups)
		if completedErr != nil || !completed ||
			result.group.snapshot.runtime.phase != generatedruntimestate.PhaseSucceeded {
			return result, errors.Join(deploymentexecution.ErrIdentityLost, completedErr)
		}
		result.replay = true
	}
	receiptID := result.group.snapshot.observedHead.activationReceiptID
	activationCount := 0
	for _, activation := range observed.database.Runtime.RouteActivations {
		if activation.ReceiptID == receiptID && activation.AppID == claim.ApplicationID() &&
			activation.SuccessorDeploymentID == deploymentID {
			result.activation, activationCount = activation, activationCount+1
		}
	}
	handoffCount := 0
	for _, handoff := range observed.database.Runtime.AccountingHandoffReceipts {
		if handoff.ActivationReceiptID == receiptID && handoff.AppID == claim.ApplicationID() &&
			handoff.SuccessorDeploymentID == deploymentID {
			result.handoff, handoffCount = handoff, handoffCount+1
		}
	}
	completionCount := 0
	for _, completion := range observed.database.Runtime.RouteCompletionReceipts {
		if completion.ActivationReceiptID == receiptID && completion.AppID == claim.ApplicationID() &&
			completion.SuccessorDeploymentID == deploymentID {
			result.completion, completionCount = completion, completionCount+1
		}
	}
	if result.replay {
		if completionCount != 1 || result.completion.CurrentJobID != claim.JobID() ||
			result.completion.CurrentWorkerAttempt != claim.Attempt() {
			return result, deploymentexecution.ErrIdentityLost
		}
	} else if completionCount != 0 {
		return result, deploymentexecution.ErrIdentityLost
	}
	result.candidateRoster, _ = generatedDomainColdActivationRoster(result.group.snapshot)
	if result.activation.PredecessorDeploymentID != "" {
		predecessor, found := generatedDomainColdGroupByDeployment(groups,
			result.activation.PredecessorDeploymentID)
		if !found {
			return result, deploymentexecution.ErrIdentityLost
		}
		result.predecessorRoster, _ = generatedDomainColdActivationRoster(predecessor.snapshot)
	} else {
		result.predecessorRoster = []generatedruntimestate.RouteActivationComponent{}
	}
	if activationCount != 1 || handoffCount != 1 || len(result.candidateRoster) == 0 ||
		(result.activation.PredecessorDeploymentID != "" && len(result.predecessorRoster) == 0) {
		return result, deploymentexecution.ErrIdentityLost
	}
	result.receipts = make(map[string]string, len(result.group.receipts))
	for name, receipt := range result.group.receipts {
		if name == "" || receipt.id == "" || receipt.physical != candidatePhysicalHandedToRoute || receipt.unknown {
			return generatedRecoveredRouteCompletionEvidence{}, deploymentexecution.ErrIdentityLost
		}
		result.receipts[name] = receipt.id
	}
	if len(result.receipts) != len(result.candidateRoster) {
		return generatedRecoveredRouteCompletionEvidence{}, deploymentexecution.ErrIdentityLost
	}
	return result, nil
}

func (a *GeneratedRouteAuthority) completeRecovered(ctx context.Context, pinned *deploymentexecution.Pinned,
	claim deploymentexecution.Claim, evidence generatedRecoveredRouteCompletionEvidence,
	hold *candidateRecoveredCompletionHold,
) error {
	tx, err := a.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	activation, err := readGeneratedRouteActivationReceiptTx(ctx, tx, evidence.activation.ReceiptID)
	if err != nil || !sameRecoveredRouteActivation(activation, evidence.activation) {
		return errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	input := generatedruntimestate.RouteCompletionInput{Activation: activation,
		AccountingHandoff: evidence.handoff, CurrentJobID: claim.JobID(),
		CurrentWorkerAttempt: claim.Attempt(), CompletedAt: time.Now().UTC()}
	existing, found, err := a.runtime.ReadRouteCompletionReceiptTx(ctx, tx, activation.ReceiptID)
	if err != nil {
		return err
	}
	if found {
		input.CompletedAt = existing.CompletedAt
		if existing != routeCompletionReceipt(input) {
			return deploymentexecution.ErrIdentityLost
		}
		if err := pinned.CheckGeneratedRouteCompletionReplayTx(tx, claim, activation, existing); err != nil {
			return err
		}
		if err := a.runtime.ValidateRouteCompletionReceiptsTx(ctx, tx, input); err != nil {
			return err
		}
		if !routeRetirementRosterMatchesTx(ctx, tx, evidence.candidateRoster,
			generatedruntimestate.ComponentActive, false) ||
			!routeRetirementRosterMatchesTx(ctx, tx, evidence.predecessorRoster,
				generatedruntimestate.ComponentStopped, false) || !hold.valid() ||
			exactGeneratedRouteCompletionEventTx(ctx, tx, input) != nil {
			return deploymentexecution.ErrIdentityLost
		}
		return tx.Commit()
	}
	if err := a.stores.ExecutionGuard().CheckPublishedRouteAttestationTx(ctx, tx, claim,
		activation.SuccessorDeploymentID); err != nil {
		return err
	}
	if err := a.runtime.ValidateRouteCompletionReceiptsTx(ctx, tx, input); err != nil {
		return err
	}
	if !routeRetirementRosterMatchesTx(ctx, tx, evidence.candidateRoster,
		generatedruntimestate.ComponentActive, false) ||
		!routeRetirementRosterMatchesTx(ctx, tx, evidence.predecessorRoster,
			generatedruntimestate.ComponentStopped, false) || !hold.valid() {
		return deploymentexecution.ErrIdentityLost
	}
	runtimeValue, runtimeChanged, err := a.runtime.RecordGeneratedRouteSucceededTx(ctx, tx, input)
	if err != nil {
		return err
	}
	mainValue, mainChanged, err := a.stores.Repository().RecordGeneratedRouteSucceededTx(ctx, tx, input)
	if err != nil {
		return err
	}
	receipt, receiptChanged, err := a.runtime.RecordRouteCompletionReceiptTx(ctx, tx, input)
	if err != nil {
		return err
	}
	jobResult, jobChanged, err := jobs.CompleteGeneratedRouteTx(ctx, tx, receipt)
	if err != nil {
		return err
	}
	if !runtimeChanged || !mainChanged || !receiptChanged || !jobChanged || !hold.valid() ||
		!exactGeneratedRouteCompletionResult(input, runtimeValue, mainValue, receipt, jobResult) {
		return deploymentexecution.ErrIdentityLost
	}
	if err := tx.Commit(); err != nil {
		commitErr := gateDatabaseError(ctx, err)
		if readErr := a.readRecoveredCompletion(ctx, pinned, claim, evidence, hold, input); readErr == nil {
			return nil
		}
		return commitErr
	}
	return nil
}

func (a *GeneratedRouteAuthority) readRecoveredCompletion(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	evidence generatedRecoveredRouteCompletionEvidence, hold *candidateRecoveredCompletionHold,
	input generatedruntimestate.RouteCompletionInput,
) error {
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	receipt, found, err := a.runtime.ReadRouteCompletionReceiptTx(ctx, tx, input.Activation.ReceiptID)
	if err != nil {
		return err
	}
	if !found || receipt != routeCompletionReceipt(input) {
		return deploymentexecution.ErrIdentityLost
	}
	if err := pinned.CheckGeneratedRouteCompletionReplayTx(tx, claim, input.Activation, receipt); err != nil {
		return err
	}
	if err := a.runtime.ValidateRouteCompletionReceiptsTx(ctx, tx, input); err != nil {
		return err
	}
	if !routeRetirementRosterMatchesTx(ctx, tx, evidence.candidateRoster,
		generatedruntimestate.ComponentActive, false) ||
		!routeRetirementRosterMatchesTx(ctx, tx, evidence.predecessorRoster,
			generatedruntimestate.ComponentStopped, false) || !hold.valid() ||
		exactGeneratedRouteCompletionEventTx(ctx, tx, input) != nil {
		return deploymentexecution.ErrIdentityLost
	}
	return tx.Commit()
}

func sameRecoveredRouteActivation(value generatedruntimestate.RouteActivationReceipt,
	observed generatedruntimestate.GeneratedDomainStartupRouteActivation,
) bool {
	return value.ReceiptID == observed.ReceiptID && value.AppID == observed.AppID &&
		value.SuccessorDeploymentID == observed.SuccessorDeploymentID &&
		value.SuccessorReleaseID == observed.SuccessorReleaseID &&
		value.SuccessorSlot == observed.SuccessorSlot &&
		value.SuccessorGeneration == observed.SuccessorGeneration &&
		value.PredecessorDeploymentID == observed.PredecessorDeploymentID &&
		value.PredecessorReleaseID == observed.PredecessorReleaseID &&
		value.PredecessorSlot == observed.PredecessorSlot &&
		value.PredecessorGeneration == observed.PredecessorGeneration &&
		value.PredecessorActivationReceiptID == observed.PredecessorActivationReceiptID &&
		value.CreatorJobID == observed.CreatorJobID &&
		value.CreatorWorkerAttempt == observed.CreatorWorkerAttempt &&
		value.CandidateRosterDigest == observed.CandidateRosterDigest &&
		value.PredecessorRosterDigest == observed.PredecessorRosterDigest &&
		value.RouteIntentDigest == observed.RouteIntentDigest &&
		value.CommittedRouteProofDigest == observed.CommittedRouteProofDigest && !value.ActivatedAt.IsZero()
}

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
