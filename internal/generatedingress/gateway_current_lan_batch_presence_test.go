package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANRecoveryBatchPresenceBeforePhysicalWork(t *testing.T) {
	f, _, _, physical := gatewayRebindCurrentStartupFixture(t)
	if present, err := f.manager.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || present {
		t.Fatalf("stable presence=%t error=%v", present, err)
	}
	appID, app := routeOperationTransferredApp(t, f.baseline)
	disable := disableRequestForGrant(t, gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw))
	batch := cloneGatewayCurrentRouteState(f.baseline)
	batch.Revision++
	batch.LANRecovery = &gatewayCurrentLANRecoveryBatch{
		Items: []gatewayCurrentLANRecoveryItem{{Kind: gatewayV2PendingLANDisable, AppID: appID, Disable: &disable}},
	}
	batch.Digest, _ = gatewayCurrentRouteStateDigest(batch)
	if err := f.store.saveNext(f.baseline, batch); err != nil {
		t.Fatal(err)
	}
	if present, err := f.manager.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || !present {
		t.Fatalf("batch presence=%t error=%v", present, err)
	}
	f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("test SQL unavailable")
	})
	if present, err := f.manager.HasGatewayV2LANRecoveryBatch(context.Background()); err == nil || present {
		t.Fatalf("unavailable authority accepted presence=%t error=%v", present, err)
	}
	retained, err := f.store.load()
	if err != nil || !reflect.DeepEqual(retained, batch) || physical.calls != 0 ||
		physical.applyCalls != 0 || physical.restoreCalls != 0 || physical.stopCalls != 0 {
		t.Fatal("presence inspection changed evidence or performed physical work")
	}
}
