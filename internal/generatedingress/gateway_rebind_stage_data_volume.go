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

type gatewayRebindStageDataVolumeObservation struct {
	Network              caddyNetworkInspection
	NetworkID            string
	NetworkFound         bool
	ConfigVolume         volumeInspection
	ConfigVolumeIdentity gatewayV1VolumeIdentity
	ConfigVolumeFound    bool
	DataVolume           volumeInspection
	DataVolumeIdentity   gatewayV1VolumeIdentity
	DataVolumeFound      bool
	StageContainerFound  bool
	FinalContainerFound  bool
	OwnedContainers      []string
	OwnedVolumes         []string
	OwnedNetworks        []string
}

type gatewayRebindStageDataVolumeDriver interface {
	inspect(context.Context, gatewayRebindProtectedIntent) (gatewayRebindStageDataVolumeObservation, error)
	inspectImage(context.Context) (imageInspection, bool, error)
	create(context.Context, gatewayRebindProtectedIntent) (string, error)
}

type managerGatewayRebindStageDataVolumeDriver struct{ manager *Manager }

func (d managerGatewayRebindStageDataVolumeDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageDataVolumeObservation, error) {
	network, networkID, networkFound, err := d.manager.inspectNamedGatewayNetwork(ctx, intent.Intent.Identity.IngressNetwork)
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	config, configIdentity, configFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.ConfigVolume)
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	data, dataIdentity, dataFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.DataVolume)
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	_, _, stageFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.StageContainer)
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	_, _, finalFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.FinalContainer)
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	containers, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "container", "ls", "--all")
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	volumes, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "volume", "ls")
	if err != nil {
		return gatewayRebindStageDataVolumeObservation{}, err
	}
	networks, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "network", "ls")
	return gatewayRebindStageDataVolumeObservation{
		Network: network, NetworkID: normalizeID(networkID), NetworkFound: networkFound,
		ConfigVolume: config, ConfigVolumeIdentity: configIdentity, ConfigVolumeFound: configFound,
		DataVolume: data, DataVolumeIdentity: dataIdentity, DataVolumeFound: dataFound,
		StageContainerFound: stageFound, FinalContainerFound: finalFound,
		OwnedContainers: containers, OwnedVolumes: volumes, OwnedNetworks: networks,
	}, err
}

func (d managerGatewayRebindStageDataVolumeDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return d.manager.inspectImage(ctx)
}

func (d managerGatewayRebindStageDataVolumeDriver) create(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (string, error) {
	args, err := gatewayRebindStageDataVolumeCreateArgs(intent)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	defer clearResult(&result)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(result.Stdout))
	if name != intent.Intent.Identity.DataVolume {
		return "", errors.New("generated ingress rebind data volume create returned an invalid identity")
	}
	return name, nil
}

func gatewayRebindStageDataVolumeCreateArgs(intent gatewayRebindProtectedIntent) ([]string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{"volume", "create", "--driver", "local"}
	args = appendGatewayV2Labels(args, gatewayRebindStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole))
	return append(args, intent.Intent.Identity.DataVolume), nil
}

func gatewayRebindStageDataVolumeOwnershipDigest(intent gatewayRebindProtectedIntent) (string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return "", errors.New("invalid generated ingress rebind data volume ownership input")
	}
	return canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Driver  string            `json:"driver"`
		Scope   string            `json:"scope"`
		Options map[string]string `json:"options"`
		Labels  map[string]string `json:"labels"`
	}{
		Version: 1, Name: intent.Intent.Identity.DataVolume, Driver: "local", Scope: "local",
		Options: map[string]string{}, Labels: gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole),
	})
}

func validGatewayRebindStageDataVolumeBinding(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageDataVolumeBinding,
) bool {
	digest, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	return err == nil && validGatewayRebindStageDataVolumeBindingValue(value) &&
		value.Name == intent.Intent.Identity.DataVolume && value.OwnershipDigest == digest
}

func gatewayRebindStageDataVolumeBindingFromObservation(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageDataVolumeObservation,
) (gatewayRebindStageDataVolumeBinding, error) {
	digest, err := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageDataVolumeBinding{}, err
	}
	binding := gatewayRebindStageDataVolumeBinding{
		Name: value.DataVolume.Name, Mountpoint: value.DataVolumeIdentity.Mountpoint,
		CreatedAt: value.DataVolumeIdentity.CreatedAt, OwnershipDigest: digest,
	}
	if !validGatewayRebindStageDataVolumeBinding(intent, binding) {
		return gatewayRebindStageDataVolumeBinding{}, errors.New("invalid generated ingress rebind data volume identity")
	}
	return binding, nil
}

func validGatewayRebindStageDataVolumeObservation(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageDataVolumeObservation, networkID string,
	config gatewayRebindStageConfigVolumeBinding, expected *gatewayRebindStageDataVolumeBinding,
) bool {
	networkOnly := gatewayRebindStageNetworkObservation{
		Network: value.Network, ID: value.NetworkID, Found: value.NetworkFound,
		OwnedNetworks: value.OwnedNetworks,
	}
	if !validGatewayRebindStageNetworkObservation(intent, networkOnly, networkID) ||
		value.StageContainerFound || value.FinalContainerFound ||
		len(value.OwnedContainers) != 0 {
		return false
	}
	if !value.ConfigVolumeFound || !validGatewayRebindStageConfigVolumeBinding(intent, config) ||
		value.ConfigVolume.Name != intent.Intent.Identity.ConfigVolume || value.ConfigVolume.Driver != "local" ||
		value.ConfigVolume.Scope != "local" || len(value.ConfigVolume.Options) != 0 ||
		!reflect.DeepEqual(value.ConfigVolume.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)) {
		return false
	}
	actualConfig := gatewayRebindStageConfigVolumeBinding{
		Name: value.ConfigVolume.Name, Mountpoint: value.ConfigVolumeIdentity.Mountpoint,
		CreatedAt: value.ConfigVolumeIdentity.CreatedAt, OwnershipDigest: config.OwnershipDigest,
	}
	if !validGatewayRebindStageConfigVolumeBinding(intent, actualConfig) || actualConfig != config {
		return false
	}
	if expected == nil {
		return !value.DataVolumeFound && reflect.DeepEqual(value.DataVolume, volumeInspection{}) &&
			value.DataVolumeIdentity == (gatewayV1VolumeIdentity{}) &&
			validOwnedNameSet(value.OwnedVolumes, intent.Intent.Identity.ConfigVolume)
	}
	if !value.DataVolumeFound || !validGatewayRebindStageDataVolumeBinding(intent, *expected) ||
		!validOwnedNameSet(value.OwnedVolumes, intent.Intent.Identity.ConfigVolume, intent.Intent.Identity.DataVolume) ||
		value.DataVolume.Name != intent.Intent.Identity.DataVolume || value.DataVolume.Driver != "local" ||
		value.DataVolume.Scope != "local" || len(value.DataVolume.Options) != 0 ||
		!reflect.DeepEqual(value.DataVolume.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole)) {
		return false
	}
	actual, err := gatewayRebindStageDataVolumeBindingFromObservation(intent, value)
	return err == nil && actual == *expected
}

type gatewayRebindStageDataVolumeAttestation struct {
	Anchor                 gatewayRebindEffectBoundaryAnchor
	DockerDigest           string
	Candidates             []gatewayRebindSuccessorNetworkCandidate
	HostRoutes             []string
	HostInterfaces         []string
	DockerIDs              []string
	DockerPrefixes         []string
	NetworkID              string
	NetworkOwnershipDigest string
	ConfigVolume           gatewayRebindStageConfigVolumeBinding
	DataVolume             *gatewayRebindStageDataVolumeBinding
}

// stageGatewayRebindSuccessorDataVolume is the third private rebind effect.
// It creates only the generation-scoped Caddy /data volume after the exact
// network and config-volume receipts have been reattested. It creates no container,
// publishes no port, changes no route, and never touches the predecessor.
func (m *Manager) stageGatewayRebindSuccessorDataVolume(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.stageGatewayRebindSuccessorDataVolumeWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageDataVolumeDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) stageGatewayRebindSuccessorDataVolumeWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageDataVolumeDriver,
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
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 4 || len(history.Progress) > 5 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.ConfigVolume == nil ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 5 {
		if stage.DataVolume == nil || m.attestGatewayRebindStageDataVolumeLocked(ctx, repository, reads,
			inspectDocker, driver, intent, *stage, stage.Network.ID, stage.DataVolume, 5) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.DataVolume != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(history.Progress[3].Record.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	// Resolve every deterministic sequence-five input before the Docker
	// mutation. The observed Mountpoint and CreatedAt are the only values that
	// necessarily remain unavailable until after volume creation.
	if _, err := gatewayRebindStageDataVolumeOwnershipDigest(intent); err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 5)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}

	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, observed, stage.Network.ID,
		*stage.ConfigVolume, nil) {
		// Sequence four has no data-volume identity binding. An existing volume,
		// including one left by a create-before-bind crash, cannot be adopted.
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindStageDataVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 4)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageDataVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 4)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindStageDataVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 4)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindStageDataVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, nil, 4)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	createdName, createErr := driver.create(ctx, intent)
	if createErr != nil || ctx.Err() != nil || createdName != intent.Intent.Identity.DataVolume {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err = driver.inspect(ctx, intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindStageDataVolumeBindingFromObservation(intent, observed)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, observed, stage.Network.ID,
		*stage.ConfigVolume, &binding) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := m.attestGatewayRebindStageDataVolumeLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, stage.Network.ID, &binding, 4); err != nil {
		return err
	}
	record, err := newGatewayRebindStageDataVolumeProgress(intent, history.Progress[3].Record,
		binding, occurredAt)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if progressStore.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageDataVolumeLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *record.Stage, stage.Network.ID, record.Stage.DataVolume, 5)
}

func (m *Manager) attestGatewayRebindStageDataVolumeLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageDataVolumeDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, networkID string,
	binding *gatewayRebindStageDataVolumeBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageDataVolumeAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, networkID, binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageDataVolumeAttestation(ctx, repository, reads, inspectDocker,
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

func (m *Manager) readGatewayRebindStageDataVolumeAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageDataVolumeDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, networkID string,
	binding *gatewayRebindStageDataVolumeBinding, progressCount uint64,
) (gatewayRebindStageDataVolumeAttestation, error) {
	anchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !reflect.DeepEqual(anchor.intent, intent) || anchor.progressCount != progressCount ||
		stage.NetworkTopologyDigest != anchor.intent.NetworkObservationDigest || stage.Network == nil ||
		stage.Network.ID != networkID {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	networkOwnership, ownershipErr := gatewayRebindStageNetworkOwnershipDigest(intent)
	if ownershipErr != nil || stage.Network.OwnershipDigest != networkOwnership {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) ||
		history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	docker, err := inspectDocker(ctx, anchor.source, anchor.predecessor.State, anchor.predecessor.Journal)
	defer clearGatewayV2DockerObservation(&docker)
	if err != nil || !validGatewayRebindPredecessorDocker(anchor.source, anchor.predecessor.State,
		anchor.predecessor.Journal, docker) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerDigest, err := canonicalDigest(docker)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if err := attestGatewayRebindStageDataVolumeImageOnce(ctx, driver, intent, stage); err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, err
	}
	if stage.ConfigVolume == nil || (stage.DataVolume != nil &&
		(binding == nil || *stage.DataVolume != *binding)) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	current, err := readGatewayRebindStageDataVolumePhysicalObservation(ctx, reads, driver,
		intent, networkID, *stage.ConfigVolume, binding)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, err
	}
	current.Anchor = anchor
	current.DockerDigest = dockerDigest
	return current, nil
}

func attestGatewayRebindStageDataVolumeImageOnce(ctx context.Context,
	driver gatewayRebindStageDataVolumeDriver, intent gatewayRebindProtectedIntent,
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

func readGatewayRebindStageDataVolumePhysicalObservation(ctx context.Context,
	reads gatewayRebindSuccessorPreflightReads, driver gatewayRebindStageDataVolumeDriver,
	intent gatewayRebindProtectedIntent, networkID string, config gatewayRebindStageConfigVolumeBinding,
	binding *gatewayRebindStageDataVolumeBinding,
) (gatewayRebindStageDataVolumeAttestation, error) {
	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	prefixes, err := reads.network.docker(ctx)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, observed, networkID, config, binding) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedIDs = append(expectedIDs, networkID)
	sort.Strings(expectedIDs)
	if !equalStrings(idsBefore, expectedIDs) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	currentCandidates := gatewayRebindCandidateProjection(candidates)
	hostRoutes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	hostInterfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(prefixes)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedPrefixes = append(expectedPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedPrefixes)
	if !validGatewayRebindStageNetworkHostDelta(intent, currentCandidates, hostRoutes, hostInterfaces) ||
		!equalStrings(dockerPrefixes, expectedPrefixes) {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	networkOwnership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageDataVolumeAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	var copiedBinding *gatewayRebindStageDataVolumeBinding
	if binding != nil {
		value := *binding
		copiedBinding = &value
	}
	return gatewayRebindStageDataVolumeAttestation{
		Candidates: currentCandidates, HostRoutes: hostRoutes, HostInterfaces: hostInterfaces,
		DockerIDs: append([]string{}, idsBefore...), DockerPrefixes: dockerPrefixes,
		NetworkID: networkID, NetworkOwnershipDigest: networkOwnership, ConfigVolume: config,
		DataVolume: copiedBinding,
	}, nil
}

var _ gatewayRebindStageDataVolumeDriver = managerGatewayRebindStageDataVolumeDriver{}
