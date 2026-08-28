package autodeploy

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	OutcomeIdle                   = "idle"
	OutcomePersistenceUnavailable = "persistence_unavailable"
	OutcomeProviderUnavailable    = "provider_unavailable"
	OutcomeSourceAccessLost       = "source_access_lost"
	OutcomeInvalidSource          = "invalid_source"
	OutcomeApplicationBusy        = "application_busy"
	OutcomeDeploymentFailed       = "deployment_failed"

	ObservedStateNone = "none"
	ObservedPauseNone = "none"

	NextActionNone             = "none"
	NextActionResolve          = "resolve"
	NextActionResolveCooldown  = "resolve_cooldown"
	NextActionDispatch         = "dispatch"
	NextActionPollJob          = "poll_job"
	NextActionRetry            = "retry"
	NextActionApprovalRequired = "approval_required"
	NextActionResumeRequired   = "resume_required"
)

type SourceScope struct {
	OwnerUserID    string
	ConnectionID   string
	InstallationID int64
	RepositoryID   int64
	Branch         string
	Ref            string
}

type SourceResolver interface {
	ResolveHead(context.Context, SourceScope) (string, error)
}

// SourceError contains only an allowlisted coordinator outcome. Provider
// response bodies, credentials, repository names, and raw errors never cross
// this boundary.
type SourceError struct{ Code string }

func (err *SourceError) Error() string {
	if err == nil {
		return ""
	}
	return err.Code
}

type CoordinatorRepository interface {
	RecoverExpiredLeases(context.Context, time.Time, int) (int, error)
	ForceReconcileEligible(context.Context, time.Time, time.Time, int, bool) (int, error)
	DeferReconcile(context.Context, WorkLease, time.Time, time.Time) error
	CountRecentProviderFailures(context.Context, WorkLease, time.Time, time.Time) (uint32, error)
	ClaimDueWithResolveCutoff(context.Context, string, time.Time, time.Duration, time.Time) (Status, WorkLease, error)
	ReleaseLease(context.Context, WorkLease, time.Time) error
	PeekNewestACK(context.Context, WorkLease, time.Time) (SourceACKHead, error)
	ReserveResolve(context.Context, WorkLease, uint64, time.Time) error
	FinalizeResolvedHead(context.Context, WorkLease, uint64, string, time.Time, time.Time) error
	PrepareDispatch(context.Context, WorkLease, time.Time) (PreparedDispatch, error)
	LinkDispatchJob(context.Context, WorkLease, uint64, uint64, string, time.Time) error
	LinkDispatchJobTx(context.Context, *sql.Tx, WorkLease, uint64, uint64, string, time.Time) error
	RefreshActiveJob(context.Context, WorkLease, time.Time) (Status, error)
	Pause(context.Context, WorkLease, string, time.Time) error
	ScheduleRetry(context.Context, WorkLease, time.Time, time.Time) error
}

type JobCreator interface {
	CreateWithInputFinalized(jobs.CreateRequest, jobs.CreateFinalizer) (jobs.Job, bool, error)
}

type CoordinatorEvent struct {
	Outcome      string
	State        string
	PauseCode    string
	NextAction   string
	RetryAttempt uint32
	Claimed      uint64
	Resolved     uint64
	Dispatched   uint64
	Paused       uint64
	Retried      uint64
}

type CoordinatorObserver func(context.Context, CoordinatorEvent)

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type CoordinatorConfig struct {
	PollInterval       time.Duration
	ReconcileInterval  time.Duration
	MinResolveInterval time.Duration
	LeaseTTL           time.Duration
	ReleaseTimeout     time.Duration
	RetryBase          time.Duration
	RetryMaximum       time.Duration
	RetryWindow        time.Duration
	MaxRetryAttempts   uint32
	BatchSize          int
	RecoveryLimit      int
	Observer           CoordinatorObserver
	Clock              Clock
}

func DefaultCoordinatorConfig() CoordinatorConfig {
	return CoordinatorConfig{
		PollInterval:       time.Second,
		ReconcileInterval:  6 * time.Hour,
		MinResolveInterval: time.Minute,
		LeaseTTL:           time.Minute,
		ReleaseTimeout:     time.Second,
		RetryBase:          15 * time.Second,
		RetryMaximum:       5 * time.Minute,
		RetryWindow:        30 * time.Minute,
		MaxRetryAttempts:   2,
		BatchSize:          32,
		RecoveryLimit:      500,
		Clock:              realClock{},
	}
}

type Coordinator struct {
	repository CoordinatorRepository
	sources    SourceResolver
	jobs       JobCreator
	config     CoordinatorConfig
	wake       chan struct{}
	wakeMu     sync.Mutex
	reconcile  bool
	runMu      sync.Mutex
	running    bool
}

func NewCoordinator(repository CoordinatorRepository, sources SourceResolver, jobService JobCreator, config CoordinatorConfig) (*Coordinator, error) {
	if repository == nil || sources == nil || jobService == nil || config.Clock == nil ||
		config.PollInterval <= 0 || config.ReconcileInterval <= 0 || config.ReconcileInterval > 24*time.Hour || config.MinResolveInterval <= 0 || config.MinResolveInterval > 24*time.Hour || config.LeaseTTL < time.Second || config.LeaseTTL > 15*time.Minute ||
		config.ReleaseTimeout <= 0 || config.RetryBase <= 0 || config.RetryMaximum < config.RetryBase || config.RetryWindow < config.RetryBase ||
		config.RetryMaximum > 24*time.Hour || config.RetryWindow > 24*time.Hour ||
		config.MaxRetryAttempts == 0 || config.MaxRetryAttempts > 500 || config.BatchSize < 1 || config.BatchSize > 500 || config.RecoveryLimit < 1 || config.RecoveryLimit > 500 {
		return nil, ErrInvalid
	}
	return &Coordinator{repository: repository, sources: sources, jobs: jobService, config: config, wake: make(chan struct{}, 1)}, nil
}

// Wake is a bounded, coalescing hint. Durable repository state remains the
// source of truth, so dropping duplicate wakeups cannot lose work.
func (coordinator *Coordinator) Wake() {
	if coordinator == nil {
		return
	}
	select {
	case coordinator.wake <- struct{}{}:
	default:
	}
}

// Reconcile requests the stronger reconnect/startup behavior while sharing
// the same bounded wake channel. It cannot be lost behind an ordinary ACK
// wake because the intent is retained until the run loop consumes it.
func (coordinator *Coordinator) Reconcile() {
	if coordinator == nil {
		return
	}
	coordinator.wakeMu.Lock()
	coordinator.reconcile = true
	coordinator.wakeMu.Unlock()
	coordinator.Wake()
}

func (coordinator *Coordinator) Run(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	coordinator.runMu.Lock()
	if coordinator.running {
		coordinator.runMu.Unlock()
		return ErrState
	}
	coordinator.running = true
	coordinator.runMu.Unlock()
	defer func() {
		coordinator.runMu.Lock()
		coordinator.running = false
		coordinator.runMu.Unlock()
	}()

	coordinator.recover(ctx)
	coordinator.takeReconcile()
	coordinator.forceReconcile(ctx, true)
	coordinator.drain(ctx)

	for ctx.Err() == nil {
		timer := coordinator.config.Clock.NewTimer(coordinator.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-coordinator.wake:
			timer.Stop()
			if coordinator.takeReconcile() {
				coordinator.forceReconcile(ctx, false)
			}
		case <-timer.C():
		}
		coordinator.drain(ctx)
	}
	return nil
}

func (coordinator *Coordinator) takeReconcile() bool {
	coordinator.wakeMu.Lock()
	requested := coordinator.reconcile
	coordinator.reconcile = false
	coordinator.wakeMu.Unlock()
	return requested
}

func (coordinator *Coordinator) recover(ctx context.Context) {
	for ctx.Err() == nil {
		count, err := coordinator.repository.RecoverExpiredLeases(ctx, coordinator.config.Clock.Now().UTC(), coordinator.config.RecoveryLimit)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				coordinator.observe(ctx, CoordinatorEvent{Outcome: OutcomePersistenceUnavailable})
			}
			return
		}
		if count < coordinator.config.RecoveryLimit {
			return
		}
	}
}

func (coordinator *Coordinator) forceReconcile(ctx context.Context, startup bool) {
	for ctx.Err() == nil {
		now := coordinator.config.Clock.Now().UTC()
		count, err := coordinator.repository.ForceReconcileEligible(ctx, now, now.Add(-coordinator.config.MinResolveInterval), coordinator.config.RecoveryLimit, startup)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				coordinator.observe(ctx, CoordinatorEvent{Outcome: OutcomePersistenceUnavailable})
			}
			return
		}
		if count < coordinator.config.RecoveryLimit {
			return
		}
	}
}

func (coordinator *Coordinator) drain(ctx context.Context) {
	event := CoordinatorEvent{Outcome: OutcomeIdle}
	for processed := 0; processed < coordinator.config.BatchSize && ctx.Err() == nil; processed++ {
		claimed, result := coordinator.processOne(ctx)
		if !claimed {
			if result.Outcome != "" {
				event.Outcome = result.Outcome
			}
			break
		}
		event.Claimed += result.Claimed
		event.Resolved += result.Resolved
		event.Dispatched += result.Dispatched
		event.Paused += result.Paused
		event.Retried += result.Retried
		if result.NextAction != "" && result.NextAction != NextActionNone {
			event.State = result.State
			event.PauseCode = result.PauseCode
			event.RetryAttempt = result.RetryAttempt
			event.NextAction = result.NextAction
		}
		if result.Outcome != "" && result.Outcome != OutcomeIdle {
			event.Outcome = result.Outcome
		}
	}
	if event.Claimed > 0 || event.Outcome != OutcomeIdle {
		coordinator.observe(ctx, event)
	}
}

func (coordinator *Coordinator) processOne(ctx context.Context) (bool, CoordinatorEvent) {
	now := coordinator.config.Clock.Now().UTC()
	status, lease, err := coordinator.repository.ClaimDueWithResolveCutoff(ctx, uuid.NewString(), now, coordinator.config.LeaseTTL, now.Add(-coordinator.config.MinResolveInterval))
	if errors.Is(err, ErrNotFound) {
		return false, CoordinatorEvent{Outcome: OutcomeIdle}
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return false, CoordinatorEvent{}
		}
		return false, CoordinatorEvent{Outcome: OutcomePersistenceUnavailable}
	}
	event := CoordinatorEvent{Outcome: OutcomeIdle, Claimed: 1}
	setEventLifecycle(&event, status, nextActionForStatus(status, now, coordinator.config.MinResolveInterval))
	defer coordinator.release(lease)

	if status.State == StateDispatching {
		coordinator.dispatch(ctx, status, lease, &event)
		return true, event
	}

	reconcile := status.NextReconcileAt != nil && !status.NextReconcileAt.After(now)
	if status.LatestResolvedGeneration < status.LastConsumedGeneration || status.State == StateRetryWait {
		reconcile = true
	}
	if status.ActiveJobID != "" {
		var valid bool
		now, valid = coordinator.mutationTime(lease, &event)
		if !valid {
			return true, event
		}
		status, err = coordinator.repository.RefreshActiveJob(ctx, lease, now)
		if err != nil {
			event.Outcome = OutcomePersistenceUnavailable
			return true, event
		}
		setEventLifecycle(&event, status, nextActionForStatus(status, now, coordinator.config.MinResolveInterval))
		if status.ActiveJobID == "" && status.State == StatePaused && status.PauseCode == PauseProviderUnavailable {
			coordinator.handleActiveProviderFailure(ctx, status, lease, now, &event)
			return true, event
		}
		if status.State == StatePaused && status.PauseCode == PauseSourceAccessLost {
			event.Outcome = OutcomeSourceAccessLost
			return true, event
		}
	}
	if status.LastReconciledAt != nil && status.LastReconciledAt.After(now.Add(-coordinator.config.MinResolveInterval)) {
		return true, event
	}

	now, valid := coordinator.mutationTime(lease, &event)
	if !valid {
		return true, event
	}
	head, observeErr := coordinator.repository.PeekNewestACK(ctx, lease, now)
	resolvingGeneration := status.LastConsumedGeneration
	if observeErr == nil && head.Generation > status.LastConsumedGeneration {
		resolvingGeneration = head.Generation
		reconcile = true
	} else if observeErr == nil {
		resolvingGeneration = head.Generation
	} else if observeErr != nil && !errors.Is(observeErr, ErrNotFound) {
		event.Outcome = OutcomePersistenceUnavailable
		return true, event
	}
	if !reconcile {
		if event.NextAction == "" {
			event.NextAction = NextActionNone
		}
		return true, event
	}
	if !status.SourceScopeActive {
		now, valid := coordinator.mutationTime(lease, &event)
		if !valid {
			return true, event
		}
		if status.State != StatePaused && status.ActiveJobID == "" {
			if err = coordinator.repository.Pause(ctx, lease, PauseSourceAccessLost, now); err == nil {
				event.Paused++
				setPausedLifecycle(&event, PauseSourceAccessLost)
			}
		}
		coordinator.deferReconcile(ctx, lease, &event)
		event.Outcome = OutcomeSourceAccessLost
		return true, event
	}

	scope := SourceScope{
		OwnerUserID: status.SourceOwnerUserID, ConnectionID: status.SourceConnectionID,
		InstallationID: status.InstallationID, RepositoryID: status.RepositoryID,
		Branch: status.TrackedBranch, Ref: status.TrackedRef,
	}
	now, valid = coordinator.mutationTime(lease, &event)
	if !valid {
		return true, event
	}
	if err = coordinator.repository.ReserveResolve(ctx, lease, resolvingGeneration, now); err != nil {
		if errors.Is(err, ErrSourceAccessLost) {
			event.Paused++
			setPausedLifecycle(&event, PauseSourceAccessLost)
			event.Outcome = OutcomeSourceAccessLost
			return true, event
		}
		event.Outcome = OutcomePersistenceUnavailable
		return true, event
	}
	event.NextAction = NextActionResolve
	sha, resolveErr := coordinator.sources.ResolveHead(ctx, scope)
	now, valid = coordinator.mutationTime(lease, &event)
	if !valid {
		return true, event
	}
	if resolveErr != nil {
		if ctx.Err() != nil {
			return true, event
		}
		coordinator.handleSourceError(ctx, status, lease, resolveErr, &event)
		return true, event
	}
	if !validSHA(sha) {
		coordinator.handleSourceError(ctx, status, lease, &SourceError{Code: OutcomeInvalidSource}, &event)
		return true, event
	}
	if err = coordinator.repository.FinalizeResolvedHead(ctx, lease, resolvingGeneration, sha, now.Add(coordinator.config.ReconcileInterval), now); err != nil {
		if errors.Is(err, ErrSourceAccessLost) {
			event.Paused++
			setPausedLifecycle(&event, PauseSourceAccessLost)
			event.Outcome = OutcomeSourceAccessLost
			return true, event
		}
		event.Outcome = OutcomePersistenceUnavailable
		return true, event
	}
	event.Resolved++

	if status.ActiveJobID != "" {
		setEventLifecycle(&event, status, NextActionPollJob)
		return true, event
	}
	if sha == status.LastSuccessfulDeployedSHA {
		event.State = StateIdle
		event.PauseCode = ObservedPauseNone
		event.RetryAttempt = 0
		event.NextAction = NextActionResolveCooldown
		return true, event
	}
	if status.State == StatePaused && sha == status.PausedSHA {
		setEventLifecycle(&event, status, nextActionForPause(status.PauseCode, false))
		return true, event
	}
	status.LatestResolvedSHA = sha
	if status.State == StatePaused {
		status.State = StateIdle
	}
	coordinator.dispatch(ctx, status, lease, &event)
	return true, event
}

func (coordinator *Coordinator) handleActiveProviderFailure(ctx context.Context, status Status, lease WorkLease, _ time.Time, event *CoordinatorEvent) {
	now, valid := coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	failures, err := coordinator.repository.CountRecentProviderFailures(ctx, lease, now.Add(-coordinator.config.RetryWindow), now)
	if err != nil {
		event.Outcome = OutcomePersistenceUnavailable
		return
	}
	if failures >= coordinator.config.MaxRetryAttempts {
		event.Paused++
		setPausedLifecycle(event, PauseProviderUnavailable)
		coordinator.deferReconcile(ctx, lease, event)
		event.Outcome = OutcomeProviderUnavailable
		return
	}
	now, valid = coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	delay := coordinator.retryDelay(failures)
	if err = coordinator.repository.ScheduleRetry(ctx, lease, now.Add(delay), now); err != nil {
		event.Outcome = OutcomePersistenceUnavailable
		return
	}
	event.Retried++
	event.State = StateRetryWait
	event.PauseCode = ObservedPauseNone
	event.RetryAttempt = status.RetryAttempt + 1
	event.NextAction = NextActionRetry
	event.Outcome = OutcomeProviderUnavailable
}

func (coordinator *Coordinator) dispatch(ctx context.Context, status Status, lease WorkLease, event *CoordinatorEvent) {
	if ctx.Err() != nil {
		return
	}
	priorAttempt := status.RetryAttempt
	event.NextAction = NextActionDispatch
	prepareAt, valid := coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	dispatch, err := coordinator.repository.PrepareDispatch(ctx, lease, prepareAt)
	if err != nil {
		event.Outcome = OutcomePersistenceUnavailable
		return
	}
	event.State = StateDispatching
	event.PauseCode = ObservedPauseNone
	event.RetryAttempt = 0
	event.NextAction = NextActionDispatch
	_, _, err = coordinator.jobs.CreateWithInputFinalized(jobs.CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: dispatch.ApplicationID,
		IdempotencyKey: DispatchIdempotencyKey(lease.ConfigRevision, dispatch.Sequence),
		RequestedBy:    status.SourceOwnerUserID,
		Input:          jobs.DeploymentInput{ReleaseID: "", ConfigurationMode: jobs.ConfigurationCurrent},
	}, func(tx *sql.Tx, job jobs.Job) error {
		linkAt := coordinator.config.Clock.Now().UTC()
		if !linkAt.Before(lease.ExpiresAt) {
			return ErrState
		}
		return coordinator.repository.LinkDispatchJobTx(ctx, tx, lease, dispatch.Sequence, dispatch.Generation, job.ID, linkAt)
	})
	if err != nil {
		if errors.Is(err, ErrSourceAccessLost) {
			event.Paused++
			setPausedLifecycle(event, PauseSourceAccessLost)
			event.Outcome = OutcomeSourceAccessLost
			return
		}
		if errors.Is(err, jobs.ErrApplicationBusy) {
			coordinator.retryOrPause(ctx, lease, priorAttempt, OutcomeApplicationBusy, PauseDeploymentFailed, coordinator.config.Clock.Now().UTC(), event)
			return
		}
		// The prepared dispatch is durable and the job/link transaction rolled
		// back together. Leave it prepared so the exact dispatch can be replayed.
		event.Outcome = OutcomePersistenceUnavailable
		return
	}
	event.Dispatched++
	event.State = StateDeploying
	event.PauseCode = ObservedPauseNone
	event.RetryAttempt = 0
	event.NextAction = NextActionPollJob
}

func (coordinator *Coordinator) handleSourceError(ctx context.Context, status Status, lease WorkLease, resolveErr error, event *CoordinatorEvent) {
	now, valid := coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	var sourceErr *SourceError
	if !errors.As(resolveErr, &sourceErr) {
		sourceErr = &SourceError{Code: OutcomeInvalidSource}
	}
	if status.State == StatePaused {
		setEventLifecycle(event, status, nextActionForStatus(status, now, coordinator.config.MinResolveInterval))
		coordinator.deferReconcile(ctx, lease, event)
		switch sourceErr.Code {
		case OutcomeSourceAccessLost:
			event.Outcome = OutcomeSourceAccessLost
		case OutcomeProviderUnavailable, OutcomePersistenceUnavailable, "rate_limited":
			event.Outcome = OutcomeProviderUnavailable
		default:
			event.Outcome = OutcomeInvalidSource
		}
		return
	}
	switch sourceErr.Code {
	case OutcomeProviderUnavailable, "rate_limited":
		coordinator.retryOrPause(ctx, lease, status.RetryAttempt, OutcomeProviderUnavailable, PauseProviderUnavailable, now, event)
	case OutcomePersistenceUnavailable:
		coordinator.retryOrPause(ctx, lease, status.RetryAttempt, OutcomePersistenceUnavailable, PauseProviderUnavailable, now, event)
	case OutcomeSourceAccessLost:
		if err := coordinator.repository.Pause(ctx, lease, PauseSourceAccessLost, now); err == nil {
			event.Paused++
			setPausedLifecycle(event, PauseSourceAccessLost)
		}
		coordinator.deferReconcile(ctx, lease, event)
		event.Outcome = OutcomeSourceAccessLost
	default:
		if err := coordinator.repository.Pause(ctx, lease, PauseInvalidSource, now); err == nil {
			event.Paused++
			setPausedLifecycle(event, PauseInvalidSource)
		}
		coordinator.deferReconcile(ctx, lease, event)
		event.Outcome = OutcomeInvalidSource
	}
}

func (coordinator *Coordinator) retryOrPause(ctx context.Context, lease WorkLease, priorAttempt uint32, outcome, exhaustedPauseCode string, now time.Time, event *CoordinatorEvent) {
	var valid bool
	now, valid = coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	nextAttempt := priorAttempt + 1
	delay := coordinator.retryDelay(nextAttempt)
	if nextAttempt >= coordinator.config.MaxRetryAttempts || delay > coordinator.config.RetryWindow {
		if err := coordinator.repository.Pause(ctx, lease, exhaustedPauseCode, now); err == nil {
			event.Paused++
			setPausedLifecycle(event, exhaustedPauseCode)
		}
		coordinator.deferReconcile(ctx, lease, event)
		event.Outcome = outcome
		return
	}
	now, valid = coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	if err := coordinator.repository.ScheduleRetry(ctx, lease, now.Add(delay), now); err == nil {
		event.Retried++
		event.State = StateRetryWait
		event.PauseCode = ObservedPauseNone
		event.RetryAttempt = nextAttempt
		event.NextAction = NextActionRetry
	} else {
		event.Outcome = OutcomePersistenceUnavailable
		return
	}
	event.Outcome = outcome
}

func (coordinator *Coordinator) mutationTime(lease WorkLease, event *CoordinatorEvent) (time.Time, bool) {
	now := coordinator.config.Clock.Now().UTC()
	if now.Before(lease.ExpiresAt) {
		return now, true
	}
	coordinator.Wake()
	event.Outcome = OutcomePersistenceUnavailable
	return now, false
}

func (coordinator *Coordinator) deferReconcile(ctx context.Context, lease WorkLease, event *CoordinatorEvent) {
	now, valid := coordinator.mutationTime(lease, event)
	if !valid {
		return
	}
	if err := coordinator.repository.DeferReconcile(ctx, lease, now.Add(coordinator.config.ReconcileInterval), now); err != nil {
		event.Outcome = OutcomePersistenceUnavailable
	}
}

func (coordinator *Coordinator) retryDelay(attempt uint32) time.Duration {
	if attempt == 0 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 30 {
		shift = 30
	}
	multiplier := time.Duration(uint64(1) << shift)
	if multiplier > time.Duration(math.MaxInt64)/coordinator.config.RetryBase {
		return coordinator.config.RetryMaximum
	}
	delay := coordinator.config.RetryBase * multiplier
	if delay > coordinator.config.RetryMaximum {
		return coordinator.config.RetryMaximum
	}
	return delay
}

func (coordinator *Coordinator) release(lease WorkLease) {
	ctx, cancel := context.WithTimeout(context.Background(), coordinator.config.ReleaseTimeout)
	defer cancel()
	if err := coordinator.repository.ReleaseLease(ctx, lease, coordinator.config.Clock.Now().UTC()); err != nil && !errors.Is(err, ErrState) {
		coordinator.observe(ctx, CoordinatorEvent{Outcome: OutcomePersistenceUnavailable})
	}
}

func (coordinator *Coordinator) observe(ctx context.Context, event CoordinatorEvent) {
	if coordinator.config.Observer != nil {
		coordinator.config.Observer(ctx, normalizeCoordinatorEvent(event))
	}
}

func setEventLifecycle(event *CoordinatorEvent, status Status, nextAction string) {
	event.State = normalizeCoordinatorState(status.State)
	event.PauseCode = normalizeCoordinatorPauseCode(status.PauseCode)
	event.RetryAttempt = status.RetryAttempt
	event.NextAction = normalizeCoordinatorNextAction(nextAction)
}

func setPausedLifecycle(event *CoordinatorEvent, pauseCode string) {
	event.State = StatePaused
	event.PauseCode = normalizeCoordinatorPauseCode(pauseCode)
	event.RetryAttempt = 0
	event.NextAction = nextActionForPause(pauseCode, false)
}

func nextActionForStatus(status Status, now time.Time, minResolveInterval time.Duration) string {
	switch status.State {
	case StateDispatching:
		return NextActionDispatch
	case StateDeploying:
		return NextActionPollJob
	case StateRetryWait:
		return NextActionRetry
	case StatePaused:
		return nextActionForPause(status.PauseCode, status.ActiveJobID != "")
	}
	if status.LastReconciledAt != nil && status.LastReconciledAt.After(now.Add(-minResolveInterval)) {
		return NextActionResolveCooldown
	}
	if status.LatestResolvedGeneration < status.LastConsumedGeneration || status.NextReconcileAt != nil && !status.NextReconcileAt.After(now) {
		return NextActionResolve
	}
	return NextActionNone
}

func nextActionForPause(pauseCode string, activeJob bool) string {
	if pauseCode == PauseApprovalRequired {
		return NextActionApprovalRequired
	}
	if activeJob {
		return NextActionPollJob
	}
	return NextActionResumeRequired
}

func normalizeCoordinatorEvent(event CoordinatorEvent) CoordinatorEvent {
	event.Outcome = normalizeCoordinatorOutcome(event.Outcome)
	event.State = normalizeCoordinatorState(event.State)
	event.PauseCode = normalizeCoordinatorPauseCode(event.PauseCode)
	event.NextAction = normalizeCoordinatorNextAction(event.NextAction)
	if event.State != StatePaused {
		event.PauseCode = ObservedPauseNone
	}
	if event.State != StateRetryWait {
		event.RetryAttempt = 0
	} else if event.RetryAttempt > 1000 {
		event.RetryAttempt = 1000
	}
	return event
}

func normalizeCoordinatorOutcome(value string) string {
	switch value {
	case OutcomeIdle, OutcomePersistenceUnavailable, OutcomeProviderUnavailable, OutcomeSourceAccessLost,
		OutcomeInvalidSource, OutcomeApplicationBusy, OutcomeDeploymentFailed:
		return value
	default:
		return OutcomePersistenceUnavailable
	}
}

func normalizeCoordinatorState(value string) string {
	switch value {
	case StateDisabled, StateIdle, StateDispatching, StateDeploying, StatePaused, StateRetryWait:
		return value
	default:
		return ObservedStateNone
	}
}

func normalizeCoordinatorPauseCode(value string) string {
	switch value {
	case PauseApprovalRequired, PauseDeploymentFailed, PauseMissingConfig, PauseSourceAccessLost,
		PauseInvalidSource, PauseProviderUnavailable, PauseRelayUnavailable:
		return value
	default:
		return ObservedPauseNone
	}
}

func normalizeCoordinatorNextAction(value string) string {
	switch value {
	case NextActionNone, NextActionResolve, NextActionResolveCooldown, NextActionDispatch, NextActionPollJob,
		NextActionRetry, NextActionApprovalRequired, NextActionResumeRequired:
		return value
	default:
		return NextActionNone
	}
}

type realClock struct{}

func (realClock) Now() time.Time                     { return time.Now() }
func (realClock) NewTimer(delay time.Duration) Timer { return realTimer{Timer: time.NewTimer(delay)} }

type realTimer struct{ *time.Timer }

func (timer realTimer) C() <-chan time.Time { return timer.Timer.C }
