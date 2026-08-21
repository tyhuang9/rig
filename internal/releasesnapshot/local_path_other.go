//go:build !windows

package releasesnapshot

func localPathIsReparsePoint(string) bool { return false }
