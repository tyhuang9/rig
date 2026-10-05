package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sort"
	"strings"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindHandoverPhysical struct {
	Stage        gatewayRebindStageContainerObservation
	Final        caddyInspection
	FinalRuntime gatewayContainerRuntime
	Predecessor  gatewayV2DockerObservation
	Applications map[string]caddyNetworkInspection
}

func (d managerGatewayRebindFinalHandoverDriver) readHandover(ctx context.Context,
	value gatewayRebindFinalHandoverContext,
) (gatewayRebindFinalHandoverObservation, error) {
	invalid := func() (gatewayRebindFinalHandoverObservation, error) {
		return gatewayRebindFinalHandoverObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil || d.inspectDocker == nil ||
		d.reads.network.candidates == nil || d.reads.network.host == nil || d.reads.network.docker == nil || d.reads.dockerIDs == nil ||
		!validGatewayRebindFinalHandoverBase(value) || (value.Plan != nil && !validGatewayRebindFinalHandoverPlan(value, *value.Plan)) ||
		(value.Final != nil && !validGatewayRebindFinalContainerBinding(value, *value.Final)) {
		return invalid()
	}
	physical := gatewayRebindHandoverPhysical{}
	var err error
	physical.Stage, err = d.inspect(ctx, value.Intent)
	if err != nil {
		return invalid()
	}
	var finalFound bool
	physical.Final, physical.FinalRuntime, finalFound, err = d.manager.inspectNamedGatewayContainer(ctx, value.Intent.Intent.Identity.FinalContainer)
	if err != nil || finalFound != physical.Stage.FinalContainerFound || (finalFound && physical.Stage.StageContainerFound) {
		return invalid()
	}
	physical.Predecessor, err = d.inspectDocker(ctx, value.Source, value.Predecessor.State, value.Predecessor.Journal)
	defer clearGatewayV2DockerObservation(&physical.Predecessor)
	if err != nil || !validGatewayRebindPredecessorDocker(value.Source, value.Predecessor.State, value.Predecessor.Journal, physical.Predecessor) ||
		normalizeID(physical.Predecessor.Image.ID) != value.SequenceTwelve.Stage.ObservedDockerImageID {
		return invalid()
	}
	proof := gatewayRebindFinalHandoverObservation{
		Stage: gatewayRebindHandoverContainerAbsent, Final: gatewayRebindHandoverContainerAbsent,
		PredecessorRunning:  physical.Predecessor.FinalContainer.Running,
		ConfigVolumePresent: physical.Stage.ConfigVolumeFound, DataVolumePresent: physical.Stage.DataVolumeFound,
		IngressNetworkPresent: physical.Stage.NetworkFound,
	}
	if physical.Stage.StageContainerFound {
		switch classifyGatewayRebindStageRuntime(value.Intent, *value.SequenceTwelve.Stage, physical.Stage) {
		case gatewayRebindStageRuntimeStopped:
			proof.Stage = gatewayRebindHandoverContainerStopped
		case gatewayRebindStageRuntimeServing:
			if physical.Stage.StageRuntime.ConfiguredNetworks[value.Intent.Intent.Identity.IngressNetwork].EndpointID != value.SequenceTwelve.Stage.StageServing.EndpointID {
				return invalid()
			}
			proof.Stage = gatewayRebindHandoverContainerRunning
		default:
			return invalid()
		}
	}
	if finalFound {
		id := value.CreatedFinalID
		if value.Final != nil {
			if id != "" {
				return invalid()
			}
			id = value.Final.ID
		}
		if !validGatewayRebindFinalHandoverContainer(value, physical.Final, physical.FinalRuntime, id) {
			return invalid()
		}
		proof.Final, proof.FinalID = gatewayRebindHandoverContainerStopped, id
		if physical.Final.Running {
			proof.Final = gatewayRebindHandoverContainerRunning
		}
	} else if value.CreatedFinalID != "" {
		return invalid()
	}
	if !validGatewayRebindHandoverResources(value, physical, proof) || !d.handoverVolumeUsersExact(ctx, value, proof) {
		return invalid()
	}
	proof.HostDigest, proof.PredecessorAddress, err = d.handoverHostProof(ctx, value, proof.IngressNetworkPresent)
	if err != nil {
		return invalid()
	}
	proof.ApplicationNetworks, physical.Applications, proof.ApplicationEndpointsDigest, err = d.handoverApplicationProof(ctx, value, physical, proof)
	if err != nil || (value.Plan != nil && !reflect.DeepEqual(proof.ApplicationNetworks, value.Plan.ApplicationNetworks)) {
		return invalid()
	}
	// Membership is validated against the complete immutable joint roster above.
	// Exclude only the proved successor from the predecessor's dynamic census.
	predecessor := physical.Predecessor
	predecessor.ApplicationNetworks = gatewayRebindWithoutFinalMember(predecessor.ApplicationNetworks, proof.FinalID)
	predecessor.V1ApplicationNetworks = gatewayRebindWithoutFinalMember(predecessor.V1ApplicationNetworks, proof.FinalID)
	proof.PredecessorObservationDigest, err = gatewayRebindPredecessorDockerDigest(predecessor, value.Predecessor.State.Identity)
	if err != nil {
		return invalid()
	}
	if !proof.PredecessorRunning {
		proof.PredecessorStopDigest = proof.PredecessorObservationDigest
	}
	containerID := ""
	if physical.Stage.StageContainerFound {
		containerID = value.SequenceTwelve.Stage.StageContainer.ID
	}
	if finalFound {
		containerID = proof.FinalID
	}
	if containerID != "" {
		if !d.handoverConfigPair(ctx, value, containerID, proof.Final == gatewayRebindHandoverContainerRunning) {
			return invalid()
		}
		proof.ConfigDigest = value.SequenceTwelve.Stage.FinalConfigIntent.ContentDigest
	}
	if proof.Stage == gatewayRebindHandoverContainerRunning {
		body, bodyErr := gatewayRebindStageConfigBytes(value.Intent)
		live, liveErr := d.liveConfig(ctx, containerID)
		valid := bodyErr == nil && liveErr == nil && sameCaddyConfig(body, live)
		clear(body)
		clear(live)
		if !valid || !proveGatewayRebindStagePublication(ctx, *value.SequenceTwelve.Stage.StageStartIntent, containerID, d.hostProbe, d.containerProbe) {
			return invalid()
		}
	}
	if !d.proveHandoverWithdrawnBindings(ctx, value, proof) {
		return invalid()
	}
	if proof.Final == gatewayRebindHandoverContainerRunning && d.proveHandoverFinalRoutes(ctx, value, proof.FinalID) {
		proof.RoutesDigest = value.Plan.RoutePlanDigest
	}
	if gatewayRebindHandoverMayProvePredecessorRoutes(value.Phase) && proof.PredecessorRunning &&
		proof.PredecessorAddress == gatewayRebindPredecessorAddressPresent && proof.Final != gatewayRebindHandoverContainerRunning &&
		d.manager.proveGatewayV2FinalRoutes(ctx, value.Predecessor.State, value.Predecessor.Journal.Resources.FinalContainerID) &&
		proveGatewayV2FinalHostPublication(ctx, value.Predecessor.State, value.Predecessor.Journal.Resources.FinalContainerID, d.hostProbe, d.containerProbe) &&
		proveGatewayV2FinalLoopbackRoutes(ctx, value.Predecessor.State, value.Predecessor.Journal, d.hostProbe) {
		proof.PredecessorRoutesDigest, err = canonicalDigest(value.Predecessor.State.Apps)
		if err != nil {
			return invalid()
		}
	}
	// Normalize only a separately validated unordered mount set. Retain every
	// observed network member in this phase-specific inventory digest.
	physical.Stage.StageContainer.Mounts = nil
	physical.Final.Mounts = nil
	physical.Predecessor.FinalContainer.Mounts = nil
	proof.InventoryDigest, err = canonicalDigest(physical)
	if err != nil {
		return invalid()
	}
	proof.Digest, err = gatewayRebindFinalHandoverObservationDigest(proof)
	if err != nil || !validGatewayRebindFinalHandoverObservationValue(proof) || ctx.Err() != nil {
		return invalid()
	}
	return proof, nil
}

func gatewayRebindHandoverMayProvePredecessorRoutes(phase gatewayRebindProgressPhase) bool {
	switch phase {
	case gatewayRebindProgressCutoverIntent, gatewayRebindProgressSuccessorServing,
		gatewayRebindProgressRollbackIntent, gatewayRebindProgressHandoverRolledBack:
		return true
	default:
		return false
	}
}

func gatewayRebindWithoutFinalMember(networks map[string]caddyNetworkInspection, id string) map[string]caddyNetworkInspection {
	if id == "" || len(networks) == 0 {
		return networks
	}
	result := make(map[string]caddyNetworkInspection, len(networks))
	for name, network := range networks {
		if _, exists := network.Containers[id]; !exists {
			result[name] = network
			continue
		}
		members := make(map[string]caddyNetworkContainerInspection, len(network.Containers))
		for memberID, member := range network.Containers {
			if normalizeID(memberID) != id {
				members[memberID] = member
			}
		}
		network.Containers = members
		result[name] = network
	}
	return result
}

func validGatewayRebindHandoverResources(value gatewayRebindFinalHandoverContext, physical gatewayRebindHandoverPhysical,
	proof gatewayRebindFinalHandoverObservation,
) bool {
	observed, stage, intent := physical.Stage, value.SequenceTwelve.Stage, value.Intent
	containers, volumes, networks := []string{}, []string{}, []string{}
	if observed.StageContainerFound {
		containers = append(containers, intent.Intent.Identity.StageContainer)
	}
	if observed.FinalContainerFound {
		containers = append(containers, intent.Intent.Identity.FinalContainer)
	}
	if observed.ConfigVolumeFound {
		volumes = append(volumes, intent.Intent.Identity.ConfigVolume)
	}
	if observed.DataVolumeFound {
		volumes = append(volumes, intent.Intent.Identity.DataVolume)
	}
	if observed.NetworkFound {
		networks = append(networks, intent.Intent.Identity.IngressNetwork)
	}
	if !validOwnedNameSet(observed.OwnedContainers, containers...) || !validOwnedNameSet(observed.OwnedVolumes, volumes...) ||
		!validOwnedNameSet(observed.OwnedNetworks, networks...) {
		return false
	}
	if observed.ConfigVolumeFound {
		binding, err := gatewayRebindStageConfigVolumeBindingFromObservation(intent, gatewayRebindStageConfigVolumeObservation{
			ConfigVolume: observed.ConfigVolume, ConfigVolumeIdentity: observed.ConfigVolumeIdentity})
		if err != nil || binding != *stage.ConfigVolume || !validGatewayRebindHandoverVolume(observed.ConfigVolume, intent, gatewayV2ConfigVolumeRole) {
			return false
		}
	} else if observed.ConfigVolumeIdentity != (gatewayV1VolumeIdentity{}) || !reflect.DeepEqual(observed.ConfigVolume, volumeInspection{}) {
		return false
	}
	if observed.DataVolumeFound {
		binding, err := gatewayRebindStageDataVolumeBindingFromObservation(intent, gatewayRebindStageDataVolumeObservation{
			DataVolume: observed.DataVolume, DataVolumeIdentity: observed.DataVolumeIdentity})
		if err != nil || binding != *stage.DataVolume || !validGatewayRebindHandoverVolume(observed.DataVolume, intent, gatewayV2DataVolumeRole) {
			return false
		}
	} else if observed.DataVolumeIdentity != (gatewayV1VolumeIdentity{}) || !reflect.DeepEqual(observed.DataVolume, volumeInspection{}) {
		return false
	}
	if !observed.NetworkFound {
		return !observed.StageContainerFound && !observed.FinalContainerFound && observed.NetworkID == "" && reflect.DeepEqual(observed.Network, caddyNetworkInspection{})
	}
	networkOnly := gatewayRebindStageNetworkObservation{Network: observed.Network, ID: observed.NetworkID, Found: true, OwnedNetworks: observed.OwnedNetworks}
	networkOnly.Network.Containers = map[string]caddyNetworkContainerInspection{}
	if !validGatewayRebindStageNetworkObservation(intent, networkOnly, stage.Network.ID) {
		return false
	}
	id, name := "", ""
	if proof.Stage == gatewayRebindHandoverContainerRunning {
		id, name = stage.StageContainer.ID, intent.Intent.Identity.StageContainer
	}
	if proof.Final == gatewayRebindHandoverContainerRunning {
		id, name = proof.FinalID, intent.Intent.Identity.FinalContainer
	}
	if id == "" {
		return len(observed.Network.Containers) == 0
	}
	if len(observed.Network.Containers) != 1 {
		return false
	}
	prefix, err := netip.ParsePrefix(intent.Intent.Network.Subnet)
	if err != nil {
		return false
	}
	for memberID, member := range observed.Network.Containers {
		actual, err := netip.ParsePrefix(member.IPv4Address)
		if err != nil || memberID != id || member.Name != name || actual.Addr().String() != intent.Intent.Network.ContainerIPv4 || actual.Bits() != prefix.Bits() {
			return false
		}
	}
	return true
}

func validGatewayRebindHandoverVolume(volume volumeInspection, intent gatewayRebindProtectedIntent, role string) bool {
	return volume.Driver == "local" && volume.Scope == "local" && len(volume.Options) == 0 &&
		reflect.DeepEqual(volume.Labels, gatewayRebindStageResourceLabels(intent, gatewayV2ManagedContainerLabel, role))
}

func (d managerGatewayRebindFinalHandoverDriver) handoverVolumeUsersExact(ctx context.Context,
	value gatewayRebindFinalHandoverContext, proof gatewayRebindFinalHandoverObservation,
) bool {
	expected := []string{}
	if proof.Stage != gatewayRebindHandoverContainerAbsent {
		expected = append(expected, value.SequenceTwelve.Stage.StageContainer.ID)
	}
	if proof.Final != gatewayRebindHandoverContainerAbsent {
		expected = append(expected, proof.FinalID)
	}
	for _, volume := range []string{value.Intent.Intent.Identity.ConfigVolume, value.Intent.Intent.Identity.DataVolume} {
		result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+volume)
		ids, parseErr := parseGatewayV2DockerNetworkIDs(result.Stdout)
		clearResult(&result)
		if err != nil || parseErr != nil || !equalStrings(ids, expected) {
			return false
		}
	}
	return true
}

func gatewayRebindFinalHandoverAutosaveMode(phase, rollbackFrom gatewayRebindProgressPhase, final, running bool) gatewayRebindFinalConfigAutosaveMode {
	if phase == gatewayRebindProgressRollbackIntent {
		if !gatewayRebindFinalHandoverRollbackSourcePhase(rollbackFrom) {
			return 0
		}
		phase = rollbackFrom
	}
	if !final {
		if phase == gatewayRebindProgressFinalConfigCopied || phase == gatewayRebindProgressFinalHandoverIntent {
			return gatewayRebindFinalConfigStageAutosave
		}
		return 0
	}
	switch phase {
	case gatewayRebindProgressFinalHandoverIntent, gatewayRebindProgressFinalContainerBound:
		if !running {
			return gatewayRebindFinalConfigStageAutosave
		}
	case gatewayRebindProgressCutoverIntent:
		if running {
			return gatewayRebindFinalConfigActiveAutosave
		}
		return gatewayRebindFinalConfigEitherAutosave
	case gatewayRebindProgressSuccessorServing, gatewayRebindProgressHandoverCommitted:
		return gatewayRebindFinalConfigActiveAutosave
	}
	return 0
}

func (d managerGatewayRebindFinalHandoverDriver) handoverConfigPair(ctx context.Context, value gatewayRebindFinalHandoverContext, id string, finalRunning bool) bool {
	mode := gatewayRebindFinalHandoverAutosaveMode(value.Phase, value.RollbackFromPhase,
		id != value.SequenceTwelve.Stage.StageContainer.ID, finalRunning)
	if mode == 0 {
		return false
	}
	stage, err := gatewayRebindStageConfigBytes(value.Intent)
	if err != nil {
		return false
	}
	defer clear(stage)
	active, err := gatewayRebindFinalConfigBytes(value.Intent, value.SequenceTwelve.Stage.FinalConfigIntent.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(active)
	result, err := d.manager.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: d.manager.options.DockerExecutable, Args: []string{"container", "cp", id + ":/config/.", "-"},
		Directory: d.manager.options.WorkingDirectory, Env: append([]string(nil), d.manager.dockerEnv...),
		Timeout: d.manager.options.CommandTimeout, OutputLimit: gatewayRebindFinalConfigArchiveLimit,
	})
	defer clearResult(&result)
	if err != nil || ctx.Err() != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return false
	}
	inventory, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(result.Stdout, stage, active, mode)
	return err == nil && inventory == gatewayRebindFinalConfigInventoryExactPair
}

func (d managerGatewayRebindFinalHandoverDriver) handoverHostProof(ctx context.Context,
	value gatewayRebindFinalHandoverContext, networkPresent bool,
) (string, gatewayRebindPredecessorAddressState, error) {
	invalid := errors.New("generated ingress rebind handover host observation is unavailable")
	candidates, err := d.reads.network.candidates()
	if err != nil {
		return "", "", invalid
	}
	host, err := d.reads.network.host()
	if err != nil {
		return "", "", invalid
	}
	ids, err := d.reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(ids) {
		return "", "", invalid
	}
	prefixes, err := d.reads.network.docker(ctx)
	if err != nil {
		return "", "", invalid
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(prefixes)
	if err != nil {
		return "", "", invalid
	}
	routes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return "", "", invalid
	}
	interfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return "", "", invalid
	}
	projection := gatewayRebindCandidateProjection(candidates)
	expectedIDs := append([]string{}, value.Intent.NetworkObservation.DockerNetworkIDs...)
	expectedPrefixes := append([]string{}, value.Intent.NetworkObservation.DockerPrefixes...)
	if networkPresent {
		expectedIDs = append(expectedIDs, value.SequenceTwelve.Stage.Network.ID)
		expectedPrefixes = append(expectedPrefixes, value.Intent.Intent.Network.Subnet)
		if !validGatewayRebindStageNetworkHostDelta(value.Intent, projection, routes, interfaces) {
			return "", "", invalid
		}
	} else if !reflect.DeepEqual(projection, value.Intent.NetworkObservation.Candidates) ||
		!equalStrings(routes, value.Intent.NetworkObservation.HostRoutes) || !equalStrings(interfaces, value.Intent.NetworkObservation.HostInterfaces) {
		return "", "", invalid
	}
	sort.Strings(expectedIDs)
	sort.Strings(expectedPrefixes)
	if !equalStrings(ids, expectedIDs) || !equalStrings(dockerPrefixes, expectedPrefixes) {
		return "", "", invalid
	}
	if _, ok := selectGatewayV2Candidate(candidates, value.Intent.Intent.SuccessorProfile.InterfaceID, value.Intent.Intent.SuccessorProfile.SelectedIPv4); !ok {
		return "", "", invalid
	}
	predecessorAddress := gatewayRebindPredecessorAddressAbsent
	count, exact := 0, false
	for _, candidate := range candidates {
		if candidate.IPv4 == value.Predecessor.State.Profile.SelectedIPv4 {
			count++
			exact = candidate.InterfaceID == value.Predecessor.State.Profile.InterfaceID
		}
	}
	if count == 1 && exact {
		predecessorAddress = gatewayRebindPredecessorAddressPresent
	} else if count != 0 {
		predecessorAddress = gatewayRebindPredecessorAddressAmbiguous
	}
	digest, err := canonicalDigest(struct {
		Candidates                                    []gatewayRebindSuccessorNetworkCandidate
		Routes, Interfaces, DockerIDs, DockerPrefixes []string
	}{projection, routes, interfaces, ids, dockerPrefixes})
	return digest, predecessorAddress, err
}

func (d managerGatewayRebindFinalHandoverDriver) handoverApplicationProof(ctx context.Context,
	value gatewayRebindFinalHandoverContext, physical gatewayRebindHandoverPhysical, proof gatewayRebindFinalHandoverObservation,
) ([]gatewayRebindHandoverApplicationNetwork, map[string]caddyNetworkInspection, string, error) {
	invalid := errors.New("generated ingress rebind handover application membership is unavailable")
	routes := gatewayRebindFinalHandoverRoutes(value)
	owners, ok := gatewayRouteNetworkOwners(routes)
	if !ok {
		return nil, nil, "", invalid
	}
	networks := make(map[string]caddyNetworkInspection, len(owners))
	bindings := make([]gatewayRebindHandoverApplicationNetwork, 0, len(owners))
	for name, appID := range owners {
		network, id, found, err := d.manager.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || !validContainerID(id) || normalizeID(id) != id || !validApplicationNetwork(network.identity(), appID) {
			return nil, nil, "", invalid
		}
		allowed := make(map[string]bool)
		for _, endpoint := range routes[appID].Endpoints {
			if endpoint.NetworkName == name {
				allowed[normalizeID(endpoint.ContainerID)] = true
			}
		}
		if proof.PredecessorRunning {
			allowed[value.Predecessor.Journal.Resources.FinalContainerID] = true
		}
		if proof.Final == gatewayRebindHandoverContainerRunning {
			allowed[proof.FinalID] = true
		}
		if len(network.Containers) != len(allowed) {
			return nil, nil, "", invalid
		}
		for memberID := range network.Containers {
			if !allowed[memberID] {
				return nil, nil, "", invalid
			}
		}
		if physical.Predecessor.ApplicationNetworkIDs[name] != id || !reflect.DeepEqual(network, physical.Predecessor.ApplicationNetworks[name]) {
			return nil, nil, "", invalid
		}
		if proof.Final != gatewayRebindHandoverContainerAbsent {
			configured, exists := physical.FinalRuntime.ConfiguredNetworks[name]
			if !exists || !validGatewayV2StoppedNetworkReference(configured.NetworkID, id) {
				return nil, nil, "", invalid
			}
			if proof.Final == gatewayRebindHandoverContainerRunning && (!validGatewayApplicationNetworkMembership(network, physical.Final, name) || normalizeID(configured.NetworkID) != id) {
				return nil, nil, "", invalid
			}
		}
		networks[name] = network
		bindings = append(bindings, gatewayRebindHandoverApplicationNetwork{Name: name, ID: id})
	}
	sort.Slice(bindings, func(i, j int) bool { return strings.Compare(bindings[i].Name, bindings[j].Name) < 0 })
	digest, err := d.manager.inspectGatewayEndpointIdentitySnapshot(ctx, routes, networks)
	return bindings, networks, digest, err
}
