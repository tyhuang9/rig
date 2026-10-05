package appaccess

import (
	"math"
	"sort"
	"time"
)

const (
	GatewayRebindSpecVersionV2       = 2
	GatewayRebindRosterVersionV2     = 2
	GatewayRebindTransferVersionV1   = 1
	GatewayRebindTransitionVersionV1 = 1
	GatewayRebindTransitionPurpose   = "lan_gateway_rebind_transition"
)

const (
	GatewayRebindSuccessorReady    GatewayRebindState = "successor_ready"
	GatewayRebindDatabaseCommitted GatewayRebindState = "database_committed"
	GatewayRebindCommitted         GatewayRebindState = "committed"
	GatewayRebindRolledBack        GatewayRebindState = "rolled_back"
	GatewayRebindUnresolved        GatewayRebindState = "unresolved"
)

type GatewayRebindSourceKind string

const (
	GatewayRebindSourceGatewayUpgrade GatewayRebindSourceKind = "gateway_upgrade"
	GatewayRebindSourceGatewayRebind  GatewayRebindSourceKind = "gateway_rebind"
)

// GatewayCurrentLineageRef is the stable protected/SQL identity of the
// gateway that is currently authoritative. It deliberately excludes mutable
// route-state revisions: those are frozen only in a rebind admission source.
type GatewayCurrentLineageRef struct {
	Kind                    GatewayRebindSourceKind `json:"kind"`
	OperationID             string                  `json:"operationId"`
	ProfileRevisionID       string                  `json:"profileRevisionId"`
	ProfileRevisionNumber   int64                   `json:"profileRevisionNumber"`
	ProfileSpecDigest       string                  `json:"profileSpecDigest"`
	ProtectedGeneration     uint64                  `json:"protectedGeneration"`
	ProtectedIdentityDigest string                  `json:"protectedIdentityDigest"`
	ProtectedJournalDigest  string                  `json:"protectedJournalDigest,omitempty"`
	ProtectedIntentDigest   string                  `json:"protectedIntentDigest,omitempty"`
	TerminalReceiptDigest   string                  `json:"terminalReceiptDigest,omitempty"`
}

// GatewayRebindSourceRef freezes the mutable source state used for one
// admission. Later ordinary route writes do not alter this checkpoint or the
// stable lineage it names.
type GatewayRebindSourceRef struct {
	Lineage                     GatewayCurrentLineageRef `json:"lineage"`
	SourceStateVersion          uint64                   `json:"sourceStateVersion"`
	SourceStateRevision         uint64                   `json:"sourceStateRevision"`
	SourceStateDigest           string                   `json:"sourceStateDigest"`
	PredecessorCheckpointDigest string                   `json:"predecessorCheckpointDigest"`
}

// GatewayRebindSpecV2 is a new canonical approval format. The legacy
// GatewayRebindSpec remains unchanged so existing protected bytes can still be
// regenerated and validated exactly.
type GatewayRebindSpecV2 struct {
	Version                        int                    `json:"version"`
	OperationID                    string                 `json:"operationId"`
	Predecessor                    GatewayRebindSourceRef `json:"predecessor"`
	SuccessorProfileRevisionID     string                 `json:"successorProfileRevisionId"`
	SuccessorProfileRevisionNumber int64                  `json:"successorProfileRevisionNumber"`
	SuccessorProfileOperationID    string                 `json:"successorProfileOperationId"`
	SuccessorProfile               GatewayProfileSpec     `json:"successorProfile"`
	RosterVersion                  int                    `json:"rosterVersion"`
	RosterDigest                   string                 `json:"rosterDigest"`
	RosterCount                    int64                  `json:"rosterCount"`
}

// GatewayRebindRosterEntryV2 preserves the immutable raw grant binding and,
// for a repeated rebind, the exact prior transfer that made it effective on
// the current predecessor profile.
type GatewayRebindRosterEntryV2 struct {
	Version                     int             `json:"version"`
	OperationID                 string          `json:"operationId"`
	Ordinal                     int64           `json:"ordinal"`
	AppID                       string          `json:"appId"`
	AllocationID                string          `json:"allocationId"`
	Port                        uint16          `json:"port"`
	AllocationOwnerOperationID  string          `json:"allocationOwnerOperationId"`
	AllocationState             AllocationState `json:"allocationState"`
	AccessRevisionID            string          `json:"accessRevisionId"`
	AccessRevisionNumber        int64           `json:"accessRevisionNumber"`
	AccessSpecDigest            string          `json:"accessSpecDigest"`
	GrantAttemptID              string          `json:"grantAttemptId"`
	GrantStateSequence          int64           `json:"grantStateSequence"`
	GrantProtectedStateDigest   string          `json:"grantProtectedStateDigest"`
	SourceProfileRevisionID     string          `json:"sourceProfileRevisionId"`
	SourceProfileRevisionNumber int64           `json:"sourceProfileRevisionNumber"`
	SourceProfileSpecDigest     string          `json:"sourceProfileSpecDigest"`
	PredecessorTransferDigest   *string         `json:"predecessorTransferDigest"`
	ServingDeploymentID         string          `json:"servingDeploymentId"`
	ServingReleaseID            string          `json:"servingReleaseId"`
	ServingSlot                 string          `json:"servingSlot"`
	RouteGeneration             int64           `json:"routeGeneration"`
	EntryDigest                 string          `json:"-"`
}

// GatewayRebindAllocationTransfer maps an immutable raw allocation/grant to
// one successor profile without rewriting any source row.
type GatewayRebindAllocationTransfer struct {
	Version                        int     `json:"version"`
	OperationID                    string  `json:"operationId"`
	Ordinal                        int64   `json:"ordinal"`
	AppID                          string  `json:"appId"`
	AllocationID                   string  `json:"allocationId"`
	GrantAttemptID                 string  `json:"grantAttemptId"`
	SourceBindingDigest            string  `json:"sourceBindingDigest"`
	RosterEntryDigest              string  `json:"rosterEntryDigest"`
	SourceProfileRevisionID        string  `json:"sourceProfileRevisionId"`
	SourceProfileRevisionNumber    int64   `json:"sourceProfileRevisionNumber"`
	SourceProfileSpecDigest        string  `json:"sourceProfileSpecDigest"`
	PredecessorTransferDigest      *string `json:"predecessorTransferDigest"`
	SuccessorProfileRevisionID     string  `json:"successorProfileRevisionId"`
	SuccessorProfileRevisionNumber int64   `json:"successorProfileRevisionNumber"`
	SuccessorProfileSpecDigest     string  `json:"successorProfileSpecDigest"`
	TerminalReceiptDigest          string  `json:"terminalReceiptDigest"`
	TransferDigest                 string  `json:"-"`
}

type GatewayBindingRef struct {
	AppID            string
	AllocationID     string
	AccessRevisionID string
	GrantAttemptID   string
}

// GatewayBindingResolution keeps immutable source proof separate from the
// current effective projection used for current authorization and URLs.
type GatewayBindingResolution struct {
	RawAllocation          Allocation
	RawAccessRevision      AppAccessRevision
	RawGrant               AppAccessGrantClaim
	RawProfile             GatewayProfileRevision
	EffectiveProfile       GatewayProfileRevision
	CurrentGatewaySource   GatewayCurrentLineageRef
	TransferChain          []GatewayRebindAllocationTransfer
	TransferChainTipDigest string
	TerminalReceiptDigest  string
}

type GatewayRebindClaimV2 struct {
	Spec                          GatewayRebindSpecV2
	RequestDigest                 string
	RebindApproval                Approval
	RebindApprovedAt              time.Time
	ConfigureApproval             Approval
	ConfigureApprovedAt           time.Time
	SuccessorProfileRequestDigest string
	State                         GatewayRebindState
	StateSequence                 int64
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

// GatewayRebindClaimRecord is an explicit stored-format union. Exactly one
// claim pointer is set and SpecVersion selects its canonical validation path.
type GatewayRebindClaimRecord struct {
	SpecVersion int
	Legacy      *GatewayRebindClaim
	V2          *GatewayRebindClaimV2
}

type GatewayRebindEvent struct {
	OperationID string
	Sequence    int64
	State       GatewayRebindState
	CreatedAt   time.Time
}

type GatewayRebindTransitionCommand struct {
	OperationID            string
	Sequence               int64
	PreviousState          GatewayRebindState
	PreviousSequence       int64
	NextState              GatewayRebindState
	Purpose                string
	ProtectedRecordDigest  string
	TerminalReceiptDigest  string
	LocalAttestationDigest string
	TerminalDisposition    GatewayRebindTerminalDisposition
	CanonicalPayload       string
	CommandDigest          string
	CreatedAt              time.Time
}

type GatewayRebindHistoryEntry struct {
	Claim     GatewayRebindClaimRecord
	RosterV1  []GatewayRebindRosterEntry
	RosterV2  []GatewayRebindRosterEntryV2
	Events    []GatewayRebindEvent
	Commands  []GatewayRebindTransitionCommand
	Transfers []GatewayRebindAllocationTransfer
}

type GatewayRebindRecoverySnapshot struct {
	History                []GatewayRebindHistoryEntry
	Active                 *GatewayRebindHistoryEntry
	CurrentProfile         *GatewayProfileRevision
	CurrentSource          *GatewayCurrentLineageRef
	DatabaseCommittedEvent *GatewayRebindEvent
	CurrentTransfers       []GatewayRebindAllocationTransfer
	Phase                  GatewayRebindState
	DatabaseCommitObserved bool
	RollbackAllowed        bool
}

type GatewayRebindTerminalDisposition string

const (
	GatewayRebindDispositionNone   GatewayRebindTerminalDisposition = "none"
	GatewayRebindDispositionCommit GatewayRebindTerminalDisposition = "commit"
	GatewayRebindDispositionAbort  GatewayRebindTerminalDisposition = "abort"
)

// GatewayRebindTransitionProof is the purpose-bound cross-store command. The
// repository derives and validates its canonical payload before arming the
// one-use SQL capability.
type GatewayRebindTransitionProof struct {
	Version                           int                               `json:"version"`
	Purpose                           string                            `json:"purpose"`
	OperationID                       string                            `json:"operationId"`
	ClaimRequestDigest                string                            `json:"claimRequestDigest"`
	ClaimSpecDigest                   string                            `json:"claimSpecDigest"`
	ExpectedState                     GatewayRebindState                `json:"expectedState"`
	ExpectedSequence                  int64                             `json:"expectedSequence"`
	NextState                         GatewayRebindState                `json:"nextState"`
	ExpectedHeadRevisionID            string                            `json:"expectedHeadRevisionId"`
	ExpectedHeadRevisionNumber        int64                             `json:"expectedHeadRevisionNumber"`
	ExpectedHeadSpecDigest            string                            `json:"expectedHeadSpecDigest"`
	ProtectedGeneration               uint64                            `json:"protectedGeneration"`
	ProtectedPhase                    string                            `json:"protectedPhase"`
	ProtectedRecordSequence           uint64                            `json:"protectedRecordSequence"`
	ProtectedRecordDigest             string                            `json:"protectedRecordDigest"`
	TerminalReceiptDigest             string                            `json:"terminalReceiptDigest,omitempty"`
	TerminalDisposition               GatewayRebindTerminalDisposition  `json:"terminalDisposition"`
	PredecessorCheckpointDigest       string                            `json:"predecessorCheckpointDigest"`
	SourceStateVersion                uint64                            `json:"sourceStateVersion"`
	SourceStateRevision               uint64                            `json:"sourceStateRevision"`
	SourceStateDigest                 string                            `json:"sourceStateDigest"`
	SuccessorOperationalStateVersion  uint64                            `json:"successorOperationalStateVersion"`
	SuccessorOperationalStateRevision uint64                            `json:"successorOperationalStateRevision"`
	SuccessorOperationalStateDigest   string                            `json:"successorOperationalStateDigest,omitempty"`
	TransferManifestDigest            string                            `json:"transferManifestDigest,omitempty"`
	Transfers                         []GatewayRebindAllocationTransfer `json:"transfers"`
	LocalAttestationDigest            string                            `json:"localAttestationDigest,omitempty"`
}

func GatewayRebindSpecV2Digest(spec GatewayRebindSpecV2) (string, error) {
	canonicalSuccessor, err := canonicalGatewaySpec(spec.SuccessorProfile)
	if err != nil || spec.Version != GatewayRebindSpecVersionV2 || !validUUID(spec.OperationID) ||
		!validGatewayRebindSourceRef(spec.Predecessor) ||
		!validUUID(spec.SuccessorProfileRevisionID) ||
		spec.SuccessorProfileRevisionNumber != spec.Predecessor.Lineage.ProfileRevisionNumber+1 ||
		!validUUID(spec.SuccessorProfileOperationID) ||
		spec.SuccessorProfileRevisionID == spec.Predecessor.Lineage.ProfileRevisionID ||
		spec.SuccessorProfileOperationID == spec.OperationID ||
		spec.RosterVersion != GatewayRebindRosterVersionV2 || !validDigest(spec.RosterDigest) || spec.RosterCount < 0 {
		return "", ErrInvalidInput
	}
	spec.SuccessorProfile = canonicalSuccessor
	return digestJSON(struct {
		Version int                 `json:"version"`
		Action  ApprovalAction      `json:"action"`
		Spec    GatewayRebindSpecV2 `json:"spec"`
	}{Version: GatewayRebindSpecVersionV2, Action: ActionRebindGateway, Spec: spec})
}

func GatewayRebindRosterEntryV2Digest(entry GatewayRebindRosterEntryV2) (string, error) {
	entry.EntryDigest = ""
	if entry.Version != GatewayRebindRosterVersionV2 || !validUUID(entry.OperationID) || entry.Ordinal <= 0 ||
		!validUUID(entry.AppID) || !validUUID(entry.AllocationID) ||
		entry.Port < GatewayPortStart || entry.Port > GatewayPortEnd ||
		!validUUID(entry.AllocationOwnerOperationID) || entry.AllocationState != AllocationActive ||
		!validUUID(entry.AccessRevisionID) || entry.AccessRevisionNumber <= 0 || !validDigest(entry.AccessSpecDigest) ||
		!validUUID(entry.GrantAttemptID) || entry.GrantStateSequence <= 0 || !validDigest(entry.GrantProtectedStateDigest) ||
		!validUUID(entry.SourceProfileRevisionID) || entry.SourceProfileRevisionNumber <= 0 || !validDigest(entry.SourceProfileSpecDigest) ||
		!validOptionalDigest(entry.PredecessorTransferDigest) ||
		!validUUID(entry.ServingDeploymentID) || !validUUID(entry.ServingReleaseID) ||
		(entry.ServingSlot != "blue" && entry.ServingSlot != "green") || entry.RouteGeneration <= 0 {
		return "", ErrInvalidInput
	}
	return digestJSON(struct {
		Version int                        `json:"version"`
		Action  ApprovalAction             `json:"action"`
		Entry   GatewayRebindRosterEntryV2 `json:"entry"`
	}{Version: GatewayRebindRosterVersionV2, Action: ActionRebindGateway, Entry: entry})
}

func GatewayRebindRosterV2Digest(entries []GatewayRebindRosterEntryV2) (string, error) {
	ordered := append([]GatewayRebindRosterEntryV2(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	seenApps := make(map[string]struct{}, len(ordered))
	seenAllocations := make(map[string]struct{}, len(ordered))
	seenPorts := make(map[uint16]struct{}, len(ordered))
	operationID := ""
	if len(ordered) > 0 {
		operationID = ordered[0].OperationID
	}
	for index := range ordered {
		if ordered[index].Ordinal != int64(index+1) || ordered[index].OperationID != operationID {
			return "", ErrInvalidInput
		}
		digest, err := GatewayRebindRosterEntryV2Digest(ordered[index])
		if err != nil || (ordered[index].EntryDigest != "" && ordered[index].EntryDigest != digest) {
			return "", ErrInvalidInput
		}
		if _, duplicate := seenApps[ordered[index].AppID]; duplicate {
			return "", ErrInvalidInput
		}
		if _, duplicate := seenAllocations[ordered[index].AllocationID]; duplicate {
			return "", ErrInvalidInput
		}
		if _, duplicate := seenPorts[ordered[index].Port]; duplicate {
			return "", ErrInvalidInput
		}
		seenApps[ordered[index].AppID] = struct{}{}
		seenAllocations[ordered[index].AllocationID] = struct{}{}
		seenPorts[ordered[index].Port] = struct{}{}
		ordered[index].EntryDigest = digest
	}
	return digestJSON(struct {
		Version int                          `json:"version"`
		Action  ApprovalAction               `json:"action"`
		Entries []GatewayRebindRosterEntryV2 `json:"entries"`
	}{Version: GatewayRebindRosterVersionV2, Action: ActionRebindGateway, Entries: ordered})
}

func GatewayRebindAllocationTransferDigest(transfer GatewayRebindAllocationTransfer) (string, error) {
	transfer.TransferDigest = ""
	if transfer.Version != GatewayRebindTransferVersionV1 || !validUUID(transfer.OperationID) || transfer.Ordinal <= 0 ||
		!validUUID(transfer.AppID) || !validUUID(transfer.AllocationID) || !validUUID(transfer.GrantAttemptID) ||
		!validDigest(transfer.SourceBindingDigest) || !validDigest(transfer.RosterEntryDigest) ||
		!validUUID(transfer.SourceProfileRevisionID) || transfer.SourceProfileRevisionNumber <= 0 || !validDigest(transfer.SourceProfileSpecDigest) ||
		!validOptionalDigest(transfer.PredecessorTransferDigest) ||
		!validUUID(transfer.SuccessorProfileRevisionID) || transfer.SuccessorProfileRevisionNumber <= transfer.SourceProfileRevisionNumber ||
		!validDigest(transfer.SuccessorProfileSpecDigest) || !validDigest(transfer.TerminalReceiptDigest) {
		return "", ErrInvalidInput
	}
	return digestJSON(struct {
		Version  int                             `json:"version"`
		Action   ApprovalAction                  `json:"action"`
		Transfer GatewayRebindAllocationTransfer `json:"transfer"`
	}{Version: GatewayRebindTransferVersionV1, Action: ActionRebindGateway, Transfer: transfer})
}

func GatewayRebindTransferManifestDigest(transfers []GatewayRebindAllocationTransfer) (string, error) {
	ordered := append([]GatewayRebindAllocationTransfer(nil), transfers...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	operationID := ""
	if len(ordered) > 0 {
		operationID = ordered[0].OperationID
	}
	seenApps := make(map[string]struct{}, len(ordered))
	seenAllocations := make(map[string]struct{}, len(ordered))
	for index := range ordered {
		if ordered[index].Ordinal != int64(index+1) || ordered[index].OperationID != operationID {
			return "", ErrInvalidInput
		}
		digest, err := GatewayRebindAllocationTransferDigest(ordered[index])
		if err != nil || (ordered[index].TransferDigest != "" && ordered[index].TransferDigest != digest) {
			return "", ErrInvalidInput
		}
		if _, exists := seenApps[ordered[index].AppID]; exists {
			return "", ErrInvalidInput
		}
		if _, exists := seenAllocations[ordered[index].AllocationID]; exists {
			return "", ErrInvalidInput
		}
		seenApps[ordered[index].AppID] = struct{}{}
		seenAllocations[ordered[index].AllocationID] = struct{}{}
		ordered[index].TransferDigest = digest
	}
	return digestJSON(struct {
		Version   int                               `json:"version"`
		Action    ApprovalAction                    `json:"action"`
		Transfers []GatewayRebindAllocationTransfer `json:"transfers"`
	}{Version: GatewayRebindTransferVersionV1, Action: ActionRebindGateway, Transfers: ordered})
}

func validGatewayCurrentLineageRef(value GatewayCurrentLineageRef) bool {
	if !validUUID(value.OperationID) || !validUUID(value.ProfileRevisionID) || value.ProfileRevisionNumber <= 0 ||
		!validDigest(value.ProfileSpecDigest) || !validDigest(value.ProtectedIdentityDigest) {
		return false
	}
	switch value.Kind {
	case GatewayRebindSourceGatewayUpgrade:
		return validDigest(value.ProtectedJournalDigest) && value.ProtectedIntentDigest == "" &&
			value.TerminalReceiptDigest == ""
	case GatewayRebindSourceGatewayRebind:
		return value.ProtectedGeneration > 0 && value.ProtectedJournalDigest == "" &&
			validDigest(value.ProtectedIntentDigest) && validDigest(value.TerminalReceiptDigest)
	default:
		return false
	}
}

func validGatewayRebindSourceRef(value GatewayRebindSourceRef) bool {
	if !validGatewayCurrentLineageRef(value.Lineage) || value.Lineage.ProtectedGeneration == math.MaxUint64 ||
		!validDigest(value.SourceStateDigest) || !validDigest(value.PredecessorCheckpointDigest) {
		return false
	}
	switch value.Lineage.Kind {
	case GatewayRebindSourceGatewayUpgrade:
		return value.SourceStateVersion == 2 && value.SourceStateRevision == 0
	case GatewayRebindSourceGatewayRebind:
		return value.SourceStateVersion == 1 && value.SourceStateRevision > 0
	default:
		return false
	}
}

func validOptionalDigest(value *string) bool {
	return value == nil || validDigest(*value)
}
