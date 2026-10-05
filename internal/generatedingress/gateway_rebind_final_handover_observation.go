package generatedingress

import "strings"

// This checks normalized proof shape and its digest. It does not establish
// live ownership; the driver must establish that before returning a value.
func validGatewayRebindFinalHandoverObservationValue(value gatewayRebindFinalHandoverObservation) bool {
	validState := func(state gatewayRebindHandoverContainerState) bool {
		return state == gatewayRebindHandoverContainerAbsent || state == gatewayRebindHandoverContainerStopped ||
			state == gatewayRebindHandoverContainerRunning
	}
	if !validState(value.Stage) || !validState(value.Final) ||
		(value.Stage != gatewayRebindHandoverContainerAbsent && value.Final != gatewayRebindHandoverContainerAbsent) ||
		(value.PredecessorAddress != gatewayRebindPredecessorAddressPresent &&
			value.PredecessorAddress != gatewayRebindPredecessorAddressAbsent &&
			value.PredecessorAddress != gatewayRebindPredecessorAddressAmbiguous) ||
		!validSHA256(value.PredecessorObservationDigest) || !validSHA256(value.HostDigest) ||
		!validSHA256(value.InventoryDigest) || !validSHA256(value.ApplicationEndpointsDigest) || !validSHA256(value.Digest) {
		return false
	}
	if value.Final == gatewayRebindHandoverContainerAbsent {
		if value.FinalID != "" || value.RoutesDigest != "" {
			return false
		}
	} else if !validContainerID(value.FinalID) || normalizeID(value.FinalID) != value.FinalID {
		return false
	}
	if value.Stage != gatewayRebindHandoverContainerAbsent || value.Final != gatewayRebindHandoverContainerAbsent {
		if !value.ConfigVolumePresent || !value.DataVolumePresent || !value.IngressNetworkPresent || !validSHA256(value.ConfigDigest) {
			return false
		}
	}
	if value.Final != gatewayRebindHandoverContainerRunning && value.RoutesDigest != "" {
		return false
	}
	if (!value.PredecessorRunning || value.PredecessorAddress != gatewayRebindPredecessorAddressPresent) && value.PredecessorRoutesDigest != "" {
		return false
	}
	if value.PredecessorRunning {
		if value.PredecessorStopDigest != "" {
			return false
		}
	} else if value.PredecessorStopDigest != value.PredecessorObservationDigest {
		return false
	}
	for _, digest := range []string{value.ConfigDigest, value.RoutesDigest, value.PredecessorRoutesDigest, value.PredecessorStopDigest} {
		if digest != "" && !validSHA256(digest) {
			return false
		}
	}
	seenIDs := make(map[string]bool, len(value.ApplicationNetworks))
	for index, network := range value.ApplicationNetworks {
		if network.Name == "" || strings.TrimSpace(network.Name) != network.Name ||
			!validContainerID(network.ID) || normalizeID(network.ID) != network.ID ||
			seenIDs[network.ID] || (index > 0 && value.ApplicationNetworks[index-1].Name >= network.Name) {
			return false
		}
		seenIDs[network.ID] = true
	}
	digest, err := gatewayRebindFinalHandoverObservationDigest(value)
	return err == nil && digest == value.Digest
}
