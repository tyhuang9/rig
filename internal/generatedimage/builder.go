package generatedimage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/pathsecurity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const (
	builderIdentityFilename = "builder-identity.json"
	buildkitConfigFilename  = "buildkitd.toml"
	dockerConfigDirectory   = "docker-config"
	buildxConfigDirectory   = "buildx-config"

	defaultBuilderPrepareTimeout = 2 * time.Minute
	defaultBuilderOutputLimit    = 64 << 10
	builderStateSchema           = 1
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
)

// BuilderError carries only a stable code. Raw CLI output is cleared while
// classifying failures and never crosses this package boundary.
type BuilderError struct{ Code BuilderErrorCode }

func (e *BuilderError) Error() string { return "generated builder: " + string(e.Code) }

func IsBuilderError(err error, code BuilderErrorCode) bool {
	var target *BuilderError
	return errors.As(err, &target) && target.Code == code
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
}

// BuilderSession is an immutable view of a prepared BuildKit builder. It owns
// no credentials. Environment returns a copy so callers cannot mutate the
// manager's scoped Docker configuration.
type BuilderSession struct {
	DockerExecutable string
	BuilderName      string
	environment      []string
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
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
}

type buildkitContainer struct {
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Memory      int64  `json:"Memory"`
		MemorySwap  int64  `json:"MemorySwap"`
		CPUPeriod   int64  `json:"CpuPeriod"`
		CPUQuota    int64  `json:"CpuQuota"`
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

// BuilderManager provisions and validates one durable Buildx builder. The
// mutex makes initialization idempotent for concurrent compile requests in a
// controller process; persisted identity makes restarts reuse only the exact
// controller-owned resources.
type BuilderManager struct {
	directory *securetemp.GeneratedBuilderDirectory
	runner    runtimeprocess.CommandRunner
	options   BuilderManagerOptions

	mu      sync.Mutex
	session *BuilderSession
}

func NewBuilderManager(runner runtimeprocess.CommandRunner, options BuilderManagerOptions) (*BuilderManager, error) {
	if runner == nil || options.DataRoot == "" || pathsecurity.RejectWindowsNamespace(options.DataRoot) || !filepath.IsAbs(options.DataRoot) || filepath.Clean(options.DataRoot) != options.DataRoot {
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
	directory, err := securetemp.NewGeneratedBuilderDirectory(options.DataRoot)
	if err != nil {
		return nil, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	return &BuilderManager{directory: directory, runner: runner, options: options}, nil
}

// Prepare creates the network/builder if absent, bootstraps BuildKit, and
// verifies that the persisted name still resolves to a docker-container
// builder. It never adopts a same-named resource with a different driver or
// a network whose ownership labels do not match the persisted identity.
func (m *BuilderManager) Prepare(ctx context.Context) (BuilderSession, error) {
	if m == nil {
		return BuilderSession{}, &BuilderError{Code: BuilderConfigurationInvalid}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return BuilderSession{}, builderContextError(err)
	}
	identity, env, err := m.preparePersistentState()
	if err != nil {
		return BuilderSession{}, err
	}
	if err := m.ensureNetwork(ctx, identity, env); err != nil {
		return BuilderSession{}, err
	}
	if err := m.ensureBuilder(ctx, identity, env); err != nil {
		return BuilderSession{}, err
	}
	session := BuilderSession{
		DockerExecutable: m.options.DockerExecutable,
		BuilderName:      identity.BuilderName,
		environment:      append([]string(nil), env...),
	}
	m.session = &session
	return session, nil
}

func (m *BuilderManager) preparePersistentState() (builderIdentity, []string, error) {
	dockerConfig, err := m.directory.EnsureDirectory(dockerConfigDirectory)
	if err != nil {
		return builderIdentity{}, nil, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	buildxConfig, err := m.directory.EnsureDirectory(buildxConfigDirectory)
	if err != nil {
		return builderIdentity{}, nil, &BuilderError{Code: BuilderFilesystemInvalid}
	}
	if err := m.ensureFixedFile(buildkitConfigFilename, []byte(buildkitdConfiguration)); err != nil {
		return builderIdentity{}, nil, err
	}
	identity, err := m.loadOrCreateIdentity()
	if err != nil {
		return builderIdentity{}, nil, err
	}
	return identity, generatedBuilderEnvironment(m.options.DockerEndpoint, dockerConfig, buildxConfig), nil
}

func (m *BuilderManager) ensureFixedFile(name string, contents []byte) error {
	existing, err := m.directory.ReadFile(name, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := m.directory.WriteNewFile(name, append([]byte(nil), contents...)); writeErr != nil && !errors.Is(writeErr, os.ErrExist) {
			return &BuilderError{Code: BuilderFilesystemInvalid}
		}
		existing, err = m.directory.ReadFile(name, 16<<10)
	}
	if err != nil {
		return &BuilderError{Code: BuilderFilesystemInvalid}
	}
	defer clear(existing)
	if string(existing) != string(contents) {
		return &BuilderError{Code: BuilderDriftDetected}
	}
	return nil
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

func (m *BuilderManager) ensureNetwork(ctx context.Context, identity builderIdentity, env []string) error {
	network, found, err := m.inspectNetwork(ctx, identity.NetworkName, env)
	if err != nil {
		return err
	}
	if found {
		if !matchesNetwork(network, identity) {
			return &BuilderError{Code: BuilderDriftDetected}
		}
		return nil
	}
	result, runErr := m.run(ctx, []string{"network", "create", "--driver", "bridge", "--opt", "com.docker.network.bridge.enable_icc=false", "--label", "rig.controller=generated-builder", "--label", "rig.builder=" + identity.BuilderName, "--label", "rig.network=" + identity.NetworkName, identity.NetworkName}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		// A concurrent controller may have created the exact resource. Inspect
		// before classifying this as a provisioning failure.
		if retried, retryFound, retryErr := m.inspectNetwork(ctx, identity.NetworkName, env); retryErr == nil && retryFound && matchesNetwork(retried, identity) {
			return nil
		}
		return provisionError(ctx, result, runErr)
	}
	network, found, err = m.inspectNetwork(ctx, identity.NetworkName, env)
	if err != nil {
		return err
	}
	if !found || !matchesNetwork(network, identity) {
		return &BuilderError{Code: BuilderDriftDetected}
	}
	return nil
}

func (m *BuilderManager) inspectNetwork(ctx context.Context, name string, env []string) (dockerNetwork, bool, error) {
	result, runErr := m.run(ctx, []string{"network", "inspect", "--format", "{{json .}}", name}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if dockerNotFound(result) {
			return dockerNetwork{}, false, nil
		}
		return dockerNetwork{}, false, provisionError(ctx, result, runErr)
	}
	var network dockerNetwork
	if json.Unmarshal(result.Stdout, &network) != nil {
		return dockerNetwork{}, false, &BuilderError{Code: BuilderDriftDetected}
	}
	return network, true, nil
}

func matchesNetwork(network dockerNetwork, identity builderIdentity) bool {
	return network.Name == identity.NetworkName && network.Driver == "bridge" && network.Scope == "local" && !network.Internal && network.Options["com.docker.network.bridge.enable_icc"] == "false" && network.Labels["rig.controller"] == "generated-builder" && network.Labels["rig.builder"] == identity.BuilderName && network.Labels["rig.network"] == identity.NetworkName
}

func (m *BuilderManager) ensureBuilder(ctx context.Context, identity builderIdentity, env []string) error {
	builder, found, err := m.findBuilder(ctx, identity.BuilderName, env)
	if err != nil {
		return err
	}
	if found && builder.Driver != "docker-container" {
		return &BuilderError{Code: BuilderDriftDetected}
	}
	if !found {
		result, runErr := m.run(ctx, []string{
			"buildx", "create", "--name", identity.BuilderName, "--node", identity.NodeName,
			"--driver", "docker-container",
			"--driver-opt", "network=" + identity.NetworkName,
			"--driver-opt", "memory=2147483648",
			"--driver-opt", "memory-swap=2147483648",
			"--driver-opt", "cpu-period=100000",
			"--driver-opt", "cpu-quota=100000",
			"--buildkitd-config", filepath.Join(m.directory.Root(), buildkitConfigFilename),
			"--use",
		}, env)
		defer clear(result.Stdout)
		defer clear(result.Stderr)
		if runErr != nil {
			// Do not assume an "already exists" error is safe: exact listing is
			// required before another process's builder can be reused.
			if retry, retryFound, retryErr := m.findBuilder(ctx, identity.BuilderName, env); retryErr == nil && retryFound && retry.Driver == "docker-container" {
				return m.bootstrapAndVerify(ctx, identity, env)
			}
			return provisionError(ctx, result, runErr)
		}
	}
	return m.bootstrapAndVerify(ctx, identity, env)
}

func (m *BuilderManager) bootstrapAndVerify(ctx context.Context, identity builderIdentity, env []string) error {
	result, runErr := m.run(ctx, []string{"buildx", "inspect", "--builder", identity.BuilderName, "--bootstrap"}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		return bootstrapError(ctx, result, runErr)
	}
	builder, found, err := m.findBuilder(ctx, identity.BuilderName, env)
	if err != nil {
		return err
	}
	if !found || builder.Driver != "docker-container" {
		return &BuilderError{Code: BuilderDriftDetected}
	}
	return m.verifyBuildkitContainer(ctx, identity, env)
}

func (m *BuilderManager) verifyBuildkitContainer(ctx context.Context, identity builderIdentity, env []string) error {
	name := buildkitContainerName(identity)
	result, runErr := m.run(ctx, []string{"container", "inspect", "--format", "{{json .}}", name}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		if dockerNotFound(result) {
			return &BuilderError{Code: BuilderDriftDetected}
		}
		return provisionError(ctx, result, runErr)
	}
	var container buildkitContainer
	if json.Unmarshal(result.Stdout, &container) != nil || !matchesBuildkitContainer(container, identity) {
		return &BuilderError{Code: BuilderDriftDetected}
	}
	return nil
}

func buildkitContainerName(identity builderIdentity) string {
	// docker-container Buildx names the Docker container from the Buildx node,
	// not from arbitrary repository input. NodeName is persisted and random.
	return "buildx_buildkit_" + identity.NodeName
}

func matchesBuildkitContainer(container buildkitContainer, identity builderIdentity) bool {
	if container.Name != "/"+buildkitContainerName(identity) || container.HostConfig.Memory != 2<<30 || container.HostConfig.MemorySwap != 2<<30 || container.HostConfig.CPUPeriod != 100000 || container.HostConfig.CPUQuota != 100000 || container.HostConfig.NetworkMode != identity.NetworkName || len(container.NetworkSettings.Networks) != 1 {
		return false
	}
	_, connected := container.NetworkSettings.Networks[identity.NetworkName]
	return connected
}

func (m *BuilderManager) findBuilder(ctx context.Context, name string, env []string) (buildxBuilder, bool, error) {
	result, runErr := m.run(ctx, []string{"buildx", "ls", "--format", "{{json .}}"}, env)
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if runErr != nil {
		return buildxBuilder{}, false, provisionError(ctx, result, runErr)
	}
	var match *buildxBuilder
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var builder buildxBuilder
		if json.Unmarshal([]byte(line), &builder) != nil || builder.Name == "" || builder.Driver == "" {
			return buildxBuilder{}, false, &BuilderError{Code: BuilderDriftDetected}
		}
		if builder.Name != name {
			continue
		}
		if match != nil {
			return buildxBuilder{}, false, &BuilderError{Code: BuilderDriftDetected}
		}
		copy := builder
		match = &copy
	}
	if match == nil {
		return buildxBuilder{}, false, nil
	}
	return *match, true, nil
}

func (m *BuilderManager) run(ctx context.Context, args []string, env []string) (runtimeprocess.CommandResult, error) {
	return m.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable:  m.options.DockerExecutable,
		Args:        append([]string(nil), args...),
		Directory:   m.directory.Root(),
		Env:         append([]string(nil), env...),
		Timeout:     m.options.PrepareTimeout,
		OutputLimit: m.options.OutputLimit,
	})
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

func dockerNotFound(result runtimeprocess.CommandResult) bool {
	message := strings.ToLower(string(append(append([]byte(nil), result.Stdout...), result.Stderr...)))
	return strings.Contains(message, "not found") || strings.Contains(message, "no such")
}

func provisionError(ctx context.Context, result runtimeprocess.CommandResult, err error) error {
	if code := commandErrorCode(ctx, result, err); code != "" {
		return &BuilderError{Code: code}
	}
	return &BuilderError{Code: BuilderProvisionFailed}
}

func bootstrapError(ctx context.Context, result runtimeprocess.CommandResult, err error) error {
	if code := commandErrorCode(ctx, result, err); code != "" {
		return &BuilderError{Code: code}
	}
	return &BuilderError{Code: BuilderBootstrapFailed}
}

func builderContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &BuilderError{Code: BuilderCancelled}
	}
	return &BuilderError{Code: BuilderTimedOut}
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

const buildkitdConfiguration = `# Controller-owned generated-image BuildKit policy.
[worker.oci]
  max-parallelism = 1
  gc = true
  reservedSpace = "256MB"
  maxUsedSpace = "1GB"
  minFreeSpace = "512MB"
`
