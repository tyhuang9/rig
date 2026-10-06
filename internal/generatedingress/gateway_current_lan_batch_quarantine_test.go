package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentLANBatchQuarantineDriver struct {
	*gatewayCurrentLANBatchRetirementDriver
	withdrawn      bool
	withdrawCalls  int
	withdrawErr    error
	beforeWithdraw func(gatewayCurrentLANRecoveryPhysicalAction)
}

func (d *gatewayCurrentLANBatchQuarantineDriver) attestGatewayCurrentPhysical(ctx context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	if selection.State == nil || selection.State.Pending == nil {
		return d.gatewayCurrentLANBatchRetirementDriver.attestGatewayCurrentPhysical(ctx, selection)
	}
	d.stableCalls++
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(*selection.State)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proof := gatewayCurrentPhysicalAttestationFixture(d.t, projection.Before, terminal, gatewayCurrentPhysicalRecoveryBefore)
	proof.Pending = cloneGatewayCurrentPendingRoute(selection.State.Pending)
	proof.Digest, _ = gatewayCurrentPhysicalAttestationDigest(proof)
	return proof, nil
}

func (d *gatewayCurrentLANBatchQuarantineDriver) attestGatewayCurrentLANRecoveryBatch(_ context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	d.calls++
	d.last = action
	return gatewayCurrentLANRecoveryPhysicalResultFixture(d.t, action, d.withdrawn || action.Complete), nil
}

func (d *gatewayCurrentLANBatchQuarantineDriver) withdrawGatewayCurrentLANRecoveryBatch(_ context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	d.withdrawCalls++
	if action.Complete {
		d.t.Fatal("completed queue reached withdrawal")
	}
	if d.beforeWithdraw != nil {
		d.beforeWithdraw(action)
	}
	d.withdrawn = true
	if d.withdrawErr != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, d.withdrawErr
	}
	return gatewayCurrentLANRecoveryPhysicalResultFixture(d.t, action, true), nil
}

func TestGatewayCurrentLANRecoveryBatchStopsUnqueueableClaimsBeforeIntent(t *testing.T) {
	for _, invalid := range []string{"committed stale grant", "missing effective authority"} {
		t.Run(invalid, func(t *testing.T) {
			f, _, grants, disables := gatewayCurrentLANBatchPendingClaimsFixture(t)
			f.manager.gatewayRebindFailStop, f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
			switch invalid {
			case "committed stale grant":
				for index := range grants {
					grants[index].DisableIntentOperationID = ""
				}
				grants[0].RequiresRecovery = true
				disables = nil
			case "missing effective authority":
				grants[0].CurrentBinding, disables[0].CurrentBinding = nil, nil
			}
			if _, err := validateGatewayV2LANAccessStartupClaims(grants, disables); err != nil {
				t.Fatalf("refusal fixture must be structurally valid: %v", err)
			}
			driver := &gatewayCurrentLANBatchQuarantineDriver{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t)}
			f.manager.gatewayCurrentPhysicalDriver = driver
			history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err == nil {
				t.Fatal("invalid claim census entered batch recovery")
			}
			installed, loadErr := f.store.load()
			after, historyErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			if loadErr != nil || historyErr != nil || !reflect.DeepEqual(installed, f.baseline) ||
				!sameGatewayHistorySnapshot(history, after) || !f.manager.gatewayRebindAdmissionBlocked() ||
				len(driver.ownedStops) != 1 || !reflect.DeepEqual(driver.ownedStops[0].State, f.baseline) ||
				driver.withdrawCalls != 0 || driver.calls != 0 || driver.stableCalls != 0 {
				t.Fatal("invalid census wrote intent, fabricated a terminal claim, or left its owner unstopped")
			}
		})
	}
}

func TestGatewayCurrentLANRecoveryBatchRetainsIntentAfterEffectUncertainty(t *testing.T) {
	for _, stage := range []string{"withdrawal acknowledgement", "SQL after withdrawal", "client cancellation"} {
		t.Run(stage, func(t *testing.T) {
			f, _, grants, disables := gatewayCurrentLANBatchPendingClaimsFixture(t)
			f.manager.gatewayRebindFailStop, f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
			claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
			if err != nil {
				t.Fatal(err)
			}
			want, err := gatewayCurrentLANRecoveryInstallState(f.baseline, claims)
			if err != nil {
				t.Fatal(err)
			}
			driver := &gatewayCurrentLANBatchQuarantineDriver{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t)}
			f.manager.gatewayCurrentPhysicalDriver = driver
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "withdrawal acknowledgement":
				driver.withdrawErr = errors.New("withdrawal acknowledgement lost")
			case "SQL after withdrawal":
				driver.beforeWithdraw = func(gatewayCurrentLANRecoveryPhysicalAction) {
					f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
						return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
					})
				}
			case "client cancellation":
				driver.beforeWithdraw = func(gatewayCurrentLANRecoveryPhysicalAction) { cancel() }
			}
			err = f.manager.QuarantineGatewayV2LANAccessRecoveryBatch(ctx, grants, disables)
			if stage == "client cancellation" {
				if err != nil || len(driver.ownedStops) != 0 || f.manager.gatewayRebindAdmissionBlocked() {
					t.Fatalf("client cancellation interrupted bounded withdrawal completion: %v", err)
				}
			} else if err == nil || len(driver.ownedStops) != 1 || !f.manager.gatewayRebindAdmissionBlocked() ||
				!reflect.DeepEqual(driver.ownedStops[0].State, want) {
				t.Fatalf("uncertain effect escaped exact owned stop and admission refusal: %v", err)
			}
			installed, loadErr := f.store.load()
			if loadErr != nil || !reflect.DeepEqual(installed, want) || driver.withdrawCalls != 1 {
				t.Fatal("effect path rewrote recovery intent, advanced a head, or repeated withdrawal")
			}
		})
	}
}

func TestGatewayCurrentLANRecoveryBatchQuarantinePreservesCompletedQueue(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchRetirementFixture(t)
	driver := &gatewayCurrentLANBatchQuarantineDriver{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t)}
	f.manager.gatewayCurrentPhysicalDriver = driver
	if err := f.manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatalf("completed queue reattestation: %v", err)
	}
	installed, err := f.store.load()
	if err != nil || !reflect.DeepEqual(installed, batch) || driver.withdrawCalls != 0 ||
		driver.calls != 1 || !driver.last.Complete || len(driver.ownedStops) != 0 {
		t.Fatal("completed queue was overwritten or executed a head effect")
	}
}

func TestGatewayCurrentLANRecoveryBatchQuarantinePreservesPendingEvidence(t *testing.T) {
	f, _, grants, disables := gatewayCurrentLANBatchPendingClaimsFixture(t)
	f.manager.gatewayRebindFailStop, f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
	transition, err := gatewayCurrentDisableTransition(f.baseline, disables[0].Request)
	if err != nil || f.store.saveNext(f.baseline, transition.Pending) != nil {
		t.Fatalf("pending disable fixture: %v", err)
	}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	want, err := gatewayCurrentLANRecoveryInstallState(transition.Pending, claims)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentLANBatchQuarantineDriver{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t)}
	f.manager.gatewayCurrentPhysicalDriver = driver
	if err := f.manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatalf("quarantine prior pending operation: %v", err)
	}
	installed, err := f.store.load()
	if err != nil || !reflect.DeepEqual(installed, want) ||
		!reflect.DeepEqual(installed.LANRecovery.LegacyPending, transition.Pending.Pending) ||
		driver.withdrawCalls != 1 || len(driver.ownedStops) != 0 {
		t.Fatal("quarantine lost the prior operation marker or changed its immutable queue")
	}
}

func TestGatewayCurrentLANRecoveryBatchQuarantinesAllItems(t *testing.T) {
	f, _, grants, disables := gatewayCurrentLANBatchPendingClaimsFixture(t)
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
	if err != nil {
		t.Fatal(err)
	}
	want, err := gatewayCurrentLANRecoveryInstallState(f.baseline, claims)
	if err != nil {
		t.Fatal(err)
	}
	driver := &gatewayCurrentLANBatchQuarantineDriver{gatewayCurrentLANBatchRetirementDriver: newGatewayCurrentLANBatchRetirementDriver(t)}
	driver.beforeWithdraw = func(action gatewayCurrentLANRecoveryPhysicalAction) {
		installed, err := f.store.load()
		if err != nil || !reflect.DeepEqual(installed, want) || !reflect.DeepEqual(action.Selected, want) ||
			len(action.Selected.LANRecovery.Items) != 2 {
			t.Fatal("withdrawal preceded the exact immutable whole-batch installation")
		}
		for _, item := range want.LANRecovery.Items {
			if action.Withdrawn.Apps[item.AppID].LAN != nil || !reflect.DeepEqual(action.Withdrawn.Apps[item.AppID].Route, want.Apps[item.AppID].Route) {
				t.Fatal("whole-batch projection left a later listener or changed loopback routing")
			}
		}
	}
	f.manager.gatewayCurrentPhysicalDriver = driver
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatalf("quarantine selected current queue: %v", err)
	}
	installed, loadErr := f.store.load()
	after, historyErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if loadErr != nil || historyErr != nil || !reflect.DeepEqual(installed, want) ||
		!sameGatewayHistorySnapshot(history, after) || driver.withdrawCalls != 1 || len(driver.ownedStops) != 0 {
		t.Fatal("quarantine changed immutable history/queue or failed to withdraw exactly once")
	}
	// Re-entry proves the same withdrawn queue without rewriting its revision,
	// publishing anything, advancing its head or repeating an effect.
	if err := f.manager.QuarantineGatewayV2LANAccessRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatalf("re-enter current quarantine: %v", err)
	}
	if installed, err := f.store.load(); err != nil || !reflect.DeepEqual(installed, want) || driver.withdrawCalls != 1 {
		t.Fatal("quarantine replay changed evidence or repeated withdrawal")
	}
}
