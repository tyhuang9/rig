//go:build windows

package generatedingress

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openGatewayLockFile(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, errors.New("generated ingress gateway lock path is invalid")
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open generated ingress gateway lock: %w", err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

func validateGatewayLockFile(file *os.File, path string) error {
	if err := validateGatewayLockPathIdentity(file, path); err != nil {
		return err
	}
	handle := windows.Handle(file.Fd())
	fileType, err := windows.GetFileType(handle)
	if err != nil || fileType != windows.FILE_TYPE_DISK {
		return errors.New("generated ingress gateway lock must be a disk file")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect generated ingress gateway lock: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		return errors.New("generated ingress gateway lock file is unsafe")
	}
	return nil
}

func tryGatewayFileLock(file *os.File) (bool, error) {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, fmt.Errorf("acquire generated ingress gateway lock: %w", err)
}

func unlockGatewayFile(file *os.File) error {
	if file == nil {
		return nil
	}
	overlapped := new(windows.Overlapped)
	if err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped); err != nil && !errors.Is(err, windows.ERROR_NOT_LOCKED) {
		return fmt.Errorf("release generated ingress gateway lock: %w", err)
	}
	return nil
}
