package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const gatewayLockFilename = "gateway.lock"

// acquireGatewayOSLock serializes generated-gateway observation and mutation
// across hostd processes. The persistent lock file is deliberately empty and
// is never unlinked: the held file handle, rather than file contents, owns the
// lock.
func acquireGatewayOSLock(ctx context.Context, store *stateStore) (func() error, error) {
	if ctx == nil {
		return nil, errors.New("generated ingress gateway lock context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || store.root == "" || filepath.Dir(store.path) != store.root {
		return nil, errors.New("generated ingress gateway lock store is invalid")
	}

	rootBefore, err := store.directoryIdentity()
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(store.root, gatewayLockFilename)
	if filepath.Dir(lockPath) != store.root || filepath.Clean(lockPath) != lockPath {
		return nil, errors.New("generated ingress gateway lock path is invalid")
	}

	file, err := openGatewayLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = file.Close()
		}
	}()

	if err := validateGatewayLockFile(file, lockPath); err != nil {
		return nil, err
	}
	if err := store.sameDirectory(rootBefore); err != nil {
		return nil, errors.New("generated ingress gateway lock directory changed")
	}

	for {
		locked, lockErr := tryGatewayFileLock(file)
		if lockErr != nil {
			return nil, lockErr
		}
		if locked {
			break
		}

		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			_ = timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	if err := validateGatewayLockFile(file, lockPath); err != nil {
		_ = unlockGatewayFile(file)
		return nil, err
	}
	if err := store.sameDirectory(rootBefore); err != nil {
		_ = unlockGatewayFile(file)
		return nil, errors.New("generated ingress gateway lock directory changed")
	}
	if err := ctx.Err(); err != nil {
		_ = unlockGatewayFile(file)
		return nil, err
	}

	keepOpen = true
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			releaseErr = errors.Join(unlockGatewayFile(file), file.Close())
		})
		return releaseErr
	}, nil
}

func validateGatewayLockPathIdentity(file *os.File, path string) error {
	if file == nil {
		return errors.New("generated ingress gateway lock handle is invalid")
	}
	handleInfo, err := file.Stat()
	if err != nil || !handleInfo.Mode().IsRegular() {
		return errors.New("generated ingress gateway lock must be a regular file")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(path) {
		return errors.New("generated ingress gateway lock path is unsafe")
	}
	if !os.SameFile(handleInfo, pathInfo) {
		return errors.New("generated ingress gateway lock file changed")
	}
	return nil
}
