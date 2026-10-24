package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const generatedRouteActivationsMigration = "042_generated_route_activations.sql"

func TestGeneratedRouteActivationsMigrationFreshSchemaAndMirror(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedRouteActivationsMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", generatedRouteActivationsMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("generated route activations migration is not byte mirrored")
	}

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertGeneratedRouteActivationSchema(t, db)
	assertNoForeignKeyViolations(t, db)
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, generatedRouteActivationsMigration).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration record=%d error=%v", applied, err)
	}
}

func TestGeneratedRouteActivationsMigrationUpgradeHasNoHistoricalBackfill(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "042_")); err != nil {
		t.Fatal(err)
	}
	fixture := seedGeneratedRouteActivationFixture(t, db, "legacy")
	activateLegacyGeneratedRouteHead(t, db, fixture)

	body, err := migrations.ReadFile("migrations/" + generatedRouteActivationsMigration)
	if err != nil {
		t.Fatal(err)
	}
	only := fstest.MapFS{"migrations/" + generatedRouteActivationsMigration: {Data: body}}
	for pass := 1; pass <= 2; pass++ {
		if err := migrateFS(db, only); err != nil {
			t.Fatalf("apply migration pass %d: %v", pass, err)
		}
	}
	assertGeneratedRouteActivationSchema(t, db)
	var receipts, generation int
	var deploymentID, releaseID, slot string
	var activationReceipt sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT deployment_id,release_id,slot,generation,activation_receipt_id
		FROM generated_runtime_active_heads WHERE app_id=?`, fixture.appID).
		Scan(&deploymentID, &releaseID, &slot, &generation, &activationReceipt); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || deploymentID != fixture.deploymentID || releaseID != fixture.releaseID || slot != "blue" || generation != 1 || activationReceipt.Valid {
		t.Fatalf("historical head was rewritten: receipts=%d deployment=%q release=%q slot=%q generation=%d activation=%#v",
			receipts, deploymentID, releaseID, slot, generation, activationReceipt)
	}
	assertSQLRejected(t, db, "legacy head advance without activation receipt",
		`UPDATE generated_runtime_active_heads SET generation=2,updated_at=? WHERE app_id=?`, fixture.stamp, fixture.appID)
	assertGeneratedRouteActivationState(t, db, fixture, fixture.deploymentID, 1, "", "draining", 0)
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedRouteActivationsMigrationActivationGuards(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	fixture := seedGeneratedRouteActivationFixture(t, db, "primary")
	activation := fixture.activation()

	assertSQLRejected(t, db, "head identity without receipt",
		`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=?
		WHERE app_id=?`, fixture.deploymentID, fixture.releaseID, fixture.stamp, fixture.appID)
	assertGeneratedRouteActivationState(t, db, fixture, "", 0, "", "switching_route", 0)

	if err := insertGeneratedRouteActivation(db, activation); err != nil {
		t.Fatal(err)
	}
	assertGeneratedRouteActivationState(t, db, fixture, "", 0, "", "switching_route", 1)

	duplicate := activation
	duplicate.id = "20000000-0000-4000-8000-000000000001"
	if err := insertGeneratedRouteActivation(db, duplicate); err == nil {
		t.Fatal("duplicate activation successor was accepted")
	}
	assertGeneratedRouteActivationState(t, db, fixture, "", 0, "", "switching_route", 1)

	assertSQLRejected(t, db, "activation update",
		`UPDATE generated_route_activations SET activated_at=? WHERE id=?`, "changed", activation.id)
	assertSQLRejected(t, db, "activation delete", `DELETE FROM generated_route_activations WHERE id=?`, activation.id)
	assertGeneratedRouteActivationState(t, db, fixture, "", 0, "", "switching_route", 1)
	assertGeneratedRouteActivationRecord(t, db, activation)

	other := seedGeneratedRouteActivationFixture(t, db, "other")
	otherActivation := other.activation()
	crossedPredecessor := otherActivation
	crossedPredecessor.predecessorDeploymentID = "crossed-predecessor"
	crossedPredecessor.predecessorReleaseID = "crossed-release"
	crossedPredecessor.predecessorSlot = "green"
	crossedPredecessor.predecessorGeneration = 1
	crossedPredecessor.predecessorActivationReceiptID = "crossed-receipt"
	if err := insertGeneratedRouteActivation(db, crossedPredecessor); err == nil {
		t.Fatal("crossed predecessor activation was accepted")
	}
	assertGeneratedRouteActivationState(t, db, other, "", 0, "", "switching_route", 0)
	if err := insertGeneratedRouteActivation(db, otherActivation); err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, db, "crossed activation receipt",
		`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=?,activation_receipt_id=?
		WHERE app_id=?`, fixture.deploymentID, fixture.releaseID, fixture.stamp, otherActivation.id, fixture.appID)
	assertGeneratedRouteActivationState(t, db, fixture, "", 0, "", "switching_route", 1)

	if _, err := db.Exec(`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=?,activation_receipt_id=?
		WHERE app_id=?`, fixture.deploymentID, fixture.releaseID, fixture.stamp, activation.id, fixture.appID); err != nil {
		t.Fatal(err)
	}
	assertGeneratedRouteActivationState(t, db, fixture, fixture.deploymentID, 1, activation.id, "switching_route", 1)
	assertSQLRejected(t, db, "head receipt removal",
		`UPDATE generated_runtime_active_heads SET activation_receipt_id=NULL WHERE app_id=?`, fixture.appID)
	assertGeneratedRouteActivationState(t, db, fixture, fixture.deploymentID, 1, activation.id, "switching_route", 1)

	assertSQLRejected(t, db, "draining without exact head receipt",
		`UPDATE generated_runtime_deployments SET phase='draining',updated_at=? WHERE deployment_id=?`, fixture.stamp, other.deploymentID)
	assertGeneratedRouteActivationState(t, db, other, "", 0, "", "switching_route", 1)
	if _, err := db.Exec(`UPDATE generated_runtime_deployments SET phase='draining',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	assertGeneratedRouteActivationState(t, db, fixture, fixture.deploymentID, 1, activation.id, "draining", 1)
	assertNoForeignKeyViolations(t, db)
}

func assertGeneratedRouteActivationSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"generated_route_activations", "generated_runtime_active_heads"} {
		var found int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found); err != nil || found != 1 {
			t.Fatalf("table %q found=%d error=%v", table, found, err)
		}
	}
	for _, trigger := range []string{
		"generated_route_activation_valid_insert",
		"generated_route_activation_immutable",
		"generated_route_activation_retain",
		"generated_runtime_active_head_activation_guard",
		"generated_runtime_draining_activation_guard",
	} {
		var found int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&found); err != nil || found != 1 {
			t.Fatalf("trigger %q found=%d error=%v", trigger, found, err)
		}
	}
	rows, err := db.Query(`PRAGMA table_info(generated_runtime_active_heads)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	foundActivation := false
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "activation_receipt_id" {
			foundActivation = dataType == "TEXT" && notNull == 0
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !foundActivation {
		t.Fatal("generated runtime active head lacks nullable activation receipt reference")
	}
}

const insertGeneratedRouteActivationSQL = `INSERT INTO generated_route_activations(
	id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
	predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
	predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,candidate_roster_digest,
	predecessor_roster_digest,route_intent_digest,committed_route_proof_digest,activated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

type generatedRouteActivationRow struct {
	id                             string
	appID                          string
	successorDeploymentID          string
	successorReleaseID             string
	successorSlot                  string
	successorGeneration            int
	predecessorDeploymentID        string
	predecessorReleaseID           string
	predecessorSlot                string
	predecessorGeneration          int
	predecessorActivationReceiptID string
	creatorJobID                   string
	creatorWorkerAttempt           int
	candidateRosterDigest          string
	predecessorRosterDigest        string
	routeIntentDigest              string
	committedRouteProofDigest      string
	activatedAt                    string
}

type generatedRouteActivationFixture struct {
	appID        string
	planID       string
	configID     string
	releaseID    string
	jobID        string
	deploymentID string
	artifactID   string
	intentID     string
	activationID string
	stamp        string
}

func seedGeneratedRouteActivationFixture(t *testing.T, db *sql.DB, name string) generatedRouteActivationFixture {
	t.Helper()
	ids := map[string][2]string{
		"primary": {"10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"},
		"other":   {"30000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000002"},
		"legacy":  {"40000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000002"},
	}
	pair, ok := ids[name]
	if !ok {
		t.Fatalf("unknown generated route activation fixture %q", name)
	}
	fixture := generatedRouteActivationFixture{
		appID:        "route-activation-" + name + "-app",
		planID:       "route-activation-" + name + "-plan",
		configID:     "route-activation-" + name + "-config",
		releaseID:    "route-activation-" + name + "-release",
		jobID:        "route-activation-" + name + "-job",
		deploymentID: "route-activation-" + name + "-deployment",
		artifactID:   "route-activation-" + name + "-artifact",
		activationID: pair[0],
		intentID:     pair[1],
		stamp:        time.Now().UTC().Format(time.RFC3339Nano),
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at)
		VALUES(?,?,?,?,?)`, "route-activation-"+name+"-owner", "route-activation-"+name+"-owner", "hash", fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES(?,?,?,'draft',?,?)`, fixture.appID, fixture.appID, "Route Activation "+name, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,1,'',?,?, 'accepted',?,?)`,
		fixture.planID, fixture.appID, "route-plan:"+fixture.planID, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
		"route-activation-"+name+"-owner", fixture.stamp, "route-activation-"+name+"-owner", fixture.stamp)
	mustExec(`INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,1,?,?,?,0,0,2,?,1)`,
		fixture.configID, fixture.appID, "route-config:"+fixture.configID, "route-activation-"+name+"-owner", fixture.stamp, fixture.planID)
	mustExec(`UPDATE application_configuration_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`,
		fixture.configID, fixture.stamp, fixture.appID)
	mustExec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
		workspace_tree_sha256,configuration_revision_id,configuration_revision_number,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1,?,1)`,
		fixture.releaseID, fixture.appID, fixture.stamp, strings.Repeat("b", 64), strings.Repeat("e", 64), fixture.configID, fixture.planID)
	mustExec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,started_at,updated_at
	) VALUES(?,'deploy','application',?,'running','running',1,?,'{}',?,?,?)`,
		fixture.jobID, fixture.appID, "route-activation-"+name+"-owner", fixture.stamp, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,started_at,configuration_mode,actual_configuration_revision_id,
		actual_configuration_revision_number,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?, 'applying',?,'current',?,1,1,'generated_node',?,1)`,
		fixture.deploymentID, fixture.appID, fixture.releaseID, fixture.jobID, fixture.stamp, fixture.configID, fixture.planID)
	mustExec(`INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,phase,migration_state,created_at,updated_at
	) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`,
		fixture.deploymentID, fixture.appID, fixture.releaseID, fixture.planID, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
	) VALUES(?,?,?,1,?,'route-test',?,1,?,'ready',?,?,?)`,
		fixture.artifactID, fixture.releaseID, fixture.planID, "api", strings.Repeat("f", 64), "sha256:"+strings.Repeat("1", 64), fixture.stamp, fixture.stamp, fixture.stamp)
	mustExec(`INSERT INTO generated_runtime_components(
		deployment_id,component_name,slot,image_artifact_id,state,created_at,updated_at
	) VALUES(?,'api','blue',?,'image_ready',?,?)`,
		fixture.deploymentID, fixture.artifactID, fixture.stamp, fixture.stamp)
	mustExec(`UPDATE generated_runtime_deployments SET phase='building',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='starting_candidate',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID)
	mustExec(`INSERT INTO generated_candidate_intents(
		id,deployment_id,component_name,creator_job_id,creator_worker_attempt,
		configuration_revision_id,configuration_revision_number,image_artifact_id,image_artifact_attempt,
		environment_operation_id,environment_staging_attempt,candidate_policy_version,candidate_policy_digest,created_at
	) VALUES(?,?, 'api', ?,1, ?,1, ?,1, ?,1,'generated-candidate-v1',?,?)`,
		fixture.intentID, fixture.deploymentID, fixture.jobID, fixture.configID, fixture.artifactID, fixture.artifactID,
		strings.Repeat("d", 64), fixture.stamp)
	mustExec(`UPDATE generated_runtime_components
		SET state='starting',container_name=?,updated_at=? WHERE deployment_id=? AND component_name='api'`,
		"rig-route-"+name+"-api", fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE generated_runtime_components
		SET state='running',container_id=?,updated_at=? WHERE deployment_id=? AND component_name='api'`,
		strings.Repeat("1", 64), fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE generated_runtime_components SET state='healthy',updated_at=? WHERE deployment_id=? AND component_name='api'`,
		fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='waiting_health',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='switching_route',updated_at=? WHERE deployment_id=?`, fixture.stamp, fixture.deploymentID)
	mustExec(`UPDATE deployments SET status='waiting_health' WHERE id=?`, fixture.deploymentID)
	return fixture
}

func (f generatedRouteActivationFixture) activation() generatedRouteActivationRow {
	return generatedRouteActivationRow{
		id:                        f.activationID,
		appID:                     f.appID,
		successorDeploymentID:     f.deploymentID,
		successorReleaseID:        f.releaseID,
		successorSlot:             "blue",
		successorGeneration:       1,
		creatorJobID:              f.jobID,
		creatorWorkerAttempt:      1,
		candidateRosterDigest:     strings.Repeat("2", 64),
		predecessorRosterDigest:   strings.Repeat("3", 64),
		routeIntentDigest:         strings.Repeat("4", 64),
		committedRouteProofDigest: strings.Repeat("5", 64),
		activatedAt:               f.stamp,
	}
}

func insertGeneratedRouteActivation(db *sql.DB, row generatedRouteActivationRow) error {
	_, err := db.Exec(insertGeneratedRouteActivationSQL,
		row.id, row.appID, row.successorDeploymentID, row.successorReleaseID, row.successorSlot, row.successorGeneration,
		row.predecessorDeploymentID, row.predecessorReleaseID, row.predecessorSlot, row.predecessorGeneration,
		row.predecessorActivationReceiptID, row.creatorJobID, row.creatorWorkerAttempt, row.candidateRosterDigest,
		row.predecessorRosterDigest, row.routeIntentDigest, row.committedRouteProofDigest, row.activatedAt)
	return err
}

func activateLegacyGeneratedRouteHead(t *testing.T, db *sql.DB, fixture generatedRouteActivationFixture) {
	t.Helper()
	if _, err := db.Exec(`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=? WHERE app_id=?`,
		fixture.deploymentID, fixture.releaseID, fixture.stamp, fixture.appID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generated_runtime_components SET state='active',updated_at=?
		WHERE deployment_id=? AND component_name='api'`, fixture.stamp, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generated_runtime_deployments SET phase='draining',updated_at=? WHERE deployment_id=?`,
		fixture.stamp, fixture.deploymentID); err != nil {
		t.Fatal(err)
	}
}

func assertGeneratedRouteActivationState(
	t *testing.T,
	db *sql.DB,
	fixture generatedRouteActivationFixture,
	wantDeploymentID string,
	wantGeneration int,
	wantActivationID string,
	wantPhase string,
	wantActivations int,
) {
	t.Helper()
	var deploymentID, releaseID, slot, activationID sql.NullString
	var generation int
	if err := db.QueryRow(`SELECT deployment_id,release_id,slot,generation,activation_receipt_id
		FROM generated_runtime_active_heads WHERE app_id=?`, fixture.appID).
		Scan(&deploymentID, &releaseID, &slot, &generation, &activationID); err != nil {
		t.Fatal(err)
	}
	wantHead := wantDeploymentID != ""
	wantReleaseID, wantSlot := "", ""
	if wantHead {
		wantReleaseID, wantSlot = fixture.releaseID, "blue"
	}
	if deploymentID.String != wantDeploymentID || deploymentID.Valid != wantHead ||
		releaseID.String != wantReleaseID || releaseID.Valid != wantHead ||
		slot.String != wantSlot || slot.Valid != wantHead ||
		generation != wantGeneration || activationID.String != wantActivationID || activationID.Valid != (wantActivationID != "") {
		t.Fatalf("head deployment=%#v release=%#v slot=%#v generation=%d activation=%#v", deploymentID, releaseID, slot, generation, activationID)
	}
	var phase string
	if err := db.QueryRow(`SELECT phase FROM generated_runtime_deployments WHERE deployment_id=?`, fixture.deploymentID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	var activations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations WHERE app_id=?`, fixture.appID).Scan(&activations); err != nil {
		t.Fatal(err)
	}
	if phase != wantPhase || activations != wantActivations {
		t.Fatalf("activation state phase=%q activations=%d", phase, activations)
	}
}

func assertGeneratedRouteActivationRecord(t *testing.T, db *sql.DB, want generatedRouteActivationRow) {
	t.Helper()
	var actual generatedRouteActivationRow
	if err := db.QueryRow(`SELECT id,app_id,successor_deployment_id,successor_release_id,successor_slot,successor_generation,
		predecessor_deployment_id,predecessor_release_id,predecessor_slot,predecessor_generation,
		predecessor_activation_receipt_id,creator_job_id,creator_worker_attempt,candidate_roster_digest,
		predecessor_roster_digest,route_intent_digest,committed_route_proof_digest,activated_at
		FROM generated_route_activations WHERE id=?`, want.id).
		Scan(&actual.id, &actual.appID, &actual.successorDeploymentID, &actual.successorReleaseID, &actual.successorSlot, &actual.successorGeneration,
			&actual.predecessorDeploymentID, &actual.predecessorReleaseID, &actual.predecessorSlot, &actual.predecessorGeneration,
			&actual.predecessorActivationReceiptID, &actual.creatorJobID, &actual.creatorWorkerAttempt, &actual.candidateRosterDigest,
			&actual.predecessorRosterDigest, &actual.routeIntentDigest, &actual.committedRouteProofDigest, &actual.activatedAt); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("activation receipt=%#v want=%#v", actual, want)
	}
}
