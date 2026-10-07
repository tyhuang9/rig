package generatedingress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type gatewayRebindTypedStageRuntime struct {
	manager        *Manager
	reads          gatewayRebindSuccessorPreflightReads
	hostProbe      gatewayV2HostStatusProbe
	containerProbe gatewayV2ContainerChallengeProbe
}

type gatewayRebindTypedStageObservation struct {
	Image                imageInspection
	ImageFound           bool
	Network              caddyNetworkInspection
	NetworkID            string
	NetworkFound         bool
	ConfigVolume         volumeInspection
	ConfigVolumeIdentity gatewayV1VolumeIdentity
	ConfigVolumeFound    bool
	DataVolume           volumeInspection
	DataVolumeIdentity   gatewayV1VolumeIdentity
	DataVolumeFound      bool
	StageContainer       caddyInspection
	StageRuntime         gatewayContainerRuntime
	StageContainerFound  bool
	FinalContainerFound  bool
	OwnedContainers      []string
	OwnedVolumes         []string
	OwnedNetworks        []string
}

func (d gatewayRebindTypedStageRuntime) read(ctx context.Context,
	intent gatewayRebindProtectedIntentV2,
) (gatewayRebindTypedStageObservation, error) {
	if d.manager == nil || ctx == nil || ctx.Err() != nil || !validGatewayRebindProtectedIntentV2(intent) {
		return gatewayRebindTypedStageObservation{}, errors.New("invalid typed stage observation input")
	}
	result := gatewayRebindTypedStageObservation{}
	var err error
	result.Image, result.ImageFound, err = d.manager.inspectImage(ctx)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	if result.ImageFound {
		result.Image.ID = normalizeID(result.Image.ID)
	}
	result.Network, result.NetworkID, result.NetworkFound, err =
		d.manager.inspectNamedGatewayNetwork(ctx, intent.Identity.IngressNetwork)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	result.NetworkID = normalizeID(result.NetworkID)
	result.ConfigVolume, result.ConfigVolumeIdentity, result.ConfigVolumeFound, err =
		d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Identity.ConfigVolume)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	result.DataVolume, result.DataVolumeIdentity, result.DataVolumeFound, err =
		d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Identity.DataVolume)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	result.StageContainer, result.StageRuntime, result.StageContainerFound, err =
		d.manager.inspectNamedGatewayContainer(ctx, intent.Identity.StageContainer)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	_, _, result.FinalContainerFound, err = d.manager.inspectNamedGatewayContainer(ctx, intent.Identity.FinalContainer)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	result.OwnedContainers, err = d.manager.inspectGatewayRebindTypedOwnedNames(ctx, intent, "container", "ls", "--all")
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	result.OwnedVolumes, err = d.manager.inspectGatewayRebindTypedOwnedNames(ctx, intent, "volume", "ls")
	if err != nil {
		return gatewayRebindTypedStageObservation{}, err
	}
	result.OwnedNetworks, err = d.manager.inspectGatewayRebindTypedOwnedNames(ctx, intent, "network", "ls")
	return result, err
}

func (d gatewayRebindTypedStageRuntime) networkTopologyMatches(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, observed gatewayRebindTypedStageObservation,
) bool {
	reads := d.reads
	if ctx == nil || ctx.Err() != nil || reads.network.candidates == nil || reads.network.host == nil ||
		reads.network.docker == nil || reads.dockerIDs == nil || !validGatewayRebindProtectedIntentV2(intent) {
		return false
	}
	candidates, err := reads.network.candidates()
	if err != nil {
		return false
	}
	host, err := reads.network.host()
	if err != nil {
		return false
	}
	idsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return false
	}
	prefixes, err := reads.network.docker(ctx)
	if err != nil || ctx.Err() != nil {
		return false
	}
	idsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return false
	}
	routes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return false
	}
	interfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return false
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(prefixes)
	if err != nil {
		return false
	}
	projection := gatewayRebindCandidateProjection(candidates)
	expectedIDs := append([]string(nil), intent.NetworkObservation.DockerNetworkIDs...)
	expectedPrefixes := append([]string(nil), intent.NetworkObservation.DockerPrefixes...)
	if observed.NetworkFound {
		expectedIDs = append(expectedIDs, observed.NetworkID)
		expectedPrefixes = append(expectedPrefixes, intent.Network.Subnet)
		if !validGatewayRebindTypedStageNetworkHostDelta(intent, projection, routes, interfaces) {
			return false
		}
	} else if !reflect.DeepEqual(projection, intent.NetworkObservation.Candidates) ||
		!equalStrings(routes, intent.NetworkObservation.HostRoutes) ||
		!equalStrings(interfaces, intent.NetworkObservation.HostInterfaces) {
		return false
	}
	sort.Strings(expectedIDs)
	sort.Strings(expectedPrefixes)
	if !equalStrings(idsBefore, expectedIDs) || !equalStrings(dockerPrefixes, expectedPrefixes) {
		return false
	}
	profile := gatewayProfileBinding{RevisionID: intent.SuccessorProfile.RevisionID,
		RevisionNumber: intent.SuccessorProfile.RevisionNumber, SpecDigest: intent.SuccessorProfile.SpecDigest,
		SelectedIPv4: intent.SuccessorProfile.SelectedIPv4, InterfaceID: intent.SuccessorProfile.InterfaceID,
		PortStart: intent.SuccessorProfile.PortStart, PortEnd: intent.SuccessorProfile.PortEnd}
	_, ok := selectGatewayV2Candidate(candidates, profile.InterfaceID, profile.SelectedIPv4)
	return ok
}

func validGatewayRebindTypedStageNetworkHostDelta(intent gatewayRebindProtectedIntentV2,
	candidates []gatewayRebindSuccessorNetworkCandidate, routes, interfaces []string,
) bool {
	if !validGatewayRebindProtectedIntentV2(intent) ||
		!gatewayRebindStageNetworkRoutesMatch(routes, intent.NetworkObservation.HostRoutes,
			intent.Network.Subnet, intent.Network.GatewayIPv4) ||
		!gatewayRebindPrefixesMatchBaselineOrPlan(interfaces, intent.NetworkObservation.HostInterfaces,
			intent.Network.Subnet) {
		return false
	}
	if reflect.DeepEqual(candidates, intent.NetworkObservation.Candidates) {
		return true
	}
	if len(candidates) != len(intent.NetworkObservation.Candidates)+1 {
		return false
	}
	bridgeName, err := gatewayRebindTypedStageBridgeName(intent)
	if err != nil {
		return false
	}
	withoutBridge := make([]gatewayRebindSuccessorNetworkCandidate, 0, len(candidates)-1)
	foundBridge := false
	for _, candidate := range candidates {
		index, name, ok := strings.Cut(candidate.InterfaceID, "/")
		parsedIndex, parseErr := strconv.Atoi(index)
		isBridge := ok && parseErr == nil && parsedIndex > 0 && strconv.Itoa(parsedIndex) == index &&
			name == bridgeName && candidate.IPv4 == intent.Network.GatewayIPv4 &&
			candidate.Prefix == intent.Network.Subnet
		if isBridge {
			if foundBridge {
				return false
			}
			foundBridge = true
			continue
		}
		withoutBridge = append(withoutBridge, candidate)
	}
	return foundBridge && reflect.DeepEqual(withoutBridge, intent.NetworkObservation.Candidates)
}

func (m *Manager) inspectGatewayRebindTypedOwnedNames(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, args ...string,
) ([]string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || len(args) < 2 {
		return nil, errors.New("invalid typed rebind ownership inventory input")
	}
	format := "{{.Name}}"
	if args[0] == "container" {
		format = "{{.Names}}"
	}
	args = append(args,
		"--filter", "label="+gatewayV2ManagedLabelKey,
		"--filter", "label="+gatewayV2IdentityLabelKey+"="+gatewayRebindSuccessorIdentityVersion,
		"--filter", "label="+gatewayV2OperationLabelKey+"="+intent.OperationID,
		"--filter", "label="+gatewayRebindIntentDigestLabelKey+"="+intent.Digest,
		"--filter", "label="+gatewayRebindGenerationLabelKey+"="+strconv.FormatUint(intent.Generation, 10),
		"--format", format)
	result, err := m.run(ctx, m.options.CommandTimeout, args...)
	if err != nil {
		return nil, err
	}
	defer clearResult(&result)
	body := strings.TrimSpace(string(result.Stdout))
	if body == "" {
		return []string{}, nil
	}
	values := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r") {
			return nil, errors.New("invalid typed rebind ownership inventory")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("duplicated typed rebind ownership inventory")
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	return values, nil
}

func gatewayRebindTypedStageImageMatches(intent gatewayRebindProtectedIntentV2,
	value gatewayRebindTypedStageObservation,
) bool {
	return value.ImageFound && validGatewayPinnedImage(value.Image, true) &&
		normalizeID(value.Image.ID) == value.Image.ID && value.Image.ID != intent.Identity.CaddyImageDigest
}

func gatewayRebindTypedStageNetworkMatches(intent gatewayRebindProtectedIntentV2,
	value gatewayRebindTypedStageObservation, expected *gatewayRebindStageNetworkBinding,
) bool {
	if expected == nil {
		return !value.NetworkFound && value.NetworkID == "" && reflect.DeepEqual(value.Network, caddyNetworkInspection{})
	}
	bridge, err := gatewayRebindTypedStageBridgeName(intent)
	return err == nil && gatewayRebindTypedStageNetworkBindingMatches(intent, *expected) &&
		value.NetworkFound && value.NetworkID == expected.ID && value.Network.Name == intent.Identity.IngressNetwork &&
		value.Network.Driver == "bridge" && value.Network.Scope == "local" && !value.Network.Internal &&
		reflect.DeepEqual(value.Network.Options, map[string]string{gatewayRebindBridgeNameOptionKey: bridge}) &&
		len(value.Network.IPAM.Config) == 1 && value.Network.IPAM.Config[0] == (networkIPAM{
		Subnet: intent.Network.Subnet, Gateway: intent.Network.GatewayIPv4}) &&
		reflect.DeepEqual(value.Network.Labels, gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole))
}

func gatewayRebindTypedStageVolumeMatches(intent gatewayRebindProtectedIntentV2,
	value volumeInspection, identity gatewayV1VolumeIdentity, found bool,
	expectedName, role string, expected *gatewayRebindStageConfigVolumeBinding,
) bool {
	if !found {
		return expected == nil && reflect.DeepEqual(value, volumeInspection{}) && identity == (gatewayV1VolumeIdentity{})
	}
	if expected == nil {
		return false
	}
	actualDigest, err := gatewayRebindTypedStageVolumeOwnershipDigest(intent, expectedName, role)
	return err == nil && actualDigest == expected.OwnershipDigest && value.Name == expectedName &&
		value.Driver == "local" && value.Scope == "local" && len(value.Options) == 0 &&
		reflect.DeepEqual(value.Labels, gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, role)) && identity.Mountpoint == expected.Mountpoint &&
		identity.CreatedAt == expected.CreatedAt
}

func gatewayRebindTypedStagePrefixMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
) bool {
	if !gatewayRebindTypedStageResourcePrefixMatches(intent, effect, value) {
		return false
	}
	if effect.StageContainer == nil {
		return !value.StageContainerFound && reflect.DeepEqual(value.StageContainer, caddyInspection{}) &&
			reflect.DeepEqual(value.StageRuntime, gatewayContainerRuntime{}) &&
			(!value.NetworkFound || len(value.Network.Containers) == 0)
	}
	return gatewayRebindTypedStoppedStageContainerMatches(intent, effect, value)
}

func gatewayRebindTypedStageResourcePrefixMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
) bool {
	if !gatewayRebindTypedStageImageMatches(intent, value) || value.Image.ID != effect.ImageID ||
		value.FinalContainerFound || !gatewayRebindTypedStageNetworkMatches(intent, value, effect.Network) {
		return false
	}
	var configBinding, dataBinding *gatewayRebindStageConfigVolumeBinding
	if effect.ConfigVolume != nil {
		value := gatewayRebindStageConfigVolumeBinding(*effect.ConfigVolume)
		configBinding = &value
	}
	if effect.DataVolume != nil {
		value := gatewayRebindStageConfigVolumeBinding{Name: effect.DataVolume.Name,
			Mountpoint: effect.DataVolume.Mountpoint, CreatedAt: effect.DataVolume.CreatedAt,
			OwnershipDigest: effect.DataVolume.OwnershipDigest}
		dataBinding = &value
	}
	if !gatewayRebindTypedStageVolumeMatches(intent, value.ConfigVolume, value.ConfigVolumeIdentity,
		value.ConfigVolumeFound, intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole, configBinding) ||
		!gatewayRebindTypedStageVolumeMatches(intent, value.DataVolume, value.DataVolumeIdentity,
			value.DataVolumeFound, intent.Identity.DataVolume, gatewayV2DataVolumeRole, dataBinding) {
		return false
	}
	expectedNetworks, expectedVolumes, expectedContainers := []string{}, []string{}, []string{}
	if effect.Network != nil {
		expectedNetworks = []string{intent.Identity.IngressNetwork}
	}
	if effect.ConfigVolume != nil {
		expectedVolumes = append(expectedVolumes, intent.Identity.ConfigVolume)
	}
	if effect.DataVolume != nil {
		expectedVolumes = append(expectedVolumes, intent.Identity.DataVolume)
		sort.Strings(expectedVolumes)
	}
	if effect.StageContainer != nil {
		expectedContainers = []string{intent.Identity.StageContainer}
	}
	if !equalStrings(value.OwnedNetworks, expectedNetworks) || !equalStrings(value.OwnedVolumes, expectedVolumes) ||
		!equalStrings(value.OwnedContainers, expectedContainers) {
		return false
	}
	return true
}

func gatewayRebindTypedStoppedStageContainerMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
) bool {
	if effect.Network == nil || effect.ConfigVolume == nil || effect.DataVolume == nil || effect.StageContainer == nil ||
		!value.StageContainerFound || value.StageContainer.Running || value.StageContainer.Restarting ||
		value.StageRuntime.Paused || value.StageRuntime.Dead || value.StageRuntime.RestartCount != 0 ||
		normalizeID(value.StageContainer.ID) != effect.StageContainer.ID ||
		normalizeID(value.StageContainer.Image) != effect.ImageID ||
		strings.TrimPrefix(value.StageContainer.Name, "/") != intent.Identity.StageContainer ||
		value.StageContainer.Hostname != intent.Identity.StageHostname || value.StageContainer.User != "1000:1000" ||
		value.StageContainer.NetworkMode != intent.Identity.IngressNetwork || !exactGatewayV2Environment(value.StageContainer.Env) ||
		!value.StageContainer.ReadOnly || value.StageContainer.Privileged || !onlyCaddyCapability(value.StageContainer.CapAdd) ||
		!exactFoldSet(value.StageContainer.CapDrop, "ALL") || !onlyNoNewPrivileges(value.StageContainer.SecurityOpt) ||
		len(value.StageContainer.Binds) != 0 || len(value.StageContainer.Tmpfs) != 0 || value.StageContainer.Memory != 268435456 ||
		value.StageContainer.MemorySwap != 268435456 || value.StageContainer.NanoCPUs != 1_000_000_000 ||
		value.StageContainer.PIDsLimit != 128 || value.StageContainer.LogType != "local" ||
		len(value.StageContainer.LogConfig) != 2 || value.StageContainer.LogConfig["max-size"] != "10m" ||
		value.StageContainer.LogConfig["max-file"] != "3" || value.StageContainer.Restart != gatewayV2StageRestartPolicy ||
		len(value.StageContainer.Entrypoint) != 1 || value.StageContainer.Entrypoint[0] != caddyExecutable ||
		len(value.StageContainer.Cmd) != 3 || value.StageContainer.Cmd[0] != "run" ||
		value.StageContainer.Cmd[1] != "--config" || value.StageContainer.Cmd[2] != "/config/"+intent.Identity.StageConfigFilename ||
		len(value.StageContainer.Ulimits) != 1 || value.StageContainer.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!validGatewayV2ContainerLabels(value.StageContainer.Labels, gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole)) ||
		!validGatewayRebindStageContainerMounts(value.StageContainer.Mounts, intent.Identity) ||
		!gatewayRebindTypedStagePortBindingsMatch(value.StageContainer.PortBindings, intent) ||
		gatewayV2HasEffectivePortBinding(value.StageRuntime.EffectivePortBindings) || len(value.StageRuntime.ConfiguredNetworks) != 1 ||
		len(value.Network.Containers) != 0 {
		return false
	}
	network, ok := value.StageRuntime.ConfiguredNetworks[intent.Identity.IngressNetwork]
	return ok && (network.NetworkID == "" || normalizeID(network.NetworkID) == effect.Network.ID) &&
		network.EndpointID == "" && network.IPAddress == "" && network.IPv6Gateway == "" &&
		network.GwPriority == caddyGatewayPriority && network.IPAMConfig != nil &&
		network.IPAMConfig.IPv4Address == intent.Network.ContainerIPv4 && network.IPAMConfig.IPv6Address == ""
}

func gatewayRebindTypedStagePortBindingsMatch(actual map[string][]map[string]string,
	intent gatewayRebindProtectedIntentV2,
) bool {
	if len(actual) != int(intent.SuccessorProfile.PortEnd-intent.SuccessorProfile.PortStart)+1 {
		return false
	}
	for port := intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		binding := actual[value+"/tcp"]
		if len(binding) != 1 || len(binding[0]) != 2 || binding[0]["HostIp"] != intent.SuccessorProfile.SelectedIPv4 ||
			binding[0]["HostPort"] != value {
			return false
		}
		if port == intent.SuccessorProfile.PortEnd {
			break
		}
	}
	return true
}

func gatewayRebindTypedRunningStageMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindTypedStageObservation,
) bool {
	if effect.StageContainer == nil || effect.Network == nil || !value.StageContainerFound ||
		!value.StageContainer.Running || value.StageContainer.Restarting || value.StageRuntime.Paused ||
		value.StageRuntime.Dead || value.StageRuntime.RestartCount != 0 ||
		!gatewayV2EffectivePortBindingsMatchConfigured(value.StageRuntime.EffectivePortBindings,
			value.StageContainer.PortBindings) || len(value.StageRuntime.ConfiguredNetworks) != 1 ||
		len(value.StageContainer.Networks) != 1 || len(value.Network.Containers) != 1 {
		return false
	}
	configured, ok := value.StageRuntime.ConfiguredNetworks[intent.Identity.IngressNetwork]
	if !ok || normalizeID(configured.NetworkID) != effect.Network.ID || !validContainerID(configured.EndpointID) ||
		normalizeID(configured.EndpointID) != configured.EndpointID || configured.IPAddress != intent.Network.ContainerIPv4 ||
		configured.IPv6Gateway != "" || configured.GwPriority != caddyGatewayPriority || configured.IPAMConfig == nil ||
		configured.IPAMConfig.IPv4Address != intent.Network.ContainerIPv4 || configured.IPAMConfig.IPv6Address != "" {
		return false
	}
	attached := value.StageContainer.Networks[intent.Identity.IngressNetwork]
	if attached == nil || attached.IPAddress != intent.Network.ContainerIPv4 || attached.GwPriority != caddyGatewayPriority ||
		attached.IPv6Gateway != "" {
		return false
	}
	subnet, err := netip.ParsePrefix(intent.Network.Subnet)
	if err != nil {
		return false
	}
	for id, member := range value.Network.Containers {
		prefix, prefixErr := netip.ParsePrefix(member.IPv4Address)
		if prefixErr != nil || normalizeID(id) != effect.StageContainer.ID || member.Name != intent.Identity.StageContainer ||
			prefix.Addr().String() != intent.Network.ContainerIPv4 || prefix.Bits() != subnet.Bits() {
			return false
		}
	}
	stopped := value
	stopped.StageContainer.Running, stopped.StageContainer.Networks = false, nil
	stopped.StageRuntime.EffectivePortBindings = map[string][]map[string]string{}
	configured.EndpointID, configured.IPAddress = "", ""
	stopped.StageRuntime.ConfiguredNetworks = map[string]gatewayV2ConfiguredNetwork{intent.Identity.IngressNetwork: configured}
	stopped.Network.Containers = map[string]caddyNetworkContainerInspection{}
	return gatewayRebindTypedStoppedStageContainerMatches(intent, effect, stopped)
}
