package jobs

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestRecoverRuntimeContextRequiresConfiguredBindings(t *testing.T) {
	service, closeDB := newTestService(t)
	defer closeDB()
	for _, candidate := range []*Service{nil, {}, service} {
		if err := candidate.RecoverRuntimeContext(context.Background()); !errors.Is(err, deploymentbinding.ErrInvalidConfiguration) {
			t.Fatalf("unconfigured recovery error=%v", err)
		}
	}
}

func TestRecoverRuntimeContextRefusalPreservesJobAndDoesNotWake(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "missing_binding"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			generic, closeDB := newTestService(t)
			defer closeDB()
			job, _, err := generic.CreateWithInput(CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: "unbound",
				IdempotencyKey: "unbound-recovery", Input: DeploymentInput{ConfigurationMode: ConfigurationCurrent}})
			if err != nil {
				t.Fatal(err)
			}
			stamp := job.CreatedAt.Format(time.RFC3339Nano)
			if _, err = generic.db.Exec(`INSERT INTO applications(id,slug,name,created_at,updated_at) VALUES(?,?,?,?,?)`,
				"unbound", "unbound", "Unbound recovery fixture", stamp, stamp); err != nil {
				t.Fatal(err)
			}
			beforeEvents, err := generic.Events(job.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			beforeJob, err := generic.Get(job.ID)
			if err != nil {
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
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			err = service.RecoverRuntimeContext(ctx)
			if canceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled recovery error=%v", err)
				}
			} else if !errors.Is(err, deploymentbinding.ErrBindingNotFound) {
				t.Fatalf("unbound recovery error=%v", err)
			}
			after, readErr := service.Get(job.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			afterEvents, readErr := service.Events(job.ID, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !reflect.DeepEqual(after, beforeJob) || !reflect.DeepEqual(afterEvents, beforeEvents) || len(service.wake) != 0 {
				t.Fatalf("recovery refusal changed state or signaled worker: job=%t events=%t wakes=%d",
					!reflect.DeepEqual(after, beforeJob), !reflect.DeepEqual(afterEvents, beforeEvents), len(service.wake))
			}
		})
	}
}

func TestRecoverRuntimeContextRecoversGenericJobWithoutWake(t *testing.T) {
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
	job, _, err := service.Create("maintenance", "machine", "recovery-host", "generic-recovery")
	if err != nil {
		t.Fatal(err)
	}
	<-service.wake
	if _, err = service.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.RecoverRuntimeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := service.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != string(Interrupted) || after.Attempt != 1 || len(service.wake) != 0 {
		t.Fatalf("generic recovery status=%s attempt=%d wakes=%d", after.Status, after.Attempt, len(service.wake))
	}
}

func TestRecoverRuntimeContextRequeuesSafeGeneratedJobAndWakesOnce(t *testing.T) {
	fixture := newSafeRuntimeRecoveryFixture(t)
	if err := fixture.service.RecoverRuntimeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.service.Get(fixture.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := fixture.service.Events(fixture.job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != string(Queued) || after.Phase != string(Queued) || after.Attempt != 1 ||
		after.Progress != 0 || after.Checkpoint != `{}` || countEvents(events, "daemon_restarted") != 1 || len(fixture.service.wake) != 1 {
		t.Fatalf("safe recovery job=%#v restart-events=%d wakes=%d", after, countEvents(events, "daemon_restarted"), len(fixture.service.wake))
	}
	if err := fixture.service.RecoverRuntimeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fixture.service.wake) != 1 {
		t.Fatalf("repeat recovery wakes=%d, want exactly one", len(fixture.service.wake))
	}
}

func TestRecoverRuntimeContextRollbackDoesNotWakeAfterRequeueMutation(t *testing.T) {
	fixture := newSafeRuntimeRecoveryFixture(t)
	fixture.service.now = func() time.Time { return fixture.now.Add(time.Minute) }
	generic, created, err := fixture.service.Create("maintenance", "machine", "later-recovery-host", "later-recovery")
	if err != nil || !created {
		t.Fatalf("create later generic job=%#v created=%t error=%v", generic, created, err)
	}
	<-fixture.service.wake
	if _, err := fixture.service.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, generic.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.db.Exec(`CREATE TRIGGER fail_later_recovery_update BEFORE UPDATE ON jobs
		WHEN OLD.id='` + generic.ID + `' AND NEW.status='interrupted'
		BEGIN SELECT RAISE(ABORT,'test later recovery update failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := runtimeRecoverySnapshot(t, fixture.service.db)
	if err := fixture.service.RecoverRuntimeContext(context.Background()); !errors.Is(err, deploymentbinding.ErrUnavailable) {
		t.Fatalf("failed recovery error=%v", err)
	}
	after := runtimeRecoverySnapshot(t, fixture.service.db)
	if after != before || len(fixture.service.wake) != 0 {
		t.Fatalf("failed recovery changed durable state=%t wakes=%d\nbefore: %s\nafter:  %s", after != before, len(fixture.service.wake), before, after)
	}
}

type safeRuntimeRecoveryFixture struct {
	service      *Service
	job          Job
	now          time.Time
	deploymentID string
}

func newSafeRuntimeRecoveryFixture(t *testing.T) safeRuntimeRecoveryFixture {
	t.Helper()
	generic, closeDB := newTestService(t)
	t.Cleanup(closeDB)
	bindings, err := deploymentbinding.New(generic.db, deploymentCreationTestValidator{db: generic.db})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithDeploymentBindings(generic.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	service.wake = make(chan struct{}, 4)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	actorID, appID, releaseID, planID, deploymentID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := service.db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, actorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, appID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
		planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret", strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), actorID, stamp, actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`,
		releaseID, appID, stamp, strings.Repeat("b", 64), strings.Repeat("f", 64), planID); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateDeploymentWithInputTransaction(context.Background(), DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "safe-runtime-recovery", RequestedBy: actorID,
		Input: DeploymentInput{ReleaseID: releaseID, ConfigurationMode: ConfigurationCurrent},
	}, DeploymentCreateHooks{FinalizeNew: func(ctx context.Context, tx *sql.Tx, job Job, _ deploymentbinding.Binding) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO deployments(
			id,app_id,release_id,job_id,status,configuration_mode,actual_configuration_revision_number,
			provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number,started_at
		) VALUES(?,?,?,?, 'applying','current',0,1,'generated_node',?,1,?)`, deploymentID, appID, releaseID, job.ID, planID, stamp)
		return err
	}})
	if err != nil || !created.Created {
		t.Fatalf("create safe deployment=%#v error=%v", created, err)
	}
	<-service.wake
	if _, err := service.db.Exec(`UPDATE jobs SET status='running',phase='apply_runtime',attempt=1,progress_percent=45,checkpoint_json='{"phase":"apply_runtime"}',started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	state := generatedruntimestate.New(service.db)
	if _, runtimeCreated, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"},
	}); err != nil || !runtimeCreated {
		t.Fatalf("begin generated runtime created=%t error=%v", runtimeCreated, err)
	}
	return safeRuntimeRecoveryFixture{service: service, job: created.Job, now: now, deploymentID: deploymentID}
}

func runtimeRecoverySnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	queries := []string{
		`SELECT quote(id)||'|'||quote(status)||'|'||quote(phase)||'|'||quote(progress_percent)||'|'||quote(attempt)||'|'||quote(checkpoint_json)||'|'||quote(error_code)||'|'||quote(error_detail)||'|'||quote(updated_at)||'|'||quote(finished_at) FROM jobs ORDER BY created_at,id`,
		`SELECT quote(job_id)||'|'||quote(sequence)||'|'||quote(attempt)||'|'||quote(timestamp)||'|'||quote(level)||'|'||quote(phase)||'|'||quote(code)||'|'||quote(message)||'|'||quote(metadata_json) FROM job_events ORDER BY job_id,sequence`,
		`SELECT quote(id)||'|'||quote(status)||'|'||quote(finished_at)||'|'||quote(failure_code)||'|'||quote(failure_summary)||'|'||quote(diagnostic_code) FROM deployments ORDER BY id`,
		`SELECT quote(deployment_id)||'|'||quote(phase)||'|'||quote(migration_state)||'|'||quote(diagnostic_code)||'|'||quote(updated_at)||'|'||quote(finished_at) FROM generated_runtime_deployments ORDER BY deployment_id`,
		`SELECT quote(deployment_id)||'|'||quote(component_name)||'|'||quote(state)||'|'||quote(diagnostic_code)||'|'||quote(updated_at)||'|'||quote(finished_at) FROM generated_runtime_components ORDER BY deployment_id,component_name`,
	}
	parts := make([]string, 0, len(queries))
	for _, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			values = append(values, value)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, strings.Join(values, "\n"))
	}
	return strings.Join(parts, "\n--\n")
}
