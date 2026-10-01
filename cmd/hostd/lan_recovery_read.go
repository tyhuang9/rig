package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/generatedingress"
)

type lanRecoverySnapshotReader interface {
	HostingGatewayStartupSnapshot(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)
}

// lanRecoveryReadIngress is intentionally observation-only. Keeping mutation
// methods out of this boundary makes the recovery read path incapable of
// advancing or retiring a protected batch.
type lanRecoveryReadIngress interface {
	ObserveGatewayV2LANRecoveryHead(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) (generatedingress.GatewayV2LANRecoveryHead, bool, error)
	InspectGatewayV2Startup(context.Context,
		[]generatedingress.GatewayV2StartupClaim) (generatedingress.GatewayV2StartupInspection, error)
	InspectGatewayV2LANAccessStartup(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) (generatedingress.GatewayV2LANAccessStartupInspection, error)
}

type lanRecoveryHeadReader struct {
	repository lanRecoverySnapshotReader
	ingress    lanRecoveryReadIngress
	pin        lanRecoveryReadPin
}

type lanRecoveryReadPin struct {
	snapshot           appaccess.HostingGatewayStartupSnapshot
	inspection         generatedingress.GatewayV2StartupInspection
	accessInspection   generatedingress.GatewayV2LANAccessStartupInspection
	recoveryBatch      bool
	recoveryBatchHead  int
	recoveryBatchCount int
	recoveryKind       string
	recoveryID         string
	recoveryAppID      string
}

func newLANRecoveryHeadReader(repository lanRecoverySnapshotReader, ingress lanRecoveryReadIngress,
	startup gatewayStartup,
) (*lanRecoveryHeadReader, error) {
	pin := lanRecoveryReadPin{
		snapshot: startup.snapshot, inspection: startup.inspection,
		accessInspection: startup.accessInspection, recoveryBatch: startup.recoveryBatch,
		recoveryBatchHead: startup.recoveryBatchHead, recoveryBatchCount: startup.recoveryBatchCount,
		recoveryKind: startup.recoveryKind, recoveryID: startup.recoveryID, recoveryAppID: startup.recoveryAppID,
	}
	if repository == nil || ingress == nil ||
		(pin.recoveryKind != controller.RecoveryLANGrant && pin.recoveryKind != controller.RecoveryLANDisable) ||
		pin.recoveryID == "" || pin.recoveryAppID == "" {
		return nil, errors.New("invalid LAN recovery read dependencies")
	}
	if pin.recoveryBatch {
		if pin.recoveryBatchHead < 0 || pin.recoveryBatchCount <= 0 ||
			pin.recoveryBatchHead >= pin.recoveryBatchCount {
			return nil, errors.New("invalid LAN recovery batch pin")
		}
	} else if pin.recoveryBatchHead != 0 || pin.recoveryBatchCount != 0 {
		return nil, errors.New("invalid singular LAN recovery pin")
	}
	position, count := 1, 1
	if pin.recoveryBatch {
		position, count = pin.recoveryBatchHead+1, pin.recoveryBatchCount
	}
	if _, err := recoveryHeadFromSnapshot(pin.snapshot, pin, position, count); err != nil {
		return nil, fmt.Errorf("invalid LAN recovery claim pin: %w", err)
	}
	return &lanRecoveryHeadReader{repository: repository, ingress: ingress, pin: pin}, nil
}

func (s *lanRecoveryHeadReader) ReadLANRecoveryHead(ctx context.Context) (controller.LANRecoveryHead, error) {
	if s == nil || ctx == nil || s.repository == nil || s.ingress == nil {
		return controller.LANRecoveryHead{}, errors.New("LAN recovery head is unavailable")
	}
	snapshot, err := s.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return controller.LANRecoveryHead{}, fmt.Errorf("read LAN recovery census: %w", err)
	}
	if !reflect.DeepEqual(snapshot, s.pin.snapshot) {
		return controller.LANRecoveryHead{}, errors.New("LAN recovery census changed from startup pin")
	}
	if s.pin.recoveryBatch {
		return s.readBatchHead(ctx, snapshot)
	}
	return s.readSingularHead(ctx, snapshot)
}

func (s *lanRecoveryHeadReader) readBatchHead(ctx context.Context,
	snapshot appaccess.HostingGatewayStartupSnapshot,
) (controller.LANRecoveryHead, error) {
	upgrade, err := s.ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(snapshot.Upgrades))
	if err != nil || !reflect.DeepEqual(upgrade, s.pin.inspection) {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	grants := lanGrantStartupClaims(snapshot.Grants)
	disables := lanDisableStartupClaims(snapshot.Disables)
	head, present, err := s.ingress.ObserveGatewayV2LANRecoveryHead(ctx, grants, disables)
	if err != nil || !present {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	if head.Kind != s.pin.recoveryKind || head.OperationID != s.pin.recoveryID ||
		head.AppID != s.pin.recoveryAppID || head.Head != s.pin.recoveryBatchHead ||
		head.Count != s.pin.recoveryBatchCount {
		return controller.LANRecoveryHead{}, errors.New("protected LAN recovery head changed")
	}
	kind, operationID, appID, completed, err := selectLANRecoveryBatchHead(snapshot, upgrade, head)
	if err != nil || completed || kind != s.pin.recoveryKind ||
		operationID != s.pin.recoveryID || appID != s.pin.recoveryAppID {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	confirmed, err := s.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(confirmed, snapshot) {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	return recoveryHeadFromSnapshot(snapshot, s.pin, head.Head+1, head.Count)
}

func (s *lanRecoveryHeadReader) readSingularHead(ctx context.Context,
	snapshot appaccess.HostingGatewayStartupSnapshot,
) (controller.LANRecoveryHead, error) {
	grants := lanGrantStartupClaims(snapshot.Grants)
	disables := lanDisableStartupClaims(snapshot.Disables)
	upgrade, err := s.ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(snapshot.Upgrades))
	if err != nil || !reflect.DeepEqual(upgrade, s.pin.inspection) {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	access, err := s.ingress.InspectGatewayV2LANAccessStartup(ctx, grants, disables)
	if err != nil || !reflect.DeepEqual(access, s.pin.accessInspection) {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	confirmedUpgrade, err := s.ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(snapshot.Upgrades))
	if err != nil || !reflect.DeepEqual(confirmedUpgrade, upgrade) {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	kind, operationID, appID, batch, err := selectLANAccessStartupRecovery(snapshot, confirmedUpgrade, access)
	if err != nil || batch || kind != s.pin.recoveryKind ||
		operationID != s.pin.recoveryID || appID != s.pin.recoveryAppID {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	confirmed, err := s.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(confirmed, snapshot) {
		return controller.LANRecoveryHead{}, firstGatewayStartupError(err)
	}
	return recoveryHeadFromSnapshot(snapshot, s.pin, 1, 1)
}

func recoveryHeadFromSnapshot(snapshot appaccess.HostingGatewayStartupSnapshot, pin lanRecoveryReadPin,
	position, count int,
) (controller.LANRecoveryHead, error) {
	result := controller.LANRecoveryHead{
		Kind: pin.recoveryKind, AppID: pin.recoveryAppID, OperationID: pin.recoveryID,
		Batch: pin.recoveryBatch, BatchPosition: position, BatchCount: count,
	}
	switch pin.recoveryKind {
	case controller.RecoveryLANGrant:
		for _, entry := range snapshot.Grants.Claims {
			if entry.Claim.AttemptID != pin.recoveryID {
				continue
			}
			approvalDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(entry.Allocation))
			if err != nil || approvalDigest != entry.Claim.Spec.AccessSpecDigest ||
				entry.Claim.Spec.AppID != pin.recoveryAppID {
				return controller.LANRecoveryHead{}, errors.New("LAN grant recovery claim changed")
			}
			result.ClaimState = string(entry.Claim.State)
			result.AccessRevisionID = entry.Claim.Spec.AccessRevisionID
			result.AccessRevisionNumber = entry.Claim.Spec.AccessRevisionNumber
			result.AllocationID = entry.Claim.Spec.AllocationID
			result.OwnerOperationID = entry.Claim.Spec.OwnerOperationID
			result.Port = entry.Claim.Spec.Port
			result.ApprovalDigest = entry.Claim.Spec.AccessSpecDigest
			return result, nil
		}
	case controller.RecoveryLANDisable:
		for _, entry := range snapshot.Disables.Claims {
			if entry.Claim.OperationID != pin.recoveryID {
				continue
			}
			approvalDigest, err := appaccess.AppAccessDisableSpecDigest(entry.Claim.Spec)
			if err != nil || approvalDigest != entry.Claim.SpecDigest ||
				entry.Claim.Spec.AppID != pin.recoveryAppID {
				return controller.LANRecoveryHead{}, errors.New("LAN disable recovery claim changed")
			}
			result.ClaimState = string(entry.Claim.State)
			result.AccessRevisionID = entry.Claim.Spec.AccessRevisionID
			result.AccessRevisionNumber = entry.Claim.Spec.AccessRevisionNumber
			result.AllocationID = entry.Claim.Spec.AllocationID
			result.OwnerOperationID = entry.Claim.Spec.OwnerOperationID
			result.Port = entry.Claim.Spec.Port
			result.ApprovalDigest = entry.Claim.SpecDigest
			return result, nil
		}
	}
	return controller.LANRecoveryHead{}, errors.New("pinned LAN recovery claim is absent")
}
