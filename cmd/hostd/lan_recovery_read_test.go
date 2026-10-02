package main

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	recoveryReadAppID        = "11111111-1111-4111-8111-111111111111"
	recoveryReadOperationID  = "22222222-2222-4222-8222-222222222222"
	recoveryReadRevisionID   = "33333333-3333-4333-8333-333333333333"
	recoveryReadAllocationID = "44444444-4444-4444-8444-444444444444"
	recoveryReadOwnerID      = "55555555-5555-4555-8555-555555555555"
	recoveryReadProfileID    = "66666666-6666-4666-8666-666666666666"
	recoveryReadGatewayID    = "77777777-7777-4777-8777-777777777777"
)

type recoverySnapshotSequence struct {
	snapshots []appaccess.HostingGatewayStartupSnapshot
	err       error
	calls     int
}

func (f *recoverySnapshotSequence) HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
	f.calls++
	if f.err != nil {
		return appaccess.HostingGatewayStartupSnapshot{}, f.err
	}
	index := f.calls - 1
	if index >= len(f.snapshots) {
		index = len(f.snapshots) - 1
	}
	return f.snapshots[index], nil
}

func TestLANRecoveryBatchReadReturnsOnlyFreshStaticHeadWithoutWrites(t *testing.T) {
	snapshot := recoveryGrantSnapshot(t)
	upgrade := generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: recoveryReadGatewayID,
	}
	ingress := &fakeGatewayStartupBatchIngress{
		upgrade: upgrade, headPresent: true,
		head: generatedingress.GatewayV2LANRecoveryHead{
			Kind: controller.RecoveryLANGrant, OperationID: recoveryReadOperationID,
			AppID: recoveryReadAppID, Head: 1, Count: 2,
		},
	}
	repository := &recoverySnapshotSequence{snapshots: []appaccess.HostingGatewayStartupSnapshot{snapshot, snapshot}}
	reader, err := newLANRecoveryHeadReader(repository, ingress, gatewayStartup{
		snapshot: snapshot, inspection: upgrade, recoveryBatch: true, recoveryBatchHead: 1, recoveryBatchCount: 2,
		recoveryKind: controller.RecoveryLANGrant, recoveryID: recoveryReadOperationID,
		recoveryAppID: recoveryReadAppID,
	})
	if err != nil {
		t.Fatal(err)
	}
	head, err := reader.ReadLANRecoveryHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(snapshot.Grants.Claims[0].Allocation))
	if err != nil {
		t.Fatal(err)
	}
	if !head.Batch || head.BatchPosition != 2 || head.BatchCount != 2 ||
		head.Kind != controller.RecoveryLANGrant || head.OperationID != recoveryReadOperationID ||
		head.AppID != recoveryReadAppID || head.ClaimState != string(appaccess.AppAccessGrantPrepared) ||
		head.AccessRevisionID != recoveryReadRevisionID || head.AccessRevisionNumber != 4 ||
		head.AllocationID != recoveryReadAllocationID || head.OwnerOperationID != recoveryReadOwnerID ||
		head.Port != 8104 || head.ApprovalDigest != wantDigest {
		t.Fatalf("unexpected recovery head: %+v", head)
	}
	if repository.calls != 2 || ingress.upgradeCalls != 1 || ingress.observeCalls != 1 ||
		ingress.observedGrants != 1 || ingress.observedDisables != 0 ||
		ingress.quarantineCalls != 0 || ingress.retireCalls != 0 || ingress.accessCalls != 0 {
		t.Fatalf("read path calls: snapshots=%d upgrades=%d observes=%d grants=%d disables=%d quarantine=%d retire=%d access=%d",
			repository.calls, ingress.upgradeCalls, ingress.observeCalls, ingress.observedGrants,
			ingress.observedDisables, ingress.quarantineCalls, ingress.retireCalls, ingress.accessCalls)
	}
}

func TestLANRecoveryBatchReadFailsClosedOnDriftForgeryAndProofFailure(t *testing.T) {
	base := recoveryGrantSnapshot(t)
	upgrade := generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: recoveryReadGatewayID,
	}
	pin := gatewayStartup{
		snapshot: base, inspection: upgrade, recoveryBatch: true, recoveryBatchHead: 0, recoveryBatchCount: 2,
		recoveryKind: controller.RecoveryLANGrant, recoveryID: recoveryReadOperationID,
		recoveryAppID: recoveryReadAppID,
	}
	head := generatedingress.GatewayV2LANRecoveryHead{
		Kind: controller.RecoveryLANGrant, OperationID: recoveryReadOperationID,
		AppID: recoveryReadAppID, Head: 0, Count: 2,
	}
	tests := []struct {
		name       string
		first      appaccess.HostingGatewayStartupSnapshot
		second     appaccess.HostingGatewayStartupSnapshot
		upgrade    generatedingress.GatewayV2StartupInspection
		head       generatedingress.GatewayV2LANRecoveryHead
		present    bool
		observeErr error
	}{
		{name: "advanced head", first: base, second: base, upgrade: upgrade, head: func() generatedingress.GatewayV2LANRecoveryHead { value := head; value.Head = 1; return value }(), present: true},
		{name: "completed head", first: base, second: base, upgrade: upgrade, head: generatedingress.GatewayV2LANRecoveryHead{Head: 2, Count: 2}, present: true},
		{name: "missing batch", first: base, second: base, upgrade: upgrade, head: head},
		{name: "gateway drift", first: base, second: base, upgrade: generatedingress.GatewayV2StartupInspection{Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: recoveryReadGatewayID}, head: head, present: true},
		{name: "gateway lock or proof failure", first: base, second: base, upgrade: upgrade, head: head, present: true, observeErr: errors.New("gateway proof failed")},
		{name: "sqlite drift", first: base, second: changedGrantState(base), upgrade: upgrade, head: head, present: true},
		{name: "forged approval digest", first: forgedGrantDigest(base), second: forgedGrantDigest(base), upgrade: upgrade, head: head, present: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &recoverySnapshotSequence{snapshots: []appaccess.HostingGatewayStartupSnapshot{test.first, test.second}}
			ingress := &fakeGatewayStartupBatchIngress{
				upgrade: test.upgrade, head: test.head, headPresent: test.present, headErr: test.observeErr,
			}
			reader, err := newLANRecoveryHeadReader(repository, ingress, pin)
			if err != nil {
				t.Fatal(err)
			}
			if value, err := reader.ReadLANRecoveryHead(context.Background()); err == nil {
				t.Fatalf("unsafe recovery read succeeded: %+v", value)
			}
			if ingress.quarantineCalls != 0 || ingress.retireCalls != 0 {
				t.Fatalf("read path mutated ingress: quarantine=%d retire=%d", ingress.quarantineCalls, ingress.retireCalls)
			}
		})
	}
}

func TestLANSingularRecoveryReadRepeatsExactGatewayAndFullCensus(t *testing.T) {
	snapshot := recoveryDisableSnapshot(t)
	upgrade := generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: recoveryReadGatewayID,
	}
	access := generatedingress.GatewayV2LANAccessStartupInspection{
		Disposition:  generatedingress.GatewayV2LANStartupRecoveryOnly,
		RecoveryKind: controller.RecoveryLANDisable, OperationID: recoveryReadOperationID,
		AppID: recoveryReadAppID,
	}
	ingress := &fakeGatewayStartupBatchIngress{upgrade: upgrade, access: access}
	repository := &recoverySnapshotSequence{snapshots: []appaccess.HostingGatewayStartupSnapshot{snapshot, snapshot}}
	reader, err := newLANRecoveryHeadReader(repository, ingress, gatewayStartup{
		snapshot: snapshot, inspection: upgrade, accessInspection: access, recoveryKind: controller.RecoveryLANDisable,
		recoveryID: recoveryReadOperationID, recoveryAppID: recoveryReadAppID,
	})
	if err != nil {
		t.Fatal(err)
	}
	head, err := reader.ReadLANRecoveryHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := appaccess.AppAccessDisableSpecDigest(snapshot.Disables.Claims[0].Claim.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if head.Batch || head.BatchPosition != 1 || head.BatchCount != 1 ||
		head.ClaimState != string(appaccess.AppAccessDisablePrepared) || head.ApprovalDigest != wantDigest {
		t.Fatalf("unexpected singular recovery head: %+v", head)
	}
	if repository.calls != 2 || ingress.upgradeCalls != 2 || ingress.accessCalls != 1 ||
		ingress.accessGrants != 0 || ingress.accessDisables != 1 ||
		ingress.observeCalls != 0 || ingress.quarantineCalls != 0 || ingress.retireCalls != 0 {
		t.Fatalf("singular read calls: snapshots=%d upgrades=%d access=%d grants=%d disables=%d observe=%d quarantine=%d retire=%d",
			repository.calls, ingress.upgradeCalls, ingress.accessCalls, ingress.accessGrants,
			ingress.accessDisables, ingress.observeCalls,
			ingress.quarantineCalls, ingress.retireCalls)
	}

	driftIngress := &fakeGatewayStartupBatchIngress{
		upgrades: []generatedingress.GatewayV2StartupInspection{upgrade, {
			Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: recoveryReadGatewayID,
		}},
		access: access,
	}
	driftReader, err := newLANRecoveryHeadReader(
		&recoverySnapshotSequence{snapshots: []appaccess.HostingGatewayStartupSnapshot{snapshot, snapshot}},
		driftIngress, gatewayStartup{inspection: upgrade, accessInspection: access,
			snapshot:     snapshot,
			recoveryKind: controller.RecoveryLANDisable, recoveryID: recoveryReadOperationID, recoveryAppID: recoveryReadAppID})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := driftReader.ReadLANRecoveryHead(context.Background()); err == nil {
		t.Fatalf("singular gateway drift succeeded: %+v", value)
	}

	ambiguousIngress := &fakeGatewayStartupBatchIngress{
		upgrade: upgrade,
		access: generatedingress.GatewayV2LANAccessStartupInspection{
			Disposition: generatedingress.GatewayV2LANStartupRecoveryOnly,
			Recoveries: []generatedingress.GatewayV2LANAccessStartupRecovery{
				{Kind: controller.RecoveryLANDisable, OperationID: recoveryReadOperationID, AppID: recoveryReadAppID},
				{Kind: controller.RecoveryLANGrant, OperationID: recoveryReadOwnerID, AppID: recoveryReadAppID},
			},
		},
	}
	ambiguousReader, err := newLANRecoveryHeadReader(
		&recoverySnapshotSequence{snapshots: []appaccess.HostingGatewayStartupSnapshot{snapshot, snapshot}},
		ambiguousIngress, gatewayStartup{snapshot: snapshot, inspection: upgrade, accessInspection: access,
			recoveryKind: controller.RecoveryLANDisable, recoveryID: recoveryReadOperationID, recoveryAppID: recoveryReadAppID})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := ambiguousReader.ReadLANRecoveryHead(context.Background()); err == nil {
		t.Fatalf("ambiguous singular recovery census succeeded: %+v", value)
	}
}

func recoveryGrantSnapshot(t *testing.T) appaccess.HostingGatewayStartupSnapshot {
	t.Helper()
	allocation := recoveryAllocation()
	digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	return appaccess.HostingGatewayStartupSnapshot{
		Upgrades: recoveryGatewaySnapshot(),
		Grants: appaccess.AppAccessGrantStartupSnapshot{Claims: []appaccess.AppAccessGrantStartupClaim{{
			Claim: appaccess.AppAccessGrantClaim{
				AttemptID: recoveryReadOperationID, State: appaccess.AppAccessGrantPrepared, StateSequence: 1,
				Spec: appaccess.AppAccessGrantSpec{
					AppID: recoveryReadAppID, AllocationID: recoveryReadAllocationID,
					OwnerOperationID: recoveryReadOwnerID, AccessRevisionID: recoveryReadRevisionID,
					AccessRevisionNumber: 4, AccessSpecDigest: digest, Port: 8104,
					GatewayProfileRevisionID: recoveryReadProfileID, GatewayProfileRevisionNumber: 3,
				},
			},
			Allocation: allocation,
		}}},
	}
}

func recoveryDisableSnapshot(t *testing.T) appaccess.HostingGatewayStartupSnapshot {
	t.Helper()
	spec := appaccess.AppAccessDisableSpec{
		AppID: recoveryReadAppID, AllocationID: recoveryReadAllocationID,
		OwnerOperationID: recoveryReadOwnerID, AccessRevisionID: recoveryReadRevisionID,
		AccessRevisionNumber: 4, Port: 8104, GatewayProfileRevisionID: recoveryReadProfileID,
		GatewayProfileRevisionNumber: 3,
	}
	digest, err := appaccess.AppAccessDisableSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return appaccess.HostingGatewayStartupSnapshot{
		Upgrades: recoveryGatewaySnapshot(),
		Disables: appaccess.AppAccessDisableStartupSnapshot{Claims: []appaccess.AppAccessDisableStartupClaim{{
			Claim: appaccess.AppAccessDisableClaim{
				OperationID: recoveryReadOperationID, Spec: spec, SpecDigest: digest,
				State: appaccess.AppAccessDisablePrepared, StateSequence: 1,
			},
			Allocation: recoveryAllocation(),
		}}},
	}
}

func recoveryGatewaySnapshot() appaccess.GatewayUpgradeStartupSnapshot {
	return appaccess.GatewayUpgradeStartupSnapshot{Claims: []appaccess.GatewayUpgradeStartupClaim{{
		Claim: appaccess.GatewayProfileUpgradeClaim{
			OperationID: recoveryReadGatewayID, State: appaccess.GatewayProfileUpgradeCommitted,
		},
	}}}
}

func recoveryAllocation() appaccess.Allocation {
	return appaccess.Allocation{
		ID: recoveryReadAllocationID, AppID: recoveryReadAppID, Port: 8104,
		OwnerOperationID: recoveryReadOwnerID, OwnerRevisionID: recoveryReadRevisionID,
		GatewayProfileRevisionID: recoveryReadProfileID, GatewayProfileRevisionNumber: 3,
		State: appaccess.AllocationReserved,
	}
}

func changedGrantState(snapshot appaccess.HostingGatewayStartupSnapshot) appaccess.HostingGatewayStartupSnapshot {
	result := snapshot
	result.Grants.Claims = append([]appaccess.AppAccessGrantStartupClaim(nil), snapshot.Grants.Claims...)
	result.Grants.Claims[0].Claim.State = appaccess.AppAccessGrantApplying
	result.Grants.Claims[0].Claim.StateSequence = 2
	return result
}

func forgedGrantDigest(snapshot appaccess.HostingGatewayStartupSnapshot) appaccess.HostingGatewayStartupSnapshot {
	result := snapshot
	result.Grants.Claims = append([]appaccess.AppAccessGrantStartupClaim(nil), snapshot.Grants.Claims...)
	result.Grants.Claims[0].Claim.Spec.AccessSpecDigest =
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return result
}
