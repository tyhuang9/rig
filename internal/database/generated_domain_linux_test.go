//go:build linux

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hostd/hostd/internal/controllerowner"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestOpenForGeneratedDomainRejectsCaptureWindowReplacement(t *testing.T) {
	for _, phase := range []string{
		"before driver open",
		"after driver open",
		"after binding before initialization",
		"after initialization before issuance",
	} {
		t.Run(phase, func(t *testing.T) {
			dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
			capture, err := captureGeneratedDomainDatabase(dataRoot, lease)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = capture.close() })
			var pending *generatedDomainPendingDatabase
			if phase != "before driver open" {
				pending, err = connectGeneratedDomainDatabase(context.Background(), capture)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pending.database.Close() })
			}
			if phase == "after binding before initialization" || phase == "after initialization before issuance" {
				if err := pending.bind(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "after initialization before issuance" {
				if err := pending.initialize(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			original, replacement := replaceGeneratedDomainCapturedPath(t, capture.databasePath)
			switch phase {
			case "before driver open":
				pending, err = connectGeneratedDomainDatabase(context.Background(), capture)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pending.database.Close() })
				err = pending.bind(context.Background())
			case "after driver open":
				err = pending.bind(context.Background())
			case "after binding before initialization":
				err = pending.initialize(context.Background())
			case "after initialization before issuance":
				err = pending.issue(context.Background())
			}
			if err == nil {
				t.Fatalf("replacement accepted during %s", phase)
			}
			info, statErr := os.Stat(replacement)
			if statErr != nil || info.Size() != 0 {
				t.Fatalf("replacement received initialization effects: size=%v error=%v", generatedDomainFileSize(info), statErr)
			}
			if phase != "after initialization before issuance" {
				info, statErr := os.Stat(original)
				if statErr != nil || info.Size() != 0 {
					t.Fatalf("captured original received pre-binding initialization effects: size=%v error=%v", generatedDomainFileSize(info), statErr)
				}
			}
			if _, err := GeneratedDomainFileIdentity(context.Background(), pending.database, dataRoot); err == nil {
				t.Fatal("failed capture published provenance")
			}
		})
	}
}

func replaceGeneratedDomainCapturedPath(t *testing.T, path string) (string, string) {
	t.Helper()
	original := path + ".captured-original"
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return original, path
}

func generatedDomainFileSize(info os.FileInfo) int64 {
	if info == nil {
		return -1
	}
	return info.Size()
}

func TestOpenForGeneratedDomainRejectsSymlinkedDatabaseBeforeOpen(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	target := filepath.Join(t.TempDir(), "foreign.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dataRoot, generatedDomainDatabaseFilename)); err != nil {
		t.Fatal(err)
	}
	if database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease); database != nil || err == nil {
		t.Fatalf("symlinked database open = %v, %v", database, err)
	}
}

func TestOpenForGeneratedDomainRejectsDSNMetacharactersBeforeFileCreation(t *testing.T) {
	for _, metacharacter := range []string{"?", "#"} {
		t.Run(metacharacter, func(t *testing.T) {
			dataRoot := filepath.Join(t.TempDir(), "state"+metacharacter+"private")
			if err := os.Mkdir(dataRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := controllerowner.Acquire(context.Background(), directories.WorkingDirectory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lease.Close() })
			if database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease); database != nil || err == nil {
				t.Fatalf("DSN metacharacter open = %v, %v", database, err)
			}
			if _, err := os.Stat(filepath.Join(dataRoot, generatedDomainDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid DSN path created database state: %v", err)
			}
		})
	}
}

func TestGeneratedDomainDatabaseProvenanceRejectsReplacedLeaseOrRootPath(t *testing.T) {
	t.Run("lock path", func(t *testing.T) {
		dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
		database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		working, err := generatedDomainWorkingDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(working, "controller-owner.lock")
		if err := os.Rename(lockPath, lockPath+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot); err == nil {
			t.Fatal("database provenance survived owner lock path replacement")
		}
	})

	t.Run("data root", func(t *testing.T) {
		dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
		database, err := OpenForGeneratedDomain(context.Background(), dataRoot, lease)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := os.Rename(dataRoot, dataRoot+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dataRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := GeneratedDomainFileIdentity(context.Background(), database, dataRoot); err == nil {
			t.Fatal("database provenance survived data-root replacement")
		}
	})
}
