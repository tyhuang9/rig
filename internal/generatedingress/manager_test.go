package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type ingressRunner struct {
	appID              string
	network            string
	endpoint           generatedruntime.RouteEndpoint
	caddyNetworks      map[string]*networkAttachment
	files              map[string][]byte
	commands           [][]string
	failProposedReload bool
	stopped            bool
	volumeOptions      map[string]string
	endpointRoleLabel  string
	capacityOutput     []byte
	caddyMissing       bool
	ingressMissing     bool
	liveConfig         []byte
	startConfig        []byte
	restartInstalls    int
	failRestartInstall int
	failRollbackReload bool
}

func (r *ingressRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.commands = append(r.commands, args)
	if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
		return jsonResult(imageInspection{ID: "sha256:" + strings.Repeat("a", 64), OS: "linux", RepoDigests: []string{"caddy@" + caddyImageDigest}}), nil
	}
	if len(args) >= 3 && args[0] == "volume" && args[1] == "inspect" {
		return jsonResult(volumeInspection{Name: caddyVolumeName, Driver: "local", Scope: "local", Options: r.volumeOptions, Labels: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"}}), nil
	}
	if len(args) >= 3 && args[0] == "network" && args[1] == "inspect" {
		name := args[len(args)-1]
		if name == caddyNetworkName {
			if r.ingressMissing {
				return runtimeprocess.CommandResult{Stderr: []byte("no such network")}, errors.New("missing")
			}
			subnet, gateway, _ := ingressNetworkCandidate(0)
			return jsonResult(networkInspection{Name: caddyNetworkName, Driver: "bridge", Scope: "local", Options: map[string]string{}, IPAM: []networkIPAM{{Subnet: subnet, Gateway: gateway}}, Labels: map[string]string{"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v1"}}), nil
		}
		return jsonResult(networkInspection{Name: r.network, Driver: "bridge", Scope: "local", Options: map[string]string{}, Labels: map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": r.appID}}), nil
	}
	if len(args) >= 3 && args[0] == "network" && args[1] == "create" {
		r.ingressMissing = false
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "connect" {
		r.caddyNetworks[args[2]] = &networkAttachment{}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "disconnect" {
		delete(r.caddyNetworks, args[2])
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "inspect" {
		identity := args[len(args)-1]
		if identity == caddyContainerName {
			if r.caddyMissing {
				return runtimeprocess.CommandResult{Stderr: []byte("no such container")}, errors.New("missing")
			}
			return jsonResult(r.caddyInspection()), nil
		}
		if identity == r.endpoint.ContainerID {
			role := r.endpointRoleLabel
			if role == "" {
				role = r.endpoint.Role
			}
			return jsonResult(endpointInspection{ID: r.endpoint.ContainerID, Labels: map[string]string{
				"io.rig.managed": "generated-runtime", "io.rig.application": r.appID, "io.rig.component": r.endpoint.Component, "io.rig.slot": "blue", "io.rig.role": role,
			}, Running: true, Health: "healthy", Networks: map[string]*networkAttachment{r.network: {Aliases: []string{r.endpoint.NetworkAlias}}}}), nil
		}
		return runtimeprocess.CommandResult{Stderr: []byte("no such container")}, errors.New("missing")
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "create" {
		r.caddyMissing = false
		r.stopped = true
		ipIndex := argumentIndex(args, "--ip")
		if ipIndex < 0 || ipIndex+1 >= len(args) {
			return runtimeprocess.CommandResult{}, errors.New("missing static ingress address")
		}
		r.caddyNetworks[caddyNetworkName] = &networkAttachment{IPAddress: args[ipIndex+1]}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" {
		body, err := os.ReadFile(args[2])
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		r.files[args[3]] = body
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" {
		r.stopped = false
		r.startConfig = append([]byte(nil), r.files[caddyContainerName+":/config/active.json"]...)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 7 && args[0] == "container" && args[1] == "exec" && args[3] == "caddy" && args[4] == "reload" {
		if r.failProposedReload && args[6] == "/config/proposed.json" {
			r.failProposedReload = false
			return runtimeprocess.CommandResult{Stderr: []byte("untrusted caddy output")}, errors.New("reload failed")
		}
		if r.failRollbackReload && args[6] == "/config/rollback.json" {
			r.failRollbackReload = false
			return runtimeprocess.CommandResult{Stderr: []byte("untrusted caddy output")}, errors.New("reload failed")
		}
		body, exists := r.files[caddyContainerName+":"+args[6]]
		if !exists {
			return runtimeprocess.CommandResult{}, errors.New("config missing")
		}
		r.liveConfig = append(r.liveConfig[:0], body...)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 8 && args[0] == "container" && args[1] == "exec" && args[2] == "--user" && args[3] == "0:0" && args[4] == caddyContainerName {
		source, destination := caddyContainerName+":"+args[6], caddyContainerName+":"+args[7]
		switch args[5] {
		case "cp":
			if args[7] == "/config/active.next.json" {
				r.restartInstalls++
				if r.restartInstalls == r.failRestartInstall {
					return runtimeprocess.CommandResult{Stderr: []byte("untrusted caddy output")}, errors.New("restart install failed")
				}
			}
			body, exists := r.files[source]
			if !exists {
				return runtimeprocess.CommandResult{}, errors.New("config missing")
			}
			r.files[destination] = append([]byte(nil), body...)
			return runtimeprocess.CommandResult{}, nil
		case "mv":
			body, exists := r.files[source]
			if !exists {
				return runtimeprocess.CommandResult{}, errors.New("config missing")
			}
			r.files[destination] = append([]byte(nil), body...)
			delete(r.files, source)
			return runtimeprocess.CommandResult{}, nil
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 6 && args[0] == "container" && args[1] == "exec" && args[3] == "sh" && args[4] == "-c" && args[5] == capacityProbeCommand {
		output := r.capacityOutput
		if output == nil {
			output = []byte("2147483648 8589934592\n")
		}
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), output...)}, nil
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "exec" {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected Docker command")
}

func (r *ingressRunner) caddyInspection() caddyInspection {
	return caddyInspection{
		ID: "sha256:" + strings.Repeat("d", 64), Name: "/" + caddyContainerName, Image: "sha256:" + strings.Repeat("a", 64),
		Labels: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.listener-isolation": "v1"}, Hostname: caddyContainerName, User: "1000:1000", Env: []string{"XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"},
		Cmd: []string{"run", "--config", "/config/active.json"}, ReadOnly: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		Mounts: []mountInspection{{Type: "volume", Name: caddyVolumeName, Destination: "/config", RW: true}}, Tmpfs: map[string]string{"/data": "rw,noexec,nosuid,nodev,size=67108864"},
		Memory: 268435456, MemorySwap: 268435456, NanoCPUs: 1_000_000_000, PIDsLimit: 128, LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"}, Restart: "unless-stopped", Running: !r.stopped,
		NetworkMode: caddyNetworkName, Ulimits: []ulimitInspection{{Name: "nofile", Hard: 1024, Soft: 1024}}, PortBindings: map[string][]map[string]string{"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": "8080"}}}, Networks: r.caddyNetworks,
	}
}

func TestSwitchPersistsAcceptedRouteAfterValidatedReload(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	request := switchRequest(runner)
	if err := manager.Switch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	state, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || state.Active[runner.appID].Slot != generatedruntime.SlotBlue {
		t.Fatalf("state = %#v", state)
	}
	if !commandBefore(runner.commands, "validate", "reload") {
		t.Fatal("Caddy config was not validated before reload")
	}
	if _, ok := runner.files[caddyContainerName+":/config/proposed.json"]; !ok {
		t.Fatal("proposed aggregate config was not copied to Caddy")
	}
}

func TestSwitchIdempotentlyRepairsCommittedRoute(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	request := switchRequest(runner)
	if err := manager.Switch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	committed, err := manager.store.load()
	if err != nil || !sameRoute(committed.Active[runner.appID], routeRecord{Slot: request.ToSlot, Endpoints: request.Endpoints}) {
		t.Fatalf("committed route before repair = %#v, error = %v", committed, err)
	}
	runner.liveConfig = []byte("drifted live configuration")
	commandCount := len(runner.commands)
	if err := manager.Switch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	attested, reconciled := false, false
	for _, command := range runner.commands[commandCount:] {
		if containsArgument(command, "/config/proposed.json") {
			t.Fatalf("idempotent repair re-ran the proposed switch: %v", command)
		}
		attested = attested || containsArgument(command, runner.endpoint.ContainerID)
		reconciled = reconciled || containsArgument(command, "/config/reconcile.json")
	}
	if !attested || !reconciled {
		t.Fatalf("idempotent repair attested=%t reconciled=%t commands=%v", attested, reconciled, runner.commands[commandCount:])
	}
	state, err := manager.store.load()
	if err != nil || state.Pending != nil || !sameRoute(state.Active[runner.appID], routeRecord{Slot: request.ToSlot, Endpoints: request.Endpoints}) {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
	expected := expectedConfig(t, runner, state.Active)
	if !bytes.Equal(runner.liveConfig, expected) || !bytes.Equal(runner.files[caddyContainerName+":/config/active.json"], expected) {
		t.Fatal("idempotent repair did not restore live and restart configurations")
	}
}

func TestSwitchIdempotentRepairFailureMarksCandidateLive(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	request := switchRequest(runner)
	if err := manager.Switch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	runner.failRestartInstall = runner.restartInstalls + 1

	err := manager.Switch(context.Background(), request)
	if !generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("error = %v, candidate may be live = false", err)
	}
	state, loadErr := manager.store.load()
	if loadErr != nil || state.Pending != nil || !sameRoute(state.Active[runner.appID], routeRecord{Slot: request.ToSlot, Endpoints: request.Endpoints}) {
		t.Fatalf("state = %#v, error = %v", state, loadErr)
	}
}

func TestSwitchIdempotentRepairAttestsCandidateBeforePublishing(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	request := switchRequest(runner)
	if err := manager.Switch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	commandCount := len(runner.commands)
	runner.endpointRoleLabel = "static"

	err := manager.Switch(context.Background(), request)
	if !IsCode(err, DiagnosticIngressDrift) || !generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("error = %v, candidate may be live = %t", err, generatedruntime.RouteCandidateMayBeLive(err))
	}
	for _, command := range runner.commands[commandCount:] {
		if containsArgument(command, "/config/reconcile.json") {
			t.Fatalf("drifted candidate was published before attestation: %v", command)
		}
	}
}

func TestSwitchRejectsEndpointRoleLabelDrift(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.endpointRoleLabel = "static"
	if err := manager.Switch(context.Background(), switchRequest(runner)); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("error = %v", err)
	}
}

func TestSwitchReloadFailureRollsBackFirstDeploymentAndClearsPendingState(t *testing.T) {
	manager, runner := newManagerFixture(t, true)
	err := manager.Switch(context.Background(), switchRequest(runner))
	if !IsCode(err, DiagnosticRouteReloadFailed) {
		t.Fatalf("error = %v", err)
	}
	state, loadErr := manager.store.load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Pending != nil || len(state.Active) != 0 {
		t.Fatalf("rollback state = %#v", state)
	}
	reloads := 0
	for _, command := range runner.commands {
		if len(command) > 4 && command[0] == "container" && command[1] == "exec" && command[4] == "reload" {
			reloads++
		}
	}
	if reloads != 3 {
		t.Fatalf("reload attempts = %d, want committed recovery, proposed, and rollback", reloads)
	}
}

func TestSwitchRestartInstallFailureCompensatesBeforeCleanupSafeError(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	previous := routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", runner.network, "web-green", 3000, 'c'),
	}}
	if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{runner.appID: previous}}); err != nil {
		t.Fatal(err)
	}
	runner.failRestartInstall = 2
	request := switchRequest(runner)
	request.FromSlot = generatedruntime.SlotGreen

	err := manager.Switch(context.Background(), request)
	if !IsCode(err, DiagnosticRouteUnresolved) || generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("error = %v, candidate may be live = %t", err, generatedruntime.RouteCandidateMayBeLive(err))
	}
	state, loadErr := manager.store.load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Pending != nil || !sameRoute(state.Active[runner.appID], previous) {
		t.Fatalf("compensated state = %#v", state)
	}
	expected := expectedConfig(t, runner, map[string]routeRecord{runner.appID: previous})
	for name, body := range map[string][]byte{
		"live":    runner.liveConfig,
		"current": runner.files[caddyContainerName+":/config/current.json"],
		"restart": runner.files[caddyContainerName+":/config/active.json"],
	} {
		if !bytes.Equal(body, expected) {
			t.Fatalf("%s config did not roll back to the old slot", name)
		}
	}
}

func TestSwitchFailedCompensationMarksCandidateLiveAndRemainsRecoverable(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	previous := routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", runner.network, "web-green", 3000, 'c'),
	}}
	if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{runner.appID: previous}}); err != nil {
		t.Fatal(err)
	}
	runner.failRestartInstall = 2
	runner.failRollbackReload = true
	request := switchRequest(runner)
	request.FromSlot = generatedruntime.SlotGreen

	err := manager.Switch(context.Background(), request)
	if !IsCode(err, DiagnosticRouteUnresolved) || !generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("error = %v, candidate may be live = %t", err, generatedruntime.RouteCandidateMayBeLive(err))
	}
	proposed := routeRecord{Slot: request.ToSlot, Endpoints: request.Endpoints}
	state, loadErr := manager.store.load()
	if loadErr != nil || state.Pending != nil || !sameRoute(state.Active[runner.appID], proposed) {
		t.Fatalf("committed state = %#v, error = %v", state, loadErr)
	}
	if !bytes.Equal(runner.liveConfig, expectedConfig(t, runner, map[string]routeRecord{runner.appID: proposed})) {
		t.Fatal("candidate was not left live after uncertain compensation")
	}
	if err := manager.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	expected := expectedConfig(t, runner, map[string]routeRecord{runner.appID: proposed})
	if !bytes.Equal(runner.liveConfig, expected) || !bytes.Equal(runner.files[caddyContainerName+":/config/active.json"], expected) {
		t.Fatal("recovery did not reconcile the live and restart configs to durable state")
	}
}

func TestProvisionAndCapacitySnapshotUsePinnedDockerDataPlane(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	if err := manager.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryAvailableBytes != 2147483648 || snapshot.DiskAvailableBytes != 8589934592 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	foundProbe := false
	for _, command := range runner.commands {
		if len(command) == 6 && command[3] == "sh" && command[5] == capacityProbeCommand {
			foundProbe = true
		}
	}
	if !foundProbe {
		t.Fatal("capacity did not execute the fixed in-container probe")
	}
}

func TestCapacitySnapshotPreservesGenuinelyLowCapacity(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.capacityOutput = []byte("1024 2048\n")
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryAvailableBytes != 1024 || snapshot.DiskAvailableBytes != 2048 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	runner.capacityOutput = []byte("malformed\n")
	if _, err := manager.Snapshot(context.Background()); err == nil {
		t.Fatal("malformed capacity output was accepted")
	}
}

func TestLowIngressCapacityBecomesReplacementCapacityDiagnostic(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.capacityOutput = []byte("1024 2048\n")
	engine, err := generatedruntime.NewEngine(runner, unusedEnvironmentStager{}, manager, generatedruntime.EngineOptions{
		DockerExecutable:      manager.options.DockerExecutable,
		DockerConfigDirectory: manager.options.DockerConfigDirectory,
		WorkingDirectory:      manager.options.WorkingDirectory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReserveReplacement(context.Background(), 1); !generatedruntime.IsCode(err, generatedruntime.DiagnosticInsufficientReplacementSpace) {
		t.Fatalf("error = %v", err)
	}
}

func TestProvisionRejectsListenerAddressOutsideOwnedSubnet(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.caddyNetworks[caddyNetworkName].IPAddress = "10.203.0.3"
	if err := manager.Provision(context.Background()); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("error = %v", err)
	}
}

func TestProvisionRepairsStoppedCaddyBeforeReconnectingApplicationNetwork(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.stopped = true
	runner.caddyNetworks[runner.network] = &networkAttachment{}
	route := routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{runner.endpoint}}
	if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{runner.appID: route}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	disconnect, start, connect := commandIndex(runner.commands, "disconnect"), commandIndex(runner.commands, "start"), commandIndex(runner.commands, "connect")
	if disconnect < 0 || start <= disconnect || connect <= start {
		t.Fatalf("command order disconnect=%d start=%d connect=%d", disconnect, start, connect)
	}
	boot := runner.startConfig
	var config caddyConfig
	if json.Unmarshal(boot, &config) != nil || len(config.Apps.HTTP.Servers["generated"].Routes) != 0 {
		t.Fatalf("stopped Caddy was not seeded with an empty boot config: %s", boot)
	}
}

func TestProvisionReconcilesMissingAndStaleApplicationNetworks(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	route := routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{runner.endpoint}}
	if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{runner.appID: route}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, attached := runner.caddyNetworks[runner.network]; !attached {
		t.Fatal("missing committed application network was not connected")
	}
	if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, attached := runner.caddyNetworks[runner.network]; attached {
		t.Fatal("stale application network was not disconnected")
	}
}

func TestProvisionRejectsNonLocalVolumeOptions(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.volumeOptions = map[string]string{"type": "nfs"}
	if err := manager.Provision(context.Background()); !IsCode(err, DiagnosticIngressDrift) {
		t.Fatalf("error = %v", err)
	}
}

func TestCaddyInspectionRequiresNetworkEnvironmentAndUlimit(t *testing.T) {
	_, runner := newManagerFixture(t, false)
	valid := runner.caddyInspection()
	if !validCaddyInspection(valid, "sha256:"+strings.Repeat("a", 64), 8080) {
		t.Fatal("valid inspection was rejected")
	}
	mutations := []func(*caddyInspection){
		func(value *caddyInspection) { value.NetworkMode = "bridge" },
		func(value *caddyInspection) { value.Env = []string{"XDG_CONFIG_HOME=/config"} },
		func(value *caddyInspection) { value.Ulimits[0].Hard = 2048 },
		func(value *caddyInspection) { value.SecurityOpt = append(value.SecurityOpt, "seccomp=unconfined") },
		func(value *caddyInspection) {
			value.PortBindings["9000/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "9000"}}
		},
	}
	for index, mutate := range mutations {
		candidate := valid
		candidate.Env = append([]string(nil), valid.Env...)
		candidate.Ulimits = append([]ulimitInspection(nil), valid.Ulimits...)
		candidate.SecurityOpt = append([]string(nil), valid.SecurityOpt...)
		candidate.PortBindings = clonePortBindings(valid.PortBindings)
		mutate(&candidate)
		if validCaddyInspection(candidate, "sha256:"+strings.Repeat("a", 64), 8080) {
			t.Fatalf("drift mutation %d was accepted", index)
		}
	}
}

func TestProvisionCreatesPinnedIngressNetworkAndStaticCaddyAddress(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	runner.ingressMissing = true
	runner.caddyMissing = true
	runner.caddyNetworks = map[string]*networkAttachment{}
	if err := manager.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	subnet, gateway, address := ingressNetworkCandidate(0)
	if !hasCommandArguments(runner.commands, "network", "create", "--subnet", subnet, "--gateway", gateway, caddyNetworkName) {
		t.Fatalf("network create did not use pinned IPAM: %#v", runner.commands)
	}
	if !hasCommandArguments(runner.commands, "container", "create", "--network", caddyNetworkName, "--ip", address) {
		t.Fatalf("container create did not use static ingress address: %#v", runner.commands)
	}
}

func TestConfigPromotionRunsAsRootWhileCaddyCommandsRemainNonRoot(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	if err := manager.Switch(context.Background(), switchRequest(runner)); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if len(command) < 4 || command[0] != "container" || command[1] != "exec" {
			continue
		}
		joined := strings.Join(command, " ")
		if strings.Contains(joined, " caddy validate ") || strings.Contains(joined, " caddy reload ") {
			if containsArgument(command, "--user") {
				t.Fatalf("Caddy command unexpectedly elevated: %v", command)
			}
		}
		if containsArgument(command, " cp ") || containsArgument(command, " mv ") {
			if len(command) < 5 || command[2] != "--user" || command[3] != "0:0" {
				t.Fatalf("config promotion was not narrowly elevated: %v", command)
			}
		}
	}
}

func newManagerFixture(t *testing.T, failReload bool) (*Manager, *ingressRunner) {
	t.Helper()
	root := t.TempDir()
	dockerConfig := filepath.Join(root, "docker-config")
	if err := os.Mkdir(dockerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	appID := "11111111-1111-4111-8111-111111111111"
	endpoint := endpoint("web", "server", "rig-app-network", "web-blue", 3000, 'b')
	_, _, ingressIP := ingressNetworkCandidate(0)
	runner := &ingressRunner{appID: appID, network: endpoint.NetworkName, endpoint: endpoint, caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {IPAddress: ingressIP}}, files: map[string][]byte{}, failProposedReload: failReload}
	manager, err := New(runner, Options{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: dockerConfig, WorkingDirectory: root, DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	return manager, runner
}

func switchRequest(runner *ingressRunner) generatedruntime.RouteSwitchRequest {
	return generatedruntime.RouteSwitchRequest{AppID: runner.appID, ToSlot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{runner.endpoint}}
}

func expectedConfig(t *testing.T, runner *ingressRunner, routes map[string]routeRecord) []byte {
	t.Helper()
	_, _, ingressIP := ingressNetworkCandidate(0)
	config, err := buildCaddyConfig(routes, ingressIP+":8080")
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func jsonResult(value any) runtimeprocess.CommandResult {
	body, _ := json.Marshal(value)
	return runtimeprocess.CommandResult{Stdout: body}
}

func commandBefore(commands [][]string, first, second string) bool {
	firstIndex, secondIndex := -1, -1
	for index, command := range commands {
		for _, argument := range command {
			if argument == first && firstIndex < 0 {
				firstIndex = index
			}
			if argument == second && secondIndex < 0 {
				secondIndex = index
			}
		}
	}
	return firstIndex >= 0 && secondIndex > firstIndex
}

func commandIndex(commands [][]string, argument string) int {
	for index, command := range commands {
		if containsArgument(command, argument) {
			return index
		}
	}
	return -1
}

func containsArgument(command []string, expected string) bool {
	expected = strings.TrimSpace(expected)
	for _, argument := range command {
		if argument == expected {
			return true
		}
	}
	return false
}

func argumentIndex(command []string, expected string) int {
	for index, argument := range command {
		if argument == expected {
			return index
		}
	}
	return -1
}

func hasCommandArguments(commands [][]string, expected ...string) bool {
	for _, command := range commands {
		position := 0
		for _, argument := range command {
			if position < len(expected) && argument == expected[position] {
				position++
			}
		}
		if position == len(expected) {
			return true
		}
	}
	return false
}

func clonePortBindings(values map[string][]map[string]string) map[string][]map[string]string {
	result := make(map[string][]map[string]string, len(values))
	for port, bindings := range values {
		result[port] = make([]map[string]string, len(bindings))
		for index, binding := range bindings {
			result[port][index] = make(map[string]string, len(binding))
			for key, value := range binding {
				result[port][index][key] = value
			}
		}
	}
	return result
}

type unusedEnvironmentStager struct{}

func (unusedEnvironmentStager) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("environment staging must not run during capacity admission")
}
