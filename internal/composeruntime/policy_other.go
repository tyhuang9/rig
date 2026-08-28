//go:build !windows

package composeruntime

func policyPathIsReparsePoint(string) bool { return false }
