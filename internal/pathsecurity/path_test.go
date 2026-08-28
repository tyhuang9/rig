package pathsecurity

import "testing"

func TestRejectWindowsNamespaceIsHostIndependent(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		`\\server\share\compose.yaml`,
		`//server/share/compose.yaml`,
		`\\?\C:\workspace\compose.yaml`,
		`//?/C:/workspace/compose.yaml`,
		`\\.\C:\workspace\compose.yaml`,
		`\\.\pipe\docker_engine`,
		`npipe:////./pipe/docker_engine`,
		`\??\C:\workspace\compose.yaml`,
		`\Device\HarddiskVolume1\workspace`,
		`\GLOBALROOT\Device\HarddiskVolumeShadowCopy1`,
	} {
		if !RejectWindowsNamespace(path) {
			t.Errorf("RejectWindowsNamespace(%q) = false", path)
		}
	}

	for _, path := range []string{
		`C:\workspace\compose.yaml`,
		`workspace\compose.yaml`,
		`/var/lib/hostd/workspace/compose.yaml`,
		`workspace/compose.yaml`,
	} {
		if RejectWindowsNamespace(path) {
			t.Errorf("RejectWindowsNamespace(%q) = true", path)
		}
	}
}

func TestRejectWindowsNamespaceRejectsNUL(t *testing.T) {
	t.Parallel()
	if !RejectWindowsNamespace("workspace\x00compose.yaml") {
		t.Fatal("NUL path accepted")
	}
}
