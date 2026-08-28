//go:build windows

package releasesnapshot

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const maxWindowsLongPathCodeUnits = 32768

func localPathIsReparsePoint(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(name)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// sameFilesystemPath compares the resolved path with the original spelling
// without mistaking a legitimate Windows 8.3 short-name alias for a reparse
// point. GetLongPathName expands short components but does not resolve links;
// EvalSymlinks, performed by the caller, still exposes link and junction
// traversal as a different canonical path.
func sameFilesystemPath(first, second string) bool {
	first, err := windowsLongPath(first)
	if err != nil {
		return false
	}
	second, err = windowsLongPath(second)
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}

func windowsLongPath(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	required, err := windows.GetLongPathName(name, nil, 0)
	if err != nil {
		return "", err
	}
	if required == 0 || required > maxWindowsLongPathCodeUnits {
		return "", windows.ERROR_BUFFER_OVERFLOW
	}
	buffer := make([]uint16, required)
	written, err := windows.GetLongPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	if written == 0 || written >= uint32(len(buffer)) {
		return "", windows.ERROR_BUFFER_OVERFLOW
	}
	return windows.UTF16ToString(buffer[:written]), nil
}
