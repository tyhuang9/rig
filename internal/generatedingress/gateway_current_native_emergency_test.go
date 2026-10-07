package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentNativeEmergencyFallbackRetainsAbortedRebindHistory(t *testing.T) {
	checkGatewayCurrentNativeEmergencyRetainedAbort(t, "")
}

func TestGatewayCurrentNativeEmergencyRetainedAbortSurvivesCorruptNativeRoute(t *testing.T) {
	for _, route := range []string{"source", "native v2"} {
		t.Run(route, func(t *testing.T) {
			checkGatewayCurrentNativeEmergencyRetainedAbort(t, route)
		})
	}
}

func checkGatewayCurrentNativeEmergencyRetainedAbort(t *testing.T, corruptRoute string) {
	t.Helper()
	ctx := context.Background()
	f := gatewayCurrentNativeEmergencyAbortedFixture(t)
	if corruptRoute != "" {
		path, purpose := f.manager.store.path, statePurpose
		if corruptRoute == "native v2" {
			store, err := newGatewayUpgradeStateStore(f.manager.options.DataRoot)
			if err != nil {
				t.Fatal(err)
			}
			path, purpose = store.v2Path, store.v2Purpose
		}
		if err := upgradeProtectedWrite(path, purpose, []byte("{")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("normal serving history accepted native route corruption")
		}
	}
	before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	// The emergency path must work without any readable SQL authority.
	f.manager.options.RebindFenceCheck = func(context.Context) error { return errors.New("SQL unavailable") }
	f.manager.options.RebindCurrentStateRepository = nil
	current, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
	if err != nil || !current.ProtectedRebindHistoryPresent || current.RebindOwnershipPresent ||
		current.UnresolvedProtectedRebindHistory || current.Incomplete || current.OwnershipIndeterminate || current.VerifiedTargets != 0 {
		t.Fatalf("aborted protected census did not permit native selection: result=%+v err=%v", current, err)
	}
	native := &fakeGatewayV2LANGrantDriver{t: t, manager: f.manager}
	f.manager.gatewayV2LANGrantDriver = native
	if err := f.manager.stopOwnedGatewayV2OnStartupFailure(ctx); err != nil || !native.gatewayStopped ||
		!reflect.DeepEqual(native.events, []string{"stop_owned_gateway"}) {
		t.Fatalf("retained abort prevented exact journal-bound native withdrawal: events=%v err=%v", native.events, err)
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(before, after) {
		t.Fatal("native emergency fallback rewrote retained abort history")
	}
}

func gatewayCurrentNativeEmergencyAbortedFixture(t *testing.T) gatewayRebindPredecessorFixture {
	t.Helper()
	f, input, physical := newGatewayRebindCoordinatorFixture(t)
	f.manager.gatewayRebindV2NetworkObserver = func(context.Context, appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("successor network unavailable")
	}
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, physical)
	if err != nil || result.FinalPhase != appaccess.GatewayRebindRolledBack || !result.FenceReleased {
		t.Fatalf("real SQL/protected no-effect abort: result=%+v err=%v", result, err)
	}
	return f
}

func TestGatewayCurrentNativeEmergencyRefusesTypedCommittedOwnership(t *testing.T) {
	f, input, physical := newGatewayRebindCoordinatorFixture(t)
	if _, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, physical); err != nil {
		t.Fatal(err)
	}
	f.manager.options.RebindFenceCheck = func(context.Context) error { return errors.New("SQL unavailable") }
	f.manager.options.RebindCurrentStateRepository = nil
	currentDriver := &gatewayCurrentStartupOwnedStopDriver{}
	f.manager.gatewayCurrentPhysicalDriver = currentDriver
	current, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err != nil || !current.ProtectedRebindHistoryPresent || !current.RebindOwnershipPresent || current.Incomplete ||
		current.VerifiedTargets != 1 || current.StoppedOrAbsentTargets != 1 || len(currentDriver.targets) != 1 {
		t.Fatalf("typed committed ownership was not enumerated: result=%+v err=%v", current, err)
	}
	native := &fakeGatewayV2LANGrantDriver{t: t, manager: f.manager}
	f.manager.gatewayV2LANGrantDriver = native
	if err := f.manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err == nil || len(native.events) != 0 {
		t.Fatal("typed current ownership allowed native predecessor fallback")
	}
}

func TestGatewayCurrentNativeEmergencyPreservesCorruptRouteWithdrawal(t *testing.T) {
	for _, route := range []string{"source", "native v2"} {
		t.Run(route, func(t *testing.T) {
			manager, store, _, _, _, driver := gatewayV2LANGrantFixture(t)
			manager.gatewayRebindFailStop = &atomic.Bool{}
			path, purpose := store.v2Path, store.v2Purpose
			if route == "source" {
				path, purpose = manager.store.path, statePurpose
			}
			if err := upgradeProtectedWrite(path, purpose, []byte("{")); err != nil {
				t.Fatal(err)
			}
			if route == "source" {
				if _, err := readGatewayRebindProtectedPresenceReadOnly(manager.options.DataRoot); err == nil {
					t.Fatal("serving presence reader accepted corrupt native source")
				}
			}
			current, err := manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
			if err != nil || current != (GatewayCurrentStartupEmergencyStopResult{}) {
				t.Fatalf("known native route corruption became unknown rebind ownership: result=%+v err=%v", current, err)
			}
			if err := manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err != nil || !driver.gatewayStopped {
				t.Fatalf("journal-bound native withdrawal failed: %v", err)
			}
		})
	}
}

type gatewayCurrentNativeEmergencyAfterStopDriver struct {
	*fakeGatewayV2LANGrantDriver
	after func()
}

func (d gatewayCurrentNativeEmergencyAfterStopDriver) stopOwnedGateway(ctx context.Context, journal gatewayMigrationJournal) error {
	err := d.fakeGatewayV2LANGrantDriver.stopOwnedGateway(ctx, journal)
	d.after()
	return err
}

func TestGatewayCurrentNativeEmergencyRefusesHistoryChangeAfterStop(t *testing.T) {
	manager, _, _, _, _, driver := gatewayV2LANGrantFixture(t)
	manager.gatewayRebindFailStop = &atomic.Bool{}
	manager.gatewayV2LANGrantDriver = gatewayCurrentNativeEmergencyAfterStopDriver{
		fakeGatewayV2LANGrantDriver: driver, after: func() {
			if err := os.WriteFile(filepath.Join(manager.store.root, "unexplained.bundle"), []byte("unknown"), 0600); err != nil {
				t.Fatal(err)
			}
		}}
	if err := manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err == nil || !driver.gatewayStopped ||
		!manager.gatewayRebindAdmissionBlocked() {
		t.Fatal("late unknown history accepted as complete native withdrawal")
	}
}

func TestGatewayCurrentNativeEmergencyCrossesExistingFailStopOnlyToWithdraw(t *testing.T) {
	manager, _, _, _, _, driver := gatewayV2LANGrantFixture(t)
	manager.gatewayRebindFailStop = &atomic.Bool{}
	manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	manager.gatewayRebindFailStop.Store(true)
	manager.gatewayRebindCommitBarrier.Store(true)
	if err := manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err != nil || !driver.gatewayStopped ||
		!manager.gatewayRebindFailStop.Load() || !manager.gatewayRebindCommitBarrier.Load() {
		t.Fatalf("existing latch blocked withdrawal or foreign barrier changed: stopped=%t err=%v", driver.gatewayStopped, err)
	}
	if release, err := manager.lockGateway(context.Background()); err == nil || release != nil {
		t.Fatal("emergency withdrawal allowed ordinary admission to resume")
	}
}

func TestGatewayCurrentNativeEmergencyWaitsForActualEffectsLease(t *testing.T) {
	manager, _, _, _, _, driver := gatewayV2LANGrantFixture(t)
	manager.gatewayRebindFailStop = &atomic.Bool{}
	realAcquire := gatewayRebindAcquireDeploymentEffects
	release, err := realAcquire(context.Background(), manager.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	attempted := make(chan struct{}, 1)
	gatewayRebindAcquireDeploymentEffects = func(ctx context.Context, directory string) (func() error, error) {
		attempted <- struct{}{}
		return realAcquire(ctx, directory)
	}
	t.Cleanup(func() { gatewayRebindAcquireDeploymentEffects = realAcquire })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- manager.stopOwnedGatewayV2OnStartupFailure(ctx) }()
	select {
	case <-attempted:
		cancel()
		if err := <-done; err == nil || len(driver.events) != 0 {
			t.Fatal("contended effects lease allowed native withdrawal")
		}
	case err := <-done:
		t.Fatalf("native emergency bypassed the held deployment effects lease: err=%v", err)
	}
}

func TestGatewayCurrentNativeEmergencyReleaseFailureRetainsAdmissionLatch(t *testing.T) {
	manager, _, _, _, _, driver := gatewayV2LANGrantFixture(t)
	manager.gatewayRebindFailStop = &atomic.Bool{}
	manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	manager.gatewayRebindCommitBarrier.Store(true)
	acquire := gatewayRebindAcquireDeploymentEffects
	released := false
	gatewayRebindAcquireDeploymentEffects = func(context.Context, string) (func() error, error) {
		return func() error { released = true; return errors.New("release acknowledgment unavailable") }, nil
	}
	t.Cleanup(func() { gatewayRebindAcquireDeploymentEffects = acquire })
	if err := manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err == nil || !driver.gatewayStopped ||
		!released || !manager.gatewayRebindFailStop.Load() || !manager.gatewayRebindCommitBarrier.Load() {
		t.Fatalf("native emergency release failure escaped: stopped=%t released=%t err=%v", driver.gatewayStopped, released, err)
	}
}
