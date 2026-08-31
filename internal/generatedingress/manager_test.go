package generatedingress

import (
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
}

func (r *ingressRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.commands = append(r.commands, args)
	if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
		return jsonResult(imageInspection{ID: "sha256:" + strings.Repeat("a", 64), OS: "linux", RepoDigests: []string{"caddy@" + caddyImageDigest}}), nil
	}
	if len(args) >= 3 && args[0] == "volume" && args[1] == "inspect" {
		return jsonResult(volumeInspection{Name: caddyVolumeName, Driver: "local", Labels: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"}}), nil
	}
	if len(args) >= 3 && args[0] == "network" && args[1] == "inspect" {
		return jsonResult(networkInspection{Name: r.network, Driver: "bridge", Scope: "local", Labels: map[string]string{"io.rig.managed": "generated-runtime", "io.rig.application": r.appID}}), nil
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "connect" {
		r.caddyNetworks[r.network] = &networkAttachment{}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "inspect" {
		identity := args[len(args)-1]
		if identity == caddyContainerName {
			return jsonResult(r.caddyInspection()), nil
		}
		if identity == r.endpoint.ContainerID {
			return jsonResult(endpointInspection{ID: r.endpoint.ContainerID, Labels: map[string]string{
				"io.rig.managed": "generated-runtime", "io.rig.application": r.appID, "io.rig.component": r.endpoint.Component, "io.rig.slot": "blue",
			}, Running: true, Health: "healthy", Networks: map[string]*networkAttachment{r.network: {Aliases: []string{r.endpoint.NetworkAlias}}}}), nil
		}
		return runtimeprocess.CommandResult{Stderr: []byte("no such container")}, errors.New("missing")
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" {
		body, err := os.ReadFile(args[2])
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		r.files[args[3]] = body
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 7 && args[0] == "container" && args[1] == "exec" && args[3] == "caddy" && args[4] == "reload" {
		if r.failProposedReload && args[6] == "/config/proposed.json" {
			r.failProposedReload = false
			return runtimeprocess.CommandResult{Stderr: []byte("untrusted caddy output")}, errors.New("reload failed")
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 6 && args[0] == "container" && args[1] == "exec" && args[3] == "sh" && args[4] == "-c" && args[5] == capacityProbeCommand {
		return runtimeprocess.CommandResult{Stdout: []byte("2147483648 8589934592\n")}, nil
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "exec" {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected Docker command")
}

func (r *ingressRunner) caddyInspection() caddyInspection {
	return caddyInspection{
		ID: "sha256:" + strings.Repeat("d", 64), Name: "/" + caddyContainerName, Image: "sha256:" + strings.Repeat("a", 64),
		Labels: map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1"}, User: "1000:1000",
		Cmd: []string{"run", "--config", "/config/active.json"}, ReadOnly: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		Mounts: []mountInspection{{Type: "volume", Name: caddyVolumeName, Destination: "/config", RW: true}}, Tmpfs: map[string]string{"/data": "rw,noexec,nosuid,nodev,size=67108864"},
		Memory: 268435456, MemorySwap: 268435456, NanoCPUs: 1_000_000_000, PIDsLimit: 128, LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"}, Restart: "unless-stopped", Running: true,
		PortBindings: map[string][]map[string]string{"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": "8080"}}}, Networks: r.caddyNetworks,
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
	if reloads != 2 {
		t.Fatalf("reload attempts = %d, want proposed plus rollback", reloads)
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

func newManagerFixture(t *testing.T, failReload bool) (*Manager, *ingressRunner) {
	t.Helper()
	root := t.TempDir()
	dockerConfig := filepath.Join(root, "docker-config")
	if err := os.Mkdir(dockerConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	appID := "11111111-1111-4111-8111-111111111111"
	endpoint := endpoint("web", "server", "rig-app-network", "web-blue", 3000, 'b')
	runner := &ingressRunner{appID: appID, network: endpoint.NetworkName, endpoint: endpoint, caddyNetworks: map[string]*networkAttachment{"none": {}}, files: map[string][]byte{}, failProposedReload: failReload}
	manager, err := New(runner, Options{DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: dockerConfig, WorkingDirectory: root, DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	return manager, runner
}

func switchRequest(runner *ingressRunner) generatedruntime.RouteSwitchRequest {
	return generatedruntime.RouteSwitchRequest{AppID: runner.appID, ToSlot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{runner.endpoint}}
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
