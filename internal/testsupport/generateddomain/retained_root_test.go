package generateddomain

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRetainedRootOwnerRemovesOnlyExactOwnedTree(t *testing.T) {
	scope := t.TempDir()
	sibling := filepath.Join(scope, "sibling")
	target := filepath.Join(scope, "target")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := newRetainedRootOwner(scope)
	if err != nil {
		t.Fatal(err)
	}
	ownedPath := owner.path
	linked := true
	if err := os.Symlink(target, filepath.Join(ownedPath, "outside")); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		linked = false
		t.Logf("symlink control unavailable on Windows: %v", err)
	}
	if err := owner.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(ownedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned root remains after cleanup: %v", err)
	}
	if info, err := os.Lstat(sibling); err != nil || !info.IsDir() {
		t.Fatalf("sibling changed during cleanup: info=%v error=%v", info, err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "target" {
		t.Fatalf("symlink target changed during cleanup: body=%q error=%v", body, err)
	}
	if linked {
		t.Log("cleanup removed the owned symlink without traversing its target")
	}
	if err := owner.cleanup(); err != nil {
		t.Fatalf("second cleanup after success: %v", err)
	}
}

func TestRetainedRootOwnerRefusesReplacementAndParentDrift(t *testing.T) {
	t.Run("replacement at owned path", func(t *testing.T) {
		scope := t.TempDir()
		owner, err := newRetainedRootOwner(scope)
		if err != nil {
			t.Fatal(err)
		}
		ownedPath := owner.path
		moved := filepath.Join(scope, "moved-original")
		if err := os.Rename(ownedPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(ownedPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := owner.cleanup(); err == nil {
			t.Fatal("cleanup accepted a replacement at the owned path")
		}
		for _, path := range []string{ownedPath, moved} {
			if info, err := os.Lstat(path); err != nil || !info.IsDir() {
				t.Fatalf("cleanup removed %s: info=%v error=%v", filepath.Base(path), info, err)
			}
		}
	})

	t.Run("missing owned path", func(t *testing.T) {
		scope := t.TempDir()
		owner, err := newRetainedRootOwner(scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(owner.path); err != nil {
			t.Fatal(err)
		}
		if err := owner.cleanup(); err == nil {
			t.Fatal("cleanup accepted an absent owned path")
		}
	})

	t.Run("parent replacement", func(t *testing.T) {
		scope := t.TempDir()
		parent := filepath.Join(scope, "parent")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		owner, err := newRetainedRootOwner(parent)
		if err != nil {
			t.Fatal(err)
		}
		movedParent := filepath.Join(scope, "moved-parent")
		if err := os.Rename(parent, movedParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := owner.cleanup(); err == nil {
			t.Fatal("cleanup accepted a replacement parent scope")
		}
		if info, err := os.Lstat(filepath.Join(movedParent, filepath.Base(owner.path))); err != nil || !info.IsDir() {
			t.Fatalf("cleanup removed root under original parent: info=%v error=%v", info, err)
		}
	})
}
