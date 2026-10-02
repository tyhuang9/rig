package generatedingress

import (
	"sort"
	"strconv"
)

// These builders produce only the exact resources accepted by the v2 observer.
// The caller must hold the gateway lock, write the relevant intent, and recheck
// host networking immediately around the corresponding Docker mutation.
func gatewayV2NetworkCreateArgs(state gatewayV2RouteState, journal gatewayMigrationJournal) ([]string, error) {
	if journal.Phase != gatewayPhaseStageIntent || !validGatewayMigrationJournal(journal) ||
		!journalMatchesInitialV2State(journal, state) || journal.Resources.IngressNetworkID != "" {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{"network", "create", "--driver", "bridge", "--subnet", state.Network.Subnet, "--gateway", state.Network.GatewayIPv4}
	args = appendGatewayV2Labels(args, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false))
	return append(args, state.Identity.IngressNetwork), nil
}

func gatewayV2VolumeCreateArgs(state gatewayV2RouteState, journal gatewayMigrationJournal, role string) ([]string, error) {
	if journal.Phase != gatewayPhaseStageIntent || !validGatewayMigrationJournal(journal) || !journalMatchesInitialV2State(journal, state) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	name := state.Identity.ConfigVolume
	if role == gatewayV2DataVolumeRole {
		name = state.Identity.DataVolume
		if journal.Resources.DataVolume != (gatewayV2VolumeResourceBinding{}) {
			return nil, &Error{Code: DiagnosticValidationFailed}
		}
	} else if role != gatewayV2ConfigVolumeRole {
		return nil, &Error{Code: DiagnosticValidationFailed}
	} else if journal.Resources.ConfigVolume != (gatewayV2VolumeResourceBinding{}) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{"volume", "create", "--driver", "local"}
	args = appendGatewayV2Labels(args, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, false))
	return append(args, name), nil
}

func gatewayV2ContainerCreateArgs(state gatewayV2RouteState, journal gatewayMigrationJournal, role, imageID string) ([]string, error) {
	if !validGatewayMigrationJournal(journal) || !journalMatchesInitialV2State(journal, state) ||
		!validContainerID(imageID) || normalizeID(imageID) != journal.Resources.ImageID ||
		journal.Resources.IngressNetworkID == "" || journal.Resources.ConfigVolume == (gatewayV2VolumeResourceBinding{}) ||
		journal.Resources.DataVolume == (gatewayV2VolumeResourceBinding{}) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	name, filename, restart := state.Identity.StageContainer, state.Identity.StageConfigFilename, gatewayV2StageRestartPolicy
	if role == gatewayV2StageContainerRole {
		if journal.Phase != gatewayPhaseStageIntent || journal.Resources.StageContainerID != "" {
			return nil, &Error{Code: DiagnosticValidationFailed}
		}
	} else if role == gatewayV2FinalContainerRole {
		if journal.Phase != gatewayPhaseTransferIntent || journal.Resources.StageContainerID == "" || journal.Resources.FinalContainerID != "" {
			return nil, &Error{Code: DiagnosticValidationFailed}
		}
		name, filename, restart = state.Identity.FinalContainer, state.Identity.ActiveConfigFilename, gatewayV2FinalRestartPolicy
	} else {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	args := []string{
		"container", "create", "--name", name, "--hostname", name,
		"--network", "name=" + state.Identity.IngressNetwork + ",gw-priority=1", "--ip", state.Network.ContainerIPv4,
		"--mount", "type=volume,src=" + state.Identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + state.Identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", restart,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for port := state.Profile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", state.Profile.SelectedIPv4+":"+value+":"+value+"/tcp")
		if port == state.Profile.PortEnd {
			break
		}
	}
	if role == gatewayV2FinalContainerRole {
		value := strconv.FormatUint(uint64(journal.Source.LocalHostPort), 10)
		args = append(args, "--publish", "127.0.0.1:"+value+":"+strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp")
	}
	args = appendGatewayV2Labels(args, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true))
	return append(args, imageID, "run", "--config", "/config/"+filename), nil
}

func appendGatewayV2Labels(args []string, labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	return args
}
