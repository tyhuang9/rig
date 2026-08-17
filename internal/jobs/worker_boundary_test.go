package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
)

func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return New(db), func() { _ = db.Close() }
}

func TestCreateWithInputRoundTripsTransactionallyAndDoesNotExposeInternalFields(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	request := CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "one", IdempotencyKey: "input-key", Input: json.RawMessage(`{"revision":"abc","force":true}`)}
	job, created, err := service.CreateWithInput(request)
	if err != nil || !created {
		t.Fatalf("create = %#v, %t, %v", job, created, err)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || string(persisted.Input) != string(request.Input) {
		t.Fatalf("persisted input = %q, %v", persisted.Input, err)
	}
	replayed, created, err := service.CreateWithInput(CreateRequest{Type: request.Type, ResourceType: request.ResourceType, ResourceID: request.ResourceID, IdempotencyKey: request.IdempotencyKey, Input: json.RawMessage(`{"revision":"different"}`)})
	if err != nil || created || replayed.ID != job.ID || string(replayed.Input) != string(request.Input) {
		t.Fatalf("idempotent replay = %#v, %t, %v", replayed, created, err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || len(events) != 1 || events[0].Code != "job_queued" {
		t.Fatalf("queued event = %#v, %v", events, err)
	}
	encoded, err := json.Marshal(persisted)
	if err != nil || string(encoded) == "" || contains(string(encoded), "input") || contains(string(encoded), "attempt") {
		t.Fatalf("public job JSON leaked internal fields: %s (%v)", encoded, err)
	}
	for _, input := range []json.RawMessage{json.RawMessage(`not-json`), json.RawMessage(`[]`), json.RawMessage(`null`)} {
		if _, _, err := service.CreateWithInput(CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "bad-" + string(input), Input: input}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input %q error = %v", input, err)
		}
	}
	legacy, created, err := service.Create("deploy", "application", "legacy", "")
	if err != nil || !created || string(legacy.Input) != "{}" {
		t.Fatalf("legacy input = %#v, %t, %v", legacy, created, err)
	}
}

func TestClaimNextIsFIFOAndPersistsSingleAssignment(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { now = now.Add(time.Nanosecond); return now }
	first, _, err := service.Create("deploy", "machine", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.Create("deploy", "machine", "two", "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := service.claimNext(context.Background())
	if err != nil || !ok || claimed.ID != first.ID || claimed.Attempt != 1 || claimed.Status != string(Assigned) {
		t.Fatalf("first claim = %#v, %t, %v", claimed, ok, err)
	}
	claimed, ok, err = service.claimNext(context.Background())
	if err != nil || !ok || claimed.ID != second.ID || claimed.Attempt != 1 {
		t.Fatalf("second claim = %#v, %t, %v", claimed, ok, err)
	}
	for _, id := range []string{first.ID, second.ID} {
		events, err := service.Events(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		assigned := 0
		for _, event := range events {
			if event.Code == "job_assigned" {
				assigned++
			}
		}
		if assigned != 1 {
			t.Fatalf("job %s assignment events = %#v", id, events)
		}
	}
	if _, ok, err := service.claimNext(context.Background()); err != nil || ok {
		t.Fatalf("empty claim = %t, %v", ok, err)
	}
}

type successfulExecutor struct {
	mu    sync.Mutex
	seen  []string
	start chan struct{}
}

func (e *successfulExecutor) Execute(_ context.Context, job Job, reporter ProgressReporter) (ExecutionResult, error) {
	e.mu.Lock()
	e.seen = append(e.seen, job.ID)
	e.mu.Unlock()
	if e.start != nil {
		select {
		case e.start <- struct{}{}:
		default:
		}
	}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 50, Checkpoint: `{"phase":"running"}`, Code: "phase_started", Message: "Running"}); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Phase: "succeeded", Progress: 100, Checkpoint: `{"phase":"succeeded"}`, Message: "Completed"}, nil
}

func TestConcurrentWorkersClaimAndExecuteEachJobOnce(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	for _, id := range []string{"one", "two"} {
		if _, _, err := service.Create("deploy", "machine", id, ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &successfulExecutor{start: make(chan struct{}, 2)}
	for range 2 {
		go func() { _ = service.RunWorker(ctx, executor) }()
	}
	for range 2 {
		select {
		case <-executor.start:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not execute a claimed job")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		count := len(executor.seen)
		executor.mu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.seen) != 2 || executor.seen[0] == executor.seen[1] {
		t.Fatalf("executed jobs = %#v", executor.seen)
	}
}

type blockingExecutor struct{ started chan<- struct{} }

func (e blockingExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 10, Checkpoint: `{"phase":"running"}`, Code: "phase_started", Message: "Running"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-ctx.Done()
	return ExecutionResult{}, ctx.Err()
}

func TestCancelCancelsActiveExecutorAndPreventsTerminalOverwrite(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	started := make(chan struct{}, 1)
	go func() { _ = service.RunWorker(ctx, blockingExecutor{started: started}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	if _, err := service.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Cancelled) {
			events, _ := service.Events(job.ID, 0)
			if events[len(events)-1].Code == "job_cancelled" {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancellation did not remain the terminal result")
}

type unsafeFailureExecutor struct{}

func (unsafeFailureExecutor) Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
	return ExecutionResult{}, errors.New("secret database password")
}

func TestUnknownExecutorFailureIsRedacted(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "failure", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { _ = service.RunWorker(ctx, unsafeFailureExecutor{}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Failed) {
			if persisted.ErrorCode != "executor_failed" || persisted.ErrorDetail != "Job execution failed" || contains(persisted.ErrorDetail, "secret") {
				t.Fatalf("unsafe failure persisted: %#v", persisted)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("failure was not persisted")
}

func TestFakeExecutorPhaseParityAndInterruptedRecovery(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	executor := NewFakeExecutor()
	executor.phaseDelay = time.Millisecond
	go func() { _ = service.RunWorker(ctx, executor) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Succeeded) {
			events, _ := service.Events(job.ID, 0)
			var phases []string
			for _, event := range events {
				if event.Code == "phase_started" {
					phases = append(phases, event.Phase)
				}
			}
			want := []string{"validate", "prepare_workspace", "apply_fake_runtime", "wait_for_health", "finalize"}
			if len(phases) != len(want) {
				t.Fatalf("phases = %#v", phases)
			}
			for i := range want {
				if phases[i] != want[i] {
					t.Fatalf("phases = %#v", phases)
				}
			}
			if events[len(events)-1].Message != "Fake deployment completed" {
				t.Fatalf("final event = %#v", events[len(events)-1])
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Succeeded) {
		t.Fatalf("fake completion = %#v, %v", persisted, err)
	}
	stop()
	interrupted, _, err := service.Create("deploy", "machine", "recover", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.claimNext(context.Background()); err != nil || !ok {
		t.Fatalf("claim for recovery = %t, %v", ok, err)
	}
	if err := service.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	persisted, err = service.Get(interrupted.ID)
	if err != nil || persisted.Status != string(Interrupted) {
		t.Fatalf("recovered job = %#v, %v", persisted, err)
	}
}
