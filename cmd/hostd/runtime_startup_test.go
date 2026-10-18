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
	defer cancel()

	var calls []string
	recovery := runtimeRecovery{
		recoverState: func(recoveryContext context.Context) error {
			if recoveryContext != ctx {
				t.Fatal("runtime recovery received a different startup context")
			}
			calls = append(calls, "recover")
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
	if want := []string{"recover", "worker"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerDefaultOffRecoversWithoutStartingWorker(t *testing.T) {
	var calls []string
	recovery := runtimeRecovery{
		recoverState: func(context.Context) error {
			calls = append(calls, "recover")
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
	if want := []string{"recover"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerRecoveryFailurePreventsWorker(t *testing.T) {
	tests := []struct {
		name      string
		failAt    string
		wantCalls []string
	}{
		{name: "combined recovery", failAt: "recover", wantCalls: []string{"recover"}},
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
				recoverState: func(context.Context) error {
					return step("recover")
				},
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
	defer cancel()
	done, err := prepareRuntimeWorker(ctx, runtimeRecovery{
		releaseAdmission: func() error {
			calls = append(calls, "release")
			held = false
			return nil
		},
		recoverState: func(context.Context) error {
			if !held {
				t.Fatal("runtime recovery ran without admission")
			}
			calls = append(calls, "recover")
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
	if want := []string{"recover", "release", "worker"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("recovery admission order = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerRecoveryFailureStillReleasesHeldAdmission(t *testing.T) {
	recoveryFailure := errors.New("injected recovery failure")
	var calls []string
	done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
		recoverState: func(context.Context) error {
			calls = append(calls, "recover")
			return recoveryFailure
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
	if want := []string{"recover", "release"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("recovery failure calls = %v, want %v", calls, want)
	}
}

func TestPrepareRuntimeWorkerHeldAdmissionReleaseFailureBlocksWorker(t *testing.T) {
	releaseFailure := errors.New("injected release failure")
	workerCalled := false
	done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
		recoverState:     func(context.Context) error { return nil },
		releaseAdmission: func() error { return releaseFailure },
	}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
		workerCalled = true
		return nil
	}, nil)
	if !errors.Is(err, releaseFailure) || done != nil || workerCalled {
		t.Fatalf("release failure done=%v err=%v worker called=%t", done, err, workerCalled)
	}
}

func TestPrepareRuntimeWorkerCancellationReleasesAdmissionWithoutWorker(t *testing.T) {
	for _, stage := range []string{"before_recovery", "during_recovery", "during_release"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before_recovery" {
				cancel()
			}
			var calls []string
			done, err := prepareRuntimeWorker(ctx, runtimeRecovery{
				recoverState: func(received context.Context) error {
					if received != ctx {
						t.Fatal("runtime recovery lost caller context")
					}
					calls = append(calls, "recover")
					if stage == "during_recovery" {
						cancel()
					}
					return nil
				},
				releaseAdmission: func() error {
					calls = append(calls, "release")
					if stage == "during_release" {
						cancel()
					}
					return nil
				},
			}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
				t.Error("cancelled startup launched a worker")
				return nil
			}, nil)
			if !errors.Is(err, context.Canceled) || done != nil {
				t.Fatalf("cancelled startup done=%v err=%v", done, err)
			}
			want := []string{"recover", "release"}
			if stage == "before_recovery" {
				want = []string{"release"}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("cancelled startup calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestPrepareRuntimeWorkerPreservesRecoveryAndReleaseFailures(t *testing.T) {
	recoveryFailure, releaseFailure := errors.New("recovery failed"), errors.New("release failed")
	releases := 0
	done, err := prepareRuntimeWorker(context.Background(), runtimeRecovery{
		recoverState: func(context.Context) error { return recoveryFailure },
		releaseAdmission: func() error {
			releases++
			return releaseFailure
		},
	}, &startupTestExecutor{}, func(context.Context, jobs.Executor) error {
		t.Error("failed startup launched a worker")
		return nil
	}, nil)
	if !errors.Is(err, recoveryFailure) || !errors.Is(err, releaseFailure) || done != nil || releases != 1 {
		t.Fatalf("combined failure done=%v err=%v releases=%d", done, err, releases)
	}
}
