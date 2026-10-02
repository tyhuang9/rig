package generatedingress

import (
	"context"
	"sort"
	"strings"
)

// gatewayV2RecoveryTopology names only complete, immutable-ID-bound
// intermediate observations. Unknown covers both incomplete evidence and
// drift; callers must not use it to authorize Docker mutation.
type gatewayV2RecoveryTopology string

const (
	gatewayV2RecoveryUnknown gatewayV2RecoveryTopology = "unknown_or_identity_drift"

	gatewayV2RecoveryStageIntentPartialInfrastructure    gatewayV2RecoveryTopology = "exact_stage_intent_v1_serving_partial_v2_infrastructure_no_containers"
	gatewayV2RecoveryStageIntentStoppedStage             gatewayV2RecoveryTopology = "exact_stage_intent_v1_serving_stopped_stage_final_absent"
	gatewayV2RecoveryTransferIntentV1ServingStoppedStage gatewayV2RecoveryTopology = "exact_transfer_intent_v1_serving_stopped_stage_final_absent"
	gatewayV2RecoveryTransferIntentV1ServingNoContainers gatewayV2RecoveryTopology = "exact_transfer_intent_v1_serving_stage_and_final_absent"
	gatewayV2RecoveryTransferIntentV1ServingStoppedFinal gatewayV2RecoveryTopology = "exact_transfer_intent_v1_serving_stage_absent_stopped_final"
	gatewayV2RecoveryTransferIntentStoppedV1             gatewayV2RecoveryTopology = "exact_transfer_intent_v1_stopped_running_stage_final_absent"
	gatewayV2RecoveryTransferIntentV1StoppedStage        gatewayV2RecoveryTopology = "exact_transfer_intent_v1_stopped_stopped_stage_final_absent"
	gatewayV2RecoveryTransferIntentNoContainers          gatewayV2RecoveryTopology = "exact_transfer_intent_v1_stopped_stage_and_final_absent"
	gatewayV2RecoveryTransferIntentStoppedFinal          gatewayV2RecoveryTopology = "exact_transfer_intent_v1_stopped_stage_absent_stopped_final"
)

// observeGatewayV2RecoveryTopology performs read-only Docker inspection. A
// caller that will use the result to choose a recovery mutation must retain
// the gateway writer lock across this observation and that mutation.
func (m *Manager) observeGatewayV2RecoveryTopology(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayV2RecoveryTopology {
	if m == nil || ctx == nil || !validGatewayTopologyInputs(source, state, journal) {
		return gatewayV2RecoveryUnknown
	}
	observation, err := m.inspectGatewayV2Docker(ctx, source, state, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return gatewayV2RecoveryUnknown
	}
	defer clearGatewayV2DockerObservation(&observation)
	return classifyGatewayV2RecoveryTopology(source, state, journal, observation)
}

// This proof authorizes compensation only. A stopped stage may have no config
// when the process dies after binding its Docker ID and before copying the
// config. Skipping that one file read must never authorize container start.
func (m *Manager) observeGatewayV2StoppedStageForCompensation(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	if m == nil || ctx == nil || journal.Phase != gatewayPhaseStageIntent || !validGatewayTopologyInputs(source, state, journal) {
		return false
	}
	observation, err := m.inspectGatewayV2DockerWithStageConfig(ctx, source, state, journal, false)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return false
	}
	defer clearGatewayV2DockerObservation(&observation)
	return classifyGatewayV2StoppedStageForCompensation(source, state, journal, observation)
}

func classifyGatewayV2StoppedStageForCompensation(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	return journal.Phase == gatewayPhaseStageIntent && validGatewayTopologyInputs(source, state, journal) &&
		validGatewayPinnedImage(observation.Image, observation.ImageFound) &&
		gatewayV2ObservedResourcesMatchJournal(journal, observation) &&
		validGatewayV1Base(source, journal, observation, true) && observation.V1Container.Running && !observation.V1Container.Restarting &&
		observation.V1Stable && observation.V1ResourcesStable && observation.V1EndpointIdentityProven &&
		observation.V2ResourcesStable && observation.StageStable && observation.FinalStable && observation.OwnedInventoriesStable &&
		validGatewayV2StoppedStageInfrastructure(state, journal, observation) && len(observation.StageConfig) == 0 &&
		len(observation.ApplicationNetworks) == 0 && len(observation.ApplicationNetworkIDs) == 0
}

func classifyGatewayV2RecoveryTopology(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) gatewayV2RecoveryTopology {
	if (journal.Phase != gatewayPhaseStageIntent && journal.Phase != gatewayPhaseTransferIntent && journal.Phase != gatewayPhaseRollbackIntent) ||
		!validGatewayTopologyInputs(source, state, journal) ||
		!validGatewayPinnedImage(observation.Image, observation.ImageFound) ||
		!gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV1Base(source, journal, observation, true) ||
		!observation.V1Stable || !observation.V1ResourcesStable || !observation.V1EndpointIdentityProven ||
		!observation.V2ResourcesStable || !observation.StageStable || !observation.FinalStable || !observation.OwnedInventoriesStable {
		return gatewayV2RecoveryUnknown
	}

	v1Serving := observation.V1Container.Running && !observation.V1Container.Restarting
	v1Stopped := !observation.V1Container.Running && !observation.V1Container.Restarting

	switch journal.Phase {
	case gatewayPhaseStageIntent:
		if !v1Serving {
			return gatewayV2RecoveryUnknown
		}
		if validGatewayV2PartialInfrastructure(state, journal, observation) {
			return gatewayV2RecoveryStageIntentPartialInfrastructure
		}
		if validGatewayV2StoppedStage(state, journal, observation) {
			return gatewayV2RecoveryStageIntentStoppedStage
		}
	case gatewayPhaseTransferIntent, gatewayPhaseRollbackIntent:
		if v1Serving && validGatewayV2StoppedStage(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentV1ServingStoppedStage
		}
		if v1Serving && validGatewayV2TransferInfrastructureNoContainers(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentV1ServingNoContainers
		}
		if v1Serving && validGatewayV2StoppedFinalForTransfer(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentV1ServingStoppedFinal
		}
		if !v1Stopped {
			return gatewayV2RecoveryUnknown
		}
		if validGatewayV2RunningStageForTransfer(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentStoppedV1
		}
		if validGatewayV2StoppedStage(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentV1StoppedStage
		}
		if validGatewayV2TransferInfrastructureNoContainers(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentNoContainers
		}
		if validGatewayV2StoppedFinalForTransfer(state, journal, observation) {
			return gatewayV2RecoveryTransferIntentStoppedFinal
		}
	}
	return gatewayV2RecoveryUnknown
}

func validGatewayV2PartialInfrastructure(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	if observation.StageContainerFound || observation.FinalContainerFound || journal.Resources.StageContainerID != "" || journal.Resources.FinalContainerID != "" ||
		len(observation.OwnedContainers) != 0 || len(observation.ApplicationNetworks) != 0 || len(observation.ApplicationNetworkIDs) != 0 {
		return false
	}
	configValid, configBound := validGatewayV2VolumeResourceBinding(journal.Resources.ConfigVolume)
	dataValid, dataBound := validGatewayV2VolumeResourceBinding(journal.Resources.DataVolume)
	if !configValid || !dataValid || observation.ConfigVolumeFound != configBound || observation.DataVolumeFound != dataBound ||
		observation.IngressFound != (journal.Resources.IngressNetworkID != "") {
		return false
	}

	expectedVolumes := make([]string, 0, 2)
	if configBound {
		if !validGatewayVolumeResourceIdentity(observation.ConfigVolumeIdentity) ||
			!validGatewayV2Volume(state, journal, observation.ConfigVolume, true, gatewayV2ConfigVolumeRole) {
			return false
		}
		expectedVolumes = append(expectedVolumes, state.Identity.ConfigVolume)
	}
	if dataBound {
		if !validGatewayVolumeResourceIdentity(observation.DataVolumeIdentity) ||
			!validGatewayV2Volume(state, journal, observation.DataVolume, true, gatewayV2DataVolumeRole) {
			return false
		}
		expectedVolumes = append(expectedVolumes, state.Identity.DataVolume)
	}
	sort.Strings(expectedVolumes)
	if (len(expectedVolumes) == 0 && len(observation.OwnedVolumes) != 0) ||
		(len(expectedVolumes) != 0 && !validOwnedNameSet(observation.OwnedVolumes, expectedVolumes...)) {
		return false
	}
	if observation.IngressFound {
		return validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) &&
			validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, true, "", "")
	}
	return len(observation.OwnedNetworks) == 0
}

func validGatewayV2StoppedStage(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	expected, err := buildGatewayV2StageConfig(state)
	return err == nil && validGatewayV2StoppedStageInfrastructure(state, journal, observation) &&
		len(observation.StageConfig) == 0 && sameCaddyConfig(expected, observation.StageRestartConfig) &&
		len(observation.ApplicationNetworks) == 0 && len(observation.ApplicationNetworkIDs) == 0
}

func validGatewayV2StoppedStageInfrastructure(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	return journal.Resources.StageContainerID != "" && journal.Resources.FinalContainerID == "" && !observation.FinalContainerFound &&
		validOwnedNameSet(observation.OwnedContainers, state.Identity.StageContainer) &&
		validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) &&
		validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) &&
		validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2StoppedContainer(state, journal, observation.StageContainer, observation.StageRuntime, observation.StageContainerFound, gatewayV2StageContainerRole, observation.Image.ID) &&
		validGatewayV2StoppedIngressNetwork(state, journal, observation.IngressNetwork, observation.IngressNetworkID, observation.IngressFound, observation.StageRuntime)
}

func validGatewayV2RunningStageForTransfer(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	return journal.Resources.FinalContainerID == "" && !observation.FinalContainerFound &&
		validOwnedNameSet(observation.OwnedContainers, state.Identity.StageContainer) &&
		validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) &&
		validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) &&
		validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2Container(state, journal, observation.StageContainer, observation.StageRuntime, observation.StageContainerFound, gatewayV2StageContainerRole, observation.Image.ID) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, observation.StageContainer.ID, state.Identity.StageContainer) &&
		validGatewayV2StageConfig(state, observation.StageConfig, observation.StageRestartConfig) &&
		observation.Stage404Proven && observation.StageHostPublicationProven &&
		len(observation.ApplicationNetworks) == 0 && len(observation.ApplicationNetworkIDs) == 0
}

func validGatewayV2TransferInfrastructureNoContainers(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	return journal.Resources.StageContainerID != "" && journal.Resources.FinalContainerID == "" &&
		!observation.StageContainerFound && !observation.FinalContainerFound && len(observation.OwnedContainers) == 0 &&
		validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) &&
		validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) &&
		validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, "", "") &&
		len(observation.ApplicationNetworks) == 0 && len(observation.ApplicationNetworkIDs) == 0
}

func validGatewayV2StoppedFinalForTransfer(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	expected, err := expectedGatewayV2FinalConfig(state)
	return err == nil && journal.Resources.FinalContainerID != "" && !observation.StageContainerFound &&
		validOwnedNameSet(observation.OwnedContainers, state.Identity.FinalContainer) &&
		validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) &&
		validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) &&
		validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2StoppedContainer(state, journal, observation.FinalContainer, observation.FinalRuntime, observation.FinalContainerFound, gatewayV2FinalContainerRole, observation.Image.ID) &&
		validGatewayV2StoppedIngressNetwork(state, journal, observation.IngressNetwork, observation.IngressNetworkID, observation.IngressFound, observation.FinalRuntime) &&
		validGatewayV2StoppedApplicationNetworks(state, observation.FinalContainer, observation.FinalRuntime, observation.ApplicationNetworks, observation.ApplicationNetworkIDs) &&
		len(observation.FinalConfig) == 0 && sameCaddyConfig(expected, observation.FinalRestartConfig) &&
		observation.FinalEndpointIdentityProven
}

func validGatewayV2StoppedIngressNetwork(state gatewayV2RouteState, journal gatewayMigrationJournal, network caddyNetworkInspection, networkID string, found bool, runtime gatewayContainerRuntime) bool {
	attachment, exists := runtime.ConfiguredNetworks[state.Identity.IngressNetwork]
	return exists && validContainerID(networkID) && normalizeID(networkID) == journal.Resources.IngressNetworkID &&
		validGatewayV2StoppedNetworkReference(attachment.NetworkID, networkID) &&
		validGatewayV2IngressNetwork(state, journal, network, found, "", "")
}

func validGatewayV2StoppedApplicationNetworks(state gatewayV2RouteState, container caddyInspection, runtime gatewayContainerRuntime,
	inspections map[string]caddyNetworkInspection, ids map[string]string,
) bool {
	expected, valid := gatewayV2ApplicationNetworkOwners(state)
	if !valid || len(inspections) != len(expected) || len(ids) != len(expected) {
		return false
	}
	containerName := strings.TrimPrefix(container.Name, "/")
	for name, appID := range expected {
		inspection, exists := inspections[name]
		id := ids[name]
		attachment, attached := runtime.ConfiguredNetworks[name]
		if !exists || !attached || !validContainerID(id) || !validGatewayV2StoppedNetworkReference(attachment.NetworkID, id) ||
			name == state.Identity.IngressNetwork || !validApplicationNetwork(inspection.identity(), appID) {
			return false
		}
		for memberID, member := range inspection.Containers {
			if normalizeID(memberID) == normalizeID(container.ID) || member.Name == containerName {
				return false
			}
		}
	}
	return true
}

// Docker may omit NetworkID from NetworkSettings.Networks until a newly
// created container is started. A nonempty value is evidence and must remain a
// canonical exact match for the independently inspected network identity.
func validGatewayV2StoppedNetworkReference(configuredID, inspectedID string) bool {
	if !validContainerID(inspectedID) {
		return false
	}
	return configuredID == "" || (validContainerID(configuredID) && normalizeID(configuredID) == normalizeID(inspectedID))
}
