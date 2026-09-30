package generatedingress

import (
	"errors"
	"os"
	"path/filepath"
)

// fenceLegacyV1Locked rejects every legacy operation once any v2 migration artifact
// exists. The caller must hold the gateway lock so marker creation and legacy
// state or Docker work cannot cross this check.
func (m *Manager) fenceLegacyV1Locked() error {
	if m == nil || m.store == nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	before, err := m.store.directoryIdentity()
	if err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}

	blocked := false
	for _, name := range []string{v2RouteStateFilename, gatewayMigrationFilename} {
		path := filepath.Join(m.store.root, name)
		if filepath.Dir(path) != m.store.root || filepath.Clean(path) != path {
			blocked = true
			continue
		}
		// Presence alone transfers ownership away from the v1 paths. Do not
		// parse through corruption or accept an orphaned migration artifact.
		if _, err := os.Lstat(path); err == nil {
			blocked = true
		} else if !errors.Is(err, os.ErrNotExist) {
			blocked = true
		}
	}
	if err := m.store.sameDirectory(before); err != nil {
		blocked = true
	}
	if blocked {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}
