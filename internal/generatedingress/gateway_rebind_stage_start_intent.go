package generatedingress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindStageStartIntentVersion = 1
	gatewayRebindStageStartEffectContext = "hostd/generated-ingress/rebind/stage-start-effect/v1"
)

// gatewayRebindStageStartIntentDriver exposes only read operations. The later
// start receipt owns the Docker start operation; this preparation slice has no
// write, route, or SQLite transition capability.
type gatewayRebindStageStartIntentDriver interface {
	gatewayRebindStageContainerAttestor
	configVolumeInventory(context.Context, gatewayRebindProtectedIntent, gatewayRebindStageIntent,
		[]byte) (gatewayRebindStageConfigInventory, error)
}

type managerGatewayRebindStageStartIntentDriver struct{ manager *Manager }

func (d managerGatewayRebindStageStartIntentDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	return (managerGatewayRebindStageConfigCopyDriver{manager: d.manager}).inspect(ctx, intent)
}

func (d managerGatewayRebindStageStartIntentDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return (managerGatewayRebindStageConfigCopyDriver{manager: d.manager}).inspectImage(ctx)
}

func (d managerGatewayRebindStageStartIntentDriver) configVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	if stage.StageStartIntent != nil {
		return d.startedStageConfigVolumeInventory(ctx, intent, stage, expected)
	}
	return (managerGatewayRebindStageConfigCopyDriver{manager: d.manager}).configVolumeInventory(ctx, intent, stage, expected)
}

func gatewayRebindStageStartIntentEndpoint(intent gatewayRebindProtectedIntent,
	values []gatewayRebindSuccessorNetworkCandidate,
) (gatewayRebindSuccessorNetworkCandidate, bool) {
	if !validGatewayRebindProtectedIntent(intent) {
		return gatewayRebindSuccessorNetworkCandidate{}, false
	}
	var selected gatewayRebindSuccessorNetworkCandidate
	for _, value := range values {
		if value.InterfaceID != intent.Intent.SuccessorProfile.InterfaceID ||
			value.IPv4 != intent.Intent.SuccessorProfile.SelectedIPv4 {
			continue
		}
		prefix, prefixErr := netip.ParsePrefix(value.Prefix)
		address, addressErr := netip.ParseAddr(value.IPv4)
		if prefixErr != nil || addressErr != nil || !address.Is4() || prefix != prefix.Masked() ||
			prefix.String() != value.Prefix || !prefix.Contains(address) || selected.InterfaceID != "" {
			return gatewayRebindSuccessorNetworkCandidate{}, false
		}
		selected = value
	}
	return selected, selected.InterfaceID != ""
}

func gatewayRebindStageStartEffectDigest(value gatewayRebindStageStartIntentBinding) (string, error) {
	if !validGatewayRebindStageStartIntentBindingFields(value, false) {
		return "", errors.New("invalid generated ingress rebind stage start effect")
	}
	return canonicalDigest(struct {
		Context              string                              `json:"context"`
		Version              int                                 `json:"version"`
		Command              []string                            `json:"command"`
		StageContainerID     string                              `json:"stageContainerId"`
		NetworkID            string                              `json:"networkId"`
		InterfaceID          string                              `json:"interfaceId"`
		SelectedIPv4         string                              `json:"selectedIpv4"`
		PortStart            uint16                              `json:"portStart"`
		PortEnd              uint16                              `json:"portEnd"`
		ContainerIPv4        string                              `json:"containerIpv4"`
		ProbeToken           string                              `json:"probeToken"`
		LocalListener        string                              `json:"localListener"`
		ProbePathPrefix      string                              `json:"probePathPrefix"`
		ProbeBodyPrefix      string                              `json:"probeBodyPrefix"`
		FallbackStatusCode   int                                 `json:"fallbackStatusCode"`
		LoopbackForbidden    bool                                `json:"loopbackForbidden"`
		StageConfigCopy      gatewayRebindStageConfigCopyBinding `json:"stageConfigCopy"`
		ProtectedIntent      string                              `json:"protectedIntentDigest"`
		ProtectedPredecessor string                              `json:"protectedPredecessorDigest"`
		PriorProgress        string                              `json:"priorProgressDigest"`
	}{
		Context: gatewayRebindStageStartEffectContext, Version: value.Version,
		Command:          []string{"container", "start", value.StageContainer.ID},
		StageContainerID: value.StageContainer.ID, NetworkID: value.Network.ID,
		InterfaceID: value.InterfaceID, SelectedIPv4: value.SelectedIPv4,
		PortStart: value.PortStart, PortEnd: value.PortEnd, ContainerIPv4: value.ContainerIPv4,
		ProbeToken: value.ProbeToken, LocalListener: net.JoinHostPort(value.ContainerIPv4,
			strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		ProbePathPrefix: "/.well-known/rig-gateway/", ProbeBodyPrefix: "rig-gateway-v2:",
		FallbackStatusCode: 404, LoopbackForbidden: true,
		StageConfigCopy: value.StageConfigCopy, ProtectedIntent: value.ProtectedIntentDigest,
		ProtectedPredecessor: value.ProtectedPredecessorDigest, PriorProgress: value.PriorProgressDigest,
	})
}

func gatewayRebindStageStartIntentBindingFor(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord,
) (gatewayRebindStageStartIntentBinding, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Sequence != 8 || previous.Phase != gatewayRebindProgressStageConfigCopied ||
		previous.ProtectedIntentDigest != intent.Digest || previous.Stage == nil ||
		previous.Stage.Network == nil || previous.Stage.ConfigVolume == nil || previous.Stage.DataVolume == nil ||
		previous.Stage.StageContainer == nil || previous.Stage.StageConfigIntent == nil ||
		previous.Stage.StageConfigCopy == nil || previous.Stage.StageStartIntent != nil ||
		!gatewayRebindStageIntentMatchesProtectedIntent(previous, intent) ||
		!validGatewayRebindStageConfigCopyBindingValue(*previous.Stage.StageConfigCopy) ||
		previous.Stage.StageConfigCopy.StageConfigIntent != *previous.Stage.StageConfigIntent {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress rebind stage start binding input")
	}
	endpoint, ok := gatewayRebindStageStartIntentEndpoint(intent, intent.NetworkObservation.Candidates)
	if !ok {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress rebind stage start endpoint")
	}
	probe, err := gatewayRebindStageConfigProbeToken(intent)
	if err != nil {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress rebind stage start probe")
	}
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress rebind protected predecessor")
	}
	value := gatewayRebindStageStartIntentBinding{
		Version: gatewayRebindStageStartIntentVersion, StageConfigCopy: *previous.Stage.StageConfigCopy,
		StageContainer: *previous.Stage.StageContainer, Network: *previous.Stage.Network,
		InterfaceID: endpoint.InterfaceID, SelectedIPv4: endpoint.IPv4,
		PortStart: intent.Intent.SuccessorProfile.PortStart, PortEnd: intent.Intent.SuccessorProfile.PortEnd,
		ContainerIPv4: intent.Intent.Network.ContainerIPv4, ProbeToken: probe,
		ProtectedIntentDigest: intent.Digest, ProtectedPredecessorDigest: predecessorDigest,
		PriorProgressDigest: previous.Digest,
	}
	value.StartEffectDigest, err = gatewayRebindStageStartEffectDigest(value)
	if err != nil || !validGatewayRebindStageStartIntentBindingValue(value) {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress rebind stage start binding input")
	}
	return value, nil
}

func validGatewayRebindStageStartIntentBindingValue(value gatewayRebindStageStartIntentBinding) bool {
	if !validGatewayRebindStageStartIntentBindingFields(value, true) {
		return false
	}
	digest, err := gatewayRebindStageStartEffectDigest(value)
	return err == nil && digest == value.StartEffectDigest
}

func validGatewayRebindStageStartIntentBindingFields(value gatewayRebindStageStartIntentBinding,
	requireEffectDigest bool,
) bool {
	selected, selectedErr := netip.ParseAddr(value.SelectedIPv4)
	container, containerErr := netip.ParseAddr(value.ContainerIPv4)
	if value.Version != gatewayRebindStageStartIntentVersion ||
		!validGatewayRebindStageConfigCopyBindingValue(value.StageConfigCopy) ||
		!validGatewayRebindStageContainerBindingValue(value.StageContainer) ||
		!validContainerID(value.Network.ID) || normalizeID(value.Network.ID) != value.Network.ID ||
		!validSHA256(value.Network.OwnershipDigest) || value.InterfaceID == "" ||
		selectedErr != nil || !selected.Is4() || !selected.IsPrivate() || selected.String() != value.SelectedIPv4 ||
		containerErr != nil || !container.Is4() || !container.IsPrivate() || container.String() != value.ContainerIPv4 ||
		value.PortStart < minimumLANPort || value.PortStart > value.PortEnd || value.PortEnd > maximumLANPort ||
		!validSHA256(value.ProbeToken) || (requireEffectDigest && !validSHA256(value.StartEffectDigest)) ||
		!validSHA256(value.ProtectedIntentDigest) || !validSHA256(value.ProtectedPredecessorDigest) ||
		!validSHA256(value.PriorProgressDigest) {
		return false
	}
	return true
}

func validGatewayRebindStageStartIntentBinding(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindStageStartIntentBinding,
) bool {
	expected, err := gatewayRebindStageStartIntentBindingFor(intent, previous)
	return err == nil && validGatewayRebindStageStartIntentBindingValue(value) && value == expected
}

type gatewayRebindStageStartIntentAttestation struct {
	Container gatewayRebindStageContainerAttestation
	Binding   gatewayRebindStageStartIntentBinding
}

// prepareGatewayRebindSuccessorStageStartIntent appends sequence nine only
// after four matching read-only observations of the exact copied, stopped
// successor. It does not start the container or persist a port-availability
// observation as permission for a later start.
func (m *Manager) prepareGatewayRebindSuccessorStageStartIntent(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.prepareGatewayRebindSuccessorStageStartIntentWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageStartIntentDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) prepareGatewayRebindSuccessorStageStartIntentWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartIntentDriver,
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
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 8 || len(history.Progress) > 9 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		stage.StageContainer == nil || stage.StageConfigIntent == nil || stage.StageConfigCopy == nil ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[7].Record
	binding, err := gatewayRebindStageStartIntentBindingFor(intent, previous)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 9 {
		if stage.StageStartIntent == nil || *stage.StageStartIntent != binding ||
			m.attestGatewayRebindStageStartIntentLocked(ctx, repository, reads, inspectDocker,
				driver, intent, *stage, binding, 9) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.StageStartIntent != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	store, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 9)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindStageStartIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 8)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageStartIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 8)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindStageStartIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 8)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindStageStartIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 8)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	record, err := newGatewayRebindStageStartIntentProgress(intent, previous, binding, occurredAt)
	if err != nil || store.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageStartIntentLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *record.Stage, binding, 9)
}

func (m *Manager) attestGatewayRebindStageStartIntentLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartIntentDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageStartIntentBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageStartIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageStartIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	final, err := m.readGatewayRebindEffectBoundaryAnchor(ctx, repository)
	if err != nil || !gatewayRebindEffectBoundaryAnchorMatchesObservation(final,
		gatewayRebindEffectBoundaryObservation{database: first.Container.Anchor.database,
			predecessor: first.Container.Anchor.predecessor, intent: first.Container.Anchor.intent,
			source: first.Container.Anchor.source, databaseDigest: first.Container.Anchor.databaseDigest,
			sourceDigest: first.Container.Anchor.sourceDigest, progressCount: first.Container.Anchor.progressCount,
			progressDigest: first.Container.Anchor.progressDigest}) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (m *Manager) readGatewayRebindStageStartIntentAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageStartIntentDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageStartIntentBinding, progressCount uint64,
) (gatewayRebindStageStartIntentAttestation, error) {
	if !validGatewayRebindStageStartIntentBindingValue(binding) || stage.Network == nil ||
		stage.StageContainer == nil || stage.StageConfigIntent == nil || stage.StageConfigCopy == nil ||
		binding.Network != *stage.Network || binding.StageContainer != *stage.StageContainer ||
		binding.StageConfigCopy != *stage.StageConfigCopy || binding.ProtectedIntentDigest != intent.Digest {
		return gatewayRebindStageStartIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) || len(history.Progress) < 8 {
		return gatewayRebindStageStartIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expected, err := gatewayRebindStageStartIntentBindingFor(intent, history.Progress[7].Record)
	if err != nil || binding != expected ||
		(progressCount == 8 && stage.StageStartIntent != nil) ||
		(progressCount == 9 && (stage.StageStartIntent == nil || *stage.StageStartIntent != binding)) ||
		(progressCount != 8 && progressCount != 9) {
		return gatewayRebindStageStartIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	container, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, stage.StageContainer, progressCount)
	if err != nil {
		return gatewayRebindStageStartIntentAttestation{}, err
	}
	if endpoint, ok := gatewayRebindStageStartIntentEndpoint(intent, container.Candidates); !ok ||
		endpoint.InterfaceID != binding.InterfaceID || endpoint.IPv4 != binding.SelectedIPv4 {
		return gatewayRebindStageStartIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	body, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindStageStartIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(body)
	inventory, err := driver.configVolumeInventory(ctx, intent, stage, body)
	if err != nil || inventory != gatewayRebindStageConfigInventoryExact || ctx.Err() != nil {
		return gatewayRebindStageStartIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindStageStartIntentAttestation{Container: container, Binding: binding}, nil
}

var _ gatewayRebindStageStartIntentDriver = managerGatewayRebindStageStartIntentDriver{}
