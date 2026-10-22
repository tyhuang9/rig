package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestGeneratedDomainStartupSnapshotTxTreatsHistoricalApplicationsAsEmpty(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, false, false)
	snapshot, err := generatedDomainSnapshotForTest(t, fixture.repository, fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActiveHeads == nil || snapshot.Deployments == nil || snapshot.Components == nil ||
		snapshot.CandidateIntents == nil {
		t.Fatal("empty snapshot contains nil slices")
	}
	if len(snapshot.ActiveHeads) != 0 || len(snapshot.Deployments) != 0 ||
		len(snapshot.Components) != 0 || len(snapshot.CandidateIntents) != 0 {
		t.Fatalf("ordinary history reported generated runtime state: %#v", snapshot)
	}
}

func TestGeneratedDomainStartupSnapshotTxReturnsSortedServingPendingMigrationAndCandidateFacts(t *testing.T) {
	fixture := newCandidateFixture(t, []string{"worker", "api"})
	input := fixture.input("api")
	fixture.commit(t, func(tx *sql.Tx) error {
		return fixture.repository.BeginCandidateTx(context.Background(), tx, input)
	})
	snapshot, err := generatedDomainSnapshotForTest(t, fixture.repository, fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Deployments) != 1 || snapshot.Deployments[0].DeploymentID != fixture.deploymentID ||
		snapshot.Deployments[0].Phase != PhaseStartingCandidate {
		t.Fatalf("deployment census=%#v", snapshot.Deployments)
	}
	if len(snapshot.Components) != 2 || snapshot.Components[0].ComponentName != "api" ||
		snapshot.Components[1].ComponentName != "worker" {
		t.Fatalf("component census is not canonical: %#v", snapshot.Components)
	}
	if len(snapshot.CandidateIntents) != 1 || snapshot.CandidateIntents[0].ReceiptID != input.ReceiptID ||
		snapshot.CandidateIntents[0].CandidatePolicyDigest != input.CandidatePolicyDigest {
		t.Fatalf("candidate census=%#v", snapshot.CandidateIntents)
	}

	phaseCases := []struct {
		name      string
		want      Phase
		migration bool
		path      []Phase
	}{
		{name: "preflight", want: PhasePreflight},
		{name: "building", want: PhaseBuilding, path: []Phase{PhaseBuilding}},
		{name: "migrating", want: PhaseMigrating, migration: true, path: []Phase{PhaseBuilding, PhaseMigrating}},
		{name: "starting candidate", want: PhaseStartingCandidate, path: []Phase{PhaseBuilding, PhaseStartingCandidate}},
		{name: "waiting health", want: PhaseWaitingHealth, path: []Phase{PhaseBuilding, PhaseStartingCandidate, PhaseWaitingHealth}},
		{name: "switching route", want: PhaseSwitchingRoute, path: []Phase{PhaseBuilding, PhaseStartingCandidate, PhaseWaitingHealth, PhaseSwitchingRoute}},
		{name: "draining", want: PhaseDraining, path: []Phase{PhaseBuilding, PhaseStartingCandidate, PhaseWaitingHealth, PhaseSwitchingRoute, PhaseDraining}},
		{name: "succeeded", want: PhaseSucceeded, path: []Phase{PhaseBuilding, PhaseStartingCandidate, PhaseWaitingHealth, PhaseSwitchingRoute, PhaseDraining, PhaseSucceeded}},
		{name: "failed", want: PhaseFailed, path: []Phase{PhaseFailed}},
		{name: "cancelled", want: PhaseCancelled, path: []Phase{PhaseCancelled}},
	}
	for _, test := range phaseCases {
		t.Run("phase "+test.name, func(t *testing.T) {
			state := newFixture(t, []string{"api"}, test.migration, test.migration)
			deployment, created, err := state.repository.Begin(context.Background(), state.beginInput([]string{"api"}))
			if err != nil || !created {
				t.Fatalf("begin: created=%t error=%v", created, err)
			}
			for _, next := range test.path {
				diagnostic := DiagnosticCode("")
				if next == PhaseFailed {
					diagnostic = DiagnosticInternalError
				} else if next == PhaseCancelled {
					diagnostic = DiagnosticCancelled
				}
				deployment, err = state.repository.Advance(context.Background(), state.appID, state.deploymentID,
					deployment.Phase, next, diagnostic)
				if err != nil {
					t.Fatalf("advance to %q: %v", next, err)
				}
			}
			got, err := generatedDomainSnapshotForTest(t, state.repository, state.db)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Deployments) != 1 || got.Deployments[0].Phase != test.want {
				t.Fatalf("phase snapshot=%#v", got.Deployments)
			}
		})
	}

	migrationCases := []struct {
		name string
		want MigrationState
	}{
		{name: "not required", want: MigrationNotRequired},
		{name: "pending", want: MigrationPending},
		{name: "running", want: MigrationRunning},
		{name: "succeeded", want: MigrationSucceeded},
		{name: "failed", want: MigrationFailed},
	}
	for _, test := range migrationCases {
		t.Run("migration "+test.name, func(t *testing.T) {
			migration := test.want != MigrationNotRequired
			state := newFixture(t, []string{"api"}, migration, migration)
			deployment, created, err := state.repository.Begin(context.Background(), state.beginInput([]string{"api"}))
			if err != nil || !created {
				t.Fatalf("begin: created=%t error=%v", created, err)
			}
			if test.want == MigrationRunning || test.want == MigrationSucceeded || test.want == MigrationFailed {
				deployment = advance(t, state.repository, deployment, PhaseBuilding)
				deployment = advance(t, state.repository, deployment, PhaseMigrating)
				deployment, err = state.repository.BeginMigration(context.Background(), state.appID, state.deploymentID)
				if err != nil {
					t.Fatalf("begin migration: %v", err)
				}
			}
			if test.want == MigrationSucceeded || test.want == MigrationFailed {
				deployment, err = state.repository.FinishMigration(context.Background(), state.appID, state.deploymentID,
					test.want == MigrationSucceeded)
				if err != nil {
					t.Fatalf("finish migration: %v", err)
				}
			}
			got, err := generatedDomainSnapshotForTest(t, state.repository, state.db)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Deployments) != 1 || got.Deployments[0].MigrationState != test.want {
				t.Fatalf("migration snapshot=%#v", got.Deployments)
			}
		})
	}

	t.Run("archived application retains positive serving head", func(t *testing.T) {
		serving := newCandidateFixture(t, []string{"api"})
		intent := serving.input("api")
		serving.commit(t, func(tx *sql.Tx) error {
			return serving.repository.BeginCandidateTx(context.Background(), tx, intent)
		})
		serving.commit(t, func(tx *sql.Tx) error {
			return serving.repository.RecordCandidateCreatedTx(context.Background(), tx, serving.appID,
				serving.deploymentID, "api", intent.ReceiptID, intent.ContainerName, serving.containerIDs["api"])
		})
		serving.commit(t, func(tx *sql.Tx) error {
			return serving.repository.FinishCandidateStartingTx(context.Background(), tx, serving.appID, serving.deploymentID)
		})
		serving.commit(t, func(tx *sql.Tx) error {
			return serving.repository.RecordCandidateHealthyTx(context.Background(), tx, serving.appID,
				serving.deploymentID, "api", intent.ReceiptID, serving.containerIDs["api"])
		})
		serving.commit(t, func(tx *sql.Tx) error {
			return serving.repository.FinishCandidateHealthTx(context.Background(), tx, serving.appID, serving.deploymentID)
		})
		head, changed, err := serving.repository.SwitchActive(context.Background(), serving.appID, serving.deploymentID, 0)
		if err != nil || !changed {
			t.Fatalf("switch active: changed=%t error=%v", changed, err)
		}
		if _, err := serving.db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, timestamp(serving.now), serving.appID); err != nil {
			t.Fatal(err)
		}
		got, err := generatedDomainSnapshotForTest(t, serving.repository, serving.db)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.ActiveHeads) != 1 || got.ActiveHeads[0].AppID != serving.appID ||
			got.ActiveHeads[0].DeploymentID != head.DeploymentID || got.ActiveHeads[0].Generation != head.Generation {
			t.Fatalf("archived positive head=%#v", got.ActiveHeads)
		}
	})
}

func TestGeneratedDomainStartupSnapshotTxReadsCandidateIntentsIndependently(t *testing.T) {
	fixture := newCandidateFixture(t, []string{"api"})
	input := fixture.input("api")
	fixture.commit(t, func(tx *sql.Tx) error {
		return fixture.repository.BeginCandidateTx(context.Background(), tx, input)
	})
	snapshot, err := generatedDomainSnapshotForTest(t, fixture.repository, fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.CandidateIntents) != 1 || snapshot.CandidateIntents[0] != (GeneratedDomainStartupCandidateIntent{
		ReceiptID: input.ReceiptID, DeploymentID: input.DeploymentID, ComponentName: input.ComponentName,
		CreatorJobID: input.CreatorJobID, CreatorWorkerAttempt: input.CreatorWorkerAttempt,
		ConfigurationRevisionID:     input.ConfigurationRevisionID,
		ConfigurationRevisionNumber: input.ConfigurationRevisionNumber,
		ImageArtifactID:             input.ImageArtifactID, ImageArtifactAttempt: input.ImageArtifactAttempt,
		EnvironmentOperationID:    input.EnvironmentOperationID,
		EnvironmentStagingAttempt: input.EnvironmentStagingAttempt,
		CandidatePolicyVersion:    input.CandidatePolicyVersion, CandidatePolicyDigest: input.CandidatePolicyDigest,
	}) {
		t.Fatalf("candidate identity changed: %#v", snapshot.CandidateIntents)
	}

	t.Run("base receipt with missing component parent is refused", func(t *testing.T) {
		state := newCandidateFixture(t, []string{"api"})
		intent := state.input("api")
		state.commit(t, func(tx *sql.Tx) error {
			return state.repository.BeginCandidateTx(context.Background(), tx, intent)
		})
		state.exec(t, `DROP TRIGGER generated_candidate_intent_immutable`)
		state.exec(t, `PRAGMA foreign_keys=OFF`)
		state.exec(t, `UPDATE generated_candidate_intents SET component_name='missing-parent' WHERE id=?`, intent.ReceiptID)
		state.exec(t, `PRAGMA foreign_keys=ON`)
		var receiptCount, changesBefore int64
		if err := state.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, intent.ReceiptID).Scan(&receiptCount); err != nil {
			t.Fatal(err)
		}
		if err := state.db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
			t.Fatal(err)
		}
		tx, err := state.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		failed, err := state.repository.GeneratedDomainStartupSnapshotTx(context.Background(), tx)
		_ = tx.Rollback()
		if !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(failed, GeneratedDomainStartupSnapshot{}) {
			t.Fatalf("missing candidate parent snapshot=%#v error=%v", failed, err)
		}
		var receiptCountAfter, changesAfter int64
		if err := state.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE id=?`, intent.ReceiptID).Scan(&receiptCountAfter); err != nil {
			t.Fatal(err)
		}
		if err := state.db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil {
			t.Fatal(err)
		}
		if receiptCount != 1 || receiptCountAfter != receiptCount || changesAfter != changesBefore {
			t.Fatalf("candidate census changed: receipts=%d->%d changes=%d->%d",
				receiptCount, receiptCountAfter, changesBefore, changesAfter)
		}
	})
}

func TestGeneratedDomainStartupSnapshotTxRejectsMalformedOrCrossedStateWithoutPartialResult(t *testing.T) {
	t.Run("deployment timestamp", func(t *testing.T) {
		fixture := newFixture(t, []string{"api"}, false, false)
		if _, _, err := fixture.repository.Begin(context.Background(), fixture.beginInput([]string{"api"})); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE generated_runtime_deployments SET updated_at='not-a-time' WHERE deployment_id=?`, fixture.deploymentID); err != nil {
			t.Fatal(err)
		}
		tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := fixture.repository.GeneratedDomainStartupSnapshotTx(context.Background(), tx)
		_ = tx.Rollback()
		if !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
			t.Fatalf("malformed snapshot=%#v error=%v", snapshot, err)
		}
	})

	zeroHeadCases := []struct {
		name   string
		update string
	}{
		{name: "deployment", update: `UPDATE generated_runtime_active_heads SET deployment_id='' WHERE app_id=?`},
		{name: "release", update: `UPDATE generated_runtime_active_heads SET release_id='' WHERE app_id=?`},
		{name: "slot", update: `UPDATE generated_runtime_active_heads SET slot='' WHERE app_id=?`},
		{name: "updated at", update: `UPDATE generated_runtime_active_heads SET updated_at='' WHERE app_id=?`},
	}
	for _, test := range zeroHeadCases {
		t.Run("generation zero non-null "+test.name, func(t *testing.T) {
			fixture := newFixture(t, []string{"api"}, false, false)
			if _, err := fixture.db.Exec(`DROP TRIGGER generated_runtime_active_head_valid_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(test.update, fixture.appID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
			var changesBefore int64
			if err := fixture.db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
				t.Fatal(err)
			}
			tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := fixture.repository.GeneratedDomainStartupSnapshotTx(context.Background(), tx)
			_ = tx.Rollback()
			if !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
				t.Fatalf("malformed zero head snapshot=%#v error=%v", snapshot, err)
			}
			var changesAfter int64
			if err := fixture.db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil {
				t.Fatal(err)
			}
			if changesAfter != changesBefore {
				t.Fatalf("zero-head validation wrote state: %d -> %d", changesBefore, changesAfter)
			}
		})
	}

	migrationTimeCases := []struct {
		name   string
		state  MigrationState
		update string
	}{
		{name: "pending with start", state: MigrationPending,
			update: `UPDATE generated_runtime_deployments SET migration_started_at=? WHERE deployment_id=?`},
		{name: "running without start", state: MigrationRunning,
			update: `UPDATE generated_runtime_deployments SET migration_started_at=NULL WHERE deployment_id=?`},
		{name: "succeeded without finish", state: MigrationSucceeded,
			update: `UPDATE generated_runtime_deployments SET migration_finished_at=NULL WHERE deployment_id=?`},
	}
	for _, test := range migrationTimeCases {
		t.Run("migration time "+test.name, func(t *testing.T) {
			fixture := newFixture(t, []string{"api"}, true, true)
			deployment, created, err := fixture.repository.Begin(context.Background(), fixture.beginInput([]string{"api"}))
			if err != nil || !created {
				t.Fatalf("begin: created=%t error=%v", created, err)
			}
			if test.state == MigrationRunning || test.state == MigrationSucceeded {
				deployment = advance(t, fixture.repository, deployment, PhaseBuilding)
				deployment = advance(t, fixture.repository, deployment, PhaseMigrating)
				deployment, err = fixture.repository.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if test.state == MigrationSucceeded {
				if _, err = fixture.repository.FinishMigration(context.Background(), fixture.appID, fixture.deploymentID, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if test.state == MigrationPending {
				_, err = fixture.db.Exec(test.update, timestamp(fixture.now), fixture.deploymentID)
			} else {
				_, err = fixture.db.Exec(test.update, fixture.deploymentID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
			tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := fixture.repository.GeneratedDomainStartupSnapshotTx(context.Background(), tx)
			_ = tx.Rollback()
			if !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
				t.Fatalf("invalid migration time snapshot=%#v error=%v", snapshot, err)
			}
		})
	}
}

func TestGeneratedDomainStartupSnapshotTxRejectsInvalidCanceledAndDoneTransactions(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, false, false)
	if snapshot, err := fixture.repository.GeneratedDomainStartupSnapshotTx(context.Background(), nil); !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
		t.Fatalf("nil transaction snapshot=%#v error=%v", snapshot, err)
	}

	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if snapshot, err := fixture.repository.GeneratedDomainStartupSnapshotTx(canceled, tx); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
		_ = tx.Rollback()
		t.Fatalf("canceled snapshot=%#v error=%v", snapshot, err)
	}
	_ = tx.Rollback()

	done, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := done.Commit(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := fixture.repository.GeneratedDomainStartupSnapshotTx(context.Background(), done); !errors.Is(err, sql.ErrTxDone) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
		t.Fatalf("done transaction snapshot=%#v error=%v", snapshot, err)
	}
}

func generatedDomainSnapshotForTest(t *testing.T, repository *Repository, db *sql.DB) (GeneratedDomainStartupSnapshot, error) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	defer tx.Rollback()
	snapshot, err := repository.GeneratedDomainStartupSnapshotTx(context.Background(), tx)
	if err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return GeneratedDomainStartupSnapshot{}, err
	}
	return snapshot, nil
}
