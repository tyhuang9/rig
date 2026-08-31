//go:build windows

package generatedruntime

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func isReparsePoint(path string) bool {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(pointer)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
