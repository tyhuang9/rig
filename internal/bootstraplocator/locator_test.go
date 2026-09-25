package bootstraplocator

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/secretfile"
)

func TestDefaultStoreUsesUserConfigDirectory(t *testing.T) {
	config, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("user config directory unavailable: %v", err)
	}
	store := DefaultStore()
	if want := filepath.Join(config, "hostd", "bootstrap-locators"); store.Directory != want {
		t.Fatalf("directory = %q, want %q", store.Directory, want)
	}
}

func TestRegisterAndReadTokenWithoutDataRootArgument(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
	root := t.TempDir()
	writeToken(t, root)
	cleanup, err := store.Register(root, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	token, err := store.ReadToken()
	if err != nil || string(token) != "example-bootstrap-token" {
		t.Fatalf("ReadToken() returned expected token: %v", err)
	}
	clear(token)
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
	if _, err := store.ReadToken(); err == nil || !strings.Contains(err.Error(), "no active") {
		t.Fatalf("ReadToken() after cleanup = %v", err)
	}
}

func TestReadTokenRejectsAmbiguousLiveRoots(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
	for i := 0; i < 2; i++ {
		root := t.TempDir()
		writeToken(t, root)
		cleanup, err := store.Register(root, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
	}
	if token, err := store.ReadToken(); err == nil || token != nil || !strings.Contains(err.Error(), "multiple active") {
		t.Fatalf("ReadToken() = %v, %v; want ambiguity", token, err)
	}
}

func TestReadTokenDeduplicatesRegistrationsForOneRoot(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
	root := t.TempDir()
	writeToken(t, root)
	for i := 0; i < 2; i++ {
		cleanup, err := store.Register(root, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
	}
	if token, err := store.ReadToken(); err != nil || string(token) != "example-bootstrap-token" {
		t.Fatalf("ReadToken() returned expected token: %v", err)
	}
}

func TestReadTokenIgnoresExpiredAndMissingTokens(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := Store{Directory: filepath.Join(t.TempDir(), "locators"), Now: func() time.Time { return now }}
	root := t.TempDir()
	writeToken(t, root)
	cleanup, err := store.Register(root, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	now = now.Add(2 * time.Minute)
	if _, err := store.ReadToken(); err == nil || !strings.Contains(err.Error(), "no active") {
		t.Fatalf("expired ReadToken() = %v", err)
	}
	now = now.Add(-2 * time.Minute)
	if err := secretfile.Remove(filepath.Join(root, tokenFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadToken(); err == nil || !strings.Contains(err.Error(), "no active") {
		t.Fatalf("missing token ReadToken() = %v", err)
	}
}

func TestReadTokenRejectsMalformedAndUnsafeLocators(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		purpose string
	}{
		{"malformed JSON", []byte("{"), locatorPurpose},
		{"trailing JSON", []byte(`{"version":1,"dataRoot":"/tmp","expiresAt":123} true`), locatorPurpose},
		{"relative root", []byte(`{"version":1,"dataRoot":"../elsewhere","expiresAt":123}`), locatorPurpose},
		{"wrong purpose", []byte(`{"version":1,"dataRoot":"/tmp","expiresAt":123}`), "different-purpose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
			path := filepath.Join(store.Directory, "locator-0123456789abcdef0123456789abcdef.secret")
			if err := secretfile.WriteNew(path, tc.purpose, tc.payload); err != nil {
				t.Fatal(err)
			}
			if token, err := store.ReadToken(); err == nil || token != nil {
				t.Fatalf("ReadToken() = %v, %v; want rejection", token, err)
			}
		})
	}
}

func TestReadTokenRejectsUnprotectedTokenAndUnsafeEntries(t *testing.T) {
	t.Run("unprotected token", func(t *testing.T) {
		store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, tokenFilename), []byte("plaintext-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		cleanup, err := store.Register(root, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
		if token, err := store.ReadToken(); err == nil || token != nil {
			t.Fatalf("ReadToken() = %v, %v; want rejection", token, err)
		}
	})
	t.Run("unexpected entry", func(t *testing.T) {
		store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
		if err := os.MkdirAll(store.Directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.Directory, "unexpected"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadToken(); err == nil {
			t.Fatal("accepted unexpected locator entry")
		}
	})
	t.Run("symlinked directory", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "locators")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := (Store{Directory: link}).ReadToken(); err == nil {
			t.Fatal("accepted symlinked locator directory")
		}
	})
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
	if _, err := store.Register(t.TempDir(), 0); err == nil {
		t.Fatal("accepted nonpositive lifetime")
	}
	if _, err := store.Register(filepath.Join(t.TempDir(), "absent"), time.Minute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Register(missing root) = %v", err)
	}
}

func TestRegisterTimerRemovesOnlyItsOwnLocator(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
	root := t.TempDir()
	writeToken(t, root)
	cleanup, err := store.Register(root, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	deadline := time.After(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, err := os.ReadDir(store.Directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			if _, err := os.Stat(filepath.Join(root, tokenFilename)); err != nil {
				t.Fatalf("timer removed the token file: %v", err)
			}
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("locator was not removed after its lifetime")
		}
	}
}

func TestReadTokenRejectsBroadLocatorDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are not enforced on Windows")
	}
	store := Store{Directory: filepath.Join(t.TempDir(), "locators")}
	if err := os.Mkdir(store.Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadToken(); err == nil {
		t.Fatal("accepted broadly accessible locator directory")
	}
}

func writeToken(t *testing.T, root string) {
	t.Helper()
	if err := secretfile.WriteNew(filepath.Join(root, tokenFilename), auth.BootstrapSecretPurpose, []byte("example-bootstrap-token")); err != nil {
		t.Fatal(err)
	}
}
