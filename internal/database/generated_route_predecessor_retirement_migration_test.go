package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const generatedRoutePredecessorRetirementMigration = "043_generated_route_predecessor_retirement.sql"

func TestGeneratedRoutePredecessorRetirementMigrationFreshSchemaAndMirror(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + generatedRoutePredecessorRetirementMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", generatedRoutePredecessorRetirementMigration))
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(public) {
		t.Fatal("generated route predecessor retirement migration is not byte mirrored")
	}

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{
		"generated_route_predecessor_retirements",
		"generated_route_predecessor_retirement_receipts",
	} {
		assertGeneratedRoutePredecessorRetirementSchemaObject(t, db, "table", table)
	}
	for _, trigger := range []string{
		"generated_route_predecessor_retirement_valid_insert",
		"generated_route_predecessor_retirement_immutable",
		"generated_route_predecessor_retirement_retain",
		"generated_route_predecessor_retirement_receipt_valid_insert",
		"generated_route_predecessor_retirement_receipt_immutable",
		"generated_route_predecessor_retirement_receipt_retain",
	} {
		assertGeneratedRoutePredecessorRetirementSchemaObject(t, db, "trigger", trigger)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
		generatedRoutePredecessorRetirementMigration).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration record=%d error=%v", applied, err)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedRoutePredecessorRetirementMigrationGuardsComponentAndAggregate(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	predecessor, successor, activation := seedGeneratedRoutePredecessorRetirementFixture(t, db)

	assertSQLRejected(t, db, "aggregate before complete roster",
		`INSERT INTO generated_route_predecessor_retirement_receipts(
		 activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		 predecessor_roster_digest,component_count,retired_at
		 ) VALUES(?,?,?,?,?,1,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.predecessorDeploymentID, activation.predecessorRosterDigest, successor.stamp)
	assertGeneratedRoutePredecessorRetirementCounts(t, db, activation.id, 0, 0)

	other := seedGeneratedRouteActivationFixture(t, db, "other")
	assertSQLRejected(t, db, "crossed candidate receipt",
		`INSERT INTO generated_route_predecessor_retirements(
		 activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		 component_name,candidate_receipt_id,container_id,candidate_policy_digest,observed_absent_at
		 ) VALUES(?,?,?,?,?,?,?,?,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.predecessorDeploymentID, "api", other.intentID, strings.Repeat("1", 64),
		strings.Repeat("d", 64), successor.stamp)
	assertSQLRejected(t, db, "missing predecessor component",
		`INSERT INTO generated_route_predecessor_retirements(
		 activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		 component_name,candidate_receipt_id,container_id,candidate_policy_digest,observed_absent_at
		 ) VALUES(?,?,?,?,?,?,?,?,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.predecessorDeploymentID, "missing", predecessor.intentID, strings.Repeat("1", 64),
		strings.Repeat("d", 64), successor.stamp)
	assertGeneratedRoutePredecessorRetirementCounts(t, db, activation.id, 0, 0)

	if _, err := db.Exec(`INSERT INTO generated_route_predecessor_retirements(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		component_name,candidate_receipt_id,container_id,candidate_policy_digest,observed_absent_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.predecessorDeploymentID, "api", predecessor.intentID, strings.Repeat("1", 64),
		strings.Repeat("d", 64), successor.stamp); err != nil {
		t.Fatal(err)
	}
	assertSQLRejected(t, db, "crossed aggregate roster digest",
		`INSERT INTO generated_route_predecessor_retirement_receipts(
		 activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		 predecessor_roster_digest,component_count,retired_at
		 ) VALUES(?,?,?,?,?,1,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.predecessorDeploymentID, strings.Repeat("9", 64), successor.stamp)
	if _, err := db.Exec(`INSERT INTO generated_route_predecessor_retirement_receipts(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		predecessor_roster_digest,component_count,retired_at
		) VALUES(?,?,?,?,?,1,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		activation.predecessorDeploymentID, activation.predecessorRosterDigest, successor.stamp); err != nil {
		t.Fatal(err)
	}
	assertGeneratedRoutePredecessorRetirementCounts(t, db, activation.id, 1, 1)

	assertSQLRejected(t, db, "component observation update",
		`UPDATE generated_route_predecessor_retirements SET observed_absent_at=?
		 WHERE activation_receipt_id=? AND component_name='api'`, "changed", activation.id)
	assertSQLRejected(t, db, "component observation delete",
		`DELETE FROM generated_route_predecessor_retirements
		 WHERE activation_receipt_id=? AND component_name='api'`, activation.id)
	assertSQLRejected(t, db, "aggregate receipt update",
		`UPDATE generated_route_predecessor_retirement_receipts SET retired_at=?
		 WHERE activation_receipt_id=?`, "changed", activation.id)
	assertSQLRejected(t, db, "aggregate receipt delete",
		`DELETE FROM generated_route_predecessor_retirement_receipts WHERE activation_receipt_id=?`, activation.id)
	assertGeneratedRoutePredecessorRetirementCounts(t, db, activation.id, 1, 1)
	assertNoForeignKeyViolations(t, db)
}

func TestGeneratedRoutePredecessorRetirementMigrationAcceptsExactZeroRosterReceipt(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	fixture := seedGeneratedRouteActivationFixture(t, db, "primary")
	activation := fixture.activation()
	if err := insertGeneratedRouteActivation(db, activation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO generated_route_predecessor_retirement_receipts(
		activation_receipt_id,app_id,successor_deployment_id,predecessor_deployment_id,
		predecessor_roster_digest,component_count,retired_at
		) VALUES(?,?,?,?,?,0,?)`, activation.id, activation.appID, activation.successorDeploymentID,
		"", activation.predecessorRosterDigest, fixture.stamp); err != nil {
		t.Fatal(err)
	}
	assertGeneratedRoutePredecessorRetirementCounts(t, db, activation.id, 0, 1)
	assertSQLRejected(t, db, "zero-roster receipt update",
		`UPDATE generated_route_predecessor_retirement_receipts SET retired_at=?
		 WHERE activation_receipt_id=?`, "changed", activation.id)
	assertSQLRejected(t, db, "zero-roster receipt delete",
		`DELETE FROM generated_route_predecessor_retirement_receipts WHERE activation_receipt_id=?`, activation.id)
	assertGeneratedRoutePredecessorRetirementCounts(t, db, activation.id, 0, 1)
	assertNoForeignKeyViolations(t, db)
}

func seedGeneratedRoutePredecessorRetirementFixture(t *testing.T, db *sql.DB) (
	generatedRouteActivationFixture,
	generatedRouteActivationFixture,
	generatedRouteActivationRow,
) {
	t.Helper()
	predecessor := seedGeneratedRouteActivationFixture(t, db, "primary")
	predecessorActivation := predecessor.activation()
	if err := insertGeneratedRouteActivation(db, predecessorActivation); err != nil {
		t.Fatal(err)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=?,activation_receipt_id=?
		WHERE app_id=?`, predecessor.deploymentID, predecessor.releaseID, predecessor.stamp,
		predecessorActivation.id, predecessor.appID)
	mustExec(`UPDATE generated_runtime_components SET state='active',updated_at=?
		WHERE deployment_id=? AND component_name='api'`, predecessor.stamp, predecessor.deploymentID)
	mustExec(`UPDATE jobs SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=?
		WHERE id=?`, predecessor.stamp, predecessor.stamp, predecessor.jobID)

	successor := generatedRouteActivationFixture{
		appID:        predecessor.appID,
		planID:       predecessor.planID,
		configID:     predecessor.configID,
		releaseID:    predecessor.releaseID,
		jobID:        "route-retirement-successor-job",
		deploymentID: "route-retirement-successor-deployment",
		artifactID:   predecessor.artifactID,
		intentID:     "50000000-0000-4000-8000-000000000002",
		activationID: "50000000-0000-4000-8000-000000000001",
		stamp:        predecessor.stamp,
	}
	mustExec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,started_at,updated_at
		) VALUES(?,'deploy','application',?,'running','running',1,?,'{}',?,?,?)`, successor.jobID,
		successor.appID, "route-activation-primary-owner", successor.stamp, successor.stamp, successor.stamp)
	mustExec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,started_at,configuration_mode,actual_configuration_revision_id,
		actual_configuration_revision_number,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,?,?, 'applying',?,'current',?,1,1,'generated_node',?,1)`, successor.deploymentID,
		successor.appID, successor.releaseID, successor.jobID, successor.stamp, successor.configID, successor.planID)
	mustExec(`INSERT INTO generated_runtime_deployments(
		deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		candidate_slot,previous_active_deployment_id,previous_active_slot,phase,migration_state,created_at,updated_at
		) VALUES(?,?,?,?,1,'green',?, 'blue','preflight','not_required',?,?)`, successor.deploymentID,
		successor.appID, successor.releaseID, successor.planID, predecessor.deploymentID, successor.stamp, successor.stamp)
	mustExec(`INSERT INTO generated_runtime_components(
		deployment_id,component_name,slot,image_artifact_id,state,created_at,updated_at
		) VALUES(?,'api','green',?,'image_ready',?,?)`, successor.deploymentID, successor.artifactID,
		successor.stamp, successor.stamp)
	mustExec(`UPDATE generated_runtime_deployments SET phase='building',updated_at=? WHERE deployment_id=?`,
		successor.stamp, successor.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='starting_candidate',updated_at=? WHERE deployment_id=?`,
		successor.stamp, successor.deploymentID)
	mustExec(`INSERT INTO generated_candidate_intents(
		id,deployment_id,component_name,creator_job_id,creator_worker_attempt,
		configuration_revision_id,configuration_revision_number,image_artifact_id,image_artifact_attempt,
		environment_operation_id,environment_staging_attempt,candidate_policy_version,candidate_policy_digest,created_at
		) VALUES(?,?, 'api', ?,1, ?,1, ?,1, ?,1,'generated-candidate-v1',?,?)`, successor.intentID,
		successor.deploymentID, successor.jobID, successor.configID, successor.artifactID, successor.artifactID,
		strings.Repeat("e", 64), successor.stamp)
	mustExec(`UPDATE generated_runtime_components
		SET state='starting',container_name='rig-route-retirement-successor-api',updated_at=?
		WHERE deployment_id=? AND component_name='api'`, successor.stamp, successor.deploymentID)
	mustExec(`UPDATE generated_runtime_components SET state='running',container_id=?,updated_at=?
		WHERE deployment_id=? AND component_name='api'`, strings.Repeat("2", 64), successor.stamp, successor.deploymentID)
	mustExec(`UPDATE generated_runtime_components SET state='healthy',updated_at=?
		WHERE deployment_id=? AND component_name='api'`, successor.stamp, successor.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='waiting_health',updated_at=? WHERE deployment_id=?`,
		successor.stamp, successor.deploymentID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='switching_route',updated_at=? WHERE deployment_id=?`,
		successor.stamp, successor.deploymentID)
	mustExec(`UPDATE deployments SET status='waiting_health' WHERE id=?`, successor.deploymentID)

	activation := generatedRouteActivationRow{
		id:                             successor.activationID,
		appID:                          successor.appID,
		successorDeploymentID:          successor.deploymentID,
		successorReleaseID:             successor.releaseID,
		successorSlot:                  "green",
		successorGeneration:            2,
		predecessorDeploymentID:        predecessor.deploymentID,
		predecessorReleaseID:           predecessor.releaseID,
		predecessorSlot:                "blue",
		predecessorGeneration:          1,
		predecessorActivationReceiptID: predecessorActivation.id,
		creatorJobID:                   successor.jobID,
		creatorWorkerAttempt:           1,
		candidateRosterDigest:          strings.Repeat("6", 64),
		predecessorRosterDigest:        strings.Repeat("7", 64),
		routeIntentDigest:              strings.Repeat("8", 64),
		committedRouteProofDigest:      strings.Repeat("9", 64),
		activatedAt:                    successor.stamp,
	}
	if err := insertGeneratedRouteActivation(db, activation); err != nil {
		t.Fatal(err)
	}
	mustExec(`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='green',generation=2,updated_at=?,activation_receipt_id=?
		WHERE app_id=?`, successor.deploymentID, successor.releaseID, successor.stamp, activation.id, successor.appID)
	mustExec(`UPDATE generated_runtime_deployments SET phase='draining',updated_at=? WHERE deployment_id=?`,
		successor.stamp, successor.deploymentID)
	mustExec(`UPDATE generated_runtime_components SET state='stopped',updated_at=?,finished_at=?
		WHERE deployment_id=? AND component_name='api'`, predecessor.stamp, predecessor.stamp, predecessor.deploymentID)
	return predecessor, successor, activation
}

func assertGeneratedRoutePredecessorRetirementSchemaObject(t *testing.T, db *sql.DB, kind, name string) {
	t.Helper()
	var found int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, kind, name).Scan(&found); err != nil || found != 1 {
		t.Fatalf("%s %q found=%d error=%v", kind, name, found, err)
	}
}

func assertGeneratedRoutePredecessorRetirementCounts(t *testing.T, db *sql.DB, receiptID string, components, aggregates int) {
	t.Helper()
	var actualComponents, actualAggregates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirements
		WHERE activation_receipt_id=?`, receiptID).Scan(&actualComponents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_route_predecessor_retirement_receipts
		WHERE activation_receipt_id=?`, receiptID).Scan(&actualAggregates); err != nil {
		t.Fatal(err)
	}
	if actualComponents != components || actualAggregates != aggregates {
		t.Fatalf("retirement rows components=%d aggregates=%d", actualComponents, actualAggregates)
	}
}
