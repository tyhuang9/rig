package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const generatedRouteAccountingHandoffFinalizationTimeout = 30 * time.Second

type generatedRouteAccountingHandoffStage uint8

const (
	generatedRouteAccountingHandoffCreated generatedRouteAccountingHandoffStage = iota
	generatedRouteAccountingHandoffAuthorized
	generatedRouteAccountingHandoffTransferred
	generatedRouteAccountingHandoffFirstUnlocked
	generatedRouteAccountingHandoffPromoted
	generatedRouteAccountingHandoffPredecessorReleased
	generatedRouteAccountingHandoffReceiptObserved
	generatedRouteAccountingHandoffComplete
)

// generatedRouteAccountingHandoffOperation is cached only by the authentic
// same-process retirement state. Durable authorization is evidence and can
// neither construct this operation nor recover it after process loss.
type generatedRouteAccountingHandoffOperation struct {
	mu sync.Mutex

	state       *generatedPredecessorRetirementState
	successor   *candidateRosterOperation
	predecessor *candidateRetirementRosterOperation
	accounting  *candidateAccountingHandoffOperation

	stage         generatedRouteAccountingHandoffStage
	authorization generatedruntimestate.RouteAccountingHandoffAuthorization
	receipt       generatedruntimestate.RouteAccountingHandoffReceipt
	closed        bool
	rostersLeft   bool
}

func (a *GeneratedRouteAuthority) recoverAccountingHandoff(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	evidence generatedRecoveredRouteCompletionEvidence,
) (*candidateRecoveredAccountingHandoffHold, generatedruntimestate.RouteAccountingHandoffReceipt, error) {
	var empty generatedruntimestate.RouteAccountingHandoffReceipt
	if a == nil || pinned == nil || evidence.replay || !evidence.recoverHandoff ||
		evidence.activation.PredecessorDeploymentID == "" || len(evidence.receipts) == 0 ||
		len(evidence.predecessorReceipts) == 0 {
		return nil, empty, deploymentexecution.ErrInvalidConfiguration
	}
	activation, err := a.readRecoveredRouteActivation(ctx, evidence.activation)
	if err != nil {
		return nil, empty, err
	}
	hold, err := a.candidates.ledger.holdRecoveredAccountingHandoff(
		generatedDomainColdGroupSpec(a.candidates, evidence.group),
		generatedDomainColdGroupSpec(a.candidates, evidence.predecessorGroup),
		evidence.receipts, evidence.predecessorReceipts)
	if err != nil {
		return nil, empty, err
	}
	fail := func(cause error) (*candidateRecoveredAccountingHandoffHold,
		generatedruntimestate.RouteAccountingHandoffReceipt, error,
	) {
		hold.leave()
		return nil, empty, cause
	}

	release, tx, err := a.lockRecoveredAccountingHandoffProof(ctx, pinned, claim, activation,
		evidence, hold.validPrepared)
	if err != nil {
		return fail(err)
	}
	authorization, changed, err := a.runtime.RecordRouteAccountingHandoffAuthorizationTx(
		ctx, tx, activation, evidence.retirement, evidence.candidateRoster,
		evidence.predecessorRoster, time.Now().UTC())
	if err != nil || evidence.authorization.ActivationReceiptID == "" && !changed ||
		evidence.authorization.ActivationReceiptID != "" && (changed || authorization != evidence.authorization) {
		_ = tx.Rollback()
		return fail(a.releaseRecoveredAccountingHandoffLock(release,
			errors.Join(deploymentexecution.ErrIdentityLost, err)))
	}
	if err := tx.Commit(); err != nil {
		return fail(a.releaseRecoveredAccountingHandoffLock(release, gateDatabaseError(ctx, err)))
	}
	accounting, err := hold.beginAccountingHandoff()
	if err != nil {
		return fail(a.releaseRecoveredAccountingHandoffLock(release, err))
	}
	failTransferred := func(release func() error, cause error) (*candidateRecoveredAccountingHandoffHold,
		generatedruntimestate.RouteAccountingHandoffReceipt, error,
	) {
		if release != nil {
			if releaseErr := release(); releaseErr != nil {
				cause = &Error{Code: DiagnosticRouteUnresolved}
			}
		}
		a.manager.gatewayRebindFailStopLatch().Store(true)
		accounting.close()
		hold.leave()
		return nil, empty, cause
	}
	if err := release(); err != nil {
		return failTransferred(nil, &Error{Code: DiagnosticRouteUnresolved})
	}

	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		generatedRouteAccountingHandoffFinalizationTimeout)
	defer cancel()
	if err := accounting.markGatewayUnlocked(); err != nil {
		return failTransferred(nil, err)
	}
	if err := accounting.releasePredecessorOnce(); err != nil {
		return failTransferred(nil, err)
	}
	release, tx, err = a.lockRecoveredAccountingHandoffProof(finalCtx, pinned, claim, activation,
		evidence, func() bool { return accounting.validateFinalState() == nil })
	if err != nil {
		return failTransferred(nil, err)
	}
	handedOffAt := time.Now().UTC()
	expectedReceipt := recoveredAccountingHandoffReceipt(authorization, handedOffAt)
	receipt, changed, err := a.runtime.RecordRouteAccountingHandoffReceiptTx(
		finalCtx, tx, authorization, handedOffAt)
	if err == nil && (!changed || receipt != expectedReceipt || !hold.valid()) {
		err = deploymentexecution.ErrIdentityLost
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		receipt, err = a.retryRecoveredAccountingHandoffReceipt(finalCtx, pinned, claim, activation,
			evidence, hold, accounting, authorization, handedOffAt)
		if err != nil {
			return failTransferred(release, err)
		}
	}
	if err := release(); err != nil {
		return failTransferred(nil, &Error{Code: DiagnosticRouteUnresolved})
	}
	if !hold.valid() {
		return failTransferred(nil, deploymentexecution.ErrAuthorityStale)
	}
	return hold, receipt, nil
}

func (a *GeneratedRouteAuthority) lockRecoveredAccountingHandoffProof(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence, valid func() bool,
) (func() error, *sql.Tx, error) {
	if valid == nil || !valid() {
		return nil, nil, deploymentexecution.ErrAuthorityStale
	}
	release, err := a.manager.lockGateway(ctx)
	if err != nil {
		return nil, nil, err
	}
	fail := func(cause error) (func() error, *sql.Tx, error) {
		return nil, nil, a.releaseRecoveredAccountingHandoffLock(release, cause)
	}
	expected, err := a.readPublishedRouteAttestation(ctx, claim, activation.SuccessorDeploymentID)
	if err != nil {
		return fail(err)
	}
	observation, err := a.manager.observeGeneratedRetirementLocked(ctx, activation.AppID)
	if err != nil || !sameRoute(expected, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		return fail(errors.Join(deploymentexecution.ErrAuthorityStale, err))
	}
	if err := a.attestRecoveredAccountingHandoffPhysical(ctx, evidence); err != nil {
		return fail(err)
	}
	tx, err := a.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return fail(gateDatabaseError(ctx, err))
	}
	if err := a.validateRecoveredAccountingHandoffTx(ctx, tx, pinned, claim, activation,
		evidence, valid); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	return release, tx, nil
}

func (a *GeneratedRouteAuthority) attestRecoveredAccountingHandoffPhysical(ctx context.Context,
	evidence generatedRecoveredRouteCompletionEvidence,
) error {
	if a == nil || a.candidates == nil || len(evidence.group.snapshot.components) == 0 ||
		len(evidence.predecessorRoster) == 0 {
		return deploymentexecution.ErrIdentityLost
	}
	for _, component := range evidence.group.snapshot.components {
		policy, ok := evidence.group.policies[component.plan.Name]
		if !ok || component.containerID == "" {
			return deploymentexecution.ErrIdentityLost
		}
		if err := a.candidates.attestCandidateImage(ctx, policy); err != nil {
			return err
		}
		if err := a.candidates.attestCandidateNetwork(ctx, policy); err != nil {
			return err
		}
		expected := policy.identity
		expected.ContainerID = component.containerID
		observed, found, err := a.candidates.inspectCandidate(ctx, component.containerID)
		if err != nil || !found || !generatedcontainer.MatchesConfigured(observed, expected, policy.limits) ||
			!generatedcontainer.CurrentlyRunning(observed) {
			return errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
	}
	for _, component := range evidence.predecessorRoster {
		if component.ContainerID == "" {
			return deploymentexecution.ErrIdentityLost
		}
		if _, found, err := a.candidates.inspectCandidate(ctx, component.ContainerID); err != nil || found {
			return errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
	}
	return nil
}

func (a *GeneratedRouteAuthority) validateRecoveredAccountingHandoffTx(ctx context.Context,
	tx *sql.Tx, pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence, valid func() bool,
) error {
	if tx == nil || valid == nil || !valid() {
		return deploymentexecution.ErrAuthorityStale
	}
	if err := pinned.CheckGeneratedRouteRetirementTx(tx, claim, activation); err != nil ||
		!routeRetirementRosterMatchesTx(ctx, tx, evidence.candidateRoster,
			generatedruntimestate.ComponentActive, false) ||
		!routeRetirementRosterMatchesTx(ctx, tx, evidence.predecessorRoster,
			generatedruntimestate.ComponentStopped, false) || !valid() {
		return errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	return nil
}

func (a *GeneratedRouteAuthority) retryRecoveredAccountingHandoffReceipt(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence, hold *candidateRecoveredAccountingHandoffHold,
	accounting *candidateAccountingHandoffOperation,
	authorization generatedruntimestate.RouteAccountingHandoffAuthorization, handedOffAt time.Time,
) (generatedruntimestate.RouteAccountingHandoffReceipt, error) {
	var empty generatedruntimestate.RouteAccountingHandoffReceipt
	if accounting == nil || hold == nil || accounting.validateFinalState() != nil || !hold.valid() {
		return empty, deploymentexecution.ErrAuthorityStale
	}
	expected, err := a.readPublishedRouteAttestation(ctx, claim, activation.SuccessorDeploymentID)
	if err != nil {
		return empty, err
	}
	observation, err := a.manager.observeGeneratedRetirementLocked(ctx, activation.AppID)
	if err != nil || !sameRoute(expected, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		return empty, errors.Join(deploymentexecution.ErrAuthorityStale, err)
	}
	if err := a.attestRecoveredAccountingHandoffPhysical(ctx, evidence); err != nil {
		return empty, err
	}
	tx, err := a.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return empty, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if err := a.validateRecoveredAccountingHandoffTx(ctx, tx, pinned, claim, activation,
		evidence, func() bool { return accounting.validateFinalState() == nil && hold.valid() }); err != nil {
		return empty, err
	}
	receipt, _, err := a.runtime.RecordRouteAccountingHandoffReceiptTx(ctx, tx, authorization, handedOffAt)
	if err != nil || receipt != recoveredAccountingHandoffReceipt(authorization, handedOffAt) ||
		accounting.validateFinalState() != nil || !hold.valid() {
		return empty, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	if err := tx.Commit(); err != nil {
		return empty, gateDatabaseError(ctx, err)
	}
	return receipt, nil
}

func recoveredAccountingHandoffReceipt(authorization generatedruntimestate.RouteAccountingHandoffAuthorization,
	handedOffAt time.Time,
) generatedruntimestate.RouteAccountingHandoffReceipt {
	return generatedruntimestate.RouteAccountingHandoffReceipt{
		ActivationReceiptID: authorization.ActivationReceiptID, AppID: authorization.AppID,
		SuccessorDeploymentID:     authorization.SuccessorDeploymentID,
		PredecessorDeploymentID:   authorization.PredecessorDeploymentID,
		CandidateRosterDigest:     authorization.CandidateRosterDigest,
		PredecessorRosterDigest:   authorization.PredecessorRosterDigest,
		SuccessorComponentCount:   authorization.SuccessorComponentCount,
		PredecessorComponentCount: authorization.PredecessorComponentCount,
		HandedOffAt:               handedOffAt,
	}
}

func (a *GeneratedRouteAuthority) releaseRecoveredAccountingHandoffLock(release func() error,
	cause error,
) error {
	if release == nil {
		return cause
	}
	if err := release(); err != nil {
		if a != nil && a.manager != nil {
			a.manager.gatewayRebindFailStopLatch().Store(true)
		}
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return cause
}

// completeAccountingHandoff is deliberately package private and accepts only
// a context. Every identity, roster and durable receipt is derived from the
// already-borrowed retirement capability.
func (r *GeneratedPredecessorRetirement) completeAccountingHandoff(ctx context.Context) (
	generatedruntimestate.RouteAccountingHandoffReceipt, error,
) {
	state, candidate, done, err := r.begin()
	if err != nil {
		return generatedruntimestate.RouteAccountingHandoffReceipt{}, err
	}
	defer done()
	if ctx == nil {
		return generatedruntimestate.RouteAccountingHandoffReceipt{}, deploymentexecution.ErrInvalidConfiguration
	}

	operation, err := state.accountingHandoffOperation(candidate)
	if err != nil {
		return generatedruntimestate.RouteAccountingHandoffReceipt{}, err
	}
	return operation.complete(ctx, candidate)
}

func (s *generatedPredecessorRetirementState) accountingHandoffOperation(
	candidate *generatedCandidateExecutionState,
) (*generatedRouteAccountingHandoffOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.left || s.execution == nil || candidate == nil ||
		candidate != s.execution.candidate.state || s.retired == nil {
		return nil, deploymentexecution.ErrScopeUnavailable
	}
	if s.accounting != nil {
		return s.accounting, nil
	}

	successorReceipts := make(map[string]string, len(s.execution.candidateRoster))
	for _, component := range s.execution.candidateRoster {
		successorReceipts[component.ComponentName] = component.CandidateReceiptID
	}
	predecessorReceipts := make(map[string]string, len(s.execution.predecessorRoster))
	for _, component := range s.execution.predecessorRoster {
		predecessorReceipts[component.ComponentName] = component.CandidateReceiptID
	}
	if candidate.reservation == nil || candidate.reservation.capacity == nil ||
		candidate.reservation.capacity.ledger != s.execution.authority.candidates.ledger {
		return nil, deploymentexecution.ErrScopeUnavailable
	}

	var successor *candidateRosterOperation
	var predecessor *candidateRetirementRosterOperation
	var err error
	successorKey := candidateCapacityGroupKey{appID: s.receipt.AppID, deploymentID: s.receipt.SuccessorDeploymentID}
	predecessorKey := candidateCapacityGroupKey{appID: s.receipt.AppID, deploymentID: s.receipt.PredecessorDeploymentID}
	predecessorFirst := len(predecessorReceipts) > 0 && candidateCapacityGroupKeyLess(predecessorKey, successorKey)
	if predecessorFirst {
		predecessor, err = s.execution.authority.candidates.ledger.enterRetirementRoster(
			s.receipt.AppID, s.receipt.PredecessorDeploymentID, predecessorReceipts)
		if err == nil {
			successor, err = candidate.reservation.capacity.closeCleanupAndEnterRoster(successorReceipts)
		}
	} else {
		successor, err = candidate.reservation.capacity.closeCleanupAndEnterRoster(successorReceipts)
		if err == nil && len(predecessorReceipts) > 0 {
			predecessor, err = s.execution.authority.candidates.ledger.enterRetirementRoster(
				s.receipt.AppID, s.receipt.PredecessorDeploymentID, predecessorReceipts)
		}
	}
	if err != nil {
		if successor != nil {
			successor.leave()
		}
		if predecessor != nil {
			predecessor.leave()
		}
		return nil, err
	}
	operation := &generatedRouteAccountingHandoffOperation{
		state: s, successor: successor, predecessor: predecessor,
	}
	s.accounting = operation
	return operation, nil
}

func (o *generatedRouteAccountingHandoffOperation) complete(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) (generatedruntimestate.RouteAccountingHandoffReceipt, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.state == nil || o.state.execution == nil || candidate == nil ||
		candidate != o.state.execution.candidate.state {
		return generatedruntimestate.RouteAccountingHandoffReceipt{}, deploymentexecution.ErrScopeUnavailable
	}
	if o.stage == generatedRouteAccountingHandoffComplete {
		return o.receipt, nil
	}

	if o.stage < generatedRouteAccountingHandoffFirstUnlocked {
		if err := o.completeBeforeFirstUnlock(ctx, candidate); err != nil {
			return generatedruntimestate.RouteAccountingHandoffReceipt{}, err
		}
	}

	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
		generatedRouteAccountingHandoffFinalizationTimeout)
	defer cancel()
	if o.stage < generatedRouteAccountingHandoffPromoted {
		if err := o.accounting.markGatewayUnlocked(); err != nil {
			return generatedruntimestate.RouteAccountingHandoffReceipt{}, err
		}
		o.stage = generatedRouteAccountingHandoffPromoted
	}
	if o.stage < generatedRouteAccountingHandoffPredecessorReleased {
		if err := o.accounting.releasePredecessorOnce(); err != nil {
			return generatedruntimestate.RouteAccountingHandoffReceipt{}, err
		}
		o.stage = generatedRouteAccountingHandoffPredecessorReleased
	}
	if o.stage < generatedRouteAccountingHandoffReceiptObserved {
		if err := o.recordReceipt(finalCtx, candidate); err != nil {
			return generatedruntimestate.RouteAccountingHandoffReceipt{}, err
		}
	}
	o.stage = generatedRouteAccountingHandoffComplete
	o.leaveRostersLocked()
	return o.receipt, nil
}

func (o *generatedRouteAccountingHandoffOperation) completeBeforeFirstUnlock(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) error {
	release, tx, err := o.lockProofAndTransaction(ctx, candidate)
	if err != nil {
		return err
	}
	if o.stage < generatedRouteAccountingHandoffAuthorized {
		authorization, _, err := o.state.execution.authority.runtime.RecordRouteAccountingHandoffAuthorizationTx(
			ctx, tx, o.state.receipt, *o.state.retired, o.state.execution.candidateRoster,
			o.state.execution.predecessorRoster, time.Now().UTC())
		if err != nil {
			_ = tx.Rollback()
			return o.releaseFirstLock(release, err)
		}
		if err := tx.Commit(); err != nil {
			return o.releaseFirstLock(release, gateDatabaseError(ctx, err))
		}
		o.authorization = authorization
		o.stage = generatedRouteAccountingHandoffAuthorized
	} else {
		if err := tx.Commit(); err != nil {
			return o.releaseFirstLock(release, gateDatabaseError(ctx, err))
		}
	}

	if o.stage < generatedRouteAccountingHandoffTransferred {
		o.accounting, err = o.state.execution.authority.candidates.ledger.beginAccountingHandoff(
			o.successor, o.predecessor)
		if err != nil {
			return o.releaseFirstLock(release, err)
		}
		o.stage = generatedRouteAccountingHandoffTransferred
	}
	if err := ctx.Err(); err != nil {
		return o.releaseFirstLock(release, err)
	}
	if err := release(); err != nil {
		o.failStopLocked()
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	o.stage = generatedRouteAccountingHandoffFirstUnlocked
	return nil
}

func (o *generatedRouteAccountingHandoffOperation) recordReceipt(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) error {
	release, tx, err := o.lockProofAndTransaction(ctx, candidate)
	if err != nil {
		return err
	}
	if err := o.accounting.validateFinalState(); err != nil {
		_ = tx.Rollback()
		return o.releaseFinalLock(release, err)
	}
	receipt, _, err := o.state.execution.authority.runtime.RecordRouteAccountingHandoffReceiptTx(
		ctx, tx, o.authorization, time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		return o.releaseFinalLock(release, err)
	}
	if err := tx.Commit(); err != nil {
		return o.releaseFinalLock(release, gateDatabaseError(ctx, err))
	}
	o.receipt = receipt
	o.stage = generatedRouteAccountingHandoffReceiptObserved
	if err := release(); err != nil {
		o.failStopLocked()
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}

func (o *generatedRouteAccountingHandoffOperation) lockProofAndTransaction(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) (func() error, *sql.Tx, error) {
	manager := o.state.execution.authority.manager
	release, err := manager.lockGateway(ctx)
	if err != nil {
		return nil, nil, err
	}
	fail := func(cause error) (func() error, *sql.Tx, error) {
		if releaseErr := release(); releaseErr != nil {
			o.failStopLocked()
			return nil, nil, &Error{Code: DiagnosticRouteUnresolved}
		}
		return nil, nil, cause
	}
	observation, err := manager.observeGeneratedRetirementLocked(ctx, o.state.receipt.AppID)
	if err != nil || !routeObservationMatchesRetirement(observation, o.state.execution.request) {
		return fail(errors.Join(deploymentexecution.ErrAuthorityStale, err))
	}
	if err := o.state.attestSuccessor(ctx); err != nil {
		return fail(err)
	}
	for _, component := range o.state.execution.predecessorRoster {
		if _, found, inspectErr := o.state.execution.authority.candidates.inspectCandidate(ctx,
			component.ContainerID); inspectErr != nil || found {
			return fail(errors.Join(deploymentexecution.ErrIdentityLost, inspectErr))
		}
	}
	tx, err := o.state.execution.authority.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return fail(gateDatabaseError(ctx, err))
	}
	if err := o.state.validateRetirementTx(ctx, tx, candidate); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	return release, tx, nil
}

func (o *generatedRouteAccountingHandoffOperation) releaseFirstLock(release func() error, cause error) error {
	if releaseErr := release(); releaseErr != nil {
		o.failStopLocked()
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return cause
}

func (o *generatedRouteAccountingHandoffOperation) releaseFinalLock(release func() error, cause error) error {
	if releaseErr := release(); releaseErr != nil {
		o.failStopLocked()
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return cause
}

func (o *generatedRouteAccountingHandoffOperation) failStopLocked() {
	if o.state != nil && o.state.execution != nil && o.state.execution.authority != nil &&
		o.state.execution.authority.manager != nil {
		o.state.execution.authority.manager.gatewayRebindFailStopLatch().Store(true)
	}
	o.closed = true
	if o.accounting != nil {
		o.accounting.close()
	}
	o.leaveRostersLocked()
}

func (o *generatedRouteAccountingHandoffOperation) leaveRostersLocked() {
	if o.rostersLeft {
		return
	}
	o.rostersLeft = true
	if o.successor != nil {
		o.successor.leave()
	}
	if o.predecessor != nil {
		o.predecessor.leave()
	}
}

func (o *generatedRouteAccountingHandoffOperation) close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.closed = true
	if o.accounting != nil {
		o.accounting.close()
	}
	o.leaveRostersLocked()
	o.mu.Unlock()
}
