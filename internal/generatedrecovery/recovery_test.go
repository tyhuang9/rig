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
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

func TestRepositoryRecoveryPreservesGeneratedAndKeepsComposeBehavior(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	composeDeploymentID, composeJobID := fixture.seedComposeDeployment(t)

	result := fixture.recover(t, fixture.now.Add(time.Minute))
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertRuntime(t, fixture.db, fixture.deploymentID, "preflight", "not_required", "")
	assertMainDeployment(t, fixture.db, composeDeploymentID, "failed", "daemon_restarted")
	if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedOther != 1 || result.Jobs.RequeuedGenerated != 1 || result.Jobs.Interrupted != 1 {
		t.Fatalf("recovery = %#v", result)
	}
	assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
	assertRequeueReset(t, fixture.db, fixture.jobID)
	assertJob(t, fixture.db, composeJobID, "interrupted", "interrupted", "daemon_restarted", 1, []byte("{}"))
	assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 1)

	// Startup recovery can be retried after a partial daemon initialization.
	// The already requeued generated job remains eligible and gets no duplicate
	// recovery event.
	result = fixture.recover(t, fixture.now.Add(2*time.Minute))
	if result.Jobs.RequeuedGenerated != 0 || result.Jobs.Interrupted != 0 {
		t.Fatalf("repeat recovery = %#v", result)
	}
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
	assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 1)
}

func TestRecoveryRequeuesSafeGeneratedContinuationPoints(t *testing.T) {
	for _, test := range []struct {
		name           string
		migration      bool
		prepare        func(*testing.T, *recoveryFixture)
		phase          string
		migrationState string
	}{
		{
			name: "migration not started", migration: true, phase: "migrating", migrationState: "pending",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseMigrating)
			},
		},
		{
			name: "migration already succeeded", migration: true, phase: "migrating", migrationState: "succeeded",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseMigrating)
				if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.state.FinishMigration(context.Background(), fixture.appID, fixture.deploymentID, true); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "candidate has durable container id", phase: "starting_candidate", migrationState: "not_required",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
				fixture.startRunning(t)
			},
		},
		{
			name: "candidate awaiting health", phase: "waiting_health", migrationState: "not_required",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
				fixture.startRunning(t)
				fixture.advance(t, generatedruntimestate.PhaseWaitingHealth)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, test.migration)
			test.prepare(t, fixture)
			result := fixture.recover(t, fixture.now.Add(time.Minute))
			if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RequeuedGenerated != 1 || result.Jobs.Interrupted != 0 {
				t.Fatalf("recovery = %#v", result)
			}
			assertRuntime(t, fixture.db, fixture.deploymentID, test.phase, test.migrationState, "")
			assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
			assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
		})
	}
}

func TestRecoveryPreservesExplicitRouteReconciliationPause(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	fixture.toSwitching(t)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='route_reconciliation_required',pause_disposition='route_reconciliation_required' WHERE id=?`, fixture.jobID); err != nil {
		t.Fatal(err)
	}

	result := fixture.recover(t, fixture.now.Add(time.Minute))
	if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RequeuedGenerated != 0 || result.Jobs.Interrupted != 0 {
		t.Fatalf("recovery = %#v", result)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "switching_route", "not_required", "")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertJob(t, fixture.db, fixture.jobID, "waiting_user", "route_reconciliation_required", "", 2, fixture.inputJSON)
	var pause string
	if err := fixture.db.QueryRow(`SELECT pause_disposition FROM jobs WHERE id=?`, fixture.jobID).Scan(&pause); err != nil || pause != "route_reconciliation_required" {
		t.Fatalf("pause disposition = %q, %v", pause, err)
	}
}

func TestRecoveryPreservesExplicitRouteReconciliationPauseWhileDraining(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	fixture.toSwitching(t)
	if _, switched, err := fixture.state.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || !switched {
		t.Fatalf("switch active: switched=%t err=%v", switched, err)
	}
	fixture.advance(t, generatedruntimestate.PhaseDraining)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='route_reconciliation_required',pause_disposition='route_reconciliation_required' WHERE id=?`, fixture.jobID); err != nil {
		t.Fatal(err)
	}

	result := fixture.recover(t, fixture.now.Add(time.Minute))
	if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RequeuedGenerated != 0 || result.Jobs.Interrupted != 0 {
		t.Fatalf("recovery = %#v", result)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "draining", "not_required", "")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertJob(t, fixture.db, fixture.jobID, "waiting_user", "route_reconciliation_required", "", 2, fixture.inputJSON)
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

		result := fixture.recover(t, fixture.now.Add(time.Minute))
		if result.Deployments.FailedGenerated != 1 || result.Deployments.PreservedGenerated != 0 || result.Jobs.Interrupted != 1 {
			t.Fatalf("recovery = %#v", result)
		}
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "failed", "daemon_restarted")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
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

		fixture.recover(t, fixture.now.Add(time.Minute))
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

		fixture.recover(t, fixture.now.Add(time.Minute))
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
	})

	t.Run("job input no longer matches pinned provenance", func(t *testing.T) {
		fixture := newRecoveryFixture(t, false)
		wrongInput, err := json.Marshal(jobs.DeploymentInput{ReleaseID: fixture.releaseID, ConfigurationMode: jobs.ConfigurationOriginal})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE jobs SET input_json=? WHERE id=?`, string(wrongInput), fixture.jobID); err != nil {
			t.Fatal(err)
		}

		fixture.recover(t, fixture.now.Add(time.Minute))
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
	})

	t.Run("post switch artifact provenance is unavailable", func(t *testing.T) {
		fixture := newRecoveryFixture(t, false)
		fixture.toSwitching(t)
		if _, switched, err := fixture.state.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || !switched {
			t.Fatalf("switch active: switched=%t err=%v", switched, err)
		}
		var artifactID string
		if err := fixture.db.QueryRow(`SELECT image_artifact_id FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, fixture.deploymentID, fixture.component).Scan(&artifactID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE generated_image_artifacts SET state='unavailable',updated_at=? WHERE id=? AND state='ready'`, timestamp(fixture.now.Add(time.Second)), artifactID); err != nil {
			t.Fatal(err)
		}

		fixture.recover(t, fixture.now.Add(time.Minute))
		assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
		assertComponent(t, fixture.db, fixture.deploymentID, fixture.component, "failed", "daemon_restarted")
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

			result := fixture.recover(t, fixture.now.Add(time.Minute))
			if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RequeuedGenerated != 1 || result.Jobs.Interrupted != 0 {
				t.Fatalf("recovery = %#v", result)
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

func TestRecoveryPreservesCommittedRuntimeAwaitingDeploymentFinalization(t *testing.T) {
	for _, mainStatus := range []string{"applying", "succeeded"} {
		t.Run(mainStatus, func(t *testing.T) {
			fixture := newRecoveryFixtureWithReviewedCurrent(t, false, true)
			fixture.toSwitching(t)
			if _, switched, err := fixture.state.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || !switched {
				t.Fatalf("switch active: switched=%t err=%v", switched, err)
			}
			fixture.advance(t, generatedruntimestate.PhaseDraining)
			fixture.advance(t, generatedruntimestate.PhaseSucceeded)
			if mainStatus == "succeeded" {
				if _, err := fixture.db.Exec(`UPDATE deployments SET status='succeeded',finished_at=? WHERE id=?`, timestamp(fixture.now), fixture.deploymentID); err != nil {
					t.Fatal(err)
				}
			}

			for repeat := 0; repeat < 2; repeat++ {
				result := fixture.recover(t, fixture.now.Add(time.Duration(repeat+1)*time.Minute))
				if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.Interrupted != 0 || result.Jobs.RequeuedGenerated != 1-repeat {
					t.Fatalf("recovery %d = %#v", repeat, result)
				}
				assertRuntime(t, fixture.db, fixture.deploymentID, "succeeded", "not_required", "")
				assertMainDeployment(t, fixture.db, fixture.deploymentID, mainStatus, "")
				assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
			}
		})
	}
}

func TestRecoveryPausesUncertainCommittedRuntimeWithoutDeletingServingState(t *testing.T) {
	fixture := newRecoveryFixtureWithReviewedCurrent(t, false, true)
	fixture.toSwitching(t)
	if _, switched, err := fixture.state.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || !switched {
		t.Fatalf("switch active: switched=%t err=%v", switched, err)
	}
	fixture.advance(t, generatedruntimestate.PhaseDraining)
	fixture.advance(t, generatedruntimestate.PhaseSucceeded)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_external',phase='cancelling' WHERE id=?`, fixture.jobID); err != nil {
		t.Fatal(err)
	}

	for repeat := 0; repeat < 2; repeat++ {
		result := fixture.recover(t, fixture.now.Add(time.Duration(repeat+1)*time.Minute))
		if result.Deployments.PreservedGenerated != 0 || result.Deployments.PausedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.Interrupted != 0 || result.Jobs.PausedGenerated != 1-repeat {
			t.Fatalf("recovery %d = %#v", repeat, result)
		}
		assertRuntime(t, fixture.db, fixture.deploymentID, "succeeded", "not_required", "")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
		assertJob(t, fixture.db, fixture.jobID, "waiting_user", "route_reconciliation_required", "", 2, fixture.inputJSON)
	}
}

type recoveryFixture struct {
	db           *sql.DB
	bindings     *deploymentbinding.Repository
	state        *generatedruntimestate.Repository
	now          time.Time
	actorID      string
	appID        string
	jobID        string
	deploymentID string
	releaseID    string
	planID       string
	configID     string
	component    string
	inputJSON    []byte
	phase        generatedruntimestate.Phase
}

func newRecoveryFixture(t *testing.T, migration bool) *recoveryFixture {
	return newRecoveryFixtureWithReviewedCurrent(t, migration, false)
}

func newRecoveryFixtureWithReviewedCurrent(t *testing.T, migration, reviewedCurrent bool) *recoveryFixture {
	return newRecoveryFixtureWithReviewedConfiguration(t, migration, reviewedCurrent, jobs.ConfigurationCurrent, 2, false)
}

func newRecoveryFixtureWithConfigurationMode(t *testing.T, migration, reviewedCurrent bool, configurationMode jobs.ConfigurationMode) *recoveryFixture {
	return newRecoveryFixtureWithReviewedConfiguration(t, migration, reviewedCurrent, configurationMode, 2, false)
}

func newRecoveryFixtureWithReviewedConfigurationVersion(t *testing.T, migration bool, bundleVersion int) *recoveryFixture {
	return newRecoveryFixtureWithReviewedConfiguration(t, migration, true, jobs.ConfigurationCurrent, bundleVersion, false)
}

func newRecoveryFixtureWithForeignReviewedConfigurationPlan(t *testing.T) *recoveryFixture {
	return newRecoveryFixtureWithReviewedConfiguration(t, false, true, jobs.ConfigurationCurrent, 2, true)
}

func newRecoveryFixtureWithReviewedConfiguration(t *testing.T, migration, reviewedCurrent bool, configurationMode jobs.ConfigurationMode, configurationBundleVersion int, foreignConfigurationPlan bool) *recoveryFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
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
	fixture := &recoveryFixture{
		db: db, bindings: bindings, state: generatedruntimestate.New(db), now: time.Date(2026, time.August, 31, 18, 0, 0, 0, time.UTC),
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
	if reviewedCurrent {
		fixture.configID = uuid.NewString()
		configurationPlanID := any(fixture.planID)
		configurationPlanNumber := any(int64(1))
		if foreignConfigurationPlan {
			foreignPlanID := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
				id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
				analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
				field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
			) VALUES(?,?,2,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
				foreignPlanID, fixture.appID, "apps/"+fixture.appID+"/deployment-plans/"+foreignPlanID+".secret", strings.Repeat("d", 64),
				strings.Repeat("e", 64), strings.Repeat("f", 64), fixture.actorID, timestamp(fixture.now), fixture.actorID, timestamp(fixture.now)); err != nil {
				t.Fatal(err)
			}
			configurationPlanID, configurationPlanNumber = foreignPlanID, int64(2)
		}
		if configurationBundleVersion == 1 {
			configurationPlanID, configurationPlanNumber = nil, nil
		}
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(
			id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
			bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,1,?,?,?,0,0,?,?,?)`, fixture.configID, fixture.appID,
			"apps/"+fixture.appID+"/configuration/"+fixture.configID+".secret", fixture.actorID, timestamp(fixture.now), configurationBundleVersion, configurationPlanID, configurationPlanNumber); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE application_configuration_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, fixture.configID, timestamp(fixture.now), fixture.appID); err != nil {
			t.Fatal(err)
		}
	}
	if migration {
		if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,?,?)`, fixture.planID, fixture.appID, fixture.actorID, timestamp(fixture.now)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, fixture.releaseID, fixture.appID, timestamp(fixture.now), strings.Repeat("b", 64), strings.Repeat("f", 64), fixture.planID); err != nil {
		t.Fatal(err)
	}
	input := jobs.DeploymentInput{ReleaseID: fixture.releaseID, ConfigurationMode: configurationMode}
	if reviewedCurrent {
		input = jobs.DeploymentInput{
			ConfigurationMode:                   jobs.ConfigurationCurrent,
			ExpectedPlanRevisionID:              fixture.planID,
			ExpectedPlanRevisionNumber:          1,
			ExpectedConfigurationRevisionID:     fixture.configID,
			ExpectedConfigurationRevisionNumber: 1,
		}
	}
	fixture.inputJSON, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,?,?,?)`, fixture.jobID, fixture.appID, fixture.actorID, string(fixture.inputJSON), timestamp(fixture.now), timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	fixture.bindJob(t, fixture.jobID)
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='apply_runtime',attempt=2,progress_percent=60,checkpoint_json='{"phase":"apply_runtime"}',started_at=?,updated_at=? WHERE id=?`, timestamp(fixture.now), timestamp(fixture.now), fixture.jobID); err != nil {
		t.Fatal(err)
	}
	configurationID := any(nil)
	configurationNumber := int64(0)
	if reviewedCurrent {
		configurationID, configurationNumber = fixture.configID, 1
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number,started_at) VALUES(?,?,?,?, 'applying',?,?,?,1,'generated_node',?,1,?)`, fixture.deploymentID, fixture.appID, fixture.releaseID, fixture.jobID, configurationMode, configurationID, configurationNumber, fixture.planID, timestamp(fixture.now)); err != nil {
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
	f.startRunning(t)
	f.advance(t, generatedruntimestate.PhaseWaitingHealth)
	if _, err := f.state.AdvanceComponent(context.Background(), f.appID, f.deploymentID, f.component, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	f.advance(t, generatedruntimestate.PhaseSwitchingRoute)
}

func (f *recoveryFixture) startRunning(t *testing.T) {
	t.Helper()
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
}

func (f *recoveryFixture) seedComposeDeployment(t *testing.T) (string, string) {
	t.Helper()
	appID, jobID, deploymentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, appID, timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,'{}',?,?)`, jobID, appID, f.actorID, timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	f.bindJob(t, jobID)
	if _, err := f.db.Exec(`UPDATE jobs SET status='running',phase='apply_runtime',attempt=1,started_at=?,updated_at=? WHERE id=?`, timestamp(f.now), timestamp(f.now), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,started_at) VALUES(?,?,?,'applying','current',1,'compose',?)`, deploymentID, appID, jobID, timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	return deploymentID, jobID
}

func (f *recoveryFixture) bindJob(t *testing.T, jobID string) {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := f.bindings.InsertNewTx(context.Background(), tx, jobID, f.now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f *recoveryFixture) recover(t *testing.T, now time.Time) generatedrecovery.RuntimeResult {
	t.Helper()
	result, err := generatedrecovery.RecoverRuntime(context.Background(), f.db, now, f.bindings)
	if err != nil {
		t.Fatal(err)
	}
	return result
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

func assertComponent(t *testing.T, db *sql.DB, deploymentID, componentName, wantState, wantDiagnostic string) {
	t.Helper()
	var state string
	var diagnostic sql.NullString
	if err := db.QueryRow(`SELECT state,diagnostic_code FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, deploymentID, componentName).Scan(&state, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if state != wantState || diagnostic.String != wantDiagnostic {
		t.Fatalf("component %s/%s = state %q diagnostic %q", deploymentID, componentName, state, diagnostic.String)
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

func assertRequeueReset(t *testing.T, db *sql.DB, jobID string) {
	t.Helper()
	var progress int
	var checkpoint string
	var started, finished sql.NullString
	if err := db.QueryRow(`SELECT progress_percent,checkpoint_json,started_at,finished_at FROM jobs WHERE id=?`, jobID).Scan(&progress, &checkpoint, &started, &finished); err != nil {
		t.Fatal(err)
	}
	if progress != 0 || checkpoint != "{}" || started.Valid || finished.Valid {
		t.Fatalf("requeued job state = progress %d checkpoint %q started %q finished %q", progress, checkpoint, started.String, finished.String)
	}
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
