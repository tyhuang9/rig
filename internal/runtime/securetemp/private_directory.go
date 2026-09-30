package securetemp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hostd/hostd/internal/pathsecurity"
)

// ValidatePrivateDirectory verifies that path is an existing directory owned
// and accessible only by the current user. It is intended for directories
// that temporarily contain files whose modes must be broadened for a
// container user: the private parent remains the host-side access boundary.
func ValidatePrivateDirectory(path string) error {
	if path == "" || pathsecurity.RejectWindowsNamespace(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("private directory path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmtPrivateDirectoryError(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) {
		return errors.New("private directory is unsafe")
	}
	if err := validatePrivateDirectory(path, info); err != nil {
		return err
	}
	return nil
}

func fmtPrivateDirectoryError(err error) error {
	return fmt.Errorf("private directory is unavailable: %w", err)
}
