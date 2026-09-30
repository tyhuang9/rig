package generatedingress

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
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
		result <- second.Switch(context.Background(), switchRequest(runner))
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
		if !IsCode(err, DiagnosticRouteUnresolved) || !generatedruntime.RouteCandidateMayBeLive(err) {
			t.Fatalf("switch error = %v, candidate may be live = %t", err, generatedruntime.RouteCandidateMayBeLive(err))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("contending switch did not finish")
	}
	if len(runner.commands) != 0 {
		t.Fatalf("contending switch issued Docker commands after marker appeared: %v", runner.commands)
	}
}

func TestLegacyV1FenceAllowsLegacyPathsWithoutMarkers(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	ctx := context.Background()
	if err := manager.Switch(ctx, switchRequest(runner)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Recover(ctx); err != nil {
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
	var capacitySnapshot generatedruntime.CapacitySnapshot
	actions := []struct {
		name             string
		candidateMayLive bool
		run              func() error
	}{
		{name: "switch", candidateMayLive: true, run: func() error {
			return manager.Switch(context.Background(), switchRequest(runner))
		}},
		{name: "provision", run: func() error { return manager.Provision(context.Background()) }},
		{name: "recover", run: func() error { return manager.Recover(context.Background()) }},
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
			if action.candidateMayLive && !generatedruntime.RouteCandidateMayBeLive(err) {
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
	if capacitySnapshot != (generatedruntime.CapacitySnapshot{}) {
		t.Fatalf("blocked capacity snapshot = %+v", capacitySnapshot)
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
		if _, err := store.transitionMigrationJournal(input.OperationID, phase, next); err != nil {
			t.Fatal(err)
		}
		phase = next
	}
}

func pendingV1State(runner *ingressRunner) routeState {
	previous := routeRecord{Slot: generatedruntime.SlotGreen, Endpoints: []generatedruntime.RouteEndpoint{
		endpoint("web", "server", runner.network, "web-green", 3000, 'c'),
	}}
	proposed := routeRecord{Slot: generatedruntime.SlotBlue, Endpoints: []generatedruntime.RouteEndpoint{runner.endpoint}}
	return routeState{
		Version: stateVersion,
		Active:  map[string]routeRecord{runner.appID: previous},
		Pending: &pendingRoute{AppID: runner.appID, Previous: &previous, Proposed: proposed},
	}
}
