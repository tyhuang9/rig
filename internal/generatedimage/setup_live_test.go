//go:build live_docker

package generatedimage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in promotion check runs the real generated recipes in local Linux
// Docker. The normal verification suite remains independent of infrastructure.
func TestLiveManualSetupRecipes(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, root, install, build, run, output string
		wantBuild                               bool
	}{
		{"node-skipped-steps", "app", "", "", "node server.js", "", true},
		{"static-custom-output", "app", "", "mkdir -p 'public files' && printf 'STATIC_READY' > 'public files/index.html'", "node -e \"process.stdout.write(require('fs').readFileSync('public files/index.html','utf8'))\"", "public files", true},
		{"static-missing-output", "app", "", "true", "", "missing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, operation := t.TempDir(), t.TempDir()
			writeTestFile(t, filepath.Join(workspace, "app", "server.js"), "process.stdout.write('NODE_READY')")
			definition := componentDefinition{name: "app", role: "server", rootDirectory: tc.root, packageManager: "npm", nodeVersion: "24", baseImage: nodeImages["24"], installBehavior: tc.install, buildCommand: tc.build, runCommand: tc.run, staticOutputDirectory: tc.output}
			if tc.output != "" {
				definition.role = "static"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			layout, err := prepareBuildContext(ctx, workspace, operation, definition, contextLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRecipe(layout, definition); err != nil {
				t.Fatal(err)
			}
			args := []string{"buildx", "build", "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain"}
			if tc.install != "" {
				args = append(args, "--secret", "id=rig-install-command,src="+layout.installCommand)
			}
			if tc.build != "" {
				args = append(args, "--secret", "id=rig-build-command,src="+layout.buildCommand)
			}
			args = append(args, layout.contextDirectory)
			output, buildErr := exec.CommandContext(ctx, docker, args...).CombinedOutput()
			if (buildErr == nil) != tc.wantBuild {
				t.Fatalf("build result=%v: %s", buildErr, output)
			}
			if !tc.wantBuild {
				if !strings.Contains(string(output), "Rig static output directory is missing or unsafe") {
					t.Fatalf("wrong build failure: %s", output)
				}
				return
			}
			imageBytes, err := os.ReadFile(layout.imageIDFile)
			if err != nil {
				t.Fatal(err)
			}
			imageID := strings.TrimSpace(string(imageBytes))
			if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
				t.Fatal("invalid Docker image identity")
			}
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if result, err := exec.CommandContext(cleanupCtx, docker, "image", "rm", imageID).CombinedOutput(); err != nil {
					t.Errorf("remove exact test image: %v %s", err, result)
				}
			})
			result, err := exec.CommandContext(ctx, docker, "run", "--rm", "--network", "none", "--memory", "128m", "--cpus", "0.5", "--pids-limit", "64", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--workdir", "/workspace/"+tc.root, imageID, "/bin/sh", "-lc", tc.run).CombinedOutput()
			if err != nil || (!strings.Contains(string(result), "NODE_READY") && !strings.Contains(string(result), "STATIC_READY")) {
				t.Fatalf("runtime result=%v: %s", err, result)
			}
		})
	}
}
