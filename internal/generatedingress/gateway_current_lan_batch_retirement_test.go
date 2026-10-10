package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

// This fixture completes the protected queue with pure transitions and
// projected SQL claims. The public retirement call is the behavior under test;
// the fixture does not stand in for ordered SQL finalization acceptance.
func gatewayCurrentLANBatchRetirementFixture(t *testing.T) (gatewayCurrentStateFixture,
	appaccess.GatewayRebindRecoverySnapshot, []GatewayV2LANStartupClaim,
	[]GatewayV2LANDisableStartupClaim, gatewayCurrentRouteState,
) {
	t.Helper()
	f, snapshot, grants, disables, batch := gatewayCurrentLANBatchObservationFixture(t)
	f.manager.gatewayRebindFailStop = &atomic.Bool{}
	f.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	for _, item := range batch.LANRecovery.Items {
		found := false
		for index := range disables {
			if disables[index].Request.OperationID != item.Disable.OperationID {
				continue
			}
			found = true
			disables[index].State, disables[index].StateSequence = appaccess.AppAccessDisableCommitted, 3
			disables[index].RetainedBinding, disables[index].CurrentBinding = disables[index].CurrentBinding, nil
			grants[index].RetainedBinding, grants[index].CurrentBinding = grants[index].CurrentBinding, nil
			cleared, err := gatewayCurrentLANRecoveryClearedHeadState(batch)
			if err != nil || f.store.saveNext(batch, cleared) != nil {
				t.Fatalf("fixture head clearance: %v", err)
			}
			disables[index].ClearAcknowledged = true
			advanced, err := gatewayCurrentLANRecoveryAdvanceHeadState(cleared)
			if err != nil || f.store.saveNext(cleared, advanced) != nil {
				t.Fatalf("fixture head advancement: %v", err)
			}
			batch = advanced
			break
		}
		if !found {
			t.Fatal("missing exact queue claim")
		}
	}
	return f, snapshot, grants, disables, batch
}

type gatewayCurrentLANBatchRetirementDriver struct {
	*gatewayCurrentLANBatchObserverDriver
	batchErr     error
	stopped      bool
	stableErr    error
	stopErr      error
	stableBefore func(gatewayCurrentSelection)
	stopBefore   func(gatewayCurrentOwnedStopTarget)
	stableCalls  int
	ownedStops   []gatewayCurrentOwnedStopTarget
}

func (d *gatewayCurrentLANBatchRetirementDriver) attestGatewayCurrentLANRecoveryBatch(ctx context.Context,
	action gatewayCurrentLANRecoveryPhysicalAction,
) (gatewayCurrentLANRecoveryPhysicalResult, error) {
	if d.batchErr != nil {
		return gatewayCurrentLANRecoveryPhysicalResult{}, d.batchErr
	}
	proof, err := d.gatewayCurrentLANBatchObserverDriver.attestGatewayCurrentLANRecoveryBatch(ctx, action)
	if err == nil && d.stopped {
		proof.Attestation.Outcome = gatewayCurrentPhysicalRecoveryStopped
		proof.Attestation.Runtime.ListenerAbsent = true
		proof.Attestation.Runtime.Digest, _ = gatewayCurrentRuntimeProofDigest(proof.Attestation.Runtime)
		proof.Attestation.Digest, _ = gatewayCurrentPhysicalAttestationDigest(proof.Attestation)
		proof.Digest, _ = gatewayCurrentLANRecoveryPhysicalResultDigest(proof)
		if !validGatewayCurrentLANRecoveryPhysicalResult(action, proof) {
			d.t.Fatal("stopped batch fixture must be valid physical absence evidence")
		}
	}
	return proof, err
}

func (d *gatewayCurrentLANBatchRetirementDriver) attestGatewayCurrentPhysical(_ context.Context,
	selection gatewayCurrentSelection,
) (gatewayCurrentPhysicalAttestation, error) {
	d.stableCalls++
	if d.stableBefore != nil {
		d.stableBefore(selection)
	}
	if d.stableErr != nil {
		return gatewayCurrentPhysicalAttestation{}, d.stableErr
	}
	terminal, err := gatewayCurrentSelectionTerminalView(selection)
	if err != nil || selection.State == nil || selection.State.LANRecovery != nil {
		d.t.Fatalf("stable retirement proof did not select a retired state: %v", err)
	}
	return gatewayCurrentPhysicalAttestationFixture(d.t, *selection.State, terminal, gatewayCurrentPhysicalStableServing), nil
}

func (d *gatewayCurrentLANBatchRetirementDriver) stopGatewayCurrentOwnedTarget(ctx context.Context,
	target gatewayCurrentOwnedStopTarget,
) error {
	if ctx.Err() != nil || !validGatewayCurrentOwnedStopTarget(target) || target.Transition != nil {
		d.t.Fatal("emergency stop lacks independent context or exact state-only ownership")
	}
	d.ownedStops = append(d.ownedStops, target)
	if d.stopBefore != nil {
		d.stopBefore(target)
	}
	return d.stopErr
}

func newGatewayCurrentLANBatchRetirementDriver(t *testing.T) *gatewayCurrentLANBatchRetirementDriver {
	return &gatewayCurrentLANBatchRetirementDriver{gatewayCurrentLANBatchObserverDriver: &gatewayCurrentLANBatchObserverDriver{
		gatewayCurrentPhysicalDriverFake: &gatewayCurrentPhysicalDriverFake{}, t: t,
	}}
}

func TestGatewayCurrentLANRecoveryRetiresCompletedBatch(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchRetirementFixture(t)
	want, err := gatewayCurrentLANRecoveryRetiredState(batch)
	if err != nil {
		t.Fatal(err)
	}
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err != nil {
		t.Fatalf("retire selected current queue: %v", err)
	}
	installed, loadErr := f.store.load()
	after, historyErr := readGatewayHistorySnapshotMode(f.manager.store, true)
	if loadErr != nil || historyErr != nil || !reflect.DeepEqual(installed, want) ||
		!sameGatewayHistorySnapshot(history, after) {
		t.Fatal("retirement failed to install only the next current revision or changed immutable history")
	}
	if driver.calls == 0 || !driver.last.Complete || !reflect.DeepEqual(driver.last.Selected, batch) ||
		driver.stableCalls != 1 || len(driver.ownedStops) != 0 ||
		driver.applyCalls+driver.restoreCalls+driver.stopCalls != 0 || f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatal("retirement omitted completed-batch proof or performed an ordinary physical effect")
	}
	if present, err := f.manager.HasGatewayV2LANRecoveryBatch(context.Background()); err != nil || present {
		t.Fatalf("retired batch remains selected: present=%t error=%v", present, err)
	}
}

func TestGatewayCurrentLANRecoveryRetirementRefusesUnfinishedQueue(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchObservationFixture(t)
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	if err := f.manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err == nil {
		t.Fatal("unfinished queue retired")
	}
	installed, err := f.store.load()
	if err != nil || !reflect.DeepEqual(installed, batch) || driver.calls != 0 || driver.stableCalls != 0 || len(driver.ownedStops) != 0 {
		t.Fatal("unfinished queue changed protected state or reached physical driver")
	}
}

func TestGatewayCurrentLANRecoveryRetirementRefusesMissingAcknowledgement(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchRetirementFixture(t)
	disables[0].ClearAcknowledged = false
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	if _, err := validateGatewayV2LANAccessStartupClaims(grants, disables); err != nil {
		t.Fatal("negative terminal census must remain structurally valid")
	}
	if err := f.manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err == nil {
		t.Fatal("queue retired without all clear acknowledgements")
	}
	installed, err := f.store.load()
	if err != nil || !reflect.DeepEqual(installed, batch) || driver.calls != 0 || driver.stableCalls != 0 || len(driver.ownedStops) != 0 {
		t.Fatal("incomplete terminal census changed evidence or reached physical driver")
	}
}

func TestGatewayCurrentLANRecoveryRetirementRetainsStoppedBatch(t *testing.T) {
	f, _, grants, disables, batch := gatewayCurrentLANBatchRetirementFixture(t)
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	driver.stopped = true
	f.manager.gatewayCurrentPhysicalDriver = driver
	if err := f.manager.RetireGatewayV2LANRecoveryBatch(context.Background(), grants, disables); err == nil {
		t.Fatal("stopped topology retired its recovery marker")
	}
	installed, err := f.store.load()
	if err != nil || !reflect.DeepEqual(installed, batch) || driver.calls != 1 || driver.stableCalls != 0 || len(driver.ownedStops) != 0 {
		t.Fatal("stopped completed batch changed evidence or attempted another physical effect")
	}
}

func TestGatewayCurrentLANRecoveryRetirementStopsExactOwnerOnLostProof(t *testing.T) {
	for _, stage := range []string{"batch proof", "SQL after batch proof", "stable proof", "cancelled stable proof", "SQL after retirement", "stop failure"} {
		t.Run(stage, func(t *testing.T) {
			f, _, grants, disables, batch := gatewayCurrentLANBatchRetirementFixture(t)
			driver := newGatewayCurrentLANBatchRetirementDriver(t)
			f.manager.gatewayCurrentPhysicalDriver = driver
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := batch
			sqlUnavailable := func() {
				f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
					return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unreadable")
				})
			}
			switch stage {
			case "batch proof":
				driver.batchErr = errors.New("whole-batch proof unavailable")
			case "SQL after batch proof":
				driver.before = func(gatewayCurrentLANRecoveryPhysicalAction) { sqlUnavailable() }
			default:
				var err error
				want, err = gatewayCurrentLANRecoveryRetiredState(batch)
				if err != nil {
					t.Fatal(err)
				}
				switch stage {
				case "stable proof":
					driver.stableErr = errors.New("stable topology proof unavailable")
				case "cancelled stable proof":
					driver.stableBefore = func(gatewayCurrentSelection) { cancel() }
				case "SQL after retirement":
					driver.stableBefore = func(gatewayCurrentSelection) { sqlUnavailable() }
				case "stop failure":
					driver.stableErr, driver.stopErr = errors.New("stable proof unavailable"), errors.New("stop acknowledgement lost")
				}
			}
			history, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			err = f.manager.RetireGatewayV2LANRecoveryBatch(ctx, grants, disables)
			if err == nil || !f.manager.gatewayRebindAdmissionBlocked() || len(driver.ownedStops) != 1 {
				t.Fatalf("failure did not retain admission and stop exact owner: error=%v blocked=%t stops=%d",
					err, f.manager.gatewayRebindAdmissionBlocked(), len(driver.ownedStops))
			}
			var diagnostic *Error
			if !errors.As(err, &diagnostic) || diagnostic.candidateMayBeLive != (stage == "stop failure") {
				t.Fatalf("incorrect uncertain-publication signal: %+v", diagnostic)
			}
			installed, loadErr := f.store.load()
			after, historyErr := readGatewayHistorySnapshotMode(f.manager.store, true)
			if loadErr != nil || historyErr != nil || !reflect.DeepEqual(installed, want) ||
				!reflect.DeepEqual(driver.ownedStops[0].State, want) || !sameGatewayHistorySnapshot(history, after) {
				t.Fatal("failure rewrote selected evidence, stopped different state, or changed immutable history")
			}
			if driver.applyCalls+driver.restoreCalls+driver.stopCalls != 0 {
				t.Fatal("batch stop fabricated an ordinary physical transition")
			}
		})
	}
}
