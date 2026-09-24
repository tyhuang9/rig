//go:build live_docker

package generatedimage

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestLiveNextFixtureImage builds the pinned public-package fixture with the
// same generated recipe and build-secret boundaries used by the compiler.
// Controller persistence and Caddy routing have separate hosted gates.
func TestLiveNextFixtureImage(t *testing.T) {
	if runtime.GOOS != "linux" || !localDockerEndpoint(os.Getenv("DOCKER_HOST")) {
		t.Fatal("Next.js image verification requires a local Linux Docker host")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-nextjs"))
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
	values := []appconfig.ValueInput{{Key: "NEXT_PUBLIC_BUILD_MARKER", Value: "next-public-A"}}
	definition, _, err := definitionForBuild(deploymentplans.DeploymentPlanRevision{Plan: plan, CanonicalDigest: digest}, "web", values)
	if err != nil || definition.technology != "nextjs" {
		t.Fatalf("accepted Next.js definition unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	layout, err := prepareBuildContext(ctx, workspace, t.TempDir(), definition, contextLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecipe(layout, definition); err != nil {
		t.Fatal(err)
	}
	publicBody, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBuildFile(layout.publicBuildValues, publicBody, 0o600); err != nil {
		t.Fatal(err)
	}
	tag := "rig-live-nextjs-recipe:" + uuid.NewString()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if exec.CommandContext(cleanup, docker, "image", "inspect", tag).Run() == nil {
			if output, err := exec.CommandContext(cleanup, docker, "image", "rm", "--force", tag).CombinedOutput(); err != nil {
				t.Errorf("remove exact Next.js image: %v %s", err, output)
			}
		}
		if exec.CommandContext(cleanup, docker, "image", "inspect", tag).Run() == nil {
			t.Error("Next.js image tag remains after cleanup")
		}
	})
	args := []string{"buildx", "build", "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain", "--tag", tag,
		"--secret", "id=rig-install-command,src=" + layout.installCommand,
		"--secret", "id=rig-build-command,src=" + layout.buildCommand,
		"--secret", "id=rig-public-build-values,src=" + layout.publicBuildValues,
		layout.contextDirectory}
	if output, err := exec.CommandContext(ctx, docker, args...).CombinedOutput(); err != nil {
		t.Fatalf("generated Next.js image build failed: %v %s", err, output)
	}
	imageBody, err := os.ReadFile(layout.imageIDFile)
	if err != nil {
		t.Fatal(err)
	}
	imageID := strings.TrimSpace(string(imageBody))
	if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
		t.Fatal("invalid Next.js image identity")
	}
	imageEnv, err := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{json .Config.Env}}", imageID).CombinedOutput()
	if err != nil || bytes.Contains(imageEnv, []byte("RIG_FIXTURE_RUNTIME_SECRET")) {
		t.Fatalf("runtime secret key reached image environment: %v", err)
	}
	history, err := exec.CommandContext(ctx, docker, "image", "history", "--no-trunc", imageID).CombinedOutput()
	if err != nil || bytes.Contains(history, []byte("npm ci")) || bytes.Contains(history, []byte("npm run build")) {
		t.Fatalf("generated image history exposed commands or was unreadable: %v", err)
	}
	check := exec.CommandContext(ctx, docker, "container", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", imageID, "-ec",
		"test -d /workspace/.next/cache && test ! -L /workspace/.next/cache && test -f /workspace/.next/BUILD_ID && test -f /workspace/node_modules/next/package.json && grep -R -F -q next-public-A /workspace/.next")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated Next.js image content check failed: %v %s", err, output)
	}
	t.Logf("generated Next.js image built from pinned public packages: %s", imageID)
}
