//go:build !windows

package controllerowner

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openLockFile(path string) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(path, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == nil {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("protect controller owner lock: %w", err)
		}
	} else if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open controller owner lock: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func validateLockFile(file *os.File, path string) error {
	if err := validateLockPathIdentity(file, path); err != nil {
		return err
	}
	var info unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &info); err != nil {
		return fmt.Errorf("inspect controller owner lock: %w", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 ||
		info.Uid != uint32(unix.Geteuid()) || info.Mode&0o077 != 0 {
		return errors.New("controller owner lock ownership or permissions are unsafe")
	}
	return nil
}

func tryLockFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, fmt.Errorf("acquire controller owner lock: %w", err)
}

func unlockFile(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("release controller owner lock: %w", err)
	}
	return nil
}
