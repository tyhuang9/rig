package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controllerrelay"
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

func (runner noncooperativeRelayRunner) Run(context.Context) error {
	close(runner.started)
	<-runner.release
	return nil
}
func (noncooperativeRelayRunner) Snapshot() controllerrelay.SupervisorSnapshot {
	return controllerrelay.SupervisorSnapshot{}
}

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

func TestControllerRelayDisabledDoesNotConstructOrRun(t *testing.T) {
	var calls int
	done := startControllerRelay(context.Background(), config.Defaults(), newStructuredLogger(&bytes.Buffer{}, "info"), func() (controllerRelayRunner, error) { calls++; return nil, nil })
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
	done := startControllerRelay(ctx, cfg, newStructuredLogger(&logs, "info"), func() (controllerRelayRunner, error) {
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
	done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (controllerRelayRunner, error) {
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
	done := startControllerRelay(context.Background(), cfg, newStructuredLogger(&logs, "info"), func() (controllerRelayRunner, error) {
		return nil, errors.New("https://relay.example secret-session-id")
	})
	if !waitForWorker(done, time.Second) {
		t.Fatal("failed construction did not finish")
	}
	if output := logs.String(); strings.Contains(output, "relay.example") || strings.Contains(output, "secret-session-id") || !strings.Contains(output, "persistence_unavailable") {
		t.Fatalf("unsafe construction log=%q", output)
	}
}

func TestControllerRelayShutdownDrainAndTimeout(t *testing.T) {
	cfg := config.Defaults()
	cfg.ControllerRelay = true
	cfg.RelayOrigin = "https://relay.example"
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	done := startControllerRelay(ctx, cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (controllerRelayRunner, error) { return relayRunnerFake{started: started, release: release}, nil })
	<-started
	cancel()
	if !waitForWorker(done, time.Second) {
		t.Fatal("canceled relay did not drain")
	}
	stuckContext, stopStuck := context.WithCancel(context.Background())
	stuckStarted, releaseStuck := make(chan struct{}), make(chan struct{})
	var timeoutLogs bytes.Buffer
	stuck := startControllerRelay(stuckContext, cfg, newStructuredLogger(&timeoutLogs, "info"), func() (controllerRelayRunner, error) {
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
