package securetemp

import "testing"

func TestNewRejectsWindowsNamespaceBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()
	for _, root := range []string{`\\server\share`, `\\?\C:\hostd`, `\\.\pipe\hostd`, `\GLOBALROOT\Device\HarddiskVolume1`} {
		if _, err := New(root); err == nil {
			t.Errorf("New(%q) succeeded", root)
		}
	}
}
