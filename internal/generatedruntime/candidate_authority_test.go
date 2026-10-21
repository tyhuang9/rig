package generatedruntime

import (
	"encoding/json"
	"errors"
	"github.com/hostd/hostd/internal/generatedingress"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/generatedcontainer"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGeneratedRuntimeCandidateExitOneHelper(t *testing.T) {
	if os.Getenv("RIG_TEST_GENERATED_RUNTIME_CANDIDATE_EXIT") == "1" {
		os.Exit(1)
	}
}

func runtimeCandidateExitOne(t *testing.T) *exec.ExitError {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestGeneratedRuntimeCandidateExitOneHelper$")
	command.Env = append(os.Environ(), "RIG_TEST_GENERATED_RUNTIME_CANDIDATE_EXIT=1")
	err = command.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected completed candidate exit one, got %T %v", err, err)
	}
	return exited
}

// runtimeCandidateSession wires candidate tests through the concrete
// repository owner. It carries only test setup; receipt, policy and cleanup
// authority remain inside generatedingress.GeneratedCandidateExecution.
type runtimeCandidateSession struct {
	engine      *Engine
	runner      *runtimeFakeRunner
	environment *runtimeFakeEnvironment
	authority   runtimeNetworkAuthority
	request     generatedingress.GeneratedCandidateRequest
	reservation ReplacementReservation
	execution   *generatedingress.GeneratedCandidateExecution
}

func newRuntimeCandidateSession(
	t *testing.T,
	spec CandidateSpec,
	limits ContainerLimits,
	capacity CapacitySource,
	run func(runtimeprocess.CommandRequest) runtimeRequestResult,
) *runtimeCandidateSession {
	return newRuntimeCandidateSessionWithConfiguration(t, spec, limits, capacity, nil, run)
}

func newRuntimeCandidateSessionWithConfiguration(
	t *testing.T,
	spec CandidateSpec,
	limits ContainerLimits,
	capacity CapacitySource,
	configurationEntries []appconfig.ScopedValueInput,
	run func(runtimeprocess.CommandRequest) runtimeRequestResult,
) *runtimeCandidateSession {
	t.Helper()
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtimeFakeRunner{run: run}
	environment := &runtimeFakeEnvironment{path: filepath.Join(root, "candidate.env")}
	authority := newRuntimeNetworkAuthorityWithConfiguration(t, directories.WorkingDirectory, spec, configurationEntries)
	if capacity == nil {
		capacity = fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}
	}
	options := EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, Limits: limits,
	}
	engine, err := NewEngine(runner, environment, capacity, authority.repository, options)
	if err != nil {
		t.Fatalf("compose candidate engine: %v", err)
	}
	candidateSlot, err := InactiveSlot(authority.spec.ActiveSlot)
	if err != nil {
		t.Fatal(err)
	}
	request := generatedingress.GeneratedCandidateRequest{
		Claim: authority.spec.Network.Claim, DeploymentID: authority.spec.DeploymentID, ReleaseID: authority.spec.ReleaseID,
		ConfigurationRevisionID: authority.spec.ConfigurationRevisionID, ConfigurationRevisionNumber: authority.spec.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: authority.spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: authority.spec.DeploymentPlanRevisionNumber,
		CandidateSlot: string(candidateSlot),
	}
	return &runtimeCandidateSession{engine: engine, runner: runner, environment: environment, authority: authority, request: request}
}

func newDefaultRuntimeCandidateSession(
	t *testing.T,
	spec CandidateSpec,
	run func(runtimeprocess.CommandRequest) runtimeRequestResult,
) *runtimeCandidateSession {
	t.Helper()
	return newRuntimeCandidateSession(t, spec, defaultLimits(), nil, run)
}

func newPreparedRuntimeCandidate(
	t *testing.T,
	spec CandidateSpec,
	limits ContainerLimits,
	capacity CapacitySource,
) (*runtimeCandidateSession, *runtimeCandidateProgram, generatedingress.GeneratedCandidateInvocation) {
	return newPreparedRuntimeCandidateWithConfiguration(t, spec, limits, capacity, nil)
}

func newPreparedRuntimeCandidateWithConfiguration(
	t *testing.T,
	spec CandidateSpec,
	limits ContainerLimits,
	capacity CapacitySource,
	configurationEntries []appconfig.ScopedValueInput,
) (*runtimeCandidateSession, *runtimeCandidateProgram, generatedingress.GeneratedCandidateInvocation) {
	t.Helper()
	var program *runtimeCandidateProgram
	session := newRuntimeCandidateSessionWithConfiguration(t, spec, limits, capacity, configurationEntries, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		return program.run(request)
	})
	program = newRuntimeCandidateProgram(t, session.authority.spec, limits)
	session.reserveAndPin(t)
	invocation := session.beginCreate(t)
	program.bind(invocation, session)
	return session, program, invocation
}

func (s *runtimeCandidateSession) reserveAndPin(t *testing.T) {
	t.Helper()
	reservation, err := s.engine.ReserveReplacement(s.authority.context, generatedingress.GeneratedReplacementRequest(s.request))
	if err != nil {
		t.Fatalf("reserve receipt-bound candidate: %v", err)
	}
	s.reservation = reservation
	t.Cleanup(reservation.Release)
	execution, err := s.engine.PinCandidates(s.authority.context, s.request, reservation)
	if err != nil {
		t.Fatalf("pin receipt-bound candidates: %v", err)
	}
	s.execution = execution
	t.Cleanup(execution.Leave)
}

func (s *runtimeCandidateSession) beginCreate(t *testing.T) generatedingress.GeneratedCandidateInvocation {
	t.Helper()
	value, err := s.execution.BeginCreate(s.authority.spec.ComponentName)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (s *runtimeCandidateSession) identity(value generatedingress.GeneratedCandidateInvocation) generatedingress.CandidateIdentity {
	identity := value.Identity()
	return generatedingress.CandidateIdentity{
		ReceiptID: identity.ReceiptID, DeploymentID: s.authority.spec.DeploymentID, ReleaseID: s.authority.spec.ReleaseID,
		ArtifactID: s.authority.spec.ArtifactID, DeploymentPlanRevisionID: s.authority.spec.DeploymentPlanRevisionID,
		ComponentName: identity.ComponentName, Role: s.authority.spec.Role, Technology: s.authority.spec.Technology,
		Slot: s.request.CandidateSlot, ContainerName: containerName(s.authority.spec.AppID, identity.ComponentName, Slot(s.request.CandidateSlot)),
		NetworkName: networkName(s.authority.spec.AppID), NetworkAlias: containerAlias(identity.ComponentName, Slot(s.request.CandidateSlot)),
		InternalPort: s.authority.spec.InternalPort,
	}
}

func (s *runtimeCandidateSession) finishStarting(t *testing.T) {
	t.Helper()
	if _, err := s.execution.FinishStarting(); err != nil {
		t.Fatal(err)
	}
}

type runtimeCandidateProgram struct {
	t          *testing.T
	spec       CandidateSpec
	identity   generatedingress.CandidateIdentity
	limits     ContainerLimits
	absenceErr error
	created    bool
	started    bool
	running    bool
	removed    bool
	health     string
	image      func(imageInspection) imageInspection
	network    func(networkInspection) networkInspection
	inspect    func(generatedcontainer.Inspection) generatedcontainer.Inspection
	create     func() runtimeRequestResult
	start      func() runtimeRequestResult
}

func newRuntimeCandidateProgram(t *testing.T, spec CandidateSpec, limits ContainerLimits) *runtimeCandidateProgram {
	t.Helper()
	return &runtimeCandidateProgram{t: t, spec: spec, limits: limits, absenceErr: runtimeCandidateExitOne(t), health: "starting"}
}

func (p *runtimeCandidateProgram) bind(value generatedingress.GeneratedCandidateInvocation, session *runtimeCandidateSession) {
	p.identity = session.identity(value)
}

func (p *runtimeCandidateProgram) run(request runtimeprocess.CommandRequest) runtimeRequestResult {
	p.t.Helper()
	switch commandKind(request.Args) {
	case "image inspect":
		value := validImageInspection(p.spec)
		if p.image != nil {
			value = p.image(value)
		}
		return runtimeJSON(value)
	case "network inspect":
		value := networkInspection{Name: networkName(p.spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": NetworkOwnershipLabelValue, "io.rig.application": p.spec.AppID}}
		if p.network != nil {
			value = p.network(value)
		}
		return runtimeJSON(value)
	case "container inspect":
		target := request.Args[len(request.Args)-1]
		if target != p.identity.ContainerName && target != testContainerID {
			p.t.Fatalf("candidate inspected unexpected target %q", target)
		}
		if !p.created || p.removed {
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("Error: No such container: " + target)}, err: p.absenceErr}
		}
		status := "created"
		if p.started && !p.running {
			status = "exited"
		}
		if p.running {
			status = "running"
		}
		value := candidateInspection(p.spec, p.identity, p.limits, testContainerID, status, p.health, p.running)
		if p.inspect != nil {
			value = p.inspect(value)
		}
		return runtimeJSON(value)
	case "container create":
		p.created = true
		if p.create != nil {
			return p.create()
		}
		return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(testContainerID)}}
	case "container start":
		p.running = true
		p.started = true
		if p.start != nil {
			return p.start()
		}
		return runtimeRequestResult{}
	case "container stop":
		p.running = false
		return runtimeRequestResult{}
	case "container rm":
		p.removed = true
		return runtimeRequestResult{}
	default:
		p.t.Fatalf("unexpected candidate request: %#v", request.Args)
		return runtimeRequestResult{err: errors.New("unreachable candidate request")}
	}
}

func candidateInspection(
	spec CandidateSpec,
	identity generatedingress.CandidateIdentity,
	limits ContainerLimits,
	containerID string,
	status string,
	health string,
	realizedNetwork bool,
) generatedcontainer.Inspection {
	labels := map[string]string{
		"io.rig.managed": "generated-runtime", "io.rig.application": spec.AppID,
		"io.rig.release": identity.ReleaseID, "io.rig.deployment": identity.DeploymentID,
		"io.rig.artifact": identity.ArtifactID, "io.rig.plan": identity.DeploymentPlanRevisionID,
		"io.rig.component": identity.ComponentName, "io.rig.role": identity.Role,
		"io.rig.slot": identity.Slot, generatedcontainer.ReceiptLabel: identity.ReceiptID,
	}
	startedAt := "0001-01-01T00:00:00Z"
	running := false
	if status == "running" || status == "exited" {
		startedAt = time.Now().UTC().Format(time.RFC3339Nano)
		running = status == "running"
	}
	networks := map[string]generatedcontainer.NetworkAttachment{}
	if realizedNetwork {
		networks[identity.NetworkName] = generatedcontainer.NetworkAttachment{Aliases: []string{identity.NetworkAlias}}
	}
	tmpfs := map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=" + stringInt(limits.TmpfsBytes)}
	if identity.Technology == "nextjs" {
		tmpfs[nextCacheDirectory(runtimeWorkingDirectory(spec.RootDirectory))] = "rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=" + stringInt(nextCacheTmpfsBytes)
	}
	return generatedcontainer.Inspection{
		ID: containerID, Name: "/" + identity.ContainerName, Image: spec.ImageContentID, ConfigImage: spec.ImageContentID,
		Labels: labels, User: containerUser, WorkingDirectory: runtimeWorkingDirectory(spec.RootDirectory),
		Entrypoint: []string{"/usr/local/bin/rig-entrypoint"}, Command: []string{"/bin/sh", "-lc", spec.RunCommand},
		Volumes: map[string]json.RawMessage{}, HealthTest: []string{"CMD-SHELL", healthCommand},
		HealthInterval: int64(2 * time.Second), HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: limits.MemoryBytes, MemorySwap: limits.MemoryBytes, NanoCPUs: limits.MilliCPUs * 1_000_000, PIDs: limits.PIDs,
		Ulimits: []generatedcontainer.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, Init: true, NetworkMode: identity.NetworkName,
		ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges:true"},
		Binds: []string{}, HostMounts: []json.RawMessage{}, PortBindings: map[string]json.RawMessage{}, Tmpfs: tmpfs,
		LogType: "local", LogConfig: map[string]string{"max-size": limits.LogSize, "max-file": stringInt(int64(limits.LogFiles))}, Restart: "no",
		Status: status, Running: running, Paused: false, Restarting: false, Dead: false, ExitCode: 0, StartedAt: startedAt,
		Health: health, Networks: networks, Mounts: []generatedcontainer.Mount{},
	}
}
