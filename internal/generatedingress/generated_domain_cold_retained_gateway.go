//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type generatedDomainColdRetainedGatewayPresence uint8

const (
	generatedDomainColdRetainedGatewayAbsent generatedDomainColdRetainedGatewayPresence = iota + 1
	generatedDomainColdRetainedGatewayPresent
)

// generatedDomainColdRetainedGatewayAuthority binds an accepted retained V1
// triple to the exact committed upgrade selected by the canonical startup
// resolver. A current V2 projection is included when it is the selected
// upgrade-origin successor.
type generatedDomainColdRetainedGatewayAuthority struct {
	kind        generatedDomainGatewayKind
	stateDigest string
	generation  uint64
	operationID string
	source      routeState
	state       gatewayV2RouteState
	journal     gatewayMigrationJournal
	currentV2   *gatewayCurrentRouteStateV2
}

// generatedDomainColdRetainedGatewayProof is retained in the cold manifest so
// the final construction check compares a fresh protected selection, census,
// detailed Docker identity, stopped runtime, and restart configuration with
// the initial proof. digest is supplementary; the complete proof remains the
// controlling comparison.
type generatedDomainColdRetainedGatewayProof struct {
	presence       generatedDomainColdRetainedGatewayPresence
	authority      *generatedDomainColdRetainedGatewayAuthority
	objects        []generatedDomainDockerObject
	image          imageInspection
	container      caddyInspection
	runtime        gatewayContainerRuntime
	volume         volumeInspection
	volumeIdentity gatewayV1VolumeIdentity
	network        caddyNetworkInspection
	networkID      string
	restartConfig  []byte
	digest         string
}

type generatedDomainColdRetainedGatewaySelectionStatus uint8

const (
	generatedDomainColdRetainedGatewaySelectionInconsistent generatedDomainColdRetainedGatewaySelectionStatus = iota
	generatedDomainColdRetainedGatewaySelectionUnsupported
	generatedDomainColdRetainedGatewaySelectionSupported
)

func attestGeneratedDomainColdRetainedGateway(ctx context.Context, manager *Manager,
	authority *GeneratedCandidateAuthority, snapshot generatedDomainDockerStartupSnapshot,
	expected generatedDomainGatewayExpectation,
) (generatedDomainColdRetainedGatewayProof, error) {
	if ctx == nil {
		return generatedDomainColdRetainedGatewayProof{}, errGeneratedDomainConstructionRefused
	}
	result := generatedDomainColdRetainedGatewayProof{presence: generatedDomainColdRetainedGatewayAbsent}
	if generatedDomainColdActiveNativeV1Expectation(expected) {
		digest, err := generatedDomainColdRetainedGatewayDigest(result)
		result.digest = digest
		return result, err
	}
	objects, presence, err := generatedDomainColdRetainedGatewayObjects(snapshot)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdRetainedGatewayTriple, err)
	}
	selected, selectionStatus := generatedDomainColdRetainedGatewaySelectedAuthority(expected)
	if selectionStatus == generatedDomainColdRetainedGatewaySelectionSupported {
		result.authority = &selected
	}
	if presence == generatedDomainColdRetainedGatewayAbsent {
		result.digest, err = generatedDomainColdRetainedGatewayDigest(result)
		return result, err
	}
	if selectionStatus != generatedDomainColdRetainedGatewaySelectionSupported {
		reason := generatedDomainConstructionReasonColdRetainedGatewaySelectionAuthority
		if selectionStatus == generatedDomainColdRetainedGatewaySelectionUnsupported {
			reason = generatedDomainConstructionReasonColdRetainedGatewaySelectionKind
		}
		return generatedDomainColdRetainedGatewayProof{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			reason,
			errGeneratedDomainConstructionRefused)
	}

	reader, err := newGeneratedDomainColdStartupReader(manager, authority, snapshot, expected)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdRetainedGatewaySelectionReader, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(generatedDomainStartupObservationTimeout)
	}
	image, err := reader.readPinnedImage(ctx, deadline, selected.journal.Resources.ImageID)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainConstructionReasonColdRetainedGatewayRead, err)
	}
	container, runtime, err := reader.readDetailedContainer(ctx, deadline, objects[0].id)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, err
	}
	volume, volumeIdentity, err := reader.readDetailedVolume(ctx, deadline, objects[2].name)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, err
	}
	network, networkID, err := reader.readDetailedNetwork(ctx, deadline, objects[1].id)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, err
	}
	restart, err := reader.readRestartConfig(ctx, deadline, container.ID, gatewayV2ActiveConfigFile)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, err
	}
	defer clear(restart)

	observation := gatewayV2DockerObservation{
		Image: image, ImageFound: true,
		V1Container: container, V1Runtime: runtime, V1ContainerFound: true,
		V1Volume: volume, V1VolumeIdentity: volumeIdentity, V1VolumeFound: true,
		V1Network: network, V1NetworkID: networkID, V1NetworkFound: true,
		V1RestartConfig: restart,
	}
	if !validStoppedGatewayV1Base(selected.source, selected.journal, observation, false) {
		reason := generatedDomainColdRetainedGatewayRefusalReason(selected, observation)
		if reason == "" {
			return generatedDomainColdRetainedGatewayProof{}, errGeneratedDomainConstructionRefused
		}
		return generatedDomainColdRetainedGatewayProof{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction, reason, errGeneratedDomainConstructionRefused)
	}
	if !generatedDomainColdRetainedGatewayCensusMatches(objects, observation) {
		return generatedDomainColdRetainedGatewayProof{}, generatedDomainConstructionBoundaryDiagnostic(
			generatedDomainConstructionStageColdConstruction,
			generatedDomainColdRetainedGatewayFinalReason(objects, observation),
			errGeneratedDomainConstructionRefused)
	}

	result.presence = generatedDomainColdRetainedGatewayPresent
	result.objects = cloneGeneratedDomainColdRetainedGatewayObjects(objects)
	result.image = cloneGeneratedDomainColdRetainedGatewayImage(image)
	result.container = cloneGeneratedDomainColdRetainedGatewayContainer(container)
	result.runtime = cloneGeneratedDomainColdRetainedGatewayRuntime(runtime)
	result.volume = cloneGeneratedDomainColdRetainedGatewayVolume(volume)
	result.volumeIdentity = volumeIdentity
	result.network = cloneGeneratedDomainColdRetainedGatewayNetwork(network)
	result.networkID = networkID
	result.restartConfig = append([]byte(nil), restart...)
	result.digest, err = generatedDomainColdRetainedGatewayDigest(result)
	if err != nil {
		return generatedDomainColdRetainedGatewayProof{}, err
	}
	return result, nil
}

// generatedDomainColdRetainedGatewayRefusalReason classifies an already-refused
// stopped V1 proof. The original journal remains the sole acceptance input; the
// local copy cannot escape this function or contribute to an accepted proof.
func generatedDomainColdRetainedGatewayRefusalReason(selected generatedDomainColdRetainedGatewayAuthority,
	observation gatewayV2DockerObservation,
) generatedDomainConstructionReason {
	digest, err := gatewayV1ObservedIdentityDigest(observation)
	if err != nil {
		return generatedDomainConstructionReasonColdRetainedGatewayPolicy
	}
	copied := selected.journal
	copied.Source.IdentityDigest = digest
	if !validStoppedGatewayV1Base(selected.source, copied, observation, false) {
		return generatedDomainConstructionReasonColdRetainedGatewayPolicy
	}
	if digest != selected.journal.Source.IdentityDigest {
		return generatedDomainConstructionReasonColdRetainedGatewayDigest
	}
	return ""
}

// generatedDomainColdActiveNativeV1Expectation recognizes only the exact
// native V1 projection minted by the protected cold-start resolver. Its Docker
// objects remain owned by the active gateway census and its mandatory full
// attestation rather than the retained-predecessor proof.
func generatedDomainColdActiveNativeV1Expectation(expected generatedDomainGatewayExpectation) bool {
	if expected.kind != generatedDomainGatewayNativeV1 || expected.nativeV1 == nil ||
		expected.nativeV2 != nil || expected.journal != nil || expected.upgrade != nil ||
		expected.currentV1 != nil || expected.currentV2 != nil || expected.terminal != nil ||
		!validRouteState(*expected.nativeV1) || expected.nativeV1.Pending != nil {
		return false
	}
	digest, err := canonicalDigest(*expected.nativeV1)
	if err != nil || digest != expected.stateDigest ||
		!reflect.DeepEqual(expected.names, generatedDomainGatewayExpectedNames(expected)) {
		return false
	}
	ids, err := generatedDomainGatewayExpectedIDs(expected)
	return err == nil && reflect.DeepEqual(expected.ids, ids)
}

func generatedDomainColdRetainedGatewaySelectedAuthority(expected generatedDomainGatewayExpectation) (
	generatedDomainColdRetainedGatewayAuthority, generatedDomainColdRetainedGatewaySelectionStatus,
) {
	// These are the exact canonical current shapes that the retained V1 proof
	// deliberately does not authorize. This branch describes the observed
	// shape; it does not independently validate or mint current authority.
	if expected.kind == generatedDomainGatewayCurrentV1 && expected.currentV1 != nil && expected.terminal != nil &&
		expected.nativeV1 == nil && expected.nativeV2 == nil && expected.journal == nil && expected.upgrade == nil &&
		expected.currentV2 == nil {
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionUnsupported
	}
	if expected.kind == generatedDomainGatewayCurrentV2 && expected.currentV2 != nil && expected.terminal != nil &&
		expected.currentV2.Lineage.Kind == appaccess.GatewayRebindSourceGatewayRebind &&
		expected.nativeV1 == nil && expected.nativeV2 == nil && expected.journal == nil && expected.upgrade == nil &&
		expected.currentV1 == nil {
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionUnsupported
	}
	if expected.nativeV1 == nil || expected.nativeV2 == nil || expected.journal == nil || expected.upgrade == nil {
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
	}
	selection := *expected.upgrade
	if selection.Store == nil || !selection.Existing || selection.PartialState || selection.Retired || selection.Aborted ||
		selection.Journal.Phase != gatewayPhaseCommitted || selection.operationID != selection.Journal.OperationID ||
		selection.Store.generation != selection.Generation ||
		(selection.Generation == 0 && selection.Store.operationID != "") ||
		(selection.Generation > 0 && selection.Store.operationID != selection.operationID) ||
		!reflect.DeepEqual(selection.State, *expected.nativeV2) ||
		!reflect.DeepEqual(selection.Journal, *expected.journal) ||
		!validGatewayTopologyInputs(*expected.nativeV1, selection.State, selection.Journal) {
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
	}
	lineage, err := gatewayUpgradeCurrentLineage(selection)
	if err != nil {
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
	}
	stateDigest, err := canonicalDigest(selection.State)
	if err != nil {
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
	}
	result := generatedDomainColdRetainedGatewayAuthority{
		kind: expected.kind, stateDigest: expected.stateDigest, generation: selection.Generation,
		operationID: selection.operationID, source: cloneRouteState(*expected.nativeV1),
		state: cloneGatewayV2RouteState(selection.State), journal: selection.Journal,
	}
	switch expected.kind {
	case generatedDomainGatewayNativeV2:
		if expected.currentV2 != nil || stateDigest != expected.stateDigest {
			return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
		}
	case generatedDomainGatewayCurrentV2:
		if !generatedDomainCurrentV2UpgradeExpectationValid(expected) || expected.currentV2 == nil ||
			expected.currentV2.Lineage != lineage || expected.currentV2.Digest != expected.stateDigest {
			return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
		}
		current := cloneGatewayCurrentRouteStateV2(*expected.currentV2)
		result.currentV2 = &current
	default:
		return generatedDomainColdRetainedGatewayAuthority{}, generatedDomainColdRetainedGatewaySelectionInconsistent
	}
	return result, generatedDomainColdRetainedGatewaySelectionSupported
}

func generatedDomainColdRetainedGatewayObjects(snapshot generatedDomainDockerStartupSnapshot) (
	[]generatedDomainDockerObject, generatedDomainColdRetainedGatewayPresence, error,
) {
	descriptors := generatedDomainNativeV1DockerDescriptors()
	if len(descriptors) != 3 || descriptors[0].kind != "container" || descriptors[1].kind != "network" ||
		descriptors[2].kind != "volume" {
		return nil, 0, errGeneratedDomainConstructionRefused
	}
	result := make([]generatedDomainDockerObject, len(descriptors))
	counts := make([]int, len(descriptors))
	for _, object := range snapshot.objects {
		for index, descriptor := range descriptors {
			if object.kind == descriptor.kind && strings.TrimPrefix(object.name, "/") == descriptor.name {
				counts[index]++
				result[index] = cloneGeneratedDomainColdRetainedGatewayObject(object)
			}
		}
	}
	absent := true
	for _, count := range counts {
		if count != 0 {
			absent = false
		}
	}
	if absent {
		return nil, generatedDomainColdRetainedGatewayAbsent, nil
	}
	for _, count := range counts {
		if count != 1 {
			return nil, 0, errGeneratedDomainConstructionRefused
		}
	}
	return result, generatedDomainColdRetainedGatewayPresent, nil
}

func generatedDomainColdRetainedGatewayCensusMatches(objects []generatedDomainDockerObject,
	observation gatewayV2DockerObservation,
) bool {
	if len(objects) != 3 || objects[0].id != observation.V1Container.ID ||
		objects[1].id != observation.V1NetworkID || objects[2].id != "" {
		return false
	}
	descriptors := generatedDomainNativeV1DockerDescriptors()
	descriptors[0].id = observation.V1Container.ID
	descriptors[1].id = observation.V1NetworkID
	descriptors[2].data.mountpoint = observation.V1VolumeIdentity.Mountpoint
	descriptors[2].data.createdAt = observation.V1VolumeIdentity.CreatedAt
	for index := range objects {
		if !generatedDomainRecoveryDockerObjectMatches(objects[index], descriptors[index]) {
			return false
		}
	}
	return true
}

func generatedDomainColdRetainedGatewayFinalReason(objects []generatedDomainDockerObject,
	observation gatewayV2DockerObservation,
) generatedDomainConstructionReason {
	descriptors := generatedDomainNativeV1DockerDescriptors()
	if len(objects) != 3 || len(descriptors) != 3 ||
		descriptors[0].kind != "container" || descriptors[1].kind != "network" || descriptors[2].kind != "volume" {
		return generatedDomainConstructionReasonColdRetainedGatewayFinal
	}
	for index := range objects {
		if objects[index].kind != descriptors[index].kind ||
			strings.TrimPrefix(objects[index].name, "/") != descriptors[index].name {
			return generatedDomainConstructionReasonColdRetainedGatewayFinal
		}
	}
	descriptors[0].id = observation.V1Container.ID
	descriptors[1].id = observation.V1NetworkID
	descriptors[2].data.mountpoint = observation.V1VolumeIdentity.Mountpoint
	descriptors[2].data.createdAt = observation.V1VolumeIdentity.CreatedAt

	identityMismatch := objects[0].id != descriptors[0].id ||
		objects[1].id != descriptors[1].id || objects[2].id != "" ||
		!generatedDomainCanonicalDockerID(objects[0].id, false) ||
		!generatedDomainCanonicalDockerID(objects[1].id, false)
	labelsMismatch := !generatedDomainExactRigLabels(objects[0].labels, descriptors[0].labels) ||
		!generatedDomainExactRigLabels(objects[1].labels, descriptors[1].labels) ||
		!validGeneratedDomainNativeV1VolumeLabels(objects[2].labels)
	projectionMismatch := objects[1].data.driver != descriptors[1].data.driver ||
		objects[1].data.scope != descriptors[1].data.scope ||
		!reflect.DeepEqual(objects[1].data.options, descriptors[1].data.options) ||
		objects[2].data.driver != descriptors[2].data.driver ||
		objects[2].data.scope != descriptors[2].data.scope ||
		!reflect.DeepEqual(objects[2].data.options, descriptors[2].data.options) ||
		descriptors[2].data.mountpoint != "" && objects[2].data.mountpoint != descriptors[2].data.mountpoint ||
		descriptors[2].data.createdAt != "" && objects[2].data.createdAt != descriptors[2].data.createdAt

	mismatches := 0
	for _, mismatch := range []bool{identityMismatch, labelsMismatch, projectionMismatch} {
		if mismatch {
			mismatches++
		}
	}
	if mismatches != 1 {
		return generatedDomainConstructionReasonColdRetainedGatewayFinal
	}
	switch {
	case identityMismatch:
		return generatedDomainConstructionReasonColdRetainedGatewayFinalIdentity
	case labelsMismatch:
		return generatedDomainConstructionReasonColdRetainedGatewayFinalLabels
	case projectionMismatch:
		return generatedDomainConstructionReasonColdRetainedGatewayFinalProjection
	default:
		return generatedDomainConstructionReasonColdRetainedGatewayFinal
	}
}

func (proof generatedDomainColdRetainedGatewayProof) accounts(object generatedDomainDockerObject) bool {
	if proof.presence != generatedDomainColdRetainedGatewayPresent || proof.authority == nil || proof.digest == "" {
		return false
	}
	for _, expected := range proof.objects {
		if reflect.DeepEqual(expected, object) {
			return true
		}
	}
	return false
}

func newGeneratedDomainColdStartupReader(manager *Manager, authority *GeneratedCandidateAuthority,
	snapshot generatedDomainDockerStartupSnapshot, expected generatedDomainGatewayExpectation,
) (*generatedDomainStartupReader, error) {
	if manager == nil || authority == nil || manager.runner != authority.process ||
		expected.kind == generatedDomainGatewayNone || snapshot.daemonID == "" ||
		manager.options.DockerExecutable != authority.options.DockerExecutable ||
		manager.options.DockerEndpoint != authority.options.DockerEndpoint ||
		manager.options.DockerConfigDirectory != authority.options.DockerConfigDirectory ||
		manager.options.WorkingDirectory != authority.options.WorkingDirectory ||
		manager.options.CommandTimeout != authority.options.CommandTimeout ||
		manager.options.OutputLimit != authority.options.OutputLimit {
		return nil, errGeneratedDomainConstructionRefused
	}
	return &generatedDomainStartupReader{runner: manager.runner,
		options: generatedDomainOptions{DockerExecutable: manager.options.DockerExecutable,
			DockerEndpoint:        manager.options.DockerEndpoint,
			DockerConfigDirectory: manager.options.DockerConfigDirectory,
			WorkingDirectory:      manager.options.WorkingDirectory,
			DataRoot:              manager.options.DataRoot, HostPort: manager.options.HostPort,
			CommandTimeout: manager.options.CommandTimeout, PullTimeout: manager.options.PullTimeout,
			OutputLimit: manager.options.OutputLimit},
		environment: append([]string(nil), manager.dockerEnv...), daemonID: snapshot.daemonID,
		expectation: expected}, nil
}

func cloneGeneratedDomainColdRetainedGatewayProof(value generatedDomainColdRetainedGatewayProof) generatedDomainColdRetainedGatewayProof {
	result := value
	if value.authority != nil {
		authority := *value.authority
		authority.source = cloneRouteState(value.authority.source)
		authority.state = cloneGatewayV2RouteState(value.authority.state)
		if value.authority.currentV2 != nil {
			current := cloneGatewayCurrentRouteStateV2(*value.authority.currentV2)
			authority.currentV2 = &current
		}
		result.authority = &authority
	}
	result.objects = cloneGeneratedDomainColdRetainedGatewayObjects(value.objects)
	result.image = cloneGeneratedDomainColdRetainedGatewayImage(value.image)
	result.container = cloneGeneratedDomainColdRetainedGatewayContainer(value.container)
	result.runtime = cloneGeneratedDomainColdRetainedGatewayRuntime(value.runtime)
	result.volume = cloneGeneratedDomainColdRetainedGatewayVolume(value.volume)
	result.network = cloneGeneratedDomainColdRetainedGatewayNetwork(value.network)
	result.restartConfig = append([]byte(nil), value.restartConfig...)
	return result
}

func cloneGeneratedDomainColdRetainedGatewayObjects(values []generatedDomainDockerObject) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, len(values))
	for index := range values {
		result[index] = cloneGeneratedDomainColdRetainedGatewayObject(values[index])
	}
	return result
}

func cloneGeneratedDomainColdRetainedGatewayObject(value generatedDomainDockerObject) generatedDomainDockerObject {
	value.labels = cloneGeneratedDomainColdRetainedGatewayStringMap(value.labels)
	value.data.options = cloneGeneratedDomainColdRetainedGatewayStringMap(value.data.options)
	return value
}

func cloneGeneratedDomainColdRetainedGatewayImage(value imageInspection) imageInspection {
	value.RepoDigests = append([]string(nil), value.RepoDigests...)
	return value
}

func cloneGeneratedDomainColdRetainedGatewayContainer(value caddyInspection) caddyInspection {
	result := value
	result.Labels = cloneGeneratedDomainColdRetainedGatewayStringMap(value.Labels)
	result.Env = append([]string(nil), value.Env...)
	result.Entrypoint = append([]string(nil), value.Entrypoint...)
	result.Cmd = append([]string(nil), value.Cmd...)
	result.CapAdd = append([]string(nil), value.CapAdd...)
	result.CapDrop = append([]string(nil), value.CapDrop...)
	result.SecurityOpt = append([]string(nil), value.SecurityOpt...)
	result.Binds = append([]string(nil), value.Binds...)
	result.Mounts = append([]mountInspection(nil), value.Mounts...)
	result.Tmpfs = cloneGeneratedDomainColdRetainedGatewayStringMap(value.Tmpfs)
	result.LogConfig = cloneGeneratedDomainColdRetainedGatewayStringMap(value.LogConfig)
	result.Ulimits = append([]ulimitInspection(nil), value.Ulimits...)
	result.PortBindings = cloneGeneratedDomainColdRetainedGatewayPortBindings(value.PortBindings)
	if value.Networks == nil {
		result.Networks = nil
		return result
	}
	result.Networks = make(map[string]*networkAttachment, len(value.Networks))
	for name, attachment := range value.Networks {
		if attachment == nil {
			result.Networks[name] = nil
			continue
		}
		copy := *attachment
		copy.Aliases = append([]string(nil), attachment.Aliases...)
		result.Networks[name] = &copy
	}
	return result
}

func cloneGeneratedDomainColdRetainedGatewayRuntime(value gatewayContainerRuntime) gatewayContainerRuntime {
	result := value
	result.EffectivePortBindings = cloneGeneratedDomainColdRetainedGatewayPortBindings(value.EffectivePortBindings)
	if value.ConfiguredNetworks == nil {
		result.ConfiguredNetworks = nil
		return result
	}
	result.ConfiguredNetworks = make(map[string]gatewayV2ConfiguredNetwork, len(value.ConfiguredNetworks))
	for name, configured := range value.ConfiguredNetworks {
		copy := configured
		if configured.IPAMConfig != nil {
			ipam := *configured.IPAMConfig
			copy.IPAMConfig = &ipam
		}
		result.ConfiguredNetworks[name] = copy
	}
	return result
}

func cloneGeneratedDomainColdRetainedGatewayVolume(value volumeInspection) volumeInspection {
	value.Options = cloneGeneratedDomainColdRetainedGatewayStringMap(value.Options)
	value.Labels = cloneGeneratedDomainColdRetainedGatewayStringMap(value.Labels)
	return value
}

func cloneGeneratedDomainColdRetainedGatewayNetwork(value caddyNetworkInspection) caddyNetworkInspection {
	value.Options = cloneGeneratedDomainColdRetainedGatewayStringMap(value.Options)
	value.Labels = cloneGeneratedDomainColdRetainedGatewayStringMap(value.Labels)
	value.IPAM.Config = append([]networkIPAM(nil), value.IPAM.Config...)
	if value.Containers == nil {
		return value
	}
	containers := value.Containers
	value.Containers = make(map[string]caddyNetworkContainerInspection, len(containers))
	for id, member := range containers {
		value.Containers[id] = member
	}
	return value
}

func cloneGeneratedDomainColdRetainedGatewayPortBindings(value map[string][]map[string]string) map[string][]map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string][]map[string]string, len(value))
	for port, bindings := range value {
		result[port] = make([]map[string]string, len(bindings))
		for index, binding := range bindings {
			result[port][index] = cloneGeneratedDomainColdRetainedGatewayStringMap(binding)
		}
	}
	return result
}

func cloneGeneratedDomainColdRetainedGatewayStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func generatedDomainColdRetainedGatewayDigest(proof generatedDomainColdRetainedGatewayProof) (string, error) {
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
	type digestAuthority struct {
		Present     bool
		Kind        generatedDomainGatewayKind
		StateDigest string
		Generation  uint64
		OperationID string
		Source      routeState
		State       gatewayV2RouteState
		Journal     gatewayMigrationJournal
		CurrentV2   *gatewayCurrentRouteStateV2
	}
	envelope := struct {
		Version        int
		Presence       generatedDomainColdRetainedGatewayPresence
		Authority      digestAuthority
		Objects        []digestObject
		Image          imageInspection
		Container      caddyInspection
		Runtime        gatewayContainerRuntime
		Volume         volumeInspection
		VolumeIdentity gatewayV1VolumeIdentity
		Network        caddyNetworkInspection
		NetworkID      string
		RestartConfig  []byte
	}{Version: 1, Presence: proof.presence, Image: proof.image, Container: proof.container,
		Runtime: proof.runtime, Volume: proof.volume, VolumeIdentity: proof.volumeIdentity,
		Network: proof.network, NetworkID: proof.networkID, RestartConfig: proof.restartConfig}
	if proof.authority != nil {
		envelope.Authority = digestAuthority{Present: true, Kind: proof.authority.kind,
			StateDigest: proof.authority.stateDigest, Generation: proof.authority.generation,
			OperationID: proof.authority.operationID, Source: proof.authority.source,
			State: proof.authority.state, Journal: proof.authority.journal,
			CurrentV2: proof.authority.currentV2}
	}
	envelope.Objects = make([]digestObject, len(proof.objects))
	for index, object := range proof.objects {
		envelope.Objects[index].Kind, envelope.Objects[index].ID, envelope.Objects[index].Name =
			object.kind, object.id, object.name
		envelope.Objects[index].Labels = object.labels
		envelope.Objects[index].Data.Driver = object.data.driver
		envelope.Objects[index].Data.Scope = object.data.scope
		envelope.Objects[index].Data.Options = object.data.options
		envelope.Objects[index].Data.Mountpoint = object.data.mountpoint
		envelope.Objects[index].Data.CreatedAt = object.data.createdAt
		envelope.Objects[index].Data.ContentID = object.data.contentID
	}
	digest, err := canonicalDigest(envelope)
	if err != nil || !validSHA256(digest) {
		return "", errors.Join(errGeneratedDomainConstructionRefused, err)
	}
	return digest, nil
}
