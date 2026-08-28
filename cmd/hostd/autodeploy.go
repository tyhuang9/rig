package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/sourceconnections"
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
	if !cfg.ComposeRuntime || !cfg.GitHubConnectionsEnabled() {
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

func newAutoDeployRunner(cfg config.Config, db *sql.DB, sources *sourceconnections.Service, jobService *jobs.Service, logger *slog.Logger) (autoDeployRunner, error) {
	if !cfg.ComposeRuntime || !cfg.GitHubConnectionsEnabled() || db == nil || sources == nil || !sources.ProviderEnabled() || jobService == nil || logger == nil {
		return nil, errors.New("github auto-deploy is unavailable")
	}
	coordinatorConfig := autodeploy.DefaultCoordinatorConfig()
	coordinatorConfig.Observer = func(_ context.Context, event autodeploy.CoordinatorEvent) {
		observeAutoDeployLifecycle(logger, event)
	}
	return autodeploy.NewCoordinator(autodeploy.NewRepository(db), sourceHeadResolver{service: sources}, jobService, coordinatorConfig)
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
	case autodeploy.PauseApprovalRequired, autodeploy.PauseDeploymentFailed, autodeploy.PauseMissingConfig,
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
