package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

type candidateValidationFixture struct {
	migration *generatedMigrationFixture
	authority *GeneratedCandidateAuthority
	state     *generatedruntimestate.Repository
	request   GeneratedCandidateRequest
	pins      candidateValidationPins
	protected candidateProtectedSnapshot
}

func newCandidateValidationFixture(t *testing.T) *candidateValidationFixture {
	t.Helper()
	migration := newGeneratedMigrationFixture(t)
	state := generatedruntimestate.New(migration.repository.db)
	if _, err := state.BeginMigration(context.Background(), migration.claim.ApplicationID(), migration.request.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.FinishMigration(context.Background(), migration.claim.ApplicationID(), migration.request.DeploymentID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Advance(context.Background(), migration.claim.ApplicationID(), migration.request.DeploymentID,
		generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
		t.Fatal(err)
	}
	request := GeneratedCandidateRequest{
		Claim: migration.claim, DeploymentID: migration.request.DeploymentID, ReleaseID: migration.request.ReleaseID,
		ConfigurationRevisionID:      migration.request.ConfigurationRevisionID,
		ConfigurationRevisionNumber:  migration.request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID:     migration.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
		CandidateSlot:                "blue",
	}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	authority := &GeneratedCandidateAuthority{repo: migration.repository}
	protected, err := authority.loadCandidateProtectedSnapshot(context.Background(), pins)
	if err != nil {
		t.Fatal(err)
	}
	return &candidateValidationFixture{
		migration: migration, authority: authority, state: state,
		request: request, pins: pins, protected: protected,
	}
}

func (f *candidateValidationFixture) read(t *testing.T) candidateAdmissionSnapshot {
	t.Helper()
	tx, err := f.migration.repository.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value, err := readCandidateAdmissionSnapshotTx(context.Background(), tx, f.pins, f.protected)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *candidateValidationFixture) validateAdmission(t *testing.T, baseline candidateAdmissionSnapshot) (candidateAdmissionSnapshot, error) {
	t.Helper()
	tx, err := f.migration.repository.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value, validationErr := validateCandidateAdmissionSnapshotTx(context.Background(), tx, f.pins, f.protected, baseline)
	if validationErr == nil {
		validationErr = tx.Commit()
	}
	return value, validationErr
}

func (f *candidateValidationFixture) validateExecution(t *testing.T, baseline candidateAdmissionSnapshot) (candidateAdmissionSnapshot, error) {
	t.Helper()
	tx, err := f.migration.repository.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value, validationErr := validateCandidateExecutionSnapshotTx(context.Background(), tx, f.pins, f.protected, baseline)
	if validationErr == nil {
		validationErr = tx.Commit()
	}
	return value, validationErr
}

func (f *candidateValidationFixture) beginReceipt(t *testing.T) string {
	t.Helper()
	receiptID := uuid.NewString()
	tx, err := f.migration.repository.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = f.state.BeginCandidateTx(context.Background(), tx, generatedruntimestate.CandidateIntentInput{
		ReceiptID: receiptID, AppID: f.pins.claim.ApplicationID(), DeploymentID: f.pins.deploymentID,
		ComponentName: "api", ContainerName: "rig-candidate-api", CreatorJobID: f.pins.claim.JobID(),
		CreatorWorkerAttempt: f.pins.claim.Attempt(), ConfigurationRevisionID: f.pins.configurationRevisionID,
		ConfigurationRevisionNumber: f.pins.configurationRevisionNumber,
		ImageArtifactID:             f.migration.request.ArtifactID, ImageArtifactAttempt: f.migration.request.ArtifactAttemptNumber,
		EnvironmentOperationID: f.migration.request.ArtifactID, EnvironmentStagingAttempt: f.pins.claim.Attempt(),
		CandidatePolicyVersion: "candidate-v1", CandidatePolicyDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return receiptID
}

func TestCandidateValidationPinsCopyFindingsAndProtectedRoster(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	finding := Finding{PolicyVersion: "v1", Capability: "network", Scope: "component:api", Disposition: "allowed"}
	finding.Fingerprint = findingFingerprint(finding.PolicyVersion, finding.Capability, finding.Scope)
	request := fixture.request
	request.Findings = []Finding{finding}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Findings[0].Scope = "changed"
	if pins.findings[0].Scope != finding.Scope {
		t.Fatal("candidate pins retained caller-owned findings")
	}
	protected, err := fixture.authority.loadCandidateProtectedSnapshot(context.Background(), pins)
	if err != nil {
		t.Fatal(err)
	}
	if len(protected.components) != 1 || protected.components[0].Name != "api" ||
		protected.components[0].RunCommand != "npm start" || protected.revision.CanonicalDigest == "" {
		t.Fatalf("protected roster = %#v", protected.components)
	}
	invalid := fixture.request
	invalid.CandidateSlot = "caller-selected"
	if _, err := candidatePinsFromRequest(invalid); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("invalid slot error = %v", err)
	}
}

func TestCandidateAdmissionAllowsReadySelectionAfterPendingBaseline(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	db := fixture.migration.repository.db
	if _, err := db.Exec(`DROP TRIGGER generated_runtime_component_runtime_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER generated_runtime_component_state_transition`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generated_runtime_components SET state='pending',image_artifact_id=NULL
		WHERE deployment_id=? AND component_name='api'`, fixture.pins.deploymentID); err != nil {
		t.Fatal(err)
	}
	baseline := fixture.read(t)
	if baseline.components[0].selectedArtifactID != "" || baseline.components[0].state != generatedruntimestate.ComponentPending {
		t.Fatalf("pending baseline = %#v", baseline.components[0])
	}
	if _, err := db.Exec(`UPDATE generated_runtime_components SET state='image_ready',image_artifact_id=?
		WHERE deployment_id=? AND component_name='api'`, fixture.migration.request.ArtifactID, fixture.pins.deploymentID); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.validateAdmission(t, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if current.components[0].selectedArtifactID != fixture.migration.request.ArtifactID ||
		current.components[0].artifact == nil || current.components[0].risky {
		t.Fatalf("selected admission = %#v", current.components[0])
	}
	if _, err := fixture.validateExecution(t, baseline); err != nil {
		t.Fatalf("first execution pin rejected newly selected Ready artifact: %v", err)
	}
}

func TestCandidateExecutionAcceptsReceiptBackedStartingAndPinsArtifact(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	baseline := fixture.read(t)
	if _, err := fixture.validateExecution(t, baseline); err != nil {
		t.Fatalf("image-ready execution: %v", err)
	}
	receiptID := fixture.beginReceipt(t)
	current, err := fixture.validateExecution(t, baseline)
	if err != nil {
		t.Fatal(err)
	}
	component := current.components[0]
	if !component.risky || component.receipt == nil || component.receipt.id != receiptID ||
		component.artifact == nil || component.artifact.id != fixture.migration.request.ArtifactID ||
		component.state != generatedruntimestate.ComponentStarting {
		t.Fatalf("starting component = %#v", component)
	}
}

func TestCandidateExecutionPinsExactReadyArtifactAfterFirstExecutionValidation(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	admission := fixture.read(t)
	pinned, err := fixture.validateExecution(t, admission)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.executionPinned || pinned.components[0].artifact == nil {
		t.Fatalf("execution baseline was not pinned: %#v", pinned.components[0])
	}
	db := fixture.migration.repository.db
	if _, err := db.Exec(`DROP TRIGGER generated_image_artifact_provenance_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER generated_image_artifact_transition`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generated_image_artifacts SET build_definition_digest=? WHERE id=?`,
		strings.Repeat("9", 64), fixture.migration.request.ArtifactID); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.validateExecution(t, pinned)
	if !errors.Is(err, deploymentexecution.ErrAuthorityStale) || current.components[0].artifact == nil {
		t.Fatalf("artifact drift current=%#v error=%v", current, err)
	}
}

func TestCandidateAdmissionAllowsReleaseOriginalConfigurationToDifferFromSelectedConfiguration(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	db := fixture.migration.repository.db
	originalID := uuid.NewString()
	originalNumber := fixture.migration.request.ConfigurationRevisionNumber + 100
	if _, err := db.Exec(`INSERT INTO application_configuration_revisions(
		id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
	) SELECT ?,app_id,?,?,created_by,created_at,variable_count,secret_count,
		bundle_version,deployment_plan_revision_id,deployment_plan_revision_number
		FROM application_configuration_revisions WHERE id=?`, originalID, originalNumber,
		"candidate-release-original:"+originalID, fixture.pins.configurationRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER release_configuration_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE releases SET configuration_revision_id=?,configuration_revision_number=? WHERE id=?`,
		originalID, originalNumber, fixture.pins.releaseID); err != nil {
		t.Fatal(err)
	}
	baseline := fixture.read(t)
	if baseline.release.configurationRevisionID != originalID ||
		baseline.main.configurationRevisionID != fixture.pins.configurationRevisionID ||
		!baseline.release.configurationValid {
		t.Fatalf("release/selected configuration snapshot = %#v / %#v", baseline.release, baseline.main)
	}
	if _, err := fixture.validateAdmission(t, baseline); err != nil {
		t.Fatalf("valid release-original configuration was coupled to selected configuration: %v", err)
	}
}

func TestCandidateValidationRejectsInconsistentMainAndMigrationStages(t *testing.T) {
	t.Run("main status", func(t *testing.T) {
		fixture := newCandidateValidationFixture(t)
		baseline := fixture.read(t)
		if _, err := fixture.migration.repository.db.Exec(`UPDATE deployments SET status='waiting_health' WHERE id=?`, fixture.pins.deploymentID); err != nil {
			t.Fatal(err)
		}
		current, err := fixture.validateAdmission(t, baseline)
		if !errors.Is(err, deploymentexecution.ErrStageInvalid) || len(current.components) != 1 {
			t.Fatalf("main/runtime stage mismatch current=%#v error=%v", current, err)
		}
	})
	t.Run("migration state", func(t *testing.T) {
		fixture := newCandidateValidationFixture(t)
		baseline := fixture.read(t)
		db := fixture.migration.repository.db
		if _, err := db.Exec(`DROP TRIGGER generated_runtime_migration_transition`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE generated_runtime_deployments
			SET migration_state='not_required',migration_started_at=NULL,migration_finished_at=NULL
			WHERE deployment_id=?`, fixture.pins.deploymentID); err != nil {
			t.Fatal(err)
		}
		current, err := fixture.validateAdmission(t, baseline)
		if !errors.Is(err, deploymentexecution.ErrStageInvalid) || len(current.components) != 1 {
			t.Fatalf("plan/migration stage mismatch current=%#v error=%v", current, err)
		}
	})
}

func TestCandidateRawSnapshotSurfacesReceiptRiskBeforeStrictMismatch(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	baseline := fixture.read(t)
	receiptID := fixture.beginReceipt(t)
	db := fixture.migration.repository.db
	if _, err := db.Exec(`DROP TRIGGER generated_candidate_intent_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE generated_candidate_intents SET image_artifact_attempt=image_artifact_attempt+1 WHERE id=?`, receiptID); err != nil {
		t.Fatal(err)
	}
	raw := fixture.read(t)
	if raw.components[0].receipt == nil || raw.components[0].receipt.id != receiptID || !raw.components[0].risky {
		t.Fatalf("raw risk was not surfaced: %#v", raw.components[0])
	}
	current, err := fixture.validateExecution(t, baseline)
	if !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("receipt mismatch error = %v", err)
	}
	if current.components[0].receipt == nil || current.components[0].receipt.id != receiptID || !current.components[0].risky {
		t.Fatalf("strict refusal omitted raw risk: %#v", current.components[0])
	}
}

func TestCandidateAdmissionReturnsRiskBeforePolicyRefusal(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	receiptID := fixture.beginReceipt(t)
	finding := Finding{PolicyVersion: "v1", Capability: "external_bind", Scope: "component:api", Disposition: "rejected"}
	finding.Fingerprint = findingFingerprint(finding.PolicyVersion, finding.Capability, finding.Scope)
	if _, err := fixture.migration.repository.db.Exec(`INSERT INTO deployment_policy_findings(
		id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), fixture.pins.deploymentID, finding.PolicyVersion,
		finding.Capability, finding.Scope, finding.Fingerprint, finding.Disposition,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	request := fixture.request
	request.Findings = []Finding{finding}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.pins = pins
	baseline := fixture.read(t)
	current, err := fixture.validateAdmission(t, baseline)
	if !errors.Is(err, ErrRejectedCapability) {
		t.Fatalf("policy refusal = %v", err)
	}
	if current.components[0].receipt == nil || current.components[0].receipt.id != receiptID || !current.components[0].risky {
		t.Fatalf("policy refusal omitted risk: %#v", current.components[0])
	}
}

func TestCandidateValidationRejectsConfigurationSourceAndRosterDrift(t *testing.T) {
	t.Run("configuration", func(t *testing.T) {
		fixture := newCandidateValidationFixture(t)
		baseline := fixture.read(t)
		db := fixture.migration.repository.db
		if _, err := db.Exec(`DROP TRIGGER configuration_revision_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE application_configuration_revisions SET bundle_version=1 WHERE id=?`, fixture.pins.configurationRevisionID); err != nil {
			t.Fatal(err)
		}
		current, err := fixture.validateAdmission(t, baseline)
		if !errors.Is(err, ErrInvalidDeployment) || len(current.components) != 1 {
			t.Fatalf("configuration drift current=%#v error=%v", current, err)
		}
	})
	t.Run("release source", func(t *testing.T) {
		fixture := newCandidateValidationFixture(t)
		baseline := fixture.read(t)
		if _, err := fixture.migration.repository.db.Exec(`UPDATE releases SET workspace_state='unavailable' WHERE id=?`, fixture.pins.releaseID); err != nil {
			t.Fatal(err)
		}
		current, err := fixture.validateAdmission(t, baseline)
		if !errors.Is(err, ErrInvalidDeployment) || len(current.components) != 1 {
			t.Fatalf("source drift current=%#v error=%v", current, err)
		}
	})
	t.Run("protected roster", func(t *testing.T) {
		fixture := newCandidateValidationFixture(t)
		if _, err := fixture.migration.repository.db.Exec(`DROP TRIGGER generated_runtime_component_no_delete`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.migration.repository.db.Exec(`DELETE FROM generated_runtime_components WHERE deployment_id=?`, fixture.pins.deploymentID); err != nil {
			t.Fatal(err)
		}
		tx, err := fixture.migration.repository.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		value, err := readCandidateAdmissionSnapshotTx(context.Background(), tx, fixture.pins, fixture.protected)
		if !errors.Is(err, ErrInvalidDeployment) || len(value.components) != 0 {
			t.Fatalf("roster drift value=%#v error=%v", value, err)
		}
	})
}

func TestCandidateExecutionRequiresExactPersistedPolicyAndStage(t *testing.T) {
	fixture := newCandidateValidationFixture(t)
	baseline := fixture.read(t)
	finding := Finding{PolicyVersion: "v1", Capability: "network", Scope: "component:api", Disposition: "allowed"}
	finding.Fingerprint = findingFingerprint(finding.PolicyVersion, finding.Capability, finding.Scope)
	request := fixture.request
	request.Findings = []Finding{finding}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.pins = pins
	if current, err := fixture.validateExecution(t, baseline); !errors.Is(err, deploymentexecution.ErrAuthorityStale) && !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("changed request current=%#v error=%v", current, err)
	}
	fixture.pins, err = candidatePinsFromRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Advance(context.Background(), fixture.pins.claim.ApplicationID(), fixture.pins.deploymentID,
		generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseFailed,
		generatedruntimestate.DiagnosticInternalError); err != nil {
		t.Fatal(err)
	}
	current, err := fixture.validateExecution(t, baseline)
	if !errors.Is(err, deploymentexecution.ErrStageInvalid) || len(current.components) != 1 {
		t.Fatalf("terminal stage current=%#v error=%v", current, err)
	}
}
