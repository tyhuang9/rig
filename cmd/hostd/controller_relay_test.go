package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controllerrelay"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/sourceconnections"
)

type relayRunnerFake struct {
	started chan struct{}
	release <-chan struct{}
	err     error
}

type noncooperativeRelayRunner struct {
	started chan struct{}
	release <-chan struct{}
}

type managedRelayRunnerFake struct {
	mu         sync.Mutex
	reconciles int
}

type firstRunAwareRelayRunner struct {
	mu              sync.Mutex
	started         bool
	preRunRequested bool
	reconcileCalls  int
	firstRunApplied int
	lateReconciles  int
}

func (runner *firstRunAwareRelayRunner) Run(context.Context) error {
	runner.mu.Lock()
	runner.started = true
	if runner.preRunRequested {
		runner.firstRunApplied++
		runner.preRunRequested = false
	}
	runner.mu.Unlock()
	return nil
}
func (*firstRunAwareRelayRunner) Snapshot() controllerrelay.SupervisorSnapshot {
	return controllerrelay.SupervisorSnapshot{}
}
func (runner *firstRunAwareRelayRunner) Reconcile() {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.reconcileCalls++
	if runner.started {
		runner.lateReconciles++
		return
	}
	runner.preRunRequested = true
}
func (runner *firstRunAwareRelayRunner) outcome() (calls, applied, late int) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.reconcileCalls, runner.firstRunApplied, runner.lateReconciles
}

func (*managedRelayRunnerFake) Run(context.Context) error { return nil }
func (*managedRelayRunnerFake) Snapshot() controllerrelay.SupervisorSnapshot {
	return controllerrelay.SupervisorSnapshot{}
}
func (runner *managedRelayRunnerFake) Reconcile() {
	runner.mu.Lock()
	runner.reconciles++
	runner.mu.Unlock()
}
func (runner *managedRelayRunnerFake) reconcileCount() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.reconciles
}

func (runner noncooperativeRelayRunner) Run(context.Context) error {
	close(runner.started)
	<-runner.release
	return nil
}
func (noncooperativeRelayRunner) Snapshot() controllerrelay.SupervisorSnapshot {
	return controllerrelay.SupervisorSnapshot{}
}
func (noncooperativeRelayRunner) Reconcile() {}

func (runner relayRunnerFake) Run(ctx context.Context) error {
	close(runner.started)
	if runner.release != nil {
		select {
		case <-runner.release:
			return runner.err
		case <-ctx.Done():
			return runner.err
		}
	}
	return runner.err
}
func (relayRunnerFake) Snapshot() controllerrelay.SupervisorSnapshot {
	return controllerrelay.SupervisorSnapshot{}
}
func (relayRunnerFake) Reconcile() {}

func TestControllerRelayDisabledDoesNotConstructOrRun(t *testing.T) {
	var calls int
	done := startControllerRelay(context.Background(), config.Defaults(), newStructuredLogger(&bytes.Buffer{}, "info"), newControllerRelayManagementTarget(), func() (controllerRelayRunner, error) { calls++; return nil, nil })
	if calls != 0 {
		t.Fatalf("disabled relay factory calls=%d", calls)
	}
	if !waitForWorker(done, time.Second) {
		t.Fatal("disabled relay did not finish")
	}
}

func TestControllerRelayEnabledStartsWithoutBlockingAndDoesNotCancelHost(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	calls := 0
	const runSecret = "relay-run-secret-session"
	var logs bytes.Buffer
	done := startControllerRelay(ctx, cfg, newStructuredLogger(&logs, "info"), newControllerRelayManagementTarget(), func() (controllerRelayRunner, error) {
		calls++
		return relayRunnerFake{started: started, release: release, err: errors.New(runSecret)}, nil
	})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("relay startup blocked host")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("relay did not start")
	}
	if calls != 1 {
		t.Fatalf("enabled relay factory calls=%d", calls)
	}
	close(release)
	if !waitForWorker(done, time.Second) {
		t.Fatal("relay completion did not drain")
	}
	if ctx.Err() != nil {
		t.Fatalf("relay completion canceled host: %v", ctx.Err())
	}
	if output := logs.String(); strings.Contains(output, runSecret) || !strings.Contains(output, "persistence_unavailable") {
		t.Fatalf("unsafe run failure log=%q", output)
	}
}

func TestControllerRelaySlowFactoryDoesNotBlockHost(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	factoryStarted, releaseFactory := make(chan struct{}), make(chan struct{})
	runnerStarted := make(chan struct{})
	start := time.Now()
	done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), newControllerRelayManagementTarget(), func() (controllerRelayRunner, error) {
		close(factoryStarted)
		<-releaseFactory
		return relayRunnerFake{started: runnerStarted}, nil
	})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("slow relay factory blocked host startup")
	}
	select {
	case <-factoryStarted:
	case <-time.After(time.Second):
		t.Fatal("factory did not begin asynchronously")
	}
	close(releaseFactory)
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("runner did not start after factory release")
	}
	if !waitForWorker(done, time.Second) {
		t.Fatal("slow factory relay did not drain")
	}
}

func TestControllerRelayConstructionFailureIsSafeAndNonfatal(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	var logs bytes.Buffer
	done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&logs, "info"), newControllerRelayManagementTarget(), func() (controllerRelayRunner, error) {
		return nil, errors.New("https://relay.example secret-session-id")
	})
	if !waitForWorker(done, time.Second) {
		t.Fatal("failed construction did not finish")
	}
	if output := logs.String(); strings.Contains(output, "relay.example") || strings.Contains(output, "secret-session-id") || !strings.Contains(output, "persistence_unavailable") {
		t.Fatalf("unsafe construction log=%q", output)
	}
}

func TestControllerRelayManagementTargetCoalescesBeforeConstructionAndPublishesOnce(t *testing.T) {
	target := newControllerRelayManagementTarget()
	const callers = 1000
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			target.Reconcile()
		}()
	}
	wait.Wait()
	runner := &managedRelayRunnerFake{}
	if !target.install(runner) {
		t.Fatal("target rejected first runtime")
	}
	if got := runner.reconcileCount(); got != 1 {
		t.Fatalf("pre-construction reconciles replayed=%d want 1", got)
	}
	if target.install(&managedRelayRunnerFake{}) {
		t.Fatal("target replaced its retained runtime")
	}
	target.Reconcile()
	if got := runner.reconcileCount(); got != 2 {
		t.Fatalf("published reconcile calls=%d want 2", got)
	}
}

func TestControllerRelayManagementTargetReplaysOnlyAfterAsyncConstruction(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	target := newControllerRelayManagementTarget()
	runner := &firstRunAwareRelayRunner{}
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), target, func() (controllerRelayRunner, error) {
		close(factoryEntered)
		<-releaseFactory
		return runner, nil
	})
	<-factoryEntered
	for index := 0; index < 1000; index++ {
		target.Reconcile()
	}
	if calls, applied, late := runner.outcome(); calls != 0 || applied != 0 || late != 0 {
		t.Fatalf("runtime action preceded successful construction: calls=%d applied=%d late=%d", calls, applied, late)
	}
	close(releaseFactory)
	if !waitForWorker(done, time.Second) {
		t.Fatal("constructed relay did not finish")
	}
	if calls, applied, late := runner.outcome(); calls != 1 || applied != 1 || late != 0 {
		t.Fatalf("async first-run replay calls=%d applied=%d late=%d want 1,1,0", calls, applied, late)
	}
}

func TestControllerRelayManagementTargetRemainsUnavailableAfterConstructionFailure(t *testing.T) {
	target := newControllerRelayManagementTarget()
	target.Reconcile()
	target.markUnavailable()
	runner := &managedRelayRunnerFake{}
	if target.install(runner) {
		t.Fatal("unavailable target accepted a late runtime")
	}
	target.Reconcile()
	if got := runner.reconcileCount(); got != 0 {
		t.Fatalf("unavailable target invoked runtime %d times", got)
	}
	if runtime, available := target.current(); available || runtime != nil {
		t.Fatalf("failed target current=%p available=%t", runtime, available)
	}
}

func TestNewControllerRelayRuntimeRetainsOneSharedManagementGraph(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataRoot = t.TempDir()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	if err := cfg.EnsureDataRoot(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(cfg.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), nil, sourceconnections.NewFileCredentialStore(cfg.DataRoot), "", time.Now)
	runtime, err := newControllerRelayRuntime(cfg, db, sources, newStructuredLogger(&bytes.Buffer{}, "info"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.repository == nil || runtime.credentials == nil || runtime.client == nil || runtime.enrollment == nil || runtime.controls == nil || runtime.supervisor == nil {
		t.Fatalf("incomplete retained runtime: %#v", runtime)
	}
	target := newControllerRelayManagementTarget()
	if !target.install(runtime) {
		t.Fatal("runtime publication failed")
	}
	got, available := target.current()
	if !available || got != runtime {
		t.Fatalf("published runtime=%p available=%t want %p", got, available, runtime)
	}
}

func TestControllerRelayShutdownDrainAndTimeout(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	done := startControllerRelay(ctx, cfg, newStructuredLogger(&bytes.Buffer{}, "info"), newControllerRelayManagementTarget(), func() (controllerRelayRunner, error) { return relayRunnerFake{started: started, release: release}, nil })
	<-started
	cancel()
	if !waitForWorker(done, time.Second) {
		t.Fatal("canceled relay did not drain")
	}
	stuckContext, stopStuck := context.WithCancel(context.Background())
	stuckStarted, releaseStuck := make(chan struct{}), make(chan struct{})
	var timeoutLogs bytes.Buffer
	stuck := startControllerRelay(stuckContext, cfg, newStructuredLogger(&timeoutLogs, "info"), newControllerRelayManagementTarget(), func() (controllerRelayRunner, error) {
		return noncooperativeRelayRunner{started: stuckStarted, release: releaseStuck}, nil
	})
	<-stuckStarted
	stopStuck()
	if waitForControllerRelay(stuck, time.Millisecond, newStructuredLogger(&timeoutLogs, "info")) {
		t.Fatal("stuck relay unexpectedly drained")
	}
	if output := timeoutLogs.String(); !strings.Contains(output, "persistence_unavailable") || strings.Contains(output, "relay.example") {
		t.Fatalf("unsafe timeout log=%q", output)
	}
	close(releaseStuck)
	if !waitForWorker(stuck, time.Second) {
		t.Fatal("noncooperative relay did not drain after release")
	}
}

func TestControllerRelayObserverLogIsAggregateAndSafe(t *testing.T) {
	const secret = "controller=secret session=secret origin=https://relay.example"
	var logs bytes.Buffer
	logger := newStructuredLogger(&logs, "info")
	logControllerRelayEvent(logger, controllerrelay.SupervisorEvent{Kind: controllerrelay.SupervisorDisconnect, Stage: controllerrelay.SessionNeedsAttention, Outcome: "queue_saturated", Attempt: 2, Recovery: controllerrelay.SupervisorRecoverySummary{Scanned: 4, LeaseScanned: 2}, Diagnostics: controllerrelay.SessionLifecycleDiagnostics{PendingCommands: 3, OldestPendingAge: time.Second, ActiveLeases: 1, ExpiredLeases: 2}}, controllerrelay.SupervisorSnapshot{ObserverDropped: 5})
	logControllerRelayEvent(logger, controllerrelay.SupervisorEvent{Kind: secret, Stage: secret, Outcome: secret}, controllerrelay.SupervisorSnapshot{})
	output := logs.String()
	if strings.Contains(output, secret) || strings.Contains(output, "relay.example") || !strings.Contains(output, "queue_saturated") || !strings.Contains(output, "observer_dropped") || !strings.Contains(output, "recovery_lease_scanned") || !strings.Contains(output, "pending_commands") {
		t.Fatalf("unsafe/incomplete relay observer log=%q", output)
	}
}

type relayWakeStoreFake struct {
	committed bool
	decision  controllerrelay.InboxDecision
	err       error
}

func (*relayWakeStoreFake) SessionAuthenticationCandidates(context.Context) (controllerrelay.ControllerIdentity, []controllerrelay.ControllerKey, error) {
	return controllerrelay.ControllerIdentity{}, nil, nil
}
func (*relayWakeStoreFake) DurableACKState(context.Context, string) ([]protocol.ACKState, error) {
	return nil, nil
}
func (*relayWakeStoreFake) PrepareSubscriptionSync(context.Context, string, string, time.Time) (controllerrelay.SyncSnapshot, error) {
	return controllerrelay.SyncSnapshot{}, nil
}
func (*relayWakeStoreFake) AcknowledgeSubscriptionSync(context.Context, string, string, uint64, uint32, time.Time) error {
	return nil
}
func (store *relayWakeStoreFake) CommitSourceDesired(context.Context, string, protocol.SourceDesired, time.Time) (controllerrelay.InboxDecision, error) {
	store.committed = store.err == nil
	return store.decision, store.err
}
func (*relayWakeStoreFake) CommitAccessChange(context.Context, string, protocol.AccessChange, time.Time) (controllerrelay.InboxDecision, error) {
	return controllerrelay.InboxDecision{}, nil
}
func (store *relayWakeStoreFake) CommitSourceDesiredFenced(context.Context, string, uint64, uint64, protocol.SourceDesired, time.Time) (controllerrelay.InboxDecision, error) {
	store.committed = store.err == nil
	return store.decision, store.err
}
func (*relayWakeStoreFake) CommitAccessChangeFenced(context.Context, string, uint64, uint64, protocol.AccessChange, time.Time) (controllerrelay.InboxDecision, error) {
	return controllerrelay.InboxDecision{}, nil
}

func TestControllerRelayWakeStoreSignalsOnlyAfterDurableACK(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		store := &relayWakeStoreFake{decision: controllerrelay.AckDecision()}
		wakes := 0
		wrapper := &controllerRelayWakeStore{repository: store, wake: func() {
			if !store.committed {
				t.Fatal("wake preceded durable commit return")
			}
			wakes++
		}}
		if fenced {
			if _, err := wrapper.CommitSourceDesiredFenced(context.Background(), "controller", 1, 1, protocol.SourceDesired{}, time.Now()); err != nil {
				t.Fatal(err)
			}
		} else if _, err := wrapper.CommitSourceDesired(context.Background(), "controller", protocol.SourceDesired{}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if wakes != 1 {
			t.Fatalf("fenced=%v wakes=%d", fenced, wakes)
		}
	}

	for _, store := range []*relayWakeStoreFake{
		{decision: controllerrelay.RejectDecision(controllerrelay.RejectInvalidEvent)},
		{decision: controllerrelay.AckDecision(), err: errors.New("persistence failed")},
	} {
		wakes := 0
		wrapper := &controllerRelayWakeStore{repository: store, wake: func() { wakes++ }}
		_, _ = wrapper.CommitSourceDesired(context.Background(), "controller", protocol.SourceDesired{}, time.Now())
		if wakes != 0 {
			t.Fatalf("non-durable/non-ACK source wakes=%d", wakes)
		}
	}
}
