package generatedingress

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayV2LANAccessStartupClassifiesPreparedDisableQueueDeterministically(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	grantTwoLANForAccessQueue(t, manager, first, second)
	firstDisable := disableRequestForGrant(t, first)
	secondDisable := distinctGatewayV2LANDisableRequest(t, disableRequestForGrant(t, second))
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANAccessClaimWithDisable(first, firstDisable.OperationID),
		gatewayV2LANAccessClaimWithDisable(second, secondDisable.OperationID),
	}
	disables := []GatewayV2LANDisableStartupClaim{
		{Request: firstDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
		{Request: secondDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
	}
	want := GatewayV2LANAccessStartupInspection{
		Disposition: GatewayV2LANStartupRecoveryOnly,
		Recoveries: []GatewayV2LANAccessStartupRecovery{
			{Kind: GatewayV2LANRecoveryDisable, OperationID: firstDisable.OperationID, AppID: first.AppID},
			{Kind: GatewayV2LANRecoveryDisable, OperationID: secondDisable.OperationID, AppID: second.AppID},
		},
	}
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || !reflect.DeepEqual(inspection, want) {
		t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
	}
	// The claims come from database rows and have no ordering guarantee. The
	// recovery queue must remain canonical when the same snapshot is permuted.
	inspection, err = manager.InspectGatewayV2LANAccessStartup(context.Background(),
		[]GatewayV2LANStartupClaim{grants[1], grants[0]},
		[]GatewayV2LANDisableStartupClaim{disables[1], disables[0]},
	)
	if err != nil || !reflect.DeepEqual(inspection, want) {
		t.Fatalf("permuted inspection=%#v err=%v want=%#v", inspection, err, want)
	}
	assertGatewayV2LANAccessQueueQuarantineRemainsFailClosed(t, manager, store, journal, driver, grants, disables)
}

func TestGatewayV2LANAccessStartupClassifiesMixedGrantAndDisableQueue(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
	secondDisable := distinctGatewayV2LANDisableRequest(t, disableRequestForGrant(t, second))
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANStartupClaim(first, appaccess.AppAccessGrantPrepared, 1),
		gatewayV2LANAccessClaimWithDisable(second, secondDisable.OperationID),
	}
	disables := []GatewayV2LANDisableStartupClaim{{
		Request: secondDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1,
	}}
	want := GatewayV2LANAccessStartupInspection{
		Disposition: GatewayV2LANStartupRecoveryOnly,
		Recoveries: []GatewayV2LANAccessStartupRecovery{
			{Kind: GatewayV2LANRecoveryGrant, OperationID: first.AttemptID, AppID: first.AppID},
			{Kind: GatewayV2LANRecoveryDisable, OperationID: secondDisable.OperationID, AppID: second.AppID},
		},
	}
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	if err != nil || !reflect.DeepEqual(inspection, want) {
		t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
	}
	inspection, err = manager.InspectGatewayV2LANAccessStartup(context.Background(),
		[]GatewayV2LANStartupClaim{grants[1], grants[0]}, disables)
	if err != nil || !reflect.DeepEqual(inspection, want) {
		t.Fatalf("permuted inspection=%#v err=%v want=%#v", inspection, err, want)
	}
	assertGatewayV2LANAccessQueueQuarantineRemainsFailClosed(t, manager, store, journal, driver, grants, disables)
}

func TestGatewayV2LANAccessStartupRejectsDuplicateAndForgedQueueIdentities(t *testing.T) {
	t.Run("duplicate grant attempt", func(t *testing.T) {
		manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
		claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantPrepared, 1)
		inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(),
			[]GatewayV2LANStartupClaim{claim, claim}, nil)
		if !IsCode(err, DiagnosticValidationFailed) || !reflect.DeepEqual(inspection, GatewayV2LANAccessStartupInspection{}) {
			t.Fatalf("inspection=%#v err=%v", inspection, err)
		}
	})
	t.Run("forged disable source", func(t *testing.T) {
		manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
		disable := disableRequestForGrant(t, request)
		forgedSource := *disable.SourceGrant
		forgedSource.ClaimRequestDigest = strings.Repeat("f", 64)
		disable.SourceGrant = &forgedSource
		grant := gatewayV2LANAccessClaimWithDisable(request, disable.OperationID)
		inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(),
			[]GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{{
				Request: disable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1,
			}})
		if !IsCode(err, DiagnosticValidationFailed) || !reflect.DeepEqual(inspection, GatewayV2LANAccessStartupInspection{}) {
			t.Fatalf("inspection=%#v err=%v", inspection, err)
		}
	})
	t.Run("ambiguous app and port", func(t *testing.T) {
		manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
		conflict := request
		conflict.AttemptID = "36363636-3636-4636-8636-363636363636"
		conflict.ClaimRequestDigest = strings.Repeat("6", 64)
		conflict.AllocationID = "37373737-3737-4737-8737-373737373737"
		conflict.OwnerOperationID = "38383838-3838-4838-8838-383838383838"
		conflict.AccessRevisionID = "39393939-3939-4939-8939-393939393939"
		conflict.AccessRevisionNumber++
		conflict.ApprovedBy = "40404040-4040-4040-8040-404040404040"
		conflict.AccessSpecDigest = mustGatewayV2LANAccessDigest(t, conflict)
		inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), []GatewayV2LANStartupClaim{
			gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantPrepared, 1),
			gatewayV2LANStartupClaim(conflict, appaccess.AppAccessGrantPrepared, 1),
		}, nil)
		if !IsCode(err, DiagnosticRouteUnresolved) || !reflect.DeepEqual(inspection, GatewayV2LANAccessStartupInspection{}) {
			t.Fatalf("inspection=%#v err=%v", inspection, err)
		}
	})
}

func TestGatewayV2LANAccessStartupClassifiesPendingAndPreparedDisableQueue(t *testing.T) {
	manager, store, _, journal, first, driver := gatewayV2LANGrantFixture(t)
	second := gatewayV2LANSecondAppRequest(t, first)
	grantTwoLANForAccessQueue(t, manager, first, second)
	firstDisable := disableRequestForGrant(t, first)
	secondDisable := distinctGatewayV2LANDisableRequest(t, disableRequestForGrant(t, second))
	authorizeFirst, _ := allowGatewayV2LANDisable(t, manager, firstDisable)
	if _, err := manager.DisableGatewayV2LAN(context.Background(), firstDisable, authorizeFirst); err != nil {
		t.Fatal(err)
	}
	grants := []GatewayV2LANStartupClaim{
		gatewayV2LANAccessClaimWithDisable(first, firstDisable.OperationID),
		gatewayV2LANAccessClaimWithDisable(second, secondDisable.OperationID),
	}
	disables := []GatewayV2LANDisableStartupClaim{
		{Request: firstDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
		{Request: secondDisable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1},
	}
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(), grants, disables)
	want := GatewayV2LANAccessStartupInspection{
		Disposition: GatewayV2LANStartupRecoveryOnly,
		Recoveries: []GatewayV2LANAccessStartupRecovery{
			{Kind: GatewayV2LANRecoveryDisable, OperationID: firstDisable.OperationID, AppID: first.AppID},
			{Kind: GatewayV2LANRecoveryDisable, OperationID: secondDisable.OperationID, AppID: second.AppID},
		},
	}
	if err != nil || !reflect.DeepEqual(inspection, want) {
		t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
	}
	assertGatewayV2LANAccessQueueQuarantineRemainsFailClosed(t, manager, store, journal, driver, grants, disables)
}

func TestGatewayV2LANAccessStartupKeepsSingleRecoveryContract(t *testing.T) {
	manager, _, _, _, request, _ := gatewayV2LANGrantFixture(t)
	disable := disableRequestForGrant(t, request)
	grant := gatewayV2LANAccessClaimWithDisable(request, disable.OperationID)
	inspection, err := manager.InspectGatewayV2LANAccessStartup(context.Background(),
		[]GatewayV2LANStartupClaim{grant}, []GatewayV2LANDisableStartupClaim{{
			Request: disable, State: appaccess.AppAccessDisablePrepared, StateSequence: 1,
		}})
	want := GatewayV2LANAccessStartupInspection{
		Disposition:  GatewayV2LANStartupRecoveryOnly,
		RecoveryKind: GatewayV2LANRecoveryDisable,
		OperationID:  disable.OperationID,
		AppID:        request.AppID,
	}
	if err != nil || !reflect.DeepEqual(inspection, want) {
		t.Fatalf("inspection=%#v err=%v want=%#v", inspection, err, want)
	}
}

func gatewayV2LANAccessClaimWithDisable(request GatewayV2LANGrantRequest, disableOperationID string) GatewayV2LANStartupClaim {
	claim := gatewayV2LANStartupClaim(request, appaccess.AppAccessGrantCommitted, 4)
	claim.DisableIntentOperationID = disableOperationID
	return claim
}

func distinctGatewayV2LANDisableRequest(t *testing.T, request GatewayV2LANDisableRequest) GatewayV2LANDisableRequest {
	t.Helper()
	request.OperationID = "35353535-3535-4535-8535-353535353535"
	request.RequestDigest = strings.Repeat("4", 64)
	if !validGatewayV2LANDisableRequest(request) {
		t.Fatal("distinct disable fixture was invalid")
	}
	return request
}

func grantTwoLANForAccessQueue(t *testing.T, manager *Manager, first, second GatewayV2LANGrantRequest) {
	t.Helper()
	authorizeFirst, _ := allowGatewayV2LANGrant(t, manager, first, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), first, authorizeFirst); err != nil {
		t.Fatal(err)
	}
	authorizeSecond, _ := allowGatewayV2LANGrant(t, manager, second, nil)
	if _, err := manager.GrantGatewayV2LAN(context.Background(), second, authorizeSecond); err != nil {
		t.Fatal(err)
	}
}

func assertGatewayV2LANAccessQueueQuarantineRemainsFailClosed(t *testing.T, manager *Manager,
	store *gatewayUpgradeStateStore, journal gatewayMigrationJournal, driver *fakeGatewayV2LANGrantDriver,
	grants []GatewayV2LANStartupClaim, disables []GatewayV2LANDisableStartupClaim,
) {
	t.Helper()
	before, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	liveBefore := cloneGatewayV2RouteState(driver.live)
	if err := manager.QuarantineGatewayV2LANAccessStartup(context.Background(), grants, disables); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("quarantine err=%v", err)
	}
	after, _, err := store.loadBoundUpgrade(journal.OperationID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(driver.live, liveBefore) {
		t.Fatalf("quarantine mutated protected or live state: after=%#v live=%#v err=%v", after, driver.live, err)
	}
}
