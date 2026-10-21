package generatedingress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	gatewayCurrentRouteStateVersionV2        = uint64(2)
	gatewayCurrentRouteStatePurposeV2        = "hostd/generated-ingress/routes/current/v2"
	gatewayCurrentRouteStateFilenamePrefixV2 = "gateway-current-routes.v2.g"
	gatewayCurrentRouteStateDigestPurposeV2  = "hostd/generated-ingress/routes/current/v2/state-digest/v1"
	gatewayCurrentAppRouteDigestPurposeV2    = "hostd/generated-ingress/routes/current/v2/app-record-digest/v1"
)

type gatewayCurrentTrafficV2 string

const (
	gatewayCurrentTrafficServingV2 gatewayCurrentTrafficV2 = "serving"
	gatewayCurrentTrafficDormantV2 gatewayCurrentTrafficV2 = "dormant"
)

type gatewayCurrentDesiredModeV2 string

const (
	gatewayCurrentDesiredUninitializedV2 gatewayCurrentDesiredModeV2 = "uninitialized"
	gatewayCurrentDesiredRunningV2       gatewayCurrentDesiredModeV2 = "running"
	gatewayCurrentDesiredStoppedV2       gatewayCurrentDesiredModeV2 = "stopped"
)

type gatewayCurrentLifecycleBoundaryV2 string

const (
	gatewayCurrentLifecycleWithdrawV2 gatewayCurrentLifecycleBoundaryV2 = "withdraw"
	gatewayCurrentLifecyclePublishV2  gatewayCurrentLifecycleBoundaryV2 = "publish"
)

// gatewayCurrentLifecycleAuthorityV2 identifies the immutable lifecycle
// request which last changed an application's traffic projection. Completion
// receipts are deliberately excluded to avoid making the protected record's
// digest depend on evidence produced after the physical boundary.
type gatewayCurrentLifecycleAuthorityV2 struct {
	OperationID  string                            `json:"operationId"`
	PinsDigest   string                            `json:"pinsDigest"`
	Boundary     gatewayCurrentLifecycleBoundaryV2 `json:"boundary"`
	IntentDigest string                            `json:"intentDigest"`
}

type gatewayCurrentAppliedAuthorityV2 struct {
	DesiredMode     gatewayCurrentDesiredModeV2         `json:"desiredMode"`
	DesiredRevision int64                               `json:"desiredRevision"`
	Lifecycle       *gatewayCurrentLifecycleAuthorityV2 `json:"lifecycle,omitempty"`
}

// gatewayCurrentRuntimePinV2 is retained for dormant applications. Dormancy
// removes an application from the traffic projection; it does not erase the
// selected runtime head or imply that its containers have stopped.
type gatewayCurrentRuntimePinV2 struct {
	DeploymentID     string                 `json:"deploymentId"`
	ReleaseID        string                 `json:"releaseId"`
	Slot             generatedidentity.Slot `json:"slot"`
	ActiveGeneration int64                  `json:"activeGeneration"`
}

type gatewayCurrentAppRouteV2 struct {
	Route     routeRecord                      `json:"route"`
	Runtime   gatewayCurrentRuntimePinV2       `json:"runtime"`
	Traffic   gatewayCurrentTrafficV2          `json:"traffic"`
	Authority gatewayCurrentAppliedAuthorityV2 `json:"authority"`
	LAN       *gatewayCurrentLANBinding        `json:"lan,omitempty"`
}

type gatewayCurrentRouteSourceV1 struct {
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}

type gatewayCurrentRouteSourceUpgrade struct {
	StateDigest            string `json:"stateDigest"`
	SourceRouteStateDigest string `json:"sourceRouteStateDigest"`
}

type gatewayCurrentRouteOriginV2 struct {
	SourceV1      *gatewayCurrentRouteSourceV1
	SourceUpgrade *gatewayCurrentRouteSourceUpgrade
}

// gatewayCurrentRouteStateV2 is initially a stable-state storage format.
// Pending lifecycle, route, and LAN recovery shapes are intentionally absent;
// strict decoding rejects them until their complete state machines exist.
type gatewayCurrentRouteStateV2 struct {
	Version                uint64                              `json:"version"`
	Revision               uint64                              `json:"revision"`
	Lineage                appaccess.GatewayCurrentLineageRef  `json:"lineage"`
	Profile                gatewayProfileBinding               `json:"profile"`
	Identity               gatewayCurrentIdentity              `json:"identity"`
	Network                gatewayV2NetworkPlan                `json:"network"`
	TransferManifestDigest string                              `json:"transferManifestDigest"`
	SourceV1               *gatewayCurrentRouteSourceV1        `json:"sourceV1,omitempty"`
	SourceUpgrade          *gatewayCurrentRouteSourceUpgrade   `json:"sourceUpgrade,omitempty"`
	Apps                   map[string]gatewayCurrentAppRouteV2 `json:"apps"`
	Digest                 string                              `json:"digest"`
}

type gatewayCurrentRouteStateV2Store struct {
	directory *stateStore
	lineage   appaccess.GatewayCurrentLineageRef
	origin    gatewayCurrentRouteOriginV2
	path      string
	purpose   string
}

type gatewayCurrentRouteStateDigestEnvelopeV2 struct {
	Purpose string                     `json:"purpose"`
	State   gatewayCurrentRouteStateV2 `json:"state"`
}

type gatewayCurrentAppRouteDigestEnvelopeV2 struct {
	Purpose string                   `json:"purpose"`
	Version uint64                   `json:"version"`
	AppID   string                   `json:"appId"`
	App     gatewayCurrentAppRouteV2 `json:"app"`
}

func validGatewayCurrentRouteSourceV1(value gatewayCurrentRouteSourceV1) bool {
	return value.Revision > 0 && validSHA256(value.Digest)
}

func validGatewayCurrentRouteSourceUpgrade(value gatewayCurrentRouteSourceUpgrade) bool {
	return validSHA256(value.StateDigest) && validSHA256(value.SourceRouteStateDigest)
}

func validGatewayCurrentRouteOriginDescriptorV2(lineage appaccess.GatewayCurrentLineageRef,
	sourceV1 *gatewayCurrentRouteSourceV1, sourceUpgrade *gatewayCurrentRouteSourceUpgrade,
) bool {
	switch lineage.Kind {
	case appaccess.GatewayRebindSourceGatewayRebind:
		return sourceV1 != nil && sourceUpgrade == nil && validGatewayCurrentRouteSourceV1(*sourceV1)
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return sourceV1 == nil && sourceUpgrade != nil && validGatewayCurrentRouteSourceUpgrade(*sourceUpgrade)
	default:
		return false
	}
}

func validGatewayCurrentRouteOriginV2(lineage appaccess.GatewayCurrentLineageRef,
	identity gatewayCurrentIdentity, sourceV1 *gatewayCurrentRouteSourceV1,
	sourceUpgrade *gatewayCurrentRouteSourceUpgrade,
) bool {
	return validGatewayCurrentRouteOriginDescriptorV2(lineage, sourceV1, sourceUpgrade) &&
		((lineage.Kind == appaccess.GatewayRebindSourceGatewayRebind && identity.Kind == gatewayCurrentIdentityRebind) ||
			(lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade && identity.Kind == gatewayCurrentIdentityUpgrade))
}

func cloneGatewayCurrentRouteOriginV2(value gatewayCurrentRouteOriginV2) gatewayCurrentRouteOriginV2 {
	result := gatewayCurrentRouteOriginV2{}
	if value.SourceV1 != nil {
		copy := *value.SourceV1
		result.SourceV1 = &copy
	}
	if value.SourceUpgrade != nil {
		copy := *value.SourceUpgrade
		result.SourceUpgrade = &copy
	}
	return result
}

func gatewayCurrentRouteOriginForStateV2(value gatewayCurrentRouteStateV2) gatewayCurrentRouteOriginV2 {
	return cloneGatewayCurrentRouteOriginV2(gatewayCurrentRouteOriginV2{
		SourceV1: value.SourceV1, SourceUpgrade: value.SourceUpgrade,
	})
}

func sameGatewayCurrentRouteOriginDescriptorV2(left, right gatewayCurrentRouteOriginV2) bool {
	return reflect.DeepEqual(left.SourceV1, right.SourceV1) &&
		reflect.DeepEqual(left.SourceUpgrade, right.SourceUpgrade)
}

func gatewayCurrentUpgradeRouteOriginV2(state gatewayV2RouteState,
	source routeState,
) (gatewayCurrentRouteOriginV2, error) {
	if !validGatewayV2RouteState(state) || !validRouteState(source) {
		return gatewayCurrentRouteOriginV2{}, errors.New("invalid generated ingress current V2 upgrade origin")
	}
	stateDigest, stateErr := canonicalDigest(state)
	sourceDigest, sourceErr := canonicalDigest(source)
	if stateErr != nil || sourceErr != nil || !validSHA256(stateDigest) || !validSHA256(sourceDigest) {
		return gatewayCurrentRouteOriginV2{}, errors.New("invalid generated ingress current V2 upgrade origin")
	}
	return gatewayCurrentRouteOriginV2{SourceUpgrade: &gatewayCurrentRouteSourceUpgrade{
		StateDigest: stateDigest, SourceRouteStateDigest: sourceDigest,
	}}, nil
}

func validGatewayCurrentRuntimeReleaseIDV2(value string) bool {
	if validCanonicalUUID(value) {
		return true
	}
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validGatewayCurrentLifecycleAuthorityV2(value gatewayCurrentLifecycleAuthorityV2) bool {
	return validCanonicalUUID(value.OperationID) && validSHA256(value.PinsDigest) &&
		validSHA256(value.IntentDigest) &&
		(value.Boundary == gatewayCurrentLifecycleWithdrawV2 || value.Boundary == gatewayCurrentLifecyclePublishV2)
}

func validGatewayCurrentAppliedAuthorityV2(traffic gatewayCurrentTrafficV2,
	value gatewayCurrentAppliedAuthorityV2,
) bool {
	if value.DesiredMode == gatewayCurrentDesiredUninitializedV2 {
		return traffic == gatewayCurrentTrafficServingV2 && value.DesiredRevision == 0 && value.Lifecycle == nil
	}
	if value.DesiredRevision <= 0 || value.Lifecycle == nil || !validGatewayCurrentLifecycleAuthorityV2(*value.Lifecycle) {
		return false
	}
	switch traffic {
	case gatewayCurrentTrafficServingV2:
		return value.DesiredMode == gatewayCurrentDesiredRunningV2 &&
			value.Lifecycle.Boundary == gatewayCurrentLifecyclePublishV2
	case gatewayCurrentTrafficDormantV2:
		return (value.DesiredMode == gatewayCurrentDesiredRunningV2 || value.DesiredMode == gatewayCurrentDesiredStoppedV2) &&
			value.Lifecycle.Boundary == gatewayCurrentLifecycleWithdrawV2
	default:
		return false
	}
}

func validGatewayCurrentRuntimePinV2(value gatewayCurrentRuntimePinV2) bool {
	return validCanonicalUUID(value.DeploymentID) && validGatewayCurrentRuntimeReleaseIDV2(value.ReleaseID) &&
		(value.Slot == generatedidentity.SlotBlue || value.Slot == generatedidentity.SlotGreen) && value.ActiveGeneration > 0
}

func validGatewayCurrentAppRouteV2(appID string, profile gatewayProfileBinding, value gatewayCurrentAppRouteV2,
	ports map[uint16]string, allocations, attempts map[string]struct{},
) bool {
	if !validAppID(appID) || validateRoute(value.Route) != nil || !validGatewayCurrentRuntimePinV2(value.Runtime) ||
		value.Route.Slot != value.Runtime.Slot || !validGatewayCurrentAppliedAuthorityV2(value.Traffic, value.Authority) {
		return false
	}
	return value.LAN == nil || validGatewayCurrentRawBinding(appID, profile, *value.LAN, ports, allocations, attempts)
}

func validGatewayCurrentRouteStateV2(value gatewayCurrentRouteStateV2) bool {
	if value.Version != gatewayCurrentRouteStateVersionV2 || value.Revision == 0 || !validSHA256(value.Digest) ||
		!validGatewayCurrentLineage(value.Lineage) || !validGatewayProfileBinding(value.Profile) ||
		value.Lineage.ProfileRevisionID != value.Profile.RevisionID ||
		value.Lineage.ProfileRevisionNumber != value.Profile.RevisionNumber ||
		value.Lineage.ProfileSpecDigest != value.Profile.SpecDigest ||
		!validGatewayCurrentIdentity(value.Lineage, value.Identity) ||
		!validGatewayV2NetworkPlan(value.Network) || !gatewayV2NetworkExcludesSelectedLAN(value.Profile, value.Network) ||
		!validSHA256(value.TransferManifestDigest) ||
		!validGatewayCurrentRouteOriginV2(value.Lineage, value.Identity, value.SourceV1, value.SourceUpgrade) ||
		value.Apps == nil || len(value.Apps) > maxStateApps {
		return false
	}
	ports := make(map[uint16]string)
	allocations := make(map[string]struct{})
	attempts := make(map[string]struct{})
	deployments := make(map[string]struct{})
	for appID, app := range value.Apps {
		if _, duplicate := deployments[app.Runtime.DeploymentID]; duplicate ||
			!validGatewayCurrentAppRouteV2(appID, value.Profile, app, ports, allocations, attempts) {
			return false
		}
		deployments[app.Runtime.DeploymentID] = struct{}{}
	}
	digest, err := gatewayCurrentRouteStateDigestV2(value)
	return err == nil && digest == value.Digest
}

func gatewayCurrentRouteStateDigestV2(value gatewayCurrentRouteStateV2) (string, error) {
	value.Digest = ""
	return canonicalDigest(gatewayCurrentRouteStateDigestEnvelopeV2{
		Purpose: gatewayCurrentRouteStateDigestPurposeV2,
		State:   value,
	})
}

func gatewayCurrentAppRouteDigestV2(appID string, profile gatewayProfileBinding,
	value gatewayCurrentAppRouteV2,
) (string, error) {
	if !validGatewayCurrentAppRouteV2(appID, profile, value, make(map[uint16]string),
		make(map[string]struct{}), make(map[string]struct{})) {
		return "", errors.New("invalid generated ingress current V2 app record")
	}
	return canonicalDigest(gatewayCurrentAppRouteDigestEnvelopeV2{
		Purpose: gatewayCurrentAppRouteDigestPurposeV2,
		Version: 1,
		AppID:   appID,
		App:     cloneGatewayCurrentAppRouteV2(value),
	})
}

func cloneGatewayCurrentAppRouteV2(value gatewayCurrentAppRouteV2) gatewayCurrentAppRouteV2 {
	cloned := cloneGatewayCurrentAppRoute(gatewayCurrentAppRoute{Route: value.Route, LAN: value.LAN})
	value.Route, value.LAN = cloned.Route, cloned.LAN
	if value.Authority.Lifecycle != nil {
		lifecycle := *value.Authority.Lifecycle
		value.Authority.Lifecycle = &lifecycle
	}
	return value
}

func cloneGatewayCurrentRouteStateV2(value gatewayCurrentRouteStateV2) gatewayCurrentRouteStateV2 {
	result := value
	if value.Identity.Upgrade != nil {
		identity := *value.Identity.Upgrade
		result.Identity.Upgrade = &identity
	}
	if value.Identity.Rebind != nil {
		identity := *value.Identity.Rebind
		result.Identity.Rebind = &identity
	}
	if value.Identity.RebindProfile != nil {
		profile := *value.Identity.RebindProfile
		result.Identity.RebindProfile = &profile
	}
	if value.SourceV1 != nil {
		source := *value.SourceV1
		result.SourceV1 = &source
	}
	if value.SourceUpgrade != nil {
		source := *value.SourceUpgrade
		result.SourceUpgrade = &source
	}
	result.Apps = make(map[string]gatewayCurrentAppRouteV2, len(value.Apps))
	for appID, app := range value.Apps {
		result.Apps[appID] = cloneGatewayCurrentAppRouteV2(app)
	}
	return result
}

func gatewayCurrentRouteStateNameV2(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayCurrentRouteStateFilenamePrefixV2 + token + ".bundle", gatewayCurrentRouteStatePurposeV2 + "/" + scope
}

func newGatewayCurrentRouteStateV2Store(dataRoot string, lineage appaccess.GatewayCurrentLineageRef,
	sourceV1 gatewayCurrentRouteSourceV1,
) (*gatewayCurrentRouteStateV2Store, error) {
	return newGatewayCurrentRouteStateV2StoreForOrigin(dataRoot, lineage,
		gatewayCurrentRouteOriginV2{SourceV1: &sourceV1})
}

func newGatewayCurrentRouteStateV2StoreForOrigin(dataRoot string, lineage appaccess.GatewayCurrentLineageRef,
	origin gatewayCurrentRouteOriginV2,
) (*gatewayCurrentRouteStateV2Store, error) {
	if !validGatewayCurrentLineage(lineage) ||
		!validGatewayCurrentRouteOriginDescriptorV2(lineage, origin.SourceV1, origin.SourceUpgrade) {
		return nil, errors.New("invalid generated ingress current V2 route store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayCurrentRouteStateNameV2(lineage.ProtectedGeneration, lineage.OperationID)
	return &gatewayCurrentRouteStateV2Store{
		directory: directory, lineage: lineage,
		origin: cloneGatewayCurrentRouteOriginV2(origin), path: filepath.Join(directory.root, name), purpose: purpose,
	}, nil
}

func (s *gatewayCurrentRouteStateV2Store) installBaseline(value gatewayCurrentRouteStateV2) error {
	if s == nil || s.directory == nil || value.Revision != 1 || value.Lineage != s.lineage ||
		!sameGatewayCurrentRouteOriginDescriptorV2(gatewayCurrentRouteOriginForStateV2(value), s.origin) ||
		!validGatewayCurrentRouteStateV2(value) {
		return errors.New("invalid generated ingress current V2 route baseline")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayCurrentRouteStateBytes); err != nil {
		return err
	}
	installed, err := s.load()
	if err != nil || !reflect.DeepEqual(installed, value) {
		return errors.New("generated ingress current V2 route baseline readback mismatch")
	}
	return nil
}

func (s *gatewayCurrentRouteStateV2Store) readCanonical() (gatewayCurrentRouteStateV2, error) {
	if s == nil || s.directory == nil {
		return gatewayCurrentRouteStateV2{}, errors.New("invalid generated ingress current V2 route store")
	}
	before, err := s.directory.directoryIdentity()
	if err != nil {
		return gatewayCurrentRouteStateV2{}, err
	}
	body, err := secretfile.ReadBounded(s.path, s.purpose, maxGatewayCurrentRouteStateBytes)
	if err != nil {
		return gatewayCurrentRouteStateV2{}, err
	}
	defer clear(body)
	if s.directory.sameDirectory(before) != nil || len(body) == 0 || len(body) > maxGatewayCurrentRouteStateBytes {
		return gatewayCurrentRouteStateV2{}, errors.New("invalid generated ingress current V2 route state")
	}
	var value gatewayCurrentRouteStateV2
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return gatewayCurrentRouteStateV2{}, errors.New("invalid generated ingress current V2 route state")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return gatewayCurrentRouteStateV2{}, errors.New("invalid generated ingress current V2 route state")
	}
	defer clear(canonical)
	if !bytes.Equal(canonical, body) {
		return gatewayCurrentRouteStateV2{}, errors.New("invalid generated ingress current V2 route state")
	}
	return value, nil
}

func (s *gatewayCurrentRouteStateV2Store) load() (gatewayCurrentRouteStateV2, error) {
	value, err := s.readCanonical()
	if err != nil || value.Lineage != s.lineage ||
		!sameGatewayCurrentRouteOriginDescriptorV2(gatewayCurrentRouteOriginForStateV2(value), s.origin) ||
		!validGatewayCurrentRouteStateV2(value) {
		return gatewayCurrentRouteStateV2{}, errors.New("invalid generated ingress current V2 route state")
	}
	return value, nil
}

func sameGatewayCurrentRouteOriginV2(left, right gatewayCurrentRouteStateV2) bool {
	return left.Version == right.Version && left.Lineage == right.Lineage && left.Profile == right.Profile &&
		reflect.DeepEqual(left.Identity, right.Identity) && left.Network == right.Network &&
		left.TransferManifestDigest == right.TransferManifestDigest &&
		reflect.DeepEqual(left.SourceV1, right.SourceV1) && reflect.DeepEqual(left.SourceUpgrade, right.SourceUpgrade)
}

// saveNext is a lock-scoped storage update, not an independently atomic CAS.
// The caller must hold the Manager/gateway OS lock across load, comparison,
// write, and readback. Operations that can cause external effects must also
// retain their outer effects lease in the established lock order. Lifecycle
// transition permission remains a later coordinator responsibility.
func (s *gatewayCurrentRouteStateV2Store) saveNext(previous, next gatewayCurrentRouteStateV2) error {
	if s == nil || s.directory == nil || previous.Revision == math.MaxUint64 ||
		next.Revision != previous.Revision+1 || !sameGatewayCurrentRouteOriginV2(previous, next) ||
		previous.Lineage != s.lineage ||
		!sameGatewayCurrentRouteOriginDescriptorV2(gatewayCurrentRouteOriginForStateV2(previous), s.origin) ||
		!validGatewayCurrentRouteStateV2(previous) ||
		!validGatewayCurrentRouteStateV2(next) {
		return errors.New("invalid generated ingress current V2 route update")
	}
	installed, err := s.load()
	if err != nil || !reflect.DeepEqual(installed, previous) {
		return errors.New("generated ingress current V2 route update is stale")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, next, false, maxGatewayCurrentRouteStateBytes); err != nil {
		return err
	}
	confirmed, err := s.load()
	if err != nil || !reflect.DeepEqual(confirmed, next) {
		return errors.New("generated ingress current V2 route update readback mismatch")
	}
	return nil
}
