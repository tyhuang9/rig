//go:build windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSessionFileUsesCurrentUserDPAPI(t *testing.T) {
	session := sessionCredentials{SessionToken: "session-secret-value", CSRFToken: "csrf-secret-value"}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := writeSessionFile(path, session); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(persisted, windowsSessionFilePrefix) {
		t.Fatalf("session file is missing DPAPI format prefix: %q", persisted)
	}
	if bytes.Contains(persisted, []byte(session.SessionToken)) || bytes.Contains(persisted, []byte(session.CSRFToken)) {
		t.Fatalf("session file contains plaintext credentials: %q", persisted)
	}
	loaded, err := (&commandApp{sessionFile: path}).loadSession()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != session {
		t.Fatalf("session round trip = %#v, want %#v", loaded, session)
	}
}

func TestWindowsSessionFileRejectsCorruptOrPlaintextData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	for _, data := range [][]byte{
		[]byte(`{"sessionToken":"plaintext","csrfToken":"plaintext"}`),
		append(append([]byte(nil), windowsSessionFilePrefix...), []byte("corrupt")...),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&commandApp{sessionFile: path}).loadSession(); err == nil {
			t.Fatalf("corrupt or plaintext session data was accepted: %q", data)
		}
	}
}
