//go:build !windows

package generatedingress

func generatedIngressPathIsReparsePoint(string) bool { return false }
