package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindInteractiveForwardSessionGuardIsRequestScoped(t *testing.T) {
	fixture, base, request := newGatewayRebindTypedForwardOnlyFixture(t)
	ctx := context.Background()
	expected, err := fixture.predecessor.manager.readGatewayRebindTypedForwardBoundaryLocked(ctx, request)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("default private driver is unchanged", func(t *testing.T) {
		actual, err := base.readGatewayRebindTypedForwardBoundaryLocked(ctx, request)
		if err != nil || !reflect.DeepEqual(expected, actual) {
			t.Fatalf("nil session guard changed the forward boundary: %v", err)
		}
		if err := base.confirmForwardServingLocked(ctx, request); err != nil {
			t.Fatalf("nil session guard changed forward confirmation: %v", err)
		}
	})

	t.Run("concurrent invocations do not share authority", func(t *testing.T) {
		var allowedCalls, deniedCalls atomic.Int64
		ready := make(chan struct{}, 2)
		release := make(chan struct{})
		allowed := base
		allowed.forwardSessionGuard = func(context.Context) error {
			if allowedCalls.Add(1) == 1 {
				ready <- struct{}{}
				<-release
			}
			return nil
		}
		denied := base
		denied.forwardSessionGuard = func(context.Context) error {
			if deniedCalls.Add(1) == 1 {
				ready <- struct{}{}
				<-release
			}
			return errors.New("session revoked")
		}
		type result struct {
			boundary gatewayRebindTypedAttemptBoundary
			err      error
		}
		results := make(chan result, 2)
		var calls sync.WaitGroup
		calls.Add(2)
		go func() {
			defer calls.Done()
			boundary, err := allowed.readGatewayRebindTypedForwardBoundaryLocked(ctx, request)
			results <- result{boundary: boundary, err: err}
		}()
		go func() {
			defer calls.Done()
			boundary, err := denied.readGatewayRebindTypedForwardBoundaryLocked(ctx, request)
			results <- result{boundary: boundary, err: err}
		}()
		<-ready
		<-ready
		close(release)
		calls.Wait()
		close(results)
		var succeeded, refused int
		for observed := range results {
			if observed.err == nil {
				succeeded++
				if !reflect.DeepEqual(expected, observed.boundary) {
					t.Fatal("allowed invocation returned a different authority boundary")
				}
			} else {
				refused++
				if !reflect.DeepEqual(observed.boundary, gatewayRebindTypedAttemptBoundary{}) {
					t.Fatal("denied invocation returned forward authority")
				}
			}
		}
		if succeeded != 1 || refused != 1 || allowedCalls.Load() != 2 || deniedCalls.Load() != 1 {
			t.Fatalf("request-local session isolation failed: success=%d refused=%d allowedCalls=%d deniedCalls=%d",
				succeeded, refused, allowedCalls.Load(), deniedCalls.Load())
		}
	})

	for _, test := range []struct {
		name      string
		refuseAt  int64
		cancelAt  int64
		wantCalls int64
	}{
		{name: "revoked before authority read", refuseAt: 1, wantCalls: 1},
		{name: "revoked after authority read", refuseAt: 2, wantCalls: 2},
		{name: "cancelled after authority read", cancelAt: 2, wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var guardCalls atomic.Int64
			driver := base
			driver.forwardSessionGuard = func(context.Context) error {
				call := guardCalls.Add(1)
				if call == test.cancelAt {
					cancel()
					return nil
				}
				if call == test.refuseAt {
					return errors.New("session revoked")
				}
				return nil
			}
			fixture.runner.effects, fixture.runner.absenceRequests = nil, nil
			err := driver.confirmForwardServingLocked(callCtx, request)
			if err == nil || guardCalls.Load() != test.wantCalls || len(fixture.runner.effects) != 0 ||
				len(fixture.runner.absenceRequests) != 0 {
				t.Fatalf("forward session refusal reached the physical driver: calls=%d effects=%v probes=%v err=%v",
					guardCalls.Load(), fixture.runner.effects, fixture.runner.absenceRequests, err)
			}
		})
	}
}

func TestGatewayRebindInteractiveSessionGuardsCurrentAttestation(t *testing.T) {
	for _, kind := range []string{"default nil", "allowed", "revoked before driver", "revoked during driver", "cancelled after authority read"} {
		t.Run(kind, func(t *testing.T) {
			f, _, selection, _ := gatewayRebindCommittedServingFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			port := f.journal.Source.LocalHostPort
			runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
			physical := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, port)
			observations := &gatewayRebindCurrentObservationRuntime{gatewayCurrentPhysicalRuntime: physical.runtime}
			physical.runtime = observations
			f.manager.gatewayCurrentPhysicalDriver = physical
			var guardCalls atomic.Int64
			var revoked atomic.Bool
			driver := managerGatewayRebindCrossStoreDriver{manager: f.manager}
			if kind != "default nil" {
				driver.forwardSessionGuard = func(context.Context) error {
					call := guardCalls.Add(1)
					if kind == "cancelled after authority read" && call == 2 {
						cancel()
						return nil
					}
					if kind == "revoked before driver" || revoked.Load() {
						return errors.New("session revoked")
					}
					return nil
				}
			}
			if kind == "revoked during driver" {
				observations.after = func(call int, _ *gatewayCurrentPhysicalAttestation) {
					if call == 1 {
						revoked.Store(true)
					}
				}
			}
			digest, err := driver.attestCommittedCurrentLocked(ctx, selection)
			allowed := kind == "default nil" || kind == "allowed"
			if allowed {
				if err != nil || !validSHA256(digest) || observations.calls != 2 || len(runner.effects) != 0 ||
					(kind == "default nil" && guardCalls.Load() != 0) || (kind == "allowed" && guardCalls.Load() == 0) {
					t.Fatalf("authorized attestation changed: guards=%d observations=%d effects=%v err=%v",
						guardCalls.Load(), observations.calls, runner.effects, err)
				}
			} else if err == nil || digest != "" || len(runner.effects) != 0 ||
				(kind != "revoked during driver" && observations.calls != 0) ||
				(kind == "revoked during driver" && observations.calls != 1) {
				t.Fatalf("session refusal reached a later attestation boundary: guards=%d observations=%d effects=%v err=%v",
					guardCalls.Load(), observations.calls, runner.effects, err)
			}
		})
	}
}

func TestGatewayRebindInteractiveSessionRevocationDoesNotBlockOwnedWithdrawal(t *testing.T) {
	fixture, driver, request := newGatewayRebindTypedForwardOnlyFixture(t)
	ctx := context.Background()
	before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	var guardCalls atomic.Int64
	driver.forwardSessionGuard = func(context.Context) error {
		guardCalls.Add(1)
		return errors.New("session revoked")
	}
	fixture.runner.effects = nil
	err = driver.withdrawForwardSuccessorLocked(ctx, request)
	wantEffects := [][]string{{"container", "stop", "--time", "10", fixture.runner.finalID}}
	if err == nil || guardCalls.Load() != 0 || fixture.runner.final.Running ||
		!reflect.DeepEqual(fixture.runner.effects, wantEffects) ||
		!fixture.predecessor.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("session revocation blocked or broadened exact-owned withdrawal: guards=%d final=%t effects=%v err=%v",
			guardCalls.Load(), fixture.runner.final.Running, fixture.runner.effects, err)
	}
	after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
		fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil ||
		request.Mode != gatewayRebindPhysicalReconcileForwardOnly || request.Terminal == nil ||
		request.Terminal.Disposition != appaccess.GatewayRebindDispositionCommit {
		t.Fatalf("owned withdrawal changed durable authority: SQL=%v history=%v", sqlErr, historyErr)
	}
}
