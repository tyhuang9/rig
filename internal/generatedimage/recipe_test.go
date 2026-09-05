package generatedimage

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentplans"
)

func TestDefinitionAndRecipeAreDeterministicAndCommandSafe(t *testing.T) {
	command := `npm run build && node -e "console.log('${VALUE}', '$(whoami)', '\\path', '雪')"`
	revision := deploymentplans.DeploymentPlanRevision{
		CanonicalDigest: strings.Repeat("a", 64),
		Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{{
			Name: "web", Role: "server", RootDirectory: "apps/web", PackageManager: "pnpm", InstallBehavior: "pnpm install --frozen-lockfile", InstallDirectory: ".", NodeVersion: "22.14.0", BuildCommand: command, RunCommand: "pnpm start", InternalPort: 3000, HealthProbe: "/health",
		}}},
	}
	first, firstDigest, err := definitionFor(revision, "web")
	if err != nil {
		t.Fatal(err)
	}
	second, secondDigest, err := definitionFor(revision, "web")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || firstDigest != secondDigest || len(firstDigest) != 64 {
		t.Fatalf("definition is not deterministic: %#v/%s %#v/%s", first, firstDigest, second, secondDigest)
	}
	if !strings.Contains(first.baseImage, "@sha256:") || !strings.HasPrefix(first.baseImage, "node:22-bookworm-slim@") {
		t.Fatalf("base image is not digest pinned: %q", first.baseImage)
	}
	_, imageDigest, found := strings.Cut(first.baseImage, "@sha256:")
	if !found || !lowerHex(imageDigest, 64) {
		t.Fatalf("base image digest is malformed: %q", first.baseImage)
	}
	recipe := containerfile(true, true, first.baseImage)
	for _, required := range []string{"USER node", "ENTRYPOINT", "--mount=type=secret,id=rig-install-command", "--mount=type=secret,id=rig-build-command", "corepack"} {
		if !strings.Contains(recipe, required) {
			t.Fatalf("recipe missing %q", required)
		}
	}
	if strings.Contains(recipe, command) || strings.Contains(recipe, first.installBehavior) || strings.Contains(recipe, "COPY --chown=node:node rig/build.command") {
		t.Fatal("raw command was serialized into the container recipe or image layer")
	}
	if strings.Contains(recipe, "# syntax=") {
		t.Fatal("recipe requested a mutable external Dockerfile frontend")
	}

	revision.Plan.Components[0].RunCommand = "pnpm run serve"
	_, changed, err := definitionFor(revision, "web")
	if err != nil {
		t.Fatal(err)
	}
	if changed == firstDigest {
		t.Fatal("run-command change did not invalidate the build definition")
	}
	revision.Plan.Components[0].InstallDirectory = "apps/web"
	_, changed, err = definitionFor(revision, "web")
	if err != nil {
		t.Fatal(err)
	}
	if changed == firstDigest {
		t.Fatal("install-directory change did not invalidate the build definition")
	}
}

func TestWorkspaceRootInstallRecipeUsesSeparateWorkingDirectories(t *testing.T) {
	for _, test := range []struct {
		manager, install string
	}{
		{manager: "npm", install: "npm ci"},
		{manager: "pnpm", install: "corepack pnpm install --frozen-lockfile"},
		{manager: "yarn", install: "corepack yarn install --immutable"},
	} {
		t.Run(test.manager, func(t *testing.T) {
			revision := deploymentplans.DeploymentPlanRevision{CanonicalDigest: strings.Repeat("c", 64), Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{{
				Name: "api", Role: "server", RootDirectory: "apps/api", PackageManager: test.manager, InstallBehavior: test.install, InstallDirectory: ".", NodeVersion: "24", BuildCommand: "npm run build", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health",
			}}}}
			definition, _, err := definitionFor(revision, "api")
			if err != nil {
				t.Fatal(err)
			}
			recipe := containerfile(true, test.manager != "npm", definition.baseImage)
			if !strings.Contains(recipe, "COPY --chown=node:node rig/root.path rig/install.path /run/rig/") || !strings.Contains(recipe, "install=$(cat /run/rig/install.path); cd -- \\\"/workspace/$install\\\"") || !strings.Contains(recipe, "root=$(cat /run/rig/root.path); cd -- \\\"/workspace/$root\\\"") {
				t.Fatalf("workspace recipe did not preserve separate install/build roots:\n%s", recipe)
			}
		})
	}
}

func TestContainerfileEnablesCorepackBeforeNonRootUsersInBothStages(t *testing.T) {
	const baseImage = "node:test"
	for _, packageManager := range []string{"pnpm", "yarn"} {
		t.Run(packageManager, func(t *testing.T) {
			recipe := containerfile(false, packageManager != "npm", baseImage)
			builder, runtimeStage, found := strings.Cut(recipe, "FROM "+baseImage+" AS runtime\n")
			if !found {
				t.Fatal("runtime stage missing")
			}
			for stageName, stage := range map[string]string{"builder": builder, "runtime": runtimeStage} {
				corepackIndex := strings.Index(stage, `RUN ["corepack", "enable"]`)
				userIndex := strings.Index(stage, "USER node")
				if corepackIndex < 0 || userIndex < 0 || corepackIndex > userIndex {
					t.Errorf("%s stage does not enable Corepack before USER node:\n%s", stageName, stage)
				}
			}
		})
	}
	if recipe := containerfile(false, false, baseImage); strings.Contains(recipe, `RUN ["corepack", "enable"]`) {
		t.Fatal("npm recipe unexpectedly enables Corepack")
	}
}

func TestManagedStaticServerDoesNotFollowSymlinksOutsideRoot(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	workspace := t.TempDir()
	root := filepath.Join(workspace, "dist")
	writeTestFile(t, filepath.Join(root, "index.html"), "SAFE INDEX")
	outside := filepath.Join(t.TempDir(), "runtime-secret")
	writeTestFile(t, outside, "RUNTIME_SECRET_MUST_NOT_LEAK")
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	script := filepath.Join(workspace, "static.mjs")
	writeTestFile(t, script, staticServerScript)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, node, script, "--root", root, "--port", strconv.Itoa(port))
	command.Dir = workspace
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()

	client := &http.Client{Timeout: time.Second}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/leak"
	var response *http.Response
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		response, err = client.Get(url)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("static server did not start: %v", err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "RUNTIME_SECRET_MUST_NOT_LEAK") || string(body) != "SAFE INDEX" {
		t.Fatalf("symlink response leaked or bypassed safe fallback: status=%d body=%q", response.StatusCode, body)
	}
}

func TestDefinitionRejectsUnsupportedNodeVersion(t *testing.T) {
	revision := deploymentplans.DeploymentPlanRevision{CanonicalDigest: strings.Repeat("b", 64), Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{{Name: "api", NodeVersion: "18"}}}}
	if _, _, err := definitionFor(revision, "api"); err == nil {
		t.Fatal("unsupported Node version was accepted")
	}
}
