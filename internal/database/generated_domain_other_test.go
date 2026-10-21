//go:build !linux && !windows

package database

import (
	"path/filepath"
	"testing"
)

func TestOpenForGeneratedDomainFailsClosedOnUnsupportedPlatform(t *testing.T) {
	if file, _, err := openGeneratedDomainDatabaseFile(filepath.Join(t.TempDir(), "control.db"), true); file != nil || err == nil {
		t.Fatalf("unsupported platform file capture = %v, %v", file, err)
	}
}
