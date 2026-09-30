package securetemp

import (
	"errors"
	"sync"
)

// PrivateDirectoryGuard prevents path components leading to a validated
// private directory from being replaced for the lifetime of the guard.
// Callers must close it after any temporary files have been removed.
type PrivateDirectoryGuard struct {
	close func() error
	once  sync.Once
	err   error
}

// AcquirePrivateDirectoryGuard validates path and acquires the platform guard
// needed to keep its ancestry safe while a caller creates broader-mode files.
func AcquirePrivateDirectoryGuard(path string) (*PrivateDirectoryGuard, error) {
	if err := ValidatePrivateDirectory(path); err != nil {
		return nil, err
	}
	closeGuard, err := acquirePrivateDirectoryGuard(path)
	if err != nil {
		return nil, err
	}
	guard := &PrivateDirectoryGuard{close: closeGuard}
	if err := ValidatePrivateDirectory(path); err != nil {
		_ = guard.Close()
		return nil, err
	}
	return guard, nil
}

// Close releases the ancestry guard. It is safe to call more than once.
func (g *PrivateDirectoryGuard) Close() error {
	if g == nil {
		return nil
	}
	g.once.Do(func() {
		if g.close == nil {
			g.err = errors.New("private directory guard is invalid")
			return
		}
		g.err = g.close()
	})
	return g.err
}
