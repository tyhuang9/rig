package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayCurrentPhysicalRuntimeFake struct {
	observeFn   func(context.Context, gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error)
	reconcileFn func(context.Context, gatewayCurrentPhysicalTarget, func(context.Context) error) error
	stopFn      func(context.Context, []gatewayCurrentPhysicalTarget, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
	observes    int
	reconciles  int
	stops       int
}

func (f *gatewayCurrentPhysicalRuntimeFake) observe(ctx context.Context,
	target gatewayCurrentPhysicalTarget,
) (gatewayCurrentPhysicalAttestation, error) {
	f.observes++
	if f.observeFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("physical state unavailable")
	}
	return f.observeFn(ctx, target)
}

func (f *gatewayCurrentPhysicalRuntimeFake) reconcile(ctx context.Context,
	target gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) error {
	f.reconciles++
	if f.reconcileFn == nil {
		return errors.New("physical reconcile unavailable")
	}
	return f.reconcileFn(ctx, target, guard)
}

func (f *gatewayCurrentPhysicalRuntimeFake) stop(ctx context.Context,
	targets []gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	f.stops++
	if f.stopFn == nil {
		return gatewayCurrentPhysicalAttestation{}, errors.New("physical stop unavailable")
	}
	return f.stopFn(ctx, targets, guard)
}

type gatewayCurrentSnapshotFunc func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error)

func (f gatewayCurrentSnapshotFunc) GatewayRebindRecoverySnapshot(ctx context.Context) (
	appaccess.GatewayRebindRecoverySnapshot, error,
) {
	return f(ctx)
}

func TestManagedGatewayCurrentPhysicalDriverReconcilesOnlyExactSelectedMarker(t *testing.T) {
	fixture, transition, snapshot := managedGatewayCurrentPhysicalTransitionFixture(t)
	physical := false
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.observeFn = func(_ context.Context, target gatewayCurrentPhysicalTarget) (
		gatewayCurrentPhysicalAttestation, error,
	) {
		if !physical {
			return gatewayCurrentPhysicalAttestation{}, errors.New("before topology")
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, target,
			gatewayCurrentPhysicalRecoveryEffective), nil
	}
	runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) error {
		if err := guard(ctx); err != nil {
			return err
		}
		physical = true
		return guard(ctx)
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	proof, err := driver.applyGatewayCurrentPhysical(context.Background(), transition)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryEffective ||
		!reflect.DeepEqual(proof.State, transition.Effective) || runtime.reconciles != 1 || runtime.observes != 2 {
		t.Fatalf("exact selected-current apply failed: outcome=%s reconciles=%d observes=%d error=%v",
			proof.Outcome, runtime.reconciles, runtime.observes, err)
	}

	drifted := false
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		value := snapshot
		if drifted {
			value.RollbackAllowed = true
		}
		return value, nil
	})
	physical = false
	runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) error {
		if err := guard(ctx); err != nil {
			return err
		}
		physical, drifted = true, true
		return nil
	}
	if _, err := driver.applyGatewayCurrentPhysical(context.Background(), transition); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("SQL drift after physical proof was accepted: %v", err)
	}
}

func TestManagedGatewayCurrentPhysicalDriverReconcilesLostAcknowledgement(t *testing.T) {
	fixture, transition, _ := managedGatewayCurrentPhysicalTransitionFixture(t)
	physical := false
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.observeFn = func(_ context.Context, target gatewayCurrentPhysicalTarget) (
		gatewayCurrentPhysicalAttestation, error,
	) {
		if !physical {
			return gatewayCurrentPhysicalAttestation{}, errors.New("before topology")
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, target,
			gatewayCurrentPhysicalRecoveryEffective), nil
	}
	runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) error {
		if err := guard(ctx); err != nil {
			return err
		}
		physical = true
		return context.Canceled
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	proof, err := driver.applyGatewayCurrentPhysical(context.Background(), transition)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryEffective || runtime.reconciles != 1 ||
		runtime.observes != 2 {
		t.Fatalf("lost physical acknowledgement was not reconciled: proof=%#v error=%v", proof, err)
	}
}

func TestManagedGatewayCurrentPhysicalDriverStopUsesProtectedAuthorityAcrossLatchAndSQLFailure(t *testing.T) {
	fixture, transition, _ := managedGatewayCurrentPhysicalTransitionFixture(t)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	fixture.manager.gatewayRebindFailStop.Store(true)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
	})
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.stopFn = func(ctx context.Context, targets []gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if len(targets) != 2 || targets[0].Resources.FinalContainer == nil ||
			targets[0].Resources.FinalContainer.ID != fixture.receipt.Resources.FinalContainer.ID {
			return gatewayCurrentPhysicalAttestation{}, errors.New("wrong owned-stop target")
		}
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, targets[0],
			gatewayCurrentPhysicalRecoveryStopped), nil
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager},
		runtime:                             runtime,
	}
	proof, err := driver.stopGatewayCurrentPhysical(context.Background(), transition)
	if err != nil || proof.Outcome != gatewayCurrentPhysicalRecoveryStopped || runtime.stops != 1 {
		t.Fatalf("latched protected-only withdrawal failed: stops=%d proof=%#v error=%v",
			runtime.stops, proof, err)
	}

	changed := transition.Pending
	changed.Pending = cloneGatewayCurrentPendingRoute(changed.Pending)
	changed.Pending.Proposed.Route.Endpoints[0].ContainerID = "sha256:" + changed.Pending.Proposed.Route.Endpoints[0].ContainerID
	changed.Digest = ""
	changed.Digest, _ = gatewayCurrentRouteStateDigest(changed)
	if err := fixture.store.saveNext(transition.Pending, changed); err == nil {
		t.Fatal("invalid replacement state unexpectedly installed")
	}
	foreign := transition
	foreign.Pending.Digest = ""
	foreign.Pending.Revision++
	foreign.Pending.Digest, _ = gatewayCurrentRouteStateDigest(foreign.Pending)
	if _, err := driver.stopGatewayCurrentPhysical(context.Background(), foreign); err == nil || runtime.stops != 1 {
		t.Fatalf("foreign protected marker reached stop: stops=%d error=%v", runtime.stops, err)
	}
}

func TestManagedGatewayCurrentPhysicalDriverStopsStableOwnedTargetWithoutSQLOrLatchAdmission(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.manager.gatewayCurrentOwnedStopTargetForStateLocked(fixture.baseline)
	if err != nil {
		t.Fatal(err)
	}
	sqlReads := 0
	fresh := &Manager{store: fixture.manager.store, options: fixture.manager.options, mu: newContextMutex(),
		gatewayRebindFailStop: &atomic.Bool{}, gatewayRebindCommitBarrier: &atomic.Bool{}}
	fresh.gatewayRebindFailStop.Store(true)
	fresh.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		sqlReads++
		return appaccess.GatewayRebindRecoverySnapshot{}, errors.New("SQL unavailable")
	})
	runtime := &gatewayCurrentPhysicalRuntimeFake{}
	runtime.stopFn = func(ctx context.Context, targets []gatewayCurrentPhysicalTarget,
		guard func(context.Context) error,
	) (gatewayCurrentPhysicalAttestation, error) {
		if len(targets) != 1 || targets[0].Pending != nil || targets[0].LANRecovery != nil ||
			targets[0].Resources.FinalContainer == nil ||
			targets[0].Resources.FinalContainer.ID != owned.FinalContainer.ID {
			return gatewayCurrentPhysicalAttestation{}, errors.New("wrong stable owned target")
		}
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		return gatewayCurrentPhysicalDriverTestAttestation(t, targets[0],
			gatewayCurrentPhysicalRecoveryStopped), nil
	}
	driver := managedGatewayCurrentPhysicalDriver{
		managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fresh},
		runtime:                             runtime,
	}
	if err := driver.stopGatewayCurrentOwnedTarget(context.Background(), owned); err != nil ||
		runtime.stops != 1 || sqlReads != 0 {
		t.Fatalf("stable owned withdrawal: stops=%d SQL reads=%d error=%v", runtime.stops, sqlReads, err)
	}

	replacement := owned
	replacement.FinalContainer.ID = strings.Repeat("8", 64)
	replacement.Digest, _ = gatewayCurrentOwnedStopTargetDigest(replacement)
	if err := driver.stopGatewayCurrentOwnedTarget(context.Background(), replacement); err == nil || runtime.stops != 1 {
		t.Fatalf("replacement owned identity reached stop: stops=%d error=%v", runtime.stops, err)
	}
}

func TestGatewayCurrentPhysicalOwnedStopTargetsUseCanonicalWholeBatchProjection(t *testing.T) {
	fixture, selection := gatewayCurrentLANRecoveryPhysicalSelectionFixture(t)
	owned, err := fixture.manager.gatewayCurrentOwnedStopTargetForStateLocked(*selection.State)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := gatewayCurrentPhysicalTargetsForOwnedStop(owned)
	if err != nil || len(targets) != 2 {
		t.Fatalf("whole-batch stop targets=%#v error=%v", targets, err)
	}
	projection, err := gatewayCurrentPhysicalOutcomeProjectionForSelection(*selection.State)
	if err != nil || projection.Withdrawn == nil {
		t.Fatal("missing canonical whole-batch withdrawal projection")
	}
	if !reflect.DeepEqual(targets[0].State, projection.Before) ||
		!reflect.DeepEqual(targets[1].State, *projection.Withdrawn) {
		t.Fatalf("owned batch stop targets did not use canonical endpoints: %#v", targets)
	}
	for _, target := range targets {
		if !reflect.DeepEqual(target.LANRecovery, selection.State.LANRecovery) || target.Pending != nil {
			t.Fatalf("owned batch target lost exact retained queue: %#v", target)
		}
	}
}

func managedGatewayCurrentPhysicalTransitionFixture(t *testing.T) (
	gatewayCurrentStateFixture, gatewayCurrentPhysicalTransition, appaccess.GatewayRebindRecoverySnapshot,
) {
	t.Helper()
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	appID, app := routeOperationTransferredApp(t, fixture.baseline)
	transition, err := gatewayCurrentSwitchTransition(fixture.baseline,
		routeOperationSwitchRequest(t, appID, app.Route))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.saveNext(transition.Before, transition.Pending); err != nil {
		t.Fatal(err)
	}
	snapshot := routeOperationCommittedSnapshot(fixture)
	fixture.manager.options.RebindCurrentStateRepository = gatewayCurrentSnapshotFunc(func(context.Context) (
		appaccess.GatewayRebindRecoverySnapshot, error,
	) {
		return snapshot, nil
	})
	return fixture, transition, snapshot
}

func gatewayCurrentPhysicalDriverTestAttestation(t *testing.T, target gatewayCurrentPhysicalTarget,
	outcome gatewayCurrentPhysicalOutcome,
) gatewayCurrentPhysicalAttestation {
	t.Helper()
	value := gatewayCurrentPhysicalAttestationFixture(t, target.State, target.Terminal, outcome)
	value.Pending = cloneGatewayCurrentPendingRoute(target.Pending)
	value.LANRecovery = cloneGatewayCurrentLANRecoveryBatch(target.LANRecovery)
	if outcome == gatewayCurrentPhysicalRecoveryStopped {
		value.Runtime.ListenerAbsent = true
		var err error
		value.Runtime.Digest, err = gatewayCurrentRuntimeProofDigest(value.Runtime)
		if err != nil {
			t.Fatal(err)
		}
	}
	var err error
	value.Digest, err = gatewayCurrentPhysicalAttestationDigest(value)
	if err != nil || !validGatewayCurrentPhysicalAttestation(value) {
		t.Fatalf("build driver attestation: valid=%t error=%v", validGatewayCurrentPhysicalAttestation(value), err)
	}
	return value
}
