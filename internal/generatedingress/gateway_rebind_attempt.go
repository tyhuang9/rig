package generatedingress

import (
	"errors"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
)

// gatewayRebindAttemptView is an in-memory adapter over the immutable legacy
// and typed protected intents. It is deliberately not serialized: each
// persisted format keeps its own validator and canonical bytes, while effect
// code consumes one strictly validated observation without inventing a
// migration-026 journal for a prior rebind.
type gatewayRebindAttemptFormat string

const (
	gatewayRebindAttemptLegacyV1 gatewayRebindAttemptFormat = "legacy_v1"
	gatewayRebindAttemptTypedV2  gatewayRebindAttemptFormat = "typed_v2"
)

type gatewayRebindAttemptView struct {
	Format                   gatewayRebindAttemptFormat
	Generation               uint64
	OperationID              string
	ClaimRequestDigest       string
	ClaimSpecDigest          string
	PreparedDatabaseDigest   string
	ProtectedIntentDigest    string
	Source                   gatewayRebindAttemptSourceView
	SuccessorProfile         gatewayRebindSuccessorIntentProfile
	Roster                   []gatewayRebindAttemptRosterEntry
	RosterDigest             string
	RosterEntryDigests       []string
	Network                  gatewayRebindSuccessorIntentNetwork
	NetworkDigest            string
	NetworkObservation       gatewayRebindSuccessorNetworkObservation
	NetworkObservationDigest string
	Identity                 gatewayRebindSuccessorIdentity
	LegacyIntent             *gatewayRebindProtectedIntent
	TypedIntent              *gatewayRebindProtectedIntentV2
}

type gatewayRebindAttemptSourceView struct {
	Lineage          appaccess.GatewayCurrentLineageRef
	StateVersion     uint64
	StateRevision    uint64
	StateDigest      string
	CheckpointDigest string
	FrozenRef        *appaccess.GatewayRebindSourceRef
	Upgrade          *gatewayRebindAttemptUpgradeSource
	Rebind           *gatewayRebindAttemptCommittedSource
}

type gatewayRebindAttemptUpgradeSource struct {
	Routes  routeState
	State   gatewayV2RouteState
	Journal gatewayMigrationJournal
}

// gatewayRebindAttemptCommittedSource retains a common validated terminal
// projection and the actual source receipt format. LegacyReceipt is never
// populated from typed bytes. A typed terminal format adds its own mutually
// exclusive receipt member without changing or re-encoding legacy receipts.
type gatewayRebindAttemptCommittedSource struct {
	State    gatewayCurrentRouteState
	Terminal gatewayRebindAttemptTerminalView
}

type gatewayRebindAttemptTerminalFormat string

const (
	gatewayRebindAttemptTerminalLegacyV1 gatewayRebindAttemptTerminalFormat = "legacy_v1"
	gatewayRebindAttemptTerminalTypedV2  gatewayRebindAttemptTerminalFormat = "typed_v2"
)

type gatewayRebindAttemptTerminalView struct {
	Format                gatewayRebindAttemptTerminalFormat
	Generation            uint64
	OperationID           string
	Disposition           gatewayRebindFinalHandoverTerminalDisposition
	ProtectedIntentDigest string
	SuccessorProfile      gatewayRebindSuccessorIntentProfile
	SuccessorIdentity     gatewayRebindSuccessorIdentity
	Resources             gatewayRebindFinalHandoverResourceBindings
	Digest                string
	LegacyReceipt         *gatewayRebindFinalHandoverTerminalReceipt
	TypedReceipt          *gatewayRebindTerminalReceiptV2
}

type gatewayRebindAttemptRosterEntry struct {
	OperationID                 string
	Ordinal                     int64
	AppID                       string
	AllocationID                string
	Port                        uint16
	AllocationOwnerOperationID  string
	AllocationState             appaccess.AllocationState
	AccessRevisionID            string
	AccessRevisionNumber        int64
	AccessSpecDigest            string
	GrantAttemptID              string
	GrantStateSequence          int64
	GrantProtectedStateDigest   string
	SourceProfileRevisionID     string
	SourceProfileRevisionNumber int64
	SourceProfileSpecDigest     string
	PredecessorTransferDigest   *string
	ServingDeploymentID         string
	ServingReleaseID            string
	ServingSlot                 string
	RouteGeneration             int64
	EntryDigest                 string
}

func newGatewayRebindAttemptViewLegacy(intent gatewayRebindProtectedIntent, source routeState,
	predecessor gatewayUpgradeGenerationSelection,
) (gatewayRebindAttemptView, error) {
	invalid := errors.New("invalid generated ingress legacy rebind attempt")
	if !validGatewayRebindProtectedIntent(intent) || !validRouteState(source) || source.Pending != nil ||
		!gatewayRebindProtectedIntentMatchesPredecessor(intent, predecessor) {
		return gatewayRebindAttemptView{}, invalid
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil || sourceDigest != predecessor.State.SourceV1StateDigest {
		return gatewayRebindAttemptView{}, invalid
	}
	lineage, err := gatewayUpgradeCurrentLineage(predecessor)
	if err != nil || lineage.OperationID != intent.Intent.Predecessor.OperationID ||
		lineage.ProtectedGeneration != intent.Intent.Predecessor.Generation ||
		lineage.ProtectedIdentityDigest != intent.Intent.Predecessor.IdentityDigest ||
		lineage.ProtectedJournalDigest != intent.Intent.Predecessor.JournalDigest {
		return gatewayRebindAttemptView{}, invalid
	}
	roster := make([]gatewayRebindAttemptRosterEntry, len(intent.Intent.Roster))
	for index, entry := range intent.Intent.Roster {
		roster[index] = gatewayRebindAttemptRosterEntry{
			OperationID: entry.OperationID, Ordinal: entry.Ordinal, AppID: entry.AppID,
			AllocationID: entry.AllocationID, Port: entry.Port,
			AllocationOwnerOperationID: entry.AllocationOwnerOperationID, AllocationState: entry.AllocationState,
			AccessRevisionID: entry.AccessRevisionID, AccessRevisionNumber: entry.AccessRevisionNumber,
			AccessSpecDigest: entry.AccessSpecDigest, GrantAttemptID: entry.GrantAttemptID,
			GrantStateSequence: entry.GrantStateSequence, GrantProtectedStateDigest: entry.GrantProtectedStateDigest,
			SourceProfileRevisionID:     predecessor.State.Profile.RevisionID,
			SourceProfileRevisionNumber: predecessor.State.Profile.RevisionNumber,
			SourceProfileSpecDigest:     predecessor.State.Profile.SpecDigest,
			ServingDeploymentID:         entry.ServingDeploymentID, ServingReleaseID: entry.ServingReleaseID,
			ServingSlot: entry.ServingSlot, RouteGeneration: entry.RouteGeneration, EntryDigest: entry.EntryDigest,
		}
	}
	legacy := intent
	return gatewayRebindAttemptView{
		Format: gatewayRebindAttemptLegacyV1, Generation: intent.Generation, OperationID: intent.OperationID,
		ClaimRequestDigest: intent.Intent.Claim.RequestDigest, ClaimSpecDigest: intent.Intent.Claim.SpecDigest,
		PreparedDatabaseDigest: intent.DatabaseDigest, ProtectedIntentDigest: intent.Digest,
		Source: gatewayRebindAttemptSourceView{
			Lineage: lineage, StateVersion: 2, StateRevision: 0,
			StateDigest: intent.Intent.Predecessor.StateDigest,
			Upgrade: &gatewayRebindAttemptUpgradeSource{Routes: cloneRouteState(source),
				State: cloneGatewayV2RouteState(predecessor.State), Journal: predecessor.Journal},
		},
		SuccessorProfile: intent.Intent.SuccessorProfile, Roster: roster,
		RosterDigest:       intent.Intent.Claim.RosterDigest,
		RosterEntryDigests: append([]string(nil), intent.Intent.RosterEntryDigests...),
		Network:            intent.Intent.Network, NetworkDigest: intent.Intent.NetworkDigest,
		NetworkObservation: intent.NetworkObservation, NetworkObservationDigest: intent.NetworkObservationDigest,
		Identity: intent.Intent.Identity, LegacyIntent: &legacy,
	}, nil
}

func newGatewayRebindAttemptViewV2(intent gatewayRebindProtectedIntentV2,
	checkpoint gatewayRebindPredecessorCheckpoint, current gatewayCurrentSelection,
) (gatewayRebindAttemptView, error) {
	invalid := errors.New("invalid generated ingress typed rebind attempt")
	if !validGatewayRebindProtectedIntentV2(intent) || !validGatewayRebindPredecessorCheckpoint(checkpoint) ||
		intent.Generation != checkpoint.Generation || intent.OperationID != checkpoint.OperationID ||
		intent.Predecessor != checkpoint.sourceRef() || current.Lineage != checkpoint.Lineage {
		return gatewayRebindAttemptView{}, invalid
	}
	source := gatewayRebindAttemptSourceView{
		Lineage: checkpoint.Lineage, StateVersion: checkpoint.SourceStateVersion,
		StateRevision: checkpoint.SourceStateRevision, StateDigest: checkpoint.SourceStateDigest,
		CheckpointDigest: checkpoint.Digest,
	}
	frozen := checkpoint.sourceRef()
	source.FrozenRef = &frozen
	switch checkpoint.Lineage.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		if current.Kind != gatewayCurrentSelectionUpgrade || current.Upgrade == nil || current.UpgradeSource == nil ||
			checkpoint.UpgradeState == nil || checkpoint.CurrentState != nil ||
			!validRouteState(*current.UpgradeSource) || current.UpgradeSource.Pending != nil ||
			!gatewayRebindCheckpointMatchesUpgradePredecessor(checkpoint, *current.Upgrade) ||
			!reflect.DeepEqual(*checkpoint.UpgradeState, current.Upgrade.State) {
			return gatewayRebindAttemptView{}, invalid
		}
		rawDigest, err := canonicalDigest(*current.UpgradeSource)
		if err != nil || rawDigest != checkpoint.UpgradeState.SourceV1StateDigest {
			return gatewayRebindAttemptView{}, invalid
		}
		source.Upgrade = &gatewayRebindAttemptUpgradeSource{
			Routes: cloneRouteState(*current.UpgradeSource), State: cloneGatewayV2RouteState(*checkpoint.UpgradeState),
			Journal: current.Upgrade.Journal,
		}
	case appaccess.GatewayRebindSourceGatewayRebind:
		if current.Kind != gatewayCurrentSelectionRebind || current.State == nil || current.Terminal == nil ||
			checkpoint.UpgradeState != nil || checkpoint.CurrentState == nil ||
			!reflect.DeepEqual(*checkpoint.CurrentState, *current.State) {
			return gatewayRebindAttemptView{}, invalid
		}
		terminal := *current.Terminal
		if !gatewayRebindAttemptTerminalMatchesLineage(terminal, checkpoint.Lineage) {
			return gatewayRebindAttemptView{}, invalid
		}
		source.Rebind = &gatewayRebindAttemptCommittedSource{
			State: cloneGatewayCurrentRouteState(*checkpoint.CurrentState), Terminal: terminal,
		}
	default:
		return gatewayRebindAttemptView{}, invalid
	}
	if !gatewayRebindAttemptRosterMatchesSource(intent.Roster, checkpoint) {
		return gatewayRebindAttemptView{}, invalid
	}
	roster := make([]gatewayRebindAttemptRosterEntry, len(intent.Roster))
	for index, entry := range intent.Roster {
		var predecessorTransfer *string
		if entry.PredecessorTransferDigest != nil {
			value := *entry.PredecessorTransferDigest
			predecessorTransfer = &value
		}
		roster[index] = gatewayRebindAttemptRosterEntry{
			OperationID: entry.OperationID, Ordinal: entry.Ordinal, AppID: entry.AppID,
			AllocationID: entry.AllocationID, Port: entry.Port,
			AllocationOwnerOperationID: entry.AllocationOwnerOperationID, AllocationState: entry.AllocationState,
			AccessRevisionID: entry.AccessRevisionID, AccessRevisionNumber: entry.AccessRevisionNumber,
			AccessSpecDigest: entry.AccessSpecDigest, GrantAttemptID: entry.GrantAttemptID,
			GrantStateSequence: entry.GrantStateSequence, GrantProtectedStateDigest: entry.GrantProtectedStateDigest,
			SourceProfileRevisionID:     entry.SourceProfileRevisionID,
			SourceProfileRevisionNumber: entry.SourceProfileRevisionNumber,
			SourceProfileSpecDigest:     entry.SourceProfileSpecDigest,
			PredecessorTransferDigest:   predecessorTransfer,
			ServingDeploymentID:         entry.ServingDeploymentID, ServingReleaseID: entry.ServingReleaseID,
			ServingSlot: entry.ServingSlot, RouteGeneration: entry.RouteGeneration, EntryDigest: entry.EntryDigest,
		}
	}
	specDigest, err := appaccess.GatewayRebindSpecV2Digest(intent.Claim.Spec)
	if err != nil {
		return gatewayRebindAttemptView{}, invalid
	}
	typed := intent
	return gatewayRebindAttemptView{
		Format: gatewayRebindAttemptTypedV2, Generation: intent.Generation, OperationID: intent.OperationID,
		ClaimRequestDigest: intent.Claim.RequestDigest, ClaimSpecDigest: specDigest,
		PreparedDatabaseDigest: intent.DatabaseDigest, ProtectedIntentDigest: intent.Digest, Source: source,
		SuccessorProfile: intent.SuccessorProfile, Roster: roster,
		RosterDigest:       intent.Claim.Spec.RosterDigest,
		RosterEntryDigests: append([]string(nil), intent.RosterEntryDigests...),
		Network:            intent.Network, NetworkDigest: intent.NetworkDigest,
		NetworkObservation: intent.NetworkObservation, NetworkObservationDigest: intent.NetworkObservationDigest,
		Identity: intent.Identity, TypedIntent: &typed,
	}, nil
}

func gatewayRebindAttemptTerminalMatchesLineage(terminal gatewayRebindAttemptTerminalView,
	lineage appaccess.GatewayCurrentLineageRef,
) bool {
	if terminal.Digest != lineage.TerminalReceiptDigest {
		return false
	}
	var receiptLineage appaccess.GatewayCurrentLineageRef
	var canonical gatewayRebindAttemptTerminalView
	var err error
	switch terminal.Format {
	case gatewayRebindAttemptTerminalLegacyV1:
		if terminal.LegacyReceipt == nil || terminal.TypedReceipt != nil {
			return false
		}
		canonical, err = newGatewayRebindAttemptTerminalViewLegacy(*terminal.LegacyReceipt)
		if err != nil || !reflect.DeepEqual(terminal, canonical) {
			return false
		}
		receiptLineage, err = gatewayRebindCurrentLineage(*terminal.LegacyReceipt)
	case gatewayRebindAttemptTerminalTypedV2:
		if terminal.TypedReceipt == nil || terminal.LegacyReceipt != nil {
			return false
		}
		canonical, err = newGatewayRebindAttemptTerminalViewV2(*terminal.TypedReceipt)
		if err != nil || !reflect.DeepEqual(terminal, canonical) {
			return false
		}
		receiptLineage, err = gatewayRebindCurrentLineageV2(*terminal.TypedReceipt)
	default:
		return false
	}
	return err == nil && receiptLineage == lineage && terminal.Generation == lineage.ProtectedGeneration &&
		terminal.OperationID == lineage.OperationID && terminal.ProtectedIntentDigest == lineage.ProtectedIntentDigest &&
		terminal.SuccessorIdentity.Digest == lineage.ProtectedIdentityDigest &&
		terminal.SuccessorProfile.RevisionID == lineage.ProfileRevisionID &&
		terminal.SuccessorProfile.RevisionNumber == lineage.ProfileRevisionNumber &&
		terminal.SuccessorProfile.SpecDigest == lineage.ProfileSpecDigest
}

func newGatewayRebindAttemptTerminalViewV2(receipt gatewayRebindTerminalReceiptV2) (gatewayRebindAttemptTerminalView, error) {
	if !validGatewayRebindTerminalReceiptV2(receipt) || receipt.Disposition != appaccess.GatewayRebindDispositionCommit ||
		receipt.Resources == nil {
		return gatewayRebindAttemptTerminalView{}, errors.New("invalid typed rebind terminal")
	}
	typed := receipt
	return gatewayRebindAttemptTerminalView{
		Format: gatewayRebindAttemptTerminalTypedV2, Generation: receipt.Generation,
		OperationID: receipt.OperationID, Disposition: gatewayRebindFinalHandoverTerminalCommit,
		ProtectedIntentDigest: receipt.ProtectedIntentDigest, SuccessorProfile: receipt.SuccessorProfile,
		SuccessorIdentity: receipt.SuccessorIdentity, Resources: *receipt.Resources, Digest: receipt.Digest,
		TypedReceipt: &typed,
	}, nil
}

func gatewayRebindAttemptRosterMatchesSource(roster []appaccess.GatewayRebindRosterEntryV2,
	checkpoint gatewayRebindPredecessorCheckpoint,
) bool {
	seen := make(map[string]struct{}, len(roster))
	for _, entry := range roster {
		if _, duplicate := seen[entry.AppID]; duplicate {
			return false
		}
		var route routeRecord
		var raw gatewayV2LANBinding
		var transfer *appaccess.GatewayRebindAllocationTransfer
		switch {
		case checkpoint.UpgradeState != nil && checkpoint.CurrentState == nil:
			app, ok := checkpoint.UpgradeState.Apps[entry.AppID]
			if !ok || app.LAN == nil {
				return false
			}
			route, raw = app.Route, *app.LAN
		case checkpoint.UpgradeState == nil && checkpoint.CurrentState != nil:
			app, ok := checkpoint.CurrentState.Apps[entry.AppID]
			if !ok || app.LAN == nil {
				return false
			}
			route, raw, transfer = app.Route, app.LAN.Raw, app.LAN.Transfer
		default:
			return false
		}
		if raw.AllocationID != entry.AllocationID || raw.Port != entry.Port ||
			raw.OwnerOperationID != entry.AllocationOwnerOperationID || raw.AccessRevisionID != entry.AccessRevisionID ||
			raw.AccessRevisionNumber != entry.AccessRevisionNumber || raw.AccessSpecDigest != entry.AccessSpecDigest ||
			raw.GrantAttemptID != entry.GrantAttemptID || raw.ProfileRevisionID != entry.SourceProfileRevisionID ||
			raw.ProfileRevisionNumber != entry.SourceProfileRevisionNumber || raw.ProfileSpecDigest != entry.SourceProfileSpecDigest ||
			string(route.Slot) != entry.ServingSlot {
			return false
		}
		if transfer == nil {
			if entry.PredecessorTransferDigest != nil {
				return false
			}
		} else if entry.PredecessorTransferDigest == nil ||
			*entry.PredecessorTransferDigest != transfer.TransferDigest {
			return false
		}
		seen[entry.AppID] = struct{}{}
	}
	lanApps := 0
	if checkpoint.UpgradeState != nil {
		for appID, app := range checkpoint.UpgradeState.Apps {
			if app.LAN != nil {
				lanApps++
				if _, ok := seen[appID]; !ok {
					return false
				}
			}
		}
	} else if checkpoint.CurrentState != nil {
		for appID, app := range checkpoint.CurrentState.Apps {
			if app.LAN != nil {
				lanApps++
				if _, ok := seen[appID]; !ok {
					return false
				}
			}
		}
	}
	return lanApps == len(roster)
}

func newGatewayRebindAttemptTerminalViewLegacy(receipt gatewayRebindFinalHandoverTerminalReceipt,
) (gatewayRebindAttemptTerminalView, error) {
	if !validGatewayRebindFinalHandoverTerminalReceiptValue(receipt) ||
		receipt.Disposition != gatewayRebindFinalHandoverTerminalCommit {
		return gatewayRebindAttemptTerminalView{}, errors.New("invalid generated ingress legacy rebind terminal")
	}
	legacy := receipt
	return gatewayRebindAttemptTerminalView{
		Format: gatewayRebindAttemptTerminalLegacyV1, Generation: receipt.Generation,
		OperationID: receipt.OperationID, Disposition: receipt.Disposition,
		ProtectedIntentDigest: receipt.ProtectedIntentDigest, SuccessorProfile: receipt.SuccessorProfile,
		SuccessorIdentity: receipt.SuccessorIdentity, Resources: receipt.Resources, Digest: receipt.Digest,
		LegacyReceipt: &legacy,
	}, nil
}

func cloneRouteState(value routeState) routeState {
	result := routeState{Version: value.Version, Active: cloneRoutes(value.Active)}
	if value.Pending != nil {
		pending := *value.Pending
		pending.Proposed.Endpoints = append([]generatedruntime.RouteEndpoint(nil), value.Pending.Proposed.Endpoints...)
		if value.Pending.Previous != nil {
			previous := *value.Pending.Previous
			previous.Endpoints = append([]generatedruntime.RouteEndpoint(nil), value.Pending.Previous.Endpoints...)
			pending.Previous = &previous
		}
		result.Pending = &pending
	}
	return result
}
