package generatedingress

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayRebindStageServingVersion = 1

type gatewayRebindStageStartDriver interface {
	gatewayRebindStageStartIntentDriver
	start(context.Context, string) error
	stop(context.Context, string) error
	liveConfig(context.Context, string) ([]byte, error)
	hostProbe(context.Context, string, uint16, string, string) gatewayV2HostProbeResult
	containerProbe(context.Context, string, string, uint16, string, string) bool
}

type managerGatewayRebindStageStartDriver struct{ manager *Manager }

func (d managerGatewayRebindStageStartDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	return (managerGatewayRebindStageStartIntentDriver{manager: d.manager}).inspect(ctx, intent)
}

func (d managerGatewayRebindStageStartDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return (managerGatewayRebindStageStartIntentDriver{manager: d.manager}).inspectImage(ctx)
}

func (d managerGatewayRebindStageStartDriver) configVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	return (managerGatewayRebindStageStartIntentDriver{manager: d.manager}).configVolumeInventory(ctx, intent, stage, expected)
}

func (d managerGatewayRebindStageStartDriver) start(ctx context.Context, id string) error {
	if !validContainerID(id) || normalizeID(id) != id {
		return errors.New("invalid generated ingress rebind stage start identity")
	}
	return d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "start", id)
}

func (d managerGatewayRebindStageStartDriver) stop(ctx context.Context, id string) error {
	if !validContainerID(id) || normalizeID(id) != id {
		return errors.New("invalid generated ingress rebind stage stop identity")
	}
	return d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, "container", "stop", "--time", "10", id)
}

func (d managerGatewayRebindStageStartDriver) liveConfig(ctx context.Context, id string) ([]byte, error) {
	return d.manager.inspectLiveCaddyConfig(ctx, id)
}

func (d managerGatewayRebindStageStartDriver) hostProbe(ctx context.Context, address string,
	port uint16, host, path string,
) gatewayV2HostProbeResult {
	return probeGatewayV2HostStatus(ctx, address, port, host, path)
}

func (d managerGatewayRebindStageStartDriver) containerProbe(ctx context.Context, id, address string,
	port uint16, host, challenge string,
) bool {
	return d.manager.probeGatewayV2ContainerChallenge(ctx, id, address, port, host, challenge)
}

func gatewayRebindStageEffectivePortBindingsDigest(binding gatewayRebindStageStartIntentBinding) (string, error) {
	if !validGatewayRebindStageStartIntentBindingValue(binding) {
		return "", errors.New("invalid generated ingress rebind stage port binding input")
	}
	values := make(map[string][]map[string]string, int(binding.PortEnd-binding.PortStart)+1)
	for port := binding.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		values[value+"/tcp"] = []map[string]string{{"HostIp": binding.SelectedIPv4, "HostPort": value}}
		if port == binding.PortEnd {
			break
		}
	}
	return canonicalDigest(values)
}

func gatewayRebindStageServingBindingFor(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, endpointID string,
) (gatewayRebindStageServingBinding, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Sequence != 9 || previous.Phase != gatewayRebindProgressStageStartIntent ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.StageConfigIntent == nil || previous.Stage.StageStartIntent == nil ||
		previous.Stage.StageServing != nil || !validContainerID(endpointID) || normalizeID(endpointID) != endpointID {
		return gatewayRebindStageServingBinding{}, errors.New("invalid generated ingress rebind stage serving binding input")
	}
	bindingDigest, err := gatewayRebindStageEffectivePortBindingsDigest(*previous.Stage.StageStartIntent)
	if err != nil {
		return gatewayRebindStageServingBinding{}, err
	}
	value := gatewayRebindStageServingBinding{
		Version: gatewayRebindStageServingVersion, StageStartIntent: *previous.Stage.StageStartIntent,
		EndpointID: endpointID, EffectivePortBindingsDigest: bindingDigest,
		ConfigDigest:          previous.Stage.StageConfigIntent.ContentDigest,
		ProtectedIntentDigest: intent.Digest, PriorProgressDigest: previous.Digest,
	}
	if !validGatewayRebindStageServingBindingValue(value) {
		return gatewayRebindStageServingBinding{}, errors.New("invalid generated ingress rebind stage serving binding input")
	}
	return value, nil
}

func validGatewayRebindStageServingBindingValue(value gatewayRebindStageServingBinding) bool {
	if value.Version != gatewayRebindStageServingVersion ||
		!validGatewayRebindStageStartIntentBindingValue(value.StageStartIntent) ||
		!validContainerID(value.EndpointID) || normalizeID(value.EndpointID) != value.EndpointID ||
		!validSHA256(value.EffectivePortBindingsDigest) || !validSHA256(value.ConfigDigest) ||
		!validSHA256(value.ProtectedIntentDigest) || !validSHA256(value.PriorProgressDigest) {
		return false
	}
	digest, err := gatewayRebindStageEffectivePortBindingsDigest(value.StageStartIntent)
	return err == nil && digest == value.EffectivePortBindingsDigest &&
		value.ConfigDigest == value.StageStartIntent.StageConfigCopy.StageConfigIntent.ContentDigest
}

func validGatewayRebindStageServingBinding(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindStageServingBinding,
) bool {
	expected, err := gatewayRebindStageServingBindingFor(intent, previous, value.EndpointID)
	return err == nil && validGatewayRebindStageServingBindingValue(value) && value == expected
}

type gatewayRebindStageServingAttestation struct {
	Anchor         gatewayRebindEffectBoundaryAnchor
	DockerDigest   string
	Candidates     []gatewayRebindSuccessorNetworkCandidate
	HostRoutes     []string
	HostInterfaces []string
	DockerIDs      []string
	DockerPrefixes []string
	EndpointID     string
	Binding        gatewayRebindStageServingBinding
}

type gatewayRebindStageRuntimeClass uint8

const (
	gatewayRebindStageRuntimeInvalid gatewayRebindStageRuntimeClass = iota
	gatewayRebindStageRuntimeStopped
	gatewayRebindStageRuntimeServing
)

func classifyGatewayRebindStageRuntime(intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	value gatewayRebindStageContainerObservation,
) gatewayRebindStageRuntimeClass {
	if stage.StageContainer == nil {
		return gatewayRebindStageRuntimeInvalid
	}
	if validGatewayRebindStageContainerObservation(intent, stage, value, stage.StageContainer) {
		return gatewayRebindStageRuntimeStopped
	}
	if validGatewayRebindRunningStageContainerObservation(intent, stage, value) {
		return gatewayRebindStageRuntimeServing
	}
	return gatewayRebindStageRuntimeInvalid
}

func validGatewayRebindRunningStageContainerObservation(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, value gatewayRebindStageContainerObservation,
) bool {
	if stage.Network == nil || stage.StageContainer == nil || !value.StageContainerFound || value.FinalContainerFound ||
		!value.StageContainer.Running || value.StageContainer.Restarting || value.StageRuntime.Paused ||
		value.StageRuntime.Dead || value.StageRuntime.RestartCount != 0 ||
		!gatewayV2EffectivePortBindingsMatchConfigured(value.StageRuntime.EffectivePortBindings,
			value.StageContainer.PortBindings) || len(value.StageRuntime.ConfiguredNetworks) != 1 ||
		len(value.StageContainer.Networks) != 1 || len(value.Network.Containers) != 1 {
		return false
	}
	configured, ok := value.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork]
	if !ok || normalizeID(configured.NetworkID) != stage.Network.ID || !validContainerID(configured.EndpointID) ||
		normalizeID(configured.EndpointID) != configured.EndpointID || configured.IPAddress != intent.Intent.Network.ContainerIPv4 ||
		configured.IPv6Gateway != "" || configured.GwPriority != caddyGatewayPriority || configured.IPAMConfig == nil ||
		configured.IPAMConfig.IPv4Address != intent.Intent.Network.ContainerIPv4 || configured.IPAMConfig.IPv6Address != "" {
		return false
	}
	attached := value.StageContainer.Networks[intent.Intent.Identity.IngressNetwork]
	if attached == nil || attached.IPAddress != intent.Intent.Network.ContainerIPv4 ||
		attached.GwPriority != caddyGatewayPriority || attached.IPv6Gateway != "" {
		return false
	}
	subnet, err := netip.ParsePrefix(intent.Intent.Network.Subnet)
	if err != nil {
		return false
	}
	for id, attachedContainer := range value.Network.Containers {
		prefix, prefixErr := netip.ParsePrefix(attachedContainer.IPv4Address)
		if prefixErr != nil || normalizeID(id) != stage.StageContainer.ID ||
			attachedContainer.Name != intent.Intent.Identity.StageContainer ||
			prefix.Addr().String() != intent.Intent.Network.ContainerIPv4 || prefix.Bits() != subnet.Bits() {
			return false
		}
	}
	// Reuse the stopped validator for every immutable container, volume and
	// network property after removing only the runtime fields that legitimately
	// differ while the exact stage is serving.
	stopped := value
	stopped.StageContainer.Running = false
	stopped.StageContainer.Networks = nil
	stopped.StageRuntime.EffectivePortBindings = map[string][]map[string]string{}
	configured.EndpointID = ""
	configured.IPAddress = ""
	stopped.StageRuntime.ConfiguredNetworks = map[string]gatewayV2ConfiguredNetwork{
		intent.Intent.Identity.IngressNetwork: configured,
	}
	stopped.Network.Containers = map[string]caddyNetworkContainerInspection{}
	return validGatewayRebindStageContainerObservation(intent, stage, stopped, stage.StageContainer)
}

func gatewayRebindStageObservationMayBeLive(value gatewayRebindStageContainerObservation) bool {
	return value.StageContainerFound && (value.StageContainer.Running || value.StageContainer.Restarting ||
		gatewayV2HasEffectivePortBinding(value.StageRuntime.EffectivePortBindings))
}

func gatewayRebindPredecessorDockerDigest(value gatewayV2DockerObservation,
	identity gatewayV2Identity,
) (string, error) {
	// The predecessor attestor has already proved the complete container. Docker
	// is free to return its two exact volume mounts in either order, so remove
	// only that independently validated set before hashing the outer observation.
	if !value.FinalContainerFound || !validGatewayV2Mounts(value.FinalContainer.Mounts, identity) {
		return "", errors.New("invalid generated ingress rebind predecessor mount projection")
	}
	value.FinalContainer.Mounts = nil
	return canonicalDigest(value)
}

func proveGatewayRebindStagePublication(ctx context.Context, binding gatewayRebindStageStartIntentBinding,
	containerID string, hostProbe gatewayV2HostStatusProbe, containerProbe gatewayV2ContainerChallengeProbe,
) bool {
	if ctx == nil || !validGatewayRebindStageStartIntentBindingValue(binding) ||
		!validContainerID(containerID) || hostProbe == nil || containerProbe == nil {
		return false
	}
	for port := binding.PortStart; ; port++ {
		challenge := gatewayV2PortChallenge(binding.ProbeToken, port)
		if !exactGatewayV2HostChallenge(ctx, hostProbe, binding.SelectedIPv4, port,
			binding.SelectedIPv4, challenge) ||
			!containerProbe(ctx, containerID, binding.ContainerIPv4, port,
				binding.SelectedIPv4, challenge) ||
			!exactGatewayV2HostStatus(ctx, hostProbe, binding.SelectedIPv4, port,
				binding.SelectedIPv4, http.StatusNotFound) ||
			!exactGatewayV2HostStatus(ctx, hostProbe, binding.SelectedIPv4, port,
				"wrong.invalid", http.StatusNotFound) ||
			gatewayV2LoopbackPublished(ctx, hostProbe, port, binding.SelectedIPv4) {
			return false
		}
		if port == binding.PortEnd {
			return true
		}
	}
}

// startGatewayRebindSuccessorStage starts only the sequence-nine-bound
// container. It appends sequence ten after exact runtime, config, network and
// publication proofs. It never changes a route, URL, SQLite claim, application
// database, or rebind fence.
func (m *Manager) startGatewayRebindSuccessorStage(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) error {
	return m.startGatewayRebindSuccessorStageWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageStartDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) startGatewayRebindSuccessorStageWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartDriver,
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
			resultErr = candidateMayBeLiveError()
		}
		if releaseErr := releaseEffects(); releaseErr != nil {
			resultErr = candidateMayBeLiveError()
		}
	}()

	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 9 || len(history.Progress) > 10 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		stage.StageContainer == nil || stage.StageConfigIntent == nil || stage.StageConfigCopy == nil ||
		stage.StageStartIntent == nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[8].Record
	if len(history.Progress) == 10 {
		if stage.StageServing == nil || m.attestGatewayRebindStageServingLocked(ctx, repository, reads,
			inspectDocker, driver, intent, *stage, *stage.StageServing, 10) != nil {
			return candidateMayBeLiveError()
		}
		return nil
	}
	if stage.StageServing != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	store, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 10)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err := driver.inspect(ctx, intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	class := classifyGatewayRebindStageRuntime(intent, *stage, observed)
	if class == gatewayRebindStageRuntimeInvalid {
		if gatewayRebindStageObservationMayBeLive(observed) {
			return candidateMayBeLiveError()
		}
		return gatewayRebindEffectBoundaryError(ctx)
	}

	startedThisCall := false
	if class == gatewayRebindStageRuntimeStopped {
		if err := m.attestGatewayRebindStageStartIntentLocked(ctx, repository, reads, inspectDocker,
			driver, intent, *stage, *stage.StageStartIntent, 9); err != nil {
			return err
		}
		if checkpoint != nil {
			checkpoint()
		}
		postCheckpoint, inspectErr := driver.inspect(ctx, intent)
		if inspectErr != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		switch classifyGatewayRebindStageRuntime(intent, *stage, postCheckpoint) {
		case gatewayRebindStageRuntimeServing:
			// A previous exact start may have reached Docker during the checkpoint.
			// Do not dispatch another start; adopt only through the full serving proof.
			class = gatewayRebindStageRuntimeServing
		case gatewayRebindStageRuntimeStopped:
			// The checkpoint models an arbitrary delay or process boundary. Repeat the
			// complete stopped-stage attestation so no protected, SQLite, predecessor,
			// image, config, host or Docker observation from before it authorizes start.
			if err := m.attestGatewayRebindStageStartIntentLocked(ctx, repository, reads, inspectDocker,
				driver, intent, *stage, *stage.StageStartIntent, 9); err != nil {
				return err
			}
			candidates, candidateErr := reads.network.candidates()
			endpoint, endpointOK := gatewayRebindStageStartIntentEndpoint(intent,
				gatewayRebindCandidateProjection(candidates))
			if candidateErr != nil || !endpointOK || endpoint.InterfaceID != stage.StageStartIntent.InterfaceID ||
				endpoint.IPv4 != stage.StageStartIntent.SelectedIPv4 || ctx.Err() != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			immediate, immediateErr := driver.inspect(ctx, intent)
			if immediateErr != nil {
				return gatewayRebindEffectBoundaryError(ctx)
			}
			switch classifyGatewayRebindStageRuntime(intent, *stage, immediate) {
			case gatewayRebindStageRuntimeStopped:
				startedThisCall = true
				_ = driver.start(ctx, stage.StageContainer.ID)
			case gatewayRebindStageRuntimeServing:
				class = gatewayRebindStageRuntimeServing
			default:
				if gatewayRebindStageObservationMayBeLive(immediate) {
					return candidateMayBeLiveError()
				}
				return gatewayRebindEffectBoundaryError(ctx)
			}
		default:
			if gatewayRebindStageObservationMayBeLive(postCheckpoint) {
				return candidateMayBeLiveError()
			}
			return gatewayRebindEffectBoundaryError(ctx)
		}
	}

	attestation, attestErr := m.attestGatewayRebindStageServingCandidateLocked(ctx, repository, reads,
		inspectDocker, driver, intent, *stage, 9)
	if attestErr != nil {
		if class == gatewayRebindStageRuntimeServing || startedThisCall {
			return m.compensateGatewayRebindStageStartLocked(ctx, repository, reads, inspectDocker,
				driver, intent, *stage, attestErr)
		}
		return attestErr
	}
	record, err := newGatewayRebindStageServingProgress(intent, previous, attestation.Binding, occurredAt)
	if err != nil {
		return m.compensateGatewayRebindStageStartLocked(ctx, repository, reads, inspectDocker,
			driver, intent, *stage, gatewayRebindEffectBoundaryError(ctx))
	}
	if err := store.installExact(ctx, record); err != nil {
		// The create-only write may have reached durable storage. Leave the exact
		// live stage untouched; a fresh Manager can scan and reattest either state.
		return candidateMayBeLiveError()
	}
	if err := m.attestGatewayRebindStageServingLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *record.Stage, attestation.Binding, 10); err != nil {
		return candidateMayBeLiveError()
	}
	return nil
}

func (m *Manager) attestGatewayRebindStageServingCandidateLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, progressCount uint64,
) (gatewayRebindStageServingAttestation, error) {
	first, err := m.readGatewayRebindStageServingAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, nil, progressCount)
	if err != nil {
		return gatewayRebindStageServingAttestation{}, err
	}
	second, err := m.readGatewayRebindStageServingAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, &first.Binding, progressCount)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return second, nil
}

func (m *Manager) attestGatewayRebindStageServingLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageServingBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageServingAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, &binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageServingAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, &binding, progressCount)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	final, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !gatewayRebindEffectBoundaryAnchorMatchesObservation(final,
		gatewayRebindEffectBoundaryObservation{database: first.Anchor.database,
			predecessor: first.Anchor.predecessor, intent: first.Anchor.intent,
			source: first.Anchor.source, databaseDigest: first.Anchor.databaseDigest,
			sourceDigest: first.Anchor.sourceDigest, progressCount: first.Anchor.progressCount,
			progressDigest: first.Anchor.progressDigest}) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (m *Manager) readGatewayRebindStageServingAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	expected *gatewayRebindStageServingBinding, progressCount uint64,
) (gatewayRebindStageServingAttestation, error) {
	if progressCount != 9 && progressCount != 10 {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	anchor, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !reflect.DeepEqual(anchor.intent, intent) || anchor.progressCount != progressCount ||
		stage.Network == nil || stage.StageContainer == nil || stage.StageConfigIntent == nil ||
		stage.StageStartIntent == nil || (progressCount == 9 && stage.StageServing != nil) ||
		(progressCount == 10 && (stage.StageServing == nil || expected == nil || *stage.StageServing != *expected)) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) ||
		history.Progress[len(history.Progress)-1].Record.Stage == nil ||
		!reflect.DeepEqual(*history.Progress[len(history.Progress)-1].Record.Stage, stage) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	docker, err := inspectDocker(ctx, anchor.source, anchor.predecessor.State, anchor.predecessor.Journal)
	defer clearGatewayV2DockerObservation(&docker)
	if err != nil || !validGatewayRebindPredecessorDocker(anchor.source, anchor.predecessor.State,
		anchor.predecessor.Journal, docker) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	dockerDigest, err := gatewayRebindPredecessorDockerDigest(docker, anchor.predecessor.State.Identity)
	if err != nil || attestGatewayRebindStageContainerImageOnce(ctx, driver, intent, stage) != nil {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	candidates, err := reads.network.candidates()
	if err != nil {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	host, err := reads.network.host()
	if err != nil {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsBefore, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsBefore) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	prefixes, err := reads.network.docker(ctx)
	if err != nil {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindRunningStageContainerObservation(intent, stage, observed) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	configured := observed.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork]
	binding, err := gatewayRebindStageServingBindingFor(intent, history.Progress[8].Record, configured.EndpointID)
	if err != nil || (expected != nil && binding != *expected) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	body, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(body)
	inventory, err := driver.configVolumeInventory(ctx, intent, stage, body)
	if err != nil || inventory != gatewayRebindStageConfigInventoryExact {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	live, err := driver.liveConfig(ctx, stage.StageContainer.ID)
	if err != nil || !sameCaddyConfig(body, live) {
		clear(live)
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	clear(live)
	if !proveGatewayRebindStagePublication(ctx, *stage.StageStartIntent, stage.StageContainer.ID,
		driver.hostProbe, driver.containerProbe) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	idsAfter, err := reads.dockerIDs(ctx)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(idsAfter) || !equalStrings(idsBefore, idsAfter) {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expectedIDs := append([]string{}, intent.NetworkObservation.DockerNetworkIDs...)
	expectedIDs = append(expectedIDs, stage.Network.ID)
	sort.Strings(expectedIDs)
	currentCandidates := gatewayRebindCandidateProjection(candidates)
	hostRoutes, routeErr := canonicalGatewayRebindPrefixes(host.Routes)
	hostInterfaces, interfaceErr := canonicalGatewayRebindPrefixes(host.Interfaces)
	dockerPrefixes, prefixErr := canonicalGatewayRebindPrefixes(prefixes)
	expectedPrefixes := append([]string{}, intent.NetworkObservation.DockerPrefixes...)
	expectedPrefixes = append(expectedPrefixes, intent.Intent.Network.Subnet)
	sort.Strings(expectedPrefixes)
	if routeErr != nil || interfaceErr != nil || prefixErr != nil || !equalStrings(idsBefore, expectedIDs) ||
		!validGatewayRebindStageNetworkHostDelta(intent, currentCandidates, hostRoutes, hostInterfaces) ||
		!equalStrings(dockerPrefixes, expectedPrefixes) || ctx.Err() != nil {
		return gatewayRebindStageServingAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindStageServingAttestation{
		Anchor: anchor, DockerDigest: dockerDigest, Candidates: currentCandidates,
		HostRoutes: hostRoutes, HostInterfaces: hostInterfaces, DockerIDs: append([]string{}, idsBefore...),
		DockerPrefixes: dockerPrefixes, EndpointID: configured.EndpointID, Binding: binding,
	}, nil
}

func (m *Manager) compensateGatewayRebindStageStartLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, original error,
) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	observed, err := driver.inspect(recoveryCtx, intent)
	if err != nil {
		return candidateMayBeLiveError()
	}
	switch classifyGatewayRebindStageRuntime(intent, stage, observed) {
	case gatewayRebindStageRuntimeServing:
		if err := driver.stop(recoveryCtx, stage.StageContainer.ID); err != nil {
			// Docker can report uncertainty after the stop reached the daemon. The
			// exact withdrawal attestation below remains authoritative.
		}
	case gatewayRebindStageRuntimeStopped:
	default:
		return candidateMayBeLiveError()
	}
	if err := m.attestGatewayRebindStageStartIntentLocked(recoveryCtx, repository, reads, inspectDocker,
		driver, intent, stage, *stage.StageStartIntent, 9); err != nil {
		return candidateMayBeLiveError()
	}
	if !proveGatewayRebindStagePublicationWithdrawn(recoveryCtx, *stage.StageStartIntent, driver.hostProbe) {
		return candidateMayBeLiveError()
	}
	if original == nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return original
}

func proveGatewayRebindStagePublicationWithdrawn(ctx context.Context,
	binding gatewayRebindStageStartIntentBinding, probe gatewayV2HostStatusProbe,
) bool {
	if ctx == nil || !validGatewayRebindStageStartIntentBindingValue(binding) || probe == nil {
		return false
	}
	const confirmations = 2
	for port := binding.PortStart; ; port++ {
		for confirmation := 0; confirmation < confirmations; confirmation++ {
			selected := probe(ctx, binding.SelectedIPv4, port, binding.SelectedIPv4, "/")
			loopback := probe(ctx, "127.0.0.1", port, binding.SelectedIPv4, "/")
			if ctx.Err() != nil || selected.Connected || loopback.Connected {
				return false
			}
		}
		if port == binding.PortEnd {
			return true
		}
	}
}

var _ gatewayRebindStageStartDriver = managerGatewayRebindStageStartDriver{}
