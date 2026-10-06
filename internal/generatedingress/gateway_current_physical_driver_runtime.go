package generatedingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

const gatewayCurrentPhysicalConfigFilename = "reconcile.json"

type managerGatewayCurrentPhysicalRuntime struct {
	manager        *Manager
	hostProbe      gatewayV2HostStatusProbe
	containerProbe gatewayV2ContainerChallengeProbe
}

type gatewayCurrentPhysicalInventory struct {
	Image               imageInspection
	ConfigVolume        volumeInspection
	ConfigVolumeID      gatewayV1VolumeIdentity
	DataVolume          volumeInspection
	DataVolumeID        gatewayV1VolumeIdentity
	Ingress             caddyNetworkInspection
	IngressID           string
	Final               caddyInspection
	FinalRuntime        gatewayContainerRuntime
	ApplicationNetworks []gatewayRebindHandoverApplicationNetwork
	EndpointDigest      string
	LiveConfig          []byte
	RestartConfig       []byte
}

func (d managerGatewayCurrentPhysicalRuntime) reconcileInventory(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (caddyInspection, []gatewayRebindHandoverApplicationNetwork, map[string]string, error) {
	invalid := func() (caddyInspection, []gatewayRebindHandoverApplicationNetwork, map[string]string, error) {
		return caddyInspection{}, nil, nil, gatewayCurrentPhysicalDriverError(ctx)
	}
	value, err := d.immutableInventoryValue(ctx, target)
	if err != nil {
		return invalid()
	}
	defer clearGatewayCurrentPhysicalInventory(&value)
	if !value.Final.Running || value.Final.Restarting ||
		!d.validIngressMembership(target, value.Ingress, value.Final) {
		return invalid()
	}
	states, err := gatewayCurrentPhysicalEndpointStates(target)
	if err != nil {
		return invalid()
	}
	configs := make([][]byte, len(states))
	for index := range states {
		configs[index], err = gatewayCurrentPhysicalConfigBytes(states[index])
		if err != nil {
			for prior := 0; prior < index; prior++ {
				clear(configs[prior])
			}
			return invalid()
		}
		defer clear(configs[index])
	}
	value.LiveConfig, err = d.manager.inspectLiveCaddyConfig(ctx, value.Final.ID)
	if err != nil {
		return invalid()
	}
	value.RestartConfig, err = d.manager.inspectStoppedCaddyRestartConfig(ctx, value.Final.ID,
		target.Identity.Rebind.ActiveConfigFilename)
	if err != nil {
		return invalid()
	}
	liveIndex, liveFound, restartFound := 0, false, false
	for index := range configs {
		if sameCaddyConfig(configs[index], value.LiveConfig) {
			liveIndex, liveFound = index, true
		}
		restartFound = restartFound || sameCaddyConfig(configs[index], value.RestartConfig)
	}
	if !liveFound || !restartFound {
		return invalid()
	}
	allowedOwners := make(map[string]string)
	stateOwners := make([]map[string]string, len(states))
	for index, state := range states {
		owners, valid := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(state))
		if !valid {
			return invalid()
		}
		stateOwners[index] = owners
		for name, owner := range owners {
			if retained, exists := allowedOwners[name]; exists && retained != owner {
				return invalid()
			}
			allowedOwners[name] = owner
		}
	}
	current := make(map[string]string)
	ids := make(map[string]string, len(value.Final.Networks))
	ids[target.Identity.Rebind.IngressNetwork] = value.IngressID
	for name := range value.Final.Networks {
		if name == target.Identity.Rebind.IngressNetwork {
			continue
		}
		owner, allowed := allowedOwners[name]
		if !allowed {
			return invalid()
		}
		network, id, found, inspectErr := d.manager.inspectNamedGatewayNetwork(ctx, name)
		if inspectErr != nil || !found || !validContainerID(id) ||
			!validApplicationNetwork(network.identity(), owner) ||
			!validGatewayApplicationNetworkMembership(network, value.Final, name) {
			return invalid()
		}
		current[name], ids[name] = owner, normalizeID(id)
	}
	for name := range stateOwners[liveIndex] {
		if _, attached := current[name]; !attached {
			return invalid()
		}
	}
	if !gatewayCurrentPhysicalConfiguredNetworksMatch(target, value.Final, value.FinalRuntime, ids) {
		return invalid()
	}
	desired, err := d.applicationNetworkBindings(ctx, target.State, value.Final, false)
	if err != nil {
		return invalid()
	}
	if _, err := d.manager.inspectGatewayRouteEndpointProof(ctx, gatewayCurrentPhysicalRoutes(target.State)); err != nil {
		return invalid()
	}
	return value.Final, desired, current, nil
}

func (d managerGatewayCurrentPhysicalRuntime) observe(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	if d.manager == nil || d.hostProbe == nil || d.containerProbe == nil || ctx == nil || ctx.Err() != nil ||
		!validGatewayCurrentPhysicalTarget(target) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	first, err := d.read(ctx, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	defer clearGatewayCurrentPhysicalInventory(&first)
	second, err := d.read(ctx, target)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	defer clearGatewayCurrentPhysicalInventory(&second)
	if !reflect.DeepEqual(first, second) || ctx.Err() != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return d.attestation(ctx, target, second)
}

func (d managerGatewayCurrentPhysicalRuntime) reconcile(ctx context.Context,
	target gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) error {
	if d.manager == nil || guard == nil || !validGatewayCurrentPhysicalTarget(target) || ctx == nil || ctx.Err() != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(ctx); err != nil {
		return err
	}
	stopped, exactRestart, err := d.stoppedRecoveryInventory(ctx, target)
	if err != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if stopped {
		return d.restartStopped(ctx, target, guard, exactRestart)
	}
	container, desired, current, err := d.reconcileInventory(ctx, target)
	if err != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	effectGuard := func(effectCtx context.Context) error {
		if err := guard(effectCtx); err != nil || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		_, _, _, err := d.reconcileInventory(effectCtx, target)
		return err
	}
	desiredByName := make(map[string]gatewayRebindHandoverApplicationNetwork, len(desired))
	for _, item := range desired {
		desiredByName[item.Name] = item
	}
	targetOwners, validOwners := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(target.State))
	if !validOwners {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	// Attach every network used by the target routes before publishing a
	// configuration that can address endpoints on it.
	for _, binding := range desired {
		if _, exists := current[binding.Name]; exists {
			continue
		}
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"network", "connect", binding.ID, container.ID); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		current[binding.Name] = targetOwners[binding.Name]
	}
	body, err := gatewayCurrentPhysicalConfigBytes(target.State)
	if err != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := effectGuard(ctx); err != nil {
		clear(body)
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := d.manager.copyGatewayV2Config(ctx, target.Resources.FinalContainer.ID, body,
		gatewayCurrentPhysicalConfigFilename); err != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := effectGuard(ctx); err != nil {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	commands := gatewayCurrentPhysicalConfigCommands(target)
	if len(commands) != 4 {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	for _, command := range commands {
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, command...); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	// Withdraw only application networks that are no longer referenced by the
	// target route census, after the active and restart configs no longer name
	// them. Every candidate network is independently proved application-owned.
	stale := make([]string, 0)
	for name := range current {
		if _, keep := desiredByName[name]; !keep {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		network, id, found, inspectErr := d.manager.inspectNamedGatewayNetwork(ctx, name)
		owner := current[name]
		if inspectErr != nil || !found || !validContainerID(id) ||
			!validApplicationNetwork(network.identity(), owner) || !gatewayCurrentNetworkContains(network, container.ID) {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout,
			"network", "disconnect", id, container.ID); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		if err := effectGuard(ctx); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	if err := effectGuard(ctx); err != nil {
		return err
	}
	proof, err := d.observe(ctx, target)
	if err != nil || !gatewayCurrentPhysicalAttestationAtTarget(proof, target) {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	return nil
}

// restartStopped recovers an exact terminal-owned current container without
// ever starting an unknown or stale configuration. A known alternate
// Before/Effective restart file may be replaced while the container is
// stopped, but the replacement is read back exactly before start. Network
// membership must already equal the authorized target endpoint; an ambiguous
// union remains unresolved rather than being published.
func (d managerGatewayCurrentPhysicalRuntime) restartStopped(ctx context.Context,
	target gatewayCurrentPhysicalTarget, guard func(context.Context) error, exactRestart bool,
) error {
	stoppedGuard := func(effectCtx context.Context, requireExactRestart bool) error {
		if err := guard(effectCtx); err != nil || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		stopped, exact, err := d.stoppedRecoveryInventory(effectCtx, target)
		if err != nil || !stopped || (requireExactRestart && !exact) {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		// The stopped inventory includes Docker, restart-config, network, and
		// endpoint reads. Reconfirm authority after those reads so a change at
		// the end of inventory cannot cross the following copy or start boundary.
		if err := guard(effectCtx); err != nil || d.manager.gatewayRebindAdmissionBlocked() {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		return nil
	}
	if err := stoppedGuard(ctx, false); err != nil {
		return err
	}
	if !exactRestart {
		body, err := gatewayCurrentPhysicalConfigBytes(target.State)
		if err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		defer clear(body)
		if err := stoppedGuard(ctx, false); err != nil {
			return err
		}
		copyErr := d.manager.copyGatewayV2Config(ctx, target.Resources.FinalContainer.ID, body,
			target.Identity.Rebind.ActiveConfigFilename)
		proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
		defer cancel()
		if err := stoppedGuard(proofCtx, true); err != nil {
			return gatewayCurrentPhysicalDriverError(ctx)
		}
		// A failed copy may be a lost acknowledgement. Exact readback is the
		// authority; the command result alone never permits start.
		_ = copyErr
	}
	if err := stoppedGuard(ctx, true); err != nil {
		return err
	}
	command := gatewayCurrentPhysicalStartCommand(target)
	if len(command) != 3 {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	startErr := d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, command...)
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
	defer cancel()
	if err := guard(proofCtx); err != nil || d.manager.gatewayRebindAdmissionBlocked() {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	proof, proofErr := d.observe(proofCtx, target)
	if proofErr != nil || !gatewayCurrentPhysicalAttestationAtTarget(proof, target) ||
		proof.Outcome == gatewayCurrentPhysicalRecoveryStopped {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(proofCtx); err != nil || d.manager.gatewayRebindAdmissionBlocked() {
		return gatewayCurrentPhysicalDriverError(ctx)
	}
	// As with every other Docker effect, a command error is superseded only by
	// a complete exact physical proof under unchanged authority.
	_ = startErr
	return nil
}

func (d managerGatewayCurrentPhysicalRuntime) stoppedRecoveryInventory(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (bool, bool, error) {
	value, err := d.immutableInventoryValue(ctx, target)
	if err != nil {
		return false, false, err
	}
	defer clearGatewayCurrentPhysicalInventory(&value)
	if value.Final.Running {
		return false, false, nil
	}
	if value.Final.Restarting || gatewayV2HasEffectivePortBinding(value.FinalRuntime.EffectivePortBindings) ||
		len(value.Ingress.Containers) != 0 || !d.listenerAbsent(ctx, target.State) {
		return false, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	value.RestartConfig, err = d.manager.inspectStoppedCaddyRestartConfig(ctx, value.Final.ID,
		target.Identity.Rebind.ActiveConfigFilename)
	if err != nil {
		return false, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	exactRestart, knownRestart := gatewayCurrentPhysicalRestartConfigKnown(target, value.RestartConfig)
	if !knownRestart {
		return false, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	value.ApplicationNetworks, err = d.applicationNetworkBindings(ctx, target.State, value.Final, true)
	if err != nil || !d.validFinalNetworks(target, value.Final, value.FinalRuntime, value.IngressID,
		value.ApplicationNetworks) {
		return false, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	if _, err := d.manager.inspectGatewayRouteEndpointProof(ctx, gatewayCurrentPhysicalRoutes(target.State)); err != nil {
		return false, false, gatewayCurrentPhysicalDriverError(ctx)
	}
	return true, exactRestart, nil
}

func gatewayCurrentPhysicalRestartConfigKnown(target gatewayCurrentPhysicalTarget, body []byte) (bool, bool) {
	exactExpected, err := gatewayCurrentPhysicalConfigBytes(target.State)
	if err != nil {
		return false, false
	}
	exact := sameCaddyConfig(exactExpected, body)
	clear(exactExpected)
	if exact {
		return true, true
	}
	states, err := gatewayCurrentPhysicalEndpointStates(target)
	if err != nil {
		return false, false
	}
	for _, state := range states {
		if reflect.DeepEqual(state, target.State) {
			continue
		}
		expected, err := gatewayCurrentPhysicalConfigBytes(state)
		if err != nil {
			return false, false
		}
		matched := sameCaddyConfig(expected, body)
		clear(expected)
		if matched {
			return false, true
		}
	}
	return false, false
}

func gatewayCurrentPhysicalStartCommand(target gatewayCurrentPhysicalTarget) []string {
	if !validGatewayCurrentPhysicalTarget(target) || target.Resources.FinalContainer == nil {
		return nil
	}
	return []string{"container", "start", target.Resources.FinalContainer.ID}
}

func (d managerGatewayCurrentPhysicalRuntime) stop(ctx context.Context,
	targets []gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if d.manager == nil || guard == nil || ctx == nil || ctx.Err() != nil || !validGatewayCurrentPhysicalStopTargets(targets) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	target := targets[0]
	if err := guard(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	named, _, namedFound, namedErr := d.manager.inspectNamedGatewayContainer(ctx,
		target.Identity.Rebind.FinalContainer)
	if namedErr != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if !namedFound {
		_, _, idFound, idErr := d.manager.inspectNamedGatewayContainer(ctx, target.Resources.FinalContainer.ID)
		owned, ownedErr := d.ownedNamesFor(ctx, target, gatewayV2ManagedContainerLabel,
			"container", "ls", "--all")
		if idErr != nil || idFound || ownedErr != nil || len(owned) != 0 || !d.listenerAbsent(ctx, target.State) {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		proof, err := d.stoppedAttestation(ctx, targets, caddyInspection{}, gatewayContainerRuntime{})
		if err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		return proof, nil
	}
	if normalizeID(named.ID) != target.Resources.FinalContainer.ID {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	container, _, err := d.immutableInventory(ctx, target)
	if err != nil || normalizeID(container.ID) != target.Resources.FinalContainer.ID {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if container.Running {
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		result, stopErr := d.manager.run(ctx, d.manager.options.CommandTimeout,
			"container", "stop", "--time", "10", target.Resources.FinalContainer.ID)
		clearResult(&result)
		guardCtx, guardCancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
		guardErr := guard(guardCtx)
		guardCancel()
		if guardErr != nil {
			return gatewayCurrentPhysicalAttestation{}, guardErr
		}
		if stopErr != nil {
			// A lost stop acknowledgement is resolved only by the exact-ID
			// inspection below; the error itself grants no authority.
			proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
			defer cancel()
			observed, observedRuntime, found, inspectErr := d.manager.inspectNamedGatewayContainer(proofCtx,
				target.Identity.Rebind.FinalContainer)
			if inspectErr != nil || !found || observed.Running || !d.validFinalBase(target, observed, observedRuntime) {
				return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
			}
			container = observed
		}
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
	defer cancel()
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	confirmed, confirmedRuntime, err := d.immutableInventory(proofCtx, target)
	if err != nil || confirmed.Running || confirmed.Restarting ||
		gatewayV2HasEffectivePortBinding(confirmedRuntime.EffectivePortBindings) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	proof, err := d.stoppedAttestation(proofCtx, targets, confirmed, confirmedRuntime)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	return proof, nil
}

func (d managerGatewayCurrentPhysicalRuntime) read(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalInventory, error) {
	invalid := func(value *gatewayCurrentPhysicalInventory) (gatewayCurrentPhysicalInventory, error) {
		if value != nil {
			clearGatewayCurrentPhysicalInventory(value)
		}
		return gatewayCurrentPhysicalInventory{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	value, err := d.immutableInventoryValue(ctx, target)
	if err != nil {
		return invalid(&value)
	}
	if !d.validIngressMembership(target, value.Ingress, value.Final) {
		return invalid(&value)
	}
	value.ApplicationNetworks, err = d.applicationNetworkBindings(ctx, target.State, value.Final, true)
	if err != nil || !d.validFinalNetworks(target, value.Final, value.FinalRuntime, value.IngressID,
		value.ApplicationNetworks) {
		return invalid(&value)
	}
	routes := gatewayCurrentPhysicalRoutes(target.State)
	value.EndpointDigest, err = d.manager.inspectGatewayRouteEndpointProof(ctx, routes)
	if err != nil {
		return invalid(&value)
	}
	value.RestartConfig, err = d.manager.inspectStoppedCaddyRestartConfig(ctx, value.Final.ID,
		target.Identity.Rebind.ActiveConfigFilename)
	if err != nil {
		return invalid(&value)
	}
	if value.Final.Running {
		value.LiveConfig, err = d.manager.inspectLiveCaddyConfig(ctx, value.Final.ID)
		if err != nil {
			return invalid(&value)
		}
	}
	return value, nil
}

func (d managerGatewayCurrentPhysicalRuntime) immutableInventory(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (caddyInspection, gatewayContainerRuntime, error) {
	value, err := d.immutableInventoryValue(ctx, target)
	if err != nil {
		clearGatewayCurrentPhysicalInventory(&value)
		return caddyInspection{}, gatewayContainerRuntime{}, err
	}
	container, runtime := value.Final, value.FinalRuntime
	clearGatewayCurrentPhysicalInventory(&value)
	return container, runtime, nil
}

func (d managerGatewayCurrentPhysicalRuntime) immutableInventoryValue(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalInventory, error) {
	invalid := func(value *gatewayCurrentPhysicalInventory) (gatewayCurrentPhysicalInventory, error) {
		clearGatewayCurrentPhysicalInventory(value)
		return gatewayCurrentPhysicalInventory{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if d.manager == nil || ctx == nil || ctx.Err() != nil || !validGatewayCurrentPhysicalTarget(target) {
		return gatewayCurrentPhysicalInventory{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	value := gatewayCurrentPhysicalInventory{}
	var found bool
	var err error
	value.Image, found, err = d.inspectImage(ctx, target.Resources.ImageID)
	if err != nil || !found || normalizeID(value.Image.ID) != target.Resources.ImageID ||
		!validGatewayPinnedImage(value.Image, true) {
		return invalid(&value)
	}
	value.ConfigVolume, value.ConfigVolumeID, found, err = d.manager.inspectNamedVolumeWithIdentity(ctx,
		target.Identity.Rebind.ConfigVolume)
	if err != nil || !found || !d.validVolume(target, value.ConfigVolume, value.ConfigVolumeID,
		target.Resources.ConfigVolume, gatewayV2ConfigVolumeRole) {
		return invalid(&value)
	}
	value.DataVolume, value.DataVolumeID, found, err = d.manager.inspectNamedVolumeWithIdentity(ctx,
		target.Identity.Rebind.DataVolume)
	if err != nil || !found || !d.validVolume(target, value.DataVolume, value.DataVolumeID,
		target.Resources.DataVolume, gatewayV2DataVolumeRole) {
		return invalid(&value)
	}
	value.Ingress, value.IngressID, found, err = d.manager.inspectNamedGatewayNetwork(ctx,
		target.Identity.Rebind.IngressNetwork)
	if err != nil || !found || !d.validIngress(target, value.Ingress, value.IngressID) {
		return invalid(&value)
	}
	if _, _, stageFound, stageErr := d.manager.inspectNamedGatewayContainer(ctx,
		target.Identity.Rebind.StageContainer); stageErr != nil || stageFound {
		return invalid(&value)
	}
	containers, volumes, networks, err := d.ownedNames(ctx, target)
	if err != nil || !validOwnedNameSet(containers, target.Identity.Rebind.FinalContainer) ||
		!validOwnedNameSet(volumes, target.Identity.Rebind.ConfigVolume, target.Identity.Rebind.DataVolume) ||
		!validOwnedNameSet(networks, target.Identity.Rebind.IngressNetwork) ||
		!d.volumeUsersExact(ctx, target) {
		return invalid(&value)
	}
	value.Final, value.FinalRuntime, found, err = d.manager.inspectNamedGatewayContainer(ctx,
		target.Identity.Rebind.FinalContainer)
	if err != nil || !found || !d.validFinalBase(target, value.Final, value.FinalRuntime) {
		return invalid(&value)
	}
	return value, nil
}

func (d managerGatewayCurrentPhysicalRuntime) attestation(ctx context.Context,
	target gatewayCurrentPhysicalTarget, inventory gatewayCurrentPhysicalInventory,
) (gatewayCurrentPhysicalAttestation, error) {
	expected, err := gatewayCurrentPhysicalConfigBytes(target.State)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	defer clear(expected)
	outcome := gatewayCurrentPhysicalStableServing
	listenerAbsent := false
	if !inventory.Final.Running {
		if !sameCaddyConfig(expected, inventory.RestartConfig) ||
			!d.listenerAbsent(ctx, target.State) {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		outcome, listenerAbsent = gatewayCurrentPhysicalRecoveryStopped, true
	} else {
		if !sameCaddyConfig(expected, inventory.LiveConfig) {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		switch {
		case sameCaddyConfig(expected, inventory.RestartConfig):
			if target.Pending != nil || target.LANRecovery != nil {
				outcome = gatewayCurrentPhysicalRecoveryOutcome(target)
			}
		case gatewayCurrentPhysicalAlternateConfigMatches(target, inventory.RestartConfig):
			outcome = gatewayCurrentPhysicalRecoveryMixed
		default:
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
		if !d.proveServing(ctx, target.State, inventory.Final.ID) {
			return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
		}
	}
	return d.attestationProof(ctx, target, inventory, expected, outcome, listenerAbsent)
}

func (d managerGatewayCurrentPhysicalRuntime) attestationProof(ctx context.Context,
	target gatewayCurrentPhysicalTarget, inventory gatewayCurrentPhysicalInventory, expected []byte,
	outcome gatewayCurrentPhysicalOutcome, listenerAbsent bool,
) (gatewayCurrentPhysicalAttestation, error) {
	if !validGatewayCurrentPhysicalTarget(target) || len(expected) == 0 ||
		(outcome != gatewayCurrentPhysicalStableServing && outcome != gatewayCurrentPhysicalRecoveryBefore &&
			outcome != gatewayCurrentPhysicalRecoveryEffective && outcome != gatewayCurrentPhysicalRecoveryMixed &&
			outcome != gatewayCurrentPhysicalRecoveryStopped) ||
		listenerAbsent != (outcome == gatewayCurrentPhysicalRecoveryStopped) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	activeDigest := sha256.Sum256(expected)
	routeDigest, err := canonicalDigest(struct {
		Context string                             `json:"context"`
		Lineage appaccess.GatewayCurrentLineageRef `json:"lineage"`
		Apps    map[string]gatewayCurrentAppRoute  `json:"apps"`
	}{"hostd/generated-ingress/routes/current-physical/v1", target.Lineage, target.State.Apps})
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	applicationDigest, err := canonicalDigest(struct {
		Networks  []gatewayRebindHandoverApplicationNetwork `json:"networks"`
		Endpoints string                                    `json:"endpoints"`
	}{inventory.ApplicationNetworks, inventory.EndpointDigest})
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	listenerDigest, err := canonicalDigest(struct {
		StateDigest  string `json:"stateDigest"`
		ConfigDigest string `json:"configDigest"`
		Absent       bool   `json:"absent"`
	}{target.State.Digest, hex.EncodeToString(activeDigest[:]), listenerAbsent})
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	runtime := gatewayCurrentRuntimeProof{
		Profile: GatewayV2ProfileBinding{RevisionID: target.State.Profile.RevisionID,
			RevisionNumber: target.State.Profile.RevisionNumber, SpecDigest: target.State.Profile.SpecDigest,
			SelectedIPv4: target.State.Profile.SelectedIPv4, InterfaceID: target.State.Profile.InterfaceID,
			PortStart: target.State.Profile.PortStart, PortEnd: target.State.Profile.PortEnd},
		ImageID: target.Resources.ImageID, ContainerID: target.Resources.FinalContainer.ID,
		ActiveConfigDigest: hex.EncodeToString(activeDigest[:]), RoutePlanDigest: routeDigest,
		ListenerDigest: listenerDigest, ListenerAbsent: listenerAbsent,
		ApplicationNetworksDigest: applicationDigest,
	}
	runtime.Digest, err = gatewayCurrentRuntimeProofDigest(runtime)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	value := gatewayCurrentPhysicalAttestation{Outcome: outcome, State: cloneGatewayCurrentRouteState(target.State),
		Lineage: target.Lineage, Terminal: target.Terminal, Identity: target.Identity,
		Resources: target.Resources, Runtime: runtime, Pending: cloneGatewayCurrentPendingRoute(target.Pending),
		LANRecovery: cloneGatewayCurrentLANRecoveryBatch(target.LANRecovery)}
	value.Digest, err = gatewayCurrentPhysicalAttestationDigest(value)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return value, nil
}

func (d managerGatewayCurrentPhysicalRuntime) stoppedAttestation(ctx context.Context,
	targets []gatewayCurrentPhysicalTarget, container caddyInspection, runtime gatewayContainerRuntime,
) (gatewayCurrentPhysicalAttestation, error) {
	if !validGatewayCurrentPhysicalStopTargets(targets) || container.Running || container.Restarting ||
		gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	target := targets[0]
	var restart []byte
	var restartErr error
	if container.ID != "" {
		restart, restartErr = d.manager.inspectStoppedCaddyRestartConfig(ctx, container.ID,
			target.Identity.Rebind.ActiveConfigFilename)
	}
	if container.ID != "" && restartErr == nil {
		defer clear(restart)
		for _, candidate := range targets {
			expected, err := gatewayCurrentPhysicalConfigBytes(candidate.State)
			if err == nil && sameCaddyConfig(expected, restart) {
				target = candidate
				clear(expected)
				break
			}
			clear(expected)
		}
	}
	if !d.listenerAbsent(ctx, target.State) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	expected, err := gatewayCurrentPhysicalConfigBytes(target.State)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	defer clear(expected)
	activeDigest := sha256.Sum256(expected)
	routeDigest, err := canonicalDigest(struct {
		Context string                             `json:"context"`
		Lineage appaccess.GatewayCurrentLineageRef `json:"lineage"`
		Apps    map[string]gatewayCurrentAppRoute  `json:"apps"`
	}{"hostd/generated-ingress/routes/current-physical-withdrawal/v1", target.Lineage, target.State.Apps})
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	applicationDigest, err := canonicalDigest(struct {
		Context  string                                `json:"context"`
		Lineage  appaccess.GatewayCurrentLineageRef    `json:"lineage"`
		Networks map[string]gatewayV2ConfiguredNetwork `json:"networks"`
	}{"hostd/generated-ingress/routes/current-physical-withdrawal-networks/v1", target.Lineage,
		runtime.ConfiguredNetworks})
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	listenerDigest, err := canonicalDigest(struct {
		Context     string                             `json:"context"`
		Lineage     appaccess.GatewayCurrentLineageRef `json:"lineage"`
		StateDigest string                             `json:"stateDigest"`
		ContainerID string                             `json:"containerId"`
		Absent      bool                               `json:"absent"`
	}{"hostd/generated-ingress/routes/current-physical-withdrawal-listener/v1", target.Lineage,
		target.State.Digest, target.Resources.FinalContainer.ID, true})
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	proof := gatewayCurrentRuntimeProof{
		Profile: GatewayV2ProfileBinding{RevisionID: target.State.Profile.RevisionID,
			RevisionNumber: target.State.Profile.RevisionNumber, SpecDigest: target.State.Profile.SpecDigest,
			SelectedIPv4: target.State.Profile.SelectedIPv4, InterfaceID: target.State.Profile.InterfaceID,
			PortStart: target.State.Profile.PortStart, PortEnd: target.State.Profile.PortEnd},
		ImageID: target.Resources.ImageID, ContainerID: target.Resources.FinalContainer.ID,
		ActiveConfigDigest: hex.EncodeToString(activeDigest[:]), RoutePlanDigest: routeDigest,
		ListenerDigest: listenerDigest, ListenerAbsent: true, ApplicationNetworksDigest: applicationDigest,
	}
	proof.Digest, err = gatewayCurrentRuntimeProofDigest(proof)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	value := gatewayCurrentPhysicalAttestation{Outcome: gatewayCurrentPhysicalRecoveryStopped,
		State: cloneGatewayCurrentRouteState(target.State), Lineage: target.Lineage, Terminal: target.Terminal,
		Identity: target.Identity, Resources: target.Resources, Runtime: proof,
		Pending:     cloneGatewayCurrentPendingRoute(target.Pending),
		LANRecovery: cloneGatewayCurrentLANRecoveryBatch(target.LANRecovery)}
	value.Digest, err = gatewayCurrentPhysicalAttestationDigest(value)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return value, nil
}

func validGatewayCurrentPhysicalStopTargets(targets []gatewayCurrentPhysicalTarget) bool {
	if len(targets) == 0 || len(targets) > 2 || !validGatewayCurrentPhysicalTarget(targets[0]) {
		return false
	}
	first := targets[0]
	seen := map[string]struct{}{first.State.Digest: {}}
	for _, target := range targets[1:] {
		if !validGatewayCurrentPhysicalTarget(target) || target.Lineage != first.Lineage ||
			!reflect.DeepEqual(target.Terminal, first.Terminal) || !reflect.DeepEqual(target.Resources, first.Resources) ||
			!reflect.DeepEqual(target.Pending, first.Pending) || !reflect.DeepEqual(target.LANRecovery, first.LANRecovery) {
			return false
		}
		if _, duplicate := seen[target.State.Digest]; duplicate {
			return false
		}
		seen[target.State.Digest] = struct{}{}
	}
	return true
}

func (d managerGatewayCurrentPhysicalRuntime) inspectImage(ctx context.Context,
	id string,
) (imageInspection, bool, error) {
	var value imageInspection
	found, err := d.manager.inspectJSON(ctx, &value, "image", "inspect", "--format", imageInspectFormat, "sha256:"+id)
	return value, found, err
}

func (d managerGatewayCurrentPhysicalRuntime) ownedNames(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) ([]string, []string, []string, error) {
	containers, err := d.ownedNamesFor(ctx, target, gatewayV2ManagedContainerLabel, "container", "ls", "--all")
	if err != nil {
		return nil, nil, nil, err
	}
	volumes, err := d.ownedNamesFor(ctx, target, gatewayV2ManagedContainerLabel, "volume", "ls")
	if err != nil {
		return nil, nil, nil, err
	}
	networks, err := d.ownedNamesFor(ctx, target, gatewayV2ManagedNetworkLabel, "network", "ls")
	return containers, volumes, networks, err
}

func (d managerGatewayCurrentPhysicalRuntime) ownedNamesFor(ctx context.Context,
	target gatewayCurrentPhysicalTarget, managed string, args ...string,
) ([]string, error) {
	if d.manager == nil || !validGatewayCurrentPhysicalTarget(target) || len(args) < 2 {
		return nil, errors.New("invalid generated ingress current ownership inventory input")
	}
	format := "{{.Name}}"
	if args[0] == "container" {
		format = "{{.Names}}"
	}
	labels := gatewayCurrentPhysicalLabels(target, managed, "")
	delete(labels, gatewayV2ResourceRoleLabelKey)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--filter", "label="+key+"="+labels[key])
	}
	args = append(args, "--format", format)
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
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
			return nil, errors.New("generated ingress current ownership inventory is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("generated ingress current ownership inventory is duplicated")
		}
		seen[value] = struct{}{}
	}
	sort.Strings(values)
	return values, nil
}

func (d managerGatewayCurrentPhysicalRuntime) volumeUsersExact(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) bool {
	if d.manager == nil || target.Resources.FinalContainer == nil {
		return false
	}
	for _, volume := range []string{target.Identity.Rebind.ConfigVolume, target.Identity.Rebind.DataVolume} {
		result, err := d.manager.run(ctx, d.manager.options.CommandTimeout,
			"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+volume)
		truncated := result.StdoutTruncated || result.StderrTruncated
		ids, parseErr := parseGatewayV2DockerNetworkIDs(result.Stdout)
		clearResult(&result)
		if err != nil || truncated || parseErr != nil ||
			!equalStrings(ids, []string{target.Resources.FinalContainer.ID}) {
			return false
		}
	}
	return true
}

func (d managerGatewayCurrentPhysicalRuntime) validVolume(target gatewayCurrentPhysicalTarget,
	volume volumeInspection, identity gatewayV1VolumeIdentity, binding any, role string,
) bool {
	labels := gatewayCurrentPhysicalLabels(target, gatewayV2ManagedContainerLabel, role)
	if volume.Driver != "local" || volume.Scope != "local" || len(volume.Options) != 0 ||
		!reflect.DeepEqual(volume.Labels, labels) {
		return false
	}
	var name, mountpoint, createdAt, ownership string
	switch value := binding.(type) {
	case gatewayRebindStageConfigVolumeBinding:
		name, mountpoint, createdAt, ownership = value.Name, value.Mountpoint, value.CreatedAt, value.OwnershipDigest
	case gatewayRebindStageDataVolumeBinding:
		name, mountpoint, createdAt, ownership = value.Name, value.Mountpoint, value.CreatedAt, value.OwnershipDigest
	default:
		return false
	}
	digest, err := canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Driver  string            `json:"driver"`
		Scope   string            `json:"scope"`
		Options map[string]string `json:"options"`
		Labels  map[string]string `json:"labels"`
	}{1, name, "local", "local", map[string]string{}, labels})
	return err == nil && volume.Name == name && identity.Mountpoint == mountpoint &&
		identity.CreatedAt == createdAt && digest == ownership
}

func (d managerGatewayCurrentPhysicalRuntime) validIngress(target gatewayCurrentPhysicalTarget,
	network caddyNetworkInspection, id string,
) bool {
	identity := target.Identity.Rebind
	labels := gatewayCurrentPhysicalLabels(target, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole)
	bridge := "rig" + identity.Digest[:12]
	digest, err := canonicalDigest(struct {
		Version int                                 `json:"version"`
		Name    string                              `json:"name"`
		Bridge  string                              `json:"bridge"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
		Labels  map[string]string                   `json:"labels"`
	}{1, identity.IngressNetwork, bridge, gatewayRebindSuccessorIntentNetwork(target.State.Network), labels})
	return err == nil && id == target.Resources.IngressNetwork.ID && digest == target.Resources.IngressNetwork.OwnershipDigest &&
		network.Name == identity.IngressNetwork && network.Driver == "bridge" && network.Scope == "local" && !network.Internal &&
		reflect.DeepEqual(network.Options, map[string]string{gatewayRebindBridgeNameOptionKey: bridge}) &&
		len(network.IPAM.Config) == 1 && network.IPAM.Config[0] == (networkIPAM{Subnet: target.State.Network.Subnet,
		Gateway: target.State.Network.GatewayIPv4}) && reflect.DeepEqual(network.Labels, labels)
}

func (d managerGatewayCurrentPhysicalRuntime) validFinalBase(target gatewayCurrentPhysicalTarget,
	container caddyInspection, runtime gatewayContainerRuntime,
) bool {
	if target.Resources.FinalContainer == nil || target.Identity.Rebind == nil {
		return false
	}
	identity := target.Identity.Rebind
	labels := gatewayCurrentPhysicalLabels(target, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)
	ownership, err := canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Labels  map[string]string `json:"labels"`
	}{1, identity.FinalContainer, labels})
	configuration, configurationErr := gatewayCurrentPhysicalInitialConfigurationDigest(target,
		d.manager.options.HostPort)
	if err != nil || configurationErr != nil || ownership != target.Resources.FinalContainer.OwnershipDigest ||
		configuration != target.Resources.FinalContainer.ConfigurationDigest ||
		normalizeID(container.ID) != target.Resources.FinalContainer.ID || normalizeID(container.Image) != target.Resources.ImageID ||
		strings.TrimPrefix(container.Name, "/") != identity.FinalContainer || container.Hostname != identity.FinalHostname ||
		container.User != "1000:1000" || container.NetworkMode != identity.IngressNetwork || !exactGatewayV2Environment(container.Env) ||
		!container.ReadOnly || container.Privileged || !onlyCaddyCapability(container.CapAdd) || !exactFoldSet(container.CapDrop, "ALL") ||
		!onlyNoNewPrivileges(container.SecurityOpt) || len(container.Binds) != 0 || len(container.Tmpfs) != 0 ||
		container.Memory != 268435456 || container.MemorySwap != 268435456 || container.NanoCPUs != 1_000_000_000 ||
		container.PIDsLimit != 128 || container.LogType != "local" || len(container.LogConfig) != 2 ||
		container.LogConfig["max-size"] != "10m" || container.LogConfig["max-file"] != "3" ||
		container.Restart != gatewayV2FinalRestartPolicy || container.Restarting || runtime.Paused || runtime.Dead ||
		len(container.Entrypoint) != 1 || container.Entrypoint[0] != caddyExecutable || len(container.Cmd) != 3 ||
		container.Cmd[0] != "run" || container.Cmd[1] != "--config" || container.Cmd[2] != "/config/"+identity.ActiveConfigFilename ||
		len(container.Ulimits) != 1 || container.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!reflect.DeepEqual(container.Labels, labels) || !validGatewayRebindStageContainerMounts(container.Mounts, *identity) ||
		!gatewayCurrentPhysicalPortBindings(container.PortBindings, target.State.Profile, d.manager.options.HostPort) {
		return false
	}
	if container.Running {
		return gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, container.PortBindings)
	}
	return !gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)
}

// gatewayCurrentPhysicalInitialConfigurationDigest rederives the immutable
// create-time container configuration retained by the terminal. Its network
// roster is historical create evidence only. Live application-network
// membership is validated separately from the selected current State.Apps.
func gatewayCurrentPhysicalInitialConfigurationDigest(target gatewayCurrentPhysicalTarget,
	localHostPort uint16,
) (string, error) {
	if !validGatewayCurrentPhysicalTarget(target) || localHostPort == 0 || target.Identity.Rebind == nil {
		return "", errors.New("invalid generated ingress current initial configuration")
	}
	identity := target.Identity.Rebind
	profile := target.Terminal.SuccessorProfile
	args := []string{
		"container", "create", "--name", identity.FinalContainer, "--hostname", identity.FinalHostname,
		"--network", "name=" + identity.IngressNetwork + ",ip=" + target.State.Network.ContainerIPv4 + ",gw-priority=1",
		"--mount", "type=volume,src=" + identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", gatewayV2FinalRestartPolicy,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for _, network := range target.Resources.ApplicationNetworks {
		args = append(args, "--network", "name="+network.Name)
	}
	for port := profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", profile.SelectedIPv4+":"+text+":"+text+"/tcp")
		if port == profile.PortEnd {
			break
		}
	}
	args = append(args, "--publish", "127.0.0.1:"+strconv.FormatUint(uint64(localHostPort), 10)+":"+
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp")
	args = appendGatewayV2Labels(args,
		gatewayCurrentPhysicalLabels(target, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole))
	args = append(args, "sha256:"+target.Resources.ImageID, "run", "--config", "/config/"+identity.ActiveConfigFilename)
	return canonicalDigest(struct {
		Version  int                                       `json:"version"`
		Args     []string                                  `json:"args"`
		Networks []gatewayRebindHandoverApplicationNetwork `json:"networks"`
	}{1, args, target.Resources.ApplicationNetworks})
}

func (d managerGatewayCurrentPhysicalRuntime) validFinalNetworks(target gatewayCurrentPhysicalTarget,
	container caddyInspection, runtime gatewayContainerRuntime, ingressID string,
	applications []gatewayRebindHandoverApplicationNetwork,
) bool {
	expected := map[string]string{target.Identity.Rebind.IngressNetwork: ingressID}
	for _, item := range applications {
		expected[item.Name] = item.ID
	}
	if len(runtime.ConfiguredNetworks) != len(expected) || (container.Running && len(container.Networks) != len(expected)) ||
		(!container.Running && len(container.Networks) != 0 && len(container.Networks) != len(expected)) {
		return false
	}
	for name, id := range expected {
		configured, exists := runtime.ConfiguredNetworks[name]
		priority := 0
		if name == target.Identity.Rebind.IngressNetwork {
			priority = caddyGatewayPriority
		}
		if !exists || configured.GwPriority != priority || configured.IPv6Gateway != "" ||
			!validGatewayV2StoppedNetworkReference(configured.NetworkID, id) {
			return false
		}
		if name == target.Identity.Rebind.IngressNetwork {
			if configured.IPAMConfig == nil || configured.IPAMConfig.IPv4Address != target.State.Network.ContainerIPv4 ||
				configured.IPAMConfig.IPv6Address != "" {
				return false
			}
		} else if configured.IPAMConfig != nil && *configured.IPAMConfig != (gatewayV2ConfiguredIPAM{}) {
			return false
		}
		if container.Running {
			address, err := netip.ParseAddr(configured.IPAddress)
			if err != nil || !address.Is4() || address.IsUnspecified() || address.IsMulticast() ||
				configured.NetworkID != id || !validContainerID(configured.EndpointID) {
				return false
			}
			attachment := container.Networks[name]
			if attachment == nil || attachment.IPAddress != configured.IPAddress || attachment.GwPriority != priority ||
				attachment.IPv6Gateway != "" {
				return false
			}
		} else if configured.EndpointID != "" || configured.IPAddress != "" {
			return false
		}
	}
	return true
}

func (d managerGatewayCurrentPhysicalRuntime) validIngressMembership(target gatewayCurrentPhysicalTarget,
	network caddyNetworkInspection, container caddyInspection,
) bool {
	if target.Identity.Rebind == nil {
		return false
	}
	if !container.Running {
		return len(network.Containers) == 0
	}
	if len(network.Containers) != 1 ||
		!validGatewayApplicationNetworkMembership(network, container, target.Identity.Rebind.IngressNetwork) {
		return false
	}
	prefix, err := netip.ParsePrefix(target.State.Network.Subnet)
	if err != nil {
		return false
	}
	for memberID, member := range network.Containers {
		address, addressErr := netip.ParsePrefix(member.IPv4Address)
		return addressErr == nil && normalizeID(memberID) == normalizeID(container.ID) &&
			member.Name == target.Identity.Rebind.FinalContainer &&
			address.Addr().String() == target.State.Network.ContainerIPv4 && address.Bits() == prefix.Bits()
	}
	return false
}

func gatewayCurrentPhysicalConfiguredNetworksMatch(target gatewayCurrentPhysicalTarget,
	container caddyInspection, runtime gatewayContainerRuntime, ids map[string]string,
) bool {
	if target.Identity.Rebind == nil || len(ids) != len(container.Networks) || len(ids) != len(runtime.ConfiguredNetworks) {
		return false
	}
	for name, id := range ids {
		configured, exists := runtime.ConfiguredNetworks[name]
		attachment := container.Networks[name]
		priority := 0
		if name == target.Identity.Rebind.IngressNetwork {
			priority = caddyGatewayPriority
		}
		if !exists || attachment == nil || configured.NetworkID != id || !validContainerID(configured.EndpointID) ||
			configured.IPAddress == "" || configured.IPAddress != attachment.IPAddress ||
			configured.GwPriority != priority || attachment.GwPriority != priority ||
			configured.IPv6Gateway != "" || attachment.IPv6Gateway != "" {
			return false
		}
		if name == target.Identity.Rebind.IngressNetwork {
			if configured.IPAMConfig == nil ||
				configured.IPAMConfig.IPv4Address != target.State.Network.ContainerIPv4 ||
				configured.IPAMConfig.IPv6Address != "" {
				return false
			}
		} else if configured.IPAMConfig != nil && *configured.IPAMConfig != (gatewayV2ConfiguredIPAM{}) {
			return false
		}
	}
	return true
}

func gatewayCurrentPhysicalEndpointStates(target gatewayCurrentPhysicalTarget) ([]gatewayCurrentRouteState, error) {
	if !validGatewayCurrentPhysicalTarget(target) {
		return nil, errors.New("invalid generated ingress current endpoint target")
	}
	if target.Pending == nil {
		return []gatewayCurrentRouteState{cloneGatewayCurrentRouteState(target.State)}, nil
	}
	before := cloneGatewayCurrentRouteState(target.State)
	if target.Pending.Previous == nil {
		delete(before.Apps, target.Pending.AppID)
	} else {
		before.Apps[target.Pending.AppID] = cloneGatewayCurrentAppRoute(*target.Pending.Previous)
	}
	before.Digest = ""
	var err error
	before.Digest, err = gatewayCurrentRouteStateDigest(before)
	if err != nil || !validGatewayCurrentRouteState(before) {
		return nil, errors.New("invalid generated ingress current before endpoint")
	}
	effective := cloneGatewayCurrentRouteState(target.State)
	effective.Apps[target.Pending.AppID] = cloneGatewayCurrentAppRoute(target.Pending.Proposed)
	effective.Digest = ""
	effective.Digest, err = gatewayCurrentRouteStateDigest(effective)
	if err != nil || !validGatewayCurrentRouteState(effective) {
		return nil, errors.New("invalid generated ingress current effective endpoint")
	}
	if reflect.DeepEqual(before, effective) {
		return nil, errors.New("generated ingress current endpoints are identical")
	}
	return []gatewayCurrentRouteState{before, effective}, nil
}

func (d managerGatewayCurrentPhysicalRuntime) applicationNetworkBindings(ctx context.Context,
	state gatewayCurrentRouteState, container caddyInspection, validateFinal bool,
) ([]gatewayRebindHandoverApplicationNetwork, error) {
	routes := gatewayCurrentPhysicalRoutes(state)
	owners, ok := gatewayRouteNetworkOwners(routes)
	if !ok {
		return nil, errors.New("invalid generated ingress current application network owners")
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]gatewayRebindHandoverApplicationNetwork, 0, len(names))
	for _, name := range names {
		network, id, found, err := d.manager.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || !validContainerID(id) || !validApplicationNetwork(network.identity(), owners[name]) {
			return nil, errors.New("invalid generated ingress current application network")
		}
		if validateFinal {
			member := gatewayCurrentNetworkContains(network, container.ID)
			if (container.Running && (!member || !validGatewayApplicationNetworkMembership(network, container, name))) ||
				(!container.Running && member) {
				return nil, errors.New("generated ingress current application network final membership changed")
			}
		}
		result = append(result, gatewayRebindHandoverApplicationNetwork{Name: name, ID: normalizeID(id)})
	}
	return result, nil
}

func (d managerGatewayCurrentPhysicalRuntime) proveServing(ctx context.Context,
	state gatewayCurrentRouteState, containerID string,
) bool {
	token, err := gatewayCurrentPhysicalProbeToken(state)
	if err != nil || !d.manager.proveGatewayRouteEndpointTransports(ctx, containerID, gatewayCurrentPhysicalRoutes(state)) {
		return false
	}
	assignments := gatewayCurrentPhysicalAssignments(state)
	for port := state.Profile.PortStart; ; port++ {
		portChallenge := gatewayV2PortChallenge(token, port)
		if !exactGatewayV2HostChallenge(ctx, d.hostProbe, state.Profile.SelectedIPv4, port,
			state.Profile.SelectedIPv4, portChallenge) ||
			!d.containerProbe(ctx, containerID, state.Network.ContainerIPv4, port,
				state.Profile.SelectedIPv4, portChallenge) ||
			!exactGatewayV2HostStatus(ctx, d.hostProbe, state.Profile.SelectedIPv4, port,
				"wrong.invalid", http.StatusNotFound) || gatewayV2LoopbackPublished(ctx, d.hostProbe, port, state.Profile.SelectedIPv4) {
			return false
		}
		root := d.hostProbe(ctx, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, "/")
		if ctx.Err() != nil || !root.Connected || !root.Responded {
			return false
		}
		if assignment, exists := assignments[port]; exists {
			challenge := gatewayV2LANAppChallenge(token, port, assignment)
			if !exactGatewayV2HostChallenge(ctx, d.hostProbe, state.Profile.SelectedIPv4, port,
				state.Profile.SelectedIPv4, challenge) || !d.containerProbe(ctx, containerID,
				state.Network.ContainerIPv4, port, state.Profile.SelectedIPv4, challenge) {
				return false
			}
		} else if root.Status != http.StatusNotFound {
			return false
		}
		if port == state.Profile.PortEnd {
			break
		}
	}
	if result := d.hostProbe(ctx, "127.0.0.1", d.manager.options.HostPort, "wrong.invalid", "/"); ctx.Err() != nil || !result.Connected || !result.Responded || result.Status != http.StatusNotFound {
		return false
	}
	appIDs := make([]string, 0, len(state.Apps))
	for appID := range state.Apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	for _, appID := range appIDs {
		challenge := gatewayV2AppChallenge(token, appID)
		result := d.hostProbe(ctx, "127.0.0.1", d.manager.options.HostPort, appID+".rig.localhost",
			gatewayV2ChallengePathPrefix+challenge)
		if ctx.Err() != nil || !result.Connected || !result.Responded || result.Status != http.StatusNotFound ||
			result.Body != gatewayV2ChallengeBodyPrefix+challenge {
			return false
		}
	}
	return true
}

func (d managerGatewayCurrentPhysicalRuntime) listenerAbsent(ctx context.Context,
	state gatewayCurrentRouteState,
) bool {
	for port := state.Profile.PortStart; ; port++ {
		if result := d.hostProbe(ctx, state.Profile.SelectedIPv4, port, state.Profile.SelectedIPv4, "/"); ctx.Err() != nil || result.Connected || result.Responded {
			return false
		}
		if port == state.Profile.PortEnd {
			break
		}
	}
	result := d.hostProbe(ctx, "127.0.0.1", d.manager.options.HostPort, "wrong.invalid", "/")
	return ctx.Err() == nil && !result.Connected && !result.Responded
}

func gatewayCurrentPhysicalConfigBytes(state gatewayCurrentRouteState) ([]byte, error) {
	if !validGatewayCurrentRouteState(state) || state.Pending != nil || state.LANRecovery != nil {
		return nil, errors.New("invalid generated ingress current config state")
	}
	token, err := gatewayCurrentPhysicalProbeToken(state)
	if err != nil {
		return nil, err
	}
	body, err := buildCaddyConfigV2(gatewayCurrentPhysicalRoutes(state),
		net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart,
			PortEnd: state.Profile.PortEnd, ProbeToken: token}, gatewayCurrentPhysicalAssignments(state))
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes {
		clear(body)
		return nil, errors.New("invalid generated ingress current config")
	}
	return body, nil
}

func gatewayCurrentPhysicalConfigCommands(target gatewayCurrentPhysicalTarget) [][]string {
	if !validGatewayCurrentPhysicalTarget(target) || target.Resources.FinalContainer == nil ||
		target.Identity.Rebind == nil {
		return nil
	}
	containerID := target.Resources.FinalContainer.ID
	path := "/config/" + gatewayCurrentPhysicalConfigFilename
	return [][]string{
		{"container", "exec", containerID, "caddy", "validate", "--config", path},
		{"container", "exec", containerID, "caddy", "reload", "--config", path},
		{"container", "exec", "--user", "0:0", containerID, "cp", path, "/config/active.next.json"},
		{"container", "exec", "--user", "0:0", containerID,
			"mv", "/config/active.next.json", "/config/" + target.Identity.Rebind.ActiveConfigFilename},
	}
}

func gatewayCurrentPhysicalProbeToken(state gatewayCurrentRouteState) (string, error) {
	if !validGatewayCurrentRouteState(state) || state.Identity.Rebind == nil {
		return "", errors.New("invalid generated ingress current probe state")
	}
	networkDigest, err := gatewayCurrentPhysicalNetworkDigest(state.Network)
	if err != nil {
		return "", err
	}
	return gatewayRebindStageConfigProbeTokenV1(state.Lineage.ProtectedIntentDigest, state.Lineage.OperationID,
		state.Identity.Rebind.Digest, networkDigest)
}

func gatewayCurrentPhysicalNetworkDigest(plan gatewayV2NetworkPlan) (string, error) {
	if !validGatewayV2NetworkPlan(plan) {
		return "", errors.New("invalid generated ingress current network plan")
	}
	return canonicalDigest(struct {
		Version int                                 `json:"version"`
		Action  string                              `json:"action"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
	}{1, "rebind-successor-network", gatewayRebindSuccessorIntentNetwork(plan)})
}

func gatewayCurrentPhysicalRoutes(state gatewayCurrentRouteState) map[string]routeRecord {
	routes := make(map[string]routeRecord, len(state.Apps))
	for appID, app := range state.Apps {
		route := app.Route
		route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
		routes[appID] = route
	}
	return routes
}

func gatewayCurrentPhysicalAssignments(state gatewayCurrentRouteState) map[uint16]caddyV2LANAssignment {
	result := make(map[uint16]caddyV2LANAssignment)
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		result[app.LAN.Raw.Port] = caddyV2LANAssignment{AppID: appID, AllocationID: app.LAN.Raw.AllocationID,
			AccessRevisionID: app.LAN.Raw.AccessRevisionID, AccessRevisionNumber: app.LAN.Raw.AccessRevisionNumber,
			AccessSpecDigest: app.LAN.Raw.AccessSpecDigest}
	}
	return result
}

func gatewayCurrentPhysicalLabels(target gatewayCurrentPhysicalTarget, managed, role string) map[string]string {
	networkDigest, _ := gatewayCurrentPhysicalNetworkDigest(target.State.Network)
	return map[string]string{
		gatewayV2ManagedLabelKey:          managed,
		gatewayV2IdentityLabelKey:         gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey:        target.Lineage.OperationID,
		gatewayV2IdentityDigestLabelKey:   target.Lineage.ProtectedIdentityDigest,
		gatewayV2PlanDigestLabelKey:       networkDigest,
		gatewayV2ResourceRoleLabelKey:     role,
		gatewayRebindIntentDigestLabelKey: target.Lineage.ProtectedIntentDigest,
		gatewayRebindGenerationLabelKey:   strconv.FormatUint(target.Lineage.ProtectedGeneration, 10),
	}
}

func gatewayCurrentPhysicalPortBindings(actual map[string][]map[string]string,
	profile gatewayProfileBinding, localPort uint16,
) bool {
	if localPort == 0 || len(actual) != int(profile.PortEnd-profile.PortStart)+2 {
		return false
	}
	for port := profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		binding := actual[text+"/tcp"]
		if len(binding) != 1 || len(binding[0]) != 2 || binding[0]["HostIp"] != profile.SelectedIPv4 ||
			binding[0]["HostPort"] != text {
			return false
		}
		if port == profile.PortEnd {
			break
		}
	}
	loopback := actual[strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp"]
	return len(loopback) == 1 && len(loopback[0]) == 2 && loopback[0]["HostIp"] == "127.0.0.1" &&
		loopback[0]["HostPort"] == strconv.FormatUint(uint64(localPort), 10)
}

func gatewayCurrentPhysicalAlternateConfigMatches(target gatewayCurrentPhysicalTarget, actual []byte) bool {
	if target.Pending == nil {
		return false
	}
	state := cloneGatewayCurrentRouteState(target.State)
	if reflect.DeepEqual(state.Apps[target.Pending.AppID], target.Pending.Proposed) {
		if target.Pending.Previous == nil {
			delete(state.Apps, target.Pending.AppID)
		} else {
			state.Apps[target.Pending.AppID] = cloneGatewayCurrentAppRoute(*target.Pending.Previous)
		}
	} else {
		state.Apps[target.Pending.AppID] = cloneGatewayCurrentAppRoute(target.Pending.Proposed)
	}
	state.Digest = ""
	var err error
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		return false
	}
	expected, err := gatewayCurrentPhysicalConfigBytes(state)
	if err != nil {
		return false
	}
	defer clear(expected)
	return sameCaddyConfig(expected, actual)
}

func gatewayCurrentPhysicalRecoveryOutcome(target gatewayCurrentPhysicalTarget) gatewayCurrentPhysicalOutcome {
	if target.LANRecovery != nil {
		return gatewayCurrentPhysicalRecoveryMixed
	}
	if target.Pending == nil {
		return gatewayCurrentPhysicalStableServing
	}
	current, exists := target.State.Apps[target.Pending.AppID]
	if exists && reflect.DeepEqual(current, target.Pending.Proposed) {
		return gatewayCurrentPhysicalRecoveryEffective
	}
	if exists == (target.Pending.Previous != nil) &&
		(target.Pending.Previous == nil || reflect.DeepEqual(current, *target.Pending.Previous)) {
		return gatewayCurrentPhysicalRecoveryBefore
	}
	return gatewayCurrentPhysicalRecoveryMixed
}

func validGatewayCurrentPhysicalTarget(target gatewayCurrentPhysicalTarget) bool {
	return validGatewayCurrentRouteState(target.State) && target.State.Pending == nil && target.State.LANRecovery == nil &&
		target.Lineage == target.State.Lineage && validGatewayCurrentIdentity(target.Lineage, target.Identity) &&
		gatewayRebindAttemptTerminalMatchesLineage(target.Terminal, target.Lineage) &&
		reflect.DeepEqual(target.Resources, target.Terminal.Resources) && target.Resources.FinalContainer != nil &&
		reflect.DeepEqual(target.Pending, cloneGatewayCurrentPendingRoute(target.Pending)) &&
		reflect.DeepEqual(target.LANRecovery, cloneGatewayCurrentLANRecoveryBatch(target.LANRecovery))
}

func gatewayCurrentNetworkContains(network caddyNetworkInspection, id string) bool {
	for member := range network.Containers {
		if normalizeID(member) == normalizeID(id) {
			return true
		}
	}
	return false
}

func clearGatewayCurrentPhysicalInventory(value *gatewayCurrentPhysicalInventory) {
	if value == nil {
		return
	}
	clear(value.LiveConfig)
	clear(value.RestartConfig)
	clearCaddyNetworkInspection(&value.Ingress)
	*value = gatewayCurrentPhysicalInventory{}
}

var _ gatewayCurrentPhysicalRuntime = managerGatewayCurrentPhysicalRuntime{}
