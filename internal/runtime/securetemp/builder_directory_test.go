package securetemp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGeneratedBuilderDirectoryPersistsOnlyProtectedDirectChildren(t *testing.T) {
	directory, err := NewGeneratedBuilderDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dockerConfig, err := directory.EnsureDirectory("docker-config")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dockerConfig) != directory.Root() {
		t.Fatalf("builder config escaped root: %q", dockerConfig)
	}
	contents := []byte("controller-state")
	if err := directory.WriteNewFile("identity.json", contents); err != nil {
		t.Fatal(err)
	}
	for _, value := range contents {
		if value != 0 {
			t.Fatal("builder state input was not cleared")
		}
	}
	body, err := directory.ReadFile("identity.json", 1024)
	if err != nil || string(body) != "controller-state" {
		t.Fatalf("builder state = %q, %v", body, err)
	}
	clear(body)
	if err := directory.WriteNewFile("identity.json", []byte("replacement")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("persistent state was replaced: %v", err)
	}
	for _, invalid := range []string{"", ".", "..", "a/b", `a\\b`, "UPPER", "space name", "two..dots"} {
		if _, err := directory.EnsureDirectory(invalid); err == nil {
			t.Errorf("EnsureDirectory(%q) succeeded", invalid)
		}
		if err := directory.WriteNewFile(invalid, []byte("x")); err == nil {
			t.Errorf("WriteNewFile(%q) succeeded", invalid)
		}
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{directory.Root(), dockerConfig, filepath.Join(directory.Root(), "identity.json")} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if info.Mode().Perm() != want {
				t.Fatalf("%s permissions = %o, want %o", path, info.Mode().Perm(), want)
			}
		}
	}
}

func TestGeneratedBuilderDirectoryRejectsSymlinkedPersistentRoot(t *testing.T) {
	dataRoot := t.TempDir()
	runtimeRoot := filepath.Join(dataRoot, "runtime")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runtimeRoot, "generated-builder")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := NewGeneratedBuilderDirectory(dataRoot); err == nil {
		t.Fatal("symlinked generated-builder root was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "identity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path was modified: %v", err)
	}
}
