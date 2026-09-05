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

func TestBeginRequiresApprovedMigrationAndIsIdempotent(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, true, false)
	input := fixture.beginInput([]string{"api"})
	if _, _, err := fixture.repository.Begin(context.Background(), input); !errors.Is(err, ErrMigrationApprovalRequired) {
		t.Fatalf("unapproved migration begin = %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,?,?)`, fixture.planID, fixture.appID, fixture.actorID, timestamp(fixture.now)); err != nil {
		t.Fatal(err)
	}
	created, wasCreated, err := fixture.repository.Begin(context.Background(), input)
	if err != nil || !wasCreated {
		t.Fatalf("begin = %#v created=%t err=%v", created, wasCreated, err)
	}
	if created.CandidateSlot != "blue" || created.PreviousActiveDeploymentID != "" || created.MigrationState != MigrationPending || len(created.Components) != 1 || created.Components[0].State != ComponentPending {
		t.Fatalf("initial state = %#v", created)
	}
	replayed, wasCreated, err := fixture.repository.Begin(context.Background(), input)
	if err != nil || wasCreated || replayed.DeploymentID != created.DeploymentID {
		t.Fatalf("replay = %#v created=%t err=%v", replayed, wasCreated, err)
	}
	input.ComponentNames = []string{"different"}
	if _, _, err := fixture.repository.Begin(context.Background(), input); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mismatched replay = %v", err)
	}
}

func TestBeginBlocksSecondGeneratedDeploymentUntilCurrentRuntimeIsTerminal(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, false, false)
	input := fixture.beginInput([]string{"api"})
	if _, created, err := fixture.repository.Begin(context.Background(), input); err != nil || !created {
		t.Fatalf("first begin created=%t err=%v", created, err)
	}
	second := input
	second.DeploymentID = uuid.NewString()
	if _, _, err := fixture.repository.Begin(context.Background(), second); !errors.Is(err, ErrDeploymentInProgress) {
		t.Fatalf("second begin = %v", err)
	}
	if _, err := fixture.repository.Advance(context.Background(), fixture.appID, fixture.deploymentID, PhasePreflight, PhaseFailed, DiagnosticInternalError); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.repository.Begin(context.Background(), second); errors.Is(err, ErrDeploymentInProgress) {
		t.Fatalf("terminal first runtime still blocked next deployment: %v", err)
	}
}

func TestMigrationAttemptCannotBeSilentlyRepeated(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, true, true)
	value, _, err := fixture.repository.Begin(context.Background(), fixture.beginInput([]string{"api"}))
	if err != nil {
		t.Fatal(err)
	}
	value = advance(t, fixture.repository, value, PhaseBuilding)
	if _, err := fixture.repository.Advance(context.Background(), fixture.appID, fixture.deploymentID, PhaseBuilding, PhaseStartingCandidate, ""); err == nil {
		t.Fatal("migration-bearing deployment skipped its migration")
	}
	value = advance(t, fixture.repository, value, PhaseMigrating)
	value, err = fixture.repository.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID)
	if err != nil || value.MigrationState != MigrationRunning || value.MigrationStartedAt.IsZero() {
		t.Fatalf("migration start = %#v err=%v", value, err)
	}
	if _, err := fixture.repository.BeginMigration(context.Background(), fixture.appID, fixture.deploymentID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("repeated migration start = %v", err)
	}
	value, err = fixture.repository.FinishMigration(context.Background(), fixture.appID, fixture.deploymentID, true)
	if err != nil || value.MigrationState != MigrationSucceeded || value.MigrationFinishedAt.IsZero() {
		t.Fatalf("migration finish = %#v err=%v", value, err)
	}
	if _, err := fixture.repository.FinishMigration(context.Background(), fixture.appID, fixture.deploymentID, true); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("repeated migration finish = %v", err)
	}
	value = advance(t, fixture.repository, value, PhaseStartingCandidate)
}

func TestComponentLifecycleAndActiveHeadCAS(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, false, false)
	value, _, err := fixture.repository.Begin(context.Background(), fixture.beginInput([]string{"api"}))
	if err != nil {
		t.Fatal(err)
	}
	artifactID := seedReadyArtifact(t, fixture, "api")
	component, err := fixture.repository.SetImageReady(context.Background(), fixture.appID, fixture.deploymentID, "api", artifactID)
	if err != nil || component.State != ComponentImageReady {
		t.Fatalf("image ready = %#v err=%v", component, err)
	}
	component, err = fixture.repository.SetContainerStarting(context.Background(), fixture.appID, fixture.deploymentID, "api", "rig-app-blue-api")
	if err != nil || component.State != ComponentStarting {
		t.Fatalf("starting = %#v err=%v", component, err)
	}
	containerID := strings.Repeat("d", 64)
	component, err = fixture.repository.SetContainerRunning(context.Background(), fixture.appID, fixture.deploymentID, "api", containerID)
	if err != nil || component.State != ComponentRunning || component.ContainerID != containerID {
		t.Fatalf("running = %#v err=%v", component, err)
	}
	component, err = fixture.repository.AdvanceComponent(context.Background(), fixture.appID, fixture.deploymentID, "api", ComponentRunning, ComponentHealthy)
	if err != nil || component.State != ComponentHealthy {
		t.Fatalf("healthy = %#v err=%v", component, err)
	}
	if _, err := fixture.repository.AdvanceComponent(context.Background(), fixture.appID, fixture.deploymentID, "api", ComponentHealthy, ComponentActive); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("component activated outside head switch: %v", err)
	}
	value = advance(t, fixture.repository, value, PhaseBuilding)
	value = advance(t, fixture.repository, value, PhaseStartingCandidate)
	value = advance(t, fixture.repository, value, PhaseWaitingHealth)
	value = advance(t, fixture.repository, value, PhaseSwitchingRoute)

	head, switched, err := fixture.repository.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0)
	if err != nil || !switched || head.DeploymentID != fixture.deploymentID || head.Slot != "blue" || head.Generation != 1 {
		t.Fatalf("active switch = %#v switched=%t err=%v", head, switched, err)
	}
	component, err = fixture.repository.Component(context.Background(), fixture.appID, fixture.deploymentID, "api")
	if err != nil || component.State != ComponentActive {
		t.Fatalf("active component = %#v err=%v", component, err)
	}
	if _, err := fixture.repository.AdvanceComponent(context.Background(), fixture.appID, fixture.deploymentID, "api", ComponentActive, ComponentDraining); err == nil {
		t.Fatal("currently routed component began draining")
	}
	if replay, switched, err := fixture.repository.SwitchActive(context.Background(), fixture.appID, fixture.deploymentID, 0); err != nil || switched || replay.Generation != 1 {
		t.Fatalf("active switch replay = %#v switched=%t err=%v", replay, switched, err)
	}
	if _, _, err := fixture.repository.SwitchActive(context.Background(), fixture.appID, uuid.NewString(), 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale active generation = %v", err)
	}
}

func TestFailureAndRecoveryQueriesUseStableCodes(t *testing.T) {
	fixture := newFixture(t, []string{"api"}, false, false)
	value, _, err := fixture.repository.Begin(context.Background(), fixture.beginInput([]string{"api"}))
	if err != nil {
		t.Fatal(err)
	}
	recoverable, err := fixture.repository.Recoverable(context.Background())
	if err != nil || len(recoverable) != 1 || recoverable[0].DeploymentID != value.DeploymentID {
		t.Fatalf("recoverable = %#v err=%v", recoverable, err)
	}
	failed, err := fixture.repository.Advance(context.Background(), fixture.appID, fixture.deploymentID, PhasePreflight, PhaseFailed, DiagnosticInsufficientReplacementCapacity)
	if err != nil || failed.Phase != PhaseFailed || failed.DiagnosticCode != DiagnosticInsufficientReplacementCapacity || failed.FinishedAt.IsZero() {
		t.Fatalf("failed = %#v err=%v", failed, err)
	}
	recoverable, err = fixture.repository.Recoverable(context.Background())
	if err != nil || len(recoverable) != 0 {
		t.Fatalf("terminal deployment remained recoverable: %#v err=%v", recoverable, err)
	}
}

type fixture struct {
	db           *sql.DB
	repository   *Repository
	appID        string
	actorID      string
	deploymentID string
	releaseID    string
	planID       string
	now          time.Time
}

func newFixture(t *testing.T, components []string, migration, approved bool) fixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := fixture{db: db, repository: New(db), appID: uuid.NewString(), actorID: uuid.NewString(), deploymentID: uuid.NewString(), releaseID: uuid.NewString(), planID: uuid.NewString(), now: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)}
	f.repository.now = func() time.Time { return f.now }
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`, f.actorID, f.actorID, "hash", timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, f.appID, f.appID, f.appID, timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	migrationDigest := ""
	if migration {
		migrationDigest = strings.Repeat("e", 64)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,?,8,?,?,?,'accepted',?,?)`,
		f.planID, f.appID, "apps/"+f.appID+"/deployment-plans/"+f.planID+".secret", strings.Repeat("a", 64),
		strings.Repeat("b", 64), strings.Repeat("c", 64), len(components), migrationDigest, f.actorID, timestamp(f.now), f.actorID, timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	if approved {
		if _, err := db.Exec(`INSERT INTO deployment_plan_migration_approvals(revision_id,app_id,approval_revision,approved_by,approved_at) VALUES(?,?,1,?,?)`, f.planID, f.appID, f.actorID, timestamp(f.now)); err != nil {
			t.Fatal(err)
		}
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
	return f
}

func (f fixture) beginInput(components []string) BeginInput {
	return BeginInput{DeploymentID: f.deploymentID, AppID: f.appID, ReleaseID: f.releaseID, DeploymentPlanRevisionID: f.planID, DeploymentPlanRevisionNumber: 1, ComponentNames: components}
}

func seedReadyArtifact(t *testing.T, f fixture, component string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,
		build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, id, f.releaseID, f.planID, 1, component, strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), timestamp(f.now), timestamp(f.now), timestamp(f.now)); err != nil {
		t.Fatal(err)
	}
	return id
}

func advance(t *testing.T, repository *Repository, value Deployment, next Phase) Deployment {
	t.Helper()
	advanced, err := repository.Advance(context.Background(), value.AppID, value.DeploymentID, value.Phase, next, "")
	if err != nil {
		t.Fatal(err)
	}
	return advanced
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
