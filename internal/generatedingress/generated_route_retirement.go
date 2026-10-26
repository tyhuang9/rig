package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// GeneratedPredecessorRetirement is an opaque, process-local child of one
// exact committed route activation. It has no public constructor or scalar
// proof input and cannot be reconstructed after process loss.
type GeneratedPredecessorRetirement struct {
	state *generatedPredecessorRetirementState
}

type generatedPredecessorRetirementState struct {
	completionEntry sync.Mutex
	mu              sync.Mutex
	changed         *sync.Cond
	active          int
	closing         bool
	left            bool

	execution           *GeneratedRouteExecution
	receipt             generatedruntimestate.RouteActivationReceipt
	successorPolicies   map[string]candidatePolicy
	predecessorPolicies map[string]candidatePolicy
	retired             *generatedruntimestate.PredecessorRetirementReceipt
	accounting          *generatedRouteAccountingHandoffOperation
	completion          *generatedRouteCompletionOperation
}

// recoverPredecessorRetirement completes only the fixed predecessor roster
// authenticated by one cold construction and one fresh scoped worker. The
// returned hold remains live for the immediately following accounting handoff.
func (a *GeneratedRouteAuthority) recoverPredecessorRetirement(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	evidence generatedRecoveredRouteCompletionEvidence,
) (*candidateRecoveredAccountingHandoffHold, generatedRecoveredRouteCompletionEvidence, error) {
	if a == nil || pinned == nil || ctx == nil || !evidence.recoverRetirement || evidence.replay ||
		evidence.activation.PredecessorDeploymentID == "" || len(evidence.predecessorRoster) == 0 ||
		len(evidence.predecessorReceipts) != len(evidence.predecessorRoster) ||
		len(evidence.predecessorPhysical) != len(evidence.predecessorRoster) ||
		a.waitRecoveredRetirement == nil {
		return nil, evidence, deploymentexecution.ErrInvalidConfiguration
	}
	activation, err := a.readRecoveredRouteActivation(ctx, evidence.activation)
	if err != nil {
		return nil, evidence, err
	}
	hold, err := a.candidates.ledger.holdRecoveredRetirementHandoff(
		generatedDomainColdGroupSpec(a.candidates, evidence.group),
		generatedDomainColdGroupSpec(a.candidates, evidence.predecessorGroup),
		evidence.receipts, evidence.predecessorReceipts, evidence.predecessorPhysical)
	if err != nil {
		return nil, evidence, err
	}
	fail := func(cause error) (*candidateRecoveredAccountingHandoffHold,
		generatedRecoveredRouteCompletionEvidence, error,
	) {
		hold.leave()
		return nil, evidence, cause
	}
	if !hold.validPrepared() {
		return fail(deploymentexecution.ErrAuthorityStale)
	}
	if err := a.waitRecoveredRetirement(ctx, maximumDrain); err != nil {
		return fail(err)
	}
	for _, component := range evidence.predecessorRoster {
		policy, ok := evidence.predecessorGroup.policies[component.ComponentName]
		if !ok || component.ContainerID == "" {
			return fail(deploymentexecution.ErrIdentityLost)
		}
		if evidence.predecessorPhysical[component.ComponentName] == candidatePhysicalConfirmedAbsent {
			absent, _, boundaryErr := a.recoveredRetirementComponentBoundary(ctx, pinned, claim,
				activation, evidence, hold, component, policy, true, false, false)
			if boundaryErr != nil || !absent {
				return fail(errors.Join(deploymentexecution.ErrIdentityLost, boundaryErr))
			}
			continue
		}
		absent, running, boundaryErr := a.recoveredRetirementComponentBoundary(ctx, pinned, claim,
			activation, evidence, hold, component, policy, false, false, true)
		if boundaryErr != nil {
			return fail(boundaryErr)
		}
		if absent {
			if err := hold.markRecoveredPredecessorAbsent(component.ComponentName); err != nil {
				return fail(err)
			}
			continue
		}
		if running {
			if err := hold.enterRecoveredRetirementCommand(component.ComponentName, candidateCommandStop); err != nil {
				return fail(err)
			}
			if err := a.runRecoveredRetirementCommand(ctx, hold, component.ComponentName,
				[]string{"container", "stop", "--time", "10", component.ContainerID}); err != nil {
				return fail(err)
			}
		}
		absent, _, boundaryErr = a.recoveredRetirementComponentBoundary(ctx, pinned, claim,
			activation, evidence, hold, component, policy, false, true, true)
		if boundaryErr != nil {
			return fail(boundaryErr)
		}
		if !absent {
			if err := hold.enterRecoveredRetirementCommand(component.ComponentName, candidateCommandRemove); err != nil {
				return fail(err)
			}
			if err := a.runRecoveredRetirementCommand(ctx, hold, component.ComponentName,
				[]string{"container", "rm", component.ContainerID}); err != nil {
				return fail(err)
			}
			absent, _, boundaryErr = a.recoveredRetirementComponentBoundary(ctx, pinned, claim,
				activation, evidence, hold, component, policy, true, false, true)
			if boundaryErr != nil || !absent {
				return fail(errors.Join(deploymentexecution.ErrIdentityLost, boundaryErr))
			}
		}
		if err := hold.markRecoveredPredecessorAbsent(component.ComponentName); err != nil {
			return fail(err)
		}
	}
	retirement, err := a.recordRecoveredPredecessorRetirement(ctx, pinned, claim, activation, evidence, hold)
	if err != nil {
		return fail(err)
	}
	evidence, err = a.refreshRecoveredRetirementHandoffEvidence(ctx, claim, evidence, retirement, hold)
	if err != nil {
		return fail(err)
	}
	return hold, evidence, nil
}

// refreshRecoveredRetirementHandoffEvidence authenticates the durable post-043
// transition without reusing the now-stale pre-retirement cold manifest. The
// same fresh Claim and continuously held restored rosters remain mandatory.
func (a *GeneratedRouteAuthority) refreshRecoveredRetirementHandoffEvidence(ctx context.Context,
	claim deploymentexecution.Claim, before generatedRecoveredRouteCompletionEvidence,
	retirement generatedruntimestate.PredecessorRetirementReceipt,
	hold *candidateRecoveredAccountingHandoffHold,
) (generatedRecoveredRouteCompletionEvidence, error) {
	if a == nil || a.construction == nil || a.construction.reservation == nil ||
		hold == nil || !hold.recoveredRetirementComplete() {
		return generatedRecoveredRouteCompletionEvidence{}, deploymentexecution.ErrAuthorityStale
	}
	input := a.construction.reservation.input
	observed, err := inspectGeneratedDomainStartupObservation(ctx, input.stores, input.runner, input.options)
	if err != nil {
		return generatedRecoveredRouteCompletionEvidence{}, err
	}
	groups, err := readGeneratedDomainColdGroups(ctx, a.candidates, observed)
	if err != nil {
		return generatedRecoveredRouteCompletionEvidence{}, err
	}
	after, err := recoveredRouteCompletionEvidence(observed, groups, claim,
		before.activation.SuccessorDeploymentID)
	if err != nil || after.replay || after.recoverRetirement || !after.recoverHandoff ||
		after.activation != before.activation || after.retirement != retirement ||
		!reflect.DeepEqual(after.candidateRoster, before.candidateRoster) ||
		!reflect.DeepEqual(after.predecessorRoster, before.predecessorRoster) ||
		!reflect.DeepEqual(after.receipts, before.receipts) ||
		!reflect.DeepEqual(after.predecessorReceipts, before.predecessorReceipts) ||
		!hold.recoveredRetirementComplete() {
		return generatedRecoveredRouteCompletionEvidence{}, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	return after, nil
}

func (a *GeneratedRouteAuthority) runRecoveredRetirementCommand(ctx context.Context,
	hold *candidateRecoveredAccountingHandoffHold, component string, args []string,
) error {
	command, err := a.candidates.prepareCandidateCommand(args)
	if err != nil {
		return err
	}
	result, runErr := a.candidates.process.Run(ctx, command)
	defer clearGeneratedCandidateResult(&result)
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		hold.latchRecoveredRetirementUnquiesced(component)
		return candidateFailure("process_termination_failed", runErr)
	}
	return candidateProcessFailure(ctx, result, runErr, "candidate_cleanup_failed")
}

func (a *GeneratedRouteAuthority) recoveredRetirementComponentBoundary(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence, hold *candidateRecoveredAccountingHandoffHold,
	component generatedruntimestate.RouteActivationComponent, policy candidatePolicy,
	requireAbsent, requireRemovable, recordAbsent bool,
) (absent, running bool, resultErr error) {
	release, err := a.manager.lockGateway(ctx)
	if err != nil {
		return false, false, err
	}
	defer releaseGatewayLock(release, &resultErr)
	if err := a.attestRecoveredRetirementRouteAndSuccessor(ctx, claim, activation, evidence); err != nil {
		return false, false, err
	}
	observed, found, err := a.candidates.inspectCandidate(ctx, component.ContainerID)
	if err != nil {
		return false, false, err
	}
	if found {
		expected := policy.identity
		expected.ContainerID = component.ContainerID
		if !generatedcontainer.MatchesConfigured(observed, expected, policy.limits) ||
			(requireRemovable && !candidateRemovableState(observed)) || requireAbsent {
			return false, false, deploymentexecution.ErrIdentityLost
		}
	}
	tx, err := a.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return false, false, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if err := a.validateRecoveredRetirementTx(ctx, tx, pinned, claim, activation, evidence, hold); err != nil {
		return false, false, err
	}
	if !found && recordAbsent {
		if _, _, err := a.runtime.RecordPredecessorRetirementComponentTx(
			ctx, tx, activation, component, time.Now().UTC()); err != nil {
			return false, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, false, gateDatabaseError(ctx, err)
	}
	return !found, found && generatedcontainer.CurrentlyRunning(observed), nil
}

func (a *GeneratedRouteAuthority) attestRecoveredRetirementRouteAndSuccessor(ctx context.Context,
	claim deploymentexecution.Claim, activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence,
) error {
	expected, err := a.readPublishedRouteAttestation(ctx, claim, activation.SuccessorDeploymentID)
	if err != nil {
		return err
	}
	observation, err := a.manager.observeGeneratedRetirementLocked(ctx, activation.AppID)
	if err != nil || !sameRoute(expected, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		return errors.Join(deploymentexecution.ErrAuthorityStale, err)
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
			!generatedcontainer.CurrentlyRunning(observed) || observed.Health != "healthy" {
			return errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
	}
	return nil
}

func (a *GeneratedRouteAuthority) validateRecoveredRetirementTx(ctx context.Context, tx *sql.Tx,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence, hold *candidateRecoveredAccountingHandoffHold,
) error {
	if hold == nil || !hold.validPrepared() {
		return deploymentexecution.ErrAuthorityStale
	}
	if err := pinned.CheckGeneratedRouteRetirementTx(tx, claim, activation); err != nil ||
		!routeRetirementRosterMatchesTx(ctx, tx, evidence.candidateRoster,
			generatedruntimestate.ComponentActive, false) ||
		!routeRetirementRosterMatchesTx(ctx, tx, evidence.predecessorRoster,
			generatedruntimestate.ComponentDraining, true) || !hold.validPrepared() {
		return errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	return nil
}

func (a *GeneratedRouteAuthority) recordRecoveredPredecessorRetirement(ctx context.Context,
	pinned *deploymentexecution.Pinned, claim deploymentexecution.Claim,
	activation generatedruntimestate.RouteActivationReceipt,
	evidence generatedRecoveredRouteCompletionEvidence, hold *candidateRecoveredAccountingHandoffHold,
) (result generatedruntimestate.PredecessorRetirementReceipt, resultErr error) {
	if hold == nil || !hold.recoveredRetirementComplete() {
		return result, deploymentexecution.ErrAuthorityStale
	}
	release, err := a.manager.lockGateway(ctx)
	if err != nil {
		return result, err
	}
	defer releaseGatewayLock(release, &resultErr)
	if err := a.attestRecoveredRetirementRouteAndSuccessor(ctx, claim, activation, evidence); err != nil {
		return result, err
	}
	for _, component := range evidence.predecessorRoster {
		if _, found, inspectErr := a.candidates.inspectCandidate(ctx, component.ContainerID); inspectErr != nil || found {
			return result, errors.Join(deploymentexecution.ErrIdentityLost, inspectErr)
		}
	}
	tx, err := a.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return result, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if err := a.validateRecoveredRetirementTx(ctx, tx, pinned, claim, activation, evidence, hold); err != nil {
		return result, err
	}
	result, _, err = a.runtime.RecordPredecessorRetirementReceiptTx(
		ctx, tx, activation, evidence.predecessorRoster, time.Now().UTC())
	if err != nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, gateDatabaseError(ctx, err)
	}
	return result, nil
}

func (e *GeneratedRouteExecution) mintPredecessorRetirement(ctx context.Context,
	result generatedRouteActivationResult, successor map[string]candidatePolicy,
) (*GeneratedPredecessorRetirement, error) {
	if e == nil || e.self != e || e.authority == nil || e.candidate == nil ||
		ctx == nil || len(successor) == 0 || !sameRouteRetirementActivation(e, result) {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	predecessor, err := e.authority.loadPredecessorRetirementPolicies(ctx, result.receipt, e.predecessorRoster)
	if err != nil {
		return nil, err
	}
	_, candidateDone, err := e.candidate.begin(false)
	if err != nil {
		return nil, err
	}
	candidateDone()
	state := &generatedPredecessorRetirementState{execution: e, receipt: result.receipt,
		successorPolicies: successor, predecessorPolicies: predecessor}
	state.changed = sync.NewCond(&state.mu)
	return &GeneratedPredecessorRetirement{state: state}, nil
}

// capturePredecessorRetirementSuccessor snapshots the already sealed candidate
// policy while the pre-activation stage still authorizes candidate observation.
// The snapshot cannot mint authority: minting remains contingent on the exact
// internally committed activation result returned later by activateCore.
func (e *GeneratedRouteExecution) capturePredecessorRetirementSuccessor() (map[string]candidatePolicy, error) {
	s, done, err := e.candidate.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	current, err := s.observeCandidateState(true)
	if err != nil || !routeRosterMatchesCandidateSnapshot(e.candidateRoster, current) {
		return nil, errors.Join(deploymentexecution.ErrIdentityLost, err)
	}
	successor := make(map[string]candidatePolicy, len(current.components))
	for _, member := range current.components {
		permit, permitErr := s.describeDurableCandidate(member)
		if permitErr != nil {
			return nil, permitErr
		}
		policy := permit.policy
		policy.identity.ContainerID = member.containerID
		successor[member.plan.Name] = policy
	}
	return successor, nil
}

func sameRouteRetirementActivation(e *GeneratedRouteExecution, result generatedRouteActivationResult) bool {
	candidate, candidateOK := canonicalGeneratedRouteActivationRoster(e.candidateRoster)
	predecessor, predecessorOK := canonicalGeneratedRouteActivationRoster(e.predecessorRoster)
	receipt := result.receipt
	return candidateOK && predecessorOK &&
		result.head.ActivationReceiptID == e.receiptID && receipt.ReceiptID == e.receiptID &&
		receipt.AppID == e.request.AppID && receipt.SuccessorDeploymentID == result.head.DeploymentID &&
		receipt.SuccessorReleaseID == result.head.ReleaseID && receipt.SuccessorSlot == result.head.Slot &&
		receipt.SuccessorGeneration == result.head.Generation &&
		receipt.PredecessorDeploymentID == e.expectedHead.DeploymentID &&
		receipt.PredecessorReleaseID == e.expectedHead.ReleaseID && receipt.PredecessorSlot == e.expectedHead.Slot &&
		receipt.PredecessorGeneration == e.expectedHead.Generation &&
		receipt.PredecessorActivationReceiptID == e.expectedHead.ActivationReceiptID &&
		receipt.CandidateRosterDigest == generatedRouteActivationRosterDigest(candidate) &&
		receipt.PredecessorRosterDigest == generatedRouteActivationRosterDigest(predecessor)
}

func (r *GeneratedPredecessorRetirement) current() error {
	_, _, done, err := r.begin()
	if err != nil {
		return err
	}
	done()
	return nil
}

func (r *GeneratedPredecessorRetirement) begin() (*generatedPredecessorRetirementState,
	*generatedCandidateExecutionState, func(), error,
) {
	if r == nil || r.state == nil {
		return nil, nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	state := r.state
	state.mu.Lock()
	if state.closing || state.left || state.execution == nil {
		state.mu.Unlock()
		return nil, nil, nil, deploymentexecution.ErrScopeUnavailable
	}
	state.active++
	state.mu.Unlock()
	leaveState := func() {
		state.mu.Lock()
		state.active--
		if state.active == 0 {
			state.changed.Broadcast()
		}
		state.mu.Unlock()
	}
	candidate, candidateDone, err := state.execution.candidate.begin(false)
	if err != nil {
		leaveState()
		return nil, nil, nil, err
	}
	done := func() {
		candidateDone()
		leaveState()
	}
	return state, candidate, done, nil
}

// Leave closes every alias after in-flight work joins. Candidate and Pinned
// ownership remain with the parent execution and are left by the executor.
func (r *GeneratedPredecessorRetirement) Leave() {
	if r == nil || r.state == nil {
		return
	}
	state := r.state
	state.mu.Lock()
	if state.left {
		state.mu.Unlock()
		return
	}
	state.closing = true
	for state.active > 0 {
		state.changed.Wait()
	}
	state.left = true
	completion := state.completion
	state.completion = nil
	accounting := state.accounting
	state.accounting = nil
	state.execution = nil
	state.successorPolicies = nil
	state.predecessorPolicies = nil
	state.changed.Broadcast()
	state.mu.Unlock()
	if completion != nil {
		completion.close()
	}
	if accounting != nil {
		accounting.close()
	}
}

// Retire removes the exact fixed predecessor roster. It retains all capacity
// charges and deliberately stops before the later accounting handoff.
func (r *GeneratedPredecessorRetirement) Retire(ctx context.Context) (
	generatedruntimestate.PredecessorRetirementReceipt, error,
) {
	state, candidate, done, err := r.begin()
	if err != nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, err
	}
	defer done()
	if ctx == nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, deploymentexecution.ErrInvalidConfiguration
	}
	return state.retire(ctx, candidate)
}

func (state *generatedPredecessorRetirementState) retire(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) (generatedruntimestate.PredecessorRetirementReceipt, error) {
	state.mu.Lock()
	if state.retired != nil {
		result := *state.retired
		state.mu.Unlock()
		return result, nil
	}
	state.mu.Unlock()
	if delay := state.execution.request.DrainPeriod; delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return generatedruntimestate.PredecessorRetirementReceipt{}, ctx.Err()
		}
	}

	receipts := make(map[string]string, len(state.execution.predecessorRoster))
	for _, component := range state.execution.predecessorRoster {
		receipts[component.ComponentName] = component.CandidateReceiptID
	}
	var roster *candidateRetirementRosterOperation
	var err error
	if len(receipts) > 0 {
		roster, err = state.execution.authority.candidates.ledger.enterRetirementRoster(
			state.receipt.AppID, state.receipt.PredecessorDeploymentID, receipts)
		if err != nil {
			return generatedruntimestate.PredecessorRetirementReceipt{}, err
		}
		defer roster.leave()
	}
	for _, component := range state.execution.predecessorRoster {
		if err := state.retireComponent(ctx, candidate, roster, component); err != nil {
			return generatedruntimestate.PredecessorRetirementReceipt{}, err
		}
	}
	result, err := state.recordAggregate(ctx, candidate)
	if err != nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, err
	}
	state.mu.Lock()
	state.retired = &result
	state.mu.Unlock()
	return result, nil
}

func (s *generatedPredecessorRetirementState) retireComponent(ctx context.Context,
	candidate *generatedCandidateExecutionState,
	roster *candidateRetirementRosterOperation, component generatedruntimestate.RouteActivationComponent,
) error {
	policy, ok := s.predecessorPolicies[component.ComponentName]
	if !ok {
		return deploymentexecution.ErrIdentityLost
	}
	absent, running, err := s.componentBoundary(ctx, candidate, component, policy, false, false)
	if err != nil {
		return err
	}
	if absent {
		return roster.markConfirmedAbsentRetained(component.ComponentName)
	}
	if running {
		if err := roster.enterCommand(component.ComponentName, candidateCommandStop); err != nil {
			return err
		}
		if err := s.runRetirementCommand(ctx, roster, component.ComponentName,
			[]string{"container", "stop", "--time", "10", component.ContainerID}); err != nil {
			return err
		}
	}
	absent, _, err = s.componentBoundary(ctx, candidate, component, policy, false, true)
	if err != nil {
		return err
	}
	if absent {
		return roster.markConfirmedAbsentRetained(component.ComponentName)
	}
	if err := roster.enterCommand(component.ComponentName, candidateCommandRemove); err != nil {
		return err
	}
	if err := s.runRetirementCommand(ctx, roster, component.ComponentName,
		[]string{"container", "rm", component.ContainerID}); err != nil {
		return err
	}
	absent, _, err = s.componentBoundary(ctx, candidate, component, policy, true, false)
	if err != nil {
		return err
	}
	if !absent {
		return deploymentexecution.ErrIdentityLost
	}
	return roster.markConfirmedAbsentRetained(component.ComponentName)
}

func (s *generatedPredecessorRetirementState) runRetirementCommand(ctx context.Context,
	roster *candidateRetirementRosterOperation, component string, args []string,
) error {
	command, err := s.execution.authority.candidates.prepareCandidateCommand(args)
	if err != nil {
		return err
	}
	result, runErr := s.execution.authority.candidates.process.Run(ctx, command)
	defer clearGeneratedCandidateResult(&result)
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		roster.latchMutationUnquiesced(component)
		return candidateFailure("process_termination_failed", runErr)
	}
	return candidateProcessFailure(ctx, result, runErr, "candidate_cleanup_failed")
}

func (s *generatedPredecessorRetirementState) componentBoundary(ctx context.Context,
	candidate *generatedCandidateExecutionState,
	component generatedruntimestate.RouteActivationComponent, policy candidatePolicy,
	requireAbsent, requireRemovable bool,
) (absent, running bool, resultErr error) {
	m := s.execution.authority.manager
	release, err := m.lockGateway(ctx)
	if err != nil {
		return false, false, err
	}
	defer releaseGatewayLock(release, &resultErr)
	observation, err := m.observeGeneratedRetirementLocked(ctx, s.receipt.AppID)
	if err != nil || !routeObservationMatchesRetirement(observation, s.execution.request) {
		return false, false, errors.Join(deploymentexecution.ErrAuthorityStale, err)
	}
	if err := s.attestSuccessor(ctx); err != nil {
		return false, false, err
	}
	observed, found, err := s.execution.authority.candidates.inspectCandidate(ctx, component.ContainerID)
	if err != nil {
		return false, false, err
	}
	if found {
		expected := policy.identity
		expected.ContainerID = component.ContainerID
		if !generatedcontainer.MatchesConfigured(observed, expected, policy.limits) ||
			(requireRemovable && !candidateRemovableState(observed)) {
			return false, false, deploymentexecution.ErrIdentityLost
		}
		if requireAbsent {
			return false, false, deploymentexecution.ErrIdentityLost
		}
	}
	tx, err := s.execution.authority.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return false, false, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if err := s.validateRetirementTx(ctx, tx, candidate); err != nil {
		return false, false, err
	}
	if !found {
		if _, _, err := s.execution.authority.runtime.RecordPredecessorRetirementComponentTx(
			ctx, tx, s.receipt, component, time.Now().UTC()); err != nil {
			return false, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, false, gateDatabaseError(ctx, err)
	}
	return !found, found && generatedcontainer.CurrentlyRunning(observed), nil
}

func (s *generatedPredecessorRetirementState) recordAggregate(ctx context.Context,
	candidate *generatedCandidateExecutionState,
) (result generatedruntimestate.PredecessorRetirementReceipt, resultErr error) {
	m := s.execution.authority.manager
	release, err := m.lockGateway(ctx)
	if err != nil {
		return result, err
	}
	defer releaseGatewayLock(release, &resultErr)
	observation, err := m.observeGeneratedRetirementLocked(ctx, s.receipt.AppID)
	if err != nil || !routeObservationMatchesRetirement(observation, s.execution.request) {
		return result, errors.Join(deploymentexecution.ErrAuthorityStale, err)
	}
	if err := s.attestSuccessor(ctx); err != nil {
		return result, err
	}
	for _, component := range s.execution.predecessorRoster {
		if _, found, inspectErr := s.execution.authority.candidates.inspectCandidate(ctx, component.ContainerID); inspectErr != nil || found {
			return result, errors.Join(deploymentexecution.ErrIdentityLost, inspectErr)
		}
	}
	tx, err := s.execution.authority.stores.Database().BeginTx(ctx, nil)
	if err != nil {
		return result, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if err := s.validateRetirementTx(ctx, tx, candidate); err != nil {
		return result, err
	}
	result, _, err = s.execution.authority.runtime.RecordPredecessorRetirementReceiptTx(
		ctx, tx, s.receipt, s.execution.predecessorRoster, time.Now().UTC())
	if err != nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.PredecessorRetirementReceipt{}, gateDatabaseError(ctx, err)
	}
	return result, nil
}

func (s *generatedPredecessorRetirementState) validateRetirementTx(ctx context.Context, tx *sql.Tx,
	candidate *generatedCandidateExecutionState,
) error {
	execution := s.execution
	if candidate == nil || candidate != execution.candidate.state || candidate.pinned == nil {
		return deploymentexecution.ErrScopeUnavailable
	}
	if err := candidate.pinned.CheckGeneratedRouteRetirementTx(tx, candidate.pins.claim, s.receipt); err != nil {
		return err
	}
	if !routeRetirementRosterMatchesTx(ctx, tx, execution.candidateRoster, generatedruntimestate.ComponentActive, false) ||
		!routeRetirementRosterMatchesTx(ctx, tx, execution.predecessorRoster, generatedruntimestate.ComponentDraining, true) {
		return deploymentexecution.ErrIdentityLost
	}
	return nil
}

func routeRetirementRosterMatchesTx(ctx context.Context, tx *sql.Tx,
	want []generatedruntimestate.RouteActivationComponent, state generatedruntimestate.ComponentState,
	allowStopped bool,
) bool {
	if len(want) == 0 {
		return true
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.deployment_id,c.component_name,c.slot,c.image_artifact_id,
		c.container_name,c.container_id,c.state,COALESCE(i.id,''),COALESCE(i.candidate_policy_digest,'')
		FROM generated_runtime_components c LEFT JOIN generated_candidate_intents i
		ON i.deployment_id=c.deployment_id AND i.component_name=c.component_name
		WHERE c.deployment_id=? ORDER BY c.component_name`, want[0].DeploymentID)
	if err != nil {
		return false
	}
	defer rows.Close()
	got := make([]generatedruntimestate.RouteActivationComponent, 0, len(want))
	for rows.Next() {
		var value generatedruntimestate.RouteActivationComponent
		var observedState generatedruntimestate.ComponentState
		if rows.Scan(&value.DeploymentID, &value.ComponentName, &value.Slot, &value.ImageArtifactID,
			&value.ContainerName, &value.ContainerID, &observedState, &value.CandidateReceiptID,
			&value.CandidatePolicyDigest) != nil || observedState != state && (!allowStopped || observedState != generatedruntimestate.ComponentStopped) {
			return false
		}
		got = append(got, value)
	}
	return rows.Err() == nil && reflect.DeepEqual(got, want)
}

func (s *generatedPredecessorRetirementState) attestSuccessor(ctx context.Context) error {
	names := make([]string, 0, len(s.successorPolicies))
	for name := range s.successorPolicies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		policy := s.successorPolicies[name]
		if err := s.execution.authority.candidates.attestCandidateImage(ctx, policy); err != nil {
			return err
		}
		if err := s.execution.authority.candidates.attestCandidateNetwork(ctx, policy); err != nil {
			return err
		}
		observed, found, err := s.execution.authority.candidates.inspectCandidate(ctx, policy.identity.ContainerID)
		if err != nil || !found || !generatedcontainer.MatchesConfigured(observed, policy.identity, policy.limits) ||
			!generatedcontainer.CurrentlyRunning(observed) || observed.Health != "healthy" {
			return errors.Join(deploymentexecution.ErrIdentityLost, err)
		}
	}
	return nil
}

func (m *Manager) observeGeneratedRetirementLocked(ctx context.Context, appID string) (Observation, error) {
	if current, handled, err := m.selectGatewayCurrentObservationLocked(ctx, appID); err != nil || handled {
		if err != nil {
			return Observation{}, err
		}
		return m.attestGatewayCurrentObservationLocked(ctx, current, appID)
	}
	_, state, journal, committed, err := m.committedV2Locked(ctx)
	if err != nil {
		return Observation{}, err
	}
	if committed {
		return m.observeCommittedV2Locked(ctx, state, journal, appID)
	}
	return m.observeLocked(ctx, appID)
}

func routeObservationMatchesRetirement(observation Observation, request generatedroute.RouteSwitchRequest) bool {
	got := routeRecord{Slot: observation.Slot, Endpoints: append([]generatedroute.RouteEndpoint(nil), observation.Endpoints...)}
	want := routeRecord{Slot: request.ToSlot, Endpoints: append([]generatedroute.RouteEndpoint(nil), request.Endpoints...)}
	return sameRoute(got, want)
}

func (a *GeneratedRouteAuthority) loadPredecessorRetirementPolicies(ctx context.Context,
	receipt generatedruntimestate.RouteActivationReceipt,
	roster []generatedruntimestate.RouteActivationComponent,
) (map[string]candidatePolicy, error) {
	result := make(map[string]candidatePolicy, len(roster))
	if len(roster) == 0 {
		return result, nil
	}
	var pins candidateValidationPins
	pins.cold = true
	err := a.stores.Database().QueryRowContext(ctx, `SELECT d.app_id,d.job_id,j.attempt,d.release_id,
		d.actual_configuration_revision_id,d.actual_configuration_revision_number,
		d.deployment_plan_revision_id,d.deployment_plan_revision_number,r.candidate_slot
		FROM deployments d JOIN jobs j ON j.id=d.job_id
		JOIN generated_runtime_deployments r ON r.deployment_id=d.id AND r.app_id=d.app_id
		WHERE d.id=? AND d.app_id=?`, receipt.PredecessorDeploymentID, receipt.AppID).Scan(
		&pins.identity.applicationID, &pins.identity.jobID, &pins.identity.attempt, &pins.releaseID,
		&pins.configurationRevisionID, &pins.configurationRevisionNumber,
		&pins.deploymentPlanRevisionID, &pins.deploymentPlanRevisionNumber, &pins.candidateSlot)
	if err != nil {
		return nil, gateDatabaseError(ctx, err)
	}
	pins.deploymentID = receipt.PredecessorDeploymentID
	if err := validateCandidateIdentityPins(pins); err != nil {
		return nil, err
	}
	protected, err := a.candidates.loadCandidateProtectedSnapshotIdentity(ctx, pins)
	if err != nil {
		return nil, err
	}
	tx, err := a.stores.Database().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	snapshot, err := readCandidateAdmissionSnapshotIdentityTx(ctx, tx, pins, protected)
	if err != nil {
		return nil, err
	}
	pins.findings = append([]Finding(nil), snapshot.persistedFindings...)
	if snapshot.runtime.phase != generatedruntimestate.PhaseSucceeded || snapshot.main.status != Succeeded ||
		len(snapshot.components) != len(roster) || validateCandidatePolicyTx(ctx, a.stores, tx, pins, snapshot, true) != nil {
		return nil, deploymentexecution.ErrIdentityLost
	}
	for index, member := range snapshot.components {
		if member.artifact == nil || member.receipt == nil || index >= len(roster) ||
			member.plan.Name != roster[index].ComponentName || member.containerID != roster[index].ContainerID ||
			member.containerName != roster[index].ContainerName || member.receipt.id != roster[index].CandidateReceiptID ||
			member.receipt.policyDigest != roster[index].CandidatePolicyDigest {
			return nil, deploymentexecution.ErrIdentityLost
		}
		policy, err := buildCandidatePolicy(pins, member.plan, *member.artifact, *member.receipt, a.candidates.options)
		if err != nil {
			return nil, err
		}
		policy.identity.ContainerID = member.containerID
		result[member.plan.Name] = policy
	}
	if err := tx.Commit(); err != nil {
		return nil, gateDatabaseError(ctx, err)
	}
	return result, nil
}
