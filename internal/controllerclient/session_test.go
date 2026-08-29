package controllerclient

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProtectedSessionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	want := Session{SessionToken: "session-secret", CSRFToken: "csrf-secret"}
	if err := WriteSessionFile(path, want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), want.SessionToken) || strings.Contains(string(raw), want.CSRFToken) {
			t.Fatal("session file contains plaintext credentials")
		}
	} else {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("session file mode = %v, want a regular file", info.Mode())
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("session file permissions = %04o, want 0600", info.Mode().Perm())
		}
	}
	got, err := ReadSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("session=%+v", got)
	}
}
