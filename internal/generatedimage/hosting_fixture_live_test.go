//go:build live_docker

package generatedimage

import (
	"bytes"
	"context"
	"encoding/json"
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
	var firstFrontendImage string
	for _, fixture := range []struct{ name, component, publicLabel, absentLabel string }{
		{name: "api", component: "api"},
		{name: "frontend-public-A", component: "frontend", publicLabel: "public-build-A", absentLabel: "public-build-B"},
		{name: "frontend-public-B", component: "frontend", publicLabel: "public-build-B", absentLabel: "public-build-A"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
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
			if fixture.component == "frontend" {
				values = append(values, appconfig.ValueInput{Key: "VITE_BUILD_LABEL", Value: fixture.publicLabel})
			}
			definition, _, err := definitionForBuild(revision, fixture.component, values)
			if err != nil {
				t.Fatal(err)
			}
			if definition.rootDirectory != fixture.component || definition.installDirectory != fixture.component {
				t.Fatalf("fixture %s paths: root=%q install=%q", fixture.component, definition.rootDirectory, definition.installDirectory)
			}
			layout, err := prepareBuildContext(ctx, workspace, t.TempDir(), definition, contextLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRecipe(layout, definition); err != nil {
				t.Fatal(err)
			}
			installPath, err := os.ReadFile(filepath.Join(layout.contextDirectory, "rig", "install.path"))
			if err != nil || string(installPath) != fixture.component {
				t.Fatalf("fixture %s staged install path mismatch: %v %q", fixture.component, err, installPath)
			}
			if info, err := os.Stat(filepath.Join(layout.contextDirectory, "source", fixture.component)); err != nil || !info.IsDir() {
				t.Fatalf("fixture %s staged source directory missing: %v", fixture.component, err)
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
				t.Fatalf("fixture %s image build failed: %v %s", fixture.name, err, output)
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
				t.Fatalf("fixture %s image environment check failed: %v", fixture.name, err)
			}
			command := `test -z "${DATABASE_URL+x}" && test -z "${TEST_SENTINEL_SECRET+x}"`
			if fixture.component == "frontend" {
				command += ` && test -f /workspace/frontend/dist/index.html && grep -R -F -q ` + fixture.publicLabel + ` /workspace/frontend/dist/assets && ! grep -R -F -q ` + fixture.absentLabel + ` /workspace/frontend/dist/assets && ! grep -R -E -q 'TEST_SENTINEL_SECRET|NOTES_FIXTURE_DB_URL|` + hostingLiveSentinelPrefix + `' /workspace/frontend/dist`
				if firstFrontendImage == "" {
					firstFrontendImage = imageID
				} else if firstFrontendImage == imageID {
					t.Fatal("changing only a public build value reused the prior frontend image")
				}
			} else {
				command += ` && test -f /workspace/api/node_modules/express/package.json && test -f /workspace/api/src/server.js`
			}
			check := exec.CommandContext(ctx, docker, "container", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", imageID, "-ec", command)
			if output, err := check.CombinedOutput(); err != nil {
				t.Fatalf("fixture %s image content check failed: %v %s", fixture.name, err, output)
			}
			if fixture.component == "frontend" {
				hostingLiveAssertFrontendHTTP(t, ctx, docker, imageID, fixture.publicLabel, fixture.absentLabel)
			}
		})
	}
}

func hostingLiveAssertFrontendHTTP(t *testing.T, ctx context.Context, docker, imageID, publicLabel, absentLabel string) {
	t.Helper()
	name := "rig-fixture-static-" + uuid.NewString()
	port := hostingLiveFreePort(t, "127.0.0.1")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, docker, "container", "rm", "--force", name).Run()
		if exec.CommandContext(cleanupCtx, docker, "container", "inspect", name).Run() == nil {
			t.Error("static frontend test container remains after cleanup")
		}
	})
	start := exec.CommandContext(ctx, docker, "container", "run", "--detach", "--rm",
		"--name", name, "--label", "io.rig.managed=generated-fixture-static",
		"--publish", "127.0.0.1:"+strconv.Itoa(port)+":8080",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--workdir", "/workspace/frontend", "--entrypoint", "/usr/local/bin/rig-static",
		imageID, "--root", "dist", "--port", "8080")
	if _, err := start.CombinedOutput(); err != nil {
		t.Fatal("start isolated static frontend image")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(12 * time.Second)
	var index []byte
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
		response, err := client.Do(request)
		if err == nil {
			index, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && len(index) <= 1<<20 {
				break
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatal("static frontend did not serve its index document")
		}
		time.Sleep(100 * time.Millisecond)
	}
	asset := regexp.MustCompile(`src="(/assets/[^"]+\.js)"`).FindSubmatch(index)
	if len(asset) != 2 {
		t.Fatal("static frontend index has no JavaScript asset")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+string(asset[1]), nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("fetch frontend JavaScript through the static server")
	}
	defer response.Body.Close()
	script, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || response.StatusCode != http.StatusOK || len(script) > 4<<20 || !bytes.Contains(script, []byte(publicLabel)) || bytes.Contains(script, []byte(absentLabel)) ||
		bytes.Contains(index, []byte("TEST_SENTINEL_SECRET")) || bytes.Contains(script, []byte("TEST_SENTINEL_SECRET")) ||
		bytes.Contains(index, []byte(hostingLiveSentinelPrefix)) || bytes.Contains(script, []byte(hostingLiveSentinelPrefix)) ||
		bytes.Contains(index, []byte("NOTES_FIXTURE_DB_URL")) || bytes.Contains(script, []byte("NOTES_FIXTURE_DB_URL")) {
		t.Fatal("served frontend assets did not preserve the selected public label and exclude server secret keys")
	}
}
