package generatedimage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const (
	builderIdentityFilename       = "builder-identity-v2.json"
	legacyBuilderIdentityFilename = "builder-identity.json"
	dockerConfigDirectory         = "docker-config"
	buildxConfigDirectory         = "buildx-config"
	buildkitImage                 = "moby/buildkit@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8"
	buildkitStatePath             = "/var/lib/buildkit"
	buildkitRemoteScheme          = "docker-container://"
	buildxBuilderListFormat       = "json"
	defaultStateQuotaBytes        = int64(2 << 30)
	minimumStateQuotaBytes        = int64(512 << 20)
	maximumStateQuotaBytes        = int64(16 << 30)
	buildkitPIDsLimit             = int64(512)
	buildkitMemoryHeadroom        = int64(1 << 30)

	defaultBuilderPrepareTimeout = 2 * time.Minute
	defaultBuilderOutputLimit    = 64 << 10
	builderStateSchema           = 2
)

// BuilderErrorCode is a durable, non-secret outcome of preparing the
// controller-owned BuildKit builder. It intentionally omits Docker output and
// command arguments.
type BuilderErrorCode string

const (
	BuilderConfigurationInvalid BuilderErrorCode = "builder_configuration_invalid"
	BuilderFilesystemInvalid    BuilderErrorCode = "builder_filesystem_invalid"
	BuilderDriftDetected        BuilderErrorCode = "builder_drift_detected"
	BuilderRuntimeUnavailable   BuilderErrorCode = "builder_runtime_unavailable"
	BuilderProvisionFailed      BuilderErrorCode = "builder_provision_failed"
	BuilderBootstrapFailed      BuilderErrorCode = "builder_bootstrap_failed"
	BuilderTimedOut             BuilderErrorCode = "builder_timeout"
	BuilderOutputTruncated      BuilderErrorCode = "builder_output_truncated"
	BuilderTerminationFailed    BuilderErrorCode = "builder_process_termination_failed"
	BuilderCancelled            BuilderErrorCode = "builder_cancelled"
	BuilderHardQuotaUnavailable BuilderErrorCode = "builder_hard_quota_unavailable"
	BuilderHardQuotaExhausted   BuilderErrorCode = "builder_hard_quota_exhausted"
)

// BuilderError carries only a stable code. Raw CLI output is cleared while
// classifying failures and never crosses this package boundary.
type BuilderError struct {
	Code  BuilderErrorCode
	cause error
}

func (e *BuilderError) Error() string { return "generated builder: " + string(e.Code) }
func (e *BuilderError) Unwrap() error { return e.cause }

func IsBuilderError(err error, code BuilderErrorCode) bool {
	var target *BuilderError
	return errors.As(err, &target) && target.Code == code
}

type builderAuthorityRefusal struct{ cause error }

func (e *builderAuthorityRefusal) Error() string { return "generated builder: authority refused" }
func (e *builderAuthorityRefusal) Unwrap() error { return e.cause }

func isBuilderAuthorityRefusal(err error) bool {
	var target *builderAuthorityRefusal
	return errors.As(err, &target)
}

// BuilderManagerOptions defines the fixed local execution boundary for the
// generated-image compiler. DockerExecutable is resolved once by the
// controller; arbitrary PATH lookup or remote endpoints are rejected.
type BuilderManagerOptions struct {
	DataRoot         string
	DockerExecutable string
	DockerEndpoint   string
	PrepareTimeout   time.Duration
	OutputLimit      int
	StateQuotaBytes  int64
}

// BuilderSession is an immutable view of a prepared BuildKit builder. It owns
// no credentials. Environment returns a copy so callers cannot mutate the
// manager's scoped Docker configuration.
type BuilderSession struct {
	DockerExecutable  string
	BuilderName       string
	environment       []string
	storageQuotaBytes int64
}

func (s BuilderSession) Environment() []string { return append([]string(nil), s.environment...) }

type builderIdentity struct {
	Schema      int    `json:"schema"`
	BuilderName string `json:"builderName"`
	NodeName    string `json:"nodeName"`
	NetworkName string `json:"networkName"`
}

type dockerNetwork struct {
	Name     string            `json:"Name"`
	Driver   string            `json:"Driver"`
	Scope    string            `json:"Scope"`
	Internal bool              `json:"Internal"`
	Labels   map[string]string `json:"Labels"`
	Options  map[string]string `json:"Options"`
}

type buildxBuilder struct {
	Name   string       `json:"Name"`
	Driver string       `json:"Driver"`
	Nodes  []buildxNode `json:"Nodes"`
}

type dockerInfo struct {
	OSType       string   `json:"OSType"`
	MemoryLimit  bool     `json:"MemoryLimit"`
	SwapLimit    bool     `json:"SwapLimit"`
	CPUCfsPeriod bool     `json:"CpuCfsPeriod"`
	CPUCfsQuota  bool     `json:"CpuCfsQuota"`
	PidsLimit    bool     `json:"PidsLimit"`
	Warnings     []string `json:"Warnings"`
}

type buildxNode struct {
	Name     string `json:"Name"`
	Endpoint string `json:"Endpoint"`
}

type dockerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}

type dockerConfiguredMount struct {
	Type         string `json:"Type"`
	Source       string `json:"Source"`
	Target       string `json:"Target"`
	ReadOnly     bool   `json:"ReadOnly"`
	TmpfsOptions *struct {
		SizeBytes int64 `json:"SizeBytes"`
		Mode      int64 `json:"Mode"`
	} `json:"TmpfsOptions"`
}

type buildkitContainer struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Running    bool `json:"Running"`
		Paused     bool `json:"Paused"`
		Restarting bool `json:"Restarting"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Cmd    []string          `json:"Cmd"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Memory       int64                      `json:"Memory"`
		MemorySwap   int64                      `json:"MemorySwap"`
		CPUPeriod    int64                      `json:"CpuPeriod"`
		CPUQuota     int64                      `json:"CpuQuota"`
		PidsLimit    int64                      `json:"PidsLimit"`
		NetworkMode  string                     `json:"NetworkMode"`
		Privileged   bool                       `json:"Privileged"`
		Binds        []string                   `json:"Binds"`
		Mounts       []dockerConfiguredMount    `json:"Mounts"`
		PortBindings map[string]json.RawMessage `json:"PortBindings"`
		LogConfig    struct {
			Type   string            `json:"Type"`
			Config map[string]string `json:"Config"`
		} `json:"LogConfig"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []dockerMount `json:"Mounts"`
}

// BuilderManager provisions and validates one durable Buildx builder. The
// mutex makes initialization idempotent for concurrent compile requests in a
// controller process; persisted identity makes restarts reuse only the exact
// controller-owned resources.
type BuilderManager struct {
	repository *deployments.Repository
	directory  *securetemp.GeneratedBuilderDirectory
	runner     runtimeprocess.CommandRunner
	options    BuilderManagerOptions

	// afterCommandPrepared is a deterministic test barrier. Production leaves
	// it nil; it never grants authority or receives a mutable repository handle.
	afterCommandPrepared func(runtimeprocess.CommandRequest)

	mu      sync.Mutex
	session *BuilderSession
}

func NewBuilderManager(repository *deployments.Repository, runner runtimeprocess.CommandRunner, options BuilderManagerOptions) (*BuilderManager, error) {
	if repository == nil || !repository.ExecutionConfigured() || nilGeneratedImageDependency(runner) || options.DataRoot == "" || pathsecurity.RejectWindowsNamespace(options.DataRoot) || !filepath.IsAbs(options.DataRoot) || filepath.Clean(options.DataRoot) != options.DataRoot {
		return nil, &BuilderError{Code: BuilderConfigurationInvalid}
	}
	if options.DockerExecutable == "" || pathsecurity.RejectWindowsNamespace(options.DockerExecutable) || !filepath.IsAbs(options.DockerExecutable) || filepath.Clean(options.DockerExecutable) != options.DockerExecutable || !localDockerEndpoint(options.DockerEndpoint) {
		return nil, &BuilderError{Code: BuilderConfigurationInvalid}
	}
	if options.PrepareTimeout == 0 {
		options.PrepareTimeout = defaultBuilderPrepareTimeout
	}
	if options.PrepareTimeout < time.Second || options.PrepareTimeout > 10*time.Minute || options.OutputLimit < 0 || options.OutputLimit > runtimeprocess.DefaultOutputLimit {
		return nil, &BuilderError{Code: BuilderConfigurationInvalid}
	}
	if options.OutputLimit == 0 {
		options.OutputLimit = defaultBuilderOutputLimit
	}
	if options.StateQuotaBytes == 0 {
		options.StateQuotaBytes = defaultStateQuotaBytes
	}
	if options.StateQuotaBytes < minimumStateQuotaBytes || options.StateQuotaBytes > maximumStateQuotaBytes {
		return nil, &BuilderError{Code: BuilderConfigurationInvalid}
	}
	directory, err := securetemp.NewGeneratedBuilderDirectory(options.DataRoot)
	if err != nil {
		return nil, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	return &BuilderManager{repository: repository, directory: directory, runner: runner, options: options}, nil
}

func (m *BuilderManager) usesRepository(repository *deployments.Repository) bool {
	return m != nil && repository != nil && m.repository == repository && repository.ExecutionConfigured()
}

// Prepare creates the network/builder if absent, bootstraps BuildKit, and
// verifies that the persisted name still resolves to a docker-container
// builder. It never adopts a same-named resource with a different driver or
// a network whose ownership labels do not match the persisted identity.
func (m *BuilderManager) prepare(ctx context.Context, selection deployments.GeneratedImageBuildSelection) (BuilderSession, error) {
	if m == nil || !m.usesRepository(m.repository) {
		return BuilderSession{}, &BuilderError{Code: BuilderConfigurationInvalid}
	}
	authority, err := m.repository.PinGeneratedImageCommand(selection)
	if err != nil {
		return BuilderSession{}, err
	}
	defer authority.Leave()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return BuilderSession{}, builderContextError(err)
	}
	identity, env, err := m.preparePersistentState()
	if err != nil {
		return BuilderSession{}, err
	}
	if err := m.verifyQuotaPlatform(ctx, env); err != nil {
		return BuilderSession{}, err
	}
	if err := m.ensureNetwork(ctx, authority, identity, env); err != nil {
		return BuilderSession{}, err
	}
	if err := m.ensureBuildkitContainer(ctx, authority, identity, env); err != nil {
		return BuilderSession{}, err
	}
	if err := m.ensureBuilder(ctx, authority, identity, env); err != nil {
		return BuilderSession{}, err
	}
	session := BuilderSession{
		DockerExecutable:  m.options.DockerExecutable,
		BuilderName:       identity.BuilderName,
		environment:       append([]string(nil), env...),
		storageQuotaBytes: m.options.StateQuotaBytes,
	}
	m.session = &session
	if err := authority.CheckObservation(); err != nil {
		return BuilderSession{}, err
	}
	return session, nil
}

func (m *BuilderManager) verifyQuotaPlatform(ctx context.Context, env []string) error {
	result, runErr := m.run(ctx, []string{"info", "--format", "{{json .}}"}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if code := commandErrorCode(ctx, result, runErr); code != "" {
		return &BuilderError{Code: code, cause: builderCause(ctx, runErr)}
	}
	if runErr != nil {
		return provisionError(ctx, result, runErr)
	}
	var info dockerInfo
	if json.Unmarshal(result.Stdout, &info) != nil || !supportsBuilderResourceControls(info) {
		return &BuilderError{Code: BuilderHardQuotaUnavailable}
	}
	return nil
}

func supportsBuilderResourceControls(info dockerInfo) bool {
	if info.OSType != "linux" || !info.MemoryLimit || !info.SwapLimit || !info.CPUCfsPeriod || !info.CPUCfsQuota || !info.PidsLimit {
		return false
	}
	for _, warning := range info.Warnings {
		normalized := strings.ToLower(strings.TrimSpace(warning))
		for _, unavailable := range []string{
			"no memory limit support",
			"no swap limit support",
			"no cpu cfs period support",
			"no cpu cfs quota support",
			"no pids limit support",
		} {
			if strings.Contains(normalized, unavailable) {
				return false
			}
		}
	}
	return true
}

func (m *BuilderManager) preparePersistentState() (builderIdentity, []string, error) {
	if err := m.rejectLegacyIdentity(); err != nil {
		return builderIdentity{}, nil, err
	}
	dockerConfig, err := m.directory.EnsureDirectory(dockerConfigDirectory)
	if err != nil {
		return builderIdentity{}, nil, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	buildxConfig, err := m.directory.EnsureDirectory(buildxConfigDirectory)
	if err != nil {
		return builderIdentity{}, nil, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	identity, err := m.loadOrCreateIdentity()
	if err != nil {
		return builderIdentity{}, nil, err
	}
	return identity, generatedBuilderEnvironment(m.options.DockerEndpoint, dockerConfig, buildxConfig), nil
}

func (m *BuilderManager) rejectLegacyIdentity() error {
	body, err := m.directory.ReadFile(legacyBuilderIdentityFilename, 8<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	clear(body)
	if err != nil {
		return &BuilderError{Code: BuilderFilesystemInvalid}
	}
	// Schema v1 used a different Buildx driver and a persistent state volume.
	// Never create v2 resources alongside it: an operator must remove the
	// legacy builder resources and identity deliberately before retrying.
	return &BuilderError{Code: BuilderDriftDetected}
}

func (m *BuilderManager) loadOrCreateIdentity() (builderIdentity, error) {
	body, err := m.directory.ReadFile(builderIdentityFilename, 8<<10)
	if errors.Is(err, os.ErrNotExist) {
		identity, createErr := newBuilderIdentity()
		if createErr != nil {
			return builderIdentity{}, &BuilderError{Code: BuilderFilesystemInvalid}
		}
		encoded, marshalErr := json.Marshal(identity)
		if marshalErr != nil {
			return builderIdentity{}, &BuilderError{Code: BuilderFilesystemInvalid}
		}
		writeErr := m.directory.WriteNewFile(builderIdentityFilename, encoded)
		clear(encoded)
		if writeErr != nil && !errors.Is(writeErr, os.ErrExist) {
			return builderIdentity{}, &BuilderError{Code: BuilderFilesystemInvalid}
		}
		body, err = m.directory.ReadFile(builderIdentityFilename, 8<<10)
	}
	if err != nil {
		return builderIdentity{}, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	defer clear(body)
	var identity builderIdentity
	if json.Unmarshal(body, &identity) != nil || !validBuilderIdentity(identity) {
		return builderIdentity{}, &BuilderError{Code: BuilderDriftDetected}
	}
	return identity, nil
}

func newBuilderIdentity() (builderIdentity, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return builderIdentity{}, err
	}
	suffix := hex.EncodeToString(random)
	return builderIdentity{
		Schema: builderStateSchema, BuilderName: "rig-buildkit-" + suffix,
		NodeName: "rig-node-" + suffix, NetworkName: "rig-buildnet-" + suffix,
	}, nil
}

func validBuilderIdentity(identity builderIdentity) bool {
	return identity.Schema == builderStateSchema && validControllerName(identity.BuilderName, "rig-buildkit-") && validControllerName(identity.NodeName, "rig-node-") && validControllerName(identity.NetworkName, "rig-buildnet-")
}

func validControllerName(value, prefix string) bool {
	if len(value) < len(prefix)+24 || len(value) > 63 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func (m *BuilderManager) ensureNetwork(ctx context.Context, authority *deployments.GeneratedImageCommandExecution, identity builderIdentity, env []string) error {
	network, found, err := m.inspectNetwork(ctx, identity.NetworkName, env)
	if err != nil {
		return err
	}
	if found {
		if !matchesNetwork(network, identity) {
			return builderFailure(ctx, BuilderDriftDetected)
		}
		return authority.CheckObservation()
	}
	result, runErr := m.runMutation(ctx, authority.CheckBeforeBuilderNetworkCreate, []string{"network", "create", "--driver", "bridge", "--opt", "com.docker.network.bridge.enable_icc=false", "--label", "rig.controller=generated-builder", "--label", "rig.builder=" + identity.BuilderName, "--label", "rig.network=" + identity.NetworkName, identity.NetworkName}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if isBuilderAuthorityRefusal(runErr) {
			return runErr
		}
		// A concurrent controller may have created the exact resource. Inspect
		// before classifying this as a provisioning failure.
		retried, retryFound, retryErr := m.inspectNetwork(ctx, identity.NetworkName, env)
		if retryErr == nil && retryFound && matchesNetwork(retried, identity) {
			if observationErr := authority.CheckObservation(); observationErr == nil {
				return nil
			} else {
				return augmentBuilderFailure(ctx, provisionError(ctx, result, runErr), retryErr, observationErr)
			}
		}
		return augmentBuilderFailure(ctx, provisionError(ctx, result, runErr), retryErr)
	}
	network, found, err = m.inspectNetwork(ctx, identity.NetworkName, env)
	if err != nil {
		return err
	}
	if !found || !matchesNetwork(network, identity) {
		return builderFailure(ctx, BuilderDriftDetected)
	}
	return authority.CheckObservation()
}

func (m *BuilderManager) inspectNetwork(ctx context.Context, name string, env []string) (dockerNetwork, bool, error) {
	result, runErr := m.run(ctx, []string{"network", "inspect", "--format", "{{json .}}", name}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if definitiveDockerInspectAbsence(ctx, result, runErr,
			"Error response from daemon: network "+name+" not found") {
			return dockerNetwork{}, false, nil
		}
		return dockerNetwork{}, false, provisionError(ctx, result, runErr)
	}
	var network dockerNetwork
	if result.StdoutTruncated || result.StderrTruncated || json.Unmarshal(result.Stdout, &network) != nil {
		return dockerNetwork{}, false, builderFailure(ctx, BuilderDriftDetected)
	}
	if cause := builderCause(ctx, nil); cause != nil {
		return dockerNetwork{}, false, &BuilderError{Code: BuilderCancelled, cause: cause}
	}
	return network, true, nil
}

func matchesNetwork(network dockerNetwork, identity builderIdentity) bool {
	return network.Name == identity.NetworkName && network.Driver == "bridge" && network.Scope == "local" && !network.Internal && network.Options["com.docker.network.bridge.enable_icc"] == "false" && network.Labels["rig.controller"] == "generated-builder" && network.Labels["rig.builder"] == identity.BuilderName && network.Labels["rig.network"] == identity.NetworkName
}

func (m *BuilderManager) ensureBuilder(ctx context.Context, authority *deployments.GeneratedImageCommandExecution, identity builderIdentity, env []string) error {
	builder, found, err := m.findBuilder(ctx, identity.BuilderName, env)
	if err != nil {
		return err
	}
	if found && !matchesBuildxBuilder(builder, identity) {
		return builderFailure(ctx, BuilderDriftDetected)
	}
	if !found {
		result, runErr := m.runMutation(ctx, authority.CheckBeforeBuildxCreate, []string{
			"buildx", "create", "--name", identity.BuilderName, "--node", identity.NodeName,
			"--driver", "remote", "--driver-opt", "default-load=false", "--use",
			buildkitRemoteEndpoint(identity),
		}, env)
		defer clear(result.Stdout)
		defer clear(result.Stderr)
		if runErr != nil {
			if isBuilderAuthorityRefusal(runErr) {
				return runErr
			}
			retry, retryFound, retryErr := m.findBuilder(ctx, identity.BuilderName, env)
			if retryErr == nil && retryFound && matchesBuildxBuilder(retry, identity) {
				if bootstrapErr := m.bootstrapAndVerify(ctx, authority, identity, env); bootstrapErr == nil {
					return nil
				} else {
					return augmentBuilderFailure(ctx, provisionError(ctx, result, runErr), retryErr, bootstrapErr)
				}
			}
			return augmentBuilderFailure(ctx, provisionError(ctx, result, runErr), retryErr)
		}
		builder, found, err = m.findBuilder(ctx, identity.BuilderName, env)
		if err != nil {
			return err
		}
		if !found || !matchesBuildxBuilder(builder, identity) {
			return builderFailure(ctx, BuilderDriftDetected)
		}
	}
	return m.bootstrapAndVerify(ctx, authority, identity, env)
}

func (m *BuilderManager) bootstrapAndVerify(ctx context.Context, authority *deployments.GeneratedImageCommandExecution, identity builderIdentity, env []string) error {
	result, runErr := m.runMutation(ctx, authority.CheckBeforeBuilderBootstrap, []string{"buildx", "inspect", "--builder", identity.BuilderName, "--bootstrap"}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if isBuilderAuthorityRefusal(runErr) {
			return runErr
		}
		return bootstrapError(ctx, result, runErr)
	}
	builder, found, err := m.findBuilder(ctx, identity.BuilderName, env)
	if err != nil {
		return err
	}
	if !found || !matchesBuildxBuilder(builder, identity) {
		return builderFailure(ctx, BuilderDriftDetected)
	}
	container, found, err := m.inspectBuildkitContainer(ctx, identity, env)
	if err != nil {
		return err
	}
	if !found || !matchesBuildkitContainer(container, identity, m.options.StateQuotaBytes) {
		return builderFailure(ctx, BuilderDriftDetected)
	}
	return authority.CheckObservation()
}

func matchesBuildxBuilder(builder buildxBuilder, identity builderIdentity) bool {
	return builder.Name == identity.BuilderName && builder.Driver == "remote" && len(builder.Nodes) == 1 && builder.Nodes[0].Name == identity.NodeName && builder.Nodes[0].Endpoint == buildkitRemoteEndpoint(identity)
}

func buildkitRemoteEndpoint(identity builderIdentity) string {
	return buildkitRemoteScheme + buildkitContainerName(identity)
}

func (m *BuilderManager) ensureBuildkitContainer(ctx context.Context, authority *deployments.GeneratedImageCommandExecution, identity builderIdentity, env []string) error {
	container, found, err := m.inspectBuildkitContainer(ctx, identity, env)
	if err != nil {
		return err
	}
	if found {
		if !matchesBuildkitContainerConfiguration(container, identity, m.options.StateQuotaBytes) {
			return builderFailure(ctx, BuilderDriftDetected)
		}
		return m.recoverBuildkitContainer(ctx, authority, identity, container, env)
	}

	quota := fmt.Sprintf("%d", m.options.StateQuotaBytes)
	args := []string{
		"container", "run", "--detach", "--name", buildkitContainerName(identity),
		"--privileged", "--network", identity.NetworkName,
		"--restart", "unless-stopped",
		"--memory", fmt.Sprintf("%d", buildkitMemoryLimit(m.options.StateQuotaBytes)), "--memory-swap", fmt.Sprintf("%d", buildkitMemoryLimit(m.options.StateQuotaBytes)),
		"--cpu-period", "100000", "--cpu-quota", "100000", "--pids-limit", fmt.Sprintf("%d", buildkitPIDsLimit),
		"--log-driver", "json-file", "--log-opt", "max-size=10m", "--log-opt", "max-file=1",
		"--mount", "type=tmpfs,destination=" + buildkitStatePath + ",tmpfs-size=" + quota + ",tmpfs-mode=0700",
		"--label", "rig.controller=generated-builder", "--label", "rig.builder=" + identity.BuilderName,
		"--label", "rig.network=" + identity.NetworkName, "--label", "rig.quota.bytes=" + quota,
		buildkitImage,
	}
	args = append(args, buildkitCommand(m.options.StateQuotaBytes)...)
	result, runErr := m.runMutation(ctx, authority.CheckBeforeBuildkitRun, args, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if isBuilderAuthorityRefusal(runErr) {
			return runErr
		}
		retried, retryFound, retryErr := m.inspectBuildkitContainer(ctx, identity, env)
		if retryErr == nil && retryFound {
			if !matchesBuildkitContainerConfiguration(retried, identity, m.options.StateQuotaBytes) {
				return augmentBuilderFailure(ctx, quotaProvisionError(ctx, result, runErr), builderFailure(ctx, BuilderDriftDetected))
			}
			if recoverErr := m.recoverBuildkitContainer(ctx, authority, identity, retried, env); recoverErr == nil {
				return nil
			} else {
				return augmentBuilderFailure(ctx, quotaProvisionError(ctx, result, runErr), recoverErr)
			}
		}
		return augmentBuilderFailure(ctx, quotaProvisionError(ctx, result, runErr), retryErr)
	}
	container, found, err = m.inspectBuildkitContainer(ctx, identity, env)
	if err != nil {
		return err
	}
	if !found || !matchesBuildkitContainerConfiguration(container, identity, m.options.StateQuotaBytes) {
		return builderFailure(ctx, BuilderDriftDetected)
	}
	return m.recoverBuildkitContainer(ctx, authority, identity, container, env)
}

func (m *BuilderManager) recoverBuildkitContainer(ctx context.Context, authority *deployments.GeneratedImageCommandExecution, identity builderIdentity, container buildkitContainer, env []string) error {
	if buildkitContainerReady(container) {
		return authority.CheckObservation()
	}
	var args []string
	var check func() error
	switch {
	case container.State.Restarting:
		args = []string{"container", "restart", "--time", "10", container.ID}
		check = authority.CheckBeforeBuildkitRestart
	case container.State.Paused:
		args = []string{"container", "unpause", container.ID}
		check = authority.CheckBeforeBuildkitUnpause
	default:
		args = []string{"container", "start", container.ID}
		check = authority.CheckBeforeBuildkitStart
	}
	result, runErr := m.runMutation(ctx, check, args, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if isBuilderAuthorityRefusal(runErr) {
		return runErr
	}
	recovered, found, inspectErr := m.inspectBuildkitContainer(ctx, identity, env)
	if inspectErr != nil {
		return reconcileBuilderMutation(ctx, result, runErr, inspectErr)
	}
	if !found || recovered.ID != container.ID || !matchesBuildkitContainerConfiguration(recovered, identity, m.options.StateQuotaBytes) {
		return reconcileBuilderMutation(ctx, result, runErr, builderFailure(ctx, BuilderDriftDetected))
	}
	if buildkitContainerReady(recovered) {
		if observationErr := authority.CheckObservation(); observationErr != nil {
			return reconcileBuilderMutation(ctx, result, runErr, observationErr)
		}
		return nil
	}
	if runErr != nil {
		return provisionError(ctx, result, runErr)
	}
	return builderFailure(ctx, BuilderProvisionFailed)
}

func (m *BuilderManager) inspectBuildkitContainer(ctx context.Context, identity builderIdentity, env []string) (buildkitContainer, bool, error) {
	name := buildkitContainerName(identity)
	result, runErr := m.run(ctx, []string{"container", "inspect", "--format", "{{json .}}", name}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if definitiveDockerInspectAbsence(ctx, result, runErr,
			"Error: No such container: "+name,
			"Error response from daemon: No such container: "+name) {
			return buildkitContainer{}, false, nil
		}
		return buildkitContainer{}, false, provisionError(ctx, result, runErr)
	}
	var container buildkitContainer
	if result.StdoutTruncated || result.StderrTruncated || json.Unmarshal(result.Stdout, &container) != nil {
		return buildkitContainer{}, false, builderFailure(ctx, BuilderDriftDetected)
	}
	if cause := builderCause(ctx, nil); cause != nil {
		return buildkitContainer{}, false, &BuilderError{Code: BuilderCancelled, cause: cause}
	}
	return container, true, nil
}

func buildkitContainerName(identity builderIdentity) string {
	return "rig-buildkitd-" + strings.TrimPrefix(identity.NodeName, "rig-node-")
}

func buildkitMemoryLimit(quotaBytes int64) int64 {
	return quotaBytes + buildkitMemoryHeadroom
}

func buildkitCommand(quotaBytes int64) []string {
	return []string{
		"--oci-worker-net", "bridge",
		"--oci-worker-gc",
		"--oci-worker-gc-keepstorage=" + fmt.Sprintf("%d", quotaBytes/(2*1_000_000)),
		"--oci-max-parallelism=1",
	}
}

func matchesBuildkitContainer(container buildkitContainer, identity builderIdentity, quotaBytes int64) bool {
	return matchesBuildkitContainerConfiguration(container, identity, quotaBytes) && buildkitContainerReady(container)
}

func matchesBuildkitContainerConfiguration(container buildkitContainer, identity builderIdentity, quotaBytes int64) bool {
	wantLabels := map[string]string{
		"rig.controller":  "generated-builder",
		"rig.builder":     identity.BuilderName,
		"rig.network":     identity.NetworkName,
		"rig.quota.bytes": fmt.Sprintf("%d", quotaBytes),
	}
	if !lowerHex(container.ID, 64) || container.Name != "/"+buildkitContainerName(identity) || !validImageContentID(container.Image) || container.Config.Image != buildkitImage || !equalStringSlices(container.Config.Cmd, buildkitCommand(quotaBytes)) {
		return false
	}
	for key, value := range wantLabels {
		if container.Config.Labels[key] != value {
			return false
		}
	}
	if container.HostConfig.Memory != buildkitMemoryLimit(quotaBytes) || container.HostConfig.MemorySwap != buildkitMemoryLimit(quotaBytes) || container.HostConfig.CPUPeriod != 100000 || container.HostConfig.CPUQuota != 100000 || container.HostConfig.PidsLimit != buildkitPIDsLimit || !container.HostConfig.Privileged || container.HostConfig.NetworkMode != identity.NetworkName || len(container.HostConfig.Binds) != 0 || len(container.HostConfig.PortBindings) != 0 || container.HostConfig.RestartPolicy.Name != "unless-stopped" || container.HostConfig.LogConfig.Type != "json-file" || len(container.HostConfig.LogConfig.Config) != 2 || container.HostConfig.LogConfig.Config["max-size"] != "10m" || container.HostConfig.LogConfig.Config["max-file"] != "1" || len(container.NetworkSettings.Networks) != 1 {
		return false
	}
	_, connected := container.NetworkSettings.Networks[identity.NetworkName]
	if !connected || len(container.HostConfig.Mounts) != 1 || len(container.Mounts) != 1 {
		return false
	}
	configured := container.HostConfig.Mounts[0]
	active := container.Mounts[0]
	return configured.Type == "tmpfs" && configured.Source == "" && configured.Target == buildkitStatePath && !configured.ReadOnly && configured.TmpfsOptions != nil && configured.TmpfsOptions.SizeBytes == quotaBytes && configured.TmpfsOptions.Mode == 0o700 && active.Type == "tmpfs" && active.Source == "" && active.Destination == buildkitStatePath && active.RW && active.Mode == "" && active.Propagation == ""
}

func buildkitContainerReady(container buildkitContainer) bool {
	return container.State.Running && !container.State.Paused && !container.State.Restarting
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (m *BuilderManager) findBuilder(ctx context.Context, name string, env []string) (buildxBuilder, bool, error) {
	// Buildx emits one record per builder only in its native JSON mode. A
	// custom {{json .}} template also emits one duplicate record per node.
	result, runErr := m.run(ctx, []string{"buildx", "ls", "--format", buildxBuilderListFormat}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		return buildxBuilder{}, false, provisionError(ctx, result, runErr)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return buildxBuilder{}, false, builderFailure(ctx, BuilderOutputTruncated)
	}
	var match *buildxBuilder
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var builder buildxBuilder
		if json.Unmarshal([]byte(line), &builder) != nil || builder.Name == "" || builder.Driver == "" {
			return buildxBuilder{}, false, builderFailure(ctx, BuilderDriftDetected)
		}
		if builder.Name != name {
			continue
		}
		if match != nil {
			return buildxBuilder{}, false, builderFailure(ctx, BuilderDriftDetected)
		}
		copy := builder
		match = &copy
	}
	if match == nil {
		if cause := builderCause(ctx, nil); cause != nil {
			return buildxBuilder{}, false, &BuilderError{Code: BuilderCancelled, cause: cause}
		}
		return buildxBuilder{}, false, nil
	}
	if cause := builderCause(ctx, nil); cause != nil {
		return buildxBuilder{}, false, &BuilderError{Code: BuilderCancelled, cause: cause}
	}
	return *match, true, nil
}

func (m *BuilderManager) run(ctx context.Context, args []string, env []string) (runtimeprocess.CommandResult, error) {
	return m.runner.Run(ctx, m.commandRequest(args, env))
}

func (m *BuilderManager) commandRequest(args []string, env []string) runtimeprocess.CommandRequest {
	return runtimeprocess.CommandRequest{
		Executable:  m.options.DockerExecutable,
		Args:        append([]string(nil), args...),
		Directory:   m.directory.Root(),
		Env:         append([]string(nil), env...),
		Timeout:     m.options.PrepareTimeout,
		OutputLimit: m.options.OutputLimit,
	}
}

func (m *BuilderManager) runMutation(ctx context.Context, check func() error, args []string, env []string) (runtimeprocess.CommandResult, error) {
	request := m.commandRequest(args, env)
	if m.afterCommandPrepared != nil {
		m.afterCommandPrepared(request)
	}
	if check == nil {
		return runtimeprocess.CommandResult{}, &builderAuthorityRefusal{cause: deploymentexecution.ErrInvalidConfiguration}
	}
	if err := check(); err != nil {
		return runtimeprocess.CommandResult{}, &builderAuthorityRefusal{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return runtimeprocess.CommandResult{}, &builderAuthorityRefusal{cause: errors.Join(deploymentexecution.ErrCanceled, err, context.Cause(ctx))}
	}
	return m.runner.Run(ctx, request)
}

func generatedBuilderEnvironment(endpoint, dockerConfig, buildxConfig string) []string {
	values := make(map[string]string, 8)
	// Keep only process-launch essentials. DOCKER_CONFIG and BUILDX_CONFIG
	// replace all inherited credential/config lookup locations.
	for _, key := range []string{"PATH", "PATHEXT", "SystemRoot", "TEMP", "TMP", "WINDIR"} {
		if value, exists := os.LookupEnv(key); exists {
			values[key] = value
		}
	}
	values["DOCKER_CONFIG"] = dockerConfig
	values["BUILDX_CONFIG"] = buildxConfig
	if endpoint != "" {
		values["DOCKER_HOST"] = endpoint
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

func definitiveDockerInspectAbsence(ctx context.Context, result runtimeprocess.CommandResult, runErr error, exactMessages ...string) bool {
	if ctx == nil || ctx.Err() != nil || runErr == nil || result.StdoutTruncated || result.StderrTruncated || strings.TrimSpace(string(result.Stdout)) != "" ||
		errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, runtimeprocess.ErrTerminationFailed) {
		return false
	}
	exitErr, ok := runErr.(*exec.ExitError)
	if !ok || exitErr.ProcessState == nil || !exitErr.ProcessState.Exited() || exitErr.ProcessState.ExitCode() != 1 {
		return false
	}
	message := strings.TrimSpace(string(result.Stderr))
	for _, expected := range exactMessages {
		if message == expected {
			return true
		}
	}
	return false
}

func provisionError(ctx context.Context, result runtimeprocess.CommandResult, err error) error {
	if code := commandErrorCode(ctx, result, err); code != "" {
		return &BuilderError{Code: code, cause: builderCause(ctx, err)}
	}
	return &BuilderError{Code: BuilderProvisionFailed, cause: builderCause(ctx, err)}
}

func quotaProvisionError(ctx context.Context, result runtimeprocess.CommandResult, err error) error {
	if code := commandErrorCode(ctx, result, err); code != "" {
		return &BuilderError{Code: code, cause: builderCause(ctx, err)}
	}
	output := strings.ToLower(string(append(append([]byte(nil), result.Stdout...), result.Stderr...)))
	if strings.Contains(output, "no space left on device") || strings.Contains(output, "disk quota exceeded") {
		return &BuilderError{Code: BuilderHardQuotaExhausted, cause: builderCause(ctx, err)}
	}
	if strings.Contains(output, "tmpfs") && (strings.Contains(output, "not supported") || strings.Contains(output, "unsupported") || strings.Contains(output, "invalid mount config") || strings.Contains(output, "unknown mount option")) {
		return &BuilderError{Code: BuilderHardQuotaUnavailable, cause: builderCause(ctx, err)}
	}
	return &BuilderError{Code: BuilderProvisionFailed, cause: builderCause(ctx, err)}
}

func bootstrapError(ctx context.Context, result runtimeprocess.CommandResult, err error) error {
	if code := commandErrorCode(ctx, result, err); code != "" {
		return &BuilderError{Code: code, cause: builderCause(ctx, err)}
	}
	return &BuilderError{Code: BuilderBootstrapFailed, cause: builderCause(ctx, err)}
}

func builderContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &BuilderError{Code: BuilderCancelled, cause: errors.Join(deploymentexecution.ErrCanceled, err)}
	}
	return &BuilderError{Code: BuilderTimedOut, cause: errors.Join(deploymentexecution.ErrCanceled, err)}
}

func builderCause(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(err, deploymentexecution.ErrCanceled, ctx.Err(), context.Cause(ctx))
	}
	return err
}

func builderFailure(ctx context.Context, code BuilderErrorCode, causes ...error) error {
	causes = append(causes, builderCause(ctx, nil))
	return &BuilderError{Code: code, cause: errors.Join(causes...)}
}

func augmentBuilderFailure(ctx context.Context, base error, causes ...error) error {
	var typed *BuilderError
	code := BuilderProvisionFailed
	if errors.As(base, &typed) {
		code = typed.Code
	}
	causes = append([]error{base}, causes...)
	return builderFailure(ctx, code, causes...)
}

func reconcileBuilderMutation(ctx context.Context, result runtimeprocess.CommandResult, runErr error, causes ...error) error {
	if runErr != nil {
		return augmentBuilderFailure(ctx, provisionError(ctx, result, runErr), causes...)
	}
	causes = append(causes, builderCause(ctx, nil))
	return errors.Join(causes...)
}

func commandErrorCode(ctx context.Context, result runtimeprocess.CommandResult, err error) BuilderErrorCode {
	if errors.Is(ctx.Err(), context.Canceled) {
		return BuilderCancelled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return BuilderTimedOut
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return BuilderTerminationFailed
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return BuilderOutputTruncated
	}
	if err == nil {
		return ""
	}
	output := strings.ToLower(string(append(append([]byte(nil), result.Stdout...), result.Stderr...)))
	if strings.Contains(output, "cannot connect to the docker daemon") || strings.Contains(output, "error during connect") || strings.Contains(output, "is the docker daemon running") {
		return BuilderRuntimeUnavailable
	}
	return ""
}
