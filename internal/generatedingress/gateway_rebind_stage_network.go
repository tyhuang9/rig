package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindIntentDigestLabelKey = "io.rig.rebind-intent-digest"
	gatewayRebindGenerationLabelKey   = "io.rig.rebind-generation"
	gatewayRebindBridgeNameOptionKey  = "com.docker.network.bridge.name"
)

type gatewayRebindStageNetworkObservation struct {
	Network             caddyNetworkInspection
	ID                  string
	Found               bool
	ConfigVolumeFound   bool
	DataVolumeFound     bool
	StageContainerFound bool
	FinalContainerFound bool
	OwnedContainers     []string
	OwnedVolumes        []string
	OwnedNetworks       []string
}

type gatewayRebindStageNetworkDriver interface {
	inspect(context.Context, gatewayRebindProtectedIntent) (gatewayRebindStageNetworkObservation, error)
	inspectImage(context.Context) (imageInspection, bool, error)
	create(context.Context, gatewayRebindProtectedIntent) (string, error)
}

type managerGatewayRebindStageNetworkDriver struct{ manager *Manager }

func (d managerGatewayRebindStageNetworkDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageNetworkObservation, error) {
	network, id, found, err := d.manager.inspectNamedGatewayNetwork(ctx, intent.Intent.Identity.IngressNetwork)
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	_, _, configFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.ConfigVolume)
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	_, _, dataFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.DataVolume)
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	_, _, stageFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.StageContainer)
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	_, _, finalFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.FinalContainer)
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	containers, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "container", "ls", "--all")
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	volumes, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "volume", "ls")
	if err != nil {
		return gatewayRebindStageNetworkObservation{}, err
	}
	networks, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "network", "ls")
	return gatewayRebindStageNetworkObservation{
		Network: network, ID: normalizeID(id), Found: found,
		ConfigVolumeFound: configFound, DataVolumeFound: dataFound,
		StageContainerFound: stageFound, FinalContainerFound: finalFound,
		OwnedContainers: containers, OwnedVolumes: volumes, OwnedNetworks: networks,
	}, err
}

func (m *Manager) inspectGatewayRebindOwnedNames(ctx context.Context, intent gatewayRebindProtectedIntent,
	args ...string,
) ([]string, error) {
	if !validGatewayRebindProtectedIntent(intent) || len(args) < 2 {
		return nil, errors.New("invalid generated ingress rebind ownership inventory input")
	}
	format := "{{.Name}}"
	if args[0] == "container" {
		format = "{{.Names}}"
	}
	args = append(args,
		"--filter", "label="+gatewayV2ManagedLabelKey,
		"--filter", "label="+gatewayV2IdentityLabelKey+"="+gatewayRebindSuccessorIdentityVersion,
		"--filter", "label="+gatewayV2OperationLabelKey+"="+intent.OperationID,
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
			return nil, errors.New("generated ingress rebind ownership inventory is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("generated ingress rebind ownership inventory is duplicated")
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	return values, nil
}

func (d managerGatewayRebindStageNetworkDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return d.manager.inspectImage(ctx)
}

func (d managerGatewayRebindStageNetworkDriver) create(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (string, error) {
	args, err := gatewayRebindStageNetworkCreateArgs(intent)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	defer clearResult(&result)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(result.Stdout))
	if !validContainerID(id) || normalizeID(id) != id {
		return "", errors.New("generated ingress rebind network create returned an invalid identity")
	}
	return id, nil
}

func gatewayRebindStageResourceLabels(intent gatewayRebindProtectedIntent, managed, role string) map[string]string {
	return map[string]string{
		gatewayV2ManagedLabelKey:          managed,
		gatewayV2IdentityLabelKey:         gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey:        intent.OperationID,
		gatewayV2IdentityDigestLabelKey:   intent.Intent.Identity.Digest,
		gatewayV2PlanDigestLabelKey:       intent.Intent.NetworkDigest,
		gatewayV2ResourceRoleLabelKey:     role,
		gatewayRebindIntentDigestLabelKey: intent.Digest,
		gatewayRebindGenerationLabelKey:   strconv.FormatUint(intent.Generation, 10),
	}
}

func gatewayRebindStageNetworkOwnershipDigest(intent gatewayRebindProtectedIntent) (string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return "", errors.New("invalid generated ingress rebind network ownership input")
	}
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil {
		return "", err
	}
	return canonicalDigest(struct {
		Version int                                 `json:"version"`
		Name    string                              `json:"name"`
		Bridge  string                              `json:"bridge"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
		Labels  map[string]string                   `json:"labels"`
	}{
		Version: 1, Name: intent.Intent.Identity.IngressNetwork, Bridge: bridgeName, Plan: intent.Intent.Network,
		Labels: gatewayRebindStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole),
	})
}

func gatewayRebindStageBridgeName(intent gatewayRebindProtectedIntent) (string, error) {
	if !validGatewayRebindProtectedIntent(intent) || len(intent.Intent.Identity.Digest) < 12 {
		return "", errors.New("invalid generated ingress rebind bridge identity")
	}
	return "rig" + intent.Intent.Identity.Digest[:12], nil
}

func gatewayRebindStageNetworkCreateArgs(intent gatewayRebindProtectedIntent) ([]string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{"network", "create", "--driver", "bridge", "--subnet", intent.Intent.Network.Subnet,
		"--gateway", intent.Intent.Network.GatewayIPv4, "--opt", gatewayRebindBridgeNameOptionKey + "=" + bridgeName}
	args = appendGatewayV2Labels(args,
		gatewayRebindStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole))
	return append(args, intent.Intent.Identity.IngressNetwork), nil
}

func validGatewayRebindStageNetworkObservation(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageNetworkObservation, expectedID string,
) bool {
	if !validGatewayRebindProtectedIntent(intent) || !value.Found || !validContainerID(value.ID) ||
		normalizeID(value.ID) != value.ID || (expectedID != "" && value.ID != expectedID) {
		return false
	}
	if value.ConfigVolumeFound || value.DataVolumeFound || value.StageContainerFound || value.FinalContainerFound ||
		len(value.OwnedContainers) != 0 || len(value.OwnedVolumes) != 0 ||
		!validOwnedNameSet(value.OwnedNetworks, intent.Intent.Identity.IngressNetwork) {
		return false
	}
	network := value.Network
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil {
		return false
	}
	return network.Name == intent.Intent.Identity.IngressNetwork && network.Driver == "bridge" &&
		network.Scope == "local" && !network.Internal && reflect.DeepEqual(network.Options,
		map[string]string{gatewayRebindBridgeNameOptionKey: bridgeName}) &&
		len(network.IPAM.Config) == 1 && network.IPAM.Config[0] == (networkIPAM{
		Subnet: intent.Intent.Network.Subnet, Gateway: intent.Intent.Network.GatewayIPv4,
	}) && len(network.Containers) == 0 &&
		reflect.DeepEqual(network.Labels,
			gatewayRebindStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole))
}

func validGatewayRebindStageNetworkAbsentObservation(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageNetworkObservation,
) bool {
	return validGatewayRebindProtectedIntent(intent) && !value.Found && value.ID == "" &&
		reflect.DeepEqual(value.Network, caddyNetworkInspection{}) && !value.ConfigVolumeFound && !value.DataVolumeFound &&
		!value.StageContainerFound && !value.FinalContainerFound && len(value.OwnedContainers) == 0 &&
		len(value.OwnedVolumes) == 0 && len(value.OwnedNetworks) == 0
}

// stageGatewayRebindSuccessorNetwork is the first private rebind effect. It
// creates no volume or container, publishes no port, changes no route, and
// never touches the predecessor. The effects lease, Manager mutex, and gateway
// OS lock remain held through the protected readback and exact Docker census.
func (m *Manager) stageGatewayRebindSuccessorNetwork(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.stageGatewayRebindSuccessorNetworkWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageNetworkDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) stageGatewayRebindSuccessorNetworkWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageNetworkDriver,
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
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 2 || len(history.Progress) > 3 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 3 {
		bound := history.Progress[2].Record
		if bound.Stage == nil || bound.Stage.Network == nil ||
			m.attestGatewayRebindStageNetworkLocked(ctx, repository, reads, inspectDocker, driver,
				intent, *bound.Stage, bound.Stage.Network.ID, 3) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}

	observed, err := driver.inspect(ctx, intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if observed.Found || !validGatewayRebindStageNetworkAbsentObservation(intent, observed) {
		// Sequence two has no resource identity binding. An existing network,
		// including one left by a create-before-bind crash, cannot be adopted.
		return gatewayRebindEffectBoundaryError(ctx)
	}
	evidence, err := m.attestGatewayRebindPreparedEffectBoundaryLocked(ctx, repository, reads, inspectDocker, nil)
	if err != nil || evidence.ProgressCount != 2 || evidence.OperationID != intent.OperationID ||
		evidence.ProtectedIntentDigest != intent.Digest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := attestGatewayRebindStageImageLocked(ctx, driver, intent, *stage); err != nil {
		return err
	}
	if checkpoint != nil {
		checkpoint()
	}
	// The complete no-effect attestation is repeated immediately before the
	// mutation so the test hook cannot create a stale authorization window.
	secondEvidence, err := m.attestGatewayRebindPreparedEffectBoundaryLocked(ctx, repository, reads, inspectDocker, nil)
	if err != nil || secondEvidence != evidence {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := attestGatewayRebindStageImageLocked(ctx, driver, intent, *stage); err != nil {
		return err
	}
	observed, err = driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkAbsentObservation(intent, observed) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	createdID, createErr := driver.create(ctx, intent)
	if createErr != nil || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err = driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, observed, createdID) {
		return gatewayRebindEffectBoundaryError(ctx)
	}

	if err := m.attestGatewayRebindStageNetworkLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *stage, observed.ID, 2); err != nil {
		return err
	}
	ownershipDigest, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	record, err := newGatewayRebindStageNetworkProgress(intent, history.Progress[1].Record,
		observed.ID, ownershipDigest, occurredAt)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	store, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 3)
	if err != nil || store.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageNetworkLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *record.Stage, observed.ID, 3)
}

type gatewayRebindStageNetworkAttestation struct {
	Anchor          gatewayRebindEffectBoundaryAnchor
	DockerDigest    string
	Candidates      []gatewayRebindSuccessorNetworkCandidate
	HostRoutes      []string
	HostInterfaces  []string
	DockerIDs       []string
	DockerPrefixes  []string
	NetworkID       string
	OwnershipDigest string
}

func attestGatewayRebindStageImageLocked(ctx context.Context, driver gatewayRebindStageNetworkDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) error {
	first, err := inspectGatewayRebindStageImage(ctx, driver, intent, stage)
	if err != nil {
		return err
	}
	second, err := inspectGatewayRebindStageImage(ctx, driver, intent, stage)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func attestGatewayRebindStageImageOnce(ctx context.Context, driver gatewayRebindStageNetworkDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) error {
	_, err := inspectGatewayRebindStageImage(ctx, driver, intent, stage)
	return err
}

func inspectGatewayRebindStageImage(ctx context.Context, driver gatewayRebindStageNetworkDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) (imageInspection, error) {
	if ctx.Err() != nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest ||
		stage.ApprovedCaddyImageDigest != intent.Intent.Identity.CaddyImageDigest {
		return imageInspection{}, gatewayRebindEffectBoundaryError(ctx)
	}
	image, found, err := driver.inspectImage(ctx)
	if err != nil || ctx.Err() != nil || !validGatewayPinnedImage(image, found) ||
		normalizeID(image.ID) != stage.ObservedDockerImageID {
		return imageInspection{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return image, nil
}

func (m *Manager) attestGatewayRebindStageNetworkLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageNetworkDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, networkID string, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageNetworkAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, networkID, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageNetworkAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, networkID, progressCount)
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

func (m *Manager) readGatewayRebindStageNetworkAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageNetworkDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, networkID string, progressCount uint64,
) (gatewayRebindStageNetworkAttestation, error) {
	anchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !reflect.DeepEqual(anchor.intent, intent) || anchor.progressCount != progressCount ||
		stage.NetworkTopologyDigest != anchor.intent.NetworkObservationDigest {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) ||
		history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	docker, err := inspectDocker(ctx, anchor.source, anchor.predecessor.State, anchor.predecessor.Journal)
	defer clearGatewayV2DockerObservation(&docker)
	if err != nil || !validGatewayRebindPredecessorDocker(anchor.source, anchor.predecessor.State,
		anchor.predecessor.Journal, docker) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerDigest, err := canonicalDigest(docker)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if err := attestGatewayRebindStageImageOnce(ctx, driver, intent, stage); err != nil {
		return gatewayRebindStageNetworkAttestation{}, err
	}
	current, err := readGatewayRebindStageNetworkPhysicalObservation(ctx, reads, driver, intent, networkID)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, err
	}
	current.Anchor = anchor
	current.DockerDigest = dockerDigest
	return current, nil
}

func readGatewayRebindStageNetworkPhysicalObservation(ctx context.Context,
	reads gatewayRebindSuccessorPreflightReads, driver gatewayRebindStageNetworkDriver,
	intent gatewayRebindProtectedIntent, networkID string,
) (gatewayRebindStageNetworkAttestation, error) {
	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	prefixes, err := reads.network.docker(ctx)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, observed, networkID) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedIDs = append(expectedIDs, networkID)
	sort.Strings(expectedIDs)
	if !equalStrings(idsBefore, expectedIDs) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	currentCandidates := gatewayRebindCandidateProjection(candidates)
	hostRoutes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	hostInterfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(prefixes)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedPrefixes = append(expectedPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedPrefixes)
	if !validGatewayRebindStageNetworkHostDelta(intent, currentCandidates, hostRoutes, hostInterfaces) ||
		!equalStrings(dockerPrefixes, expectedPrefixes) {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	ownershipDigest, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageNetworkAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindStageNetworkAttestation{
		Candidates: currentCandidates, HostRoutes: hostRoutes, HostInterfaces: hostInterfaces,
		DockerIDs: append([]string{}, idsBefore...), DockerPrefixes: dockerPrefixes,
		NetworkID: networkID, OwnershipDigest: ownershipDigest,
	}, nil
}

func validGatewayRebindStageNetworkHostDelta(intent gatewayRebindProtectedIntent,
	candidates []gatewayRebindSuccessorNetworkCandidate, routes, interfaces []string,
) bool {
	if !validGatewayRebindProtectedIntent(intent) ||
		!gatewayRebindPrefixesMatchBaselineOrPlan(routes, intent.NetworkObservation.HostRoutes,
			intent.Intent.Network.Subnet) ||
		!gatewayRebindPrefixesMatchBaselineOrPlan(interfaces, intent.NetworkObservation.HostInterfaces,
			intent.Intent.Network.Subnet) {
		return false
	}
	if reflect.DeepEqual(candidates, intent.NetworkObservation.Candidates) {
		return true
	}
	if len(candidates) != len(intent.NetworkObservation.Candidates)+1 {
		return false
	}
	bridgeName, err := gatewayRebindStageBridgeName(intent)
	if err != nil {
		return false
	}
	withoutBridge := make([]gatewayRebindSuccessorNetworkCandidate, 0, len(candidates)-1)
	foundBridge := false
	for _, candidate := range candidates {
		index, name, ok := strings.Cut(candidate.InterfaceID, "/")
		parsedIndex, parseErr := strconv.Atoi(index)
		isBridge := ok && parseErr == nil && parsedIndex > 0 && strconv.Itoa(parsedIndex) == index &&
			name == bridgeName && candidate.IPv4 == intent.Intent.Network.GatewayIPv4 &&
			candidate.Prefix == intent.Intent.Network.Subnet
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

func gatewayRebindPrefixesMatchBaselineOrPlan(values, baseline []string, plan string) bool {
	if equalStrings(values, baseline) {
		return true
	}
	expected := append([]string{}, baseline...)
	expected = append(expected, plan)
	sort.Strings(expected)
	return equalStrings(values, expected)
}

func gatewayRebindCandidateProjection(values []hostNetworkCandidate) []gatewayRebindSuccessorNetworkCandidate {
	result := make([]gatewayRebindSuccessorNetworkCandidate, 0, len(values))
	for _, value := range values {
		result = append(result, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: value.InterfaceID, IPv4: value.IPv4, Prefix: value.Prefix.String(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return gatewayRebindCandidateLess(result[i], result[j]) })
	return result
}

var _ gatewayRebindStageNetworkDriver = managerGatewayRebindStageNetworkDriver{}
