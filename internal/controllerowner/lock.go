package controllerowner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const filename = "controller-owner.lock"

var ErrAlreadyOwned = errors.New("controller data root is already owned by another process")

// Lease holds the controller's process-lifetime owner lock. The lock file is
// deliberately persistent and empty: removing it would allow a second inode
// to acquire a different lock for the same directory.
type Lease struct {
	file  *os.File
	guard *securetemp.PrivateDirectoryGuard
	once  sync.Once
	err   error
}

// Acquire takes the owner lock in an existing private controller working
// directory. Callers must acquire it before opening the database or starting
// any controller recovery work, and hold it until the process has stopped.
// Contention fails immediately rather than waiting behind another controller.
func Acquire(ctx context.Context, privateDirectory string) (*Lease, error) {
	if ctx == nil {
		return nil, errors.New("controller owner lock context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	guard, err := securetemp.AcquirePrivateDirectoryGuard(privateDirectory)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(privateDirectory, filename)
	if filepath.Dir(path) != privateDirectory || filepath.Clean(path) != path {
		_ = guard.Close()
		return nil, errors.New("controller owner lock path is invalid")
	}
	file, err := openLockFile(path)
	if err != nil {
		_ = guard.Close()
		return nil, err
	}
	cleanup := func() {
		_ = file.Close()
		_ = guard.Close()
	}
	if err := validateLockFile(file, path); err != nil {
		cleanup()
		return nil, err
	}
	if err := securetemp.ValidatePrivateDirectory(privateDirectory); err != nil {
		cleanup()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, err
	}
	locked, err := tryLockFile(file)
	if err != nil {
		cleanup()
		return nil, err
	}
	if !locked {
		cleanup()
		return nil, ErrAlreadyOwned
	}
	if err := validateLockFile(file, path); err != nil {
		_ = unlockFile(file)
		cleanup()
		return nil, err
	}
	if err := securetemp.ValidatePrivateDirectory(privateDirectory); err != nil {
		_ = unlockFile(file)
		cleanup()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = unlockFile(file)
		cleanup()
		return nil, err
	}
	return &Lease{file: file, guard: guard}, nil
}

func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file == nil || l.guard == nil {
			l.err = errors.New("controller owner lease is invalid")
			return
		}
		l.err = errors.Join(unlockFile(l.file), l.file.Close(), l.guard.Close())
	})
	return l.err
}

func validateLockPathIdentity(file *os.File, path string) error {
	if file == nil {
		return errors.New("controller owner lock handle is invalid")
	}
	handleInfo, err := file.Stat()
	if err != nil || !handleInfo.Mode().IsRegular() || handleInfo.Size() != 0 {
		return errors.New("controller owner lock must be an empty regular file")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(handleInfo, pathInfo) {
		return errors.New("controller owner lock path changed or is unsafe")
	}
	return nil
}
