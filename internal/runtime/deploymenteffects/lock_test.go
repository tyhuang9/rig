package deploymenteffects

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

const lockHelperEnvironment = "HOSTD_DEPLOYMENT_EFFECTS_LOCK_HELPER"

func TestAcquireSerializesSeparateHandlesForSameWorkingDirectory(t *testing.T) {
	directory := deploymentEffectsTestDirectory(t)
	releaseFirst, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = releaseFirst() })

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if release, err := Acquire(ctx, directory); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		t.Fatalf("contended acquisition error = %v, want deadline exceeded", err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	releaseSecond, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := releaseSecond(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsCancellationBeforeAndAcrossContention(t *testing.T) {
	t.Run("before acquisition", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if release, err := Acquire(ctx, directory); !errors.Is(err, context.Canceled) {
			if release != nil {
				_ = release()
			}
			t.Fatalf("error = %v, want canceled", err)
		}
		if _, err := os.Lstat(filepath.Join(directory, lockFilename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled acquisition created lock file: %v", err)
		}
	})

	t.Run("during contention", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		releaseFirst, err := Acquire(context.Background(), directory)
		if err != nil {
			t.Fatal(err)
		}
		defer releaseFirst()
		originalTryLock := acquireTryLockFile
		contended := make(chan struct{})
		var contendedOnce sync.Once
		acquireTryLockFile = func(file *os.File) (bool, error) {
			locked, err := originalTryLock(file)
			if !locked && err == nil {
				contendedOnce.Do(func() { close(contended) })
			}
			return locked, err
		}
		t.Cleanup(func() { acquireTryLockFile = originalTryLock })
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			release, err := Acquire(ctx, directory)
			if release != nil {
				_ = release()
			}
			result <- err
		}()
		select {
		case <-contended:
		case <-time.After(time.Second):
			t.Fatal("second acquisition did not contend")
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("contended cancellation error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("contended acquisition did not observe cancellation")
		}
	})
}

func TestReleaseIsIdempotentAndLeavesPersistentEmptyFile(t *testing.T) {
	directory := deploymentEffectsTestDirectory(t)
	release, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
	info, err := os.Lstat(filepath.Join(directory, lockFilename))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("persistent lock file is missing or unsafe: info=%v err=%v", info, err)
	}
}

func TestAcquireRejectsUnsafeLockPaths(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(directory, lockFilename)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if release, err := Acquire(context.Background(), directory); err == nil {
			if release != nil {
				_ = release()
			}
			t.Fatal("symlink lock path was accepted")
		}
	})

	t.Run("directory", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		if err := os.Mkdir(filepath.Join(directory, lockFilename), 0o700); err != nil {
			t.Fatal(err)
		}
		if release, err := Acquire(context.Background(), directory); err == nil {
			if release != nil {
				_ = release()
			}
			t.Fatal("directory lock path was accepted")
		}
	})

	t.Run("nonempty file", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		if err := os.WriteFile(filepath.Join(directory, lockFilename), []byte("unexpected"), 0o600); err != nil {
			t.Fatal(err)
		}
		if release, err := Acquire(context.Background(), directory); err == nil {
			if release != nil {
				_ = release()
			}
			t.Fatal("nonempty lock file was accepted")
		}
	})

	t.Run("hard link", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, filepath.Join(directory, lockFilename)); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		if release, err := Acquire(context.Background(), directory); err == nil {
			if release != nil {
				_ = release()
			}
			t.Fatal("multiply linked lock file was accepted")
		}
	})
}

func TestAcquireRejectsUnsafeWorkingDirectory(t *testing.T) {
	if release, err := Acquire(context.Background(), "relative"); err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("relative working directory was accepted")
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(context.Background(), path); err == nil {
		if release != nil {
			_ = release()
		}
		t.Fatal("non-directory working path was accepted")
	}
}

func TestLockIsReleasedWhenHoldingProcessExits(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess coverage is disabled in short mode")
	}
	directory := deploymentEffectsTestDirectory(t)
	command := exec.Command(os.Args[0], "-test.run=^TestDeploymentEffectsLockCrashHelper$")
	command.Env = append(os.Environ(), lockHelperEnvironment+"="+directory)
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
	ready := make(chan bool, 1)
	go func() { ready <- scanner.Scan() }()
	var scanned bool
	select {
	case scanned = <-ready:
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("lock helper did not report readiness before deadline")
	}
	if !scanned || scanner.Text() != "locked" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("lock helper did not report acquisition: %v", scanner.Err())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	if release, err := Acquire(ctx, directory); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			_ = release()
		}
		cancel()
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("cross-process contention error = %v", err)
	}
	cancel()
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("terminate lock helper: %v", err)
	}
	_ = command.Wait()

	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
	defer acquireCancel()
	release, err := Acquire(acquireCtx, directory)
	if err != nil {
		t.Fatalf("acquire after helper exit: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentEffectsLockCrashHelper(t *testing.T) {
	directory := os.Getenv(lockHelperEnvironment)
	if directory == "" {
		return
	}
	if _, err := Acquire(context.Background(), directory); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, "locked")
	buffer := make([]byte, 1)
	_, _ = os.Stdin.Read(buffer)
	os.Exit(0)
}

func deploymentEffectsTestDirectory(t *testing.T) string {
	t.Helper()
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directories.WorkingDirectory
}
