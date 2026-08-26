package controllerrelay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	SupervisorHandshake    = "handshake"
	SupervisorFallback     = "fallback"
	SupervisorReady        = "ready"
	SupervisorDisconnect   = "disconnect"
	SupervisorBackoff      = "backoff"
	SupervisorAttention    = "needs_attention"
	SupervisorStopped      = "stopped"
	SupervisorRecovery     = "recovery"
	SupervisorDiagnostics  = "diagnostics"
	maximumSupervisorTries = 1_000_000
)

var (
	ErrSupervisorNotPaused                   = errors.New("controller relay supervisor is not paused")
	ErrSupervisorRateLimited                 = errors.New("controller relay supervisor resume is rate limited")
	errSupervisorReconcile                   = errors.New("controller relay supervisor reconcile requested")
	errSupervisorBackoffCancellationObserved = errors.New("controller relay supervisor backoff cancellation observed")
)

// SupervisorEvent is the safe, aggregate event surface for a session
// lifecycle. It never includes relay frames, session identifiers, credentials,
// source data, or provider data.
type SupervisorEvent struct {
	Kind          string
	Stage         string
	Fallback      bool
	Outcome       string
	Attempt       uint32
	NextAttemptAt *time.Time
	Recovery      SupervisorRecoverySummary
	Diagnostics   SessionLifecycleDiagnostics
}

func (SupervisorEvent) String() string         { return "controller relay supervisor event" }
func (value SupervisorEvent) GoString() string { return value.String() }
func (value SupervisorEvent) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("kind", safeSupervisorKind(value.Kind)),
		slog.String("stage", safeSupervisorStage(value.Stage)),
		slog.Bool("fallback", value.Fallback),
		slog.String("outcome", safeSessionErrorCode(value.Outcome)),
		slog.Uint64("attempt", uint64(value.Attempt)),
		slog.Int("recovery_scanned", value.Recovery.Scanned),
		slog.Int("recovery_cleaned", value.Recovery.Cleaned),
		slog.Int("recovery_issues", value.Recovery.Issues),
		slog.Int("pending_commands", value.Diagnostics.PendingCommands),
		slog.Int("active_leases", value.Diagnostics.ActiveLeases),
		slog.Int("expired_leases", value.Diagnostics.ExpiredLeases),
	)
}

func (value SupervisorEvent) MarshalJSON() ([]byte, error) {
	type safeEvent struct {
		Kind          string                      `json:"kind"`
		Stage         string                      `json:"stage"`
		Fallback      bool                        `json:"fallback"`
		Outcome       string                      `json:"outcome"`
		Attempt       uint32                      `json:"attempt"`
		NextAttemptAt *time.Time                  `json:"next_attempt_at,omitempty"`
		Recovery      SupervisorRecoverySummary   `json:"recovery"`
		Diagnostics   SessionLifecycleDiagnostics `json:"diagnostics"`
	}
	return json.Marshal(safeEvent{Kind: safeSupervisorKind(value.Kind), Stage: safeSupervisorStage(value.Stage), Fallback: value.Fallback, Outcome: safeSessionErrorCode(value.Outcome), Attempt: value.Attempt, NextAttemptAt: value.NextAttemptAt, Recovery: value.Recovery, Diagnostics: value.Diagnostics})
}

// SupervisorRecoverySummary contains only bounded recovery aggregates.
type SupervisorRecoverySummary struct {
	Scanned           int
	Cleaned           int
	Issues            int
	LeaseScanned      int
	RevokedScanned    int
	CredentialScanned int
	TemporaryScanned  int
}

// SupervisorObserver can record safe lifecycle aggregates. Observer failures
// are ignored so observability cannot alter lifecycle decisions.
type SupervisorObserver func(context.Context, SupervisorEvent) error

// SupervisorSnapshot is a lock-free-copyable aggregate view of the last
// lifecycle result; controller and key identifiers remain private.
type SupervisorSnapshot struct {
	State                  string
	Epoch                  uint64
	Fence                  uint64
	Paused                 bool
	Outcome                string
	Recovery               SupervisorRecoverySummary
	Diagnostics            SessionLifecycleDiagnostics
	DiagnosticsUnavailable bool
	ObserverDropped        uint64
}

func (value SupervisorSnapshot) String() string   { return "controller relay supervisor snapshot" }
func (value SupervisorSnapshot) GoString() string { return value.String() }
func (value SupervisorSnapshot) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("state", safeSupervisorStage(value.State)),
		slog.Bool("paused", value.Paused),
		slog.String("outcome", safeSessionErrorCode(value.Outcome)),
		slog.Uint64("observer_dropped", value.ObserverDropped),
	)
}

func (value SupervisorSnapshot) MarshalJSON() ([]byte, error) {
	type safeSnapshot struct {
		State                  string                      `json:"state"`
		Epoch                  uint64                      `json:"epoch"`
		Fence                  uint64                      `json:"fence"`
		Paused                 bool                        `json:"paused"`
		Outcome                string                      `json:"outcome"`
		Recovery               SupervisorRecoverySummary   `json:"recovery"`
		Diagnostics            SessionLifecycleDiagnostics `json:"diagnostics"`
		DiagnosticsUnavailable bool                        `json:"diagnostics_unavailable"`
		ObserverDropped        uint64                      `json:"observer_dropped"`
	}
	return json.Marshal(safeSnapshot{State: safeSupervisorStage(value.State), Epoch: value.Epoch, Fence: value.Fence, Paused: value.Paused, Outcome: safeSessionErrorCode(value.Outcome), Recovery: value.Recovery, Diagnostics: value.Diagnostics, DiagnosticsUnavailable: value.DiagnosticsUnavailable, ObserverDropped: value.ObserverDropped})
}

// SessionLifecycleDiagnostics contains only operational counts and age
// aggregates sampled at lifecycle boundaries.
type SessionLifecycleDiagnostics struct {
	PendingCommands  int
	OldestPendingAge time.Duration
	ActiveLeases     int
	ExpiredLeases    int
}

// sessionSupervisorStore is intentionally narrower than Repository. The
// concrete repository uses BeginSessionEpoch to establish a FK-backed fence
// before recovery, and supplies the CAS transition primitive thereafter.
type sessionSupervisorStore interface {
	BeginSessionEpoch(context.Context, time.Time) (SessionStatus, error)
	AdvanceSessionStatus(context.Context, uint64, uint64, SessionStatus) error
	SessionLifecycleDiagnostics(context.Context, string, time.Time) (SessionLifecycleDiagnostics, error)
}

type controllerKeyRecovery interface {
	RecoverControllerKeysPage(context.Context, ControllerKeyRecoveryCursor, int) (ControllerKeyRecoveryPage, error)
}

type fencedReadyRotationCompleter interface {
	CompleteRotationAfterFencedReady(context.Context, string, string, uint64, uint64) error
}

type sessionRunOnce interface{ RunOnce(context.Context) error }

type supervisorResumeToken struct {
	run   uint64
	pause uint64
}

type SupervisorConfig struct {
	RecoveryPageSize  int
	MaxRecoveryPages  int
	InitialBackoff    time.Duration
	MaximumBackoff    time.Duration
	BackoffMultiplier uint32
	JitterPercent     uint32
	ResumeInterval    time.Duration
	ShutdownTimeout   time.Duration
	Now               func() time.Time
	Sleep             func(context.Context, time.Duration) error
	Jitter            func(time.Duration, uint32) time.Duration
	// OnReady is a dedicated durable-lifecycle hook. Implementations must be
	// nonblocking and coalescing; unlike Observer, delivery is not lossy.
	OnReady  func()
	Observer SupervisorObserver
}

func DefaultSupervisorConfig() SupervisorConfig {
	return SupervisorConfig{
		RecoveryPageSize:  128,
		MaxRecoveryPages:  10000,
		InitialBackoff:    500 * time.Millisecond,
		MaximumBackoff:    5 * time.Minute,
		BackoffMultiplier: 2,
		JitterPercent:     20,
		ResumeInterval:    time.Second,
		ShutdownTimeout:   10 * time.Second,
		Now:               time.Now,
		Sleep:             supervisorSleep,
		Jitter:            supervisorJitter,
	}
}

// Supervisor owns retry, durable status fencing, startup recovery, and explicit
// resume. It is a library seam; it starts no background goroutine itself.
type Supervisor struct {
	runner    sessionRunOnce
	store     sessionSupervisorStore
	recovery  controllerKeyRecovery
	completer fencedReadyRotationCompleter
	config    SupervisorConfig

	mu                 sync.RWMutex
	snapshot           SupervisorSnapshot
	status             SessionStatus
	reconnect          SessionTransportReconnect
	resumeCh           chan supervisorResumeToken
	resumeQueued       bool
	lastResume         time.Time
	running            bool
	runGeneration      uint64
	pauseGeneration    uint64
	pauseRunGeneration uint64
	reconcileRequested uint64
	reconcileApplied   uint64
	reconcileTarget    uint64
	reconcileIssued    bool
	reconcileCancel    context.CancelCauseFunc
	reconcileObserved  bool
	readyTransition    bool
	afterRunOnce       func()
	afterBackoffSleep  func()
	observerMu         sync.Mutex
	observerBusy       bool
	observerNext       *supervisorObserverDelivery
}

type supervisorObserverDelivery struct {
	ctx   context.Context
	event SupervisorEvent
}

func NewSupervisor(transport *SessionTransport, store sessionSupervisorStore, recovery controllerKeyRecovery, completer fencedReadyRotationCompleter, config SupervisorConfig) (*Supervisor, error) {
	if transport == nil || store == nil || recovery == nil || completer == nil || !validSupervisorConfig(config) {
		return nil, errors.New("invalid controller relay supervisor configuration")
	}
	if transport.config.ControlHandler != nil {
		handler, ok := transport.config.ControlHandler.(sessionFenceRequiredControlHandler)
		if !ok || !handler.requiresSessionFence() {
			return nil, errors.New("invalid controller relay supervisor configuration")
		}
	}
	if transport.store != nil {
		if _, ok := transport.store.(fencedSessionTransportStore); !ok {
			return nil, errors.New("invalid controller relay supervisor configuration")
		}
	}
	supervisor := &Supervisor{store: store, recovery: recovery, completer: completer, config: config, resumeCh: make(chan supervisorResumeToken, 1), snapshot: SupervisorSnapshot{DiagnosticsUnavailable: true}}
	token, claimed := transport.claimLifecycle(supervisor.observeTransport)
	if !claimed {
		return nil, errors.New("controller relay supervisor already owns transport lifecycle")
	}
	supervisor.runner = sessionTransportRunner{transport: transport, token: token}
	return supervisor, nil
}

func (supervisor *Supervisor) Snapshot() SupervisorSnapshot {
	if supervisor == nil {
		return SupervisorSnapshot{}
	}
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return supervisor.snapshot
}

// Resume coalesces concurrent accepted requests. It only wakes a paused Run
// loop and rate-limits accepted requests; it never dials directly.
func (supervisor *Supervisor) Resume() error {
	if supervisor == nil {
		return ErrSupervisorNotPaused
	}
	now := supervisor.now()
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if !supervisor.running || !supervisor.snapshot.Paused {
		return ErrSupervisorNotPaused
	}
	if supervisor.resumeQueued {
		return nil
	}
	if !supervisor.lastResume.IsZero() && now.Sub(supervisor.lastResume) < supervisor.config.ResumeInterval {
		return ErrSupervisorRateLimited
	}
	supervisor.lastResume = now
	supervisor.resumeQueued = true
	token := supervisorResumeToken{run: supervisor.runGeneration, pause: supervisor.pauseGeneration}
	select {
	case supervisor.resumeCh <- token:
	default:
	}
	return nil
}

// Reconcile coalesces durable management changes into one immediate session
// refresh. It never starts a connection and never resumes a paused or
// previously stopped supervisor. A request made before the first Run is
// retained for that Run's initial fenced handshake.
func (supervisor *Supervisor) Reconcile() {
	if supervisor == nil {
		return
	}
	supervisor.mu.Lock()
	if !supervisor.running {
		if supervisor.runGeneration == 0 && supervisor.reconcileRequested == 0 {
			supervisor.reconcileRequested = 1
		}
		supervisor.mu.Unlock()
		return
	}
	if supervisor.snapshot.Paused {
		supervisor.mu.Unlock()
		return
	}
	// A request newer than this attempt's captured target is already queued.
	// Coalesce callers until a new RunOnce captures that generation.
	if supervisor.reconcileRequested > supervisor.reconcileTarget {
		supervisor.mu.Unlock()
		return
	}
	supervisor.reconcileRequested++
	if supervisor.reconcileRequested == 0 {
		// A Run generation cannot realistically exhaust uint64 values. Fail
		// closed by retaining a pending maximum generation if it ever does.
		supervisor.reconcileRequested--
	}
	if supervisor.reconcileCancel != nil && !supervisor.reconcileIssued && !supervisor.readyTransition {
		supervisor.reconcileIssued = true
		supervisor.reconcileCancel(errSupervisorReconcile)
	}
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) Run(ctx context.Context) error {
	if supervisor == nil || ctx == nil {
		return sessionFailure(sessionErrorIdentity, true)
	}
	supervisor.mu.Lock()
	if supervisor.running {
		supervisor.mu.Unlock()
		return sessionFailure(sessionErrorIdentity, true)
	}
	firstRun := supervisor.runGeneration == 0
	supervisor.running = true
	supervisor.runGeneration++
	if supervisor.runGeneration == 0 {
		supervisor.runGeneration = 1
	}
	supervisor.clearResumeLocked()
	if firstRun {
		supervisor.prepareFirstRunReconcileLocked()
	} else {
		supervisor.clearReconcileLocked()
	}
	supervisor.mu.Unlock()
	defer func() {
		supervisor.mu.Lock()
		supervisor.running = false
		supervisor.clearResumeLocked()
		supervisor.clearReconcileLocked()
		supervisor.mu.Unlock()
	}()

	needsRecovery := true
	for {
		if needsRecovery {
			if err := supervisor.recover(ctx); err != nil {
				if ctx.Err() != nil {
					supervisor.stop(ctx)
					return nil
				}
				supervisor.pause(ctx, recoveryFailureOutcome(err))
				if !supervisor.waitResume(ctx) {
					supervisor.stop(ctx)
					return nil
				}
				continue
			}
			needsRecovery = false
		}

		runOnceCtx, cancelRunOnce := context.WithCancelCause(ctx)
		supervisor.mu.Lock()
		supervisor.reconcileCancel = cancelRunOnce
		supervisor.reconcileTarget = supervisor.reconcileRequested
		supervisor.reconcileIssued = false
		supervisor.reconcileObserved = false
		supervisor.mu.Unlock()
		err := supervisor.runner.RunOnce(runOnceCtx)
		if supervisor.afterRunOnce != nil {
			supervisor.afterRunOnce()
		}
		cause := context.Cause(runOnceCtx)
		supervisor.mu.Lock()
		supervisor.reconcileCancel = nil
		reconcileObserved := supervisor.reconcileObserved
		supervisor.mu.Unlock()
		cancelRunOnce(context.Canceled)
		if ctx.Err() != nil {
			supervisor.stop(ctx)
			return nil
		}
		if reconcileCancellation(cause, err, reconcileObserved) {
			continue
		}
		if err == nil {
			err = sessionFailure(sessionErrorConnectionClosed, false)
		}
		info, ok := ClassifySessionTransportError(err)
		if !ok {
			info = SessionTransportErrorInfo{Code: sessionErrorProtocol, Fatal: true}
		}
		supervisor.sampleDiagnostics(ctx)
		supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorDisconnect, Stage: supervisor.Snapshot().State, Outcome: info.Code, Diagnostics: supervisor.Snapshot().Diagnostics})
		if info.Fatal {
			supervisor.pause(ctx, info.Code)
			if !supervisor.waitResume(ctx) {
				supervisor.stop(ctx)
				return nil
			}
			needsRecovery = true
			continue
		}
		delay, backoffErr := supervisor.persistBackoff(ctx, info.Code)
		if backoffErr != nil {
			supervisor.pause(ctx, sessionErrorPersistence)
			if !supervisor.waitResume(ctx) {
				supervisor.stop(ctx)
				return nil
			}
			needsRecovery = true
			continue
		}
		backoffCtx, cancelBackoff := context.WithCancelCause(ctx)
		supervisor.mu.Lock()
		supervisor.reconcileCancel = cancelBackoff
		supervisor.reconcileIssued = false
		if supervisor.reconcileRequested > supervisor.reconcileTarget {
			supervisor.reconcileIssued = true
			cancelBackoff(errSupervisorReconcile)
		}
		supervisor.mu.Unlock()
		err = supervisor.config.Sleep(backoffCtx, delay)
		if supervisor.afterBackoffSleep != nil {
			supervisor.afterBackoffSleep()
		}
		supervisor.mu.Lock()
		supervisor.reconcileCancel = nil
		supervisor.mu.Unlock()
		cancelBackoff(context.Canceled)
		if ctx.Err() != nil {
			supervisor.stop(ctx)
			return nil
		}
		if errors.Is(err, errSupervisorBackoffCancellationObserved) {
			continue
		}
		if err != nil {
			supervisor.stop(ctx)
			return nil
		}
	}
}

func (supervisor *Supervisor) recover(ctx context.Context) error {
	supervisor.beginRecovery()
	status, err := supervisor.store.BeginSessionEpoch(ctx, supervisor.now())
	if err == nil {
		supervisor.setStatus(status, "")
	} else if errors.Is(err, ErrNotFound) {
		supervisor.clearStatus()
		return sessionFailure(sessionErrorIdentity, true)
	} else {
		supervisor.clearStatus()
		return sessionFailure(sessionErrorPersistence, true)
	}

	cursor := ControllerKeyRecoveryCursor{}
	summary := SupervisorRecoverySummary{}
	defer func() { supervisor.setRecovery(summary) }()
	var recoveredErr error
	seen := map[ControllerKeyRecoveryCursor]struct{}{cursor: {}}
	for pageCount := 0; pageCount < supervisor.config.MaxRecoveryPages; pageCount++ {
		page, pageErr := supervisor.recovery.RecoverControllerKeysPage(ctx, cursor, supervisor.config.RecoveryPageSize)
		summary.Scanned += page.Scanned
		summary.Cleaned += page.Cleaned
		summary.Issues += len(page.NeedsAttention)
		summary.LeaseScanned += page.LeaseScanned
		summary.RevokedScanned += page.RevokedScanned
		summary.CredentialScanned += page.CredentialScanned
		summary.TemporaryScanned += page.TemporaryScanned
		supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorRecovery, Recovery: summary})
		if pageErr != nil && recoveredErr == nil {
			recoveredErr = pageErr
		}
		if page.Complete {
			supervisor.setRecovery(summary)
			supervisor.sampleDiagnostics(ctx)
			if summary.Issues != 0 && recoveredErr == nil {
				recoveredErr = sessionFailure(sessionErrorCredential, true)
			}
			return recoveredErr
		}
		if page.NextCursor == cursor {
			if pageErr != nil {
				return pageErr
			}
			return sessionFailure(sessionErrorPersistence, true)
		}
		if _, found := seen[page.NextCursor]; found {
			return sessionFailure(sessionErrorPersistence, true)
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return sessionFailure(sessionErrorPersistence, true)
}

func (supervisor *Supervisor) observeTransport(ctx context.Context, event *sessionTransportLifecycleEvent) error {
	if event == nil {
		return sessionFailure(sessionErrorPersistence, true)
	}
	switch event.Stage {
	case SessionTransportConnecting:
		status, err := supervisor.nextCandidate(ctx, event)
		if err != nil {
			supervisor.markObservedReconcileCancellation(ctx, err)
			return err
		}
		supervisor.setStatus(status, "")
		kind := SupervisorHandshake
		if event.Fallback {
			kind = SupervisorFallback
		}
		supervisor.emit(ctx, SupervisorEvent{Kind: kind, Stage: SessionTransportConnecting, Fallback: event.Fallback, Attempt: status.Attempt})
	case SessionTransportAuthenticating:
		status, err := supervisor.advance(ctx, event.ControllerID, func(current SessionStatus) SessionStatus {
			current.Fence++
			current.State = SessionAuthenticating
			current.KeyID = event.KeyID
			current.ErrorCode = ""
			current.NextAttemptAt = nil
			current.StateChangedAt = supervisor.now()
			current.UpdatedAt = current.StateChangedAt
			return current
		})
		if err != nil {
			supervisor.markObservedReconcileCancellation(ctx, err)
			return err
		}
		supervisor.setStatus(status, "")
		supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorHandshake, Stage: SessionTransportAuthenticating, Fallback: event.Fallback, Attempt: status.Attempt})
	case SessionTransportReady:
		supervisor.mu.Lock()
		if supervisor.reconcileIssued && supervisor.reconcileRequested > supervisor.reconcileTarget {
			supervisor.reconcileObserved = true
			supervisor.mu.Unlock()
			return errSupervisorReconcile
		}
		supervisor.readyTransition = true
		supervisor.mu.Unlock()
		readySucceeded := false
		defer func() { supervisor.finishReadyTransition(readySucceeded) }()
		readyAt := supervisor.now()
		status, err := supervisor.advance(ctx, event.ControllerID, func(current SessionStatus) SessionStatus {
			current.Fence++
			current.State = SessionReady
			current.KeyID = event.KeyID
			current.ErrorCode = ""
			current.Attempt = 0
			current.NextAttemptAt = nil
			current.LastReadyAt = &readyAt
			current.LastSeenAt = &readyAt
			current.StateChangedAt = readyAt
			current.UpdatedAt = readyAt
			return current
		})
		if err != nil {
			return err
		}
		supervisor.mu.Lock()
		supervisor.reconnect = clampReconnect(event.Reconnect, supervisor.config)
		supervisor.mu.Unlock()
		supervisor.setStatus(status, "")
		event.readyOwner = sessionReadyOwner{Epoch: status.Epoch, Fence: status.Fence}
		if supervisor.config.OnReady != nil {
			supervisor.config.OnReady()
		}
		if event.Pending && supervisor.completer != nil {
			if err = supervisor.completer.CompleteRotationAfterFencedReady(ctx, event.ControllerID, event.KeyID, status.Epoch, status.Fence); err != nil {
				return err
			}
		}
		supervisor.sampleDiagnostics(ctx)
		supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorReady, Stage: SessionTransportReady, Fallback: event.Fallback})
		readySucceeded = true
	default:
		return sessionFailure(sessionErrorProtocol, true)
	}
	return nil
}

func (supervisor *Supervisor) nextCandidate(ctx context.Context, event *sessionTransportLifecycleEvent) (SessionStatus, error) {
	current := supervisor.ownedStatus()
	if current.ControllerID == "" || current.ControllerID != event.ControllerID || current.Epoch == 0 || current.Fence == 0 {
		return SessionStatus{}, ErrState
	}
	attempt := current.Attempt + 1
	if attempt == 0 || attempt > maximumSupervisorTries {
		attempt = maximumSupervisorTries
	}
	now := supervisor.now()
	next := SessionStatus{ControllerID: event.ControllerID, Epoch: current.Epoch + 1, Fence: 1, State: SessionConnecting, KeyID: event.KeyID, Attempt: attempt, LastReadyAt: current.LastReadyAt, LastSeenAt: current.LastSeenAt, StateChangedAt: now, UpdatedAt: now}
	if next.Epoch == 0 || next.Epoch > math.MaxInt64 {
		return SessionStatus{}, ErrState
	}
	if err := supervisor.store.AdvanceSessionStatus(ctx, current.Epoch, current.Fence, next); err != nil {
		return SessionStatus{}, err
	}
	return next, nil
}

func (supervisor *Supervisor) persistBackoff(ctx context.Context, outcome string) (time.Duration, error) {
	current := supervisor.ownedStatus()
	if current.ControllerID == "" || current.Epoch == 0 || current.Fence == 0 {
		return 0, ErrState
	}
	attempt := current.Attempt
	if attempt == 0 {
		attempt = 1
	}
	delay := supervisor.backoff(attempt)
	nextAt := supervisor.now().Add(delay)
	status, err := supervisor.advance(ctx, current.ControllerID, func(current SessionStatus) SessionStatus {
		current.Fence++
		current.State = SessionBackoff
		current.KeyID = ""
		current.ErrorCode = durableSessionOutcome(outcome)
		current.Attempt = attempt
		current.NextAttemptAt = &nextAt
		current.StateChangedAt = supervisor.now()
		current.UpdatedAt = current.StateChangedAt
		return current
	})
	if err != nil {
		return 0, err
	}
	supervisor.setStatus(status, outcome)
	supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorBackoff, Stage: SessionBackoff, Outcome: outcome, Attempt: attempt, NextAttemptAt: &nextAt})
	return delay, nil
}

func (supervisor *Supervisor) pause(ctx context.Context, outcome string) {
	current := supervisor.ownedStatus()
	if current.ControllerID != "" && current.Epoch != 0 && current.Fence != 0 {
		if status, err := supervisor.advance(ctx, current.ControllerID, func(current SessionStatus) SessionStatus {
			current.Fence++
			current.State = SessionNeedsAttention
			current.KeyID = ""
			current.ErrorCode = durableSessionOutcome(outcome)
			current.NextAttemptAt = nil
			current.StateChangedAt = supervisor.now()
			current.UpdatedAt = current.StateChangedAt
			return current
		}); err == nil {
			supervisor.setStatus(status, outcome)
		}
	}
	supervisor.mu.Lock()
	supervisor.clearReconcileLocked()
	supervisor.pauseGeneration++
	if supervisor.pauseGeneration == 0 {
		supervisor.pauseGeneration = 1
	}
	supervisor.pauseRunGeneration = supervisor.runGeneration
	supervisor.resumeQueued = false
	supervisor.drainResumeLocked()
	supervisor.snapshot.Paused = true
	supervisor.snapshot.Outcome = safeSessionErrorCode(outcome)
	supervisor.mu.Unlock()
	supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorAttention, Stage: SessionNeedsAttention, Outcome: safeSessionErrorCode(outcome)})
}

func (supervisor *Supervisor) stop(ctx context.Context) {
	current := supervisor.ownedStatus()
	outcome := ""
	if current.ControllerID != "" && current.Epoch != 0 && current.Fence != 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supervisor.config.ShutdownTimeout)
		defer cancel()
		if status, err := supervisor.advance(persistCtx, current.ControllerID, func(current SessionStatus) SessionStatus {
			current.Fence++
			current.State = SessionStopped
			current.KeyID = ""
			current.ErrorCode = ""
			current.Attempt = 0
			current.NextAttemptAt = nil
			current.StateChangedAt = supervisor.now()
			current.UpdatedAt = current.StateChangedAt
			return current
		}); err == nil {
			supervisor.setStatus(status, "")
		} else {
			outcome = sessionErrorPersistence
		}
	}
	supervisor.mu.Lock()
	supervisor.clearResumeLocked()
	supervisor.clearReconcileLocked()
	if outcome == "" {
		supervisor.snapshot.Outcome = ""
	} else {
		supervisor.snapshot.Outcome = outcome
	}
	supervisor.mu.Unlock()
	supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorStopped, Stage: SessionStopped, Outcome: outcome})
}

func (supervisor *Supervisor) waitResume(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case token := <-supervisor.resumeCh:
			supervisor.mu.Lock()
			valid := supervisor.running && supervisor.snapshot.Paused &&
				token.run == supervisor.runGeneration && token.pause == supervisor.pauseGeneration &&
				supervisor.pauseRunGeneration == supervisor.runGeneration
			if valid {
				supervisor.resumeQueued = false
				supervisor.snapshot.Paused = false
			}
			supervisor.mu.Unlock()
			if valid {
				return true
			}
		}
	}
}

func (supervisor *Supervisor) advance(ctx context.Context, controllerID string, mutate func(SessionStatus) SessionStatus) (SessionStatus, error) {
	current := supervisor.ownedStatus()
	if current.ControllerID != controllerID || current.Epoch == 0 || current.Fence == 0 {
		return SessionStatus{}, ErrState
	}
	next := mutate(current)
	if err := supervisor.store.AdvanceSessionStatus(ctx, current.Epoch, current.Fence, next); err != nil {
		return SessionStatus{}, err
	}
	return next, nil
}

func (supervisor *Supervisor) backoff(attempt uint32) time.Duration {
	supervisor.mu.RLock()
	policy := supervisor.reconnect
	supervisor.mu.RUnlock()
	base := supervisor.config.InitialBackoff
	maximum := supervisor.config.MaximumBackoff
	multiplier := supervisor.config.BackoffMultiplier
	jitter := supervisor.config.JitterPercent
	if policy.InitialDelay > 0 {
		base, maximum, multiplier, jitter = policy.InitialDelay, policy.MaximumDelay, policy.Multiplier, policy.Jitter
	}
	delay := base
	for index := uint32(1); index < attempt && delay < maximum; index++ {
		if delay > maximum/time.Duration(multiplier) {
			delay = maximum
			break
		}
		delay *= time.Duration(multiplier)
	}
	if delay > maximum {
		delay = maximum
	}
	delay = supervisor.config.Jitter(delay, jitter)
	if delay < base {
		return base
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func (supervisor *Supervisor) setStatus(status SessionStatus, outcome string) {
	supervisor.mu.Lock()
	supervisor.status = status
	supervisor.snapshot.State = status.State
	supervisor.snapshot.Epoch = status.Epoch
	supervisor.snapshot.Fence = status.Fence
	if outcome == "" {
		supervisor.snapshot.Outcome = ""
	} else {
		supervisor.snapshot.Outcome = safeSessionErrorCode(outcome)
	}
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) setRecovery(summary SupervisorRecoverySummary) {
	supervisor.mu.Lock()
	supervisor.snapshot.Recovery = summary
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) beginRecovery() {
	supervisor.mu.Lock()
	supervisor.snapshot.Recovery = SupervisorRecoverySummary{}
	supervisor.snapshot.Diagnostics = SessionLifecycleDiagnostics{}
	supervisor.snapshot.DiagnosticsUnavailable = true
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) clearStatus() {
	supervisor.mu.Lock()
	supervisor.status = SessionStatus{}
	supervisor.snapshot.State = ""
	supervisor.snapshot.Epoch = 0
	supervisor.snapshot.Fence = 0
	supervisor.snapshot.Outcome = ""
	supervisor.snapshot.Diagnostics = SessionLifecycleDiagnostics{}
	supervisor.snapshot.DiagnosticsUnavailable = true
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) sampleDiagnostics(ctx context.Context) {
	status := supervisor.ownedStatus()
	if status.ControllerID == "" {
		return
	}
	diagnostics, err := supervisor.store.SessionLifecycleDiagnostics(ctx, status.ControllerID, supervisor.now())
	if err != nil {
		supervisor.mu.Lock()
		supervisor.snapshot.Diagnostics = SessionLifecycleDiagnostics{}
		supervisor.snapshot.DiagnosticsUnavailable = true
		supervisor.mu.Unlock()
		supervisor.emit(ctx, SupervisorEvent{Kind: SupervisorDiagnostics, Outcome: sessionErrorPersistence})
		return
	}
	supervisor.mu.Lock()
	supervisor.snapshot.Diagnostics = diagnostics
	supervisor.snapshot.DiagnosticsUnavailable = false
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) clearResumeLocked() {
	supervisor.snapshot.Paused = false
	supervisor.resumeQueued = false
	supervisor.pauseRunGeneration = 0
	supervisor.lastResume = time.Time{}
	supervisor.drainResumeLocked()
}

func (supervisor *Supervisor) drainResumeLocked() {
	for {
		select {
		case <-supervisor.resumeCh:
		default:
			return
		}
	}
}

func (supervisor *Supervisor) clearReconcileLocked() {
	supervisor.reconcileRequested = 0
	supervisor.reconcileApplied = 0
	supervisor.reconcileTarget = 0
	supervisor.reconcileIssued = false
	supervisor.reconcileCancel = nil
	supervisor.reconcileObserved = false
	supervisor.readyTransition = false
}

func (supervisor *Supervisor) prepareFirstRunReconcileLocked() {
	requested := supervisor.reconcileRequested
	supervisor.clearReconcileLocked()
	supervisor.reconcileRequested = requested
}

func (supervisor *Supervisor) markObservedReconcileCancellation(ctx context.Context, err error) {
	if !errors.Is(context.Cause(ctx), errSupervisorReconcile) || !errors.Is(err, context.Canceled) {
		return
	}
	supervisor.mu.Lock()
	supervisor.reconcileObserved = true
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) finishReadyTransition(succeeded bool) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.readyTransition = false
	if !succeeded {
		return
	}
	if supervisor.reconcileTarget > supervisor.reconcileApplied {
		supervisor.reconcileApplied = supervisor.reconcileTarget
	}
	if supervisor.reconcileRequested > supervisor.reconcileApplied && supervisor.reconcileCancel != nil && !supervisor.reconcileIssued {
		supervisor.reconcileIssued = true
		supervisor.reconcileCancel(errSupervisorReconcile)
	}
}

func (supervisor *Supervisor) ownedStatus() SessionStatus {
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return supervisor.status
}

func (supervisor *Supervisor) emit(ctx context.Context, event SupervisorEvent) {
	if supervisor.config.Observer == nil {
		return
	}
	supervisor.observerMu.Lock()
	if supervisor.observerBusy {
		supervisor.observerNext = &supervisorObserverDelivery{ctx: ctx, event: event}
		supervisor.observerMu.Unlock()
		supervisor.mu.Lock()
		supervisor.snapshot.ObserverDropped++
		supervisor.mu.Unlock()
		return
	}
	supervisor.observerBusy = true
	supervisor.observerMu.Unlock()
	go supervisor.deliverObserver(supervisorObserverDelivery{ctx: ctx, event: event})
}

// deliverObserver owns at most one bounded, coalescing observer worker for a
// supervisor. Every delivery keeps its originating lifecycle context, so a
// worker unblocked after a cancellation cannot reuse that canceled context for
// a later Run generation.
func (supervisor *Supervisor) deliverObserver(delivery supervisorObserverDelivery) {
	for {
		_ = supervisor.config.Observer(delivery.ctx, delivery.event)
		supervisor.observerMu.Lock()
		if supervisor.observerNext == nil {
			supervisor.observerBusy = false
			supervisor.observerMu.Unlock()
			return
		}
		delivery = *supervisor.observerNext
		supervisor.observerNext = nil
		supervisor.observerMu.Unlock()
	}
}

func (supervisor *Supervisor) now() time.Time { return supervisor.config.Now().UTC() }

func validSupervisorConfig(config SupervisorConfig) bool {
	return config.RecoveryPageSize > 0 && config.RecoveryPageSize <= 1000 && config.MaxRecoveryPages > 0 && config.MaxRecoveryPages <= 10000 &&
		config.InitialBackoff > 0 && config.InitialBackoff <= config.MaximumBackoff && config.MaximumBackoff <= 5*time.Minute &&
		config.BackoffMultiplier >= 2 && config.BackoffMultiplier <= 4 && config.JitterPercent <= 50 && config.ResumeInterval >= 0 && config.ResumeInterval <= time.Hour && config.ShutdownTimeout > 0 && config.ShutdownTimeout <= time.Minute &&
		config.Now != nil && config.Sleep != nil && config.Jitter != nil
}

func clampReconnect(value SessionTransportReconnect, config SupervisorConfig) SessionTransportReconnect {
	if value.InitialDelay <= 0 || value.MaximumDelay < value.InitialDelay || value.Multiplier < 2 || value.Multiplier > 4 || value.Jitter > 50 {
		return SessionTransportReconnect{}
	}
	value.InitialDelay = minDuration(maxDuration(value.InitialDelay, config.InitialBackoff), config.MaximumBackoff)
	value.MaximumDelay = minDuration(maxDuration(value.MaximumDelay, value.InitialDelay), config.MaximumBackoff)
	if value.MaximumDelay < value.InitialDelay {
		value.MaximumDelay = value.InitialDelay
	}
	if value.Jitter > config.JitterPercent {
		value.Jitter = config.JitterPercent
	}
	return value
}

func durableSessionOutcome(outcome string) string {
	switch safeSessionErrorCode(outcome) {
	case sessionErrorRelayUnavailable, sessionErrorConnectionClosed, sessionErrorQueueSaturated, sessionErrorExpired:
		return ErrorRelayUnavailable
	case sessionErrorProtocol:
		return ErrorProtocol
	case sessionErrorIdentity, sessionErrorCredential:
		return ErrorKeyRevoked
	case sessionErrorPersistence:
		return ErrorRelayUnavailable
	default:
		return ErrorProtocol
	}
}

func safeSupervisorKind(value string) string {
	switch value {
	case SupervisorHandshake, SupervisorFallback, SupervisorReady, SupervisorDisconnect, SupervisorBackoff, SupervisorAttention, SupervisorStopped, SupervisorRecovery, SupervisorDiagnostics:
		return value
	default:
		return SupervisorDiagnostics
	}
}

func safeSupervisorStage(value string) string {
	switch value {
	case "", SessionDisconnected, SessionConnecting, SessionAuthenticating, SessionReady, SessionBackoff, SessionNeedsAttention, SessionStopped:
		return value
	default:
		return SessionDisconnected
	}
}

func recoveryFailureOutcome(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return sessionErrorPersistence
	}
	if info, ok := ClassifySessionTransportError(err); ok {
		return info.Code
	}
	var controlErr *SessionControlError
	if !errors.As(err, &controlErr) {
		return sessionErrorProtocol
	}
	switch controlErr.Code {
	case controlErrorCredential:
		return sessionErrorCredential
	case controlErrorPersistence:
		return sessionErrorPersistence
	case controlErrorExpired:
		return sessionErrorExpired
	case controlErrorRevoked:
		return sessionErrorIdentity
	default:
		return sessionErrorProtocol
	}
}

func reconcileCancellation(cause, runErr error, observed bool) bool {
	if !errors.Is(cause, errSupervisorReconcile) {
		return false
	}
	return observed || errors.Is(runErr, errSessionCancellationObserved)
}

func supervisorSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errSupervisorReconcile) {
			return errSupervisorBackoffCancellationObserved
		}
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func supervisorJitter(delay time.Duration, percent uint32) time.Duration {
	if delay <= 0 || percent == 0 {
		return delay
	}
	spread := int64(delay) * int64(percent) / 100
	if spread <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int64N(spread*2+1)-spread)
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
