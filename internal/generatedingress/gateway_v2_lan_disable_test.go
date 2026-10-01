package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type fakeGatewayV2LANDisableLease struct {
	t             *testing.T
	manager       *Manager
	request       GatewayV2LANDisableRequest
	revalidations int
	withdrawals   int
	releases      int
	denyWithdraw  bool
}

func (l *fakeGatewayV2LANDisableLease) Revalidate(_ context.Context, request GatewayV2LANDisableRequest) error {
	if !reflect.DeepEqual(request, l.request) || l.manager.mu.TryLock() {
		l.t.Fatal("disable revalidation lacked exact request or gateway lock")
	}
	l.revalidations++
	return nil
}

func (l *fakeGatewayV2LANDisableLease) Withdraw(_ context.Context, request GatewayV2LANDisableRequest) error {
	if !reflect.DeepEqual(request, l.request) || l.manager.mu.TryLock() {
		l.t.Fatal("disable withdrawal lacked exact request or gateway lock")
	}
	l.withdrawals++
	if l.denyWithdraw {
		return errors.New("injected ambiguous DB transition")
	}
	return nil
}

func (l *fakeGatewayV2LANDisableLease) Release() error { l.releases++; return nil }

func permitGatewayV2LANDisableResolution(context.Context, GatewayV2LANDisableRequest) error {
	return nil
}

func disableRequestForGrant(t *testing.T, grant GatewayV2LANGrantRequest) GatewayV2LANDisableRequest {
	t.Helper()
	request := GatewayV2LANDisableRequest{
		OperationID: "15151515-1515-4515-8515-151515151515", RequestDigest: strings.Repeat("2", 64),
		AppID: grant.AppID, AllocationID: grant.AllocationID, OwnerOperationID: grant.OwnerOperationID,
		Port: grant.Port, AccessRevisionID: grant.AccessRevisionID,
		AccessRevisionNumber: grant.AccessRevisionNumber, AccessSpecDigest: grant.AccessSpecDigest,
		ApprovedBy:                   "16161616-1616-4616-8616-161616161616",
		GatewayProfileRevisionID:     grant.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: grant.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     grant.GatewayProfileSpecDigest,
		SourceGrant:                  &grant,
	}
	digest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpec{
		AppID: request.AppID, AllocationID: request.AllocationID,
		OwnerOperationID: request.OwnerOperationID, AccessRevisionID: request.AccessRevisionID,
		AccessRevisionNumber: request.AccessRevisionNumber, Port: request.Port,
		GatewayProfileRevisionID:     request.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: request.GatewayProfileRevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	request.SpecDigest = digest
	return request
}

func grantedLANForDisable(t *testing.T) (*Manager, *gatewayUpgradeStateStore, gatewayMigrationJournal,
	GatewayV2LANGrantRequest, *fakeGatewayV2LANGrantDriver,
) {
	t.Helper()
	manager, store, _, journal, grant, driver := gatewayV2LANGrantFixture(t)
	authorize, _ := allowGatewayV2LANGrant(t, manager, grant, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), grant, authorize); err != nil {
		t.Fatal(err)
	}
	driver.events = nil
	return manager, store, journal, grant, driver
}

func allowGatewayV2LANDisable(t *testing.T, manager *Manager, request GatewayV2LANDisableRequest) (GatewayV2LANDisableAuthorizer, *fakeGatewayV2LANDisableLease) {
	t.Helper()
	lease := &fakeGatewayV2LANDisableLease{t: t, manager: manager, request: request}
	return func(_ context.Context, candidate GatewayV2LANDisableRequest) (GatewayV2LANDisableAuthorizationLease, error) {
		if !reflect.DeepEqual(candidate, request) || manager.mu.TryLock() {
			t.Fatal("disable authorizer lacked exact request or gateway lock")
		}
		return lease, nil
	}, lease
}

func TestGatewayV2LANDisableWithdrawsOnlyExactAppAndResolvesUnderLock(t *testing.T) {
	manager, store, journal, first, driver := grantedLANForDisable(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	driver.events = nil
	request := disableRequestForGrant(t, first)
	authorize, lease := allowGatewayV2LANDisable(t, manager, request)
	result, err := manager.DisableGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || !reflect.DeepEqual(result.Receipt.Request, request) || result.Receipt.ObservedAt.IsZero() ||
		lease.withdrawals != 1 || lease.releases != 1 {
		t.Fatalf("disable=%#v lease=%#v err=%v events=%v", result, lease, err, driver.events)
	}
	pending, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || pending.Pending == nil || pending.Pending.Kind != gatewayV2PendingLANDisable ||
		pending.Apps[first.AppID].LAN == nil || driver.live.Apps[first.AppID].LAN != nil ||
		driver.live.Apps[second.AppID].LAN == nil ||
		!reflect.DeepEqual(driver.live.Apps[first.AppID].Route, pending.Apps[first.AppID].Route) {
		t.Fatalf("pending=%#v live=%#v err=%v", pending.Pending, driver.live.Apps, err)
	}
	observed, err := manager.ObserveGatewayV2LANDisable(context.Background(), request)
	if err != nil || observed.Disposition != GatewayV2LANDisableWithdrawnPending || observed.ObservedAt.IsZero() {
		t.Fatalf("observation=%#v err=%v", observed, err)
	}
	callbackCalls := 0
	err = manager.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(_ context.Context, observed GatewayV2LANDisableObservation) error {
			callbackCalls++
			if manager.mu.TryLock() || observed.Disposition != GatewayV2LANDisableWithdrawnPending ||
				!reflect.DeepEqual(observed.Request, request) || driver.live.Apps[first.AppID].LAN != nil {
				t.Fatal("terminal callback lacked locked exact withdrawal proof")
			}
			return nil
		})
	if err != nil || callbackCalls != 1 {
		t.Fatalf("resolution calls=%d err=%v", callbackCalls, err)
	}
	cleared, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || cleared.Pending != nil || cleared.Apps[first.AppID].LAN != nil ||
		cleared.Apps[second.AppID].LAN == nil || !reflect.DeepEqual(cleared, driver.live) {
		t.Fatalf("cleared=%#v live=%#v err=%v", cleared, driver.live, err)
	}
}

func TestGatewayV2LANDisableFailedResolutionRetains404AndRecoversAfterRestart(t *testing.T) {
	manager, store, journal, grant, driver := grantedLANForDisable(t)
	request := disableRequestForGrant(t, grant)
	authorize, _ := allowGatewayV2LANDisable(t, manager, request)
	if _, err := manager.DisableGatewayV2LAN(context.Background(), request, authorize); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("injected terminal DB failure")
	if err := manager.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("terminal failure=%v", err)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANDisable ||
		driver.live.Apps[grant.AppID].LAN != nil {
		t.Fatalf("retained=%#v live=%#v err=%v", retained.Pending, driver.live.Apps, err)
	}
	restarted, err := New(manager.runner, manager.options)
	if err != nil {
		t.Fatal(err)
	}
	restarted.gatewayV2LANGrantDriver = &fakeGatewayV2LANGrantDriver{t: t, manager: restarted, live: cloneGatewayV2RouteState(driver.live)}
	if err := restarted.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { return nil }); err != nil {
		t.Fatalf("restart resolution=%v", err)
	}
	cleared, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || cleared.Pending != nil || cleared.Apps[grant.AppID].LAN != nil {
		t.Fatalf("restart cleared=%#v err=%v", cleared, err)
	}
}

func TestGatewayV2LANDisableAmbiguousWithdrawalFailureStopsOwnedGateway(t *testing.T) {
	manager, store, journal, grant, driver := grantedLANForDisable(t)
	request := disableRequestForGrant(t, grant)
	authorize, lease := allowGatewayV2LANDisable(t, manager, request)
	lease.denyWithdraw = true
	if _, err := manager.DisableGatewayV2LAN(context.Background(), request, authorize); !IsCode(err, DiagnosticRouteUnresolved) ||
		!driver.gatewayStopped {
		t.Fatalf("failure=%v stopped=%t", err, driver.gatewayStopped)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.Pending != nil {
		t.Fatalf("unexpected protected change=%#v err=%v", retained.Pending, err)
	}
}

func TestGatewayV2LANDisableInterfaceDriftStopsOwnedGatewayBeforeAuthorization(t *testing.T) {
	manager, store, journal, grant, driver := grantedLANForDisable(t)
	request := disableRequestForGrant(t, grant)
	driver.failSelectedPreflightAt = driver.selectedPreflightCalls + 1
	authorize, lease := allowGatewayV2LANDisable(t, manager, request)
	if _, err := manager.DisableGatewayV2LAN(context.Background(), request, authorize); !IsCode(err, DiagnosticRouteUnresolved) ||
		!driver.gatewayStopped || lease.withdrawals != 0 {
		t.Fatalf("interface drift err=%v stopped=%t lease=%#v", err, driver.gatewayStopped, lease)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.Pending != nil || retained.Apps[grant.AppID].LAN == nil {
		t.Fatalf("protected state=%#v err=%v", retained, err)
	}
}

func TestGatewayV2LANDisableNeverGrantedAbsenceWithNoSourceGrant(t *testing.T) {
	manager, store, before, journal, grant, driver := gatewayV2LANGrantFixture(t)
	request := disableRequestForGrant(t, grant)
	request.SourceGrant = nil
	authorize, lease := allowGatewayV2LANDisable(t, manager, request)
	result, err := manager.DisableGatewayV2LAN(context.Background(), request, authorize)
	if err != nil || lease.withdrawals != 1 || lease.releases != 1 || driver.applyCalls != 0 ||
		result.Receipt.ObservedAt.IsZero() {
		t.Fatalf("absence result=%#v lease=%#v events=%v err=%v", result, lease, driver.events, err)
	}
	callbackCalls := 0
	if err := manager.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(_ context.Context, observed GatewayV2LANDisableObservation) error {
			callbackCalls++
			if observed.Disposition != GatewayV2LANDisableDisabled || manager.mu.TryLock() {
				t.Fatal("no-source absence lacked locked 404 proof")
			}
			return nil
		}); err != nil || callbackCalls != 1 {
		t.Fatalf("absence resolution calls=%d err=%v", callbackCalls, err)
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) || driver.applyCalls != 0 {
		t.Fatalf("absence changed topology after=%#v before=%#v err=%v", after, before, err)
	}
}

func TestGatewayV2LANDisableStartupClassifiesAndQuarantinesBeforeWorkers(t *testing.T) {
	manager, store, journal, first, driver := grantedLANForDisable(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	request := disableRequestForGrant(t, first)
	grants := []GatewayV2LANStartupClaim{
		{Request: first, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
			DisableIntentOperationID: request.OperationID},
		{Request: second, State: appaccess.AppAccessGrantCommitted, StateSequence: 4},
	}
	disables := []GatewayV2LANDisableStartupClaim{{Request: request, State: appaccess.AppAccessDisablePrepared, StateSequence: 1}}
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || inspection.Disposition != GatewayV2LANStartupRecoveryOnly ||
		inspection.RecoveryKind != GatewayV2LANRecoveryDisable || inspection.OperationID != request.OperationID || inspection.AppID != first.AppID {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
	if err := manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, disables); err != nil {
		t.Fatal(err)
	}
	retained, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || retained.Pending == nil || retained.Pending.Kind != gatewayV2PendingLANDisable ||
		driver.live.Apps[first.AppID].LAN != nil || driver.live.Apps[second.AppID].LAN == nil {
		t.Fatalf("quarantine state=%#v live=%#v err=%v", retained.Pending, driver.live.Apps, err)
	}
	// Simulate a crash after the SQLite terminal commit and before the
	// protected pending record can be cleared.
	disables[0].State = appaccess.AppAccessDisableCommitted
	disables[0].StateSequence = 3
	inspection, err = manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || inspection.Disposition != GatewayV2LANStartupRecoveryOnly ||
		inspection.RecoveryKind != GatewayV2LANRecoveryDisable {
		t.Fatalf("terminal-before-clear inspection=%#v err=%v", inspection, err)
	}
	if err := manager.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	inspection, err = manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || inspection.Disposition != GatewayV2LANStartupNormal {
		t.Fatalf("terminal startup=%#v err=%v", inspection, err)
	}
}

func TestGatewayV2LANDisableHistoricalCommitAllowsSamePortReenable(t *testing.T) {
	manager, store, journal, oldGrant, driver := grantedLANForDisable(t)
	request := disableRequestForGrant(t, oldGrant)
	authorizeDisable, _ := allowGatewayV2LANDisable(t, manager, request)
	if _, err := manager.DisableGatewayV2LAN(context.Background(), request, authorizeDisable); err != nil {
		t.Fatal(err)
	}
	if err := manager.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	newGrant := oldGrant
	newGrant.AttemptID = "22222222-2222-4222-8222-222222222222"
	newGrant.ClaimRequestDigest = strings.Repeat("3", 64)
	newGrant.AllocationID = "23232323-2323-4323-8323-232323232323"
	newGrant.OwnerOperationID = "24242424-2424-4424-8424-242424242424"
	newGrant.AccessRevisionID = "25252525-2525-4525-8525-252525252525"
	newGrant.AccessRevisionNumber++
	newGrant.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, newGrant)
	authorizeGrant, _ := allowGatewayV2LANGrant(t, manager, newGrant, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), newGrant, authorizeGrant); err != nil {
		t.Fatal(err)
	}
	grants := []GatewayV2LANStartupClaim{
		{Request: oldGrant, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
			DisableIntentOperationID: request.OperationID},
		{Request: newGrant, State: appaccess.AppAccessGrantCommitted, StateSequence: 4},
	}
	disables := []GatewayV2LANDisableStartupClaim{{Request: request, State: appaccess.AppAccessDisableCommitted, StateSequence: 3}}
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || inspection.Disposition != GatewayV2LANStartupNormal {
		t.Fatalf("same-port startup=%#v err=%v", inspection, err)
	}
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || before.Apps[oldGrant.AppID].LAN == nil ||
		before.Apps[oldGrant.AppID].LAN.GrantAttemptID != newGrant.AttemptID {
		t.Fatalf("new grant state=%#v err=%v", before, err)
	}
	called := false
	err = manager.WithGatewayV2LANDisableResolution(context.Background(), request, permitGatewayV2LANDisableResolution,
		func(context.Context, GatewayV2LANDisableObservation) error { called = true; return nil })
	if !IsCode(err, DiagnosticRouteUnresolved) || called {
		t.Fatalf("historical disable replay err=%v called=%t", err, called)
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(driver.live, before) {
		t.Fatalf("historical replay changed new grant: err=%v", err)
	}
}

func TestGatewayV2LANDisableResolutionRequiresAuthorizationBeforeMutation(t *testing.T) {
	manager, store, journal, grant, driver := grantedLANForDisable(t)
	request := disableRequestForGrant(t, grant)
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	beforeApply := driver.applyCalls
	denied := errors.New("administrator session revoked")
	callbackCalled := false
	err = manager.WithGatewayV2LANDisableResolution(context.Background(), request,
		func(context.Context, GatewayV2LANDisableRequest) error { return denied },
		func(context.Context, GatewayV2LANDisableObservation) error { callbackCalled = true; return nil })
	if !errors.Is(err, denied) || callbackCalled || driver.applyCalls != beforeApply {
		t.Fatalf("unauthorized resolution err=%v callback=%t apply=%d", err, callbackCalled, driver.applyCalls)
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) {
		t.Fatalf("unauthorized resolution changed protected or live route: %v", err)
	}
}

func TestGatewayV2LANDisableStartupRejectsCompetingUnresolvedGrant(t *testing.T) {
	manager, store, journal, first, driver := grantedLANForDisable(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	request := disableRequestForGrant(t, first)
	grants := []GatewayV2LANStartupClaim{
		{Request: first, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
			DisableIntentOperationID: request.OperationID},
		{Request: second, State: appaccess.AppAccessGrantDBActive, StateSequence: 3},
	}
	disables := []GatewayV2LANDisableStartupClaim{{Request: request, State: appaccess.AppAccessDisablePrepared, StateSequence: 1}}
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables); err == nil ||
		inspection != (GatewayV2LANAccessStartupInspection{}) {
		t.Fatalf("competing inspection=%#v err=%v", inspection, err)
	}
	if err := manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, disables); err == nil {
		t.Fatal("competing recovery work allowed startup quarantine mutation")
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) {
		t.Fatalf("competing recovery mutated state=%#v live=%#v err=%v", after, driver.live, err)
	}
}

func TestGatewayV2LANDisableStartupRejectsMultiplePendingRoutes(t *testing.T) {
	manager, store, journal, first, driver := grantedLANForDisable(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	firstDisable := disableRequestForGrant(t, first)
	secondDisable := disableRequestForGrant(t, second)
	grants := []GatewayV2LANStartupClaim{
		{Request: first, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
			DisableIntentOperationID: firstDisable.OperationID},
		{Request: second, State: appaccess.AppAccessGrantCommitted, StateSequence: 4,
			DisableIntentOperationID: secondDisable.OperationID},
	}
	disables := []GatewayV2LANDisableStartupClaim{
		{Request: firstDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
		{Request: secondDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
	}
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables); err == nil ||
		inspection != (GatewayV2LANAccessStartupInspection{}) {
		t.Fatalf("multiple-route inspection=%#v err=%v", inspection, err)
	}
	if err := manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, disables); err == nil {
		t.Fatal("single-route quarantine was allowed with two pending disables")
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, before) {
		t.Fatalf("multiple-route rejection mutated gateway state=%#v err=%v", after, err)
	}
}
