package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type committedV2Runner struct {
	containerID string
	files       map[string][]byte
	commands    [][]string
}

func newCommittedV2Runner() *committedV2Runner {
	return &committedV2Runner{containerID: upgradeResourceID('e'), files: make(map[string][]byte)}
}

func TestCommittedV2LANConfigFilenamesReachExactContainer(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}

	for _, filename := range []string{
		"lan-grant.json", "lan-grant-rollback.json", "lan-grant-recovery.json",
		"lan-grant-commit-recovery.json", "lan-grant-quarantine.json",
		"lan-disable.json", "lan-disable-recovery.json", "lan-disable-startup.json",
		"lan-recovery-batch.json",
	} {
		t.Run(filename, func(t *testing.T) {
			if err := manager.applyCommittedV2Routes(context.Background(), state, runner.containerID, filename); err != nil {
				t.Fatalf("fixed LAN config name was rejected: %v", err)
			}
			if _, ok := runner.files[runner.containerID+":/config/"+filename]; !ok {
				t.Fatal("exact bound container did not receive the candidate config")
			}
			if _, ok := runner.files[runner.containerID+":/config/"+state.Identity.ActiveConfigFilename]; !ok {
				t.Fatal("exact bound container did not receive the active config")
			}
		})
	}
	if validConfigFilename("lan-grant.json") {
		t.Fatal("v1 config copy accepted a LAN-only filename")
	}
	for _, filename := range []string{"", "custom.json", "../active.json", "lan-grant.json/.."} {
		before := len(runner.commands)
		if err := manager.applyCommittedV2Routes(context.Background(), state, runner.containerID, filename); !IsCode(err, DiagnosticRouteInvalid) {
			t.Fatalf("unlisted config filename %q: %v", filename, err)
		}
		if len(runner.commands) != before {
			t.Fatalf("unlisted config filename %q reached Docker", filename)
		}
	}
}

func TestGatewayV2StageConfigFilenameUsesGuardedV2CopyAllowlist(t *testing.T) {
	if !validGatewayV2ConfigFilename(gatewayV2StageConfigFilename) {
		t.Fatal("fixed stage config filename was rejected by the guarded v2 copy path")
	}
	for _, filename := range []string{"stage-2.json", "./stage.json", "stage.json/.."} {
		if validGatewayV2ConfigFilename(filename) {
			t.Fatalf("unlisted stage config filename %q was accepted", filename)
		}
	}
}

func (r *committedV2Runner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.commands = append(r.commands, args)
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] != "-" {
		target := strings.SplitN(args[3], ":", 2)
		if len(target) != 2 || target[0] != r.containerID {
			return runtimeprocess.CommandResult{}, errors.New("v2 config targeted a replacement container")
		}
		body, err := os.ReadFile(args[2])
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		r.files[args[3]] = append([]byte(nil), body...)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 7 && args[0] == "container" && args[1] == "exec" && args[2] == r.containerID && args[3] == "caddy" {
		if _, ok := r.files[r.containerID+":"+args[6]]; !ok {
			return runtimeprocess.CommandResult{}, errors.New("missing v2 config")
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 8 && args[0] == "container" && args[1] == "exec" && args[2] == "--user" && args[3] == "0:0" && args[4] == r.containerID {
		source := r.containerID + ":" + args[6]
		destination := r.containerID + ":" + args[7]
		body, ok := r.files[source]
		if !ok {
			return runtimeprocess.CommandResult{}, errors.New("missing v2 restart config")
		}
		r.files[destination] = append([]byte(nil), body...)
		if args[5] == "mv" {
			delete(r.files, source)
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if reflect.DeepEqual(args, []string{"container", "exec", gatewayV2ContainerName, "sh", "-c", capacityProbeCommand}) {
		return runtimeprocess.CommandResult{Stdout: []byte("1073741824 4294967296\n")}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected committed v2 Docker command")
}

func TestCommittedV2FinalContainerRequiresJournalBoundIdentity(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Phase = gatewayPhaseCommitted
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	observation.FinalContainer.Labels["org.opencontainers.image.title"] = "Caddy"

	if !validCommittedV2FinalContainer(state, journal, observation.FinalContainer, observation.FinalRuntime,
		observation.FinalContainerFound, observation.Image.ID) {
		t.Fatal("journal-bound final container with pinned-image metadata was rejected")
	}
	replacement := observation.FinalContainer
	replacement.ID = "sha256:" + strings.Repeat("9", 64)
	if validCommittedV2FinalContainer(state, journal, replacement, observation.FinalRuntime,
		observation.FinalContainerFound, observation.Image.ID) {
		t.Fatal("replacement final container outside the journal binding was accepted")
	}
}

func TestCommittedV2PrivateSwitchObserveRecoveryCoreAndSnapshot(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology {
		return gatewayTopologyExactFinalV2
	}
	manager.gatewayCandidateObserver = func(context.Context, gatewayV2RouteState, gatewayMigrationJournal, string, gatewayV2AppRoute) error {
		return nil
	}

	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := before.Apps[upgradeTestAppA]
	request := generatedroute.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedidentity.SlotGreen,
		Endpoints: []generatedroute.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		},
	}
	if err := exerciseManagerSwitch(manager, context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after, afterJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pending != nil || after.Apps[upgradeTestAppA].Route.Slot != generatedidentity.SlotGreen || afterJournal.Phase != gatewayPhaseCommitted {
		t.Fatalf("committed v2 state = %#v journal = %#v", after, afterJournal)
	}
	if _, ok := runner.files[journal.Resources.FinalContainerID+":"+"/config/active.json"]; !ok {
		t.Fatal("v2 restart config was not installed")
	}

	observation, err := manager.Observe(context.Background(), upgradeTestAppA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Slot != generatedidentity.SlotGreen || observation.URL != "http://"+upgradeTestAppA+".rig.localhost:8080" {
		t.Fatalf("observation = %#v", observation)
	}
	if err := exerciseCommittedV2RecoveryCore(manager, context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryAvailableBytes != 1073741824 || snapshot.DiskAvailableBytes != 4294967296 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	for _, command := range runner.commands {
		for _, argument := range command {
			if argument == caddyContainerName {
				t.Fatalf("committed v2 journey issued a v1 command: %v", command)
			}
		}
	}
}

func TestCommittedV2UnknownTopologyStopsBeforeDockerMutation(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology {
		return gatewayTopologyUnknownOrDrift
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := state.Apps[upgradeTestAppA]
	err = exerciseManagerSwitch(manager, context.Background(), generatedroute.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedidentity.SlotGreen,
		Endpoints: []generatedroute.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		},
	})
	if !IsCode(err, DiagnosticRouteUnresolved) || !testRouteCandidateMayBeLive(err) {
		t.Fatalf("switch error = %v candidate may be live = %t", err, testRouteCandidateMayBeLive(err))
	}
	if len(runner.commands) != 0 {
		t.Fatalf("unknown topology caused Docker mutation: %v", runner.commands)
	}
	retained, retainedJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(retained, state) || !reflect.DeepEqual(retainedJournal, journal) {
		t.Fatalf("durable state changed: state=%#v journal=%#v err=%v", retained, retainedJournal, loadErr)
	}
}

func TestCommittedV2RecoveryRollsBackDurableProposedRoute(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := cloneGatewayV2AppRoute(state.Apps[upgradeTestAppA])
	proposed := gatewayV2AppRoute{Route: routeRecord{Slot: generatedidentity.SlotGreen, Endpoints: []generatedroute.RouteEndpoint{
		endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
		endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
	}}}
	pending := cloneGatewayV2RouteState(state)
	pending.Pending = &gatewayV2PendingRoute{AppID: upgradeTestAppA, Previous: &previous, Proposed: proposed}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}

	observations := 0
	manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
		observations++
		slot := candidate.Apps[upgradeTestAppA].Route.Slot
		switch observations {
		case 1:
			if slot == generatedidentity.SlotBlue {
				return gatewayTopologyUnknownOrDrift
			}
		case 2:
			if slot == generatedidentity.SlotGreen {
				return gatewayTopologyExactFinalV2
			}
		default:
			if slot == generatedidentity.SlotBlue {
				return gatewayTopologyExactFinalV2
			}
		}
		return gatewayTopologyUnknownOrDrift
	}
	if err := exerciseCommittedV2RecoveryCore(manager, context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Pending != nil || !reflect.DeepEqual(recovered.Apps[upgradeTestAppA], previous) {
		t.Fatalf("recovered state = %#v", recovered)
	}
	if _, ok := runner.files[journal.Resources.FinalContainerID+":"+"/config/rollback.json"]; !ok {
		t.Fatal("recovery did not install the committed rollback config")
	}
}

func TestCommittedV2RecoveryRepairsReloadOnlyCrashWindow(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := cloneGatewayV2AppRoute(state.Apps[upgradeTestAppA])
	proposed := gatewayV2AppRoute{Route: routeRecord{Slot: generatedidentity.SlotGreen, Endpoints: []generatedroute.RouteEndpoint{
		endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
		endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
	}}}
	pending := cloneGatewayV2RouteState(state)
	pending.Pending = &gatewayV2PendingRoute{AppID: upgradeTestAppA, Previous: &previous, Proposed: proposed}
	if err := store.saveCommittedV2State(pending, journal); err != nil {
		t.Fatal(err)
	}

	mixedCalls := 0
	manager.gatewayMixedRestartObserver = func(_ context.Context, _ routeState, committed, candidate gatewayV2RouteState, _ gatewayMigrationJournal) bool {
		mixedCalls++
		if committed.Apps[upgradeTestAppA].Route.Slot != generatedidentity.SlotBlue || candidate.Apps[upgradeTestAppA].Route.Slot != generatedidentity.SlotGreen {
			t.Fatal("mixed recovery did not derive the exact committed and proposed states")
		}
		return true
	}
	manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
		_, rollbackInstalled := runner.files[journal.Resources.FinalContainerID+":"+"/config/rollback.json"]
		if rollbackInstalled && candidate.Apps[upgradeTestAppA].Route.Slot == generatedidentity.SlotBlue {
			return gatewayTopologyExactFinalV2
		}
		return gatewayTopologyUnknownOrDrift
	}
	if err := exerciseCommittedV2RecoveryCore(manager, context.Background()); err != nil {
		t.Fatal(err)
	}
	if mixedCalls != 1 {
		t.Fatalf("mixed topology observations = %d", mixedCalls)
	}
	recovered, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Pending != nil || !reflect.DeepEqual(recovered.Apps[upgradeTestAppA], previous) {
		t.Fatalf("recovered state = %#v", recovered)
	}
	if _, ok := runner.files[journal.Resources.FinalContainerID+":"+"/config/rollback.json"]; !ok {
		t.Fatal("mixed crash recovery did not reload the committed config")
	}
}

func TestCommittedV2RejectsRogueCandidateBeforePendingOrReload(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology {
		return gatewayTopologyExactFinalV2
	}
	preflightCalls := 0
	manager.gatewayCandidateObserver = func(_ context.Context, _ gatewayV2RouteState, _ gatewayMigrationJournal, appID string, proposed gatewayV2AppRoute) error {
		preflightCalls++
		if appID != upgradeTestAppA || proposed.Route.Endpoints[0].ContainerID != strings.Repeat("f", 64) {
			t.Fatal("candidate preflight did not receive the exact proposed endpoint")
		}
		return &Error{Code: DiagnosticIngressDrift}
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := before.Apps[upgradeTestAppA]
	routeErr := exerciseManagerSwitch(manager, context.Background(), generatedroute.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedidentity.SlotGreen,
		Endpoints: []generatedroute.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'f'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		},
	})
	if !IsCode(routeErr, DiagnosticIngressDrift) || testRouteCandidateMayBeLive(routeErr) {
		t.Fatalf("switch error = %v candidate may be live = %t", routeErr, testRouteCandidateMayBeLive(routeErr))
	}
	if preflightCalls != 1 {
		t.Fatalf("candidate preflight calls = %d", preflightCalls)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("rogue candidate caused Docker work: %v", runner.commands)
	}
	after, afterJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterJournal, journal) {
		t.Fatalf("rogue candidate changed durable state: state=%#v journal=%#v err=%v", after, afterJournal, err)
	}
}

func TestCommittedV2RejectsUnattachedApplicationNetworkBeforePendingOrReload(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology {
		return gatewayTopologyExactFinalV2
	}
	manager.gatewayCandidateObserver = func(context.Context, gatewayV2RouteState, gatewayMigrationJournal, string, gatewayV2AppRoute) error {
		t.Fatal("live candidate proof ran for an unattached network")
		return nil
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := before.Apps[upgradeTestAppA]
	routeErr := exerciseManagerSwitch(manager, context.Background(), generatedroute.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedidentity.SlotGreen,
		Endpoints: []generatedroute.RouteEndpoint{
			endpoint("frontend", "static", "new-net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "new-net-a", "api-green", 3000, 'e'),
		},
	})
	if !IsCode(routeErr, DiagnosticRouteUnresolved) || testRouteCandidateMayBeLive(routeErr) {
		t.Fatalf("switch error = %v candidate may be live = %t", routeErr, testRouteCandidateMayBeLive(routeErr))
	}
	if len(runner.commands) != 0 {
		t.Fatalf("unattached network caused Docker work: %v", runner.commands)
	}
	after, afterJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterJournal, journal) {
		t.Fatalf("unattached network changed durable state: state=%#v journal=%#v err=%v", after, afterJournal, err)
	}
}

type generatedRoutePreviousCommittedV2ClearFixture struct {
	manager       *Manager
	runner        *committedV2Runner
	store         *gatewayUpgradeStateStore
	transition    *generatedRouteActivationGatewayTransition
	before        gatewayV2RouteState
	pending       gatewayV2RouteState
	effective     gatewayV2RouteState
	journal       gatewayMigrationJournal
	topologyCalls *int
	mixedCalls    *int
}

func newGeneratedRoutePreviousCommittedV2ClearFixture(t *testing.T) generatedRoutePreviousCommittedV2ClearFixture {
	t.Helper()
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)
	runner := newCommittedV2Runner()
	manager.runner = runner
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, journal, err := store.loadBoundUpgrade("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	previous := cloneGatewayV2AppRoute(before.Apps[upgradeTestAppA])
	proposed := cloneGatewayV2AppRoute(previous)
	proposed.Route = routeRecord{Slot: generatedidentity.SlotGreen,
		Endpoints: []generatedroute.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		}}
	pending := cloneGatewayV2RouteState(before)
	pending.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingRouteSwitch,
		AppID: upgradeTestAppA, Previous: &previous, Proposed: cloneGatewayV2AppRoute(proposed)}
	effective := cloneGatewayV2RouteState(before)
	effective.Apps[upgradeTestAppA] = cloneGatewayV2AppRoute(proposed)
	request := generatedroute.RouteSwitchRequest{AppID: upgradeTestAppA,
		FromSlot: previous.Route.Slot, ToSlot: proposed.Route.Slot,
		Endpoints: append([]generatedroute.RouteEndpoint(nil), proposed.Route.Endpoints...)}
	transition := &generatedRouteActivationGatewayTransition{kind: generatedDomainGatewayNativeV2,
		request: canonicalGeneratedRouteRequest(request),
		nativeV2: &generatedRouteActivationNativeV2Transition{
			before: cloneGatewayV2RouteState(before), pending: cloneGatewayV2RouteState(pending),
			effective: cloneGatewayV2RouteState(effective), journal: journal,
			operationID: before.OperationID,
		}}
	if !validGeneratedRouteActivationGatewayTransition(transition) {
		t.Fatal("committed V2 PreviousExact clear fixture produced an invalid transition")
	}
	return generatedRoutePreviousCommittedV2ClearFixture{manager: manager, runner: runner,
		store: store, transition: transition, before: before, pending: pending,
		effective: effective, journal: journal, topologyCalls: new(int), mixedCalls: new(int)}
}

func (f generatedRoutePreviousCommittedV2ClearFixture) installProtected(t *testing.T,
	state gatewayV2RouteState,
) {
	t.Helper()
	if err := f.store.saveCommittedV2State(state, f.journal); err != nil {
		t.Fatal(err)
	}
}

func (f generatedRoutePreviousCommittedV2ClearFixture) setPhysical(kind string) {
	f.manager.gatewayMixedRestartObserver = nil
	f.manager.gatewayTopologyObserver = func(_ context.Context, _ routeState,
		candidate gatewayV2RouteState, _ gatewayMigrationJournal,
	) gatewayObservedTopology {
		(*f.topologyCalls)++
		switch kind {
		case "previous":
			if reflect.DeepEqual(candidate, f.before) {
				return gatewayTopologyExactFinalV2
			}
		case "proposed":
			if reflect.DeepEqual(candidate, f.effective) {
				return gatewayTopologyExactFinalV2
			}
		}
		return gatewayTopologyUnknownOrDrift
	}
	if kind == "mixed" {
		f.manager.gatewayMixedRestartObserver = func(context.Context, routeState,
			gatewayV2RouteState, gatewayV2RouteState, gatewayMigrationJournal,
		) bool {
			(*f.mixedCalls)++
			return true
		}
	} else if kind == "unknown" {
		f.manager.gatewayMixedRestartObserver = func(context.Context, routeState,
			gatewayV2RouteState, gatewayV2RouteState, gatewayMigrationJournal,
		) bool {
			(*f.mixedCalls)++
			return false
		}
	}
}

func generatedRoutePreviousAssertCommittedV2ClearResult(t *testing.T,
	f generatedRoutePreviousCommittedV2ClearFixture, protected *gatewayV2RouteState,
	journalBytes []byte, files map[string][]byte, commandsBefore int,
	transitionBefore *generatedRouteActivationGatewayTransition,
	protectedBefore *generatedDomainProtectedStartupSnapshot, observerBefore, observerDelta [2]int,
) {
	t.Helper()
	if protected == nil {
		if _, err := os.Stat(f.store.v2Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing committed V2 protected state was recreated: %v", err)
		}
	} else {
		state, journal, err := f.store.loadBoundUpgrade(f.journal.OperationID)
		if err != nil || !reflect.DeepEqual(state, *protected) || !reflect.DeepEqual(journal, f.journal) {
			t.Fatalf("committed V2 PreviousExact protected state changed: state=%#v journal=%#v error=%v",
				state, journal, err)
		}
	}
	afterJournal, err := os.ReadFile(f.store.journalPath)
	if err != nil || !reflect.DeepEqual(afterJournal, journalBytes) {
		t.Fatalf("committed V2 PreviousExact clear changed its journal bytes: %v", err)
	}
	if !reflect.DeepEqual(f.runner.files, files) || len(f.runner.commands) != commandsBefore {
		t.Fatalf("committed V2 PreviousExact clear issued physical work: files=%#v commands=%v",
			f.runner.files, f.runner.commands[commandsBefore:])
	}
	if *f.topologyCalls != observerBefore[0]+observerDelta[0] ||
		*f.mixedCalls != observerBefore[1]+observerDelta[1] {
		t.Fatalf("committed V2 PreviousExact observer calls=%d/%d before=%v delta=%v",
			*f.topologyCalls, *f.mixedCalls, observerBefore, observerDelta)
	}
	if protectedBefore != nil {
		after, err := readGeneratedDomainProtectedStartup(context.Background(), f.manager.options.DataRoot)
		if err != nil || !generatedRoutePreviousSameProtected(*protectedBefore, after) {
			t.Fatalf("committed V2 PreviousExact refusal/idempotent path rewrote protected artifacts: %v", err)
		}
	}
	if !reflect.DeepEqual(f.transition, transitionBefore) {
		t.Fatal("committed V2 PreviousExact clear mutated its sealed transition input")
	}
}

func generatedRoutePreviousCommittedV2Snapshots(t *testing.T,
	f generatedRoutePreviousCommittedV2ClearFixture,
) ([]byte, map[string][]byte, int, *generatedRouteActivationGatewayTransition, [2]int) {
	t.Helper()
	journal, err := os.ReadFile(f.store.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string][]byte, len(f.runner.files))
	for name, body := range f.runner.files {
		files[name] = append([]byte(nil), body...)
	}
	return journal, files, len(f.runner.commands), cloneGeneratedRouteActivationGatewayTransition(f.transition),
		[2]int{*f.topologyCalls, *f.mixedCalls}
}

func generatedRoutePreviousCommittedV2ProtectedSnapshot(t *testing.T,
	f generatedRoutePreviousCommittedV2ClearFixture,
) generatedDomainProtectedStartupSnapshot {
	t.Helper()
	value, err := readGeneratedDomainProtectedStartup(context.Background(), f.manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGeneratedRouteActivationPreviousExactCommittedV2ClearMatrix(t *testing.T) {
	t.Run("exact_previous_clears", func(t *testing.T) {
		f := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		f.installProtected(t, f.pending)
		f.setPhysical("previous")
		journal, files, commands, transition, observers := generatedRoutePreviousCommittedV2Snapshots(t, f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.transition); err != nil {
			t.Fatal(err)
		}
		generatedRoutePreviousAssertCommittedV2ClearResult(t, f, &f.before,
			journal, files, commands, transition, nil, observers, [2]int{2, 0})
	})

	t.Run("already_cleared_is_idempotent", func(t *testing.T) {
		f := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		f.installProtected(t, f.pending)
		f.installProtected(t, f.before)
		f.setPhysical("previous")
		journal, files, commands, transition, observers := generatedRoutePreviousCommittedV2Snapshots(t, f)
		protected := generatedRoutePreviousCommittedV2ProtectedSnapshot(t, f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.transition); err != nil {
			t.Fatal(err)
		}
		generatedRoutePreviousAssertCommittedV2ClearResult(t, f, &f.before,
			journal, files, commands, transition, &protected, observers, [2]int{1, 0})
	})

	t.Run("crossed_protected_state_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		crossed := cloneGatewayV2RouteState(f.pending)
		crossed.Pending.Proposed.Route.Endpoints[0] =
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'f')
		f.installProtected(t, crossed)
		f.setPhysical("previous")
		journal, files, commands, transition, observers := generatedRoutePreviousCommittedV2Snapshots(t, f)
		protected := generatedRoutePreviousCommittedV2ProtectedSnapshot(t, f)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.transition); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("crossed committed V2 protected state error=%v", err)
		}
		generatedRoutePreviousAssertCommittedV2ClearResult(t, f, &crossed,
			journal, files, commands, transition, &protected, observers, [2]int{})
	})

	for _, test := range []struct{ name, physical string }{
		{"physical_proposed_refuses", "proposed"},
		{"physical_mixed_refuses", "mixed"},
		{"physical_unknown_refuses", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
			f.installProtected(t, f.pending)
			f.setPhysical(test.physical)
			journal, files, commands, transition, observers := generatedRoutePreviousCommittedV2Snapshots(t, f)
			protected := generatedRoutePreviousCommittedV2ProtectedSnapshot(t, f)
			if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.transition); !IsCode(err, DiagnosticRouteUnresolved) {
				t.Fatalf("committed V2 %s physical error=%v", test.physical, err)
			}
			delta := [2]int{2, 0}
			if test.physical == "mixed" || test.physical == "unknown" {
				delta[1] = 1
			}
			generatedRoutePreviousAssertCommittedV2ClearResult(t, f, &f.pending,
				journal, files, commands, transition, &protected, observers, delta)
		})
	}

	t.Run("protected_state_missing_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		f.installProtected(t, f.pending)
		f.setPhysical("previous")
		if err := os.Remove(f.store.v2Path); err != nil {
			t.Fatal(err)
		}
		journal, files, commands, transition, observers := generatedRoutePreviousCommittedV2Snapshots(t, f)
		protected := generatedRoutePreviousRawProtected(t, f.store.directory)
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(context.Background(), f.transition); !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("missing committed V2 protected state error=%v", err)
		}
		generatedRoutePreviousAssertCommittedV2ClearResult(t, f, nil,
			journal, files, commands, transition, nil, observers, [2]int{})
		if after := generatedRoutePreviousRawProtected(t, f.store.directory); !generatedRoutePreviousSameRawProtected(protected, after) {
			t.Fatal("missing committed V2 refusal changed remaining protected artifacts")
		}
	})

	t.Run("canceled_context_refuses", func(t *testing.T) {
		f := newGeneratedRoutePreviousCommittedV2ClearFixture(t)
		f.installProtected(t, f.pending)
		f.setPhysical("previous")
		journal, files, commands, transition, observers := generatedRoutePreviousCommittedV2Snapshots(t, f)
		protected := generatedRoutePreviousCommittedV2ProtectedSnapshot(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := f.manager.clearPreviousGeneratedDomainRouteActivation(ctx, f.transition); !IsCode(err, DiagnosticCancelled) {
			t.Fatalf("canceled committed V2 clear error=%v", err)
		}
		generatedRoutePreviousAssertCommittedV2ClearResult(t, f, &f.pending,
			journal, files, commands, transition, &protected, observers, [2]int{})
	})
}

func TestObservationBudgetsSeparateLockAcquisitionFromCommittedV2Probes(t *testing.T) {
	t.Run("committed v2 gets the larger post-lock budget", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		installCommittedV2Pair(t, manager)
		var observerRemaining time.Duration
		manager.gatewayTopologyObserver = func(ctx context.Context, _ routeState, _ gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("v2 observer context has no deadline")
			}
			observerRemaining = time.Until(deadline)
			return gatewayTopologyExactFinalV2
		}
		var callbackRemaining time.Duration
		err := manager.WithObservation(context.Background(), upgradeTestAppA, func(ctx context.Context, _ Observation) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("v2 callback context has no deadline")
			}
			callbackRemaining = time.Until(deadline)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if observerRemaining <= observationTimeout || callbackRemaining <= observationTimeout || observerRemaining > v2ObservationTimeout || callbackRemaining > v2ObservationTimeout {
			t.Fatalf("v2 budgets observer=%s callback=%s", observerRemaining, callbackRemaining)
		}
	})

	t.Run("v1 retains the total observation bound", func(t *testing.T) {
		manager, runner := newManagerFixture(t, false)
		if err := exerciseManagerSwitch(manager, context.Background(), switchRequest(runner)); err != nil {
			t.Fatal(err)
		}
		var callbackRemaining time.Duration
		err := manager.WithObservation(context.Background(), runner.appID, func(ctx context.Context, _ Observation) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("v1 callback context has no deadline")
			}
			callbackRemaining = time.Until(deadline)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if callbackRemaining <= 0 || callbackRemaining > observationTimeout {
			t.Fatalf("v1 callback budget = %s", callbackRemaining)
		}
	})

	t.Run("caller deadline still caps committed v2", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		installCommittedV2Pair(t, manager)
		callerBudget := 45 * time.Second
		callerContext, cancel := context.WithTimeout(context.Background(), callerBudget)
		defer cancel()
		var observerRemaining time.Duration
		manager.gatewayTopologyObserver = func(ctx context.Context, _ routeState, _ gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("caller-bounded v2 observer has no deadline")
			}
			observerRemaining = time.Until(deadline)
			return gatewayTopologyExactFinalV2
		}
		if err := manager.WithObservation(callerContext, upgradeTestAppA, func(context.Context, Observation) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if observerRemaining <= observationTimeout || observerRemaining > callerBudget {
			t.Fatalf("caller-bounded v2 budget = %s", observerRemaining)
		}
	})
}
