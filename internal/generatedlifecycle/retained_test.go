package generatedlifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
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
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo, service := applifecycle.New(db), jobs.New(db)
	appID, actorID := uuid.NewString(), uuid.NewString()
	planID, releaseID, deploymentID, deployJobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	mustExec(t, db, `INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`, actorID, actorID, "hash", stamp, stamp)
	mustExec(t, db, `INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, appID, appID, appID, stamp, stamp)
	mustExec(t, db, `INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,?,0,'',?,?,'accepted',?,?)`,
		planID, appID, "apps/plan.json", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), componentCount, actorID, stamp, actorID, stamp)
	mustExec(t, db, `INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, appID, stamp, strings.Repeat("d", 64), strings.Repeat("e", 64), planID)
	mustExec(t, db, `INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,input_json,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,'{}',?,?)`, deployJobID, appID, actorID, stamp, stamp)
	mustExec(t, db, `INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number)
		VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, appID, releaseID, deployJobID, planID)
	names := []string{"api"}
	if componentCount == 2 {
		names = append(names, "web")
	}
	runtimeState := generatedruntimestate.New(db)
	state, created, err := runtimeState.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: names,
	})
	if err != nil || !created {
		t.Fatalf("runtime begin = %#v created=%t error=%v", state, created, err)
	}
	components := make([]applifecycle.RetainedComponent, 0, len(names))
	for _, name := range names {
		artifactID, containerID := uuid.NewString(), retainedDigest(name+"-container")
		network := "rig-a-" + strings.ReplaceAll(appID, "-", "")
		containerName := network + "-c-" + retainedDigest(name)[:12] + "-" + string(generatedruntime.SlotBlue)
		alias := "rig-c-" + retainedDigest(name)[:12] + "-" + string(generatedruntime.SlotBlue)
		definition, image := retainedDigest(name+"-definition"), "sha256:"+retainedDigest(name+"-image")
		mustExec(t, db, `INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at)
			VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, name, definition, image, stamp, stamp, stamp)
		if _, err := runtimeState.SetImageReady(context.Background(), appID, deploymentID, name, artifactID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeState.SetContainerStarting(context.Background(), appID, deploymentID, name, containerName); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeState.SetContainerRunning(context.Background(), appID, deploymentID, name, containerID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeState.AdvanceComponent(context.Background(), appID, deploymentID, name, generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
			t.Fatal(err)
		}
		components = append(components, applifecycle.RetainedComponent{
			Name: name, Role: generatedruntime.RoleServer, Technology: "node", ContainerID: containerID,
			ContainerName: containerName, ArtifactID: artifactID, ImageContentID: image,
			BuildDefinitionDigest: definition, NetworkName: network, NetworkAlias: alias, InternalPort: 8080,
			WorkingDirectory: "/workspace/apps/api", RunCommandDigest: retainedDigest("node server.js"),
			Limits: applifecycle.RetainedLimits{MemoryBytes: 512 << 20, MemorySwapBytes: 512 << 20,
				CPUQuota: 100000, CPUPeriod: 100000, PIDsLimit: 256, TmpfsBytes: 64 << 20,
				LogDriver: "local", LogMaxSize: "10m", LogMaxFiles: 3, HardeningPolicyVersion: "v1"},
		})
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute} {
		state, err = runtimeState.Advance(context.Background(), appID, deploymentID, state.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtimeState.SwitchActive(context.Background(), appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("active head = %#v changed=%t error=%v", head, changed, err)
	}
	state, err = runtimeState.Advance(context.Background(), appID, deploymentID, state.Phase, generatedruntimestate.PhaseDraining, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtimeState.Advance(context.Background(), appID, deploymentID, state.Phase, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE deployments SET status='succeeded',started_at=?,finished_at=? WHERE id=?`, stamp, stamp, deploymentID)
	mustExec(t, db, `UPDATE jobs SET status='succeeded',phase='succeeded' WHERE id=?`, deployJobID)
	pins := applifecycle.GeneratedLifecyclePinsV1{
		Version: 1, DeploymentID: deploymentID, ReleaseID: releaseID, Slot: head.Slot, ActiveGeneration: head.Generation,
		PlanRevision:        applifecycle.RevisionRef{ID: planID, Number: 1},
		Configuration:       applifecycle.ConfigurationRef{PlanRevision: applifecycle.RevisionRef{ID: planID, Number: 1}, RuntimeComponents: names},
		BaselineDisposition: baseline, Components: components,
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind", SelectedLineageGeneration: 1,
			SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("f", 64),
			ProtectedFormatVersion: 2, TargetRecordDigest: strings.Repeat("a", 64)},
	}
	accepted, err := repo.Accept(context.Background(), service, applifecycle.LifecycleRequest{
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
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := newRetainedAdapterRunner(components, appID, deploymentID, releaseID, planID)
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
	protectedRoot := filepath.Join(root, "candidate-protected")
	plans, err := deploymentplans.New(db, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	deploymentRepository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := generatedruntime.NewEngine(runner, &noEnvironmentStager{}, fixedAdapterCapacity{}, deploymentRepository, generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, ReplacementDiskBytes: 128 << 20,
		Limits: generatedruntime.ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewRetained(repo, engine)
	if err != nil {
		t.Fatal(err)
	}
	scope, releaseScope, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &retainedAdapterFixture{db: db, repo: repo, jobs: service, operation: accepted.Operation, claim: claim,
		adapter: adapter, runner: runner, ctx: deploymenteffects.WithScope(context.Background(), scope), release: releaseScope}
	t.Cleanup(func() { _ = releaseScope() })
	return fixture
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
}

type adapterContainer struct {
	pin          applifecycle.RetainedComponent
	appID        string
	deploymentID string
	releaseID    string
	planID       string
	status       string
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
		component := r.components[request.Args[len(request.Args)-1]]
		if component == nil {
			return runtimeprocess.CommandResult{Stderr: []byte("not found")}, errors.New("exit 1")
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
	return map[string]any{
		"id": c.pin.ContainerID, "name": "/" + c.pin.ContainerName, "image": c.pin.ImageContentID,
		"labels": map[string]string{"io.rig.managed": "generated-runtime", "io.rig.application": c.appID,
			"io.rig.release": c.releaseID, "io.rig.deployment": c.deploymentID, "io.rig.artifact": c.pin.ArtifactID,
			"io.rig.plan": c.planID, "io.rig.component": c.pin.Name, "io.rig.role": c.pin.Role, "io.rig.slot": "blue"},
		"user": "node", "workingDir": c.pin.WorkingDirectory, "cmd": []string{"/bin/sh", "-lc", "node server.js"},
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

func (*noEnvironmentStager) StageGeneratedCandidate(string, int, []byte) (deployments.GeneratedCandidateEnvironmentLease, error) {
	return nil, errors.New("retained adapter must not stage candidate environment")
}

type fixedAdapterCapacity struct{}

func (fixedAdapterCapacity) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 8 << 30, DiskAvailableBytes: 8 << 30}, nil
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
