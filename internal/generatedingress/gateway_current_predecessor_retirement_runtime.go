package generatedingress

import (
	"context"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentPredecessorRetirementRuntime interface {
	stopPredecessorOwned(context.Context, gatewayFinalOwnershipFacts, func(context.Context) error) error
	stopNativePredecessorOwned(context.Context, gatewayCurrentNativePredecessorRetirement, func(context.Context) error) error
	retiredProfileListenersSafe(context.Context, gatewayCurrentPredecessorRetirementAction, gatewayProfileBinding, func(context.Context) error) bool
	retiredLocalListenerSafe(context.Context, gatewayCurrentPredecessorRetirementAction, uint16, func(context.Context) error) bool
	predecessorInventoriesStopped(context.Context, gatewayCurrentPredecessorRetirementAction) bool
}

func (d managedGatewayCurrentPhysicalDriver) retireGatewayCurrentPredecessors(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, guard func(context.Context) error,
) error {
	physical, ok := d.runtime.(gatewayCurrentPredecessorRetirementRuntime)
	if !ok || d.manager() == nil || guard == nil || guard(ctx) != nil || !validGatewayFinalOwnershipFacts(action.CurrentFacts) {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	for _, target := range action.Predecessors {
		if target.Facts.Terminal.Resources.FinalContainer == nil || target.Facts.Terminal.Resources.FinalContainer.ID == action.CurrentFinalID ||
			guard(ctx) != nil || physical.stopPredecessorOwned(ctx, target.Facts, guard) != nil || guard(ctx) != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	if err := physical.stopNativePredecessorOwned(ctx, action.Native, guard); err != nil {
		return err
	}
	profiles := []gatewayProfileBinding{action.Native.State.Profile}
	for _, target := range action.Predecessors {
		profiles = append(profiles, target.Facts.profile())
	}
	for _, profile := range profiles {
		if !physical.retiredProfileListenersSafe(ctx, action, profile, guard) {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	localPorts := []uint16{action.Native.Journal.Source.LocalHostPort}
	for _, target := range action.Predecessors {
		localPorts = append(localPorts, target.Facts.LocalHostPort)
	}
	for _, port := range localPorts {
		if !physical.retiredLocalListenerSafe(ctx, action, port, guard) {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	if !physical.predecessorInventoriesStopped(ctx, action) || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	return guard(ctx)
}

func (d managedGatewayCurrentPhysicalDriver) observeGatewayCurrentPredecessorsRetired(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction,
) error {
	physical, ok := d.runtime.(gatewayCurrentPredecessorRetirementRuntime)
	if !ok || ctx == nil || ctx.Err() != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	guard := func(check context.Context) error {
		if check == nil || check.Err() != nil {
			return gatewayCurrentPhysicalDriverError(check)
		}
		return nil
	}
	if !physical.predecessorInventoriesStopped(ctx, action) {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	profiles := []gatewayProfileBinding{action.Native.State.Profile}
	for _, target := range action.Predecessors {
		profiles = append(profiles, target.Facts.profile())
	}
	for _, profile := range profiles {
		if !physical.retiredProfileListenersSafe(ctx, action, profile, guard) {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	localPorts := []uint16{action.Native.Journal.Source.LocalHostPort}
	for _, target := range action.Predecessors {
		localPorts = append(localPorts, target.Facts.LocalHostPort)
	}
	for _, port := range localPorts {
		if !physical.retiredLocalListenerSafe(ctx, action, port, guard) {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	if !physical.predecessorInventoriesStopped(ctx, action) {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	return nil
}

func (d managerGatewayCurrentPhysicalRuntime) predecessorInventoriesStopped(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction,
) bool {
	for _, target := range action.Predecessors {
		container, runtime, present, err := d.terminalOwnedOwnershipInventory(ctx, target.Facts)
		if err != nil || (present && (container.Running || container.Restarting || runtime.Paused || runtime.Dead ||
			gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings))) {
			return false
		}
	}
	container, runtime, present, err := d.nativePredecessorOwnershipInventory(ctx, action.Native)
	return err == nil && (!present || (!container.Running && !container.Restarting && !runtime.Paused && !runtime.Dead &&
		!gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings))) && ctx.Err() == nil
}

func (d managerGatewayCurrentPhysicalRuntime) stopPredecessorOwned(ctx context.Context,
	facts gatewayFinalOwnershipFacts, guard func(context.Context) error,
) error {
	container, runtime, present, err := d.terminalOwnedOwnershipInventory(ctx, facts)
	if err != nil || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if present && (container.Running || container.Restarting || runtime.Paused || runtime.Dead || gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)) {
		if guard(ctx) != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		result, _ := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "stop", "--time", "10", facts.Terminal.Resources.FinalContainer.ID)
		clearResult(&result)
	}
	for count := 0; count < 2; count++ {
		proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
		if guard(proofCtx) != nil {
			cancel()
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		confirmed, confirmedRuntime, confirmedPresent, proofErr := d.terminalOwnedOwnershipInventory(proofCtx, facts)
		failed := proofErr != nil || (confirmedPresent && (confirmed.Running || confirmed.Restarting || confirmedRuntime.Paused || confirmedRuntime.Dead ||
			gatewayV2HasEffectivePortBinding(confirmedRuntime.EffectivePortBindings))) || guard(proofCtx) != nil
		cancel()
		if failed {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	return nil
}

func (d managerGatewayCurrentPhysicalRuntime) retirementCandidates(ctx context.Context) ([]gatewayRebindSuccessorNetworkCandidate, error) {
	if d.manager.gatewayRebindV2NetworkObserver != nil {
		value, err := d.manager.gatewayRebindV2NetworkObserver(ctx, appaccess.GatewayRebindClaimV2{})
		return value.Candidates, err
	}
	values, err := gatewayV2ProductionNetworkPlanReads(d.manager).candidates()
	if err != nil {
		return nil, err
	}
	return gatewayRebindCandidateProjection(values), nil
}

func (d managerGatewayCurrentPhysicalRuntime) retiredProfileListenersSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, profile gatewayProfileBinding, guard func(context.Context) error,
) bool {
	if !validGatewayProfileBinding(profile) || d.hostProbe == nil || guard(ctx) != nil {
		return false
	}
	first, err := d.retirementCandidates(ctx)
	second, secondErr := d.retirementCandidates(ctx)
	if err != nil || secondErr != nil || !reflect.DeepEqual(first, second) {
		return false
	}
	matches := 0
	for _, candidate := range second {
		if candidate.IPv4 == profile.SelectedIPv4 {
			matches++
			if candidate.InterfaceID != profile.InterfaceID {
				return false
			}
		}
	}
	if matches == 0 {
		return true
	}
	if matches != 1 {
		return false
	}
	currentProfile := action.CurrentFacts.profile()
	sharedResponse := false
	for port := profile.PortStart; ; port++ {
		result := d.hostProbe(ctx, profile.SelectedIPv4, port, profile.SelectedIPv4, "/")
		if ctx.Err() != nil {
			return false
		}
		if result.Connected || result.Responded {
			if profile.SelectedIPv4 != currentProfile.SelectedIPv4 || port < currentProfile.PortStart || port > currentProfile.PortEnd {
				return false
			}
			sharedResponse = true
		}
		if port == profile.PortEnd {
			break
		}
	}
	if !sharedResponse {
		return guard(ctx) == nil
	}
	return d.exactCurrentServing(ctx, action, guard)
}

func (d managerGatewayCurrentPhysicalRuntime) exactCurrentServing(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, guard func(context.Context) error,
) bool {
	if guard(ctx) != nil {
		return false
	}
	for _, target := range action.CurrentTargets {
		proof, err := d.observe(ctx, target)
		if err == nil && gatewayCurrentPhysicalAttestationAtTarget(proof, target) &&
			gatewayCurrentPhysicalAttestationMatchesSelection(proof, action.CurrentState) && guard(ctx) == nil {
			return true
		}
	}
	return false
}

func (d managerGatewayCurrentPhysicalRuntime) retiredLocalListenerSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, port uint16, guard func(context.Context) error,
) bool {
	if d.hostProbe == nil || guard(ctx) != nil {
		return false
	}
	result := d.hostProbe(ctx, "127.0.0.1", port, "wrong.invalid", "/")
	if ctx.Err() != nil {
		return false
	}
	return (!result.Connected && !result.Responded) ||
		(port == action.CurrentFacts.LocalHostPort && d.exactCurrentServing(ctx, action, guard))
}

func (d managerGatewayCurrentPhysicalRuntime) stopNativePredecessorOwned(ctx context.Context,
	target gatewayCurrentNativePredecessorRetirement, guard func(context.Context) error,
) error {
	if d.manager == nil || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	container, runtime, present, err := d.nativePredecessorOwnershipInventory(ctx, target)
	if err != nil || guard(ctx) != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if present && (container.Running || container.Restarting || runtime.Paused || runtime.Dead || gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)) {
		if guard(ctx) != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		result, _ := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "stop", "--time", "10", target.Journal.Resources.FinalContainerID)
		clearResult(&result)
	}
	for count := 0; count < 2; count++ {
		proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
		if guard(proofCtx) != nil {
			cancel()
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		confirmed, confirmedRuntime, confirmedPresent, proofErr := d.nativePredecessorOwnershipInventory(proofCtx, target)
		failed := proofErr != nil || (confirmedPresent && (confirmed.Running || confirmed.Restarting || confirmedRuntime.Paused || confirmedRuntime.Dead ||
			gatewayV2HasEffectivePortBinding(confirmedRuntime.EffectivePortBindings))) || guard(proofCtx) != nil
		cancel()
		if failed {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	return nil
}

func (d managerGatewayCurrentPhysicalRuntime) nativePredecessorOwnershipInventory(ctx context.Context,
	target gatewayCurrentNativePredecessorRetirement,
) (caddyInspection, gatewayContainerRuntime, bool, error) {
	invalid := func() (caddyInspection, gatewayContainerRuntime, bool, error) {
		return caddyInspection{}, gatewayContainerRuntime{}, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	state, journal := target.State, target.Journal
	if d.manager == nil || ctx == nil || ctx.Err() != nil || journal.Phase != gatewayPhaseCommitted ||
		!validGatewayV2RouteState(state) || !validGatewayMigrationJournal(journal) || state.OperationID != journal.OperationID ||
		!journalMatchesV2Plan(journal, state) {
		return invalid()
	}
	image, found, err := d.inspectImage(ctx, journal.Resources.ImageID)
	if err != nil || !found || normalizeID(image.ID) != journal.Resources.ImageID || !validGatewayPinnedImage(image, true) {
		return invalid()
	}
	config, configID, found, err := d.manager.inspectNamedVolumeWithIdentity(ctx, state.Identity.ConfigVolume)
	observation := gatewayV2DockerObservation{Image: image, ImageFound: true, ConfigVolume: config, ConfigVolumeIdentity: configID, ConfigVolumeFound: found}
	if err != nil {
		return invalid()
	}
	data, dataID, found, err := d.manager.inspectNamedVolumeWithIdentity(ctx, state.Identity.DataVolume)
	if err != nil {
		return invalid()
	}
	observation.DataVolume, observation.DataVolumeIdentity, observation.DataVolumeFound = data, dataID, found
	network, networkID, found, err := d.manager.inspectNamedGatewayNetwork(ctx, state.Identity.IngressNetwork)
	if err != nil || !found || normalizeID(networkID) != journal.Resources.IngressNetworkID || network.Name != state.Identity.IngressNetwork ||
		network.Driver != "bridge" || network.Scope != "local" || network.Internal || len(network.Options) != 0 ||
		len(network.IPAM.Config) != 1 || network.IPAM.Config[0] != (networkIPAM{Subnet: state.Network.Subnet, Gateway: state.Network.GatewayIPv4}) ||
		!reflect.DeepEqual(network.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false)) {
		return invalid()
	}
	if _, _, stageFound, stageErr := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer); stageErr != nil || stageFound {
		return invalid()
	}
	labels := func(managed string) map[string]string {
		return map[string]string{
			gatewayV2ManagedLabelKey: managed, gatewayV2OperationLabelKey: state.OperationID,
		}
	}
	containers, err := d.ownedNamesWithLabels(ctx, labels(gatewayV2ManagedContainerLabel), "container", "ls", "--all")
	if err != nil || (len(containers) != 0 && !validOwnedNameSet(containers, state.Identity.FinalContainer)) {
		return invalid()
	}
	volumes, err := d.ownedNamesWithLabels(ctx, labels(gatewayV2ManagedContainerLabel), "volume", "ls")
	observation.OwnedVolumes = volumes
	if err != nil || !validGatewayV2Volumes(state, journal, observation) {
		return invalid()
	}
	networks, err := d.ownedNamesWithLabels(ctx, labels(gatewayV2ManagedNetworkLabel), "network", "ls")
	if err != nil || !validOwnedNameSet(networks, state.Identity.IngressNetwork) {
		return invalid()
	}
	named, namedRuntime, namedFound, namedErr := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	byID, idRuntime, idFound, idErr := d.manager.inspectNamedGatewayContainer(ctx, journal.Resources.FinalContainerID)
	if namedErr != nil || idErr != nil || namedFound != idFound ||
		(namedFound && (!reflect.DeepEqual(named, byID) || !reflect.DeepEqual(namedRuntime, idRuntime) ||
			normalizeID(named.ID) != journal.Resources.FinalContainerID ||
			!validGatewayV2ContainerStatic(state, journal, named, true, gatewayV2FinalContainerRole, image.ID))) ||
		(!namedFound && len(containers) != 0) {
		return invalid()
	}
	if !namedFound && len(network.Containers) != 0 {
		return invalid()
	}
	if namedFound && len(network.Containers) > 1 {
		return invalid()
	}
	for memberID, member := range network.Containers {
		if !namedFound || normalizeID(memberID) != journal.Resources.FinalContainerID || member.Name != state.Identity.FinalContainer {
			return invalid()
		}
	}
	expectedUsers := []string{}
	if namedFound {
		expectedUsers = []string{journal.Resources.FinalContainerID}
	}
	for _, name := range []string{state.Identity.ConfigVolume, state.Identity.DataVolume} {
		result, runErr := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+name)
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
	return named, namedRuntime, namedFound, nil
}

var _ gatewayCurrentPredecessorRetirementDriver = managedGatewayCurrentPhysicalDriver{}
