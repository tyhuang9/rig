package secretfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxSecretFileBytes = 64 << 10

// Write atomically persists a purpose-bound secret using the platform's
// current-user protection and restrictive filesystem permissions.
func Write(path, purpose string, plaintext []byte) error {
	if path == "" || purpose == "" || len(plaintext) == 0 {
		return errors.New("secret path, purpose, and value are required")
	}
	persisted, err := protect(purpose, plaintext)
	if err != nil {
		return err
	}
	defer clear(persisted)

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create secret directory: %w", err)
	}
	if err := restrictDirectory(directory); err != nil {
		return fmt.Errorf("protect secret directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".hostd-secret-*")
	if err != nil {
		return fmt.Errorf("create secret file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect secret file: %w", err)
	}
	if _, err := temporary.Write(persisted); err != nil {
		return fmt.Errorf("write secret file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync secret file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close secret file: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace secret file: %w", err)
	}
	keep = true
	return nil
}

// Read loads and decrypts a purpose-bound secret. It rejects symlinks,
// non-regular files, unsafe POSIX permissions, and unsupported formats.
func Read(path, purpose string) ([]byte, error) {
	if path == "" || purpose == "" {
		return nil, errors.New("secret path and purpose are required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secret path must be a regular file")
	}
	if err := verifyPermissions(path, info); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer file.Close()
	persisted, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	defer clear(persisted)
	if len(persisted) > maxSecretFileBytes {
		return nil, errors.New("secret file is too large")
	}
	plaintext, err := unprotect(purpose, persisted)
	if err != nil {
		return nil, err
	}
	if len(plaintext) == 0 {
		return nil, errors.New("secret file is empty")
	}
	return plaintext, nil
}

func Remove(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
