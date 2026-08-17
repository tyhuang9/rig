//go:build !windows

package secretfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPOSIXSecretFileUsesRestrictivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	secret := []byte("bootstrap-secret-value")
	if err := Write(path, "bootstrap", secret); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %o", info.Mode().Perm())
	}
	loaded, err := Read(path, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(loaded)
	if !bytes.Equal(loaded, secret) {
		t.Fatalf("round trip = %q", loaded)
	}
	if _, err := Read(path, "different-purpose"); err == nil {
		t.Fatal("wrong purpose was accepted")
	}
}

func TestPOSIXSecretFileRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	if err := Write(path, "bootstrap", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, "bootstrap"); err == nil {
		t.Fatal("broad permissions were accepted")
	}
}
