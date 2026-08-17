package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteBootstrapTokenUsesDedicatedUnstructuredConsoleFormat(t *testing.T) {
	var output bytes.Buffer
	const token = "one-time-test-token"
	if err := WriteBootstrapToken(&output, token); err != nil {
		t.Fatal(err)
	}
	line := output.String()
	if !strings.HasPrefix(line, bootstrapOutputPrefix) || !strings.Contains(line, token) {
		t.Fatalf("unexpected bootstrap console output: %q", line)
	}
	if strings.Contains(line, "bootstrap_token") || strings.HasPrefix(strings.TrimSpace(line), "{") {
		t.Fatal("bootstrap credential was formatted as structured logging")
	}
}
