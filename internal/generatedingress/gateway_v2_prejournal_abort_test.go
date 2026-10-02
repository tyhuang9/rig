package generatedingress

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestClassifyGatewayV2PreparationAbortRequiresExactV1AndCompleteV2Absence(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	preparation := gatewayUpgradePreparation{
		OperationID: state.OperationID, Profile: state.Profile, LocalHostPort: journal.Source.LocalHostPort,
		ApprovedActionDigest: state.UpgradeAction.Digest, ApprovedBy: state.UpgradeAction.ApprovedBy,
	}
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	digest, ok := classifyGatewayV2PreparationAbort(source, preparation, observation)
	if !ok || !validSHA256(digest) {
		t.Fatalf("exact v1/no-v2 proof rejected: digest=%q ok=%t", digest, ok)
	}

	drift := observation
	drift.OwnedNetworks = []string{state.Identity.IngressNetwork}
	if _, ok := classifyGatewayV2PreparationAbort(source, preparation, drift); ok {
		t.Fatal("owned v2 network was accepted")
	}
	drift = observation
	drift.FinalContainerFound = true
	if _, ok := classifyGatewayV2PreparationAbort(source, preparation, drift); ok {
		t.Fatal("deterministic final container was accepted")
	}
	drift = observation
	drift.V1Container.Running = false
	drift.V1Config = nil
	if _, ok := classifyGatewayV2PreparationAbort(source, preparation, drift); ok {
		t.Fatal("stopped v1 gateway was accepted")
	}
	drift = observation
	drift.OwnedInventoriesStable = false
	if _, ok := classifyGatewayV2PreparationAbort(source, preparation, drift); ok {
		t.Fatal("incomplete owned-v2 inventory was accepted")
	}
}

func TestFinalizeGatewayV2PreparationAbortEmptyDoesNotSelectNetwork(t *testing.T) {
	manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	sourceDriver.selectErr = errors.New("injected network inventory failure")

	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if sourceDriver.selectCalls != 0 || sourceDriver.calls != 0 || sourceDriver.abortCalls != 1 {
		t.Fatalf("abort proof called migration planner: %+v", sourceDriver)
	}
	receipt, err := store.loadPreJournalAbortReceipt()
	if err != nil || receipt.OperationID != request.OperationID || receipt.InitialStateDigest != "" || receipt.NetworkPlanDigest != "" {
		t.Fatalf("empty abort receipt=%+v err=%v", receipt, err)
	}
	if _, err := store.loadV2State(); err == nil {
		t.Fatal("empty abort installed v2 state")
	}
	if _, err := store.loadMigrationJournal(); err == nil {
		t.Fatal("empty abort installed migration journal")
	}
}

func TestFinalizeGatewayV2PreparationAbortBindsExactStateOnlyArtifact(t *testing.T) {
	manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	state := installGatewayV2PreparationAbortStateOnly(t, manager, store, request, sourceDriver)

	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.loadPreJournalAbortReceipt()
	if err != nil || receipt.InitialStateDigest == "" || receipt.NetworkPlanDigest == "" {
		t.Fatalf("state-only receipt=%+v err=%v", receipt, err)
	}
	wantStateDigest, _ := canonicalDigest(state)
	wantPlanDigest, _ := gatewayV2PlanDigest(state)
	if receipt.InitialStateDigest != wantStateDigest || receipt.NetworkPlanDigest != wantPlanDigest ||
		sourceDriver.selectCalls != 0 || sourceDriver.abortCalls != 1 {
		t.Fatalf("state-only abort receipt=%+v driver=%+v", receipt, sourceDriver)
	}
	if _, err := store.loadMigrationJournal(); err == nil {
		t.Fatal("state-only abort installed migration journal")
	}
}

func TestFinalizeGatewayV2PreparationAbortRejectsWrongOperationAndDockerDrift(t *testing.T) {
	t.Run("wrong operation", func(t *testing.T) {
		manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
		installGatewayV2PreparationAbortStateOnly(t, manager, store, request, sourceDriver)
		wrong := request
		wrong.OperationID = "77777777-7777-4777-8777-777777777777"
		if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), wrong); err == nil {
			t.Fatal("wrong operation finalized state-only abort")
		}
		if _, err := os.Lstat(store.abortPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wrong operation wrote abort receipt: %v", err)
		}
	})

	t.Run("Docker drift", func(t *testing.T) {
		manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
		sourceDriver.abortErr = errors.New("injected incomplete owned-v2 inventory")
		if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err == nil {
			t.Fatal("incomplete Docker observation finalized abort")
		}
		if sourceDriver.abortCalls != 1 {
			t.Fatalf("abort proof calls=%d, want 1", sourceDriver.abortCalls)
		}
		if _, err := os.Lstat(store.abortPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Docker drift wrote abort receipt: %v", err)
		}
	})
}

func TestFinalizeGatewayV2PreparationAbortReceiptWriteAmbiguityRequiresReplay(t *testing.T) {
	manager, store, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	originalWriteNew := upgradeProtectedWriteNew
	failed := false
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		err := originalWriteNew(path, purpose, body)
		if err == nil && !failed && purpose == store.abortPurpose {
			failed = true
			return errors.New("injected post-install abort receipt failure")
		}
		return err
	}
	restored := false
	restore := func() {
		if !restored {
			upgradeProtectedWriteNew = originalWriteNew
			restored = true
		}
	}
	t.Cleanup(restore)

	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err == nil {
		t.Fatal("ambiguous receipt write reported success")
	}
	if _, err := store.loadPreJournalAbortReceipt(); err != nil {
		t.Fatalf("post-install receipt missing: %v", err)
	}
	restore()
	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatalf("fresh receipt replay: %v", err)
	}
	if sourceDriver.abortCalls != 2 || sourceDriver.selectCalls != 0 {
		t.Fatalf("receipt replay proof=%+v", sourceDriver)
	}
}

func TestFinalizeGatewayV2PreparationAbortReceiptReplayRequiresFreshProof(t *testing.T) {
	manager, _, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	sourceDriver.abortErr = errors.New("current v1 stopped or v2 inventory drifted")
	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err == nil {
		t.Fatal("receipt replay accepted a failed current proof")
	}
	if sourceDriver.abortCalls != 2 || sourceDriver.selectCalls != 0 {
		t.Fatalf("receipt replay proof=%+v", sourceDriver)
	}
}

func TestFinalizeGatewayV2PreparationAbortReplayReattestsCurrentV1AfterRestart(t *testing.T) {
	manager, store, request, sourceDriver, _, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	current, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	current.Active = map[string]routeRecord{}
	if err := manager.store.save(current); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(manager.runner, manager.options)
	if err != nil {
		t.Fatal(err)
	}
	restartDriver := &fakeGatewayV2CoordinatorDriver{
		t: t, manager: restarted, osLockHeld: osLockHeld, digest: sourceDriver.digest,
		plan: sourceDriver.plan, rollbackTopology: gatewayTopologyExactV1Only,
	}
	restarted.gatewayV2CoordinatorDriver = restartDriver
	if err := restarted.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatalf("restart receipt replay after v1 route change: %v", err)
	}
	if restartDriver.abortCalls != 1 || restartDriver.selectCalls != 0 || restartDriver.calls != 0 {
		t.Fatalf("restart replay did not use the dedicated read-only proof: %+v", restartDriver)
	}
	if _, err := store.loadPreJournalAbortReceipt(); err != nil {
		t.Fatalf("receipt changed or disappeared during replay: %v", err)
	}
}

func TestFinalizeGatewayV2PreparationAbortDoesNotSucceedWhenGatewayLockReleaseFails(t *testing.T) {
	manager, store, request, _, _, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	successfulAcquire := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if *osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		*osLockHeld = true
		return func() error {
			*osLockHeld = false
			return errors.New("injected gateway lock release failure")
		}, nil
	}

	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); !IsCode(err, DiagnosticRouteUnresolved) || *osLockHeld {
		t.Fatalf("lock release failure=%v lockHeld=%t", err, *osLockHeld)
	}
	if _, err := store.loadPreJournalAbortReceipt(); err != nil {
		t.Fatalf("durable receipt missing after release failure: %v", err)
	}
	managerAcquireGatewayOSLock = successfulAcquire
	if err := manager.FinalizeGatewayV2PreparationAbort(context.Background(), request); err != nil {
		t.Fatalf("fresh retry after release failure: %v", err)
	}
}

func TestUpgradeGatewayV2DoesNotReportPreparationAbortWhenGatewayLockReleaseFails(t *testing.T) {
	manager, store, request, sourceDriver, _, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	sourceDriver.selectErr = errors.New("injected network selection failure")
	successfulAcquire := managerAcquireGatewayOSLock
	managerAcquireGatewayOSLock = func(context.Context, *stateStore) (func() error, error) {
		if *osLockHeld {
			t.Fatal("gateway OS lock acquired twice")
		}
		*osLockHeld = true
		return func() error {
			*osLockHeld = false
			return errors.New("injected gateway lock release failure")
		}, nil
	}

	result, err := manager.UpgradeGatewayV2(context.Background(), request, allowGatewayV2Upgrade)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || *osLockHeld {
		t.Fatalf("coordinator release failure result=%+v err=%v lockHeld=%t", result, err, *osLockHeld)
	}
	if _, err := store.loadPreJournalAbortReceipt(); err != nil {
		t.Fatalf("durable abort receipt missing after release failure: %v", err)
	}
	managerAcquireGatewayOSLock = successfulAcquire
	result, err = manager.UpgradeGatewayV2(context.Background(), request, allowGatewayV2Upgrade)
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		t.Fatalf("fresh coordinator replay result=%+v err=%v", result, err)
	}
}

func installGatewayV2PreparationAbortStateOnly(t *testing.T, manager *Manager, store *gatewayUpgradeStateStore,
	request GatewayV2UpgradeRequest, sourceDriver *fakeGatewayV2CoordinatorDriver,
) gatewayV2RouteState {
	t.Helper()
	preparation, err := gatewayV2UpgradePreparation(request)
	if err != nil {
		t.Fatal(err)
	}
	preparation.LocalHostPort = manager.options.HostPort
	preparation.Network = sourceDriver.plan
	preparation.SourceIdentityDigest = sourceDriver.digest
	source, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := prepareGatewayV2State(source, preparation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	return state
}
