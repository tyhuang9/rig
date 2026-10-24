package generatedruntimestate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
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
			if test.want == PhaseDraining || test.want == PhaseSucceeded {
				state := newHistoricalStartupServingFixture(t, test.want)
				got, err := generatedDomainSnapshotForTest(t, state.repository, state.db)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Deployments) != 1 || got.Deployments[0].Phase != test.want {
					t.Fatalf("historical phase snapshot=%#v", got.Deployments)
				}
				return
			}
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
		// This head predates activation receipts. Upgrade its real historical
		// schema, then retain visibility without manufacturing current authority.
		serving := newHistoricalStartupServingFixture(t, PhaseDraining)
		if _, err := serving.db.Exec(`UPDATE applications SET archived_at=? WHERE id=?`, timestamp(serving.now), serving.appID); err != nil {
			t.Fatal(err)
		}
		got, err := generatedDomainSnapshotForTest(t, serving.repository, serving.db)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.ActiveHeads) != 1 || got.ActiveHeads[0].AppID != serving.appID ||
			got.ActiveHeads[0].DeploymentID != serving.deploymentID || got.ActiveHeads[0].Generation != 1 ||
			got.ActiveHeads[0].ActivationReceiptID != "" {
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
		t.Run("current guard refuses generation zero non-null "+test.name, func(t *testing.T) {
			fixture := newFixture(t, []string{"api"}, false, false)
			beforeHead, err := fixture.repository.Active(context.Background(), fixture.appID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`DROP TRIGGER generated_runtime_active_head_valid_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(test.update, fixture.appID); err == nil ||
				!strings.Contains(err.Error(), "generated runtime active head requires an exact activation receipt") {
				t.Fatalf("obsolete active-head write error = %v", err)
			}
			afterHead, err := fixture.repository.Active(context.Background(), fixture.appID)
			if err != nil {
				t.Fatal(err)
			}
			if afterHead != beforeHead {
				t.Fatalf("refused obsolete active-head write changed state: before=%#v after=%#v", beforeHead, afterHead)
			}
			// Preserve the reader's original malformed-state boundary separately
			// from the current writer guard. This deliberate corruption setup drops
			// only the 042 activation guard after proving that it refused the same
			// write; production writers never receive this escape.
			if _, err := fixture.db.Exec(`DROP TRIGGER generated_runtime_active_head_activation_guard`); err != nil {
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

// newHistoricalStartupServingFixture applies the checked-in schema through 041,
// writes a serving head permitted by that schema, and upgrades normally. It is
// deliberately a read-compatibility fixture, not a current activation writer.
func newHistoricalStartupServingFixture(t *testing.T, phase Phase) fixture {
	t.Helper()
	if phase != PhaseDraining && phase != PhaseSucceeded {
		t.Fatalf("unsupported historical serving phase %q", phase)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrationDir := filepath.Join("..", "database", "migrations")
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "042_" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrationDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("historical migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, entry.Name()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		applied++
	}
	if applied != 41 {
		t.Fatalf("historical migration discovery=%d want=41", applied)
	}
	f := fixture{db: db, appID: uuid.NewString(), actorID: uuid.NewString(),
		deploymentID: uuid.NewString(), releaseID: uuid.NewString(), planID: uuid.NewString(),
		now: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)}
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`, f.actorID, f.actorID, "hash", timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, f.appID, f.appID, f.appID, timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,?,8,?,?,?,'accepted',?,?)`,
		f.planID, f.appID, "apps/"+f.appID+"/deployment-plans/"+f.planID+".secret", strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), 1, "", f.actorID, timestamp(f.now), f.actorID, timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, f.releaseID, f.appID, timestamp(f.now), strings.Repeat("b", 64), strings.Repeat("f", 64), f.planID); err != nil {
		t.Fatal(err)
	}
	jobID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES(?,'deploy','application',?,'running','running',?,?,?)`, jobID, f.appID, f.actorID, timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, f.deploymentID, f.appID, f.releaseID, jobID, f.planID); err != nil {
		t.Fatal(err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,phase,migration_state,created_at,updated_at
	) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`,
		f.deploymentID, f.appID, f.releaseID, f.planID, timestamp(f.now), timestamp(f.now))
	artifactID := seedReadyArtifact(t, f, "api")
	mustExec(`INSERT INTO generated_runtime_components(
		deployment_id,component_name,slot,image_artifact_id,container_name,container_id,state,created_at,updated_at
	) VALUES(?,'api','blue',?,?,?,'healthy',?,?)`,
		f.deploymentID, artifactID, "rig-historical-api", strings.Repeat("1", 64), timestamp(f.now), timestamp(f.now))
	for _, next := range []Phase{PhaseBuilding, PhaseStartingCandidate, PhaseWaitingHealth, PhaseSwitchingRoute} {
		mustExec(`UPDATE generated_runtime_deployments SET phase=?,updated_at=? WHERE deployment_id=?`,
			next, timestamp(f.now), f.deploymentID)
	}
	mustExec(`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=? WHERE app_id=?`,
		f.deploymentID, f.releaseID, timestamp(f.now), f.appID)
	mustExec(`UPDATE generated_runtime_components SET state='active',updated_at=? WHERE deployment_id=?`,
		timestamp(f.now), f.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='draining',updated_at=? WHERE deployment_id=?`,
		timestamp(f.now), f.deploymentID)
	if phase == PhaseSucceeded {
		mustExec(`UPDATE generated_runtime_deployments SET phase='succeeded',updated_at=?,finished_at=? WHERE deployment_id=?`,
			timestamp(f.now), timestamp(f.now), f.deploymentID)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("upgrade historical serving head: %v", err)
	}
	var activationCount int
	var receiptID sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&activationCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT activation_receipt_id FROM generated_runtime_active_heads WHERE app_id=?`, f.appID).Scan(&receiptID); err != nil {
		t.Fatal(err)
	}
	if activationCount != 0 || receiptID.Valid {
		t.Fatalf("historical upgrade minted activation authority: count=%d receipt=%#v", activationCount, receiptID)
	}
	f.repository = New(db)
	f.repository.now = func() time.Time { return f.now }
	return f
}
