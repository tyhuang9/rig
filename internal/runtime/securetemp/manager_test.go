package securetemp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
)

func TestProtectedFilesClearInputAndCleanupExactly(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := manager.Create(uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	env := []byte("TOKEN=secret\n")
	compose := []byte(`{"services":{"app":{"environment":{"TOKEN":"secret"}}}}`)
	if err := files.WriteEnv(env); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCompose(compose); err != nil {
		t.Fatal(err)
	}
	for _, value := range append(env, compose...) {
		if value != 0 {
			t.Fatal("secret-bearing input was not cleared")
		}
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{files.Directory, files.EnvPath, files.ComposePath} {
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
	if err := files.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := files.Cleanup(); err != nil {
		t.Fatalf("idempotent cleanup: %v", err)
	}
	if _, err := os.Stat(files.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operation directory remains: %v", err)
	}
}

func TestRecoverRemovesOnlyExactOwnedOperations(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := manager.Create(uuid.NewString(), 3)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(manager.root, "do-not-remove")
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash operation remains: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unknown entry was removed: %v", err)
	}
}

func TestNewRejectsRelativeAndSymlinkedRuntimeAncestorBeforeCreation(t *testing.T) {
	if _, err := New("relative"); err == nil {
		t.Fatal("relative data root was accepted")
	}
	root := filepath.Join(t.TempDir(), "data")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "runtime")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := New(root); err == nil {
		t.Fatal("symlinked runtime ancestor was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "compose")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created outside trusted data root: %v", err)
	}
}
