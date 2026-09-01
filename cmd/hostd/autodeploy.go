package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/sourceconnections"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const autoDeployShutdownTimeout = 10 * time.Second

type autoDeployRunner interface {
	Run(context.Context) error
	Wake()
	Reconcile()
}

type autoDeployFactory func() (autoDeployRunner, error)

// startAutoDeploy is safe-by-default. Auto-deploy is an optional coordinator;
// its absence or failure never affects the manual deployment worker.
func startAutoDeploy(ctx context.Context, cfg config.Config, logger *slog.Logger, factory autoDeployFactory) (<-chan struct{}, func(), func()) {
	done := make(chan struct{})
	noopWake := func() {}
	if (!cfg.ComposeRuntime && !cfg.GeneratedRuntime) || !cfg.GitHubConnectionsEnabled() {
		close(done)
		return done, noopWake, noopWake
	}
	target := &autoDeployWakeTarget{}
	go func() {
		defer close(done)
		if factory == nil {
			logger.Warn("github auto-deploy unavailable", "outcome", autodeploy.OutcomePersistenceUnavailable)
			return
		}
		runner, err := factory()
		if err != nil || runner == nil {
			logger.Warn("github auto-deploy unavailable", "outcome", autodeploy.OutcomePersistenceUnavailable)
			return
		}
		target.set(runner)
		if runErr := runner.Run(ctx); runErr != nil {
			logger.Warn("github auto-deploy stopped", "outcome", autodeploy.OutcomePersistenceUnavailable)
		}
	}()
	return done, target.Wake, target.Reconcile
}

type autoDeployWakeTarget struct {
	mutex            sync.Mutex
	runner           autoDeployRunner
	pending          bool
	reconcilePending bool
}

func (target *autoDeployWakeTarget) Reconcile() {
	target.mutex.Lock()
	runner := target.runner
	if runner == nil {
		target.reconcilePending = true
	}
	target.mutex.Unlock()
	if runner != nil {
		runner.Reconcile()
	}
}

func (target *autoDeployWakeTarget) Wake() {
	target.mutex.Lock()
	runner := target.runner
	if runner == nil {
		target.pending = true
	}
	target.mutex.Unlock()
	if runner != nil {
		runner.Wake()
	}
}

func (target *autoDeployWakeTarget) set(runner autoDeployRunner) {
	target.mutex.Lock()
	target.runner = runner
	pending := target.pending
	reconcilePending := target.reconcilePending
	target.pending = false
	target.reconcilePending = false
	target.mutex.Unlock()
	if reconcilePending {
		runner.Reconcile()
	} else if pending {
		runner.Wake()
	}
}

func waitForAutoDeploy(done <-chan struct{}, timeout time.Duration, logger *slog.Logger) bool {
	if waitForWorker(done, timeout) {
		return true
	}
	logger.Warn("github auto-deploy did not stop before shutdown timeout", "outcome", autodeploy.OutcomePersistenceUnavailable)
	return false
}

func newAutoDeployRunner(cfg config.Config, repository *autodeploy.Repository, sources *sourceconnections.Service, jobService *jobs.Service, logger *slog.Logger) (autoDeployRunner, error) {
	if cfg.GeneratedRuntime {
		return nil, errors.New("github auto-deploy is unavailable")
	}
	return newGeneratedAwareAutoDeployRunner(cfg, repository, sources, jobService, nil, logger)
}

func newGeneratedAwareAutoDeployRunner(cfg config.Config, repository *autodeploy.Repository, sources *sourceconnections.Service, jobService *jobs.Service, preflight autodeploy.DispatchPreflight, logger *slog.Logger) (autoDeployRunner, error) {
	if (!cfg.ComposeRuntime && !cfg.GeneratedRuntime) || !cfg.GitHubConnectionsEnabled() || repository == nil || sources == nil || !sources.ProviderEnabled() || jobService == nil || logger == nil || (cfg.GeneratedRuntime && preflight == nil) {
		return nil, errors.New("github auto-deploy is unavailable")
	}
	coordinatorConfig := autodeploy.DefaultCoordinatorConfig()
	coordinatorConfig.Preflight = preflight
	coordinatorConfig.Observer = func(_ context.Context, event autodeploy.CoordinatorEvent) {
		observeAutoDeployLifecycle(logger, event)
	}
	return autodeploy.NewCoordinator(repository, sourceHeadResolver{service: sources}, jobService, coordinatorConfig)
}

type autoDeployPlanReader interface {
	Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error)
}

type autoDeployReleaseMaterializer interface {
	Materialize(context.Context, string, string) (releasesnapshot.Release, error)
	ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error)
}

type githubSourceInspector func(context.Context, sourceinspection.GitHubReader, string, sourceinspection.GitHubSource) (sourceinspection.Result, error)

type generatedAutoDeployPreflight struct {
	plans    autoDeployPlanReader
	sources  sourceinspection.GitHubReader
	releases autoDeployReleaseMaterializer
	inspect  githubSourceInspector
}

func newGeneratedAutoDeployPreflight(plans autoDeployPlanReader, sources sourceinspection.GitHubReader, releases autoDeployReleaseMaterializer) (autodeploy.DispatchPreflight, error) {
	if plans == nil || sources == nil || releases == nil {
		return nil, errors.New("generated auto-deploy preflight is unavailable")
	}
	return &generatedAutoDeployPreflight{plans: plans, sources: sources, releases: releases, inspect: sourceinspection.InspectGitHub}, nil
}

func (preflight *generatedAutoDeployPreflight) Prepare(ctx context.Context, request autodeploy.DispatchPreflightRequest) (autodeploy.DispatchPreflightResult, error) {
	if preflight == nil || preflight.plans == nil || preflight.sources == nil || preflight.releases == nil || preflight.inspect == nil ||
		request.ApplicationID == "" || request.OwnerUserID == "" || request.ResolvedSHA == "" || request.Source.OwnerUserID != request.OwnerUserID ||
		request.Source.ConnectionID == "" || request.Source.InstallationID <= 0 || request.Source.RepositoryID <= 0 || request.Source.Branch == "" || request.Source.Ref != "refs/heads/"+request.Source.Branch {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
	revision, err := preflight.plans.Get(ctx, request.ApplicationID)
	if err != nil {
		return autodeploy.DispatchPreflightResult{}, safePlanPreflightError(err)
	}
	if revision.RevisionNumber == 0 {
		if request.Source.ComposePath == "" {
			return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightPlanReview}
		}
		return autodeploy.DispatchPreflightResult{}, nil
	}
	if revision.Plan.Strategy == deploymentplans.StrategyCompose {
		if request.Source.ComposePath == "" {
			return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightPlanReview}
		}
		return autodeploy.DispatchPreflightResult{}, nil
	}
	if revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode || request.Source.ComposePath != "" || revision.ID == "" || revision.AppID != request.ApplicationID || revision.State != deploymentplans.RevisionAccepted || revision.Plan.Source.Provider != "github" || revision.Plan.Source.RepositoryID != request.Source.RepositoryID {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightPlanReview}
	}
	inspection, err := preflight.inspect(ctx, preflight.sources, request.OwnerUserID, sourceinspection.GitHubSource{
		ConnectionID: request.Source.ConnectionID, InstallationID: request.Source.InstallationID,
		RepositoryID: request.Source.RepositoryID, Branch: request.Source.Branch,
	})
	if err != nil {
		return autodeploy.DispatchPreflightResult{}, safeInspectionPreflightError(err)
	}
	if inspection.ResolvedSHA != request.ResolvedSHA {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightHeadChanged}
	}
	if inspection.Source.Type != "github" || inspection.Source.ConnectionID != request.Source.ConnectionID || inspection.Source.InstallationID != request.Source.InstallationID || inspection.Source.RepositoryID != request.Source.RepositoryID || inspection.Source.TrackedBranch != request.Source.Branch || inspection.Source.TrackedRef != request.Source.Ref {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
	differences, compareErr := deploymentplans.CompareAnalysis(revision.Plan, inspection.Analysis)
	if compareErr != nil || len(differences) != 0 {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightPlanReview}
	}
	release, err := preflight.releases.Materialize(ctx, request.OwnerUserID, request.ApplicationID)
	if err != nil {
		return autodeploy.DispatchPreflightResult{}, safeReleasePreflightError(err)
	}
	if !releaseMatchesPreflight(release, request, revision) {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightHeadChanged}
	}
	ready, err := preflight.releases.ReadyWorkspace(ctx, request.ApplicationID, release.ID)
	if err != nil {
		return autodeploy.DispatchPreflightResult{}, safeReleasePreflightError(err)
	}
	if !releaseMatchesPreflight(ready, request, revision) || ready.ID != release.ID || ready.WorkspaceState != releasesnapshot.WorkspaceStateReady {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
	current, err := preflight.plans.Get(ctx, request.ApplicationID)
	if err != nil {
		return autodeploy.DispatchPreflightResult{}, safePlanPreflightError(err)
	}
	if current.ID != revision.ID || current.RevisionNumber != revision.RevisionNumber || current.CanonicalDigest != revision.CanonicalDigest {
		return autodeploy.DispatchPreflightResult{}, &autodeploy.PreflightError{Code: autodeploy.PreflightHeadChanged}
	}
	return autodeploy.DispatchPreflightResult{ReleaseID: ready.ID}, nil
}

func releaseMatchesPreflight(release releasesnapshot.Release, request autodeploy.DispatchPreflightRequest, revision deploymentplans.DeploymentPlanRevision) bool {
	return release.ID != "" && release.AppID == request.ApplicationID && release.SourceProvider == "github" && release.RepositoryID == request.Source.RepositoryID && release.ResolvedSHA == request.ResolvedSHA && release.ComposePath == "" && release.DeploymentPlanRevisionID == revision.ID && release.DeploymentPlanRevisionNumber == revision.RevisionNumber
}

func safePlanPreflightError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if deploymentplans.IsCode(err, "app_not_found") || deploymentplans.IsCode(err, "deployment_plan_unavailable") {
		return &autodeploy.PreflightError{Code: autodeploy.PreflightPlanReview}
	}
	return &autodeploy.SourceError{Code: autodeploy.OutcomePersistenceUnavailable}
}

func safeInspectionPreflightError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if sourceinspection.IsCode(err, "source_too_large") || sourceinspection.IsCode(err, "invalid_source") {
		return &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
	return safeSourceError(err)
}

func safeReleasePreflightError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if releasesnapshot.IsCode(err, "canceled") {
		return err
	}
	if releasesnapshot.IsCode(err, "deployment_plan_review_required") {
		return &autodeploy.PreflightError{Code: autodeploy.PreflightPlanReview}
	}
	if releasesnapshot.IsCode(err, "source_access_lost") {
		return &autodeploy.SourceError{Code: autodeploy.OutcomeSourceAccessLost}
	}
	if releasesnapshot.IsCode(err, "provider_unavailable") {
		return &autodeploy.SourceError{Code: autodeploy.OutcomeProviderUnavailable}
	}
	if releasesnapshot.IsCode(err, "internal_error") || releasesnapshot.IsCode(err, releasesnapshot.ErrorCodeSourceStorageFull) {
		return &autodeploy.SourceError{Code: autodeploy.OutcomePersistenceUnavailable}
	}
	return &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
}

func observeAutoDeployLifecycle(logger *slog.Logger, event autodeploy.CoordinatorEvent) {
	logger.Info("github auto-deploy lifecycle",
		"outcome", safeAutoDeployOutcome(event.Outcome),
		"state", safeAutoDeployState(event.State),
		"pause_code", safeAutoDeployPauseCode(event.PauseCode),
		"retry_attempt", safeAutoDeployRetryAttempt(event.RetryAttempt),
		"next_action", safeAutoDeployNextAction(event.NextAction),
		"claimed", event.Claimed, "resolved", event.Resolved, "dispatched", event.Dispatched,
		"paused", event.Paused, "retried", event.Retried)
}

type sourceHeadResolver struct{ service *sourceconnections.Service }

func (resolver sourceHeadResolver) ResolveHead(ctx context.Context, scope autodeploy.SourceScope) (string, error) {
	if resolver.service == nil || scope.OwnerUserID == "" || scope.ConnectionID == "" || scope.InstallationID <= 0 || scope.RepositoryID <= 0 || scope.Branch == "" || scope.Ref != "refs/heads/"+scope.Branch {
		return "", &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
	repository, branch, err := resolver.service.Resolve(ctx, scope.OwnerUserID, scope.ConnectionID, scope.InstallationID, scope.RepositoryID, scope.Branch)
	if err != nil {
		return "", safeSourceError(err)
	}
	if repository.ID != scope.RepositoryID || branch.Name != scope.Branch {
		return "", &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
	return branch.SHA, nil
}

func safeSourceError(err error) error {
	switch {
	case sourceconnections.IsCode(err, "source_access_lost"), sourceconnections.IsCode(err, "connection_not_found"), sourceconnections.IsCode(err, "authorization_expired"), sourceconnections.IsCode(err, "authorization_denied"), sourceconnections.IsCode(err, "authentication_required"):
		return &autodeploy.SourceError{Code: autodeploy.OutcomeSourceAccessLost}
	case sourceconnections.IsCode(err, "provider_unavailable"), sourceconnections.IsCode(err, "rate_limited"):
		return &autodeploy.SourceError{Code: autodeploy.OutcomeProviderUnavailable}
	case sourceconnections.IsCode(err, "internal_error"):
		return &autodeploy.SourceError{Code: autodeploy.OutcomePersistenceUnavailable}
	default:
		return &autodeploy.SourceError{Code: autodeploy.OutcomeInvalidSource}
	}
}

func safeAutoDeployOutcome(value string) string {
	switch value {
	case autodeploy.OutcomeIdle, autodeploy.OutcomePersistenceUnavailable, autodeploy.OutcomeProviderUnavailable,
		autodeploy.OutcomeSourceAccessLost, autodeploy.OutcomeInvalidSource, autodeploy.OutcomeApplicationBusy,
		autodeploy.OutcomeDeploymentFailed:
		return value
	default:
		return autodeploy.OutcomePersistenceUnavailable
	}
}

func safeAutoDeployState(value string) string {
	switch value {
	case autodeploy.StateDisabled, autodeploy.StateIdle, autodeploy.StateDispatching,
		autodeploy.StateDeploying, autodeploy.StatePaused, autodeploy.StateRetryWait:
		return value
	default:
		return autodeploy.ObservedStateNone
	}
}

func safeAutoDeployPauseCode(value string) string {
	switch value {
	case autodeploy.PauseApprovalRequired, autodeploy.PauseMigrationApprovalRequired, autodeploy.PauseInsufficientReplacementCapacity,
		autodeploy.PauseDeploymentPlanReviewRequired, autodeploy.PauseDeploymentFailed, autodeploy.PauseMissingConfig,
		autodeploy.PauseSourceAccessLost, autodeploy.PauseInvalidSource, autodeploy.PauseProviderUnavailable,
		autodeploy.PauseRelayUnavailable:
		return value
	default:
		return autodeploy.ObservedPauseNone
	}
}

func safeAutoDeployRetryAttempt(value uint32) uint32 {
	if value > 1000 {
		return 1000
	}
	return value
}

func safeAutoDeployNextAction(value string) string {
	switch value {
	case autodeploy.NextActionNone, autodeploy.NextActionResolve, autodeploy.NextActionResolveCooldown,
		autodeploy.NextActionDispatch, autodeploy.NextActionPollJob, autodeploy.NextActionRetry,
		autodeploy.NextActionApprovalRequired, autodeploy.NextActionResumeRequired:
		return value
	default:
		return autodeploy.NextActionNone
	}
}
