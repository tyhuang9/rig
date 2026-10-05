package generatedingress

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"strconv"
)

func validGatewayRebindFinalHandoverBase(value gatewayRebindFinalHandoverContext) bool {
	intent, progress := value.Intent, value.SequenceTwelve
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindProgressRecord(progress) ||
		progress.Sequence != 12 || progress.Phase != gatewayRebindProgressFinalConfigCopied ||
		progress.ProtectedIntentDigest != intent.Digest || progress.Generation != intent.Generation ||
		progress.OperationID != intent.OperationID || progress.Stage == nil ||
		!gatewayRebindProtectedIntentMatchesPredecessor(intent, value.Predecessor) ||
		!validGatewayTopologyInputs(value.Source, value.Predecessor.State, value.Predecessor.Journal) ||
		value.Predecessor.Journal.Phase != gatewayPhaseCommitted {
		return false
	}
	stage := progress.Stage
	if stage.Identity != intent.Intent.Identity || stage.NetworkPlan != intent.Intent.Network ||
		stage.NetworkPlanDigest != intent.Intent.NetworkDigest ||
		stage.NetworkTopologyDigest != intent.NetworkObservationDigest ||
		stage.Network == nil || stage.ConfigVolume == nil || stage.DataVolume == nil ||
		stage.StageContainer == nil || stage.StageServing == nil ||
		stage.FinalConfigIntent == nil || stage.FinalConfigCopy == nil ||
		!validGatewayRebindStageConfigVolumeBinding(intent, *stage.ConfigVolume) ||
		!validGatewayRebindStageDataVolumeBinding(intent, *stage.DataVolume) ||
		!validGatewayRebindStageContainerBinding(intent, *stage.StageContainer) ||
		!gatewayRebindFinalConfigRoutePlanMatchesPredecessor(intent, value.Predecessor, stage.FinalConfigIntent.RoutePlan) {
		return false
	}
	config, err := gatewayRebindFinalConfigBytes(intent, stage.FinalConfigIntent.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(config)
	digest := sha256.Sum256(config)
	return stage.FinalConfigIntent.ContentLength == int64(len(config)) &&
		stage.FinalConfigIntent.ContentDigest == hex.EncodeToString(digest[:])
}

func gatewayRebindFinalHandoverPlanFor(value gatewayRebindFinalHandoverContext,
	initial gatewayRebindFinalHandoverObservation,
) (gatewayRebindFinalHandoverPlan, error) {
	if !validGatewayRebindFinalHandoverBase(value) || !validGatewayRebindFinalHandoverObservationValue(initial) ||
		initial.Stage != gatewayRebindHandoverContainerRunning || initial.Final != gatewayRebindHandoverContainerAbsent ||
		!initial.ConfigVolumePresent || !initial.DataVolumePresent || !initial.IngressNetworkPresent ||
		initial.ConfigDigest != value.SequenceTwelve.Stage.FinalConfigIntent.ContentDigest {
		return gatewayRebindFinalHandoverPlan{}, errors.New("invalid generated ingress rebind initial handover observation")
	}
	return buildGatewayRebindFinalHandoverPlan(value, initial.ApplicationNetworks,
		initial.PredecessorObservationDigest, initial.PredecessorRunning)
}

func buildGatewayRebindFinalHandoverPlan(value gatewayRebindFinalHandoverContext,
	networks []gatewayRebindHandoverApplicationNetwork, predecessorObservationDigest string, predecessorRunning bool,
) (gatewayRebindFinalHandoverPlan, error) {
	if !validGatewayRebindFinalHandoverBase(value) || !validSHA256(predecessorObservationDigest) {
		return gatewayRebindFinalHandoverPlan{}, errors.New("invalid generated ingress rebind handover plan input")
	}
	networks = append([]gatewayRebindHandoverApplicationNetwork{}, networks...)
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	if !validGatewayRebindHandoverApplicationNetworks(value, networks) {
		return gatewayRebindFinalHandoverPlan{}, errors.New("invalid generated ingress rebind application network bindings")
	}
	args := gatewayRebindFinalHandoverCreateArgs(value, networks)
	configurationDigest, err := canonicalDigest(struct {
		Version  int                                       `json:"version"`
		Args     []string                                  `json:"args"`
		Networks []gatewayRebindHandoverApplicationNetwork `json:"networks"`
	}{1, args, networks})
	if err != nil {
		return gatewayRebindFinalHandoverPlan{}, err
	}
	ownershipDigest, err := canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Labels  map[string]string `json:"labels"`
	}{1, value.Intent.Intent.Identity.FinalContainer,
		gatewayRebindStageResourceLabels(value.Intent, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole)})
	if err != nil {
		return gatewayRebindFinalHandoverPlan{}, err
	}
	predecessorDigest, err := canonicalDigest(value.Intent.Intent.Predecessor)
	if err != nil {
		return gatewayRebindFinalHandoverPlan{}, err
	}
	plan := gatewayRebindFinalHandoverPlan{
		PredecessorObservationDigest: predecessorObservationDigest, PredecessorInitiallyRunning: predecessorRunning,
		Version: 1, Context: gatewayRebindFinalHandoverPlanContext,
		ProtectedIntentDigest: value.Intent.Digest, SequenceTwelveDigest: value.SequenceTwelve.Digest,
		PredecessorDigest:    predecessorDigest,
		RoutePlanDigest:      value.SequenceTwelve.Stage.FinalConfigIntent.RoutePlan.Digest,
		FinalConfigDigest:    value.SequenceTwelve.Stage.FinalConfigIntent.ContentDigest,
		FinalOwnershipDigest: ownershipDigest, FinalConfigurationDigest: configurationDigest,
		LocalHostPort: value.Predecessor.Journal.Source.LocalHostPort, ApplicationNetworks: networks,
	}
	plan.Digest, err = gatewayRebindFinalHandoverPlanDigest(plan)
	return plan, err
}

func validGatewayRebindFinalHandoverPlan(value gatewayRebindFinalHandoverContext,
	plan gatewayRebindFinalHandoverPlan,
) bool {
	expected, err := buildGatewayRebindFinalHandoverPlan(value, plan.ApplicationNetworks,
		plan.PredecessorObservationDigest, plan.PredecessorInitiallyRunning)
	return err == nil && reflect.DeepEqual(plan, expected)
}

func validGatewayRebindHandoverApplicationNetworks(value gatewayRebindFinalHandoverContext,
	networks []gatewayRebindHandoverApplicationNetwork,
) bool {
	routes := gatewayRebindFinalHandoverRoutes(value)
	owners, valid := gatewayRouteNetworkOwners(routes)
	if !valid || len(networks) != len(owners) {
		return false
	}
	ids := make(map[string]bool, len(networks))
	for index, network := range networks {
		if _, exists := owners[network.Name]; !exists || network.Name == value.Intent.Intent.Identity.IngressNetwork ||
			network.Name == value.Predecessor.State.Identity.IngressNetwork || !validContainerID(network.ID) ||
			normalizeID(network.ID) != network.ID || ids[network.ID] ||
			(index > 0 && networks[index-1].Name >= network.Name) {
			return false
		}
		ids[network.ID] = true
	}
	return true
}

func gatewayRebindFinalHandoverRoutes(value gatewayRebindFinalHandoverContext) map[string]routeRecord {
	if value.SequenceTwelve.Stage == nil || value.SequenceTwelve.Stage.FinalConfigIntent == nil {
		return nil
	}
	routes := make(map[string]routeRecord, len(value.SequenceTwelve.Stage.FinalConfigIntent.RoutePlan.Routes))
	for _, binding := range value.SequenceTwelve.Stage.FinalConfigIntent.RoutePlan.Routes {
		routes[binding.AppID] = binding.Route
	}
	return routes
}

// Only callers that have validated the base and complete sorted network roster
// may use this builder. The public-to-this-package wrapper below also compares
// the resulting arguments with the immutable handover plan.
func gatewayRebindFinalHandoverCreateArgs(value gatewayRebindFinalHandoverContext,
	networks []gatewayRebindHandoverApplicationNetwork,
) []string {
	intent, stage := value.Intent, value.SequenceTwelve.Stage
	identity := intent.Intent.Identity
	args := []string{
		"container", "create", "--name", identity.FinalContainer, "--hostname", identity.FinalHostname,
		"--network", "name=" + identity.IngressNetwork + ",ip=" + intent.Intent.Network.ContainerIPv4 + ",gw-priority=1",
		"--mount", "type=volume,src=" + identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", gatewayV2FinalRestartPolicy,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for _, network := range networks {
		args = append(args, "--network", "name="+network.Name)
	}
	for port := intent.Intent.SuccessorProfile.PortStart; ; port++ {
		portText := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", intent.Intent.SuccessorProfile.SelectedIPv4+":"+portText+":"+portText+"/tcp")
		if port == intent.Intent.SuccessorProfile.PortEnd {
			break
		}
	}
	args = append(args, "--publish", "127.0.0.1:"+strconv.FormatUint(uint64(value.Predecessor.Journal.Source.LocalHostPort), 10)+":"+
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp")
	args = appendGatewayV2Labels(args, gatewayRebindStageResourceLabels(intent, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole))
	return append(args, "sha256:"+stage.ObservedDockerImageID, "run", "--config", "/config/"+identity.ActiveConfigFilename)
}

func gatewayRebindFinalContainerCreateArgs(value gatewayRebindFinalHandoverContext) ([]string, error) {
	if value.Plan == nil || !validGatewayRebindFinalHandoverPlan(value, *value.Plan) {
		return nil, &Error{Code: DiagnosticValidationFailed}
	}
	return gatewayRebindFinalHandoverCreateArgs(value, value.Plan.ApplicationNetworks), nil
}

func gatewayRebindFinalContainerBindingFor(value gatewayRebindFinalHandoverContext,
	id string,
) (gatewayRebindFinalContainerBinding, error) {
	if value.Plan == nil || !validGatewayRebindFinalHandoverPlan(value, *value.Plan) ||
		!validContainerID(id) || normalizeID(id) != id || id == value.SequenceTwelve.Stage.StageContainer.ID ||
		id == value.Predecessor.Journal.Resources.FinalContainerID {
		return gatewayRebindFinalContainerBinding{}, &Error{Code: DiagnosticValidationFailed}
	}
	return gatewayRebindFinalContainerBinding{ID: id, OwnershipDigest: value.Plan.FinalOwnershipDigest,
		ConfigurationDigest: value.Plan.FinalConfigurationDigest}, nil
}

func validGatewayRebindFinalContainerBinding(value gatewayRebindFinalHandoverContext,
	binding gatewayRebindFinalContainerBinding,
) bool {
	expected, err := gatewayRebindFinalContainerBindingFor(value, binding.ID)
	return err == nil && binding == expected
}
