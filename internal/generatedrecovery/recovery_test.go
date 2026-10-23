package generatedrecovery_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
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
	fixture := newRecoveryActivationFixture(t, false)
	fixture.toSwitching(t)
	fixture.activate(t)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='route_reconciliation_required',pause_disposition='route_reconciliation_required' WHERE id=?`, fixture.jobID); err != nil {
		t.Fatal(err)
	}

	result := fixture.recover(t, fixture.now.Add(time.Minute))
	if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RequeuedGenerated != 0 || result.Jobs.Interrupted != 0 {
		t.Fatalf("recovery = %#v", result)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "draining", "not_required", "")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "waiting_health", "")
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
		fixture := newRecoveryActivationFixture(t, false)
		fixture.toSwitching(t)
		fixture.activate(t)
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
		recoveries int
	}{
		{name: "before active head switch", recoveries: 1},
		{name: "after atomic head switch", switchHead: true, recoveries: 1},
		{name: "draining recovery repeated", switchHead: true, recoveries: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryActivationFixture(t, false)
			fixture.toSwitching(t)
			if test.switchHead {
				fixture.activate(t)
			}
			phase := "switching_route"
			if test.switchHead {
				phase = "draining"
			}
			// Head activation and the draining transition now commit atomically;
			// there is no current-schema positive-head/switching-route window.
			for repeat := 0; repeat < test.recoveries; repeat++ {
				result := fixture.recover(t, fixture.now.Add(time.Duration(repeat+1)*time.Minute))
				if result.Deployments.PreservedGenerated != 1 || result.Deployments.FailedGenerated != 0 || result.Jobs.RequeuedGenerated != 1-repeat || result.Jobs.Interrupted != 0 {
					t.Fatalf("recovery %d = %#v", repeat, result)
				}
				assertRuntime(t, fixture.db, fixture.deploymentID, phase, "not_required", "")
				assertMainDeployment(t, fixture.db, fixture.deploymentID, "waiting_health", "")
				assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
				assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 1)
			}
		})
	}
}

func TestRecoveryPreservesCommittedRuntimeAwaitingDeploymentFinalization(t *testing.T) {
	for _, mainStatus := range []string{"waiting_health", "succeeded"} {
		t.Run(mainStatus, func(t *testing.T) {
			fixture := newRecoveryActivationFixture(t, true)
			fixture.toSwitching(t)
			fixture.activate(t)
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
	fixture := newRecoveryActivationFixture(t, true)
	fixture.toSwitching(t)
	fixture.activate(t)
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
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "waiting_health", "")
		assertJob(t, fixture.db, fixture.jobID, "waiting_user", "route_reconciliation_required", "", 2, fixture.inputJSON)
	}
}

type recoveryFixture struct {
	activation   *recoveryActivationState
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
	return newRecoveryFixtureWithDomain(t, migration, reviewedCurrent, configurationMode, configurationBundleVersion, foreignConfigurationPlan, false)
}

// Only cases that require serving history pay for real domain construction.
// Malformed-input and pre-route recovery fixtures keep their original setup.
func newRecoveryActivationFixture(t *testing.T, reviewedCurrent bool) *recoveryFixture {
	return newRecoveryFixtureWithDomain(t, false, reviewedCurrent, jobs.ConfigurationCurrent, 2, false, true)
}

func newRecoveryFixtureWithDomain(t *testing.T, migration, reviewedCurrent bool, configurationMode jobs.ConfigurationMode, configurationBundleVersion int, foreignConfigurationPlan, authentic bool) *recoveryFixture {
	t.Helper()
	root := t.TempDir()
	var db *sql.DB
	var err error
	var directories runtimedocker.ControllerDirectories
	if authentic {
		directories, err = runtimedocker.PrepareControllerDirectories(root)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lease.Close() })
		db, err = database.OpenForGeneratedDomain(context.Background(), root, lease)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		db, err = database.Open(root)
		if err != nil {
			t.Fatal(err)
		}
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
	if authentic {
		fixture.activation = newRecoveryActivationDomain(t, fixture, lifecycleRepository, root, directories)
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
	if authentic {
		fixture.planID = fixture.activation.authorPlan(t, fixture)
	} else if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,?,?,?,'accepted',?,?)`,
		fixture.planID, fixture.appID, "apps/"+fixture.appID+"/deployment-plans/"+fixture.planID+".secret", strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), migrationDigest, fixture.actorID, timestamp(fixture.now), fixture.actorID, timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	if authentic {
		revision, err := fixture.activation.configuration.ReplaceScoped(context.Background(), fixture.appID, fixture.actorID, appconfig.ScopedReplaceInput{
			PlanRevisionID: fixture.planID, PlanRevisionNumber: 1,
			Components: []appconfig.ComponentTarget{{Name: fixture.component, Role: generatedruntime.RoleServer}},
		})
		if err != nil {
			t.Fatal(err)
		}
		fixture.configID = revision.RevisionID
	} else if reviewedCurrent {
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
	if reviewedCurrent || authentic {
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
	if f.activation != nil {
		f.activation.toSwitching(t, f)
		return
	}
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

// Recovery tests observe SQL behavior; this fixture obtains serving history
// through the real sealed candidate and route operations before those assertions.
// Its runner supplies physical observations, never activation receipts or proof.
type recoveryActivationState struct {
	domain        *generatedingress.GeneratedDomain
	engine        *generatedruntime.Engine
	repository    *deployments.Repository
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	directories   runtimedocker.ControllerDirectories
	runner        *recoveryActivationRunner
	ctx           context.Context
	execution     *generatedingress.GeneratedCandidateExecution
	endpoint      generatedruntime.RouteEndpoint
	releaseScope  func() error
}

func newRecoveryActivationDomain(t *testing.T, fixture *recoveryFixture, lifecycle *applifecycle.Repository,
	root string, directories runtimedocker.ControllerDirectories) *recoveryActivationState {
	t.Helper()
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(fixture.db, fixture.bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(fixture.db, root)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(fixture.db, root)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(fixture.db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	runner := &recoveryActivationRunner{absent: recoveryActivationExitOne(t)}
	process := generateddomain.NewRunner(generateddomain.Options{Fallback: runner, MemoryAvailableBytes: 8 << 30, DiskAvailableBytes: 8 << 30})
	environment := &recoveryActivationEnvironment{}
	options := generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: 64 << 10, ReplacementDiskBytes: 128 << 20,
		Limits: generatedruntime.ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3},
	}
	domain, err := generatedingress.NewGeneratedDomain(context.Background(), stores, process, environment, generatedingress.GeneratedDomainOptions{
		DataRoot: root, DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory,
		WorkingDirectory: options.WorkingDirectory, CommandTimeout: options.CommandTimeout, PullTimeout: 5 * time.Minute,
		HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		MemoryBytes: options.Limits.MemoryBytes, MilliCPUs: options.Limits.MilliCPUs, PIDs: options.Limits.PIDs,
		TmpfsBytes: options.Limits.TmpfsBytes, LogSize: options.Limits.LogSize, LogFiles: options.Limits.LogFiles,
		ReplacementDiskBytes: options.ReplacementDiskBytes,
	})
	if err != nil {
		t.Fatalf("construct authentic recovery domain: %v", err)
	}
	engine, err := generatedruntime.NewEngine(process, environment, domain.Candidates(), options)
	if err != nil {
		t.Fatal(err)
	}
	return &recoveryActivationState{domain: domain, engine: engine, repository: repository, plans: plans,
		configuration: configuration, directories: directories, runner: runner}
}

func (a *recoveryActivationState) authorPlan(t *testing.T, fixture *recoveryFixture) string {
	t.Helper()
	component := deploymentplans.Component{Name: fixture.component, Role: generatedruntime.RoleServer, Technology: "node",
		RootDirectory: "apps/api", PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: "apps/api",
		NodeVersion: "20", BuildCommand: "npm run build", RunCommand: "node server.js", InternalPort: 8080, HealthProbe: "/health"}
	var provenance []deploymentplans.FieldProvenance
	for _, field := range []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe", "technology", "staticOutputDirectory"} {
		provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components." + fixture.component + "." + field,
			Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
	}
	plan, err := a.plans.Replace(context.Background(), fixture.appID, fixture.actorID, deploymentplans.ReplaceInput{Plan: deploymentplans.Plan{
		SetupVersion: projectanalysis.SetupVersion, Strategy: deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "manual-setup", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: strings.Repeat("b", 64)},
		Components: []deploymentplans.Component{component}, FieldProvenance: provenance,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return plan.ID
}

func (a *recoveryActivationState) toSwitching(t *testing.T, f *recoveryFixture) {
	t.Helper()
	if a.execution != nil || f.phase != generatedruntimestate.PhasePreflight {
		t.Fatal("recovery activation fixture already prepared")
	}
	binding, err := f.bindings.Read(context.Background(), f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, f.jobID, f.appID, 2)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), a.directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	a.releaseScope = release
	t.Cleanup(func() {
		if a.releaseScope != nil {
			_ = a.releaseScope()
			a.releaseScope = nil
		}
	})
	a.ctx = deploymenteffects.WithScope(context.Background(), scope)
	f.advance(t, generatedruntimestate.PhaseBuilding)
	configurationNumber := int64(0)
	if f.configID != "" {
		configurationNumber = 1
	}
	request := generatedingress.GeneratedReplacementRequest{Claim: claim, DeploymentID: f.deploymentID, ReleaseID: f.releaseID,
		ConfigurationRevisionID: f.configID, ConfigurationRevisionNumber: configurationNumber,
		DeploymentPlanRevisionID: f.planID, DeploymentPlanRevisionNumber: 1, CandidateSlot: string(generatedruntime.SlotBlue)}
	reservation, err := a.engine.ReserveReplacement(a.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reservation.Release)
	artifactID := f.imageReady(t)
	description, err := generatedruntime.DescribeInactiveCandidate(f.appID, f.component, "")
	if err != nil {
		t.Fatal(err)
	}
	a.runner.container = recoveryActivationContainer{appID: f.appID, deploymentID: f.deploymentID, releaseID: f.releaseID, planID: f.planID,
		status: "absent", pin: applifecycle.RetainedComponent{Name: f.component, Role: generatedruntime.RoleServer,
			ContainerID: strings.Repeat("d", 64), ContainerName: description.ContainerName,
			ArtifactID: artifactID, ImageContentID: "sha256:" + strings.Repeat("b", 64), BuildDefinitionDigest: strings.Repeat("a", 64),
			NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias, InternalPort: 8080,
			WorkingDirectory: "/workspace/apps/api"}}
	if err := a.repository.Gate(a.ctx, deployments.GateRequest{Claim: claim, DeploymentID: f.deploymentID}); err != nil {
		t.Fatal(err)
	}
	f.advance(t, generatedruntimestate.PhaseStartingCandidate)
	execution, err := a.engine.PinCandidates(a.ctx, generatedingress.GeneratedCandidateRequest(request), reservation)
	if err != nil {
		t.Fatal(err)
	}
	a.execution = execution
	t.Cleanup(execution.Leave)
	if err := a.engine.EnsureAppNetwork(a.ctx, deployments.GeneratedNetworkRequest{Claim: claim, DeploymentID: f.deploymentID, ReleaseID: f.releaseID,
		ConfigurationRevisionID: f.configID, ConfigurationRevisionNumber: configurationNumber, DeploymentPlanRevisionID: f.planID, DeploymentPlanRevisionNumber: 1}); err != nil {
		t.Fatal(err)
	}
	invocation, err := execution.BeginCreate(f.component)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.FinishStarting(); err != nil {
		t.Fatal(err)
	}
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if value, err := execution.FinishHealth(); err != nil || value.Phase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatalf("authentic recovery candidate phase=%s error=%v", value.Phase, err)
	}
	a.endpoint = generatedruntime.RouteEndpoint{Component: f.component, Role: generatedruntime.RoleServer,
		ContainerID: identity.ContainerID, NetworkName: identity.NetworkName, NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort}
	f.phase = generatedruntimestate.PhaseSwitchingRoute
}

func (f *recoveryFixture) activate(t *testing.T) generatedruntimestate.ActiveHead {
	t.Helper()
	a := f.activation
	if a == nil || a.execution == nil || f.phase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatal("recovery fixture has no genuine candidate execution")
	}
	activation, err := a.domain.Routes().Pin(a.ctx, a.execution, generatedruntime.RouteSwitchRequest{
		AppID: f.appID, ToSlot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{a.endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := activation.Activate(a.ctx)
	if err != nil || value.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activate recovery fixture: phase=%s error=%v", value.Phase, err)
	}
	head, err := f.state.Active(a.ctx, f.appID)
	if err != nil || head.DeploymentID != f.deploymentID || head.Generation != 1 || head.ActivationReceiptID == "" {
		t.Fatalf("recovery activation head=%#v error=%v", head, err)
	}
	f.phase = generatedruntimestate.PhaseDraining
	a.execution.Leave()
	release := a.releaseScope
	a.releaseScope = nil
	if err := release(); err != nil {
		t.Fatal(err)
	}
	return head
}

type recoveryActivationRunner struct {
	container recoveryActivationContainer
	absent    *exec.ExitError
}

type recoveryActivationContainer struct {
	pin                                                       applifecycle.RetainedComponent
	appID, deploymentID, releaseID, planID, status, receiptID string
}

func (r *recoveryActivationRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture command")
	}
	c := &r.container
	identity := args[len(args)-1]
	marshal := func(value any) (runtimeprocess.CommandResult, error) {
		body, err := json.Marshal(value)
		return runtimeprocess.CommandResult{Stdout: body}, err
	}
	switch args[0] + " " + args[1] {
	case "network inspect":
		if identity != c.pin.NetworkName {
			return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture network")
		}
		return marshal(map[string]any{"name": c.pin.NetworkName, "driver": "bridge", "scope": "local", "internal": false,
			"labels": map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": c.appID}})
	case "image inspect":
		if identity != c.pin.ImageContentID {
			return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture image")
		}
		return marshal(map[string]any{"id": c.pin.ImageContentID, "size": 1, "user": "node", "workingDir": "/workspace",
			"entrypoint": []string{generatedcontainer.Entrypoint}, "labels": map[string]string{
				"io.rig.managed": "generated-image", "io.rig.application": c.appID, "io.rig.release": c.releaseID,
				"io.rig.artifact": c.pin.ArtifactID, "io.rig.plan": c.planID, "io.rig.component": c.pin.Name,
				"io.rig.role": c.pin.Role, "io.rig.definition": c.pin.BuildDefinitionDigest}})
	case "container inspect":
		if identity != c.pin.ContainerName && identity != c.pin.ContainerID {
			return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture container")
		}
		if c.status == "absent" {
			return runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + identity)}, r.absent
		}
		return marshal(c.inspection())
	case "container create":
		name, receipt := "", ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--name" {
				name = args[i+1]
			}
			if args[i] == "--label" && strings.HasPrefix(args[i+1], generatedcontainer.ReceiptLabel+"=") {
				receipt = strings.TrimPrefix(args[i+1], generatedcontainer.ReceiptLabel+"=")
			}
		}
		if name != c.pin.ContainerName || receipt == "" || c.status != "absent" {
			return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture create")
		}
		c.status, c.receiptID = "created", receipt
		return runtimeprocess.CommandResult{Stdout: []byte(c.pin.ContainerID + "\n")}, nil
	case "container start":
		if identity != c.pin.ContainerID || c.status != "created" {
			return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture start")
		}
		c.status = "running"
		return runtimeprocess.CommandResult{}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected recovery fixture command")
	}
}

type recoveryActivationEnvironment struct{}

func (*recoveryActivationEnvironment) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("recovery fixture must not stage environment")
}
func (*recoveryActivationEnvironment) StageGeneratedCandidate(string, int, []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	return nil, errors.New("recovery fixture must not stage candidate environment")
}

// A completed process exit is required by the strict absence observer.
func TestRecoveryActivationExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_RECOVERY_ACTIVATION_EXIT") == "1" {
		os.Exit(1)
	}
}
func recoveryActivationExitOne(t *testing.T) *exec.ExitError {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestRecoveryActivationExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_RECOVERY_ACTIVATION_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected completed exit one, got %T %v", err, err)
	}
	return exited
}

func (c *recoveryActivationContainer) inspection() map[string]any {
	running := c.status == "running" || c.status == "paused" || c.status == "restarting"
	networks := map[string]any(nil)
	if running {
		networks = map[string]any{c.pin.NetworkName: map[string]any{"Aliases": []string{c.pin.NetworkAlias}}}
	}
	startedAt := "2026-10-01T00:00:00Z"
	if c.status == "created" {
		startedAt = "0001-01-01T00:00:00Z"
	}
	return map[string]any{
		"id": c.pin.ContainerID, "name": "/" + c.pin.ContainerName, "image": c.pin.ImageContentID,
		"configImage": c.pin.ImageContentID, "entrypoint": []string{generatedcontainer.Entrypoint},
		"volumes": map[string]any{}, "hostMounts": []any{}, "mounts": []any{}, "startedAt": startedAt,
		"labels": map[string]string{"io.rig.managed": "generated-runtime", "io.rig.application": c.appID,
			"io.rig.release": c.releaseID, "io.rig.deployment": c.deploymentID, "io.rig.artifact": c.pin.ArtifactID,
			"io.rig.plan": c.planID, "io.rig.component": c.pin.Name, "io.rig.role": c.pin.Role, "io.rig.slot": "blue", generatedcontainer.ReceiptLabel: c.receiptID},
		"user": "node", "workingDir": c.pin.WorkingDirectory, "cmd": []string{"/bin/sh", "-lc", "node server.js"},
		"healthTest":     []string{"CMD-SHELL", `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`},
		"healthInterval": int64(2 * time.Second), "healthTimeout": int64(2 * time.Second), "healthStartPeriod": int64(5 * time.Second), "healthRetries": 3,
		"memory": int64(512 << 20), "memorySwap": int64(512 << 20), "nanoCpus": int64(1_000_000_000), "pidsLimit": int64(256),
		"ulimits": []map[string]any{{"Name": "nofile", "Soft": int64(1024), "Hard": int64(1024)}}, "init": true,
		"networkMode": c.pin.NetworkName, "readonlyRootfs": true, "privileged": false, "capAdd": []string{}, "capDrop": []string{"ALL"},
		"securityOpt": []string{"no-new-privileges:true"}, "binds": []string{}, "portBindings": map[string]any{},
		"tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864"}, "logType": "local", "logConfig": map[string]string{"max-size": "10m", "max-file": "3"}, "restart": "no",
		"status": c.status, "running": running, "paused": c.status == "paused", "restarting": c.status == "restarting", "dead": c.status == "dead",
		"exitCode": 0, "health": map[bool]string{true: "healthy", false: ""}[running], "networks": networks,
	}
}
