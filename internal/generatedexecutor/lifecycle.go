package generatedexecutor

import (
	"context"
	"errors"
	"github.com/hostd/hostd/internal/generatedingress"
	"sort"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

var (
	errMigrationInterrupted      = errors.New("generated migration execution was interrupted")
	errMigrationPreviouslyFailed = errors.New("generated migration previously failed")
)

func (e *Executor) build(ctx context.Context, claim deploymentexecution.Claim, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment) (map[string]generatedimage.Artifact, error) {
	artifacts := make(map[string]generatedimage.Artifact, len(runtimeDeployment.Components))
	for _, stateComponent := range sortedComponents(runtimeDeployment.Components) {
		switch stateComponent.State {
		case generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady:
			artifact, err := e.compileValidated(ctx, claim, resolved, stateComponent.Name)
			if err != nil {
				return artifacts, err
			}
			artifacts[stateComponent.Name] = artifact
		default:
			return artifacts, staticError("unexpected component state while building")
		}
	}
	return artifacts, nil
}

func (e *Executor) compileValidated(ctx context.Context, claim deploymentexecution.Claim, resolved resolvedDeployment, component string) (generatedimage.Artifact, error) {
	artifact, err := e.compiler.Compile(ctx, generatedimage.CompileRequest{
		Claim: claim, DeploymentID: resolved.deployment.ID, ReleaseID: resolved.release.ID,
		ConfigurationRevisionID: resolved.configurationID, ConfigurationRevisionNumber: resolved.configurationNumber,
		DeploymentPlanRevisionID: resolved.plan.ID, DeploymentPlanRevisionNumber: resolved.plan.RevisionNumber,
		ComponentName: component, Findings: append([]deployments.Finding(nil), resolved.deployment.Findings...),
	})
	if err != nil {
		return generatedimage.Artifact{}, err
	}
	if !artifactMatches(resolved, component, artifact) {
		return generatedimage.Artifact{}, staticError("compiler artifact provenance mismatch")
	}
	return artifact, nil
}

func artifactMatches(resolved resolvedDeployment, component string, artifact generatedimage.Artifact) bool {
	return artifact.State == generatedimage.ArtifactReady && artifact.ID != "" && artifact.ReleaseID == resolved.release.ID &&
		artifact.DeploymentPlanRevisionID == resolved.plan.ID && artifact.DeploymentPlanRevisionNumber == resolved.plan.RevisionNumber &&
		artifact.ComponentID == component && artifact.ImageContentID != "" && artifact.BuildDefinitionDigest != ""
}

func (e *Executor) loadArtifacts(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment) (map[string]generatedimage.Artifact, error) {
	result := make(map[string]generatedimage.Artifact, len(runtimeDeployment.Components))
	for _, component := range sortedComponents(runtimeDeployment.Components) {
		if component.ImageArtifactID == "" {
			return nil, staticError("component image is not durable")
		}
		artifact, err := e.artifacts.Get(ctx, component.ImageArtifactID)
		if err != nil || !artifactMatches(resolved, component.Name, artifact) {
			return nil, staticError("component artifact unavailable")
		}
		result[component.Name] = artifact
	}
	return result, nil
}

func (e *Executor) runMigration(ctx context.Context, claim deploymentexecution.Claim, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, artifacts map[string]generatedimage.Artifact) (generatedruntimestate.Deployment, error) {
	migration := resolved.plan.Plan.Migration
	if migration == nil || migration.Approval.Status != deploymentplans.MigrationApprovalApproved {
		return runtimeDeployment, generatedMigrationUncertain(ctx, staticError("migration approval unavailable"))
	}
	switch runtimeDeployment.MigrationState {
	case generatedruntimestate.MigrationRunning:
		return runtimeDeployment, generatedMigrationUncertain(ctx, errMigrationInterrupted)
	case generatedruntimestate.MigrationFailed:
		return runtimeDeployment, generatedMigrationFailed(errMigrationPreviouslyFailed)
	case generatedruntimestate.MigrationPending, generatedruntimestate.MigrationSucceeded:
	default:
		return runtimeDeployment, generatedMigrationUncertain(ctx, staticError("invalid migration state"))
	}
	artifact, exists := artifacts[migration.ComponentName]
	if !exists {
		return runtimeDeployment, generatedMigrationUncertain(ctx, staticError("migration component artifact unavailable"))
	}
	execution, err := e.pinMigration(ctx, generatedMigrationRequest(claim, resolved, migration, artifact))
	if err != nil || execution == nil {
		if err == nil {
			err = deployments.ErrInvalidDeployment
		}
		return runtimeDeployment, generatedMigrationUncertain(ctx, err)
	}
	defer execution.Leave()

	if runtimeDeployment.MigrationState == generatedruntimestate.MigrationSucceeded {
		observed, observeErr := execution.ObserveSucceeded()
		if observeErr != nil {
			return runtimeDeployment, generatedMigrationUncertain(ctx, observeErr)
		}
		prepared, finishErr := execution.FinishPreparation()
		if finishErr != nil {
			return observed, generatedMigrationUncertain(ctx, finishErr)
		}
		return prepared, nil
	}

	invocation, begun, beginErr := execution.Begin()
	if beginErr != nil || invocation == nil {
		if beginErr == nil {
			beginErr = deployments.ErrInvalidDeployment
		}
		return runtimeDeployment, generatedMigrationUncertain(ctx, beginErr)
	}
	runtimeDeployment = begun
	runErr := e.migrations.Run(ctx, invocation)
	if runErr != nil {
		failed, recordErr := execution.RecordObservedFailure()
		if recordErr != nil {
			return runtimeDeployment, generatedMigrationUncertain(ctx, errors.Join(runErr, recordErr))
		}
		return failed, generatedMigrationFailed(runErr)
	}
	succeeded, recordErr := execution.RecordSucceeded()
	if recordErr != nil {
		return runtimeDeployment, generatedMigrationUncertain(ctx, recordErr)
	}
	prepared, finishErr := execution.FinishPreparation()
	if finishErr != nil {
		return succeeded, generatedMigrationUncertain(ctx, finishErr)
	}
	return prepared, nil
}

func generatedMigrationRequest(claim deploymentexecution.Claim, resolved resolvedDeployment, migration *deploymentplans.Migration, artifact generatedimage.Artifact) deployments.GeneratedMigrationRequest {
	return deployments.GeneratedMigrationRequest{
		Claim: claim, DeploymentID: resolved.deployment.ID, ReleaseID: resolved.release.ID,
		ConfigurationRevisionID: resolved.configurationID, ConfigurationRevisionNumber: resolved.configurationNumber,
		DeploymentPlanRevisionID: resolved.plan.ID, DeploymentPlanRevisionNumber: resolved.plan.RevisionNumber,
		ComponentName: migration.ComponentName, CompilerVersion: artifact.CompilerVersion, BuildDefinitionDigest: artifact.BuildDefinitionDigest,
		PlanCanonicalDigest: resolved.plan.CanonicalDigest, MigrationEvidenceDigest: migration.EvidenceDigest,
		SourceProvider: resolved.release.SourceProvider, SourceRepositoryID: resolved.release.RepositoryID, SourceResolvedDigest: resolved.release.ResolvedSHA,
		PlanSourceProvider: resolved.plan.Plan.Source.Provider, PlanSourceRepositoryID: resolved.plan.Plan.Source.RepositoryID, PlanSourceResolvedDigest: resolved.plan.Plan.Source.ResolvedDigest,
		ArchiveSHA256: resolved.release.ArchiveSHA256, WorkspaceTreeSHA256: resolved.release.WorkspaceTreeSHA256,
		ArtifactID: artifact.ID, ArtifactAttemptNumber: artifact.AttemptNumber, ImageContentID: artifact.ImageContentID,
		Findings: append([]deployments.Finding(nil), resolved.deployment.Findings...),
	}
}

func generatedMigrationUncertain(ctx context.Context, cause error) error {
	code := "runtime_unavailable"
	if ctx != nil && ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		code = "internal_error"
		if ctx != nil && ctx.Err() != nil {
			code = cancellationCode(ctx)
			return errors.Join(executionError(code), cause, deploymentexecution.ErrCanceled, ctx.Err())
		}
	}
	return errors.Join(executionError(code), cause)
}

func generatedMigrationFailed(cause error) error {
	return errors.Join(executionError("apply_failed"), cause)
}

func (e *Executor) startCandidates(execution *generatedingress.GeneratedCandidateExecution, runtimeDeployment generatedruntimestate.Deployment) (map[string]generatedingress.GeneratedCandidateInvocation, error) {
	result := make(map[string]generatedingress.GeneratedCandidateInvocation, len(runtimeDeployment.Components))
	for _, persisted := range sortedComponents(runtimeDeployment.Components) {
		var invocation generatedingress.GeneratedCandidateInvocation
		var err error
		switch persisted.State {
		case generatedruntimestate.ComponentImageReady:
			invocation, err = execution.BeginCreate(persisted.Name)
			if err != nil {
				return result, err
			}
			// Retain the original handle even when create or its result fails.
			// Only this handle can request explicit inactive cleanup.
			result[persisted.Name] = invocation
			if _, err = execution.CreateAndRecord(invocation); err != nil {
				return result, err
			}
		case generatedruntimestate.ComponentRunning:
			invocation, err = execution.AdoptCandidate(persisted.Name)
			if err != nil {
				return result, err
			}
			result[persisted.Name] = invocation
		default:
			// Starting with no durable ID never permits discovery-based adoption.
			return result, staticError("candidate state requires reconciliation")
		}
		if err = execution.StartCandidate(invocation); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (e *Executor) healthyCandidates(execution *generatedingress.GeneratedCandidateExecution, runtimeDeployment generatedruntimestate.Deployment, existing map[string]generatedingress.GeneratedCandidateInvocation) error {
	for _, persisted := range sortedComponents(runtimeDeployment.Components) {
		if persisted.State != generatedruntimestate.ComponentRunning && persisted.State != generatedruntimestate.ComponentHealthy {
			return staticError("candidate health requires reconciliation")
		}
		invocation := existing[persisted.Name]
		if invocation == nil {
			var err error
			invocation, err = execution.AdoptCandidate(persisted.Name)
			if err != nil {
				return err
			}
			existing[persisted.Name] = invocation
		}
		// Persisted Healthy is reobserved through the same bound owner.
		if err := execution.WaitHealthyAndRecord(invocation); err != nil {
			return err
		}
	}
	return nil
}

func observeRouteCandidates(execution *generatedingress.GeneratedCandidateExecution, appID string, runtimeDeployment generatedruntimestate.Deployment) (map[string]generatedruntime.Candidate, error) {
	result := make(map[string]generatedruntime.Candidate, len(runtimeDeployment.Components))
	for _, persisted := range sortedComponents(runtimeDeployment.Components) {
		identity, err := execution.ObserveRouteCandidate(persisted.Name)
		if err != nil {
			return nil, err
		}
		// This value is route description only and carries no lifecycle handle.
		result[persisted.Name] = generatedruntime.Candidate{
			AppID: appID, DeploymentID: identity.DeploymentID, ReleaseID: identity.ReleaseID,
			ArtifactID: identity.ArtifactID, DeploymentPlanRevisionID: identity.DeploymentPlanRevisionID,
			Component: identity.ComponentName, Role: identity.Role, Technology: identity.Technology,
			Slot: generatedruntime.Slot(identity.Slot), ContainerID: identity.ContainerID,
			ContainerName: identity.ContainerName, NetworkName: identity.NetworkName,
			NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort,
		}
	}
	return result, nil
}

func candidateRequest(claim deploymentexecution.Claim, resolved resolvedDeployment, slot string) generatedingress.GeneratedCandidateRequest {
	return generatedingress.GeneratedCandidateRequest{
		Claim: claim, DeploymentID: resolved.deployment.ID, ReleaseID: resolved.release.ID,
		ConfigurationRevisionID: resolved.configurationID, ConfigurationRevisionNumber: resolved.configurationNumber,
		DeploymentPlanRevisionID: resolved.plan.ID, DeploymentPlanRevisionNumber: resolved.plan.RevisionNumber,
		CandidateSlot: slot, Findings: append([]deployments.Finding(nil), resolved.deployment.Findings...),
	}
}

// candidateFailure requests cleanup explicitly, then asks the owner whether
// its private observations prove a terminal failure. An empty map, an adopted
// handle, cleanup uncertainty or a closed route handoff cannot supply proof.
func (e *Executor) candidateFailure(ctx context.Context, execution *generatedingress.GeneratedCandidateExecution, reservation generatedruntime.ReplacementReservation, invocations map[string]generatedingress.GeneratedCandidateInvocation, cause error) error {
	var cleanupErr error
	for _, invocation := range invocations {
		cleanupErr = errors.Join(cleanupErr, execution.RemoveInactive(invocation))
	}
	if cleanupErr != nil {
		// An uncertain cleanup must preserve the complete replacement budget,
		// including siblings whose runner was not entered. The owner retains
		// risky individual shares even if whole-reservation retention refuses.
		retainErr := e.runtime.RetainReplacementCapacity(reservation)
		return generatedAuthorizationRefusal(ctx, errors.Join(cause, cleanupErr, retainErr))
	}
	failed, err := execution.RecordObservedFailure()
	if err != nil {
		return generatedAuthorizationRefusal(ctx, errors.Join(cause, err))
	}
	code := "apply_failed"
	if failed.DiagnosticCode == generatedruntimestate.DiagnosticHealthFailed {
		code = "health_failed"
	}
	return errors.Join(executionError(code), cause)
}

func (e *Executor) switchRoute(ctx context.Context, resolved resolvedDeployment,
	runtimeDeployment generatedruntimestate.Deployment, candidateExecution *generatedingress.GeneratedCandidateExecution,
	candidates map[string]generatedruntime.Candidate,
) (generatedruntimestate.Deployment, predecessorRetirement, error) {
	request := generatedruntime.RouteSwitchRequest{
		AppID: resolved.deployment.AppID, FromSlot: generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot),
		ToSlot: generatedruntime.Slot(runtimeDeployment.CandidateSlot), DrainPeriod: e.options.DrainPeriod,
		Endpoints: routeEndpoints(resolved.plan, candidates),
	}
	activation, err := e.routes.activate(ctx, candidateExecution, request, runtimeDeployment)
	if err != nil {
		if activation.retirement != nil {
			activation.retirement.Leave()
		}
		return runtimeDeployment, nil, newRouteSwitchError(err, "route activation failed")
	}
	updated := activation.deployment
	if updated.DeploymentID != runtimeDeployment.DeploymentID || updated.AppID != runtimeDeployment.AppID ||
		updated.Phase != generatedruntimestate.PhaseDraining || activation.retirement == nil {
		if activation.retirement != nil {
			activation.retirement.Leave()
		}
		return runtimeDeployment, nil, staticError("route activation returned invalid runtime state")
	}
	return updated, activation.retirement, nil
}

// routeSwitchError preserves only the closed pause cause derived at the
// sealed route adapter. It intentionally excludes and does not unwrap the
// underlying route error because that owner may carry Docker output or
// endpoint data.
type routeSwitchError struct {
	label string
	cause routePauseCause
}

func (e routeSwitchError) Error() string { return "generated executor: " + e.label }

func (e routeSwitchError) GatewayReadinessFailed() bool {
	return e.cause == routePauseCauseGatewayReadinessFailed
}

func (e routeSwitchError) routePauseCause() routePauseCause { return e.cause }

func newRouteSwitchError(err error, label string) error {
	return routeSwitchError{label: label, cause: classifyRouteSwitchCause(err)}
}

func classifyRouteSwitchCause(err error) routePauseCause {
	switch {
	case generatedruntime.RouteGatewayReadinessFailed(err):
		return routePauseCauseGatewayReadinessFailed
	case errors.Is(err, context.Canceled):
		return routePauseCauseCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return routePauseCauseDeadlineExceeded
	case errors.Is(err, generatedruntimestate.ErrNotFound):
		return routePauseCauseStateNotFound
	case errors.Is(err, generatedruntimestate.ErrInvalidState):
		return routePauseCauseInvalidState
	case errors.Is(err, generatedruntimestate.ErrInvalidTransition):
		return routePauseCauseInvalidTransition
	case errors.Is(err, generatedruntimestate.ErrConflict):
		return routePauseCauseStateConflict
	default:
		return routePauseCauseUnknown
	}
}

func routeEndpoints(plan deploymentplans.DeploymentPlanRevision, candidates map[string]generatedruntime.Candidate) []generatedruntime.RouteEndpoint {
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]generatedruntime.RouteEndpoint, 0, len(names))
	for _, name := range names {
		candidate := candidates[name]
		component, exists := componentPlan(plan, name)
		if !exists {
			continue
		}
		result = append(result, generatedruntime.RouteEndpoint{
			Component: name, Role: component.Role, ContainerID: candidate.ContainerID,
			NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort,
		})
	}
	return result
}

func (e *Executor) drainPrevious(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment) error {
	if runtimeDeployment.PreviousActiveDeploymentID == "" {
		return nil
	}
	postSwitch, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.options.DrainPeriod+e.options.CleanupTimeout)
	defer cancel()
	if err := e.waitDrain(postSwitch, e.options.DrainPeriod); err != nil {
		return err
	}
	previous, plan, artifacts, err := e.previousResources(postSwitch, resolved.deployment.AppID, runtimeDeployment.PreviousActiveDeploymentID)
	if err != nil {
		return err
	}
	candidates, err := reconstructPreviousCandidates(resolved.deployment.AppID, previous, plan, artifacts)
	if err != nil {
		return err
	}
	for _, component := range sortedComponents(previous.Components) {
		if component.State == generatedruntimestate.ComponentStopped {
			continue
		}
		candidate, exists := candidates[component.Name]
		if !exists {
			return staticError("previous candidate unavailable")
		}
		if err = e.runtime.StopAndRemove(postSwitch, candidate, 0); err != nil {
			return err
		}
		expected := component.State
		if expected != generatedruntimestate.ComponentDraining && expected != generatedruntimestate.ComponentActive {
			return staticError("previous component state is not drainable")
		}
		if _, err = e.state.AdvanceComponent(postSwitch, resolved.deployment.AppID, previous.DeploymentID, component.Name, expected, generatedruntimestate.ComponentStopped); err != nil {
			return staticError("persist previous component stopped")
		}
	}
	return nil
}

func (e *Executor) retirePrevious(ctx context.Context, retirement predecessorRetirement) error {
	if retirement == nil {
		return deployments.ErrGeneratedCandidateRetirementUnavailable
	}
	_, err := retirement.Retire(ctx)
	return err
}

func (e *Executor) previousResources(ctx context.Context, appID, deploymentID string) (generatedruntimestate.Deployment, deploymentplans.DeploymentPlanRevision, map[string]generatedimage.Artifact, error) {
	previous, err := e.state.Get(ctx, appID, deploymentID)
	if err != nil {
		return generatedruntimestate.Deployment{}, deploymentplans.DeploymentPlanRevision{}, nil, err
	}
	plan, err := e.plans.GetRevision(ctx, appID, previous.DeploymentPlanRevisionID, previous.DeploymentPlanRevisionNumber)
	if err != nil || plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode {
		return generatedruntimestate.Deployment{}, deploymentplans.DeploymentPlanRevision{}, nil, staticError("previous plan unavailable")
	}
	artifacts := make(map[string]generatedimage.Artifact, len(previous.Components))
	for _, component := range previous.Components {
		artifact, getErr := e.artifacts.Get(ctx, component.ImageArtifactID)
		if getErr != nil || artifact.ComponentID != component.Name || artifact.ReleaseID != previous.ReleaseID || artifact.DeploymentPlanRevisionID != plan.ID {
			return generatedruntimestate.Deployment{}, deploymentplans.DeploymentPlanRevision{}, nil, staticError("previous artifact unavailable")
		}
		artifacts[component.Name] = artifact
	}
	return previous, plan, artifacts, nil
}

func reconstructPreviousCandidates(appID string, previous generatedruntimestate.Deployment, plan deploymentplans.DeploymentPlanRevision, artifacts map[string]generatedimage.Artifact) (map[string]generatedruntime.Candidate, error) {
	result := make(map[string]generatedruntime.Candidate, len(previous.Components))
	activeForDescription := generatedruntime.SlotBlue
	if previous.CandidateSlot == string(generatedruntime.SlotBlue) {
		activeForDescription = generatedruntime.SlotGreen
	}
	for _, persisted := range previous.Components {
		component, exists := componentPlan(plan, persisted.Name)
		artifact, artifactExists := artifacts[persisted.Name]
		if !exists || !artifactExists || persisted.ContainerID == "" {
			return nil, staticError("previous candidate provenance unavailable")
		}
		description, err := generatedruntime.DescribeInactiveCandidate(appID, persisted.Name, activeForDescription)
		if err != nil || string(description.Slot) != previous.CandidateSlot || description.ContainerName != persisted.ContainerName {
			return nil, staticError("previous candidate identity mismatch")
		}
		result[persisted.Name] = generatedruntime.Candidate{
			AppID: appID, ReleaseID: previous.ReleaseID, DeploymentID: previous.DeploymentID,
			ArtifactID: artifact.ID, DeploymentPlanRevisionID: plan.ID, Component: persisted.Name, Role: component.Role, Technology: component.Technology,
			Slot: description.Slot, ContainerID: persisted.ContainerID, ContainerName: description.ContainerName,
			NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias,
			InternalPort: component.InternalPort, ImageContentID: artifact.ImageContentID,
			WorkingDirectory: runtimeWorkingDirectory(component.RootDirectory), RunCommandDigest: commandDigest(component.RunCommand),
		}
	}
	return result, nil
}
