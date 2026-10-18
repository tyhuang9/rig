package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scopedAdmissionContextKey struct{}

type scopedAdmissionExecutorFunc func(context.Context, Job, ProgressReporter) (ExecutionResult, error)

func (f scopedAdmissionExecutorFunc) Execute(ctx context.Context, job Job, reporter ProgressReporter) (ExecutionResult, error) {
	return f(ctx, job, reporter)
}

type scopedAdmissionExecutor struct {
	t             *testing.T
	wantScope     string
	leaseHeld     *atomic.Bool
	executions    *atomic.Int32
	scopeObserved chan<- context.Context
}

func (e scopedAdmissionExecutor) Execute(ctx context.Context, _ Job, _ ProgressReporter) (ExecutionResult, error) {
	e.t.Helper()
	e.executions.Add(1)
	if got, _ := ctx.Value(scopedAdmissionContextKey{}).(string); got != e.wantScope {
		e.t.Fatalf("executor scope = %q, want %q", got, e.wantScope)
	}
	if !e.leaseHeld.Load() {
		e.t.Fatal("executor ran after scoped admission was released")
	}
	e.scopeObserved <- ctx
	return ExecutionResult{CompletionCode: "deployment_completed"}, nil
}

func TestScopedAdmissionReachesExecutorAndCoversTerminalPersistence(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "scoped-success", "")
	if err != nil {
		t.Fatal(err)
	}

	var acquisitions, executions atomic.Int32
	var leaseHeld atomic.Bool
	var orderMu sync.Mutex
	order := make([]string, 0, 4)
	appendOrder := func(value string) {
		orderMu.Lock()
		order = append(order, value)
		orderMu.Unlock()
	}
	service.beforeRegister = func() {
		if !leaseHeld.Load() {
			t.Fatal("job was claimed before scoped admission")
		}
		appendOrder("claimed")
	}

	parent, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	scopeObserved := make(chan context.Context, 1)
	released := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorkerWithScopedAdmission(parent, scopedAdmissionExecutor{
			t: t, wantScope: "scope-1", leaseHeld: &leaseHeld, executions: &executions,
			scopeObserved: scopeObserved,
		}, func(parent context.Context) (context.Context, func() error, error) {
			acquisitions.Add(1)
			leaseHeld.Store(true)
			appendOrder("acquired")
			scoped, invalidate := context.WithCancel(context.WithValue(parent, scopedAdmissionContextKey{}, "scope-1"))
			return scoped, func() error {
				persisted, getErr := service.Get(job.ID)
				if getErr != nil {
					return getErr
				}
				if persisted.Status != string(Succeeded) || persisted.Phase != "succeeded" {
					return errors.New("scoped admission released before terminal persistence")
				}
				appendOrder("persisted")
				leaseHeld.Store(false)
				invalidate()
				appendOrder("released")
				released <- struct{}{}
				return nil
			}, nil
		})
	}()

	var admittedContext context.Context
	select {
	case admittedContext = <-scopeObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("scoped executor did not run")
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("scoped admission was not released")
	}
	if acquisitions.Load() != 1 || executions.Load() != 1 {
		t.Fatalf("acquisitions=%d executions=%d", acquisitions.Load(), executions.Load())
	}
	select {
	case <-admittedContext.Done():
	default:
		t.Fatal("release did not invalidate the admitted context")
	}
	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	wantOrder := []string{"acquired", "claimed", "persisted", "released"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("scope order = %v, want %v", gotOrder, wantOrder)
	}
	for index := range wantOrder {
		if gotOrder[index] != wantOrder[index] {
			t.Fatalf("scope order = %v, want %v", gotOrder, wantOrder)
		}
	}

	cancelWorker()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scoped worker did not stop")
	}
}

type scopedCancellationExecutor struct {
	started        chan<- struct{}
	cleanupStarted chan<- struct{}
	finishCleanup  <-chan struct{}
	leaseHeld      *atomic.Bool
}

func (e scopedCancellationExecutor) Execute(ctx context.Context, _ Job, _ ProgressReporter) (ExecutionResult, error) {
	if ctx.Value(scopedAdmissionContextKey{}) != "cancel-scope" || !e.leaseHeld.Load() {
		return ExecutionResult{}, errors.New("missing live cancellation scope")
	}
	e.started <- struct{}{}
	<-ctx.Done()
	e.cleanupStarted <- struct{}{}
	<-e.finishCleanup
	if !e.leaseHeld.Load() {
		return ExecutionResult{}, errors.New("scope released before executor cleanup")
	}
	return ExecutionResult{}, errors.New("executor stopped after cancellation cleanup")
}

func TestScopedAdmissionCancellationRetainsLeaseThroughExecutorCleanup(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "scoped-cancel", "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var leaseHeld atomic.Bool
	started := make(chan struct{}, 1)
	cleanupStarted := make(chan struct{}, 1)
	finishCleanup := make(chan struct{})
	released := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorkerWithScopedAdmission(ctx, scopedCancellationExecutor{
			started: started, cleanupStarted: cleanupStarted, finishCleanup: finishCleanup, leaseHeld: &leaseHeld,
		}, func(parent context.Context) (context.Context, func() error, error) {
			leaseHeld.Store(true)
			return context.WithValue(parent, scopedAdmissionContextKey{}, "cancel-scope"), func() error {
				leaseHeld.Store(false)
				released <- struct{}{}
				return nil
			}, nil
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scoped executor did not start")
	}
	cancel()
	select {
	case <-cleanupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("scoped executor did not enter cancellation cleanup")
	}
	select {
	case <-released:
		t.Fatal("scoped admission released before cancellation cleanup")
	default:
	}
	close(finishCleanup)
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("scoped admission was not released after cleanup")
	}
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled scoped worker did not stop")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Assigned) {
		t.Fatalf("canceled scoped job = %#v, %v", persisted, err)
	}
}

func TestScopedAdmissionCancellationAfterAcquireReleasesWithoutClaim(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "scoped-cancel-after-acquire", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var executed, released atomic.Bool
	err = service.RunWorkerWithScopedAdmission(ctx, scopedAdmissionExecutorFunc(func(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
		executed.Store(true)
		return ExecutionResult{}, nil
	}), func(parent context.Context) (context.Context, func() error, error) {
		cancel()
		return context.WithValue(parent, scopedAdmissionContextKey{}, "canceled-before-claim"), func() error {
			released.Store(true)
			return nil
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed.Load() || !released.Load() {
		t.Fatalf("executed=%t released=%t", executed.Load(), released.Load())
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Queued) || persisted.Attempt != 0 {
		t.Fatalf("job after cancellation at acquire return = %#v, %v", persisted, err)
	}
}

func TestScopedAdmissionIncompleteScopeRefusesBeforeClaim(t *testing.T) {
	tests := []struct {
		name         string
		admitted     func(context.Context) context.Context
		withRelease  bool
		releaseErr   error
		wantReleases int32
	}{
		{name: "nil context", admitted: func(context.Context) context.Context { return nil }, withRelease: true, wantReleases: 1},
		{name: "nil context with cleanup failure", admitted: func(context.Context) context.Context { return nil }, withRelease: true, releaseErr: errors.New("release failed"), wantReleases: 1},
		{name: "nil release", admitted: func(parent context.Context) context.Context { return parent }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, closeDB := newTestService(t)
			defer closeDB()
			job, _, err := service.Create("deploy", "machine", "invalid-"+test.name, "")
			if err != nil {
				t.Fatal(err)
			}
			var executed atomic.Bool
			var releases atomic.Int32
			release := func() error {
				releases.Add(1)
				return test.releaseErr
			}
			if !test.withRelease {
				release = nil
			}
			err = service.RunWorkerWithScopedAdmission(context.Background(), scopedAdmissionExecutorFunc(func(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
				executed.Store(true)
				return ExecutionResult{}, nil
			}), func(parent context.Context) (context.Context, func() error, error) {
				return test.admitted(parent), release, nil
			})
			if err == nil || !strings.Contains(err.Error(), "deployment effects admission returned incomplete scope") {
				t.Fatalf("incomplete scope error = %v", err)
			}
			if test.releaseErr != nil && !errors.Is(err, test.releaseErr) {
				t.Fatalf("incomplete scope error = %v, want cleanup error %v", err, test.releaseErr)
			}
			if executed.Load() || releases.Load() != test.wantReleases {
				t.Fatalf("executed=%t releases=%d wantReleases=%d", executed.Load(), releases.Load(), test.wantReleases)
			}
			persisted, getErr := service.Get(job.ID)
			if getErr != nil || persisted.Status != string(Queued) || persisted.Attempt != 0 {
				t.Fatalf("job after incomplete scope = %#v, %v", persisted, getErr)
			}
		})
	}
}
