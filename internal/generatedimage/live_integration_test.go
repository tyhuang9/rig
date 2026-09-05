package generatedimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const (
	liveInstallCanary = "rig_live_install_command_canary_b92f9f21"
	liveBuildCanary   = "rig_live_build_command_canary_278e491c"
)

func TestLiveGeneratedImageCompiler(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_GENERATED_RUNTIME") != "1" {
		t.Skip("set RIG_RUN_LIVE_GENERATED_RUNTIME=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("live generated image compiler requires Linux")
	}
	dockerExecutable, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("live generated image compiler requires Docker")
	}
	dockerExecutable, err = filepath.Abs(dockerExecutable)
	if err != nil {
		t.Fatal("live generated image compiler could not resolve Docker")
	}
	dockerEndpoint := os.Getenv("DOCKER_HOST")
	if !localDockerEndpoint(dockerEndpoint) {
		t.Fatal("live generated image compiler requires a local Docker endpoint")
	}

	runner := runtimeprocess.ExecRunner{}
	dataRoot := t.TempDir()
	builder, err := NewBuilderManager(runner, BuilderManagerOptions{
		DataRoot:         dataRoot,
		DockerExecutable: dockerExecutable,
		DockerEndpoint:   dockerEndpoint,
		PrepareTimeout:   2 * time.Minute,
		OutputLimit:      64 << 10,
	})
	if err != nil {
		t.Fatal("live generated image builder configuration failed")
	}
	identity, dockerEnvironment, err := builder.preparePersistentState()
	if err != nil {
		t.Fatal("live generated image builder state failed")
	}
	liveContext, cancelLive := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancelLive()
	if !liveDockerIsLinux(liveContext, runner, dockerExecutable, builder.directory.Root(), dockerEnvironment) {
		t.Fatal("live generated image Docker preflight failed")
	}

	workspace := t.TempDir()
	const componentRoot = "app root"
	componentDirectory := filepath.Join(workspace, componentRoot)
	if err := os.Mkdir(componentDirectory, 0o700); err != nil {
		t.Fatal("live generated image fixture setup failed")
	}
	// The lockfile is deliberately at the workspace root while the component
	// build runs in a spaced child path. This is the generated monorepo split.
	liveWriteFile(t, filepath.Join(workspace, "package.json"), `{"name":"rig-live-generated-image","version":"1.0.0","private":true}`)
	liveWriteFile(t, filepath.Join(workspace, "package-lock.json"), `{"name":"rig-live-generated-image","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"rig-live-generated-image","version":"1.0.0"}}}`)
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), workspace)
	if err != nil {
		t.Fatal("live generated image fixture inspection failed")
	}

	installCommand := `npm ci && test "$(id -u):$(id -g)" = "1000:1000" && install_marker=` + liveInstallCanary + ` && test "$install_marker" = ` + liveInstallCanary
	buildCommand := `build_marker=` + liveBuildCanary + ` && expanded="$(printf '%s' shell-ok)" && test "$build_marker" = ` + liveBuildCanary + ` && test "$expanded" = shell-ok && node -e "require('node:fs').writeFileSync('artifact.txt','generated-image-ok')"`
	appID, releaseID, revisionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	release := releasesnapshot.Release{
		ID: releaseID, AppID: appID, SourceProvider: "local",
		ResolvedSHA: inspection.Analysis.StructuralFingerprint, WorkspaceTreeSHA256: strings.Repeat("c", 64),
		WorkspacePath: workspace, WorkspaceState: releasesnapshot.WorkspaceStateReady,
		DeploymentPlanRevisionID: revisionID, DeploymentPlanRevisionNumber: 1,
	}
	revision := deploymentplans.DeploymentPlanRevision{
		ID: revisionID, AppID: appID, RevisionNumber: 1, CanonicalDigest: strings.Repeat("a", 64),
		Plan: deploymentplans.Plan{
			Strategy: deploymentplans.StrategyGeneratedNode,
			Detector: deploymentplans.Detector{
				Name: "projectanalysis", Version: projectanalysis.SchemaVersion,
				SourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint,
			},
			Source: deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint},
			Components: []deploymentplans.Component{{
				Name: "app", Role: "server", RootDirectory: componentRoot,
				PackageManager: "npm", InstallBehavior: installCommand, InstallDirectory: ".",
				NodeVersion: "24", BuildCommand: buildCommand, RunCommand: "node server.js",
				InternalPort: 3000, HealthProbe: "/",
			}},
		},
	}
	definition, definitionDigest, err := definitionFor(revision, "app")
	if err != nil {
		t.Fatal("live generated image definition failed")
	}
	imageReference := imageTag(appID, releaseID, definition.name, definitionDigest)
	containerName := "rig-image-live-" + strings.ReplaceAll(appID, "-", "")[:12]
	cleanup := liveGeneratedImageCleanup{
		runner: runner, dockerExecutable: dockerExecutable, directory: builder.directory.Root(),
		environment: dockerEnvironment, identity: identity, imageReference: imageReference, containerName: containerName,
	}
	t.Cleanup(func() {
		if !cleanup.run() {
			t.Error("live generated image Docker cleanup failed")
		}
	})

	temporary, err := securetemp.NewGeneratedBuild(dataRoot)
	if err != nil {
		t.Fatal("live generated image temporary storage failed")
	}
	releaseReader := &compilerReleaseReader{release: release}
	artifacts := &compilerArtifactWriter{}
	compiler, err := NewCompiler(
		releaseReader,
		compilerPlanReader{revision: revision},
		artifacts,
		temporary,
		builder,
		runner,
		CompilerOptions{BuildTimeout: 6 * time.Minute},
	)
	if err != nil {
		t.Fatal("live generated image compiler configuration failed")
	}
	artifact, err := compiler.Compile(liveContext, appID, releaseID, "app")
	if err != nil {
		t.Fatalf("live generated image production compile failed: diagnostic=%s", liveGeneratedImageFailureCode(err))
	}
	if artifact.State != ArtifactReady || !validImageContentID(artifact.ImageContentID) || artifacts.failed != "" {
		t.Fatal("live generated image production compile returned an invalid artifact")
	}
	operationEntries, err := os.ReadDir(filepath.Join(dataRoot, "runtime", "generated-build"))
	if err != nil || len(operationEntries) != 0 {
		t.Fatal("live generated image protected build material was not removed")
	}

	if !liveRunBuiltImage(liveContext, runner, dockerExecutable, builder.directory.Root(), dockerEnvironment, containerName, artifact.ImageContentID, componentRoot) {
		t.Fatal("live generated image artifact execution failed")
	}
	if !liveImageExcludesCommands(liveContext, runner, dockerExecutable, builder.directory.Root(), dockerEnvironment, artifact.ImageContentID, installCommand, buildCommand) {
		t.Fatal("live generated image persisted command material")
	}
}

func liveGeneratedImageFailureCode(err error) string {
	var compileError *CompileError
	if errors.As(err, &compileError) && validLiveGeneratedImageDiagnostic(string(compileError.Code)) {
		return compileError.Code
	}
	var builderError *BuilderError
	if errors.As(err, &builderError) && validLiveGeneratedImageDiagnostic(string(builderError.Code)) {
		return string(builderError.Code)
	}
	return "unclassified"
}

func validLiveGeneratedImageDiagnostic(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && character != '_' {
			return false
		}
	}
	return true
}

func TestLiveGeneratedImageFailureCodeIsTypedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrapped: %w", &CompileError{Code: string(DiagnosticBuildFailed)}), "build_failed"},
		{fmt.Errorf("wrapped: %w", &BuilderError{Code: BuilderRuntimeUnavailable}), "builder_runtime_unavailable"},
		{errors.New("sensitive raw error"), "unclassified"},
		{&CompileError{Code: "sensitive\npath"}, "unclassified"},
	} {
		if got := liveGeneratedImageFailureCode(test.err); got != test.want {
			t.Fatalf("live generated image diagnostic = %q, want %q", got, test.want)
		}
	}
}

func liveWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal("live generated image fixture write failed")
	}
}

func liveDockerIsLinux(ctx context.Context, runner runtimeprocess.CommandRunner, executable, directory string, environment []string) bool {
	result, err := runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: executable, Args: []string{"info", "--format", "{{.OSType}}"},
		Directory: directory, Env: environment, Timeout: time.Minute, OutputLimit: 4 << 10,
	})
	ok := err == nil && !result.StdoutTruncated && !result.StderrTruncated && strings.TrimSpace(string(result.Stdout)) == "linux"
	clear(result.Stdout)
	clear(result.Stderr)
	return ok
}

func liveRunBuiltImage(ctx context.Context, runner runtimeprocess.CommandRunner, executable, directory string, environment []string, containerName, imageID, componentRoot string) bool {
	result, err := runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: executable,
		Args: []string{
			"container", "run", "--rm", "--name", containerName,
			"--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--label", "io.rig.managed=generated-image-live-test", "--label", "rig.live=" + containerName,
			"--workdir", "/workspace/" + componentRoot,
			imageID, "/bin/sh", "-lc", `test "$(cat artifact.txt)" = generated-image-ok && test "$(id -u):$(id -g)" = "1000:1000"`,
		},
		Directory: directory, Env: environment, Timeout: time.Minute, OutputLimit: 4 << 10,
	})
	ok := err == nil && !result.StdoutTruncated && !result.StderrTruncated && len(result.Stdout) == 0 && len(result.Stderr) == 0
	clear(result.Stdout)
	clear(result.Stderr)
	return ok
}

func liveImageExcludesCommands(ctx context.Context, runner runtimeprocess.CommandRunner, executable, directory string, environment []string, imageID, installCommand, buildCommand string) bool {
	requests := [][]string{
		{"image", "inspect", imageID},
		{"image", "history", "--no-trunc", "--format", "{{json .CreatedBy}}", imageID},
	}
	forbidden := [][]byte{
		[]byte(installCommand), []byte(buildCommand), []byte(liveInstallCanary), []byte(liveBuildCanary),
	}
	for _, args := range requests {
		result, err := runner.Run(ctx, runtimeprocess.CommandRequest{
			Executable: executable, Args: args, Directory: directory, Env: environment,
			Timeout: time.Minute, OutputLimit: runtimeprocess.DefaultOutputLimit,
		})
		clean := err == nil && !result.StdoutTruncated && !result.StderrTruncated
		for _, value := range forbidden {
			if bytes.Contains(result.Stdout, value) || bytes.Contains(result.Stderr, value) {
				clean = false
			}
		}
		clear(result.Stdout)
		clear(result.Stderr)
		if !clean {
			return false
		}
	}
	return true
}

type liveGeneratedImageCleanup struct {
	runner           runtimeprocess.CommandRunner
	dockerExecutable string
	directory        string
	environment      []string
	identity         builderIdentity
	imageReference   string
	containerName    string
}

func (cleanup liveGeneratedImageCleanup) run() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	commands := [][]string{
		{"buildx", "rm", "--force", cleanup.identity.BuilderName},
		{"container", "rm", "--force", cleanup.containerName},
		{"container", "rm", "--force", buildkitContainerName(cleanup.identity)},
		{"network", "rm", cleanup.identity.NetworkName},
		{"image", "rm", "--force", cleanup.imageReference},
	}
	for _, args := range commands {
		result, _ := cleanup.runner.Run(ctx, runtimeprocess.CommandRequest{
			Executable: cleanup.dockerExecutable, Args: args, Directory: cleanup.directory,
			Env: cleanup.environment, Timeout: 30 * time.Second, OutputLimit: 16 << 10,
		})
		clear(result.Stdout)
		clear(result.Stderr)
	}
	checks := []struct {
		args      []string
		forbidden string
	}{
		{args: []string{"container", "ls", "--all", "--quiet", "--filter", "label=rig.live=" + cleanup.containerName}},
		{args: []string{"container", "ls", "--all", "--quiet", "--filter", "label=rig.builder=" + cleanup.identity.BuilderName}},
		{args: []string{"network", "ls", "--quiet", "--filter", "label=rig.builder=" + cleanup.identity.BuilderName}},
		{args: []string{"image", "ls", "--quiet", "--filter", "reference=" + cleanup.imageReference}},
		{args: []string{"buildx", "ls", "--format", "{{.Name}}"}, forbidden: cleanup.identity.BuilderName},
	}
	clean := true
	for _, check := range checks {
		result, err := cleanup.runner.Run(ctx, runtimeprocess.CommandRequest{
			Executable: cleanup.dockerExecutable, Args: check.args, Directory: cleanup.directory,
			Env: cleanup.environment, Timeout: 30 * time.Second, OutputLimit: 16 << 10,
		})
		if err != nil || result.StdoutTruncated || result.StderrTruncated || (check.forbidden == "" && len(bytes.TrimSpace(result.Stdout)) != 0) || (check.forbidden != "" && liveOutputHasLine(result.Stdout, check.forbidden)) {
			clean = false
		}
		clear(result.Stdout)
		clear(result.Stderr)
	}
	return clean
}

func liveOutputHasLine(output []byte, value string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSuffix(strings.TrimSpace(line), "*") == value {
			return true
		}
	}
	return false
}
