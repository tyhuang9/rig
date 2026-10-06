package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

type gatewayRebindTypedEffectGuard func(context.Context) error

type gatewayRebindTypedStageDriver interface {
	observeImage(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectGuard) (string, error)
	bindNetwork(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) (gatewayRebindStageNetworkBinding, error)
	bindConfigVolume(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) (gatewayRebindStageConfigVolumeBinding, error)
	bindDataVolume(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) (gatewayRebindStageDataVolumeBinding, error)
	bindStageContainer(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) (gatewayRebindStageContainerBinding, error)
	copyStageConfig(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) error
	serveStage(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindTypedEffectProgress,
		gatewayRebindTypedEffectGuard) (string, error)
	copyFinalConfig(context.Context, gatewayRebindProtectedIntentV2, gatewayRebindPredecessorCheckpoint,
		gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard) error
}

func (d gatewayRebindTypedStageRuntime) stable(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedStageObservation, error) {
	if guard == nil || guard(ctx) != nil {
		return gatewayRebindTypedStageObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	first, err := d.read(ctx, intent)
	if err != nil {
		return gatewayRebindTypedStageObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	second, err := d.read(ctx, intent)
	if err != nil || !reflect.DeepEqual(first, second) || ctx.Err() != nil || guard(ctx) != nil {
		return gatewayRebindTypedStageObservation{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return second, nil
}

func (d gatewayRebindTypedStageRuntime) observeImage(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, guard gatewayRebindTypedEffectGuard,
) (string, error) {
	value, err := d.stable(ctx, intent, guard)
	if err != nil || !gatewayRebindTypedStageImageMatches(intent, value) || value.NetworkFound ||
		value.ConfigVolumeFound || value.DataVolumeFound || value.StageContainerFound || value.FinalContainerFound ||
		len(value.OwnedContainers) != 0 || len(value.OwnedVolumes) != 0 || len(value.OwnedNetworks) != 0 {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	return value.Image.ID, nil
}

func (d gatewayRebindTypedStageRuntime) bindNetwork(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageNetworkBinding, error) {
	value, err := d.stable(ctx, intent, guard)
	if err != nil || effect.Network != nil {
		return gatewayRebindStageNetworkBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	if value.NetworkFound {
		binding, buildErr := gatewayRebindTypedStageNetworkBindingFor(intent, value.NetworkID)
		candidate := effect
		candidate.Network = &binding
		if buildErr != nil || !gatewayRebindTypedStagePrefixMatches(intent, candidate, value) {
			return gatewayRebindStageNetworkBinding{}, gatewayRebindEffectBoundaryError(ctx)
		}
		return binding, nil
	}
	if !gatewayRebindTypedStagePrefixMatches(intent, effect, value) || guard(ctx) != nil {
		return gatewayRebindStageNetworkBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	bridge, err := gatewayRebindTypedStageBridgeName(intent)
	if err != nil {
		return gatewayRebindStageNetworkBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	args := []string{"network", "create", "--driver", "bridge", "--subnet", intent.Network.Subnet,
		"--gateway", intent.Network.GatewayIPv4, "--opt", gatewayRebindBridgeNameOptionKey + "=" + bridge}
	args = appendGatewayV2Labels(args, gatewayRebindTypedStageResourceLabels(intent,
		gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole))
	result, runErr := d.manager.run(ctx, d.manager.options.CommandTimeout, append(args, intent.Identity.IngressNetwork)...)
	defer clearResult(&result)
	id := normalizeID(strings.TrimSpace(string(result.Stdout)))
	if runErr != nil || !validContainerID(id) || guard(ctx) != nil {
		return gatewayRebindStageNetworkBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindTypedStageNetworkBindingFor(intent, id)
	confirmed, inspectErr := d.stable(ctx, intent, guard)
	candidate := effect
	candidate.Network = &binding
	if err != nil || inspectErr != nil || !gatewayRebindTypedStagePrefixMatches(intent, candidate, confirmed) {
		return gatewayRebindStageNetworkBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return binding, nil
}

func (d gatewayRebindTypedStageRuntime) bindConfigVolume(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageConfigVolumeBinding, error) {
	return d.bindVolume(ctx, intent, effect, true, guard)
}

func (d gatewayRebindTypedStageRuntime) bindDataVolume(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageDataVolumeBinding, error) {
	value, err := d.bindVolume(ctx, intent, effect, false, guard)
	return gatewayRebindStageDataVolumeBinding(value), err
}

func (d gatewayRebindTypedStageRuntime) bindVolume(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress, config bool,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageConfigVolumeBinding, error) {
	if effect.Network == nil || (config && effect.ConfigVolume != nil) ||
		(!config && (effect.ConfigVolume == nil || effect.DataVolume != nil)) {
		return gatewayRebindStageConfigVolumeBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	name, role := intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole
	if !config {
		name, role = intent.Identity.DataVolume, gatewayV2DataVolumeRole
	}
	value, err := d.stable(ctx, intent, guard)
	if err != nil {
		return gatewayRebindStageConfigVolumeBinding{}, err
	}
	found, volume, identity := value.ConfigVolumeFound, value.ConfigVolume, value.ConfigVolumeIdentity
	if !config {
		found, volume, identity = value.DataVolumeFound, value.DataVolume, value.DataVolumeIdentity
	}
	build := func() (gatewayRebindStageConfigVolumeBinding, error) {
		if config {
			value, buildErr := gatewayRebindTypedStageConfigVolumeBindingFor(intent, identity.Mountpoint, identity.CreatedAt)
			return value, buildErr
		}
		value, buildErr := gatewayRebindTypedStageDataVolumeBindingFor(intent, identity.Mountpoint, identity.CreatedAt)
		return gatewayRebindStageConfigVolumeBinding(value), buildErr
	}
	if found {
		binding, buildErr := build()
		candidate := effect
		if config {
			copy := binding
			candidate.ConfigVolume = &copy
		} else {
			copy := gatewayRebindStageDataVolumeBinding(binding)
			candidate.DataVolume = &copy
		}
		if buildErr != nil || volume.Name != name || !gatewayRebindTypedStagePrefixMatches(intent, candidate, value) {
			return gatewayRebindStageConfigVolumeBinding{}, gatewayRebindEffectBoundaryError(ctx)
		}
		return binding, nil
	}
	if !gatewayRebindTypedStagePrefixMatches(intent, effect, value) || guard(ctx) != nil {
		return gatewayRebindStageConfigVolumeBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	args := []string{"volume", "create", "--driver", "local"}
	args = appendGatewayV2Labels(args, gatewayRebindTypedStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, role))
	result, runErr := d.manager.run(ctx, d.manager.options.CommandTimeout, append(args, name)...)
	defer clearResult(&result)
	if runErr != nil || strings.TrimSpace(string(result.Stdout)) != name || guard(ctx) != nil {
		return gatewayRebindStageConfigVolumeBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	confirmed, inspectErr := d.stable(ctx, intent, guard)
	if inspectErr != nil {
		return gatewayRebindStageConfigVolumeBinding{}, inspectErr
	}
	if config {
		identity = confirmed.ConfigVolumeIdentity
	} else {
		identity = confirmed.DataVolumeIdentity
	}
	binding, buildErr := build()
	candidate := effect
	if config {
		copy := binding
		candidate.ConfigVolume = &copy
	} else {
		copy := gatewayRebindStageDataVolumeBinding(binding)
		candidate.DataVolume = &copy
	}
	if buildErr != nil || !gatewayRebindTypedStagePrefixMatches(intent, candidate, confirmed) {
		return gatewayRebindStageConfigVolumeBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return binding, nil
}

func (d gatewayRebindTypedStageRuntime) bindStageContainer(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageContainerBinding, error) {
	if effect.Network == nil || effect.ConfigVolume == nil || effect.DataVolume == nil || effect.StageContainer != nil {
		return gatewayRebindStageContainerBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	value, err := d.stable(ctx, intent, guard)
	if err != nil {
		return gatewayRebindStageContainerBinding{}, err
	}
	if value.StageContainerFound {
		binding, buildErr := gatewayRebindTypedStageContainerBindingFor(intent, effect, value.StageContainer.ID)
		candidate := effect
		candidate.StageContainer = &binding
		if buildErr != nil || !gatewayRebindTypedStagePrefixMatches(intent, candidate, value) {
			return gatewayRebindStageContainerBinding{}, gatewayRebindEffectBoundaryError(ctx)
		}
		return binding, nil
	}
	if !gatewayRebindTypedStagePrefixMatches(intent, effect, value) || guard(ctx) != nil {
		return gatewayRebindStageContainerBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	args, err := gatewayRebindTypedStageContainerCreateArgs(intent, effect)
	if err != nil {
		return gatewayRebindStageContainerBinding{}, err
	}
	result, runErr := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	defer clearResult(&result)
	id := normalizeID(strings.TrimSpace(string(result.Stdout)))
	if runErr != nil || !validContainerID(id) || guard(ctx) != nil {
		return gatewayRebindStageContainerBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	binding, err := gatewayRebindTypedStageContainerBindingFor(intent, effect, id)
	confirmed, inspectErr := d.stable(ctx, intent, guard)
	candidate := effect
	candidate.StageContainer = &binding
	if err != nil || inspectErr != nil || !gatewayRebindTypedStagePrefixMatches(intent, candidate, confirmed) {
		return gatewayRebindStageContainerBinding{}, gatewayRebindEffectBoundaryError(ctx)
	}
	return binding, nil
}

func (d gatewayRebindTypedStageRuntime) stageConfigInventory(ctx context.Context,
	effect gatewayRebindTypedEffectProgress, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	if effect.StageContainer == nil {
		return 0, errors.New("typed stage container is missing")
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "cp",
		effect.StageContainer.ID+":/config/.", "-")
	if err != nil {
		return 0, err
	}
	defer clearResult(&result)
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return 0, errors.New("typed stage config inventory is incomplete")
	}
	return gatewayRebindExactStageConfigVolumeArchive(result.Stdout, expected)
}

func (d gatewayRebindTypedStageRuntime) copyStageConfig(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) error {
	if effect.StageConfigIntent == nil || effect.StageConfigCopy != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	value, err := d.stable(ctx, intent, guard)
	if err != nil || !gatewayRebindTypedStagePrefixMatches(intent, effect, value) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	body, err := gatewayRebindTypedStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(body)
	inventory, err := d.stageConfigInventory(ctx, effect, body)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if inventory == gatewayRebindStageConfigInventoryEmpty {
		if guard(ctx) != nil || d.manager.copyGatewayV2Config(ctx, effect.StageContainer.ID,
			append([]byte(nil), body...), intent.Identity.StageConfigFilename) != nil || guard(ctx) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
	} else if inventory != gatewayRebindStageConfigInventoryExact {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	confirmed, err := d.stable(ctx, intent, guard)
	if err != nil || !gatewayRebindTypedStagePrefixMatches(intent, effect, confirmed) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	inventory, err = d.stageConfigInventory(ctx, effect, body)
	if err != nil || inventory != gatewayRebindStageConfigInventoryExact {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

func (d gatewayRebindTypedStageRuntime) serveStage(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (string, error) {
	if effect.StageStartIntent == nil || effect.StageServing != nil {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	value, err := d.stable(ctx, intent, guard)
	if err != nil {
		return "", err
	}
	if gatewayRebindTypedStoppedStageContainerMatches(intent, effect, value) {
		if guard(ctx) != nil || d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"container", "start", effect.StageContainer.ID) != nil || guard(ctx) != nil {
			return "", gatewayRebindEffectBoundaryError(ctx)
		}
	} else if !gatewayRebindTypedRunningStageMatches(intent, effect, value) {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	confirmed, err := d.stable(ctx, intent, guard)
	if err != nil || !gatewayRebindTypedRunningStageMatches(intent, effect, confirmed) {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	body, err := gatewayRebindTypedStageConfigBytes(intent)
	if err != nil {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	live, liveErr := d.manager.inspectLiveCaddyConfig(ctx, effect.StageContainer.ID)
	validConfig := liveErr == nil && sameCaddyConfig(body, live)
	clear(body)
	clear(live)
	if !validConfig || !proveGatewayRebindStagePublication(ctx, *effect.StageStartIntent,
		effect.StageContainer.ID, probeGatewayV2HostStatus, d.manager.probeGatewayV2ContainerChallenge) ||
		guard(ctx) != nil {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	endpoint := confirmed.StageRuntime.ConfiguredNetworks[intent.Identity.IngressNetwork].EndpointID
	if !validContainerID(endpoint) || normalizeID(endpoint) != endpoint {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	return endpoint, nil
}

func (d gatewayRebindTypedStageRuntime) finalConfigInventory(ctx context.Context,
	effect gatewayRebindTypedEffectProgress, expectedStage, expectedActive []byte,
) (gatewayRebindFinalConfigInventory, error) {
	if effect.StageContainer == nil {
		return 0, errors.New("typed stage container is missing")
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, "container", "cp",
		effect.StageContainer.ID+":/config/.", "-")
	if err != nil {
		return 0, err
	}
	defer clearResult(&result)
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return 0, errors.New("typed final config inventory is incomplete")
	}
	return gatewayRebindExactFinalConfigVolumeArchive(result.Stdout, expectedStage, expectedActive)
}

func (d gatewayRebindTypedStageRuntime) copyFinalConfig(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, checkpoint gatewayRebindPredecessorCheckpoint,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	if effect.FinalConfigIntent == nil || effect.FinalConfigCopy != nil || effect.StageServing == nil ||
		!gatewayRebindTypedFinalConfigContentMatches(intent, *effect.FinalConfigIntent) ||
		checkpoint.sourceRef() != intent.Predecessor {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	value, err := d.stable(ctx, intent, guard)
	if err != nil || !gatewayRebindTypedRunningStageMatches(intent, effect, value) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	stageBody, err := gatewayRebindTypedStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(stageBody)
	activeBody, err := gatewayRebindTypedFinalConfigBytes(intent, checkpoint,
		effect.FinalConfigIntent.RoutePlan)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	defer clear(activeBody)
	inventory, err := d.finalConfigInventory(ctx, effect, stageBody, activeBody)
	if err != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	if inventory == gatewayRebindFinalConfigInventoryStageOnly {
		if guard(ctx) != nil || d.manager.copyGatewayV2Config(ctx, effect.StageContainer.ID,
			append([]byte(nil), activeBody...), intent.Identity.ActiveConfigFilename) != nil || guard(ctx) != nil {
			return gatewayRebindEffectBoundaryError(ctx)
		}
	} else if inventory != gatewayRebindFinalConfigInventoryExactPair {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	confirmed, err := d.stable(ctx, intent, guard)
	if err != nil || !gatewayRebindTypedRunningStageMatches(intent, effect, confirmed) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	inventory, err = d.finalConfigInventory(ctx, effect, stageBody, activeBody)
	if err != nil || inventory != gatewayRebindFinalConfigInventoryExactPair || guard(ctx) != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return nil
}

var _ gatewayRebindTypedStageDriver = gatewayRebindTypedStageRuntime{}
