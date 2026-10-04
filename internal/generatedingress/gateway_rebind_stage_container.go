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

type gatewayRebindStageContainerObservation struct {
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

type gatewayRebindStageContainerDriver interface {
	gatewayRebindStageContainerAttestor
	create(context.Context, gatewayRebindProtectedIntent, gatewayRebindStageIntent) (string, error)
}

type gatewayRebindStageContainerAttestor interface {
	inspect(context.Context, gatewayRebindProtectedIntent) (gatewayRebindStageContainerObservation, error)
	inspectImage(context.Context) (imageInspection, bool, error)
}

type managerGatewayRebindStageContainerDriver struct{ manager *Manager }

func (d managerGatewayRebindStageContainerDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	network, networkID, networkFound, err := d.manager.inspectNamedGatewayNetwork(ctx, intent.Intent.Identity.IngressNetwork)
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	config, configIdentity, configFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.ConfigVolume)
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	data, dataIdentity, dataFound, err := d.manager.inspectNamedVolumeWithIdentity(ctx, intent.Intent.Identity.DataVolume)
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	stage, runtime, stageFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.StageContainer)
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	_, _, finalFound, err := d.manager.inspectNamedGatewayContainer(ctx, intent.Intent.Identity.FinalContainer)
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	containers, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "container", "ls", "--all")
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	volumes, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "volume", "ls")
	if err != nil {
		return gatewayRebindStageContainerObservation{}, err
	}
	networks, err := d.manager.inspectGatewayRebindOwnedNames(ctx, intent, "network", "ls")
	return gatewayRebindStageContainerObservation{
		Network: network, NetworkID: normalizeID(networkID), NetworkFound: networkFound,
		ConfigVolume: config, ConfigVolumeIdentity: configIdentity, ConfigVolumeFound: configFound,
		DataVolume: data, DataVolumeIdentity: dataIdentity, DataVolumeFound: dataFound,
		StageContainer: stage, StageRuntime: runtime, StageContainerFound: stageFound,
		FinalContainerFound: finalFound, OwnedContainers: containers, OwnedVolumes: volumes,
		OwnedNetworks: networks,
	}, err
}

func (d managerGatewayRebindStageContainerDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return d.manager.inspectImage(ctx)
}

func (d managerGatewayRebindStageContainerDriver) create(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) (string, error) {
	args, err := gatewayRebindStageContainerCreateArgs(intent, stage)
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
		return "", errors.New("generated ingress rebind stage container create returned an invalid identity")
	}
	return id, nil
}

func gatewayRebindStageContainerCreateArgs(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent,
) ([]string, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindStageIntent(stage) ||
		stage.Identity != intent.Intent.Identity || stage.NetworkPlan != intent.Intent.Network ||
		stage.NetworkPlanDigest != intent.Intent.NetworkDigest ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest || stage.Network == nil ||
		stage.ConfigVolume == nil || stage.DataVolume == nil || stage.StageContainer != nil ||
		!validGatewayRebindStageConfigVolumeBinding(intent, *stage.ConfigVolume) ||
		!validGatewayRebindStageDataVolumeBinding(intent, *stage.DataVolume) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{
		"container", "create", "--name", intent.Intent.Identity.StageContainer,
		"--hostname", intent.Intent.Identity.StageHostname,
		"--network", "name=" + intent.Intent.Identity.IngressNetwork + ",ip=" + intent.Intent.Network.ContainerIPv4 + ",gw-priority=1",
		"--mount", "type=volume,src=" + intent.Intent.Identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + intent.Intent.Identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", gatewayV2StageRestartPolicy,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for port := intent.Intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", intent.Intent.SuccessorProfile.SelectedIPv4+":"+value+":"+value+"/tcp")
		if port == intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
	args = appendGatewayV2Labels(args, gatewayRebindStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole))
	return append(args, "sha256:"+stage.ObservedDockerImageID, "run", "--config",
		"/config/"+intent.Intent.Identity.StageConfigFilename), nil
}

func gatewayRebindStageContainerOwnershipDigest(intent gatewayRebindProtectedIntent) (string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return "", errors.New("invalid generated ingress rebind stage container ownership input")
	}
	return canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Labels  map[string]string `json:"labels"`
	}{1, intent.Intent.Identity.StageContainer, gatewayRebindStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole)})
}

func gatewayRebindStageContainerConfigurationDigest(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent,
) (string, error) {
	stage.StageContainer = nil
	stage.StageConfigIntent = nil
	stage.StageConfigCopy = nil
	args, err := gatewayRebindStageContainerCreateArgs(intent, stage)
	if err != nil {
		return "", errors.New("invalid generated ingress rebind stage container configuration input")
	}
	return canonicalDigest(struct {
		Version int      `json:"version"`
		Args    []string `json:"args"`
	}{1, args})
}

func validGatewayRebindStageContainerBinding(intent gatewayRebindProtectedIntent,
	value gatewayRebindStageContainerBinding,
) bool {
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	return err == nil && validGatewayRebindStageContainerBindingValue(value) &&
		value.OwnershipDigest == ownership
}

func gatewayRebindStageContainerBindingFromObservation(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, value gatewayRebindStageContainerObservation,
) (gatewayRebindStageContainerBinding, error) {
	ownership, err := gatewayRebindStageContainerOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageContainerBinding{}, err
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, stage)
	if err != nil {
		return gatewayRebindStageContainerBinding{}, err
	}
	binding := gatewayRebindStageContainerBinding{
		ID: normalizeID(value.StageContainer.ID), OwnershipDigest: ownership,
		ConfigurationDigest: configuration,
	}
	if !validGatewayRebindStageContainerBinding(intent, binding) {
		return gatewayRebindStageContainerBinding{}, errors.New("invalid generated ingress rebind stage container identity")
	}
	return binding, nil
}

func validGatewayRebindStageContainerObservation(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, value gatewayRebindStageContainerObservation,
	expected *gatewayRebindStageContainerBinding,
) bool {
	if stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		value.FinalContainerFound {
		return false
	}
	volumes := gatewayRebindStageDataVolumeObservation{
		Network: value.Network, NetworkID: value.NetworkID, NetworkFound: value.NetworkFound,
		ConfigVolume: value.ConfigVolume, ConfigVolumeIdentity: value.ConfigVolumeIdentity,
		ConfigVolumeFound: value.ConfigVolumeFound, DataVolume: value.DataVolume,
		DataVolumeIdentity: value.DataVolumeIdentity, DataVolumeFound: value.DataVolumeFound,
		OwnedVolumes: value.OwnedVolumes, OwnedNetworks: value.OwnedNetworks,
	}
	if !validGatewayRebindStageDataVolumeObservation(intent, volumes, stage.Network.ID,
		*stage.ConfigVolume, stage.DataVolume) {
		return false
	}
	if expected == nil {
		return stage.StageContainer == nil && !value.StageContainerFound && reflect.DeepEqual(value.StageContainer, caddyInspection{}) &&
			reflect.DeepEqual(value.StageRuntime, gatewayContainerRuntime{}) && len(value.OwnedContainers) == 0 &&
			len(value.Network.Containers) == 0
	}
	configuration, err := gatewayRebindStageContainerConfigurationDigest(intent, stage)
	if err != nil || !validGatewayRebindStageContainerBinding(intent, *expected) ||
		expected.ConfigurationDigest != configuration || !value.StageContainerFound ||
		!validOwnedNameSet(value.OwnedContainers, intent.Intent.Identity.StageContainer) {
		return false
	}
	if stage.StageContainer != nil && *stage.StageContainer != *expected {
		return false
	}
	return validGatewayRebindStoppedStageContainer(intent, stage, value.StageContainer,
		value.StageRuntime, *expected) && len(value.Network.Containers) == 0
}

func validGatewayRebindStoppedStageContainer(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, value caddyInspection, runtime gatewayContainerRuntime,
	binding gatewayRebindStageContainerBinding,
) bool {
	if value.Running || value.Restarting || runtime.Paused || runtime.Dead || runtime.RestartCount != 0 ||
		normalizeID(value.ID) != binding.ID || normalizeID(value.Image) != stage.ObservedDockerImageID ||
		strings.TrimPrefix(value.Name, "/") != intent.Intent.Identity.StageContainer ||
		value.Hostname != intent.Intent.Identity.StageHostname || value.User != "1000:1000" ||
		value.NetworkMode != intent.Intent.Identity.IngressNetwork || !exactGatewayV2Environment(value.Env) ||
		!value.ReadOnly || value.Privileged || !onlyCaddyCapability(value.CapAdd) ||
		!exactFoldSet(value.CapDrop, "ALL") || !onlyNoNewPrivileges(value.SecurityOpt) ||
		len(value.Binds) != 0 || len(value.Tmpfs) != 0 || value.Memory != 268435456 ||
		value.MemorySwap != 268435456 || value.NanoCPUs != 1_000_000_000 || value.PIDsLimit != 128 ||
		value.LogType != "local" || len(value.LogConfig) != 2 || value.LogConfig["max-size"] != "10m" ||
		value.LogConfig["max-file"] != "3" || value.Restart != gatewayV2StageRestartPolicy ||
		len(value.Entrypoint) != 1 || value.Entrypoint[0] != caddyExecutable || len(value.Cmd) != 3 ||
		value.Cmd[0] != "run" || value.Cmd[1] != "--config" ||
		value.Cmd[2] != "/config/"+intent.Intent.Identity.StageConfigFilename ||
		len(value.Ulimits) != 1 || value.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!validGatewayV2ContainerLabels(value.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole)) ||
		!validGatewayRebindStageContainerMounts(value.Mounts, intent.Intent.Identity) ||
		!validGatewayRebindStageContainerPortBindings(value.PortBindings, intent) ||
		gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings) {
		return false
	}
	if len(runtime.ConfiguredNetworks) != 1 {
		return false
	}
	network, ok := runtime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork]
	if !ok || (network.NetworkID != "" && normalizeID(network.NetworkID) != stage.Network.ID) ||
		network.EndpointID != "" || network.IPAddress != "" || network.IPv6Gateway != "" ||
		network.GwPriority != caddyGatewayPriority || network.IPAMConfig == nil ||
		network.IPAMConfig.IPv4Address != intent.Intent.Network.ContainerIPv4 ||
		network.IPAMConfig.IPv6Address != "" {
		return false
	}
	return true
}

func validGatewayRebindStageContainerMounts(actual []mountInspection,
	identity gatewayRebindSuccessorIdentity,
) bool {
	if len(actual) != 2 {
		return false
	}
	wanted := map[string]string{identity.ConfigVolume: "/config", identity.DataVolume: "/data"}
	for _, mount := range actual {
		destination, ok := wanted[mount.Name]
		if !ok || mount.Type != "volume" || mount.Destination != destination || !mount.RW {
			return false
		}
		delete(wanted, mount.Name)
	}
	return len(wanted) == 0
}

func validGatewayRebindStageContainerPortBindings(actual map[string][]map[string]string,
	intent gatewayRebindProtectedIntent,
) bool {
	if len(actual) != int(intent.Intent.SuccessorProfile.PortEnd-intent.Intent.SuccessorProfile.PortStart)+1 {
		return false
	}
	for port := intent.Intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		binding := actual[value+"/tcp"]
		if len(binding) != 1 || len(binding[0]) != 2 ||
			binding[0]["HostIp"] != intent.Intent.SuccessorProfile.SelectedIPv4 ||
			binding[0]["HostPort"] != value {
			return false
		}
		if port == intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
	return true
}

type gatewayRebindStageContainerAttestation struct {
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
	DataVolume             gatewayRebindStageDataVolumeBinding
	StageContainer         *gatewayRebindStageContainerBinding
}

// stageGatewayRebindSuccessorContainer is the fourth private rebind effect.
// It creates only a stopped generation-scoped stage container after all exact
// network and volume receipts have been reattested. It never starts a
// container, copies config, publishes a listener, changes a route, or writes SQLite.
func (m *Manager) stageGatewayRebindSuccessorContainer(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.stageGatewayRebindSuccessorContainerWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageContainerDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) stageGatewayRebindSuccessorContainerWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageContainerDriver,
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
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 5 || len(history.Progress) > 6 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 6 {
		if stage.StageContainer == nil || m.attestGatewayRebindStageContainerLocked(ctx, repository, reads,
			inspectDocker, driver, intent, *stage, stage.StageContainer, 6) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.StageContainer != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(history.Progress[4].Record.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if _, err := gatewayRebindStageContainerOwnershipDigest(intent); err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if _, err := gatewayRebindStageContainerConfigurationDigest(intent, *stage); err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 6)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}

	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, *stage, observed, nil) {
		// Sequence five has no container identity binding. An existing container,
		// including one left by a create-before-bind crash, cannot be adopted.
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, nil, 5)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, nil, 5)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, nil, 5)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, nil, 5)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	createdID, createErr := driver.create(ctx, intent, *stage)
	if createErr != nil || ctx.Err() != nil || !validContainerID(createdID) || normalizeID(createdID) != createdID {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err = driver.inspect(ctx, intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindStageContainerBindingFromObservation(intent, *stage, observed)
	if err != nil || binding.ID != createdID ||
		!validGatewayRebindStageContainerObservation(intent, *stage, observed, &binding) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := m.attestGatewayRebindStageContainerLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, &binding, 5); err != nil {
		return err
	}
	record, err := newGatewayRebindStageContainerProgress(intent, history.Progress[4].Record, binding, occurredAt)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if progressStore.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageContainerLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *record.Stage, record.Stage.StageContainer, 6)
}

func (m *Manager) attestGatewayRebindStageContainerLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageContainerAttestor,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding *gatewayRebindStageContainerBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
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

func (m *Manager) readGatewayRebindStageContainerAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageContainerAttestor,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding *gatewayRebindStageContainerBinding, progressCount uint64,
) (gatewayRebindStageContainerAttestation, error) {
	anchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !reflect.DeepEqual(anchor.intent, intent) || anchor.progressCount != progressCount ||
		stage.NetworkTopologyDigest != anchor.intent.NetworkObservationDigest || stage.Network == nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	networkOwnership, ownershipErr := gatewayRebindStageNetworkOwnershipDigest(intent)
	if ownershipErr != nil || stage.Network.OwnershipDigest != networkOwnership {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) ||
		history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	docker, err := inspectDocker(ctx, anchor.source, anchor.predecessor.State, anchor.predecessor.Journal)
	defer clearGatewayV2DockerObservation(&docker)
	if err != nil || !validGatewayRebindPredecessorDocker(anchor.source, anchor.predecessor.State,
		anchor.predecessor.Journal, docker) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerDigest, err := canonicalDigest(docker)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if err := attestGatewayRebindStageContainerImageOnce(ctx, driver, intent, stage); err != nil {
		return gatewayRebindStageContainerAttestation{}, err
	}
	if stage.ConfigVolume == nil || stage.DataVolume == nil ||
		(stage.StageContainer != nil && (binding == nil || *stage.StageContainer != *binding)) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	current, err := readGatewayRebindStageContainerPhysicalObservation(ctx, reads, driver,
		intent, stage, binding)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, err
	}
	current.Anchor = anchor
	current.DockerDigest = dockerDigest
	return current, nil
}

func attestGatewayRebindStageContainerImageOnce(ctx context.Context,
	driver gatewayRebindStageContainerAttestor, intent gatewayRebindProtectedIntent,
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

func readGatewayRebindStageContainerPhysicalObservation(ctx context.Context,
	reads gatewayRebindSuccessorPreflightReads, driver gatewayRebindStageContainerAttestor,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding *gatewayRebindStageContainerBinding,
) (gatewayRebindStageContainerAttestation, error) {
	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	prefixes, err := reads.network.docker(ctx)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, stage, observed, binding) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedIDs = append(expectedIDs, stage.Network.ID)
	sort.Strings(expectedIDs)
	if !equalStrings(idsBefore, expectedIDs) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	currentCandidates := gatewayRebindCandidateProjection(candidates)
	hostRoutes, err := canonicalGatewayRebindPrefixes(host.Routes)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	hostInterfaces, err := canonicalGatewayRebindPrefixes(host.Interfaces)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(prefixes)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedPrefixes = append(expectedPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedPrefixes)
	if !validGatewayRebindStageNetworkHostDelta(intent, currentCandidates, hostRoutes, hostInterfaces) ||
		!equalStrings(dockerPrefixes, expectedPrefixes) {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	networkOwnership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
	if err != nil {
		return gatewayRebindStageContainerAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	var copiedBinding *gatewayRebindStageContainerBinding
	if binding != nil {
		value := *binding
		copiedBinding = &value
	}
	return gatewayRebindStageContainerAttestation{
		Candidates: currentCandidates, HostRoutes: hostRoutes, HostInterfaces: hostInterfaces,
		DockerIDs: append([]string{}, idsBefore...), DockerPrefixes: dockerPrefixes,
		NetworkID: stage.Network.ID, NetworkOwnershipDigest: networkOwnership,
		ConfigVolume: *stage.ConfigVolume, DataVolume: *stage.DataVolume,
		StageContainer: copiedBinding,
	}, nil
}

var _ gatewayRebindStageContainerDriver = managerGatewayRebindStageContainerDriver{}
