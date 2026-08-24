//go:build windows

package config

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestSafeWindowsAttributesRejectsEveryReparsePoint(t *testing.T) {
	if safeWindowsAttributes(windows.FILE_ATTRIBUTE_REPARSE_POINT) {
		t.Fatal("bare reparse point accepted")
	}
	if safeWindowsAttributes(windows.FILE_ATTRIBUTE_ARCHIVE | windows.FILE_ATTRIBUTE_REPARSE_POINT) {
		t.Fatal("file reparse point accepted")
	}
	if !safeWindowsAttributes(windows.FILE_ATTRIBUTE_ARCHIVE) {
		t.Fatal("ordinary file rejected")
	}
}
