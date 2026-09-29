//go:build !windows

package generatedingress

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayOSLockRejectsSymlink(t *testing.T) {
	store, err := newStateStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.root, gatewayLockFilename)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if release, err := acquireGatewayOSLock(context.Background(), store); err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("acquisition unexpectedly accepted a symlink lock path")
	}
}

func TestGatewayOSLockRejectsBroadPermissions(t *testing.T) {
	store, err := newStateStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.root, gatewayLockFilename)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireGatewayOSLock(context.Background(), store); err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("acquisition unexpectedly accepted group-readable lock file")
	}
}
