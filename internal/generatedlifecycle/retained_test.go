package generatedlifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

func TestRetainedAdapterResumesPartialTwoComponentStartAndProvesHealth(t *testing.T) {
	fixture := newRetainedAdapterFixture(t, 2, applifecycle.Start, applifecycle.BaselineStopped)
	fixture.setAll("exited")
	writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeRoutesAbsent)
	for _, component := range fixture.operation.Pins.Components {
		beginAdapterEffect(t, fixture, applifecycle.BoundaryStart, component.Name, applifecycle.EffectCommand)
	}
	set, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	first := fixture.operation.Pins.Components[0].Name
	firstProof, err := fixture.adapter.Start(fixture.ctx, fixture.claim, set, first)
	if err != nil || firstProof.Outcome != applifecycle.OutcomeContainerRunning {
		t.Fatalf("first start proof = %#v, %v", firstProof, err)
	}
	observeAdapterProof(t, fixture, firstProof)
	if err := fixture.adapter.ReleaseStarts(fixture.ctx, set); err != nil {
		t.Fatal(err)
	}

	// Simulate a fresh coordinator invocation after A's receipt. The complete
	// roster retains A as immutable history while only B remains commandable.
	resumed, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	second := fixture.operation.Pins.Components[1].Name
	secondProof, err := fixture.adapter.Start(fixture.ctx, fixture.claim, resumed, second)
	if err != nil || secondProof.Outcome != applifecycle.OutcomeContainerRunning {
		t.Fatalf("resumed second start proof = %#v, %v", secondProof, err)
	}
	observeAdapterProof(t, fixture, secondProof)
	if err := fixture.adapter.ReleaseStarts(fixture.ctx, resumed); err != nil {
		t.Fatal(err)
	}
	beginAdapterEffect(t, fixture, applifecycle.BoundaryHealth, "", applifecycle.EffectObservationOnly)
	health, err := fixture.adapter.Healthy(fixture.ctx, fixture.claim)
	if err != nil || health.Outcome != applifecycle.OutcomeHealthy || health.ComponentName != "" {
		t.Fatalf("health proof = %#v, %v", health, err)
	}
	if fixture.runner.count("container start") != 2 || fixture.runner.hasDestructive() {
		t.Fatalf("retained command inventory = %#v", fixture.runner.kinds())
	}
}

func TestRetainedAdapterStopUsesCurrentRepositoryAuthorityAndNeverRemoves(t *testing.T) {
	fixture := newRetainedAdapterFixture(t, 1, applifecycle.Stop, applifecycle.BaselineRunning)
	fixture.setAll("running")
	writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectCommand, applifecycle.OutcomeRoutesAbsent)
	component := fixture.operation.Pins.Components[0].Name
	beginAdapterEffect(t, fixture, applifecycle.BoundaryStop, component, applifecycle.EffectCommand)
	fixture.runner.loseStopAck = true
	proof, err := fixture.adapter.Stop(fixture.ctx, fixture.claim, component, time.Second)
	if err != nil || proof.Outcome != applifecycle.OutcomeContainerStopped || proof.ComponentName != component {
		t.Fatalf("stop proof = %#v, %v", proof, err)
	}
	if fixture.runner.count("container stop") != 1 || fixture.runner.hasDestructive() {
		t.Fatalf("stop command inventory = %#v", fixture.runner.kinds())
	}
	observeAdapterProof(t, fixture, proof)
	if _, err := fixture.adapter.Stop(fixture.ctx, fixture.claim, component, time.Second); !errors.Is(err, ErrRetainedAuthority) {
		t.Fatalf("receipted stop was reauthorized: %v", err)
	}
}

func TestRetainedAdapterUnknownComponentBlocksOtherReservedStart(t *testing.T) {
	fixture := newRetainedAdapterFixture(t, 2, applifecycle.Start, applifecycle.BaselineStopped)
	fixture.setAll("exited")
	writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeRoutesAbsent)
	for _, component := range fixture.operation.Pins.Components {
		beginAdapterEffect(t, fixture, applifecycle.BoundaryStart, component.Name, applifecycle.EffectCommand)
	}
	set, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	first, second := fixture.operation.Pins.Components[0].Name, fixture.operation.Pins.Components[1].Name
	fixture.runner.uncertainStart = first
	if _, err := fixture.adapter.Start(fixture.ctx, fixture.claim, set, first); err == nil {
		t.Fatal("uncertain first start returned proof")
	}
	secondStarts := fixture.runner.countStart(second)
	if _, err := fixture.adapter.Start(fixture.ctx, fixture.claim, set, second); err == nil {
		t.Fatal("second start was admitted with an unknown roster member")
	}
	if fixture.runner.countStart(second) != secondStarts || fixture.runner.hasDestructive() {
		t.Fatalf("second component mutated after uncertainty: %#v", fixture.runner.kinds())
	}
	if err := fixture.adapter.ReleaseStarts(fixture.ctx, set); err == nil {
		t.Fatal("uncertain reservation was released")
	}
}

func TestRetainedAdapterRechecksWholeRosterAtStartCommandBoundary(t *testing.T) {
	fixture := newRetainedAdapterFixture(t, 2, applifecycle.Start, applifecycle.BaselineStopped)
	fixture.setAll("exited")
	writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeRoutesAbsent)
	for _, component := range fixture.operation.Pins.Components {
		beginAdapterEffect(t, fixture, applifecycle.BoundaryStart, component.Name, applifecycle.EffectCommand)
	}
	set, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	first, second := fixture.operation.Pins.Components[0].Name, fixture.operation.Pins.Components[1].Name
	// Prepare observes first once, and Start's early roster census observes it
	// again. The third observation occurs inside the exact pre-command guard.
	fixture.runner.makeUnknownAt(first, 3)
	secondStarts := fixture.runner.countStart(second)
	if _, err := fixture.adapter.Start(fixture.ctx, fixture.claim, set, second); err == nil {
		t.Fatal("start was admitted after another member became unknown at the command boundary")
	}
	if fixture.runner.countStart(second) != secondStarts || fixture.runner.hasDestructive() {
		t.Fatalf("component mutated after final roster drift: %#v", fixture.runner.kinds())
	}
	if err := fixture.adapter.ReleaseStarts(fixture.ctx, set); err == nil {
		t.Fatal("unknown roster allowed reservation release after guarded refusal")
	}
}

func TestRetainedAdapterRefusesReceiptedComponentDriftWithoutReissue(t *testing.T) {
	fixture := newRetainedAdapterFixture(t, 2, applifecycle.Start, applifecycle.BaselineStopped)
	fixture.setAll("exited")
	writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeRoutesAbsent)
	for _, component := range fixture.operation.Pins.Components {
		beginAdapterEffect(t, fixture, applifecycle.BoundaryStart, component.Name, applifecycle.EffectCommand)
	}
	set, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	first, second := fixture.operation.Pins.Components[0].Name, fixture.operation.Pins.Components[1].Name
	proof, err := fixture.adapter.Start(fixture.ctx, fixture.claim, set, first)
	if err != nil {
		t.Fatal(err)
	}
	observeAdapterProof(t, fixture, proof)
	if err := fixture.adapter.ReleaseStarts(fixture.ctx, set); err != nil {
		t.Fatal(err)
	}
	fixture.runner.set(first, "exited")
	secondStarts := fixture.runner.countStart(second)
	if _, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim); !errors.Is(err, ErrRetainedAuthority) {
		t.Fatalf("receipted component drift error = %v", err)
	}
	if fixture.runner.countStart(first) != 1 || fixture.runner.countStart(second) != secondStarts || fixture.runner.hasDestructive() {
		t.Fatalf("receipted drift reissued a command: %#v", fixture.runner.kinds())
	}
}

func TestRetainedAdapterPostEffectRefusalsReturnNoProofOrSecondCommand(t *testing.T) {
	t.Run("start cancellation after successful observation", func(t *testing.T) {
		fixture := newRetainedAdapterFixture(t, 1, applifecycle.Start, applifecycle.BaselineStopped)
		fixture.setAll("exited")
		writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeRoutesAbsent)
		component := fixture.operation.Pins.Components[0].Name
		beginAdapterEffect(t, fixture, applifecycle.BoundaryStart, component, applifecycle.EffectCommand)
		set, err := fixture.adapter.PrepareStarts(fixture.ctx, fixture.claim)
		if err != nil {
			t.Fatal(err)
		}
		attemptContext, cancel := context.WithCancel(fixture.ctx)
		fixture.runner.armAfterStartObservation(cancel)
		proof, err := fixture.adapter.Start(attemptContext, fixture.claim, set, component)
		if err == nil || proof != (applifecycle.EffectProofV1{}) {
			t.Fatalf("cancelled post-start result = %#v, %v", proof, err)
		}
		if fixture.runner.countStart(component) != 1 {
			t.Fatalf("start command count = %d, want 1", fixture.runner.countStart(component))
		}
		fixture.runner.set(component, "restarting")
		secondProof, secondErr := fixture.adapter.Start(fixture.ctx, fixture.claim, set, component)
		if secondErr == nil || secondProof != (applifecycle.EffectProofV1{}) || fixture.runner.countStart(component) != 1 {
			t.Fatalf("second start result = %#v commands=%d error=%v", secondProof, fixture.runner.countStart(component), secondErr)
		}
		if err := fixture.adapter.ReleaseStarts(fixture.ctx, set); err == nil {
			t.Fatal("unknown post-command state released aggregate reservation")
		}
		if fixture.runner.hasDestructive() {
			t.Fatalf("destructive post-start command inventory = %#v", fixture.runner.kinds())
		}
	})

	t.Run("stop authority drift after successful observation", func(t *testing.T) {
		fixture := newRetainedAdapterFixture(t, 1, applifecycle.Stop, applifecycle.BaselineRunning)
		fixture.setAll("running")
		writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectCommand, applifecycle.OutcomeRoutesAbsent)
		component := fixture.operation.Pins.Components[0].Name
		beginAdapterEffect(t, fixture, applifecycle.BoundaryStop, component, applifecycle.EffectCommand)
		fixture.runner.armAfterStopObservation(func() {
			mustExec(t, fixture.db, `UPDATE jobs SET attempt=attempt+1 WHERE id=?`, fixture.operation.JobID)
		})
		proof, err := fixture.adapter.Stop(fixture.ctx, fixture.claim, component, time.Second)
		if err == nil || proof != (applifecycle.EffectProofV1{}) {
			t.Fatalf("drifted post-stop result = %#v, %v", proof, err)
		}
		secondProof, secondErr := fixture.adapter.Stop(fixture.ctx, fixture.claim, component, time.Second)
		if secondErr == nil || secondProof != (applifecycle.EffectProofV1{}) || fixture.runner.count("container stop") != 1 {
			t.Fatalf("second stop result = %#v commands=%d error=%v", secondProof, fixture.runner.count("container stop"), secondErr)
		}
		if fixture.runner.hasDestructive() {
			t.Fatalf("destructive post-stop command inventory = %#v", fixture.runner.kinds())
		}
	})

	t.Run("health cancellation after final observation", func(t *testing.T) {
		fixture := newRetainedAdapterFixture(t, 1, applifecycle.Start, applifecycle.BaselineStopped)
		fixture.setAll("running")
		writeAdapterEffect(t, fixture, applifecycle.BoundaryWithdraw, "", applifecycle.EffectObservationOnly, applifecycle.OutcomeRoutesAbsent)
		component := fixture.operation.Pins.Components[0].Name
		writeAdapterEffect(t, fixture, applifecycle.BoundaryStart, component, applifecycle.EffectCommand, applifecycle.OutcomeContainerRunning)
		beginAdapterEffect(t, fixture, applifecycle.BoundaryHealth, "", applifecycle.EffectObservationOnly)
		healthContext, cancel := context.WithCancel(fixture.ctx)
		fixture.runner.armAfterNextObservation(cancel)
		proof, err := fixture.adapter.Healthy(healthContext, fixture.claim)
		if err == nil || proof != (applifecycle.EffectProofV1{}) {
			t.Fatalf("cancelled post-health result = %#v, %v", proof, err)
		}
		if fixture.runner.count("container start") != 0 || fixture.runner.count("container stop") != 0 || fixture.runner.hasDestructive() {
			t.Fatalf("health cancellation command inventory = %#v", fixture.runner.kinds())
		}
	})
}

type retainedAdapterFixture struct {
	db        *sql.DB
	repo      *applifecycle.Repository
	jobs      *jobs.Service
	operation applifecycle.Operation
	claim     applifecycle.WorkerClaim
	adapter   *Retained
	runner    *retainedAdapterRunner
	ctx       context.Context
	release   func() error
}

func newRetainedAdapterFixture(t *testing.T, componentCount int, action applifecycle.Action, baseline applifecycle.BaselineDisposition) *retainedAdapterFixture {
	t.Helper()
	root := generateddomain.NewRetainedRoot(t)
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	db, err := database.OpenForGeneratedDomain(context.Background(), root, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := applifecycle.New(db)
	bindingValidator, err := applifecycle.NewDeploymentBindingValidator(repo)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, bindingValidator)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(repo)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, root)
	if err != nil {
		t.Fatal(err)
	}
	deploymentRepository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := deploymentRepository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	runner := newRetainedAdapterRunner(nil, "", "", "", "")
	runner.absentError = retainedAdapterExitOne(t)
	process := generateddomain.NewRunner(generateddomain.Options{Fallback: runner, MemoryAvailableBytes: 8 << 30, DiskAvailableBytes: 8 << 30})
	environment := &noEnvironmentStager{}
	options := generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: 64 << 10, ReplacementDiskBytes: 128 << 20,
		Limits: generatedruntime.ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3},
	}
	// Construct the real empty domain before creating any deployment or lifecycle
	// history. The transcript supplies only process observations, never authority.
	domain, err := generatedingress.NewGeneratedDomain(context.Background(), stores, process, environment, generatedingress.GeneratedDomainOptions{
		DataRoot: root, DockerExecutable: options.DockerExecutable,
		DockerConfigDirectory: options.DockerConfigDirectory, WorkingDirectory: options.WorkingDirectory,
		CommandTimeout: options.CommandTimeout, PullTimeout: 5 * time.Minute,
		HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval,
		OutputLimit: options.OutputLimit, MemoryBytes: options.Limits.MemoryBytes,
		MilliCPUs: options.Limits.MilliCPUs, PIDs: options.Limits.PIDs,
		TmpfsBytes: options.Limits.TmpfsBytes, LogSize: options.Limits.LogSize,
		LogFiles: options.Limits.LogFiles, ReplacementDiskBytes: options.ReplacementDiskBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := generatedruntime.NewEngine(process, environment, domain.Candidates(), options)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	appID, actorID, machineID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	mustExec(t, db, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, actorID, "hash", stamp, stamp)
	mustExec(t, db, `INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machineID, machineID, stamp, stamp)
	mustExec(t, db, `INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,'draft',?,?)`, appID, appID, appID, machineID, stamp, stamp)
	names := []string{"api"}
	if componentCount == 2 {
		names = append(names, "web")
	}
	var planComponents []deploymentplans.Component
	var provenance []deploymentplans.FieldProvenance
	var configurationTargets []appconfig.ComponentTarget
	for _, name := range names {
		component := deploymentplans.Component{
			Name: name, Role: generatedruntime.RoleServer, Technology: "node", RootDirectory: "apps/" + name,
			PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: "apps/" + name,
			NodeVersion: "20", BuildCommand: "npm run build", RunCommand: "node server.js", InternalPort: 8080, HealthProbe: "/health",
		}
		if name == "web" {
			component.Role = generatedruntime.RoleStatic
			component.Technology = "static"
			component.StaticOutputDirectory = "dist"
			component.RunCommand = projectanalysis.ManagedStaticCommand(component.StaticOutputDirectory, int(component.InternalPort))
		}
		planComponents = append(planComponents, component)
		configurationTargets = append(configurationTargets, appconfig.ComponentTarget{Name: name, Role: component.Role})
		for _, field := range []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe", "technology", "staticOutputDirectory"} {
			provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components." + name + "." + field, Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
		}
	}
	plan, err := plans.Replace(context.Background(), appID, actorID, deploymentplans.ReplaceInput{Plan: deploymentplans.Plan{
		SetupVersion: projectanalysis.SetupVersion, Strategy: deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "manual-setup", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: strings.Repeat("b", 64)},
		Components: planComponents, FieldProvenance: provenance,
	}})
	if err != nil {
		t.Fatal(err)
	}
	configurationRevision, err := configuration.ReplaceScoped(context.Background(), appID, actorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: plan.ID, PlanRevisionNumber: plan.RevisionNumber, Components: configurationTargets,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`, releaseID, appID, stamp, strings.Repeat("d", 64), strings.Repeat("e", 64), configurationRevision.RevisionID, configurationRevision.RevisionNumber, plan.ID, plan.RevisionNumber)
	created, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "retained-adapter-deployment", RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create original deployment: created=%t error=%v", created.Created, err)
	}
	mustExec(t, db, `UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID)
	deployment, _, err := deploymentRepository.GetOrCreateByJob(context.Background(), appID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = deploymentRepository.InitializeRuntime(context.Background(), appID, deployment.ID, releaseID,
		configurationRevision.RevisionID, configurationRevision.RevisionNumber, deployments.RuntimeGeneratedNode, plan.ID, plan.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	deploymentID := deployment.ID
	originalClaim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	runtimeState := generatedruntimestate.New(db)
	if _, made, err := runtimeState.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber, ComponentNames: names,
	}); err != nil || !made {
		t.Fatalf("runtime begin: created=%t error=%v", made, err)
	}
	if _, err := runtimeState.Advance(context.Background(), appID, deploymentID, generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, ""); err != nil {
		t.Fatal(err)
	}
	scope, releaseScope, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = releaseScope() })
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	request := generatedingress.GeneratedReplacementRequest{
		Claim: originalClaim, DeploymentID: deploymentID, ReleaseID: releaseID,
		ConfigurationRevisionID: configurationRevision.RevisionID, ConfigurationRevisionNumber: configurationRevision.RevisionNumber,
		DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber, CandidateSlot: string(generatedruntime.SlotBlue),
	}
	reservation, err := engine.ReserveReplacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reservation.Release)
	components := make([]applifecycle.RetainedComponent, 0, len(planComponents))
	for _, protected := range planComponents {
		name := protected.Name
		artifactID, containerID := uuid.NewString(), retainedDigest(name+"-container")
		network := "rig-a-" + strings.ReplaceAll(appID, "-", "")
		containerName := network + "-c-" + retainedDigest(name)[:12] + "-" + string(generatedruntime.SlotBlue)
		alias := "rig-c-" + retainedDigest(name)[:12] + "-" + string(generatedruntime.SlotBlue)
		definition, image := retainedDigest(name+"-definition"), "sha256:"+retainedDigest(name+"-image")
		mustExec(t, db, `INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at)
			VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, plan.ID, plan.RevisionNumber, name, definition, image, stamp, stamp, stamp)
		if _, err := runtimeState.SetImageReady(ctx, appID, deploymentID, name, artifactID); err != nil {
			t.Fatal(err)
		}
		component := applifecycle.RetainedComponent{
			Name: name, Role: protected.Role, Technology: protected.Technology, ContainerID: containerID,
			ContainerName: containerName, ArtifactID: artifactID, ImageContentID: image,
			BuildDefinitionDigest: definition, NetworkName: network, NetworkAlias: alias, InternalPort: protected.InternalPort,
			WorkingDirectory: "/workspace/" + protected.RootDirectory, RunCommandDigest: retainedDigest(protected.RunCommand),
			Limits: applifecycle.RetainedLimits{MemoryBytes: 512 << 20, MemorySwapBytes: 512 << 20,
				CPUQuota: 100000, CPUPeriod: 100000, PIDsLimit: 256, TmpfsBytes: 64 << 20,
				LogDriver: "local", LogMaxSize: "10m", LogMaxFiles: 3, HardeningPolicyVersion: "v1"},
		}
		components = append(components, component)
		runner.components[containerID] = &adapterContainer{pin: component, runCommand: protected.RunCommand,
			appID: appID, deploymentID: deploymentID, releaseID: releaseID, planID: plan.ID, status: "absent"}
	}
	if err := deploymentRepository.Gate(ctx, deployments.GateRequest{Claim: originalClaim, DeploymentID: deploymentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeState.Advance(ctx, appID, deploymentID, generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
		t.Fatal(err)
	}
	execution, err := engine.PinCandidates(ctx, generatedingress.GeneratedCandidateRequest(request), reservation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(execution.Leave)
	if err := engine.EnsureAppNetwork(ctx, deployments.GeneratedNetworkRequest{
		Claim: originalClaim, DeploymentID: deploymentID, ReleaseID: releaseID,
		ConfigurationRevisionID: configurationRevision.RevisionID, ConfigurationRevisionNumber: configurationRevision.RevisionNumber,
		DeploymentPlanRevisionID: plan.ID, DeploymentPlanRevisionNumber: plan.RevisionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	var invocations []generatedingress.GeneratedCandidateInvocation
	var endpoints []generatedruntime.RouteEndpoint
	for _, component := range components {
		invocation, err := execution.BeginCreate(component.Name)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if identity.ContainerID != component.ContainerID || identity.ContainerName != component.ContainerName || identity.NetworkAlias != component.NetworkAlias {
			t.Fatal("candidate fixture differs from protected component identity")
		}
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatal(err)
		}
		invocations = append(invocations, invocation)
		endpoints = append(endpoints, generatedruntime.RouteEndpoint{Component: component.Name, Role: component.Role,
			ContainerID: identity.ContainerID, NetworkName: identity.NetworkName, NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort})
	}
	if _, err := execution.FinishStarting(); err != nil {
		t.Fatal(err)
	}
	for _, invocation := range invocations {
		if err := execution.WaitHealthyAndRecord(invocation); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	activation, err := domain.Routes().Pin(ctx, execution, generatedruntime.RouteSwitchRequest{
		AppID: appID, ToSlot: generatedruntime.SlotBlue, Endpoints: endpoints,
	})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := activation.Activate(ctx)
	if err != nil || activated.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activate original retained deployment: phase=%s error=%v", activated.Phase, err)
	}
	head, err := runtimeState.Active(ctx, appID)
	if err != nil || head.DeploymentID != deploymentID || head.ActivationReceiptID == "" || head.Generation != 1 {
		t.Fatalf("receipt-backed active head = %#v error=%v", head, err)
	}
	if _, err := runtimeState.Advance(ctx, appID, deploymentID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := deploymentRepository.Transition(ctx, appID, deploymentID, deployments.Succeeded, ""); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`, stamp, created.Job.ID)
	execution.Leave()
	reservation.Release()
	pins := applifecycle.GeneratedLifecyclePinsV1{
		Version: 1, DeploymentID: deploymentID, ReleaseID: releaseID, Slot: head.Slot, ActiveGeneration: head.Generation,
		PlanRevision: applifecycle.RevisionRef{ID: plan.ID, Number: plan.RevisionNumber},
		Configuration: applifecycle.ConfigurationRef{RevisionID: configurationRevision.RevisionID, RevisionNumber: configurationRevision.RevisionNumber,
			FormatVersion: 2, PlanRevision: applifecycle.RevisionRef{ID: plan.ID, Number: plan.RevisionNumber}, RuntimeComponents: names},
		BaselineDisposition: baseline, Components: components,
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind", SelectedLineageGeneration: 1,
			SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("f", 64),
			ProtectedFormatVersion: 2, TargetRecordDigest: strings.Repeat("a", 64)},
	}
	accepted, err := repo.Accept(ctx, service, applifecycle.LifecycleRequest{
		ApplicationID: appID, ActorID: actorID, IdempotencyKey: "retained-adapter", Action: action,
	}, pins)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE jobs SET status='running',phase='running',attempt=attempt+1 WHERE id=?`, accepted.Operation.JobID)
	job, err := service.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(job)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewRetained(repo, engine)
	if err != nil {
		t.Fatal(err)
	}
	if runner.count("container create") != len(names) || runner.count("container start") != len(names) {
		t.Fatalf("original deployment command inventory = %#v", runner.kinds())
	}
	// Assertions below measure only the lifecycle operation, after the genuine
	// deployment has completed. Preserve its physical state and receipt labels.
	runner.requests = nil
	runner.inspectCounts = make(map[string]int)
	return &retainedAdapterFixture{db: db, repo: repo, jobs: service, operation: accepted.Operation, claim: claim,
		adapter: adapter, runner: runner, ctx: ctx, release: releaseScope}
}

func beginAdapterEffect(t *testing.T, fixture *retainedAdapterFixture, boundary applifecycle.Boundary, component string, mode applifecycle.EffectMode) applifecycle.Intent {
	t.Helper()
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	intent, created, err := fixture.repo.BeginBoundaryTx(context.Background(), tx, fixture.claim, fixture.operation, applifecycle.EffectRequestV1{
		Version: 1, OperationID: fixture.operation.JobID, ApplicationID: fixture.operation.ApplicationID,
		DesiredRevision: fixture.operation.DesiredRevision, Boundary: boundary, ComponentName: component,
		Mode: mode, PinsDigest: fixture.operation.PinsDigest,
	})
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("begin %s/%s created=%t error=%v", boundary, component, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func writeAdapterEffect(t *testing.T, fixture *retainedAdapterFixture, boundary applifecycle.Boundary, component string, mode applifecycle.EffectMode, outcome applifecycle.Outcome) {
	t.Helper()
	intent := beginAdapterEffect(t, fixture, boundary, component, mode)
	observeAdapterProof(t, fixture, applifecycle.EffectProofV1{
		Version: 1, OperationID: fixture.operation.JobID, ApplicationID: fixture.operation.ApplicationID,
		DesiredRevision: fixture.operation.DesiredRevision, Boundary: boundary, ComponentName: component,
		Outcome: outcome, IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("e", 64), PinsDigest: fixture.operation.PinsDigest,
	})
}

func observeAdapterProof(t *testing.T, fixture *retainedAdapterFixture, proof applifecycle.EffectProofV1) {
	t.Helper()
	authority, err := fixture.repo.PendingRuntimeBoundary(context.Background(), fixture.claim, proof.Boundary, proof.ComponentName)
	if err != nil && (proof.Boundary == applifecycle.BoundaryWithdraw || proof.Boundary == applifecycle.BoundaryPublish) {
		// Route boundaries are outside the retained adapter; load their intent by
		// retaining it above and reconstruct only through the transaction helper.
		authority.Intent = beginIntentLookup(t, fixture, proof.Boundary, proof.ComponentName)
	} else if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.repo.ObserveBoundaryTx(context.Background(), tx, fixture.claim, authority.Intent, proof); err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("observe %s/%s created=%t error=%v", proof.Boundary, proof.ComponentName, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func beginIntentLookup(t *testing.T, fixture *retainedAdapterFixture, boundary applifecycle.Boundary, component string) applifecycle.Intent {
	t.Helper()
	// Route prerequisite tests need only the immutable intent retained by the
	// immediately preceding begin. Read its canonical public identity from SQL.
	var intent applifecycle.Intent
	var requestJSON, digest, created string
	var attempt int
	if err := fixture.db.QueryRow(`SELECT request_json,request_digest,writer_attempt,created_at FROM application_lifecycle_effect_intents WHERE job_id=? AND boundary=? AND component_name=?`, fixture.operation.JobID, string(boundary), component).Scan(&requestJSON, &digest, &attempt, &created); err != nil {
		t.Fatal(err)
	}
	var request applifecycle.EffectRequestV1
	if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
		t.Fatal(err)
	}
	intent.JobID, intent.Key, intent.WriterAttempt, intent.Request, intent.RequestDigest = fixture.operation.JobID, applifecycle.EffectKey{Boundary: boundary, ComponentName: component}, attempt, request, digest
	intent.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return intent
}

type retainedAdapterRunner struct {
	mu             sync.Mutex
	components     map[string]*adapterContainer
	requests       []runtimeprocess.CommandRequest
	loseStopAck    bool
	uncertainStart string
	inspectCounts  map[string]int
	unknownAt      map[string]int
	afterStart     func()
	afterStop      func()
	afterObserve   func()
	absentError    *exec.ExitError
}

type adapterContainer struct {
	pin          applifecycle.RetainedComponent
	runCommand   string
	appID        string
	deploymentID string
	releaseID    string
	planID       string
	status       string
	receiptID    string
}

func newRetainedAdapterRunner(components []applifecycle.RetainedComponent, appID, deploymentID, releaseID, planID string) *retainedAdapterRunner {
	values := make(map[string]*adapterContainer, len(components))
	for _, component := range components {
		values[component.ContainerID] = &adapterContainer{pin: component, appID: appID, deploymentID: deploymentID, releaseID: releaseID, planID: planID, status: "running"}
	}
	return &retainedAdapterRunner{components: values, inspectCounts: make(map[string]int), unknownAt: make(map[string]int)}
}

func (r *retainedAdapterRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request.Args = append([]string(nil), request.Args...)
	r.requests = append(r.requests, request)
	kind := adapterCommandKind(request.Args)
	switch kind {
	case "container inspect":
		identity := request.Args[len(request.Args)-1]
		component := r.components[identity]
		if component == nil {
			for _, value := range r.components {
				if value.pin.ContainerName == identity {
					component = value
					break
				}
			}
		}
		if component == nil || component.status == "absent" {
			return runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + identity)}, r.absentError
		}
		r.inspectCounts[component.pin.Name]++
		if threshold := r.unknownAt[component.pin.Name]; threshold != 0 && r.inspectCounts[component.pin.Name] >= threshold {
			component.status = "restarting"
		}
		return adapterJSON(component.inspection()), nil
	case "network inspect":
		for _, component := range r.components {
			if component.pin.NetworkName == request.Args[len(request.Args)-1] {
				result := adapterJSON(map[string]any{"name": component.pin.NetworkName, "driver": "bridge", "scope": "local", "internal": false,
					"labels": map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": component.appID}})
				hook := r.afterObserve
				r.afterObserve = nil
				if hook != nil {
					hook()
				}
				return result, nil
			}
		}
		return runtimeprocess.CommandResult{Stderr: []byte("not found")}, errors.New("exit 1")
	case "image inspect":
		for _, component := range r.components {
			if component.pin.ImageContentID == request.Args[len(request.Args)-1] {
				return adapterJSON(map[string]any{
					"id": component.pin.ImageContentID, "size": 1, "user": "node", "workingDir": "/workspace",
					"entrypoint": []string{generatedcontainer.Entrypoint},
					"labels": map[string]string{"io.rig.managed": "generated-image", "io.rig.application": component.appID,
						"io.rig.release": component.releaseID, "io.rig.artifact": component.pin.ArtifactID,
						"io.rig.plan": component.planID, "io.rig.component": component.pin.Name,
						"io.rig.role": component.pin.Role, "io.rig.definition": component.pin.BuildDefinitionDigest},
				}), nil
			}
		}
		return runtimeprocess.CommandResult{}, errors.New("unexpected retained fixture image")
	case "container create":
		name, receiptID := "", ""
		for index, value := range request.Args {
			if value == "--name" && index+1 < len(request.Args) {
				name = request.Args[index+1]
			}
			if value == "--label" && index+1 < len(request.Args) && strings.HasPrefix(request.Args[index+1], generatedcontainer.ReceiptLabel+"=") {
				receiptID = strings.TrimPrefix(request.Args[index+1], generatedcontainer.ReceiptLabel+"=")
			}
		}
		for _, component := range r.components {
			if component.pin.ContainerName == name && component.status == "absent" && receiptID != "" {
				component.status, component.receiptID = "created", receiptID
				return runtimeprocess.CommandResult{Stdout: []byte(component.pin.ContainerID + "\n")}, nil
			}
		}
		return runtimeprocess.CommandResult{}, errors.New("unexpected retained fixture create")
	case "container start":
		component := r.components[request.Args[len(request.Args)-1]]
		if component == nil {
			return runtimeprocess.CommandResult{}, errors.New("exit 1")
		}
		if component.pin.Name == r.uncertainStart {
			component.status = "restarting"
			return runtimeprocess.CommandResult{}, errors.New("lost start acknowledgement")
		}
		component.status = "running"
		r.afterObserve = r.afterStart
		r.afterStart = nil
		return runtimeprocess.CommandResult{}, nil
	case "container stop":
		component := r.components[request.Args[len(request.Args)-1]]
		component.status = "exited"
		r.afterObserve = r.afterStop
		r.afterStop = nil
		if r.loseStopAck {
			return runtimeprocess.CommandResult{}, errors.New("lost stop acknowledgement")
		}
		return runtimeprocess.CommandResult{}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected retained Docker command")
	}
}

func (c *adapterContainer) inspection() map[string]any {
	running := c.status == "running" || c.status == "paused" || c.status == "restarting"
	networks := map[string]any(nil)
	if running {
		networks = map[string]any{c.pin.NetworkName: map[string]any{"Aliases": []string{c.pin.NetworkAlias}}}
	}
	startedAt := "2026-10-01T00:00:00Z"
	if c.status == "created" {
		startedAt = "0001-01-01T00:00:00Z"
	}
	return map[string]any{
		"id": c.pin.ContainerID, "name": "/" + c.pin.ContainerName, "image": c.pin.ImageContentID,
		"configImage": c.pin.ImageContentID, "entrypoint": []string{generatedcontainer.Entrypoint},
		"volumes": map[string]any{}, "hostMounts": []any{}, "mounts": []any{}, "startedAt": startedAt,
		"labels": map[string]string{"io.rig.managed": "generated-runtime", "io.rig.application": c.appID,
			"io.rig.release": c.releaseID, "io.rig.deployment": c.deploymentID, "io.rig.artifact": c.pin.ArtifactID,
			"io.rig.plan": c.planID, "io.rig.component": c.pin.Name, "io.rig.role": c.pin.Role, "io.rig.slot": "blue", generatedcontainer.ReceiptLabel: c.receiptID},
		"user": "node", "workingDir": c.pin.WorkingDirectory, "cmd": []string{"/bin/sh", "-lc", c.runCommand},
		"healthTest":     []string{"CMD-SHELL", `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`},
		"healthInterval": int64(2 * time.Second), "healthTimeout": int64(2 * time.Second), "healthStartPeriod": int64(5 * time.Second), "healthRetries": 3,
		"memory": int64(512 << 20), "memorySwap": int64(512 << 20), "nanoCpus": int64(1_000_000_000), "pidsLimit": int64(256),
		"ulimits": []map[string]any{{"Name": "nofile", "Soft": int64(1024), "Hard": int64(1024)}}, "init": true,
		"networkMode": c.pin.NetworkName, "readonlyRootfs": true, "privileged": false, "capAdd": []string{}, "capDrop": []string{"ALL"},
		"securityOpt": []string{"no-new-privileges:true"}, "binds": []string{}, "portBindings": map[string]any{},
		"tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864"}, "logType": "local", "logConfig": map[string]string{"max-size": "10m", "max-file": "3"}, "restart": "no",
		"status": c.status, "running": running, "paused": c.status == "paused", "restarting": c.status == "restarting", "dead": c.status == "dead",
		"exitCode": 0, "health": map[bool]string{true: "healthy", false: ""}[running], "networks": networks,
	}
}

func (r *retainedAdapterRunner) setAll(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, component := range r.components {
		component.status = status
	}
}

func (f *retainedAdapterFixture) setAll(status string) { f.runner.setAll(status) }

func (r *retainedAdapterRunner) set(component, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, value := range r.components {
		if value.pin.Name == component {
			value.status = status
			return
		}
	}
}

func (r *retainedAdapterRunner) makeUnknownAt(component string, inspection int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unknownAt[component] = inspection
}

func (r *retainedAdapterRunner) armAfterStartObservation(hook func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterStart = hook
}

func (r *retainedAdapterRunner) armAfterStopObservation(hook func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterStop = hook
}

func (r *retainedAdapterRunner) armAfterNextObservation(hook func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterObserve = hook
}

func (r *retainedAdapterRunner) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]string, len(r.requests))
	for index := range r.requests {
		result[index] = adapterCommandKind(r.requests[index].Args)
	}
	return result
}

func (r *retainedAdapterRunner) count(kind string) int {
	count := 0
	for _, value := range r.kinds() {
		if value == kind {
			count++
		}
	}
	return count
}

func (r *retainedAdapterRunner) countStart(component string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, request := range r.requests {
		if adapterCommandKind(request.Args) == "container start" {
			value := r.components[request.Args[len(request.Args)-1]]
			if value != nil && value.pin.Name == component {
				count++
			}
		}
	}
	return count
}

func (r *retainedAdapterRunner) hasDestructive() bool {
	for _, kind := range r.kinds() {
		if kind == "container rm" || kind == "container create" || kind == "network create" || kind == "image build" {
			return true
		}
	}
	return false
}

func adapterCommandKind(args []string) string {
	if len(args) < 2 {
		return strings.Join(args, " ")
	}
	return args[0] + " " + args[1]
}

func adapterJSON(value any) runtimeprocess.CommandResult {
	encoded, _ := json.Marshal(value)
	return runtimeprocess.CommandResult{Stdout: encoded}
}

type noEnvironmentStager struct{}

func (*noEnvironmentStager) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("retained adapter must not stage environment")
}

func (*noEnvironmentStager) StageGeneratedCandidate(string, int, []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	return nil, errors.New("retained adapter must not stage candidate environment")
}

// Produce a completed process result for the candidate owner's strict Docker
// absence check. An arbitrary Go error is deliberately insufficient evidence.
func TestRetainedAdapterExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_RETAINED_ADAPTER_EXIT") == "1" {
		os.Exit(1)
	}
}

func retainedAdapterExitOne(t *testing.T) *exec.ExitError {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestRetainedAdapterExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_RETAINED_ADAPTER_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected completed exit one, got %T %v", err, err)
	}
	return exited
}

func retainedDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func mustExec(t *testing.T, db *sql.DB, query string, arguments ...any) {
	t.Helper()
	if _, err := db.Exec(query, arguments...); err != nil {
		t.Fatal(err)
	}
}
