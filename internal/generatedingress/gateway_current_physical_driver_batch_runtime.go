package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
)

type gatewayCurrentLANRecoveryInventory struct {
	gatewayCurrentPhysicalInventory
	LiveState    *gatewayCurrentRouteState
	RestartState gatewayCurrentRouteState
}

func (d managerGatewayCurrentPhysicalRuntime) observeLANRecovery(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	if !validGatewayCurrentLANRecoveryPhysicalAction(action) || !gatewayCurrentLANRecoveryTargetMatches(action, target) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	first, err := d.readLANRecovery(ctx, action, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	defer clearGatewayCurrentLANRecoveryInventory(&first)
	second, err := d.readLANRecovery(ctx, action, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	defer clearGatewayCurrentLANRecoveryInventory(&second)
	if !reflect.DeepEqual(first, second) || ctx.Err() != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return d.gatewayCurrentLANRecoveryAttestation(ctx, action, target, second)
}

func (d managerGatewayCurrentPhysicalRuntime) reconcileLANRecovery(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
	guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if action.Complete || guard == nil || !validGatewayCurrentLANRecoveryPhysicalAction(action) ||
		!gatewayCurrentLANRecoveryTargetMatches(action, target) || ctx == nil || ctx.Err() != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	initial, err := d.readLANRecovery(ctx, action, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	stopped := !initial.Final.Running
	clearGatewayCurrentLANRecoveryInventory(&initial)
	if stopped {
		return d.reconcileStoppedLANRecovery(ctx, action, target, guard)
	}
	proof, err := d.observeLANRecovery(ctx, action, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	if absent, ok := gatewayCurrentLANRecoveryObservedProjection(action, proof.State); ok && absent {
		return proof, nil
	}
	withdrawnTarget := target
	withdrawnTarget.State = cloneGatewayCurrentRouteState(action.Withdrawn)
	effectGuard := func(effectCtx context.Context) error {
		if err := guard(effectCtx); err != nil || d.manager == nil || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		_, err := d.observeLANRecovery(effectCtx, action, target)
		return err
	}
	if err := effectGuard(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	body, err := gatewayCurrentPhysicalConfigBytes(action.Withdrawn)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := d.manager.copyGatewayV2Config(ctx, target.Resources.FinalContainer.ID, body,
		gatewayCurrentPhysicalConfigFilename); err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	for _, command := range gatewayCurrentPhysicalConfigCommands(withdrawnTarget) {
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, command...); err != nil {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
	defer cancel()
	proof, err = d.observeLANRecovery(proofCtx, action, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	absent, ok := gatewayCurrentLANRecoveryObservedProjection(action, proof.State)
	if !ok || !absent {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return proof, nil
}

func (d managerGatewayCurrentPhysicalRuntime) reconcileStoppedLANRecovery(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
	guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	stoppedGuard := func(effectCtx context.Context, requireWithdrawn bool) error {
		if err := guard(effectCtx); err != nil || d.manager == nil || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		inventory, err := d.readLANRecovery(effectCtx, action, target)
		if err != nil {
			return err
		}
		defer clearGatewayCurrentLANRecoveryInventory(&inventory)
		if inventory.Final.Running || inventory.Final.Restarting ||
			(requireWithdrawn && !reflect.DeepEqual(inventory.RestartState, action.Withdrawn)) {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		return nil
	}
	if err := stoppedGuard(ctx, false); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	body, err := gatewayCurrentPhysicalConfigBytes(action.Withdrawn)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	defer clear(body)
	copyErr := d.manager.copyGatewayV2Config(ctx, target.Resources.FinalContainer.ID, body,
		target.Identity.Rebind.ActiveConfigFilename)
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
	defer cancel()
	if err := stoppedGuard(proofCtx, true); err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	// A failed copy may be a lost acknowledgement. Exact stopped readback is
	// the authority; this withdrawal path never starts or publishes a listener.
	_ = copyErr
	proof, err := d.observeLANRecovery(proofCtx, action, target)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	absent, ok := gatewayCurrentLANRecoveryObservedProjection(action, proof.State)
	if !ok || !absent {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	return proof, nil
}

func (d managerGatewayCurrentPhysicalRuntime) readLANRecovery(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
) (gatewayCurrentLANRecoveryInventory, error) {
	invalid := func(value *gatewayCurrentLANRecoveryInventory) (gatewayCurrentLANRecoveryInventory, error) {
		clearGatewayCurrentLANRecoveryInventory(value)
		return gatewayCurrentLANRecoveryInventory{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil ||
		!validGatewayCurrentLANRecoveryPhysicalAction(action) || !gatewayCurrentLANRecoveryTargetMatches(action, target) {
		return gatewayCurrentLANRecoveryInventory{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	value := gatewayCurrentLANRecoveryInventory{}
	base, err := d.immutableInventoryValue(ctx, target)
	value.gatewayCurrentPhysicalInventory = base
	if err != nil || !d.validIngressMembership(target, value.Ingress, value.Final) {
		return invalid(&value)
	}
	value.RestartConfig, err = d.manager.inspectStoppedCaddyRestartConfig(ctx, value.Final.ID,
		target.Identity.Rebind.ActiveConfigFilename)
	if err != nil {
		return invalid(&value)
	}
	value.RestartState, err = gatewayCurrentLANRecoveryStateForConfig(action, value.RestartConfig)
	if err != nil {
		return invalid(&value)
	}
	if !value.Final.Running {
		return value, nil
	}
	value.ApplicationNetworks, err = d.applicationNetworkBindings(ctx, action.Before, value.Final, true)
	if err != nil || !d.validFinalNetworks(target, value.Final, value.FinalRuntime, value.IngressID,
		value.ApplicationNetworks) {
		return invalid(&value)
	}
	value.EndpointDigest, err = d.manager.inspectGatewayRouteEndpointProof(ctx,
		gatewayCurrentPhysicalRoutes(action.Before))
	if err != nil {
		return invalid(&value)
	}
	value.LiveConfig, err = d.manager.inspectLiveCaddyConfig(ctx, value.Final.ID)
	if err != nil {
		return invalid(&value)
	}
	live, liveErr := gatewayCurrentLANRecoveryStateForConfig(action, value.LiveConfig)
	if liveErr != nil {
		return invalid(&value)
	}
	value.LiveState = &live
	return value, nil
}

func (d managerGatewayCurrentPhysicalRuntime) gatewayCurrentLANRecoveryAttestation(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction, target gatewayCurrentPhysicalTarget,
	inventory gatewayCurrentLANRecoveryInventory,
) (gatewayCurrentPhysicalAttestation, error) {
	observed := inventory.RestartState
	if !inventory.Final.Running {
		observedTarget := target
		observedTarget.State = cloneGatewayCurrentRouteState(observed)
		return d.stoppedAttestation(ctx, []gatewayCurrentPhysicalTarget{observedTarget},
			inventory.Final, inventory.FinalRuntime)
	}
	if inventory.LiveState == nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	observed = *inventory.LiveState
	if !d.proveServing(ctx, observed, inventory.Final.ID) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if _, ok := gatewayCurrentLANRecoveryObservedProjection(action, observed); !ok {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	observedTarget := target
	observedTarget.State = cloneGatewayCurrentRouteState(observed)
	expected, err := gatewayCurrentPhysicalConfigBytes(observed)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	defer clear(expected)
	return d.attestationProof(ctx, observedTarget, inventory.gatewayCurrentPhysicalInventory,
		expected, gatewayCurrentPhysicalRecoveryMixed, false)
}

func gatewayCurrentLANRecoveryTargetMatches(action gatewayCurrentLANRecoveryPhysicalAction,
	target gatewayCurrentPhysicalTarget,
) bool {
	return validGatewayCurrentPhysicalTarget(target) && reflect.DeepEqual(target.State, action.Before) &&
		target.Lineage == action.Lineage && gatewayRebindAttemptTerminalViewSame(target.Terminal, action.Terminal) &&
		target.Pending == nil && reflect.DeepEqual(target.LANRecovery, action.Selected.LANRecovery)
}

func gatewayCurrentLANRecoveryStateForConfig(action gatewayCurrentLANRecoveryPhysicalAction,
	body []byte,
) (gatewayCurrentRouteState, error) {
	if !validGatewayCurrentLANRecoveryPhysicalAction(action) || len(body) == 0 {
		return gatewayCurrentRouteState{}, errors.New("invalid current LAN recovery config")
	}
	var config caddyConfig
	if err := json.Unmarshal(body, &config); err != nil {
		return gatewayCurrentRouteState{}, errors.New("invalid current LAN recovery config")
	}
	observed := cloneGatewayCurrentRouteState(action.Before)
	token, err := gatewayCurrentPhysicalProbeToken(observed)
	if err != nil {
		return gatewayCurrentRouteState{}, err
	}
	for _, item := range action.Selected.LANRecovery.Items {
		app := observed.Apps[item.AppID]
		if app.LAN == nil {
			continue
		}
		assignment := caddyV2LANAssignment{AppID: item.AppID, AllocationID: app.LAN.Raw.AllocationID,
			AccessRevisionID:     app.LAN.Raw.AccessRevisionID,
			AccessRevisionNumber: app.LAN.Raw.AccessRevisionNumber,
			AccessSpecDigest:     app.LAN.Raw.AccessSpecDigest}
		want := gatewayV2ProbeRoute(observed.Profile.SelectedIPv4,
			gatewayV2LANAppChallenge(token, app.LAN.Raw.Port, assignment))
		server := config.Apps.HTTP.Servers[lanServerName(app.LAN.Raw.Port)]
		found := false
		for _, route := range server.Routes {
			if reflect.DeepEqual(route, want) {
				if found {
					return gatewayCurrentRouteState{}, errors.New("duplicate current LAN recovery assignment")
				}
				found = true
			}
		}
		if !found {
			app.LAN = nil
			observed.Apps[item.AppID] = app
		}
	}
	observed.Digest = ""
	observed.Digest, err = gatewayCurrentRouteStateDigest(observed)
	if err != nil || !validGatewayCurrentRouteState(observed) {
		return gatewayCurrentRouteState{}, errors.New("invalid current LAN recovery projection")
	}
	if _, ok := gatewayCurrentLANRecoveryObservedProjection(action, observed); !ok {
		return gatewayCurrentRouteState{}, errors.New("unauthorized current LAN recovery projection")
	}
	expected, err := gatewayCurrentPhysicalConfigBytes(observed)
	if err != nil {
		return gatewayCurrentRouteState{}, err
	}
	defer clear(expected)
	if !sameCaddyConfig(expected, body) {
		return gatewayCurrentRouteState{}, errors.New("noncanonical current LAN recovery config")
	}
	return observed, nil
}

func clearGatewayCurrentLANRecoveryInventory(value *gatewayCurrentLANRecoveryInventory) {
	if value == nil {
		return
	}
	clearGatewayCurrentPhysicalInventory(&value.gatewayCurrentPhysicalInventory)
	*value = gatewayCurrentLANRecoveryInventory{}
}

var _ gatewayCurrentLANRecoveryRuntime = managerGatewayCurrentPhysicalRuntime{}
