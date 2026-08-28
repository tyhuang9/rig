//go:build !windows

package securetemp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func secureMkdir(path string) error { return os.Mkdir(path, 0o700) }

func secureOpenNew(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func isReparsePoint(string) bool { return false }

func rejectReparseAncestors(root, target string) error {
	current := root
	info, err := os.Lstat(current)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe runtime temp ancestor")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || hasParentPrefix(relative) {
		return errors.New("runtime temp path escaped root")
	}
	for _, part := range splitPath(relative) {
		current = filepath.Join(current, part)
		info, err = os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe runtime temp ancestor")
		}
	}
	return nil
}

func splitPath(value string) []string {
	if value == "" || value == "." {
		return nil
	}
	return strings.Split(value, string(filepath.Separator))
}
