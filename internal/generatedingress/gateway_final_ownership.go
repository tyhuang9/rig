package generatedingress

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
)

// Immutable container facts shared by serving inspection and terminal-only
// withdrawal. This carries no current routes or authority to publish traffic.
type gatewayFinalOwnershipFacts struct {
	Lineage       appaccess.GatewayCurrentLineageRef
	Terminal      gatewayRebindAttemptTerminalView
	Network       gatewayV2NetworkPlan
	LocalHostPort uint16
}

func gatewayCurrentFinalOwnershipFacts(target gatewayCurrentPhysicalTarget, port uint16) gatewayFinalOwnershipFacts {
	return gatewayFinalOwnershipFacts{Lineage: target.Lineage, Terminal: target.Terminal, Network: target.State.Network, LocalHostPort: port}
}

func validGatewayFinalOwnershipFacts(facts gatewayFinalOwnershipFacts) bool {
	return facts.LocalHostPort != 0 && validGatewayV2NetworkPlan(facts.Network) &&
		gatewayRebindAttemptTerminalMatchesLineage(facts.Terminal, facts.Lineage) && facts.Terminal.Resources.FinalContainer != nil
}

func (facts gatewayFinalOwnershipFacts) profile() gatewayProfileBinding {
	p := facts.Terminal.SuccessorProfile
	return gatewayProfileBinding{RevisionID: p.RevisionID, RevisionNumber: p.RevisionNumber, SpecDigest: p.SpecDigest,
		SelectedIPv4: p.SelectedIPv4, InterfaceID: p.InterfaceID, PortStart: p.PortStart, PortEnd: p.PortEnd}
}

func gatewayFinalOwnershipVolumeMatches(facts gatewayFinalOwnershipFacts,
	volume volumeInspection, identity gatewayV1VolumeIdentity, binding any, role string,
) bool {
	labels := gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, role)
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

func gatewayFinalOwnershipIngressMatches(facts gatewayFinalOwnershipFacts,
	network caddyNetworkInspection, id string,
) bool {
	identity := facts.Terminal.SuccessorIdentity
	if len(identity.Digest) < 12 {
		return false
	}
	labels := gatewayFinalOwnershipLabels(facts, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole)
	bridge := "rig" + identity.Digest[:12]
	digest, err := canonicalDigest(struct {
		Version int                                 `json:"version"`
		Name    string                              `json:"name"`
		Bridge  string                              `json:"bridge"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
		Labels  map[string]string                   `json:"labels"`
	}{1, identity.IngressNetwork, bridge, gatewayRebindSuccessorIntentNetwork(facts.Network), labels})
	return err == nil && id == facts.Terminal.Resources.IngressNetwork.ID && digest == facts.Terminal.Resources.IngressNetwork.OwnershipDigest &&
		network.Name == identity.IngressNetwork && network.Driver == "bridge" && network.Scope == "local" && !network.Internal &&
		reflect.DeepEqual(network.Options, map[string]string{gatewayRebindBridgeNameOptionKey: bridge}) &&
		len(network.IPAM.Config) == 1 && network.IPAM.Config[0] == (networkIPAM{Subnet: facts.Network.Subnet,
		Gateway: facts.Network.GatewayIPv4}) && reflect.DeepEqual(network.Labels, labels)
}

func gatewayFinalOwnershipContainerMatches(facts gatewayFinalOwnershipFacts,
	container caddyInspection, runtime gatewayContainerRuntime,
) bool {
	if !validGatewayFinalOwnershipFacts(facts) {
		return false
	}
	identity := facts.Terminal.SuccessorIdentity
	labels := gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)
	ownership, err := canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Labels  map[string]string `json:"labels"`
	}{1, identity.FinalContainer, labels})
	configuration, configurationErr := gatewayFinalOwnershipConfigurationDigest(facts)
	if err != nil || configurationErr != nil || ownership != facts.Terminal.Resources.FinalContainer.OwnershipDigest ||
		configuration != facts.Terminal.Resources.FinalContainer.ConfigurationDigest ||
		normalizeID(container.ID) != facts.Terminal.Resources.FinalContainer.ID || normalizeID(container.Image) != facts.Terminal.Resources.ImageID ||
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
		!reflect.DeepEqual(container.Labels, labels) || !validGatewayRebindStageContainerMounts(container.Mounts, identity) ||
		!gatewayCurrentPhysicalPortBindings(container.PortBindings, facts.profile(), facts.LocalHostPort) {
		return false
	}
	if container.Running {
		return gatewayV2EffectivePortBindingsMatchConfigured(runtime.EffectivePortBindings, container.PortBindings)
	}
	return !gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings)
}

func gatewayFinalOwnershipConfigurationDigest(facts gatewayFinalOwnershipFacts) (string, error) {
	if !validGatewayFinalOwnershipFacts(facts) {
		return "", errors.New("invalid generated ingress current initial configuration")
	}
	identity := facts.Terminal.SuccessorIdentity
	profile := facts.Terminal.SuccessorProfile
	args := []string{
		"container", "create", "--name", identity.FinalContainer, "--hostname", identity.FinalHostname,
		"--network", "name=" + identity.IngressNetwork + ",ip=" + facts.Network.ContainerIPv4 + ",gw-priority=1",
		"--mount", "type=volume,src=" + identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", gatewayV2FinalRestartPolicy,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for _, network := range facts.Terminal.Resources.ApplicationNetworks {
		args = append(args, "--network", "name="+network.Name)
	}
	for port := profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", profile.SelectedIPv4+":"+text+":"+text+"/tcp")
		if port == profile.PortEnd {
			break
		}
	}
	args = append(args, "--publish", "127.0.0.1:"+strconv.FormatUint(uint64(facts.LocalHostPort), 10)+":"+
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp")
	args = appendGatewayV2Labels(args,
		gatewayFinalOwnershipLabels(facts, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole))
	args = append(args, "sha256:"+facts.Terminal.Resources.ImageID, "run", "--config", "/config/"+identity.ActiveConfigFilename)
	// Retained terminal formats use distinct create-time digest envelopes.
	// Select the exact validated format; accepting either digest would blur
	// the immutable evidence contract between legacy and typed attempts.
	switch facts.Terminal.Format {
	case gatewayRebindAttemptTerminalLegacyV1:
		return canonicalDigest(struct {
			Version  int                                       `json:"version"`
			Args     []string                                  `json:"args"`
			Networks []gatewayRebindHandoverApplicationNetwork `json:"networks"`
		}{1, args, facts.Terminal.Resources.ApplicationNetworks})
	case gatewayRebindAttemptTerminalTypedV2:
		return canonicalDigest(struct {
			Version int      `json:"version"`
			Args    []string `json:"args"`
		}{1, args})
	default:
		return "", errors.New("invalid generated ingress current terminal format")
	}
}

func gatewayFinalOwnershipLabels(facts gatewayFinalOwnershipFacts, managed, role string) map[string]string {
	networkDigest, _ := gatewayCurrentPhysicalNetworkDigest(facts.Network)
	return map[string]string{
		gatewayV2ManagedLabelKey:          managed,
		gatewayV2IdentityLabelKey:         gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey:        facts.Lineage.OperationID,
		gatewayV2IdentityDigestLabelKey:   facts.Lineage.ProtectedIdentityDigest,
		gatewayV2PlanDigestLabelKey:       networkDigest,
		gatewayV2ResourceRoleLabelKey:     role,
		gatewayRebindIntentDigestLabelKey: facts.Lineage.ProtectedIntentDigest,
		gatewayRebindGenerationLabelKey:   strconv.FormatUint(facts.Lineage.ProtectedGeneration, 10),
	}
}
