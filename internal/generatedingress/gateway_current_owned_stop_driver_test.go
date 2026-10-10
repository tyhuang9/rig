package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentOwnedStopDriverUsesProtectedAuthorityAndRejectsDrift(t *testing.T) {
	f, _, _, _, batch := gatewayCurrentLANBatchRetirementFixture(t)
	ctx := context.Background()
	release, err := f.manager.lockGatewayRawForInspection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}()
	target, err := f.manager.gatewayCurrentOwnedStopTargetForStateLocked(batch)
	if err != nil {
		t.Fatal(err)
	}
	// A failure latch and unreadable SQL prohibit ordinary admission, but must
	// not prevent withdrawing this one exact retained owner.
	f.manager.gatewayRebindFailStopLatch().Store(true)
	sqlReads := 0
	f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		sqlReads++
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
	})
	t.Run("unsupported driver fails without ordinary effects", func(t *testing.T) {
		driver := &gatewayCurrentPhysicalDriverFake{}
		f.manager.gatewayCurrentPhysicalDriver = driver
		if err := f.manager.stopGatewayCurrentOwnedTargetLocked(ctx, target); err == nil || driver.stopCalls != 0 {
			t.Fatal("unsupported state-only stop fell through to an ordinary transition")
		}
	})
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	t.Run("exact withdrawal survives SQL failure and latch", func(t *testing.T) {
		if err := f.manager.stopGatewayCurrentOwnedTargetLocked(ctx, target); err != nil || len(driver.ownedStops) != 1 {
			t.Fatalf("protected-only stop refused exact owner: %v", err)
		}
	})
	t.Run("changed ownership after effect stays unresolved", func(t *testing.T) {
		next := cloneGatewayCurrentRouteState(batch)
		next.Revision++
		next.Digest, _ = gatewayCurrentRouteStateDigest(next)
		driver.stopBefore = func(gatewayCurrentOwnedStopTarget) {
			if err := f.store.saveNext(batch, next); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.manager.stopGatewayCurrentOwnedTargetLocked(ctx, target); err == nil || len(driver.ownedStops) != 2 {
			t.Fatal("changed protected evidence was accepted after stop")
		}
		if loaded, err := f.store.load(); err != nil || !reflect.DeepEqual(loaded, next) {
			t.Fatal("stop wrapper overwrote changed protected evidence")
		}
		driver.stopBefore = nil
	})
	t.Run("stale captured target fails before another effect", func(t *testing.T) {
		if err := f.manager.stopGatewayCurrentOwnedTargetLocked(ctx, target); err == nil || len(driver.ownedStops) != 2 {
			t.Fatal("stale ownership reached the physical driver")
		}
	})
	if sqlReads != 0 || !f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatal("withdrawal depended on SQL or released ordinary admission")
	}
}
