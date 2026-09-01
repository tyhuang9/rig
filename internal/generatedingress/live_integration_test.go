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

// liveCreateObserver is test-only. It observes the generated-runtime lifecycle
// without retaining IDs or Docker output. It stores only fixed booleans and
// fixed diagnostic reasons.
type liveCreateObserver struct {
	delegate      runtimeprocess.CommandRunner
	mu            sync.Mutex
	latest        liveCreateObservation
	awaitingStart bool
}

type liveCreateObservation struct {
	createSucceeded bool
	inspectReason   liveCreateInspectReason
	hardening       liveCreateHardening
	realization     liveCreateRealization
	startReason     liveStartReason
}

type liveCreateHardening struct {
	NetworkMode       bool
	Tmpfs             bool
	PIDsLimit         bool
	Init              bool
	SecurityOptions   bool
	HealthStartPeriod bool
}

type liveCreateRealization struct {
	MountsRealized  bool
	NetworkRealized bool
}

type liveCreateInspectReason string

const (
	liveCreateInspectNone            liveCreateInspectReason = "none"
	liveCreateInspectCommandFailed   liveCreateInspectReason = "inspect_command_failed"
	liveCreateInspectOutputTruncated liveCreateInspectReason = "inspect_output_truncated"
	liveCreateInspectDecodeFailed    liveCreateInspectReason = "inspect_decode_failed"
	liveCreateInspectMissing         liveCreateInspectReason = "inspect_missing"
)

type liveStartReason string

const (
	liveStartNone                  liveStartReason = "none"
	liveStartCancelled             liveStartReason = "cancelled"
	liveStartTimeout               liveStartReason = "timeout"
	liveStartProcessTermination    liveStartReason = "process_termination_failed"
	liveStartOutputTruncated       liveStartReason = "output_truncated"
	liveStartDaemonUnavailable     liveStartReason = "daemon_unavailable"
	liveStartDockerInitFailed      liveStartReason = "docker_init_failed"
	liveStartDockerInitMissing     liveStartReason = "docker_init_missing"
	liveStartUserLookupFailed      liveStartReason = "user_lookup_failed"
	liveStartWorkdirFailed         liveStartReason = "workdir_failed"
	liveStartOCIMountFailed        liveStartReason = "oci_mount_failed"
	liveStartOCIEntrypointDenied   liveStartReason = "oci_entrypoint_permission"
	liveStartOCIEntrypointMissing  liveStartReason = "oci_entrypoint_missing"
	liveStartOCINetworkFailed      liveStartReason = "oci_network_failed"
	liveStartOCIResourceFailed     liveStartReason = "oci_resource_failed"
	liveStartPermissionDenied      liveStartReason = "permission_denied"
	liveStartFileMissing           liveStartReason = "file_missing"
	liveStartReadOnlyFilesystem    liveStartReason = "read_only_filesystem"
	liveStartOperationNotPermitted liveStartReason = "operation_not_permitted"
	liveStartInvalidArgument       liveStartReason = "invalid_argument"
	liveStartNoSpace               liveStartReason = "no_space"
	liveStartMemoryExhausted       liveStartReason = "memory_exhausted"
	liveStartPIDsExhausted         liveStartReason = "pids_exhausted"
	liveStartResourceFailed        liveStartReason = "resource_failed"
	liveStartSecurityPolicyFailed  liveStartReason = "security_policy_failed"
	liveStartCreateTaskFailed      liveStartReason = "create_task_failed"
	liveStartShimTaskFailed        liveStartReason = "shim_task_failed"
	liveStartTaskStartFailed       liveStartReason = "task_start_failed"
	liveStartOCIRuntimeFailed      liveStartReason = "oci_runtime_failed"
	liveStartCommandFailed         liveStartReason = "command_failed"
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
	if liveContainerStart(request.Args) {
		observer.observeStartResult(ctx, result, err)
		return result, err
	}
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
	observer.awaitingStart = true
	observer.mu.Unlock()
	return result, err
}

func liveContainerStart(args []string) bool {
	return len(args) == 3 && args[0] == "container" && args[1] == "start"
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
			expectation.tmpfs = liveTmpfsOptions(args[index+1])
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

func liveTmpfsOptions(value string) string {
	if !strings.HasPrefix(value, "/tmp:") {
		return ""
	}
	return strings.TrimPrefix(value, "/tmp:")
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
	hardening, realization, reason := decodeLiveCreateInspection(result.Stdout, expectation)
	clearLiveCommandResult(result)
	if reason != liveCreateInspectNone {
		observation.inspectReason = reason
		return observation
	}
	observation.hardening = hardening
	observation.realization = realization
	return observation
}

func decodeLiveCreateInspection(raw []byte, expectation liveCreateExpectation) (liveCreateHardening, liveCreateRealization, liveCreateInspectReason) {
	var containers []liveRawContainerInspection
	err := json.Unmarshal(raw, &containers)
	clear(raw)
	if err != nil {
		return liveCreateHardening{}, liveCreateRealization{}, liveCreateInspectDecodeFailed
	}
	if len(containers) != 1 {
		clearLiveRawContainerInspections(containers)
		return liveCreateHardening{}, liveCreateRealization{}, liveCreateInspectMissing
	}
	hardening, realization := liveCreateHardeningFromRaw(containers[0], expectation)
	clearLiveRawContainerInspections(containers)
	return hardening, realization, liveCreateInspectNone
}

func liveCreateHardeningFromRaw(container liveRawContainerInspection, expectation liveCreateExpectation) (liveCreateHardening, liveCreateRealization) {
	hardening := liveCreateHardening{
		NetworkMode:     container.HostConfig.NetworkMode == expectation.network,
		Tmpfs:           len(container.HostConfig.Tmpfs) == 1 && container.HostConfig.Tmpfs["/tmp"] == expectation.tmpfs,
		SecurityOptions: liveOnlyNoNewPrivileges(container.HostConfig.SecurityOpt),
	}
	realization := liveCreateRealization{
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
	return hardening, realization
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

func (observer *liveCreateObserver) observeStartResult(ctx context.Context, result runtimeprocess.CommandResult, err error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.awaitingStart {
		return
	}
	observer.latest.startReason = classifyLiveStartResult(ctx, result, err)
	observer.awaitingStart = false
}

func classifyLiveStartResult(ctx context.Context, result runtimeprocess.CommandResult, err error) liveStartReason {
	if err == nil {
		return liveStartNone
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return liveStartCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return liveStartTimeout
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return liveStartProcessTermination
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return liveStartOutputTruncated
	}
	var executableError *exec.Error
	var pathError *os.PathError
	if errors.As(err, &executableError) || errors.As(err, &pathError) || liveStartOutputContainsAny(result, "cannot connect to the docker daemon", "error during connect", "is the docker daemon running") {
		return liveStartDaemonUnavailable
	}
	if liveStartOutputContains(result, "failed to set up container networking") {
		return liveStartOCINetworkFailed
	}
	if liveStartOutputContainsAll(result, "docker-init", "no such file or directory") || liveStartOutputContainsAll(result, "docker-init", "executable file not found") {
		return liveStartDockerInitMissing
	}
	if liveStartOutputContains(result, "docker-init") {
		return liveStartDockerInitFailed
	}
	if liveStartOutputContainsAll(result, "unable to find user", "no matching entries in passwd file") {
		return liveStartUserLookupFailed
	}
	if liveStartOutputContainsAll(result, "chdir to cwd (", ") set in config.json failed") {
		return liveStartWorkdirFailed
	}
	if liveStartOutputContainsAll(result, "oci runtime create failed", "error mounting") || liveStartOutputContainsAll(result, "oci runtime create failed", "failed to mount") {
		return liveStartOCIMountFailed
	}
	if liveStartOutputContainsAll(result, "oci runtime create failed", "rig-entrypoint", "permission denied") {
		return liveStartOCIEntrypointDenied
	}
	if liveStartOutputContainsAll(result, "oci runtime create failed", "rig-entrypoint") && liveStartOutputContainsAny(result, "no such file or directory", "executable file not found") {
		return liveStartOCIEntrypointMissing
	}
	if liveStartOutputContainsAll(result, "oci runtime create failed", "failed to setup network") || liveStartOutputContainsAll(result, "oci runtime create failed", "network namespace") {
		return liveStartOCINetworkFailed
	}
	if liveStartOutputContains(result, "invalid tmpfs option") {
		return liveStartInvalidArgument
	}
	if liveStartOutputContainsAll(result, "pids", "resource temporarily unavailable") || liveStartOutputContainsAny(result, "cannot set pids limit", "pids.max") {
		return liveStartPIDsExhausted
	}
	if liveStartOutputContainsAny(result, "unable to init seccomp", "unable to apply apparmor profile") {
		return liveStartSecurityPolicyFailed
	}
	if liveStartOutputContains(result, "unable to apply cgroup configuration") {
		return liveStartResourceFailed
	}
	if liveStartOutputContainsAll(result, "oci runtime create failed", "cgroup") {
		return liveStartOCIResourceFailed
	}
	if liveStartOutputContains(result, "cannot allocate memory") {
		return liveStartMemoryExhausted
	}
	if liveStartOutputContainsAny(result, "error setting rootfs as readonly", "read-only file system") {
		return liveStartReadOnlyFilesystem
	}
	if liveStartOutputContains(result, "operation not permitted") {
		return liveStartOperationNotPermitted
	}
	if liveStartOutputContains(result, "invalid argument") {
		return liveStartInvalidArgument
	}
	if liveStartOutputContains(result, "no space left on device") {
		return liveStartNoSpace
	}
	if liveStartOutputContains(result, "permission denied") {
		return liveStartPermissionDenied
	}
	if liveStartOutputContainsAny(result, "no such file or directory", "executable file not found") {
		return liveStartFileMissing
	}
	if liveStartOutputContains(result, "resource temporarily unavailable") {
		return liveStartResourceFailed
	}
	if liveStartOutputContains(result, "failed to create shim task") {
		return liveStartShimTaskFailed
	}
	if liveStartOutputContains(result, "failed to create task") {
		return liveStartCreateTaskFailed
	}
	if liveStartOutputContainsAny(result, "failed to start task", "oci runtime start failed") {
		return liveStartTaskStartFailed
	}
	if liveStartOutputContainsAny(result, "oci runtime create failed", "runc create failed") {
		return liveStartOCIRuntimeFailed
	}
	return liveStartCommandFailed
}

func liveStartOutputContainsAll(result runtimeprocess.CommandResult, patterns ...string) bool {
	return containsAllASCIIInsensitive(result.Stdout, patterns...) || containsAllASCIIInsensitive(result.Stderr, patterns...)
}

func liveStartOutputContainsAny(result runtimeprocess.CommandResult, patterns ...string) bool {
	for _, pattern := range patterns {
		if liveStartOutputContains(result, pattern) {
			return true
		}
	}
	return false
}

func liveStartOutputContains(result runtimeprocess.CommandResult, pattern string) bool {
	return containsASCIIInsensitive(result.Stdout, pattern) || containsASCIIInsensitive(result.Stderr, pattern)
}

func containsAllASCIIInsensitive(value []byte, patterns ...string) bool {
	for _, pattern := range patterns {
		if !containsASCIIInsensitive(value, pattern) {
			return false
		}
	}
	return true
}

func containsASCIIInsensitive(value []byte, pattern string) bool {
	if len(pattern) == 0 || len(value) < len(pattern) {
		return false
	}
	for start := 0; start <= len(value)-len(pattern); start++ {
		matched := true
		for offset := range len(pattern) {
			if lowerASCII(value[start+offset]) != lowerASCII(pattern[offset]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func lowerASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func (observer *liveCreateObserver) reset() {
	observer.mu.Lock()
	observer.latest = liveCreateObservation{}
	observer.awaitingStart = false
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
	startReason := observation.startReason
	if startReason == "" {
		startReason = liveStartNone
	}
	realization := "unobserved"
	if observation.createSucceeded && reason == liveCreateInspectNone {
		realization = "mounts_realized:" + strconv.FormatBool(observation.realization.MountsRealized) + ",network_realized:" + strconv.FormatBool(observation.realization.NetworkRealized)
	}
	return " generated_runtime_create_observed=" + strconv.FormatBool(observation.createSucceeded) +
		" inspect_reason=" + string(reason) +
		" hardening_mismatches=" + mismatches +
		" realization_state=" + realization +
		" start_reason=" + string(startReason)
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
			HealthStartPeriod: true,
		},
		realization: liveCreateRealization{NetworkRealized: true},
	}}).failureDiagnostic()
	const want = " generated_runtime_create_observed=true inspect_reason=none hardening_mismatches=network_mode,tmpfs,security_options realization_state=mounts_realized:false,network_realized:true start_reason=none"
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
	const want = " generated_runtime_create_observed=false inspect_reason=none hardening_mismatches=none realization_state=unobserved start_reason=none"
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
	if !observed || expectation.network != "test-network" || expectation.tmpfs != "rw,noexec,nosuid,nodev,size=16777216" || expectation.pids != 128 {
		t.Fatalf("generated-runtime create was not observed: observed=%t network_present=%t tmpfs_normalized=%t pids_valid=%t", observed, expectation.network != "", expectation.tmpfs == "rw,noexec,nosuid,nodev,size=16777216", expectation.pids == 128)
	}
	arguments[len(arguments)-1] = "io.rig.managed=generated-ingress"
	if _, observed := liveGeneratedRuntimeCreateExpectation(arguments); observed {
		t.Fatal("non-runtime container create reached the diagnostic observer")
	}
}

func TestLiveRawInspectDecodeClassifiesAndRedacts(t *testing.T) {
	expectation := liveCreateExpectation{network: "test-network", tmpfs: "rw,noexec,nosuid,nodev,size=16777216", pids: 128}
	raw := []byte(`[{"Name":"sensitive-container","Image":"sha256:aaaaaaaa","Config":{"Healthcheck":{"StartPeriod":5000000000},"Cmd":["sensitive-command"],"Env":["SENSITIVE=value"]},"HostConfig":{"NetworkMode":"test-network","Tmpfs":{"/tmp":"rw,noexec,nosuid,nodev,size=16777216"},"PidsLimit":128,"Init":true,"SecurityOpt":["no-new-privileges:true"]},"Mounts":[{"Type":"tmpfs","Source":"","Destination":"/tmp","RW":true}],"NetworkSettings":{"Networks":{"test-network":{"Aliases":["sensitive-alias"]}}},"Labels":{"sensitive-label":"sensitive-value"}}]`)
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
	const want = " generated_runtime_create_observed=true inspect_reason=none hardening_mismatches=none realization_state=mounts_realized:true,network_realized:true start_reason=none"
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

func TestLiveStartResultClassifiesFixedReasonsWithoutDisclosure(t *testing.T) {
	for _, test := range []struct {
		name      string
		output    string
		truncated bool
		err       error
		want      liveStartReason
	}{
		{name: "none", output: "sensitive-start-output", want: liveStartNone},
		{name: "cancelled", output: "sensitive-start-output", err: context.Canceled, want: liveStartCancelled},
		{name: "timeout", output: "sensitive-start-output", err: context.DeadlineExceeded, want: liveStartTimeout},
		{name: "process termination", output: "sensitive-start-output", err: runtimeprocess.ErrTerminationFailed, want: liveStartProcessTermination},
		{name: "output truncated", output: "sensitive-start-output", truncated: true, err: errors.New("command exited"), want: liveStartOutputTruncated},
		{name: "typed daemon unavailable", output: "sensitive-start-output", err: &exec.Error{Name: "docker", Err: errors.New("sensitive-typed-error")}, want: liveStartDaemonUnavailable},
		{name: "daemon unavailable", output: "cannot connect to the Docker daemon: sensitive-start-output", err: errors.New("command exited"), want: liveStartDaemonUnavailable},
		{name: "network before oci", output: "failed to set up container networking: sensitive-start-output", err: errors.New("command exited"), want: liveStartOCINetworkFailed},
		{name: "missing docker init", output: "docker-init: no such file or directory: sensitive-start-output", err: errors.New("command exited"), want: liveStartDockerInitMissing},
		{name: "missing docker init executable", output: "docker-init: executable file not found: sensitive-start-output", err: errors.New("command exited"), want: liveStartDockerInitMissing},
		{name: "docker init failed", output: "docker-init failed: sensitive-start-output", err: errors.New("command exited"), want: liveStartDockerInitFailed},
		{name: "user lookup", output: "unable to find user sensitive-user: no matching entries in passwd file: sensitive-start-output", err: errors.New("command exited"), want: liveStartUserLookupFailed},
		{name: "workdir", output: "chdir to cwd (sensitive-workdir) set in config.json failed: sensitive-start-output", err: errors.New("command exited"), want: liveStartWorkdirFailed},
		{name: "oci mount", output: "OCI runtime create failed: error mounting sensitive-start-output", err: errors.New("command exited"), want: liveStartOCIMountFailed},
		{name: "oci entrypoint permission", output: "OCI runtime create failed: exec sensitive-rig-entrypoint: permission denied: sensitive-start-output", err: errors.New("command exited"), want: liveStartOCIEntrypointDenied},
		{name: "oci entrypoint missing", output: "OCI runtime create failed: exec sensitive-rig-entrypoint: no such file or directory: sensitive-start-output", err: errors.New("command exited"), want: liveStartOCIEntrypointMissing},
		{name: "invalid tmpfs", output: "invalid tmpfs option: sensitive-start-output", err: errors.New("command exited"), want: liveStartInvalidArgument},
		{name: "source cgroup", output: "unable to apply cgroup configuration: sensitive-start-output", err: errors.New("command exited"), want: liveStartResourceFailed},
		{name: "source security", output: "unable to init seccomp: sensitive-start-output", err: errors.New("command exited"), want: liveStartSecurityPolicyFailed},
		{name: "oci resource", output: "OCI runtime create failed: cgroup configuration failed: sensitive-start-output", err: errors.New("command exited"), want: liveStartOCIResourceFailed},
		{name: "pids exhausted", output: "pids: resource temporarily unavailable: sensitive-start-output", err: errors.New("command exited"), want: liveStartPIDsExhausted},
		{name: "pids max", output: "failed to write pids.max: sensitive-start-output", err: errors.New("command exited"), want: liveStartPIDsExhausted},
		{name: "memory exhausted", output: "cannot allocate memory: sensitive-start-output", err: errors.New("command exited"), want: liveStartMemoryExhausted},
		{name: "read only filesystem", output: "error setting rootfs as readonly: sensitive-start-output", err: errors.New("command exited"), want: liveStartReadOnlyFilesystem},
		{name: "operation not permitted", output: "operation not permitted: sensitive-start-output", err: errors.New("command exited"), want: liveStartOperationNotPermitted},
		{name: "invalid argument", output: "invalid argument: sensitive-start-output", err: errors.New("command exited"), want: liveStartInvalidArgument},
		{name: "no space", output: "no space left on device: sensitive-start-output", err: errors.New("command exited"), want: liveStartNoSpace},
		{name: "permission denied", output: "permission denied: sensitive-start-output", err: errors.New("command exited"), want: liveStartPermissionDenied},
		{name: "file missing", output: "no such file or directory: sensitive-start-output", err: errors.New("command exited"), want: liveStartFileMissing},
		{name: "resource failed", output: "resource temporarily unavailable: sensitive-start-output", err: errors.New("command exited"), want: liveStartResourceFailed},
		{name: "shim task", output: "failed to create shim task: sensitive-start-output", err: errors.New("command exited"), want: liveStartShimTaskFailed},
		{name: "create task", output: "failed to create task: sensitive-start-output", err: errors.New("command exited"), want: liveStartCreateTaskFailed},
		{name: "task start", output: "failed to start task: sensitive-start-output", err: errors.New("command exited"), want: liveStartTaskStartFailed},
		{name: "oci runtime start", output: "OCI runtime start failed: sensitive-start-output", err: errors.New("command exited"), want: liveStartTaskStartFailed},
		{name: "oci runtime", output: "OCI runtime create failed: runc create failed: sensitive-start-output", err: errors.New("command exited"), want: liveStartOCIRuntimeFailed},
		{name: "command failed", output: "sensitive-start-output", err: errors.New("command exited"), want: liveStartCommandFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			stderr := []byte(test.output)
			result := runtimeprocess.CommandResult{Stderr: stderr, StderrTruncated: test.truncated}
			reason := classifyLiveStartResult(context.Background(), result, test.err)
			if reason != test.want {
				t.Fatalf("start reason = %q, want %q", reason, test.want)
			}
			if !bytes.Contains(stderr, []byte("sensitive-start-output")) {
				t.Fatal("start classifier cleared output needed by Engine")
			}
			diagnostic := (&liveCreateObserver{latest: liveCreateObservation{createSucceeded: true, inspectReason: liveCreateInspectNone, startReason: reason}}).failureDiagnostic()
			if !strings.Contains(diagnostic, "start_reason="+string(test.want)) {
				t.Fatal("start diagnostic did not report its fixed reason")
			}
			if strings.Contains(diagnostic, "sensitive-start-output") || strings.Contains(diagnostic, "sensitive-typed-error") || strings.Contains(diagnostic, "rig-entrypoint") {
				t.Fatal("start diagnostic exposed command output or error details")
			}
		})
	}
}

func TestLiveStartResultClassifierPrecedence(t *testing.T) {
	for _, test := range []struct {
		name   string
		result runtimeprocess.CommandResult
		err    error
		want   liveStartReason
	}{
		{name: "success wins over sensitive output", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: permission denied: sensitive-start-output")}, want: liveStartNone},
		{name: "truncation before content", result: runtimeprocess.CommandResult{Stderr: []byte("failed to set up container networking: sensitive-start-output"), StderrTruncated: true}, err: errors.New("command exited"), want: liveStartOutputTruncated},
		{name: "source entrypoint before generic permission", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: exec sensitive-rig-entrypoint: permission denied: sensitive-start-output")}, err: errors.New("command exited"), want: liveStartOCIEntrypointDenied},
		{name: "pids before generic cgroup", result: runtimeprocess.CommandResult{Stderr: []byte("cannot set pids limit: unable to apply cgroup configuration: sensitive-start-output")}, err: errors.New("command exited"), want: liveStartPIDsExhausted},
		{name: "security before generic resource", result: runtimeprocess.CommandResult{Stderr: []byte("unable to apply apparmor profile: resource temporarily unavailable: sensitive-start-output")}, err: errors.New("command exited"), want: liveStartSecurityPolicyFailed},
		{name: "source workdir before generic file", result: runtimeprocess.CommandResult{Stderr: []byte("chdir to cwd (sensitive-workdir) set in config.json failed: no such file or directory: sensitive-start-output")}, err: errors.New("command exited"), want: liveStartWorkdirFailed},
		{name: "shim task before generic oci", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: failed to create shim task: sensitive-start-output")}, err: errors.New("command exited"), want: liveStartShimTaskFailed},
		{name: "no cross stream cause synthesis", result: runtimeprocess.CommandResult{Stdout: []byte("OCI runtime create failed: sensitive-start-output"), Stderr: []byte("error mounting sensitive-start-output")}, err: errors.New("command exited"), want: liveStartOCIRuntimeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if reason := classifyLiveStartResult(context.Background(), test.result, test.err); reason != test.want {
				t.Fatalf("start reason = %q, want %q", reason, test.want)
			}
		})
	}
}

func TestLiveStartObserverRequiresPendingGeneratedCreate(t *testing.T) {
	observer := &liveCreateObserver{latest: liveCreateObservation{createSucceeded: true, inspectReason: liveCreateInspectNone}}
	observer.observeStartResult(context.Background(), runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}, errors.New("command exited"))
	if observer.latest.startReason != "" {
		t.Fatal("start result without a generated create was retained")
	}
	observer.awaitingStart = true
	observer.observeStartResult(context.Background(), runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}, errors.New("command exited"))
	if observer.latest.startReason != liveStartCommandFailed || observer.awaitingStart {
		t.Fatal("pending generated create did not retain only a fixed start reason")
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
