//go:build !windows

package generatedingress

import (
	"os"
	"testing"
)

func relaxWorkingDirectoryPermissionsForTest(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
