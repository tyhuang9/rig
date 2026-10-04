package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestNewRejectsMissingRebindFenceBeforeStateWrites(t *testing.T) {
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "unwritten-state")
	_, err = New(&ingressRunner{}, Options{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, DataRoot: stateRoot,
	})
	if err == nil {
		t.Fatal("missing rebind fence check was accepted")
	}
	if _, statErr := os.Stat(stateRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state was written before rebind fence validation: %v", statErr)
	}
}

func TestGatewayLockChecksRebindFenceWhileBothLocksAreHeldAndReleasesOnFailure(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	original := managerAcquireGatewayOSLock
	osLockHeld := false
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		release, err := original(ctx, store)
		if err != nil {
			return nil, err
		}
		osLockHeld = true
		return func() error {
			osLockHeld = false
			return release()
		}, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = original })

	fenceFailure := errors.New("injected active rebind claim")
	manager.options.RebindFenceCheck = func(context.Context) error {
		if manager.mu.TryLock() {
			manager.mu.Unlock()
			t.Error("rebind fence ran without Manager lock")
		}
		if !osLockHeld {
			t.Error("rebind fence ran without gateway OS lock")
		}
		return fenceFailure
	}
	if release, err := manager.lockGateway(context.Background()); release != nil || !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("release present=%t err=%v", release != nil, err)
	}
	if osLockHeld || !manager.mu.TryLock() {
		t.Fatalf("failed fence retained a lock: os=%t", osLockHeld)
	}
	manager.mu.Unlock()

	manager.options.RebindFenceCheck = func(context.Context) error { return nil }
	release, err := manager.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayLockReleasesWhenFenceCancelsContext(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	manager.options.RebindFenceCheck = func(context.Context) error {
		cancel()
		return nil
	}
	if release, err := manager.lockGateway(ctx); release != nil || !IsCode(err, DiagnosticCancelled) {
		t.Fatalf("release present=%t err=%v", release != nil, err)
	}
	if !manager.mu.TryLock() {
		t.Fatal("cancelled fence retained Manager lock")
	}
	manager.mu.Unlock()
}

func TestRebindFenceBlocksNormalGatewayEffects(t *testing.T) {
	fenceFailure := errors.New("injected active rebind claim")

	for _, method := range []string{"switch", "provision"} {
		t.Run(method, func(t *testing.T) {
			manager, runner := newManagerFixture(t, false)
			manager.options.RebindFenceCheck = func(context.Context) error { return fenceFailure }
			before := len(runner.commands)
			var err error
			if method == "switch" {
				err = manager.Switch(context.Background(), switchRequest(runner))
			} else {
				err = manager.Provision(context.Background())
			}
			if !IsCode(err, DiagnosticRouteUnresolved) || len(runner.commands) != before {
				t.Fatalf("err=%v Docker commands=%d", err, len(runner.commands)-before)
			}
		})
	}

	t.Run("gateway v2 stage", func(t *testing.T) {
		manager, store, state, driver := gatewayV2StageTestFixture(t)
		before, beforeJournal, err := store.loadBoundUpgrade(state.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		manager.options.RebindFenceCheck = func(context.Context) error { return fenceFailure }
		err = manager.StageGatewayV2(context.Background(), state.OperationID)
		after, afterJournal, loadErr := store.loadBoundUpgrade(state.OperationID)
		if !IsCode(err, DiagnosticRouteUnresolved) || loadErr != nil || len(driver.events) != 0 ||
			!reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterJournal, beforeJournal) {
			t.Fatalf("err=%v load=%v events=%v", err, loadErr, driver.events)
		}
	})

	t.Run("LAN grant", func(t *testing.T) {
		manager, store, state, journal, request, driver := gatewayV2LANGrantFixture(t)
		authorize, leases := allowGatewayV2LANGrant(t, manager, request, nil)
		manager.options.RebindFenceCheck = func(context.Context) error { return fenceFailure }
		_, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize)
		after, afterJournal, loadErr := store.loadBoundUpgrade(journal.OperationID)
		if !IsCode(err, DiagnosticRouteUnresolved) || loadErr != nil || len(*leases) != 0 || len(driver.events) != 0 ||
			!reflect.DeepEqual(after, state) || !reflect.DeepEqual(afterJournal, journal) {
			t.Fatalf("err=%v load=%v leases=%d events=%v", err, loadErr, len(*leases), driver.events)
		}
	})
}

func TestIndependentManagersDoNoGatewayWorkWhileAnotherHoldsTheLock(t *testing.T) {
	first, runner := newManagerFixture(t, false)
	second, err := New(runner, first.options)
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.lockGateway(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	before := len(runner.commands)
	methods := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "provision", run: second.Provision},
		{name: "switch", run: func(ctx context.Context) error {
			return second.Switch(ctx, switchRequest(runner))
		}},
		{name: "observation", run: func(ctx context.Context) error {
			return second.WithObservation(ctx, runner.appID, func(context.Context, Observation) error {
				t.Error("observation callback ran without the gateway lock")
				return nil
			})
		}},
		{name: "capacity", run: func(ctx context.Context) error {
			_, err := second.Snapshot(ctx)
			return err
		}},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			if err := method.run(ctx); !IsCode(err, DiagnosticCancelled) {
				t.Fatalf("competing method error = %v, want cancelled", err)
			}
			if len(runner.commands) != before {
				t.Fatalf("competing method issued %d Docker commands", len(runner.commands)-before)
			}
		})
	}
}

func TestSwitchReleaseFailurePreservesLiveCandidate(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	original := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		release, err := original(ctx, store)
		if err != nil {
			return nil, err
		}
		return func() error {
			if err := release(); err != nil {
				return err
			}
			return errors.New("injected post-switch release failure")
		}, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = original })

	err := manager.Switch(context.Background(), switchRequest(runner))
	if !IsCode(err, DiagnosticRouteUnresolved) || !generatedruntime.RouteCandidateMayBeLive(err) {
		t.Fatalf("switch error=%v candidateMayBeLive=%t", err, generatedruntime.RouteCandidateMayBeLive(err))
	}
	state, loadErr := manager.store.load()
	if loadErr != nil || state.Active[runner.appID].Slot != generatedruntime.SlotBlue {
		t.Fatalf("serving route after release fault=%#v err=%v", state.Active[runner.appID], loadErr)
	}
}
