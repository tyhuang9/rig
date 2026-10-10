package generatedingress

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentStartupOwnedStopDriver struct {
	gatewayCurrentPhysicalDriver
	targets []gatewayCurrentOwnedStopTarget
	err     error
	onStop  func()
}

func prepareGatewayCurrentStartupEmergencyManager(t *testing.T, manager *Manager) {
	t.Helper()
	if manager == nil {
		t.Fatal("missing emergency-stop manager")
	}
	leaseManager, _ := newManagerFixture(t, false)
	manager.mu = newContextMutex()
	manager.options.WorkingDirectory = leaseManager.options.WorkingDirectory
}

func (d *gatewayCurrentStartupOwnedStopDriver) stopGatewayCurrentOwnedTarget(_ context.Context,
	target gatewayCurrentOwnedStopTarget,
) error {
	if !validGatewayCurrentOwnedStopTarget(target) {
		return errors.New("invalid test owned-stop target")
	}
	d.targets = append(d.targets, target)
	if d.onStop != nil {
		d.onStop()
	}
	return d.err
}

func TestGatewayCurrentStartupEmergencyStopProvesAbsenceBeforeNativeFallback(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayCurrentStartupOwnedStopDriver{}
	fixture.manager.gatewayCurrentPhysicalDriver = driver

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err != nil || result != (GatewayCurrentStartupEmergencyStopResult{}) || len(driver.targets) != 0 ||
		fixture.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("native fallback proof result=%#v targets=%d blocked=%t error=%v",
			result, len(driver.targets), fixture.manager.gatewayRebindAdmissionBlocked(), err)
	}
}

func TestGatewayCurrentStartupEmergencyStopNeverClearsForeignCommitBarrier(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier.Store(true)
	fixture.manager.gatewayCurrentPhysicalDriver = &gatewayCurrentStartupOwnedStopDriver{}

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err != nil || result != (GatewayCurrentStartupEmergencyStopResult{}) ||
		!fixture.manager.gatewayRebindCommitBarrier.Load() || fixture.manager.gatewayRebindFailStop.Load() {
		t.Fatalf("foreign barrier result=%#v barrier=%t blocked=%t error=%v", result,
			fixture.manager.gatewayRebindCommitBarrier.Load(), fixture.manager.gatewayRebindFailStop.Load(), err)
	}
}

func TestGatewayCurrentStartupEmergencyStopWithdrawsExactProtectedTargetsAndLatches(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentStartupEmergencyManager(t, fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayCurrentStartupOwnedStopDriver{}
	fixture.manager.gatewayCurrentPhysicalDriver = driver

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err != nil || !result.ProtectedRebindHistoryPresent || result.UnresolvedProtectedRebindHistory ||
		!result.RebindOwnershipPresent || result.OwnershipIndeterminate ||
		result.VerifiedTargets != 1 || result.StoppedOrAbsentTargets != 1 || result.Incomplete ||
		len(driver.targets) != 1 || driver.targets[0].FinalContainer.ID != fixture.receipt.Resources.FinalContainer.ID ||
		!fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("owned emergency stop result=%#v targets=%#v blocked=%t error=%v",
			result, driver.targets, fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
	if release, lockErr := fixture.manager.lockGatewayRaw(context.Background()); lockErr == nil || release != nil {
		if release != nil {
			_ = release()
		}
		t.Fatalf("ordinary admission crossed emergency stop latch: release=%t error=%v", release != nil, lockErr)
	}
}

func TestGatewayCurrentStartupEmergencyStopRefusesNonterminalProtectedHistory(t *testing.T) {
	fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
	fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
		return errors.New("injected crash after SQL prepared claim")
	}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("post-claim interruption unexpectedly completed")
	}
	fixture.manager.gatewayRebindAfterClaim = nil
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil {
		t.Fatalf("prepared snapshot unavailable: %#v error=%v", snapshot, err)
	}
	if _, err := fixture.manager.recoverGatewayRebindPreparedAdmissionLocked(context.Background(),
		fixture.repository, snapshot); err != nil {
		t.Fatalf("install prepared protected admission: %v", err)
	}
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	stopDriver := &gatewayCurrentStartupOwnedStopDriver{}
	fixture.manager.gatewayCurrentPhysicalDriver = stopDriver

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err == nil || !IsCode(err, DiagnosticRouteUnresolved) || !result.ProtectedRebindHistoryPresent ||
		!result.UnresolvedProtectedRebindHistory || result.RebindOwnershipPresent ||
		result.OwnershipIndeterminate || result.VerifiedTargets != 0 || result.StoppedOrAbsentTargets != 0 ||
		!result.Incomplete || len(stopDriver.targets) != 0 || !fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("nonterminal protected history result=%#v targets=%d blocked=%t error=%v",
			result, len(stopDriver.targets), fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
}

func TestGatewayCurrentStartupEmergencyStopRefusesOrphanCurrentBundle(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentStartupEmergencyManager(t, fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	for _, retained := range fixture.history.Intents {
		if err := os.Remove(retained.Store.path); err != nil {
			t.Fatal(err)
		}
	}
	for _, retained := range fixture.history.Progress {
		if err := os.Remove(retained.Store.path); err != nil {
			t.Fatal(err)
		}
	}
	for _, retained := range fixture.history.Terminals {
		if err := os.Remove(retained.Store.path); err != nil {
			t.Fatal(err)
		}
	}
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayCurrentStartupOwnedStopDriver{}
	fixture.manager.gatewayCurrentPhysicalDriver = driver

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err == nil || !IsCode(err, DiagnosticRouteUnresolved) || !result.ProtectedRebindHistoryPresent ||
		!result.UnresolvedProtectedRebindHistory || result.RebindOwnershipPresent || result.OwnershipIndeterminate ||
		result.VerifiedTargets != 0 || result.StoppedOrAbsentTargets != 0 || !result.Incomplete ||
		len(driver.targets) != 0 || !fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("orphan current result=%#v targets=%d blocked=%t error=%v", result, len(driver.targets),
			fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
}

func TestGatewayCurrentStartupEmergencyCensusRejectsOrphanBesideCommittedBundle(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	committed, _ := gatewayCurrentRouteStateName(fixture.baseline.Lineage.ProtectedGeneration,
		fixture.baseline.Lineage.OperationID)
	orphan, _ := gatewayCurrentRouteStateName(fixture.baseline.Lineage.ProtectedGeneration+1,
		fixture.baseline.Lineage.OperationID)
	presence := gatewayRebindProtectedPresenceSnapshot{present: true,
		files: map[string]gatewayHistoryFileFingerprint{
			committed: {},
			orphan:    {},
		}}

	census, lineages := gatewayCurrentOwnedStopCensus(fixture.history, presence)
	if !census.ProtectedRebindHistory || !census.UnresolvedAttempt || !census.CommittedOwnership ||
		len(lineages) != 1 || lineages[0] != fixture.baseline.Lineage {
		t.Fatalf("orphan beside committed census=%#v lineages=%#v", census, lineages)
	}
}

func TestGatewayCurrentStartupEmergencyStopReportsMissingBundleWithoutStoppingByName(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentStartupEmergencyManager(t, fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.store.path); err != nil {
		t.Fatal(err)
	}
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayCurrentStartupOwnedStopDriver{}
	fixture.manager.gatewayCurrentPhysicalDriver = driver

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err == nil || !IsCode(err, DiagnosticRouteUnresolved) || !result.ProtectedRebindHistoryPresent ||
		result.UnresolvedProtectedRebindHistory || !result.RebindOwnershipPresent ||
		!result.OwnershipIndeterminate || result.VerifiedTargets != 0 || result.StoppedOrAbsentTargets != 0 ||
		!result.Incomplete || len(driver.targets) != 0 || !fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("missing bundle result=%#v targets=%d blocked=%t error=%v",
			result, len(driver.targets), fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
}

func TestGatewayCurrentStartupEmergencyStopRejectsWholeHistoryDriftAfterPhysicalStop(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentStartupEmergencyManager(t, fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayCurrentStartupOwnedStopDriver{onStop: func() {
		if err := os.Remove(fixture.history.Terminals[0].Store.path); err != nil {
			t.Fatalf("remove retained terminal during stop: %v", err)
		}
	}}
	fixture.manager.gatewayCurrentPhysicalDriver = driver

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err == nil || !IsCode(err, DiagnosticRouteUnresolved) || !result.ProtectedRebindHistoryPresent ||
		!result.RebindOwnershipPresent || !result.OwnershipIndeterminate || result.VerifiedTargets != 1 ||
		result.StoppedOrAbsentTargets != 0 || !result.Incomplete || len(driver.targets) != 1 ||
		!fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("post-stop history drift result=%#v targets=%d blocked=%t error=%v", result,
			len(driver.targets), fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
}

func TestGatewayCurrentStartupEmergencyStopReleaseFailureZeroesResultAndLatches(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	prepareGatewayCurrentStartupEmergencyManager(t, fixture.manager)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	fixture.manager.gatewayCurrentPhysicalDriver = &gatewayCurrentStartupOwnedStopDriver{}
	original := gatewayRebindAcquireDeploymentEffects
	gatewayRebindAcquireDeploymentEffects = func(context.Context, string) (func() error, error) {
		return func() error { return errors.New("injected emergency effects release failure") }, nil
	}
	t.Cleanup(func() { gatewayRebindAcquireDeploymentEffects = original })

	result, err := fixture.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
	if err == nil || result != (GatewayCurrentStartupEmergencyStopResult{}) ||
		!fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("emergency release failure result=%#v blocked=%t error=%v",
			result, fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
}
