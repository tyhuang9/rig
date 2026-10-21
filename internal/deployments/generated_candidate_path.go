package deployments

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/hostd/hostd/internal/pathsecurity"
)

func generatedCandidateCleanAbsolutePath(value string) bool {
	return value != "" && !pathsecurity.RejectWindowsNamespace(value) &&
		filepath.IsAbs(value) && filepath.Clean(value) == value
}

func generatedCandidateRealDirectory(value string) bool {
	if !generatedCandidateCleanAbsolutePath(value) {
		return false
	}
	for current := value; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			generatedCandidateIsReparsePoint(current) {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return true
		}
	}
}

// generatedCandidateValidateCommandDirectories is shared by factory setup and
// command preparation. Rechecking it immediately before a Docker invocation
// prevents an authority from continuing after either fixed directory changes.
func generatedCandidateValidateCommandDirectories(options GeneratedCandidateProcessOptions) error {
	if !generatedCandidateRealDirectory(options.WorkingDirectory) {
		return errors.New("unsafe generated candidate working directory")
	}
	if !generatedCandidateRealDirectory(options.DockerConfigDirectory) {
		return errors.New("unsafe generated candidate Docker configuration")
	}
	entries, err := os.ReadDir(options.DockerConfigDirectory)
	if err != nil || len(entries) != 0 {
		return errors.New("generated candidate Docker configuration must be empty")
	}
	return nil
}
