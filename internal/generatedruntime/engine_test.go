package generatedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/google/uuid"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	testImageID     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testContainerID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type runtimeRequestResult struct {
	result runtimeprocess.CommandResult
	err    error
}

type runtimeFakeRunner struct {
	requests        []runtimeprocess.CommandRequest
	rejectCancelled bool
	run             func(runtimeprocess.CommandRequest) runtimeRequestResult
}

func (r *runtimeFakeRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	r.requests = append(r.requests, request)
	if r.rejectCancelled && ctx.Err() != nil {
		return runtimeprocess.CommandResult{}, ctx.Err()
	}
	response := r.run(request)
	return response.result, response.err
}

type runtimeFakeEnvironment struct {
	path       string
	contents   []byte
	staged     int
	cleaned    int
	stageError error
}

func (s *runtimeFakeEnvironment) Stage(_ string, _ int, contents []byte) (EnvironmentLease, error) {
	s.staged++
	s.contents = append([]byte(nil), contents...)
	clear(contents)
	if s.stageError != nil {
		return nil, s.stageError
	}
	return &runtimeFakeEnvironmentLease{path: s.path, cleanup: func() { s.cleaned++ }}, nil
}

type runtimeFakeEnvironmentLease struct {
	path    string
	cleanup func()
}

func (l *runtimeFakeEnvironmentLease) Path() string { return l.path }
func (l *runtimeFakeEnvironmentLease) Cleanup() error {
	l.cleanup()
	return nil
}

func TestGeneratedRuntimeCandidateLifecycleUsesExactHardenedDockerArguments(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "docker-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := candidateSpec()
	command := `node server.js && printf '$() ${TOKEN}' \\unicode-✓`
	spec.RunCommand = command
	environment := &runtimeFakeEnvironment{path: filepath.Join(root, "runtime.env")}
	limits := ContainerLimits{MemoryBytes: 384 << 20, MilliCPUs: 750, PIDs: 192, TmpfsBytes: 32 << 20, LogSize: "5m", LogFiles: 2}
	networkInspections := 0
	containerInspections := 0
	runner := &runtimeFakeRunner{}
	runner.run = func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(spec))
		case "network inspect":
			networkInspections++
			if networkInspections == 1 {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("Error: No such network")}, err: errors.New("exit 1")}
			}
			return runtimeJSON(networkInspection{Name: networkName(spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": spec.AppID}})
		case "network create":
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(strings.Repeat("c", 64))}}
		case "container inspect":
			identity := request.Args[len(request.Args)-1]
			if identity != testContainerID {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("Error: No such container")}, err: errors.New("exit 1")}
			}
			containerInspections++
			inspection := hardenedInspection(spec, limits)
			switch containerInspections {
			case 1, 2:
				inspection.Running = false
				inspection.Health = ""
			case 3:
				inspection.Running = true
				inspection.Health = "starting"
			default:
				inspection.Running = true
				inspection.Health = "healthy"
			}
			return runtimeJSON(inspection)
		case "container create":
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(testContainerID + "\n")}}
		case "container start", "container stop", "container rm":
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(testContainerID + "\n")}}
		default:
			t.Fatalf("unexpected Docker request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	}
	engine, err := NewEngine(runner, environment, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: config,
		WorkingDirectory: root, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: time.Millisecond * 100, Limits: limits, ReplacementDiskBytes: 128 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := engine.ReserveReplacement(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.Reservation = reservation
	candidate, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Slot != SlotGreen || candidate.ContainerName != containerName(spec.AppID, spec.ComponentName, SlotGreen) || candidate.NetworkName != networkName(spec.AppID) {
		t.Fatalf("unexpected candidate identity: %+v", candidate)
	}
	if candidate.Role != RoleServer || candidate.lease != nil {
		t.Fatalf("candidate did not consume aggregate admission: %+v", candidate)
	}
	if err := engine.StartCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := engine.WaitHealthy(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := engine.StopAndRemove(context.Background(), candidate, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	reservation.Release()
	if environment.cleaned != 1 || string(environment.contents) != "SECRET='value'\n" {
		t.Fatalf("environment was not scoped and cleaned: cleaned=%d contents=%q", environment.cleaned, environment.contents)
	}

	create := findRuntimeRequest(t, runner.requests, "container create")
	assertArgumentPair(t, create.Args, "--env-file", environment.path)
	assertArgumentPair(t, create.Args, "--workdir", "/workspace/apps/api")
	assertArgumentPair(t, create.Args, "--user", containerUser)
	assertArgumentPair(t, create.Args, "--network", networkName(spec.AppID))
	assertArgumentPair(t, create.Args, "--memory", "402653184")
	assertArgumentPair(t, create.Args, "--memory-swap", "402653184")
	assertArgumentPair(t, create.Args, "--cpus", "0.750")
	assertArgumentPair(t, create.Args, "--pids-limit", "192")
	assertArgumentPair(t, create.Args, "--health-cmd", healthCommand)
	assertArgumentPair(t, create.Args, "--label", "io.rig.role="+RoleServer)
	if got := create.Args[len(create.Args)-4:]; !reflect.DeepEqual(got, []string{spec.ImageContentID, "/bin/sh", "-lc", command}) {
		t.Fatalf("runtime command lost exact argument boundaries: %#v", got)
	}
	for _, prohibited := range []string{"--publish", "-p", "--volume", "-v", "--mount", "--privileged", "/var/run/docker.sock"} {
		if (strings.HasPrefix(prohibited, "-") && containsExactArgument(create.Args, prohibited)) || (!strings.HasPrefix(prohibited, "-") && containsArgument(create.Args, prohibited)) {
			t.Fatalf("prohibited Docker argument present: %s in %#v", prohibited, create.Args)
		}
	}
	for _, request := range runner.requests {
		if containsEnvironmentValue(request.Env, "SECRET") || containsEnvironmentValue(request.Env, "value") {
			t.Fatalf("application configuration escaped into Docker client environment: %#v", request.Env)
		}
		if request.Executable != filepath.Join(root, "docker.exe") || request.Directory != root {
			t.Fatalf("unscoped Docker execution: %+v", request)
		}
	}
	stop := findRuntimeRequest(t, runner.requests, "container stop")
	assertArgumentPair(t, stop.Args, "--time", "2")
}

func TestGeneratedRuntimeRejectsImageLabelDriftBeforeDockerMutation(t *testing.T) {
	expected := candidateSpec()
	engine, runner, spec := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "image inspect" {
			t.Fatalf("unexpected mutation after image drift: %#v", request.Args)
		}
		labels := imageLabels(expected)
		labels["io.rig.application"] = uuid.NewString()
		image := validImageInspection(expected)
		image.Labels = labels
		return runtimeJSON(image)
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticImageDriftDetected) {
		t.Fatalf("expected image drift, got %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("image drift reached Docker mutation: %d requests", len(runner.requests))
	}
}

func TestGeneratedRuntimeRejectsImageComponentDriftBeforeDockerMutation(t *testing.T) {
	engine, runner, spec := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "image inspect" {
			t.Fatalf("component drift reached Docker mutation: %#v", request.Args)
		}
		image := validImageInspection(candidateSpec())
		image.Labels["io.rig.component"] = "other-component"
		return runtimeJSON(image)
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticImageDriftDetected) {
		t.Fatalf("expected image drift, got %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("component drift reached Docker mutation: %d requests", len(runner.requests))
	}
}

func TestGeneratedRuntimeRejectsImageRoleDriftBeforeDockerMutation(t *testing.T) {
	engine, runner, spec := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "image inspect" {
			t.Fatalf("role drift reached Docker mutation: %#v", request.Args)
		}
		image := validImageInspection(candidateSpec())
		image.Labels["io.rig.role"] = RoleStatic
		return runtimeJSON(image)
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticImageDriftDetected) {
		t.Fatalf("expected image role drift, got %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("role drift reached Docker mutation: %d requests", len(runner.requests))
	}
}

func TestGeneratedRuntimeOmitsEnvironmentFileWhenConfigurationIsEmpty(t *testing.T) {
	var create runtimeprocess.CommandRequest
	expected := candidateSpec()
	engine, _, spec := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(expected))
		case "network inspect":
			return runtimeJSON(networkInspection{Name: networkName(expected.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": expected.AppID}})
		case "container inspect":
			if request.Args[len(request.Args)-1] != testContainerID {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			return runtimeJSON(hardenedInspection(expected, defaultLimits()))
		case "container create":
			create = request
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(testContainerID)}}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	environment := engine.environment.(*runtimeFakeEnvironment)
	spec.Environment = nil
	spec.EnvironmentOperationID = ""
	spec.EnvironmentOperationAttempt = 0
	if _, err := engine.CreateInactiveCandidate(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if environment.staged != 0 || containsExactArgument(create.Args, "--env-file") {
		t.Fatalf("empty configuration was staged: calls=%d args=%#v", environment.staged, create.Args)
	}
}

func TestGeneratedRuntimeRejectsCapacityBeforeDockerMutation(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "docker-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := candidateSpec()
	runner := &runtimeFakeRunner{run: func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "image inspect" {
			t.Fatalf("unexpected Docker mutation: %#v", request.Args)
		}
		return runtimeJSON(validImageInspection(spec))
	}}
	engine, err := NewEngine(runner, &runtimeFakeEnvironment{path: filepath.Join(root, "env")}, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 1, DiskAvailableBytes: 1}}, EngineOptions{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: config, WorkingDirectory: root, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("expected replacement capacity rejection, got %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("capacity rejection reached mutation: %d requests", len(runner.requests))
	}
}

func TestGeneratedRuntimeDerivesDiskAdmissionFromMaximumLogs(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "docker-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := candidateSpec()
	runner := &runtimeFakeRunner{run: func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "image inspect" {
			t.Fatalf("under-reserved replacement reached mutation: %#v", request.Args)
		}
		return runtimeJSON(validImageInspection(spec))
	}}
	limits := defaultLimits()
	limits.LogSize = "50m"
	limits.LogFiles = 10
	engine, err := NewEngine(runner, &runtimeFakeEnvironment{path: filepath.Join(root, "env")}, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 550 << 20}}, EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: config, WorkingDirectory: root,
		CommandTimeout: time.Second, Limits: limits, ReplacementDiskBytes: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CreateInactiveCandidate(context.Background(), spec); !IsCode(err, DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("expected derived disk capacity rejection, got %v", err)
	}
	if engine.options.ReplacementDiskBytes != 564<<20 {
		t.Fatalf("derived replacement disk = %d, want %d", engine.options.ReplacementDiskBytes, uint64(564<<20))
	}
}

func TestGeneratedRuntimeHardeningRequiresAliasAndExactTmpfsPolicy(t *testing.T) {
	spec := candidateSpec()
	candidate := candidateForSpec(spec)
	tests := []struct {
		name   string
		mutate func(*containerInspection)
	}{
		{name: "missing network alias", mutate: func(container *containerInspection) {
			container.Networks[candidate.NetworkName] = networkAttachmentInspection{Aliases: []string{"other"}}
		}},
		{name: "contradictory tmpfs option", mutate: func(container *containerInspection) {
			container.Tmpfs["/tmp"] += ",exec"
		}},
		{name: "additional tmpfs", mutate: func(container *containerInspection) {
			container.Tmpfs["/run"] = "rw,nosuid,nodev"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			container := hardenedInspection(spec, defaultLimits())
			test.mutate(&container)
			if matchesCandidateHardening(container, candidate, defaultLimits()) {
				t.Fatal("unsafe hardening inspection was accepted")
			}
		})
	}
}

func TestGeneratedRuntimeCreatedCandidateAllowsUnrealizedRuntimeStateUntilHealthCheck(t *testing.T) {
	spec := candidateSpec()
	created := false
	started := false
	removed := false
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(spec))
		case "network inspect":
			return runtimeJSON(networkInspection{Name: networkName(spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": spec.AppID}})
		case "container inspect":
			identity := request.Args[len(request.Args)-1]
			if removed || !created || (identity != testContainerID && identity != containerName(spec.AppID, spec.ComponentName, SlotGreen)) {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			inspection := configuredInspection(spec, defaultLimits())
			inspection.Running = started
			inspection.Health = "starting"
			return runtimeJSON(inspection)
		case "container create":
			created = true
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(testContainerID)}}
		case "container start":
			started = true
			return runtimeRequestResult{}
		case "container rm":
			if !containsExactArgument(request.Args, "--force") {
				t.Fatalf("failed candidate cleanup was not forced: %#v", request.Args)
			}
			removed = true
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	candidate, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if err != nil {
		t.Fatalf("created candidate rejected before runtime state materialized: %v", err)
	}
	if err := engine.StartCandidate(context.Background(), candidate); err != nil {
		t.Fatalf("created candidate rejected before start: %v", err)
	}
	if err := engine.WaitHealthy(context.Background(), candidate); !IsCode(err, DiagnosticCandidateHardeningFailed) {
		t.Fatalf("health check accepted missing realized mounts or network attachment: %v", err)
	}
	if !removed {
		t.Fatal("fully labeled created candidate was not removed during failure cleanup")
	}
	findRuntimeRequest(t, runner.requests, "container start")
	findRuntimeRequest(t, runner.requests, "container rm")
}

func TestGeneratedRuntimeStopAndRemoveAllowsUnrealizedCreatedCandidate(t *testing.T) {
	spec := candidateSpec()
	candidate := candidateForSpec(spec)
	removed := false
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			if removed {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			return runtimeJSON(configuredInspection(spec, defaultLimits()))
		case "container rm":
			removed = true
			return runtimeRequestResult{}
		case "container stop":
			t.Fatal("created candidate should be removed without a stop request")
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	if err := engine.StopAndRemove(context.Background(), candidate, time.Second); err != nil {
		t.Fatalf("unrealized created candidate was not removed: %v", err)
	}
	if !removed {
		t.Fatal("created candidate was not removed")
	}
	findRuntimeRequest(t, runner.requests, "container rm")
}

func TestGeneratedRuntimeCleanupOwnershipDoesNotRequireConfiguredNetwork(t *testing.T) {
	spec := candidateSpec()
	for _, test := range []struct {
		name             string
		networkMode      string
		mutateLabels     func(map[string]string)
		wantCleanupError bool
	}{
		{name: "empty network mode", networkMode: ""},
		{name: "wrong network mode", networkMode: "unexpected-network"},
		{
			name:             "missing ownership label stays protected",
			networkMode:      "",
			mutateLabels:     func(labels map[string]string) { delete(labels, "io.rig.deployment") },
			wantCleanupError: true,
		},
		{
			name:        "wrong ownership label stays protected",
			networkMode: "",
			mutateLabels: func(labels map[string]string) {
				labels["io.rig.deployment"] = uuid.NewString()
			},
			wantCleanupError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := candidateForSpec(spec)
			removed := false
			inspection := configuredInspection(spec, defaultLimits())
			inspection.NetworkMode = test.networkMode
			if test.mutateLabels != nil {
				test.mutateLabels(inspection.Labels)
			}
			if matchesCandidateConfiguredHardening(inspection, candidate, defaultLimits()) {
				t.Fatal("network configuration drift passed configured hardening")
			}
			engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				switch commandKind(request.Args) {
				case "container inspect":
					if removed {
						return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
					}
					return runtimeJSON(inspection)
				case "container rm":
					removed = true
					return runtimeRequestResult{}
				default:
					t.Fatalf("unexpected request: %#v", request.Args)
					return runtimeRequestResult{}
				}
			})
			err := engine.StopAndRemove(context.Background(), candidate, time.Second)
			if test.wantCleanupError {
				if !IsCode(err, DiagnosticCandidateHardeningFailed) {
					t.Fatalf("expected protected ownership failure, got %v", err)
				}
				if removed {
					t.Fatal("container with incomplete ownership reached removal")
				}
				return
			}
			if err != nil {
				t.Fatalf("fully owned container with network drift was not removed: %v", err)
			}
			if !removed {
				t.Fatal("fully owned container with network drift was not removed")
			}
			findRuntimeRequest(t, runner.requests, "container rm")
		})
	}
}

func TestGeneratedRuntimeCleansContainerThatFailsHardeningVerification(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "docker-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := candidateSpec()
	networkInspections := 0
	created := false
	removed := false
	runner := &runtimeFakeRunner{}
	runner.run = func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(spec))
		case "network inspect":
			networkInspections++
			if networkInspections == 1 {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such network")}, err: errors.New("exit")}
			}
			return runtimeJSON(networkInspection{Name: networkName(spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": spec.AppID}})
		case "network create":
			return runtimeRequestResult{}
		case "container inspect":
			identity := request.Args[len(request.Args)-1]
			if removed || (!created && identity != testContainerID) || (identity != testContainerID && identity != containerName(spec.AppID, spec.ComponentName, SlotGreen)) {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			inspection := configuredInspection(spec, defaultLimits())
			inspection.Privileged = true
			return runtimeJSON(inspection)
		case "container create":
			created = true
			return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: []byte(testContainerID)}}
		case "container rm":
			if !containsArgument(request.Args, "--force") {
				t.Fatalf("unsafe candidate cleanup was not forced: %#v", request.Args)
			}
			removed = true
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	}
	engine, err := NewEngine(runner, &runtimeFakeEnvironment{path: filepath.Join(root, "env")}, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, EngineOptions{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: config, WorkingDirectory: root, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticCandidateHardeningFailed) {
		t.Fatalf("expected hardening failure, got %v", err)
	}
	findRuntimeRequest(t, runner.requests, "container rm")
}

func TestGeneratedRuntimeReconcilesAmbiguousCreateByDeterministicName(t *testing.T) {
	spec := candidateSpec()
	created := false
	removed := false
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(spec))
		case "network inspect":
			return runtimeJSON(networkInspection{Name: networkName(spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": spec.AppID}})
		case "container inspect":
			identity := request.Args[len(request.Args)-1]
			if removed || !created {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			if identity != testContainerID && identity != containerName(spec.AppID, spec.ComponentName, SlotGreen) {
				t.Fatalf("ambiguous create reconciled through unexpected identity: %q", identity)
			}
			return runtimeJSON(hardenedInspection(spec, defaultLimits()))
		case "container create":
			created = true
			return runtimeRequestResult{err: context.DeadlineExceeded}
		case "container rm":
			removed = true
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticRuntimeTimeout) {
		t.Fatalf("expected original create timeout after successful reconciliation, got %v", err)
	}
	if !removed {
		t.Fatal("ambiguous create was not removed")
	}
	findRuntimeRequest(t, runner.requests, "container rm")
}

func TestGeneratedRuntimeFailsClosedWhenAmbiguousCreateOwnershipCannotBeProved(t *testing.T) {
	spec := candidateSpec()
	created := false
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(spec))
		case "network inspect":
			return runtimeJSON(networkInspection{Name: networkName(spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": spec.AppID}})
		case "container inspect":
			if !created {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			inspection := hardenedInspection(spec, defaultLimits())
			inspection.Labels["io.rig.deployment"] = uuid.NewString()
			return runtimeJSON(inspection)
		case "container create":
			created = true
			return runtimeRequestResult{err: context.DeadlineExceeded}
		case "container rm":
			t.Fatal("container with unproved ownership was removed")
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticCandidateCleanupFailed) {
		t.Fatalf("expected fail-closed cleanup diagnostic, got %v", err)
	}
	engine.capacity.mu.Lock()
	reservedMemory := engine.capacity.memory
	engine.capacity.mu.Unlock()
	if reservedMemory != uint64(defaultLimits().MemoryBytes) {
		t.Fatalf("unresolved candidate released admission: reserved=%d", reservedMemory)
	}
	for _, request := range runner.requests {
		if commandKind(request.Args) == "container rm" {
			t.Fatal("unowned container reached removal")
		}
	}
}

func TestGeneratedRuntimeReconcilesAmbiguousStart(t *testing.T) {
	spec := candidateSpec()
	candidate := candidateForSpec(spec)
	started := false
	removed := false
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			if removed {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
			}
			inspection := hardenedInspection(spec, defaultLimits())
			inspection.Running = started
			if started {
				inspection.Health = "starting"
			}
			return runtimeJSON(inspection)
		case "container start":
			started = true
			return runtimeRequestResult{err: context.DeadlineExceeded}
		case "container rm":
			removed = true
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	if err := engine.StartCandidate(context.Background(), candidate); !IsCode(err, DiagnosticRuntimeTimeout) {
		t.Fatalf("expected original start timeout after successful reconciliation, got %v", err)
	}
	if !started || !removed {
		t.Fatalf("ambiguous start was not reconciled: started=%t removed=%t", started, removed)
	}
	findRuntimeRequest(t, runner.requests, "container rm")
}

func TestGeneratedRuntimeHealthWaitFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		running  bool
		health   string
		cancel   bool
		expected DiagnosticCode
	}{
		{name: "unhealthy", running: true, health: "unhealthy", expected: DiagnosticCandidateUnhealthy},
		{name: "exited", running: false, health: "starting", expected: DiagnosticCandidateExited},
		{name: "missing health", running: true, health: "", expected: DiagnosticCandidateHardeningFailed},
		{name: "cancelled", running: true, health: "starting", cancel: true, expected: DiagnosticCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := candidateSpec()
			candidate := candidateForSpec(spec)
			removed := false
			engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				switch commandKind(request.Args) {
				case "container inspect":
					if removed {
						return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such container")}, err: errors.New("exit")}
					}
					inspection := hardenedInspection(spec, defaultLimits())
					inspection.Running = test.running
					inspection.Health = test.health
					return runtimeJSON(inspection)
				case "container rm":
					if !containsExactArgument(request.Args, "--force") {
						t.Fatalf("failed candidate cleanup was not forced: %#v", request.Args)
					}
					removed = true
					return runtimeRequestResult{}
				default:
					t.Fatalf("unexpected health command: %#v", request.Args)
					return runtimeRequestResult{}
				}
			})
			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				runner.rejectCancelled = true
			}
			if err := engine.WaitHealthy(ctx, candidate); !IsCode(err, test.expected) {
				t.Fatalf("expected %s, got %v", test.expected, err)
			}
			if !removed {
				t.Fatal("failed health candidate was not removed")
			}
		})
	}
}

func TestGeneratedRuntimeRefusesNetworkOwnershipDrift(t *testing.T) {
	engine, runner, spec := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "image inspect":
			return runtimeJSON(validImageInspection(candidateSpec()))
		case "network inspect":
			return runtimeJSON(networkInspection{Name: networkName(candidateSpec().AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "somebody-else"}})
		default:
			t.Fatalf("network drift reached mutation: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if !IsCode(err, DiagnosticNetworkDriftDetected) {
		t.Fatalf("expected network drift, got %v", err)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("network drift should stop after two read-only inspections, got %d", len(runner.requests))
	}
}

func TestGeneratedRuntimeErrorsNeverIncludeDockerOutput(t *testing.T) {
	secret := "super-secret-docker-output"
	engine, _, spec := newRuntimeTestEngine(t, func(runtimeprocess.CommandRequest) runtimeRequestResult {
		return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte(secret)}, err: errors.New("exit 1: " + secret)}
	})
	_, err := engine.CreateInactiveCandidate(context.Background(), spec)
	if err == nil || strings.Contains(err.Error(), secret) || !IsCode(err, DiagnosticImageUnavailable) {
		t.Fatalf("unsafe error classification: %v", err)
	}
}

func TestGeneratedRuntimeRejectsInvalidEnvironmentBeforeInspection(t *testing.T) {
	engine, runner, spec := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		t.Fatalf("invalid configuration reached Docker: %#v", request.Args)
		return runtimeRequestResult{}
	})
	spec.Environment = []byte{'A', '=', 'x', 0, 'y'}
	if _, err := engine.CreateInactiveCandidate(context.Background(), spec); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("expected validation failure, got %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("invalid configuration reached Docker: %d requests", len(runner.requests))
	}
}

func TestGeneratedRuntimeIdentitiesAndSlotsAreDeterministic(t *testing.T) {
	app := "11111111-1111-4111-8111-111111111111"
	if first, second := networkName(app), networkName(app); first != second || len(first) > 63 {
		t.Fatalf("invalid network identity %q %q", first, second)
	}
	if first, second := containerName(app, "apps/api", SlotBlue), containerName(app, "apps/api", SlotBlue); first != second || len(first) > 63 {
		t.Fatalf("invalid container identity %q %q", first, second)
	}
	if slot, _ := InactiveSlot(""); slot != SlotBlue {
		t.Fatalf("first slot = %s", slot)
	}
	if slot, _ := InactiveSlot(SlotBlue); slot != SlotGreen {
		t.Fatalf("blue replacement = %s", slot)
	}
	if slot, _ := InactiveSlot(SlotGreen); slot != SlotBlue {
		t.Fatalf("green replacement = %s", slot)
	}
	if _, err := InactiveSlot("red"); err == nil {
		t.Fatal("invalid active slot accepted")
	}
}

func TestGeneratedRuntimeValidateImageIsReadOnly(t *testing.T) {
	expected := candidateSpec()
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		if commandKind(request.Args) != "image inspect" {
			t.Fatalf("read-only image validation mutated Docker: %#v", request.Args)
		}
		return runtimeJSON(validImageInspection(expected))
	})
	err := engine.ValidateImage(context.Background(), ImageSpec{
		AppID: expected.AppID, ReleaseID: expected.ReleaseID, ArtifactID: expected.ArtifactID,
		DeploymentPlanRevisionID: expected.DeploymentPlanRevisionID, ImageContentID: expected.ImageContentID,
		ComponentName: expected.ComponentName, Role: expected.Role, BuildDefinitionDigest: expected.BuildDefinitionDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || commandKind(runner.requests[0].Args) != "image inspect" {
		t.Fatalf("validation requests = %#v", runner.requests)
	}
}

func TestGeneratedRuntimeDescriptionsExposeDeterministicPreMutationIdentity(t *testing.T) {
	spec := candidateSpec()
	description, err := DescribeInactiveCandidate(spec.AppID, spec.ComponentName, SlotBlue)
	if err != nil {
		t.Fatal(err)
	}
	if description.Slot != SlotGreen || description.ContainerName != containerName(spec.AppID, spec.ComponentName, SlotGreen) || description.NetworkName != networkName(spec.AppID) || description.NetworkAlias != containerAlias(spec.ComponentName, SlotGreen) {
		t.Fatalf("unexpected candidate description: %+v", description)
	}
	network, err := DescribeAppNetwork(spec.AppID)
	if err != nil || network.Name != description.NetworkName {
		t.Fatalf("unexpected app network: %+v %v", network, err)
	}
	if _, err := DescribeInactiveCandidate("invalid", spec.ComponentName, SlotBlue); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("invalid app description error = %v", err)
	}
	if _, err := DescribeAppNetwork("invalid"); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("invalid app network error = %v", err)
	}
}

func TestGeneratedRuntimeEnsureAppNetworkReusesExactOwnershipChecks(t *testing.T) {
	spec := candidateSpec()
	inspections := 0
	engine, runner, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "network inspect":
			inspections++
			if inspections == 1 {
				return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte("No such network")}, err: errors.New("exit")}
			}
			return runtimeJSON(networkInspection{Name: networkName(spec.AppID), Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": spec.AppID}})
		case "network create":
			return runtimeRequestResult{}
		default:
			t.Fatalf("unexpected network request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	if err := engine.EnsureAppNetwork(context.Background(), spec.AppID); err != nil {
		t.Fatal(err)
	}
	if got, want := len(runner.requests), 3; got != want {
		t.Fatalf("network requests=%d want=%d", got, want)
	}
}

func candidateSpec() CandidateSpec {
	return CandidateSpec{
		AppID: "11111111-1111-4111-8111-111111111111", ReleaseID: "22222222-2222-4222-8222-222222222222",
		DeploymentID: "33333333-3333-4333-8333-333333333333", ArtifactID: "44444444-4444-4444-8444-444444444444",
		DeploymentPlanRevisionID: "55555555-5555-4555-8555-555555555555", ComponentName: "api", Role: RoleServer, RootDirectory: "apps/api",
		RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health?ready=1", ImageContentID: testImageID,
		BuildDefinitionDigest: strings.Repeat("d", 64),
		ActiveSlot:            SlotBlue, EnvironmentOperationID: "66666666-6666-4666-8666-666666666666", EnvironmentOperationAttempt: 1,
		Environment: []byte("SECRET='value'\n"),
	}
}

func candidateForSpec(spec CandidateSpec) Candidate {
	slot, _ := InactiveSlot(spec.ActiveSlot)
	return Candidate{
		AppID: spec.AppID, ReleaseID: spec.ReleaseID, DeploymentID: spec.DeploymentID,
		ArtifactID: spec.ArtifactID, DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID,
		Component: spec.ComponentName, Role: spec.Role, Slot: slot, ContainerID: testContainerID,
		ContainerName: containerName(spec.AppID, spec.ComponentName, slot), NetworkName: networkName(spec.AppID),
		NetworkAlias: containerAlias(spec.ComponentName, slot), InternalPort: spec.InternalPort,
		ImageContentID: spec.ImageContentID, WorkingDirectory: runtimeWorkingDirectory(spec.RootDirectory), RunCommandDigest: sha256Hex(spec.RunCommand),
	}
}

func defaultLimits() ContainerLimits {
	return ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3}
}

func imageLabels(spec CandidateSpec) map[string]string {
	return map[string]string{"io.rig.managed": "generated-image", "io.rig.application": spec.AppID, "io.rig.release": spec.ReleaseID, "io.rig.artifact": spec.ArtifactID, "io.rig.plan": spec.DeploymentPlanRevisionID, "io.rig.component": spec.ComponentName, "io.rig.role": spec.Role, "io.rig.definition": spec.BuildDefinitionDigest}
}

type templateDockerContainer struct {
	ID              string
	Name            string
	Image           string
	Config          templateDockerConfig
	HostConfig      templateDockerHostConfig
	Mounts          []templateDockerMount
	State           templateDockerState
	NetworkSettings *templateDockerNetworkSettings
}

type templateDockerConfig struct {
	Labels      map[string]string
	User        string
	WorkingDir  string
	Cmd         []string
	Healthcheck *templateDockerHealthcheck
}

type templateDockerHealthcheck struct {
	Test        []string
	Interval    int64
	Timeout     int64
	StartPeriod int64
	Retries     int
}

type templateDockerHostConfig struct {
	Memory         int64
	MemorySwap     int64
	NanoCPUs       int64
	PidsLimit      *int64
	Ulimits        []templateDockerUlimit
	Init           *bool
	NetworkMode    string
	ReadonlyRootfs bool
	Privileged     bool
	CapAdd         []string
	CapDrop        []string
	SecurityOpt    []string
	Binds          []string
	PortBindings   map[string]any
	Tmpfs          map[string]string
	LogConfig      templateDockerLogConfig
	RestartPolicy  templateDockerRestartPolicy
}

type templateDockerUlimit struct {
	Name string
	Soft int64
	Hard int64
}

type templateDockerLogConfig struct {
	Type   string
	Config map[string]string
}

type templateDockerRestartPolicy struct {
	Name string
}

type templateDockerMount struct {
	Type        string
	Source      string
	Destination string
	RW          bool
}

type templateDockerState struct {
	Running  bool
	ExitCode int
	Health   *templateDockerHealth
}

type templateDockerHealth struct {
	Status string
}

type templateDockerNetworkSettings struct {
	Networks map[string]struct{}
}

func TestContainerInspectFormatHandlesMissingHealthAndNetworkState(t *testing.T) {
	templateValue, err := template.New("container-inspect").Funcs(template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
	}).Parse(containerInspectFormat)
	if err != nil {
		t.Fatal("container inspection template did not parse")
	}
	var rendered strings.Builder
	input := templateDockerContainer{
		ID:              "container-id",
		State:           templateDockerState{Health: nil},
		NetworkSettings: nil,
	}
	if err := templateValue.Execute(&rendered, input); err != nil {
		t.Fatal("container inspection template did not execute")
	}
	var decoded containerInspection
	if err := json.Unmarshal([]byte(rendered.String()), &decoded); err != nil {
		t.Fatal("container inspection template output was not JSON")
	}
	if decoded.HealthTest != nil || decoded.HealthInterval != 0 || decoded.HealthTimeout != 0 || decoded.HealthStartPeriod != 0 || decoded.HealthRetries != 0 {
		t.Fatal("missing configured health check was not rendered as null defaults")
	}
	if decoded.PIDs != 0 || decoded.Init {
		t.Fatal("missing pointer hardening values were not rendered as null defaults")
	}
	if decoded.Health != "" {
		t.Fatal("missing health state was not rendered as an empty string")
	}
	if decoded.Networks != nil {
		t.Fatal("missing network state was not rendered as null")
	}
}

func validImageInspection(spec CandidateSpec) imageInspection {
	return imageInspection{ID: spec.ImageContentID, Size: 100, Labels: imageLabels(spec), User: "node", WorkingDirectory: "/workspace", Entrypoint: []string{"/usr/local/bin/rig-entrypoint"}}
}

func hardenedInspection(spec CandidateSpec, limits ContainerLimits) containerInspection {
	slot, _ := InactiveSlot(spec.ActiveSlot)
	network := networkName(spec.AppID)
	return containerInspection{
		ID: testContainerID, Name: "/" + containerName(spec.AppID, spec.ComponentName, slot), Image: spec.ImageContentID,
		Labels: runtimeLabels(spec, slot), User: containerUser, WorkingDirectory: runtimeWorkingDirectory(spec.RootDirectory),
		Command: []string{"/bin/sh", "-lc", spec.RunCommand}, HealthTest: []string{"CMD-SHELL", healthCommand},
		HealthInterval: int64(2 * time.Second), HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: limits.MemoryBytes, MemorySwap: limits.MemoryBytes, NanoCPUs: limits.MilliCPUs * 1_000_000, PIDs: limits.PIDs,
		NetworkMode: network, ReadonlyRootfs: true, Init: true, CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges:true"}, Ulimits: []ulimitInspection{{Name: "nofile", Soft: 1024, Hard: 1024}},
		Tmpfs:   map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=" + stringInt(limits.TmpfsBytes)},
		LogType: "local", LogConfig: map[string]string{"max-size": limits.LogSize, "max-file": stringInt(int64(limits.LogFiles))}, Restart: "no",
		Mounts: []mountInspection{{Type: "tmpfs", Destination: "/tmp", RW: true}}, Networks: map[string]networkAttachmentInspection{network: {Aliases: []string{containerAlias(spec.ComponentName, slot)}}},
	}
}

func configuredInspection(spec CandidateSpec, limits ContainerLimits) containerInspection {
	inspection := hardenedInspection(spec, limits)
	inspection.Mounts = nil
	inspection.Networks = nil
	return inspection
}

func stringInt(value int64) string { return strings.TrimSpace(string(mustJSONNumber(value))) }

func mustJSONNumber(value int64) []byte {
	result, _ := json.Marshal(value)
	return result
}

func runtimeJSON(value any) runtimeRequestResult {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return runtimeRequestResult{result: runtimeprocess.CommandResult{Stdout: body}}
}

func commandKind(args []string) string {
	if len(args) < 2 {
		return strings.Join(args, " ")
	}
	return args[0] + " " + args[1]
}

func findRuntimeRequest(t *testing.T, requests []runtimeprocess.CommandRequest, kind string) runtimeprocess.CommandRequest {
	t.Helper()
	for _, request := range requests {
		if commandKind(request.Args) == kind {
			return request
		}
	}
	t.Fatalf("request %q not found in %#v", kind, requests)
	return runtimeprocess.CommandRequest{}
}

func assertArgumentPair(t *testing.T, args []string, key, value string) {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return
		}
	}
	t.Fatalf("argument pair %q %q not found in %#v", key, value, args)
}

func containsArgument(args []string, expected string) bool {
	for _, value := range args {
		if value == expected || strings.Contains(value, expected) {
			return true
		}
	}
	return false
}

func containsExactArgument(args []string, expected string) bool {
	for _, value := range args {
		if value == expected {
			return true
		}
	}
	return false
}

func containsEnvironmentValue(environment []string, value string) bool {
	for _, entry := range environment {
		if strings.Contains(entry, value) {
			return true
		}
	}
	return false
}

func newRuntimeTestEngine(t *testing.T, run func(runtimeprocess.CommandRequest) runtimeRequestResult) (*Engine, *runtimeFakeRunner, CandidateSpec) {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "docker-config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &runtimeFakeRunner{run: run}
	engine, err := NewEngine(runner, &runtimeFakeEnvironment{path: filepath.Join(root, "env")}, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, EngineOptions{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: config, WorkingDirectory: root, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return engine, runner, candidateSpec()
}
