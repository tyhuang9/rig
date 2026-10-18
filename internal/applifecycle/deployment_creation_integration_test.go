package applifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/jobs"
)

func TestTypedDeploymentCreationRetainsRunningAuthorityAcrossDesiredDrift(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	authorityRequest := fixture.request(Start, BaselineRunning, "typed-running-authority", 0)
	authority, err := fixture.repo.Accept(context.Background(), fixture.jobs, authorityRequest, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	claim := runningClaim(t, fixture, authority.Operation.JobID)
	runCompleteGraph(t, fixture, claim, authority.Operation)
	stamp := operationStamp()
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`,
		stamp, stamp, authority.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	validator, err := NewDeploymentBindingValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(fixture.db, validator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(fixture.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	request := jobs.DeploymentCreateRequest{
		ApplicationID: fixture.appID, IdempotencyKey: "typed-running-deployment", RequestedBy: fixture.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}
	created, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created || created.Binding.Mode() != deploymentbinding.ModeRunning ||
		created.Binding.AuthorizingLifecycleJobID() != authority.Operation.JobID || created.Binding.DesiredRevision() != 1 {
		t.Fatalf("created=%#v binding=%q/%d/%q error=%v", created.Job, created.Binding.Mode(), created.Binding.DesiredRevision(), created.Binding.AuthorizingLifecycleJobID(), err)
	}
	queued, err := service.Get(created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	queuedEvents, err := service.Events(created.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}

	driftRequest := fixture.request(Restart, BaselineRunning, "typed-running-drift", 1)
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`,
		stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if drift, driftErr := fixture.repo.Accept(context.Background(), fixture.jobs, driftRequest, fixture.pins); driftErr != nil || !drift.Created {
		t.Fatalf("drift=%#v error=%v", drift, driftErr)
	} else if _, driftErr = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`,
		stamp, stamp, drift.Operation.JobID); driftErr != nil {
		t.Fatal(driftErr)
	}
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='queued',phase='queued',finished_at=NULL,updated_at=? WHERE id=?`,
		queued.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), created.Job.ID); err != nil {
		t.Fatal(err)
	}
	queuedReplay, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, jobs.DeploymentCreateHooks{})
	if err != nil || queuedReplay.Created || !queuedReplay.Binding.Equal(created.Binding) || !reflect.DeepEqual(queuedReplay.Job, queued) {
		t.Fatalf("queued replay=%#v binding-equal=%t error=%v", queuedReplay.Job, queuedReplay.Binding.Equal(created.Binding), err)
	}
	afterQueuedEvents, err := service.Events(created.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterQueuedEvents, queuedEvents) {
		t.Fatalf("queued replay mutated events=%t", !reflect.DeepEqual(afterQueuedEvents, queuedEvents))
	}

	if _, err = service.Cancel(created.Job.ID); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Get(created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminalEvents, err := service.Events(created.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	terminalReplay, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, jobs.DeploymentCreateHooks{})
	if err != nil || terminalReplay.Created || !terminalReplay.Binding.Equal(created.Binding) || !reflect.DeepEqual(terminalReplay.Job, terminal) {
		t.Fatalf("terminal replay=%#v binding-equal=%t error=%v", terminalReplay.Job, terminalReplay.Binding.Equal(created.Binding), err)
	}
	afterTerminalEvents, err := service.Events(created.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterTerminalEvents, terminalEvents) {
		t.Fatalf("terminal replay mutated events=%t", !reflect.DeepEqual(afterTerminalEvents, terminalEvents))
	}

	assertTypedDeploymentCreationRefused(t, fixture, service, "typed-unresolved-deployment", deploymentbinding.ErrLifecycleUnresolved)
}

func TestTypedDeploymentCreationRefusesStoppedAuthorityWithoutWrites(t *testing.T) {
	fixture := newOperationFixture(t, 1)
	startRequest := fixture.request(Start, BaselineRunning, "typed-stopped-start", 0)
	started, err := fixture.repo.Accept(context.Background(), fixture.jobs, startRequest, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	startClaim := runningClaim(t, fixture, started.Operation.JobID)
	runCompleteGraph(t, fixture, startClaim, started.Operation)
	stamp := operationStamp()
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`,
		stamp, stamp, started.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	stopRequest := fixture.request(Stop, BaselineRunning, "typed-stopped-authority", 1)
	stopped, err := fixture.repo.Accept(context.Background(), fixture.jobs, stopRequest, fixture.pins)
	if err != nil {
		t.Fatal(err)
	}
	stopClaim := runningClaim(t, fixture, stopped.Operation.JobID)
	runCompleteGraph(t, fixture, stopClaim, stopped.Operation)
	if _, err = fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`,
		stamp, stamp, stopped.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	validator, err := NewDeploymentBindingValidator(fixture.repo)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(fixture.db, validator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(fixture.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	assertTypedDeploymentCreationRefused(t, fixture, service, "typed-stopped-deployment", deploymentbinding.ErrInvalidBinding)
}

func assertTypedDeploymentCreationRefused(t *testing.T, fixture *operationFixture, service *jobs.Service, key string, want error) {
	t.Helper()
	_, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: fixture.appID, IdempotencyKey: key, RequestedBy: fixture.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if !errors.Is(err, want) {
		t.Fatalf("create error=%v want=%v", err, want)
	}
	var jobsCount, bindingsCount, eventsCount int
	if err = fixture.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, key).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings b JOIN jobs j ON j.id=b.job_id WHERE j.idempotency_key=?`, key).Scan(&bindingsCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.db.QueryRow(`SELECT COUNT(*) FROM job_events e JOIN jobs j ON j.id=e.job_id WHERE j.idempotency_key=?`, key).Scan(&eventsCount); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 0 || bindingsCount != 0 || eventsCount != 0 {
		t.Fatalf("refused create jobs=%d bindings=%d events=%d", jobsCount, bindingsCount, eventsCount)
	}
}
