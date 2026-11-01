package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const generatedRouteActivationWritersMigration = "046_generated_route_activation_writers.sql"

func TestGeneratedRouteActivationWritersMigrationFreshAndMirror(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedRouteActivationWritersMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", generatedRouteActivationWritersMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("generated route activation writer migration is not byte mirrored")
	}

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertGeneratedRouteActivationWriterSchema(t, db)
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedRouteActivationWritersMigrationUpgradePreservesHistoricalChain(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "046_")); err != nil {
		t.Fatal(err)
	}
	fixture := seedGeneratedRouteActivationFixture(t, db, "legacy")
	activation := fixture.activation()
	completeGeneratedRouteActivationChain(t, db, fixture, activation, 1, false)

	var before [4]int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM generated_route_activations),
		(SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts),
		(SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts),
		(SELECT COUNT(*) FROM generated_route_completion_receipts)`).Scan(
		&before[0], &before[1], &before[2], &before[3]); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/" + generatedRouteActivationWritersMigration)
	if err != nil {
		t.Fatal(err)
	}
	only := fstest.MapFS{"migrations/" + generatedRouteActivationWritersMigration: {Data: body}}
	for pass := 1; pass <= 2; pass++ {
		if err := migrateFS(db, only); err != nil {
			t.Fatalf("apply 046 pass %d: %v", pass, err)
		}
	}
	assertGeneratedRouteActivationWriterSchema(t, db)
	var writerJobID sql.NullString
	var writerAttempt sql.NullInt64
	if err := db.QueryRow(`SELECT writer_job_id,writer_worker_attempt FROM generated_route_activations WHERE id=?`,
		activation.id).Scan(&writerJobID, &writerAttempt); err != nil {
		t.Fatal(err)
	}
	if writerJobID.Valid || writerAttempt.Valid {
		t.Fatalf("legacy activation writer was backfilled: job=%#v attempt=%#v", writerJobID, writerAttempt)
	}
	var after [4]int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM generated_route_activations),
		(SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts),
		(SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts),
		(SELECT COUNT(*) FROM generated_route_completion_receipts)`).Scan(
		&after[0], &after[1], &after[2], &after[3]); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("046 rewrote historical 042-045 chain: before=%v after=%v", before, after)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedRouteActivationWritersMigrationActivationAndCompletionGuards(t *testing.T) {
	t.Run("legacy null writer requires original attempt", func(t *testing.T) {
		t.Run("original attempt", func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			fixture := seedGeneratedRouteActivationFixture(t, db, "primary")
			if err := insertGeneratedRouteActivation(db, fixture.activation()); err != nil {
				t.Fatalf("legacy same-attempt activation: %v", err)
			}
		})
		t.Run("later attempt", func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			fixture := seedGeneratedRouteActivationFixture(t, db, "primary")
			if _, err := db.Exec(`UPDATE jobs SET attempt=2 WHERE id=?`, fixture.jobID); err != nil {
				t.Fatal(err)
			}
			assertGeneratedRouteActivationFixturePreconditions(t, db, fixture, 2)
			before := generatedRouteActivationGuardStateFor(t, db, fixture)
			if err := insertGeneratedRouteActivation(db, fixture.activation()); err == nil {
				t.Fatal("legacy null writer was accepted after a later worker attempt")
			}
			assertGeneratedRouteActivationGuardState(t, db, fixture, before)
		})
	})

	t.Run("explicit writer permits current attempt and seals completion", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedRouteActivationFixture(t, db, "other")
		activation := fixture.activation()
		completeGeneratedRouteActivationChain(t, db, fixture, activation, 2, true)
		var writerJobID string
		var writerAttempt int
		if err := db.QueryRow(`SELECT writer_job_id,writer_worker_attempt FROM generated_route_activations WHERE id=?`,
			activation.id).Scan(&writerJobID, &writerAttempt); err != nil {
			t.Fatal(err)
		}
		if writerJobID != fixture.jobID || writerAttempt != 2 {
			t.Fatalf("writer=%q/%d want=%q/2", writerJobID, writerAttempt, fixture.jobID)
		}
		var currentAttempt int
		if err := db.QueryRow(`SELECT current_worker_attempt FROM generated_route_completion_receipts WHERE activation_receipt_id=?`,
			activation.id).Scan(&currentAttempt); err != nil {
			t.Fatal(err)
		}
		if currentAttempt != writerAttempt {
			t.Fatalf("completion current attempt=%d writer=%d", currentAttempt, writerAttempt)
		}
	})

	t.Run("mixed and crossed writer pairs are refused", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedRouteActivationFixture(t, db, "legacy")
		activation := fixture.activation()
		for _, writer := range []struct {
			name    string
			jobID   any
			attempt any
		}{
			{name: "writer job only", jobID: fixture.jobID, attempt: nil},
			{name: "writer attempt only", jobID: nil, attempt: 1},
			{name: "future writer attempt", jobID: fixture.jobID, attempt: 2},
		} {
			t.Run(writer.name, func(t *testing.T) {
				row := activation
				row.id = "40000000-0000-4000-8000-000000000046"
				if err := insertGeneratedRouteActivationWriter(db, row, writer.jobID, writer.attempt); err == nil {
					t.Fatalf("%s was accepted", writer.name)
				}
			})
		}
	})

	t.Run("writer authority and ordering are refused", func(t *testing.T) {
		for _, test := range []struct {
			name           string
			creatorAttempt int
			setup          func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, activation generatedRouteActivationRow) (string, int)
		}{
			{
				name: "wrong writer job",
				setup: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, _ generatedRouteActivationRow) (string, int) {
					alternate := seedGeneratedRouteActivationFixture(t, db, "other")
					assertGeneratedRouteActivationFixturePreconditions(t, db, fixture, fixture.creatorAttempt)
					assertGeneratedRouteActivationFixturePreconditions(t, db, alternate, alternate.creatorAttempt)
					return alternate.jobID, alternate.creatorAttempt
				},
			},
			{
				name: "missing persisted binding",
				setup: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, _ generatedRouteActivationRow) (string, int) {
					if _, err := db.Exec(`DROP TRIGGER application_deployment_lifecycle_binding_retain`); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`DELETE FROM application_deployment_lifecycle_bindings WHERE job_id=?`, fixture.jobID); err != nil {
						t.Fatal(err)
					}
					return fixture.jobID, fixture.creatorAttempt
				},
			},
			{
				name: "mismatched persisted binding",
				setup: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, _ generatedRouteActivationRow) (string, int) {
					if _, err := db.Exec(`DROP TRIGGER application_deployment_lifecycle_binding_immutable`); err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`UPDATE application_deployment_lifecycle_bindings
						SET app_id=? WHERE job_id=?`, fixture.appID+"-mismatched", fixture.jobID); err != nil {
						t.Fatal(err)
					}
					return fixture.jobID, fixture.creatorAttempt
				},
			},
			{
				name: "non-running writer",
				setup: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, _ generatedRouteActivationRow) (string, int) {
					if _, err := db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, fixture.stamp, fixture.jobID); err != nil {
						t.Fatal(err)
					}
					return fixture.jobID, fixture.creatorAttempt
				},
			},
			{
				name: "cancelling writer",
				setup: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, _ generatedRouteActivationRow) (string, int) {
					if _, err := db.Exec(`UPDATE jobs SET phase='cancelling' WHERE id=?`, fixture.jobID); err != nil {
						t.Fatal(err)
					}
					return fixture.jobID, fixture.creatorAttempt
				},
			},
			{
				name:           "writer precedes immutable creator",
				creatorAttempt: 2,
				setup: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture, _ generatedRouteActivationRow) (string, int) {
					if _, err := db.Exec(`UPDATE jobs SET attempt=1 WHERE id=?`, fixture.jobID); err != nil {
						t.Fatal(err)
					}
					return fixture.jobID, 1
				},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				db := openMemoryDatabase(t)
				if err := Migrate(db); err != nil {
					t.Fatal(err)
				}
				creatorAttempt := test.creatorAttempt
				if creatorAttempt == 0 {
					creatorAttempt = 1
				}
				fixture := seedGeneratedRouteActivationFixtureAtAttempt(t, db, "primary", creatorAttempt)
				activation := fixture.activation()
				writerID, writerAttempt := test.setup(t, db, fixture, activation)
				before := generatedRouteActivationGuardStateFor(t, db, fixture)
				if err := insertGeneratedRouteActivationWriter(db, activation, writerID, writerAttempt); err == nil {
					t.Fatalf("%s was accepted", test.name)
				}
				assertGeneratedRouteActivationGuardState(t, db, fixture, before)
			})
		}
	})

	t.Run("completion current attempt cannot precede effective writer", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedRouteActivationFixture(t, db, "other")
		activation := fixture.activation()
		prepareGeneratedRouteActivationCompletion(t, db, fixture, activation, 2, true)
		if _, err := db.Exec(`UPDATE jobs SET attempt=1 WHERE id=?`, fixture.jobID); err != nil {
			t.Fatal(err)
		}
		before := generatedRouteActivationGuardStateFor(t, db, fixture)
		if err := insertGeneratedRouteCompletion(db, fixture, activation, 1); err == nil {
			t.Fatal("completion before effective writer was accepted")
		}
		assertGeneratedRouteActivationGuardState(t, db, fixture, before)
	})
}

func TestGeneratedRouteActivationWritersMigrationFailureRestoresSchema(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "046_")); err != nil {
		t.Fatal(err)
	}
	var oldActivationTrigger, oldCompletionTrigger string
	for _, value := range []struct {
		name string
		into *string
	}{
		{"generated_route_activation_valid_insert", &oldActivationTrigger},
		{"generated_route_completion_receipt_valid_insert", &oldCompletionTrigger},
	} {
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, value.name).Scan(value.into); err != nil {
			t.Fatal(err)
		}
	}
	body, err := migrations.ReadFile("migrations/" + generatedRouteActivationWritersMigration)
	if err != nil {
		t.Fatal(err)
	}
	failing := append(append([]byte(nil), body...), []byte("\nSELECT missing_column FROM generated_route_writer_forced_failure;\n")...)
	err = migrateFS(db, fstest.MapFS{"migrations/" + generatedRouteActivationWritersMigration: {Data: failing}})
	if err == nil {
		t.Fatal("failing 046 migration unexpectedly succeeded")
	}
	for _, value := range []struct {
		name string
		want string
	}{
		{"generated_route_activation_valid_insert", oldActivationTrigger},
		{"generated_route_completion_receipt_valid_insert", oldCompletionTrigger},
	} {
		var actual string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, value.name).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != value.want {
			t.Fatalf("failed 046 migration did not restore %s", value.name)
		}
	}
	var writerColumns, applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('generated_route_activations')
		WHERE name IN ('writer_job_id','writer_worker_attempt')`).Scan(&writerColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
		generatedRouteActivationWritersMigration).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if writerColumns != 0 || applied != 0 {
		t.Fatalf("failed 046 migration leaked schema state: columns=%d ledger=%d", writerColumns, applied)
	}
}

func assertGeneratedRouteActivationWriterSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	columns := map[string]bool{}
	wantTypes := map[string]string{"writer_job_id": "TEXT", "writer_worker_attempt": "INTEGER"}
	rows, err := db.Query(`PRAGMA table_info(generated_route_activations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if wantType, ok := wantTypes[name]; ok {
			columns[name] = dataType == wantType
			if dataType != wantType {
				t.Fatalf("writer column %s type=%s want=%s", name, dataType, wantType)
			}
			if notNull != 0 {
				t.Fatalf("writer column %s is unexpectedly NOT NULL", name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !columns["writer_job_id"] || !columns["writer_worker_attempt"] {
		t.Fatalf("writer columns=%v", columns)
	}
	for _, trigger := range []string{
		"generated_route_activation_valid_insert",
		"generated_route_completion_receipt_valid_insert",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("trigger %s count=%d error=%v", trigger, count, err)
		}
	}
}

func insertGeneratedRouteActivationWriter(db *sql.DB, row generatedRouteActivationRow,
	writerJobID, writerWorkerAttempt any,
) error {
	_, err := db.Exec(`INSERT INTO generated_route_activations(
		id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
		predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
		predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,writer_job_id,
		writer_worker_attempt,candidate_roster_digest,predecessor_roster_digest,route_intent_digest,
		committed_route_proof_digest,activated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.id, row.appID, row.successorDeploymentID, row.successorReleaseID, row.successorSlot,
		row.successorGeneration, row.predecessorDeploymentID, row.predecessorReleaseID, row.predecessorSlot,
		row.predecessorGeneration, row.predecessorActivationReceiptID, row.creatorJobID,
		row.creatorWorkerAttempt, writerJobID, writerWorkerAttempt, row.candidateRosterDigest,
		row.predecessorRosterDigest, row.routeIntentDigest, row.committedRouteProofDigest, row.activatedAt)
	return err
}

type generatedRouteActivationGuardState struct {
	head               string
	phase              string
	activations        int
	retirements        int
	authorizations     int
	handoffs           int
	completionReceipts int
}

func assertGeneratedRouteActivationFixturePreconditions(t *testing.T, db *sql.DB,
	fixture generatedRouteActivationFixture, currentAttempt int,
) {
	t.Helper()
	var activations, bindings, activeJobs int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM generated_route_activations
			WHERE app_id=? AND successor_deployment_id=?),
		(SELECT COUNT(*) FROM application_deployment_lifecycle_bindings
			WHERE job_id=? AND app_id=?),
		(SELECT COUNT(*) FROM jobs
			WHERE id=? AND type='deploy' AND resource_type='application' AND resource_id=?
				AND status='running' AND phase<>'cancelling' AND attempt=?)`,
		fixture.appID, fixture.deploymentID, fixture.jobID, fixture.appID,
		fixture.jobID, fixture.appID, currentAttempt).Scan(&activations, &bindings, &activeJobs); err != nil {
		t.Fatal(err)
	}
	if activations != 0 || bindings != 1 || activeJobs != 1 {
		t.Fatalf("fixture preconditions activations=%d bindings=%d activeJobs=%d", activations, bindings, activeJobs)
	}
}

func generatedRouteActivationGuardStateFor(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture) generatedRouteActivationGuardState {
	t.Helper()
	var state generatedRouteActivationGuardState
	if err := db.QueryRow(`SELECT
		COALESCE(h.deployment_id,'') || '|' || COALESCE(h.release_id,'') || '|' || COALESCE(h.slot,'') || '|' ||
		CAST(h.generation AS TEXT) || '|' || COALESCE(h.activation_receipt_id,''),
		r.phase,
		(SELECT COUNT(*) FROM generated_route_activations),
		(SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts),
		(SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations),
		(SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts),
		(SELECT COUNT(*) FROM generated_route_completion_receipts)
		FROM generated_runtime_active_heads h
		JOIN generated_runtime_deployments r ON r.deployment_id=?
		WHERE h.app_id=?`, fixture.deploymentID, fixture.appID).Scan(
		&state.head, &state.phase, &state.activations, &state.retirements, &state.authorizations,
		&state.handoffs, &state.completionReceipts); err != nil {
		t.Fatal(err)
	}
	return state
}

func assertGeneratedRouteActivationGuardState(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture,
	want generatedRouteActivationGuardState,
) {
	t.Helper()
	if got := generatedRouteActivationGuardStateFor(t, db, fixture); got != want {
		t.Fatalf("failed guard changed route activation state: got=%#v want=%#v", got, want)
	}
}

func completeGeneratedRouteActivationChain(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture,
	activation generatedRouteActivationRow, writerAttempt int, explicitWriter bool,
) {
	t.Helper()
	prepareGeneratedRouteActivationCompletion(t, db, fixture, activation, writerAttempt, explicitWriter)
	if err := insertGeneratedRouteCompletion(db, fixture, activation, writerAttempt); err != nil {
		t.Fatal(err)
	}
}

func prepareGeneratedRouteActivationCompletion(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture,
	activation generatedRouteActivationRow, writerAttempt int, explicitWriter bool,
) {
	t.Helper()
	if writerAttempt < activation.creatorWorkerAttempt {
		t.Fatalf("writer attempt %d precedes creator %d", writerAttempt, activation.creatorWorkerAttempt)
	}
	if writerAttempt != activation.creatorWorkerAttempt {
		if _, err := db.Exec(`UPDATE jobs SET attempt=? WHERE id=?`, writerAttempt, fixture.jobID); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if explicitWriter {
		err = insertGeneratedRouteActivationWriter(db, activation, fixture.jobID, writerAttempt)
	} else {
		err = insertGeneratedRouteActivation(db, activation)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE generated_runtime_active_heads
			SET deployment_id=?,release_id=?,slot=?,generation=?,activation_receipt_id=?,updated_at=?
			WHERE app_id=? AND generation=0`, []any{activation.successorDeploymentID, activation.successorReleaseID,
			activation.successorSlot, activation.successorGeneration, activation.id, activation.activatedAt, activation.appID}},
		{`UPDATE generated_runtime_components SET state='active',updated_at=?
			WHERE deployment_id=? AND state='healthy'`, []any{activation.activatedAt, activation.successorDeploymentID}},
		{`UPDATE generated_runtime_deployments SET phase='draining',updated_at=?
			WHERE deployment_id=? AND phase='switching_route'`, []any{activation.activatedAt, activation.successorDeploymentID}},
		{`INSERT INTO generated_route_predecessor_retirement_receipts(
			activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
			predecessor_roster_digest,component_count,retired_at
		) VALUES(?,?,?,?,?,?,?)`, []any{activation.id, activation.appID, activation.successorDeploymentID,
			activation.predecessorDeploymentID, activation.predecessorRosterDigest, 0, activation.activatedAt}},
		{`INSERT INTO generated_route_accounting_handoff_authorizations(
			activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
			candidate_roster_digest,predecessor_roster_digest,successor_component_count,
			predecessor_component_count,authorized_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, []any{activation.id, activation.appID, activation.successorDeploymentID,
			activation.predecessorDeploymentID, activation.candidateRosterDigest, activation.predecessorRosterDigest,
			1, 0, activation.activatedAt}},
		{`INSERT INTO generated_route_accounting_handoff_receipts(
			activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
			candidate_roster_digest,predecessor_roster_digest,successor_component_count,
			predecessor_component_count,handed_off_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, []any{activation.id, activation.appID, activation.successorDeploymentID,
			activation.predecessorDeploymentID, activation.candidateRosterDigest, activation.predecessorRosterDigest,
			1, 0, activation.activatedAt}},
		{`UPDATE generated_runtime_deployments SET phase='succeeded',updated_at=?,finished_at=?
			WHERE deployment_id=? AND phase='draining'`, []any{activation.activatedAt, activation.activatedAt, activation.successorDeploymentID}},
		{`UPDATE deployments SET status='succeeded',finished_at=?
			WHERE id=? AND status='waiting_health'`, []any{activation.activatedAt, activation.successorDeploymentID}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func insertGeneratedRouteCompletion(db *sql.DB, fixture generatedRouteActivationFixture,
	activation generatedRouteActivationRow, currentAttempt int,
) error {
	_, err := db.Exec(`INSERT INTO generated_route_completion_receipts(
		activation_receipt_id,app_id,successor_deployment_id,creator_job_id,creator_worker_attempt,
		current_job_id,current_worker_attempt,completion_code,completed_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.creatorJobID, activation.creatorWorkerAttempt, fixture.jobID, currentAttempt,
		"deployment_completed", activation.activatedAt)
	return err
}
