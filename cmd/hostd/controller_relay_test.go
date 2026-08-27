package main

import (
	"bytes"
	"context"
	"errors"
	"runtime"
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

type controllerRelayManagementFake struct {
	mu      sync.Mutex
	status  controllerrelay.ManagementStatus
	calls   [5]int
	entered [5]chan<- struct{}
	blocked [5]<-chan struct{}
}

func (management *controllerRelayManagementFake) Status() controllerrelay.ManagementStatus {
	management.mu.Lock()
	defer management.mu.Unlock()
	return management.status
}
func (management *controllerRelayManagementFake) ReadModel(context.Context, string) (controllerrelay.ManagementReadModel, error) {
	management.recordCall(4)
	return controllerrelay.ManagementReadModel{RemovableBindings: make([]controllerrelay.ManagementBindingSummary, 0)}, nil
}
func (management *controllerRelayManagementFake) StartEnrollment(context.Context, string, controllerrelay.ManagementEnrollmentInput) (controllerrelay.ManagementEnrollmentStart, error) {
	management.recordCall(0)
	return controllerrelay.ManagementEnrollmentStart{}, nil
}
func (management *controllerRelayManagementFake) PollEnrollment(context.Context, string, string) (controllerrelay.ManagementEnrollmentStatus, error) {
	management.recordCall(1)
	return controllerrelay.ManagementEnrollmentStatus{}, nil
}
func (management *controllerRelayManagementFake) RemoveBinding(context.Context, string, string) (controllerrelay.ManagementBindingStatus, error) {
	management.recordCall(2)
	return controllerrelay.ManagementBindingStatus{}, nil
}
func (management *controllerRelayManagementFake) RotateKey(context.Context) (controllerrelay.ManagementKeyRotationStatus, error) {
	management.recordCall(3)
	return controllerrelay.ManagementKeyRotationStatus{}, nil
}
func (management *controllerRelayManagementFake) recordCall(index int) {
	management.mu.Lock()
	management.calls[index]++
	entered := management.entered[index]
	blocked := management.blocked[index]
	management.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if blocked != nil {
		<-blocked
	}
}
func (management *controllerRelayManagementFake) callCounts() [5]int {
	management.mu.Lock()
	defer management.mu.Unlock()
	return management.calls
}

type controllerRelayManagedRunnerFake struct {
	*managedRelayRunnerFake
	management controllerRelayManagement
}

type terminatingManagedRelayRunnerFake struct {
	management controllerRelayManagement
	started    chan struct{}
	release    <-chan struct{}
	exitErr    error
	cancelErr  error
}

func (runner *terminatingManagedRelayRunnerFake) Run(ctx context.Context) error {
	close(runner.started)
	select {
	case <-runner.release:
		return runner.exitErr
	case <-ctx.Done():
		return runner.cancelErr
	}
}
func (*terminatingManagedRelayRunnerFake) Snapshot() controllerrelay.SupervisorSnapshot {
	return controllerrelay.SupervisorSnapshot{}
}
func (*terminatingManagedRelayRunnerFake) Reconcile() {}
func (runner *terminatingManagedRelayRunnerFake) controllerRelayManagementService() controllerRelayManagement {
	return runner.management
}

func (runner *controllerRelayManagedRunnerFake) controllerRelayManagementService() controllerRelayManagement {
	return runner.management
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
	target := newControllerRelayManagementTarget()
	done := startControllerRelay(context.Background(), config.Defaults(), newStructuredLogger(&bytes.Buffer{}, "info"), target, func() (controllerRelayRunner, error) { calls++; return nil, nil })
	if calls != 0 {
		t.Fatalf("disabled relay factory calls=%d", calls)
	}
	if !waitForWorker(done, time.Second) {
		t.Fatal("disabled relay did not finish")
	}
	if status := target.Status(); status.Availability != controllerrelay.ManagementUnavailable || !status.DiagnosticsUnavailable {
		t.Fatalf("disabled relay status=%#v", status)
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
	target := newControllerRelayManagementTarget()
	done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&logs, "info"), target, func() (controllerRelayRunner, error) {
		return nil, errors.New("https://relay.example secret-session-id")
	})
	if !waitForWorker(done, time.Second) {
		t.Fatal("failed construction did not finish")
	}
	if output := logs.String(); strings.Contains(output, "relay.example") || strings.Contains(output, "secret-session-id") || !strings.Contains(output, "persistence_unavailable") {
		t.Fatalf("unsafe construction log=%q", output)
	}
	if status := target.Status(); status.Availability != controllerrelay.ManagementUnavailable || !status.DiagnosticsUnavailable {
		t.Fatalf("construction failure status=%#v", status)
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

func TestControllerRelayManagementTargetLifecycleStatusAndUnavailableMethods(t *testing.T) {
	target := newControllerRelayManagementTarget()
	if status := target.Status(); status.Availability != controllerrelay.ManagementInitializing || !status.DiagnosticsUnavailable {
		t.Fatalf("initial status=%#v", status)
	}
	ctx := context.Background()
	assertUnavailable := func(err error) {
		t.Helper()
		if !controllerrelay.IsManagementCode(err, controllerrelay.ManagementErrorUnavailable) {
			t.Fatalf("management error=%v", err)
		}
	}
	readModel, err := target.ReadModel(ctx, "owner")
	assertUnavailable(err)
	if readModel.RemovableBindings == nil || len(readModel.RemovableBindings) != 0 {
		t.Fatalf("initializing read model=%#v", readModel)
	}
	_, err = target.StartEnrollment(ctx, "owner", controllerrelay.ManagementEnrollmentInput{})
	assertUnavailable(err)
	_, err = target.PollEnrollment(ctx, "owner", "enrollment")
	assertUnavailable(err)
	_, err = target.RemoveBinding(ctx, "owner", "binding")
	assertUnavailable(err)
	_, err = target.RotateKey(ctx)
	assertUnavailable(err)

	target.markUnavailable()
	if status := target.Status(); status.Availability != controllerrelay.ManagementUnavailable || !status.DiagnosticsUnavailable {
		t.Fatalf("unavailable status=%#v", status)
	}
	_, err = target.RotateKey(ctx)
	assertUnavailable(err)
	readModel, err = target.ReadModel(ctx, "owner")
	assertUnavailable(err)
	if readModel.RemovableBindings == nil || len(readModel.RemovableBindings) != 0 {
		t.Fatalf("unavailable read model=%#v", readModel)
	}
}

func TestControllerRelayManagementTargetUnexpectedExitIsTerminalButCancellationIsClean(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"

	for name, exitErr := range map[string]error{"return": nil, "error": errors.New("secret runtime failure")} {
		t.Run("unexpected_"+name, func(t *testing.T) {
			target := newControllerRelayManagementTarget()
			management := &controllerRelayManagementFake{status: controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementAvailable, State: controllerrelay.SessionReady}}
			started, release := make(chan struct{}), make(chan struct{})
			runner := &terminatingManagedRelayRunnerFake{management: management, started: started, release: release, exitErr: exitErr}
			done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), target, func() (controllerRelayRunner, error) { return runner, nil })
			<-started
			if status := target.Status(); status.Availability != controllerrelay.ManagementAvailable || status.DiagnosticsUnavailable {
				t.Fatalf("installed status=%#v", status)
			}
			close(release)
			if !waitForWorker(done, time.Second) {
				t.Fatal("unexpected relay exit did not drain")
			}
			if status := target.Status(); status.Availability != controllerrelay.ManagementUnavailable || !status.DiagnosticsUnavailable {
				t.Fatalf("unexpected exit status=%#v", status)
			}
			assertUnavailable := func(err error) {
				t.Helper()
				if !controllerrelay.IsManagementCode(err, controllerrelay.ManagementErrorUnavailable) {
					t.Fatalf("post-exit management error=%v", err)
				}
			}
			_, err := target.StartEnrollment(context.Background(), "owner", controllerrelay.ManagementEnrollmentInput{})
			assertUnavailable(err)
			_, err = target.PollEnrollment(context.Background(), "owner", "enrollment")
			assertUnavailable(err)
			_, err = target.RemoveBinding(context.Background(), "owner", "binding")
			assertUnavailable(err)
			_, err = target.RotateKey(context.Background())
			assertUnavailable(err)
			if calls := management.callCounts(); calls != [5]int{} {
				t.Fatalf("post-exit mutation reached management: %v", calls)
			}
			if target.install(&controllerRelayManagedRunnerFake{managedRelayRunnerFake: &managedRelayRunnerFake{}, management: management}) {
				t.Fatal("terminal target accepted a replacement runtime")
			}
		})
	}

	for name, cancelErr := range map[string]error{"nil_return": nil, "canceled_error": context.Canceled} {
		t.Run("clean_"+name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			target := newControllerRelayManagementTarget()
			management := &controllerRelayManagementFake{status: controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementAvailable, State: controllerrelay.SessionStopped}}
			started, release := make(chan struct{}), make(chan struct{})
			runner := &terminatingManagedRelayRunnerFake{management: management, started: started, release: release, cancelErr: cancelErr}
			done := startControllerRelay(ctx, cfg, newStructuredLogger(&bytes.Buffer{}, "info"), target, func() (controllerRelayRunner, error) { return runner, nil })
			<-started
			cancel()
			if !waitForWorker(done, time.Second) {
				t.Fatal("clean relay shutdown did not drain")
			}
			if status := target.Status(); status.Availability != controllerrelay.ManagementAvailable || status.DiagnosticsUnavailable {
				t.Fatalf("clean shutdown status=%#v", status)
			}
			if _, err := target.RotateKey(context.Background()); err != nil {
				t.Fatalf("clean shutdown discarded supervisor-owned management: %v", err)
			}
			if calls := management.callCounts(); calls[3] != 1 {
				t.Fatalf("clean shutdown management calls=%v", calls)
			}
		})
	}
}

func TestControllerRelayManagementTargetDrainsAdmittedMutationsBeforeTerminalBoundary(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	tests := []struct {
		name   string
		index  int
		invoke func(*controllerRelayManagementTarget) error
	}{
		{name: "read_model", index: 4, invoke: func(target *controllerRelayManagementTarget) error {
			_, err := target.ReadModel(context.Background(), "owner")
			return err
		}},
		{name: "start_enrollment", index: 0, invoke: func(target *controllerRelayManagementTarget) error {
			_, err := target.StartEnrollment(context.Background(), "owner", controllerrelay.ManagementEnrollmentInput{})
			return err
		}},
		{name: "poll_enrollment", index: 1, invoke: func(target *controllerRelayManagementTarget) error {
			_, err := target.PollEnrollment(context.Background(), "owner", "enrollment")
			return err
		}},
		{name: "remove_binding", index: 2, invoke: func(target *controllerRelayManagementTarget) error {
			_, err := target.RemoveBinding(context.Background(), "owner", "binding")
			return err
		}},
		{name: "rotate_key", index: 3, invoke: func(target *controllerRelayManagementTarget) error {
			_, err := target.RotateKey(context.Background())
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := newControllerRelayManagementTarget()
			entered := make(chan struct{}, 1)
			unblock := make(chan struct{})
			management := &controllerRelayManagementFake{status: controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementAvailable}}
			management.entered[test.index] = entered
			management.blocked[test.index] = unblock
			runnerStarted, runnerExit := make(chan struct{}), make(chan struct{})
			runner := &terminatingManagedRelayRunnerFake{management: management, started: runnerStarted, release: runnerExit}
			done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), target, func() (controllerRelayRunner, error) { return runner, nil })
			<-runnerStarted

			callDone := make(chan error, 1)
			go func() { callDone <- test.invoke(target) }()
			<-entered
			close(runnerExit)
			if !waitForManagementDrain(target, time.Second) {
				t.Fatal("unexpected exit did not begin management drain")
			}
			if status := target.Status(); status.Availability != controllerrelay.ManagementUnavailable || !status.DiagnosticsUnavailable {
				t.Fatalf("draining status=%#v", status)
			}
			select {
			case <-done:
				t.Fatal("terminal boundary crossed before admitted mutation drained")
			default:
			}
			if err := test.invoke(target); !controllerrelay.IsManagementCode(err, controllerrelay.ManagementErrorUnavailable) {
				t.Fatalf("mutation admitted during drain: %v", err)
			}
			if calls := management.callCounts(); calls[test.index] != 1 {
				t.Fatalf("management calls during drain=%v", calls)
			}

			close(unblock)
			if err := <-callDone; err != nil {
				t.Fatalf("pre-boundary admitted mutation failed: %v", err)
			}
			if !waitForWorker(done, time.Second) {
				t.Fatal("unexpected exit did not finish after management drain")
			}
			if status := target.Status(); status.Availability != controllerrelay.ManagementUnavailable || !status.DiagnosticsUnavailable {
				t.Fatalf("terminal status=%#v", status)
			}
			if err := test.invoke(target); !controllerrelay.IsManagementCode(err, controllerrelay.ManagementErrorUnavailable) {
				t.Fatalf("post-boundary mutation result=%v", err)
			}
		})
	}
}

func waitForManagementDrain(target *controllerRelayManagementTarget, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		target.mu.RLock()
		draining := target.managementDraining
		target.mu.RUnlock()
		if draining {
			return true
		}
		runtime.Gosched()
	}
	return false
}

func TestControllerRelayManagementTargetConcurrentPublicationAndMutations(t *testing.T) {
	target := newControllerRelayManagementTarget()
	management := &controllerRelayManagementFake{status: controllerrelay.ManagementStatus{Availability: controllerrelay.ManagementAvailable, State: controllerrelay.SessionReady}}
	runner := &controllerRelayManagedRunnerFake{managedRelayRunnerFake: &managedRelayRunnerFake{}, management: management}
	const callers = 400
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		index := index
		go func() {
			defer wait.Done()
			<-start
			for target.Status().Availability != controllerrelay.ManagementAvailable {
				runtime.Gosched()
			}
			switch index % 4 {
			case 0:
				_, _ = target.StartEnrollment(context.Background(), "owner", controllerrelay.ManagementEnrollmentInput{})
			case 1:
				_, _ = target.PollEnrollment(context.Background(), "owner", "enrollment")
			case 2:
				_, _ = target.RemoveBinding(context.Background(), "owner", "binding")
			case 3:
				_, _ = target.RotateKey(context.Background())
			}
		}()
	}
	close(start)
	if !target.install(runner) {
		t.Fatal("concurrent target publication failed")
	}
	wait.Wait()
	if status := target.Status(); status.Availability != controllerrelay.ManagementAvailable || status.State != controllerrelay.SessionReady {
		t.Fatalf("published status=%#v", status)
	}
	if got := management.callCounts(); got != [5]int{callers / 4, callers / 4, callers / 4, callers / 4, 0} {
		t.Fatalf("management calls=%v", got)
	}
	if runtime, available := target.current(); available || runtime != nil {
		t.Fatalf("fake runner exposed runtime=%p available=%t", runtime, available)
	}
}

func TestNewControllerRelayRuntimeRetainsOneSharedManagementGraph(t *testing.T) {
	cfg := config.Defaults()
	cfg.DataRoot = t.TempDir()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	cfg.GitHubClientID = "client"
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
	if runtime.repository == nil || runtime.credentials == nil || runtime.client == nil || runtime.enrollment == nil || runtime.controls == nil || runtime.supervisor == nil || runtime.management == nil {
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
	if status := target.Status(); status.Availability != controllerrelay.ManagementAvailable {
		t.Fatalf("published management status=%#v", status)
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
