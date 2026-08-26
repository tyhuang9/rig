package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/autodeploy"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/sourceconnections"
)

type autoDeployRunnerFake struct {
	started    chan struct{}
	release    <-chan struct{}
	err        error
	mutex      sync.Mutex
	wakes      int
	reconciles int
}

func (runner *autoDeployRunnerFake) Run(ctx context.Context) error {
	if runner.started != nil {
		close(runner.started)
	}
	if runner.release != nil {
		select {
		case <-runner.release:
			return runner.err
		case <-ctx.Done():
			return nil
		}
	}
	return runner.err
}

func (runner *autoDeployRunnerFake) Wake() {
	runner.mutex.Lock()
	runner.wakes++
	runner.mutex.Unlock()
}

func (runner *autoDeployRunnerFake) Reconcile() {
	runner.mutex.Lock()
	runner.reconciles++
	runner.mutex.Unlock()
}

func (runner *autoDeployRunnerFake) wakeCount() int {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	return runner.wakes
}

func TestAutoDeployDefaultOffDoesNotConstruct(t *testing.T) {
	for _, cfg := range []config.Config{
		config.Defaults(),
		func() config.Config { value := config.Defaults(); value.ComposeRuntime = true; return value }(),
		func() config.Config {
			value := config.Defaults()
			value.GitHubClientID, value.GitHubAppSlug = "client", "app"
			return value
		}(),
		func() config.Config {
			value := config.Defaults()
			value.FakeRuntime = true
			value.GitHubClientID, value.GitHubAppSlug = "client", "app"
			return value
		}(),
	} {
		calls := 0
		done, wake, reconcile := startAutoDeploy(context.Background(), cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (autoDeployRunner, error) {
			calls++
			return nil, nil
		})
		wake()
		reconcile()
		if calls != 0 || !waitForWorker(done, time.Second) {
			t.Fatalf("default-off factory calls=%d cfg=%#v", calls, cfg)
		}
	}
}

func TestAutoDeployEnabledIsAsyncIndependentAndForwardsPendingWake(t *testing.T) {
	cfg := enabledAutoDeployConfig()
	factoryStarted, releaseFactory := make(chan struct{}), make(chan struct{})
	runnerStarted, releaseRunner := make(chan struct{}), make(chan struct{})
	runner := &autoDeployRunnerFake{started: runnerStarted, release: releaseRunner}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	done, wake, reconcile := startAutoDeploy(ctx, cfg, newStructuredLogger(&bytes.Buffer{}, "info"), func() (autoDeployRunner, error) {
		close(factoryStarted)
		<-releaseFactory
		return runner, nil
	})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("auto-deploy factory blocked host startup")
	}
	<-factoryStarted
	for index := 0; index < 100; index++ {
		wake()
	}
	reconcile()
	close(releaseFactory)
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("auto-deploy runner did not start")
	}
	if runner.wakeCount() != 0 || runner.reconciles != 1 {
		t.Fatalf("pending wakes=%d reconciles=%d", runner.wakeCount(), runner.reconciles)
	}
	close(releaseRunner)
	if !waitForWorker(done, time.Second) || ctx.Err() != nil {
		t.Fatalf("coordinator completion affected host ctx=%v", ctx.Err())
	}
}

func TestAutoDeployConstructionAndRunFailuresAreSanitized(t *testing.T) {
	const secret = "token=secret repository=private sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, factory := range []autoDeployFactory{
		func() (autoDeployRunner, error) { return nil, errors.New(secret) },
		func() (autoDeployRunner, error) { return &autoDeployRunnerFake{err: errors.New(secret)}, nil },
	} {
		var logs bytes.Buffer
		done, _, _ := startAutoDeploy(context.Background(), enabledAutoDeployConfig(), newStructuredLogger(&logs, "info"), factory)
		if !waitForWorker(done, time.Second) {
			t.Fatal("failure path did not drain")
		}
		output := logs.String()
		if strings.Contains(output, secret) || strings.Contains(output, "private") || strings.Contains(output, "aaaaaaaa") || !strings.Contains(output, autodeploy.OutcomePersistenceUnavailable) {
			t.Fatalf("unsafe coordinator log=%q", output)
		}
	}
}

func TestAutoDeployObserverLogUsesOnlyAggregateAllowlist(t *testing.T) {
	const secret = "app=identity repo=private sha=aaaaaaaa token=secret raw=provider-body"
	var logs bytes.Buffer
	logger := newStructuredLogger(&logs, "info")
	observeAutoDeployLifecycle(logger, autodeploy.CoordinatorEvent{
		Outcome: secret, State: secret, PauseCode: secret, NextAction: secret, RetryAttempt: ^uint32(0),
		Claimed: 1, Resolved: 2, Dispatched: 3, Paused: 4, Retried: 5,
	})
	output := logs.String()
	if strings.Contains(output, secret) || strings.Contains(output, "identity") || strings.Contains(output, "private") || strings.Contains(output, "aaaaaaaa") || strings.Contains(output, "provider-body") ||
		!strings.Contains(output, autodeploy.OutcomePersistenceUnavailable) || !strings.Contains(output, `"state":"none"`) || !strings.Contains(output, `"pause_code":"none"`) ||
		!strings.Contains(output, `"retry_attempt":1000`) || !strings.Contains(output, `"next_action":"none"`) || !strings.Contains(output, `"dispatched":3`) {
		t.Fatalf("unsafe aggregate log=%q", output)
	}
}

func TestAutoDeployObserverLogPreservesAllowlistedLifecycleFields(t *testing.T) {
	var logs bytes.Buffer
	observeAutoDeployLifecycle(newStructuredLogger(&logs, "info"), autodeploy.CoordinatorEvent{
		Outcome: autodeploy.OutcomeProviderUnavailable, State: autodeploy.StateRetryWait,
		PauseCode: autodeploy.ObservedPauseNone, RetryAttempt: 3, NextAction: autodeploy.NextActionRetry,
		Claimed: 1, Retried: 1,
	})
	output := logs.String()
	for _, expected := range []string{
		autodeploy.OutcomeProviderUnavailable, `"state":"retry_wait"`, `"pause_code":"none"`,
		`"retry_attempt":3`, `"next_action":"retry"`, `"claimed":1`, `"retried":1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("lifecycle log missing %q output=%q", expected, output)
		}
	}
}

func TestSafeSourceErrorTreatsLocalPersistenceAsTransient(t *testing.T) {
	for _, code := range []string{"internal_error", "provider_unavailable", "connection_not_found", "invalid_source"} {
		mapped := safeSourceError(&sourceconnections.Error{Code: code})
		var sourceErr *autodeploy.SourceError
		if !errors.As(mapped, &sourceErr) {
			t.Fatalf("code=%q mapped=%T", code, mapped)
		}
		want := autodeploy.OutcomeInvalidSource
		switch code {
		case "internal_error":
			want = autodeploy.OutcomePersistenceUnavailable
		case "provider_unavailable":
			want = autodeploy.OutcomeProviderUnavailable
		case "connection_not_found":
			want = autodeploy.OutcomeSourceAccessLost
		}
		if sourceErr.Code != want {
			t.Fatalf("code=%q mapped=%q want=%q", code, sourceErr.Code, want)
		}
	}
}

func enabledAutoDeployConfig() config.Config {
	cfg := config.Defaults()
	cfg.ComposeRuntime = true
	cfg.GitHubClientID = "client"
	cfg.GitHubAppSlug = "app"
	return cfg
}
