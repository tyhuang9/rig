//go:build !windows

package deploymenteffects

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRejectsBroadPermissions(t *testing.T) {
	t.Run("broad permissions", func(t *testing.T) {
		directory := deploymentEffectsTestDirectory(t)
		path := filepath.Join(directory, lockFilename)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if release, err := Acquire(context.Background(), directory); err == nil {
			if release != nil {
				_ = release()
			}
			t.Fatal("broad lock permissions were accepted")
		}
	})
}

func TestLockPathIdentityRejectsReplacement(t *testing.T) {
	directory := deploymentEffectsTestDirectory(t)
	path := filepath.Join(directory, lockFilename)
	file, err := openLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := validateLockFile(file, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateLockFile(file, path); err == nil {
		t.Fatal("replacement lock path retained the original file identity")
	}
}
