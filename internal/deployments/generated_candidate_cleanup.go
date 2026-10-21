package deployments

import (
	"errors"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// RemoveInactive is explicit and only accepts the original new-create handle.
// Adoption, public identities and later execution pins cannot gain this child.
func (e *GeneratedCandidateExecution) RemoveInactive(value GeneratedCandidateInvocation) error {
	s, done, err := e.begin(true)
	if err != nil {
		return err
	}
	defer done()
	p, err := s.invocation(value)
	if err != nil {
		return err
	}
	if !p.createdHere || p.cleanup == nil || p.cleanupCtx == nil || p.mutationUnquiesced {
		return deploymentexecution.ErrScopeUnavailable
	}
	operation, err := s.reservation.capacity.enterCleanup(p.identity.ComponentName, p.identity.ReceiptID)
	if err != nil {
		return err
	}
	defer operation.leave()
	if err := p.cleanupGuard(candidateCleanupObservation); err != nil {
		return err
	}
	identity := p.containerID
	if identity == "" {
		identity = p.policy.identity.ContainerName
	}
	observed, found, err := s.authority.inspectCandidate(p.cleanupCtx, identity)
	if err != nil {
		return err
	}
	if !found {
		return p.confirmCandidateAbsent(operation)
	}
	snapshot, err := operation.snapshot()
	if err != nil {
		return err
	}
	if !snapshot.createEntered || !lowerHex(observed.ID, 64) {
		return candidateFailure("candidate_cleanup_failed", nil)
	}
	expected := p.policy.identity
	expected.ContainerID = p.containerID
	if expected.ContainerID == "" {
		expected.ContainerID = observed.ID
	}
	if !generatedcontainer.MatchesConfigured(observed, expected, p.policy.limits) {
		return candidateFailure("candidate_hardening_failed", nil)
	}
	// A rejected name lookup must never change the later cleanup target.
	// Only the complete receipt-bound observation can establish its ID.
	p.containerID = expected.ContainerID
	if generatedcontainer.CurrentlyRunning(observed) {
		command, err := s.authority.prepareCandidateCommand([]string{"container", "stop", "--time", "10", p.containerID})
		if err != nil {
			return err
		}
		if err := p.cleanupGuard(candidateCleanupStop); err != nil {
			return err
		}
		if err := operation.enterCommand(candidateCommandStop); err != nil {
			return err
		}
		result, runErr := s.authority.process.Run(p.cleanupCtx, command)
		clearGeneratedMigrationResult(&result)
		if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
			operation.latchMutationUnquiesced()
			p.mutationUnquiesced = true
			return candidateFailure("process_termination_failed", runErr)
		}
		if err := contextError(p.cleanupCtx); err != nil {
			return candidateFailure("candidate_cleanup_failed", err)
		}
	}
	// This second inspection is mandatory even when stop returned success.
	// A stopped observation grants no remove permission without its own guard.
	observed, found, err = s.authority.inspectCandidate(p.cleanupCtx, p.containerID)
	if err != nil {
		return err
	}
	if !found {
		return p.confirmCandidateAbsent(operation)
	}
	if !p.matchesConfigured(observed) || !candidateRemovableState(observed) {
		return candidateFailure("candidate_cleanup_failed", nil)
	}
	command, err := s.authority.prepareCandidateCommand([]string{"container", "rm", p.containerID})
	if err != nil {
		return err
	}
	if err := p.cleanupGuard(candidateCleanupRemove); err != nil {
		return err
	}
	if err := operation.enterCommand(candidateCommandRemove); err != nil {
		return err
	}
	result, runErr := s.authority.process.Run(p.cleanupCtx, command)
	clearGeneratedMigrationResult(&result)
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		operation.latchMutationUnquiesced()
		p.mutationUnquiesced = true
		return candidateFailure("process_termination_failed", runErr)
	}
	if err := contextError(p.cleanupCtx); err != nil {
		return candidateFailure("candidate_cleanup_failed", err)
	}
	_, found, err = s.authority.inspectCandidate(p.cleanupCtx, p.containerID)
	if err != nil {
		return err
	}
	if found {
		return candidateFailure("candidate_cleanup_failed", runErr)
	}
	return p.confirmCandidateAbsent(operation)
}

func candidateRemovableState(value generatedcontainer.Inspection) bool {
	if generatedcontainer.NeverStarted(value) {
		return true
	}
	started, err := time.Parse(time.RFC3339Nano, value.StartedAt)
	return err == nil && !started.IsZero() && value.Status == "exited" && !value.Running && !value.Paused && !value.Restarting && !value.Dead
}

type candidateCleanupBoundary uint8

const (
	candidateCleanupStop candidateCleanupBoundary = iota + 1
	candidateCleanupRemove
	candidateCleanupObservation
)

func (p *generatedCandidateInvocationPermit) cleanupGuard(boundary candidateCleanupBoundary) error {
	s := p.owner
	ctx := p.cleanupCtx
	if err := contextError(ctx); err != nil {
		return err
	}
	protected, err := s.authority.loadCandidateProtectedSnapshot(ctx, s.pins)
	if err != nil {
		return err
	}
	tx, err := s.authority.repo.db.BeginTx(ctx, nil)
	if err != nil {
		return gateDatabaseError(ctx, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET id=id WHERE 0`); err != nil {
		return gateDatabaseError(ctx, err)
	}
	switch boundary {
	case candidateCleanupStop:
		err = p.cleanup.CheckBeforeStopTx(tx, s.pins.claim, s.pins.deploymentID, p.identity.ComponentName, p.identity.ReceiptID, p.invocationID)
	case candidateCleanupRemove:
		err = p.cleanup.CheckBeforeRemoveTx(tx, s.pins.claim, s.pins.deploymentID, p.identity.ComponentName, p.identity.ReceiptID, p.invocationID)
	case candidateCleanupObservation:
		err = p.cleanup.CheckRemovedObservationTx(tx, s.pins.claim, s.pins.deploymentID, p.identity.ComponentName, p.identity.ReceiptID, p.invocationID)
	default:
		err = deploymentexecution.ErrStageInvalid
	}
	if err != nil {
		return err
	}
	current, err := validateCandidateExecutionSnapshotTx(ctx, tx, s.pins, protected, s.baseline)
	if err != nil {
		return err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate && current.runtime.phase != generatedruntimestate.PhaseWaitingHealth {
		return deploymentexecution.ErrStageInvalid
	}
	if err := s.validateCandidateHeadAndPolicy(current, false); err != nil {
		return err
	}
	member, err := candidateMember(current, p.identity.ComponentName)
	if err != nil {
		return err
	}
	if member.receipt == nil || *member.receipt != p.receipt || member.containerName != p.policy.identity.ContainerName ||
		(member.containerID != "" && member.containerID != p.containerID) {
		return deploymentexecution.ErrIdentityLost
	}
	if member.state != generatedruntimestate.ComponentStarting && member.state != generatedruntimestate.ComponentRunning && member.state != generatedruntimestate.ComponentHealthy {
		return deploymentexecution.ErrStageInvalid
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return gateDatabaseError(ctx, err)
	}
	return nil
}

func (p *generatedCandidateInvocationPermit) confirmCandidateAbsent(operation *candidateComponentOperation) error {
	if err := p.cleanupEnvironment(); err != nil {
		return candidateFailure("configuration_unavailable", err)
	}
	if err := p.cleanupGuard(candidateCleanupObservation); err != nil {
		return err
	}
	if err := operation.retireConfirmedAbsent(); err != nil {
		return err
	}
	p.absenceConfirmed = true
	return nil
}
