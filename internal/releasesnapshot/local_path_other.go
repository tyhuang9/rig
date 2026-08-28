//go:build !windows

package releasesnapshot

import "path/filepath"

func localPathIsReparsePoint(string) bool { return false }

func sameFilesystemPath(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}
