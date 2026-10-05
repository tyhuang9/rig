package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const gatewayRebindStageConfigProbeContext = "hostd/generated-ingress/rebind/stage-config-probe/v1"

type gatewayRebindStageConfigIntentDriver interface {
	gatewayRebindStageContainerAttestor
	configVolumeEmpty(context.Context, gatewayRebindProtectedIntent, gatewayRebindStageIntent) (bool, error)
}

type managerGatewayRebindStageConfigIntentDriver struct{ manager *Manager }

func (d managerGatewayRebindStageConfigIntentDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	return (managerGatewayRebindStageContainerDriver{manager: d.manager}).inspect(ctx, intent)
}

func (d managerGatewayRebindStageConfigIntentDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return (managerGatewayRebindStageContainerDriver{manager: d.manager}).inspectImage(ctx)
}

func (d managerGatewayRebindStageConfigIntentDriver) configVolumeEmpty(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) (bool, error) {
	if d.manager == nil || ctx == nil || !validGatewayRebindProtectedIntent(intent) ||
		stage.StageContainer == nil || stage.ConfigVolume == nil ||
		!validGatewayRebindStageContainerBinding(intent, *stage.StageContainer) ||
		!validGatewayRebindStageConfigVolumeBinding(intent, *stage.ConfigVolume) {
		return false, errors.New("invalid generated ingress rebind config volume inventory input")
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "cp",
		stage.StageContainer.ID+":/config/.", "-")
	if err != nil {
		return false, err
	}
	defer clearResult(&result)
	if len(result.Stderr) != 0 {
		return false, errors.New("generated ingress rebind config volume inventory emitted diagnostics")
	}
	if err := gatewayRebindExactEmptyConfigVolumeArchive(result.Stdout); err != nil {
		return false, err
	}
	return true, nil
}

// gatewayRebindExactEmptyConfigVolumeArchive accepts only the successful tar
// shape produced for /config without configuration files. Docker may seed one
// exact empty caddy/ directory from the pinned image. Unexpected files, links,
// metadata entries, malformed archives, and truncated command output fail closed.
func gatewayRebindExactEmptyConfigVolumeArchive(value []byte) error {
	const tarBlockSize = 512
	if len(value) < 3*tarBlockSize || len(value) > defaultOutputLimit || len(value)%tarBlockSize != 0 {
		return errors.New("generated ingress rebind config volume archive is invalid")
	}
	source := bytes.NewReader(value)
	reader := tar.NewReader(source)
	header, err := reader.Next()
	if err != nil || header == nil || len(value)-source.Len() != tarBlockSize ||
		header.Typeflag != tar.TypeDir || header.Size != 0 || header.Linkname != "" ||
		(header.Name != "." && header.Name != "./") {
		return errors.New("generated ingress rebind config volume archive is not empty")
	}
	offset := tarBlockSize
	header, err = reader.Next()
	if err != io.EOF {
		if err != nil || len(value)-source.Len() != 2*tarBlockSize || !validGatewayRebindPinnedImageConfigDirectory(header) {
			return errors.New("generated ingress rebind config volume archive has unexpected entries")
		}
		offset += tarBlockSize
		_, err = reader.Next()
	}
	if err != io.EOF {
		return errors.New("generated ingress rebind config volume archive has unexpected entries")
	}
	// Preserve two complete zero end blocks and reject hidden metadata records,
	// incomplete trailers and all data after the explicitly permitted headers.
	if len(value) < offset+2*tarBlockSize {
		return errors.New("generated ingress rebind config volume archive is truncated")
	}
	for _, value := range value[offset:] {
		if value != 0 {
			return errors.New("generated ingress rebind config volume archive has trailing data")
		}
	}
	return nil
}

func validGatewayRebindPinnedImageConfigDirectory(header *tar.Header) bool {
	return header != nil && (header.Name == "caddy/" || header.Name == "./caddy/") &&
		header.Typeflag == tar.TypeDir && header.Size == 0 && header.Linkname == "" &&
		header.Uid == 0 && header.Gid == 0 && header.Mode == 0o1777
}

// gatewayRebindStageConfigBytes is the sequence-seven v1 byte format. Protected
// history validation regenerates these bytes, so any future builder change
// must retain this output for v1 records and use a distinct version for new
// records.
func gatewayRebindStageConfigBytes(intent gatewayRebindProtectedIntent) ([]byte, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return nil, errors.New("invalid generated ingress rebind stage config input")
	}
	probe, err := gatewayRebindStageConfigProbeToken(intent)
	if err != nil {
		return nil, errors.New("invalid generated ingress rebind stage config input")
	}
	body, err := buildCaddyConfigV2(map[string]routeRecord{},
		net.JoinHostPort(intent.Intent.Network.ContainerIPv4,
			strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{
			SelectedIPv4: intent.Intent.SuccessorProfile.SelectedIPv4,
			PortStart:    intent.Intent.SuccessorProfile.PortStart,
			PortEnd:      intent.Intent.SuccessorProfile.PortEnd,
			ProbeToken:   probe,
		}, map[uint16]caddyV2LANAssignment{})
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes {
		clear(body)
		return nil, errors.New("invalid generated ingress rebind stage config")
	}
	return body, nil
}

func gatewayRebindStageConfigProbeToken(intent gatewayRebindProtectedIntent) (string, error) {
	if !validGatewayRebindProtectedIntent(intent) {
		return "", errors.New("invalid generated ingress rebind stage config probe input")
	}
	return gatewayRebindStageConfigProbeTokenV1(intent.Digest, intent.OperationID,
		intent.Intent.Identity.Digest, intent.Intent.NetworkDigest)
}

// gatewayRebindStageConfigProbeTokenV1 is part of the sequence-seven byte
// format. Keep its context and field projection stable for existing records.
func gatewayRebindStageConfigProbeTokenV1(protectedIntentDigest, operationID,
	identityDigest, networkDigest string,
) (string, error) {
	return canonicalDigest(struct {
		Context               string `json:"context"`
		ProtectedIntentDigest string `json:"protectedIntentDigest"`
		OperationID           string `json:"operationId"`
		IdentityDigest        string `json:"identityDigest"`
		NetworkDigest         string `json:"networkDigest"`
	}{
		Context: gatewayRebindStageConfigProbeContext, ProtectedIntentDigest: protectedIntentDigest,
		OperationID: operationID, IdentityDigest: identityDigest,
		NetworkDigest: networkDigest,
	})
}

func gatewayRebindStageConfigIntentBindingFor(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord,
) (gatewayRebindStageConfigIntentBinding, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Sequence != 6 || previous.Stage == nil || previous.Stage.ConfigVolume == nil ||
		previous.Stage.StageContainer == nil || previous.Stage.StageConfigIntent != nil ||
		previous.ProtectedIntentDigest != intent.Digest {
		return gatewayRebindStageConfigIntentBinding{}, errors.New("invalid generated ingress rebind stage config binding input")
	}
	body, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindStageConfigIntentBinding{}, err
	}
	defer clear(body)
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil {
		return gatewayRebindStageConfigIntentBinding{}, errors.New("invalid generated ingress rebind protected predecessor")
	}
	digest := sha256.Sum256(body)
	return gatewayRebindStageConfigIntentBinding{
		ContentDigest: hex.EncodeToString(digest[:]), ContentLength: int64(len(body)),
		Destination:    "/config/" + intent.Intent.Identity.StageConfigFilename,
		StageContainer: *previous.Stage.StageContainer, ConfigVolume: *previous.Stage.ConfigVolume,
		ProtectedIntentDigest: intent.Digest, ProtectedPredecessorDigest: predecessorDigest,
		PriorProgressDigest: previous.Digest,
	}, nil
}

func validGatewayRebindStageConfigIntentBinding(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindStageConfigIntentBinding,
) bool {
	expected, err := gatewayRebindStageConfigIntentBindingFor(intent, previous)
	return err == nil && validGatewayRebindStageConfigIntentBindingValue(value) && value == expected
}

type gatewayRebindStageConfigIntentAttestation struct {
	Container         gatewayRebindStageContainerAttestation
	ConfigVolumeEmpty bool
	Binding           gatewayRebindStageConfigIntentBinding
}

// prepareGatewayRebindSuccessorStageConfigIntent is a private protected-state
// step. It proves the exact stopped successor and an empty /config mount, then
// pins deterministic probe-and-404-only bytes. It does not copy config, start a
// container, publish a route, write SQLite, or provision an application DB.
func (m *Manager) prepareGatewayRebindSuccessorStageConfigIntent(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.prepareGatewayRebindSuccessorStageConfigIntentWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageConfigIntentDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) prepareGatewayRebindSuccessorStageConfigIntentWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigIntentDriver,
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
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 6 || len(history.Progress) > 7 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		stage.StageContainer == nil || stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 7 {
		if stage.StageConfigIntent == nil || m.attestGatewayRebindStageConfigIntentLocked(ctx, repository,
			reads, inspectDocker, driver, intent, *stage, *stage.StageConfigIntent, 7) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.StageConfigIntent != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[5].Record
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindStageConfigIntentBindingFor(intent, previous)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 7)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindStageConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 6)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 6)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindStageConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 6)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindStageConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, 6)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	record, err := newGatewayRebindStageConfigIntentProgress(intent, previous, binding, occurredAt)
	if err != nil || progressStore.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageConfigIntentLocked(ctx, repository, reads, inspectDocker,
		driver, intent, *record.Stage, *record.Stage.StageConfigIntent, 7)
}

func (m *Manager) attestGatewayRebindStageConfigIntentLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigIntentDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageConfigIntentBinding, progressCount uint64,
) error {
	first, err := m.readGatewayRebindStageConfigIntentAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, progressCount)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageConfigIntentAttestation(ctx, repository, reads, inspectDocker,
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

func (m *Manager) readGatewayRebindStageConfigIntentAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigIntentDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageConfigIntentBinding, progressCount uint64,
) (gatewayRebindStageConfigIntentAttestation, error) {
	if !validGatewayRebindStageConfigIntentBindingValue(binding) || stage.StageContainer == nil ||
		stage.ConfigVolume == nil || binding.StageContainer != *stage.StageContainer ||
		binding.ConfigVolume != *stage.ConfigVolume || binding.ProtectedIntentDigest != intent.Digest {
		return gatewayRebindStageConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) || len(history.Progress) < 6 {
		return gatewayRebindStageConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	expected, err := gatewayRebindStageConfigIntentBindingFor(intent, history.Progress[5].Record)
	if err != nil || binding != expected ||
		(progressCount == 6 && stage.StageConfigIntent != nil) ||
		(progressCount == 7 && (stage.StageConfigIntent == nil || *stage.StageConfigIntent != binding)) ||
		(progressCount != 6 && progressCount != 7) {
		return gatewayRebindStageConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	container, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, stage.StageContainer, progressCount)
	if err != nil {
		return gatewayRebindStageConfigIntentAttestation{}, err
	}
	empty, err := driver.configVolumeEmpty(ctx, intent, stage)
	if err != nil || !empty || ctx.Err() != nil {
		return gatewayRebindStageConfigIntentAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindStageConfigIntentAttestation{Container: container, ConfigVolumeEmpty: true, Binding: binding}, nil
}

var _ gatewayRebindStageConfigIntentDriver = managerGatewayRebindStageConfigIntentDriver{}
