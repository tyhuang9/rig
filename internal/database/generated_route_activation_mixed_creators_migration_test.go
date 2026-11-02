package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const generatedRouteActivationMixedCreatorsMigration = "047_generated_route_activation_mixed_creators.sql"

func TestGeneratedRouteActivationMixedCreatorsMigrationFreshAndMirror(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedRouteActivationMixedCreatorsMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations",
		generatedRouteActivationMixedCreatorsMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("generated route activation mixed-creator migration is not byte mirrored")
	}
	if !strings.Contains(string(embedded), "MIN(i.creator_worker_attempt)") {
		t.Fatal("mixed-creator migration omits deterministic earliest creator")
	}

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied, triggers int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM schema_migrations WHERE version=?),
		(SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name='generated_route_activation_valid_insert')`,
		generatedRouteActivationMixedCreatorsMigration).Scan(&applied, &triggers); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || triggers != 1 {
		t.Fatalf("047 applied=%d activation triggers=%d", applied, triggers)
	}
	assertGeneratedRouteActivationWriterSchema(t, db)
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedRouteActivationMixedCreatorsMigrationPreservesHistoricalChain(t *testing.T) {
	for _, test := range []struct {
		name           string
		explicitWriter bool
	}{
		{name: "legacy null writer", explicitWriter: false},
		{name: "uniform explicit writer", explicitWriter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := migrateFS(db, migrationSetBefore(t, "047_")); err != nil {
				t.Fatal(err)
			}
			fixture := seedGeneratedRouteActivationFixture(t, db, "legacy")
			activation := fixture.activation()
			completeGeneratedRouteActivationChain(t, db, fixture, activation, 1, test.explicitWriter)
			before := generatedRouteActivationHistoryRows(t, db)
			var triggerBefore string
			if err := db.QueryRow(`SELECT sql FROM sqlite_schema
				WHERE type='trigger' AND name='generated_route_activation_valid_insert'`).Scan(&triggerBefore); err != nil {
				t.Fatal(err)
			}

			body, err := migrations.ReadFile("migrations/" + generatedRouteActivationMixedCreatorsMigration)
			if err != nil {
				t.Fatal(err)
			}
			only := fstest.MapFS{"migrations/" + generatedRouteActivationMixedCreatorsMigration: {Data: body}}
			var triggerAfter string
			for pass := 1; pass <= 2; pass++ {
				if err := migrateFS(db, only); err != nil {
					t.Fatalf("apply 047 pass %d: %v", pass, err)
				}
				after := generatedRouteActivationHistoryRows(t, db)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("047 pass %d rewrote historical 041-045 rows: before=%#v after=%#v",
						pass, before, after)
				}
				var current string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema
					WHERE type='trigger' AND name='generated_route_activation_valid_insert'`).Scan(&current); err != nil {
					t.Fatal(err)
				}
				if pass == 1 {
					triggerAfter = current
				} else if current != triggerAfter {
					t.Fatal("047 replay changed the activation trigger")
				}
			}
			if triggerAfter == triggerBefore || !strings.Contains(triggerAfter, "MIN(i.creator_worker_attempt)") {
				t.Fatal("047 did not replace the uniform-creator activation trigger")
			}
			assertNoForeignKeyViolations(t, db)
		})
	}
}

func TestGeneratedRouteActivationMixedCreatorsMigrationGuards(t *testing.T) {
	t.Run("same job mixed attempts use earliest creator and current writer", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedRouteActivationFixtureAtAttempts(t, db, "primary", 1, 3)
		activation := fixture.activation()
		if err := insertGeneratedRouteActivationWriter(db, activation, fixture.jobID, 3); err != nil {
			t.Fatalf("insert genuine mixed-creator activation: %v", err)
		}
		var creatorJob, writerJob string
		var creatorAttempt, writerAttempt int
		if err := db.QueryRow(`SELECT creator_job_id,creator_worker_attempt,writer_job_id,
			writer_worker_attempt FROM generated_route_activations WHERE id=?`, activation.id).
			Scan(&creatorJob, &creatorAttempt, &writerJob, &writerAttempt); err != nil {
			t.Fatal(err)
		}
		if creatorJob != fixture.jobID || writerJob != fixture.jobID ||
			creatorAttempt != 1 || writerAttempt != 3 {
			t.Fatalf("mixed provenance creator=%s/%d writer=%s/%d",
				creatorJob, creatorAttempt, writerJob, writerAttempt)
		}
		assertNoForeignKeyViolations(t, db)
	})

	for _, test := range []struct {
		name           string
		creatorAttempt int
		writerJob      any
		writerAttempt  any
		mutate         func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture)
	}{
		{name: "legacy null writer cannot hide mixed attempts", creatorAttempt: 1},
		{name: "creator must equal earliest roster attempt", creatorAttempt: 3,
			writerJob: "fixture", writerAttempt: 3},
		{name: "writer cannot precede a component creator", creatorAttempt: 1,
			writerJob: "fixture", writerAttempt: 2,
			mutate: func(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture) {
				t.Helper()
				if _, err := db.Exec(`UPDATE jobs SET attempt=2 WHERE id=?`, fixture.jobID); err != nil {
					t.Fatal(err)
				}
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			fixture := seedGeneratedRouteActivationFixtureAtAttempts(t, db, "primary", 1, 3)
			activation := fixture.activation()
			activation.creatorWorkerAttempt = test.creatorAttempt
			if test.mutate != nil {
				test.mutate(t, db, fixture)
			}
			before := generatedRouteActivationGuardStateFor(t, db, fixture)
			writerJob := test.writerJob
			if writerJob == "fixture" {
				writerJob = fixture.jobID
			}
			var err error
			if writerJob == nil && test.writerAttempt == nil {
				err = insertGeneratedRouteActivation(db, activation)
			} else {
				err = insertGeneratedRouteActivationWriter(db, activation, writerJob, test.writerAttempt)
			}
			if err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
			assertGeneratedRouteActivationGuardState(t, db, fixture, before)
			var intents int
			if err := db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents
				WHERE deployment_id=?`, fixture.deploymentID).Scan(&intents); err != nil {
				t.Fatal(err)
			}
			if intents != 2 {
				t.Fatalf("refused mixed activation rewrote 041 roster: count=%d", intents)
			}
		})
	}

	for _, test := range []struct {
		name  string
		setup func(t *testing.T, db *sql.DB) (generatedRouteActivationFixture, generatedRouteActivationRow, any, any)
	}{
		{
			name: "missing component intent",
			setup: func(t *testing.T, db *sql.DB) (generatedRouteActivationFixture, generatedRouteActivationRow, any, any) {
				fixture := seedGeneratedRouteActivationFixtureAtAttempts(t, db, "primary", 1, -1)
				return fixture, fixture.activation(), fixture.jobID, 1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			fixture, activation, writerJob, writerAttempt := test.setup(t, db)
			before := generatedRouteActivationGuardStateFor(t, db, fixture)
			historyBefore := generatedRouteActivationHistoryRows(t, db)
			if err := insertGeneratedRouteActivationWriter(db, activation, writerJob, writerAttempt); err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
			assertGeneratedRouteActivationGuardState(t, db, fixture, before)
			if historyAfter := generatedRouteActivationHistoryRows(t, db); !reflect.DeepEqual(historyAfter, historyBefore) {
				t.Fatalf("%s refusal rewrote 041-045 history: before=%#v after=%#v",
					test.name, historyBefore, historyAfter)
			}
		})
	}

	t.Run("immutable component creator rejects foreign update", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedRouteActivationFixtureAtAttempts(t, db, "primary", 1, 3)
		foreign := seedGeneratedRouteActivationFixture(t, db, "other")
		before := generatedRouteActivationHistoryRows(t, db)
		if _, err := db.Exec(`UPDATE generated_candidate_intents SET creator_job_id=?
			WHERE deployment_id=? AND component_name='frontend'`, foreign.jobID, fixture.deploymentID); err == nil {
			t.Fatal("immutable frontend creator update was accepted")
		}
		if after := generatedRouteActivationHistoryRows(t, db); !reflect.DeepEqual(after, before) {
			t.Fatalf("immutable creator refusal rewrote 041-045 history: before=%#v after=%#v", before, after)
		}
	})
}

func TestGeneratedRouteActivationMixedCreatorsMigrationFailureRestoresTrigger(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "047_")); err != nil {
		t.Fatal(err)
	}
	var triggerBefore string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='trigger' AND name='generated_route_activation_valid_insert'`).Scan(&triggerBefore); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/" + generatedRouteActivationMixedCreatorsMigration)
	if err != nil {
		t.Fatal(err)
	}
	failing := append(append([]byte(nil), body...),
		[]byte("\nSELECT missing_column FROM generated_route_mixed_creator_forced_failure;\n")...)
	err = migrateFS(db, fstest.MapFS{
		"migrations/" + generatedRouteActivationMixedCreatorsMigration: {Data: failing},
	})
	if err == nil {
		t.Fatal("failing 047 migration unexpectedly succeeded")
	}
	var triggerAfter string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='trigger' AND name='generated_route_activation_valid_insert'`).Scan(&triggerAfter); err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
		generatedRouteActivationMixedCreatorsMigration).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if triggerAfter != triggerBefore || applied != 0 {
		t.Fatalf("failed 047 leaked schema state: trigger changed=%t applied=%d",
			triggerAfter != triggerBefore, applied)
	}
}

func generatedRouteActivationHistoryRows(t *testing.T, db *sql.DB) map[string][][]string {
	t.Helper()
	result := make(map[string][][]string)
	for _, table := range []string{
		"generated_candidate_intents",
		"generated_route_activations",
		"generated_route_predecessor_retirements",
		"generated_route_predecessor_retirement_receipts",
		"generated_route_accounting_handoff_authorizations",
		"generated_route_accounting_handoff_receipts",
		"generated_route_completion_receipts",
		"generated_runtime_active_heads",
	} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			record := make([]string, len(values))
			for index, value := range values {
				switch typed := value.(type) {
				case nil:
					record[index] = "<NULL>"
				case []byte:
					record[index] = "bytes:" + string(typed)
				default:
					record[index] = fmt.Sprintf("%T:%v", typed, typed)
				}
			}
			result[table] = append(result[table], record)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
