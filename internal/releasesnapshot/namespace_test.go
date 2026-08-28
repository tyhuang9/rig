package releasesnapshot

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestReleasePathsRejectWindowsNamespacesBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()
	app := uuid.NewString()
	if _, err := managedPath(`\\server\share`, app, strings.Repeat("a", 32), "releases"); err == nil {
		t.Fatal("UNC managed root accepted")
	}
	if err := ValidateComposeWorkspace(t.TempDir(), `\\?\C:\compose.yaml`); !IsCode(err, "invalid_source") {
		t.Fatalf("extended namespace compose error=%v", err)
	}
}
