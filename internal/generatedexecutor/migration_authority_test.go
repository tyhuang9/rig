package generatedexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hostd/hostd/internal/generatedingress"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedmigration"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type executorMigrationAuthority struct {
	repository   *deployments.Repository
	runner       *executorMigrationRunner
	plan         deploymentplans.DeploymentPlanRevision
	release      releasesnapshot.Release
	artifact     generatedimage.Artifact
	claim        deploymentexecution.Claim
	deploymentID string
	state        *generatedruntimestate.Repository
	process      *executorMigrationProcess
	engine       *generatedruntime.Engine
	withScope    func(context.Context) context.Context
}

type executorMigrationOwnerFake struct {
	observed      generatedruntimestate.Deployment
	observeErr    error
	finishErr     error
	observeCalls  int
	beginCalls    int
	recordSuccess int
	recordFailure int
	finishCalls   int
	leaveCalls    int
}

func (f *executorMigrationOwnerFake) ObserveSucceeded() (generatedruntimestate.Deployment, error) {
	f.observeCalls++
	return f.observed, f.observeErr
}
func (f *executorMigrationOwnerFake) Begin() (deployments.GeneratedMigrationInvocation, generatedruntimestate.Deployment, error) {
	f.beginCalls++
	return nil, generatedruntimestate.Deployment{}, deployments.ErrInvalidTransition
}
func (f *executorMigrationOwnerFake) RecordSucceeded() (generatedruntimestate.Deployment, error) {
	f.recordSuccess++
	return generatedruntimestate.Deployment{}, deployments.ErrInvalidTransition
}
func (f *executorMigrationOwnerFake) RecordObservedFailure() (generatedruntimestate.Deployment, error) {
	f.recordFailure++
	return generatedruntimestate.Deployment{}, deployments.ErrInvalidTransition
}
func (f *executorMigrationOwnerFake) FinishPreparation() (generatedruntimestate.Deployment, error) {
	f.finishCalls++
	return f.observed, f.finishErr
}
func (f *executorMigrationOwnerFake) Leave() { f.leaveCalls++ }

type executorMigrationRunner struct {
	repository *deployments.Repository
}

func (r *executorMigrationRunner) Run(_ context.Context, invocation deployments.GeneratedMigrationInvocation) error {
	command, err := r.repository.PinGeneratedMigrationCommand(invocation)
	if err != nil {
		return err
	}
	defer command.Leave()
	if err := command.CreateAndInspect(); err != nil {
		return errors.Join(err, command.RemoveAndConfirm())
	}
	if err := command.StartAndInspect(); err != nil {
		return errors.Join(err, command.RemoveAndConfirm())
	}
	exitCode, waitErr := command.WaitAndInspectExited()
	cleanupErr := command.RemoveAndConfirm()
	if waitErr != nil {
		return errors.Join(waitErr, cleanupErr)
	}
	if exitCode != 0 {
		return errors.Join(errors.New("generated migration observed nonzero exit"), cleanupErr)
	}
	return cleanupErr
}

type executorMigrationStager struct {
	path    string
	cleaned bool
}

func (s *executorMigrationStager) StageGeneratedMigration(_ string, _ int, contents []byte) (deployments.GeneratedMigrationEnvironmentLease, error) {
	clear(contents)
	return &executorMigrationLease{stager: s, path: s.path}, nil
}

func (s *executorMigrationStager) Stage(operationID string, attempt int, contents []byte) (generatedruntime.EnvironmentLease, error) {
	lease, err := s.StageGeneratedMigration(operationID, attempt, contents)
	if err != nil {
		return nil, err
	}
	return lease.(*executorMigrationLease), nil
}

func (s *executorMigrationStager) StageGeneratedCandidate(operationID string, attempt int, contents []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	lease, err := s.StageGeneratedMigration(operationID, attempt, contents)
	if err != nil {
		return nil, err
	}
	return lease.(*executorMigrationLease), nil
}

type executorMigrationLease struct {
	stager *executorMigrationStager
	path   string
}

func (l *executorMigrationLease) Path() string { return l.path }
func (l *executorMigrationLease) Cleanup() error {
	l.stager.cleaned = true
	return nil
}

type executorMigrationProcess struct {
	id              string
	imageContentID  string
	imageLabels     map[string]string
	expectedNetwork string
	networkLabels   map[string]string
	name            string
	image           string
	user            string
	workdir         string
	command         string
	network         string
	labels          map[string]string
	status          string
	startedAt       string
	exitCode        int
	removed         bool
	candidate       bool
	alias           string
	health          string
	memory          int64
	memorySwap      int64
	nanoCPUs        int64
	pids            int64
	tmpfs           map[string]string
	logType         string
	logConfig       map[string]string
}

func (p *executorMigrationProcess) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	kind := strings.Join(request.Args[:min(2, len(request.Args))], " ")
	switch kind {
	case "image inspect":
		if request.Args[len(request.Args)-1] != p.imageContentID {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated migration image inspection")
		}
		body, err := json.Marshal(map[string]any{
			"id": p.imageContentID, "size": int64(1), "labels": p.imageLabels,
			"user": "node", "workingDir": "/workspace", "entrypoint": []string{"/usr/local/bin/rig-entrypoint"},
		})
		return runtimeprocess.CommandResult{Stdout: body}, err
	case "network inspect":
		if request.Args[len(request.Args)-1] != p.expectedNetwork {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated migration network inspection")
		}
		body, err := json.Marshal(map[string]any{
			"name": p.expectedNetwork, "driver": "bridge", "scope": "local", "internal": false, "labels": p.networkLabels,
		})
		return runtimeprocess.CommandResult{Stdout: body}, err
	case "container create":
		p.captureCreate(request.Args)
		p.status = "created"
		p.removed = false
		p.health = ""
		return runtimeprocess.CommandResult{Stdout: []byte(p.id + "\n")}, nil
	case "container start":
		p.status = "running"
		p.startedAt = "2026-10-20T12:00:00.123456789Z"
		if p.candidate {
			p.health = "healthy"
		}
		return runtimeprocess.CommandResult{Stdout: []byte(p.id + "\n")}, nil
	case "container wait":
		p.status = "exited"
		return runtimeprocess.CommandResult{Stdout: []byte(strconv.Itoa(p.exitCode) + "\n")}, nil
	case "container rm":
		p.removed = true
		return runtimeprocess.CommandResult{Stdout: []byte(p.id + "\n")}, nil
	case "container inspect":
		if p.removed || p.status == "" {
			return runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + request.Args[len(request.Args)-1])}, executorMigrationCompletedExitOne()
		}
		body, err := json.Marshal(p.inspection())
		return runtimeprocess.CommandResult{Stdout: body}, err
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected Docker command")
	}
}

func (p *executorMigrationProcess) captureCreate(args []string) {
	p.name = executorMigrationArgument(args, "--name")
	p.user = executorMigrationArgument(args, "--user")
	p.workdir = executorMigrationArgument(args, "--workdir")
	p.network = executorMigrationArgument(args, "--network")
	p.alias = executorMigrationArgument(args, "--network-alias")
	p.labels = map[string]string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--label" {
			key, value, ok := strings.Cut(args[index+1], "=")
			if ok {
				p.labels[key] = value
			}
		}
	}
	p.candidate = p.labels["io.rig.managed"] == "generated-runtime"
	if p.candidate {
		p.memory, _ = strconv.ParseInt(executorMigrationArgument(args, "--memory"), 10, 64)
		p.memorySwap, _ = strconv.ParseInt(executorMigrationArgument(args, "--memory-swap"), 10, 64)
		p.pids, _ = strconv.ParseInt(executorMigrationArgument(args, "--pids-limit"), 10, 64)
		cpus, _ := strconv.ParseFloat(executorMigrationArgument(args, "--cpus"), 64)
		p.nanoCPUs = int64(cpus * 1_000_000_000)
		p.tmpfs, p.logConfig = map[string]string{}, map[string]string{}
		for index := 0; index+1 < len(args); index++ {
			switch args[index] {
			case "--tmpfs":
				key, value, ok := strings.Cut(args[index+1], ":")
				if ok {
					p.tmpfs[key] = value
				}
			case "--log-opt":
				key, value, ok := strings.Cut(args[index+1], "=")
				if ok {
					p.logConfig[key] = value
				}
			}
		}
		p.logType = executorMigrationArgument(args, "--log-driver")
	}
	p.image = args[len(args)-4]
	p.command = args[len(args)-1]
}

func (p *executorMigrationProcess) inspection() map[string]any {
	running := p.status == "running"
	startedAt := "0001-01-01T00:00:00Z"
	if p.status == "running" || p.status == "exited" {
		startedAt = p.startedAt
	}
	var networks map[string]any
	if p.status == "running" || p.status == "exited" {
		networks = map[string]any{p.network: map[string]any{}}
	}
	if p.candidate {
		candidateNetworks := map[string]any{}
		if running {
			candidateNetworks[p.network] = map[string]any{"Aliases": []string{p.alias}}
		}
		return map[string]any{
			"id": p.id, "name": "/" + p.name, "image": p.image, "configImage": p.image,
			"user": p.user, "workingDir": p.workdir, "entrypoint": []string{"/usr/local/bin/rig-entrypoint"}, "cmd": []string{"/bin/sh", "-lc", p.command},
			"labels": p.labels, "volumes": map[string]any{},
			"healthTest":     []string{"CMD-SHELL", `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`},
			"healthInterval": int64(2 * time.Second), "healthTimeout": int64(2 * time.Second), "healthStartPeriod": int64(5 * time.Second), "healthRetries": 3,
			"memory": p.memory, "memorySwap": p.memorySwap, "nanoCpus": p.nanoCPUs, "pidsLimit": p.pids,
			"ulimits": []map[string]any{{"Name": "nofile", "Soft": int64(1024), "Hard": int64(1024)}}, "init": true, "networkMode": p.network,
			"readonlyRootfs": true, "privileged": false, "capAdd": []string{}, "capDrop": []string{"ALL"}, "securityOpt": []string{"no-new-privileges=true"},
			"binds": []string{}, "hostMounts": []any{}, "portBindings": map[string]any{}, "tmpfs": p.tmpfs,
			"logType": p.logType, "logConfig": p.logConfig, "restart": "no",
			"status": p.status, "running": running, "paused": false, "restarting": false, "dead": false, "exitCode": 0, "startedAt": startedAt, "health": p.health,
			"networks": candidateNetworks, "mounts": []any{},
		}
	}
	return map[string]any{
		"id": p.id, "name": "/" + p.name, "image": p.image, "configImage": p.image,
		"user": p.user, "workingDir": p.workdir, "entrypoint": []string{"/usr/local/bin/rig-entrypoint"}, "cmd": []string{"/bin/sh", "-lc", p.command},
		"labels": p.labels, "volumes": map[string]any{},
		"memory": int64(536870912), "memorySwap": int64(536870912), "cpuPeriod": int64(100000), "cpuQuota": int64(100000), "pidsLimit": int64(256),
		"ulimits": []map[string]any{{"Name": "nofile", "Soft": int64(1024), "Hard": int64(1024)}}, "init": true, "networkMode": p.network,
		"readonlyRootfs": true, "privileged": false, "capAdd": []string{}, "capDrop": []string{"ALL"}, "securityOpt": []string{"no-new-privileges"},
		"binds": []string{}, "hostMounts": []any{}, "portBindings": map[string]any{}, "tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864,mode=1777"},
		"logType": "json-file", "logConfig": map[string]string{"max-size": "1m", "max-file": "1"}, "restart": "no",
		"status": p.status, "running": running, "paused": false, "restarting": false, "dead": false, "exitCode": p.exitCode, "startedAt": startedAt,
		"networks": networks, "mounts": []any{},
	}
}

func TestGeneratedExecutorMigrationExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_GENERATED_EXECUTOR_MIGRATION_EXIT") == "1" {
		os.Exit(1)
	}
}

func executorMigrationCompletedExitOne() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "-test.run=^TestGeneratedExecutorMigrationExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_GENERATED_EXECUTOR_MIGRATION_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		return errors.New("executor migration absence helper did not complete with exit status one")
	}
	return exited
}

func executorMigrationArgument(args []string, name string) string {
	for index := range args {
		if args[index] == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func TestGeneratedExecutorRejectsCallerOnlyMigrationOutcomes(t *testing.T) {
	for _, test := range []struct {
		name      string
		runnerErr error
	}{
		{name: "public migration failed code", runnerErr: &generatedmigration.Error{Code: "migration_failed"}},
		{name: "nil runner result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, true)
			fixture.migrations.delegate = nil
			fixture.migrations.err = test.runnerErr
			_, err := fixture.execute(context.Background(), fixture.deploymentJob(), fixture.reporter)
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "runtime_unavailable" {
				t.Fatalf("error=%v", err)
			}
			if test.runnerErr != nil && !errors.Is(err, test.runnerErr) {
				t.Fatalf("runner cause was not preserved: %v", err)
			}
			persisted, getErr := fixture.migrationAuthority.state.Get(context.Background(), testAppID, fixture.migrationAuthority.deploymentID)
			if getErr != nil || persisted.Phase != generatedruntimestate.PhaseMigrating || persisted.MigrationState != generatedruntimestate.MigrationRunning {
				t.Fatalf("unproved result changed durable outcome: runtime=%+v err=%v", persisted, getErr)
			}
			if fixture.deployments.deployment.Status != deployments.Applying || fixture.runtime.stopped != 0 || fixture.runtime.retained != 0 {
				t.Fatalf("migration uncertainty used detached failure or cleanup: deployment=%+v stopped=%d retained=%d", fixture.deployments.deployment, fixture.runtime.stopped, fixture.runtime.retained)
			}
		})
	}
}

func TestGeneratedExecutorPersistsOnlyPrivatelyObservedNonzeroMigrationExit(t *testing.T) {
	fixture := newExecutorFixture(t, true)
	fixture.migrationAuthority.process.exitCode = 17
	_, err := fixture.execute(context.Background(), fixture.deploymentJob(), fixture.reporter)
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "apply_failed" {
		t.Fatalf("error=%v", err)
	}
	persisted, getErr := fixture.migrationAuthority.state.Get(context.Background(), testAppID, fixture.migrationAuthority.deploymentID)
	if getErr != nil || persisted.Phase != generatedruntimestate.PhaseMigrating || persisted.MigrationState != generatedruntimestate.MigrationFailed {
		t.Fatalf("observed nonzero outcome=%+v err=%v", persisted, getErr)
	}
	if fixture.deployments.deployment.Status != deployments.Applying || fixture.runtime.stopped != 0 {
		t.Fatalf("observed migration failure used detached terminal path: deployment=%+v stopped=%d", fixture.deployments.deployment, fixture.runtime.stopped)
	}
}

func TestGeneratedExecutorRetainsCandidateRiskOnSucceededHandoffUncertainty(t *testing.T) {
	fixture := newExecutorFixture(t, true)
	release := fixture.executor.releases.(*fakeReleases).release
	fixture.deployments.deployment = deployments.Deployment{
		ID: fixture.migrationAuthority.deploymentID, AppID: testAppID, JobID: testJobID, Status: deployments.Applying,
		ConfigurationMode: "current", ReleaseID: release.ID, ActualConfigurationRevisionID: release.ConfigurationRevisionID,
		ActualConfigurationRevisionNumber: release.ConfigurationRevisionNumber, RuntimeStrategy: deployments.RuntimeGeneratedNode,
		DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber, ProvenanceInitialized: true,
	}
	fixture.state.deployment = generatedruntimestate.Deployment{
		DeploymentID: fixture.migrationAuthority.deploymentID, AppID: testAppID, ReleaseID: release.ID,
		DeploymentPlanRevisionID: fixture.plan.ID, DeploymentPlanRevisionNumber: fixture.plan.RevisionNumber, CandidateSlot: "blue",
		Phase: generatedruntimestate.PhaseMigrating, MigrationState: generatedruntimestate.MigrationSucceeded,
		Components: []generatedruntimestate.Component{{DeploymentID: fixture.migrationAuthority.deploymentID, Name: "api", Slot: "blue", ImageArtifactID: fixture.migrationAuthority.artifact.ID, State: generatedruntimestate.ComponentImageReady, ContainerName: "uncertain-candidate"}},
	}
	handoffErr := errors.New("migration handoff acknowledgement unavailable")
	owner := &executorMigrationOwnerFake{observed: fixture.state.deployment, finishErr: handoffErr}
	fixture.executor.pinMigration = func(context.Context, deployments.GeneratedMigrationRequest) (generatedMigrationExecution, error) {
		return owner, nil
	}
	_, err := fixture.execute(context.Background(), fixture.deploymentJob(), fixture.reporter)
	if !errors.Is(err, handoffErr) {
		t.Fatalf("handoff cause was not preserved: %v", err)
	}
	if owner.observeCalls != 1 || owner.finishCalls != 1 || owner.beginCalls != 0 || owner.recordSuccess != 0 || owner.recordFailure != 0 || owner.leaveCalls != 1 {
		t.Fatalf("owner calls=%+v", owner)
	}
	if len(fixture.migrations.requests) != 0 || fixture.runtime.stopped != 0 || fixture.runtime.retained != 1 || fixture.state.deployment.Phase != generatedruntimestate.PhaseMigrating || fixture.state.deployment.MigrationState != generatedruntimestate.MigrationSucceeded || fixture.deployments.deployment.Status != deployments.Applying {
		t.Fatalf("handoff uncertainty lost evidence or capacity: runtime=%+v deployment=%+v runs=%d stopped=%d retained=%d", fixture.state.deployment, fixture.deployments.deployment, len(fixture.migrations.requests), fixture.runtime.stopped, fixture.runtime.retained)
	}
}

func newExecutorMigrationAuthority(t *testing.T, component deploymentplans.Component) *executorMigrationAuthority {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	machineID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, testActorID, testActorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machineID, "local", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?, 'draft',?,?)`, testAppID, testAppID, testAppID, machineID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',0,?,'{}',?,?)`, testJobID, testAppID, testActorID, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindings.InsertNewTx(context.Background(), tx, testJobID, time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=2,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, testJobID); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(binding, testJobID, testAppID, 2)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	scope, releaseScope, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := releaseScope(); err != nil {
			t.Errorf("release migration test scope: %v", err)
		}
	})

	plans, err := deploymentplans.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory", "nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe"}
	provenance := make([]deploymentplans.FieldProvenance, 0, len(fields))
	for _, field := range fields {
		provenance = append(provenance, deploymentplans.FieldProvenance{Field: "components.api." + field, Origin: deploymentplans.ProvenanceInferred, Confidence: 100, Evidence: []string{"package.json"}})
	}
	planValue := deploymentplans.Plan{
		Strategy:   deploymentplans.StrategyGeneratedNode,
		Detector:   deploymentplans.Detector{Name: "package-json", Version: "1", SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: strings.Repeat("c", 64)},
		Components: []deploymentplans.Component{component}, FieldProvenance: provenance,
		Migration: &deploymentplans.Migration{ComponentName: "api", RootDirectory: ".", Command: "npm run migrate", EnvironmentKeys: []string{"DATABASE_URL"}, EvidenceDigest: strings.Repeat("b", 64), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}},
	}
	revision, err := plans.Replace(context.Background(), testAppID, testActorID, deploymentplans.ReplaceInput{Plan: planValue})
	if err != nil {
		t.Fatal(err)
	}
	if err := plans.ApproveMigration(context.Background(), testAppID, revision.ID, revision.RevisionNumber, 0, testActorID); err != nil {
		t.Fatal(err)
	}
	revision, err = plans.GetRevision(context.Background(), testAppID, revision.ID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	secret := "postgres://application-owned"
	config, err := configuration.ReplaceScoped(context.Background(), testAppID, testActorID, appconfig.ScopedReplaceInput{
		PlanRevisionID: revision.ID, PlanRevisionNumber: revision.RevisionNumber,
		Components: []appconfig.ComponentTarget{{Name: "api", Role: "server"}},
		Entries:    []appconfig.ScopedValueInput{{ScopedKey: appconfig.ScopedKey{Phase: appconfig.PhaseMigration, Component: "api", Key: "DATABASE_URL"}, Sensitivity: appconfig.SensitivitySecret, Value: &secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceDigest := strings.Repeat("f", 64)
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`, testReleaseID, testAppID, stamp, strings.Repeat("c", 64), workspaceDigest, config.RevisionID, config.RevisionNumber, revision.ID, revision.RevisionNumber); err != nil {
		t.Fatal(err)
	}

	dockerConfig := t.TempDir()
	stager := &executorMigrationStager{path: filepath.Join(t.TempDir(), "migration.env")}
	process := &executorMigrationProcess{id: strings.Repeat("1", 64)}
	network, err := generatedidentity.DescribeAppNetwork(testAppID)
	if err != nil {
		t.Fatal(err)
	}
	process.expectedNetwork = network.Name
	process.networkLabels = map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": testAppID}
	options := deployments.GeneratedMigrationProcessOptions{DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerConfigDirectory: dockerConfig, WorkingDirectory: t.TempDir(), Timeout: time.Minute, OutputLimit: 16 << 10}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedMigration(db, guard, plans, configuration, stager, process, options)
	if err != nil {
		t.Fatal(err)
	}
	deployment, _, err := repository.GetOrCreateByJob(context.Background(), testAppID, testJobID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = repository.InitializeRuntime(context.Background(), testAppID, deployment.ID, testReleaseID, config.RevisionID, config.RevisionNumber, deployments.RuntimeGeneratedNode, revision.ID, revision.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	guardedContext := deploymenteffects.WithScope(context.Background(), scope)
	if err := repository.Gate(guardedContext, deployments.GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatal(err)
	}
	state := generatedruntimestate.New(db)
	runtimeDeployment, _, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: testAppID, ReleaseID: testReleaseID, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber, ComponentNames: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	runtimeDeployment, err = state.Advance(context.Background(), testAppID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := generatedimage.NewArtifactRepository(db)
	artifact, _, err := artifacts.Begin(context.Background(), generatedimage.BeginArtifactInput{ReleaseID: testReleaseID, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber, ComponentID: "api", CompilerVersion: "executor-test", BuildDefinitionDigest: strings.Repeat("d", 64)})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = artifacts.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	process.imageContentID = artifact.ImageContentID
	process.imageLabels = map[string]string{
		"io.rig.managed": "generated-image", "io.rig.application": testAppID,
		"io.rig.release": testReleaseID, "io.rig.artifact": artifact.ID,
		"io.rig.plan": revision.ID, "io.rig.component": component.Name,
		"io.rig.role": component.Role, "io.rig.definition": artifact.BuildDefinitionDigest,
	}
	if _, err := state.SetImageReady(context.Background(), testAppID, deployment.ID, "api", artifact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Advance(context.Background(), testAppID, deployment.ID, runtimeDeployment.Phase, generatedruntimestate.PhaseMigrating, ""); err != nil {
		t.Fatal(err)
	}
	capacity := &scriptedCapacitySource{}
	capacity.Set(generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 512 << 20, DiskAvailableBytes: 512 << 20})
	engine, err := generatedruntime.NewEngine(process, stager, capacity, repository, generatedruntime.EngineOptions{
		DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory,
		WorkingDirectory: options.WorkingDirectory, CommandTimeout: time.Minute, HealthTimeout: time.Minute,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: options.OutputLimit,
		Limits:               generatedruntime.ContainerLimits{MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 64, TmpfsBytes: 8 << 20, LogSize: "1m", LogFiles: 2},
		ReplacementDiskBytes: 128 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &executorMigrationAuthority{
		repository: repository, runner: &executorMigrationRunner{repository: repository}, plan: revision,
		release:  releasesnapshot.Release{ID: testReleaseID, AppID: testAppID, SourceProvider: "local", ResolvedSHA: strings.Repeat("c", 64), WorkspaceTreeSHA256: workspaceDigest, ConfigurationRevisionID: config.RevisionID, ConfigurationRevisionNumber: config.RevisionNumber, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber},
		artifact: artifact, claim: claim, deploymentID: deployment.ID, state: state, process: process, engine: engine,
		withScope: func(ctx context.Context) context.Context { return deploymenteffects.WithScope(ctx, scope) },
	}
}
