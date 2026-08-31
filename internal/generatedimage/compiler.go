package generatedimage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const defaultBuildTimeout = 30 * time.Minute

type CompileError struct{ Code string }

func (e *CompileError) Error() string { return "generated image: " + e.Code }

func IsCompileCode(err error, code string) bool {
	var target *CompileError
	return errors.As(err, &target) && target.Code == code
}

type releaseWorkspaceReader interface {
	ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error)
}

type deploymentPlanReader interface {
	GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error)
}

type artifactWriter interface {
	Begin(context.Context, BeginArtifactInput) (Artifact, bool, error)
	Complete(context.Context, string, string) (Artifact, error)
	Fail(context.Context, string, DiagnosticCode) (Artifact, error)
	Cancel(context.Context, string) (Artifact, error)
}

type builderPreparer interface {
	Prepare(context.Context) (BuilderSession, error)
}

type CompilerOptions struct {
	BuildTimeout     time.Duration
	ContextBytes     int64
	ContextEntries   int
	BuildConcurrency int
}

type Compiler struct {
	releases  releaseWorkspaceReader
	plans     deploymentPlanReader
	artifacts artifactWriter
	temporary *securetemp.Manager
	builder   builderPreparer
	runner    runtimeprocess.CommandRunner
	options   CompilerOptions
	cleanup   func(*securetemp.Files) error
	buildSlot chan struct{}
}

func NewCompiler(releases releaseWorkspaceReader, plans deploymentPlanReader, artifacts artifactWriter, temporary *securetemp.Manager, builder builderPreparer, runner runtimeprocess.CommandRunner, options CompilerOptions) (*Compiler, error) {
	if releases == nil || plans == nil || artifacts == nil || temporary == nil || builder == nil || runner == nil {
		return nil, errors.New("generated image compiler dependencies are required")
	}
	if options.BuildTimeout == 0 {
		options.BuildTimeout = defaultBuildTimeout
	}
	if options.BuildConcurrency == 0 {
		options.BuildConcurrency = 1
	}
	if options.BuildTimeout < time.Second || options.BuildTimeout > 2*time.Hour || options.ContextBytes < 0 || options.ContextEntries < 0 || options.BuildConcurrency < 1 || options.BuildConcurrency > 4 {
		return nil, errors.New("generated build limits are outside supported bounds")
	}
	return &Compiler{releases: releases, plans: plans, artifacts: artifacts, temporary: temporary, builder: builder, runner: runner, options: options, cleanup: func(files *securetemp.Files) error { return files.Cleanup() }, buildSlot: make(chan struct{}, options.BuildConcurrency)}, nil
}

// Compile produces or reuses the immutable image for one component. Repository
// commands remain data until BuildKit executes the controller-generated recipe.
func (c *Compiler) Compile(ctx context.Context, appID, releaseID, componentName string) (Artifact, error) {
	if uuid.Validate(appID) != nil || !validReleaseID(releaseID) || !validText(componentName, 256) {
		return Artifact{}, &CompileError{Code: "validation_failed"}
	}
	release, err := c.releases.ReadyWorkspace(ctx, appID, releaseID)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return Artifact{}, &CompileError{Code: string(DiagnosticBuildCancelled)}
		}
		return Artifact{}, &CompileError{Code: "source_integrity_failed"}
	}
	if release.DeploymentPlanRevisionID == "" || release.DeploymentPlanRevisionNumber < 1 {
		return Artifact{}, &CompileError{Code: "deployment_plan_review_required"}
	}
	revision, err := c.plans.GetRevision(ctx, appID, release.DeploymentPlanRevisionID, release.DeploymentPlanRevisionNumber)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return Artifact{}, &CompileError{Code: string(DiagnosticBuildCancelled)}
		}
		return Artifact{}, &CompileError{Code: "deployment_plan_review_required"}
	}
	if !compatiblePlan(ctx, release, revision) {
		if errors.Is(ctx.Err(), context.Canceled) {
			return Artifact{}, &CompileError{Code: string(DiagnosticBuildCancelled)}
		}
		return Artifact{}, &CompileError{Code: "deployment_plan_review_required"}
	}
	definition, definitionDigest, err := definitionFor(revision, componentName)
	if err != nil {
		return Artifact{}, &CompileError{Code: "unsupported_runtime"}
	}
	artifact, created, err := c.artifacts.Begin(ctx, BeginArtifactInput{
		ReleaseID: release.ID, DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber,
		ComponentID: definition.name, CompilerVersion: CompilerVersion, BuildDefinitionDigest: definitionDigest,
	})
	if err != nil {
		return Artifact{}, &CompileError{Code: "internal_error"}
	}
	if !created {
		return artifact, nil
	}
	select {
	case c.buildSlot <- struct{}{}:
		defer func() { <-c.buildSlot }()
	case <-ctx.Done():
		return Artifact{}, c.cancelArtifact(artifact.ID)
	}
	return c.build(ctx, appID, release, revision, definition, definitionDigest, artifact)
}

func (c *Compiler) build(ctx context.Context, appID string, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision, definition componentDefinition, definitionDigest string, artifact Artifact) (Artifact, error) {
	files, err := c.temporary.Create(artifact.ID, int(artifact.AttemptNumber))
	if err != nil {
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticInternalError)
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		cleaned = true
		return c.cleanup(files)
	}
	defer func() { _ = cleanup() }()

	layout, err := prepareBuildContext(ctx, release.WorkspacePath, files.Directory, definition, contextLimits{bytes: c.options.ContextBytes, entries: c.options.ContextEntries})
	if err != nil {
		code := DiagnosticBuildContextInvalid
		if errors.Is(err, errBuildContextTooLarge) {
			code = DiagnosticBuildContextTooLarge
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			_ = cleanup()
			return Artifact{}, c.cancelArtifact(artifact.ID)
		}
		_ = cleanup()
		return Artifact{}, c.failArtifact(ctx, artifact.ID, code)
	}
	// Revalidate after copying so a file-swap race cannot reach Docker.
	validated, err := c.releases.ReadyWorkspace(ctx, appID, release.ID)
	if err != nil || validated.WorkspaceTreeSHA256 != release.WorkspaceTreeSHA256 || !compatiblePlan(ctx, validated, revision) {
		_ = cleanup()
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticSourceIntegrityFailed)
	}
	if err := writeRecipe(layout, definition); err != nil {
		_ = cleanup()
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticInternalError)
	}
	session, err := c.builder.Prepare(ctx)
	if err != nil {
		_ = cleanup()
		diagnostic := builderDiagnostic(err)
		if diagnostic == DiagnosticBuildCancelled {
			return Artifact{}, c.cancelArtifact(artifact.ID)
		}
		return Artifact{}, c.failArtifact(ctx, artifact.ID, diagnostic)
	}
	if !validBuilderSession(session) {
		_ = cleanup()
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticInternalError)
	}

	tag := imageTag(appID, release.ID, definition.name, definitionDigest)
	args := []string{"buildx", "build", "--builder", session.BuilderName, "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain", "--secret", "id=rig-install-command,src=" + layout.installCommand}
	if definition.buildCommand != "" {
		args = append(args, "--secret", "id=rig-build-command,src="+layout.buildCommand)
	}
	args = append(args,
		"--label", "io.rig.managed=generated-image",
		"--label", "io.rig.application="+appID,
		"--label", "io.rig.release="+release.ID,
		"--label", "io.rig.artifact="+artifact.ID,
		"--label", "io.rig.plan="+revision.ID,
		"--label", "io.rig.definition="+definitionDigest,
		"--tag", tag, layout.contextDirectory,
	)
	result, runErr := c.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: session.DockerExecutable,
		Args:       args,
		Directory:  files.Directory, Env: session.Environment(), Timeout: c.options.BuildTimeout, OutputLimit: runtimeprocess.DefaultOutputLimit,
	})
	diagnostic := classifyBuildResult(ctx, result, runErr)
	clear(result.Stdout)
	clear(result.Stderr)
	if diagnostic != "" {
		_ = cleanup()
		if diagnostic == DiagnosticBuildCancelled {
			return Artifact{}, c.cancelArtifact(artifact.ID)
		}
		return Artifact{}, c.failArtifact(ctx, artifact.ID, diagnostic)
	}
	imageID, err := readImageID(layout.imageIDFile)
	if err != nil {
		_ = cleanup()
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticBuildFailed)
	}
	if err := cleanup(); err != nil {
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticInternalError)
	}
	completed, err := c.artifacts.Complete(ctx, artifact.ID, imageID)
	if err != nil {
		return Artifact{}, c.failArtifact(ctx, artifact.ID, DiagnosticInternalError)
	}
	return completed, nil
}

func compatiblePlan(ctx context.Context, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision) bool {
	if revision.ID != release.DeploymentPlanRevisionID || revision.AppID != release.AppID || revision.RevisionNumber != release.DeploymentPlanRevisionNumber || revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode || revision.Plan.Detector.Name != "projectanalysis" || revision.Plan.Detector.Version != projectanalysis.SchemaVersion || revision.Plan.Source.Provider != release.SourceProvider || revision.Plan.Source.RepositoryID != release.RepositoryID {
		return false
	}
	inspection, err := sourceinspection.InspectLocalContext(ctx, release.WorkspacePath)
	if err != nil || inspection.Analysis.StructuralFingerprint != revision.Plan.Detector.SourceStructuralFingerprint {
		return false
	}
	if release.SourceProvider == "local" && revision.Plan.Source.ResolvedDigest != inspection.Analysis.StructuralFingerprint {
		return false
	}
	return true
}

func (c *Compiler) failArtifact(ctx context.Context, artifactID string, code DiagnosticCode) error {
	finalize, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = c.artifacts.Fail(finalize, artifactID, code)
	return &CompileError{Code: string(code)}
}

func (c *Compiler) cancelArtifact(artifactID string) error {
	finalize, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.artifacts.Cancel(finalize, artifactID)
	return &CompileError{Code: string(DiagnosticBuildCancelled)}
}

func classifyBuildResult(ctx context.Context, result runtimeprocess.CommandResult, err error) DiagnosticCode {
	if errors.Is(ctx.Err(), context.Canceled) {
		return DiagnosticBuildCancelled
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return DiagnosticProcessTerminationFailed
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return DiagnosticBuildTimeout
	}
	var executableError *exec.Error
	var pathError *os.PathError
	if errors.As(err, &executableError) || errors.As(err, &pathError) {
		return DiagnosticRuntimeUnavailable
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return DiagnosticBuildOutputTruncated
	}
	if err != nil {
		output := strings.ToLower(string(result.Stderr))
		if strings.Contains(output, "no space left on device") || strings.Contains(output, "disk quota exceeded") {
			return DiagnosticBuildDiskExhausted
		}
		if strings.Contains(output, "cannot connect to the docker daemon") || strings.Contains(output, "error during connect") {
			return DiagnosticRuntimeUnavailable
		}
		return DiagnosticBuildFailed
	}
	return ""
}

func readImageID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || generatedImagePathIsReparsePoint(path) || info.Size() < 1 || info.Size() > 128 {
		return "", errors.New("invalid image id file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	defer clear(body)
	value := strings.TrimSpace(string(body))
	if !validImageContentID(value) {
		return "", errors.New("invalid image id")
	}
	return value, nil
}

func localDockerEndpoint(value string) bool {
	if value == "" || value == "npipe:////./pipe/docker_engine" {
		return true
	}
	if !strings.HasPrefix(value, "unix://") {
		return false
	}
	path := strings.TrimPrefix(value, "unix://")
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !pathsecurity.RejectWindowsNamespace(path)
}

func validBuilderSession(session BuilderSession) bool {
	if session.DockerExecutable == "" || pathsecurity.RejectWindowsNamespace(session.DockerExecutable) || !filepath.IsAbs(session.DockerExecutable) || filepath.Clean(session.DockerExecutable) != session.DockerExecutable || !validBuilderName(session.BuilderName) || session.storageQuotaBytes < minimumStateQuotaBytes || session.storageQuotaBytes > maximumStateQuotaBytes {
		return false
	}
	allowed := map[string]bool{"PATH": true, "PATHEXT": true, "SystemRoot": true, "TEMP": true, "TMP": true, "WINDIR": true, "DOCKER_CONFIG": true, "BUILDX_CONFIG": true, "DOCKER_HOST": true}
	seen := map[string]bool{}
	for _, entry := range session.Environment() {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" || seen[key] || !allowed[key] {
			return false
		}
		seen[key] = true
		if (key == "DOCKER_CONFIG" || key == "BUILDX_CONFIG") && (value == "" || pathsecurity.RejectWindowsNamespace(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value) {
			return false
		}
		if key == "DOCKER_HOST" && !localDockerEndpoint(value) {
			return false
		}
	}
	return seen["DOCKER_CONFIG"] && seen["BUILDX_CONFIG"]
}

func builderDiagnostic(err error) DiagnosticCode {
	var builderErr *BuilderError
	if !errors.As(err, &builderErr) {
		return DiagnosticInternalError
	}
	switch builderErr.Code {
	case BuilderCancelled:
		return DiagnosticBuildCancelled
	case BuilderTimedOut:
		return DiagnosticBuildTimeout
	case BuilderOutputTruncated:
		return DiagnosticBuildOutputTruncated
	case BuilderTerminationFailed:
		return DiagnosticProcessTerminationFailed
	case BuilderHardQuotaUnavailable:
		return DiagnosticBuildCapacityExceeded
	case BuilderHardQuotaExhausted:
		return DiagnosticBuildDiskExhausted
	case BuilderRuntimeUnavailable, BuilderProvisionFailed, BuilderBootstrapFailed:
		return DiagnosticRuntimeUnavailable
	default:
		return DiagnosticInternalError
	}
}

func validBuilderName(value string) bool {
	if len(value) < 3 || len(value) > 63 || !strings.HasPrefix(value, "rig-") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func imageTag(appID, releaseID, componentName, definitionDigest string) string {
	component := sha256Hex(componentName)[:12]
	return fmt.Sprintf("rig-generated/%s:r%s-c%s-d%s", strings.ReplaceAll(appID, "-", ""), releaseID, component, definitionDigest[:12])
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}
