//go:build windows

package controllerowner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestControllerOwnerLeasePreventsLockPathReplacement(t *testing.T) {
	directory := ownerTestDirectory(t)
	lease, err := Acquire(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	path := filepath.Join(directory, filename)
	if err := os.Rename(path, path+".replacement-attempt"); err == nil {
		t.Fatal("Windows lock handle permitted replacement while the lease was live")
	}
	if err := lease.ValidateForDirectory(directory); err != nil {
		t.Fatalf("failed replacement changed live lease: %v", err)
	}
}
