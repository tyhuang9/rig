//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
)

type generatedDomainColdRetainedV2Presence uint8

const (
	generatedDomainColdRetainedV2Absent generatedDomainColdRetainedV2Presence = iota + 1
	generatedDomainColdRetainedV2Present
)

type generatedDomainColdRetainedV2Observation struct {
	image                       imageInspection
	configVolume                volumeInspection
	configVolumeIdentity        gatewayV1VolumeIdentity
	dataVolume                  volumeInspection
	dataVolumeIdentity          gatewayV1VolumeIdentity
	ingressNetwork              caddyNetworkInspection
	ingressNetworkID            string
	finalContainer              caddyInspection
	finalRuntime                gatewayContainerRuntime
	finalRestartConfig          []byte
	applicationNetworks         map[string]caddyNetworkInspection
	applicationNetworkIDs       map[string]string
	ownedContainers             []string
	ownedVolumes                []string
	ownedNetworks               []string
	v2ResourcesStable           bool
	stageStable                 bool
	finalStable                 bool
	ownedInventoriesStable      bool
	finalEndpointIdentityProven bool
}

type generatedDomainColdRetainedV2Proof struct {
	presence  generatedDomainColdRetainedV2Presence
	authority *generatedDomainRetainedV2Projection
	objects   []generatedDomainDockerObject
	observed  generatedDomainColdRetainedV2Observation
	digest    string
}

type generatedDomainColdRetainedV2SelectionStatus uint8

const (
	generatedDomainColdRetainedV2SelectionInconsistent generatedDomainColdRetainedV2SelectionStatus = iota
	generatedDomainColdRetainedV2SelectionUnsupported
	generatedDomainColdRetainedV2SelectionSupported
)

func attestGeneratedDomainColdRetainedV2(ctx context.Context, manager *Manager,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
) (generatedDomainColdRetainedV2Proof, error) {
	if ctx == nil || manager == nil {
		return generatedDomainColdRetainedV2Proof{}, errGeneratedDomainConstructionRefused
	}
	projection, status := generatedDomainColdRetainedV2SelectedProjection(expected)
	if status == generatedDomainColdRetainedV2SelectionInconsistent {
		return generatedDomainColdRetainedV2Proof{}, errGeneratedDomainConstructionRefused
	}
	objects, presence, err := generatedDomainColdRetainedV2Objects(snapshot, projection.state, projection.journal)
	if err != nil {
		return generatedDomainColdRetainedV2Proof{}, err
	}
	result := generatedDomainColdRetainedV2Proof{presence: presence}
	if status == generatedDomainColdRetainedV2SelectionSupported {
		copy := cloneGeneratedDomainRetainedV2Projection(projection)
		result.authority = &copy
	}
	if presence == generatedDomainColdRetainedV2Absent {
		result.digest, err = generatedDomainColdRetainedV2Digest(result)
		return result, err
	}
	if status != generatedDomainColdRetainedV2SelectionSupported {
		return generatedDomainColdRetainedV2Proof{}, errGeneratedDomainConstructionRefused
	}
	first, err := manager.inspectGatewayV2DockerWithStageConfig(ctx, projection.source, projection.state,
		projection.journal, true, gatewayV2DockerProbePassiveRebind)
	if err != nil || !validGeneratedDomainColdRetainedV2Observation(projection, first) {
		return generatedDomainColdRetainedV2Proof{}, errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	second, err := manager.inspectGatewayV2DockerWithStageConfig(ctx, projection.source, projection.state,
		projection.journal, true, gatewayV2DockerProbePassiveRebind)
	if err != nil || !validGeneratedDomainColdRetainedV2Observation(projection, second) ||
		!sameGeneratedDomainColdRetainedV2Observation(first, second, projection.state.Identity) {
		return generatedDomainColdRetainedV2Proof{}, errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	if !generatedDomainColdRetainedV2CensusMatches(objects, projection, second) {
		return generatedDomainColdRetainedV2Proof{}, errGeneratedDomainConstructionRefused
	}
	result.objects = cloneGeneratedDomainColdRetainedGatewayObjects(objects)
	result.observed = projectGeneratedDomainColdRetainedV2Observation(second)
	result.digest, err = generatedDomainColdRetainedV2Digest(result)
	if err != nil {
		return generatedDomainColdRetainedV2Proof{}, err
	}
	return result, nil
}

func generatedDomainColdRetainedV2SelectedProjection(expected generatedDomainGatewayExpectation) (
	generatedDomainRetainedV2Projection, generatedDomainColdRetainedV2SelectionStatus,
) {
	if expected.kind != generatedDomainGatewayCurrentV1 || expected.currentV1 == nil || expected.terminal == nil ||
		expected.nativeV1 != nil || expected.nativeV2 != nil || expected.journal != nil || expected.upgrade != nil ||
		expected.currentV2 != nil {
		if expected.retainedV2 != nil {
			return generatedDomainRetainedV2Projection{}, generatedDomainColdRetainedV2SelectionInconsistent
		}
		return generatedDomainRetainedV2Projection{}, generatedDomainColdRetainedV2SelectionUnsupported
	}
	if expected.retainedV2 == nil {
		return generatedDomainRetainedV2Projection{}, generatedDomainColdRetainedV2SelectionUnsupported
	}
	projection := cloneGeneratedDomainRetainedV2Projection(*expected.retainedV2)
	if expected.currentV1.Digest != expected.stateDigest ||
		!reflect.DeepEqual(projection.current, *expected.currentV1) ||
		!reflect.DeepEqual(projection.terminal, *expected.terminal) ||
		!generatedDomainRetainedV2ProjectionValid(&projection) {
		return generatedDomainRetainedV2Projection{}, generatedDomainColdRetainedV2SelectionInconsistent
	}
	return projection, generatedDomainColdRetainedV2SelectionSupported
}

func generatedDomainColdRetainedV2Objects(snapshot generatedDomainDockerStartupSnapshot,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) ([]generatedDomainDockerObject, generatedDomainColdRetainedV2Presence, error) {
	identity := state.Identity
	expected := []struct{ kind, name string }{
		{kind: "container", name: identity.FinalContainer},
		{kind: "network", name: identity.IngressNetwork},
		{kind: "volume", name: identity.ConfigVolume},
		{kind: "volume", name: identity.DataVolume},
	}
	objects := make([]generatedDomainDockerObject, len(expected))
	counts := make([]int, len(expected))
	for _, object := range snapshot.objects {
		matched := false
		for index, value := range expected {
			if object.kind == value.kind && strings.TrimPrefix(object.name, "/") == value.name {
				counts[index]++
				objects[index] = cloneGeneratedDomainColdRetainedGatewayObject(object)
				matched = true
			}
		}
		if !matched && (object.kind == "container" && strings.TrimPrefix(object.name, "/") == identity.StageContainer ||
			generatedDomainColdRetainedV2OwnsObject(object, state, journal)) {
			return nil, 0, errGeneratedDomainConstructionRefused
		}
	}
	present := 0
	for _, count := range counts {
		if count > 0 {
			present++
		}
		if count > 1 {
			return nil, 0, errGeneratedDomainConstructionRefused
		}
	}
	if present == 0 {
		return nil, generatedDomainColdRetainedV2Absent, nil
	}
	if present != len(expected) {
		return nil, 0, errGeneratedDomainConstructionRefused
	}
	return objects, generatedDomainColdRetainedV2Present, nil
}

func generatedDomainColdRetainedV2OwnsObject(object generatedDomainDockerObject, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) bool {
	if object.kind == "image" || object.labels[gatewayV2IdentityLabelKey] != gatewayV2IdentityVersion ||
		object.labels[gatewayV2OperationLabelKey] != state.OperationID ||
		object.labels[gatewayV2IdentityDigestLabelKey] != state.Identity.Digest ||
		(journal.Target.PlanDigest != "" && object.labels[gatewayV2PlanDigestLabelKey] != journal.Target.PlanDigest) {
		return false
	}
	role := object.labels[gatewayV2ResourceRoleLabelKey]
	switch object.kind {
	case "container":
		return object.labels[gatewayV2ManagedLabelKey] == gatewayV2ManagedContainerLabel &&
			(role == gatewayV2StageContainerRole || role == gatewayV2FinalContainerRole)
	case "network":
		return object.labels[gatewayV2ManagedLabelKey] == gatewayV2ManagedNetworkLabel && role == gatewayV2IngressNetworkRole
	case "volume":
		return object.labels[gatewayV2ManagedLabelKey] == gatewayV2ManagedContainerLabel &&
			(role == gatewayV2ConfigVolumeRole || role == gatewayV2DataVolumeRole)
	default:
		return false
	}
}

func validGeneratedDomainColdRetainedV2Observation(projection generatedDomainRetainedV2Projection,
	value gatewayV2DockerObservation,
) bool {
	state, journal := projection.state, projection.journal
	configBinding, configErr := newGatewayV2VolumeResourceBinding(value.ConfigVolumeIdentity)
	dataBinding, dataErr := newGatewayV2VolumeResourceBinding(value.DataVolumeIdentity)
	expectedConfig, expectedErr := expectedGatewayV2FinalConfig(state)
	return generatedDomainRetainedV2ProjectionValid(&projection) &&
		validGatewayPinnedImage(value.Image, value.ImageFound) &&
		normalizeID(value.Image.ID) == journal.Resources.ImageID &&
		value.FinalContainerFound && normalizeID(value.FinalContainer.ID) == journal.Resources.FinalContainerID &&
		value.IngressFound && normalizeID(value.IngressNetworkID) == journal.Resources.IngressNetworkID &&
		configErr == nil && configBinding == journal.Resources.ConfigVolume &&
		dataErr == nil && dataBinding == journal.Resources.DataVolume &&
		!value.StageContainerFound &&
		validOwnedNameSet(value.OwnedContainers, state.Identity.FinalContainer) &&
		validOwnedNameSet(value.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume) &&
		validOwnedNameSet(value.OwnedNetworks, state.Identity.IngressNetwork) &&
		validGatewayV2Volumes(state, journal, value) &&
		validGatewayV2RebindStoppedContainer(state, journal, value.FinalContainer, value.FinalRuntime,
			value.FinalContainerFound, gatewayV2FinalContainerRole, value.Image.ID) &&
		validGatewayV2StoppedIngressNetwork(state, journal, value.IngressNetwork, value.IngressNetworkID,
			value.IngressFound, value.FinalRuntime) &&
		validGatewayV2StoppedApplicationNetworks(state, value.FinalContainer, value.FinalRuntime,
			value.ApplicationNetworks, value.ApplicationNetworkIDs) &&
		expectedErr == nil && len(value.FinalConfig) == 0 && sameCaddyConfig(expectedConfig, value.FinalRestartConfig) &&
		value.V2ResourcesStable && value.StageStable && value.FinalStable && value.OwnedInventoriesStable &&
		value.FinalEndpointIdentityProven
}

func sameGeneratedDomainColdRetainedV2Observation(first, second gatewayV2DockerObservation,
	identity gatewayV2Identity,
) bool {
	if !validGatewayV2Mounts(first.FinalContainer.Mounts, identity) ||
		!validGatewayV2Mounts(second.FinalContainer.Mounts, identity) {
		return false
	}
	left := projectGeneratedDomainColdRetainedV2Observation(first)
	right := projectGeneratedDomainColdRetainedV2Observation(second)
	return reflect.DeepEqual(left, right)
}

func projectGeneratedDomainColdRetainedV2Observation(value gatewayV2DockerObservation) generatedDomainColdRetainedV2Observation {
	result := generatedDomainColdRetainedV2Observation{
		image:                 cloneGeneratedDomainColdRetainedGatewayImage(value.Image),
		configVolume:          cloneGeneratedDomainColdRetainedGatewayVolume(value.ConfigVolume),
		configVolumeIdentity:  value.ConfigVolumeIdentity,
		dataVolume:            cloneGeneratedDomainColdRetainedGatewayVolume(value.DataVolume),
		dataVolumeIdentity:    value.DataVolumeIdentity,
		ingressNetwork:        cloneGeneratedDomainColdRetainedGatewayNetwork(value.IngressNetwork),
		ingressNetworkID:      value.IngressNetworkID,
		finalContainer:        cloneGeneratedDomainColdRetainedGatewayContainer(value.FinalContainer),
		finalRuntime:          cloneGeneratedDomainColdRetainedGatewayRuntime(value.FinalRuntime),
		finalRestartConfig:    append([]byte(nil), value.FinalRestartConfig...),
		applicationNetworks:   cloneGeneratedDomainColdRetainedV2Networks(value.ApplicationNetworks),
		applicationNetworkIDs: cloneGeneratedDomainColdRetainedGatewayStringMap(value.ApplicationNetworkIDs),
		ownedContainers:       append([]string(nil), value.OwnedContainers...),
		ownedVolumes:          append([]string(nil), value.OwnedVolumes...),
		ownedNetworks:         append([]string(nil), value.OwnedNetworks...),
		v2ResourcesStable:     value.V2ResourcesStable, stageStable: value.StageStable,
		finalStable: value.FinalStable, ownedInventoriesStable: value.OwnedInventoriesStable,
		finalEndpointIdentityProven: value.FinalEndpointIdentityProven,
	}
	result.finalContainer.Mounts = canonicalGatewayRebindTypedMounts(result.finalContainer.Mounts)
	return result
}

func cloneGeneratedDomainColdRetainedV2Networks(values map[string]caddyNetworkInspection) map[string]caddyNetworkInspection {
	if values == nil {
		return nil
	}
	result := make(map[string]caddyNetworkInspection, len(values))
	for name, value := range values {
		result[name] = cloneGeneratedDomainColdRetainedGatewayNetwork(value)
	}
	return result
}

func generatedDomainColdRetainedV2CensusMatches(objects []generatedDomainDockerObject,
	projection generatedDomainRetainedV2Projection, observed gatewayV2DockerObservation,
) bool {
	if len(objects) != 4 {
		return false
	}
	state, journal := projection.state, projection.journal
	descriptors := []generatedDomainRecoveryDockerDescriptor{
		{kind: "container", id: journal.Resources.FinalContainerID, name: state.Identity.FinalContainer,
			labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole, true)},
		{kind: "network", id: journal.Resources.IngressNetworkID, name: state.Identity.IngressNetwork,
			labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false),
			data:   generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}},
		{kind: "volume", name: state.Identity.ConfigVolume,
			labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole, false),
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
				mountpoint: observed.ConfigVolumeIdentity.Mountpoint, createdAt: observed.ConfigVolumeIdentity.CreatedAt}},
		{kind: "volume", name: state.Identity.DataVolume,
			labels: gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole, false),
			data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
				mountpoint: observed.DataVolumeIdentity.Mountpoint, createdAt: observed.DataVolumeIdentity.CreatedAt}},
	}
	for index := range objects {
		if !generatedDomainRecoveryDockerObjectMatches(objects[index], descriptors[index]) {
			return false
		}
	}
	return true
}

func (proof generatedDomainColdRetainedV2Proof) accounts(object generatedDomainDockerObject) bool {
	if proof.presence != generatedDomainColdRetainedV2Present || proof.authority == nil || proof.digest == "" {
		return false
	}
	for _, expected := range proof.objects {
		if reflect.DeepEqual(expected, object) {
			return true
		}
	}
	return false
}

func cloneGeneratedDomainColdRetainedV2Proof(value generatedDomainColdRetainedV2Proof) generatedDomainColdRetainedV2Proof {
	result := value
	if value.authority != nil {
		copy := cloneGeneratedDomainRetainedV2Projection(*value.authority)
		result.authority = &copy
	}
	result.objects = cloneGeneratedDomainColdRetainedGatewayObjects(value.objects)
	result.observed = cloneGeneratedDomainColdRetainedV2Observation(value.observed)
	return result
}

func cloneGeneratedDomainColdRetainedV2Observation(value generatedDomainColdRetainedV2Observation) generatedDomainColdRetainedV2Observation {
	value.image = cloneGeneratedDomainColdRetainedGatewayImage(value.image)
	value.configVolume = cloneGeneratedDomainColdRetainedGatewayVolume(value.configVolume)
	value.dataVolume = cloneGeneratedDomainColdRetainedGatewayVolume(value.dataVolume)
	value.ingressNetwork = cloneGeneratedDomainColdRetainedGatewayNetwork(value.ingressNetwork)
	value.finalContainer = cloneGeneratedDomainColdRetainedGatewayContainer(value.finalContainer)
	value.finalRuntime = cloneGeneratedDomainColdRetainedGatewayRuntime(value.finalRuntime)
	value.finalRestartConfig = append([]byte(nil), value.finalRestartConfig...)
	value.applicationNetworks = cloneGeneratedDomainColdRetainedV2Networks(value.applicationNetworks)
	value.applicationNetworkIDs = cloneGeneratedDomainColdRetainedGatewayStringMap(value.applicationNetworkIDs)
	value.ownedContainers = append([]string(nil), value.ownedContainers...)
	value.ownedVolumes = append([]string(nil), value.ownedVolumes...)
	value.ownedNetworks = append([]string(nil), value.ownedNetworks...)
	return value
}

func generatedDomainColdRetainedV2Digest(proof generatedDomainColdRetainedV2Proof) (string, error) {
	type digestObject struct {
		Kind, ID, Name string
		Labels         map[string]string
		Data           struct {
			Driver, Scope string
			Options       map[string]string
			Mountpoint    string
			CreatedAt     string
			ContentID     string
		}
	}
	var authority any
	if proof.authority != nil {
		authority = struct {
			Source         routeState
			State          gatewayV2RouteState
			Journal        gatewayMigrationJournal
			UpgradeLineage appaccess.GatewayCurrentLineageRef
			Current        gatewayCurrentRouteState
			Terminal       gatewayRebindAttemptTerminalView
		}{proof.authority.source, proof.authority.state, proof.authority.journal,
			proof.authority.upgradeLineage, proof.authority.current, proof.authority.terminal}
	}
	objects := make([]digestObject, len(proof.objects))
	for index, object := range proof.objects {
		objects[index].Kind, objects[index].ID, objects[index].Name = object.kind, object.id, object.name
		objects[index].Labels = object.labels
		objects[index].Data.Driver = object.data.driver
		objects[index].Data.Scope = object.data.scope
		objects[index].Data.Options = object.data.options
		objects[index].Data.Mountpoint = object.data.mountpoint
		objects[index].Data.CreatedAt = object.data.createdAt
		objects[index].Data.ContentID = object.data.contentID
	}
	observedDigest, err := canonicalDigest(struct {
		Image                 imageInspection
		ConfigVolume          volumeInspection
		ConfigVolumeIdentity  gatewayV1VolumeIdentity
		DataVolume            volumeInspection
		DataVolumeIdentity    gatewayV1VolumeIdentity
		IngressNetwork        caddyNetworkInspection
		IngressNetworkID      string
		FinalContainer        caddyInspection
		FinalRuntime          gatewayContainerRuntime
		FinalRestartConfig    []byte
		ApplicationNetworks   map[string]caddyNetworkInspection
		ApplicationNetworkIDs map[string]string
		OwnedContainers       []string
		OwnedVolumes          []string
		OwnedNetworks         []string
		Stable                [5]bool
	}{proof.observed.image, proof.observed.configVolume, proof.observed.configVolumeIdentity,
		proof.observed.dataVolume, proof.observed.dataVolumeIdentity, proof.observed.ingressNetwork,
		proof.observed.ingressNetworkID, proof.observed.finalContainer, proof.observed.finalRuntime,
		proof.observed.finalRestartConfig, proof.observed.applicationNetworks,
		proof.observed.applicationNetworkIDs, proof.observed.ownedContainers, proof.observed.ownedVolumes,
		proof.observed.ownedNetworks, [5]bool{proof.observed.v2ResourcesStable, proof.observed.stageStable,
			proof.observed.finalStable, proof.observed.ownedInventoriesStable,
			proof.observed.finalEndpointIdentityProven}})
	if err != nil {
		return "", errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	digest, err := canonicalDigest(struct {
		Version   int
		Presence  generatedDomainColdRetainedV2Presence
		Authority any
		Objects   []digestObject
		Observed  string
	}{1, proof.presence, authority, objects, observedDigest})
	if err != nil || !validSHA256(digest) {
		return "", errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	return digest, nil
}
