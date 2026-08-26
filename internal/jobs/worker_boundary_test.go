package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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

func insertReadyReleaseFixture(t *testing.T, service *Service, appID, releaseID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := service.db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, "fixture-"+appID, "Fixture", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`INSERT INTO releases(id,app_id,created_at,workspace_state) VALUES(?,?,?, 'ready')`, releaseID, appID, now); err != nil {
		t.Fatal(err)
	}
}

func countEvents(events []Event, code string) int {
	count := 0
	for _, event := range events {
		if event.Code == code {
			count++
		}
	}
	return count
}

type unrecognizedInput struct{}

func (unrecognizedInput) jobInput() {}

func TestCreateWithInputRoundTripsTransactionallyAndDoesNotExposeInternalFields(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	request := CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "one", IdempotencyKey: "input-key", Input: NoInput{}}
	job, created, err := service.CreateWithInput(request)
	if err != nil || !created {
		t.Fatalf("create = %#v, %t, %v", job, created, err)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || string(persisted.Input) != "{}" {
		t.Fatalf("persisted input = %q, %v", persisted.Input, err)
	}
	replayed, created, err := service.CreateWithInput(CreateRequest{Type: request.Type, ResourceType: request.ResourceType, ResourceID: request.ResourceID, IdempotencyKey: request.IdempotencyKey, Input: NoInput{}})
	if err != nil || created || replayed.ID != job.ID || string(replayed.Input) != "{}" {
		t.Fatalf("idempotent replay = %#v, %t, %v", replayed, created, err)
	}
	if _, _, err = service.CreateWithInput(CreateRequest{Type: request.Type, ResourceType: request.ResourceType, ResourceID: request.ResourceID, IdempotencyKey: request.IdempotencyKey, Input: unrecognizedInput{}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid idempotent replay error = %v", err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || len(events) != 1 || events[0].Code != "job_queued" {
		t.Fatalf("queued event = %#v, %v", events, err)
	}
	encoded, err := json.Marshal(persisted)
	if err != nil || string(encoded) == "" || contains(string(encoded), "input") || contains(string(encoded), "attempt") {
		t.Fatalf("public job JSON leaked internal fields: %s (%v)", encoded, err)
	}
	if _, _, err := service.CreateWithInput(CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "bad", Input: unrecognizedInput{}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unrecognized input error = %v", err)
	}
	legacy, created, err := service.Create("deploy", "application", "legacy", "")
	if err != nil || !created || string(legacy.Input) != "{}" {
		t.Fatalf("legacy input = %#v, %t, %v", legacy, created, err)
	}
}

func TestIdempotentReplayRequiresIdenticalActorAndSealedInput(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	releaseID := uuid.NewString()
	request := CreateRequest{
		Type:           "deploy",
		ResourceType:   "application",
		ResourceID:     "one",
		IdempotencyKey: "same-key",
		RequestedBy:    "actor-one",
		Input: DeploymentInput{
			ReleaseID:         releaseID,
			ConfigurationMode: ConfigurationOriginal,
		},
	}
	insertReadyReleaseFixture(t, service, request.ResourceID, releaseID)
	created, wasCreated, err := service.CreateWithInput(request)
	if err != nil || !wasCreated {
		t.Fatalf("create = %#v, %t, %v", created, wasCreated, err)
	}
	replayed, wasCreated, err := service.CreateWithInput(request)
	if err != nil || wasCreated || replayed.ID != created.ID {
		t.Fatalf("replay = %#v, %t, %v", replayed, wasCreated, err)
	}

	differentActor := request
	differentActor.RequestedBy = "actor-two"
	if _, _, err := service.CreateWithInput(differentActor); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("actor mismatch error = %v", err)
	}
	differentMode := request
	differentMode.Input = DeploymentInput{ReleaseID: releaseID, ConfigurationMode: ConfigurationCurrent}
	if _, _, err := service.CreateWithInput(differentMode); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("input mismatch error = %v", err)
	}
	differentRelease := request
	differentRelease.Input = DeploymentInput{ReleaseID: uuid.NewString(), ConfigurationMode: ConfigurationOriginal}
	if _, _, err := service.CreateWithInput(differentRelease); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("release mismatch error = %v", err)
	}
}

func TestCreateWithInputFinalizedRollsBackJobEventAndWakeTogether(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	request := CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "one", IdempotencyKey: "atomic-finalize", RequestedBy: "owner", Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent}}
	finalizeErr := errors.New("link failed")
	var rolledBackID string
	_, _, err := service.CreateWithInputFinalized(request, func(tx *sql.Tx, job Job) error {
		rolledBackID = job.ID
		var count int
		if queryErr := tx.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='job_queued'`, job.ID).Scan(&count); queryErr != nil || count != 1 {
			t.Fatalf("queued event was not visible in finalize tx count=%d err=%v", count, queryErr)
		}
		return finalizeErr
	})
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("finalize error=%v", err)
	}
	if _, err = service.Get(rolledBackID); !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("rolled-back job remains: %v", err)
	}
	var jobsCount, eventsCount int
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=?`, rolledBackID).Scan(&eventsCount); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 0 || eventsCount != 0 || len(service.wake) != 0 {
		t.Fatalf("partial finalize jobs=%d events=%d wake=%d", jobsCount, eventsCount, len(service.wake))
	}

	finalizeCalls := 0
	created, wasCreated, err := service.CreateWithInputFinalized(request, func(_ *sql.Tx, job Job) error {
		finalizeCalls++
		if job.RequestedBy != request.RequestedBy {
			t.Fatalf("finalize actor=%q", job.RequestedBy)
		}
		return nil
	})
	if err != nil || !wasCreated || len(service.wake) != 1 {
		t.Fatalf("committed create=%#v created=%t err=%v wake=%d", created, wasCreated, err, len(service.wake))
	}
	<-service.wake
	replayed, wasCreated, err := service.CreateWithInputFinalized(request, func(_ *sql.Tx, job Job) error {
		finalizeCalls++
		if job.ID != created.ID {
			t.Fatalf("replay finalizer job=%q want=%q", job.ID, created.ID)
		}
		return nil
	})
	if err != nil || wasCreated || replayed.ID != created.ID || finalizeCalls != 2 || len(service.wake) != 1 {
		t.Fatalf("replay=%#v created=%t calls=%d wake=%d err=%v", replayed, wasCreated, finalizeCalls, len(service.wake), err)
	}
	events, err := service.Events(created.ID, 0)
	if err != nil || countEvents(events, "job_queued") != 1 {
		t.Fatalf("replay queued events=%#v err=%v", events, err)
	}
}

func TestCreateWithInputFinalizedConcurrentReplayFinalizesOneJob(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	request := CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "one", IdempotencyKey: "atomic-race", RequestedBy: "owner", Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent}}
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	finalize := func(_ *sql.Tx, _ Job) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	type result struct {
		job     Job
		created bool
		err     error
	}
	results := make(chan result, 2)
	go func() {
		job, created, err := service.CreateWithInputFinalized(request, finalize)
		results <- result{job: job, created: created, err: err}
	}()
	<-entered
	go func() {
		job, created, err := service.CreateWithInputFinalized(request, finalize)
		results <- result{job: job, created: created, err: err}
	}()
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.job.ID == "" || first.job.ID != second.job.ID || first.created == second.created || calls.Load() != 2 {
		t.Fatalf("concurrent results first=%#v second=%#v calls=%d", first, second, calls.Load())
	}
	var count int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("job count=%d err=%v", count, err)
	}
	events, err := service.Events(first.job.ID, 0)
	if err != nil || countEvents(events, "job_queued") != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
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

func TestClaimNextContentionAssignsOneJobOnce(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "contended", "")
	if err != nil {
		t.Fatal(err)
	}
	const claimers = 8
	start := make(chan struct{})
	results := make(chan struct {
		job Job
		ok  bool
		err error
	}, claimers)
	var group sync.WaitGroup
	for range claimers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			claimed, ok, err := service.claimNext(context.Background())
			results <- struct {
				job Job
				ok  bool
				err error
			}{claimed, ok, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	assigned := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			assigned++
			if result.job.ID != job.ID || result.job.Attempt != 1 {
				t.Fatalf("claimed job = %#v", result.job)
			}
		}
	}
	if assigned != 1 {
		t.Fatalf("successful claims = %d", assigned)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(events, "job_assigned") != 1 {
		t.Fatalf("assignment events = %#v", events)
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
	return ExecutionResult{}, nil
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
	executor := &successfulExecutor{start: make(chan struct{}, 2)}
	var workers sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- service.RunWorker(ctx, executor)
		}()
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
	if len(executor.seen) != 2 || executor.seen[0] == executor.seen[1] {
		executor.mu.Unlock()
		t.Fatalf("executed jobs = %#v", executor.seen)
	}
	executor.mu.Unlock()
	for _, id := range []string{"one", "two"} {
		deadline = time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			jobs, err := service.List(10)
			if err != nil {
				t.Fatal(err)
			}
			for _, job := range jobs {
				if job.ResourceID == id && job.Status == string(Succeeded) {
					events, err := service.Events(job.ID, 0)
					if err != nil {
						t.Fatal(err)
					}
					if countEvents(events, "job_assigned") != 1 || countEvents(events, "job_succeeded") != 1 {
						t.Fatalf("job %s events = %#v", id, events)
					}
					goto completed
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("job %s did not reach succeeded", id)
	completed:
	}
	cancel()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers did not stop")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("worker error = %v", err)
		}
	}
}

func TestWorkerCancellationBetweenWakeAndClaimIsCleanShutdown(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	enteredClaim := make(chan struct{}, 1)
	releaseClaim := make(chan struct{})
	service.beforeClaim = func() {
		enteredClaim <- struct{}{}
		<-releaseClaim
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, &successfulExecutor{}) }()
	service.signal()
	select {
	case <-enteredClaim:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach claim boundary")
	}
	cancel()
	close(releaseClaim)
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("worker cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancellation")
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

type cleanupExecutor struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (e cleanupExecutor) Execute(_ context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 10, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-e.release
	return ExecutionResult{}, context.Canceled
}

func TestActiveCancellationHoldsApplicationUntilExecutorCleanupAcrossServices(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	peerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer peerDB.Close()
	service := New(db)
	peer := New(peerDB)
	job, _, err := service.Create("deploy", "application", "same-app", "request-one")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, cleanupExecutor{started: started, release: release}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	cancelling, err := peer.Cancel(job.ID)
	if err != nil || cancelling.Status != string(Waiting) || cancelling.Phase != "cancelling" {
		t.Fatalf("cancellation request = %#v, %v", cancelling, err)
	}
	if _, err := peer.Cancel(job.ID); err != nil {
		t.Fatalf("repeat cancellation = %v", err)
	}
	events, err := peer.Events(job.ID, 0)
	if err != nil || countEvents(events, "cancellation_requested") != 1 || countEvents(events, "job_cancelled") != 0 {
		t.Fatalf("cancellation events before cleanup = %#v, %v", events, err)
	}
	if _, _, err := peer.Create("deploy", "application", "same-app", "request-two"); err == nil {
		t.Fatal("replacement application work was accepted while cleanup ran")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := peer.Get(job.ID)
		if err == nil && persisted.Status == string(Cancelled) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	persisted, err := peer.Get(job.ID)
	if err != nil || persisted.Status != string(Cancelled) {
		t.Fatalf("cleanup cancellation = %#v, %v", persisted, err)
	}
	events, err = peer.Events(job.ID, 0)
	if err != nil || countEvents(events, "job_cancelled") != 1 {
		t.Fatalf("final cancellation events = %#v, %v", events, err)
	}
	if _, created, err := peer.Create("deploy", "application", "same-app", "request-two"); err != nil || !created {
		t.Fatalf("replacement after cleanup = %t, %v", created, err)
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("worker error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

type mustNotExecuteExecutor struct{ executed chan<- struct{} }

func (e mustNotExecuteExecutor) Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
	e.executed <- struct{}{}
	return ExecutionResult{}, nil
}

func TestCancellationBetweenClaimAndRegistrationFinalizesWithoutExecuting(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "claim-race", "")
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{}, 1)
	continueRegistration := make(chan struct{})
	service.beforeRegister = func() {
		claimed <- struct{}{}
		<-continueRegistration
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	executed := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, mustNotExecuteExecutor{executed: executed}) }()
	select {
	case <-claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not pause after claim")
	}
	cancelling, err := service.Cancel(job.ID)
	if err != nil || cancelling.Status != string(Waiting) || cancelling.Phase != "cancelling" {
		t.Fatalf("claim-race cancellation = %#v, %v", cancelling, err)
	}
	close(continueRegistration)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Cancelled) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Cancelled) {
		t.Fatalf("claim-race final state = %#v, %v", persisted, err)
	}
	select {
	case <-executed:
		t.Fatal("executor ran after cancellation request")
	default:
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || countEvents(events, "cancellation_requested") != 1 || countEvents(events, "job_cancelled") != 1 {
		t.Fatalf("claim-race events = %#v, %v", events, err)
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestCancellationAfterAssignmentVerificationFinalizesWithoutExecuting(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "verified-race", "")
	if err != nil {
		t.Fatal(err)
	}
	verified := make(chan struct{}, 1)
	continueExecution := make(chan struct{})
	service.beforeExecute = func() {
		verified <- struct{}{}
		<-continueExecution
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	executed := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, mustNotExecuteExecutor{executed: executed}) }()
	select {
	case <-verified:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not verify assignment")
	}
	cancelling, err := service.Cancel(job.ID)
	if err != nil || cancelling.Status != string(Waiting) || cancelling.Phase != "cancelling" {
		t.Fatalf("verified-race cancellation = %#v, %v", cancelling, err)
	}
	close(continueExecution)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, getErr := service.Get(job.ID)
		if getErr == nil && persisted.Status == string(Cancelled) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Cancelled) {
		t.Fatalf("verified-race final state = %#v, %v", persisted, err)
	}
	select {
	case <-executed:
		t.Fatal("executor ran after cancellation request")
	default:
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || countEvents(events, "cancellation_requested") != 1 || countEvents(events, "job_cancelled") != 1 {
		t.Fatalf("verified-race events = %#v, %v", events, err)
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	service.beforeExecute = nil
	replacement, created, err := service.Create("deploy", "application", "verified-race", "replacement")
	if err != nil || !created {
		t.Fatalf("replacement create = %#v, %t, %v", replacement, created, err)
	}
	if err := service.runOne(context.Background(), &successfulExecutor{}); err != nil {
		t.Fatalf("replacement execution = %v", err)
	}
	replacement, err = service.Get(replacement.ID)
	if err != nil || replacement.Status != string(Succeeded) {
		t.Fatalf("replacement final state = %#v, %v", replacement, err)
	}
}

type successAfterCancellationExecutor struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (e successAfterCancellationExecutor) Execute(_ context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-e.release
	return ExecutionResult{}, nil
}

func TestConcreteSuccessWinsAfterCancellationRequest(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "success-after-cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- service.runOne(context.Background(), successAfterCancellationExecutor{started: started, release: release})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	cancelling, err := service.Cancel(job.ID)
	if err != nil || cancelling.Status != string(Waiting) || cancelling.Phase != "cancelling" {
		t.Fatalf("cancellation request = %#v, %v", cancelling, err)
	}
	close(release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not finish concrete success")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Succeeded) || persisted.Phase != "succeeded" {
		t.Fatalf("success after cancellation = %#v, %v", persisted, err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || countEvents(events, "cancellation_requested") != 1 || countEvents(events, "job_cancelled") != 0 || countEvents(events, "job_succeeded") != 1 {
		t.Fatalf("success after cancellation events = %#v, %v", events, err)
	}
}

type cancellationAwareExecutor struct {
	started  chan<- struct{}
	returned chan<- struct{}
}

func (e cancellationAwareExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 10, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-ctx.Done()
	if e.returned != nil {
		e.returned <- struct{}{}
	}
	return ExecutionResult{}, ctx.Err()
}

func TestPeerCancellationWatcherDeliversRequestToLocalExecutor(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	peerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer peerDB.Close()
	service := New(db)
	peer := New(peerDB)
	job, _, err := service.Create("deploy", "application", "peer-cancel", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	started := make(chan struct{}, 1)
	returned := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- service.RunWorker(ctx, cancellationAwareExecutor{started: started, returned: returned})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	if _, err := peer.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("peer cancellation watcher did not signal executor")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Cancelled) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Cancelled) {
		t.Fatalf("peer cancellation did not finalize: %#v, %v", persisted, err)
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner repeat cancellation did not signal executor")
	}
}

type unsafeFailureExecutor struct{}

func (unsafeFailureExecutor) Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
	return ExecutionResult{}, errors.New("secret database password")
}

type secretExecutionErrorExecutor struct{}

func (secretExecutionErrorExecutor) Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
	return ExecutionResult{}, &ExecutionError{Code: "validation_failed", Detail: "secret database password"}
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

func TestExecutionErrorDetailsAreNeverPersisted(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "typed-failure", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { _ = service.RunWorker(ctx, secretExecutionErrorExecutor{}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Failed) {
			if persisted.ErrorCode != "validation_failed" || persisted.ErrorDetail != "Job validation failed" || contains(persisted.ErrorDetail, "secret") {
				t.Fatalf("unsafe typed failure persisted: %#v", persisted)
			}
			events, err := service.Events(job.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if contains(event.Message, "secret") {
					t.Fatalf("secret leaked through event: %#v", event)
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("typed failure was not persisted")
}

type maliciousFailureExecutor struct{}

func (maliciousFailureExecutor) Execute(_ context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 35, Checkpoint: `{"phase":"durable"}`, Code: "phase_started", Message: "Durable progress"}); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{}, errors.New("failure")
}

func TestFailureUsesLastDurableProgressAndCheckpoint(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "malicious-failure", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { _ = service.RunWorker(ctx, maliciousFailureExecutor{}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Failed) {
			if persisted.Progress != 35 || persisted.Checkpoint != `{"phase":"running"}` {
				t.Fatalf("failure used executor result: %#v", persisted)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("malicious failure was not persisted")
}

func TestReporterUsesBoundedSafeEventsAndMonotonicProgress(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "reporter", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.claimNext(context.Background()); err != nil || !ok {
		t.Fatalf("claim = %t, %v", ok, err)
	}
	reporter := reporter{service: service, jobID: job.ID}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Checkpoint: `{"secret":"runtime-token"}`, Code: "phase_started", Message: "secret runtime token"}); err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 19, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("regression error = %v", err)
	}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Checkpoint: string(make([]byte, maxCheckpointBytes+1)), Code: "phase_started"}); err != nil {
		t.Fatalf("ignored oversize checkpoint error = %v", err)
	}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "secret_token", Progress: 20, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("invalid phase error = %v", err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Checkpoint != `{"phase":"running"}` || contains(persisted.Checkpoint, "secret") {
		t.Fatalf("executor checkpoint leaked: %#v, %v", persisted, err)
	}
	for _, event := range events {
		if contains(event.Message, "secret") {
			t.Fatalf("executor message leaked: %#v", event)
		}
	}
	for i := 0; i < maxJobEventsPerAttempt-reservedTerminalEvents-3; i++ {
		if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); err != nil {
			t.Fatalf("report %d = %v", i, err)
		}
	}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); !errors.Is(err, ErrEventBudget) {
		t.Fatalf("event budget error = %v", err)
	}
	if _, err := service.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); !errors.Is(err, ErrJobTerminal) {
		t.Fatalf("report after cancellation request = %v", err)
	}
}

func TestExportedJobMutatorsUseConstrainedPublicData(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "machine", "mutators", "")
	if err != nil {
		t.Fatal(err)
	}
	event, err := service.Append(job.ID, "info", "validate", "phase_started", "secret append text")
	if err != nil || event.Message != "Job phase started" || contains(event.Message, "secret") {
		t.Fatalf("safe append = %#v, %v", event, err)
	}
	if _, err := service.Append(job.ID, "warn", "secret_phase", "phase_started", "secret"); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("unsafe append = %v", err)
	}
	if err := service.Update(job.ID, Running, "running", 20, `{"secret":"ignored"}`); !errors.Is(err, ErrJobTerminal) {
		t.Fatalf("queued update bypass = %v", err)
	}
	if _, ok, err := service.claimNext(context.Background()); err != nil || !ok {
		t.Fatalf("claim = %t, %v", ok, err)
	}
	if err := service.Update(job.ID, Running, "running", 20, `{"secret":"ignored"}`); err != nil {
		t.Fatalf("constrained update = %v", err)
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Checkpoint != `{"phase":"running"}` || contains(persisted.Checkpoint, "secret") {
		t.Fatalf("constrained update state = %#v, %v", persisted, err)
	}
	if err := service.Update(job.ID, Running, "secret_phase", 20, "{}"); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("unsafe update phase = %v", err)
	}
}

type panicExecutor struct{}

func (panicExecutor) Execute(context.Context, Job, ProgressReporter) (ExecutionResult, error) {
	panic("secret executor panic")
}

func TestExecutorPanicBecomesGenericSafeFailure(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "panic", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { _ = service.RunWorker(ctx, panicExecutor{}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, err := service.Get(job.ID)
		if err == nil && persisted.Status == string(Failed) {
			if persisted.ErrorCode != "executor_failed" || persisted.ErrorDetail != "Job execution failed" {
				t.Fatalf("panic failure = %#v", persisted)
			}
			events, err := service.Events(job.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if contains(event.Message, "secret") {
					t.Fatalf("panic leaked: %#v", event)
				}
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("panic failure was not persisted")
}

type successOnShutdownExecutor struct{ started chan<- struct{} }

func (e successOnShutdownExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Checkpoint: `{"phase":"running"}`, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-ctx.Done()
	return ExecutionResult{}, nil
}

func TestConcreteSuccessWinsAgainstParentShutdownWithCanonicalResult(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "success-shutdown", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, successOnShutdownExecutor{started: started}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Succeeded) || persisted.Phase != "succeeded" || persisted.Progress != 100 || persisted.Checkpoint != `{"phase":"succeeded"}` {
		t.Fatalf("canonical success = %#v, %v", persisted, err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || events[len(events)-1].Message != "Job completed" || contains(events[len(events)-1].Message, "secret") {
		t.Fatalf("success event = %#v, %v", events, err)
	}
}

type shutdownFailureExecutor struct {
	started chan<- struct{}
	result  ExecutionResult
	err     error
}

func (e shutdownFailureExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "running", Progress: 20, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	<-ctx.Done()
	return e.result, e.err
}

func TestParentShutdownLeavesGenericExecutorFailureForRecovery(t *testing.T) {
	testParentShutdownLeavesActiveJob(t, shutdownFailureExecutor{err: errors.New("cleanup error")})
}

func TestParentShutdownLeavesInvalidCompletionForRecovery(t *testing.T) {
	testParentShutdownLeavesActiveJob(t, shutdownFailureExecutor{result: ExecutionResult{CompletionCode: "invalid"}})
}

func testParentShutdownLeavesActiveJob(t *testing.T, executor shutdownFailureExecutor) {
	t.Helper()
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "shutdown-nonterminal", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	executor.started = started
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, executor) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("worker error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Running) {
		t.Fatalf("shutdown job = %#v, %v", persisted, err)
	}
	if err := service.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	persisted, err = service.Get(job.ID)
	if err != nil || persisted.Status != string(Interrupted) {
		t.Fatalf("recovered job = %#v, %v", persisted, err)
	}
}

func TestWorkerShutdownLeavesActiveJobForRecovery(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	job, _, err := service.Create("deploy", "application", "shutdown", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	workerDone := make(chan error, 1)
	go func() { workerDone <- service.RunWorker(ctx, blockingExecutor{started: started}) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not start")
	}
	stop()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("worker returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after shutdown")
	}
	persisted, err := service.Get(job.ID)
	if err != nil || persisted.Status != string(Running) {
		t.Fatalf("shutdown job = %#v, %v", persisted, err)
	}
	if err := service.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	persisted, err = service.Get(job.ID)
	if err != nil || persisted.Status != string(Interrupted) {
		t.Fatalf("recovered shutdown job = %#v, %v", persisted, err)
	}
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
