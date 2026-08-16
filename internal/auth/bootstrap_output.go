package auth

import (
	"fmt"
	"io"
)

const bootstrapOutputPrefix = "HOSTD BOOTSTRAP TOKEN (sensitive, one-time, expires in 15 minutes): "

// WriteBootstrapToken writes only to the protected local console. Callers must
// never route this credential through structured, request, or audit logging.
func WriteBootstrapToken(w io.Writer, token string) error {
	_, err := fmt.Fprintln(w, bootstrapOutputPrefix+token)
	return err
}
