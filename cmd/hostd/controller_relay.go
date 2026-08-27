package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controllerrelay"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/sourceconnections"
)

const controllerRelayShutdownTimeout = 10 * time.Second

type controllerRelayRunner interface {
	Run(context.Context) error
	Snapshot() controllerrelay.SupervisorSnapshot
	Reconcile()
}

type controllerRelayFactory func() (controllerRelayRunner, error)

type controllerRelayManagement interface {
	Status() controllerrelay.ManagementStatus
	StartEnrollment(context.Context, string, controllerrelay.ManagementEnrollmentInput) (controllerrelay.ManagementEnrollmentStart, error)
	PollEnrollment(context.Context, string, string) (controllerrelay.ManagementEnrollmentStatus, error)
	RemoveBinding(context.Context, string, string) (controllerrelay.ManagementBindingStatus, error)
	RotateKey(context.Context) (controllerrelay.ManagementKeyRotationStatus, error)
}

type controllerRelayManagedRunner interface {
	controllerRelayRunner
	controllerRelayManagementService() controllerRelayManagement
}

// controllerRelayRuntime retains the single dependency graph shared by relay
// lifecycle and management operations. These references are immutable after
// construction; the management target controls concurrent publication.
type controllerRelayRuntime struct {
	repository  *controllerrelay.Repository
	credentials *controllerrelay.FileCredentialStore
	client      *controllerrelay.RelayHTTPSClient
	enrollment  *controllerrelay.EnrollmentService
	controls    *controllerrelay.SessionControlService
	supervisor  *controllerrelay.Supervisor
	management  *controllerrelay.ManagementService
}

func (runtime *controllerRelayRuntime) Run(ctx context.Context) error {
	return runtime.supervisor.Run(ctx)
}

func (runtime *controllerRelayRuntime) Snapshot() controllerrelay.SupervisorSnapshot {
	if runtime == nil || runtime.supervisor == nil {
		return controllerrelay.SupervisorSnapshot{}
	}
	return runtime.supervisor.Snapshot()
}

func (runtime *controllerRelayRuntime) Reconcile() {
	if runtime != nil && runtime.supervisor != nil {
		runtime.supervisor.Reconcile()
	}
}

func (runtime *controllerRelayRuntime) controllerRelayManagementService() controllerRelayManagement {
	if runtime == nil {
		return nil
	}
	return runtime.management
}

// controllerRelayManagementTarget is the host-owned publication point used by
// lifecycle startup and, later, controller APIs. Reconcile calls made while
// asynchronous construction is pending are collapsed into one replay.
type controllerRelayManagementTarget struct {
	mu          sync.RWMutex
	runner      controllerRelayRunner
	runtime     *controllerRelayRuntime
	management  controllerRelayManagement
	pending     bool
	unavailable bool

	managementInFlight int
	managementDraining bool
	managementDrained  chan struct{}
}

func newControllerRelayManagementTarget() *controllerRelayManagementTarget {
	return &controllerRelayManagementTarget{}
}

func (target *controllerRelayManagementTarget) Reconcile() {
	if target == nil {
		return
	}
	target.mu.Lock()
	if target.unavailable {
		target.mu.Unlock()
		return
	}
	runner := target.runner
	if runner == nil {
		target.pending = true
		target.mu.Unlock()
		return
	}
	target.mu.Unlock()
	runner.Reconcile()
}

func (target *controllerRelayManagementTarget) install(runner controllerRelayRunner) bool {
	if target == nil || runner == nil {
		return false
	}
	target.mu.Lock()
	if target.runner != nil || target.unavailable {
		target.mu.Unlock()
		return false
	}
	target.runner = runner
	target.runtime, _ = runner.(*controllerRelayRuntime)
	if managed, ok := runner.(controllerRelayManagedRunner); ok {
		target.management = managed.controllerRelayManagementService()
	}
	pending := target.pending
	target.pending = false
	target.mu.Unlock()
	if pending {
		runner.Reconcile()
	}
	return true
}

func (target *controllerRelayManagementTarget) markUnavailable() {
	if target == nil {
		return
	}
	target.mu.Lock()
	if target.runner == nil {
		target.pending = false
		target.unavailable = true
	}
	target.mu.Unlock()
}

// markUnexpectedExit permanently unpublishes an installed relay graph. A
// stopped runner cannot safely serve management mutations, and this terminal
// state prevents a late runtime from replacing the failed graph.
func (target *controllerRelayManagementTarget) markUnexpectedExit() {
	if target == nil {
		return
	}
	target.mu.Lock()
	if target.runner == nil || target.unavailable {
		target.mu.Unlock()
		return
	}
	target.managementDraining = true
	if target.managementInFlight > 0 {
		if target.managementDrained == nil {
			target.managementDrained = make(chan struct{})
		}
		drained := target.managementDrained
		target.mu.Unlock()
		<-drained
		target.mu.Lock()
	}
	if target.runner != nil && target.managementDraining {
		target.runner = nil
		target.runtime = nil
		target.management = nil
		target.pending = false
		target.unavailable = true
	}
	target.managementDraining = false
	target.managementDrained = nil
	target.mu.Unlock()
}

func (target *controllerRelayManagementTarget) current() (*controllerRelayRuntime, bool) {
	if target == nil {
		return nil, false
	}
	target.mu.RLock()
	defer target.mu.RUnlock()
	return target.runtime, target.runtime != nil && !target.unavailable
}

func (target *controllerRelayManagementTarget) Status() controllerrelay.ManagementStatus {
	if target == nil {
		return controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementUnavailable, DiagnosticsUnavailable: true}
	}
	target.mu.RLock()
	management := target.management
	unavailable := target.unavailable
	draining := target.managementDraining
	target.mu.RUnlock()
	if unavailable || draining {
		return controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementUnavailable, DiagnosticsUnavailable: true}
	}
	if management == nil {
		return controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementInitializing, DiagnosticsUnavailable: true}
	}
	return management.Status()
}

func (target *controllerRelayManagementTarget) StartEnrollment(ctx context.Context, owner string, input controllerrelay.ManagementEnrollmentInput) (controllerrelay.ManagementEnrollmentStart, error) {
	management, done := target.beginManagementCall()
	if management == nil {
		return controllerrelay.ManagementEnrollmentStart{}, controllerRelayManagementUnavailable()
	}
	defer done()
	return management.StartEnrollment(ctx, owner, input)
}

func (target *controllerRelayManagementTarget) PollEnrollment(ctx context.Context, owner, enrollmentID string) (controllerrelay.ManagementEnrollmentStatus, error) {
	management, done := target.beginManagementCall()
	if management == nil {
		return controllerrelay.ManagementEnrollmentStatus{}, controllerRelayManagementUnavailable()
	}
	defer done()
	return management.PollEnrollment(ctx, owner, enrollmentID)
}

func (target *controllerRelayManagementTarget) RemoveBinding(ctx context.Context, owner, bindingID string) (controllerrelay.ManagementBindingStatus, error) {
	management, done := target.beginManagementCall()
	if management == nil {
		return controllerrelay.ManagementBindingStatus{}, controllerRelayManagementUnavailable()
	}
	defer done()
	return management.RemoveBinding(ctx, owner, bindingID)
}

func (target *controllerRelayManagementTarget) RotateKey(ctx context.Context) (controllerrelay.ManagementKeyRotationStatus, error) {
	management, done := target.beginManagementCall()
	if management == nil {
		return controllerrelay.ManagementKeyRotationStatus{}, controllerRelayManagementUnavailable()
	}
	defer done()
	return management.RotateKey(ctx)
}

func (target *controllerRelayManagementTarget) beginManagementCall() (controllerRelayManagement, func()) {
	if target == nil {
		return nil, nil
	}
	target.mu.Lock()
	if target.unavailable || target.managementDraining || target.management == nil {
		target.mu.Unlock()
		return nil, nil
	}
	management := target.management
	target.managementInFlight++
	target.mu.Unlock()

	var once sync.Once
	return management, func() {
		once.Do(func() {
			target.mu.Lock()
			target.managementInFlight--
			if target.managementDraining && target.managementInFlight == 0 && target.managementDrained != nil {
				drained := target.managementDrained
				target.managementDrained = nil
				close(drained)
			}
			target.mu.Unlock()
		})
	}
}

func controllerRelayManagementUnavailable() error {
	return &controllerrelay.ManagementError{Code: controllerrelay.ManagementErrorUnavailable}
}

// startControllerRelay is deliberately a no-op before invoking its factory
// unless the paired controller relay configuration enabled it.
func startControllerRelay(ctx context.Context, cfg config.Config, logger *slog.Logger, target *controllerRelayManagementTarget, factory controllerRelayFactory) <-chan struct{} {
	done := make(chan struct{})
	if !cfg.ControllerRelay {
		target.markUnavailable()
		close(done)
		return done
	}
	go func() {
		defer close(done)
		if factory == nil {
			target.markUnavailable()
			logger.Warn("controller relay unavailable", "outcome", "persistence_unavailable")
			return
		}
		runner, err := factory()
		if err != nil || runner == nil {
			// Construction must not make the primary host or manual deployments
			// unavailable. Never attach an arbitrary construction error because it
			// may include credential-provider or endpoint detail.
			target.markUnavailable()
			logger.Warn("controller relay unavailable", "outcome", "persistence_unavailable")
			return
		}
		if !target.install(runner) {
			target.markUnavailable()
			logger.Warn("controller relay unavailable", "outcome", "persistence_unavailable")
			return
		}
		runErr := runner.Run(ctx)
		if ctx.Err() == nil {
			target.markUnexpectedExit()
		}
		if runErr != nil {
			// Relay lifecycle outcomes are isolated from host lifetime and are
			// intentionally fixed-safe here; detailed observability comes only
			// from the safe Supervisor observer below.
			logger.Warn("controller relay stopped", "outcome", "persistence_unavailable")
		}
	}()
	return done
}

func waitForControllerRelay(done <-chan struct{}, timeout time.Duration, logger *slog.Logger) bool {
	if waitForWorker(done, timeout) {
		return true
	}
	logger.Warn("controller relay did not stop before shutdown timeout", "outcome", "persistence_unavailable")
	return false
}

func newControllerRelayRuntime(cfg config.Config, db *sql.DB, sources *sourceconnections.Service, logger *slog.Logger, wake ...func()) (*controllerRelayRuntime, error) {
	if !cfg.ControllerRelay || db == nil || sources == nil || logger == nil {
		return nil, errors.New("controller relay is unavailable")
	}
	repository := controllerrelay.NewRepository(db)
	var wakeCoordinator func()
	if len(wake) > 0 {
		wakeCoordinator = wake[0]
	}
	reconcileCoordinator := wakeCoordinator
	if len(wake) > 1 {
		reconcileCoordinator = wake[1]
	}
	store := &controllerRelayWakeStore{repository: repository, wake: wakeCoordinator}
	credentials, err := controllerrelay.NewFileCredentialStore(cfg.DataRoot)
	if err != nil {
		return nil, err
	}
	client, err := controllerrelay.NewRelayHTTPSClient(cfg.RelayOrigin, nil)
	if err != nil {
		return nil, err
	}
	enrollment, err := controllerrelay.NewEnrollmentService(repository, sources, credentials, client, time.Now, nil)
	if err != nil {
		return nil, err
	}
	controls, err := controllerrelay.NewSessionControlService(repository, credentials, controllerrelay.DefaultSessionControlConfig())
	if err != nil {
		return nil, err
	}
	transportConfig := controllerrelay.DefaultSessionTransportConfig()
	transportConfig.ControlHandler = controls
	transport, err := controllerrelay.NewSessionTransport(cfg.RelayOrigin, store, credentials, nil, nil, transportConfig)
	if err != nil {
		return nil, err
	}
	supervisorConfig := controllerrelay.DefaultSupervisorConfig()
	supervisorConfig.OnReady = reconcileCoordinator
	var supervisor *controllerrelay.Supervisor
	supervisorConfig.Observer = func(_ context.Context, event controllerrelay.SupervisorEvent) error {
		snapshot := controllerrelay.SupervisorSnapshot{}
		if supervisor != nil {
			snapshot = supervisor.Snapshot()
		}
		logControllerRelayEvent(logger, event, snapshot)
		return nil
	}
	supervisor, err = controllerrelay.NewSupervisor(transport, repository, controls, controls, supervisorConfig)
	if err != nil {
		return nil, err
	}
	management, err := controllerrelay.NewManagementService(repository, enrollment, controls, supervisor)
	if err != nil {
		return nil, err
	}
	return &controllerRelayRuntime{
		repository: repository, credentials: credentials, client: client,
		enrollment: enrollment, controls: controls, supervisor: supervisor, management: management,
	}, nil
}

// controllerRelayWakeStore preserves the complete durable/fenced session-store
// contract. A wake is emitted only after a durable source ACK decision returns;
// it is never emitted before the repository transaction commits.
type controllerRelayWakeStore struct {
	repository controllerRelaySessionStore
	wake       func()
}

type controllerRelaySessionStore interface {
	SessionAuthenticationCandidates(context.Context) (controllerrelay.ControllerIdentity, []controllerrelay.ControllerKey, error)
	DurableACKState(context.Context, string) ([]protocol.ACKState, error)
	PrepareSubscriptionSync(context.Context, string, string, time.Time) (controllerrelay.SyncSnapshot, error)
	AcknowledgeSubscriptionSync(context.Context, string, string, uint64, uint32, time.Time) error
	CommitSourceDesired(context.Context, string, protocol.SourceDesired, time.Time) (controllerrelay.InboxDecision, error)
	CommitAccessChange(context.Context, string, protocol.AccessChange, time.Time) (controllerrelay.InboxDecision, error)
	CommitSourceDesiredFenced(context.Context, string, uint64, uint64, protocol.SourceDesired, time.Time) (controllerrelay.InboxDecision, error)
	CommitAccessChangeFenced(context.Context, string, uint64, uint64, protocol.AccessChange, time.Time) (controllerrelay.InboxDecision, error)
}

func (store *controllerRelayWakeStore) SessionAuthenticationCandidates(ctx context.Context) (controllerrelay.ControllerIdentity, []controllerrelay.ControllerKey, error) {
	return store.repository.SessionAuthenticationCandidates(ctx)
}

func (store *controllerRelayWakeStore) DurableACKState(ctx context.Context, controllerID string) ([]protocol.ACKState, error) {
	return store.repository.DurableACKState(ctx, controllerID)
}

func (store *controllerRelayWakeStore) PrepareSubscriptionSync(ctx context.Context, controllerID, messageID string, at time.Time) (controllerrelay.SyncSnapshot, error) {
	return store.repository.PrepareSubscriptionSync(ctx, controllerID, messageID, at)
}

func (store *controllerRelayWakeStore) AcknowledgeSubscriptionSync(ctx context.Context, controllerID, messageID string, generation uint64, count uint32, at time.Time) error {
	return store.repository.AcknowledgeSubscriptionSync(ctx, controllerID, messageID, generation, count, at)
}

func (store *controllerRelayWakeStore) CommitSourceDesired(ctx context.Context, controllerID string, source protocol.SourceDesired, at time.Time) (controllerrelay.InboxDecision, error) {
	decision, err := store.repository.CommitSourceDesired(ctx, controllerID, source, at)
	store.wakeAfterACK(decision, err)
	return decision, err
}

func (store *controllerRelayWakeStore) CommitAccessChange(ctx context.Context, controllerID string, change protocol.AccessChange, at time.Time) (controllerrelay.InboxDecision, error) {
	return store.repository.CommitAccessChange(ctx, controllerID, change, at)
}

func (store *controllerRelayWakeStore) CommitSourceDesiredFenced(ctx context.Context, controllerID string, epoch, fence uint64, source protocol.SourceDesired, at time.Time) (controllerrelay.InboxDecision, error) {
	decision, err := store.repository.CommitSourceDesiredFenced(ctx, controllerID, epoch, fence, source, at)
	store.wakeAfterACK(decision, err)
	return decision, err
}

func (store *controllerRelayWakeStore) CommitAccessChangeFenced(ctx context.Context, controllerID string, epoch, fence uint64, change protocol.AccessChange, at time.Time) (controllerrelay.InboxDecision, error) {
	return store.repository.CommitAccessChangeFenced(ctx, controllerID, epoch, fence, change, at)
}

func (store *controllerRelayWakeStore) wakeAfterACK(decision controllerrelay.InboxDecision, err error) {
	if err == nil && decision.Kind == controllerrelay.DecisionAck && store.wake != nil {
		store.wake()
	}
}

func logControllerRelayEvent(logger *slog.Logger, event controllerrelay.SupervisorEvent, snapshot controllerrelay.SupervisorSnapshot) {
	logger.Info("controller relay lifecycle",
		"kind", safeRelayKind(event.Kind), "stage", safeRelayStage(event.Stage), "fallback", event.Fallback,
		"outcome", safeRelayOutcome(event.Outcome), "attempt", event.Attempt,
		"recovery_scanned", event.Recovery.Scanned, "recovery_cleaned", event.Recovery.Cleaned, "recovery_issues", event.Recovery.Issues,
		"recovery_lease_scanned", event.Recovery.LeaseScanned, "recovery_revoked_scanned", event.Recovery.RevokedScanned,
		"recovery_credential_scanned", event.Recovery.CredentialScanned, "recovery_temporary_scanned", event.Recovery.TemporaryScanned,
		"pending_commands", event.Diagnostics.PendingCommands, "oldest_pending_age", event.Diagnostics.OldestPendingAge,
		"active_leases", event.Diagnostics.ActiveLeases, "expired_leases", event.Diagnostics.ExpiredLeases,
		"observer_dropped", snapshot.ObserverDropped)
}

func safeRelayKind(value string) string {
	switch value {
	case controllerrelay.SupervisorHandshake, controllerrelay.SupervisorFallback, controllerrelay.SupervisorReady, controllerrelay.SupervisorDisconnect, controllerrelay.SupervisorBackoff, controllerrelay.SupervisorAttention, controllerrelay.SupervisorStopped, controllerrelay.SupervisorRecovery, controllerrelay.SupervisorDiagnostics:
		return value
	default:
		return "unknown"
	}
}

func safeRelayStage(value string) string {
	switch value {
	case controllerrelay.SessionTransportConnecting, controllerrelay.SessionTransportAuthenticating, controllerrelay.SessionTransportReady, controllerrelay.SessionBackoff, controllerrelay.SessionNeedsAttention, controllerrelay.SessionStopped:
		return value
	default:
		return "unknown"
	}
}

func safeRelayOutcome(value string) string {
	switch value {
	case "relay_unavailable", "connection_closed", "protocol_error", "persistence_unavailable", "credential_unavailable", "identity_unavailable", "queue_saturated", "session_expired":
		return value
	default:
		return "protocol_error"
	}
}
