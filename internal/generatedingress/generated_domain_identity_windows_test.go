//go:build windows

package generatedingress

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func generatedDomainIdentityAliasInfo(t *testing.T, target string, directory bool, name string) os.FileInfo {
	t.Helper()
	alias := filepath.Join(t.TempDir(), name)
	if directory {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	} else if err := os.Link(target, alias); err != nil {
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
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
}

func generatedDomainDockerEndpointAliases() []string {
	return []string{"", "npipe:////./pipe/docker_engine"}
}
