//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsSessionFilePrefix = []byte("hostctl-session-dpapi-v1\x00")

func protectSessionFile(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("session data is empty")
	}
	input := windows.DataBlob{Size: uint32(len(plaintext)), Data: &plaintext[0]}
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		freeWindowsDataBlob(&output)
		return nil, fmt.Errorf("protect with current-user DPAPI: %w", err)
	}
	defer freeWindowsDataBlob(&output)
	protected := unsafe.Slice(output.Data, output.Size)
	persisted := make([]byte, len(windowsSessionFilePrefix)+len(protected))
	copy(persisted, windowsSessionFilePrefix)
	copy(persisted[len(windowsSessionFilePrefix):], protected)
	runtime.KeepAlive(plaintext)
	return persisted, nil
}

func unprotectSessionFile(persisted []byte) ([]byte, error) {
	if !bytes.HasPrefix(persisted, windowsSessionFilePrefix) || len(persisted) == len(windowsSessionFilePrefix) {
		return nil, errors.New("session file is not a supported DPAPI-protected format")
	}
	ciphertext := persisted[len(windowsSessionFilePrefix):]
	input := windows.DataBlob{Size: uint32(len(ciphertext)), Data: &ciphertext[0]}
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		freeWindowsDataBlob(&output)
		return nil, fmt.Errorf("unprotect with current-user DPAPI: %w", err)
	}
	defer freeWindowsDataBlob(&output)
	plaintext := append([]byte(nil), unsafe.Slice(output.Data, output.Size)...)
	runtime.KeepAlive(ciphertext)
	return plaintext, nil
}

func freeWindowsDataBlob(blob *windows.DataBlob) {
	if blob.Data == nil {
		return
	}
	clear(unsafe.Slice(blob.Data, blob.Size))
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))
	blob.Data = nil
	blob.Size = 0
}
