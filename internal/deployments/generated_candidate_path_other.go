//go:build !windows

package deployments

func generatedCandidateIsReparsePoint(string) bool { return false }
