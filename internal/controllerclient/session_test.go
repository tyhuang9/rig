package controllerclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedSessionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	want := Session{SessionToken: "session-secret", CSRFToken: "csrf-secret"}
	if err := WriteSessionFile(path, want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), want.SessionToken) || strings.Contains(string(raw), want.CSRFToken) {
		t.Fatal("session file contains plaintext credentials")
	}
	got, err := ReadSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("session=%+v", got)
	}
}
