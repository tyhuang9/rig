package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// The approved stage file and one bounded Caddy snapshot, plus TAR framing.
// This bound applies only after durable stage-start intent, not generic Docker output.
const gatewayRebindStartedStageConfigArchiveLimit = 128 << 10

// Caddy v2.11.4 unmarshals its input into any, then marshals that raw value
// before persisting autosave.json. Derive only the expected snapshot here;
// never normalize an observed file into an acceptable configuration.
func gatewayRebindCanonicalAutosaveConfig(expected []byte) ([]byte, error) {
	invalid := errors.New("invalid generated ingress rebind autosave config")
	if len(expected) == 0 || len(expected) > gatewayV2MaxConfigBytes {
		return nil, invalid
	}
	var decoded any
	if err := json.Unmarshal(expected, &decoded); err != nil {
		return nil, invalid
	}
	if value, ok := decoded.(map[string]any); !ok || value == nil {
		return nil, invalid
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || len(canonical) == 0 || len(canonical) > gatewayV2MaxConfigBytes {
		clear(canonical)
		return nil, invalid
	}
	return canonical, nil
}

func validGatewayRebindConfigAutosaveHeader(header *tar.Header, expected []byte) bool {
	return header != nil && len(expected) > 0 && len(expected) <= gatewayV2MaxConfigBytes &&
		(header.Name == "caddy/autosave.json" || header.Name == "./caddy/autosave.json") &&
		header.Typeflag == tar.TypeReg && header.Linkname == "" && header.Size == int64(len(expected)) &&
		header.Uid == 1000 && header.Gid == 1000 && header.Mode == 0600
}

// A start can succeed and then be compensated or lose its acknowledgment.
// The stopped stage may therefore retain its exact snapshot after sequence nine.
// A caller-supplied binding alone must not authorize the broader inventory.
func (d managerGatewayRebindStageStartIntentDriver) startedStageConfigVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	invalid := errors.New("generated ingress rebind started-stage inventory is unavailable")
	if d.manager == nil || d.manager.runner == nil || ctx == nil || ctx.Err() != nil {
		return 0, invalid
	}
	history, err := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !gatewayRebindStageAutosaveHistoryMatches(history, intent, stage, expected) {
		return 0, invalid
	}
	result, err := d.manager.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: d.manager.options.DockerExecutable,
		Args:       []string{"container", "cp", stage.StageContainer.ID + ":/config/.", "-"},
		Directory:  d.manager.options.WorkingDirectory, Env: append([]string(nil), d.manager.dockerEnv...),
		Timeout: d.manager.options.CommandTimeout, OutputLimit: gatewayRebindStartedStageConfigArchiveLimit,
	})
	defer clearResult(&result)
	if err != nil || ctx.Err() != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return 0, invalid
	}
	inventory, err := gatewayRebindExactStartedStageConfigVolumeArchive(result.Stdout, expected)
	if err != nil {
		return 0, err
	}
	confirmed, err := d.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(history, confirmed) ||
		!gatewayRebindStageAutosaveHistoryMatches(confirmed, intent, stage, expected) {
		return 0, invalid
	}
	return inventory, nil
}

func gatewayRebindStageAutosaveHistoryMatches(history gatewayRebindProtectedIntentHistory,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) bool {
	if len(history.Intents) != 1 || !reflect.DeepEqual(history.Intents[0].Intent, intent) || len(history.Progress) < 9 ||
		!validGatewayRebindStageIntent(stage) || stage.StageStartIntent == nil || stage.StageConfigIntent == nil ||
		stage.StageContainer == nil || stage.StageConfigCopy == nil || stage.Network == nil ||
		history.Progress[8].Record.Sequence != 9 || history.Progress[8].Record.Phase != gatewayRebindProgressStageStartIntent ||
		history.Progress[8].Record.Stage == nil || history.Progress[8].Record.Stage.StageStartIntent == nil {
		return false
	}
	// Later phases may append fields, but cannot change the retained sequence-nine
	// prefix. Spell out that prefix without teaching this reader later phases.
	prefix := gatewayRebindStageIntent{
		Identity: stage.Identity, ApprovedCaddyImageDigest: stage.ApprovedCaddyImageDigest,
		ObservedDockerImageID: stage.ObservedDockerImageID, NetworkPlan: stage.NetworkPlan,
		NetworkPlanDigest: stage.NetworkPlanDigest, NetworkTopologyDigest: stage.NetworkTopologyDigest,
		Network: stage.Network, ConfigVolume: stage.ConfigVolume, DataVolume: stage.DataVolume,
		StageContainer: stage.StageContainer, StageConfigIntent: stage.StageConfigIntent,
		StageConfigCopy: stage.StageConfigCopy, StageStartIntent: stage.StageStartIntent,
	}
	if !reflect.DeepEqual(prefix, *history.Progress[8].Record.Stage) {
		return false
	}
	binding, err := gatewayRebindStageStartIntentBindingFor(intent, history.Progress[7].Record)
	if err != nil || binding != *stage.StageStartIntent || binding != *history.Progress[8].Record.Stage.StageStartIntent ||
		binding.StageContainer != *stage.StageContainer || binding.Network != *stage.Network ||
		binding.StageConfigCopy != *stage.StageConfigCopy ||
		!gatewayRebindStageConfigIntentMatchesIntent(intent, stage, *stage.StageConfigIntent) {
		return false
	}
	body, err := gatewayRebindStageConfigBytes(intent)
	defer clear(body)
	return err == nil && bytes.Equal(body, expected)
}
