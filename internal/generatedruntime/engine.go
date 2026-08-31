package generatedruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	defaultCommandTimeout  = 30 * time.Second
	defaultHealthTimeout   = 2 * time.Minute
	defaultHealthPoll      = time.Second
	defaultRuntimeOutput   = 64 << 10
	defaultReplacementDisk = 256 << 20
	containerUser          = "node"
)

const (
	networkInspectFormat   = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"internal":{{json .Internal}},"labels":{{json .Labels}}}`
	imageInspectFormat     = `{"id":{{json .Id}},"size":{{json .Size}},"labels":{{json .Config.Labels}},"user":{{json .Config.User}},"workingDir":{{json .Config.WorkingDir}},"entrypoint":{{json .Config.Entrypoint}}}`
	containerInspectFormat = `{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},"labels":{{json .Config.Labels}},"user":{{json .Config.User}},"workingDir":{{json .Config.WorkingDir}},"cmd":{{json .Config.Cmd}},"healthTest":{{json .Config.Healthcheck.Test}},"healthInterval":{{json .Config.Healthcheck.Interval}},"healthTimeout":{{json .Config.Healthcheck.Timeout}},"healthStartPeriod":{{json .Config.Healthcheck.StartPeriod}},"healthRetries":{{json .Config.Healthcheck.Retries}},"memory":{{json .HostConfig.Memory}},"memorySwap":{{json .HostConfig.MemorySwap}},"nanoCpus":{{json .HostConfig.NanoCpus}},"pidsLimit":{{json .HostConfig.PidsLimit}},"ulimits":{{json .HostConfig.Ulimits}},"init":{{json .HostConfig.Init}},"networkMode":{{json .HostConfig.NetworkMode}},"readonlyRootfs":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"capAdd":{{json .HostConfig.CapAdd}},"capDrop":{{json .HostConfig.CapDrop}},"securityOpt":{{json .HostConfig.SecurityOpt}},"binds":{{json .HostConfig.Binds}},"portBindings":{{json .HostConfig.PortBindings}},"tmpfs":{{json .HostConfig.Tmpfs}},"logType":{{json .HostConfig.LogConfig.Type}},"logConfig":{{json .HostConfig.LogConfig.Config}},"restart":{{json .HostConfig.RestartPolicy.Name}},"mounts":{{json .Mounts}},"running":{{json .State.Running}},"exitCode":{{json .State.ExitCode}},"health":{{json .State.Health.Status}},"networks":{{json .NetworkSettings.Networks}}}`
)

// The string is controller-owned and contains no plan values. Port and path
// are read from controller-owned environment variables, keeping user content
// out of Docker's CMD-SHELL health-check serialization.
const healthCommand = `node -e "const h=require('node:http');const q=h.get({host:'127.0.0.1',port:process.env.RIG_RUNTIME_INTERNAL_PORT,path:process.env.RIG_RUNTIME_HEALTH_PATH,timeout:1500},r=>{r.resume();process.exit(r.statusCode>=200&&r.statusCode<400?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})"`

type EngineOptions struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	CommandTimeout        time.Duration
	HealthTimeout         time.Duration
	HealthPollInterval    time.Duration
	OutputLimit           int
	Limits                ContainerLimits
	ReplacementDiskBytes  uint64
}

type Engine struct {
	runner      runtimeprocess.CommandRunner
	environment EnvironmentStager
	capacity    *capacityGate
	options     EngineOptions
	dockerEnv   []string
	networkMu   sync.Mutex
	candidateMu sync.Mutex
}

func NewEngine(runner runtimeprocess.CommandRunner, environment EnvironmentStager, capacity CapacitySource, options EngineOptions) (*Engine, error) {
	if runner == nil || environment == nil {
		return nil, errors.New("generated runtime dependencies are required")
	}
	if options.CommandTimeout == 0 {
		options.CommandTimeout = defaultCommandTimeout
	}
	if options.HealthTimeout == 0 {
		options.HealthTimeout = defaultHealthTimeout
	}
	if options.HealthPollInterval == 0 {
		options.HealthPollInterval = defaultHealthPoll
	}
	if options.OutputLimit == 0 {
		options.OutputLimit = defaultRuntimeOutput
	}
	if options.Limits == (ContainerLimits{}) {
		options.Limits = ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3}
	}
	if options.ReplacementDiskBytes == 0 {
		options.ReplacementDiskBytes = defaultReplacementDisk
	}
	if !validEngineOptions(options) {
		return nil, errors.New("generated runtime options are invalid")
	}
	dockerEnv, err := runtimeDockerEnvironment(options.DockerEndpoint, options.DockerConfigDirectory)
	if err != nil {
		return nil, err
	}
	gate, err := newCapacityGate(capacity)
	if err != nil {
		return nil, err
	}
	return &Engine{runner: runner, environment: environment, capacity: gate, options: options, dockerEnv: dockerEnv}, nil
}

func (e *Engine) CreateInactiveCandidate(ctx context.Context, spec CandidateSpec) (Candidate, error) {
	defer clear(spec.Environment)
	if e == nil || !validCandidateSpec(spec) {
		return Candidate{}, &Error{Code: DiagnosticValidationFailed}
	}
	slot, err := InactiveSlot(spec.ActiveSlot)
	if err != nil {
		return Candidate{}, &Error{Code: DiagnosticValidationFailed}
	}
	image, err := e.inspectImage(ctx, spec)
	if err != nil {
		return Candidate{}, err
	}
	lease, err := e.capacity.acquire(ctx, capacityRequest{memory: uint64(e.options.Limits.MemoryBytes), disk: e.options.ReplacementDiskBytes})
	if err != nil {
		return Candidate{}, err
	}
	releaseOnFailure := true
	defer func() {
		if releaseOnFailure {
			lease.Release()
		}
	}()

	network := networkName(spec.AppID)
	if err := e.ensureNetwork(ctx, spec.AppID, network); err != nil {
		return Candidate{}, err
	}
	name := containerName(spec.AppID, spec.ComponentName, slot)
	alias := containerAlias(spec.ComponentName, slot)

	// Docker container names are the final cross-process exclusion boundary;
	// this mutex avoids needless same-process create races and makes fake-driven
	// verification deterministic.
	e.candidateMu.Lock()
	defer e.candidateMu.Unlock()
	if _, found, inspectErr := e.inspectContainer(ctx, name); inspectErr != nil {
		return Candidate{}, inspectErr
	} else if found {
		return Candidate{}, &Error{Code: DiagnosticCandidateSlotOccupied}
	}

	environmentLease, err := e.environment.Stage(spec.EnvironmentOperationID, spec.EnvironmentOperationAttempt, spec.Environment)
	spec.Environment = nil
	if err != nil || environmentLease == nil || !validAbsolutePath(environmentLease.Path()) {
		if environmentLease != nil {
			_ = environmentLease.Cleanup()
		}
		return Candidate{}, &Error{Code: DiagnosticConfigurationUnavailable}
	}
	cleaned := false
	cleanupEnvironment := func() error {
		if cleaned {
			return nil
		}
		cleaned = true
		return environmentLease.Cleanup()
	}
	defer func() { _ = cleanupEnvironment() }()

	workingDirectory := runtimeWorkingDirectory(spec.RootDirectory)
	labels := runtimeLabels(spec, slot)
	args := []string{
		"container", "create",
		"--name", name,
		"--hostname", name,
		"--network", network,
		"--network-alias", alias,
		"--env-file", environmentLease.Path(),
		"--env", "RIG_RUNTIME_INTERNAL_PORT=" + strconv.FormatUint(uint64(spec.InternalPort), 10),
		"--env", "RIG_RUNTIME_HEALTH_PATH=" + spec.HealthProbe,
		"--user", containerUser,
		"--workdir", workingDirectory,
		"--read-only",
		"--init",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(e.options.Limits.TmpfsBytes, 10),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--memory", strconv.FormatInt(e.options.Limits.MemoryBytes, 10),
		"--memory-swap", strconv.FormatInt(e.options.Limits.MemoryBytes, 10),
		"--cpus", formatCPUs(e.options.Limits.MilliCPUs),
		"--pids-limit", strconv.FormatInt(e.options.Limits.PIDs, 10),
		"--ulimit", "nofile=1024:1024",
		"--log-driver", "local",
		"--log-opt", "max-size=" + e.options.Limits.LogSize,
		"--log-opt", "max-file=" + strconv.Itoa(e.options.Limits.LogFiles),
		"--restart", "no",
		"--health-cmd", healthCommand,
		"--health-interval", "2s",
		"--health-timeout", "2s",
		"--health-start-period", "5s",
		"--health-retries", "3",
	}
	for _, key := range sortedKeys(labels) {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, spec.ImageContentID, "/bin/sh", "-lc", spec.RunCommand)
	result, runErr := e.run(ctx, args, e.options.CommandTimeout)
	containerID := strings.TrimSpace(string(result.Stdout))
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateCreateFailed)
	clearResult(&result)
	if diagnostic != "" || !lowerHex(containerID, 64) {
		if lowerHex(containerID, 64) {
			_ = e.removeUnstarted(context.WithoutCancel(ctx), containerID)
		}
		if diagnostic == "" {
			diagnostic = DiagnosticCandidateCreateFailed
		}
		return Candidate{}, &Error{Code: diagnostic}
	}
	if err := cleanupEnvironment(); err != nil {
		if cleanupErr := e.removeUnstarted(context.WithoutCancel(ctx), containerID); cleanupErr != nil {
			return Candidate{}, cleanupErr
		}
		return Candidate{}, &Error{Code: DiagnosticConfigurationUnavailable}
	}

	candidate := Candidate{
		AppID: spec.AppID, ReleaseID: spec.ReleaseID, DeploymentID: spec.DeploymentID,
		ArtifactID: spec.ArtifactID, DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID,
		Component: spec.ComponentName, Slot: slot, ContainerID: containerID,
		ContainerName: name, NetworkName: network, NetworkAlias: alias,
		InternalPort: spec.InternalPort, ImageContentID: spec.ImageContentID,
		WorkingDirectory: workingDirectory, RunCommandDigest: sha256Hex(spec.RunCommand), lease: lease,
	}
	container, found, err := e.inspectContainer(ctx, containerID)
	if err != nil || !found || !matchesCreatedContainer(container, spec, candidate, image.ID, workingDirectory, labels, e.options.Limits) {
		if cleanupErr := e.removeUnstarted(context.WithoutCancel(ctx), containerID); cleanupErr != nil {
			return Candidate{}, cleanupErr
		}
		return Candidate{}, &Error{Code: DiagnosticCandidateHardeningFailed}
	}
	releaseOnFailure = false
	return candidate, nil
}

func (e *Engine) StartCandidate(ctx context.Context, candidate Candidate) error {
	if e == nil || !validCandidate(candidate) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	container, found, err := e.inspectContainer(ctx, candidate.ContainerID)
	if err != nil {
		return err
	}
	if !found || !matchesCandidateHardening(container, candidate, e.options.Limits) {
		return &Error{Code: DiagnosticCandidateHardeningFailed}
	}
	result, runErr := e.run(ctx, []string{"container", "start", candidate.ContainerID}, e.options.CommandTimeout)
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateStartFailed)
	clearResult(&result)
	if diagnostic != "" {
		return &Error{Code: diagnostic}
	}
	return nil
}

func (e *Engine) WaitHealthy(ctx context.Context, candidate Candidate) error {
	if e == nil || !validCandidate(candidate) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	waitCtx, cancel := context.WithTimeout(ctx, e.options.HealthTimeout)
	defer cancel()
	for {
		container, found, err := e.inspectContainer(waitCtx, candidate.ContainerID)
		if err != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				return &Error{Code: DiagnosticCandidateUnhealthy}
			}
			return err
		}
		if !found || !matchesCandidateHardening(container, candidate, e.options.Limits) {
			return &Error{Code: DiagnosticCandidateHardeningFailed}
		}
		if !container.Running {
			return &Error{Code: DiagnosticCandidateExited}
		}
		switch container.Health {
		case "healthy":
			return nil
		case "unhealthy":
			return &Error{Code: DiagnosticCandidateUnhealthy}
		case "starting":
		default:
			return &Error{Code: DiagnosticCandidateHardeningFailed}
		}
		timer := time.NewTimer(e.options.HealthPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return &Error{Code: DiagnosticCancelled}
			}
			return &Error{Code: DiagnosticCandidateUnhealthy}
		case <-timer.C:
		}
	}
}

// StopAndRemove performs the post-drain cleanup. It releases capacity only
// after Docker confirms removal; a failed cleanup remains reserved so another
// deployment cannot overcommit the host.
func (e *Engine) StopAndRemove(ctx context.Context, candidate Candidate, grace time.Duration) error {
	if e == nil || !validCandidate(candidate) || grace < 0 || grace > 30*time.Second {
		return &Error{Code: DiagnosticValidationFailed}
	}
	container, found, err := e.inspectContainer(ctx, candidate.ContainerID)
	if err != nil {
		return err
	}
	if !found {
		candidate.lease.Release()
		return nil
	}
	if !matchesCandidateOwnership(container, candidate) {
		return &Error{Code: DiagnosticCandidateHardeningFailed}
	}
	seconds := int64(grace / time.Second)
	if grace%time.Second != 0 {
		seconds++
	}
	result, runErr := e.run(ctx, []string{"container", "stop", "--time", strconv.FormatInt(seconds, 10), candidate.ContainerID}, grace+e.options.CommandTimeout)
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateCleanupFailed)
	clearResult(&result)
	if diagnostic != "" {
		return &Error{Code: diagnostic}
	}
	result, runErr = e.run(ctx, []string{"container", "rm", candidate.ContainerID}, e.options.CommandTimeout)
	diagnostic = e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateCleanupFailed)
	clearResult(&result)
	if diagnostic != "" {
		return &Error{Code: diagnostic}
	}
	candidate.lease.Release()
	return nil
}

// ReleaseAdmission is called after a candidate becomes the active slot and
// the old slot has been removed. Actual usage is then reflected by the next
// host capacity snapshot.
func (e *Engine) ReleaseAdmission(candidate Candidate) {
	candidate.lease.Release()
}

func (e *Engine) inspectImage(ctx context.Context, spec CandidateSpec) (imageInspection, error) {
	result, runErr := e.run(ctx, []string{"image", "inspect", "--format", imageInspectFormat, spec.ImageContentID}, e.options.CommandTimeout)
	if runErr != nil {
		notFound := dockerNotFound(result)
		diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticImageUnavailable)
		clearResult(&result)
		if notFound {
			return imageInspection{}, &Error{Code: DiagnosticImageUnavailable}
		}
		return imageInspection{}, &Error{Code: diagnostic}
	}
	if result.StdoutTruncated || result.StderrTruncated {
		clearResult(&result)
		return imageInspection{}, &Error{Code: DiagnosticRuntimeOutputTruncated}
	}
	var image imageInspection
	decodeErr := json.Unmarshal(result.Stdout, &image)
	clearResult(&result)
	if decodeErr != nil || image.ID != spec.ImageContentID || image.Size < 1 {
		return imageInspection{}, &Error{Code: DiagnosticImageDriftDetected}
	}
	expected := map[string]string{
		"io.rig.managed":     "generated-image",
		"io.rig.application": spec.AppID,
		"io.rig.release":     spec.ReleaseID,
		"io.rig.artifact":    spec.ArtifactID,
		"io.rig.plan":        spec.DeploymentPlanRevisionID,
		"io.rig.definition":  spec.BuildDefinitionDigest,
	}
	if !containsLabels(image.Labels, expected) || image.User != "node" || image.WorkingDirectory != "/workspace" || len(image.Entrypoint) != 1 || image.Entrypoint[0] != "/usr/local/bin/rig-entrypoint" {
		return imageInspection{}, &Error{Code: DiagnosticImageDriftDetected}
	}
	return image, nil
}

func (e *Engine) ensureNetwork(ctx context.Context, appID, name string) error {
	e.networkMu.Lock()
	defer e.networkMu.Unlock()
	if network, found, err := e.inspectNetwork(ctx, name); err != nil {
		return err
	} else if found {
		if matchesNetwork(network, appID, name) {
			return nil
		}
		return &Error{Code: DiagnosticNetworkDriftDetected}
	}
	args := []string{"network", "create", "--driver", "bridge", "--label", "io.rig.managed=generated-runtime-network", "--label", "io.rig.application=" + appID, name}
	result, runErr := e.run(ctx, args, e.options.CommandTimeout)
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticNetworkProvisionFailed)
	clearResult(&result)
	if diagnostic != "" {
		// A concurrent controller operation may have won the name race. Adopt
		// only the exact labeled network, never merely the same name.
		if network, found, inspectErr := e.inspectNetwork(ctx, name); inspectErr == nil && found && matchesNetwork(network, appID, name) {
			return nil
		}
		return &Error{Code: diagnostic}
	}
	network, found, err := e.inspectNetwork(ctx, name)
	if err != nil {
		return err
	}
	if !found || !matchesNetwork(network, appID, name) {
		return &Error{Code: DiagnosticNetworkDriftDetected}
	}
	return nil
}

func (e *Engine) inspectNetwork(ctx context.Context, name string) (networkInspection, bool, error) {
	result, runErr := e.run(ctx, []string{"network", "inspect", "--format", networkInspectFormat, name}, e.options.CommandTimeout)
	if runErr != nil {
		notFound := dockerNotFound(result)
		diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticNetworkProvisionFailed)
		clearResult(&result)
		if notFound {
			return networkInspection{}, false, nil
		}
		return networkInspection{}, false, &Error{Code: diagnostic}
	}
	var network networkInspection
	decodeErr := json.Unmarshal(result.Stdout, &network)
	truncated := result.StdoutTruncated || result.StderrTruncated
	clearResult(&result)
	if truncated {
		return networkInspection{}, false, &Error{Code: DiagnosticRuntimeOutputTruncated}
	}
	if decodeErr != nil {
		return networkInspection{}, false, &Error{Code: DiagnosticNetworkDriftDetected}
	}
	return network, true, nil
}

func (e *Engine) inspectContainer(ctx context.Context, identity string) (containerInspection, bool, error) {
	result, runErr := e.run(ctx, []string{"container", "inspect", "--format", containerInspectFormat, identity}, e.options.CommandTimeout)
	if runErr != nil {
		notFound := dockerNotFound(result)
		diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateHardeningFailed)
		clearResult(&result)
		if notFound {
			return containerInspection{}, false, nil
		}
		return containerInspection{}, false, &Error{Code: diagnostic}
	}
	var container containerInspection
	decodeErr := json.Unmarshal(result.Stdout, &container)
	truncated := result.StdoutTruncated || result.StderrTruncated
	clearResult(&result)
	if truncated {
		return containerInspection{}, false, &Error{Code: DiagnosticRuntimeOutputTruncated}
	}
	if decodeErr != nil {
		return containerInspection{}, false, &Error{Code: DiagnosticCandidateHardeningFailed}
	}
	return container, true, nil
}

func (e *Engine) removeUnstarted(ctx context.Context, containerID string) error {
	if !lowerHex(containerID, 64) {
		return &Error{Code: DiagnosticCandidateCleanupFailed}
	}
	result, runErr := e.run(ctx, []string{"container", "rm", "--force", containerID}, e.options.CommandTimeout)
	diagnostic := e.commandDiagnostic(ctx, result, runErr, DiagnosticCandidateCleanupFailed)
	clearResult(&result)
	if diagnostic != "" {
		return &Error{Code: diagnostic}
	}
	return nil
}

func (e *Engine) run(ctx context.Context, args []string, timeout time.Duration) (runtimeprocess.CommandResult, error) {
	if err := validateEmptyDockerConfig(e.options.DockerConfigDirectory); err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	return e.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: e.options.DockerExecutable,
		Args:       append([]string(nil), args...), Directory: e.options.WorkingDirectory,
		Env: append([]string(nil), e.dockerEnv...), Timeout: timeout, OutputLimit: e.options.OutputLimit,
	})
}

func (e *Engine) commandDiagnostic(ctx context.Context, result runtimeprocess.CommandResult, err error, fallback DiagnosticCode) DiagnosticCode {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return DiagnosticCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return DiagnosticRuntimeTimeout
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return DiagnosticProcessTerminationFailed
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return DiagnosticRuntimeOutputTruncated
	}
	var executableError *exec.Error
	var pathError *os.PathError
	if errors.As(err, &executableError) || errors.As(err, &pathError) || dockerUnavailable(result) {
		return DiagnosticRuntimeUnavailable
	}
	if err != nil {
		return fallback
	}
	return ""
}

type networkInspection struct {
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Scope    string            `json:"scope"`
	Internal bool              `json:"internal"`
	Labels   map[string]string `json:"labels"`
}

type imageInspection struct {
	ID               string            `json:"id"`
	Size             int64             `json:"size"`
	Labels           map[string]string `json:"labels"`
	User             string            `json:"user"`
	WorkingDirectory string            `json:"workingDir"`
	Entrypoint       []string          `json:"entrypoint"`
}

type containerInspection struct {
	ID                string                     `json:"id"`
	Name              string                     `json:"name"`
	Image             string                     `json:"image"`
	Labels            map[string]string          `json:"labels"`
	User              string                     `json:"user"`
	WorkingDirectory  string                     `json:"workingDir"`
	Command           []string                   `json:"cmd"`
	HealthTest        []string                   `json:"healthTest"`
	HealthInterval    int64                      `json:"healthInterval"`
	HealthTimeout     int64                      `json:"healthTimeout"`
	HealthStartPeriod int64                      `json:"healthStartPeriod"`
	HealthRetries     int                        `json:"healthRetries"`
	Memory            int64                      `json:"memory"`
	MemorySwap        int64                      `json:"memorySwap"`
	NanoCPUs          int64                      `json:"nanoCpus"`
	PIDs              int64                      `json:"pidsLimit"`
	Ulimits           []ulimitInspection         `json:"ulimits"`
	Init              bool                       `json:"init"`
	NetworkMode       string                     `json:"networkMode"`
	ReadonlyRootfs    bool                       `json:"readonlyRootfs"`
	Privileged        bool                       `json:"privileged"`
	CapAdd            []string                   `json:"capAdd"`
	CapDrop           []string                   `json:"capDrop"`
	SecurityOptions   []string                   `json:"securityOpt"`
	Binds             []string                   `json:"binds"`
	PortBindings      map[string]json.RawMessage `json:"portBindings"`
	Tmpfs             map[string]string          `json:"tmpfs"`
	LogType           string                     `json:"logType"`
	LogConfig         map[string]string          `json:"logConfig"`
	Restart           string                     `json:"restart"`
	Mounts            []mountInspection          `json:"mounts"`
	Running           bool                       `json:"running"`
	ExitCode          int                        `json:"exitCode"`
	Health            string                     `json:"health"`
	Networks          map[string]json.RawMessage `json:"networks"`
}

type mountInspection struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type ulimitInspection struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

func validEngineOptions(options EngineOptions) bool {
	if !validAbsolutePath(options.DockerExecutable) || !validAbsoluteDirectory(options.WorkingDirectory) || !validAbsoluteDirectory(options.DockerConfigDirectory) || !localDockerEndpoint(options.DockerEndpoint) {
		return false
	}
	if options.CommandTimeout < time.Second || options.CommandTimeout > 10*time.Minute || options.HealthTimeout < time.Second || options.HealthTimeout > 30*time.Minute || options.HealthPollInterval < 100*time.Millisecond || options.HealthPollInterval > 30*time.Second || options.OutputLimit < 1024 || options.OutputLimit > runtimeprocess.DefaultOutputLimit {
		return false
	}
	limits := options.Limits
	if limits.MemoryBytes < 64<<20 || limits.MemoryBytes > 64<<30 || limits.MilliCPUs < 100 || limits.MilliCPUs > 16000 || limits.PIDs < 32 || limits.PIDs > 4096 || limits.TmpfsBytes < 1<<20 || limits.TmpfsBytes > 1<<30 || limits.LogFiles < 1 || limits.LogFiles > 10 {
		return false
	}
	if limits.LogSize != "1m" && limits.LogSize != "5m" && limits.LogSize != "10m" && limits.LogSize != "20m" && limits.LogSize != "50m" {
		return false
	}
	return options.ReplacementDiskBytes >= 64<<20 && options.ReplacementDiskBytes <= 64<<30
}

func validCandidateSpec(spec CandidateSpec) bool {
	if !canonicalUUID(spec.AppID) || !validReleaseID(spec.ReleaseID) || !canonicalUUID(spec.DeploymentID) || !canonicalUUID(spec.ArtifactID) || !canonicalUUID(spec.DeploymentPlanRevisionID) || !validText(spec.ComponentName, 256) || !validRootDirectory(spec.RootDirectory) || deploymentplans.ValidateCommand(spec.RunCommand) != nil || spec.InternalPort == 0 || !validHealthProbe(spec.HealthProbe) || !validImageID(spec.ImageContentID) || !lowerHex(spec.BuildDefinitionDigest, 64) || !canonicalUUID(spec.EnvironmentOperationID) || spec.EnvironmentOperationAttempt < 1 || len(spec.Environment) == 0 || len(spec.Environment) > maximumEnvironmentBytes {
		return false
	}
	_, err := InactiveSlot(spec.ActiveSlot)
	return err == nil
}

func validCandidate(candidate Candidate) bool {
	if !canonicalUUID(candidate.AppID) || !validReleaseID(candidate.ReleaseID) || !canonicalUUID(candidate.DeploymentID) || !canonicalUUID(candidate.ArtifactID) || !canonicalUUID(candidate.DeploymentPlanRevisionID) || !validText(candidate.Component, 256) || !validImageContainerID(candidate.ContainerID) || candidate.InternalPort == 0 || !validImageID(candidate.ImageContentID) || !validRuntimeWorkingDirectory(candidate.WorkingDirectory) || !lowerHex(candidate.RunCommandDigest, 64) {
		return false
	}
	if candidate.Slot != SlotBlue && candidate.Slot != SlotGreen {
		return false
	}
	return candidate.NetworkName == networkName(candidate.AppID) && candidate.ContainerName == containerName(candidate.AppID, candidate.Component, candidate.Slot) && candidate.NetworkAlias == containerAlias(candidate.Component, candidate.Slot)
}

func validRuntimeWorkingDirectory(value string) bool {
	if value == "/workspace" {
		return true
	}
	if !strings.HasPrefix(value, "/workspace/") {
		return false
	}
	return validRootDirectory(strings.TrimPrefix(value, "/workspace/"))
}

func validHealthProbe(value string) bool {
	if !validText(value, 2048) || !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.IsAbs() == false && parsed.Host == "" && parsed.Fragment == ""
}

func validRootDirectory(value string) bool {
	if !validText(value, 1024) || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	return value == "." || value != ""
}

func runtimeWorkingDirectory(root string) string {
	if root == "." {
		return "/workspace"
	}
	return "/workspace/" + root
}

func validText(value string, maximum int) bool {
	if !utf8.ValidString(value) || value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validReleaseID(value string) bool { return canonicalUUID(value) || lowerHex(value, 32) }
func validImageID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && lowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}
func validImageContainerID(value string) bool { return lowerHex(value, 64) }

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

func networkName(appID string) string { return "rig-a-" + strings.ReplaceAll(appID, "-", "") }

func componentDigest(component string) string {
	sum := sha256.Sum256([]byte(component))
	return hex.EncodeToString(sum[:])[:12]
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func containerName(appID, component string, slot Slot) string {
	return networkName(appID) + "-c-" + componentDigest(component) + "-" + string(slot)
}

func containerAlias(component string, slot Slot) string {
	return "rig-c-" + componentDigest(component) + "-" + string(slot)
}

func runtimeLabels(spec CandidateSpec, slot Slot) map[string]string {
	return map[string]string{
		"io.rig.managed":     "generated-runtime",
		"io.rig.application": spec.AppID,
		"io.rig.release":     spec.ReleaseID,
		"io.rig.deployment":  spec.DeploymentID,
		"io.rig.artifact":    spec.ArtifactID,
		"io.rig.plan":        spec.DeploymentPlanRevisionID,
		"io.rig.component":   spec.ComponentName,
		"io.rig.slot":        string(slot),
	}
}

func matchesNetwork(network networkInspection, appID, name string) bool {
	return network.Name == name && network.Driver == "bridge" && network.Scope == "local" && !network.Internal && containsLabels(network.Labels, map[string]string{"io.rig.managed": "generated-runtime-network", "io.rig.application": appID})
}

func matchesCreatedContainer(container containerInspection, spec CandidateSpec, candidate Candidate, imageID, workingDirectory string, labels map[string]string, limits ContainerLimits) bool {
	if !matchesCandidateHardening(container, candidate, limits) || container.Image != imageID || container.WorkingDirectory != workingDirectory || container.Command[2] != spec.RunCommand || !containsLabels(container.Labels, labels) {
		return false
	}
	return true
}

func matchesCandidateHardening(container containerInspection, candidate Candidate, limits ContainerLimits) bool {
	if !matchesCandidateOwnership(container, candidate) || container.Image != candidate.ImageContentID || container.User != containerUser || container.WorkingDirectory != candidate.WorkingDirectory || len(container.Command) != 3 || container.Command[0] != "/bin/sh" || container.Command[1] != "-lc" || sha256Hex(container.Command[2]) != candidate.RunCommandDigest {
		return false
	}
	if len(container.HealthTest) != 2 || container.HealthTest[0] != "CMD-SHELL" || container.HealthTest[1] != healthCommand || container.HealthInterval != int64(2*time.Second) || container.HealthTimeout != int64(2*time.Second) || container.HealthStartPeriod != int64(5*time.Second) || container.HealthRetries != 3 {
		return false
	}
	if container.Memory != limits.MemoryBytes || container.MemorySwap != limits.MemoryBytes || container.NanoCPUs != limits.MilliCPUs*1_000_000 || container.PIDs != limits.PIDs || len(container.Ulimits) != 1 || container.Ulimits[0] != (ulimitInspection{Name: "nofile", Soft: 1024, Hard: 1024}) || !container.Init || container.NetworkMode != candidate.NetworkName || !container.ReadonlyRootfs || container.Privileged || len(container.CapAdd) != 0 || len(container.CapDrop) != 1 || !containsFold(container.CapDrop, "ALL") || !onlyNoNewPrivileges(container.SecurityOptions) {
		return false
	}
	if len(container.Binds) != 0 || len(container.PortBindings) != 0 || container.Restart != "no" || container.LogType != "local" || container.LogConfig["max-size"] != limits.LogSize || container.LogConfig["max-file"] != strconv.Itoa(limits.LogFiles) || !onlyRuntimeTmpfsMount(container.Mounts) {
		return false
	}
	tmpfs, exists := container.Tmpfs["/tmp"]
	return exists && containsAllCommaValues(tmpfs, []string{"rw", "noexec", "nosuid", "nodev", "size=" + strconv.FormatInt(limits.TmpfsBytes, 10)}) && len(container.Networks) == 1 && container.Networks[candidate.NetworkName] != nil
}

func onlyRuntimeTmpfsMount(mounts []mountInspection) bool {
	return len(mounts) == 1 && mounts[0].Type == "tmpfs" && mounts[0].Source == "" && mounts[0].Destination == "/tmp" && mounts[0].RW
}

func onlyNoNewPrivileges(values []string) bool {
	if len(values) != 1 {
		return false
	}
	switch strings.ToLower(values[0]) {
	case "no-new-privileges", "no-new-privileges:true", "no-new-privileges=true":
		return true
	default:
		return false
	}
}

func matchesCandidateOwnership(container containerInspection, candidate Candidate) bool {
	expected := map[string]string{
		"io.rig.managed": "generated-runtime", "io.rig.application": candidate.AppID,
		"io.rig.release": candidate.ReleaseID, "io.rig.deployment": candidate.DeploymentID,
		"io.rig.artifact": candidate.ArtifactID, "io.rig.plan": candidate.DeploymentPlanRevisionID,
		"io.rig.component": candidate.Component, "io.rig.slot": string(candidate.Slot),
	}
	return container.ID == candidate.ContainerID && strings.TrimPrefix(container.Name, "/") == candidate.ContainerName && containsLabels(container.Labels, expected) && container.NetworkMode == candidate.NetworkName && container.Networks[candidate.NetworkName] != nil
}

func containsLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func containsAllCommaValues(value string, expected []string) bool {
	actual := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		actual[item] = true
	}
	for _, item := range expected {
		if !actual[item] {
			return false
		}
	}
	return true
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func formatCPUs(milli int64) string {
	return strconv.FormatFloat(float64(milli)/1000, 'f', 3, 64)
}

func clearResult(result *runtimeprocess.CommandResult) {
	if result == nil {
		return
	}
	clear(result.Stdout)
	clear(result.Stderr)
	*result = runtimeprocess.CommandResult{}
}

func dockerNotFound(result runtimeprocess.CommandResult) bool {
	message := strings.ToLower(string(append(append([]byte(nil), result.Stdout...), result.Stderr...)))
	return strings.Contains(message, "no such image") || strings.Contains(message, "no such network") || strings.Contains(message, "no such container") || strings.Contains(message, "not found")
}

func dockerUnavailable(result runtimeprocess.CommandResult) bool {
	message := strings.ToLower(string(append(append([]byte(nil), result.Stdout...), result.Stderr...)))
	return strings.Contains(message, "cannot connect to the docker daemon") || strings.Contains(message, "error during connect") || strings.Contains(message, "is the docker daemon running")
}

func validAbsolutePath(value string) bool {
	return value != "" && !pathsecurity.RejectWindowsNamespace(value) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validAbsoluteDirectory(value string) bool {
	if !validAbsolutePath(value) {
		return false
	}
	info, err := os.Lstat(value)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && !isReparsePoint(value)
}

func validateEmptyDockerConfig(directory string) error {
	if !validAbsoluteDirectory(directory) {
		return errors.New("unsafe generated runtime Docker configuration")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		return errors.New("generated runtime Docker configuration must be empty")
	}
	return nil
}

func runtimeDockerEnvironment(endpoint, dockerConfig string) ([]string, error) {
	if err := validateEmptyDockerConfig(dockerConfig); err != nil {
		return nil, err
	}
	values := map[string]string{"DOCKER_CONFIG": dockerConfig}
	for _, key := range []string{"PATH", "PATHEXT", "SystemRoot", "TEMP", "TMP", "WINDIR"} {
		if value, exists := os.LookupEnv(key); exists {
			values[key] = value
		}
	}
	if endpoint != "" {
		values["DOCKER_HOST"] = endpoint
	}
	keys := sortedKeys(values)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment, nil
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
