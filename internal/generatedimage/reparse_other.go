//go:build !windows

package generatedimage

func generatedImagePathIsReparsePoint(string) bool { return false }
