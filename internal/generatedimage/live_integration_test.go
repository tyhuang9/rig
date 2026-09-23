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
	buildObservation := &liveGeneratedImageBuildObservation{delegate: runner, status: "not_attempted"}
	dataRoot := t.TempDir()
	builder, err := NewBuilderManager(buildObservation, BuilderManagerOptions{
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
	liveWriteFile(t, filepath.Join(componentDirectory, "server.js"), "'use strict';\n")
	// The lockfile is deliberately at the workspace root while the component
	// build runs in a spaced child path. This is the generated monorepo split.
	liveWriteFile(t, filepath.Join(workspace, "package.json"), `{"name":"rig-live-generated-image","version":"1.0.0","private":true}`)
	liveWriteFile(t, filepath.Join(workspace, "package-lock.json"), `{"name":"rig-live-generated-image","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"rig-live-generated-image","version":"1.0.0"}}}`)
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), workspace)
	if err != nil {
		t.Fatal("live generated image fixture inspection failed")
	}

	installCommand := `test -w . && npm ci && test "$(id -u):$(id -g)" = "1000:1000" && install_marker=` + liveInstallCanary + ` && test "$install_marker" = ` + liveInstallCanary
	buildCommand := `test "$PWD" = "/workspace/app root" && build_marker=` + liveBuildCanary + ` && expanded="$(printf '%s' shell-ok)" && test "$build_marker" = ` + liveBuildCanary + ` && test "$expanded" = shell-ok && node -e "require('node:fs').writeFileSync('artifact.txt','generated-image-ok')"`
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
	builderObservation := &liveGeneratedImageBuilderObservation{delegate: builder, status: "not_attempted"}
	compiler, err := NewCompiler(
		releaseReader,
		compilerPlanReader{revision: revision},
		artifacts,
		temporary,
		builderObservation,
		buildObservation,
		CompilerOptions{BuildTimeout: 6 * time.Minute},
	)
	if err != nil {
		t.Fatal("live generated image compiler configuration failed")
	}
	artifact, err := compiler.Compile(liveContext, appID, releaseID, "app")
	if err != nil {
		t.Fatalf("live generated image production compile failed: diagnostic=%s,builder_status=%s,build_status=%s", liveGeneratedImageFailureCode(err), builderObservation.status, buildObservation.status)
	}
	if artifact.State != ArtifactReady || !validImageContentID(artifact.ImageContentID) || artifacts.failed != "" {
		t.Fatal("live generated image production compile returned an invalid artifact")
	}
	operationEntries, err := os.ReadDir(filepath.Join(dataRoot, "runtime", "generated-build"))
	if err != nil || len(operationEntries) != 0 {
		t.Fatal("live generated image protected build material was not removed")
	}

	if status := liveRunBuiltImage(liveContext, runner, dockerExecutable, builder.directory.Root(), dockerEnvironment, containerName, artifact.ImageContentID, componentRoot); status != "success" {
		t.Fatalf("live generated image artifact execution failed: runtime_status=%s", status)
	}
	if !liveImageExcludesCommands(liveContext, runner, dockerExecutable, builder.directory.Root(), dockerEnvironment, artifact.ImageContentID, installCommand, buildCommand) {
		t.Fatal("live generated image persisted command material")
	}
}

type liveGeneratedImageBuilderObservation struct {
	delegate builderPreparer
	status   string
}

type liveGeneratedImageBuildObservation struct {
	delegate runtimeprocess.CommandRunner
	status   string
}

func (observation *liveGeneratedImageBuildObservation) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	result, err := observation.delegate.Run(ctx, request)
	if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] == "build" {
		observation.status = liveGeneratedImageBuildResultStatus(ctx, result, err)
	}
	return result, err
}

func liveGeneratedImageBuildResultStatus(ctx context.Context, result runtimeprocess.CommandResult, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	case errors.Is(err, runtimeprocess.ErrTerminationFailed):
		return "termination_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case result.StdoutTruncated || result.StderrTruncated:
		return "output_truncated"
	case err == nil:
		return "success"
	}
	var executableError *exec.Error
	var pathError *os.PathError
	if errors.As(err, &executableError) || errors.As(err, &pathError) {
		return "runtime_unavailable"
	}
	combined := make([]byte, 0, len(result.Stdout)+len(result.Stderr))
	combined = append(combined, result.Stdout...)
	combined = append(combined, result.Stderr...)
	lower := bytes.ToLower(combined)
	clear(combined)
	defer clear(lower)
	containsAny := func(markers ...string) bool {
		for _, marker := range markers {
			if bytes.Contains(lower, []byte(marker)) {
				return true
			}
		}
		return false
	}
	switch {
	case containsAny("dockerfile parse error", "failed to parse dockerfile", "unknown instruction", "unknown flag:"):
		return "dockerfile_invalid"
	case containsAny("permission denied", "operation not permitted"):
		return "permission_denied"
	case containsAny("secret is required but not available", "failed to stat /run/secrets", "invalid mount config"):
		return "secret_mount_invalid"
	case containsAny("no space left on device", "disk quota exceeded"):
		return "storage_exhausted"
	case containsAny("network is unreachable", "temporary failure in name resolution", "dial tcp", "i/o timeout"):
		return "network_unavailable"
	case containsAny("failed to resolve source metadata", "pull access denied", "manifest unknown"):
		return "base_image_unavailable"
	case containsAny("exporting to docker image format", "failed to load", "failed to export"):
		return "export_failed"
	case bytes.LastIndex(lower, []byte("/run/rig/root.path")) > bytes.LastIndex(lower, []byte("/run/rig/install.path")):
		return "build_step_failed"
	case bytes.Contains(lower, []byte("/run/rig/install.path")):
		return "install_step_failed"
	default:
		return "unclassified"
	}
}

func (observation *liveGeneratedImageBuilderObservation) Prepare(ctx context.Context) (BuilderSession, error) {
	session, err := observation.delegate.Prepare(ctx)
	if err != nil {
		observation.status = liveGeneratedImageFailureCode(err)
		return BuilderSession{}, err
	}
	if !validBuilderSession(session) {
		observation.status = "invalid_session"
		return session, nil
	}
	observation.status = "ready"
	return session, nil
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

func TestLiveGeneratedImageBuilderObservationUsesOnlyFixedStatus(t *testing.T) {
	validSession := BuilderSession{
		DockerExecutable: filepath.Join(t.TempDir(), "docker"),
		BuilderName:      "rig-buildkit-0123456789abcdef01234567",
		environment: []string{
			"BUILDX_CONFIG=" + filepath.Join(t.TempDir(), "buildx"),
			"DOCKER_CONFIG=" + filepath.Join(t.TempDir(), "docker"),
		},
		storageQuotaBytes: defaultStateQuotaBytes,
	}
	for _, test := range []struct {
		name    string
		builder *compilerBuilder
		want    string
	}{
		{"ready", &compilerBuilder{session: validSession}, "ready"},
		{"invalid session", &compilerBuilder{session: BuilderSession{}}, "invalid_session"},
		{"typed failure", &compilerBuilder{err: &BuilderError{Code: BuilderDriftDetected}}, "builder_drift_detected"},
		{"raw failure", &compilerBuilder{err: errors.New("sensitive raw builder error")}, "unclassified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := &liveGeneratedImageBuilderObservation{delegate: test.builder, status: "not_attempted"}
			_, _ = observation.Prepare(context.Background())
			if observation.status != test.want || !validLiveGeneratedImageDiagnostic(observation.status) {
				t.Fatalf("builder observation = %q, want %q", observation.status, test.want)
			}
		})
	}
}

func TestLiveGeneratedImageBuildObservationUsesOnlyFixedStatus(t *testing.T) {
	for _, test := range []struct {
		name      string
		stdout    string
		stderr    string
		err       error
		truncated bool
		cancelled bool
		want      string
	}{
		{name: "success", want: "success"},
		{name: "Dockerfile", stderr: "dockerfile parse error: sensitive/path", err: errors.New("exit status 1"), want: "dockerfile_invalid"},
		{name: "permission", stderr: "permission denied: sensitive/path", err: errors.New("exit status 1"), want: "permission_denied"},
		{name: "secret mount", stderr: "secret is required but not available: sensitive material", err: errors.New("exit status 1"), want: "secret_mount_invalid"},
		{name: "install", stderr: "process /run/rig/install.path failed with secret material", err: errors.New("exit status 1"), want: "install_step_failed"},
		{name: "build", stderr: "completed /run/rig/install.path then failed /run/rig/root.path with secret material", err: errors.New("exit status 1"), want: "build_step_failed"},
		{name: "storage", stderr: "no space left on device: sensitive/path", err: errors.New("exit status 1"), want: "storage_exhausted"},
		{name: "network", stderr: "network is unreachable: sensitive host", err: errors.New("exit status 1"), want: "network_unavailable"},
		{name: "base image", stderr: "failed to resolve source metadata for sensitive image", err: errors.New("exit status 1"), want: "base_image_unavailable"},
		{name: "export", stderr: "completed /run/rig/root.path then failed to export sensitive image", err: errors.New("exit status 1"), want: "export_failed"},
		{name: "runtime", err: &exec.Error{Name: "sensitive executable", Err: errors.New("sensitive error")}, want: "runtime_unavailable"},
		{name: "timeout", err: context.DeadlineExceeded, want: "timeout"},
		{name: "termination", err: runtimeprocess.ErrTerminationFailed, want: "termination_failed"},
		{name: "cancelled", err: errors.New("sensitive raw error"), cancelled: true, want: "cancelled"},
		{name: "raw", stdout: "sensitive stdout", stderr: "sensitive stderr", err: errors.New("sensitive raw error"), want: "unclassified"},
		{name: "truncation precedence", stderr: "permission denied", err: errors.New("exit status 1"), truncated: true, want: "output_truncated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runtimeprocess.CommandResult{Stdout: []byte(test.stdout), Stderr: []byte(test.stderr), StderrTruncated: test.truncated}
			stdoutBefore, stderrBefore := append([]byte(nil), result.Stdout...), append([]byte(nil), result.Stderr...)
			ctx := context.Background()
			if test.cancelled {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if got := liveGeneratedImageBuildResultStatus(ctx, result, test.err); got != test.want || !validLiveGeneratedImageDiagnostic(got) {
				t.Fatalf("build observation = %q, want %q", got, test.want)
			}
			if !bytes.Equal(result.Stdout, stdoutBefore) || !bytes.Equal(result.Stderr, stderrBefore) {
				t.Fatal("build observation mutated the compiler result")
			}
			clear(result.Stdout)
			clear(result.Stderr)
			clear(stdoutBefore)
			clear(stderrBefore)
		})
	}

	runner := &compilerRunner{result: runtimeprocess.CommandResult{Stderr: []byte("permission denied")}, err: errors.New("exit status 1")}
	observation := &liveGeneratedImageBuildObservation{delegate: runner, status: "not_attempted"}
	_, _ = observation.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"buildx", "ls"}})
	if observation.status != "not_attempted" {
		t.Fatal("non-build command changed the build observation")
	}
	_, _ = observation.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"buildx", "build"}})
	if observation.status != "permission_denied" {
		t.Fatalf("observed build status = %q, want permission_denied", observation.status)
	}
	clear(runner.result.Stderr)
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

func liveRunBuiltImage(ctx context.Context, runner runtimeprocess.CommandRunner, executable, directory string, environment []string, containerName, imageID, componentRoot string) string {
	result, err := runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: executable,
		Args: []string{
			"container", "run", "--rm", "--name", containerName,
			"--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--label", "io.rig.managed=generated-image-live-test", "--label", "rig.live=" + containerName,
			"--workdir", "/workspace/" + componentRoot,
			imageID, "/bin/sh", "-lc", `test "$PWD" = "/workspace/app root" || exit 81; test -f artifact.txt || exit 82; test -r artifact.txt || exit 83; test "$(cat artifact.txt)" = generated-image-ok || exit 84; test "$(id -u)" = 1000 || exit 85; test "$(id -g)" = 1000 || exit 86`,
		},
		Directory: directory, Env: environment, Timeout: time.Minute, OutputLimit: 4 << 10,
	})
	status := liveGeneratedImageRuntimeStatus(ctx, result, err)
	clear(result.Stdout)
	clear(result.Stderr)
	return status
}

func liveGeneratedImageRuntimeStatus(ctx context.Context, result runtimeprocess.CommandResult, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return "cancelled"
	case errors.Is(err, runtimeprocess.ErrTerminationFailed):
		return "termination_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case result.StdoutTruncated || result.StderrTruncated:
		return "output_truncated"
	case err == nil && len(result.Stdout) == 0 && len(result.Stderr) == 0:
		return "success"
	case err == nil:
		return "unexpected_output"
	}
	var executableError *exec.Error
	var pathError *os.PathError
	if errors.As(err, &executableError) || errors.As(err, &pathError) {
		return "runtime_unavailable"
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if status := liveGeneratedImageRuntimeExitStatus(exitError.ExitCode()); status != "unclassified" {
			return status
		}
	}
	combined := make([]byte, 0, len(result.Stdout)+len(result.Stderr))
	combined = append(combined, result.Stdout...)
	combined = append(combined, result.Stderr...)
	lower := bytes.ToLower(combined)
	clear(combined)
	defer clear(lower)
	switch {
	case bytes.Contains(lower, []byte("unable to find image")), bytes.Contains(lower, []byte("no such image")), bytes.Contains(lower, []byte("pull access denied")):
		return "image_unavailable"
	case bytes.Contains(lower, []byte("working directory")):
		return "workdir_invalid"
	case bytes.Contains(lower, []byte("read-only file system")):
		return "rootfs_read_only"
	case bytes.Contains(lower, []byte("operation not permitted")), bytes.Contains(lower, []byte("permission denied")):
		return "permission_denied"
	default:
		return "unclassified"
	}
}

func liveGeneratedImageRuntimeExitStatus(code int) string {
	switch code {
	case 81:
		return "workdir_mismatch"
	case 82:
		return "artifact_missing"
	case 83:
		return "artifact_unreadable"
	case 84:
		return "artifact_mismatch"
	case 85:
		return "user_mismatch"
	case 86:
		return "group_mismatch"
	case 125:
		return "docker_run_failed"
	case 126:
		return "command_not_executable"
	case 127:
		return "command_not_found"
	default:
		return "unclassified"
	}
}

func TestLiveGeneratedImageRuntimeExitStatusIsFixed(t *testing.T) {
	for code, want := range map[int]string{
		0: "unclassified", 1: "unclassified", 81: "workdir_mismatch", 82: "artifact_missing", 83: "artifact_unreadable",
		84: "artifact_mismatch", 85: "user_mismatch", 86: "group_mismatch", 125: "docker_run_failed", 126: "command_not_executable", 127: "command_not_found",
	} {
		if got := liveGeneratedImageRuntimeExitStatus(code); got != want || !validLiveGeneratedImageDiagnostic(got) {
			t.Fatalf("runtime exit status for %d = %q, want %q", code, got, want)
		}
	}
}

func TestLiveGeneratedImageRuntimeStatusIsFixedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name      string
		result    runtimeprocess.CommandResult
		err       error
		cancelled bool
		want      string
	}{
		{name: "success", want: "success"},
		{name: "unexpected output", result: runtimeprocess.CommandResult{Stderr: []byte("sensitive output")}, want: "unexpected_output"},
		{name: "image unavailable", result: runtimeprocess.CommandResult{Stderr: []byte("No such image: sensitive/image")}, err: errors.New("sensitive error"), want: "image_unavailable"},
		{name: "workdir", result: runtimeprocess.CommandResult{Stderr: []byte("working directory sensitive/path")}, err: errors.New("sensitive error"), want: "workdir_invalid"},
		{name: "rootfs", result: runtimeprocess.CommandResult{Stderr: []byte("read-only file system sensitive/path")}, err: errors.New("sensitive error"), want: "rootfs_read_only"},
		{name: "permission", result: runtimeprocess.CommandResult{Stderr: []byte("operation not permitted sensitive/path")}, err: errors.New("sensitive error"), want: "permission_denied"},
		{name: "runtime", err: &exec.Error{Name: "sensitive executable", Err: errors.New("sensitive error")}, want: "runtime_unavailable"},
		{name: "timeout", err: context.DeadlineExceeded, want: "timeout"},
		{name: "termination", err: runtimeprocess.ErrTerminationFailed, want: "termination_failed"},
		{name: "truncated", result: runtimeprocess.CommandResult{Stderr: []byte("No such image: sensitive/image"), StderrTruncated: true}, err: errors.New("sensitive error"), want: "output_truncated"},
		{name: "cancelled", err: errors.New("sensitive error"), cancelled: true, want: "cancelled"},
		{name: "unclassified", result: runtimeprocess.CommandResult{Stderr: []byte("sensitive output")}, err: errors.New("sensitive error"), want: "unclassified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.cancelled {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			stdoutBefore, stderrBefore := append([]byte(nil), test.result.Stdout...), append([]byte(nil), test.result.Stderr...)
			if got := liveGeneratedImageRuntimeStatus(ctx, test.result, test.err); got != test.want || !validLiveGeneratedImageDiagnostic(got) {
				t.Fatalf("runtime status = %q, want %q", got, test.want)
			}
			if !bytes.Equal(test.result.Stdout, stdoutBefore) || !bytes.Equal(test.result.Stderr, stderrBefore) {
				t.Fatal("runtime classification mutated the runner result")
			}
			clear(test.result.Stdout)
			clear(test.result.Stderr)
			clear(stdoutBefore)
			clear(stderrBefore)
		})
	}
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
