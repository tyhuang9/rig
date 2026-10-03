package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindStageConfigVolumeObservation struct {
	Network              caddyNetworkInspection
	NetworkID            string
	NetworkFound         bool
	ConfigVolume         volumeInspection
	ConfigVolumeIdentity gatewayV1VolumeIdentity
	ConfigVolumeFound    bool
	DataVolumeFound      bool
	StageContainerFound  bool
	FinalContainerFound  bool
	OwnedContainers      []string
	OwnedVolumes         []string
	OwnedNetworks        []string
}

type gatewayRebindStageConfigVolumeDriver interface {
	inspect(context.Context, gatewayRebindProtectedIntent) (gatewayRebindStageConfigVolumeObservation, error)
	inspectImage(context.Context) (imageInspection, bool, error)
	create(context.Context, gatewayRebindProtectedIntent) (string, error)
}

type managerGatewayRebindStageConfigVolumeDriver struct{ manager *Manager }

func (d managerGatewayRebindStageConfigVolumeDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageConfigVolumeObservation, error) {
	network, networkID, networkFound, err := d.manager.inspectNamedGatewayNetwork(ctx, intent.Intent.Identity.IngressNetwork)
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	config, configIdentity, configFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.ConfigVolume)
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	_, _, dataFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.DataVolume)
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	_, _, stageFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.StageContainer)
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	_, _, finalFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.FinalContainer)
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	containers, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "container", "ls", "--all")
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	volumes, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "volume", "ls")
	if err != nil {
		return gatewayRebindStageConfigVolumeObservation{}, err
	}
	networks, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "network", "ls")
	return gatewayRebindStageConfigVolumeObservation{
		Network: network, NetworkID: normalizeID(networkID), NetworkFound: networkFound,
		ConfigVolume: config, ConfigVolumeIdentity: configIdentity, ConfigVolumeFound: configFound,
		DataVolumeFound: dataFound, StageContainerFound: stageFound, FinalContainerFound: finalFound,
		OwnedContainers: containers, OwnedVolumes: volumes, OwnedNetworks: networks,
	}, err
}

func (d managerGatewayRebindStageConfigVolumeDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return d.manager.inspectImage(ctx)
}

func (d managerGatewayRebindStageConfigVolumeDriver) create(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (string, error) {
	args, err := gatewayRebindStageConfigVolumeCreateArgs(intent)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	defer clearResult(&result)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(result.Stdout))
	if name != intent.Intent.Identity.ConfigVolume {
		return "", errors.New("generated ingress rebind config volume create returned an invalid identity")
	}
	return name, nil
}

func gatewayRebindStageConfigVolumeCreateArgs(intent gatewayRebindProtectedIntent) ([]string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{"volume", "create", "--driver", "local"}
	args = appendGatewayV2Labels(args, gatewayRebindStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole))
	return append(args, intent.Intent.Identity.ConfigVolume), nil
}

func gatewayRebindStageConfigVolumeOwnershipDigest(intent gatewayRebindProtectedIntent) (string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return "", errors.New("invalid generated ingress rebind config volume ownership input")
	}
	return canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Driver  string            `json:"driver"`
		Scope   string            `json:"scope"`
		Options map[string]string `json:"options"`
		Labels  map[string]string `json:"labels"`
	}{
		Version: 1, Name: intent.Intent.Identity.ConfigVolume, Driver: "local", Scope: "local",
		Options: map[string]string{}, Labels: gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole),
	})
}

func validGatewayRebindStageConfigVolumeBinding(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageConfigVolumeBinding,
) bool {
	digest, err := gatewayRebindStageConfigVolumeOwnershipDigest(intent)
	return err == nil && validGatewayRebindStageConfigVolumeBindingValue(value) &&
		value.Name == intent.Intent.Identity.ConfigVolume && value.OwnershipDigest == digest
}

func gatewayRebindStageConfigVolumeBindingFromObservation(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageConfigVolumeObservation,
) (gatewayRebindStageConfigVolumeBinding, error) {
	digest, err := gatewayRebindStageConfigVolumeOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageConfigVolumeBinding{}, err
	}
	binding := gatewayRebindStageConfigVolumeBinding{
		Name: value.ConfigVolume.Name, Mountpoint: value.ConfigVolumeIdentity.Mountpoint,
		CreatedAt: value.ConfigVolumeIdentity.CreatedAt, OwnershipDigest: digest,
	}
	if !validGatewayRebindStageConfigVolumeBinding(intent, binding) {
		return gatewayRebindStageConfigVolumeBinding{}, errors.New("invalid generated ingress rebind config volume identity")
	}
	return binding, nil
}

func validGatewayRebindStageConfigVolumeObservation(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageConfigVolumeObservation, networkID string,
	expected *gatewayRebindStageConfigVolumeBinding,
) bool {
	networkOnly := gatewayRebindStageNetworkObservation{
		Network: value.Network, ID: value.NetworkID, Found: value.NetworkFound,
		OwnedNetworks: value.OwnedNetworks,
	}
	if !validGatewayRebindStageNetworkObservation(intent, networkOnly, networkID) ||
		value.DataVolumeFound || value.StageContainerFound || value.FinalContainerFound ||
		len(value.OwnedContainers) != 0 {
		return false
	}
	if expected == nil {
		return !value.ConfigVolumeFound && reflect.DeepEqual(value.ConfigVolume, volumeInspection{}) &&
			value.ConfigVolumeIdentity == (gatewayV1VolumeIdentity{}) && len(value.OwnedVolumes) == 0
	}
	if !value.ConfigVolumeFound || !validGatewayRebindStageConfigVolumeBinding(intent, *expected) ||
		!validOwnedNameSet(value.OwnedVolumes, intent.Intent.Identity.ConfigVolume) ||
		value.ConfigVolume.Name != intent.Intent.Identity.ConfigVolume || value.ConfigVolume.Driver != "local" ||
		value.ConfigVolume.Scope != "local" || len(value.ConfigVolume.Options) != 0 ||
		!reflect.DeepEqual(value.ConfigVolume.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)) {
		return false
	}
	actual, err := gatewayRebindStageConfigVolumeBindingFromObservation(intent, value)
	return err == nil && actual == *expected
}

type gatewayRebindStageConfigVolumeAttestation struct {
	Anchor                 gatewayRebindEffectBoundaryAnchor
	DockerDigest           string
	Candidates             []gatewayRebindSuccessorNetworkCandidate
	HostRoutes             []string
	HostInterfaces         []string
	DockerIDs              []string
	DockerPrefixes         []string
	NetworkID              string
	NetworkOwnershipDigest string
	ConfigVolume           *gatewayRebindStageConfigVolumeBinding
}

// stageGatewayRebindSuccessorConfigVolume is the second private rebind effect.
// It creates only the generation-scoped config volume after the exact network
// receipt has been reattested. It creates no data volume or container,
// publishes no port, changes no route, and never touches the predecessor.
func (m *Manager) stageGatewayRebindSuccessorConfigVolume(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.stageGatewayRebindSuccessorConfigVolumeWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageConfigVolumeDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) stageGatewayRebindSuccessorConfigVolumeWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigVolumeDriver,
	occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	if m == nil || ctx == nil || repository == nil || inspectDocker == nil || driver == nil ||
		reads.network.candidates == nil || reads.network.host == nil || reads.network.docker == nil ||
		reads.dockerIDs == nil || !validGatewayRebindProgressTime(occurredAt) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	releaseEffects, err := gatewayRebindAcquireDeploymentEffects(ctx, m.options.WorkingDirectory)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	releaseGateway, err := m.lockGatewayRaw(ctx)
	if err != nil {
		if releaseErr := releaseEffects(); releaseErr != nil {
			return &Error{Code: DiagnosticRouteUnresolved}
		}
		return err
	}
	defer func() {
		if releaseErr := releaseGateway(); releaseErr != nil {
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
		if releaseErr := releaseEffects(); releaseErr != nil {
			resultErr = &Error{Code: DiagnosticRouteUnresolved}
		}
	}()

	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 3 || len(history.Progress) > 4 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 4 {
		if stage.ConfigVolume == nil || m.attestGatewayRebindStageConfigVolumeLocked(ctx, repository, reads,
			inspectDocker, driver, intent, *stage, stage.Network.ID, stage.ConfigVolume, 4) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.ConfigVolume != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(history.Progress[2].Record.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	// Resolve every deterministic sequence-four input before the Docker
	// mutation. The observed Mountpoint and CreatedAt are the only values that
	// necessarily remain unavailable until after volume creation.
	if _, err := gatewayRebindStageConfigVolumeOwnershipDigest(intent); err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 4)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}

	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, observed, stage.Network.ID, nil) {
		// Sequence three has no volume identity binding. An existing volume,
		// including one left by a create-before-bind crash, cannot be adopted.
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindStageConfigVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 3)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageConfigVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 3)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindStageConfigVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 3)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindStageConfigVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 3)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	createdName, createErr := driver.create(ctx, intent)
	if createErr != nil || ctx.Err() != nil || createdName != intent.Intent.Identity.ConfigVolume {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err = driver.inspect(ctx, intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindStageConfigVolumeBindingFromObservation(intent, observed)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, observed, stage.Network.ID, &binding) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := m.attestGatewayRebindStageConfigVolumeLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, &binding, 3); err != nil {
		return err
	}
	record, err := newGatewayRebindStageConfigVolumeProgress(intent, history.Progress[2].Record,
		binding, occurredAt)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if progressStore.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageConfigVolumeLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *record.Stage, stage.Network.ID, record.Stage.ConfigVolume, 4)
}

func (m *Manager) attestGatewayRebindStageConfigVolumeLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigVolumeDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, networkID string,
	binding *gatewayRebindStageConfigVolumeBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageConfigVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, networkID, binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageConfigVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, networkID, binding, progressCount)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	final, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !gatewayRebindEffectBoundaryAnchorMatchesObservation(final,
		gatewayRebindEffectBoundaryObservation{database: first.Anchor.database, predecessor: first.Anchor.predecessor,
			intent: first.Anchor.intent, source: first.Anchor.source, databaseDigest: first.Anchor.databaseDigest,
			sourceDigest: first.Anchor.sourceDigest, progressCount: first.Anchor.progressCount,
			progressDigest: first.Anchor.progressDigest}) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (m *Manager) readGatewayRebindStageConfigVolumeAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigVolumeDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, networkID string,
	binding *gatewayRebindStageConfigVolumeBinding, progressCount uint64,
) (gatewayRebindStageConfigVolumeAttestation, error) {
	anchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !reflect.DeepEqual(anchor.intent, intent) || anchor.progressCount != progressCount ||
		stage.NetworkTopologyDigest != anchor.intent.NetworkObservationDigest || stage.Network == nil ||
		stage.Network.ID != networkID {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	networkOwnership, ownershipErr := gatewayRebindStageNetworkOwnershipDigest(intent)
	if ownershipErr != nil || stage.Network.OwnershipDigest != networkOwnership {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) ||
		history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	docker, err := inspectDocker(ctx, anchor.source, anchor.predecessor.State, anchor.predecessor.Journal)
	defer clearGatewayV2DockerObservation(&docker)
	if err != nil || !validGatewayRebindPredecessorDocker(anchor.source, anchor.predecessor.State,
		anchor.predecessor.Journal, docker) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerDigest, err := canonicalDigest(docker)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if err := attestGatewayRebindStageConfigVolumeImageOnce(ctx, driver, intent, stage); err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, err
	}
	current, err := readGatewayRebindStageConfigVolumePhysicalObservation(ctx, reads, driver,
		intent, networkID, binding)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, err
	}
	current.Anchor = anchor
	current.DockerDigest = dockerDigest
	return current, nil
}

func attestGatewayRebindStageConfigVolumeImageOnce(ctx context.Context,
	driver gatewayRebindStageConfigVolumeDriver, intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent,
) error {
	if ctx.Err() != nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest ||
		stage.ApprovedCaddyImageDigest != intent.Intent.Identity.CaddyImageDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	image, found, err := driver.inspectImage(ctx)
	if err != nil || ctx.Err() != nil || !validGatewayPinnedImage(image, found) ||
		normalizeID(image.ID) != stage.ObservedDockerImageID {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func readGatewayRebindStageConfigVolumePhysicalObservation(ctx context.Context,
	reads gatewayRebindSuccessorPreflightReads, driver gatewayRebindStageConfigVolumeDriver,
	intent gatewayRebindProtectedIntent, networkID string, binding *gatewayRebindStageConfigVolumeBinding,
) (gatewayRebindStageConfigVolumeAttestation, error) {
	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	prefixes, err := reads.network.docker(ctx)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, observed, networkID, binding) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedIDs = append(expectedIDs, networkID)
	sort.Strings(expectedIDs)
	if !equalStrings(idsBefore, expectedIDs) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	currentCandidates := gatewayRebindCandidateProjection(candidates)
	hostRoutes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	hostInterfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(prefixes)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedPrefixes = append(expectedPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedPrefixes)
	if !validGatewayRebindStageNetworkHostDelta(intent, currentCandidates, hostRoutes, hostInterfaces) ||
		!equalStrings(dockerPrefixes, expectedPrefixes) {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	networkOwnership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageConfigVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	var copiedBinding *gatewayRebindStageConfigVolumeBinding
	if binding != nil {
		value := *binding
		copiedBinding = &value
	}
	return gatewayRebindStageConfigVolumeAttestation{
		Candidates: currentCandidates, HostRoutes: hostRoutes, HostInterfaces: hostInterfaces,
		DockerIDs: append([]string{}, idsBefore...), DockerPrefixes: dockerPrefixes,
		NetworkID: networkID, NetworkOwnershipDigest: networkOwnership, ConfigVolume: copiedBinding,
	}, nil
}

var _ gatewayRebindStageConfigVolumeDriver = managerGatewayRebindStageConfigVolumeDriver{}
