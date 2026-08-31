package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	caddyContainerName = "rig-generated-caddy-v1"
	caddyVolumeName    = "rig-generated-caddy-config-v1"
	caddyImage         = "caddy@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648"
	caddyImageDigest   = "sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648"
	defaultHostPort    = uint16(8080)
	defaultOutputLimit = 64 << 10
	defaultTimeout     = 30 * time.Second
	defaultPullTimeout = 5 * time.Minute
	maximumDrain       = 30 * time.Second
)

type Options struct {
	DockerExecutable      string
	DockerEndpoint        string
	DockerConfigDirectory string
	WorkingDirectory      string
	DataRoot              string
	HostPort              uint16
	CommandTimeout        time.Duration
	PullTimeout           time.Duration
	OutputLimit           int
}

type Manager struct {
	runner    runtimeprocess.CommandRunner
	store     *stateStore
	options   Options
	dockerEnv []string
	mu        sync.Mutex
}

func New(runner runtimeprocess.CommandRunner, options Options) (*Manager, error) {
	if runner == nil {
		return nil, errors.New("generated ingress runner is required")
	}
	if options.HostPort == 0 {
		options.HostPort = defaultHostPort
	}
	if options.CommandTimeout == 0 {
		options.CommandTimeout = defaultTimeout
	}
	if options.PullTimeout == 0 {
		options.PullTimeout = defaultPullTimeout
	}
	if options.OutputLimit == 0 {
		options.OutputLimit = defaultOutputLimit
	}
	if !validOptions(options) {
		return nil, errors.New("generated ingress options are invalid")
	}
	store, err := newStateStore(options.DataRoot)
	if err != nil {
		return nil, err
	}
	dockerEnv, err := dockerEnvironment(options.DockerEndpoint, options.DockerConfigDirectory)
	if err != nil {
		return nil, err
	}
	return &Manager{runner: runner, store: store, options: options, dockerEnv: dockerEnv}, nil
}

// Switch atomically reloads the aggregate Caddy route set, durably records the
// new active route, installs it as Caddy's restart configuration, and then
// waits for the requested bounded drain period. It never stops application
// containers; the deployment coordinator owns that final step.
func (m *Manager) Switch(ctx context.Context, request generatedruntime.RouteSwitchRequest) error {
	if m == nil || ctx == nil || !validSwitchRequest(request) {
		return &Error{Code: DiagnosticValidationFailed}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.store.load()
	if err != nil {
		return &Error{Code: DiagnosticRouteStateFailed}
	}
	if state.Pending != nil {
		if err := m.rollbackPending(ctx, &state); err != nil {
			return err
		}
	}
	previous, hadPrevious := state.Active[request.AppID]
	committedRoutes := cloneRoutes(state.Active)
	proposed := routeRecord{Slot: request.ToSlot, Endpoints: append([]generatedruntime.RouteEndpoint(nil), request.Endpoints...)}
	if request.FromSlot == "" {
		if hadPrevious {
			return &Error{Code: DiagnosticRouteInvalid}
		}
	} else if !hadPrevious || previous.Slot != request.FromSlot {
		return &Error{Code: DiagnosticRouteInvalid}
	}

	if err := m.ensureCaddy(ctx, state.Active); err != nil {
		return err
	}
	if err := m.verifyEndpoints(ctx, request.AppID, proposed); err != nil {
		return err
	}
	if err := m.connectAppNetwork(ctx, request.AppID, proposed.Endpoints[0].NetworkName); err != nil {
		return err
	}

	updated := cloneRoutes(state.Active)
	updated[request.AppID] = proposed
	if err := m.verifyCaddyNetworks(ctx, updated); err != nil {
		return err
	}
	state.Pending = &pendingRoute{AppID: request.AppID, Proposed: proposed}
	if hadPrevious {
		copy := previous
		copy.Endpoints = append([]generatedruntime.RouteEndpoint(nil), previous.Endpoints...)
		state.Pending.Previous = &copy
	}
	if err := m.store.save(state); err != nil {
		return &Error{Code: DiagnosticRouteStateFailed}
	}
	if err := m.applyRoutes(ctx, updated, "proposed.json"); err != nil {
		return m.rollbackAfterFailure(err, &state)
	}

	state.Active = updated
	state.Pending = nil
	if err := m.store.save(state); err != nil {
		rollback := routeState{Version: stateVersion, Active: committedRoutes}
		return m.rollbackAfterFailure(&Error{Code: DiagnosticRouteStateFailed}, &rollback)
	}
	if err := m.installRestartConfig(context.WithoutCancel(ctx)); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	if request.DrainPeriod > 0 {
		timer := time.NewTimer(request.DrainPeriod)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return &Error{Code: DiagnosticCancelled}
		}
	}
	return nil
}

// Recover rolls back an uncertain pending switch to the last committed route,
// then reapplies the committed aggregate config and restart file.
func (m *Manager) Recover(ctx context.Context) error {
	return m.Provision(ctx)
}

// Provision creates or verifies the pinned Caddy boundary and reapplies its
// committed routes. Startup calls it before generated deployment workers so
// capacity checks can query the Docker VM through this exact container.
func (m *Manager) Provision(ctx context.Context) error {
	if m == nil || ctx == nil {
		return &Error{Code: DiagnosticValidationFailed}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.store.load()
	if err != nil {
		return &Error{Code: DiagnosticRouteStateFailed}
	}
	if state.Pending != nil {
		if err := m.rollbackPending(ctx, &state); err != nil {
			return err
		}
	}
	if err := m.ensureCaddy(ctx, state.Active); err != nil {
		return err
	}
	for appID, route := range state.Active {
		if err := m.connectAppNetwork(ctx, appID, route.Endpoints[0].NetworkName); err != nil {
			return err
		}
	}
	if err := m.verifyCaddyNetworks(ctx, state.Active); err != nil {
		return err
	}
	if err := m.applyRoutes(ctx, state.Active, "recovery.json"); err != nil {
		return err
	}
	if err := m.installRestartConfig(context.WithoutCancel(ctx)); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}

func (m *Manager) rollbackPending(ctx context.Context, state *routeState) error {
	if state == nil || state.Pending == nil {
		return nil
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout*3)
	defer cancel()
	if err := m.ensureCaddy(rollbackCtx, state.Active); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	if err := m.applyRoutes(rollbackCtx, state.Active, "rollback.json"); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	state.Pending = nil
	if err := m.store.save(*state); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return nil
}

func (m *Manager) rollbackAfterFailure(original error, state *routeState) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), m.options.CommandTimeout*3)
	defer cancel()
	if err := m.applyRoutes(rollbackCtx, state.Active, "rollback.json"); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	state.Pending = nil
	if err := m.store.save(*state); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	return original
}

func (m *Manager) ensureCaddy(ctx context.Context, routes map[string]routeRecord) error {
	imageID, err := m.ensureImage(ctx)
	if err != nil {
		return err
	}
	if err := m.ensureVolume(ctx); err != nil {
		return err
	}
	inspection, found, err := m.inspectCaddy(ctx)
	if err != nil {
		return err
	}
	if !found {
		if err := m.createCaddy(ctx, imageID, routes); err != nil {
			return err
		}
		inspection, found, err = m.inspectCaddy(ctx)
		if err != nil || !found {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
	}
	if !validCaddyInspection(inspection, imageID, m.options.HostPort) {
		return &Error{Code: DiagnosticIngressDrift}
	}
	if !inspection.Running {
		if _, err := m.run(ctx, m.options.CommandTimeout, "container", "start", caddyContainerName); err != nil {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
		inspection, found, err = m.inspectCaddy(ctx)
		if err != nil || !found || !inspection.Running || !validCaddyInspection(inspection, imageID, m.options.HostPort) {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
	}
	return nil
}

func (m *Manager) ensureImage(ctx context.Context) (string, error) {
	inspection, found, err := m.inspectImage(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		if _, err := m.run(ctx, m.options.PullTimeout, "image", "pull", caddyImage); err != nil {
			return "", &Error{Code: DiagnosticIngressUnavailable}
		}
		inspection, found, err = m.inspectImage(ctx)
		if err != nil || !found {
			return "", &Error{Code: DiagnosticIngressUnavailable}
		}
	}
	if inspection.OS != "linux" || !validContainerID(inspection.ID) || !containsDigest(inspection.RepoDigests, caddyImageDigest) {
		return "", &Error{Code: DiagnosticIngressDrift}
	}
	return inspection.ID, nil
}

func (m *Manager) ensureVolume(ctx context.Context) error {
	inspection, found, err := m.inspectVolume(ctx)
	if err != nil {
		return err
	}
	if !found {
		if _, err := m.run(ctx, m.options.CommandTimeout, "volume", "create", "--driver", "local", "--label", "io.rig.managed=generated-ingress", "--label", "io.rig.identity-version=v1", caddyVolumeName); err != nil {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
		inspection, found, err = m.inspectVolume(ctx)
	}
	if err != nil || !found || inspection.Name != caddyVolumeName || inspection.Driver != "local" || inspection.Labels["io.rig.managed"] != "generated-ingress" || inspection.Labels["io.rig.identity-version"] != "v1" {
		return &Error{Code: DiagnosticIngressDrift}
	}
	return nil
}

func (m *Manager) createCaddy(ctx context.Context, imageID string, routes map[string]routeRecord) error {
	args := []string{"container", "create", "--name", caddyContainerName, "--hostname", caddyContainerName, "--network", "none",
		"--mount", "type=volume,src=" + caddyVolumeName + ",dst=/config", "--user", "1000:1000", "--read-only",
		"--tmpfs", "/data:rw,noexec,nosuid,nodev,size=67108864", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128", "--ulimit", "nofile=1024:1024",
		"--publish", "127.0.0.1:" + strconv.FormatUint(uint64(m.options.HostPort), 10) + ":8080/tcp", "--restart", "unless-stopped",
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
		"--label", "io.rig.managed=generated-ingress", "--label", "io.rig.identity-version=v1",
		imageID, "run", "--config", "/config/active.json"}
	_, createErr := m.run(ctx, m.options.CommandTimeout, args...)
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout)
	defer cancel()
	inspection, found, inspectErr := m.inspectCaddy(reconcileCtx)
	if inspectErr != nil || !found || !validCaddyInspection(inspection, imageID, m.options.HostPort) {
		if createErr != nil {
			return createErr
		}
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	config, err := buildCaddyConfig(routes)
	if err != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if err := m.copyConfig(reconcileCtx, config, "active.json"); err != nil {
		return err
	}
	if _, err := m.run(reconcileCtx, m.options.CommandTimeout, "container", "start", caddyContainerName); err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	return nil
}

func (m *Manager) applyRoutes(ctx context.Context, routes map[string]routeRecord, filename string) error {
	config, err := buildCaddyConfig(routes)
	if err != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if err := m.copyConfig(ctx, config, filename); err != nil {
		return err
	}
	containerPath := "/config/" + filename
	if _, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "caddy", "validate", "--config", containerPath); err != nil {
		return &Error{Code: DiagnosticRouteValidateFailed}
	}
	if _, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "caddy", "reload", "--config", containerPath); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	if _, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "cp", containerPath, "/config/current.json"); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	return nil
}

func (m *Manager) installRestartConfig(ctx context.Context) error {
	if _, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "cp", "/config/current.json", "/config/active.next.json"); err != nil {
		return err
	}
	_, err := m.run(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "mv", "/config/active.next.json", "/config/active.json")
	return err
}

func (m *Manager) copyConfig(ctx context.Context, contents []byte, filename string) error {
	defer clear(contents)
	if len(contents) == 0 || !validConfigFilename(filename) {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	file, err := os.CreateTemp(m.options.WorkingDirectory, ".rig-caddy-*.json")
	if err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	if _, err := file.Write(contents); err != nil || file.Sync() != nil || file.Close() != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	if _, err := m.run(ctx, m.options.CommandTimeout, "container", "cp", path, caddyContainerName+":/config/"+filename); err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	return nil
}

func (m *Manager) connectAppNetwork(ctx context.Context, appID, network string) error {
	if !validName(network, 96) {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	inspection, found, err := m.inspectNetwork(ctx, network)
	if err != nil || !found || inspection.Name != network || inspection.Driver != "bridge" || inspection.Scope != "local" ||
		inspection.Labels["io.rig.managed"] != "generated-runtime" || inspection.Labels["io.rig.application"] != appID {
		return &Error{Code: DiagnosticIngressDrift}
	}
	caddy, found, err := m.inspectCaddy(ctx)
	if err != nil || !found {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	if _, exists := caddy.Networks[network]; !exists {
		if _, err := m.run(ctx, m.options.CommandTimeout, "network", "connect", network, caddyContainerName); err != nil {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
		caddy, found, err = m.inspectCaddy(ctx)
		if err != nil || !found {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
	}
	if _, exists := caddy.Networks[network]; !exists {
		return &Error{Code: DiagnosticIngressDrift}
	}
	return nil
}

func (m *Manager) verifyCaddyNetworks(ctx context.Context, routes map[string]routeRecord) error {
	inspection, found, err := m.inspectCaddy(ctx)
	if err != nil || !found {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	allowed := map[string]bool{"none": true}
	for _, route := range routes {
		allowed[route.Endpoints[0].NetworkName] = true
	}
	for network := range inspection.Networks {
		if !allowed[network] {
			return &Error{Code: DiagnosticIngressDrift}
		}
	}
	return nil
}

func (m *Manager) verifyEndpoints(ctx context.Context, appID string, route routeRecord) error {
	for _, endpoint := range route.Endpoints {
		result, err := m.run(ctx, m.options.CommandTimeout, "container", "inspect", "--format", endpointInspectFormat, endpoint.ContainerID)
		if err != nil || result.StdoutTruncated || result.StderrTruncated {
			clearResult(&result)
			return &Error{Code: DiagnosticIngressDrift}
		}
		var inspection endpointInspection
		decodeErr := json.Unmarshal(result.Stdout, &inspection)
		clearResult(&result)
		attachment := inspection.Networks[endpoint.NetworkName]
		if decodeErr != nil || normalizeID(inspection.ID) != normalizeID(endpoint.ContainerID) || !inspection.Running || inspection.Health != "healthy" ||
			inspection.Labels["io.rig.managed"] != "generated-runtime" || inspection.Labels["io.rig.application"] != appID ||
			inspection.Labels["io.rig.component"] != endpoint.Component || inspection.Labels["io.rig.slot"] != string(route.Slot) ||
			attachment == nil || !containsString(attachment.Aliases, endpoint.NetworkAlias) {
			return &Error{Code: DiagnosticIngressDrift}
		}
	}
	return nil
}

func (m *Manager) run(ctx context.Context, timeout time.Duration, args ...string) (runtimeprocess.CommandResult, error) {
	result, err := m.runner.Run(ctx, runtimeprocess.CommandRequest{Executable: m.options.DockerExecutable, Args: append([]string(nil), args...), Directory: m.options.WorkingDirectory, Env: append([]string(nil), m.dockerEnv...), Timeout: timeout, OutputLimit: m.options.OutputLimit})
	if err != nil {
		code := DiagnosticIngressUnavailable
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = DiagnosticCancelled
		}
		clearResult(&result)
		return runtimeprocess.CommandResult{}, &Error{Code: code}
	}
	if result.StdoutTruncated || result.StderrTruncated {
		clearResult(&result)
		return runtimeprocess.CommandResult{}, &Error{Code: DiagnosticIngressUnavailable}
	}
	return result, nil
}

type imageInspection struct {
	ID          string   `json:"id"`
	OS          string   `json:"os"`
	RepoDigests []string `json:"repoDigests"`
}

type volumeInspection struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Labels map[string]string `json:"labels"`
}

type caddyInspection struct {
	ID           string                         `json:"id"`
	Name         string                         `json:"name"`
	Image        string                         `json:"image"`
	Labels       map[string]string              `json:"labels"`
	User         string                         `json:"user"`
	Cmd          []string                       `json:"cmd"`
	ReadOnly     bool                           `json:"readOnly"`
	Privileged   bool                           `json:"privileged"`
	CapAdd       []string                       `json:"capAdd"`
	CapDrop      []string                       `json:"capDrop"`
	SecurityOpt  []string                       `json:"securityOpt"`
	Binds        []string                       `json:"binds"`
	Mounts       []mountInspection              `json:"mounts"`
	Tmpfs        map[string]string              `json:"tmpfs"`
	Memory       int64                          `json:"memory"`
	MemorySwap   int64                          `json:"memorySwap"`
	NanoCPUs     int64                          `json:"nanoCpus"`
	PIDsLimit    int64                          `json:"pidsLimit"`
	LogType      string                         `json:"logType"`
	LogConfig    map[string]string              `json:"logConfig"`
	Restart      string                         `json:"restart"`
	Running      bool                           `json:"running"`
	PortBindings map[string][]map[string]string `json:"portBindings"`
	Networks     map[string]*networkAttachment  `json:"networks"`
}

type mountInspection struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type networkInspection struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Scope  string            `json:"scope"`
	Labels map[string]string `json:"labels"`
}

type endpointInspection struct {
	ID       string                        `json:"id"`
	Labels   map[string]string             `json:"labels"`
	Running  bool                          `json:"running"`
	Health   string                        `json:"health"`
	Networks map[string]*networkAttachment `json:"networks"`
}

type networkAttachment struct {
	Aliases []string `json:"Aliases"`
}

const (
	imageInspectFormat    = `{"id":{{json .Id}},"os":{{json .Os}},"repoDigests":{{json .RepoDigests}}}`
	volumeInspectFormat   = `{"name":{{json .Name}},"driver":{{json .Driver}},"labels":{{json .Labels}}}`
	caddyInspectFormat    = `{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},"labels":{{json .Config.Labels}},"user":{{json .Config.User}},"cmd":{{json .Config.Cmd}},"readOnly":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"capAdd":{{json .HostConfig.CapAdd}},"capDrop":{{json .HostConfig.CapDrop}},"securityOpt":{{json .HostConfig.SecurityOpt}},"binds":{{json .HostConfig.Binds}},"mounts":{{json .Mounts}},"tmpfs":{{json .HostConfig.Tmpfs}},"memory":{{json .HostConfig.Memory}},"memorySwap":{{json .HostConfig.MemorySwap}},"nanoCpus":{{json .HostConfig.NanoCpus}},"pidsLimit":{{json .HostConfig.PidsLimit}},"logType":{{json .HostConfig.LogConfig.Type}},"logConfig":{{json .HostConfig.LogConfig.Config}},"restart":{{json .HostConfig.RestartPolicy.Name}},"running":{{json .State.Running}},"portBindings":{{json .HostConfig.PortBindings}},"networks":{{json .NetworkSettings.Networks}}}`
	networkInspectFormat  = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"labels":{{json .Labels}}}`
	endpointInspectFormat = `{"id":{{json .Id}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"health":{{json .State.Health.Status}},"networks":{{json .NetworkSettings.Networks}}}`
)

func (m *Manager) inspectImage(ctx context.Context) (imageInspection, bool, error) {
	var value imageInspection
	found, err := m.inspectJSON(ctx, &value, "image", "inspect", "--format", imageInspectFormat, caddyImage)
	return value, found, err
}

func (m *Manager) inspectVolume(ctx context.Context) (volumeInspection, bool, error) {
	var value volumeInspection
	found, err := m.inspectJSON(ctx, &value, "volume", "inspect", "--format", volumeInspectFormat, caddyVolumeName)
	return value, found, err
}

func (m *Manager) inspectCaddy(ctx context.Context) (caddyInspection, bool, error) {
	var value caddyInspection
	found, err := m.inspectJSON(ctx, &value, "container", "inspect", "--format", caddyInspectFormat, caddyContainerName)
	return value, found, err
}

func (m *Manager) inspectNetwork(ctx context.Context, name string) (networkInspection, bool, error) {
	var value networkInspection
	found, err := m.inspectJSON(ctx, &value, "network", "inspect", "--format", networkInspectFormat, name)
	return value, found, err
}

func (m *Manager) inspectJSON(ctx context.Context, destination any, args ...string) (bool, error) {
	result, err := m.runner.Run(ctx, runtimeprocess.CommandRequest{Executable: m.options.DockerExecutable, Args: append([]string(nil), args...), Directory: m.options.WorkingDirectory, Env: append([]string(nil), m.dockerEnv...), Timeout: m.options.CommandTimeout, OutputLimit: m.options.OutputLimit})
	if err != nil {
		notFound := dockerNotFound(result)
		clearResult(&result)
		if notFound {
			return false, nil
		}
		return false, &Error{Code: DiagnosticIngressUnavailable}
	}
	if result.StdoutTruncated || result.StderrTruncated || json.Unmarshal(result.Stdout, destination) != nil {
		clearResult(&result)
		return false, &Error{Code: DiagnosticIngressDrift}
	}
	clearResult(&result)
	return true, nil
}

func validCaddyInspection(value caddyInspection, imageID string, hostPort uint16) bool {
	if normalizeID(value.Image) != normalizeID(imageID) || strings.TrimPrefix(value.Name, "/") != caddyContainerName || value.User != "1000:1000" ||
		!value.ReadOnly || value.Privileged || len(value.CapAdd) != 0 || !containsFold(value.CapDrop, "ALL") || !containsString(value.SecurityOpt, "no-new-privileges") ||
		len(value.Binds) != 0 || value.Memory != 268435456 || value.MemorySwap != 268435456 || value.NanoCPUs != 1_000_000_000 || value.PIDsLimit != 128 ||
		len(value.Tmpfs) != 1 || value.Tmpfs["/data"] != "rw,noexec,nosuid,nodev,size=67108864" ||
		value.LogType != "local" || value.LogConfig["max-size"] != "10m" || value.LogConfig["max-file"] != "3" || value.Restart != "unless-stopped" ||
		len(value.Cmd) != 3 || value.Cmd[0] != "run" || value.Cmd[1] != "--config" || value.Cmd[2] != "/config/active.json" ||
		value.Labels["io.rig.managed"] != "generated-ingress" || value.Labels["io.rig.identity-version"] != "v1" {
		return false
	}
	mountOK := false
	for _, mount := range value.Mounts {
		if mount.Type == "volume" && mount.Name == caddyVolumeName && mount.Destination == "/config" && mount.RW {
			mountOK = true
		} else if mount.Type != "tmpfs" || mount.Destination != "/data" {
			return false
		}
	}
	binding := value.PortBindings["8080/tcp"]
	return mountOK && len(binding) == 1 && binding[0]["HostIp"] == "127.0.0.1" && binding[0]["HostPort"] == strconv.FormatUint(uint64(hostPort), 10)
}

func validOptions(options Options) bool {
	return validAbsoluteDirectory(options.WorkingDirectory) && validAbsoluteDirectory(options.DockerConfigDirectory) &&
		filepath.IsAbs(options.DockerExecutable) && filepath.Clean(options.DockerExecutable) == options.DockerExecutable &&
		options.HostPort > 0 && options.CommandTimeout >= time.Second && options.CommandTimeout <= 5*time.Minute &&
		options.PullTimeout >= time.Second && options.PullTimeout <= 30*time.Minute && options.OutputLimit > 0 && options.OutputLimit <= runtimeprocess.DefaultOutputLimit &&
		localDockerEndpoint(options.DockerEndpoint)
}

func validSwitchRequest(request generatedruntime.RouteSwitchRequest) bool {
	if !validAppID(request.AppID) || request.ToSlot == "" || request.ToSlot == request.FromSlot || request.DrainPeriod < 0 || request.DrainPeriod > maximumDrain {
		return false
	}
	return validateRoute(routeRecord{Slot: request.ToSlot, Endpoints: request.Endpoints}) == nil
}

func validAbsoluteDirectory(value string) bool {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	info, err := os.Lstat(value)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func dockerEnvironment(endpoint, config string) ([]string, error) {
	if !validAbsoluteDirectory(config) {
		return nil, errors.New("generated ingress Docker configuration is unsafe")
	}
	entries, err := os.ReadDir(config)
	if err != nil || len(entries) != 0 {
		return nil, errors.New("generated ingress Docker configuration must be empty")
	}
	values := map[string]string{"DOCKER_CONFIG": config}
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

func localDockerEndpoint(value string) bool {
	if value == "" || value == "npipe:////./pipe/docker_engine" {
		return true
	}
	if !strings.HasPrefix(value, "unix://") {
		return false
	}
	path := strings.TrimPrefix(value, "unix://")
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func validConfigFilename(value string) bool {
	return value == "active.json" || value == "proposed.json" || value == "recovery.json" || value == "rollback.json"
}

func containsDigest(values []string, digest string) bool {
	for _, value := range values {
		if strings.HasSuffix(value, "@"+digest) {
			return true
		}
	}
	return false
}

func normalizeID(value string) string { return strings.TrimPrefix(value, "sha256:") }

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
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
	return strings.Contains(message, "no such image") || strings.Contains(message, "no such volume") || strings.Contains(message, "no such container") || strings.Contains(message, "no such network") || strings.Contains(message, "not found")
}

func (m *Manager) URL(appID string) (string, error) {
	if m == nil || !validAppID(appID) {
		return "", &Error{Code: DiagnosticValidationFailed}
	}
	return fmt.Sprintf("http://%s.rig.localhost:%d", appID, m.options.HostPort), nil
}
