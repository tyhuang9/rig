package auth

import (
	"fmt"
	"io"
)

// WriteBootstrapTokenPath writes only the path to a protected local file.
// Callers must never route the credential itself through process output or logs.
func WriteBootstrapTokenPath(w io.Writer, path string) error {
	_, err := fmt.Fprintln(w, path)
	return err
}
