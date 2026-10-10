package generatedingress

import (
	"context"
	"reflect"
	"strconv"
)

type gatewayCurrentTerminalStopRuntime interface {
	stopTerminalOwned(context.Context, gatewayCurrentTerminalStopTarget, func(context.Context) error) error
}

func (d managedGatewayCurrentPhysicalDriver) stopGatewayCurrentTerminalTarget(ctx context.Context,
	target gatewayCurrentTerminalStopTarget, guard func(context.Context) error,
) error {
	physical, ok := d.runtime.(gatewayCurrentTerminalStopRuntime)
	if !ok || ctx == nil || guard == nil || !validGatewayCurrentTerminalStopTarget(target) || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := physical.stopTerminalOwned(ctx, target, guard); err != nil {
		return err
	}
	return guard(ctx)
}

// No route/config reader, serving proof, copy, start, remove or SQL capability
// is used here. A lost stop acknowledgment is resolved through fresh immutable
// inventory and every configured listener's absence, never through the error.
func (d managerGatewayCurrentPhysicalRuntime) stopTerminalOwned(ctx context.Context,
	target gatewayCurrentTerminalStopTarget, guard func(context.Context) error,
) error {
	if d.manager == nil || d.hostProbe == nil || ctx == nil || ctx.Err() != nil || guard == nil ||
		!validGatewayCurrentTerminalStopTarget(target) || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	container, present, err := d.terminalOwnedInventory(ctx, target.Facts)
	if err != nil || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if present && container.Running {
		result, _ := d.manager.run(ctx, d.manager.options.CommandTimeout,
			"container", "stop", "--time", "10", target.Facts.Terminal.Resources.FinalContainer.ID)
		clearResult(&result)
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
	defer cancel()
	if guard(proofCtx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	confirmed, present, err := d.terminalOwnedInventory(proofCtx, target.Facts)
	if err != nil || (present && confirmed.Running) || !d.terminalOwnedListenersAbsent(proofCtx, target.Facts) || guard(proofCtx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	return nil
}

func (d managerGatewayCurrentPhysicalRuntime) terminalOwnedInventory(ctx context.Context,
	facts gatewayFinalOwnershipFacts,
) (caddyInspection, bool, error) {
	invalid := func() (caddyInspection, bool, error) {
		return caddyInspection{}, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil || !validGatewayFinalOwnershipFacts(facts) {
		return invalid()
	}
	identity, resources := facts.Terminal.SuccessorIdentity, facts.Terminal.Resources
	image, found, err := d.inspectImage(ctx, resources.ImageID)
	if err != nil || !found || normalizeID(image.ID) != resources.ImageID || !validGatewayPinnedImage(image, true) {
		return invalid()
	}
	volume, volumeID, found, err := d.manager.inspectNamedVolumeWithIdentity(ctx, identity.ConfigVolume)
	if err != nil || !found || !gatewayFinalOwnershipVolumeMatches(facts, volume, volumeID, resources.ConfigVolume, gatewayV2ConfigVolumeRole) {
		return invalid()
	}
	volume, volumeID, found, err = d.manager.inspectNamedVolumeWithIdentity(ctx, identity.DataVolume)
	if err != nil || !found || !gatewayFinalOwnershipVolumeMatches(facts, volume, volumeID, resources.DataVolume, gatewayV2DataVolumeRole) {
		return invalid()
	}
	network, networkID, found, err := d.manager.inspectNamedGatewayNetwork(ctx, identity.IngressNetwork)
	if err != nil || !found || !gatewayFinalOwnershipIngressMatches(facts, network, networkID) {
		return invalid()
	}
	if _, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, identity.StageContainer); err != nil || found {
		return invalid()
	}
	// Use operation/generation inventories so an extra resource with a crossed
	// plan or identity label cannot disappear behind exact-label filtering.
	labels := func(managed string) map[string]string {
		return map[string]string{
			gatewayV2ManagedLabelKey: managed, gatewayV2OperationLabelKey: facts.Lineage.OperationID,
			gatewayRebindGenerationLabelKey: strconv.FormatUint(facts.Lineage.ProtectedGeneration, 10),
		}
	}
	containers, err := d.ownedNamesWithLabels(ctx, labels(gatewayV2ManagedContainerLabel), "container", "ls", "--all")
	if err != nil {
		return invalid()
	}
	volumes, err := d.ownedNamesWithLabels(ctx, labels(gatewayV2ManagedContainerLabel), "volume", "ls")
	if err != nil || !validOwnedNameSet(volumes, identity.ConfigVolume, identity.DataVolume) {
		return invalid()
	}
	networks, err := d.ownedNamesWithLabels(ctx, labels(gatewayV2ManagedNetworkLabel), "network", "ls")
	if err != nil || !validOwnedNameSet(networks, identity.IngressNetwork) {
		return invalid()
	}
	named, namedRuntime, namedFound, namedErr := d.manager.inspectNamedGatewayContainer(ctx, identity.FinalContainer)
	byID, idRuntime, idFound, idErr := d.manager.inspectNamedGatewayContainer(ctx, resources.FinalContainer.ID)
	if namedErr != nil || idErr != nil || namedFound != idFound {
		return invalid()
	}
	expectedUsers := []string{}
	if namedFound {
		if !reflect.DeepEqual(named, byID) || !reflect.DeepEqual(namedRuntime, idRuntime) ||
			!gatewayFinalOwnershipContainerMatches(facts, named, namedRuntime) || !validOwnedNameSet(containers, identity.FinalContainer) {
			return invalid()
		}
		expectedUsers = []string{resources.FinalContainer.ID}
	} else if len(containers) != 0 {
		return invalid()
	}
	for _, name := range []string{identity.ConfigVolume, identity.DataVolume} {
		result, runErr := d.manager.run(ctx, d.manager.options.CommandTimeout,
			"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+name)
		ids, parseErr := parseGatewayV2DockerNetworkIDs(result.Stdout)
		truncated := result.StdoutTruncated || result.StderrTruncated
		clearResult(&result)
		if runErr != nil || parseErr != nil || truncated || !equalStrings(ids, expectedUsers) {
			return invalid()
		}
	}
	if ctx.Err() != nil {
		return invalid()
	}
	return named, namedFound, nil
}

func (d managerGatewayCurrentPhysicalRuntime) terminalOwnedListenersAbsent(ctx context.Context,
	facts gatewayFinalOwnershipFacts,
) bool {
	if !validGatewayFinalOwnershipFacts(facts) || d.hostProbe == nil {
		return false
	}
	profile := facts.Terminal.SuccessorProfile
	for port := profile.PortStart; ; port++ {
		result := d.hostProbe(ctx, profile.SelectedIPv4, port, profile.SelectedIPv4, "/")
		if ctx.Err() != nil || result.Connected || result.Responded {
			return false
		}
		if port == profile.PortEnd {
			break
		}
	}
	result := d.hostProbe(ctx, "127.0.0.1", facts.LocalHostPort, "wrong.invalid", "/")
	return ctx.Err() == nil && !result.Connected && !result.Responded
}

var _ gatewayCurrentTerminalStopDriver = managedGatewayCurrentPhysicalDriver{}
var _ gatewayCurrentTerminalStopRuntime = managerGatewayCurrentPhysicalRuntime{}
