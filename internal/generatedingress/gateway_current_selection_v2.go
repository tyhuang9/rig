package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentV2Selection struct {
	Lineage    appaccess.GatewayCurrentLineageRef
	Origin     gatewayCurrentRouteOriginV2
	Source     gatewayCurrentSelection
	Store      *gatewayCurrentRouteStateV2Store
	State      gatewayCurrentRouteStateV2
	Projection gatewayCurrentProjectionV2
}

type gatewayCurrentProtectedSelection struct {
	Legacy *gatewayCurrentSelection
	V2     *gatewayCurrentV2Selection
}

type gatewayCurrentV2CensusEntry struct {
	Generation  uint64
	OperationID string
	Fingerprint gatewayHistoryFileFingerprint
	State       gatewayCurrentRouteStateV2
}

type gatewayCurrentV2Census struct {
	files map[string]gatewayCurrentV2CensusEntry
}

func gatewayCurrentRouteV2NamespaceName(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "gateway-current-routes.v2")
}

func parseGatewayCurrentRouteStateNameV2(name string) (uint64, string, error) {
	if !gatewayCurrentRouteV2NamespaceName(name) || !strings.HasPrefix(name, gatewayCurrentRouteStateFilenamePrefixV2) ||
		!strings.HasSuffix(name, ".bundle") {
		return 0, "", errors.New("generated ingress current V2 filename is invalid")
	}
	tail := strings.TrimSuffix(strings.TrimPrefix(name, gatewayCurrentRouteStateFilenamePrefixV2), ".bundle")
	parts := strings.Split(tail, ".")
	if len(parts) != 2 || len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
		return 0, "", errors.New("generated ingress current V2 filename is invalid")
	}
	generation, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || generation == math.MaxUint64 || fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
		return 0, "", errors.New("generated ingress current V2 filename is invalid")
	}
	return generation, parts[1], nil
}

func scanGatewayCurrentV2Census(store *stateStore) (gatewayCurrentV2Census, error) {
	if store == nil {
		return gatewayCurrentV2Census{}, errors.New("generated ingress current V2 census store is unavailable")
	}
	before, err := store.directoryIdentity()
	if err != nil {
		return gatewayCurrentV2Census{}, err
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return gatewayCurrentV2Census{}, err
	}
	return scanGatewayCurrentV2CensusEntries(store, before, entries, 0)
}

func scanGatewayCurrentV2CensusBounded(store *stateStore) (gatewayCurrentV2Census, error) {
	if store == nil {
		return gatewayCurrentV2Census{}, errors.New("generated ingress current V2 census store is unavailable")
	}
	before, err := store.directoryIdentity()
	if err != nil {
		return gatewayCurrentV2Census{}, err
	}
	entries, err := readGeneratedDomainProtectedDirectoryBounded(store.root)
	if err != nil {
		return gatewayCurrentV2Census{}, err
	}
	return scanGatewayCurrentV2CensusEntries(store, before, entries, generatedDomainStartupMaximumBytes)
}

type gatewayCurrentV2CensusScanEntry struct {
	name        string
	path        string
	generation  uint64
	operationID string
	fingerprint gatewayHistoryFileFingerprint
}

func scanGatewayCurrentV2CensusEntries(store *stateStore, before os.FileInfo, entries []os.DirEntry,
	maximumBytes int64,
) (gatewayCurrentV2Census, error) {
	scanned := make([]gatewayCurrentV2CensusScanEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !gatewayCurrentRouteV2NamespaceName(entry.Name()) {
			continue
		}
		generation, operationID, parseErr := parseGatewayCurrentRouteStateNameV2(entry.Name())
		if parseErr != nil {
			return gatewayCurrentV2Census{}, parseErr
		}
		path := filepath.Join(store.root, entry.Name())
		if filepath.Dir(path) != store.root || filepath.Clean(path) != path {
			return gatewayCurrentV2Census{}, errors.New("generated ingress current V2 path is invalid")
		}
		fingerprint, fingerprintErr := fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
		if fingerprintErr != nil {
			return gatewayCurrentV2Census{}, fingerprintErr
		}
		if maximumBytes > 0 && fingerprint.size > maximumBytes-total {
			return gatewayCurrentV2Census{}, errors.New("generated ingress current V2 census exceeds its aggregate byte bound")
		}
		total += fingerprint.size
		scanned = append(scanned, gatewayCurrentV2CensusScanEntry{
			name: entry.Name(), path: path, generation: generation, operationID: operationID,
			fingerprint: fingerprint,
		})
	}
	var reader *generatedDomainProtectedReader
	if maximumBytes > 0 {
		captured := make(map[string]gatewayHistoryFileFingerprint, len(scanned))
		for _, entry := range scanned {
			captured[entry.path] = entry.fingerprint
		}
		var readerErr error
		reader, readerErr = newGeneratedDomainProtectedReader(store, before, captured)
		if readerErr != nil {
			return gatewayCurrentV2Census{}, readerErr
		}
		defer reader.clear()
	}
	result := gatewayCurrentV2Census{files: make(map[string]gatewayCurrentV2CensusEntry)}
	for _, entry := range scanned {
		_, purpose := gatewayCurrentRouteStateNameV2(entry.generation, entry.operationID)
		unbound := &gatewayCurrentRouteStateV2Store{directory: store, path: entry.path, purpose: purpose}
		var state gatewayCurrentRouteStateV2
		var readErr error
		if reader != nil {
			state, readErr = readGeneratedDomainCurrentRouteStateV2(reader, unbound)
		} else {
			state, readErr = unbound.readCanonical()
		}
		if readErr != nil || !validGatewayCurrentRouteStateV2(state) ||
			state.Lineage.ProtectedGeneration != entry.generation || state.Lineage.OperationID != entry.operationID ||
			(entry.generation == 0 && state.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade) {
			return gatewayCurrentV2Census{}, errors.New("generated ingress current V2 filename lineage is invalid")
		}
		result.files[entry.name] = gatewayCurrentV2CensusEntry{
			Generation: entry.generation, OperationID: entry.operationID, Fingerprint: entry.fingerprint,
			State: cloneGatewayCurrentRouteStateV2(state),
		}
	}
	if store.sameDirectory(before) != nil {
		return gatewayCurrentV2Census{}, errors.New("generated ingress current V2 census directory changed")
	}
	return result, nil
}

func sameGatewayCurrentV2Census(left, right gatewayCurrentV2Census) bool {
	if len(left.files) != len(right.files) {
		return false
	}
	for name, l := range left.files {
		r, ok := right.files[name]
		if !ok || l.Generation != r.Generation || l.OperationID != r.OperationID ||
			!reflect.DeepEqual(l.State, r.State) || !os.SameFile(l.Fingerprint.info, r.Fingerprint.info) ||
			l.Fingerprint.size != r.Fingerprint.size || l.Fingerprint.mode != r.Fingerprint.mode ||
			l.Fingerprint.modTime != r.Fingerprint.modTime || l.Fingerprint.digest != r.Fingerprint.digest {
			return false
		}
	}
	return true
}

func gatewayCurrentExpectedOriginV2(selection gatewayCurrentSelection) (gatewayCurrentRouteOriginV2, error) {
	switch selection.Kind {
	case gatewayCurrentSelectionRebind:
		if selection.State == nil || !validGatewayCurrentRouteState(*selection.State) {
			return gatewayCurrentRouteOriginV2{}, errors.New("generated ingress current V2 rebind source is invalid")
		}
		source := gatewayCurrentRouteSourceV1{Revision: selection.State.Revision, Digest: selection.State.Digest}
		return gatewayCurrentRouteOriginV2{SourceV1: &source}, nil
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || selection.UpgradeSource == nil {
			return gatewayCurrentRouteOriginV2{}, errors.New("generated ingress current V2 upgrade source is invalid")
		}
		return gatewayCurrentUpgradeRouteOriginV2(selection.Upgrade.State, *selection.UpgradeSource)
	default:
		return gatewayCurrentRouteOriginV2{}, errors.New("generated ingress current V2 source kind is invalid")
	}
}

func gatewayCurrentV2MatchesSelectedOrigin(state gatewayCurrentRouteStateV2,
	selection gatewayCurrentSelection,
) bool {
	if state.Lineage != selection.Lineage {
		return false
	}
	switch selection.Kind {
	case gatewayCurrentSelectionRebind:
		if selection.State == nil {
			return false
		}
		source := *selection.State
		if state.Profile != source.Profile || !reflect.DeepEqual(state.Identity, source.Identity) ||
			state.Network != source.Network || state.TransferManifestDigest != source.TransferManifestDigest ||
			len(state.Apps) != len(source.Apps) {
			return false
		}
		for appID, app := range state.Apps {
			legacy, ok := source.Apps[appID]
			if !ok || !reflect.DeepEqual(app.LAN, legacy.LAN) {
				return false
			}
		}
		return true
	case gatewayCurrentSelectionUpgrade:
		if selection.Upgrade == nil || selection.UpgradeSource == nil || len(selection.Upgrade.State.Apps) != len(state.Apps) {
			return false
		}
		upgrade := selection.Upgrade.State
		if state.Profile != upgrade.Profile || state.Network != upgrade.Network || state.Identity.Kind != gatewayCurrentIdentityUpgrade ||
			state.Identity.Upgrade == nil || !reflect.DeepEqual(*state.Identity.Upgrade, upgrade.Identity) {
			return false
		}
		emptyManifest, err := appaccess.GatewayRebindTransferManifestDigest(nil)
		if err != nil || state.TransferManifestDigest != emptyManifest {
			return false
		}
		for appID, app := range state.Apps {
			source, ok := upgrade.Apps[appID]
			if !ok || (source.LAN == nil) != (app.LAN == nil) {
				return false
			}
			if source.LAN != nil && (app.LAN.Transfer != nil || app.LAN.Raw != *source.LAN) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func sameGatewayCurrentLegacySelection(left, right gatewayCurrentSelection) bool {
	return left.Kind == right.Kind && left.Lineage == right.Lineage &&
		reflect.DeepEqual(left.Upgrade, right.Upgrade) && reflect.DeepEqual(left.UpgradeSource, right.UpgradeSource) &&
		reflect.DeepEqual(left.Receipt, right.Receipt) && reflect.DeepEqual(left.Terminal, right.Terminal) &&
		reflect.DeepEqual(left.State, right.State)
}

// readGatewayCurrentProtectedSelectionLocked selects one explicit protected
// format while its caller holds the Manager and gateway OS locks. It performs
// only bounded reads and never grants effect permission.
func (m *Manager) readGatewayCurrentProtectedSelectionLocked(ctx context.Context) (
	gatewayCurrentProtectedSelection, appaccess.GatewayRebindRecoverySnapshot, error,
) {
	invalid := func() (gatewayCurrentProtectedSelection, appaccess.GatewayRebindRecoverySnapshot, error) {
		return gatewayCurrentProtectedSelection{}, appaccess.GatewayRebindRecoverySnapshot{},
			errors.New("generated ingress current protected selection failed")
	}
	if m == nil || m.store == nil || ctx == nil || ctx.Err() != nil || m.options.RebindCurrentStateRepository == nil {
		return invalid()
	}
	firstSQL, err := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || firstSQL.CurrentSource == nil {
		return invalid()
	}
	firstCensus, err := scanGatewayCurrentV2Census(m.store)
	if err != nil {
		return invalid()
	}
	legacy, err := m.selectGatewayCurrentCurrentV2AwareLocked(ctx, firstSQL)
	if err != nil {
		return invalid()
	}
	origin, err := gatewayCurrentExpectedOriginV2(legacy)
	if err != nil {
		return invalid()
	}
	if len(firstCensus.files) > 1 {
		return invalid()
	}

	var result gatewayCurrentProtectedSelection
	if len(firstCensus.files) == 0 {
		copy := legacy
		result.Legacy = &copy
	} else {
		name, _ := gatewayCurrentRouteStateNameV2(legacy.Lineage.ProtectedGeneration, legacy.Lineage.OperationID)
		entry, ok := firstCensus.files[name]
		if !ok {
			return invalid()
		}
		store, storeErr := newGatewayCurrentRouteStateV2StoreForOrigin(m.options.DataRoot, legacy.Lineage, origin)
		if storeErr != nil || filepath.Base(store.path) != name {
			return invalid()
		}
		state, loadErr := store.load()
		projection, projectErr := projectGatewayCurrentStateV2(state)
		config, renderErr := gatewayCurrentPhysicalConfigBytesV2(state)
		if loadErr != nil || projectErr != nil || renderErr != nil || len(config) == 0 ||
			!reflect.DeepEqual(state, entry.State) || !gatewayCurrentV2MatchesSelectedOrigin(state, legacy) {
			clear(config)
			return invalid()
		}
		clear(config)
		result.V2 = &gatewayCurrentV2Selection{Lineage: legacy.Lineage,
			Origin: cloneGatewayCurrentRouteOriginV2(origin), Source: legacy, Store: store,
			State: cloneGatewayCurrentRouteStateV2(state), Projection: projection}
	}

	secondSQL, sqlErr := m.options.RebindCurrentStateRepository.GatewayRebindRecoverySnapshot(ctx)
	confirmedLegacy, err := m.selectGatewayCurrentCurrentV2AwareLocked(ctx, firstSQL)
	secondCensus, censusErr := scanGatewayCurrentV2Census(m.store)
	if err != nil || sqlErr != nil || censusErr != nil || ctx.Err() != nil ||
		!sameGatewayCurrentLegacySelection(legacy, confirmedLegacy) || !reflect.DeepEqual(firstSQL, secondSQL) ||
		!sameGatewayCurrentV2Census(firstCensus, secondCensus) {
		return invalid()
	}
	return result, firstSQL, nil
}
