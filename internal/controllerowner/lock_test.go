package controllerowner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/runtime/docker"
)

func TestControllerOwnerLockContentionAndRelease(t *testing.T) {
	directory := ownerTestDirectory(t)
	lease, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(context.Background(), directory)
	if second != nil || !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("second owner lease = %v, %v", second, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	third, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, filename)); err != nil {
		t.Fatalf("persistent lock file was removed: %v", err)
	}
}

func TestControllerOwnerLockRejectsCancelledAndUnsafeInputs(t *testing.T) {
	directory := ownerTestDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lease, err := Acquire(ctx, directory); lease != nil || err == nil {
		t.Fatalf("cancelled acquire = %v, %v", lease, err)
	}
	if lease, err := Acquire(context.Background(), "."); lease != nil || err == nil {
		t.Fatalf("relative directory acquired = %v, %v", lease, err)
	}
	path := filepath.Join(directory, filename)
	if err := os.WriteFile(path, []byte("unexpected state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if lease, err := Acquire(context.Background(), directory); lease != nil || err == nil {
		t.Fatalf("nonempty lock file acquired = %v, %v", lease, err)
	}
}

func TestControllerOwnerLockRejectsSymlinkedFile(t *testing.T) {
	directory := ownerTestDirectory(t)
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, filename)); err != nil {
		t.Skipf("cannot create file symlink on this host: %v", err)
	}
	if lease, err := Acquire(context.Background(), directory); lease != nil || err == nil {
		t.Fatalf("symlinked lock file acquired = %v, %v", lease, err)
	}
}

// The child intentionally exits without Close to prove that the kernel, rather
// than a deletion or cleanup callback, releases ownership on process exit.
func TestControllerOwnerLockChild(t *testing.T) {
	directory := os.Getenv("RIG_CONTROLLER_OWNER_TEST_CHILD")
	if directory == "" {
		return
	}
	if _, err := Acquire(context.Background(), directory); err != nil {
		fmt.Fprintln(os.Stdout, "ERROR:", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "LOCKED")
	_, _ = os.Stdin.Read(make([]byte, 1))
	os.Exit(0)
}

func TestControllerOwnerLockReleasedAfterProcessExit(t *testing.T) {
	directory := ownerTestDirectory(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestControllerOwnerLockChild$")
	cmd.Env = append(os.Environ(), "RIG_CONTROLLER_OWNER_TEST_CHILD="+directory)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "LOCKED" {
		t.Fatalf("child did not acquire owner lock: %q %v", line, err)
	}
	if lease, err := Acquire(context.Background(), directory); lease != nil || !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("parent acquired while child owns lock: %v, %v", lease, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatalf("lock did not release at process exit: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func ownerTestDirectory(t *testing.T) string {
	t.Helper()
	directories, err := docker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directories.WorkingDirectory
}
