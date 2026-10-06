package generatedingress

import (
	"errors"
	"os"
	"reflect"
	"sort"

	"github.com/hostd/hostd/internal/appaccess"
)

const (
	gatewayCurrentOwnedStopTargetVersion = 1
	gatewayCurrentOwnedStopTargetPurpose = "hostd/generated-ingress/routes/current-owned-stop/v1"
)

// gatewayCurrentOwnedStopTarget is protected withdrawal authority only. It
// does not assert that Lineage is SQL current and cannot authorize serving,
// publication, or any new effect. The physical adapter may inspect and stop
// only FinalContainer.ID after proving its complete retained ownership and
// configuration metadata still match this target.
type gatewayCurrentOwnedStopTarget struct {
	Version               int
	Purpose               string
	Lineage               appaccess.GatewayCurrentLineageRef
	Terminal              gatewayRebindAttemptTerminalView
	State                 gatewayCurrentRouteState
	PermittedStateDigests []string
	Pending               *gatewayCurrentPendingRoute
	Transition            *gatewayCurrentPhysicalTransition
	FinalContainer        gatewayRebindFinalContainerBinding
	Digest                string
}

func gatewayCurrentOwnedStopTargetDigest(value gatewayCurrentOwnedStopTarget) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func validGatewayCurrentOwnedStopTarget(value gatewayCurrentOwnedStopTarget) bool {
	if value.Version != gatewayCurrentOwnedStopTargetVersion || value.Purpose != gatewayCurrentOwnedStopTargetPurpose ||
		!validGatewayCurrentRouteState(value.State) || value.State.Lineage != value.Lineage ||
		!gatewayRebindAttemptTerminalMatchesLineage(value.Terminal, value.Lineage) ||
		value.Terminal.Resources.FinalContainer == nil ||
		!reflect.DeepEqual(value.FinalContainer, *value.Terminal.Resources.FinalContainer) ||
		!validSHA256(value.Digest) || len(value.PermittedStateDigests) == 0 ||
		len(value.PermittedStateDigests) > 3 {
		return false
	}
	foundState := false
	seen := make(map[string]struct{}, len(value.PermittedStateDigests))
	for _, digest := range value.PermittedStateDigests {
		if !validSHA256(digest) {
			return false
		}
		if _, duplicate := seen[digest]; duplicate {
			return false
		}
		seen[digest] = struct{}{}
		foundState = foundState || digest == value.State.Digest
	}
	if !foundState || !sort.StringsAreSorted(value.PermittedStateDigests) {
		return false
	}
	if value.Transition == nil {
		if len(value.PermittedStateDigests) != 1 || value.PermittedStateDigests[0] != value.State.Digest ||
			!reflect.DeepEqual(value.Pending, value.State.Pending) {
			return false
		}
	} else {
		transition := *value.Transition
		if !validGatewayCurrentPhysicalTransition(transition) || transition.Before.Lineage != value.Lineage ||
			!reflect.DeepEqual(value.Pending, transition.Pending.Pending) ||
			!reflect.DeepEqual(value.PermittedStateDigests, canonicalGatewayCurrentOwnedStopDigests(
				transition.Before.Digest, transition.Pending.Digest, transition.Effective.Digest)) ||
			(!reflect.DeepEqual(value.State, transition.Before) && !reflect.DeepEqual(value.State, transition.Pending) &&
				!reflect.DeepEqual(value.State, transition.Effective)) {
			return false
		}
	}
	digest, err := gatewayCurrentOwnedStopTargetDigest(value)
	return err == nil && digest == value.Digest
}

func (m *Manager) gatewayCurrentOwnedStopTargetForTransitionLocked(
	transition gatewayCurrentPhysicalTransition,
) (gatewayCurrentOwnedStopTarget, error) {
	if m == nil || !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("invalid current owned-stop transition")
	}
	target, err := m.gatewayCurrentOwnedStopTargetForStateLocked(transition.Pending)
	if err != nil {
		return gatewayCurrentOwnedStopTarget{}, err
	}
	target.PermittedStateDigests = canonicalGatewayCurrentOwnedStopDigests(
		transition.Before.Digest, transition.Pending.Digest, transition.Effective.Digest)
	target.Pending = cloneGatewayCurrentPendingRoute(transition.Pending.Pending)
	copy := cloneGatewayCurrentPhysicalTransition(transition)
	target.Transition = &copy
	target.Digest, err = gatewayCurrentOwnedStopTargetDigest(target)
	if err != nil || !validGatewayCurrentOwnedStopTarget(target) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("invalid current owned-stop transition target")
	}
	return target, nil
}

func (m *Manager) gatewayCurrentOwnedStopTargetForStateLocked(
	state gatewayCurrentRouteState,
) (gatewayCurrentOwnedStopTarget, error) {
	if m == nil || !validGatewayCurrentRouteState(state) || state.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind {
		return gatewayCurrentOwnedStopTarget{}, errors.New("invalid current owned-stop state")
	}
	terminal, err := m.gatewayCurrentOwnedStopTerminalLocked(state.Lineage)
	if err != nil || terminal.Resources.FinalContainer == nil {
		return gatewayCurrentOwnedStopTarget{}, errors.New("current owned-stop terminal is unavailable")
	}
	target := gatewayCurrentOwnedStopTarget{
		Version: gatewayCurrentOwnedStopTargetVersion, Purpose: gatewayCurrentOwnedStopTargetPurpose,
		Lineage: state.Lineage, Terminal: terminal, State: cloneGatewayCurrentRouteState(state),
		PermittedStateDigests: []string{state.Digest}, Pending: cloneGatewayCurrentPendingRoute(state.Pending),
		FinalContainer: *terminal.Resources.FinalContainer,
	}
	target.Digest, err = gatewayCurrentOwnedStopTargetDigest(target)
	if err != nil || !validGatewayCurrentOwnedStopTarget(target) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("invalid current owned-stop target")
	}
	return target, nil
}

// revalidateGatewayCurrentOwnedStopTargetLocked is the effect-boundary guard.
// It reloads the generation-scoped state and canonical receipt without SQL,
// permits only a state named by the captured transition, and returns a fresh
// withdrawal target for that exact retained state.
func (m *Manager) revalidateGatewayCurrentOwnedStopTargetLocked(
	target gatewayCurrentOwnedStopTarget,
) (gatewayCurrentOwnedStopTarget, error) {
	if m == nil || !validGatewayCurrentOwnedStopTarget(target) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("invalid current owned-stop target")
	}
	store, err := newGatewayCurrentRouteStateStore(m.options.DataRoot, target.Lineage)
	if err != nil {
		return gatewayCurrentOwnedStopTarget{}, err
	}
	state, err := store.load()
	if err != nil || state.Lineage != target.Lineage || !gatewayCurrentOwnedStopDigestPermitted(target, state.Digest) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("current owned-stop protected state changed")
	}
	fresh, err := m.gatewayCurrentOwnedStopTargetForStateLocked(state)
	if err != nil || !reflect.DeepEqual(fresh.Terminal, target.Terminal) ||
		!reflect.DeepEqual(fresh.FinalContainer, target.FinalContainer) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("current owned-stop ownership changed")
	}
	fresh.PermittedStateDigests = append([]string(nil), target.PermittedStateDigests...)
	fresh.Pending = cloneGatewayCurrentPendingRoute(target.Pending)
	if target.Transition != nil {
		copy := cloneGatewayCurrentPhysicalTransition(*target.Transition)
		fresh.Transition = &copy
	}
	fresh.Digest, err = gatewayCurrentOwnedStopTargetDigest(fresh)
	if err != nil || !validGatewayCurrentOwnedStopTarget(fresh) {
		return gatewayCurrentOwnedStopTarget{}, errors.New("invalid revalidated current owned-stop target")
	}
	return fresh, nil
}

// gatewayCurrentOwnedStopTargetsProtectedLocked enumerates withdrawal targets
// only. It deliberately returns every exact committed-lineage route bundle,
// rather than selecting SQL current from protected history. Missing/corrupt
// bundles, duplicate final IDs, and conflicting ownership fail closed.
func (m *Manager) gatewayCurrentOwnedStopTargetsProtectedLocked() ([]gatewayCurrentOwnedStopTarget, error) {
	targets, _, err := m.gatewayCurrentOwnedStopTargetsProtectedPartialLocked()
	if err != nil {
		return nil, err
	}
	return targets, nil
}

type gatewayCurrentOwnedStopHistoryCensus struct {
	ProtectedRebindHistory bool
	UnresolvedAttempt      bool
	CommittedOwnership     bool
}

type gatewayCurrentOwnedStopAttemptKey struct {
	generation  uint64
	operationID string
}

func gatewayCurrentOwnedStopCensus(history gatewayRebindProtectedIntentHistory,
	presence gatewayRebindProtectedPresenceSnapshot,
) (gatewayCurrentOwnedStopHistoryCensus, []appaccess.GatewayCurrentLineageRef) {
	census := gatewayCurrentOwnedStopHistoryCensus{ProtectedRebindHistory: presence.present}
	attempts := make(map[gatewayCurrentOwnedStopAttemptKey]struct{})
	terminals := make(map[gatewayCurrentOwnedStopAttemptKey]struct{})
	addAttempt := func(generation uint64, operationID string) {
		attempts[gatewayCurrentOwnedStopAttemptKey{generation: generation, operationID: operationID}] = struct{}{}
	}
	addTerminal := func(generation uint64, operationID string) {
		key := gatewayCurrentOwnedStopAttemptKey{generation: generation, operationID: operationID}
		attempts[key], terminals[key] = struct{}{}, struct{}{}
	}
	for _, selected := range history.Checkpoints {
		addAttempt(selected.Generation, selected.Checkpoint.OperationID)
	}
	for _, selected := range history.Intents {
		addAttempt(selected.Generation, selected.Intent.OperationID)
	}
	for _, selected := range history.IntentsV2 {
		addAttempt(selected.Generation, selected.Intent.OperationID)
	}
	for _, selected := range history.Progress {
		addAttempt(selected.Generation, selected.Record.OperationID)
	}
	for _, selected := range history.Terminals {
		addTerminal(selected.Generation, selected.Receipt.OperationID)
	}
	for _, selected := range history.TerminalsV2 {
		addTerminal(selected.Generation, selected.Receipt.OperationID)
	}
	for key := range attempts {
		if _, terminal := terminals[key]; !terminal {
			census.UnresolvedAttempt = true
		}
	}

	lineages := make([]appaccess.GatewayCurrentLineageRef, 0, len(history.Terminals)+len(history.TerminalsV2))
	committed := make(map[gatewayCurrentOwnedStopAttemptKey]struct{})
	appendLineage := func(lineage appaccess.GatewayCurrentLineageRef, lineageErr error) {
		if lineageErr != nil {
			return
		}
		lineages = append(lineages, lineage)
		committed[gatewayCurrentOwnedStopAttemptKey{generation: lineage.ProtectedGeneration,
			operationID: lineage.OperationID}] = struct{}{}
	}
	for _, retained := range history.Terminals {
		appendLineage(gatewayRebindCurrentLineage(retained.Receipt))
	}
	for _, retained := range history.TerminalsV2 {
		appendLineage(gatewayRebindCurrentLineageV2(retained.Receipt))
	}
	// Every generation-scoped current bundle must be justified by its own
	// committed terminal. Aggregate retained history is insufficient: an
	// orphan bundle beside an unrelated complete generation still represents
	// unresolved protected ownership and possible live traffic.
	for name := range presence.files {
		generation, operationID, relevant, err := parseGatewayCurrentRoutePresenceName(name)
		if err != nil {
			census.UnresolvedAttempt = true
			continue
		}
		if !relevant {
			continue
		}
		if _, ok := committed[gatewayCurrentOwnedStopAttemptKey{generation: generation,
			operationID: operationID}]; !ok {
			census.UnresolvedAttempt = true
		}
	}
	if census.ProtectedRebindHistory && len(attempts) == 0 {
		census.UnresolvedAttempt = true
	}
	census.CommittedOwnership = len(lineages) != 0
	return census, lineages
}

// gatewayCurrentOwnedStopTargetsProtectedPartialLocked retains independently
// proved targets when another committed generation has a missing, corrupt, or
// ambiguous route bundle. Its caller may withdraw those exact IDs but must
// preserve the returned error and keep startup failed: partial withdrawal is
// never evidence that all owned traffic is absent.
func (m *Manager) gatewayCurrentOwnedStopTargetsProtectedPartialLocked() (
	[]gatewayCurrentOwnedStopTarget, gatewayCurrentOwnedStopHistoryCensus, error,
) {
	if m == nil {
		return nil, gatewayCurrentOwnedStopHistoryCensus{}, errors.New("invalid current owned-stop manager")
	}
	presence, err := readGatewayRebindProtectedPresenceMode(m.options.DataRoot, false)
	if err != nil {
		return nil, gatewayCurrentOwnedStopHistoryCensus{}, err
	}
	if !presence.present {
		// Native route corruption must not prevent a separate exact journal-
		// bound emergency withdrawal. No current rebind artifact is present;
		// this is not serving authority or a native ownership proof.
		return nil, gatewayCurrentOwnedStopHistoryCensus{}, nil
	}
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return nil, gatewayCurrentOwnedStopHistoryCensus{ProtectedRebindHistory: presence.present}, err
	}
	census, lineages := gatewayCurrentOwnedStopCensus(history, presence)
	sort.Slice(lineages, func(i, j int) bool {
		if lineages[i].ProtectedGeneration != lineages[j].ProtectedGeneration {
			return lineages[i].ProtectedGeneration < lineages[j].ProtectedGeneration
		}
		return lineages[i].OperationID < lineages[j].OperationID
	})
	result := make([]gatewayCurrentOwnedStopTarget, 0, len(lineages))
	var failures []error
	for _, lineage := range lineages {
		store, storeErr := newGatewayCurrentRouteStateStore(m.options.DataRoot, lineage)
		if storeErr != nil {
			failures = append(failures, storeErr)
			continue
		}
		if _, statErr := os.Lstat(store.path); statErr != nil {
			failures = append(failures, errors.New("current owned-stop route bundle is missing or unreadable"))
			continue
		}
		state, loadErr := store.load()
		if loadErr != nil {
			failures = append(failures, errors.New("current owned-stop route bundle is corrupt"))
			continue
		}
		target, targetErr := m.gatewayCurrentOwnedStopTargetForStateLocked(state)
		if targetErr != nil {
			failures = append(failures, targetErr)
			continue
		}
		result = append(result, target)
	}
	counts := make(map[string]int, len(result))
	for _, target := range result {
		counts[target.FinalContainer.ID]++
	}
	unique := result[:0]
	for _, target := range result {
		if counts[target.FinalContainer.ID] != 1 {
			failures = append(failures, errors.New("current owned-stop container ownership is ambiguous"))
			continue
		}
		unique = append(unique, target)
	}
	return unique, census, errors.Join(failures...)
}

func cloneGatewayCurrentPhysicalTransition(value gatewayCurrentPhysicalTransition) gatewayCurrentPhysicalTransition {
	return gatewayCurrentPhysicalTransition{Kind: value.Kind, AppID: value.AppID,
		Before: cloneGatewayCurrentRouteState(value.Before), Pending: cloneGatewayCurrentRouteState(value.Pending),
		Effective: cloneGatewayCurrentRouteState(value.Effective)}
}

func (m *Manager) gatewayCurrentOwnedStopTerminalLocked(
	lineage appaccess.GatewayCurrentLineageRef,
) (gatewayRebindAttemptTerminalView, error) {
	history, err := m.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		return gatewayRebindAttemptTerminalView{}, err
	}
	var result *gatewayRebindAttemptTerminalView
	matches := 0
	accept := func(candidate gatewayRebindAttemptTerminalView, candidateErr error) {
		if candidateErr != nil || !gatewayRebindAttemptTerminalMatchesLineage(candidate, lineage) {
			return
		}
		matches++
		copy := candidate
		result = &copy
	}
	for _, retained := range history.Terminals {
		accept(newGatewayRebindAttemptTerminalViewLegacy(retained.Receipt))
	}
	for _, retained := range history.TerminalsV2 {
		accept(newGatewayRebindAttemptTerminalViewV2(retained.Receipt))
	}
	if result == nil || matches != 1 {
		return gatewayRebindAttemptTerminalView{}, errors.New("current owned-stop terminal is missing or ambiguous")
	}
	return *result, nil
}

func canonicalGatewayCurrentOwnedStopDigests(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func gatewayCurrentOwnedStopDigestPermitted(target gatewayCurrentOwnedStopTarget, digest string) bool {
	index := sort.SearchStrings(target.PermittedStateDigests, digest)
	return index < len(target.PermittedStateDigests) && target.PermittedStateDigests[index] == digest
}
