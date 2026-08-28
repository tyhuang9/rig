package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

type atomicExecutor struct {
	started chan<- struct{}
	release <-chan struct{}
	result  error
}

func (e atomicExecutor) Execute(ctx context.Context, _ Job, reporter ProgressReporter) (ExecutionResult, error) {
	if err := reporter.Report(ProgressUpdate{Status: Running, Phase: "apply_fake_runtime", Progress: 70, Code: "phase_started"}); err != nil {
		return ExecutionResult{}, err
	}
	e.started <- struct{}{}
	select {
	case <-e.release:
		return ExecutionResult{}, e.result
	case <-ctx.Done():
		return ExecutionResult{}, ctx.Err()
	}
}

func TestRunnerCancellationAndProgressHaveOneTerminalOutcome(t *testing.T) {
	for attempt := 0; attempt < 16; attempt++ {
		db, err := database.Open(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		service := New(db)
		job, _, err := service.Create("deploy", "application", "app-race", "race-request")
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		runDone := make(chan error, 1)
		go func() {
			runDone <- service.runOne(context.Background(), atomicExecutor{started: started, release: release})
		}()
		<-started
		_, cancelErr := service.Cancel(job.ID)
		if cancelErr != nil {
			db.Close()
			t.Fatalf("attempt %d: cancel = %v", attempt, cancelErr)
		}
		if err := <-runDone; err != nil {
			db.Close()
			t.Fatalf("attempt %d: runner = %v", attempt, err)
		}
		persisted, err := service.Get(job.ID)
		if err != nil || persisted.Status != string(Cancelled) {
			db.Close()
			t.Fatalf("attempt %d: cancelled job = %#v, %v", attempt, persisted, err)
		}
		events, err := service.Events(job.ID, 0)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if countEvents(events, "cancellation_requested") != 1 || countEvents(events, "job_cancelled") != 1 || countEvents(events, "job_succeeded") != 0 || countEvents(events, "job_failed") != 0 {
			db.Close()
			t.Fatalf("attempt %d: events = %#v", attempt, events)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunnerCompletionAndCancellationHaveOneTerminalOutcome(t *testing.T) {
	for attempt := 0; attempt < 16; attempt++ {
		db, err := database.Open(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		service := New(db)
		job, _, err := service.Create("deploy", "application", "app-completion-race", "completion-race-request")
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		runDone := make(chan error, 1)
		go func() {
			runDone <- service.runOne(context.Background(), atomicExecutor{started: started, release: release})
		}()
		<-started
		var group sync.WaitGroup
		group.Add(2)
		var cancelErr error
		go func() {
			defer group.Done()
			_, cancelErr = service.Cancel(job.ID)
		}()
		go func() {
			defer group.Done()
			close(release)
		}()
		group.Wait()
		if cancelErr != nil && !errors.Is(cancelErr, ErrJobTerminal) {
			db.Close()
			t.Fatalf("attempt %d: cancel = %v", attempt, cancelErr)
		}
		if err := <-runDone; err != nil {
			db.Close()
			t.Fatalf("attempt %d: runner = %v", attempt, err)
		}
		events, err := service.Events(job.ID, 0)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		terminalEvents := countEvents(events, "job_cancelled") + countEvents(events, "job_succeeded") + countEvents(events, "job_failed")
		if terminalEvents != 1 {
			db.Close()
			t.Fatalf("attempt %d: terminal events = %#v", attempt, events)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
