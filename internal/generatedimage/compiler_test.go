package generatedimage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
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

type compilerConfiguration struct {
	values             []appconfig.ValueInput
	err                error
	calls              int
	lastRevisionID     string
	lastRevisionNumber int64
	lastPlanID         string
	lastPlanNumber     int64
	lastComponent      string
}

func (c *compilerConfiguration) ExportComponentBuildForExecution(_ context.Context, _ string, revisionID string, revisionNumber int64, planID string, planNumber int64, component string) (appconfig.ExecutionConfiguration, error) {
	c.calls++
	c.lastRevisionID, c.lastRevisionNumber = revisionID, revisionNumber
	c.lastPlanID, c.lastPlanNumber, c.lastComponent = planID, planNumber, component
	if c.err != nil {
		return appconfig.ExecutionConfiguration{}, c.err
	}
	return appconfig.ExecutionConfiguration{RevisionID: revisionID, RevisionNumber: revisionNumber, PublicBuildValues: append([]appconfig.ValueInput(nil), c.values...)}, nil
}

func (r compilerPlanReader) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return r.revision, nil
}

type compilerRunner struct {
	request runtimeprocess.CommandRequest
	result  runtimeprocess.CommandResult
	err     error
	run     func(runtimeprocess.CommandRequest) error
	calls   int
}

type compilerCommandRunner struct {
	build  *compilerRunner
	daemon *builderDaemonFake
}

func (r *compilerCommandRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] == "build" {
		return r.build.Run(ctx, request)
	}
	return r.daemon.Run(ctx, request)
}

type compilerImageValidator struct {
	specs []generatedruntime.ImageSpec
	err   error
}

func (v *compilerImageValidator) ValidateImage(_ context.Context, spec generatedruntime.ImageSpec) error {
	v.specs = append(v.specs, spec)
	return v.err
}

func (r *compilerRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	r.calls++
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
		if !hasArgumentPair(request.Args, "--label", "io.rig.component=app") {
			t.Fatalf("component provenance label is missing: %#v", request.Args)
		}
		if !hasArgumentPair(request.Args, "--label", "io.rig.role=server") {
			t.Fatalf("component role provenance label is missing: %#v", request.Args)
		}
		contextDirectory := request.Args[len(request.Args)-1]
		assertFileEquals(t, filepath.Join(contextDirectory, "rig", "root.path"), ".")
		assertFileEquals(t, filepath.Join(contextDirectory, "rig", "install.path"), ".")
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

	artifact, err := fixture.compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if artifact.State != ArtifactReady || artifact.ImageContentID != "sha256:"+strings.Repeat("1", 64) {
		t.Fatalf("artifact = %#v", artifact)
	}
	if fixture.releaseReader.calls != 2 {
		t.Fatalf("immutable release was validated %d times, want 2", fixture.releaseReader.calls)
	}
	if fixture.builderPrepareCalls() != 1 {
		t.Fatalf("builder preparation calls = %d, want 1", fixture.builderPrepareCalls())
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

func TestCompilerBuildsWithOnlyPinnedPublicValues(t *testing.T) {
	fixture := newCompilerFixture(t)
	configurationID := fixture.release.ConfigurationRevisionID
	const publicValue = `literal $() ' " = & \\ 雪`
	expectedPublicValue := publicValue
	fixture.config.values = []appconfig.ValueInput{{Key: "VITE_LABEL", Value: publicValue}, {Key: "API_PUBLIC_MARKER", Value: "visible"}}
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		path := secretSource(t, request.Args, "rig-public-build-values")
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var values []appconfig.ValueInput
		if err := json.Unmarshal(body, &values); err != nil {
			return err
		}
		if len(values) != 2 || values[0].Key != "API_PUBLIC_MARKER" || values[1].Key != "VITE_LABEL" || values[1].Value != expectedPublicValue {
			t.Fatalf("public input was not transported literally in canonical order: %#v", values)
		}
		contextDirectory := request.Args[len(request.Args)-1]
		containerfile := flagValue(t, request.Args, "--file")
		recipe, err := os.ReadFile(containerfile)
		if err != nil {
			return err
		}
		metadata := strings.Join(append(append([]string{request.Executable}, request.Args...), request.Env...), "\n") + string(recipe)
		if strings.Contains(metadata, expectedPublicValue) || strings.Contains(metadata, "VITE_LABEL") {
			t.Fatal("public value or key was serialized into Docker arguments, environment, or recipe")
		}
		if _, err := os.Stat(filepath.Join(contextDirectory, "public-build-values.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("public input reached Docker context: %v", err)
		}
		if !strings.Contains(string(recipe), "--mount=type=secret,id=rig-public-build-values") || !strings.Contains(string(recipe), "run-build.mjs") {
			t.Fatal("build recipe did not mount and consume protected public input")
		}
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("a", 64)), 0o600)
	}
	first, err := fixture.compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.config.lastRevisionID != configurationID || fixture.config.lastRevisionNumber != 1 || fixture.config.lastPlanID != fixture.revision.ID || fixture.config.lastPlanNumber != 1 || fixture.config.lastComponent != "app" {
		t.Fatalf("build config exporter received wrong pins: %#v", fixture.config)
	}
	secondFixture := newCompilerFixture(t)
	secondFixture.config.values = append([]appconfig.ValueInput(nil), fixture.config.values...)
	secondFixture.config.values[0].Value = "changed"
	expectedPublicValue = "changed"
	secondFixture.runner.run = fixture.runner.run
	second, err := secondFixture.compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.BuildDefinitionDigest == second.BuildDefinitionDigest || first.ID == second.ID || fixture.config.calls != 1 || secondFixture.config.calls != 1 {
		t.Fatal("changed public value reused a build definition or artifact")
	}
	if _, err := os.Stat(filepath.Dir(secretSource(t, secondFixture.runner.request.Args, "rig-public-build-values"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected build input remains after success: %v", err)
	}
}

func TestCompilerEmptyBuildScopeAndLegacyReview(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		assertFileEquals(t, secretSource(t, request.Args, "rig-public-build-values"), "[]")
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("b", 64)), 0o600)
	}
	if _, err := fixture.compile(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.config.err = &appconfig.Error{Code: "configuration_review_required"}
	fixture.runner.request = runtimeprocess.CommandRequest{}
	if _, err := fixture.compile(context.Background()); !IsCompileCode(err, "configuration_review_required") {
		t.Fatalf("legacy unscoped config diagnostic = %v", err)
	}
	if fixture.runner.request.Executable != "" {
		t.Fatal("legacy unscoped config reached Docker")
	}
}

func TestCompilerStopsStructuralDriftBeforeDocker(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.releaseReader.beforeSecond = func() {
		writeTestFile(t, filepath.Join(fixture.release.WorkspacePath, "package.json"), `{"name":"demo","scripts":{"build":"changed","start":"node server.js"},"dependencies":{"express":"1.0.0"}}`)
	}
	_, err := fixture.compile(context.Background())
	artifact := fixture.latestArtifact(t)
	if !IsCompileCode(err, string(DiagnosticSourceIntegrityFailed)) || artifact.State != ArtifactFailed || artifact.DiagnosticCode != DiagnosticSourceIntegrityFailed {
		t.Fatalf("structural drift result = %v, artifact = %#v", err, artifact)
	}
	if fixture.runner.request.Executable != "" {
		t.Fatal("Docker was invoked after source drift")
	}
	if fixture.builderPrepareCalls() != 0 {
		t.Fatal("builder was mutated after source drift")
	}
}

func TestCompilerUsesStableDiagnosticsAndCleansFailureMaterial(t *testing.T) {
	fixture := newCompilerFixture(t)
	fixture.runner.result = runtimeprocess.CommandResult{Stderr: []byte("no space left on device: raw details")}
	fixture.runner.err = errors.New("exit status 1")
	_, err := fixture.compile(context.Background())
	artifact := fixture.latestArtifact(t)
	if !IsCompileCode(err, string(DiagnosticBuildDiskExhausted)) || !IsCompileUncertain(err) || artifact.State != ArtifactBuilding || artifact.DiagnosticCode != "" {
		t.Fatalf("disk failure result = %v, artifact = %#v", err, artifact)
	}
	if _, statErr := os.Stat(fixture.runner.request.Directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("protected build material remains after failure: %v", statErr)
	}
}

func TestCompilerFailsClosedWhenOwnedBuilderIsUnavailable(t *testing.T) {
	fixture := newCompilerFixture(t)
	identity, _, err := fixture.builder.preparePersistentState()
	if err != nil {
		t.Fatal(err)
	}
	fixture.daemon.network = &dockerNetwork{Name: identity.NetworkName, Driver: "bridge", Scope: "local"}
	_, err = fixture.compile(context.Background())
	if !IsCompileCode(err, string(DiagnosticInternalError)) || !IsCompileUncertain(err) || fixture.latestArtifact(t).State != ArtifactBuilding {
		t.Fatalf("builder drift result = %v, artifact = %#v", err, fixture.latestArtifact(t))
	}
	if fixture.runner.request.Executable != "" {
		t.Fatal("image build ran with a drifted builder")
	}

	fixture = newCompilerFixture(t)
	fixture.daemon.err = errors.New("daemon unavailable")
	_, err = fixture.compile(context.Background())
	if !IsCompileCode(err, string(DiagnosticRuntimeUnavailable)) || !IsCompileUncertain(err) || fixture.latestArtifact(t).State != ArtifactBuilding {
		t.Fatalf("builder runtime result = %v, artifact = %#v", err, fixture.latestArtifact(t))
	}
}

func TestCompilerRevalidatesAuthorityImmediatelyBeforeBuildLoad(t *testing.T) {
	fixture := newCompilerFixture(t)
	barrierCalls := 0
	var beforeArtifact Artifact
	var beforeComponent generatedruntimestate.Component
	fixture.compiler.afterBuildCommandPrepared = func(request runtimeprocess.CommandRequest) {
		if len(request.Args) < 2 || request.Args[0] != "buildx" || request.Args[1] != "build" {
			t.Fatalf("unexpected prepared command: %#v", request.Args)
		}
		barrierCalls++
		beforeArtifact = fixture.latestArtifact(t)
		state := generatedruntimestate.New(fixture.authority.db)
		var err error
		beforeComponent, err = state.Component(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
		if err != nil {
			t.Fatal(err)
		}
		invalidateGeneratedImageJobAttempt(t, fixture.authority)
	}

	artifact, err := fixture.compile(context.Background())
	if artifact != (Artifact{}) || !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	if barrierCalls != 1 {
		t.Fatalf("prepared-command barrier calls=%d", barrierCalls)
	}
	if len(fixture.runner.request.Args) != 0 {
		t.Fatalf("refused build entered runner: %#v", fixture.runner.request.Args)
	}
	afterArtifact := fixture.latestArtifact(t)
	if afterArtifact != beforeArtifact {
		t.Fatalf("artifact changed after refusal: before=%#v after=%#v", beforeArtifact, afterArtifact)
	}
	state := generatedruntimestate.New(fixture.authority.db)
	afterComponent, componentErr := state.Component(context.Background(), fixture.authority.claim.ApplicationID(), fixture.request.DeploymentID, fixture.request.ComponentName)
	if componentErr != nil || afterComponent != beforeComponent {
		t.Fatalf("component changed after refusal: before=%#v after=%#v err=%v", beforeComponent, afterComponent, componentErr)
	}
}

func TestCompilerRetainsAttemptWhenCompletionPersistenceFails(t *testing.T) {
	fixture := newCompilerFixture(t)
	if _, err := fixture.authority.db.Exec(`CREATE TRIGGER generated_image_result_fault BEFORE UPDATE OF state ON generated_runtime_components WHEN NEW.state='image_ready' BEGIN SELECT RAISE(ABORT,'generated image result fault'); END`); err != nil {
		t.Fatal(err)
	}
	fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
		return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("2", 64)), 0o600)
	}
	_, err := fixture.compile(context.Background())
	artifact := fixture.latestArtifact(t)
	if !IsCompileCode(err, "image_result_unavailable") || !IsCompileUncertain(err) || artifact.State != ArtifactBuilding || artifact.DiagnosticCode != "" {
		t.Fatalf("completion persistence result = %v, artifact = %#v", err, artifact)
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
	invalid := validDockerInfo()
	invalid.MemoryLimit = false
	fixture.daemon.infoOverride = &invalid
	_, err := fixture.compile(context.Background())
	if !IsCompileCode(err, string(DiagnosticBuildCapacityExceeded)) || !IsCompileUncertain(err) || fixture.latestArtifact(t).State != ArtifactBuilding {
		t.Fatalf("unverified quota result = %v, artifact = %#v", err, fixture.latestArtifact(t))
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
			fixture.daemon.containerRunErr = errors.New("container run failed")
			if test.builder == BuilderHardQuotaUnavailable {
				fixture.daemon.containerRunStderr = []byte("invalid mount config: tmpfs not supported")
			} else {
				fixture.daemon.containerRunStderr = []byte("no space left on device")
			}
			_, err := fixture.compile(context.Background())
			if !IsCompileCode(err, string(test.want)) || !IsCompileUncertain(err) || fixture.latestArtifact(t).State != ArtifactBuilding {
				t.Fatalf("quota result = %v, artifact = %#v", err, fixture.latestArtifact(t))
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
	authority     *generatedImageTestAuthority
	request       CompileRequest
	runner        *compilerRunner
	commandRunner *compilerCommandRunner
	builder       *BuilderManager
	daemon        *builderDaemonFake
	validator     *compilerImageValidator
	temporary     *securetemp.Manager
	config        *compilerConfiguration
}

func newCompilerFixture(t *testing.T) compilerFixture {
	return newCompilerFixtureWithAuthority(t, func(t *testing.T, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision) *generatedImageTestAuthority {
		return newGeneratedImageTestAuthority(t, release, revision, "app")
	})
}

func newCompilerFixtureWithAuthority(t *testing.T, createAuthority func(*testing.T, releasesnapshot.Release, deploymentplans.DeploymentPlanRevision) *generatedImageTestAuthority) compilerFixture {
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
	appID, releaseID, revisionID, configurationID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	release := releasesnapshot.Release{
		ID: releaseID, AppID: appID, SourceProvider: "local", RepositoryID: 0, ResolvedSHA: inspection.Analysis.StructuralFingerprint,
		WorkspaceTreeSHA256: strings.Repeat("c", 64), WorkspacePath: workspace, WorkspaceState: releasesnapshot.WorkspaceStateReady,
		ConfigurationRevisionID: configurationID, ConfigurationRevisionNumber: 1,
		DeploymentPlanRevisionID: revisionID, DeploymentPlanRevisionNumber: 1,
	}
	revision := deploymentplans.DeploymentPlanRevision{
		ID: revisionID, AppID: appID, RevisionNumber: 1, CanonicalDigest: strings.Repeat("a", 64),
		Plan: deploymentplans.Plan{
			Strategy:   deploymentplans.StrategyGeneratedNode,
			Detector:   deploymentplans.Detector{Name: "projectanalysis", Version: projectanalysis.SchemaVersion, SourceStructuralFingerprint: inspection.Analysis.StructuralFingerprint},
			Source:     deploymentplans.SourceIdentity{Provider: "local", ResolvedDigest: inspection.Analysis.StructuralFingerprint},
			Components: []deploymentplans.Component{{Name: "app", Role: "server", RootDirectory: ".", PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: ".", NodeVersion: "24", BuildCommand: "npm run compile", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}},
		},
	}
	releaseReader := &compilerReleaseReader{release: release}
	authority := createAuthority(t, release, revision)
	runner := &compilerRunner{}
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	commandRunner := &compilerCommandRunner{build: runner, daemon: daemon}
	builder, err := NewBuilderManager(authority.repository, commandRunner, BuilderManagerOptions{DataRoot: t.TempDir(), DockerExecutable: filepath.Join(t.TempDir(), "docker.exe"), PrepareTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := securetemp.NewGeneratedBuild(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := &compilerConfiguration{}
	validator := &compilerImageValidator{}
	compiler, err := NewCompiler(releaseReader, compilerPlanReader{revision: revision}, config, authority.repository, temporary, builder, commandRunner, validator, CompilerOptions{BuildTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	request := CompileRequest{Claim: authority.claim, DeploymentID: authority.request.DeploymentID, ReleaseID: release.ID,
		ConfigurationRevisionID: release.ConfigurationRevisionID, ConfigurationRevisionNumber: release.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber, ComponentName: "app"}
	return compilerFixture{compiler: compiler, release: release, revision: revision, releaseReader: releaseReader, authority: authority, request: request, runner: runner, commandRunner: commandRunner, builder: builder, daemon: daemon, validator: validator, temporary: temporary, config: config}
}

func (f *compilerFixture) compile(ctx context.Context) (Artifact, error) {
	return f.compiler.Compile(deploymenteffects.WithScope(ctx, f.authority.scope), f.request)
}

func (f *compilerFixture) rebind(t *testing.T, component string) {
	f.rebindWithConfiguration(t, component, f.release.ConfigurationRevisionID, f.release.ConfigurationRevisionNumber)
}

func (f *compilerFixture) rebindWithConfiguration(t *testing.T, component, configurationID string, configurationNumber int64) {
	t.Helper()
	f.authority = newGeneratedImageTestAuthorityWithConfiguration(t, f.release, f.revision, component, configurationID, configurationNumber)
	var err error
	f.builder, err = NewBuilderManager(f.authority.repository, f.commandRunner, BuilderManagerOptions{DataRoot: t.TempDir(), DockerExecutable: filepath.Join(t.TempDir(), "docker.exe"), PrepareTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.compiler, err = NewCompiler(f.releaseReader, compilerPlanReader{revision: f.revision}, f.config, f.authority.repository, f.temporary, f.builder, f.commandRunner, f.validator, CompilerOptions{BuildTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.request = CompileRequest{Claim: f.authority.claim, DeploymentID: f.authority.request.DeploymentID, ReleaseID: f.release.ID,
		ConfigurationRevisionID: configurationID, ConfigurationRevisionNumber: configurationNumber,
		DeploymentPlanRevisionID: f.revision.ID, DeploymentPlanRevisionNumber: f.revision.RevisionNumber, ComponentName: component}
}

func (f *compilerFixture) builderPrepareCalls() int {
	calls := 0
	for _, request := range f.daemon.requests() {
		if len(request.Args) > 0 && request.Args[0] == "info" {
			calls++
		}
	}
	return calls
}

func (f *compilerFixture) latestArtifact(t *testing.T) Artifact {
	t.Helper()
	var id string
	if err := f.authority.db.QueryRow(`SELECT id FROM generated_image_artifacts WHERE release_id=? AND component_id=? ORDER BY attempt_number DESC LIMIT 1`, f.release.ID, f.request.ComponentName).Scan(&id); err != nil {
		t.Fatal(err)
	}
	artifact, err := NewArtifactRepository(f.authority.db).Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestCompilerBuildsWorkspaceDependenciesAtRepositoryRoot(t *testing.T) {
	cases := []struct {
		name, packageManager, lockfile, install string
	}{
		{name: "npm", packageManager: "npm", lockfile: "package-lock.json", install: "npm ci"},
		{name: "pnpm", packageManager: "pnpm", lockfile: "pnpm-lock.yaml", install: "corepack pnpm install --frozen-lockfile"},
		{name: "yarn", packageManager: "yarn", lockfile: "yarn.lock", install: "corepack yarn install --immutable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompilerFixture(t)
			workspace := fixture.release.WorkspacePath
			writeTestFile(t, filepath.Join(workspace, "package.json"), `{"packageManager":"`+test.packageManager+`@`+map[string]string{"npm": "11.0.0", "pnpm": "10.0.0", "yarn": "4.6.0"}[test.packageManager]+`","workspaces":["apps/*"]}`)
			for _, lockfile := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock"} {
				if err := os.Remove(filepath.Join(workspace, lockfile)); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			writeTestFile(t, filepath.Join(workspace, test.lockfile), "locked\n")
			writeTestFile(t, filepath.Join(workspace, "apps", "api", "package.json"), `{"name":"api","scripts":{"build":"node build.js","start":"node server.js"},"dependencies":{"express":"5"}}`)
			writeTestFile(t, filepath.Join(workspace, "apps", "api", "build.js"), "console.log('build')")
			writeTestFile(t, filepath.Join(workspace, "apps", "api", "server.js"), "console.log('ready')")
			inspection, err := sourceinspection.InspectLocalContext(context.Background(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			fixture.release.ResolvedSHA = inspection.Analysis.StructuralFingerprint
			fixture.releaseReader.release = fixture.release
			fixture.revision.Plan.Detector.SourceStructuralFingerprint = inspection.Analysis.StructuralFingerprint
			fixture.revision.Plan.Source.ResolvedDigest = inspection.Analysis.StructuralFingerprint
			fixture.revision.Plan.Components = []deploymentplans.Component{{Name: "app", Role: "server", RootDirectory: "apps/api", PackageManager: test.packageManager, InstallBehavior: test.install, InstallDirectory: ".", NodeVersion: "24", BuildCommand: "node build.js", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/"}}
			fixture.rebind(t, "app")
			fixture.runner.run = func(request runtimeprocess.CommandRequest) error {
				contextDirectory := request.Args[len(request.Args)-1]
				assertFileEquals(t, filepath.Join(contextDirectory, "rig", "root.path"), "apps/api")
				assertFileEquals(t, filepath.Join(contextDirectory, "rig", "install.path"), ".")
				assertFileEquals(t, secretSource(t, request.Args, "rig-install-command"), test.install)
				recipe, err := os.ReadFile(flagValue(t, request.Args, "--file"))
				if err != nil {
					return err
				}
				assertCommandSecretRun(t, string(recipe), "rig-install-command", `install=$(cat /run/rig/install.path) && rig_command=$(cat /run/secrets/rig-install-command) && cd -- "/workspace/$install" && exec /bin/sh -lc "$rig_command"`)
				assertCommandSecretRun(t, string(recipe), "rig-build-command", `root=$(cat /run/rig/root.path) && rig_command=$(cat /run/secrets/rig-build-command) && cd -- "/workspace/$root" && exec node /run/rig/run-build.mjs "$rig_command"`)
				return os.WriteFile(flagValue(t, request.Args, "--iidfile"), []byte("sha256:"+strings.Repeat("3", 64)), 0o600)
			}
			artifact, err := fixture.compile(context.Background())
			if err != nil || artifact.State != ArtifactReady {
				t.Fatalf("workspace compile artifact=%#v err=%v", artifact, err)
			}
		})
	}
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

func hasArgumentPair(args []string, key, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return true
		}
	}
	return false
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
