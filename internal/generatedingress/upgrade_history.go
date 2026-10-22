package generatedingress

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const (
	gatewayRollbackRetirementPurpose  = "hostd/generated-ingress/migration/v1/rollback-retirement"
	gatewayRollbackRetirementFilename = "gateway-v1-to-v2-retirement.bundle"
	gatewayRollbackRetirementVersion  = 1
	maxGatewayRollbackReceiptBytes    = 4 << 10

	gatewayPreJournalAbortPurpose  = "hostd/generated-ingress/migration/v1/pre-journal-abort"
	gatewayPreJournalAbortFilename = "gateway-v1-to-v2-abort.bundle"
	gatewayPreJournalAbortVersion  = 1
	gatewayPreJournalAbortBoundary = "before_journal"
	maxGatewayAbortReceiptBytes    = 8 << 10

	gatewayV2GenerationDigits        = 20
	gatewayV2GenerationStatePrefix   = "routes-v2.g"
	gatewayV2GenerationJournalPrefix = "gateway-v1-to-v2.g"
	gatewayV2GenerationReceiptPrefix = "gateway-v1-to-v2-retirement.g"
	gatewayV2GenerationAbortPrefix   = "gateway-v1-to-v2-abort.g"

	// Protected generated-ingress bundles are read by secretfile, whose
	// persisted-format limit is 64 KiB. Keep snapshot reads bounded to the
	// same ceiling so history discovery cannot be used for unbounded I/O.
	maxGatewayHistoryArtifactBytes = 64 << 10
)

// gatewayRollbackRetirementReceipt is immutable evidence that a rolled-back
// generation was explicitly retired after the caller established exact v1
// service and absence of every generation-bound v2 resource. The storage
// layer binds the receipt to protected state; the future cleanup coordinator
// owns the live resource proof that authorizes creating it.
type gatewayRollbackRetirementReceipt struct {
	Version       int                       `json:"version"`
	Generation    uint64                    `json:"generation"`
	OperationID   string                    `json:"operationId"`
	TerminalPhase gatewayMigrationPhase     `json:"terminalPhase"`
	StateDigest   string                    `json:"stateDigest"`
	JournalDigest string                    `json:"journalDigest"`
	Source        gatewayMigrationSourceRef `json:"source"`
}

// gatewayPreJournalAbortReceipt is immutable evidence that an approved
// operation terminated before a migration journal or any v2 side effect was
// created. Optional state digests are present together only when the initial
// protected route state had already been installed.
type gatewayPreJournalAbortReceipt struct {
	Version            int                       `json:"version"`
	Generation         uint64                    `json:"generation"`
	OperationID        string                    `json:"operationId"`
	Outcome            GatewayV2UpgradeOutcome   `json:"outcome"`
	Boundary           string                    `json:"boundary"`
	Profile            gatewayProfileBinding     `json:"profile"`
	Action             gatewayUpgradeActionRef   `json:"action"`
	Source             gatewayMigrationSourceRef `json:"source"`
	V2IdentityDigest   string                    `json:"v2IdentityDigest"`
	InitialStateDigest string                    `json:"initialStateDigest,omitempty"`
	NetworkPlanDigest  string                    `json:"networkPlanDigest,omitempty"`
}

type gatewayHistoryArtifactKind uint8

const (
	gatewayHistoryState gatewayHistoryArtifactKind = iota + 1
	gatewayHistoryJournal
	gatewayHistoryReceipt
	gatewayHistoryAbort
	gatewayHistoryRebindIntent
	gatewayHistoryRebindProgress
	gatewayHistoryRebindTerminal
	gatewayHistoryRebindPredecessorCheckpoint
)

type gatewayHistoryArtifact struct {
	path string
}

type gatewayHistoryGeneration struct {
	generation  uint64
	operationID string
	state       gatewayHistoryArtifact
	journal     gatewayHistoryArtifact
	receipt     gatewayHistoryArtifact
	abort       gatewayHistoryArtifact
}

type gatewayRebindHistoryGeneration struct {
	generation  uint64
	operationID string
	checkpoint  gatewayHistoryArtifact
	intent      gatewayHistoryArtifact
	progress    map[uint64]gatewayHistoryArtifact
	terminal    gatewayHistoryArtifact
}

type gatewayHistorySnapshot struct {
	generations   map[uint64]gatewayHistoryGeneration
	rebindIntents map[uint64]gatewayRebindHistoryGeneration
	files         map[string]gatewayHistoryFileFingerprint
	reader        *generatedDomainProtectedReader
}

type gatewayHistoryFileFingerprint struct {
	info    os.FileInfo
	size    int64
	mode    os.FileMode
	modTime int64
	digest  [sha256.Size]byte
}

type gatewayUpgradeHistory struct {
	store       *gatewayUpgradeStateStore
	state       gatewayV2RouteState
	journal     gatewayMigrationJournal
	committed   bool
	generations []gatewayUpgradeGenerationSelection
}

// gatewayUpgradeGenerationSelection is the scanner's exact protected binding
// for one immutable generation. Existing marks a complete journal-backed or
// aborted generation. PartialState distinguishes the only accepted incomplete
// artifact, while a zero-valued new selection owns unused create-only paths.
type gatewayUpgradeGenerationSelection struct {
	Store        *gatewayUpgradeStateStore
	Generation   uint64
	State        gatewayV2RouteState
	Journal      gatewayMigrationJournal
	Existing     bool
	PartialState bool
	Retired      bool
	Aborted      bool
	operationID  string
}

// newGatewayUpgradeGenerationStore returns the create-only paths for a later
// operation. Generation zero intentionally retains the original fixed paths
// and purpose strings for compatibility with already-written installations.
func newGatewayUpgradeGenerationStore(dataRoot string, generation uint64, operationID string) (*gatewayUpgradeStateStore, error) {
	if generation == 0 || !validCanonicalUUID(operationID) {
		return nil, errors.New("invalid generated ingress upgrade generation")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	stateName, journalName, receiptName, abortName, statePurpose, journalPurpose, receiptPurpose, abortPurpose := gatewayUpgradeGenerationNames(generation, operationID)
	return &gatewayUpgradeStateStore{
		directory:      directory,
		generation:     generation,
		operationID:    operationID,
		v2Path:         filepath.Join(directory.root, stateName),
		v2Purpose:      statePurpose,
		journalPath:    filepath.Join(directory.root, journalName),
		journalPurpose: journalPurpose,
		receiptPath:    filepath.Join(directory.root, receiptName),
		receiptPurpose: receiptPurpose,
		abortPath:      filepath.Join(directory.root, abortName),
		abortPurpose:   abortPurpose,
	}, nil
}

// bindGatewayUpgradeStateStoreReadOnly describes one upgrade generation below
// an already verified read-only namespace. It performs no path creation or
// permission change and is shared by the startup classifier and the canonical
// history validator below.
func bindGatewayUpgradeStateStoreReadOnly(directory *stateStore, generation uint64,
	operationID string,
) (*gatewayUpgradeStateStore, error) {
	if directory == nil || directory.root == "" ||
		filepath.Dir(directory.path) != directory.root || (generation > 0 && !validCanonicalUUID(operationID)) ||
		(generation == 0 && operationID != "") {
		return nil, errors.New("invalid generated ingress read-only upgrade generation")
	}
	if generation == 0 {
		return &gatewayUpgradeStateStore{
			directory:      directory,
			v2Path:         filepath.Join(directory.root, v2RouteStateFilename),
			v2Purpose:      v2RouteStatePurpose,
			journalPath:    filepath.Join(directory.root, gatewayMigrationFilename),
			journalPurpose: gatewayMigrationPurpose,
			receiptPath:    filepath.Join(directory.root, gatewayRollbackRetirementFilename),
			receiptPurpose: gatewayRollbackRetirementPurpose,
			abortPath:      filepath.Join(directory.root, gatewayPreJournalAbortFilename),
			abortPurpose:   gatewayPreJournalAbortPurpose,
		}, nil
	}
	stateName, journalName, receiptName, abortName, statePurpose, journalPurpose, receiptPurpose, abortPurpose :=
		gatewayUpgradeGenerationNames(generation, operationID)
	return &gatewayUpgradeStateStore{
		directory: directory, generation: generation, operationID: operationID,
		v2Path: filepath.Join(directory.root, stateName), v2Purpose: statePurpose,
		journalPath: filepath.Join(directory.root, journalName), journalPurpose: journalPurpose,
		receiptPath: filepath.Join(directory.root, receiptName), receiptPurpose: receiptPurpose,
		abortPath: filepath.Join(directory.root, abortName), abortPurpose: abortPurpose,
	}, nil
}

func gatewayUpgradeGenerationNames(generation uint64, operationID string) (string, string, string, string, string, string, string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayV2GenerationStatePrefix + token + ".bundle",
		gatewayV2GenerationJournalPrefix + token + ".bundle",
		gatewayV2GenerationReceiptPrefix + token + ".bundle",
		gatewayV2GenerationAbortPrefix + token + ".bundle",
		v2RouteStatePurpose + "/" + scope,
		gatewayMigrationPurpose + "/" + scope,
		gatewayRollbackRetirementPurpose + "/" + scope,
		gatewayPreJournalAbortPurpose + "/" + scope
}

// installRollbackRetirementReceipt only persists the caller's already-proved
// retirement decision. The caller must hold the gateway writer lock and must
// have freshly proved exact v1 topology plus absence of the journal-bound v2
// resources. Exact replay is accepted; replacement is impossible.
func (s *gatewayUpgradeStateStore) installRollbackRetirementReceipt(state gatewayV2RouteState, journal gatewayMigrationJournal) (gatewayRollbackRetirementReceipt, error) {
	if s == nil || s.directory == nil || journal.Phase != gatewayPhaseRolledBack {
		return gatewayRollbackRetirementReceipt{}, errors.New("invalid generated ingress rollback retirement")
	}
	installedState, installedJournal, err := s.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(installedState, state) || !reflect.DeepEqual(installedJournal, journal) {
		return gatewayRollbackRetirementReceipt{}, errors.New("generated ingress rollback retirement is stale")
	}
	receipt, err := newGatewayRollbackRetirementReceipt(s.generation, state, journal)
	if err != nil {
		return gatewayRollbackRetirementReceipt{}, err
	}
	if err := s.writeExact(s.receiptPath, s.receiptPurpose, receipt, true, maxGatewayRollbackReceiptBytes); err != nil {
		return gatewayRollbackRetirementReceipt{}, err
	}
	loaded, err := s.loadRollbackRetirementReceipt(state, journal)
	if err != nil || !reflect.DeepEqual(loaded, receipt) {
		return gatewayRollbackRetirementReceipt{}, errors.New("generated ingress rollback retirement was not installed")
	}
	return loaded, nil
}

func (s *gatewayUpgradeStateStore) loadRollbackRetirementReceipt(state gatewayV2RouteState, journal gatewayMigrationJournal) (gatewayRollbackRetirementReceipt, error) {
	var receipt gatewayRollbackRetirementReceipt
	if s == nil || s.directory == nil || s.receiptPath == "" || s.receiptPurpose == "" ||
		s.readStrict(s.receiptPath, s.receiptPurpose, maxGatewayRollbackReceiptBytes, &receipt) != nil ||
		!validGatewayRollbackRetirementReceipt(receipt, s.generation, state, journal) {
		return gatewayRollbackRetirementReceipt{}, errors.New("generated ingress rollback retirement receipt is invalid")
	}
	return receipt, nil
}

func newGatewayRollbackRetirementReceipt(generation uint64, state gatewayV2RouteState, journal gatewayMigrationJournal) (gatewayRollbackRetirementReceipt, error) {
	stateDigest, stateErr := canonicalDigest(state)
	journalDigest, journalErr := canonicalDigest(journal)
	receipt := gatewayRollbackRetirementReceipt{
		Version: gatewayRollbackRetirementVersion, Generation: generation, OperationID: journal.OperationID,
		TerminalPhase: journal.Phase, StateDigest: stateDigest, JournalDigest: journalDigest, Source: journal.Source,
	}
	if stateErr != nil || journalErr != nil || !validGatewayRollbackRetirementReceipt(receipt, generation, state, journal) {
		return gatewayRollbackRetirementReceipt{}, errors.New("invalid generated ingress rollback retirement receipt")
	}
	return receipt, nil
}

func validGatewayRollbackRetirementReceipt(receipt gatewayRollbackRetirementReceipt, generation uint64, state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	if receipt.Version != gatewayRollbackRetirementVersion || receipt.Generation != generation ||
		receipt.OperationID != state.OperationID || receipt.OperationID != journal.OperationID ||
		receipt.TerminalPhase != gatewayPhaseRolledBack || journal.Phase != gatewayPhaseRolledBack ||
		!reflect.DeepEqual(receipt.Source, journal.Source) || !historicallyBoundGatewayUpgrade(state, journal) {
		return false
	}
	stateDigest, stateErr := canonicalDigest(state)
	journalDigest, journalErr := canonicalDigest(journal)
	return stateErr == nil && journalErr == nil && receipt.StateDigest == stateDigest && receipt.JournalDigest == journalDigest
}

// installPreJournalAbortReceipt persists only an already-proved no-op abort.
// The caller must hold the gateway writer lock and prove exact v1 service and
// absence of v2 side effects. This store independently rejects a journal,
// rollback receipt, or a state whose immutable preparation binding differs.
func (s *gatewayUpgradeStateStore) installPreJournalAbortReceipt(receipt gatewayPreJournalAbortReceipt) (gatewayPreJournalAbortReceipt, error) {
	if !s.validPreJournalAbortReceipt(receipt) {
		return gatewayPreJournalAbortReceipt{}, errors.New("invalid generated ingress pre-journal abort receipt")
	}
	if err := s.writeExact(s.abortPath, s.abortPurpose, receipt, true, maxGatewayAbortReceiptBytes); err != nil {
		return gatewayPreJournalAbortReceipt{}, err
	}
	loaded, err := s.loadPreJournalAbortReceipt()
	if err != nil || !reflect.DeepEqual(loaded, receipt) {
		return gatewayPreJournalAbortReceipt{}, errors.New("generated ingress pre-journal abort receipt was not installed")
	}
	return loaded, nil
}

func (s *gatewayUpgradeStateStore) loadPreJournalAbortReceipt() (gatewayPreJournalAbortReceipt, error) {
	var receipt gatewayPreJournalAbortReceipt
	if s == nil || s.directory == nil || s.abortPath == "" || s.abortPurpose == "" ||
		s.readStrict(s.abortPath, s.abortPurpose, maxGatewayAbortReceiptBytes, &receipt) != nil ||
		!s.validPreJournalAbortReceipt(receipt) {
		return gatewayPreJournalAbortReceipt{}, errors.New("generated ingress pre-journal abort receipt is invalid")
	}
	return receipt, nil
}

func (s *gatewayUpgradeStateStore) validPreJournalAbortReceipt(receipt gatewayPreJournalAbortReceipt) bool {
	if s == nil || s.directory == nil || receipt.Version != gatewayPreJournalAbortVersion || receipt.Generation != s.generation ||
		!validCanonicalUUID(receipt.OperationID) || (s.operationID != "" && receipt.OperationID != s.operationID) ||
		receipt.Outcome != GatewayV2UpgradeRolledBack || receipt.Boundary != gatewayPreJournalAbortBoundary ||
		!validGatewayProfileBinding(receipt.Profile) || receipt.Action.Name != gatewayUpgradeActionName ||
		!validCanonicalUUID(receipt.Action.ApprovedBy) || receipt.Source.Format != stateVersion ||
		!validSHA256(receipt.Source.StateDigest) || receipt.Source.IdentityVersion != gatewayV1IdentityVersion ||
		!validSHA256(receipt.Source.IdentityDigest) || receipt.Source.LocalHostPort == 0 {
		return false
	}
	actionDigest, err := gatewayUpgradeActionDigest(receipt.Profile, gatewayV2IdentityVersion)
	if err != nil || receipt.Action.Digest != actionDigest {
		return false
	}
	identity, err := newGatewayV2Identity(receipt.OperationID)
	if err != nil || receipt.V2IdentityDigest != identity.Digest {
		return false
	}
	journalExists, err := gatewayUpgradeArtifactExists(s.journalPath)
	if err != nil || journalExists {
		return false
	}
	retirementExists, err := gatewayUpgradeArtifactExists(s.receiptPath)
	if err != nil || retirementExists {
		return false
	}
	stateExists, err := gatewayUpgradeArtifactExists(s.v2Path)
	if err != nil {
		return false
	}
	if !stateExists {
		return receipt.InitialStateDigest == "" && receipt.NetworkPlanDigest == ""
	}
	if !validSHA256(receipt.InitialStateDigest) || !validSHA256(receipt.NetworkPlanDigest) {
		return false
	}
	state, err := s.loadV2State()
	if err != nil || !validPreJournalAbortInitialState(state) ||
		state.OperationID != receipt.OperationID || state.SourceV1StateDigest != receipt.Source.StateDigest ||
		!reflect.DeepEqual(state.Profile, receipt.Profile) || state.UpgradeAction != receipt.Action ||
		!reflect.DeepEqual(state.Identity, identity) {
		return false
	}
	stateDigest, stateErr := canonicalDigest(state)
	planDigest, planErr := gatewayV2PlanDigest(state)
	return stateErr == nil && planErr == nil && receipt.InitialStateDigest == stateDigest && receipt.NetworkPlanDigest == planDigest
}

func validPreJournalAbortInitialState(state gatewayV2RouteState) bool {
	if !validGatewayV2RouteState(state) || state.Pending != nil {
		return false
	}
	for _, app := range state.Apps {
		if app.LAN != nil {
			return false
		}
	}
	return true
}

func gatewayUpgradeArtifactExists(path string) (bool, error) {
	if path == "" {
		return false, errors.New("generated ingress upgrade artifact path is missing")
	}
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// loadHistoricalBoundUpgrade validates an immutable generation without reading
// the mutable current v1 route file. That omission is deliberate: after a
// valid rollback retirement, normal v1 route switches must not invalidate old
// evidence. Active committed v2 selection performs the stronger current-v1
// check through loadBoundUpgrade below.
func (s *gatewayUpgradeStateStore) loadHistoricalBoundUpgrade(operationID string) (gatewayV2RouteState, gatewayMigrationJournal, error) {
	if s == nil || !validCanonicalUUID(operationID) || (s.operationID != "" && s.operationID != operationID) {
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
	if state.OperationID != operationID || journal.OperationID != operationID || !historicallyBoundGatewayUpgrade(state, journal) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress upgrade binding is invalid")
	}
	return state, journal, nil
}

func historicallyBoundGatewayUpgrade(state gatewayV2RouteState, journal gatewayMigrationJournal) bool {
	if !validGatewayV2RouteState(state) || !validGatewayMigrationJournal(journal) ||
		state.OperationID != journal.OperationID || state.SourceV1StateDigest != journal.Source.StateDigest {
		return false
	}
	if journal.Phase == gatewayPhaseCommitted {
		return journalMatchesV2Plan(journal, state)
	}
	return journalMatchesInitialV2State(journal, state)
}

// scanGatewayUpgradeHistoryLocked chooses the durable owner while the caller
// holds the gateway writer lock. Every observed generation must be complete,
// contiguous, uniquely numbered, purpose-bound, and terminal. Only a latest
// committed generation owns v2; otherwise all generations must have valid
// rollback-retirement or pre-journal abort receipts before v1 can resume.
func (m *Manager) scanGatewayUpgradeHistoryLocked() (gatewayUpgradeHistory, error) {
	return m.scanGatewayUpgradeHistoryLockedMode(false, "")
}

// selectGatewayUpgradeGenerationLocked resolves only the latest existing
// operation. It accepts an unretired rolled-back tail so the cleanup
// coordinator can prove and retire it; earlier operations cannot be reopened
// after a later generation exists.
func (m *Manager) selectGatewayUpgradeGenerationLocked(operationID string) (gatewayUpgradeGenerationSelection, error) {
	if !validCanonicalUUID(operationID) {
		return gatewayUpgradeGenerationSelection{}, errors.New("invalid generated ingress operation")
	}
	history, err := m.scanGatewayUpgradeHistoryLockedMode(true, "")
	if err != nil || len(history.generations) == 0 {
		return gatewayUpgradeGenerationSelection{}, errors.New("generated ingress upgrade operation is not current")
	}
	latest := history.generations[len(history.generations)-1]
	if latest.Aborted || latest.operationID != operationID {
		return gatewayUpgradeGenerationSelection{}, errors.New("generated ingress upgrade operation is not current")
	}
	return latest, nil
}

// resolveGatewayUpgradeGenerationLocked returns an exact replay selection for
// the latest operation, or create-only paths for the next generation when all
// prior generations are validly retired. Reusing an operation ID from older
// history and advancing past committed or unretired history both fail closed.
func (m *Manager) resolveGatewayUpgradeGenerationLocked(operationID string) (gatewayUpgradeGenerationSelection, error) {
	if !validCanonicalUUID(operationID) {
		return gatewayUpgradeGenerationSelection{}, errors.New("invalid generated ingress operation")
	}
	history, err := m.scanGatewayUpgradeHistoryLockedMode(true, operationID)
	if err != nil {
		return gatewayUpgradeGenerationSelection{}, err
	}
	if len(history.generations) == 0 {
		store, storeErr := newGatewayUpgradeStateStore(m.options.DataRoot)
		return gatewayUpgradeGenerationSelection{Store: store, Generation: 0}, storeErr
	}
	latest := history.generations[len(history.generations)-1]
	if latest.operationID == operationID {
		return latest, nil
	}
	for _, generation := range history.generations[:len(history.generations)-1] {
		if generation.operationID == operationID {
			return gatewayUpgradeGenerationSelection{}, errors.New("generated ingress upgrade operation belongs to historical generation")
		}
	}
	terminal := latest.Aborted || (latest.Retired && latest.Journal.Phase == gatewayPhaseRolledBack)
	if !terminal || latest.Generation == ^uint64(0) {
		return gatewayUpgradeGenerationSelection{}, errors.New("generated ingress upgrade history does not permit a new generation")
	}
	next := latest.Generation + 1
	store, err := newGatewayUpgradeGenerationStore(m.options.DataRoot, next, operationID)
	if err != nil {
		return gatewayUpgradeGenerationSelection{}, err
	}
	return gatewayUpgradeGenerationSelection{Store: store, Generation: next}, nil
}

func (m *Manager) scanGatewayUpgradeHistoryLockedMode(allowCurrentTail bool, partialOperationID string) (gatewayUpgradeHistory, error) {
	result, _, err := m.scanGatewayUpgradeHistoryLockedModeWithSnapshot(allowCurrentTail, partialOperationID, false)
	return result, err
}

// scanGatewayUpgradeHistoryLockedModeRebindAware is available only to protected
// rebind readers, proof-bound prepared startup, and the runtime ownership path
// after it freshly proves a complete native-ABORT census. Ordinary startup uses
// the legacy wrapper above and fails closed when any rebind artifact exists.
func (m *Manager) scanGatewayUpgradeHistoryLockedModeRebindAware(allowCurrentTail bool, partialOperationID string) (gatewayUpgradeHistory, gatewayHistorySnapshot, error) {
	return m.scanGatewayUpgradeHistoryLockedModeWithSnapshotPolicy(allowCurrentTail, partialOperationID, true, false)
}

func (m *Manager) scanGatewayUpgradeHistoryLockedModeCurrentV2Aware(allowCurrentTail bool,
	partialOperationID string,
) (gatewayUpgradeHistory, gatewayHistorySnapshot, error) {
	return m.scanGatewayUpgradeHistoryLockedModeWithSnapshotPolicy(allowCurrentTail, partialOperationID, true, true)
}

func (m *Manager) scanGatewayUpgradeHistoryLockedModeWithSnapshot(allowCurrentTail bool, partialOperationID string,
	allowRebind bool,
) (gatewayUpgradeHistory, gatewayHistorySnapshot, error) {
	return m.scanGatewayUpgradeHistoryLockedModeWithSnapshotPolicy(allowCurrentTail, partialOperationID, allowRebind, false)
}

func (m *Manager) scanGatewayUpgradeHistoryLockedModeWithSnapshotPolicy(allowCurrentTail bool,
	partialOperationID string, allowRebind, allowCurrentV2 bool,
) (gatewayUpgradeHistory, gatewayHistorySnapshot, error) {
	if m == nil || m.store == nil {
		return gatewayUpgradeHistory{}, gatewayHistorySnapshot{}, errors.New("generated ingress history store is unavailable")
	}
	before, err := readGatewayHistorySnapshotPolicy(m.store, allowRebind, allowCurrentV2)
	if err != nil {
		return gatewayUpgradeHistory{}, gatewayHistorySnapshot{}, err
	}
	result, err := m.scanGatewayUpgradeHistorySnapshot(before, allowCurrentTail, partialOperationID)
	if err != nil {
		return gatewayUpgradeHistory{}, gatewayHistorySnapshot{}, err
	}
	after, err := readGatewayHistorySnapshotPolicy(m.store, allowRebind, allowCurrentV2)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		return gatewayUpgradeHistory{}, gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history changed during inspection")
	}
	return result, before, nil
}

func (m *Manager) scanGatewayUpgradeHistorySnapshot(before gatewayHistorySnapshot,
	allowCurrentTail bool, partialOperationID string,
) (gatewayUpgradeHistory, error) {
	if m == nil || m.store == nil {
		return gatewayUpgradeHistory{}, errors.New("generated ingress history store is unavailable")
	}
	return scanGatewayUpgradeHistorySnapshotReadOnly(m.store, before, allowCurrentTail, partialOperationID)
}

func loadGeneratedDomainUpgradeSource(store *stateStore, reader *generatedDomainProtectedReader) (routeState, error) {
	if reader == nil {
		return store.load()
	}
	return readGeneratedDomainRouteState(reader, store)
}

func loadGeneratedDomainUpgradeState(store *gatewayUpgradeStateStore,
	reader *generatedDomainProtectedReader,
) (gatewayV2RouteState, error) {
	if reader == nil {
		return store.loadV2State()
	}
	var value gatewayV2RouteState
	if store == nil || store.directory == nil {
		return value, errors.New("generated ingress v2 state store is invalid")
	}
	if _, err := reader.readStrict(store.v2Path, store.v2Purpose, maxV2RouteStateBytes, &value); err != nil ||
		!validGatewayV2RouteState(value) {
		return gatewayV2RouteState{}, errors.New("generated ingress v2 state is invalid")
	}
	return cloneGatewayV2RouteState(value), nil
}

func loadGeneratedDomainMigrationJournal(store *gatewayUpgradeStateStore,
	reader *generatedDomainProtectedReader,
) (gatewayMigrationJournal, error) {
	if reader == nil {
		return store.loadMigrationJournal()
	}
	var value gatewayMigrationJournal
	if store == nil || store.directory == nil {
		return value, errors.New("generated ingress migration journal store is invalid")
	}
	if _, err := reader.readStrict(store.journalPath, store.journalPurpose, maxGatewayMigrationBytes, &value); err != nil ||
		!validGatewayMigrationJournal(value) {
		return gatewayMigrationJournal{}, errors.New("generated ingress migration journal is invalid")
	}
	return value, nil
}

func loadGeneratedDomainHistoricalBoundUpgrade(store *gatewayUpgradeStateStore,
	reader *generatedDomainProtectedReader, operationID string,
) (gatewayV2RouteState, gatewayMigrationJournal, error) {
	if reader == nil {
		return store.loadHistoricalBoundUpgrade(operationID)
	}
	if store == nil || !validCanonicalUUID(operationID) || (store.operationID != "" && store.operationID != operationID) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("invalid generated ingress operation")
	}
	state, err := loadGeneratedDomainUpgradeState(store, reader)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	journal, err := loadGeneratedDomainMigrationJournal(store, reader)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	if state.OperationID != operationID || journal.OperationID != operationID ||
		!historicallyBoundGatewayUpgrade(state, journal) {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress upgrade binding is invalid")
	}
	return state, journal, nil
}

func loadGeneratedDomainBoundUpgrade(store *gatewayUpgradeStateStore, reader *generatedDomainProtectedReader,
	operationID string,
) (gatewayV2RouteState, gatewayMigrationJournal, error) {
	if reader == nil {
		return store.loadBoundUpgrade(operationID)
	}
	state, journal, err := loadGeneratedDomainHistoricalBoundUpgrade(store, reader, operationID)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	bound := journalMatchesInitialV2State(journal, state)
	if journal.Phase == gatewayPhaseCommitted {
		bound = journalMatchesV2Plan(journal, state)
	}
	if !bound {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress upgrade binding is invalid")
	}
	source, err := loadGeneratedDomainUpgradeSource(store.directory, reader)
	if err != nil {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, err
	}
	sourceDigest, err := canonicalDigest(source)
	if err != nil || sourceDigest != journal.Source.StateDigest || sourceDigest != state.SourceV1StateDigest {
		return gatewayV2RouteState{}, gatewayMigrationJournal{}, errors.New("generated ingress v1 source state changed")
	}
	return state, journal, nil
}

func loadGeneratedDomainRollbackRetirementReceipt(store *gatewayUpgradeStateStore,
	reader *generatedDomainProtectedReader, state gatewayV2RouteState, journal gatewayMigrationJournal,
) (gatewayRollbackRetirementReceipt, error) {
	if reader == nil {
		return store.loadRollbackRetirementReceipt(state, journal)
	}
	var value gatewayRollbackRetirementReceipt
	if store == nil || store.directory == nil || store.receiptPath == "" || store.receiptPurpose == "" {
		return value, errors.New("generated ingress rollback retirement receipt store is invalid")
	}
	if _, err := reader.readStrict(store.receiptPath, store.receiptPurpose, maxGatewayRollbackReceiptBytes, &value); err != nil ||
		!validGatewayRollbackRetirementReceipt(value, store.generation, state, journal) {
		return gatewayRollbackRetirementReceipt{}, errors.New("generated ingress rollback retirement receipt is invalid")
	}
	return value, nil
}

func loadGeneratedDomainPreJournalAbortReceipt(store *gatewayUpgradeStateStore,
	reader *generatedDomainProtectedReader,
) (gatewayPreJournalAbortReceipt, error) {
	if reader == nil {
		return store.loadPreJournalAbortReceipt()
	}
	var value gatewayPreJournalAbortReceipt
	if store == nil || store.directory == nil || store.abortPath == "" || store.abortPurpose == "" {
		return value, errors.New("generated ingress pre-journal abort receipt store is invalid")
	}
	if _, err := reader.readStrict(store.abortPath, store.abortPurpose, maxGatewayAbortReceiptBytes, &value); err != nil ||
		!store.validPreJournalAbortReceipt(value) {
		return gatewayPreJournalAbortReceipt{}, errors.New("generated ingress pre-journal abort receipt is invalid")
	}
	return value, nil
}

func scanGatewayUpgradeHistorySnapshotReadOnly(source *stateStore, before gatewayHistorySnapshot,
	allowCurrentTail bool, partialOperationID string,
) (gatewayUpgradeHistory, error) {
	if source == nil {
		return gatewayUpgradeHistory{}, errors.New("generated ingress history store is unavailable")
	}
	generations := make([]uint64, 0, len(before.generations))
	for generation := range before.generations {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	for index, generation := range generations {
		if generation != uint64(index) {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history has a generation gap")
		}
	}

	var result gatewayUpgradeHistory
	seenOperations := make(map[string]struct{}, len(generations))
	for index, generation := range generations {
		artifacts := before.generations[generation]
		if generation > 0 && artifacts.operationID == "" {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history operation is missing")
		}
		store, err := bindGatewayUpgradeStateStoreReadOnly(source, generation, artifacts.operationID)
		if err != nil || (artifacts.state.path != "" && store.v2Path != artifacts.state.path) ||
			(artifacts.journal.path != "" && store.journalPath != artifacts.journal.path) ||
			(artifacts.receipt.path != "" && store.receiptPath != artifacts.receipt.path) ||
			(artifacts.abort.path != "" && store.abortPath != artifacts.abort.path) {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history path is invalid")
		}
		if artifacts.abort.path != "" {
			if artifacts.journal.path != "" || artifacts.receipt.path != "" {
				return gatewayUpgradeHistory{}, errors.New("generated ingress pre-journal abort conflicts with migration history")
			}
			receipt, loadErr := loadGeneratedDomainPreJournalAbortReceipt(store, before.reader)
			if loadErr != nil || (artifacts.operationID != "" && receipt.OperationID != artifacts.operationID) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress pre-journal abort receipt is invalid")
			}
			var state gatewayV2RouteState
			if artifacts.state.path != "" {
				state, loadErr = loadGeneratedDomainUpgradeState(store, before.reader)
				if loadErr != nil {
					return gatewayUpgradeHistory{}, loadErr
				}
			}
			if _, duplicate := seenOperations[receipt.OperationID]; duplicate {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade operation is duplicated across generations")
			}
			seenOperations[receipt.OperationID] = struct{}{}
			result.generations = append(result.generations, gatewayUpgradeGenerationSelection{
				Store: store, Generation: generation, State: state, Existing: true, Aborted: true, operationID: receipt.OperationID,
			})
			continue
		}
		if artifacts.state.path == "" {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history is partial")
		}
		if artifacts.journal.path == "" {
			if partialOperationID == "" || index != len(generations)-1 || artifacts.receipt.path != "" {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history is partial")
			}
			state, sourceErr := loadGeneratedDomainUpgradeState(store, before.reader)
			if sourceErr != nil || state.OperationID != partialOperationID ||
				(store.operationID != "" && store.operationID != state.OperationID) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress partial state operation is invalid")
			}
			sourceState, sourceErr := loadGeneratedDomainUpgradeSource(source, before.reader)
			if sourceErr != nil || !gatewayV2InitialStateMatchesSource(state, sourceState) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress partial state source is stale")
			}
			if _, duplicate := seenOperations[state.OperationID]; duplicate {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade operation is duplicated across generations")
			}
			seenOperations[state.OperationID] = struct{}{}
			result.generations = append(result.generations, gatewayUpgradeGenerationSelection{
				Store: store, Generation: generation, State: state, PartialState: true, operationID: state.OperationID,
			})
			continue
		}
		operationID := artifacts.operationID
		if generation == 0 {
			journal, loadErr := loadGeneratedDomainMigrationJournal(store, before.reader)
			if loadErr != nil {
				return gatewayUpgradeHistory{}, loadErr
			}
			operationID = journal.OperationID
		}
		state, journal, loadErr := loadGeneratedDomainHistoricalBoundUpgrade(store, before.reader, operationID)
		if loadErr != nil {
			return gatewayUpgradeHistory{}, loadErr
		}
		if _, duplicate := seenOperations[journal.OperationID]; duplicate {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade operation is duplicated across generations")
		}
		seenOperations[journal.OperationID] = struct{}{}
		if journal.Phase == gatewayPhaseCommitted {
			if index != len(generations)-1 || artifacts.receipt.path != "" {
				return gatewayUpgradeHistory{}, errors.New("generated ingress committed generation is not the active tail")
			}
			activeState, activeJournal, activeErr := loadGeneratedDomainBoundUpgrade(store, before.reader, operationID)
			if activeErr != nil || !reflect.DeepEqual(activeState, state) || !reflect.DeepEqual(activeJournal, journal) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress committed generation source is stale")
			}
			selection := gatewayUpgradeGenerationSelection{
				Store: store, Generation: generation, State: state, Journal: journal, Existing: true, operationID: journal.OperationID,
			}
			result.store, result.state, result.journal, result.committed = store, state, journal, true
			result.generations = append(result.generations, selection)
			continue
		}
		selection := gatewayUpgradeGenerationSelection{
			Store: store, Generation: generation, State: state, Journal: journal, Existing: true, operationID: journal.OperationID,
		}
		if journal.Phase != gatewayPhaseRolledBack {
			if artifacts.receipt.path != "" || !allowCurrentTail || index != len(generations)-1 {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history is unresolved")
			}
			// The coordinator may resume only this exact current operation.
			// Strict ownership scans never enable this mode, so a nonterminal
			// generation can neither release v1 nor allocate a later generation.
			result.generations = append(result.generations, selection)
			continue
		}
		if artifacts.receipt.path == "" {
			if !allowCurrentTail || index != len(generations)-1 {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history is unresolved")
			}
			result.generations = append(result.generations, selection)
			continue
		}
		if _, loadErr := loadGeneratedDomainRollbackRetirementReceipt(store, before.reader, state, journal); loadErr != nil {
			return gatewayUpgradeHistory{}, loadErr
		}
		selection.Retired = true
		result.generations = append(result.generations, selection)
	}

	return result, nil
}

func readGatewayHistorySnapshot(store *stateStore) (gatewayHistorySnapshot, error) {
	return readGatewayHistorySnapshotMode(store, false)
}

func readGatewayHistorySnapshotMode(store *stateStore, allowRebind bool) (gatewayHistorySnapshot, error) {
	return readGatewayHistorySnapshotPolicy(store, allowRebind, false)
}

func readGatewayHistorySnapshotPolicy(store *stateStore, allowRebind, allowCurrentV2 bool) (gatewayHistorySnapshot, error) {
	if store == nil {
		return gatewayHistorySnapshot{}, errors.New("generated ingress history store is unavailable")
	}
	before, err := store.directoryIdentity()
	if err != nil {
		return gatewayHistorySnapshot{}, err
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return gatewayHistorySnapshot{}, err
	}
	return readGatewayHistorySnapshotPolicyEntries(store, before, entries, allowRebind, allowCurrentV2, 0)
}

func readGatewayHistorySnapshotPolicyBounded(store *stateStore, allowRebind, allowCurrentV2 bool) (
	gatewayHistorySnapshot, error,
) {
	if store == nil {
		return gatewayHistorySnapshot{}, errors.New("generated ingress history store is unavailable")
	}
	before, err := store.directoryIdentity()
	if err != nil {
		return gatewayHistorySnapshot{}, err
	}
	entries, err := readGeneratedDomainProtectedDirectoryBounded(store.root)
	if err != nil {
		return gatewayHistorySnapshot{}, err
	}
	return readGatewayHistorySnapshotPolicyEntries(store, before, entries, allowRebind, allowCurrentV2,
		generatedDomainStartupMaximumBytes)
}

type gatewayHistorySnapshotScanEntry struct {
	name        string
	path        string
	generation  uint64
	operationID string
	sequence    uint64
	kind        gatewayHistoryArtifactKind
	currentV2   bool
	readerOnly  bool
	fingerprint gatewayHistoryFileFingerprint
}

func readGatewayHistorySnapshotPolicyEntries(store *stateStore, before os.FileInfo, entries []os.DirEntry,
	allowRebind, allowCurrentV2 bool, maximumBytes int64,
) (gatewayHistorySnapshot, error) {
	scanned := make([]gatewayHistorySnapshotScanEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if maximumBytes > 0 && name == stateFilename {
			path := filepath.Join(store.root, name)
			fingerprint, readErr := fingerprintGatewayHistoryArtifactBound(path, maxStateBytes)
			if readErr != nil || fingerprint.size > maximumBytes-total {
				return gatewayHistorySnapshot{}, errors.New("generated ingress history source exceeds its aggregate byte bound")
			}
			total += fingerprint.size
			scanned = append(scanned, gatewayHistorySnapshotScanEntry{
				name: name, path: path, readerOnly: true, fingerprint: fingerprint,
			})
			continue
		}
		if gatewayCurrentRouteV2NamespaceName(name) {
			if !allowCurrentV2 {
				return gatewayHistorySnapshot{}, errors.New("generated ingress current V2 state requires a format-aware scanner")
			}
			path := filepath.Join(store.root, name)
			fingerprint, readErr := fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
			if readErr != nil {
				return gatewayHistorySnapshot{}, readErr
			}
			if maximumBytes > 0 && fingerprint.size > maximumBytes-total {
				return gatewayHistorySnapshot{}, errors.New("generated ingress history exceeds its aggregate byte bound")
			}
			total += fingerprint.size
			scanned = append(scanned, gatewayHistorySnapshotScanEntry{
				name: name, path: path, currentV2: true, fingerprint: fingerprint,
			})
			continue
		}
		generation, operationID, sequence, kind, relevant, parseErr := parseGatewayHistoryArtifactName(name)
		if parseErr != nil {
			return gatewayHistorySnapshot{}, parseErr
		}
		if !relevant {
			continue
		}
		if (kind == gatewayHistoryRebindIntent || kind == gatewayHistoryRebindProgress ||
			kind == gatewayHistoryRebindTerminal || kind == gatewayHistoryRebindPredecessorCheckpoint) && !allowRebind {
			return gatewayHistorySnapshot{}, errors.New("generated ingress rebind history requires a rebind-aware scanner")
		}
		path := filepath.Join(store.root, name)
		if filepath.Dir(path) != store.root || filepath.Clean(path) != path {
			return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history path is invalid")
		}
		fingerprint, readErr := fingerprintGatewayHistoryArtifact(path)
		if readErr != nil {
			return gatewayHistorySnapshot{}, readErr
		}
		if maximumBytes > 0 && fingerprint.size > maximumBytes-total {
			return gatewayHistorySnapshot{}, errors.New("generated ingress history exceeds its aggregate byte bound")
		}
		total += fingerprint.size
		scanned = append(scanned, gatewayHistorySnapshotScanEntry{
			name: name, path: path, generation: generation, operationID: operationID,
			sequence: sequence, kind: kind, fingerprint: fingerprint,
		})
	}
	snapshot := gatewayHistorySnapshot{
		generations:   make(map[uint64]gatewayHistoryGeneration),
		rebindIntents: make(map[uint64]gatewayRebindHistoryGeneration),
		files:         make(map[string]gatewayHistoryFileFingerprint),
	}
	for _, entry := range scanned {
		if entry.readerOnly {
			continue
		}
		if entry.currentV2 {
			snapshot.files[entry.name] = entry.fingerprint
			continue
		}
		artifact := gatewayHistoryArtifact{path: entry.path}
		if entry.kind == gatewayHistoryRebindIntent || entry.kind == gatewayHistoryRebindProgress ||
			entry.kind == gatewayHistoryRebindTerminal || entry.kind == gatewayHistoryRebindPredecessorCheckpoint {
			artifacts := snapshot.rebindIntents[entry.generation]
			if (artifacts.operationID != "" && artifacts.operationID != entry.operationID) ||
				(entry.kind == gatewayHistoryRebindPredecessorCheckpoint && artifacts.checkpoint.path != "") ||
				(entry.kind == gatewayHistoryRebindIntent && artifacts.intent.path != "") ||
				(entry.kind == gatewayHistoryRebindTerminal && artifacts.terminal.path != "") {
				return gatewayHistorySnapshot{}, errors.New("generated ingress rebind generation has conflicting artifacts")
			}
			artifacts.generation, artifacts.operationID = entry.generation, entry.operationID
			if entry.kind == gatewayHistoryRebindPredecessorCheckpoint {
				artifacts.checkpoint = artifact
			} else if entry.kind == gatewayHistoryRebindIntent {
				artifacts.intent = artifact
			} else if entry.kind == gatewayHistoryRebindTerminal {
				artifacts.terminal = artifact
			} else {
				if artifacts.progress == nil {
					artifacts.progress = make(map[uint64]gatewayHistoryArtifact)
				}
				if _, duplicate := artifacts.progress[entry.sequence]; duplicate {
					return gatewayHistorySnapshot{}, errors.New("generated ingress rebind generation has duplicate progress")
				}
				artifacts.progress[entry.sequence] = artifact
			}
			snapshot.rebindIntents[entry.generation] = artifacts
			snapshot.files[entry.name] = entry.fingerprint
			continue
		}
		artifacts := snapshot.generations[entry.generation]
		artifacts.generation = entry.generation
		if artifacts.operationID != "" && entry.operationID != "" && artifacts.operationID != entry.operationID {
			return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade generation has duplicate operations")
		}
		if entry.operationID != "" {
			artifacts.operationID = entry.operationID
		}
		switch entry.kind {
		case gatewayHistoryState:
			if artifacts.state.path != "" {
				return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade generation has duplicate state")
			}
			artifacts.state = artifact
		case gatewayHistoryJournal:
			if artifacts.journal.path != "" {
				return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade generation has duplicate journal")
			}
			artifacts.journal = artifact
		case gatewayHistoryReceipt:
			if artifacts.receipt.path != "" {
				return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade generation has duplicate receipt")
			}
			artifacts.receipt = artifact
		case gatewayHistoryAbort:
			if artifacts.abort.path != "" {
				return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade generation has duplicate abort receipt")
			}
			artifacts.abort = artifact
		default:
			return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history artifact kind is invalid")
		}
		snapshot.generations[entry.generation] = artifacts
		snapshot.files[entry.name] = entry.fingerprint
	}
	if store.sameDirectory(before) != nil {
		return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history directory changed")
	}
	if maximumBytes > 0 {
		captured := make(map[string]gatewayHistoryFileFingerprint, len(scanned))
		for _, entry := range scanned {
			captured[entry.path] = entry.fingerprint
		}
		reader, readerErr := newGeneratedDomainProtectedReader(store, before, captured)
		if readerErr != nil {
			return gatewayHistorySnapshot{}, readerErr
		}
		snapshot.reader = reader
	}
	return snapshot, nil
}

// fingerprintGatewayHistoryArtifact reads a protected bundle through a
// verified regular-file handle. Path and handle identities are checked before
// and after the bounded read. The surrounding two-snapshot scan therefore
// rejects replacements and content changes that persist across its reads.
func fingerprintGatewayHistoryArtifact(path string) (gatewayHistoryFileFingerprint, error) {
	return fingerprintGatewayHistoryArtifactBound(path, maxGatewayHistoryArtifactBytes)
}

func fingerprintGatewayHistoryArtifactBound(path string, maximum int64) (gatewayHistoryFileFingerprint, error) {
	unsafe := func() (gatewayHistoryFileFingerprint, error) {
		return gatewayHistoryFileFingerprint{}, errors.New("generated ingress upgrade history artifact is unsafe")
	}
	before, err := os.Lstat(path)
	if maximum <= 0 || err != nil || !safeGatewayHistoryArtifact(path, before) || before.Size() <= 0 || before.Size() > maximum {
		return unsafe()
	}
	file, err := os.Open(path)
	if err != nil {
		return unsafe()
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !sameGatewayHistoryFileMetadata(before, opened) {
		return unsafe()
	}

	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || read <= 0 || read > maximum || read != opened.Size() {
		return unsafe()
	}
	afterHandle, err := file.Stat()
	if err != nil || !sameGatewayHistoryFileMetadata(opened, afterHandle) {
		return unsafe()
	}
	afterPath, err := os.Lstat(path)
	if err != nil || !safeGatewayHistoryArtifact(path, afterPath) || !sameGatewayHistoryFileMetadata(afterHandle, afterPath) {
		return unsafe()
	}

	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return gatewayHistoryFileFingerprint{
		info:    afterPath,
		size:    afterPath.Size(),
		mode:    afterPath.Mode(),
		modTime: afterPath.ModTime().UnixNano(),
		digest:  digest,
	}, nil
}

func safeGatewayHistoryArtifact(path string, info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && !generatedIngressPathIsReparsePoint(path)
}

func sameGatewayHistoryFileMetadata(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Size() == right.Size() && left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func parseGatewayHistoryArtifactName(name string) (uint64, string, uint64, gatewayHistoryArtifactKind, bool, error) {
	lowerName := strings.ToLower(name)
	switch name {
	case v2RouteStateFilename:
		return 0, "", 0, gatewayHistoryState, true, nil
	case gatewayMigrationFilename:
		return 0, "", 0, gatewayHistoryJournal, true, nil
	case gatewayRollbackRetirementFilename:
		return 0, "", 0, gatewayHistoryReceipt, true, nil
	case gatewayPreJournalAbortFilename:
		return 0, "", 0, gatewayHistoryAbort, true, nil
	}
	prefixes := []struct {
		prefix string
		kind   gatewayHistoryArtifactKind
	}{
		{gatewayV2GenerationStatePrefix, gatewayHistoryState},
		{gatewayV2GenerationJournalPrefix, gatewayHistoryJournal},
		{gatewayV2GenerationReceiptPrefix, gatewayHistoryReceipt},
		{gatewayV2GenerationAbortPrefix, gatewayHistoryAbort},
	}
	for _, candidate := range prefixes {
		if !strings.HasPrefix(name, candidate.prefix) {
			continue
		}
		tail := strings.TrimSuffix(strings.TrimPrefix(name, candidate.prefix), ".bundle")
		parts := strings.Split(tail, ".")
		if !strings.HasSuffix(name, ".bundle") || len(parts) != 2 || len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
			return 0, "", 0, 0, true, errors.New("generated ingress upgrade history filename is invalid")
		}
		generation, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || generation == 0 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
			return 0, "", 0, 0, true, errors.New("generated ingress upgrade history generation is invalid")
		}
		return generation, parts[1], 0, candidate.kind, true, nil
	}
	intentPrefixes := []string{gatewayRebindProtectedIntentFilenamePrefix, gatewayRebindProtectedIntentFilenamePrefixV2}
	for _, intentPrefix := range intentPrefixes {
		if !strings.HasPrefix(lowerName, strings.ToLower(intentPrefix)) {
			continue
		}
		if !strings.HasPrefix(name, intentPrefix) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history filename is invalid")
		}
		tail := strings.TrimSuffix(strings.TrimPrefix(name, intentPrefix), ".bundle")
		parts := strings.Split(tail, ".")
		if !strings.HasSuffix(name, ".bundle") || len(parts) != 2 ||
			len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history filename is invalid")
		}
		generation, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || generation == 0 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history generation is invalid")
		}
		return generation, parts[1], 0, gatewayHistoryRebindIntent, true, nil
	}
	if strings.HasPrefix(lowerName, gatewayRebindPredecessorCheckpointFilenamePrefix) {
		if !strings.HasPrefix(name, gatewayRebindPredecessorCheckpointFilenamePrefix) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind predecessor checkpoint filename is invalid")
		}
		tail := strings.TrimSuffix(strings.TrimPrefix(name, gatewayRebindPredecessorCheckpointFilenamePrefix), ".bundle")
		parts := strings.Split(tail, ".")
		if !strings.HasSuffix(name, ".bundle") || len(parts) != 2 ||
			len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind predecessor checkpoint filename is invalid")
		}
		generation, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || generation == 0 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind predecessor checkpoint generation is invalid")
		}
		return generation, parts[1], 0, gatewayHistoryRebindPredecessorCheckpoint, true, nil
	}
	if strings.HasPrefix(lowerName, gatewayRebindProgressFilenamePrefix) {
		if !strings.HasPrefix(name, gatewayRebindProgressFilenamePrefix) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history filename is invalid")
		}
		tail := strings.TrimSuffix(strings.TrimPrefix(name, gatewayRebindProgressFilenamePrefix), ".bundle")
		parts := strings.Split(tail, ".")
		if !strings.HasSuffix(name, ".bundle") || len(parts) != 3 ||
			len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) ||
			len(parts[2]) != gatewayRebindProgressSequenceDigits+1 || !strings.HasPrefix(parts[2], "s") {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history filename is invalid")
		}
		generation, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || generation == 0 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history generation is invalid")
		}
		sequence, err := strconv.ParseUint(parts[2][1:], 10, 64)
		if err != nil || sequence == 0 || fmt.Sprintf("s%0*d", gatewayRebindProgressSequenceDigits, sequence) != parts[2] {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind history sequence is invalid")
		}
		return generation, parts[1], sequence, gatewayHistoryRebindProgress, true, nil
	}
	terminalPrefixes := []string{gatewayRebindFinalHandoverTerminalFilenamePrefix, gatewayRebindTerminalFilenamePrefixV2}
	for _, terminalPrefix := range terminalPrefixes {
		if !strings.HasPrefix(lowerName, strings.ToLower(terminalPrefix)) {
			continue
		}
		if !strings.HasPrefix(name, terminalPrefix) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind terminal filename is invalid")
		}
		tail := strings.TrimSuffix(strings.TrimPrefix(name, terminalPrefix), ".bundle")
		parts := strings.Split(tail, ".")
		if !strings.HasSuffix(name, ".bundle") || len(parts) != 2 ||
			len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind terminal filename is invalid")
		}
		generation, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || generation == 0 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
			return 0, "", 0, 0, true, errors.New("generated ingress rebind terminal generation is invalid")
		}
		return generation, parts[1], 0, gatewayHistoryRebindTerminal, true, nil
	}
	// Reserve the whole upgrade-history namespace. New terminal record types
	// must be explicitly taught to this scanner before they can affect
	// ownership, so unknown artifacts fail closed.
	if strings.HasPrefix(lowerName, "routes-v2") || strings.HasPrefix(lowerName, "gateway-v1-to-v2") ||
		strings.HasPrefix(lowerName, "gateway-rebind") {
		return 0, "", 0, 0, true, errors.New("generated ingress upgrade history filename is invalid")
	}
	return 0, "", 0, 0, false, nil
}

func sameGatewayHistorySnapshot(left, right gatewayHistorySnapshot) bool {
	if len(left.files) != len(right.files) {
		return false
	}
	for name, leftFile := range left.files {
		rightFile, ok := right.files[name]
		if !ok || !os.SameFile(leftFile.info, rightFile.info) ||
			leftFile.size != rightFile.size || leftFile.mode != rightFile.mode ||
			leftFile.modTime != rightFile.modTime || leftFile.digest != rightFile.digest {
			return false
		}
	}
	return true
}
