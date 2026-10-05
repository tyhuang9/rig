package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// Two bounded configurations plus TAR headers and padding. This limit applies
// only to the exact /config inventory read, never to generic Docker commands.
const gatewayRebindFinalConfigArchiveLimit = 128 << 10

type gatewayRebindFinalConfigInventory uint8

const (
	gatewayRebindFinalConfigInventoryStageOnly gatewayRebindFinalConfigInventory = iota + 1
	gatewayRebindFinalConfigInventoryExactPair
)

type managerGatewayRebindFinalConfigCopyDriver struct{ manager *Manager }

func (d managerGatewayRebindFinalConfigCopyDriver) inspect(ctx context.Context,
	intent gatewayRebindProtectedIntent,
) (gatewayRebindStageContainerObservation, error) {
	return (managerGatewayRebindFinalConfigIntentDriver{manager: d.manager}).inspect(ctx, intent)
}

func (d managerGatewayRebindFinalConfigCopyDriver) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	return (managerGatewayRebindFinalConfigIntentDriver{manager: d.manager}).inspectImage(ctx)
}

func (d managerGatewayRebindFinalConfigCopyDriver) configVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	return (managerGatewayRebindFinalConfigIntentDriver{manager: d.manager}).configVolumeInventory(ctx, intent, stage, expected)
}

func (d managerGatewayRebindFinalConfigCopyDriver) liveConfig(ctx context.Context, id string) ([]byte, error) {
	return (managerGatewayRebindFinalConfigIntentDriver{manager: d.manager}).liveConfig(ctx, id)
}

func (d managerGatewayRebindFinalConfigCopyDriver) hostProbe(ctx context.Context, address string,
	port uint16, host, path string,
) gatewayV2HostProbeResult {
	return (managerGatewayRebindFinalConfigIntentDriver{manager: d.manager}).hostProbe(ctx, address, port, host, path)
}

func (d managerGatewayRebindFinalConfigCopyDriver) containerProbe(ctx context.Context, id, address string,
	port uint16, host, challenge string,
) bool {
	return (managerGatewayRebindFinalConfigIntentDriver{manager: d.manager}).containerProbe(ctx, id, address, port, host, challenge)
}

func (d managerGatewayRebindFinalConfigCopyDriver) finalConfigVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expectedStage, expectedActive []byte,
) (gatewayRebindFinalConfigInventory, error) {
	if d.manager == nil || d.manager.runner == nil || ctx == nil || ctx.Err() != nil ||
		!gatewayRebindFinalConfigDriverContentsMatch(intent, stage, expectedStage, expectedActive) {
		return 0, errors.New("invalid generated ingress rebind final config inventory input")
	}
	result, err := d.manager.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: d.manager.options.DockerExecutable,
		Args:       []string{"container", "cp", stage.StageContainer.ID + ":/config/.", "-"},
		Directory:  d.manager.options.WorkingDirectory, Env: append([]string(nil), d.manager.dockerEnv...),
		Timeout: d.manager.options.CommandTimeout, OutputLimit: gatewayRebindFinalConfigArchiveLimit,
	})
	defer clearResult(&result)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, &Error{Code: DiagnosticCancelled}
	}
	if err != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return 0, &Error{Code: DiagnosticIngressUnavailable}
	}
	return gatewayRebindExactFinalConfigVolumeArchive(result.Stdout, expectedStage, expectedActive)
}

func (d managerGatewayRebindFinalConfigCopyDriver) copyFinalConfig(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, contents []byte,
) error {
	if d.manager == nil || d.manager.runner == nil || ctx == nil || ctx.Err() != nil {
		return errors.New("invalid generated ingress rebind final config copy input")
	}
	expectedStage, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return errors.New("invalid generated ingress rebind final config copy input")
	}
	defer clear(expectedStage)
	if !gatewayRebindFinalConfigDriverContentsMatch(intent, stage, expectedStage, contents) {
		return errors.New("generated ingress rebind final config copy content mismatch")
	}
	// The shared copy helper clears its input. Retain the caller's bytes for
	// mandatory post-copy inventory comparison, and clear the clone on all paths.
	copyBytes := append([]byte(nil), contents...)
	defer clear(copyBytes)
	return d.manager.copyGatewayV2Config(ctx, stage.StageContainer.ID, copyBytes,
		intent.Intent.Identity.ActiveConfigFilename)
}

func gatewayRebindFinalConfigDriverContentsMatch(intent gatewayRebindProtectedIntent,
	stage gatewayRebindStageIntent, expectedStage, expectedActive []byte,
) bool {
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindStageIntent(stage) ||
		stage.Identity != intent.Intent.Identity || stage.NetworkPlan != intent.Intent.Network ||
		stage.NetworkPlanDigest != intent.Intent.NetworkDigest ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest ||
		stage.ApprovedCaddyImageDigest != intent.Intent.Identity.CaddyImageDigest ||
		stage.StageContainer == nil || stage.ConfigVolume == nil || stage.StageConfigIntent == nil ||
		stage.StageServing == nil || stage.FinalConfigIntent == nil ||
		!validGatewayRebindStageContainerBinding(intent, *stage.StageContainer) ||
		!validGatewayRebindStageConfigVolumeBinding(intent, *stage.ConfigVolume) ||
		!gatewayRebindStageConfigIntentMatchesIntent(intent, stage, *stage.StageConfigIntent) ||
		len(expectedStage) == 0 || len(expectedStage) > gatewayV2MaxConfigBytes ||
		len(expectedActive) == 0 || len(expectedActive) > gatewayV2MaxConfigBytes {
		return false
	}
	binding := stage.FinalConfigIntent
	if !validGatewayRebindFinalConfigIntentBindingValue(*binding) ||
		binding.ProtectedIntentDigest != intent.Digest ||
		binding.SuccessorIdentityDigest != intent.Intent.Identity.Digest ||
		binding.SequenceTenDigest != binding.PriorProgressDigest ||
		binding.Destination != "/config/"+intent.Intent.Identity.ActiveConfigFilename {
		return false
	}
	predecessorDigest, err := canonicalDigest(intent.Intent.Predecessor)
	if err != nil || predecessorDigest != binding.ProtectedPredecessorDigest {
		return false
	}
	stageBytes, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		return false
	}
	defer clear(stageBytes)
	activeBytes, err := gatewayRebindFinalConfigBytes(intent, binding.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(activeBytes)
	digest := sha256.Sum256(activeBytes)
	return bytes.Equal(stageBytes, expectedStage) && bytes.Equal(activeBytes, expectedActive) &&
		int64(len(activeBytes)) == binding.ContentLength && hex.EncodeToString(digest[:]) == binding.ContentDigest
}

// Require the complete bounded /config inventory. The raw offsets around Next
// reject metadata records that archive/tar otherwise consumes transparently.
// The existing stage-only parser remains unchanged and rejects active.json.
func gatewayRebindExactFinalConfigVolumeArchive(value, expectedStage, expectedActive []byte) (gatewayRebindFinalConfigInventory, error) {
	const blockSize = 512
	invalid := errors.New("generated ingress rebind final config archive is invalid")
	if len(value) < 3*blockSize || len(value) > gatewayRebindFinalConfigArchiveLimit || len(value)%blockSize != 0 ||
		len(expectedStage) == 0 || len(expectedStage) > gatewayV2MaxConfigBytes ||
		len(expectedActive) == 0 || len(expectedActive) > gatewayV2MaxConfigBytes {
		return 0, invalid
	}
	source := bytes.NewReader(value)
	reader := tar.NewReader(source)
	root, err := reader.Next()
	if err != nil || root == nil || len(value)-source.Len() != blockSize || root.Typeflag != tar.TypeDir ||
		root.Size != 0 || root.Linkname != "" || (root.Name != "." && root.Name != "./") {
		return 0, invalid
	}
	offset := blockSize
	seenStage, seenActive := false, false
	for {
		entry, err := reader.Next()
		if err == io.EOF {
			if !seenStage || len(value) < offset+2*blockSize || !allGatewayRebindTarZero(value[offset:]) {
				return 0, invalid
			}
			if seenActive {
				return gatewayRebindFinalConfigInventoryExactPair, nil
			}
			return gatewayRebindFinalConfigInventoryStageOnly, nil
		}
		if err != nil || entry == nil || len(value)-source.Len() != offset+blockSize ||
			entry.Typeflag != tar.TypeReg || entry.Linkname != "" {
			return 0, invalid
		}
		var expected []byte
		switch entry.Name {
		case gatewayV2StageConfigFilename, "./" + gatewayV2StageConfigFilename:
			if seenStage {
				return 0, invalid
			}
			seenStage, expected = true, expectedStage
		case gatewayV2ActiveConfigFile, "./" + gatewayV2ActiveConfigFile:
			if seenActive {
				return 0, invalid
			}
			seenActive, expected = true, expectedActive
		default:
			return 0, invalid
		}
		if entry.Size != int64(len(expected)) {
			return 0, invalid
		}
		dataStart := offset + blockSize
		payloadEnd := dataStart + ((len(expected)+blockSize-1)/blockSize)*blockSize
		if payloadEnd > len(value) || !bytes.Equal(value[dataStart:dataStart+len(expected)], expected) ||
			!allGatewayRebindTarZero(value[dataStart+len(expected):payloadEnd]) {
			return 0, invalid
		}
		if count, err := io.Copy(io.Discard, reader); err != nil || count != int64(len(expected)) {
			return 0, invalid
		}
		offset = payloadEnd
	}
}

var _ gatewayRebindFinalConfigCopyDriver = managerGatewayRebindFinalConfigCopyDriver{}
