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

	gatewayV2GenerationDigits        = 20
	gatewayV2GenerationStatePrefix   = "routes-v2.g"
	gatewayV2GenerationJournalPrefix = "gateway-v1-to-v2.g"
	gatewayV2GenerationReceiptPrefix = "gateway-v1-to-v2-retirement.g"

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

type gatewayHistoryArtifactKind uint8

const (
	gatewayHistoryState gatewayHistoryArtifactKind = iota + 1
	gatewayHistoryJournal
	gatewayHistoryReceipt
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
}

type gatewayHistorySnapshot struct {
	generations map[uint64]gatewayHistoryGeneration
	files       map[string]gatewayHistoryFileFingerprint
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
// for one immutable generation. Existing is false only when resolution has
// allocated the next create-only paths; such a selection has zero state and
// journal values.
type gatewayUpgradeGenerationSelection struct {
	Store        *gatewayUpgradeStateStore
	Generation   uint64
	State        gatewayV2RouteState
	Journal      gatewayMigrationJournal
	Existing     bool
	PartialState bool
	Retired      bool
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
	stateName, journalName, receiptName, statePurpose, journalPurpose, receiptPurpose := gatewayUpgradeGenerationNames(generation, operationID)
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
	}, nil
}

func gatewayUpgradeGenerationNames(generation uint64, operationID string) (string, string, string, string, string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayV2GenerationStatePrefix + token + ".bundle",
		gatewayV2GenerationJournalPrefix + token + ".bundle",
		gatewayV2GenerationReceiptPrefix + token + ".bundle",
		v2RouteStatePurpose + "/" + scope,
		gatewayMigrationPurpose + "/" + scope,
		gatewayRollbackRetirementPurpose + "/" + scope
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
// rollback-retirement receipts before v1 can resume.
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
	if latest.Journal.OperationID != operationID {
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
	if latest.PartialState {
		return latest, nil
	}
	if latest.Journal.OperationID == operationID {
		return latest, nil
	}
	for _, generation := range history.generations[:len(history.generations)-1] {
		if generation.Journal.OperationID == operationID {
			return gatewayUpgradeGenerationSelection{}, errors.New("generated ingress upgrade operation belongs to historical generation")
		}
	}
	if !latest.Retired || latest.Journal.Phase != gatewayPhaseRolledBack || latest.Generation == ^uint64(0) {
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
	if m == nil || m.store == nil {
		return gatewayUpgradeHistory{}, errors.New("generated ingress history store is unavailable")
	}
	before, err := readGatewayHistorySnapshot(m.store)
	if err != nil {
		return gatewayUpgradeHistory{}, err
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
		if artifacts.state.path == "" {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history is partial")
		}
		var store *gatewayUpgradeStateStore
		if generation == 0 {
			store, err = newGatewayUpgradeStateStore(m.options.DataRoot)
		} else {
			if artifacts.operationID == "" {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history operation is missing")
			}
			store, err = newGatewayUpgradeGenerationStore(m.options.DataRoot, generation, artifacts.operationID)
		}
		if err != nil || store.v2Path != artifacts.state.path ||
			(artifacts.journal.path != "" && store.journalPath != artifacts.journal.path) ||
			(artifacts.receipt.path != "" && store.receiptPath != artifacts.receipt.path) {
			return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history path is invalid")
		}
		if artifacts.journal.path == "" {
			if partialOperationID == "" || index != len(generations)-1 || artifacts.receipt.path != "" {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history is partial")
			}
			state, sourceErr := store.loadV2State()
			if sourceErr != nil || state.OperationID != partialOperationID ||
				(store.operationID != "" && store.operationID != state.OperationID) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress partial state operation is invalid")
			}
			source, sourceErr := m.store.load()
			if sourceErr != nil || !gatewayV2InitialStateMatchesSource(state, source) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress partial state source is stale")
			}
			if _, duplicate := seenOperations[state.OperationID]; duplicate {
				return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade operation is duplicated across generations")
			}
			seenOperations[state.OperationID] = struct{}{}
			result.generations = append(result.generations, gatewayUpgradeGenerationSelection{
				Store: store, Generation: generation, State: state, PartialState: true,
			})
			continue
		}
		operationID := artifacts.operationID
		if generation == 0 {
			journal, loadErr := store.loadMigrationJournal()
			if loadErr != nil {
				return gatewayUpgradeHistory{}, loadErr
			}
			operationID = journal.OperationID
		}
		state, journal, loadErr := store.loadHistoricalBoundUpgrade(operationID)
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
			activeState, activeJournal, activeErr := store.loadBoundUpgrade(operationID)
			if activeErr != nil || !reflect.DeepEqual(activeState, state) || !reflect.DeepEqual(activeJournal, journal) {
				return gatewayUpgradeHistory{}, errors.New("generated ingress committed generation source is stale")
			}
			selection := gatewayUpgradeGenerationSelection{
				Store: store, Generation: generation, State: state, Journal: journal, Existing: true,
			}
			result.store, result.state, result.journal, result.committed = store, state, journal, true
			result.generations = append(result.generations, selection)
			continue
		}
		selection := gatewayUpgradeGenerationSelection{
			Store: store, Generation: generation, State: state, Journal: journal, Existing: true,
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
		if _, loadErr := store.loadRollbackRetirementReceipt(state, journal); loadErr != nil {
			return gatewayUpgradeHistory{}, loadErr
		}
		selection.Retired = true
		result.generations = append(result.generations, selection)
	}

	after, err := readGatewayHistorySnapshot(m.store)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		return gatewayUpgradeHistory{}, errors.New("generated ingress upgrade history changed during inspection")
	}
	return result, nil
}

func readGatewayHistorySnapshot(store *stateStore) (gatewayHistorySnapshot, error) {
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
	snapshot := gatewayHistorySnapshot{
		generations: make(map[uint64]gatewayHistoryGeneration),
		files:       make(map[string]gatewayHistoryFileFingerprint),
	}
	for _, entry := range entries {
		generation, operationID, kind, relevant, parseErr := parseGatewayHistoryArtifactName(entry.Name())
		if parseErr != nil {
			return gatewayHistorySnapshot{}, parseErr
		}
		if !relevant {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		if filepath.Dir(path) != store.root || filepath.Clean(path) != path {
			return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history path is invalid")
		}
		fingerprint, readErr := fingerprintGatewayHistoryArtifact(path)
		if readErr != nil {
			return gatewayHistorySnapshot{}, readErr
		}
		artifacts := snapshot.generations[generation]
		artifacts.generation = generation
		if artifacts.operationID != "" && operationID != "" && artifacts.operationID != operationID {
			return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade generation has duplicate operations")
		}
		if operationID != "" {
			artifacts.operationID = operationID
		}
		artifact := gatewayHistoryArtifact{path: path}
		switch kind {
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
		default:
			return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history artifact kind is invalid")
		}
		snapshot.generations[generation] = artifacts
		snapshot.files[entry.Name()] = fingerprint
	}
	if store.sameDirectory(before) != nil {
		return gatewayHistorySnapshot{}, errors.New("generated ingress upgrade history directory changed")
	}
	return snapshot, nil
}

// fingerprintGatewayHistoryArtifact reads a protected bundle through a
// verified regular-file handle. Path and handle identities are checked before
// and after the bounded read. The surrounding two-snapshot scan therefore
// rejects replacements and content changes that persist across its reads.
func fingerprintGatewayHistoryArtifact(path string) (gatewayHistoryFileFingerprint, error) {
	unsafe := func() (gatewayHistoryFileFingerprint, error) {
		return gatewayHistoryFileFingerprint{}, errors.New("generated ingress upgrade history artifact is unsafe")
	}
	before, err := os.Lstat(path)
	if err != nil || !safeGatewayHistoryArtifact(path, before) || before.Size() <= 0 || before.Size() > maxGatewayHistoryArtifactBytes {
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
	read, err := io.Copy(hash, io.LimitReader(file, maxGatewayHistoryArtifactBytes+1))
	if err != nil || read <= 0 || read > maxGatewayHistoryArtifactBytes || read != opened.Size() {
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

func parseGatewayHistoryArtifactName(name string) (uint64, string, gatewayHistoryArtifactKind, bool, error) {
	switch name {
	case v2RouteStateFilename:
		return 0, "", gatewayHistoryState, true, nil
	case gatewayMigrationFilename:
		return 0, "", gatewayHistoryJournal, true, nil
	case gatewayRollbackRetirementFilename:
		return 0, "", gatewayHistoryReceipt, true, nil
	}
	prefixes := []struct {
		prefix string
		kind   gatewayHistoryArtifactKind
	}{
		{gatewayV2GenerationStatePrefix, gatewayHistoryState},
		{gatewayV2GenerationJournalPrefix, gatewayHistoryJournal},
		{gatewayV2GenerationReceiptPrefix, gatewayHistoryReceipt},
	}
	for _, candidate := range prefixes {
		if !strings.HasPrefix(name, candidate.prefix) {
			continue
		}
		tail := strings.TrimSuffix(strings.TrimPrefix(name, candidate.prefix), ".bundle")
		parts := strings.Split(tail, ".")
		if !strings.HasSuffix(name, ".bundle") || len(parts) != 2 || len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
			return 0, "", 0, true, errors.New("generated ingress upgrade history filename is invalid")
		}
		generation, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || generation == 0 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
			return 0, "", 0, true, errors.New("generated ingress upgrade history generation is invalid")
		}
		return generation, parts[1], candidate.kind, true, nil
	}
	// Reserve the whole upgrade-history namespace. A future abort record must
	// be explicitly taught to this scanner before its presence can affect
	// ownership; an unknown abort-like artifact therefore fails closed today.
	if strings.HasPrefix(name, "routes-v2") || strings.HasPrefix(name, "gateway-v1-to-v2") {
		return 0, "", 0, true, errors.New("generated ingress upgrade history filename is invalid")
	}
	return 0, "", 0, false, nil
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
