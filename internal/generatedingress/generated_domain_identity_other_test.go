//go:build !windows

package generatedingress

import (
	"os"
	"path/filepath"
	"testing"
)

func generatedDomainIdentityAliasInfo(t *testing.T, target string, _ bool, name string) os.FileInfo {
	t.Helper()
	alias := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func generatedDomainDirectoryAlias(t *testing.T, target, name string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), name)
	generatedDomainCreateDirectoryAlias(t, target, alias)
	return alias
}

func generatedDomainCreateDirectoryAlias(t *testing.T, target, alias string) {
	t.Helper()
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
}

func generatedDomainDockerEndpointAliases() []string {
	return []string{"", "unix:///var/run/docker.sock"}
}
