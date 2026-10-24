package generatedingress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainBootstrapReceipt struct {
	image          imageInspection
	volume         volumeInspection
	volumeIdentity gatewayV1VolumeIdentity
	network        caddyNetworkInspection
	networkID      string
	ingressIP      string
	container      caddyInspection
	containerRun   gatewayContainerRuntime
	containerID    string
	database       *deployments.GeneratedDomainStartupSnapshot
	protected      *generatedDomainProtectedStartupSnapshot
	inventories    map[string][]string
	stateInstalled bool
	started        bool
}

func bootstrapGeneratedDomainEmpty(
	ctx context.Context,
	manager *Manager,
	input generatedDomainReservationInput,
	physical generatedDomainPhysicalIdentity,
) error {
	receipt := &generatedDomainBootstrapReceipt{}
	if err := receipt.captureOptionalImage(ctx, manager); err != nil {
		return err
	}
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		return err
	}
	if receipt.image.ID == "" {
		result, err := manager.run(ctx, manager.options.PullTimeout, "image", "pull", caddyImage)
		if err != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
			clearResult(&result)
			return errors.New("generated domain pinned image pull was not acknowledged")
		}
		clearResult(&result)
		if err := receipt.captureRequiredImage(ctx, manager); err != nil {
			return err
		}
		if err := receipt.acceptInventoryEffect("image", receipt.image.ID); err != nil {
			return err
		}
		if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
			return err
		}
	}

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		clear(nonceBytes)
		return errors.New("generated domain bootstrap nonce is unavailable")
	}
	nonce := hex.EncodeToString(nonceBytes)
	clear(nonceBytes)
	if !validGeneratedDomainBootstrapVolumeNonce(nonce) {
		return errors.New("generated domain bootstrap nonce is invalid")
	}
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		return err
	}
	result, err := manager.run(ctx, manager.options.CommandTimeout,
		"volume", "create", "--driver", "local",
		"--label", gatewayV2ManagedLabelKey+"="+gatewayV2ManagedContainerLabel,
		"--label", gatewayV2IdentityLabelKey+"="+gatewayV1IdentityVersion,
		"--label", generatedDomainBootstrapVolumeNonceLabel+"="+nonce,
		caddyVolumeName)
	if err != nil || !generatedDomainAcknowledgedIdentity(result, caddyVolumeName) {
		clearResult(&result)
		return errors.New("generated domain bootstrap volume create was not acknowledged")
	}
	clearResult(&result)
	volume, volumeIdentity, found, err := manager.inspectNamedVolumeWithIdentity(ctx, caddyVolumeName)
	if err != nil || !found || !validGeneratedDomainBootstrapVolume(volume, volumeIdentity, nonce) {
		return errors.New("generated domain bootstrap volume receipt is invalid")
	}
	receipt.volume, receipt.volumeIdentity = volume, volumeIdentity
	if err := receipt.acceptInventoryEffect("volume", caddyVolumeName); err != nil {
		return err
	}
	inventories, err := receipt.guard(ctx, manager, input, physical)
	if err != nil {
		return err
	}

	subnet, gateway, ingressIP, err := generatedDomainBootstrapNetworkCandidate(ctx, manager, inventories["network"])
	if err != nil {
		return err
	}
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		return err
	}
	result, err = manager.run(ctx, manager.options.CommandTimeout,
		"network", "create", "--driver", "bridge", "--subnet", subnet, "--gateway", gateway,
		"--label", gatewayV2ManagedLabelKey+"="+gatewayV2ManagedNetworkLabel,
		"--label", gatewayV2IdentityLabelKey+"="+gatewayV1IdentityVersion,
		caddyNetworkName)
	networkID := strings.TrimSpace(string(result.Stdout))
	if err != nil || !generatedDomainCanonicalDockerID(networkID, false) ||
		!generatedDomainAcknowledgedIdentity(result, networkID) {
		clearResult(&result)
		return errors.New("generated domain bootstrap network create was not acknowledged")
	}
	clearResult(&result)
	network, observedID, found, err := manager.inspectNamedGatewayNetwork(ctx, networkID)
	if err != nil || !found || normalizeID(observedID) != networkID ||
		!validGeneratedDomainBootstrapNetwork(network, networkID, subnet, gateway, ingressIP, false, "") {
		return errors.New("generated domain bootstrap network receipt is invalid")
	}
	byName, namedID, namedFound, err := manager.inspectNamedGatewayNetwork(ctx, caddyNetworkName)
	if err != nil || !namedFound || normalizeID(namedID) != networkID || !reflect.DeepEqual(network, byName) {
		return errors.New("generated domain bootstrap network name changed")
	}
	receipt.network, receipt.networkID, receipt.ingressIP = network, networkID, ingressIP
	if err := receipt.acceptInventoryEffect("network", networkID); err != nil {
		return err
	}
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		return err
	}

	args, err := caddyCreateArgs(receipt.image.ID, ingressIP, manager.options.HostPort)
	if err != nil {
		return err
	}
	result, err = manager.run(ctx, manager.options.CommandTimeout, args...)
	containerID := strings.TrimSpace(string(result.Stdout))
	if err != nil || !generatedDomainCanonicalDockerID(containerID, false) ||
		!generatedDomainAcknowledgedIdentity(result, containerID) {
		clearResult(&result)
		return errors.New("generated domain bootstrap container create was not acknowledged")
	}
	clearResult(&result)
	container, runtime, found, err := manager.inspectNamedGatewayContainer(ctx, containerID)
	if err != nil || !found || !validGeneratedDomainBootstrapContainer(container, runtime, containerID,
		receipt.image.ID, receipt.networkID, receipt.ingressIP, manager.options.HostPort, false) {
		return errors.New("generated domain bootstrap container receipt is invalid")
	}
	byNameContainer, byNameRuntime, namedFound, err := manager.inspectNamedGatewayContainer(ctx, caddyContainerName)
	if err != nil || !namedFound || !reflect.DeepEqual(container, byNameContainer) ||
		!reflect.DeepEqual(runtime, byNameRuntime) {
		return errors.New("generated domain bootstrap container name changed")
	}
	receipt.container, receipt.containerRun, receipt.containerID = container, runtime, containerID
	if err := receipt.acceptInventoryEffect("container", containerID); err != nil {
		return err
	}
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		return err
	}

	config, err := buildCaddyConfig(map[string]routeRecord{}, net.JoinHostPort(receipt.ingressIP,
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)))
	if err != nil {
		return err
	}
	expectedConfig := append([]byte(nil), config...)
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		clear(config)
		clear(expectedConfig)
		return err
	}
	if err := manager.copyConfigToContainer(ctx, config, gatewayV2ActiveConfigFile, containerID); err != nil {
		clear(expectedConfig)
		return err
	}
	restart, err := manager.inspectStoppedCaddyRestartConfig(ctx, containerID, gatewayV2ActiveConfigFile)
	if err != nil || !sameCaddyConfig(expectedConfig, restart) {
		clear(expectedConfig)
		clear(restart)
		return errors.New("generated domain bootstrap restart config readback failed")
	}
	clear(restart)
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		clear(expectedConfig)
		return err
	}

	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		clear(expectedConfig)
		return err
	}
	if err := manager.store.createInitialEmpty(); err != nil {
		clear(expectedConfig)
		return err
	}
	receipt.stateInstalled = true
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		clear(expectedConfig)
		return err
	}

	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		clear(expectedConfig)
		return err
	}
	result, err = manager.run(ctx, manager.options.CommandTimeout, "container", "start", containerID)
	if err != nil || !generatedDomainAcknowledgedIdentity(result, containerID) {
		clear(expectedConfig)
		clearResult(&result)
		return errors.New("generated domain bootstrap container start was not acknowledged")
	}
	clearResult(&result)
	receipt.started = true
	if _, err := receipt.guard(ctx, manager, input, physical); err != nil {
		clear(expectedConfig)
		return err
	}
	live, err := manager.inspectLiveCaddyConfig(ctx, containerID)
	if err != nil || !sameCaddyConfig(expectedConfig, live) {
		clear(expectedConfig)
		clear(live)
		return errors.New("generated domain bootstrap live config is invalid")
	}
	clear(expectedConfig)
	clear(live)
	return nil
}

func (r *generatedDomainBootstrapReceipt) captureOptionalImage(ctx context.Context, manager *Manager) error {
	image, found, err := manager.inspectImage(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !validGatewayPinnedImage(image, true) {
		return errors.New("generated domain pinned image is invalid")
	}
	r.image = image
	return nil
}

func (r *generatedDomainBootstrapReceipt) captureRequiredImage(ctx context.Context, manager *Manager) error {
	image, found, err := manager.inspectImage(ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		return errors.New("generated domain pinned image is unavailable")
	}
	r.image = image
	return nil
}

func (r *generatedDomainBootstrapReceipt) guard(
	ctx context.Context,
	manager *Manager,
	input generatedDomainReservationInput,
	physical generatedDomainPhysicalIdentity,
) (map[string][]string, error) {
	if ctx == nil || manager == nil || r == nil || manager.gatewayRebindAdmissionBlocked() {
		return nil, errors.New("generated domain bootstrap guard is invalid")
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return nil, err
	}
	database, err := input.stores.GeneratedDomainStartupSnapshot(ctx)
	if err != nil || !generatedDomainStartupDatabaseEffectEmpty(database) {
		return nil, errors.New("generated domain bootstrap database changed")
	}
	if r.database == nil {
		retained := database
		r.database = &retained
	} else if !reflect.DeepEqual(*r.database, database) {
		return nil, errors.New("generated domain bootstrap database changed")
	}
	protected, err := readGeneratedDomainProtectedStartup(ctx, input.options.DataRoot)
	if err != nil || protected.ambiguous {
		return nil, errors.New("generated domain bootstrap protected state changed")
	}
	if !r.stateInstalled {
		if !protected.effectEmpty {
			return nil, errors.New("generated domain bootstrap protected state is not empty")
		}
	} else {
		expected, resolveErr := resolveGeneratedDomainGatewayExpectation(database, protected)
		if resolveErr != nil || expected.kind != generatedDomainGatewayNativeV1 || expected.nativeV1 == nil ||
			len(expected.nativeV1.Active) != 0 {
			return nil, errors.New("generated domain bootstrap protected state is not exact")
		}
		if r.protected == nil {
			retained := protected
			r.protected = &retained
		} else if !sameGeneratedDomainProtectedStartup(*r.protected, protected) {
			return nil, errors.New("generated domain bootstrap protected state changed")
		}
	}
	observedPhysical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil || !sameGeneratedDomainPhysicalIdentity(observedPhysical, physical) {
		return nil, errors.New("generated domain bootstrap physical identity changed")
	}
	reader, err := newGeneratedDomainStartupReader(input.runner, input.options, physical.manager.daemonID)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(generatedDomainStartupObservationTimeout)
	first, inventories, err := reader.readDockerCensus(ctx, deadline)
	if err != nil {
		return nil, err
	}
	second, confirmed, err := reader.readDockerCensus(ctx, deadline)
	if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(inventories, confirmed) {
		return nil, errors.New("generated domain bootstrap Docker census changed")
	}
	if r.inventories == nil {
		r.inventories = cloneGeneratedDomainBootstrapInventories(confirmed)
	} else if !reflect.DeepEqual(r.inventories, confirmed) {
		return nil, errors.New("generated domain bootstrap Docker inventory changed")
	}
	confirmedPhysical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, input.stores, input.runner, input.options)
	if err != nil || !sameGeneratedDomainPhysicalIdentity(confirmedPhysical, physical) {
		return nil, errors.New("generated domain bootstrap physical identity changed")
	}
	if err := r.matchesSelectedObjects(second.objects); err != nil {
		return nil, err
	}
	if r.image.ID != "" {
		image, found, imageErr := manager.inspectImage(ctx)
		if imageErr != nil || !found || !reflect.DeepEqual(image, r.image) || !validGatewayPinnedImage(image, true) {
			return nil, errors.New("generated domain bootstrap image receipt changed")
		}
	}
	if r.volume.Name != "" {
		volume, identity, found, volumeErr := manager.inspectNamedVolumeWithIdentity(ctx, caddyVolumeName)
		if volumeErr != nil || !found || !reflect.DeepEqual(volume, r.volume) || identity != r.volumeIdentity {
			return nil, errors.New("generated domain bootstrap volume receipt changed")
		}
	}
	if r.networkID != "" {
		network, id, found, networkErr := manager.inspectNamedGatewayNetwork(ctx, r.networkID)
		if networkErr != nil || !found || normalizeID(id) != r.networkID ||
			!validGeneratedDomainBootstrapNetwork(network, r.networkID,
				r.network.IPAM.Config[0].Subnet, r.network.IPAM.Config[0].Gateway,
				r.ingressIP, r.started, r.containerID) {
			return nil, errors.New("generated domain bootstrap network receipt changed")
		}
	}
	if r.containerID != "" {
		container, runtime, found, containerErr := manager.inspectNamedGatewayContainer(ctx, r.containerID)
		if containerErr != nil || !found || !validGeneratedDomainBootstrapContainer(container, runtime,
			r.containerID, r.image.ID, r.networkID, r.ingressIP, manager.options.HostPort, r.started) {
			return nil, errors.New("generated domain bootstrap container receipt changed")
		}
	}
	if err := checkGeneratedDomainManagerRebindFence(ctx, manager); err != nil {
		return nil, err
	}
	return inventories, nil
}

func (r *generatedDomainBootstrapReceipt) acceptInventoryEffect(kind, identity string) error {
	if r == nil || r.inventories == nil || identity == "" {
		return errors.New("generated domain bootstrap Docker inventory receipt is unavailable")
	}
	values, present := r.inventories[kind]
	if !present {
		return errors.New("generated domain bootstrap Docker inventory kind is unavailable")
	}
	for _, retained := range values {
		if retained == identity {
			return errors.New("generated domain bootstrap Docker effect already existed")
		}
	}
	r.inventories = cloneGeneratedDomainBootstrapInventories(r.inventories)
	r.inventories[kind] = append(r.inventories[kind], identity)
	sort.Strings(r.inventories[kind])
	return nil
}

func cloneGeneratedDomainBootstrapInventories(values map[string][]string) map[string][]string {
	result := make(map[string][]string, len(values))
	for kind, identities := range values {
		retained := make([]string, len(identities))
		copy(retained, identities)
		result[kind] = retained
	}
	return result
}

func (r *generatedDomainBootstrapReceipt) matchesSelectedObjects(objects []generatedDomainDockerObject) error {
	expected := make(map[string]generatedDomainDockerObject)
	if r.volume.Name != "" {
		expected["volume\x00"+caddyVolumeName] = generatedDomainDockerObject{kind: "volume", name: caddyVolumeName,
			labels: r.volume.Labels, data: generatedDomainDockerObjectData{driver: r.volume.Driver,
				scope: r.volume.Scope, options: cloneStringMap(r.volume.Options), mountpoint: r.volumeIdentity.Mountpoint,
				createdAt: r.volumeIdentity.CreatedAt}}
	}
	if r.networkID != "" {
		expected["network\x00"+caddyNetworkName] = generatedDomainDockerObject{kind: "network", id: r.networkID,
			name: caddyNetworkName, labels: r.network.Labels, data: generatedDomainDockerObjectData{
				driver: r.network.Driver, scope: r.network.Scope, options: cloneStringMap(r.network.Options)}}
	}
	if r.containerID != "" {
		expected["container\x00"+caddyContainerName] = generatedDomainDockerObject{kind: "container",
			id: r.containerID, name: caddyContainerName, labels: r.container.Labels,
			data: generatedDomainDockerObjectData{options: map[string]string{}}}
	}
	if len(objects) != len(expected) {
		return errors.New("generated domain bootstrap Docker namespace is not exact")
	}
	for _, object := range objects {
		key := object.kind + "\x00" + strings.TrimPrefix(object.name, "/")
		wanted, ok := expected[key]
		if !ok || !reflect.DeepEqual(object, wanted) {
			return errors.New("generated domain bootstrap Docker receipt changed")
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		return errors.New("generated domain bootstrap Docker receipt is missing")
	}
	return nil
}

func validGeneratedDomainBootstrapVolume(value volumeInspection, identity gatewayV1VolumeIdentity, nonce string) bool {
	return value.Name == caddyVolumeName && value.Driver == "local" && value.Scope == "local" &&
		len(value.Options) == 0 && identity.Mountpoint != "" && identity.CreatedAt != "" &&
		reflect.DeepEqual(value.Labels, map[string]string{
			gatewayV2ManagedLabelKey:                 gatewayV2ManagedContainerLabel,
			gatewayV2IdentityLabelKey:                gatewayV1IdentityVersion,
			generatedDomainBootstrapVolumeNonceLabel: nonce,
		})
}

func validGeneratedDomainBootstrapNetwork(value caddyNetworkInspection, id, subnet, gateway, ingressIP string,
	started bool, containerID string,
) bool {
	if !generatedDomainCanonicalDockerID(id, false) || value.Name != caddyNetworkName || value.Driver != "bridge" ||
		value.Scope != "local" || value.Internal || len(value.Options) != 0 ||
		len(value.IPAM.Config) != 1 || value.IPAM.Config[0] != (networkIPAM{Subnet: subnet, Gateway: gateway}) ||
		!reflect.DeepEqual(value.Labels, map[string]string{
			gatewayV2ManagedLabelKey:  gatewayV2ManagedNetworkLabel,
			gatewayV2IdentityLabelKey: gatewayV1IdentityVersion,
		}) {
		return false
	}
	if containerID == "" {
		return !started && len(value.Containers) == 0
	}
	// A stopped container has a requested network attachment, but Docker may
	// expose its network membership only after Start. If membership is already
	// visible, it must still be the one exact receipt-owned container.
	if !started && len(value.Containers) == 0 {
		return true
	}
	if len(value.Containers) != 1 {
		return false
	}
	for observedID, container := range value.Containers {
		prefix, err := netip.ParsePrefix(container.IPv4Address)
		expected, expectedErr := netip.ParsePrefix(subnet)
		return err == nil && normalizeID(observedID) == containerID && container.Name == caddyContainerName &&
			expectedErr == nil && prefix.Addr().String() == ingressIP && prefix.Bits() == expected.Bits() &&
			prefix.Masked() == expected.Masked()
	}
	return false
}

func validGeneratedDomainBootstrapContainer(value caddyInspection, runtime gatewayContainerRuntime,
	id, imageID, networkID, ingressIP string, hostPort uint16, running bool,
) bool {
	if normalizeID(value.ID) != id || value.Running != running || value.Restarting ||
		!validGatewayContainerRuntime(runtime, !running) || !validCaddyInspection(value, imageID, hostPort) ||
		!generatedDomainExactRigLabels(value.Labels, map[string]string{
			gatewayV2ManagedLabelKey:      gatewayV2ManagedContainerLabel,
			gatewayV2IdentityLabelKey:     gatewayV1IdentityVersion,
			gatewayV2ListenerIsolationKey: gatewayV1IdentityVersion,
		}) || len(runtime.ConfiguredNetworks) != 1 {
		return false
	}
	network := runtime.ConfiguredNetworks[caddyNetworkName]
	if network.GwPriority != caddyGatewayPriority ||
		network.IPv6Gateway != "" || network.IPAMConfig == nil || network.IPAMConfig.IPv4Address != ingressIP ||
		network.IPAMConfig.IPv6Address != "" {
		return false
	}
	if running {
		return normalizeID(network.NetworkID) == networkID && network.IPAddress == ingressIP &&
			gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, value.PortBindings)
	}
	return (network.NetworkID == "" || normalizeID(network.NetworkID) == networkID) &&
		network.EndpointID == "" && network.IPAddress == "" &&
		!gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)
}

func generatedDomainBootstrapNetworkCandidate(ctx context.Context, manager *Manager,
	ids []string,
) (string, string, string, error) {
	used := make([]netip.Prefix, 0)
	for _, id := range ids {
		network, observedID, found, err := manager.inspectNamedGatewayNetwork(ctx, id)
		if err != nil || !found || normalizeID(observedID) != normalizeID(id) {
			return "", "", "", errors.New("generated domain Docker network inventory changed")
		}
		for _, config := range network.IPAM.Config {
			if config.Subnet == "" {
				continue
			}
			prefix, parseErr := netip.ParsePrefix(config.Subnet)
			if parseErr != nil {
				return "", "", "", errors.New("generated domain Docker network subnet is invalid")
			}
			used = append(used, prefix.Masked())
		}
	}
	for index := 0; index < 64; index++ {
		subnet, gateway, address := ingressNetworkCandidate(index)
		candidate, _ := netip.ParsePrefix(subnet)
		available := true
		for _, occupied := range used {
			if generatedDomainPrefixesOverlap(candidate, occupied) {
				available = false
				break
			}
		}
		if available {
			return subnet, gateway, address, nil
		}
	}
	return "", "", "", errors.New("generated domain ingress network address is unavailable")
}

func generatedDomainPrefixesOverlap(left, right netip.Prefix) bool {
	return left.Addr().BitLen() == right.Addr().BitLen() &&
		(left.Contains(right.Addr()) || right.Contains(left.Addr()))
}

func generatedDomainAcknowledgedIdentity(result runtimeprocess.CommandResult, identity string) bool {
	if result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 || identity == "" {
		return false
	}
	return bytes.Equal(result.Stdout, []byte(identity+"\n")) || bytes.Equal(result.Stdout, []byte(identity+"\r\n"))
}
