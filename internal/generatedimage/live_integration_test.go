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
	builderObservation := &liveGeneratedImageBuilderObservation{delegate: builder, status: "not_attempted"}
	compiler, err := NewCompiler(
		releaseReader,
		compilerPlanReader{revision: revision},
		artifacts,
		temporary,
		builderObservation,
		runner,
		CompilerOptions{BuildTimeout: 6 * time.Minute},
	)
	if err != nil {
		t.Fatal("live generated image compiler configuration failed")
	}
	artifact, err := compiler.Compile(liveContext, appID, releaseID, "app")
	if err != nil {
		driftStatus := "not_applicable"
		if builderObservation.status == string(BuilderDriftDetected) {
			driftStatus = liveGeneratedImageBuilderDriftStatus(liveContext, builder, identity, dockerEnvironment)
		}
		t.Fatalf("live generated image production compile failed: diagnostic=%s,builder_status=%s,builder_drift=%s", liveGeneratedImageFailureCode(err), builderObservation.status, driftStatus)
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

type liveGeneratedImageBuilderObservation struct {
	delegate builderPreparer
	status   string
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

func liveGeneratedImageBuilderDriftStatus(ctx context.Context, manager *BuilderManager, identity builderIdentity, environment []string) string {
	if ctx.Err() != nil {
		return "observation_cancelled"
	}
	observationContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	network, found, err := manager.inspectNetwork(observationContext, identity.NetworkName, environment)
	if err != nil {
		return "network_unavailable"
	}
	if !found {
		return "network_missing"
	}
	if !matchesNetwork(network, identity) {
		return "network_mismatch"
	}
	network = dockerNetwork{}

	container, found, err := manager.inspectBuildkitContainer(observationContext, identity, environment)
	if err != nil {
		return "container_unavailable"
	}
	if !found {
		return "container_missing"
	}
	containerStatus := liveGeneratedImageBuildkitContainerDrift(container, identity, manager.options.StateQuotaBytes)
	clearLiveGeneratedImageBuildkitContainer(&container)
	if containerStatus != "none" {
		return containerStatus
	}

	builder, found, err := manager.findBuilder(observationContext, identity.BuilderName, environment)
	if err != nil {
		return "builder_unavailable"
	}
	if !found {
		return "builder_missing"
	}
	builderStatus := liveGeneratedImageBuildxBuilderDrift(builder, identity)
	clearLiveGeneratedImageBuildxBuilder(&builder)
	return builderStatus
}

func liveGeneratedImageBuildkitContainerDrift(container buildkitContainer, identity builderIdentity, quotaBytes int64) string {
	if !lowerHex(container.ID, 64) || container.Name != "/"+buildkitContainerName(identity) {
		return "container_identity"
	}
	if !validImageContentID(container.Image) || container.Config.Image != buildkitImage {
		return "container_image"
	}
	if !equalStringSlices(container.Config.Cmd, buildkitCommand(quotaBytes)) {
		return "container_command"
	}
	for key, value := range map[string]string{
		"rig.controller": "generated-builder", "rig.builder": identity.BuilderName,
		"rig.network": identity.NetworkName, "rig.quota.bytes": fmt.Sprintf("%d", quotaBytes),
	} {
		if container.Config.Labels[key] != value {
			return "container_labels"
		}
	}
	if container.HostConfig.Memory != buildkitMemoryLimit(quotaBytes) || container.HostConfig.MemorySwap != buildkitMemoryLimit(quotaBytes) || container.HostConfig.CPUPeriod != 100000 || container.HostConfig.CPUQuota != 100000 || container.HostConfig.PidsLimit != buildkitPIDsLimit || !container.HostConfig.Privileged || len(container.HostConfig.Binds) != 0 || len(container.HostConfig.PortBindings) != 0 || container.HostConfig.RestartPolicy.Name != "unless-stopped" || container.HostConfig.LogConfig.Type != "json-file" || len(container.HostConfig.LogConfig.Config) != 2 || container.HostConfig.LogConfig.Config["max-size"] != "10m" || container.HostConfig.LogConfig.Config["max-file"] != "1" {
		return "container_resources"
	}
	if container.HostConfig.NetworkMode != identity.NetworkName || len(container.NetworkSettings.Networks) != 1 {
		return "container_network"
	}
	if _, connected := container.NetworkSettings.Networks[identity.NetworkName]; !connected {
		return "container_network"
	}
	if len(container.HostConfig.Mounts) != 1 {
		return "container_configured_mount"
	}
	configured := container.HostConfig.Mounts[0]
	if configured.Type != "tmpfs" || configured.Source != "" || configured.Target != buildkitStatePath || configured.ReadOnly || configured.TmpfsOptions == nil || configured.TmpfsOptions.SizeBytes != quotaBytes || configured.TmpfsOptions.Mode != 0o700 {
		return "container_configured_mount"
	}
	if len(container.Mounts) != 1 {
		return "container_active_mount"
	}
	active := container.Mounts[0]
	if active.Type != "tmpfs" || active.Source != "" || active.Destination != buildkitStatePath || !active.RW || active.Mode != "" || active.Propagation != "" {
		return "container_active_mount"
	}
	if !buildkitContainerReady(container) {
		return "container_lifecycle"
	}
	return "none"
}

func liveGeneratedImageBuildxBuilderDrift(builder buildxBuilder, identity builderIdentity) string {
	if builder.Name != identity.BuilderName {
		return "builder_identity"
	}
	if builder.Driver != "remote" {
		return "builder_driver"
	}
	if len(builder.Nodes) != 1 {
		return "builder_nodes"
	}
	if builder.Nodes[0].Name != identity.NodeName {
		return "builder_node_name"
	}
	if builder.Nodes[0].Endpoint != buildkitRemoteEndpoint(identity) {
		return "builder_endpoint"
	}
	return "none"
}

func clearLiveGeneratedImageBuildkitContainer(container *buildkitContainer) {
	for key := range container.Config.Labels {
		delete(container.Config.Labels, key)
	}
	for key := range container.HostConfig.LogConfig.Config {
		delete(container.HostConfig.LogConfig.Config, key)
	}
	for key, value := range container.NetworkSettings.Networks {
		clear(value)
		delete(container.NetworkSettings.Networks, key)
	}
	*container = buildkitContainer{}
}

func clearLiveGeneratedImageBuildxBuilder(builder *buildxBuilder) {
	for index := range builder.Nodes {
		builder.Nodes[index] = buildxNode{}
	}
	*builder = buildxBuilder{}
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

func TestLiveGeneratedImageBuilderDriftClassifiersUseOnlyFixedGroups(t *testing.T) {
	identity := builderIdentity{
		Schema: builderStateSchema, BuilderName: "rig-buildkit-0123456789abcdef01234567",
		NodeName: "rig-node-0123456789abcdef01234567", NetworkName: "rig-buildnet-0123456789abcdef01234567",
	}

	for _, test := range []struct {
		name   string
		mutate func(*buildkitContainer)
		want   string
	}{
		{"valid", func(*buildkitContainer) {}, "none"},
		{"identity", func(container *buildkitContainer) { container.ID = "sensitive/raw/id" }, "container_identity"},
		{"resources", func(container *buildkitContainer) { container.HostConfig.Memory-- }, "container_resources"},
		{"network", func(container *buildkitContainer) { container.NetworkSettings.Networks = nil }, "container_network"},
		{"configured mount", func(container *buildkitContainer) { container.HostConfig.Mounts[0].TmpfsOptions.Mode-- }, "container_configured_mount"},
		{"active mount", func(container *buildkitContainer) { container.Mounts[0].Mode = "sensitive/raw/mode" }, "container_active_mount"},
		{"lifecycle", func(container *buildkitContainer) { container.State.Restarting = true }, "container_lifecycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := validBuildkitContainer(identity, defaultStateQuotaBytes)
			test.mutate(&container)
			if got := liveGeneratedImageBuildkitContainerDrift(container, identity, defaultStateQuotaBytes); got != test.want || !validLiveGeneratedImageDiagnostic(got) {
				t.Fatalf("container drift group = %q, want %q", got, test.want)
			}
			clearLiveGeneratedImageBuildkitContainer(&container)
			if container.ID != "" || container.Name != "" || container.Config.Labels != nil || container.HostConfig.Mounts != nil || container.NetworkSettings.Networks != nil || container.Mounts != nil {
				t.Fatal("container observation was not cleared")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*buildxBuilder)
		want   string
	}{
		{"valid", func(*buildxBuilder) {}, "none"},
		{"driver", func(builder *buildxBuilder) { builder.Driver = "sensitive/raw/driver" }, "builder_driver"},
		{"nodes", func(builder *buildxBuilder) { builder.Nodes = nil }, "builder_nodes"},
		{"endpoint", func(builder *buildxBuilder) { builder.Nodes[0].Endpoint = "sensitive/raw/endpoint" }, "builder_endpoint"},
	} {
		t.Run("builder "+test.name, func(t *testing.T) {
			builder := buildxBuilder{Name: identity.BuilderName, Driver: "remote", Nodes: []buildxNode{{Name: identity.NodeName, Endpoint: buildkitRemoteEndpoint(identity)}}}
			test.mutate(&builder)
			if got := liveGeneratedImageBuildxBuilderDrift(builder, identity); got != test.want || !validLiveGeneratedImageDiagnostic(got) {
				t.Fatalf("builder drift group = %q, want %q", got, test.want)
			}
			clearLiveGeneratedImageBuildxBuilder(&builder)
			if builder.Name != "" || builder.Driver != "" || builder.Nodes != nil {
				t.Fatal("builder observation was not cleared")
			}
		})
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
