package generatedexecutor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
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
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtimeexecutor"
	"github.com/hostd/hostd/internal/sourceconnections"
	"github.com/hostd/hostd/internal/sourceinspection"
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

func TestGeneratedNetworkRefusalAfterFirstRealCandidateRetainsWholeAggregate(t *testing.T) {
	fixture := newCapacityResumeFixture(t)
	inspection, err := sourceinspection.InspectLocal(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	planValue, _, err := deploymentplans.AcceptSetup(inspection.Analysis, projectanalysis.DeploymentSetup{Components: []projectanalysis.SetupComponent{
		{
			ID: "api", Technology: "node", RootDirectory: ".", PackageManager: "npm", NodeVersion: "22",
			InstallCommand: "npm ci", StartCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health",
		},
		{
			ID: "web", Technology: "static", RootDirectory: ".", PackageManager: "npm", NodeVersion: "22",
			InstallCommand: "npm ci", BuildCommand: "npm run build", OutputDirectory: "public", InternalPort: 3001, HealthProbe: "/health",
		},
	}}, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.plans.Replace(context.Background(), fixture.app.ID, fixture.actorID, deploymentplans.ReplaceInput{
		ExpectedRevisionNumber: fixture.plan.RevisionNumber, Plan: planValue,
	})
	if err != nil {
		t.Fatal(err)
	}
	marker := "retained"
	configuration, err := fixture.configurations.ReplaceScoped(context.Background(), fixture.app.ID, fixture.actorID, appconfig.ScopedReplaceInput{
		ExpectedRevisionNumber: fixture.configuration.RevisionNumber,
		PlanRevisionID:         plan.ID, PlanRevisionNumber: plan.RevisionNumber,
		Components: []appconfig.ComponentTarget{{Name: "api", Role: "server"}, {Name: "web", Role: "static"}},
		Entries: []appconfig.ScopedValueInput{
			{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: "api", Key: "MARKER"}, Sensitivity: appconfig.SensitivityPublic, Value: &marker},
			{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseBuild, Component: "web", Key: "MARKER"}, Sensitivity: appconfig.SensitivityPublic, Value: &marker},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err := fixture.materializer.MaterializeLocal(context.Background(), fixture.app.ID, fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make([]generatedimage.Artifact, 0, len(plan.Plan.Components))
	for index, component := range plan.Plan.Components {
		artifact, created, err := fixture.artifactStore.Begin(context.Background(), generatedimage.BeginArtifactInput{
			ReleaseID: release.ID, DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
			ComponentID: component.Name, CompilerVersion: "network-retention/test", BuildDefinitionDigest: strings.Repeat(string(rune('c'+index)), 64),
		})
		if err != nil || !created {
			t.Fatalf("begin %s artifact: created=%t error=%v", component.Name, created, err)
		}
		artifact, err = fixture.artifactStore.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat(string(rune('d'+index)), 64))
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}
	fixture.compiler.artifacts = artifacts
	fixture.capacity.Set(generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 1 << 30, DiskAvailableBytes: 512 << 20})
	runner := &realNetworkRetentionRunner{db: fixture.db, specs: make(map[string]realNetworkRetentionSpec), containers: make(map[string]realNetworkRetentionSpec)}
	for index, component := range plan.Plan.Components {
		artifact := artifacts[index]
		runner.specs[artifact.ImageContentID] = realNetworkRetentionSpec{
			appID: fixture.app.ID, releaseID: release.ID, deploymentPlanID: plan.ID,
			artifactID: artifact.ID, component: component, imageID: artifact.ImageContentID,
			definition: artifact.BuildDefinitionDigest,
		}
	}
	environment := &realNetworkRetentionEnvironment{path: filepath.Join(fixture.dataRoot, "network-retention.env")}
	realRuntime, err := generatedruntime.NewEngine(runner, environment, fixture.capacity, fixture.deployments, generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(fixture.dataRoot, "docker.exe"), DockerConfigDirectory: fixture.directories.DockerConfigDirectory,
		WorkingDirectory: fixture.directories.WorkingDirectory, CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := NewAuthorizationGate(fixture.deployments, fixture.plans, realRuntime)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(fixture.applications, fixture.materializer, fixture.configurations, fixture.deployments, fixture.plans,
		fixture.compiler, fixture.artifactStore, fixture.runtimeState, realRuntime, authorization, fixture.routes, fixture.migrations, Options{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: fixture.app.ID, RequestedBy: fixture.actorID, IdempotencyKey: "network-retention",
		Input: jobs.DeploymentInput{
			ConfigurationMode:      jobs.ConfigurationCurrent,
			ExpectedPlanRevisionID: plan.ID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
			ExpectedConfigurationRevisionID: configuration.RevisionID, ExpectedConfigurationRevisionNumber: configuration.RevisionNumber,
		},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create retention job: created=%t error=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.jobs.Get(created.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, fixture.app.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	runner.jobID = job.ID
	ctx, releaseScope, err := fixture.admission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := releaseScope(); err != nil {
			t.Errorf("release retention scope: %v", err)
		}
	}()
	result, executeErr := executor.ExecuteBound(ctx, job, claim, &fakeReporter{})
	var executionErr *jobs.ExecutionError
	if result != (jobs.ExecutionResult{}) || !errors.As(executeErr, &executionErr) || executionErr.Code != "runtime_unavailable" ||
		!errors.Is(executeErr, deploymentexecution.ErrIdentityLost) || !generatedruntime.IsGeneratedNetworkBoundaryFailure(executeErr) {
		t.Fatalf("retention result=%#v error=%v", result, executeErr)
	}
	if runner.containerCreates != 1 || runner.containerStarts != 1 || runner.cleanupCalls != 0 {
		t.Fatalf("container creates=%d starts=%d cleanup calls=%d", runner.containerCreates, runner.containerStarts, runner.cleanupCalls)
	}
	values, err := fixture.deployments.List(context.Background(), fixture.app.ID, 10)
	if err != nil || len(values) != 1 || values[0].Status != deployments.Applying {
		t.Fatalf("retained main deployment=%+v error=%v", values, err)
	}
	runtimeDeployment, err := fixture.runtimeState.Get(context.Background(), fixture.app.ID, values[0].ID)
	if err != nil || runtimeDeployment.Phase != generatedruntimestate.PhaseStartingCandidate || len(runtimeDeployment.Components) != 2 ||
		runtimeDeployment.Components[0].State != generatedruntimestate.ComponentRunning || runtimeDeployment.Components[1].State != generatedruntimestate.ComponentStarting {
		t.Fatalf("retained runtime=%+v error=%v", runtimeDeployment, err)
	}
	if _, err := realRuntime.ReserveReplacement(context.Background(), 1); !generatedruntime.IsCode(err, generatedruntime.DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("retained aggregate allowed competitor: %v", err)
	}
}

func TestResumedGeneratedNetworkRiskRetainsRealCapacityBeforeExplicitRefusal(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    generatedruntimestate.ComponentState
		retain   bool
		complete bool
	}{
		{name: "name only", state: generatedruntimestate.ComponentStarting, retain: true},
		{name: "running candidate", state: generatedruntimestate.ComponentRunning, retain: true, complete: true},
		{name: "unknown state", state: generatedruntimestate.ComponentState("unknown"), retain: true},
		{name: "all unattempted", state: generatedruntimestate.ComponentImageReady},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCapacityResumeFixture(t)
			fixture.capacity.Set(generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 512 << 20, DiskAvailableBytes: 256 << 20})
			runner := &realNetworkRetentionRunner{
				db: fixture.db, jobID: "", networkCreated: true, invalidateOnNetworkInspect: true,
				specs: map[string]realNetworkRetentionSpec{fixture.compiler.artifacts[0].ImageContentID: {
					appID: fixture.app.ID, releaseID: fixture.release.ID, deploymentPlanID: fixture.plan.ID,
					artifactID: fixture.compiler.artifacts[0].ID, imageID: fixture.compiler.artifacts[0].ImageContentID,
					definition: fixture.compiler.artifacts[0].BuildDefinitionDigest, component: fixture.plan.Plan.Components[0],
				}},
				containers: make(map[string]realNetworkRetentionSpec),
			}
			realRuntime, err := generatedruntime.NewEngine(runner, &realNetworkRetentionEnvironment{path: filepath.Join(fixture.dataRoot, "resumed-network.env")}, fixture.capacity, fixture.deployments, generatedruntime.EngineOptions{
				DockerExecutable: filepath.Join(fixture.dataRoot, "docker.exe"), DockerConfigDirectory: fixture.directories.DockerConfigDirectory,
				WorkingDirectory: fixture.directories.WorkingDirectory, CommandTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			authorization, err := NewAuthorizationGate(fixture.deployments, fixture.plans, realRuntime)
			if err != nil {
				t.Fatal(err)
			}
			executor, err := NewExecutor(fixture.applications, fixture.materializer, fixture.configurations, fixture.deployments, fixture.plans,
				fixture.compiler, fixture.artifactStore, fixture.runtimeState, realRuntime, authorization, fixture.routes, fixture.migrations, Options{})
			if err != nil {
				t.Fatal(err)
			}
			created, err := fixture.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
				ApplicationID: fixture.app.ID, RequestedBy: fixture.actorID, IdempotencyKey: "resumed-network-" + test.name,
				Input: jobs.DeploymentInput{
					ConfigurationMode:      jobs.ConfigurationCurrent,
					ExpectedPlanRevisionID: fixture.plan.ID, ExpectedPlanRevisionNumber: fixture.plan.RevisionNumber,
					ExpectedConfigurationRevisionID: fixture.configuration.RevisionID, ExpectedConfigurationRevisionNumber: fixture.configuration.RevisionNumber,
				},
			}, jobs.DeploymentCreateHooks{})
			if err != nil || !created.Created {
				t.Fatalf("create resumed job: created=%t error=%v", created.Created, err)
			}
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := fixture.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
				t.Fatal(err)
			}
			deployment, _, err := fixture.deployments.GetOrCreateByJob(context.Background(), fixture.app.ID, created.Job.ID, "current")
			if err != nil {
				t.Fatal(err)
			}
			deployment, err = fixture.deployments.InitializeRuntime(context.Background(), fixture.app.ID, deployment.ID, fixture.release.ID,
				fixture.configuration.RevisionID, fixture.configuration.RevisionNumber, deployments.RuntimeGeneratedNode, fixture.plan.ID, fixture.plan.RevisionNumber)
			if err != nil {
				t.Fatal(err)
			}
			runtimeDeployment, wasCreated, err := fixture.runtimeState.Begin(context.Background(), generatedruntimestate.BeginInput{
				DeploymentID: deployment.ID, AppID: fixture.app.ID, ReleaseID: fixture.release.ID,
				DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber, ComponentNames: []string{"api"},
			})
			if err != nil || !wasCreated {
				t.Fatalf("begin resumed runtime: created=%t error=%v", wasCreated, err)
			}
			for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
				runtimeDeployment, err = fixture.runtimeState.Advance(context.Background(), fixture.app.ID, deployment.ID, runtimeDeployment.Phase, next, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			component, err := fixture.runtimeState.SetImageReady(context.Background(), fixture.app.ID, deployment.ID, "api", fixture.compiler.artifacts[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.state == generatedruntimestate.ComponentStarting || test.state == generatedruntimestate.ComponentRunning {
				description, err := generatedruntime.DescribeInactiveCandidate(fixture.app.ID, "api", "")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.runtimeState.SetContainerStarting(context.Background(), fixture.app.ID, deployment.ID, "api", description.ContainerName); err != nil {
					t.Fatal(err)
				}
				if test.complete {
					if _, err := fixture.runtimeState.SetContainerRunning(context.Background(), fixture.app.ID, deployment.ID, "api", strings.Repeat("a", 64)); err != nil {
						t.Fatal(err)
					}
				}
			} else if test.state != generatedruntimestate.ComponentImageReady {
				if _, err := fixture.db.Exec(`DROP TRIGGER generated_runtime_component_state_transition`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.db.Exec(`UPDATE generated_runtime_components SET state=? WHERE deployment_id=? AND component_name=?`, test.state, deployment.ID, component.Name); err != nil {
					t.Fatal(err)
				}
			}
			job, err := fixture.jobs.Get(created.Job.ID)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, fixture.app.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			runner.jobID = job.ID
			ctx, releaseScope, err := fixture.admission(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := releaseScope(); err != nil {
					t.Errorf("release resumed scope: %v", err)
				}
			}()
			initialReservation, err := authorization.Authorize(ctx, AuthorizationRequest{
				Claim: claim, AppID: fixture.app.ID, DeploymentID: deployment.ID, ReleaseID: fixture.release.ID,
				DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber,
				CandidateSlot: generatedruntime.Slot(runtimeDeployment.CandidateSlot), ComponentCount: 1,
			})
			if err != nil {
				t.Fatalf("seed recovered gate: %v", err)
			}
			initialReservation.Release()
			beforeMain, err := fixture.deployments.Get(context.Background(), fixture.app.ID, deployment.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeRuntime, err := fixture.runtimeState.Get(context.Background(), fixture.app.ID, deployment.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, executeErr := executor.ExecuteBound(ctx, job, claim, &fakeReporter{})
			if !generatedruntime.IsGeneratedNetworkBoundaryFailure(executeErr) || !errors.Is(executeErr, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("resumed refusal=%v", executeErr)
			}
			if runner.containerCreates != 0 || runner.containerStarts != 0 || runner.cleanupCalls != 0 {
				t.Fatalf("resumed refusal mutated containers: creates=%d starts=%d cleanup=%d", runner.containerCreates, runner.containerStarts, runner.cleanupCalls)
			}
			retainedMain, err := fixture.deployments.Get(context.Background(), fixture.app.ID, deployment.ID)
			beforeMainJSON, beforeJSONErr := json.Marshal(beforeMain)
			retainedMainJSON, retainedJSONErr := json.Marshal(retainedMain)
			if err != nil || beforeJSONErr != nil || retainedJSONErr != nil || string(retainedMainJSON) != string(beforeMainJSON) || retainedMain.ProvenanceInitialized != beforeMain.ProvenanceInitialized {
				t.Fatalf("resumed main evidence changed: before=%+v after=%+v error=%v", beforeMain, retainedMain, err)
			}
			retained, err := fixture.runtimeState.Get(context.Background(), fixture.app.ID, deployment.ID)
			if err != nil || !reflect.DeepEqual(retained, beforeRuntime) {
				t.Fatalf("resumed evidence changed: before=%+v after=%+v error=%v", beforeRuntime, retained, err)
			}
			competitor, competitorErr := realRuntime.ReserveReplacement(context.Background(), 1)
			if test.retain {
				if !generatedruntime.IsCode(competitorErr, generatedruntime.DiagnosticInsufficientReplacementSpace) {
					t.Fatalf("resumed retention allowed competitor: %v", competitorErr)
				}
			} else {
				if competitorErr != nil || competitor == nil {
					t.Fatalf("all-unattempted reservation remained charged: reservation=%v error=%v", competitor, competitorErr)
				}
				competitor.Release()
			}
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
	validator, err := applifecycle.NewDeploymentBindingValidator(applifecycle.New(db))
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
	positive, err := applifecycle.NewPositiveFenceValidator(applifecycle.New(db))
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	deploymentRepository, err := deployments.NewWithExecutionGuard(db, guard)
	if err != nil {
		t.Fatal(err)
	}
	admissionEngine, err := generatedruntime.NewEngine(capacityUnusedRunner{}, capacityUnusedEnvironment{}, capacity, deploymentRepository, generatedruntime.EngineOptions{
		DockerExecutable:      filepath.Join(dataRoot, "docker.exe"),
		WorkingDirectory:      dataRoot,
		DockerConfigDirectory: dockerConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
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
	boundExecutor, err := NewExecutor(applicationStore, materializer, configurations, deploymentRepository, plans, compiler, artifactRepository, runtimeState, runtime, authorization, routes, migrations, Options{})
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
		compiler: compiler, runtime: runtime, routes: routes, migrations: migrations}
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

type realNetworkRetentionSpec struct {
	appID, releaseID, deploymentPlanID string
	deploymentID                       string
	artifactID, imageID, definition    string
	component                          deploymentplans.Component
}

type realNetworkRetentionRunner struct {
	db                         *sql.DB
	jobID                      string
	specs                      map[string]realNetworkRetentionSpec
	containers                 map[string]realNetworkRetentionSpec
	networkCreated             bool
	containerCreates           int
	containerStarts            int
	cleanupCalls               int
	invalidated                bool
	invalidateOnNetworkInspect bool
}

func (r *realNetworkRetentionRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if len(request.Args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("unexpected generated runtime command")
	}
	switch request.Args[0] + " " + request.Args[1] {
	case "image inspect":
		spec, ok := r.specs[request.Args[len(request.Args)-1]]
		if !ok {
			return runtimeprocess.CommandResult{}, errors.New("unexpected image")
		}
		return retentionJSON(map[string]any{
			"id": spec.imageID, "size": 100,
			"labels": map[string]string{
				"io.rig.managed": "generated-image", "io.rig.application": spec.appID,
				"io.rig.release": spec.releaseID, "io.rig.artifact": spec.artifactID,
				"io.rig.plan": spec.deploymentPlanID, "io.rig.component": spec.component.Name,
				"io.rig.role": spec.component.Role, "io.rig.definition": spec.definition,
			},
			"user": "node", "workingDir": "/workspace", "entrypoint": []string{"/usr/local/bin/rig-entrypoint"},
		})
	case "network inspect":
		if !r.networkCreated {
			return runtimeprocess.CommandResult{Stderr: []byte("No such network")}, errors.New("network missing")
		}
		var spec realNetworkRetentionSpec
		for _, value := range r.specs {
			spec = value
			break
		}
		description, _ := generatedruntime.DescribeAppNetwork(spec.appID)
		if r.invalidateOnNetworkInspect && !r.invalidated {
			r.invalidated = true
			if _, err := r.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, r.jobID); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
		return retentionJSON(map[string]any{
			"name": description.Name, "driver": "bridge", "scope": "local", "internal": false,
			"labels": map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": spec.appID},
		})
	case "network create":
		r.networkCreated = true
		return runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("9", 64))}, nil
	case "container inspect":
		identity := request.Args[len(request.Args)-1]
		spec, ok := r.containers[identity]
		if !ok {
			return runtimeprocess.CommandResult{Stderr: []byte("No such container")}, errors.New("container missing")
		}
		if !r.invalidated && identity == strings.Repeat("a", 64) {
			r.invalidated = true
			if _, err := r.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, r.jobID); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
		return retentionJSON(retentionContainerInspection(spec, identity))
	case "container create":
		imageID := request.Args[len(request.Args)-4]
		spec, ok := r.specs[imageID]
		if !ok {
			return runtimeprocess.CommandResult{}, errors.New("unexpected candidate image")
		}
		if err := r.db.QueryRow(`SELECT id FROM deployments WHERE job_id=?`, r.jobID).Scan(&spec.deploymentID); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		description, _ := generatedruntime.DescribeInactiveCandidate(spec.appID, spec.component.Name, "")
		containerID := strings.Repeat(map[string]string{"api": "a", "web": "b"}[spec.component.Name], 64)
		r.containers[containerID] = spec
		r.containers[description.ContainerName] = spec
		r.containerCreates++
		return runtimeprocess.CommandResult{Stdout: []byte(containerID + "\n")}, nil
	case "container start":
		r.containerStarts++
		return runtimeprocess.CommandResult{}, nil
	case "container stop", "container rm":
		r.cleanupCalls++
		return runtimeprocess.CommandResult{}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected generated runtime command")
	}
}

func retentionContainerInspection(spec realNetworkRetentionSpec, containerID string) map[string]any {
	description, _ := generatedruntime.DescribeInactiveCandidate(spec.appID, spec.component.Name, "")
	limits := generatedruntime.ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3}
	return map[string]any{
		"id": containerID, "name": "/" + description.ContainerName, "image": spec.imageID,
		"labels": map[string]string{
			"io.rig.managed": "generated-runtime", "io.rig.application": spec.appID,
			"io.rig.release": spec.releaseID, "io.rig.deployment": spec.deploymentID, "io.rig.artifact": spec.artifactID,
			"io.rig.plan": spec.deploymentPlanID, "io.rig.component": spec.component.Name,
			"io.rig.role": spec.component.Role, "io.rig.slot": string(description.Slot),
		},
		"user": "node", "workingDir": "/workspace", "cmd": []string{"/bin/sh", "-lc", spec.component.RunCommand},
		"healthTest":     []string{"CMD-SHELL", `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`},
		"healthInterval": int64(2 * time.Second), "healthTimeout": int64(2 * time.Second), "healthStartPeriod": int64(5 * time.Second), "healthRetries": 3,
		"memory": limits.MemoryBytes, "memorySwap": limits.MemoryBytes, "nanoCpus": limits.MilliCPUs * 1_000_000,
		"pidsLimit": limits.PIDs, "ulimits": []map[string]any{{"Name": "nofile", "Soft": 1024, "Hard": 1024}}, "init": true,
		"networkMode": description.NetworkName, "readonlyRootfs": true, "privileged": false, "capAdd": []string{}, "capDrop": []string{"ALL"},
		"securityOpt": []string{"no-new-privileges:true"}, "binds": []string{}, "portBindings": map[string]any{},
		"tmpfs":   map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(limits.TmpfsBytes, 10)},
		"logType": "local", "logConfig": map[string]string{"max-size": limits.LogSize, "max-file": strconv.Itoa(limits.LogFiles)}, "restart": "no",
		"status": "created", "running": false, "paused": false, "restarting": false, "dead": false, "exitCode": 0, "health": "",
		"networks": map[string]any{description.NetworkName: map[string]any{"Aliases": []string{description.NetworkAlias}}},
	}
}

func retentionJSON(value any) (runtimeprocess.CommandResult, error) {
	body, err := json.Marshal(value)
	return runtimeprocess.CommandResult{Stdout: body}, err
}

type realNetworkRetentionEnvironment struct{ path string }

func (e *realNetworkRetentionEnvironment) Stage(_ string, _ int, contents []byte) (generatedruntime.EnvironmentLease, error) {
	defer func() {
		for index := range contents {
			contents[index] = 0
		}
	}()
	if err := os.WriteFile(e.path, contents, 0o600); err != nil {
		return nil, err
	}
	return realNetworkRetentionLease{path: e.path}, nil
}

type realNetworkRetentionLease struct{ path string }

func (l realNetworkRetentionLease) Path() string { return l.path }
func (l realNetworkRetentionLease) Cleanup() error {
	err := os.Remove(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
