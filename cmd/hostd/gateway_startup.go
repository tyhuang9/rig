package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayStartup struct {
	ingress    *generatedingress.Manager
	snapshot   appaccess.GatewayUpgradeStartupSnapshot
	inspection generatedingress.GatewayV2StartupInspection
}

func inspectGatewayStartup(ctx context.Context, cfg config.Config, db *sql.DB,
	dockerExecutable string, directories docker.ControllerDirectories,
) (gatewayStartup, error) {
	if ctx == nil || db == nil {
		return gatewayStartup{}, errors.New("gateway startup dependencies are required")
	}
	repository := appaccess.New(db)
	snapshot, err := repository.GatewayUpgradeStartupSnapshot(ctx)
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("read gateway upgrade startup snapshot: %w", err)
	}
	if !cfg.GeneratedRuntime {
		history, err := hasProtectedGatewayUpgradeHistory(cfg.DataRoot)
		if err != nil {
			return gatewayStartup{}, fmt.Errorf("inspect protected gateway history: %w", err)
		}
		if len(snapshot.Claims) != 0 || history {
			return gatewayStartup{}, errors.New("generated runtime is required to reconcile existing gateway upgrade history")
		}
		return gatewayStartup{snapshot: snapshot}, nil
	}
	ingress, err := generatedingress.New(runtimeprocess.ExecRunner{}, generatedingress.Options{
		DockerExecutable: dockerExecutable, DockerEndpoint: cfg.DockerEndpoint,
		DockerConfigDirectory: directories.DockerConfigDirectory, WorkingDirectory: directories.WorkingDirectory,
		DataRoot: cfg.DataRoot,
	})
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("create gateway startup inspector: %w", err)
	}
	inspection, err := ingress.InspectGatewayV2Startup(ctx, gatewayStartupClaims(snapshot))
	if err != nil {
		return gatewayStartup{}, fmt.Errorf("inspect gateway upgrade startup: %w", err)
	}
	return gatewayStartup{ingress: ingress, snapshot: snapshot, inspection: inspection}, nil
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
