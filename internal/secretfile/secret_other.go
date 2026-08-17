//go:build !windows

package secretfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

var plainPrefix = []byte("hostd-secret-v1\x00")

func protect(purpose string, plaintext []byte) ([]byte, error) {
	persisted := make([]byte, 0, len(plainPrefix)+len(purpose)+1+len(plaintext))
	persisted = append(persisted, plainPrefix...)
	persisted = append(persisted, purpose...)
	persisted = append(persisted, 0)
	persisted = append(persisted, plaintext...)
	return persisted, nil
}

func unprotect(purpose string, persisted []byte) ([]byte, error) {
	prefix := append(append([]byte(nil), plainPrefix...), purpose...)
	prefix = append(prefix, 0)
	defer clear(prefix)
	if !bytes.HasPrefix(persisted, prefix) || len(persisted) == len(prefix) {
		return nil, errors.New("secret file format or purpose does not match")
	}
	return append([]byte(nil), persisted[len(prefix):]...), nil
}

func replaceFile(source, destination string) error { return os.Rename(source, destination) }

func restrictDirectory(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}

func verifyPermissions(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secret file permissions are too broad: %o", info.Mode().Perm())
	}
	return nil
}
