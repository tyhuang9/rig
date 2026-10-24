package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/runtime/docker"
	"github.com/hostd/hostd/internal/secretfile"
)

func TestControllerDatabaseOpenerPreservesRuntimeModeAndOwnerProof(t *testing.T) {
	open := openControllerDatabase
	newBoundary := func(t *testing.T) (string, *controllerowner.Lease) {
		t.Helper()
		dataRoot := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(dataRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		directories, err := docker.PrepareControllerDirectories(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lease.Close() })
		return dataRoot, lease
	}

	t.Run("generated runtime binds exact owner and database", func(t *testing.T) {
		dataRoot, lease := newBoundary(t)
		db, err := open(context.Background(), config.Config{DataRoot: dataRoot, GeneratedRuntime: true}, lease)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if identity, err := database.GeneratedDomainFileIdentity(context.Background(), db, dataRoot); err != nil || identity == nil {
			t.Fatalf("generated runtime database identity = %v, %v", identity, err)
		}
	})

	for _, test := range []struct {
		name          string
		configuration config.Config
	}{
		{name: "disabled runtime remains generic", configuration: config.Config{}},
		{name: "compose runtime remains generic", configuration: config.Config{ComposeRuntime: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := filepath.Join(t.TempDir(), "state")
			test.configuration.DataRoot = dataRoot
			db, err := open(context.Background(), test.configuration, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if _, err := database.GeneratedDomainFileIdentity(context.Background(), db, dataRoot); err == nil {
				t.Fatal("non-generated runtime database acquired generated-domain provenance")
			}
		})
	}

	t.Run("generated runtime refuses missing owner lease", func(t *testing.T) {
		dataRoot, lease := newBoundary(t)
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := open(context.Background(), config.Config{DataRoot: dataRoot, GeneratedRuntime: true}, nil)
		if db != nil {
			_ = db.Close()
		}
		if db != nil || err == nil {
			t.Fatalf("generated runtime missing-lease open = %v, %v", db, err)
		}
		if _, statErr := os.Stat(filepath.Join(dataRoot, "control.db")); !os.IsNotExist(statErr) {
			t.Fatalf("generated runtime missing-lease open touched database: %v", statErr)
		}
	})

	t.Run("generated runtime refuses foreign owner lease", func(t *testing.T) {
		dataRoot, _ := newBoundary(t)
		_, foreignLease := newBoundary(t)
		db, err := open(context.Background(), config.Config{DataRoot: dataRoot, GeneratedRuntime: true}, foreignLease)
		if db != nil {
			_ = db.Close()
		}
		if db != nil || err == nil {
			t.Fatalf("generated runtime foreign-lease open = %v, %v", db, err)
		}
		if _, statErr := os.Stat(filepath.Join(dataRoot, "control.db")); !os.IsNotExist(statErr) {
			t.Fatalf("generated runtime foreign-lease open touched database: %v", statErr)
		}
	})
}

func TestStartupRejectsNonLoopbackBeforeDataRootCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	if _, err := startupConfig([]string{"--listen", "0.0.0.0:7345", "--data-root", root}); err == nil {
		t.Fatal("non-loopback startup configuration was accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("data root was touched before listen validation: %v", err)
	}
}

func TestBootstrapTokenUsesProtectedFileInsteadOfProcessOutput(t *testing.T) {
	const token = "one-time-test-token"
	var secretOutput bytes.Buffer
	var structuredLogs bytes.Buffer
	logger := newStructuredLogger(&structuredLogs, "info")
	dataRoot := t.TempDir()

	cleanup, err := prepareBootstrapToken(&secretOutput, dataRoot, token, time.Minute, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	logger.Info("hostd listening", "address", "127.0.0.1:7345")

	path := filepath.Join(dataRoot, bootstrapSecretFilename)
	if secretOutput.String() != path+"\n" || strings.Contains(secretOutput.String(), token) {
		t.Fatalf("bootstrap output exposed a token or omitted its path: %q", secretOutput.String())
	}
	if strings.Contains(structuredLogs.String(), token) {
		t.Fatalf("bootstrap token leaked to structured logs: %q", structuredLogs.String())
	}
	stored, err := secretfile.Read(path, "bootstrap-token")
	if err != nil || string(stored) != token {
		t.Fatalf("protected bootstrap file = %q, %v", stored, err)
	}
	clear(stored)
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("bootstrap file remained after cleanup: %v", err)
	}
	if strings.Contains(secretOutput.String(), "hostd listening") {
		t.Fatalf("ordinary log leaked to secret output: %q", secretOutput.String())
	}
	if !strings.Contains(structuredLogs.String(), `"msg":"hostd listening"`) {
		t.Fatalf("ordinary log missing from structured output: %q", structuredLogs.String())
	}
}

func TestBootstrapTokenExpiresAndStaleFileIsRemoved(t *testing.T) {
	dataRoot := t.TempDir()
	path := filepath.Join(dataRoot, bootstrapSecretFilename)
	cleanup, err := prepareBootstrapToken(io.Discard, dataRoot, "short-lived-token", 10*time.Millisecond, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired bootstrap file remained: %v", err)
	}
	if err := secretfile.Write(path, "bootstrap-token", []byte("stale")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareBootstrapToken(io.Discard, dataRoot, "", time.Minute, func(error) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale bootstrap file remained: %v", err)
	}
}

func TestBootstrapTokenCleanupToleratesMissingFile(t *testing.T) {
	dataRoot := t.TempDir()
	reported := make(chan error, 1)
	cleanup, err := prepareBootstrapToken(io.Discard, dataRoot, "one-time-test-token", time.Minute, func(err error) {
		reported <- err
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, bootstrapSecretFilename)
	if err := secretfile.Remove(path); err != nil {
		t.Fatal(err)
	}
	cleanup()
	select {
	case err := <-reported:
		t.Fatalf("already-missing cleanup was reported as an error: %v", err)
	default:
	}
}

func TestBootstrapTokenProtectionFailureDoesNotExposeToken(t *testing.T) {
	const token = "one-time-test-token"
	dataRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dataRoot, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := prepareBootstrapToken(&output, dataRoot, token, time.Minute, func(error) {}); err == nil {
		t.Fatal("bootstrap secret file protection failure was accepted")
	} else if strings.Contains(err.Error(), token) || output.Len() != 0 {
		t.Fatalf("bootstrap token leaked on protection failure: output=%q error=%q", output.String(), err)
	}
}

func TestWaitForWorkerDrainsOrTimesOut(t *testing.T) {
	done := make(chan struct{})
	go func() {
		time.Sleep(time.Millisecond)
		close(done)
	}()
	if !waitForWorker(done, time.Second) {
		t.Fatal("worker drain was not observed")
	}
	if waitForWorker(make(chan struct{}), time.Millisecond) {
		t.Fatal("worker timeout was reported as drained")
	}
}
