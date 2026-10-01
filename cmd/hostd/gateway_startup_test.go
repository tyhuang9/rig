package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
)

type fakeGatewayStartupBatchIngress struct {
	quarantineErr error
	head          generatedingress.GatewayV2LANRecoveryHead
	headPresent   bool
	headErr       error
	retireErr     error
	upgrade       generatedingress.GatewayV2StartupInspection
	upgrades      []generatedingress.GatewayV2StartupInspection
	upgradeErr    error
	access        generatedingress.GatewayV2LANAccessStartupInspection
	accessErr     error
	batchPresent  bool
	batchErr      error

	quarantineCalls int
	observeCalls    int
	retireCalls     int
	upgradeCalls    int
	accessCalls     int
	hasCalls        int
}

func (f *fakeGatewayStartupBatchIngress) HasGatewayV2LANRecoveryBatch(context.Context) (bool, error) {
	f.hasCalls++
	return f.batchPresent, f.batchErr
}

func (f *fakeGatewayStartupBatchIngress) QuarantineGatewayV2LANAccessRecoveryBatch(context.Context,
	[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
) error {
	f.quarantineCalls++
	return f.quarantineErr
}

func (f *fakeGatewayStartupBatchIngress) ObserveGatewayV2LANRecoveryHead(context.Context,
	[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
) (generatedingress.GatewayV2LANRecoveryHead, bool, error) {
	f.observeCalls++
	return f.head, f.headPresent, f.headErr
}

func (f *fakeGatewayStartupBatchIngress) RetireGatewayV2LANRecoveryBatch(context.Context,
	[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
) error {
	f.retireCalls++
	return f.retireErr
}

func (f *fakeGatewayStartupBatchIngress) InspectGatewayV2Startup(context.Context,
	[]generatedingress.GatewayV2StartupClaim,
) (generatedingress.GatewayV2StartupInspection, error) {
	f.upgradeCalls++
	if len(f.upgrades) >= f.upgradeCalls {
		return f.upgrades[f.upgradeCalls-1], f.upgradeErr
	}
	return f.upgrade, f.upgradeErr
}

func (f *fakeGatewayStartupBatchIngress) InspectGatewayV2LANAccessStartup(context.Context,
	[]generatedingress.GatewayV2LANStartupClaim, []generatedingress.GatewayV2LANDisableStartupClaim,
) (generatedingress.GatewayV2LANAccessStartupInspection, error) {
	f.accessCalls++
	return f.access, f.accessErr
}

func batchGatewayStartupSnapshot() appaccess.HostingGatewayStartupSnapshot {
	return appaccess.HostingGatewayStartupSnapshot{
		Upgrades: appaccess.GatewayUpgradeStartupSnapshot{Claims: []appaccess.GatewayUpgradeStartupClaim{{
			Claim: appaccess.GatewayProfileUpgradeClaim{
				OperationID: "gateway-operation", State: appaccess.GatewayProfileUpgradeCommitted,
			},
		}}},
		Grants: appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{{
			Claim: appaccess.AppAccessGrantClaim{
				AttemptID: "grant-operation", Spec: appaccess.AppAccessGrantSpec{AppID: "grant-app"},
			},
		}}},
		Disables: appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{
			{Claim: appaccess.AppAccessDisableClaim{
				OperationID: "disable-one", Spec: appaccess.AppAccessDisableSpec{AppID: "disable-app-one"},
			}},
			{Claim: appaccess.AppAccessDisableClaim{
				OperationID: "disable-two", Spec: appaccess.AppAccessDisableSpec{AppID: "disable-app-two"},
			}},
		}},
	}
}

func batchGatewayStartupInspection() generatedingress.GatewayV2StartupInspection {
	return generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: "gateway-operation",
	}
}

func quarantinedBatchGatewayStartupInspection() generatedingress.GatewayV2StartupInspection {
	return generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: "gateway-operation",
	}
}

func TestLANGrantStartupRecoveryRequiresExactCommittedGatewayAndAttempt(t *testing.T) {
	const gatewayID = "gateway-operation"
	const attemptID = "grant-attempt"
	const appID = "application"
	base := appaccess.HostingGatewayStartupSnapshot{
		Upgrades: appaccess.GatewayUpgradeStartupSnapshot{Claims: []appaccess.GatewayUpgradeStartupClaim{{
			Claim: appaccess.GatewayProfileUpgradeClaim{OperationID: gatewayID, State: appaccess.GatewayProfileUpgradeCommitted},
		}}},
		Grants: appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{{
			Claim: appaccess.AppAccessGrantClaim{AttemptID: attemptID, Spec: appaccess.AppAccessGrantSpec{AppID: appID}},
		}}},
	}
	grantRecovery := generatedingress.GatewayV2LANStartupInspection{
		Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly, AttemptID: attemptID,
	}
	for _, gatewayDisposition := range []generatedingress.GatewayV2StartupDisposition{
		generatedingress.GatewayV2StartupNormalV2, generatedingress.GatewayV2StartupRecoveryOnly,
	} {
		kind, operationID, selectedAppID, err := selectLANStartupRecovery(base,
			generatedingress.GatewayV2StartupInspection{Disposition: gatewayDisposition, OperationID: gatewayID}, grantRecovery)
		if err != nil || kind != controller.RecoveryLANGrant || operationID != attemptID || selectedAppID != appID {
			t.Fatalf("disposition %q: kind=%q operation=%q app=%q err=%v", gatewayDisposition, kind, operationID, selectedAppID, err)
		}
	}
	blocked := []struct {
		name     string
		snapshot appaccess.HostingGatewayStartupSnapshot
		gateway  generatedingress.GatewayV2StartupInspection
		grant    generatedingress.GatewayV2LANStartupInspection
	}{
		{"missing attempt", base, generatedingress.GatewayV2StartupInspection{Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: gatewayID}, generatedingress.GatewayV2LANStartupInspection{Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly, AttemptID: "other"}},
		{"wrong gateway", base, generatedingress.GatewayV2StartupInspection{Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: "other"}, grantRecovery},
		{"recovery mismatch", base, generatedingress.GatewayV2StartupInspection{Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: gatewayID}, generatedingress.GatewayV2LANStartupInspection{Disposition: generatedingress.GatewayV2LANStartupNormal}},
	}
	for _, test := range blocked {
		t.Run(test.name, func(t *testing.T) {
			if kind, operationID, selectedAppID, err := selectLANStartupRecovery(test.snapshot, test.gateway, test.grant); err == nil {
				t.Fatalf("accepted inconsistent startup: kind=%q operation=%q app=%q", kind, operationID, selectedAppID)
			}
		})
	}
	base.Upgrades.Claims[0].Claim.State = appaccess.GatewayProfileUpgradePrepared
	if _, _, _, err := selectLANStartupRecovery(base,
		generatedingress.GatewayV2StartupInspection{Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: gatewayID}, grantRecovery); err == nil {
		t.Fatal("accepted LAN grant recovery before the gateway upgrade committed")
	}
}

func TestLANDisableStartupRecoveryRequiresExactCommittedGatewayAndOperation(t *testing.T) {
	const gatewayID = "gateway-operation"
	const operationID = "disable-operation"
	const appID = "application"
	base := appaccess.HostingGatewayStartupSnapshot{
		Upgrades: appaccess.GatewayUpgradeStartupSnapshot{Claims: []appaccess.GatewayUpgradeStartupClaim{{
			Claim: appaccess.GatewayProfileUpgradeClaim{OperationID: gatewayID, State: appaccess.GatewayProfileUpgradeCommitted},
		}}},
		Disables: appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{{
			Claim: appaccess.AppAccessDisableClaim{OperationID: operationID, Spec: appaccess.AppAccessDisableSpec{AppID: appID}},
		}}},
	}
	upgrade := generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: gatewayID,
	}
	disable := generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition:  generatedingress.GatewayV2LANStartupRecoveryOnly,
		RecoveryKind: controller.RecoveryLANDisable, OperationID: operationID, AppID: appID,
	}
	if kind, gotOperation, gotApp, batch, err := selectLANAccessStartupRecovery(base, upgrade, disable); err != nil || batch ||
		kind != controller.RecoveryLANDisable || gotOperation != operationID || gotApp != appID {
		t.Fatalf("exact disable selection kind=%q operation=%q app=%q batch=%t err=%v", kind, gotOperation, gotApp, batch, err)
	}
	for _, test := range []struct {
		name      string
		operation string
		app       string
		gatewayOK bool
	}{
		{name: "different operation", operation: "other", app: appID, gatewayOK: true},
		{name: "different app", operation: operationID, app: "other", gatewayOK: true},
		{name: "uncommitted gateway", operation: operationID, app: appID},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := disable
			candidate.OperationID, candidate.AppID = test.operation, test.app
			snapshot := base
			if !test.gatewayOK {
				snapshot.Upgrades.Claims[0].Claim.State = appaccess.GatewayProfileUpgradePrepared
			}
			if _, _, _, _, err := selectLANAccessStartupRecovery(snapshot, upgrade, candidate); err == nil {
				t.Fatal("accepted mismatched LAN disable startup recovery")
			}
		})
	}
}

func TestLANAccessStartupMultiRecoverySelectsBatchWithoutGuessingHead(t *testing.T) {
	snapshot := batchGatewayStartupSnapshot()
	upgrade := batchGatewayStartupInspection()
	tests := []struct {
		name       string
		recoveries []generatedingress.GatewayV2LANAccessStartupRecovery
	}{
		{
			name: "two disables",
			recoveries: []generatedingress.GatewayV2LANAccessStartupRecovery{
				{Kind: controller.RecoveryLANDisable, OperationID: "disable-one", AppID: "disable-app-one"},
				{Kind: controller.RecoveryLANDisable, OperationID: "disable-two", AppID: "disable-app-two"},
			},
		},
		{
			name: "mixed grant and disable",
			recoveries: []generatedingress.GatewayV2LANAccessStartupRecovery{
				{Kind: controller.RecoveryLANGrant, OperationID: "grant-operation", AppID: "grant-app"},
				{Kind: controller.RecoveryLANDisable, OperationID: "disable-two", AppID: "disable-app-two"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access := generatedingress.GatewayV2LANAccessStartupInspection{
				Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly,
				Recoveries:  test.recoveries,
			}
			kind, operationID, appID, batch, err := selectLANAccessStartupRecovery(snapshot, upgrade, access)
			if err != nil || !batch || kind != "" || operationID != "" || appID != "" {
				t.Fatalf("selection kind=%q operation=%q app=%q batch=%t err=%v", kind, operationID, appID, batch, err)
			}
		})
	}
}

func TestLANRecoveryBatchRejectsUnknownAndInvalidHeadKinds(t *testing.T) {
	snapshot := batchGatewayStartupSnapshot()
	upgrade := batchGatewayStartupInspection()
	access := generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly,
		Recoveries: []generatedingress.GatewayV2LANAccessStartupRecovery{
			{Kind: controller.RecoveryLANDisable, OperationID: "disable-one", AppID: "disable-app-one"},
			{Kind: "unknown", OperationID: "disable-two", AppID: "disable-app-two"},
		},
	}
	if _, _, _, _, err := selectLANAccessStartupRecovery(snapshot, upgrade, access); err == nil {
		t.Fatal("accepted an unknown recovery kind in the startup census")
	}
	for _, head := range []generatedingress.GatewayV2LANRecoveryHead{
		{Kind: "unknown", OperationID: "disable-one", AppID: "disable-app-one", Head: 0, Count: 2},
		{Kind: controller.RecoveryLANDisable, OperationID: "missing", AppID: "disable-app-one", Head: 0, Count: 2},
		{Head: 3, Count: 2},
		{Kind: controller.RecoveryLANDisable, OperationID: "disable-two", AppID: "disable-app-two", Head: 2, Count: 2},
	} {
		if _, _, _, _, err := selectLANRecoveryBatchHead(snapshot, upgrade, head); err == nil {
			t.Fatalf("accepted invalid protected head: %+v", head)
		}
	}
}

func TestLANRecoveryBatchPinsOnlyObservedHead(t *testing.T) {
	snapshot := batchGatewayStartupSnapshot()
	upgrade := batchGatewayStartupInspection()
	ingress := &fakeGatewayStartupBatchIngress{
		headPresent: true,
		head: generatedingress.GatewayV2LANRecoveryHead{
			Kind: controller.RecoveryLANDisable, OperationID: "disable-two", AppID: "disable-app-two",
			Head: 1, Count: 2,
		},
		upgrade: quarantinedBatchGatewayStartupInspection(),
	}
	gate := gatewayStartup{snapshot: snapshot, inspection: upgrade, recoveryBatch: true}
	got, err := quarantineLANRecoveryBatchStartup(context.Background(), gate, ingress,
		func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) { return snapshot, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !got.recoveryBatch || got.recoveryKind != controller.RecoveryLANDisable ||
		got.recoveryID != "disable-two" || got.recoveryAppID != "disable-app-two" {
		t.Fatalf("pinned gate: %+v", got)
	}
	if ingress.quarantineCalls != 1 || ingress.observeCalls != 1 || ingress.retireCalls != 0 || ingress.accessCalls != 0 {
		t.Fatalf("calls quarantine=%d observe=%d retire=%d access=%d", ingress.quarantineCalls,
			ingress.observeCalls, ingress.retireCalls, ingress.accessCalls)
	}
}

func TestLANRecoveryBatchChangedSnapshotStopsBeforeHeadDispatch(t *testing.T) {
	snapshot := batchGatewayStartupSnapshot()
	changed := batchGatewayStartupSnapshot()
	changed.Upgrades.Claims[0].Claim.State = appaccess.GatewayProfileUpgradePrepared
	ingress := &fakeGatewayStartupBatchIngress{}
	gate := gatewayStartup{snapshot: snapshot, inspection: batchGatewayStartupInspection(), recoveryBatch: true}
	if _, err := quarantineLANRecoveryBatchStartup(context.Background(), gate, ingress,
		func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) { return changed, nil }); err == nil {
		t.Fatal("accepted a changed SQLite startup snapshot")
	}
	if ingress.quarantineCalls != 1 || ingress.upgradeCalls != 0 || ingress.observeCalls != 0 || ingress.retireCalls != 0 {
		t.Fatalf("continued after snapshot drift: quarantine=%d upgrade=%d observe=%d retire=%d",
			ingress.quarantineCalls, ingress.upgradeCalls, ingress.observeCalls, ingress.retireCalls)
	}
}

func TestLANRecoveryBatchFinalHeadRetiresBeforeNormalStartup(t *testing.T) {
	snapshot := batchGatewayStartupSnapshot()
	upgrade := batchGatewayStartupInspection()
	ingress := &fakeGatewayStartupBatchIngress{
		headPresent: true,
		head:        generatedingress.GatewayV2LANRecoveryHead{Head: 2, Count: 2},
		upgrades: []generatedingress.GatewayV2StartupInspection{
			quarantinedBatchGatewayStartupInspection(),
			upgrade,
		},
		access: generatedingress.GatewayV2LANAccessStartupInspection{
			Disposition: generatedingress.GatewayV2LANStartupNormal,
		},
	}
	reads := 0
	gate := gatewayStartup{snapshot: snapshot, inspection: upgrade, recoveryBatch: true}
	got, err := quarantineLANRecoveryBatchStartup(context.Background(), gate, ingress,
		func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
			reads++
			return snapshot, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if got.recoveryBatch || got.recoveryKind != "" || got.recoveryID != "" || got.recoveryAppID != "" {
		t.Fatalf("completed batch retained recovery gate: %+v", got)
	}
	if reads != 2 || ingress.quarantineCalls != 1 || ingress.observeCalls != 1 || ingress.retireCalls != 1 ||
		ingress.upgradeCalls != 2 || ingress.accessCalls != 1 || ingress.hasCalls != 1 {
		t.Fatalf("reads=%d quarantine=%d observe=%d retire=%d upgrades=%d access=%d has=%d", reads,
			ingress.quarantineCalls, ingress.observeCalls, ingress.retireCalls, ingress.upgradeCalls,
			ingress.accessCalls, ingress.hasCalls)
	}
}

func TestLANRecoveryBatchQuarantineFailureStopsBeforeRecoveryDispatch(t *testing.T) {
	snapshot := batchGatewayStartupSnapshot()
	quarantineErr := errors.New("quarantine failed")
	ingress := &fakeGatewayStartupBatchIngress{quarantineErr: quarantineErr}
	gate := gatewayStartup{snapshot: snapshot, inspection: batchGatewayStartupInspection(), recoveryBatch: true}
	snapshotRead := false
	if _, err := quarantineLANRecoveryBatchStartup(context.Background(), gate, ingress,
		func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
			snapshotRead = true
			return snapshot, nil
		}); !errors.Is(err, quarantineErr) {
		t.Fatalf("error=%v", err)
	}
	if snapshotRead || ingress.observeCalls != 0 || ingress.upgradeCalls != 0 || ingress.retireCalls != 0 {
		t.Fatalf("recovery dispatch continued after quarantine failure: snapshot=%t observe=%d upgrade=%d retire=%d",
			snapshotRead, ingress.observeCalls, ingress.upgradeCalls, ingress.retireCalls)
	}
}

func TestHistoricalLANDisableSuccessorSelectionPrefersReusedPort(t *testing.T) {
	resolvedAt := time.Now().UTC().Add(-time.Minute)
	disable := appaccess.AppAccessDisableStartupClaim{Claim: appaccess.AppAccessDisableClaim{
		OperationID: "old-disable", State: appaccess.AppAccessDisableCommitted,
		Spec: appaccess.AppAccessDisableSpec{AppID: "old-app", AllocationID: "old-allocation",
			OwnerOperationID: "old-owner", AccessRevisionID: "old-revision", AccessRevisionNumber: 1,
			GatewayProfileRevisionID: "profile", GatewayProfileRevisionNumber: 1, Port: 8100},
		Proof: &appaccess.AppAccessDisableProof{ObservedAt: resolvedAt},
	}}
	grant := func(attemptID, appID, allocationID string, port uint16) appaccess.AppAccessGrantStartupClaim {
		return appaccess.AppAccessGrantStartupClaim{
			Claim: appaccess.AppAccessGrantClaim{AttemptID: attemptID,
				State: appaccess.AppAccessGrantCommitted, ApprovedAt: resolvedAt.Add(time.Second), CreatedAt: resolvedAt.Add(time.Second),
				Spec: appaccess.AppAccessGrantSpec{AppID: appID, AllocationID: allocationID,
					OwnerOperationID: attemptID, AccessRevisionID: attemptID + "-revision", AccessRevisionNumber: 2,
					GatewayProfileRevisionID: "profile", GatewayProfileRevisionNumber: 1, Port: port}},
			Allocation:        appaccess.Allocation{State: appaccess.AllocationActive},
			AccessHeadCurrent: true, ProfileHeadCurrent: true, ApproverIsAdministrator: true,
		}
	}
	snapshot := appaccess.HostingGatewayStartupSnapshot{
		Disables: appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{disable}},
		Grants: appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{
			grant("new-app-grant", "old-app", "new-allocation", 8101),
			grant("reused-port-grant", "other-app", "other-allocation", 8100),
		}},
	}
	index, successor, relation, found := nextHistoricalLANDisableSuccessor(snapshot)
	if !found || index != 0 || successor.Claim.AttemptID != "reused-port-grant" ||
		relation != generatedingress.GatewayV2LANSuccessorSamePort {
		t.Fatalf("reused-port successor: index=%d grant=%q relation=%q found=%t", index, successor.Claim.AttemptID, relation, found)
	}
	snapshot.Grants.Claims[1].Claim.RetiredAt = &resolvedAt
	_, successor, relation, found = nextHistoricalLANDisableSuccessor(snapshot)
	if !found || successor.Claim.AttemptID != "new-app-grant" ||
		relation != generatedingress.GatewayV2LANSuccessorSameAppNewPort404 {
		t.Fatalf("same-app new-port successor: grant=%q relation=%q found=%t", successor.Claim.AttemptID, relation, found)
	}
	snapshot.Grants.Claims[0].AccessHeadCurrent = false
	if _, _, _, found := nextHistoricalLANDisableSuccessor(snapshot); found {
		t.Fatal("stale successor selected")
	}
	snapshot.Grants.Claims[0].AccessHeadCurrent = true
	snapshot.Grants.Claims[1].Claim.RetiredAt = nil
	snapshot.Grants.Claims[1].Claim.ApprovedAt = resolvedAt.Add(-time.Second)
	_, successor, relation, found = nextHistoricalLANDisableSuccessor(snapshot)
	if !found || successor.Claim.AttemptID != "reused-port-grant" ||
		relation != generatedingress.GatewayV2LANSuccessorSamePort {
		t.Fatal("later grant for another app rejected because its access approval predates the disable")
	}
}

func TestProtectedGatewayUpgradeHistoryDetector(t *testing.T) {
	root := t.TempDir()
	if present, err := hasProtectedGatewayUpgradeHistory(root); err != nil || present {
		t.Fatalf("empty DataRoot: present=%t error=%v", present, err)
	}
	stateDirectory := filepath.Join(root, "runtime", "generated-ingress")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDirectory, "routes.bundle"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if present, err := hasProtectedGatewayUpgradeHistory(root); err != nil || present {
		t.Fatalf("v1-only DataRoot: present=%t error=%v", present, err)
	}
	for _, name := range []string{"routes-v2.bundle", "gateway-v1-to-v2-abort.bundle", "routes-v2.g00001.bundle"} {
		path := filepath.Join(stateDirectory, name)
		if err := os.WriteFile(path, []byte("v2"), 0o600); err != nil {
			t.Fatal(err)
		}
		if present, err := hasProtectedGatewayUpgradeHistory(root); err != nil || !present {
			t.Fatalf("%s: present=%t error=%v", name, present, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDisabledGeneratedRuntimeRejectsProtectedUpgradeArtifact(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stateDirectory := filepath.Join(root, "runtime", "generated-ingress")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDirectory, "gateway-v1-to-v2.bundle"), []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = inspectGatewayStartup(context.Background(), config.Config{DataRoot: root}, db, "", docker.ControllerDirectories{})
	if err == nil {
		t.Fatal("disabled generated runtime accepted protected upgrade history")
	}
}

func TestOccupiedControllerListenerStopsBeforeRuntimeSetup(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := t.TempDir()
	if code := runServer([]string{"-listen", listener.Addr().String(), "-data-root", root}); code != 1 {
		t.Fatalf("occupied listener exit=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime", "generated-build", "controller-working")); !os.IsNotExist(err) {
		t.Fatalf("runtime setup began before listener reservation: %v", err)
	}
}
