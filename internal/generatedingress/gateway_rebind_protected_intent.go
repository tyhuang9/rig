package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayRebindProtectedIntentVersion        = 1
	gatewayRebindProtectedIntentPurpose        = "hostd/generated-ingress/rebind/successor-intent/v1"
	gatewayRebindProtectedIntentFilenamePrefix = "gateway-rebind-successor-intent.v1.g"
	maxGatewayRebindProtectedIntentBytes       = 64 << 10
)

// gatewayRebindProtectedIntent is immutable preparation evidence. Installing
// it does not authorize Docker, network, SQLite, or routing effects. A future
// coordinator must hold the gateway writer lock and deployment-effects lease,
// repeat every protected/SQLite/network/Docker attestation, and then use the
// exact readback before creating a successor resource.
type gatewayRebindProtectedIntent struct {
	Version     int    `json:"version"`
	Purpose     string `json:"purpose"`
	Generation  uint64 `json:"generation"`
	OperationID string `json:"operationId"`
	// DatabaseDigest detects drift against the exact snapshot used during
	// construction. A protected-only reader can validate its syntax but cannot
	// prove the current SQLite head without a fresh cross-store read.
	DatabaseDigest           string                                   `json:"databaseDigest"`
	Intent                   gatewayRebindSuccessorIntent             `json:"intent"`
	NetworkObservation       gatewayRebindSuccessorNetworkObservation `json:"networkObservation"`
	NetworkObservationDigest string                                   `json:"networkObservationDigest"`
	Digest                   string                                   `json:"digest"`
}

// gatewayRebindSuccessorNetworkObservation is a canonical projection of the
// private, stable observation used to select the successor plan. Slice fields
// are sorted copies, so equivalent set ordering produces the same digest. The
// current preflight observes a sorted Docker-ID list and aggregate prefixes;
// it does not retain a per-ID prefix map. This type intentionally remains
// private because the public preflight is advisory and exposes only its plan.
type gatewayRebindSuccessorNetworkObservation struct {
	Version            int                                      `json:"version"`
	OperationID        string                                   `json:"operationId"`
	ClaimRequestDigest string                                   `json:"claimRequestDigest"`
	ProfileSpecDigest  string                                   `json:"profileSpecDigest"`
	Candidates         []gatewayRebindSuccessorNetworkCandidate `json:"candidates"`
	HostRoutes         []string                                 `json:"hostRoutes"`
	HostInterfaces     []string                                 `json:"hostInterfaces"`
	DockerNetworkIDs   []string                                 `json:"dockerNetworkIds"`
	DockerPrefixes     []string                                 `json:"dockerPrefixes"`
	Plan               gatewayRebindSuccessorIntentNetwork      `json:"plan"`
}

type gatewayRebindSuccessorNetworkCandidate struct {
	InterfaceID string `json:"interfaceId"`
	IPv4        string `json:"ipv4"`
	Prefix      string `json:"prefix"`
}

type gatewayRebindProtectedIntentStore struct {
	directory   *stateStore
	generation  uint64
	operationID string
	path        string
	purpose     string
}

type gatewayRebindProtectedIntentSelection struct {
	Store      *gatewayRebindProtectedIntentStore
	Generation uint64
	Intent     gatewayRebindProtectedIntent
	Existing   bool
}

type gatewayRebindProtectedIntentHistory struct {
	Predecessor gatewayUpgradeGenerationSelection
	Source      routeState
	Checkpoints []gatewayRebindPredecessorCheckpointSelection
	Intents     []gatewayRebindProtectedIntentSelection
	IntentsV2   []gatewayRebindProtectedIntentV2Selection
	Progress    []gatewayRebindProgressSelection
	Terminals   []gatewayRebindFinalHandoverTerminalSelection
}

type gatewayRebindPredecessorCheckpointSelection struct {
	Store      *gatewayRebindPredecessorCheckpointStore
	Generation uint64
	Checkpoint gatewayRebindPredecessorCheckpoint
	Existing   bool
}

func newGatewayRebindProtectedIntent(snapshot appaccess.GatewayRebindStartupSnapshot,
	predecessor gatewayUpgradeGenerationSelection,
	observation gatewayRebindSuccessorPreflightObservation,
	generation uint64,
) (gatewayRebindProtectedIntent, error) {
	invalid := errors.New("invalid generated ingress rebind protected intent input")
	if !reflect.DeepEqual(snapshot, observation.database) {
		return gatewayRebindProtectedIntent{}, invalid
	}
	intent, err := newGatewayRebindInitialSuccessorIntentAtGeneration(snapshot, predecessor, observation.result, generation)
	if err != nil {
		return gatewayRebindProtectedIntent{}, invalid
	}
	network, err := newGatewayRebindSuccessorNetworkObservation(observation)
	if err != nil || !gatewayRebindNetworkObservationMatchesIntent(network, intent) {
		return gatewayRebindProtectedIntent{}, invalid
	}
	databaseDigest, err := canonicalDigest(snapshot)
	if err != nil {
		return gatewayRebindProtectedIntent{}, invalid
	}
	networkDigest, err := canonicalDigest(network)
	if err != nil {
		return gatewayRebindProtectedIntent{}, invalid
	}
	protected := gatewayRebindProtectedIntent{
		Version: gatewayRebindProtectedIntentVersion, Purpose: gatewayRebindProtectedIntentPurpose,
		Generation: generation, OperationID: intent.Claim.OperationID,
		DatabaseDigest: databaseDigest, Intent: intent,
		NetworkObservation: network, NetworkObservationDigest: networkDigest,
	}
	protected.Digest, err = gatewayRebindProtectedIntentDigest(protected)
	if err != nil || !validGatewayRebindProtectedIntent(protected) {
		return gatewayRebindProtectedIntent{}, invalid
	}
	return protected, nil
}

func newGatewayRebindSuccessorNetworkObservation(value gatewayRebindSuccessorPreflightObservation) (gatewayRebindSuccessorNetworkObservation, error) {
	candidates := make([]gatewayRebindSuccessorNetworkCandidate, 0, len(value.candidates))
	for _, candidate := range value.candidates {
		if candidate.InterfaceID == "" || candidate.IPv4 == "" || !candidate.Prefix.IsValid() ||
			!candidate.Prefix.Addr().Is4() || candidate.Prefix != candidate.Prefix.Masked() {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid generated ingress rebind network observation")
		}
		address, err := netip.ParseAddr(candidate.IPv4)
		if err != nil || !address.Is4() || !candidate.Prefix.Contains(address) {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid generated ingress rebind network observation")
		}
		candidates = append(candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: candidate.InterfaceID, IPv4: candidate.IPv4, Prefix: candidate.Prefix.String(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].InterfaceID != candidates[j].InterfaceID {
			return candidates[i].InterfaceID < candidates[j].InterfaceID
		}
		if candidates[i].IPv4 != candidates[j].IPv4 {
			return candidates[i].IPv4 < candidates[j].IPv4
		}
		return candidates[i].Prefix < candidates[j].Prefix
	})
	for index := 1; index < len(candidates); index++ {
		if candidates[index] == candidates[index-1] {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid generated ingress rebind network observation")
		}
	}
	hostRoutes, err := canonicalGatewayRebindPrefixes(value.host.Routes)
	if err != nil {
		return gatewayRebindSuccessorNetworkObservation{}, err
	}
	hostInterfaces, err := canonicalGatewayRebindPrefixes(value.host.Interfaces)
	if err != nil {
		return gatewayRebindSuccessorNetworkObservation{}, err
	}
	dockerPrefixes, err := canonicalGatewayRebindPrefixes(value.docker)
	if err != nil || !validGatewayRebindSuccessorDockerIDs(value.dockerIDs) {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid generated ingress rebind network observation")
	}
	return gatewayRebindSuccessorNetworkObservation{
		Version:     gatewayRebindProtectedIntentVersion,
		OperationID: value.result.RebindOperationID, ClaimRequestDigest: value.result.ClaimRequestDigest,
		ProfileSpecDigest: value.result.Profile.SpecDigest,
		Candidates:        candidates, HostRoutes: hostRoutes, HostInterfaces: hostInterfaces,
		DockerNetworkIDs: append([]string{}, value.dockerIDs...), DockerPrefixes: dockerPrefixes,
		Plan: gatewayRebindSuccessorIntentNetwork(value.result.Network),
	}, nil
}

func canonicalGatewayRebindPrefixes(values []netip.Prefix) ([]string, error) {
	if !validGatewayV2PrefixInventory(values) {
		return nil, errors.New("invalid generated ingress rebind network observation")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.String())
	}
	sort.Strings(result)
	return result, nil
}

func parseGatewayRebindPrefixes(values []string) ([]netip.Prefix, bool) {
	result := make([]netip.Prefix, 0, len(values))
	for index, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.String() != value ||
			(index > 0 && values[index-1] > value) {
			return nil, false
		}
		result = append(result, prefix)
	}
	return result, true
}

func gatewayRebindNetworkObservationMatchesIntent(observation gatewayRebindSuccessorNetworkObservation,
	intent gatewayRebindSuccessorIntent,
) bool {
	if observation.Version != gatewayRebindProtectedIntentVersion ||
		observation.OperationID != intent.Claim.OperationID ||
		observation.ClaimRequestDigest != intent.Claim.RequestDigest ||
		observation.ProfileSpecDigest != intent.SuccessorProfile.SpecDigest ||
		observation.Plan != intent.Network || !validGatewayRebindSuccessorDockerIDs(observation.DockerNetworkIDs) {
		return false
	}
	candidates := make([]hostNetworkCandidate, 0, len(observation.Candidates))
	for index, candidate := range observation.Candidates {
		prefix, err := netip.ParsePrefix(candidate.Prefix)
		address, addressErr := netip.ParseAddr(candidate.IPv4)
		if candidate.InterfaceID == "" || err != nil || addressErr != nil || !address.Is4() ||
			prefix.String() != candidate.Prefix || !prefix.Contains(address) ||
			(index > 0 && !gatewayRebindCandidateLess(observation.Candidates[index-1], candidate)) {
			return false
		}
		candidates = append(candidates, hostNetworkCandidate{
			InterfaceID: candidate.InterfaceID, IPv4: candidate.IPv4, Prefix: prefix,
		})
	}
	hostRoutes, ok := parseGatewayRebindPrefixes(observation.HostRoutes)
	if !ok {
		return false
	}
	hostInterfaces, ok := parseGatewayRebindPrefixes(observation.HostInterfaces)
	if !ok {
		return false
	}
	docker, ok := parseGatewayRebindPrefixes(observation.DockerPrefixes)
	if !ok {
		return false
	}
	profile := gatewayProfileBinding{
		RevisionID: intent.SuccessorProfile.RevisionID, RevisionNumber: intent.SuccessorProfile.RevisionNumber,
		SpecDigest: intent.SuccessorProfile.SpecDigest, SelectedIPv4: intent.SuccessorProfile.SelectedIPv4,
		InterfaceID: intent.SuccessorProfile.InterfaceID, PortStart: intent.SuccessorProfile.PortStart,
		PortEnd: intent.SuccessorProfile.PortEnd,
	}
	plan, err := selectGatewayV2NetworkPlan(context.Background(), profile, gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return candidates, nil },
		host: func() (gatewayV2HostNetworkSnapshot, error) {
			return gatewayV2HostNetworkSnapshot{Routes: hostRoutes, Interfaces: hostInterfaces}, nil
		},
		docker: func(context.Context) ([]netip.Prefix, error) { return docker, nil },
	})
	return err == nil && gatewayRebindSuccessorIntentNetwork(plan) == observation.Plan
}

func gatewayRebindCandidateLess(left, right gatewayRebindSuccessorNetworkCandidate) bool {
	if left.InterfaceID != right.InterfaceID {
		return left.InterfaceID < right.InterfaceID
	}
	if left.IPv4 != right.IPv4 {
		return left.IPv4 < right.IPv4
	}
	return left.Prefix < right.Prefix
}

func validGatewayRebindProtectedIntent(value gatewayRebindProtectedIntent) bool {
	if value.Version != gatewayRebindProtectedIntentVersion || value.Purpose != gatewayRebindProtectedIntentPurpose ||
		value.Generation == 0 || !validCanonicalUUID(value.OperationID) || !validSHA256(value.DatabaseDigest) ||
		value.Intent.Version != gatewayRebindSuccessorIntentVersion || value.Intent.Claim.OperationID != value.OperationID ||
		value.Intent.Identity.Generation != value.Generation || value.Intent.Identity.OperationID != value.OperationID ||
		value.Intent.Predecessor.Kind != gatewayRebindInitialSourceKind || value.Intent.Predecessor.Generation >= value.Generation ||
		!validGatewayRebindStoredSuccessorIntent(value.Intent) ||
		!gatewayRebindNetworkObservationMatchesIntent(value.NetworkObservation, value.Intent) {
		return false
	}
	networkDigest, err := canonicalDigest(value.NetworkObservation)
	if err != nil || networkDigest != value.NetworkObservationDigest {
		return false
	}
	digest, err := gatewayRebindProtectedIntentDigest(value)
	return err == nil && digest == value.Digest
}

func validGatewayRebindStoredSuccessorIntent(intent gatewayRebindSuccessorIntent) bool {
	if !validSHA256(intent.Digest) || !validSHA256(intent.Claim.RequestDigest) ||
		!validSHA256(intent.Claim.SpecDigest) || !validSHA256(intent.Claim.SuccessorProfileRequestDigest) ||
		!validSHA256(intent.Claim.RosterDigest) || intent.Claim.RosterCount != int64(len(intent.Roster)) ||
		len(intent.Roster) != len(intent.RosterEntryDigests) ||
		intent.Claim.RebindApproval.Action != appaccess.ActionRebindGateway ||
		intent.Claim.ConfigureApproval.Action != appaccess.ActionConfigureGateway ||
		!validCanonicalUUID(intent.Claim.RebindApproval.ActorID) ||
		!validCanonicalUUID(intent.Claim.ConfigureApproval.ActorID) ||
		!validSHA256(intent.Claim.RebindApproval.SpecDigest) ||
		!validSHA256(intent.Claim.ConfigureApproval.SpecDigest) ||
		intent.Claim.RebindApproval.SpecDigest != intent.Claim.SpecDigest ||
		intent.Claim.ConfigureApproval.SpecDigest != intent.SuccessorProfile.SpecDigest ||
		intent.SuccessorProfile.ApprovedBy != intent.Claim.ConfigureApproval.ActorID ||
		intent.SuccessorProfile.RequestDigest != intent.Claim.SuccessorProfileRequestDigest ||
		!validGatewayProfileBinding(intent.Predecessor.Profile) ||
		!validCanonicalUUID(intent.Predecessor.OperationID) ||
		!validSHA256(intent.Predecessor.StateDigest) || !validSHA256(intent.Predecessor.JournalDigest) ||
		!validSHA256(intent.Predecessor.IdentityDigest) ||
		!validGatewayV2ResourceBindings(gatewayPhaseCommitted, intent.Predecessor.Resources) {
		return false
	}
	profile := GatewayRebindSuccessorProfile{
		RevisionID: intent.SuccessorProfile.RevisionID, RevisionNumber: intent.SuccessorProfile.RevisionNumber,
		OperationID: intent.SuccessorProfile.OperationID, RequestDigest: intent.SuccessorProfile.RequestDigest,
		SpecDigest: intent.SuccessorProfile.SpecDigest, SelectedIPv4: intent.SuccessorProfile.SelectedIPv4,
		InterfaceID: intent.SuccessorProfile.InterfaceID, PortStart: intent.SuccessorProfile.PortStart,
		PortEnd: intent.SuccessorProfile.PortEnd, ApprovedBy: intent.SuccessorProfile.ApprovedBy,
	}
	identity, err := newGatewayRebindSuccessorIdentity(intent.Identity.Generation, intent.Claim.OperationID, profile)
	if err != nil || !reflect.DeepEqual(identity, intent.Identity) {
		return false
	}
	for index, entry := range intent.Roster {
		digest, digestErr := appaccess.GatewayRebindRosterEntryDigest(entry)
		if digestErr != nil || entry.Ordinal != int64(index+1) || entry.OperationID != intent.Claim.OperationID ||
			entry.EntryDigest != digest || intent.RosterEntryDigests[index] != digest {
			return false
		}
	}
	rosterDigest, err := appaccess.GatewayRebindRosterDigest(intent.Roster)
	if err != nil || rosterDigest != intent.Claim.RosterDigest {
		return false
	}
	networkDigest, err := canonicalDigest(struct {
		Version int                                 `json:"version"`
		Action  string                              `json:"action"`
		Plan    gatewayRebindSuccessorIntentNetwork `json:"plan"`
	}{1, "rebind-successor-network", intent.Network})
	if err != nil || networkDigest != intent.NetworkDigest {
		return false
	}
	digest, err := gatewayRebindInitialSuccessorIntentDigest(intent)
	return err == nil && digest == intent.Digest
}

func gatewayRebindProtectedIntentDigest(value gatewayRebindProtectedIntent) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func newGatewayRebindProtectedIntentStore(dataRoot string, generation uint64, operationID string) (*gatewayRebindProtectedIntentStore, error) {
	if generation == 0 || !validCanonicalUUID(operationID) {
		return nil, errors.New("invalid generated ingress rebind protected intent store")
	}
	directory, err := newStateStore(dataRoot)
	if err != nil {
		return nil, err
	}
	name, purpose := gatewayRebindProtectedIntentName(generation, operationID)
	return &gatewayRebindProtectedIntentStore{
		directory: directory, generation: generation, operationID: operationID,
		path: filepath.Join(directory.root, name), purpose: purpose,
	}, nil
}

func gatewayRebindProtectedIntentName(generation uint64, operationID string) (string, string) {
	token := fmt.Sprintf("%0*d.%s", gatewayV2GenerationDigits, generation, operationID)
	scope := fmt.Sprintf("generation/%0*d/%s", gatewayV2GenerationDigits, generation, operationID)
	return gatewayRebindProtectedIntentFilenamePrefix + token + ".bundle", gatewayRebindProtectedIntentPurpose + "/" + scope
}

// installExact is deliberately not a Manager method or public effect path. It
// provides create-only storage and exact readback for a future coordinator
// that has already satisfied the lock, lease, and fresh-attestation contract.
// A post-install durability error remains an error; recovery must reacquire
// those guards, repeat all observations, and invoke a fresh exact replay.
func (s *gatewayRebindProtectedIntentStore) installExact(value gatewayRebindProtectedIntent) error {
	if s == nil || s.directory == nil || s.generation != value.Generation || s.operationID != value.OperationID ||
		!validGatewayRebindProtectedIntent(value) {
		return errors.New("invalid generated ingress rebind protected intent install")
	}
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.writeExact(s.path, s.purpose, value, true, maxGatewayRebindProtectedIntentBytes); err != nil {
		return err
	}
	installed, err := s.load()
	if err != nil {
		return fmt.Errorf("generated ingress rebind protected intent readback failed: %w", err)
	}
	if !reflect.DeepEqual(installed, value) {
		return errors.New("generated ingress rebind protected intent readback mismatch")
	}
	return nil
}

func (s *gatewayRebindProtectedIntentStore) load() (gatewayRebindProtectedIntent, error) {
	if s == nil || s.directory == nil {
		return gatewayRebindProtectedIntent{}, errors.New("invalid generated ingress rebind protected intent store")
	}
	var value gatewayRebindProtectedIntent
	state := gatewayUpgradeStateStore{directory: s.directory}
	if err := state.readStrict(s.path, s.purpose, maxGatewayRebindProtectedIntentBytes, &value); err != nil {
		return gatewayRebindProtectedIntent{}, errors.New("invalid generated ingress rebind protected intent")
	}
	// EntryDigest is deliberately excluded from the shared roster JSON shape.
	// Rehydrate it only from the separately persisted ordered digest vector;
	// full validation below recomputes every digest before accepting the value.
	if len(value.Intent.Roster) != len(value.Intent.RosterEntryDigests) {
		return gatewayRebindProtectedIntent{}, errors.New("invalid generated ingress rebind protected intent")
	}
	for index := range value.Intent.Roster {
		value.Intent.Roster[index].EntryDigest = value.Intent.RosterEntryDigests[index]
	}
	if value.Generation != s.generation || value.OperationID != s.operationID || !validGatewayRebindProtectedIntent(value) {
		return gatewayRebindProtectedIntent{}, errors.New("invalid generated ingress rebind protected intent")
	}
	return value, nil
}

// resolveGatewayRebindProtectedIntentLocked reserves only the first generation
// after the committed fixed-name predecessor, or returns an exact replay of
// the sole installed intent. Any different rebind operation remains blocked
// until a later slice supplies typed, validated abort/rollback terminal
// receipts. This prevents an unresolved intent from being treated as retired.
func (m *Manager) resolveGatewayRebindProtectedIntentLocked(operationID string) (gatewayRebindProtectedIntentSelection, error) {
	if !validCanonicalUUID(operationID) {
		return gatewayRebindProtectedIntentSelection{}, errors.New("invalid generated ingress rebind operation")
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return gatewayRebindProtectedIntentSelection{}, err
	}
	if len(history.Intents) != 0 || len(history.IntentsV2) != 0 {
		if len(history.IntentsV2) != 0 {
			return gatewayRebindProtectedIntentSelection{}, errors.New("generated ingress rebind intent history is unresolved")
		}
		latest := history.Intents[len(history.Intents)-1]
		if latest.Intent.OperationID == operationID {
			return latest, nil
		}
		return gatewayRebindProtectedIntentSelection{}, errors.New("generated ingress rebind intent history is unresolved")
	}
	if history.Predecessor.Generation == math.MaxUint64 {
		return gatewayRebindProtectedIntentSelection{}, errors.New("generated ingress rebind generation is exhausted")
	}
	generation := history.Predecessor.Generation + 1
	store, err := newGatewayRebindProtectedIntentStore(m.options.DataRoot, generation, operationID)
	if err != nil {
		return gatewayRebindProtectedIntentSelection{}, err
	}
	return gatewayRebindProtectedIntentSelection{Store: store, Generation: generation}, nil
}

// scanGatewayRebindProtectedIntentHistoryLocked validates protected history
// only. It cannot prove that DatabaseDigest still names the current SQLite
// claim; future effect and recovery callers must perform that cross-store
// comparison while holding their required lock and lease.
func (m *Manager) scanGatewayRebindProtectedIntentHistoryLocked(checkpoint func()) (gatewayRebindProtectedIntentHistory, error) {
	upgrade, before, err := m.scanGatewayUpgradeHistoryLockedModeRebindAware(false, "")
	if err != nil || !upgrade.committed || len(upgrade.generations) == 0 {
		return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind predecessor history is invalid")
	}
	predecessor := upgrade.generations[len(upgrade.generations)-1]
	if predecessor.Generation == math.MaxUint64 {
		return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind generation is exhausted")
	}
	generations := make([]uint64, 0, len(before.rebindIntents))
	for generation := range before.rebindIntents {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	source, err := m.store.load()
	if err != nil {
		return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind predecessor source is invalid")
	}
	result := gatewayRebindProtectedIntentHistory{Predecessor: predecessor, Source: source}
	seenOperations := make(map[string]struct{}, len(upgrade.generations)+len(generations))
	for _, selection := range upgrade.generations {
		seenOperations[selection.operationID] = struct{}{}
	}
	for index, generation := range generations {
		expected := predecessor.Generation + 1 + uint64(index)
		if generation != expected {
			return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind intent history has a generation gap")
		}
		artifact := before.rebindIntents[generation]
		if _, duplicate := seenOperations[artifact.operationID]; duplicate {
			return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind operation is duplicated across generations")
		}
		seenOperations[artifact.operationID] = struct{}{}
		artifactPredecessor := predecessor
		var installedCheckpoint *gatewayRebindPredecessorCheckpoint
		if artifact.checkpoint.path != "" {
			checkpointStore, checkpointStoreErr := newGatewayRebindPredecessorCheckpointStore(
				m.options.DataRoot, generation, artifact.operationID)
			if checkpointStoreErr != nil || checkpointStore.path != artifact.checkpoint.path {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind predecessor checkpoint path is invalid")
			}
			checkpoint, checkpointErr := checkpointStore.load()
			if checkpointErr != nil || !gatewayRebindCheckpointMatchesProtectedHistory(checkpoint, predecessor, result) {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind predecessor checkpoint history is invalid")
			}
			// The checkpoint freezes the exact admission source. Later ordinary
			// route writes may mutate the live upgrade state without changing its
			// stable origin, so all retained intent/progress/terminal evidence is
			// checked against these frozen bytes rather than today's route bytes.
			if checkpoint.UpgradeState != nil {
				artifactPredecessor.State = cloneGatewayV2RouteState(*checkpoint.UpgradeState)
			}
			checkpointCopy := checkpoint
			installedCheckpoint = &checkpointCopy
			result.Checkpoints = append(result.Checkpoints, gatewayRebindPredecessorCheckpointSelection{
				Store: checkpointStore, Generation: generation, Checkpoint: checkpoint, Existing: true,
			})
		}
		if artifact.intent.path == "" {
			if artifact.checkpoint.path == "" || index != len(generations)-1 || len(artifact.progress) != 0 || artifact.terminal.path != "" {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind predecessor checkpoint is incomplete")
			}
			continue
		}
		if strings.HasPrefix(filepath.Base(artifact.intent.path), gatewayRebindProtectedIntentFilenamePrefixV2) {
			if installedCheckpoint == nil || artifact.terminal.path != "" {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress typed rebind intent history is incomplete")
			}
			store, storeErr := newGatewayRebindProtectedIntentV2Store(m.options.DataRoot, generation, artifact.operationID)
			if storeErr != nil || store.path != artifact.intent.path {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress typed rebind intent history path is invalid")
			}
			intent, loadErr := store.load()
			if loadErr != nil || intent.Generation != installedCheckpoint.Generation ||
				intent.Predecessor != installedCheckpoint.sourceRef() {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress typed rebind intent history is invalid")
			}
			result.IntentsV2 = append(result.IntentsV2, gatewayRebindProtectedIntentV2Selection{
				Store: store, Generation: generation, Intent: intent, Existing: true,
			})
			progress, progressErr := scanGatewayRebindProgressForIntentV2(m.options.DataRoot, intent, artifact)
			if progressErr != nil {
				return gatewayRebindProtectedIntentHistory{}, progressErr
			}
			result.Progress = append(result.Progress, progress...)
			if index != len(generations)-1 {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress typed rebind intent history is unresolved")
			}
			continue
		}
		store, storeErr := newGatewayRebindProtectedIntentStore(m.options.DataRoot, generation, artifact.operationID)
		if storeErr != nil || store.path != artifact.intent.path {
			return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind intent history path is invalid")
		}
		intent, loadErr := store.load()
		if loadErr != nil || !gatewayRebindProtectedIntentMatchesPredecessor(intent, artifactPredecessor) {
			return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind intent history is invalid")
		}
		progress, progressErr := scanGatewayRebindProgressForIntent(m.options.DataRoot, intent, artifactPredecessor, artifact)
		if progressErr != nil {
			return gatewayRebindProtectedIntentHistory{}, progressErr
		}
		if len(progress) >= 13 {
			sequenceTwelve := progress[11].Record
			for _, selection := range progress[12:] {
				record := selection.Record
				if record.Handover == nil || record.Handover.Plan == nil {
					return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind handover history is invalid")
				}
				handoverContext := gatewayRebindFinalHandoverContext{
					Intent: intent, SequenceTwelve: sequenceTwelve, Predecessor: artifactPredecessor, Source: source,
					Phase: record.Phase, Plan: record.Handover.Plan, Final: record.Handover.Final,
				}
				if !validGatewayRebindFinalHandoverProgressContext(handoverContext, record) {
					return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind handover history is invalid")
				}
			}
		}
		result.Intents = append(result.Intents, gatewayRebindProtectedIntentSelection{
			Store: store, Generation: generation, Intent: intent, Existing: true,
		})
		result.Progress = append(result.Progress, progress...)
		if artifact.terminal.path != "" {
			terminalStore, terminalStoreErr := newGatewayRebindFinalHandoverTerminalStore(
				m.options.DataRoot, generation, artifact.operationID)
			if terminalStoreErr != nil || terminalStore.path != artifact.terminal.path {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind terminal history path is invalid")
			}
			receipt, receiptErr := terminalStore.load()
			terminalHistory := result
			terminalHistory.Predecessor = artifactPredecessor
			if receiptErr != nil || !gatewayRebindFinalHandoverTerminalInstallPermitted(terminalHistory, receipt) {
				return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind terminal history is invalid")
			}
			result.Terminals = append(result.Terminals, gatewayRebindFinalHandoverTerminalSelection{
				Store: terminalStore, Generation: generation, Receipt: receipt, Existing: true,
			})
		}
		if index != len(generations)-1 && artifact.terminal.path == "" {
			return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind intent history is unresolved")
		}
	}
	if checkpoint != nil {
		checkpoint()
	}
	after, err := readGatewayHistorySnapshotMode(m.store, true)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind intent history changed during inspection")
	}
	confirmedSource, err := m.store.load()
	if err != nil || !reflect.DeepEqual(source, confirmedSource) {
		return gatewayRebindProtectedIntentHistory{}, errors.New("generated ingress rebind predecessor source changed during inspection")
	}
	return result, nil
}

func gatewayRebindCheckpointMatchesProtectedHistory(checkpoint gatewayRebindPredecessorCheckpoint,
	predecessor gatewayUpgradeGenerationSelection, history gatewayRebindProtectedIntentHistory,
) bool {
	switch checkpoint.Lineage.Kind {
	case appaccess.GatewayRebindSourceGatewayUpgrade:
		return gatewayRebindCheckpointMatchesUpgradePredecessor(checkpoint, predecessor)
	case appaccess.GatewayRebindSourceGatewayRebind:
		if checkpoint.UpgradeState != nil || checkpoint.CurrentState == nil {
			return false
		}
		for _, terminal := range history.Terminals {
			if terminal.Receipt.Digest != checkpoint.Lineage.TerminalReceiptDigest ||
				terminal.Receipt.Disposition != gatewayRebindFinalHandoverTerminalCommit {
				continue
			}
			lineage, err := gatewayRebindCurrentLineage(terminal.Receipt)
			return err == nil && lineage == checkpoint.Lineage
		}
	}
	return false
}

func gatewayRebindCheckpointMatchesUpgradePredecessor(checkpoint gatewayRebindPredecessorCheckpoint,
	predecessor gatewayUpgradeGenerationSelection,
) bool {
	if checkpoint.UpgradeState == nil || checkpoint.CurrentState != nil || predecessor.Store == nil ||
		!predecessor.Existing || predecessor.PartialState || predecessor.Retired || predecessor.Aborted ||
		checkpoint.Lineage.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		checkpoint.Lineage.ProtectedGeneration != predecessor.Generation ||
		checkpoint.Lineage.OperationID != predecessor.operationID {
		return false
	}
	// Only the immutable origin must still match the live upgrade selection.
	// The checkpoint's source state is self-validating and intentionally
	// remains historical after grants, disables, or redeploys change routes.
	lineage, err := gatewayUpgradeCurrentLineage(predecessor)
	return err == nil && checkpoint.Lineage == lineage
}

func gatewayRebindProtectedIntentMatchesPredecessor(intent gatewayRebindProtectedIntent,
	predecessor gatewayUpgradeGenerationSelection,
) bool {
	if predecessor.Store == nil || !predecessor.Existing || predecessor.PartialState || predecessor.Retired || predecessor.Aborted ||
		intent.Intent.Predecessor.Generation != predecessor.Generation ||
		intent.Intent.Predecessor.OperationID != predecessor.Journal.OperationID ||
		intent.Intent.Predecessor.Profile != predecessor.State.Profile ||
		intent.Intent.Predecessor.IdentityDigest != predecessor.State.Identity.Digest ||
		intent.Intent.Predecessor.Resources != predecessor.Journal.Resources {
		return false
	}
	stateDigest, err := canonicalDigest(predecessor.State)
	if err != nil || stateDigest != intent.Intent.Predecessor.StateDigest ||
		!gatewayRebindProtectedRosterMatchesPredecessor(intent.Intent.Roster, predecessor.State) {
		return false
	}
	journalDigest, err := canonicalDigest(predecessor.Journal)
	return err == nil && journalDigest == intent.Intent.Predecessor.JournalDigest
}

func gatewayRebindProtectedRosterMatchesPredecessor(roster []appaccess.GatewayRebindRosterEntry,
	state gatewayV2RouteState,
) bool {
	if state.Pending != nil || state.LANRecovery != nil {
		return false
	}
	seenApps := make(map[string]struct{}, len(roster))
	for _, entry := range roster {
		if _, duplicate := seenApps[entry.AppID]; duplicate {
			return false
		}
		app, exists := state.Apps[entry.AppID]
		if !exists || app.LAN == nil {
			return false
		}
		lan := *app.LAN
		if lan.AllocationID != entry.AllocationID || lan.Port != entry.Port ||
			lan.GrantAttemptID != entry.GrantAttemptID ||
			lan.OwnerOperationID != entry.AllocationOwnerOperationID ||
			lan.AccessRevisionID != entry.AccessRevisionID ||
			lan.AccessRevisionNumber != entry.AccessRevisionNumber ||
			lan.AccessSpecDigest != entry.AccessSpecDigest ||
			lan.ProfileRevisionID != state.Profile.RevisionID ||
			lan.ProfileRevisionNumber != state.Profile.RevisionNumber ||
			lan.ProfileSpecDigest != state.Profile.SpecDigest ||
			string(app.Route.Slot) != entry.ServingSlot {
			return false
		}
		seenApps[entry.AppID] = struct{}{}
	}

	lanApps := 0
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		lanApps++
		if _, expected := seenApps[appID]; !expected {
			return false
		}
	}
	// The predecessor has no serving deployment/release IDs, route generation,
	// or grant state sequence/protected digest. The intent roster has no grant
	// request digest or grant approver. A future effect/recovery path must
	// reconstruct those cross-store facts from fresh SQLite and grant reads
	// while holding its writer lock and effects lease.
	return lanApps == len(roster)
}
