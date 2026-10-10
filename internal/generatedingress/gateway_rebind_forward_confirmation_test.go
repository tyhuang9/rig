package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindRefuseForwardConfirmationDriver struct {
	*gatewayRebindBoundedPhysicalDriver
	repository *appaccess.Repository
	failAt     appaccess.GatewayRebindState
	phases     []appaccess.GatewayRebindState
}

func (d *gatewayRebindRefuseForwardConfirmationDriver) confirmForwardServingLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	if err := d.gatewayRebindBoundedPhysicalDriver.confirmForwardServingLocked(ctx, request); err != nil {
		return err
	}
	snapshot, err := d.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || request.Mode != gatewayRebindPhysicalReconcileForwardOnly || request.Terminal == nil ||
		request.Terminal.Disposition != appaccess.GatewayRebindDispositionCommit || !gatewayRebindTypedSnapshotMatchesRequest(snapshot, request) {
		d.t.Fatalf("forward confirmation used stale SQL/receipt authority: phase=%s err=%v", request.SQLPhase, err)
	}
	d.phases = append(d.phases, request.SQLPhase)
	if request.SQLPhase == d.failAt {
		return errors.New("injected lost forward serving authority")
	}
	return nil
}

func TestGatewayRebindForwardConfirmationFencesCommitAndRecovery(t *testing.T) {
	phases := []appaccess.GatewayRebindState{appaccess.GatewayRebindPrepared, appaccess.GatewayRebindSuccessorReady, appaccess.GatewayRebindDatabaseCommitted}
	for _, recovering := range []bool{false, true} {
		for index, phase := range phases {
			name := "commit/" + string(phase)
			if recovering {
				name = "recovery/" + string(phase)
			}
			t.Run(name, func(t *testing.T) {
				f, input, bounded := newGatewayRebindCoordinatorFixture(t)
				driver := &gatewayRebindRefuseForwardConfirmationDriver{gatewayRebindBoundedPhysicalDriver: bounded, repository: f.repository, failAt: phase}
				if recovering {
					driver.failAt = appaccess.GatewayRebindPrepared
				}
				result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
				if err == nil || result != (GatewayRebindCommitResult{}) {
					t.Fatalf("forward denial completed commit: result=%#v err=%v", result, err)
				}
				if recovering {
					fresh := freshGatewayRebindRecoveryManager(f.manager)
					fresh.gatewayRebindFailStop, fresh.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
					driver.phases, driver.failAt = nil, phase
					driver.withdrawCalls = 0
					recovered, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), f.repository, driver)
					if err == nil || !reflect.DeepEqual(recovered, GatewayRebindStartupRecoveryResult{}) {
						t.Fatalf("forward denial completed recovery: result=%#v err=%v", recovered, err)
					}
				}
				snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
				if err != nil || snapshot.Active == nil || snapshot.Phase != phase || !reflect.DeepEqual(driver.phases, phases[:index+1]) ||
					f.repository.CheckGatewayRebindFence(context.Background()) == nil || driver.withdrawCalls != 1 {
					t.Fatalf("forward denial crossed SQL boundary: phase=%s calls=%v err=%v", snapshot.Phase, driver.phases, err)
				}
				history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if err != nil || len(history.Progress) != 17 || len(history.TerminalsV2) != 1 ||
					history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionCommit ||
					history.Progress[16].Record.Phase != gatewayRebindProgressHandoverCommitted {
					t.Fatalf("forward denial changed immutable decision: progress=%d terminals=%d err=%v", len(history.Progress), len(history.TerminalsV2), err)
				}
			})
		}
	}
}
