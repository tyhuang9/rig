package generatedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedcontainer"
	"github.com/hostd/hostd/internal/generatedingress"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/generateddomain"
)

const (
	testImageID     = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
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

func (s *runtimeFakeEnvironment) StageGeneratedCandidate(operationID string, attempt int, contents []byte) (generatedingress.GeneratedCandidateEnvironmentLease, error) {
	lease, err := s.Stage(operationID, attempt, contents)
	return lease, err
}

func (s *runtimeFakeEnvironment) StageGeneratedMigration(operationID string, attempt int, contents []byte) (deployments.GeneratedMigrationEnvironmentLease, error) {
	lease, err := s.Stage(operationID, attempt, contents)
	return lease, err
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
	spec := candidateSpec()
	spec.Technology = "nextjs"
	command := `node server.js && printf '$() ${TOKEN}' \\unicode-✓`
	spec.RunCommand = command
	limits := ContainerLimits{MemoryBytes: 384 << 20, MilliCPUs: 750, PIDs: 192, TmpfsBytes: 32 << 20, LogSize: "5m", LogFiles: 2}
	var program *runtimeCandidateProgram
	session := newRuntimeCandidateSession(t, spec, limits, fixedDomainCapacity{memory: 4 << 30, disk: 4 << 30}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		return program.run(request)
	})
	spec = session.authority.spec
	program = newRuntimeCandidateProgram(t, spec, limits)
	session.reserveAndPin(t)
	invocation := session.beginCreate(t)
	program.bind(invocation, session)
	identity, err := session.execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Slot != string(SlotGreen) || identity.ContainerName != containerName(spec.AppID, spec.ComponentName, SlotGreen) || identity.NetworkName != networkName(spec.AppID) || identity.ReceiptID == "" {
		t.Fatalf("unexpected receipt-bound candidate identity: %+v", identity)
	}
	if err := session.execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	session.finishStarting(t)
	program.health = "healthy"
	if err := session.execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := session.execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	if session.environment.cleaned != 1 || len(session.environment.contents) == 0 {
		t.Fatalf("environment was not staged and cleaned: cleaned=%d contents=%q", session.environment.cleaned, session.environment.contents)
	}

	create := findRuntimeRequest(t, session.runner.requests, "container create")
	assertArgumentPair(t, create.Args, "--env-file", session.environment.path)
	assertArgumentPair(t, create.Args, "--workdir", "/workspace/apps/api")
	assertArgumentPair(t, create.Args, "--user", containerUser)
	assertArgumentPair(t, create.Args, "--network", networkName(spec.AppID))
	assertArgumentPair(t, create.Args, "--memory", "402653184")
	assertArgumentPair(t, create.Args, "--memory-swap", "402653184")
	assertArgumentPair(t, create.Args, "--cpus", "0.750")
	assertArgumentPair(t, create.Args, "--pids-limit", "192")
	assertArgumentPair(t, create.Args, "--health-cmd", healthCommand)
	assertArgumentPair(t, create.Args, "--tmpfs", nextCacheDirectory("/workspace/apps/api")+":rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=33554432")
	assertArgumentPair(t, create.Args, "--label", "io.rig.role="+RoleServer)
	if got := create.Args[len(create.Args)-4:]; !reflect.DeepEqual(got, []string{spec.ImageContentID, "/bin/sh", "-lc", command}) {
		t.Fatalf("runtime command lost exact argument boundaries: %#v", got)
	}
	for _, prohibited := range []string{"--publish", "-p", "--volume", "-v", "--mount", "--privileged", "/var/run/docker.sock"} {
		if (strings.HasPrefix(prohibited, "-") && containsExactArgument(create.Args, prohibited)) || (!strings.HasPrefix(prohibited, "-") && containsArgument(create.Args, prohibited)) {
			t.Fatalf("prohibited Docker argument present: %s in %#v", prohibited, create.Args)
		}
	}
	for _, request := range session.runner.requests {
		if containsEnvironmentValue(request.Env, "SECRET") || containsEnvironmentValue(request.Env, "value") {
			t.Fatalf("application configuration escaped into Docker client environment: %#v", request.Env)
		}
		if request.Executable != session.engine.options.DockerExecutable || request.Directory != session.engine.options.WorkingDirectory {
			t.Fatalf("unscoped Docker execution: %+v", request)
		}
	}
}

func TestGeneratedRuntimeRejectsImageLabelDriftBeforeDockerMutation(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.image = func(image imageInspection) imageInspection {
		image.Labels["io.rig.application"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		return image
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticImageDriftDetected) {
		t.Fatalf("expected image drift, got %v", err)
	}
	if len(session.runner.requests) != 1 {
		t.Fatalf("image drift reached Docker mutation: %d requests", len(session.runner.requests))
	}
}

func TestGeneratedRuntimeRejectsImageComponentDriftBeforeDockerMutation(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.image = func(image imageInspection) imageInspection {
		image.Labels["io.rig.component"] = "other-component"
		return image
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticImageDriftDetected) {
		t.Fatalf("expected image drift, got %v", err)
	}
	if len(session.runner.requests) != 1 {
		t.Fatalf("component drift reached Docker mutation: %d requests", len(session.runner.requests))
	}
}

func TestGeneratedRuntimeRejectsImageRoleDriftBeforeDockerMutation(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.image = func(image imageInspection) imageInspection {
		image.Labels["io.rig.role"] = RoleStatic
		return image
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticImageDriftDetected) {
		t.Fatalf("expected image role drift, got %v", err)
	}
	if len(session.runner.requests) != 1 {
		t.Fatalf("role drift reached Docker mutation: %d requests", len(session.runner.requests))
	}
}

func TestGeneratedRuntimeOmitsEnvironmentFileWhenConfigurationIsEmpty(t *testing.T) {
	spec := candidateSpec()
	spec.Environment = nil
	spec.EnvironmentOperationID = ""
	spec.EnvironmentOperationAttempt = 0
	session, _, invocation := newPreparedRuntimeCandidate(t, spec, defaultLimits(), fixedDomainCapacity{})
	if _, err := session.execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	create := findRuntimeRequest(t, session.runner.requests, "container create")
	if session.environment.staged != 0 || containsExactArgument(create.Args, "--env-file") {
		t.Fatalf("empty configuration was staged: calls=%d args=%#v", session.environment.staged, create.Args)
	}
}

func TestGeneratedRuntimeStagesEnvironmentFileForExplicitEmptyPublicValue(t *testing.T) {
	empty := ""
	entries := []appconfig.ScopedValueInput{{
		ScopedKey:   appconfig.ScopedKey{Phase: appconfig.PhaseRuntime, Component: "api", Key: "PUBLIC"},
		Sensitivity: appconfig.SensitivityPublic,
		Value:       &empty,
	}}
	session, _, invocation := newPreparedRuntimeCandidateWithConfiguration(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{}, entries)
	if _, err := session.execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	create := findRuntimeRequest(t, session.runner.requests, "container create")
	if session.environment.staged != 1 {
		t.Fatalf("explicit empty public value did not stage: calls=%d", session.environment.staged)
	}
	if got, want := string(session.environment.contents), "# hostd application configuration\nPUBLIC=\n"; got != want {
		t.Fatalf("staged protected configuration=%q want %q", got, want)
	}
	assertArgumentPair(t, create.Args, "--env-file", session.environment.path)
}

func TestGeneratedRuntimeRejectsCapacityBeforeDockerMutation(t *testing.T) {
	spec := candidateSpec()
	spec.ActiveSlot = ""
	session := newRuntimeCandidateSession(t, spec, defaultLimits(), fixedDomainCapacity{memory: 1, disk: 1}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		t.Fatalf("capacity rejection reached Docker: %#v", request.Args)
		return runtimeRequestResult{}
	})
	_, err := session.engine.ReserveReplacement(session.authority.context, generatedingress.GeneratedReplacementRequest(session.request))
	if !IsCode(err, DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("expected replacement capacity rejection, got %v", err)
	}
	if len(session.runner.requests) != 0 {
		t.Fatalf("capacity rejection reached Docker: %d requests", len(session.runner.requests))
	}
	for _, command := range []string{"container create", "container start", "container rm", "network create"} {
		if countRuntimeRequests(session.runner.requests, command) != 0 {
			t.Fatalf("shared-domain capacity rejection reached %s", command)
		}
	}
}

func TestGeneratedRuntimeDerivesDiskAdmissionFromMaximumLogs(t *testing.T) {
	limits := defaultLimits()
	limits.LogSize = "50m"
	limits.LogFiles = 10
	session := newRuntimeCandidateSession(t, candidateSpec(), limits, fixedDomainCapacity{memory: 4 << 30, disk: 550 << 20}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		t.Fatalf("under-reserved replacement reached Docker: %#v", request.Args)
		return runtimeRequestResult{}
	})
	if _, err := session.engine.ReserveReplacement(session.authority.context, generatedingress.GeneratedReplacementRequest(session.request)); !IsCode(err, DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("expected derived disk capacity rejection, got %v", err)
	}
	if session.engine.options.ReplacementDiskBytes != 564<<20 {
		t.Fatalf("derived replacement disk = %d, want %d", session.engine.options.ReplacementDiskBytes, uint64(564<<20))
	}
}

func TestGeneratedRuntimeRequiresTwoLocalLogFiles(t *testing.T) {
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	options := EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		CommandTimeout: time.Second, HealthTimeout: time.Second, HealthPollInterval: 100 * time.Millisecond,
		OutputLimit: 1024, Limits: defaultLimits(), ReplacementDiskBytes: 1 << 30,
	}
	for _, test := range []struct {
		files int
		valid bool
	}{
		{files: 0, valid: false},
		{files: 1, valid: false},
		{files: 2, valid: true},
		{files: 10, valid: true},
		{files: 11, valid: false},
	} {
		options.Limits.LogFiles = test.files
		if got := validEngineOptions(options); got != test.valid {
			t.Fatalf("validEngineOptions with max-file=%d = %t, want %t", test.files, got, test.valid)
		}
	}

	runner := &runtimeFakeRunner{run: func(runtimeprocess.CommandRequest) runtimeRequestResult { return runtimeRequestResult{} }}
	environment := &runtimeFakeEnvironment{path: filepath.Join(root, "runtime.env")}
	fixture := newRuntimeDomainFixture(t, root, runner, environment, EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		CommandTimeout: time.Second, HealthTimeout: time.Second, HealthPollInterval: 100 * time.Millisecond,
		OutputLimit: 1024, ReplacementDiskBytes: 1 << 30,
	}, generateddomain.Options{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30})
	engine := fixture.engine
	if engine.options.Limits.LogFiles != 3 {
		t.Fatalf("default log files = %d, want 3", engine.options.Limits.LogFiles)
	}
}

func TestGeneratedRuntimeHardeningRequiresAliasAndExactTmpfsPolicy(t *testing.T) {
	spec := candidateSpec()
	candidate := candidateForSpec(spec)
	if !matchesCandidateHardening(hardenedInspection(spec, defaultLimits()), candidate, defaultLimits()) {
		t.Fatal("exact tmpfs policy with realized network was rejected")
	}
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

func TestGeneratedRuntimeNextCacheMountRequiresExactPolicy(t *testing.T) {
	spec := candidateSpec()
	spec.Technology = "nextjs"
	candidate := candidateForSpec(spec)
	cachePath := nextCacheDirectory(candidate.WorkingDirectory)
	if !matchesCandidateHardening(hardenedInspection(spec, defaultLimits()), candidate, defaultLimits()) {
		t.Fatal("exact Next.js cache mount was rejected")
	}
	for _, test := range []struct {
		name   string
		mutate func(*containerInspection)
	}{
		{name: "missing cache", mutate: func(container *containerInspection) { delete(container.Tmpfs, cachePath) }},
		{name: "wrong owner", mutate: func(container *containerInspection) {
			container.Tmpfs[cachePath] = strings.Replace(container.Tmpfs[cachePath], "uid=1000", "uid=0", 1)
		}},
		{name: "larger cache", mutate: func(container *containerInspection) {
			container.Tmpfs[cachePath] = strings.Replace(container.Tmpfs[cachePath], "size=33554432", "size=67108864", 1)
		}},
		{name: "extra writable path", mutate: func(container *containerInspection) { container.Tmpfs["/workspace/other"] = "rw,size=1024" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := hardenedInspection(spec, defaultLimits())
			test.mutate(&container)
			if matchesCandidateHardening(container, candidate, defaultLimits()) {
				t.Fatal("drifted Next.js cache mount was accepted")
			}
		})
	}
}

func TestGeneratedRuntimeRejectsUnsafeNextCacheRootAndTechnology(t *testing.T) {
	spec := candidateSpec()
	spec.Technology = "nextjs"
	if !validCandidateSpec(spec) || !validCandidate(candidateForSpec(spec)) {
		t.Fatal("valid Next.js candidate was rejected")
	}
	for _, root := range []string{"api:rw", "apps/api:rw,noexec", "../api"} {
		invalid := spec
		invalid.RootDirectory = root
		if validCandidateSpec(invalid) {
			t.Fatalf("unsafe Next.js mount root %q accepted", root)
		}
	}
	invalid := candidateForSpec(spec)
	invalid.WorkingDirectory = "/workspace/api:rw"
	if validCandidate(invalid) {
		t.Fatal("unsafe recovered Next.js mount root accepted")
	}
	for _, technology := range []string{"unknown", "NextJS", "static"} {
		invalid := spec
		invalid.Technology = technology
		if validCandidateSpec(invalid) {
			t.Fatalf("invalid server technology %q accepted", technology)
		}
	}
}

func TestGeneratedRuntimeCreatedCandidateAllowsUnavailableNetworkUntilHealthCheck(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	if _, err := session.execution.CreateAndRecord(invocation); err != nil {
		t.Fatalf("created candidate rejected before runtime state materialized: %v", err)
	}
	if err := session.execution.StartCandidate(invocation); err != nil {
		t.Fatalf("created candidate rejected before start: %v", err)
	}
	session.finishStarting(t)
	program.inspect = func(inspection generatedcontainer.Inspection) generatedcontainer.Inspection {
		inspection.Networks = map[string]generatedcontainer.NetworkAttachment{}
		return inspection
	}
	if err := session.execution.WaitHealthyAndRecord(invocation); !IsCode(err, DiagnosticCandidateExited) {
		t.Fatalf("health check accepted missing realized network attachment: %v", err)
	}
	for _, request := range session.runner.requests {
		if commandKind(request.Args) == "container rm" {
			t.Fatal("health failure implicitly removed a receipt-owned candidate")
		}
	}
	findRuntimeRequest(t, session.runner.requests, "container start")
}

func TestGeneratedRuntimeStopAndRemoveAllowsUnrealizedCreatedCandidate(t *testing.T) {
	spec := candidateSpec()
	candidate := candidateForSpec(spec)
	removed := false
	engine, runner, _, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
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

func TestGeneratedRuntimeStopAndRemoveRefusesReceiptOwnedCandidateBeforeDocker(t *testing.T) {
	session, _, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	candidate := candidateForSpec(session.authority.spec)
	if candidate.ContainerName != containerName(session.authority.spec.AppID, session.authority.spec.ComponentName, SlotGreen) || invocation.Identity().ReceiptID == "" {
		t.Fatalf("test setup did not create a receipt-bound candidate: %+v", candidate)
	}
	err := session.engine.StopAndRemove(session.authority.context, candidate, time.Second)
	if !errors.Is(err, deployments.ErrGeneratedCandidateRetirementUnavailable) {
		t.Fatalf("legacy removal accepted durable candidate receipt: %v", err)
	}
	if wrapped := fmt.Errorf("retirement barrier: %w", err); !IsCode(wrapped, DiagnosticCode("retirement_unavailable")) {
		t.Fatalf("wrapped retirement barrier lost its diagnostic: %v", wrapped)
	}
	if len(session.runner.requests) != 0 {
		t.Fatalf("legacy receipt refusal reached Docker: %#v", session.runner.requests)
	}
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
			engine, runner, _, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
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
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.inspect = func(inspection generatedcontainer.Inspection) generatedcontainer.Inspection {
		inspection.Privileged = true
		return inspection
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticCandidateHardeningFailed) {
		t.Fatalf("expected hardening failure, got %v", err)
	}
	if err := session.execution.RemoveInactive(invocation); !IsCode(err, DiagnosticCandidateHardeningFailed) {
		t.Fatalf("explicit cleanup should recheck hardening, got %v", err)
	}
	for _, request := range session.runner.requests {
		if commandKind(request.Args) == "container rm" {
			t.Fatal("hardening failure removed a receipt-owned candidate")
		}
	}
}

func TestGeneratedRuntimeReconcilesAmbiguousCreateByDeterministicName(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.create = func() runtimeRequestResult { return runtimeRequestResult{err: context.DeadlineExceeded} }
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticRuntimeTimeout) {
		t.Fatalf("expected original create timeout, got %v", err)
	}
	for _, request := range session.runner.requests {
		if commandKind(request.Args) == "container rm" {
			t.Fatal("ambiguous create implicitly removed the candidate")
		}
	}
	if err := session.execution.RemoveInactive(invocation); err != nil {
		t.Fatalf("original invocation could not remove its reconciled candidate: %v", err)
	}
	findRuntimeRequest(t, session.runner.requests, "container rm")
}

func TestGeneratedRuntimeFailsClosedWhenAmbiguousCreateOwnershipCannotBeProved(t *testing.T) {
	limits := defaultLimits()
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), limits,
		fixedDomainCapacity{memory: uint64(limits.MemoryBytes), disk: 1 << 30})
	program.create = func() runtimeRequestResult { return runtimeRequestResult{err: context.DeadlineExceeded} }
	program.inspect = func(inspection generatedcontainer.Inspection) generatedcontainer.Inspection {
		inspection.Labels["io.rig.deployment"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		return inspection
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticRuntimeTimeout) {
		t.Fatalf("expected unresolved create timeout, got %v", err)
	}
	if err := session.execution.RemoveInactive(invocation); !IsCode(err, DiagnosticCandidateHardeningFailed) {
		t.Fatalf("unproved ownership cleanup was accepted: %v", err)
	}
	competing, acquireErr := session.engine.candidates.ReserveRetainedCapacity(session.authority.context,
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1)
	if competing != nil {
		_ = session.engine.candidates.ReleaseRetainedCapacity(
			"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", competing)
	}
	if !errors.Is(acquireErr, generatedcapacity.ErrInsufficientCapacity) {
		t.Fatalf("unresolved candidate released admission to a competing replacement: %v", acquireErr)
	}
	for _, request := range session.runner.requests {
		if commandKind(request.Args) == "container rm" {
			t.Fatal("unowned container reached removal")
		}
	}
}

func TestGeneratedRuntimeReconcilesAmbiguousStart(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	if _, err := session.execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	program.start = func() runtimeRequestResult { return runtimeRequestResult{err: context.DeadlineExceeded} }
	if err := session.execution.StartCandidate(invocation); err != nil {
		t.Fatalf("lost acknowledgement did not accept the exact running candidate: %v", err)
	}
	if starts := countRuntimeRequests(session.runner.requests, "container start"); starts != 1 {
		t.Fatalf("lost acknowledgement retried start %d times", starts)
	}
	for _, request := range session.runner.requests {
		if commandKind(request.Args) == "container rm" {
			t.Fatal("lost start acknowledgement implicitly removed the candidate")
		}
	}
}

func TestGeneratedRuntimeHealthWaitFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		running  bool
		health   string
		expected DiagnosticCode
	}{
		{name: "unhealthy", running: true, health: "unhealthy", expected: DiagnosticCandidateUnhealthy},
		{name: "exited", running: false, health: "starting", expected: DiagnosticCandidateExited},
		{name: "missing health", running: true, health: "", expected: DiagnosticCandidateHardeningFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
			if _, err := session.execution.CreateAndRecord(invocation); err != nil {
				t.Fatal(err)
			}
			if err := session.execution.StartCandidate(invocation); err != nil {
				t.Fatal(err)
			}
			session.finishStarting(t)
			program.health = test.health
			if !test.running {
				program.running = false
				program.started = true
			}
			if err := session.execution.WaitHealthyAndRecord(invocation); !IsCode(err, test.expected) {
				t.Fatalf("expected %s, got %v", test.expected, err)
			}
			for _, request := range session.runner.requests {
				if commandKind(request.Args) == "container rm" {
					t.Fatal("failed health candidate was implicitly removed")
				}
			}
			if err := session.execution.RemoveInactive(invocation); err != nil {
				t.Fatalf("original invocation failed explicit cleanup: %v", err)
			}
			findRuntimeRequest(t, session.runner.requests, "container rm")
		})
	}
}

func TestGeneratedRuntimeRefusesNetworkOwnershipDrift(t *testing.T) {
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.network = func(network networkInspection) networkInspection {
		network.Labels["io.rig.managed"] = "somebody-else"
		return network
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if !IsCode(err, DiagnosticNetworkDriftDetected) {
		t.Fatalf("expected network drift, got %v", err)
	}
	if len(session.runner.requests) != 2 {
		t.Fatalf("network drift should stop after two read-only inspections, got %d", len(session.runner.requests))
	}
}

func TestGeneratedRuntimeErrorsNeverIncludeDockerOutput(t *testing.T) {
	secret := "super-secret-docker-output"
	session, program, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	program.image = func(image imageInspection) imageInspection {
		return image
	}
	session.runner.run = func(runtimeprocess.CommandRequest) runtimeRequestResult {
		return runtimeRequestResult{result: runtimeprocess.CommandResult{Stderr: []byte(secret)}, err: errors.New("exit 1: " + secret)}
	}
	_, err := session.execution.CreateAndRecord(invocation)
	if err == nil || strings.Contains(err.Error(), secret) || !IsCode(err, DiagnosticImageUnavailable) {
		t.Fatalf("unsafe error classification: %v", err)
	}
}

func TestGeneratedRuntimeRejectsUnavailableProtectedConfiguration(t *testing.T) {
	session, _, invocation := newPreparedRuntimeCandidate(t, candidateSpec(), defaultLimits(), fixedDomainCapacity{})
	session.environment.stageError = errors.New("stager unavailable")
	if _, err := session.execution.CreateAndRecord(invocation); !IsCode(err, DiagnosticConfigurationUnavailable) {
		t.Fatalf("expected protected configuration failure, got %v", err)
	}
	for _, request := range session.runner.requests {
		if commandKind(request.Args) == "container create" {
			t.Fatal("unavailable protected configuration reached create")
		}
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
	engine, runner, _, _ := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
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
	engine, runner, spec, runtimeContext := newRuntimeTestEngine(t, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
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
	if err := engine.EnsureAppNetwork(runtimeContext, spec.Network); err != nil {
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
		RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health", ImageContentID: testImageID,
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
		Component: spec.ComponentName, Role: spec.Role, Technology: spec.Technology, Slot: slot, ContainerID: testContainerID,
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

type templateDockerState struct {
	Status     string
	Running    bool
	Paused     bool
	Restarting bool
	Dead       bool
	ExitCode   int
	Health     *templateDockerHealth
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
	inspection := containerInspection{
		ID: testContainerID, Name: "/" + containerName(spec.AppID, spec.ComponentName, slot), Image: spec.ImageContentID,
		Labels: runtimeLabels(spec, slot), User: containerUser, WorkingDirectory: runtimeWorkingDirectory(spec.RootDirectory),
		Command: []string{"/bin/sh", "-lc", spec.RunCommand}, HealthTest: []string{"CMD-SHELL", healthCommand},
		HealthInterval: int64(2 * time.Second), HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: limits.MemoryBytes, MemorySwap: limits.MemoryBytes, NanoCPUs: limits.MilliCPUs * 1_000_000, PIDs: limits.PIDs,
		NetworkMode: network, ReadonlyRootfs: true, Init: true, CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges:true"}, Ulimits: []ulimitInspection{{Name: "nofile", Soft: 1024, Hard: 1024}},
		Tmpfs:   map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=" + stringInt(limits.TmpfsBytes)},
		LogType: "local", LogConfig: map[string]string{"max-size": limits.LogSize, "max-file": stringInt(int64(limits.LogFiles))}, Restart: "no",
		Networks: map[string]networkAttachmentInspection{network: {Aliases: []string{containerAlias(spec.ComponentName, slot)}}},
	}
	if spec.Technology == "nextjs" {
		inspection.Tmpfs[nextCacheDirectory(runtimeWorkingDirectory(spec.RootDirectory))] = "rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=" + stringInt(nextCacheTmpfsBytes)
	}
	return inspection
}

func configuredInspection(spec CandidateSpec, limits ContainerLimits) containerInspection {
	inspection := hardenedInspection(spec, limits)
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

func countRuntimeRequests(requests []runtimeprocess.CommandRequest, kind string) int {
	count := 0
	for _, request := range requests {
		if commandKind(request.Args) == kind {
			count++
		}
	}
	return count
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

func newRuntimeTestEngine(t *testing.T, run func(runtimeprocess.CommandRequest) runtimeRequestResult) (*Engine, *runtimeFakeRunner, CandidateSpec, context.Context) {
	t.Helper()
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtimeFakeRunner{run: run}
	environment := &runtimeFakeEnvironment{path: filepath.Join(root, "env")}
	options := EngineOptions{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second}
	fixture := newRuntimeDomainFixture(t, root, runner, environment, options, generateddomain.Options{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30})
	authority := newRuntimeNetworkAuthority(t, fixture, candidateSpec())
	return fixture.engine, runner, authority.spec, authority.context
}
