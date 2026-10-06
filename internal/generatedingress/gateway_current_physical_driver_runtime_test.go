package generatedingress

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestManagedGatewayCurrentPhysicalRuntimeBindsImmutableContainerAndDynamicNetworks(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.options.HostPort = fixture.history.Predecessor.Journal.Source.LocalHostPort
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
	runtimeDriver := managerGatewayCurrentPhysicalRuntime{manager: fixture.manager}
	container, runtime := gatewayCurrentPhysicalRuntimeTestContainer(target,
		fixture.manager.options.HostPort, true)
	if !runtimeDriver.validFinalBase(target, container, runtime) {
		t.Fatal("exact terminal-owned final container was rejected")
	}
	initial := append([]gatewayRebindHandoverApplicationNetwork(nil), target.Resources.ApplicationNetworks...)
	if !runtimeDriver.validFinalNetworks(target, container, runtime, target.Resources.IngressNetwork.ID, initial) {
		t.Fatal("exact initial application-network census was rejected")
	}

	for name, mutate := range map[string]func(*caddyInspection, *gatewayContainerRuntime, *Manager){
		"replacement id": func(c *caddyInspection, _ *gatewayContainerRuntime, _ *Manager) {
			c.ID = strings.Repeat("8", 64)
		},
		"foreign labels": func(c *caddyInspection, _ *gatewayContainerRuntime, _ *Manager) {
			c.Labels[gatewayV2OperationLabelKey] = "foreign"
		},
		"wrong published port": func(c *caddyInspection, _ *gatewayContainerRuntime, _ *Manager) {
			for key := range c.PortBindings {
				c.PortBindings[key][0]["HostIp"] = "0.0.0.0"
				break
			}
		},
		"wrong local host port": func(_ *caddyInspection, _ *gatewayContainerRuntime, m *Manager) {
			m.options.HostPort++
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneGatewayCurrentPhysicalTestContainer(container)
			candidateRuntime := cloneGatewayCurrentPhysicalTestRuntime(runtime)
			originalPort := fixture.manager.options.HostPort
			defer func() { fixture.manager.options.HostPort = originalPort }()
			mutate(&candidate, &candidateRuntime, fixture.manager)
			if runtimeDriver.validFinalBase(target, candidate, candidateRuntime) {
				t.Fatal("changed immutable container authority was accepted")
			}
		})
	}

	dynamic := target
	dynamic.State = cloneGatewayCurrentRouteState(target.State)
	changedNetwork := "rig-current-" + strings.Repeat("a", 12)
	changed := false
	for appID, app := range dynamic.State.Apps {
		if len(app.Route.Endpoints) == 0 {
			continue
		}
		app = cloneGatewayCurrentAppRoute(app)
		for index := range app.Route.Endpoints {
			app.Route.Endpoints[index].NetworkName = changedNetwork
		}
		dynamic.State.Apps[appID] = app
		changed = true
		break
	}
	if !changed {
		t.Fatal("fixture has no route endpoint")
	}
	dynamic.State.Digest = ""
	var err error
	dynamic.State.Digest, err = gatewayCurrentRouteStateDigest(dynamic.State)
	if err != nil || !validGatewayCurrentPhysicalTarget(dynamic) {
		t.Fatalf("dynamic current network state invalid: %v", err)
	}
	dynamicContainer, dynamicRuntime := gatewayCurrentPhysicalRuntimeTestContainer(dynamic,
		fixture.manager.options.HostPort, true)
	currentNetworks := gatewayCurrentPhysicalRuntimeTestApplicationNetworks(t, dynamic.State)
	gatewayCurrentPhysicalRuntimeInstallNetworks(dynamic, &dynamicContainer, &dynamicRuntime, currentNetworks, true)
	if reflect.DeepEqual(currentNetworks, dynamic.Resources.ApplicationNetworks) {
		t.Fatal("test did not change the current application-network census")
	}
	if !runtimeDriver.validFinalBase(dynamic, dynamicContainer, dynamicRuntime) ||
		!runtimeDriver.validFinalNetworks(dynamic, dynamicContainer, dynamicRuntime,
			dynamic.Resources.IngressNetwork.ID, currentNetworks) {
		t.Fatal("current application-network change was frozen to terminal initial census")
	}
	dynamicRuntime.ConfiguredNetworks["rig-unexpected"] = gatewayV2ConfiguredNetwork{
		NetworkID: strings.Repeat("7", 64), EndpointID: strings.Repeat("6", 64), IPAddress: "10.90.0.9",
	}
	dynamicContainer.Networks["rig-unexpected"] = &networkAttachment{IPAddress: "10.90.0.9"}
	if runtimeDriver.validFinalNetworks(dynamic, dynamicContainer, dynamicRuntime,
		dynamic.Resources.IngressNetwork.ID, currentNetworks) {
		t.Fatal("unexpected third application network was accepted")
	}
}

func TestManagedGatewayCurrentPhysicalRuntimeCommandsUseProtectedContainerIdentity(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.options.HostPort = fixture.history.Predecessor.Journal.Source.LocalHostPort
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
	commands := gatewayCurrentPhysicalConfigCommands(target)
	containerID := target.Resources.FinalContainer.ID
	active := target.Identity.Rebind.ActiveConfigFilename
	want := [][]string{
		{"container", "exec", containerID, "caddy", "validate", "--config", "/config/reconcile.json"},
		{"container", "exec", containerID, "caddy", "reload", "--config", "/config/reconcile.json"},
		{"container", "exec", "--user", "0:0", containerID, "cp", "/config/reconcile.json", "/config/active.next.json"},
		{"container", "exec", "--user", "0:0", containerID, "mv", "/config/active.next.json", "/config/" + active},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("current config commands=%v want=%v", commands, want)
	}
}

func TestManagedGatewayCurrentPhysicalRuntimeStoppedProofDoesNotRequireServingEndpoints(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	fixture.manager.options.HostPort = fixture.history.Predecessor.Journal.Source.LocalHostPort
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, fixture)
	target.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingRouteSwitch, AppID: firstGatewayCurrentAppID(t, target.State),
		Proposed: cloneGatewayCurrentAppRoute(target.State.Apps[firstGatewayCurrentAppID(t, target.State)])}
	runtimeDriver := managerGatewayCurrentPhysicalRuntime{
		manager: fixture.manager,
		hostProbe: func(context.Context, string, uint16, string, string) gatewayV2HostProbeResult {
			return gatewayV2HostProbeResult{}
		},
	}
	proof, err := runtimeDriver.stoppedAttestation(context.Background(), []gatewayCurrentPhysicalTarget{target},
		caddyInspection{}, gatewayContainerRuntime{ConfiguredNetworks: map[string]gatewayV2ConfiguredNetwork{
			"retired-mixed-network": {NetworkID: strings.Repeat("5", 64)},
		}})
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped || !proof.Runtime.ListenerAbsent ||
		!reflect.DeepEqual(proof.State, target.State) {
		t.Fatalf("withdrawal-only stopped proof required serving endpoints/config: proof=%#v error=%v", proof, err)
	}
}

func TestManagedGatewayCurrentPhysicalRuntimeStartsOnlyKnownAuthorizedRestartConfig(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	request := routeOperationSwitchRequest(t, appID, app.Route)
	for index := range request.Endpoints {
		request.Endpoints[index].NetworkName = "rig-current-" + strings.Repeat("b", 12)
		request.Endpoints[index].NetworkAlias += "-restart"
	}
	transition, err := gatewayCurrentSwitchTransition(fixture.baseline,
		request)
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
	effective, err := gatewayCurrentPhysicalConfigBytes(transition.Effective)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(effective)
	if exact, known := gatewayCurrentPhysicalRestartConfigKnown(target, effective); !exact || !known {
		t.Fatal("exact authorized restart config was not accepted")
	}
	before, err := gatewayCurrentPhysicalConfigBytes(transition.Before)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(before)
	if exact, known := gatewayCurrentPhysicalRestartConfigKnown(target, before); exact || !known {
		t.Fatal("known alternate restart config was not classified for guarded replacement")
	}
	unknown := append([]byte(nil), before...)
	unknown[len(unknown)-1] ^= 1
	if exact, known := gatewayCurrentPhysicalRestartConfigKnown(target, unknown); exact || known {
		t.Fatal("unknown restart config was accepted")
	}
	want := []string{"container", "start", target.Resources.FinalContainer.ID}
	if got := gatewayCurrentPhysicalStartCommand(target); !reflect.DeepEqual(got, want) {
		t.Fatalf("start command=%#v want=%#v", got, want)
	}
	foreign := target
	foreign.Resources.FinalContainer = nil
	if got := gatewayCurrentPhysicalStartCommand(foreign); got != nil {
		t.Fatalf("invalid target produced start command: %#v", got)
	}
}

func gatewayCurrentPhysicalRuntimeTestTarget(t *testing.T,
	fixture gatewayCurrentStateFixture,
) gatewayCurrentPhysicalTarget {
	t.Helper()
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: fixture.baseline.Lineage,
		Receipt: &fixture.receipt, State: &fixture.baseline}
	target, err := gatewayCurrentPhysicalTargetFor(selection, fixture.baseline, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func gatewayCurrentPhysicalRuntimeTestContainer(target gatewayCurrentPhysicalTarget,
	localPort uint16, running bool,
) (caddyInspection, gatewayContainerRuntime) {
	identity := target.Identity.Rebind
	container := caddyInspection{
		ID: target.Resources.FinalContainer.ID, Name: "/" + identity.FinalContainer,
		Image:    "sha256:" + target.Resources.ImageID,
		Labels:   gatewayCurrentPhysicalLabels(target, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole),
		Hostname: identity.FinalHostname, User: "1000:1000",
		Env: []string{"XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"}, Entrypoint: []string{caddyExecutable},
		Cmd: []string{"run", "--config", "/config/" + identity.ActiveConfigFilename}, ReadOnly: true,
		CapAdd: []string{caddyCapability}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		Mounts: []mountInspection{{Type: "volume", Name: identity.ConfigVolume, Destination: "/config", RW: true},
			{Type: "volume", Name: identity.DataVolume, Destination: "/data", RW: true}},
		Memory: 268435456, MemorySwap: 268435456, NanoCPUs: 1_000_000_000, PIDsLimit: 128,
		LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"},
		Restart: gatewayV2FinalRestartPolicy, NetworkMode: identity.IngressNetwork,
		Ulimits: []ulimitInspection{{Name: "nofile", Hard: 1024, Soft: 1024}}, Running: running,
		PortBindings: make(map[string][]map[string]string), Networks: make(map[string]*networkAttachment),
	}
	for port := target.State.Profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		container.PortBindings[text+"/tcp"] = []map[string]string{{"HostIp": target.State.Profile.SelectedIPv4,
			"HostPort": text}}
		if port == target.State.Profile.PortEnd {
			break
		}
	}
	container.PortBindings[strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp"] =
		[]map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.FormatUint(uint64(localPort), 10)}}
	runtime := gatewayContainerRuntime{EffectivePortBindings: make(map[string][]map[string]string),
		ConfiguredNetworks: make(map[string]gatewayV2ConfiguredNetwork)}
	gatewayCurrentPhysicalRuntimeInstallNetworks(target, &container, &runtime,
		target.Resources.ApplicationNetworks, running)
	if running {
		for key, bindings := range container.PortBindings {
			copy := make([]map[string]string, len(bindings))
			for index := range bindings {
				copy[index] = map[string]string{"HostIp": bindings[index]["HostIp"], "HostPort": bindings[index]["HostPort"]}
			}
			runtime.EffectivePortBindings[key] = copy
		}
	}
	return container, runtime
}

func gatewayCurrentPhysicalRuntimeInstallNetworks(target gatewayCurrentPhysicalTarget,
	container *caddyInspection, runtime *gatewayContainerRuntime,
	applications []gatewayRebindHandoverApplicationNetwork, running bool,
) {
	container.Networks = make(map[string]*networkAttachment)
	runtime.ConfiguredNetworks = make(map[string]gatewayV2ConfiguredNetwork)
	networks := append([]gatewayRebindHandoverApplicationNetwork{{Name: target.Identity.Rebind.IngressNetwork,
		ID: target.Resources.IngressNetwork.ID}}, applications...)
	for index, network := range networks {
		configured := gatewayV2ConfiguredNetwork{NetworkID: network.ID}
		if index == 0 {
			configured.GwPriority = caddyGatewayPriority
			configured.IPAMConfig = &gatewayV2ConfiguredIPAM{IPv4Address: target.State.Network.ContainerIPv4}
		}
		if running {
			configured.EndpointID = strings.Repeat(strconv.Itoa((index%8)+1), 64)
			configured.IPAddress = "10.88.0." + strconv.Itoa(index+2)
			if index == 0 {
				configured.IPAddress = target.State.Network.ContainerIPv4
			}
			container.Networks[network.Name] = &networkAttachment{IPAddress: configured.IPAddress,
				GwPriority: configured.GwPriority}
		}
		runtime.ConfiguredNetworks[network.Name] = configured
	}
}

func gatewayCurrentPhysicalRuntimeTestApplicationNetworks(t *testing.T,
	state gatewayCurrentRouteState,
) []gatewayRebindHandoverApplicationNetwork {
	t.Helper()
	owners, ok := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(state))
	if !ok {
		t.Fatal("invalid route network ownership")
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]gatewayRebindHandoverApplicationNetwork, len(names))
	for index, name := range names {
		result[index] = gatewayRebindHandoverApplicationNetwork{Name: name,
			ID: strings.Repeat(strconv.Itoa((index%8)+1), 64)}
	}
	return result
}

func cloneGatewayCurrentPhysicalTestContainer(value caddyInspection) caddyInspection {
	result := value
	result.Labels = cloneGatewayCurrentPhysicalTestStringMap(value.Labels)
	result.PortBindings = cloneGatewayCurrentPhysicalTestPortBindings(value.PortBindings)
	result.Networks = make(map[string]*networkAttachment, len(value.Networks))
	for name, attachment := range value.Networks {
		copy := *attachment
		result.Networks[name] = &copy
	}
	return result
}

func cloneGatewayCurrentPhysicalTestRuntime(value gatewayContainerRuntime) gatewayContainerRuntime {
	result := value
	result.EffectivePortBindings = cloneGatewayCurrentPhysicalTestPortBindings(value.EffectivePortBindings)
	result.ConfiguredNetworks = make(map[string]gatewayV2ConfiguredNetwork, len(value.ConfiguredNetworks))
	for name, configured := range value.ConfiguredNetworks {
		copy := configured
		if configured.IPAMConfig != nil {
			ipam := *configured.IPAMConfig
			copy.IPAMConfig = &ipam
		}
		result.ConfiguredNetworks[name] = copy
	}
	return result
}

func cloneGatewayCurrentPhysicalTestPortBindings(value map[string][]map[string]string) map[string][]map[string]string {
	result := make(map[string][]map[string]string, len(value))
	for key, bindings := range value {
		result[key] = make([]map[string]string, len(bindings))
		for index, binding := range bindings {
			result[key][index] = cloneGatewayCurrentPhysicalTestStringMap(binding)
		}
	}
	return result
}

func cloneGatewayCurrentPhysicalTestStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func firstGatewayCurrentAppID(t *testing.T, state gatewayCurrentRouteState) string {
	t.Helper()
	for appID := range state.Apps {
		return appID
	}
	t.Fatal("current state has no app")
	return ""
}
