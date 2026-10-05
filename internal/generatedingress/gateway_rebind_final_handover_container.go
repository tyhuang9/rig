package generatedingress

import (
	"net/netip"
	"strconv"
	"strings"
)

// This validates the container against the retained rebind plan directly.
// Network membership, volume identity, config contents and stable before/after
// observation are separate joint-topology proofs performed by the driver.
func validGatewayRebindFinalHandoverContainer(value gatewayRebindFinalHandoverContext,
	container caddyInspection, runtime gatewayContainerRuntime, expectedID string,
) bool {
	if value.Plan == nil || !validGatewayRebindFinalHandoverPlan(value, *value.Plan) {
		return false
	}
	binding, err := gatewayRebindFinalContainerBindingFor(value, expectedID)
	if err != nil {
		return false
	}
	if value.Final != nil {
		if *value.Final != binding || value.CreatedFinalID != "" {
			return false
		}
	} else if value.CreatedFinalID != expectedID || container.Running {
		// A successful create response permits only its stopped readback. It
		// cannot be reconstructed from a name or used as a running binding.
		return false
	}
	intent, stage := value.Intent, value.SequenceTwelve.Stage
	identity := intent.Intent.Identity
	if !validContainerID(container.ID) || normalizeID(container.ID) != expectedID ||
		normalizeID(container.Image) != stage.ObservedDockerImageID ||
		container.Restarting || !validGatewayContainerRuntime(runtime, false) ||
		strings.TrimPrefix(container.Name, "/") != identity.FinalContainer ||
		container.Hostname != identity.FinalHostname || container.User != "1000:1000" ||
		container.NetworkMode != identity.IngressNetwork || !exactGatewayV2Environment(container.Env) ||
		!container.ReadOnly || container.Privileged || !onlyCaddyCapability(container.CapAdd) ||
		!exactFoldSet(container.CapDrop, "ALL") || !onlyNoNewPrivileges(container.SecurityOpt) ||
		len(container.Binds) != 0 || len(container.Tmpfs) != 0 || container.Memory != 268435456 ||
		container.MemorySwap != 268435456 || container.NanoCPUs != 1_000_000_000 || container.PIDsLimit != 128 ||
		container.LogType != "local" || len(container.LogConfig) != 2 || container.LogConfig["max-size"] != "10m" ||
		container.LogConfig["max-file"] != "3" || container.Restart != gatewayV2FinalRestartPolicy ||
		len(container.Entrypoint) != 1 || container.Entrypoint[0] != caddyExecutable || len(container.Cmd) != 3 ||
		container.Cmd[0] != "run" || container.Cmd[1] != "--config" || container.Cmd[2] != "/config/"+identity.ActiveConfigFilename ||
		len(container.Ulimits) != 1 || container.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) ||
		!validGatewayV2ContainerLabels(container.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)) ||
		!validGatewayRebindStageContainerMounts(container.Mounts, identity) ||
		!validGatewayRebindFinalHandoverPortBindings(container.PortBindings, value) ||
		(container.Running && !gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, container.PortBindings)) ||
		(!container.Running && gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)) {
		return false
	}

	networks := make(map[string]string, len(value.Plan.ApplicationNetworks)+1)
	networks[identity.IngressNetwork] = stage.Network.ID
	for _, network := range value.Plan.ApplicationNetworks {
		networks[network.Name] = network.ID
	}
	if len(runtime.ConfiguredNetworks) != len(networks) ||
		(container.Running && len(container.Networks) != len(networks)) ||
		(!container.Running && len(container.Networks) != 0 && len(container.Networks) != len(networks)) {
		return false
	}
	for name, id := range networks {
		configured, exists := runtime.ConfiguredNetworks[name]
		priority := 0
		if name == identity.IngressNetwork {
			priority = caddyGatewayPriority
		}
		if !exists || configured.GwPriority != priority || configured.IPv6Gateway != "" ||
			!validGatewayV2StoppedNetworkReference(configured.NetworkID, id) {
			return false
		}
		if name == identity.IngressNetwork {
			if configured.IPAMConfig == nil || configured.IPAMConfig.IPv4Address != intent.Intent.Network.ContainerIPv4 ||
				configured.IPAMConfig.IPv6Address != "" {
				return false
			}
		} else if configured.IPAMConfig != nil && *configured.IPAMConfig != (gatewayV2ConfiguredIPAM{}) {
			return false
		}
		if container.Running {
			address, addressErr := netip.ParseAddr(configured.IPAddress)
			if configured.NetworkID == "" || !validContainerID(configured.EndpointID) ||
				normalizeID(configured.EndpointID) != configured.EndpointID || addressErr != nil || !address.Is4() ||
				address.IsUnspecified() || address.IsMulticast() || address.String() != configured.IPAddress ||
				(name == identity.IngressNetwork && configured.IPAddress != intent.Intent.Network.ContainerIPv4) {
				return false
			}
		} else if configured.EndpointID != "" || configured.IPAddress != "" {
			return false
		}
		if container.Running || len(container.Networks) != 0 {
			attached := container.Networks[name]
			if attached == nil || attached.GwPriority != priority || attached.IPv6Gateway != "" ||
				attached.IPAddress != configured.IPAddress {
				return false
			}
		}
	}
	return true
}

func validGatewayRebindFinalHandoverPortBindings(actual map[string][]map[string]string,
	value gatewayRebindFinalHandoverContext,
) bool {
	profile := value.Intent.Intent.SuccessorProfile
	if len(actual) != int(profile.PortEnd-profile.PortStart)+2 {
		return false
	}
	for port := profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		binding := actual[text+"/tcp"]
		if len(binding) != 1 || len(binding[0]) != 2 ||
			binding[0]["HostIp"] != profile.SelectedIPv4 || binding[0]["HostPort"] != text {
			return false
		}
		if port == profile.PortEnd {
			break
		}
	}
	loopback := actual[strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp"]
	return len(loopback) == 1 && len(loopback[0]) == 2 && loopback[0]["HostIp"] == "127.0.0.1" &&
		loopback[0]["HostPort"] == strconv.FormatUint(uint64(value.Plan.LocalHostPort), 10)
}
