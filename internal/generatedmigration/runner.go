// Package generatedmigration runs one approved deployment migration in a
// short-lived, hardened container. It owns no durable retry policy; the
// generated runtime state repository decides whether an attempt may run.
package generatedmigration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	defaultTimeout     = 10 * time.Minute
	defaultOutputLimit = 16 << 10
)

type configurationExporter interface {
	ExportRevisionKeysForExecution(context.Context, string, string, int64, []string) (appconfig.ExecutionConfiguration, error)
}

type Options struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	Timeout               time.Duration
	OutputLimit           int
}

type Runner struct {
	configuration configurationExporter
	environment   generatedruntime.EnvironmentStager
	runner        runtimeprocess.CommandRunner
	options       Options
	dockerEnv     []string
}

type Error struct{ Code string }

func (e *Error) Error() string { return "generated migration: " + e.Code }

func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

func New(configuration configurationExporter, environment generatedruntime.EnvironmentStager, runner runtimeprocess.CommandRunner, options Options) (*Runner, error) {
	if configuration == nil || environment == nil || runner == nil {
		return nil, errors.New("generated migration dependencies are required")
	}
	if options.Timeout == 0 {
		options.Timeout = defaultTimeout
	}
	if options.OutputLimit == 0 {
		options.OutputLimit = defaultOutputLimit
	}
	if !validOptions(options) {
		return nil, errors.New("generated migration options are invalid")
	}
	dockerEnv, err := dockerEnvironment(options.DockerEndpoint, options.DockerConfigDirectory)
	if err != nil {
		return nil, err
	}
	return &Runner{configuration: configuration, environment: environment, runner: runner, options: options, dockerEnv: dockerEnv}, nil
}

func (r *Runner) Run(ctx context.Context, request generatedruntime.MigrationRequest) (runError error) {
	if r == nil || ctx == nil || !validRequest(request) {
		return &Error{Code: "validation_failed"}
	}
	configuration, err := r.configuration.ExportRevisionKeysForExecution(ctx, request.AppID, request.ConfigurationRevisionID, request.ConfigurationRevisionNumber, request.AllowedEnvironmentKeys)
	if err != nil {
		return &Error{Code: "configuration_unavailable"}
	}
	defer configuration.Clear()
	lease, err := r.environment.Stage(request.DeploymentID, 1, configuration.Environment)
	configuration.Environment = nil
	if err != nil {
		return &Error{Code: "configuration_unavailable"}
	}
	defer func() {
		if cleanupErr := lease.Cleanup(); cleanupErr != nil {
			runError = &Error{Code: "migration_cleanup_failed"}
		}
	}()

	network, err := generatedruntime.DescribeAppNetwork(request.AppID)
	if err != nil {
		return &Error{Code: "validation_failed"}
	}
	name := migrationContainerName(request.DeploymentID)
	workingDirectory := "/workspace"
	if request.RootDirectory != "." {
		workingDirectory += "/" + request.RootDirectory
	}
	args := []string{
		"container", "create", "--name", name,
		"--label", "io.rig.managed=generated-migration",
		"--label", "io.rig.application=" + request.AppID,
		"--label", "io.rig.release=" + request.ReleaseID,
		"--label", "io.rig.deployment=" + request.DeploymentID,
		"--label", "io.rig.artifact=" + request.ArtifactID,
		"--label", "io.rig.plan=" + request.DeploymentPlanRevisionID,
		"--network", network.Name,
		"--env-file", lease.Path(),
		"--user", "node", "--workdir", workingDirectory,
		"--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=67108864,mode=1777",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--memory", "536870912", "--memory-swap", "536870912",
		"--cpu-period", "100000", "--cpu-quota", "100000", "--pids-limit", "256",
		"--ulimit", "nofile=1024:1024", "--init",
		"--log-driver", "json-file", "--log-opt", "max-size=1m", "--log-opt", "max-file=1",
		"--entrypoint", "/usr/local/bin/rig-entrypoint",
		request.ImageContentID, "/bin/sh", "-lc", request.Command,
	}
	created, runErr := r.run(ctx, args, 30*time.Second)
	containerID := strings.TrimSpace(string(created.Stdout))
	diagnostic := commandDiagnostic(ctx, created, runErr, "migration_create_failed")
	clearResult(&created)
	if diagnostic != "" || !lowerHex(containerID, 64) {
		if diagnostic == "" {
			diagnostic = "migration_create_failed"
		}
		return &Error{Code: diagnostic}
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		cleaned = true
		result, cleanupErr := r.run(context.WithoutCancel(ctx), []string{"container", "rm", "--force", containerID}, 30*time.Second)
		code := commandDiagnostic(context.WithoutCancel(ctx), result, cleanupErr, "migration_cleanup_failed")
		clearResult(&result)
		if code != "" {
			return &Error{Code: code}
		}
		return nil
	}
	defer cleanup()

	started, startErr := r.run(ctx, []string{"container", "start", containerID}, 30*time.Second)
	diagnostic = commandDiagnostic(ctx, started, startErr, "migration_start_failed")
	clearResult(&started)
	if diagnostic != "" {
		_ = cleanup()
		return &Error{Code: diagnostic}
	}
	waited, waitErr := r.run(ctx, []string{"container", "wait", containerID}, r.options.Timeout)
	exitText := strings.TrimSpace(string(waited.Stdout))
	diagnostic = commandDiagnostic(ctx, waited, waitErr, "migration_failed")
	clearResult(&waited)
	if diagnostic == "" {
		exitCode, parseErr := strconv.Atoi(exitText)
		if parseErr != nil || exitCode != 0 {
			diagnostic = "migration_failed"
		}
	}
	if cleanupErr := cleanup(); diagnostic == "" && cleanupErr != nil {
		return cleanupErr
	}
	if diagnostic != "" {
		return &Error{Code: diagnostic}
	}
	return nil
}

func (r *Runner) run(ctx context.Context, args []string, timeout time.Duration) (runtimeprocess.CommandResult, error) {
	if err := validateEmptyDirectory(r.options.DockerConfigDirectory); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return r.runner.Run(ctx, runtimeprocess.CommandRequest{Executable: r.options.DockerExecutable, Args: append([]string(nil), args...), Directory: r.options.WorkingDirectory, Env: append([]string(nil), r.dockerEnv...), Timeout: timeout, OutputLimit: r.options.OutputLimit})
}

func validOptions(options Options) bool {
	return cleanAbsolute(options.DockerExecutable) && cleanAbsolute(options.DockerConfigDirectory) && cleanAbsolute(options.WorkingDirectory) && localDockerEndpoint(options.DockerEndpoint) && options.Timeout >= time.Second && options.Timeout <= time.Hour && options.OutputLimit > 0 && options.OutputLimit <= runtimeprocess.DefaultOutputLimit
}

func validRequest(request generatedruntime.MigrationRequest) bool {
	if uuid.Validate(request.AppID) != nil || uuid.Validate(request.DeploymentID) != nil || uuid.Validate(request.ArtifactID) != nil || uuid.Validate(request.DeploymentPlanRevisionID) != nil || !validReleaseID(request.ReleaseID) || !validText(request.ComponentName, 256) || !validRoot(request.RootDirectory) || !validImageID(request.ImageContentID) || deploymentplans.ValidateCommand(request.Command) != nil || request.ConfigurationRevisionNumber < 0 || (request.ConfigurationRevisionNumber == 0) != (request.ConfigurationRevisionID == "") || len(request.AllowedEnvironmentKeys) > 8 {
		return false
	}
	keys := append([]string(nil), request.AllowedEnvironmentKeys...)
	sort.Strings(keys)
	for index, key := range keys {
		if key != "DATABASE_URL" || (index > 0 && keys[index-1] == key) {
			return false
		}
	}
	return true
}

func validRoot(value string) bool {
	return value != "" && value == path.Clean(value) && value != ".." && !strings.HasPrefix(value, "../") && !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\")
}
func validImageID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}
func validReleaseID(value string) bool { return uuid.Validate(value) == nil || lowerHex(value, 32) }
func validText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}
func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func migrationContainerName(deploymentID string) string {
	return "rig-migration-" + strings.ReplaceAll(deploymentID, "-", "")
}
func cleanAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !pathsecurity.RejectWindowsNamespace(value)
}
func localDockerEndpoint(value string) bool {
	if value == "" || value == "npipe:////./pipe/docker_engine" {
		return true
	}
	if !strings.HasPrefix(value, "unix://") {
		return false
	}
	return cleanAbsolute(strings.TrimPrefix(value, "unix://"))
}

func dockerEnvironment(endpoint, dockerConfig string) ([]string, error) {
	if err := validateEmptyDirectory(dockerConfig); err != nil {
		return nil, err
	}
	values := map[string]string{"DOCKER_CONFIG": dockerConfig}
	for _, key := range []string{"PATH", "PATHEXT", "SystemRoot", "TEMP", "TMP", "WINDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if endpoint != "" {
		values["DOCKER_HOST"] = endpoint
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

func validateEmptyDirectory(directory string) error {
	if !cleanAbsolute(directory) {
		return errors.New("invalid Docker configuration directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		return errors.New("Docker configuration directory must be empty")
	}
	return nil
}

func commandDiagnostic(ctx context.Context, result runtimeprocess.CommandResult, err error, fallback string) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "migration_timeout"
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return "process_termination_failed"
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "runtime_output_truncated"
	}
	var executableError *exec.Error
	var pathError *os.PathError
	if errors.As(err, &executableError) || errors.As(err, &pathError) || dockerUnavailable(result) {
		return "runtime_unavailable"
	}
	if err != nil {
		return fallback
	}
	return ""
}

func dockerUnavailable(result runtimeprocess.CommandResult) bool {
	message := strings.ToLower(string(append(append([]byte(nil), result.Stdout...), result.Stderr...)))
	return strings.Contains(message, "cannot connect to the docker daemon") || strings.Contains(message, "error during connect")
}
func clearResult(result *runtimeprocess.CommandResult) {
	clear(result.Stdout)
	clear(result.Stderr)
	result.Stdout = nil
	result.Stderr = nil
}
