package sourceinspection

import "testing"

func TestInspectLocalRejectsWindowsNamespaceBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()
	for _, path := range []string{`\\server\share`, `\\?\C:\workspace`, `\\.\pipe\source`, `\GLOBALROOT\Device\HarddiskVolume1`} {
		if _, err := InspectLocal(path); !IsCode(err, "invalid_source") {
			t.Errorf("InspectLocal(%q) error=%v", path, err)
		}
	}
}
