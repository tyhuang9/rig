package jobs

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentbinding"
)

type mutableDeploymentBindingTestValidator struct {
	db                *sql.DB
	historicalErr     error
	currentCompletion deploymentbinding.Completion
	currentErr        error
	unresolved        bool
	unresolvedErr     error
	historicalCalls   int
	currentCalls      int
	unresolvedCalls   int
}

func (validator *mutableDeploymentBindingTestValidator) ConfiguredFor(db *sql.DB) bool {
	return validator != nil && validator.db == db
}

func (validator *mutableDeploymentBindingTestValidator) HistoricalBindingTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) error {
	validator.historicalCalls++
	return validator.historicalErr
}

func (validator *mutableDeploymentBindingTestValidator) CurrentOperationTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) (deploymentbinding.Completion, error) {
	validator.currentCalls++
	return validator.currentCompletion, validator.currentErr
}

func (validator *mutableDeploymentBindingTestValidator) UnresolvedHistoryTx(context.Context, *sql.Tx, string) (bool, error) {
	validator.unresolvedCalls++
	return validator.unresolved, validator.unresolvedErr
}

func (validator *mutableDeploymentBindingTestValidator) resetCalls() {
	validator.historicalCalls = 0
	validator.currentCalls = 0
	validator.unresolvedCalls = 0
}

func newLegacyDeploymentTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	generic, closeDB := newTestService(t)
	return newLegacyDeploymentTestServiceForDB(t, generic.db), closeDB
}

func newLegacyDeploymentTestServiceForDB(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	bindings, err := deploymentbinding.New(db, deploymentCreationTestValidator{db: db})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func insertDeploymentBindingTestApplication(t *testing.T, db *sql.DB, appID string) {
	t.Helper()
	stamp := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`, appID, appID, appID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func createLegacyDeploymentBindingTestJob(t *testing.T, service *Service, appID, key string) Job {
	t.Helper()
	result, err := service.CreateDeploymentWithInputTransaction(context.Background(), DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: key, RequestedBy: "binding-test-actor",
		Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent},
	}, DeploymentCreateHooks{})
	if err != nil || !result.Created || result.Binding.Mode() != deploymentbinding.ModeLegacyUninitialized {
		t.Fatalf("legacy deployment result=%#v error=%v", result, err)
	}
	return result.Job
}

// advanceSyntheticRunningHead is limited to the jobs-package gate tests. It
// creates the smallest structurally valid mutable head while the test validator
// owns canonical graph outcomes; production tests in applifecycle cover the
// complete pins, intent, receipt, and history graph.
func advanceSyntheticRunningHead(t *testing.T, db *sql.DB, appID string, revision int64) string {
	t.Helper()
	if revision < 1 {
		t.Fatalf("invalid synthetic revision %d", revision)
	}
	action := "start"
	var previous any
	baseline := "stopped"
	if revision > 1 {
		action = "restart"
		previous = "running"
		baseline = "running"
	}
	revisionText := strconv.FormatInt(revision, 10)
	jobID := "binding-authority-" + appID + "-" + revisionText
	key := "binding-authority-key-" + revisionText
	stamp := time.Date(2026, 10, 18, 12, 0, int(revision), 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,idempotency_key,requested_by,input_json,
		created_at,updated_at,finished_at
	) VALUES(?,?, 'application',?,'succeeded','succeeded',?,'binding-test-actor','{}',?,?,?)`,
		jobID, action, appID, key, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO application_desired_lifecycle_transitions(
		app_id,revision_number,expected_revision,previous_state,desired_state,job_id,action,actor_id,created_at
	) VALUES(?,?,?,?, 'running',?,?, 'binding-test-actor',?)`,
		appID, revision, revision-1, previous, jobID, action, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS application_lifecycle_operation_valid_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`PRAGMA foreign_keys=ON`) })
	if _, err := db.Exec(`INSERT INTO application_lifecycle_operations(
		job_id,app_id,desired_revision,expected_revision,action,actor_id,idempotency_key,version,
		runtime_strategy,baseline_disposition,deployment_id,active_generation,release_id,slot,
		plan_revision_id,plan_revision_number,configuration_revision_id,configuration_revision_number,
		configuration_format,pins_json,pins_digest,created_at
	) VALUES(?,?,?,?,?,?,?,1,'generated_node',?, ?,1,?,'blue',?,1,'',0,0,'{}',?,?)`,
		jobID, appID, revision, revision-1, action, "binding-test-actor", key, baseline,
		"synthetic-deployment-"+jobID, "synthetic-release-"+jobID, "synthetic-plan-"+jobID,
		strings.Repeat("a", 64), stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func newRunningDeploymentBindingTestFixture(t *testing.T, appID string) (*Service, *mutableDeploymentBindingTestValidator, Job, func()) {
	t.Helper()
	generic, closeDB := newTestService(t)
	insertDeploymentBindingTestApplication(t, generic.db, appID)
	advanceSyntheticRunningHead(t, generic.db, appID, 1)
	if _, err := generic.db.Exec(`DROP TRIGGER application_deployment_lifecycle_binding_valid_insert`); err != nil {
		closeDB()
		t.Fatal(err)
	}
	validator := &mutableDeploymentBindingTestValidator{db: generic.db, currentCompletion: deploymentbinding.CompletionComplete}
	bindings, err := deploymentbinding.New(generic.db, validator)
	if err != nil {
		closeDB()
		t.Fatal(err)
	}
	service, err := NewWithDeploymentBindings(generic.db, bindings)
	if err != nil {
		closeDB()
		t.Fatal(err)
	}
	result, err := service.CreateDeploymentWithInputTransaction(context.Background(), DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "running-deployment", RequestedBy: "binding-test-actor",
		Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent},
	}, DeploymentCreateHooks{})
	if err != nil || result.Binding.Mode() != deploymentbinding.ModeRunning {
		closeDB()
		t.Fatalf("running deployment result=%#v error=%v", result, err)
	}
	validator.resetCalls()
	return service, validator, result.Job, closeDB
}

type deploymentBindingJobSnapshot struct {
	status, phase, checkpoint, errorCode, errorDetail, pauseDisposition string
	progress, attempt                                                   int
	startedAt, updatedAt, finishedAt                                    string
	events                                                              int
}

func snapshotDeploymentBindingJob(t *testing.T, service *Service, jobID string) deploymentBindingJobSnapshot {
	t.Helper()
	var snapshot deploymentBindingJobSnapshot
	if err := service.db.QueryRow(`SELECT status,phase,checkpoint_json,COALESCE(error_code,''),COALESCE(error_detail,''),
		COALESCE(pause_disposition,''),progress_percent,attempt,COALESCE(started_at,''),updated_at,COALESCE(finished_at,''),
		(SELECT COUNT(*) FROM job_events WHERE job_id=jobs.id)
		FROM jobs WHERE id=?`, jobID).Scan(
		&snapshot.status, &snapshot.phase, &snapshot.checkpoint, &snapshot.errorCode, &snapshot.errorDetail,
		&snapshot.pauseDisposition, &snapshot.progress, &snapshot.attempt, &snapshot.startedAt, &snapshot.updatedAt,
		&snapshot.finishedAt, &snapshot.events); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func drainDeploymentBindingWake(service *Service) {
	for len(service.wake) > 0 {
		<-service.wake
	}
}

type deploymentBindingCountingExecutor struct {
	jobs []string
}

func (executor *deploymentBindingCountingExecutor) Execute(_ context.Context, job Job, _ ProgressReporter) (ExecutionResult, error) {
	executor.jobs = append(executor.jobs, job.ID)
	return ExecutionResult{}, nil
}

func TestClaimNextRetiresStaleDeploymentAndRunsNextEligibleJob(t *testing.T) {
	service, closeDB := newLegacyDeploymentTestService(t)
	defer closeDB()
	service.wake = make(chan struct{}, 8)
	insertDeploymentBindingTestApplication(t, service.db, "claim-stale-app")
	stale := createLegacyDeploymentBindingTestJob(t, service, "claim-stale-app", "claim-stale")
	if _, err := service.db.Exec(`INSERT INTO deployments(
		id,app_id,job_id,status,configuration_mode,provenance_initialized
	) VALUES('claim-stale-deployment','claim-stale-app',?,'preparing','current',1)`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE jobs SET progress_percent=37,checkpoint_json='{"phase":"waiting"}',
		attempt=3,started_at=?,updated_at=? WHERE id=?`,
		"2026-10-18T11:59:00Z", "2026-10-18T11:59:30Z", stale.ID); err != nil {
		t.Fatal(err)
	}
	advanceSyntheticRunningHead(t, service.db, "claim-stale-app", 1)
	next, _, err := service.Create("fake", "machine", "claim-next", "claim-next")
	if err != nil {
		t.Fatal(err)
	}
	drainDeploymentBindingWake(service)
	retiredAt := time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return retiredAt }
	executor := &deploymentBindingCountingExecutor{}
	if err := service.runOne(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	if len(executor.jobs) != 0 {
		t.Fatalf("stale deployment reached executor: %#v", executor.jobs)
	}
	retired := snapshotDeploymentBindingJob(t, service, stale.ID)
	if retired.status != string(NeedsAttention) || retired.phase != "needs_attention" ||
		retired.errorCode != deploymentLifecycleStaleCode || retired.errorDetail != deploymentLifecycleStaleMessage ||
		retired.progress != 37 || retired.checkpoint != `{"phase":"waiting"}` || retired.attempt != 3 ||
		retired.pauseDisposition != "" || retired.startedAt != "2026-10-18T11:59:00Z" ||
		retired.updatedAt != retiredAt.Format(time.RFC3339Nano) || retired.finishedAt != retiredAt.Format(time.RFC3339Nano) || retired.events != 2 {
		t.Fatalf("retired stale deployment=%#v", retired)
	}
	events, err := service.Events(stale.ID, 0)
	if err != nil || countEvents(events, deploymentLifecycleStaleCode) != 1 || countEvents(events, "job_assigned") != 0 {
		t.Fatalf("stale events=%#v error=%v", events, err)
	}
	if err := service.runOne(context.Background(), executor); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executor.jobs, []string{next.ID}) {
		t.Fatalf("next eligible executions=%#v want=%q", executor.jobs, next.ID)
	}
	if after := snapshotDeploymentBindingJob(t, service, stale.ID); !reflect.DeepEqual(after, retired) {
		t.Fatalf("stale deployment retired more than once before=%#v after=%#v", retired, after)
	}
	var deploymentJobID, deploymentStatus string
	if err := service.db.QueryRow(`SELECT job_id,status FROM deployments WHERE id='claim-stale-deployment'`).Scan(&deploymentJobID, &deploymentStatus); err != nil || deploymentJobID != stale.ID || deploymentStatus != "preparing" {
		t.Fatalf("stale deployment runtime row job=%q status=%q error=%v", deploymentJobID, deploymentStatus, err)
	}
}

func TestClaimNextAssignsCurrentBoundDeploymentOnce(t *testing.T) {
	service, closeDB := newLegacyDeploymentTestService(t)
	defer closeDB()
	insertDeploymentBindingTestApplication(t, service.db, "claim-current-app")
	job := createLegacyDeploymentBindingTestJob(t, service, "claim-current-app", "claim-current")
	claimed, disposition, err := service.claimNext(context.Background())
	if err != nil || disposition != claimClaimed || claimed.ID != job.ID || claimed.Attempt != 1 || claimed.Status != string(Assigned) {
		t.Fatalf("current claim=%#v disposition=%v error=%v", claimed, disposition, err)
	}
	events, err := service.Events(job.ID, 0)
	if err != nil || countEvents(events, "job_assigned") != 1 {
		t.Fatalf("current claim events=%#v error=%v", events, err)
	}
	if _, disposition, err := service.claimNext(context.Background()); err != nil || disposition != claimNone {
		t.Fatalf("repeat current claim disposition=%v error=%v", disposition, err)
	}
}

func TestClaimNextBindingFailuresAreReadOnly(t *testing.T) {
	t.Run("missing binding", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		insertDeploymentBindingTestApplication(t, service.db, "claim-missing-app")
		job, _, err := service.CreateWithInput(CreateRequest{
			Type: "deploy", ResourceType: "application", ResourceID: "claim-missing-app",
			RequestedBy: "binding-test-actor", Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent},
		})
		if err != nil {
			t.Fatal(err)
		}
		drainDeploymentBindingWake(service)
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		if _, disposition, err := service.claimNext(context.Background()); !errors.Is(err, deploymentbinding.ErrBindingNotFound) || disposition != claimNone {
			t.Fatalf("missing binding claim disposition=%v error=%v", disposition, err)
		}
		if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
			t.Fatalf("missing binding mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
		}
	})

	t.Run("unresolved authority exits worker once", func(t *testing.T) {
		service, validator, job, closeDB := newRunningDeploymentBindingTestFixture(t, "claim-unresolved-app")
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		validator.unresolved = true
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		service.signal()
		executor := &deploymentBindingCountingExecutor{}
		err := service.RunWorker(context.Background(), executor)
		if !errors.Is(err, ErrDeploymentLifecycleUnresolved) {
			t.Fatalf("unresolved worker error=%v", err)
		}
		if validator.historicalCalls != 2 || validator.currentCalls != 1 || validator.unresolvedCalls != 1 || len(executor.jobs) != 0 {
			t.Fatalf("unresolved calls historical=%d current=%d unresolved=%d executor=%#v",
				validator.historicalCalls, validator.currentCalls, validator.unresolvedCalls, executor.jobs)
		}
		if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
			t.Fatalf("unresolved mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
		}
	})

	t.Run("corrupt historical authority precedes newer stale head", func(t *testing.T) {
		service, validator, job, closeDB := newRunningDeploymentBindingTestFixture(t, "claim-corrupt-app")
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		advanceSyntheticRunningHead(t, service.db, "claim-corrupt-app", 2)
		validator.historicalErr = deploymentbinding.ErrInvalidBinding
		validator.resetCalls()
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		service.signal()
		executor := &deploymentBindingCountingExecutor{}
		if err := service.RunWorker(context.Background(), executor); !errors.Is(err, deploymentbinding.ErrInvalidBinding) {
			t.Fatalf("corrupt binding worker error=%v", err)
		}
		if validator.historicalCalls != 1 || validator.currentCalls != 0 || validator.unresolvedCalls != 0 || len(executor.jobs) != 0 {
			t.Fatalf("corrupt check order historical=%d current=%d unresolved=%d executor=%#v", validator.historicalCalls, validator.currentCalls, validator.unresolvedCalls, executor.jobs)
		}
		if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
			t.Fatalf("corrupt binding mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
		}
	})
}

func TestClaimNextStaleRetirementEventFailureRollsBack(t *testing.T) {
	service, closeDB := newLegacyDeploymentTestService(t)
	defer closeDB()
	service.wake = make(chan struct{}, 4)
	insertDeploymentBindingTestApplication(t, service.db, "claim-event-app")
	job := createLegacyDeploymentBindingTestJob(t, service, "claim-event-app", "claim-event")
	advanceSyntheticRunningHead(t, service.db, "claim-event-app", 1)
	drainDeploymentBindingWake(service)
	before := snapshotDeploymentBindingJob(t, service, job.ID)
	if _, err := service.db.Exec(`CREATE TRIGGER fail_stale_retirement_event
		BEFORE INSERT ON job_events WHEN NEW.code='deployment_lifecycle_stale'
		BEGIN SELECT RAISE(ABORT,'fixture stale event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, disposition, err := service.claimNext(context.Background()); err == nil || disposition != claimNone {
		t.Fatalf("event failure claim disposition=%v error=%v", disposition, err)
	}
	if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
		t.Fatalf("event failure mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
	}
}

func TestResumeContextChecksDeploymentBindingBeforeRequeue(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		insertDeploymentBindingTestApplication(t, service.db, "resume-stale-app")
		job := createLegacyDeploymentBindingTestJob(t, service, "resume-stale-app", "resume-stale")
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=2 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		advanceSyntheticRunningHead(t, service.db, "resume-stale-app", 1)
		drainDeploymentBindingWake(service)
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		if _, err := service.ResumeContext(context.Background(), job.ID); !errors.Is(err, ErrDeploymentLifecycleStale) {
			t.Fatalf("stale resume error=%v", err)
		}
		if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
			t.Fatalf("stale resume mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
		}
	})

	t.Run("unresolved", func(t *testing.T) {
		service, validator, job, closeDB := newRunningDeploymentBindingTestFixture(t, "resume-unresolved-app")
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=4 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		validator.unresolved = true
		validator.resetCalls()
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		if _, err := service.ResumeContext(context.Background(), job.ID); !errors.Is(err, ErrDeploymentLifecycleUnresolved) {
			t.Fatalf("unresolved resume error=%v", err)
		}
		if validator.historicalCalls != 2 || validator.currentCalls != 1 || validator.unresolvedCalls != 1 {
			t.Fatalf("unresolved resume calls historical=%d current=%d unresolved=%d",
				validator.historicalCalls, validator.currentCalls, validator.unresolvedCalls)
		}
		if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
			t.Fatalf("unresolved resume mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
		}
	})

	t.Run("current", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		insertDeploymentBindingTestApplication(t, service.db, "resume-current-app")
		job := createLegacyDeploymentBindingTestJob(t, service, "resume-current-app", "resume-current")
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=5 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		drainDeploymentBindingWake(service)
		resumed, err := service.ResumeContext(context.Background(), job.ID)
		if err != nil || resumed.Status != string(Queued) || resumed.Attempt != 5 || resumed.PauseDisposition != "" || len(service.wake) != 1 {
			t.Fatalf("current resume=%#v error=%v wakes=%d", resumed, err, len(service.wake))
		}
		events, err := service.Events(job.ID, 0)
		if err != nil || countEvents(events, "job_resumed") != 1 {
			t.Fatalf("current resume events=%#v error=%v", events, err)
		}
	})

	t.Run("deleted application is read-only stale", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		insertDeploymentBindingTestApplication(t, service.db, "resume-deleted-app")
		job := createLegacyDeploymentBindingTestJob(t, service, "resume-deleted-app", "resume-deleted")
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=6 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		drainDeploymentBindingWake(service)
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		beforeEvents, err := service.Events(job.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec(`DELETE FROM applications WHERE id='resume-deleted-app'`); err != nil {
			t.Fatal(err)
		}
		if err := service.CheckMissingApplicationDeploymentBindingContext(context.Background(), job.ID); !errors.Is(err, ErrDeploymentLifecycleStale) {
			t.Fatalf("deleted-app binding check error=%v", err)
		}
		afterEvents, err := service.Events(job.ID, 0)
		if after := snapshotDeploymentBindingJob(t, service, job.ID); err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterEvents, beforeEvents) || len(service.wake) != 0 {
			t.Fatalf("deleted-app binding check mutation before=%#v after=%#v events=%#v/%#v wakes=%d error=%v",
				before, after, beforeEvents, afterEvents, len(service.wake), err)
		}
	})

	t.Run("deleted application does not mask corrupt history", func(t *testing.T) {
		service, validator, job, closeDB := newRunningDeploymentBindingTestFixture(t, "resume-corrupt-deleted-app")
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=7 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		beforeEvents, err := service.Events(job.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = service.db.Exec(`PRAGMA foreign_keys=ON`) })
		if _, err := service.db.Exec(`DELETE FROM applications WHERE id='resume-corrupt-deleted-app'`); err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			t.Fatal(err)
		}
		validator.historicalErr = deploymentbinding.ErrInvalidBinding
		validator.resetCalls()
		if err := service.CheckMissingApplicationDeploymentBindingContext(context.Background(), job.ID); !errors.Is(err, deploymentbinding.ErrInvalidBinding) || errors.Is(err, ErrDeploymentLifecycleStale) {
			t.Fatalf("corrupt deleted-app binding check error=%v", err)
		}
		if validator.historicalCalls != 1 || validator.currentCalls != 0 || validator.unresolvedCalls != 0 {
			t.Fatalf("corrupt deleted-app calls historical=%d current=%d unresolved=%d",
				validator.historicalCalls, validator.currentCalls, validator.unresolvedCalls)
		}
		afterEvents, err := service.Events(job.ID, 0)
		if after := snapshotDeploymentBindingJob(t, service, job.ID); err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterEvents, beforeEvents) || len(service.wake) != 0 {
			t.Fatalf("corrupt deleted-app binding check mutation before=%#v after=%#v events=%#v/%#v wakes=%d error=%v",
				before, after, beforeEvents, afterEvents, len(service.wake), err)
		}
	})

	t.Run("resume event failure rolls back", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		insertDeploymentBindingTestApplication(t, service.db, "resume-event-app")
		job := createLegacyDeploymentBindingTestJob(t, service, "resume-event-app", "resume-event")
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=8 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		drainDeploymentBindingWake(service)
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		beforeEvents, err := service.Events(job.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec(`CREATE TRIGGER fail_resume_event
			BEFORE INSERT ON job_events WHEN NEW.code='job_resumed'
			BEGIN SELECT RAISE(ABORT,'fixture resume event failure'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ResumeContext(context.Background(), job.ID); err == nil {
			t.Fatal("resume event failure unexpectedly succeeded")
		}
		afterEvents, err := service.Events(job.ID, 0)
		if after := snapshotDeploymentBindingJob(t, service, job.ID); err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterEvents, beforeEvents) || len(service.wake) != 0 {
			t.Fatalf("resume event failure mutation before=%#v after=%#v events=%#v/%#v wakes=%d error=%v",
				before, after, beforeEvents, afterEvents, len(service.wake), err)
		}
	})

	t.Run("canceled caller context is read-only", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		insertDeploymentBindingTestApplication(t, service.db, "resume-canceled-app")
		job := createLegacyDeploymentBindingTestJob(t, service, "resume-canceled-app", "resume-canceled")
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=?,attempt=9 WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		drainDeploymentBindingWake(service)
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		beforeEvents, err := service.Events(job.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := service.ResumeContext(ctx, job.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled resume error=%v", err)
		}
		afterEvents, err := service.Events(job.ID, 0)
		if after := snapshotDeploymentBindingJob(t, service, job.ID); err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterEvents, beforeEvents) || len(service.wake) != 0 {
			t.Fatalf("canceled resume mutation before=%#v after=%#v events=%#v/%#v wakes=%d error=%v",
				before, after, beforeEvents, afterEvents, len(service.wake), err)
		}
	})

	t.Run("missing-application check refuses generic work", func(t *testing.T) {
		service, closeDB := newLegacyDeploymentTestService(t)
		defer closeDB()
		service.wake = make(chan struct{}, 4)
		job, _, err := service.CreateWithInput(CreateRequest{
			Type: "restart", ResourceType: "application", ResourceID: "absent-generic-app",
			RequestedBy: "binding-test-actor", Input: NoInput{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.db.Exec(`UPDATE jobs SET status='waiting_user',phase=?,pause_disposition=? WHERE id=?`,
			PauseInsufficientReplacementCapacity, PauseInsufficientReplacementCapacity, job.ID); err != nil {
			t.Fatal(err)
		}
		drainDeploymentBindingWake(service)
		before := snapshotDeploymentBindingJob(t, service, job.ID)
		if err := service.CheckMissingApplicationDeploymentBindingContext(context.Background(), job.ID); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) {
			t.Fatalf("generic missing-application check error=%v", err)
		}
		if after := snapshotDeploymentBindingJob(t, service, job.ID); !reflect.DeepEqual(after, before) || len(service.wake) != 0 {
			t.Fatalf("generic missing-application check mutation before=%#v after=%#v wakes=%d", before, after, len(service.wake))
		}
	})
}
