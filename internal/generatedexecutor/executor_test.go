package generatedexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
)

const (
	testAppID        = "11111111-1111-4111-8111-111111111111"
	testJobID        = "22222222-2222-4222-8222-222222222222"
	testActorID      = "33333333-3333-4333-8333-333333333333"
	testDeploymentID = "44444444-4444-4444-8444-444444444444"
	testReleaseID    = "55555555-5555-4555-8555-555555555555"
	testPlanID       = "66666666-6666-4666-8666-666666666666"
	testConfigID     = "77777777-7777-4777-8777-777777777777"
	testArtifactID   = "88888888-8888-4888-8888-888888888888"
	testArtifactID2  = "99999999-9999-4999-8999-999999999999"
)

type fakeApplications struct{ app apps.Application }

func (f *fakeApplications) Get(string) (apps.Application, error) { return f.app, nil }

type fakeReleases struct{ release releasesnapshot.Release }

func (f *fakeReleases) Materialize(context.Context, string, string) (releasesnapshot.Release, error) {
	return f.release, nil
}
func (f *fakeReleases) MaterializeLocal(context.Context, string, string) (releasesnapshot.Release, error) {
	return f.release, nil
}
func (f *fakeReleases) ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error) {
	return f.release, nil
}

type fakeConfiguration struct {
	exports int
	value   []byte
}

func (f *fakeConfiguration) ExportCurrentForExecution(context.Context, string) (appconfig.ExecutionConfiguration, error) {
	return f.export(), nil
}
func (f *fakeConfiguration) ExportRevisionForExecution(context.Context, string, string, int64) (appconfig.ExecutionConfiguration, error) {
	return f.export(), nil
}
func (f *fakeConfiguration) export() appconfig.ExecutionConfiguration {
	f.exports++
	return appconfig.ExecutionConfiguration{RevisionID: testConfigID, RevisionNumber: 3, Environment: append([]byte(nil), f.value...)}
}

type fakeDeployments struct{ deployment deployments.Deployment }

func (f *fakeDeployments) GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error) {
	return f.deployment, false, nil
}
func (f *fakeDeployments) Get(context.Context, string, string) (deployments.Deployment, error) {
	return f.deployment, nil
}
func (f *fakeDeployments) InitializeRuntime(_ context.Context, _, _, releaseID, configurationID string, configurationNumber int64, strategy deployments.RuntimeStrategy, planID string, planNumber int64) (deployments.Deployment, error) {
	f.deployment.ReleaseID = releaseID
	f.deployment.ActualConfigurationRevisionID = configurationID
	f.deployment.ActualConfigurationRevisionNumber = configurationNumber
	f.deployment.RuntimeStrategy = strategy
	f.deployment.DeploymentPlanRevisionID = planID
	f.deployment.DeploymentPlanRevisionNumber = planNumber
	f.deployment.ProvenanceInitialized = true
	return f.deployment, nil
}
func (f *fakeDeployments) Transition(_ context.Context, _, _ string, status deployments.Status, diagnostic string) (deployments.Deployment, error) {
	f.deployment.Status = status
	f.deployment.DiagnosticCode = diagnostic
	return f.deployment, nil
}

type fakePlans struct {
	revision deploymentplans.DeploymentPlanRevision
}

func (f *fakePlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return f.revision, nil
}

type fakeCompiler struct {
	events    *[]string
	artifacts []generatedimage.Artifact
	calls     int
}

func (f *fakeCompiler) Compile(context.Context, string, string, string) (generatedimage.Artifact, error) {
	*f.events = append(*f.events, "compile")
	index := f.calls
	f.calls++
	if index >= len(f.artifacts) {
		index = len(f.artifacts) - 1
	}
	return f.artifacts[index], nil
}

type fakeArtifacts struct {
	values            map[string]generatedimage.Artifact
	markedUnavailable []string
}

func (f *fakeArtifacts) Get(_ context.Context, id string) (generatedimage.Artifact, error) {
	value, exists := f.values[id]
	if !exists {
		return generatedimage.Artifact{}, generatedimage.ErrArtifactNotFound
	}
	return value, nil
}
func (f *fakeArtifacts) MarkUnavailable(_ context.Context, id string) (generatedimage.Artifact, error) {
	f.markedUnavailable = append(f.markedUnavailable, id)
	value := f.values[id]
	value.State = generatedimage.ArtifactUnavailable
	f.values[id] = value
	return value, nil
}

type fakeRuntimeState struct {
	deployment            generatedruntimestate.Deployment
	active                generatedruntimestate.ActiveHead
	beginErr              error
	migrationRequired     bool
	switchCalls           int
	failAdvanceToDraining bool
}

func (f *fakeRuntimeState) Begin(_ context.Context, input generatedruntimestate.BeginInput) (generatedruntimestate.Deployment, bool, error) {
	if f.beginErr != nil {
		return generatedruntimestate.Deployment{}, false, f.beginErr
	}
	if f.deployment.DeploymentID == "" {
		migrationState := generatedruntimestate.MigrationNotRequired
		if f.migrationRequired {
			migrationState = generatedruntimestate.MigrationPending
		}
		candidateSlot := "blue"
		if f.active.Slot == "blue" {
			candidateSlot = "green"
		}
		f.deployment = generatedruntimestate.Deployment{
			DeploymentID: input.DeploymentID, AppID: input.AppID, ReleaseID: input.ReleaseID,
			DeploymentPlanRevisionID: input.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: input.DeploymentPlanRevisionNumber,
			CandidateSlot: candidateSlot, PreviousActiveDeploymentID: f.active.DeploymentID, PreviousActiveSlot: f.active.Slot,
			Phase: generatedruntimestate.PhasePreflight, MigrationState: migrationState,
		}
		for _, name := range input.ComponentNames {
			f.deployment.Components = append(f.deployment.Components, generatedruntimestate.Component{DeploymentID: input.DeploymentID, Name: name, Slot: candidateSlot, State: generatedruntimestate.ComponentPending})
		}
	}
	return f.deployment, true, nil
}
func (f *fakeRuntimeState) Get(_ context.Context, _, deploymentID string) (generatedruntimestate.Deployment, error) {
	if deploymentID == f.deployment.DeploymentID {
		return f.deployment, nil
	}
	return generatedruntimestate.Deployment{}, generatedruntimestate.ErrNotFound
}
func (f *fakeRuntimeState) Active(context.Context, string) (generatedruntimestate.ActiveHead, error) {
	return f.active, nil
}
func (f *fakeRuntimeState) Advance(_ context.Context, _, _ string, _, next generatedruntimestate.Phase, diagnostic generatedruntimestate.DiagnosticCode) (generatedruntimestate.Deployment, error) {
	if next == generatedruntimestate.PhaseDraining && f.failAdvanceToDraining {
		return f.deployment, generatedruntimestate.ErrInvalidTransition
	}
	f.deployment.Phase = next
	f.deployment.DiagnosticCode = diagnostic
	return f.deployment, nil
}
func (f *fakeRuntimeState) BeginMigration(context.Context, string, string) (generatedruntimestate.Deployment, error) {
	f.deployment.MigrationState = generatedruntimestate.MigrationRunning
	return f.deployment, nil
}
func (f *fakeRuntimeState) FinishMigration(_ context.Context, _, _ string, succeeded bool) (generatedruntimestate.Deployment, error) {
	f.deployment.MigrationState = generatedruntimestate.MigrationFailed
	if succeeded {
		f.deployment.MigrationState = generatedruntimestate.MigrationSucceeded
	}
	return f.deployment, nil
}
func (f *fakeRuntimeState) SetImageReady(_ context.Context, _, _, name, artifactID string) (generatedruntimestate.Component, error) {
	component := f.component(name)
	component.State, component.ImageArtifactID = generatedruntimestate.ComponentImageReady, artifactID
	return *component, nil
}
func (f *fakeRuntimeState) SetContainerStarting(_ context.Context, _, _, name, containerName string) (generatedruntimestate.Component, error) {
	component := f.component(name)
	component.State, component.ContainerName = generatedruntimestate.ComponentStarting, containerName
	return *component, nil
}
func (f *fakeRuntimeState) SetContainerRunning(_ context.Context, _, _, name, containerID string) (generatedruntimestate.Component, error) {
	component := f.component(name)
	component.State, component.ContainerID = generatedruntimestate.ComponentRunning, containerID
	return *component, nil
}
func (f *fakeRuntimeState) AdvanceComponent(_ context.Context, _, deploymentID, name string, _, next generatedruntimestate.ComponentState) (generatedruntimestate.Component, error) {
	if deploymentID != f.deployment.DeploymentID {
		return generatedruntimestate.Component{}, generatedruntimestate.ErrNotFound
	}
	component := f.component(name)
	component.State = next
	return *component, nil
}
func (f *fakeRuntimeState) FailComponent(context.Context, string, string, string, generatedruntimestate.ComponentState, generatedruntimestate.DiagnosticCode) (generatedruntimestate.Component, error) {
	return generatedruntimestate.Component{}, nil
}
func (f *fakeRuntimeState) SwitchActive(context.Context, string, string, int64) (generatedruntimestate.ActiveHead, bool, error) {
	f.switchCalls++
	f.active = generatedruntimestate.ActiveHead{AppID: testAppID, DeploymentID: testDeploymentID, ReleaseID: testReleaseID, Slot: "blue", Generation: f.active.Generation + 1}
	for index := range f.deployment.Components {
		f.deployment.Components[index].State = generatedruntimestate.ComponentActive
	}
	return f.active, true, nil
}
func (f *fakeRuntimeState) component(name string) *generatedruntimestate.Component {
	for index := range f.deployment.Components {
		if f.deployment.Components[index].Name == name {
			return &f.deployment.Components[index]
		}
	}
	panic("missing fake component")
}

type fakeRuntime struct {
	events        *[]string
	validateErrs  []error
	validateCalls int
	healthErr     error
	createdSpecs  []generatedruntime.CandidateSpec
	adopted       int
	stopped       int
}

type fakeReplacementReservation struct{ releases int }

func (r *fakeReplacementReservation) Release() { r.releases++ }

func (f *fakeRuntime) ReserveReplacement(context.Context, int) (generatedruntime.ReplacementReservation, error) {
	return &fakeReplacementReservation{}, nil
}

func (f *fakeRuntime) ValidateImage(context.Context, generatedruntime.ImageSpec) error {
	*f.events = append(*f.events, "validate_image")
	index := f.validateCalls
	f.validateCalls++
	if index < len(f.validateErrs) {
		return f.validateErrs[index]
	}
	return nil
}
func (f *fakeRuntime) EnsureAppNetwork(context.Context, string) error {
	*f.events = append(*f.events, "ensure_network")
	return nil
}
func (f *fakeRuntime) CreateInactiveCandidate(_ context.Context, spec generatedruntime.CandidateSpec) (generatedruntime.Candidate, error) {
	*f.events = append(*f.events, "create")
	f.createdSpecs = append(f.createdSpecs, spec)
	description, _ := generatedruntime.DescribeInactiveCandidate(spec.AppID, spec.ComponentName, spec.ActiveSlot)
	return generatedruntime.Candidate{
		AppID: spec.AppID, ReleaseID: spec.ReleaseID, DeploymentID: spec.DeploymentID, ArtifactID: spec.ArtifactID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, Component: spec.ComponentName, Role: spec.Role, Slot: description.Slot,
		ContainerID: "a" + string(make([]byte, 63)), ContainerName: description.ContainerName,
		NetworkName: description.NetworkName, NetworkAlias: description.NetworkAlias, InternalPort: spec.InternalPort,
		ImageContentID: spec.ImageContentID, WorkingDirectory: runtimeWorkingDirectory(spec.RootDirectory), RunCommandDigest: commandDigest(spec.RunCommand),
	}, nil
}
func (f *fakeRuntime) AdoptCandidate(candidate generatedruntime.Candidate, _ generatedruntime.ReplacementReservation) (generatedruntime.Candidate, error) {
	f.adopted++
	return candidate, nil
}
func (f *fakeRuntime) StartCandidate(context.Context, generatedruntime.Candidate) error {
	*f.events = append(*f.events, "start")
	return nil
}
func (f *fakeRuntime) WaitHealthy(context.Context, generatedruntime.Candidate) error {
	*f.events = append(*f.events, "health")
	return f.healthErr
}
func (f *fakeRuntime) StopAndRemove(context.Context, generatedruntime.Candidate, time.Duration) error {
	f.stopped++
	return nil
}
func (f *fakeRuntime) ReleaseAdmission(generatedruntime.Candidate) {}

type fakeAuthorization struct {
	events       *[]string
	err          error
	deployments  *fakeDeployments
	calls        int
	reservations []*fakeReplacementReservation
}

func (f *fakeAuthorization) Authorize(context.Context, AuthorizationRequest) (generatedruntime.ReplacementReservation, error) {
	f.calls++
	*f.events = append(*f.events, "authorize")
	if f.err != nil {
		return nil, f.err
	}
	if f.deployments.deployment.Status == deployments.Preparing || f.deployments.deployment.Status == deployments.NeedsAttention {
		f.deployments.deployment.Status = deployments.Applying
	}
	reservation := &fakeReplacementReservation{}
	f.reservations = append(f.reservations, reservation)
	return reservation, nil
}

type fakeRoutes struct {
	events   *[]string
	requests []generatedruntime.RouteSwitchRequest
}

func (f *fakeRoutes) Switch(_ context.Context, request generatedruntime.RouteSwitchRequest) error {
	*f.events = append(*f.events, "route")
	f.requests = append(f.requests, request)
	return nil
}

type fakeMigrations struct {
	events   *[]string
	requests []generatedruntime.MigrationRequest
}

func (f *fakeMigrations) Run(_ context.Context, request generatedruntime.MigrationRequest) error {
	*f.events = append(*f.events, "migration")
	f.requests = append(f.requests, request)
	return nil
}

type fakeReporter struct{ phases []string }

func (f *fakeReporter) Report(update jobs.ProgressUpdate) error {
	f.phases = append(f.phases, update.Phase)
	return nil
}

type executorFixture struct {
	executor      *Executor
	deployments   *fakeDeployments
	state         *fakeRuntimeState
	compiler      *fakeCompiler
	artifacts     *fakeArtifacts
	runtime       *fakeRuntime
	authorization *fakeAuthorization
	routes        *fakeRoutes
	migrations    *fakeMigrations
	reporter      *fakeReporter
	events        *[]string
	plan          deploymentplans.DeploymentPlanRevision
}

func newExecutorFixture(t *testing.T, migration bool) *executorFixture {
	t.Helper()
	events := []string{}
	component := deploymentplans.Component{Name: "api", Role: generatedruntime.RoleServer, RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", NodeVersion: "22", BuildCommand: "npm run build", RunCommand: "npm start && echo $READY", InternalPort: 3000, HealthProbe: "/health"}
	plan := deploymentplans.DeploymentPlanRevision{ID: testPlanID, AppID: testAppID, RevisionNumber: 2, State: deploymentplans.RevisionAccepted, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{component}}}
	if migration {
		plan.Plan.Migration = &deploymentplans.Migration{ComponentName: "api", RootDirectory: ".", Command: "npm run migrate && echo $DATABASE_URL", EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: string(make([]byte, 64)), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalApproved}}
	}
	release := releasesnapshot.Release{ID: testReleaseID, AppID: testAppID, DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2, ConfigurationRevisionID: testConfigID, ConfigurationRevisionNumber: 3}
	artifact := readyArtifact(testArtifactID)
	deploymentsFake := &fakeDeployments{deployment: deployments.Deployment{ID: testDeploymentID, AppID: testAppID, JobID: testJobID, Status: deployments.Preparing, ConfigurationMode: "current"}}
	state := &fakeRuntimeState{active: generatedruntimestate.ActiveHead{AppID: testAppID}, migrationRequired: migration}
	compiler := &fakeCompiler{events: &events, artifacts: []generatedimage.Artifact{artifact}}
	artifacts := &fakeArtifacts{values: map[string]generatedimage.Artifact{artifact.ID: artifact}}
	runtime := &fakeRuntime{events: &events}
	authorization := &fakeAuthorization{events: &events, deployments: deploymentsFake}
	routes := &fakeRoutes{events: &events}
	migrations := &fakeMigrations{events: &events}
	executor, err := NewExecutor(
		&fakeApplications{app: apps.Application{ID: testAppID, Source: apps.Source{Type: apps.SourceLocal, Path: `C:\source`}}},
		&fakeReleases{release: release}, &fakeConfiguration{value: []byte("DATABASE_URL='secret'\n")}, deploymentsFake,
		&fakePlans{revision: plan}, compiler, artifacts, state, runtime, authorization, routes, migrations, Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &executorFixture{executor: executor, deployments: deploymentsFake, state: state, compiler: compiler, artifacts: artifacts, runtime: runtime, authorization: authorization, routes: routes, migrations: migrations, reporter: &fakeReporter{}, events: &events, plan: plan}
}

func readyArtifact(id string) generatedimage.Artifact {
	return generatedimage.Artifact{
		ID: id, ReleaseID: testReleaseID, DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
		ComponentID: "api", BuildDefinitionDigest: string(make([]byte, 64)), ImageContentID: "sha256:" + string(make([]byte, 64)), State: generatedimage.ArtifactReady,
	}
}

func deploymentJob() jobs.Job {
	input, _ := json.Marshal(jobs.DeploymentInput{ReleaseID: testReleaseID, ConfigurationMode: jobs.ConfigurationCurrent})
	return jobs.Job{ID: testJobID, Type: "deploy", ResourceType: "application", ResourceID: testAppID, RequestedBy: testActorID, Attempt: 1, Input: input}
}

func TestGeneratedExecutorOrdersGateBeforeMutationAndCompletesMigrationBlueGreen(t *testing.T) {
	fixture := newExecutorFixture(t, true)
	result, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletionCode != "deployment_completed" || fixture.deployments.deployment.Status != deployments.Succeeded || fixture.state.deployment.Phase != generatedruntimestate.PhaseSucceeded {
		t.Fatalf("result=%+v deployment=%+v runtime phase=%s", result, fixture.deployments.deployment, fixture.state.deployment.Phase)
	}
	wantEvents := []string{"authorize", "compile", "validate_image", "ensure_network", "ensure_network", "migration", "ensure_network", "create", "start", "health", "route"}
	if !reflect.DeepEqual(*fixture.events, wantEvents) {
		t.Fatalf("events=%v want=%v", *fixture.events, wantEvents)
	}
	if len(fixture.migrations.requests) != 1 || !reflect.DeepEqual(fixture.migrations.requests[0].AllowedEnvironmentKeys, []string{"DATABASE_URL"}) {
		t.Fatalf("migration request=%+v", fixture.migrations.requests)
	}
	if fixture.migrations.requests[0].Command != fixture.plan.Plan.Migration.Command {
		t.Fatal("accepted migration command was not preserved exactly")
	}
	if len(fixture.runtime.createdSpecs) != 1 || fixture.runtime.createdSpecs[0].RunCommand != fixture.plan.Plan.Components[0].RunCommand || fixture.runtime.createdSpecs[0].Role != generatedruntime.RoleServer || fixture.runtime.createdSpecs[0].Reservation == nil {
		t.Fatal("accepted run command was not preserved exactly")
	}
	if len(fixture.authorization.reservations) != 1 || fixture.authorization.reservations[0].releases != 1 {
		t.Fatal("successful deployment did not release its aggregate reservation exactly once")
	}
	if fixture.state.switchCalls != 1 || len(fixture.routes.requests) != 1 {
		t.Fatalf("switch calls=%d route requests=%d", fixture.state.switchCalls, len(fixture.routes.requests))
	}
	if endpoints := fixture.routes.requests[0].Endpoints; len(endpoints) != 1 || endpoints[0].Role != generatedruntime.RoleServer {
		t.Fatalf("route endpoints=%+v", endpoints)
	}
}

func TestGeneratedExecutorWaitsBeforeMutationWhenMigrationApprovalIsMissing(t *testing.T) {
	fixture := newExecutorFixture(t, true)
	fixture.state.beginErr = generatedruntimestate.ErrMigrationApprovalRequired
	result, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseMigrationApprovalRequired {
		t.Fatalf("result=%+v", result)
	}
	if fixture.authorization.calls != 0 || fixture.compiler.calls != 0 || len(*fixture.events) != 0 {
		t.Fatalf("unexpected pre-approval work: calls=%d events=%v", fixture.compiler.calls, *fixture.events)
	}
}

func TestGeneratedExecutorWaitsBeforeMutationWhenCapacityAdmissionFails(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	fixture.authorization.err = ErrInsufficientReplacementCapacity
	result, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseInsufficientReplacementCapacity || fixture.compiler.calls != 0 {
		t.Fatalf("result=%+v compile calls=%d", result, fixture.compiler.calls)
	}
	if !reflect.DeepEqual(*fixture.events, []string{"authorize"}) {
		t.Fatalf("events=%v", *fixture.events)
	}
}

func TestGeneratedExecutorPreservesRuntimePolicyApprovalPause(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	fixture.authorization.err = deployments.ErrApprovalRequired
	result, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != jobs.PauseApprovalRequired || fixture.compiler.calls != 0 {
		t.Fatalf("result=%+v compile calls=%d", result, fixture.compiler.calls)
	}
	if !reflect.DeepEqual(*fixture.events, []string{"authorize"}) {
		t.Fatalf("events=%v", *fixture.events)
	}
}

func TestGeneratedExecutorMarksDriftedImageUnavailableAndRebuildsOnce(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	second := readyArtifact(testArtifactID2)
	fixture.compiler.artifacts = append(fixture.compiler.artifacts, second)
	fixture.artifacts.values[second.ID] = second
	fixture.runtime.validateErrs = []error{&generatedruntime.Error{Code: generatedruntime.DiagnosticImageDriftDetected}, nil}
	if _, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter); err != nil {
		t.Fatal(err)
	}
	if fixture.compiler.calls != 2 || !reflect.DeepEqual(fixture.artifacts.markedUnavailable, []string{testArtifactID}) {
		t.Fatalf("compile calls=%d marked=%v", fixture.compiler.calls, fixture.artifacts.markedUnavailable)
	}
	if fixture.state.deployment.Components[0].ImageArtifactID != testArtifactID2 {
		t.Fatalf("persisted artifact=%s", fixture.state.deployment.Components[0].ImageArtifactID)
	}
}

func TestGeneratedExecutorFailsClosedWhenRebuiltImageStillDrifts(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	second := readyArtifact(testArtifactID2)
	fixture.compiler.artifacts = append(fixture.compiler.artifacts, second)
	fixture.artifacts.values[second.ID] = second
	fixture.runtime.validateErrs = []error{
		&generatedruntime.Error{Code: generatedruntime.DiagnosticImageDriftDetected},
		&generatedruntime.Error{Code: generatedruntime.DiagnosticImageDriftDetected},
	}
	_, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err == nil || fixture.compiler.calls != 2 || len(fixture.runtime.createdSpecs) != 0 {
		t.Fatalf("err=%v compile calls=%d create calls=%d", err, fixture.compiler.calls, len(fixture.runtime.createdSpecs))
	}
	if !reflect.DeepEqual(fixture.artifacts.markedUnavailable, []string{testArtifactID}) {
		t.Fatalf("marked unavailable=%v", fixture.artifacts.markedUnavailable)
	}
}

func TestGeneratedExecutorHealthFailurePreservesOldActiveHead(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	oldDeploymentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	fixture.state.active = generatedruntimestate.ActiveHead{AppID: testAppID, DeploymentID: oldDeploymentID, ReleaseID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Slot: "blue", Generation: 4}
	fixture.runtime.healthErr = &generatedruntime.Error{Code: generatedruntime.DiagnosticCandidateUnhealthy}
	_, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "health_failed" {
		t.Fatalf("err=%v", err)
	}
	if fixture.state.switchCalls != 0 || len(fixture.routes.requests) != 0 || fixture.state.active.DeploymentID != oldDeploymentID || fixture.state.active.Generation != 4 {
		t.Fatalf("old active changed before healthy switch: active=%+v route=%d", fixture.state.active, len(fixture.routes.requests))
	}
	if fixture.runtime.stopped != 1 {
		t.Fatalf("candidate cleanup calls=%d", fixture.runtime.stopped)
	}
	if len(fixture.authorization.reservations) != 1 || fixture.authorization.reservations[0].releases != 1 {
		t.Fatal("failed candidate lifecycle did not release aggregate admission exactly once")
	}
}

func TestGeneratedExecutorDoesNotRerunInterruptedMigration(t *testing.T) {
	fixture := newExecutorFixture(t, true)
	fixture.deployments.deployment = initializedDeployment(deployments.Applying)
	fixture.state.deployment = generatedruntimestate.Deployment{
		DeploymentID: testDeploymentID, AppID: testAppID, ReleaseID: testReleaseID,
		DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2, CandidateSlot: "blue",
		Phase: generatedruntimestate.PhaseMigrating, MigrationState: generatedruntimestate.MigrationRunning,
		Components: []generatedruntimestate.Component{{DeploymentID: testDeploymentID, Name: "api", Slot: "blue", ImageArtifactID: testArtifactID, State: generatedruntimestate.ComponentImageReady}},
	}
	_, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err == nil || len(fixture.migrations.requests) != 0 {
		t.Fatalf("err=%v migration calls=%d", err, len(fixture.migrations.requests))
	}
}

func TestGeneratedExecutorRecoveryAcquiresAndConsumesNewProcessReservation(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	fixture.deployments.deployment = initializedDeployment(deployments.WaitingHealth)
	description, err := generatedruntime.DescribeInactiveCandidate(testAppID, "api", "")
	if err != nil {
		t.Fatal(err)
	}
	fixture.state.deployment = generatedruntimestate.Deployment{
		DeploymentID: testDeploymentID, AppID: testAppID, ReleaseID: testReleaseID,
		DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
		CandidateSlot: string(description.Slot), Phase: generatedruntimestate.PhaseWaitingHealth,
		MigrationState: generatedruntimestate.MigrationNotRequired,
		Components: []generatedruntimestate.Component{{
			DeploymentID: testDeploymentID, Name: "api", Slot: string(description.Slot),
			ImageArtifactID: testArtifactID, ContainerID: "a" + string(make([]byte, 63)),
			ContainerName: description.ContainerName, State: generatedruntimestate.ComponentRunning,
		}},
	}
	result, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletionCode != "deployment_completed" || fixture.compiler.calls != 0 || len(fixture.runtime.createdSpecs) != 0 {
		t.Fatalf("result=%+v compiler=%d creates=%d", result, fixture.compiler.calls, len(fixture.runtime.createdSpecs))
	}
	if fixture.authorization.calls != 1 || fixture.runtime.adopted < 1 || len(fixture.authorization.reservations) != 1 || fixture.authorization.reservations[0].releases != 1 {
		t.Fatalf("authorization=%d adopted=%d reservations=%+v", fixture.authorization.calls, fixture.runtime.adopted, fixture.authorization.reservations)
	}
}

func TestGeneratedExecutorNeverCleansNewSlotAfterActiveCAS(t *testing.T) {
	fixture := newExecutorFixture(t, false)
	fixture.state.failAdvanceToDraining = true
	_, err := fixture.executor.Execute(context.Background(), deploymentJob(), fixture.reporter)
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "apply_failed" {
		t.Fatalf("err=%v", err)
	}
	if fixture.state.active.DeploymentID != testDeploymentID || fixture.runtime.stopped != 0 {
		t.Fatalf("active=%+v candidate cleanup=%d", fixture.state.active, fixture.runtime.stopped)
	}
	if fixture.state.deployment.Phase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatalf("durable phase=%s", fixture.state.deployment.Phase)
	}
}

func initializedDeployment(status deployments.Status) deployments.Deployment {
	return deployments.Deployment{
		ID: testDeploymentID, AppID: testAppID, JobID: testJobID, Status: status, ConfigurationMode: "current",
		ReleaseID: testReleaseID, ActualConfigurationRevisionID: testConfigID, ActualConfigurationRevisionNumber: 3,
		RuntimeStrategy: deployments.RuntimeGeneratedNode, DeploymentPlanRevisionID: testPlanID, DeploymentPlanRevisionNumber: 2,
		ProvenanceInitialized: true,
	}
}
