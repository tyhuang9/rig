//go:build !windows

package securetemp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePrivateDirectoryRejectsPermissiveUnixMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "working")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(path); err != nil {
		t.Fatalf("private directory rejected: %v", err)
	}
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(path); err == nil {
		t.Fatal("group-traversable directory was accepted")
	}
}

func TestValidatePrivateDirectoryRejectsUnixSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "working")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(link); err == nil {
		t.Fatal("symlinked directory was accepted")
	}
}

func TestValidatePrivateDirectoryAllowsPrivateLeafBeneathReadableAncestor(t *testing.T) {
	ancestor := filepath.Join(t.TempDir(), "readable")
	if err := os.Mkdir(ancestor, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ancestor, "working")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(path); err != nil {
		t.Fatalf("private leaf beneath non-writable readable ancestor rejected: %v", err)
	}
}

func TestValidatePrivateDirectoryRejectsWritableUnixAncestor(t *testing.T) {
	ancestor := filepath.Join(t.TempDir(), "writable")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ancestor, "working")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(path); err == nil {
		t.Fatal("private leaf beneath writable ancestor was accepted")
	}
}

func TestValidatePrivateDirectoryAllowsPrivateLeafBeneathStickyAncestor(t *testing.T) {
	ancestor := filepath.Join(t.TempDir(), "sticky")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ancestor, "working")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateDirectory(path); err != nil {
		t.Fatalf("private leaf beneath sticky ancestor rejected: %v", err)
	}
}
