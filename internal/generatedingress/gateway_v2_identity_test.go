package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestClassifyGatewayV2TopologyExactStates(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)

	v1Only := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	if got := classifyGatewayV2Topology(source, state, journal, v1Only); got != gatewayTopologyExactV1Only {
		t.Fatalf("v1-only topology = %q", got)
	}

	staged := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
	if got := classifyGatewayV2Topology(source, state, journal, staged); got != gatewayTopologyExactV1WithStage {
		t.Fatalf("staged topology = %q", got)
	}

	final := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	if got := classifyGatewayV2Topology(source, state, journal, final); got != gatewayTopologyExactFinalV2 {
		t.Fatalf("final topology = %q", got)
	}
}

func TestClassifyGatewayV2TopologyFailsClosedOnDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*gatewayV2DockerObservation)
	}{
		{"missing pinned image", func(value *gatewayV2DockerObservation) { value.ImageFound = false }},
		{"v1 resource identity drift", func(value *gatewayV2DockerObservation) { value.V1NetworkID = "sha256:" + strings.Repeat("9", 64) }},
		{"v1 resources changed after probes", func(value *gatewayV2DockerObservation) { value.V1ResourcesStable = false }},
		{"v1 endpoint identity unproven", func(value *gatewayV2DockerObservation) { value.V1EndpointIdentityProven = false }},
		{"final endpoint identity unproven", func(value *gatewayV2DockerObservation) { value.FinalEndpointIdentityProven = false }},
		{"v2 resources changed after probes", func(value *gatewayV2DockerObservation) { value.V2ResourcesStable = false }},
		{"owned inventories changed after probes", func(value *gatewayV2DockerObservation) { value.OwnedInventoriesStable = false }},
		{"extra owned container", func(value *gatewayV2DockerObservation) {
			value.OwnedContainers = append(value.OwnedContainers, "extra")
		}},
		{"extra volume label", func(value *gatewayV2DockerObservation) { value.DataVolume.Labels["extra"] = "drift" }},
		{"network subnet drift", func(value *gatewayV2DockerObservation) { value.IngressNetwork.IPAM.Config[0].Subnet = "10.241.0.0/28" }},
		{"network extra container", func(value *gatewayV2DockerObservation) {
			value.IngressNetwork.Containers[strings.Repeat("e", 64)] = caddyNetworkContainerInspection{Name: "extra", IPv4Address: "10.240.0.3/28"}
		}},
		{"final label drift", func(value *gatewayV2DockerObservation) {
			value.FinalContainer.Labels[gatewayV2PlanDigestLabelKey] = strings.Repeat("f", 64)
		}},
		{"extra final binding", func(value *gatewayV2DockerObservation) {
			value.FinalContainer.PortBindings["9999/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "9999"}}
		}},
		{"effective final binding drift", func(value *gatewayV2DockerObservation) {
			value.FinalRuntime.EffectivePortBindings = map[string][]map[string]string{}
		}},
		{"final paused", func(value *gatewayV2DockerObservation) { value.FinalRuntime.Paused = true }},
		{"final dead", func(value *gatewayV2DockerObservation) { value.FinalRuntime.Dead = true }},
		{"final restarted", func(value *gatewayV2DockerObservation) { value.FinalRuntime.RestartCount = 1 }},
		{"missing persistent data mount", func(value *gatewayV2DockerObservation) { value.FinalContainer.Mounts = value.FinalContainer.Mounts[:1] }},
		{"unexpected final network", func(value *gatewayV2DockerObservation) {
			value.FinalContainer.Networks["foreign"] = &networkAttachment{}
		}},
		{"application network ownership drift", func(value *gatewayV2DockerObservation) {
			for name, network := range value.ApplicationNetworks {
				network.Labels["io.rig.application"] = "33333333-3333-4333-8333-333333333333"
				value.ApplicationNetworks[name] = network
				break
			}
		}},
		{"application network identity missing", func(value *gatewayV2DockerObservation) {
			for name := range value.ApplicationNetworkIDs {
				delete(value.ApplicationNetworkIDs, name)
				break
			}
		}},
		{"application network gateway membership missing", func(value *gatewayV2DockerObservation) {
			for name, network := range value.ApplicationNetworks {
				delete(network.Containers, value.FinalContainer.ID)
				value.ApplicationNetworks[name] = network
				break
			}
		}},
		{"application network gateway identity replaced", func(value *gatewayV2DockerObservation) {
			for name, network := range value.ApplicationNetworks {
				member := network.Containers[value.FinalContainer.ID]
				delete(network.Containers, value.FinalContainer.ID)
				network.Containers["sha256:"+strings.Repeat("f", 64)] = member
				value.ApplicationNetworks[name] = network
				break
			}
		}},
		{"live config absent", func(value *gatewayV2DockerObservation) { value.FinalConfig = nil }},
		{"live config drift", func(value *gatewayV2DockerObservation) {
			value.FinalConfig = []byte(`{"admin":{"listen":"localhost:2019"}}`)
		}},
		{"restart config drift", func(value *gatewayV2DockerObservation) {
			value.FinalRestartConfig = []byte(`{"admin":{"listen":"localhost:2019"}}`)
		}},
		{"404 policy unproven", func(value *gatewayV2DockerObservation) { value.Final404Proven = false }},
		{"application route unproven", func(value *gatewayV2DockerObservation) { value.FinalRoutesProven = false }},
		{"host publication unproven", func(value *gatewayV2DockerObservation) { value.FinalHostPublicationProven = false }},
		{"final changed after probes", func(value *gatewayV2DockerObservation) { value.FinalStable = false }},
		{"v1 still running", func(value *gatewayV2DockerObservation) { value.V1Container.Running = true }},
		{"v1 restart config absent", func(value *gatewayV2DockerObservation) { value.V1RestartConfig = nil }},
		{"v1 application network missing", func(value *gatewayV2DockerObservation) {
			for name := range value.V1ApplicationNetworks {
				delete(value.V1ApplicationNetworks, name)
				break
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, state, journal := gatewayV2IdentityTestState(t)
			value := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
			test.mutate(&value)
			if got := classifyGatewayV2Topology(source, state, journal, value); got != gatewayTopologyUnknownOrDrift {
				t.Fatalf("topology = %q", got)
			}
		})
	}
}

func TestClassifyGatewayV2StageFailsClosedWithoutRestartAndPublicationProof(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayV2DockerObservation)
	}{
		{"running v1 disk config diverges from live", func(value *gatewayV2DockerObservation) {
			value.V1RestartConfig = []byte(`{"admin":{"listen":"localhost:2019"}}`)
		}},
		{"restart config diverges from live", func(value *gatewayV2DockerObservation) {
			value.StageRestartConfig = []byte(`{"admin":{"listen":"localhost:2019"}}`)
		}},
		{"host publication is unproven", func(value *gatewayV2DockerObservation) { value.StageHostPublicationProven = false }},
		{"stage paused", func(value *gatewayV2DockerObservation) { value.StageRuntime.Paused = true }},
		{"stage restarted", func(value *gatewayV2DockerObservation) { value.StageRuntime.RestartCount = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, state, journal := gatewayV2IdentityTestState(t)
			value := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
			test.mutate(&value)
			if got := classifyGatewayV2Topology(source, state, journal, value); got != gatewayTopologyUnknownOrDrift {
				t.Fatalf("topology = %q", got)
			}
		})
	}
}

func TestGatewayV2StageConfigContainsOnlyBounded404Listeners(t *testing.T) {
	_, state, _ := gatewayV2IdentityTestState(t)
	body, err := buildGatewayV2StageConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	servers := config.Apps.HTTP.Servers
	if len(servers) != int(state.Profile.PortEnd-state.Profile.PortStart)+1 {
		t.Fatalf("server count = %d", len(servers))
	}
	if _, local := servers["generated"]; local {
		t.Fatal("stage config includes the local application listener")
	}
	for port := state.Profile.PortStart; ; port++ {
		server, exists := servers["lan_"+strconvForGatewayTest(port)]
		if !exists || len(server.Listen) != 1 || server.Listen[0] != state.Network.ContainerIPv4+":"+strconvForGatewayTest(port) ||
			len(server.Routes) != 1 || !reflect.DeepEqual(server.Routes[0], notFoundRoute()) {
			t.Fatalf("stage server %d = %#v", port, server)
		}
		if port == state.Profile.PortEnd {
			break
		}
	}
}

func TestGatewayV2ConfigProofBoundCoversNearCapacityV1State(t *testing.T) {
	routes := make(map[string]routeRecord, maxStateApps)
	for index := 0; index < maxStateApps; index++ {
		appID := fmt.Sprintf("%08x-0000-4000-8000-%012x", index+1, index+1)
		network := fmt.Sprintf("net-%02d-%s", index, strings.Repeat("n", 89))
		alias := fmt.Sprintf("alias-%02d-%s", index, strings.Repeat("a", 86))
		routes[appID] = routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
			{Component: strings.Repeat("s", 64), Role: "server", ContainerID: strings.Repeat("a", 64), NetworkName: network, NetworkAlias: alias, InternalPort: 65535},
			{Component: strings.Repeat("t", 64), Role: "static", ContainerID: strings.Repeat("b", 64), NetworkName: network, NetworkAlias: alias, InternalPort: 65534},
		}}
	}
	state := routeState{Version: stateVersion, Active: routes}
	for len(state.Active) > 0 {
		body, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) <= maxStateBytes {
			if len(body) < maxStateBytes-(2<<10) {
				t.Fatalf("fixture is not near the protected state bound: %d", len(body))
			}
			break
		}
		delete(state.Active, fmt.Sprintf("%08x-0000-4000-8000-%012x", len(state.Active), len(state.Active)))
	}
	if !validRouteState(state) {
		t.Fatal("near-capacity route state is invalid")
	}
	v1Config, err := buildCaddyConfig(state.Active, "10.203.0.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	_, preparation := upgradeTestPreparation(t)
	v2, _, err := prepareGatewayV2State(state, preparation)
	if err != nil {
		t.Fatal(err)
	}
	routeInputs, assignments := gatewayV2ConfigInputs(v2)
	v2Config, err := buildCaddyConfigV2(routeInputs, v2.Network.ContainerIPv4+":8080", caddyV2Profile{
		SelectedIPv4: v2.Profile.SelectedIPv4, PortStart: v2.Profile.PortStart, PortEnd: v2.Profile.PortEnd,
	}, assignments)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"v1": v1Config, "v2": v2Config} {
		if len(body) > gatewayV2MaxConfigBytes {
			t.Fatalf("%s config size = %d, bound = %d", name, len(body), gatewayV2MaxConfigBytes)
		}
		if len(body)+(4<<10) > defaultOutputLimit {
			t.Fatalf("%s config leaves insufficient docker-cp framing room: %d", name, len(body))
		}
	}
}

func TestInspectStoppedCaddyRestartConfigReadsOnlyExactSingleTarEntry(t *testing.T) {
	body := []byte(`{"admin":{"listen":"localhost:2019"}}`)
	runner := &gatewayV2TarRunner{body: body}
	manager := &Manager{runner: runner, options: Options{DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit}}
	got, err := manager.inspectStoppedCaddyRestartConfig(context.Background(), caddyContainerName, gatewayV2ActiveConfigFile)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("config = %q, err = %v", got, err)
	}
	if len(runner.requests) != 1 || !reflect.DeepEqual(runner.requests[0].Args, []string{"container", "cp", caddyContainerName + ":/config/active.json", "-"}) {
		t.Fatalf("requests = %#v", runner.requests)
	}

	runner.extra = true
	if _, err := manager.inspectStoppedCaddyRestartConfig(context.Background(), caddyContainerName, gatewayV2ActiveConfigFile); err == nil {
		t.Fatal("restart archive with an extra entry was accepted")
	}
	runner.extra = false
	runner.name = "other.json"
	if _, err := manager.inspectStoppedCaddyRestartConfig(context.Background(), caddyContainerName, gatewayV2ActiveConfigFile); err == nil {
		t.Fatal("restart archive with a different path was accepted")
	}
	runner.name = ""
	runner.runErr = errors.New("mounted path cannot be copied")
	if _, err := manager.inspectStoppedCaddyRestartConfig(context.Background(), caddyContainerName, gatewayV2ActiveConfigFile); err == nil {
		t.Fatal("restart config copy failure was accepted")
	}
}

func TestInspectLiveCaddyConfigBypassesCurlConfigAndProxy(t *testing.T) {
	runner := &gatewayV2ConfigRunner{}
	manager := &Manager{runner: runner, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
	}}
	containerID := "sha256:" + strings.Repeat("b", 64)
	if _, err := manager.inspectLiveCaddyConfig(context.Background(), containerID); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || !reflect.DeepEqual(runner.requests[0].Args, []string{
		"container", "exec", containerID, "curl", "--disable", "--silent", "--show-error", "--fail",
		"--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/",
	}) {
		t.Fatalf("admin config probe requests = %#v", runner.requests)
	}
}

func TestObserveGatewayMigrationTopologyMapsInspectionFailureToUnknown(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	runner := failingGatewayV2Runner{}
	manager := &Manager{runner: runner, options: Options{DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit}}
	if got := manager.observeGatewayMigrationTopology(context.Background(), source, state, journal); got != gatewayTopologyUnknownOrDrift {
		t.Fatalf("topology = %q", got)
	}
}

func gatewayV2IdentityTestState(t *testing.T) (routeState, gatewayV2RouteState, gatewayMigrationJournal) {
	t.Helper()
	source, preparation := upgradeTestPreparation(t)
	identityDigest, err := gatewayV1ObservedIdentityDigest(gatewayV2DockerObservation{
		Image:            gatewayV2IdentityTestImage(),
		V1Container:      caddyInspection{ID: "sha256:" + strings.Repeat("b", 64)},
		V1NetworkID:      "sha256:" + strings.Repeat("e", 64),
		V1VolumeIdentity: gatewayV1VolumeIdentity{Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data", CreatedAt: "2026-09-29T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	preparation.SourceIdentityDigest = identityDigest
	state, journal, err := prepareGatewayV2State(source, preparation)
	if err != nil {
		t.Fatal(err)
	}
	return source, state, journal
}

func gatewayV2IdentityTestImage() imageInspection {
	return imageInspection{ID: "sha256:" + strings.Repeat("a", 64), OS: "linux", RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}
}

func gatewayV2IdentityTestObservation(t *testing.T, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, topology gatewayObservedTopology) gatewayV2DockerObservation {
	t.Helper()
	imageID := "sha256:" + strings.Repeat("a", 64)
	v1ID := "sha256:" + strings.Repeat("b", 64)
	stageID := "sha256:" + strings.Repeat("c", 64)
	finalID := "sha256:" + strings.Repeat("d", 64)
	v1Subnet, v1Gateway, v1IP := ingressNetworkCandidate(0)
	v1Config, err := buildCaddyConfig(source.Active, v1IP+":8080")
	if err != nil {
		t.Fatal(err)
	}
	v1Networks := map[string]*networkAttachment{
		caddyNetworkName: {IPAddress: v1IP, GwPriority: caddyGatewayPriority},
		"net-a":          {IPAddress: "172.30.0.2"},
		"net-b":          {IPAddress: "172.31.0.2"},
	}
	observation := gatewayV2DockerObservation{
		Image: gatewayV2IdentityTestImage(), ImageFound: true,
		V1Container: gatewayV2IdentityTestContainer(caddyContainerName, v1ID, imageID, caddyNetworkName,
			map[string]string{"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1", "io.rig.listener-isolation": "v1"}, v1Networks,
			map[string][]map[string]string{"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": strconvForGatewayTest(journal.Source.LocalHostPort)}}},
			[]mountInspection{{Type: "volume", Name: caddyVolumeName, Destination: "/config", RW: true}}, gatewayV2FinalRestartPolicy, "/config/active.json"),
		V1ContainerFound: true,
		V1Volume: volumeInspection{Name: caddyVolumeName, Driver: "local", Scope: "local", Options: map[string]string{}, Labels: map[string]string{
			"io.rig.managed": "generated-ingress", "io.rig.identity-version": "v1",
		}}, V1VolumeFound: true,
		V1VolumeIdentity: gatewayV1VolumeIdentity{Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data", CreatedAt: "2026-09-29T00:00:00Z"},
		V1Network: caddyNetworkInspection{Name: caddyNetworkName, Driver: "bridge", Scope: "local", Options: map[string]string{},
			IPAM: caddyNetworkIPAM{Config: []networkIPAM{{Subnet: v1Subnet, Gateway: v1Gateway}}}, Labels: map[string]string{
				"io.rig.managed": "generated-ingress-network", "io.rig.identity-version": "v1",
			}, Containers: map[string]caddyNetworkContainerInspection{v1ID: {Name: caddyContainerName, IPv4Address: v1IP + "/28"}}},
		V1NetworkFound: true, V1NetworkID: "sha256:" + strings.Repeat("e", 64), V1Config: v1Config, V1RestartConfig: v1Config,
		V1ApplicationNetworkIDs: gatewayV2IdentityTestApplicationNetworkIDs(source.Active),
		V1Stable:                true, V1ResourcesStable: true, V1EndpointIdentityProven: true, V2ResourcesStable: true,
		StageStable: true, FinalStable: true, OwnedInventoriesStable: true,
		OwnedContainers: []string{}, OwnedVolumes: []string{}, OwnedNetworks: []string{},
	}
	observation.V1Container.Tmpfs = map[string]string{"/data": "rw,noexec,nosuid,nodev,size=67108864"}
	observation.V1Runtime.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(observation.V1Container.PortBindings)
	observation.V1ApplicationNetworks = gatewayV2IdentityTestApplicationNetworks(source.Active, observation.V1Container)

	if topology == gatewayTopologyExactV1Only {
		return observation
	}
	observation.ConfigVolume = gatewayV2IdentityTestVolume(state, journal, state.Identity.ConfigVolume, gatewayV2ConfigVolumeRole)
	observation.ConfigVolumeIdentity = gatewayV1VolumeIdentity{Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v2/_data", CreatedAt: "2026-09-29T00:01:00Z"}
	observation.ConfigVolumeFound = true
	observation.DataVolume = gatewayV2IdentityTestVolume(state, journal, state.Identity.DataVolume, gatewayV2DataVolumeRole)
	observation.DataVolumeIdentity = gatewayV1VolumeIdentity{Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-data-v2/_data", CreatedAt: "2026-09-29T00:02:00Z"}
	observation.DataVolumeFound = true
	observation.IngressNetworkID = "sha256:" + strings.Repeat("6", 64)
	observation.V2ResourcesStable = true
	observation.OwnedVolumes = []string{state.Identity.ConfigVolume, state.Identity.DataVolume}
	sort.Strings(observation.OwnedVolumes)
	observation.OwnedNetworks = []string{state.Identity.IngressNetwork}

	if topology == gatewayTopologyExactV1WithStage {
		observation.StageContainer = gatewayV2IdentityTestV2Container(state, journal, gatewayV2StageContainerRole, stageID, imageID)
		observation.StageContainerFound = true
		observation.OwnedContainers = []string{state.Identity.StageContainer}
		observation.IngressNetwork = gatewayV2IdentityTestNetwork(state, journal, stageID, state.Identity.StageContainer)
		observation.IngressFound = true
		observation.StageConfig, err = buildGatewayV2StageConfig(state)
		if err != nil {
			t.Fatal(err)
		}
		observation.StageRestartConfig = append([]byte(nil), observation.StageConfig...)
		observation.StageRuntime.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(observation.StageContainer.PortBindings)
		observation.Stage404Proven = true
		observation.StageHostPublicationProven = true
		observation.StageStable = true
		return observation
	}

	observation.V1Container.Running = false
	observation.V1Config = nil
	observation.FinalContainer = gatewayV2IdentityTestV2Container(state, journal, gatewayV2FinalContainerRole, finalID, imageID)
	observation.FinalContainerFound = true
	observation.OwnedContainers = []string{state.Identity.FinalContainer}
	observation.IngressNetwork = gatewayV2IdentityTestNetwork(state, journal, finalID, state.Identity.FinalContainer)
	observation.IngressFound = true
	observation.FinalRuntime.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(observation.FinalContainer.PortBindings)
	observation.ApplicationNetworks = gatewayV2IdentityTestApplicationNetworksV2(state, observation.FinalContainer)
	observation.ApplicationNetworkIDs = gatewayV2IdentityTestApplicationNetworkIDsV2(state)
	routes, assignments := gatewayV2ConfigInputs(state)
	observation.FinalConfig, err = buildCaddyConfigV2(routes, state.Network.ContainerIPv4+":8080", caddyV2Profile{
		SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd,
	}, assignments)
	if err != nil {
		t.Fatal(err)
	}
	observation.FinalRestartConfig = append([]byte(nil), observation.FinalConfig...)
	observation.Final404Proven = true
	observation.FinalRoutesProven = true
	observation.FinalHostPublicationProven = true
	observation.FinalEndpointIdentityProven = true
	observation.FinalStable = true
	return observation
}

func gatewayV2IdentityTestContainer(name, id, imageID, networkMode string, labels map[string]string,
	networks map[string]*networkAttachment, bindings map[string][]map[string]string, mounts []mountInspection, restart, config string,
) caddyInspection {
	return caddyInspection{
		ID: id, Name: "/" + name, Image: imageID, Labels: labels, Hostname: name, User: "1000:1000",
		Env: []string{"PATH=/usr/local/bin", "XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"}, Entrypoint: []string{caddyExecutable},
		Cmd: []string{"run", "--config", config}, ReadOnly: true, CapAdd: []string{caddyCapability}, CapDrop: []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"}, Mounts: mounts, Memory: 268435456, MemorySwap: 268435456,
		NanoCPUs: 1_000_000_000, PIDsLimit: 128, LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"},
		Restart: restart, NetworkMode: networkMode, Ulimits: []ulimitInspection{{Name: "nofile", Hard: 1024, Soft: 1024}},
		Running: true, PortBindings: bindings, Networks: networks,
	}
}

func gatewayV2IdentityTestV2Container(state gatewayV2RouteState, journal gatewayMigrationJournal, role, id, imageID string) caddyInspection {
	name, config, restart := state.Identity.StageContainer, "/config/"+state.Identity.StageConfigFilename, gatewayV2StageRestartPolicy
	networks := map[string]*networkAttachment{state.Identity.IngressNetwork: {IPAddress: state.Network.ContainerIPv4, GwPriority: caddyGatewayPriority}}
	if role == gatewayV2FinalContainerRole {
		name, config, restart = state.Identity.FinalContainer, "/config/"+state.Identity.ActiveConfigFilename, gatewayV2FinalRestartPolicy
		owners, _ := gatewayV2ApplicationNetworkOwners(state)
		for network := range owners {
			networks[network] = &networkAttachment{IPAddress: "172.30.0.2"}
		}
	}
	bindings := make(map[string][]map[string]string)
	for port := state.Profile.PortStart; ; port++ {
		bindings[strconvForGatewayTest(port)+"/tcp"] = []map[string]string{{"HostIp": state.Profile.SelectedIPv4, "HostPort": strconvForGatewayTest(port)}}
		if port == state.Profile.PortEnd {
			break
		}
	}
	if role == gatewayV2FinalContainerRole {
		bindings["8080/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconvForGatewayTest(journal.Source.LocalHostPort)}}
	}
	return gatewayV2IdentityTestContainer(name, id, imageID, state.Identity.IngressNetwork,
		gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true), networks, bindings,
		[]mountInspection{{Type: "volume", Name: state.Identity.ConfigVolume, Destination: "/config", RW: true},
			{Type: "volume", Name: state.Identity.DataVolume, Destination: "/data", RW: true}}, restart, config)
}

func gatewayV2IdentityTestVolume(state gatewayV2RouteState, journal gatewayMigrationJournal, name, role string) volumeInspection {
	return volumeInspection{Name: name, Driver: "local", Scope: "local", Options: map[string]string{},
		Labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, false)}
}

func gatewayV2IdentityTestNetwork(state gatewayV2RouteState, journal gatewayMigrationJournal, containerID, containerName string) caddyNetworkInspection {
	containers := map[string]caddyNetworkContainerInspection{}
	if containerID != "" {
		containers[containerID] = caddyNetworkContainerInspection{Name: containerName, IPv4Address: state.Network.ContainerIPv4 + "/28"}
	}
	return caddyNetworkInspection{Name: state.Identity.IngressNetwork, Driver: "bridge", Scope: "local", Options: map[string]string{},
		IPAM:   caddyNetworkIPAM{Config: []networkIPAM{{Subnet: state.Network.Subnet, Gateway: state.Network.GatewayIPv4}}},
		Labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false), Containers: containers}
}

func gatewayV2IdentityTestApplicationNetworks(routes map[string]routeRecord, container caddyInspection) map[string]caddyNetworkInspection {
	owners, _ := gatewayRouteNetworkOwners(routes)
	result := make(map[string]caddyNetworkInspection, len(owners))
	for name, appID := range owners {
		attachment := container.Networks[name]
		result[name] = caddyNetworkInspection{
			Name: name, Driver: "bridge", Scope: "local", Options: map[string]string{},
			Labels: map[string]string{"io.rig.managed": generatedruntime.NetworkOwnershipLabelValue, "io.rig.application": appID},
			Containers: map[string]caddyNetworkContainerInspection{container.ID: {
				Name: strings.TrimPrefix(container.Name, "/"), IPv4Address: attachment.IPAddress + "/24",
			}},
		}
	}
	return result
}

func gatewayV2IdentityTestApplicationNetworksV2(state gatewayV2RouteState, container caddyInspection) map[string]caddyNetworkInspection {
	routes, _ := gatewayV2ConfigInputs(state)
	return gatewayV2IdentityTestApplicationNetworks(routes, container)
}

func gatewayV2IdentityTestPortBindingsCopy(values map[string][]map[string]string) map[string][]map[string]string {
	result := make(map[string][]map[string]string, len(values))
	for port, bindings := range values {
		result[port] = make([]map[string]string, len(bindings))
		for index, binding := range bindings {
			result[port][index] = map[string]string{"HostIp": binding["HostIp"], "HostPort": binding["HostPort"]}
		}
	}
	return result
}

func gatewayV2IdentityTestApplicationNetworkIDs(routes map[string]routeRecord) map[string]string {
	owners, _ := gatewayRouteNetworkOwners(routes)
	result := make(map[string]string, len(owners))
	index := 7
	for name := range owners {
		result[name] = "sha256:" + strings.Repeat(strconv.Itoa(index), 64)
		index++
	}
	return result
}

func gatewayV2IdentityTestApplicationNetworkIDsV2(state gatewayV2RouteState) map[string]string {
	routes, _ := gatewayV2ConfigInputs(state)
	return gatewayV2IdentityTestApplicationNetworkIDs(routes)
}

func strconvForGatewayTest(value uint16) string { return strconv.Itoa(int(value)) }

type gatewayV2TarRunner struct {
	body     []byte
	name     string
	extra    bool
	runErr   error
	requests []runtimeprocess.CommandRequest
}

func (r *gatewayV2TarRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	if r.runErr != nil {
		return runtimeprocess.CommandResult{}, r.runErr
	}
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	name := r.name
	if name == "" {
		name = gatewayV2ActiveConfigFile
	}
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(r.body)), Typeflag: tar.TypeReg}); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if _, err := writer.Write(r.body); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if r.extra {
		if err := writer.WriteHeader(&tar.Header{Name: "extra", Mode: 0o600, Size: 2, Typeflag: tar.TypeReg}); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		if _, err := writer.Write([]byte("{}")); err != nil {
			return runtimeprocess.CommandResult{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return runtimeprocess.CommandResult{Stdout: output.Bytes()}, nil
}

type failingGatewayV2Runner struct{}

type gatewayV2ConfigRunner struct {
	requests []runtimeprocess.CommandRequest
}

func (r *gatewayV2ConfigRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	return runtimeprocess.CommandResult{Stdout: []byte("{}")}, nil
}

func (failingGatewayV2Runner) Run(_ context.Context, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{}, errors.New("inspection failed")
}
