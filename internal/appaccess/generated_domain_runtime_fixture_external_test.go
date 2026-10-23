package appaccess_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntime/liveauthority"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	generateddomaintest "github.com/hostd/hostd/internal/testsupport/generateddomain"
)

func init() {
	appaccess.RegisterGeneratedDomainRuntimeFixtureAdapter(newGeneratedDomainRuntimeFixture)
}

type generatedDomainRuntimeFixture struct {
	mu sync.Mutex

	dataRoot      string
	directories   runtimedocker.ControllerDirectories
	lease         *controllerowner.Lease
	database      *sql.DB
	plans         *deploymentplans.Store
	configuration *appconfig.Store
	stores        *deployments.GeneratedOwnerStores
	environment   *generatedruntime.SecureEnvironmentStager
	authority     *liveauthority.Authority
	process       *generateddomaintest.Runner
	fallback      *generatedDomainRuntimeCandidateRunner
	domain        *generatedingress.GeneratedDomain
	engine        *generatedruntime.Engine
	readers       []*sql.DB
	containerSeq  uint64
	artifacts     map[string]string
	closed        bool
}

func newGeneratedDomainRuntimeFixture(t *testing.T) appaccess.GeneratedDomainRuntimeFixture {
	t.Helper()
	ctx := context.Background()
	dataRoot := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controllerowner.Acquire(ctx, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenForGeneratedDomain(ctx, dataRoot, lease)
	if err != nil {
		_ = lease.Close()
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	runtimeTemporary, err := securetemp.NewGeneratedRuntime(dataRoot)
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	environment, err := generatedruntime.NewSecureEnvironmentStager(runtimeTemporary)
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	authority := liveauthority.NewWithStores(t, db, directories.WorkingDirectory, plans, configuration)
	stores, err := authority.Repository().GeneratedOwnerStores()
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	fallback := &generatedDomainRuntimeCandidateRunner{absent: generatedDomainRuntimeExitOne(t)}
	process := generateddomaintest.NewRunner(generateddomaintest.Options{
		Fallback: fallback, MemoryAvailableBytes: 8 << 30, DiskAvailableBytes: 8 << 30,
	})
	options := generatedruntime.EngineOptions{
		DockerExecutable: filepath.Join(dataRoot, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: 64 << 10, ReplacementDiskBytes: 128 << 20,
		Limits: generatedruntime.ContainerLimits{
			MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3,
		},
	}
	domain, err := generatedingress.NewGeneratedDomain(ctx, stores, process, environment, generatedingress.GeneratedDomainOptions{
		DataRoot: dataRoot, DockerExecutable: options.DockerExecutable, DockerConfigDirectory: options.DockerConfigDirectory,
		WorkingDirectory: options.WorkingDirectory, CommandTimeout: options.CommandTimeout, PullTimeout: 5 * time.Minute,
		HealthTimeout: options.HealthTimeout, HealthPollInterval: options.HealthPollInterval, OutputLimit: options.OutputLimit,
		MemoryBytes: options.Limits.MemoryBytes, MilliCPUs: options.Limits.MilliCPUs, PIDs: options.Limits.PIDs,
		TmpfsBytes: options.Limits.TmpfsBytes, LogSize: options.Limits.LogSize, LogFiles: options.Limits.LogFiles,
		ReplacementDiskBytes: options.ReplacementDiskBytes,
	})
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	engine, err := generatedruntime.NewEngine(process, environment, domain.Candidates(), options)
	if err != nil {
		_ = db.Close()
		_ = lease.Close()
		t.Fatal(err)
	}
	fixture := &generatedDomainRuntimeFixture{
		dataRoot: dataRoot, directories: directories, lease: lease, database: db, plans: plans,
		configuration: configuration, stores: stores, environment: environment, authority: authority,
		process: process, fallback: fallback, domain: domain, engine: engine,
	}
	t.Cleanup(fixture.Close)
	return fixture
}

func (f *generatedDomainRuntimeFixture) Database() *sql.DB {
	if f == nil {
		return nil
	}
	return f.database
}

func (f *generatedDomainRuntimeFixture) OpenSealedReader(t *testing.T) *sql.DB {
	t.Helper()
	if f == nil {
		t.Fatal("generated domain runtime fixture is unavailable")
	}
	f.mu.Lock()
	if f.closed || f.database == nil || f.lease == nil {
		f.mu.Unlock()
		t.Fatal("generated domain runtime fixture is closed")
	}
	dataRoot, owner, lease := f.dataRoot, f.database, f.lease
	f.mu.Unlock()
	ctx := context.Background()
	reader, err := database.OpenForGeneratedDomain(ctx, dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	ownerIdentity, err := database.GeneratedDomainFileIdentity(ctx, owner, dataRoot)
	if err != nil {
		_ = reader.Close()
		t.Fatal(err)
	}
	readerIdentity, err := database.GeneratedDomainFileIdentity(ctx, reader, dataRoot)
	if err != nil || !os.SameFile(ownerIdentity, readerIdentity) {
		_ = reader.Close()
		t.Fatalf("sealed reader identity=%v owner identity=%v error=%v", readerIdentity, ownerIdentity, err)
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		_ = reader.Close()
		t.Fatal("generated domain runtime fixture closed while opening reader")
	}
	f.readers = append(f.readers, reader)
	f.mu.Unlock()
	return reader
}

func (f *generatedDomainRuntimeFixture) Activate(t *testing.T, seed appaccess.GeneratedDomainRuntimeSeed) (generatedruntimestate.ActiveHead, []appaccess.GatewayStartupRuntimeComponent) {
	t.Helper()
	if f == nil || uuid.Validate(seed.AppID) != nil {
		t.Fatal("generated domain runtime activation seed is invalid")
	}
	f.mu.Lock()
	if f.closed || f.database == nil || f.authority == nil || f.domain == nil || f.engine == nil {
		f.mu.Unlock()
		t.Fatal("generated domain runtime fixture is unavailable")
	}
	db, authority, domain, engine := f.database, f.authority, f.domain, f.engine
	f.mu.Unlock()
	ctx := context.Background()
	artifactID := uuid.NewString()
	if seed.ReleaseID != "" {
		f.mu.Lock()
		artifactID = f.artifacts[seed.ReleaseID]
		f.mu.Unlock()
		if artifactID == "" {
			t.Fatal("generated domain runtime fixture release has no original image artifact")
		}
	}
	spec := authority.PrepareCandidate(t, ctx, generatedruntime.CandidateSpec{
		AppID: seed.AppID, ReleaseID: seed.ReleaseID, ArtifactID: artifactID, ComponentName: "api",
		Role: generatedruntime.RoleServer, Technology: "node", RootDirectory: "apps/api", RunCommand: "node server.js",
		InternalPort: 8080, HealthProbe: "/health", ImageContentID: "sha256:" + strings.Repeat("8", 64),
		BuildDefinitionDigest: strings.Repeat("7", 64),
	})
	f.mu.Lock()
	if f.artifacts == nil {
		f.artifacts = make(map[string]string)
	}
	if original := f.artifacts[spec.ReleaseID]; original != "" && original != spec.ArtifactID {
		f.mu.Unlock()
		t.Fatal("generated domain runtime fixture release changed image artifact")
	}
	f.artifacts[spec.ReleaseID] = spec.ArtifactID
	f.mu.Unlock()
	runtime := generatedruntimestate.New(db)
	predecessor, err := runtime.Active(ctx, seed.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if predecessor.DeploymentID != "" {
		previous, err := runtime.Get(ctx, seed.AppID, predecessor.DeploymentID)
		if err != nil || previous.Phase != generatedruntimestate.PhaseSucceeded {
			t.Fatalf("generated domain fixture previous activation remains unfinished: %+v error=%v", previous, err)
		}
	}
	session := authority.Begin(t, ctx, engine, spec)
	defer session.Close()
	bound := session.BoundSpec()
	if string(bound.ActiveSlot) != predecessor.Slot {
		t.Fatalf("candidate predecessor slot=%q, active slot=%q", bound.ActiveSlot, predecessor.Slot)
	}
	description, err := generatedruntime.DescribeInactiveCandidate(bound.AppID, bound.ComponentName, bound.ActiveSlot)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.containerSeq++
	containerID := fmt.Sprintf("%064x", f.containerSeq)
	f.mu.Unlock()
	f.fallback.setContainer(generatedDomainRuntimeCandidateContainer{
		spec: bound, description: description, containerID: containerID, status: "absent",
	})
	candidate, err := session.Start(t, bound)
	if err != nil {
		t.Fatalf("start generated domain candidate: %v (commands=%v)", err, f.fallback.commandKinds())
	}
	if err := session.WaitHealthyAndHandoff(); err != nil {
		t.Fatalf("handoff generated domain candidate: %v", err)
	}
	request := generatedroute.RouteSwitchRequest{
		AppID: candidate.AppID, FromSlot: generatedruntime.Slot(predecessor.Slot), ToSlot: candidate.Slot,
		Endpoints: []generatedroute.RouteEndpoint{{
			Component: candidate.Component, Role: candidate.Role, ContainerID: candidate.ContainerID,
			NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort,
		}},
	}
	if err := session.ActivateRoute(domain.Routes(), request); err != nil {
		t.Fatalf("activate generated domain route: %v (commands=%v)", err, f.fallback.commandKinds())
	}
	if predecessor.DeploymentID == "" {
		if _, err := session.PublishedHistory(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	head, components := f.currentRuntime(t, runtime, seed.AppID, candidate.DeploymentID, candidate.ReleaseID)
	deployment, err := runtime.Get(ctx, seed.AppID, candidate.DeploymentID)
	if err != nil || head.ActivationReceiptID == "" || head.Slot != string(candidate.Slot) || len(components) != 1 ||
		components[0].Name != candidate.Component || components[0].State != "active" || components[0].ContainerID != candidate.ContainerID {
		t.Fatalf("generated domain activated runtime head=%#v components=%#v deployment=%#v error=%v", head, components, deployment, err)
	}
	wantPhase := generatedruntimestate.PhaseSucceeded
	if predecessor.DeploymentID != "" {
		wantPhase = generatedruntimestate.PhaseDraining
	}
	if deployment.Phase != wantPhase {
		t.Fatalf("generated domain activation phase=%q want=%q", deployment.Phase, wantPhase)
	}
	return head, components
}

func (f *generatedDomainRuntimeFixture) currentRuntime(t *testing.T, runtime *generatedruntimestate.Repository, appID, deploymentID, releaseID string) (generatedruntimestate.ActiveHead, []appaccess.GatewayStartupRuntimeComponent) {
	t.Helper()
	head, err := runtime.Active(context.Background(), appID)
	if err != nil || head.AppID != appID || head.DeploymentID != deploymentID || head.ReleaseID != releaseID || head.Generation < 1 || head.ActivationReceiptID == "" {
		t.Fatalf("generated domain active head=%#v error=%v", head, err)
	}
	snapshot, err := appaccess.New(f.database).HostingGatewayStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundHead := false
	for _, value := range snapshot.RuntimeHeads {
		if value.AppID == head.AppID && value.DeploymentID == head.DeploymentID && value.ReleaseID == head.ReleaseID &&
			value.Slot == head.Slot && value.Generation == head.Generation && value.UpdatedAt.Equal(head.UpdatedAt) {
			foundHead = true
			break
		}
	}
	if !foundHead {
		t.Fatalf("current runtime head missing from gateway snapshot: %#v", snapshot.RuntimeHeads)
	}
	components := make([]appaccess.GatewayStartupRuntimeComponent, 0, 1)
	for _, value := range snapshot.RuntimeComponents {
		if value.AppID == head.AppID && value.DeploymentID == head.DeploymentID && value.ReleaseID == head.ReleaseID && value.Slot == head.Slot {
			components = append(components, value)
		}
	}
	if len(components) != 1 {
		t.Fatalf("current runtime component census=%#v", components)
	}
	return head, components
}

func (f *generatedDomainRuntimeFixture) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	readers, db, lease := f.readers, f.database, f.lease
	f.readers, f.database, f.lease = nil, nil, nil
	f.mu.Unlock()
	for _, reader := range readers {
		_ = reader.Close()
	}
	if db != nil {
		_ = db.Close()
	}
	if lease != nil {
		_ = lease.Close()
	}
}

type generatedDomainRuntimeCandidateRunner struct {
	mu         sync.Mutex
	current    *generatedDomainRuntimeCandidateContainer
	containers map[string]*generatedDomainRuntimeCandidateContainer
	networks   map[string]string
	absent     *exec.ExitError
	commands   []string
}

type generatedDomainRuntimeCandidateContainer struct {
	spec        generatedruntime.CandidateSpec
	description generatedruntime.CandidateDescription
	containerID string
	status      string
	receiptID   string
}

func (r *generatedDomainRuntimeCandidateRunner) setContainer(value generatedDomainRuntimeCandidateContainer) {
	r.mu.Lock()
	if r.containers == nil {
		r.containers = make(map[string]*generatedDomainRuntimeCandidateContainer)
	}
	if r.networks == nil {
		r.networks = make(map[string]string)
	}
	current := &value
	r.current = current
	r.containers[value.description.ContainerName] = current
	r.containers[value.containerID] = current
	r.networks[value.description.NetworkName] = value.spec.AppID
	r.mu.Unlock()
}

func (r *generatedDomainRuntimeCandidateRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	args := request.Args
	if len(args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain runtime command")
	}
	r.commands = append(r.commands, args[0]+" "+args[1])
	identity := args[len(args)-1]
	marshal := func(value any) (runtimeprocess.CommandResult, error) {
		body, err := json.Marshal(value)
		return runtimeprocess.CommandResult{Stdout: body}, err
	}
	switch args[0] + " " + args[1] {
	case "network inspect":
		appID := r.networks[identity]
		if appID == "" {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain app network")
		}
		return marshal(map[string]any{
			"name": identity, "driver": "bridge", "scope": "local", "internal": false,
			"labels": map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": appID},
		})
	case "image inspect":
		c := r.current
		if c == nil {
			return runtimeprocess.CommandResult{}, errors.New("generated domain candidate is unavailable")
		}
		if identity != c.spec.ImageContentID {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain candidate image")
		}
		return marshal(map[string]any{
			"id": c.spec.ImageContentID, "size": 1, "user": "node", "workingDir": "/workspace",
			"entrypoint": []string{generatedcontainer.Entrypoint},
			"labels": map[string]string{
				"io.rig.managed": "generated-image", "io.rig.application": c.spec.AppID,
				"io.rig.release": c.spec.ReleaseID, "io.rig.artifact": c.spec.ArtifactID,
				"io.rig.plan": c.spec.DeploymentPlanRevisionID, "io.rig.component": c.spec.ComponentName,
				"io.rig.role": c.spec.Role, "io.rig.definition": c.spec.BuildDefinitionDigest,
			},
		})
	case "container inspect":
		c := r.containers[identity]
		if c == nil {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain candidate container")
		}
		if c.status == "absent" {
			return runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + identity)}, r.absent
		}
		return marshal(c.inspection())
	case "container create":
		c := r.current
		if c == nil {
			return runtimeprocess.CommandResult{}, errors.New("generated domain candidate is unavailable")
		}
		name, receipt := "", ""
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--name" {
				name = args[index+1]
			}
			if args[index] == "--label" && strings.HasPrefix(args[index+1], generatedcontainer.ReceiptLabel+"=") {
				receipt = strings.TrimPrefix(args[index+1], generatedcontainer.ReceiptLabel+"=")
			}
		}
		if name != c.description.ContainerName || receipt == "" || c.status != "absent" {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain candidate create")
		}
		c.status, c.receiptID = "created", receipt
		return runtimeprocess.CommandResult{Stdout: []byte(c.containerID + "\n")}, nil
	case "container start":
		c := r.containers[identity]
		if c == nil {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain candidate start")
		}
		if identity != c.containerID || c.status != "created" {
			return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain candidate start")
		}
		c.status = "running"
		return runtimeprocess.CommandResult{}, nil
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected generated domain candidate command")
	}
}

func (r *generatedDomainRuntimeCandidateRunner) commandKinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

func (c *generatedDomainRuntimeCandidateContainer) inspection() generatedcontainer.Inspection {
	running := c.status == "running"
	networks := map[string]generatedcontainer.NetworkAttachment(nil)
	if running {
		networks = map[string]generatedcontainer.NetworkAttachment{
			c.description.NetworkName: {Aliases: []string{c.description.NetworkAlias}},
		}
	}
	startedAt := "2026-10-01T00:00:00Z"
	if c.status == "created" {
		startedAt = "0001-01-01T00:00:00Z"
	}
	return generatedcontainer.Inspection{
		ID: c.containerID, Name: "/" + c.description.ContainerName, Image: c.spec.ImageContentID,
		ConfigImage: c.spec.ImageContentID, Entrypoint: []string{generatedcontainer.Entrypoint},
		Volumes: map[string]json.RawMessage{}, HostMounts: []json.RawMessage{}, Mounts: []generatedcontainer.Mount{}, StartedAt: startedAt,
		Labels: map[string]string{
			"io.rig.managed": "generated-runtime", "io.rig.application": c.spec.AppID,
			"io.rig.release": c.spec.ReleaseID, "io.rig.deployment": c.spec.DeploymentID,
			"io.rig.artifact": c.spec.ArtifactID, "io.rig.plan": c.spec.DeploymentPlanRevisionID,
			"io.rig.component": c.spec.ComponentName, "io.rig.role": c.spec.Role,
			"io.rig.slot": string(c.description.Slot), generatedcontainer.ReceiptLabel: c.receiptID,
		},
		User: generatedcontainer.User, WorkingDirectory: "/workspace/apps/api", Command: []string{"/bin/sh", "-lc", c.spec.RunCommand},
		HealthTest: []string{"CMD-SHELL", generatedcontainer.HealthCommand}, HealthInterval: int64(2 * time.Second),
		HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: int64(512 << 20), MemorySwap: int64(512 << 20), NanoCPUs: int64(1_000_000_000), PIDs: int64(256),
		Ulimits: []generatedcontainer.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, Init: true,
		NetworkMode: c.description.NetworkName, ReadonlyRootfs: true, CapAdd: []string{}, CapDrop: []string{"ALL"},
		SecurityOptions: []string{"no-new-privileges:true"}, Binds: []string{}, PortBindings: map[string]json.RawMessage{},
		Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864"}, LogType: "local",
		LogConfig: map[string]string{"max-size": "10m", "max-file": "3"}, Restart: "no",
		Status: c.status, Running: running, ExitCode: 0,
		Health: map[bool]string{true: "healthy", false: ""}[running], Networks: networks,
	}
}

func TestGeneratedDomainRuntimeFixtureExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_APPACCESS_GENERATED_DOMAIN_EXIT") == "1" {
		os.Exit(1)
	}
}

func generatedDomainRuntimeExitOne(t *testing.T) *exec.ExitError {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestGeneratedDomainRuntimeFixtureExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_APPACCESS_GENERATED_DOMAIN_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected completed exit one, got %T %v", err, err)
	}
	return exited
}
