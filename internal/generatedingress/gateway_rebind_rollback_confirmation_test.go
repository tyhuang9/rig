package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindRefuseRollbackConfirmationDriver struct {
	*gatewayRebindBoundedPhysicalDriver
	calls, failAt int
}

func (d *gatewayRebindRefuseRollbackConfirmationDriver) confirmRollbackServingLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	d.calls++
	if err := d.gatewayRebindBoundedPhysicalDriver.confirmRollbackServingLocked(ctx, request); err != nil {
		return err
	}
	if (d.calls == 1) != (request.Terminal == nil) ||
		(d.calls == 2 && request.Mode != gatewayRebindPhysicalReconcileRollbackOnly) {
		d.t.Fatal("rollback confirmation did not use the actual retained terminal state")
	}
	if d.calls == d.failAt {
		return errors.New("injected loss of rollback serving authority")
	}
	return nil
}

func TestGatewayRebindRollbackConfirmationFencesCommitAndRecovery(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		for _, failAt := range []int{1, 2} {
			name := "commit before receipt"
			if failAt == 2 {
				name = "commit before SQL release"
			}
			if recovering {
				name = "recovery " + name
			}
			t.Run(name, func(t *testing.T) {
				f, input, bounded := newGatewayRebindCoordinatorFixture(t)
				bounded.rollback = true
				driver := &gatewayRebindRefuseRollbackConfirmationDriver{gatewayRebindBoundedPhysicalDriver: bounded, failAt: failAt}
				if recovering {
					driver.failAt = 1
				}
				result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
				if err == nil || result != (GatewayRebindCommitResult{}) {
					t.Fatalf("commit confirmation refusal returned result=%#v error=%v", result, err)
				}
				if recovering {
					fresh := freshGatewayRebindRecoveryManager(f.manager)
					fresh.gatewayRebindFailStop, fresh.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
					driver.calls, driver.failAt = 0, failAt
					recovered, recoverErr := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), f.repository, driver)
					if recoverErr == nil || !reflect.DeepEqual(recovered, GatewayRebindStartupRecoveryResult{}) {
						t.Fatalf("recovery confirmation refusal returned result=%#v error=%v", recovered, recoverErr)
					}
				}
				snapshot, err := f.repository.GatewayRebindRecoverySnapshot(context.Background())
				if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
					f.repository.CheckGatewayRebindFence(context.Background()) == nil || driver.calls != failAt {
					t.Fatalf("confirmation refusal released or bypassed SQL fence: calls=%d snapshot=%#v err=%v", driver.calls, snapshot, err)
				}
				history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				wantTerminals := 0
				if failAt == 2 {
					wantTerminals = 1
				}
				if err != nil || len(history.TerminalsV2) != wantTerminals || len(history.Progress) == 0 ||
					history.Progress[len(history.Progress)-1].Record.Phase != gatewayRebindProgressHandoverRolledBack {
					t.Fatalf("confirmation refusal did not preserve immutable completion/receipt: terminals=%d progress=%d err=%v",
						len(history.TerminalsV2), len(history.Progress), err)
				}
			})
		}
	}
}
