package generatedingress

// validGatewayRebindPredecessorDocker proves ownership of the committed v2
// predecessor without claiming its historical host address is reachable. A
// Docker restart may leave the exact final container stopped when its old bind
// address has disappeared; that state remains identifiable for a guarded
// stop-by-ID cutover. Neither state authorizes a successor effect on its own.
func validGatewayRebindPredecessorDocker(source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal, observation gatewayV2DockerObservation,
) bool {
	if journal.Phase != gatewayPhaseCommitted ||
		!validGatewayTopologyInputs(source, state, journal) ||
		!validGatewayPinnedImage(observation.Image, observation.ImageFound) ||
		!gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV1Base(source, journal, observation, false) ||
		!observation.V1Stable || !observation.V1ResourcesStable ||
		observation.V1Container.Running || observation.V1Container.Restarting ||
		!observation.V2ResourcesStable || !observation.StageStable ||
		!observation.FinalStable || !observation.OwnedInventoriesStable ||
		observation.StageContainerFound ||
		!validOwnedNameSet(observation.OwnedContainers, state.Identity.FinalContainer) ||
		!validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork) ||
		!validContainerID(observation.IngressNetworkID) ||
		!validGatewayV2Volumes(state, journal, observation) {
		return false
	}
	if !observation.FinalContainer.Running {
		return validGatewayV2StoppedFinal(state, journal, observation, true)
	}
	return validGatewayV2Container(state, journal, observation.FinalContainer, observation.FinalRuntime,
		observation.FinalContainerFound, gatewayV2FinalContainerRole, observation.Image.ID) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound,
			observation.FinalContainer.ID, state.Identity.FinalContainer) &&
		validGatewayV2RunningApplicationNetworkIDs(state, observation.FinalRuntime, observation.ApplicationNetworkIDs) &&
		validGatewayV2ApplicationNetworks(state, observation.FinalContainer, observation.ApplicationNetworks, observation.ApplicationNetworkIDs) &&
		validGatewayV2FinalConfig(state, observation.FinalConfig, observation.FinalRestartConfig) &&
		observation.Final404Proven && observation.FinalRoutesProven && observation.FinalEndpointIdentityProven
}
