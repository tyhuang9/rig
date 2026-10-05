//go:build windows

package deploymenteffects

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLockHandlePreventsPathReplacement(t *testing.T) {
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
	if err := os.Rename(path, path+".replaced"); err == nil {
		t.Fatal("open lock handle permitted path replacement")
	}
	if err := validateLockFile(file, path); err != nil {
		t.Fatalf("failed replacement changed lock identity: %v", err)
	}
}
