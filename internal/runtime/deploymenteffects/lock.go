package deploymenteffects

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const (
	lockFilename      = "deployment-effects.lock"
	lockRetryInterval = 20 * time.Millisecond
)

var acquireTryLockFile = tryLockFile

// Acquire serializes deployment effects across processes that share one
// private controller working directory. The persistent empty lock file is
// never unlinked: the held file handle, rather than the pathname, owns the
// lock.
func Acquire(ctx context.Context, workingDirectory string) (func() error, error) {
	if ctx == nil {
		return nil, errors.New("deployment effects lock context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	guard, err := securetemp.AcquirePrivateDirectoryGuard(workingDirectory)
	if err != nil {
		return nil, err
	}
	keepGuard := false
	defer func() {
		if !keepGuard {
			_ = guard.Close()
		}
	}()
	directoryBefore, err := lockDirectoryIdentity(workingDirectory)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(workingDirectory, lockFilename)
	if filepath.Dir(path) != workingDirectory || filepath.Clean(path) != path {
		return nil, errors.New("deployment effects lock path is invalid")
	}
	file, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	keepFile := false
	defer func() {
		if !keepFile {
			_ = file.Close()
		}
	}()
	if err := validateLockFile(file, path); err != nil {
		return nil, err
	}
	if err := sameLockDirectory(workingDirectory, directoryBefore); err != nil {
		return nil, err
	}

	for {
		locked, lockErr := acquireTryLockFile(file)
		if lockErr != nil {
			return nil, lockErr
		}
		if locked {
			break
		}
		timer := time.NewTimer(lockRetryInterval)
		select {
		case <-ctx.Done():
			_ = timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	if err := validateLockFile(file, path); err != nil {
		_ = unlockFile(file)
		return nil, err
	}
	if err := sameLockDirectory(workingDirectory, directoryBefore); err != nil {
		_ = unlockFile(file)
		return nil, err
	}
	if err := securetemp.ValidatePrivateDirectory(workingDirectory); err != nil {
		_ = unlockFile(file)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = unlockFile(file)
		return nil, err
	}

	keepFile = true
	keepGuard = true
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			releaseErr = errors.Join(unlockFile(file), file.Close(), guard.Close())
		})
		return releaseErr
	}, nil
}

func lockDirectoryIdentity(path string) (os.FileInfo, error) {
	if err := securetemp.ValidatePrivateDirectory(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || lockPathIsReparsePoint(path) {
		return nil, errors.New("deployment effects lock directory is unsafe")
	}
	return info, nil
}

func sameLockDirectory(path string, before os.FileInfo) error {
	after, err := lockDirectoryIdentity(path)
	if err != nil || before == nil || !os.SameFile(before, after) {
		return errors.New("deployment effects lock directory changed")
	}
	return nil
}

func validateLockPathIdentity(file *os.File, path string) error {
	if file == nil {
		return errors.New("deployment effects lock handle is invalid")
	}
	handleInfo, err := file.Stat()
	if err != nil || !handleInfo.Mode().IsRegular() || handleInfo.Size() != 0 {
		return errors.New("deployment effects lock must be an empty regular file")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		lockPathIsReparsePoint(path) || !os.SameFile(handleInfo, pathInfo) {
		return errors.New("deployment effects lock path changed or is unsafe")
	}
	return nil
}
