package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/generatedingress"
)

func rebindStartupMappingFixture() (appaccess.HostingGatewayStartupSnapshot, generatedingress.GatewayV2StartupInspection) {
	source := appaccess.GatewayCurrentAuthorityRef{Kind: appaccess.GatewayRebindSourceGatewayRebind,
		OperationID: "11111111-1111-4111-8111-111111111111", ProfileRevisionID: "22222222-2222-4222-8222-222222222222",
		ProfileRevisionNumber: 2, ProfileSpecDigest: strings.Repeat("a", 64), TerminalReceiptDigest: strings.Repeat("b", 64)}
	profile := appaccess.GatewayProfileRevision{ID: source.ProfileRevisionID, RevisionNumber: source.ProfileRevisionNumber, SpecDigest: source.ProfileSpecDigest}
	event := appaccess.GatewayRebindEvent{OperationID: source.OperationID, Sequence: 3, State: appaccess.GatewayRebindDatabaseCommitted}
	return appaccess.HostingGatewayStartupSnapshot{
		Upgrades: appaccess.GatewayUpgradeStartupSnapshot{CurrentProfile: &profile},
		Rebind: appaccess.GatewayRebindRecoverySnapshot{CurrentSource: &source, CurrentProfile: &profile, CurrentDatabaseCommittedEvent: &event,
			History: []appaccess.GatewayRebindHistoryEntry{{Claim: appaccess.GatewayRebindClaimRecord{SpecVersion: 2,
				V2: &appaccess.GatewayRebindClaimV2{Spec: appaccess.GatewayRebindSpecV2{OperationID: source.OperationID}, State: appaccess.GatewayRebindCommitted}},
				Events: []appaccess.GatewayRebindEvent{event}}}},
	}, generatedingress.GatewayV2StartupInspection{Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: source.OperationID, CurrentGatewaySource: source}
}

func TestRebindStartupMappingRequiresExactCommittedCurrentCensus(t *testing.T) {
	for _, name := range []string{"valid", "missing authority", "changed authority", "wrong recovery identity", "active attempt", "missing commit", "changed commit", "unreleased claim", "duplicate history", "mixed claim version", "profile drift", "native fallback"} {
		t.Run(name, func(t *testing.T) {
			snapshot, inspection := rebindStartupMappingFixture()
			switch name {
			case "missing authority":
				snapshot.Rebind.CurrentSource = nil
			case "changed authority":
				inspection.CurrentGatewaySource.TerminalReceiptDigest = strings.Repeat("c", 64)
			case "wrong recovery identity":
				inspection.OperationID = "33333333-3333-4333-8333-333333333333"
			case "active attempt":
				snapshot.Rebind.Active = &appaccess.GatewayRebindHistoryEntry{}
			case "missing commit":
				snapshot.Rebind.CurrentDatabaseCommittedEvent = nil
			case "changed commit":
				snapshot.Rebind.CurrentDatabaseCommittedEvent.Sequence++
			case "unreleased claim":
				snapshot.Rebind.History[0].Claim.V2.State = appaccess.GatewayRebindDatabaseCommitted
			case "duplicate history":
				snapshot.Rebind.History = append(snapshot.Rebind.History, snapshot.Rebind.History[0])
			case "mixed claim version":
				snapshot.Rebind.History[0].Claim.Legacy = &appaccess.GatewayRebindClaim{}
			case "profile drift":
				copy := *snapshot.Upgrades.CurrentProfile
				copy.Spec.SelectedIPv4 = "192.0.2.5"
				snapshot.Upgrades.CurrentProfile = &copy
			case "native fallback":
				inspection.CurrentGatewaySource = appaccess.GatewayCurrentAuthorityRef{}
				snapshot.Upgrades.Claims = []appaccess.GatewayUpgradeStartupClaim{{Claim: appaccess.GatewayProfileUpgradeClaim{OperationID: inspection.OperationID, State: appaccess.GatewayProfileUpgradeCommitted}}}
			}
			if got := committedGatewayStartupInspection(snapshot, inspection); got != (name == "valid") {
				t.Fatalf("mapping acceptance=%t", got)
			}
		})
	}
}

func TestRebindStartupMappingPreservesRawLANRecoveryIdentity(t *testing.T) {
	snapshot, inspection := rebindStartupMappingFixture()
	inspection.Disposition = generatedingress.GatewayV2StartupRecoveryOnly
	attemptID, appID := "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
	snapshot.Grants.Claims = []appaccess.AppAccessGrantStartupClaim{{Claim: appaccess.AppAccessGrantClaim{AttemptID: attemptID,
		Spec: appaccess.AppAccessGrantSpec{AppID: appID}}}}
	before := snapshot.Grants.Claims[0]
	access := generatedingress.GatewayV2LANAccessStartupInspection{Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly,
		RecoveryKind: controller.RecoveryLANGrant, OperationID: attemptID, AppID: appID}
	kind, operationID, selectedAppID, batch, err := selectLANAccessStartupRecovery(snapshot, inspection, access)
	if err != nil || kind != controller.RecoveryLANGrant || operationID != attemptID || selectedAppID != appID || batch || !reflect.DeepEqual(snapshot.Grants.Claims[0], before) {
		t.Fatalf("recovery identity changed: kind=%s operation=%s app=%s batch=%t error=%v", kind, operationID, selectedAppID, batch, err)
	}
	access.OperationID = inspection.OperationID
	if _, _, _, _, err := selectLANAccessStartupRecovery(snapshot, inspection, access); err == nil {
		t.Fatal("gateway rebind ID was accepted as a raw grant recovery attempt")
	}
	changed := inspection
	changed.CurrentGatewaySource.TerminalReceiptDigest = strings.Repeat("c", 64)
	if gatewayInspectionMatchesQuarantinedLANBatch(inspection, changed) {
		t.Fatal("quarantine accepted changed current authority")
	}
	changed.Disposition = generatedingress.GatewayV2StartupNormalV2
	if gatewayInspectionMatchesRetiredLANBatch(inspection, changed) {
		t.Fatal("retirement accepted changed current authority")
	}
}
