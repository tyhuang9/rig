package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteBootstrapTokenPathDoesNotExposeCredential(t *testing.T) {
	var output bytes.Buffer
	const token = "one-time-test-token"
	const path = "C:/protected/bootstrap-token.secret"
	if err := WriteBootstrapTokenPath(&output, path); err != nil {
		t.Fatal(err)
	}
	line := output.String()
	if line != path+"\n" {
		t.Fatalf("unexpected bootstrap console output: %q", line)
	}
	if strings.Contains(line, token) || strings.HasPrefix(strings.TrimSpace(line), "{") {
		t.Fatal("bootstrap credential was exposed in process output")
	}
}
