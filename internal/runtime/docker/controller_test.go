package docker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

func TestResolveExecutableReturnsOneCanonicalRegularFile(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "docker-test")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveExecutable(func(name string) (string, error) {
		if name != "docker" {
			t.Fatalf("lookup name = %q", name)
		}
		return executable, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want || !filepath.IsAbs(resolved) {
		t.Fatalf("resolved executable = %q, want %q", resolved, want)
	}
}

func TestResolveExecutableFailsClosed(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name string
		path string
		err  error
	}{
		{name: "lookup failure", err: errors.New("missing")},
		{name: "relative", path: "docker"},
		{name: "directory", path: directory},
		{name: "missing absolute", path: filepath.Join(directory, "missing")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveExecutable(func(string) (string, error) { return test.path, test.err }); err == nil {
				t.Fatal("unsafe Docker executable was accepted")
			}
		})
	}
}

func TestPrepareControllerDirectoriesKeepsRecoveryBoundariesSeparate(t *testing.T) {
	root := t.TempDir()
	directories, err := PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{directories.DockerConfigDirectory, directories.WorkingDirectory, directories.RuntimeEnvironmentRoot}
	seen := map[string]bool{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || seen[path] {
			t.Fatalf("controller path is invalid or repeated: %q", path)
		}
		seen[path] = true
		if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("controller path %q is unsafe: info=%v err=%v", path, info, err)
		}
	}

	compose, err := securetemp.New(root)
	if err != nil {
		t.Fatal(err)
	}
	build, err := securetemp.NewGeneratedBuild(root)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := securetemp.New(directories.RuntimeEnvironmentRoot)
	if err != nil {
		t.Fatal(err)
	}
	composeFiles, err := compose.Create(uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	buildFiles, err := build.Create(uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	environmentFiles, err := environment.Create(uuid.NewString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	parents := map[string]bool{
		filepath.Dir(composeFiles.Directory):     true,
		filepath.Dir(buildFiles.Directory):       true,
		filepath.Dir(environmentFiles.Directory): true,
	}
	if len(parents) != 3 {
		t.Fatalf("temporary operation roots are not separate: %#v", parents)
	}
	if err := environment.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(environmentFiles.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment recovery left its operation: %v", err)
	}
	for _, path := range []string{composeFiles.Directory, buildFiles.Directory} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("environment recovery crossed into %q: %v", path, err)
		}
	}
}
