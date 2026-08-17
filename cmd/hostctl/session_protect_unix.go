//go:build !windows

package main

func protectSessionFile(plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func unprotectSessionFile(persisted []byte) ([]byte, error) { return persisted, nil }
