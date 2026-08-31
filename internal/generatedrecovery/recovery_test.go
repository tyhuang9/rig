package generatedrecovery_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

func TestRepositoryRecoveryPreservesGeneratedAndKeepsComposeBehavior(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	composeDeploymentID, composeJobID := fixture.seedComposeDeployment(t)

	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertRuntime(t, fixture.db, fixture.deploymentID, "preflight", "not_required", "")
	assertMainDeployment(t, fixture.db, composeDeploymentID, "failed", "daemon_restarted")

	if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
	assertJob(t, fixture.db, composeJobID, "interrupted", "interrupted", "daemon_restarted", 1, []byte("{}"))
	assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 1)

	// Startup recovery can be retried after a partial daemon initialization.
	// The already requeued generated job remains eligible and gets no duplicate
	// recovery event.
	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
	assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 1)
}

func TestRecoveryFailsClosedForUnsafeGeneratedStates(t *testing.T) {
	t.Run("migration running", func(t *testing.T) {
		fixture := newRecoveryFixture(t, true)
		fixture.advance(t, generatedruntimestate.PhaseBuilding)
		fixture.imageReady(t)
		fixture.advance(t, generatedruntimestate.PhaseMigrating)
		if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
			t.Fatal(err)
		}

		result, err := generatedrecovery.RecoverDeployments(context.Background(), fixture.db, fixture.now.Add(time.Minute))
		if err != nil || result.FailedGenerated != 1 || result.PreservedGenerated != 0 {
			t.Fatalf("recovery = %#v err=%v", result, err)
		}
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "failed", "daemon_restarted")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
		jobsResult, err := generatedrecovery.RecoverJobs(context.Background(), fixture.db, fixture.now.Add(2*time.Minute))
		if err != nil || jobsResult.Interrupted != 1 || jobsResult.RequeuedGenerated != 0 {
			t.Fatalf("job recovery = %#v err=%v", jobsResult, err)
		}
		assertJob(t, fixture.db, fixture.jobID, "interrupted", "interrupted", "daemon_restarted", 2, fixture.inputJSON)
	})

	t.Run("component starting without durable id", func(t *testing.T) {
		fixture := newRecoveryFixture(t, false)
		fixture.advance(t, generatedruntimestate.PhaseBuilding)
		fixture.imageReady(t)
		fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
		description, err := generatedruntime.DescribeInactiveCandidate(fixture.appID, fixture.component, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.state.SetContainerStarting(context.Background(), fixture.appID, fixture.deploymentID, fixture.component, description.ContainerName); err != nil {
			t.Fatal(err)
		}

		if _, err := generatedrecovery.RecoverDeployments(context.Background(), fixture.db, fixture.now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
		var state, diagnostic string
		if err := fixture.db.QueryRow(`SELECT state,diagnostic_code FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, fixture.deploymentID, fixture.component).Scan(&state, &diagnostic); err != nil {
			t.Fatal(err)
		}
		if state != "failed" || diagnostic != "daemon_restarted" {
			t.Fatalf("component = state %q diagnostic %q", state, diagnostic)
		}
	})

	t.Run("ready artifact became unavailable", func(t *testing.T) {
		fixture := newRecoveryFixture(t, false)
		fixture.advance(t, generatedruntimestate.PhaseBuilding)
		artifactID := fixture.imageReady(t)
		if _, err := fixture.db.Exec(`UPDATE generated_image_artifacts SET state='unavailable',updated_at=? WHERE id=? AND state='ready'`, timestamp(fixture.now.Add(time.Second)), artifactID); err != nil {
			t.Fatal(err)
		}

		if _, err := generatedrecovery.RecoverDeployments(context.Background(), fixture.db, fixture.now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
	})
}

func TestRecoveryResumesSwitchingAndDrainingIdempotently(t *testing.T) {
	for _, test := range []struct {
		name       string
		switchHead bool
		draining   bool
	}{
		{name: "before active head switch"},
		{name: "after active head switch", switchHead: true},
		{name: "draining", switchHead: true, draining: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, false)
			fixture.toSwitching(t)
			if test.switchHead {
				if _, switched, err := fixture.state.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || !switched {
					t.Fatalf("switch active: switched=%t err=%v", switched, err)
				}
			}
			if test.draining {
				fixture.advance(t, generatedruntimestate.PhaseDraining)
			}

			result, err := generatedrecovery.RecoverDeployments(context.Background(), fixture.db, fixture.now.Add(time.Minute))
			if err != nil || result.PreservedGenerated != 1 || result.FailedGenerated != 0 {
				t.Fatalf("deployment recovery = %#v err=%v", result, err)
			}
			jobResult, err := generatedrecovery.RecoverJobs(context.Background(), fixture.db, fixture.now.Add(2*time.Minute))
			if err != nil || jobResult.RequeuedGenerated != 1 || jobResult.Interrupted != 0 {
				t.Fatalf("job recovery = %#v err=%v", jobResult, err)
			}
			phase := "switching_route"
			if test.draining {
				phase = "draining"
			}
			assertRuntime(t, fixture.db, fixture.deploymentID, phase, "not_required", "")
			assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
			assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
		})
	}
}

type recoveryFixture struct {
	db           *sql.DB
	state        *generatedruntimestate.Repository
	now          time.Time
	actorID      string
	appID        string
	jobID        string
	deploymentID string
	releaseID    string
	planID       string
	component    string
	inputJSON    []byte
	phase        generatedruntimestate.Phase
}

func newRecoveryFixture(t *testing.T, migration bool) *recoveryFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := &recoveryFixture{
		db: db, state: generatedruntimestate.New(db), now: time.Date(2026, time.August, 31, 18, 0, 0, 0, time.UTC),
		actorID: uuid.NewString(), appID: uuid.NewString(), jobID: uuid.NewString(), deploymentID: uuid.NewString(),
		releaseID: uuid.NewString(), planID: uuid.NewString(), component: "api", phase: generatedruntimestate.PhasePreflight,
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, fixture.actorID, fixture.actorID, "hash", timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, fixture.appID, fixture.appID, fixture.appID, timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	migrationDigest := ""
	if migration {
		migrationDigest = strings.Repeat("e", 64)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,?,?,?,'accepted',?,?)`,
		fixture.planID, fixture.appID, "apps/"+fixture.appID+"/deployment-plans/"+fixture.planID+".secret", strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), migrationDigest, fixture.actorID, timestamp(fixture.now), fixture.actorID, timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if migration {
		if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,?,?)`, fixture.planID, fixture.appID, fixture.actorID, timestamp(fixture.now)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, fixture.releaseID, fixture.appID, timestamp(fixture.now), strings.Repeat("b", 64), strings.Repeat("f", 64), fixture.planID); err != nil {
		t.Fatal(err)
	}
	fixture.inputJSON, err = json.Marshal(jobs.DeploymentInput{ReleaseID: fixture.releaseID, ConfigurationMode: jobs.ConfigurationCurrent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,attempt,started_at,created_at,updated_at) VALUES(?,'deploy','application',?,'running','apply_runtime',?,?,2,?,?,?)`, fixture.jobID, fixture.appID, fixture.actorID, string(fixture.inputJSON), timestamp(fixture.now), timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number,started_at) VALUES(?,?,?,?,'applying','current',1,'generated_node',?,1,?)`, fixture.deploymentID, fixture.appID, fixture.releaseID, fixture.jobID, fixture.planID, timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: fixture.deploymentID, AppID: fixture.appID, ReleaseID: fixture.releaseID,
		DeploymentPlanRevisionID: fixture.planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{fixture.component},
	}); err != nil || !created {
		t.Fatalf("begin runtime: created=%t err=%v", created, err)
	}
	return fixture
}

func (f *recoveryFixture) advance(t *testing.T, next generatedruntimestate.Phase) {
	t.Helper()
	if _, err := f.state.Advance(context.Background(), f.appID, f.deploymentID, f.phase, next, ""); err != nil {
		t.Fatalf("advance %s to %s: %v", f.phase, next, err)
	}
	f.phase = next
}

func (f *recoveryFixture) imageReady(t *testing.T) string {
	t.Helper()
	artifactID := uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,
		build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, f.releaseID, f.planID, 1, f.component,
		strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), timestamp(f.now), timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.SetImageReady(context.Background(), f.appID, f.deploymentID, f.component, artifactID); err != nil {
		t.Fatal(err)
	}
	return artifactID
}

func (f *recoveryFixture) toSwitching(t *testing.T) {
	t.Helper()
	f.advance(t, generatedruntimestate.PhaseBuilding)
	f.imageReady(t)
	f.advance(t, generatedruntimestate.PhaseStartingCandidate)
	description, err := generatedruntime.DescribeInactiveCandidate(f.appID, f.component, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.SetContainerStarting(context.Background(), f.appID, f.deploymentID, f.component, description.ContainerName); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.SetContainerRunning(context.Background(), f.appID, f.deploymentID, f.component, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	f.advance(t, generatedruntimestate.PhaseWaitingHealth)
	if _, err := f.state.AdvanceComponent(context.Background(), f.appID, f.deploymentID, f.component, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	f.advance(t, generatedruntimestate.PhaseSwitchingRoute)
}

func (f *recoveryFixture) seedComposeDeployment(t *testing.T) (string, string) {
	t.Helper()
	appID, jobID, deploymentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, appID, timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,attempt,started_at,created_at,updated_at) VALUES(?,'deploy','application',?,'running','apply_runtime',?,'{}',1,?,?,?)`, jobID, appID, f.actorID, timestamp(f.now), timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,started_at) VALUES(?,?,?,'applying','current',1,'compose',?)`, deploymentID, appID, jobID, timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	return deploymentID, jobID
}

func assertMainDeployment(t *testing.T, db *sql.DB, deploymentID, wantStatus, wantDiagnostic string) {
	t.Helper()
	var status string
	var diagnostic sql.NullString
	if err := db.QueryRow(`SELECT status,diagnostic_code FROM deployments WHERE id=?`, deploymentID).Scan(&status, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || diagnostic.String != wantDiagnostic {
		t.Fatalf("deployment %s = status %q diagnostic %q", deploymentID, status, diagnostic.String)
	}
}

func assertRuntime(t *testing.T, db *sql.DB, deploymentID, wantPhase, wantMigration, wantDiagnostic string) {
	t.Helper()
	var phase, migration string
	var diagnostic sql.NullString
	if err := db.QueryRow(`SELECT phase,migration_state,diagnostic_code FROM generated_runtime_deployments WHERE deployment_id=?`, deploymentID).Scan(&phase, &migration, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if phase != wantPhase || migration != wantMigration || diagnostic.String != wantDiagnostic {
		t.Fatalf("runtime %s = phase %q migration %q diagnostic %q", deploymentID, phase, migration, diagnostic.String)
	}
}

func assertJob(t *testing.T, db *sql.DB, jobID, wantStatus, wantPhase, wantError string, wantAttempt int, wantInput []byte) {
	t.Helper()
	var status, phase, input string
	var errorCode sql.NullString
	var attempt int
	if err := db.QueryRow(`SELECT status,phase,error_code,attempt,input_json FROM jobs WHERE id=?`, jobID).Scan(&status, &phase, &errorCode, &attempt, &input); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || phase != wantPhase || errorCode.String != wantError || attempt != wantAttempt || input != string(wantInput) {
		t.Fatalf("job %s = status %q phase %q error %q attempt %d input %q", jobID, status, phase, errorCode.String, attempt, input)
	}
}

func assertEventCount(t *testing.T, db *sql.DB, jobID, code string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code=?`, jobID, code).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("job %s event %q count = %d, want %d", jobID, code, count, want)
	}
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
