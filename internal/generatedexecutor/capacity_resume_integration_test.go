package generatedexecutor

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/githubapp"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
	"github.com/hostd/hostd/internal/sourceinspection"
)

func TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement(t *testing.T) {
	fixture := newCapacityResumeFixture(t)

	job, created, err := fixture.jobs.CreateWithInput(jobs.CreateRequest{
		Type: "deploy", ResourceType: "application", ResourceID: fixture.app.ID,
		RequestedBy: fixture.actorID, IdempotencyKey: "capacity-resume",
		Input: jobs.DeploymentInput{
			ConfigurationMode:                   jobs.ConfigurationCurrent,
			ExpectedPlanRevisionID:              fixture.plan.ID,
			ExpectedPlanRevisionNumber:          fixture.plan.RevisionNumber,
			ExpectedConfigurationRevisionID:     fixture.configuration.RevisionID,
			ExpectedConfigurationRevisionNumber: fixture.configuration.RevisionNumber,
		},
	})
	if err != nil || !created {
		t.Fatalf("create job=%+v created=%t err=%v", job, created, err)
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

	fixture.capacity.Set(generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 1 << 30, DiskAvailableBytes: 1 << 30})
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
	if fixture.capacity.Calls() != 3 {
		t.Fatalf("capacity admission checks=%d want 3", fixture.capacity.Calls())
	}
	if fixture.compiler.calls != 1 || len(fixture.runtime.createdSpecs) != 1 || len(fixture.routes.requests) != 1 || len(fixture.migrations.requests) != 0 {
		t.Fatalf("replacement side effects compile=%d candidates=%d routes=%d migrations=%d", fixture.compiler.calls, len(fixture.runtime.createdSpecs), len(fixture.routes.requests), len(fixture.migrations.requests))
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

type capacityResumeFixture struct {
	executor       *Executor
	db             *sql.DB
	app            apps.Application
	actorID        string
	jobs           *jobs.Service
	deployments    *deployments.Repository
	runtimeState   *generatedruntimestate.Repository
	configurations *appconfig.Store
	plan           deploymentplans.DeploymentPlanRevision
	configuration  appconfig.Configuration
	release        releasesnapshot.Release
	capacity       *scriptedCapacitySource
	compiler       *fakeCompiler
	runtime        *capacityResumeRuntime
	routes         *fakeRoutes
	migrations     *fakeMigrations
}

func newCapacityResumeFixture(t *testing.T) *capacityResumeFixture {
	t.Helper()
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

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
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := plans.Replace(context.Background(), app.ID, actorID, deploymentplans.ReplaceInput{Plan: acceptedPlan})
	if err != nil {
		t.Fatal(err)
	}
	configurations, err := appconfig.New(db, dataRoot)
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

	capacity := &scriptedCapacitySource{}
	capacity.Set(generatedruntime.CapacitySnapshot{})
	dockerConfig := filepath.Join(dataRoot, "docker-config")
	if err := os.Mkdir(dockerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	admissionEngine, err := generatedruntime.NewEngine(capacityUnusedRunner{}, capacityUnusedEnvironment{}, capacity, generatedruntime.EngineOptions{
		DockerExecutable:      filepath.Join(dataRoot, "docker.exe"),
		WorkingDirectory:      dataRoot,
		DockerConfigDirectory: dockerConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	deploymentRepository := deployments.New(db)
	authorization, err := NewAuthorizationGate(deploymentRepository, plans, admissionEngine)
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
	compiler := &fakeCompiler{events: &events, artifacts: []generatedimage.Artifact{artifact}}
	runtimeState := generatedruntimestate.New(db)
	runtime := &capacityResumeRuntime{fakeRuntime: &fakeRuntime{events: &events}}
	routes := &fakeRoutes{events: &events}
	migrations := &fakeMigrations{events: &events}
	executor, err := NewExecutor(applicationStore, materializer, configurations, deploymentRepository, plans, compiler, artifactRepository, runtimeState, runtime, authorization, routes, migrations, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &capacityResumeFixture{executor: executor, db: db, app: app, actorID: actorID, jobs: jobs.New(db), deployments: deploymentRepository, runtimeState: runtimeState,
		configurations: configurations, plan: plan, configuration: configuration, release: release, capacity: capacity,
		compiler: compiler, runtime: runtime, routes: routes, migrations: migrations}
	return fixture
}

func (f *capacityResumeFixture) runUntilJob(t *testing.T, id string, status jobs.Status) jobs.Job {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.jobs.RunWorker(ctx, f.executor) }()
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
			t.Fatalf("job did not reach %s: status=%q code=%q err=%v deployments=%d deploymentErr=%v runtimePhase=%q runtimeDiagnostic=%q componentState=%q componentDiagnostic=%q stateErr=%v", status, job.Status, job.ErrorCode, getErr, len(deployments), deploymentErr, phase, diagnostic, componentState, componentDiagnostic, stateErr)
		case <-ticker.C:
		}
	}
}

func (f *capacityResumeFixture) assertPausedWithoutRuntimeSideEffects(t *testing.T, job jobs.Job, attempt int) {
	t.Helper()
	if job.Attempt != attempt || job.PauseDisposition != jobs.PauseInsufficientReplacementCapacity || job.ErrorCode != "" {
		t.Fatalf("paused job=%+v", job)
	}
	if f.compiler.calls != 0 || len(f.runtime.createdSpecs) != 0 || len(f.runtime.started) != 0 || len(f.routes.requests) != 0 || len(f.migrations.requests) != 0 {
		t.Fatalf("capacity gate allowed runtime work: compiler=%d candidates=%d starts=%d routes=%d migrations=%d", f.compiler.calls, len(f.runtime.createdSpecs), len(f.runtime.started), len(f.routes.requests), len(f.migrations.requests))
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

type scriptedCapacitySource struct {
	mu       sync.Mutex
	snapshot generatedruntime.CapacitySnapshot
	calls    int
}

func (s *scriptedCapacitySource) Set(snapshot generatedruntime.CapacitySnapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
}

func (s *scriptedCapacitySource) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.snapshot, nil
}

func (s *scriptedCapacitySource) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type capacityUnusedRunner struct{}

type capacityResumeRuntime struct{ *fakeRuntime }

func (r *capacityResumeRuntime) CreateInactiveCandidate(ctx context.Context, spec generatedruntime.CandidateSpec) (generatedruntime.Candidate, error) {
	candidate, err := r.fakeRuntime.CreateInactiveCandidate(ctx, spec)
	if err == nil {
		candidate.ContainerID = strings.Repeat("c", 64)
	}
	return candidate, err
}

func (capacityUnusedRunner) Run(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{}, errors.New("capacity admission must not execute Docker")
}

type capacityUnusedEnvironment struct{}

func (capacityUnusedEnvironment) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("capacity admission must not stage an environment")
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
