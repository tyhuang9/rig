package generatedingress

// gatewayV2ObservedResourcesMatchJournal rejects a resource that exists before
// its immutable identity was durably bound. A historical binding may outlive a
// removed resource during rollback; phase-specific topology checks decide
// which resources must still be present.
func gatewayV2ObservedResourcesMatchJournal(journal gatewayMigrationJournal, observed gatewayV2DockerObservation) bool {
	bound := journal.Resources
	if bound.ImageID != "" && (!observed.ImageFound || normalizeID(observed.Image.ID) != bound.ImageID) {
		return false
	}
	if observed.IngressFound && (bound.IngressNetworkID == "" || normalizeID(observed.IngressNetworkID) != bound.IngressNetworkID) {
		return false
	}
	if observed.StageContainerFound && (bound.StageContainerID == "" || normalizeID(observed.StageContainer.ID) != bound.StageContainerID) {
		return false
	}
	if observed.FinalContainerFound && (bound.FinalContainerID == "" || normalizeID(observed.FinalContainer.ID) != bound.FinalContainerID) {
		return false
	}
	if !gatewayV2ObservedVolumeMatchesBinding(observed.ConfigVolumeFound, observed.ConfigVolumeIdentity, bound.ConfigVolume) ||
		!gatewayV2ObservedVolumeMatchesBinding(observed.DataVolumeFound, observed.DataVolumeIdentity, bound.DataVolume) {
		return false
	}
	return true
}

func gatewayV2ObservedVolumeMatchesBinding(found bool, observed gatewayV1VolumeIdentity, bound gatewayV2VolumeResourceBinding) bool {
	if !found {
		return true
	}
	if bound == (gatewayV2VolumeResourceBinding{}) {
		return false
	}
	identity, err := newGatewayV2VolumeResourceBinding(observed)
	return err == nil && identity == bound
}
