package generatedimage

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
