//go:build windows

package config

import (
	"errors"

	"golang.org/x/sys/windows"
)

func verifyPlatformFile(path string) error {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errors.New("invalid file path")
	}
	attributes, err := windows.GetFileAttributes(path16)
	if err != nil {
		return err
	}
	if !safeWindowsAttributes(attributes) {
		return errors.New("reparse-point files are not accepted")
	}
	return nil
}

func safeWindowsAttributes(attributes uint32) bool {
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
