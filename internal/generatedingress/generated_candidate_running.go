package generatedingress

import (
	"context"
	"errors"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func (e *GeneratedCandidateExecution) AdoptCandidate(component string) (GeneratedCandidateInvocation, error) {
	s, done, err := e.begin(false)
	if err != nil {
		return nil, err
	}
	defer done()
	if s.invocations[component] != nil {
		return nil, ErrInvalidTransition
	}
	current, err := s.observeCandidateState(false)
	if err != nil {
		return nil, err
	}
	member, err := candidateMember(current, component)
	if err != nil {
		return nil, err
	}
	if member.receipt == nil || member.artifact == nil || !lowerHex(member.containerID, 64) ||
		(member.state != generatedruntimestate.ComponentRunning && member.state != generatedruntimestate.ComponentHealthy) ||
		(current.runtime.phase != generatedruntimestate.PhaseStartingCandidate && current.runtime.phase != generatedruntimestate.PhaseWaitingHealth) {
		return nil, deploymentexecution.ErrStageInvalid
	}
	operation, err := s.reservation.capacity.enterComponent(component, member.receipt.id, candidateOperationMutate)
	if err != nil {
		return nil, err
	}
	defer operation.leave()
	policy, err := buildCandidatePolicy(s.pins, member.plan, *member.artifact, *member.receipt, s.authority.options)
	if err != nil {
		return nil, err
	}
	p := &generatedCandidateInvocationPermit{owner: s, receipt: *member.receipt, policy: policy, containerID: member.containerID, environmentClean: true,
		identity: GeneratedCandidateInvocationIdentity{CurrentClaim: s.pins.claim, DeploymentID: s.pins.deploymentID, ComponentName: component,
			ReceiptID: member.receipt.id, CreatorJobID: member.receipt.creatorJobID, CreatorWorkerAttempt: member.receipt.creatorWorkerAttempt}}
	observed, found, err := s.authority.inspectCandidate(s.ctx, p.containerID)
	if err != nil {
		return nil, err
	}
	if !found || !p.matchesConfigured(observed) {
		return nil, candidateFailure("candidate_hardening_failed", nil)
	}
	created := current.runtime.phase == generatedruntimestate.PhaseStartingCandidate && member.state == generatedruntimestate.ComponentRunning && generatedcontainer.NeverStarted(observed)
	if !created && !p.matchesRunning(observed) {
		return nil, candidateFailure("candidate_exited", nil)
	}
	tx, latest, err := s.candidateWriter(candidateBoundaryObservation, false)
	if err != nil {
		return nil, err
	}
	if err := p.checkDurableIdentity(latest, member.state); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if latest.runtime.phase != current.runtime.phase {
		_ = tx.Rollback()
		return nil, deploymentexecution.ErrStageInvalid
	}
	if err := tx.Commit(); err != nil {
		return nil, gateDatabaseError(s.ctx, err)
	}
	if err := operation.markPresent(); err != nil {
		return nil, err
	}
	// Adoption never derives a cleanup child or creates a new environment.
	s.invocations[component] = p
	return p, nil
}

func (e *GeneratedCandidateExecution) StartCandidate(value GeneratedCandidateInvocation) error {
	s, done, err := e.begin(false)
	if err != nil {
		return err
	}
	defer done()
	p, err := s.invocation(value)
	if err != nil {
		return err
	}
	operation, err := s.reservation.capacity.enterComponent(p.identity.ComponentName, p.identity.ReceiptID, candidateOperationMutate)
	if err != nil {
		return err
	}
	defer operation.leave()
	current, err := s.observeCandidateState(false)
	if err != nil {
		return err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate {
		return deploymentexecution.ErrStageInvalid
	}
	if err := p.checkDurableIdentity(current, generatedruntimestate.ComponentRunning); err != nil {
		return err
	}
	observed, found, err := s.authority.inspectCandidate(s.ctx, p.containerID)
	if err != nil {
		return err
	}
	if !found || !p.matchesConfigured(observed) {
		return candidateFailure("candidate_hardening_failed", nil)
	}
	if p.matchesRunning(observed) {
		return p.acceptRunningObservation()
	}
	if !generatedcontainer.NeverStarted(observed) {
		return candidateFailure("candidate_exited", nil)
	}
	if err := s.authority.attestCandidateNetwork(s.ctx, p.policy); err != nil {
		return err
	}
	command, err := s.authority.prepareCandidateCommand([]string{"container", "start", p.containerID})
	if err != nil {
		return err
	}
	tx, current, err := s.candidateWriter(candidateBoundaryStart, false)
	if err != nil {
		return err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate {
		_ = tx.Rollback()
		return deploymentexecution.ErrStageInvalid
	}
	if err := p.checkDurableIdentity(current, generatedruntimestate.ComponentRunning); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	if err := operation.enterCommand(candidateCommandStart); err != nil {
		return err
	}
	result, runErr := s.authority.process.Run(s.ctx, command)
	defer clearGeneratedCandidateResult(&result)
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		operation.latchMutationUnquiesced()
		p.mutationUnquiesced = true
		return candidateProcessFailure(s.ctx, result, runErr, "candidate_start_failed")
	}
	if err := contextError(s.ctx); err != nil {
		return candidateFailure("candidate_start_failed", err)
	}
	// A lost start acknowledgement can only be reconciled by a fresh exact
	// running observation. It never permits a second start invocation.
	observed, found, err = s.authority.inspectCandidate(s.ctx, p.containerID)
	if err != nil {
		return err
	}
	if !found || !p.matchesRunning(observed) {
		if !found || (p.matchesConfigured(observed) && candidateRemovableState(observed)) {
			p.observedFailure = generatedruntimestate.DiagnosticStartFailed
		}
		return candidateFailure("candidate_start_failed", runErr)
	}
	return p.acceptRunningObservation()
}

func (p *generatedCandidateInvocationPermit) acceptRunningObservation() error {
	s := p.owner
	tx, current, err := s.candidateWriter(candidateBoundaryObservation, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate {
		return deploymentexecution.ErrStageInvalid
	}
	if err := p.checkDurableIdentity(current, generatedruntimestate.ComponentRunning); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	return nil
}

func (e *GeneratedCandidateExecution) WaitHealthyAndRecord(value GeneratedCandidateInvocation) error {
	s, done, err := e.begin(false)
	if err != nil {
		return err
	}
	defer done()
	p, err := s.invocation(value)
	if err != nil {
		return err
	}
	// Recording health is a positive durable mutation. An earlier unquiesced
	// command in any sibling execution must close this path as well.
	operation, err := s.reservation.capacity.enterComponent(p.identity.ComponentName, p.identity.ReceiptID, candidateOperationMutate)
	if err != nil {
		return err
	}
	defer operation.leave()
	current, err := s.observeCandidateState(false)
	if err != nil {
		return err
	}
	if current.runtime.phase != generatedruntimestate.PhaseWaitingHealth || current.main.status != WaitingHealth {
		return deploymentexecution.ErrStageInvalid
	}
	if err := p.checkDurableIdentity(current, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(s.ctx, s.authority.options.HealthTimeout)
	defer cancel()
	for {
		observed, found, err := s.authority.inspectCandidate(healthCtx, p.containerID)
		if err != nil {
			return err
		}
		if !found || !p.matchesRunning(observed) {
			if !found || (p.matchesConfigured(observed) && candidateRemovableState(observed)) {
				p.observedFailure = generatedruntimestate.DiagnosticHealthFailed
			}
			return candidateFailure("candidate_exited", nil)
		}
		if observed.Health == "healthy" {
			break
		}
		if observed.Health == "unhealthy" {
			p.observedFailure = generatedruntimestate.DiagnosticHealthFailed
			return candidateFailure("candidate_unhealthy", nil)
		}
		if observed.Health != "starting" {
			return candidateFailure("candidate_hardening_failed", nil)
		}
		timer := time.NewTimer(s.authority.options.HealthPollInterval)
		select {
		case <-healthCtx.Done():
			timer.Stop()
			return candidateFailure("runtime_timeout", healthCtx.Err())
		case <-timer.C:
		}
	}
	tx, current, err := s.candidateWriter(candidateBoundaryResult, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if current.runtime.phase != generatedruntimestate.PhaseWaitingHealth || current.main.status != WaitingHealth {
		return deploymentexecution.ErrStageInvalid
	}
	if err := p.checkDurableIdentity(current, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		return err
	}
	member, _ := candidateMember(current, p.identity.ComponentName)
	if member.state == generatedruntimestate.ComponentRunning {
		if err := generatedruntimestate.New(s.authority.stores.Database()).RecordCandidateHealthyTx(s.ctx, tx, s.pins.claim.ApplicationID(), s.pins.deploymentID, p.identity.ComponentName, p.identity.ReceiptID, p.containerID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(s.ctx, err)
	}
	return nil
}

func (p *generatedCandidateInvocationPermit) checkDurableIdentity(current candidateAdmissionSnapshot, states ...generatedruntimestate.ComponentState) error {
	member, err := candidateMember(current, p.identity.ComponentName)
	if err != nil {
		return err
	}
	if member.receipt == nil || *member.receipt != p.receipt || member.containerName != p.policy.identity.ContainerName ||
		!lowerHex(p.containerID, 64) || member.containerID != p.containerID {
		return deploymentexecution.ErrIdentityLost
	}
	for _, state := range states {
		if member.state == state {
			return nil
		}
	}
	return deploymentexecution.ErrStageInvalid
}

func (p *generatedCandidateInvocationPermit) matchesConfigured(value generatedcontainer.Inspection) bool {
	expected := p.policy.identity
	expected.ContainerID = p.containerID
	return generatedcontainer.MatchesConfigured(value, expected, p.policy.limits)
}

func (p *generatedCandidateInvocationPermit) matchesRunning(value generatedcontainer.Inspection) bool {
	expected := p.policy.identity
	expected.ContainerID = p.containerID
	return generatedcontainer.MatchesRunning(value, expected, p.policy.limits) && generatedcontainer.CurrentlyRunning(value)
}
