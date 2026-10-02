//go:build !windows

package generatedingress

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openGatewayLockFile(path string) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(path, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == nil {
		if chmodErr := unix.Fchmod(fd, 0o600); chmodErr != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("protect generated ingress gateway lock: %w", chmodErr)
		}
	} else if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open generated ingress gateway lock: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func validateGatewayLockFile(file *os.File, path string) error {
	if err := validateGatewayLockPathIdentity(file, path); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect generated ingress gateway lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(unix.Geteuid()) || stat.Mode&0o077 != 0 {
		return errors.New("generated ingress gateway lock ownership or permissions are unsafe")
	}
	return nil
}

func tryGatewayFileLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, fmt.Errorf("acquire generated ingress gateway lock: %w", err)
}

func unlockGatewayFile(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("release generated ingress gateway lock: %w", err)
	}
	return nil
}
