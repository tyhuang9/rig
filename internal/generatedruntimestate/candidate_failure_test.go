package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRecordCandidateObservedFailureTxFailsCompleteAcceptedRoster(t *testing.T) {
	t.Run("starting with selected config distinct from release original", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api", "worker"})
		if f.releaseConfigurationID == f.configurationID {
			t.Fatal("fixture did not preserve distinct release-original and selected configurations")
		}
		api := f.input("api")
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.BeginCandidateTx(context.Background(), tx, api)
		})

		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateObservedFailureTx(
				context.Background(), tx, f.appID, f.deploymentID, DiagnosticStartFailed,
			)
		})
		assertCandidateObservedFailure(t, f, DiagnosticStartFailed, "apply_failed", map[string]int{
			"api": 1, "worker": 0,
		})
	})

	t.Run("waiting health with running and healthy members", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api", "worker"})
		candidateFailurePrepareRunning(t, f, "api", "worker")
		worker := f.input("worker")
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateHealthyTx(
				context.Background(), tx, f.appID, f.deploymentID, "worker", worker.ReceiptID, f.containerIDs["worker"],
			)
		})

		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateObservedFailureTx(
				context.Background(), tx, f.appID, f.deploymentID, DiagnosticHealthFailed,
			)
		})
		assertCandidateObservedFailure(t, f, DiagnosticHealthFailed, "health_failed", map[string]int{
			"api": 1, "worker": 1,
		})
	})
}

func TestRecordCandidateObservedFailureTxObeysCallerRollbackAndSingleUse(t *testing.T) {
	f := newCandidateFixture(t, []string{"api"})
	input := f.input("api")
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.BeginCandidateTx(context.Background(), tx, input)
	})

	tx := f.beginTx(t)
	if err := f.repository.RecordCandidateObservedFailureTx(
		context.Background(), tx, f.appID, f.deploymentID, DiagnosticStartFailed,
	); err != nil {
		t.Fatal(err)
	}
	assertCandidateStagesTx(t, tx, f.deploymentID, PhaseFailed, "failed")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCandidateStages(t, f, PhaseStartingCandidate, "applying")
	assertCandidateState(t, f, "api", 1, ComponentStarting, input.ContainerName, "")

	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateObservedFailureTx(
			context.Background(), tx, f.appID, f.deploymentID, DiagnosticStartFailed,
		)
	})
	if err := f.inTx(false, func(tx *sql.Tx) error {
		return f.repository.RecordCandidateObservedFailureTx(
			context.Background(), tx, f.appID, f.deploymentID, DiagnosticStartFailed,
		)
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("replayed failure error = %v", err)
	}
}

func TestRecordCandidateObservedFailureTxRejectsForeignStageAndPostRoute(t *testing.T) {
	t.Run("foreign application", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api"})
		input := f.input("api")
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.BeginCandidateTx(context.Background(), tx, input)
		})
		err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateObservedFailureTx(
				context.Background(), tx, uuid.NewString(), f.deploymentID, DiagnosticStartFailed,
			)
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign application error = %v", err)
		}
		assertCandidateStages(t, f, PhaseStartingCandidate, "applying")
	})

	t.Run("unbounded diagnostic", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api"})
		input := f.input("api")
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.BeginCandidateTx(context.Background(), tx, input)
		})
		err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateObservedFailureTx(
				context.Background(), tx, f.appID, f.deploymentID, DiagnosticRuntimeUnavailable,
			)
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("unbounded diagnostic error = %v", err)
		}
	})

	t.Run("diagnostic does not match stage", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api"})
		candidateFailurePrepareRunning(t, f, "api")
		err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateObservedFailureTx(
				context.Background(), tx, f.appID, f.deploymentID, DiagnosticStartFailed,
			)
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("stage mismatch error = %v", err)
		}
		assertCandidateStages(t, f, PhaseWaitingHealth, "waiting_health")
	})

	t.Run("route handoff phase", func(t *testing.T) {
		f := newCandidateFixture(t, []string{"api"})
		candidateFailurePrepareRunning(t, f, "api")
		input := f.input("api")
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateHealthyTx(
				context.Background(), tx, f.appID, f.deploymentID, "api", input.ReceiptID, f.containerIDs["api"],
			)
		})
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.FinishCandidateHealthTx(context.Background(), tx, f.appID, f.deploymentID)
		})
		err := f.inTx(false, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateObservedFailureTx(
				context.Background(), tx, f.appID, f.deploymentID, DiagnosticHealthFailed,
			)
		})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("post-route error = %v", err)
		}
		assertCandidateStages(t, f, PhaseSwitchingRoute, "waiting_health")
	})
}

func TestRecordCandidateObservedFailureTxRejectsIncompleteOrDriftedRoster(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *candidateFixture)
	}{
		{
			name: "accepted count no longer covers roster",
			mutate: func(t *testing.T, f *candidateFixture) {
				f.exec(t, `DROP TRIGGER deployment_plan_revision_immutable`)
				f.exec(t, `UPDATE deployment_plan_revisions SET component_count=1 WHERE id=?`, f.planID)
			},
		},
		{
			name: "selected artifact is unavailable",
			mutate: func(t *testing.T, f *candidateFixture) {
				f.exec(t, `UPDATE generated_image_artifacts
					SET state='unavailable',diagnostic_code='image_unavailable',updated_at=?
					WHERE id=?`, timestamp(f.now), f.artifactIDs["api"])
			},
		},
		{
			name: "receipt no longer matches selected configuration",
			mutate: func(t *testing.T, f *candidateFixture) {
				f.exec(t, `DROP TRIGGER deployment_identity_immutable`)
				f.exec(t, `UPDATE deployments
					SET actual_configuration_revision_id=?,actual_configuration_revision_number=1
					WHERE id=?`, f.releaseConfigurationID, f.deploymentID)
			},
		},
		{
			name: "starting member has no receipt",
			mutate: func(t *testing.T, f *candidateFixture) {
				f.exec(t, `UPDATE generated_runtime_components
					SET state='starting',container_name='rig-candidate-worker'
					WHERE deployment_id=? AND component_name='worker'`, f.deploymentID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newCandidateFixture(t, []string{"api", "worker"})
			api := f.input("api")
			f.commit(t, func(tx *sql.Tx) error {
				return f.repository.BeginCandidateTx(context.Background(), tx, api)
			})
			test.mutate(t, f)
			err := f.inTx(false, func(tx *sql.Tx) error {
				return f.repository.RecordCandidateObservedFailureTx(
					context.Background(), tx, f.appID, f.deploymentID, DiagnosticStartFailed,
				)
			})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("error = %v", err)
			}
			assertCandidateStages(t, f, PhaseStartingCandidate, "applying")
		})
	}
}

func candidateFailurePrepareRunning(t *testing.T, f *candidateFixture, components ...string) {
	t.Helper()
	for _, component := range components {
		input := f.input(component)
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.BeginCandidateTx(context.Background(), tx, input)
		})
		f.commit(t, func(tx *sql.Tx) error {
			return f.repository.RecordCandidateCreatedTx(
				context.Background(), tx, f.appID, f.deploymentID, component,
				input.ReceiptID, input.ContainerName, f.containerIDs[component],
			)
		})
	}
	f.commit(t, func(tx *sql.Tx) error {
		return f.repository.FinishCandidateStartingTx(context.Background(), tx, f.appID, f.deploymentID)
	})
}

func assertCandidateObservedFailure(
	t *testing.T,
	f *candidateFixture,
	diagnostic DiagnosticCode,
	mainDiagnostic string,
	receipts map[string]int,
) {
	t.Helper()
	var phase Phase
	var runtimeDiagnostic, runtimeFinished string
	if err := f.db.QueryRow(`SELECT phase,diagnostic_code,finished_at
		FROM generated_runtime_deployments WHERE deployment_id=?`, f.deploymentID).
		Scan(&phase, &runtimeDiagnostic, &runtimeFinished); err != nil {
		t.Fatal(err)
	}
	if phase != PhaseFailed || runtimeDiagnostic != string(diagnostic) || runtimeFinished == "" {
		t.Fatalf("runtime phase=%q diagnostic=%q finished=%q", phase, runtimeDiagnostic, runtimeFinished)
	}
	var status, gotMainDiagnostic, failureCode, failureSummary, mainFinished string
	if err := f.db.QueryRow(`SELECT status,diagnostic_code,failure_code,failure_summary,finished_at
		FROM deployments WHERE id=?`, f.deploymentID).Scan(
		&status, &gotMainDiagnostic, &failureCode, &failureSummary, &mainFinished,
	); err != nil {
		t.Fatal(err)
	}
	wantSummary := "Deployment did not become healthy"
	if diagnostic == DiagnosticStartFailed {
		wantSummary = "Container runtime failed to apply the deployment"
	}
	if status != "failed" || gotMainDiagnostic != mainDiagnostic || failureCode != mainDiagnostic ||
		failureSummary != wantSummary || mainFinished == "" {
		t.Fatalf("main status=%q diagnostic=%q failure=%q summary=%q finished=%q",
			status, gotMainDiagnostic, failureCode, failureSummary, mainFinished)
	}
	for component, wantReceipts := range receipts {
		var gotReceipts int
		var state ComponentState
		var gotDiagnostic, finished string
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents
			WHERE deployment_id=? AND component_name=?`, f.deploymentID, component).Scan(&gotReceipts); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT state,diagnostic_code,finished_at
			FROM generated_runtime_components WHERE deployment_id=? AND component_name=?`,
			f.deploymentID, component).Scan(&state, &gotDiagnostic, &finished); err != nil {
			t.Fatal(err)
		}
		if gotReceipts != wantReceipts || state != ComponentFailed || gotDiagnostic != string(diagnostic) || finished == "" {
			t.Fatalf("%s receipts=%d state=%q diagnostic=%q finished=%q",
				component, gotReceipts, state, gotDiagnostic, finished)
		}
	}
}
