package docker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hostd/hostd/internal/runtime/securetemp"
)

// ControllerDirectories are persistent, protected paths owned only by the
// generated-runtime controller.
type ControllerDirectories struct {
	DockerConfigDirectory string
	WorkingDirectory      string
}

// ResolveExecutable resolves the Docker CLI once so every generated-runtime
// boundary executes the same absolute target without consulting PATH again.
func ResolveExecutable() (string, error) {
	return resolveExecutable(exec.LookPath)
}

func resolveExecutable(lookPath func(string) (string, error)) (string, error) {
	if lookPath == nil {
		return "", errors.New("docker executable resolver is required")
	}
	path, err := lookPath("docker")
	if err != nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("docker executable is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return "", errors.New("docker executable is unavailable")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("docker executable is unavailable")
	}
	return resolved, nil
}

// PrepareControllerDirectories creates direct children through the existing
// protected generated-controller boundary. It deliberately does not broaden
// securetemp's accepted cleanup namespaces.
func PrepareControllerDirectories(dataRoot string) (ControllerDirectories, error) {
	directory, err := securetemp.NewGeneratedBuilderDirectory(dataRoot)
	if err != nil {
		return ControllerDirectories{}, err
	}
	dockerConfig, err := directory.EnsureDirectory("controller-docker-config")
	if err != nil {
		return ControllerDirectories{}, err
	}
	working, err := directory.EnsureDirectory("controller-working")
	if err != nil {
		return ControllerDirectories{}, err
	}
	return ControllerDirectories{
		DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
	}, nil
}
