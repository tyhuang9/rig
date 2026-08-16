package jobs_test

import (
	"context"
	"path/filepath"
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
