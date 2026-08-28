//go:build windows

package securetemp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentUserSecurityAttributes() (*windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}, nil
}

func secureMkdir(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := currentUserSecurityAttributes()
	if err != nil {
		return err
	}
	return windows.CreateDirectory(name, attributes)
}

func secureOpenNew(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes, err := currentUserSecurityAttributes()
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func isReparsePoint(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(name)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func rejectReparseAncestors(root, target string) error {
	current := root
	if unsafeDirectory(current) {
		return errors.New("unsafe runtime temp ancestor")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || hasParentPrefix(relative) {
		return errors.New("runtime temp path escaped root")
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if unsafeDirectory(current) {
			return errors.New("unsafe runtime temp ancestor")
		}
	}
	return nil
}

func unsafeDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isReparsePoint(path)
}
