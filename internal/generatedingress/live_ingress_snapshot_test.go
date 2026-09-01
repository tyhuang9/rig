package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	liveCaddyMismatchCommand
	liveCaddyMismatchLabels
	liveCaddyMismatchUlimit
	liveCaddyMismatchMount
	liveCaddyMismatchPort
)

func liveIngressFailureDiagnostic(ctx context.Context, manager *Manager) string {
	return liveIngressFailureDiagnosticWithin(ctx, manager, 5*time.Second)
}

func liveIngressFailureDiagnosticWithin(ctx context.Context, manager *Manager, timeout time.Duration) string {
	if manager == nil || ctx == nil || ctx.Err() != nil || timeout <= 0 {
		return "ingress_snapshot=image:not_attempted,volume:not_attempted,network:not_attempted,caddy:not_attempted,caddy_running:unobserved,caddy_mismatches=unobserved"
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
				}
			}
		}
		clearLiveCaddyInspection(&caddy)
	}
	imageID = ""

	return "ingress_snapshot=image:" + string(imageStatus) +
		",volume:" + string(volumeStatus) +
		",network:" + string(networkStatus) +
		",caddy:" + string(caddyStatus) +
		",caddy_running:" + caddyRunning +
		",caddy_mismatches=" + caddyMismatches
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
	names := make([]string, 0, 15)
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
	*value = networkInspection{}
}

func clearLiveCaddyInspection(value *caddyInspection) {
	if value == nil {
		return
	}
	clear(value.Labels)
	clear(value.Env)
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
	const expected = "ingress_snapshot=image:valid,volume:valid,network:valid,caddy:valid,caddy_running:true,caddy_mismatches=none"
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
	const missing = "ingress_snapshot=image:missing,volume:missing,network:missing,caddy:missing,caddy_running:unobserved,caddy_mismatches=unobserved"
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != missing {
		t.Fatalf("missing diagnostic = %q", diagnostic)
	}

	manager.runner = liveSnapshotRunnerFunc(func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{}, errors.New("sensitive-canary")
	})
	const failed = "ingress_snapshot=image:error,volume:error,network:error,caddy:error,caddy_running:unobserved,caddy_mismatches=unobserved"
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != failed {
		t.Fatalf("error diagnostic = %q", diagnostic)
	}

	manager.runner = liveSnapshotRunnerFunc(func(ctx context.Context, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		<-ctx.Done()
		return runtimeprocess.CommandResult{}, ctx.Err()
	})
	started := time.Now()
	const timedOut = "ingress_snapshot=image:error,volume:not_attempted,network:not_attempted,caddy:not_attempted,caddy_running:unobserved,caddy_mismatches=unobserved"
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, 20*time.Millisecond); diagnostic != timedOut {
		t.Fatalf("timeout diagnostic = %q", diagnostic)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("diagnostic exceeded total timeout: %v", elapsed)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	const notAttempted = "ingress_snapshot=image:not_attempted,volume:not_attempted,network:not_attempted,caddy:not_attempted,caddy_running:unobserved,caddy_mismatches=unobserved"
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
	const suppressed = "ingress_snapshot=image:valid,volume:valid,network:valid,caddy:invalid,caddy_running:true,caddy_mismatches=suppressed"
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
	const incomplete = "ingress_snapshot=image:invalid,volume:valid,network:valid,caddy:invalid,caddy_running:true,caddy_mismatches=comparison_incomplete"
	if diagnostic := liveIngressFailureDiagnosticWithin(context.Background(), manager, time.Second); diagnostic != incomplete || strings.Contains(diagnostic, "sensitive-canary") {
		t.Fatalf("incomplete diagnostic = %q", diagnostic)
	}
}

func TestLiveIngressInspectionClearHelpersZeroDecodedState(t *testing.T) {
	_, runner := newManagerFixture(t, false)
	image := imageInspection{ID: "sensitive", OS: "linux", RepoDigests: []string{"sensitive"}}
	volume := volumeInspection{Name: "sensitive", Options: map[string]string{"sensitive": "sensitive"}, Labels: map[string]string{"sensitive": "sensitive"}}
	network := networkInspection{Name: "sensitive", Options: map[string]string{"sensitive": "sensitive"}, IPAM: []networkIPAM{{Subnet: "sensitive"}}, Labels: map[string]string{"sensitive": "sensitive"}}
	caddy := runner.caddyInspection()
	clearLiveImageInspection(&image)
	clearLiveVolumeInspection(&volume)
	clearLiveNetworkInspection(&network)
	clearLiveCaddyInspection(&caddy)
	if !reflect.DeepEqual(image, imageInspection{}) || !reflect.DeepEqual(volume, volumeInspection{}) || !reflect.DeepEqual(network, networkInspection{}) || !reflect.DeepEqual(caddy, caddyInspection{}) {
		t.Fatal("decoded inspection state was not cleared")
	}
}
