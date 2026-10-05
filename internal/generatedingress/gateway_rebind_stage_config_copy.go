package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindStageConfigInventory uint8

const (
	gatewayRebindStageConfigInventoryEmpty gatewayRebindStageConfigInventory = iota + 1
	gatewayRebindStageConfigInventoryExact
)

type gatewayRebindStageConfigCopyDriver interface {
	gatewayRebindStageContainerAttestor
	configVolumeInventory(context.Context, gatewayRebindProtectedIntent, gatewayRebindStageIntent,
		[]byte) (gatewayRebindStageConfigInventory, error)
	copyStageConfig(context.Context, gatewayRebindProtectedIntent, gatewayRebindStageIntent, []byte) error
}

type managerGatewayRebindStageConfigCopyDriver struct{ manager *Manager }

func (d managerGatewayRebindStageConfigCopyDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	return (managerGatewayRebindStageContainerDriver{manager: d.manager}).inspect(ctx, intent)
}

func (d managerGatewayRebindStageConfigCopyDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return (managerGatewayRebindStageContainerDriver{manager: d.manager}).inspectImage(ctx)
}

func (d managerGatewayRebindStageConfigCopyDriver) configVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	if d.manager == nil || ctx == nil || !validGatewayRebindProtectedIntent(intent) ||
		stage.StageContainer == nil || stage.ConfigVolume == nil || stage.StageConfigIntent == nil ||
		!validGatewayRebindStageContainerBinding(intent, *stage.StageContainer) ||
		!validGatewayRebindStageConfigVolumeBinding(intent, *stage.ConfigVolume) ||
		!gatewayRebindStageConfigIntentMatchesIntent(intent, stage, *stage.StageConfigIntent) ||
		len(expected) == 0 || len(expected) > gatewayV2MaxConfigBytes {
		return 0, errors.New("invalid generated ingress rebind config volume inventory input")
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "cp",
		stage.StageContainer.ID+":/config/.", "-")
	if err != nil {
		return 0, err
	}
	defer clearResult(&result)
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return 0, errors.New("generated ingress rebind config volume inventory is incomplete")
	}
	return gatewayRebindExactStageConfigVolumeArchive(result.Stdout, expected)
}

func (d managerGatewayRebindStageConfigCopyDriver) copyStageConfig(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, contents []byte,
) error {
	if d.manager == nil || ctx == nil || !validGatewayRebindProtectedIntent(intent) ||
		stage.StageContainer == nil || stage.ConfigVolume == nil || stage.StageConfigIntent == nil ||
		!gatewayRebindStageConfigIntentMatchesIntent(intent, stage, *stage.StageConfigIntent) ||
		len(contents) == 0 || len(contents) > gatewayV2MaxConfigBytes {
		return errors.New("invalid generated ingress rebind stage config copy input")
	}
	expected, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return errors.New("invalid generated ingress rebind stage config copy input")
	}
	defer clear(expected)
	digest := sha256.Sum256(contents)
	if !bytes.Equal(contents, expected) ||
		hex.EncodeToString(digest[:]) != stage.StageConfigIntent.ContentDigest ||
		int64(len(contents)) != stage.StageConfigIntent.ContentLength {
		return errors.New("generated ingress rebind stage config copy content mismatch")
	}
	// copyGatewayV2Config clears its input. Keep the caller's bytes available
	// for the mandatory post-copy comparison.
	return d.manager.copyGatewayV2Config(ctx, stage.StageContainer.ID,
		append([]byte(nil), contents...), intent.Intent.Identity.StageConfigFilename)
}

// gatewayRebindExactStageConfigVolumeArchive accepts the complete bounded TAR
// inventory for /config only when it contains the root directory and either no
// file or one regular stage.json whose bytes exactly match expected, plus at
// most one empty caddy/ directory seeded by the pinned image. Exact consumed
// offsets reject PAX/GNU metadata that archive/tar otherwise hides.
func gatewayRebindExactStageConfigVolumeArchive(value, expected []byte) (gatewayRebindStageConfigInventory, error) {
	return gatewayRebindStageConfigVolumeArchive(value, expected, false)
}

func gatewayRebindExactStartedStageConfigVolumeArchive(value, expected []byte) (gatewayRebindStageConfigInventory, error) {
	return gatewayRebindStageConfigVolumeArchive(value, expected, true)
}

func gatewayRebindStageConfigVolumeArchive(value, expected []byte, allowAutosave bool) (gatewayRebindStageConfigInventory, error) {
	const tarBlockSize = 512
	limit := defaultOutputLimit
	if allowAutosave {
		limit = gatewayRebindStartedStageConfigArchiveLimit
	}
	if len(value) < 3*tarBlockSize || len(value) > limit || len(value)%tarBlockSize != 0 ||
		len(expected) > gatewayV2MaxConfigBytes || (allowAutosave && len(expected) == 0) {
		return 0, errors.New("generated ingress rebind config volume archive is invalid")
	}
	source := bytes.NewReader(value)
	reader := tar.NewReader(source)
	root, err := reader.Next()
	if err != nil || root == nil || len(value)-source.Len() != tarBlockSize ||
		root.Typeflag != tar.TypeDir || root.Size != 0 || root.Linkname != "" ||
		(root.Name != "." && root.Name != "./") {
		return 0, errors.New("generated ingress rebind config volume archive root is invalid")
	}
	offset := tarBlockSize
	seedSeen, autosaveSeen := false, false
	inventory := gatewayRebindStageConfigInventoryEmpty
	for {
		entry, err := reader.Next()
		if err == io.EOF {
			if len(value) < offset+2*tarBlockSize || !allGatewayRebindTarZero(value[offset:]) {
				return 0, errors.New("generated ingress rebind config volume archive is truncated or has trailing data")
			}
			if autosaveSeen && (!seedSeen || inventory != gatewayRebindStageConfigInventoryExact) {
				return 0, errors.New("generated ingress rebind autosave lacks exact stage inventory")
			}
			return inventory, nil
		}
		if err != nil || entry == nil || len(value)-source.Len() != offset+tarBlockSize {
			return 0, errors.New("generated ingress rebind config volume archive entry is invalid")
		}
		if validGatewayRebindPinnedImageConfigDirectory(entry) {
			if seedSeen {
				return 0, errors.New("generated ingress rebind config volume archive repeats the image directory")
			}
			seedSeen = true
			offset += tarBlockSize
			continue
		}
		isAutosave := entry.Name == "caddy/autosave.json" || entry.Name == "./caddy/autosave.json"
		entryExpected := expected
		if isAutosave {
			if !allowAutosave || autosaveSeen {
				return 0, errors.New("generated ingress rebind config volume archive has unexpected autosave")
			}
			entryExpected, err = gatewayRebindCanonicalAutosaveConfig(expected)
			if err != nil || !validGatewayRebindConfigAutosaveHeader(entry, entryExpected) {
				clear(entryExpected)
				return 0, errors.New("generated ingress rebind autosave header is invalid")
			}
			defer clear(entryExpected)
			autosaveSeen = true
		} else if inventory != gatewayRebindStageConfigInventoryEmpty || len(expected) == 0 ||
			entry.Typeflag != tar.TypeReg || entry.Linkname != "" || entry.Size != int64(len(expected)) ||
			(entry.Name != gatewayV2StageConfigFilename && entry.Name != "./"+gatewayV2StageConfigFilename) {
			return 0, errors.New("generated ingress rebind config volume archive entry is invalid")
		}
		body := make([]byte, len(entryExpected))
		defer clear(body)
		if _, err := io.ReadFull(reader, body); err != nil || !bytes.Equal(body, entryExpected) {
			return 0, errors.New("generated ingress rebind stage config content mismatch")
		}
		var extra [1]byte
		if count, err := reader.Read(extra[:]); count != 0 || err != io.EOF {
			return 0, errors.New("generated ingress rebind stage config entry is malformed")
		}
		dataStart := offset + tarBlockSize
		payloadEnd := dataStart + ((len(entryExpected)+tarBlockSize-1)/tarBlockSize)*tarBlockSize
		if payloadEnd > len(value) || !allGatewayRebindTarZero(value[dataStart+len(entryExpected):payloadEnd]) {
			return 0, errors.New("generated ingress rebind stage config padding is invalid")
		}
		offset = payloadEnd
		if !isAutosave {
			inventory = gatewayRebindStageConfigInventoryExact
		}
	}
}

func allGatewayRebindTarZero(value []byte) bool {
	for _, octet := range value {
		if octet != 0 {
			return false
		}
	}
	return true
}

func gatewayRebindStageConfigIntentMatchesIntent(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, value gatewayRebindStageConfigIntentBinding,
) bool {
	if !validGatewayRebindProtectedIntent(intent) || stage.StageContainer == nil || stage.ConfigVolume == nil ||
		!validGatewayRebindStageConfigIntentBindingValue(value) || value.StageContainer != *stage.StageContainer ||
		value.ConfigVolume != *stage.ConfigVolume || value.ProtectedIntentDigest != intent.Digest ||
		value.Destination != "/config/"+intent.Intent.Identity.StageConfigFilename {
		return false
	}
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil || value.ProtectedPredecessorDigest != predecessorDigest {
		return false
	}
	body, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return false
	}
	defer clear(body)
	digest := sha256.Sum256(body)
	return value.ContentLength == int64(len(body)) && value.ContentDigest == hex.EncodeToString(digest[:])
}

func gatewayRebindStageConfigCopyBindingFor(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord,
) (gatewayRebindStageConfigCopyBinding, error) {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(previous) ||
		previous.Sequence != 7 || previous.Phase != gatewayRebindProgressStageConfigIntent ||
		previous.Stage == nil || previous.Stage.StageConfigIntent == nil || previous.Stage.StageConfigCopy != nil ||
		previous.ProtectedIntentDigest != intent.Digest ||
		!gatewayRebindStageConfigIntentMatchesIntent(intent, *previous.Stage, *previous.Stage.StageConfigIntent) {
		return gatewayRebindStageConfigCopyBinding{}, errors.New("invalid generated ingress rebind stage config copy binding input")
	}
	return gatewayRebindStageConfigCopyBinding{
		StageConfigIntent: *previous.Stage.StageConfigIntent, PriorProgressDigest: previous.Digest,
	}, nil
}

func validGatewayRebindStageConfigCopyBinding(intent gatewayRebindProtectedIntent,
	previous gatewayRebindProgressRecord, value gatewayRebindStageConfigCopyBinding,
) bool {
	expected, err := gatewayRebindStageConfigCopyBindingFor(intent, previous)
	return err == nil && validGatewayRebindStageConfigCopyBindingValue(value) && value == expected
}

type gatewayRebindStageConfigCopyAttestation struct {
	Container gatewayRebindStageContainerAttestation
	Inventory gatewayRebindStageConfigInventory
	Binding   gatewayRebindStageConfigCopyBinding
}

// copyGatewayRebindSuccessorStageConfig is a private rebind effect. It copies
// only the sequence-seven bytes into the exact stopped successor, reads those
// bytes back, and appends the sequence-eight receipt. It does not start the
// container, publish a route, or write SQLite.
func (m *Manager) copyGatewayRebindSuccessorStageConfig(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, occurredAt time.Time, checkpoint func(),
) (resultErr error) {
	return m.copyGatewayRebindSuccessorStageConfigWithDriver(ctx, repository, reads, inspectDocker,
		managerGatewayRebindStageConfigCopyDriver{manager: m}, occurredAt, checkpoint)
}

func (m *Manager) copyGatewayRebindSuccessorStageConfigWithDriver(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigCopyDriver,
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
	if err != nil || len(history.Intents) != 1 || len(history.Progress) < 7 || len(history.Progress) > 8 {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	intent := history.Intents[0].Intent
	stage := history.Progress[len(history.Progress)-1].Record.Stage
	if stage == nil || stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		stage.StageContainer == nil || stage.StageConfigIntent == nil ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previous := history.Progress[6].Record
	binding, err := gatewayRebindStageConfigCopyBindingFor(intent, previous)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if len(history.Progress) == 8 {
		if stage.StageConfigCopy == nil || *stage.StageConfigCopy != binding ||
			m.attestGatewayRebindStageConfigCopyLocked(ctx, repository, reads, inspectDocker, driver,
				intent, *stage, binding, 8, gatewayRebindStageConfigInventoryExact) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
		return nil
	}
	if stage.StageConfigCopy != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	previousAt, err := parseGatewayRebindProgressTime(previous.OccurredAt)
	if err != nil || !occurredAt.After(previousAt) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	body, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(body)
	progressStore, err := newGatewayRebindProgressStore(m.options.DataRoot, intent.Generation, intent.OperationID, 8)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := m.readGatewayRebindStageConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, body, 7)
	if err != nil {
		return err
	}
	second, err := m.readGatewayRebindStageConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, body, 7)
	if err != nil || !reflect.DeepEqual(first, second) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if checkpoint != nil {
		checkpoint()
	}
	third, err := m.readGatewayRebindStageConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, body, 7)
	if err != nil {
		return err
	}
	fourth, err := m.readGatewayRebindStageConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, *stage, binding, body, 7)
	if err != nil || !reflect.DeepEqual(first, third) || !reflect.DeepEqual(third, fourth) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if first.Inventory == gatewayRebindStageConfigInventoryEmpty {
		if err := driver.copyStageConfig(ctx, intent, *stage, append([]byte(nil), body...)); err != nil || ctx.Err() != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
	} else if first.Inventory != gatewayRebindStageConfigInventoryExact {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if err := m.attestGatewayRebindStageConfigCopyLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *stage, binding, 7, gatewayRebindStageConfigInventoryExact); err != nil {
		return err
	}
	record, err := newGatewayRebindStageConfigCopyProgress(intent, previous, binding, occurredAt)
	if err != nil || progressStore.installExact(ctx, record) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return m.attestGatewayRebindStageConfigCopyLocked(ctx, repository, reads, inspectDocker, driver,
		intent, *record.Stage, binding, 8, gatewayRebindStageConfigInventoryExact)
}

func (m *Manager) attestGatewayRebindStageConfigCopyLocked(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigCopyDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageConfigCopyBinding, progressCount uint64,
	want gatewayRebindStageConfigInventory,
) error {
	body, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(body)
	first, err := m.readGatewayRebindStageConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, body, progressCount)
	if err != nil || first.Inventory != want {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := m.readGatewayRebindStageConfigCopyAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, binding, body, progressCount)
	if err != nil || second.Inventory != want || !reflect.DeepEqual(first, second) {
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

func (m *Manager) readGatewayRebindStageConfigCopyAttestation(ctx context.Context,
	repository *appaccess.Repository, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector, driver gatewayRebindStageConfigCopyDriver,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
	binding gatewayRebindStageConfigCopyBinding, expected []byte, progressCount uint64,
) (gatewayRebindStageConfigCopyAttestation, error) {
	if !validGatewayRebindStageConfigCopyBindingValue(binding) || stage.StageConfigIntent == nil ||
		binding.StageConfigIntent != *stage.StageConfigIntent ||
		!gatewayRebindStageConfigIntentMatchesIntent(intent, stage, *stage.StageConfigIntent) {
		return gatewayRebindStageConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != int(progressCount) || len(history.Progress) < 7 {
		return gatewayRebindStageConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	want, err := gatewayRebindStageConfigCopyBindingFor(intent, history.Progress[6].Record)
	if err != nil || binding != want ||
		(progressCount == 7 && stage.StageConfigCopy != nil) ||
		(progressCount == 8 && (stage.StageConfigCopy == nil || *stage.StageConfigCopy != binding)) ||
		(progressCount != 7 && progressCount != 8) {
		return gatewayRebindStageConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	container, err := m.readGatewayRebindStageContainerAttestation(ctx, repository, reads, inspectDocker,
		driver, intent, stage, stage.StageContainer, progressCount)
	if err != nil {
		return gatewayRebindStageConfigCopyAttestation{}, err
	}
	inventory, err := driver.configVolumeInventory(ctx, intent, stage, expected)
	if err != nil || (inventory != gatewayRebindStageConfigInventoryEmpty &&
		inventory != gatewayRebindStageConfigInventoryExact) || ctx.Err() != nil {
		return gatewayRebindStageConfigCopyAttestation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return gatewayRebindStageConfigCopyAttestation{
		Container: container, Inventory: inventory, Binding: binding,
	}, nil
}

var _ gatewayRebindStageConfigCopyDriver = managerGatewayRebindStageConfigCopyDriver{}
