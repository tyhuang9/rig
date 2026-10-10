package generatedingress

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

const (
	gatewayCurrentRouteStateVersion        = uint64(1)
	gatewayCurrentRouteStatePurpose        = "hostd/generated-ingress/routes/current/v1"
	gatewayCurrentRouteStateFilenamePrefix = "gateway-current-routes.v1.g"
	maxGatewayCurrentRouteStateBytes       = 256 << 10

	gatewayRebindPredecessorCheckpointVersion        = 1
	gatewayRebindPredecessorCheckpointPurpose        = "hostd/generated-ingress/rebind/predecessor-state/v1"
	gatewayRebindPredecessorCheckpointFilenamePrefix = "gateway-rebind-predecessor-state.v1.g"
	maxGatewayRebindPredecessorCheckpointBytes       = 256 << 10
)

type gatewayCurrentIdentityKind string

const (
	gatewayCurrentIdentityUpgrade gatewayCurrentIdentityKind = "migration-026-upgrade"
	gatewayCurrentIdentityRebind  gatewayCurrentIdentityKind = "gateway-rebind"
)

// gatewayCurrentIdentity is an explicit stored-format union. The old fixed
// v2 identity and the generation-scoped rebind identity retain their own
// validators and canonical bytes; neither is reinterpreted as the other.
type gatewayCurrentIdentity struct {
	Kind          gatewayCurrentIdentityKind           `json:"kind"`
	Upgrade       *gatewayV2Identity                   `json:"upgrade,omitempty"`
	Rebind        *gatewayRebindSuccessorIdentity      `json:"rebind,omitempty"`
	RebindProfile *gatewayRebindSuccessorIntentProfile `json:"rebindProfile,omitempty"`
}

// gatewayCurrentLANBinding preserves the immutable raw grant binding. A
// transfer is present only when that raw grant was moved to the effective
// profile in this state. It is the chain tip; SQL retains and validates the
// complete oldest-to-newest chain.
type gatewayCurrentLANBinding struct {
	Raw      gatewayV2LANBinding                        `json:"raw"`
	Transfer *appaccess.GatewayRebindAllocationTransfer `json:"transfer,omitempty"`
}

type gatewayCurrentAppRoute struct {
	Route routeRecord               `json:"route"`
	LAN   *gatewayCurrentLANBinding `json:"lan,omitempty"`
}

type gatewayCurrentPendingRoute struct {
	Kind                gatewayV2PendingKind        `json:"kind,omitempty"`
	AppID               string                      `json:"appId"`
	Previous            *gatewayCurrentAppRoute     `json:"previous,omitempty"`
	Proposed            gatewayCurrentAppRoute      `json:"proposed"`
	ActivationUncertain bool                        `json:"activationUncertain,omitempty"`
	Disable             *GatewayV2LANDisableRequest `json:"disable,omitempty"`
}

type gatewayCurrentLANRecoveryBatch struct {
	Head          int                             `json:"head"`
	Items         []gatewayCurrentLANRecoveryItem `json:"items"`
	LegacyPending *gatewayCurrentPendingRoute     `json:"legacyPending,omitempty"`
}

type gatewayCurrentLANRecoveryItem struct {
	Kind    gatewayV2PendingKind        `json:"kind"`
	AppID   string                      `json:"appId"`
	Grant   *gatewayCurrentLANBinding   `json:"grant,omitempty"`
	Disable *GatewayV2LANDisableRequest `json:"disable,omitempty"`
}

// gatewayCurrentRouteState is the mutable operational state of one selected
// gateway generation. Its lineage, profile, identity, network and activation
// manifest are immutable after the create-only baseline. Normal route and LAN
// operations increment Revision and may change only Apps/Pending/LANRecovery.
// A later generation gets a different path, so an ambiguous SQL decision can
// never overwrite the previous current generation.
type gatewayCurrentRouteState struct {
	Version                uint64                             `json:"version"`
	Revision               uint64                             `json:"revision"`
	Lineage                appaccess.GatewayCurrentLineageRef `json:"lineage"`
	Profile                gatewayProfileBinding              `json:"profile"`
	Identity               gatewayCurrentIdentity             `json:"identity"`
	Network                gatewayV2NetworkPlan               `json:"network"`
	TransferManifestDigest string                             `json:"transferManifestDigest"`
	Apps                   map[string]gatewayCurrentAppRoute  `json:"apps"`
	Pending                *gatewayCurrentPendingRoute        `json:"pending,omitempty"`
	LANRecovery            *gatewayCurrentLANRecoveryBatch    `json:"lanRecovery,omitempty"`
	Digest                 string                             `json:"digest"`
}

type gatewayCurrentRouteStateStore struct {
	directory   *stateStore
	dataRoot    string
	generation  uint64
	operationID string
	path        string
	purpose     string
}

// GatewayV2LANEffectiveBindingProof is the minimal protected-side evidence a
// locked controller callback compares with a fresh SQL binding resolution.
// ProtectedLineage is the full protected identity; SQL compares its exact
// GatewayCurrentAuthorityRef projection and does not fabricate legacy upgrade
// generation, identity or journal metadata.
type GatewayV2LANEffectiveBindingProof struct {
	EffectiveProfile       GatewayV2ProfileBinding
	ProtectedLineage       appaccess.GatewayCurrentLineageRef
	TransferChainTipDigest string
	TerminalReceiptDigest  string
}

// gatewayRebindPredecessorCheckpoint is immutable admission evidence. The
// source union contains exact protected bytes for either the legacy upgrade
// state (version 2, revision 0) or a revisioned rebind current state (version
// 1). It is installed only after the prepared SQL claim commits.
type gatewayRebindPredecessorCheckpoint struct {
	Version             int                                `json:"version"`
	Purpose             string                             `json:"purpose"`
	Generation          uint64                             `json:"generation"`
	OperationID         string                             `json:"operationId"`
	Lineage             appaccess.GatewayCurrentLineageRef `json:"lineage"`
	SourceStateVersion  uint64                             `json:"sourceStateVersion"`
	SourceStateRevision uint64                             `json:"sourceStateRevision"`
	SourceStateDigest   string                             `json:"sourceStateDigest"`
	UpgradeState        *gatewayV2RouteState               `json:"upgradeState,omitempty"`
	CurrentState        *gatewayCurrentRouteState          `json:"currentState,omitempty"`
	Digest              string                             `json:"digest"`
}

type gatewayRebindPredecessorCheckpointStore struct {
	directory   *stateStore
	generation  uint64
	operationID string
	path        string
	purpose     string
}

func gatewayCurrentAuthority(lineage appaccess.GatewayCurrentLineageRef) appaccess.GatewayCurrentAuthorityRef {
	return appaccess.GatewayCurrentAuthorityRef{
		Kind: lineage.Kind, OperationID: lineage.OperationID,
		ProfileRevisionID:     lineage.ProfileRevisionID,
		ProfileRevisionNumber: lineage.ProfileRevisionNumber,
		ProfileSpecDigest:     lineage.ProfileSpecDigest,
		TerminalReceiptDigest: lineage.TerminalReceiptDigest,
	}
}

func gatewayUpgradeCurrentLineage(selection gatewayUpgradeGenerationSelection) (appaccess.GatewayCurrentLineageRef, error) {
	if selection.Store == nil || !selection.Existing || selection.PartialState || selection.Retired || selection.Aborted ||
		selection.Journal.Phase != gatewayPhaseCommitted || !validGatewayV2RouteState(selection.State) ||
		!validGatewayMigrationJournal(selection.Journal) || !journalMatchesV2Plan(selection.Journal, selection.State) {
		return appaccess.GatewayCurrentLineageRef{}, errors.New("invalid generated ingress current upgrade lineage")
	}
	journalDigest, err := canonicalDigest(selection.Journal)
	if err != nil {
		return appaccess.GatewayCurrentLineageRef{}, err
	}
	return appaccess.GatewayCurrentLineageRef{
		Kind:                    appaccess.GatewayRebindSourceGatewayUpgrade,
		OperationID:             selection.State.OperationID,
		ProfileRevisionID:       selection.State.Profile.RevisionID,
		ProfileRevisionNumber:   selection.State.Profile.RevisionNumber,
		ProfileSpecDigest:       selection.State.Profile.SpecDigest,
		ProtectedGeneration:     selection.Generation,
		ProtectedIdentityDigest: selection.State.Identity.Digest,
		ProtectedJournalDigest:  journalDigest,
	}, nil
}

func gatewayRebindCurrentLineage(receipt gatewayRebindFinalHandoverTerminalReceipt) (appaccess.GatewayCurrentLineageRef, error) {
	if !validGatewayRebindFinalHandoverTerminalReceiptValue(receipt) ||
		receipt.Disposition != gatewayRebindFinalHandoverTerminalCommit {
		return appaccess.GatewayCurrentLineageRef{}, errors.New("invalid generated ingress current rebind lineage")
	}
	return appaccess.GatewayCurrentLineageRef{
		Kind:                    appaccess.GatewayRebindSourceGatewayRebind,
		OperationID:             receipt.OperationID,
		ProfileRevisionID:       receipt.SuccessorProfile.RevisionID,
		ProfileRevisionNumber:   receipt.SuccessorProfile.RevisionNumber,
		ProfileSpecDigest:       receipt.SuccessorProfile.SpecDigest,
		ProtectedGeneration:     receipt.Generation,
		ProtectedIdentityDigest: receipt.SuccessorIdentity.Digest,
		ProtectedIntentDigest:   receipt.ProtectedIntentDigest,
		TerminalReceiptDigest:   receipt.Digest,
	}, nil
}

func validGatewayCurrentLineage(value appaccess.GatewayCurrentLineageRef) bool {
	if !validCanonicalUUID(value.OperationID) || !validCanonicalUUID(value.ProfileRevisionID) ||
		value.ProfileRevisionNumber <= 0 || !validSHA256(value.ProfileSpecDigest) ||
		!validSHA256(value.ProtectedIdentityDigest) || value.ProtectedGeneration == math.MaxUint64 {
		return false
	}
	switch value.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return validSHA256(value.ProtectedJournalDigest) && value.ProtectedIntentDigest == "" &&
			value.TerminalReceiptDigest == ""
	case appaccess.GatewayRebindSourceGatewayRebind:
		return value.ProtectedGeneration > 0 && value.ProtectedJournalDigest == "" &&
			validSHA256(value.ProtectedIntentDigest) && validSHA256(value.TerminalReceiptDigest)
	default:
		return false
	}
}

func validGatewayCurrentIdentity(lineage appaccess.GatewayCurrentLineageRef, value gatewayCurrentIdentity) bool {
	switch value.Kind {
	case gatewayCurrentIdentityUpgrade:
		return lineage.Kind == appaccess.GatewayRebindSourceGatewayUpgrade && value.Upgrade != nil && value.Rebind == nil &&
			value.RebindProfile == nil &&
			validGatewayV2Identity(lineage.OperationID, *value.Upgrade) && value.Upgrade.Digest == lineage.ProtectedIdentityDigest
	case gatewayCurrentIdentityRebind:
		if lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind || value.Upgrade != nil || value.Rebind == nil ||
			value.RebindProfile == nil {
			return false
		}
		identity := *value.Rebind
		profile := GatewayRebindSuccessorProfile(*value.RebindProfile)
		return validGatewayRebindSuccessorIdentity(lineage.ProtectedGeneration, lineage.OperationID, profile, identity) &&
			identity.Digest == lineage.ProtectedIdentityDigest &&
			identity.Generation == lineage.ProtectedGeneration && identity.OperationID == lineage.OperationID &&
			identity.ProfileRevisionID == lineage.ProfileRevisionID &&
			identity.ProfileRevisionNumber == lineage.ProfileRevisionNumber &&
			identity.ProfileSpecDigest == lineage.ProfileSpecDigest
	default:
		return false
	}
}

func validGatewayCurrentRawBinding(appID string, profile gatewayProfileBinding, value gatewayCurrentLANBinding,
	ports map[uint16]string, allocations, attempts map[string]struct{},
) bool {
	raw := value.Raw
	if !validCanonicalUUID(raw.GrantAttemptID) || !validSHA256(raw.GrantRequestDigest) ||
		!validCanonicalUUID(raw.OwnerOperationID) || !validCanonicalUUID(raw.AccessRevisionID) ||
		raw.AccessRevisionNumber <= 0 || !validSHA256(raw.AccessSpecDigest) ||
		!validCanonicalUUID(raw.AllocationID) || raw.Port < profile.PortStart || raw.Port > profile.PortEnd ||
		!validCanonicalUUID(raw.ProfileRevisionID) || raw.ProfileRevisionNumber <= 0 ||
		!validSHA256(raw.ProfileSpecDigest) || !validCanonicalUUID(raw.ApprovedBy) {
		return false
	}
	if owner, duplicate := ports[raw.Port]; duplicate && owner != appID {
		return false
	}
	if _, duplicate := allocations[raw.AllocationID]; duplicate {
		return false
	}
	if _, duplicate := attempts[raw.GrantAttemptID]; duplicate {
		return false
	}
	ports[raw.Port], allocations[raw.AllocationID], attempts[raw.GrantAttemptID] = appID, struct{}{}, struct{}{}
	if value.Transfer == nil {
		return raw.ProfileRevisionID == profile.RevisionID && raw.ProfileRevisionNumber == profile.RevisionNumber &&
			raw.ProfileSpecDigest == profile.SpecDigest
	}
	transfer := *value.Transfer
	digest, err := appaccess.GatewayRebindAllocationTransferDigest(transfer)
	return err == nil && digest == transfer.TransferDigest && transfer.AppID == appID &&
		transfer.AllocationID == raw.AllocationID && transfer.GrantAttemptID == raw.GrantAttemptID &&
		transfer.SourceProfileRevisionID == raw.ProfileRevisionID &&
		transfer.SourceProfileRevisionNumber == raw.ProfileRevisionNumber &&
		transfer.SourceProfileSpecDigest == raw.ProfileSpecDigest &&
		transfer.SuccessorProfileRevisionID == profile.RevisionID &&
		transfer.SuccessorProfileRevisionNumber == profile.RevisionNumber &&
		transfer.SuccessorProfileSpecDigest == profile.SpecDigest
}

func validGatewayCurrentRouteState(value gatewayCurrentRouteState) bool {
	if value.Version != gatewayCurrentRouteStateVersion || value.Revision == 0 || !validSHA256(value.Digest) ||
		!validGatewayCurrentLineage(value.Lineage) || !validGatewayProfileBinding(value.Profile) ||
		value.Lineage.ProfileRevisionID != value.Profile.RevisionID ||
		value.Lineage.ProfileRevisionNumber != value.Profile.RevisionNumber ||
		value.Lineage.ProfileSpecDigest != value.Profile.SpecDigest ||
		!validGatewayCurrentIdentity(value.Lineage, value.Identity) ||
		!validGatewayV2NetworkPlan(value.Network) || !gatewayV2NetworkExcludesSelectedLAN(value.Profile, value.Network) ||
		!validSHA256(value.TransferManifestDigest) || value.Apps == nil || len(value.Apps) > maxStateApps {
		return false
	}
	ports := make(map[uint16]string)
	allocations := make(map[string]struct{})
	attempts := make(map[string]struct{})
	for appID, app := range value.Apps {
		if !validAppID(appID) || validateRoute(app.Route) != nil {
			return false
		}
		if app.LAN != nil && !validGatewayCurrentRawBinding(appID, value.Profile, *app.LAN, ports, allocations, attempts) {
			return false
		}
	}
	if value.Pending != nil && value.LANRecovery != nil {
		return false
	}
	if value.Pending != nil && !validGatewayCurrentPendingRoute(value) {
		return false
	}
	if value.LANRecovery != nil && !validGatewayCurrentLANRecoveryBatch(value) {
		return false
	}
	digest, err := gatewayCurrentRouteStateDigest(value)
	return err == nil && digest == value.Digest
}

func gatewayCurrentRouteStateDigest(value gatewayCurrentRouteState) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayCurrentEffectiveProof(state gatewayCurrentRouteState,
	binding *gatewayCurrentLANBinding,
) (GatewayV2LANEffectiveBindingProof, error) {
	if !validGatewayCurrentRouteState(state) || binding == nil {
		return GatewayV2LANEffectiveBindingProof{}, errors.New("invalid generated ingress effective binding proof")
	}
	proof := GatewayV2LANEffectiveBindingProof{
		EffectiveProfile: GatewayV2ProfileBinding{
			RevisionID: state.Profile.RevisionID, RevisionNumber: state.Profile.RevisionNumber,
			SpecDigest: state.Profile.SpecDigest, SelectedIPv4: state.Profile.SelectedIPv4,
			InterfaceID: state.Profile.InterfaceID, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd,
		},
		ProtectedLineage:      state.Lineage,
		TerminalReceiptDigest: state.Lineage.TerminalReceiptDigest,
	}
	if binding.Transfer != nil {
		proof.TransferChainTipDigest = binding.Transfer.TransferDigest
	}
	return proof, nil
}

func gatewayUpgradeEffectiveProof(selection gatewayUpgradeGenerationSelection) (GatewayV2LANEffectiveBindingProof, error) {
	lineage, err := gatewayUpgradeCurrentLineage(selection)
	if err != nil {
		return GatewayV2LANEffectiveBindingProof{}, errors.New("invalid generated ingress upgrade effective binding proof")
	}
	return GatewayV2LANEffectiveBindingProof{
		EffectiveProfile: GatewayV2ProfileBinding{
			RevisionID: selection.State.Profile.RevisionID, RevisionNumber: selection.State.Profile.RevisionNumber,
			SpecDigest: selection.State.Profile.SpecDigest, SelectedIPv4: selection.State.Profile.SelectedIPv4,
			InterfaceID: selection.State.Profile.InterfaceID, PortStart: selection.State.Profile.PortStart,
			PortEnd: selection.State.Profile.PortEnd,
		},
		ProtectedLineage: lineage,
	}, nil
}

// newGatewayCurrentRouteBaselineFromV1Terminal derives the additive current
// state for the existing terminal v1 receipt. The receipt remains unchanged;
// the baseline is separately bound to its exact digest and the SQL-validated
// complete transfer manifest.
func newGatewayCurrentRouteBaselineFromV1Terminal(intent gatewayRebindProtectedIntent,
	receipt gatewayRebindFinalHandoverTerminalReceipt, predecessor gatewayV2RouteState,
	transfers []appaccess.GatewayRebindAllocationTransfer,
) (gatewayCurrentRouteState, error) {
	invalid := errors.New("invalid generated ingress rebind current baseline input")
	if !validGatewayRebindProtectedIntent(intent) || !validGatewayRebindFinalHandoverTerminalReceiptValue(receipt) ||
		receipt.Disposition != gatewayRebindFinalHandoverTerminalCommit || receipt.Generation != intent.Generation ||
		receipt.OperationID != intent.OperationID || receipt.ProtectedIntentDigest != intent.Digest ||
		receipt.SuccessorIdentity != intent.Intent.Identity || receipt.SuccessorProfile != intent.Intent.SuccessorProfile ||
		receipt.RosterCount != int64(len(transfers)) || !validGatewayV2RouteState(predecessor) ||
		predecessor.Pending != nil || predecessor.LANRecovery != nil ||
		!gatewayRebindProtectedRosterMatchesPredecessor(intent.Intent.Roster, predecessor) {
		return gatewayCurrentRouteState{}, invalid
	}
	lineage, err := gatewayRebindCurrentLineage(receipt)
	if err != nil {
		return gatewayCurrentRouteState{}, invalid
	}
	manifestDigest, err := appaccess.GatewayRebindTransferManifestDigest(transfers)
	if err != nil {
		return gatewayCurrentRouteState{}, invalid
	}
	byApp := make(map[string]appaccess.GatewayRebindAllocationTransfer, len(transfers))
	for _, transfer := range transfers {
		digest, digestErr := appaccess.GatewayRebindAllocationTransferDigest(transfer)
		if digestErr != nil || transfer.TransferDigest != digest || transfer.OperationID != receipt.OperationID ||
			transfer.TerminalReceiptDigest != receipt.Digest ||
			transfer.SuccessorProfileRevisionID != receipt.SuccessorProfile.RevisionID ||
			transfer.SuccessorProfileRevisionNumber != receipt.SuccessorProfile.RevisionNumber ||
			transfer.SuccessorProfileSpecDigest != receipt.SuccessorProfile.SpecDigest {
			return gatewayCurrentRouteState{}, invalid
		}
		if _, duplicate := byApp[transfer.AppID]; duplicate {
			return gatewayCurrentRouteState{}, invalid
		}
		byApp[transfer.AppID] = transfer
	}
	apps := make(map[string]gatewayCurrentAppRoute, len(predecessor.Apps))
	usedTransfers := 0
	for appID, app := range predecessor.Apps {
		current := gatewayCurrentAppRoute{Route: app.Route}
		current.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
		if app.LAN != nil {
			transfer, ok := byApp[appID]
			if !ok || transfer.AllocationID != app.LAN.AllocationID || transfer.GrantAttemptID != app.LAN.GrantAttemptID ||
				transfer.SourceProfileRevisionID != app.LAN.ProfileRevisionID ||
				transfer.SourceProfileRevisionNumber != app.LAN.ProfileRevisionNumber ||
				transfer.SourceProfileSpecDigest != app.LAN.ProfileSpecDigest {
				return gatewayCurrentRouteState{}, invalid
			}
			raw := *app.LAN
			transferCopy := transfer
			if transfer.PredecessorTransferDigest != nil {
				predecessorDigest := *transfer.PredecessorTransferDigest
				transferCopy.PredecessorTransferDigest = &predecessorDigest
			}
			current.LAN = &gatewayCurrentLANBinding{Raw: raw, Transfer: &transferCopy}
			usedTransfers++
		}
		apps[appID] = current
	}
	if usedTransfers != len(transfers) {
		return gatewayCurrentRouteState{}, invalid
	}
	state := gatewayCurrentRouteState{
		Version: gatewayCurrentRouteStateVersion, Revision: 1, Lineage: lineage,
		Profile: gatewayProfileBinding{
			RevisionID: receipt.SuccessorProfile.RevisionID, RevisionNumber: receipt.SuccessorProfile.RevisionNumber,
			SpecDigest: receipt.SuccessorProfile.SpecDigest, SelectedIPv4: receipt.SuccessorProfile.SelectedIPv4,
			InterfaceID: receipt.SuccessorProfile.InterfaceID, PortStart: receipt.SuccessorProfile.PortStart,
			PortEnd: receipt.SuccessorProfile.PortEnd,
		},
		Identity: gatewayCurrentIdentity{Kind: gatewayCurrentIdentityRebind, Rebind: &receipt.SuccessorIdentity,
			RebindProfile: &receipt.SuccessorProfile},
		Network: gatewayV2NetworkPlan(intent.Intent.Network), TransferManifestDigest: manifestDigest, Apps: apps,
	}
	state.Digest, err = gatewayCurrentRouteStateDigest(state)
	if err != nil || !validGatewayCurrentRouteState(state) {
		return gatewayCurrentRouteState{}, invalid
	}
	return state, nil
}

func cloneGatewayCurrentRouteState(value gatewayCurrentRouteState) gatewayCurrentRouteState {
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
	result.Apps = make(map[string]gatewayCurrentAppRoute, len(value.Apps))
	for appID, app := range value.Apps {
		result.Apps[appID] = cloneGatewayCurrentAppRoute(app)
	}
	result.Pending = cloneGatewayCurrentPendingRoute(value.Pending)
	result.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(value.LANRecovery)
	return result
}

func cloneGatewayCurrentAppRoute(app gatewayCurrentAppRoute) gatewayCurrentAppRoute {
	app.Route.Endpoints = append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...)
	if app.LAN != nil {
		binding := *app.LAN
		if app.LAN.Transfer != nil {
			transfer := *app.LAN.Transfer
			if transfer.PredecessorTransferDigest != nil {
				predecessor := *transfer.PredecessorTransferDigest
				transfer.PredecessorTransferDigest = &predecessor
			}
			binding.Transfer = &transfer
		}
		app.LAN = &binding
	}
	return app
}

func cloneGatewayCurrentPendingRoute(value *gatewayCurrentPendingRoute) *gatewayCurrentPendingRoute {
	if value == nil {
		return nil
	}
	pending := *value
	pending.Proposed = cloneGatewayCurrentAppRoute(value.Proposed)
	if value.Previous != nil {
		previous := cloneGatewayCurrentAppRoute(*value.Previous)
		pending.Previous = &previous
	}
	if value.Disable != nil {
		disable := *value.Disable
		if disable.SourceGrant != nil {
			source := *disable.SourceGrant
			disable.SourceGrant = &source
		}
		pending.Disable = &disable
	}
	return &pending
}

func cloneGatewayCurrentLANRecoveryBatch(value *gatewayCurrentLANRecoveryBatch) *gatewayCurrentLANRecoveryBatch {
	if value == nil {
		return nil
	}
	batch := *value
	batch.Items = append([]gatewayCurrentLANRecoveryItem(nil), value.Items...)
	for index := range batch.Items {
		if batch.Items[index].Grant != nil {
			grant := cloneGatewayCurrentLANBinding(*batch.Items[index].Grant)
			batch.Items[index].Grant = &grant
		}
		if batch.Items[index].Disable != nil {
			disable := *batch.Items[index].Disable
			if disable.SourceGrant != nil {
				source := *disable.SourceGrant
				disable.SourceGrant = &source
			}
			batch.Items[index].Disable = &disable
		}
	}
	batch.LegacyPending = cloneGatewayCurrentPendingRoute(value.LegacyPending)
	return &batch
}

func cloneGatewayCurrentLANBinding(value gatewayCurrentLANBinding) gatewayCurrentLANBinding {
	if value.Transfer != nil {
		transfer := *value.Transfer
		if transfer.PredecessorTransferDigest != nil {
			predecessor := *transfer.PredecessorTransferDigest
			transfer.PredecessorTransferDigest = &predecessor
		}
		value.Transfer = &transfer
	}
	return value
}

func newGatewayCurrentRouteStateStore(dataRoot string, lineage appaccess.GatewayCurrentLineageRef) (*gatewayCurrentRouteStateStore, error) {
	if !validGatewayCurrentLineage(lineage) {
		return nil, errors.New("invalid generated ingress current route store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayCurrentRouteStateName(lineage.ProtectedGeneration, lineage.OperationID)
	return &gatewayCurrentRouteStateStore{directory: directory, dataRoot: dataRoot,
		generation: lineage.ProtectedGeneration, operationID: lineage.OperationID,
		path: filepath.Join(directory.root, name), purpose: purpose}, nil
}

func gatewayCurrentRouteStateName(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayCurrentRouteStateFilenamePrefix + token + ".bundle", gatewayCurrentRouteStatePurpose + "/" + scope
}

func (s *gatewayCurrentRouteStateStore) installBaseline(value gatewayCurrentRouteState) error {
	if s == nil || s.directory == nil || value.Revision != 1 || value.Lineage.ProtectedGeneration != s.generation ||
		value.Lineage.OperationID != s.operationID || !validGatewayCurrentRouteState(value) {
		return errors.New("invalid generated ingress current route baseline")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayCurrentRouteStateBytes); err != nil {
		return err
	}
	installed, err := s.load()
	if err != nil || !reflect.DeepEqual(installed, value) {
		return errors.New("generated ingress current route baseline readback mismatch")
	}
	return nil
}

func (s *gatewayCurrentRouteStateStore) load() (gatewayCurrentRouteState, error) {
	if s == nil || s.directory == nil {
		return gatewayCurrentRouteState{}, errors.New("invalid generated ingress current route store")
	}
	var value gatewayCurrentRouteState
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayCurrentRouteStateBytes, &value); err != nil ||
		value.Lineage.ProtectedGeneration != s.generation || value.Lineage.OperationID != s.operationID ||
		!validGatewayCurrentRouteState(value) {
		return gatewayCurrentRouteState{}, errors.New("invalid generated ingress current route state")
	}
	return value, nil
}

func sameGatewayCurrentRouteOrigin(left, right gatewayCurrentRouteState) bool {
	return left.Version == right.Version && left.Lineage == right.Lineage && left.Profile == right.Profile &&
		reflect.DeepEqual(left.Identity, right.Identity) && left.Network == right.Network &&
		left.TransferManifestDigest == right.TransferManifestDigest
}

// saveNext is the only overwrite primitive for a selected rebind current
// generation. Callers must first prove selection from fresh SQL authority.
// The immutable activation origin cannot change and revisions cannot skip.
func (s *gatewayCurrentRouteStateStore) saveNext(previous, next gatewayCurrentRouteState) error {
	if s == nil || s.directory == nil || previous.Revision == math.MaxUint64 ||
		next.Revision != previous.Revision+1 || !sameGatewayCurrentRouteOrigin(previous, next) ||
		previous.Lineage.ProtectedGeneration != s.generation || previous.Lineage.OperationID != s.operationID ||
		!validGatewayCurrentRouteState(previous) || !validGatewayCurrentRouteState(next) {
		return errors.New("invalid generated ingress current route update")
	}
	installed, err := s.load()
	if err != nil || !reflect.DeepEqual(installed, previous) {
		return errors.New("generated ingress current route update is stale")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, next, false, maxGatewayCurrentRouteStateBytes); err != nil {
		return err
	}
	confirmed, err := s.load()
	if err != nil || !reflect.DeepEqual(confirmed, next) {
		return errors.New("generated ingress current route update readback mismatch")
	}
	return nil
}

func newGatewayRebindPredecessorCheckpoint(generation uint64, operationID string,
	lineage appaccess.GatewayCurrentLineageRef, upgrade *gatewayV2RouteState, current *gatewayCurrentRouteState,
) (gatewayRebindPredecessorCheckpoint, error) {
	invalid := errors.New("invalid generated ingress rebind predecessor checkpoint input")
	if generation == 0 || !validCanonicalUUID(operationID) || !validGatewayCurrentLineage(lineage) ||
		generation <= lineage.ProtectedGeneration || (upgrade == nil) == (current == nil) {
		return gatewayRebindPredecessorCheckpoint{}, invalid
	}
	value := gatewayRebindPredecessorCheckpoint{
		Version: gatewayRebindPredecessorCheckpointVersion, Purpose: gatewayRebindPredecessorCheckpointPurpose,
		Generation: generation, OperationID: operationID, Lineage: lineage,
	}
	if upgrade != nil {
		if lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade || !validGatewayV2RouteState(*upgrade) ||
			upgrade.OperationID != lineage.OperationID || upgrade.Profile.RevisionID != lineage.ProfileRevisionID ||
			upgrade.Profile.RevisionNumber != lineage.ProfileRevisionNumber || upgrade.Profile.SpecDigest != lineage.ProfileSpecDigest ||
			upgrade.Identity.Digest != lineage.ProtectedIdentityDigest || upgrade.Pending != nil || upgrade.LANRecovery != nil {
			return gatewayRebindPredecessorCheckpoint{}, invalid
		}
		copy := cloneGatewayV2RouteState(*upgrade)
		value.SourceStateVersion, value.SourceStateRevision, value.UpgradeState = 2, 0, &copy
		value.SourceStateDigest, _ = canonicalDigest(copy)
	} else {
		if lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind || !validGatewayCurrentRouteState(*current) ||
			current.Pending != nil || current.LANRecovery != nil || current.Lineage != lineage {
			return gatewayRebindPredecessorCheckpoint{}, invalid
		}
		copy := cloneGatewayCurrentRouteState(*current)
		value.SourceStateVersion, value.SourceStateRevision, value.CurrentState = 1, copy.Revision, &copy
		value.SourceStateDigest = copy.Digest
	}
	var err error
	value.Digest, err = gatewayRebindPredecessorCheckpointDigest(value)
	if err != nil || !validGatewayRebindPredecessorCheckpoint(value) {
		return gatewayRebindPredecessorCheckpoint{}, invalid
	}
	return value, nil
}

func gatewayRebindPredecessorCheckpointDigest(value gatewayRebindPredecessorCheckpoint) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayRebindPredecessorCheckpoint(value gatewayRebindPredecessorCheckpoint) bool {
	if value.Version != gatewayRebindPredecessorCheckpointVersion || value.Purpose != gatewayRebindPredecessorCheckpointPurpose ||
		value.Generation == 0 || value.Generation <= value.Lineage.ProtectedGeneration ||
		!validCanonicalUUID(value.OperationID) || !validGatewayCurrentLineage(value.Lineage) ||
		!validSHA256(value.SourceStateDigest) || !validSHA256(value.Digest) ||
		(value.UpgradeState == nil) == (value.CurrentState == nil) {
		return false
	}
	if value.UpgradeState != nil {
		state := *value.UpgradeState
		digest, err := canonicalDigest(state)
		if value.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade || value.SourceStateVersion != 2 ||
			value.SourceStateRevision != 0 || err != nil || digest != value.SourceStateDigest ||
			!validGatewayV2RouteState(state) || state.Pending != nil || state.LANRecovery != nil ||
			state.OperationID != value.Lineage.OperationID || state.Profile.RevisionID != value.Lineage.ProfileRevisionID ||
			state.Profile.RevisionNumber != value.Lineage.ProfileRevisionNumber || state.Profile.SpecDigest != value.Lineage.ProfileSpecDigest ||
			state.Identity.Digest != value.Lineage.ProtectedIdentityDigest {
			return false
		}
	} else {
		state := *value.CurrentState
		if value.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind || value.SourceStateVersion != 1 ||
			value.SourceStateRevision != state.Revision || value.SourceStateDigest != state.Digest ||
			!validGatewayCurrentRouteState(state) || state.Pending != nil || state.LANRecovery != nil || state.Lineage != value.Lineage {
			return false
		}
	}
	digest, err := gatewayRebindPredecessorCheckpointDigest(value)
	return err == nil && digest == value.Digest
}

func (value gatewayRebindPredecessorCheckpoint) sourceRef() appaccess.GatewayRebindSourceRef {
	return appaccess.GatewayRebindSourceRef{
		Lineage: value.Lineage, SourceStateVersion: value.SourceStateVersion,
		SourceStateRevision: value.SourceStateRevision, SourceStateDigest: value.SourceStateDigest,
		PredecessorCheckpointDigest: value.Digest,
	}
}

func newGatewayRebindPredecessorCheckpointStore(dataRoot string, generation uint64,
	operationID string,
) (*gatewayRebindPredecessorCheckpointStore, error) {
	if generation == 0 || !validCanonicalUUID(operationID) {
		return nil, errors.New("invalid generated ingress rebind predecessor checkpoint store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindPredecessorCheckpointName(generation, operationID)
	return &gatewayRebindPredecessorCheckpointStore{directory: directory, generation: generation,
		operationID: operationID, path: filepath.Join(directory.root, name), purpose: purpose}, nil
}

func gatewayRebindPredecessorCheckpointName(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayRebindPredecessorCheckpointFilenamePrefix + token + ".bundle",
		gatewayRebindPredecessorCheckpointPurpose + "/" + scope
}

func (s *gatewayRebindPredecessorCheckpointStore) installExact(value gatewayRebindPredecessorCheckpoint) error {
	if s == nil || s.directory == nil || value.Generation != s.generation || value.OperationID != s.operationID ||
		!validGatewayRebindPredecessorCheckpoint(value) {
		return errors.New("invalid generated ingress rebind predecessor checkpoint install")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayRebindPredecessorCheckpointBytes); err != nil {
		return err
	}
	installed, err := s.load()
	if err != nil || !reflect.DeepEqual(installed, value) {
		return errors.New("generated ingress rebind predecessor checkpoint readback mismatch")
	}
	return nil
}

func (s *gatewayRebindPredecessorCheckpointStore) load() (gatewayRebindPredecessorCheckpoint, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindPredecessorCheckpoint{}, errors.New("invalid generated ingress rebind predecessor checkpoint store")
	}
	var value gatewayRebindPredecessorCheckpoint
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayRebindPredecessorCheckpointBytes, &value); err != nil ||
		value.Generation != s.generation || value.OperationID != s.operationID ||
		!validGatewayRebindPredecessorCheckpoint(value) {
		return gatewayRebindPredecessorCheckpoint{}, errors.New("invalid generated ingress rebind predecessor checkpoint")
	}
	return value, nil
}
