package generatedingress

import (
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

// Every routed component must belong to the exact active deployment and slot,
// including loopback-only applications. This is SQL authorization, not proof
// that the referenced containers still exist or serve the approved endpoint.
func gatewayCurrentServingRuntimeCensusMatches(target gatewayCurrentRouteState,
	snapshot appaccess.HostingGatewayStartupSnapshot,
) bool {
	if !validGatewayCurrentRouteState(target) || target.Pending != nil || target.LANRecovery != nil ||
		!gatewayCurrentRouteMutationSnapshotReady(snapshot.Rebind) ||
		!reflect.DeepEqual(snapshot.Upgrades.CurrentProfile, snapshot.Rebind.CurrentProfile) ||
		len(target.Apps) != len(snapshot.RuntimeHeads) {
		return false
	}
	for _, upgrade := range snapshot.Upgrades.Claims {
		if upgrade.Claim.State != appaccess.GatewayProfileUpgradeCommitted && upgrade.Claim.State != appaccess.GatewayProfileUpgradeRolledBack {
			return false
		}
	}
	heads := make(map[string]appaccess.GatewayRebindRuntimeHead, len(snapshot.RuntimeHeads))
	for i, head := range snapshot.RuntimeHeads {
		app, exists := target.Apps[head.AppID]
		if !exists || !validCanonicalUUID(head.AppID) || !validCanonicalUUID(head.DeploymentID) ||
			!validCanonicalUUID(head.ReleaseID) || head.Generation <= 0 || head.UpdatedAt.IsZero() ||
			string(app.Route.Slot) != head.Slot || (i > 0 && snapshot.RuntimeHeads[i-1].AppID >= head.AppID) {
			return false
		}
		heads[head.AppID] = head
	}
	components := make(map[string]appaccess.GatewayStartupRuntimeComponent, len(snapshot.RuntimeComponents))
	containers := make(map[string]bool, len(snapshot.RuntimeComponents))
	for i, component := range snapshot.RuntimeComponents {
		head, found := heads[component.AppID]
		key := component.AppID + "\x00" + component.Name
		if !found || component.DeploymentID != head.DeploymentID || component.ReleaseID != head.ReleaseID ||
			component.Slot != head.Slot || component.State != "active" || !validCanonicalUUID(component.ImageArtifactID) ||
			!validSHA256(component.ContainerID) || component.ContainerName == "" || containers[component.ContainerID] ||
			component.CreatedAt.IsZero() || component.UpdatedAt.Before(component.CreatedAt) ||
			!component.FinishedAt.IsZero() || component.DiagnosticCode != "" {
			return false
		}
		if i > 0 {
			prior := snapshot.RuntimeComponents[i-1]
			if prior.AppID > component.AppID || (prior.AppID == component.AppID && prior.Name >= component.Name) {
				return false
			}
		}
		components[key], containers[component.ContainerID] = component, true
	}
	matched := 0
	for appID, app := range target.Apps {
		for _, endpoint := range app.Route.Endpoints {
			key := appID + "\x00" + endpoint.Component
			component, found := components[key]
			if !found || component.ContainerID != endpoint.ContainerID {
				return false
			}
			delete(components, key)
			matched++
		}
	}
	return matched == len(snapshot.RuntimeComponents) && len(components) == 0
}
