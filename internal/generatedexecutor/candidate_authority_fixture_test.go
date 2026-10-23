package generatedexecutor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/hostd/hostd/internal/generatedingress"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

// sealedExecutorFixture is the only successful candidate test harness in this
// package. The repository, plan/config stores, claim, artifacts, capacity and
// Docker observations are real. The wrappers below can refuse an operation,
// but they cannot manufacture a successful reservation or candidate handle.
type sealedExecutorFixture struct {
	db                *sql.DB
	app               apps.Application
	actorID           string
	machineID         string
	job               jobs.Job
	claim             deploymentexecution.Claim
	context           context.Context
	plan              deploymentplans.DeploymentPlanRevision
	configuration     appconfig.Configuration
	configurationView *sealedConfigurationExporter
	release           releasesnapshot.Release
	artifact          generatedimage.Artifact
	artifacts         []generatedimage.Artifact
	artifactStore     *generatedimage.ArtifactRepository
	bindings          *deploymentbinding.Repository
	plans             *deploymentplans.Store
	configurations    *appconfig.Store
	releases          *fakeReleases

	repository    *deployments.Repository
	domain        *generatedingress.GeneratedDomain
	deployments   *sealedDeploymentStore
	stateStore    *generatedruntimestate.Repository
	state         *sealedRuntimeState
	engine        *generatedruntime.Engine
	runtime       *sealedRuntimeEngine
	authorization *sealedAuthorizationGate
	compiler      *fakeCompiler
	routes        *fakeRoutes
	migrations    *fakeMigrations
	reporter      *fakeReporter
	runner        *sealedCandidateRunner
	environment   *sealedCandidateEnvironment
	capacity      *generateddomain.Runner
	executor      *Executor
}

func newSealedExecutorFixture(t *testing.T) *sealedExecutorFixture {
	return newSealedExecutorFixtureWithTechnology(t, "node")
}

func newSealedExecutorFixtureWithTechnology(t *testing.T, technology string) *sealedExecutorFixture {
	component := deploymentplans.Component{
		Name: "api", Role: generatedruntime.RoleServer, Technology: technology, RootDirectory: ".",
		PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "22",
		BuildCommand: "npm run build", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health",
	}
	return newSealedExecutorFixtureWithComponents(t, []deploymentplans.Component{component})
}

func newSealedExecutorFixtureWithComponents(t *testing.T, components []deploymentplans.Component) *sealedExecutorFixture {
	return newSealedExecutorFixtureWithComponentsAndMigration(t, components, false)
}

func newSealedExecutorFixtureWithMigration(t *testing.T) *sealedExecutorFixture {
	component := deploymentplans.Component{
		Name: "api", Role: generatedruntime.RoleServer, Technology: "node", RootDirectory: ".",
		PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "22",
		BuildCommand: "npm run build", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health",
	}
	return newSealedExecutorFixtureWithComponentsAndMigration(t, []deploymentplans.Component{component}, true)
}

func newSealedExecutorFixtureWithComponentsAndMigration(t *testing.T, components []deploymentplans.Component, migration bool) *sealedExecutorFixture {
	t.Helper()
	if len(components) == 0 {
		t.Fatal("candidate fixture needs a component")
	}
	dataRoot := generateddomain.NewRetainedRoot(t)
	prepareExecutorDomainDatabase(t, dataRoot)
	runner := newSealedCandidateRunner(t)
	environment := &sealedCandidateEnvironment{root: filepath.Join(dataRoot, "candidate-environments")}
	if err := os.Mkdir(environment.root, 0o700); err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	engineOptions := generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(dataRoot, "docker"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Minute,
		HealthTimeout: time.Minute, HealthPollInterval: 100 * time.Millisecond, OutputLimit: 16 << 10,
		Limits:               generatedruntime.ContainerLimits{MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 64, TmpfsBytes: 8 << 20, LogSize: "1m", LogFiles: 2},
		ReplacementDiskBytes: 128 << 20,
	}
	domainFixture := newExecutorDomainFixture(t, dataRoot, runner, environment, engineOptions)
	db, bindings, plans, configurations := domainFixture.db, domainFixture.bindings, domainFixture.plans, domainFixture.configuration
	repository := domainFixture.repository

	actorID, machineID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'candidate-owner','hash',?,?)`, actorID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,'candidate-local','local','ready','test','test','test','test',?,?)`, machineID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"name":"candidate-fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	applications := apps.New(db)
	app, err := applications.Create("Candidate fixture", "", workspace, machineID)
	if err != nil {
		t.Fatal(err)
	}

	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe"}
	provenance := make([]deploymentplans.FieldProvenance, 0, (len(fields)+2)*len(components))
	for _, component := range components {
		componentFields := fields
		if component.Technology != "" {
			componentFields = append(append([]string(nil), fields...), "technology", "staticOutputDirectory")
		}
		for _, field := range componentFields {
			provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components." + component.Name + "." + field, Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
		}
	}
	planValue := deploymentplans.Plan{
		Strategy:   deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "package-json", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: strings.Repeat("b", 64)},
		Components: append([]deploymentplans.Component(nil), components...), FieldProvenance: provenance,
	}
	for _, component := range components {
		if component.Technology != "" {
			planValue.SetupVersion = projectanalysis.SetupVersion
			planValue.Detector.Name = "manual-setup"
			break
		}
	}
	if migration {
		planValue.Migration = &deploymentplans.Migration{
			ComponentName: components[0].Name, RootDirectory: ".", Command: "npm run migrate",
			EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: strings.Repeat("9", 64),
			Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending},
		}
	}
	plan, err := plans.Replace(context.Background(), app.ID, actorID, deploymentplans.ReplaceInput{Plan: planValue})
	if err != nil {
		t.Fatal(err)
	}
	if migration {
		if err := plans.ApproveMigration(context.Background(), app.ID, plan.ID, plan.RevisionNumber, 0, actorID); err != nil {
			t.Fatal(err)
		}
		plan, err = plans.GetRevision(context.Background(), app.ID, plan.ID, plan.RevisionNumber)
		if err != nil {
			t.Fatal(err)
		}
	}
	secret := "candidate-runtime-secret"
	targets := make([]appconfig.ComponentTarget, 0, len(components))
	entries := make([]appconfig.ScopedValueInput, 0, len(components))
	for _, component := range components {
		targets = append(targets, appconfig.ComponentTarget{Name: component.Name, Role: component.Role})
		if component.Role == generatedruntime.RoleStatic {
			continue
		}
		value := secret + "-" + component.Name
		entries = append(entries, appconfig.ScopedValueInput{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: component.Name, Key: "OWNER_SECRET"},
			Sensitivity: appconfig.SensitivitySecret, Value: &value,
		})
	}
	if migration {
		migrationSecret := "postgres://candidate-migration"
		entries = append(entries, appconfig.ScopedValueInput{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseMigration, Component: components[0].Name, Key: "DATABASE_URL"},
			Sensitivity: appconfig.SensitivitySecret, Value: &migrationSecret,
		})
	}
	configuration, err := configurations.ReplaceScoped(context.Background(), app.ID, actorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: plan.ID, PlanRevisionNumber: plan.RevisionNumber,
		Components: targets, Entries: entries,
	})
	if err != nil {
		t.Fatal(err)
	}

	jobID, releaseID := uuid.NewString(), uuid.NewString()
	release := releasesnapshot.Release{
		ID: releaseID, AppID: app.ID, SourceProvider: "local", ResolvedSHA: strings.Repeat("b", 64),
		WorkspaceTreeSHA256: strings.Repeat("c", 64), ConfigurationRevisionID: configuration.RevisionID,
		ConfigurationRevisionNumber: configuration.RevisionNumber, DeploymentPlanRevisionID: plan.ID,
		DeploymentPlanRevisionNumber: plan.RevisionNumber,
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`,
		release.ID, app.ID, stamp, release.ResolvedSHA, release.WorkspaceTreeSHA256, configuration.RevisionID, configuration.RevisionNumber, plan.ID, plan.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(jobs.DeploymentInput{
		ConfigurationMode:      jobs.ConfigurationCurrent,
		ExpectedPlanRevisionID: plan.ID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
		ExpectedConfigurationRevisionID: configuration.RevisionID, ExpectedConfigurationRevisionNumber: configuration.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',0,?,?,?,?)`, jobID, app.ID, actorID, string(input), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindings.InsertNewTx(context.Background(), tx, jobID, now)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, jobID); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, jobID, app.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	scope, releaseScope, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := releaseScope(); err != nil {
			t.Errorf("release candidate fixture scope: %v", err)
		}
	})
	guarded := deploymenteffects.WithScope(context.Background(), scope)

	artifactStore := generatedimage.NewArtifactRepository(db)
	artifacts := make([]generatedimage.Artifact, 0, len(components))
	for index, component := range components {
		artifact, created, err := artifactStore.Begin(context.Background(), generatedimage.BeginArtifactInput{
			ReleaseID: release.ID, DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
			ComponentID: component.Name, CompilerVersion: "sealed-executor/test", BuildDefinitionDigest: strings.Repeat(string(rune('d'+index)), 64),
		})
		if err != nil || !created {
			t.Fatalf("begin %s candidate artifact=%+v created=%t err=%v", component.Name, artifact, created, err)
		}
		artifact, err = artifactStore.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat(string(rune('e'+index)), 64))
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
		runner.addImage(sealedCandidateImage{appID: app.ID, releaseID: release.ID, planID: plan.ID, artifact: artifact, component: component})
	}
	artifact := artifacts[0]

	capacity := domainFixture.process
	capacity.SetCapacity(512<<20, 512<<20)
	engine, err := generatedruntime.NewEngine(domainFixture.process, environment, domainFixture.domain.Candidates(), engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	stateStore := generatedruntimestate.New(db)
	deploymentStore := &sealedDeploymentStore{repository: repository}
	state := &sealedRuntimeState{repository: stateStore}
	runtime := &sealedRuntimeEngine{engine: engine}
	authorizationGate, err := NewAuthorizationGate(repository, plans, engine)
	if err != nil {
		t.Fatal(err)
	}
	authorization := &sealedAuthorizationGate{delegate: authorizationGate}
	events := []string{}
	compiler := &fakeCompiler{events: &events, artifacts: artifacts, repository: repository, release: release, plan: plan}
	routes := &fakeRoutes{events: &events, delegate: generatedRouteController{authority: domainFixture.domain.Routes()}}
	migrations := &fakeMigrations{events: &events}
	if migration {
		migrations.delegate = &executorMigrationRunner{repository: repository}
	}
	configurationView := &sealedConfigurationExporter{store: configurations}
	releaseView := &fakeReleases{release: release}
	executor, err := newExecutor(applications, releaseView, configurationView, deploymentStore, plans,
		compiler, artifactStore, state, runtime, authorization, routes, migrations, Options{DrainPeriod: time.Millisecond, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: app.ID, RequestedBy: actorID, Attempt: 1, Input: input}
	return &sealedExecutorFixture{
		db: db, app: app, actorID: actorID, machineID: machineID, job: job, claim: claim, context: guarded,
		plan: plan, configuration: configuration, configurationView: configurationView, release: release, artifact: artifact, artifacts: artifacts,
		artifactStore: artifactStore, bindings: bindings,
		plans: plans, configurations: configurations, releases: releaseView,
		repository: repository, domain: domainFixture.domain, deployments: deploymentStore, stateStore: stateStore, state: state,
		engine: engine, runtime: runtime, authorization: authorization, compiler: compiler,
		routes: routes, migrations: migrations, reporter: &fakeReporter{}, runner: runner,
		environment: environment, capacity: capacity, executor: executor,
	}
}

type sealedConfigurationExporter struct {
	store    *appconfig.Store
	mismatch bool
}

func (s *sealedConfigurationExporter) RevisionIdentity(ctx context.Context, appID string) (appconfig.RevisionIdentity, error) {
	return s.store.RevisionIdentity(ctx, appID)
}
func (s *sealedConfigurationExporter) ExactRevisionIdentity(ctx context.Context, appID, revisionID string, revisionNumber int64) (appconfig.RevisionIdentity, error) {
	value, err := s.store.ExactRevisionIdentity(ctx, appID, revisionID, revisionNumber)
	if s.mismatch {
		value.RevisionNumber++
	}
	return value, err
}
func (s *sealedConfigurationExporter) ExportComponentRuntimeForExecution(ctx context.Context, appID, revisionID string, revisionNumber int64, operationID string, attempt int64, component string) (appconfig.ExecutionConfiguration, error) {
	value, err := s.store.ExportComponentRuntimeForExecution(ctx, appID, revisionID, revisionNumber, operationID, attempt, component)
	if s.mismatch {
		value.RevisionNumber++
	}
	return value, err
}

func (f *sealedExecutorFixture) execute() (jobs.ExecutionResult, error) {
	return f.executor.ExecuteBound(f.context, f.job, f.claim, f.reporter)
}

// capacityCompetitor creates an independently bound deployment in the same
// repository and engine, so admission uses the identical raw host budget.
// Its initial successful reservation proves the request itself is valid.
func (f *sealedExecutorFixture) capacityCompetitor(t *testing.T) func(bool) {
	t.Helper()
	other := *f
	var err error
	other.app, err = apps.New(f.db).Create("Capacity competitor", "", f.app.Source.Path, f.machineID)
	if err != nil {
		t.Fatal(err)
	}
	planValue := f.plan.Plan
	component := planValue.Components[0]
	planValue.Components = []deploymentplans.Component{component}
	planValue.Migration = nil
	planValue.FieldProvenance = nil
	for _, field := range f.plan.Plan.FieldProvenance {
		if !strings.HasPrefix(field.Field, "components.") || strings.HasPrefix(field.Field, "components."+component.Name+".") {
			planValue.FieldProvenance = append(planValue.FieldProvenance, field)
		}
	}
	other.plan, err = f.plans.Replace(f.context, other.app.ID, f.actorID, deploymentplans.ReplaceInput{Plan: planValue})
	if err != nil {
		t.Fatal(err)
	}
	other.configuration, err = f.configurations.ReplaceScoped(f.context, other.app.ID, f.actorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: other.plan.ID, PlanRevisionNumber: other.plan.RevisionNumber,
		Components: []appconfig.ComponentTarget{{Name: component.Name, Role: component.Role}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	other.release = f.release
	other.release.ID, other.release.AppID = uuid.NewString(), other.app.ID
	other.release.ConfigurationRevisionID, other.release.ConfigurationRevisionNumber = other.configuration.RevisionID, other.configuration.RevisionNumber
	other.release.DeploymentPlanRevisionID, other.release.DeploymentPlanRevisionNumber = other.plan.ID, other.plan.RevisionNumber
	if _, err := f.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`,
		other.release.ID, other.app.ID, stamp, other.release.ResolvedSHA, other.release.WorkspaceTreeSHA256, other.configuration.RevisionID, other.configuration.RevisionNumber, other.plan.ID, other.plan.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(jobs.DeploymentInput{
		ConfigurationMode:      jobs.ConfigurationCurrent,
		ExpectedPlanRevisionID: other.plan.ID, ExpectedPlanRevisionNumber: other.plan.RevisionNumber,
		ExpectedConfigurationRevisionID: other.configuration.RevisionID, ExpectedConfigurationRevisionNumber: other.configuration.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	other.job = jobs.Job{ID: uuid.NewString(), Type: "deploy", ResourceType: "application", ResourceID: other.app.ID, RequestedBy: f.actorID, Attempt: 1, Input: input}
	if _, err := f.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',0,?,?,?,?)`, other.job.ID, other.app.ID, f.actorID, string(input), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(f.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.bindings.InsertNewTx(f.context, tx, other.job.ID, now)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, other.job.ID); err != nil {
		t.Fatal(err)
	}
	other.claim, err = deploymentexecution.NewClaim(binding, other.job.ID, other.app.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, authorization := other.prepareAuthorization(t)
	request := generatedingress.GeneratedReplacementRequest{
		Claim: other.claim, DeploymentID: authorization.DeploymentID, ReleaseID: other.release.ID,
		ConfigurationRevisionID: other.configuration.RevisionID, ConfigurationRevisionNumber: other.configuration.RevisionNumber,
		DeploymentPlanRevisionID: other.plan.ID, DeploymentPlanRevisionNumber: other.plan.RevisionNumber,
		CandidateSlot: string(authorization.CandidateSlot),
	}
	check := func(wantRetained bool) {
		t.Helper()
		reservation, reserveErr := f.engine.ReserveReplacement(f.context, request)
		if reservation != nil {
			reservation.Release()
		}
		if wantRetained {
			if reservation != nil || !errors.Is(reserveErr, generatedcapacity.ErrInsufficientCapacity) {
				t.Fatalf("retained capacity admitted a distinct deployment: reservation=%v err=%v", reservation, reserveErr)
			}
		} else if reservation == nil || reserveErr != nil {
			t.Fatalf("unused capacity was not available to a distinct deployment: reservation=%v err=%v", reservation, reserveErr)
		}
	}
	check(false)
	return check
}

// beginReplacement records a real serving predecessor before installing the
// next bound deployment. The returned check observes both durable and physical
// predecessor state after a replacement failure.
func (f *sealedExecutorFixture) beginReplacement(t *testing.T) func() {
	t.Helper()
	if result, err := f.execute(); err != nil || result.CompletionCode != "deployment_completed" {
		t.Fatalf("first deployment result=%+v err=%v", result, err)
	}
	beforeHead, err := f.stateStore.Active(f.context, f.app.ID)
	if err != nil || beforeHead.DeploymentID == "" {
		t.Fatalf("first active head=%+v err=%v", beforeHead, err)
	}
	beforeRuntime := f.runtimeDeployment(t)
	if len(beforeRuntime.Components) != 1 || beforeRuntime.Components[0].State != generatedruntimestate.ComponentActive {
		t.Fatalf("first serving runtime=%+v", beforeRuntime)
	}
	containerID := beforeRuntime.Components[0].ContainerID
	f.runner.mu.Lock()
	container := f.runner.containers[containerID]
	if container == nil || !container.exists || !container.running {
		f.runner.mu.Unlock()
		t.Fatalf("first serving container unavailable: %s", containerID)
	}
	beforeContainer, err := json.Marshal(container.inspection())
	f.runner.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	f.nextDeployment(t)
	f.routes.requests = nil
	return func() {
		t.Helper()
		afterHead, headErr := f.stateStore.Active(f.context, f.app.ID)
		afterRuntime, runtimeErr := f.stateStore.Get(f.context, f.app.ID, beforeHead.DeploymentID)
		if headErr != nil || runtimeErr != nil || !reflect.DeepEqual(afterHead, beforeHead) || !reflect.DeepEqual(afterRuntime, beforeRuntime) {
			t.Fatalf("serving predecessor changed: head=%+v runtime=%+v headErr=%v runtimeErr=%v", afterHead, afterRuntime, headErr, runtimeErr)
		}
		f.runner.mu.Lock()
		container := f.runner.containers[containerID]
		var afterContainer []byte
		if container != nil && container.exists && container.running {
			afterContainer, err = json.Marshal(container.inspection())
		}
		f.runner.mu.Unlock()
		if err != nil || string(afterContainer) != string(beforeContainer) {
			t.Fatalf("serving predecessor container changed: before=%s after=%s err=%v", beforeContainer, afterContainer, err)
		}
	}
}

func (f *sealedExecutorFixture) prepareAuthorization(t *testing.T) (deployments.Deployment, AuthorizationRequest) {
	t.Helper()
	deployment, _, err := f.repository.GetOrCreateByJob(f.context, f.app.ID, f.job.ID, string(jobs.ConfigurationCurrent))
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = f.repository.InitializeRuntime(f.context, f.app.ID, deployment.ID, f.release.ID,
		f.configuration.RevisionID, f.configuration.RevisionNumber, deployments.RuntimeGeneratedNode, f.plan.ID, f.plan.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	componentNames := make([]string, 0, len(f.plan.Plan.Components))
	for _, component := range f.plan.Plan.Components {
		componentNames = append(componentNames, component.Name)
	}
	runtimeDeployment, _, err := f.stateStore.Begin(f.context, generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: f.app.ID, ReleaseID: f.release.ID,
		DeploymentPlanRevisionID: f.plan.ID, DeploymentPlanRevisionNumber: f.plan.RevisionNumber,
		ComponentNames: componentNames,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtimeDeployment.Phase == generatedruntimestate.PhasePreflight {
		if _, err := f.stateStore.Advance(f.context, f.app.ID, deployment.ID, generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, ""); err != nil {
			t.Fatal(err)
		}
	}
	return deployment, AuthorizationRequest{
		Claim: f.claim, AppID: f.app.ID, DeploymentID: deployment.ID, ReleaseID: f.release.ID,
		ConfigurationRevisionID: f.configuration.RevisionID, ConfigurationRevisionNumber: f.configuration.RevisionNumber,
		DeploymentPlanRevisionID: f.plan.ID, DeploymentPlanRevisionNumber: f.plan.RevisionNumber,
		CandidateSlot: generatedruntime.SlotBlue, ComponentCount: len(componentNames),
	}
}

type sealedCandidateSeed uint8

const (
	sealedSeedReceiptOnly sealedCandidateSeed = iota + 1
	sealedSeedCreated
	sealedSeedRunning
	sealedSeedAttemptUnknown
)

// seedCandidate drives the same protected image, network, receipt and Docker
// APIs as the executor, stopping at the requested recoverable boundary.
func (f *sealedExecutorFixture) seedCandidate(t *testing.T, seed sealedCandidateSeed) {
	t.Helper()
	deployment, request := f.prepareAuthorization(t)
	reservation, err := f.authorization.delegate.Authorize(f.context, request)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Release()
	artifact, err := f.compiler.Compile(f.context, generatedimage.CompileRequest{
		Claim: f.claim, DeploymentID: deployment.ID, ReleaseID: f.release.ID,
		ConfigurationRevisionID: f.configuration.RevisionID, ConfigurationRevisionNumber: f.configuration.RevisionNumber,
		DeploymentPlanRevisionID: f.plan.ID, DeploymentPlanRevisionNumber: f.plan.RevisionNumber,
		ComponentName: "api",
	})
	if err != nil || artifact.ID != f.artifact.ID {
		t.Fatalf("seed candidate artifact=%+v err=%v", artifact, err)
	}
	network := deployments.GeneratedNetworkRequest{
		Claim: f.claim, DeploymentID: deployment.ID, ReleaseID: f.release.ID,
		ConfigurationRevisionID: f.configuration.RevisionID, ConfigurationRevisionNumber: f.configuration.RevisionNumber,
		DeploymentPlanRevisionID: f.plan.ID, DeploymentPlanRevisionNumber: f.plan.RevisionNumber,
	}
	if err := f.engine.EnsureAppNetwork(f.context, network); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.FinishGeneratedImagePreparation(f.context, network); err != nil {
		t.Fatal(err)
	}
	execution, err := f.engine.PinCandidates(f.context, generatedingress.GeneratedCandidateRequest{
		Claim: f.claim, DeploymentID: deployment.ID, ReleaseID: f.release.ID,
		ConfigurationRevisionID: f.configuration.RevisionID, ConfigurationRevisionNumber: f.configuration.RevisionNumber,
		DeploymentPlanRevisionID: f.plan.ID, DeploymentPlanRevisionNumber: f.plan.RevisionNumber,
		CandidateSlot: string(generatedruntime.SlotBlue),
	}, reservation)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	invocation, err := execution.BeginCreate("api")
	if err != nil {
		t.Fatal(err)
	}
	if seed == sealedSeedReceiptOnly {
		return
	}
	if seed == sealedSeedAttemptUnknown {
		f.runner.createErrs = append(f.runner.createErrs, runtimeprocess.ErrTerminationFailed)
		if _, err := execution.CreateAndRecord(invocation); err == nil {
			t.Fatal("unknown candidate create unexpectedly succeeded")
		}
		return
	}
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if seed == sealedSeedRunning {
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *sealedExecutorFixture) deployment(t *testing.T) deployments.Deployment {
	t.Helper()
	values, err := f.repository.List(context.Background(), f.app.ID, 10)
	if err != nil {
		t.Fatalf("candidate deployments=%+v err=%v", values, err)
	}
	for _, value := range values {
		if value.JobID == f.job.ID {
			return value
		}
	}
	t.Fatalf("deployment for job %s is unavailable: %+v", f.job.ID, values)
	return deployments.Deployment{}
}

func (f *sealedExecutorFixture) runtimeDeployment(t *testing.T) generatedruntimestate.Deployment {
	t.Helper()
	deployment := f.deployment(t)
	value, err := f.stateStore.Get(context.Background(), f.app.ID, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// nextDeployment installs a second immutable release and a newly bound worker
// attempt. The first deployment must have reached success through the owner;
// this helper never writes candidate or runtime completion facts directly.
func (f *sealedExecutorFixture) nextDeployment(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, f.job.ID); err != nil {
		t.Fatal(err)
	}
	jobID, releaseID := uuid.NewString(), uuid.NewString()
	release := f.release
	release.ID = releaseID
	if _, err := f.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`,
		release.ID, f.app.ID, stamp, release.ResolvedSHA, release.WorkspaceTreeSHA256, f.configuration.RevisionID, f.configuration.RevisionNumber, f.plan.ID, f.plan.RevisionNumber); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(jobs.DeploymentInput{
		ConfigurationMode:      jobs.ConfigurationCurrent,
		ExpectedPlanRevisionID: f.plan.ID, ExpectedPlanRevisionNumber: f.plan.RevisionNumber,
		ExpectedConfigurationRevisionID: f.configuration.RevisionID, ExpectedConfigurationRevisionNumber: f.configuration.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',0,?,?,?,?)`, jobID, f.app.ID, f.actorID, string(input), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.bindings.InsertNewTx(context.Background(), tx, jobID, now)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, jobID); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, jobID, f.app.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	artifact, created, err := f.artifactStore.Begin(context.Background(), generatedimage.BeginArtifactInput{
		ReleaseID: release.ID, DeploymentPlanRevisionID: f.plan.ID, DeploymentPlanRevisionNumber: f.plan.RevisionNumber,
		ComponentID: "api", CompilerVersion: "sealed-executor/test", BuildDefinitionDigest: strings.Repeat("f", 64),
	})
	if err != nil || !created {
		t.Fatalf("begin next artifact=%+v created=%t err=%v", artifact, created, err)
	}
	artifact, err = f.artifactStore.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	f.runner.addImage(sealedCandidateImage{appID: f.app.ID, releaseID: release.ID, planID: f.plan.ID, artifact: artifact, component: f.plan.Plan.Components[0]})
	f.job = jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: f.app.ID, RequestedBy: f.actorID, Attempt: 1, Input: input}
	f.claim, f.release, f.artifact = claim, release, artifact
	f.releases.release = release
	f.compiler.artifacts = []generatedimage.Artifact{artifact}
	f.compiler.release = release
	f.authorization.calls = 0
	f.runtime.pinCalls = 0
}

type sealedDeploymentStore struct {
	repository          *deployments.Repository
	getErr              error
	transitionErr       error
	transitionErrStatus deployments.Status
	finishErr           error
	finishRequests      []deployments.GeneratedNetworkRequest
	readFindings        [][]deployments.Finding
}

func (s *sealedDeploymentStore) GetOrCreateByJob(ctx context.Context, appID, jobID, mode string) (deployments.Deployment, bool, error) {
	value, created, err := s.repository.GetOrCreateByJob(ctx, appID, jobID, mode)
	s.readFindings = append(s.readFindings, value.Findings)
	return value, created, err
}
func (s *sealedDeploymentStore) Get(ctx context.Context, appID, deploymentID string) (deployments.Deployment, error) {
	if s.getErr != nil {
		return deployments.Deployment{}, s.getErr
	}
	value, err := s.repository.Get(ctx, appID, deploymentID)
	s.readFindings = append(s.readFindings, value.Findings)
	return value, err
}
func (s *sealedDeploymentStore) InitializeRuntime(ctx context.Context, appID, deploymentID, releaseID, configurationID string, configurationNumber int64, strategy deployments.RuntimeStrategy, planID string, planNumber int64) (deployments.Deployment, error) {
	return s.repository.InitializeRuntime(ctx, appID, deploymentID, releaseID, configurationID, configurationNumber, strategy, planID, planNumber)
}
func (s *sealedDeploymentStore) Transition(ctx context.Context, appID, deploymentID string, status deployments.Status, diagnostic string) (deployments.Deployment, error) {
	if s.transitionErr != nil && status == s.transitionErrStatus {
		return deployments.Deployment{}, s.transitionErr
	}
	return s.repository.Transition(ctx, appID, deploymentID, status, diagnostic)
}
func (s *sealedDeploymentStore) FinishGeneratedImagePreparation(ctx context.Context, request deployments.GeneratedNetworkRequest) (generatedruntimestate.Phase, error) {
	s.finishRequests = append(s.finishRequests, request)
	if s.finishErr != nil {
		return "", s.finishErr
	}
	return s.repository.FinishGeneratedImagePreparation(ctx, request)
}
func (s *sealedDeploymentStore) PinGeneratedMigration(ctx context.Context, request deployments.GeneratedMigrationRequest) (*deployments.GeneratedMigrationExecution, error) {
	return s.repository.PinGeneratedMigration(ctx, request)
}

type sealedRuntimeState struct {
	repository     *generatedruntimestate.Repository
	beginErr       error
	getErr         error
	advanceErrTo   generatedruntimestate.Phase
	advanceErr     error
	activeOverride *generatedruntimestate.ActiveHead
}

func (s *sealedRuntimeState) Begin(ctx context.Context, input generatedruntimestate.BeginInput) (generatedruntimestate.Deployment, bool, error) {
	if s.beginErr != nil {
		return generatedruntimestate.Deployment{}, false, s.beginErr
	}
	return s.repository.Begin(ctx, input)
}
func (s *sealedRuntimeState) Get(ctx context.Context, appID, deploymentID string) (generatedruntimestate.Deployment, error) {
	if s.getErr != nil {
		return generatedruntimestate.Deployment{}, s.getErr
	}
	return s.repository.Get(ctx, appID, deploymentID)
}
func (s *sealedRuntimeState) Active(ctx context.Context, appID string) (generatedruntimestate.ActiveHead, error) {
	if s.activeOverride != nil {
		return *s.activeOverride, nil
	}
	return s.repository.Active(ctx, appID)
}
func (s *sealedRuntimeState) Advance(ctx context.Context, appID, deploymentID string, expected, next generatedruntimestate.Phase, diagnostic generatedruntimestate.DiagnosticCode) (generatedruntimestate.Deployment, error) {
	if next == s.advanceErrTo && s.advanceErr != nil {
		return generatedruntimestate.Deployment{}, s.advanceErr
	}
	return s.repository.Advance(ctx, appID, deploymentID, expected, next, diagnostic)
}
func (s *sealedRuntimeState) AdvanceComponent(ctx context.Context, appID, deploymentID, component string, expected, next generatedruntimestate.ComponentState) (generatedruntimestate.Component, error) {
	return s.repository.AdvanceComponent(ctx, appID, deploymentID, component, expected, next)
}
func (s *sealedRuntimeState) FailComponent(ctx context.Context, appID, deploymentID, component string, expected generatedruntimestate.ComponentState, diagnostic generatedruntimestate.DiagnosticCode) (generatedruntimestate.Component, error) {
	return s.repository.FailComponent(ctx, appID, deploymentID, component, expected, diagnostic)
}

type sealedRuntimeEngine struct {
	engine          *generatedruntime.Engine
	networkErrs     []error
	networkCalls    int
	retained        int
	stopErrs        []error
	stopCalls       int
	reserveRequests []generatedingress.GeneratedReplacementRequest
	pinCalls        int
}

func (s *sealedRuntimeEngine) ReserveReplacement(ctx context.Context, request generatedingress.GeneratedReplacementRequest) (generatedruntime.ReplacementReservation, error) {
	s.reserveRequests = append(s.reserveRequests, request)
	return s.engine.ReserveReplacement(ctx, request)
}
func (s *sealedRuntimeEngine) RetainReplacementCapacity(reservation generatedruntime.ReplacementReservation) error {
	s.retained++
	return s.engine.RetainReplacementCapacity(reservation)
}
func (s *sealedRuntimeEngine) EnsureAppNetwork(ctx context.Context, request deployments.GeneratedNetworkRequest) error {
	call := s.networkCalls
	s.networkCalls++
	if call < len(s.networkErrs) && s.networkErrs[call] != nil {
		return s.networkErrs[call]
	}
	return s.engine.EnsureAppNetwork(ctx, request)
}
func (s *sealedRuntimeEngine) PinCandidates(ctx context.Context, request generatedingress.GeneratedCandidateRequest, reservation generatedruntime.ReplacementReservation) (*generatedingress.GeneratedCandidateExecution, error) {
	s.pinCalls++
	return s.engine.PinCandidates(ctx, request, reservation)
}
func (s *sealedRuntimeEngine) StopAndRemove(ctx context.Context, candidate generatedruntime.Candidate, timeout time.Duration) error {
	call := s.stopCalls
	s.stopCalls++
	if call < len(s.stopErrs) && s.stopErrs[call] != nil {
		return s.stopErrs[call]
	}
	return s.engine.StopAndRemove(ctx, candidate, timeout)
}

type sealedAuthorizationGate struct {
	delegate AuthorizationGate
	err      error
	calls    int
	after    func()
}

func (s *sealedAuthorizationGate) Authorize(ctx context.Context, request AuthorizationRequest) (generatedruntime.ReplacementReservation, error) {
	s.calls++
	if s.after != nil {
		s.after()
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.delegate.Authorize(ctx, request)
}

type sealedCandidateImage struct {
	appID, releaseID, planID string
	artifact                 generatedimage.Artifact
	component                deploymentplans.Component
}

type sealedCandidateContainer struct {
	id, name, image, network, alias, user, workdir, command string
	labels, tmpfs, logConfig                                map[string]string
	memory, memorySwap, nanoCPUs, pids                      int64
	logType, restart                                        string
	running, exists                                         bool
	startedAt, health                                       string
	migration                                               bool
}

type sealedCandidateRunner struct {
	mu                   sync.Mutex
	absenceErr           error
	images               map[string]sealedCandidateImage
	networks             map[string]string
	containers           map[string]*sealedCandidateContainer
	commands             []runtimeprocess.CommandRequest
	createErrs           []error
	startErrs            []error
	stopErrs             []error
	removeErrs           []error
	createCalls          int
	startCalls           int
	stopCalls            int
	removeCalls          int
	beforeNetworkInspect func()
	afterCreate          func()
	afterStart           func()
	startHealth          string
}

func newSealedCandidateRunner(t *testing.T) *sealedCandidateRunner {
	t.Helper()
	return &sealedCandidateRunner{
		absenceErr: executorMigrationCompletedExitOne(), images: make(map[string]sealedCandidateImage),
		networks: make(map[string]string), containers: make(map[string]*sealedCandidateContainer),
	}
}

func (r *sealedCandidateRunner) addImage(image sealedCandidateImage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images[image.artifact.ImageContentID] = image
}

func (r *sealedCandidateRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	r.commands = append(r.commands, request)
	if len(request.Args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("unexpected candidate command")
	}
	switch request.Args[0] + " " + request.Args[1] {
	case "image inspect":
		image, ok := r.images[request.Args[len(request.Args)-1]]
		if !ok {
			return runtimeprocess.CommandResult{}, errors.New("unexpected candidate image")
		}
		return sealedJSON(map[string]any{
			"id": image.artifact.ImageContentID, "size": 1,
			"labels": map[string]string{
				"io.rig.managed": "generated-image", "io.rig.application": image.appID,
				"io.rig.release": image.releaseID, "io.rig.artifact": image.artifact.ID,
				"io.rig.plan": image.planID, "io.rig.component": image.component.Name,
				"io.rig.role": image.component.Role, "io.rig.definition": image.artifact.BuildDefinitionDigest,
			},
			"user": generatedcontainer.User, "workingDir": "/workspace", "entrypoint": []string{generatedcontainer.Entrypoint},
		})
	case "network inspect":
		if r.beforeNetworkInspect != nil {
			callback := r.beforeNetworkInspect
			r.beforeNetworkInspect = nil
			callback()
		}
		name := request.Args[len(request.Args)-1]
		appID, ok := r.networks[name]
		if !ok {
			return runtimeprocess.CommandResult{Stderr: []byte("Error response from daemon: No such network: " + name)}, r.absenceErr
		}
		return sealedJSON(map[string]any{"name": name, "driver": "bridge", "scope": "local", "internal": false,
			"labels": map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": appID}})
	case "network create":
		name := request.Args[len(request.Args)-1]
		r.networks[name] = sealedArgumentLabel(request.Args, "io.rig.application")
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("9", 64) + "\n")}, nil
	case "container inspect":
		target := request.Args[len(request.Args)-1]
		container, ok := r.containers[target]
		if !ok || !container.exists {
			return runtimeprocess.CommandResult{Stderr: []byte("Error response from daemon: No such container: " + target)}, r.absenceErr
		}
		if container.migration {
			return sealedJSON(container.migrationInspection())
		}
		return sealedJSON(container.inspection())
	case "container create":
		call := r.createCalls
		r.createCalls++
		if call < len(r.createErrs) && r.createErrs[call] != nil {
			return runtimeprocess.CommandResult{}, r.createErrs[call]
		}
		container, err := sealedContainerFromCreate(request.Args, r.createCalls)
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		r.containers[container.id] = container
		r.containers[container.name] = container
		if r.afterCreate != nil {
			callback := r.afterCreate
			r.afterCreate = nil
			callback()
		}
		return runtimeprocess.CommandResult{Stdout: []byte(container.id + "\n")}, nil
	case "container start":
		call := r.startCalls
		r.startCalls++
		if call < len(r.startErrs) && r.startErrs[call] != nil {
			return runtimeprocess.CommandResult{}, r.startErrs[call]
		}
		container := r.containers[request.Args[len(request.Args)-1]]
		if container == nil {
			return runtimeprocess.CommandResult{}, errors.New("unknown candidate container")
		}
		container.running = true
		container.startedAt = "2026-10-20T12:00:00.123456789Z"
		container.health = r.startHealth
		if container.health == "" {
			container.health = "healthy"
		}
		if r.afterStart != nil {
			callback := r.afterStart
			r.afterStart = nil
			callback()
		}
		return runtimeprocess.CommandResult{Stdout: []byte(container.id + "\n")}, nil
	case "container wait":
		container := r.containers[request.Args[len(request.Args)-1]]
		if container == nil || !container.migration {
			return runtimeprocess.CommandResult{}, errors.New("unknown generated migration container")
		}
		container.running = false
		return runtimeprocess.CommandResult{Stdout: []byte("0\n")}, nil
	case "container stop":
		call := r.stopCalls
		r.stopCalls++
		if call < len(r.stopErrs) && r.stopErrs[call] != nil {
			return runtimeprocess.CommandResult{}, r.stopErrs[call]
		}
		container := r.containers[request.Args[len(request.Args)-1]]
		if container != nil {
			container.running = false
			container.health = ""
		}
		return runtimeprocess.CommandResult{}, nil
	case "container rm":
		call := r.removeCalls
		r.removeCalls++
		if call < len(r.removeErrs) && r.removeErrs[call] != nil {
			return runtimeprocess.CommandResult{}, r.removeErrs[call]
		}
		container := r.containers[request.Args[len(request.Args)-1]]
		if container != nil {
			container.exists = false
		}
		return runtimeprocess.CommandResult{}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected candidate command")
	}
}

func sealedContainerFromCreate(args []string, ordinal int) (*sealedCandidateContainer, error) {
	if len(args) < 6 {
		return nil, errors.New("candidate create arguments are incomplete")
	}
	labels := make(map[string]string)
	tmpfs := make(map[string]string)
	logConfig := make(map[string]string)
	for index := 0; index+1 < len(args); index++ {
		switch args[index] {
		case "--label":
			key, value, ok := strings.Cut(args[index+1], "=")
			if ok {
				labels[key] = value
			}
		case "--tmpfs":
			path, value, ok := strings.Cut(args[index+1], ":")
			if ok {
				tmpfs[path] = value
			}
		case "--log-opt":
			key, value, ok := strings.Cut(args[index+1], "=")
			if ok {
				logConfig[key] = value
			}
		}
	}
	memory, _ := strconv.ParseInt(sealedArgument(args, "--memory"), 10, 64)
	memorySwap, _ := strconv.ParseInt(sealedArgument(args, "--memory-swap"), 10, 64)
	pids, _ := strconv.ParseInt(sealedArgument(args, "--pids-limit"), 10, 64)
	cpus, _ := strconv.ParseFloat(sealedArgument(args, "--cpus"), 64)
	return &sealedCandidateContainer{
		id: strings.Repeat(strconv.FormatInt(int64((ordinal%8)+1), 10), 64), name: sealedArgument(args, "--name"),
		image: args[len(args)-4], network: sealedArgument(args, "--network"), alias: sealedArgument(args, "--network-alias"),
		user: sealedArgument(args, "--user"), workdir: sealedArgument(args, "--workdir"), command: args[len(args)-1],
		labels: labels, tmpfs: tmpfs, logConfig: logConfig, memory: memory, memorySwap: memorySwap,
		nanoCPUs: int64(cpus * 1_000_000_000), pids: pids, logType: sealedArgument(args, "--log-driver"),
		restart: sealedArgument(args, "--restart"), exists: true, migration: labels["io.rig.managed"] == "generated-migration",
	}, nil
}

func (c *sealedCandidateContainer) inspection() generatedcontainer.Inspection {
	status := "created"
	startedAt := time.Time{}.Format(time.RFC3339Nano)
	health := ""
	networks := map[string]generatedcontainer.NetworkAttachment{}
	if c.running {
		status, startedAt, health = "running", c.startedAt, c.health
		networks[c.network] = generatedcontainer.NetworkAttachment{Aliases: []string{c.alias}}
	} else if c.startedAt != "" {
		status, startedAt = "exited", c.startedAt
	}
	return generatedcontainer.Inspection{
		ID: c.id, Name: "/" + c.name, Image: c.image, ConfigImage: c.image, Labels: c.labels,
		User: c.user, WorkingDirectory: c.workdir, Entrypoint: []string{generatedcontainer.Entrypoint},
		Command: []string{"/bin/sh", "-lc", c.command}, Volumes: map[string]json.RawMessage{},
		HealthTest: []string{"CMD-SHELL", generatedcontainer.HealthCommand}, HealthInterval: int64(2 * time.Second),
		HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: c.memory, MemorySwap: c.memorySwap, NanoCPUs: c.nanoCPUs, PIDs: c.pids,
		Ulimits: []generatedcontainer.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, Init: true,
		NetworkMode: c.network, ReadonlyRootfs: true, CapAdd: []string{}, CapDrop: []string{"ALL"},
		SecurityOptions: []string{"no-new-privileges=true"}, Binds: []string{}, HostMounts: []json.RawMessage{},
		PortBindings: map[string]json.RawMessage{}, Tmpfs: c.tmpfs, LogType: c.logType, LogConfig: c.logConfig,
		Restart: c.restart, Status: status, Running: c.running, StartedAt: startedAt, Health: health,
		Networks: networks, Mounts: []generatedcontainer.Mount{},
	}
}

func (c *sealedCandidateContainer) migrationInspection() map[string]any {
	status := "created"
	startedAt := time.Time{}.Format(time.RFC3339Nano)
	networks := map[string]any{}
	if c.running {
		status, startedAt = "running", c.startedAt
	} else if c.startedAt != "" {
		status, startedAt = "exited", c.startedAt
	}
	if c.startedAt != "" {
		networks[c.network] = map[string]any{}
	}
	return map[string]any{
		"id": c.id, "name": "/" + c.name, "image": c.image, "configImage": c.image,
		"user": c.user, "workingDir": c.workdir, "entrypoint": []string{generatedcontainer.Entrypoint},
		"cmd": []string{"/bin/sh", "-lc", c.command}, "labels": c.labels, "volumes": map[string]any{},
		"memory": int64(536870912), "memorySwap": int64(536870912), "cpuPeriod": int64(100000), "cpuQuota": int64(100000), "pidsLimit": int64(256),
		"ulimits": []map[string]any{{"Name": "nofile", "Soft": int64(1024), "Hard": int64(1024)}}, "init": true,
		"networkMode": c.network, "readonlyRootfs": true, "privileged": false, "capAdd": []string{}, "capDrop": []string{"ALL"},
		"securityOpt": []string{"no-new-privileges"}, "binds": []string{}, "hostMounts": []any{}, "portBindings": map[string]any{},
		"tmpfs":   map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864,mode=1777"},
		"logType": "json-file", "logConfig": map[string]string{"max-size": "1m", "max-file": "1"}, "restart": "no",
		"status": status, "running": c.running, "paused": false, "restarting": false, "dead": false, "exitCode": 0,
		"startedAt": startedAt, "networks": networks, "mounts": []any{},
	}
}

func sealedArgument(args []string, name string) string {
	for index := range args {
		if args[index] == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func sealedArgumentLabel(args []string, key string) string {
	for index := range args {
		if args[index] == "--label" && index+1 < len(args) {
			name, value, ok := strings.Cut(args[index+1], "=")
			if ok && name == key {
				return value
			}
		}
	}
	return ""
}

func sealedJSON(value any) (runtimeprocess.CommandResult, error) {
	body, err := json.Marshal(value)
	return runtimeprocess.CommandResult{Stdout: body}, err
}

type sealedCandidateEnvironment struct {
	mu           sync.Mutex
	root         string
	stageCalls   int
	cleanupCalls int
}

func (s *sealedCandidateEnvironment) Stage(operationID string, attempt int, contents []byte) (generatedruntime.EnvironmentLease, error) {
	return s.stage(operationID, attempt, contents)
}
func (s *sealedCandidateEnvironment) StageGeneratedCandidate(operationID string, attempt int, contents []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	return s.stage(operationID, attempt, contents)
}
func (s *sealedCandidateEnvironment) StageGeneratedMigration(operationID string, attempt int, contents []byte) (deployments.GeneratedMigrationEnvironmentLease, error) {
	return s.stage(operationID, attempt, contents)
}
func (s *sealedCandidateEnvironment) stage(operationID string, attempt int, contents []byte) (*sealedCandidateEnvironmentLease, error) {
	defer func() {
		for index := range contents {
			contents[index] = 0
		}
	}()
	s.mu.Lock()
	s.stageCalls++
	s.mu.Unlock()
	path := filepath.Join(s.root, operationID+"-"+strconv.Itoa(attempt)+".env")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return nil, err
	}
	return &sealedCandidateEnvironmentLease{owner: s, path: path}, nil
}

type sealedCandidateEnvironmentLease struct {
	owner *sealedCandidateEnvironment
	path  string
	once  sync.Once
	err   error
}

func (l *sealedCandidateEnvironmentLease) Path() string { return l.path }
func (l *sealedCandidateEnvironmentLease) Cleanup() error {
	l.once.Do(func() {
		l.owner.mu.Lock()
		l.owner.cleanupCalls++
		l.owner.mu.Unlock()
		l.err = os.Remove(l.path)
		if errors.Is(l.err, os.ErrNotExist) {
			l.err = nil
		}
	})
	return l.err
}
