package generatedexecutor

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	"github.com/hostd/hostd/internal/runtimeexecutor"
	"github.com/hostd/hostd/internal/sourceconnections"
	"github.com/hostd/hostd/internal/sourceinspection"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

func TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement(t *testing.T) {
	fixture := newCapacityResumeFixture(t)

	result, err := fixture.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: fixture.app.ID,
		RequestedBy:   fixture.actorID, IdempotencyKey: "capacity-resume",
		Input: jobs.DeploymentInput{
			ConfigurationMode:                   jobs.ConfigurationCurrent,
			ExpectedPlanRevisionID:              fixture.plan.ID,
			ExpectedPlanRevisionNumber:          fixture.plan.RevisionNumber,
			ExpectedConfigurationRevisionID:     fixture.configuration.RevisionID,
			ExpectedConfigurationRevisionNumber: fixture.configuration.RevisionNumber,
		},
	}, jobs.DeploymentCreateHooks{})
	job := result.Job
	if err != nil || !result.Created {
		t.Fatalf("create job=%+v created=%t err=%v", job, result.Created, err)
	}

	firstPause := fixture.runUntilJob(t, job.ID, jobs.WaitingUser)
	fixture.assertPausedWithoutRuntimeSideEffects(t, firstPause, 1)
	firstDeployment := fixture.onlyDeployment(t)
	if !firstDeployment.ProvenanceInitialized || firstDeployment.ReleaseID != fixture.release.ID ||
		firstDeployment.DeploymentPlanRevisionID != fixture.plan.ID || firstDeployment.DeploymentPlanRevisionNumber != fixture.plan.RevisionNumber ||
		firstDeployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID || firstDeployment.ActualConfigurationRevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("first paused deployment lost accepted provenance: %+v", firstDeployment)
	}

	resumed, err := fixture.jobs.Resume(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != string(jobs.Queued) || resumed.Attempt != 1 {
		t.Fatalf("resume changed attempt before claim: job=%+v", resumed)
	}
	secondPause := fixture.runUntilJob(t, job.ID, jobs.WaitingUser)
	fixture.assertPausedWithoutRuntimeSideEffects(t, secondPause, 2)
	secondDeployment := fixture.onlyDeployment(t)
	if secondDeployment.ID != firstDeployment.ID || secondDeployment.ReleaseID != fixture.release.ID ||
		secondDeployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID {
		t.Fatalf("repeat pause changed deployment provenance: first=%+v second=%+v", firstDeployment, secondDeployment)
	}

	// The current configuration changes while the accepted job is paused. Resume
	// must use the deployment's immutable actual revision, never repin to this head.
	driftValue := "drift"
	drifted, err := fixture.configurations.ReplaceScoped(context.Background(), fixture.app.ID, fixture.actorID, appconfig.ScopedReplaceInput{
		ExpectedRevisionNumber: fixture.configuration.RevisionNumber,
		PlanRevisionID:         fixture.plan.ID,
		PlanRevisionNumber:     fixture.plan.RevisionNumber,
		Components:             []appconfig.ComponentTarget{{Name: "api", Role: "server"}},
		Entries: []appconfig.ScopedValueInput{{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: "api", Key: "MARKER"},
			Sensitivity: appconfig.SensitivityPublic, Value: &driftValue,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if drifted.RevisionID == fixture.configuration.RevisionID {
		t.Fatal("configuration drift did not create a new immutable head")
	}

	fixture.capacity.SetCapacity(1<<30, 1<<30)
	resumed, err = fixture.jobs.Resume(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != string(jobs.Queued) || resumed.Attempt != 2 {
		t.Fatalf("second resume changed attempt before claim: job=%+v", resumed)
	}
	completed := fixture.runUntilJob(t, job.ID, jobs.Succeeded)
	if completed.Attempt != 3 || completed.PauseDisposition != "" {
		t.Fatalf("completed job=%+v", completed)
	}
	if fixture.capacity.CapacityCalls() != 3 {
		t.Fatalf("capacity admission checks=%d want 3", fixture.capacity.CapacityCalls())
	}
	if fixture.compiler.calls != 1 || fixture.runner.createCalls != 1 || len(fixture.routes.requests) != 1 || len(fixture.migrations.requests) != 0 {
		t.Fatalf("replacement side effects compile=%d candidates=%d routes=%d migrations=%d", fixture.compiler.calls, fixture.runner.createCalls, len(fixture.routes.requests), len(fixture.migrations.requests))
	}
	if len(fixture.compiler.pins) != 1 || fixture.compiler.pins[0].RevisionID != fixture.configuration.RevisionID || fixture.compiler.pins[0].RevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("compiler used changed configuration head: pins=%+v accepted=%+v", fixture.compiler.pins, fixture.configuration)
	}
	finalDeployment := fixture.onlyDeployment(t)
	fixture.assertOneRelease(t)
	if finalDeployment.ID != firstDeployment.ID || finalDeployment.Status != deployments.Succeeded ||
		finalDeployment.ReleaseID != fixture.release.ID || finalDeployment.DeploymentPlanRevisionID != fixture.plan.ID ||
		finalDeployment.DeploymentPlanRevisionNumber != fixture.plan.RevisionNumber ||
		finalDeployment.ActualConfigurationRevisionID != fixture.configuration.RevisionID || finalDeployment.ActualConfigurationRevisionNumber != fixture.configuration.RevisionNumber {
		t.Fatalf("completed deployment=%+v", finalDeployment)
	}
	runtimeDeployment, err := fixture.runtimeState.Get(context.Background(), fixture.app.ID, finalDeployment.ID)
	if err != nil || runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded {
		t.Fatalf("runtime deployment=%+v err=%v", runtimeDeployment, err)
	}
	active, err := fixture.runtimeState.Active(context.Background(), fixture.app.ID)
	if err != nil || active.DeploymentID != finalDeployment.ID || active.ReleaseID != fixture.release.ID {
		t.Fatalf("active head=%+v err=%v", active, err)
	}
}

func TestGeneratedNetworkRefusalAfterFirstRealCandidateRetainsWholeAggregate(t *testing.T) {
	api := deploymentplans.Component{Name: "api", Role: generatedruntime.RoleServer, Technology: "node", RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "22", BuildCommand: "npm run build", RunCommand: "node api.js", InternalPort: 3000, HealthProbe: "/health"}
	web := api
	web.Name, web.Role, web.Technology, web.RootDirectory, web.StaticOutputDirectory, web.InternalPort = "web", generatedruntime.RoleStatic, "static", "apps/web", "dist", 3001
	web.RunCommand = projectanalysis.ManagedStaticCommand(web.StaticOutputDirectory, int(web.InternalPort))
	web.InstallDirectory = web.RootDirectory
	fixture := newSealedExecutorFixtureWithComponents(t, []deploymentplans.Component{api, web})
	fixture.capacity.SetCapacity(256<<20, 256<<20)
	checkCompetitor := fixture.capacityCompetitor(t)
	fixture.runner.afterStart = func() {
		fixture.runner.beforeNetworkInspect = func() {
			if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, fixture.job.ID); err != nil {
				t.Errorf("invalidate candidate claim: %v", err)
			}
		}
	}
	result, executeErr := fixture.execute()
	var executionErr *jobs.ExecutionError
	if result != (jobs.ExecutionResult{}) || !errors.As(executeErr, &executionErr) || executionErr.Code != "runtime_unavailable" ||
		!errors.Is(executeErr, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("retention result=%#v error=%v", result, executeErr)
	}
	if fixture.runner.createCalls != 1 || fixture.runner.startCalls != 1 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
		t.Fatalf("container creates=%d starts=%d stops=%d removals=%d", fixture.runner.createCalls, fixture.runner.startCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
	}
	deployment, runtimeDeployment := fixture.deployment(t), fixture.runtimeDeployment(t)
	if deployment.Status != deployments.Applying || runtimeDeployment.Phase != generatedruntimestate.PhaseStartingCandidate || len(runtimeDeployment.Components) != 2 {
		t.Fatalf("retained deployment=%+v runtime=%+v", deployment, runtimeDeployment)
	}
	var receipts int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`, deployment.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("retained receipts=%d err=%v", receipts, err)
	}
	checkCompetitor(true)
}

func TestResumedGeneratedNetworkRiskRetainsRealCapacityBeforeExplicitRefusal(t *testing.T) {
	for _, test := range []struct {
		name         string
		seed         sealedCandidateSeed
		wantRetained int
	}{
		{name: "name only", seed: sealedSeedReceiptOnly, wantRetained: 1},
		{name: "configured candidate", seed: sealedSeedCreated, wantRetained: 1},
		{name: "running candidate", seed: sealedSeedRunning, wantRetained: 1},
		{name: "attempt unknown", seed: sealedSeedAttemptUnknown, wantRetained: 1},
		{name: "all unattempted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSealedExecutorFixture(t)
			fixture.capacity.SetCapacity(128<<20, 128<<20)
			checkCompetitor := fixture.capacityCompetitor(t)
			if test.seed != 0 {
				fixture.seedCandidate(t, test.seed)
			}
			var beforeMain deployments.Deployment
			var beforeRuntime generatedruntimestate.Deployment
			if test.seed != 0 {
				beforeMain = fixture.deployment(t)
				beforeRuntime = fixture.runtimeDeployment(t)
			}
			fixture.runtime.networkErrs = []error{generatedNetworkBoundaryTestError()}
			createsBefore, startsBefore := fixture.runner.createCalls, fixture.runner.startCalls
			result, executeErr := fixture.execute()
			var executionErr *jobs.ExecutionError
			if result != (jobs.ExecutionResult{}) || !errors.As(executeErr, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("resumed refusal result=%+v error=%v", result, executeErr)
			}
			retainedMain := fixture.deployment(t)
			retainedRuntime := fixture.runtimeDeployment(t)
			if retainedMain.Status == deployments.Failed || retainedMain.Status == deployments.Cancelled ||
				retainedRuntime.Phase == generatedruntimestate.PhaseFailed || retainedRuntime.Phase == generatedruntimestate.PhaseCancelled {
				t.Fatalf("resumed refusal terminalized evidence: beforeMain=%+v beforeRuntime=%+v main=%+v runtime=%+v", beforeMain, beforeRuntime, retainedMain, retainedRuntime)
			}
			if fixture.runtime.retained != test.wantRetained || fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
				t.Fatalf("retention=%d want=%d legacyCleanup=%d stops=%d removals=%d", fixture.runtime.retained, test.wantRetained, fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
			}
			if fixture.runner.createCalls != createsBefore || fixture.runner.startCalls != startsBefore {
				t.Fatalf("resumed network refusal created or started a candidate: creates=%d/%d starts=%d/%d", fixture.runner.createCalls, createsBefore, fixture.runner.startCalls, startsBefore)
			}
			if test.seed != 0 && (!reflect.DeepEqual(retainedMain, beforeMain) || !reflect.DeepEqual(retainedRuntime, beforeRuntime)) {
				t.Fatalf("resumed refusal changed durable evidence: beforeMain=%+v afterMain=%+v beforeRuntime=%+v afterRuntime=%+v", beforeMain, retainedMain, beforeRuntime, retainedRuntime)
			}
			checkCompetitor(test.wantRetained != 0)
		})
	}
}

type capacityResumeFixture struct {
	executor       jobs.Executor
	admission      jobs.ScopedAdmission
	db             *sql.DB
	app            apps.Application
	actorID        string
	jobs           *jobs.Service
	applications   *apps.Store
	deployments    *deployments.Repository
	runtimeState   *generatedruntimestate.Repository
	configurations *appconfig.Store
	plans          *deploymentplans.Store
	materializer   *releasesnapshot.Materializer
	artifactStore  *generatedimage.ArtifactRepository
	workspace      string
	dataRoot       string
	directories    runtimedocker.ControllerDirectories
	plan           deploymentplans.DeploymentPlanRevision
	configuration  appconfig.Configuration
	release        releasesnapshot.Release
	capacity       *generateddomain.Runner
	compiler       *fakeCompiler
	runtime        *sealedRuntimeEngine
	runner         *sealedCandidateRunner
	routes         *fakeRoutes
	migrations     *fakeMigrations
}

func newCapacityResumeFixture(t *testing.T) *capacityResumeFixture {
	t.Helper()
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
		DockerExecutable: filepath.Join(dataRoot, "docker.exe"), WorkingDirectory: directories.WorkingDirectory,
		DockerConfigDirectory: directories.DockerConfigDirectory, CommandTimeout: time.Minute, HealthTimeout: time.Minute,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: 16 << 10,
		Limits:               generatedruntime.ContainerLimits{MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 64, TmpfsBytes: 8 << 20, LogSize: "1m", LogFiles: 2},
		ReplacementDiskBytes: 128 << 20,
	}
	domainFixture := newExecutorDomainFixture(t, dataRoot, runner, environment, engineOptions)
	db, bindings, plans, configurations := domainFixture.db, domainFixture.bindings, domainFixture.plans, domainFixture.configuration
	deploymentRepository := domainFixture.repository

	actorID, machineID := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'capacity-owner','hash',?,?)`, actorID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,'capacity-local','local','ready','test','test','test','test',?,?)`, machineID, now, now); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	for path, body := range map[string]string{
		"package.json":      `{"name":"capacity-fixture","scripts":{"start":"node server.js"}}`,
		"package-lock.json": `{"name":"capacity-fixture","lockfileVersion":3,"packages":{}}`,
		"server.js":         `require("node:http").createServer((_, response) => response.end("ok")).listen(process.env.PORT || 3000)`,
	} {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	applicationStore := apps.New(db)
	app, err := applicationStore.Create("Capacity Resume", "", workspace, machineID)
	if err != nil {
		t.Fatal(err)
	}

	inspection, err := sourceinspection.InspectLocal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	acceptedPlan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, projectanalysis.DeploymentSetup{Components: []projectanalysis.SetupComponent{{
		ID: "api", Technology: "node", RootDirectory: ".", PackageManager: "npm", NodeVersion: "22",
		InstallCommand: "npm ci", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health",
	}}}, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := plans.Replace(context.Background(), app.ID, actorID, deploymentplans.ReplaceInput{Plan: acceptedPlan})
	if err != nil {
		t.Fatal(err)
	}
	marker := "accepted"
	configuration, err := configurations.ReplaceScoped(context.Background(), app.ID, actorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: plan.ID, PlanRevisionNumber: plan.RevisionNumber,
		Components: []appconfig.ComponentTarget{{Name: "api", Role: "server"}},
		Entries: []appconfig.ScopedValueInput{{
			ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: "api", Key: "MARKER"},
			Sensitivity: appconfig.SensitivityPublic, Value: &marker,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := releasesnapshot.New(db, capacityUnusedSourceReader{}, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded, getErr := plans.GetRevision(context.Background(), app.ID, plan.ID, plan.RevisionNumber); getErr != nil || reloaded.ID != plan.ID {
		t.Fatalf("accepted plan unavailable before release materialization: plan=%+v err=%v", reloaded, getErr)
	}
	release, err := materializer.MaterializeLocal(context.Background(), app.ID, workspace)
	if err != nil {
		var state, code string
		queryErr := db.QueryRow(`SELECT workspace_state,COALESCE(materialization_error_code,'') FROM releases ORDER BY created_at DESC LIMIT 1`).Scan(&state, &code)
		t.Fatalf("materialize local release: err=%v state=%q code=%q queryErr=%v", err, state, code, queryErr)
	}

	capacity := domainFixture.process
	capacity.SetCapacity(0, 0)
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	artifactRepository := generatedimage.NewArtifactRepository(db)
	artifact, created, err := artifactRepository.Begin(context.Background(), generatedimage.BeginArtifactInput{
		ReleaseID: release.ID, DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
		ComponentID: "api", CompilerVersion: "capacity-resume/test", BuildDefinitionDigest: strings.Repeat("a", 64),
	})
	if err != nil || !created {
		t.Fatalf("begin artifact=%+v created=%t err=%v", artifact, created, err)
	}
	artifact, err = artifactRepository.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	runner.addImage(sealedCandidateImage{appID: app.ID, releaseID: release.ID, planID: plan.ID, artifact: artifact, component: plan.Plan.Components[0]})
	engine, err := generatedruntime.NewEngine(domainFixture.process, environment, domainFixture.domain.Candidates(), engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := NewAuthorizationGate(deploymentRepository, plans, engine)
	if err != nil {
		t.Fatal(err)
	}
	compiler := &fakeCompiler{
		events: &events, artifacts: []generatedimage.Artifact{artifact}, repository: deploymentRepository,
		release: release, plan: plan,
	}
	runtimeState := generatedruntimestate.New(db)
	runtime := &sealedRuntimeEngine{engine: engine}
	routes := &fakeRoutes{events: &events, delegate: generatedRouteController{authority: domainFixture.domain.Routes()}}
	migrations := &fakeMigrations{events: &events}
	boundExecutor, err := newExecutor(applicationStore, materializer, configurations, deploymentRepository, plans, compiler, artifactRepository, runtimeState, runtime, authorization, routes, migrations, Options{})
	if err != nil {
		t.Fatal(err)
	}
	router, err := runtimeexecutor.New(deploymentRepository, plans, materializer, nil, boundExecutor, bindings, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	admission := func(parent context.Context) (context.Context, func() error, error) {
		scope, release, err := deploymenteffects.AcquireScoped(parent, directories.WorkingDirectory)
		if err != nil {
			return nil, nil, err
		}
		return deploymenteffects.WithScope(parent, scope), release, nil
	}
	fixture := &capacityResumeFixture{executor: router, admission: admission, db: db, app: app, actorID: actorID, jobs: jobService, applications: applicationStore, deployments: deploymentRepository, runtimeState: runtimeState,
		plans: plans, materializer: materializer, artifactStore: artifactRepository, workspace: workspace, dataRoot: dataRoot, directories: directories,
		configurations: configurations, plan: plan, configuration: configuration, release: release, capacity: capacity,
		compiler: compiler, runtime: runtime, runner: runner, routes: routes, migrations: migrations}
	return fixture
}

func (f *capacityResumeFixture) runUntilJob(t *testing.T, id string, status jobs.Status) jobs.Job {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.jobs.RunWorkerWithScopedAdmission(ctx, f.executor, f.admission) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	return f.waitForJob(t, id, status)
}

func (f *capacityResumeFixture) waitForJob(t *testing.T, id string, status jobs.Status) jobs.Job {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := f.jobs.Get(id)
		if err == nil && job.Status == string(status) {
			return job
		}
		select {
		case <-deadline.C:
			job, getErr := f.jobs.Get(id)
			deployments, deploymentErr := f.deployments.List(context.Background(), f.app.ID, 10)
			var phase generatedruntimestate.Phase
			var diagnostic generatedruntimestate.DiagnosticCode
			var componentState generatedruntimestate.ComponentState
			var componentDiagnostic generatedruntimestate.DiagnosticCode
			var stateErr error
			if len(deployments) == 1 {
				runtimeDeployment, err := f.runtimeState.Get(context.Background(), f.app.ID, deployments[0].ID)
				stateErr = err
				phase, diagnostic = runtimeDeployment.Phase, runtimeDeployment.DiagnosticCode
				if len(runtimeDeployment.Components) == 1 {
					componentState, componentDiagnostic = runtimeDeployment.Components[0].State, runtimeDeployment.Components[0].DiagnosticCode
				}
			}
			f.runner.mu.Lock()
			fallbackCommandCount := len(f.runner.commands)
			f.runner.mu.Unlock()
			t.Fatalf("job did not reach %s: status=%q code=%q err=%v deployments=%d deploymentErr=%v runtimePhase=%q runtimeDiagnostic=%q componentState=%q componentDiagnostic=%q stateErr=%v capacityCalls=%d routeInputs=%d routeCause=%q fallbackCommands=%d domainCommands=%d", status, job.Status, job.ErrorCode, getErr, len(deployments), deploymentErr, phase, diagnostic, componentState, componentDiagnostic, stateErr, f.capacity.CapacityCalls(), len(f.routes.inputs), classifyRoutePauseCause(f.routes.lastErr), fallbackCommandCount, len(f.capacity.Commands()))
		case <-ticker.C:
		}
	}
}

func (f *capacityResumeFixture) assertPausedWithoutRuntimeSideEffects(t *testing.T, job jobs.Job, attempt int) {
	t.Helper()
	if job.Attempt != attempt || job.PauseDisposition != jobs.PauseInsufficientReplacementCapacity || job.ErrorCode != "" {
		t.Fatalf("paused job=%+v", job)
	}
	if f.compiler.calls != 0 || f.runner.createCalls != 0 || f.runner.startCalls != 0 || len(f.routes.requests) != 0 || len(f.migrations.requests) != 0 {
		t.Fatalf("capacity gate allowed runtime work: compiler=%d candidates=%d starts=%d routes=%d migrations=%d", f.compiler.calls, f.runner.createCalls, f.runner.startCalls, len(f.routes.requests), len(f.migrations.requests))
	}
}

func (f *capacityResumeFixture) onlyDeployment(t *testing.T) deployments.Deployment {
	t.Helper()
	values, err := f.deployments.List(context.Background(), f.app.ID, 10)
	if err != nil || len(values) != 1 {
		t.Fatalf("deployments=%+v err=%v", values, err)
	}
	return values[0]
}

func (f *capacityResumeFixture) assertOneRelease(t *testing.T) {
	t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM releases WHERE app_id=? AND workspace_state='ready'`, f.app.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ready releases=%d err=%v", count, err)
	}
}

type capacityUnusedSourceReader struct{}

func (capacityUnusedSourceReader) Resolve(context.Context, string, string, int64, int64, string) (sourceconnections.SourceRepository, sourceconnections.Branch, error) {
	return sourceconnections.SourceRepository{}, sourceconnections.Branch{}, errors.New("local materialization does not resolve GitHub")
}
func (capacityUnusedSourceReader) ReadTree(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (githubapp.Tree, error) {
	return githubapp.Tree{}, errors.New("local materialization does not read GitHub")
}
func (capacityUnusedSourceReader) DownloadArchive(context.Context, string, string, int64, sourceconnections.SourceRepository, string) (io.ReadCloser, error) {
	return nil, errors.New("local materialization does not download GitHub")
}
