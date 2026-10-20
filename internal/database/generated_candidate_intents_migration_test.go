package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const generatedCandidateIntentsMigration = "041_generated_candidate_intents.sql"

const insertGeneratedCandidateIntentSQL = `INSERT INTO generated_candidate_intents(
	id,deployment_id,component_name,creator_job_id,creator_worker_attempt,
	configuration_revision_id,configuration_revision_number,image_artifact_id,image_artifact_attempt,
	environment_operation_id,environment_staging_attempt,candidate_policy_version,candidate_policy_digest,created_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

type generatedCandidateIntentFixture struct {
	appID           string
	planID          string
	releaseConfigID string
	configID        string
	releaseID       string
	jobID           string
	deploymentID    string
	artifactID      string
	receiptID       string
	componentName   string
	jobAttempt      int64
	artifactTry     int64
	stamp           string
}

type generatedCandidateIntentRow struct {
	id                        string
	deploymentID              string
	componentName             string
	creatorJobID              string
	creatorWorkerAttempt      int64
	configurationRevisionID   string
	configurationRevision     int64
	imageArtifactID           string
	imageArtifactAttempt      int64
	environmentOperationID    string
	environmentStagingAttempt int64
	policyVersion             string
	policyDigest              string
	createdAt                 string
}

type generatedCandidateIntentExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func TestGeneratedCandidateIntentsMigrationFreshSchemaAndMirror(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedCandidateIntentsMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", generatedCandidateIntentsMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("generated candidate intents migration is not byte mirrored")
	}

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertGeneratedCandidateIntentSchema(t, db)
	assertGeneratedCandidateIntentForeignKeys(t, db)
	assertNoForeignKeyViolations(t, db)
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, generatedCandidateIntentsMigration).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration record=%d error=%v", applied, err)
	}
}

func TestGeneratedCandidateIntentsMigrationUpgradeReopenAndNoBackfill(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataRoot, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if err := migrateFS(db, migrationSetBefore(t, "041_")); err != nil {
		t.Fatal(err)
	}
	fixture := seedGeneratedCandidateIntentFixture(t, db)
	if _, err := db.Exec(`INSERT INTO job_events(job_id,sequence,attempt,timestamp,level,phase,code,message)
		VALUES(?,1,?,'2026-10-20T01:02:04Z','info','running','retained_event','retained message')`, fixture.jobID, fixture.jobAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events(actor_id,action,resource_type,resource_id,metadata_json,created_at)
		VALUES('candidate-owner','candidate.history','deployment',?,'{"retained":true}','2026-10-20T01:02:05Z')`, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}

	body, err := migrations.ReadFile("migrations/" + generatedCandidateIntentsMigration)
	if err != nil {
		t.Fatal(err)
	}
	only := fstest.MapFS{"migrations/" + generatedCandidateIntentsMigration: {Data: body}}
	for pass := 1; pass <= 2; pass++ {
		if err := migrateFS(db, only); err != nil {
			t.Fatalf("apply migration pass %d: %v", pass, err)
		}
	}
	assertGeneratedCandidateIntentSchema(t, db)
	assertGeneratedCandidateIntentForeignKeys(t, db)
	assertGeneratedCandidateIntentHistory(t, db, fixture, 0)
	assertNoForeignKeyViolations(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertGeneratedCandidateIntentHistory(t, reopened, fixture, 0)
	assertNoForeignKeyViolations(t, reopened)
}

func TestGeneratedCandidateIntentValidReceiptIsImmutableAcrossWorkerAttempts(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	fixture := seedGeneratedCandidateIntentFixture(t, db)
	if fixture.artifactTry == fixture.jobAttempt {
		t.Fatal("fixture must preserve an artifact retry distinct from the creator worker attempt")
	}
	var releaseConfigID, selectedConfigID string
	var releaseConfigRevision, selectedConfigRevision int64
	if err := db.QueryRow(`SELECT r.configuration_revision_id,r.configuration_revision_number,
		d.actual_configuration_revision_id,d.actual_configuration_revision_number
		FROM releases r JOIN deployments d ON d.release_id=r.id
		WHERE d.id=?`, fixture.deploymentID).Scan(
		&releaseConfigID, &releaseConfigRevision, &selectedConfigID, &selectedConfigRevision,
	); err != nil {
		t.Fatal(err)
	}
	if releaseConfigID != fixture.releaseConfigID || releaseConfigRevision != 1 ||
		selectedConfigID != fixture.configID || selectedConfigRevision != 2 || releaseConfigID == selectedConfigID {
		t.Fatalf("configuration provenance release=%q/%d selected=%q/%d",
			releaseConfigID, releaseConfigRevision, selectedConfigID, selectedConfigRevision)
	}
	row := fixture.intent()
	if err := insertGeneratedCandidateIntent(db, row); err != nil {
		t.Fatalf("insert valid intent: %v", err)
	}
	if _, err := db.Exec(`UPDATE jobs SET attempt=attempt+1,updated_at='2026-10-20T02:00:00Z' WHERE id=?`, fixture.jobID); err != nil {
		t.Fatalf("advance worker attempt: %v", err)
	}
	var creatorAttempt, stagingAttempt, artifactAttempt int64
	var creatorJob, operationID, policyVersion, policyDigest string
	if err := db.QueryRow(`SELECT creator_job_id,creator_worker_attempt,image_artifact_attempt,
		environment_operation_id,environment_staging_attempt,candidate_policy_version,candidate_policy_digest
		FROM generated_candidate_intents WHERE id=?`, fixture.receiptID).Scan(
		&creatorJob, &creatorAttempt, &artifactAttempt, &operationID, &stagingAttempt, &policyVersion, &policyDigest,
	); err != nil {
		t.Fatal(err)
	}
	if creatorJob != fixture.jobID || creatorAttempt != fixture.jobAttempt || stagingAttempt != fixture.jobAttempt ||
		artifactAttempt != fixture.artifactTry || operationID != fixture.artifactID ||
		policyVersion != "generated-candidate-v1" || policyDigest != strings.Repeat("d", 64) {
		t.Fatalf("retained receipt job=%q creator=%d staging=%d artifact=%d operation=%q policy=%q/%q",
			creatorJob, creatorAttempt, stagingAttempt, artifactAttempt, operationID, policyVersion, policyDigest)
	}

	if _, err := db.Exec(`PRAGMA recursive_triggers = OFF`); err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, db, "intent update", `UPDATE generated_candidate_intents SET created_at='changed' WHERE id=?`, fixture.receiptID)
	assertSQLRejected(t, db, "intent delete", `DELETE FROM generated_candidate_intents WHERE id=?`, fixture.receiptID)
	assertSQLRejected(t, db, "intent replace", `INSERT OR REPLACE INTO generated_candidate_intents
		SELECT * FROM generated_candidate_intents WHERE id=?`, fixture.receiptID)
	// Use the current creator attempt so eligibility cannot mask a missing
	// duplicate guard. With recursive triggers off, REPLACE must still fail
	// for both the primary key and the deployment/component unique key.
	for _, replacementID := range []string{fixture.receiptID, "99999999-9999-4999-8999-999999999999"} {
		replacement := fixture.intent()
		replacement.id = replacementID
		replacement.creatorWorkerAttempt++
		replacement.environmentStagingAttempt++
		assertSQLRejected(t, db, "otherwise valid intent replace",
			strings.Replace(insertGeneratedCandidateIntentSQL, "INSERT INTO", "INSERT OR REPLACE INTO", 1),
			replacement.id, replacement.deploymentID, replacement.componentName, replacement.creatorJobID, replacement.creatorWorkerAttempt,
			replacement.configurationRevisionID, replacement.configurationRevision, replacement.imageArtifactID, replacement.imageArtifactAttempt,
			replacement.environmentOperationID, replacement.environmentStagingAttempt, replacement.policyVersion, replacement.policyDigest, replacement.createdAt)
	}
	var retainedID string
	var retainedCount, retainedCreator, retainedStaging int64
	if err := db.QueryRow(`SELECT COUNT(*),id,creator_worker_attempt,environment_staging_attempt
		FROM generated_candidate_intents WHERE deployment_id=? AND component_name=?`,
		fixture.deploymentID, fixture.componentName).Scan(&retainedCount, &retainedID, &retainedCreator, &retainedStaging); err != nil {
		t.Fatal(err)
	}
	if retainedCount != 1 || retainedID != fixture.receiptID || retainedCreator != fixture.jobAttempt || retainedStaging != fixture.jobAttempt {
		t.Fatalf("replacement rewrote retained receipt: count=%d id=%q creator=%d staging=%d", retainedCount, retainedID, retainedCreator, retainedStaging)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedCandidateIntentRejectsDuplicateAndInvalidProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, generatedCandidateIntentFixture, *generatedCandidateIntentRow)
	}{
		{name: "non canonical receipt id", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.id = "not-a-receipt"
		}},
		{name: "unknown creator job", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.creatorJobID = "missing-job"
		}},
		{name: "wrong creator attempt", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.creatorWorkerAttempt++
		}},
		{name: "changed current attempt", mutate: func(t *testing.T, db *sql.DB, fixture generatedCandidateIntentFixture, _ *generatedCandidateIntentRow) {
			if _, err := db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, fixture.jobID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong configuration revision", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.configurationRevision++
		}},
		{name: "release configuration is not selected", mutate: func(_ *testing.T, _ *sql.DB, fixture generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.configurationRevisionID = fixture.releaseConfigID
			row.configurationRevision = 1
		}},
		{name: "wrong artifact attempt", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.imageArtifactAttempt++
		}},
		{name: "wrong staging operation", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.environmentOperationID = "99999999-9999-4999-8999-999999999999"
		}},
		{name: "wrong staging attempt", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.environmentStagingAttempt++
		}},
		{name: "empty policy version", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.policyVersion = ""
		}},
		{name: "non canonical policy digest", mutate: func(_ *testing.T, _ *sql.DB, _ generatedCandidateIntentFixture, row *generatedCandidateIntentRow) {
			row.policyDigest = strings.Repeat("D", 64)
		}},
		{name: "ineligible component state", mutate: func(t *testing.T, db *sql.DB, fixture generatedCandidateIntentFixture, _ *generatedCandidateIntentRow) {
			if _, err := db.Exec(`UPDATE generated_runtime_components SET state='starting',container_name='rig-candidate-api'
				WHERE deployment_id=? AND component_name=?`, fixture.deploymentID, fixture.componentName); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "ineligible deployment status", mutate: func(t *testing.T, db *sql.DB, fixture generatedCandidateIntentFixture, _ *generatedCandidateIntentRow) {
			if _, err := db.Exec(`UPDATE deployments SET status='waiting_health' WHERE id=?`, fixture.deploymentID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			fixture := seedGeneratedCandidateIntentFixture(t, db)
			row := fixture.intent()
			test.mutate(t, db, fixture, &row)
			if err := insertGeneratedCandidateIntent(db, row); err == nil {
				t.Fatal("invalid generated candidate intent was accepted")
			}
		})
	}

	t.Run("duplicate deployment component", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedCandidateIntentFixture(t, db)
		row := fixture.intent()
		if err := insertGeneratedCandidateIntent(db, row); err != nil {
			t.Fatal(err)
		}
		row.id = "99999999-9999-4999-8999-999999999999"
		if err := insertGeneratedCandidateIntent(db, row); err == nil {
			t.Fatal("second receipt for deployment component was accepted")
		}
	})

	t.Run("null receipt id", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedCandidateIntentFixture(t, db)
		args := generatedCandidateIntentArgs(fixture.intent())
		args[0] = nil
		if _, err := db.Exec(insertGeneratedCandidateIntentSQL, args...); err == nil {
			t.Fatal("NULL generated candidate intent id was accepted")
		}
	})
}

func TestGeneratedCandidateIntentReceiptAndStartingStateAreTransactionAtomic(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedCandidateIntentFixture(t, db)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := insertGeneratedCandidateIntent(tx, fixture.intent()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE generated_runtime_components
			SET state='starting',container_name='rig-candidate-api',updated_at='2026-10-20T01:03:00Z'
			WHERE deployment_id=? AND component_name=? AND state='image_ready'`, fixture.deploymentID, fixture.componentName); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertGeneratedCandidateIntentState(t, db, fixture, 1, "starting", true)
	})

	t.Run("rollback after deliberate failure", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		fixture := seedGeneratedCandidateIntentFixture(t, db)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := insertGeneratedCandidateIntent(tx, fixture.intent()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE generated_runtime_components
			SET state='starting',container_name='rig-candidate-api',updated_at='2026-10-20T01:03:00Z'
			WHERE deployment_id=? AND component_name=? AND state='image_ready'`, fixture.deploymentID, fixture.componentName); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE generated_candidate_intents SET created_at='forbidden' WHERE id=?`, fixture.receiptID); err == nil {
			_ = tx.Rollback()
			t.Fatal("deliberate transaction failure was accepted")
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		assertGeneratedCandidateIntentState(t, db, fixture, 0, "image_ready", false)
	})
}

func seedGeneratedCandidateIntentFixture(t *testing.T, db *sql.DB) generatedCandidateIntentFixture {
	t.Helper()
	fixture := generatedCandidateIntentFixture{
		appID:           "11111111-1111-4111-8111-111111111111",
		planID:          "22222222-2222-4222-8222-222222222222",
		releaseConfigID: "99999999-9999-4999-8999-999999999999",
		configID:        "33333333-3333-4333-8333-333333333333",
		releaseID:       "44444444-4444-4444-8444-444444444444",
		jobID:           "55555555-5555-4555-8555-555555555555",
		deploymentID:    "66666666-6666-4666-8666-666666666666",
		artifactID:      "77777777-7777-4777-8777-777777777777",
		receiptID:       "88888888-8888-4888-8888-888888888888",
		componentName:   "api",
		jobAttempt:      7,
		artifactTry:     4,
		stamp:           "2026-10-20T01:02:03Z",
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at)
		VALUES('candidate-owner','candidate-owner','hash',?,?)`, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,'draft',?,?)`, fixture.appID, fixture.appID, "Candidate App", fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,1,'','candidate-owner',?,'accepted','candidate-owner',?)`,
		fixture.planID, fixture.appID, "candidate-plan:"+fixture.planID, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES
		(?,?,1,?,'candidate-owner',?,0,0,2,?,1),
		(?,?,2,?,'candidate-owner',?,0,0,2,?,1)`,
		fixture.releaseConfigID, fixture.appID, "candidate-config:"+fixture.releaseConfigID, fixture.stamp, fixture.planID,
		fixture.configID, fixture.appID, "candidate-config:"+fixture.configID, fixture.stamp, fixture.planID)
	mustExec(`UPDATE application_configuration_heads
		SET revision_id=?,revision_number=2,updated_at=? WHERE app_id=?`,
		fixture.configID, fixture.stamp, fixture.appID)
	mustExec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
		workspace_tree_sha256,configuration_revision_id,configuration_revision_number,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1,?,1)`,
		fixture.releaseID, fixture.appID, fixture.stamp, strings.Repeat("b", 64), strings.Repeat("e", 64), fixture.releaseConfigID, fixture.planID)
	mustExec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,started_at,updated_at
	) VALUES(?,'deploy','application',?,'running','running',?,'candidate-owner','{}',?,?,?)`,
		fixture.jobID, fixture.appID, fixture.jobAttempt, fixture.stamp, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,started_at,configuration_mode,actual_configuration_revision_id,
		actual_configuration_revision_number,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?, 'applying',?,'current',?,2,1,'generated_node',?,1)`,
		fixture.deploymentID, fixture.appID, fixture.releaseID, fixture.jobID, fixture.stamp, fixture.configID, fixture.planID)
	mustExec(`INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,phase,migration_state,created_at,updated_at
	) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`,
		fixture.deploymentID, fixture.appID, fixture.releaseID, fixture.planID, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'candidate-test',?,?,?,'ready',?,?,?)`,
		fixture.artifactID, fixture.releaseID, fixture.planID, 1, fixture.componentName,
		strings.Repeat("f", 64), fixture.artifactTry, "sha256:"+strings.Repeat("1", 64), fixture.stamp, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO generated_runtime_components(
		deployment_id,component_name,slot,image_artifact_id,state,created_at,updated_at
	) VALUES(?,?,'blue',?,'image_ready',?,?)`,
		fixture.deploymentID, fixture.componentName, fixture.artifactID, fixture.stamp, fixture.stamp)
	mustExec(`UPDATE generated_runtime_deployments SET phase='building',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='starting_candidate',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID)
	return fixture
}

func (f generatedCandidateIntentFixture) intent() generatedCandidateIntentRow {
	return generatedCandidateIntentRow{
		id: f.receiptID, deploymentID: f.deploymentID, componentName: f.componentName,
		creatorJobID: f.jobID, creatorWorkerAttempt: f.jobAttempt,
		configurationRevisionID: f.configID, configurationRevision: 2,
		imageArtifactID: f.artifactID, imageArtifactAttempt: f.artifactTry,
		environmentOperationID: f.artifactID, environmentStagingAttempt: f.jobAttempt,
		policyVersion: "generated-candidate-v1", policyDigest: strings.Repeat("d", 64), createdAt: f.stamp,
	}
}

func insertGeneratedCandidateIntent(exec generatedCandidateIntentExecer, row generatedCandidateIntentRow) error {
	_, err := exec.Exec(insertGeneratedCandidateIntentSQL, generatedCandidateIntentArgs(row)...)
	return err
}

func generatedCandidateIntentArgs(row generatedCandidateIntentRow) []any {
	return []any{
		row.id, row.deploymentID, row.componentName, row.creatorJobID, row.creatorWorkerAttempt,
		row.configurationRevisionID, row.configurationRevision, row.imageArtifactID, row.imageArtifactAttempt,
		row.environmentOperationID, row.environmentStagingAttempt, row.policyVersion, row.policyDigest, row.createdAt,
	}
}

func assertGeneratedCandidateIntentSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='generated_candidate_intents'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candidate intent table count=%d error=%v", count, err)
	}
	for _, trigger := range []string{
		"generated_candidate_intent_valid_insert",
		"generated_candidate_intent_immutable",
		"generated_candidate_intent_retain",
	} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("candidate intent trigger %s count=%d error=%v", trigger, count, err)
		}
	}
}

func assertGeneratedCandidateIntentForeignKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list(generated_candidate_intents)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	foreignKeys := map[string]string{}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		foreignKeys[from] = table
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"deployment_id": "generated_runtime_components", "component_name": "generated_runtime_components",
		"creator_job_id": "jobs", "configuration_revision_id": "application_configuration_revisions",
		"image_artifact_id": "generated_image_artifacts", "environment_operation_id": "generated_image_artifacts",
	}
	for column, table := range want {
		if foreignKeys[column] != table {
			t.Fatalf("candidate intent foreign key %s=%q want %q; all=%#v", column, foreignKeys[column], table, foreignKeys)
		}
	}
}

func assertGeneratedCandidateIntentHistory(t *testing.T, db *sql.DB, fixture generatedCandidateIntentFixture, wantIntents int) {
	t.Helper()
	var intents, events, audits int
	var state, artifactID string
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state,image_artifact_id FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.deploymentID, fixture.componentName).Scan(&state, &artifactID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_events WHERE job_id=? AND code='retained_event'`, fixture.jobID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_id=? AND action='candidate.history'`, fixture.deploymentID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if intents != wantIntents || state != "image_ready" || artifactID != fixture.artifactID || events != 1 || audits != 1 {
		t.Fatalf("preserved history intents=%d state=%q artifact=%q events=%d audits=%d", intents, state, artifactID, events, audits)
	}
}

func assertGeneratedCandidateIntentState(t *testing.T, db *sql.DB, fixture generatedCandidateIntentFixture, wantIntents int, wantState string, wantName bool) {
	t.Helper()
	var intents int
	var state string
	var name sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=? AND component_name=?`, fixture.deploymentID, fixture.componentName).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state,container_name FROM generated_runtime_components
		WHERE deployment_id=? AND component_name=?`, fixture.deploymentID, fixture.componentName).Scan(&state, &name); err != nil {
		t.Fatal(err)
	}
	if intents != wantIntents || state != wantState || name.Valid != wantName {
		t.Fatalf("transaction result intents=%d state=%q name=%#v", intents, state, name)
	}
}
