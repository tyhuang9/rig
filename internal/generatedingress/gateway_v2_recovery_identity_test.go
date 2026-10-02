package generatedingress

import (
	"context"
	"strings"
	"testing"
)

func TestClassifyGatewayV2RecoveryTopologyExactIntermediateStates(t *testing.T) {
	source, state, baseJournal := gatewayV2IdentityTestState(t)

	partialJournal := baseJournal
	partialJournal.Phase = gatewayPhaseStageIntent
	partialJournal.Resources.ImageID = strings.Repeat("a", 64)
	partial := gatewayV2IdentityTestObservation(t, source, state, partialJournal, gatewayTopologyExactV1Only)
	partial.ConfigVolume = gatewayV2IdentityTestVolume(state, partialJournal, state.Identity.ConfigVolume, gatewayV2ConfigVolumeRole)
	partial.ConfigVolumeIdentity = gatewayV1VolumeIdentity{
		Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v2/_data", CreatedAt: "2026-09-29T00:01:00Z",
	}
	partial.ConfigVolumeFound = true
	partialJournal.Resources.ConfigVolume = mustGatewayV2RecoveryVolumeBinding(t, partial.ConfigVolumeIdentity)
	partial.OwnedVolumes = []string{state.Identity.ConfigVolume}
	if got := classifyGatewayV2RecoveryTopology(source, state, partialJournal, partial); got != gatewayV2RecoveryStageIntentPartialInfrastructure {
		t.Fatalf("partial infrastructure topology = %q", got)
	}

	stoppedStageJournal := baseJournal
	stoppedStageJournal.Phase = gatewayPhaseStageIntent
	stoppedStageJournal.Resources = gatewayV2IdentityTestBoundResources(t)
	stoppedStageJournal.Resources.FinalContainerID = ""
	stoppedStage := gatewayV2IdentityTestObservation(t, source, state, stoppedStageJournal, gatewayTopologyExactV1WithStage)
	stopGatewayV2TestContainer(&stoppedStage.StageContainer, &stoppedStage.StageRuntime, &stoppedStage.StageConfig, state, stoppedStage.IngressNetworkID, nil)
	stoppedStage.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	stoppedStage.Stage404Proven = false
	stoppedStage.StageHostPublicationProven = false
	if got := classifyGatewayV2RecoveryTopology(source, state, stoppedStageJournal, stoppedStage); got != gatewayV2RecoveryStageIntentStoppedStage {
		t.Fatalf("stopped stage topology = %q", got)
	}

	stoppedV1Journal := stoppedStageJournal
	stoppedV1Journal.Phase = gatewayPhaseTransferIntent
	if got := classifyGatewayV2RecoveryTopology(source, state, stoppedV1Journal, stoppedStage); got != gatewayV2RecoveryTransferIntentV1ServingStoppedStage {
		t.Fatalf("transfer v1-serving stopped-stage topology = %q", got)
	}
	stoppedV1 := gatewayV2IdentityTestObservation(t, source, state, stoppedV1Journal, gatewayTopologyExactV1WithStage)
	stoppedV1.V1Container.Running = false
	stoppedV1.V1Config = nil
	if got := classifyGatewayV2RecoveryTopology(source, state, stoppedV1Journal, stoppedV1); got != gatewayV2RecoveryTransferIntentStoppedV1 {
		t.Fatalf("stopped v1 topology = %q", got)
	}

	transferStoppedStage := gatewayV2IdentityTestObservation(t, source, state, stoppedV1Journal, gatewayTopologyExactV1WithStage)
	transferStoppedStage.V1Container.Running = false
	transferStoppedStage.V1Config = nil
	stopGatewayV2TestContainer(&transferStoppedStage.StageContainer, &transferStoppedStage.StageRuntime, &transferStoppedStage.StageConfig, state, transferStoppedStage.IngressNetworkID, nil)
	transferStoppedStage.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	if got := classifyGatewayV2RecoveryTopology(source, state, stoppedV1Journal, transferStoppedStage); got != gatewayV2RecoveryTransferIntentV1StoppedStage {
		t.Fatalf("transfer stopped stage topology = %q", got)
	}

	transferNoContainers := transferStoppedStage
	transferNoContainers.StageContainer = caddyInspection{}
	transferNoContainers.StageRuntime = gatewayContainerRuntime{}
	transferNoContainers.StageContainerFound = false
	transferNoContainers.StageRestartConfig = nil
	transferNoContainers.OwnedContainers = nil
	if got := classifyGatewayV2RecoveryTopology(source, state, stoppedV1Journal, transferNoContainers); got != gatewayV2RecoveryTransferIntentNoContainers {
		t.Fatalf("transfer no-container topology = %q", got)
	}

	stoppedFinalJournal := baseJournal
	stoppedFinalJournal.Phase = gatewayPhaseTransferIntent
	stoppedFinalJournal.Resources = gatewayV2IdentityTestBoundResources(t)
	stoppedFinal := gatewayV2IdentityTestObservation(t, source, state, stoppedFinalJournal, gatewayTopologyExactFinalV2)
	stopGatewayV2TestContainer(&stoppedFinal.FinalContainer, &stoppedFinal.FinalRuntime, &stoppedFinal.FinalConfig, state, stoppedFinal.IngressNetworkID, stoppedFinal.ApplicationNetworkIDs)
	disconnectStoppedGatewayV2TestContainer(&stoppedFinal, stoppedFinal.FinalContainer.ID, state.Identity.FinalContainer)
	stoppedFinal.Final404Proven = false
	stoppedFinal.FinalRoutesProven = false
	stoppedFinal.FinalHostPublicationProven = false
	if got := classifyGatewayV2RecoveryTopology(source, state, stoppedFinalJournal, stoppedFinal); got != gatewayV2RecoveryTransferIntentStoppedFinal {
		t.Fatalf("stopped final topology = %q", got)
	}
}

func TestClassifyGatewayV2RecoveryTopologyRejectsUnboundAndDriftedResources(t *testing.T) {
	source, state, baseJournal := gatewayV2IdentityTestState(t)

	t.Run("observed unbound partial volume", func(t *testing.T) {
		journal := baseJournal
		journal.Phase = gatewayPhaseStageIntent
		journal.Resources.ImageID = strings.Repeat("a", 64)
		observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
		observation.ConfigVolume = gatewayV2IdentityTestVolume(state, journal, state.Identity.ConfigVolume, gatewayV2ConfigVolumeRole)
		observation.ConfigVolumeIdentity = gatewayV1VolumeIdentity{
			Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v2/_data", CreatedAt: "2026-09-29T00:01:00Z",
		}
		observation.ConfigVolumeFound = true
		observation.OwnedVolumes = []string{state.Identity.ConfigVolume}
		if got := classifyGatewayV2RecoveryTopology(source, state, journal, observation); got != gatewayV2RecoveryUnknown {
			t.Fatalf("unbound volume topology = %q", got)
		}
	})

	tests := []struct {
		name   string
		mutate func(*gatewayMigrationJournal, *gatewayV2DockerObservation)
	}{
		{"final immutable id drift", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.FinalContainer.ID = "sha256:" + strings.Repeat("9", 64)
		}},
		{"config volume creation identity drift", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.ConfigVolumeIdentity.CreatedAt = "2026-09-29T00:09:00Z"
		}},
		{"bound final disappeared", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.FinalContainerFound = false
			observation.OwnedContainers = nil
		}},
		{"stopped final has effective host listener", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.FinalRuntime.EffectivePortBindings = gatewayV2IdentityTestPortBindingsCopy(observation.FinalContainer.PortBindings)
		}},
		{"stopped final configured ingress identity drift", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			attachment := observation.FinalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork]
			attachment.NetworkID = strings.Repeat("9", 64)
			observation.FinalRuntime.ConfiguredNetworks[state.Identity.IngressNetwork] = attachment
		}},
		{"stopped final configured application network identity drift", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			attachment := observation.FinalRuntime.ConfiguredNetworks["net-a"]
			attachment.NetworkID = strings.Repeat("9", 64)
			observation.FinalRuntime.ConfiguredNetworks["net-a"] = attachment
		}},
		{"restart config drift", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.FinalRestartConfig = []byte(`{"admin":{"listen":"localhost:2019"}}`)
		}},
		{"extra owned container", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.OwnedContainers = append(observation.OwnedContainers, "unexpected")
		}},
		{"inventory changed during observation", func(_ *gatewayMigrationJournal, observation *gatewayV2DockerObservation) {
			observation.OwnedInventoriesStable = false
		}},
		{"phase does not describe intermediate state", func(journal *gatewayMigrationJournal, _ *gatewayV2DockerObservation) {
			journal.Phase = gatewayPhaseV2Serving
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal := baseJournal
			journal.Phase = gatewayPhaseTransferIntent
			journal.Resources = gatewayV2IdentityTestBoundResources(t)
			observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
			stopGatewayV2TestContainer(&observation.FinalContainer, &observation.FinalRuntime, &observation.FinalConfig, state, observation.IngressNetworkID, observation.ApplicationNetworkIDs)
			disconnectStoppedGatewayV2TestContainer(&observation, observation.FinalContainer.ID, state.Identity.FinalContainer)
			test.mutate(&journal, &observation)
			if got := classifyGatewayV2RecoveryTopology(source, state, journal, observation); got != gatewayV2RecoveryUnknown {
				t.Fatalf("drift topology = %q", got)
			}
		})
	}
}

func TestObserveGatewayV2RecoveryTopologyFailsClosedOnInspectionError(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseStageIntent
	manager := &Manager{runner: failingGatewayV2Runner{}, options: Options{
		DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: defaultTimeout, OutputLimit: defaultOutputLimit,
	}}
	if got := manager.observeGatewayV2RecoveryTopology(context.Background(), source, state, journal); got != gatewayV2RecoveryUnknown {
		t.Fatalf("inspection failure topology = %q", got)
	}
}

func stopGatewayV2TestContainer(container *caddyInspection, runtime *gatewayContainerRuntime, liveConfig *[]byte, state gatewayV2RouteState,
	ingressNetworkID string, applicationNetworkIDs map[string]string,
) {
	container.Running = false
	runtime.EffectivePortBindings = nil
	runtime.ConfiguredNetworks = make(map[string]gatewayV2ConfiguredNetwork, len(container.Networks))
	for name := range container.Networks {
		networkID := applicationNetworkIDs[name]
		var ipam *gatewayV2ConfiguredIPAM
		priority := 0
		if name == state.Identity.IngressNetwork {
			networkID = ingressNetworkID
			ipam = &gatewayV2ConfiguredIPAM{IPv4Address: state.Network.ContainerIPv4}
			priority = caddyGatewayPriority
		}
		runtime.ConfiguredNetworks[name] = gatewayV2ConfiguredNetwork{IPAMConfig: ipam, NetworkID: networkID, GwPriority: priority}
	}
	*liveConfig = nil
}

func disconnectStoppedGatewayV2TestContainer(observation *gatewayV2DockerObservation, containerID, containerName string) {
	observation.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	for name, network := range observation.ApplicationNetworks {
		for memberID, member := range network.Containers {
			if normalizeID(memberID) == normalizeID(containerID) || member.Name == containerName {
				delete(network.Containers, memberID)
			}
		}
		observation.ApplicationNetworks[name] = network
	}
}

func mustGatewayV2RecoveryVolumeBinding(t *testing.T, identity gatewayV1VolumeIdentity) gatewayV2VolumeResourceBinding {
	t.Helper()
	binding, err := newGatewayV2VolumeResourceBinding(identity)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
