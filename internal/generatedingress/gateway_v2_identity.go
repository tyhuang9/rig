package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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
	gatewayV2ChallengePathPrefix      = "/.well-known/rig-gateway/"
	gatewayV2ChallengeBodyPrefix      = "rig-gateway-v2:"
	gatewayV2ChallengeContext         = "rig-gateway-v2-host-proof"
	// A protected v1 state is capped at 48 KiB. Generated Caddy config omits
	// several endpoint fields and the v2 LAN matrix adds less than 12 KiB.
	// Keeping the body at 60 KiB leaves room for docker-cp tar framing inside
	// the Manager's default 64 KiB command-output boundary.
	gatewayV2MaxConfigBytes = 60 << 10
)

var gatewayContainerInspectFormat = strings.TrimSuffix(caddyInspectFormat, "}") +
	`,"paused":{{json .State.Paused}},"dead":{{json .State.Dead}},"restartCount":{{json .RestartCount}},"effectivePortBindings":{{json .NetworkSettings.Ports}},"configuredNetworks":{{json .NetworkSettings.Networks}}}`

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
	// EndpointIdentityProven requires stable immutable endpoint IDs, ownership
	// labels, health, and network-wide unique aliases. Committed v2 operation
	// no longer depends on historical v1 endpoint liveness; migration phases do
	// require this proof while v1 remains the rollback target.
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
	// route probes. It requires a state-bound challenge through both the exact
	// selected host address and the immutable Caddy container, plus loopback
	// non-exposure; Docker's port maps alone are insufficient.
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
	Paused                bool                                  `json:"paused"`
	Dead                  bool                                  `json:"dead"`
	RestartCount          int                                   `json:"restartCount"`
	EffectivePortBindings map[string][]map[string]string        `json:"effectivePortBindings"`
	ConfiguredNetworks    map[string]gatewayV2ConfiguredNetwork `json:"configuredNetworks"`
}

type gatewayV2ConfiguredNetwork struct {
	IPAMConfig  *gatewayV2ConfiguredIPAM `json:"IPAMConfig"`
	NetworkID   string                   `json:"NetworkID"`
	EndpointID  string                   `json:"EndpointID"`
	IPAddress   string                   `json:"IPAddress"`
	GwPriority  int                      `json:"GwPriority"`
	IPv6Gateway string                   `json:"IPv6Gateway"`
}

type gatewayV2ConfiguredIPAM struct {
	IPv4Address string `json:"IPv4Address"`
	IPv6Address string `json:"IPv6Address"`
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

type gatewayEndpointIdentityRecord struct {
	ApplicationID string   `json:"applicationId"`
	ContainerID   string   `json:"containerId"`
	Component     string   `json:"component"`
	Role          string   `json:"role"`
	Slot          string   `json:"slot"`
	NetworkName   string   `json:"networkName"`
	NetworkAlias  string   `json:"networkAlias"`
	IPAddress     string   `json:"ipAddress"`
	Aliases       []string `json:"aliases"`
}

type gatewayNetworkAliasRecord struct {
	NetworkName string   `json:"networkName"`
	ContainerID string   `json:"containerId"`
	IPAddress   string   `json:"ipAddress"`
	Aliases     []string `json:"aliases"`
}

type gatewayV2HostProbeResult struct {
	Status    int
	Body      string
	Connected bool
	Responded bool
}

type gatewayV2HostStatusProbe func(context.Context, string, uint16, string, string) gatewayV2HostProbeResult
type gatewayV2ContainerChallengeProbe func(context.Context, string, string, uint16, string, string) bool

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

// observeGatewayV2MixedRestart recognizes only the committed-v2 crash window
// where Caddy applied the proposed live config but the durable restart config
// still contains the last committed routes. The caller supplies Pending=nil
// derived states and must hold the gateway writer lock before using this proof
// to authorize a rollback mutation.
func (m *Manager) observeGatewayV2MixedRestart(ctx context.Context, source routeState, committed, proposed gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	if m == nil || ctx == nil || !validGatewayV2MixedRestartInputs(source, committed, proposed, journal) {
		return false
	}
	committedEndpointsBefore, err := m.inspectGatewayRouteEndpointProof(ctx, gatewayV2RouteRecords(committed))
	if err != nil {
		return false
	}
	candidate, _, found, err := m.inspectNamedGatewayContainer(ctx, proposed.Identity.FinalContainer)
	if err != nil || !found || !validContainerID(candidate.ID) ||
		!m.proveGatewayRouteEndpointTransports(ctx, candidate.ID, gatewayV2RouteRecords(committed)) {
		return false
	}
	committedEndpointsAfter, err := m.inspectGatewayRouteEndpointProof(ctx, gatewayV2RouteRecords(committed))
	if err != nil || committedEndpointsBefore != committedEndpointsAfter {
		return false
	}
	observation, err := m.inspectGatewayV2Docker(ctx, source, proposed, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		return false
	}
	defer clearGatewayV2DockerObservation(&observation)
	return normalizeID(candidate.ID) == normalizeID(observation.FinalContainer.ID) &&
		classifyGatewayV2MixedRestart(source, committed, proposed, journal, observation)
}

func (m *Manager) inspectGatewayV2Docker(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) (gatewayV2DockerObservation, error) {
	return m.inspectGatewayV2DockerWithStageConfig(ctx, source, state, journal, true)
}

// The compensation-only path may inspect a stopped, ID-bound stage whose
// restart config was never copied before a crash. It must never use this
// observation to start a listener or attest serving topology.
func (m *Manager) inspectGatewayV2DockerWithStageConfig(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, requireStoppedStageConfig bool) (gatewayV2DockerObservation, error) {
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
	requireV1RollbackTopology := journal.Phase != gatewayPhaseCommitted
	v1Owners, valid := gatewayRouteNetworkOwners(source.Active)
	if !valid {
		return observation, errors.New("invalid generated ingress v1 application networks")
	}
	observation.V1ApplicationNetworks = make(map[string]caddyNetworkInspection)
	observation.V1ApplicationNetworkIDs = make(map[string]string)
	if requireV1RollbackTopology {
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
	}
	requireV1EndpointIdentity := requireV1RollbackTopology
	var v1EndpointIdentityBefore string
	if requireV1EndpointIdentity {
		v1EndpointIdentityBefore, err = m.inspectGatewayEndpointIdentitySnapshot(ctx, source.Active, observation.V1ApplicationNetworks)
		if err != nil {
			return observation, err
		}
	}
	if observation.StageContainerFound {
		if observation.StageContainer.Running && !observation.StageContainer.Restarting {
			observation.StageConfig, err = m.inspectLiveCaddyConfig(ctx, observation.StageContainer.ID)
			if err != nil {
				return observation, err
			}
		}
		if requireStoppedStageConfig || observation.StageContainer.Running || observation.StageContainer.Restarting {
			observation.StageRestartConfig, err = m.inspectStoppedCaddyRestartConfig(ctx, observation.StageContainer.ID, state.Identity.StageConfigFilename)
			if err != nil {
				return observation, err
			}
		}
		if observation.StageContainer.Running && !observation.StageContainer.Restarting {
			observation.Stage404Proven = m.proveGatewayV2Stage404(ctx, state, observation.StageContainer.ID)
			observation.StageHostPublicationProven = proveGatewayV2StageHostPublication(ctx, state, observation.StageContainer.ID, probeGatewayV2HostStatus, m.probeGatewayV2ContainerChallenge)
		}
	}
	if observation.FinalContainerFound {
		if observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
			observation.FinalConfig, err = m.inspectLiveCaddyConfig(ctx, observation.FinalContainer.ID)
			if err != nil {
				return observation, err
			}
		}
		observation.FinalRestartConfig, err = m.inspectStoppedCaddyRestartConfig(ctx, observation.FinalContainer.ID, state.Identity.ActiveConfigFilename)
		if err != nil {
			return observation, err
		}
		if observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
			observation.Final404Proven = m.proveGatewayV2Final404(ctx, state, observation.FinalContainer.ID)
		}
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
	var finalEndpointIdentityBefore string
	if observation.FinalContainerFound {
		finalEndpointIdentityBefore, err = m.inspectGatewayEndpointIdentitySnapshot(ctx, gatewayV2RouteRecords(state), observation.ApplicationNetworks)
		if err != nil {
			return observation, err
		}
	}
	if observation.FinalContainerFound && observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
		observation.FinalRoutesProven = m.proveGatewayV2FinalRoutes(ctx, state, observation.FinalContainer.ID)
		observation.FinalHostPublicationProven = proveGatewayV2FinalHostPublication(ctx, state, observation.FinalContainer.ID, probeGatewayV2HostStatus, m.probeGatewayV2ContainerChallenge)
	}

	v1ConfigStable := true
	if observation.V1ContainerFound {
		var confirmedLive []byte
		if observation.V1Container.Running && !observation.V1Container.Restarting {
			confirmedLive, err = m.inspectLiveCaddyConfig(ctx, observation.V1Container.ID)
			if err != nil {
				return observation, err
			}
		}
		confirmedRestart, inspectErr := m.inspectStoppedCaddyRestartConfig(ctx, observation.V1Container.ID, gatewayV2ActiveConfigFile)
		if inspectErr != nil {
			return observation, inspectErr
		}
		v1ConfigStable = sameOptionalCaddyConfig(observation.V1Config, confirmedLive) && sameOptionalCaddyConfig(observation.V1RestartConfig, confirmedRestart)
		clear(confirmedLive)
		clear(confirmedRestart)
	}
	confirmedV1, confirmedV1Runtime, confirmedV1Found, confirmErr := m.inspectNamedGatewayContainer(ctx, caddyContainerName)
	if confirmErr != nil {
		return observation, confirmErr
	}
	observation.V1Stable = v1ConfigStable && observation.V1ContainerFound == confirmedV1Found && (!confirmedV1Found ||
		(reflect.DeepEqual(observation.V1Container, confirmedV1) && reflect.DeepEqual(observation.V1Runtime, confirmedV1Runtime)))
	observation.V1ResourcesStable = m.confirmGatewayV1Resources(ctx, observation)
	observation.V2ResourcesStable = m.confirmGatewayV2Resources(ctx, state, observation)
	stageConfigStable := true
	if observation.StageContainerFound {
		var confirmedLive []byte
		if observation.StageContainer.Running && !observation.StageContainer.Restarting {
			confirmedLive, err = m.inspectLiveCaddyConfig(ctx, observation.StageContainer.ID)
			if err != nil {
				return observation, err
			}
		}
		var confirmedRestart []byte
		if requireStoppedStageConfig || observation.StageContainer.Running || observation.StageContainer.Restarting {
			var configErr error
			confirmedRestart, configErr = m.inspectStoppedCaddyRestartConfig(ctx, observation.StageContainer.ID, state.Identity.StageConfigFilename)
			if configErr != nil {
				clear(confirmedLive)
				return observation, configErr
			}
		}
		stageConfigStable = sameOptionalCaddyConfig(observation.StageConfig, confirmedLive) &&
			(!requireStoppedStageConfig && !observation.StageContainer.Running && !observation.StageContainer.Restarting ||
				sameCaddyConfig(observation.StageRestartConfig, confirmedRestart))
		clear(confirmedLive)
		clear(confirmedRestart)
	}
	confirmedStage, confirmedStageRuntime, confirmedStageFound, inspectErr := m.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if inspectErr != nil {
		return observation, inspectErr
	}
	observation.StageStable = stageConfigStable && observation.StageContainerFound == confirmedStageFound && (!confirmedStageFound ||
		(reflect.DeepEqual(observation.StageContainer, confirmedStage) && reflect.DeepEqual(observation.StageRuntime, confirmedStageRuntime)))
	finalConfigStable := true
	if observation.FinalContainerFound {
		var confirmedLive []byte
		if observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
			confirmedLive, err = m.inspectLiveCaddyConfig(ctx, observation.FinalContainer.ID)
			if err != nil {
				return observation, err
			}
		}
		confirmedRestart, configErr := m.inspectStoppedCaddyRestartConfig(ctx, observation.FinalContainer.ID, state.Identity.ActiveConfigFilename)
		if configErr != nil {
			clear(confirmedLive)
			return observation, configErr
		}
		finalConfigStable = sameOptionalCaddyConfig(observation.FinalConfig, confirmedLive) && sameCaddyConfig(observation.FinalRestartConfig, confirmedRestart)
		clear(confirmedLive)
		clear(confirmedRestart)
	}
	confirmedFinal, confirmedFinalRuntime, confirmedFinalFound, inspectErr := m.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if inspectErr != nil {
		return observation, inspectErr
	}
	observation.FinalStable = finalConfigStable && observation.FinalContainerFound == confirmedFinalFound && (!confirmedFinalFound ||
		(reflect.DeepEqual(observation.FinalContainer, confirmedFinal) && reflect.DeepEqual(observation.FinalRuntime, confirmedFinalRuntime)))
	observation.OwnedInventoriesStable = m.confirmGatewayV2OwnedInventories(ctx, observation)
	if requireV1EndpointIdentity {
		confirmedV1ApplicationNetworks, confirmErr := m.reinspectGatewayApplicationNetworks(ctx, observation.V1ApplicationNetworks, observation.V1ApplicationNetworkIDs)
		if confirmErr != nil {
			return observation, confirmErr
		}
		v1EndpointIdentityAfter, inspectErr := m.inspectGatewayEndpointIdentitySnapshot(ctx, source.Active, confirmedV1ApplicationNetworks)
		if inspectErr != nil {
			return observation, inspectErr
		}
		observation.V1EndpointIdentityProven = v1EndpointIdentityBefore == v1EndpointIdentityAfter
	}
	if finalEndpointIdentityBefore != "" {
		confirmedApplicationNetworks, inspectErr := m.reinspectGatewayApplicationNetworks(ctx, observation.ApplicationNetworks, observation.ApplicationNetworkIDs)
		if inspectErr != nil {
			return observation, inspectErr
		}
		finalEndpointIdentityAfter, inspectErr := m.inspectGatewayEndpointIdentitySnapshot(ctx, gatewayV2RouteRecords(state), confirmedApplicationNetworks)
		if inspectErr != nil {
			return observation, inspectErr
		}
		observation.FinalEndpointIdentityProven = finalEndpointIdentityBefore == finalEndpointIdentityAfter
	}
	return observation, nil
}

func classifyGatewayV2Topology(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) gatewayObservedTopology {
	requireV1RollbackTopology := journal.Phase != gatewayPhaseCommitted
	if !validGatewayTopologyInputs(source, state, journal) || !validGatewayPinnedImage(observation.Image, observation.ImageFound) ||
		!gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV1Base(source, journal, observation, requireV1RollbackTopology) || !observation.StageStable || !observation.FinalStable || !observation.OwnedInventoriesStable {
		return gatewayTopologyUnknownOrDrift
	}

	infraAbsent := observation.V2ResourcesStable && !observation.ConfigVolumeFound && !observation.DataVolumeFound && !observation.IngressFound &&
		len(observation.OwnedVolumes) == 0 && len(observation.OwnedNetworks) == 0
	infraExactIdle := observation.V2ResourcesStable && validContainerID(observation.IngressNetworkID) && validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, "", "")

	v1Serving := observation.V1Stable && observation.V1ResourcesStable && observation.V1EndpointIdentityProven &&
		observation.V1Container.Running && !observation.V1Container.Restarting
	v1StoppedRestartable := observation.V1Stable && observation.V1ResourcesStable &&
		!observation.V1Container.Running && !observation.V1Container.Restarting
	v1RollbackReady := v1StoppedRestartable && observation.V1EndpointIdentityProven
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
	if validGatewayV2FinalTopology(state, journal, observation, v1StoppedRestartable, v1RollbackReady,
		validGatewayV2FinalConfig(state, observation.FinalConfig, observation.FinalRestartConfig)) {
		return gatewayTopologyExactFinalV2
	}
	return gatewayTopologyUnknownOrDrift
}

func classifyGatewayV2MixedRestart(source routeState, committed, proposed gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation) bool {
	if !validGatewayV2MixedRestartInputs(source, committed, proposed, journal) || !validGatewayPinnedImage(observation.Image, observation.ImageFound) ||
		!gatewayV2ObservedResourcesMatchJournal(journal, observation) ||
		!validGatewayV1Base(source, journal, observation, false) || !observation.StageStable || !observation.FinalStable || !observation.OwnedInventoriesStable {
		return false
	}
	v1StoppedRestartable := observation.V1Stable && observation.V1ResourcesStable &&
		!observation.V1Container.Running && !observation.V1Container.Restarting
	return validGatewayV2FinalTopology(proposed, journal, observation, v1StoppedRestartable, false,
		validGatewayV2MixedFinalConfig(committed, proposed, observation.FinalConfig, observation.FinalRestartConfig))
}

func validGatewayV2FinalTopology(state gatewayV2RouteState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation,
	v1StoppedRestartable, v1RollbackReady, configProven bool,
) bool {
	return v1StoppedRestartable && (journal.Phase == gatewayPhaseCommitted || v1RollbackReady) && observation.V2ResourcesStable &&
		!observation.StageContainerFound && validOwnedNameSet(observation.OwnedContainers, state.Identity.FinalContainer) &&
		validContainerID(observation.IngressNetworkID) && validGatewayV2Volumes(state, journal, observation) &&
		validGatewayV2Container(state, journal, observation.FinalContainer, observation.FinalRuntime, observation.FinalContainerFound, gatewayV2FinalContainerRole, observation.Image.ID) &&
		validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, observation.FinalContainer.ID, state.Identity.FinalContainer) &&
		validGatewayV2ApplicationNetworks(state, observation.FinalContainer, observation.ApplicationNetworks, observation.ApplicationNetworkIDs) &&
		configProven && observation.Final404Proven && observation.FinalRoutesProven && observation.FinalHostPublicationProven &&
		observation.FinalEndpointIdentityProven && observation.FinalStable
}

func validGatewayV2MixedRestartInputs(source routeState, committed, proposed gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	return journal.Phase == gatewayPhaseCommitted && validGatewayTopologyInputs(source, committed, journal) && validGatewayTopologyInputs(source, proposed, journal) &&
		committed.OperationID == proposed.OperationID && committed.SourceV1StateDigest == proposed.SourceV1StateDigest &&
		committed.Profile == proposed.Profile && committed.UpgradeAction == proposed.UpgradeAction && committed.Identity == proposed.Identity &&
		committed.Network == proposed.Network && validGatewayV2MixedRouteTransition(committed, proposed)
}

func validGatewayV2MixedRouteTransition(committed, proposed gatewayV2RouteState) bool {
	changedAppID := ""
	for appID, committedApp := range committed.Apps {
		proposedApp, exists := proposed.Apps[appID]
		if exists && reflect.DeepEqual(committedApp, proposedApp) {
			continue
		}
		if changedAppID != "" || !exists {
			return false
		}
		changedAppID = appID
	}
	for appID := range proposed.Apps {
		if _, exists := committed.Apps[appID]; exists {
			continue
		}
		if changedAppID != "" {
			return false
		}
		changedAppID = appID
	}
	if changedAppID == "" {
		return false
	}
	proposedApp, exists := proposed.Apps[changedAppID]
	if !exists {
		return false
	}
	pendingState := cloneGatewayV2RouteState(committed)
	var previous *gatewayV2AppRoute
	pendingKind := gatewayV2PendingRouteSwitch
	if committedApp, exists := committed.Apps[changedAppID]; exists {
		switch {
		case committedApp.Route.Slot != proposedApp.Route.Slot:
			pendingKind = gatewayV2PendingRouteSwitch
		case reflect.DeepEqual(committedApp.Route, proposedApp.Route) && committedApp.LAN == nil && proposedApp.LAN != nil:
			pendingKind = gatewayV2PendingLANGrant
		default:
			return false
		}
		cloned := cloneGatewayV2AppRoute(committedApp)
		previous = &cloned
	}
	pendingState.Pending = &gatewayV2PendingRoute{
		Kind: pendingKind, AppID: changedAppID, Previous: previous, Proposed: cloneGatewayV2AppRoute(proposedApp),
	}
	return validGatewayV2RouteState(pendingState)
}

func validGatewayTopologyInputs(source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	if !validRouteState(source) || source.Pending != nil || !validGatewayV2RouteState(state) || state.Pending != nil || !validGatewayMigrationJournal(journal) {
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

func validGatewayV1Base(source routeState, journal gatewayMigrationJournal, observation gatewayV2DockerObservation, requireRollbackTopology bool) bool {
	if !observation.V1ContainerFound || !observation.V1VolumeFound || !observation.V1NetworkFound ||
		!validGatewayContainerRuntime(observation.V1Runtime, true) ||
		!gatewayV2EffectivePortBindingsMatchConfigured(observation.V1Runtime.EffectivePortBindings, observation.V1Container.PortBindings) ||
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
	if !requireRollbackTopology {
		identityDigest, err := gatewayV1ObservedIdentityDigest(observation)
		return valid && err == nil && identityDigest == journal.Source.IdentityDigest
	}
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
	return validGatewayV2ContainerState(state, journal, value, runtime, found, role, imageID, true)
}

func validGatewayV2StoppedContainer(state gatewayV2RouteState, journal gatewayMigrationJournal, value caddyInspection, runtime gatewayContainerRuntime, found bool, role, imageID string) bool {
	return validGatewayV2ContainerState(state, journal, value, runtime, found, role, imageID, false)
}

func validGatewayV2ContainerState(state gatewayV2RouteState, journal gatewayMigrationJournal, value caddyInspection, runtime gatewayContainerRuntime, found bool, role, imageID string, running bool) bool {
	name, configFilename, restart := state.Identity.StageContainer, state.Identity.StageConfigFilename, gatewayV2StageRestartPolicy
	if role == gatewayV2FinalContainerRole {
		name, configFilename, restart = state.Identity.FinalContainer, state.Identity.ActiveConfigFilename, gatewayV2FinalRestartPolicy
	}
	if !found || value.Running != running || value.Restarting || !validGatewayContainerRuntime(runtime, false) || !validContainerID(value.ID) || normalizeID(value.Image) != normalizeID(imageID) ||
		strings.TrimPrefix(value.Name, "/") != name || value.Hostname != name || value.User != "1000:1000" || value.NetworkMode != state.Identity.IngressNetwork ||
		!exactGatewayV2Environment(value.Env) || !value.ReadOnly || value.Privileged || !onlyCaddyCapability(value.CapAdd) || !exactFoldSet(value.CapDrop, "ALL") ||
		!onlyNoNewPrivileges(value.SecurityOpt) || len(value.Binds) != 0 || len(value.Tmpfs) != 0 || value.Memory != 268435456 || value.MemorySwap != 268435456 ||
		value.NanoCPUs != 1_000_000_000 || value.PIDsLimit != 128 || value.LogType != "local" || len(value.LogConfig) != 2 ||
		value.LogConfig["max-size"] != "10m" || value.LogConfig["max-file"] != "3" || value.Restart != restart ||
		len(value.Entrypoint) != 1 || value.Entrypoint[0] != caddyExecutable || len(value.Cmd) != 3 || value.Cmd[0] != "run" || value.Cmd[1] != "--config" ||
		value.Cmd[2] != "/config/"+configFilename || len(value.Ulimits) != 1 || value.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!reflect.DeepEqual(value.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true)) ||
		!validGatewayV2Mounts(value.Mounts, state.Identity) || !validGatewayV2PortBindings(value.PortBindings, state, journal, role) ||
		(running && !gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, value.PortBindings)) ||
		(!running && gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)) {
		return false
	}
	expectedNetworks, valid := gatewayV2ExpectedContainerNetworks(state, role)
	if !valid {
		return false
	}
	if !running {
		return validGatewayV2StoppedContainerNetworks(state, role, expectedNetworks, runtime.ConfiguredNetworks)
	}
	if len(value.Networks) != len(expectedNetworks) {
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

func validGatewayV2StoppedContainerNetworks(state gatewayV2RouteState, role string, expected map[string]struct{}, actual map[string]gatewayV2ConfiguredNetwork) bool {
	if len(actual) != len(expected) {
		return false
	}
	for name := range expected {
		attachment, exists := actual[name]
		if !exists || !validContainerID(attachment.NetworkID) || attachment.EndpointID != "" || attachment.IPAddress != "" || attachment.IPv6Gateway != "" {
			return false
		}
		if name == state.Identity.IngressNetwork {
			if attachment.GwPriority != caddyGatewayPriority || attachment.IPAMConfig == nil ||
				attachment.IPAMConfig.IPv4Address != state.Network.ContainerIPv4 || attachment.IPAMConfig.IPv6Address != "" {
				return false
			}
			continue
		}
		if role != gatewayV2FinalContainerRole || attachment.GwPriority != 0 ||
			(attachment.IPAMConfig != nil && *attachment.IPAMConfig != (gatewayV2ConfiguredIPAM{})) {
			return false
		}
	}
	return true
}

func gatewayV2HasEffectivePortBinding(values map[string][]map[string]string) bool {
	for _, bindings := range values {
		if len(bindings) != 0 {
			return true
		}
	}
	return false
}

// Docker's NetworkSettings.Ports includes empty entries for ports exposed by
// the image even when HostConfig.PortBindings contains only the ports Rig
// published. Preserve exact equality for every configured publication while
// accepting only those additional keys that have no effective host binding.
func gatewayV2EffectivePortBindingsMatchConfigured(effective, configured map[string][]map[string]string) bool {
	for port, expected := range configured {
		actual, exists := effective[port]
		if !exists || !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	for port, bindings := range effective {
		if _, expected := configured[port]; !expected && len(bindings) != 0 {
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

func gatewayV2RouteRecords(state gatewayV2RouteState) map[string]routeRecord {
	routes := make(map[string]routeRecord, len(state.Apps))
	for appID, app := range state.Apps {
		routes[appID] = app.Route
	}
	return routes
}

func (m *Manager) inspectGatewayRouteEndpointProof(ctx context.Context, routes map[string]routeRecord) (string, error) {
	owners, valid := gatewayRouteNetworkOwners(routes)
	if m == nil || ctx == nil || !valid {
		return "", errors.New("invalid generated ingress route endpoint proof input")
	}
	networks := make(map[string]caddyNetworkInspection, len(owners))
	type networkIdentity struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	identities := make([]networkIdentity, 0, len(owners))
	for name, appID := range owners {
		network, id, found, err := m.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || !validContainerID(id) || !validApplicationNetwork(network.identity(), appID) {
			return "", errors.New("generated ingress route endpoint network is unavailable")
		}
		networks[name] = network
		identities = append(identities, networkIdentity{Name: name, ID: normalizeID(id)})
	}
	sort.Slice(identities, func(left, right int) bool { return identities[left].Name < identities[right].Name })
	endpointDigest, err := m.inspectGatewayEndpointIdentitySnapshot(ctx, routes, networks)
	if err != nil {
		return "", err
	}
	return canonicalDigest(struct {
		Version         int               `json:"version"`
		NetworkIdentity []networkIdentity `json:"networkIdentity"`
		EndpointDigest  string            `json:"endpointDigest"`
	}{Version: 1, NetworkIdentity: identities, EndpointDigest: endpointDigest})
}

func (m *Manager) proveGatewayRouteEndpointTransports(ctx context.Context, caddyID string, routes map[string]routeRecord) bool {
	if m == nil || ctx == nil || !validContainerID(caddyID) {
		return false
	}
	for _, route := range routes {
		if validateRoute(route) != nil {
			return false
		}
		for _, endpoint := range route.Endpoints {
			if m.probeGatewayEndpoint(ctx, caddyID, endpoint) != nil {
				return false
			}
		}
	}
	return true
}

// inspectGatewayEndpointIdentitySnapshot attests each endpoint by immutable
// container ID and proves that every requested network alias has exactly one
// owner across the complete Docker network membership. The returned digest is
// suitable for a before/after comparison around transport and route probes.
func (m *Manager) inspectGatewayEndpointIdentitySnapshot(ctx context.Context, routes map[string]routeRecord, networks map[string]caddyNetworkInspection) (string, error) {
	owners, valid := gatewayRouteNetworkOwners(routes)
	if m == nil || ctx == nil || !valid || len(networks) != len(owners) {
		return "", errors.New("invalid generated ingress endpoint identity input")
	}
	type expectedAlias struct {
		applicationID string
		containerID   string
		component     string
		role          string
		slot          string
	}
	expected := make(map[string]expectedAlias)
	for appID, route := range routes {
		for _, endpoint := range route.Endpoints {
			key := endpoint.NetworkName + "\x00" + endpoint.NetworkAlias
			want := expectedAlias{applicationID: appID, containerID: normalizeID(endpoint.ContainerID), component: endpoint.Component, role: endpoint.Role, slot: string(route.Slot)}
			if previous, exists := expected[key]; exists && previous != want {
				return "", errors.New("generated ingress endpoint alias ownership is ambiguous")
			}
			expected[key] = want
		}
	}

	inspected := make(map[string]endpointInspection)
	aliasOwners := make(map[string]string)
	aliasRecords := make([]gatewayNetworkAliasRecord, 0)
	networkNames := make([]string, 0, len(networks))
	for name := range networks {
		networkNames = append(networkNames, name)
	}
	sort.Strings(networkNames)
	for _, networkName := range networkNames {
		network, exists := networks[networkName]
		if !exists || network.Name != networkName || owners[networkName] == "" {
			return "", errors.New("generated ingress endpoint network identity is invalid")
		}
		seenMembers := make(map[string]struct{}, len(network.Containers))
		memberIDs := make([]string, 0, len(network.Containers))
		for id := range network.Containers {
			if !validContainerID(id) {
				return "", errors.New("generated ingress endpoint network member identity is invalid")
			}
			normalized := normalizeID(id)
			if _, duplicate := seenMembers[normalized]; duplicate {
				return "", errors.New("generated ingress endpoint network member identity is duplicated")
			}
			seenMembers[normalized] = struct{}{}
			memberIDs = append(memberIDs, id)
		}
		sort.Slice(memberIDs, func(left, right int) bool { return normalizeID(memberIDs[left]) < normalizeID(memberIDs[right]) })
		for _, memberID := range memberIDs {
			normalized := normalizeID(memberID)
			inspection, exists := inspected[normalized]
			if !exists {
				var err error
				inspection, err = m.inspectGatewayEndpointIdentity(ctx, memberID)
				if err != nil {
					return "", err
				}
				inspected[normalized] = inspection
			}
			attachment := inspection.Networks[networkName]
			member := network.Containers[memberID]
			memberPrefix, memberErr := netip.ParsePrefix(member.IPv4Address)
			if normalizeID(inspection.ID) != normalized || attachment == nil || attachment.IPAddress == "" || memberErr != nil ||
				!memberPrefix.Addr().Is4() || memberPrefix.Addr().String() != attachment.IPAddress {
				return "", errors.New("generated ingress endpoint network attachment is invalid")
			}
			aliases := append([]string(nil), attachment.Aliases...)
			sort.Strings(aliases)
			for index, alias := range aliases {
				if !validName(alias, 96) || (index > 0 && aliases[index-1] == alias) {
					return "", errors.New("generated ingress endpoint network aliases are invalid")
				}
				key := networkName + "\x00" + alias
				if prior, duplicate := aliasOwners[key]; duplicate && prior != normalized {
					aliasOwners[key] = ""
				} else if !duplicate {
					aliasOwners[key] = normalized
				}
			}
			aliasRecords = append(aliasRecords, gatewayNetworkAliasRecord{NetworkName: networkName, ContainerID: normalized, IPAddress: attachment.IPAddress, Aliases: aliases})
		}
	}

	endpointRecords := make([]gatewayEndpointIdentityRecord, 0, len(expected))
	for key, want := range expected {
		separator := strings.IndexByte(key, 0)
		if separator <= 0 || separator == len(key)-1 || aliasOwners[key] != want.containerID {
			return "", errors.New("generated ingress endpoint alias is not uniquely owned")
		}
		networkName, networkAlias := key[:separator], key[separator+1:]
		inspection, exists := inspected[want.containerID]
		if !exists || !inspection.Running || inspection.Health != "healthy" ||
			inspection.Labels["io.rig.managed"] != "generated-runtime" || inspection.Labels["io.rig.application"] != want.applicationID ||
			inspection.Labels["io.rig.component"] != want.component || inspection.Labels["io.rig.slot"] != want.slot || inspection.Labels["io.rig.role"] != want.role {
			return "", errors.New("generated ingress endpoint identity is invalid")
		}
		attachment := inspection.Networks[networkName]
		if attachment == nil {
			return "", errors.New("generated ingress endpoint attachment is unavailable")
		}
		aliases := append([]string(nil), attachment.Aliases...)
		sort.Strings(aliases)
		endpointRecords = append(endpointRecords, gatewayEndpointIdentityRecord{
			ApplicationID: want.applicationID, ContainerID: want.containerID, Component: want.component, Role: want.role, Slot: want.slot,
			NetworkName: networkName, NetworkAlias: networkAlias, IPAddress: attachment.IPAddress, Aliases: aliases,
		})
	}
	sort.Slice(endpointRecords, func(left, right int) bool {
		leftKey := endpointRecords[left].ApplicationID + "\x00" + endpointRecords[left].NetworkName + "\x00" + endpointRecords[left].NetworkAlias
		rightKey := endpointRecords[right].ApplicationID + "\x00" + endpointRecords[right].NetworkName + "\x00" + endpointRecords[right].NetworkAlias
		return leftKey < rightKey
	})
	sort.Slice(aliasRecords, func(left, right int) bool {
		leftKey := aliasRecords[left].NetworkName + "\x00" + aliasRecords[left].ContainerID
		rightKey := aliasRecords[right].NetworkName + "\x00" + aliasRecords[right].ContainerID
		return leftKey < rightKey
	})
	digest, err := canonicalDigest(struct {
		Version   int                             `json:"version"`
		Endpoints []gatewayEndpointIdentityRecord `json:"endpoints"`
		Members   []gatewayNetworkAliasRecord     `json:"members"`
	}{Version: 1, Endpoints: endpointRecords, Members: aliasRecords})
	if err != nil {
		return "", errors.New("generated ingress endpoint identity digest failed")
	}
	return digest, nil
}

func (m *Manager) inspectGatewayEndpointIdentity(ctx context.Context, containerID string) (endpointInspection, error) {
	var value endpointInspection
	found, err := m.inspectJSON(ctx, &value, "container", "inspect", "--format", endpointInspectFormat, containerID)
	if err != nil || !found || !validContainerID(value.ID) || normalizeID(value.ID) != normalizeID(containerID) {
		return endpointInspection{}, errors.New("generated ingress endpoint inspection failed")
	}
	return value, nil
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
	expected, err := expectedGatewayV2FinalConfig(state)
	return err == nil && sameCaddyConfig(expected, live) && sameCaddyConfig(expected, restart)
}

func validGatewayV2MixedFinalConfig(committed, proposed gatewayV2RouteState, live, restart []byte) bool {
	committedExpected, committedErr := expectedGatewayV2FinalConfig(committed)
	proposedExpected, proposedErr := expectedGatewayV2FinalConfig(proposed)
	return committedErr == nil && proposedErr == nil && !sameCaddyConfig(committedExpected, proposedExpected) &&
		sameCaddyConfig(proposedExpected, live) && sameCaddyConfig(committedExpected, restart)
}

func expectedGatewayV2FinalConfig(state gatewayV2RouteState) ([]byte, error) {
	routes, assignments := gatewayV2ConfigInputs(state)
	challenge, challengeErr := gatewayV2HostChallenge(state)
	if challengeErr != nil {
		return nil, challengeErr
	}
	return buildCaddyConfigV2(routes, net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd, ProbeToken: challenge}, assignments)
}

func buildGatewayV2StageConfig(state gatewayV2RouteState) ([]byte, error) {
	if !validGatewayV2RouteState(state) {
		return nil, errors.New("invalid generated ingress v2 stage state")
	}
	challenge, err := gatewayV2HostChallenge(state)
	if err != nil {
		return nil, err
	}
	servers := make(map[string]caddyServer, int(state.Profile.PortEnd-state.Profile.PortStart)+1)
	for port := state.Profile.PortStart; ; port++ {
		servers["lan_"+strconv.FormatUint(uint64(port), 10)] = caddyServer{
			Listen:         []string{net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(port), 10))},
			AutomaticHTTPS: caddyAutomaticHTTPS{Disable: true}, Routes: []caddyRoute{gatewayV2ProbeRoute(state.Profile.SelectedIPv4, gatewayV2PortChallenge(challenge, port)), notFoundRoute()},
		}
		if port == state.Profile.PortEnd {
			break
		}
	}
	return json.Marshal(caddyConfig{Admin: caddyAdmin{Listen: "localhost:2019"}, Apps: caddyApps{HTTP: caddyHTTP{Servers: servers}}})
}

func gatewayV2ConfigInputs(state gatewayV2RouteState) (map[string]routeRecord, map[uint16]caddyV2LANAssignment) {
	routes := make(map[string]routeRecord, len(state.Apps))
	assignments := make(map[uint16]caddyV2LANAssignment)
	for appID, app := range state.Apps {
		routes[appID] = app.Route
		if app.LAN != nil {
			assignments[app.LAN.Port] = caddyV2LANAssignment{
				AppID: appID, AllocationID: app.LAN.AllocationID,
				AccessRevisionID: app.LAN.AccessRevisionID, AccessRevisionNumber: app.LAN.AccessRevisionNumber,
				AccessSpecDigest: app.LAN.AccessSpecDigest,
			}
		}
	}
	return routes, assignments
}

func gatewayV2HostChallenge(state gatewayV2RouteState) (string, error) {
	planDigest, err := gatewayV2PlanDigest(state)
	if err != nil || !validCanonicalUUID(state.OperationID) || !validSHA256(state.Identity.Digest) {
		return "", errors.New("invalid generated ingress v2 host challenge input")
	}
	return canonicalDigest(struct {
		Context        string `json:"context"`
		OperationID    string `json:"operationId"`
		IdentityDigest string `json:"identityDigest"`
		PlanDigest     string `json:"planDigest"`
	}{
		Context: gatewayV2ChallengeContext, OperationID: state.OperationID,
		IdentityDigest: state.Identity.Digest, PlanDigest: planDigest,
	})
}

func sameCaddyConfig(expected, actual []byte) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	var expectedJSON, actualJSON any
	return json.Unmarshal(expected, &expectedJSON) == nil && json.Unmarshal(actual, &actualJSON) == nil && reflect.DeepEqual(expectedJSON, actualJSON)
}

func sameOptionalCaddyConfig(expected, actual []byte) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return len(expected) == 0 && len(actual) == 0
	}
	return sameCaddyConfig(expected, actual)
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

func (m *Manager) reinspectGatewayApplicationNetworks(ctx context.Context, inspections map[string]caddyNetworkInspection, ids map[string]string) (map[string]caddyNetworkInspection, error) {
	if len(inspections) != len(ids) {
		return nil, errors.New("generated ingress application network identity set changed")
	}
	confirmed := make(map[string]caddyNetworkInspection, len(inspections))
	for name, expected := range inspections {
		observed, id, found, err := m.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || id != ids[name] || !reflect.DeepEqual(observed, expected) {
			return nil, errors.New("generated ingress application network identity changed")
		}
		confirmed[name] = observed
	}
	return confirmed, nil
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

func proveGatewayV2StageHostPublication(ctx context.Context, state gatewayV2RouteState, caddyID string, probe gatewayV2HostStatusProbe, containerProbe gatewayV2ContainerChallengeProbe) bool {
	challenge, err := gatewayV2HostChallenge(state)
	if !validGatewayV2RouteState(state) || !validContainerID(caddyID) || err != nil || probe == nil || containerProbe == nil {
		return false
	}
	for port := state.Profile.PortStart; ; port++ {
		portChallenge := gatewayV2PortChallenge(challenge, port)
		if !exactGatewayV2HostChallenge(ctx, probe, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, portChallenge) ||
			!containerProbe(ctx, caddyID, state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, portChallenge) {
			return false
		}
		if !exactGatewayV2HostStatus(ctx, probe, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, http.StatusNotFound) ||
			!exactGatewayV2HostStatus(ctx, probe, state.Profile.SelectedIPv4, port, "wrong.invalid", http.StatusNotFound) ||
			gatewayV2LoopbackPublished(ctx, probe, port, state.Profile.SelectedIPv4) {
			return false
		}
		if port == state.Profile.PortEnd {
			return true
		}
	}
}

func proveGatewayV2FinalHostPublication(ctx context.Context, state gatewayV2RouteState, caddyID string, probe gatewayV2HostStatusProbe, containerProbe gatewayV2ContainerChallengeProbe) bool {
	challenge, err := gatewayV2HostChallenge(state)
	if !validGatewayV2RouteState(state) || !validContainerID(caddyID) || err != nil || probe == nil || containerProbe == nil {
		return false
	}
	_, assignments := gatewayV2ConfigInputs(state)
	for port := state.Profile.PortStart; ; port++ {
		portChallenge := gatewayV2PortChallenge(challenge, port)
		if !exactGatewayV2HostChallenge(ctx, probe, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, portChallenge) ||
			!containerProbe(ctx, caddyID, state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, portChallenge) {
			return false
		}
		if !exactGatewayV2HostStatus(ctx, probe, state.Profile.SelectedIPv4, port, "wrong.invalid", http.StatusNotFound) ||
			gatewayV2LoopbackPublished(ctx, probe, port, state.Profile.SelectedIPv4) {
			return false
		}
		result := probe(ctx, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, "/")
		if ctx.Err() != nil || !result.Connected || !result.Responded {
			return false
		}
		if assignment, assigned := assignments[port]; assigned {
			appChallenge := gatewayV2LANAppChallenge(challenge, port, assignment)
			if !exactGatewayV2HostChallenge(ctx, probe, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, appChallenge) ||
				!containerProbe(ctx, caddyID, state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, appChallenge) {
				return false
			}
			if result.Status < 200 || result.Status > 599 {
				return false
			}
		} else if result.Status != http.StatusNotFound {
			return false
		}
		if port == state.Profile.PortEnd {
			return true
		}
	}
}

func exactGatewayV2HostStatus(ctx context.Context, probe gatewayV2HostStatusProbe, address string, port uint16, host string, expected int) bool {
	result := probe(ctx, address, port, host, "/")
	return ctx.Err() == nil && result.Connected && result.Responded && result.Status == expected
}

func exactGatewayV2HostChallenge(ctx context.Context, probe gatewayV2HostStatusProbe, address string, port uint16, host, challenge string) bool {
	if !validSHA256(challenge) {
		return false
	}
	result := probe(ctx, address, port, host, gatewayV2ChallengePathPrefix+challenge)
	return ctx.Err() == nil && result.Connected && result.Responded && result.Status == http.StatusNotFound &&
		result.Body == gatewayV2ChallengeBodyPrefix+challenge
}

func gatewayV2LoopbackPublished(ctx context.Context, probe gatewayV2HostStatusProbe, port uint16, host string) bool {
	result := probe(ctx, "127.0.0.1", port, host, "/")
	return ctx.Err() != nil || result.Connected
}

func (m *Manager) probeGatewayV2ContainerChallenge(ctx context.Context, caddyID, address string, port uint16, host, challenge string) bool {
	parsed, err := netip.ParseAddr(address)
	hostAddress, hostErr := netip.ParseAddr(host)
	if m == nil || ctx == nil || !validContainerID(caddyID) || err != nil || !parsed.Is4() || hostErr != nil || !hostAddress.Is4() || !hostAddress.IsPrivate() ||
		hostAddress.String() != host || port == 0 || !validSHA256(challenge) {
		return false
	}
	result, runErr := m.run(ctx, gatewayProbeProcessTimeout, "container", "exec", caddyID,
		"curl", "--disable", "--silent", "--show-error", "--output", "-", "--write-out", "\n%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
		"--header", "Host: "+host,
		"http://"+net.JoinHostPort(parsed.String(), strconv.FormatUint(uint64(port), 10))+gatewayV2ChallengePathPrefix+challenge)
	defer clearResult(&result)
	want := gatewayV2ChallengeBodyPrefix + challenge + "\n404"
	return runErr == nil && string(result.Stdout) == want
}

// probeGatewayV2HostStatus originates the request on the host. This is
// intentionally distinct from the in-container Caddy probes: Docker Desktop
// can report a desired port binding without establishing the expected host
// listener. Proxy environment variables and redirects are disabled so the
// result describes only the selected local address.
func probeGatewayV2HostStatus(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
	parsed, err := netip.ParseAddr(address)
	challengePath := strings.HasPrefix(path, gatewayV2ChallengePathPrefix) && validSHA256(strings.TrimPrefix(path, gatewayV2ChallengePathPrefix))
	if ctx == nil || err != nil || !parsed.Is4() || port == 0 || (!validName(host, 253) && host != parsed.String()) || (path != "/" && !challengePath) {
		return gatewayV2HostProbeResult{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var connected atomic.Bool
	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 2 * time.Second,
		DialContext: func(dialContext context.Context, network, target string) (net.Conn, error) {
			connection, dialErr := dialer.DialContext(dialContext, network, target)
			if dialErr == nil {
				connected.Store(true)
			}
			return connection, dialErr
		},
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet,
		"http://"+net.JoinHostPort(parsed.String(), strconv.FormatUint(uint64(port), 10))+path, nil)
	if err != nil {
		return gatewayV2HostProbeResult{}
	}
	request.Host = host
	request.Header.Set("Connection", "close")
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return gatewayV2HostProbeResult{Connected: connected.Load()}
	}
	status := response.StatusCode
	body := ""
	if challengePath {
		bodyBytes, readErr := io.ReadAll(io.LimitReader(response.Body, 256))
		if readErr != nil || len(bodyBytes) >= 256 {
			_ = response.Body.Close()
			clear(bodyBytes)
			return gatewayV2HostProbeResult{Status: status, Connected: connected.Load()}
		}
		body = string(bodyBytes)
		clear(bodyBytes)
	}
	_ = response.Body.Close()
	return gatewayV2HostProbeResult{Status: status, Body: body, Connected: connected.Load(), Responded: status >= 100 && status <= 599}
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
	clear(value.V1Runtime.ConfiguredNetworks)
	clear(value.StageRuntime.ConfiguredNetworks)
	clear(value.FinalRuntime.ConfiguredNetworks)
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
