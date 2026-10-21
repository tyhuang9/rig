package generatedingress

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// candidateTestRepository exposes only the concrete test database alongside
// the real repository API. Production ownership still flows through the
// sealed GeneratedOwnerStores bundle.
type candidateTestRepository struct {
	*deployments.Repository
	db     *sql.DB
	stores *deployments.GeneratedOwnerStores
}

func (r *candidateTestRepository) GeneratedCandidateAuthority(
	process runtimeprocess.CommandRunner,
	environment GeneratedCandidateEnvironmentStager,
	budget *generatedcapacity.Budget,
	options GeneratedCandidateProcessOptions,
) (*GeneratedCandidateAuthority, error) {
	if r == nil {
		return nil, deploymentexecution.ErrInvalidConfiguration
	}
	return NewGeneratedCandidateAuthority(r.stores, process, environment, budget, options)
}

func (r *candidateTestRepository) candidateAuthority() *GeneratedCandidateAuthority {
	if r == nil || r.stores == nil || r.stores.Repository() == nil {
		return nil
	}
	generatedCandidateAuthorityRegistries.Lock()
	registry := generatedCandidateAuthorityRegistries.values[r.stores.Repository()]
	generatedCandidateAuthorityRegistries.Unlock()
	if registry == nil {
		return nil
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.authority
}

type candidateTestGuarded struct {
	context context.Context
	appID   string
	actorID string
}

type candidateMigrationRequestFixture struct {
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ArtifactID                   string
	ArtifactAttemptNumber        int64
}

type generatedMigrationFixture struct {
	repository    *candidateTestRepository
	configuration *appconfig.Store
	guarded       *candidateTestGuarded
	request       candidateMigrationRequestFixture
	plan          deploymentplans.Plan
	claim         deploymentexecution.Claim
	binding       deploymentbinding.Binding
}

func newGeneratedMigrationFixture(t *testing.T) *generatedMigrationFixture {
	t.Helper()
	return newGeneratedMigrationFixtureWithComponents(t, []string{"api"}, []string{"api"})
}

func newGeneratedMigrationFixtureWithComponents(t *testing.T, planComponentNames, runtimeComponentNames []string) *generatedMigrationFixture {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newGeneratedMigrationFixtureOnDatabaseWithComponents(t, db, planComponentNames, runtimeComponentNames)
}

func newGeneratedMigrationFixtureOnDatabaseWithComponents(t *testing.T, db *sql.DB, planComponentNames, runtimeComponentNames []string) *generatedMigrationFixture {
	t.Helper()
	if db == nil {
		t.Fatal("generated migration fixture requires a database")
	}
	actorID := uuid.NewString()
	appID := uuid.NewString()
	machineID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, "owner", "hash", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machineID, "local", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?, 'draft',?,?)`, appID, appID, appID, machineID, now, now); err != nil {
		t.Fatal(err)
	}

	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release deployment effects scope: %v", err)
		}
	})
	guarded := &candidateTestGuarded{context: deploymenteffects.WithScope(context.Background(), scope), appID: appID, actorID: actorID}

	plans, err := deploymentplans.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "runCommand", "internalPort", "healthProbe"}
	components := make([]deploymentplans.Component, 0, len(planComponentNames))
	provenance := make([]deploymentplans.FieldProvenance, 0, len(planComponentNames)*len(fields))
	configurationComponents := make([]appconfig.ComponentTarget, 0, len(planComponentNames))
	for _, name := range planComponentNames {
		root := "apps/" + name
		components = append(components, deploymentplans.Component{Name: name, Role: "server", RootDirectory: root, PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: root, NodeVersion: "24", RunCommand: "npm start", InternalPort: 3000, HealthProbe: "/health"})
		configurationComponents = append(configurationComponents, appconfig.ComponentTarget{Name: name, Role: "server"})
		for _, field := range fields {
			provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components." + name + "." + field, Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
		}
	}
	plan := deploymentplans.Plan{
		Strategy:        deploymentplans.StrategyGeneratedNode,
		Detector:        deploymentplans.Detector{Name: "package-json", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:          deploymentplans.SourceIdentity{Provider: "github", RepositoryID: 7, ResolvedDigest: strings.Repeat("c", 64)},
		Components:      components,
		FieldProvenance: provenance,
		Migration:       &deploymentplans.Migration{ComponentName: "api", RootDirectory: "apps/api", Command: "npm run migrate -- --locked", EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: strings.Repeat("b", 64), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}},
	}
	revision, err := plans.Replace(context.Background(), appID, actorID, deploymentplans.ReplaceInput{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := plans.ApproveMigration(context.Background(), appID, revision.ID, revision.RevisionNumber, 0, actorID); err != nil {
		t.Fatal(err)
	}
	secret := "postgres://application-owned"
	configurationRevision, err := configuration.ReplaceScoped(context.Background(), appID, actorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: revision.ID, PlanRevisionNumber: revision.RevisionNumber,
		Components: configurationComponents,
		Entries:    []appconfig.ScopedValueInput{{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseMigration, Component: "api", Key: "DATABASE_URL"}, Sensitivity: appconfig.SensitivitySecret, Value: &secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "generated-candidate-" + fmt.Sprint(time.Now().UnixNano()), RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create deployment job: created=%t err=%v", created.Created, err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=2,started_at=?,updated_at=? WHERE id=?`, now, now, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	releaseID := uuid.NewString()
	workspace := strings.Repeat("f", 64)
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'github',7,?,'ready',?,?,?,?,?)`, releaseID, appID, now, strings.Repeat("c", 64), workspace, configurationRevision.RevisionID, configurationRevision.RevisionNumber, revision.ID, revision.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	testRepository := &candidateTestRepository{Repository: repository, db: db, stores: stores}
	deployment, _, err := repository.GetOrCreateByJob(context.Background(), appID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = repository.InitializeRuntime(context.Background(), appID, deployment.ID, releaseID, configurationRevision.RevisionID, configurationRevision.RevisionNumber, deployments.RuntimeGeneratedNode, revision.ID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, appID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Gate(guarded.context, deployments.GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatal(err)
	}
	state := generatedruntimestate.New(db)
	runtimeDeployment, wasCreated, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: appID, ReleaseID: releaseID, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber, ComponentNames: append([]string(nil), runtimeComponentNames...)})
	if err != nil || !wasCreated {
		t.Fatalf("begin generated runtime: created=%t err=%v", wasCreated, err)
	}
	runtimeDeployment, err = state.Advance(context.Background(), appID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatal(err)
	}
	artifactID := ""
	buildDigest := strings.Repeat("d", 64)
	imageID := "sha256:" + strings.Repeat("e", 64)
	for _, name := range runtimeComponentNames {
		componentArtifactID := uuid.NewString()
		if _, err := db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,'test-compiler',?,3,?,'ready',?,?,?)`, componentArtifactID, releaseID, revision.ID, revision.RevisionNumber, name, buildDigest, imageID, now, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := state.SetImageReady(context.Background(), appID, deployment.ID, name, componentArtifactID); err != nil {
			t.Fatal(err)
		}
		if name == "api" {
			artifactID = componentArtifactID
		}
	}
	if artifactID == "" {
		t.Fatal("runtime fixture requires the candidate component api")
	}
	if _, err := state.Advance(context.Background(), appID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseMigrating, ""); err != nil {
		t.Fatal(err)
	}
	return &generatedMigrationFixture{
		repository: testRepository, configuration: configuration, guarded: guarded, plan: plan, claim: claim, binding: created.Binding,
		request: candidateMigrationRequestFixture{
			DeploymentID: deployment.ID, ReleaseID: releaseID,
			ConfigurationRevisionID: configurationRevision.RevisionID, ConfigurationRevisionNumber: configurationRevision.RevisionNumber,
			DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber,
			ArtifactID: artifactID, ArtifactAttemptNumber: 3,
		},
	}
}

func findingFingerprint(policyVersion, capability, scope string) string {
	canonical, _ := json.Marshal(struct {
		PolicyVersion string `json:"policyVersion"`
		Capability    string `json:"capability"`
		Scope         string `json:"scope"`
	}{policyVersion, capability, scope})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func TestGeneratedCandidateExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_GENERATED_CANDIDATE_EXIT") == "1" {
		os.Exit(1)
	}
}

func generatedMigrationCompletedExitOne(t *testing.T) *exec.ExitError {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestGeneratedCandidateExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_GENERATED_CANDIDATE_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected completed exit one, got %T %v", err, err)
	}
	return exited
}
