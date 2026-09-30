//go:build !windows

package securetemp

func acquirePrivateDirectoryGuard(string) (func() error, error) {
	// ValidatePrivateDirectory proves that no untrusted user has rename
	// authority through the ancestry. No persistent kernel handle is needed.
	return func() error { return nil }, nil
}
