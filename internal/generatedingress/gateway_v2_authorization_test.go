package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestUpgradeGatewayV2RejectsNilAuthorizerBeforeLockOrWrites(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, transferDriver, osLockHeld := gatewayV2CoordinatorTestFixture(t)

	result, err := manager.UpgradeGatewayV2(context.Background(), request, nil)
	if !IsCode(err, DiagnosticValidationFailed) || result.Outcome != GatewayV2UpgradeUnresolved {
		t.Fatalf("nil authorizer result=%+v err=%v", result, err)
	}
	if *osLockHeld || sourceDriver.selectCalls != 0 || sourceDriver.calls != 0 ||
		stageDriver.createCalls != 0 || transferDriver.createCalls != 0 {
		t.Fatalf("nil authorizer reached protected work: source=%+v stage=%+v transfer=%+v lock=%t",
			sourceDriver, stageDriver, transferDriver, *osLockHeld)
	}
	if _, err := store.loadV2State(); err == nil {
		t.Fatal("nil authorizer wrote protected state")
	}
	if _, err := store.loadMigrationJournal(); err == nil {
		t.Fatal("nil authorizer wrote migration journal")
	}
}

func TestUpgradeGatewayV2InitialAuthorizationDenialRunsLockedAndHasNoSideEffects(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, transferDriver, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	calls := 0
	authorize := func(_ context.Context, got GatewayV2UpgradeRequest) error {
		calls++
		if !reflect.DeepEqual(got, request) {
			t.Fatalf("authorization request=%+v, want %+v", got, request)
		}
		if manager.mu.TryLock() {
			manager.mu.Unlock()
			t.Fatal("authorization callback ran without Manager lock")
		}
		if !*osLockHeld {
			t.Fatal("authorization callback ran without OS lock")
		}
		return errors.New("claim was revoked")
	}

	result, err := manager.UpgradeGatewayV2(context.Background(), request, authorize)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || calls != 1 {
		t.Fatalf("initial denial result=%+v err=%v calls=%d", result, err, calls)
	}
	if *osLockHeld || sourceDriver.selectCalls != 0 || sourceDriver.calls != 0 || sourceDriver.abortCalls != 0 ||
		stageDriver.createCalls != 0 || transferDriver.createCalls != 0 {
		t.Fatalf("initial denial reached protected work: source=%+v stage=%+v transfer=%+v lock=%t",
			sourceDriver, stageDriver, transferDriver, *osLockHeld)
	}
	if _, err := store.loadV2State(); err == nil {
		t.Fatal("initial denial wrote protected state")
	}
	if _, err := store.loadMigrationJournal(); err == nil {
		t.Fatal("initial denial wrote migration journal")
	}
	if _, err := store.loadPreJournalAbortReceipt(); err == nil {
		t.Fatal("initial denial wrote an abort receipt")
	}
}

func TestUpgradeGatewayV2LateAuthorizationDenialLeavesRecoverableHistoryWithoutDockerMutation(t *testing.T) {
	manager, store, request, _, stageDriver, transferDriver, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	calls := 0
	authorize := func(_ context.Context, got GatewayV2UpgradeRequest) error {
		calls++
		if got != request {
			t.Fatalf("authorization request=%+v, want %+v", got, request)
		}
		if manager.mu.TryLock() {
			manager.mu.Unlock()
			t.Fatal("authorization callback ran without Manager lock")
		}
		if !*osLockHeld {
			t.Fatal("authorization callback ran without OS lock")
		}
		if calls == 2 {
			return errors.New("claim changed after preflight")
		}
		return nil
	}

	result, err := manager.UpgradeGatewayV2(context.Background(), request, authorize)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || calls != 2 {
		t.Fatalf("late denial result=%+v err=%v calls=%d", result, err, calls)
	}
	if *osLockHeld || stageDriver.createCalls != 0 || stageDriver.networkExists || stageDriver.configExists ||
		stageDriver.dataExists || stageDriver.stageExists || transferDriver.createCalls != 0 {
		t.Fatalf("late denial mutated Docker: stage=%+v transfer=%+v lock=%t", stageDriver, transferDriver, *osLockHeld)
	}
	state, journal, loadErr := store.loadBoundUpgrade(request.OperationID)
	if loadErr != nil || journal.Phase != gatewayPhaseStageIntent || journal.Resources.ImageID == "" ||
		journal.Resources.IngressNetworkID != "" || journal.Resources.ConfigVolume != (gatewayV2VolumeResourceBinding{}) ||
		journal.Resources.DataVolume != (gatewayV2VolumeResourceBinding{}) || journal.Resources.StageContainerID != "" {
		t.Fatalf("late-denial history state=%+v journal=%+v err=%v", state, journal, loadErr)
	}
	if _, receiptErr := store.loadRollbackRetirementReceipt(state, journal); receiptErr == nil {
		t.Fatal("late denial installed rollback retirement receipt")
	}
	if _, abortErr := store.loadPreJournalAbortReceipt(); abortErr == nil {
		t.Fatal("late denial installed pre-journal abort receipt")
	}

	result, err = manager.UpgradeGatewayV2(context.Background(), request, allowGatewayV2Upgrade)
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("authorized recovery result=%+v err=%v", result, err)
	}
}

func TestUpgradeGatewayV2AuthorizesEveryReplayAndFirstMutationOnly(t *testing.T) {
	manager, _, request, _, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	calls := 0
	authorize := func(context.Context, GatewayV2UpgradeRequest) error {
		calls++
		return nil
	}
	result, err := manager.UpgradeGatewayV2(context.Background(), request, authorize)
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted || calls != 2 {
		t.Fatalf("fresh authorization result=%+v err=%v calls=%d", result, err, calls)
	}
	stageCreates, finalCreates := stageDriver.createCalls, transferDriver.createCalls
	calls = 0
	result, err = manager.UpgradeGatewayV2(context.Background(), request, authorize)
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted || calls != 1 {
		t.Fatalf("read-only replay result=%+v err=%v calls=%d", result, err, calls)
	}
	if stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
		t.Fatalf("read-only replay mutated Docker: stageCreates=%d/%d finalCreates=%d/%d",
			stageDriver.createCalls, stageCreates, transferDriver.createCalls, finalCreates)
	}

	calls = 0
	result, err = manager.UpgradeGatewayV2(context.Background(), request, func(context.Context, GatewayV2UpgradeRequest) error {
		calls++
		return errors.New("replay denied")
	})
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || calls != 1 {
		t.Fatalf("denied replay result=%+v err=%v calls=%d", result, err, calls)
	}
	if stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
		t.Fatal("denied replay mutated Docker")
	}
}

func TestUpgradeGatewayV2ReauthorizesRollbackRetirementBeforeCleanup(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	state, journal := prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)
	sourceDriver.retirementObservation = gatewayV2RollbackRetirementObservation{IngressNetworkPresent: true}
	calls := 0
	authorize := func(context.Context, GatewayV2UpgradeRequest) error {
		calls++
		if calls == 2 {
			return errors.New("cleanup authorization revoked")
		}
		return nil
	}

	result, err := manager.UpgradeGatewayV2(context.Background(), request, authorize)
	if !IsCode(err, DiagnosticRouteUnresolved) || result.Outcome != GatewayV2UpgradeUnresolved || calls != 2 {
		t.Fatalf("cleanup denial result=%+v err=%v calls=%d", result, err, calls)
	}
	if sourceDriver.retirementRemoveNetworkCalls != 0 || !sourceDriver.retirementObservation.IngressNetworkPresent {
		t.Fatalf("cleanup denial mutated retained resource: %+v", sourceDriver)
	}
	if _, receiptErr := store.loadRollbackRetirementReceipt(state, journal); receiptErr == nil {
		t.Fatal("cleanup denial installed retirement receipt")
	}
}
