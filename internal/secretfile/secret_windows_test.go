//go:build windows

package secretfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSecretFileUsesCurrentUserDPAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	secret := []byte("bootstrap-secret-value")
	if err := Write(path, "bootstrap", secret); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(persisted, windowsPrefix) || bytes.Contains(persisted, secret) {
		t.Fatalf("secret file was not DPAPI protected: %q", persisted)
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

func TestWindowsSecretFileRejectsPlaintextAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.bin")
	for _, value := range [][]byte{[]byte("plaintext"), append(append([]byte(nil), windowsPrefix...), []byte("corrupt")...)} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path, "bootstrap"); err == nil {
			t.Fatalf("unsafe value was accepted: %q", value)
		}
	}
}
