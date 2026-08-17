package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestBootstrapTokenUsesDedicatedOutputInsteadOfStructuredLogs(t *testing.T) {
	const token = "one-time-test-token"
	var secretOutput bytes.Buffer
	var structuredLogs bytes.Buffer
	logger := newStructuredLogger(&structuredLogs, "info")

	if err := writeBootstrapToken(&secretOutput, token); err != nil {
		t.Fatal(err)
	}
	logger.Info("hostd listening", "address", "127.0.0.1:7345")

	if !strings.Contains(secretOutput.String(), token) {
		t.Fatalf("bootstrap token missing from secret output: %q", secretOutput.String())
	}
	if strings.Contains(structuredLogs.String(), token) {
		t.Fatalf("bootstrap token leaked to structured logs: %q", structuredLogs.String())
	}
	if strings.Contains(secretOutput.String(), "hostd listening") {
		t.Fatalf("ordinary log leaked to secret output: %q", secretOutput.String())
	}
	if !strings.Contains(structuredLogs.String(), `"msg":"hostd listening"`) {
		t.Fatalf("ordinary log missing from structured output: %q", structuredLogs.String())
	}
}
