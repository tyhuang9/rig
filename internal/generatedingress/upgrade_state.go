package generatedingress

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	v2RouteStatePurpose  = "hostd/generated-ingress/routes/v2"
	v2RouteStateFilename = "routes-v2.bundle"
	v2RouteStateVersion  = 2
	maxV2RouteStateBytes = 56 << 10

	gatewayMigrationPurpose  = "hostd/generated-ingress/migration/v1"
	gatewayMigrationFilename = "gateway-v1-to-v2.bundle"
	gatewayMigrationVersion  = 1
	maxGatewayMigrationBytes = 12 << 10

	gatewayUpgradeActionName = "upgrade_generated_ingress"
	gatewayTargetFormat      = 2
	gatewayV1IdentityVersion = "v1"
	gatewayV2IdentityVersion = "v2"
	gatewayV2PlanVersion     = 1

	gatewayV2ContainerName       = "rig-generated-caddy-v2"
	gatewayV2ConfigVolumeName    = "rig-generated-caddy-config-v2"
	gatewayV2DataVolumeName      = "rig-generated-caddy-data-v2"
	gatewayV2NetworkName         = "rig-generated-caddy-ingress-v2"
	gatewayV2StageContainerBase  = "rig-generated-caddy-v2-stage-"
	gatewayV2StageConfigFilename = "stage.json"
	gatewayV2ActiveConfigFile    = "active.json"
	gatewayV2CaddyImageDigest    = "sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648"
)

var (
	upgradeProtectedRead     = secretfile.Read
	upgradeProtectedWrite    = secretfile.Write
	upgradeProtectedWriteNew = secretfile.WriteNew
)

type gatewayProfileBinding struct {
	RevisionID     string `json:"revisionId"`
	RevisionNumber int64  `json:"revisionNumber"`
	SpecDigest     string `json:"specDigest"`
	SelectedIPv4   string `json:"selectedIpv4"`
	InterfaceID    string `json:"interfaceId"`
	PortStart      uint16 `json:"portStart"`
	PortEnd        uint16 `json:"portEnd"`
}

type gatewayUpgradeActionRef struct {
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	ApprovedBy string `json:"approvedBy"`
}

type gatewayV2NetworkPlan struct {
	Subnet        string `json:"subnet"`
	GatewayIPv4   string `json:"gatewayIpv4"`
	ContainerIPv4 string `json:"containerIpv4"`
}

type gatewayV2Identity struct {
	Version              string `json:"version"`
	Digest               string `json:"digest"`
	CaddyImageDigest     string `json:"caddyImageDigest"`
	FinalContainer       string `json:"finalContainer"`
	StageContainer       string `json:"stageContainer"`
	ConfigVolume         string `json:"configVolume"`
	DataVolume           string `json:"dataVolume"`
	IngressNetwork       string `json:"ingressNetwork"`
	StageConfigFilename  string `json:"stageConfigFilename"`
	ActiveConfigFilename string `json:"activeConfigFilename"`
}

type gatewayV2LANBinding struct {
	AccessRevisionID      string `json:"accessRevisionId"`
	AccessRevisionNumber  int64  `json:"accessRevisionNumber"`
	AccessSpecDigest      string `json:"accessSpecDigest"`
	AllocationID          string `json:"allocationId"`
	Port                  uint16 `json:"port"`
	ProfileRevisionID     string `json:"profileRevisionId"`
	ProfileRevisionNumber int64  `json:"profileRevisionNumber"`
	ProfileSpecDigest     string `json:"profileSpecDigest"`
}

type gatewayV2AppRoute struct {
	Route routeRecord          `json:"route"`
	LAN   *gatewayV2LANBinding `json:"lan,omitempty"`
}

// gatewayV2PendingRoute is the durable write-ahead record for a committed-v2
// route reload. Apps always remains the last committed route set while this
// record is present, so restart recovery has an unambiguous rollback target.
type gatewayV2PendingRoute struct {
	AppID    string             `json:"appId"`
	Previous *gatewayV2AppRoute `json:"previous,omitempty"`
	Proposed gatewayV2AppRoute  `json:"proposed"`
}

type gatewayV2RouteState struct {
	Version             int                          `json:"version"`
	OperationID         string                       `json:"operationId"`
	SourceV1StateDigest string                       `json:"sourceV1StateDigest"`
	Profile             gatewayProfileBinding        `json:"profile"`
	UpgradeAction       gatewayUpgradeActionRef      `json:"upgradeAction"`
	Identity            gatewayV2Identity            `json:"identity"`
	Network             gatewayV2NetworkPlan         `json:"network"`
	Apps                map[string]gatewayV2AppRoute `json:"apps"`
	Pending             *gatewayV2PendingRoute       `json:"pending,omitempty"`
}

type gatewayMigrationPhase string

const (
	gatewayPhasePrepared       gatewayMigrationPhase = "prepared"
	gatewayPhaseStageIntent    gatewayMigrationPhase = "stage_intent"
	gatewayPhaseStaged         gatewayMigrationPhase = "staged"
	gatewayPhaseTransferIntent gatewayMigrationPhase = "transfer_intent"
	gatewayPhaseV2Serving      gatewayMigrationPhase = "v2_serving"
	gatewayPhaseCommitted      gatewayMigrationPhase = "committed"
	gatewayPhaseRollbackIntent gatewayMigrationPhase = "rollback_intent"
	gatewayPhaseRolledBack     gatewayMigrationPhase = "rolled_back"
	gatewayPhaseUncertain      gatewayMigrationPhase = "uncertain"
)

type gatewayMigrationSourceRef struct {
	Format          int    `json:"format"`
	StateDigest     string `json:"stateDigest"`
	IdentityVersion string `json:"identityVersion"`
	IdentityDigest  string `json:"identityDigest"`
	LocalHostPort   uint16 `json:"localHostPort"`
}

type gatewayMigrationTargetRef struct {
	Format          int    `json:"format"`
	StateDigest     string `json:"stateDigest"`
	IdentityVersion string `json:"identityVersion"`
	IdentityDigest  string `json:"identityDigest"`
	PlanDigest      string `json:"planDigest"`
}

// gatewayV2ResourceBindings records immutable Docker identities as historical
// migration evidence. Docker volumes do not expose an object ID, so their ID
// is the canonical SHA-256 digest of the observed mountpoint and creation time.
// Values are monotonic: once bound, they can only be replayed exactly.
type gatewayV2ResourceBindings struct {
	ImageID          string                         `json:"imageId,omitempty"`
	IngressNetworkID string                         `json:"ingressNetworkId,omitempty"`
	ConfigVolume     gatewayV2VolumeResourceBinding `json:"configVolume"`
	DataVolume       gatewayV2VolumeResourceBinding `json:"dataVolume"`
	StageContainerID string                         `json:"stageContainerId,omitempty"`
	FinalContainerID string                         `json:"finalContainerId,omitempty"`
}

type gatewayV2VolumeResourceBinding struct {
	IdentityDigest string `json:"identityDigest,omitempty"`
	Mountpoint     string `json:"mountpoint,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
}

type gatewayV2ResourceKind string

const (
	gatewayV2ResourceImage          gatewayV2ResourceKind = "image"
	gatewayV2ResourceIngressNetwork gatewayV2ResourceKind = "ingress_network"
	gatewayV2ResourceConfigVolume   gatewayV2ResourceKind = "config_volume"
	gatewayV2ResourceDataVolume     gatewayV2ResourceKind = "data_volume"
	gatewayV2ResourceStageContainer gatewayV2ResourceKind = "stage_container"
	gatewayV2ResourceFinalContainer gatewayV2ResourceKind = "final_container"
)

type gatewayMigrationJournal struct {
	Version       int                       `json:"version"`
	OperationID   string                    `json:"operationId"`
	Phase         gatewayMigrationPhase     `json:"phase"`
	Source        gatewayMigrationSourceRef `json:"source"`
	Target        gatewayMigrationTargetRef `json:"target"`
	Profile       gatewayProfileBinding     `json:"profile"`
	UpgradeAction gatewayUpgradeActionRef   `json:"upgradeAction"`
	Resources     gatewayV2ResourceBindings `json:"resources"`
}

type gatewayUpgradePreparation struct {
	OperationID          string
	Profile              gatewayProfileBinding
	Network              gatewayV2NetworkPlan
	SourceIdentityDigest string
	LocalHostPort        uint16
	ApprovedActionDigest string
	ApprovedBy           string
}

type gatewayObservedTopology string

const (
	// Each non-unknown value represents a complete attestation, not a partial
	// observation. Extra resources, identity drift, or an incomplete predicate
	// must be mapped to gatewayTopologyUnknownOrDrift by the future observer.
	gatewayTopologyUnknownOrDrift   gatewayObservedTopology = "unknown_or_identity_drift"
	gatewayTopologyExactV1Only      gatewayObservedTopology = "exact_v1_serving_restartable_stage_and_final_absent"
	gatewayTopologyExactV1WithStage gatewayObservedTopology = "exact_v1_serving_restartable_exact_operation_bound_stage_final_absent"
	gatewayTopologyExactFinalV2     gatewayObservedTopology = "exact_v2_final_serving_v1_stopped_restartable_stage_absent"
)

type gatewayRecoveryDecision string

const (
	gatewayRecoveryWriteRollbackIntent gatewayRecoveryDecision = "write_rollback_intent"
	gatewayRecoveryRecordV2Serving     gatewayRecoveryDecision = "record_v2_serving"
	gatewayRecoveryCommit              gatewayRecoveryDecision = "commit"
	gatewayRecoveryRollbackToV1        gatewayRecoveryDecision = "rollback_to_v1"
	gatewayRecoveryRecordRolledBack    gatewayRecoveryDecision = "record_rolled_back"
	gatewayRecoveryComplete            gatewayRecoveryDecision = "complete"
	gatewayRecoveryMarkUncertain       gatewayRecoveryDecision = "mark_uncertain"
	gatewayRecoveryTerminalDrift       gatewayRecoveryDecision = "terminal_drift"
	gatewayRecoveryBlockedUncertain    gatewayRecoveryDecision = "blocked_uncertain"
)

type gatewayUpgradeStateStore struct {
	directory   *stateStore
	v2Path      string
	journalPath string
}

func newGatewayUpgradeStateStore(dataRoot string) (*gatewayUpgradeStateStore, error) {
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	return &gatewayUpgradeStateStore{
		directory:   directory,
		v2Path:      filepath.Join(directory.root, v2RouteStateFilename),
		journalPath: filepath.Join(directory.root, gatewayMigrationFilename),
	}, nil
}

func prepareGatewayV2State(source routeState, input gatewayUpgradePreparation) (gatewayV2RouteState, gatewayMigrationJournal, error) {
	if !validRouteState(source) || source.Pending != nil || !validCanonicalUUID(input.OperationID) ||
		!validGatewayProfileBinding(input.Profile) || !validGatewayV2NetworkPlan(input.Network) ||
		!validSHA256(input.SourceIdentityDigest) || input.LocalHostPort == 0 || !validCanonicalUUID(input.ApprovedBy) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid generated ingress upgrade preparation")
	}
	actionDigest, err := gatewayUpgradeActionDigest(input.Profile, gatewayV2IdentityVersion)
	if err != nil || input.ApprovedActionDigest != actionDigest {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress upgrade action approval is stale")
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	identity, err := newGatewayV2Identity(input.OperationID)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	apps := make(map[string]gatewayV2AppRoute, len(source.Active))
	for appID, route := range cloneRoutes(source.Active) {
		apps[appID] = gatewayV2AppRoute{Route: route}
	}
	state := gatewayV2RouteState{
		Version:             v2RouteStateVersion,
		OperationID:         input.OperationID,
		SourceV1StateDigest: sourceDigest,
		Profile:             input.Profile,
		UpgradeAction:       gatewayUpgradeActionRef{Name: gatewayUpgradeActionName, Digest: actionDigest, ApprovedBy: input.ApprovedBy},
		Identity:            identity,
		Network:             input.Network,
		Apps:                apps,
	}
	if !validGatewayV2RouteState(state) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid generated ingress v2 target state")
	}
	targetDigest, err := canonicalDigest(state)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	planDigest, err := gatewayV2PlanDigest(state)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	journal := gatewayMigrationJournal{
		Version:     gatewayMigrationVersion,
		OperationID: input.OperationID,
		Phase:       gatewayPhasePrepared,
		Source: gatewayMigrationSourceRef{
			Format: 1, StateDigest: sourceDigest, IdentityVersion: gatewayV1IdentityVersion,
			IdentityDigest: input.SourceIdentityDigest, LocalHostPort: input.LocalHostPort,
		},
		Target: gatewayMigrationTargetRef{
			Format: gatewayTargetFormat, StateDigest: targetDigest, IdentityVersion: gatewayV2IdentityVersion,
			IdentityDigest: identity.Digest, PlanDigest: planDigest,
		},
		Profile:       input.Profile,
		UpgradeAction: state.UpgradeAction,
	}
	if !validGatewayMigrationJournal(journal) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid generated ingress migration journal")
	}
	return cloneGatewayV2RouteState(state), journal, nil
}

func gatewayUpgradeActionDigest(profile gatewayProfileBinding, identityVersion string) (string, error) {
	if !validGatewayProfileBinding(profile) || identityVersion != gatewayV2IdentityVersion ||
		gatewayUpgradeActionName != string(appaccess.ActionUpgradeGateway) ||
		gatewayTargetFormat != appaccess.GatewayUpgradeTargetFormat ||
		gatewayV2IdentityVersion != appaccess.GatewayUpgradeIdentityVersion {
		return "", errors.New("invalid generated ingress upgrade action")
	}
	return appaccess.GatewayProfileUpgradeSpecDigest(appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID:     profile.RevisionID,
		ProfileRevisionNumber: profile.RevisionNumber,
		ProfileSpecDigest:     profile.SpecDigest,
	})
}

func newGatewayV2Identity(operationID string) (gatewayV2Identity, error) {
	if !validCanonicalUUID(operationID) {
		return gatewayV2Identity{}, errors.New("invalid generated ingress operation")
	}
	identity := gatewayV2Identity{
		Version: gatewayV2IdentityVersion, CaddyImageDigest: gatewayV2CaddyImageDigest,
		FinalContainer: gatewayV2ContainerName, StageContainer: gatewayV2StageContainerBase + operationID,
		ConfigVolume: gatewayV2ConfigVolumeName, DataVolume: gatewayV2DataVolumeName, IngressNetwork: gatewayV2NetworkName,
		StageConfigFilename: gatewayV2StageConfigFilename, ActiveConfigFilename: gatewayV2ActiveConfigFile,
	}
	digest, err := gatewayV2IdentityDigest(identity)
	if err != nil {
		return gatewayV2Identity{}, err
	}
	identity.Digest = digest
	return identity, nil
}

func gatewayV2IdentityDigest(identity gatewayV2Identity) (string, error) {
	identity.Digest = ""
	if identity.Version != gatewayV2IdentityVersion || identity.CaddyImageDigest != gatewayV2CaddyImageDigest ||
		identity.FinalContainer != gatewayV2ContainerName || !strings.HasPrefix(identity.StageContainer, gatewayV2StageContainerBase) ||
		!validCanonicalUUID(strings.TrimPrefix(identity.StageContainer, gatewayV2StageContainerBase)) ||
		identity.ConfigVolume != gatewayV2ConfigVolumeName || identity.DataVolume != gatewayV2DataVolumeName || identity.IngressNetwork != gatewayV2NetworkName ||
		identity.StageConfigFilename != gatewayV2StageConfigFilename || identity.ActiveConfigFilename != gatewayV2ActiveConfigFile {
		return "", errors.New("invalid generated ingress v2 identity")
	}
	return canonicalDigest(identity)
}

func gatewayV2PlanDigest(state gatewayV2RouteState) (string, error) {
	if !validGatewayProfileBinding(state.Profile) || !validGatewayV2NetworkPlan(state.Network) ||
		!gatewayV2NetworkExcludesSelectedLAN(state.Profile, state.Network) || !validGatewayV2Identity(state.OperationID, state.Identity) {
		return "", errors.New("invalid generated ingress v2 plan")
	}
	return canonicalDigest(struct {
		Version  int                   `json:"version"`
		Profile  gatewayProfileBinding `json:"profile"`
		Identity gatewayV2Identity     `json:"identity"`
		Network  gatewayV2NetworkPlan  `json:"network"`
	}{Version: gatewayV2PlanVersion, Profile: state.Profile, Identity: state.Identity, Network: state.Network})
}

func validGatewayProfileBinding(profile gatewayProfileBinding) bool {
	if !validCanonicalUUID(profile.RevisionID) || profile.RevisionNumber <= 0 || !validSHA256(profile.SpecDigest) {
		return false
	}
	digest, err := appaccess.GatewayProfileSpecDigest(appaccess.GatewayProfileSpec{
		SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID,
		PortStart: profile.PortStart, PortEnd: profile.PortEnd,
	})
	return err == nil && digest == profile.SpecDigest
}

func validGatewayV2NetworkPlan(plan gatewayV2NetworkPlan) bool {
	prefix, err := netip.ParsePrefix(plan.Subnet)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix != prefix.Masked() || prefix.Bits() < 8 || prefix.Bits() > 30 {
		return false
	}
	gateway, gatewayErr := netip.ParseAddr(plan.GatewayIPv4)
	container, containerErr := netip.ParseAddr(plan.ContainerIPv4)
	broadcast := lastIPv4Address(prefix)
	return gatewayErr == nil && containerErr == nil && gateway.Is4() && container.Is4() &&
		gateway.IsPrivate() && container.IsPrivate() && gateway.String() == plan.GatewayIPv4 && container.String() == plan.ContainerIPv4 &&
		prefix.Contains(gateway) && prefix.Contains(container) && gateway != prefix.Addr() && container != prefix.Addr() &&
		gateway != broadcast && container != broadcast && gateway != container
}

func gatewayV2NetworkExcludesSelectedLAN(profile gatewayProfileBinding, plan gatewayV2NetworkPlan) bool {
	prefix, prefixErr := netip.ParsePrefix(plan.Subnet)
	selected, selectedErr := netip.ParseAddr(profile.SelectedIPv4)
	return prefixErr == nil && selectedErr == nil && !prefix.Contains(selected)
}

func lastIPv4Address(prefix netip.Prefix) netip.Addr {
	value := prefix.Masked().Addr().As4()
	number := binary.BigEndian.Uint32(value[:]) | uint32(1<<(32-prefix.Bits())-1)
	var result [4]byte
	binary.BigEndian.PutUint32(result[:], number)
	return netip.AddrFrom4(result)
}

func validGatewayV2Identity(operationID string, identity gatewayV2Identity) bool {
	digest, err := gatewayV2IdentityDigest(identity)
	return err == nil && identity.StageContainer == gatewayV2StageContainerBase+operationID && digest == identity.Digest
}

func validGatewayV2RouteState(state gatewayV2RouteState) bool {
	if state.Version != v2RouteStateVersion || !validCanonicalUUID(state.OperationID) || !validSHA256(state.SourceV1StateDigest) ||
		!validGatewayProfileBinding(state.Profile) || state.UpgradeAction.Name != gatewayUpgradeActionName || !validCanonicalUUID(state.UpgradeAction.ApprovedBy) ||
		!validGatewayV2Identity(state.OperationID, state.Identity) || !validGatewayV2NetworkPlan(state.Network) ||
		!gatewayV2NetworkExcludesSelectedLAN(state.Profile, state.Network) || state.Apps == nil || len(state.Apps) > maxStateApps {
		return false
	}
	actionDigest, err := gatewayUpgradeActionDigest(state.Profile, state.Identity.Version)
	if err != nil || actionDigest != state.UpgradeAction.Digest {
		return false
	}
	ports := make(map[uint16]string)
	allocations := make(map[string]struct{})
	revisions := make(map[string]struct{})
	for appID, app := range state.Apps {
		if !validAppID(appID) || !validGatewayV2AppRoute(appID, state.Profile, app, ports, allocations, revisions) {
			return false
		}
	}
	return validGatewayV2PendingRoute(state)
}

func validGatewayV2AppRoute(appID string, profile gatewayProfileBinding, app gatewayV2AppRoute, ports map[uint16]string, allocations, revisions map[string]struct{}) bool {
	if validateRoute(app.Route) != nil {
		return false
	}
	if app.LAN == nil {
		return true
	}
	binding := app.LAN
	if !validCanonicalUUID(binding.AccessRevisionID) || binding.AccessRevisionNumber <= 0 || !validSHA256(binding.AccessSpecDigest) ||
		!validCanonicalUUID(binding.AllocationID) || binding.Port < profile.PortStart || binding.Port > profile.PortEnd ||
		binding.ProfileRevisionID != profile.RevisionID || binding.ProfileRevisionNumber != profile.RevisionNumber || binding.ProfileSpecDigest != profile.SpecDigest {
		return false
	}
	accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpec{
		AppID: appID, AllocationID: binding.AllocationID, Port: binding.Port,
		GatewayProfileRevisionID: binding.ProfileRevisionID, GatewayProfileRevisionNumber: binding.ProfileRevisionNumber,
	})
	if err != nil || accessDigest != binding.AccessSpecDigest {
		return false
	}
	if _, duplicate := ports[binding.Port]; duplicate {
		return false
	}
	if _, duplicate := allocations[binding.AllocationID]; duplicate {
		return false
	}
	if _, duplicate := revisions[binding.AccessRevisionID]; duplicate {
		return false
	}
	ports[binding.Port] = appID
	allocations[binding.AllocationID] = struct{}{}
	revisions[binding.AccessRevisionID] = struct{}{}
	return true
}

func validGatewayV2PendingRoute(state gatewayV2RouteState) bool {
	if state.Pending == nil {
		return true
	}
	pending := state.Pending
	if !validAppID(pending.AppID) || validateRoute(pending.Proposed.Route) != nil {
		return false
	}
	committed, exists := state.Apps[pending.AppID]
	if exists != (pending.Previous != nil) {
		return false
	}
	if pending.Previous != nil && !reflect.DeepEqual(committed, *pending.Previous) {
		return false
	}
	// Route switches may not grant, move, or revoke LAN access. That belongs to
	// the separately approved app-access transaction.
	if pending.Previous == nil {
		return pending.Proposed.LAN == nil
	}
	return pending.Previous.Route.Slot != pending.Proposed.Route.Slot && reflect.DeepEqual(pending.Previous.LAN, pending.Proposed.LAN)
}

func validGatewayMigrationJournal(journal gatewayMigrationJournal) bool {
	if journal.Version != gatewayMigrationVersion || !validCanonicalUUID(journal.OperationID) || !validGatewayMigrationPhase(journal.Phase) ||
		journal.Source.Format != 1 || !validSHA256(journal.Source.StateDigest) || journal.Source.IdentityVersion != gatewayV1IdentityVersion ||
		!validSHA256(journal.Source.IdentityDigest) || journal.Source.LocalHostPort == 0 ||
		journal.Target.Format != gatewayTargetFormat || !validSHA256(journal.Target.StateDigest) || journal.Target.IdentityVersion != gatewayV2IdentityVersion ||
		!validSHA256(journal.Target.IdentityDigest) || !validSHA256(journal.Target.PlanDigest) ||
		!validGatewayProfileBinding(journal.Profile) || journal.UpgradeAction.Name != gatewayUpgradeActionName || !validCanonicalUUID(journal.UpgradeAction.ApprovedBy) ||
		!validGatewayV2ResourceBindings(journal.Phase, journal.Resources) {
		return false
	}
	actionDigest, err := gatewayUpgradeActionDigest(journal.Profile, journal.Target.IdentityVersion)
	return err == nil && actionDigest == journal.UpgradeAction.Digest
}

func validGatewayV2ResourceBindings(phase gatewayMigrationPhase, bindings gatewayV2ResourceBindings) bool {
	values := []string{bindings.ImageID, bindings.IngressNetworkID, bindings.StageContainerID, bindings.FinalContainerID}
	for _, value := range values {
		if value != "" && !validSHA256(value) {
			return false
		}
	}
	configVolumeValid, configVolumePresent := validGatewayV2VolumeResourceBinding(bindings.ConfigVolume)
	dataVolumeValid, dataVolumePresent := validGatewayV2VolumeResourceBinding(bindings.DataVolume)
	if !configVolumeValid || !dataVolumeValid {
		return false
	}
	infraComplete := bindings.ImageID != "" && bindings.IngressNetworkID != "" && configVolumePresent && dataVolumePresent
	infraAbsent := bindings.ImageID == "" && bindings.IngressNetworkID == "" && !configVolumePresent && !dataVolumePresent
	if bindings.StageContainerID != "" && !infraComplete {
		return false
	}
	if bindings.FinalContainerID != "" && (bindings.StageContainerID == "" || !infraComplete) {
		return false
	}
	switch phase {
	case gatewayPhasePrepared:
		return infraAbsent && bindings.StageContainerID == "" && bindings.FinalContainerID == ""
	case gatewayPhaseStageIntent:
		return bindings.FinalContainerID == ""
	case gatewayPhaseStaged:
		return infraComplete && bindings.StageContainerID != "" && bindings.FinalContainerID == ""
	case gatewayPhaseTransferIntent:
		return infraComplete && bindings.StageContainerID != ""
	case gatewayPhaseV2Serving, gatewayPhaseCommitted:
		return infraComplete && bindings.StageContainerID != "" && bindings.FinalContainerID != ""
	case gatewayPhaseRollbackIntent, gatewayPhaseRolledBack, gatewayPhaseUncertain:
		return true
	default:
		return false
	}
}

func newGatewayV2VolumeResourceBinding(identity gatewayV1VolumeIdentity) (gatewayV2VolumeResourceBinding, error) {
	if identity.Mountpoint == "" || identity.CreatedAt == "" || strings.TrimSpace(identity.Mountpoint) != identity.Mountpoint || strings.TrimSpace(identity.CreatedAt) != identity.CreatedAt {
		return gatewayV2VolumeResourceBinding{}, errors.New("invalid generated ingress volume identity")
	}
	digest, err := canonicalDigest(struct {
		Version    int    `json:"version"`
		Mountpoint string `json:"mountpoint"`
		CreatedAt  string `json:"createdAt"`
	}{Version: 1, Mountpoint: identity.Mountpoint, CreatedAt: identity.CreatedAt})
	if err != nil {
		return gatewayV2VolumeResourceBinding{}, err
	}
	return gatewayV2VolumeResourceBinding{IdentityDigest: digest, Mountpoint: identity.Mountpoint, CreatedAt: identity.CreatedAt}, nil
}

func validGatewayV2VolumeResourceBinding(binding gatewayV2VolumeResourceBinding) (valid, present bool) {
	if binding == (gatewayV2VolumeResourceBinding{}) {
		return true, false
	}
	expected, err := newGatewayV2VolumeResourceBinding(gatewayV1VolumeIdentity{Mountpoint: binding.Mountpoint, CreatedAt: binding.CreatedAt})
	return err == nil && reflect.DeepEqual(binding, expected), true
}

func validGatewayMigrationPhase(phase gatewayMigrationPhase) bool {
	switch phase {
	case gatewayPhasePrepared, gatewayPhaseStageIntent, gatewayPhaseStaged, gatewayPhaseTransferIntent,
		gatewayPhaseV2Serving, gatewayPhaseCommitted, gatewayPhaseRollbackIntent, gatewayPhaseRolledBack, gatewayPhaseUncertain:
		return true
	default:
		return false
	}
}

func validGatewayMigrationTransition(from, to gatewayMigrationPhase) bool {
	if !validGatewayMigrationPhase(from) || !validGatewayMigrationPhase(to) {
		return false
	}
	if from == to {
		return true
	}
	if to == gatewayPhaseUncertain {
		return from != gatewayPhaseCommitted && from != gatewayPhaseRolledBack && from != gatewayPhaseUncertain
	}
	switch from {
	case gatewayPhasePrepared:
		return to == gatewayPhaseStageIntent || to == gatewayPhaseRollbackIntent
	case gatewayPhaseStageIntent:
		return to == gatewayPhaseStaged || to == gatewayPhaseRollbackIntent
	case gatewayPhaseStaged:
		return to == gatewayPhaseTransferIntent || to == gatewayPhaseRollbackIntent
	case gatewayPhaseTransferIntent:
		return to == gatewayPhaseV2Serving || to == gatewayPhaseRollbackIntent
	case gatewayPhaseV2Serving:
		return to == gatewayPhaseCommitted || to == gatewayPhaseRollbackIntent
	case gatewayPhaseRollbackIntent:
		return to == gatewayPhaseRolledBack
	default:
		return false
	}
}

func decideGatewayMigrationRecovery(phase gatewayMigrationPhase, topology gatewayObservedTopology) (gatewayRecoveryDecision, error) {
	if !validGatewayMigrationPhase(phase) || !validGatewayObservedTopology(topology) {
		return "", errors.New("invalid generated ingress recovery observation")
	}
	if phase == gatewayPhaseUncertain {
		return gatewayRecoveryBlockedUncertain, nil
	}
	if phase == gatewayPhaseCommitted {
		if topology == gatewayTopologyExactFinalV2 {
			return gatewayRecoveryComplete, nil
		}
		return gatewayRecoveryTerminalDrift, nil
	}
	if phase == gatewayPhaseRolledBack {
		if topology == gatewayTopologyExactV1Only {
			return gatewayRecoveryComplete, nil
		}
		return gatewayRecoveryTerminalDrift, nil
	}
	if topology == gatewayTopologyUnknownOrDrift {
		return gatewayRecoveryMarkUncertain, nil
	}
	switch phase {
	case gatewayPhasePrepared, gatewayPhaseStageIntent, gatewayPhaseStaged:
		if topology == gatewayTopologyExactV1Only || topology == gatewayTopologyExactV1WithStage {
			return gatewayRecoveryWriteRollbackIntent, nil
		}
	case gatewayPhaseTransferIntent:
		if topology == gatewayTopologyExactFinalV2 {
			return gatewayRecoveryRecordV2Serving, nil
		}
		if topology == gatewayTopologyExactV1Only || topology == gatewayTopologyExactV1WithStage {
			return gatewayRecoveryWriteRollbackIntent, nil
		}
	case gatewayPhaseV2Serving:
		if topology == gatewayTopologyExactFinalV2 {
			return gatewayRecoveryCommit, nil
		}
		if topology == gatewayTopologyExactV1Only || topology == gatewayTopologyExactV1WithStage {
			return gatewayRecoveryWriteRollbackIntent, nil
		}
	case gatewayPhaseRollbackIntent:
		if topology == gatewayTopologyExactV1Only {
			return gatewayRecoveryRecordRolledBack, nil
		}
		if topology == gatewayTopologyExactV1WithStage || topology == gatewayTopologyExactFinalV2 {
			return gatewayRecoveryRollbackToV1, nil
		}
	}
	return gatewayRecoveryMarkUncertain, nil
}

func validGatewayObservedTopology(topology gatewayObservedTopology) bool {
	switch topology {
	case gatewayTopologyUnknownOrDrift, gatewayTopologyExactV1Only, gatewayTopologyExactV1WithStage, gatewayTopologyExactFinalV2:
		return true
	default:
		return false
	}
}

func (s *gatewayUpgradeStateStore) createV2State(state gatewayV2RouteState) error {
	if !validGatewayV2RouteState(state) {
		return errors.New("invalid generated ingress v2 state")
	}
	for _, app := range state.Apps {
		if app.LAN != nil {
			return errors.New("initial generated ingress v2 state must not contain LAN bindings")
		}
	}
	source, err := s.directory.load()
	if err != nil {
		return err
	}
	digest, err := canonicalDigest(source)
	if err != nil || digest != state.SourceV1StateDigest {
		return errors.New("generated ingress v1 source state changed")
	}
	return s.writeExact(s.v2Path, v2RouteStatePurpose, state, true, maxV2RouteStateBytes)
}

func (s *gatewayUpgradeStateStore) loadV2State() (gatewayV2RouteState, error) {
	var state gatewayV2RouteState
	if err := s.readStrict(s.v2Path, v2RouteStatePurpose, maxV2RouteStateBytes, &state); err != nil || !validGatewayV2RouteState(state) {
		return gatewayV2RouteState{}, errors.New("generated ingress v2 state is invalid")
	}
	return cloneGatewayV2RouteState(state), nil
}

func (s *gatewayUpgradeStateStore) createMigrationJournal(journal gatewayMigrationJournal) error {
	if journal.Phase != gatewayPhasePrepared || !validGatewayMigrationJournal(journal) {
		return errors.New("invalid generated ingress migration journal")
	}
	if _, installed, err := s.loadBoundUpgrade(journal.OperationID); err == nil {
		if reflect.DeepEqual(installed, journal) {
			return nil
		}
		return errors.New("generated ingress migration journal idempotency mismatch")
	}
	state, err := s.loadV2State()
	if err != nil {
		return err
	}
	if !journalMatchesInitialV2State(journal, state) {
		return errors.New("generated ingress migration target is stale")
	}
	source, err := s.directory.load()
	if err != nil {
		return err
	}
	digest, err := canonicalDigest(source)
	if err != nil || digest != journal.Source.StateDigest {
		return errors.New("generated ingress migration source is stale")
	}
	return s.writeExact(s.journalPath, gatewayMigrationPurpose, journal, true, maxGatewayMigrationBytes)
}

func (s *gatewayUpgradeStateStore) loadMigrationJournal() (gatewayMigrationJournal, error) {
	var journal gatewayMigrationJournal
	if err := s.readStrict(s.journalPath, gatewayMigrationPurpose, maxGatewayMigrationBytes, &journal); err != nil || !validGatewayMigrationJournal(journal) {
		return gatewayMigrationJournal{}, errors.New("generated ingress migration journal is invalid")
	}
	return journal, nil
}

func (s *gatewayUpgradeStateStore) loadBoundUpgrade(operationID string) (gatewayV2RouteState, gatewayMigrationJournal, error) {
	if !validCanonicalUUID(operationID) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid generated ingress operation")
	}
	state, err := s.loadV2State()
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	journal, err := s.loadMigrationJournal()
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	bound := journalMatchesInitialV2State(journal, state)
	if journal.Phase == gatewayPhaseCommitted {
		// Target.StateDigest is immutable historical evidence of the zero-LAN
		// migration target. After commitment, valid access/deployment revisions
		// may change Apps while the exact profile, identity, and plan stay bound.
		bound = journalMatchesV2Plan(journal, state)
	}
	if state.OperationID != operationID || journal.OperationID != operationID || !bound {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress upgrade binding is invalid")
	}
	source, err := s.directory.load()
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil || sourceDigest != journal.Source.StateDigest || sourceDigest != state.SourceV1StateDigest {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress v1 source state changed")
	}
	return state, journal, nil
}

func (s *gatewayUpgradeStateStore) transitionMigrationJournal(operationID string, expected, next gatewayMigrationPhase) (gatewayMigrationJournal, error) {
	if !validGatewayMigrationTransition(expected, next) {
		return gatewayMigrationJournal{}, errors.New("invalid generated ingress migration transition")
	}
	_, journal, err := s.loadBoundUpgrade(operationID)
	if err != nil {
		return gatewayMigrationJournal{}, err
	}
	if journal.Phase == next {
		return journal, nil
	}
	if journal.Phase != expected {
		return gatewayMigrationJournal{}, errors.New("stale generated ingress migration phase")
	}
	journal.Phase = next
	if !validGatewayMigrationJournal(journal) {
		return gatewayMigrationJournal{}, errors.New("generated ingress migration resources are incomplete for phase")
	}
	return s.installMigrationJournal(journal)
}

// bindMigrationDockerResource records a normalized immutable Docker image,
// network, or container ID. Creation-phase bindings are monotonic; later
// phases permit exact replay only and reject replacement identities.
func (s *gatewayUpgradeStateStore) bindMigrationDockerResource(operationID string, expected gatewayMigrationPhase, kind gatewayV2ResourceKind, dockerID string) (gatewayMigrationJournal, error) {
	id := normalizeID(dockerID)
	if !validCanonicalUUID(operationID) || !validGatewayMigrationPhase(expected) || !validSHA256(id) {
		return gatewayMigrationJournal{}, errors.New("invalid generated ingress resource binding")
	}
	_, journal, err := s.loadBoundUpgrade(operationID)
	if err != nil {
		return gatewayMigrationJournal{}, err
	}
	if journal.Phase != expected {
		return gatewayMigrationJournal{}, errors.New("stale generated ingress migration phase")
	}
	var installed *string
	switch kind {
	case gatewayV2ResourceImage:
		installed = &journal.Resources.ImageID
	case gatewayV2ResourceIngressNetwork:
		installed = &journal.Resources.IngressNetworkID
	case gatewayV2ResourceStageContainer:
		installed = &journal.Resources.StageContainerID
	case gatewayV2ResourceFinalContainer:
		installed = &journal.Resources.FinalContainerID
	default:
		return gatewayMigrationJournal{}, errors.New("invalid generated ingress Docker resource kind")
	}
	if *installed != "" {
		if *installed == id {
			return journal, nil
		}
		return gatewayMigrationJournal{}, errors.New("generated ingress resource identity mismatch")
	}
	if !gatewayV2ResourceMayFirstBind(expected, kind) {
		return gatewayMigrationJournal{}, errors.New("generated ingress resource binding is out of phase")
	}
	*installed = id
	if !validGatewayMigrationJournal(journal) {
		return gatewayMigrationJournal{}, errors.New("generated ingress resource binding violates phase invariants")
	}
	return s.installMigrationJournal(journal)
}

// bindMigrationVolumeResource records Docker's stable volume creation
// identity. Docker has no volume object ID, so the protected binding retains
// both creation fields and their canonical digest.
func (s *gatewayUpgradeStateStore) bindMigrationVolumeResource(operationID string, expected gatewayMigrationPhase, kind gatewayV2ResourceKind, identity gatewayV1VolumeIdentity) (gatewayMigrationJournal, error) {
	binding, err := newGatewayV2VolumeResourceBinding(identity)
	if err != nil || !validCanonicalUUID(operationID) || !validGatewayMigrationPhase(expected) {
		return gatewayMigrationJournal{}, errors.New("invalid generated ingress volume binding")
	}
	_, journal, err := s.loadBoundUpgrade(operationID)
	if err != nil {
		return gatewayMigrationJournal{}, err
	}
	if journal.Phase != expected {
		return gatewayMigrationJournal{}, errors.New("stale generated ingress migration phase")
	}
	var installed *gatewayV2VolumeResourceBinding
	switch kind {
	case gatewayV2ResourceConfigVolume:
		installed = &journal.Resources.ConfigVolume
	case gatewayV2ResourceDataVolume:
		installed = &journal.Resources.DataVolume
	default:
		return gatewayMigrationJournal{}, errors.New("invalid generated ingress volume resource kind")
	}
	if *installed != (gatewayV2VolumeResourceBinding{}) {
		if reflect.DeepEqual(*installed, binding) {
			return journal, nil
		}
		return gatewayMigrationJournal{}, errors.New("generated ingress volume identity mismatch")
	}
	if !gatewayV2ResourceMayFirstBind(expected, kind) {
		return gatewayMigrationJournal{}, errors.New("generated ingress volume binding is out of phase")
	}
	*installed = binding
	if !validGatewayMigrationJournal(journal) {
		return gatewayMigrationJournal{}, errors.New("generated ingress volume binding violates phase invariants")
	}
	return s.installMigrationJournal(journal)
}

func gatewayV2ResourceMayFirstBind(phase gatewayMigrationPhase, kind gatewayV2ResourceKind) bool {
	switch kind {
	case gatewayV2ResourceImage, gatewayV2ResourceIngressNetwork, gatewayV2ResourceConfigVolume, gatewayV2ResourceDataVolume, gatewayV2ResourceStageContainer:
		return phase == gatewayPhaseStageIntent
	case gatewayV2ResourceFinalContainer:
		return phase == gatewayPhaseTransferIntent
	default:
		return false
	}
}

func (s *gatewayUpgradeStateStore) installMigrationJournal(journal gatewayMigrationJournal) (gatewayMigrationJournal, error) {
	if !validGatewayMigrationJournal(journal) {
		return gatewayMigrationJournal{}, errors.New("invalid generated ingress migration journal update")
	}
	if err := s.writeExact(s.journalPath, gatewayMigrationPurpose, journal, false, maxGatewayMigrationBytes); err != nil {
		return gatewayMigrationJournal{}, err
	}
	installed, err := s.loadMigrationJournal()
	if err != nil || !reflect.DeepEqual(installed, journal) {
		return gatewayMigrationJournal{}, errors.New("generated ingress migration phase was not installed")
	}
	return installed, nil
}

func (s *gatewayUpgradeStateStore) saveCommittedV2State(state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if journal.Phase != gatewayPhaseCommitted || !validGatewayV2RouteState(state) || !journalMatchesV2Plan(journal, state) {
		return errors.New("invalid committed generated ingress v2 state")
	}
	current, currentJournal, err := s.loadBoundUpgrade(journal.OperationID)
	if err != nil || currentJournal.Phase != gatewayPhaseCommitted || current.OperationID != state.OperationID {
		return errors.New("generated ingress v2 state is not committed")
	}
	if !validCommittedV2StateTransition(current, state) {
		return errors.New("invalid committed generated ingress v2 state transition")
	}
	if err := s.writeExact(s.v2Path, v2RouteStatePurpose, state, false, maxV2RouteStateBytes); err != nil {
		return err
	}
	installed, installedJournal, err := s.loadBoundUpgrade(journal.OperationID)
	if err != nil || installedJournal.Phase != gatewayPhaseCommitted || !reflect.DeepEqual(installed, state) {
		return errors.New("committed generated ingress v2 state was not installed")
	}
	return nil
}

func validCommittedV2StateTransition(current, next gatewayV2RouteState) bool {
	if reflect.DeepEqual(current, next) {
		return true
	}
	if current.Pending == nil && next.Pending != nil {
		current.Pending = next.Pending
		return reflect.DeepEqual(current, next)
	}
	if current.Pending == nil || next.Pending != nil {
		return false
	}
	rolledBack := cloneGatewayV2RouteState(current)
	rolledBack.Pending = nil
	if reflect.DeepEqual(rolledBack, next) {
		return true
	}
	committed := cloneGatewayV2RouteState(rolledBack)
	committed.Apps[current.Pending.AppID] = cloneGatewayV2AppRoute(current.Pending.Proposed)
	return reflect.DeepEqual(committed, next)
}

func journalMatchesInitialV2State(journal gatewayMigrationJournal, state gatewayV2RouteState) bool {
	stateDigest, stateErr := canonicalDigest(state)
	return stateErr == nil && journal.Target.StateDigest == stateDigest && journalMatchesV2Plan(journal, state)
}

func journalMatchesV2Plan(journal gatewayMigrationJournal, state gatewayV2RouteState) bool {
	planDigest, planErr := gatewayV2PlanDigest(state)
	return planErr == nil && journal.OperationID == state.OperationID && journal.Target.PlanDigest == planDigest &&
		journal.Target.IdentityVersion == state.Identity.Version && journal.Target.IdentityDigest == state.Identity.Digest &&
		reflect.DeepEqual(journal.Profile, state.Profile) && journal.UpgradeAction == state.UpgradeAction
}

func (s *gatewayUpgradeStateStore) readStrict(path, purpose string, maximum int, target any) error {
	before, err := s.directory.directoryIdentity()
	if err != nil {
		return err
	}
	body, err := upgradeProtectedRead(path, purpose)
	if err != nil {
		return err
	}
	defer clear(body)
	if s.directory.sameDirectory(before) != nil || len(body) == 0 || len(body) > maximum {
		return errors.New("generated ingress protected state is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("generated ingress protected state is invalid")
	}
	return nil
}

func (s *gatewayUpgradeStateStore) writeExact(path, purpose string, value any, createOnly bool, maximum int) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > maximum {
		clear(body)
		return errors.New("generated ingress protected state is too large")
	}
	defer clear(body)
	before, err := s.directory.directoryIdentity()
	if err != nil {
		return err
	}
	if createOnly {
		err = upgradeProtectedWriteNew(path, purpose, body)
	} else {
		err = upgradeProtectedWrite(path, purpose, body)
	}
	if err == nil {
		return s.directory.sameDirectory(before)
	}
	// A post-install directory-sync failure is not durable success. The caller
	// must stop mutations and recover from a fresh observation.
	if secretfile.WasInstalled(err) {
		return err
	}
	if !createOnly || !errors.Is(err, os.ErrExist) {
		return err
	}
	// An exact, previously installed create-only artifact is an idempotent
	// replay. A changed artifact or a failed read must remain an error.
	installed, readErr := upgradeProtectedRead(path, purpose)
	defer clear(installed)
	if readErr == nil && bytes.Equal(installed, body) && s.directory.sameDirectory(before) == nil {
		return nil
	}
	return err
}

func cloneGatewayV2RouteState(state gatewayV2RouteState) gatewayV2RouteState {
	result := state
	result.Apps = make(map[string]gatewayV2AppRoute, len(state.Apps))
	for appID, app := range state.Apps {
		app.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
		if app.LAN != nil {
			binding := *app.LAN
			app.LAN = &binding
		}
		result.Apps[appID] = app
	}
	if state.Pending != nil {
		pending := *state.Pending
		pending.Proposed = cloneGatewayV2AppRoute(pending.Proposed)
		if pending.Previous != nil {
			previous := cloneGatewayV2AppRoute(*pending.Previous)
			pending.Previous = &previous
		}
		result.Pending = &pending
	}
	return result
}

func cloneGatewayV2AppRoute(app gatewayV2AppRoute) gatewayV2AppRoute {
	app.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
	if app.LAN != nil {
		binding := *app.LAN
		app.LAN = &binding
	}
	return app
}

func canonicalDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func validCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
