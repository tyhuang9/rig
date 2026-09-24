package generatedimage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkippedInstallBuildAndStaticValidationReachCompiler(t *testing.T) {
	workspace, operation := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "public", "index.html"), "hello")
	definition := componentDefinition{name: "site", role: "static", rootDirectory: ".", installDirectory: ".", packageManager: "npm", nodeVersion: "24", staticOutputDirectory: "public", baseImage: nodeImages["24"]}
	layout, err := prepareBuildContext(context.Background(), workspace, operation, definition, contextLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecipe(layout, definition); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{layout.installCommand, layout.buildCommand} {
		if _, err := os.Stat(filename); !os.IsNotExist(err) {
			t.Fatalf("skipped command file exists: %s", filename)
		}
	}
	recipe, err := os.ReadFile(layout.containerfile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recipe), "rig-install-command") || strings.Contains(string(recipe), "rig-build-command") {
		t.Fatal("skipped command executes in recipe")
	}
	if !strings.Contains(string(recipe), `RUN ["node", "/run/rig/check-static.mjs"]`) || !strings.Contains(string(recipe), "COPY --chmod=0444 rig/static-root.path") {
		t.Fatal("static output lacks readable isolated build validation")
	}
	output, err := os.ReadFile(filepath.Join(layout.contextDirectory, "rig", "static-root.path"))
	if err != nil || string(output) != "public" {
		t.Fatal("static output directory lost")
	}
	definition.buildCommand = "npm run build"
	definition.installBehavior = "npm ci"
	recipeText := componentContainerfile(definition)
	if strings.Index(recipeText, "rig-build-command") > strings.Index(recipeText, `RUN ["node", "/run/rig/check-static.mjs"]`) {
		t.Fatal("output checked before build")
	}
}

func TestStaticBuildOutputCheckExecutesAgainstPresentMissingAndUnsafeDirectories(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for static output verification")
	}
	for _, tc := range []struct {
		name, root, output string
		valid              bool
	}{
		{"present", "app", "public", true}, {"spaces", "app", "public files", true}, {"missing", "app", "missing", false}, {"file", "app", "server.js", false}, {"escape", "app", "../../outside", false}, {"root escape", "../outside", ".", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, metadata := t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(workspace, "app", "public", "index.html"), "hello")
			writeTestFile(t, filepath.Join(workspace, "app", "public files", "index.html"), "hello")
			writeTestFile(t, filepath.Join(workspace, "app", "server.js"), "// code")
			writeTestFile(t, filepath.Join(metadata, "root.path"), tc.root)
			writeTestFile(t, filepath.Join(metadata, "static-root.path"), tc.output)
			quote := func(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }
			script := strings.NewReplacer(`"/workspace"`, quote(workspace), `"/run/rig/root.path"`, quote(filepath.Join(metadata, "root.path")), `"/run/rig/static-root.path"`, quote(filepath.Join(metadata, "static-root.path"))).Replace(staticOutputCheckScript)
			filename := filepath.Join(metadata, "check.mjs")
			writeTestFile(t, filename, script)
			output, err := exec.Command(node, filename).CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v output=%s", tc.valid, err, output)
			}
		})
	}
}

func TestNextCacheRecipeChecksBuildAndRuntimeOutput(t *testing.T) {
	definition := componentDefinition{
		name: "web", role: "server", technology: "nextjs", rootDirectory: "apps/web",
		installDirectory: "apps/web", packageManager: "npm", installBehavior: "npm ci",
		buildCommand: "npm run build", runCommand: "npm run start", baseImage: nodeImages["24"],
	}
	recipe := componentContainerfile(definition)
	builder, runtimeStage, found := strings.Cut(recipe, "FROM "+definition.baseImage+" AS runtime\n")
	if !found {
		t.Fatal("runtime stage missing")
	}
	build := strings.Index(builder, "--mount=type=secret,id=rig-build-command")
	check := strings.Index(builder, `RUN ["node", "/run/rig/check-next-cache.mjs", "create"]`)
	if build < 0 || check <= build || !strings.Contains(builder, "COPY --chmod=0444 rig/check-next-cache.mjs /run/rig/check-next-cache.mjs") {
		t.Fatal("Next cache check is not after the user build command")
	}
	runtimeCopy := strings.Index(runtimeStage, "COPY --from=builder --chown=node:node /workspace/ /workspace/")
	runtimeCheck := strings.Index(runtimeStage, `RUN ["node", "/run/rig/check-next-cache.mjs", "verify"]`)
	runtimeUser := strings.LastIndex(runtimeStage, "USER node")
	if runtimeCopy < 0 || runtimeCheck <= runtimeCopy || runtimeUser <= runtimeCheck || !strings.Contains(runtimeStage, "COPY --chmod=0444 rig/root.path /run/rig/root.path") {
		t.Fatal("Next cache path is not rechecked after runtime COPY and before USER node")
	}
	if strings.Contains(componentContainerfile(componentDefinition{baseImage: definition.baseImage, technology: "node"}), "check-next-cache") {
		t.Fatal("Node-only recipe gained the Next cache check")
	}

	workspace, operation := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "apps", "web", "package.json"), `{"name":"web"}`)
	layout, err := prepareBuildContext(context.Background(), workspace, operation, definition, contextLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecipe(layout, definition); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(layout.contextDirectory, "rig", "check-next-cache.mjs")); err != nil || string(body) != nextCacheCheckScript {
		t.Fatal("Next cache checker was not staged verbatim")
	}
}

func TestNextCacheCheckRejectsMissingAndSymlinkedDirectories(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for Next cache verification")
	}
	for _, tc := range []struct {
		name, root, action, mutation string
		valid                        bool
	}{
		{"create missing cache", "apps/web", "create", "", true},
		{"verify existing cache", "apps/web", "verify", "cache", true},
		{"verify missing cache", "apps/web", "verify", "", false},
		{"missing next output", "apps/web", "create", "missing-next", false},
		{"cache file", "apps/web", "create", "cache-file", false},
		{"cache symlink", "apps/web", "create", "cache-link", false},
		{"next symlink", "apps/web", "create", "next-link", false},
		{"root symlink", "apps/web", "create", "root-link", false},
		{"workspace symlink", ".", "create", "workspace-link", false},
		{"root escape", "../outside", "create", "", false},
		{"absolute root", "/outside", "create", "", false},
		{"empty root segment", "apps//web", "create", "", false},
		{"root current segment", "apps/./web", "create", "", false},
		{"root backslash", `apps\web`, "create", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container, metadata := t.TempDir(), t.TempDir()
			workspace := filepath.Join(container, "workspace")
			app := filepath.Join(workspace, "apps", "web")
			if err := os.MkdirAll(filepath.Join(app, ".next"), 0o700); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(app, ".next", "cache")
			switch tc.mutation {
			case "cache":
				if err := os.Mkdir(cache, 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing-next":
				if err := os.Remove(filepath.Join(app, ".next")); err != nil {
					t.Fatal(err)
				}
			case "cache-file":
				writeTestFile(t, cache, "unsafe")
			case "cache-link", "next-link", "root-link", "workspace-link":
				outside := t.TempDir()
				link := cache
				switch tc.mutation {
				case "next-link":
					link = filepath.Join(app, ".next")
				case "root-link":
					link = app
				case "workspace-link":
					link = workspace
				}
				if tc.mutation != "cache-link" {
					if err := os.RemoveAll(link); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(outside, link); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlink privilege unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			if tc.root == "." && tc.mutation != "workspace-link" {
				if err := os.MkdirAll(filepath.Join(workspace, ".next"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeTestFile(t, filepath.Join(metadata, "root.path"), tc.root)
			quote := func(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }
			script := strings.NewReplacer(`"/workspace"`, quote(workspace), `"/run/rig/root.path"`, quote(filepath.Join(metadata, "root.path"))).Replace(nextCacheCheckScript)
			filename := filepath.Join(metadata, "check.mjs")
			writeTestFile(t, filename, script)
			output, err := exec.Command(node, filename, tc.action).CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v output=%s", tc.valid, err, output)
			}
			if tc.name == "create missing cache" {
				if info, err := os.Lstat(cache); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					t.Fatal("cache directory was not created safely")
				}
			}
		})
	}
}
