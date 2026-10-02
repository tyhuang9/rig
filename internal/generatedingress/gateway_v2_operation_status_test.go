package generatedingress

import (
	"context"
	"errors"
	"testing"
)

func TestObserveGatewayV2OperationReportsServingOnlyAfterFreshCommittedProof(t *testing.T) {
	manager, _, request, _, stageDriver, transferDriver, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	if result, err := manager.UpgradeGatewayV2(context.Background(), request, allowGatewayV2Upgrade); err != nil ||
		result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("initial upgrade result=%+v err=%v", result, err)
	}
	stageCreates, finalCreates := stageDriver.createCalls, transferDriver.createCalls

	status, err := manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status != (GatewayV2OperationStatus{
		OperationID: request.OperationID, Availability: GatewayV2OperationServing,
	}) {
		t.Fatalf("serving status=%+v err=%v", status, err)
	}
	if *osLockHeld || stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
		t.Fatalf("status observation mutated resources: stage=%+v transfer=%+v lock=%t", stageDriver, transferDriver, *osLockHeld)
	}

	transferDriver.finalRunning = false
	status, err = manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status.Availability != GatewayV2OperationUnknown {
		t.Fatalf("drifted committed status=%+v err=%v", status, err)
	}
	if stageDriver.createCalls != stageCreates || transferDriver.createCalls != finalCreates {
		t.Fatal("drifted status observation mutated resources")
	}
}

func TestObserveGatewayV2OperationReportsUnavailableOnlyAfterFreshAbortProof(t *testing.T) {
	manager, _, request, sourceDriver, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	sourceDriver.selectErr = errors.New("network inventory unavailable")
	result, upgradeErr := manager.UpgradeGatewayV2(context.Background(), request, allowGatewayV2Upgrade)
	if upgradeErr == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		t.Fatalf("pre-journal abort result=%+v err=%v", result, upgradeErr)
	}
	proofs := sourceDriver.abortCalls

	status, err := manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status.Availability != GatewayV2OperationUnavailable || sourceDriver.abortCalls != proofs+1 {
		t.Fatalf("aborted status=%+v err=%v proofs=%d", status, err, sourceDriver.abortCalls)
	}
	sourceDriver.abortErr = errors.New("v1 stopped or v2 resource appeared")
	status, err = manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status.Availability != GatewayV2OperationUnknown {
		t.Fatalf("drifted aborted status=%+v err=%v", status, err)
	}
}

func TestObserveGatewayV2OperationReportsUnavailableOnlyAfterFreshRollbackProof(t *testing.T) {
	manager, store, request, sourceDriver, stageDriver, _, _ := gatewayV2CoordinatorTestFixture(t)
	prepareRolledBackForRetirementTest(t, manager, store, request, sourceDriver, stageDriver)

	status, err := manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status.Availability != GatewayV2OperationUnavailable || sourceDriver.retirementObserveCalls != 1 {
		t.Fatalf("rolled-back status=%+v err=%v driver=%+v", status, err, sourceDriver)
	}
	sourceDriver.retirementObserveError = errors.New("v1 or retained infrastructure drifted")
	status, err = manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status.Availability != GatewayV2OperationUnknown {
		t.Fatalf("drifted rolled-back status=%+v err=%v", status, err)
	}
}

func TestObserveGatewayV2OperationDoesNotInferUnavailableFromMissingHistory(t *testing.T) {
	manager, _, request, sourceDriver, stageDriver, transferDriver, _ := gatewayV2CoordinatorTestFixture(t)
	status, err := manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if err != nil || status.Availability != GatewayV2OperationUnknown {
		t.Fatalf("missing-history status=%+v err=%v", status, err)
	}
	if sourceDriver.calls != 0 || sourceDriver.abortCalls != 0 || sourceDriver.retirementObserveCalls != 0 ||
		stageDriver.createCalls != 0 || transferDriver.createCalls != 0 {
		t.Fatalf("missing-history observation reached runtime drivers: source=%+v stage=%+v transfer=%+v",
			sourceDriver, stageDriver, transferDriver)
	}
}

func TestObserveGatewayV2OperationReturnsUnknownWhenLockReleaseFails(t *testing.T) {
	manager, _, request, _, _, _, osLockHeld := gatewayV2CoordinatorTestFixture(t)
	if result, err := manager.UpgradeGatewayV2(context.Background(), request, allowGatewayV2Upgrade); err != nil ||
		result.Outcome != GatewayV2UpgradeCommitted {
		t.Fatalf("initial upgrade result=%+v err=%v", result, err)
	}
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

	status, err := manager.ObserveGatewayV2Operation(context.Background(), request.OperationID)
	if !IsCode(err, DiagnosticRouteUnresolved) || status.Availability != GatewayV2OperationUnknown || *osLockHeld {
		t.Fatalf("release-failure status=%+v err=%v lock=%t", status, err, *osLockHeld)
	}
}

func TestObserveGatewayV2OperationValidatesExactOperationID(t *testing.T) {
	manager, _, _, _, _, _, _ := gatewayV2CoordinatorTestFixture(t)
	status, err := manager.ObserveGatewayV2Operation(context.Background(), "not-an-operation")
	if !IsCode(err, DiagnosticValidationFailed) || status.Availability != GatewayV2OperationUnknown ||
		status.OperationID != "not-an-operation" {
		t.Fatalf("invalid operation status=%+v err=%v", status, err)
	}
}
