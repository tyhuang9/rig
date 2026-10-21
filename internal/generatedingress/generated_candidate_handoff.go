package generatedingress

import (
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func (e *GeneratedCandidateExecution) FinishStarting() (generatedruntimestate.Deployment, error) {
	s, done, err := e.begin(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer done()
	current, err := s.observeCandidateState(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate || current.main.status != Applying {
		return generatedruntimestate.Deployment{}, deploymentexecution.ErrStageInvalid
	}
	operations := make([]*candidateComponentOperation, 0, len(current.components))
	defer func() {
		for _, operation := range operations {
			operation.leave()
		}
	}()
	for _, member := range current.components {
		if member.state != generatedruntimestate.ComponentRunning || member.receipt == nil {
			return generatedruntimestate.Deployment{}, deploymentexecution.ErrStageInvalid
		}
		operation, err := s.reservation.capacity.enterComponent(member.plan.Name, member.receipt.id, candidateOperationMutate)
		if err != nil {
			return generatedruntimestate.Deployment{}, err
		}
		operations = append(operations, operation)
		p, err := s.describeDurableCandidate(member)
		if err != nil {
			return generatedruntimestate.Deployment{}, err
		}
		observed, found, err := s.authority.inspectCandidate(s.ctx, p.containerID)
		if err != nil {
			return generatedruntimestate.Deployment{}, err
		}
		if !found || !p.matchesRunning(observed) {
			return generatedruntimestate.Deployment{}, candidateFailure("candidate_start_failed", nil)
		}
		if err := operation.markPresent(); err != nil {
			return generatedruntimestate.Deployment{}, err
		}
	}
	tx, latest, err := s.candidateWriter(candidateBoundaryResult, false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer tx.Rollback()
	if !sameCandidateObservedRoster(current, latest, generatedruntimestate.ComponentRunning) || latest.runtime.phase != generatedruntimestate.PhaseStartingCandidate {
		return generatedruntimestate.Deployment{}, deploymentexecution.ErrIdentityLost
	}
	state := generatedruntimestate.New(s.authority.stores.Database())
	if err := state.FinishCandidateStartingTx(s.ctx, tx, s.pins.claim.ApplicationID(), s.pins.deploymentID); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.Deployment{}, gateDatabaseError(s.ctx, err)
	}
	return state.Get(s.ctx, s.pins.claim.ApplicationID(), s.pins.deploymentID)
}

func (e *GeneratedCandidateExecution) FinishHealth() (generatedruntimestate.Deployment, error) {
	s, done, err := e.begin(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer done()
	current, err := s.observeCandidateState(false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if current.runtime.phase != generatedruntimestate.PhaseWaitingHealth || current.main.status != WaitingHealth {
		return generatedruntimestate.Deployment{}, deploymentexecution.ErrStageInvalid
	}
	receipts := make(map[string]string, len(current.components))
	for _, member := range current.components {
		if member.state != generatedruntimestate.ComponentHealthy || member.receipt == nil {
			return generatedruntimestate.Deployment{}, deploymentexecution.ErrStageInvalid
		}
		receipts[member.plan.Name] = member.receipt.id
	}
	// Closing and joining cleanup precedes acquisition of roster tokens and
	// every physical observation. The latch survives all later failures.
	roster, err := s.reservation.capacity.closeCleanupAndEnterRoster(receipts)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer roster.leave()
	for _, member := range current.components {
		p, err := s.describeDurableCandidate(member)
		if err != nil {
			return generatedruntimestate.Deployment{}, err
		}
		observed, found, err := s.authority.inspectCandidate(s.ctx, p.containerID)
		if err != nil {
			return generatedruntimestate.Deployment{}, err
		}
		if !found || !p.matchesRunning(observed) || observed.Health != "healthy" {
			return generatedruntimestate.Deployment{}, candidateFailure("candidate_unhealthy", nil)
		}
	}
	// Mark accounting before opening SQL; a failed or ambiguous handoff
	// transaction must never reopen cleanup or make these shares releasable.
	if err := roster.markHandedToRoute(); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	tx, latest, err := s.candidateWriter(candidateBoundaryResult, false)
	if err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	defer tx.Rollback()
	if !sameCandidateObservedRoster(current, latest, generatedruntimestate.ComponentHealthy) || latest.runtime.phase != generatedruntimestate.PhaseWaitingHealth {
		return generatedruntimestate.Deployment{}, deploymentexecution.ErrIdentityLost
	}
	state := generatedruntimestate.New(s.authority.stores.Database())
	if err := state.FinishCandidateHealthTx(s.ctx, tx, s.pins.claim.ApplicationID(), s.pins.deploymentID); err != nil {
		return generatedruntimestate.Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return generatedruntimestate.Deployment{}, gateDatabaseError(s.ctx, err)
	}
	return state.Get(s.ctx, s.pins.claim.ApplicationID(), s.pins.deploymentID)
}

func (e *GeneratedCandidateExecution) ObserveRouteCandidate(component string) (CandidateIdentity, error) {
	s, done, err := e.begin(false)
	if err != nil {
		return CandidateIdentity{}, err
	}
	defer done()
	current, err := s.observeCandidateState(true)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if current.runtime.phase != generatedruntimestate.PhaseSwitchingRoute || current.main.status != WaitingHealth {
		return CandidateIdentity{}, deploymentexecution.ErrStageInvalid
	}
	member, err := candidateMember(current, component)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if member.receipt == nil || (member.state != generatedruntimestate.ComponentHealthy && member.state != generatedruntimestate.ComponentActive) {
		return CandidateIdentity{}, deploymentexecution.ErrStageInvalid
	}
	if (member.state == generatedruntimestate.ComponentActive) != (s.anchor.mode == candidateReadOnlySelfHead) {
		return CandidateIdentity{}, deploymentexecution.ErrAuthorityStale
	}
	operation, err := s.reservation.capacity.enterComponent(component, member.receipt.id, candidateOperationObserve)
	if err != nil {
		return CandidateIdentity{}, err
	}
	defer operation.leave()
	p, err := s.describeDurableCandidate(member)
	if err != nil {
		return CandidateIdentity{}, err
	}
	observed, found, err := s.authority.inspectCandidate(s.ctx, p.containerID)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if !found || !p.matchesRunning(observed) || observed.Health != "healthy" {
		return CandidateIdentity{}, candidateFailure("candidate_unhealthy", nil)
	}
	tx, latest, err := s.candidateWriter(candidateBoundaryObservation, true)
	if err != nil {
		return CandidateIdentity{}, err
	}
	defer tx.Rollback()
	if latest.runtime.phase != generatedruntimestate.PhaseSwitchingRoute || !sameCandidateObservedRoster(current, latest, member.state) {
		return CandidateIdentity{}, deploymentexecution.ErrIdentityLost
	}
	if err := tx.Commit(); err != nil {
		return CandidateIdentity{}, gateDatabaseError(s.ctx, err)
	}
	return p.candidateIdentity(), nil
}

// A description reconstructed for observation is never installed in the
// invocation map and therefore cannot be supplied to any command method.
func (s *generatedCandidateExecutionState) describeDurableCandidate(member candidateAdmissionComponent) (*generatedCandidateInvocationPermit, error) {
	if member.receipt == nil || member.artifact == nil || !lowerHex(member.containerID, 64) {
		return nil, deploymentexecution.ErrIdentityLost
	}
	policy, err := buildCandidatePolicy(s.pins, member.plan, *member.artifact, *member.receipt, s.authority.options)
	if err != nil {
		return nil, err
	}
	if policy.identity.ContainerName != member.containerName || policy.version != member.receipt.policyVersion || policy.digest != member.receipt.policyDigest {
		return nil, deploymentexecution.ErrIdentityLost
	}
	return &generatedCandidateInvocationPermit{owner: s, receipt: *member.receipt, policy: policy, containerID: member.containerID, environmentClean: true,
		identity: GeneratedCandidateInvocationIdentity{CurrentClaim: s.pins.claim, DeploymentID: s.pins.deploymentID, ComponentName: member.plan.Name,
			ReceiptID: member.receipt.id, CreatorJobID: member.receipt.creatorJobID, CreatorWorkerAttempt: member.receipt.creatorWorkerAttempt}}, nil
}

func sameCandidateObservedRoster(before, after candidateAdmissionSnapshot, state generatedruntimestate.ComponentState) bool {
	if len(before.components) == 0 || len(before.components) != len(after.components) {
		return false
	}
	for index, first := range before.components {
		last := after.components[index]
		if first.plan != last.plan || first.state != state || last.state != state || first.containerName != last.containerName || first.containerID != last.containerID ||
			first.receipt == nil || last.receipt == nil || *first.receipt != *last.receipt || first.artifact == nil || last.artifact == nil || *first.artifact != *last.artifact {
			return false
		}
	}
	return true
}
