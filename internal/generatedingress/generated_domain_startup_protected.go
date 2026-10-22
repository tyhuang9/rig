package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/secretfile"
)

const (
	generatedDomainStartupMaximumEntries = 4096
	generatedDomainStartupMaximumBytes   = 16 << 20
)

type generatedDomainProtectedArtifact struct {
	name        string
	fingerprint gatewayHistoryFileFingerprint
}

type generatedDomainProtectedScanEntry struct {
	entry       os.DirEntry
	name        string
	path        string
	fingerprint gatewayHistoryFileFingerprint
}

// generatedDomainProtectedReader binds every classifier-only decode to the
// exact file fingerprint captured by the scanner and charges the actual
// decrypted JSON bytes before decoding or retaining any typed value. The
// ordinary effect-capable stores deliberately do not use this reader.
type generatedDomainProtectedReader struct {
	directory       *stateStore
	directoryBefore os.FileInfo
	captured        map[string]gatewayHistoryFileFingerprint
	bodies          map[string][]byte
	usedBytes       int64
}

func newGeneratedDomainProtectedReader(directory *stateStore, directoryBefore os.FileInfo,
	captured map[string]gatewayHistoryFileFingerprint,
) (*generatedDomainProtectedReader, error) {
	if directory == nil || directoryBefore == nil || len(captured) > generatedDomainStartupMaximumEntries {
		return nil, errors.New("generated domain protected reader input is invalid")
	}
	return &generatedDomainProtectedReader{
		directory: directory, directoryBefore: directoryBefore,
		captured: captured, bodies: make(map[string][]byte, len(captured)),
	}, nil
}

func (r *generatedDomainProtectedReader) clear() {
	if r == nil {
		return
	}
	for path, body := range r.bodies {
		clear(body)
		delete(r.bodies, path)
	}
}

func sameGeneratedDomainProtectedFingerprint(left, right gatewayHistoryFileFingerprint) bool {
	return left.info != nil && right.info != nil && os.SameFile(left.info, right.info) &&
		left.size == right.size && left.mode == right.mode && left.modTime == right.modTime &&
		left.digest == right.digest
}

func (r *generatedDomainProtectedReader) read(path, purpose string, maximum int) ([]byte, error) {
	if r == nil || r.directory == nil || path == "" || purpose == "" || maximum <= 0 {
		return nil, errors.New("generated domain protected read input is invalid")
	}
	if body, ok := r.bodies[path]; ok {
		if len(body) == 0 || len(body) > maximum {
			return nil, errors.New("generated domain protected artifact is invalid")
		}
		return body, nil
	}
	captured, ok := r.captured[path]
	if !ok {
		return nil, errors.New("generated domain protected artifact was not preflighted")
	}
	var body []byte
	var err error
	if strings.HasPrefix(purpose, gatewayCurrentRouteStatePurposeV2+"/") {
		body, err = secretfile.ReadBounded(path, purpose, maxGatewayCurrentRouteStateBytes)
	} else {
		body, err = upgradeProtectedRead(path, purpose)
	}
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > maximum || int64(len(body)) > generatedDomainStartupMaximumBytes-r.usedBytes {
		clear(body)
		return nil, errors.New("generated domain protected decoded bytes exceed their bound")
	}
	// Re-fingerprint before exposing bytes to a decoder. This rejects a file
	// that changed after the namespace census, while the actual plaintext
	// debit above prevents aggregate decoded growth from hiding behind a small
	// earlier persisted-size fingerprint.
	after, fingerprintErr := fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
	if fingerprintErr != nil || !sameGeneratedDomainProtectedFingerprint(captured, after) ||
		r.directory.sameDirectory(r.directoryBefore) != nil {
		clear(body)
		return nil, errors.New("generated domain protected artifact changed during read")
	}
	r.usedBytes += int64(len(body))
	r.bodies[path] = body
	return body, nil
}

func (r *generatedDomainProtectedReader) readStrict(path, purpose string, maximum int, target any) ([]byte, error) {
	body, err := r.read(path, purpose, maximum)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("generated domain protected artifact is invalid")
	}
	return body, nil
}

type generatedDomainProtectedStartupSnapshot struct {
	directoryPresent bool
	effectEmpty      bool
	nativeV1         *routeState
	upgrade          gatewayUpgradeHistory
	currentV1        map[string]gatewayCurrentRouteState
	currentV2        gatewayCurrentV2Census
	rebindArtifacts  []generatedDomainRebindArtifact
	rebind           gatewayRebindProtectedIntentHistory
	hasRebind        bool
	terminals        map[string]gatewayRebindAttemptTerminalView
	artifacts        []generatedDomainProtectedArtifact
	directoryPaths   map[string]os.FileInfo
	ambiguous        bool
}

type generatedDomainRebindArtifact struct {
	generation  uint64
	operationID string
	sequence    uint64
	kind        gatewayHistoryArtifactKind
}

func readGeneratedDomainProtectedStartup(ctx context.Context, dataRoot string) (generatedDomainProtectedStartupSnapshot, error) {
	if ctx == nil || ctx.Err() != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected startup input is invalid")
	}
	store, present, paths, err := inspectStateStoreReadOnly(dataRoot)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected namespace is unavailable")
	}
	result := generatedDomainProtectedStartupSnapshot{
		directoryPresent: present,
		effectEmpty:      !present,
		directoryPaths:   cloneGeneratedDomainPathIdentities(paths),
	}
	if !present {
		result.currentV2 = gatewayCurrentV2Census{files: make(map[string]gatewayCurrentV2CensusEntry)}
		result.currentV1 = make(map[string]gatewayCurrentRouteState)
		result.terminals = make(map[string]gatewayRebindAttemptTerminalView)
		return result, nil
	}
	before, err := store.directoryIdentity()
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected directory is unavailable")
	}
	entries, err := readGeneratedDomainProtectedDirectoryBounded(store.root)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected namespace is unavailable")
	}
	scanEntries, err := fingerprintGeneratedDomainProtectedEntries(store, entries)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, err
	}
	captured := make(map[string]gatewayHistoryFileFingerprint, len(scanEntries))
	for _, scanned := range scanEntries {
		captured[scanned.path] = scanned.fingerprint
	}
	reader, err := newGeneratedDomainProtectedReader(store, before, captured)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, err
	}
	defer reader.clear()
	result.currentV2 = gatewayCurrentV2Census{files: make(map[string]gatewayCurrentV2CensusEntry)}
	result.currentV1 = make(map[string]gatewayCurrentRouteState)
	result.terminals = make(map[string]gatewayRebindAttemptTerminalView)
	lockOnly := true
	for _, scanned := range scanEntries {
		if ctx.Err() != nil {
			return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected namespace is invalid")
		}
		name, path, fingerprint := scanned.name, scanned.path, scanned.fingerprint
		switch {
		case name == gatewayLockFilename:
		case name == stateFilename:
			value, loadErr := readGeneratedDomainRouteState(reader, store)
			if loadErr != nil {
				err = loadErr
			} else {
				copy := cloneRouteState(value)
				result.nativeV1 = &copy
			}
		case gatewayCurrentRouteV2NamespaceName(name):
			generation, operationID, parseErr := parseGatewayCurrentRouteStateNameV2(name)
			_, purpose := gatewayCurrentRouteStateNameV2(generation, operationID)
			bound := &gatewayCurrentRouteStateV2Store{directory: store, path: path, purpose: purpose}
			state, readErr := readGeneratedDomainCurrentRouteStateV2(reader, bound)
			if parseErr != nil || readErr != nil || !validGatewayCurrentRouteStateV2(state) ||
				state.Lineage.ProtectedGeneration != generation || state.Lineage.OperationID != operationID {
				err = errors.New("generated domain protected current state is invalid")
			} else {
				result.currentV2.files[name] = gatewayCurrentV2CensusEntry{Generation: generation,
					OperationID: operationID, Fingerprint: fingerprint, State: cloneGatewayCurrentRouteStateV2(state)}
			}
		default:
			generation, operationID, sequence, kind, relevant, parseErr := parseGatewayHistoryArtifactName(name)
			currentGeneration, currentOperation, currentRelevant, currentErr := parseGatewayCurrentRoutePresenceName(name)
			if parseErr != nil || currentErr != nil || (!relevant && !currentRelevant) {
				err = errors.New("generated domain protected artifact is unknown")
				break
			}
			if currentRelevant {
				bound := &gatewayCurrentRouteStateStore{directory: store, dataRoot: dataRoot,
					generation: currentGeneration, operationID: currentOperation, path: path}
				_, bound.purpose = gatewayCurrentRouteStateName(currentGeneration, currentOperation)
				state, loadErr := readGeneratedDomainCurrentRouteState(reader, bound)
				err = loadErr
				if err == nil {
					result.currentV1[name] = cloneGatewayCurrentRouteState(state)
				}
			} else if kind >= gatewayHistoryRebindIntent {
				err = validateGeneratedDomainRebindPresenceArtifact(reader, store, path, name, generation,
					operationID, sequence, kind)
				if err == nil {
					result.rebindArtifacts = append(result.rebindArtifacts, generatedDomainRebindArtifact{
						generation: generation, operationID: operationID, sequence: sequence, kind: kind,
					})
					if kind == gatewayHistoryRebindTerminal {
						view, committed, viewErr := readGeneratedDomainTerminalView(reader, store, path, name,
							generation, operationID)
						if viewErr != nil {
							err = viewErr
						} else if _, duplicate := result.terminals[operationID]; committed && duplicate {
							err = errors.New("generated domain protected terminal is duplicated")
						} else if committed {
							result.terminals[operationID] = view
						}
					}
				}
			}
		}
		if err != nil {
			return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected artifact is invalid")
		}
		if name != gatewayLockFilename {
			lockOnly = false
		}
		result.artifacts = append(result.artifacts, generatedDomainProtectedArtifact{name: name, fingerprint: fingerprint})
	}
	if store.sameDirectory(before) != nil || ctx.Err() != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected namespace changed")
	}
	historySnapshot, err := readGatewayHistorySnapshotPolicyBounded(store, true, true)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected history is invalid")
	}
	if historySnapshot.reader != nil {
		defer historySnapshot.reader.clear()
	}
	partialOperationID, err := generatedDomainPartialUpgradeOperation(store, historySnapshot)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected history is invalid")
	}
	result.upgrade, err = scanGatewayUpgradeHistorySnapshotReadOnly(store, historySnapshot, true, partialOperationID)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected history is invalid")
	}
	result.currentV2, err = scanGatewayCurrentV2CensusBounded(store)
	if err != nil {
		return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected current state is invalid")
	}
	if result.upgrade.committed {
		result.rebind, err = scanGatewayRebindProtectedIntentHistoryReadOnly(
			store, result.upgrade, historySnapshot, nil, true)
		if err != nil {
			return generatedDomainProtectedStartupSnapshot{}, errors.New("generated domain protected rebind history is invalid")
		}
		result.hasRebind = true
	}
	sort.Slice(result.artifacts, func(i, j int) bool { return result.artifacts[i].name < result.artifacts[j].name })
	result.effectEmpty = len(entries) == 0 || lockOnly
	return result, nil
}

func readGeneratedDomainProtectedDirectoryBounded(root string) ([]os.DirEntry, error) {
	directory, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	result := make([]os.DirEntry, 0, 128)
	for {
		entries, readErr := directory.ReadDir(128)
		if len(entries) > generatedDomainStartupMaximumEntries-len(result) {
			return nil, errors.New("generated domain protected namespace exceeds its entry bound")
		}
		result = append(result, entries...)
		switch readErr {
		case nil:
			continue
		case io.EOF:
			sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
			return result, nil
		default:
			return nil, readErr
		}
	}
}

func fingerprintGeneratedDomainProtectedEntries(store *stateStore, entries []os.DirEntry) (
	[]generatedDomainProtectedScanEntry, error,
) {
	if store == nil || len(entries) > generatedDomainStartupMaximumEntries {
		return nil, errors.New("generated domain protected namespace exceeds its entry bound")
	}
	result := make([]generatedDomainProtectedScanEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, errors.New("generated domain protected namespace is invalid")
		}
		name := entry.Name()
		path := filepath.Join(store.root, name)
		if filepath.Dir(path) != store.root || filepath.Clean(path) != path {
			return nil, errors.New("generated domain protected path is invalid")
		}
		var fingerprint gatewayHistoryFileFingerprint
		var err error
		switch {
		case name == gatewayLockFilename:
			fingerprint, err = fingerprintGatewayPresenceLock(path)
		case name == stateFilename:
			fingerprint, err = fingerprintGatewayHistoryArtifactBound(path, maxStateBytes)
		case gatewayCurrentRouteV2NamespaceName(name):
			if _, _, parseErr := parseGatewayCurrentRouteStateNameV2(name); parseErr != nil {
				return nil, errors.New("generated domain protected artifact is unknown")
			}
			fingerprint, err = fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
		default:
			_, _, _, _, relevant, historyErr := parseGatewayHistoryArtifactName(name)
			_, _, currentRelevant, currentErr := parseGatewayCurrentRoutePresenceName(name)
			if historyErr != nil || currentErr != nil || (!relevant && !currentRelevant) {
				return nil, errors.New("generated domain protected artifact is unknown")
			}
			fingerprint, err = fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
		}
		if err != nil {
			return nil, errors.New("generated domain protected artifact is invalid")
		}
		if fingerprint.size > generatedDomainStartupMaximumBytes-total {
			return nil, errors.New("generated domain protected namespace exceeds its bound")
		}
		total += fingerprint.size
		result = append(result, generatedDomainProtectedScanEntry{
			entry: entry, name: name, path: path, fingerprint: fingerprint,
		})
	}
	return result, nil
}

func readGeneratedDomainRouteState(reader *generatedDomainProtectedReader, store *stateStore) (routeState, error) {
	if store == nil {
		return routeState{}, errors.New("generated domain route state store is invalid")
	}
	value := routeState{Version: stateVersion, Active: map[string]routeRecord{}}
	if _, err := reader.readStrict(store.path, statePurpose, maxStateBytes, &value); err != nil ||
		value.Version != stateVersion || value.Active == nil || len(value.Active) > maxStateApps || !validRouteState(value) {
		return routeState{}, errors.New("generated domain route state is invalid")
	}
	return value, nil
}

func readGeneratedDomainCurrentRouteState(reader *generatedDomainProtectedReader,
	store *gatewayCurrentRouteStateStore,
) (gatewayCurrentRouteState, error) {
	if store == nil || store.directory == nil {
		return gatewayCurrentRouteState{}, errors.New("generated domain current route store is invalid")
	}
	var value gatewayCurrentRouteState
	if _, err := reader.readStrict(store.path, store.purpose, maxGatewayCurrentRouteStateBytes, &value); err != nil ||
		value.Lineage.ProtectedGeneration != store.generation || value.Lineage.OperationID != store.operationID ||
		!validGatewayCurrentRouteState(value) {
		return gatewayCurrentRouteState{}, errors.New("generated domain current route state is invalid")
	}
	return value, nil
}

func readGeneratedDomainCurrentRouteStateV2(reader *generatedDomainProtectedReader,
	store *gatewayCurrentRouteStateV2Store,
) (gatewayCurrentRouteStateV2, error) {
	if store == nil || store.directory == nil {
		return gatewayCurrentRouteStateV2{}, errors.New("generated domain current V2 route store is invalid")
	}
	var value gatewayCurrentRouteStateV2
	body, err := reader.readStrict(store.path, store.purpose, maxGatewayCurrentRouteStateBytes, &value)
	if err != nil {
		return gatewayCurrentRouteStateV2{}, err
	}
	canonical, marshalErr := json.Marshal(value)
	if marshalErr != nil || !bytes.Equal(canonical, body) {
		clear(canonical)
		return gatewayCurrentRouteStateV2{}, errors.New("generated domain current V2 route state is noncanonical")
	}
	clear(canonical)
	return value, nil
}

func validateGeneratedDomainRebindPresenceArtifact(reader *generatedDomainProtectedReader, directory *stateStore,
	path, name string, generation uint64, operationID string, sequence uint64, kind gatewayHistoryArtifactKind,
) error {
	switch kind {
	case gatewayHistoryRebindPredecessorCheckpoint:
		store := &gatewayRebindPredecessorCheckpointStore{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindPredecessorCheckpointName(generation, operationID)
		_, err := readGeneratedDomainRebindCheckpoint(reader, store)
		return err
	case gatewayHistoryRebindIntent:
		if strings.HasPrefix(name, gatewayRebindProtectedIntentFilenamePrefixV2) {
			store := &gatewayRebindProtectedIntentV2Store{directory: directory, generation: generation,
				operationID: operationID, path: path}
			_, store.purpose = gatewayRebindProtectedIntentV2Name(generation, operationID)
			_, err := readGeneratedDomainRebindIntentV2(reader, store)
			return err
		}
		store := &gatewayRebindProtectedIntentStore{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindProtectedIntentName(generation, operationID)
		_, err := readGeneratedDomainRebindIntent(reader, store)
		return err
	case gatewayHistoryRebindProgress:
		store := &gatewayRebindProgressStore{directory: directory, generation: generation,
			operationID: operationID, sequence: sequence, path: path}
		_, store.purpose = gatewayRebindProgressName(generation, operationID, sequence)
		_, err := readGeneratedDomainRebindProgress(reader, store)
		return err
	case gatewayHistoryRebindTerminal:
		if strings.HasPrefix(name, gatewayRebindTerminalFilenamePrefixV2) {
			store := &gatewayRebindTerminalStoreV2{directory: directory, generation: generation,
				operationID: operationID, path: path}
			_, store.purpose = gatewayRebindTerminalNameV2(generation, operationID)
			_, err := readGeneratedDomainRebindTerminalV2(reader, store)
			return err
		}
		store := &gatewayRebindFinalHandoverTerminalStore{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindFinalHandoverTerminalName(generation, operationID)
		_, err := readGeneratedDomainRebindTerminalLegacy(reader, store)
		return err
	default:
		return errors.New("generated domain rebind artifact kind is invalid")
	}
}

func readGeneratedDomainRebindCheckpoint(reader *generatedDomainProtectedReader,
	store *gatewayRebindPredecessorCheckpointStore,
) (gatewayRebindPredecessorCheckpoint, error) {
	if reader == nil {
		return store.load()
	}
	var value gatewayRebindPredecessorCheckpoint
	if store == nil || store.directory == nil {
		return value, errors.New("generated domain rebind checkpoint store is invalid")
	}
	if _, err := reader.readStrict(store.path, store.purpose, maxGatewayRebindPredecessorCheckpointBytes, &value); err != nil ||
		value.Generation != store.generation || value.OperationID != store.operationID ||
		!validGatewayRebindPredecessorCheckpoint(value) {
		return gatewayRebindPredecessorCheckpoint{}, errors.New("generated domain rebind checkpoint is invalid")
	}
	return value, nil
}

func readGeneratedDomainRebindIntent(reader *generatedDomainProtectedReader,
	store *gatewayRebindProtectedIntentStore,
) (gatewayRebindProtectedIntent, error) {
	if reader == nil {
		return store.load()
	}
	var value gatewayRebindProtectedIntent
	if store == nil || store.directory == nil {
		return value, errors.New("generated domain rebind intent store is invalid")
	}
	if _, err := reader.readStrict(store.path, store.purpose, maxGatewayRebindProtectedIntentBytes, &value); err != nil ||
		len(value.Intent.Roster) != len(value.Intent.RosterEntryDigests) {
		return gatewayRebindProtectedIntent{}, errors.New("generated domain rebind intent is invalid")
	}
	for index := range value.Intent.Roster {
		value.Intent.Roster[index].EntryDigest = value.Intent.RosterEntryDigests[index]
	}
	if value.Generation != store.generation || value.OperationID != store.operationID ||
		!validGatewayRebindProtectedIntent(value) {
		return gatewayRebindProtectedIntent{}, errors.New("generated domain rebind intent is invalid")
	}
	return value, nil
}

func readGeneratedDomainRebindIntentV2(reader *generatedDomainProtectedReader,
	store *gatewayRebindProtectedIntentV2Store,
) (gatewayRebindProtectedIntentV2, error) {
	if reader == nil {
		return store.load()
	}
	var value gatewayRebindProtectedIntentV2
	if store == nil || store.directory == nil {
		return value, errors.New("generated domain typed rebind intent store is invalid")
	}
	if _, err := reader.readStrict(store.path, store.purpose, maxGatewayRebindProtectedIntentBytes, &value); err != nil ||
		len(value.Roster) != len(value.RosterEntryDigests) {
		return gatewayRebindProtectedIntentV2{}, errors.New("generated domain typed rebind intent is invalid")
	}
	for index := range value.Roster {
		value.Roster[index].EntryDigest = value.RosterEntryDigests[index]
	}
	if value.Generation != store.generation || value.OperationID != store.operationID ||
		!validGatewayRebindProtectedIntentV2(value) {
		return gatewayRebindProtectedIntentV2{}, errors.New("generated domain typed rebind intent is invalid")
	}
	return value, nil
}

func readGeneratedDomainRebindProgress(reader *generatedDomainProtectedReader,
	store *gatewayRebindProgressStore,
) (gatewayRebindProgressRecord, error) {
	if reader == nil {
		return store.load()
	}
	var value gatewayRebindProgressRecord
	if store == nil || store.directory == nil {
		return value, errors.New("generated domain rebind progress store is invalid")
	}
	if _, err := reader.readStrict(store.path, store.purpose, gatewayRebindProgressMaxBytes(store.sequence), &value); err != nil ||
		value.Generation != store.generation || value.OperationID != store.operationID ||
		value.Sequence != store.sequence || !validGatewayRebindProgressRecord(value) {
		return gatewayRebindProgressRecord{}, errors.New("generated domain rebind progress is invalid")
	}
	return value, nil
}

func readGeneratedDomainRebindTerminalV2(reader *generatedDomainProtectedReader,
	store *gatewayRebindTerminalStoreV2,
) (gatewayRebindTerminalReceiptV2, error) {
	if reader == nil {
		return store.load()
	}
	var value gatewayRebindTerminalReceiptV2
	if store == nil || store.directory == nil {
		return value, errors.New("generated domain typed rebind terminal store is invalid")
	}
	if _, err := reader.readStrict(store.path, store.purpose, maxGatewayRebindFinalHandoverTerminalBytes, &value); err != nil ||
		value.Generation != store.generation || value.OperationID != store.operationID ||
		!validGatewayRebindTerminalReceiptV2(value) {
		return gatewayRebindTerminalReceiptV2{}, errors.New("generated domain typed rebind terminal is invalid")
	}
	return value, nil
}

func readGeneratedDomainRebindTerminalLegacy(reader *generatedDomainProtectedReader,
	store *gatewayRebindFinalHandoverTerminalStore,
) (gatewayRebindFinalHandoverTerminalReceipt, error) {
	if reader == nil {
		return store.load()
	}
	var value gatewayRebindFinalHandoverTerminalReceipt
	if store == nil || store.directory == nil {
		return value, errors.New("generated domain rebind terminal store is invalid")
	}
	if _, err := reader.readStrict(store.path, store.purpose, maxGatewayRebindFinalHandoverTerminalBytes, &value); err != nil ||
		value.Generation != store.generation || value.OperationID != store.operationID ||
		!validGatewayRebindFinalHandoverTerminalReceiptValue(value) {
		return gatewayRebindFinalHandoverTerminalReceipt{}, errors.New("generated domain rebind terminal is invalid")
	}
	return value, nil
}

func sameGeneratedDomainProtectedStartup(left, right generatedDomainProtectedStartupSnapshot) bool {
	if left.directoryPresent != right.directoryPresent || left.effectEmpty != right.effectEmpty ||
		left.ambiguous != right.ambiguous || !reflect.DeepEqual(left.nativeV1, right.nativeV1) ||
		!sameGeneratedDomainUpgradeHistory(left.upgrade, right.upgrade) ||
		!reflect.DeepEqual(left.currentV1, right.currentV1) ||
		!sameGatewayCurrentV2Census(left.currentV2, right.currentV2) ||
		!reflect.DeepEqual(left.rebindArtifacts, right.rebindArtifacts) || left.hasRebind != right.hasRebind ||
		!sameGeneratedDomainRebindHistory(left.rebind, right.rebind) ||
		!reflect.DeepEqual(left.terminals, right.terminals) ||
		len(left.directoryPaths) != len(right.directoryPaths) || len(left.artifacts) != len(right.artifacts) {
		return false
	}
	for path, before := range left.directoryPaths {
		after, ok := right.directoryPaths[path]
		if !ok || before == nil || after == nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			return false
		}
	}
	for index := range left.artifacts {
		l, r := left.artifacts[index], right.artifacts[index]
		if l.name != r.name || l.fingerprint.info == nil || r.fingerprint.info == nil ||
			!os.SameFile(l.fingerprint.info, r.fingerprint.info) || l.fingerprint.size != r.fingerprint.size ||
			l.fingerprint.mode != r.fingerprint.mode || l.fingerprint.modTime != r.fingerprint.modTime ||
			l.fingerprint.digest != r.fingerprint.digest {
			return false
		}
	}
	return true
}

func sameGeneratedDomainRebindHistory(left, right gatewayRebindProtectedIntentHistory) bool {
	if !reflect.DeepEqual(left.Source, right.Source) ||
		!reflect.DeepEqual(left.Predecessor.State, right.Predecessor.State) ||
		!reflect.DeepEqual(left.Predecessor.Journal, right.Predecessor.Journal) ||
		left.Predecessor.Generation != right.Predecessor.Generation ||
		left.Predecessor.PartialState != right.Predecessor.PartialState ||
		left.Predecessor.Retired != right.Predecessor.Retired || left.Predecessor.Aborted != right.Predecessor.Aborted ||
		len(left.Intents) != len(right.Intents) || len(left.IntentsV2) != len(right.IntentsV2) ||
		len(left.Progress) != len(right.Progress) || len(left.Checkpoints) != len(right.Checkpoints) ||
		len(left.Terminals) != len(right.Terminals) || len(left.TerminalsV2) != len(right.TerminalsV2) {
		return false
	}
	for index := range left.Intents {
		if !reflect.DeepEqual(left.Intents[index].Intent, right.Intents[index].Intent) {
			return false
		}
	}
	for index := range left.IntentsV2 {
		if !reflect.DeepEqual(left.IntentsV2[index].Intent, right.IntentsV2[index].Intent) {
			return false
		}
	}
	for index := range left.Progress {
		if !reflect.DeepEqual(left.Progress[index].Record, right.Progress[index].Record) {
			return false
		}
	}
	for index := range left.Checkpoints {
		if !reflect.DeepEqual(left.Checkpoints[index].Checkpoint, right.Checkpoints[index].Checkpoint) {
			return false
		}
	}
	for index := range left.Terminals {
		if !reflect.DeepEqual(left.Terminals[index].Receipt, right.Terminals[index].Receipt) {
			return false
		}
	}
	for index := range left.TerminalsV2 {
		if !reflect.DeepEqual(left.TerminalsV2[index].Receipt, right.TerminalsV2[index].Receipt) {
			return false
		}
	}
	return true
}

func readGeneratedDomainTerminalView(reader *generatedDomainProtectedReader, directory *stateStore, path, name string, generation uint64,
	operationID string,
) (gatewayRebindAttemptTerminalView, bool, error) {
	if strings.HasPrefix(name, gatewayRebindTerminalFilenamePrefixV2) {
		store := &gatewayRebindTerminalStoreV2{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindTerminalNameV2(generation, operationID)
		receipt, err := readGeneratedDomainRebindTerminalV2(reader, store)
		if err != nil {
			return gatewayRebindAttemptTerminalView{}, false, err
		}
		switch receipt.Disposition {
		case appaccess.GatewayRebindDispositionAbort:
			return gatewayRebindAttemptTerminalView{}, false, nil
		case appaccess.GatewayRebindDispositionCommit:
			view, err := newGatewayRebindAttemptTerminalViewV2(receipt)
			return view, err == nil, err
		default:
			return gatewayRebindAttemptTerminalView{}, false,
				errors.New("generated domain typed rebind terminal disposition is invalid")
		}
	}
	store := &gatewayRebindFinalHandoverTerminalStore{directory: directory, generation: generation,
		operationID: operationID, path: path}
	_, store.purpose = gatewayRebindFinalHandoverTerminalName(generation, operationID)
	receipt, err := readGeneratedDomainRebindTerminalLegacy(reader, store)
	if err != nil {
		return gatewayRebindAttemptTerminalView{}, false, err
	}
	switch receipt.Disposition {
	case gatewayRebindFinalHandoverTerminalAbort:
		return gatewayRebindAttemptTerminalView{}, false, nil
	case gatewayRebindFinalHandoverTerminalCommit:
		view, err := newGatewayRebindAttemptTerminalViewLegacy(receipt)
		return view, err == nil, err
	default:
		return gatewayRebindAttemptTerminalView{}, false,
			errors.New("generated domain rebind terminal disposition is invalid")
	}
}

func generatedDomainPartialUpgradeOperation(store *stateStore, snapshot gatewayHistorySnapshot) (string, error) {
	generations := make([]uint64, 0, len(snapshot.generations))
	for generation, artifacts := range snapshot.generations {
		if artifacts.state.path == "" || artifacts.journal.path != "" || artifacts.abort.path != "" {
			continue
		}
		generations = append(generations, generation)
	}
	if len(generations) == 0 {
		return "", nil
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	generation := generations[len(generations)-1]
	artifacts := snapshot.generations[generation]
	{
		bound, err := bindGatewayUpgradeStateStoreReadOnly(store, generation, artifacts.operationID)
		if err != nil {
			return "", err
		}
		state, err := loadGeneratedDomainUpgradeState(bound, snapshot.reader)
		if err != nil {
			return "", err
		}
		if artifacts.operationID != "" && artifacts.operationID != state.OperationID {
			return "", errors.New("generated domain partial upgrade operation is crossed")
		}
		return state.OperationID, nil
	}
}

func sameGeneratedDomainUpgradeHistory(left, right gatewayUpgradeHistory) bool {
	if left.committed != right.committed || !reflect.DeepEqual(left.state, right.state) ||
		!reflect.DeepEqual(left.journal, right.journal) || len(left.generations) != len(right.generations) {
		return false
	}
	for index := range left.generations {
		l, r := left.generations[index], right.generations[index]
		if l.Generation != r.Generation || l.Existing != r.Existing || l.PartialState != r.PartialState ||
			l.Retired != r.Retired || l.Aborted != r.Aborted || l.operationID != r.operationID ||
			!reflect.DeepEqual(l.State, r.State) || !reflect.DeepEqual(l.Journal, r.Journal) {
			return false
		}
	}
	return true
}
