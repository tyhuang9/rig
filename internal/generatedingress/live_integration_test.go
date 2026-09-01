package generatedingress

import (
	"bytes"
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	delegate runtimeprocess.CommandRunner
	random   io.Reader
	mu       sync.Mutex
	latest   liveCreateObservation
	pending  livePendingStart
}

type liveCreateObservation struct {
	createSucceeded bool
	inspectReason   liveCreateInspectReason
	hardening       liveCreateHardening
	realization     liveCreateRealization
	startReason     liveStartReason
	createTaskMarks uint32
	stateStatus     liveStateInspectStatus
	stateReason     liveStartReason
	stateMarks      uint32
}

type livePendingStart struct {
	key    [sha256.Size]byte
	tag    [sha256.Size]byte
	active bool
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

type liveStateInspectStatus string

const (
	liveStateInspectNotAttempted liveStateInspectStatus = "not_attempted"
	liveStateInspectNone         liveStateInspectStatus = "none"
	liveStateInspectCancelled    liveStateInspectStatus = "cancelled"
	liveStateInspectCommand      liveStateInspectStatus = "command_failed"
	liveStateInspectDeadline     liveStateInspectStatus = "deadline_exceeded"
	liveStateInspectTruncated    liveStateInspectStatus = "output_truncated"
	liveStateInspectDecode       liveStateInspectStatus = "decode_failed"
	liveStateInspectNonString    liveStateInspectStatus = "non_string"
	liveStateInspectMissing      liveStateInspectStatus = "missing"
	liveStateInspectEmpty        liveStateInspectStatus = "empty"
)

type liveStartReason string

const (
	liveCreateTaskMarkOuter uint32 = 1 << iota
	liveCreateTaskMarkShimStart
	liveCreateTaskMarkRuntimePath
	liveCreateTaskMarkShimLaunch
	liveCreateTaskMarkShimLogPipe
	liveCreateTaskMarkBootstrapWrite
	liveCreateTaskMarkTaskAdd
	liveCreateTaskMarkShimTaskCreate
	liveCreateTaskMarkShimIO
	liveCreateTaskMarkOCICreate
	liveCreateTaskMarkOCIErrorUnavailable
	liveCreateTaskMarkRunCCreate
	liveCreateTaskMarkRunCParent
	liveCreateTaskMarkRunCProcess
	liveCreateTaskMarkExecFIFO
	liveCreateTaskMarkContainerInit
	liveCreateTaskMarkExecFormat
	liveCreateTaskMarkFileExists
	liveCreateTaskMarkConnectionRefused
	liveCreateTaskMarkPIDPipeEOF
)

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
	liveStartOCIErrorUnavailable   liveStartReason = "oci_error_unavailable"
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
		observer.observeStartResult(ctx, request, result, err)
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
	observer.clearPendingStartLocked()
	observer.bindPendingStartLocked(containerID)
	observer.mu.Unlock()
	return result, err
}

func liveContainerStart(args []string) bool {
	return len(args) == 3 && args[0] == "container" && args[1] == "start"
}

func (observer *liveCreateObserver) randomReader() io.Reader {
	if observer.random != nil {
		return observer.random
	}
	return cryptorand.Reader
}

func (observer *liveCreateObserver) bindPendingStartLocked(containerID []byte) bool {
	if _, err := io.ReadFull(observer.randomReader(), observer.pending.key[:]); err != nil {
		observer.clearPendingStartLocked()
		return false
	}
	mac := hmac.New(sha256.New, observer.pending.key[:])
	_, _ = mac.Write(containerID)
	mac.Sum(observer.pending.tag[:0])
	observer.pending.active = true
	return true
}

func (observer *liveCreateObserver) pendingStartMatchesLocked(args []string) bool {
	if !observer.pending.active || !liveContainerStart(args) || !liveContainerIDString(args[2]) {
		return false
	}
	mac := hmac.New(sha256.New, observer.pending.key[:])
	_, _ = mac.Write([]byte(args[2]))
	var tag [sha256.Size]byte
	mac.Sum(tag[:0])
	matches := subtle.ConstantTimeCompare(tag[:], observer.pending.tag[:]) == 1
	clear(tag[:])
	return matches
}

func (observer *liveCreateObserver) clearPendingStartLocked() {
	clear(observer.pending.key[:])
	clear(observer.pending.tag[:])
	observer.pending.active = false
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

func liveContainerIDString(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range value {
		character := value[index]
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

func (observer *liveCreateObserver) observeStartResult(ctx context.Context, request runtimeprocess.CommandRequest, result runtimeprocess.CommandResult, err error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.pendingStartMatchesLocked(request.Args) {
		return
	}
	observer.clearPendingStartLocked()
	reason := classifyLiveStartResult(ctx, result, err)
	observer.latest.startReason = reason
	if liveStartReasonAllowsCreateTaskMarkers(reason) {
		observer.latest.createTaskMarks = liveCreateTaskMarkers(result)
	} else {
		observer.latest.createTaskMarks = 0
	}
	observer.latest.stateStatus = liveStateInspectNotAttempted
	observer.latest.stateReason = liveStartNone
	observer.latest.stateMarks = 0
	if !liveStartReasonAllowsStateInspect(reason) {
		return
	}
	status, stateReason, stateMarks := observer.inspectStartStateError(ctx, request)
	observer.latest.stateStatus = status
	observer.latest.stateReason = stateReason
	observer.latest.stateMarks = stateMarks
}

func liveStartReasonAllowsCreateTaskMarkers(reason liveStartReason) bool {
	switch reason {
	case liveStartNone, liveStartCancelled, liveStartTimeout, liveStartProcessTermination, liveStartOutputTruncated:
		return false
	default:
		return true
	}
}

func liveStartReasonAllowsStateInspect(reason liveStartReason) bool {
	switch reason {
	case liveStartNone, liveStartCancelled, liveStartTimeout, liveStartProcessTermination, liveStartOutputTruncated:
		return false
	default:
		return true
	}
}

func (observer *liveCreateObserver) inspectStartStateError(ctx context.Context, request runtimeprocess.CommandRequest) (liveStateInspectStatus, liveStartReason, uint32) {
	inspectionRequest := runtimeprocess.CommandRequest{
		Executable:  request.Executable,
		Args:        []string{"container", "inspect", "--format", "{{json .State.Error}}", request.Args[2]},
		Directory:   request.Directory,
		Env:         request.Env,
		Timeout:     liveCreateObservationTimeout,
		OutputLimit: liveCreateObservationOutputLimit,
	}
	result, err := observer.delegate.Run(ctx, inspectionRequest)
	clear(inspectionRequest.Args)
	inspectionRequest.Args = nil
	return liveStateObservationFromInspectResult(ctx, &result, err)
}

func liveStateObservationFromInspectResult(ctx context.Context, result *runtimeprocess.CommandResult, err error) (liveStateInspectStatus, liveStartReason, uint32) {
	defer clearLiveCommandResult(result)
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return liveStateInspectCancelled, liveStartNone, 0
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return liveStateInspectDeadline, liveStartNone, 0
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return liveStateInspectTruncated, liveStartNone, 0
	}
	if err != nil {
		return liveStateInspectCommand, liveStartNone, 0
	}
	encoded := bytes.TrimSpace(result.Stdout)
	if len(encoded) == 0 {
		return liveStateInspectMissing, liveStartNone, 0
	}
	if !json.Valid(encoded) {
		return liveStateInspectDecode, liveStartNone, 0
	}
	if encoded[0] != '"' {
		return liveStateInspectNonString, liveStartNone, 0
	}
	if len(encoded) == 2 {
		return liveStateInspectEmpty, liveStartNone, 0
	}
	return liveStateInspectNone, classifyLiveStateError(encoded), liveStateErrorMarkers(encoded)
}

func classifyLiveStateError(encoded []byte) liveStartReason {
	return classifyLiveStartResult(context.Background(), runtimeprocess.CommandResult{Stderr: encoded}, errors.New("state error"))
}

func liveStateErrorMarkers(encoded []byte) uint32 {
	var markers uint32
	if containsAllASCIIInsensitive(encoded, "failed to start shim", "start failed") {
		markers |= liveCreateTaskMarkShimLaunch
	}
	for _, marker := range []struct {
		value   uint32
		pattern string
	}{
		{value: liveCreateTaskMarkShimStart, pattern: "failed to start shim"},
		{value: liveCreateTaskMarkRuntimePath, pattern: "failed to resolve runtime path"},
		{value: liveCreateTaskMarkShimLogPipe, pattern: "open shim log pipe"},
		{value: liveCreateTaskMarkBootstrapWrite, pattern: "failed to write bootstrap.json"},
		{value: liveCreateTaskMarkTaskAdd, pattern: "failed to add task"},
		{value: liveCreateTaskMarkShimTaskCreate, pattern: "failed to create shim task"},
		{value: liveCreateTaskMarkShimIO, pattern: "failed to create init process i/o"},
		{value: liveCreateTaskMarkOCICreate, pattern: "oci runtime create failed"},
		{value: liveCreateTaskMarkOCIErrorUnavailable, pattern: "unable to retrieve oci runtime error"},
		{value: liveCreateTaskMarkRunCCreate, pattern: "runc create failed"},
		{value: liveCreateTaskMarkRunCParent, pattern: "unable to create new parent process"},
		{value: liveCreateTaskMarkRunCProcess, pattern: "unable to start container process"},
		{value: liveCreateTaskMarkExecFIFO, pattern: "unable to setup exec fifo"},
		{value: liveCreateTaskMarkContainerInit, pattern: "error during container init"},
		{value: liveCreateTaskMarkExecFormat, pattern: "exec format error"},
		{value: liveCreateTaskMarkFileExists, pattern: "file exists"},
		{value: liveCreateTaskMarkConnectionRefused, pattern: "connection refused"},
		{value: liveCreateTaskMarkPIDPipeEOF, pattern: "pipe: eof"},
	} {
		if containsASCIIInsensitive(encoded, marker.pattern) {
			markers |= marker.value
		}
	}
	return markers
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
	if result.StdoutTruncated || result.StderrTruncated {
		return liveStartOutputTruncated
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return liveStartProcessTermination
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
	if liveStartOutputContains(result, "unable to retrieve oci runtime error") {
		return liveStartOCIErrorUnavailable
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
	if liveStartOutputContainsAny(result, "failed to start task", "oci runtime start failed") {
		return liveStartTaskStartFailed
	}
	if liveStartOutputContainsAny(result, "oci runtime create failed", "runc create failed") {
		return liveStartOCIRuntimeFailed
	}
	if liveStartOutputContains(result, "failed to create task") {
		return liveStartCreateTaskFailed
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

func liveCreateTaskMarkers(result runtimeprocess.CommandResult) uint32 {
	return liveCreateTaskMarkersInStream(result.Stdout) | liveCreateTaskMarkersInStream(result.Stderr)
}

func liveCreateTaskMarkersInStream(value []byte) uint32 {
	if !containsASCIIInsensitive(value, "failed to create task for container") {
		return 0
	}
	markers := liveCreateTaskMarkOuter
	if containsAllASCIIInsensitive(value, "failed to start shim", "start failed") {
		markers |= liveCreateTaskMarkShimLaunch
	}
	for _, marker := range []struct {
		value   uint32
		pattern string
	}{
		{value: liveCreateTaskMarkShimStart, pattern: "failed to start shim"},
		{value: liveCreateTaskMarkRuntimePath, pattern: "failed to resolve runtime path"},
		{value: liveCreateTaskMarkShimLogPipe, pattern: "open shim log pipe"},
		{value: liveCreateTaskMarkBootstrapWrite, pattern: "failed to write bootstrap.json"},
		{value: liveCreateTaskMarkTaskAdd, pattern: "failed to add task"},
		{value: liveCreateTaskMarkShimTaskCreate, pattern: "failed to create shim task"},
		{value: liveCreateTaskMarkShimIO, pattern: "failed to create init process i/o"},
		{value: liveCreateTaskMarkOCICreate, pattern: "oci runtime create failed"},
		{value: liveCreateTaskMarkOCIErrorUnavailable, pattern: "unable to retrieve oci runtime error"},
		{value: liveCreateTaskMarkRunCCreate, pattern: "runc create failed"},
		{value: liveCreateTaskMarkRunCParent, pattern: "unable to create new parent process"},
		{value: liveCreateTaskMarkRunCProcess, pattern: "unable to start container process"},
		{value: liveCreateTaskMarkExecFIFO, pattern: "unable to setup exec fifo"},
		{value: liveCreateTaskMarkContainerInit, pattern: "error during container init"},
		{value: liveCreateTaskMarkExecFormat, pattern: "exec format error"},
		{value: liveCreateTaskMarkFileExists, pattern: "file exists"},
		{value: liveCreateTaskMarkConnectionRefused, pattern: "connection refused"},
		{value: liveCreateTaskMarkPIDPipeEOF, pattern: "pipe: eof"},
	} {
		if containsASCIIInsensitive(value, marker.pattern) {
			markers |= marker.value
		}
	}
	return markers
}

func liveCreateTaskMarkerNames(markers uint32) string {
	var names []string
	for _, marker := range []struct {
		value uint32
		name  string
	}{
		{value: liveCreateTaskMarkOuter, name: "create_task_outer"},
		{value: liveCreateTaskMarkShimStart, name: "shim_start"},
		{value: liveCreateTaskMarkRuntimePath, name: "runtime_path"},
		{value: liveCreateTaskMarkShimLaunch, name: "shim_launch"},
		{value: liveCreateTaskMarkShimLogPipe, name: "shim_log_pipe"},
		{value: liveCreateTaskMarkBootstrapWrite, name: "bootstrap_write"},
		{value: liveCreateTaskMarkTaskAdd, name: "task_add"},
		{value: liveCreateTaskMarkShimTaskCreate, name: "shim_task_create"},
		{value: liveCreateTaskMarkShimIO, name: "shim_io"},
		{value: liveCreateTaskMarkOCICreate, name: "oci_create"},
		{value: liveCreateTaskMarkOCIErrorUnavailable, name: "oci_error_unavailable"},
		{value: liveCreateTaskMarkRunCCreate, name: "runc_create"},
		{value: liveCreateTaskMarkRunCParent, name: "runc_parent"},
		{value: liveCreateTaskMarkRunCProcess, name: "runc_process"},
		{value: liveCreateTaskMarkExecFIFO, name: "exec_fifo"},
		{value: liveCreateTaskMarkContainerInit, name: "container_init"},
		{value: liveCreateTaskMarkExecFormat, name: "exec_format"},
		{value: liveCreateTaskMarkFileExists, name: "file_exists"},
		{value: liveCreateTaskMarkConnectionRefused, name: "connection_refused"},
		{value: liveCreateTaskMarkPIDPipeEOF, name: "pid_pipe_eof"},
	} {
		if markers&marker.value != 0 {
			names = append(names, marker.name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ",")
}

func (observer *liveCreateObserver) reset() {
	observer.mu.Lock()
	observer.latest = liveCreateObservation{}
	observer.clearPendingStartLocked()
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
	createTaskMarkers := liveCreateTaskMarkerNames(observation.createTaskMarks)
	stateStatus := observation.stateStatus
	if stateStatus == "" {
		stateStatus = liveStateInspectNotAttempted
	}
	stateReason := observation.stateReason
	if stateReason == "" {
		stateReason = liveStartNone
	}
	stateMarkers := liveCreateTaskMarkerNames(observation.stateMarks)
	realization := "unobserved"
	if observation.createSucceeded && reason == liveCreateInspectNone {
		realization = "mounts_realized:" + strconv.FormatBool(observation.realization.MountsRealized) + ",network_realized:" + strconv.FormatBool(observation.realization.NetworkRealized)
	}
	return " generated_runtime_create_observed=" + strconv.FormatBool(observation.createSucceeded) +
		" inspect_reason=" + string(reason) +
		" hardening_mismatches=" + mismatches +
		" realization_state=" + realization +
		" start_reason=" + string(startReason) +
		" create_task_markers=" + createTaskMarkers +
		" state_error_status=" + string(stateStatus) +
		" state_error_reason=" + string(stateReason) +
		" state_error_markers=" + stateMarkers
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
		TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 2,
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

	probeConfig := liveProbeConfig{runner: runner.delegate, executable: docker, directory: working, env: []string{"DOCKER_CONFIG=" + dockerConfig}}
	blueSpec := liveCandidateSpec("blue", appID, planID)
	blueSpec.ImageContentID = buildLiveImage(t, ctx, docker, root, imageTags[0], blueSpec, "blue")
	blue := startLiveCandidate(t, ctx, engine, runner, probeConfig, limits, blueSpec)
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
	green := startLiveCandidate(t, ctx, engine, runner, probeConfig, limits, greenSpec)
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
	const want = " generated_runtime_create_observed=true inspect_reason=none hardening_mismatches=network_mode,tmpfs,security_options realization_state=mounts_realized:false,network_realized:true start_reason=none create_task_markers=none state_error_status=not_attempted state_error_reason=none state_error_markers=none"
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
		createSucceeded: true, inspectReason: liveCreateInspectNone, hardening: liveCreateHardening{NetworkMode: false}, startReason: liveStartCreateTaskFailed, createTaskMarks: liveCreateTaskMarkOuter,
	}}
	observer.reset()
	const want = " generated_runtime_create_observed=false inspect_reason=none hardening_mismatches=none realization_state=unobserved start_reason=none create_task_markers=none state_error_status=not_attempted state_error_reason=none state_error_markers=none"
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
	const want = " generated_runtime_create_observed=true inspect_reason=none hardening_mismatches=none realization_state=mounts_realized:true,network_realized:true start_reason=none create_task_markers=none state_error_status=not_attempted state_error_reason=none state_error_markers=none"
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
		{name: "cancellation before truncation", result: runtimeprocess.CommandResult{StderrTruncated: true}, err: errors.Join(context.Canceled, errors.New("command exited")), want: liveStartCancelled},
		{name: "deadline before truncation", result: runtimeprocess.CommandResult{StderrTruncated: true}, err: errors.Join(context.DeadlineExceeded, errors.New("command exited")), want: liveStartTimeout},
		{name: "truncation before process termination", result: runtimeprocess.CommandResult{StderrTruncated: true}, err: runtimeprocess.ErrTerminationFailed, want: liveStartOutputTruncated},
		{name: "truncation before generic command", result: runtimeprocess.CommandResult{Stderr: []byte("failed to set up container networking: sensitive-start-output"), StderrTruncated: true}, err: errors.New("command exited"), want: liveStartOutputTruncated},
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

func TestLiveOCIErrorUnavailablePrecedesGenericErrnoReasons(t *testing.T) {
	for _, generic := range []string{
		"cannot allocate memory",
		"read-only file system",
		"operation not permitted",
		"invalid argument",
		"no space left on device",
		"permission denied",
		"no such file or directory",
		"resource temporarily unavailable",
	} {
		t.Run(generic, func(t *testing.T) {
			result := runtimeprocess.CommandResult{Stderr: []byte("unable to retrieve OCI runtime error: " + generic + ": sensitive-start-output")}
			if reason := classifyLiveStartResult(context.Background(), result, errors.New("command exited")); reason != liveStartOCIErrorUnavailable {
				t.Fatalf("start reason = %q, want %q", reason, liveStartOCIErrorUnavailable)
			}
		})
	}
}

func TestLiveCreateTaskMarkersAreFixedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause string
		marks uint32
		names string
	}{
		{name: "outer", marks: liveCreateTaskMarkOuter, names: "create_task_outer"},
		{name: "shim start", cause: "failed to start shim", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkShimStart, names: "create_task_outer,shim_start"},
		{name: "runtime path", cause: "failed to resolve runtime path", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkRuntimePath, names: "create_task_outer,runtime_path"},
		{name: "shim launch", cause: "failed to start shim: start failed", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkShimStart | liveCreateTaskMarkShimLaunch, names: "create_task_outer,shim_start,shim_launch"},
		{name: "shim log pipe", cause: "open shim log pipe", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkShimLogPipe, names: "create_task_outer,shim_log_pipe"},
		{name: "bootstrap write", cause: "failed to write bootstrap.json", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkBootstrapWrite, names: "create_task_outer,bootstrap_write"},
		{name: "task add", cause: "failed to add task", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkTaskAdd, names: "create_task_outer,task_add"},
		{name: "shim task create", cause: "failed to create shim task", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkShimTaskCreate, names: "create_task_outer,shim_task_create"},
		{name: "shim io", cause: "failed to create init process i/o", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkShimIO, names: "create_task_outer,shim_io"},
		{name: "oci create", cause: "oci runtime create failed", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkOCICreate, names: "create_task_outer,oci_create"},
		{name: "oci error unavailable", cause: "unable to retrieve oci runtime error", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkOCIErrorUnavailable, names: "create_task_outer,oci_error_unavailable"},
		{name: "runc create", cause: "runc create failed", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkRunCCreate, names: "create_task_outer,runc_create"},
		{name: "runc parent", cause: "unable to create new parent process", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkRunCParent, names: "create_task_outer,runc_parent"},
		{name: "runc process", cause: "unable to start container process", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkRunCProcess, names: "create_task_outer,runc_process"},
		{name: "exec fifo", cause: "unable to setup exec fifo", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkExecFIFO, names: "create_task_outer,exec_fifo"},
		{name: "container init", cause: "error during container init", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkContainerInit, names: "create_task_outer,container_init"},
		{name: "exec format", cause: "exec format error", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkExecFormat, names: "create_task_outer,exec_format"},
		{name: "file exists", cause: "file exists", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkFileExists, names: "create_task_outer,file_exists"},
		{name: "connection refused", cause: "connection refused", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkConnectionRefused, names: "create_task_outer,connection_refused"},
		{name: "pid pipe eof", cause: "pipe: eof", marks: liveCreateTaskMarkOuter | liveCreateTaskMarkPIDPipeEOF, names: "create_task_outer,pid_pipe_eof"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := []byte("failed to create task for container: " + test.cause + ": sensitive-create-task-output")
			marks := liveCreateTaskMarkers(runtimeprocess.CommandResult{Stderr: output})
			if marks != test.marks {
				t.Fatalf("create-task markers = %#x, want %#x", marks, test.marks)
			}
			if names := liveCreateTaskMarkerNames(marks); names != test.names {
				t.Fatalf("create-task marker names = %q, want %q", names, test.names)
			}
			diagnostic := (&liveCreateObserver{latest: liveCreateObservation{createSucceeded: true, inspectReason: liveCreateInspectNone, startReason: liveStartCreateTaskFailed, createTaskMarks: marks}}).failureDiagnostic()
			if !strings.Contains(diagnostic, "create_task_markers="+test.names) {
				t.Fatal("create-task diagnostic did not report its fixed markers")
			}
			if strings.Contains(diagnostic, "sensitive-create-task-output") || (test.cause != "" && strings.Contains(diagnostic, test.cause)) {
				t.Fatal("create-task diagnostic exposed start output")
			}
		})
	}
}

func TestLiveCreateTaskMarkersStayWithinOneStreamAndCanonicalOrder(t *testing.T) {
	outer := []byte("failed to create task for container: failed to resolve runtime path: sensitive-create-task-output")
	separateCause := []byte("runc create failed: sensitive-create-task-output")
	if marks := liveCreateTaskMarkers(runtimeprocess.CommandResult{Stdout: outer, Stderr: separateCause}); marks != liveCreateTaskMarkOuter|liveCreateTaskMarkRuntimePath {
		t.Fatalf("cross-stream markers = %#x, want only stdout markers", marks)
	}
	if marks := liveCreateTaskMarkers(runtimeprocess.CommandResult{
		Stdout: []byte("failed to create task for container: pipe: eof: sensitive-create-task-output"),
		Stderr: []byte("failed to create task for container: runc create failed: sensitive-create-task-output"),
	}); liveCreateTaskMarkerNames(marks) != "create_task_outer,runc_create,pid_pipe_eof" {
		t.Fatalf("canonical marker names = %q", liveCreateTaskMarkerNames(marks))
	}
	if marks := liveCreateTaskMarkers(runtimeprocess.CommandResult{Stderr: separateCause}); marks != 0 {
		t.Fatalf("marker without outer create-task phrase = %#x, want 0", marks)
	}
}

func TestLiveCreateTaskMarkersRequireShimLaunchContext(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   uint32
	}{
		{name: "oci runtime start is not shim launch", output: "failed to create task for container: OCI runtime start failed: sensitive-create-task-output", want: liveCreateTaskMarkOuter},
		{name: "shim source chain is shim launch", output: "failed to create task for container: failed to start shim: start failed: sensitive-create-task-output", want: liveCreateTaskMarkOuter | liveCreateTaskMarkShimStart | liveCreateTaskMarkShimLaunch},
	} {
		t.Run(test.name, func(t *testing.T) {
			if marks := liveCreateTaskMarkers(runtimeprocess.CommandResult{Stderr: []byte(test.output)}); marks != test.want {
				t.Fatalf("create-task markers = %#x, want %#x", marks, test.want)
			}
		})
	}
}

func TestLiveCreateTaskMarkersAreClearedForControlAndTruncation(t *testing.T) {
	runner := &liveStateInspectRunner{}
	observer, request := newLivePendingStartObserver(t, runner)
	observer.observeStartResult(context.Background(), request, runtimeprocess.CommandResult{
		Stderr:          []byte("failed to create task for container: oci runtime create failed: sensitive-create-task-output"),
		StderrTruncated: true,
	}, errors.New("command exited"))
	if observer.latest.startReason != liveStartOutputTruncated || observer.latest.createTaskMarks != 0 || runner.calls != 0 {
		t.Fatal("truncated start retained create-task markers")
	}
	observer, request = newLivePendingStartObserver(t, runner)
	observer.observeStartResult(context.Background(), request, runtimeprocess.CommandResult{Stderr: []byte("failed to create task for container: oci runtime create failed: sensitive-create-task-output")}, nil)
	if observer.latest.startReason != liveStartNone || observer.latest.createTaskMarks != 0 || runner.calls != 0 {
		t.Fatal("successful start retained create-task markers")
	}
}

func TestLiveStartObserverRequiresPendingGeneratedCreate(t *testing.T) {
	runner := &liveStateInspectRunner{}
	observer := &liveCreateObserver{delegate: runner, latest: liveCreateObservation{createSucceeded: true, inspectReason: liveCreateInspectNone}}
	request := runtimeprocess.CommandRequest{Args: []string{"container", "start", strings.Repeat("a", 64)}}
	observer.observeStartResult(context.Background(), request, runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}, errors.New("command exited"))
	if observer.latest.startReason != "" {
		t.Fatal("start result without a generated create was retained")
	}
	observer, request = newLivePendingStartObserver(t, runner)
	runner.result = runtimeprocess.CommandResult{Stdout: []byte(`""`)}
	observer.observeStartResult(context.Background(), request, runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}, errors.New("command exited"))
	if observer.latest.startReason != liveStartCommandFailed || observer.pending.active || runner.calls != 1 {
		t.Fatal("pending generated create did not retain only a fixed start reason")
	}
}

type liveStateInspectRunner struct {
	result        runtimeprocess.CommandResult
	err           error
	calls         int
	inspectShape  bool
	inspectConfig bool
}

func (runner *liveStateInspectRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	runner.calls++
	runner.inspectShape = len(request.Args) == 5 && request.Args[0] == "container" && request.Args[1] == "inspect" && request.Args[2] == "--format" && request.Args[3] == "{{json .State.Error}}" && liveContainerIDString(request.Args[4])
	runner.inspectConfig = request.Executable == "docker" && request.Directory == "test-directory" && len(request.Env) == 1 && request.Env[0] == "SENSITIVE_ENV=value" && request.Timeout <= liveCreateObservationTimeout && request.Timeout > 0 && request.OutputLimit == liveCreateObservationOutputLimit
	return runner.result, runner.err
}

func newLivePendingStartObserver(t *testing.T, runner *liveStateInspectRunner) (*liveCreateObserver, runtimeprocess.CommandRequest) {
	t.Helper()
	observer := &liveCreateObserver{
		delegate: runner,
		random:   bytes.NewReader(bytes.Repeat([]byte{0x5a}, sha256.Size)),
		latest:   liveCreateObservation{createSucceeded: true, inspectReason: liveCreateInspectNone},
	}
	id := strings.Repeat("a", 64)
	observer.mu.Lock()
	if !observer.bindPendingStartLocked([]byte(id)) {
		observer.mu.Unlock()
		t.Fatal("bind pending start")
	}
	observer.mu.Unlock()
	return observer, runtimeprocess.CommandRequest{Executable: "docker", Args: []string{"container", "start", id}, Directory: "test-directory", Env: []string{"SENSITIVE_ENV=value"}}
}

func TestLiveStartObserverRequiresExactBoundIDAndClearsHMAC(t *testing.T) {
	runner := &liveStateInspectRunner{result: runtimeprocess.CommandResult{Stdout: []byte(`""`)}}
	observer, request := newLivePendingStartObserver(t, runner)
	if !observer.pending.active || allZero(observer.pending.key[:]) || allZero(observer.pending.tag[:]) {
		t.Fatal("pending HMAC was not bound")
	}
	for _, args := range [][]string{
		{"container", "start", strings.Repeat("b", 64)},
		{"container", "start", request.Args[2][:63]},
		{"container", "start", "candidate-name"},
		{"container", "start", strings.Repeat("A", 64)},
		{"container", "start"},
	} {
		observer.observeStartResult(context.Background(), runtimeprocess.CommandRequest{Args: args}, runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}, errors.New("command exited"))
		if !observer.pending.active || runner.calls != 0 {
			t.Fatal("mismatched or invalid start consumed pending state")
		}
	}
	start := runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}
	observer.observeStartResult(context.Background(), request, start, errors.New("command exited"))
	if observer.pending.active || !allZero(observer.pending.key[:]) || !allZero(observer.pending.tag[:]) || runner.calls != 1 || !runner.inspectShape || !runner.inspectConfig {
		t.Fatal("exact start did not consume and clear the bound HMAC")
	}
	if !bytes.Contains(start.Stderr, []byte("sensitive-start-output")) {
		t.Fatal("state inspection cleared the original start result")
	}
	observer.observeStartResult(context.Background(), request, start, errors.New("command exited"))
	if runner.calls != 1 {
		t.Fatal("replayed start triggered a second inspection")
	}

	observer, request = newLivePendingStartObserver(t, runner)
	observer.reset()
	if observer.pending.active || !allZero(observer.pending.key[:]) || !allZero(observer.pending.tag[:]) {
		t.Fatal("reset did not clear the pending HMAC")
	}
	observer.observeStartResult(context.Background(), request, start, errors.New("command exited"))
	if runner.calls != 1 {
		t.Fatal("reset pending start triggered inspection")
	}
}

func TestLiveStateInspectClassifiesAndClearsOwnedBuffers(t *testing.T) {
	cancelledCtx, cancelCancelled := context.WithCancel(context.Background())
	cancelCancelled()
	deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, test := range []struct {
		name   string
		ctx    context.Context
		stdout string
		err    error
		trunc  bool
		want   liveStateInspectStatus
	}{
		{name: "command", ctx: context.Background(), stdout: "sensitive-state-output", err: errors.New("inspect failed"), want: liveStateInspectCommand},
		{name: "cancelled before truncation", ctx: cancelledCtx, stdout: "sensitive-state-output", trunc: true, err: errors.New("inspect failed"), want: liveStateInspectCancelled},
		{name: "deadline before truncation", ctx: deadlineCtx, stdout: "sensitive-state-output", trunc: true, err: errors.New("inspect failed"), want: liveStateInspectDeadline},
		{name: "truncated before command", ctx: context.Background(), stdout: "sensitive-state-output", trunc: true, err: errors.New("inspect failed"), want: liveStateInspectTruncated},
		{name: "decode", ctx: context.Background(), stdout: "{sensitive-state-output", want: liveStateInspectDecode},
		{name: "non string", ctx: context.Background(), stdout: "null", want: liveStateInspectNonString},
		{name: "missing", ctx: context.Background(), stdout: " \n\t", want: liveStateInspectMissing},
		{name: "empty", ctx: context.Background(), stdout: `""`, want: liveStateInspectEmpty},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := []byte(test.stdout)
			stderr := []byte("sensitive-inspect-stderr")
			result := runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr, StdoutTruncated: test.trunc}
			status, reason, marks := liveStateObservationFromInspectResult(test.ctx, &result, test.err)
			if status != test.want || reason != liveStartNone || marks != 0 {
				t.Fatalf("state inspection = (%q, %q, %#x), want (%q, none, 0)", status, reason, marks, test.want)
			}
			if result.Stdout != nil || result.Stderr != nil || !allZero(stdout) || !allZero(stderr) {
				t.Fatal("state inspection retained owned buffers")
			}
		})
	}
}

func TestLiveStateInspectAugmentsDiagnosticsWithoutCrossSourceSynthesis(t *testing.T) {
	runner := &liveStateInspectRunner{result: runtimeprocess.CommandResult{Stdout: []byte(`"OCI runtime create failed: runc create failed: sensitive-state-output"`)}}
	observer, request := newLivePendingStartObserver(t, runner)
	observer.observeStartResult(context.Background(), request, runtimeprocess.CommandResult{Stderr: []byte("failed to create task for container: sensitive-start-output")}, errors.New("command exited"))
	if observer.latest.startReason != liveStartCreateTaskFailed || observer.latest.createTaskMarks != liveCreateTaskMarkOuter || observer.latest.stateStatus != liveStateInspectNone || observer.latest.stateReason != liveStartOCIRuntimeFailed || observer.latest.stateMarks != liveCreateTaskMarkOCICreate|liveCreateTaskMarkRunCCreate {
		t.Fatal("state inspection did not retain only fixed augmenting signals")
	}
	diagnostic := observer.failureDiagnostic()
	if !strings.Contains(diagnostic, "create_task_markers=create_task_outer") || !strings.Contains(diagnostic, "state_error_reason=oci_runtime_failed") || !strings.Contains(diagnostic, "state_error_markers=oci_create,runc_create") || strings.Contains(diagnostic, "sensitive-start-output") || strings.Contains(diagnostic, "sensitive-state-output") {
		t.Fatal("state inspection diagnostic was not redacted")
	}

	stdout := []byte(`"OCI runtime create failed: sensitive-state-output"`)
	stderr := []byte("runc create failed: sensitive-inspect-stderr")
	status, reason, marks := liveStateObservationFromInspectResult(context.Background(), &runtimeprocess.CommandResult{Stdout: stdout, Stderr: stderr}, nil)
	if status != liveStateInspectNone || reason != liveStartOCIRuntimeFailed || marks != liveCreateTaskMarkOCICreate || !allZero(stdout) || !allZero(stderr) {
		t.Fatal("state inspection synthesized markers across streams")
	}
}

func TestLiveStateInspectOCIErrorUnavailablePrecedesMissingFile(t *testing.T) {
	stdout := []byte(`"unable to retrieve OCI runtime error: no such file or directory: sensitive-state-output"`)
	status, reason, marks := liveStateObservationFromInspectResult(context.Background(), &runtimeprocess.CommandResult{Stdout: stdout}, nil)
	if status != liveStateInspectNone || reason != liveStartOCIErrorUnavailable || marks != liveCreateTaskMarkOCIErrorUnavailable || !allZero(stdout) {
		t.Fatal("OCI error-unavailable did not precede generic missing-file classification")
	}
}

func TestLivePendingStartEntropyFailureClearsInactiveHMAC(t *testing.T) {
	observer := &liveCreateObserver{random: livePartialEntropyReader{}}
	for index := range observer.pending.key {
		observer.pending.key[index] = 0xff
		observer.pending.tag[index] = 0xff
	}
	observer.pending.active = true
	observer.mu.Lock()
	bound := observer.bindPendingStartLocked([]byte(strings.Repeat("a", 64)))
	observer.mu.Unlock()
	if bound || observer.pending.active || !allZero(observer.pending.key[:]) || !allZero(observer.pending.tag[:]) {
		t.Fatal("entropy failure retained a pending HMAC")
	}
}

func TestLiveStateDiagnosticRedactsFixedCanaries(t *testing.T) {
	canaries := []string{
		strings.Repeat("d", 64),
		"sensitive-container-name",
		"/host/sensitive/path",
		"sensitive-command --flag",
		"SENSITIVE_ENV=value",
		"sensitive-image:tag",
		"sensitive-label=value",
		"raw-error-canary",
	}
	rawState := "OCI runtime create failed: " + strings.Join(canaries, " | ")
	state := []byte(strconv.Quote(rawState))
	runner := &liveStateInspectRunner{result: runtimeprocess.CommandResult{Stdout: state}}
	observer, request := newLivePendingStartObserver(t, runner)
	start := runtimeprocess.CommandResult{Stderr: []byte("failed to create task for container: " + strings.Join(canaries, " | "))}
	observer.observeStartResult(context.Background(), request, start, errors.New("command exited"))
	diagnostic := observer.failureDiagnostic()
	for _, canary := range canaries {
		if strings.Contains(diagnostic, canary) {
			t.Fatal("state diagnostic exposed a sensitive canary")
		}
	}
	if !allZero(state) || !bytes.Contains(start.Stderr, []byte("raw-error-canary")) {
		t.Fatal("observer did not clear only its owned state output")
	}
}

func TestLiveStartObserverResetDuringBlockedStateInspectIsRaceSafe(t *testing.T) {
	runner := &liveBlockingStateInspectRunner{
		liveStateInspectRunner: liveStateInspectRunner{result: runtimeprocess.CommandResult{Stdout: []byte(`""`)}},
		started:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	observer, request := newLivePendingStartObserver(t, &runner.liveStateInspectRunner)
	observer.delegate = runner
	startDone := make(chan struct{})
	go func() {
		observer.observeStartResult(context.Background(), request, runtimeprocess.CommandResult{Stderr: []byte("sensitive-start-output")}, errors.New("command exited"))
		close(startDone)
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("state inspection did not start")
	}
	resetStarted := make(chan struct{})
	resetDone := make(chan struct{})
	go func() {
		close(resetStarted)
		observer.reset()
		close(resetDone)
	}()
	<-resetStarted
	select {
	case <-resetDone:
		t.Fatal("reset completed while state inspection held the observer lock")
	default:
	}
	close(runner.release)
	select {
	case <-startDone:
	case <-time.After(time.Second):
		t.Fatal("start observation did not finish")
	}
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("reset did not finish")
	}
	observer.mu.Lock()
	latest := observer.latest
	observer.mu.Unlock()
	if latest != (liveCreateObservation{}) || observer.pending.active {
		t.Fatal("reset did not win after blocked state inspection")
	}
}

type livePartialEntropyReader struct{}

func (livePartialEntropyReader) Read(values []byte) (int, error) {
	if len(values) == 0 {
		return 0, errors.New("entropy unavailable")
	}
	values[0] = 0xff
	return 1, errors.New("entropy unavailable")
}

type liveBlockingStateInspectRunner struct {
	liveStateInspectRunner
	started chan struct{}
	release chan struct{}
}

func (runner *liveBlockingStateInspectRunner) Run(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	close(runner.started)
	<-runner.release
	return runner.result, runner.err
}

func allZero(values []byte) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
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

func startLiveCandidate(t *testing.T, ctx context.Context, engine *generatedruntime.Engine, observer *liveCreateObserver, probeConfig liveProbeConfig, limits generatedruntime.ContainerLimits, spec generatedruntime.CandidateSpec) generatedruntime.Candidate {
	t.Helper()
	observer.reset()
	candidate, err := engine.CreateInactiveCandidate(ctx, spec)
	if err != nil {
		t.Fatalf("create candidate: %v%s", err, observer.failureDiagnostic())
	}
	if err := engine.StartCandidate(ctx, candidate); err != nil {
		probe := liveProbeOutcome{status: "not_eligible"}
		if observer.startFailureAllowsProbe() {
			probe = liveRunLoggingTupleSplit(ctx, liveProbeConfigForCandidate(probeConfig, spec, candidate, limits))
		}
		t.Fatalf("start candidate: %v%s%s", err, observer.failureDiagnostic(), probe.diagnostic())
	}
	if err := engine.WaitHealthy(ctx, candidate); err != nil {
		t.Fatalf("wait for candidate: %v%s", err, observer.failureDiagnostic())
	}
	return candidate
}

// startFailureAllowsProbe keeps the bisection narrowly diagnostic: cancellation,
// timeouts, truncation, daemon failures, and source-specific causes are already
// actionable and must never create extra containers. Only wrapper/inconclusive
// failures with no State.Error detail may exercise the fixed probe matrix.
func (observer *liveCreateObserver) startFailureAllowsProbe() bool {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.latest.startReason == liveStartCreateTaskFailed &&
		observer.latest.createTaskMarks == liveCreateTaskMarkOuter &&
		observer.latest.stateStatus == liveStateInspectNone &&
		observer.latest.stateReason == liveStartCreateTaskFailed &&
		observer.latest.stateMarks == 0
}

func TestLiveStartFailureAllowsProbeOnlyForExactOuterWrapper(t *testing.T) {
	observer := &liveCreateObserver{latest: liveCreateObservation{
		startReason: liveStartCreateTaskFailed, createTaskMarks: liveCreateTaskMarkOuter,
		stateStatus: liveStateInspectNone, stateReason: liveStartCreateTaskFailed,
	}}
	if !observer.startFailureAllowsProbe() {
		t.Fatal("exact outer wrapper was not eligible")
	}
	for _, mutate := range []func(*liveCreateObservation){
		func(o *liveCreateObservation) { o.createTaskMarks |= liveCreateTaskMarkOCICreate },
		func(o *liveCreateObservation) { o.startReason = liveStartCommandFailed },
		func(o *liveCreateObservation) { o.stateReason = liveStartNone },
		func(o *liveCreateObservation) { o.stateStatus = liveStateInspectTruncated },
	} {
		candidate := observer.latest
		mutate(&candidate)
		observer.latest = candidate
		if observer.startFailureAllowsProbe() {
			t.Fatal("non-exact start detail was eligible")
		}
	}
}

// liveProbeConfigForCandidate copies only the fixed, non-secret runtime shape
// already asserted by this synthetic live fixture. The probe must exercise the
// image and launch contract that actually failed, never a convenient base image.
func liveProbeConfigForCandidate(base liveProbeConfig, spec generatedruntime.CandidateSpec, candidate generatedruntime.Candidate, limits generatedruntime.ContainerLimits) liveProbeConfig {
	base.image = candidate.ImageContentID
	base.command = spec.RunCommand
	base.network = candidate.NetworkName
	base.alias = candidate.NetworkAlias
	base.hostname = candidate.ContainerName
	base.user = "node"
	base.workdir = candidate.WorkingDirectory
	base.internalPort = strconv.FormatUint(uint64(candidate.InternalPort), 10)
	base.healthPath = spec.HealthProbe
	base.memory = strconv.FormatInt(limits.MemoryBytes, 10)
	base.pids = strconv.FormatInt(limits.PIDs, 10)
	base.tmpfs = "/tmp:rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(limits.TmpfsBytes, 10)
	base.cpus = fmt.Sprintf("%.3f", float64(limits.MilliCPUs)/1000)
	base.logSize = limits.LogSize
	base.logFiles = strconv.Itoa(limits.LogFiles)
	return base
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
