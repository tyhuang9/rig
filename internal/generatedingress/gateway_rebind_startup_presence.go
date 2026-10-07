package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
)

// GatewayRebindStartupRepository is the SQL half of the Docker-independent
// startup presence probe.
type GatewayRebindStartupRepository interface {
	GatewayRebindRecoverySnapshot(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)
}

// GatewayRebindStartupPresence distinguishes proved absence from a retained
// operation that requires a Docker-capable startup recovery path.
type GatewayRebindStartupPresence struct {
	Present                  bool
	ProtectedArtifacts       bool
	ActiveOperationID        string
	ActivePhase              appaccess.GatewayRebindState
	SelectedCurrentAuthority *appaccess.GatewayCurrentAuthorityRef
}

type gatewayRebindProtectedPresenceSnapshot struct {
	present        bool
	directoryPaths map[string]os.FileInfo
	files          map[string]gatewayHistoryFileFingerprint
}

// InspectGatewayRebindStartupPresence performs no Docker work and creates no
// protected directory. Any SQL rebind history makes Present true, including a
// committed operation with no Active claim. Protected rebind artifacts without
// retained SQL history are rejected rather than interpreted as absence. This
// result is an advisory startup gate; recovery revalidates the complete state.
func InspectGatewayRebindStartupPresence(ctx context.Context, dataRoot string,
	repository GatewayRebindStartupRepository,
) (GatewayRebindStartupPresence, error) {
	if ctx == nil || repository == nil {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticValidationFailed}
	}
	firstSQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	firstProtected, err := readGatewayRebindProtectedPresenceReadOnly(dataRoot)
	if err != nil || ctx.Err() != nil {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	secondSQL, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(firstSQL, secondSQL) {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	secondProtected, err := readGatewayRebindProtectedPresenceReadOnly(dataRoot)
	if err != nil || ctx.Err() != nil || !sameGatewayRebindProtectedPresence(firstProtected, secondProtected) {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	snapshot := firstSQL
	protectedPresent := firstProtected.present
	sqlPresent := len(snapshot.History) != 0
	if !sqlPresent {
		if snapshot.Active != nil || snapshot.Phase != "" || snapshot.DatabaseCommittedEvent != nil ||
			snapshot.CurrentDatabaseCommittedEvent != nil ||
			len(snapshot.CurrentTransfers) != 0 || snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed ||
			protectedPresent {
			return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
		}
		if snapshot.CurrentSource != nil && !validGatewayRebindNativeStartupAuthority(snapshot) {
			return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
		}
		return GatewayRebindStartupPresence{}, nil
	}
	if snapshot.CurrentProfile == nil || snapshot.CurrentSource == nil {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	current := *snapshot.CurrentSource
	result := GatewayRebindStartupPresence{Present: true, ProtectedArtifacts: protectedPresent,
		SelectedCurrentAuthority: &current}
	if snapshot.Active != nil {
		operationID, operationErr := gatewayRebindHistoryOperationID(*snapshot.Active)
		if operationErr != nil || snapshot.Phase == "" {
			return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
		}
		result.ActiveOperationID, result.ActivePhase = operationID, snapshot.Phase
	} else if snapshot.Phase != "" || snapshot.DatabaseCommittedEvent != nil ||
		snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed {
		return GatewayRebindStartupPresence{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	return result, nil
}

func validGatewayRebindNativeStartupAuthority(snapshot appaccess.GatewayRebindRecoverySnapshot) bool {
	if snapshot.CurrentSource == nil || snapshot.CurrentProfile == nil ||
		snapshot.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		snapshot.CurrentSource.TerminalReceiptDigest != "" ||
		!validCanonicalUUID(snapshot.CurrentSource.OperationID) ||
		!gatewayCurrentProfileMatchesAuthority(snapshot.CurrentProfile, *snapshot.CurrentSource) {
		return false
	}
	profile := snapshot.CurrentProfile
	return validGatewayProfileBinding(gatewayProfileBinding{
		RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
		SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
		PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd,
	})
}

func readGatewayRebindProtectedPresenceReadOnly(dataRoot string) (gatewayRebindProtectedPresenceSnapshot, error) {
	return readGatewayRebindProtectedPresenceMode(dataRoot, true)
}

// Emergency ownership enumeration fingerprints native route files without
// trusting their JSON. Only an independently journal-bound native stop may
// follow a complete absence of rebind ownership; serving reads stay strict.
func readGatewayRebindProtectedPresenceMode(dataRoot string, validateNativeRoute bool) (gatewayRebindProtectedPresenceSnapshot, error) {
	return readGatewayRebindProtectedPresenceInspection(dataRoot, validateNativeRoute, true)
}

// Only terminal-owned emergency withdrawal may fingerprint current route
// bytes without decoding them. Normal presence and serving readers stay strict.
func readGatewayRebindProtectedPresenceForTerminalWithdrawal(dataRoot string) (gatewayRebindProtectedPresenceSnapshot, error) {
	return readGatewayRebindProtectedPresenceInspection(dataRoot, false, false)
}

func readGatewayRebindProtectedPresenceInspection(dataRoot string, validateNativeRoute, validateCurrentRoute bool) (gatewayRebindProtectedPresenceSnapshot, error) {
	store, directoryPresent, pathIdentities, err := inspectStateStoreReadOnly(dataRoot)
	result := gatewayRebindProtectedPresenceSnapshot{directoryPaths: pathIdentities,
		files: make(map[string]gatewayHistoryFileFingerprint)}
	if err != nil || !directoryPresent {
		return result, err
	}
	before, err := store.directoryIdentity()
	if err != nil {
		return gatewayRebindProtectedPresenceSnapshot{}, err
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return gatewayRebindProtectedPresenceSnapshot{}, err
	}
	for _, entry := range entries {
		generation, operationID, sequence, kind, relevant, parseErr := parseGatewayHistoryArtifactName(entry.Name())
		currentGeneration, currentOperation, currentRelevant, currentErr := parseGatewayCurrentRoutePresenceName(entry.Name())
		if parseErr != nil || currentErr != nil {
			return gatewayRebindProtectedPresenceSnapshot{}, errors.New("generated ingress rebind presence filename is invalid")
		}
		isRebind := relevant && (kind == gatewayHistoryRebindIntent || kind == gatewayHistoryRebindProgress ||
			kind == gatewayHistoryRebindTerminal || kind == gatewayHistoryRebindPredecessorCheckpoint)
		if !relevant && !currentRelevant && entry.Name() == stateFilename {
			if entry.IsDir() {
				return gatewayRebindProtectedPresenceSnapshot{}, errors.New("generated ingress route state is unsafe")
			}
			fingerprint, fingerprintErr := fingerprintGatewayHistoryArtifactBound(
				filepath.Join(store.root, entry.Name()), maxGatewayCurrentRouteStateBytes)
			if fingerprintErr != nil {
				return gatewayRebindProtectedPresenceSnapshot{}, fingerprintErr
			}
			if validateNativeRoute {
				if _, loadErr := store.load(); loadErr != nil {
					return gatewayRebindProtectedPresenceSnapshot{}, loadErr
				}
			}
			result.files[entry.Name()] = fingerprint
			continue
		}
		if !relevant && !currentRelevant && entry.Name() == gatewayLockFilename {
			fingerprint, fingerprintErr := fingerprintGatewayPresenceLock(filepath.Join(store.root, entry.Name()))
			if fingerprintErr != nil {
				return gatewayRebindProtectedPresenceSnapshot{}, fingerprintErr
			}
			result.files[entry.Name()] = fingerprint
			continue
		}
		if !relevant && !currentRelevant {
			return gatewayRebindProtectedPresenceSnapshot{}, errors.New("generated ingress presence found an unknown artifact")
		}
		path := filepath.Join(store.root, entry.Name())
		fingerprint, fingerprintErr := fingerprintGatewayHistoryArtifactBound(path, maxGatewayCurrentRouteStateBytes)
		if fingerprintErr != nil {
			return gatewayRebindProtectedPresenceSnapshot{}, fingerprintErr
		}
		result.files[entry.Name()] = fingerprint
		if !isRebind && !currentRelevant {
			continue
		}
		if currentRelevant && validateCurrentRoute {
			currentStore := &gatewayCurrentRouteStateStore{directory: store, dataRoot: dataRoot,
				generation: currentGeneration, operationID: currentOperation, path: path}
			_, currentStore.purpose = gatewayCurrentRouteStateName(currentGeneration, currentOperation)
			if _, loadErr := currentStore.load(); loadErr != nil {
				return gatewayRebindProtectedPresenceSnapshot{}, loadErr
			}
		} else if !currentRelevant {
			if validateErr := validateGatewayRebindPresenceArtifact(store, path, entry.Name(), generation,
				operationID, sequence, kind); validateErr != nil {
				return gatewayRebindProtectedPresenceSnapshot{}, validateErr
			}
		}
		result.present = true
	}
	if store.sameDirectory(before) != nil {
		return gatewayRebindProtectedPresenceSnapshot{}, errors.New("generated ingress rebind presence directory changed")
	}
	return result, nil
}

func fingerprintGatewayPresenceLock(path string) (gatewayHistoryFileFingerprint, error) {
	file, err := os.Open(path)
	if err != nil {
		return gatewayHistoryFileFingerprint{}, errors.New("generated ingress gateway lock is unreadable")
	}
	defer file.Close()
	if err := validateGatewayLockFile(file, path); err != nil {
		return gatewayHistoryFileFingerprint{}, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 0 {
		return gatewayHistoryFileFingerprint{}, errors.New("generated ingress gateway lock is invalid")
	}
	return gatewayHistoryFileFingerprint{info: info, size: info.Size(), mode: info.Mode(),
		modTime: info.ModTime().UnixNano()}, nil
}

func validateGatewayRebindPresenceArtifact(directory *stateStore, path, name string, generation uint64,
	operationID string, sequence uint64, kind gatewayHistoryArtifactKind,
) error {
	switch kind {
	case gatewayHistoryRebindPredecessorCheckpoint:
		store := &gatewayRebindPredecessorCheckpointStore{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindPredecessorCheckpointName(generation, operationID)
		_, err := store.load()
		return err
	case gatewayHistoryRebindIntent:
		if strings.HasPrefix(name, gatewayRebindProtectedIntentFilenamePrefixV2) {
			store := &gatewayRebindProtectedIntentV2Store{directory: directory, generation: generation,
				operationID: operationID, path: path}
			_, store.purpose = gatewayRebindProtectedIntentV2Name(generation, operationID)
			_, err := store.load()
			return err
		}
		store := &gatewayRebindProtectedIntentStore{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindProtectedIntentName(generation, operationID)
		_, err := store.load()
		return err
	case gatewayHistoryRebindProgress:
		store := &gatewayRebindProgressStore{directory: directory, generation: generation,
			operationID: operationID, sequence: sequence, path: path}
		_, store.purpose = gatewayRebindProgressName(generation, operationID, sequence)
		_, err := store.load()
		return err
	case gatewayHistoryRebindTerminal:
		if strings.HasPrefix(name, gatewayRebindTerminalFilenamePrefixV2) {
			store := &gatewayRebindTerminalStoreV2{directory: directory, generation: generation,
				operationID: operationID, path: path}
			_, store.purpose = gatewayRebindTerminalNameV2(generation, operationID)
			_, err := store.load()
			return err
		}
		store := &gatewayRebindFinalHandoverTerminalStore{directory: directory, generation: generation,
			operationID: operationID, path: path}
		_, store.purpose = gatewayRebindFinalHandoverTerminalName(generation, operationID)
		_, err := store.load()
		return err
	default:
		return errors.New("generated ingress rebind presence artifact kind is invalid")
	}
}

func parseGatewayCurrentRoutePresenceName(name string) (uint64, string, bool, error) {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, strings.ToLower(gatewayCurrentRouteStateFilenamePrefix)) {
		return 0, "", false, nil
	}
	if !strings.HasPrefix(name, gatewayCurrentRouteStateFilenamePrefix) || !strings.HasSuffix(name, ".bundle") {
		return 0, "", true, errors.New("generated ingress current route filename is invalid")
	}
	tail := strings.TrimSuffix(strings.TrimPrefix(name, gatewayCurrentRouteStateFilenamePrefix), ".bundle")
	parts := strings.Split(tail, ".")
	if len(parts) != 2 || len(parts[0]) != gatewayV2GenerationDigits || !validCanonicalUUID(parts[1]) {
		return 0, "", true, errors.New("generated ingress current route filename is invalid")
	}
	generation, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || generation == 0 || generation > math.MaxInt64 ||
		fmt.Sprintf("%0*d", gatewayV2GenerationDigits, generation) != parts[0] {
		return 0, "", true, errors.New("generated ingress current route filename is invalid")
	}
	return generation, parts[1], true, nil
}

func sameGatewayRebindProtectedPresence(left, right gatewayRebindProtectedPresenceSnapshot) bool {
	if left.present != right.present || len(left.directoryPaths) != len(right.directoryPaths) ||
		len(left.files) != len(right.files) {
		return false
	}
	for path, leftInfo := range left.directoryPaths {
		rightInfo, ok := right.directoryPaths[path]
		if !ok || !os.SameFile(leftInfo, rightInfo) || leftInfo.Mode() != rightInfo.Mode() {
			return false
		}
	}
	names := make([]string, 0, len(left.files))
	for name := range left.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		leftValue := left.files[name]
		rightValue, ok := right.files[name]
		if !ok || !os.SameFile(leftValue.info, rightValue.info) || leftValue.size != rightValue.size ||
			leftValue.mode != rightValue.mode || leftValue.modTime != rightValue.modTime ||
			leftValue.digest != rightValue.digest {
			return false
		}
	}
	return true
}
