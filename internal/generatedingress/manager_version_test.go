package generatedingress

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
)

func TestLegacyV1FenceRejectsCommittedV2PairBeforeAllLegacyWork(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)

	assertLegacyV1Blocked(t, manager, runner)
}

func TestLegacyV1FenceRejectsEachOrphanedCorruptMarkerWithoutRollback(t *testing.T) {
	for _, marker := range []string{v2RouteStateFilename, gatewayMigrationFilename} {
		t.Run(marker, func(t *testing.T) {
			manager, runner := newManagerFixture(t, false)
			pending := pendingV1State(runner)
			if err := manager.store.save(pending); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(manager.store.root, marker), []byte("not a protected bundle"), 0o600); err != nil {
				t.Fatal(err)
			}

			assertLegacyV1Blocked(t, manager, runner)

			installed, err := manager.store.load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(installed.Pending, pending.Pending) || !reflect.DeepEqual(installed.Active, pending.Active) {
				t.Fatalf("pending v1 state changed behind fence: %#v", installed)
			}
		})
	}
}

func TestLegacyV1FenceRejectsDirectoryMarker(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	if err := os.Mkdir(filepath.Join(manager.store.root, gatewayMigrationFilename), 0o700); err != nil {
		t.Fatal(err)
	}
	assertLegacyV1Blocked(t, manager, runner)
}

func TestLegacyV1FenceChecksMarkerAfterGatewayLockContention(t *testing.T) {
	first, runner := newManagerFixture(t, false)
	second, err := New(runner, first.options)
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = release()
		}
	}()

	original := managerAcquireGatewayOSLock
	entered := make(chan struct{})
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		close(entered)
		return original(ctx, store)
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = original })

	result := make(chan error, 1)
	go func() {
		result <- exerciseManagerSwitch(second, context.Background(), switchRequest(runner))
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("contending switch did not reach the gateway lock")
	}
	if err := os.WriteFile(filepath.Join(first.store.root, v2RouteStateFilename), []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	released = true

	select {
	case err := <-result:
		if !IsCode(err, DiagnosticRouteUnresolved) || !testRouteCandidateMayBeLive(err) {
			t.Fatalf("switch error = %v, candidate may be live = %t", err, testRouteCandidateMayBeLive(err))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("contending switch did not finish")
	}
	if len(runner.commands) != 0 {
		t.Fatalf("contending switch issued Docker commands after marker appeared: %v", runner.commands)
	}
}

func TestLegacyV1FenceAllowsPrivateRouteAndReadPathsWithoutMarkers(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	ctx := context.Background()
	if err := exerciseManagerSwitch(manager, ctx, switchRequest(runner)); err != nil {
		t.Fatal(err)
	}
	if err := exerciseLegacyV1FenceCore(manager, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Observe(ctx, runner.appID); err != nil {
		t.Fatal(err)
	}
	callbackRan := false
	if err := manager.WithObservation(ctx, runner.appID, func(context.Context, Observation) error {
		callbackRan = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !callbackRan {
		t.Fatal("legacy observation callback did not run")
	}
	snapshot, err := manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MemoryAvailableBytes == 0 || snapshot.DiskAvailableBytes == 0 {
		t.Fatalf("legacy capacity snapshot = %+v", snapshot)
	}
}

func assertLegacyV1Blocked(t *testing.T, manager *Manager, runner *ingressRunner) {
	t.Helper()
	callbackRan := false
	var capacitySnapshot generatedcapacity.Snapshot
	actions := []struct {
		name             string
		candidateMayLive bool
		run              func() error
	}{
		{name: "switch", candidateMayLive: true, run: func() error {
			return exerciseManagerSwitch(manager, context.Background(), switchRequest(runner))
		}},
		{name: "legacy format fence", run: func() error {
			return exerciseLegacyV1FenceCore(manager, context.Background())
		}},
		{name: "observe", run: func() error {
			_, err := manager.Observe(context.Background(), runner.appID)
			return err
		}},
		{name: "with observation", run: func() error {
			return manager.WithObservation(context.Background(), runner.appID, func(context.Context, Observation) error {
				callbackRan = true
				return nil
			})
		}},
		{name: "capacity", run: func() error {
			var err error
			capacitySnapshot, err = manager.Snapshot(context.Background())
			return err
		}},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			before := len(runner.commands)
			err := action.run()
			if !IsCode(err, DiagnosticRouteUnresolved) {
				t.Fatalf("error = %v, want %s", err, DiagnosticRouteUnresolved)
			}
			if action.candidateMayLive && !testRouteCandidateMayBeLive(err) {
				t.Fatalf("switch error did not retain candidate: %v", err)
			}
			if len(runner.commands) != before {
				t.Fatalf("issued Docker commands behind v1 fence: %v", runner.commands[before:])
			}
		})
	}
	if callbackRan {
		t.Fatal("observation callback ran behind v1 fence")
	}
	if capacitySnapshot != (generatedcapacity.Snapshot{}) {
		t.Fatalf("blocked capacity snapshot = %+v", capacitySnapshot)
	}
}

// These public methods always refuse. Private primitive tests above preserve
// format and physical-state semantics; they do not grant ordinary Managers
// domain construction, recovery or generated route authority.
func TestOrdinaryManagerMutatorsRefuseEveryShapeBeforeGatewayWork(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "legacy"
		if committed {
			name = "committed v2"
		}
		t.Run(name, func(t *testing.T) {
			manager, runner := newManagerFixture(t, false)
			if committed {
				installCommittedV2Pair(t, manager)
			}
			copied := *manager
			shapes := []struct {
				name    string
				manager *Manager
			}{
				{name: "nil"},
				{name: "zero", manager: &Manager{}},
				{name: "constructed", manager: manager},
				{name: "copied", manager: &copied},
			}
			original := managerAcquireGatewayOSLock
			lockCalls := 0
			managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
				lockCalls++
				return nil, &Error{Code: DiagnosticRouteUnresolved}
			}
			t.Cleanup(func() { managerAcquireGatewayOSLock = original })
			before := len(runner.commands)
			for _, shape := range shapes {
				t.Run(shape.name, func(t *testing.T) {
					for _, ctx := range []context.Context{context.Background(), nil} {
						results := []error{
							shape.manager.Switch(ctx, switchRequest(runner)),
							shape.manager.Provision(ctx),
							shape.manager.Recover(ctx),
						}
						for index, err := range results {
							if !IsCode(err, DiagnosticValidationFailed) || testRouteCandidateMayBeLive(err) {
								t.Fatalf("ordinary mutator %d error=%v candidate may be live=%t", index, err, testRouteCandidateMayBeLive(err))
							}
						}
					}
				})
			}
			if lockCalls != 0 || len(runner.commands) != before {
				t.Fatalf("refused ordinary mutation acquired gateway lock %d times or issued %d commands", lockCalls, len(runner.commands)-before)
			}
		})
	}
}

func installCommittedV2Pair(t *testing.T, manager *Manager) {
	t.Helper()
	source, input := upgradeTestPreparation(t)
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	phase := gatewayPhasePrepared
	for _, next := range []gatewayMigrationPhase{
		gatewayPhaseStageIntent,
		gatewayPhaseStaged,
		gatewayPhaseTransferIntent,
		gatewayPhaseV2Serving,
		gatewayPhaseCommitted,
	} {
		if next == gatewayPhaseStaged {
			bindUpgradeStageResources(t, store, input.OperationID)
		}
		if next == gatewayPhaseV2Serving {
			bindUpgradeFinalResource(t, store, input.OperationID)
		}
		if _, err := store.transitionMigrationJournal(input.OperationID, phase, next); err != nil {
			t.Fatal(err)
		}
		phase = next
	}
	// Tests that specifically assert the legacy fence use an unresolved v2
	// topology and therefore must not reach Docker. Committed-v2 manager tests
	// replace this observer with an exact topology.
	manager.gatewayTopologyObserver = func(context.Context, routeState, gatewayV2RouteState, gatewayMigrationJournal) gatewayObservedTopology {
		return gatewayTopologyUnknownOrDrift
	}
}

func pendingV1State(runner *ingressRunner) routeState {
	previous := routeRecord{Slot: generatedidentity.SlotGreen, Endpoints: []generatedroute.RouteEndpoint{
		endpoint("web", "server", runner.network, "web-green", 3000, 'c'),
	}}
	proposed := routeRecord{Slot: generatedidentity.SlotBlue, Endpoints: []generatedroute.RouteEndpoint{runner.endpoint}}
	return routeState{
		Version: stateVersion,
		Active:  map[string]routeRecord{runner.appID: previous},
		Pending: &pendingRoute{AppID: runner.appID, Previous: &previous, Proposed: proposed},
	}
}
