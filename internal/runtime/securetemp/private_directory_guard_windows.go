//go:build windows

package securetemp

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func acquirePrivateDirectoryGuard(path string) (func() error, error) {
	var handles []windows.Handle
	closeHandles := func() error {
		var closeErr error
		for index := len(handles) - 1; index >= 0; index-- {
			if err := windows.CloseHandle(handles[index]); err != nil {
				closeErr = errors.Join(closeErr, err)
			}
		}
		handles = nil
		return closeErr
	}
	for current := path; ; current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			_ = closeHandles()
			return nil, errors.New("private directory ancestor path is malformed")
		}
		handle, err := windows.CreateFile(
			name,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err != nil {
			_ = closeHandles()
			return nil, fmtPrivateDirectoryError(err)
		}
		handles = append(handles, handle)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
			info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
			info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			_ = closeHandles()
			return nil, errors.New("private directory ancestor is unsafe")
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return closeHandles, nil
}
