//go:build linux

package database

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openGeneratedDomainDatabaseFile(path string, create bool) (*os.File, bool, error) {
	identityFlags := unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(path, identityFlags, 0)
	created := false
	if errors.Is(err, unix.ENOENT) && create {
		createdFD, createErr := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		err = createErr
		created = err == nil
		if err == nil {
			if err = unix.Fchmod(createdFD, 0o600); err == nil {
				err = unix.Close(createdFD)
			} else {
				_ = unix.Close(createdFD)
			}
			if err == nil {
				fd, err = unix.Open(path, identityFlags, 0)
			}
		} else if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Open(path, identityFlags, 0)
			created = false
		}
	}
	if err != nil {
		return nil, false, errors.New("generated domain database file is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, errors.New("generated domain database file handle is unavailable")
	}
	return file, created, nil
}

func validateGeneratedDomainDatabaseFile(file *os.File, path string) (os.FileInfo, error) {
	if file == nil || file.Name() != path {
		return nil, errors.New("generated domain database file handle is invalid")
	}
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil ||
		status.Mode&unix.S_IFMT != unix.S_IFREG || status.Nlink != 1 ||
		status.Uid != uint32(unix.Geteuid()) || status.Mode&0o600 != 0o600 || status.Mode&0o022 != 0 {
		return nil, errors.New("generated domain database file ownership is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("generated domain database file handle is invalid")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) || opened.Mode() != current.Mode() {
		return nil, errors.New("generated domain database file path changed")
	}
	return opened, nil
}
