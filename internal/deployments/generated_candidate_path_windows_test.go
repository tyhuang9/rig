//go:build windows

package deployments

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func candidateFactoryUnsafeDirectoryAlias(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "junction")
	output, err := exec.Command(
		"cmd.exe",
		"/d",
		"/c",
		"mklink",
		"/J",
		alias,
		target,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("create Windows reparse-point fixture: %v: %s", err, output)
	}
	info, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("junction fixture mode = %v", info.Mode())
	}
	if !generatedCandidateIsReparsePoint(alias) {
		t.Fatal("junction fixture is not reported as a reparse point")
	}
	return alias
}
