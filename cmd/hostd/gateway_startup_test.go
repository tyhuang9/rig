package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/runtime/docker"
)

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
