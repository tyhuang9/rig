package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

type deploymentCreationTestValidator struct {
	db *sql.DB
}

func (validator deploymentCreationTestValidator) ConfiguredFor(db *sql.DB) bool {
	return validator.db == db
}

func (deploymentCreationTestValidator) HistoricalBindingTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) error {
	return nil
}

func (deploymentCreationTestValidator) CurrentOperationTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) (deploymentbinding.Completion, error) {
	return deploymentbinding.CompletionComplete, nil
}

func (deploymentCreationTestValidator) UnresolvedHistoryTx(context.Context, *sql.Tx, string) (bool, error) {
	return false, nil
}

func TestCreateDeploymentWithInputTransactionConcurrentReplayCreatesOneAtomicUnit(t *testing.T) {
	generic, closeDB := newTestService(t)
	defer closeDB()
	bindings, err := deploymentbinding.New(generic.db, deploymentCreationTestValidator{db: generic.db})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithDeploymentBindings(generic.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	service.wake = make(chan struct{}, 4)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = service.db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`,
		"typed-concurrent-app", "typed-concurrent-app", "Typed concurrent app", stamp, stamp); err != nil {
		t.Fatal(err)
	}

	request := DeploymentCreateRequest{
		ApplicationID: "typed-concurrent-app", IdempotencyKey: "typed-concurrent-key", RequestedBy: "actor",
		Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent},
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var validateNew, finalizeNew, validateReplay atomic.Int32
	hooks := DeploymentCreateHooks{
		ValidateNew: func(context.Context, *sql.Tx, Job) error {
			if validateNew.Add(1) == 1 {
				close(entered)
				<-release
			}
			return nil
		},
		FinalizeNew: func(context.Context, *sql.Tx, Job, deploymentbinding.Binding) error {
			finalizeNew.Add(1)
			return nil
		},
		ValidateReplay: func(context.Context, *sql.Tx, Job, deploymentbinding.Binding) error {
			validateReplay.Add(1)
			return nil
		},
	}
	type createResult struct {
		result DeploymentCreateResult
		err    error
	}
	results := make(chan createResult, 2)
	go func() {
		result, createErr := service.CreateDeploymentWithInputTransaction(context.Background(), request, hooks)
		results <- createResult{result: result, err: createErr}
	}()
	<-entered
	go func() {
		result, createErr := service.CreateDeploymentWithInputTransaction(context.Background(), request, hooks)
		results <- createResult{result: result, err: createErr}
	}()
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.result.Job.ID == "" || first.result.Job.ID != second.result.Job.ID ||
		!first.result.Binding.Equal(second.result.Binding) || first.result.Created == second.result.Created {
		t.Fatalf("concurrent results first=%#v second=%#v", first, second)
	}
	if validateNew.Load() != 1 || finalizeNew.Load() != 1 || validateReplay.Load() != 1 {
		t.Fatalf("hook counts validate-new=%d finalize-new=%d validate-replay=%d", validateNew.Load(), finalizeNew.Load(), validateReplay.Load())
	}
	var jobsCount, bindingsCount, queuedEvents int
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings WHERE job_id=?`, first.result.Job.ID).Scan(&bindingsCount); err != nil {
		t.Fatal(err)
	}
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='job_queued'`, first.result.Job.ID).Scan(&queuedEvents); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 1 || bindingsCount != 1 || queuedEvents != 1 || len(service.wake) != 1 {
		t.Fatalf("atomic units jobs=%d bindings=%d queued-events=%d wakes=%d", jobsCount, bindingsCount, queuedEvents, len(service.wake))
	}
	if _, err = service.Cancel(first.result.Job.ID); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Get(first.result.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminalEvents, err := service.Events(first.result.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	wakesBeforeReplay := len(service.wake)
	terminalReplay, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, hooks)
	if err != nil || terminalReplay.Created || terminalReplay.Job.ID != terminal.ID || !terminalReplay.Binding.Equal(first.result.Binding) {
		t.Fatalf("terminal replay=%#v error=%v", terminalReplay, err)
	}
	afterEvents, err := service.Events(first.result.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEvents) != len(terminalEvents) || len(service.wake) != wakesBeforeReplay || validateReplay.Load() != 2 {
		t.Fatalf("terminal replay events=%d/%d wakes=%d/%d replay-validations=%d", len(terminalEvents), len(afterEvents), wakesBeforeReplay, len(service.wake), validateReplay.Load())
	}
}

func TestCreateDeploymentWithInputTransactionBindingFailureRollsBackWithoutWake(t *testing.T) {
	generic, closeDB := newTestService(t)
	defer closeDB()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	const appID = "typed-stopped-app"
	if _, err := generic.db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, appID, "Typed stopped app", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	operation, created, err := generic.CreateWithInput(CreateRequest{
		Type: "stop", ResourceType: "application", ResourceID: appID, IdempotencyKey: "stop-authority", RequestedBy: "actor", Input: NoInput{},
	})
	if err != nil || !created {
		t.Fatalf("operation=%#v created=%t error=%v", operation, created, err)
	}
	if _, err = generic.db.Exec(`INSERT INTO application_desired_lifecycle_transitions(
		app_id,revision_number,expected_revision,previous_state,desired_state,job_id,action,actor_id,created_at
	) VALUES(?,1,0,NULL,'stopped',?,'stop','actor',?)`, appID, operation.ID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = generic.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, operation.ID); err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(generic.db, deploymentCreationTestValidator{db: generic.db})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithDeploymentBindings(generic.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	service.wake = make(chan struct{}, 4)
	const key = "stopped-refusal"
	if _, err = service.CreateDeploymentWithInputTransaction(context.Background(), DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: key, RequestedBy: "actor", Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent},
	}, DeploymentCreateHooks{}); !errors.Is(err, deploymentbinding.ErrInvalidBinding) {
		t.Fatalf("binding failure=%v", err)
	}
	var jobsCount, bindingsCount, eventsCount int
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, key).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings b JOIN jobs j ON j.id=b.job_id WHERE j.idempotency_key=?`, key).Scan(&bindingsCount); err != nil {
		t.Fatal(err)
	}
	if err = service.db.QueryRow(`SELECT COUNT(*) FROM job_events e JOIN jobs j ON j.id=e.job_id WHERE j.idempotency_key=?`, key).Scan(&eventsCount); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 0 || bindingsCount != 0 || eventsCount != 0 || len(service.wake) != 0 {
		t.Fatalf("refused create jobs=%d bindings=%d events=%d wakes=%d", jobsCount, bindingsCount, eventsCount, len(service.wake))
	}
}
