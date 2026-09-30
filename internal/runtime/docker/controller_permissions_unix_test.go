//go:build !windows

package docker

import (
	"os"
	"testing"
)

func createPermissiveDirectoryForTest(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
