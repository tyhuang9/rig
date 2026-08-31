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
	options       Options
	waitDrain     func(context.Context, time.Duration) error
}

var _ jobs.Executor = (*Executor)(nil)

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

func (e *Executor) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if reporter == nil || job.Type != "deploy" || job.ResourceType != "application" ||
		uuid.Validate(job.ResourceID) != nil || uuid.Validate(job.ID) != nil || uuid.Validate(job.RequestedBy) != nil || job.Attempt < 1 {
		return jobs.ExecutionResult{}, executionError("validation_failed")
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
	if err != nil {
		return jobs.ExecutionResult{}, e.failMain(ctx, deployment, "internal_error")
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhaseSucceeded {
		return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhaseFailed || runtimeDeployment.Phase == generatedruntimestate.PhaseCancelled {
		return jobs.ExecutionResult{}, executionError("apply_failed")
	}

	if err := report(reporter, jobs.Running, "apply_runtime", 60); err != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhasePreflight {
		runtimeDeployment, err = e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, "")
		if err != nil {
			return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
		}
	}

	reservation, err := e.authorization.Authorize(ctx, AuthorizationRequest{
		AppID: deployment.AppID, DeploymentID: deployment.ID, ReleaseID: deployment.ReleaseID,
		DeploymentPlanRevisionID:     deployment.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: deployment.DeploymentPlanRevisionNumber,
		CandidateSlot:                generatedruntime.Slot(runtimeDeployment.CandidateSlot), ComponentCount: len(componentNames),
	})
	if errors.Is(err, ErrInsufficientReplacementCapacity) {
		return waitingFor(jobs.PauseInsufficientReplacementCapacity), nil
	}
	if errors.Is(err, deployments.ErrApprovalRequired) {
		return waitingFor(jobs.PauseApprovalRequired), nil
	}
	if err != nil || reservation == nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
	}
	defer reservation.Release()
	deployment, err = e.deployments.Get(ctx, deployment.AppID, deployment.ID)
	if err != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
	}

	artifacts := make(map[string]generatedimage.Artifact, len(componentNames))
	candidates := make(map[string]generatedruntime.Candidate, len(componentNames))
	for {
		switch runtimeDeployment.Phase {
		case generatedruntimestate.PhaseBuilding:
			artifacts, err = e.build(ctx, resolved, runtimeDeployment)
			if err != nil {
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticRuntimeUnavailable)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			if err = e.runtime.EnsureAppNetwork(ctx, deployment.AppID); err != nil {
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticRuntimeUnavailable)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			next := generatedruntimestate.PhaseStartingCandidate
			if runtimeDeployment.MigrationState == generatedruntimestate.MigrationPending {
				next = generatedruntimestate.PhaseMigrating
			}
			runtimeDeployment, err = e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhaseBuilding, next, "")
			if err != nil {
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
			}

		case generatedruntimestate.PhaseMigrating:
			if len(artifacts) == 0 {
				artifacts, err = e.loadArtifacts(ctx, resolved, runtimeDeployment)
				if err != nil {
					return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
				}
			}
			if err = e.runtime.EnsureAppNetwork(ctx, deployment.AppID); err != nil {
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticRuntimeUnavailable)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			runtimeDeployment, err = e.runMigration(ctx, resolved, runtimeDeployment, artifacts)
			if err != nil {
				code := "apply_failed"
				diagnostic := generatedruntimestate.DiagnosticMigrationFailed
				if errors.Is(err, errMigrationInterrupted) {
					code, diagnostic = "internal_error", generatedruntimestate.DiagnosticDaemonRestarted
				}
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			runtimeDeployment, err = e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate, "")
			if err != nil {
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
			}

		case generatedruntimestate.PhaseStartingCandidate:
			if len(artifacts) == 0 {
				artifacts, err = e.loadArtifacts(ctx, resolved, runtimeDeployment)
				if err != nil {
					return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
				}
			}
			if err = e.runtime.EnsureAppNetwork(ctx, deployment.AppID); err != nil {
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticRuntimeUnavailable)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			candidates, err = e.startCandidates(ctx, job, resolved, runtimeDeployment, artifacts, reservation)
			if err != nil {
				if cleanupErr := e.cleanupCandidates(ctx, candidates); cleanupErr != nil {
					err = cleanupErr
				}
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticStartFailed)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			runtimeDeployment, err = e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, "")
			if err != nil {
				_ = e.cleanupCandidates(ctx, candidates)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
			}
			if deployment.Status == deployments.Applying {
				deployment, err = e.deployments.Transition(ctx, deployment.AppID, deployment.ID, deployments.WaitingHealth, "")
				if err != nil {
					_ = e.cleanupCandidates(ctx, candidates)
					return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
				}
			}

		case generatedruntimestate.PhaseWaitingHealth:
			if err = report(reporter, jobs.Running, "wait_for_health", 85); err != nil {
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
			}
			if len(artifacts) == 0 {
				artifacts, err = e.loadArtifacts(ctx, resolved, runtimeDeployment)
				if err != nil {
					return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
				}
			}
			candidates, err = e.healthyCandidates(ctx, resolved, runtimeDeployment, artifacts, candidates, reservation)
			if err != nil {
				if cleanupErr := e.cleanupCandidates(ctx, candidates); cleanupErr != nil {
					err = cleanupErr
				}
				code, diagnostic := runtimeFailure(ctx, err, generatedruntimestate.DiagnosticHealthFailed)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, code, diagnostic)
			}
			runtimeDeployment, err = e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute, "")
			if err != nil {
				_ = e.cleanupCandidates(ctx, candidates)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
			}

		case generatedruntimestate.PhaseSwitchingRoute:
			if len(artifacts) == 0 {
				artifacts, err = e.loadArtifacts(ctx, resolved, runtimeDeployment)
				if err != nil {
					return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
				}
			}
			candidates, err = e.reconstructCandidates(resolved, runtimeDeployment, artifacts, reservation)
			if err != nil {
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "internal_error", generatedruntimestate.DiagnosticInternalError)
			}
			var switched bool
			runtimeDeployment, switched, err = e.switchRoute(ctx, resolved, runtimeDeployment, candidates)
			if err != nil {
				if switched {
					return jobs.ExecutionResult{}, e.failMain(ctx, deployment, "apply_failed")
				}
				_ = e.cleanupCandidates(ctx, candidates)
				return jobs.ExecutionResult{}, e.fail(ctx, deployment, runtimeDeployment, "apply_failed", generatedruntimestate.DiagnosticRouteSwitchFailed)
			}

		case generatedruntimestate.PhaseDraining:
			if err = e.drainPrevious(ctx, resolved, runtimeDeployment); err != nil {
				// The new slot is already active. Keep Draining recoverable and
				// never tear down the serving candidate on cleanup failure.
				return jobs.ExecutionResult{}, e.failMain(ctx, deployment, "apply_failed")
			}
			for _, candidate := range candidates {
				e.runtime.ReleaseAdmission(candidate)
			}
			runtimeDeployment, err = e.state.Advance(ctx, deployment.AppID, deployment.ID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, "")
			if err != nil {
				return jobs.ExecutionResult{}, e.failMain(ctx, deployment, "internal_error")
			}
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

func (e *Executor) resolve(ctx context.Context, job jobs.Job, input jobs.DeploymentInput, deployment deployments.Deployment, reporter jobs.ProgressReporter) (resolvedDeployment, error) {
	application, err := e.applications.Get(job.ResourceID)
	if err != nil {
		return resolvedDeployment{}, codedError("invalid_source")
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

	plan, err := e.plans.GetRevision(ctx, job.ResourceID, release.DeploymentPlanRevisionID, release.DeploymentPlanRevisionNumber)
	if err != nil || plan.ID != release.DeploymentPlanRevisionID || plan.RevisionNumber != release.DeploymentPlanRevisionNumber ||
		plan.AppID != job.ResourceID || plan.Plan.Strategy != deploymentplans.StrategyGeneratedNode || len(plan.Plan.Components) == 0 {
		return resolvedDeployment{}, codedError("invalid_source")
	}

	var configuration appconfig.ExecutionConfiguration
	if deployment.ProvenanceInitialized {
		if deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode || deployment.DeploymentPlanRevisionID != plan.ID || deployment.DeploymentPlanRevisionNumber != plan.RevisionNumber {
			return resolvedDeployment{}, codedError("invalid_source")
		}
		configuration, err = e.configuration.ExportRevisionForExecution(ctx, job.ResourceID, deployment.ActualConfigurationRevisionID, deployment.ActualConfigurationRevisionNumber)
	} else if input.ConfigurationMode == jobs.ConfigurationOriginal {
		configuration, err = e.configuration.ExportRevisionForExecution(ctx, job.ResourceID, release.ConfigurationRevisionID, release.ConfigurationRevisionNumber)
	} else {
		configuration, err = e.configuration.ExportCurrentForExecution(ctx, job.ResourceID)
	}
	if err != nil {
		return resolvedDeployment{}, codedError("configuration_unavailable")
	}
	configurationID, configurationNumber := configuration.RevisionID, configuration.RevisionNumber
	configuration.Clear()

	if !deployment.ProvenanceInitialized {
		deployment, err = e.deployments.InitializeRuntime(ctx, job.ResourceID, deployment.ID, release.ID, configurationID, configurationNumber, deployments.RuntimeGeneratedNode, plan.ID, plan.RevisionNumber)
		if err != nil {
			return resolvedDeployment{}, codedError("internal_error")
		}
	}
	return resolvedDeployment{deployment: deployment, release: release, plan: plan, configurationID: configurationID, configurationNumber: configurationNumber}, nil
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
