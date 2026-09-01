//go:build !windows

package generatedruntime

func isReparsePoint(string) bool { return false }
