//go:build windows

package securetemp

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const privateDirectoryFullAccess windows.ACCESS_MASK = 0x001f01ff

func validatePrivateDirectory(path string, expected os.FileInfo) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errors.New("private directory path is malformed")
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmtPrivateDirectoryError(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		windows.CloseHandle(handle)
		return errors.New("private directory handle is unavailable")
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(expected, opened) {
		return errors.New("private directory changed during validation")
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil ||
		handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("private directory is unsafe")
	}

	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.New("private directory security descriptor is invalid")
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || currentUser == nil || currentUser.User.Sid == nil || !currentUser.User.Sid.IsValid() {
		return errors.New("current user SID is unavailable")
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil || owner == nil || ownerDefaulted || !owner.Equals(currentUser.User.Sid) {
		return errors.New("private directory is not owned by the current user")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private directory DACL is absent or permits inheritance")
	}
	dacl, daclDefaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || daclDefaulted || dacl.AceCount != 1 {
		return errors.New("private directory DACL is not restricted to one explicit entry")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil ||
		ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 ||
		ace.Mask != privateDirectoryFullAccess {
		return errors.New("private directory DACL entry is invalid")
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || !trustee.IsValid() || !trustee.Equals(currentUser.User.Sid) {
		return errors.New("private directory DACL grants access to another identity")
	}

	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) || !os.SameFile(opened, current) {
		return errors.New("private directory changed during validation")
	}
	return nil
}
