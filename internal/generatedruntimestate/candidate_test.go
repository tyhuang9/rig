package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hostd/hostd/internal/database"
)

func TestCandidateTransactionLifecycleIsAtomicAndSingleUse(t *testing.T) {
	f := newCandidateFixture(t, []string{"api"})
	input := f.input("api")

	tx := f.beginTx(t)
	if err := f.repository.BeginCandidateTx(context.Background(), tx, input); err != nil {
		t.Fatal(err)
	}
	assertCandidateComponentTx(t, tx, f.deploymentID, "api", ComponentStarting, input.ContainerName, "")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCandidateState(t, f, "api", 0, ComponentImageReady, "", "")

	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.BeginCandidateTx(context.Background(), tx, input)
	})
	assertCandidateState(t, f, "api", 1, ComponentStarting, input.ContainerName, "")
	if err := f.inTx(false, func(tx *sql.Tx) error {
		return f.repository.BeginCandidateTx(context.Background(), tx, input)
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("replayed begin error = %v", err)
	}

	// The retained receipt describes its original creator. Later observation
	// writers are authorized against the current claim by their owner and must
	// not require that the original worker attempt is still current.
	f.exec(t, `UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`, timestamp(f.now), f.jobID)
	tx = f.beginTx(t)
	if err := f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, input.ContainerName, f.containerIDs["api"]); err != nil {
		t.Fatal(err)
	}
	assertCandidateComponentTx(t, tx, f.deploymentID, "api", ComponentRunning, input.ContainerName, f.containerIDs["api"])
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCandidateState(t, f, "api", 1, ComponentStarting, input.ContainerName, "")
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, input.ContainerName, f.containerIDs["api"])
	})

	tx = f.beginTx(t)
	if err := f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID); err != nil {
		t.Fatal(err)
	}
	assertCandidateStagesTx(t, tx, f.deploymentID, PhaseWaitingHealth, "waiting_health")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCandidateStages(t, f, PhaseStartingCandidate, "applying")
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
	})

	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateHealthyTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, f.containerIDs["api"])
	})
	tx = f.beginTx(t)
	if err := f.repository.FinishCandidateHealthTx(context.Background(), tx, f.appID, f.deploymentID); err != nil {
		t.Fatal(err)
	}
	assertCandidateStagesTx(t, tx, f.deploymentID, PhaseSwitchingRoute, "waiting_health")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCandidateStages(t, f, PhaseWaitingHealth, "waiting_health")
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateHealthTx(context.Background(), tx, f.appID, f.deploymentID)
	})
	assertCandidateStages(t, f, PhaseSwitchingRoute, "waiting_health")
	assertCandidateState(t, f, "api", 1, ComponentHealthy, input.ContainerName, f.containerIDs["api"])

	for name, call := range map[string]func(*sql.Tx) error{
		"created": func(tx *sql.Tx) error {
			return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, input.ContainerName, f.containerIDs["api"])
		},
		"healthy": func(tx *sql.Tx) error {
			return f.repository.RecordCandidateHealthyTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, f.containerIDs["api"])
		},
		"finish starting": func(tx *sql.Tx) error {
			return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
		},
		"finish health": func(tx *sql.Tx) error {
			return f.repository.FinishCandidateHealthTx(context.Background(), tx, f.appID, f.deploymentID)
		},
	} {
		t.Run("replay "+name, func(t *testing.T) {
			if err := f.inTx(false, call); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBeginCandidateTxRejectsForeignAndDriftedProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *candidateFixture, *CandidateIntentInput)
	}{
		{name: "foreign app", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.AppID = uuid.NewString()
		}},
		{name: "foreign deployment", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.DeploymentID = uuid.NewString()
		}},
		{name: "wrong creator attempt", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.CreatorWorkerAttempt++
			input.EnvironmentStagingAttempt++
		}},
		{name: "current creator attempt changed", mutate: func(t *testing.T, f *candidateFixture, _ *CandidateIntentInput) {
			f.exec(t, `UPDATE jobs SET attempt=attempt+1 WHERE id=?`, f.jobID)
		}},
		{name: "release configuration is not selected", mutate: func(_ *testing.T, f *candidateFixture, input *CandidateIntentInput) {
			input.ConfigurationRevisionID = f.releaseConfigurationID
			input.ConfigurationRevisionNumber = 1
		}},
		{name: "wrong configuration revision", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.ConfigurationRevisionNumber++
		}},
		{name: "wrong artifact", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.ImageArtifactID = uuid.NewString()
			input.EnvironmentOperationID = input.ImageArtifactID
		}},
		{name: "wrong artifact attempt", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.ImageArtifactAttempt++
		}},
		{name: "wrong environment operation", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.EnvironmentOperationID = uuid.NewString()
		}},
		{name: "wrong environment attempt", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.EnvironmentStagingAttempt++
		}},
		{name: "invalid policy", mutate: func(_ *testing.T, _ *candidateFixture, input *CandidateIntentInput) {
			input.CandidatePolicyDigest = strings.Repeat("D", 64)
		}},
		{name: "runtime phase drift", mutate: func(t *testing.T, f *candidateFixture, _ *CandidateIntentInput) {
			f.exec(t, `UPDATE generated_runtime_deployments SET phase='waiting_health' WHERE deployment_id=?`, f.deploymentID)
		}},
		{name: "main stage drift", mutate: func(t *testing.T, f *candidateFixture, _ *CandidateIntentInput) {
			f.exec(t, `UPDATE deployments SET status='waiting_health' WHERE id=?`, f.deploymentID)
		}},
		{name: "component state drift", mutate: func(t *testing.T, f *candidateFixture, _ *CandidateIntentInput) {
			f.exec(t, `UPDATE generated_runtime_components SET state='starting',container_name='other-name' WHERE deployment_id=? AND component_name='api'`, f.deploymentID)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newCandidateFixture(t, []string{"api"})
			input := f.input("api")
			test.mutate(t, f, &input)
			if err := f.inTx(false, func(tx *sql.Tx) error {
				return f.repository.BeginCandidateTx(context.Background(), tx, input)
			}); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("error = %v", err)
			}
			var receipts int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if receipts != 0 {
				t.Fatalf("receipt count = %d", receipts)
			}
		})
	}
}

func TestBeginCandidateTxRejectsDuplicateReceiptAndComponent(t *testing.T) {
	f := newCandidateFixture(t, []string{"api", "worker"})
	api := f.input("api")
	f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(context.Background(), tx, api) })

	t.Run("same component", func(t *testing.T) {
		second := api
		second.ReceiptID = uuid.NewString()
		if err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.BeginCandidateTx(context.Background(), tx, second)
		}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("same receipt", func(t *testing.T) {
		second := f.input("worker")
		second.ReceiptID = api.ReceiptID
		if err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.BeginCandidateTx(context.Background(), tx, second)
		}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("error = %v", err)
		}
		assertCandidateState(t, f, "worker", 0, ComponentImageReady, "", "")
	})
}

func TestCandidateObservationWritersRequireExactReceiptAndPhysicalIdentity(t *testing.T) {
	f := newCandidateFixture(t, []string{"api"})
	input := f.input("api")
	f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(context.Background(), tx, input) })

	createdCases := []struct {
		name                                string
		appID, receiptID, containerName, id string
	}{
		{name: "foreign app", appID: uuid.NewString(), receiptID: input.ReceiptID, containerName: input.ContainerName, id: f.containerIDs["api"]},
		{name: "foreign receipt", appID: f.appID, receiptID: uuid.NewString(), containerName: input.ContainerName, id: f.containerIDs["api"]},
		{name: "wrong name", appID: f.appID, receiptID: input.ReceiptID, containerName: "other-name", id: f.containerIDs["api"]},
		{name: "invalid id", appID: f.appID, receiptID: input.ReceiptID, containerName: input.ContainerName, id: "not-an-id"},
	}
	for _, test := range createdCases {
		t.Run("created "+test.name, func(t *testing.T) {
			if err := f.inTx(false, func(tx *sql.Tx) error {
				return f.repository.RecordCandidateCreatedTx(context.Background(), tx, test.appID, f.deploymentID, "api", test.receiptID, test.containerName, test.id)
			}); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("error = %v", err)
			}
			assertCandidateState(t, f, "api", 1, ComponentStarting, input.ContainerName, "")
		})
	}
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, input.ContainerName, f.containerIDs["api"])
	})
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
	})

	healthyCases := []struct {
		name, appID, receiptID, id string
	}{
		{name: "foreign app", appID: uuid.NewString(), receiptID: input.ReceiptID, id: f.containerIDs["api"]},
		{name: "foreign receipt", appID: f.appID, receiptID: uuid.NewString(), id: f.containerIDs["api"]},
		{name: "wrong id", appID: f.appID, receiptID: input.ReceiptID, id: strings.Repeat("f", 64)},
	}
	for _, test := range healthyCases {
		t.Run("healthy "+test.name, func(t *testing.T) {
			if err := f.inTx(false, func(tx *sql.Tx) error {
				return f.repository.RecordCandidateHealthyTx(context.Background(), tx, test.appID, f.deploymentID, "api", test.receiptID, test.id)
			}); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("error = %v", err)
			}
			assertCandidateState(t, f, "api", 1, ComponentRunning, input.ContainerName, f.containerIDs["api"])
		})
	}
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateHealthyTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, f.containerIDs["api"])
	})
}

func TestCandidateFinishRequiresCompleteReceiptBackedRoster(t *testing.T) {
	f := newCandidateFixture(t, []string{"api", "worker"})
	api := f.input("api")
	worker := f.input("worker")
	f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(context.Background(), tx, api) })
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", api.ReceiptID, api.ContainerName, f.containerIDs["api"])
	})
	if err := f.inTx(false, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial starting roster error = %v", err)
	}
	assertCandidateStages(t, f, PhaseStartingCandidate, "applying")

	f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(context.Background(), tx, worker) })
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "worker", worker.ReceiptID, worker.ContainerName, f.containerIDs["worker"])
	})
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
	})

	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateHealthyTx(context.Background(), tx, f.appID, f.deploymentID, "api", api.ReceiptID, f.containerIDs["api"])
	})
	if err := f.inTx(false, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateHealthTx(context.Background(), tx, f.appID, f.deploymentID)
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("partial healthy roster error = %v", err)
	}
	assertCandidateStages(t, f, PhaseWaitingHealth, "waiting_health")
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateHealthyTx(context.Background(), tx, f.appID, f.deploymentID, "worker", worker.ReceiptID, f.containerIDs["worker"])
	})
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateHealthTx(context.Background(), tx, f.appID, f.deploymentID)
	})
	assertCandidateStages(t, f, PhaseSwitchingRoute, "waiting_health")
}

func TestCandidateFinishRejectsArtifactAndStageDrift(t *testing.T) {
	t.Run("artifact no longer ready", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api"})
		input := f.input("api")
		f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(context.Background(), tx, input) })
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, input.ContainerName, f.containerIDs["api"])
		})
		f.exec(t, `UPDATE generated_image_artifacts SET state='unavailable',diagnostic_code='image_unavailable',updated_at=? WHERE id=?`, timestamp(f.now.Add(time.Minute)), input.ImageArtifactID)
		if err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
		}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("error = %v", err)
		}
		assertCandidateStages(t, f, PhaseStartingCandidate, "applying")
	})

	t.Run("wrong main stage", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api"})
		input := f.input("api")
		f.commit(t, func(tx *sql.Tx) error { return f.repository.BeginCandidateTx(context.Background(), tx, input) })
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateCreatedTx(context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, input.ContainerName, f.containerIDs["api"])
		})
		f.exec(t, `UPDATE deployments SET status='waiting_health' WHERE id=?`, f.deploymentID)
		if err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
		}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("error = %v", err)
		}
	})
}

type candidateFixture struct {
	db                     *sql.DB
	repository             *Repository
	now                    time.Time
	actorID                string
	appID                  string
	planID                 string
	releaseConfigurationID string
	configurationID        string
	releaseID              string
	jobID                  string
	deploymentID           string
	jobAttempt             int
	artifactIDs            map[string]string
	artifactAttempts       map[string]int64
	receiptIDs             map[string]string
	containerNames         map[string]string
	containerIDs           map[string]string
}

func newCandidateFixture(t *testing.T, components []string) *candidateFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &candidateFixture{
		db: db, repository: New(db), now: time.Date(2026, time.October, 20, 1, 2, 3, 0, time.UTC),
		actorID: uuid.NewString(), appID: uuid.NewString(), planID: uuid.NewString(),
		releaseConfigurationID: uuid.NewString(), configurationID: uuid.NewString(), releaseID: uuid.NewString(),
		jobID: uuid.NewString(), deploymentID: uuid.NewString(), jobAttempt: 7,
		artifactIDs: map[string]string{}, artifactAttempts: map[string]int64{}, receiptIDs: map[string]string{},
		containerNames: map[string]string{}, containerIDs: map[string]string{},
	}
	f.repository.now = func() time.Time { return f.now }
	stamp := timestamp(f.now)
	f.exec(t, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, f.actorID, f.actorID, "hash", stamp, stamp)
	f.exec(t, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, f.appID, f.appID, "Candidate App", stamp, stamp)
	f.exec(t, `INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,?,1,'',?,?,'accepted',?,?)`,
		f.planID, f.appID, "candidate-plan:"+f.planID, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), len(components), f.actorID, stamp, f.actorID, stamp)
	f.exec(t, `INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES
		(?,?,1,?,?,?,0,0,2,?,1),
		(?,?,2,?,?,?,0,0,2,?,1)`,
		f.releaseConfigurationID, f.appID, "candidate-config:"+f.releaseConfigurationID, f.actorID, stamp, f.planID,
		f.configurationID, f.appID, "candidate-config:"+f.configurationID, f.actorID, stamp, f.planID)
	f.exec(t, `UPDATE application_configuration_heads SET revision_id=?,revision_number=2,updated_at=? WHERE app_id=?`, f.configurationID, stamp, f.appID)
	f.exec(t, `INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
		workspace_tree_sha256,configuration_revision_id,configuration_revision_number,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1,?,1)`,
		f.releaseID, f.appID, stamp, strings.Repeat("b", 64), strings.Repeat("e", 64), f.releaseConfigurationID, f.planID)
	f.exec(t, `INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,started_at,updated_at
	) VALUES(?,'deploy','application',?,'running','running',?,?,'{}',?,?,?)`,
		f.jobID, f.appID, f.jobAttempt, f.actorID, stamp, stamp, stamp)
	f.exec(t, `INSERT INTO deployments(
		id,app_id,release_id,job_id,status,started_at,configuration_mode,actual_configuration_revision_id,
		actual_configuration_revision_number,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'applying',?,'current',?,2,1,'generated_node',?,1)`,
		f.deploymentID, f.appID, f.releaseID, f.jobID, stamp, f.configurationID, f.planID)
	f.exec(t, `INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,phase,migration_state,created_at,updated_at
	) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`, f.deploymentID, f.appID, f.releaseID, f.planID, stamp, stamp)
	for index, component := range components {
		artifactID := uuid.NewString()
		f.artifactIDs[component] = artifactID
		f.artifactAttempts[component] = int64(index + 4)
		f.receiptIDs[component] = uuid.NewString()
		f.containerNames[component] = "rig-candidate-" + component
		f.containerIDs[component] = strings.Repeat(string(rune('1'+index)), 64)
		f.exec(t, `INSERT INTO generated_image_artifacts(
			id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
			compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
		) VALUES(?,?,?,?,?,'candidate-test',?,?,?,'ready',?,?,?)`, artifactID, f.releaseID, f.planID, 1, component,
			strings.Repeat(string(rune('a'+index)), 64), f.artifactAttempts[component], "sha256:"+strings.Repeat(string(rune('a'+index)), 64), stamp, stamp, stamp)
		f.exec(t, `INSERT INTO generated_runtime_components(
			deployment_id,component_name,slot,image_artifact_id,state,created_at,updated_at
		) VALUES(?,?,'blue',?,'image_ready',?,?)`, f.deploymentID, component, artifactID, stamp, stamp)
	}
	f.exec(t, `UPDATE generated_runtime_deployments SET phase='building',updated_at=? WHERE deployment_id=?`, stamp, f.deploymentID)
	f.exec(t, `UPDATE generated_runtime_deployments SET phase='starting_candidate',updated_at=? WHERE deployment_id=?`, stamp, f.deploymentID)
	return f
}

func (f *candidateFixture) input(component string) CandidateIntentInput {
	return CandidateIntentInput{
		ReceiptID: f.receiptIDs[component], AppID: f.appID, DeploymentID: f.deploymentID,
		ComponentName: component, ContainerName: f.containerNames[component], CreatorJobID: f.jobID,
		CreatorWorkerAttempt: f.jobAttempt, ConfigurationRevisionID: f.configurationID, ConfigurationRevisionNumber: 2,
		ImageArtifactID: f.artifactIDs[component], ImageArtifactAttempt: f.artifactAttempts[component],
		EnvironmentOperationID: f.artifactIDs[component], EnvironmentStagingAttempt: f.jobAttempt,
		CandidatePolicyVersion: "generated-candidate-v1", CandidatePolicyDigest: strings.Repeat("d", 64),
	}
}

func (f *candidateFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *candidateFixture) beginTx(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func (f *candidateFixture) inTx(commit bool, call func(*sql.Tx) error) error {
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

func (f *candidateFixture) commit(t *testing.T, call func(*sql.Tx) error) {
	t.Helper()
	if err := f.inTx(true, call); err != nil {
		t.Fatal(err)
	}
}

func assertCandidateState(t *testing.T, f *candidateFixture, component string, receipts int, state ComponentState, name, id string) {
	t.Helper()
	var gotReceipts int
	var gotState ComponentState
	var gotName, gotID sql.NullString
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=? AND component_name=?`, f.deploymentID, component).Scan(&gotReceipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT state,container_name,container_id FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, f.deploymentID, component).Scan(&gotState, &gotName, &gotID); err != nil {
		t.Fatal(err)
	}
	if gotReceipts != receipts || gotState != state || gotName.String != name || gotName.Valid != (name != "") || gotID.String != id || gotID.Valid != (id != "") {
		t.Fatalf("component receipts=%d state=%q name=%#v id=%#v", gotReceipts, gotState, gotName, gotID)
	}
}

func assertCandidateComponentTx(t *testing.T, tx *sql.Tx, deploymentID, component string, state ComponentState, name, id string) {
	t.Helper()
	var gotState ComponentState
	var gotName, gotID sql.NullString
	if err := tx.QueryRow(`SELECT state,container_name,container_id FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`, deploymentID, component).Scan(&gotState, &gotName, &gotID); err != nil {
		t.Fatal(err)
	}
	if gotState != state || gotName.String != name || gotName.Valid != (name != "") || gotID.String != id || gotID.Valid != (id != "") {
		t.Fatalf("component state=%q name=%#v id=%#v", gotState, gotName, gotID)
	}
}

func assertCandidateStages(t *testing.T, f *candidateFixture, phase Phase, status string) {
	t.Helper()
	assertCandidateStagesQuery(t, f.db, f.deploymentID, phase, status)
}

func assertCandidateStagesTx(t *testing.T, tx *sql.Tx, deploymentID string, phase Phase, status string) {
	t.Helper()
	assertCandidateStagesQuery(t, tx, deploymentID, phase, status)
}

type candidateQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func assertCandidateStagesQuery(t *testing.T, queryer candidateQueryer, deploymentID string, phase Phase, status string) {
	t.Helper()
	var gotPhase Phase
	var gotStatus string
	if err := queryer.QueryRow(`SELECT rd.phase,d.status FROM generated_runtime_deployments rd JOIN deployments d ON d.id=rd.deployment_id WHERE rd.deployment_id=?`, deploymentID).Scan(&gotPhase, &gotStatus); err != nil {
		t.Fatal(err)
	}
	if gotPhase != phase || gotStatus != status {
		t.Fatalf("phase=%q status=%q", gotPhase, gotStatus)
	}
}
