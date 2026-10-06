package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// gatewayRebindTypedHandoverRuntime is the normalized v2-source late-effect
// adapter. It consumes only typed checkpoint/current lineage and retained
// terminal evidence; it never fabricates a migration journal for a prior
// rebind. The production factory remains closed until forward and rollback
// paths have both passed their concrete executor gates.
type gatewayRebindTypedHandoverRuntime struct {
	manager        *Manager
	stage          gatewayRebindTypedStageRuntime
	hostProbe      gatewayV2HostStatusProbe
	containerProbe gatewayV2ContainerChallengeProbe
	listenerAbsent func(context.Context, string, uint16) bool
}

type gatewayRebindTypedRollbackPreparation struct {
	Owned                   gatewayRebindTypedRollbackOwnedResources
	PredecessorRoutesDigest string
	AdoptionProofDigest     string
}

// gatewayRebindTypedRollbackDriver is deliberately separate from the forward
// handover interface. The coordinator must durably append the returned intent
// before rollbackSuccessor may remove any exact-owned resource or restart the
// predecessor.
type gatewayRebindTypedRollbackDriver interface {
	prepareRollback(context.Context, gatewayRebindPreparedAttempt, gatewayCurrentSelection,
		[]gatewayRebindProgressRecord, gatewayRebindTypedEffectGuard) (gatewayRebindTypedRollbackPreparation, error)
	rollbackSuccessor(context.Context, gatewayRebindPreparedAttempt, gatewayCurrentSelection,
		gatewayRebindProgressRecord, gatewayRebindTypedEffectGuard) (gatewayRebindFinalHandoverTerminalProof, error)
}

type gatewayRebindTypedPredecessorObservation struct {
	Selection       gatewayCurrentSelection
	Profile         gatewayProfileBinding
	ContainerID     string
	LocalHostPort   uint16
	Running         bool
	Address         gatewayRebindPredecessorAddressState
	Observation     string
	Routes          string
	PhysicalCurrent *gatewayCurrentPhysicalAttestation
	PhysicalUpgrade *gatewayV2DockerObservation
}

type gatewayRebindTypedHandoverPhysical struct {
	Stage               gatewayRebindTypedStageObservation
	Final               caddyInspection
	FinalRuntime        gatewayContainerRuntime
	FinalFound          bool
	Predecessor         gatewayRebindTypedPredecessorObservation
	ApplicationNetworks map[string]caddyNetworkInspection
}

func newGatewayRebindTypedHandoverRuntime(manager *Manager) gatewayRebindTypedHandoverRuntime {
	inventory := managerGatewayV2DockerInventory{manager: manager}
	reads := gatewayRebindSuccessorPreflightReads{network: gatewayV2ProductionNetworkPlanReads(manager),
		dockerIDs: inventory.listNetworkIDs}
	return gatewayRebindTypedHandoverRuntime{manager: manager,
		stage: gatewayRebindTypedStageRuntime{manager: manager, reads: reads}, hostProbe: probeGatewayV2HostStatus,
		containerProbe: func(ctx context.Context, id, address string, port uint16, host, challenge string) bool {
			return manager.probeGatewayV2ContainerChallenge(ctx, id, address, port, host, challenge)
		}, listenerAbsent: probeGatewayRebindHandoverListenerAbsent}
}

func gatewayRebindTypedPredecessorSelection(attempt gatewayRebindPreparedAttempt,
	history gatewayRebindProtectedIntentHistory,
) (gatewayCurrentSelection, error) {
	invalid := errors.New("generated ingress typed predecessor selection is unavailable")
	checkpoint := attempt.Checkpoint
	if !validGatewayRebindPredecessorCheckpoint(checkpoint) || checkpoint.sourceRef() != attempt.Intent.Predecessor ||
		!gatewayRebindCheckpointMatchesProtectedHistory(checkpoint, history.Predecessor, history) {
		return gatewayCurrentSelection{}, invalid
	}
	switch checkpoint.Lineage.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		if checkpoint.UpgradeState == nil || checkpoint.CurrentState != nil ||
			!gatewayRebindCheckpointMatchesUpgradePredecessor(checkpoint, history.Predecessor) {
			return gatewayCurrentSelection{}, invalid
		}
		upgrade, source := history.Predecessor, cloneRouteState(history.Source)
		selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: checkpoint.Lineage,
			Upgrade: &upgrade, UpgradeSource: &source}
		if _, err := newGatewayRebindAttemptViewV2(attempt.Intent, checkpoint, selection); err != nil {
			return gatewayCurrentSelection{}, invalid
		}
		return selection, nil
	case appaccess.GatewayRebindSourceGatewayRebind:
		if checkpoint.CurrentState == nil || checkpoint.UpgradeState != nil {
			return gatewayCurrentSelection{}, invalid
		}
		var terminal gatewayRebindAttemptTerminalView
		found := false
		for _, retained := range history.Terminals {
			if retained.Receipt.Digest != checkpoint.Lineage.TerminalReceiptDigest {
				continue
			}
			value, err := newGatewayRebindAttemptTerminalViewLegacy(retained.Receipt)
			if err != nil || found {
				return gatewayCurrentSelection{}, invalid
			}
			terminal, found = value, true
		}
		for _, retained := range history.TerminalsV2 {
			if retained.Receipt.Digest != checkpoint.Lineage.TerminalReceiptDigest {
				continue
			}
			value, err := newGatewayRebindAttemptTerminalViewV2(retained.Receipt)
			if err != nil || found {
				return gatewayCurrentSelection{}, invalid
			}
			terminal, found = value, true
		}
		if !found || !gatewayRebindAttemptTerminalMatchesLineage(terminal, checkpoint.Lineage) {
			return gatewayCurrentSelection{}, invalid
		}
		state := cloneGatewayCurrentRouteState(*checkpoint.CurrentState)
		selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionRebind, Lineage: checkpoint.Lineage,
			Terminal: &terminal, State: &state}
		if _, err := newGatewayRebindAttemptViewV2(attempt.Intent, checkpoint, selection); err != nil {
			return gatewayCurrentSelection{}, invalid
		}
		return selection, nil
	default:
		return gatewayCurrentSelection{}, invalid
	}
}

func (d gatewayRebindTypedHandoverRuntime) predecessor(ctx context.Context,
	attempt gatewayRebindPreparedAttempt,
) (gatewayRebindTypedPredecessorObservation, error) {
	invalid := func() (gatewayRebindTypedPredecessorObservation, error) {
		return gatewayRebindTypedPredecessorObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil {
		return invalid()
	}
	history, err := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return invalid()
	}
	selection, err := gatewayRebindTypedPredecessorSelection(attempt, history)
	if err != nil {
		return invalid()
	}
	value := gatewayRebindTypedPredecessorObservation{Selection: selection,
		Address: gatewayRebindPredecessorAddressAbsent}
	switch selection.Kind {
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || selection.UpgradeSource == nil {
			return invalid()
		}
		first, err := d.manager.inspectGatewayRebindDocker(ctx, *selection.UpgradeSource,
			selection.Upgrade.State, selection.Upgrade.Journal)
		if err != nil || !validGatewayRebindPredecessorDocker(*selection.UpgradeSource,
			selection.Upgrade.State, selection.Upgrade.Journal, first) {
			clearGatewayV2DockerObservation(&first)
			return invalid()
		}
		second, err := d.manager.inspectGatewayRebindDocker(ctx, *selection.UpgradeSource,
			selection.Upgrade.State, selection.Upgrade.Journal)
		if err != nil || !validGatewayRebindPredecessorDocker(*selection.UpgradeSource,
			selection.Upgrade.State, selection.Upgrade.Journal, second) ||
			!sameGatewayRebindPredecessorDockerObservation(first, second, selection.Upgrade.State.Identity) {
			clearGatewayV2DockerObservation(&first)
			clearGatewayV2DockerObservation(&second)
			return invalid()
		}
		value.Observation, err = gatewayRebindPredecessorDockerDigest(second, selection.Upgrade.State.Identity)
		if err != nil {
			clearGatewayV2DockerObservation(&first)
			clearGatewayV2DockerObservation(&second)
			return invalid()
		}
		value.Profile = selection.Upgrade.State.Profile
		value.ContainerID = selection.Upgrade.Journal.Resources.FinalContainerID
		value.LocalHostPort = selection.Upgrade.Journal.Source.LocalHostPort
		value.Running = second.FinalContainer.Running
		if value.Running && d.manager.proveGatewayV2FinalRoutes(ctx, selection.Upgrade.State, value.ContainerID) &&
			proveGatewayV2FinalHostPublication(ctx, selection.Upgrade.State, value.ContainerID,
				d.hostProbe, d.containerProbe) &&
			proveGatewayV2FinalLoopbackRoutes(ctx, selection.Upgrade.State, selection.Upgrade.Journal, d.hostProbe) {
			value.Routes, _ = canonicalDigest(selection.Upgrade.State.Apps)
		}
		value.PhysicalUpgrade = &second
		clearGatewayV2DockerObservation(&first)
	case gatewayCurrentSelectionRebind:
		if selection.State == nil {
			return invalid()
		}
		target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
		if err != nil {
			return invalid()
		}
		runtime := managerGatewayCurrentPhysicalRuntime{manager: d.manager, hostProbe: d.hostProbe,
			containerProbe: d.containerProbe}
		physical, err := runtime.observe(ctx, target)
		if err != nil || (physical.Outcome != gatewayCurrentPhysicalStableServing &&
			physical.Outcome != gatewayCurrentPhysicalRecoveryStopped) {
			return invalid()
		}
		value.Profile = gatewayProfileBinding{RevisionID: selection.State.Profile.RevisionID,
			RevisionNumber: selection.State.Profile.RevisionNumber, SpecDigest: selection.State.Profile.SpecDigest,
			SelectedIPv4: selection.State.Profile.SelectedIPv4, InterfaceID: selection.State.Profile.InterfaceID,
			PortStart: selection.State.Profile.PortStart, PortEnd: selection.State.Profile.PortEnd}
		value.ContainerID = selection.Terminal.Resources.FinalContainer.ID
		value.Running = physical.Outcome == gatewayCurrentPhysicalStableServing
		value.Observation = physical.Digest
		value.Routes, _ = canonicalDigest(selection.State.Apps)
		value.LocalHostPort = gatewayRebindRetainedHandoverLocalPort(history.Progress,
			selection.Lineage.ProtectedGeneration, selection.Lineage.OperationID)
		value.PhysicalCurrent = &physical
	default:
		return invalid()
	}
	if value.LocalHostPort == 0 || !validContainerID(value.ContainerID) || !validSHA256(value.Observation) ||
		!validGatewayProfileBinding(value.Profile) {
		return invalid()
	}
	candidates, err := d.stage.reads.network.candidates()
	if err != nil {
		return invalid()
	}
	count, exact := 0, false
	for _, candidate := range candidates {
		if candidate.IPv4 == value.Profile.SelectedIPv4 {
			count++
			exact = candidate.InterfaceID == value.Profile.InterfaceID
		}
	}
	if count == 1 && exact {
		value.Address = gatewayRebindPredecessorAddressPresent
	} else if count != 0 {
		value.Address = gatewayRebindPredecessorAddressAmbiguous
	}
	return value, nil
}

func gatewayRebindTypedFinalPortBindingsMatch(actual map[string][]map[string]string,
	intent gatewayRebindProtectedIntentV2, plan gatewayRebindFinalHandoverPlan,
) bool {
	profile := intent.SuccessorProfile
	if len(actual) != int(profile.PortEnd-profile.PortStart)+2 {
		return false
	}
	for port := profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		binding := actual[text+"/tcp"]
		if len(binding) != 1 || len(binding[0]) != 2 || binding[0]["HostIp"] != profile.SelectedIPv4 ||
			binding[0]["HostPort"] != text {
			return false
		}
		if port == profile.PortEnd {
			break
		}
	}
	loopback := actual[strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp"]
	return len(loopback) == 1 && len(loopback[0]) == 2 && loopback[0]["HostIp"] == "127.0.0.1" &&
		loopback[0]["HostPort"] == strconv.FormatUint(uint64(plan.LocalHostPort), 10)
}

func gatewayRebindTypedFinalContainerMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, container caddyInspection, runtime gatewayContainerRuntime,
	expectedID string,
) bool {
	if effect.HandoverIntent == nil || effect.Network == nil || effect.FinalContainer == nil ||
		!validGatewayRebindTypedHandoverIntentValue(*effect.HandoverIntent) ||
		!validGatewayRebindFinalContainerBindingValue(*effect.FinalContainer) ||
		effect.FinalContainer.ID != expectedID {
		return false
	}
	plan := effect.HandoverIntent.Plan
	binding, err := gatewayRebindTypedFinalContainerBindingFor(intent, effect, plan, expectedID)
	if err != nil || binding != *effect.FinalContainer || !validContainerID(container.ID) ||
		normalizeID(container.ID) != expectedID || normalizeID(container.Image) != effect.ImageID ||
		container.Restarting || !validGatewayContainerRuntime(runtime, false) ||
		strings.TrimPrefix(container.Name, "/") != intent.Identity.FinalContainer ||
		container.Hostname != intent.Identity.FinalHostname || container.User != "1000:1000" ||
		container.NetworkMode != intent.Identity.IngressNetwork || !exactGatewayV2Environment(container.Env) ||
		!container.ReadOnly || container.Privileged || !onlyCaddyCapability(container.CapAdd) ||
		!exactFoldSet(container.CapDrop, "ALL") || !onlyNoNewPrivileges(container.SecurityOpt) ||
		len(container.Binds) != 0 || len(container.Tmpfs) != 0 || container.Memory != 268435456 ||
		container.MemorySwap != 268435456 || container.NanoCPUs != 1_000_000_000 || container.PIDsLimit != 128 ||
		container.LogType != "local" || len(container.LogConfig) != 2 || container.LogConfig["max-size"] != "10m" ||
		container.LogConfig["max-file"] != "3" || container.Restart != gatewayV2FinalRestartPolicy ||
		len(container.Entrypoint) != 1 || container.Entrypoint[0] != caddyExecutable || len(container.Cmd) != 3 ||
		container.Cmd[0] != "run" || container.Cmd[1] != "--config" ||
		container.Cmd[2] != "/config/"+intent.Identity.ActiveConfigFilename || len(container.Ulimits) != 1 ||
		container.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!validGatewayV2ContainerLabels(container.Labels, gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)) ||
		!validGatewayRebindStageContainerMounts(container.Mounts, intent.Identity) ||
		!gatewayRebindTypedFinalPortBindingsMatch(container.PortBindings, intent, plan) ||
		(container.Running && !gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings,
			container.PortBindings)) || (!container.Running && gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)) {
		return false
	}
	networks := map[string]string{intent.Identity.IngressNetwork: effect.Network.ID}
	for _, network := range plan.ApplicationNetworks {
		networks[network.Name] = network.ID
	}
	if len(runtime.ConfiguredNetworks) != len(networks) || (container.Running && len(container.Networks) != len(networks)) ||
		(!container.Running && len(container.Networks) != 0 && len(container.Networks) != len(networks)) {
		return false
	}
	for name, id := range networks {
		configured, exists := runtime.ConfiguredNetworks[name]
		priority := 0
		if name == intent.Identity.IngressNetwork {
			priority = caddyGatewayPriority
		}
		if !exists || configured.GwPriority != priority || configured.IPv6Gateway != "" ||
			!validGatewayV2StoppedNetworkReference(configured.NetworkID, id) {
			return false
		}
		if name == intent.Identity.IngressNetwork {
			if configured.IPAMConfig == nil || configured.IPAMConfig.IPv4Address != intent.Network.ContainerIPv4 ||
				configured.IPAMConfig.IPv6Address != "" {
				return false
			}
		} else if configured.IPAMConfig != nil && *configured.IPAMConfig != (gatewayV2ConfiguredIPAM{}) {
			return false
		}
		if container.Running {
			address, addressErr := netip.ParseAddr(configured.IPAddress)
			if configured.NetworkID == "" || !validContainerID(configured.EndpointID) ||
				normalizeID(configured.EndpointID) != configured.EndpointID || addressErr != nil || !address.Is4() ||
				address.IsUnspecified() || address.IsMulticast() || address.String() != configured.IPAddress ||
				(name == intent.Identity.IngressNetwork && configured.IPAddress != intent.Network.ContainerIPv4) {
				return false
			}
		} else if configured.EndpointID != "" || configured.IPAddress != "" {
			return false
		}
		if container.Running || len(container.Networks) != 0 {
			attached := container.Networks[name]
			if attached == nil || attached.GwPriority != priority || attached.IPv6Gateway != "" ||
				attached.IPAddress != configured.IPAddress {
				return false
			}
		}
	}
	return true
}

func gatewayRebindTypedRoutes(effect gatewayRebindTypedEffectProgress) (map[string]routeRecord, bool) {
	if effect.FinalConfigIntent == nil ||
		!validGatewayRebindTypedFinalConfigRoutePlanShape(effect.FinalConfigIntent.RoutePlan) {
		return nil, false
	}
	routes := make(map[string]routeRecord, len(effect.FinalConfigIntent.RoutePlan.Routes))
	for _, binding := range effect.FinalConfigIntent.RoutePlan.Routes {
		if _, duplicate := routes[binding.AppID]; duplicate {
			return nil, false
		}
		routes[binding.AppID] = binding.Route
	}
	return routes, true
}

func gatewayRebindTypedApplicationNetworks(ctx context.Context, manager *Manager,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	predecessor gatewayRebindTypedPredecessorObservation, final caddyInspection,
	finalRuntime gatewayContainerRuntime, finalFound bool,
) ([]gatewayRebindHandoverApplicationNetwork, map[string]caddyNetworkInspection, string, error) {
	invalid := errors.New("generated ingress typed application networks are unavailable")
	routes, valid := gatewayRebindTypedRoutes(effect)
	owners, validOwners := gatewayRouteNetworkOwners(routes)
	if manager == nil || !valid || !validOwners {
		return nil, nil, "", invalid
	}
	bindings := make([]gatewayRebindHandoverApplicationNetwork, 0, len(owners))
	networks := make(map[string]caddyNetworkInspection, len(owners))
	for name, appID := range owners {
		network, id, found, err := manager.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || !validContainerID(id) || normalizeID(id) != id ||
			!validApplicationNetwork(network.identity(), appID) {
			return nil, nil, "", invalid
		}
		allowed := make(map[string]bool)
		for _, endpoint := range routes[appID].Endpoints {
			if endpoint.NetworkName == name {
				allowed[normalizeID(endpoint.ContainerID)] = true
			}
		}
		if predecessor.Running {
			allowed[predecessor.ContainerID] = true
		}
		if finalFound && final.Running {
			allowed[normalizeID(final.ID)] = true
		}
		if len(network.Containers) != len(allowed) {
			return nil, nil, "", invalid
		}
		for member := range network.Containers {
			if !allowed[normalizeID(member)] {
				return nil, nil, "", invalid
			}
		}
		if finalFound {
			configured, exists := finalRuntime.ConfiguredNetworks[name]
			if !exists || !validGatewayV2StoppedNetworkReference(configured.NetworkID, id) ||
				(final.Running && (!validGatewayApplicationNetworkMembership(network, final, name) ||
					normalizeID(configured.NetworkID) != id)) {
				return nil, nil, "", invalid
			}
		}
		bindings = append(bindings, gatewayRebindHandoverApplicationNetwork{Name: name, ID: id})
		networks[name] = network
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Name < bindings[j].Name })
	if !gatewayRebindTypedApplicationNetworkNamesMatch(intent, effect, bindings) {
		return nil, nil, "", invalid
	}
	digest, err := manager.inspectGatewayEndpointIdentitySnapshot(ctx, routes, networks)
	if err != nil {
		return nil, nil, "", invalid
	}
	return bindings, networks, digest, nil
}

func gatewayRebindTypedApplicationNetworkNamesMatch(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, bindings []gatewayRebindHandoverApplicationNetwork,
) bool {
	if effect.FinalConfigIntent == nil {
		return false
	}
	plan := effect.FinalConfigIntent.RoutePlan
	if effect.HandoverIntent != nil && effect.HandoverIntent.Plan.RoutePlanDigest != plan.Digest {
		return false
	}
	return validGatewayRebindTypedHandoverApplicationNetworks(plan, bindings) &&
		plan.SuccessorIdentity == intent.Identity
}

func gatewayRebindTypedHandoverResourcePrefixMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
) bool {
	if value.StageContainerFound == value.FinalContainerFound {
		return false
	}
	return gatewayRebindTypedHandoverResourceInventoryMatches(intent, effect, value,
		value.StageContainerFound, value.FinalContainerFound)
}

func gatewayRebindTypedHandoverResourceInventoryMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
	stageExpected, finalExpected bool,
) bool {
	if effect.Network == nil || effect.ConfigVolume == nil || effect.DataVolume == nil || effect.StageContainer == nil ||
		!gatewayRebindTypedStageImageMatches(intent, value) || value.Image.ID != effect.ImageID ||
		!gatewayRebindTypedStageNetworkMatches(intent, value, effect.Network) ||
		value.StageContainerFound != stageExpected || value.FinalContainerFound != finalExpected {
		return false
	}
	config := gatewayRebindStageConfigVolumeBinding(*effect.ConfigVolume)
	data := gatewayRebindStageConfigVolumeBinding{Name: effect.DataVolume.Name,
		Mountpoint: effect.DataVolume.Mountpoint, CreatedAt: effect.DataVolume.CreatedAt,
		OwnershipDigest: effect.DataVolume.OwnershipDigest}
	if !gatewayRebindTypedStageVolumeMatches(intent, value.ConfigVolume, value.ConfigVolumeIdentity,
		value.ConfigVolumeFound, intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole, &config) ||
		!gatewayRebindTypedStageVolumeMatches(intent, value.DataVolume, value.DataVolumeIdentity,
			value.DataVolumeFound, intent.Identity.DataVolume, gatewayV2DataVolumeRole, &data) {
		return false
	}
	wantContainers := []string{}
	if stageExpected {
		wantContainers = append(wantContainers, intent.Identity.StageContainer)
	}
	if finalExpected {
		wantContainers = append(wantContainers, intent.Identity.FinalContainer)
	}
	sort.Strings(wantContainers)
	wantVolumes := []string{intent.Identity.ConfigVolume, intent.Identity.DataVolume}
	sort.Strings(wantVolumes)
	return equalStrings(value.OwnedContainers, wantContainers) && equalStrings(value.OwnedVolumes, wantVolumes) &&
		equalStrings(value.OwnedNetworks, []string{intent.Identity.IngressNetwork})
}

func (d gatewayRebindTypedHandoverRuntime) configInventory(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	containerID string, finalRunning bool,
) bool {
	if d.manager == nil || d.manager.runner == nil || effect.FinalConfigIntent == nil || !validContainerID(containerID) {
		return false
	}
	stage, err := gatewayRebindTypedStageConfigBytes(attempt.Intent)
	if err != nil {
		return false
	}
	defer clear(stage)
	active, err := gatewayRebindTypedFinalConfigBytes(attempt.Intent, attempt.Checkpoint,
		effect.FinalConfigIntent.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(active)
	result, err := d.manager.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable:  d.manager.options.DockerExecutable,
		Args:        []string{"container", "cp", containerID + ":/config/.", "-"},
		Directory:   d.manager.options.WorkingDirectory,
		Env:         append([]string(nil), d.manager.dockerEnv...),
		Timeout:     d.manager.options.CommandTimeout,
		OutputLimit: gatewayRebindFinalConfigArchiveLimit,
	})
	defer clearResult(&result)
	if err != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 || ctx.Err() != nil {
		return false
	}
	sequence := uint64(0)
	for candidate := uint64(2); candidate <= gatewayRebindProgressMaximumSequence; candidate++ {
		if validGatewayRebindTypedEffectProgress(effect, candidate) {
			if sequence != 0 {
				return false
			}
			sequence = candidate
		}
	}
	phase, ok := gatewayRebindTypedEffectPhase(sequence)
	if !ok {
		return false
	}
	mode := gatewayRebindFinalHandoverAutosaveMode(phase, "", effect.FinalContainer != nil, finalRunning)
	if mode == 0 {
		return false
	}
	inventory, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(result.Stdout, stage, active, mode)
	return err == nil && inventory == gatewayRebindFinalConfigInventoryExactPair
}

func (d gatewayRebindTypedHandoverRuntime) finalRoutes(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress, id string,
) bool {
	if effect.HandoverIntent == nil || effect.FinalConfigIntent == nil || !validContainerID(id) ||
		d.hostProbe == nil || d.containerProbe == nil {
		return false
	}
	routes, valid := gatewayRebindTypedRoutes(effect)
	if !valid {
		return false
	}
	body, err := gatewayRebindTypedFinalConfigBytes(attempt.Intent, attempt.Checkpoint,
		effect.FinalConfigIntent.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(body)
	live, err := d.manager.inspectLiveCaddyConfig(ctx, id)
	defer clear(live)
	if err != nil || !sameCaddyConfig(body, live) ||
		!d.manager.proveGatewayRouteEndpointTransports(ctx, id, routes) {
		return false
	}
	token, err := gatewayRebindTypedStageConfigProbeToken(attempt.Intent)
	if err != nil {
		return false
	}
	assignments := make(map[uint16]caddyV2LANAssignment)
	for _, binding := range effect.FinalConfigIntent.RoutePlan.Routes {
		if binding.SourceLAN != nil {
			assignments[binding.SourceLAN.Raw.Port] = caddyV2LANAssignment{AppID: binding.AppID,
				AllocationID:         binding.SourceLAN.Raw.AllocationID,
				AccessRevisionID:     binding.SourceLAN.Raw.AccessRevisionID,
				AccessRevisionNumber: binding.SourceLAN.Raw.AccessRevisionNumber,
				AccessSpecDigest:     binding.SourceLAN.Raw.AccessSpecDigest}
		}
		host := binding.AppID + ".rig.localhost"
		challenge := gatewayV2AppChallenge(token, binding.AppID)
		if !exactGatewayV2HostChallenge(ctx, d.hostProbe, "127.0.0.1", effect.HandoverIntent.Plan.LocalHostPort,
			host, challenge) || !d.manager.probeGatewayV2AnyStatus(ctx, id,
			attempt.Intent.Network.ContainerIPv4, gatewayV2ContainerPort, host) {
			return false
		}
	}
	profile := attempt.Intent.SuccessorProfile
	for port := profile.PortStart; ; port++ {
		challenge := gatewayV2PortChallenge(token, port)
		if !exactGatewayV2HostChallenge(ctx, d.hostProbe, profile.SelectedIPv4, port, profile.SelectedIPv4, challenge) ||
			!d.containerProbe(ctx, id, attempt.Intent.Network.ContainerIPv4, port, profile.SelectedIPv4, challenge) ||
			!d.proveListenerAbsent(ctx, "127.0.0.1", port) {
			return false
		}
		if assignment, exists := assignments[port]; exists {
			appChallenge := gatewayV2LANAppChallenge(token, port, assignment)
			if !exactGatewayV2HostChallenge(ctx, d.hostProbe, profile.SelectedIPv4, port,
				profile.SelectedIPv4, appChallenge) || !d.containerProbe(ctx, id,
				attempt.Intent.Network.ContainerIPv4, port, profile.SelectedIPv4, appChallenge) {
				return false
			}
		}
		if port == profile.PortEnd {
			break
		}
	}
	return true
}

func (d gatewayRebindTypedHandoverRuntime) withdrawn(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	predecessor gatewayRebindTypedPredecessorObservation, stageRunning, finalRunning bool,
) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	successor := attempt.Intent.SuccessorProfile
	if !stageRunning && !finalRunning {
		for port := successor.PortStart; ; port++ {
			shared := predecessor.Running && predecessor.Profile.SelectedIPv4 == successor.SelectedIPv4 &&
				port >= predecessor.Profile.PortStart && port <= predecessor.Profile.PortEnd
			if !shared && !d.proveListenerAbsent(ctx, successor.SelectedIPv4, port) {
				return false
			}
			if port == successor.PortEnd {
				break
			}
		}
	}
	if !predecessor.Running {
		if predecessor.Address == gatewayRebindPredecessorAddressPresent {
			for port := predecessor.Profile.PortStart; ; port++ {
				shared := finalRunning && successor.SelectedIPv4 == predecessor.Profile.SelectedIPv4 &&
					port >= successor.PortStart && port <= successor.PortEnd
				if !shared && !d.proveListenerAbsent(ctx,
					predecessor.Profile.SelectedIPv4, port) {
					return false
				}
				if port == predecessor.Profile.PortEnd {
					break
				}
			}
		}
		if !finalRunning && !d.proveListenerAbsent(ctx, "127.0.0.1", predecessor.LocalHostPort) {
			return false
		}
	}
	return ctx.Err() == nil
}

func (d gatewayRebindTypedHandoverRuntime) proveListenerAbsent(ctx context.Context,
	address string, port uint16,
) bool {
	return d.listenerAbsent != nil && d.listenerAbsent(ctx, address, port)
}

func gatewayRebindTypedPreparedAttemptFromHistory(intent gatewayRebindProtectedIntentV2,
	history gatewayRebindProtectedIntentHistory,
) (gatewayRebindPreparedAttempt, error) {
	invalid := errors.New("generated ingress typed retained attempt is unavailable")
	if !validGatewayRebindProtectedIntentV2(intent) {
		return gatewayRebindPreparedAttempt{}, invalid
	}
	generation, operationID := intent.Claim.Spec.SuccessorProtectedGeneration, intent.OperationID
	var checkpoint gatewayRebindPredecessorCheckpoint
	checkpointCount := 0
	for _, retained := range history.Checkpoints {
		if retained.Generation == generation && retained.Checkpoint.OperationID == operationID {
			checkpoint, checkpointCount = retained.Checkpoint, checkpointCount+1
		}
	}
	var progress gatewayRebindProgressRecord
	progressCount := 0
	for _, retained := range history.Progress {
		if retained.Generation == generation && retained.Record.OperationID == operationID && retained.Record.Sequence == 1 {
			progress, progressCount = retained.Record, progressCount+1
		}
	}
	if checkpointCount != 1 || progressCount != 1 || checkpoint.sourceRef() != intent.Predecessor ||
		progress.TypedEffect != nil || progress.TypedRollback != nil || progress.Phase != gatewayRebindProgressSuccessorIntent {
		return gatewayRebindPreparedAttempt{}, invalid
	}
	attempt := gatewayRebindPreparedAttempt{Claim: intent.Claim, Checkpoint: checkpoint, Intent: intent, Progress: progress}
	selection, err := gatewayRebindTypedPredecessorSelection(attempt, history)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, invalid
	}
	if _, err = newGatewayRebindAttemptViewV2(intent, checkpoint, selection); err != nil {
		return gatewayRebindPreparedAttempt{}, invalid
	}
	return attempt, nil
}

func gatewayRebindTypedIngressMembershipMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, physical gatewayRebindTypedHandoverPhysical,
) bool {
	if effect.Network == nil || !physical.Stage.NetworkFound || physical.Stage.NetworkID != effect.Network.ID {
		return false
	}
	id, name := "", ""
	if physical.Stage.StageContainerFound && physical.Stage.StageContainer.Running {
		id, name = effect.StageContainer.ID, intent.Identity.StageContainer
	}
	if physical.FinalFound && physical.Final.Running {
		if id != "" {
			return false
		}
		id, name = normalizeID(physical.Final.ID), intent.Identity.FinalContainer
	}
	if id == "" {
		return len(physical.Stage.Network.Containers) == 0
	}
	if len(physical.Stage.Network.Containers) != 1 {
		return false
	}
	prefix, err := netip.ParsePrefix(intent.Network.Subnet)
	if err != nil {
		return false
	}
	for memberID, member := range physical.Stage.Network.Containers {
		address, parseErr := netip.ParsePrefix(member.IPv4Address)
		return parseErr == nil && normalizeID(memberID) == id && member.Name == name &&
			address.Addr().String() == intent.Network.ContainerIPv4 && address.Bits() == prefix.Bits()
	}
	return false
}

func (d gatewayRebindTypedHandoverRuntime) readResources(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	stageExpected, finalExpected bool,
) (gatewayRebindTypedHandoverPhysical, error) {
	invalid := func() (gatewayRebindTypedHandoverPhysical, error) {
		return gatewayRebindTypedHandoverPhysical{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || stageExpected && finalExpected || effect.FinalConfigIntent == nil ||
		effect.FinalConfigCopy == nil || effect.StageContainer == nil || effect.Network == nil ||
		effect.ConfigVolume == nil || effect.DataVolume == nil {
		return invalid()
	}
	physical := gatewayRebindTypedHandoverPhysical{}
	var err error
	physical.Stage, err = d.stage.read(ctx, attempt.Intent)
	if err != nil || !d.stage.networkTopologyMatches(ctx, attempt.Intent, physical.Stage) ||
		!gatewayRebindTypedHandoverResourceInventoryMatches(attempt.Intent, effect, physical.Stage,
			stageExpected, finalExpected) {
		return invalid()
	}
	physical.Final, physical.FinalRuntime, physical.FinalFound, err =
		d.manager.inspectNamedGatewayContainer(ctx, attempt.Intent.Identity.FinalContainer)
	if err != nil || physical.FinalFound != finalExpected {
		return invalid()
	}
	if stageExpected {
		if physical.Stage.StageContainer.Running {
			if !gatewayRebindTypedRunningStageMatches(attempt.Intent, effect, physical.Stage) {
				return invalid()
			}
		} else if !gatewayRebindTypedStoppedStageContainerMatches(attempt.Intent, effect, physical.Stage) {
			return invalid()
		}
	}
	if finalExpected && !gatewayRebindTypedFinalContainerMatches(attempt.Intent, effect,
		physical.Final, physical.FinalRuntime, normalizeID(physical.Final.ID)) {
		return invalid()
	}
	physical.Predecessor, err = d.predecessor(ctx, attempt)
	if err != nil {
		return invalid()
	}
	_, networks, _, err := gatewayRebindTypedApplicationNetworks(ctx, d.manager,
		attempt.Intent, effect, physical.Predecessor, physical.Final, physical.FinalRuntime, physical.FinalFound)
	if err != nil {
		return invalid()
	}
	physical.ApplicationNetworks = networks
	if !gatewayRebindTypedIngressMembershipMatches(attempt.Intent, effect, physical) {
		return invalid()
	}
	return physical, nil
}

func (d gatewayRebindTypedHandoverRuntime) stableResources(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	stageExpected, finalExpected bool, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedHandoverPhysical, error) {
	if guard == nil || guard(ctx) != nil {
		return gatewayRebindTypedHandoverPhysical{}, gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := d.readResources(ctx, attempt, effect, stageExpected, finalExpected)
	if err != nil {
		return gatewayRebindTypedHandoverPhysical{}, err
	}
	second, err := d.readResources(ctx, attempt, effect, stageExpected, finalExpected)
	if err != nil || !reflect.DeepEqual(first, second) || ctx.Err() != nil || guard(ctx) != nil {
		return gatewayRebindTypedHandoverPhysical{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return second, nil
}

func (d gatewayRebindTypedHandoverRuntime) read(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
) (gatewayRebindFinalHandoverObservation, gatewayRebindTypedHandoverPhysical, error) {
	invalid := func() (gatewayRebindFinalHandoverObservation, gatewayRebindTypedHandoverPhysical, error) {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindTypedHandoverPhysical{},
			gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || effect.FinalConfigIntent == nil || effect.FinalConfigCopy == nil ||
		effect.StageContainer == nil || effect.Network == nil || effect.ConfigVolume == nil || effect.DataVolume == nil {
		return invalid()
	}
	finalExpected := effect.FinalContainer != nil
	physical, err := d.readResources(ctx, attempt, effect, !finalExpected, finalExpected)
	if err != nil {
		return invalid()
	}
	bindings, networks, endpointDigest, err := gatewayRebindTypedApplicationNetworks(ctx, d.manager,
		attempt.Intent, effect, physical.Predecessor, physical.Final, physical.FinalRuntime, physical.FinalFound)
	if err != nil {
		return invalid()
	}
	physical.ApplicationNetworks = networks
	proof := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent,
		Final: gatewayRebindHandoverContainerAbsent, PredecessorRunning: physical.Predecessor.Running,
		PredecessorAddress:           physical.Predecessor.Address,
		PredecessorObservationDigest: physical.Predecessor.Observation,
		ApplicationNetworks:          bindings, ApplicationEndpointsDigest: endpointDigest,
		ConfigVolumePresent: physical.Stage.ConfigVolumeFound, DataVolumePresent: physical.Stage.DataVolumeFound,
		IngressNetworkPresent: physical.Stage.NetworkFound}
	if physical.Stage.StageContainerFound {
		proof.Stage = gatewayRebindHandoverContainerStopped
		if physical.Stage.StageContainer.Running {
			proof.Stage = gatewayRebindHandoverContainerRunning
		}
	}
	if physical.FinalFound {
		proof.Final, proof.FinalID = gatewayRebindHandoverContainerStopped, normalizeID(physical.Final.ID)
		if physical.Final.Running {
			proof.Final = gatewayRebindHandoverContainerRunning
		}
	}
	if !physical.Predecessor.Running {
		proof.PredecessorStopDigest = proof.PredecessorObservationDigest
	} else if validSHA256(physical.Predecessor.Routes) {
		proof.PredecessorRoutesDigest = physical.Predecessor.Routes
	}
	containerID := ""
	if physical.Stage.StageContainerFound {
		containerID = effect.StageContainer.ID
	}
	if physical.FinalFound {
		containerID = proof.FinalID
	}
	if containerID != "" {
		if !d.configInventory(ctx, attempt, effect, containerID, proof.Final == gatewayRebindHandoverContainerRunning) {
			return invalid()
		}
		proof.ConfigDigest = effect.FinalConfigIntent.ContentDigest
	}
	if proof.Final == gatewayRebindHandoverContainerRunning {
		if !d.finalRoutes(ctx, attempt, effect, proof.FinalID) {
			return invalid()
		}
		proof.RoutesDigest = effect.FinalConfigIntent.RoutePlan.Digest
	}
	if !d.withdrawn(ctx, attempt, effect, physical.Predecessor,
		proof.Stage == gatewayRebindHandoverContainerRunning, proof.Final == gatewayRebindHandoverContainerRunning) {
		return invalid()
	}
	proof.HostDigest, err = canonicalDigest(struct {
		Network gatewayRebindSuccessorNetworkObservation `json:"network"`
		ID      string                                   `json:"id"`
		Present bool                                     `json:"present"`
		Address gatewayRebindPredecessorAddressState     `json:"predecessorAddress"`
	}{attempt.Intent.NetworkObservation, physical.Stage.NetworkID, physical.Stage.NetworkFound,
		physical.Predecessor.Address})
	if err != nil {
		return invalid()
	}
	stageInventory := physical.Stage
	stageInventory.StageContainer.Mounts = nil
	finalInventory := physical.Final
	finalInventory.Mounts = nil
	proof.InventoryDigest, err = canonicalDigest(struct {
		Stage        gatewayRebindTypedStageObservation `json:"stage"`
		Final        caddyInspection                    `json:"final"`
		FinalRuntime gatewayContainerRuntime            `json:"finalRuntime"`
		Predecessor  string                             `json:"predecessor"`
		Applications map[string]caddyNetworkInspection  `json:"applications"`
	}{stageInventory, finalInventory, physical.FinalRuntime, physical.Predecessor.Observation,
		physical.ApplicationNetworks})
	if err != nil {
		return invalid()
	}
	proof.Digest, err = gatewayRebindFinalHandoverObservationDigest(proof)
	if err != nil || !validGatewayRebindFinalHandoverObservationValue(proof) {
		return invalid()
	}
	return proof, physical, nil
}

func (d gatewayRebindTypedHandoverRuntime) stable(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverObservation, gatewayRebindTypedHandoverPhysical, error) {
	if guard == nil || guard(ctx) != nil {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindTypedHandoverPhysical{},
			gatewayRebindEffectBoundaryError(ctx)
	}
	firstProof, firstPhysical, err := d.read(ctx, attempt, effect)
	if err != nil {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindTypedHandoverPhysical{}, err
	}
	secondProof, secondPhysical, err := d.read(ctx, attempt, effect)
	if err != nil || !reflect.DeepEqual(firstProof, secondProof) || !reflect.DeepEqual(firstPhysical, secondPhysical) ||
		ctx.Err() != nil {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindTypedHandoverPhysical{},
			gatewayRebindEffectBoundaryError(ctx)
	}
	// read performs config and publication probes after its resource snapshot.
	// A final inventory read binds those late observations to the exact same
	// containers, volumes, networks and predecessor before any caller acts.
	finalExpected := effect.FinalContainer != nil
	finalPhysical, err := d.readResources(ctx, attempt, effect, !finalExpected, finalExpected)
	if err != nil || !reflect.DeepEqual(secondPhysical, finalPhysical) || guard(ctx) != nil {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindTypedHandoverPhysical{},
			gatewayRebindEffectBoundaryError(ctx)
	}
	return secondProof, secondPhysical, nil
}

func gatewayRebindTypedSelectionAuthorizesAttempt(current gatewayCurrentSelection,
	predecessor gatewayCurrentSelection, attempt gatewayRebindPreparedAttempt,
) bool {
	if sameGatewayCurrentSelection(current, predecessor) {
		return true
	}
	if current.Kind != gatewayCurrentSelectionRebind || current.State == nil || current.Terminal == nil ||
		current.Terminal.Disposition != gatewayRebindFinalHandoverTerminalCommit ||
		current.Terminal.ProtectedIntentDigest != attempt.Intent.Digest ||
		current.Lineage.OperationID != attempt.Intent.OperationID ||
		current.Lineage.ProtectedGeneration != attempt.Intent.Claim.Spec.SuccessorProtectedGeneration ||
		!gatewayRebindAttemptTerminalMatchesLineage(*current.Terminal, current.Lineage) {
		return false
	}
	profile := attempt.Intent.SuccessorProfile
	return current.Lineage.ProfileRevisionID == profile.RevisionID &&
		current.Lineage.ProfileRevisionNumber == profile.RevisionNumber &&
		current.Lineage.ProfileSpecDigest == profile.SpecDigest &&
		current.State.Profile.RevisionID == profile.RevisionID &&
		current.State.Profile.RevisionNumber == profile.RevisionNumber &&
		current.State.Profile.SpecDigest == profile.SpecDigest
}

func gatewayRebindTypedPredecessorMatchesPlan(value gatewayRebindTypedPredecessorObservation,
	plan gatewayRebindFinalHandoverPlan,
) bool {
	return value.Observation == plan.PredecessorObservationDigest &&
		value.Running == plan.PredecessorInitiallyRunning && value.LocalHostPort == plan.LocalHostPort
}

func (d gatewayRebindTypedHandoverRuntime) prepareHandover(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedHandoverPreparation, error) {
	invalid := func() (gatewayRebindTypedHandoverPreparation, error) {
		return gatewayRebindTypedHandoverPreparation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if effect.HandoverIntent != nil || effect.FinalContainer != nil || effect.CutoverIntent != nil ||
		effect.FinalConfigIntent == nil || effect.FinalConfigCopy == nil {
		return invalid()
	}
	proof, physical, err := d.stable(ctx, attempt, effect, guard)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, physical.Predecessor.Selection, attempt) ||
		proof.Stage != gatewayRebindHandoverContainerRunning || proof.Final != gatewayRebindHandoverContainerAbsent ||
		proof.ConfigDigest != effect.FinalConfigIntent.ContentDigest || proof.RoutesDigest != "" ||
		proof.PredecessorObservationDigest != physical.Predecessor.Observation ||
		!reflect.DeepEqual(proof.ApplicationNetworks, effect.ApplicationNetworks) && len(effect.ApplicationNetworks) != 0 {
		return invalid()
	}
	return gatewayRebindTypedHandoverPreparation{LocalHostPort: physical.Predecessor.LocalHostPort,
		PredecessorObservationDigest: physical.Predecessor.Observation,
		PredecessorInitiallyRunning:  physical.Predecessor.Running,
		ApplicationNetworks:          append([]gatewayRebindHandoverApplicationNetwork(nil), proof.ApplicationNetworks...)}, nil
}

func (d gatewayRebindTypedHandoverRuntime) retainedAttempt(ctx context.Context,
	intent gatewayRebindProtectedIntentV2,
) (gatewayRebindPreparedAttempt, error) {
	if d.manager == nil || ctx == nil || ctx.Err() != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindEffectBoundaryError(ctx)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(intent, history)
	if err != nil {
		return gatewayRebindPreparedAttempt{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return attempt, nil
}

func (d gatewayRebindTypedHandoverRuntime) finalBindingIfPresent(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalContainerBinding, bool) {
	if guard == nil || guard(ctx) != nil || effect.HandoverIntent == nil {
		return gatewayRebindFinalContainerBinding{}, false
	}
	container, _, found, err := d.manager.inspectNamedGatewayContainer(ctx, attempt.Intent.Identity.FinalContainer)
	if err != nil || !found || !validContainerID(container.ID) {
		return gatewayRebindFinalContainerBinding{}, false
	}
	binding, err := gatewayRebindTypedFinalContainerBindingFor(attempt.Intent, effect,
		effect.HandoverIntent.Plan, normalizeID(container.ID))
	if err != nil {
		return gatewayRebindFinalContainerBinding{}, false
	}
	candidate := effect
	candidate.FinalContainer = &binding
	physical, err := d.stableResources(ctx, attempt, candidate, false, true, guard)
	if err != nil || !gatewayRebindTypedPredecessorMatchesPlan(physical.Predecessor, effect.HandoverIntent.Plan) {
		return gatewayRebindFinalContainerBinding{}, false
	}
	return binding, true
}

func (d gatewayRebindTypedHandoverRuntime) bindFinalContainer(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalContainerBinding, error) {
	invalid := func() (gatewayRebindFinalContainerBinding, error) {
		return gatewayRebindFinalContainerBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || effect.HandoverIntent == nil || effect.FinalContainer != nil ||
		!validGatewayRebindTypedHandoverIntentValue(*effect.HandoverIntent) {
		return invalid()
	}
	attempt, err := d.retainedAttempt(ctx, intent)
	if err != nil {
		return invalid()
	}
	if binding, ok := d.finalBindingIfPresent(ctx, attempt, effect, guard); ok {
		return binding, nil
	}
	physical, stageErr := d.stableResources(ctx, attempt, effect, true, false, guard)
	if stageErr == nil {
		if !gatewayRebindTypedPredecessorMatchesPlan(physical.Predecessor, effect.HandoverIntent.Plan) {
			return invalid()
		}
		if physical.Stage.StageContainer.Running {
			if guard(ctx) != nil {
				return invalid()
			}
			stopErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
				"container", "stop", "--time", "10", effect.StageContainer.ID)
			physical, err = d.stableResources(ctx, attempt, effect, true, false, guard)
			if err != nil || physical.Stage.StageContainer.Running ||
				!gatewayRebindTypedPredecessorMatchesPlan(physical.Predecessor, effect.HandoverIntent.Plan) {
				_ = stopErr
				return invalid()
			}
		}
		if guard(ctx) != nil {
			return invalid()
		}
		removeErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"container", "rm", effect.StageContainer.ID)
		physical, err = d.stableResources(ctx, attempt, effect, false, false, guard)
		if err != nil || !gatewayRebindTypedPredecessorMatchesPlan(physical.Predecessor, effect.HandoverIntent.Plan) {
			_ = removeErr
			return invalid()
		}
	} else {
		physical, err = d.stableResources(ctx, attempt, effect, false, false, guard)
		if err != nil || !gatewayRebindTypedPredecessorMatchesPlan(physical.Predecessor, effect.HandoverIntent.Plan) {
			return invalid()
		}
	}
	if guard(ctx) != nil {
		return invalid()
	}
	args, err := gatewayRebindTypedFinalContainerCreateArgs(intent, effect,
		effect.HandoverIntent.Plan.LocalHostPort, effect.HandoverIntent.Plan.ApplicationNetworks)
	if err != nil {
		return invalid()
	}
	result, runErr := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	createdID := normalizeID(strings.TrimSpace(string(result.Stdout)))
	clearResult(&result)
	container, _, found, inspectErr := d.manager.inspectNamedGatewayContainer(ctx, intent.Identity.FinalContainer)
	if inspectErr != nil || !found || !validContainerID(container.ID) || guard(ctx) != nil {
		return invalid()
	}
	observedID := normalizeID(container.ID)
	if runErr == nil && createdID != observedID {
		return invalid()
	}
	binding, err := gatewayRebindTypedFinalContainerBindingFor(intent, effect,
		effect.HandoverIntent.Plan, observedID)
	if err != nil {
		return invalid()
	}
	candidate := effect
	candidate.FinalContainer = &binding
	physical, err = d.stableResources(ctx, attempt, candidate, false, true, guard)
	if err != nil || !gatewayRebindTypedPredecessorMatchesPlan(physical.Predecessor, effect.HandoverIntent.Plan) {
		return invalid()
	}
	return binding, nil
}

func (d gatewayRebindTypedHandoverRuntime) prepareCutover(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverObservation, error) {
	if effect.HandoverIntent == nil || effect.FinalContainer == nil || effect.CutoverIntent != nil {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	proof, physical, err := d.stable(ctx, attempt, effect, guard)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, physical.Predecessor.Selection, attempt) ||
		!gatewayRebindFinalHandoverReadyForCutover(proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return proof, nil
}

func (d gatewayRebindTypedHandoverRuntime) serveSuccessor(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection,
	effect gatewayRebindTypedEffectProgress, mode gatewayRebindPhysicalReconcileMode,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverObservation, error) {
	invalid := func() (gatewayRebindFinalHandoverObservation, error) {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if mode == gatewayRebindPhysicalReconcileRollbackOnly || effect.HandoverIntent == nil ||
		effect.FinalContainer == nil || effect.CutoverIntent == nil || effect.SuccessorServing != nil {
		return invalid()
	}
	proof, physical, err := d.stable(ctx, attempt, effect, guard)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, physical.Predecessor.Selection, attempt) {
		return invalid()
	}
	if gatewayRebindFinalHandoverSuccessorIsServing(proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
		return proof, nil
	}
	if !gatewayRebindFinalHandoverReadyForCutover(proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
		return invalid()
	}
	if physical.Predecessor.Running {
		if guard(ctx) != nil {
			return invalid()
		}
		stopErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"container", "stop", "--time", "10", physical.Predecessor.ContainerID)
		proof, physical, err = d.stable(ctx, attempt, effect, guard)
		if err != nil || physical.Predecessor.Running || proof.Final != gatewayRebindHandoverContainerStopped ||
			!validSHA256(proof.PredecessorStopDigest) {
			_ = stopErr
			return invalid()
		}
	}
	if guard(ctx) != nil {
		return invalid()
	}
	startErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
		"container", "start", effect.FinalContainer.ID)
	proof, physical, err = d.stable(ctx, attempt, effect, guard)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, physical.Predecessor.Selection, attempt) ||
		!gatewayRebindFinalHandoverSuccessorIsServing(proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
		_ = startErr
		return invalid()
	}
	return proof, nil
}

func (d gatewayRebindTypedHandoverRuntime) finalizeSuccessor(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection, previous gatewayRebindProgressRecord,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverResourceBindings, gatewayRebindFinalHandoverTerminalProof, error) {
	invalid := func() (gatewayRebindFinalHandoverResourceBindings, gatewayRebindFinalHandoverTerminalProof, error) {
		return gatewayRebindFinalHandoverResourceBindings{}, gatewayRebindFinalHandoverTerminalProof{},
			gatewayRebindEffectBoundaryError(ctx)
	}
	if previous.Sequence != 16 || effect.HandoverIntent == nil || effect.FinalContainer == nil ||
		effect.SuccessorServing == nil || effect.Resources != nil || effect.PhysicalProof != nil {
		return invalid()
	}
	proofObservation, physical, err := d.stable(ctx, attempt, effect, guard)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, physical.Predecessor.Selection, attempt) ||
		!reflect.DeepEqual(proofObservation, effect.SuccessorServing.Observation) ||
		!gatewayRebindFinalHandoverSuccessorIsServing(proofObservation,
			effect.HandoverIntent.Plan, *effect.FinalContainer) {
		return invalid()
	}
	resources := gatewayRebindFinalHandoverResourceBindings{ImageID: effect.ImageID,
		IngressNetwork: *effect.Network, ConfigVolume: *effect.ConfigVolume, DataVolume: *effect.DataVolume,
		StageContainer: *effect.StageContainer, FinalContainer: effect.FinalContainer,
		ApplicationNetworks: append([]gatewayRebindHandoverApplicationNetwork(nil), effect.ApplicationNetworks...)}
	resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
	if err != nil || !validGatewayRebindFinalHandoverResourceBindingsValue(resources) {
		return invalid()
	}
	proof := gatewayRebindFinalHandoverTerminalProof{Version: gatewayRebindFinalHandoverProgressVersion,
		Context: gatewayRebindFinalHandoverOutcomeContext, Kind: gatewayRebindFinalHandoverOutcomeCommit,
		PriorProgressDigest: previous.Digest, Observation: proofObservation}
	proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	if err != nil || !validGatewayRebindFinalHandoverOutcomeValue(proof) || guard(ctx) != nil {
		return invalid()
	}
	return resources, proof, nil
}

func gatewayRebindTypedEffectForRollback(records []gatewayRebindProgressRecord,
	rollback gatewayRebindTypedRollbackProgress,
) (gatewayRebindTypedEffectProgress, error) {
	invalid := errors.New("generated ingress typed rollback source is unavailable")
	if !validGatewayRebindTypedRollbackProgress(rollback) {
		return gatewayRebindTypedEffectProgress{}, invalid
	}
	for _, record := range records {
		if record.Sequence != rollback.FromSequence || record.Digest != rollback.FromDigest ||
			record.Phase != rollback.FromPhase {
			continue
		}
		if record.Sequence == 1 && record.TypedEffect == nil {
			return gatewayRebindTypedEffectProgress{}, nil
		}
		if record.TypedEffect == nil || record.TypedRollback != nil {
			return gatewayRebindTypedEffectProgress{}, invalid
		}
		return *record.TypedEffect, nil
	}
	return gatewayRebindTypedEffectProgress{}, invalid
}

func gatewayRebindTypedStageObservationMatchesEffect(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
) bool {
	if !gatewayRebindTypedStageResourcePrefixMatches(intent, effect, value) || value.FinalContainerFound {
		return false
	}
	if effect.StageContainer == nil {
		return gatewayRebindTypedStagePrefixMatches(intent, effect, value)
	}
	return gatewayRebindTypedStoppedStageContainerMatches(intent, effect, value) ||
		gatewayRebindTypedRunningStageMatches(intent, effect, value)
}

func (d gatewayRebindTypedHandoverRuntime) stableRollbackPrefix(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedStageObservation, error) {
	if d.manager == nil || guard == nil || guard(ctx) != nil {
		return gatewayRebindTypedStageObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	matches := func(value gatewayRebindTypedStageObservation) bool {
		if effect.ImageID == "" {
			return gatewayRebindTypedStageImageMatches(attempt.Intent, value) && !value.NetworkFound &&
				!value.ConfigVolumeFound && !value.DataVolumeFound && !value.StageContainerFound &&
				!value.FinalContainerFound && len(value.OwnedContainers) == 0 &&
				len(value.OwnedVolumes) == 0 && len(value.OwnedNetworks) == 0
		}
		return gatewayRebindTypedStageObservationMatchesEffect(attempt.Intent, effect, value)
	}
	first, err := d.stage.read(ctx, attempt.Intent)
	if err != nil || !d.stage.networkTopologyMatches(ctx, attempt.Intent, first) || !matches(first) {
		return gatewayRebindTypedStageObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := d.stage.read(ctx, attempt.Intent)
	if err != nil || !reflect.DeepEqual(first, second) ||
		!d.stage.networkTopologyMatches(ctx, attempt.Intent, second) ||
		!matches(second) ||
		ctx.Err() != nil || guard(ctx) != nil {
		return gatewayRebindTypedStageObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return second, nil
}

func gatewayRebindTypedRollbackAdoptionDigest(intent gatewayRebindProtectedIntentV2,
	last gatewayRebindProgressRecord, owned gatewayRebindTypedRollbackOwnedResources,
	stage gatewayRebindTypedStageObservation, finalID string,
) (string, error) {
	return canonicalDigest(struct {
		Purpose  string                                   `json:"purpose"`
		Intent   string                                   `json:"protectedIntentDigest"`
		Progress string                                   `json:"progressDigest"`
		Owned    gatewayRebindTypedRollbackOwnedResources `json:"owned"`
		Stage    gatewayRebindTypedStageObservation       `json:"stage"`
		FinalID  string                                   `json:"finalId,omitempty"`
	}{"hostd/generated-ingress/rebind/typed-rollback-adoption/v1", intent.Digest, last.Digest,
		owned, stage, finalID})
}

func (d gatewayRebindTypedHandoverRuntime) prepareRollback(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection,
	progress []gatewayRebindProgressRecord, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedRollbackPreparation, error) {
	invalid := func() (gatewayRebindTypedRollbackPreparation, error) {
		return gatewayRebindTypedRollbackPreparation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || guard == nil || len(progress) == 0 || progress[len(progress)-1].TypedRollback != nil ||
		guard(ctx) != nil {
		return invalid()
	}
	last := progress[len(progress)-1]
	effect := gatewayRebindTypedEffectProgress{}
	if last.TypedEffect != nil {
		effect = *last.TypedEffect
	} else if last.Sequence != 1 {
		return invalid()
	}
	bound := gatewayRebindTypedRollbackOwnedFromEffect(effect)
	planned := bound
	stage, err := d.stage.read(ctx, attempt.Intent)
	if err != nil || !d.stage.networkTopologyMatches(ctx, attempt.Intent, stage) {
		return invalid()
	}
	adoptedFinalID := ""
	switch last.Sequence {
	case 2:
		if planned.IngressNetwork == nil && stage.NetworkFound {
			binding, buildErr := gatewayRebindTypedStageNetworkBindingFor(attempt.Intent, stage.NetworkID)
			if buildErr != nil {
				return invalid()
			}
			planned.IngressNetwork = &binding
			effect.Network = &binding
		}
	case 3:
		if planned.ConfigVolume == nil && stage.ConfigVolumeFound {
			binding, buildErr := gatewayRebindTypedStageConfigVolumeBindingFor(attempt.Intent,
				stage.ConfigVolumeIdentity.Mountpoint, stage.ConfigVolumeIdentity.CreatedAt)
			if buildErr != nil {
				return invalid()
			}
			planned.ConfigVolume = &binding
			effect.ConfigVolume = &binding
		}
	case 4:
		if planned.DataVolume == nil && stage.DataVolumeFound {
			binding, buildErr := gatewayRebindTypedStageDataVolumeBindingFor(attempt.Intent,
				stage.DataVolumeIdentity.Mountpoint, stage.DataVolumeIdentity.CreatedAt)
			if buildErr != nil {
				return invalid()
			}
			planned.DataVolume = &binding
			effect.DataVolume = &binding
		}
	case 5:
		if planned.StageContainer == nil && stage.StageContainerFound {
			binding, buildErr := gatewayRebindTypedStageContainerBindingFor(attempt.Intent, effect,
				normalizeID(stage.StageContainer.ID))
			if buildErr != nil {
				return invalid()
			}
			planned.StageContainer = &binding
			effect.StageContainer = &binding
		}
	case 13:
		if planned.FinalContainer == nil && stage.FinalContainerFound {
			container, _, found, inspectErr := d.manager.inspectNamedGatewayContainer(ctx,
				attempt.Intent.Identity.FinalContainer)
			if inspectErr != nil || !found || !validContainerID(container.ID) || effect.HandoverIntent == nil {
				return invalid()
			}
			binding, buildErr := gatewayRebindTypedFinalContainerBindingFor(attempt.Intent, effect,
				effect.HandoverIntent.Plan, normalizeID(container.ID))
			if buildErr != nil {
				return invalid()
			}
			planned.FinalContainer = &binding
			effect.FinalContainer = &binding
			adoptedFinalID = binding.ID
		}
	}
	if !gatewayRebindTypedRollbackOwnedContains(bound, planned, last.Sequence) {
		return invalid()
	}
	confirmedStage := stage
	if planned.FinalContainer != nil {
		physical, stableErr := d.stableResources(ctx, attempt, effect, false, true, guard)
		if stableErr != nil {
			return invalid()
		}
		confirmedStage = physical.Stage
	} else if last.Sequence == 13 && planned.StageContainer != nil && !stage.StageContainerFound &&
		!stage.FinalContainerFound {
		// The retained handover intent authorizes replacing the stage with the
		// final container. A crash after the exact stage removal and before the
		// final create leaves neither container present. Keep the historical
		// stage binding in the rollback plan, but prove precisely this one
		// forward-effect gap before installing the rollback intent.
		gap := effect
		gap.StageContainer = nil
		gap.ApplicationNetworks = nil
		confirmedStage, err = d.stableRollbackPrefix(ctx, attempt, gap, guard)
		if err != nil {
			return invalid()
		}
	} else if effect.FinalConfigCopy != nil && effect.FinalConfigIntent != nil && last.Sequence >= 12 {
		physical, stableErr := d.stableResources(ctx, attempt, effect, effect.StageContainer != nil, false, guard)
		if stableErr != nil {
			return invalid()
		}
		confirmedStage = physical.Stage
	} else {
		confirmedStage, err = d.stableRollbackPrefix(ctx, attempt, effect, guard)
		if err != nil {
			return invalid()
		}
	}
	predecessor, err := d.predecessor(ctx, attempt)
	if err != nil || predecessor.Address != gatewayRebindPredecessorAddressPresent ||
		!gatewayRebindTypedSelectionAuthorizesAttempt(current, predecessor.Selection, attempt) || guard(ctx) != nil {
		return invalid()
	}
	routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(attempt.Checkpoint)
	if err != nil {
		return invalid()
	}
	adoptionDigest := ""
	if !reflect.DeepEqual(bound, planned) {
		adoptionDigest, err = gatewayRebindTypedRollbackAdoptionDigest(attempt.Intent, last, planned, confirmedStage,
			adoptedFinalID)
		if err != nil {
			return invalid()
		}
	}
	return gatewayRebindTypedRollbackPreparation{Owned: planned, PredecessorRoutesDigest: routesDigest,
		AdoptionProofDigest: adoptionDigest}, nil
}

func gatewayRebindTypedRollbackProjection(effect gatewayRebindTypedEffectProgress,
	owned gatewayRebindTypedRollbackOwnedResources,
) gatewayRebindTypedEffectProgress {
	result := effect
	result.Network, result.ConfigVolume, result.DataVolume = owned.IngressNetwork, owned.ConfigVolume, owned.DataVolume
	result.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), owned.ApplicationNetworks...)
	result.FinalContainer = owned.FinalContainer
	if owned.FinalContainer == nil {
		result.StageContainer = owned.StageContainer
	}
	return result
}

func cloneGatewayRebindTypedRollbackOwned(value gatewayRebindTypedRollbackOwnedResources) gatewayRebindTypedRollbackOwnedResources {
	result := value
	result.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), value.ApplicationNetworks...)
	return result
}

// gatewayRebindTypedRollbackRemainingCandidates enumerates only the monotone
// removal order authorized by a durable rollback intent. It lets a fresh
// process reconcile a successful delete whose command acknowledgement or the
// final rollback-complete record was lost, without accepting holes, reordered
// deletion, or replacement resources.
func gatewayRebindTypedRollbackRemainingCandidates(
	owned gatewayRebindTypedRollbackOwnedResources,
) []gatewayRebindTypedRollbackOwnedResources {
	current := cloneGatewayRebindTypedRollbackOwned(owned)
	values := []gatewayRebindTypedRollbackOwnedResources{current}
	appendCurrent := func() {
		if !reflect.DeepEqual(values[len(values)-1], current) {
			values = append(values, cloneGatewayRebindTypedRollbackOwned(current))
		}
	}
	if current.FinalContainer != nil {
		current.FinalContainer = nil
		// The final-container creation path has already removed the stage.
		current.StageContainer = nil
		current.ApplicationNetworks = nil
		appendCurrent()
	} else if current.StageContainer != nil {
		current.StageContainer = nil
		current.ApplicationNetworks = nil
		appendCurrent()
	}
	if current.DataVolume != nil {
		current.DataVolume = nil
		appendCurrent()
	}
	if current.ConfigVolume != nil {
		current.ConfigVolume = nil
		appendCurrent()
	}
	if current.IngressNetwork != nil {
		current.IngressNetwork = nil
		appendCurrent()
	}
	return values
}

func (d gatewayRebindTypedHandoverRuntime) rollbackState(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	remaining gatewayRebindTypedRollbackOwnedResources, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedStageObservation, error) {
	projected := gatewayRebindTypedRollbackProjection(effect, remaining)
	if remaining.FinalContainer != nil {
		physical, err := d.stableResources(ctx, attempt, projected, false, true, guard)
		if err != nil {
			return gatewayRebindTypedStageObservation{}, err
		}
		return physical.Stage, nil
	}
	return d.stableRollbackPrefix(ctx, attempt, projected, guard)
}

func (d gatewayRebindTypedHandoverRuntime) rollbackRemaining(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	owned gatewayRebindTypedRollbackOwnedResources, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedRollbackOwnedResources, error) {
	var matched *gatewayRebindTypedRollbackOwnedResources
	for _, candidate := range gatewayRebindTypedRollbackRemainingCandidates(owned) {
		if _, err := d.rollbackState(ctx, attempt, effect, candidate, guard); err != nil {
			continue
		}
		if matched != nil {
			return gatewayRebindTypedRollbackOwnedResources{}, gatewayRebindEffectBoundaryError(ctx)
		}
		copy := cloneGatewayRebindTypedRollbackOwned(candidate)
		matched = &copy
	}
	if matched == nil || guard(ctx) != nil {
		return gatewayRebindTypedRollbackOwnedResources{}, gatewayRebindEffectBoundaryError(ctx)
	}
	// Reattest the chosen exact projection after classification. A changing
	// inventory may not be normalized into a later removal prefix.
	if _, err := d.rollbackState(ctx, attempt, effect, *matched, guard); err != nil {
		return gatewayRebindTypedRollbackOwnedResources{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return *matched, nil
}

func (d gatewayRebindTypedHandoverRuntime) rollbackRemoveContainer(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	remaining gatewayRebindTypedRollbackOwnedResources, final bool, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedRollbackOwnedResources, error) {
	id := ""
	if final && remaining.FinalContainer != nil {
		id = remaining.FinalContainer.ID
	} else if !final && remaining.StageContainer != nil {
		id = remaining.StageContainer.ID
	} else {
		return remaining, nil
	}
	if guard(ctx) != nil {
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	state, err := d.rollbackState(ctx, attempt, effect, remaining, guard)
	if err != nil {
		return remaining, err
	}
	running := false
	if final {
		container, _, found, inspectErr := d.manager.inspectNamedGatewayContainer(ctx,
			attempt.Intent.Identity.FinalContainer)
		if inspectErr != nil || !found || normalizeID(container.ID) != id || guard(ctx) != nil {
			return remaining, gatewayRebindEffectBoundaryError(ctx)
		}
		running = container.Running
	} else {
		running = state.StageContainer.Running
	}
	if running {
		stopErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"container", "stop", "--time", "10", id)
		state, err = d.rollbackState(ctx, attempt, effect, remaining, guard)
		if err != nil || (!final && state.StageContainer.Running) {
			_ = stopErr
			return remaining, gatewayRebindEffectBoundaryError(ctx)
		}
		if final {
			container, _, found, inspectErr := d.manager.inspectNamedGatewayContainer(ctx,
				attempt.Intent.Identity.FinalContainer)
			if inspectErr != nil || !found || normalizeID(container.ID) != id || container.Running {
				_ = stopErr
				return remaining, gatewayRebindEffectBoundaryError(ctx)
			}
		}
	}
	if guard(ctx) != nil {
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	removeErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "rm", id)
	if final {
		remaining.FinalContainer = nil
		remaining.ApplicationNetworks = nil
		// The stage binding is retained in the immutable rollback plan, but the
		// final-container path has already removed that physical stage.
		remaining.StageContainer = nil
	} else {
		remaining.StageContainer = nil
		remaining.ApplicationNetworks = nil
	}
	if _, err = d.rollbackState(ctx, attempt, effect, remaining, guard); err != nil {
		_ = removeErr
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	return remaining, nil
}

func (d gatewayRebindTypedHandoverRuntime) rollbackRemoveVolume(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	remaining gatewayRebindTypedRollbackOwnedResources, data bool, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedRollbackOwnedResources, error) {
	name := ""
	if data && remaining.DataVolume != nil {
		name = remaining.DataVolume.Name
	} else if !data && remaining.ConfigVolume != nil {
		name = remaining.ConfigVolume.Name
	} else {
		return remaining, nil
	}
	if guard(ctx) != nil {
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	if _, err := d.rollbackState(ctx, attempt, effect, remaining, guard); err != nil {
		return remaining, err
	}
	removeErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "volume", "rm", name)
	if data {
		remaining.DataVolume = nil
	} else {
		remaining.ConfigVolume = nil
	}
	if _, err := d.rollbackState(ctx, attempt, effect, remaining, guard); err != nil {
		_ = removeErr
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	return remaining, nil
}

func (d gatewayRebindTypedHandoverRuntime) rollbackRemoveNetwork(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, effect gatewayRebindTypedEffectProgress,
	remaining gatewayRebindTypedRollbackOwnedResources, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedRollbackOwnedResources, error) {
	if remaining.IngressNetwork == nil {
		return remaining, nil
	}
	if guard(ctx) != nil {
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	if _, err := d.rollbackState(ctx, attempt, effect, remaining, guard); err != nil {
		return remaining, err
	}
	removeErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
		"network", "rm", remaining.IngressNetwork.ID)
	remaining.IngressNetwork = nil
	if _, err := d.rollbackState(ctx, attempt, effect, remaining, guard); err != nil {
		_ = removeErr
		return remaining, gatewayRebindEffectBoundaryError(ctx)
	}
	return remaining, nil
}

func (d gatewayRebindTypedHandoverRuntime) rollbackSuccessor(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, current gatewayCurrentSelection, rollbackRecord gatewayRebindProgressRecord,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverTerminalProof, error) {
	invalid := func() (gatewayRebindFinalHandoverTerminalProof, error) {
		return gatewayRebindFinalHandoverTerminalProof{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || guard == nil || rollbackRecord.Phase != gatewayRebindProgressRollbackIntent ||
		rollbackRecord.TypedRollback == nil || rollbackRecord.TypedRollback.PhysicalProof != nil || guard(ctx) != nil {
		return invalid()
	}
	history, err := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return invalid()
	}
	rollback := *rollbackRecord.TypedRollback
	effect, err := gatewayRebindTypedEffectForRollback(historyProgressRecords(history,
		attempt.Intent.Generation, attempt.Intent.OperationID), rollback)
	if err != nil {
		return invalid()
	}
	predecessor, err := d.predecessor(ctx, attempt)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, predecessor.Selection, attempt) ||
		predecessor.Address != gatewayRebindPredecessorAddressPresent {
		return invalid()
	}
	remaining, err := d.rollbackRemaining(ctx, attempt, effect, rollback.Owned, guard)
	if err != nil {
		return invalid()
	}
	if remaining.FinalContainer != nil {
		remaining, err = d.rollbackRemoveContainer(ctx, attempt, effect, remaining, true, guard)
	} else if remaining.StageContainer != nil {
		remaining, err = d.rollbackRemoveContainer(ctx, attempt, effect, remaining, false, guard)
	}
	if err == nil {
		remaining, err = d.rollbackRemoveVolume(ctx, attempt, effect, remaining, true, guard)
	}
	if err == nil {
		remaining, err = d.rollbackRemoveVolume(ctx, attempt, effect, remaining, false, guard)
	}
	if err == nil {
		remaining, err = d.rollbackRemoveNetwork(ctx, attempt, effect, remaining, guard)
	}
	if err != nil || !reflect.DeepEqual(remaining, gatewayRebindTypedRollbackOwnedResources{}) {
		return invalid()
	}
	predecessor, err = d.predecessor(ctx, attempt)
	if err != nil || !gatewayRebindTypedSelectionAuthorizesAttempt(current, predecessor.Selection, attempt) ||
		predecessor.Address != gatewayRebindPredecessorAddressPresent {
		return invalid()
	}
	if !predecessor.Running {
		if guard(ctx) != nil {
			return invalid()
		}
		startErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"container", "start", predecessor.ContainerID)
		predecessor, err = d.predecessor(ctx, attempt)
		if err != nil || !predecessor.Running || predecessor.Address != gatewayRebindPredecessorAddressPresent ||
			!gatewayRebindTypedSelectionAuthorizesAttempt(current, predecessor.Selection, attempt) || guard(ctx) != nil {
			_ = startErr
			return invalid()
		}
	}
	routesDigest, routesErr := gatewayRebindTypedCheckpointRoutesDigest(attempt.Checkpoint)
	if routesErr != nil || rollback.PredecessorRoutesDigest != routesDigest || !validSHA256(predecessor.Routes) {
		return invalid()
	}
	emptyEffect := gatewayRebindTypedEffectProgress{ImageID: effect.ImageID, StagePlanDigest: effect.StagePlanDigest}
	if effect.ImageID == "" {
		emptyEffect = gatewayRebindTypedEffectProgress{}
	}
	stage, err := d.stage.read(ctx, attempt.Intent)
	if err != nil || !d.stage.networkTopologyMatches(ctx, attempt.Intent, stage) || stage.NetworkFound ||
		stage.ConfigVolumeFound || stage.DataVolumeFound || stage.StageContainerFound || stage.FinalContainerFound ||
		len(stage.OwnedContainers) != 0 || len(stage.OwnedVolumes) != 0 || len(stage.OwnedNetworks) != 0 || guard(ctx) != nil {
		return invalid()
	}
	if emptyEffect.ImageID != "" && (!gatewayRebindTypedStageImageMatches(attempt.Intent, stage) ||
		stage.Image.ID != emptyEffect.ImageID) {
		return invalid()
	}
	observation := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent,
		Final: gatewayRebindHandoverContainerAbsent, PredecessorRunning: true,
		PredecessorAddress:           gatewayRebindPredecessorAddressPresent,
		PredecessorObservationDigest: predecessor.Observation,
		PredecessorRoutesDigest:      rollback.PredecessorRoutesDigest}
	observation.HostDigest, err = canonicalDigest(struct {
		Purpose string                               `json:"purpose"`
		Network gatewayRebindSuccessorIntentNetwork  `json:"network"`
		Address gatewayRebindPredecessorAddressState `json:"predecessorAddress"`
	}{"hostd/generated-ingress/rebind/typed-rollback-host/v1", attempt.Intent.Network,
		predecessor.Address})
	if err == nil {
		observation.InventoryDigest, err = canonicalDigest(struct {
			Purpose     string                              `json:"purpose"`
			Intent      string                              `json:"protectedIntentDigest"`
			Predecessor string                              `json:"predecessorObservationDigest"`
			Network     gatewayRebindSuccessorIntentNetwork `json:"network"`
		}{"hostd/generated-ingress/rebind/typed-rollback-inventory/v1", attempt.Intent.Digest,
			predecessor.Observation, attempt.Intent.Network})
	}
	if err == nil {
		observation.ApplicationEndpointsDigest, err = canonicalDigest(struct {
			Purpose     string `json:"purpose"`
			Predecessor string `json:"predecessorObservationDigest"`
		}{"hostd/generated-ingress/rebind/typed-rollback-endpoints/v1", predecessor.Observation})
	}
	if err == nil {
		observation.Digest, err = gatewayRebindFinalHandoverObservationDigest(observation)
	}
	proof := gatewayRebindFinalHandoverTerminalProof{Version: gatewayRebindFinalHandoverProgressVersion,
		Context: gatewayRebindFinalHandoverOutcomeContext, Kind: gatewayRebindFinalHandoverOutcomeAbort,
		PriorProgressDigest: rollbackRecord.Digest, Observation: observation}
	if err == nil {
		proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	}
	if err != nil || !validGatewayRebindTypedRollbackPhysicalProof(proof, rollback, rollbackRecord.Digest) ||
		guard(ctx) != nil {
		return invalid()
	}
	return proof, nil
}

func historyProgressRecords(history gatewayRebindProtectedIntentHistory,
	generation uint64, operationID string,
) []gatewayRebindProgressRecord {
	values := make([]gatewayRebindProgressRecord, 0)
	for _, retained := range history.Progress {
		if retained.Generation == generation && retained.Record.OperationID == operationID {
			values = append(values, retained.Record)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Sequence < values[j].Sequence })
	return values
}

var _ gatewayRebindTypedHandoverDriver = gatewayRebindTypedHandoverRuntime{}
var _ gatewayRebindTypedRollbackDriver = gatewayRebindTypedHandoverRuntime{}
