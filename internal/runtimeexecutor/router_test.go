package runtimeexecutor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
)

const routerReleaseID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type routerDeployments struct {
	deployment deployments.Deployment
	calls      int
}

func (r *routerDeployments) GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error) {
	r.calls++
	return r.deployment, false, nil
}

type routerPlans struct {
	head      deploymentplans.DeploymentPlanRevision
	revision  deploymentplans.DeploymentPlanRevision
	err       error
	gets      int
	revisions int
}

func (r *routerPlans) Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error) {
	r.gets++
	return r.head, r.err
}

func (r *routerPlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	r.revisions++
	return r.revision, r.err
}

type routerReleases struct {
	release releasesnapshot.Release
	err     error
	calls   int
}

func (r *routerReleases) ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error) {
	r.calls++
	return r.release, r.err
}

type recordingBindings struct {
	delegate       deploymentBindings
	readBinding    *deploymentbinding.Binding
	readErr        error
	checkErr       error
	classification deploymentbinding.Classification
	reads          int
	checks         int
	readContexts   []context.Context
	checkContexts  []context.Context
	checked        []deploymentbinding.Binding
	afterRead      func()
	beforeCheck    func(int)
	afterCheck     func(int)
}

func (b *recordingBindings) Read(ctx context.Context, jobID string) (deploymentbinding.Binding, error) {
	b.reads++
	b.readContexts = append(b.readContexts, ctx)
	defer func() {
		if b.afterRead != nil {
			b.afterRead()
		}
	}()
	if b.readErr != nil {
		return deploymentbinding.Binding{}, b.readErr
	}
	if b.readBinding != nil {
		return *b.readBinding, nil
	}
	return b.delegate.Read(ctx, jobID)
}

func (b *recordingBindings) Check(ctx context.Context, binding deploymentbinding.Binding) (deploymentbinding.Classification, error) {
	b.checks++
	b.checkContexts = append(b.checkContexts, ctx)
	b.checked = append(b.checked, binding)
	if b.beforeCheck != nil {
		b.beforeCheck(b.checks)
	}
	if b.checkErr != nil {
		return 0, b.checkErr
	}
	if b.classification != 0 {
		return b.classification, nil
	}
	classification, err := b.delegate.Check(ctx, binding)
	if b.afterCheck != nil {
		b.afterCheck(b.checks)
	}
	return classification, err
}

type recordingExecutor struct {
	calls     int
	contexts  []context.Context
	jobs      []jobs.Job
	reporters []jobs.ProgressReporter
	claims    []deploymentexecution.Claim
}

func (e *recordingExecutor) ExecuteBound(ctx context.Context, job jobs.Job, claim deploymentexecution.Claim, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	e.calls++
	e.contexts = append(e.contexts, ctx)
	e.jobs = append(e.jobs, job)
	e.claims = append(e.claims, claim)
	e.reporters = append(e.reporters, reporter)
	return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
}

type noopReporter struct{}

func (noopReporter) Report(jobs.ProgressUpdate) error { return nil }

func clearRebindFence(context.Context) error { return nil }

type routerBindingFixture struct {
	db        *sql.DB
	jobs      *jobs.Service
	lifecycle *applifecycle.Repository
	bindings  *deploymentbinding.Repository
	appID     string
	actorID   string
	job       jobs.Job
	binding   deploymentbinding.Binding
	pins      applifecycle.GeneratedLifecyclePinsV1
}

func newRouterBindingFixture(t *testing.T, running bool) *routerBindingFixture {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := &routerBindingFixture{
		db: db, jobs: jobs.New(db), lifecycle: applifecycle.New(db), appID: uuid.NewString(), actorID: uuid.NewString(),
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`,
		fixture.actorID, fixture.actorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`,
		fixture.appID, fixture.appID, fixture.appID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if running {
		fixture.pins = fixture.prepareRunningPins(t, stamp)
		fixture.completeOperation(t, applifecycle.Start, applifecycle.BaselineRunning, 0, "initial-running")
	}
	validator, err := applifecycle.NewDeploymentBindingValidator(fixture.lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	fixture.bindings, err = deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	typedJobs, err := jobs.NewWithDeploymentBindings(db, fixture.bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := typedJobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: fixture.appID, IdempotencyKey: "router-deployment", RequestedBy: fixture.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create deployment job created=%t error=%v", created.Created, err)
	}
	fixture.jobs = typedJobs
	fixture.job = created.Job
	fixture.job.Attempt = 1
	fixture.binding = created.Binding
	return fixture
}

func (f *routerBindingFixture) completeOperation(t *testing.T, action applifecycle.Action, baseline applifecycle.BaselineDisposition, expected int64, key string) applifecycle.Operation {
	t.Helper()
	pins := f.pins
	pins.BaselineDisposition = baseline
	accepted, err := f.lifecycle.Accept(context.Background(), f.jobs, applifecycle.LifecycleRequest{
		ApplicationID: f.appID, ActorID: f.actorID, IdempotencyKey: key, Action: action, ExpectedDesiredRevision: expected,
	}, pins)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1 WHERE id=?`, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	operationJob, err := f.jobs.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(operationJob)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range routerEffectSpecs(accepted.Operation) {
		request := applifecycle.EffectRequestV1{
			Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: f.appID,
			DesiredRevision: accepted.Operation.DesiredRevision, Boundary: spec.boundary,
			ComponentName: spec.component, Mode: spec.mode, PinsDigest: accepted.Operation.PinsDigest,
		}
		tx, err := f.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		intent, created, err := f.lifecycle.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request)
		if err != nil || !created {
			_ = tx.Rollback()
			t.Fatalf("begin %s/%s created=%t error=%v", spec.boundary, spec.component, created, err)
		}
		proof := applifecycle.EffectProofV1{
			Version: 1, OperationID: accepted.Operation.JobID, ApplicationID: f.appID,
			DesiredRevision: accepted.Operation.DesiredRevision, Boundary: spec.boundary,
			ComponentName: spec.component, Outcome: spec.outcome, IntentDigest: intent.RequestDigest,
			EvidenceDigest: strings.Repeat("e", 64), PinsDigest: accepted.Operation.PinsDigest,
		}
		if _, observed, err := f.lifecycle.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof); err != nil || !observed {
			_ = tx.Rollback()
			t.Fatalf("observe %s/%s created=%t error=%v", spec.boundary, spec.component, observed, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, accepted.Operation.JobID); err != nil {
		t.Fatal(err)
	}
	return accepted.Operation
}

func (f *routerBindingFixture) completeLaterStopStart(t *testing.T, key string) {
	t.Helper()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, f.job.ID); err != nil {
		t.Fatal(err)
	}
	f.completeOperation(t, applifecycle.Stop, applifecycle.BaselineRunning, 1, key+"-stop")
	f.completeOperation(t, applifecycle.Start, applifecycle.BaselineStopped, 2, key+"-start")
	if _, err := f.db.Exec(`UPDATE jobs SET status='queued',phase='queued',attempt=0,started_at=NULL,finished_at=NULL,updated_at=? WHERE id=?`, stamp, f.job.ID); err != nil {
		t.Fatal(err)
	}
}

func (f *routerBindingFixture) completeLaterRestart(t *testing.T, key string) {
	t.Helper()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, f.job.ID); err != nil {
		t.Fatal(err)
	}
	f.completeOperation(t, applifecycle.Restart, applifecycle.BaselineRunning, 1, key)
	if _, err := f.db.Exec(`UPDATE jobs SET status='queued',phase='queued',attempt=0,started_at=NULL,finished_at=NULL,updated_at=? WHERE id=?`, stamp, f.job.ID); err != nil {
		t.Fatal(err)
	}
}

func (f *routerBindingFixture) prepareRunningPins(t *testing.T, stamp string) applifecycle.GeneratedLifecyclePinsV1 {
	t.Helper()
	planID, releaseID, deploymentID, deployJobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := f.db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,0,'',?,?,'accepted',?,?)`,
		planID, f.appID, "apps/"+f.appID+"/plan.json", strings.Repeat("1", 64), strings.Repeat("2", 64),
		strings.Repeat("3", 64), f.actorID, stamp, f.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, f.appID, stamp,
		strings.Repeat("4", 64), strings.Repeat("5", 64), planID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,'{}',?,?)`, deployJobID, f.appID, f.actorID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, f.appID, releaseID, deployJobID, planID); err != nil {
		t.Fatal(err)
	}
	runtime := generatedruntimestate.New(f.db)
	value, created, err := runtime.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: f.appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"},
	})
	if err != nil || !created {
		t.Fatalf("runtime begin=%#v created=%t error=%v", value, created, err)
	}
	artifactID := uuid.NewString()
	containerID := strings.Repeat("6", 64)
	definition := strings.Repeat("7", 64)
	image := "sha256:" + strings.Repeat("8", 64)
	if _, err := f.db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at)
		VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, "api", definition, image, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetImageReady(context.Background(), f.appID, deploymentID, "api", artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerStarting(context.Background(), f.appID, deploymentID, "api", "rig-api-retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerRunning(context.Background(), f.appID, deploymentID, "api", containerID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AdvanceComponent(context.Background(), f.appID, deploymentID, "api", generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(context.Background(), f.appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtime.SwitchActive(context.Background(), f.appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("active head=%#v changed=%t error=%v", head, changed, err)
	}
	value, err = runtime.Advance(context.Background(), f.appID, deploymentID, value.Phase, generatedruntimestate.PhaseDraining, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Advance(context.Background(), f.appID, deploymentID, value.Phase, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE deployments SET status='succeeded',started_at=?,finished_at=? WHERE id=?`, stamp, stamp, deploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, deployJobID); err != nil {
		t.Fatal(err)
	}
	return applifecycle.GeneratedLifecyclePinsV1{
		Version: 1, DeploymentID: deploymentID, ReleaseID: releaseID, Slot: head.Slot, ActiveGeneration: head.Generation,
		PlanRevision:        applifecycle.RevisionRef{ID: planID, Number: 1},
		Configuration:       applifecycle.ConfigurationRef{PlanRevision: applifecycle.RevisionRef{ID: planID, Number: 1}, RuntimeComponents: []string{"api"}},
		BaselineDisposition: applifecycle.BaselineRunning,
		Components: []applifecycle.RetainedComponent{{
			Name: "api", Role: "service", Technology: "node", ContainerID: containerID, ContainerName: "rig-api-retained",
			ArtifactID: artifactID, ImageContentID: image, BuildDefinitionDigest: definition, NetworkName: "rig", NetworkAlias: "api",
			InternalPort: 8080, WorkingDirectory: "/app", RunCommandDigest: strings.Repeat("9", 64),
			Limits: applifecycle.RetainedLimits{MemoryBytes: 128 << 20, MemorySwapBytes: 256 << 20, CPUQuota: 50000,
				CPUPeriod: 100000, PIDsLimit: 128, TmpfsBytes: 16 << 20, LogDriver: "local", LogMaxSize: "10m", LogMaxFiles: 3, HardeningPolicyVersion: "v1"},
		}},
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind", SelectedLineageGeneration: 1,
			SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("a", 64),
			ProtectedFormatVersion: 2, TargetRecordDigest: strings.Repeat("b", 64)},
	}
}

type routerEffectSpec struct {
	boundary  applifecycle.Boundary
	component string
	mode      applifecycle.EffectMode
	outcome   applifecycle.Outcome
}

func routerEffectSpecs(operation applifecycle.Operation) []routerEffectSpec {
	component := operation.Pins.Components[0].Name
	var specs []routerEffectSpec
	if !(operation.Action == applifecycle.Start && operation.Pins.BaselineDisposition == applifecycle.BaselineRunning) {
		mode := applifecycle.EffectCommand
		if operation.Pins.BaselineDisposition == applifecycle.BaselineStopped {
			mode = applifecycle.EffectObservationOnly
		}
		specs = append(specs, routerEffectSpec{applifecycle.BoundaryWithdraw, "", mode, applifecycle.OutcomeRoutesAbsent})
	}
	if operation.Action == applifecycle.Stop || (operation.Action == applifecycle.Restart && operation.Pins.BaselineDisposition == applifecycle.BaselineRunning) {
		mode := applifecycle.EffectCommand
		if operation.Pins.BaselineDisposition == applifecycle.BaselineStopped {
			mode = applifecycle.EffectObservationOnly
		}
		specs = append(specs, routerEffectSpec{applifecycle.BoundaryStop, component, mode, applifecycle.OutcomeContainerStopped})
	}
	if (operation.Action == applifecycle.Start && operation.Pins.BaselineDisposition == applifecycle.BaselineStopped) || operation.Action == applifecycle.Restart {
		specs = append(specs, routerEffectSpec{applifecycle.BoundaryStart, component, applifecycle.EffectCommand, applifecycle.OutcomeContainerRunning})
	}
	if operation.Action == applifecycle.Start || operation.Action == applifecycle.Restart {
		specs = append(specs,
			routerEffectSpec{applifecycle.BoundaryHealth, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeHealthy},
			routerEffectSpec{applifecycle.BoundaryPublish, "", applifecycle.EffectCommand, applifecycle.OutcomeServing},
		)
	}
	complete := applifecycle.OutcomeCompleteRunning
	if operation.Action == applifecycle.Stop {
		complete = applifecycle.OutcomeCompleteStopped
	}
	return append(specs, routerEffectSpec{applifecycle.BoundaryComplete, "", applifecycle.EffectObservationOnly, complete})
}

func newBoundRouter(t *testing.T, fixture *routerBindingFixture, deploymentRepository deploymentReader,
	plans planReader, releases releaseReader, compose, generated BoundExecutor,
) (*Router, *recordingBindings) {
	t.Helper()
	bindings := &recordingBindings{delegate: fixture.bindings}
	router, err := New(deploymentRepository, plans, releases, compose, generated, bindings, clearRebindFence)
	if err != nil {
		t.Fatal(err)
	}
	return router, bindings
}

func generatedRouterInput(job jobs.Job) jobs.Job {
	job.Input, _ = json.Marshal(jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent,
		ExpectedPlanRevisionID: uuid.NewString(), ExpectedPlanRevisionNumber: 1})
	return job
}

func TestRouterRequiresFenceAndBindingDependencies(t *testing.T) {
	fixture := newRouterBindingFixture(t, false)
	dependencies := &routerDeployments{}
	plans := &routerPlans{}
	releases := &routerReleases{}
	if router, err := New(dependencies, plans, releases, &recordingExecutor{}, nil, fixture.bindings, nil); err == nil || router != nil {
		t.Fatalf("missing fence router=%v error=%v", router, err)
	}
	if router, err := New(dependencies, plans, releases, &recordingExecutor{}, nil, nil, clearRebindFence); err == nil || router != nil {
		t.Fatalf("missing bindings router=%v error=%v", router, err)
	}
	var typedNil *recordingBindings
	if router, err := New(dependencies, plans, releases, &recordingExecutor{}, nil, typedNil, clearRebindFence); err == nil || router != nil {
		t.Fatalf("typed nil bindings router=%v error=%v", router, err)
	}
	var typedNilExecutor *recordingExecutor
	if router, err := New(dependencies, plans, releases, typedNilExecutor, nil, fixture.bindings, clearRebindFence); err == nil || router != nil {
		t.Fatalf("typed nil delegates router=%v error=%v", router, err)
	}
}

func TestRouterFenceAndCancellationRefuseBeforeBindingRead(t *testing.T) {
	fixture := newRouterBindingFixture(t, false)
	for _, test := range []struct {
		name         string
		ctx          func() context.Context
		fence        func(context.Context) error
		wantCanceled bool
	}{
		{name: "fence", ctx: context.Background, fence: func(context.Context) error { return errors.New("held") }},
		{name: "already canceled", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, fence: clearRebindFence, wantCanceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bindings := &recordingBindings{delegate: fixture.bindings}
			deploymentRepository := &routerDeployments{}
			router, err := New(deploymentRepository, &routerPlans{}, &routerReleases{}, &recordingExecutor{}, nil, bindings, test.fence)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Execute(test.ctx(), fixture.job, noopReporter{})
			if test.wantCanceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
			} else {
				var executionErr *jobs.ExecutionError
				if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
					t.Fatalf("fence error=%v", err)
				}
			}
			if bindings.reads != 0 || deploymentRepository.calls != 0 {
				t.Fatalf("reads=%d deployments=%d", bindings.reads, deploymentRepository.calls)
			}
		})
	}
}

func TestRouterBindingInitialRefusalsPreserveTypedCauses(t *testing.T) {
	fixture := newRouterBindingFixture(t, false)
	tests := []struct {
		name           string
		readErr        error
		checkErr       error
		classification deploymentbinding.Classification
		want           error
	}{
		{name: "missing", readErr: deploymentbinding.ErrBindingNotFound, want: deploymentbinding.ErrBindingNotFound},
		{name: "invalid", checkErr: deploymentbinding.ErrInvalidBinding, want: deploymentbinding.ErrInvalidBinding},
		{name: "unresolved", checkErr: deploymentbinding.ErrLifecycleUnresolved, want: deploymentbinding.ErrLifecycleUnresolved},
		{name: "unavailable", checkErr: deploymentbinding.ErrUnavailable, want: deploymentbinding.ErrUnavailable},
		{name: "stale", classification: deploymentbinding.ClassificationStale},
		{name: "unknown", classification: deploymentbinding.Classification(99), want: deploymentbinding.ErrInvalidConfiguration},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings := &recordingBindings{delegate: fixture.bindings, readErr: test.readErr, checkErr: test.checkErr, classification: test.classification}
			deploymentRepository := &routerDeployments{}
			plans := &routerPlans{}
			releases := &routerReleases{}
			compose, generated := &recordingExecutor{}, &recordingExecutor{}
			router, err := New(deploymentRepository, plans, releases, compose, generated, bindings, clearRebindFence)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Execute(context.Background(), fixture.job, noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("error=%v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want cause=%v", err, test.want)
			}
			if deploymentRepository.calls != 0 || plans.gets != 0 || releases.calls != 0 || compose.calls != 0 || generated.calls != 0 {
				t.Fatalf("later calls deployment=%d plans=%d releases=%d compose=%d generated=%d", deploymentRepository.calls, plans.gets, releases.calls, compose.calls, generated.calls)
			}
		})
	}
}

func TestRouterRejectsBindingJobAndApplicationIdentityMismatch(t *testing.T) {
	for _, mismatch := range []string{"application", "job"} {
		t.Run(mismatch, func(t *testing.T) {
			fixture := newRouterBindingFixture(t, false)
			job := fixture.job
			deploymentRepository := &routerDeployments{}
			router, bindings := newBoundRouter(t, fixture, deploymentRepository, &routerPlans{}, &routerReleases{}, &recordingExecutor{}, nil)
			if mismatch == "application" {
				job.ResourceID = uuid.NewString()
			} else {
				job.ID = uuid.NewString()
				bindings.readBinding = &fixture.binding
			}
			_, err := router.Execute(context.Background(), job, noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || !errors.Is(err, deploymentbinding.ErrInvalidBinding) || deploymentRepository.calls != 0 {
				t.Fatalf("error=%v deployment calls=%d", err, deploymentRepository.calls)
			}
		})
	}
}

func TestRouterRealCurrentBindingsDelegateWithOriginalValues(t *testing.T) {
	type contextKey string
	for _, running := range []bool{false, true} {
		for _, strategy := range []string{"compose", "generated"} {
			t.Run(map[bool]string{false: "legacy", true: "running"}[running]+"/"+strategy, func(t *testing.T) {
				fixture := newRouterBindingFixture(t, running)
				job := fixture.job
				plans := &routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: fixture.appID}}
				if strategy == "generated" {
					job = generatedRouterInput(job)
				}
				compose, generated := &recordingExecutor{}, &recordingExecutor{}
				deploymentRepository := &routerDeployments{}
				router, bindings := newBoundRouter(t, fixture, deploymentRepository, plans, &routerReleases{}, compose, generated)
				ctx := context.WithValue(context.Background(), contextKey("authority"), "original")
				reporter := &noopReporter{}
				if _, err := router.Execute(ctx, job, reporter); err != nil {
					t.Fatal(err)
				}
				expectedClaim, err := deploymentexecution.NewClaim(fixture.binding, job.ID, job.ResourceID, job.Attempt)
				if err != nil {
					t.Fatal(err)
				}
				selected := compose
				if strategy == "generated" {
					selected = generated
				}
				nonselected := generated
				if strategy == "generated" {
					nonselected = compose
				}
				if selected.calls != 1 || nonselected.calls != 0 || bindings.reads != 1 || bindings.checks != 2 || deploymentRepository.calls != 1 ||
					len(selected.contexts) != 1 || selected.contexts[0] != ctx || !reflect.DeepEqual(selected.jobs[0], job) || selected.reporters[0] != reporter ||
					len(selected.claims) != 1 || !reflect.DeepEqual(selected.claims[0], expectedClaim) ||
					len(bindings.readContexts) != 1 || bindings.readContexts[0] != ctx || len(bindings.checkContexts) != 2 ||
					bindings.checkContexts[0] != ctx || bindings.checkContexts[1] != ctx ||
					!bindings.checked[0].Equal(fixture.binding) || !bindings.checked[1].Equal(fixture.binding) {
					t.Fatalf("delegate=%d reads=%d checks=%d deployments=%d", selected.calls, bindings.reads, bindings.checks, deploymentRepository.calls)
				}
				if selected.contexts[0].Value(contextKey("authority")) != "original" || bindings.readContexts[0].Value(contextKey("authority")) != "original" ||
					bindings.checkContexts[0].Value(contextKey("authority")) != "original" || bindings.checkContexts[1].Value(contextKey("authority")) != "original" {
					t.Fatal("caller context value was not preserved")
				}
			})
		}
	}
}

func TestRouterSameStateRevisionDriftRefusesInitialAndLateDelegation(t *testing.T) {
	for _, transition := range []string{"restart", "stop-start"} {
		for _, stage := range []string{"initial", "late"} {
			for _, strategy := range []string{"compose", "generated"} {
				t.Run(transition+"/"+stage+"/"+strategy, func(t *testing.T) {
					fixture := newRouterBindingFixture(t, true)
					drift := func() {
						if transition == "restart" {
							fixture.completeLaterRestart(t, transition+"-"+stage+"-drift")
						} else {
							fixture.completeLaterStopStart(t, transition+"-"+stage+"-drift")
						}
					}
					if stage == "initial" {
						drift()
					}
					job := fixture.job
					if strategy == "generated" {
						job = generatedRouterInput(job)
					}
					compose, generated := &recordingExecutor{}, &recordingExecutor{}
					deploymentRepository := &routerDeployments{}
					router, bindings := newBoundRouter(t, fixture, deploymentRepository,
						&routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: fixture.appID}}, &routerReleases{}, compose, generated)
					if stage == "late" {
						bindings.afterCheck = func(check int) {
							if check == 1 {
								drift()
							}
						}
					}
					_, err := router.Execute(context.Background(), job, noopReporter{})
					var executionErr *jobs.ExecutionError
					if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || compose.calls != 0 || generated.calls != 0 {
						t.Fatalf("error=%v compose=%d generated=%d", err, compose.calls, generated.calls)
					}
					if stage == "initial" && deploymentRepository.calls != 0 {
						t.Fatalf("initial drift prepared deployment %d times", deploymentRepository.calls)
					}
					if stage == "late" && (deploymentRepository.calls != 1 || bindings.checks != 2) {
						t.Fatalf("late drift deployment=%d checks=%d", deploymentRepository.calls, bindings.checks)
					}
				})
			}
		}
	}
}

type preparedDeploymentSnapshot struct {
	id                    string
	appID                 string
	jobID                 string
	status                string
	configurationMode     string
	provenanceInitialized int64
	runtimeStrategy       string
	releaseID             string
	planRevisionID        string
	planRevisionNumber    int64
}

func readPreparedDeployment(t *testing.T, db *sql.DB, jobID string) preparedDeploymentSnapshot {
	t.Helper()
	var value preparedDeploymentSnapshot
	if err := db.QueryRow(`SELECT id,app_id,job_id,status,configuration_mode,provenance_initialized,
		COALESCE(runtime_strategy,''),COALESCE(release_id,''),COALESCE(deployment_plan_revision_id,''),COALESCE(deployment_plan_revision_number,0)
		FROM deployments WHERE job_id=?`, jobID).Scan(
		&value.id, &value.appID, &value.jobID, &value.status, &value.configurationMode, &value.provenanceInitialized,
		&value.runtimeStrategy, &value.releaseID, &value.planRevisionID, &value.planRevisionNumber,
	); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRouterLateBindingRefusalRetainsPreparingEvidence(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*testing.T, *routerBindingFixture, *recordingBindings)
		wantCause error
	}{
		{
			name: "same-state revision drift",
			configure: func(t *testing.T, fixture *routerBindingFixture, bindings *recordingBindings) {
				bindings.afterCheck = func(check int) {
					if check == 1 {
						fixture.completeLaterStopStart(t, "retained-preparing-drift")
					}
				}
			},
		},
		{
			name: "typed authority failure",
			configure: func(_ *testing.T, _ *routerBindingFixture, bindings *recordingBindings) {
				bindings.afterCheck = func(check int) {
					if check == 1 {
						bindings.checkErr = deploymentbinding.ErrUnavailable
					}
				}
			},
			wantCause: deploymentbinding.ErrUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRouterBindingFixture(t, true)
			job := generatedRouterInput(fixture.job)
			var deploymentCountBefore int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE job_id=?`, job.ID).Scan(&deploymentCountBefore); err != nil || deploymentCountBefore != 0 {
				t.Fatalf("deployment count before=%d error=%v", deploymentCountBefore, err)
			}

			executor := &recordingExecutor{}
			router, bindings := newBoundRouter(t, fixture, deployments.New(fixture.db), &routerPlans{}, &routerReleases{}, nil, executor)
			test.configure(t, fixture, bindings)
			var preparedBeforeRefusal preparedDeploymentSnapshot
			var jobBefore jobs.Job
			var eventsBefore []jobs.Event
			var bindingBefore deploymentbinding.Binding
			bindings.beforeCheck = func(check int) {
				if check == 2 {
					preparedBeforeRefusal = readPreparedDeployment(t, fixture.db, job.ID)
					var err error
					jobBefore, err = fixture.jobs.Get(job.ID)
					if err != nil {
						t.Fatal(err)
					}
					eventsBefore, err = fixture.jobs.Events(job.ID, 0)
					if err != nil {
						t.Fatal(err)
					}
					bindingBefore, err = fixture.bindings.Read(context.Background(), job.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}

			_, err := router.Execute(context.Background(), job, noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("error=%v", err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error=%v want cause=%v", err, test.wantCause)
			}
			if executor.calls != 0 || bindings.reads != 1 || bindings.checks != 2 {
				t.Fatalf("executor=%d reads=%d checks=%d", executor.calls, bindings.reads, bindings.checks)
			}
			preparedAfterRefusal := readPreparedDeployment(t, fixture.db, job.ID)
			if preparedBeforeRefusal.status != string(deployments.Preparing) || preparedBeforeRefusal.configurationMode != string(jobs.ConfigurationCurrent) ||
				preparedBeforeRefusal.appID != fixture.appID || preparedBeforeRefusal.jobID != job.ID ||
				preparedBeforeRefusal.provenanceInitialized != 0 || !reflect.DeepEqual(preparedAfterRefusal, preparedBeforeRefusal) {
				t.Fatalf("prepared before=%#v after=%#v", preparedBeforeRefusal, preparedAfterRefusal)
			}
			jobAfter, err := fixture.jobs.Get(job.ID)
			if err != nil || !reflect.DeepEqual(jobAfter, jobBefore) {
				t.Fatalf("job before=%#v after=%#v error=%v", jobBefore, jobAfter, err)
			}
			eventsAfter, err := fixture.jobs.Events(job.ID, 0)
			if err != nil || !reflect.DeepEqual(eventsAfter, eventsBefore) {
				t.Fatalf("events before=%#v after=%#v error=%v", eventsBefore, eventsAfter, err)
			}
			bindingAfter, err := fixture.bindings.Read(context.Background(), job.ID)
			if err != nil || !bindingAfter.Equal(bindingBefore) {
				t.Fatalf("binding changed error=%v", err)
			}
			var bindingsCount, deploymentsCount int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM application_deployment_lifecycle_bindings WHERE job_id=?`, job.ID).Scan(&bindingsCount); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE job_id=?`, job.ID).Scan(&deploymentsCount); err != nil {
				t.Fatal(err)
			}
			if bindingsCount != 1 || deploymentsCount != 1 {
				t.Fatalf("bindings=%d deployments=%d", bindingsCount, deploymentsCount)
			}
		})
	}
}

func TestRouterHistoricalCorruptionWinsOverNewerHead(t *testing.T) {
	fixture := newRouterBindingFixture(t, true)
	fixture.completeLaterStopStart(t, "newer-head")
	if _, err := fixture.db.Exec(`DROP TRIGGER application_desired_lifecycle_transition_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE application_desired_lifecycle_transitions SET actor_id=? WHERE app_id=? AND revision_number=1`, uuid.NewString(), fixture.appID); err != nil {
		t.Fatal(err)
	}
	deploymentRepository := &routerDeployments{}
	router, _ := newBoundRouter(t, fixture, deploymentRepository, &routerPlans{}, &routerReleases{}, &recordingExecutor{}, nil)
	_, err := router.Execute(context.Background(), fixture.job, noopReporter{})
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || !errors.Is(err, deploymentbinding.ErrInvalidBinding) || deploymentRepository.calls != 0 {
		t.Fatalf("error=%v deployment calls=%d", err, deploymentRepository.calls)
	}
}

func TestRouterSuccessfulBindingCancellationStopsBeforePreparation(t *testing.T) {
	for _, stage := range []string{"read", "first_check"} {
		for _, strategy := range []string{"compose", "generated"} {
			t.Run(stage+"/"+strategy, func(t *testing.T) {
				fixture := newRouterBindingFixture(t, false)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				job := fixture.job
				if strategy == "generated" {
					job = generatedRouterInput(job)
				}
				deploymentRepository := &routerDeployments{}
				plans := &routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: fixture.appID}}
				releases := &routerReleases{}
				compose, generated := &recordingExecutor{}, &recordingExecutor{}
				router, bindings := newBoundRouter(t, fixture, deploymentRepository, plans, releases, compose, generated)
				wantChecks := 0
				if stage == "read" {
					bindings.afterRead = cancel
				} else {
					wantChecks = 1
					bindings.afterCheck = func(check int) {
						if check == 1 {
							cancel()
						}
					}
				}
				result, err := router.Execute(ctx, job, noopReporter{})
				if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, jobs.ExecutionResult{}) ||
					bindings.reads != 1 || bindings.checks != wantChecks || deploymentRepository.calls != 0 ||
					plans.gets != 0 || plans.revisions != 0 || releases.calls != 0 || compose.calls != 0 || generated.calls != 0 {
					t.Fatalf("error=%v result=%#v reads=%d checks=%d deployments=%d plans=%d/%d releases=%d delegates=%d/%d",
						err, result, bindings.reads, bindings.checks, deploymentRepository.calls, plans.gets,
						plans.revisions, releases.calls, compose.calls, generated.calls)
				}
			})
		}
	}
}

func TestRouterCancellationDuringSecondBindingValidationPreservesCause(t *testing.T) {
	fixture := newRouterBindingFixture(t, false)
	type contextKey string
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey("authority"), "original"))
	defer cancel()
	deploymentRepository := &routerDeployments{}
	compose := &recordingExecutor{}
	router, bindings := newBoundRouter(t, fixture, deploymentRepository,
		&routerPlans{head: deploymentplans.DeploymentPlanRevision{AppID: fixture.appID}}, &routerReleases{}, compose, nil)
	bindings.beforeCheck = func(check int) {
		if check == 2 {
			cancel()
		}
	}
	_, err := router.Execute(ctx, fixture.job, noopReporter{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, deploymentbinding.ErrCanceled) || deploymentRepository.calls != 1 || compose.calls != 0 ||
		len(bindings.readContexts) != 1 || bindings.readContexts[0] != ctx || len(bindings.checkContexts) != 2 ||
		bindings.checkContexts[0] != ctx || bindings.checkContexts[1] != ctx ||
		bindings.readContexts[0].Value(contextKey("authority")) != "original" || bindings.checkContexts[1].Value(contextKey("authority")) != "original" {
		t.Fatalf("error=%v deployments=%d compose=%d", err, deploymentRepository.calls, compose.calls)
	}
}

func TestRouterPreservesReleaseAndDeploymentStrategyProvenance(t *testing.T) {
	fixture := newRouterBindingFixture(t, false)
	planID := uuid.NewString()
	generatedPlan := deploymentplans.DeploymentPlanRevision{ID: planID, AppID: fixture.appID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode}}
	t.Run("accepted generated head without reviewed input", func(t *testing.T) {
		compose, generated := &recordingExecutor{}, &recordingExecutor{}
		router, _ := newBoundRouter(t, fixture, &routerDeployments{}, &routerPlans{head: generatedPlan}, &routerReleases{}, compose, generated)
		if _, err := router.Execute(context.Background(), fixture.job, noopReporter{}); err != nil || compose.calls != 0 || generated.calls != 1 {
			t.Fatalf("error=%v compose=%d generated=%d", err, compose.calls, generated.calls)
		}
	})
	t.Run("reviewed generated input ignores newer compose head", func(t *testing.T) {
		job := generatedRouterInput(fixture.job)
		composeHead := deploymentplans.DeploymentPlanRevision{ID: uuid.NewString(), AppID: fixture.appID, RevisionNumber: 2, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyCompose}}
		compose, generated := &recordingExecutor{}, &recordingExecutor{}
		router, _ := newBoundRouter(t, fixture, &routerDeployments{}, &routerPlans{head: composeHead}, &routerReleases{}, compose, generated)
		if _, err := router.Execute(context.Background(), job, noopReporter{}); err != nil || compose.calls != 0 || generated.calls != 1 {
			t.Fatalf("error=%v compose=%d generated=%d", err, compose.calls, generated.calls)
		}
	})
	t.Run("release revision", func(t *testing.T) {
		job := fixture.job
		job.Input, _ = json.Marshal(jobs.DeploymentInput{ReleaseID: routerReleaseID, ConfigurationMode: jobs.ConfigurationCurrent})
		release := releasesnapshot.Release{ID: routerReleaseID, AppID: fixture.appID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1}
		generated := &recordingExecutor{}
		router, _ := newBoundRouter(t, fixture, &routerDeployments{}, &routerPlans{revision: generatedPlan}, &routerReleases{release: release}, &recordingExecutor{}, generated)
		if _, err := router.Execute(context.Background(), job, noopReporter{}); err != nil || generated.calls != 1 {
			t.Fatalf("error=%v generated=%d", err, generated.calls)
		}
	})
	t.Run("persisted compose", func(t *testing.T) {
		compose := &recordingExecutor{}
		deploymentRepository := &routerDeployments{deployment: deployments.Deployment{RuntimeStrategy: deployments.RuntimeCompose, ProvenanceInitialized: true}}
		router, _ := newBoundRouter(t, fixture, deploymentRepository, &routerPlans{err: errors.New("must not read plan")}, &routerReleases{err: errors.New("must not read release")}, compose, nil)
		if _, err := router.Execute(context.Background(), fixture.job, noopReporter{}); err != nil || compose.calls != 1 {
			t.Fatalf("error=%v compose=%d", err, compose.calls)
		}
	})
	t.Run("generated unavailable never falls back", func(t *testing.T) {
		job := fixture.job
		job.Input, _ = json.Marshal(jobs.DeploymentInput{ReleaseID: routerReleaseID, ConfigurationMode: jobs.ConfigurationCurrent})
		release := releasesnapshot.Release{ID: routerReleaseID, AppID: fixture.appID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1}
		compose := &recordingExecutor{}
		router, _ := newBoundRouter(t, fixture, &routerDeployments{}, &routerPlans{revision: generatedPlan}, &routerReleases{release: release}, compose, nil)
		_, err := router.Execute(context.Background(), job, noopReporter{})
		var executionErr *jobs.ExecutionError
		if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" || compose.calls != 0 {
			t.Fatalf("error=%v compose=%d", err, compose.calls)
		}
	})
}

func TestRouterRetainsExistingRoutingFailureCodes(t *testing.T) {
	fixture := newRouterBindingFixture(t, false)
	storageErr := errors.New("repository unavailable")
	malformedReleaseJob := fixture.job
	malformedReleaseJob.Input, _ = json.Marshal(jobs.DeploymentInput{ReleaseID: routerReleaseID, ConfigurationMode: jobs.ConfigurationCurrent})
	pinnedReleaseJob := fixture.job
	pinnedReleaseJob.Input, _ = json.Marshal(jobs.DeploymentInput{ReleaseID: routerReleaseID, ConfigurationMode: jobs.ConfigurationCurrent})
	planID := uuid.NewString()
	pinnedRelease := releasesnapshot.Release{ID: routerReleaseID, AppID: fixture.appID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1}
	for _, test := range []struct {
		name       string
		job        jobs.Job
		plans      *routerPlans
		releases   *routerReleases
		deployment deployments.Deployment
		want       string
	}{
		{name: "plan read", job: fixture.job, plans: &routerPlans{err: storageErr}, releases: &routerReleases{}, want: "internal_error"},
		{name: "release read", job: pinnedReleaseJob, plans: &routerPlans{}, releases: &routerReleases{err: storageErr}, want: "internal_error"},
		{name: "revision read", job: pinnedReleaseJob, plans: &routerPlans{err: storageErr}, releases: &routerReleases{release: pinnedRelease}, want: "internal_error"},
		{name: "invalid persisted strategy", job: generatedRouterInput(fixture.job), plans: &routerPlans{}, releases: &routerReleases{}, deployment: deployments.Deployment{ProvenanceInitialized: true, RuntimeStrategy: deployments.RuntimeCompose}, want: "invalid_source"},
		{name: "malformed release", job: malformedReleaseJob, plans: &routerPlans{}, releases: &routerReleases{release: releasesnapshot.Release{ID: "different", AppID: fixture.appID}}, want: "invalid_source"},
		{name: "mismatched plan", job: pinnedReleaseJob, plans: &routerPlans{revision: deploymentplans.DeploymentPlanRevision{ID: planID, AppID: uuid.NewString(), RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode}}}, releases: &routerReleases{release: pinnedRelease}, want: "invalid_source"},
		{name: "unsupported strategy", job: fixture.job, plans: &routerPlans{head: deploymentplans.DeploymentPlanRevision{ID: planID, AppID: fixture.appID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: "unsupported"}}}, releases: &routerReleases{}, want: "invalid_source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			compose, generated := &recordingExecutor{}, &recordingExecutor{}
			router, _ := newBoundRouter(t, fixture, &routerDeployments{deployment: test.deployment}, test.plans, test.releases, compose, generated)
			_, err := router.Execute(context.Background(), test.job, noopReporter{})
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != test.want || compose.calls != 0 || generated.calls != 0 {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
