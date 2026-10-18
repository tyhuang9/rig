package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/jobs"
)

func TestNewWithDeploymentBindingsRejectsMissingAndForeignDependencies(t *testing.T) {
	db, service, bindings, _ := deploymentJobFixture(t)
	if service == nil || !bindings.UsesDatabase(db) {
		t.Fatal("configured deployment job service was not constructed")
	}
	if _, err := jobs.NewWithDeploymentBindings(nil, bindings); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) {
		t.Fatalf("nil database error = %v", err)
	}
	if _, err := jobs.NewWithDeploymentBindings(db, nil); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) {
		t.Fatalf("nil repository error = %v", err)
	}
	foreign, err := database.Open(filepath.Join(t.TempDir(), "foreign"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	if _, err := jobs.NewWithDeploymentBindings(foreign, bindings); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) {
		t.Fatalf("foreign repository error = %v", err)
	}
	if _, err := jobs.New(db).CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: "unconfigured", Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{}); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) {
		t.Fatalf("generic service deployment creation error = %v", err)
	}
}

func TestCreateDeploymentWithInputTransactionCreatesAndReplaysImmutableBinding(t *testing.T) {
	_, service, bindings, appID := deploymentJobFixture(t)
	request := jobs.DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "deployment-request", RequestedBy: "actor-one",
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}
	var validateNew, finalizeNew, validateReplay int
	result, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, jobs.DeploymentCreateHooks{
		ValidateNew: func(ctx context.Context, tx *sql.Tx, job jobs.Job) error {
			validateNew++
			var bindings int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_deployment_lifecycle_bindings WHERE job_id=?`, job.ID).Scan(&bindings); err != nil {
				return err
			}
			if bindings != 0 {
				t.Fatalf("binding existed before new validation")
			}
			return nil
		},
		FinalizeNew: func(ctx context.Context, tx *sql.Tx, job jobs.Job, binding deploymentbinding.Binding) error {
			finalizeNew++
			persisted, err := bindings.ReadTx(ctx, tx, job.ID)
			if err != nil {
				return err
			}
			if !persisted.Equal(binding) {
				t.Fatal("finalizer did not receive the inserted binding")
			}
			return nil
		},
		ValidateReplay: func(context.Context, *sql.Tx, jobs.Job, deploymentbinding.Binding) error {
			validateReplay++
			return nil
		},
	})
	if err != nil || !result.Created || result.Job.ID == "" || result.Binding.JobID() != result.Job.ID ||
		result.Binding.ApplicationID() != appID || result.Binding.Mode() != deploymentbinding.ModeLegacyUninitialized {
		t.Fatalf("create result=%#v binding=%q/%q/%q err=%v", result.Job, result.Binding.JobID(), result.Binding.ApplicationID(), result.Binding.Mode(), err)
	}
	if validateNew != 1 || finalizeNew != 1 || validateReplay != 0 {
		t.Fatalf("new hook counts validate=%d finalize=%d replay=%d", validateNew, finalizeNew, validateReplay)
	}
	if _, err := service.Cancel(result.Job.ID); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.Get(result.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := service.Events(result.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, jobs.DeploymentCreateHooks{
		ValidateNew: func(context.Context, *sql.Tx, jobs.Job) error {
			validateNew++
			return nil
		},
		FinalizeNew: func(context.Context, *sql.Tx, jobs.Job, deploymentbinding.Binding) error {
			finalizeNew++
			return nil
		},
		ValidateReplay: func(_ context.Context, _ *sql.Tx, job jobs.Job, binding deploymentbinding.Binding) error {
			validateReplay++
			if job.ID != result.Job.ID || !binding.Equal(result.Binding) {
				t.Fatal("replay did not retain exact job and binding")
			}
			return nil
		},
	})
	if err != nil || replayed.Created || replayed.Job.ID != result.Job.ID || !replayed.Binding.Equal(result.Binding) {
		t.Fatalf("replay result=%#v binding=%q created=%t err=%v", replayed.Job, replayed.Binding.JobID(), replayed.Created, err)
	}
	if validateNew != 1 || finalizeNew != 1 || validateReplay != 1 {
		t.Fatalf("replay hook counts validate=%d finalize=%d replay=%d", validateNew, finalizeNew, validateReplay)
	}
	after, err := service.Get(result.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterEvents, err := service.Events(result.Job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != terminal.Status || !after.UpdatedAt.Equal(terminal.UpdatedAt) || len(afterEvents) != len(beforeEvents) {
		t.Fatalf("replay mutated terminal job before=%#v/%d after=%#v/%d", terminal, len(beforeEvents), after, len(afterEvents))
	}

	if _, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: request.IdempotencyKey, RequestedBy: "different-actor", Input: request.Input,
	}, jobs.DeploymentCreateHooks{}); !errors.Is(err, jobs.ErrIdempotency) {
		t.Fatalf("changed actor replay error = %v", err)
	}
}

func TestCreateDeploymentWithInputTransactionRefusesUnboundReplay(t *testing.T) {
	db, service, _, appID := deploymentJobFixture(t)
	request := jobs.DeploymentCreateRequest{ApplicationID: appID, IdempotencyKey: "unbound", RequestedBy: "actor", Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}}
	generic := jobs.New(db)
	job, created, err := generic.CreateWithInput(jobs.CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: appID, IdempotencyKey: request.IdempotencyKey,
		RequestedBy: request.RequestedBy, Input: request.Input,
	})
	if err != nil || !created {
		t.Fatalf("seed unbound job=%#v created=%t err=%v", job, created, err)
	}
	if _, err := service.CreateDeploymentWithInputTransaction(context.Background(), request, jobs.DeploymentCreateHooks{}); !errors.Is(err, deploymentbinding.ErrBindingNotFound) {
		t.Fatalf("unbound replay error = %v", err)
	}
}

func TestCreateDeploymentWithInputTransactionRollsBackNewFailures(t *testing.T) {
	for _, stage := range []string{"validation", "finalization"} {
		t.Run(stage, func(t *testing.T) {
			db, service, _, appID := deploymentJobFixture(t)
			failure := errors.New("fixture failure")
			hooks := jobs.DeploymentCreateHooks{}
			if stage == "validation" {
				hooks.ValidateNew = func(context.Context, *sql.Tx, jobs.Job) error { return failure }
			} else {
				hooks.FinalizeNew = func(context.Context, *sql.Tx, jobs.Job, deploymentbinding.Binding) error { return failure }
			}
			_, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
				ApplicationID: appID, IdempotencyKey: "rollback-" + stage, RequestedBy: "actor",
				Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
			}, hooks)
			if !errors.Is(err, failure) {
				t.Fatalf("create error = %v", err)
			}
			var jobsCount, bindingCount, eventCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE idempotency_key=?`, "rollback-"+stage).Scan(&jobsCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings`).Scan(&bindingCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM job_events`).Scan(&eventCount); err != nil {
				t.Fatal(err)
			}
			if jobsCount != 0 || bindingCount != 0 || eventCount != 0 {
				t.Fatalf("rollback retained jobs=%d bindings=%d events=%d", jobsCount, bindingCount, eventCount)
			}
		})
	}
}

func deploymentJobFixture(t *testing.T) (*sql.DB, *jobs.Service, *deploymentbinding.Repository, string) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	validator, err := applifecycle.NewDeploymentBindingValidator(applifecycle.New(db))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	appID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, "fixture-"+appID, "Fixture", now, now); err != nil {
		t.Fatal(err)
	}
	return db, service, bindings, appID
}
