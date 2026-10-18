package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestCreateWithInputTransactionOrdersReplayBeforeJobMutation(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	request := CreateRequest{Type: "start", ResourceType: "application", ResourceID: "app", IdempotencyKey: "key", RequestedBy: "actor", Input: NoInput{}}
	beforeCalls, finalizeCalls := 0, 0
	result, err := service.CreateWithInputTransaction(context.Background(), request, CreateTransactionHooks{
		BeforeCreate: func(ctx context.Context, tx *sql.Tx) (bool, error) {
			beforeCalls++
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&count); err != nil || count != 0 {
				t.Fatalf("pre-create job count=%d error=%v", count, err)
			}
			return false, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, job Job) error {
			finalizeCalls++
			var events int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='job_queued'`, job.ID).Scan(&events); err != nil || events != 1 {
				t.Fatalf("finalize events=%d error=%v", events, err)
			}
			return nil
		},
	})
	if err != nil || !result.Created || result.Handled || result.Job.ID == "" || beforeCalls != 1 || finalizeCalls != 1 || len(service.wake) != 1 {
		t.Fatalf("result=%#v before=%d finalize=%d wake=%d error=%v", result, beforeCalls, finalizeCalls, len(service.wake), err)
	}
}

func TestCreateWithInputTransactionHandledReplayCreatesNothingAndDoesNotWake(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	result, err := service.CreateWithInputTransaction(context.Background(), CreateRequest{
		Type: "stop", ResourceType: "application", ResourceID: "app", IdempotencyKey: "historical", RequestedBy: "actor", Input: NoInput{},
	}, CreateTransactionHooks{BeforeCreate: func(context.Context, *sql.Tx) (bool, error) { return true, nil }})
	if err != nil || !result.Handled || result.Created || result.Job.ID != "" || len(service.wake) != 0 {
		t.Fatalf("handled result=%#v wake=%d error=%v", result, len(service.wake), err)
	}
	var jobs, events int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM job_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || events != 0 {
		t.Fatalf("handled replay wrote jobs=%d events=%d", jobs, events)
	}
}

func TestCreateWithInputTransactionCancellationRollsBackAndPreservesNoWake(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := service.CreateWithInputTransaction(ctx, CreateRequest{
		Type: "restart", ResourceType: "application", ResourceID: "app", IdempotencyKey: "cancelled", RequestedBy: "actor", Input: NoInput{},
	}, CreateTransactionHooks{BeforeCreate: func(context.Context, *sql.Tx) (bool, error) {
		cancel()
		return false, nil
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	var jobs, events int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM job_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || events != 0 || len(service.wake) != 0 {
		t.Fatalf("cancelled create wrote jobs=%d events=%d wake=%d", jobs, events, len(service.wake))
	}
}

func TestCreateWithInputTransactionExistingHookPrecedesMutableReplayComparison(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	request := CreateRequest{Type: "start", ResourceType: "application", ResourceID: "app", IdempotencyKey: "existing", RequestedBy: "actor", Input: NoInput{}}
	created, wasCreated, err := service.CreateWithInput(request)
	if err != nil || !wasCreated {
		t.Fatalf("create=%#v created=%t error=%v", created, wasCreated, err)
	}
	<-service.wake
	if _, err := service.db.Exec(`UPDATE jobs SET requested_by='mutable-drift' WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	result, err := service.CreateWithInputTransaction(context.Background(), request, CreateTransactionHooks{
		Existing: func(_ context.Context, _ *sql.Tx, job Job) (bool, error) {
			calls++
			if job.ID != created.ID || job.RequestedBy != "mutable-drift" {
				t.Fatalf("existing hook job=%#v", job)
			}
			return true, nil
		},
	})
	if err != nil || !result.Handled || result.Created || result.Job.ID != created.ID || calls != 1 || len(service.wake) != 0 {
		t.Fatalf("result=%#v calls=%d wake=%d error=%v", result, calls, len(service.wake), err)
	}
}
