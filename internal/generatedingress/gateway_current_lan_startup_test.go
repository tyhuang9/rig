package generatedingress

import (
	"context"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func gatewayCurrentStartupGrants(t *testing.T, state gatewayCurrentRouteState) []GatewayV2LANStartupClaim {
	t.Helper()
	var claims []GatewayV2LANStartupClaim
	for appID, app := range state.Apps {
		if app.LAN == nil {
			continue
		}
		var chain []appaccess.GatewayRebindAllocationTransfer
		if app.LAN.Transfer != nil {
			chain = []appaccess.GatewayRebindAllocationTransfer{*app.LAN.Transfer}
		}
		projection := gatewayCurrentBindingProjection(t, state, app.LAN, chain)
		claims = append(claims, GatewayV2LANStartupClaim{
			Request: gatewayV2LANGrantRequestForBinding(appID, app.LAN.Raw),
			State:   appaccess.AppAccessGrantCommitted, StateSequence: 4, CurrentBinding: &projection})
	}
	return claims
}

func TestGatewayCurrentLANStartupCensusBindsRawGrantAndRecovery(t *testing.T) {
	f := newGatewayCurrentStateFixture(t)
	run := func(t *testing.T, state gatewayCurrentRouteState, grants []GatewayV2LANStartupClaim,
		disables []GatewayV2LANDisableStartupClaim,
	) (GatewayV2LANAccessStartupInspection, error) {
		t.Helper()
		claims, err := validateGatewayV2LANAccessStartupClaims(grants, disables)
		if err != nil {
			return GatewayV2LANAccessStartupInspection{}, err
		}
		return gatewayCurrentLANStartupCensus(state, claims)
	}
	t.Run("transferred committed grants", func(t *testing.T) {
		got, err := run(t, f.baseline, gatewayCurrentStartupGrants(t, f.baseline), nil)
		if err != nil || got.Disposition != GatewayV2LANStartupNormal {
			t.Fatalf("stable census=%+v error=%v", got, err)
		}
	})
	for _, name := range []string{"missing grant", "missing current binding", "retained live binding", "wrong receipt", "broken chain", "raw profile rewritten"} {
		t.Run(name, func(t *testing.T) {
			grants := gatewayCurrentStartupGrants(t, f.baseline)
			switch name {
			case "missing grant":
				grants = grants[1:]
			case "missing current binding":
				grants[0].CurrentBinding = nil
			case "retained live binding":
				grants[0].RetainedBinding, grants[0].CurrentBinding = grants[0].CurrentBinding, nil
				grants[0].DisableIntentOperationID = grants[0].Request.OwnerOperationID
			case "wrong receipt":
				grants[0].CurrentBinding.TerminalReceiptDigest = grants[0].Request.AccessSpecDigest
			case "broken chain":
				grants[0].CurrentBinding.TransferChain[0].TransferDigest = ""
			case "raw profile rewritten":
				grants[0].Request.GatewayProfileRevisionID = f.baseline.Profile.RevisionID
			}
			got, err := run(t, f.baseline, grants, nil)
			if err == nil || got.Disposition != "" {
				t.Fatalf("unsafe census accepted: %+v %v", got, err)
			}
		})
	}
	t.Run("ordinary route pending preserves LAN authority", func(t *testing.T) {
		appID, app := routeOperationTransferredApp(t, f.baseline)
		transition, err := gatewayCurrentSwitchTransition(f.baseline, routeOperationSwitchRequest(t, appID, app.Route))
		if err != nil {
			t.Fatal(err)
		}
		got, err := run(t, transition.Pending, gatewayCurrentStartupGrants(t, f.baseline), nil)
		if err != nil || got.Disposition != GatewayV2LANStartupNormal {
			t.Fatalf("route pending census=%+v error=%v", got, err)
		}
	})
	for _, phase := range []string{"prepared", "pending", "terminal pending", "terminal live without pending"} {
		t.Run(phase, func(t *testing.T) {
			state := cloneGatewayCurrentRouteState(f.baseline)
			grants := gatewayCurrentStartupGrants(t, state)
			request := disableRequestForGrant(t, grants[0].Request)
			grants[0].DisableIntentOperationID = request.OperationID
			disable := GatewayV2LANDisableStartupClaim{Request: request, State: appaccess.AppAccessDisablePrepared,
				StateSequence: 1, CurrentBinding: grants[0].CurrentBinding}
			if phase == "pending" || phase == "terminal pending" {
				previous := cloneGatewayCurrentAppRoute(state.Apps[request.AppID])
				proposed := cloneGatewayCurrentAppRoute(previous)
				proposed.LAN = nil
				state.Revision++
				state.Pending = &gatewayCurrentPendingRoute{Kind: gatewayV2PendingLANDisable, AppID: request.AppID,
					Previous: &previous, Proposed: proposed, Disable: &request, ActivationUncertain: true}
				state.Digest, _ = gatewayCurrentRouteStateDigest(state)
			}
			if phase == "terminal pending" || phase == "terminal live without pending" {
				disable.State, disable.StateSequence = appaccess.AppAccessDisableCommitted, 3
				disable.RetainedBinding, disable.CurrentBinding = disable.CurrentBinding, nil
				grants[0].RetainedBinding, grants[0].CurrentBinding = grants[0].CurrentBinding, nil
			}
			got, err := run(t, state, grants, []GatewayV2LANDisableStartupClaim{disable})
			if phase == "terminal live without pending" {
				if err == nil || got.Disposition != "" {
					t.Fatal("retained terminal history authorized an unmarked live binding")
				}
			} else if err != nil || got.Disposition != GatewayV2LANStartupRecoveryOnly ||
				got.RecoveryKind != GatewayV2LANRecoveryDisable || got.OperationID != request.OperationID || got.AppID != request.AppID {
				t.Fatalf("disable recovery identity lost: %+v %v", got, err)
			}
		})
	}
	for _, kind := range []gatewayV2PendingKind{gatewayV2PendingLANGrant, gatewayV2PendingLANWithdrawal} {
		t.Run(string(kind), func(t *testing.T) {
			state := cloneGatewayCurrentRouteState(f.baseline)
			appID, original := routeOperationTransferredApp(t, state)
			request := routeOperationNativeGrantRequest(t, state, appID)
			raw, err := gatewayV2LANBindingForRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			previous, proposed := cloneGatewayCurrentAppRoute(original), cloneGatewayCurrentAppRoute(original)
			previous.LAN = nil
			proposed.LAN = &gatewayCurrentLANBinding{Raw: raw}
			phase, sequence := appaccess.AppAccessGrantPrepared, int64(1)
			if kind == gatewayV2PendingLANWithdrawal {
				previous, proposed = proposed, previous
				phase, sequence = appaccess.AppAccessGrantRolledBack, 2
			}
			state.Apps[appID] = previous
			state.Revision++
			state.Pending = &gatewayCurrentPendingRoute{Kind: kind, AppID: appID, Previous: &previous, Proposed: proposed,
				ActivationUncertain: kind == gatewayV2PendingLANWithdrawal}
			state.Digest, _ = gatewayCurrentRouteStateDigest(state)
			grants := gatewayCurrentStartupGrants(t, f.baseline)
			for i := range grants {
				if grants[i].Request.AppID == appID {
					grants[i] = GatewayV2LANStartupClaim{Request: request, State: phase, StateSequence: sequence}
				}
			}
			got, err := run(t, state, grants, nil)
			if err != nil || got.Disposition != GatewayV2LANStartupRecoveryOnly || got.RecoveryKind != GatewayV2LANRecoveryGrant || got.OperationID != request.AttemptID {
				t.Fatalf("native pending grant identity lost: %+v %v", got, err)
			}
		})
	}
}

func TestGatewayCurrentLANStartupPublicReadRechecksAuthority(t *testing.T) {
	f, snapshot, _, physical := gatewayRebindCurrentStartupFixture(t)
	grants := gatewayCurrentStartupGrants(t, f.baseline)
	got, err := f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, nil)
	if err != nil || got.Disposition != GatewayV2LANStartupNormal || physical.calls != 1 {
		t.Fatalf("public rebound census=%+v calls=%d error=%v", got, physical.calls, err)
	}
	grantOnly, err := f.manager.InspectGatewayV2LANStartup(context.Background(), grants)
	if err != nil || grantOnly.Disposition != GatewayV2LANStartupNormal || grantOnly.AttemptID != "" {
		t.Fatalf("grant-only current startup=%+v error=%v", grantOnly, err)
	}
	physical.before = func() {
		changed := snapshot
		changed.RollbackAllowed = true
		f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
			return changed, nil
		})
	}
	got, err = f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, nil)
	if err == nil || got.Disposition != "" {
		t.Fatalf("SQL drift after physical proof permitted startup: %+v %v", got, err)
	}
	f.manager.options.RebindCurrentStateRepository = gatewayCurrentSelectionRepositoryFunc(func(context.Context) (appaccess.GatewayRebindRecoverySnapshot, error) {
		return snapshot, nil
	})
	next := cloneGatewayCurrentRouteState(f.baseline)
	next.Revision++
	next.Digest, _ = gatewayCurrentRouteStateDigest(next)
	physical.before = func() {
		if err := f.store.saveNext(f.baseline, next); err != nil {
			t.Fatal(err)
		}
	}
	got, err = f.manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, nil)
	if err == nil || got.Disposition != "" {
		t.Fatalf("protected state drift after proof permitted startup: %+v %v", got, err)
	}
	retained, err := f.store.load()
	if err != nil || !reflect.DeepEqual(retained, next) || physical.applyCalls != 0 || physical.restoreCalls != 0 || physical.stopCalls != 0 {
		t.Fatal("read-only LAN inspection changed protected or physical state")
	}
}
