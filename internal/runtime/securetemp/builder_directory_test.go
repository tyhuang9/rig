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
	if _, err := OpenGeneratedBuilderDirectory(dataRoot); err == nil {
		t.Fatal("symlinked existing generated-builder root was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "identity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path was modified: %v", err)
	}
}

func TestOpenGeneratedBuilderDirectoryRequiresExistingPinnedRoot(t *testing.T) {
	t.Run("missing state remains absent", func(t *testing.T) {
		dataRoot := t.TempDir()
		if _, err := OpenGeneratedBuilderDirectory(dataRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing generated builder root error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dataRoot, "runtime")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only open created runtime state: %v", err)
		}
	})

	t.Run("broad runtime parent is refused without mutation", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("portable chmod cannot broaden a Windows directory ACL")
		}
		dataRoot := t.TempDir()
		directory, err := NewGeneratedBuilderDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		runtimeRoot := filepath.Dir(directory.Root())
		if err := os.Chmod(runtimeRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenGeneratedBuilderDirectory(dataRoot); err == nil {
			t.Fatal("broad generated-builder runtime parent was accepted")
		}
		info, err := os.Stat(runtimeRoot)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("read-only open mutated broad runtime parent mode = %v", info.Mode().Perm())
		}
		if _, err := os.Stat(directory.Root()); err != nil {
			t.Fatalf("read-only open mutated generated-builder child: %v", err)
		}
	})

	t.Run("existing private state preserves identity", func(t *testing.T) {
		dataRoot := t.TempDir()
		created, err := NewGeneratedBuilderDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := created.WriteNewFile("identity.json", []byte("exact")); err != nil {
			t.Fatal(err)
		}
		opened, err := OpenGeneratedBuilderDirectory(dataRoot)
		if err != nil || !created.Same(opened) {
			t.Fatalf("existing generated builder root = same %t, %v", created.Same(opened), err)
		}
		body, err := opened.ReadFile("identity.json", 32)
		if err != nil || string(body) != "exact" {
			t.Fatalf("existing generated builder state = %q, %v", body, err)
		}
		clear(body)
	})

	t.Run("file snapshots bind identity and exact contents", func(t *testing.T) {
		dataRoot := t.TempDir()
		directory, err := NewGeneratedBuilderDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := directory.WriteNewFile("identity.json", []byte("exact")); err != nil {
			t.Fatal(err)
		}
		firstBody, first, err := directory.ReadFileSnapshot("identity.json", 32)
		if err != nil {
			t.Fatal(err)
		}
		clear(firstBody)
		secondBody, second, err := directory.ReadFileSnapshot("identity.json", 32)
		if err != nil || !first.Same(second) {
			t.Fatalf("unchanged generated builder snapshot = same %t, %v", first.Same(second), err)
		}
		clear(secondBody)
		path := filepath.Join(directory.Root(), "identity.json")
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("exact"), 0o600); err != nil {
			t.Fatal(err)
		}
		replacementBody, replacement, err := directory.ReadFileSnapshot("identity.json", 32)
		if err != nil || first.Same(replacement) {
			t.Fatalf("replacement generated builder snapshot = same %t, %v", first.Same(replacement), err)
		}
		clear(replacementBody)
		if err := os.WriteFile(path, []byte("reencoded"), 0o600); err != nil {
			t.Fatal(err)
		}
		reencodedBody, reencoded, err := directory.ReadFileSnapshot("identity.json", 32)
		if err != nil || replacement.Same(reencoded) {
			t.Fatalf("rewritten generated builder snapshot = same %t, %v", replacement.Same(reencoded), err)
		}
		clear(reencodedBody)
	})

	t.Run("renamed root and replacement are not adopted", func(t *testing.T) {
		dataRoot := t.TempDir()
		created, err := NewGeneratedBuilderDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := created.WriteNewFile("identity.json", []byte("original")); err != nil {
			t.Fatal(err)
		}
		opened, err := OpenGeneratedBuilderDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		moved := opened.Root() + "-moved"
		if err := os.Rename(opened.Root(), moved); err != nil {
			t.Fatal(err)
		}
		if _, err := ensureSecureDirectory(filepath.Dir(opened.Root()), filepath.Base(opened.Root())); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(opened.Root(), "identity.json"), []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		replacement, err := OpenGeneratedBuilderDirectory(dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if opened.Same(replacement) {
			t.Fatal("replacement generated builder root retained the original identity")
		}
		if _, err := opened.ReadFile("identity.json", 32); err == nil {
			t.Fatal("pinned generated builder handle adopted a replacement root")
		}
		body, err := os.ReadFile(filepath.Join(moved, "identity.json"))
		if err != nil || string(body) != "original" {
			t.Fatalf("moved original builder state = %q, %v", body, err)
		}
		body, err = os.ReadFile(filepath.Join(replacement.Root(), "identity.json"))
		if err != nil || string(body) != "replacement" {
			t.Fatalf("replacement builder state = %q, %v", body, err)
		}
	})
}
