package composeruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/pathsecurity"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/sourceinspection"
)

const (
	defaultConfigTimeout = 30 * time.Second
	defaultApplyTimeout  = 15 * time.Minute
	defaultWaitTimeout   = 2 * time.Minute
)

type applicationReader interface {
	Get(string) (apps.Application, error)
}

type releaseResolver interface {
	Materialize(context.Context, string, string) (releasesnapshot.Release, error)
	ReadyRelease(context.Context, string, string) (releasesnapshot.Release, error)
}

type configurationExporter interface {
	ExportCurrentForExecution(context.Context, string) (appconfig.ExecutionConfiguration, error)
	ExportRevisionForExecution(context.Context, string, string, int64) (appconfig.ExecutionConfiguration, error)
}

type deploymentStore interface {
	GetOrCreateByJob(context.Context, string, string, string) (deployments.Deployment, bool, error)
	Get(context.Context, string, string) (deployments.Deployment, error)
	Initialize(context.Context, string, string, string, string, int64) (deployments.Deployment, error)
	Gate(context.Context, string, string, []deployments.Finding) error
	Transition(context.Context, string, string, deployments.Status, string) (deployments.Deployment, error)
}

type ExecutorOptions struct {
	DockerExecutable string
	DockerEndpoint   string
	ConfigTimeout    time.Duration
	ApplyTimeout     time.Duration
	WaitTimeout      time.Duration
}

type Executor struct {
	applications  applicationReader
	releases      releaseResolver
	configuration configurationExporter
	deployments   deploymentStore
	temporary     *securetemp.Manager
	runner        runtimeprocess.CommandRunner
	options       ExecutorOptions
	cleanup       func(*securetemp.Files) error
}

func NewExecutor(applications applicationReader, releases releaseResolver, configuration configurationExporter, deploymentRepository deploymentStore, temporary *securetemp.Manager, runner runtimeprocess.CommandRunner, options ExecutorOptions) (*Executor, error) {
	if applications == nil || releases == nil || configuration == nil || deploymentRepository == nil || temporary == nil || runner == nil {
		return nil, errors.New("compose runtime dependencies are required")
	}
	if options.DockerExecutable == "" {
		options.DockerExecutable = "docker"
	}
	if options.ConfigTimeout == 0 {
		options.ConfigTimeout = defaultConfigTimeout
	}
	if options.ApplyTimeout == 0 {
		options.ApplyTimeout = defaultApplyTimeout
	}
	if options.WaitTimeout == 0 {
		options.WaitTimeout = defaultWaitTimeout
	}
	if options.ConfigTimeout < time.Second || options.ConfigTimeout > 5*time.Minute ||
		options.ApplyTimeout < time.Second || options.ApplyTimeout > 2*time.Hour ||
		options.WaitTimeout < time.Second || options.WaitTimeout > time.Hour ||
		options.ApplyTimeout <= options.WaitTimeout {
		return nil, errors.New("compose runtime timeouts are outside supported bounds")
	}
	return &Executor{
		applications:  applications,
		releases:      releases,
		configuration: configuration,
		deployments:   deploymentRepository,
		temporary:     temporary,
		runner:        runner,
		options:       options,
		cleanup:       func(files *securetemp.Files) error { return files.Cleanup() },
	}, nil
}

func (e *Executor) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if reporter == nil || job.Type != "deploy" || job.ResourceType != "application" || uuid.Validate(job.ResourceID) != nil || uuid.Validate(job.ID) != nil || uuid.Validate(job.RequestedBy) != nil || job.Attempt < 1 {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "validation_failed"}
	}
	input, err := jobs.DeploymentInputFor(job)
	if err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "validation_failed"}
	}
	if err := report(reporter, jobs.Running, "validate", 5); err != nil {
		return jobs.ExecutionResult{}, err
	}

	deployment, _, err := e.deployments.GetOrCreateByJob(ctx, job.ResourceID, job.ID, string(input.ConfigurationMode))
	if err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "internal_error"}
	}

	source, configuration, err := e.resolve(ctx, job, input, deployment, reporter)
	if err != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, executionCode(ctx, err))
	}
	defer clear(configuration.Environment)

	if !deployment.ProvenanceInitialized {
		deployment, err = e.deployments.Initialize(ctx, job.ResourceID, deployment.ID, source.releaseID, configuration.RevisionID, configuration.RevisionNumber)
		if err != nil {
			return jobs.ExecutionResult{}, e.fail(ctx, deployment, "internal_error")
		}
	}
	if err := releasesnapshot.ValidateComposeWorkspace(source.workspace, source.composePath); err != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, "invalid_source")
	}
	if err := report(reporter, jobs.Running, "render_compose", 45); err != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, "internal_error")
	}

	files, err := e.temporary.Create(job.ID, job.Attempt)
	if err != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, "internal_error")
	}
	cleaned := false
	var cleanupErr error
	cleanup := func() error {
		if !cleaned {
			cleaned = true
			cleanupErr = e.cleanup(files)
		}
		return cleanupErr
	}
	failAfterTemp := func(code string) error {
		if cleanup() != nil {
			code = "internal_error"
		}
		if code == "interrupted" {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return e.fail(ctx, deployment, "internal_error")
		}
		return e.fail(ctx, deployment, code)
	}
	defer func() {
		if !cleaned {
			_ = e.cleanup(files)
		}
	}()
	if err := files.WriteEnv(configuration.Environment); err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	configuration.Environment = nil

	project, err := projectName(job.ResourceID)
	if err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	environment := scopedCommandEnvironment(e.options.DockerEndpoint)
	composeSource := filepath.Join(source.workspace, filepath.FromSlash(source.composePath))
	configResult, configErr := e.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: e.options.DockerExecutable,
		Args: []string{
			"compose", "--project-name", project,
			"--project-directory", source.workspace,
			"--env-file", files.EnvPath,
			"-f", composeSource,
			"config", "--format", "json", "--no-env-resolution",
		},
		Directory:   source.workspace,
		Env:         environment,
		Timeout:     e.options.ConfigTimeout,
		OutputLimit: runtimeprocess.DefaultOutputLimit,
	})
	defer clear(configResult.Stdout)
	defer clear(configResult.Stderr)
	if configErr != nil || configResult.StdoutTruncated || configResult.StderrTruncated || len(configResult.Stdout) == 0 {
		code := "compose_invalid"
		if commandUnavailable(configErr) {
			code = "runtime_unavailable"
		} else if errors.Is(configErr, context.Canceled) {
			code = cancellationCode(ctx)
		}
		return jobs.ExecutionResult{}, failAfterTemp(code)
	}

	if err := report(reporter, jobs.Running, "evaluate_policy", 60); err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	findings, err := EvaluatePolicy(configResult.Stdout, source.workspace)
	if err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("compose_invalid")
	}
	if err := files.WriteCompose(configResult.Stdout); err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	configResult.Stdout = nil

	if err := report(reporter, jobs.Running, "apply_runtime", 75); err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	if err := report(reporter, jobs.Running, "wait_for_health", 90); err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	upRequest := runtimeprocess.CommandRequest{
		Executable: e.options.DockerExecutable,
		Args: []string{
			"compose", "--project-name", project,
			"--project-directory", source.workspace,
			"--env-file", files.EnvPath,
			"-f", files.ComposePath,
			"up", "-d", "--wait", "--wait-timeout", waitTimeoutSeconds(e.options.WaitTimeout),
		},
		Directory:   source.workspace,
		Env:         environment,
		Timeout:     e.options.ApplyTimeout,
		OutputLimit: runtimeprocess.DefaultOutputLimit,
	}

	// Gate is intentionally the final database action before the mutating
	// command. It rechecks approvals and moves the deployment to applying in one
	// transaction; revocation is blocked for the duration of up --wait.
	if err := e.deployments.Gate(ctx, job.ResourceID, deployment.ID, deploymentFindings(findings)); err != nil {
		if cleanup() != nil {
			return jobs.ExecutionResult{}, e.fail(ctx, deployment, "internal_error")
		}
		if errors.Is(err, deployments.ErrApprovalRequired) {
			return jobs.ExecutionResult{Disposition: jobs.ExecutionWaitingUser, PauseDisposition: "approval_required"}, nil
		}
		if errors.Is(err, deployments.ErrRejectedCapability) {
			return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "policy_rejected"}
		}
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, "internal_error")
	}
	upResult, upErr := e.runner.Run(ctx, upRequest)
	clear(upResult.Stdout)
	clear(upResult.Stderr)
	if upErr != nil || upResult.StdoutTruncated || upResult.StderrTruncated {
		code := "apply_failed"
		if commandUnavailable(upErr) {
			code = "runtime_unavailable"
		} else if errors.Is(upErr, context.Canceled) {
			code = cancellationCode(ctx)
		} else if errors.Is(upErr, context.DeadlineExceeded) {
			code = "health_failed"
		}
		return jobs.ExecutionResult{}, failAfterTemp(code)
	}
	deployment, err = e.deployments.Transition(ctx, job.ResourceID, deployment.ID, deployments.WaitingHealth, "")
	if err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	if err := report(reporter, jobs.Running, "finalize", 100); err != nil {
		return jobs.ExecutionResult{}, failAfterTemp("internal_error")
	}
	if cleanup() != nil {
		return jobs.ExecutionResult{}, e.fail(ctx, deployment, "internal_error")
	}

	if _, err := e.deployments.Transition(ctx, job.ResourceID, deployment.ID, deployments.Succeeded, ""); err != nil {
		return jobs.ExecutionResult{}, &jobs.ExecutionError{Code: "internal_error"}
	}
	return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
}

type resolvedSource struct {
	releaseID   string
	workspace   string
	composePath string
}

func (e *Executor) resolve(ctx context.Context, job jobs.Job, input jobs.DeploymentInput, deployment deployments.Deployment, reporter jobs.ProgressReporter) (resolvedSource, appconfig.ExecutionConfiguration, error) {
	application, err := e.applications.Get(job.ResourceID)
	if err != nil {
		return resolvedSource{}, appconfig.ExecutionConfiguration{}, codedError("invalid_source")
	}
	if err := report(reporter, jobs.Running, "prepare_workspace", 20); err != nil {
		return resolvedSource{}, appconfig.ExecutionConfiguration{}, err
	}

	var source resolvedSource
	var release releasesnapshot.Release
	if deployment.ProvenanceInitialized && deployment.ReleaseID != "" {
		release, err = e.releases.ReadyRelease(ctx, job.ResourceID, deployment.ReleaseID)
	} else if !deployment.ProvenanceInitialized && input.ReleaseID != "" {
		release, err = e.releases.ReadyRelease(ctx, job.ResourceID, input.ReleaseID)
	} else if !deployment.ProvenanceInitialized && application.Source.Type == apps.SourceGitHub {
		if err := report(reporter, jobs.Waiting, "materialize_release", 30); err != nil {
			return resolvedSource{}, appconfig.ExecutionConfiguration{}, err
		}
		release, err = e.releases.Materialize(ctx, job.RequestedBy, job.ResourceID)
	}
	if err != nil {
		return resolvedSource{}, appconfig.ExecutionConfiguration{}, err
	}
	if release.ID != "" {
		source = resolvedSource{releaseID: release.ID, workspace: release.WorkspacePath, composePath: release.ComposePath}
	} else {
		workspace, composePath, localErr := resolveLocalCompose(application.Source.Path)
		if localErr != nil {
			return resolvedSource{}, appconfig.ExecutionConfiguration{}, codedError("invalid_source")
		}
		source = resolvedSource{workspace: workspace, composePath: composePath}
	}

	var configuration appconfig.ExecutionConfiguration
	if deployment.ProvenanceInitialized {
		configuration, err = e.configuration.ExportRevisionForExecution(ctx, job.ResourceID, deployment.ActualConfigurationRevisionID, deployment.ActualConfigurationRevisionNumber)
	} else if input.ConfigurationMode == jobs.ConfigurationOriginal && release.ID != "" {
		configuration, err = e.configuration.ExportRevisionForExecution(ctx, job.ResourceID, release.ConfigurationRevisionID, release.ConfigurationRevisionNumber)
	} else {
		configuration, err = e.configuration.ExportCurrentForExecution(ctx, job.ResourceID)
	}
	if err != nil {
		return resolvedSource{}, appconfig.ExecutionConfiguration{}, codedError("configuration_unavailable")
	}
	return source, configuration, nil
}

func resolveLocalCompose(sourcePath string) (string, string, error) {
	if pathsecurity.RejectWindowsNamespace(sourcePath) {
		return "", "", errors.New("unsafe local source namespace")
	}
	inspection, err := sourceinspection.InspectLocal(sourcePath)
	if err != nil {
		return "", "", err
	}
	if inspection.Source.ComposePath == "" || len(inspection.Findings) != 0 {
		return "", "", errors.New("local Compose source is ambiguous or invalid")
	}
	selectedPath := inspection.Source.Path
	workspace := selectedPath
	if info, statErr := os.Lstat(selectedPath); statErr != nil {
		return "", "", statErr
	} else if !info.IsDir() {
		workspace = filepath.Dir(selectedPath)
	}
	workspace, err = canonicalPath(workspace)
	if err != nil {
		return "", "", err
	}
	return workspace, inspection.Source.ComposePath, nil
}

func report(reporter jobs.ProgressReporter, status jobs.Status, phase string, progress int) error {
	return reporter.Report(jobs.ProgressUpdate{Status: status, Phase: phase, Progress: progress, Code: "phase_started"})
}

func deploymentFindings(findings []PolicyFinding) []deployments.Finding {
	result := make([]deployments.Finding, len(findings))
	for index, finding := range findings {
		result[index] = deployments.Finding{
			PolicyVersion: finding.PolicyVersion,
			Capability:    finding.Capability,
			Scope:         finding.Scope,
			Fingerprint:   finding.Fingerprint,
			Disposition:   finding.Disposition,
		}
	}
	return result
}

func projectName(appID string) (string, error) {
	parsed, err := uuid.Parse(appID)
	if err != nil || parsed.String() != appID {
		return "", errors.New("invalid application identity")
	}
	return "rig-" + strings.ReplaceAll(appID, "-", ""), nil
}

func waitTimeoutSeconds(value time.Duration) string {
	seconds := int64(value / time.Second)
	if value%time.Second != 0 {
		seconds++
	}
	return strconv.FormatInt(seconds, 10)
}

func scopedCommandEnvironment(dockerEndpoint string) []string {
	allowed := []string{"HOME", "PATH", "PATHEXT", "SystemRoot", "TEMP", "TMP", "USERPROFILE", "WINDIR", "XDG_CONFIG_HOME"}
	values := make(map[string]string, len(allowed)+1)
	for _, key := range allowed {
		if value, exists := os.LookupEnv(key); exists {
			values[key] = value
		}
	}
	if dockerEndpoint != "" {
		values["DOCKER_HOST"] = dockerEndpoint
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

type runtimeError struct{ code string }

func (e *runtimeError) Error() string { return e.code }

func codedError(code string) error { return &runtimeError{code: code} }

func executionCode(ctx context.Context, err error) string {
	var runtimeErr *runtimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr.code
	}
	var releaseErr *releasesnapshot.Error
	if errors.As(err, &releaseErr) {
		switch releaseErr.Code {
		case "invalid_source", "source_access_lost", "source_too_large", "provider_unavailable":
			return releaseErr.Code
		case "release_not_found":
			return "invalid_source"
		case "canceled":
			return cancellationCode(ctx)
		}
	}
	if errors.Is(err, context.Canceled) {
		return cancellationCode(ctx)
	}
	return "internal_error"
}

func cancellationCode(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), jobs.ErrCancellationRequested) {
		return "cancelled"
	}
	return "interrupted"
}

func commandUnavailable(err error) bool {
	var executableError *exec.Error
	var pathError *os.PathError
	return errors.As(err, &executableError) || errors.As(err, &pathError)
}

func (e *Executor) fail(ctx context.Context, deployment deployments.Deployment, code string) error {
	if deployment.ID != "" {
		status := deployments.Failed
		if code == "cancelled" {
			status = deployments.Cancelled
		}
		finalize, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, transitionErr := e.deployments.Transition(finalize, deployment.AppID, deployment.ID, status, code)
		cancel()
		if errors.Is(transitionErr, deployments.ErrInvalidTransition) {
			verify, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
			persisted, getErr := e.deployments.Get(verify, deployment.AppID, deployment.ID)
			verifyCancel()
			if getErr == nil && (persisted.Status == deployments.Failed || persisted.Status == deployments.Cancelled || persisted.Status == deployments.Succeeded) {
				return &jobs.ExecutionError{Code: code}
			}
		}
		if transitionErr != nil {
			return &jobs.ExecutionError{Code: "internal_error", Detail: fmt.Sprintf("persist deployment terminal state: %v", transitionErr)}
		}
	}
	return &jobs.ExecutionError{Code: code}
}
