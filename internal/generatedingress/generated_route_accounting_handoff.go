package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
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
