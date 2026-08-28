package main

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/jobs"
)

type startupTestExecutor struct{}

func (*startupTestExecutor) Execute(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	return jobs.ExecutionResult{}, nil
}

func TestPrepareRuntimeWorkerRecoversInOrderBeforeStartingOneWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls []string
	recovery := runtimeRecovery{
		temporary: func() error {
			calls = append(calls, "temporary")
			return nil
		},
		deployments: func(recoveryContext context.Context) error {
			if recoveryContext.Err() != nil {
				t.Fatalf("deployment recovery inherited cancelled startup context: %v", recoveryContext.Err())
			}
			calls = append(calls, "deployments")
			return nil
		},
		jobs: func() error {
			calls = append(calls, "jobs")
			return nil
		},
	}
	executor := &startupTestExecutor{}
	var runCount atomic.Int32
	done, err := prepareRuntimeWorker(ctx, recovery, executor, func(runContext context.Context, got jobs.Executor) error {
		if runContext != ctx {
			t.Fatal("worker received a different startup context")
		}
		if got != executor {
			t.Fatalf("worker executor = %#v, want supplied executor %#v", got, executor)
		}
		runCount.Add(1)
		calls = append(calls, "worker")
		return nil
	}, func(err error) {
		t.Errorf("unexpected worker failure: %v", err)
	})
	if err != nil {
		t.Fatalf("prepareRuntimeWorker() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
	if got := runCount.Load(); got != 1 {
		t.Fatalf("worker run count = %d, want 1", got)
	}
	if want := []string{"temporary", "deployments", "jobs", "worker"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerDefaultOffRecoversWithoutStartingWorker(t *testing.T) {
	var calls []string
	recovery := runtimeRecovery{
		temporary: func() error {
			calls = append(calls, "temporary")
			return nil
		},
		deployments: func(context.Context) error {
			calls = append(calls, "deployments")
			return nil
		},
		jobs: func() error {
			calls = append(calls, "jobs")
			return nil
		},
	}
	var runCount atomic.Int32
	done, err := prepareRuntimeWorker(context.Background(), recovery, nil, func(context.Context, jobs.Executor) error {
		runCount.Add(1)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("prepareRuntimeWorker() error = %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("default-off worker completion channel was not already closed")
	}
	if got := runCount.Load(); got != 0 {
		t.Fatalf("worker run count = %d, want 0", got)
	}
	if want := []string{"temporary", "deployments", "jobs"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerRecoveryFailurePreventsWorker(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		wantCalls []string
	}{
		{name: "temporary cleanup", failAt: "temporary", wantCalls: []string{"temporary"}},
		{name: "deployment recovery", failAt: "deployments", wantCalls: []string{"temporary", "deployments"}},
		{name: "job recovery", failAt: "jobs", wantCalls: []string{"temporary", "deployments", "jobs"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := errors.New("recovery failed")
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if name == test.failAt {
					return failure
				}
				return nil
			}
			var runCount atomic.Int32
			done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
				temporary: func() error { return step("temporary") },
				deployments: func(context.Context) error {
					return step("deployments")
				},
				jobs: func() error { return step("jobs") },
			}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
				runCount.Add(1)
				return nil
			}, nil)
			if !errors.Is(err, failure) {
				t.Fatalf("prepareRuntimeWorker() error = %v, want wrapped recovery failure", err)
			}
			if done != nil {
				t.Fatal("recovery failure returned a worker completion channel")
			}
			if got := runCount.Load(); got != 0 {
				t.Fatalf("worker run count = %d, want 0", got)
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("recovery calls = %v, want %v", calls, test.wantCalls)
			}
		})
	}
}
