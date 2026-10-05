package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
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

func TestCommittedV2ManagerSwitchObserveRecoverAndSnapshot(t *testing.T) {
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
	request := generatedruntime.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedruntime.SlotGreen,
		Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		},
	}
	if err := manager.Switch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after, afterJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pending != nil || after.Apps[upgradeTestAppA].Route.Slot != generatedruntime.SlotGreen || afterJournal.Phase != gatewayPhaseCommitted {
		t.Fatalf("committed v2 state = %#v journal = %#v", after, afterJournal)
	}
	if _, ok := runner.files[journal.Resources.FinalContainerID+":"+"/config/active.json"]; !ok {
		t.Fatal("v2 restart config was not installed")
	}

	observation, err := manager.Observe(context.Background(), upgradeTestAppA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Slot != generatedruntime.SlotGreen || observation.URL != "http://"+upgradeTestAppA+".rig.localhost:8080" {
		t.Fatalf("observation = %#v", observation)
	}
	if err := manager.Recover(context.Background()); err != nil {
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
	err = manager.Switch(context.Background(), generatedruntime.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedruntime.SlotGreen,
		Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		},
	})
	if !IsCode(err, DiagnosticRouteUnresolved) || !generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("switch error = %v candidate may be live = %t", err, generatedruntime.RouteCandidateMayBeLive(err))
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
	proposed := gatewayV2AppRoute{Route: routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
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
			if slot == generatedruntime.SlotBlue {
				return gatewayTopologyUnknownOrDrift
			}
		case 2:
			if slot == generatedruntime.SlotGreen {
				return gatewayTopologyExactFinalV2
			}
		default:
			if slot == generatedruntime.SlotBlue {
				return gatewayTopologyExactFinalV2
			}
		}
		return gatewayTopologyUnknownOrDrift
	}
	if err := manager.Recover(context.Background()); err != nil {
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
	proposed := gatewayV2AppRoute{Route: routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
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
		if committed.Apps[upgradeTestAppA].Route.Slot != generatedruntime.SlotBlue || candidate.Apps[upgradeTestAppA].Route.Slot != generatedruntime.SlotGreen {
			t.Fatal("mixed recovery did not derive the exact committed and proposed states")
		}
		return true
	}
	manager.gatewayTopologyObserver = func(_ context.Context, _ routeState, candidate gatewayV2RouteState, _ gatewayMigrationJournal) gatewayObservedTopology {
		_, rollbackInstalled := runner.files[journal.Resources.FinalContainerID+":"+"/config/rollback.json"]
		if rollbackInstalled && candidate.Apps[upgradeTestAppA].Route.Slot == generatedruntime.SlotBlue {
			return gatewayTopologyExactFinalV2
		}
		return gatewayTopologyUnknownOrDrift
	}
	if err := manager.Recover(context.Background()); err != nil {
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
	routeErr := manager.Switch(context.Background(), generatedruntime.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedruntime.SlotGreen,
		Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "net-a", "frontend-green", 4173, 'f'),
			endpoint("api", "server", "net-a", "api-green", 3000, 'e'),
		},
	})
	if !IsCode(routeErr, DiagnosticIngressDrift) || generatedruntime.RouteCandidateMayBeLive(routeErr) {
		t.Fatalf("switch error = %v candidate may be live = %t", routeErr, generatedruntime.RouteCandidateMayBeLive(routeErr))
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
	routeErr := manager.Switch(context.Background(), generatedruntime.RouteSwitchRequest{
		AppID: upgradeTestAppA, FromSlot: previous.Route.Slot, ToSlot: generatedruntime.SlotGreen,
		Endpoints: []generatedruntime.RouteEndpoint{
			endpoint("frontend", "static", "new-net-a", "frontend-green", 4173, 'd'),
			endpoint("api", "server", "new-net-a", "api-green", 3000, 'e'),
		},
	})
	if !IsCode(routeErr, DiagnosticRouteUnresolved) || generatedruntime.RouteCandidateMayBeLive(routeErr) {
		t.Fatalf("switch error = %v candidate may be live = %t", routeErr, generatedruntime.RouteCandidateMayBeLive(routeErr))
	}
	if len(runner.commands) != 0 {
		t.Fatalf("unattached network caused Docker work: %v", runner.commands)
	}
	after, afterJournal, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterJournal, journal) {
		t.Fatalf("unattached network changed durable state: state=%#v journal=%#v err=%v", after, afterJournal, err)
	}
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
		if err := manager.Switch(context.Background(), switchRequest(runner)); err != nil {
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
