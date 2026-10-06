package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// gatewayCurrentPhysicalExecutor is a deterministic Docker boundary. It
// returns the same normalized facts as the production Docker formats and
// applies only the exact network/config/start effects issued by the runtime.
type gatewayCurrentPhysicalExecutor struct {
	target           gatewayCurrentPhysicalTarget
	container        caddyInspection
	containerRuntime gatewayContainerRuntime
	ingress          caddyNetworkInspection
	networks         map[string]caddyNetworkInspection
	networkIDs       map[string]string
	endpoints        map[string]endpointInspection
	files            map[string][]byte
	live             []byte
	ownedContainers  []string
	requests         [][]string
	effects          [][]string
	lostReloadAck    bool
	lostStartAck     bool
	lostStopAck      bool
	containerPresent bool
	localHostPort    uint16
}

func newGatewayCurrentPhysicalExecutor(t *testing.T, target gatewayCurrentPhysicalTarget,
	physical gatewayCurrentRouteState, restart gatewayCurrentRouteState, localHostPort uint16,
) *gatewayCurrentPhysicalExecutor {
	t.Helper()
	container, containerRuntime := gatewayCurrentPhysicalRuntimeTestContainer(target, localHostPort, true)
	result := &gatewayCurrentPhysicalExecutor{target: target, container: container,
		containerRuntime: containerRuntime, networks: make(map[string]caddyNetworkInspection),
		networkIDs: make(map[string]string), endpoints: make(map[string]endpointInspection),
		files: make(map[string][]byte), ownedContainers: []string{target.Identity.Rebind.FinalContainer},
		containerPresent: true, localHostPort: localHostPort}
	result.ingress = caddyNetworkInspection{Name: target.Identity.Rebind.IngressNetwork, Driver: "bridge", Scope: "local",
		Options: map[string]string{gatewayRebindBridgeNameOptionKey: "rig" + target.Identity.Rebind.Digest[:12]},
		IPAM: caddyNetworkIPAM{Config: []networkIPAM{{Subnet: target.State.Network.Subnet,
			Gateway: target.State.Network.GatewayIPv4}}},
		Labels: gatewayCurrentPhysicalLabels(target, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
		Containers: map[string]caddyNetworkContainerInspection{target.Resources.FinalContainer.ID: {
			Name: target.Identity.Rebind.FinalContainer, IPv4Address: target.State.Network.ContainerIPv4 + "/" +
				strings.TrimPrefix(target.State.Network.Subnet[strings.LastIndex(target.State.Network.Subnet, "/"):], "/")}}}
	result.networkIDs[target.Identity.Rebind.IngressNetwork] = target.Resources.IngressNetwork.ID
	result.installRouteNetworks(t, physical, true)
	result.installRouteNetworks(t, target.State, false)
	result.syncFinalNetworks(physical)
	result.live = mustGatewayCurrentPhysicalConfig(t, physical)
	result.files[target.Identity.Rebind.ActiveConfigFilename] = mustGatewayCurrentPhysicalConfig(t, restart)
	return result
}

func (r *gatewayCurrentPhysicalExecutor) installRouteNetworks(t *testing.T,
	state gatewayCurrentRouteState, attachFinal bool,
) {
	t.Helper()
	owners, ok := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(state))
	if !ok {
		t.Fatal("invalid executor route owners")
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	for index, name := range names {
		if _, exists := r.networkIDs[name]; !exists {
			digest := sha256.Sum256([]byte(name))
			r.networkIDs[name] = hex.EncodeToString(digest[:])
		}
		network := r.networks[name]
		if network.Name == "" {
			network = caddyNetworkInspection{Name: name, Driver: "bridge", Scope: "local", Options: map[string]string{},
				Labels: map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue,
					"io.rig.application": owners[name]}, Containers: make(map[string]caddyNetworkContainerInspection)}
		}
		if attachFinal {
			address := "172.30." + strconv.Itoa(index+1) + ".2"
			network.Containers[r.target.Resources.FinalContainer.ID] = caddyNetworkContainerInspection{
				Name: r.target.Identity.Rebind.FinalContainer, IPv4Address: address + "/24"}
		}
		r.networks[name] = network
	}
	for appID, app := range state.Apps {
		for index, endpoint := range app.Route.Endpoints {
			network := r.networks[endpoint.NetworkName]
			address := "172.31." + strconv.Itoa(index+1) + ".10"
			network.Containers[normalizeID(endpoint.ContainerID)] = caddyNetworkContainerInspection{
				Name: endpoint.NetworkAlias, IPv4Address: address + "/24"}
			r.networks[endpoint.NetworkName] = network
			r.endpoints[normalizeID(endpoint.ContainerID)] = endpointInspection{ID: normalizeID(endpoint.ContainerID),
				Running: true, Health: "healthy", Labels: map[string]string{"io.rig.managed": "generated-runtime",
					"io.rig.application": appID, "io.rig.component": endpoint.Component,
					"io.rig.slot": string(app.Route.Slot), "io.rig.role": endpoint.Role},
				Networks: map[string]*networkAttachment{endpoint.NetworkName: {
					Aliases: []string{endpoint.NetworkAlias}, IPAddress: address}}}
		}
	}
}

func (r *gatewayCurrentPhysicalExecutor) syncFinalNetworks(state gatewayCurrentRouteState) {
	r.container.Networks = map[string]*networkAttachment{r.target.Identity.Rebind.IngressNetwork: {
		IPAddress: r.target.State.Network.ContainerIPv4, GwPriority: caddyGatewayPriority}}
	r.containerRuntime.ConfiguredNetworks = map[string]gatewayV2ConfiguredNetwork{r.target.Identity.Rebind.IngressNetwork: {
		NetworkID: r.target.Resources.IngressNetwork.ID, EndpointID: strings.Repeat("1", 64),
		IPAddress: r.target.State.Network.ContainerIPv4, GwPriority: caddyGatewayPriority,
		IPAMConfig: &gatewayV2ConfiguredIPAM{IPv4Address: r.target.State.Network.ContainerIPv4}}}
	owners, _ := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(state))
	for name := range owners {
		r.attach(name)
	}
}

func (r *gatewayCurrentPhysicalExecutor) stopAt(restart []byte) {
	r.container.Running = false
	r.container.Restarting = false
	owners, _ := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(r.target.State))
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	applications := make([]gatewayRebindHandoverApplicationNetwork, 0, len(names))
	for _, name := range names {
		applications = append(applications, gatewayRebindHandoverApplicationNetwork{
			Name: name, ID: r.networkIDs[name],
		})
	}
	r.containerRuntime = gatewayContainerRuntime{
		EffectivePortBindings: map[string][]map[string]string{},
		ConfiguredNetworks:    map[string]gatewayV2ConfiguredNetwork{},
	}
	gatewayCurrentPhysicalRuntimeInstallNetworks(r.target, &r.container, &r.containerRuntime,
		applications, false)
	r.ingress.Containers = map[string]caddyNetworkContainerInspection{}
	for name, network := range r.networks {
		delete(network.Containers, r.target.Resources.FinalContainer.ID)
		r.networks[name] = network
	}
	r.live = nil
	r.files[r.target.Identity.Rebind.ActiveConfigFilename] = append([]byte(nil), restart...)
}

func (r *gatewayCurrentPhysicalExecutor) attach(name string) {
	network := r.networks[name]
	address := "172.30.9.2"
	if member, ok := network.Containers[r.target.Resources.FinalContainer.ID]; ok {
		address = strings.TrimSuffix(member.IPv4Address, "/24")
	} else {
		network.Containers[r.target.Resources.FinalContainer.ID] = caddyNetworkContainerInspection{
			Name: r.target.Identity.Rebind.FinalContainer, IPv4Address: address + "/24"}
		r.networks[name] = network
	}
	r.container.Networks[name] = &networkAttachment{IPAddress: address}
	r.containerRuntime.ConfiguredNetworks[name] = gatewayV2ConfiguredNetwork{
		NetworkID: r.networkIDs[name], EndpointID: strings.Repeat("2", 64), IPAddress: address}
}

func (r *gatewayCurrentPhysicalExecutor) detach(name string) {
	delete(r.container.Networks, name)
	delete(r.containerRuntime.ConfiguredNetworks, name)
	network := r.networks[name]
	delete(network.Containers, r.target.Resources.FinalContainer.ID)
	r.networks[name] = network
}

func (r *gatewayCurrentPhysicalExecutor) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.requests = append(r.requests, args)
	if len(args) >= 2 && args[1] == "inspect" {
		return r.inspect(args)
	}
	if len(args) >= 2 && args[1] == "ls" {
		return r.list(args)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] == "-" {
		name := strings.TrimPrefix(args[2], r.target.Resources.FinalContainer.ID+":/config/")
		body, ok := r.files[name]
		if !ok {
			return runtimeprocess.CommandResult{}, errors.New("missing restart config")
		}
		return gatewayCurrentPhysicalTarResult(name, body)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" {
		body, err := os.ReadFile(args[2])
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		prefix := r.target.Resources.FinalContainer.ID + ":/config/"
		if !strings.HasPrefix(args[3], prefix) {
			return runtimeprocess.CommandResult{}, errors.New("replacement config target")
		}
		r.effects = append(r.effects, args)
		r.files[strings.TrimPrefix(args[3], prefix)] = append([]byte(nil), body...)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "connect" {
		name := r.networkName(args[2])
		if name == "" || args[3] != r.target.Resources.FinalContainer.ID {
			return runtimeprocess.CommandResult{}, errors.New("foreign network connect")
		}
		r.effects = append(r.effects, args)
		r.attach(name)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "disconnect" {
		name := r.networkName(args[2])
		if name == "" || args[3] != r.target.Resources.FinalContainer.ID {
			return runtimeprocess.CommandResult{}, errors.New("foreign network disconnect")
		}
		r.effects = append(r.effects, args)
		r.detach(name)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "exec" {
		if args[2] == r.target.Resources.FinalContainer.ID && args[3] == "curl" {
			if strings.Contains(args[len(args)-1], "127.0.0.1:2019/config") {
				return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.live...)}, nil
			}
			return runtimeprocess.CommandResult{Stdout: []byte("200")}, nil
		}
		if len(args) >= 7 && args[2] == r.target.Resources.FinalContainer.ID && args[3] == "caddy" {
			r.effects = append(r.effects, args)
			if args[4] == "reload" {
				r.live = append([]byte(nil), r.files[strings.TrimPrefix(args[6], "/config/")]...)
				if r.lostReloadAck {
					r.lostReloadAck = false
					return runtimeprocess.CommandResult{}, errors.New("lost reload acknowledgement")
				}
			}
			return runtimeprocess.CommandResult{}, nil
		}
		if len(args) == 8 && args[2] == "--user" && args[3] == "0:0" &&
			args[4] == r.target.Resources.FinalContainer.ID {
			r.effects = append(r.effects, args)
			source, destination := strings.TrimPrefix(args[6], "/config/"), strings.TrimPrefix(args[7], "/config/")
			body, ok := r.files[source]
			if !ok {
				return runtimeprocess.CommandResult{}, errors.New("missing config source")
			}
			r.files[destination] = append([]byte(nil), body...)
			if args[5] == "mv" {
				delete(r.files, source)
			}
			return runtimeprocess.CommandResult{}, nil
		}
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" &&
		args[2] == r.target.Resources.FinalContainer.ID {
		r.effects = append(r.effects, args)
		r.container.Running = true
		r.syncFinalNetworks(r.target.State)
		r.ingress.Containers[r.target.Resources.FinalContainer.ID] = caddyNetworkContainerInspection{
			Name: r.target.Identity.Rebind.FinalContainer,
			IPv4Address: r.target.State.Network.ContainerIPv4 + "/" +
				strings.TrimPrefix(r.target.State.Network.Subnet[strings.LastIndex(r.target.State.Network.Subnet, "/"):], "/"),
		}
		r.containerRuntime.EffectivePortBindings = cloneGatewayCurrentPhysicalTestPortBindings(r.container.PortBindings)
		r.live = append([]byte(nil), r.files[r.target.Identity.Rebind.ActiveConfigFilename]...)
		if r.lostStartAck {
			r.lostStartAck = false
			return runtimeprocess.CommandResult{}, errors.New("lost start acknowledgement")
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "stop" && args[2] == "--time" &&
		args[3] == "10" && args[4] == r.target.Resources.FinalContainer.ID {
		r.effects = append(r.effects, args)
		r.stopAt(r.files[r.target.Identity.Rebind.ActiveConfigFilename])
		if r.lostStopAck {
			r.lostStopAck = false
			return runtimeprocess.CommandResult{}, errors.New("lost stop acknowledgement")
		}
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected current physical command")
}

func (r *gatewayCurrentPhysicalExecutor) inspect(args []string) (runtimeprocess.CommandResult, error) {
	name := args[len(args)-1]
	switch args[0] {
	case "image":
		return jsonResult(imageInspection{ID: "sha256:" + r.target.Resources.ImageID, OS: "linux",
			RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}), nil
	case "volume":
		binding := r.target.Resources.ConfigVolume
		role := gatewayV2ConfigVolumeRole
		if name == r.target.Identity.Rebind.DataVolume {
			binding = gatewayRebindStageConfigVolumeBinding{Name: r.target.Resources.DataVolume.Name,
				Mountpoint: r.target.Resources.DataVolume.Mountpoint, CreatedAt: r.target.Resources.DataVolume.CreatedAt,
				OwnershipDigest: r.target.Resources.DataVolume.OwnershipDigest}
			role = gatewayV2DataVolumeRole
		}
		if name != binding.Name {
			return gatewayCurrentPhysicalNotFound("volume")
		}
		return jsonResult(gatewayVolumeIdentityInspection{Name: binding.Name, Driver: "local", Scope: "local",
			Options: map[string]string{}, Labels: gatewayCurrentPhysicalLabels(r.target, gatewayV2ManagedContainerLabel, role),
			Mountpoint: binding.Mountpoint, CreatedAt: binding.CreatedAt}), nil
	case "network":
		if name == r.target.Identity.Rebind.IngressNetwork {
			return gatewayCurrentPhysicalNetworkResult(r.target.Resources.IngressNetwork.ID, r.ingress)
		}
		network, ok := r.networks[name]
		if !ok {
			return gatewayCurrentPhysicalNotFound("network")
		}
		return gatewayCurrentPhysicalNetworkResult(r.networkIDs[name], network)
	case "container":
		if !r.containerPresent {
			return gatewayCurrentPhysicalNotFound("container")
		}
		if args[3] == endpointInspectFormat {
			if normalizeID(name) == r.target.Resources.FinalContainer.ID {
				return jsonResult(endpointInspection{ID: r.target.Resources.FinalContainer.ID,
					Running: r.container.Running, Health: "healthy", Labels: r.container.Labels,
					Networks: r.container.Networks}), nil
			}
			value, ok := r.endpoints[normalizeID(name)]
			if !ok {
				return gatewayCurrentPhysicalNotFound("container")
			}
			return jsonResult(value), nil
		}
		if name != r.target.Identity.Rebind.FinalContainer && normalizeID(name) != r.target.Resources.FinalContainer.ID {
			return gatewayCurrentPhysicalNotFound("container")
		}
		return jsonResult(gatewayContainerInspection{caddyInspection: r.container,
			gatewayContainerRuntime: r.containerRuntime}), nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected inspect")
}

func (r *gatewayCurrentPhysicalExecutor) list(args []string) (runtimeprocess.CommandResult, error) {
	if args[0] == "container" && containsArgumentPrefix(args, "volume=") {
		return runtimeprocess.CommandResult{Stdout: []byte(r.target.Resources.FinalContainer.ID + "\n")}, nil
	}
	var names []string
	switch args[0] {
	case "container":
		names = append([]string(nil), r.ownedContainers...)
	case "volume":
		names = []string{r.target.Identity.Rebind.ConfigVolume, r.target.Identity.Rebind.DataVolume}
	case "network":
		names = []string{r.target.Identity.Rebind.IngressNetwork}
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected ownership list")
	}
	sort.Strings(names)
	return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(names, "\n") + "\n")}, nil
}

func (r *gatewayCurrentPhysicalExecutor) networkName(id string) string {
	for name, candidate := range r.networkIDs {
		if candidate == normalizeID(id) {
			return name
		}
	}
	return ""
}

func gatewayCurrentPhysicalNetworkResult(id string, value caddyNetworkInspection) (runtimeprocess.CommandResult, error) {
	body, err := json.Marshal([]gatewayNetworkIdentityInspection{{ID: normalizeID(id), Name: value.Name,
		Driver: value.Driver, Scope: value.Scope, Internal: value.Internal, Options: value.Options,
		IPAM: value.IPAM, Labels: value.Labels, Containers: value.Containers}})
	return runtimeprocess.CommandResult{Stdout: body}, err
}

func gatewayCurrentPhysicalNotFound(kind string) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{Stderr: []byte("no such " + kind)}, errors.New("not found")
}

func gatewayCurrentPhysicalTarResult(name string, body []byte) (runtimeprocess.CommandResult, error) {
	var output bytes.Buffer
	w := tar.NewWriter(&output)
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if _, err := w.Write(body); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if err := w.Close(); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return runtimeprocess.CommandResult{Stdout: output.Bytes()}, nil
}

func containsArgumentPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func mustGatewayCurrentPhysicalConfig(t *testing.T, state gatewayCurrentRouteState) []byte {
	t.Helper()
	body, err := gatewayCurrentPhysicalConfigBytes(state)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestManagedGatewayCurrentPhysicalRuntimeExecutorRejectsInventoryBeforeEffects(t *testing.T) {
	fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Before, transition.Before, localPort)
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}

	runner.ownedContainers = append(runner.ownedContainers, "replacement")
	if err := runtimeDriver.reconcile(context.Background(), target, func(context.Context) error { return nil }); err == nil ||
		len(runner.effects) != 0 {
		t.Fatalf("tampered owned inventory reached effects: effects=%v error=%v", runner.effects, err)
	}
	runner.ownedContainers = []string{target.Identity.Rebind.FinalContainer}
	runner.container.Networks["rig-unexpected"] = &networkAttachment{IPAddress: "172.99.0.2"}
	runner.containerRuntime.ConfiguredNetworks["rig-unexpected"] = gatewayV2ConfiguredNetwork{
		NetworkID: strings.Repeat("9", 64), EndpointID: strings.Repeat("8", 64), IPAddress: "172.99.0.2"}
	if err := runtimeDriver.reconcile(context.Background(), target, func(context.Context) error { return nil }); err == nil ||
		len(runner.effects) != 0 {
		t.Fatalf("unexpected application network reached effects: effects=%v error=%v", runner.effects, err)
	}
}

func TestManagedGatewayCurrentPhysicalRuntimeExecutorRecoversMixedChangedNetworkAndLostReloadAck(t *testing.T) {
	fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Before, transition.Before, localPort)
	runner.lostReloadAck = true
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
	if err := runtimeDriver.reconcile(context.Background(), target, func(context.Context) error { return nil }); err == nil {
		t.Fatal("lost reload acknowledgement was returned as a clean effect success")
	}
	if len(runner.effects) == 0 || !sameCaddyConfig(runner.live, mustGatewayCurrentPhysicalConfig(t, transition.Effective)) {
		requestCount := len(runner.requests)
		_, endpointErr := fixture.manager.inspectGatewayRouteEndpointProof(context.Background(),
			gatewayCurrentPhysicalRoutes(target.State))
		_, _, _, inventoryErr := runtimeDriver.reconcileInventory(context.Background(), target)
		start := requestCount - 20
		if start < 0 {
			start = 0
		}
		t.Fatalf("lost acknowledgement did not retain exact live target: effects=%v endpoint=%v inventory=%v blocked=%t requests=%v",
			runner.effects, endpointErr, inventoryErr, fixture.manager.gatewayRebindAdmissionBlocked(),
			runner.requests[start:requestCount])
	}
	// The idempotent retry resolves the exact mixed state and installs the
	// restart file before withdrawing the old network.
	if err := runtimeDriver.reconcile(context.Background(), target, func(context.Context) error { return nil }); err != nil {
		_, _, _, inventoryErr := runtimeDriver.reconcileInventory(context.Background(), target)
		proof, observeErr := runtimeDriver.observe(context.Background(), target)
		start := len(runner.requests) - 20
		if start < 0 {
			start = 0
		}
		t.Fatalf("retry exact mixed recovery: %v inventory=%v observe=%v proof=%#v effects=%v requests=%v",
			err, inventoryErr, observeErr, proof, runner.effects, runner.requests[start:])
	}
	proof, err := runtimeDriver.observe(context.Background(), target)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryEffective ||
		!reflect.DeepEqual(proof.State, transition.Effective) {
		t.Fatalf("effective changed-network proof=%#v error=%v", proof, err)
	}
}

func TestManagedGatewayCurrentPhysicalRuntimeExecutorRestartsOnlyAuthorizedStoppedState(t *testing.T) {
	for _, test := range []struct {
		name      string
		alternate bool
		lostAck   bool
	}{
		{name: "exact restart config"},
		{name: "known alternate with lost start acknowledgement", alternate: true, lostAck: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
			localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
			restartState := transition.Effective
			if test.alternate {
				restartState = transition.Before
			}
			runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Before, restartState, localPort)
			runner.stopAt(mustGatewayCurrentPhysicalConfig(t, restartState))
			runner.lostStartAck = test.lostAck
			fixture.manager.runner = runner
			fixture.manager.options.HostPort = localPort
			runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
				hostProbe: func(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
					if !runner.container.Running {
						return gatewayV2HostProbeResult{}
					}
					return gatewayCurrentPhysicalExecutorHostProbe(localPort)(ctx, address, port, host, path)
				},
				containerProbe: func(context.Context, string, string, uint16, string, string) bool {
					return runner.container.Running
				},
			}
			stopped, exact, inventoryErr := runtimeDriver.stoppedRecoveryInventory(context.Background(), target)
			if inventoryErr != nil || !stopped || exact == test.alternate {
				inventory, immutableErr := runtimeDriver.immutableInventoryValue(context.Background(), target)
				applications, applicationsErr := runtimeDriver.applicationNetworkBindings(context.Background(),
					target.State, inventory.Final, true)
				_, endpointErr := fixture.manager.inspectGatewayRouteEndpointProof(context.Background(),
					gatewayCurrentPhysicalRoutes(target.State))
				t.Fatalf("stopped inventory classification: stopped=%t exact=%t error=%v immutable=%v applications=%v final=%t endpoint=%v",
					stopped, exact, inventoryErr, immutableErr, applicationsErr,
					runtimeDriver.validFinalNetworks(target, inventory.Final, inventory.FinalRuntime,
						inventory.IngressID, applications), endpointErr)
			}
			if err := runtimeDriver.reconcile(context.Background(), target, func(context.Context) error { return nil }); err != nil {
				t.Fatalf("restart authorized stopped state: effects=%v error=%v", runner.effects, err)
			}
			proof, err := runtimeDriver.observe(context.Background(), target)
			if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryEffective ||
				!reflect.DeepEqual(proof.State, transition.Effective) {
				t.Fatalf("restarted physical proof=%#v error=%v", proof, err)
			}
			start := []string{"container", "start", target.Resources.FinalContainer.ID}
			if !containsGatewayCurrentPhysicalEffect(runner.effects, start) {
				t.Fatalf("exact protected container was not started: effects=%v", runner.effects)
			}
			if test.alternate {
				active := runner.files[target.Identity.Rebind.ActiveConfigFilename]
				if !sameCaddyConfig(active, mustGatewayCurrentPhysicalConfig(t, transition.Effective)) {
					t.Fatal("known alternate restart file was not replaced with the authorized target")
				}
			}
		})
	}

	fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Before, transition.Before, localPort)
	unknown := mustGatewayCurrentPhysicalConfig(t, transition.Before)
	unknown[len(unknown)-1] ^= 1
	runner.stopAt(unknown)
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe: func(context.Context, string, uint16, string, string) gatewayV2HostProbeResult {
			return gatewayV2HostProbeResult{}
		},
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return false },
	}
	if err := runtimeDriver.reconcile(context.Background(), target, func(context.Context) error { return nil }); err == nil ||
		len(runner.effects) != 0 {
		t.Fatalf("unknown stopped restart state reached effects: effects=%v error=%v", runner.effects, err)
	}
}

func TestManagedGatewayCurrentPhysicalRuntimeExecutorStopsOnlyExactOwnedIdentity(t *testing.T) {
	newStopCase := func(t *testing.T) (gatewayCurrentStateFixture, gatewayCurrentPhysicalTransition,
		[]gatewayCurrentPhysicalTarget, *gatewayCurrentPhysicalExecutor, managerGatewayCurrentPhysicalRuntime,
	) {
		t.Helper()
		fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
		localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
		selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: fixture.baseline.Lineage,
			Receipt: &fixture.receipt, State: &transition.Pending}
		before, err := gatewayCurrentPhysicalTargetFor(selection, transition.Before,
			transition.Pending.Pending, nil)
		if err != nil {
			t.Fatal(err)
		}
		effective, err := gatewayCurrentPhysicalTargetFor(selection, transition.Effective,
			transition.Pending.Pending, nil)
		if err != nil {
			t.Fatal(err)
		}
		runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Before, transition.Before, localPort)
		fixture.manager.runner = runner
		fixture.manager.options.HostPort = localPort
		runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
			hostProbe: func(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
				if !runner.containerPresent || !runner.container.Running {
					return gatewayV2HostProbeResult{}
				}
				return gatewayCurrentPhysicalExecutorHostProbe(localPort)(ctx, address, port, host, path)
			},
			containerProbe: func(context.Context, string, string, uint16, string, string) bool {
				return runner.containerPresent && runner.container.Running
			},
		}
		return fixture, transition, []gatewayCurrentPhysicalTarget{before, effective}, runner, runtimeDriver
	}

	t.Run("lost stop acknowledgement proves exact stopped identity", func(t *testing.T) {
		_, _, targets, runner, runtimeDriver := newStopCase(t)
		runner.lostStopAck = true
		proof, err := runtimeDriver.stop(context.Background(), targets, func(context.Context) error { return nil })
		if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped || !proof.Runtime.ListenerAbsent ||
			proof.Resources.FinalContainer == nil ||
			proof.Resources.FinalContainer.ID != runner.target.Resources.FinalContainer.ID {
			t.Fatalf("exact stopped proof=%#v effects=%v error=%v", proof, runner.effects, err)
		}
		want := []string{"container", "stop", "--time", "10", runner.target.Resources.FinalContainer.ID}
		if !containsGatewayCurrentPhysicalEffect(runner.effects, want) {
			t.Fatalf("stop did not use exact protected ID: effects=%v", runner.effects)
		}
	})

	t.Run("definitely absent exact id", func(t *testing.T) {
		_, _, targets, runner, runtimeDriver := newStopCase(t)
		runner.containerPresent = false
		runner.container.Running = false
		runner.ownedContainers = nil
		proof, err := runtimeDriver.stop(context.Background(), targets, func(context.Context) error { return nil })
		if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped || !proof.Runtime.ListenerAbsent ||
			len(runner.effects) != 0 {
			t.Fatalf("definite absence proof=%#v effects=%v error=%v", proof, runner.effects, err)
		}
	})

	t.Run("same name replacement refuses", func(t *testing.T) {
		_, _, targets, runner, runtimeDriver := newStopCase(t)
		runner.container.ID = strings.Repeat("8", 64)
		if _, err := runtimeDriver.stop(context.Background(), targets, func(context.Context) error { return nil }); err == nil ||
			len(runner.effects) != 0 {
			t.Fatalf("same-name replacement reached stop: effects=%v error=%v", runner.effects, err)
		}
	})
}

func containsGatewayCurrentPhysicalEffect(effects [][]string, want []string) bool {
	for _, effect := range effects {
		if reflect.DeepEqual(effect, want) {
			return true
		}
	}
	return false
}

func gatewayCurrentPhysicalExecutorTransition(t *testing.T) (gatewayCurrentStateFixture,
	gatewayCurrentPhysicalTransition, gatewayCurrentPhysicalTarget,
) {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	controllerRoot := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(controllerRoot)
	if err != nil {
		t.Fatal(err)
	}
	workingIdentity, err := os.Lstat(directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.options.WorkingDirectory = directories.WorkingDirectory
	fixture.manager.workingDirectoryIdentity = workingIdentity
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	request := routeOperationSwitchRequest(t, appID, app.Route)
	changedNetwork := "rig-current-" + strings.Repeat("c", 12)
	for index := range request.Endpoints {
		request.Endpoints[index].NetworkName = changedNetwork
		digest := sha256.Sum256([]byte(request.Endpoints[index].ContainerID + "\x00current"))
		request.Endpoints[index].ContainerID = hex.EncodeToString(digest[:])
		request.Endpoints[index].NetworkAlias += "-current"
	}
	transition, err := gatewayCurrentSwitchTransition(fixture.baseline, request)
	if err != nil {
		t.Fatal(err)
	}
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: fixture.baseline.Lineage,
		Receipt: &fixture.receipt, State: &transition.Pending}
	target, err := gatewayCurrentPhysicalTargetFor(selection, transition.Effective,
		transition.Pending.Pending, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, transition, target
}

func gatewayCurrentPhysicalExecutorHostProbe(localPort uint16) gatewayV2HostStatusProbe {
	return func(_ context.Context, address string, port uint16, _ string, path string) gatewayV2HostProbeResult {
		if address == "127.0.0.1" && port != localPort {
			return gatewayV2HostProbeResult{}
		}
		result := gatewayV2HostProbeResult{Connected: true, Responded: true, Status: 404}
		if strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
			result.Body = gatewayV2ChallengeBodyPrefix + strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
		}
		return result
	}
}
