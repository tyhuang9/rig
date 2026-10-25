package deployments

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
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

func TestRecordGeneratedRouteDeploymentSucceededTx(t *testing.T) {
	t.Run("success and committed replay", func(t *testing.T) {
		f := newDeploymentRouteCompletionFixture(t)
		f.commit(t, func(tx *sql.Tx) error {
			if _, _, err := f.state.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input); err != nil {
				return err
			}
			value, changed, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
			if err != nil {
				return err
			}
			if !changed || value.Status != Succeeded {
				return errors.New("main deployment did not reach succeeded")
			}
			receipt, _, err := f.state.RecordRouteCompletionReceiptTx(context.Background(), tx, f.input)
			if err != nil {
				return err
			}
			_, _, err = jobs.CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			return err
		})
		f.commit(t, func(tx *sql.Tx) error {
			value, changed, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
			if err != nil {
				return err
			}
			if changed || value.Status != Succeeded {
				return errors.New("main deployment replay changed state")
			}
			return nil
		})
	})

	t.Run("stale current attempt", func(t *testing.T) {
		f := newDeploymentRouteCompletionFixture(t)
		f.recordRuntime(t)
		f.input.CurrentWorkerAttempt++
		err := f.inTx(false, func(tx *sql.Tx) error {
			_, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
			return err
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("stale attempt error = %v", err)
		}
		assertDeploymentCompletionStatus(t, f.db, f.input.Activation.SuccessorDeploymentID, WaitingHealth)
	})

	t.Run("succeeded without completion receipt", func(t *testing.T) {
		f := newDeploymentRouteCompletionFixture(t)
		f.recordRuntime(t)
		if _, err := f.db.Exec(`UPDATE deployments SET status='succeeded',finished_at=? WHERE id=?`,
			completionTimestamp(f.input.CompletedAt), f.input.Activation.SuccessorDeploymentID); err != nil {
			t.Fatal(err)
		}
		execCompletionSQL(t, f.db, `UPDATE jobs SET status='succeeded',phase='succeeded',
			progress_percent=100,checkpoint_json='{"phase":"succeeded"}',finished_at=? WHERE id=?`,
			completionTimestamp(f.input.CompletedAt), f.input.CurrentJobID)
		err := f.inTx(false, func(tx *sql.Tx) error {
			_, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
			return err
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("receipt-less terminal error = %v", err)
		}
	})
}

func TestRecordGeneratedRouteDeploymentSucceededTxRejectsInvalidCurrentState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *deploymentRouteCompletionFixture)
	}{
		{name: "cancelling", mutate: func(t *testing.T, f *deploymentRouteCompletionFixture) {
			execCompletionSQL(t, f.db, `UPDATE jobs SET phase='cancelling' WHERE id=?`, f.input.CurrentJobID)
		}},
		{name: "terminal", mutate: func(t *testing.T, f *deploymentRouteCompletionFixture) {
			execCompletionSQL(t, f.db, `UPDATE jobs SET status='succeeded',phase='succeeded',
				progress_percent=100,checkpoint_json='{"phase":"succeeded"}',finished_at=? WHERE id=?`,
				completionTimestamp(f.input.CompletedAt), f.input.CurrentJobID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeploymentRouteCompletionFixture(t)
			f.recordRuntime(t)
			test.mutate(t, f)
			err := f.inTx(false, func(tx *sql.Tx) error {
				_, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
				return err
			})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("invalid current state error = %v", err)
			}
			assertDeploymentCompletionStatus(t, f.db, f.input.Activation.SuccessorDeploymentID, WaitingHealth)
		})
	}
}

func TestRecordGeneratedRouteDeploymentSucceededTxRejectsCrossedReceipts(t *testing.T) {
	f := newDeploymentRouteCompletionFixture(t)
	f.recordRuntime(t)
	f.input.Activation.CandidateRosterDigest = strings.Repeat("f", 64)
	f.input.AccountingHandoff.CandidateRosterDigest = f.input.Activation.CandidateRosterDigest
	err := f.inTx(false, func(tx *sql.Tx) error {
		_, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
		return err
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("crossed retained receipts error = %v", err)
	}
	assertDeploymentCompletionStatus(t, f.db, f.input.Activation.SuccessorDeploymentID, WaitingHealth)
}

func TestGeneratedRouteCompletionAtomicRollbackAndReadback(t *testing.T) {
	t.Run("cancellation winner rolls back every completion fact", func(t *testing.T) {
		f := newDeploymentRouteCompletionFixture(t)
		err := f.inTx(false, func(tx *sql.Tx) error {
			if err := f.recordCompletionState(tx); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE jobs SET phase='cancelling' WHERE id=? AND status='running'`,
				f.input.CurrentJobID); err != nil {
				return err
			}
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			_, _, err = jobs.CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			return err
		})
		if err == nil {
			t.Fatal("cancellation winner was accepted")
		}
		assertGeneratedRouteCompletionRollback(t, f)
	})

	t.Run("terminal event failure rolls back every completion fact", func(t *testing.T) {
		f := newDeploymentRouteCompletionFixture(t)
		if _, err := f.db.Exec(`CREATE TRIGGER reject_test_route_completion_event
			BEFORE INSERT ON job_events WHEN NEW.code='job_succeeded'
			BEGIN SELECT RAISE(ABORT,'test terminal event failure'); END`); err != nil {
			t.Fatal(err)
		}
		err := f.inTx(false, func(tx *sql.Tx) error {
			if err := f.recordCompletionState(tx); err != nil {
				return err
			}
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			_, _, err = jobs.CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			return err
		})
		if err == nil {
			t.Fatal("forced terminal event failure was accepted")
		}
		assertGeneratedRouteCompletionRollback(t, f)
	})

	t.Run("committed replay remains a single terminal event", func(t *testing.T) {
		f := newDeploymentRouteCompletionFixture(t)
		var eventID int64
		f.commit(t, func(tx *sql.Tx) error {
			if err := f.recordCompletionState(tx); err != nil {
				return err
			}
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			result, changed, err := jobs.CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			if err != nil {
				return err
			}
			if !changed {
				return errors.New("first atomic completion was not applied")
			}
			eventID = result.Event.ID
			return nil
		})
		f.commit(t, func(tx *sql.Tx) error {
			if err := f.recordCompletionState(tx); err != nil {
				return err
			}
			receipt, err := f.readReceipt(tx)
			if err != nil {
				return err
			}
			result, changed, err := jobs.CompleteGeneratedRouteTx(context.Background(), tx, receipt)
			if err != nil {
				return err
			}
			if changed || result.Event.ID != eventID {
				return errors.New("atomic completion replay changed the event")
			}
			return nil
		})
		assertGeneratedRouteCompletionEventCount(t, f.db, f.input.CurrentJobID, 1)
	})
}

type deploymentRouteCompletionFixture struct {
	db         *sql.DB
	repository *Repository
	state      *generatedruntimestate.Repository
	input      generatedruntimestate.RouteCompletionInput
}

func newDeploymentRouteCompletionFixture(t *testing.T) *deploymentRouteCompletionFixture {
	t.Helper()
	db, err := databasefixture.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.October, 25, 1, 2, 3, 0, time.UTC)
	stamp := completionTimestamp(now)
	actorID, appID, planID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	releaseConfigurationID, configurationID := uuid.NewString(), uuid.NewString()
	releaseID, jobID, deploymentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	artifactID, candidateReceiptID := uuid.NewString(), uuid.NewString()
	component, containerName, containerID := "api", "rig-completion-api", strings.Repeat("1", 64)
	jobAttempt := 7
	execCompletionSQL(t, db, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, actorID, "hash", stamp, stamp)
	execCompletionSQL(t, db, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, appID, appID, "Completion App", stamp, stamp)
	execCompletionSQL(t, db, `INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,1,'',?,?,'accepted',?,?)`,
		planID, appID, "completion-plan:"+planID, strings.Repeat("a", 64), strings.Repeat("b", 64),
		strings.Repeat("c", 64), actorID, stamp, actorID, stamp)
	execCompletionSQL(t, db, `INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,1,?,?,?,0,0,2,?,1),(?,?,2,?,?,?,0,0,2,?,1)`,
		releaseConfigurationID, appID, "completion-config:"+releaseConfigurationID, actorID, stamp, planID,
		configurationID, appID, "completion-config:"+configurationID, actorID, stamp, planID)
	execCompletionSQL(t, db, `UPDATE application_configuration_heads SET revision_id=?,revision_number=2,updated_at=? WHERE app_id=?`, configurationID, stamp, appID)
	execCompletionSQL(t, db, `INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
		workspace_tree_sha256,configuration_revision_id,configuration_revision_number,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1,?,1)`, releaseID, appID, stamp,
		strings.Repeat("b", 64), strings.Repeat("e", 64), releaseConfigurationID, planID)
	execCompletionSQL(t, db, `INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,started_at,updated_at
	) VALUES(?,'deploy','application',?,'running','running',?,?,'{}',?,?,?)`,
		jobID, appID, jobAttempt, actorID, stamp, stamp, stamp)
	execCompletionSQL(t, db, `INSERT INTO deployments(
		id,app_id,release_id,job_id,status,started_at,configuration_mode,actual_configuration_revision_id,
		actual_configuration_revision_number,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'applying',?,'current',?,2,1,'generated_node',?,1)`,
		deploymentID, appID, releaseID, jobID, stamp, configurationID, planID)
	execCompletionSQL(t, db, `INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,phase,migration_state,created_at,updated_at
	) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`, deploymentID, appID, releaseID, planID, stamp, stamp)
	execCompletionSQL(t, db, `INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'completion-test',?,4,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1,
		component, strings.Repeat("a", 64), "sha256:"+strings.Repeat("a", 64), stamp, stamp, stamp)
	execCompletionSQL(t, db, `INSERT INTO generated_runtime_components(
		deployment_id,component_name,slot,image_artifact_id,state,created_at,updated_at
	) VALUES(?,?,'blue',?,'image_ready',?,?)`, deploymentID, component, artifactID, stamp, stamp)
	execCompletionSQL(t, db, `UPDATE generated_runtime_deployments SET phase='building',updated_at=? WHERE deployment_id=?`, stamp, deploymentID)
	execCompletionSQL(t, db, `UPDATE generated_runtime_deployments SET phase='starting_candidate',updated_at=? WHERE deployment_id=?`, stamp, deploymentID)

	state := generatedruntimestate.New(db)
	intent := generatedruntimestate.CandidateIntentInput{
		ReceiptID: candidateReceiptID, AppID: appID, DeploymentID: deploymentID, ComponentName: component,
		ContainerName: containerName, CreatorJobID: jobID, CreatorWorkerAttempt: jobAttempt,
		ConfigurationRevisionID: configurationID, ConfigurationRevisionNumber: 2,
		ImageArtifactID: artifactID, ImageArtifactAttempt: 4,
		EnvironmentOperationID: artifactID, EnvironmentStagingAttempt: jobAttempt,
		CandidatePolicyVersion: "generated-candidate-v1", CandidatePolicyDigest: strings.Repeat("d", 64),
	}
	commitCompletionTx(t, db, func(tx *sql.Tx) error { return state.BeginCandidateTx(context.Background(), tx, intent) })
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.RecordCandidateCreatedTx(context.Background(), tx, appID, deploymentID, component,
			candidateReceiptID, containerName, containerID)
	})
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.FinishCandidateStartingTx(context.Background(), tx, appID, deploymentID)
	})
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.RecordCandidateHealthyTx(context.Background(), tx, appID, deploymentID,
			component, candidateReceiptID, containerID)
	})
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		return state.FinishCandidateHealthTx(context.Background(), tx, appID, deploymentID)
	})
	candidate, err := state.RouteActivationRoster(context.Background(), appID, deploymentID, generatedruntimestate.ComponentHealthy)
	if err != nil {
		t.Fatal(err)
	}
	activation := generatedruntimestate.RouteActivationReceipt{
		ReceiptID: uuid.NewString(), AppID: appID, SuccessorDeploymentID: deploymentID,
		SuccessorReleaseID: releaseID, SuccessorSlot: "blue", SuccessorGeneration: 1,
		CreatorJobID: jobID, CreatorWorkerAttempt: jobAttempt,
		CandidateRosterDigest:   completionRosterDigest(candidate),
		PredecessorRosterDigest: completionRosterDigest(nil), RouteIntentDigest: strings.Repeat("4", 64),
		CommittedRouteProofDigest: strings.Repeat("5", 64), ActivatedAt: now,
	}
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO generated_route_activations(
			id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
			predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
			predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,candidate_roster_digest,
			predecessor_roster_digest,route_intent_digest,committed_route_proof_digest,activated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, activation.ReceiptID, appID, deploymentID, releaseID,
			"blue", 1, "", "", "", 0, "", jobID, jobAttempt, activation.CandidateRosterDigest,
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
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		value, _, err := state.RecordPredecessorRetirementReceiptTx(context.Background(), tx, activation, nil, now.Add(time.Second))
		retirement = value
		return err
	})
	var authorization generatedruntimestate.RouteAccountingHandoffAuthorization
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		value, _, err := state.RecordRouteAccountingHandoffAuthorizationTx(context.Background(), tx,
			activation, retirement, candidate, nil, now.Add(time.Minute))
		authorization = value
		return err
	})
	var handoff generatedruntimestate.RouteAccountingHandoffReceipt
	commitCompletionTx(t, db, func(tx *sql.Tx) error {
		value, _, err := state.RecordRouteAccountingHandoffReceiptTx(context.Background(), tx,
			authorization, now.Add(2*time.Minute))
		handoff = value
		return err
	})
	input := generatedruntimestate.RouteCompletionInput{
		Activation: activation, AccountingHandoff: handoff, CurrentJobID: jobID,
		CurrentWorkerAttempt: jobAttempt, CompletedAt: now.Add(3 * time.Minute),
	}
	return &deploymentRouteCompletionFixture{db: db, repository: New(db), state: state, input: input}
}

func (f *deploymentRouteCompletionFixture) recordRuntime(t *testing.T) {
	t.Helper()
	f.commit(t, func(tx *sql.Tx) error {
		_, _, err := f.state.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input)
		return err
	})
}

func (f *deploymentRouteCompletionFixture) recordCompletionState(tx *sql.Tx) error {
	if _, _, err := f.state.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input); err != nil {
		return err
	}
	if _, _, err := f.repository.RecordGeneratedRouteSucceededTx(context.Background(), tx, f.input); err != nil {
		return err
	}
	_, _, err := f.state.RecordRouteCompletionReceiptTx(context.Background(), tx, f.input)
	return err
}

func (f *deploymentRouteCompletionFixture) readReceipt(tx *sql.Tx) (generatedruntimestate.RouteCompletionReceipt, error) {
	value, found, err := f.state.ReadRouteCompletionReceiptTx(context.Background(), tx, f.input.Activation.ReceiptID)
	if err != nil {
		return value, err
	}
	if !found {
		return value, errors.New("completion receipt is absent")
	}
	return value, nil
}

func (f *deploymentRouteCompletionFixture) inTx(commit bool, call func(*sql.Tx) error) error {
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

func (f *deploymentRouteCompletionFixture) commit(t *testing.T, call func(*sql.Tx) error) {
	t.Helper()
	if err := f.inTx(true, call); err != nil {
		t.Fatal(err)
	}
}

func commitCompletionTx(t *testing.T, db *sql.DB, call func(*sql.Tx) error) {
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

func execCompletionSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func completionRosterDigest(roster []generatedruntimestate.RouteActivationComponent) string {
	body, _ := json.Marshal(roster)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func completionTimestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func assertDeploymentCompletionStatus(t *testing.T, db *sql.DB, deploymentID string, want Status) {
	t.Helper()
	var actual Status
	if err := db.QueryRow(`SELECT status FROM deployments WHERE id=?`, deploymentID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("deployment status = %s want %s", actual, want)
	}
}

func assertGeneratedRouteCompletionRollback(t *testing.T, f *deploymentRouteCompletionFixture) {
	t.Helper()
	var runtimePhase string
	var deploymentStatus Status
	var receipts int
	if err := f.db.QueryRow(`SELECT phase FROM generated_runtime_deployments WHERE deployment_id=?`,
		f.input.Activation.SuccessorDeploymentID).Scan(&runtimePhase); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT status FROM deployments WHERE id=?`,
		f.input.Activation.SuccessorDeploymentID).Scan(&deploymentStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM generated_route_completion_receipts
		WHERE activation_receipt_id=?`, f.input.Activation.ReceiptID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if runtimePhase != "draining" || deploymentStatus != WaitingHealth || receipts != 0 {
		t.Fatalf("rollback state runtime=%s deployment=%s receipts=%d", runtimePhase, deploymentStatus, receipts)
	}
	var status, phase string
	if err := f.db.QueryRow(`SELECT status,phase FROM jobs WHERE id=?`, f.input.CurrentJobID).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "running" || phase != "running" {
		t.Fatalf("rollback job state = %s/%s", status, phase)
	}
	assertGeneratedRouteCompletionEventCount(t, f.db, f.input.CurrentJobID, 0)
}

func assertGeneratedRouteCompletionEventCount(t *testing.T, db *sql.DB, jobID string, want int) {
	t.Helper()
	var actual int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='job_succeeded'`, jobID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("job completion event count = %d want %d", actual, want)
	}
}
