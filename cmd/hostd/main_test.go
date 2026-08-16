package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupRejectsNonLoopbackBeforeDataRootCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	if _, err := startupConfig([]string{"--listen", "0.0.0.0:7345", "--data-root", root}); err == nil {
		t.Fatal("non-loopback startup configuration was accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("data root was touched before listen validation: %v", err)
	}
}
