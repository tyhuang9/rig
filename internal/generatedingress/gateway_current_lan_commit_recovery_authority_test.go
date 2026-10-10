package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestGatewayCurrentLANCommitRecoveryWithdrawsAfterAuthorityLoss(t *testing.T) {
	for _, kind := range []gatewayV2PendingKind{gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal} {
		t.Run(string(kind), func(t *testing.T) {
			fixture, driver, request, pending := newGatewayCurrentCommitRecoveryFixture(t, kind)
			applyBefore, restoreBefore := driver.applyCalls, driver.restoreCalls
			revoked := false
			revoke := func() { revoked = true }
			if kind == gatewayV2PendingLANGrant {
				driver.afterApply = revoke
			} else {
				driver.afterRestore = revoke
			}
			checks := 0
			err := fixture.manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
				func(_ context.Context, observed GatewayV2LANGrantObservation) error {
					checks++
					if observed.Request != request || observed.Disposition != GatewayV2LANGrantWithdrawnPendingReconciliation {
						t.Fatal("authority check lost pinned recovery identity")
					}
					if revoked {
						return errors.New("grant approver or session revoked")
					}
					return nil
				})
			if err == nil {
				t.Fatal("republished grant after its authority was revoked")
			}
			if !revoked || checks < 2 {
				t.Fatalf("authority was not rechecked after publication: revoked=%t checks=%d", revoked, checks)
			}
			if got := routeOperationLoad(t, fixture.store); !reflect.DeepEqual(got, pending) {
				t.Fatal("authority loss cleared or rewrote the retained recovery marker")
			}
			wantApply, wantRestore := 1, 2
			if kind == gatewayV2PendingLANWithdrawal {
				wantApply, wantRestore = 2, 1
			}
			if driver.applyCalls-applyBefore != wantApply || driver.restoreCalls-restoreBefore != wantRestore || driver.stopCalls != 0 {
				t.Fatalf("revocation did not perform exact owned withdrawal: apply=%d restore=%d stop=%d", driver.applyCalls-applyBefore, driver.restoreCalls-restoreBefore, driver.stopCalls)
			}
		})
	}
}

// Model a guarded adapter explicitly; unguarded adapters remain unavailable for
// serving recovery. The production adapter's inner effect boundary is tested below.
func (d *gatewayCurrentStateMachineDriver) applyGatewayCurrentPhysicalAuthorized(ctx context.Context,
	transition gatewayCurrentPhysicalTransition, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if err := authorize(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proof, err := d.applyGatewayCurrentPhysical(ctx, transition)
	if err == nil {
		err = authorize(ctx)
	}
	return proof, err
}

func (d *gatewayCurrentStateMachineDriver) restoreGatewayCurrentPhysicalAuthorized(ctx context.Context,
	transition gatewayCurrentPhysicalTransition, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if err := authorize(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proof, err := d.restoreGatewayCurrentPhysical(ctx, transition)
	if err == nil {
		err = authorize(ctx)
	}
	return proof, err
}

func newGatewayCurrentCommitRecoveryFixture(t *testing.T, kind gatewayV2PendingKind) (
	gatewayCurrentStateFixture, *gatewayCurrentStateMachineDriver, GatewayV2LANGrantRequest, gatewayCurrentRouteState,
) {
	t.Helper()
	fixture, driver, request := newGatewayCurrentGrantOperationFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, fixture.manager, request, func(lease *fakeGatewayV2LANGrantLease) {
		if kind == gatewayV2PendingLANGrant {
			lease.activateErr = errors.New("uncertain commit")
		}
	})
	_, err := fixture.manager.GrantGatewayV2LAN(context.Background(), request, authorize)
	if kind == gatewayV2PendingLANGrant {
		if !IsCode(err, DiagnosticRouteUnresolved) {
			t.Fatalf("expected uncertain grant: %v", err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		err = fixture.manager.WithGatewayV2LANCommitResolution(context.Background(), request,
			func(context.Context, GatewayV2LANGrantReceipt) error { return errors.New("lost acknowledgment") })
		if err == nil {
			t.Fatal("expected retained withdrawal")
		}
	}
	pending := routeOperationLoad(t, fixture.store)
	if pending.Pending == nil || pending.Pending.Kind != kind || !pending.Pending.ActivationUncertain {
		t.Fatal("expected exact uncertain marker")
	}
	return fixture, driver, request, pending
}

func TestGatewayCurrentLANCommitRecoveryRetainsAuthorityAtCompletion(t *testing.T) {
	for _, kind := range []gatewayV2PendingKind{gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal} {
		for _, boundary := range []string{"before_publication", "after_clear"} {
			t.Run(string(kind)+"/"+boundary, func(t *testing.T) {
				fixture, driver, request, pending := newGatewayCurrentCommitRecoveryFixture(t, kind)
				applyBefore, restoreBefore := driver.applyCalls, driver.restoreCalls
				revoked, checks := false, 0
				var pinned GatewayV2LANGrantObservation
				if boundary == "after_clear" {
					driver.afterAttest = func() { revoked = true }
				}
				err := fixture.manager.WithGatewayV2LANCommitRecovery(context.Background(), request,
					func(_ context.Context, observation GatewayV2LANGrantObservation) error {
						checks++
						if checks == 1 {
							pinned = observation
						} else if !reflect.DeepEqual(pinned, observation) {
							t.Fatal("authority recheck fabricated a fresh physical observation")
						}
						if revoked {
							return errors.New("serving authority lost")
						}
						if boundary == "before_publication" && checks == 1 {
							revoked = true
						}
						return nil
					})
				if err == nil || !revoked || checks < 2 {
					t.Fatalf("boundary accepted revoked authority: %v checks=%d", err, checks)
				}
				retained := routeOperationLoad(t, fixture.store)
				if boundary == "before_publication" {
					if !reflect.DeepEqual(retained, pending) {
						t.Fatal("early denial changed pending history")
					}
					wantApply, wantRestore := 0, 2
					if kind == gatewayV2PendingLANWithdrawal {
						wantApply, wantRestore = 2, 0
					}
					if driver.applyCalls-applyBefore != wantApply || driver.restoreCalls-restoreBefore != wantRestore {
						t.Fatal("early denial published a grant or missed withdrawal")
					}
				} else {
					if retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANWithdrawal || retained.Revision != pending.Revision+2 || !retained.Pending.ActivationUncertain || retained.Pending.Proposed.LAN != nil || retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil {
						t.Fatalf("late denial did not append exact withdrawal history: revision=%d pending=%#v", retained.Revision, retained.Pending)
					}
					raw, rawErr := gatewayV2LANBindingForRequest(request)
					if rawErr != nil || !reflect.DeepEqual(retained.Pending.Previous.LAN.Raw, raw) || !reflect.DeepEqual(retained.Apps[request.AppID], *retained.Pending.Previous) {
						t.Fatal("late quarantine rewrote retained raw authority")
					}
					if driver.applyCalls-applyBefore != 2 || driver.restoreCalls-restoreBefore != 1 {
						t.Fatal("late denial did not withdraw exact publication")
					}
				}
				if driver.stopCalls != 0 {
					t.Fatal("proven withdrawal unnecessarily stopped gateway")
				}
			})
		}
	}
}

func TestManagedGatewayCurrentLANCommitRecoveryPhysicalAuthority(t *testing.T) {
	for _, operation := range []string{"apply", "restore"} {
		for _, boundary := range []string{"before_effect", "after_effect", "lost_acknowledgment", "already_exact"} {
			t.Run(operation+"/"+boundary, func(t *testing.T) {
				fixture, transition, _ := managedGatewayCurrentPhysicalTransitionFixture(t)
				physical, revoked := boundary == "already_exact", false
				checks, effects := 0, 0
				outcome := gatewayCurrentPhysicalRecoveryEffective
				if operation == "restore" {
					outcome = gatewayCurrentPhysicalRecoveryBefore
				}
				runtime := &gatewayCurrentPhysicalRuntimeFake{}
				runtime.observeFn = func(_ context.Context, target gatewayCurrentPhysicalTarget) (gatewayCurrentPhysicalAttestation, error) {
					if !physical {
						return gatewayCurrentPhysicalAttestation{}, errors.New("not at requested topology")
					}
					proof := gatewayCurrentPhysicalDriverTestAttestation(t, target, outcome)
					if boundary == "already_exact" {
						revoked = true
					}
					return proof, nil
				}
				runtime.reconcileFn = func(ctx context.Context, _ gatewayCurrentPhysicalTarget, guard func(context.Context) error) error {
					if boundary == "before_effect" {
						revoked = true
					}
					if err := guard(ctx); err != nil {
						return err
					}
					effects++
					physical = true
					revoked = true
					if boundary == "lost_acknowledgment" {
						return errors.New("lost acknowledgment")
					}
					return nil
				}
				authorize := func(context.Context) error {
					checks++
					if revoked {
						return errors.New("serving authority revoked")
					}
					return nil
				}
				driver := managedGatewayCurrentPhysicalDriver{managerGatewayCurrentPhysicalDriver: managerGatewayCurrentPhysicalDriver{manager: fixture.manager}, runtime: runtime}
				var err error
				if operation == "apply" {
					_, err = driver.applyGatewayCurrentPhysicalAuthorized(context.Background(), transition, authorize)
				} else {
					_, err = driver.restoreGatewayCurrentPhysicalAuthorized(context.Background(), transition, authorize)
				}
				if err == nil || checks == 0 {
					t.Fatalf("managed driver accepted revoked authority at %s: checks=%d err=%v", boundary, checks, err)
				}
				if (boundary == "before_effect" || boundary == "already_exact") && effects != 0 {
					t.Fatal("revoked authority allowed an effect")
				}
				if (boundary == "after_effect" || boundary == "lost_acknowledgment") && effects != 1 {
					t.Fatal("effect boundary was not exercised")
				}
			})
		}
	}
}

type gatewayCurrentLANRecoveryWithdrawalFailure struct {
	*gatewayCurrentStateMachineDriver
	failRestore, failStop bool
}

func (d *gatewayCurrentLANRecoveryWithdrawalFailure) restoreGatewayCurrentPhysical(ctx context.Context, transition gatewayCurrentPhysicalTransition) (gatewayCurrentPhysicalAttestation, error) {
	if d.failRestore {
		d.restoreCalls++
		return gatewayCurrentPhysicalAttestation{}, errors.New("withdrawal outcome unknown")
	}
	return d.gatewayCurrentStateMachineDriver.restoreGatewayCurrentPhysical(ctx, transition)
}
func (d *gatewayCurrentLANRecoveryWithdrawalFailure) stopGatewayCurrentPhysical(ctx context.Context, transition gatewayCurrentPhysicalTransition) (gatewayCurrentPhysicalAttestation, error) {
	if d.failStop {
		d.stopCalls++
		return gatewayCurrentPhysicalAttestation{}, errors.New("owned stop outcome unknown")
	}
	return d.gatewayCurrentStateMachineDriver.stopGatewayCurrentPhysical(ctx, transition)
}
func TestGatewayCurrentLANCommitRecoveryStopsUncertainInitialWithdrawal(t *testing.T) {
	for _, kind := range []gatewayV2PendingKind{gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal} {
		for _, stopFails := range []bool{false, true} {
			name := string(kind) + "/stop_proved"
			if stopFails {
				name = string(kind) + "/stop_unproved"
			}
			t.Run(name, func(t *testing.T) {
				fixture, base, request, pending := newGatewayCurrentCommitRecoveryFixture(t, kind)
				driver := &gatewayCurrentLANRecoveryWithdrawalFailure{gatewayCurrentStateMachineDriver: base, failRestore: kind == gatewayV2PendingLANGrant, failStop: stopFails}
				if kind == gatewayV2PendingLANWithdrawal {
					base.failApply = true
				}
				fixture.manager.gatewayCurrentPhysicalDriver = driver
				err := fixture.manager.WithGatewayV2LANCommitRecovery(context.Background(), request, func(context.Context, GatewayV2LANGrantObservation) error {
					t.Fatal("uncertain withdrawal reached serving authorization")
					return nil
				})
				var diagnostic *Error
				if !errors.As(err, &diagnostic) || diagnostic.Code != DiagnosticRouteUnresolved || diagnostic.candidateMayBeLive != stopFails || driver.stopCalls != 1 {
					t.Fatalf("uncertain withdrawal lost exact stop evidence: stops=%d err=%#v", driver.stopCalls, err)
				}
				if !reflect.DeepEqual(routeOperationLoad(t, fixture.store), pending) {
					t.Fatal("uncertain withdrawal rewrote retained recovery history")
				}
			})
		}
	}
}

func TestGatewayCurrentLANCommitRecoveryRejectsUnguardedDriver(t *testing.T) {
	fixture, driver, request, pending := newGatewayCurrentCommitRecoveryFixture(t, gatewayV2PendingLANGrant)
	applyBefore, restoreBefore := driver.applyCalls, driver.restoreCalls
	fixture.manager.gatewayCurrentPhysicalDriver = struct{ gatewayCurrentPhysicalDriver }{driver}
	err := fixture.manager.WithGatewayV2LANCommitRecovery(context.Background(), request, func(context.Context, GatewayV2LANGrantObservation) error { return nil })
	if err == nil || driver.applyCalls != applyBefore || driver.restoreCalls-restoreBefore != 2 || driver.stopCalls != 0 {
		t.Fatalf("unguarded driver published recovery: apply=%d restore=%d stop=%d err=%v", driver.applyCalls-applyBefore, driver.restoreCalls-restoreBefore, driver.stopCalls, err)
	}
	if !reflect.DeepEqual(routeOperationLoad(t, fixture.store), pending) {
		t.Fatal("unguarded driver cleared recovery history")
	}
}
