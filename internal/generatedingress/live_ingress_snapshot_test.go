package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type liveIngressSnapshotStatus string

const (
	liveIngressSnapshotNotAttempted liveIngressSnapshotStatus = "not_attempted"
	liveIngressSnapshotValid        liveIngressSnapshotStatus = "valid"
	liveIngressSnapshotInvalid      liveIngressSnapshotStatus = "invalid"
	liveIngressSnapshotMissing      liveIngressSnapshotStatus = "missing"
	liveIngressSnapshotError        liveIngressSnapshotStatus = "error"
)

const (
	liveCaddyStartupInspectFormat = `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Config.Labels}},"status":{{json .State.Status}},"running":{{json .State.Running}},"restarting":{{json .State.Restarting}},"oomKilled":{{json .State.OOMKilled}},"dead":{{json .State.Dead}},"exitCode":{{json .State.ExitCode}},"error":{{json .State.Error}}}`
	liveCaddyStartupOutputLimit   = 16 << 10
)

type liveCaddyStartupInspection struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels"`
	Status     string            `json:"status"`
	Running    bool              `json:"running"`
	Restarting bool              `json:"restarting"`
	OOMKilled  bool              `json:"oomKilled"`
	Dead       bool              `json:"dead"`
	ExitCode   int               `json:"exitCode"`
	Error      json.RawMessage   `json:"error"`
}

type liveCaddyStartupDiagnostic struct {
	Status        string
	Exit          string
	StateStatus   string
	OOMKilled     string
	Dead          string
	StateWrappers string
	StateCauses   string
	StdoutMarkers string
	StderrMarkers string
}

func (value liveCaddyStartupDiagnostic) String() string {
	return "caddy_startup_status:" + value.Status +
		",caddy_exit:" + value.Exit +
		",caddy_state_status:" + value.StateStatus +
		",caddy_oom_killed:" + value.OOMKilled +
		",caddy_dead:" + value.Dead +
		",caddy_state_wrappers:" + value.StateWrappers +
		",caddy_state_causes:" + value.StateCauses +
		",caddy_stdout_markers:" + value.StdoutMarkers +
		",caddy_stderr_markers:" + value.StderrMarkers
}

func liveCaddyStartupNotAttempted() liveCaddyStartupDiagnostic {
	return liveCaddyStartupDiagnostic{Status: "not_attempted", Exit: "unobserved", StateStatus: "unobserved", OOMKilled: "unobserved", Dead: "unobserved", StateWrappers: "unobserved", StateCauses: "unobserved", StdoutMarkers: "unobserved", StderrMarkers: "unobserved"}
}

const (
	liveCaddyMismatchIdentity uint64 = 1 << iota
	liveCaddyMismatchUserNetwork
	liveCaddyMismatchEnvironment
	liveCaddyMismatchRootfs
	liveCaddyMismatchCapabilities
	liveCaddyMismatchSecurity
	liveCaddyMismatchBinds
	liveCaddyMismatchResources
	liveCaddyMismatchTmpfs
	liveCaddyMismatchLogging
	liveCaddyMismatchEntrypoint
	liveCaddyMismatchCommand
	liveCaddyMismatchLabels
	liveCaddyMismatchUlimit
	liveCaddyMismatchMount
	liveCaddyMismatchPort
)

func liveIngressFailureDiagnostic(ctx context.Context, manager *Manager) string {
	return liveIngressFailureDiagnosticWithin(ctx, manager, 5*time.Second)
}

func liveIngressRouteFailureDiagnostic(ctx context.Context, manager *Manager, request generatedruntime.RouteSwitchRequest) string {
	const unavailable = "route_snapshot=listen:not_attempted,listen_reason:not_attempted,endpoints:not_attempted"
	if manager == nil || ctx == nil || ctx.Err() != nil || !validSwitchRequest(request) {
		return unavailable
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	endpoints := append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...)
	defer clearLiveRouteEndpoints(endpoints)

	listenStatus, listenReason := "unavailable", "caddy_unavailable"
	if diagnosticCtx.Err() == nil {
		listenStatus, listenReason = liveIngressListenReason(diagnosticCtx, manager)
	}
	endpointStatus := "not_attempted"
	if diagnosticCtx.Err() == nil {
		err := manager.verifyEndpoints(diagnosticCtx, request.AppID, routeRecord{Slot: request.ToSlot, Endpoints: endpoints})
		endpointStatus = liveIngressReadOnlyStatus(err)
	}
	return "route_snapshot=listen:" + listenStatus + ",listen_reason:" + listenReason + ",endpoints:" + endpointStatus
}

func liveIngressListenReason(ctx context.Context, manager *Manager) (string, string) {
	if ctx == nil || manager == nil || ctx.Err() != nil {
		return "unavailable", "caddy_unavailable"
	}
	caddy, found, err := manager.inspectCaddy(ctx)
	defer clearLiveCaddyInspection(&caddy)
	if err != nil {
		return "unavailable", "caddy_unavailable"
	}
	if !found {
		return "unavailable", "caddy_missing"
	}
	if !caddy.Running || !validContainerID(caddy.ID) || strings.TrimPrefix(caddy.Name, "/") != caddyContainerName || caddy.Labels["io.rig.managed"] != "generated-ingress" || caddy.Labels["io.rig.identity-version"] != "v1" || caddy.Labels["io.rig.listener-isolation"] != "v1" {
		return "unavailable", "caddy_unavailable"
	}
	attachment := caddy.Networks[caddyNetworkName]
	if attachment == nil {
		return "drift", "attachment_missing"
	}
	if ctx.Err() != nil {
		return "unavailable", "network_unavailable"
	}
	network, networkFound, networkErr := manager.inspectNetwork(ctx, caddyNetworkName)
	defer clearLiveNetworkInspection(&network)
	if networkErr != nil {
		return "drift", "network_unavailable"
	}
	if !networkFound {
		return "drift", "network_missing"
	}
	expectedIP, networkValid := ingressNetworkIdentity(network)
	if !networkValid {
		expectedIP = ""
		return "drift", "network_invalid"
	}
	if len(network.Containers) == 0 || network.Containers[0].IPv4Address == "" {
		expectedIP = ""
		return "drift", "ip_missing"
	}
	if len(network.Containers) != 1 || normalizeID(network.Containers[0].ID) != normalizeID(caddy.ID) || network.Containers[0].Name != caddyContainerName {
		expectedIP = ""
		return "drift", "ip_invalid"
	}
	parsedPrefix, parseErr := netip.ParsePrefix(network.Containers[0].IPv4Address)
	if parseErr != nil || !parsedPrefix.Addr().Is4() {
		expectedIP = ""
		return "drift", "ip_invalid"
	}
	if parsedPrefix.Addr().String() == expectedIP {
		expectedIP = ""
		return "valid", "ip_expected"
	}
	prefix, prefixErr := netip.ParsePrefix(network.IPAM[0].Subnet)
	expectedIP = ""
	if prefixErr == nil && prefix.Contains(parsedPrefix.Addr()) {
		return "drift", "ip_other_in_subnet"
	}
	return "drift", "ip_outside_subnet"
}

func liveIngressReadOnlyStatus(err error) string {
	if err == nil {
		return "valid"
	}
	if IsCode(err, DiagnosticIngressDrift) {
		return "drift"
	}
	return "unavailable"
}

func clearLiveRouteEndpoints(endpoints []generatedruntime.RouteEndpoint) {
	for index := range endpoints {
		endpoints[index] = generatedruntime.RouteEndpoint{}
	}
	clear(endpoints)
}

func liveIngressFailureDiagnosticWithin(ctx context.Context, manager *Manager, timeout time.Duration) string {
	startup := liveCaddyStartupNotAttempted()
	if manager == nil || ctx == nil || ctx.Err() != nil || timeout <= 0 {
		return "ingress_snapshot=image:not_attempted,volume:not_attempted,network:not_attempted,caddy:not_attempted,caddy_running:unobserved,caddy_mismatches=unobserved," + startup.String()
	}
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	imageStatus := liveIngressSnapshotNotAttempted
	imageID := ""
	if diagnosticCtx.Err() == nil {
		image, imageFound, imageErr := manager.inspectImage(diagnosticCtx)
		imageStatus = liveIngressSnapshotError
		if imageErr == nil && !imageFound {
			imageStatus = liveIngressSnapshotMissing
		} else if imageErr == nil {
			imageStatus = liveIngressSnapshotInvalid
			if image.OS == "linux" && validContainerID(image.ID) && containsDigest(image.RepoDigests, caddyImageDigest) {
				imageStatus = liveIngressSnapshotValid
				imageID = image.ID
			}
		}
		clearLiveImageInspection(&image)
	}

	volumeStatus := liveIngressSnapshotNotAttempted
	if diagnosticCtx.Err() == nil {
		volume, volumeFound, volumeErr := manager.inspectVolume(diagnosticCtx)
		volumeStatus = liveIngressSnapshotError
		if volumeErr == nil && !volumeFound {
			volumeStatus = liveIngressSnapshotMissing
		} else if volumeErr == nil {
			volumeStatus = liveIngressSnapshotInvalid
			if volume.Name == caddyVolumeName && volume.Driver == "local" && volume.Scope == "local" && len(volume.Options) == 0 && volume.Labels["io.rig.managed"] == "generated-ingress" && volume.Labels["io.rig.identity-version"] == "v1" {
				volumeStatus = liveIngressSnapshotValid
			}
		}
		clearLiveVolumeInspection(&volume)
	}

	networkStatus := liveIngressSnapshotNotAttempted
	if diagnosticCtx.Err() == nil {
		networkInspection, networkFound, networkErr := manager.inspectNetwork(diagnosticCtx, caddyNetworkName)
		networkStatus = liveIngressSnapshotError
		if networkErr == nil && !networkFound {
			networkStatus = liveIngressSnapshotMissing
		} else if networkErr == nil {
			networkStatus = liveIngressSnapshotInvalid
			if _, valid := ingressNetworkIdentity(networkInspection); valid {
				networkStatus = liveIngressSnapshotValid
			}
		}
		clearLiveNetworkInspection(&networkInspection)
	}

	caddyStatus := liveIngressSnapshotNotAttempted
	caddyRunning := "unobserved"
	caddyMismatches := "unobserved"
	caddyID := ""
	if diagnosticCtx.Err() == nil {
		caddy, caddyFound, caddyErr := manager.inspectCaddy(diagnosticCtx)
		caddyStatus = liveIngressSnapshotError
		if caddyErr == nil && !caddyFound {
			caddyStatus = liveIngressSnapshotMissing
		} else if caddyErr == nil {
			caddyRunning = strconv.FormatBool(caddy.Running)
			caddyStatus = liveIngressSnapshotInvalid
			managed := caddy.Labels["io.rig.managed"] == "generated-ingress" && caddy.Labels["io.rig.identity-version"] == "v1"
			switch {
			case !managed:
				caddyMismatches = "suppressed"
			case imageStatus != liveIngressSnapshotValid:
				caddyMismatches = "comparison_incomplete"
			default:
				mismatches := liveCaddyMismatchBits(caddy, imageID, manager.options.HostPort)
				caddyMismatches = liveCaddyMismatchNames(mismatches)
				if mismatches == 0 {
					caddyStatus = liveIngressSnapshotValid
					if imageStatus == liveIngressSnapshotValid && volumeStatus == liveIngressSnapshotValid && networkStatus == liveIngressSnapshotValid && !caddy.Running && validContainerID(caddy.ID) {
						caddyID = caddy.ID
					}
				}
			}
		}
		clearLiveCaddyInspection(&caddy)
	}
	if caddyStatus == liveIngressSnapshotValid && caddyRunning == "false" && caddyID != "" && diagnosticCtx.Err() == nil {
		startup = liveCaddyStartupExitDiagnostic(diagnosticCtx, manager, caddyID)
	}
	caddyID = ""
	imageID = ""

	return "ingress_snapshot=image:" + string(imageStatus) +
		",volume:" + string(volumeStatus) +
		",network:" + string(networkStatus) +
		",caddy:" + string(caddyStatus) +
		",caddy_running:" + caddyRunning +
		",caddy_mismatches=" + caddyMismatches +
		"," + startup.String()
}

func liveCaddyStartupExitDiagnostic(ctx context.Context, manager *Manager, caddyID string) liveCaddyStartupDiagnostic {
	diagnostic := liveCaddyStartupNotAttempted()
	if ctx == nil || manager == nil || ctx.Err() != nil || !validContainerID(caddyID) {
		return diagnostic
	}

	stateResult, stateErr := manager.runner.Run(ctx, liveCaddyStartupRequest(ctx, manager, []string{"container", "inspect", "--format", liveCaddyStartupInspectFormat, caddyID}))
	defer clearResult(&stateResult)
	if status := liveCaddyStartupCommandFailure(ctx, stateErr); status != "" {
		diagnostic.Status = status
		return diagnostic
	}
	if stateResult.StdoutTruncated || stateResult.StderrTruncated {
		diagnostic.Status = "state_truncated"
		return diagnostic
	}
	var state liveCaddyStartupInspection
	if json.Unmarshal(stateResult.Stdout, &state) != nil {
		diagnostic.Status = "state_decode_error"
		return diagnostic
	}
	defer clearLiveCaddyStartupInspection(&state)
	diagnostic.Exit = liveCaddyExitBucket(state.ExitCode)
	diagnostic.StateStatus = liveCaddyStateStatus(state.Status)
	diagnostic.OOMKilled = strconv.FormatBool(state.OOMKilled)
	diagnostic.Dead = strconv.FormatBool(state.Dead)
	diagnostic.StateWrappers, diagnostic.StateCauses = liveCaddyStartupStateClassifiers(state.Error)
	if state.ID != caddyID || strings.TrimPrefix(state.Name, "/") != caddyContainerName || state.Labels["io.rig.managed"] != "generated-ingress" || state.Labels["io.rig.identity-version"] != "v1" || state.Labels["io.rig.listener-isolation"] != "v1" {
		diagnostic.Status = "ownership_changed"
		diagnostic.StdoutMarkers = "unobserved"
		diagnostic.StderrMarkers = "unobserved"
		return diagnostic
	}
	if state.Running || state.Restarting {
		diagnostic.Status = "state_changed"
		diagnostic.StdoutMarkers = "unobserved"
		diagnostic.StderrMarkers = "unobserved"
		return diagnostic
	}
	if status := liveCaddyStartupCommandFailure(ctx, nil); status != "" {
		diagnostic.Status = status
		return diagnostic
	}

	logsResult, logsErr := manager.runner.Run(ctx, liveCaddyStartupRequest(ctx, manager, []string{"container", "logs", "--tail", "64", caddyID}))
	defer clearResult(&logsResult)
	if status := liveCaddyStartupCommandFailure(ctx, logsErr); status != "" {
		diagnostic.Status = status
		return diagnostic
	}
	if logsResult.StdoutTruncated || logsResult.StderrTruncated {
		diagnostic.Status = "logs_truncated"
		return diagnostic
	}
	diagnostic.Status = "ok"
	diagnostic.StdoutMarkers = liveCaddyStartupMarkers(logsResult.Stdout)
	diagnostic.StderrMarkers = liveCaddyStartupMarkers(logsResult.Stderr)
	return diagnostic
}

func liveCaddyStartupCommandFailure(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return "termination_failed"
	}
	if err != nil {
		return "command_error"
	}
	return ""
}

func liveCaddyStartupRequest(ctx context.Context, manager *Manager, args []string) runtimeprocess.CommandRequest {
	timeout := manager.options.CommandTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	return runtimeprocess.CommandRequest{
		Executable: manager.options.DockerExecutable,
		Args:       append([]string(nil), args...), Directory: manager.options.WorkingDirectory,
		Env: append([]string(nil), manager.dockerEnv...), Timeout: timeout, OutputLimit: liveCaddyStartupOutputLimit,
	}
}

func liveCaddyExitBucket(exitCode int) string {
	switch exitCode {
	case 0:
		return "success"
	case 1:
		return "general_failure"
	case 2:
		return "shell_misuse"
	case 126:
		return "cannot_execute"
	case 127:
		return "command_not_found"
	case 128:
		return "invalid_exit"
	case 137:
		return "sigkill"
	case 139:
		return "segfault"
	case 143:
		return "terminated"
	case 255:
		return "runtime_255"
	default:
		if exitCode >= 129 && exitCode <= 192 {
			return "signal_other"
		}
		return "other_nonzero"
	}
}

func liveCaddyStateStatus(value string) string {
	switch value {
	case "created", "running", "paused", "restarting", "removing", "exited", "dead":
		return value
	default:
		return "unknown"
	}
}

func liveCaddyStartupMarkers(value []byte) string {
	if len(bytes.TrimSpace(value)) == 0 {
		return "none"
	}
	lower := bytes.ToLower(value)
	defer clear(lower)
	markers := []struct {
		name     string
		patterns [][]byte
	}{
		{"unknown_command", [][]byte{[]byte("unknown command")}},
		{"address_in_use", [][]byte{[]byte("address already in use")}},
		{"config_missing", [][]byte{[]byte("no such file or directory"), []byte("config file does not exist")}},
		{"autosave_failed", [][]byte{[]byte("unable to autosave"), []byte("autosave failed")}},
		{"read_only", [][]byte{[]byte("read-only file system"), []byte("read only file system")}},
		{"permission_denied", [][]byte{[]byte("permission denied")}},
		{"load_failed", [][]byte{[]byte("loading initial config"), []byte("loading new config"), []byte("adapting config"), []byte("cannot unmarshal"), []byte("invalid character")}},
	}
	found := make([]bool, len(markers))
	for _, line := range bytes.Split(lower, []byte{'\n'}) {
		for index, marker := range markers {
			for _, pattern := range marker.patterns {
				if bytes.Contains(line, pattern) {
					found[index] = true
					break
				}
			}
		}
	}
	names := make([]string, 0, len(markers))
	for index, marker := range markers {
		if found[index] {
			names = append(names, marker.name)
		}
	}
	if len(names) == 0 {
		return "other"
	}
	return strings.Join(names, "+")
}

func liveCaddyStartupStateClassifiers(value json.RawMessage) (string, string) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return "none", "none"
	}
	lower := bytes.ToLower(trimmed)
	defer clear(lower)
	wrapperPatterns := []struct {
		name    string
		pattern []byte
	}{
		{"create_task", []byte("failed to create task")},
		{"shim_task", []byte("failed to create shim task")},
		{"oci_create", []byte("oci runtime create failed")},
		{"oci_start", []byte("oci runtime start failed")},
		{"runc_create", []byte("runc create failed")},
		{"start_process", []byte("unable to start container process")},
		{"container_init", []byte("error during container init")},
		{"exec", []byte("exec:")},
	}
	wrapperFound := make([]bool, len(wrapperPatterns))
	causeFound := make(map[string]bool, 8)
	for _, line := range bytes.Split(lower, []byte{'\n'}) {
		for index, wrapper := range wrapperPatterns {
			if bytes.Contains(line, wrapper.pattern) {
				wrapperFound[index] = true
			}
		}
		causeFound[liveCaddyStartupStateCause(line)] = true
	}
	wrappers := make([]string, 0, len(wrapperPatterns))
	for index, wrapper := range wrapperPatterns {
		if wrapperFound[index] {
			wrappers = append(wrappers, wrapper.name)
		}
	}
	if len(wrappers) == 0 {
		wrappers = append(wrappers, "none")
	}
	causeOrder := []string{"docker_init_missing", "docker_init_failed", "user_lookup", "workdir", "exec_format", "executable_missing", "entrypoint_denied", "mount_failed", "network_failed", "security_policy", "cgroup_failed", "pids_exhausted", "resource_exhausted", "no_space", "read_only", "operation_not_permitted", "permission_denied", "invalid_argument", "resource_unavailable", "other"}
	causes := make([]string, 0, len(causeFound))
	for _, cause := range causeOrder {
		if causeFound[cause] {
			causes = append(causes, cause)
		}
	}
	return strings.Join(wrappers, "+"), strings.Join(causes, "+")
}

func liveCaddyStartupStateCause(line []byte) string {
	contains := func(pattern string) bool { return bytes.Contains(line, []byte(pattern)) }
	switch {
	case contains("docker-init") && (contains("no such file") || contains("not found")):
		return "docker_init_missing"
	case contains("docker-init"):
		return "docker_init_failed"
	case contains("unable to setup user") || contains("unable to find user") || contains("no matching entries in passwd") || contains("unable to find group"):
		return "user_lookup"
	case contains("chdir to cwd") || contains("working directory") || contains("current working directory"):
		return "workdir"
	case contains("exec format error"):
		return "exec_format"
	case contains("executable file not found") || contains("executable not found"):
		return "executable_missing"
	case contains("exec:") && contains("permission denied"):
		return "entrypoint_denied"
	case (contains("mount") || contains("tmpfs")) && (contains("failed") || contains("error") || contains("invalid")):
		return "mount_failed"
	case contains("failed to set up container networking") || contains("failed programming external connectivity") || contains("port is already allocated") || contains("network namespace"):
		return "network_failed"
	case contains("apparmor") || contains("seccomp") || contains("selinux") || contains("security policy"):
		return "security_policy"
	case contains("cgroup"):
		return "cgroup_failed"
	case contains("pids limit") || contains("too many processes"):
		return "pids_exhausted"
	case contains("out of memory") || contains("cannot allocate memory") || contains("oom"):
		return "resource_exhausted"
	case contains("no space left on device"):
		return "no_space"
	case contains("read-only file system") || contains("read only file system"):
		return "read_only"
	case contains("operation not permitted"):
		return "operation_not_permitted"
	case contains("permission denied"):
		return "permission_denied"
	case contains("invalid argument"):
		return "invalid_argument"
	case contains("resource temporarily unavailable"):
		return "resource_unavailable"
	default:
		return "other"
	}
}

func clearLiveCaddyStartupInspection(value *liveCaddyStartupInspection) {
	if value == nil {
		return
	}
	clear(value.Labels)
	clear(value.Error)
	*value = liveCaddyStartupInspection{}
}

func liveCaddyMismatchBits(value caddyInspection, imageID string, hostPort uint16) uint64 {
	var mismatches uint64
	if imageID == "" || normalizeID(value.Image) != normalizeID(imageID) || strings.TrimPrefix(value.Name, "/") != caddyContainerName {
		mismatches |= liveCaddyMismatchIdentity
	}
	if value.User != "1000:1000" || value.Hostname != caddyContainerName || value.NetworkMode != caddyNetworkName {
		mismatches |= liveCaddyMismatchUserNetwork
	}
	if !containsString(value.Env, "XDG_CONFIG_HOME=/config") || !containsString(value.Env, "XDG_DATA_HOME=/data") {
		mismatches |= liveCaddyMismatchEnvironment
	}
	if !value.ReadOnly || value.Privileged {
		mismatches |= liveCaddyMismatchRootfs
	}
	if len(value.CapAdd) != 0 || !exactFoldSet(value.CapDrop, "ALL") {
		mismatches |= liveCaddyMismatchCapabilities
	}
	if !onlyNoNewPrivileges(value.SecurityOpt) {
		mismatches |= liveCaddyMismatchSecurity
	}
	if len(value.Binds) != 0 {
		mismatches |= liveCaddyMismatchBinds
	}
	if value.Memory != 268435456 || value.MemorySwap != 268435456 || value.NanoCPUs != 1_000_000_000 || value.PIDsLimit != 128 {
		mismatches |= liveCaddyMismatchResources
	}
	if len(value.Tmpfs) != 1 || value.Tmpfs["/data"] != "rw,noexec,nosuid,nodev,size=67108864" {
		mismatches |= liveCaddyMismatchTmpfs
	}
	if value.LogType != "local" || value.LogConfig["max-size"] != "10m" || value.LogConfig["max-file"] != "3" || value.Restart != "unless-stopped" {
		mismatches |= liveCaddyMismatchLogging
	}
	if len(value.Entrypoint) != 1 || value.Entrypoint[0] != caddyExecutable {
		mismatches |= liveCaddyMismatchEntrypoint
	}
	if len(value.Cmd) != 3 || value.Cmd[0] != "run" || value.Cmd[1] != "--config" || value.Cmd[2] != "/config/active.json" {
		mismatches |= liveCaddyMismatchCommand
	}
	if value.Labels["io.rig.managed"] != "generated-ingress" || value.Labels["io.rig.identity-version"] != "v1" || value.Labels["io.rig.listener-isolation"] != "v1" {
		mismatches |= liveCaddyMismatchLabels
	}
	if len(value.Ulimits) != 1 || value.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) {
		mismatches |= liveCaddyMismatchUlimit
	}
	mountOK := false
	for _, mount := range value.Mounts {
		if mount.Type == "volume" && mount.Name == caddyVolumeName && mount.Destination == "/config" && mount.RW {
			mountOK = true
		} else if mount.Type != "tmpfs" || mount.Destination != "/data" {
			mismatches |= liveCaddyMismatchMount
		}
	}
	if !mountOK {
		mismatches |= liveCaddyMismatchMount
	}
	binding := value.PortBindings["8080/tcp"]
	if len(value.PortBindings) != 1 || len(binding) != 1 || len(binding[0]) != 2 || binding[0]["HostIp"] != "127.0.0.1" || binding[0]["HostPort"] != strconv.FormatUint(uint64(hostPort), 10) {
		mismatches |= liveCaddyMismatchPort
	}
	return mismatches
}

func liveCaddyMismatchNames(bits uint64) string {
	if bits == 0 {
		return "none"
	}
	names := make([]string, 0, 16)
	for _, mismatch := range []struct {
		bit  uint64
		name string
	}{
		{liveCaddyMismatchIdentity, "identity"},
		{liveCaddyMismatchUserNetwork, "user_network"},
		{liveCaddyMismatchEnvironment, "environment"},
		{liveCaddyMismatchRootfs, "rootfs"},
		{liveCaddyMismatchCapabilities, "capabilities"},
		{liveCaddyMismatchSecurity, "security"},
		{liveCaddyMismatchBinds, "binds"},
		{liveCaddyMismatchResources, "resources"},
		{liveCaddyMismatchTmpfs, "tmpfs"},
		{liveCaddyMismatchLogging, "logging"},
		{liveCaddyMismatchEntrypoint, "entrypoint"},
		{liveCaddyMismatchCommand, "command"},
		{liveCaddyMismatchLabels, "labels"},
		{liveCaddyMismatchUlimit, "ulimit"},
		{liveCaddyMismatchMount, "mount"},
		{liveCaddyMismatchPort, "port"},
	} {
		if bits&mismatch.bit != 0 {
			names = append(names, mismatch.name)
		}
	}
	return strings.Join(names, "+")
}

func clearLiveImageInspection(value *imageInspection) {
	if value == nil {
		return
	}
	clear(value.RepoDigests)
	*value = imageInspection{}
}

func clearLiveVolumeInspection(value *volumeInspection) {
	if value == nil {
		return
	}
	clear(value.Options)
	clear(value.Labels)
	*value = volumeInspection{}
}

func clearLiveNetworkInspection(value *networkInspection) {
	if value == nil {
		return
	}
	clear(value.Options)
	clear(value.Labels)
	clear(value.IPAM)
	for index := range value.Containers {
		value.Containers[index] = networkContainerInspection{}
	}
	clear(value.Containers)
	*value = networkInspection{}
}

func clearLiveCaddyInspection(value *caddyInspection) {
	if value == nil {
		return
	}
	clear(value.Labels)
	clear(value.Env)
	clear(value.Entrypoint)
	clear(value.Cmd)
	clear(value.CapAdd)
	clear(value.CapDrop)
	clear(value.SecurityOpt)
	clear(value.Binds)
	clear(value.Mounts)
	clear(value.Tmpfs)
	clear(value.LogConfig)
	clear(value.Ulimits)
	for _, bindings := range value.PortBindings {
		for _, binding := range bindings {
			clear(binding)
		}
		clear(bindings)
	}
	clear(value.PortBindings)
	for _, attachment := range value.Networks {
		if attachment != nil {
			clear(attachment.Aliases)
			*attachment = networkAttachment{}
		}
	}
	clear(value.Networks)
	*value = caddyInspection{}
}

type liveSnapshotRunnerFunc func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)

func (run liveSnapshotRunnerFunc) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return run(ctx, request)
}

func TestLiveIngressFailureDiagnosticUsesOnlyFixedStates(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	expected := "ingress_snapshot=image:valid,volume:valid,network:valid,caddy:valid,caddy_running:true,caddy_mismatches=none," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnostic(context.Background(), manager); diagnostic != expected {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
	for _, canary := range []string{runner.appID, runner.endpoint.ContainerID, caddyImage, caddyContainerName} {
		if strings.Contains(expected, canary) {
			t.Fatal("diagnostic contained a raw identity canary")
		}
	}
}

func TestLiveCaddyMismatchBitsMatchValidator(t *testing.T) {
	_, runner := newManagerFixture(t, false)
	imageID := "sha256:" + strings.Repeat("a", 64)
	valid := runner.caddyInspection()
	if bits := liveCaddyMismatchBits(valid, imageID, 8080); bits != 0 || !validCaddyInspection(valid, imageID, 8080) {
		t.Fatalf("valid Caddy inspection mismatch bits = %d", bits)
	}
	tests := []struct {
		name   string
		bit    uint64
		mutate func(*caddyInspection)
	}{
		{"identity", liveCaddyMismatchIdentity, func(value *caddyInspection) { value.Image = "sha256:" + strings.Repeat("c", 64) }},
		{"user network", liveCaddyMismatchUserNetwork, func(value *caddyInspection) { value.User = "0:0" }},
		{"environment", liveCaddyMismatchEnvironment, func(value *caddyInspection) { value.Env = nil }},
		{"rootfs", liveCaddyMismatchRootfs, func(value *caddyInspection) { value.ReadOnly = false }},
		{"capabilities", liveCaddyMismatchCapabilities, func(value *caddyInspection) { value.CapAdd = []string{"NET_ADMIN"} }},
		{"security", liveCaddyMismatchSecurity, func(value *caddyInspection) { value.SecurityOpt = []string{"no-new-privileges=false"} }},
		{"binds", liveCaddyMismatchBinds, func(value *caddyInspection) { value.Binds = []string{"sensitive-host-path"} }},
		{"resources", liveCaddyMismatchResources, func(value *caddyInspection) { value.Memory = 1 }},
		{"tmpfs", liveCaddyMismatchTmpfs, func(value *caddyInspection) { value.Tmpfs["/data"] = "rw" }},
		{"logging", liveCaddyMismatchLogging, func(value *caddyInspection) { value.LogType = "json-file" }},
		{"entrypoint", liveCaddyMismatchEntrypoint, func(value *caddyInspection) { value.Entrypoint = []string{"caddy"} }},
		{"command", liveCaddyMismatchCommand, func(value *caddyInspection) { value.Cmd = nil }},
		{"labels", liveCaddyMismatchLabels, func(value *caddyInspection) { value.Labels = nil }},
		{"ulimit", liveCaddyMismatchUlimit, func(value *caddyInspection) { value.Ulimits = nil }},
		{"mount", liveCaddyMismatchMount, func(value *caddyInspection) { value.Mounts = nil }},
		{"port", liveCaddyMismatchPort, func(value *caddyInspection) { value.PortBindings = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := runner.caddyInspection()
			test.mutate(&candidate)
			bits := liveCaddyMismatchBits(candidate, imageID, 8080)
			if bits != test.bit {
				t.Fatalf("mismatch bits = %d, want %d (%s)", bits, test.bit, liveCaddyMismatchNames(bits))
			}
			if validCaddyInspection(candidate, imageID, 8080) {
				t.Fatal("validator accepted a diagnosed mismatch")
			}
		})
	}
}

func TestLiveIngressFailureDiagnosticClassifiesMissingErrorsAndTotalTimeout(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	manager.runner = liveSnapshotRunnerFunc(func(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		kind := "object"
		if len(request.Args) > 0 {
			kind = request.Args[0]
		}
		return runtimeprocess.CommandResult{Stderr: []byte("No such " + kind + ": sensitive-canary")}, errors.New("sensitive-canary")
	})
	missing := "ingress_snapshot=image:missing,volume:missing,network:missing,caddy:missing,caddy_running:unobserved,caddy_mismatches=unobserved," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != missing {
		t.Fatalf("missing diagnostic = %q", diagnostic)
	}

	manager.runner = liveSnapshotRunnerFunc(func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{}, errors.New("sensitive-canary")
	})
	failed := "ingress_snapshot=image:error,volume:error,network:error,caddy:error,caddy_running:unobserved,caddy_mismatches=unobserved," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != failed {
		t.Fatalf("error diagnostic = %q", diagnostic)
	}

	manager.runner = liveSnapshotRunnerFunc(func(ctx context.Context, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		<-ctx.Done()
		return runtimeprocess.CommandResult{}, ctx.Err()
	})
	started := time.Now()
	timedOut := "ingress_snapshot=image:error,volume:not_attempted,network:not_attempted,caddy:not_attempted,caddy_running:unobserved,caddy_mismatches=unobserved," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, 20*time.Millisecond); diagnostic != timedOut {
		t.Fatalf("timeout diagnostic = %q", diagnostic)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("diagnostic exceeded total timeout: %v", elapsed)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	notAttempted := "ingress_snapshot=image:not_attempted,volume:not_attempted,network:not_attempted,caddy:not_attempted,caddy_running:unobserved,caddy_mismatches=unobserved," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnosticWithin(cancelled, manager, time.Second); diagnostic != notAttempted {
		t.Fatalf("cancelled diagnostic = %q", diagnostic)
	}
}

func TestLiveIngressFailureDiagnosticSuppressesForeignAndIncompleteComparisons(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	delegate := manager.runner
	foreign := runner.caddyInspection()
	foreign.Labels = map[string]string{"io.rig.managed": "foreign", "sensitive-canary": "sensitive-canary"}
	foreign.Env = append(foreign.Env, "SENSITIVE_CANARY=sensitive-canary")
	manager.runner = liveSnapshotRunnerFunc(func(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if len(request.Args) > 0 && request.Args[0] == "container" {
			return jsonResult(foreign), nil
		}
		return delegate.Run(ctx, request)
	})
	suppressed := "ingress_snapshot=image:valid,volume:valid,network:valid,caddy:invalid,caddy_running:true,caddy_mismatches=suppressed," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != suppressed || strings.Contains(diagnostic, "sensitive-canary") {
		t.Fatalf("foreign diagnostic = %q", diagnostic)
	}
	clearLiveCaddyInspection(&foreign)

	manager, _ = newManagerFixture(t, false)
	delegate = manager.runner
	manager.runner = liveSnapshotRunnerFunc(func(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if len(request.Args) > 0 && request.Args[0] == "image" {
			return jsonResult(imageInspection{ID: "sensitive-canary", OS: "linux", RepoDigests: []string{"sensitive-canary"}}), nil
		}
		return delegate.Run(ctx, request)
	})
	incomplete := "ingress_snapshot=image:invalid,volume:valid,network:valid,caddy:invalid,caddy_running:true,caddy_mismatches=comparison_incomplete," + liveCaddyStartupNotAttempted().String()
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != incomplete || strings.Contains(diagnostic, "sensitive-canary") {
		t.Fatalf("incomplete diagnostic = %q", diagnostic)
	}
}

func TestLiveIngressInspectionClearHelpersZeroDecodedState(t *testing.T) {
	_, runner := newManagerFixture(t, false)
	image := imageInspection{ID: "sensitive", OS: "linux", RepoDigests: []string{"sensitive"}}
	volume := volumeInspection{Name: "sensitive", Options: map[string]string{"sensitive": "sensitive"}, Labels: map[string]string{"sensitive": "sensitive"}}
	network := networkInspection{Name: "sensitive", Options: map[string]string{"sensitive": "sensitive"}, IPAM: []networkIPAM{{Subnet: "sensitive"}}, Labels: map[string]string{"sensitive": "sensitive"}, Containers: []networkContainerInspection{{ID: "sensitive", Name: "sensitive", IPv4Address: "sensitive"}}}
	caddy := runner.caddyInspection()
	clearLiveImageInspection(&image)
	clearLiveVolumeInspection(&volume)
	clearLiveNetworkInspection(&network)
	clearLiveCaddyInspection(&caddy)
	if !reflect.DeepEqual(image, imageInspection{}) || !reflect.DeepEqual(volume, volumeInspection{}) || !reflect.DeepEqual(network, networkInspection{}) || !reflect.DeepEqual(caddy, caddyInspection{}) {
		t.Fatal("decoded inspection state was not cleared")
	}
}

type liveCaddyStartupRunner struct {
	delegate      runtimeprocess.CommandRunner
	id            string
	state         liveCaddyStartupInspection
	stateResult   *runtimeprocess.CommandResult
	stateErr      error
	logsResult    runtimeprocess.CommandResult
	logsErr       error
	requests      []runtimeprocess.CommandRequest
	stateRaw      []byte
	logsStdoutRaw []byte
	logsStderrRaw []byte
}

func (runner *liveCaddyStartupRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if reflect.DeepEqual(request.Args, []string{"container", "inspect", "--format", liveCaddyStartupInspectFormat, runner.id}) {
		runner.requests = append(runner.requests, request)
		if runner.stateResult != nil {
			result := *runner.stateResult
			runner.stateRaw = result.Stdout
			return result, runner.stateErr
		}
		result := jsonResult(runner.state)
		runner.stateRaw = result.Stdout
		return result, runner.stateErr
	}
	if reflect.DeepEqual(request.Args, []string{"container", "logs", "--tail", "64", runner.id}) {
		runner.requests = append(runner.requests, request)
		runner.logsStdoutRaw = runner.logsResult.Stdout
		runner.logsStderrRaw = runner.logsResult.Stderr
		return runner.logsResult, runner.logsErr
	}
	return runner.delegate.Run(ctx, request)
}

func newLiveCaddyStartupRunner(t *testing.T) (*Manager, *liveCaddyStartupRunner) {
	t.Helper()
	manager, fixture := newManagerFixture(t, false)
	fixture.stopped = true
	caddy := fixture.caddyInspection()
	runner := &liveCaddyStartupRunner{
		delegate: manager.runner,
		id:       caddy.ID,
		state: liveCaddyStartupInspection{
			ID: caddy.ID, Name: caddy.Name, Labels: map[string]string{
				"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.listener-isolation": "v1",
			}, ExitCode: 1, Error: json.RawMessage(`""`),
		},
	}
	manager.runner = runner
	return manager, runner
}

func TestLiveCaddyStartupDiagnosticPinsIdentityAndClassifiesStreams(t *testing.T) {
	manager, runner := newLiveCaddyStartupRunner(t)
	runner.logsResult = runtimeprocess.CommandResult{
		Stdout: []byte("serving initial configuration\n"),
		Stderr: []byte("unable to autosave config: permission denied\n"),
	}
	diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second)
	want := "ingress_snapshot=image:valid,volume:valid,network:valid,caddy:valid,caddy_running:false,caddy_mismatches=none," +
		"caddy_startup_status:ok,caddy_exit:general_failure,caddy_state_status:unknown,caddy_oom_killed:false,caddy_dead:false,caddy_state_wrappers:none,caddy_state_causes:none,caddy_stdout_markers:other,caddy_stderr_markers:autosave_failed+permission_denied"
	if diagnostic != want || strings.Contains(diagnostic, runner.id) || strings.Contains(diagnostic, caddyContainerName) {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("startup requests = %d", len(runner.requests))
	}
	for _, request := range runner.requests {
		if request.Executable != manager.options.DockerExecutable || request.Directory != manager.options.WorkingDirectory || !reflect.DeepEqual(request.Env, manager.dockerEnv) || request.OutputLimit != liveCaddyStartupOutputLimit || request.Timeout <= 0 || request.Timeout > time.Second {
			t.Fatalf("startup request configuration = %#v", request)
		}
		if request.Args[len(request.Args)-1] != runner.id || containsArgument(request.Args, caddyContainerName) {
			t.Fatalf("startup request did not pin the validated ID: %v", request.Args)
		}
	}
	if !allZero(runner.stateRaw) || !allZero(runner.logsStdoutRaw) || !allZero(runner.logsStderrRaw) {
		t.Fatal("startup diagnostic did not clear owned command buffers")
	}
}

func TestLiveCaddyStartupDiagnosticReattestsBeforeLogs(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*liveCaddyStartupInspection)
		want   string
	}{
		{"id changed", func(state *liveCaddyStartupInspection) { state.ID = strings.Repeat("e", 64) }, "ownership_changed"},
		{"name changed", func(state *liveCaddyStartupInspection) { state.Name = "/foreign" }, "ownership_changed"},
		{"ownership changed", func(state *liveCaddyStartupInspection) { state.Labels["io.rig.managed"] = "foreign" }, "ownership_changed"},
		{"running", func(state *liveCaddyStartupInspection) { state.Running = true }, "state_changed"},
		{"restarting", func(state *liveCaddyStartupInspection) { state.Restarting = true }, "state_changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runner := newLiveCaddyStartupRunner(t)
			test.mutate(&runner.state)
			diagnostic := liveCaddyStartupExitDiagnostic(context.Background(), manager, runner.id)
			if diagnostic.Status != test.want || len(runner.requests) != 1 {
				t.Fatalf("diagnostic = %#v, requests = %v", diagnostic, runner.requests)
			}
		})
	}
}

func TestLiveCaddyStartupDiagnosticFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*liveCaddyStartupRunner)
		ctx   func() context.Context
		want  string
	}{
		{"state command", func(runner *liveCaddyStartupRunner) { runner.stateErr = errors.New("sensitive-canary") }, context.Background, "command_error"},
		{"state termination", func(runner *liveCaddyStartupRunner) { runner.stateErr = runtimeprocess.ErrTerminationFailed }, context.Background, "termination_failed"},
		{"state truncated", func(runner *liveCaddyStartupRunner) {
			runner.stateResult = &runtimeprocess.CommandResult{Stdout: []byte("sensitive-canary"), StdoutTruncated: true}
		}, context.Background, "state_truncated"},
		{"state decode", func(runner *liveCaddyStartupRunner) {
			runner.stateResult = &runtimeprocess.CommandResult{Stdout: []byte("sensitive-canary")}
		}, context.Background, "state_decode_error"},
		{"logs command", func(runner *liveCaddyStartupRunner) { runner.logsErr = errors.New("sensitive-canary") }, context.Background, "command_error"},
		{"logs truncated", func(runner *liveCaddyStartupRunner) {
			runner.logsResult = runtimeprocess.CommandResult{Stderr: []byte("sensitive-canary"), StderrTruncated: true}
		}, context.Background, "logs_truncated"},
		{"cancelled", func(runner *liveCaddyStartupRunner) { runner.stateErr = context.Canceled }, context.Background, "cancelled"},
		{"deadline", func(runner *liveCaddyStartupRunner) { runner.stateErr = context.DeadlineExceeded }, context.Background, "timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runner := newLiveCaddyStartupRunner(t)
			test.setup(runner)
			diagnostic := liveCaddyStartupExitDiagnostic(test.ctx(), manager, runner.id)
			if diagnostic.Status != test.want || strings.Contains(diagnostic.String(), "sensitive-canary") {
				t.Fatalf("diagnostic = %#v", diagnostic)
			}
		})
	}
}

func TestLiveCaddyStartupClassifierDoesNotSynthesizeMarkers(t *testing.T) {
	if got := liveCaddyStartupMarkers([]byte("permission\ndenied")); got != "other" {
		t.Fatalf("cross-line marker = %q", got)
	}
	if stdout, stderr := liveCaddyStartupMarkers([]byte("permission")), liveCaddyStartupMarkers([]byte("denied")); stdout != "other" || stderr != "other" {
		t.Fatalf("cross-stream markers = %q/%q", stdout, stderr)
	}
	if got := liveCaddyStartupMarkers([]byte("UNKNOWN COMMAND\naddress already in use\nconfig file does not exist\nunable to autosave\nread-only file system\npermission denied\nloading initial config\x00sensitive-canary")); got != "unknown_command+address_in_use+config_missing+autosave_failed+read_only+permission_denied+load_failed" {
		t.Fatalf("canonical marker union = %q", got)
	}
	if wrappers, causes := liveCaddyStartupStateClassifiers(json.RawMessage(`""`)); wrappers != "none" || causes != "none" {
		t.Fatalf("empty state classifiers = %q/%q", wrappers, causes)
	}
}

func TestLiveCaddyStartupStateClassifierUsesFixedBuckets(t *testing.T) {
	for code, want := range map[int]string{
		0: "success", 1: "general_failure", 2: "shell_misuse", 126: "cannot_execute", 127: "command_not_found",
		128: "invalid_exit", 137: "sigkill", 139: "segfault", 143: "terminated", 255: "runtime_255", 130: "signal_other", 42: "other_nonzero",
	} {
		if got := liveCaddyExitBucket(code); got != want {
			t.Fatalf("exit %d bucket = %q, want %q", code, got, want)
		}
	}
	for status, want := range map[string]string{"created": "created", "running": "running", "paused": "paused", "restarting": "restarting", "removing": "removing", "exited": "exited", "dead": "dead", "sensitive-canary": "unknown"} {
		if got := liveCaddyStateStatus(status); got != want {
			t.Fatalf("state %q bucket = %q, want %q", status, got, want)
		}
	}

	wrappers, cause := liveCaddyStartupStateClassifiers(json.RawMessage(`"failed to create task for container: failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: unable to setup user: operation not permitted: sensitive-canary"`))
	if wrappers != "create_task+shim_task+oci_create+runc_create+start_process+container_init" || cause != "user_lookup" {
		t.Fatalf("runtime state classifiers = %q/%q", wrappers, cause)
	}
	for _, test := range []struct {
		message string
		want    string
	}{
		{"docker-init: no such file or directory", "docker_init_missing"},
		{"docker-init failed", "docker_init_failed"},
		{"chdir to cwd permission denied", "workdir"},
		{"exec format error", "exec_format"},
		{"executable file not found", "executable_missing"},
		{"exec: permission denied", "entrypoint_denied"},
		{"error mounting tmpfs: permission denied", "mount_failed"},
		{"failed to set up container networking: port is already allocated", "network_failed"},
		{"apparmor denied operation", "security_policy"},
		{"setting cgroup config failed", "cgroup_failed"},
		{"pids limit reached", "pids_exhausted"},
		{"cannot allocate memory", "resource_exhausted"},
		{"no space left on device", "no_space"},
		{"read-only file system", "read_only"},
		{"operation not permitted", "operation_not_permitted"},
		{"permission denied", "permission_denied"},
		{"invalid argument", "invalid_argument"},
		{"resource temporarily unavailable", "resource_unavailable"},
		{"sensitive-canary", "other"},
	} {
		_, got := liveCaddyStartupStateClassifiers(json.RawMessage(strconv.Quote(test.message)))
		if got != test.want || strings.Contains(got, "sensitive-canary") {
			t.Fatalf("state cause for %q = %q, want %q", test.message, got, test.want)
		}
	}
}

func TestLiveCaddyStartupDiagnosticSharesSnapshotDeadline(t *testing.T) {
	manager, runner := newLiveCaddyStartupRunner(t)
	runner.delegate = liveSnapshotRunnerFunc(func(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if len(request.Args) > 0 && request.Args[0] == "image" {
			<-ctx.Done()
			return runtimeprocess.CommandResult{}, ctx.Err()
		}
		return runtimeprocess.CommandResult{}, errors.New("unexpected command")
	})
	started := time.Now()
	diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, 20*time.Millisecond)
	if len(runner.requests) != 0 || !strings.Contains(diagnostic, "caddy_startup_status:not_attempted") {
		t.Fatalf("diagnostic = %q, startup requests = %v", diagnostic, runner.requests)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("diagnostic exceeded total deadline: %v", elapsed)
	}
}

func TestLiveIngressRouteFailureDiagnosticIsReadOnlyAndIndependent(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	request := switchRequest(runner)
	original := request
	original.Endpoints = append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...)
	if diagnostic := liveIngressRouteFailureDiagnostic(context.Background(), manager, request); diagnostic != "route_snapshot=listen:valid,listen_reason:ip_expected,endpoints:valid" {
		t.Fatalf("valid route diagnostic = %q", diagnostic)
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("route diagnostic mutated the request")
	}
	for _, command := range runner.commands {
		for _, forbidden := range []string{"create", "start", "connect", "disconnect", "cp", "exec"} {
			if containsArgument(command, forbidden) {
				t.Fatalf("route diagnostic issued a mutating command: %v", command)
			}
		}
	}

	manager, runner = newManagerFixture(t, false)
	runner.ingressContainers[0].IPv4Address = "10.0.0.1/28"
	if diagnostic := liveIngressRouteFailureDiagnostic(context.Background(), manager, switchRequest(runner)); diagnostic != "route_snapshot=listen:drift,listen_reason:ip_outside_subnet,endpoints:valid" {
		t.Fatalf("listen drift diagnostic = %q", diagnostic)
	}

	manager, runner = newManagerFixture(t, false)
	request = switchRequest(runner)
	request.Endpoints[0].NetworkAlias = "wrong-alias"
	if diagnostic := liveIngressRouteFailureDiagnostic(context.Background(), manager, request); diagnostic != "route_snapshot=listen:valid,listen_reason:ip_expected,endpoints:drift" {
		t.Fatalf("endpoint drift diagnostic = %q", diagnostic)
	}
}

func TestLiveIngressRouteFailureDiagnosticSkipsInvalidOrCancelledRequests(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	const skipped = "route_snapshot=listen:not_attempted,listen_reason:not_attempted,endpoints:not_attempted"
	if diagnostic := liveIngressRouteFailureDiagnostic(cancelled, manager, switchRequest(runner)); diagnostic != skipped || len(runner.commands) != 0 {
		t.Fatalf("cancelled route diagnostic = %q, commands = %v", diagnostic, runner.commands)
	}
	if diagnostic := liveIngressRouteFailureDiagnostic(context.Background(), manager, generatedruntime.RouteSwitchRequest{}); diagnostic != skipped || len(runner.commands) != 0 {
		t.Fatalf("invalid route diagnostic = %q, commands = %v", diagnostic, runner.commands)
	}
}
