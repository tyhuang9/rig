package generatedrecovery_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

func TestRecoveryPreservesStrictReviewedCurrentInput(t *testing.T) {
	fixture := newRecoveryFixtureWithReviewedCurrent(t, false, true)
	fixture.advance(t, generatedruntimestate.PhaseBuilding)

	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "building", "not_required", "")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
	assertRequeueReset(t, fixture.db, fixture.jobID)
	assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 1)
}

func TestRecoveryPreservesCapacityPauseWithStrictReviewedCurrentInput(t *testing.T) {
	fixture := newRecoveryFixtureWithReviewedCurrent(t, false, true)
	fixture.advance(t, generatedruntimestate.PhaseBuilding)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='insufficient_replacement_capacity',pause_disposition='insufficient_replacement_capacity' WHERE id=?`, fixture.jobID); err != nil {
		t.Fatal(err)
	}

	for run := 0; run < 2; run++ {
		if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
			t.Fatal(err)
		}
		assertRuntime(t, fixture.db, fixture.deploymentID, "building", "not_required", "")
		assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
		assertJob(t, fixture.db, fixture.jobID, "waiting_user", "insufficient_replacement_capacity", "", 2, fixture.inputJSON)
		assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 0)
	}
}

func TestRecoveryPreservesStrictReviewedCurrentInputAtSafeContinuationPoints(t *testing.T) {
	for _, test := range []struct {
		name           string
		migration      bool
		prepare        func(*testing.T, *recoveryFixture)
		phase          string
		migrationState string
	}{
		{
			name: "building", phase: "building", migrationState: "not_required",
			prepare: func(t *testing.T, fixture *recoveryFixture) { fixture.advance(t, generatedruntimestate.PhaseBuilding) },
		},
		{
			name: "migration pending", migration: true, phase: "migrating", migrationState: "pending",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseMigrating)
			},
		},
		{
			name: "migration succeeded", migration: true, phase: "migrating", migrationState: "succeeded",
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
			name: "starting candidate", phase: "starting_candidate", migrationState: "not_required",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
				fixture.startRunning(t)
			},
		},
		{
			name: "waiting health", phase: "waiting_health", migrationState: "not_required",
			prepare: func(t *testing.T, fixture *recoveryFixture) {
				fixture.advance(t, generatedruntimestate.PhaseBuilding)
				fixture.imageReady(t)
				fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
				fixture.startRunning(t)
				fixture.advance(t, generatedruntimestate.PhaseWaitingHealth)
			},
		},
		{
			name: "switching route", phase: "switching_route", migrationState: "not_required",
			prepare: func(t *testing.T, fixture *recoveryFixture) { fixture.toSwitching(t) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixtureWithReviewedCurrent(t, test.migration, true)
			test.prepare(t, fixture)
			if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
				t.Fatal(err)
			}
			assertRuntime(t, fixture.db, fixture.deploymentID, test.phase, test.migrationState, "")
			assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
			assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
		})
	}
}

func TestRecoveryPreservesReviewedCurrentZeroConfigurationAndLegacyInputs(t *testing.T) {
	for _, test := range []struct {
		name     string
		original bool
		input    func(*testing.T, *recoveryFixture) []byte
	}{
		{
			name: "reviewed zero configuration",
			input: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: fixture.planID, ExpectedPlanRevisionNumber: 1})
			},
		},
		{
			name: "legacy current",
			input: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ReleaseID: fixture.releaseID, ConfigurationMode: jobs.ConfigurationCurrent})
			},
		},
		{
			name: "legacy original", original: true,
			input: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ReleaseID: fixture.releaseID, ConfigurationMode: jobs.ConfigurationOriginal})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, false)
			if test.original {
				fixture = newRecoveryFixtureWithConfigurationMode(t, false, false, jobs.ConfigurationOriginal)
			}
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			input := test.input(t, fixture)
			setRecoveryInput(t, fixture, input)
			if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
				t.Fatal(err)
			}
			assertRuntime(t, fixture.db, fixture.deploymentID, "building", "not_required", "")
			assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, input)
		})
	}
}

func TestRecoveryPreservesRecordedReviewedPinsAfterConfigurationHeadDrift(t *testing.T) {
	fixture := newRecoveryFixtureWithReviewedCurrent(t, false, true)
	fixture.advance(t, generatedruntimestate.PhaseBuilding)
	assertConfigurationHead(t, fixture, fixture.configID, 1)
	newHeadID := uuid.NewString()
	if _, err := fixture.db.Exec(`INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,2,?,?,?,0,0,2,?,1)`, newHeadID, fixture.appID,
		"apps/"+fixture.appID+"/configuration/"+newHeadID+".secret", fixture.actorID, timestamp(fixture.now), fixture.planID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE application_configuration_heads SET revision_id=?,revision_number=2,updated_at=? WHERE app_id=?`, newHeadID, timestamp(fixture.now), fixture.appID); err != nil {
		t.Fatal(err)
	}
	assertConfigurationHead(t, fixture, newHeadID, 2)

	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "building", "not_required", "")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
	assertJob(t, fixture.db, fixture.jobID, "queued", "queued", "", 2, fixture.inputJSON)
	var actualID string
	var actualNumber int64
	if err := fixture.db.QueryRow(`SELECT actual_configuration_revision_id,actual_configuration_revision_number FROM deployments WHERE id=?`, fixture.deploymentID).Scan(&actualID, &actualNumber); err != nil || actualID != fixture.configID || actualNumber != 1 {
		t.Fatalf("actual configuration pin=%q/%d err=%v", actualID, actualNumber, err)
	}
}

func TestRecoveryFailsClosedForInvalidReviewedJobInputWithoutRewritingBytes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *recoveryFixture) []byte
	}{
		{
			name: "unknown field",
			mutate: func(_ *testing.T, fixture *recoveryFixture) []byte {
				return append(append([]byte(nil), fixture.inputJSON[:len(fixture.inputJSON)-1]...), []byte(`,"unexpected":true}`)...)
			},
		},
		{name: "trailing document", mutate: func(_ *testing.T, fixture *recoveryFixture) []byte {
			return append(append([]byte(nil), fixture.inputJSON...), []byte(` {}`)...)
		}},
		{name: "malformed json", mutate: func(_ *testing.T, _ *recoveryFixture) []byte { return []byte(`{"configurationMode":`) }},
		{
			name: "mixed release and reviewed pins",
			mutate: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ReleaseID: fixture.releaseID, ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: fixture.planID, ExpectedPlanRevisionNumber: 1, ExpectedConfigurationRevisionID: fixture.configID, ExpectedConfigurationRevisionNumber: 1})
			},
		},
		{
			name: "wrong plan pin",
			mutate: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: uuid.NewString(), ExpectedPlanRevisionNumber: 1, ExpectedConfigurationRevisionID: fixture.configID, ExpectedConfigurationRevisionNumber: 1})
			},
		},
		{
			name: "plan number mismatch",
			mutate: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: fixture.planID, ExpectedPlanRevisionNumber: 2, ExpectedConfigurationRevisionID: fixture.configID, ExpectedConfigurationRevisionNumber: 1})
			},
		},
		{
			name: "configuration number mismatch",
			mutate: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: fixture.planID, ExpectedPlanRevisionNumber: 1, ExpectedConfigurationRevisionID: fixture.configID, ExpectedConfigurationRevisionNumber: 2})
			},
		},
		{
			name: "malformed plan id and number pair",
			mutate: func(_ *testing.T, fixture *recoveryFixture) []byte {
				return []byte(`{"releaseId":"","configurationMode":"current","expectedPlanRevisionId":"","expectedPlanRevisionNumber":1,"expectedConfigurationRevisionId":"` + fixture.configID + `","expectedConfigurationRevisionNumber":1}`)
			},
		},
		{
			name: "malformed configuration id and number pair",
			mutate: func(_ *testing.T, fixture *recoveryFixture) []byte {
				return []byte(`{"releaseId":"","configurationMode":"current","expectedPlanRevisionId":"` + fixture.planID + `","expectedPlanRevisionNumber":1,"expectedConfigurationRevisionId":"","expectedConfigurationRevisionNumber":1}`)
			},
		},
		{
			name: "invalid reviewed uuid",
			mutate: func(_ *testing.T, fixture *recoveryFixture) []byte {
				return []byte(`{"releaseId":"","configurationMode":"current","expectedPlanRevisionId":"not-a-uuid","expectedPlanRevisionNumber":1,"expectedConfigurationRevisionId":"` + fixture.configID + `","expectedConfigurationRevisionNumber":1}`)
			},
		},
		{
			name: "reviewed pins with original configuration mode",
			mutate: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationOriginal, ExpectedPlanRevisionID: fixture.planID, ExpectedPlanRevisionNumber: 1, ExpectedConfigurationRevisionID: fixture.configID, ExpectedConfigurationRevisionNumber: 1})
			},
		},
		{
			name: "mismatched missing configuration pin",
			mutate: func(t *testing.T, fixture *recoveryFixture) []byte {
				return marshalRecoveryInput(t, jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent, ExpectedPlanRevisionID: fixture.planID, ExpectedPlanRevisionNumber: 1, ExpectedConfigurationRevisionID: uuid.NewString(), ExpectedConfigurationRevisionNumber: 1})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixtureWithReviewedCurrent(t, false, true)
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			input := test.mutate(t, fixture)
			setRecoveryInput(t, fixture, input)
			if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
			assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
			if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
				t.Fatal(err)
			}
			assertJob(t, fixture.db, fixture.jobID, "interrupted", "interrupted", "daemon_restarted", 2, input)
		})
	}
}

func TestRecoveryFailsClosedForReviewedCurrentBoundToVersionOneConfiguration(t *testing.T) {
	fixture := newRecoveryFixtureWithReviewedConfigurationVersion(t, false, 1)
	fixture.advance(t, generatedruntimestate.PhaseBuilding)

	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
	if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	assertJob(t, fixture.db, fixture.jobID, "interrupted", "interrupted", "daemon_restarted", 2, fixture.inputJSON)
}

func TestRecoveryFailsClosedForReviewedCurrentBoundToAnotherAcceptedPlan(t *testing.T) {
	fixture := newRecoveryFixtureWithForeignReviewedConfigurationPlan(t)
	fixture.advance(t, generatedruntimestate.PhaseBuilding)

	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "not_required", "daemon_restarted")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
	if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	assertJob(t, fixture.db, fixture.jobID, "interrupted", "interrupted", "daemon_restarted", 2, fixture.inputJSON)
}

func TestRecoveryFailsClosedWhenReviewedMigrationApprovalIsMissing(t *testing.T) {
	fixture := newRecoveryFixtureWithReviewedCurrent(t, true, true)
	fixture.advance(t, generatedruntimestate.PhaseBuilding)
	fixture.imageReady(t)
	fixture.advance(t, generatedruntimestate.PhaseMigrating)
	if _, err := fixture.db.Exec(`DELETE FROM deployment_plan_migration_approvals WHERE revision_id=? AND app_id=?`, fixture.planID, fixture.appID); err != nil {
		t.Fatal(err)
	}

	if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRuntime(t, fixture.db, fixture.deploymentID, "failed", "pending", "daemon_restarted")
	assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
}

func TestRecoveryPreservesReviewedCapacityPausesAtSafeContinuationPoints(t *testing.T) {
	for _, test := range []struct {
		name           string
		prepare        func(*testing.T, *recoveryFixture)
		phase          string
		migrationState string
		migration      bool
	}{
		{name: "building", phase: "building", migrationState: "not_required", prepare: func(t *testing.T, fixture *recoveryFixture) { fixture.advance(t, generatedruntimestate.PhaseBuilding) }},
		{name: "migration pending", migration: true, phase: "migrating", migrationState: "pending", prepare: func(t *testing.T, fixture *recoveryFixture) {
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			fixture.imageReady(t)
			fixture.advance(t, generatedruntimestate.PhaseMigrating)
		}},
		{name: "migration succeeded", migration: true, phase: "migrating", migrationState: "succeeded", prepare: func(t *testing.T, fixture *recoveryFixture) {
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			fixture.imageReady(t)
			fixture.advance(t, generatedruntimestate.PhaseMigrating)
			if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.state.FinishMigration(context.Background(), fixture.appID, fixture.deploymentID, true); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "starting candidate", phase: "starting_candidate", prepare: func(t *testing.T, fixture *recoveryFixture) {
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			fixture.imageReady(t)
			fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
			fixture.startRunning(t)
		}, migrationState: "not_required"},
		{name: "waiting health", phase: "waiting_health", prepare: func(t *testing.T, fixture *recoveryFixture) {
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			fixture.imageReady(t)
			fixture.advance(t, generatedruntimestate.PhaseStartingCandidate)
			fixture.startRunning(t)
			fixture.advance(t, generatedruntimestate.PhaseWaitingHealth)
		}, migrationState: "not_required"},
		{name: "switching route", phase: "switching_route", migrationState: "not_required", prepare: func(t *testing.T, fixture *recoveryFixture) { fixture.toSwitching(t) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixtureWithReviewedCurrent(t, test.migration, true)
			test.prepare(t, fixture)
			setCapacityPause(t, fixture, "insufficient_replacement_capacity")
			for run := 0; run < 2; run++ {
				if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := jobs.New(fixture.db).RecoverInterrupted(); err != nil {
					t.Fatal(err)
				}
				assertRuntime(t, fixture.db, fixture.deploymentID, test.phase, test.migrationState, "")
				assertMainDeployment(t, fixture.db, fixture.deploymentID, "applying", "")
				assertJob(t, fixture.db, fixture.jobID, "waiting_user", "insufficient_replacement_capacity", "", 2, fixture.inputJSON)
				assertEventCount(t, fixture.db, fixture.jobID, "daemon_restarted", 0)
			}
		})
	}
}

func TestRecoveryRejectsCapacityPauseOutsideSafeContinuation(t *testing.T) {
	for _, test := range []struct {
		name           string
		prepare        func(*testing.T, *recoveryFixture)
		pause          string
		migration      bool
		migrationState string
	}{
		{name: "preflight", migrationState: "not_required", prepare: func(_ *testing.T, _ *recoveryFixture) {}, pause: "insufficient_replacement_capacity"},
		{name: "draining", prepare: func(t *testing.T, fixture *recoveryFixture) {
			fixture.toSwitching(t)
			if _, switched, err := fixture.state.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || !switched {
				t.Fatalf("switch active: switched=%t err=%v", switched, err)
			}
			fixture.advance(t, generatedruntimestate.PhaseDraining)
		}, pause: "insufficient_replacement_capacity", migrationState: "not_required"},
		{name: "mismatched disposition", migrationState: "not_required", prepare: func(t *testing.T, fixture *recoveryFixture) { fixture.advance(t, generatedruntimestate.PhaseBuilding) }, pause: "route_reconciliation_required"},
		{name: "migration running", prepare: func(t *testing.T, fixture *recoveryFixture) {
			fixture.advance(t, generatedruntimestate.PhaseBuilding)
			fixture.imageReady(t)
			fixture.advance(t, generatedruntimestate.PhaseMigrating)
			if _, err := fixture.state.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); err != nil {
				t.Fatal(err)
			}
		}, pause: "insufficient_replacement_capacity", migration: true, migrationState: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixtureWithReviewedCurrent(t, test.migration, true)
			test.prepare(t, fixture)
			setCapacityPause(t, fixture, test.pause)
			if err := deployments.New(fixture.db).Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertRuntime(t, fixture.db, fixture.deploymentID, "failed", test.migrationState, "daemon_restarted")
			assertMainDeployment(t, fixture.db, fixture.deploymentID, "failed", "daemon_restarted")
			assertJob(t, fixture.db, fixture.jobID, "waiting_user", "insufficient_replacement_capacity", "", 2, fixture.inputJSON)
		})
	}
}

func marshalRecoveryInput(t *testing.T, input jobs.DeploymentInput) []byte {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func setRecoveryInput(t *testing.T, fixture *recoveryFixture, input []byte) {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE jobs SET input_json=? WHERE id=?`, string(input), fixture.jobID); err != nil {
		t.Fatal(err)
	}
}

func setCapacityPause(t *testing.T, fixture *recoveryFixture, disposition string) {
	t.Helper()
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='waiting_user',phase='insufficient_replacement_capacity',pause_disposition=? WHERE id=?`, disposition, fixture.jobID); err != nil {
		t.Fatal(err)
	}
}

func assertConfigurationHead(t *testing.T, fixture *recoveryFixture, wantID string, wantNumber int64) {
	t.Helper()
	var id string
	var number int64
	if err := fixture.db.QueryRow(`SELECT revision_id,revision_number FROM application_configuration_heads WHERE app_id=?`, fixture.appID).Scan(&id, &number); err != nil || id != wantID || number != wantNumber {
		t.Fatalf("configuration head=%q/%d err=%v, want %q/%d", id, number, err, wantID, wantNumber)
	}
}
