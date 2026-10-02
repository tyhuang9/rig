package generatedingress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const gatewayLockHelperEnvironment = "HOSTD_GATEWAY_LOCK_HELPER"

func TestGatewayOSLockSerializesIndependentStores(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	firstStore, err := newStateStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := newStateStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}

	releaseFirst, err := acquireGatewayOSLock(context.Background(), firstStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = releaseFirst() })

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if release, err := acquireGatewayOSLock(ctx, secondStore); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		t.Fatalf("second acquisition error = %v, want context deadline exceeded", err)
	}

	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	releaseSecond, err := acquireGatewayOSLock(context.Background(), secondStore)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := releaseSecond(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayOSLockRejectsCanceledContext(t *testing.T) {
	store, err := newStateStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := acquireGatewayOSLock(ctx, store); !errors.Is(err, context.Canceled) {
		if release != nil {
			_ = release()
		}
		t.Fatalf("acquisition error = %v, want context canceled", err)
	}
	if _, err := os.Lstat(filepath.Join(store.root, gatewayLockFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled acquisition created lock file: %v", err)
	}
}

func TestGatewayOSLockReleaseIsIdempotent(t *testing.T) {
	store, err := newStateStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireGatewayOSLock(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	info, err := os.Lstat(filepath.Join(store.root, gatewayLockFilename))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("persistent lock file missing or unsafe after release: %v", err)
	}
}

func TestGatewayOSLockRejectsNonRegularPath(t *testing.T) {
	store, err := newStateStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.root, gatewayLockFilename)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireGatewayOSLock(context.Background(), store); err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("acquisition unexpectedly accepted a directory lock path")
	}
}

func TestGatewayOSLockReleasedWhenProcessExits(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess coverage is disabled in short mode")
	}
	dataRoot := filepath.Join(t.TempDir(), "data")
	store, err := newStateStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestGatewayOSLockCrashHelper$")
	command.Env = append(os.Environ(), gatewayLockHelperEnvironment+"="+dataRoot)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("lock helper did not report acquisition: %v", scanner.Err())
	}

	contendedCtx, contendedCancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	if release, err := acquireGatewayOSLock(contendedCtx, store); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		contendedCancel()
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("cross-process contention error = %v, want context deadline exceeded", err)
	}
	contendedCancel()

	if err := command.Process.Kill(); err != nil {
		t.Fatalf("terminate lock helper: %v", err)
	}
	_ = command.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := acquireGatewayOSLock(ctx, store)
	if err != nil {
		t.Fatalf("acquire after helper process exit: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayOSLockCrashHelper(t *testing.T) {
	dataRoot := os.Getenv(gatewayLockHelperEnvironment)
	if dataRoot == "" {
		return
	}
	store, err := newStateStore(dataRoot)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := acquireGatewayOSLock(context.Background(), store); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	_, _ = fmt.Fprintln(os.Stdout, "locked")
	buffer := make([]byte, 1)
	_, _ = os.Stdin.Read(buffer)
	os.Exit(0)
}

func TestGatewayOSLockRejectsUnsafeLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse paths are covered by handle validation")
	}
	store, err := newStateStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.root, gatewayLockFilename)
	if err := os.Link(target, lockPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if release, err := acquireGatewayOSLock(context.Background(), store); err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("acquisition unexpectedly accepted a multiply linked lock file")
	}
}
