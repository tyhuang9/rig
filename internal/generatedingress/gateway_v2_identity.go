package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	gatewayV2ManagedLabelKey          = "io.rig.managed"
	gatewayV2IdentityLabelKey         = "io.rig.identity-version"
	gatewayV2OperationLabelKey        = "io.rig.operation-id"
	gatewayV2IdentityDigestLabelKey   = "io.rig.identity-digest"
	gatewayV2PlanDigestLabelKey       = "io.rig.plan-digest"
	gatewayV2ResourceRoleLabelKey     = "io.rig.gateway-role"
	gatewayV2ListenerIsolationKey     = "io.rig.listener-isolation"
	gatewayV2ManagedContainerLabel    = "generated-ingress"
	gatewayV2ManagedNetworkLabel      = "generated-ingress-network"
	gatewayV2ConfigVolumeRole         = "config-volume"
	gatewayV2DataVolumeRole           = "data-volume"
	gatewayV2IngressNetworkRole       = "ingress-network"
	gatewayV2StageContainerRole       = "stage"
	gatewayV2FinalContainerRole       = "final"
	gatewayV2ListenerIsolationVersion = "v2"
	gatewayV2ContainerPort            = uint16(8080)
	gatewayV2StageRestartPolicy       = "no"
	gatewayV2FinalRestartPolicy       = "unless-stopped"
	// A protected v1 state is capped at 48 KiB. Generated Caddy config omits
	// several endpoint fields and the v2 LAN matrix adds less than 12 KiB.
	// Keeping the body at 60 KiB leaves room for docker-cp tar framing inside
	// the Manager's default 64 KiB command-output boundary.
	gatewayV2MaxConfigBytes = 60 << 10
)

var gatewayContainerInspectFormat = strings.TrimSuffix(caddyInspectFormat, "}") +
	`,"paused":{{json .State.Paused}},"dead":{{json .State.Dead}},"restartCount":{{json .RestartCount}},"effectivePortBindings":{{json .NetworkSettings.Ports}}}`

// gatewayV2DockerObservation is a read-only snapshot. Its classifier is pure:
// all Docker reads and live policy probes happen before this value is passed to
// classifyGatewayV2Topology. Missing evidence is represented explicitly so it
// can never be confused with a valid empty result.
type gatewayV2DockerObservation struct {
	Image      imageInspection
	ImageFound bool

	V1Container             caddyInspection
	V1Runtime               gatewayContainerRuntime
	V1ContainerFound        bool
	V1Volume                volumeInspection
	V1VolumeFound           bool
	V1VolumeIdentity        gatewayV1VolumeIdentity
	V1Network               caddyNetworkInspection
	V1NetworkFound          bool
	V1NetworkID             string
	V1Config                []byte
	V1RestartConfig         []byte
	V1ApplicationNetworks   map[string]caddyNetworkInspection
	V1ApplicationNetworkIDs map[string]string
	V1Stable                bool
	V1ResourcesStable       bool
	// EndpointIdentityProven requires immutable endpoint IDs, labels, health,
	// and unique aliases. The live observer leaves it false until that proof is
	// implemented; a healthy Caddy container alone cannot prove app isolation.
	V1EndpointIdentityProven bool

	ConfigVolume         volumeInspection
	ConfigVolumeIdentity gatewayV1VolumeIdentity
	ConfigVolumeFound    bool
	DataVolume           volumeInspection
	DataVolumeIdentity   gatewayV1VolumeIdentity
	DataVolumeFound      bool
	IngressNetwork       caddyNetworkInspection
	IngressNetworkID     string
	IngressFound         bool
	V2ResourcesStable    bool

	StageContainer      caddyInspection
	StageRuntime        gatewayContainerRuntime
	StageContainerFound bool
	StageConfig         []byte
	StageRestartConfig  []byte
	Stage404Proven      bool
	// HostPublicationProven is deliberately separate from the in-container
	// route probes. Docker's desired and effective port maps alone do not prove
	// that the selected host address is reachable. The first read-only slice
	// leaves this false until a host-side probe surface is added.
	StageHostPublicationProven  bool
	StageStable                 bool
	FinalContainer              caddyInspection
	FinalRuntime                gatewayContainerRuntime
	FinalContainerFound         bool
	FinalConfig                 []byte
	FinalRestartConfig          []byte
	Final404Proven              bool
	FinalRoutesProven           bool
	FinalHostPublicationProven  bool
	FinalEndpointIdentityProven bool
	FinalStable                 bool

	ApplicationNetworks    map[string]caddyNetworkInspection
	ApplicationNetworkIDs  map[string]string
	OwnedContainers        []string
	OwnedVolumes           []string
	OwnedNetworks          []string
	OwnedInventoriesStable bool
}

type gatewayContainerRuntime struct {
	Paused                bool                           `json:"paused"`
	Dead                  bool                           `json:"dead"`
	RestartCount          int                            `json:"restartCount"`
	EffectivePortBindings map[string][]map[string]string `json:"effectivePortBindings"`
}

type gatewayContainerInspection struct {
	caddyInspection
	gatewayContainerRuntime
}

type gatewayV1VolumeIdentity struct {
	Mountpoint string `json:"mountpoint"`
	CreatedAt  string `json:"createdAt"`
}

type gatewayVolumeIdentityInspection struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Options    map[string]string `json:"options"`
	Labels     map[string]string `json:"labels"`
	Mountpoint string            `json:"mountpoint"`
	CreatedAt  string            `json:"createdAt"`
}

type gatewayNetworkIdentityInspection struct {
	ID         string                                     `json:"Id"`
	Name       string                                     `json:"Name"`
	Driver     string                                     `json:"Driver"`
	Scope      string                                     `json:"Scope"`
	Internal   bool                                       `json:"Internal"`
	Options    map[string]string                          `json:"Options"`
	IPAM       caddyNetworkIPAM                           `json:"IPAM"`
	Labels     map[string]string                          `json:"Labels"`
	Containers map[string]caddyNetworkContainerInspection `json:"Containers"`
}

func (value gatewayNetworkIdentityInspection) caddy() caddyNetworkInspection {
	return caddyNetworkInspection{
		Name: value.Name, Driver: value.Driver, Scope: value.Scope, Internal: value.Internal,
		Options: value.Options, IPAM: value.IPAM, Labels: value.Labels, Containers: value.Containers,
	}
}

// observeGatewayMigrationTopology performs Docker reads only. Callers must
// hold the gateway writer lock around this observation if they will use the
// result to authorize a later mutation.
func (m *Manager) observeGatewayMigrationTopology(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayObservedTopology {
	if m == nil || ctx == nil || !validGatewayTopologyInputs(source, state, journal) {
		return gatewayTopologyUnknownOrDrift
	}
	observation, err := m.inspectGatewayV2Docker(ctx, source, state, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return gatewayTopologyUnknownOrDrift
	}
	defer clearGatewayV2DockerObservation(&observation)
	return classifyGatewayV2Topology(source, state, journal, observation)
}

func (m *Manager) inspectGatewayV2Docker(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
	var observation gatewayV2DockerObservation
	var err error
	observation.Image, observation.ImageFound, err = m.inspectImage(ctx)
	if err != nil {
		return observation, err
	}
	observation.V1Container, observation.V1Runtime, observation.V1ContainerFound, err = m.inspectNamedGatewayContainer(ctx, caddyContainerName)
	if err != nil {
		return observation, err
	}
	observation.V1Volume, observation.V1VolumeIdentity, observation.V1VolumeFound, err = m.inspectNamedVolumeWithIdentity(ctx, caddyVolumeName)
	if err != nil {
		return observation, err
	}
	observation.V1Network, observation.V1NetworkID, observation.V1NetworkFound, err = m.inspectNamedGatewayNetwork(ctx, caddyNetworkName)
	if err != nil {
		return observation, err
	}

	observation.ConfigVolume, observation.ConfigVolumeIdentity, observation.ConfigVolumeFound, err = m.inspectNamedVolumeWithIdentity(ctx, state.Identity.ConfigVolume)
	if err != nil {
		return observation, err
	}
	observation.DataVolume, observation.DataVolumeIdentity, observation.DataVolumeFound, err = m.inspectNamedVolumeWithIdentity(ctx, state.Identity.DataVolume)
	if err != nil {
		return observation, err
	}
	observation.IngressNetwork, observation.IngressNetworkID, observation.IngressFound, err = m.inspectNamedGatewayNetwork(ctx, state.Identity.IngressNetwork)
	if err != nil {
		return observation, err
	}
	observation.StageContainer, observation.StageRuntime, observation.StageContainerFound, err = m.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil {
		return observation, err
	}
	observation.FinalContainer, observation.FinalRuntime, observation.FinalContainerFound, err = m.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if err != nil {
		return observation, err
	}

	observation.OwnedContainers, err = m.inspectGatewayV2OwnedNames(ctx, "container", "ls", "--all")
	if err != nil {
		return observation, err
	}
	observation.OwnedVolumes, err = m.inspectGatewayV2OwnedNames(ctx, "volume", "ls")
	if err != nil {
		return observation, err
	}
	observation.OwnedNetworks, err = m.inspectGatewayV2OwnedNames(ctx, "network", "ls")
	if err != nil {
		return observation, err
	}

	if observation.V1ContainerFound {
		if observation.V1Container.Running && !observation.V1Container.Restarting {
			observation.V1Config, err = m.inspectLiveCaddyConfig(ctx, observation.V1Container.ID)
		}
		if err != nil {
			return observation, err
		}
		observation.V1RestartConfig, err = m.inspectStoppedCaddyRestartConfig(ctx, observation.V1Container.ID, gatewayV2ActiveConfigFile)
		if err != nil {
			return observation, err
		}
	}
	v1Owners, valid := gatewayRouteNetworkOwners(source.Active)
	if !valid {
		return observation, errors.New("invalid generated ingress v1 application networks")
	}
	observation.V1ApplicationNetworks = make(map[string]caddyNetworkInspection, len(v1Owners))
	observation.V1ApplicationNetworkIDs = make(map[string]string, len(v1Owners))
	for name := range v1Owners {
		full, id, found, inspectErr := m.inspectNamedGatewayNetwork(ctx, name)
		if inspectErr != nil || !found {
			return observation, errors.New("generated ingress v1 application network is unavailable")
		}
		observation.V1ApplicationNetworks[name] = full
		observation.V1ApplicationNetworkIDs[name] = id
	}
	if observation.StageContainerFound && observation.StageContainer.Running && !observation.StageContainer.Restarting {
		observation.StageConfig, err = m.inspectLiveCaddyConfig(ctx, observation.StageContainer.ID)
		if err != nil {
			return observation, err
		}
		observation.StageRestartConfig, err = m.inspectStoppedCaddyRestartConfig(ctx, observation.StageContainer.ID, state.Identity.StageConfigFilename)
		if err != nil {
			return observation, err
		}
		observation.Stage404Proven = m.proveGatewayV2Stage404(ctx, state, observation.StageContainer.ID)
	}
	if observation.FinalContainerFound && observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
		observation.FinalConfig, err = m.inspectLiveCaddyConfig(ctx, observation.FinalContainer.ID)
		if err != nil {
			return observation, err
		}
		observation.FinalRestartConfig, err = m.inspectStoppedCaddyRestartConfig(ctx, observation.FinalContainer.ID, state.Identity.ActiveConfigFilename)
		if err != nil {
			return observation, err
		}
		observation.Final404Proven = m.proveGatewayV2Final404(ctx, state, observation.FinalContainer.ID)
		observation.FinalRoutesProven = m.proveGatewayV2FinalRoutes(ctx, state, observation.FinalContainer.ID)
	}

	if observation.FinalContainerFound {
		expected, valid := gatewayV2ApplicationNetworkOwners(state)
		if !valid {
			return observation, errors.New("invalid generated ingress v2 application networks")
		}
		observation.ApplicationNetworks = make(map[string]caddyNetworkInspection, len(expected))
		observation.ApplicationNetworkIDs = make(map[string]string, len(expected))
		for name := range expected {
			full, id, found, inspectErr := m.inspectNamedGatewayNetwork(ctx, name)
			if inspectErr != nil || !found {
				return observation, errors.New("generated ingress v2 application network is unavailable")
			}
			observation.ApplicationNetworks[name] = full
			observation.ApplicationNetworkIDs[name] = id
		}
	}
	confirmedV1, confirmedV1Runtime, confirmedV1Found, confirmErr := m.inspectNamedGatewayContainer(ctx, caddyContainerName)
	if confirmErr != nil {
		return observation, confirmErr
	}
	observation.V1Stable = observation.V1ContainerFound == confirmedV1Found && (!confirmedV1Found ||
		(reflect.DeepEqual(observation.V1Container, confirmedV1) && reflect.DeepEqual(observation.V1Runtime, confirmedV1Runtime)))
	observation.V1ResourcesStable = m.confirmGatewayV1Resources(ctx, observation)
	observation.V2ResourcesStable = m.confirmGatewayV2Resources(ctx, state, observation)
	confirmedStage, confirmedStageRuntime, confirmedStageFound, inspectErr := m.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if inspectErr != nil {
		return observation, inspectErr
	}
	observation.StageStable = observation.StageContainerFound == confirmedStageFound && (!confirmedStageFound ||
		(reflect.DeepEqual(observation.StageContainer, confirmedStage) && reflect.DeepEqual(observation.StageRuntime, confirmedStageRuntime)))
	confirmedFinal, confirmedFinalRuntime, confirmedFinalFound, inspectErr := m.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if inspectErr != nil {
		return observation, inspectErr
	}
	observation.FinalStable = observation.FinalContainerFound == confirmedFinalFound && (!confirmedFinalFound ||
		(reflect.DeepEqual(observation.FinalContainer, confirmedFinal) && reflect.DeepEqual(observation.FinalRuntime, confirmedFinalRuntime)))
	observation.OwnedInventoriesStable = m.confirmGatewayV2OwnedInventories(ctx, observation)
	return observation, nil
}

func classifyGatewayV2Topology(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) gatewayObservedTopology {
	if !validGatewayTopologyInputs(source, state, journal) || !validGatewayPinnedImage(observation.Image, observation.ImageFound) ||
		!validGatewayV1Base(source, journal, observation) || !observation.StageStable || !observation.FinalStable || !observation.OwnedInventoriesStable {
		return gatewayTopologyUnknownOrDrift
	}

	infraAbsent := observation.V2ResourcesStable && !observation.ConfigVolumeFound && !observation.DataVolumeFound && !observation.IngressFound &&
		len(observation.OwnedVolumes) == 0 && len(observation.OwnedNetworks) == 0
	infraExactIdle := observation.V2ResourcesStable && validContainerID(observation.IngressNetworkID) && validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, "", "")

	v1Serving := observation.V1Stable && observation.V1ResourcesStable && observation.V1EndpointIdentityProven &&
		observation.V1Container.Running && !observation.V1Container.Restarting
	v1StoppedRestartable := observation.V1Stable && observation.V1ResourcesStable && observation.V1EndpointIdentityProven &&
		!observation.V1Container.Running && !observation.V1Container.Restarting
	stageAbsent := !observation.StageContainerFound
	finalAbsent := !observation.FinalContainerFound

	if v1Serving && stageAbsent && finalAbsent && len(observation.OwnedContainers) == 0 && (infraAbsent || infraExactIdle) {
		return gatewayTopologyExactV1Only
	}
	if v1Serving && observation.V2ResourcesStable && finalAbsent && validOwnedNameSet(observation.OwnedContainers, state.Identity.StageContainer) &&
		validContainerID(observation.IngressNetworkID) && validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2Container(state, journal, observation.StageContainer, observation.StageRuntime, observation.StageContainerFound, gatewayV2StageContainerRole, observation.Image.ID) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, observation.StageContainer.ID, state.Identity.StageContainer) &&
		validGatewayV2StageConfig(state, observation.StageConfig, observation.StageRestartConfig) && observation.Stage404Proven &&
		observation.StageHostPublicationProven && observation.StageStable {
		return gatewayTopologyExactV1WithStage
	}
	if v1StoppedRestartable && observation.V2ResourcesStable && stageAbsent && validOwnedNameSet(observation.OwnedContainers, state.Identity.FinalContainer) &&
		validContainerID(observation.IngressNetworkID) && validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2Container(state, journal, observation.FinalContainer, observation.FinalRuntime, observation.FinalContainerFound, gatewayV2FinalContainerRole, observation.Image.ID) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, observation.FinalContainer.ID, state.Identity.FinalContainer) &&
		validGatewayV2ApplicationNetworks(state, observation.FinalContainer, observation.ApplicationNetworks, observation.ApplicationNetworkIDs) &&
		validGatewayV2FinalConfig(state, observation.FinalConfig, observation.FinalRestartConfig) && observation.Final404Proven && observation.FinalRoutesProven &&
		observation.FinalHostPublicationProven && observation.FinalEndpointIdentityProven && observation.FinalStable {
		return gatewayTopologyExactFinalV2
	}
	return gatewayTopologyUnknownOrDrift
}

func validGatewayTopologyInputs(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	if !validRouteState(source) || source.Pending != nil || !validGatewayV2RouteState(state) || !validGatewayMigrationJournal(journal) {
		return false
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil || sourceDigest != state.SourceV1StateDigest || sourceDigest != journal.Source.StateDigest ||
		state.OperationID != journal.OperationID || !journalMatchesV2Plan(journal, state) {
		return false
	}
	if journal.Phase != gatewayPhaseCommitted && !journalMatchesInitialV2State(journal, state) {
		return false
	}
	return true
}

func validGatewayPinnedImage(value imageInspection, found bool) bool {
	return found && value.OS == "linux" && validContainerID(value.ID) && containsDigest(value.RepoDigests, gatewayV2CaddyImageDigest)
}

func validGatewayV1Base(source routeState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	if !observation.V1ContainerFound || !observation.V1VolumeFound || !observation.V1NetworkFound ||
		!validGatewayContainerRuntime(observation.V1Runtime, true) ||
		!reflect.DeepEqual(observation.V1Runtime.EffectivePortBindings, observation.V1Container.PortBindings) ||
		!validCaddyInspection(observation.V1Container, observation.Image.ID, journal.Source.LocalHostPort) ||
		observation.V1Volume.Name != caddyVolumeName || observation.V1Volume.Driver != "local" || observation.V1Volume.Scope != "local" ||
		len(observation.V1Volume.Options) != 0 || observation.V1Volume.Labels[gatewayV2ManagedLabelKey] != gatewayV2ManagedContainerLabel ||
		observation.V1Volume.Labels[gatewayV2IdentityLabelKey] != gatewayV1IdentityVersion {
		return false
	}
	listenIP, valid := caddyIngressAddress(observation.V1Network, observation.V1Container.ID)
	if !valid {
		return false
	}
	expected, err := buildCaddyConfig(source.Active, net.JoinHostPort(listenIP, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)))
	if err != nil || !sameCaddyConfig(expected, observation.V1RestartConfig) ||
		(observation.V1Container.Running && !sameCaddyConfig(expected, observation.V1Config)) ||
		(!observation.V1Container.Running && len(observation.V1Config) != 0) {
		return false
	}
	owners, valid := gatewayRouteNetworkOwners(source.Active)
	if !valid || len(observation.V1ApplicationNetworks) != len(owners) || len(observation.V1ApplicationNetworkIDs) != len(owners) ||
		len(observation.V1Container.Networks) != len(owners)+1 {
		return false
	}
	for name, appID := range owners {
		inspection, exists := observation.V1ApplicationNetworks[name]
		if !exists || !validContainerID(observation.V1ApplicationNetworkIDs[name]) || !validApplicationNetwork(inspection.identity(), appID) ||
			!validGatewayApplicationNetworkMembership(inspection, observation.V1Container, name) {
			return false
		}
	}
	identityDigest, err := gatewayV1ObservedIdentityDigest(observation)
	return err == nil && identityDigest == journal.Source.IdentityDigest
}

func gatewayV1ObservedIdentityDigest(observation gatewayV2DockerObservation) (string, error) {
	if !validContainerID(observation.Image.ID) || !validContainerID(observation.V1Container.ID) ||
		!validContainerID(observation.V1NetworkID) || observation.V1VolumeIdentity.Mountpoint == "" || observation.V1VolumeIdentity.CreatedAt == "" {
		return "", errors.New("invalid generated ingress v1 observed identity")
	}
	return canonicalDigest(struct {
		Version          int    `json:"version"`
		ImageID          string `json:"imageId"`
		ContainerID      string `json:"containerId"`
		ConfigVolumePath string `json:"configVolumePath"`
		VolumeCreatedAt  string `json:"volumeCreatedAt"`
		IngressNetworkID string `json:"ingressNetworkId"`
		RestartCount     int    `json:"restartCount"`
	}{
		Version: 1, ImageID: normalizeID(observation.Image.ID), ContainerID: normalizeID(observation.V1Container.ID),
		ConfigVolumePath: observation.V1VolumeIdentity.Mountpoint, VolumeCreatedAt: observation.V1VolumeIdentity.CreatedAt,
		IngressNetworkID: normalizeID(observation.V1NetworkID), RestartCount: observation.V1Runtime.RestartCount,
	})
}

func validGatewayContainerRuntime(value gatewayContainerRuntime, allowRestarts bool) bool {
	return !value.Paused && !value.Dead && value.RestartCount >= 0 && (allowRestarts || value.RestartCount == 0)
}

func validGatewayV2Volumes(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	return validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) &&
		validGatewayVolumeResourceIdentity(observation.ConfigVolumeIdentity) && validGatewayVolumeResourceIdentity(observation.DataVolumeIdentity) &&
		validGatewayV2Volume(state, journal, observation.ConfigVolume, observation.ConfigVolumeFound, gatewayV2ConfigVolumeRole) &&
		validGatewayV2Volume(state, journal, observation.DataVolume, observation.DataVolumeFound, gatewayV2DataVolumeRole)
}

func validGatewayV2Volume(state gatewayV2RouteState, journal gatewayMigrationJournal, value volumeInspection, found bool, role string) bool {
	expectedName := state.Identity.ConfigVolume
	if role == gatewayV2DataVolumeRole {
		expectedName = state.Identity.DataVolume
	}
	return found && value.Name == expectedName && value.Driver == "local" && value.Scope == "local" && len(value.Options) == 0 &&
		reflect.DeepEqual(value.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, false))
}

func validGatewayVolumeResourceIdentity(value gatewayV1VolumeIdentity) bool {
	return value.Mountpoint != "" && value.CreatedAt != ""
}

func validGatewayV2IngressNetwork(state gatewayV2RouteState, journal gatewayMigrationJournal, value caddyNetworkInspection, found bool, containerID, containerName string) bool {
	if !found || value.Name != state.Identity.IngressNetwork || value.Driver != "bridge" || value.Scope != "local" || value.Internal || len(value.Options) != 0 ||
		len(value.IPAM.Config) != 1 || value.IPAM.Config[0] != (networkIPAM{Subnet: state.Network.Subnet, Gateway: state.Network.GatewayIPv4}) ||
		!reflect.DeepEqual(value.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false)) {
		return false
	}
	if containerID == "" {
		return len(value.Containers) == 0
	}
	if !validContainerID(containerID) || len(value.Containers) != 1 {
		return false
	}
	for id, container := range value.Containers {
		prefix, err := netip.ParsePrefix(container.IPv4Address)
		return err == nil && normalizeID(id) == normalizeID(containerID) && container.Name == containerName &&
			prefix.Addr().String() == state.Network.ContainerIPv4 && prefix.Bits() == mustGatewayV2PrefixBits(state.Network.Subnet)
	}
	return false
}

func validGatewayV2Container(state gatewayV2RouteState, journal gatewayMigrationJournal, value caddyInspection, runtime gatewayContainerRuntime, found bool, role, imageID string) bool {
	name, configFilename, restart := state.Identity.StageContainer, state.Identity.StageConfigFilename, gatewayV2StageRestartPolicy
	if role == gatewayV2FinalContainerRole {
		name, configFilename, restart = state.Identity.FinalContainer, state.Identity.ActiveConfigFilename, gatewayV2FinalRestartPolicy
	}
	if !found || !value.Running || value.Restarting || !validGatewayContainerRuntime(runtime, false) || !validContainerID(value.ID) || normalizeID(value.Image) != normalizeID(imageID) ||
		strings.TrimPrefix(value.Name, "/") != name || value.Hostname != name || value.User != "1000:1000" || value.NetworkMode != state.Identity.IngressNetwork ||
		!exactGatewayV2Environment(value.Env) || !value.ReadOnly || value.Privileged || !onlyCaddyCapability(value.CapAdd) || !exactFoldSet(value.CapDrop, "ALL") ||
		!onlyNoNewPrivileges(value.SecurityOpt) || len(value.Binds) != 0 || len(value.Tmpfs) != 0 || value.Memory != 268435456 || value.MemorySwap != 268435456 ||
		value.NanoCPUs != 1_000_000_000 || value.PIDsLimit != 128 || value.LogType != "local" || len(value.LogConfig) != 2 ||
		value.LogConfig["max-size"] != "10m" || value.LogConfig["max-file"] != "3" || value.Restart != restart ||
		len(value.Entrypoint) != 1 || value.Entrypoint[0] != caddyExecutable || len(value.Cmd) != 3 || value.Cmd[0] != "run" || value.Cmd[1] != "--config" ||
		value.Cmd[2] != "/config/"+configFilename || len(value.Ulimits) != 1 || value.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!reflect.DeepEqual(value.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true)) ||
		!validGatewayV2Mounts(value.Mounts, state.Identity) || !validGatewayV2PortBindings(value.PortBindings, state, journal, role) ||
		!reflect.DeepEqual(runtime.EffectivePortBindings, value.PortBindings) {
		return false
	}
	expectedNetworks, valid := gatewayV2ExpectedContainerNetworks(state, role)
	if !valid || len(value.Networks) != len(expectedNetworks) {
		return false
	}
	for name := range expectedNetworks {
		attachment := value.Networks[name]
		priority := 0
		if name == state.Identity.IngressNetwork {
			priority = caddyGatewayPriority
		}
		if attachment == nil || attachment.GwPriority != priority || attachment.IPv6Gateway != "" {
			return false
		}
		if name == state.Identity.IngressNetwork && attachment.IPAddress != state.Network.ContainerIPv4 {
			return false
		}
	}
	return true
}

func exactGatewayV2Environment(values []string) bool {
	return exactEnvironmentAssignment(values, "XDG_CONFIG_HOME", "/config") && exactEnvironmentAssignment(values, "XDG_DATA_HOME", "/data")
}

func exactEnvironmentAssignment(values []string, key, expected string) bool {
	prefix, found := key+"=", false
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			if found || value != prefix+expected {
				return false
			}
			found = true
		}
	}
	return found
}

func validGatewayV2Mounts(values []mountInspection, identity gatewayV2Identity) bool {
	if len(values) != 2 {
		return false
	}
	wanted := map[string]string{"/config": identity.ConfigVolume, "/data": identity.DataVolume}
	for _, value := range values {
		name, exists := wanted[value.Destination]
		if !exists || value.Type != "volume" || value.Name != name || !value.RW {
			return false
		}
		delete(wanted, value.Destination)
	}
	return len(wanted) == 0
}

func validGatewayV2PortBindings(values map[string][]map[string]string, state gatewayV2RouteState, journal gatewayMigrationJournal, role string) bool {
	expected := make(map[string]map[string]string, int(state.Profile.PortEnd-state.Profile.PortStart)+2)
	for port := state.Profile.PortStart; ; port++ {
		expected[strconv.FormatUint(uint64(port), 10)+"/tcp"] = map[string]string{"HostIp": state.Profile.SelectedIPv4, "HostPort": strconv.FormatUint(uint64(port), 10)}
		if port == state.Profile.PortEnd {
			break
		}
	}
	if role == gatewayV2FinalContainerRole {
		expected["8080/tcp"] = map[string]string{"HostIp": "127.0.0.1", "HostPort": strconv.FormatUint(uint64(journal.Source.LocalHostPort), 10)}
	}
	if len(values) != len(expected) {
		return false
	}
	for containerPort, binding := range expected {
		actual := values[containerPort]
		if len(actual) != 1 || !reflect.DeepEqual(actual[0], binding) {
			return false
		}
	}
	return true
}

func validGatewayV2ApplicationNetworks(state gatewayV2RouteState, container caddyInspection, inspections map[string]caddyNetworkInspection, ids map[string]string) bool {
	expected, valid := gatewayV2ApplicationNetworkOwners(state)
	if !valid || len(inspections) != len(expected) || len(ids) != len(expected) {
		return false
	}
	for name, appID := range expected {
		inspection, exists := inspections[name]
		if !exists || !validContainerID(ids[name]) || name == state.Identity.IngressNetwork || !validApplicationNetwork(inspection.identity(), appID) ||
			!validGatewayApplicationNetworkMembership(inspection, container, name) {
			return false
		}
	}
	return true
}

func validGatewayApplicationNetworkMembership(network caddyNetworkInspection, container caddyInspection, networkName string) bool {
	attachment := container.Networks[networkName]
	if network.Name != networkName || attachment == nil || !validContainerID(container.ID) || attachment.IPAddress == "" {
		return false
	}
	found := false
	expectedName := strings.TrimPrefix(container.Name, "/")
	for id, member := range network.Containers {
		if member.Name == expectedName && normalizeID(id) != normalizeID(container.ID) {
			return false
		}
		if normalizeID(id) != normalizeID(container.ID) {
			continue
		}
		prefix, err := netip.ParsePrefix(member.IPv4Address)
		if found || err != nil || !prefix.Addr().Is4() || prefix.Addr().String() != attachment.IPAddress || member.Name != expectedName {
			return false
		}
		found = true
	}
	return found
}

func gatewayV2ApplicationNetworkOwners(state gatewayV2RouteState) (map[string]string, bool) {
	routes := make(map[string]routeRecord, len(state.Apps))
	for appID, app := range state.Apps {
		routes[appID] = app.Route
	}
	return gatewayRouteNetworkOwners(routes)
}

func gatewayRouteNetworkOwners(routes map[string]routeRecord) (map[string]string, bool) {
	result := make(map[string]string)
	for appID, route := range routes {
		if validateRoute(route) != nil {
			return nil, false
		}
		for _, endpoint := range route.Endpoints {
			if owner, exists := result[endpoint.NetworkName]; exists && owner != appID {
				return nil, false
			}
			result[endpoint.NetworkName] = appID
		}
	}
	return result, true
}

func gatewayV2ExpectedContainerNetworks(state gatewayV2RouteState, role string) (map[string]struct{}, bool) {
	result := map[string]struct{}{state.Identity.IngressNetwork: {}}
	if role == gatewayV2StageContainerRole {
		return result, true
	}
	owners, valid := gatewayV2ApplicationNetworkOwners(state)
	if !valid {
		return nil, false
	}
	for name := range owners {
		result[name] = struct{}{}
	}
	return result, true
}

func validGatewayV2StageConfig(state gatewayV2RouteState, live, restart []byte) bool {
	expected, err := buildGatewayV2StageConfig(state)
	return err == nil && sameCaddyConfig(expected, live) && sameCaddyConfig(expected, restart)
}

func validGatewayV2FinalConfig(state gatewayV2RouteState, live, restart []byte) bool {
	routes, assignments := gatewayV2ConfigInputs(state)
	expected, err := buildCaddyConfigV2(routes, net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd}, assignments)
	return err == nil && sameCaddyConfig(expected, live) && sameCaddyConfig(expected, restart)
}

func buildGatewayV2StageConfig(state gatewayV2RouteState) ([]byte, error) {
	if !validGatewayV2RouteState(state) {
		return nil, errors.New("invalid generated ingress v2 stage state")
	}
	servers := make(map[string]caddyServer, int(state.Profile.PortEnd-state.Profile.PortStart)+1)
	for port := state.Profile.PortStart; ; port++ {
		servers["lan_"+strconv.FormatUint(uint64(port), 10)] = caddyServer{
			Listen:         []string{net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(port), 10))},
			AutomaticHTTPS: caddyAutomaticHTTPS{Disable: true}, Routes: []caddyRoute{notFoundRoute()},
		}
		if port == state.Profile.PortEnd {
			break
		}
	}
	return json.Marshal(caddyConfig{Admin: caddyAdmin{Listen: "localhost:2019"}, Apps: caddyApps{HTTP: caddyHTTP{Servers: servers}}})
}

func gatewayV2ConfigInputs(state gatewayV2RouteState) (map[string]routeRecord, map[uint16]string) {
	routes := make(map[string]routeRecord, len(state.Apps))
	assignments := make(map[uint16]string)
	for appID, app := range state.Apps {
		routes[appID] = app.Route
		if app.LAN != nil {
			assignments[app.LAN.Port] = appID
		}
	}
	return routes, assignments
}

func sameCaddyConfig(expected, actual []byte) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	var expectedJSON, actualJSON any
	return json.Unmarshal(expected, &expectedJSON) == nil && json.Unmarshal(actual, &actualJSON) == nil && reflect.DeepEqual(expectedJSON, actualJSON)
}

func gatewayV2ResourceLabels(state gatewayV2RouteState, journal gatewayMigrationJournal, managed, role string, listener bool) map[string]string {
	labels := map[string]string{
		gatewayV2ManagedLabelKey:        managed,
		gatewayV2IdentityLabelKey:       gatewayV2IdentityVersion,
		gatewayV2OperationLabelKey:      state.OperationID,
		gatewayV2IdentityDigestLabelKey: state.Identity.Digest,
		gatewayV2PlanDigestLabelKey:     journal.Target.PlanDigest,
		gatewayV2ResourceRoleLabelKey:   role,
	}
	if listener {
		labels[gatewayV2ListenerIsolationKey] = gatewayV2ListenerIsolationVersion
	}
	return labels
}

func validOwnedNameSet(actual []string, expected ...string) bool {
	wanted := append([]string(nil), expected...)
	sort.Strings(wanted)
	return reflect.DeepEqual(actual, wanted)
}

func mustGatewayV2PrefixBits(subnet string) int {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil {
		return -1
	}
	return prefix.Bits()
}

func (m *Manager) inspectNamedVolumeWithIdentity(ctx context.Context, name string) (volumeInspection, gatewayV1VolumeIdentity, bool, error) {
	const format = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}},"labels":{{json .Labels}},"mountpoint":{{json .Mountpoint}},"createdAt":{{json .CreatedAt}}}`
	var value gatewayVolumeIdentityInspection
	found, err := m.inspectJSON(ctx, &value, "volume", "inspect", "--format", format, name)
	if err != nil || !found {
		return volumeInspection{}, gatewayV1VolumeIdentity{}, found, err
	}
	return volumeInspection{Name: value.Name, Driver: value.Driver, Scope: value.Scope, Options: value.Options, Labels: value.Labels},
		gatewayV1VolumeIdentity{Mountpoint: value.Mountpoint, CreatedAt: value.CreatedAt}, true, nil
}

func (m *Manager) inspectNamedGatewayContainer(ctx context.Context, name string) (caddyInspection, gatewayContainerRuntime, bool, error) {
	var value gatewayContainerInspection
	found, err := m.inspectJSON(ctx, &value, "container", "inspect", "--format", gatewayContainerInspectFormat, name)
	return value.caddyInspection, value.gatewayContainerRuntime, found, err
}

func (m *Manager) inspectNamedGatewayNetwork(ctx context.Context, name string) (caddyNetworkInspection, string, bool, error) {
	var value caddyNetworkInspection
	result, err := m.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: m.options.DockerExecutable, Args: []string{"network", "inspect", "--format", "json", name},
		Directory: m.options.WorkingDirectory, Env: append([]string(nil), m.dockerEnv...), Timeout: m.options.CommandTimeout, OutputLimit: m.options.OutputLimit,
	})
	if result.StdoutTruncated || result.StderrTruncated {
		clearResult(&result)
		return value, "", false, errors.New("generated ingress network inspection was truncated")
	}
	if err != nil {
		notFound := dockerNotFound(result)
		clearResult(&result)
		if notFound {
			return value, "", false, nil
		}
		return value, "", false, errors.New("generated ingress network inspection failed")
	}
	var values []gatewayNetworkIdentityInspection
	decodeErr := json.Unmarshal(result.Stdout, &values)
	clearResult(&result)
	if decodeErr != nil || len(values) != 1 || !validContainerID(values[0].ID) {
		for index := range values {
			candidate := values[index].caddy()
			clearCaddyNetworkInspection(&candidate)
		}
		clear(values)
		return value, "", false, errors.New("generated ingress network inspection was invalid")
	}
	value = values[0].caddy()
	id := values[0].ID
	values[0] = gatewayNetworkIdentityInspection{}
	clear(values)
	return value, id, true, nil
}

func (m *Manager) confirmGatewayV1Resources(ctx context.Context, observation gatewayV2DockerObservation) bool {
	volume, volumeIdentity, volumeFound, err := m.inspectNamedVolumeWithIdentity(ctx, caddyVolumeName)
	if err != nil || volumeFound != observation.V1VolumeFound || !reflect.DeepEqual(volume, observation.V1Volume) ||
		volumeIdentity != observation.V1VolumeIdentity {
		return false
	}
	network, networkID, networkFound, err := m.inspectNamedGatewayNetwork(ctx, caddyNetworkName)
	return err == nil && networkFound == observation.V1NetworkFound && networkID == observation.V1NetworkID &&
		reflect.DeepEqual(network, observation.V1Network) &&
		m.confirmGatewayApplicationNetworks(ctx, observation.V1ApplicationNetworks, observation.V1ApplicationNetworkIDs)
}

func (m *Manager) confirmGatewayV2Resources(ctx context.Context, state gatewayV2RouteState, observation gatewayV2DockerObservation) bool {
	config, configIdentity, configFound, err := m.inspectNamedVolumeWithIdentity(ctx, state.Identity.ConfigVolume)
	if err != nil || configFound != observation.ConfigVolumeFound || !reflect.DeepEqual(config, observation.ConfigVolume) || configIdentity != observation.ConfigVolumeIdentity {
		return false
	}
	data, dataIdentity, dataFound, err := m.inspectNamedVolumeWithIdentity(ctx, state.Identity.DataVolume)
	if err != nil || dataFound != observation.DataVolumeFound || !reflect.DeepEqual(data, observation.DataVolume) || dataIdentity != observation.DataVolumeIdentity {
		return false
	}
	network, networkID, networkFound, err := m.inspectNamedGatewayNetwork(ctx, state.Identity.IngressNetwork)
	if err != nil || networkFound != observation.IngressFound || networkID != observation.IngressNetworkID || !reflect.DeepEqual(network, observation.IngressNetwork) {
		return false
	}
	if observation.FinalContainerFound {
		return m.confirmGatewayApplicationNetworks(ctx, observation.ApplicationNetworks, observation.ApplicationNetworkIDs)
	}
	return len(observation.ApplicationNetworks) == 0 && len(observation.ApplicationNetworkIDs) == 0
}

func (m *Manager) confirmGatewayV2OwnedInventories(ctx context.Context, observation gatewayV2DockerObservation) bool {
	containers, err := m.inspectGatewayV2OwnedNames(ctx, "container", "ls", "--all")
	if err != nil || !reflect.DeepEqual(containers, observation.OwnedContainers) {
		return false
	}
	volumes, err := m.inspectGatewayV2OwnedNames(ctx, "volume", "ls")
	if err != nil || !reflect.DeepEqual(volumes, observation.OwnedVolumes) {
		return false
	}
	networks, err := m.inspectGatewayV2OwnedNames(ctx, "network", "ls")
	return err == nil && reflect.DeepEqual(networks, observation.OwnedNetworks)
}

func (m *Manager) confirmGatewayApplicationNetworks(ctx context.Context, inspections map[string]caddyNetworkInspection, ids map[string]string) bool {
	if len(inspections) != len(ids) {
		return false
	}
	for name, expected := range inspections {
		observed, id, found, err := m.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || id != ids[name] || !reflect.DeepEqual(observed, expected) {
			return false
		}
	}
	return true
}

func (m *Manager) inspectGatewayV2OwnedNames(ctx context.Context, args ...string) ([]string, error) {
	format := "{{.Name}}"
	if len(args) > 0 && args[0] == "container" {
		format = "{{.Names}}"
	}
	args = append(args,
		"--filter", "label="+gatewayV2ManagedLabelKey,
		"--filter", "label="+gatewayV2IdentityLabelKey+"="+gatewayV2IdentityVersion,
		"--format", format,
	)
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
			return nil, errors.New("generated ingress v2 ownership inventory is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("generated ingress v2 ownership inventory is duplicated")
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	return values, nil
}

func (m *Manager) inspectLiveCaddyConfig(ctx context.Context, container string) ([]byte, error) {
	result, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", container,
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/")
	if err != nil {
		return nil, err
	}
	defer clearResult(&result)
	if len(result.Stdout) == 0 || len(result.Stdout) > gatewayV2MaxConfigBytes || !json.Valid(result.Stdout) {
		return nil, errors.New("generated ingress live config is invalid")
	}
	return append([]byte(nil), result.Stdout...), nil
}

func (m *Manager) inspectStoppedCaddyRestartConfig(ctx context.Context, container, filename string) ([]byte, error) {
	result, err := m.run(ctx, m.options.CommandTimeout, "container", "cp", container+":/config/"+filename, "-")
	if err != nil {
		return nil, err
	}
	defer clearResult(&result)
	reader := tar.NewReader(bytes.NewReader(result.Stdout))
	header, err := reader.Next()
	if err != nil || header == nil || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > gatewayV2MaxConfigBytes ||
		strings.TrimPrefix(header.Name, "./") != filename {
		return nil, errors.New("generated ingress restart config archive is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(reader, gatewayV2MaxConfigBytes+1))
	if err != nil || int64(len(body)) != header.Size || !json.Valid(body) {
		clear(body)
		return nil, errors.New("generated ingress restart config is invalid")
	}
	if _, err := reader.Next(); err != io.EOF {
		clear(body)
		return nil, errors.New("generated ingress restart config archive has extra entries")
	}
	return body, nil
}

func (m *Manager) proveGatewayV2Stage404(ctx context.Context, state gatewayV2RouteState, caddyID string) bool {
	for port := state.Profile.PortStart; ; port++ {
		if !m.probeGatewayV2Status(ctx, caddyID, state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, "404") ||
			!m.probeGatewayV2Status(ctx, caddyID, state.Network.ContainerIPv4, port, "wrong.invalid", "404") {
			return false
		}
		if port == state.Profile.PortEnd {
			return true
		}
	}
}

func (m *Manager) proveGatewayV2Final404(ctx context.Context, state gatewayV2RouteState, caddyID string) bool {
	_, assignments := gatewayV2ConfigInputs(state)
	for port := state.Profile.PortStart; ; port++ {
		if !m.probeGatewayV2Status(ctx, caddyID, state.Network.ContainerIPv4, port, "wrong.invalid", "404") {
			return false
		}
		if _, assigned := assignments[port]; !assigned &&
			!m.probeGatewayV2Status(ctx, caddyID, state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, "404") {
			return false
		}
		if port == state.Profile.PortEnd {
			return true
		}
	}
}

func (m *Manager) proveGatewayV2FinalRoutes(ctx context.Context, state gatewayV2RouteState, caddyID string) bool {
	if !validContainerID(caddyID) {
		return false
	}
	for appID, app := range state.Apps {
		// Re-attest the immutable application container identities, ownership
		// labels, health, and expected network aliases before using their
		// endpoints in the route proof.
		if m.verifyEndpoints(ctx, appID, app.Route) != nil {
			return false
		}
		for _, endpoint := range app.Route.Endpoints {
			if m.probeGatewayEndpoint(ctx, caddyID, endpoint) != nil {
				return false
			}
		}
		if !m.probeGatewayV2AnyStatus(ctx, caddyID, state.Network.ContainerIPv4, gatewayV2ContainerPort, appID+".rig.localhost") {
			return false
		}
		if app.LAN != nil && !m.probeGatewayV2AnyStatus(ctx, caddyID, state.Network.ContainerIPv4, app.LAN.Port, state.Profile.SelectedIPv4) {
			return false
		}
	}
	return true
}

func (m *Manager) probeGatewayV2AnyStatus(ctx context.Context, container, address string, port uint16, host string) bool {
	result, err := m.run(ctx, gatewayProbeProcessTimeout, "container", "exec", container,
		"curl", "--disable", "--silent", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
		"--header", "Host: "+host, "http://"+net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10))+"/")
	defer clearResult(&result)
	return err == nil && successfulGatewayStatus(result.Stdout)
}

func (m *Manager) probeGatewayV2Status(ctx context.Context, container, address string, port uint16, host, expected string) bool {
	result, err := m.run(ctx, gatewayProbeProcessTimeout, "container", "exec", container,
		"curl", "--disable", "--silent", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
		"--header", "Host: "+host, "http://"+net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10))+"/")
	defer clearResult(&result)
	return err == nil && string(result.Stdout) == expected
}

func clearGatewayV2DockerObservation(value *gatewayV2DockerObservation) {
	if value == nil {
		return
	}
	clear(value.V1Config)
	clear(value.V1RestartConfig)
	clear(value.StageConfig)
	clear(value.StageRestartConfig)
	clear(value.FinalConfig)
	clear(value.FinalRestartConfig)
	for name := range value.ApplicationNetworks {
		delete(value.ApplicationNetworks, name)
	}
	for name := range value.V1ApplicationNetworks {
		delete(value.V1ApplicationNetworks, name)
	}
	clear(value.OwnedContainers)
	clear(value.OwnedVolumes)
	clear(value.OwnedNetworks)
	clearCaddyNetworkInspection(&value.V1Network)
	clearCaddyNetworkInspection(&value.IngressNetwork)
	*value = gatewayV2DockerObservation{}
}
