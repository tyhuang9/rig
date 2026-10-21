//go:build windows

package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedDomainCapturePreventsWindowsDatabaseReplacement(t *testing.T) {
	dataRoot, lease := newGeneratedDomainDatabaseBoundary(t)
	capture, err := captureGeneratedDomainDatabase(dataRoot, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.close() })
	if err := os.Rename(capture.databasePath, capture.databasePath+".replacement-attempt"); err == nil {
		t.Fatal("retained Windows database handle permitted capture-window replacement")
	}
	if _, err := capture.recheck(); err != nil {
		t.Fatalf("failed replacement changed capture: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, generatedDomainDatabaseFilename)); err != nil {
		t.Fatal(err)
	}
}
