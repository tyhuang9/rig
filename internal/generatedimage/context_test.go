package generatedimage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareBuildContextExcludesSensitiveAndGeneratedPaths(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "package.json"), `{"name":"demo"}`)
	writeTestFile(t, filepath.Join(workspace, "src", "index.js"), "console.log('ok')")
	writeTestFile(t, filepath.Join(workspace, ".env.production"), "TOKEN=do-not-copy")
	writeTestFile(t, filepath.Join(workspace, ".npmrc"), "registry=https://registry.npmjs.org/")
	writeTestFile(t, filepath.Join(workspace, ".git", "config"), "do-not-copy")
	writeTestFile(t, filepath.Join(workspace, "node_modules", "pkg", "index.js"), "do-not-copy")
	writeTestFile(t, filepath.Join(workspace, "dist", "bundle.js"), "do-not-copy")

	operation := filepath.Join(t.TempDir(), "operation")
	if err := os.Mkdir(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	component := componentDefinition{rootDirectory: ".", installBehavior: "npm ci --ignore-scripts", installDirectory: ".", buildCommand: `npm run build && echo "$VALUE"`}
	layout, err := prepareBuildContext(context.Background(), workspace, operation, component, contextLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout.contextDirectory, "source", "src", "index.js")); err != nil {
		t.Fatalf("ordinary source was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.contextDirectory, "source", ".npmrc")); err != nil {
		t.Fatalf("non-secret package-manager configuration was not copied: %v", err)
	}
	for _, relative := range []string{"source/.env.production", "source/.git", "source/node_modules", "source/dist"} {
		if _, err := os.Stat(filepath.Join(layout.contextDirectory, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("excluded path %q reached build context: %v", relative, err)
		}
	}
	for path, want := range map[string]string{layout.installCommand: component.installBehavior, layout.buildCommand: component.buildCommand} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Fatalf("command file %q = %q, %v; want exact %q", path, body, err, want)
		}
		if strings.HasPrefix(filepath.Clean(path), filepath.Clean(layout.contextDirectory)+string(filepath.Separator)) {
			t.Fatalf("command-bearing file entered cacheable build context: %s", path)
		}
	}
	if err := filepath.Walk(layout.contextDirectory, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), component.installBehavior) || strings.Contains(string(body), component.buildCommand) {
			t.Fatalf("raw command persisted in build context file %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareBuildContextRejectsCredentialFiles(t *testing.T) {
	for name, body := range map[string]string{
		".npmrc":           "//registry.npmjs.org/:_authToken=secret",
		".yarnrc.yml":      "npmAuthToken: secret",
		".pnpmrc":          strings.Repeat("# safe padding\n", 30<<10) + "//registry.npmjs.org/:_authToken=late-secret",
		"deploy.pem":       "-----BEGIN PRIVATE KEY-----\nsecret",
		"credentials.json": `{"token":"secret"}`,
	} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			writeTestFile(t, filepath.Join(workspace, "package.json"), `{"name":"demo"}`)
			writeTestFile(t, filepath.Join(workspace, name), body)
			operation := filepath.Join(t.TempDir(), "operation")
			if err := os.Mkdir(operation, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := prepareBuildContext(context.Background(), workspace, operation, componentDefinition{rootDirectory: ".", installBehavior: "npm ci", installDirectory: "."}, contextLimits{})
			if !errors.Is(err, errInvalidBuildContext) {
				t.Fatalf("credential-bearing source error = %v", err)
			}
		})
	}
}

func TestPrepareBuildContextRejectsUnsafeRootsAndBounds(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "package.json"), `{"name":"demo"}`)
	operation := filepath.Join(t.TempDir(), "operation")
	if err := os.Mkdir(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := prepareBuildContext(context.Background(), workspace, operation, componentDefinition{rootDirectory: "..", installBehavior: "npm ci", installDirectory: "."}, contextLimits{})
	if !errors.Is(err, errInvalidBuildContext) {
		t.Fatalf("unsafe component root error = %v", err)
	}

	operation = filepath.Join(t.TempDir(), "operation")
	if err := os.Mkdir(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = prepareBuildContext(context.Background(), workspace, operation, componentDefinition{rootDirectory: ".", installBehavior: "npm ci", installDirectory: ".."}, contextLimits{})
	if !errors.Is(err, errInvalidBuildContext) {
		t.Fatalf("unsafe install root error = %v", err)
	}

	operation = filepath.Join(t.TempDir(), "operation")
	if err := os.Mkdir(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = prepareBuildContext(context.Background(), workspace, operation, componentDefinition{rootDirectory: ".", installBehavior: "npm ci", installDirectory: "."}, contextLimits{bytes: 1, entries: 10})
	if !errors.Is(err, errBuildContextTooLarge) {
		t.Fatalf("bounded context error = %v", err)
	}
}

func TestPrepareBuildContextRejectsLinks(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "package.json"), `{"name":"demo"}`)
	outside := filepath.Join(t.TempDir(), "outside.js")
	writeTestFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(workspace, "linked.js")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	operation := filepath.Join(t.TempDir(), "operation")
	if err := os.Mkdir(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := prepareBuildContext(context.Background(), workspace, operation, componentDefinition{rootDirectory: ".", installBehavior: "npm ci", installDirectory: "."}, contextLimits{})
	if !errors.Is(err, errInvalidBuildContext) {
		t.Fatalf("linked source error = %v", err)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
