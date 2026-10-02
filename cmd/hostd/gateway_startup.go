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
	ingress          *generatedingress.Manager
	snapshot         appaccess.HostingGatewayStartupSnapshot
	inspection       generatedingress.GatewayV2StartupInspection
	grantInspection  generatedingress.GatewayV2LANStartupInspection
	accessInspection generatedingress.GatewayV2LANAccessStartupInspection
	recoveryKind     string
	recoveryID       string
	recoveryAppID    string
}

func inspectGatewayStartup(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories,
) (gatewayStartup, error) {
	if ctx == nil || db == nil {
		return gatewayStartup{}, errors.New("gateway startup dependencies are required")
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
	ingress, err := newGatewayStartupIngress(cfg, dockerExecutable, directories)
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
	accessInspection, err := ingress.InspectGatewayV2LANAccessStartup(ctx,
		lanGrantStartupClaims(snapshot.Grants), lanDisableStartupClaims(snapshot.Disables))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect LAN access startup: %w", err)
	}
	result.accessInspection = accessInspection
	result.recoveryKind, result.recoveryID, result.recoveryAppID, err = selectLANAccessStartupRecovery(snapshot, inspection, accessInspection)
	if err != nil {
		return gatewayStartup{}, err
	}
	return result, nil
}

func newGatewayStartupIngress(cfg config.Config, dockerExecutable string,
	directories docker.ControllerDirectories,
) (*generatedingress.Manager, error) {
	ingress, err := generatedingress.New(runtimeprocess.ExecRunner{}, generatedingress.Options{
		DockerExecutable: dockerExecutable, DockerEndpoint: cfg.DockerEndpoint,
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		DataRoot: cfg.DataRoot,
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
	ingress, err := newGatewayStartupIngress(cfg, dockerExecutable, directories)
	if err != nil {
		return err
	}
	return ingress.StopOwnedGatewayV2OnStartupFailure(ctx)
}

// quarantineLANRecoveryStartup withdraws any observed unfinished LAN grant or
// disable before the recovery-only controller serves its listener. It then
// repeats both cross-store inspections against the same immutable census.
// A failed withdrawal or changed snapshot prevents controller startup.
func quarantineLANRecoveryStartup(ctx context.Context, db *sql.DB, gate gatewayStartup) (gatewayStartup, error) {
	if ctx == nil || db == nil || gate.ingress == nil ||
		(gate.recoveryKind != controller.RecoveryLANGrant && gate.recoveryKind != controller.RecoveryLANDisable) ||
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
	kind, operationID, appID, err := selectLANAccessStartupRecovery(confirmedSnapshot, upgradeInspection, accessInspection)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("LAN grant quarantine changed pinned recovery identity: %w", err)
	}
	if kind != gate.recoveryKind || operationID != gate.recoveryID || appID != gate.recoveryAppID {
		return gatewayStartup{}, errors.New("LAN grant quarantine changed pinned recovery identity")
	}
	gate.inspection = upgradeInspection
	gate.accessInspection = accessInspection
	return gate, nil
}

func selectLANAccessStartupRecovery(snapshot appaccess.HostingGatewayStartupSnapshot,
	inspection generatedingress.GatewayV2StartupInspection,
	access generatedingress.GatewayV2LANAccessStartupInspection,
) (kind, operationID, appID string, err error) {
	if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 &&
		inspection.Disposition != generatedingress.GatewayV2StartupRecoveryOnly {
		return "", "", "", errors.New("LAN access startup requires a v2 gateway")
	}
	if !committedGatewayStartupOperation(snapshot.Upgrades, inspection.OperationID) {
		return "", "", "", errors.New("LAN access startup requires a committed v2 gateway")
	}
	switch access.Disposition {
	case generatedingress.GatewayV2LANStartupNormal:
		if inspection.Disposition != generatedingress.GatewayV2StartupNormalV2 ||
			access.RecoveryKind != "" || access.OperationID != "" || access.AppID != "" {
			return "", "", "", errors.New("gateway and LAN access startup inspections disagree")
		}
		return "", "", "", nil
	case generatedingress.GatewayV2LANStartupRecoveryOnly:
		switch access.RecoveryKind {
		case controller.RecoveryLANGrant:
			selectedAppID, ok := grantStartupAppID(snapshot.Grants, access.OperationID)
			if !ok || selectedAppID != access.AppID {
				return "", "", "", errors.New("LAN grant recovery identity is not in the startup snapshot")
			}
		case controller.RecoveryLANDisable:
			selectedAppID, ok := disableStartupAppID(snapshot.Disables, access.OperationID)
			if !ok || selectedAppID != access.AppID {
				return "", "", "", errors.New("LAN disable recovery identity is not in the startup snapshot")
			}
		default:
			return "", "", "", errors.New("unknown LAN access recovery kind")
		}
		return access.RecoveryKind, access.OperationID, access.AppID, nil
	default:
		return "", "", "", errors.New("unknown LAN access startup disposition")
	}
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
