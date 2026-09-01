package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveNodeImage = "node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5"

type liveEnvironmentStager struct{}

func (liveEnvironmentStager) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("live lifecycle test does not stage configuration")
}

type liveCapacitySource struct{}

func (liveCapacitySource) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 8 << 30}, nil
}

const (
	liveCreateObservationTimeout     = 5 * time.Second
	liveCreateObservationOutputLimit = 64 << 10
)

// liveCreateObserver is test-only. It observes the generated-runtime create
// result before Engine can inspect or clean it, but retains only fixed boolean
// hardening checks; IDs, inspect output, and Docker configuration never leave
// Run's stack frame.
type liveCreateObserver struct {
	delegate runtimeprocess.CommandRunner
	mu       sync.Mutex
	latest   liveCreateObservation
}

type liveCreateObservation struct {
	createSucceeded bool
	inspectReason   liveCreateInspectReason
	hardening       liveCreateHardening
}

type liveCreateHardening struct {
	NetworkMode       bool
	Tmpfs             bool
	PIDsLimit         bool
	Init              bool
	SecurityOptions   bool
	HealthStartPeriod bool
	MountsRealized    bool
	NetworkRealized   bool
}

type liveCreateInspectReason string

const (
	liveCreateInspectNone            liveCreateInspectReason = "none"
	liveCreateInspectCommandFailed   liveCreateInspectReason = "inspect_command_failed"
	liveCreateInspectOutputTruncated liveCreateInspectReason = "inspect_output_truncated"
	liveCreateInspectDecodeFailed    liveCreateInspectReason = "inspect_decode_failed"
	liveCreateInspectMissing         liveCreateInspectReason = "inspect_missing"
)

type liveCreateExpectation struct {
	network string
	tmpfs   string
	pids    int64
}

// liveRawContainerInspection is deliberately allowlisted. It exists only
// while the bounded inspect output is decoded and is cleared before a caller
// can store any observation.
type liveRawContainerInspection struct {
	Config struct {
		Healthcheck *liveRawHealthcheck `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig      liveRawHostConfig       `json:"HostConfig"`
	Mounts          []liveRawMount          `json:"Mounts"`
	NetworkSettings *liveRawNetworkSettings `json:"NetworkSettings"`
}

type liveRawHealthcheck struct {
	StartPeriod int64 `json:"StartPeriod"`
}

type liveRawHostConfig struct {
	NetworkMode string            `json:"NetworkMode"`
	Tmpfs       map[string]string `json:"Tmpfs"`
	PidsLimit   *int64            `json:"PidsLimit"`
	Init        *bool             `json:"Init"`
	SecurityOpt []string          `json:"SecurityOpt"`
}

type liveRawMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type liveRawNetworkSettings struct {
	Networks map[string]struct{} `json:"Networks"`
}

func (observer *liveCreateObserver) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	result, err := observer.delegate.Run(ctx, request)
	expectation, observe := liveGeneratedRuntimeCreateExpectation(request.Args)
	if !observe || err != nil || result.StdoutTruncated || result.StderrTruncated {
		return result, err
	}
	containerID := bytes.TrimSpace(result.Stdout)
	if !liveContainerID(containerID) {
		return result, err
	}
	observation := observer.inspectCreatedContainer(ctx, request, string(containerID), expectation)
	observer.mu.Lock()
	observer.latest = observation
	observer.mu.Unlock()
	return result, err
}

func liveGeneratedRuntimeCreateExpectation(args []string) (liveCreateExpectation, bool) {
	if len(args) < 2 || args[0] != "container" || args[1] != "create" {
		return liveCreateExpectation{}, false
	}
	var expectation liveCreateExpectation
	managed := false
	for index := 2; index+1 < len(args); index++ {
		switch args[index] {
		case "--network":
			expectation.network = args[index+1]
			index++
		case "--tmpfs":
			expectation.tmpfs = args[index+1]
			index++
		case "--pids-limit":
			value, err := strconv.ParseInt(args[index+1], 10, 64)
			if err == nil && value > 0 {
				expectation.pids = value
			}
			index++
		case "--label":
			managed = args[index+1] == "io.rig.managed=generated-runtime" || managed
			index++
		}
	}
	return expectation, managed && expectation.network != "" && expectation.tmpfs != "" && expectation.pids > 0
}

func (observer *liveCreateObserver) inspectCreatedContainer(ctx context.Context, request runtimeprocess.CommandRequest, containerID string, expectation liveCreateExpectation) liveCreateObservation {
	inspection, err := observer.delegate.Run(ctx, runtimeprocess.CommandRequest{
		Executable:  request.Executable,
		Args:        []string{"container", "inspect", containerID},
		Directory:   request.Directory,
		Env:         append([]string(nil), request.Env...),
		Timeout:     liveCreateObservationTimeout,
		OutputLimit: liveCreateObservationOutputLimit,
	})
	return liveCreateObservationFromInspectResult(&inspection, err, expectation)
}

func liveCreateObservationFromInspectResult(result *runtimeprocess.CommandResult, err error, expectation liveCreateExpectation) liveCreateObservation {
	observation := liveCreateObservation{createSucceeded: true, inspectReason: liveCreateInspectNone}
	if err != nil {
		clearLiveCommandResult(result)
		observation.inspectReason = liveCreateInspectCommandFailed
		return observation
	}
	if result.StdoutTruncated || result.StderrTruncated {
		clearLiveCommandResult(result)
		observation.inspectReason = liveCreateInspectOutputTruncated
		return observation
	}
	hardening, reason := decodeLiveCreateInspection(result.Stdout, expectation)
	clearLiveCommandResult(result)
	if reason != liveCreateInspectNone {
		observation.inspectReason = reason
		return observation
	}
	observation.hardening = hardening
	return observation
}

func decodeLiveCreateInspection(raw []byte, expectation liveCreateExpectation) (liveCreateHardening, liveCreateInspectReason) {
	var containers []liveRawContainerInspection
	err := json.Unmarshal(raw, &containers)
	clear(raw)
	if err != nil {
		return liveCreateHardening{}, liveCreateInspectDecodeFailed
	}
	if len(containers) != 1 {
		clearLiveRawContainerInspections(containers)
		return liveCreateHardening{}, liveCreateInspectMissing
	}
	hardening := liveCreateHardeningFromRaw(containers[0], expectation)
	clearLiveRawContainerInspections(containers)
	return hardening, liveCreateInspectNone
}

func liveCreateHardeningFromRaw(container liveRawContainerInspection, expectation liveCreateExpectation) liveCreateHardening {
	hardening := liveCreateHardening{
		NetworkMode:     container.HostConfig.NetworkMode == expectation.network,
		Tmpfs:           len(container.HostConfig.Tmpfs) == 1 && container.HostConfig.Tmpfs["/tmp"] == expectation.tmpfs,
		SecurityOptions: liveOnlyNoNewPrivileges(container.HostConfig.SecurityOpt),
		MountsRealized:  liveOnlyRuntimeTmpfsMount(container.Mounts),
		NetworkRealized: container.NetworkSettings != nil && networkExists(container.NetworkSettings.Networks, expectation.network),
	}
	if container.HostConfig.PidsLimit != nil {
		hardening.PIDsLimit = *container.HostConfig.PidsLimit == expectation.pids
	}
	if container.HostConfig.Init != nil {
		hardening.Init = *container.HostConfig.Init
	}
	if container.Config.Healthcheck != nil {
		hardening.HealthStartPeriod = container.Config.Healthcheck.StartPeriod == int64(5*time.Second)
	}
	return hardening
}

func liveOnlyNoNewPrivileges(values []string) bool {
	if len(values) != 1 {
		return false
	}
	switch strings.ToLower(values[0]) {
	case "no-new-privileges", "no-new-privileges:true", "no-new-privileges=true":
		return true
	default:
		return false
	}
}

func liveOnlyRuntimeTmpfsMount(mounts []liveRawMount) bool {
	return len(mounts) == 1 && mounts[0].Type == "tmpfs" && mounts[0].Source == "" && mounts[0].Destination == "/tmp" && mounts[0].RW
}

func networkExists(networks map[string]struct{}, expected string) bool {
	_, exists := networks[expected]
	return exists
}

func clearLiveRawContainerInspections(containers []liveRawContainerInspection) {
	for index := range containers {
		container := &containers[index]
		if container.Config.Healthcheck != nil {
			*container.Config.Healthcheck = liveRawHealthcheck{}
		}
		if container.HostConfig.PidsLimit != nil {
			*container.HostConfig.PidsLimit = 0
		}
		if container.HostConfig.Init != nil {
			*container.HostConfig.Init = false
		}
		clear(container.HostConfig.Tmpfs)
		clear(container.HostConfig.SecurityOpt)
		clear(container.Mounts)
		if container.NetworkSettings != nil {
			clear(container.NetworkSettings.Networks)
			*container.NetworkSettings = liveRawNetworkSettings{}
		}
		*container = liveRawContainerInspection{}
	}
	clear(containers)
}

func liveContainerID(value []byte) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func clearLiveCommandResult(result *runtimeprocess.CommandResult) {
	if result == nil {
		return
	}
	clear(result.Stdout)
	clear(result.Stderr)
	*result = runtimeprocess.CommandResult{}
}

func (observer *liveCreateObserver) reset() {
	observer.mu.Lock()
	observer.latest = liveCreateObservation{}
	observer.mu.Unlock()
}

func (observer *liveCreateObserver) failureDiagnostic() string {
	observer.mu.Lock()
	observation := observer.latest
	observer.mu.Unlock()
	mismatches := "none"
	reason := observation.inspectReason
	if reason == "" {
		reason = liveCreateInspectNone
	}
	if observation.createSucceeded && reason == liveCreateInspectNone {
		if names := liveCreateHardeningMismatches(observation.hardening); len(names) > 0 {
			mismatches = strings.Join(names, ",")
		}
	}
	return " generated_runtime_create_observed=" + strconv.FormatBool(observation.createSucceeded) +
		" inspect_reason=" + string(reason) +
		" hardening_mismatches=" + mismatches
}

func liveCreateHardeningMismatches(hardening liveCreateHardening) []string {
	checks := []struct {
		name string
		ok   bool
	}{
		{name: "network_mode", ok: hardening.NetworkMode},
		{name: "tmpfs", ok: hardening.Tmpfs},
		{name: "pids_limit", ok: hardening.PIDsLimit},
		{name: "init", ok: hardening.Init},
		{name: "security_options", ok: hardening.SecurityOptions},
		{name: "health_start_period", ok: hardening.HealthStartPeriod},
		{name: "mounts_realized", ok: hardening.MountsRealized},
		{name: "network_realized", ok: hardening.NetworkRealized},
	}
	mismatches := make([]string, 0, len(checks))
	for _, check := range checks {
		if !check.ok {
			mismatches = append(mismatches, check.name)
		}
	}
	return mismatches
}

func TestLiveGeneratedBlueGreenLifecycle(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_GENERATED_RUNTIME") != "1" {
		t.Skip("set RIG_RUN_LIVE_GENERATED_RUNTIME=1 on a disposable Linux Docker host")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker executable is unavailable")
	}
	docker, err = filepath.Abs(docker)
	if err != nil {
		t.Fatal("resolve docker executable")
	}
	root := t.TempDir()
	dockerConfig := filepath.Join(root, "docker-config")
	working := filepath.Join(root, "working")
	state := filepath.Join(root, "ingress-state")
	for _, directory := range []string{dockerConfig, working, state} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create live lifecycle directory: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if output, err := dockerCommand(ctx, docker, root, "info"); err != nil {
		t.Fatalf("docker daemon unavailable: %v: %s", err, output)
	}

	appID := "11111111-1111-4111-8111-111111111111"
	planID := "22222222-2222-4222-8222-222222222222"
	network, err := generatedruntime.DescribeAppNetwork(appID)
	if err != nil {
		t.Fatal(err)
	}
	hostPort := freeLoopbackPort(t)
	imageTags := []string{"rig-generated-live-test:blue", "rig-generated-live-test:green"}
	defer cleanupLiveDocker(t, docker, root, network.Name, imageTags)

	runner := &liveCreateObserver{delegate: runtimeprocess.ExecRunner{}}
	limits := generatedruntime.ContainerLimits{
		MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 128,
		TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 1,
	}
	engine, err := generatedruntime.NewEngine(runner, liveEnvironmentStager{}, liveCapacitySource{}, generatedruntime.EngineOptions{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		CommandTimeout: 45 * time.Second, HealthTimeout: 90 * time.Second, HealthPollInterval: 250 * time.Millisecond,
		OutputLimit: 64 << 10, Limits: limits, ReplacementDiskBytes: 96 << 20,
	})
	if err != nil {
		t.Fatalf("create generated runtime: %v", err)
	}
	ingress, err := New(runner, Options{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		DataRoot: state, HostPort: hostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: 64 << 10,
	})
	if err != nil {
		t.Fatalf("create generated ingress: %v", err)
	}

	blueSpec := liveCandidateSpec("blue", appID, planID)
	blueSpec.ImageContentID = buildLiveImage(t, ctx, docker, root, imageTags[0], blueSpec, "blue")
	blue := startLiveCandidate(t, ctx, engine, runner, blueSpec)
	defer func() { _ = engine.StopAndRemove(context.Background(), blue, 0) }()
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{
		AppID: appID, ToSlot: blue.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(blue)},
	}); err != nil {
		t.Fatalf("route first slot: %v", err)
	}
	assertLiveResponse(t, hostPort, appID, "blue")
	engine.ReleaseAdmission(blue)

	greenSpec := liveCandidateSpec("green", appID, planID)
	greenSpec.ActiveSlot = blue.Slot
	greenSpec.ImageContentID = buildLiveImage(t, ctx, docker, root, imageTags[1], greenSpec, "green")
	green := startLiveCandidate(t, ctx, engine, runner, greenSpec)
	defer func() { _ = engine.StopAndRemove(context.Background(), green, 0) }()

	// A healthy inactive candidate must not receive traffic before the route commit.
	assertLiveResponse(t, hostPort, appID, "blue")
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{
		AppID: appID, FromSlot: blue.Slot, ToSlot: green.Slot,
		Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(green)}, DrainPeriod: 100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("switch to replacement slot: %v", err)
	}
	assertLiveResponse(t, hostPort, appID, "green")
	if err := engine.StopAndRemove(ctx, blue, time.Second); err != nil {
		t.Fatalf("remove drained slot: %v", err)
	}
	assertLiveResponse(t, hostPort, appID, "green")
	engine.ReleaseAdmission(green)
}

func TestLiveCreateObservationDiagnosticIsRedacted(t *testing.T) {
	diagnostic := (&liveCreateObserver{latest: liveCreateObservation{
		createSucceeded: true, inspectReason: liveCreateInspectNone,
		hardening: liveCreateHardening{
			NetworkMode: false, Tmpfs: false, PIDsLimit: true, Init: true, SecurityOptions: false,
			HealthStartPeriod: true, MountsRealized: false, NetworkRealized: true,
		},
	}}).failureDiagnostic()
	const want = " generated_runtime_create_observed=true inspect_reason=none hardening_mismatches=network_mode,tmpfs,security_options,mounts_realized"
	if diagnostic != want {
		t.Fatalf("redacted observation diagnostic = %q, want %q", diagnostic, want)
	}
	for _, forbidden := range []string{"sha256:", "io.rig.", "/workspace", "container", "label"} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("redacted observation diagnostic exposed %q: %q", forbidden, diagnostic)
		}
	}
}

func TestLiveCreateObserverResetDropsPriorAttempt(t *testing.T) {
	observer := &liveCreateObserver{latest: liveCreateObservation{
		createSucceeded: true, inspectReason: liveCreateInspectNone, hardening: liveCreateHardening{NetworkMode: false},
	}}
	observer.reset()
	const want = " generated_runtime_create_observed=false inspect_reason=none hardening_mismatches=none"
	if diagnostic := observer.failureDiagnostic(); diagnostic != want {
		t.Fatalf("reset observation diagnostic = %q, want %q", diagnostic, want)
	}
}

func TestLiveCreateObserverFiltersOtherContainerCreates(t *testing.T) {
	arguments := []string{
		"container", "create", "--network", "test-network", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16777216",
		"--pids-limit", "128", "--label", "io.rig.managed=generated-runtime",
	}
	expectation, observed := liveGeneratedRuntimeCreateExpectation(arguments)
	if !observed || expectation.network != "test-network" || expectation.tmpfs == "" || expectation.pids != 128 {
		t.Fatalf("generated-runtime create was not observed: observed=%t network_present=%t tmpfs_present=%t pids_valid=%t", observed, expectation.network != "", expectation.tmpfs != "", expectation.pids == 128)
	}
	arguments[len(arguments)-1] = "io.rig.managed=generated-ingress"
	if _, observed := liveGeneratedRuntimeCreateExpectation(arguments); observed {
		t.Fatal("non-runtime container create reached the diagnostic observer")
	}
}

func TestLiveRawInspectDecodeClassifiesAndRedacts(t *testing.T) {
	expectation := liveCreateExpectation{network: "test-network", tmpfs: "/tmp:rw,noexec,nosuid,nodev,size=16777216", pids: 128}
	raw := []byte(`[{"Name":"sensitive-container","Image":"sha256:aaaaaaaa","Config":{"Healthcheck":{"StartPeriod":5000000000},"Cmd":["sensitive-command"],"Env":["SENSITIVE=value"]},"HostConfig":{"NetworkMode":"test-network","Tmpfs":{"/tmp":"/tmp:rw,noexec,nosuid,nodev,size=16777216"},"PidsLimit":128,"Init":true,"SecurityOpt":["no-new-privileges:true"]},"Mounts":[{"Type":"tmpfs","Source":"","Destination":"/tmp","RW":true}],"NetworkSettings":{"Networks":{"test-network":{"Aliases":["sensitive-alias"]}}},"Labels":{"sensitive-label":"sensitive-value"}}]`)
	result := runtimeprocess.CommandResult{Stdout: raw, Stderr: []byte("sensitive-stderr")}
	observation := liveCreateObservationFromInspectResult(&result, nil, expectation)
	if observation.inspectReason != liveCreateInspectNone {
		t.Fatalf("raw inspect reason = %q", observation.inspectReason)
	}
	if names := liveCreateHardeningMismatches(observation.hardening); len(names) != 0 {
		t.Fatalf("raw inspect hardening mismatches = %v", names)
	}
	for _, value := range raw {
		if value != 0 {
			t.Fatal("raw inspect stdout was retained")
		}
	}
	if result.Stdout != nil || result.Stderr != nil {
		t.Fatal("raw inspect result was retained")
	}
	diagnostic := (&liveCreateObserver{latest: observation}).failureDiagnostic()
	const want = " generated_runtime_create_observed=true inspect_reason=none hardening_mismatches=none"
	if diagnostic != want {
		t.Fatalf("redacted raw inspect diagnostic = %q, want %q", diagnostic, want)
	}
	for _, forbidden := range []string{"sensitive-container", "sha256:", "sensitive-command", "SENSITIVE=", "/tmp", "sensitive-label", "sensitive-stderr"} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("raw inspect diagnostic exposed %q", forbidden)
		}
	}
}

func TestLiveRawInspectClassifiesFixedReasons(t *testing.T) {
	expectation := liveCreateExpectation{network: "test-network", tmpfs: "/tmp:rw", pids: 128}
	for _, test := range []struct {
		name      string
		output    string
		truncated bool
		err       error
		want      liveCreateInspectReason
	}{
		{name: "command failed", output: "[]", err: errors.New("inspect failed"), want: liveCreateInspectCommandFailed},
		{name: "output truncated", output: "[]", truncated: true, want: liveCreateInspectOutputTruncated},
		{name: "decode failed", output: "{", want: liveCreateInspectDecodeFailed},
		{name: "container missing", output: "[]", want: liveCreateInspectMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := []byte(test.output)
			stderr := []byte("sensitive-stderr")
			result := runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr, StdoutTruncated: test.truncated}
			observation := liveCreateObservationFromInspectResult(&result, test.err, expectation)
			if observation.inspectReason != test.want {
				t.Fatalf("inspect reason = %q, want %q", observation.inspectReason, test.want)
			}
			for _, value := range append(stdout, stderr...) {
				if value != 0 {
					t.Fatal("inspect result bytes were retained")
				}
			}
			if diagnostic := (&liveCreateObserver{latest: observation}).failureDiagnostic(); !strings.Contains(diagnostic, "inspect_reason="+string(test.want)) || strings.Contains(diagnostic, "sensitive-stderr") {
				t.Fatalf("fixed inspect classification diagnostic = %q", diagnostic)
			}
		})
	}
}

func liveCandidateSpec(version, appID, planID string) generatedruntime.CandidateSpec {
	identities := map[string][]string{
		"blue":  {"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"},
		"green": {"66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888"},
	}
	values := identities[version]
	return generatedruntime.CandidateSpec{
		AppID: appID, ReleaseID: values[0], DeploymentID: values[1], ArtifactID: values[2],
		DeploymentPlanRevisionID: planID, ComponentName: "api", Role: generatedruntime.RoleServer,
		RootDirectory: ".", RunCommand: "node server.mjs", InternalPort: 3000, HealthProbe: "/health",
		BuildDefinitionDigest: strings.Repeat(map[string]string{"blue": "a", "green": "b"}[version], 64),
	}
}

func buildLiveImage(t *testing.T, ctx context.Context, docker, root, tag string, spec generatedruntime.CandidateSpec, version string) string {
	t.Helper()
	contextRoot := filepath.Join(root, "image-"+version)
	if err := os.Mkdir(contextRoot, 0o700); err != nil {
		t.Fatalf("create image context: %v", err)
	}
	containerfile := fmt.Sprintf("FROM %s\nWORKDIR /workspace\nCOPY --chmod=0555 rig-entrypoint /usr/local/bin/rig-entrypoint\nCOPY --chown=node:node server.mjs /workspace/server.mjs\nUSER node\nENTRYPOINT [\"/usr/local/bin/rig-entrypoint\"]\n", liveNodeImage)
	entrypoint := "#!/bin/sh\nset -eu\nexec \"$@\"\n"
	server := fmt.Sprintf("import { createServer } from 'node:http';\nconst version = %q;\nconst port = Number(process.env.RIG_RUNTIME_INTERNAL_PORT);\ncreateServer((request, response) => { response.writeHead(200, { 'content-type': 'text/plain' }); response.end(version); }).listen(port, '0.0.0.0');\n", version)
	for name, contents := range map[string]string{"Dockerfile": containerfile, "rig-entrypoint": entrypoint, "server.mjs": server} {
		if err := os.WriteFile(filepath.Join(contextRoot, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write image fixture: %v", err)
		}
	}
	labels := map[string]string{
		"io.rig.managed": "generated-image", "io.rig.application": spec.AppID,
		"io.rig.release": spec.ReleaseID, "io.rig.artifact": spec.ArtifactID,
		"io.rig.plan": spec.DeploymentPlanRevisionID, "io.rig.component": spec.ComponentName,
		"io.rig.role": spec.Role, "io.rig.definition": spec.BuildDefinitionDigest,
	}
	args := []string{"build", "--pull", "--quiet", "--tag", tag}
	for _, key := range []string{"io.rig.managed", "io.rig.application", "io.rig.release", "io.rig.artifact", "io.rig.plan", "io.rig.component", "io.rig.role", "io.rig.definition"} {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, contextRoot)
	if output, err := dockerCommand(ctx, docker, root, args...); err != nil {
		t.Fatalf("build %s image: %v: %s", version, err, output)
	}
	output, err := dockerCommand(ctx, docker, root, "image", "inspect", "--format", "{{.ID}}", tag)
	if err != nil {
		t.Fatalf("inspect %s image: %v: %s", version, err, output)
	}
	return strings.TrimSpace(output)
}

func startLiveCandidate(t *testing.T, ctx context.Context, engine *generatedruntime.Engine, observer *liveCreateObserver, spec generatedruntime.CandidateSpec) generatedruntime.Candidate {
	t.Helper()
	observer.reset()
	candidate, err := engine.CreateInactiveCandidate(ctx, spec)
	if err != nil {
		t.Fatalf("create candidate: %v%s", err, observer.failureDiagnostic())
	}
	if err := engine.StartCandidate(ctx, candidate); err != nil {
		t.Fatalf("start candidate: %v%s", err, observer.failureDiagnostic())
	}
	if err := engine.WaitHealthy(ctx, candidate); err != nil {
		t.Fatalf("wait for candidate: %v%s", err, observer.failureDiagnostic())
	}
	return candidate
}

func liveEndpoint(candidate generatedruntime.Candidate) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{
		Component: candidate.Component, Role: candidate.Role, ContainerID: candidate.ContainerID,
		NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort,
	}
}

func assertLiveResponse(t *testing.T, port uint16, appID, expected string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = appID + ".rig.localhost"
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 64))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && string(body) == expected {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("generated ingress did not serve %q", expected)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func freeLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return uint16(port)
}

func cleanupLiveDocker(t *testing.T, docker, root, appNetwork string, imageTags []string) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	commands := [][]string{
		{"container", "rm", "--force", caddyContainerName},
		{"network", "rm", appNetwork, caddyNetworkName},
		{"volume", "rm", "--force", caddyVolumeName},
		{"image", "rm", "--force", imageTags[0], imageTags[1]},
	}
	for _, args := range commands {
		_, _ = dockerCommand(cleanupCtx, docker, root, args...)
	}
}

func dockerCommand(ctx context.Context, docker, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, docker, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
