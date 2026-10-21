package generatedruntime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

func newConfiguredRuntimeDeployments(t *testing.T, workingDirectory string) *deployments.Repository {
	t.Helper()
	_, repository, _, _, _ := configuredRuntimeDeployments(t, workingDirectory)
	return repository
}

func configuredRuntimeDeployments(t *testing.T, workingDirectory string) (*sql.DB, *deployments.Repository, *deploymentbinding.Repository, *deploymentplans.Store, *appconfig.Store) {
	t.Helper()
	db, err := databasefixture.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lifecycle := applifecycle.New(db)
	bindingValidator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, bindingValidator)
	if err != nil {
		t.Fatal(err)
	}
	positiveFence, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positiveFence, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	protectedRoot, err := os.MkdirTemp("", "rig-runtime-protected-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(protectedRoot); err != nil {
			t.Errorf("remove generated runtime protected fixture: %v", err)
		}
	})
	plans, err := deploymentplans.New(db, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	migrationDockerConfig := filepath.Join(t.TempDir(), "migration-docker-config")
	if err := os.Mkdir(migrationDockerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	migrationEnvironment := &runtimeFakeEnvironment{path: filepath.Join(t.TempDir(), "migration.env")}
	migrationProcess := &runtimeFakeRunner{run: func(runtimeprocess.CommandRequest) runtimeRequestResult {
		return runtimeRequestResult{}
	}}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedMigration(
		db, guard, plans, configuration, migrationEnvironment, migrationProcess,
		deployments.GeneratedMigrationProcessOptions{
			DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerConfigDirectory: migrationDockerConfig,
			WorkingDirectory: workingDirectory, Timeout: time.Second, OutputLimit: 1024,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return db, repository, bindings, plans, configuration
}

type runtimeNetworkAuthority struct {
	db         *sql.DB
	repository *deployments.Repository
	context    context.Context
	release    func() error
	scope      deploymenteffects.Scope
	spec       CandidateSpec
}

func newRuntimeNetworkAuthority(t *testing.T, workingDirectory string, spec CandidateSpec) runtimeNetworkAuthority {
	return newRuntimeNetworkAuthorityWithConfiguration(t, workingDirectory, spec, nil)
}

// newRuntimeNetworkAuthorityWithConfiguration supplies an exact scoped
// configuration through the protected configuration store. A nil entries slice
// keeps the standard candidate fixture configuration; a non-nil slice lets a
// boundary test provide its own real protected runtime entries.
func newRuntimeNetworkAuthorityWithConfiguration(t *testing.T, workingDirectory string, spec CandidateSpec, entries []appconfig.ScopedValueInput) runtimeNetworkAuthority {
	t.Helper()
	db, repository, bindings, plans, configuration := configuredRuntimeDeployments(t, workingDirectory)
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	actorID, machineID := uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{actorID, "runtime-owner", "hash", stamp, stamp}},
		{`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, []any{machineID, "local", stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,'draft',?,?)`, []any{spec.AppID, spec.AppID, spec.AppID, machineID, stamp, stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	created, err := jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: spec.AppID, IdempotencyKey: "generated-network-authority", RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create generated network job: created=%t error=%v", created.Created, err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.Read(context.Background(), created.Job.ID); err != nil {
		t.Fatalf("read generated network binding: %v", err)
	}
	component := runtimePlanComponent(spec)
	spec.Technology = component.Technology
	plan, err := plans.Replace(context.Background(), spec.AppID, actorID, deploymentplans.ReplaceInput{Plan: deploymentplans.Plan{
		SetupVersion: projectanalysis.SetupVersion, Strategy: deploymentplans.StrategyGeneratedNode,
		Detector:        deploymentplans.Detector{Name: "manual-setup", Version: "1", SourceStructuralFingerprint: strings.Repeat("1", 64)},
		Source:          deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: strings.Repeat("2", 64)},
		Components:      []deploymentplans.Component{component},
		FieldProvenance: runtimePlanProvenance(component),
	}})
	if err != nil {
		t.Fatalf("write accepted protected plan: %v", err)
	}
	loadedPlan, err := plans.GetRevision(context.Background(), spec.AppID, plan.ID, plan.RevisionNumber)
	if err != nil {
		t.Fatalf("read accepted protected plan: %v", err)
	}
	if loadedPlan.ID != plan.ID || loadedPlan.RevisionNumber != plan.RevisionNumber || loadedPlan.State != deploymentplans.RevisionAccepted {
		t.Fatalf("accepted protected plan identity=%+v", loadedPlan)
	}
	configurationInput := appconfig.ScopedReplaceInput{
		PlanRevisionID: plan.ID, PlanRevisionNumber: plan.RevisionNumber,
		Components: []appconfig.ComponentTarget{{Name: component.Name, Role: component.Role}},
	}
	if entries != nil {
		configurationInput.Entries = append([]appconfig.ScopedValueInput(nil), entries...)
	} else if len(spec.Environment) != 0 {
		value := "value"
		configurationInput.Entries = []appconfig.ScopedValueInput{{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: component.Name, Key: "SECRET"},
			Sensitivity: appconfig.SensitivitySecret, Value: &value,
		}}
	}
	configurationRevision, err := configuration.ReplaceScoped(context.Background(), spec.AppID, actorID, configurationInput)
	if err != nil {
		t.Fatalf("write scoped protected configuration: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`, spec.ReleaseID, spec.AppID, stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), configurationRevision.RevisionID, configurationRevision.RevisionNumber, plan.ID, plan.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	state := generatedruntimestate.New(db)
	deployment, _, err := repository.GetOrCreateByJob(context.Background(), spec.AppID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = repository.InitializeRuntime(context.Background(), spec.AppID, deployment.ID, spec.ReleaseID,
		configurationRevision.RevisionID, configurationRevision.RevisionNumber, deployments.RuntimeGeneratedNode, plan.ID, plan.RevisionNumber)
	if err != nil {
		t.Fatalf("initialize generated deployment provenance: %v", err)
	}
	if _, err := repository.Transition(context.Background(), spec.AppID, deployment.ID, deployments.Applying, ""); err != nil {
		t.Fatalf("enter applying deployment state: %v", err)
	}
	spec.DeploymentID = deployment.ID
	spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber = configurationRevision.RevisionID, configurationRevision.RevisionNumber
	spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber = plan.ID, plan.RevisionNumber
	if spec.ActiveSlot != "" {
		seedRuntimeActivePredecessor(t, db, state, spec, stamp)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, spec.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release generated runtime scope: %v", err)
		}
	})
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	if err := repository.Gate(ctx, deployments.GateRequest{Claim: claim, DeploymentID: spec.DeploymentID}); err != nil {
		t.Fatalf("gate generated deployment: %v", err)
	}
	runtimeDeployment, createdRuntime, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: spec.DeploymentID, AppID: spec.AppID, ReleaseID: spec.ReleaseID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
		ComponentNames: []string{spec.ComponentName},
	})
	if err != nil || !createdRuntime {
		t.Fatalf("begin generated runtime: created=%t error=%v", createdRuntime, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
		runtimeDeployment, err = state.Advance(context.Background(), spec.AppID, spec.DeploymentID, runtimeDeployment.Phase, next, "")
		if err != nil {
			t.Fatalf("advance generated runtime to %q: %v", next, err)
		}
	}
	artifacts := generatedimagestate.NewRepository(db)
	artifact, _, err := artifacts.Begin(context.Background(), generatedimagestate.BeginArtifactInput{
		ReleaseID: spec.ReleaseID, DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
		ComponentID: component.Name, CompilerVersion: "runtime-test", BuildDefinitionDigest: spec.BuildDefinitionDigest,
	})
	if err != nil {
		t.Fatalf("begin ready generated image artifact: %v", err)
	}
	artifact, err = artifacts.Complete(context.Background(), artifact.ID, spec.ImageContentID)
	if err != nil {
		t.Fatalf("complete generated image artifact: %v", err)
	}
	if _, err := state.SetImageReady(context.Background(), spec.AppID, spec.DeploymentID, component.Name, artifact.ID); err != nil {
		t.Fatalf("record ready generated image artifact: %v", err)
	}
	if _, err := plans.GetRevision(context.Background(), spec.AppID, plan.ID, plan.RevisionNumber); err != nil {
		t.Fatalf("read accepted protected plan after runtime fixture setup: %v", err)
	}
	spec.ArtifactID = artifact.ID
	spec.Network = deployments.GeneratedNetworkRequest{
		Claim: claim, DeploymentID: spec.DeploymentID, ReleaseID: spec.ReleaseID,
		ConfigurationRevisionID: spec.ConfigurationRevisionID, ConfigurationRevisionNumber: spec.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
	}
	return runtimeNetworkAuthority{db: db, repository: repository, context: ctx, release: release, scope: scope, spec: spec}
}

func runtimePlanComponent(spec CandidateSpec) deploymentplans.Component {
	technology := spec.Technology
	if technology == "" {
		technology = "node"
	}
	return deploymentplans.Component{
		Name: spec.ComponentName, Role: spec.Role, RootDirectory: spec.RootDirectory,
		PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: spec.RootDirectory,
		NodeVersion: "20", BuildCommand: "npm run build", RunCommand: spec.RunCommand,
		InternalPort: spec.InternalPort, HealthProbe: spec.HealthProbe, Technology: technology,
	}
}

func runtimePlanProvenance(component deploymentplans.Component) []deploymentplans.FieldProvenance {
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe", "technology", "staticOutputDirectory"}
	result := make([]deploymentplans.FieldProvenance, 0, len(fields))
	for _, field := range fields {
		result = append(result, deploymentplans.FieldProvenance{Field: "components." + component.Name + "." + field, Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
	}
	return result
}

func seedRuntimeActivePredecessor(t *testing.T, db *sql.DB, state *generatedruntimestate.Repository, spec CandidateSpec, stamp string) {
	t.Helper()
	if spec.ActiveSlot != SlotBlue {
		t.Fatalf("unsupported active predecessor slot %q", spec.ActiveSlot)
	}
	priorID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,status,started_at,finished_at,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,'succeeded',?,?,'current',?,?,1,'generated_node',?,?)`, priorID, spec.AppID, spec.ReleaseID, stamp, stamp, spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	value, created, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: priorID, AppID: spec.AppID, ReleaseID: spec.ReleaseID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
		ComponentNames: []string{spec.ComponentName},
	})
	if err != nil || !created {
		t.Fatalf("begin generated predecessor: created=%t error=%v", created, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
		value, err = state.Advance(context.Background(), spec.AppID, priorID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	artifactID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, spec.ReleaseID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber, spec.ComponentName, strings.Repeat("6", 64), "sha256:"+strings.Repeat("7", 64), stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	component, err := state.SetImageReady(context.Background(), spec.AppID, priorID, spec.ComponentName, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	component, err = state.SetContainerStarting(context.Background(), spec.AppID, priorID, spec.ComponentName, containerName(spec.AppID, spec.ComponentName, SlotBlue))
	if err != nil {
		t.Fatal(err)
	}
	component, err = state.SetContainerRunning(context.Background(), spec.AppID, priorID, spec.ComponentName, strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.AdvanceComponent(context.Background(), spec.AppID, priorID, spec.ComponentName, component.State, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute} {
		value, err = state.Advance(context.Background(), spec.AppID, priorID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, switched, err := state.SwitchActive(context.Background(), spec.AppID, priorID, 0); err != nil || !switched {
		t.Fatalf("switch generated predecessor: switched=%t error=%v", switched, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded} {
		value, err = state.Advance(context.Background(), spec.AppID, priorID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
}
