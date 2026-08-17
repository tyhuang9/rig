//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNonWindowsSessionFileRemainsProtectedJSON(t *testing.T) {
	session := sessionCredentials{SessionToken: "session-secret-value", CSRFToken: "csrf-secret-value"}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := writeSessionFile(path, session); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(persisted) || !bytes.Contains(persisted, []byte(session.SessionToken)) || !bytes.Contains(persisted, []byte(session.CSRFToken)) {
		t.Fatalf("non-Windows session file no longer contains expected JSON: %q", persisted)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("session mode = %o", info.Mode().Perm())
	}
}
