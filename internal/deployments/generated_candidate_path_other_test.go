//go:build !windows

package deployments

import (
	"os"
	"path/filepath"
	"testing"
)

func candidateFactoryUnsafeDirectoryAlias(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "symlink")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("create directory symlink fixture: %v", err)
	}
	info, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink fixture mode = %v", info.Mode())
	}
	return alias
}
