//go:build !windows

package sourceinspection

func inspectionReparsePoint(string) bool { return false }
