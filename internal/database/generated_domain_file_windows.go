//go:build windows

package database

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openGeneratedDomainDatabaseFile(path string, create bool) (*os.File, bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, errors.New("generated domain database file path is malformed")
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	created := false
	if (errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)) && create {
		handle, err = windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		created = err == nil
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			handle, err = windows.CreateFile(
				name,
				windows.GENERIC_READ|windows.GENERIC_WRITE,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
				nil,
				windows.OPEN_EXISTING,
				windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
				0,
			)
			created = false
		}
	}
	if err != nil {
		return nil, false, errors.New("generated domain database file is unavailable")
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, false, errors.New("generated domain database file handle is unavailable")
	}
	return file, created, nil
}

func validateGeneratedDomainDatabaseFile(file *os.File, path string) (os.FileInfo, error) {
	if file == nil || file.Name() != path {
		return nil, errors.New("generated domain database file handle is invalid")
	}
	handle := windows.Handle(file.Fd())
	var status windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &status); err != nil ||
		status.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		status.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		status.NumberOfLinks != 1 {
		return nil, errors.New("generated domain database file is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("generated domain database file handle is invalid")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		return nil, errors.New("generated domain database file path changed")
	}
	attributes, err := windows.GetFileAttributes(nameForGeneratedDomainFile(path))
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil, errors.New("generated domain database file is unsafe")
	}
	return opened, nil
}

func nameForGeneratedDomainFile(path string) *uint16 {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	return name
}
