package sourceconnections

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileCredentialStorePurposeBindsAndRemovesCredentials(t *testing.T) {
	store := NewFileCredentialStore(t.TempDir())
	id := "11111111111111111111111111111111"
	otherID := "22222222222222222222222222222222"
	if err := store.WriteDevice(id, "device-sensitive"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadDevice(id); err != nil || got != "device-sensitive" {
		t.Fatalf("ReadDevice = %q, %v", got, err)
	}
	raw, err := os.ReadFile(store.devicePath(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.devicePath(otherID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.devicePath(otherID), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadDevice(otherID); err == nil {
		t.Fatal("purpose-mismatched device credential was accepted")
	}

	now := time.Now().UTC()
	bundle := TokenBundle{Version: 1, Generation: 1, AccessToken: "access-sensitive", RefreshToken: "refresh-sensitive", AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(24 * time.Hour), ProviderUserID: "42", ProviderLogin: "octo"}
	if err := store.WriteBundle(id, bundle); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadBundle(id)
	if err != nil || got.Generation != 1 || got.AccessToken != bundle.AccessToken {
		t.Fatalf("ReadBundle = %#v, %v", got, err)
	}
	if rendered := bundle.String() + bundle.GoString(); strings.Contains(rendered, "sensitive") {
		t.Fatalf("bundle formatting exposed credentials: %s", rendered)
	}
	if err := store.RemoveDevice(id); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBundle(id); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBundle(id); err != nil {
		t.Fatalf("idempotent RemoveBundle: %v", err)
	}
}

func TestFileCredentialStoreRejectsUnsupportedBundleVersion(t *testing.T) {
	store := NewFileCredentialStore(t.TempDir())
	id := "33333333333333333333333333333333"
	now := time.Now().UTC()
	bundle := TokenBundle{Version: 2, Generation: 1, AccessToken: "access", RefreshToken: "refresh", AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(24 * time.Hour), ProviderUserID: "42", ProviderLogin: "octo"}
	if err := store.WriteBundle(id, bundle); err == nil {
		t.Fatal("unsupported bundle version was written")
	}
}
