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

func TestGatewayV2LANStartupRequiresExactClaimsForEveryCommittedBinding(t *testing.T) {
	manager, _, _, _, first, driver := gatewayV2LANGrantFixture(t)
	authorizeFirst, _ := allowGatewayV2LANGrant(t, manager, first, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), first, authorizeFirst); err != nil {
		t.Fatal(err)
	}
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	driver.events = nil
	claims := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantCommitted, 4),
		gatewayV2LANStartupClaim(second, appaccess.AppAccessGrantCommitted, 4),
	}
	inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), claims)
	if err != nil || inspection != (GatewayV2LANStartupInspection{Disposition: GatewayV2LANStartupNormal}) {
		t.Fatalf("inspection=%#v err=%v events=%v", inspection, err, driver.events)
	}
	if countEvent(driver.events, "prove_all_granted") != 1 || !containsString(driver.events, "prove_committed") {
		t.Fatalf("startup did not prove both app bindings: %v", driver.events)
	}

	if inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), claims[:1]); !IsCode(err, DiagnosticRouteUnresolved) || inspection != (GatewayV2LANStartupInspection{}) {
		t.Fatalf("missing claim inspection=%#v err=%v", inspection, err)
	}
	wrong := append([]GatewayV2LANStartupClaim(nil), claims...)
	wrong[1].Request.ClaimRequestDigest = strings.Repeat("f", 64)
	if inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), wrong); !IsCode(err, DiagnosticRouteUnresolved) || inspection != (GatewayV2LANStartupInspection{}) {
		t.Fatalf("mismatched claim inspection=%#v err=%v", inspection, err)
	}
}

func TestGatewayV2LANStartupWithholdsNormalForDisableOrStaleCurrentFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*GatewayV2LANStartupClaim)
	}{
		{name: "approved disable", mutate: func(claim *GatewayV2LANStartupClaim) {
			claim.DisableIntentOperationID = "20202020-2020-4020-8020-202020202020"
		}},
		{name: "stale current facts", mutate: func(claim *GatewayV2LANStartupClaim) {
			claim.RequiresRecovery = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
			authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
			if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
				t.Fatal(err)
			}
			claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
			test.mutate(&claim)
			inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim})
			want := GatewayV2LANStartupInspection{
				Disposition: GatewayV2LANStartupRecoveryOnly,
				AttemptID:   request.AttemptID,
			}
			if err != nil || inspection != want {
				t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
			}
		})
	}
}

func TestGatewayV2LANStartupKeepsRolledBackHistoryDistinctFromLaterCommit(t *testing.T) {
	manager, _, _, _, current, _ := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, current, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), current, authorize); err != nil {
		t.Fatal(err)
	}
	historical := current
	historical.AttemptID = "21212121-2121-4121-8121-212121212121"
	historical.ClaimRequestDigest = strings.Repeat("3", 64)
	claims := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(historical, appaccess.AppAccessGrantRolledBack, 2),
		gatewayV2LANStartupClaim(current, appaccess.AppAccessGrantCommitted, 4),
	}
	inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), claims)
	if err != nil || inspection.Disposition != GatewayV2LANStartupNormal || inspection.AttemptID != "" {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
}

func TestGatewayV2LANStartupClassifiesClaimWithoutProtectedBindingAsRecoveryOnly(t *testing.T) {
	for _, state := range []struct {
		value    appaccess.AppAccessGrantState
		sequence int64
	}{
		{appaccess.AppAccessGrantPrepared, 1},
		{appaccess.AppAccessGrantApplying, 2},
		{appaccess.AppAccessGrantDBActive, 3},
		{appaccess.AppAccessGrantUncertain, 3},
		{appaccess.AppAccessGrantCommitted, 4},
	} {
		t.Run(string(state.value), func(t *testing.T) {
			manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
			inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{
				gatewayV2LANStartupClaim(request, state.value, state.sequence),
			})
			want := GatewayV2LANStartupInspection{
				Disposition: GatewayV2LANStartupRecoveryOnly,
				AttemptID:   request.AttemptID,
			}
			if err != nil || inspection != want {
				t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
			}
		})
	}
}

func TestGatewayV2LANStartupClassifiesEveryExactPendingTopology(t *testing.T) {
	for _, topology := range []gatewayV2PendingLiveTopology{
		gatewayV2PendingCommittedExact,
		gatewayV2PendingProposedExact,
		gatewayV2PendingReloadOnlyMixed,
		gatewayV2PendingUnknown,
	} {
		t.Run(string(topology), func(t *testing.T) {
			manager, store, committed, journal, request, driver := gatewayV2LANGrantFixture(t)
			binding, _ := gatewayV2LANBindingForRequest(request)
			previous := cloneGatewayV2AppRoute(committed.Apps[request.AppID])
			proposedApp := cloneGatewayV2AppRoute(previous)
			proposedApp.LAN = &binding
			pending := cloneGatewayV2RouteState(committed)
			pending.Pending = &gatewayV2PendingRoute{
				Kind: gatewayV2PendingLANGrant, AppID: request.AppID, Previous: &previous, Proposed: proposedApp,
			}
			if err := store.saveCommittedV2State(pending, journal); err != nil {
				t.Fatal(err)
			}
			uncertain := cloneGatewayV2RouteState(pending)
			uncertain.Pending.ActivationUncertain = true
			if err := store.saveCommittedV2State(uncertain, journal); err != nil {
				t.Fatal(err)
			}
			driver.pendingTopology = topology
			inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{
				gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantUncertain, 3),
			})
			if topology == gatewayV2PendingUnknown {
				if !IsCode(err, DiagnosticRouteUnresolved) || inspection != (GatewayV2LANStartupInspection{}) {
					t.Fatalf("unknown inspection=%#v err=%v", inspection, err)
				}
				return
			}
			want := GatewayV2LANStartupInspection{
				Disposition: GatewayV2LANStartupRecoveryOnly,
				AttemptID:   request.AttemptID,
			}
			if err != nil || inspection != want {
				t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
			}
			retained, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
			if loadErr != nil || !reflect.DeepEqual(retained, uncertain) {
				t.Fatalf("startup classifier mutated pending state=%#v err=%v", retained, loadErr)
			}
		})
	}
}

func TestGatewayV2LANStartupRejectsCompetingRecoveryAttempts(t *testing.T) {
	manager, _, _, _, first, _ := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	claims := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantPrepared, 1),
		gatewayV2LANStartupClaim(second, appaccess.AppAccessGrantPrepared, 1),
	}
	inspection, err := manager.InspectGatewayV2LANStartup(context.Background(), claims)
	if !IsCode(err, DiagnosticRouteUnresolved) || inspection != (GatewayV2LANStartupInspection{}) {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
}

func TestGatewayV2LANStartupQuarantineWithdrawsNonterminalBindingAndPreservesOtherApp(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	authorizeFirst, _ := allowGatewayV2LANGrant(t, manager, first, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), first, authorizeFirst); err != nil {
		t.Fatal(err)
	}
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	other := cloneGatewayV2AppRoute(before.Apps[second.AppID])
	claims := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantDBActive, 3),
		gatewayV2LANStartupClaim(second, appaccess.AppAccessGrantCommitted, 4),
	}
	driver.events = nil
	if err := manager.QuarantineGatewayV2LANStartup(context.Background(), claims); err != nil {
		t.Fatalf("quarantine err=%v events=%v", err, driver.events)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
		retained.Pending.Previous == nil || retained.Pending.Previous.LAN == nil ||
		retained.Pending.Previous.LAN.GrantAttemptID != first.AttemptID ||
		driver.live.Apps[first.AppID].LAN != nil || !reflect.DeepEqual(driver.live.Apps[second.AppID], other) ||
		!reflect.DeepEqual(retained.Apps[second.AppID], other) {
		t.Fatalf("retained=%#v live=%#v other=%#v err=%v", retained, driver.live, other, err)
	}
	if err := manager.QuarantineGatewayV2LANStartup(context.Background(), claims); err != nil {
		t.Fatalf("idempotent quarantine err=%v", err)
	}
}

func TestGatewayV2LANStartupQuarantineWithdrawsCommittedGrantWithDisableIntent(t *testing.T) {
	manager, store, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
	claim.DisableIntentOperationID = "20202020-2020-4020-8020-202020202020"
	if err := manager.QuarantineGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim}); err != nil {
		t.Fatal(err)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANWithdrawal ||
		driver.live.Apps[request.AppID].LAN != nil {
		t.Fatalf("retained=%#v live=%#v err=%v", retained, driver.live, err)
	}
}

func TestGatewayV2LANStartupQuarantineRemainsClassifiableAfterManagerRestart(t *testing.T) {
	manager, _, _, journal, request, driver := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, request, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantDBActive, 3)
	if err := manager.QuarantineGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(manager.runner, manager.options)
	if err != nil {
		t.Fatal(err)
	}
	restarted.gatewayTopologyObserver = func(_ context.Context, _ routeState, observed gatewayV2RouteState,
		_ gatewayMigrationJournal,
	) gatewayObservedTopology {
		if observed.Apps[request.AppID].LAN == nil {
			return gatewayTopologyExactFinalV2
		}
		return gatewayTopologyUnknownOrDrift
	}
	restarted.gatewayV2LANGrantDriver = &fakeGatewayV2LANGrantDriver{
		t: t, manager: restarted, live: cloneGatewayV2RouteState(driver.live),
	}
	_, preparation := upgradeTestPreparation(t)
	upgradeRequest := GatewayV2UpgradeRequest{
		OperationID: preparation.OperationID,
		Profile: GatewayV2ProfileBinding{
			RevisionID: preparation.Profile.RevisionID, RevisionNumber: preparation.Profile.RevisionNumber,
			SpecDigest: preparation.Profile.SpecDigest, SelectedIPv4: preparation.Profile.SelectedIPv4,
			InterfaceID: preparation.Profile.InterfaceID, PortStart: preparation.Profile.PortStart,
			PortEnd: preparation.Profile.PortEnd,
		},
		ApprovedBy: preparation.ApprovedBy, ApprovedActionDigest: preparation.ApprovedActionDigest,
	}
	upgrade, err := restarted.InspectGatewayV2Startup(context.Background(), []GatewayV2StartupClaim{{
		Request: upgradeRequest, State: appaccess.GatewayProfileUpgradeCommitted, StateSequence: 3,
	}})
	if err != nil || upgrade.Disposition != GatewayV2StartupRecoveryOnly || upgrade.OperationID != journal.OperationID {
		t.Fatalf("upgrade inspection=%#v err=%v", upgrade, err)
	}
	grant, err := restarted.InspectGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{claim})
	if err != nil || grant.Disposition != GatewayV2LANStartupRecoveryOnly || grant.AttemptID != request.AttemptID {
		t.Fatalf("grant inspection=%#v err=%v", grant, err)
	}
}

func TestGatewayV2LANStartupQuarantineRejectsMultipleUnresolvedExposuresBeforeMutation(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	authorizeFirst, _ := allowGatewayV2LANGrant(t, manager, first, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), first, authorizeFirst); err != nil {
		t.Fatal(err)
	}
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	driver.events = nil
	err = manager.QuarantineGatewayV2LANStartup(context.Background(), []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantDBActive, 3),
		gatewayV2LANStartupClaim(second, appaccess.AppAccessGrantUncertain, 3),
	})
	if !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("err=%v", err)
	}
	after, _, loadErr := store.loadBoundUpgrade(journal.OperationID)
	if loadErr != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) ||
		containsString(driver.events, "apply:lan-grant-quarantine.json") {
		t.Fatalf("after=%#v live=%#v events=%v err=%v", after, driver.live, driver.events, loadErr)
	}
}

func TestGatewayV2LANStartupFailureStopsOnlyJournalBoundGatewayWithoutReadingRouteState(t *testing.T) {
	manager, store, _, _, _, driver := gatewayV2LANGrantFixture(t)
	manager.gatewayRebindFailStop = &atomic.Bool{}
	manager.options.RebindFenceCheck = func(context.Context) error {
		return errors.New("database unavailable during emergency stop")
	}
	if err := upgradeProtectedWrite(store.v2Path, store.v2Purpose, []byte("{")); err != nil {
		t.Fatal(err)
	}
	if err := manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err != nil ||
		!driver.gatewayStopped || !reflect.DeepEqual(driver.events, []string{"stop_owned_gateway"}) {
		t.Fatalf("err=%v stopped=%t events=%v", err, driver.gatewayStopped, driver.events)
	}

	driver.events = nil
	driver.gatewayStopped = false
	driver.stopOwnedGatewayErr = errors.New("injected Docker stop failure")
	if err := manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); !IsCode(err, DiagnosticRouteUnresolved) || driver.gatewayStopped {
		t.Fatalf("err=%v stopped=%t events=%v", err, driver.gatewayStopped, driver.events)
	}
}

func gatewayV2LANStartupClaim(request GatewayV2LANGrantRequest, state appaccess.AppAccessGrantState,
	sequence int64,
) GatewayV2LANStartupClaim {
	return GatewayV2LANStartupClaim{Request: request, State: state, StateSequence: sequence}
}

func gatewayV2LANSecondAppRequest(t *testing.T, first GatewayV2LANGrantRequest) GatewayV2LANGrantRequest {
	t.Helper()
	request := first
	request.AttemptID = "15151515-1515-4515-8515-151515151515"
	request.ClaimRequestDigest = strings.Repeat("2", 64)
	request.AppID = upgradeTestAppB
	request.AllocationID = "16161616-1616-4616-8616-161616161616"
	request.OwnerOperationID = "17171717-1717-4717-8717-171717171717"
	request.AccessRevisionID = "18181818-1818-4818-8818-181818181818"
	request.AccessRevisionNumber = 4
	request.ApprovedBy = "19191919-1919-4919-8919-191919191919"
	request.Port = 8101
	request.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, request)
	return request
}

func countEvent(events []string, expected string) int {
	count := 0
	for _, event := range events {
		if event == expected {
			count++
		}
	}
	return count
}
