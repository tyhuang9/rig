package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayStartup struct {
	ingress            *generatedingress.Manager
	snapshot           appaccess.HostingGatewayStartupSnapshot
	inspection         generatedingress.GatewayV2StartupInspection
	grantInspection    generatedingress.GatewayV2LANStartupInspection
	accessInspection   generatedingress.GatewayV2LANAccessStartupInspection
	recoveryBatch      bool
	recoveryBatchHead  int
	recoveryBatchCount int
	recoveryKind       string
	recoveryID         string
	recoveryAppID      string
}

type gatewayStartupBatchIngress interface {
	HasGatewayV2LANRecoveryBatch(context.Context) (bool, error)
	QuarantineGatewayV2LANAccessRecoveryBatch(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) error
	ObserveGatewayV2LANRecoveryHead(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) (generatedingress.GatewayV2LANRecoveryHead, bool, error)
	RetireGatewayV2LANRecoveryBatch(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) error
	InspectGatewayV2Startup(context.Context, []generatedingress.GatewayV2StartupClaim) (generatedingress.GatewayV2StartupInspection, error)
	InspectGatewayV2LANAccessStartup(context.Context, []generatedingress.GatewayV2LANStartupClaim,
		[]generatedingress.GatewayV2LANDisableStartupClaim) (generatedingress.GatewayV2LANAccessStartupInspection, error)
}

type gatewayStartupSnapshotReader func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error)

func inspectGatewayStartup(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories,
) (gatewayStartup, error) {
	if ctx == nil || db == nil {
		return gatewayStartup{}, errors.New("gateway startup dependencies are required")
	}
	return inspectGatewayStartupWithFence(ctx, cfg, db, dockerExecutable, directories, rebindFenceCheck(db))
}

func inspectGatewayStartupWithFence(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories, fenceCheck func(context.Context) error,
) (gatewayStartup, error) {
	if fenceCheck == nil {
		return gatewayStartup{}, errors.New("gateway rebind fence check is required")
	}
	if err := fenceCheck(ctx); err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect gateway rebind fence: %w", err)
	}
	repository := appaccess.New(db)
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("read hosting gateway startup snapshot: %w", err)
	}
	if !cfg.GeneratedRuntime {
		history, err := hasProtectedGatewayUpgradeHistory(cfg.DataRoot)
		if err != nil {
			return gatewayStartup{}, fmt.Errorf("inspect protected gateway history: %w", err)
		}
		if len(snapshot.Upgrades.Claims) != 0 || len(snapshot.Grants.Claims) != 0 || len(snapshot.Disables.Claims) != 0 || history {
			return gatewayStartup{}, errors.New("generated runtime is required to reconcile existing gateway and LAN grant history")
		}
		return gatewayStartup{snapshot: snapshot}, nil
	}
	ingress, err := newGatewayStartupIngress(cfg, dockerExecutable, directories, fenceCheck)
	if err != nil {
		return gatewayStartup{}, err
	}
	inspection, err := ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(snapshot.Upgrades))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect gateway upgrade startup: %w", err)
	}
	result := gatewayStartup{ingress: ingress, snapshot: snapshot, inspection: inspection}
	if inspection.Disposition == generatedingress.GatewayV2StartupNormalV1 {
		if len(snapshot.Grants.Claims) != 0 || len(snapshot.Disables.Claims) != 0 {
			return gatewayStartup{}, errors.New("LAN grant history exists without a committed v2 gateway")
		}
		return result, nil
	}
	if inspection.Disposition == generatedingress.GatewayV2StartupRecoveryOnly &&
		!committedGatewayStartupOperation(snapshot.Upgrades, inspection.OperationID) {
		if len(snapshot.Grants.Claims) != 0 || len(snapshot.Disables.Claims) != 0 {
			return gatewayStartup{}, errors.New("LAN grant history exists while gateway upgrade requires recovery")
		}
		result.recoveryKind = controller.RecoveryGatewayUpgrade
		result.recoveryID = inspection.OperationID
		return result, nil
	}
	if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 &&
		inspection.Disposition != generatedingress.GatewayV2StartupRecoveryOnly {
		return gatewayStartup{}, errors.New("unknown gateway startup disposition")
	}
	batchPresent, err := ingress.HasGatewayV2LANRecoveryBatch(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect protected LAN recovery batch: %w", err)
	}
	if batchPresent {
		result.recoveryBatch = true
		return result, nil
	}
	snapshot, err = attestHistoricalLANDisableSuccessors(ctx, repository, ingress, snapshot)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("attest historical LAN disable successor: %w", err)
	}
	result.snapshot = snapshot
	accessInspection, err := ingress.InspectGatewayV2LANAccessStartup(ctx,
		lanGrantStartupClaims(snapshot.Grants), lanDisableStartupClaims(snapshot.Disables))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect LAN access startup: %w", err)
	}
	result.accessInspection = accessInspection
	result.recoveryKind, result.recoveryID, result.recoveryAppID, result.recoveryBatch, err =
		selectLANAccessStartupRecovery(snapshot, inspection, accessInspection)
	if err != nil {
		return gatewayStartup{}, err
	}
	return result, nil
}

func newGatewayStartupIngress(cfg config.Config, dockerExecutable string,
	directories docker.ControllerDirectories, fenceCheck func(context.Context) error,
) (*generatedingress.Manager, error) {
	ingress, err := generatedingress.New(runtimeprocess.ExecRunner{}, generatedingress.Options{
		DockerExecutable: dockerExecutable, DockerEndpoint: cfg.DockerEndpoint,
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		DataRoot: cfg.DataRoot, RebindFenceCheck: fenceCheck,
	})
	if err != nil {
		return nil, fmt.Errorf("create gateway startup inspector: %w", err)
	}
	return ingress, nil
}

// stopOwnedGatewayOnStartupFailure is the emergency fallback when SQLite or
// protected ingress state cannot be reconciled. It never chooses a container
// by name alone: generated ingress must prove its purpose-bound journal and
// the exact running container ownership before stopping it.
func stopOwnedGatewayOnStartupFailure(ctx context.Context, cfg config.Config, dockerExecutable string,
	directories docker.ControllerDirectories,
) error {
	return generatedingress.StopOwnedGatewayV2OnStartupFailure(ctx, runtimeprocess.ExecRunner{}, generatedingress.Options{
		DockerExecutable: dockerExecutable, DockerEndpoint: cfg.DockerEndpoint,
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		DataRoot: cfg.DataRoot,
	})
}

// quarantineLANRecoveryStartup withdraws any observed unfinished LAN grant or
// disable before the recovery-only controller serves its listener. It then
// repeats both cross-store inspections against the same immutable census.
// A failed withdrawal or changed snapshot prevents controller startup.
func quarantineLANRecoveryStartup(ctx context.Context, db *sql.DB, gate gatewayStartup) (gatewayStartup, error) {
	if ctx == nil || db == nil || gate.ingress == nil {
		return gatewayStartup{}, errors.New("invalid LAN access recovery startup")
	}
	if gate.recoveryBatch {
		if gate.recoveryKind != "" || gate.recoveryID != "" || gate.recoveryAppID != "" {
			return gatewayStartup{}, errors.New("invalid LAN recovery batch startup")
		}
		return quarantineLANRecoveryBatchStartup(ctx, gate, gate.ingress,
			appaccess.New(db).HostingGatewayStartupSnapshot)
	}
	if (gate.recoveryKind != controller.RecoveryLANGrant && gate.recoveryKind != controller.RecoveryLANDisable) ||
		gate.recoveryID == "" || gate.recoveryAppID == "" {
		return gatewayStartup{}, errors.New("invalid LAN access recovery startup")
	}
	grants := lanGrantStartupClaims(gate.snapshot.Grants)
	disables := lanDisableStartupClaims(gate.snapshot.Disables)
	if err := gate.ingress.QuarantineGatewayV2LANAccessStartup(ctx, grants, disables); err != nil {
		return gatewayStartup{}, fmt.Errorf("quarantine LAN access before recovery listener: %w", err)
	}
	confirmedSnapshot, err := appaccess.New(db).HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("LAN grant startup claim snapshot changed during quarantine: %w", err)
	}
	if !reflect.DeepEqual(confirmedSnapshot, gate.snapshot) {
		return gatewayStartup{}, errors.New("LAN grant startup claim snapshot changed during quarantine")
	}
	upgradeInspection, err := gate.ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(confirmedSnapshot.Upgrades))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("reinspect gateway after LAN quarantine: %w", err)
	}
	accessInspection, err := gate.ingress.InspectGatewayV2LANAccessStartup(ctx, grants, disables)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("reinspect LAN access after quarantine: %w", err)
	}
	kind, operationID, appID, recoveryBatch, err := selectLANAccessStartupRecovery(
		confirmedSnapshot, upgradeInspection, accessInspection)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("LAN grant quarantine changed pinned recovery identity: %w", err)
	}
	if recoveryBatch || kind != gate.recoveryKind || operationID != gate.recoveryID || appID != gate.recoveryAppID {
		return gatewayStartup{}, errors.New("LAN grant quarantine changed pinned recovery identity")
	}
	gate.inspection = upgradeInspection
	gate.accessInspection = accessInspection
	return gate, nil
}

// quarantineLANRecoveryBatchStartup installs or resumes the immutable batch,
// then pins only the protected head. A completed batch is retired only after
// the ingress implementation has repeated its terminal and cross-store proof.
func quarantineLANRecoveryBatchStartup(ctx context.Context, gate gatewayStartup,
	ingress gatewayStartupBatchIngress, readSnapshot gatewayStartupSnapshotReader,
) (gatewayStartup, error) {
	if ctx == nil || ingress == nil || readSnapshot == nil || !gate.recoveryBatch ||
		gate.recoveryKind != "" || gate.recoveryID != "" || gate.recoveryAppID != "" {
		return gatewayStartup{}, errors.New("invalid LAN recovery batch startup")
	}
	grants := lanGrantStartupClaims(gate.snapshot.Grants)
	disables := lanDisableStartupClaims(gate.snapshot.Disables)
	if err := ingress.QuarantineGatewayV2LANAccessRecoveryBatch(ctx, grants, disables); err != nil {
		return gatewayStartup{}, fmt.Errorf("quarantine LAN recovery batch before recovery listener: %w", err)
	}
	confirmedSnapshot, err := readSnapshot(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("LAN recovery batch startup claim snapshot changed during quarantine: %w", err)
	}
	if !reflect.DeepEqual(confirmedSnapshot, gate.snapshot) {
		return gatewayStartup{}, errors.New("LAN recovery batch startup claim snapshot changed during quarantine")
	}
	upgradeInspection, err := ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(confirmedSnapshot.Upgrades))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("reinspect gateway after LAN recovery batch quarantine: %w", err)
	}
	if !gatewayInspectionMatchesQuarantinedLANBatch(gate.inspection, upgradeInspection) {
		return gatewayStartup{}, errors.New("gateway startup inspection did not enter the LAN recovery batch fence")
	}
	head, present, err := ingress.ObserveGatewayV2LANRecoveryHead(ctx, grants, disables)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("observe protected LAN recovery batch head: %w", err)
	}
	if !present {
		return gatewayStartup{}, errors.New("protected LAN recovery batch disappeared during quarantine")
	}
	kind, operationID, appID, completed, err := selectLANRecoveryBatchHead(confirmedSnapshot, upgradeInspection, head)
	if err != nil {
		return gatewayStartup{}, err
	}
	if !completed {
		gate.inspection = upgradeInspection
		gate.recoveryBatchHead = head.Head
		gate.recoveryBatchCount = head.Count
		gate.recoveryKind = kind
		gate.recoveryID = operationID
		gate.recoveryAppID = appID
		return gate, nil
	}
	if err := ingress.RetireGatewayV2LANRecoveryBatch(ctx, grants, disables); err != nil {
		return gatewayStartup{}, fmt.Errorf("retire completed LAN recovery batch: %w", err)
	}
	retiredSnapshot, err := readSnapshot(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("LAN recovery batch startup claim snapshot changed during retirement: %w", err)
	}
	if !reflect.DeepEqual(retiredSnapshot, confirmedSnapshot) {
		return gatewayStartup{}, errors.New("LAN recovery batch startup claim snapshot changed during retirement")
	}
	retiredUpgradeInspection, err := ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(retiredSnapshot.Upgrades))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("reinspect gateway after LAN recovery batch retirement: %w", err)
	}
	if !gatewayInspectionMatchesRetiredLANBatch(upgradeInspection, retiredUpgradeInspection) {
		return gatewayStartup{}, errors.New("gateway startup inspection did not leave the LAN recovery batch fence")
	}
	retiredGrants := lanGrantStartupClaims(retiredSnapshot.Grants)
	retiredDisables := lanDisableStartupClaims(retiredSnapshot.Disables)
	retiredAccessInspection, err := ingress.InspectGatewayV2LANAccessStartup(ctx, retiredGrants, retiredDisables)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("reinspect LAN access after recovery batch retirement: %w", err)
	}
	kind, operationID, appID, recoveryBatch, err := selectLANAccessStartupRecovery(
		retiredSnapshot, retiredUpgradeInspection, retiredAccessInspection)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("completed LAN recovery batch did not return to normal startup: %w", err)
	}
	if kind != "" || operationID != "" || appID != "" || recoveryBatch {
		return gatewayStartup{}, errors.New("completed LAN recovery batch did not return to normal startup")
	}
	batchPresent, err := ingress.HasGatewayV2LANRecoveryBatch(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("reinspect protected LAN recovery batch retirement: %w", err)
	}
	if batchPresent {
		return gatewayStartup{}, errors.New("completed LAN recovery batch remains installed after retirement")
	}
	gate.snapshot = retiredSnapshot
	gate.inspection = retiredUpgradeInspection
	gate.accessInspection = retiredAccessInspection
	gate.recoveryBatch = false
	return gate, nil
}

func selectLANAccessStartupRecovery(snapshot appaccess.HostingGatewayStartupSnapshot,
	inspection generatedingress.GatewayV2StartupInspection,
	access generatedingress.GatewayV2LANAccessStartupInspection,
) (kind, operationID, appID string, recoveryBatch bool, err error) {
	if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 &&
		inspection.Disposition != generatedingress.GatewayV2StartupRecoveryOnly {
		return "", "", "", false, errors.New("LAN access startup requires a v2 gateway")
	}
	if !committedGatewayStartupOperation(snapshot.Upgrades, inspection.OperationID) {
		return "", "", "", false, errors.New("LAN access startup requires a committed v2 gateway")
	}
	switch access.Disposition {
	case generatedingress.GatewayV2LANStartupNormal:
		if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 ||
			access.RecoveryKind != "" || access.OperationID != "" || access.AppID != "" || len(access.Recoveries) != 0 {
			return "", "", "", false, errors.New("gateway and LAN access startup inspections disagree")
		}
		return "", "", "", false, nil
	case generatedingress.GatewayV2LANStartupRecoveryOnly:
		if len(access.Recoveries) != 0 {
			if len(access.Recoveries) < 2 || access.RecoveryKind != "" || access.OperationID != "" || access.AppID != "" {
				return "", "", "", false, errors.New("invalid LAN access recovery census")
			}
			seen := make(map[string]struct{}, len(access.Recoveries))
			for _, recovery := range access.Recoveries {
				if err := validateLANAccessStartupRecoveryIdentity(snapshot, recovery.Kind,
					recovery.OperationID, recovery.AppID); err != nil {
					return "", "", "", false, err
				}
				key := recovery.Kind + "\x00" + recovery.OperationID
				if _, duplicate := seen[key]; duplicate {
					return "", "", "", false, errors.New("duplicate LAN access recovery census identity")
				}
				seen[key] = struct{}{}
			}
			return "", "", "", true, nil
		}
		if err := validateLANAccessStartupRecoveryIdentity(snapshot, access.RecoveryKind,
			access.OperationID, access.AppID); err != nil {
			return "", "", "", false, err
		}
		return access.RecoveryKind, access.OperationID, access.AppID, false, nil
	default:
		return "", "", "", false, errors.New("unknown LAN access startup disposition")
	}
}

func selectLANRecoveryBatchHead(snapshot appaccess.HostingGatewayStartupSnapshot,
	inspection generatedingress.GatewayV2StartupInspection,
	head generatedingress.GatewayV2LANRecoveryHead,
) (kind, operationID, appID string, completed bool, err error) {
	if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 &&
		inspection.Disposition != generatedingress.GatewayV2StartupRecoveryOnly {
		return "", "", "", false, errors.New("LAN recovery batch requires a v2 gateway")
	}
	if !committedGatewayStartupOperation(snapshot.Upgrades, inspection.OperationID) {
		return "", "", "", false, errors.New("LAN recovery batch requires a committed v2 gateway")
	}
	if head.Count <= 0 || head.Head < 0 || head.Head > head.Count {
		return "", "", "", false, errors.New("invalid protected LAN recovery batch head")
	}
	if head.Head == head.Count {
		if head.Kind != "" || head.OperationID != "" || head.AppID != "" {
			return "", "", "", false, errors.New("completed protected LAN recovery batch has an operation identity")
		}
		return "", "", "", true, nil
	}
	if err := validateLANAccessStartupRecoveryIdentity(snapshot, head.Kind, head.OperationID, head.AppID); err != nil {
		return "", "", "", false, err
	}
	return head.Kind, head.OperationID, head.AppID, false, nil
}

func gatewayInspectionMatchesQuarantinedLANBatch(initial, quarantined generatedingress.GatewayV2StartupInspection) bool {
	return (initial.Disposition == generatedingress.GatewayV2StartupNormalV2 ||
		initial.Disposition == generatedingress.GatewayV2StartupRecoveryOnly) &&
		initial.OperationID != "" && quarantined.Disposition == generatedingress.GatewayV2StartupRecoveryOnly &&
		quarantined.OperationID == initial.OperationID
}

func gatewayInspectionMatchesRetiredLANBatch(quarantined, retired generatedingress.GatewayV2StartupInspection) bool {
	return quarantined.Disposition == generatedingress.GatewayV2StartupRecoveryOnly &&
		quarantined.OperationID != "" && retired.Disposition == generatedingress.GatewayV2StartupNormalV2 &&
		retired.OperationID == quarantined.OperationID
}

func validateLANAccessStartupRecoveryIdentity(snapshot appaccess.HostingGatewayStartupSnapshot,
	kind, operationID, appID string,
) error {
	switch kind {
	case controller.RecoveryLANGrant:
		selectedAppID, ok := grantStartupAppID(snapshot.Grants, operationID)
		if !ok || selectedAppID != appID {
			return errors.New("LAN grant recovery identity is not in the startup snapshot")
		}
	case controller.RecoveryLANDisable:
		selectedAppID, ok := disableStartupAppID(snapshot.Disables, operationID)
		if !ok || selectedAppID != appID {
			return errors.New("LAN disable recovery identity is not in the startup snapshot")
		}
	default:
		return errors.New("unknown LAN access recovery kind")
	}
	return nil
}

func selectLANStartupRecovery(snapshot appaccess.HostingGatewayStartupSnapshot,
	inspection generatedingress.GatewayV2StartupInspection,
	grantInspection generatedingress.GatewayV2LANStartupInspection,
) (kind, operationID, appID string, err error) {
	if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 &&
		inspection.Disposition != generatedingress.GatewayV2StartupRecoveryOnly {
		return "", "", "", errors.New("LAN grant startup requires a v2 gateway")
	}
	switch grantInspection.Disposition {
	case generatedingress.GatewayV2LANStartupNormal:
		if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 || grantInspection.AttemptID != "" {
			return "", "", "", errors.New("gateway and LAN grant startup inspections disagree")
		}
	case generatedingress.GatewayV2LANStartupRecoveryOnly:
		if !committedGatewayStartupOperation(snapshot.Upgrades, inspection.OperationID) {
			return "", "", "", errors.New("LAN grant recovery requires a committed v2 gateway")
		}
		selectedAppID, ok := grantStartupAppID(snapshot.Grants, grantInspection.AttemptID)
		if !ok {
			return "", "", "", errors.New("LAN grant recovery attempt is not in the startup snapshot")
		}
		return controller.RecoveryLANGrant, grantInspection.AttemptID, selectedAppID, nil
	default:
		return "", "", "", errors.New("unknown LAN grant startup disposition")
	}
	return "", "", "", nil
}

func committedGatewayStartupOperation(snapshot appaccess.GatewayUpgradeStartupSnapshot, operationID string) bool {
	if operationID == "" {
		return false
	}
	for _, entry := range snapshot.Claims {
		if entry.Claim.OperationID == operationID {
			return entry.Claim.State == appaccess.GatewayProfileUpgradeCommitted
		}
	}
	return false
}

func grantStartupAppID(snapshot appaccess.AppAccessGrantStartupSnapshot, attemptID string) (string, bool) {
	if attemptID == "" {
		return "", false
	}
	for _, entry := range snapshot.Claims {
		if entry.Claim.AttemptID == attemptID {
			return entry.Claim.Spec.AppID, true
		}
	}
	return "", false
}

func disableStartupAppID(snapshot appaccess.AppAccessDisableStartupSnapshot, operationID string) (string, bool) {
	if operationID == "" {
		return "", false
	}
	for _, entry := range snapshot.Claims {
		if entry.Claim.OperationID == operationID {
			return entry.Claim.Spec.AppID, true
		}
	}
	return "", false
}

func lanDisableStartupClaims(snapshot appaccess.AppAccessDisableStartupSnapshot) []generatedingress.GatewayV2LANDisableStartupClaim {
	claims := make([]generatedingress.GatewayV2LANDisableStartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		request := generatedingress.GatewayV2LANDisableRequest{
			OperationID: entry.Claim.OperationID, RequestDigest: entry.Claim.RequestDigest,
			SpecDigest: entry.Claim.SpecDigest, AppID: entry.Claim.Spec.AppID,
			AllocationID: entry.Claim.Spec.AllocationID, OwnerOperationID: entry.Claim.Spec.OwnerOperationID,
			Port: entry.Claim.Spec.Port, AccessRevisionID: entry.Claim.Spec.AccessRevisionID,
			AccessRevisionNumber: entry.Claim.Spec.AccessRevisionNumber,
			AccessSpecDigest:     entry.Revision.SpecDigest, ApprovedBy: entry.Claim.ApprovedBy,
			GatewayProfileRevisionID:     entry.Claim.Spec.GatewayProfileRevisionID,
			GatewayProfileRevisionNumber: entry.Claim.Spec.GatewayProfileRevisionNumber,
			GatewayProfileSpecDigest:     entry.Profile.SpecDigest,
		}
		if entry.SourceGrant != nil {
			grant := gatewayLANGrantRequest(*entry.SourceGrant)
			request.SourceGrant = &grant
		}
		claims = append(claims, generatedingress.GatewayV2LANDisableStartupClaim{
			Request: request, State: entry.Claim.State, StateSequence: entry.Claim.StateSequence,
			CurrentBinding: gatewayLANStartupBinding(entry.EffectiveProfile, entry.CurrentGatewaySource,
				entry.TransferChain, entry.TransferChainTipDigest, entry.TerminalReceiptDigest),
			RetainedBinding: gatewayLANStartupBinding(entry.RetainedEffectiveProfile, entry.RetainedGatewaySource,
				entry.RetainedTransferChain, entry.RetainedTransferChainTipDigest, entry.RetainedTerminalReceiptDigest),
			ClearAcknowledged: entry.ProtectedClearAck != nil || entry.SuccessorAck != nil,
			RequiresRecovery: entry.Claim.State != appaccess.AppAccessDisableCommitted &&
				(entry.AppArchived || !entry.AccessHeadCurrent || !entry.ProfileHeadCurrent ||
					!entry.ApproverIsAdministrator),
		})
	}
	return claims
}

func lanGrantStartupClaims(snapshot appaccess.AppAccessGrantStartupSnapshot) []generatedingress.GatewayV2LANStartupClaim {
	claims := make([]generatedingress.GatewayV2LANStartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		claim := generatedingress.GatewayV2LANStartupClaim{
			Request:       gatewayLANGrantRequest(entry.Claim),
			State:         entry.Claim.State,
			StateSequence: entry.Claim.StateSequence,
			CurrentBinding: gatewayLANStartupBinding(entry.EffectiveProfile, entry.CurrentGatewaySource,
				entry.TransferChain, entry.TransferChainTipDigest, entry.TerminalReceiptDigest),
			RetainedBinding: gatewayLANStartupBinding(entry.RetainedEffectiveProfile, entry.RetainedGatewaySource,
				entry.RetainedTransferChain, entry.RetainedTransferChainTipDigest, entry.RetainedTerminalReceiptDigest),
			RequiresRecovery: entry.AppArchived || !entry.AccessHeadCurrent ||
				!entry.ProfileHeadCurrent || !entry.ApproverIsAdministrator,
		}
		if entry.DisableIntent != nil {
			claim.DisableIntentOperationID = entry.DisableIntent.OperationID
		}
		claims = append(claims, claim)
	}
	return claims
}

func gatewayLANStartupBinding(profile appaccess.GatewayProfileRevision, source appaccess.GatewayCurrentAuthorityRef,
	chain []appaccess.GatewayRebindAllocationTransfer, tip, receipt string,
) *generatedingress.GatewayV2LANStartupBindingProjection {
	// A raw profile on a precommit or legacy claim is not effective authority.
	// Preserve partial authority evidence so runtime validation can reject it;
	// never replace a missing source with the raw request's profile.
	if source == (appaccess.GatewayCurrentAuthorityRef{}) && len(chain) == 0 && tip == "" && receipt == "" {
		return nil
	}
	chainCopy := append([]appaccess.GatewayRebindAllocationTransfer(nil), chain...)
	for index := range chainCopy {
		if previous := chainCopy[index].PredecessorTransferDigest; previous != nil {
			digest := *previous
			chainCopy[index].PredecessorTransferDigest = &digest
		}
	}
	return &generatedingress.GatewayV2LANStartupBindingProjection{
		EffectiveProfile: generatedingress.GatewayV2ProfileBinding{
			RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
			SelectedIPv4: profile.Spec.SelectedIPv4, InterfaceID: profile.Spec.InterfaceID,
			PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd,
		},
		GatewaySource: source, TransferChain: chainCopy,
		TransferChainTipDigest: tip, TerminalReceiptDigest: receipt,
	}
}

func gatewayLANGrantRequest(claim appaccess.AppAccessGrantClaim) generatedingress.GatewayV2LANGrantRequest {
	return generatedingress.GatewayV2LANGrantRequest{
		AttemptID: claim.AttemptID, ClaimRequestDigest: claim.RequestDigest,
		AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID, Port: claim.Spec.Port,
		AccessRevisionID:             claim.Spec.AccessRevisionID,
		AccessRevisionNumber:         claim.Spec.AccessRevisionNumber,
		AccessSpecDigest:             claim.Spec.AccessSpecDigest,
		ApprovedBy:                   claim.Spec.ApprovedBy,
		GatewayProfileRevisionID:     claim.Spec.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: claim.Spec.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     claim.Spec.GatewayProfileSpecDigest,
	}
}

func hasProtectedGatewayUpgradeHistory(dataRoot string) (bool, error) {
	root := filepath.Join(dataRoot, "runtime", "generated-ingress")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("generated ingress state directory is unsafe")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "routes-v2") || strings.HasPrefix(entry.Name(), "gateway-v1-to-v2") {
			return true, nil
		}
	}
	return false, nil
}

func gatewayStartupClaims(snapshot appaccess.GatewayUpgradeStartupSnapshot) []generatedingress.GatewayV2StartupClaim {
	claims := make([]generatedingress.GatewayV2StartupClaim, 0, len(snapshot.Claims))
	for _, entry := range snapshot.Claims {
		claims = append(claims, generatedingress.GatewayV2StartupClaim{
			Request: generatedingress.GatewayV2UpgradeRequest{
				OperationID: entry.Claim.OperationID,
				Profile: generatedingress.GatewayV2ProfileBinding{
					RevisionID: entry.Profile.ID, RevisionNumber: entry.Profile.RevisionNumber,
					SelectedIPv4: entry.Profile.Spec.SelectedIPv4, InterfaceID: entry.Profile.Spec.InterfaceID,
					PortStart: entry.Profile.Spec.PortStart, PortEnd: entry.Profile.Spec.PortEnd,
					SpecDigest: entry.Profile.SpecDigest,
				},
				ApprovedBy: entry.Claim.ApprovedBy, ApprovedActionDigest: entry.ApprovedActionDigest,
			},
			State: entry.Claim.State, StateSequence: entry.Claim.StateSequence,
		})
	}
	return claims
}
