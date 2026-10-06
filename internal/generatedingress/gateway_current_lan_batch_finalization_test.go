package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentLANRecoveryFinalizesTwoDisablesInOrder(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchObservationFixture(t)
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	for head, item := range batch.LANRecovery.Items {
		request := *item.Disable
		before, err := f.store.load()
		if err != nil {
			t.Fatal(err)
		}
		cleared, err := gatewayCurrentLANRecoveryClearedHeadState(before)
		if err != nil {
			t.Fatal(err)
		}
		callbacks := 0
		err = f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
			func(_ context.Context, observation GatewayV2LANDisableObservation) error {
				callbacks++
				if driver.calls == 0 || !reflect.DeepEqual(observation.Request, request) ||
					observation.Disposition != GatewayV2LANDisableWithdrawnPending ||
					observation.GatewayOperationID != batch.Lineage.OperationID ||
					observation.ProtectedStateDigest != before.Digest || observation.ObservedAt.IsZero() {
					t.Fatal("resolve lacks the exact current head and whole-batch withdrawal proof")
				}
				for i := range disables {
					if disables[i].Request.OperationID == request.OperationID {
						disables[i].State, disables[i].StateSequence = appaccess.AppAccessDisableCommitted, 3
						disables[i].RetainedBinding, disables[i].CurrentBinding = disables[i].CurrentBinding, nil
						grants[i].RetainedBinding, grants[i].CurrentBinding = grants[i].CurrentBinding, nil
					}
				}
				return nil
			}, func(_ context.Context, observation GatewayV2LANDisableObservation) error {
				callbacks++
				installed, err := f.store.load()
				if err != nil || !reflect.DeepEqual(installed, cleared) || callbacks != 2 ||
					observation.Disposition != GatewayV2LANDisableDisabled ||
					observation.ProtectedStateDigest != cleared.Digest ||
					driver.last.Selected.Digest != cleared.Digest {
					t.Fatal("acknowledgment preceded protected clear and fresh proof")
				}
				for i := range disables {
					if disables[i].Request.OperationID == request.OperationID {
						disables[i].ClearAcknowledged = true
					}
				}
				return nil
			})
		if err != nil || callbacks != 2 {
			t.Fatalf("current disable finalization: callbacks=%d error=%v", callbacks, err)
		}
		advanced, err := f.store.load()
		if err != nil || advanced.LANRecovery.Head != head+1 ||
			!reflect.DeepEqual(advanced.LANRecovery.Items, batch.LANRecovery.Items) ||
			advanced.Apps[item.AppID].LAN != nil || advanced.Revision != before.Revision+2 {
			t.Fatal("finalization did not clear and advance exactly one immutable head")
		}
		if head == 0 && advanced.Apps[batch.LANRecovery.Items[1].AppID].LAN == nil {
			t.Fatal("finalization cleared the later item")
		}
		observed, present, err := f.manager.ObserveGatewayV2LANRecoveryHead(context.Background(), grants, disables)
		if err != nil || !present || observed.Head != head+1 {
			t.Fatalf("finalized census: %+v %t %v", observed, present, err)
		}
	}
	if err := f.manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil || !sameGatewayHistorySnapshot(history, after) || len(driver.ownedStops) != 0 ||
		driver.applyCalls+driver.restoreCalls+driver.stopCalls != 0 {
		t.Fatal("ordered finalization altered retained history or invoked ordinary effects")
	}
}

func TestGatewayCurrentLANRecoveryFinalizationReplayAndHeadGuard(t *testing.T) {
	f, _, _, _, batch := gatewayCurrentLANBatchObservationFixture(t)
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	called := 0
	callback := func(context.Context, GatewayV2LANDisableObservation) error { called++; return nil }
	if err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), *batch.LANRecovery.Items[1].Disable,
		callback, callback); err == nil || called != 0 || driver.calls != 0 {
		t.Fatal("later head reached a callback or physical proof")
	}
	request := *batch.LANRecovery.Items[0].Disable
	callbackErr := errors.New("terminal write may have committed; callback lost acknowledgment")
	if err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request,
		func(context.Context, GatewayV2LANDisableObservation) error { return callbackErr }, callback); !errors.Is(err, callbackErr) {
		t.Fatalf("terminal callback failure: %v", err)
	}
	if installed, err := f.store.load(); err != nil || !reflect.DeepEqual(installed, batch) || called != 0 {
		t.Fatal("terminal callback failure cleared or advanced the queue")
	}
	if err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request, callback,
		func(context.Context, GatewayV2LANDisableObservation) error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("clear acknowledgment failure: %v", err)
	}
	cleared, err := f.store.load()
	if err != nil || cleared.LANRecovery.Head != 0 || cleared.Apps[request.AppID].LAN != nil ||
		cleared.Revision != batch.Revision+1 {
		t.Fatal("acknowledgment failure lost the cleared current head")
	}
	if err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request, callback,
		func(_ context.Context, observation GatewayV2LANDisableObservation) error {
			installed, err := f.store.load()
			if err != nil || !reflect.DeepEqual(installed, cleared) || observation.ProtectedStateDigest != cleared.Digest {
				t.Fatal("cleared replay created a revision-only write or changed acknowledgment identity")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	advanced, err := f.store.load()
	if err != nil || advanced.Revision != cleared.Revision+1 || advanced.LANRecovery.Head != 1 ||
		!reflect.DeepEqual(advanced.LANRecovery.Items, batch.LANRecovery.Items) || len(driver.ownedStops) != 0 {
		t.Fatal("replay did not advance exactly once or unnecessarily stopped the owner")
	}
	called = 0
	if err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), request, callback, callback); err == nil || called != 0 {
		t.Fatal("old operation advanced a second head")
	}
}

func TestGatewayCurrentLANRecoveryFinalizationStopsOnLostAuthority(t *testing.T) {
	for _, mode := range []string{"partial_batch", "sql_after_terminal", "cancel_after_terminal", "protected_drift"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _, _, batch := gatewayCurrentLANBatchObservationFixture(t)
			f.manager.gatewayRebindFailStop = &atomic.Bool{}
			driver := newGatewayCurrentLANBatchRetirementDriver(t)
			f.manager.gatewayCurrentPhysicalDriver = driver
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := batch
			if mode == "partial_batch" {
				driver.partial = true
			}
			if mode == "protected_drift" {
				driver.before = func(gatewayCurrentLANRecoveryPhysicalAction) {
					driver.before = nil
					var err error
					want, err = gatewayCurrentLANRecoveryClearedHeadState(batch)
					if err != nil || f.store.saveNext(batch, want) != nil {
						t.Fatal("could not inject protected state drift")
					}
					// A clearance before the SQL callback is an unapproved transition,
					// even though it is structurally the computed next state.
				}
			}
			resolved, acknowledged := 0, 0
			err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(ctx, *batch.LANRecovery.Items[0].Disable,
				func(context.Context, GatewayV2LANDisableObservation) error {
					resolved++
					switch mode {
					case "sql_after_terminal":
						f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
							return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
						})
					case "cancel_after_terminal":
						cancel()
					}
					return nil
				}, func(context.Context, GatewayV2LANDisableObservation) error { acknowledged++; return nil })
			if err == nil || !f.manager.gatewayRebindFailStop.Load() || len(driver.ownedStops) != 1 || acknowledged != 0 ||
				((mode == "partial_batch" || mode == "protected_drift") && resolved != 0) {
				t.Fatalf("unsafe finalization: err=%v resolve=%d ack=%d stop=%d", err, resolved, acknowledged, len(driver.ownedStops))
			}
			installed, loadErr := f.store.load()
			if loadErr != nil || !reflect.DeepEqual(installed, want) || driver.ownedStops[0].State.Digest != want.Digest {
				t.Fatal("failed finalization rewrote recovery evidence or stopped a different owner")
			}
		})
	}
}

func TestGatewayCurrentLANRecoveryFinalizesUnpublishedGrant(t *testing.T) {
	f, snapshot, _, _, _ := gatewayCurrentLANBatchObservationFixture(t)
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	// Start from a new revision with the native app route but no publication.
	// The immutable transferred app remains untouched and keeps serving.
	installed, err := f.store.load()
	if err != nil {
		t.Fatal(err)
	}
	const appID = "79797979-7979-4797-8797-797979797979"
	request := gatewayV2LANGrantRequestForBinding(appID, installed.Apps[appID].LAN.Raw)
	prepared, err := gatewayCurrentNextState(installed, func(next *gatewayCurrentRouteState) {
		next.LANRecovery = nil
		app := next.Apps[appID]
		app.LAN = nil
		next.Apps[appID] = app
	})
	if err != nil || f.store.saveNext(installed, prepared) != nil {
		t.Fatal("prepared grant fixture")
	}
	grants := gatewayCurrentStartupGrants(t, prepared)
	grants = append(grants, GatewayV2LANStartupClaim{Request: request, State: appaccess.AppAccessGrantPrepared, StateSequence: 1})
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := gatewayCurrentLANRecoveryInstallState(prepared, claims)
	if err != nil || !gatewayCurrentLANRecoveryCensusMatchesHead(batch, claims, snapshot.CurrentTransfers) ||
		f.store.saveNext(prepared, batch) != nil {
		t.Fatal("unpublished grant batch fixture")
	}
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	called := false
	if err := f.manager.WithGatewayV2LANRecoveryGrantFinalization(context.Background(), request,
		func(_ context.Context, observation GatewayV2LANGrantObservation) error {
			called = true
			if observation.Request != request || observation.ProtectedStateDigest != batch.Digest ||
				observation.GatewayOperationID != batch.Lineage.OperationID ||
				observation.EffectiveBinding.ProtectedLineage != batch.Lineage ||
				observation.EffectiveBinding.TransferChainTipDigest != "" ||
				observation.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation || !observation.ActivationUncertain ||
				!reflect.DeepEqual(observation.Endpoints, batch.Apps[appID].Route.Endpoints) {
				t.Fatal("unpublished grant observation invented publication or lost immutable head identity")
			}
			return nil
		}); err != nil || !called {
		t.Fatalf("unpublished grant finalization: called=%t error=%v", called, err)
	}
	advanced, err := f.store.load()
	if err != nil || advanced.LANRecovery.Head != 1 || advanced.Revision != batch.Revision+1 ||
		!reflect.DeepEqual(advanced.Apps, batch.Apps) || !reflect.DeepEqual(advanced.LANRecovery.Items, batch.LANRecovery.Items) {
		t.Fatal("unpublished grant rollback wrote a clear revision or altered serving routes")
	}
}

func TestGatewayCurrentLANRecoveryFinalizationPreservesAmbiguousWrite(t *testing.T) {
	for _, writeNumber := range []int{1, 2} {
		t.Run(map[int]string{1: "clear", 2: "advance"}[writeNumber], func(t *testing.T) {
			f, _, _, _, batch := gatewayCurrentLANBatchObservationFixture(t)
			f.manager.gatewayRebindFailStop = &atomic.Bool{}
			driver := newGatewayCurrentLANBatchRetirementDriver(t)
			f.manager.gatewayCurrentPhysicalDriver = driver
			originalWrite := upgradeProtectedWrite
			writes := 0
			upgradeProtectedWrite = func(path, purpose string, body []byte) error {
				err := originalWrite(path, purpose, body)
				if err == nil && purpose == f.store.purpose {
					writes++
					if writes == writeNumber {
						return errors.New("write succeeded but acknowledgment was lost")
					}
				}
				return err
			}
			t.Cleanup(func() { upgradeProtectedWrite = originalWrite })
			resolved, acknowledged := 0, 0
			err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(context.Background(), *batch.LANRecovery.Items[0].Disable,
				func(context.Context, GatewayV2LANDisableObservation) error { resolved++; return nil },
				func(context.Context, GatewayV2LANDisableObservation) error { acknowledged++; return nil })
			if err == nil || writes != writeNumber || resolved != 1 || acknowledged != writeNumber-1 ||
				len(driver.ownedStops) != 1 || !f.manager.gatewayRebindFailStop.Load() {
				t.Fatalf("ambiguous write: err=%v writes=%d resolve=%d ack=%d stops=%d", err, writes, resolved, acknowledged, len(driver.ownedStops))
			}
			installed, err := f.store.load()
			if err != nil || installed.Revision != batch.Revision+uint64(writeNumber) ||
				installed.LANRecovery.Head != writeNumber-1 || installed.Apps[batch.LANRecovery.Items[0].AppID].LAN != nil ||
				!reflect.DeepEqual(installed.LANRecovery.Items, batch.LANRecovery.Items) ||
				!reflect.DeepEqual(driver.ownedStops[0].State, installed) {
				t.Fatal("lost write acknowledgment discarded the exact installed evidence")
			}
		})
	}
}
