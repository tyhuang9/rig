package generatedruntime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

// runtimeDomainFixture builds a complete domain before any application,
// deployment or runtime rows are written. The test owns only setup values;
// runtime, capacity and route authority remain sealed inside the domain.
type runtimeDomainFixture struct {
	db               *sql.DB
	repository       *deployments.Repository
	bindings         *deploymentbinding.Repository
	plans            *deploymentplans.Store
	configuration    *appconfig.Store
	context          context.Context
	workingDirectory string
	engine           *Engine
	domain           *generatedingress.GeneratedDomain
	predecessor      *runtimePredecessorRunner
}

func newRuntimeDomainFixture(t *testing.T, dataRoot string, runner runtimeprocess.CommandRunner,
	environment generatedingress.GeneratedCandidateEnvironmentStager, options EngineOptions,
	runnerOptions generateddomain.Options,
) runtimeDomainFixture {
	t.Helper()
	options = normalizedEngineOptions(options)
	lease, err := controllerowner.Acquire(context.Background(), options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	db, err := database.OpenForGeneratedDomain(context.Background(), dataRoot, lease)
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
	guard, err := deploymentexecution.NewGuard(db, bindings, positiveFence, options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
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
	predecessor := &runtimePredecessorRunner{fallback: runner, limits: options.Limits, absenceErr: runtimeCandidateExitOne(t)}
	ownedEnvironment := &runtimePredecessorEnvironment{
		delegate: environment, predecessor: predecessor,
		path: filepath.Join(options.WorkingDirectory, "runtime-predecessor.env"),
	}
	runnerOptions.Fallback = predecessor
	process := generateddomain.NewRunner(runnerOptions)
	domain, err := generatedingress.NewGeneratedDomain(context.Background(), stores, process, ownedEnvironment, generatedingress.GeneratedDomainOptions{
		DockerExecutable: options.DockerExecutable, DockerEndpoint: options.DockerEndpoint,
		DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
		DataRoot: dataRoot, CommandTimeout: options.CommandTimeout, PullTimeout: time.Second,
		HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval,
		OutputLimit: options.OutputLimit, MemoryBytes: options.Limits.MemoryBytes,
		MilliCPUs: options.Limits.MilliCPUs, PIDs: options.Limits.PIDs,
		TmpfsBytes: options.Limits.TmpfsBytes, LogSize: options.Limits.LogSize,
		LogFiles: options.Limits.LogFiles, ReplacementDiskBytes: options.ReplacementDiskBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(process, ownedEnvironment, domain.Candidates(), options)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeDomainFixture{db: db, repository: repository, bindings: bindings, plans: plans,
		configuration: configuration, context: context.Background(), workingDirectory: options.WorkingDirectory,
		engine: engine, domain: domain, predecessor: predecessor}
}

type runtimeNetworkAuthority struct {
	db         *sql.DB
	repository *deployments.Repository
	context    context.Context
	release    func() error
	scope      deploymenteffects.Scope
	spec       CandidateSpec
}

func newRuntimeNetworkAuthority(t *testing.T, fixture runtimeDomainFixture, spec CandidateSpec) runtimeNetworkAuthority {
	return newRuntimeNetworkAuthorityWithConfiguration(t, fixture, spec, nil)
}

// newRuntimeNetworkAuthorityWithConfiguration supplies an exact scoped
// configuration through the protected configuration store. A nil entries slice
// keeps the standard candidate fixture configuration; a non-nil slice lets a
// boundary test provide its own real protected runtime entries.
func newRuntimeNetworkAuthorityWithConfiguration(t *testing.T, fixture runtimeDomainFixture, spec CandidateSpec, entries []appconfig.ScopedValueInput) runtimeNetworkAuthority {
	t.Helper()
	db, repository, bindings, plans, configuration := fixture.db, fixture.repository, fixture.bindings, fixture.plans, fixture.configuration
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
	spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber = configurationRevision.RevisionID, configurationRevision.RevisionNumber
	spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber = plan.ID, plan.RevisionNumber
	if spec.ActiveSlot != "" {
		seedRuntimeActivePredecessor(t, fixture, actorID, spec)
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
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, spec.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.workingDirectory)
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

// seedRuntimeActivePredecessor creates the fixture predecessor through the
// same Claim-bound candidate and sealed-route path used by a real first
// deployment. A first deployment has no predecessor retirement work, so the
// fixture completes its receipt-backed Draining state through the same exact
// runtime, deployment and job transitions as the live authority.
func seedRuntimeActivePredecessor(t *testing.T, fixture runtimeDomainFixture, actorID string, successor CandidateSpec) {
	t.Helper()
	if successor.ActiveSlot != SlotBlue {
		t.Fatalf("unsupported active predecessor slot %q", successor.ActiveSlot)
	}
	if fixture.domain == nil || fixture.predecessor == nil {
		t.Fatal("runtime predecessor requires the sealed generated domain")
	}
	ctx := context.Background()
	prior := successor
	prior.ReleaseID = uuid.NewString()
	prior.DeploymentID, prior.ArtifactID, prior.ActiveSlot = "", "", ""
	// The generated-domain test runner owns the fixed Caddy image content ID.
	// Give the application predecessor a distinct immutable image so its exact
	// inspection is delegated to the application candidate transcript.
	prior.ImageContentID = "sha256:" + strings.Repeat("e", 64)
	jobService, err := jobs.NewWithDeploymentBindings(fixture.db, fixture.bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := jobService.CreateDeploymentWithInputTransaction(ctx, jobs.DeploymentCreateRequest{
		ApplicationID: prior.AppID, IdempotencyKey: "generated-runtime-predecessor-" + uuid.NewString(), RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create generated predecessor job: created=%t error=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`, prior.ReleaseID, prior.AppID, stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), prior.ConfigurationRevisionID, prior.ConfigurationRevisionNumber, prior.DeploymentPlanRevisionID, prior.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	deployment, _, err := fixture.repository.GetOrCreateByJob(ctx, prior.AppID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = fixture.repository.InitializeRuntime(ctx, prior.AppID, deployment.ID, prior.ReleaseID,
		prior.ConfigurationRevisionID, prior.ConfigurationRevisionNumber, deployments.RuntimeGeneratedNode,
		prior.DeploymentPlanRevisionID, prior.DeploymentPlanRevisionNumber)
	if err != nil {
		t.Fatalf("initialize generated predecessor provenance: %v", err)
	}
	if _, err := fixture.repository.Transition(ctx, prior.AppID, deployment.ID, deployments.Applying, ""); err != nil {
		t.Fatalf("enter generated predecessor applying state: %v", err)
	}
	prior.DeploymentID = deployment.ID
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, prior.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	prior.Network = deployments.GeneratedNetworkRequest{
		Claim: claim, DeploymentID: prior.DeploymentID, ReleaseID: prior.ReleaseID,
		ConfigurationRevisionID: prior.ConfigurationRevisionID, ConfigurationRevisionNumber: prior.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: prior.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: prior.DeploymentPlanRevisionNumber,
	}
	scope, release, err := deploymenteffects.AcquireScoped(ctx, fixture.workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release generated predecessor scope: %v", err)
		}
	}()
	ctx = deploymenteffects.WithScope(ctx, scope)
	if err := fixture.repository.Gate(ctx, deployments.GateRequest{Claim: claim, DeploymentID: prior.DeploymentID}); err != nil {
		t.Fatalf("gate generated predecessor: %v", err)
	}
	state := generatedruntimestate.New(fixture.db)
	runtimeDeployment, began, err := state.Begin(ctx, generatedruntimestate.BeginInput{
		DeploymentID: prior.DeploymentID, AppID: prior.AppID, ReleaseID: prior.ReleaseID,
		DeploymentPlanRevisionID: prior.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: prior.DeploymentPlanRevisionNumber,
		ComponentNames: []string{prior.ComponentName},
	})
	if err != nil || !began {
		t.Fatalf("begin generated predecessor runtime: began=%t error=%v", began, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
		runtimeDeployment, err = state.Advance(ctx, prior.AppID, prior.DeploymentID, runtimeDeployment.Phase, next, "")
		if err != nil {
			t.Fatalf("advance generated predecessor to %q: %v", next, err)
		}
	}
	artifacts := generatedimagestate.NewRepository(fixture.db)
	artifact, _, err := artifacts.Begin(ctx, generatedimagestate.BeginArtifactInput{
		ReleaseID: prior.ReleaseID, DeploymentPlanRevisionID: prior.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: prior.DeploymentPlanRevisionNumber, ComponentID: prior.ComponentName,
		CompilerVersion: "runtime-test", BuildDefinitionDigest: prior.BuildDefinitionDigest,
	})
	if err != nil {
		t.Fatalf("begin generated predecessor artifact: %v", err)
	}
	artifact, err = artifacts.Complete(ctx, artifact.ID, prior.ImageContentID)
	if err != nil {
		t.Fatalf("complete generated predecessor artifact: %v", err)
	}
	if _, err := state.SetImageReady(ctx, prior.AppID, prior.DeploymentID, prior.ComponentName, artifact.ID); err != nil {
		t.Fatalf("record generated predecessor artifact: %v", err)
	}
	prior.ArtifactID = artifact.ID
	description, err := DescribeInactiveCandidate(prior.AppID, prior.ComponentName, prior.ActiveSlot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.predecessor.begin(prior, description)
	defer fixture.predecessor.close()
	request := generatedingress.GeneratedCandidateRequest{
		Claim: claim, DeploymentID: prior.DeploymentID, ReleaseID: prior.ReleaseID,
		ConfigurationRevisionID: prior.ConfigurationRevisionID, ConfigurationRevisionNumber: prior.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: prior.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: prior.DeploymentPlanRevisionNumber,
		CandidateSlot: string(description.Slot),
	}
	reservation, err := fixture.engine.ReserveReplacement(ctx, generatedingress.GeneratedReplacementRequest(request))
	if err != nil {
		t.Fatalf("reserve generated predecessor: %v", err)
	}
	defer reservation.Release()
	execution, err := fixture.engine.PinCandidates(ctx, request, reservation)
	if err != nil {
		t.Fatalf("pin generated predecessor: %v", err)
	}
	defer execution.Leave()
	if err := fixture.engine.EnsureAppNetwork(ctx, prior.Network); err != nil {
		t.Fatalf("ensure generated predecessor network: %v", err)
	}
	invocation, err := execution.BeginCreate(prior.ComponentName)
	if err != nil {
		t.Fatalf("begin generated predecessor create: %v", err)
	}
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatalf("create generated predecessor candidate: %v", err)
	}
	if err := fixture.predecessor.bind(identity); err != nil {
		t.Fatalf("bind generated predecessor observation: %v", err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatalf("start generated predecessor candidate: %v", err)
	}
	if _, err := execution.FinishStarting(); err != nil {
		t.Fatalf("finish generated predecessor start: %v", err)
	}
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatalf("wait generated predecessor health: %v", err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatalf("finish generated predecessor health: %v", err)
	}
	route, err := fixture.domain.Routes().Pin(ctx, execution, generatedroute.RouteSwitchRequest{
		AppID: prior.AppID, ToSlot: description.Slot,
		Endpoints: []generatedroute.RouteEndpoint{{Component: identity.ComponentName, Role: identity.Role,
			ContainerID: identity.ContainerID, NetworkName: identity.NetworkName, NetworkAlias: identity.NetworkAlias,
			InternalPort: identity.InternalPort}},
	})
	if err != nil {
		t.Fatalf("pin generated predecessor route: %v", err)
	}
	activated, err := route.Activate(ctx)
	if err != nil {
		t.Fatalf("activate generated predecessor route: %v", err)
	}
	head, err := state.Active(ctx, prior.AppID)
	if err != nil || head.DeploymentID != prior.DeploymentID || head.ActivationReceiptID == "" || string(head.Slot) != string(description.Slot) {
		t.Fatalf("generated predecessor head=%#v error=%v", head, err)
	}
	if activated.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("generated predecessor phase=%q want=%q", activated.Phase, generatedruntimestate.PhaseDraining)
	}
	if _, err := state.Advance(ctx, prior.AppID, prior.DeploymentID,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatalf("finish generated predecessor route: %v", err)
	}
	if _, err := fixture.repository.Transition(ctx, prior.AppID, prior.DeploymentID, deployments.Succeeded, ""); err != nil {
		t.Fatalf("finish generated predecessor deployment: %v", err)
	}
	finished := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := fixture.db.ExecContext(ctx, `UPDATE jobs SET status='succeeded',phase='succeeded',error_code='',updated_at=?,finished_at=? WHERE id=? AND status='running'`, finished, finished, created.Job.ID)
	if err != nil {
		t.Fatalf("finish generated predecessor job: %v", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("finish generated predecessor job changed=%d want=1", changed)
	}
}

// runtimePredecessorRunner is deliberately narrow: while the fixture seeds a
// single original candidate it supplies only that candidate's typed Docker
// observations. The generated domain retains every route and receipt action;
// all unrelated commands keep their caller-provided fallback behavior.
type runtimePredecessorRunner struct {
	fallback    runtimeprocess.CommandRunner
	limits      ContainerLimits
	active      bool
	spec        CandidateSpec
	description CandidateDescription
	containerID string
	status      string
	identity    generatedingress.CandidateIdentity
	absenceErr  error
}

// runtimePredecessorEnvironment keeps first-deployment fixture staging out of
// the consumer stager's behavior counters. Both the domain and Engine receive
// this exact owner, so the production pointer-identity validation remains real.
type runtimePredecessorEnvironment struct {
	delegate    generatedingress.GeneratedCandidateEnvironmentStager
	predecessor *runtimePredecessorRunner
	path        string
}

func (s *runtimePredecessorEnvironment) StageGeneratedCandidate(operationID string, attempt int, contents []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	if s != nil && s.predecessor != nil && s.predecessor.active {
		clear(contents)
		return runtimePredecessorEnvironmentLease{path: s.path}, nil
	}
	return s.delegate.StageGeneratedCandidate(operationID, attempt, contents)
}

type runtimePredecessorEnvironmentLease struct{ path string }

func (l runtimePredecessorEnvironmentLease) Path() string { return l.path }
func (runtimePredecessorEnvironmentLease) Cleanup() error { return nil }

func (r *runtimePredecessorRunner) begin(spec CandidateSpec, description CandidateDescription) {
	r.active, r.spec, r.description = true, spec, description
	r.containerID, r.status, r.identity = strings.Repeat("9", 64), "absent", generatedingress.CandidateIdentity{}
}

func (r *runtimePredecessorRunner) close() {
	r.active, r.spec, r.description = false, CandidateSpec{}, CandidateDescription{}
	r.containerID, r.status, r.identity = "", "", generatedingress.CandidateIdentity{}
}

func (r *runtimePredecessorRunner) bind(identity generatedingress.CandidateIdentity) error {
	if !r.active || identity.ContainerID != r.containerID || identity.ContainerName != r.description.ContainerName ||
		identity.NetworkName != r.description.NetworkName || identity.NetworkAlias != r.description.NetworkAlias ||
		identity.Slot != string(r.description.Slot) || identity.ReceiptID == "" {
		return errors.New("generated predecessor candidate identity mismatch")
	}
	r.identity = identity
	return nil
}

func (r *runtimePredecessorRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if !r.active || len(request.Args) < 2 {
		return r.runFallback(ctx, request)
	}
	args, target := request.Args, request.Args[len(request.Args)-1]
	switch args[0] + " " + args[1] {
	case "network inspect":
		if target != r.description.NetworkName {
			return r.runFallback(ctx, request)
		}
		return runtimeJSON(networkInspection{Name: r.description.NetworkName, Driver: "bridge", Scope: "local", Labels: map[string]string{
			"io.rig.managed": NetworkOwnershipLabelValue, "io.rig.application": r.spec.AppID,
		}}).result, nil
	case "image inspect":
		if target != r.spec.ImageContentID {
			return r.runFallback(ctx, request)
		}
		return runtimeJSON(validImageInspection(r.spec)).result, nil
	case "container inspect":
		if target != r.description.ContainerName && target != r.containerID {
			return r.runFallback(ctx, request)
		}
		if r.status == "absent" {
			return runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + target)}, r.absenceErr
		}
		if r.identity.ReceiptID == "" {
			return runtimeprocess.CommandResult{}, errors.New("generated predecessor candidate was observed before receipt binding")
		}
		running := r.status == "running"
		return runtimeJSON(candidateInspection(r.spec, r.identity, r.limits, r.containerID, r.status, map[bool]string{true: "healthy", false: ""}[running], running)).result, nil
	case "container create":
		name, receipt := "", ""
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--name" {
				name = args[index+1]
			}
			if args[index] == "--label" && strings.HasPrefix(args[index+1], generatedcontainer.ReceiptLabel+"=") {
				receipt = strings.TrimPrefix(args[index+1], generatedcontainer.ReceiptLabel+"=")
			}
		}
		if name != r.description.ContainerName || receipt == "" || r.status != "absent" {
			return r.runFallback(ctx, request)
		}
		r.status = "created"
		r.identity = generatedingress.CandidateIdentity{ReceiptID: receipt, DeploymentID: r.spec.DeploymentID,
			ReleaseID: r.spec.ReleaseID, ArtifactID: r.spec.ArtifactID, DeploymentPlanRevisionID: r.spec.DeploymentPlanRevisionID,
			ComponentName: r.spec.ComponentName, Role: r.spec.Role, Technology: r.spec.Technology, Slot: string(r.description.Slot),
			ContainerID: r.containerID, ContainerName: r.description.ContainerName, NetworkName: r.description.NetworkName,
			NetworkAlias: r.description.NetworkAlias, InternalPort: r.spec.InternalPort}
		return runtimeprocess.CommandResult{Stdout: []byte(r.containerID + "\n")}, nil
	case "container start":
		if target != r.containerID || r.status != "created" {
			return r.runFallback(ctx, request)
		}
		r.status = "running"
		return runtimeprocess.CommandResult{}, nil
	default:
		return r.runFallback(ctx, request)
	}
}

func (r *runtimePredecessorRunner) runFallback(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if r.fallback == nil {
		return runtimeprocess.CommandResult{}, errors.New("unexpected generated predecessor command")
	}
	return r.fallback.Run(ctx, request)
}
