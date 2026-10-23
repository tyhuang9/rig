package generatedrecovery_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"modernc.org/sqlite"
)

func TestRecoverRuntimeRetainsStaleDeploymentEvidenceAndRetiresJobOnce(t *testing.T) {
	fixture := newRecoveryFixture(t, true)
	fixture.advance(t, "building")
	fixture.imageReady(t)
	fixture.advance(t, "migrating")
	if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	evidenceBefore := recoveryEvidenceSnapshot(t, fixture.db)
	makeRecoveryApplicationAbsent(t, fixture.db, fixture.appID)

	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deployments.RetainedStale != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RetiredStale != 1 {
		t.Fatalf("recovery result = %#v", result)
	}
	if got := recoveryEvidenceSnapshot(t, fixture.db); got != evidenceBefore {
		t.Fatalf("stale deployment evidence changed\nbefore: %s\nafter:  %s", evidenceBefore, got)
	}
	assertStaleRecoveryJob(t, fixture.db, fixture.jobID, 2, 60, `{"phase":"apply_runtime"}`)
	assertEventCount(t, fixture.db, fixture.jobID, "deployment_lifecycle_stale", 1)

	repeat, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(2*time.Minute), fixture.bindings)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Deployments.RetainedStale != 1 || repeat.Jobs.RetiredStale != 0 {
		t.Fatalf("repeat recovery result = %#v", repeat)
	}
	if got := recoveryEvidenceSnapshot(t, fixture.db); got != evidenceBefore {
		t.Fatalf("repeat changed stale deployment evidence\nbefore: %s\nafter:  %s", evidenceBefore, got)
	}
	assertEventCount(t, fixture.db, fixture.jobID, "deployment_lifecycle_stale", 1)
}

func TestRecoverRuntimeStaleCensusIncludesQueuedWaitingAndTerminalRuntime(t *testing.T) {
	tests := []struct {
		name          string
		activation    bool
		prepare       func(*testing.T, *recoveryFixture)
		wantRetired   int
		wantJobStatus string
	}{
		{
			name: "queued",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				if _, err := fixture.db.Exec(`UPDATE jobs SET status='queued',phase='queued',started_at=NULL WHERE id=?`, fixture.jobID); err != nil {
					t.Fatal(err)
				}
			},
			wantRetired: 1, wantJobStatus: "needs_attention",
		},
		{
			name: "waiting user with succeeded runtime", activation: true,
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.toSwitching(t)
				fixture.activate(t)
				fixture.advance(t, "succeeded")
				if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='route_reconciliation_required',pause_disposition='route_reconciliation_required' WHERE id=?`, fixture.jobID); err != nil {
					t.Fatal(err)
				}
			},
			wantRetired: 1, wantJobStatus: "needs_attention",
		},
		{
			name: "terminal job with nonterminal runtime",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), fixture.jobID); err != nil {
					t.Fatal(err)
				}
			},
			wantRetired: 0, wantJobStatus: "succeeded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var fixture *recoveryFixture
			if test.activation {
				fixture = newRecoveryActivationFixture(t, false)
			} else {
				fixture = newRecoveryFixture(t, false)
			}
			test.prepare(t, fixture)
			before := recoveryEvidenceSnapshot(t, fixture.db)
			mutableBefore := recoveryMutableSnapshot(t, fixture.db)
			makeRecoveryApplicationAbsent(t, fixture.db, fixture.appID)
			result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deployments.RetainedStale != 1 || result.Jobs.RetiredStale != test.wantRetired {
				t.Fatalf("recovery result = %#v", result)
			}
			if got := recoveryEvidenceSnapshot(t, fixture.db); got != before {
				t.Fatalf("stale evidence changed\nbefore: %s\nafter:  %s", before, got)
			}
			var status string
			if err := fixture.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, fixture.jobID).Scan(&status); err != nil || status != test.wantJobStatus {
				t.Fatalf("job status=%q error=%v", status, err)
			}
			if test.wantRetired == 0 {
				if after := recoveryMutableSnapshot(t, fixture.db); after != mutableBefore {
					t.Fatalf("terminal stale job or events changed\nbefore: %s\nafter: %s", mutableBefore, after)
				}
			}
		})
	}
}

func TestRecoverRuntimeRetiresStaleQueuedJobBeforeDeploymentExists(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "queued-stale"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lifecycleRepository := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycleRepository)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	appID, jobID := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, appID, timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,input_json,created_at,updated_at)
		VALUES(?,'deploy','application',?,'queued','queued','{}',?,?)`, jobID, appID, timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.InsertNewTx(context.Background(), tx, jobID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	makeRecoveryApplicationAbsent(t, db, appID)
	result, err := generatedrecovery.RecoverRuntime(context.Background(), db, now.Add(time.Minute), bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deployments != (generatedrecovery.DeploymentResult{}) || result.Jobs.RetiredStale != 1 {
		t.Fatalf("recovery result=%#v", result)
	}
	assertStaleRecoveryJob(t, db, jobID, 0, 0, `{}`)
	assertEventCount(t, db, jobID, "deployment_lifecycle_stale", 1)
}

func TestRecoverRuntimePreservesStaleComposeDeployment(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	composeDeploymentID, composeJobID := fixture.seedComposeDeployment(t)
	var composeAppID string
	if err := fixture.db.QueryRow(`SELECT resource_id FROM jobs WHERE id=?`, composeJobID).Scan(&composeAppID); err != nil {
		t.Fatal(err)
	}
	before := recoveryDeploymentRow(t, fixture.db, composeDeploymentID)
	makeRecoveryApplicationAbsent(t, fixture.db, composeAppID)
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deployments.RetainedStale != 1 || result.Jobs.RetiredStale != 1 {
		t.Fatalf("recovery result = %#v", result)
	}
	if after := recoveryDeploymentRow(t, fixture.db, composeDeploymentID); after != before {
		t.Fatalf("stale compose deployment changed\nbefore: %s\nafter:  %s", before, after)
	}
	assertEventCount(t, fixture.db, composeJobID, "deployment_lifecycle_stale", 1)
}

func TestRecoverRuntimeClassifiesAllSubjectsBeforeWrites(t *testing.T) {
	fixture := newRecoveryFixture(t, true)
	fixture.advance(t, "building")
	fixture.imageReady(t)
	fixture.advance(t, "migrating")
	if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	seedRecoveryQueuedJob(t, fixture, "zz-corrupt-app", "zz-corrupt-job")
	seedRecoveryQueuedJob(t, fixture, "aa-stale-app", "aa-stale-job")
	makeRecoveryApplicationAbsent(t, fixture.db, "aa-stale-app")
	if _, err := fixture.db.Exec(`DROP TRIGGER application_deployment_lifecycle_binding_retain`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM application_deployment_lifecycle_bindings WHERE job_id='zz-corrupt-job'`); err != nil {
		t.Fatal(err)
	}
	before := recoveryMutableSnapshot(t, fixture.db)
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if !errors.Is(err, deploymentbinding.ErrBindingNotFound) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("recovery result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("classification failure changed recovery state\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRecoverRuntimeRejectsUnclassifiedDeploymentLinkage(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *recoveryFixture)
	}{
		{
			name: "orphan runtime deployment ID",
			mutate: func(t *testing.T, fixture *recoveryFixture) {
				if _, err := fixture.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`DELETE FROM deployments WHERE id=?`, fixture.deploymentID); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
				var runtimes int
				if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_deployments WHERE deployment_id=?`, fixture.deploymentID).Scan(&runtimes); err != nil || runtimes != 1 {
					t.Fatalf("orphan runtime count=%d error=%v", runtimes, err)
				}
			},
		},
		{
			name: "missing deployment job",
			mutate: func(t *testing.T, fixture *recoveryFixture) {
				if _, err := fixture.db.Exec(`UPDATE deployments SET job_id=NULL WHERE id=?`, fixture.deploymentID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "foreign deployment job",
			mutate: func(t *testing.T, fixture *recoveryFixture) {
				if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,input_json,created_at,updated_at)
					VALUES('foreign-job','maintenance','machine','local','succeeded','succeeded','{}',?,?)`, timestamp(fixture.now), timestamp(fixture.now)); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`UPDATE deployments SET job_id='foreign-job' WHERE id=?`, fixture.deploymentID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, false)
			for _, trigger := range []string{"deployment_identity_immutable", "deployment_linkage_valid_update"} {
				if _, err := fixture.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
					t.Fatal(err)
				}
			}
			test.mutate(t, fixture)
			before := recoveryMutableSnapshot(t, fixture.db)
			result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
			if !errors.Is(err, deploymentbinding.ErrInvalidBinding) || result != (generatedrecovery.RuntimeResult{}) {
				t.Fatalf("recovery result=%#v error=%v", result, err)
			}
			if after := recoveryMutableSnapshot(t, fixture.db); after != before {
				t.Fatalf("invalid linkage changed recovery state\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestRecoverRuntimeUnavailableStorageRollsBack(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	_, staleJobID := fixture.seedComposeDeployment(t)
	var staleAppID string
	if err := fixture.db.QueryRow(`SELECT resource_id FROM jobs WHERE id=?`, staleJobID).Scan(&staleAppID); err != nil {
		t.Fatal(err)
	}
	makeRecoveryApplicationAbsent(t, fixture.db, staleAppID)
	before := recoveryMutableSnapshot(t, fixture.db)
	if _, err := fixture.db.Exec(`DROP TABLE application_deployment_lifecycle_bindings`); err != nil {
		t.Fatal(err)
	}
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if !errors.Is(err, deploymentbinding.ErrUnavailable) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("recovery result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("unavailable storage changed recovery state\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRecoverRuntimeEventFailureRollsBackCurrentAndStaleWork(t *testing.T) {
	fixture := newRecoveryFixture(t, true)
	fixture.advance(t, "building")
	fixture.imageReady(t)
	fixture.advance(t, "migrating")
	if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	_, staleJobID := fixture.seedComposeDeployment(t)
	var staleAppID string
	if err := fixture.db.QueryRow(`SELECT resource_id FROM jobs WHERE id=?`, staleJobID).Scan(&staleAppID); err != nil {
		t.Fatal(err)
	}
	makeRecoveryApplicationAbsent(t, fixture.db, staleAppID)
	if _, err := fixture.db.Exec(`CREATE TRIGGER fail_stale_recovery_event BEFORE INSERT ON job_events
		WHEN NEW.code='deployment_lifecycle_stale' BEGIN SELECT RAISE(ABORT,'test stale event failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := recoveryMutableSnapshot(t, fixture.db)
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if !errors.Is(err, deploymentbinding.ErrUnavailable) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("recovery result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("event failure did not roll back all work\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRecoverRuntimeCurrentJobUpdateFailureRollsBackCurrentAndStaleWork(t *testing.T) {
	fixture := newRecoveryFixture(t, true)
	fixture.advance(t, "building")
	fixture.imageReady(t)
	fixture.advance(t, "migrating")
	if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	_, staleJobID := fixture.seedComposeDeployment(t)
	var staleAppID string
	if err := fixture.db.QueryRow(`SELECT resource_id FROM jobs WHERE id=?`, staleJobID).Scan(&staleAppID); err != nil {
		t.Fatal(err)
	}
	makeRecoveryApplicationAbsent(t, fixture.db, staleAppID)
	if _, err := fixture.db.Exec(`CREATE TRIGGER fail_current_recovery_update BEFORE UPDATE ON jobs
		WHEN OLD.id='` + fixture.jobID + `' AND NEW.status='interrupted'
		BEGIN SELECT RAISE(ABORT,'test current job update failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := recoveryMutableSnapshot(t, fixture.db)
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if !errors.Is(err, deploymentbinding.ErrUnavailable) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("recovery result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("current job update failure did not roll back all work\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRecoverRuntimeCancellationAfterMutationRollsBackAllWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	functionName := "cancel_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var observedPhase string
	var calls int
	if err := sqlite.RegisterScalarFunction(functionName, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		calls++
		observedPhase, _ = args[0].(string)
		cancel()
		return int64(0), nil
	}); err != nil {
		t.Fatal(err)
	}
	fixture := newRecoveryFixture(t, true)
	fixture.advance(t, "building")
	fixture.imageReady(t)
	fixture.advance(t, "migrating")
	if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	_, staleJobID := fixture.seedComposeDeployment(t)
	var staleAppID string
	if err := fixture.db.QueryRow(`SELECT resource_id FROM jobs WHERE id=?`, staleJobID).Scan(&staleAppID); err != nil {
		t.Fatal(err)
	}
	makeRecoveryApplicationAbsent(t, fixture.db, staleAppID)
	if _, err := fixture.db.Exec(`CREATE TRIGGER cancel_after_stale_recovery_update AFTER UPDATE ON jobs
		WHEN NEW.error_code='deployment_lifecycle_stale'
		BEGIN SELECT ` + functionName + `((SELECT phase FROM generated_runtime_deployments WHERE deployment_id='` + fixture.deploymentID + `')); END`); err != nil {
		t.Fatal(err)
	}
	before := recoveryMutableSnapshot(t, fixture.db)
	result, err := generatedrecovery.RecoverRuntime(ctx, fixture.db, fixture.now.Add(time.Minute), fixture.bindings)
	if !errors.Is(err, deploymentbinding.ErrCanceled) || !errors.Is(err, context.Canceled) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("post-mutation cancellation result=%#v error=%v", result, err)
	}
	if calls != 1 || observedPhase != "failed" {
		t.Fatalf("cancellation hook calls=%d observed current runtime phase=%q", calls, observedPhase)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("post-mutation cancellation did not roll back all work\nbefore: %s\nafter: %s", before, after)
	}
}

func TestLegacyRecoveryWrappersRefuseDeploymentWorkAndKeepGenericBehavior(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	before := recoveryMutableSnapshot(t, fixture.db)
	if result, err := generatedrecovery.RecoverDeployments(context.Background(), fixture.db, fixture.now); !errors.Is(err, generatedrecovery.ErrGuardedRecoveryRequired) || result != (generatedrecovery.DeploymentResult{}) {
		t.Fatalf("deployment wrapper result=%#v error=%v", result, err)
	}
	if result, err := generatedrecovery.RecoverJobs(context.Background(), fixture.db, fixture.now); !errors.Is(err, generatedrecovery.ErrGuardedRecoveryRequired) || result != (generatedrecovery.JobResult{}) {
		t.Fatalf("job wrapper result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("legacy wrappers changed deployment work\nbefore: %s\nafter:  %s", before, after)
	}

	db, err := database.Open(filepath.Join(t.TempDir(), "generic"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,input_json,created_at,updated_at)
		VALUES('generic','maintenance','machine','local','running','working','{}',?,?)`, timestamp(now), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	result, err := generatedrecovery.RecoverJobs(context.Background(), db, now.Add(time.Minute))
	if err != nil || result.Interrupted != 1 {
		t.Fatalf("generic recovery result=%#v error=%v", result, err)
	}
}

func TestRecoverRuntimeRejectsMissingOrForeignDependenciesAndCancellation(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	before := recoveryMutableSnapshot(t, fixture.db)
	if _, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now, nil); !errors.Is(err, generatedrecovery.ErrInvalidRecovery) {
		t.Fatalf("nil bindings error=%v", err)
	}
	foreign := newRecoveryFixture(t, false)
	if _, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now, foreign.bindings); !errors.Is(err, generatedrecovery.ErrInvalidRecovery) {
		t.Fatalf("foreign bindings error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generatedrecovery.RecoverRuntime(ctx, fixture.db, fixture.now, fixture.bindings); !errors.Is(err, deploymentbinding.ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery error=%v", err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("invalid dependency or cancellation changed state\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRecoverRuntimeUsesOneWriterTransactionForCanonicalValidation(t *testing.T) {
	fixture := newRecoveryActivationFixture(t, false)
	repository, recorder, jobID := seedCanonicalRunningRecoveryJob(t, fixture)
	recorder.transactions = nil
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), repository)
	if err != nil {
		t.Fatal(err)
	}
	if result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("queued current recovery result=%#v", result)
	}
	if len(recorder.transactions) < 4 {
		t.Fatalf("canonical validator calls=%d, want historical/read/check/current/history calls", len(recorder.transactions))
	}
	for index := 1; index < len(recorder.transactions); index++ {
		if recorder.transactions[index] != recorder.transactions[0] {
			t.Fatalf("validator transaction changed at call %d", index)
		}
	}
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, jobID).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("queued current job status=%q error=%v", status, err)
	}

	currentDeploymentID, _ := fixture.seedComposeDeployment(t)
	_, staleJobID := fixture.seedComposeDeployment(t)
	var staleAppID string
	if err := fixture.db.QueryRow(`SELECT resource_id FROM jobs WHERE id=?`, staleJobID).Scan(&staleAppID); err != nil {
		t.Fatal(err)
	}
	makeRecoveryApplicationAbsent(t, fixture.db, staleAppID)
	before := recoveryMutableSnapshot(t, fixture.db)
	for _, failure := range []error{deploymentbinding.ErrLifecycleUnresolved, deploymentbinding.ErrUnavailable} {
		recorder.historyFailure = failure
		if result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(2*time.Minute), repository); !errors.Is(err, failure) || result != (generatedrecovery.RuntimeResult{}) {
			t.Fatalf("late authority failure=%v result=%#v error=%v", failure, result, err)
		}
		if after := recoveryMutableSnapshot(t, fixture.db); after != before {
			t.Fatalf("late authority failure=%v changed state\nbefore: %s\nafter:  %s", failure, before, after)
		}
	}
	recorder.historyFailure = nil
	ctx, cancel := context.WithCancel(context.Background())
	recorder.cancel = cancel
	if result, err := generatedrecovery.RecoverRuntime(ctx, fixture.db, fixture.now.Add(3*time.Minute), repository); !errors.Is(err, deploymentbinding.ErrCanceled) || !errors.Is(err, context.Canceled) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("mid-validation cancellation result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("mid-validation cancellation changed state\nbefore: %s\nafter:  %s", before, after)
	}
	result, err = generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(4*time.Minute), repository)
	if err != nil || result.Deployments.FailedOther != 1 || result.Jobs.RetiredStale != 1 {
		t.Fatalf("recovery after cleared failures result=%#v error=%v", result, err)
	}
	assertMainDeployment(t, fixture.db, currentDeploymentID, "failed", "daemon_restarted")
}

func TestRecoverRuntimeRejectsCorruptHistoricalAuthorityAfterHeadAdvanced(t *testing.T) {
	fixture := newRecoveryActivationFixture(t, false)
	repository, _, boundJobID := seedCanonicalRunningRecoveryJob(t, fixture)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), boundJobID); err != nil {
		t.Fatal(err)
	}
	var authorizingJobID string
	if err := fixture.db.QueryRow(`SELECT authorizing_lifecycle_job_id FROM application_deployment_lifecycle_bindings WHERE job_id=?`, boundJobID).Scan(&authorizingJobID); err != nil {
		t.Fatal(err)
	}
	lifecycleRepository := applifecycle.New(fixture.db)
	authorizingOperation, err := lifecycleRepository.Operation(context.Background(), authorizingJobID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := lifecycleRepository.Accept(context.Background(), jobs.New(fixture.db), applifecycle.LifecycleRequest{
		ApplicationID: fixture.appID, ActorID: fixture.actorID, IdempotencyKey: "recovery-advance-stopped",
		Action: applifecycle.Stop, ExpectedDesiredRevision: 1,
	}, authorizingOperation.Pins)
	if err != nil || !accepted.Created || accepted.Operation.DesiredRevision != 2 {
		t.Fatalf("advance desired head=%#v error=%v", accepted, err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,started_at)
		VALUES(?,?,?,'applying','current',1,'compose',?)`, uuid.NewString(), fixture.appID, boundJobID, timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`DROP TRIGGER application_lifecycle_effect_receipt_retain`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM application_lifecycle_effect_receipts WHERE job_id=? AND boundary='complete'`, authorizingJobID); err != nil {
		t.Fatal(err)
	}
	before := recoveryMutableSnapshot(t, fixture.db)
	result, err := generatedrecovery.RecoverRuntime(context.Background(), fixture.db, fixture.now.Add(time.Minute), repository)
	if !errors.Is(err, deploymentbinding.ErrInvalidBinding) || result != (generatedrecovery.RuntimeResult{}) {
		t.Fatalf("recovery result=%#v error=%v", result, err)
	}
	if after := recoveryMutableSnapshot(t, fixture.db); after != before {
		t.Fatalf("corrupt historical authority changed recovery state\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRecoverRuntimeWriterReservationBlocksConcurrentDesiredAuthoring(t *testing.T) {
	fixture := newRecoveryActivationFixture(t, false)
	repository, recorder, boundJobID := seedCanonicalRunningRecoveryJob(t, fixture)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), boundJobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,started_at)
		VALUES(?,?,?,'applying','current',1,'compose',?)`, uuid.NewString(), fixture.appID, boundJobID, timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	stopJobID := "concurrent-stop-authoring"
	if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
		VALUES(?,'stop','application',?,'queued','queued',?,'{}',?,?)`, stopJobID, fixture.appID, fixture.actorID, timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}

	databaseFile := recoveryDatabaseFile(t, fixture.db)
	concurrentDB, err := database.Open(filepath.Dir(databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = concurrentDB.Close() })
	if _, err := concurrentDB.Exec(`PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var barrierOnce sync.Once
	var releaseOnce sync.Once
	recorder.historicalHook = func() {
		barrierOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer releaseOnce.Do(func() { close(release) })
	type recoveryOutcome struct {
		result generatedrecovery.RuntimeResult
		err    error
	}
	outcomes := make(chan recoveryOutcome, 1)
	go func() {
		result, recoveryErr := generatedrecovery.RecoverRuntime(ctx, fixture.db, fixture.now.Add(time.Minute), repository)
		outcomes <- recoveryOutcome{result: result, err: recoveryErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not reach canonical validation barrier")
	}

	concurrentLifecycle := applifecycle.New(concurrentDB)
	concurrentTx, err := concurrentDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, authorErr := concurrentLifecycle.AuthorTransition(context.Background(), concurrentTx, applifecycle.AuthorRequest{
		ApplicationID: fixture.appID, JobID: stopJobID, Action: applifecycle.Stop,
		ActorID: fixture.actorID, ExpectedRevision: 1,
	})
	_ = concurrentTx.Rollback()
	if authorErr == nil || (!strings.Contains(strings.ToLower(authorErr.Error()), "locked") && !strings.Contains(strings.ToLower(authorErr.Error()), "busy")) {
		t.Errorf("concurrent desired authoring error=%v, want SQLite writer reservation refusal", authorErr)
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case outcome := <-outcomes:
		if outcome.err != nil || outcome.result.Deployments.FailedOther != 1 || outcome.result.Jobs != (generatedrecovery.JobResult{}) {
			t.Fatalf("recovery result=%#v error=%v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish after releasing validation barrier")
	}

	concurrentTx, err = concurrentDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transition, created, err := concurrentLifecycle.AuthorTransition(context.Background(), concurrentTx, applifecycle.AuthorRequest{
		ApplicationID: fixture.appID, JobID: stopJobID, Action: applifecycle.Stop,
		ActorID: fixture.actorID, ExpectedRevision: 1,
	})
	if err != nil || !created || transition.Revision != 2 {
		_ = concurrentTx.Rollback()
		t.Fatalf("desired authoring after recovery transition=%#v created=%t error=%v", transition, created, err)
	}
	if err := concurrentTx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type recordingCanonicalValidator struct {
	delegate       deploymentbinding.LifecycleTxValidator
	transactions   []*sql.Tx
	historyFailure error
	cancel         context.CancelFunc
	historicalHook func()
}

func (v *recordingCanonicalValidator) ConfiguredFor(db *sql.DB) bool {
	return v != nil && v.delegate != nil && v.delegate.ConfiguredFor(db)
}

func (v *recordingCanonicalValidator) HistoricalBindingTx(ctx context.Context, tx *sql.Tx, ref deploymentbinding.OperationRef) error {
	v.transactions = append(v.transactions, tx)
	err := v.delegate.HistoricalBindingTx(ctx, tx, ref)
	if err == nil && v.historicalHook != nil {
		v.historicalHook()
	}
	if err == nil && v.cancel != nil {
		cancel := v.cancel
		v.cancel = nil
		cancel()
	}
	return err
}

func recoveryDatabaseFile(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			t.Fatal(err)
		}
		if name == "main" && path != "" {
			return path
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("main recovery database path not found")
	return ""
}

func (v *recordingCanonicalValidator) CurrentOperationTx(ctx context.Context, tx *sql.Tx, ref deploymentbinding.OperationRef) (deploymentbinding.Completion, error) {
	v.transactions = append(v.transactions, tx)
	return v.delegate.CurrentOperationTx(ctx, tx, ref)
}

func (v *recordingCanonicalValidator) UnresolvedHistoryTx(ctx context.Context, tx *sql.Tx, appID string) (bool, error) {
	v.transactions = append(v.transactions, tx)
	unresolved, err := v.delegate.UnresolvedHistoryTx(ctx, tx, appID)
	if err == nil && v.historyFailure != nil {
		return false, v.historyFailure
	}
	return unresolved, err
}

func seedCanonicalRunningRecoveryJob(t *testing.T, fixture *recoveryFixture) (*deploymentbinding.Repository, *recordingCanonicalValidator, string) {
	t.Helper()
	fixture.toSwitching(t)
	head := fixture.activate(t)
	fixture.advance(t, generatedruntimestate.PhaseSucceeded)
	if _, err := fixture.db.Exec(`UPDATE deployments SET status='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), fixture.jobID); err != nil {
		t.Fatal(err)
	}
	var artifactID, imageContentID, definitionDigest, containerName, containerID string
	if err := fixture.db.QueryRow(`SELECT c.image_artifact_id,a.image_content_id,a.build_definition_digest,c.container_name,c.container_id
		FROM generated_runtime_components c JOIN generated_image_artifacts a ON a.id=c.image_artifact_id
		WHERE c.deployment_id=? AND c.component_name=?`, fixture.deploymentID, fixture.component).
		Scan(&artifactID, &imageContentID, &definitionDigest, &containerName, &containerID); err != nil {
		t.Fatal(err)
	}
	pins := applifecycle.GeneratedLifecyclePinsV1{
		Version: 1, DeploymentID: fixture.deploymentID, ReleaseID: fixture.releaseID, Slot: head.Slot,
		ActiveGeneration: head.Generation, PlanRevision: applifecycle.RevisionRef{ID: fixture.planID, Number: 1},
		Configuration:       applifecycle.ConfigurationRef{RevisionID: fixture.configID, RevisionNumber: 1, FormatVersion: 2, PlanRevision: applifecycle.RevisionRef{ID: fixture.planID, Number: 1}, RuntimeComponents: []string{fixture.component}},
		BaselineDisposition: applifecycle.BaselineRunning,
		Components: []applifecycle.RetainedComponent{{
			Name: fixture.component, Role: "service", Technology: "node", ContainerID: containerID,
			ContainerName: containerName, ArtifactID: artifactID, ImageContentID: imageContentID,
			BuildDefinitionDigest: definitionDigest, NetworkName: "rig", NetworkAlias: fixture.component,
			InternalPort: 8080, WorkingDirectory: "/app", RunCommandDigest: strings.Repeat("9", 64),
			Limits: applifecycle.RetainedLimits{MemoryBytes: 128 << 20, MemorySwapBytes: 256 << 20,
				CPUQuota: 50000, CPUPeriod: 100000, PIDsLimit: 128, TmpfsBytes: 16 << 20,
				LogDriver: "local", LogMaxSize: "10m", LogMaxFiles: 3, HardeningPolicyVersion: "v1"},
		}},
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind", SelectedLineageGeneration: 1,
			SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("8", 64),
			ProtectedFormatVersion: 2, TargetRecordDigest: strings.Repeat("7", 64)},
	}
	lifecycleRepository := applifecycle.New(fixture.db)
	lifecycleJobs := jobs.New(fixture.db)
	accepted, err := lifecycleRepository.Accept(context.Background(), lifecycleJobs, applifecycle.LifecycleRequest{
		ApplicationID: fixture.appID, ActorID: fixture.actorID, IdempotencyKey: "recovery-running-authority",
		Action: applifecycle.Start, ExpectedDesiredRevision: 0,
	}, pins)
	if err != nil || !accepted.Created {
		t.Fatalf("accept lifecycle=%#v error=%v", accepted, err)
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	lifecycleJob, err := lifecycleJobs.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(lifecycleJob)
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range []struct {
		boundary applifecycle.Boundary
		mode     applifecycle.EffectMode
		outcome  applifecycle.Outcome
	}{
		{applifecycle.BoundaryHealth, applifecycle.EffectObservationOnly, applifecycle.OutcomeHealthy},
		{applifecycle.BoundaryPublish, applifecycle.EffectCommand, applifecycle.OutcomeServing},
		{applifecycle.BoundaryComplete, applifecycle.EffectObservationOnly, applifecycle.OutcomeCompleteRunning},
	} {
		request := applifecycle.EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID,
			ApplicationID: fixture.appID, DesiredRevision: 1, Boundary: effect.boundary, Mode: effect.mode,
			PinsDigest: accepted.Operation.PinsDigest}
		tx, err := fixture.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		intent, created, err := lifecycleRepository.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request)
		if err != nil || !created {
			_ = tx.Rollback()
			t.Fatalf("begin %s created=%t error=%v", effect.boundary, created, err)
		}
		proof := applifecycle.EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID,
			ApplicationID: fixture.appID, DesiredRevision: 1, Boundary: effect.boundary, Outcome: effect.outcome,
			IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("6", 64), PinsDigest: accepted.Operation.PinsDigest}
		if _, observed, err := lifecycleRepository.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err != nil || !observed {
			_ = tx.Rollback()
			t.Fatalf("observe %s created=%t error=%v", effect.boundary, observed, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	canonical, err := applifecycle.NewDeploymentBindingValidator(lifecycleRepository)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingCanonicalValidator{delegate: canonical}
	bindings, err := deploymentbinding.New(fixture.db, recorder)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "running-bound-recovery-job"
	if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
		VALUES(?,'deploy','application',?,'queued','queued',?,'{}',?,?)`, jobID, fixture.appID, fixture.actorID, timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.InsertNewTx(context.Background(), tx, jobID, fixture.now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return bindings, recorder, jobID
}

func seedRecoveryQueuedJob(t *testing.T, fixture *recoveryFixture, appID, jobID string) {
	t.Helper()
	if _, err := fixture.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, appID, timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,input_json,created_at,updated_at)
		VALUES(?,'deploy','application',?,'queued','queued','{}',?,?)`, jobID, appID, timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	fixture.bindJob(t, jobID)
}

func makeRecoveryApplicationAbsent(t *testing.T, db *sql.DB, appID string) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM applications WHERE id=?`, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}

func assertStaleRecoveryJob(t *testing.T, db *sql.DB, jobID string, wantAttempt, wantProgress int, wantCheckpoint string) {
	t.Helper()
	var status, phase, code, detail, checkpoint string
	var attempt, progress int
	if err := db.QueryRow(`SELECT status,phase,error_code,error_detail,attempt,progress_percent,checkpoint_json FROM jobs WHERE id=?`, jobID).
		Scan(&status, &phase, &code, &detail, &attempt, &progress, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if status != "needs_attention" || phase != "needs_attention" || code != "deployment_lifecycle_stale" ||
		detail != "Deployment no longer matches the application's desired lifecycle." || attempt != wantAttempt ||
		progress != wantProgress || checkpoint != wantCheckpoint {
		t.Fatalf("stale job=%q/%q code=%q detail=%q attempt=%d progress=%d checkpoint=%q",
			status, phase, code, detail, attempt, progress, checkpoint)
	}
}

func recoveryEvidenceSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	parts := []string{
		recoveryRows(t, db, `SELECT quote(id)||'|'||quote(app_id)||'|'||quote(release_id)||'|'||quote(job_id)||'|'||quote(status)||'|'||quote(started_at)||'|'||quote(finished_at)||'|'||quote(failure_code)||'|'||quote(failure_summary)||'|'||quote(diagnostic_code)||'|'||quote(configuration_mode)||'|'||quote(actual_configuration_revision_id)||'|'||quote(actual_configuration_revision_number)||'|'||quote(provenance_initialized)||'|'||quote(runtime_strategy)||'|'||quote(deployment_plan_revision_id)||'|'||quote(deployment_plan_revision_number) FROM deployments ORDER BY id`),
		recoveryRows(t, db, `SELECT quote(deployment_id)||'|'||quote(app_id)||'|'||quote(release_id)||'|'||quote(deployment_plan_revision_id)||'|'||quote(deployment_plan_revision_number)||'|'||quote(candidate_slot)||'|'||quote(previous_active_deployment_id)||'|'||quote(previous_active_slot)||'|'||quote(phase)||'|'||quote(migration_state)||'|'||quote(migration_started_at)||'|'||quote(migration_finished_at)||'|'||quote(diagnostic_code)||'|'||quote(created_at)||'|'||quote(updated_at)||'|'||quote(finished_at) FROM generated_runtime_deployments ORDER BY deployment_id`),
		recoveryRows(t, db, `SELECT quote(deployment_id)||'|'||quote(component_name)||'|'||quote(slot)||'|'||quote(image_artifact_id)||'|'||quote(container_name)||'|'||quote(container_id)||'|'||quote(state)||'|'||quote(diagnostic_code)||'|'||quote(created_at)||'|'||quote(updated_at)||'|'||quote(finished_at) FROM generated_runtime_components ORDER BY deployment_id,component_name`),
	}
	return strings.Join(parts, "\n--\n")
}

func recoveryMutableSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return recoveryEvidenceSnapshot(t, db) + "\n==\n" + strings.Join([]string{
		recoveryRows(t, db, `SELECT quote(id)||'|'||quote(status)||'|'||quote(phase)||'|'||quote(progress_percent)||'|'||quote(attempt)||'|'||quote(checkpoint_json)||'|'||quote(pause_disposition)||'|'||quote(error_code)||'|'||quote(error_detail)||'|'||quote(started_at)||'|'||quote(updated_at)||'|'||quote(finished_at) FROM jobs ORDER BY id`),
		recoveryRows(t, db, `SELECT quote(job_id)||'|'||quote(sequence)||'|'||quote(attempt)||'|'||quote(timestamp)||'|'||quote(level)||'|'||quote(phase)||'|'||quote(code)||'|'||quote(message)||'|'||quote(metadata_json) FROM job_events ORDER BY job_id,sequence`),
	}, "\n--\n")
}

func recoveryDeploymentRow(t *testing.T, db *sql.DB, deploymentID string) string {
	t.Helper()
	return recoveryRows(t, db, `SELECT quote(id)||'|'||quote(app_id)||'|'||quote(job_id)||'|'||quote(status)||'|'||quote(finished_at)||'|'||quote(failure_code)||'|'||quote(failure_summary)||'|'||quote(diagnostic_code)||'|'||quote(runtime_strategy) FROM deployments WHERE id=?`, deploymentID)
}

func recoveryRows(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(values, "\n")
}
