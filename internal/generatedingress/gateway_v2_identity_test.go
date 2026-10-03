package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestClassifyGatewayV2TopologyExactStates(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)

	v1Only := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	if got := classifyGatewayV2Topology(source, state, journal, v1Only); got != gatewayTopologyExactV1Only {
		t.Fatalf("v1-only topology = %q", got)
	}
	unboundStage := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
	if got := classifyGatewayV2Topology(source, state, journal, unboundStage); got != gatewayTopologyUnknownOrDrift {
		t.Fatalf("unbound stage topology = %q", got)
	}

	stagedJournal := journal
	stagedJournal.Resources = gatewayV2IdentityTestBoundResources(t)
	stagedJournal.Resources.FinalContainerID = ""
	stagedJournal.Phase = gatewayPhaseStaged
	staged := gatewayV2IdentityTestObservation(t, source, state, stagedJournal, gatewayTopologyExactV1WithStage)
	if got := classifyGatewayV2Topology(source, state, stagedJournal, staged); got != gatewayTopologyExactV1WithStage {
		t.Fatalf("staged topology = %q", got)
	}

	finalJournal := journal
	finalJournal.Resources = gatewayV2IdentityTestBoundResources(t)
	finalJournal.Phase = gatewayPhaseV2Serving
	final := gatewayV2IdentityTestObservation(t, source, state, finalJournal, gatewayTopologyExactFinalV2)
	if got := classifyGatewayV2Topology(source, state, finalJournal, final); got != gatewayTopologyExactFinalV2 {
		t.Fatalf("final topology = %q", got)
	}
}

func TestGatewayV2RunningV1StillRequiresLivePublicationAndMembership(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	if !validGatewayV1Base(source, journal, observation, true) {
		t.Fatal("exact running-v1 source was rejected")
	}
	observation.V1Runtime.EffectivePortBindings = map[string][]map[string]string{"8080/tcp": nil}
	if validGatewayV1Base(source, journal, observation, true) {
		t.Fatal("running v1 without effective host publication was accepted")
	}
	observation = gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	observation.V1Network.Containers = map[string]caddyNetworkContainerInspection{}
	if validGatewayV1Base(source, journal, observation, true) {
		t.Fatal("running v1 without live ingress membership was accepted")
	}
	observation = gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	network := observation.V1ApplicationNetworks["net-a"]
	delete(network.Containers, observation.V1Container.ID)
	observation.V1ApplicationNetworks["net-a"] = network
	if validGatewayV1Base(source, journal, observation, true) {
		t.Fatal("running v1 without live application-network membership was accepted")
	}
}

func TestClassifyGatewayV2TopologyAcceptsUnboundExposedPorts(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseV2Serving
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	for _, effective := range []map[string][]map[string]string{
		observation.V1Runtime.EffectivePortBindings,
		observation.FinalRuntime.EffectivePortBindings,
	} {
		effective["80/tcp"] = nil
		effective["443/tcp"] = []map[string]string{}
		effective["443/udp"] = nil
		effective["2019/tcp"] = nil
	}
	if got := classifyGatewayV2Topology(source, state, journal, observation); got != gatewayTopologyExactFinalV2 {
		t.Fatalf("topology with unbound image-exposed ports = %q", got)
	}
}

func TestValidGatewayV2ContainerLabelsAllowsOnlyForeignImageMetadata(t *testing.T) {
	_, state, journal := gatewayV2IdentityTestState(t)
	expected := gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole, true)
	clone := func() map[string]string {
		labels := make(map[string]string, len(expected)+1)
		for key, value := range expected {
			labels[key] = value
		}
		return labels
	}

	if !validGatewayV2ContainerLabels(clone(), expected) {
		t.Fatal("exact Rig container labels were rejected")
	}
	foreign := clone()
	foreign["org.opencontainers.image.title"] = "Caddy"
	if !validGatewayV2ContainerLabels(foreign, expected) {
		t.Fatal("foreign pinned-image metadata label was rejected")
	}
	missing := clone()
	delete(missing, gatewayV2OperationLabelKey)
	if validGatewayV2ContainerLabels(missing, expected) {
		t.Fatal("missing required Rig label was accepted")
	}
	mismatch := clone()
	mismatch[gatewayV2OperationLabelKey] = "33333333-3333-4333-8333-333333333333"
	if validGatewayV2ContainerLabels(mismatch, expected) {
		t.Fatal("mismatched required Rig label was accepted")
	}
	extraRig := clone()
	extraRig["io.rig.unexpected"] = "metadata"
	if validGatewayV2ContainerLabels(extraRig, expected) {
		t.Fatal("additional Rig namespace label was accepted")
	}
}

func TestClassifyGatewayV2TopologyAcceptsForeignPinnedImageMetadataLabel(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseV2Serving
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	observation.FinalContainer.Labels["org.opencontainers.image.title"] = "Caddy"
	if got := classifyGatewayV2Topology(source, state, journal, observation); got != gatewayTopologyExactFinalV2 {
		t.Fatalf("topology with foreign pinned-image metadata label = %q", got)
	}
}

func TestClassifyCommittedFinalV2DoesNotDependOnHistoricalV1AppTopology(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseCommitted
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	observation.V1EndpointIdentityProven = false
	observation.V1ApplicationNetworks = map[string]caddyNetworkInspection{}
	observation.V1ApplicationNetworkIDs = map[string]string{}
	for name := range observation.V1Container.Networks {
		if name != caddyNetworkName {
			delete(observation.V1Container.Networks, name)
			delete(observation.V1Runtime.ConfiguredNetworks, name)
		}
	}
	if got := classifyGatewayV2Topology(source, state, journal, observation); got != gatewayTopologyExactFinalV2 {
		t.Fatalf("committed final topology = %q", got)
	}

	journal.Phase = gatewayPhaseV2Serving
	if got := classifyGatewayV2Topology(source, state, journal, observation); got != gatewayTopologyUnknownOrDrift {
		t.Fatalf("rollback-capable final topology without v1 endpoint proof = %q", got)
	}
}

func TestClassifyGatewayV2MixedRestartRequiresProposedLiveAndCommittedRestartConfig(t *testing.T) {
	source, committed, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseCommitted
	proposed := cloneGatewayV2RouteState(committed)
	app := proposed.Apps[upgradeTestAppA]
	app.Route = routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
		endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
	}}
	proposed.Apps[upgradeTestAppA] = app
	if !validGatewayV2MixedRestartInputs(source, committed, proposed, journal) {
		t.Fatal("mixed restart fixture inputs are invalid")
	}
	multiple := cloneGatewayV2RouteState(proposed)
	other := multiple.Apps[upgradeTestAppB]
	other.Route = routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", "net-b", "web-blue", 3000, 'f'),
	}}
	multiple.Apps[upgradeTestAppB] = other
	if validGatewayV2MixedRestartInputs(source, committed, multiple, journal) {
		t.Fatal("multiple app route changes were accepted as one pending transition")
	}
	sameSlot := cloneGatewayV2RouteState(committed)
	sameSlotApp := sameSlot.Apps[upgradeTestAppA]
	sameSlotApp.Route.Endpoints[0].NetworkAlias = "frontend-blue-replaced"
	sameSlot.Apps[upgradeTestAppA] = sameSlotApp
	if validGatewayV2MixedRestartInputs(source, committed, sameSlot, journal) {
		t.Fatal("same-slot endpoint change was accepted as a route transition")
	}
	observation := gatewayV2IdentityTestObservation(t, source, proposed, journal, gatewayTopologyExactFinalV2)
	committedConfig, err := expectedGatewayV2FinalConfig(committed)
	if err != nil {
		t.Fatal(err)
	}
	observation.FinalRestartConfig = committedConfig
	if !classifyGatewayV2MixedRestart(source, committed, proposed, journal, observation) {
		t.Fatal("exact proposed-live/committed-restart topology was rejected")
	}

	wrongLive := observation
	wrongLive.FinalConfig = append([]byte(nil), committedConfig...)
	if classifyGatewayV2MixedRestart(source, committed, proposed, journal, wrongLive) {
		t.Fatal("committed live config was accepted as the mixed restart window")
	}
	unknown := observation
	unknown.FinalHostPublicationProven = false
	if classifyGatewayV2MixedRestart(source, committed, proposed, journal, unknown) {
		t.Fatal("unproven host publication was accepted as the mixed restart window")
	}
	if classifyGatewayV2MixedRestart(source, committed, committed, journal, observation) {
		t.Fatal("identical committed and proposed states were accepted as mixed")
	}

	manager := &Manager{runner: failingGatewayV2Runner{}, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
	}}
	if manager.observeGatewayV2MixedRestart(context.Background(), source, committed, proposed, journal) {
		t.Fatal("inspection failure was accepted as the mixed restart window")
	}
}

func TestClassifyGatewayV2MixedRestartAcceptsOnlyExactLANGrantTransition(t *testing.T) {
	source, committed, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseCommitted
	proposed := cloneGatewayV2RouteState(committed)
	bindLANForTest(t, &proposed, upgradeTestAppA, 8100)
	if !validGatewayV2MixedRestartInputs(source, committed, proposed, journal) {
		t.Fatal("exact same-route nil-to-binding LAN transition was rejected")
	}

	observation := gatewayV2IdentityTestObservation(t, source, proposed, journal, gatewayTopologyExactFinalV2)
	committedConfig, err := expectedGatewayV2FinalConfig(committed)
	if err != nil {
		t.Fatal(err)
	}
	observation.FinalRestartConfig = committedConfig
	if !classifyGatewayV2MixedRestart(source, committed, proposed, journal, observation) {
		t.Fatal("exact live LAN grant with committed restart config was not classified as recoverable mixed topology")
	}

	changedRoute := cloneGatewayV2RouteState(proposed)
	app := changedRoute.Apps[upgradeTestAppA]
	app.Route.Slot = generatedruntime.SlotGreen
	changedRoute.Apps[upgradeTestAppA] = app
	if validGatewayV2MixedRestartInputs(source, committed, changedRoute, journal) {
		t.Fatal("combined route switch and LAN grant was accepted")
	}
	movedBinding := cloneGatewayV2RouteState(proposed)
	app = movedBinding.Apps[upgradeTestAppA]
	app.LAN.Port = 8101
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: upgradeTestAppA, AllocationID: app.LAN.AllocationID, Port: app.LAN.Port,
		GatewayProfileRevisionID:     app.LAN.ProfileRevisionID,
		GatewayProfileRevisionNumber: app.LAN.ProfileRevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.LAN.AccessSpecDigest = digest
	movedBinding.Apps[upgradeTestAppA] = app
	if !validGatewayV2MixedRestartInputs(source, committed, movedBinding, journal) {
		t.Fatal("a valid first grant at another pool port should remain recoverable")
	}
	wrongLive := observation
	wrongLive.FinalConfig = committedConfig
	if classifyGatewayV2MixedRestart(source, committed, proposed, journal, wrongLive) {
		t.Fatal("committed live 404 config was accepted as an applied LAN grant")
	}
}

func TestClassifyGatewayV2TopologyRejectsDurablePendingRoute(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseCommitted
	previous := cloneGatewayV2AppRoute(state.Apps[upgradeTestAppA])
	proposed := gatewayV2AppRoute{Route: routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
		endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
	}}}
	state.Pending = &gatewayV2PendingRoute{AppID: upgradeTestAppA, Previous: &previous, Proposed: proposed}
	if !validGatewayV2RouteState(state) {
		t.Fatal("pending route fixture is invalid")
	}
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	if got := classifyGatewayV2Topology(source, state, journal, observation); got != gatewayTopologyUnknownOrDrift {
		t.Fatalf("topology with durable pending route = %q", got)
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
		{"bound ingress network ID changed", func(value *gatewayV2DockerObservation) { value.IngressNetworkID = "sha256:" + strings.Repeat("9", 64) }},
		{"bound config volume replaced", func(value *gatewayV2DockerObservation) { value.ConfigVolumeIdentity.CreatedAt = "2026-09-29T00:03:00Z" }},
		{"bound final container ID changed", func(value *gatewayV2DockerObservation) { value.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64) }},
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
		{"unexpected effective final binding", func(value *gatewayV2DockerObservation) {
			value.FinalRuntime.EffectivePortBindings["9999/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "9999"}}
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
		{"configured application network identity replaced", func(value *gatewayV2DockerObservation) {
			for name := range value.ApplicationNetworkIDs {
				attachment := value.FinalRuntime.ConfiguredNetworks[name]
				attachment.NetworkID = "sha256:" + strings.Repeat("9", 64)
				value.FinalRuntime.ConfiguredNetworks[name] = attachment
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
			journal.Resources = gatewayV2IdentityTestBoundResources(t)
			journal.Phase = gatewayPhaseV2Serving
			value := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
			if got := classifyGatewayV2Topology(source, state, journal, value); got != gatewayTopologyExactFinalV2 {
				t.Fatalf("unmodified final topology = %q", got)
			}
			test.mutate(&value)
			if got := classifyGatewayV2Topology(source, state, journal, value); got != gatewayTopologyUnknownOrDrift {
				t.Fatalf("topology = %q", got)
			}
		})
	}
}

func TestGatewayV2EffectivePortBindingsMatchConfigured(t *testing.T) {
	configured := map[string][]map[string]string{
		"8080/tcp": {{"HostIp": "192.168.1.20", "HostPort": "20481"}},
	}
	tests := []struct {
		name      string
		effective map[string][]map[string]string
		want      bool
	}{
		{
			name:      "exact configured publication",
			effective: gatewayV2IdentityTestPortBindingsCopy(configured),
			want:      true,
		},
		{
			name: "additional exposed ports without host bindings",
			effective: map[string][]map[string]string{
				"80/tcp":   nil,
				"443/tcp":  {},
				"443/udp":  nil,
				"2019/tcp": nil,
				"8080/tcp": {{"HostIp": "192.168.1.20", "HostPort": "20481"}},
			},
			want: true,
		},
		{
			name:      "configured publication absent",
			effective: map[string][]map[string]string{"80/tcp": nil},
			want:      false,
		},
		{
			name: "configured host address changed",
			effective: map[string][]map[string]string{
				"8080/tcp": {{"HostIp": "0.0.0.0", "HostPort": "20481"}},
			},
			want: false,
		},
		{
			name: "configured host port changed",
			effective: map[string][]map[string]string{
				"8080/tcp": {{"HostIp": "192.168.1.20", "HostPort": "20482"}},
			},
			want: false,
		},
		{
			name: "configured publication duplicated",
			effective: map[string][]map[string]string{
				"8080/tcp": {
					{"HostIp": "192.168.1.20", "HostPort": "20481"},
					{"HostIp": "192.168.1.20", "HostPort": "20481"},
				},
			},
			want: false,
		},
		{
			name: "unexpected effective publication",
			effective: map[string][]map[string]string{
				"8080/tcp": {{"HostIp": "192.168.1.20", "HostPort": "20481"}},
				"9999/tcp": {{"HostIp": "127.0.0.1", "HostPort": "9999"}},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := gatewayV2EffectivePortBindingsMatchConfigured(test.effective, configured); got != test.want {
				t.Fatalf("gatewayV2EffectivePortBindingsMatchConfigured() = %t, want %t", got, test.want)
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
		{"bound stage ID changed", func(value *gatewayV2DockerObservation) { value.StageContainer.ID = "sha256:" + strings.Repeat("9", 64) }},
		{"stage hostname changed to overlong container name", func(value *gatewayV2DockerObservation) {
			value.StageContainer.Hostname = strings.TrimPrefix(value.StageContainer.Name, "/")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, state, journal := gatewayV2IdentityTestState(t)
			journal.Resources = gatewayV2IdentityTestBoundResources(t)
			journal.Resources.FinalContainerID = ""
			journal.Phase = gatewayPhaseStaged
			value := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
			if got := classifyGatewayV2Topology(source, state, journal, value); got != gatewayTopologyExactV1WithStage {
				t.Fatalf("unmodified stage topology = %q", got)
			}
			test.mutate(&value)
			if got := classifyGatewayV2Topology(source, state, journal, value); got != gatewayTopologyUnknownOrDrift {
				t.Fatalf("topology = %q", got)
			}
		})
	}
}

func TestStableGatewayV2StageContainerAcceptsOnlyExactMountOrderChange(t *testing.T) {
	identity := gatewayV2Identity{ConfigVolume: "config-volume", DataVolume: "data-volume"}
	first := caddyInspection{ID: "stage-id", Hostname: "stage-host", Running: false, Mounts: []mountInspection{
		{Type: "volume", Name: identity.ConfigVolume, Destination: "/config", RW: true},
		{Type: "volume", Name: identity.DataVolume, Destination: "/data", RW: true},
	}}
	runtime := gatewayContainerRuntime{EffectivePortBindings: map[string][]map[string]string{}}
	copyStage := func() caddyInspection {
		value := first
		value.Mounts = append([]mountInspection(nil), first.Mounts...)
		return value
	}
	if !stableGatewayV2StageContainer(first, copyStage(), runtime, runtime, identity) {
		t.Fatal("identical stage reads were rejected")
	}
	reordered := copyStage()
	reordered.Mounts[0], reordered.Mounts[1] = reordered.Mounts[1], reordered.Mounts[0]
	if !stableGatewayV2StageContainer(first, reordered, runtime, runtime, identity) {
		t.Fatal("exact mounts in reverse Docker order were rejected")
	}
	if first.Mounts[0].Destination != "/config" || reordered.Mounts[0].Destination != "/data" {
		t.Fatal("comparison mutated either observed mount slice")
	}
	for _, test := range []struct {
		name   string
		mutate func(*caddyInspection)
	}{
		{"mount write mode", func(value *caddyInspection) { value.Mounts[0].RW = false }},
		{"mount identity", func(value *caddyInspection) { value.Mounts[0].Name = "replaced" }},
		{"mount type", func(value *caddyInspection) { value.Mounts[0].Type = "bind" }},
		{"mount destination", func(value *caddyInspection) { value.Mounts[0].Destination = "/elsewhere" }},
		{"missing mount", func(value *caddyInspection) { value.Mounts = value.Mounts[:1] }},
		{"extra mount", func(value *caddyInspection) { value.Mounts = append(value.Mounts, value.Mounts[0]) }},
		{"container ID", func(value *caddyInspection) { value.ID = "replaced" }},
		{"hostname", func(value *caddyInspection) { value.Hostname = "replaced" }},
		{"running state", func(value *caddyInspection) { value.Running = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			confirmed := copyStage()
			test.mutate(&confirmed)
			if stableGatewayV2StageContainer(first, confirmed, runtime, runtime, identity) {
				t.Fatal("changed stage read was accepted")
			}
		})
	}
	changedRuntime := gatewayContainerRuntime{EffectivePortBindings: map[string][]map[string]string{
		"8080/tcp": {{"HostIp": "127.0.0.1", "HostPort": "8080"}},
	}}
	if stableGatewayV2StageContainer(first, reordered, runtime, changedRuntime, identity) {
		t.Fatal("changed runtime ports were accepted")
	}
}

func TestGatewayV2StageConfigContainsOnlyBounded404Listeners(t *testing.T) {
	_, state, _ := gatewayV2IdentityTestState(t)
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
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
			len(server.Routes) != 2 || !reflect.DeepEqual(server.Routes[0], gatewayV2ProbeRoute(state.Profile.SelectedIPv4, gatewayV2PortChallenge(challenge, port))) ||
			!reflect.DeepEqual(server.Routes[1], notFoundRoute()) {
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

func TestInspectGatewayEndpointIdentitySnapshotProvesUniqueAliasAndStableIdentity(t *testing.T) {
	routes, networks, inspections := gatewayV2EndpointIdentityFixture()
	runner := &gatewayV2EndpointIdentityRunner{inspections: inspections}
	manager := &Manager{runner: runner, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
	}}
	first, err := manager.inspectGatewayEndpointIdentitySnapshot(context.Background(), routes, networks)
	if err != nil || first == "" {
		t.Fatalf("snapshot = %q, err = %v", first, err)
	}
	second, err := manager.inspectGatewayEndpointIdentitySnapshot(context.Background(), routes, networks)
	if err != nil || second != first {
		t.Fatalf("stable snapshot = %q, err = %v, want %q", second, err, first)
	}
	if len(runner.requests) != 4 {
		t.Fatalf("container inspections = %d, want each of two immutable members twice", len(runner.requests))
	}

	otherID := "sha256:" + strings.Repeat("2", 64)
	other := runner.inspections[normalizeID(otherID)]
	other.Networks["net-a"].Aliases = []string{"other", "frontend-blue"}
	runner.inspections[normalizeID(otherID)] = other
	if _, err := manager.inspectGatewayEndpointIdentitySnapshot(context.Background(), routes, networks); err == nil {
		t.Fatal("duplicate endpoint alias was accepted")
	}
}

func TestInspectGatewayEndpointIdentitySnapshotFailsClosedOnEndpointDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]endpointInspection)
	}{
		{"unhealthy endpoint", func(values map[string]endpointInspection) {
			endpoint := values[strings.Repeat("1", 64)]
			endpoint.Health = "unhealthy"
			values[strings.Repeat("1", 64)] = endpoint
		}},
		{"ownership label drift", func(values map[string]endpointInspection) {
			endpoint := values[strings.Repeat("1", 64)]
			endpoint.Labels["io.rig.application"] = "33333333-3333-4333-8333-333333333333"
			values[strings.Repeat("1", 64)] = endpoint
		}},
		{"immutable identity replaced", func(values map[string]endpointInspection) {
			endpoint := values[strings.Repeat("1", 64)]
			endpoint.ID = "sha256:" + strings.Repeat("3", 64)
			values[strings.Repeat("1", 64)] = endpoint
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			routes, networks, inspections := gatewayV2EndpointIdentityFixture()
			test.mutate(inspections)
			manager := &Manager{runner: &gatewayV2EndpointIdentityRunner{inspections: inspections}, options: Options{
				DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
			}}
			if _, err := manager.inspectGatewayEndpointIdentitySnapshot(context.Background(), routes, networks); err == nil {
				t.Fatal("endpoint drift was accepted")
			}
		})
	}
}

func TestGatewayV2HostPublicationProofUsesSelectedAddressAndRejectsLoopback(t *testing.T) {
	_, state, _ := gatewayV2IdentityTestState(t)
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	caddyID := "sha256:" + strings.Repeat("d", 64)
	var calls int
	probe := func(_ context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
		calls++
		if port < state.Profile.PortStart || port > state.Profile.PortEnd {
			t.Fatalf("port = %d", port)
		}
		if address == "127.0.0.1" {
			return gatewayV2HostProbeResult{}
		}
		if address != state.Profile.SelectedIPv4 || (host != state.Profile.SelectedIPv4 && host != "wrong.invalid") {
			t.Fatalf("probe = %s:%d Host %q", address, port, host)
		}
		result := gatewayV2HostProbeResult{Status: http.StatusNotFound, Connected: true, Responded: true}
		portChallenge := gatewayV2PortChallenge(challenge, port)
		if path == gatewayV2ChallengePathPrefix+portChallenge {
			result.Body = gatewayV2ChallengeBodyPrefix + portChallenge
		} else if path != "/" {
			t.Fatalf("path = %q", path)
		}
		return result
	}
	containerCalls := 0
	containerProbe := func(_ context.Context, gotID, address string, port uint16, host, token string) bool {
		containerCalls++
		return gotID == caddyID && address == state.Network.ContainerIPv4 && port >= state.Profile.PortStart && port <= state.Profile.PortEnd &&
			host == state.Profile.SelectedIPv4 && token == gatewayV2PortChallenge(challenge, port)
	}
	if !proveGatewayV2StageHostPublication(context.Background(), state, caddyID, probe, containerProbe) {
		t.Fatal("exact stage host publication was rejected")
	}
	stageCalls := calls
	stageContainerCalls := containerCalls
	if !proveGatewayV2FinalHostPublication(context.Background(), state, caddyID, probe, containerProbe) {
		t.Fatal("exact final host publication was rejected")
	}
	wantPorts := int(state.Profile.PortEnd-state.Profile.PortStart) + 1
	wantPerProof := 4 * wantPorts
	if stageCalls != wantPerProof || calls-stageCalls != wantPerProof {
		t.Fatalf("stage calls = %d, final calls = %d, want %d each", stageCalls, calls-stageCalls, wantPerProof)
	}
	if stageContainerCalls != wantPorts || containerCalls-stageContainerCalls != wantPorts {
		t.Fatalf("stage container calls = %d, final container calls = %d, want %d each", stageContainerCalls, containerCalls-stageContainerCalls, wantPorts)
	}

	broadProbe := func(_ context.Context, _ string, _ uint16, _ string, path string) gatewayV2HostProbeResult {
		result := gatewayV2HostProbeResult{Status: http.StatusNotFound, Connected: true, Responded: true}
		if token := strings.TrimPrefix(path, gatewayV2ChallengePathPrefix); path == gatewayV2ChallengePathPrefix+token && validSHA256(token) {
			result.Body = gatewayV2ChallengeBodyPrefix + token
		}
		return result
	}
	if proveGatewayV2StageHostPublication(context.Background(), state, caddyID, broadProbe, containerProbe) {
		t.Fatal("stage publication reachable through loopback was accepted")
	}
	if proveGatewayV2FinalHostPublication(context.Background(), state, caddyID, broadProbe, containerProbe) {
		t.Fatal("final publication reachable through loopback was accepted")
	}
	connectedWithoutHTTP := func(_ context.Context, address string, _ uint16, _ string, path string) gatewayV2HostProbeResult {
		if address == "127.0.0.1" {
			return gatewayV2HostProbeResult{Connected: true}
		}
		result := gatewayV2HostProbeResult{Status: http.StatusNotFound, Connected: true, Responded: true}
		if token := strings.TrimPrefix(path, gatewayV2ChallengePathPrefix); path == gatewayV2ChallengePathPrefix+token && validSHA256(token) {
			result.Body = gatewayV2ChallengeBodyPrefix + token
		}
		return result
	}
	if proveGatewayV2StageHostPublication(context.Background(), state, caddyID, connectedWithoutHTTP, containerProbe) {
		t.Fatal("stage publication with a raw loopback listener was accepted")
	}
	generic404 := func(_ context.Context, _ string, _ uint16, _ string, _ string) gatewayV2HostProbeResult {
		return gatewayV2HostProbeResult{Status: http.StatusNotFound, Connected: true, Responded: true}
	}
	if proveGatewayV2StageHostPublication(context.Background(), state, caddyID, generic404, containerProbe) {
		t.Fatal("unrelated generic 404 listener was accepted as the stage gateway")
	}
	swappedPortProbe := func(_ context.Context, _ string, port uint16, _ string, path string) gatewayV2HostProbeResult {
		otherPort := port + 1
		if port == state.Profile.PortEnd {
			otherPort = state.Profile.PortStart
		}
		otherChallenge := gatewayV2PortChallenge(challenge, otherPort)
		return gatewayV2HostProbeResult{
			Status: http.StatusNotFound, Body: gatewayV2ChallengeBodyPrefix + otherChallenge,
			Connected: true, Responded: strings.HasPrefix(path, gatewayV2ChallengePathPrefix),
		}
	}
	if proveGatewayV2StageHostPublication(context.Background(), state, caddyID, swappedPortProbe, containerProbe) {
		t.Fatal("challenge response from a different LAN port was accepted")
	}
}

func TestProbeGatewayV2HostStatusOriginatesDirectHTTP(t *testing.T) {
	challenge := strings.Repeat("a", 64)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != "wrong.invalid" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.URL.Path == gatewayV2ChallengePathPrefix+challenge {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(gatewayV2ChallengeBodyPrefix + challenge))
			return
		}
		if request.URL.Path != "/" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	server.Start()
	address, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	result := probeGatewayV2HostStatus(context.Background(), address, uint16(portValue), "wrong.invalid", "/")
	if !result.Connected || !result.Responded || result.Status != http.StatusNotFound {
		t.Fatalf("result = %#v", result)
	}
	challengeResult := probeGatewayV2HostStatus(context.Background(), address, uint16(portValue), "wrong.invalid", gatewayV2ChallengePathPrefix+challenge)
	if !challengeResult.Connected || !challengeResult.Responded || challengeResult.Status != http.StatusNotFound || challengeResult.Body != gatewayV2ChallengeBodyPrefix+challenge {
		t.Fatalf("challenge result = %#v", challengeResult)
	}
	server.Close()
	if result := probeGatewayV2HostStatus(context.Background(), address, uint16(portValue), "wrong.invalid", "/"); result.Connected || result.Responded {
		t.Fatalf("closed host listener result = %#v", result)
	}
}

func TestProbeGatewayV2ContainerChallengeRequiresExactAttestedResponse(t *testing.T) {
	_, state, _ := gatewayV2IdentityTestState(t)
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	caddyID := "sha256:" + strings.Repeat("d", 64)
	runner := &gatewayV2ChallengeRunner{stdout: []byte(gatewayV2ChallengeBodyPrefix + challenge + "\n404")}
	manager := &Manager{runner: runner, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
	}}
	if !manager.probeGatewayV2ContainerChallenge(context.Background(), caddyID, state.Network.ContainerIPv4, state.Profile.PortStart, state.Profile.SelectedIPv4, challenge) {
		t.Fatal("exact immutable-container challenge response was rejected")
	}
	if len(runner.requests) != 1 {
		t.Fatalf("requests = %d", len(runner.requests))
	}
	request := runner.requests[0]
	if request.Args[0] != "container" || request.Args[1] != "exec" || request.Args[2] != caddyID ||
		!containsString(request.Args, "Host: "+state.Profile.SelectedIPv4) ||
		request.Args[len(request.Args)-1] != "http://"+state.Network.ContainerIPv4+":"+strconvForGatewayTest(state.Profile.PortStart)+gatewayV2ChallengePathPrefix+challenge {
		t.Fatalf("challenge request = %#v", request.Args)
	}
	runner.stdout = []byte("\n404")
	if manager.probeGatewayV2ContainerChallenge(context.Background(), caddyID, state.Network.ContainerIPv4, state.Profile.PortStart, state.Profile.SelectedIPv4, challenge) {
		t.Fatal("generic container 404 was accepted as the gateway challenge")
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

func gatewayV2IdentityTestBoundResources(t *testing.T) gatewayV2ResourceBindings {
	t.Helper()
	config, err := newGatewayV2VolumeResourceBinding(gatewayV1VolumeIdentity{
		Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v2/_data", CreatedAt: "2026-09-29T00:01:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := newGatewayV2VolumeResourceBinding(gatewayV1VolumeIdentity{
		Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-data-v2/_data", CreatedAt: "2026-09-29T00:02:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return gatewayV2ResourceBindings{
		ImageID: strings.Repeat("a", 64), IngressNetworkID: strings.Repeat("6", 64),
		ConfigVolume: config, DataVolume: data,
		StageContainerID: strings.Repeat("c", 64), FinalContainerID: strings.Repeat("d", 64),
	}
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
		observation.StageRuntime.ConfiguredNetworks = gatewayV2IdentityTestConfiguredNetworks(state, observation.StageContainer,
			observation.IngressNetworkID, nil)
		observation.Stage404Proven = true
		observation.StageHostPublicationProven = true
		observation.StageStable = true
		return observation
	}

	stopGatewayV1IdentityTestContainer(&observation)
	observation.FinalContainer = gatewayV2IdentityTestV2Container(state, journal, gatewayV2FinalContainerRole, finalID, imageID)
	observation.FinalContainerFound = true
	observation.OwnedContainers = []string{state.Identity.FinalContainer}
	observation.IngressNetwork = gatewayV2IdentityTestNetwork(state, journal, finalID, state.Identity.FinalContainer)
	observation.IngressFound = true
	observation.FinalRuntime.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(observation.FinalContainer.PortBindings)
	observation.ApplicationNetworks = gatewayV2IdentityTestApplicationNetworksV2(state, observation.FinalContainer)
	observation.ApplicationNetworkIDs = gatewayV2IdentityTestApplicationNetworkIDsV2(state)
	observation.FinalRuntime.ConfiguredNetworks = gatewayV2IdentityTestConfiguredNetworks(state, observation.FinalContainer,
		observation.IngressNetworkID, observation.ApplicationNetworkIDs)
	routes, assignments := gatewayV2ConfigInputs(state)
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal(err)
	}
	observation.FinalConfig, err = buildCaddyConfigV2(routes, state.Network.ContainerIPv4+":8080", caddyV2Profile{
		SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd,
		ProbeToken: challenge,
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
	container := gatewayV2IdentityTestContainer(name, id, imageID, state.Identity.IngressNetwork,
		gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true), networks, bindings,
		[]mountInspection{{Type: "volume", Name: state.Identity.ConfigVolume, Destination: "/config", RW: true},
			{Type: "volume", Name: state.Identity.DataVolume, Destination: "/data", RW: true}}, restart, config)
	container.Hostname = gatewayV2ExpectedHostname(state, role)
	return container
}

func gatewayV2IdentityTestConfiguredNetworks(state gatewayV2RouteState, container caddyInspection, ingressNetworkID string,
	applicationNetworkIDs map[string]string,
) map[string]gatewayV2ConfiguredNetwork {
	result := make(map[string]gatewayV2ConfiguredNetwork, len(container.Networks))
	for name := range container.Networks {
		networkID := applicationNetworkIDs[name]
		var ipam *gatewayV2ConfiguredIPAM
		priority := 0
		if name == state.Identity.IngressNetwork {
			networkID = ingressNetworkID
			ipam = &gatewayV2ConfiguredIPAM{IPv4Address: state.Network.ContainerIPv4}
			priority = caddyGatewayPriority
		}
		result[name] = gatewayV2ConfiguredNetwork{IPAMConfig: ipam, NetworkID: networkID, GwPriority: priority}
	}
	return result
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

func stopGatewayV1IdentityTestContainer(observation *gatewayV2DockerObservation) {
	observation.V1Container.Running = false
	observation.V1Config = nil
	observation.V1Runtime.EffectivePortBindings = map[string][]map[string]string{"8080/tcp": nil}
	configured := make(map[string]gatewayV2ConfiguredNetwork, len(observation.V1Container.Networks))
	for name, attachment := range observation.V1Container.Networks {
		stopped := *attachment
		stopped.IPAddress = ""
		observation.V1Container.Networks[name] = &stopped
		if name == caddyNetworkName {
			address, _ := ingressNetworkIdentity(observation.V1Network.identity())
			configured[name] = gatewayV2ConfiguredNetwork{
				IPAMConfig: &gatewayV2ConfiguredIPAM{IPv4Address: address},
				NetworkID:  observation.V1NetworkID, GwPriority: caddyGatewayPriority,
			}
			continue
		}
		configured[name] = gatewayV2ConfiguredNetwork{NetworkID: observation.V1ApplicationNetworkIDs[name]}
	}
	observation.V1Runtime.ConfiguredNetworks = configured
	observation.V1Network.Containers = map[string]caddyNetworkContainerInspection{}
	for name, network := range observation.V1ApplicationNetworks {
		delete(network.Containers, observation.V1Container.ID)
		observation.V1ApplicationNetworks[name] = network
	}
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

func gatewayV2EndpointIdentityFixture() (map[string]routeRecord, map[string]caddyNetworkInspection, map[string]endpointInspection) {
	const appID = "11111111-1111-4111-8111-111111111111"
	endpointID := "sha256:" + strings.Repeat("1", 64)
	otherID := "sha256:" + strings.Repeat("2", 64)
	routes := map[string]routeRecord{appID: {
		Slot: generatedruntime.SlotBlue,
		Endpoints: []generatedruntime.RouteEndpoint{{
			Component: "frontend", Role: "static", ContainerID: endpointID, NetworkName: "net-a", NetworkAlias: "frontend-blue", InternalPort: 4173,
		}},
	}}
	networks := map[string]caddyNetworkInspection{"net-a": {
		Name: "net-a",
		Containers: map[string]caddyNetworkContainerInspection{
			endpointID: {Name: "frontend", IPv4Address: "172.30.0.2/24"},
			otherID:    {Name: "other", IPv4Address: "172.30.0.3/24"},
		},
	}}
	inspections := map[string]endpointInspection{
		normalizeID(endpointID): {
			ID: endpointID, Running: true, Health: "healthy",
			Labels: map[string]string{
				"io.rig.managed": "generated-runtime", "io.rig.application": appID, "io.rig.component": "frontend",
				"io.rig.slot": string(generatedruntime.SlotBlue), "io.rig.role": "static",
			},
			Networks: map[string]*networkAttachment{"net-a": {Aliases: []string{"frontend-blue"}, IPAddress: "172.30.0.2"}},
		},
		normalizeID(otherID): {
			ID: otherID, Running: true, Health: "healthy", Labels: map[string]string{},
			Networks: map[string]*networkAttachment{"net-a": {Aliases: []string{"other"}, IPAddress: "172.30.0.3"}},
		},
	}
	return routes, networks, inspections
}

type gatewayV2EndpointIdentityRunner struct {
	inspections map[string]endpointInspection
	requests    []runtimeprocess.CommandRequest
}

type gatewayV2ChallengeRunner struct {
	stdout   []byte
	requests []runtimeprocess.CommandRequest
}

func (r *gatewayV2ChallengeRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.stdout...)}, nil
}

func (r *gatewayV2EndpointIdentityRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.requests = append(r.requests, request)
	if len(request.Args) != 5 || request.Args[0] != "container" || request.Args[1] != "inspect" || request.Args[2] != "--format" || request.Args[3] != endpointInspectFormat {
		return runtimeprocess.CommandResult{}, errors.New("unexpected endpoint identity request")
	}
	value, exists := r.inspections[normalizeID(request.Args[4])]
	if !exists {
		return runtimeprocess.CommandResult{}, errors.New("endpoint is unavailable")
	}
	body, err := json.Marshal(value)
	return runtimeprocess.CommandResult{Stdout: body}, err
}

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
