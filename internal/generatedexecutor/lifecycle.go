package generatedexecutor

import (
	"context"
	"errors"
	"sort"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

var errMigrationInterrupted = errors.New("generated migration execution was interrupted")

func (e *Executor) build(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment) (map[string]generatedimage.Artifact, error) {
	artifacts := make(map[string]generatedimage.Artifact, len(runtimeDeployment.Components))
	for _, stateComponent := range sortedComponents(runtimeDeployment.Components) {
		switch stateComponent.State {
		case generatedruntimestate.ComponentPending:
			artifact, err := e.compileValidated(ctx, resolved, stateComponent.Name)
			if err != nil {
				return artifacts, err
			}
			if _, err = e.state.SetImageReady(ctx, resolved.deployment.AppID, resolved.deployment.ID, stateComponent.Name, artifact.ID); err != nil {
				return artifacts, staticError("persist image readiness")
			}
			artifacts[stateComponent.Name] = artifact
		case generatedruntimestate.ComponentImageReady:
			artifact, err := e.artifacts.Get(ctx, stateComponent.ImageArtifactID)
			if err != nil || !artifactMatches(resolved, stateComponent.Name, artifact) {
				return artifacts, staticError("persisted image provenance unavailable")
			}
			if err = e.runtime.ValidateImage(ctx, imageSpec(resolved, artifact)); err != nil {
				// The component row is already immutable at ImageReady. Replacing
				// its artifact would make recovery ambiguous, so fail closed.
				return artifacts, err
			}
			artifacts[stateComponent.Name] = artifact
		default:
			return artifacts, staticError("unexpected component state while building")
		}
	}
	return artifacts, nil
}

func (e *Executor) compileValidated(ctx context.Context, resolved resolvedDeployment, component string) (generatedimage.Artifact, error) {
	artifact, err := e.compiler.Compile(ctx, resolved.deployment.AppID, resolved.release.ID, component)
	if err != nil {
		return generatedimage.Artifact{}, err
	}
	if !artifactMatches(resolved, component, artifact) {
		return generatedimage.Artifact{}, staticError("compiler artifact provenance mismatch")
	}
	err = e.runtime.ValidateImage(ctx, imageSpec(resolved, artifact))
	if err == nil {
		return artifact, nil
	}
	if !generatedruntime.IsCode(err, generatedruntime.DiagnosticImageUnavailable) && !generatedruntime.IsCode(err, generatedruntime.DiagnosticImageDriftDetected) {
		return generatedimage.Artifact{}, err
	}
	if _, markErr := e.artifacts.MarkUnavailable(ctx, artifact.ID); markErr != nil {
		return generatedimage.Artifact{}, staticError("mark image unavailable")
	}
	artifact, err = e.compiler.Compile(ctx, resolved.deployment.AppID, resolved.release.ID, component)
	if err != nil {
		return generatedimage.Artifact{}, err
	}
	if !artifactMatches(resolved, component, artifact) {
		return generatedimage.Artifact{}, staticError("rebuilt artifact provenance mismatch")
	}
	if err = e.runtime.ValidateImage(ctx, imageSpec(resolved, artifact)); err != nil {
		return generatedimage.Artifact{}, err
	}
	return artifact, nil
}

func imageSpec(resolved resolvedDeployment, artifact generatedimage.Artifact) generatedruntime.ImageSpec {
	component, _ := componentPlan(resolved.plan, artifact.ComponentID)
	return generatedruntime.ImageSpec{
		AppID: resolved.deployment.AppID, ReleaseID: resolved.release.ID, ArtifactID: artifact.ID,
		DeploymentPlanRevisionID: resolved.plan.ID, ImageContentID: artifact.ImageContentID,
		ComponentName: artifact.ComponentID, Role: component.Role, BuildDefinitionDigest: artifact.BuildDefinitionDigest,
	}
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

func (e *Executor) runMigration(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, artifacts map[string]generatedimage.Artifact) (generatedruntimestate.Deployment, error) {
	migration := resolved.plan.Plan.Migration
	if migration == nil || migration.Approval.Status != deploymentplans.MigrationApprovalApproved {
		return runtimeDeployment, staticError("migration approval unavailable")
	}
	switch runtimeDeployment.MigrationState {
	case generatedruntimestate.MigrationSucceeded:
		return runtimeDeployment, nil
	case generatedruntimestate.MigrationRunning:
		return runtimeDeployment, errMigrationInterrupted
	case generatedruntimestate.MigrationFailed:
		return runtimeDeployment, staticError("migration previously failed")
	case generatedruntimestate.MigrationPending:
	default:
		return runtimeDeployment, staticError("invalid migration state")
	}
	artifact, exists := artifacts[migration.ComponentName]
	if !exists {
		return runtimeDeployment, staticError("migration component artifact unavailable")
	}
	updated, err := e.state.BeginMigration(ctx, resolved.deployment.AppID, resolved.deployment.ID)
	if err != nil {
		return runtimeDeployment, staticError("persist migration start")
	}
	runtimeDeployment = updated
	err = e.migrations.Run(ctx, generatedruntime.MigrationRequest{
		AppID: resolved.deployment.AppID, ReleaseID: resolved.release.ID, DeploymentID: resolved.deployment.ID,
		ArtifactID: artifact.ID, DeploymentPlanRevisionID: resolved.plan.ID,
		ComponentName: migration.ComponentName, RootDirectory: migration.RootDirectory,
		ImageContentID: artifact.ImageContentID, Command: migration.Command,
		ConfigurationRevisionID: resolved.configurationID, ConfigurationRevisionNumber: resolved.configurationNumber,
		AllowedEnvironmentKeys: append([]string(nil), migration.EnvironmentKeys...),
	})
	if err != nil {
		updated, finishErr := e.state.FinishMigration(context.WithoutCancel(ctx), resolved.deployment.AppID, resolved.deployment.ID, false)
		if finishErr == nil {
			runtimeDeployment = updated
		}
		return runtimeDeployment, staticError("migration execution failed")
	}
	updated, err = e.state.FinishMigration(ctx, resolved.deployment.AppID, resolved.deployment.ID, true)
	if err != nil {
		return runtimeDeployment, staticError("persist migration completion")
	}
	return updated, nil
}

func (e *Executor) startCandidates(ctx context.Context, job jobs.Job, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, artifacts map[string]generatedimage.Artifact, reservation generatedruntime.ReplacementReservation) (map[string]generatedruntime.Candidate, error) {
	result := make(map[string]generatedruntime.Candidate, len(runtimeDeployment.Components))
	activeSlot := generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot)
	for _, persisted := range sortedComponents(runtimeDeployment.Components) {
		component, exists := componentPlan(resolved.plan, persisted.Name)
		artifact, artifactExists := artifacts[persisted.Name]
		if !exists || !artifactExists {
			return result, staticError("candidate provenance unavailable")
		}
		switch persisted.State {
		case generatedruntimestate.ComponentImageReady:
			description, err := generatedruntime.DescribeInactiveCandidate(resolved.deployment.AppID, component.Name, activeSlot)
			if err != nil || string(description.Slot) != runtimeDeployment.CandidateSlot {
				return result, staticError("candidate identity mismatch")
			}
			if _, err = e.state.SetContainerStarting(ctx, resolved.deployment.AppID, resolved.deployment.ID, component.Name, description.ContainerName); err != nil {
				return result, staticError("persist candidate intent")
			}
			configuration, err := e.configuration.ExportRevisionForExecution(ctx, resolved.deployment.AppID, resolved.configurationID, resolved.configurationNumber)
			if err != nil {
				return result, codedError("configuration_unavailable")
			}
			candidate, createErr := e.runtime.CreateInactiveCandidate(ctx, generatedruntime.CandidateSpec{
				AppID: resolved.deployment.AppID, ReleaseID: resolved.release.ID, DeploymentID: resolved.deployment.ID,
				ArtifactID: artifact.ID, DeploymentPlanRevisionID: resolved.plan.ID, ComponentName: component.Name, Role: component.Role,
				RootDirectory: component.RootDirectory, RunCommand: component.RunCommand, InternalPort: component.InternalPort,
				HealthProbe: component.HealthProbe, ImageContentID: artifact.ImageContentID,
				BuildDefinitionDigest: artifact.BuildDefinitionDigest, ActiveSlot: activeSlot,
				EnvironmentOperationID: artifact.ID, EnvironmentOperationAttempt: job.Attempt, Environment: configuration.Environment,
				Reservation: reservation,
			})
			configuration.Environment = nil
			configuration.Clear()
			if createErr != nil {
				return result, createErr
			}
			result[component.Name] = candidate
			if _, err = e.state.SetContainerRunning(ctx, resolved.deployment.AppID, resolved.deployment.ID, component.Name, candidate.ContainerID); err != nil {
				return result, staticError("persist candidate container")
			}
			if err = e.runtime.StartCandidate(ctx, candidate); err != nil {
				return result, err
			}
		case generatedruntimestate.ComponentStarting:
			// A crash between Docker create and SetContainerRunning cannot be
			// resolved from intent alone without trusting container discovery.
			return result, staticError("candidate create was interrupted")
		case generatedruntimestate.ComponentRunning:
			candidate, err := reconstructCandidate(resolved, runtimeDeployment, persisted, artifact)
			if err != nil {
				return result, err
			}
			candidate, err = e.runtime.AdoptCandidate(candidate, reservation)
			if err != nil {
				return result, err
			}
			result[component.Name] = candidate
			if err = e.runtime.StartCandidate(ctx, candidate); err != nil {
				return result, err
			}
		default:
			return result, staticError("unexpected component state while starting")
		}
	}
	return result, nil
}

func (e *Executor) healthyCandidates(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, artifacts map[string]generatedimage.Artifact, existing map[string]generatedruntime.Candidate, reservation generatedruntime.ReplacementReservation) (map[string]generatedruntime.Candidate, error) {
	result := existing
	if result == nil {
		result = make(map[string]generatedruntime.Candidate, len(runtimeDeployment.Components))
	}
	for _, persisted := range sortedComponents(runtimeDeployment.Components) {
		artifact, exists := artifacts[persisted.Name]
		if !exists {
			return result, staticError("health artifact unavailable")
		}
		candidate, exists := result[persisted.Name]
		if !exists {
			var err error
			candidate, err = reconstructCandidate(resolved, runtimeDeployment, persisted, artifact)
			if err != nil {
				return result, err
			}
			candidate, err = e.runtime.AdoptCandidate(candidate, reservation)
			if err != nil {
				return result, err
			}
			result[persisted.Name] = candidate
		}
		switch persisted.State {
		case generatedruntimestate.ComponentRunning:
			if err := e.runtime.WaitHealthy(ctx, candidate); err != nil {
				return result, err
			}
			if _, err := e.state.AdvanceComponent(ctx, resolved.deployment.AppID, resolved.deployment.ID, persisted.Name, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
				return result, staticError("persist candidate health")
			}
		case generatedruntimestate.ComponentHealthy:
		default:
			return result, staticError("unexpected component state while waiting for health")
		}
	}
	return result, nil
}

func (e *Executor) reconstructCandidates(resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, artifacts map[string]generatedimage.Artifact, reservation generatedruntime.ReplacementReservation) (map[string]generatedruntime.Candidate, error) {
	result := make(map[string]generatedruntime.Candidate, len(runtimeDeployment.Components))
	for _, component := range sortedComponents(runtimeDeployment.Components) {
		if component.State != generatedruntimestate.ComponentHealthy && component.State != generatedruntimestate.ComponentActive {
			return nil, staticError("route candidate is not healthy")
		}
		artifact, exists := artifacts[component.Name]
		if !exists {
			return nil, staticError("route artifact unavailable")
		}
		candidate, err := reconstructCandidate(resolved, runtimeDeployment, component, artifact)
		if err != nil {
			return nil, err
		}
		candidate, err = e.runtime.AdoptCandidate(candidate, reservation)
		if err != nil {
			return nil, err
		}
		result[component.Name] = candidate
	}
	return result, nil
}

func reconstructCandidate(resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, persisted generatedruntimestate.Component, artifact generatedimage.Artifact) (generatedruntime.Candidate, error) {
	component, exists := componentPlan(resolved.plan, persisted.Name)
	if !exists || persisted.ContainerID == "" || persisted.ContainerName == "" || persisted.Slot != runtimeDeployment.CandidateSlot {
		return generatedruntime.Candidate{}, staticError("durable candidate provenance unavailable")
	}
	description, err := generatedruntime.DescribeInactiveCandidate(resolved.deployment.AppID, persisted.Name, generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot))
	if err != nil || description.ContainerName != persisted.ContainerName || string(description.Slot) != persisted.Slot {
		return generatedruntime.Candidate{}, staticError("durable candidate identity mismatch")
	}
	return generatedruntime.Candidate{
		AppID: resolved.deployment.AppID, ReleaseID: resolved.release.ID, DeploymentID: resolved.deployment.ID,
		ArtifactID: artifact.ID, DeploymentPlanRevisionID: resolved.plan.ID, Component: persisted.Name, Role: component.Role,
		Slot: description.Slot, ContainerID: persisted.ContainerID, ContainerName: description.ContainerName,
		NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias, InternalPort: component.InternalPort,
		ImageContentID: artifact.ImageContentID, WorkingDirectory: runtimeWorkingDirectory(component.RootDirectory),
		RunCommandDigest: commandDigest(component.RunCommand),
	}, nil
}

func (e *Executor) switchRoute(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment, candidates map[string]generatedruntime.Candidate) (generatedruntimestate.Deployment, bool, error) {
	head, err := e.state.Active(ctx, resolved.deployment.AppID)
	if err != nil {
		return runtimeDeployment, false, staticError("active head unavailable")
	}
	if head.DeploymentID == resolved.deployment.ID {
		request := generatedruntime.RouteSwitchRequest{
			AppID: resolved.deployment.AppID, FromSlot: generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot),
			ToSlot: generatedruntime.Slot(runtimeDeployment.CandidateSlot), DrainPeriod: e.options.DrainPeriod,
			Endpoints: routeEndpoints(resolved.plan, candidates),
		}
		if err = e.routes.Switch(ctx, request); err != nil {
			// The durable active head already names this candidate. Never clean it
			// up merely because reattesting or reinstalling its route failed.
			return runtimeDeployment, true, staticError("route reconciliation failed")
		}
		updated, advanceErr := e.state.Advance(ctx, resolved.deployment.AppID, resolved.deployment.ID, generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, "")
		return updated, true, advanceErr
	}
	if head.DeploymentID != runtimeDeployment.PreviousActiveDeploymentID || head.Slot != runtimeDeployment.PreviousActiveSlot {
		return runtimeDeployment, false, staticError("active head changed")
	}
	request := generatedruntime.RouteSwitchRequest{
		AppID: resolved.deployment.AppID, FromSlot: generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot),
		ToSlot: generatedruntime.Slot(runtimeDeployment.CandidateSlot), DrainPeriod: e.options.DrainPeriod,
		Endpoints: routeEndpoints(resolved.plan, candidates),
	}
	if err = e.routes.Switch(ctx, request); err != nil {
		return runtimeDeployment, generatedruntime.RouteCandidateMayBeLive(err), staticError("route switch failed")
	}
	if _, _, err = e.state.SwitchActive(ctx, resolved.deployment.AppID, resolved.deployment.ID, head.Generation); err != nil {
		if runtimeDeployment.PreviousActiveDeploymentID == "" {
			// A first deployment has no previous route to restore. The candidate
			// route was already committed, so preserve it until reconciliation can
			// prove and persist the serving state.
			return runtimeDeployment, true, staticError("active head compare-and-swap failed")
		}
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.options.CleanupTimeout)
		restoreErr := e.restorePreviousRoute(rollback, resolved, runtimeDeployment)
		cancel()
		if restoreErr != nil {
			// The route already switched to the candidate, and restoring the
			// previous route was not proven successful. Preserve both slots and
			// require reconciliation rather than deleting a possibly live target.
			return runtimeDeployment, true, staticError("active head compare-and-swap failed")
		}
		return runtimeDeployment, false, staticError("active head compare-and-swap failed")
	}
	updated, err := e.state.Advance(ctx, resolved.deployment.AppID, resolved.deployment.ID, generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, "")
	if err != nil {
		return runtimeDeployment, true, staticError("persist draining phase")
	}
	if runtimeDeployment.PreviousActiveDeploymentID != "" {
		previous, getErr := e.state.Get(ctx, resolved.deployment.AppID, runtimeDeployment.PreviousActiveDeploymentID)
		if getErr != nil {
			return updated, true, staticError("previous deployment unavailable")
		}
		for _, component := range previous.Components {
			if component.State == generatedruntimestate.ComponentActive {
				if _, err = e.state.AdvanceComponent(ctx, resolved.deployment.AppID, previous.DeploymentID, component.Name, generatedruntimestate.ComponentActive, generatedruntimestate.ComponentDraining); err != nil {
					return updated, true, staticError("persist previous draining component")
				}
			}
		}
	}
	return updated, true, nil
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

func (e *Executor) restorePreviousRoute(ctx context.Context, resolved resolvedDeployment, runtimeDeployment generatedruntimestate.Deployment) error {
	if runtimeDeployment.PreviousActiveDeploymentID == "" {
		return nil
	}
	previous, plan, artifacts, err := e.previousResources(ctx, resolved.deployment.AppID, runtimeDeployment.PreviousActiveDeploymentID)
	if err != nil {
		return err
	}
	candidates, err := reconstructPreviousCandidates(resolved.deployment.AppID, previous, plan, artifacts)
	if err != nil {
		return err
	}
	return e.routes.Switch(ctx, generatedruntime.RouteSwitchRequest{
		AppID: resolved.deployment.AppID, FromSlot: generatedruntime.Slot(runtimeDeployment.CandidateSlot),
		ToSlot: generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot), Endpoints: routeEndpoints(plan, candidates),
	})
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
			ArtifactID: artifact.ID, DeploymentPlanRevisionID: plan.ID, Component: persisted.Name, Role: component.Role,
			Slot: description.Slot, ContainerID: persisted.ContainerID, ContainerName: description.ContainerName,
			NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias,
			InternalPort: component.InternalPort, ImageContentID: artifact.ImageContentID,
			WorkingDirectory: runtimeWorkingDirectory(component.RootDirectory), RunCommandDigest: commandDigest(component.RunCommand),
		}
	}
	return result, nil
}

func (e *Executor) cleanupCandidates(ctx context.Context, candidates map[string]generatedruntime.Candidate) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.options.CleanupTimeout)
	defer cancel()
	var firstErr error
	for _, candidate := range candidates {
		if err := e.runtime.StopAndRemove(cleanup, candidate, 0); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
