package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	"github.com/hostd/hostd/internal/runtime/docker"
)

type admissionAssertingExecutor struct {
	held   *atomic.Bool
	called chan<- struct{}
}

func (e admissionAssertingExecutor) Execute(ctx context.Context, job Job, reporter ProgressReporter) (ExecutionResult, error) {
	if !e.held.Load() {
		return ExecutionResult{}, errors.New("deployment effects lease was not held during execution")
	}
	select {
	case e.called <- struct{}{}:
	default:
	}
	return (&successfulExecutor{}).Execute(ctx, job, reporter)
}

func TestAdmittedWorkerRequiresAdmissionAndKeepsItThroughTerminalState(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	if err := service.RunWorkerWithAdmission(context.Background(), &successfulExecutor{}, nil); err == nil {
		t.Fatal("worker accepted missing effects admission")
	}
	job, _, err := service.Create("deploy", "machine", "admitted", "")
	if err != nil {
		t.Fatal(err)
	}
	var held atomic.Bool
	called := make(chan struct{}, 1)
	released := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorkerWithAdmission(ctx, admissionAssertingExecutor{held: &held, called: called},
			func(context.Context) (func() error, error) {
				if held.Swap(true) {
					return nil, errors.New("effects admission was already held")
				}
				return func() error {
					if !held.Swap(false) {
						return errors.New("effects admission was released twice")
					}
					stored, err := service.Get(job.ID)
					if err != nil || stored.Status != string(Succeeded) {
						return errors.New("effects admission released before terminal job state")
					}
					released <- struct{}{}
					return nil
				}, nil
			})
	}()
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted executor did not run")
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("effects admission was not released after terminal persistence")
	}
	cancel()
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
}

func TestAdmittedWorkerCancellationWhileWaitingLeavesJobQueued(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "waiting", "")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	called := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorkerWithAdmission(ctx, admissionAssertingExecutor{held: &atomic.Bool{}, called: called},
			func(ctx context.Context) (func() error, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not wait for effects admission")
	}
	cancel()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled admission wait did not stop")
	}
	if len(called) != 0 {
		t.Fatal("executor ran without effects admission")
	}
	stored, err := service.Get(job.ID)
	if err != nil || stored.Status != string(Queued) || stored.Attempt != 0 {
		t.Fatalf("job after canceled admission = %#v, %v", stored, err)
	}
}

func TestAdmittedWorkerFenceFailureLeavesJobQueued(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "fenced", "")
	if err != nil {
		t.Fatal(err)
	}
	fenceFailure := errors.New("injected rebind fence failure")
	err = service.RunWorkerWithAdmission(context.Background(), &successfulExecutor{},
		func(context.Context) (func() error, error) { return nil, fenceFailure })
	if !errors.Is(err, fenceFailure) {
		t.Fatalf("worker fence failure = %v", err)
	}
	stored, err := service.Get(job.ID)
	if err != nil || stored.Status != string(Queued) || stored.Attempt != 0 {
		t.Fatalf("job after fence failure = %#v, %v", stored, err)
	}
}

func TestAdmittedWorkerReleaseFailureDoesNotRewriteSuccessfulJob(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "release-failure", "")
	if err != nil {
		t.Fatal(err)
	}
	releaseFailure := errors.New("injected effects lock release failure")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = service.RunWorkerWithAdmission(ctx, &successfulExecutor{},
		func(context.Context) (func() error, error) {
			return func() error { return releaseFailure }, nil
		})
	if !errors.Is(err, releaseFailure) {
		t.Fatalf("release failure = %v", err)
	}
	stored, err := service.Get(job.ID)
	if err != nil || stored.Status != string(Succeeded) {
		t.Fatalf("job after release failure = %#v, %v", stored, err)
	}
}

type blockingFirstAdmittedExecutor struct {
	firstID string
	started chan<- struct{}
	release <-chan struct{}
}

func (e blockingFirstAdmittedExecutor) Execute(ctx context.Context, job Job, reporter ProgressReporter) (ExecutionResult, error) {
	if job.ID == e.firstID {
		e.started <- struct{}{}
		select {
		case <-e.release:
		case <-ctx.Done():
			return ExecutionResult{}, ctx.Err()
		}
	}
	return (&successfulExecutor{}).Execute(ctx, job, reporter)
}

func TestAdmittedWorkersShareEffectsLockBeforeAssignment(t *testing.T) {
	root := t.TempDir()
	directories, err := docker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	firstService, secondService := New(db), New(db)
	first, _, err := firstService.Create("deploy", "machine", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	firstContext, stopFirst := context.WithCancel(context.Background())
	defer stopFirst()
	firstDone := make(chan error, 1)
	acquire := func(ctx context.Context) (func() error, error) {
		return deploymenteffects.Acquire(ctx, directories.WorkingDirectory)
	}
	go func() {
		firstDone <- firstService.RunWorkerWithAdmission(firstContext,
			blockingFirstAdmittedExecutor{firstID: first.ID, started: started, release: releaseFirst}, acquire)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not enter execution")
	}
	second, _, err := secondService.Create("deploy", "machine", "second", "")
	if err != nil {
		t.Fatal(err)
	}
	secondContext, stopSecond := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer stopSecond()
	secondAttempted := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- secondService.RunWorkerWithAdmission(secondContext, &successfulExecutor{},
			func(ctx context.Context) (func() error, error) {
				secondAttempted <- struct{}{}
				return acquire(ctx)
			})
	}()
	select {
	case <-secondAttempted:
	case <-time.After(2 * time.Second):
		t.Fatal("second worker did not seek effects admission")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("contended worker did not honor cancellation")
	}
	queued, err := secondService.Get(second.ID)
	if err != nil || queued.Status != string(Queued) || queued.Attempt != 0 {
		t.Fatalf("second job while first held lease = %#v, %v", queued, err)
	}
	close(releaseFirst)
	deadline := time.After(2 * time.Second)
	for {
		completed, err := firstService.Get(second.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status == string(Succeeded) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("second job did not run after first lease released: %#v", completed)
		case <-time.After(10 * time.Millisecond):
		}
	}
	stopFirst()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not stop")
	}
}

type admittedShutdownExecutor struct {
	started chan<- struct{}
	cleanup <-chan struct{}
}

func (e admittedShutdownExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-ctx.Done()
	<-e.cleanup
	return ExecutionResult{}, errors.New("executor cleanup failed after owner shutdown")
}

func TestAdmittedWorkerHoldsEffectsLeaseThroughShutdownCleanup(t *testing.T) {
	root := t.TempDir()
	directories, err := docker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "shutdown", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := make(chan struct{})
	var cleanupOnce sync.Once
	allowCleanup := func() { cleanupOnce.Do(func() { close(cleanup) }) }
	t.Cleanup(allowCleanup)
	started := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorkerWithAdmission(ctx,
			admittedShutdownExecutor{started: started, cleanup: cleanup},
			func(ctx context.Context) (func() error, error) {
				return deploymenteffects.Acquire(ctx, directories.WorkingDirectory)
			})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("admitted executor did not start")
	}
	cancel()
	contendedCtx, stopContention := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer stopContention()
	if release, err := deploymenteffects.Acquire(contendedCtx, directories.WorkingDirectory); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		t.Fatalf("effects lease was available during shutdown cleanup: %v", err)
	}
	select {
	case err := <-workerDone:
		t.Fatalf("worker stopped before executor cleanup completed: %v", err)
	default:
	}
	allowCleanup()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("shutdown worker error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admitted worker did not stop after executor cleanup")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Running) {
		t.Fatalf("shutdown job must remain visible for recovery: %#v, %v", persisted, err)
	}
	release, err := deploymenteffects.Acquire(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatalf("effects lease was not released after cleanup: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	persisted, err = service.Get(job.ID)
	if err != nil || persisted.Status != string(Interrupted) {
		t.Fatalf("recovered shutdown job = %#v, %v", persisted, err)
	}
}
