//go:build live_docker

package generatedimage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

// This opt-in test uses the real fixture's locked public dependencies and
// generated recipe. Database connectivity remains a separate acceptance gate.
func TestLiveHostingNotesFixtureImages(t *testing.T) {
	if runtime.GOOS != "linux" || !localDockerEndpoint(os.Getenv("DOCKER_HOST")) {
		t.Fatal("fixture image verification requires a local Linux Docker host")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-notes"))
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := sourceinspection.InspectLocal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	setupBody, err := os.ReadFile(filepath.Join(workspace, "rig-setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var setup projectanalysis.DeploymentSetup
	if err := json.Unmarshal(setupBody, &setup); err != nil {
		t.Fatal(err)
	}
	plan, _, err := deploymentplans.AcceptSetup(inspection.Analysis, setup, deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := deploymentplans.CanonicalDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	revision := deploymentplans.DeploymentPlanRevision{Plan: plan, CanonicalDigest: digest}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	for _, name := range []string{"api", "frontend"} {
		t.Run(name, func(t *testing.T) {
			imageTag := "rig-live-hosting-notes:" + uuid.NewString()
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if err := exec.CommandContext(cleanupCtx, docker, "image", "inspect", imageTag).Run(); err == nil {
					if result, err := exec.CommandContext(cleanupCtx, docker, "image", "rm", "--force", imageTag).CombinedOutput(); err != nil {
						t.Errorf("remove exact fixture image: %v %s", err, result)
					}
				}
				if err := exec.CommandContext(cleanupCtx, docker, "image", "inspect", imageTag).Run(); err == nil {
					t.Error("fixture image remains after cleanup")
				}
			})
			values := []appconfig.ValueInput{}
			if name == "frontend" {
				values = append(values, appconfig.ValueInput{Key: "VITE_BUILD_LABEL", Value: "public-build-A"})
			}
			definition, _, err := definitionForBuild(revision, name, values)
			if err != nil {
				t.Fatal(err)
			}
			if definition.rootDirectory != name || definition.installDirectory != name {
				t.Fatalf("fixture %s paths: root=%q install=%q", name, definition.rootDirectory, definition.installDirectory)
			}
			layout, err := prepareBuildContext(ctx, workspace, t.TempDir(), definition, contextLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRecipe(layout, definition); err != nil {
				t.Fatal(err)
			}
			installPath, err := os.ReadFile(filepath.Join(layout.contextDirectory, "rig", "install.path"))
			if err != nil || string(installPath) != name {
				t.Fatalf("fixture %s staged install path mismatch: %v %q", name, err, installPath)
			}
			if info, err := os.Stat(filepath.Join(layout.contextDirectory, "source", name)); err != nil || !info.IsDir() {
				t.Fatalf("fixture %s staged source directory missing: %v", name, err)
			}
			args := []string{"buildx", "build", "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain", "--tag", imageTag}
			if definition.installBehavior != "" {
				args = append(args, "--secret", "id=rig-install-command,src="+layout.installCommand)
			}
			if definition.buildCommand != "" {
				body, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeBuildFile(layout.publicBuildValues, body, 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--secret", "id=rig-build-command,src="+layout.buildCommand, "--secret", "id=rig-public-build-values,src="+layout.publicBuildValues)
			}
			args = append(args, layout.contextDirectory)
			build := exec.CommandContext(ctx, docker, args...)
			output, err := build.CombinedOutput()
			if err != nil {
				probeFile := filepath.Join(t.TempDir(), "Probe.Containerfile")
				probeRecipe := fmt.Sprintf("FROM %s\nCOPY --chown=node:node source/ /workspace/\nCOPY --chown=1000:1000 --chmod=0400 rig/root.path rig/install.path /run/rig/\nRUN [\"/bin/sh\",\"-ec\",\"printf 'install='; cat /run/rig/install.path; echo; ls -ld /workspace/api /workspace/frontend\"]\n", definition.baseImage)
				if writeErr := os.WriteFile(probeFile, []byte(probeRecipe), 0o600); writeErr != nil {
					t.Fatalf("fixture %s image build failed: %v %s; probe setup: %v", name, err, output, writeErr)
				}
				probeOutput, probeErr := exec.CommandContext(ctx, docker, "buildx", "build", "--file", probeFile, "--no-cache", "--progress", "plain", layout.contextDirectory).CombinedOutput()
				t.Fatalf("fixture %s image build failed: %v %s; staged context probe: %v %s", name, err, output, probeErr, probeOutput)
			}
			imageBody, err := os.ReadFile(layout.imageIDFile)
			if err != nil {
				t.Fatal(err)
			}
			imageID := strings.TrimSpace(string(imageBody))
			if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
				t.Fatal("invalid fixture image identity")
			}
			inspect, err := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{json .Config.Env}}", imageID).CombinedOutput()
			if err != nil || bytes.Contains(inspect, []byte("DATABASE_URL")) || bytes.Contains(inspect, []byte("TEST_SENTINEL_SECRET")) {
				t.Fatalf("fixture %s image environment check failed: %v", name, err)
			}
			command := `test -z "${DATABASE_URL+x}" && test -z "${TEST_SENTINEL_SECRET+x}"`
			if name == "frontend" {
				command += ` && test -f /workspace/frontend/dist/index.html && grep -R -F -q public-build-A /workspace/frontend/dist/assets`
			} else {
				command += ` && test -f /workspace/api/node_modules/express/package.json && test -f /workspace/api/src/server.js`
			}
			check := exec.CommandContext(ctx, docker, "container", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", imageID, "-ec", command)
			if output, err := check.CombinedOutput(); err != nil {
				t.Fatalf("fixture %s image content check failed: %v %s", name, err, output)
			}
		})
	}
}
