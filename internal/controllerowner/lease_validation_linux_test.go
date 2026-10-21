//go:build linux

package controllerowner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerOwnerLeaseRejectsReplacedLockPath(t *testing.T) {
	directory := ownerTestDirectory(t)
	lease, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	path := filepath.Join(directory, filename)
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.ValidateForDirectory(directory); err == nil {
		t.Fatal("lease validated after the persistent lock path was replaced")
	}
}
