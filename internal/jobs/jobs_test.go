package jobs_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
)

func TestIdempotencyEventsAndRecovery(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := jobs.New(db)
	j, created, err := s.Create("deploy", "application", "app-one", "same-request")
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := s.Create("deploy", "application", "app-one", "same-request")
	if err != nil || created || again.ID != j.ID {
		t.Fatal("idempotency failed")
	}
	if _, _, err := s.Create("restart", "application", "app-one", "other-request"); err == nil {
		t.Fatal("concurrent mutation accepted")
	}
	if err := s.Update(j.ID, jobs.Running, "apply", 40, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Get(j.ID)
	if err != nil || recovered.Status != string(jobs.Interrupted) {
		t.Fatalf("recovery: %#v %v", recovered, err)
	}
	events, err := s.Events(j.ID, 0)
	if err != nil || len(events) == 0 {
		t.Fatal("missing durable events")
	}
	if events[len(events)-1].Code != "daemon_restarted" {
		t.Fatalf("missing durable restart terminal event: %#v", events)
	}
	replayed, err := s.Events(j.ID, events[0].ID)
	if err != nil || len(replayed) != len(events)-1 || replayed[0].ID <= events[0].ID {
		t.Fatalf("event replay after ID failed: %#v %v", replayed, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.RunFakeWorker(ctx)
	next, _, err := s.Create("deploy", "application", "app-two", "new")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := s.Get(next.ID)
		if v.Status == string(jobs.Succeeded) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake worker did not complete job")
}

func TestCancellationIsAtomicAndEmitsOneTerminalEvent(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := jobs.New(db)
	job, _, err := service.Create("deploy", "application", "app-cancel", "cancel-request")
	if err != nil {
		t.Fatal(err)
	}
	const attempts = 8
	results := make(chan error, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.Cancel(job.ID)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	succeeded := 0
	conflicted := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, jobs.ErrJobTerminal):
			conflicted++
		default:
			t.Fatalf("unexpected cancellation error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != attempts-1 {
		t.Fatalf("cancellation results: %d succeeded, %d conflicted", succeeded, conflicted)
	}
	cancelled, err := service.Get(job.ID)
	if err != nil || cancelled.Status != string(jobs.Cancelled) {
		t.Fatalf("job was not cancelled: %#v %v", cancelled, err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	terminalEvents := 0
	for _, event := range events {
		if event.Code == "job_cancelled" {
			terminalEvents++
		}
	}
	if terminalEvents != 1 {
		t.Fatalf("cancel terminal event count = %d: %#v", terminalEvents, events)
	}
	if _, err := service.Cancel("missing"); !errors.Is(err, jobs.ErrJobNotFound) {
		t.Fatalf("missing cancellation error = %v", err)
	}
}
