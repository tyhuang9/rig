package deployments

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"unicode/utf8"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// These observations are private to the concrete owner. Their callers must
// revalidate original authority in the same transaction as accepting a result.
func (a *GeneratedCandidateAuthority) inspectCandidate(ctx context.Context, identity string) (generatedcontainer.Inspection, bool, error) {
	command, err := a.prepareCandidateCommand([]string{"container", "inspect", "--format", generatedcontainer.InspectFormat, identity})
	if err != nil {
		return generatedcontainer.Inspection{}, false, err
	}
	result, runErr := a.process.Run(ctx, command)
	defer clearGeneratedMigrationResult(&result)
	if contextError(ctx) == nil && !errors.Is(runErr, runtimeprocess.ErrTerminationFailed) &&
		!errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) &&
		generatedMigrationDefiniteAbsence(result, runErr, identity) {
		return generatedcontainer.Inspection{}, false, nil
	}
	if err := candidateProcessFailure(ctx, result, runErr, "runtime_unavailable"); err != nil {
		return generatedcontainer.Inspection{}, false, err
	}
	value, err := generatedcontainer.Decode(result.Stdout)
	if err != nil {
		return generatedcontainer.Inspection{}, false, candidateFailure("candidate_hardening_failed", err)
	}
	return value, true, nil
}

func (a *GeneratedCandidateAuthority) attestCandidateImage(ctx context.Context, policy candidatePolicy) error {
	command, err := a.prepareCandidateCommand([]string{"image", "inspect", "--format", generatedMigrationImageInspectFormat, policy.identity.ImageContentID})
	if err != nil {
		return err
	}
	result, runErr := a.process.Run(ctx, command)
	defer clearGeneratedMigrationResult(&result)
	if err := candidateProcessFailure(ctx, result, runErr, "image_unavailable"); err != nil {
		return err
	}
	value, err := decodeGeneratedMigrationImageInspection(result.Stdout)
	if err != nil {
		return candidateFailure("image_drift_detected", err)
	}
	i := policy.identity
	labels := map[string]string{"io.rig.managed": "generated-image", "io.rig.application": i.AppID, "io.rig.release": i.ReleaseID,
		"io.rig.artifact": i.ArtifactID, "io.rig.plan": i.DeploymentPlanRevisionID, "io.rig.component": i.Component,
		"io.rig.role": i.Role, "io.rig.definition": policy.buildDefinitionDigest}
	if value.ID != i.ImageContentID || value.Size < 1 || !generatedMigrationContainsLabels(value.Labels, labels) || value.User != generatedcontainer.User ||
		value.WorkingDirectory != "/workspace" || len(value.Entrypoint) != 1 || value.Entrypoint[0] != generatedcontainer.Entrypoint {
		return candidateFailure("image_drift_detected", nil)
	}
	return nil
}

func (a *GeneratedCandidateAuthority) attestCandidateNetwork(ctx context.Context, policy candidatePolicy) error {
	command, err := a.prepareCandidateCommand([]string{"network", "inspect", "--format", generatedMigrationNetworkInspectFormat, policy.identity.NetworkName})
	if err != nil {
		return err
	}
	result, runErr := a.process.Run(ctx, command)
	defer clearGeneratedMigrationResult(&result)
	if err := candidateProcessFailure(ctx, result, runErr, "network_drift_detected"); err != nil {
		return err
	}
	value, err := decodeGeneratedMigrationNetworkInspection(result.Stdout)
	if err != nil {
		return candidateFailure("network_drift_detected", err)
	}
	if value.Name != policy.identity.NetworkName || value.Driver != "bridge" || value.Scope != "local" || value.Internal ||
		!generatedMigrationContainsLabels(value.Labels, map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": policy.identity.AppID}) {
		return candidateFailure("network_drift_detected", nil)
	}
	return nil
}

func (e *GeneratedCandidateExecution) CreateAndRecord(value GeneratedCandidateInvocation) (identity CandidateIdentity, err error) {
	s, done, err := e.begin(false)
	if err != nil {
		return CandidateIdentity{}, err
	}
	defer done()
	p, err := s.invocation(value)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if !p.createdHere || p.createInvoked {
		return CandidateIdentity{}, ErrInvalidTransition
	}
	operation, err := s.reservation.capacity.enterComponent(p.identity.ComponentName, p.identity.ReceiptID, candidateOperationMutate)
	if err != nil {
		return CandidateIdentity{}, err
	}
	defer operation.leave()
	p.createInvoked = true
	cleanupAttempted := false
	defer func() {
		if cleanupAttempted || p.mutationUnquiesced {
			return
		}
		// Staging may return a lease together with an error. Every quiescent
		// return attempts cleanup, including a refusal at the final guard.
		// Leave retains a failed lease for a later cleanup attempt.
		if cleanupErr := p.cleanupEnvironment(); cleanupErr != nil {
			identity = CandidateIdentity{}
			err = errors.Join(err, candidateFailure("configuration_unavailable", cleanupErr))
		}
	}()
	current, err := s.observeCandidateState(false)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if err := p.checkStartingIntent(current); err != nil {
		return CandidateIdentity{}, err
	}
	if err := s.authority.attestCandidateImage(s.ctx, p.policy); err != nil {
		return CandidateIdentity{}, err
	}
	if err := s.authority.attestCandidateNetwork(s.ctx, p.policy); err != nil {
		return CandidateIdentity{}, err
	}
	if _, found, err := s.authority.inspectCandidate(s.ctx, p.policy.identity.ContainerName); err != nil {
		return CandidateIdentity{}, err
	} else if found {
		return CandidateIdentity{}, candidateFailure("candidate_slot_occupied", nil)
	}
	if err := p.stageEnvironment(); err != nil {
		return CandidateIdentity{}, err
	}
	environmentPath := ""
	if p.environment != nil {
		environmentPath = p.environment.Path()
	}
	args, err := p.policy.createArgs(environmentPath)
	if err != nil {
		return CandidateIdentity{}, err
	}
	command, err := s.authority.prepareCandidateCommand(args)
	if err != nil {
		return CandidateIdentity{}, err
	}
	tx, current, err := s.candidateWriter(candidateBoundaryCreate, false)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if err := p.checkStartingIntent(current); err != nil {
		_ = tx.Rollback()
		return CandidateIdentity{}, err
	}
	if err := tx.Commit(); err != nil {
		return CandidateIdentity{}, gateDatabaseError(s.ctx, err)
	}
	if err := operation.enterCommand(candidateCommandCreate); err != nil {
		return CandidateIdentity{}, err
	}
	result, runErr := s.authority.process.Run(s.ctx, command)
	defer clearGeneratedMigrationResult(&result)
	if errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		operation.latchMutationUnquiesced()
		p.mutationUnquiesced = true
		return CandidateIdentity{}, candidateProcessFailure(s.ctx, result, runErr, "candidate_create_failed")
	}
	processErr := candidateProcessFailure(s.ctx, result, runErr, "candidate_create_failed")
	if exited, completed := runErr.(*exec.ExitError); completed && exited.ProcessState != nil &&
		exited.ProcessState.Exited() && exited.ProcessState.ExitCode() != 0 && contextError(s.ctx) == nil {
		p.observedFailure = generatedruntimestate.DiagnosticStartFailed
	}
	cleanupAttempted = true
	cleanupErr := p.cleanupEnvironment()
	if processErr != nil {
		if cleanupErr != nil {
			return CandidateIdentity{}, errors.Join(processErr, candidateFailure("configuration_unavailable", cleanupErr))
		}
		return CandidateIdentity{}, processErr
	}
	if cleanupErr != nil {
		return CandidateIdentity{}, candidateFailure("configuration_unavailable", cleanupErr)
	}
	id := strings.TrimSpace(string(result.Stdout))
	if !lowerHex(id, 64) {
		return CandidateIdentity{}, candidateFailure("candidate_create_failed", nil)
	}
	observed, found, err := s.authority.inspectCandidate(s.ctx, id)
	if err != nil {
		return CandidateIdentity{}, err
	}
	expected := p.policy.identity
	expected.ContainerID = id
	if !found || !generatedcontainer.MatchesConfigured(observed, expected, p.policy.limits) || !generatedcontainer.NeverStarted(observed) {
		return CandidateIdentity{}, candidateFailure("candidate_hardening_failed", nil)
	}
	p.containerID = id
	if err := operation.markPresent(); err != nil {
		return CandidateIdentity{}, err
	}
	tx, current, err = s.candidateWriter(candidateBoundaryResult, false)
	if err != nil {
		return CandidateIdentity{}, err
	}
	defer tx.Rollback()
	if err := p.checkStartingIntent(current); err != nil {
		return CandidateIdentity{}, err
	}
	if err := generatedruntimestate.New(s.authority.repo.db).RecordCandidateCreatedTx(s.ctx, tx, s.pins.claim.ApplicationID(), s.pins.deploymentID, p.identity.ComponentName, p.identity.ReceiptID, p.policy.identity.ContainerName, id); err != nil {
		return CandidateIdentity{}, err
	}
	if err := tx.Commit(); err != nil {
		return CandidateIdentity{}, gateDatabaseError(s.ctx, err)
	}
	return p.candidateIdentity(), nil
}

func (p *generatedCandidateInvocationPermit) checkStartingIntent(current candidateAdmissionSnapshot) error {
	member, err := candidateMember(current, p.identity.ComponentName)
	if err != nil {
		return err
	}
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate || current.main.status != Applying || member.state != generatedruntimestate.ComponentStarting ||
		member.containerName != p.policy.identity.ContainerName || member.containerID != "" || member.receipt == nil || *member.receipt != p.receipt {
		return deploymentexecution.ErrIdentityLost
	}
	return nil
}

func (p *generatedCandidateInvocationPermit) stageEnvironment() error {
	s := p.owner
	configuration, err := s.authority.repo.migrationConfiguration.ExportComponentRuntimeForExecution(s.ctx, s.pins.claim.ApplicationID(), s.pins.configurationRevisionID,
		s.pins.configurationRevisionNumber, s.pins.deploymentPlanRevisionID, s.pins.deploymentPlanRevisionNumber, p.identity.ComponentName)
	if err != nil {
		return candidateFailure("configuration_unavailable", err)
	}
	defer configuration.Clear()
	if configuration.RevisionID != p.receipt.configurationRevisionID || configuration.RevisionNumber != p.receipt.configurationRevisionNumber ||
		len(configuration.Environment) > 64<<10 || !utf8.Valid(configuration.Environment) || bytes.IndexByte(configuration.Environment, 0) >= 0 {
		return candidateFailure("configuration_unavailable", nil)
	}
	// Protected exports include a comment header even when the selected
	// component has no runtime entries. Do not create an environment file for
	// that empty configuration. A key with an empty value still needs staging.
	hasEntries := false
	for _, line := range bytes.Split(configuration.Environment, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) != 0 && line[0] != '#' {
			hasEntries = true
			break
		}
	}
	if !hasEntries {
		return nil
	}
	contents := configuration.Environment
	lease, err := s.authority.environment.StageGeneratedCandidate(p.receipt.environmentOperationID, p.receipt.environmentStagingAttempt, contents)
	clear(contents)
	configuration.Environment = nil
	if !nilInterface(lease) {
		p.environment = lease
		p.environmentClean = false
	}
	if err != nil || nilInterface(lease) {
		return candidateFailure("configuration_unavailable", err)
	}
	if !generatedCandidateCleanAbsolutePath(lease.Path()) {
		return candidateFailure("configuration_unavailable", nil)
	}
	return nil
}

func (p *generatedCandidateInvocationPermit) cleanupEnvironment() error {
	if p.environment == nil {
		return nil
	}
	err := p.environment.Cleanup()
	if err == nil {
		p.environment = nil
		p.environmentClean = true
	}
	return err
}

func (p *generatedCandidateInvocationPermit) candidateIdentity() CandidateIdentity {
	i := p.policy.identity
	return CandidateIdentity{ReceiptID: i.ReceiptID, DeploymentID: i.DeploymentID, ReleaseID: i.ReleaseID, ArtifactID: i.ArtifactID,
		DeploymentPlanRevisionID: i.DeploymentPlanRevisionID, ComponentName: i.Component, Role: i.Role, Technology: i.Technology, Slot: i.Slot,
		ContainerID: p.containerID, ContainerName: i.ContainerName, NetworkName: i.NetworkName, NetworkAlias: i.NetworkAlias, InternalPort: p.policy.internalPort}
}
