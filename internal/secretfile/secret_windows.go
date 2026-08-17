//go:build windows

package secretfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsPrefix = []byte("hostd-secret-dpapi-v1\x00")
var protectedPrefix = []byte("hostd-secret-v1\x00")

func protect(purpose string, plaintext []byte) ([]byte, error) {
	cleartext := make([]byte, 0, len(protectedPrefix)+len(purpose)+1+len(plaintext))
	cleartext = append(cleartext, protectedPrefix...)
	cleartext = append(cleartext, purpose...)
	cleartext = append(cleartext, 0)
	cleartext = append(cleartext, plaintext...)
	defer clear(cleartext)
	input := windows.DataBlob{Size: uint32(len(cleartext)), Data: &cleartext[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		freeDataBlob(&output)
		return nil, fmt.Errorf("protect secret with current-user DPAPI: %w", err)
	}
	defer freeDataBlob(&output)
	ciphertext := unsafe.Slice(output.Data, output.Size)
	persisted := make([]byte, len(windowsPrefix)+len(ciphertext))
	copy(persisted, windowsPrefix)
	copy(persisted[len(windowsPrefix):], ciphertext)
	runtime.KeepAlive(cleartext)
	return persisted, nil
}

func unprotect(purpose string, persisted []byte) ([]byte, error) {
	if !bytes.HasPrefix(persisted, windowsPrefix) || len(persisted) == len(windowsPrefix) {
		return nil, errors.New("secret file is not a supported DPAPI-protected format")
	}
	ciphertext := persisted[len(windowsPrefix):]
	input := windows.DataBlob{Size: uint32(len(ciphertext)), Data: &ciphertext[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		freeDataBlob(&output)
		return nil, fmt.Errorf("unprotect secret with current-user DPAPI: %w", err)
	}
	defer freeDataBlob(&output)
	cleartext := append([]byte(nil), unsafe.Slice(output.Data, output.Size)...)
	defer clear(cleartext)
	runtime.KeepAlive(ciphertext)
	prefix := append(append([]byte(nil), protectedPrefix...), purpose...)
	prefix = append(prefix, 0)
	defer clear(prefix)
	if !bytes.HasPrefix(cleartext, prefix) || len(cleartext) == len(prefix) {
		return nil, errors.New("secret file purpose does not match")
	}
	return append([]byte(nil), cleartext[len(prefix):]...), nil
}

func freeDataBlob(blob *windows.DataBlob) {
	if blob.Data == nil {
		return
	}
	clear(unsafe.Slice(blob.Data, blob.Size))
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))
	blob.Data = nil
	blob.Size = 0
}

func replaceFile(source, destination string) error {
	sourceUTF16, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationUTF16, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourceUTF16, destinationUTF16, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func restrictDirectory(string) error { return nil }

func verifyPermissions(string, os.FileInfo) error { return nil }
