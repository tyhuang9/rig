//go:build live_docker

package generatedimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/sourceinspection"
)

// TestLiveStandaloneRecipeMatrix uses the generated recipe, real locked public
// packages, and a published Docker port. The published-port request rejects a
// server that binds only inside its own loopback namespace.
func TestLiveStandaloneRecipeMatrix(t *testing.T) {
	if runtime.GOOS != "linux" || !localDockerEndpoint(os.Getenv("DOCKER_HOST")) {
		t.Fatal("recipe matrix requires a local Linux Docker host")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	for _, tc := range []struct {
		name, directory, component, route, marker string
		port                                      int
	}{
		{name: "standalone-node-api", directory: "hosting-node-api", component: "api", route: "/version", port: 3000},
		{name: "vite-only-yarn", directory: "hosting-vite-only", component: "web", route: "/", marker: "vite-public-A", port: 8080},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", tc.directory))
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := sourceinspection.InspectLocal(workspace)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(workspace, "rig-setup.json"))
			if err != nil {
				t.Fatal(err)
			}
			var setup projectanalysis.DeploymentSetup
			if err := json.Unmarshal(body, &setup); err != nil {
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
			var values []appconfig.ValueInput
			if tc.marker != "" {
				values = []appconfig.ValueInput{{Key: "VITE_BUILD_MARKER", Value: tc.marker}}
			}
			definition, _, err := definitionForBuild(deploymentplans.DeploymentPlanRevision{Plan: plan, CanonicalDigest: digest}, tc.component, values)
			if err != nil {
				t.Fatal(err)
			}
			layout, err := prepareBuildContext(ctx, workspace, t.TempDir(), definition, contextLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.marker != "" {
				if _, err := os.Stat(filepath.Join(layout.contextDirectory, "source", ".yarn", "install-state.gz")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("host Yarn install state reached generated source context: %v", err)
				}
			}
			if err := writeRecipe(layout, definition); err != nil {
				t.Fatal(err)
			}
			tag := "rig-live-recipe-matrix:" + uuid.NewString()
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if exec.CommandContext(cleanup, docker, "image", "inspect", tag).Run() == nil {
					if output, err := exec.CommandContext(cleanup, docker, "image", "rm", "--force", tag).CombinedOutput(); err != nil {
						t.Errorf("remove exact recipe image: %v %s", err, output)
					}
				}
				if exec.CommandContext(cleanup, docker, "image", "inspect", tag).Run() == nil {
					t.Error("recipe image remains after cleanup")
				}
			})
			args := []string{"buildx", "build", "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain", "--tag", tag,
				"--secret", "id=rig-install-command,src=" + layout.installCommand}
			if definition.buildCommand != "" {
				publicBody, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeBuildFile(layout.publicBuildValues, publicBody, 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--secret", "id=rig-build-command,src="+layout.buildCommand, "--secret", "id=rig-public-build-values,src="+layout.publicBuildValues)
			}
			args = append(args, layout.contextDirectory)
			if output, err := exec.CommandContext(ctx, docker, args...).CombinedOutput(); err != nil {
				t.Fatalf("generated %s image build failed: %v %s", tc.name, err, output)
			}
			imageBody, err := os.ReadFile(layout.imageIDFile)
			if err != nil {
				t.Fatal(err)
			}
			imageID := strings.TrimSpace(string(imageBody))
			if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
				t.Fatal("invalid generated image identity")
			}
			imageEnv, err := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{json .Config.Env}}", imageID).CombinedOutput()
			if err != nil || bytes.Contains(imageEnv, []byte("TEST_SENTINEL_SECRET")) {
				t.Fatalf("runtime secret key reached image environment: %v", err)
			}
			history, err := exec.CommandContext(ctx, docker, "image", "history", "--no-trunc", imageID).CombinedOutput()
			if err != nil || bytes.Contains(history, []byte(definition.installBehavior)) || definition.buildCommand != "" && bytes.Contains(history, []byte(definition.buildCommand)) {
				t.Fatalf("generated image history exposed command or was unreadable: %v", err)
			}
			probe := "test -f /workspace/server.js && test -f /workspace/node_modules/express/package.json"
			if tc.marker != "" {
				probe = "test -f /workspace/dist/index.html && grep -R -F -q vite-public-A /workspace/dist/assets"
			}
			check := exec.CommandContext(ctx, docker, "container", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", imageID, "-ec", probe)
			if output, err := check.CombinedOutput(); err != nil {
				t.Fatalf("generated image content check failed: %v %s", err, output)
			}
			container := "rig-recipe-matrix-" + uuid.NewString()
			hostPort := hostingLiveFreePort(t, "127.0.0.1")
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if output, err := exec.CommandContext(cleanup, docker, "container", "rm", "--force", container).CombinedOutput(); err != nil && exec.CommandContext(cleanup, docker, "container", "inspect", container).Run() == nil {
					t.Errorf("remove exact recipe container: %v %s", err, output)
				}
				if exec.CommandContext(cleanup, docker, "container", "inspect", container).Run() == nil {
					t.Error("recipe container remains after cleanup")
				}
			})
			runArgs := []string{"container", "run", "--detach", "--rm", "--name", container, "--label", "io.rig.managed=generated-fixture-recipe-matrix",
				"--publish", "127.0.0.1:" + strconv.Itoa(hostPort) + ":" + strconv.Itoa(tc.port), "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--workdir", "/workspace"}
			if tc.marker == "" {
				runArgs = append(runArgs, "--env", "RUNTIME_MARKER=slot-A", "--env", "TEST_SENTINEL_SECRET=runtime-only-value")
			}
			runArgs = append(runArgs, imageID, "/bin/sh", "-lc", definition.runCommand)
			if output, err := exec.CommandContext(ctx, docker, runArgs...).CombinedOutput(); err != nil {
				t.Fatalf("start generated recipe image: %v %s", err, output)
			}
			client := &http.Client{Timeout: 2 * time.Second}
			url := "http://127.0.0.1:" + strconv.Itoa(hostPort) + tc.route
			var responseBody []byte
			deadline := time.Now().Add(15 * time.Second)
			for {
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				response, requestErr := client.Do(request)
				if requestErr == nil {
					responseBody, requestErr = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
					response.Body.Close()
					if requestErr == nil && response.StatusCode == http.StatusOK && len(responseBody) <= 1<<20 {
						break
					}
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					t.Fatal("generated image did not answer through Docker published port")
				}
				time.Sleep(100 * time.Millisecond)
			}
			if tc.marker == "" {
				var result struct {
					Version, RuntimeMarker string
					SecretPresent          bool
				}
				if err := json.Unmarshal(responseBody, &result); err != nil || result.Version != "standalone-node-v1" || result.RuntimeMarker != "slot-A" || !result.SecretPresent || bytes.Contains(responseBody, []byte("runtime-only-value")) {
					t.Fatalf("standalone API response did not preserve runtime scope: %+v %v", result, err)
				}
			} else {
				asset := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindSubmatch(responseBody)
				if len(asset) != 2 {
					t.Fatal("Vite index has no JavaScript asset")
				}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(hostPort)+string(asset[1]), nil)
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				assetBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(assetBody, []byte(tc.marker)) || bytes.Contains(assetBody, []byte("TEST_SENTINEL_SECRET")) {
					t.Fatal("served Vite asset did not preserve public build marker and secret boundary")
				}
			}
			t.Logf("%s generated image %s served through Docker published port", tc.name, imageID)
		})
	}
}
