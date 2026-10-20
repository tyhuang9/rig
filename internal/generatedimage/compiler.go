package generatedimage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/projectanalysis"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const defaultBuildTimeout = 30 * time.Minute

type CompileError struct {
	Code      string
	cause     error
	uncertain bool
}

func (e *CompileError) Error() string                  { return "generated image: " + e.Code }
func (e *CompileError) Unwrap() error                  { return e.cause }
func (e *CompileError) generatedImageBoundaryFailure() {}
func (e *CompileError) generatedImageUncertain()       {}

func IsCompileCode(err error, code string) bool {
	var target *CompileError
	return errors.As(err, &target) && target.Code == code
}

func IsGeneratedImageBoundaryFailure(err error) bool {
	var target interface{ generatedImageBoundaryFailure() }
	return errors.As(err, &target)
}

func IsCompileUncertain(err error) bool {
	var target *CompileError
	return errors.As(err, &target) && target.uncertain
}

type releaseWorkspaceReader interface {
	ReadyWorkspace(context.Context, string, string) (releasesnapshot.Release, error)
}

type deploymentPlanReader interface {
	GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error)
}

type buildConfigurationExporter interface {
	ExportComponentBuildForExecution(context.Context, string, string, int64, string, int64, string) (appconfig.ExecutionConfiguration, error)
}

type imageValidator interface {
	ValidateImage(context.Context, generatedruntime.ImageSpec) error
}

type CompileRequest struct {
	Claim                        deploymentexecution.Claim
	DeploymentID                 string
	ReleaseID                    string
	ConfigurationRevisionID      string
	ConfigurationRevisionNumber  int64
	DeploymentPlanRevisionID     string
	DeploymentPlanRevisionNumber int64
	ComponentName                string
	Findings                     []deployments.Finding
}

type CompilerOptions struct {
	BuildTimeout     time.Duration
	ContextBytes     int64
	ContextEntries   int
	BuildConcurrency int
}

type Compiler struct {
	releases   releaseWorkspaceReader
	plans      deploymentPlanReader
	config     buildConfigurationExporter
	repository *deployments.Repository
	temporary  *securetemp.Manager
	builder    *BuilderManager
	runner     runtimeprocess.CommandRunner
	validator  imageValidator
	options    CompilerOptions
	cleanup    func(*securetemp.Files) error
	buildSlot  chan struct{}

	// afterBuildCommandPrepared is a deterministic test barrier. Production
	// leaves it nil; the fixed guarded check still decides whether Docker runs.
	afterBuildCommandPrepared func(runtimeprocess.CommandRequest)
	// afterImageValidated is a deterministic test barrier between physical
	// observation and the guarded atomic Ready/ImageReady result transaction.
	afterImageValidated func()
}

func NewCompiler(releases releaseWorkspaceReader, plans deploymentPlanReader, config buildConfigurationExporter, repository *deployments.Repository, temporary *securetemp.Manager, builder *BuilderManager, runner runtimeprocess.CommandRunner, validator imageValidator, options CompilerOptions) (*Compiler, error) {
	if nilGeneratedImageDependency(releases) || nilGeneratedImageDependency(plans) || nilGeneratedImageDependency(config) || repository == nil || !repository.ExecutionConfigured() || temporary == nil || builder == nil || !builder.usesRepository(repository) || nilGeneratedImageDependency(runner) || nilGeneratedImageDependency(validator) {
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
	return &Compiler{releases: releases, plans: plans, config: config, repository: repository, temporary: temporary, builder: builder, runner: runner, validator: validator, options: options, cleanup: func(files *securetemp.Files) error { return files.Cleanup() }, buildSlot: make(chan struct{}, options.BuildConcurrency)}, nil
}

func nilGeneratedImageDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Compile produces or reuses the immutable image for one component. Repository
// commands remain data until BuildKit executes the controller-generated recipe.
func (c *Compiler) Compile(ctx context.Context, request CompileRequest) (Artifact, error) {
	if c == nil || ctx == nil || request.Claim.JobID() == "" || uuid.Validate(request.Claim.ApplicationID()) != nil || request.Claim.Attempt() < 1 || uuid.Validate(request.DeploymentID) != nil || !validReleaseID(request.ReleaseID) || !validText(request.ComponentName, 256) || uuid.Validate(request.ConfigurationRevisionID) != nil || request.ConfigurationRevisionNumber < 1 || uuid.Validate(request.DeploymentPlanRevisionID) != nil || request.DeploymentPlanRevisionNumber < 1 {
		return Artifact{}, compileBoundary("validation_failed", false, deploymentexecution.ErrInvalidClaim)
	}
	request.Findings = append([]deployments.Finding(nil), request.Findings...)
	appID := request.Claim.ApplicationID()
	release, err := c.releases.ReadyWorkspace(ctx, appID, request.ReleaseID)
	if err != nil {
		return Artifact{}, compileReadFailure(ctx, "source_integrity_failed", err)
	}
	if release.ID != request.ReleaseID || release.AppID != appID || release.DeploymentPlanRevisionID != request.DeploymentPlanRevisionID || release.DeploymentPlanRevisionNumber != request.DeploymentPlanRevisionNumber {
		return Artifact{}, compileBoundary("source_integrity_failed", false, errors.Join(deployments.ErrInvalidDeployment, compileContextCause(ctx)))
	}
	revision, err := c.plans.GetRevision(ctx, appID, request.DeploymentPlanRevisionID, request.DeploymentPlanRevisionNumber)
	if err != nil {
		return Artifact{}, compileReadFailure(ctx, "deployment_plan_review_required", err)
	}
	if !compatiblePlan(ctx, release, revision) {
		return Artifact{}, compileBoundary("deployment_plan_review_required", false, compileContextCause(ctx))
	}
	configuration, err := c.config.ExportComponentBuildForExecution(ctx, appID, request.ConfigurationRevisionID, request.ConfigurationRevisionNumber, revision.ID, revision.RevisionNumber, request.ComponentName)
	if err != nil {
		if appconfig.IsCode(err, "configuration_review_required") {
			return Artifact{}, compileBoundary("configuration_review_required", false, errors.Join(err, compileContextCause(ctx)))
		}
		return Artifact{}, compileReadFailure(ctx, "configuration_unavailable", err)
	}
	defer configuration.Clear()
	if configuration.RevisionID != request.ConfigurationRevisionID || configuration.RevisionNumber != request.ConfigurationRevisionNumber || len(configuration.Environment) != 0 || len(configuration.SecretOrigins) != 0 {
		return Artifact{}, compileBoundary("configuration_unavailable", false, errors.Join(deployments.ErrInvalidDeployment, compileContextCause(ctx)))
	}
	publicValues, err := canonicalPublicBuildValues(configuration.PublicBuildValues)
	if err != nil {
		return Artifact{}, compileBoundary("configuration_unavailable", false, errors.Join(err, compileContextCause(ctx)))
	}
	defer clear(publicValues)
	definition, definitionDigest, err := definitionForBuild(revision, request.ComponentName, publicValues)
	if err != nil {
		return Artifact{}, compileBoundary("unsupported_runtime", false, errors.Join(err, compileContextCause(ctx)))
	}
	if len(publicValues) != 0 && definition.buildCommand == "" {
		return Artifact{}, compileBoundary("build_configuration_requires_build_command", false, errors.Join(deployments.ErrInvalidDeployment, compileContextCause(ctx)))
	}
	authorityRequest := deployments.GeneratedImageRequest{
		Claim: request.Claim, DeploymentID: request.DeploymentID, ReleaseID: release.ID,
		ConfigurationRevisionID: request.ConfigurationRevisionID, ConfigurationRevisionNumber: request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: revision.ID, DeploymentPlanRevisionNumber: revision.RevisionNumber,
		ComponentName: definition.name, CompilerVersion: CompilerVersion, BuildDefinitionDigest: definitionDigest,
		PlanCanonicalDigest: revision.CanonicalDigest,
		SourceProvider:      release.SourceProvider, SourceRepositoryID: release.RepositoryID, SourceResolvedDigest: release.ResolvedSHA,
		PlanSourceProvider: revision.Plan.Source.Provider, PlanSourceRepositoryID: revision.Plan.Source.RepositoryID, PlanSourceResolvedDigest: revision.Plan.Source.ResolvedDigest,
		ArchiveSHA256: release.ArchiveSHA256, WorkspaceTreeSHA256: release.WorkspaceTreeSHA256,
		Findings: append([]deployments.Finding(nil), request.Findings...),
	}
	execution, err := c.repository.PinGeneratedImage(ctx, authorityRequest)
	if err != nil {
		return Artifact{}, compileBoundary("authority_unavailable", false, err)
	}
	defer execution.Leave()
	attached, err := execution.HasPersistedImage()
	if err != nil {
		return Artifact{}, compileBoundary("authority_unavailable", false, err)
	}
	var artifact Artifact
	var created bool
	if attached {
		artifact, err = execution.ObservePersistedImage()
	} else {
		artifact, created, err = execution.SelectArtifact()
	}
	if err != nil {
		return Artifact{}, compileBoundary("authority_unavailable", false, err)
	}
	if !created && artifact.State == generatedimagestate.ArtifactBuilding {
		return Artifact{}, compileBoundary("build_in_progress", true, generatedimagestate.ErrInvalidArtifactTransition)
	}
	if !created {
		spec := compileImageSpec(request, definition, artifact)
		if validationErr := c.validator.ValidateImage(ctx, spec); validationErr == nil {
			if c.afterImageValidated != nil {
				c.afterImageValidated()
			}
			if err := execution.RecordReusedImageReady(); err != nil {
				return Artifact{}, compileBoundary("image_result_unavailable", true, err)
			}
			return artifact, nil
		} else if !attached && generatedruntime.IsDefinitiveImageMismatch(validationErr) {
			artifact, err = execution.ReplaceMissingReusableArtifact()
			if err != nil {
				return Artifact{}, compileBoundary("image_result_unavailable", true, errors.Join(validationErr, err))
			}
			created = true
		} else {
			return Artifact{}, compileBoundary("image_validation_uncertain", true, errors.Join(validationErr, compileContextCause(ctx)))
		}
	}
	if !created || artifact.State != generatedimagestate.ArtifactBuilding {
		return Artifact{}, compileBoundary("image_selection_invalid", false, deployments.ErrInvalidTransition)
	}
	select {
	case c.buildSlot <- struct{}{}:
		defer func() { <-c.buildSlot }()
	case <-ctx.Done():
		return Artifact{}, compileBoundary(string(DiagnosticBuildCancelled), true, compileContextCause(ctx))
	}
	selection, err := execution.BuildSelection()
	if err != nil {
		return Artifact{}, compileBoundary("authority_unavailable", false, err)
	}
	return c.build(ctx, request, release, revision, definition, definitionDigest, publicValues, artifact, execution, selection)
}

func (c *Compiler) build(ctx context.Context, request CompileRequest, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision, definition componentDefinition, definitionDigest string, publicValues []appconfig.ValueInput, artifact Artifact, execution *deployments.GeneratedImageExecution, selection deployments.GeneratedImageBuildSelection) (Artifact, error) {
	files, err := c.temporary.Create(artifact.ID, int(artifact.AttemptNumber))
	if err != nil {
		return Artifact{}, c.recordKnownFailure(ctx, execution, DiagnosticInternalError, err)
	}
	cleaned := false
	var cleanupErr error
	cleanup := func() error {
		if cleaned {
			return cleanupErr
		}
		cleaned = true
		cleanupErr = c.cleanup(files)
		return cleanupErr
	}
	defer func() { _ = cleanup() }()

	layout, err := prepareBuildContext(ctx, release.WorkspacePath, files.Directory, definition, contextLimits{bytes: c.options.ContextBytes, entries: c.options.ContextEntries})
	if err != nil {
		code := DiagnosticBuildContextInvalid
		if errors.Is(err, errBuildContextTooLarge) {
			code = DiagnosticBuildContextTooLarge
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return Artifact{}, compileBoundary(string(DiagnosticBuildCancelled), true, finalizeBuildCause(ctx, cleanup, err))
		}
		return Artifact{}, c.recordKnownFailure(ctx, execution, code, errors.Join(err, cleanup()))
	}
	// Revalidate after copying so a file-swap race cannot reach Docker.
	validated, err := c.releases.ReadyWorkspace(ctx, request.Claim.ApplicationID(), release.ID)
	if err != nil || validated.WorkspaceTreeSHA256 != release.WorkspaceTreeSHA256 || !compatiblePlan(ctx, validated, revision) {
		return Artifact{}, c.recordKnownFailure(ctx, execution, DiagnosticSourceIntegrityFailed, finalizeBuildCause(ctx, cleanup, err))
	}
	if err := writeRecipe(layout, definition); err != nil {
		return Artifact{}, c.recordKnownFailure(ctx, execution, DiagnosticInternalError, errors.Join(err, cleanup()))
	}
	if definition.buildCommand != "" {
		body, err := json.Marshal(publicValues)
		if err != nil {
			return Artifact{}, c.recordKnownFailure(ctx, execution, DiagnosticInternalError, errors.Join(err, cleanup()))
		}
		if err := writeBuildFile(layout.publicBuildValues, body, 0o600); err != nil {
			clear(body)
			return Artifact{}, c.recordKnownFailure(ctx, execution, DiagnosticInternalError, errors.Join(err, cleanup()))
		}
		clear(body)
	}
	session, err := c.builder.prepare(ctx, selection)
	if err != nil {
		return Artifact{}, compileBoundary(string(builderDiagnostic(err)), true, finalizeBuildCause(ctx, cleanup, err))
	}
	if !validBuilderSession(session) {
		return Artifact{}, compileBoundary(string(DiagnosticInternalError), true, finalizeBuildCause(ctx, cleanup, deployments.ErrInvalidDeployment))
	}

	tag := imageTag(request.Claim.ApplicationID(), release.ID, definition.name, definitionDigest)
	args := []string{"buildx", "build", "--builder", session.BuilderName, "--file", layout.containerfile, "--iidfile", layout.imageIDFile, "--load", "--no-cache", "--progress", "plain"}
	if definition.installBehavior != "" {
		args = append(args, "--secret", "id=rig-install-command,src="+layout.installCommand)
	}
	if definition.buildCommand != "" {
		args = append(args, "--secret", "id=rig-build-command,src="+layout.buildCommand)
		args = append(args, "--secret", "id=rig-public-build-values,src="+layout.publicBuildValues)
	}
	args = append(args,
		"--label", "io.rig.managed=generated-image",
		"--label", "io.rig.application="+request.Claim.ApplicationID(),
		"--label", "io.rig.release="+release.ID,
		"--label", "io.rig.artifact="+artifact.ID,
		"--label", "io.rig.plan="+revision.ID,
		"--label", "io.rig.component="+definition.name,
		"--label", "io.rig.role="+definition.role,
		"--label", "io.rig.definition="+definitionDigest,
		"--tag", tag, layout.contextDirectory,
	)
	command := runtimeprocess.CommandRequest{
		Executable: session.DockerExecutable,
		Args:       args,
		Directory:  files.Directory, Env: session.Environment(), Timeout: c.options.BuildTimeout, OutputLimit: runtimeprocess.DefaultOutputLimit,
	}
	if c.afterBuildCommandPrepared != nil {
		c.afterBuildCommandPrepared(command)
	}
	commandAuthority, err := c.repository.PinGeneratedImageCommand(selection)
	if err != nil {
		return Artifact{}, compileBoundary("authority_unavailable", false, finalizeBuildCause(ctx, cleanup, err))
	}
	defer commandAuthority.Leave()
	if err := commandAuthority.CheckBeforeBuildLoad(); err != nil {
		return Artifact{}, compileBoundary("authority_unavailable", false, finalizeBuildCause(ctx, cleanup, err))
	}
	if err := compileContextCause(ctx); err != nil {
		return Artifact{}, compileBoundary(string(DiagnosticBuildCancelled), true, finalizeBuildCause(ctx, cleanup, err))
	}
	result, runErr := c.runner.Run(ctx, command)
	diagnostic := classifyBuildResult(ctx, result, runErr)
	clear(result.Stdout)
	clear(result.Stderr)
	if diagnostic != "" {
		return Artifact{}, compileBoundary(string(diagnostic), true, finalizeBuildCause(ctx, cleanup, runErr))
	}
	imageID, err := readImageID(layout.imageIDFile)
	if err != nil {
		return Artifact{}, compileBoundary(string(DiagnosticBuildFailed), true, finalizeBuildCause(ctx, cleanup, err))
	}
	if err := cleanup(); err != nil {
		return Artifact{}, compileBoundary(string(DiagnosticInternalError), true, errors.Join(err, compileContextCause(ctx)))
	}
	validatedArtifact := artifact
	validatedArtifact.ImageContentID = imageID
	if err := c.validator.ValidateImage(ctx, compileImageSpec(request, definition, validatedArtifact)); err != nil {
		return Artifact{}, compileBoundary("image_validation_uncertain", true, errors.Join(err, compileContextCause(ctx)))
	}
	if c.afterImageValidated != nil {
		c.afterImageValidated()
	}
	if err := execution.RecordBuiltImageReady(imageID); err != nil {
		return Artifact{}, compileBoundary("image_result_unavailable", true, err)
	}
	completed, ok := execution.SelectedArtifact()
	if !ok || completed.State != generatedimagestate.ArtifactReady || completed.ImageContentID != imageID {
		return Artifact{}, compileBoundary("image_result_unavailable", true, deployments.ErrInvalidDeployment)
	}
	return completed, nil
}

func compileImageSpec(request CompileRequest, definition componentDefinition, artifact Artifact) generatedruntime.ImageSpec {
	return generatedruntime.ImageSpec{AppID: request.Claim.ApplicationID(), ReleaseID: request.ReleaseID, ArtifactID: artifact.ID,
		DeploymentPlanRevisionID: request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: request.DeploymentPlanRevisionNumber,
		ConfigurationRevisionID: request.ConfigurationRevisionID, ConfigurationRevisionNumber: request.ConfigurationRevisionNumber,
		ComponentName: request.ComponentName, Role: definition.role, ImageContentID: artifact.ImageContentID, BuildDefinitionDigest: artifact.BuildDefinitionDigest}
}

func (c *Compiler) recordKnownFailure(ctx context.Context, execution *deployments.GeneratedImageExecution, code DiagnosticCode, cause error) error {
	if canceled := compileContextCause(ctx); canceled != nil {
		return compileBoundary(string(code), true, errors.Join(cause, canceled))
	}
	if err := execution.RecordObservedBuildFailure(generatedimagestate.DiagnosticCode(code)); err != nil {
		return compileBoundary(string(code), true, errors.Join(cause, err))
	}
	return compileBoundary(string(code), false, cause)
}

func compileReadFailure(ctx context.Context, code string, cause error) error {
	return compileBoundary(code, false, errors.Join(cause, compileContextCause(ctx)))
}

func compileContextCause(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	return errors.Join(deploymentexecution.ErrCanceled, ctx.Err(), context.Cause(ctx))
}

func finalizeBuildCause(ctx context.Context, cleanup func() error, causes ...error) error {
	causes = append(causes, cleanup(), compileContextCause(ctx))
	return errors.Join(causes...)
}

func compileBoundary(code string, uncertain bool, causes ...error) error {
	return &CompileError{Code: code, uncertain: uncertain, cause: errors.Join(causes...)}
}

func compatiblePlan(ctx context.Context, release releasesnapshot.Release, revision deploymentplans.DeploymentPlanRevision) bool {
	if revision.ID != release.DeploymentPlanRevisionID || revision.AppID != release.AppID || revision.RevisionNumber != release.DeploymentPlanRevisionNumber || revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode || revision.Plan.Source.Provider != release.SourceProvider || revision.Plan.Source.RepositoryID != release.RepositoryID {
		return false
	}
	inspection, err := sourceinspection.InspectLocalContext(ctx, release.WorkspacePath)
	if err != nil {
		return false
	}
	if setup, explicit := deploymentplans.SetupFromPlan(revision.Plan); explicit {
		configured, err := sourceinspection.WithSetup(inspection, setup)
		if err != nil {
			return false
		}
		differences, err := deploymentplans.CompareAnalysis(revision.Plan, configured.Analysis)
		return err == nil && len(differences) == 0
	}
	if revision.Plan.Detector.Name != "projectanalysis" || revision.Plan.Detector.Version != projectanalysis.SchemaVersion || inspection.Analysis.StructuralFingerprint != revision.Plan.Detector.SourceStructuralFingerprint {
		return false
	}
	if release.SourceProvider == "local" && revision.Plan.Source.ResolvedDigest != inspection.Analysis.StructuralFingerprint {
		return false
	}
	return true
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
