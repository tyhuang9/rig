package jobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

func TestCompleteGeneratedRouteTx(t *testing.T) {
	t.Run("later current attempt succeeds and replays", func(t *testing.T) {
		f := newJobRouteCompletionFixture(t)
		var first GeneratedRouteCompletionResult
		f.commit(t, func(tx *sql.Tx) error {
			if err := f.recordCompletionState(tx); err != nil {
				return err
			}
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			value, changed, err := CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			if err != nil {
				return err
			}
			if !changed {
				return errors.New("first job completion was not applied")
			}
			first = value
			return nil
		})
		if first.Job.Status != string(Succeeded) || first.Job.Attempt != f.input.CurrentWorkerAttempt ||
			first.Event.Code != "job_succeeded" || first.Event.Attempt != f.input.CurrentWorkerAttempt {
			t.Fatalf("completion result = %#v", first)
		}
		f.commit(t, func(tx *sql.Tx) error {
			if err := f.recordCompletionState(tx); err != nil {
				return err
			}
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			value, changed, err := CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			if err != nil {
				return err
			}
			if changed || value.Event.ID != first.Event.ID || value.Job.Attempt != first.Job.Attempt {
				return errors.New("exact job completion replay changed state")
			}
			return nil
		})
		if err := New(f.db).succeed(f.input.CurrentJobID, "deployment_completed"); !errors.Is(err, ErrJobTerminal) {
			t.Fatalf("generic terminal observation = %v", err)
		}
		assertJobCompletionCount(t, f.db, f.input.CurrentJobID, 1)
	})

	t.Run("event budget exhaustion refuses without CAS", func(t *testing.T) {
		f := newJobRouteCompletionFixture(t)
		f.commit(t, f.recordCompletionState)
		stamp := jobCompletionTimestamp(f.input.CompletedAt.Add(-time.Minute))
		for sequence := 1; sequence <= maxJobEventsPerAttempt; sequence++ {
			if _, err := f.db.Exec(`INSERT INTO job_events(
				job_id,sequence,attempt,timestamp,level,phase,code,message
			) VALUES(?,?,?,?,?,?,?,?)`, f.input.CurrentJobID, sequence, f.input.CurrentWorkerAttempt,
				stamp, "info", "running", "phase_started", "Job phase started"); err != nil {
				t.Fatal(err)
			}
		}
		err := f.inTx(false, func(tx *sql.Tx) error {
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			_, _, err = CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			return err
		})
		if !errors.Is(err, ErrEventBudget) {
			t.Fatalf("event-budget error = %v", err)
		}
		assertJobCompletionState(t, f.db, f.input.CurrentJobID, string(Running), "running")
		assertJobCompletionCount(t, f.db, f.input.CurrentJobID, 0)
	})
}

func TestCompleteGeneratedRouteTxRejectsInvalidCurrentState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate string
		arg    func(*jobRouteCompletionFixture) string
	}{
		{name: "stale", mutate: `UPDATE jobs SET attempt=attempt+1 WHERE id=?`,
			arg: func(f *jobRouteCompletionFixture) string { return f.input.CurrentJobID }},
		{name: "cancelling", mutate: `UPDATE jobs SET phase='cancelling' WHERE id=?`,
			arg: func(f *jobRouteCompletionFixture) string { return f.input.CurrentJobID }},
		{name: "roster_drift", mutate: `UPDATE generated_runtime_components
			SET state='failed',diagnostic_code='internal_error',finished_at=updated_at WHERE deployment_id=?`,
			arg: func(f *jobRouteCompletionFixture) string { return f.input.Activation.SuccessorDeploymentID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newJobRouteCompletionFixture(t)
			f.commit(t, f.recordCompletionState)
			if _, err := f.db.Exec(test.mutate, test.arg(f)); err != nil {
				t.Fatal(err)
			}
			err := f.inTx(false, func(tx *sql.Tx) error {
				receipt, err := f.readReceipt(tx)
				if err != nil {
					return err
				}
				_, _, err = CompleteGeneratedRouteTx(context.Background(), tx, receipt)
				return err
			})
			if err == nil {
				t.Fatal("stale/cancelling/current-drift completion was accepted")
			}
			assertJobCompletionNotSucceeded(t, f.db, f.input.CurrentJobID)
			assertJobCompletionCount(t, f.db, f.input.CurrentJobID, 0)
		})
	}
}

type jobRouteCompletionFixture struct {
	db    *sql.DB
	state *generatedruntimestate.Repository
	input generatedruntimestate.RouteCompletionInput
}

func newJobRouteCompletionFixture(t *testing.T) *jobRouteCompletionFixture {
	t.Helper()
	db, err := databasefixture.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.October, 25, 3, 4, 5, 0, time.UTC)
	stamp := jobCompletionTimestamp(now)
	actorID, appID, planID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	releaseConfigurationID, configurationID := uuid.NewString(), uuid.NewString()
	releaseID, jobID, deploymentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	artifactID, candidateReceiptID := uuid.NewString(), uuid.NewString()
	component, containerName, containerID := "api", "rig-job-completion-api", strings.Repeat("1", 64)
	creatorAttempt, currentAttempt := 7, 8
	execJobCompletionSQL(t, db, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, actorID, "hash", stamp, stamp)
	execJobCompletionSQL(t, db, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, "Job Completion App", stamp, stamp)
	execJobCompletionSQL(t, db, `INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,1,'',?,?,'accepted',?,?)`,
		planID, appID, "job-completion-plan:"+planID, strings.Repeat("a", 64), strings.Repeat("b", 64),
		strings.Repeat("c", 64), actorID, stamp, actorID, stamp)
	execJobCompletionSQL(t, db, `INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,1,?,?,?,0,0,2,?,1),(?,?,2,?,?,?,0,0,2,?,1)`,
		releaseConfigurationID, appID, "job-completion-config:"+releaseConfigurationID, actorID, stamp, planID,
		configurationID, appID, "job-completion-config:"+configurationID, actorID, stamp, planID)
	execJobCompletionSQL(t, db, `UPDATE application_configuration_heads SET revision_id=?,revision_number=2,updated_at=? WHERE app_id=?`, configurationID, stamp, appID)
	execJobCompletionSQL(t, db, `INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
		workspace_tree_sha256,configuration_revision_id,configuration_revision_number,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1,?,1)`, releaseID, appID, stamp,
		strings.Repeat("b", 64), strings.Repeat("e", 64), releaseConfigurationID, planID)
	execJobCompletionSQL(t, db, `INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,started_at,updated_at
	) VALUES(?,'deploy','application',?,'running','running',?,?,'{}',?,?,?)`,
		jobID, appID, creatorAttempt, actorID, stamp, stamp, stamp)
	execJobCompletionSQL(t, db, `INSERT INTO deployments(
		id,app_id,release_id,job_id,status,started_at,configuration_mode,actual_configuration_revision_id,
		actual_configuration_revision_number,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'applying',?,'current',?,2,1,'generated_node',?,1)`,
		deploymentID, appID, releaseID, jobID, stamp, configurationID, planID)
	execJobCompletionSQL(t, db, `INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,phase,migration_state,created_at,updated_at
	) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`, deploymentID, appID, releaseID, planID, stamp, stamp)
	execJobCompletionSQL(t, db, `INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'job-completion-test',?,4,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1,
		component, strings.Repeat("a", 64), "sha256:"+strings.Repeat("a", 64), stamp, stamp, stamp)
	execJobCompletionSQL(t, db, `INSERT INTO generated_runtime_components(
		deployment_id,component_name,slot,image_artifact_id,state,created_at,updated_at
	) VALUES(?,?,'blue',?,'image_ready',?,?)`, deploymentID, component, artifactID, stamp, stamp)
	execJobCompletionSQL(t, db, `UPDATE generated_runtime_deployments SET phase='building',updated_at=? WHERE deployment_id=?`, stamp, deploymentID)
	execJobCompletionSQL(t, db, `UPDATE generated_runtime_deployments SET phase='starting_candidate',updated_at=? WHERE deployment_id=?`, stamp, deploymentID)

	state := generatedruntimestate.New(db)
	intent := generatedruntimestate.CandidateIntentInput{
		ReceiptID: candidateReceiptID, AppID: appID, DeploymentID: deploymentID, ComponentName: component,
		ContainerName: containerName, CreatorJobID: jobID, CreatorWorkerAttempt: creatorAttempt,
		ConfigurationRevisionID: configurationID, ConfigurationRevisionNumber: 2,
		ImageArtifactID: artifactID, ImageArtifactAttempt: 4,
		EnvironmentOperationID: artifactID, EnvironmentStagingAttempt: creatorAttempt,
		CandidatePolicyVersion: "generated-candidate-v1", CandidatePolicyDigest: strings.Repeat("d", 64),
	}
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error { return state.BeginCandidateTx(context.Background(), tx, intent) })
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.RecordCandidateCreatedTx(context.Background(), tx, appID, deploymentID, component,
			candidateReceiptID, containerName, containerID)
	})
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.FinishCandidateStartingTx(context.Background(), tx, appID, deploymentID)
	})
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.RecordCandidateHealthyTx(context.Background(), tx, appID, deploymentID,
			component, candidateReceiptID, containerID)
	})
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.FinishCandidateHealthTx(context.Background(), tx, appID, deploymentID)
	})
	candidate, err := state.RouteActivationRoster(context.Background(), appID, deploymentID, generatedruntimestate.ComponentHealthy)
	if err != nil {
		t.Fatal(err)
	}
	activation := generatedruntimestate.RouteActivationReceipt{
		ReceiptID: uuid.NewString(), AppID: appID, SuccessorDeploymentID: deploymentID,
		SuccessorReleaseID: releaseID, SuccessorSlot: "blue", SuccessorGeneration: 1,
		CreatorJobID: jobID, CreatorWorkerAttempt: creatorAttempt,
		CandidateRosterDigest:   jobCompletionRosterDigest(candidate),
		PredecessorRosterDigest: jobCompletionRosterDigest(nil), RouteIntentDigest: strings.Repeat("4", 64),
		CommittedRouteProofDigest: strings.Repeat("5", 64), ActivatedAt: now,
	}
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO generated_route_activations(
			id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
			predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
			predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,candidate_roster_digest,
			predecessor_roster_digest,route_intent_digest,committed_route_proof_digest,activated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, activation.ReceiptID, appID, deploymentID, releaseID,
			"blue", 1, "", "", "", 0, "", jobID, creatorAttempt, activation.CandidateRosterDigest,
			activation.PredecessorRosterDigest, activation.RouteIntentDigest,
			activation.CommittedRouteProofDigest, stamp); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE generated_runtime_active_heads
			SET deployment_id=?,release_id=?,slot='blue',generation=1,activation_receipt_id=?,updated_at=?
			WHERE app_id=? AND generation=0`, deploymentID, releaseID, activation.ReceiptID, stamp, appID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE generated_runtime_components SET state='active',updated_at=?
			WHERE deployment_id=? AND state='healthy'`, stamp, deploymentID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE generated_runtime_deployments SET phase='draining',updated_at=?
			WHERE deployment_id=? AND phase='switching_route'`, stamp, deploymentID)
		return err
	})
	var retirement generatedruntimestate.PredecessorRetirementReceipt
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		value, _, err := state.RecordPredecessorRetirementReceiptTx(context.Background(), tx, activation, nil, now.Add(time.Second))
		retirement = value
		return err
	})
	var authorization generatedruntimestate.RouteAccountingHandoffAuthorization
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		value, _, err := state.RecordRouteAccountingHandoffAuthorizationTx(context.Background(), tx,
			activation, retirement, candidate, nil, now.Add(time.Minute))
		authorization = value
		return err
	})
	var handoff generatedruntimestate.RouteAccountingHandoffReceipt
	commitJobCompletionTx(t, db, func(tx *sql.Tx) error {
		value, _, err := state.RecordRouteAccountingHandoffReceiptTx(context.Background(), tx,
			authorization, now.Add(2*time.Minute))
		handoff = value
		return err
	})
	execJobCompletionSQL(t, db, `UPDATE jobs SET attempt=?,updated_at=? WHERE id=? AND attempt=?`,
		currentAttempt, stamp, jobID, creatorAttempt)
	return &jobRouteCompletionFixture{
		db: db, state: state,
		input: generatedruntimestate.RouteCompletionInput{
			Activation: activation, AccountingHandoff: handoff, CurrentJobID: jobID,
			CurrentWorkerAttempt: currentAttempt, CompletedAt: now.Add(3 * time.Minute),
		},
	}
}

func (f *jobRouteCompletionFixture) recordCompletionState(tx *sql.Tx) error {
	if _, _, err := f.state.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input); err != nil {
		return err
	}
	stamp := jobCompletionTimestamp(f.input.CompletedAt)
	result, err := tx.Exec(`UPDATE deployments SET
		status='succeeded',started_at=COALESCE(started_at,?),finished_at=?,diagnostic_code=NULL,
		failure_code=NULL,failure_summary=NULL
		WHERE id=? AND app_id=? AND job_id=? AND status='waiting_health'`,
		stamp, stamp, f.input.Activation.SuccessorDeploymentID, f.input.Activation.AppID, f.input.CurrentJobID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(`SELECT status FROM deployments
			WHERE id=? AND app_id=? AND job_id=?`, f.input.Activation.SuccessorDeploymentID,
			f.input.Activation.AppID, f.input.CurrentJobID).Scan(&status); err != nil {
			return err
		}
		if status != "succeeded" {
			return errors.New("test main deployment completion did not apply")
		}
	}
	_, _, err = f.state.RecordRouteCompletionReceiptTx(context.Background(), tx, f.input)
	return err
}

func (f *jobRouteCompletionFixture) readReceipt(tx *sql.Tx) (generatedruntimestate.RouteCompletionReceipt, error) {
	value, found, err := f.state.ReadRouteCompletionReceiptTx(context.Background(), tx, f.input.Activation.ReceiptID)
	if err != nil {
		return value, err
	}
	if !found {
		return value, errors.New("completion receipt is absent")
	}
	return value, nil
}

func (f *jobRouteCompletionFixture) inTx(commit bool, call func(*sql.Tx) error) error {
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err = call(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if commit {
		return tx.Commit()
	}
	return tx.Rollback()
}

func (f *jobRouteCompletionFixture) commit(t *testing.T, call func(*sql.Tx) error) {
	t.Helper()
	if err := f.inTx(true, call); err != nil {
		t.Fatal(err)
	}
}

func commitJobCompletionTx(t *testing.T, db *sql.DB, call func(*sql.Tx) error) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := call(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func execJobCompletionSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func jobCompletionRosterDigest(roster []generatedruntimestate.RouteActivationComponent) string {
	body, _ := json.Marshal(roster)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func jobCompletionTimestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func assertJobCompletionState(t *testing.T, db *sql.DB, jobID, status, phase string) {
	t.Helper()
	var actualStatus, actualPhase string
	if err := db.QueryRow(`SELECT status,phase FROM jobs WHERE id=?`, jobID).Scan(&actualStatus, &actualPhase); err != nil {
		t.Fatal(err)
	}
	if actualStatus != status || actualPhase != phase {
		t.Fatalf("job state = %s/%s want %s/%s", actualStatus, actualPhase, status, phase)
	}
}

func assertJobCompletionCount(t *testing.T, db *sql.DB, jobID string, want int) {
	t.Helper()
	var actual int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='job_succeeded'`, jobID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("job completion event count = %d want %d", actual, want)
	}
}

func assertJobCompletionNotSucceeded(t *testing.T, db *sql.DB, jobID string) {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM jobs WHERE id=?`, jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == string(Succeeded) {
		t.Fatal("job reached succeeded after refused completion")
	}
}
