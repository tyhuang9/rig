package generatedingress

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindTypedFinalConfigPlanVersion   = 1
	gatewayRebindTypedFinalConfigPlanContext   = "hostd/generated-ingress/rebind/typed-final-route-plan/v1"
	gatewayRebindTypedFinalConfigIntentVersion = 1
	gatewayRebindTypedFinalConfigIntentContext = "hostd/generated-ingress/rebind/typed-final-config-intent/v1"
	gatewayRebindTypedHandoverIntentVersion    = 1
	gatewayRebindTypedHandoverIntentContext    = "hostd/generated-ingress/rebind/typed-final-handover-intent/v1"
	gatewayRebindTypedCutoverIntentVersion     = 1
	gatewayRebindTypedCutoverIntentContext     = "hostd/generated-ingress/rebind/typed-cutover-intent/v1"
)

// gatewayRebindTypedFinalConfigRoutePlan is the v2-source route plan. It
// retains the actual typed predecessor and every application head, including
// loopback-only applications. It deliberately has no migration journal or
// legacy predecessor-upgrade field.
type gatewayRebindTypedFinalConfigRoutePlan struct {
	Version                   int                                         `json:"version"`
	Context                   string                                      `json:"context"`
	OperationID               string                                      `json:"operationId"`
	Predecessor               appaccess.GatewayRebindSourceRef            `json:"predecessor"`
	ClaimSpecDigest           string                                      `json:"claimSpecDigest"`
	PredecessorIngressNetwork string                                      `json:"predecessorIngressNetwork"`
	EffectiveSuccessorProfile gatewayRebindSuccessorIntentProfile         `json:"effectiveSuccessorProfile"`
	SuccessorIdentity         gatewayRebindSuccessorIdentity              `json:"successorIdentity"`
	Routes                    []gatewayRebindTypedFinalConfigRouteBinding `json:"routes"`
	RuntimeHeadsDigest        string                                      `json:"runtimeHeadsDigest"`
	RouteMapDigest            string                                      `json:"routeMapDigest"`
	Digest                    string                                      `json:"digest"`
}

type gatewayRebindTypedFinalConfigRouteBinding struct {
	AppID               string                              `json:"appId"`
	Route               routeRecord                         `json:"route"`
	SourceLAN           *gatewayCurrentLANBinding           `json:"sourceLan,omitempty"`
	RuntimeHead         gatewayRebindFinalConfigRuntimeHead `json:"runtimeHead"`
	SourceBindingDigest string                              `json:"sourceBindingDigest"`
}

type gatewayRebindTypedFinalConfigIntentBinding struct {
	Version                    int                                    `json:"version"`
	Context                    string                                 `json:"context"`
	RoutePlan                  gatewayRebindTypedFinalConfigRoutePlan `json:"routePlan"`
	ContentDigest              string                                 `json:"contentDigest"`
	ContentLength              int64                                  `json:"contentLength"`
	Destination                string                                 `json:"destination"`
	SuccessorIdentityDigest    string                                 `json:"successorIdentityDigest"`
	ProtectedIntentDigest      string                                 `json:"protectedIntentDigest"`
	ProtectedPredecessorDigest string                                 `json:"protectedPredecessorDigest"`
	StageServing               gatewayRebindStageServingBinding       `json:"stageServing"`
	PriorProgressDigest        string                                 `json:"priorProgressDigest"`
	Digest                     string                                 `json:"digest"`
}

// gatewayRebindTypedHandoverIntent wraps the version-neutral physical create
// plan with the exact typed route plan and predecessor source. The embedded
// plan's PredecessorDigest is the canonical typed source digest.
type gatewayRebindTypedHandoverIntent struct {
	Version                     int                              `json:"version"`
	Context                     string                           `json:"context"`
	Plan                        gatewayRebindFinalHandoverPlan   `json:"plan"`
	FinalConfigIntentDigest     string                           `json:"finalConfigIntentDigest"`
	FinalConfigCopyDigest       string                           `json:"finalConfigCopyDigest"`
	Predecessor                 appaccess.GatewayRebindSourceRef `json:"predecessor"`
	PredecessorCheckpointDigest string                           `json:"predecessorCheckpointDigest"`
	PriorProgressDigest         string                           `json:"priorProgressDigest"`
	Digest                      string                           `json:"digest"`
}

// gatewayRebindTypedCutoverIntent binds withdrawal of the actual typed source
// and start of the exact final successor. It carries no synthetic migration
// journal identity.
type gatewayRebindTypedCutoverIntent struct {
	Version                      int                              `json:"version"`
	Context                      string                           `json:"context"`
	HandoverIntentDigest         string                           `json:"handoverIntentDigest"`
	FinalBindingDigest           string                           `json:"finalBindingDigest"`
	Predecessor                  appaccess.GatewayRebindSourceRef `json:"predecessor"`
	PredecessorCheckpointDigest  string                           `json:"predecessorCheckpointDigest"`
	PreparationObservationDigest string                           `json:"preparationObservationDigest"`
	PriorProgressDigest          string                           `json:"priorProgressDigest"`
	Digest                       string                           `json:"digest"`
}

func gatewayRebindTypedFinalConfigSourceBindingDigest(value gatewayRebindTypedFinalConfigRouteBinding) (string, error) {
	value.SourceBindingDigest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedFinalConfigRouteMapDigest(routes []gatewayRebindTypedFinalConfigRouteBinding) (string, error) {
	return canonicalDigest(struct {
		Version int                                         `json:"version"`
		Context string                                      `json:"context"`
		Routes  []gatewayRebindTypedFinalConfigRouteBinding `json:"routes"`
	}{gatewayRebindTypedFinalConfigPlanVersion, gatewayRebindTypedFinalConfigPlanContext, routes})
}

func gatewayRebindTypedRuntimeHeads(routes []gatewayRebindTypedFinalConfigRouteBinding) ([]appaccess.GatewayRebindRuntimeHead, error) {
	heads := make([]gatewayRebindFinalConfigRuntimeHead, len(routes))
	for index := range routes {
		heads[index] = routes[index].RuntimeHead
	}
	result := make([]appaccess.GatewayRebindRuntimeHead, len(heads))
	for index, head := range heads {
		updatedAt, err := time.Parse(time.RFC3339Nano, head.UpdatedAt)
		if err != nil {
			return nil, err
		}
		result[index] = appaccess.GatewayRebindRuntimeHead{AppID: head.AppID, DeploymentID: head.DeploymentID,
			ReleaseID: head.ReleaseID, Slot: head.Slot, Generation: head.Generation, UpdatedAt: updatedAt}
	}
	return result, nil
}

func gatewayRebindTypedFinalConfigRoutePlanDigest(value gatewayRebindTypedFinalConfigRoutePlan) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedFinalConfigIntentDigest(value gatewayRebindTypedFinalConfigIntentBinding) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedHandoverIntentDigest(value gatewayRebindTypedHandoverIntent) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedCutoverIntentDigest(value gatewayRebindTypedCutoverIntent) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindTypedPredecessorDigest(intent gatewayRebindProtectedIntentV2) (string, error) {
	return canonicalDigest(intent.Predecessor)
}

func gatewayRebindTypedStageConfigProbeToken(intent gatewayRebindProtectedIntentV2) (string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) {
		return "", errors.New("invalid generated ingress typed stage probe input")
	}
	return gatewayRebindStageConfigProbeTokenV1(intent.Digest, intent.OperationID,
		intent.Identity.Digest, intent.NetworkDigest)
}

func gatewayRebindTypedStageConfigBytes(intent gatewayRebindProtectedIntentV2) ([]byte, error) {
	probe, err := gatewayRebindTypedStageConfigProbeToken(intent)
	if err != nil {
		return nil, err
	}
	body, err := buildCaddyConfigV2(map[string]routeRecord{},
		net.JoinHostPort(intent.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{SelectedIPv4: intent.SuccessorProfile.SelectedIPv4,
			PortStart: intent.SuccessorProfile.PortStart, PortEnd: intent.SuccessorProfile.PortEnd, ProbeToken: probe},
		map[uint16]caddyV2LANAssignment{})
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes {
		clear(body)
		return nil, errors.New("invalid generated ingress typed stage config")
	}
	return body, nil
}

func gatewayRebindTypedStageResourceLabels(intent gatewayRebindProtectedIntentV2,
	managed, role string,
) map[string]string {
	return map[string]string{
		gatewayV2ManagedLabelKey:          managed,
		gatewayV2IdentityLabelKey:         gatewayRebindSuccessorIdentityVersion,
		gatewayV2OperationLabelKey:        intent.OperationID,
		gatewayV2IdentityDigestLabelKey:   intent.Identity.Digest,
		gatewayV2PlanDigestLabelKey:       intent.NetworkDigest,
		gatewayV2ResourceRoleLabelKey:     role,
		gatewayRebindIntentDigestLabelKey: intent.Digest,
		gatewayRebindGenerationLabelKey:   strconv.FormatUint(intent.Generation, 10),
	}
}

func gatewayRebindTypedStageBridgeName(intent gatewayRebindProtectedIntentV2) (string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || len(intent.Identity.Digest) < 12 {
		return "", errors.New("invalid generated ingress typed bridge identity")
	}
	return "rig" + intent.Identity.Digest[:12], nil
}

func gatewayRebindTypedStageNetworkOwnershipDigest(intent gatewayRebindProtectedIntentV2) (string, error) {
	bridgeName, err := gatewayRebindTypedStageBridgeName(intent)
	if err != nil {
		return "", err
	}
	return canonicalDigest(struct {
		Version int                                 `json:"version"`
		Name    string                              `json:"name"`
		Bridge  string                              `json:"bridge"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
		Labels  map[string]string                   `json:"labels"`
	}{1, intent.Identity.IngressNetwork, bridgeName, intent.Network,
		gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole)})
}

func gatewayRebindTypedStageVolumeOwnershipDigest(intent gatewayRebindProtectedIntentV2,
	name, role string,
) (string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || name == "" {
		return "", errors.New("invalid generated ingress typed volume ownership input")
	}
	return canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Driver  string            `json:"driver"`
		Scope   string            `json:"scope"`
		Options map[string]string `json:"options"`
		Labels  map[string]string `json:"labels"`
	}{1, name, "local", "local", map[string]string{},
		gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedContainerLabel, role)})
}

func gatewayRebindTypedStageContainerOwnershipDigest(intent gatewayRebindProtectedIntentV2,
	name, role string,
) (string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || name == "" {
		return "", errors.New("invalid generated ingress typed container ownership input")
	}
	return canonicalDigest(struct {
		Version int               `json:"version"`
		Name    string            `json:"name"`
		Labels  map[string]string `json:"labels"`
	}{1, name, gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedContainerLabel, role)})
}

func gatewayRebindTypedStageNetworkBindingMatches(intent gatewayRebindProtectedIntentV2,
	value gatewayRebindStageNetworkBinding,
) bool {
	digest, err := gatewayRebindTypedStageNetworkOwnershipDigest(intent)
	return err == nil && validContainerID(value.ID) && normalizeID(value.ID) == value.ID &&
		value.OwnershipDigest == digest
}

func gatewayRebindTypedStageNetworkBindingFor(intent gatewayRebindProtectedIntentV2,
	id string,
) (gatewayRebindStageNetworkBinding, error) {
	digest, err := gatewayRebindTypedStageNetworkOwnershipDigest(intent)
	value := gatewayRebindStageNetworkBinding{ID: normalizeID(id), OwnershipDigest: digest}
	if err != nil || !gatewayRebindTypedStageNetworkBindingMatches(intent, value) {
		return gatewayRebindStageNetworkBinding{}, errors.New("invalid generated ingress typed network binding")
	}
	return value, nil
}

func gatewayRebindTypedStageConfigVolumeBindingMatches(intent gatewayRebindProtectedIntentV2,
	value gatewayRebindStageConfigVolumeBinding,
) bool {
	digest, err := gatewayRebindTypedStageVolumeOwnershipDigest(intent,
		intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole)
	return err == nil && validGatewayRebindStageConfigVolumeBindingValue(value) &&
		value.Name == intent.Identity.ConfigVolume && value.OwnershipDigest == digest
}

func gatewayRebindTypedStageConfigVolumeBindingFor(intent gatewayRebindProtectedIntentV2,
	mountpoint, createdAt string,
) (gatewayRebindStageConfigVolumeBinding, error) {
	digest, err := gatewayRebindTypedStageVolumeOwnershipDigest(intent,
		intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole)
	value := gatewayRebindStageConfigVolumeBinding{Name: intent.Identity.ConfigVolume,
		Mountpoint: mountpoint, CreatedAt: createdAt, OwnershipDigest: digest}
	if err != nil || !gatewayRebindTypedStageConfigVolumeBindingMatches(intent, value) {
		return gatewayRebindStageConfigVolumeBinding{}, errors.New("invalid generated ingress typed config volume binding")
	}
	return value, nil
}

func gatewayRebindTypedStageDataVolumeBindingMatches(intent gatewayRebindProtectedIntentV2,
	value gatewayRebindStageDataVolumeBinding,
) bool {
	digest, err := gatewayRebindTypedStageVolumeOwnershipDigest(intent,
		intent.Identity.DataVolume, gatewayV2DataVolumeRole)
	return err == nil && validGatewayRebindStageDataVolumeBindingValue(value) &&
		value.Name == intent.Identity.DataVolume && value.OwnershipDigest == digest
}

func gatewayRebindTypedStageDataVolumeBindingFor(intent gatewayRebindProtectedIntentV2,
	mountpoint, createdAt string,
) (gatewayRebindStageDataVolumeBinding, error) {
	digest, err := gatewayRebindTypedStageVolumeOwnershipDigest(intent,
		intent.Identity.DataVolume, gatewayV2DataVolumeRole)
	value := gatewayRebindStageDataVolumeBinding{Name: intent.Identity.DataVolume,
		Mountpoint: mountpoint, CreatedAt: createdAt, OwnershipDigest: digest}
	if err != nil || !gatewayRebindTypedStageDataVolumeBindingMatches(intent, value) {
		return gatewayRebindStageDataVolumeBinding{}, errors.New("invalid generated ingress typed data volume binding")
	}
	return value, nil
}

func gatewayRebindTypedStageContainerCreateArgs(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress,
) ([]string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || effect.Network == nil || effect.ConfigVolume == nil ||
		effect.DataVolume == nil || !validSHA256(effect.ImageID) ||
		!gatewayRebindTypedStageNetworkBindingMatches(intent, *effect.Network) ||
		!gatewayRebindTypedStageConfigVolumeBindingMatches(intent, *effect.ConfigVolume) ||
		!gatewayRebindTypedStageDataVolumeBindingMatches(intent, *effect.DataVolume) {
		return nil, errors.New("invalid generated ingress typed stage container input")
	}
	args := []string{
		"container", "create", "--name", intent.Identity.StageContainer,
		"--hostname", intent.Identity.StageHostname,
		"--network", "name=" + intent.Identity.IngressNetwork + ",ip=" + intent.Network.ContainerIPv4 + ",gw-priority=1",
		"--mount", "type=volume,src=" + intent.Identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + intent.Identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", gatewayV2StageRestartPolicy,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for port := intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", intent.SuccessorProfile.SelectedIPv4+":"+value+":"+value+"/tcp")
		if port == intent.SuccessorProfile.PortEnd {
			break
		}
	}
	args = appendGatewayV2Labels(args, gatewayRebindTypedStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole))
	return append(args, "sha256:"+effect.ImageID, "run", "--config", "/config/"+intent.Identity.StageConfigFilename), nil
}

func gatewayRebindTypedStageContainerConfigurationDigest(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress,
) (string, error) {
	args, err := gatewayRebindTypedStageContainerCreateArgs(intent, effect)
	if err != nil {
		return "", err
	}
	return canonicalDigest(struct {
		Version int      `json:"version"`
		Args    []string `json:"args"`
	}{1, args})
}

func gatewayRebindTypedStageContainerBindingMatches(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, value gatewayRebindStageContainerBinding,
) bool {
	ownership, ownershipErr := gatewayRebindTypedStageContainerOwnershipDigest(intent,
		intent.Identity.StageContainer, gatewayV2StageContainerRole)
	configuration, configurationErr := gatewayRebindTypedStageContainerConfigurationDigest(intent, effect)
	return ownershipErr == nil && configurationErr == nil && validGatewayRebindStageContainerBindingValue(value) &&
		value.OwnershipDigest == ownership && value.ConfigurationDigest == configuration
}

func gatewayRebindTypedStageContainerBindingFor(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, id string,
) (gatewayRebindStageContainerBinding, error) {
	ownership, err := gatewayRebindTypedStageContainerOwnershipDigest(intent,
		intent.Identity.StageContainer, gatewayV2StageContainerRole)
	configuration, configurationErr := gatewayRebindTypedStageContainerConfigurationDigest(intent, effect)
	value := gatewayRebindStageContainerBinding{ID: normalizeID(id), OwnershipDigest: ownership,
		ConfigurationDigest: configuration}
	if err != nil || configurationErr != nil || !gatewayRebindTypedStageContainerBindingMatches(intent, effect, value) {
		return gatewayRebindStageContainerBinding{}, errors.New("invalid generated ingress typed stage container binding")
	}
	return value, nil
}

func gatewayRebindTypedFinalConfigRoutePlanBuild(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, heads []appaccess.GatewayRebindRuntimeHead,
) (gatewayRebindTypedFinalConfigRoutePlan, error) {
	invalid := errors.New("invalid generated ingress typed final route plan input")
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindPredecessorCheckpoint(checkpoint) ||
		intent.Predecessor != checkpoint.sourceRef() || !sameGatewayRebindRuntimeHeads(heads, intent.RuntimeHeads) {
		return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
	}
	apps, profile, ok := gatewayRebindTypedCheckpointApps(checkpoint)
	if !ok || len(apps) != len(heads) {
		return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
	}
	roster := make(map[string]appaccess.GatewayRebindRosterEntryV2, len(intent.Roster))
	for _, entry := range intent.Roster {
		if _, duplicate := roster[entry.AppID]; duplicate {
			return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
		}
		roster[entry.AppID] = entry
	}
	appIDs := make([]string, 0, len(apps))
	for appID := range apps {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)
	routes := make([]gatewayRebindTypedFinalConfigRouteBinding, 0, len(appIDs))
	lanCount := 0
	for index, appID := range appIDs {
		if index >= len(heads) || heads[index].AppID != appID || !validGatewayRebindRuntimeHead(heads[index]) {
			return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
		}
		app := apps[appID]
		if string(app.Route.Slot) != heads[index].Slot {
			return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
		}
		entry, listed := roster[appID]
		if app.LAN == nil {
			if listed {
				return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
			}
		} else {
			lanCount++
			if !listed || !gatewayRebindTypedRosterMatchesBinding(entry, *app.LAN, heads[index], profile) ||
				app.LAN.Raw.Port < intent.SuccessorProfile.PortStart || app.LAN.Raw.Port > intent.SuccessorProfile.PortEnd {
				return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
			}
		}
		binding := gatewayRebindTypedFinalConfigRouteBinding{AppID: appID, Route: app.Route,
			SourceLAN: cloneGatewayCurrentLANBindingPointer(app.LAN), RuntimeHead: gatewayRebindFinalConfigRuntimeHead{
				AppID: appID, DeploymentID: heads[index].DeploymentID, ReleaseID: heads[index].ReleaseID,
				Slot: heads[index].Slot, Generation: heads[index].Generation,
				UpdatedAt: heads[index].UpdatedAt.UTC().Format(time.RFC3339Nano),
			}}
		var err error
		binding.SourceBindingDigest, err = gatewayRebindTypedFinalConfigSourceBindingDigest(binding)
		if err != nil {
			return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
		}
		routes = append(routes, binding)
	}
	if lanCount != len(roster) {
		return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(intent.Claim.Spec)
	if err != nil {
		return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
	}
	predecessorIngressNetwork, ok := gatewayRebindTypedCheckpointIngressNetwork(checkpoint)
	if !ok {
		return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
	}
	plan := gatewayRebindTypedFinalConfigRoutePlan{Version: gatewayRebindTypedFinalConfigPlanVersion,
		Context: gatewayRebindTypedFinalConfigPlanContext, OperationID: intent.OperationID, Predecessor: intent.Predecessor,
		ClaimSpecDigest: specDigest, PredecessorIngressNetwork: predecessorIngressNetwork,
		EffectiveSuccessorProfile: intent.SuccessorProfile,
		SuccessorIdentity:         intent.Identity, Routes: routes}
	plan.RuntimeHeadsDigest, err = appaccess.GatewayRebindRuntimeHeadsV2Digest(intent.OperationID, heads)
	if err == nil {
		plan.RouteMapDigest, err = gatewayRebindTypedFinalConfigRouteMapDigest(routes)
	}
	if err == nil {
		plan.Digest, err = gatewayRebindTypedFinalConfigRoutePlanDigest(plan)
	}
	if err != nil {
		return gatewayRebindTypedFinalConfigRoutePlan{}, invalid
	}
	return plan, nil
}

func gatewayRebindTypedCheckpointIngressNetwork(checkpoint gatewayRebindPredecessorCheckpoint) (string, bool) {
	if checkpoint.UpgradeState != nil {
		value := checkpoint.UpgradeState.Identity.IngressNetwork
		return value, value != ""
	}
	if checkpoint.CurrentState == nil {
		return "", false
	}
	identity := checkpoint.CurrentState.Identity
	switch {
	case identity.Upgrade != nil:
		return identity.Upgrade.IngressNetwork, identity.Upgrade.IngressNetwork != ""
	case identity.Rebind != nil:
		return identity.Rebind.IngressNetwork, identity.Rebind.IngressNetwork != ""
	default:
		return "", false
	}
}

func gatewayRebindTypedFinalConfigRoutePlanFor(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, heads []appaccess.GatewayRebindRuntimeHead,
) (gatewayRebindTypedFinalConfigRoutePlan, error) {
	plan, err := gatewayRebindTypedFinalConfigRoutePlanBuild(intent, checkpoint, heads)
	if err != nil || !validGatewayRebindTypedFinalConfigRoutePlanValue(intent, checkpoint, plan) {
		return gatewayRebindTypedFinalConfigRoutePlan{}, errors.New("invalid generated ingress typed final route plan input")
	}
	return plan, nil
}

func gatewayRebindTypedCheckpointApps(checkpoint gatewayRebindPredecessorCheckpoint) (map[string]gatewayCurrentAppRoute,
	gatewayProfileBinding, bool,
) {
	if checkpoint.UpgradeState != nil {
		state := checkpoint.UpgradeState
		apps := make(map[string]gatewayCurrentAppRoute, len(state.Apps))
		for appID, app := range state.Apps {
			current := gatewayCurrentAppRoute{Route: app.Route}
			if app.LAN != nil {
				raw := *app.LAN
				current.LAN = &gatewayCurrentLANBinding{Raw: raw}
			}
			apps[appID] = current
		}
		return apps, state.Profile, true
	}
	if checkpoint.CurrentState != nil {
		state := checkpoint.CurrentState
		apps := make(map[string]gatewayCurrentAppRoute, len(state.Apps))
		for appID, app := range state.Apps {
			copy := gatewayCurrentAppRoute{Route: app.Route, LAN: cloneGatewayCurrentLANBindingPointer(app.LAN)}
			apps[appID] = copy
		}
		return apps, state.Profile, true
	}
	return nil, gatewayProfileBinding{}, false
}

func cloneGatewayCurrentLANBindingPointer(value *gatewayCurrentLANBinding) *gatewayCurrentLANBinding {
	if value == nil {
		return nil
	}
	result := *value
	if value.Transfer != nil {
		transfer := *value.Transfer
		result.Transfer = &transfer
	}
	return &result
}

func validGatewayRebindRuntimeHead(value appaccess.GatewayRebindRuntimeHead) bool {
	return validCanonicalUUID(value.AppID) && validCanonicalUUID(value.DeploymentID) &&
		validGatewayRebindRuntimeReleaseID(value.ReleaseID) && (value.Slot == "blue" || value.Slot == "green") &&
		value.Generation > 0 && !value.UpdatedAt.IsZero() && value.UpdatedAt.UTC() == value.UpdatedAt
}

func sameGatewayRebindRuntimeHeads(left, right []appaccess.GatewayRebindRuntimeHead) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].AppID != right[index].AppID ||
			left[index].DeploymentID != right[index].DeploymentID ||
			left[index].ReleaseID != right[index].ReleaseID ||
			left[index].Slot != right[index].Slot ||
			left[index].Generation != right[index].Generation ||
			!left[index].UpdatedAt.Equal(right[index].UpdatedAt) {
			return false
		}
	}
	return true
}

func gatewayRebindTypedRosterMatchesBinding(entry appaccess.GatewayRebindRosterEntryV2,
	binding gatewayCurrentLANBinding, head appaccess.GatewayRebindRuntimeHead, profile gatewayProfileBinding,
) bool {
	raw := binding.Raw
	if entry.AppID != head.AppID || entry.AllocationID != raw.AllocationID || entry.Port != raw.Port ||
		entry.AllocationOwnerOperationID != raw.OwnerOperationID || entry.AccessRevisionID != raw.AccessRevisionID ||
		entry.AccessRevisionNumber != raw.AccessRevisionNumber || entry.AccessSpecDigest != raw.AccessSpecDigest ||
		entry.GrantAttemptID != raw.GrantAttemptID || entry.SourceProfileRevisionID != raw.ProfileRevisionID ||
		entry.SourceProfileRevisionNumber != raw.ProfileRevisionNumber || entry.SourceProfileSpecDigest != raw.ProfileSpecDigest ||
		entry.ServingDeploymentID != head.DeploymentID || entry.ServingReleaseID != head.ReleaseID ||
		entry.ServingSlot != head.Slot || entry.RouteGeneration != head.Generation {
		return false
	}
	if binding.Transfer == nil {
		return entry.PredecessorTransferDigest == nil && raw.ProfileRevisionID == profile.RevisionID &&
			raw.ProfileRevisionNumber == profile.RevisionNumber && raw.ProfileSpecDigest == profile.SpecDigest
	}
	transfer := binding.Transfer
	return entry.PredecessorTransferDigest != nil && *entry.PredecessorTransferDigest == transfer.TransferDigest &&
		transfer.SuccessorProfileRevisionID == profile.RevisionID &&
		transfer.SuccessorProfileRevisionNumber == profile.RevisionNumber &&
		transfer.SuccessorProfileSpecDigest == profile.SpecDigest
}

func validGatewayRebindTypedFinalConfigRoutePlanValue(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, value gatewayRebindTypedFinalConfigRoutePlan,
) bool {
	if value.Version != gatewayRebindTypedFinalConfigPlanVersion || value.Context != gatewayRebindTypedFinalConfigPlanContext ||
		value.OperationID != intent.OperationID || value.Predecessor != intent.Predecessor ||
		value.EffectiveSuccessorProfile != intent.SuccessorProfile ||
		value.SuccessorIdentity != intent.Identity || !validSHA256(value.ClaimSpecDigest) ||
		!validSHA256(value.RuntimeHeadsDigest) || !validSHA256(value.RouteMapDigest) || !validSHA256(value.Digest) {
		return false
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(intent.Claim.Spec)
	if err != nil || value.ClaimSpecDigest != specDigest {
		return false
	}
	heads := make([]appaccess.GatewayRebindRuntimeHead, len(value.Routes))
	for index, route := range value.Routes {
		updatedAt, err := time.Parse(time.RFC3339Nano, route.RuntimeHead.UpdatedAt)
		if err != nil {
			return false
		}
		heads[index] = appaccess.GatewayRebindRuntimeHead{AppID: route.RuntimeHead.AppID,
			DeploymentID: route.RuntimeHead.DeploymentID, ReleaseID: route.RuntimeHead.ReleaseID,
			Slot: route.RuntimeHead.Slot, Generation: route.RuntimeHead.Generation, UpdatedAt: updatedAt}
		digest, digestErr := gatewayRebindTypedFinalConfigSourceBindingDigest(route)
		if digestErr != nil || digest != route.SourceBindingDigest {
			return false
		}
	}
	expected, err := gatewayRebindTypedFinalConfigRoutePlanBuild(intent, checkpoint, heads)
	return err == nil && reflect.DeepEqual(expected, value)
}

func validGatewayRebindTypedFinalConfigIntentBindingValue(value gatewayRebindTypedFinalConfigIntentBinding) bool {
	if value.Version != gatewayRebindTypedFinalConfigIntentVersion || value.Context != gatewayRebindTypedFinalConfigIntentContext ||
		!validGatewayRebindTypedFinalConfigRoutePlanShape(value.RoutePlan) ||
		!validSHA256(value.ContentDigest) || value.ContentLength <= 0 || value.ContentLength > gatewayV2MaxConfigBytes ||
		value.Destination != "/config/"+gatewayV2ActiveConfigFile || !validSHA256(value.SuccessorIdentityDigest) ||
		!validSHA256(value.ProtectedIntentDigest) || !validSHA256(value.ProtectedPredecessorDigest) ||
		!validGatewayRebindStageServingBindingValue(value.StageServing) ||
		!validSHA256(value.PriorProgressDigest) || !validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindTypedFinalConfigIntentDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindTypedHandoverIntentValue(value gatewayRebindTypedHandoverIntent) bool {
	if value.Version != gatewayRebindTypedHandoverIntentVersion || value.Context != gatewayRebindTypedHandoverIntentContext ||
		!validGatewayRebindFinalHandoverPlanValue(value.Plan) || !validSHA256(value.FinalConfigIntentDigest) ||
		!validSHA256(value.FinalConfigCopyDigest) || !validGatewayRebindSourceRef(value.Predecessor) ||
		value.PredecessorCheckpointDigest != value.Predecessor.PredecessorCheckpointDigest ||
		!validSHA256(value.PriorProgressDigest) || !validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindTypedHandoverIntentDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindTypedCutoverIntentValue(value gatewayRebindTypedCutoverIntent) bool {
	if value.Version != gatewayRebindTypedCutoverIntentVersion || value.Context != gatewayRebindTypedCutoverIntentContext ||
		!validSHA256(value.HandoverIntentDigest) || !validSHA256(value.FinalBindingDigest) ||
		!validGatewayRebindSourceRef(value.Predecessor) ||
		value.PredecessorCheckpointDigest != value.Predecessor.PredecessorCheckpointDigest ||
		!validSHA256(value.PreparationObservationDigest) || !validSHA256(value.PriorProgressDigest) ||
		!validSHA256(value.Digest) {
		return false
	}
	digest, err := gatewayRebindTypedCutoverIntentDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindSourceRef(value appaccess.GatewayRebindSourceRef) bool {
	return validGatewayCurrentLineage(value.Lineage) && value.SourceStateVersion > 0 &&
		validSHA256(value.SourceStateDigest) && validSHA256(value.PredecessorCheckpointDigest)
}

func gatewayRebindTypedStageConfigIntentFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress,
) (gatewayRebindStageConfigIntentBinding, error) {
	if previous.Sequence != 6 || effect.StageContainer == nil || effect.ConfigVolume == nil ||
		effect.StageConfigIntent != nil {
		return gatewayRebindStageConfigIntentBinding{}, errors.New("invalid generated ingress typed stage config input")
	}
	body, err := gatewayRebindTypedStageConfigBytes(intent)
	if err != nil {
		return gatewayRebindStageConfigIntentBinding{}, err
	}
	defer clear(body)
	predecessorDigest, err := gatewayRebindTypedPredecessorDigest(intent)
	if err != nil {
		return gatewayRebindStageConfigIntentBinding{}, err
	}
	digest := sha256.Sum256(body)
	value := gatewayRebindStageConfigIntentBinding{ContentDigest: hex.EncodeToString(digest[:]),
		ContentLength: int64(len(body)), Destination: "/config/" + intent.Identity.StageConfigFilename,
		StageContainer: *effect.StageContainer, ConfigVolume: *effect.ConfigVolume,
		ProtectedIntentDigest: intent.Digest, ProtectedPredecessorDigest: predecessorDigest,
		PriorProgressDigest: previous.Digest}
	if !validGatewayRebindStageConfigIntentBindingValue(value) {
		return gatewayRebindStageConfigIntentBinding{}, errors.New("invalid generated ingress typed stage config input")
	}
	return value, nil
}

func gatewayRebindTypedStageConfigCopyFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress,
) (gatewayRebindStageConfigCopyBinding, error) {
	if previous.Sequence != 7 || effect.StageConfigIntent == nil || effect.StageConfigCopy != nil {
		return gatewayRebindStageConfigCopyBinding{}, errors.New("invalid generated ingress typed stage copy input")
	}
	value := gatewayRebindStageConfigCopyBinding{StageConfigIntent: *effect.StageConfigIntent,
		PriorProgressDigest: previous.Digest}
	if !validGatewayRebindStageConfigCopyBindingValue(value) {
		return gatewayRebindStageConfigCopyBinding{}, errors.New("invalid generated ingress typed stage copy input")
	}
	return value, nil
}

func gatewayRebindTypedStageStartIntentFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress,
) (gatewayRebindStageStartIntentBinding, error) {
	if previous.Sequence != 8 || effect.StageConfigCopy == nil || effect.StageContainer == nil ||
		effect.Network == nil || effect.StageStartIntent != nil {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress typed stage start input")
	}
	probe, err := gatewayRebindTypedStageConfigProbeToken(intent)
	if err != nil {
		return gatewayRebindStageStartIntentBinding{}, err
	}
	predecessorDigest, err := gatewayRebindTypedPredecessorDigest(intent)
	if err != nil {
		return gatewayRebindStageStartIntentBinding{}, err
	}
	value := gatewayRebindStageStartIntentBinding{Version: gatewayRebindStageStartIntentVersion,
		StageConfigCopy: *effect.StageConfigCopy, StageContainer: *effect.StageContainer, Network: *effect.Network,
		InterfaceID: intent.SuccessorProfile.InterfaceID, SelectedIPv4: intent.SuccessorProfile.SelectedIPv4,
		PortStart: intent.SuccessorProfile.PortStart, PortEnd: intent.SuccessorProfile.PortEnd,
		ContainerIPv4: intent.Network.ContainerIPv4, ProbeToken: probe,
		ProtectedIntentDigest: intent.Digest, ProtectedPredecessorDigest: predecessorDigest,
		PriorProgressDigest: previous.Digest}
	value.StartEffectDigest, err = gatewayRebindStageStartEffectDigest(value)
	if err != nil || !validGatewayRebindStageStartIntentBindingValue(value) {
		return gatewayRebindStageStartIntentBinding{}, errors.New("invalid generated ingress typed stage start input")
	}
	return value, nil
}

func gatewayRebindTypedStageServingFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress, endpointID string,
) (gatewayRebindStageServingBinding, error) {
	if previous.Sequence != 9 || effect.StageStartIntent == nil || effect.StageConfigIntent == nil ||
		effect.StageServing != nil || !validContainerID(endpointID) || normalizeID(endpointID) != endpointID {
		return gatewayRebindStageServingBinding{}, errors.New("invalid generated ingress typed stage serving input")
	}
	bindingDigest, err := gatewayRebindStageEffectivePortBindingsDigest(*effect.StageStartIntent)
	if err != nil {
		return gatewayRebindStageServingBinding{}, err
	}
	value := gatewayRebindStageServingBinding{Version: gatewayRebindStageServingVersion,
		StageStartIntent: *effect.StageStartIntent, EndpointID: endpointID,
		EffectivePortBindingsDigest: bindingDigest, ConfigDigest: effect.StageConfigIntent.ContentDigest,
		ProtectedIntentDigest: intent.Digest, PriorProgressDigest: previous.Digest}
	if !validGatewayRebindStageServingBindingValue(value) {
		return gatewayRebindStageServingBinding{}, errors.New("invalid generated ingress typed stage serving input")
	}
	return value, nil
}

func gatewayRebindTypedFinalConfigBytes(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, plan gatewayRebindTypedFinalConfigRoutePlan,
) ([]byte, error) {
	if !validGatewayRebindTypedFinalConfigRoutePlanValue(intent, checkpoint, plan) {
		return nil, errors.New("invalid generated ingress typed final config plan")
	}
	return gatewayRebindTypedFinalConfigBytesForPlan(intent, plan)
}

func gatewayRebindTypedFinalConfigBytesForPlan(intent gatewayRebindProtectedIntentV2,
	plan gatewayRebindTypedFinalConfigRoutePlan,
) ([]byte, error) {
	if !gatewayRebindTypedFinalRoutePlanMatchesIntent(intent, plan) {
		return nil, errors.New("invalid generated ingress typed final config plan")
	}
	routes := make(map[string]routeRecord, len(plan.Routes))
	assignments := make(map[uint16]caddyV2LANAssignment, len(intent.Roster))
	for _, binding := range plan.Routes {
		routes[binding.AppID] = binding.Route
		if binding.SourceLAN != nil {
			raw := binding.SourceLAN.Raw
			assignments[raw.Port] = caddyV2LANAssignment{AppID: binding.AppID, AllocationID: raw.AllocationID,
				AccessRevisionID: raw.AccessRevisionID, AccessRevisionNumber: raw.AccessRevisionNumber,
				AccessSpecDigest: raw.AccessSpecDigest}
		}
	}
	probe, err := gatewayRebindTypedStageConfigProbeToken(intent)
	if err != nil {
		return nil, err
	}
	body, err := buildCaddyConfigV2(routes,
		net.JoinHostPort(intent.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)),
		caddyV2Profile{SelectedIPv4: plan.EffectiveSuccessorProfile.SelectedIPv4,
			PortStart: plan.EffectiveSuccessorProfile.PortStart, PortEnd: plan.EffectiveSuccessorProfile.PortEnd,
			ProbeToken: probe}, assignments)
	if err != nil || len(body) == 0 || len(body) > gatewayV2MaxConfigBytes {
		clear(body)
		return nil, errors.New("invalid generated ingress typed final config")
	}
	return body, nil
}

func gatewayRebindTypedFinalConfigIntentFor(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, previous gatewayRebindProgressRecord,
	effect gatewayRebindTypedEffectProgress, heads []appaccess.GatewayRebindRuntimeHead,
) (gatewayRebindTypedFinalConfigIntentBinding, error) {
	if previous.Sequence != 10 || effect.StageServing == nil || effect.FinalConfigIntent != nil {
		return gatewayRebindTypedFinalConfigIntentBinding{}, errors.New("invalid generated ingress typed final config input")
	}
	plan, err := gatewayRebindTypedFinalConfigRoutePlanFor(intent, checkpoint, heads)
	if err != nil {
		return gatewayRebindTypedFinalConfigIntentBinding{}, err
	}
	body, err := gatewayRebindTypedFinalConfigBytes(intent, checkpoint, plan)
	if err != nil {
		return gatewayRebindTypedFinalConfigIntentBinding{}, err
	}
	defer clear(body)
	predecessorDigest, err := gatewayRebindTypedPredecessorDigest(intent)
	if err != nil {
		return gatewayRebindTypedFinalConfigIntentBinding{}, err
	}
	digest := sha256.Sum256(body)
	value := gatewayRebindTypedFinalConfigIntentBinding{Version: gatewayRebindTypedFinalConfigIntentVersion,
		Context: gatewayRebindTypedFinalConfigIntentContext, RoutePlan: plan,
		ContentDigest: hex.EncodeToString(digest[:]), ContentLength: int64(len(body)),
		Destination:             "/config/" + intent.Identity.ActiveConfigFilename,
		SuccessorIdentityDigest: intent.Identity.Digest, ProtectedIntentDigest: intent.Digest,
		ProtectedPredecessorDigest: predecessorDigest, StageServing: *effect.StageServing,
		PriorProgressDigest: previous.Digest}
	value.Digest, err = gatewayRebindTypedFinalConfigIntentDigest(value)
	if err != nil || !validGatewayRebindTypedFinalConfigIntentBindingValue(value) {
		return gatewayRebindTypedFinalConfigIntentBinding{}, errors.New("invalid generated ingress typed final config input")
	}
	return value, nil
}

func gatewayRebindTypedFinalConfigCopyFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress,
) (gatewayRebindFinalConfigCopyBinding, error) {
	if previous.Sequence != 11 || effect.FinalConfigIntent == nil || effect.FinalConfigCopy != nil {
		return gatewayRebindFinalConfigCopyBinding{}, errors.New("invalid generated ingress typed final config copy input")
	}
	predecessorDigest, err := gatewayRebindTypedPredecessorDigest(intent)
	if err != nil {
		return gatewayRebindFinalConfigCopyBinding{}, err
	}
	value := gatewayRebindFinalConfigCopyBinding{Version: gatewayRebindFinalConfigCopyVersion,
		Context: gatewayRebindFinalConfigCopyContext, FinalConfigIntentDigest: effect.FinalConfigIntent.Digest,
		ProtectedIntentDigest: intent.Digest, ProtectedPredecessorDigest: predecessorDigest,
		SuccessorIdentityDigest: intent.Identity.Digest, SequenceElevenDigest: previous.Digest,
		PriorProgressDigest: previous.Digest}
	value.CopyEffectDigest, err = gatewayRebindFinalConfigCopyEffectDigest(value)
	if err != nil || !validGatewayRebindFinalConfigCopyBindingValue(value) {
		return gatewayRebindFinalConfigCopyBinding{}, errors.New("invalid generated ingress typed final config copy input")
	}
	return value, nil
}

func gatewayRebindTypedFinalContainerCreateArgs(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, localHostPort uint16,
	applicationNetworks []gatewayRebindHandoverApplicationNetwork,
) ([]string, error) {
	if !validGatewayRebindProtectedIntentV2(intent) || effect.StageContainer == nil ||
		!gatewayRebindTypedStageContainerBindingMatches(intent, effect, *effect.StageContainer) ||
		effect.FinalConfigIntent == nil || localHostPort == 0 ||
		!validGatewayRebindTypedHandoverApplicationNetworks(effect.FinalConfigIntent.RoutePlan, applicationNetworks) {
		return nil, errors.New("invalid generated ingress typed final container input")
	}
	args := []string{
		"container", "create", "--name", intent.Identity.FinalContainer,
		"--hostname", intent.Identity.FinalHostname,
		"--network", "name=" + intent.Identity.IngressNetwork + ",ip=" + intent.Network.ContainerIPv4 + ",gw-priority=1",
		"--mount", "type=volume,src=" + intent.Identity.ConfigVolume + ",dst=/config",
		"--mount", "type=volume,src=" + intent.Identity.DataVolume + ",dst=/data",
		"--user", "1000:1000", "--entrypoint", caddyExecutable, "--read-only",
		"--cap-drop", "ALL", "--cap-add", caddyCapability, "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128",
		"--ulimit", "nofile=1024:1024", "--restart", gatewayV2FinalRestartPolicy,
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
	}
	for _, network := range applicationNetworks {
		args = append(args, "--network", "name="+network.Name)
	}
	for port := intent.SuccessorProfile.PortStart; ; port++ {
		portText := strconv.FormatUint(uint64(port), 10)
		args = append(args, "--publish", intent.SuccessorProfile.SelectedIPv4+":"+portText+":"+portText+"/tcp")
		if port == intent.SuccessorProfile.PortEnd {
			break
		}
	}
	args = append(args, "--publish", "127.0.0.1:"+strconv.FormatUint(uint64(localHostPort), 10)+":"+
		strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)+"/tcp")
	args = appendGatewayV2Labels(args, gatewayRebindTypedStageResourceLabels(intent,
		gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole))
	return append(args, "sha256:"+effect.ImageID, "run", "--config", "/config/"+intent.Identity.ActiveConfigFilename), nil
}

func validGatewayRebindTypedHandoverApplicationNetworks(plan gatewayRebindTypedFinalConfigRoutePlan,
	networks []gatewayRebindHandoverApplicationNetwork,
) bool {
	if !validGatewayRebindTypedFinalConfigRoutePlanShape(plan) {
		return false
	}
	routes := make(map[string]routeRecord, len(plan.Routes))
	for _, binding := range plan.Routes {
		routes[binding.AppID] = binding.Route
	}
	owners, valid := gatewayRouteNetworkOwners(routes)
	if !valid || len(networks) != len(owners) {
		return false
	}
	ids := make(map[string]bool, len(networks))
	for index, network := range networks {
		if _, exists := owners[network.Name]; !exists || network.Name == plan.SuccessorIdentity.IngressNetwork ||
			network.Name == plan.PredecessorIngressNetwork || !validContainerID(network.ID) ||
			normalizeID(network.ID) != network.ID || ids[network.ID] ||
			(index > 0 && networks[index-1].Name >= network.Name) {
			return false
		}
		ids[network.ID] = true
	}
	return true
}

func gatewayRebindTypedFinalContainerConfigurationDigest(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, localHostPort uint16,
	applicationNetworks []gatewayRebindHandoverApplicationNetwork,
) (string, error) {
	args, err := gatewayRebindTypedFinalContainerCreateArgs(intent, effect, localHostPort, applicationNetworks)
	if err != nil {
		return "", err
	}
	return canonicalDigest(struct {
		Version int      `json:"version"`
		Args    []string `json:"args"`
	}{1, args})
}

func gatewayRebindTypedFinalContainerBindingFor(intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, plan gatewayRebindFinalHandoverPlan, id string,
) (gatewayRebindFinalContainerBinding, error) {
	ownership, err := gatewayRebindTypedStageContainerOwnershipDigest(intent,
		intent.Identity.FinalContainer, gatewayV2FinalContainerRole)
	configuration, configurationErr := gatewayRebindTypedFinalContainerConfigurationDigest(intent,
		effect, plan.LocalHostPort, plan.ApplicationNetworks)
	value := gatewayRebindFinalContainerBinding{ID: normalizeID(id), OwnershipDigest: ownership,
		ConfigurationDigest: configuration}
	if err != nil || configurationErr != nil || !validGatewayRebindFinalContainerBindingValue(value) ||
		effect.StageContainer == nil || value.ID == effect.StageContainer.ID ||
		value.OwnershipDigest != plan.FinalOwnershipDigest ||
		value.ConfigurationDigest != plan.FinalConfigurationDigest {
		return gatewayRebindFinalContainerBinding{}, errors.New("invalid generated ingress typed final container binding")
	}
	return value, nil
}

func gatewayRebindTypedHandoverIntentFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress,
	localHostPort uint16, predecessorObservationDigest string, predecessorInitiallyRunning bool,
	applicationNetworks []gatewayRebindHandoverApplicationNetwork,
) (gatewayRebindTypedHandoverIntent, error) {
	if previous.Sequence != 12 || effect.FinalConfigIntent == nil || effect.FinalConfigCopy == nil ||
		effect.HandoverIntent != nil || localHostPort == 0 || !validSHA256(predecessorObservationDigest) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover prerequisite")
	}
	networks := append([]gatewayRebindHandoverApplicationNetwork(nil), applicationNetworks...)
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	if !validGatewayRebindTypedHandoverApplicationNetworks(effect.FinalConfigIntent.RoutePlan, networks) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover networks")
	}
	predecessorDigest, err := gatewayRebindTypedPredecessorDigest(intent)
	if err != nil {
		return gatewayRebindTypedHandoverIntent{}, err
	}
	finalOwnershipDigest, err := gatewayRebindTypedStageContainerOwnershipDigest(intent,
		intent.Identity.FinalContainer, gatewayV2FinalContainerRole)
	if err != nil {
		return gatewayRebindTypedHandoverIntent{}, err
	}
	finalConfigurationDigest, err := gatewayRebindTypedFinalContainerConfigurationDigest(intent,
		effect, localHostPort, networks)
	if err != nil {
		return gatewayRebindTypedHandoverIntent{}, err
	}
	plan := gatewayRebindFinalHandoverPlan{PredecessorObservationDigest: predecessorObservationDigest,
		PredecessorInitiallyRunning: predecessorInitiallyRunning, Version: gatewayRebindFinalHandoverProgressVersion,
		Context: gatewayRebindFinalHandoverPlanContext, ProtectedIntentDigest: intent.Digest,
		SequenceTwelveDigest: previous.Digest, PredecessorDigest: predecessorDigest,
		RoutePlanDigest:      effect.FinalConfigIntent.RoutePlan.Digest,
		FinalConfigDigest:    effect.FinalConfigIntent.ContentDigest,
		FinalOwnershipDigest: finalOwnershipDigest, FinalConfigurationDigest: finalConfigurationDigest,
		LocalHostPort: localHostPort, ApplicationNetworks: networks}
	plan.Digest, err = gatewayRebindFinalHandoverPlanDigest(plan)
	if err != nil || !validGatewayRebindFinalHandoverPlanValue(plan) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover plan")
	}
	value := gatewayRebindTypedHandoverIntent{Version: gatewayRebindTypedHandoverIntentVersion,
		Context: gatewayRebindTypedHandoverIntentContext, Plan: plan,
		FinalConfigIntentDigest: effect.FinalConfigIntent.Digest,
		FinalConfigCopyDigest:   effect.FinalConfigCopy.CopyEffectDigest,
		Predecessor:             intent.Predecessor, PredecessorCheckpointDigest: intent.Predecessor.PredecessorCheckpointDigest,
		PriorProgressDigest: previous.Digest}
	value.Digest, err = gatewayRebindTypedHandoverIntentDigest(value)
	if err != nil {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover digest")
	}
	if !validGatewayRebindSourceRef(value.Predecessor) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover predecessor")
	}
	if !validSHA256(value.FinalConfigIntentDigest) || !validSHA256(value.FinalConfigCopyDigest) ||
		value.PredecessorCheckpointDigest != value.Predecessor.PredecessorCheckpointDigest ||
		!validSHA256(value.PriorProgressDigest) || !validSHA256(value.Digest) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover scalar binding")
	}
	if !validGatewayRebindFinalHandoverPlanValue(value.Plan) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover retained plan")
	}
	if repeated, repeatedErr := gatewayRebindTypedHandoverIntentDigest(value); repeatedErr != nil || repeated != value.Digest {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover self digest")
	}
	if !validGatewayRebindTypedHandoverIntentValue(value) {
		return gatewayRebindTypedHandoverIntent{}, errors.New("invalid generated ingress typed handover input")
	}
	return value, nil
}

func gatewayRebindTypedCutoverIntentFor(intent gatewayRebindProtectedIntentV2,
	previous gatewayRebindProgressRecord, effect gatewayRebindTypedEffectProgress,
	preparationObservationDigest string,
) (gatewayRebindTypedCutoverIntent, error) {
	if previous.Sequence != 14 || effect.HandoverIntent == nil || effect.FinalContainer == nil ||
		effect.CutoverIntent != nil || !validSHA256(preparationObservationDigest) {
		return gatewayRebindTypedCutoverIntent{}, errors.New("invalid generated ingress typed cutover input")
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*effect.FinalContainer)
	if err != nil {
		return gatewayRebindTypedCutoverIntent{}, err
	}
	value := gatewayRebindTypedCutoverIntent{Version: gatewayRebindTypedCutoverIntentVersion,
		Context: gatewayRebindTypedCutoverIntentContext, HandoverIntentDigest: effect.HandoverIntent.Digest,
		FinalBindingDigest: finalDigest, Predecessor: intent.Predecessor,
		PredecessorCheckpointDigest:  intent.Predecessor.PredecessorCheckpointDigest,
		PreparationObservationDigest: preparationObservationDigest, PriorProgressDigest: previous.Digest}
	value.Digest, err = gatewayRebindTypedCutoverIntentDigest(value)
	if err != nil || !validGatewayRebindTypedCutoverIntentValue(value) {
		return gatewayRebindTypedCutoverIntent{}, errors.New("invalid generated ingress typed cutover input")
	}
	return value, nil
}

func gatewayRebindTypedSuccessorServingFor(previous gatewayRebindProgressRecord,
	effect gatewayRebindTypedEffectProgress, observation gatewayRebindFinalHandoverObservation,
) (gatewayRebindFinalHandoverServingProof, error) {
	if previous.Sequence != 15 || effect.HandoverIntent == nil || effect.CutoverIntent == nil ||
		effect.FinalContainer == nil || effect.SuccessorServing != nil ||
		!validGatewayRebindFinalHandoverObservationValue(observation) {
		return gatewayRebindFinalHandoverServingProof{}, errors.New("invalid generated ingress typed successor serving input")
	}
	finalDigest, err := gatewayRebindFinalContainerBindingDigest(*effect.FinalContainer)
	if err != nil {
		return gatewayRebindFinalHandoverServingProof{}, err
	}
	value := gatewayRebindFinalHandoverServingProof{Version: gatewayRebindFinalHandoverProgressVersion,
		Context: gatewayRebindFinalHandoverServingContext, PlanDigest: effect.HandoverIntent.Plan.Digest,
		FinalBindingDigest: finalDigest, CutoverIntentDigest: effect.CutoverIntent.Digest,
		PriorProgressDigest: previous.Digest, Observation: observation}
	value.Digest, err = gatewayRebindFinalHandoverServingDigest(value)
	if err != nil || !validGatewayRebindFinalHandoverServingValue(value) {
		return gatewayRebindFinalHandoverServingProof{}, errors.New("invalid generated ingress typed successor serving input")
	}
	return value, nil
}

func gatewayRebindTypedProgressMatchesCheckpoint(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, progress []gatewayRebindProgressRecord,
) bool {
	if !validGatewayRebindPredecessorCheckpoint(checkpoint) || checkpoint.sourceRef() != intent.Predecessor {
		return false
	}
	for _, record := range progress {
		if record.TypedRollback != nil {
			continue
		}
		if record.Sequence < 11 {
			continue
		}
		if record.TypedEffect == nil || record.TypedEffect.FinalConfigIntent == nil {
			return false
		}
		plan := record.TypedEffect.FinalConfigIntent.RoutePlan
		heads := make([]appaccess.GatewayRebindRuntimeHead, len(plan.Routes))
		for index, route := range plan.Routes {
			updatedAt, err := time.Parse(time.RFC3339Nano, route.RuntimeHead.UpdatedAt)
			if err != nil {
				return false
			}
			heads[index] = appaccess.GatewayRebindRuntimeHead{AppID: route.RuntimeHead.AppID,
				DeploymentID: route.RuntimeHead.DeploymentID, ReleaseID: route.RuntimeHead.ReleaseID,
				Slot: route.RuntimeHead.Slot, Generation: route.RuntimeHead.Generation, UpdatedAt: updatedAt}
		}
		expected, err := gatewayRebindTypedFinalConfigRoutePlanBuild(intent, checkpoint, heads)
		if err != nil || !reflect.DeepEqual(expected, plan) {
			return false
		}
	}
	return true
}

func gatewayRebindTypedEffectSemanticMatch(value gatewayRebindTypedEffectProgress,
	intent gatewayRebindProtectedIntentV2, prior gatewayRebindProgressRecord,
) bool {
	if prior.Sequence == 1 {
		return value.Network == nil && value.StageConfigIntent == nil
	}
	previous := prior.TypedEffect
	if previous == nil {
		return false
	}
	predecessorDigest, err := gatewayRebindTypedPredecessorDigest(intent)
	if err != nil {
		return false
	}
	switch prior.Sequence + 1 {
	case 3:
		return value.Network != nil && gatewayRebindTypedStageNetworkBindingMatches(intent, *value.Network)
	case 4:
		return value.ConfigVolume != nil &&
			gatewayRebindTypedStageConfigVolumeBindingMatches(intent, *value.ConfigVolume)
	case 5:
		return value.DataVolume != nil &&
			gatewayRebindTypedStageDataVolumeBindingMatches(intent, *value.DataVolume)
	case 6:
		return value.StageContainer != nil &&
			gatewayRebindTypedStageContainerBindingMatches(intent, *previous, *value.StageContainer)
	case 7:
		expected, err := gatewayRebindTypedStageConfigIntentFor(intent, prior, *previous)
		return err == nil && value.StageConfigIntent != nil && reflect.DeepEqual(expected, *value.StageConfigIntent)
	case 8:
		expected, err := gatewayRebindTypedStageConfigCopyFor(intent, prior, *previous)
		return err == nil && value.StageConfigCopy != nil && reflect.DeepEqual(expected, *value.StageConfigCopy)
	case 9:
		expected, err := gatewayRebindTypedStageStartIntentFor(intent, prior, *previous)
		return err == nil && value.StageStartIntent != nil && reflect.DeepEqual(expected, *value.StageStartIntent)
	case 10:
		if value.StageServing == nil {
			return false
		}
		expected, err := gatewayRebindTypedStageServingFor(intent, prior, *previous, value.StageServing.EndpointID)
		return err == nil && reflect.DeepEqual(expected, *value.StageServing)
	case 11:
		binding := value.FinalConfigIntent
		return binding != nil && binding.ProtectedIntentDigest == intent.Digest &&
			binding.ProtectedPredecessorDigest == predecessorDigest &&
			binding.SuccessorIdentityDigest == intent.Identity.Digest &&
			binding.PriorProgressDigest == prior.Digest && previous.StageServing != nil &&
			reflect.DeepEqual(binding.StageServing, *previous.StageServing) &&
			gatewayRebindTypedFinalRoutePlanMatchesIntent(intent, binding.RoutePlan) &&
			gatewayRebindTypedFinalConfigContentMatches(intent, *binding)
	case 12:
		expected, err := gatewayRebindTypedFinalConfigCopyFor(intent, prior, *previous)
		return err == nil && value.FinalConfigCopy != nil && reflect.DeepEqual(expected, *value.FinalConfigCopy)
	case 13:
		binding := value.HandoverIntent
		if binding == nil || !reflect.DeepEqual(binding.Plan.ApplicationNetworks, value.ApplicationNetworks) {
			return false
		}
		expected, err := gatewayRebindTypedHandoverIntentFor(intent, prior, *previous,
			binding.Plan.LocalHostPort, binding.Plan.PredecessorObservationDigest,
			binding.Plan.PredecessorInitiallyRunning, binding.Plan.ApplicationNetworks)
		return err == nil && reflect.DeepEqual(expected, *binding)
	case 14:
		if value.FinalContainer == nil || previous.HandoverIntent == nil {
			return false
		}
		expected, err := gatewayRebindTypedFinalContainerBindingFor(intent, *previous,
			previous.HandoverIntent.Plan, value.FinalContainer.ID)
		return err == nil && reflect.DeepEqual(expected, *value.FinalContainer)
	case 15:
		binding := value.CutoverIntent
		if binding == nil || previous.HandoverIntent == nil || previous.FinalContainer == nil {
			return false
		}
		finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.FinalContainer)
		return err == nil && binding.HandoverIntentDigest == previous.HandoverIntent.Digest &&
			binding.FinalBindingDigest == finalDigest && binding.Predecessor == intent.Predecessor &&
			binding.PredecessorCheckpointDigest == intent.Predecessor.PredecessorCheckpointDigest &&
			binding.PriorProgressDigest == prior.Digest
	case 16:
		serving := value.SuccessorServing
		if serving == nil || previous.HandoverIntent == nil || previous.CutoverIntent == nil ||
			previous.FinalContainer == nil {
			return false
		}
		finalDigest, err := gatewayRebindFinalContainerBindingDigest(*previous.FinalContainer)
		plan := previous.HandoverIntent.Plan
		observation := serving.Observation
		return err == nil && serving.PlanDigest == plan.Digest &&
			serving.FinalBindingDigest == finalDigest &&
			serving.CutoverIntentDigest == previous.CutoverIntent.Digest && serving.PriorProgressDigest == prior.Digest &&
			observation.Stage == gatewayRebindHandoverContainerAbsent &&
			observation.Final == gatewayRebindHandoverContainerRunning &&
			observation.FinalID == previous.FinalContainer.ID && !observation.PredecessorRunning &&
			observation.PredecessorAddress != gatewayRebindPredecessorAddressAmbiguous &&
			observation.ConfigVolumePresent && observation.DataVolumePresent && observation.IngressNetworkPresent &&
			observation.ConfigDigest == plan.FinalConfigDigest && observation.RoutesDigest == plan.RoutePlanDigest &&
			validSHA256(observation.PredecessorStopDigest) &&
			reflect.DeepEqual(observation.ApplicationNetworks, plan.ApplicationNetworks)
	case 17:
		return previous.SuccessorServing != nil && previous.Network != nil && previous.ConfigVolume != nil &&
			previous.DataVolume != nil && previous.StageContainer != nil && previous.FinalContainer != nil &&
			value.Resources != nil && value.PhysicalProof != nil &&
			value.Resources.ImageID == previous.ImageID &&
			reflect.DeepEqual(value.Resources.IngressNetwork, *previous.Network) &&
			reflect.DeepEqual(value.Resources.ConfigVolume, *previous.ConfigVolume) &&
			reflect.DeepEqual(value.Resources.DataVolume, *previous.DataVolume) &&
			reflect.DeepEqual(value.Resources.StageContainer, *previous.StageContainer) &&
			reflect.DeepEqual(value.Resources.FinalContainer, previous.FinalContainer) &&
			reflect.DeepEqual(value.Resources.ApplicationNetworks, previous.ApplicationNetworks) &&
			reflect.DeepEqual(value.PhysicalProof.Observation, previous.SuccessorServing.Observation)
	default:
		return false
	}
}

func gatewayRebindTypedFinalRoutePlanMatchesIntent(intent gatewayRebindProtectedIntentV2,
	plan gatewayRebindTypedFinalConfigRoutePlan,
) bool {
	if !validGatewayRebindTypedFinalConfigRoutePlanShape(plan) || plan.Predecessor != intent.Predecessor ||
		plan.OperationID != intent.OperationID || plan.EffectiveSuccessorProfile != intent.SuccessorProfile ||
		plan.SuccessorIdentity != intent.Identity || plan.RuntimeHeadsDigest != intent.Claim.Spec.RuntimeHeadsDigest ||
		int64(len(plan.Routes)) != intent.Claim.Spec.RuntimeHeadsCount {
		return false
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(intent.Claim.Spec)
	if err != nil || specDigest != plan.ClaimSpecDigest || len(plan.Routes) < len(intent.Roster) {
		return false
	}
	heads, err := gatewayRebindTypedRuntimeHeads(plan.Routes)
	if err != nil || !sameGatewayRebindRuntimeHeads(heads, intent.RuntimeHeads) {
		return false
	}
	roster := make(map[string]appaccess.GatewayRebindRosterEntryV2, len(intent.Roster))
	for _, entry := range intent.Roster {
		roster[entry.AppID] = entry
	}
	lanCount := 0
	for _, route := range plan.Routes {
		entry, listed := roster[route.AppID]
		if route.SourceLAN == nil {
			if listed {
				return false
			}
			continue
		}
		lanCount++
		if !listed || !gatewayRebindTypedRouteBindingMatchesRoster(route, entry) {
			return false
		}
	}
	return lanCount == len(roster)
}

func gatewayRebindTypedRouteBindingMatchesRoster(route gatewayRebindTypedFinalConfigRouteBinding,
	entry appaccess.GatewayRebindRosterEntryV2,
) bool {
	raw := route.SourceLAN.Raw
	if entry.AppID != route.AppID || entry.AppID != route.RuntimeHead.AppID ||
		entry.AllocationID != raw.AllocationID || entry.Port != raw.Port ||
		entry.AllocationOwnerOperationID != raw.OwnerOperationID || entry.AccessRevisionID != raw.AccessRevisionID ||
		entry.AccessRevisionNumber != raw.AccessRevisionNumber || entry.AccessSpecDigest != raw.AccessSpecDigest ||
		entry.GrantAttemptID != raw.GrantAttemptID || entry.SourceProfileRevisionID != raw.ProfileRevisionID ||
		entry.SourceProfileRevisionNumber != raw.ProfileRevisionNumber || entry.SourceProfileSpecDigest != raw.ProfileSpecDigest ||
		entry.ServingDeploymentID != route.RuntimeHead.DeploymentID || entry.ServingReleaseID != route.RuntimeHead.ReleaseID ||
		entry.ServingSlot != route.RuntimeHead.Slot || entry.RouteGeneration != route.RuntimeHead.Generation {
		return false
	}
	if route.SourceLAN.Transfer == nil {
		return entry.PredecessorTransferDigest == nil
	}
	return entry.PredecessorTransferDigest != nil &&
		*entry.PredecessorTransferDigest == route.SourceLAN.Transfer.TransferDigest
}

func validGatewayRebindTypedFinalConfigRoutePlanShape(value gatewayRebindTypedFinalConfigRoutePlan) bool {
	if value.Version != gatewayRebindTypedFinalConfigPlanVersion || value.Context != gatewayRebindTypedFinalConfigPlanContext ||
		!validCanonicalUUID(value.OperationID) || !validGatewayRebindSourceRef(value.Predecessor) ||
		!validSHA256(value.ClaimSpecDigest) || value.PredecessorIngressNetwork == "" ||
		strings.TrimSpace(value.PredecessorIngressNetwork) != value.PredecessorIngressNetwork ||
		value.PredecessorIngressNetwork == value.SuccessorIdentity.IngressNetwork ||
		!validSHA256(value.RuntimeHeadsDigest) || !validSHA256(value.RouteMapDigest) || !validSHA256(value.Digest) ||
		len(value.Routes) > maxStateApps {
		return false
	}
	for index, route := range value.Routes {
		if !validAppID(route.AppID) || index > 0 && value.Routes[index-1].AppID >= route.AppID ||
			route.RuntimeHead.AppID != route.AppID || !validCanonicalUUID(route.RuntimeHead.DeploymentID) ||
			!validGatewayRebindRuntimeReleaseID(route.RuntimeHead.ReleaseID) ||
			(route.RuntimeHead.Slot != "blue" && route.RuntimeHead.Slot != "green") ||
			route.RuntimeHead.Generation <= 0 || string(route.Route.Slot) != route.RuntimeHead.Slot ||
			validateRoute(route.Route) != nil {
			return false
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, route.RuntimeHead.UpdatedAt)
		if err != nil || updatedAt.IsZero() || updatedAt.UTC().Format(time.RFC3339Nano) != route.RuntimeHead.UpdatedAt {
			return false
		}
		if route.SourceLAN != nil {
			raw := route.SourceLAN.Raw
			if !validCanonicalUUID(raw.GrantAttemptID) || !validCanonicalUUID(raw.AllocationID) ||
				!validCanonicalUUID(raw.AccessRevisionID) || !validCanonicalUUID(raw.OwnerOperationID) ||
				!validSHA256(raw.AccessSpecDigest) || !validSHA256(raw.ProfileSpecDigest) || raw.Port == 0 {
				return false
			}
			if route.SourceLAN.Transfer != nil {
				digest, err := appaccess.GatewayRebindAllocationTransferDigest(*route.SourceLAN.Transfer)
				if err != nil || digest != route.SourceLAN.Transfer.TransferDigest {
					return false
				}
			}
		}
		digest, err := gatewayRebindTypedFinalConfigSourceBindingDigest(route)
		if err != nil || digest != route.SourceBindingDigest {
			return false
		}
	}
	heads, headsErr := gatewayRebindTypedRuntimeHeads(value.Routes)
	headsDigest := ""
	if headsErr == nil {
		headsDigest, headsErr = appaccess.GatewayRebindRuntimeHeadsV2Digest(value.OperationID, heads)
	}
	routesDigest, routesErr := gatewayRebindTypedFinalConfigRouteMapDigest(value.Routes)
	digest, digestErr := gatewayRebindTypedFinalConfigRoutePlanDigest(value)
	return headsErr == nil && routesErr == nil && digestErr == nil && headsDigest == value.RuntimeHeadsDigest &&
		routesDigest == value.RouteMapDigest && digest == value.Digest
}

func gatewayRebindTypedFinalConfigContentMatches(intent gatewayRebindProtectedIntentV2,
	binding gatewayRebindTypedFinalConfigIntentBinding,
) bool {
	if !validGatewayRebindTypedFinalConfigIntentBindingValue(binding) ||
		!gatewayRebindTypedFinalRoutePlanMatchesIntent(intent, binding.RoutePlan) {
		return false
	}
	body, err := gatewayRebindTypedFinalConfigBytesForPlan(intent, binding.RoutePlan)
	if err != nil {
		return false
	}
	defer clear(body)
	digest := sha256.Sum256(body)
	return binding.ContentDigest == hex.EncodeToString(digest[:]) && binding.ContentLength == int64(len(body))
}
