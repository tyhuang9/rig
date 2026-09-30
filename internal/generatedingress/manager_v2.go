package generatedingress

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
)

type gatewayV2PendingLiveTopology string

const (
	gatewayV2PendingUnknown         gatewayV2PendingLiveTopology = "unknown_or_identity_drift"
	gatewayV2PendingCommittedExact  gatewayV2PendingLiveTopology = "committed_live_and_restart_exact"
	gatewayV2PendingProposedExact   gatewayV2PendingLiveTopology = "proposed_live_and_restart_exact"
	gatewayV2PendingReloadOnlyMixed gatewayV2PendingLiveTopology = "proposed_live_committed_restart_exact"
)

// committedV2Locked selects the state owner while the gateway lock is held.
// A partial, corrupt, or non-committed pair is an unresolved migration and can
// never fall through to legacy Docker mutation.
func (m *Manager) committedV2Locked() (*gatewayUpgradeStateStore, gatewayV2RouteState, gatewayMigrationJournal, bool, error) {
	if m == nil || m.store == nil {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	before, err := m.store.directoryIdentity()
	if err != nil {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	present := make([]bool, 2)
	for index, name := range []string{v2RouteStateFilename, gatewayMigrationFilename} {
		path := filepath.Join(m.store.root, name)
		info, statErr := os.Lstat(path)
		switch {
		case statErr == nil:
			present[index] = true
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(path) {
				return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
			}
		case errors.Is(statErr, os.ErrNotExist):
		default:
			return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
		}
	}
	if m.store.sameDirectory(before) != nil {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	if !present[0] && !present[1] {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, nil
	}
	if !present[0] || !present[1] {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	store, err := newGatewayUpgradeStateStore(m.options.DataRoot)
	if err != nil {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	journal, err := store.loadMigrationJournal()
	if err != nil || journal.Phase != gatewayPhaseCommitted {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	state, boundJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(journal, boundJournal) {
		return nil, gatewayV2RouteState{}, gatewayMigrationJournal{}, false, &Error{Code: DiagnosticRouteUnresolved}
	}
	return store, state, journal, true, nil
}

func (m *Manager) observeV2Topology(ctx context.Context, source routeState, state gatewayV2RouteState, journal gatewayMigrationJournal) gatewayObservedTopology {
	if m.gatewayTopologyObserver != nil {
		return m.gatewayTopologyObserver(ctx, source, state, journal)
	}
	return m.observeGatewayMigrationTopology(ctx, source, state, journal)
}

func (m *Manager) attestCommittedV2Locked(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if state.Pending != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	source, err := m.store.load()
	if err != nil {
		return &Error{Code: DiagnosticRouteStateFailed}
	}
	if m.observeV2Topology(ctx, source, state, journal) != gatewayTopologyExactFinalV2 {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}

func (m *Manager) switchCommittedV2Locked(ctx context.Context, store *gatewayUpgradeStateStore, state gatewayV2RouteState, journal gatewayMigrationJournal, request generatedruntime.RouteSwitchRequest) error {
	if state.Pending != nil {
		var err error
		state, err = m.rollbackCommittedV2PendingLocked(ctx, store, state, journal)
		if err != nil {
			return markCandidateMayBeLive(err)
		}
	}
	if err := m.attestCommittedV2Locked(ctx, state, journal); err != nil {
		return markCandidateMayBeLive(err)
	}

	previous, hadPrevious := state.Apps[request.AppID]
	proposed := gatewayV2AppRoute{Route: routeRecord{Slot: request.ToSlot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...)}}
	if hadPrevious {
		proposed.LAN = cloneGatewayV2AppRoute(previous).LAN
	}
	if hadPrevious && reflect.DeepEqual(previous, proposed) {
		return nil
	}
	if request.FromSlot == "" {
		if hadPrevious {
			return &Error{Code: DiagnosticRouteInvalid}
		}
	} else if !hadPrevious || previous.Route.Slot != request.FromSlot {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if err := m.preflightCommittedV2Candidate(ctx, state, journal, request.AppID, proposed); err != nil {
		return err
	}

	pendingState := cloneGatewayV2RouteState(state)
	pendingState.Pending = &gatewayV2PendingRoute{AppID: request.AppID, Proposed: cloneGatewayV2AppRoute(proposed)}
	if hadPrevious {
		copy := cloneGatewayV2AppRoute(previous)
		pendingState.Pending.Previous = &copy
	}
	if err := store.saveCommittedV2State(pendingState, journal); err != nil {
		return markCandidateMayBeLive(&Error{Code: DiagnosticRouteStateFailed})
	}
	// Re-attest immediately before reload so an endpoint or network change
	// cannot hide in the protected-state write window.
	if err := m.preflightCommittedV2Candidate(ctx, state, journal, request.AppID, proposed); err != nil {
		if clearErr := store.saveCommittedV2State(state, journal); clearErr != nil {
			return &Error{Code: DiagnosticRouteStateFailed}
		}
		return err
	}

	proposedState := cloneGatewayV2RouteState(state)
	proposedState.Apps[request.AppID] = cloneGatewayV2AppRoute(proposed)
	if err := m.applyCommittedV2Routes(ctx, proposedState, "proposed.json"); err != nil {
		return m.rollbackCommittedV2AfterFailure(store, state, journal, err)
	}
	if err := m.attestCommittedV2Locked(ctx, proposedState, journal); err != nil {
		return m.rollbackCommittedV2AfterFailure(store, state, journal, err)
	}
	if err := store.saveCommittedV2State(proposedState, journal); err != nil {
		// The protected install may have completed even when its directory sync
		// failed. Stop all mutation and let a fresh locked recovery decide.
		return candidateMayBeLiveError()
	}
	return nil
}

func (m *Manager) preflightCommittedV2Candidate(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal, appID string, proposed gatewayV2AppRoute) error {
	if state.Pending != nil || !validAppID(appID) || validateRoute(proposed.Route) != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	proposedState := cloneGatewayV2RouteState(state)
	proposedState.Apps[appID] = cloneGatewayV2AppRoute(proposed)
	currentOwners, currentValid := gatewayV2ApplicationNetworkOwners(state)
	proposedOwners, proposedValid := gatewayV2ApplicationNetworkOwners(proposedState)
	// This increment does not mutate final-container network attachments. A new
	// application network therefore requires the separate locked reconcile
	// transaction before its route can be switched.
	if !currentValid || !proposedValid || !reflect.DeepEqual(currentOwners, proposedOwners) {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	if m.gatewayCandidateObserver != nil {
		return m.gatewayCandidateObserver(ctx, state, journal, appID, proposed)
	}

	networks := make(map[string]caddyNetworkInspection, len(proposedOwners))
	ids := make(map[string]string, len(proposedOwners))
	for name, owner := range proposedOwners {
		network, id, found, err := m.inspectNamedGatewayNetwork(ctx, name)
		if err != nil || !found || !validContainerID(id) || !validApplicationNetwork(network.identity(), owner) {
			return &Error{Code: DiagnosticIngressDrift}
		}
		networks[name] = network
		ids[name] = id
	}
	before, err := m.inspectGatewayEndpointIdentitySnapshot(ctx, gatewayV2RouteRecords(proposedState), networks)
	if err != nil {
		return &Error{Code: DiagnosticIngressDrift}
	}
	image, imageFound, err := m.inspectImage(ctx)
	if err != nil || !validGatewayPinnedImage(image, imageFound) {
		return &Error{Code: DiagnosticIngressDrift}
	}
	final, runtime, found, err := m.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if err != nil || !validGatewayV2Container(state, journal, final, runtime, found, gatewayV2FinalContainerRole, image.ID) || !final.Running || final.Restarting {
		return &Error{Code: DiagnosticIngressDrift}
	}
	for _, endpoint := range proposed.Route.Endpoints {
		if err := m.probeGatewayEndpoint(ctx, final.ID, endpoint); err != nil {
			return err
		}
	}
	confirmed, err := m.reinspectGatewayApplicationNetworks(ctx, networks, ids)
	if err != nil {
		return &Error{Code: DiagnosticIngressDrift}
	}
	after, err := m.inspectGatewayEndpointIdentitySnapshot(ctx, gatewayV2RouteRecords(proposedState), confirmed)
	if err != nil || before != after {
		return &Error{Code: DiagnosticIngressDrift}
	}
	confirmedFinal, confirmedRuntime, confirmedFound, err := m.inspectNamedGatewayContainer(ctx, state.Identity.FinalContainer)
	if err != nil || !confirmedFound || !reflect.DeepEqual(final, confirmedFinal) || !reflect.DeepEqual(runtime, confirmedRuntime) {
		return &Error{Code: DiagnosticIngressDrift}
	}
	return nil
}

func (m *Manager) recoverCommittedV2Locked(ctx context.Context, store *gatewayUpgradeStateStore, state gatewayV2RouteState, journal gatewayMigrationJournal) error {
	if state.Pending != nil {
		var err error
		state, err = m.rollbackCommittedV2PendingLocked(ctx, store, state, journal)
		if err != nil {
			return err
		}
	}
	return m.attestCommittedV2Locked(ctx, state, journal)
}

func (m *Manager) rollbackCommittedV2PendingLocked(ctx context.Context, store *gatewayUpgradeStateStore, state gatewayV2RouteState, journal gatewayMigrationJournal) (gatewayV2RouteState, error) {
	if state.Pending == nil {
		return state, nil
	}
	source, err := m.store.load()
	if err != nil {
		return gatewayV2RouteState{}, &Error{Code: DiagnosticRouteStateFailed}
	}
	committed := cloneGatewayV2RouteState(state)
	committed.Pending = nil
	proposed := cloneGatewayV2RouteState(committed)
	proposed.Apps[state.Pending.AppID] = cloneGatewayV2AppRoute(state.Pending.Proposed)

	topology := m.observeCommittedV2PendingTopology(ctx, source, committed, proposed, journal)
	if topology == gatewayV2PendingUnknown {
		return gatewayV2RouteState{}, &Error{Code: DiagnosticRouteUnresolved}
	}
	if topology == gatewayV2PendingProposedExact || topology == gatewayV2PendingReloadOnlyMixed {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout*3)
		defer cancel()
		if err := m.applyCommittedV2Routes(rollbackCtx, committed, "rollback.json"); err != nil {
			return gatewayV2RouteState{}, candidateMayBeLiveError()
		}
		if m.observeV2Topology(rollbackCtx, source, committed, journal) != gatewayTopologyExactFinalV2 {
			return gatewayV2RouteState{}, candidateMayBeLiveError()
		}
	}
	if err := store.saveCommittedV2State(committed, journal); err != nil {
		return gatewayV2RouteState{}, candidateMayBeLiveError()
	}
	return committed, nil
}

func (m *Manager) observeCommittedV2PendingTopology(ctx context.Context, source routeState, committed, proposed gatewayV2RouteState, journal gatewayMigrationJournal) gatewayV2PendingLiveTopology {
	if m.observeV2Topology(ctx, source, committed, journal) == gatewayTopologyExactFinalV2 {
		return gatewayV2PendingCommittedExact
	}
	if m.observeV2Topology(ctx, source, proposed, journal) == gatewayTopologyExactFinalV2 {
		return gatewayV2PendingProposedExact
	}
	mixed := false
	if m.gatewayMixedRestartObserver != nil {
		mixed = m.gatewayMixedRestartObserver(ctx, source, committed, proposed, journal)
	} else {
		mixed = m.observeGatewayV2MixedRestart(ctx, source, committed, proposed, journal)
	}
	if mixed {
		return gatewayV2PendingReloadOnlyMixed
	}
	return gatewayV2PendingUnknown
}

func (m *Manager) rollbackCommittedV2AfterFailure(store *gatewayUpgradeStateStore, committed gatewayV2RouteState, journal gatewayMigrationJournal, original error) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), m.options.CommandTimeout*3)
	defer cancel()
	if err := m.applyCommittedV2Routes(rollbackCtx, committed, "rollback.json"); err != nil {
		return candidateMayBeLiveError()
	}
	if err := m.attestCommittedV2Locked(rollbackCtx, committed, journal); err != nil {
		return candidateMayBeLiveError()
	}
	if err := store.saveCommittedV2State(committed, journal); err != nil {
		return candidateMayBeLiveError()
	}
	return original
}

func (m *Manager) observeCommittedV2Locked(ctx context.Context, state gatewayV2RouteState, journal gatewayMigrationJournal, appID string) (Observation, error) {
	if err := m.attestCommittedV2Locked(ctx, state, journal); err != nil {
		return Observation{}, err
	}
	app, exists := state.Apps[appID]
	if !exists {
		return Observation{}, &Error{Code: DiagnosticRouteInvalid}
	}
	url := "http://" + appID + ".rig.localhost:" + strconv.FormatUint(uint64(m.options.HostPort), 10)
	return Observation{URL: url, Slot: app.Route.Slot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), app.Route.Endpoints...), ObservedAt: time.Now().UTC()}, nil
}

func (m *Manager) applyCommittedV2Routes(ctx context.Context, state gatewayV2RouteState, filename string) error {
	if state.Pending != nil || !validConfigFilename(filename) {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	probeToken, err := gatewayV2HostChallenge(state)
	if err != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	routes, assignments := gatewayV2ConfigInputs(state)
	config, err := buildCaddyConfigV2(routes, net.JoinHostPort(state.Network.ContainerIPv4, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)), caddyV2Profile{
		SelectedIPv4: state.Profile.SelectedIPv4, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd, ProbeToken: probeToken,
	}, assignments)
	if err != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if err := m.copyGatewayV2Config(ctx, state.Identity.FinalContainer, config, filename); err != nil {
		return err
	}
	containerPath := "/config/" + filename
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", state.Identity.FinalContainer, "caddy", "validate", "--config", containerPath); err != nil {
		return &Error{Code: DiagnosticRouteValidateFailed}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", state.Identity.FinalContainer, "caddy", "reload", "--config", containerPath); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", "--user", "0:0", state.Identity.FinalContainer, "cp", containerPath, "/config/active.next.json"); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", "--user", "0:0", state.Identity.FinalContainer, "mv", "/config/active.next.json", "/config/"+state.Identity.ActiveConfigFilename); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	return nil
}

func (m *Manager) copyGatewayV2Config(ctx context.Context, container string, contents []byte, filename string) error {
	defer clear(contents)
	if container != gatewayV2ContainerName {
		return &Error{Code: DiagnosticIngressDrift}
	}
	if len(contents) == 0 || len(contents) > gatewayV2MaxConfigBytes || !validConfigFilename(filename) || !m.validWorkingDirectory() {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	file, err := os.CreateTemp(m.options.WorkingDirectory, ".rig-caddy-v2-*.json")
	if err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	if _, err := file.Write(contents); err != nil || file.Sync() != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	created, err := file.Stat()
	if err != nil || !created.Mode().IsRegular() || file.Close() != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	installed, err := os.Lstat(path)
	if err != nil || !installed.Mode().IsRegular() || installed.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(path) || !os.SameFile(created, installed) || !m.validWorkingDirectory() {
		return &Error{Code: DiagnosticIngressDrift}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "cp", path, container+":/config/"+filename); err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	return nil
}
