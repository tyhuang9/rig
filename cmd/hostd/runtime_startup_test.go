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
	if want := []string{"deployments", "jobs", "worker"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerDefaultOffRecoversWithoutStartingWorker(t *testing.T) {
	var calls []string
	recovery := runtimeRecovery{
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
	if want := []string{"deployments", "jobs"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerRecoveryFailurePreventsWorker(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		wantCalls []string
	}{
		{name: "deployment recovery", failAt: "deployments", wantCalls: []string{"deployments"}},
		{name: "job recovery", failAt: "jobs", wantCalls: []string{"deployments", "jobs"}},
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

func TestPrepareRuntimeWorkerMissingDependenciesStillReleasesHeldAdmission(t *testing.T) {
	releaseCalls := 0
	done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
		jobs: func() error { return nil },
		releaseAdmission: func() error {
			releaseCalls++
			return nil
		},
	}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
		t.Fatal("worker started with missing recovery dependencies")
		return nil
	}, nil)
	if err == nil || done != nil || releaseCalls != 1 {
		t.Fatalf("missing dependencies done=%v err=%v release calls=%d", done, err, releaseCalls)
	}
}

func TestPrepareRuntimeWorkerReleasesHeldAdmissionAfterRecoveryBeforeWorker(t *testing.T) {
	var calls []string
	held := true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		releaseAdmission: func() error {
			calls = append(calls, "release")
			held = false
			return nil
		},
		deployments: func(context.Context) error {
			if !held {
				t.Fatal("deployment recovery ran without admission")
			}
			calls = append(calls, "deployments")
			return nil
		},
		jobs: func() error {
			if !held {
				t.Fatal("job recovery ran without admission")
			}
			calls = append(calls, "jobs")
			return nil
		},
	}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
		if held {
			t.Error("worker started before recovery admission was released")
		}
		calls = append(calls, "worker")
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
	if want := []string{"deployments", "jobs", "release", "worker"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("recovery admission order = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerRecoveryFailureStillReleasesHeldAdmission(t *testing.T) {
	recoveryFailure := errors.New("injected recovery failure")
	var calls []string
	done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
		deployments: func(context.Context) error {
			calls = append(calls, "deployments")
			return recoveryFailure
		},
		jobs: func() error {
			calls = append(calls, "jobs")
			return nil
		},
		releaseAdmission: func() error {
			calls = append(calls, "release")
			return nil
		},
	}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
		calls = append(calls, "worker")
		return nil
	}, nil)
	if !errors.Is(err, recoveryFailure) || done != nil {
		t.Fatalf("recovery failure done=%v err=%v", done, err)
	}
	if want := []string{"deployments", "release"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("recovery failure calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerHeldAdmissionReleaseFailureBlocksWorker(t *testing.T) {
	releaseFailure := errors.New("injected release failure")
	workerCalled := false
	done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
		deployments:      func(context.Context) error { return nil },
		jobs:             func() error { return nil },
		releaseAdmission: func() error { return releaseFailure },
	}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
		workerCalled = true
		return nil
	}, nil)
	if !errors.Is(err, releaseFailure) || done != nil || workerCalled {
		t.Fatalf("release failure done=%v err=%v worker called=%t", done, err, workerCalled)
	}
}
