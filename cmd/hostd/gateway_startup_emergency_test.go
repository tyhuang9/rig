package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedingress"
)

func TestGatewayStartupEmergencyReleasesAdmissionBeforeWithdrawal(t *testing.T) {
	for _, releaseFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "released", true: "release acknowledgment lost"}[releaseFails], func(t *testing.T) {
			var order []string
			releaseFailure, stopFailure := errors.New("release failed"), errors.New("stop failed")
			admission := &deploymentEffectsStartupLease{release: func() error {
				order = append(order, "release")
				if releaseFails {
					return releaseFailure
				}
				return nil
			}}
			var observed context.Context
			err := runGatewayStartupEmergencyStop(admission, func(ctx context.Context) error {
				order = append(order, "stop")
				observed = ctx
				deadline, bounded := ctx.Deadline()
				if ctx.Err() != nil || !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
					t.Fatal("emergency withdrawal requires a fresh bounded context")
				}
				return stopFailure
			})
			if !reflect.DeepEqual(order, []string{"release", "stop"}) || admission.pending() ||
				!errors.Is(err, stopFailure) || errors.Is(err, releaseFailure) != releaseFails || observed.Err() == nil {
				t.Fatalf("incorrect emergency ordering, errors or context cleanup: order=%v err=%v", order, err)
			}
			// The existing deferred admission release must not run twice.
			_ = admission.Release()
			if len(order) != 2 {
				t.Fatal("emergency admission was released twice")
			}
		})
	}
	called := false
	if err := runGatewayStartupEmergencyStop(nil, func(ctx context.Context) error {
		called = ctx.Err() == nil
		return nil
	}); err != nil || !called {
		t.Fatal("database failure before admission cannot withdraw owned gateway")
	}
}

func TestGatewayStartupEmergencyDispatchRequiresCompleteProtectedCensus(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     generatedingress.GatewayCurrentStartupEmergencyStopResult
		currentErr error
		native     bool
		wantError  bool
	}{
		{name: "no rebind history", native: true},
		{name: "retained abort only", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{ProtectedRebindHistoryPresent: true}, native: true},
		{name: "exact committed owners stopped", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			ProtectedRebindHistoryPresent: true, RebindOwnershipPresent: true, VerifiedTargets: 2, StoppedOrAbsentTargets: 2}},
		{name: "scan or lease failed", currentErr: errors.New("ownership unavailable"), wantError: true},
		{name: "incomplete", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{Incomplete: true}, wantError: true},
		{name: "indeterminate", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{OwnershipIndeterminate: true}, wantError: true},
		{name: "active attempt", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			ProtectedRebindHistoryPresent: true, UnresolvedProtectedRebindHistory: true}, wantError: true},
		{name: "one stop unproved", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			ProtectedRebindHistoryPresent: true, RebindOwnershipPresent: true, VerifiedTargets: 2, StoppedOrAbsentTargets: 1}, wantError: true},
		{name: "missing owner target", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			ProtectedRebindHistoryPresent: true, RebindOwnershipPresent: true}, wantError: true},
		{name: "missing history evidence", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			RebindOwnershipPresent: true, VerifiedTargets: 1, StoppedOrAbsentTargets: 1}, wantError: true},
		{name: "unassociated target", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			VerifiedTargets: 1, StoppedOrAbsentTargets: 1}, wantError: true},
		{name: "invalid count", result: generatedingress.GatewayCurrentStartupEmergencyStopResult{
			VerifiedTargets: -1, StoppedOrAbsentTargets: -1}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			err := stopOwnedGatewayWithStartupHistory(context.Background(),
				func(context.Context) (generatedingress.GatewayCurrentStartupEmergencyStopResult, error) {
					calls = append(calls, "current")
					return test.result, test.currentErr
				}, func(context.Context) error {
					calls = append(calls, "native")
					return nil
				})
			wantCalls := []string{"current"}
			if test.native {
				wantCalls = append(wantCalls, "native")
			}
			if (err != nil) != test.wantError || !reflect.DeepEqual(calls, wantCalls) ||
				(test.currentErr != nil && !errors.Is(err, test.currentErr)) {
				t.Fatalf("unsafe emergency fallback: calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestGatewayStartupEmergencyDispatchRetainsCancellationAndNativeFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nativeCalls := 0
	current := func(context.Context) (generatedingress.GatewayCurrentStartupEmergencyStopResult, error) {
		cancel()
		return generatedingress.GatewayCurrentStartupEmergencyStopResult{}, nil
	}
	nativeFailure := errors.New("native ownership failed")
	native := func(context.Context) error { nativeCalls++; return nativeFailure }
	if err := stopOwnedGatewayWithStartupHistory(ctx, current, native); err == nil || nativeCalls != 0 {
		t.Fatal("cancelled current census allowed native fallback")
	}
	current = func(context.Context) (generatedingress.GatewayCurrentStartupEmergencyStopResult, error) {
		return generatedingress.GatewayCurrentStartupEmergencyStopResult{}, nil
	}
	if err := stopOwnedGatewayWithStartupHistory(context.Background(), current, native); !errors.Is(err, nativeFailure) || nativeCalls != 1 {
		t.Fatal("native ownership failure reported as successful withdrawal")
	}
}
