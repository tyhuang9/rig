package generatedexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
)

const (
	defaultDrainPeriod    = 30 * time.Second
	defaultCleanupTimeout = 45 * time.Second
)

type Executor struct {
	applications  applicationReader
	releases      releaseResolver
	configuration configurationExporter
	deployments   deploymentStore
	plans         planReader
	compiler      imageCompiler
	artifacts     artifactStore
	state         runtimeState
	runtime       runtimeEngine
	authorization AuthorizationGate
	routes        generatedruntime.RouteSwitcher
	migrations    generatedruntime.MigrationRunner
	pinMigration  func(context.Context, deployments.GeneratedMigrationRequest) (generatedMigrationExecution, error)
	options       Options
	waitDrain     func(context.Context, time.Duration) error
}

func NewExecutor(
	applications applicationReader,
	releases releaseResolver,
	configuration configurationExporter,
	deploymentRepository deploymentStore,
	plans planReader,
	compiler imageCompiler,
	artifacts artifactStore,
	state runtimeState,
	runtime runtimeEngine,
	authorization AuthorizationGate,
	routes generatedruntime.RouteSwitcher,
	migrations generatedruntime.MigrationRunner,
	options Options,
) (*Executor, error) {
	if applications == nil || releases == nil || configuration == nil || deploymentRepository == nil || plans == nil ||
		compiler == nil || artifacts == nil || state == nil || runtime == nil || authorization == nil || routes == nil || migrations == nil {
		return nil, errors.New("generated executor dependencies are required")
	}
	if options.DrainPeriod == 0 {
		options.DrainPeriod = defaultDrainPeriod
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = defaultCleanupTimeout
	}
	if options.DrainPeriod < 0 || options.DrainPeriod > 30*time.Second || options.CleanupTimeout < time.Second || options.CleanupTimeout > 5*time.Minute {
		return nil, errors.New("generated executor timeouts are outside supported bounds")
	}
	return &Executor{
		applications: applications, releases: releases, configuration: configuration,
		deployments: deploymentRepository, plans: plans, compiler: compiler, artifacts: artifacts,
		state: state, runtime: runtime, authorization: authorization, routes: routes, migrations: migrations,
		pinMigration: func(ctx context.Context, request deployments.GeneratedMigrationRequest) (generatedMigrationExecution, error) {
			return deploymentRepository.PinGeneratedMigration(ctx, request)
		},
		options: options, waitDrain: waitForDrain,
	}, nil
}

type resolvedDeployment struct {
	deployment          deployments.Deployment
	release             releasesnapshot.Release
	plan                deploymentplans.DeploymentPlanRevision
	configurationID     string
	configurationNumber int64
}

func (e *Executor) ExecuteBound(ctx context.Context, job jobs.Job, claim deploymentexecution.Claim, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if reporter == nil || job.Type != "deploy" || job.ResourceType != "application" ||
		uuid.Validate(job.ResourceID) != nil || uuid.Validate(job.ID) != nil || uuid.Validate(job.RequestedBy) != nil || job.Attempt < 1 {
		return jobs.ExecutionResult{}, executionError("validation_failed")
	}
	if claim.JobID() == "" || claim.ApplicationID() == "" || claim.Attempt() < 1 ||
		claim.JobID() != job.ID || claim.ApplicationID() != job.ResourceID || claim.Attempt() != job.Attempt {
		return jobs.ExecutionResult{}, errors.Join(executionError("validation_failed"), deploymentexecution.ErrInvalidClaim)
	}
	input, err := jobs.DeploymentInputFor(job)
	if err != nil {
		return jobs.ExecutionResult{}, executionError("validation_failed")
	}
	if err := report(reporter, jobs.Running, "validate", 5); err != nil {
		return jobs.ExecutionResult{}, err
	}

	deployment, _, err := e.deployments.GetOrCreateByJob(ctx, job.ResourceID, job.ID, string(input.ConfigurationMode))
	if err != nil {
		return jobs.ExecutionResult{}, executionError("internal_error")
	}
	resolved, err := e.resolve(ctx, job, input, deployment, reporter)
	if err != nil {
		if refusal, ok := e.existingMigrationRefusal(ctx, deployment, err); ok {
			return jobs.ExecutionResult{}, refusal
		}
		return jobs.ExecutionResult{}, e.failMain(ctx, deployment, executionCode(ctx, err))
	}
	deployment = resolved.deployment

	componentNames := make([]string, 0, len(resolved.plan.Plan.Components))
	for _, component := range resolved.plan.Plan.Components {
		componentNames = append(componentNames, component.Name)
	}
	runtimeDeployment, _, err := e.state.Begin(ctx, generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: deployment.AppID, ReleaseID: deployment.ReleaseID,
		DeploymentPlanRevisionID:     deployment.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: deployment.DeploymentPlanRevisionNumber,
		ComponentNames:               componentNames,
	})
	if errors.Is(err, generatedruntimestate.ErrMigrationApprovalRequired) {
		return waitingFor(jobs.PauseMigrationApprovalRequired), nil
	}
	if errors.Is(err, generatedruntimestate.ErrDeploymentInProgress) {
		return waitingFor(jobs.PauseRouteReconciliationRequired), nil
	}
	if err != nil {
		if refusal, ok := e.existingMigrationRefusal(ctx, deployment, err); ok {
			return jobs.ExecutionResult{}, refusal
		}
		return jobs.ExecutionResult{}, e.failMain(ctx, deployment, "internal_error")
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhaseSucceeded {
		// A controller can stop after committing runtime success but before
		// completing its deployment and job rows. Only finish the same pinned
		// deployment while its durable active head still names this candidate.
		head, headErr := e.state.Active(ctx, deployment.AppID)
		if headErr != nil || head.DeploymentID != deployment.ID || head.ReleaseID != deployment.ReleaseID || head.Slot != runtimeDeployment.CandidateSlot {
			return waitingFor(jobs.PauseRouteReconciliationRequired), nil
		}
		if deployment.Status != deployments.Succeeded {
			if _, err := e.deployments.Transition(ctx, deployment.AppID, deployment.ID, deployments.Succeeded, ""); err != nil {
				return jobs.ExecutionResult{}, executionError("internal_error")
			}
		}
		return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhaseFailed || runtimeDeployment.Phase == generatedruntimestate.PhaseCancelled {
		return jobs.ExecutionResult{}, executionError("apply_failed")
	}

	if err := report(reporter, jobs.Running, "apply_runtime", 60); err != nil {
		if generatedCandidateBoundaryRisk(runtimeDeployment) {
			return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
		}
		if runtimeDeployment.Phase == generatedruntimestate.PhaseMigrating {
			return jobs.ExecutionResult{}, generatedMigrationUncertain(ctx, err)
		}
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhasePreflight {
		advanced, advanceErr := e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, "")
		if advanceErr != nil {
			return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
		}
		runtimeDeployment = advanced
	}

	var reservation generatedruntime.ReplacementReservation
	if runtimeDeployment.Phase != generatedruntimestate.PhaseDraining {
		reservation, err = e.authorization.Authorize(ctx, AuthorizationRequest{
			Claim: claim,
			AppID: deployment.AppID, DeploymentID: deployment.ID, ReleaseID: deployment.ReleaseID,
			ConfigurationRevisionID: resolved.configurationID, ConfigurationRevisionNumber: resolved.configurationNumber,
			Findings:                     append([]deployments.Finding(nil), resolved.deployment.Findings...),
			DeploymentPlanRevisionID:     deployment.DeploymentPlanRevisionID,
			DeploymentPlanRevisionNumber: deployment.DeploymentPlanRevisionNumber,
			CandidateSlot:                generatedruntime.Slot(runtimeDeployment.CandidateSlot), ComponentCount: len(componentNames),
		})
		if err != nil && (ctx.Err() != nil || errors.Is(err, deploymentexecution.ErrCanceled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
		}
		if errors.Is(err, ErrInsufficientReplacementCapacity) {
			return waitingFor(jobs.PauseInsufficientReplacementCapacity), nil
		}
		if errors.Is(err, deployments.ErrApprovalRequired) {
			return waitingFor(jobs.PauseApprovalRequired), nil
		}
		if err != nil {
			return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
		}
		if reservation == nil {
			return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, errors.New("generated replacement reservation is unavailable"))
		}
		defer reservation.Release()
	}
	networkCapacityRisk := generatedNetworkCapacityRisk(runtimeDeployment)
	persistedDeployment, getErr := e.deployments.Get(ctx, deployment.AppID, deployment.ID)
	if getErr != nil {
		if generatedCandidateBoundaryRisk(runtimeDeployment) {
			return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, getErr)
		}
		if runtimeDeployment.Phase == generatedruntimestate.PhaseMigrating {
			return jobs.ExecutionResult{}, e.generatedMigrationRefusal(reservation, networkCapacityRisk, generatedMigrationUncertain(ctx, getErr))
		}
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
	}
	deployment = persistedDeployment
	networkRequest := generatedNetworkRequest(claim, resolved)

	artifacts := make(map[string]generatedimage.Artifact, len(componentNames))
	invocations := make(map[string]deployments.GeneratedCandidateInvocation, len(componentNames))
	var candidateExecution *deployments.GeneratedCandidateExecution
	defer func() {
		if candidateExecution != nil {
			candidateExecution.Leave()
		}
	}()
	for {
		switch runtimeDeployment.Phase {
		case generatedruntimestate.PhaseBuilding:
			artifacts, err = e.build(ctx, claim, resolved, runtimeDeployment)
			if err != nil {
				// Every refusal while interpreting or compiling the durable Building
				// aggregate is image-boundary uncertainty. In particular, malformed
				// candidate evidence must retain capacity and must not flow through
				// detached terminal writers.
				return jobs.ExecutionResult{}, e.generatedImageRefusal(ctx, reservation, networkCapacityRisk, err)
			}
			if err = e.runtime.EnsureAppNetwork(ctx, networkRequest); err != nil {
				if generatedruntime.IsGeneratedNetworkBoundaryFailure(err) {
					return jobs.ExecutionResult{}, e.generatedNetworkRefusal(ctx, reservation, networkCapacityRisk, err)
				}
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticRuntimeUnavailable)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			next, finishErr := e.deployments.FinishGeneratedImagePreparation(ctx, networkRequest)
			if finishErr != nil {
				return jobs.ExecutionResult{}, e.generatedImageRefusal(ctx, reservation, networkCapacityRisk, finishErr)
			}
			advanced, getErr := e.state.Get(ctx, deployment.AppID, deployment.ID)
			if getErr != nil {
				return jobs.ExecutionResult{}, e.generatedImageRefusal(ctx, reservation, networkCapacityRisk, getErr)
			}
			if advanced.Phase != next {
				return jobs.ExecutionResult{}, e.generatedImageRefusal(ctx, reservation, networkCapacityRisk, deployments.ErrInvalidDeployment)
			}
			runtimeDeployment = advanced

		case generatedruntimestate.PhaseMigrating:
			if len(artifacts) == 0 {
				artifacts, err = e.loadArtifacts(ctx, resolved, runtimeDeployment)
				if err != nil {
					return jobs.ExecutionResult{}, e.generatedMigrationRefusal(reservation, networkCapacityRisk, generatedMigrationUncertain(ctx, err))
				}
			}
			if runtimeDeployment.MigrationState == generatedruntimestate.MigrationPending {
				if err = e.runtime.EnsureAppNetwork(ctx, networkRequest); err != nil {
					return jobs.ExecutionResult{}, e.generatedMigrationRefusal(reservation, networkCapacityRisk, generatedMigrationUncertain(ctx, err))
				}
			}
			runtimeDeployment, err = e.runMigration(ctx, claim, resolved, runtimeDeployment, artifacts)
			if err != nil {
				return jobs.ExecutionResult{}, e.generatedMigrationRefusal(reservation, networkCapacityRisk, err)
			}

		case generatedruntimestate.PhaseStartingCandidate:
			if candidateExecution == nil {
				candidateExecution, err = e.runtime.PinCandidates(ctx, candidateRequest(claim, resolved, runtimeDeployment.CandidateSlot), reservation)
				if err != nil {
					return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
				}
			}
			if err = e.runtime.EnsureAppNetwork(ctx, networkRequest); err != nil {
				return jobs.ExecutionResult{}, e.generatedNetworkRefusal(ctx, reservation, networkCapacityRisk, err)
			}
			invocations, err = e.startCandidates(candidateExecution, runtimeDeployment)
			if err != nil {
				return jobs.ExecutionResult{}, e.candidateFailure(ctx, candidateExecution, reservation, invocations, err)
			}
			advanced, advanceErr := candidateExecution.FinishStarting()
			if advanceErr != nil {
				return jobs.ExecutionResult{}, e.candidateFailure(ctx, candidateExecution, reservation, invocations, advanceErr)
			}
			runtimeDeployment = advanced
			deployment.Status = deployments.WaitingHealth

		case generatedruntimestate.PhaseWaitingHealth:
			if candidateExecution == nil {
				candidateExecution, err = e.runtime.PinCandidates(ctx, candidateRequest(claim, resolved, runtimeDeployment.CandidateSlot), reservation)
				if err != nil {
					return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
				}
			}
			if err = report(reporter, jobs.Running, "wait_for_health", 85); err != nil {
				return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
			}
			if err = e.healthyCandidates(candidateExecution, runtimeDeployment, invocations); err != nil {
				return jobs.ExecutionResult{}, e.candidateFailure(ctx, candidateExecution, reservation, invocations, err)
			}
			advanced, advanceErr := candidateExecution.FinishHealth()
			if advanceErr != nil {
				return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, advanceErr)
			}
			runtimeDeployment = advanced

		case generatedruntimestate.PhaseSwitchingRoute:
			if candidateExecution == nil {
				candidateExecution, err = e.runtime.PinCandidates(ctx, candidateRequest(claim, resolved, runtimeDeployment.CandidateSlot), reservation)
				if err != nil {
					return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, err)
				}
			}
			candidates, observeErr := observeRouteCandidates(candidateExecution, deployment.AppID, runtimeDeployment)
			if observeErr != nil {
				return jobs.ExecutionResult{}, generatedAuthorizationRefusal(ctx, observeErr)
			}
			advanced, _, switchErr := e.switchRoute(ctx, resolved, runtimeDeployment, candidates)
			if switchErr != nil {
				// FinishHealth irreversibly closed inactive cleanup before route
				// publication. Route failures always preserve these candidates.
				return waitingFor(jobs.PauseRouteReconciliationRequired), nil
			}
			runtimeDeployment = advanced

		case generatedruntimestate.PhaseDraining:
			if err = e.drainPrevious(ctx, resolved, runtimeDeployment); err != nil {
				// The new slot is already active. Keep the deployment and job
				// nonterminal so retry or restart recovery can finish old-slot cleanup.
				return waitingFor(jobs.PauseRouteReconciliationRequired), nil
			}
			advanced, advanceErr := e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, "")
			if advanceErr != nil {
				return waitingFor(jobs.PauseRouteReconciliationRequired), nil
			}
			runtimeDeployment = advanced
			if err = report(reporter, jobs.Running, "finalize", 100); err != nil {
				return jobs.ExecutionResult{}, e.failMain(ctx, deployment, "internal_error")
			}
			if deployment.Status != deployments.Succeeded {
				if _, err = e.deployments.Transition(ctx, deployment.AppID, deployment.ID, deployments.Succeeded, ""); err != nil {
					return jobs.ExecutionResult{}, executionError("internal_error")
				}
			}
			return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil

		default:
			return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
		}
	}
}

func generatedAuthorizationRefusal(ctx context.Context, cause error) error {
	refusal := executionError("runtime_unavailable")
	if ctx != nil && ctx.Err() != nil || errors.Is(cause, deploymentexecution.ErrCanceled) ||
		errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		if ctx != nil && ctx.Err() != nil {
			return errors.Join(refusal, cause, deploymentexecution.ErrCanceled, ctx.Err())
		}
		return errors.Join(refusal, cause)
	}
	return errors.Join(refusal, cause)
}

func generatedNetworkRequest(claim deploymentexecution.Claim, resolved resolvedDeployment) deployments.GeneratedNetworkRequest {
	return deployments.GeneratedNetworkRequest{
		Claim: claim, DeploymentID: resolved.deployment.ID, ReleaseID: resolved.deployment.ReleaseID,
		ConfigurationRevisionID: resolved.configurationID, ConfigurationRevisionNumber: resolved.configurationNumber,
		DeploymentPlanRevisionID: resolved.plan.ID, DeploymentPlanRevisionNumber: resolved.plan.RevisionNumber,
		Findings: append([]deployments.Finding(nil), resolved.deployment.Findings...),
	}
}

func (e *Executor) generatedImageRefusal(ctx context.Context, reservation generatedruntime.ReplacementReservation, retain bool, cause error) error {
	if retain {
		cause = errors.Join(cause, e.runtime.RetainReplacementCapacity(reservation))
	}
	return generatedAuthorizationRefusal(ctx, cause)
}

func generatedNetworkCapacityRisk(deployment generatedruntimestate.Deployment) bool {
	for _, component := range deployment.Components {
		if component.ContainerName != "" || component.ContainerID != "" {
			return true
		}
		switch component.State {
		case generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady:
		default:
			return true
		}
	}
	return false
}

func (e *Executor) generatedNetworkRefusal(ctx context.Context, reservation generatedruntime.ReplacementReservation, retain bool, cause error) error {
	if retain {
		cause = errors.Join(cause, e.runtime.RetainReplacementCapacity(reservation))
	}
	return generatedAuthorizationRefusal(ctx, cause)
}

func (e *Executor) generatedMigrationRefusal(reservation generatedruntime.ReplacementReservation, retain bool, cause error) error {
	if retain {
		cause = errors.Join(cause, e.runtime.RetainReplacementCapacity(reservation))
	}
	return cause
}

func (e *Executor) existingMigrationRefusal(ctx context.Context, deployment deployments.Deployment, cause error) (error, bool) {
	if deployment.ID == "" || deployment.AppID == "" {
		return generatedMigrationUncertain(ctx, errors.Join(cause, deployments.ErrInvalidDeployment)), true
	}
	persisted, err := e.state.Get(ctx, deployment.AppID, deployment.ID)
	if errors.Is(err, generatedruntimestate.ErrNotFound) {
		return nil, false
	}
	if err != nil {
		return generatedMigrationUncertain(ctx, errors.Join(cause, err)), true
	}
	if generatedCandidateBoundaryRisk(persisted) {
		return generatedAuthorizationRefusal(ctx, cause), true
	}
	if persisted.Phase != generatedruntimestate.PhaseMigrating {
		return nil, false
	}
	return generatedMigrationUncertain(ctx, cause), true
}

// Durable phase and identity remain evidence even before a current invocation
// map exists. No generic failure writer may terminalize these candidates.
func generatedCandidateBoundaryRisk(value generatedruntimestate.Deployment) bool {
	switch value.Phase {
	case generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth,
		generatedruntimestate.PhaseSwitchingRoute, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded:
		return true
	}
	for _, component := range value.Components {
		if component.ContainerName != "" || component.ContainerID != "" {
			return true
		}
	}
	return false
}

func (e *Executor) resolve(ctx context.Context, job jobs.Job, input jobs.DeploymentInput, deployment deployments.Deployment, reporter jobs.ProgressReporter) (resolvedDeployment, error) {
	application, err := e.applications.Get(job.ResourceID)
	if err != nil {
		return resolvedDeployment{}, codedError("internal_error")
	}
	if !deployment.ProvenanceInitialized && input.ReleaseID == "" && !input.HasReviewedRevisions() {
		return resolvedDeployment{}, codedError("reviewed_revisions_required")
	}
	if deployment.ProvenanceInitialized && input.HasReviewedRevisions() &&
		(deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode || deployment.DeploymentPlanRevisionID != input.ExpectedPlanRevisionID || deployment.DeploymentPlanRevisionNumber != input.ExpectedPlanRevisionNumber ||
			deployment.ActualConfigurationRevisionID != input.ExpectedConfigurationRevisionID || deployment.ActualConfigurationRevisionNumber != input.ExpectedConfigurationRevisionNumber) {
		return resolvedDeployment{}, codedError("invalid_source")
	}
	if input.HasReviewedRevisions() && !deployment.ProvenanceInitialized {
		if err := e.checkReviewedHeads(ctx, job.ResourceID, input); err != nil {
			return resolvedDeployment{}, err
		}
	}
	if err := report(reporter, jobs.Running, "prepare_workspace", 20); err != nil {
		return resolvedDeployment{}, err
	}

	var release releasesnapshot.Release
	if deployment.ProvenanceInitialized {
		release, err = e.releases.ReadyWorkspace(ctx, job.ResourceID, deployment.ReleaseID)
	} else if input.ReleaseID != "" {
		release, err = e.releases.ReadyWorkspace(ctx, job.ResourceID, input.ReleaseID)
	} else {
		if err = report(reporter, jobs.Waiting, "materialize_release", 30); err != nil {
			return resolvedDeployment{}, err
		}
		switch application.Source.Type {
		case apps.SourceGitHub:
			release, err = e.releases.Materialize(ctx, job.RequestedBy, job.ResourceID)
		case apps.SourceLocal:
			release, err = e.releases.MaterializeLocal(ctx, job.ResourceID, application.Source.Path)
		default:
			return resolvedDeployment{}, codedError("invalid_source")
		}
	}
	if err != nil {
		return resolvedDeployment{}, err
	}
	if release.ID == "" || release.DeploymentPlanRevisionID == "" || release.DeploymentPlanRevisionNumber < 1 {
		return resolvedDeployment{}, codedError("invalid_source")
	}
	if input.HasReviewedRevisions() && !deployment.ProvenanceInitialized {
		if err := e.checkReviewedHeads(ctx, job.ResourceID, input); err != nil {
			return resolvedDeployment{}, err
		}
		if release.DeploymentPlanRevisionID != input.ExpectedPlanRevisionID || release.DeploymentPlanRevisionNumber != input.ExpectedPlanRevisionNumber {
			return resolvedDeployment{}, codedError("reviewed_revision_stale")
		}
	}

	plan, err := e.plans.GetRevision(ctx, job.ResourceID, release.DeploymentPlanRevisionID, release.DeploymentPlanRevisionNumber)
	if err != nil {
		return resolvedDeployment{}, codedError("internal_error")
	}
	if plan.ID != release.DeploymentPlanRevisionID || plan.RevisionNumber != release.DeploymentPlanRevisionNumber ||
		plan.AppID != job.ResourceID || plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode || len(plan.Plan.Components) == 0 {
		return resolvedDeployment{}, codedError("invalid_source")
	}

	var configurationID string
	var configurationNumber int64
	if deployment.ProvenanceInitialized {
		if deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode || deployment.DeploymentPlanRevisionID != plan.ID || deployment.DeploymentPlanRevisionNumber != plan.RevisionNumber {
			return resolvedDeployment{}, codedError("invalid_source")
		}
		configurationID, configurationNumber = deployment.ActualConfigurationRevisionID, deployment.ActualConfigurationRevisionNumber
	} else if input.ConfigurationMode == jobs.ConfigurationOriginal {
		configurationID, configurationNumber = release.ConfigurationRevisionID, release.ConfigurationRevisionNumber
	} else if input.HasReviewedRevisions() {
		configurationID, configurationNumber = input.ExpectedConfigurationRevisionID, input.ExpectedConfigurationRevisionNumber
	} else {
		var identity appconfig.RevisionIdentity
		identity, err = e.configuration.RevisionIdentity(ctx, job.ResourceID)
		if err != nil {
			return resolvedDeployment{}, codedError("configuration_unavailable")
		}
		configurationID, configurationNumber = identity.RevisionID, identity.RevisionNumber
	}
	if configurationNumber < 0 || (configurationNumber == 0) != (configurationID == "") {
		return resolvedDeployment{}, codedError("configuration_unavailable")
	}
	if configurationNumber > 0 {
		identity, identityErr := e.configuration.ExactRevisionIdentity(ctx, job.ResourceID, configurationID, configurationNumber)
		if identityErr != nil {
			return resolvedDeployment{}, codedError("configuration_unavailable")
		}
		if identity.RevisionID != configurationID || identity.RevisionNumber != configurationNumber || identity.FormatVersion < 2 || identity.DeploymentPlanRevisionID != plan.ID || identity.DeploymentPlanRevisionNumber != plan.RevisionNumber {
			return resolvedDeployment{}, codedError("configuration_review_required")
		}
	}

	if !deployment.ProvenanceInitialized {
		deployment, err = e.deployments.InitializeRuntime(ctx, job.ResourceID, deployment.ID, release.ID, configurationID, configurationNumber, deployments.RuntimeGeneratedNode, plan.ID, plan.RevisionNumber)
		if err != nil {
			return resolvedDeployment{}, codedError("internal_error")
		}
	}
	return resolvedDeployment{deployment: deployment, release: release, plan: plan, configurationID: configurationID, configurationNumber: configurationNumber}, nil
}

func (e *Executor) checkReviewedHeads(ctx context.Context, appID string, input jobs.DeploymentInput) error {
	plan, err := e.plans.Get(ctx, appID)
	if err != nil || plan.ID != input.ExpectedPlanRevisionID || plan.RevisionNumber != input.ExpectedPlanRevisionNumber ||
		plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode || plan.State != deploymentplans.RevisionAccepted {
		return codedError("reviewed_revision_stale")
	}
	configuration, err := e.configuration.RevisionIdentity(ctx, appID)
	if err != nil || configuration.RevisionID != input.ExpectedConfigurationRevisionID || configuration.RevisionNumber != input.ExpectedConfigurationRevisionNumber {
		return codedError("reviewed_revision_stale")
	}
	if configuration.RevisionNumber > 0 && (configuration.FormatVersion != 2 || configuration.DeploymentPlanRevisionID != input.ExpectedPlanRevisionID || configuration.DeploymentPlanRevisionNumber != input.ExpectedPlanRevisionNumber) {
		return codedError("reviewed_revision_stale")
	}
	return nil
}

func report(reporter jobs.ProgressReporter, status jobs.Status, phase string, progress int) error {
	return reporter.Report(jobs.ProgressUpdate{Status: status, Phase: phase, Progress: progress, Code: "phase_started"})
}

func waitingFor(disposition string) jobs.ExecutionResult {
	return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser, PauseDisposition: disposition}
}

type runtimeError struct{ code string }

func (e *runtimeError) Error() string  { return e.code }
func codedError(code string) error     { return &runtimeError{code: code} }
func executionError(code string) error { return &jobs.ExecutionError{Code: code} }

func executionCode(ctx context.Context, err error) string {
	var runtimeErr *runtimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr.code
	}
	var releaseErr *releasesnapshot.Error
	if errors.As(err, &releaseErr) {
		switch releaseErr.Code {
		case "invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full", "provider_unavailable":
			return releaseErr.Code
		case "release_not_found":
			return "invalid_source"
		}
	}
	if errors.Is(err, context.Canceled) {
		return cancellationCode(ctx)
	}
	return "internal_error"
}

func cancellationCode(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), jobs.ErrCancellationRequested) {
		return "cancelled"
	}
	return "internal_error"
}

func runtimeFailure(ctx context.Context, err error, fallback generatedruntimestate.DiagnosticCode) (string, generatedruntimestate.DiagnosticCode) {
	var runtimeErr *runtimeError
	if (errors.As(err, &runtimeErr) && runtimeErr.code == "configuration_review_required") || generatedimage.IsCompileCode(err, "configuration_review_required") {
		return "configuration_review_required", generatedruntimestate.DiagnosticInternalError
	}
	if errors.As(err, &runtimeErr) && runtimeErr.code == "configuration_unavailable" {
		return "configuration_unavailable", generatedruntimestate.DiagnosticInternalError
	}
	if generatedimage.IsCompileCode(err, "build_configuration_requires_build_command") {
		return "build_configuration_requires_build_command", generatedruntimestate.DiagnosticInternalError
	}
	if generatedimage.IsCompileCode(err, "configuration_unavailable") {
		return "configuration_unavailable", generatedruntimestate.DiagnosticInternalError
	}
	if errors.Is(err, context.Canceled) || generatedruntime.IsCode(err, generatedruntime.DiagnosticCancelled) || generatedimage.IsCompileCode(err, string(generatedimage.DiagnosticBuildCancelled)) {
		return cancellationCode(ctx), generatedruntimestate.DiagnosticCancelled
	}
	if generatedruntime.IsCode(err, generatedruntime.DiagnosticRuntimeUnavailable) || generatedimage.IsCompileCode(err, "runtime_unavailable") {
		return "runtime_unavailable", generatedruntimestate.DiagnosticRuntimeUnavailable
	}
	if generatedruntime.IsCode(err, generatedruntime.DiagnosticProcessTerminationFailed) || generatedimage.IsCompileCode(err, "process_termination_failed") {
		return "process_termination_failed", generatedruntimestate.DiagnosticRuntimeUnavailable
	}
	if generatedruntime.IsCode(err, generatedruntime.DiagnosticCandidateCleanupFailed) {
		return "apply_failed", generatedruntimestate.DiagnosticRuntimeUnavailable
	}
	if fallback == generatedruntimestate.DiagnosticHealthFailed {
		return "health_failed", fallback
	}
	return "apply_failed", fallback
}

func (e *Executor) fail(ctx context.Context, deployment deployments.Deployment, runtimeDeployment generatedruntimestate.Deployment, code string, diagnostic generatedruntimestate.DiagnosticCode) error {
	finalize, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if persisted, err := e.state.Get(finalize, runtimeDeployment.AppID, runtimeDeployment.DeploymentID); err == nil {
		runtimeDeployment = persisted
	} else if !errors.Is(err, generatedruntimestate.ErrNotFound) {
		return generatedAuthorizationRefusal(ctx, err)
	}
	if generatedCandidateBoundaryRisk(runtimeDeployment) {
		return generatedAuthorizationRefusal(ctx, executionError(code))
	}
	if runtimeDeployment.DeploymentID != "" && runtimeDeployment.Phase != generatedruntimestate.PhaseFailed && runtimeDeployment.Phase != generatedruntimestate.PhaseCancelled && runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded {
		componentDiagnostic, markComponents := failureComponentDiagnostic(diagnostic)
		if markComponents {
			for _, component := range runtimeDeployment.Components {
				switch component.State {
				case generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady,
					generatedruntimestate.ComponentStarting, generatedruntimestate.ComponentRunning,
					generatedruntimestate.ComponentHealthy:
					_, _ = e.state.FailComponent(finalize, runtimeDeployment.AppID, runtimeDeployment.DeploymentID, component.Name, component.State, componentDiagnostic)
				}
			}
		}
		next := generatedruntimestate.PhaseFailed
		if diagnostic == generatedruntimestate.DiagnosticCancelled {
			next = generatedruntimestate.PhaseCancelled
		}
		if _, err := e.state.Advance(finalize, runtimeDeployment.AppID, runtimeDeployment.DeploymentID, runtimeDeployment.Phase, next, diagnostic); err != nil && !errors.Is(err, generatedruntimestate.ErrInvalidTransition) {
			code = "internal_error"
		}
	}
	return e.failMain(finalize, deployment, code)
}

func failureComponentDiagnostic(diagnostic generatedruntimestate.DiagnosticCode) (generatedruntimestate.DiagnosticCode, bool) {
	switch diagnostic {
	case generatedruntimestate.DiagnosticStartFailed, generatedruntimestate.DiagnosticHealthFailed,
		generatedruntimestate.DiagnosticRuntimeUnavailable, generatedruntimestate.DiagnosticDaemonRestarted,
		generatedruntimestate.DiagnosticCancelled, generatedruntimestate.DiagnosticInternalError:
		return diagnostic, true
	default:
		return "", false
	}
}

func (e *Executor) failMain(ctx context.Context, deployment deployments.Deployment, code string) error {
	if deployment.ID == "" {
		return executionError(code)
	}
	if persisted, err := e.state.Get(ctx, deployment.AppID, deployment.ID); err == nil {
		if generatedCandidateBoundaryRisk(persisted) {
			return generatedAuthorizationRefusal(ctx, executionError(code))
		}
	} else if !errors.Is(err, generatedruntimestate.ErrNotFound) {
		return generatedAuthorizationRefusal(ctx, err)
	}
	status := deployments.Failed
	if code == "cancelled" {
		status = deployments.Cancelled
	}
	if deployment.Status == deployments.Succeeded || deployment.Status == deployments.Failed || deployment.Status == deployments.Cancelled {
		return executionError(code)
	}
	if _, err := e.deployments.Transition(ctx, deployment.AppID, deployment.ID, status, code); err != nil && !errors.Is(err, deployments.ErrInvalidTransition) {
		return &jobs.ExecutionError{Code: "internal_error", Detail: "persist generated deployment terminal state"}
	}
	return executionError(code)
}

func waitForDrain(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func componentPlan(revision deploymentplans.DeploymentPlanRevision, name string) (deploymentplans.Component, bool) {
	for _, component := range revision.Plan.Components {
		if component.Name == name {
			return component, true
		}
	}
	return deploymentplans.Component{}, false
}

func sortedComponents(components []generatedruntimestate.Component) []generatedruntimestate.Component {
	result := append([]generatedruntimestate.Component(nil), components...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func commandDigest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

func runtimeWorkingDirectory(root string) string {
	if root == "." {
		return "/workspace"
	}
	return "/workspace/" + root
}

func staticError(label string) error { return fmt.Errorf("generated executor: %s", label) }
