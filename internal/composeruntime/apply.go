package composeruntime

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

type inspectedComposeInvocation struct {
	claim                       deploymentexecution.Claim
	deploymentID                string
	releaseID                   string
	configurationRevisionID     string
	configurationRevisionNumber int64
	findings                    []deployments.Finding
	workspace                   string
	project                     string
	files                       *securetemp.Files
	environment                 []string
}

type composeApplyAdapter struct {
	deployments *deployments.Repository
	runner      runtimeprocess.CommandRunner
	options     ExecutorOptions
	cleanup     func(*securetemp.Files) error
}

func newComposeApplyAdapter(repository *deployments.Repository, runner runtimeprocess.CommandRunner, options ExecutorOptions, cleanup func(*securetemp.Files) error) *composeApplyAdapter {
	return &composeApplyAdapter{deployments: repository, runner: runner, options: options, cleanup: cleanup}
}

func (a *composeApplyAdapter) RunUp(ctx context.Context, invocation inspectedComposeInvocation, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	cleanup := func() error {
		if a == nil || a.cleanup == nil || invocation.files == nil {
			return errors.New("compose protected-file cleanup is unavailable")
		}
		return a.cleanup(invocation.files)
	}
	failBeforeOrAfterCommand := func(code string, cause error) (jobs.ExecutionResult, error) {
		failure := errors.Join(&jobs.ExecutionError{Code: code}, cause, cleanup())
		if canceled := contextErrorForApply(ctx); canceled != nil {
			failure = errors.Join(failure, canceled)
		}
		return jobs.ExecutionResult{}, failure
	}
	if a == nil || a.deployments == nil || !a.deployments.ExecutionConfigured() || a.runner == nil || reporter == nil || invocation.files == nil {
		return failBeforeOrAfterCommand("runtime_unavailable", deploymentexecution.ErrInvalidConfiguration)
	}
	wantProject, err := projectName(invocation.claim.ApplicationID())
	if err != nil || invocation.project != wantProject || invocation.workspace == "" || !filepath.IsAbs(invocation.workspace) || filepath.Clean(invocation.workspace) != invocation.workspace ||
		invocation.files.Directory == "" || invocation.files.EnvPath == "" || invocation.files.ComposePath == "" {
		return failBeforeOrAfterCommand("runtime_unavailable", deploymentexecution.ErrInvalidClaim)
	}
	request := runtimeprocess.CommandRequest{
		Executable: a.options.DockerExecutable,
		Args: []string{
			"compose", "--project-name", invocation.project,
			"--project-directory", invocation.workspace,
			"--env-file", invocation.files.EnvPath,
			"-f", invocation.files.ComposePath,
			"up", "-d", "--wait", "--wait-timeout", waitTimeoutSeconds(a.options.WaitTimeout),
		},
		Directory: invocation.workspace, Env: append([]string(nil), invocation.environment...),
		Timeout: a.options.ApplyTimeout, OutputLimit: runtimeprocess.DefaultOutputLimit,
	}
	execution, err := a.deployments.PinComposeExecution(ctx, deployments.ComposeExecutionRequest{
		Claim: invocation.claim, DeploymentID: invocation.deploymentID, ReleaseID: invocation.releaseID,
		ConfigurationRevisionID:     invocation.configurationRevisionID,
		ConfigurationRevisionNumber: invocation.configurationRevisionNumber,
		Findings:                    append([]deployments.Finding(nil), invocation.findings...),
	})
	if err != nil {
		return failBeforeOrAfterCommand("runtime_unavailable", err)
	}
	defer execution.Leave()
	if err := execution.CheckBeforeUp(); err != nil {
		return failBeforeOrAfterCommand("runtime_unavailable", err)
	}
	if err := contextErrorForApply(ctx); err != nil {
		return failBeforeOrAfterCommand(applyDiagnosticCode(ctx, runtimeprocess.CommandResult{}, err), err)
	}
	result, runErr := a.runner.Run(ctx, request)
	clear(result.Stdout)
	clear(result.Stderr)
	if runErr != nil || result.StdoutTruncated || result.StderrTruncated || ctx.Err() != nil {
		if runErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		}
		return failBeforeOrAfterCommand(applyDiagnosticCode(ctx, result, runErr), runErr)
	}
	if err := execution.RecordWaitingHealth(); err != nil {
		return failBeforeOrAfterCommand("runtime_unavailable", err)
	}
	if err := report(reporter, jobs.Running, "finalize", 100); err != nil {
		failure := errors.Join(&jobs.ExecutionError{Code: "internal_error"}, err, cleanup())
		if canceled := contextErrorForApply(ctx); canceled != nil {
			failure = errors.Join(failure, canceled)
		}
		return jobs.ExecutionResult{}, failure
	}
	if err := cleanup(); err != nil {
		failure := errors.Join(&jobs.ExecutionError{Code: "internal_error"}, err)
		if canceled := contextErrorForApply(ctx); canceled != nil {
			failure = errors.Join(failure, canceled)
		}
		return jobs.ExecutionResult{}, failure
	}
	if err := execution.RecordSucceeded(); err != nil {
		return jobs.ExecutionResult{}, errors.Join(&jobs.ExecutionError{Code: "runtime_unavailable"}, err)
	}
	return jobs.ExecutionResult{CompletionCode: "deployment_completed"}, nil
}

func contextErrorForApply(ctx context.Context) error {
	if ctx == nil {
		return deploymentexecution.ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(deploymentexecution.ErrCanceled, err)
	}
	return nil
}
