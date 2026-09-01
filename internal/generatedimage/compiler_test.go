package generatedimage

import (
	"context"
	"errors"
	"os"
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

type compilerReleaseReader struct {
	release      releasesnapshot.Release
	calls        int
	beforeSecond func()
}

func (r *compilerReleaseReader) ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error) {
	r.calls++
	if r.calls == 2 && r.beforeSecond != nil {
		r.beforeSecond()
	}
	return r.release, nil
}

type compilerPlanReader struct {
	revision deploymentplans.DeploymentPlanRevision
}

func (r compilerPlanReader) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return r.revision, nil
}

type compilerArtifactWriter struct {
	artifact    Artifact
	failed      DiagnosticCode
	canceled    bool
	completeErr error
}

func (w *compilerArtifactWriter) Begin(_ context.Context, input BeginArtifactInput) (Artifact, bool, error) {
	w.artifact = Artifact{
		ID: uuid.NewString(), ReleaseID: input.ReleaseID, DeploymentPlanRevisionID: input.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: input.DeploymentPlanRevisionNumber, ComponentID: input.ComponentID,
		CompilerVersion: input.CompilerVersion, BuildDefinitionDigest: input.BuildDefinitionDigest, AttemptNumber: 1, State: ArtifactBuilding,
	}
	return w.artifact, true, nil
}

func (w *compilerArtifactWriter) Complete(_ context.Context, id, imageID string) (Artifact, error) {
	if w.completeErr != nil {
		return Artifact{}, w.completeErr
	}
	if id != w.artifact.ID {
		return Artifact{}, errors.New("wrong artifact")
	}
	w.artifact.ImageContentID, w.artifact.State = imageID, ArtifactReady
	return w.artifact, nil
}

func (w *compilerArtifactWriter) Fail(_ context.Context, _ string, code DiagnosticCode) (Artifact, error) {
	w.failed, w.artifact.DiagnosticCode, w.artifact.State = code, code, ArtifactFailed
	return w.artifact, nil
}

func (w *compilerArtifactWriter) Cancel(context.Context, string) (Artifact, error) {
	w.canceled, w.artifact.State = true, ArtifactCancelled
	return w.artifact, nil
}

type compilerRunner struct {
	request runtimeprocess.CommandRequest
	result  runtimeprocess.CommandResult
	err     error
	run     func(runtimeprocess.CommandRequest) error
}

type compilerBuilder struct {
	session BuilderSession
	err     error
	calls   int
}

func (b *compilerBuilder) Prepare(context.Context) (BuilderSession, error) {
	b.calls++
	return b.session, b.err
}

func (r *compilerRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.request = request
	if r.run != nil {
		if err := r.run(request); err != nil {
			return r.result, err
		}
	}
	return r.result, r.err
}

func TestCompilerKeepsCommandsAndConfigurationOutOfDockerArguments(t *testing.T) {
	fixture := newCompilerFixture(t)
	t.Setenv("APP_DATABASE_PASSWORD", "configuration-must-not-reach-build")
	buildCommand := `npm run build && node -e "console.log('${VALUE}', '$(whoami)', '\\path', '雪')"`
	fixture.revision.Plan.Components[0].BuildCommand = buildCommand
	fixture.revision.Plan.Components[0].RunCommand = `node server.js --label="${VALUE}"`
	fixture.runner.result = runtimeprocess.CommandResult{Stdout: []byte("untrusted output secret"), Stderr: []byte("untrusted error secret")}
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		installPath := secretSource(t, request.Args, "rig-install-command")
		buildPath := secretSource(t, request.Args, "rig-build-command")
		assertFileEquals(t, installPath, fixture.revision.Plan.Components[0].InstallBehavior)
		assertFileEquals(t, buildPath, buildCommand)
		containerfile := flagValue(t, request.Args, "--file")
		body, err := os.ReadFile(containerfile)
		if err != nil {
			return err
		}
		serialized := strings.Join(append(append([]string{request.Executable}, request.Args...), request.Env...), "\n") + "\n" + string(body)
		for _, forbidden := range []string{buildCommand, fixture.revision.Plan.Components[0].RunCommand, "configuration-must-not-reach-build", "APP_DATABASE_PASSWORD"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("untrusted command or runtime configuration crossed compiler boundary: %q", forbidden)
			}
		}
		if _, err := os.Stat(filepath.Join(request.Args[len(request.Args)-1], "source", ".env")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf(".env reached the build context: %v", err)
		}
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("1", 64)), 0o600)
	}

	artifact, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.State != ArtifactReady || artifact.ImageContentID != "sha256:"+strings.Repeat("1", 64) {
		t.Fatalf("artifact = %#v", artifact)
	}
	if fixture.releaseReader.calls != 2 {
		t.Fatalf("immutable release was validated %d times, want 2", fixture.releaseReader.calls)
	}
	if fixture.builder.calls != 1 {
		t.Fatalf("builder preparation calls = %d, want 1", fixture.builder.calls)
	}
	for _, output := range append(fixture.runner.result.Stdout, fixture.runner.result.Stderr...) {
		if output != 0 {
			t.Fatal("raw build output was not cleared")
		}
	}
	if _, err := os.Stat(filepath.Dir(flagValue(t, fixture.runner.request.Args, "--file"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected build material remains after success: %v", err)
	}
}

func TestCompilerStopsStructuralDriftBeforeDocker(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.releaseReader.beforeSecond = func() {
		writeTestFile(t, filepath.Join(fixture.release.WorkspacePath, "package.json"), `{"name":"demo","scripts":{"build":"changed","start":"node server.js"},"dependencies":{"express":"1.0.0"}}`)
	}
	_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if !IsCompileCode(err, string(DiagnosticSourceIntegrityFailed)) || fixture.artifacts.failed != DiagnosticSourceIntegrityFailed {
		t.Fatalf("structural drift result = %v, artifact diagnostic = %q", err, fixture.artifacts.failed)
	}
	if fixture.runner.request.Executable != "" {
		t.Fatal("Docker was invoked after source drift")
	}
	if fixture.builder.calls != 0 {
		t.Fatal("builder was mutated after source drift")
	}
}

func TestCompilerUsesStableDiagnosticsAndCleansFailureMaterial(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.runner.result = runtimeprocess.CommandResult{Stderr: []byte("no space left on device: raw details")}
	fixture.runner.err = errors.New("exit status 1")
	_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if !IsCompileCode(err, string(DiagnosticBuildDiskExhausted)) || fixture.artifacts.failed != DiagnosticBuildDiskExhausted {
		t.Fatalf("disk failure result = %v, diagnostic = %q", err, fixture.artifacts.failed)
	}
	if _, statErr := os.Stat(fixture.runner.request.Directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("protected build material remains after failure: %v", statErr)
	}
}

func TestCompilerFailsClosedWhenOwnedBuilderIsUnavailable(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.builder.err = &BuilderError{Code: BuilderDriftDetected}
	_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if !IsCompileCode(err, string(DiagnosticInternalError)) || fixture.artifacts.failed != DiagnosticInternalError {
		t.Fatalf("builder drift result = %v, diagnostic = %q", err, fixture.artifacts.failed)
	}
	if fixture.runner.request.Executable != "" {
		t.Fatal("image build ran with a drifted builder")
	}

	fixture = newCompilerFixture(t)
	fixture.builder.err = &BuilderError{Code: BuilderRuntimeUnavailable}
	_, err = fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if !IsCompileCode(err, string(DiagnosticRuntimeUnavailable)) || fixture.artifacts.failed != DiagnosticRuntimeUnavailable {
		t.Fatalf("builder runtime result = %v, diagnostic = %q", err, fixture.artifacts.failed)
	}
}

func TestCompilerTerminalizesAttemptWhenCompletionPersistenceFails(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.artifacts.completeErr = errors.New("database unavailable")
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("2", 64)), 0o600)
	}
	_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if !IsCompileCode(err, string(DiagnosticInternalError)) || fixture.artifacts.failed != DiagnosticInternalError || fixture.artifacts.artifact.State != ArtifactFailed {
		t.Fatalf("completion persistence result = %v, artifact = %#v", err, fixture.artifacts.artifact)
	}
}

func TestClassifyBuildResultUsesOnlyStableBoundaryState(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, test := range map[string]struct {
		ctx    context.Context
		result runtimeprocess.CommandResult
		err    error
		want   DiagnosticCode
	}{
		"cancelled":          {ctx: canceled, err: errors.New("raw"), want: DiagnosticBuildCancelled},
		"termination failed": {ctx: context.Background(), err: runtimeprocess.ErrTerminationFailed, want: DiagnosticProcessTerminationFailed},
		"timeout":            {ctx: context.Background(), err: context.DeadlineExceeded, want: DiagnosticBuildTimeout},
		"missing client":     {ctx: context.Background(), err: &os.PathError{Op: "fork", Path: "secret-path", Err: os.ErrNotExist}, want: DiagnosticRuntimeUnavailable},
		"truncated":          {ctx: context.Background(), result: runtimeprocess.CommandResult{StdoutTruncated: true}, want: DiagnosticBuildOutputTruncated},
		"daemon unavailable": {ctx: context.Background(), result: runtimeprocess.CommandResult{Stderr: []byte("Cannot connect to the Docker daemon at private endpoint")}, err: errors.New("raw"), want: DiagnosticRuntimeUnavailable},
		"quota exceeded":     {ctx: context.Background(), result: runtimeprocess.CommandResult{Stderr: []byte("disk quota exceeded")}, err: errors.New("raw"), want: DiagnosticBuildDiskExhausted},
		"generic":            {ctx: context.Background(), result: runtimeprocess.CommandResult{Stderr: []byte("repository-secret")}, err: errors.New("repository-secret"), want: DiagnosticBuildFailed},
	} {
		t.Run(name, func(t *testing.T) {
			if got := classifyBuildResult(test.ctx, test.result, test.err); got != test.want {
				t.Fatalf("diagnostic = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompilerRejectsBuilderSessionWithoutVerifiedHardQuota(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.builder.session.storageQuotaBytes = 0
	_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
	if !IsCompileCode(err, string(DiagnosticInternalError)) || fixture.artifacts.failed != DiagnosticInternalError {
		t.Fatalf("unverified quota result = %v, diagnostic = %q", err, fixture.artifacts.failed)
	}
	if fixture.runner.request.Executable != "" {
		t.Fatal("build ran without a verified hard-quota session")
	}
}

func TestBuilderQuotaErrorsUseStableCompilerDiagnostics(t *testing.T) {
	for name, test := range map[string]struct {
		builder BuilderErrorCode
		want    DiagnosticCode
	}{
		"unsupported": {builder: BuilderHardQuotaUnavailable, want: DiagnosticBuildCapacityExceeded},
		"exhausted":   {builder: BuilderHardQuotaExhausted, want: DiagnosticBuildDiskExhausted},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			fixture.builder.err = &BuilderError{Code: test.builder}
			_, err := fixture.compiler.Compile(context.Background(), fixture.release.AppID, fixture.release.ID, "app")
			if !IsCompileCode(err, string(test.want)) || fixture.artifacts.failed != test.want {
				t.Fatalf("quota result = %v, diagnostic = %q", err, fixture.artifacts.failed)
			}
			if fixture.runner.request.Executable != "" {
				t.Fatal("build ran after quota preparation failed")
			}
		})
	}
}

func TestReadImageIDRejectsMalformedAndLinkedFiles(t *testing.T) {
	for name, value := range map[string]string{
		"tag":        "rig-generated/app:latest",
		"uppercase":  "sha256:" + strings.Repeat("A", 64),
		"short":      "sha256:abcd",
		"additional": "sha256:" + strings.Repeat("a", 64) + " extra",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.id")
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readImageID(path); err == nil {
				t.Fatalf("malformed image ID %q was accepted", value)
			}
		})
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("sha256:"+strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "image.id")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := readImageID(link); err == nil {
		t.Fatal("linked image ID file was accepted")
	}
}

type compilerFixture struct {
	compiler      *Compiler
	release       releasesnapshot.Release
	revision      deploymentplans.DeploymentPlanRevision
	releaseReader *compilerReleaseReader
	artifacts     *compilerArtifactWriter
	runner        *compilerRunner
	builder       *compilerBuilder
}

func newCompilerFixture(t *testing.T) compilerFixture {
	t.Helper()
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "package.json"), `{"name":"demo","scripts":{"build":"npm run compile","start":"node server.js"},"dependencies":{"express":"1.0.0"}}`)
	writeTestFile(t, filepath.Join(workspace, "package-lock.json"), `{"lockfileVersion":3,"packages":{}}`)
	writeTestFile(t, filepath.Join(workspace, "server.js"), "console.log('ready')")
	writeTestFile(t, filepath.Join(workspace, ".env"), "DATABASE_URL=do-not-copy")
	inspection, err := sourceinspection.InspectLocalContext(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	appID, releaseID, revisionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	release := releasesnapshot.Release{
		ID: releaseID, AppID: appID, SourceProvider: "local", RepositoryID: 0, ResolvedSHA: inspection.Analysis.StructuralFingerprint,
		WorkspaceTreeSHA256: strings.Repeat("c", 64), WorkspacePath: workspace, WorkspaceState: releasesnapshot.WorkspaceStateReady,
		DeploymentPlanRevisionID: revisionID, DeploymentPlanRevisionNumber: 1,
	}
	revision := deploymentplans.DeploymentPlanRevision{
		ID: revisionID, AppID: appID, RevisionNumber: 1, CanonicalDigest: strings.Repeat("a", 64),
		Plan: deploymentplans.Plan{
			Strategy:   deploymentplans.StrategyGeneratedNode,
			Detector:   deploymentplans.Detector{Name: "projectanalysis", Version: projectanalysis.SchemaVersion, SourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint},
			Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint},
			Components: []deploymentplans.Component{{Name: "app", Role: "server", RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", NodeVersion: "24", BuildCommand: "npm run compile", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}},
		},
	}
	releaseReader := &compilerReleaseReader{release: release}
	artifacts := &compilerArtifactWriter{}
	runner := &compilerRunner{}
	dockerConfig, buildxConfig := filepath.Join(t.TempDir(), "docker-config"), filepath.Join(t.TempDir(), "buildx-config")
	for _, directory := range []string{dockerConfig, buildxConfig} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	builder := &compilerBuilder{session: BuilderSession{
		DockerExecutable: filepath.Join(t.TempDir(), "docker.exe"), BuilderName: "rig-buildkit-0123456789abcdef01234567",
		environment: []string{"BUILDX_CONFIG=" + buildxConfig, "DOCKER_CONFIG=" + dockerConfig}, storageQuotaBytes: defaultStateQuotaBytes,
	}}
	temporary, err := securetemp.NewGeneratedBuild(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := NewCompiler(releaseReader, compilerPlanReader{revision: revision}, artifacts, temporary, builder, runner, CompilerOptions{BuildTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return compilerFixture{compiler: compiler, release: release, revision: revision, releaseReader: releaseReader, artifacts: artifacts, runner: runner, builder: builder}
}

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	t.Fatalf("flag %s not found in %#v", flag, args)
	return ""
}

func secretSource(t *testing.T, args []string, id string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--secret" {
			continue
		}
		prefix := "id=" + id + ",src="
		if strings.HasPrefix(args[index+1], prefix) {
			return strings.TrimPrefix(args[index+1], prefix)
		}
	}
	t.Fatalf("secret %s not found in %#v", id, args)
	return ""
}

func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatalf("%s = %q, %v; want exact %q", path, body, err, want)
	}
}
