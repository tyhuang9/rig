package jobs

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/database"
)

func TestWorkerTransitionAndCancellationAreAtomic(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
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

		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var transitionErr, cancelErr error
		go func() {
			defer group.Done()
			<-start
			transitionErr = service.transition(job.ID, Running, "apply_fake_runtime", 70, `{"phase":"apply_fake_runtime"}`, "info", "phase_started", "Fake runtime: apply_fake_runtime")
		}()
		go func() {
			defer group.Done()
			<-start
			_, cancelErr = service.Cancel(job.ID)
		}()
		close(start)
		group.Wait()

		if cancelErr != nil {
			db.Close()
			t.Fatalf("attempt %d: cancellation failed: %v", attempt, cancelErr)
		}
		if transitionErr != nil && !errors.Is(transitionErr, ErrJobTerminal) {
			db.Close()
			t.Fatalf("attempt %d: transition error = %v", attempt, transitionErr)
		}
		persisted, err := service.Get(job.ID)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if persisted.Status != string(Cancelled) {
			db.Close()
			t.Fatalf("attempt %d: terminal status = %q", attempt, persisted.Status)
		}
		events, err := service.Events(job.ID, 0)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		cancelEvents := 0
		terminalEvents := 0
		for _, event := range events {
			switch event.Code {
			case "job_cancelled":
				cancelEvents++
				terminalEvents++
			case "job_succeeded", "job_failed", "daemon_restarted":
				terminalEvents++
			}
		}
		if cancelEvents != 1 || terminalEvents != 1 {
			db.Close()
			t.Fatalf("attempt %d: cancel events = %d, terminal events = %d: %#v", attempt, cancelEvents, terminalEvents, events)
		}
		if last := events[len(events)-1]; last.Code != "job_cancelled" {
			db.Close()
			t.Fatalf("attempt %d: terminal cancellation was not the final event: %#v", attempt, events)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerCompletionAndCancellationHaveOneTerminalOutcome(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
		db, err := database.Open(filepath.Join(t.TempDir(), "state"))
		if err != nil {
			t.Fatal(err)
		}
		service := New(db)
		job, _, err := service.Create("deploy", "application", "app-terminal-race", "terminal-race-request")
		if err != nil {
			db.Close()
			t.Fatal(err)
		}

		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var transitionErr, cancelErr error
		go func() {
			defer group.Done()
			<-start
			transitionErr = service.transition(job.ID, Succeeded, "succeeded", 100, `{"phase":"succeeded"}`, "info", "job_succeeded", "Fake deployment completed")
		}()
		go func() {
			defer group.Done()
			<-start
			_, cancelErr = service.Cancel(job.ID)
		}()
		close(start)
		group.Wait()

		transitionWon := transitionErr == nil && errors.Is(cancelErr, ErrJobTerminal)
		cancelWon := cancelErr == nil && errors.Is(transitionErr, ErrJobTerminal)
		if !transitionWon && !cancelWon {
			db.Close()
			t.Fatalf("attempt %d: transition error = %v, cancellation error = %v", attempt, transitionErr, cancelErr)
		}
		events, err := service.Events(job.ID, 0)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		terminalEvents := 0
		for _, event := range events {
			if event.Code == "job_cancelled" || event.Code == "job_succeeded" {
				terminalEvents++
			}
		}
		if terminalEvents != 1 {
			db.Close()
			t.Fatalf("attempt %d: terminal events = %d: %#v", attempt, terminalEvents, events)
		}
		last := events[len(events)-1]
		if last.Code != "job_cancelled" && last.Code != "job_succeeded" {
			db.Close()
			t.Fatalf("attempt %d: final event is not terminal: %#v", attempt, events)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
