package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/runtime/docker"
)

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
