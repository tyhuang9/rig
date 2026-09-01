package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	caddyNetworkName   = "rig-generated-caddy-ingress-v1"
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
	runner                   runtimeprocess.CommandRunner
	store                    *stateStore
	options                  Options
	dockerEnv                []string
	workingDirectoryIdentity os.FileInfo
	mu                       sync.Mutex
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
	workingDirectoryIdentity, err := os.Lstat(options.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	return &Manager{runner: runner, store: store, options: options, dockerEnv: dockerEnv, workingDirectoryIdentity: workingDirectoryIdentity}, nil
}

// Switch atomically reloads the aggregate Caddy route set, durably records the
// new active route, and installs it as Caddy's restart configuration. It never
// stops application containers or waits for connection draining; the
// deployment coordinator owns those post-commit operations.
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
	if hadPrevious && sameRoute(previous, proposed) {
		if err := m.verifyEndpoints(ctx, request.AppID, proposed); err != nil {
			return markCandidateMayBeLive(err)
		}
		if err := m.ensureCaddy(ctx, state.Active); err != nil {
			return markCandidateMayBeLive(err)
		}
		if err := m.reconcileCaddyNetworks(ctx, state.Active); err != nil {
			return markCandidateMayBeLive(err)
		}
		if err := m.applyRoutes(ctx, state.Active, "reconcile.json"); err != nil {
			return markCandidateMayBeLive(err)
		}
		if err := m.installRestartConfig(context.WithoutCancel(ctx)); err != nil {
			return markCandidateMayBeLive(err)
		}
		return nil
	}
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
	updated := cloneRoutes(state.Active)
	updated[request.AppID] = proposed
	state.Pending = &pendingRoute{AppID: request.AppID, Proposed: proposed}
	if hadPrevious {
		copy := previous
		copy.Endpoints = append([]generatedruntime.RouteEndpoint(nil), previous.Endpoints...)
		state.Pending.Previous = &copy
	}
	if err := m.store.save(state); err != nil {
		return &Error{Code: DiagnosticRouteStateFailed}
	}
	if err := m.reconcileCaddyNetworks(ctx, updated); err != nil {
		return m.rollbackAfterFailure(err, &state)
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
		rollback := routeState{Version: stateVersion, Active: committedRoutes}
		return m.rollbackAfterFailure(&Error{Code: DiagnosticRouteUnresolved}, &rollback)
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
	if err := m.reconcileCaddyNetworks(ctx, state.Active); err != nil {
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
		return candidateMayBeLiveError()
	}
	if err := m.reconcileCaddyNetworks(rollbackCtx, state.Active); err != nil {
		return candidateMayBeLiveError()
	}
	if err := m.applyRoutes(rollbackCtx, state.Active, "rollback.json"); err != nil {
		return candidateMayBeLiveError()
	}
	if err := m.installRestartConfig(rollbackCtx); err != nil {
		return candidateMayBeLiveError()
	}
	state.Pending = nil
	if err := m.store.save(*state); err != nil {
		return candidateMayBeLiveError()
	}
	return nil
}

func (m *Manager) rollbackAfterFailure(original error, state *routeState) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), m.options.CommandTimeout*3)
	defer cancel()
	if err := m.reconcileCaddyNetworks(rollbackCtx, state.Active); err != nil {
		return candidateMayBeLiveError()
	}
	if err := m.applyRoutes(rollbackCtx, state.Active, "rollback.json"); err != nil {
		return candidateMayBeLiveError()
	}
	if err := m.installRestartConfig(rollbackCtx); err != nil {
		return candidateMayBeLiveError()
	}
	state.Pending = nil
	if err := m.store.save(*state); err != nil {
		return candidateMayBeLiveError()
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
	ingressIP, err := m.ensureIngressNetwork(ctx)
	if err != nil {
		return err
	}
	inspection, found, err := m.inspectCaddy(ctx)
	if err != nil {
		return err
	}
	if !found {
		if err := m.createCaddy(ctx, imageID, ingressIP); err != nil {
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
		if err := m.disconnectApplicationNetworks(ctx, inspection); err != nil {
			return err
		}
		bootConfig, buildErr := buildCaddyConfig(map[string]routeRecord{}, "0.0.0.0:8080")
		if buildErr != nil || m.copyConfig(ctx, bootConfig, "active.json") != nil {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
		startErr := m.runDiscard(ctx, m.options.CommandTimeout, "container", "start", caddyContainerName)
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout)
		inspection, found, err = m.inspectCaddy(reconcileCtx)
		cancel()
		if err != nil || !found || !inspection.Running || !validCaddyInspection(inspection, imageID, m.options.HostPort) {
			if startErr != nil {
				return startErr
			}
			return &Error{Code: DiagnosticIngressUnavailable}
		}
	}
	if err := m.applyRoutes(ctx, routes, "recovery.json"); err != nil {
		return err
	}
	if err := m.installRestartConfig(context.WithoutCancel(ctx)); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved}
	}
	if err := m.reconcileCaddyNetworks(ctx, routes); err != nil {
		return err
	}
	return nil
}

func (m *Manager) ensureImage(ctx context.Context) (string, error) {
	inspection, found, err := m.inspectImage(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		pullErr := m.runDiscard(ctx, m.options.PullTimeout, "image", "pull", caddyImage)
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout)
		inspection, found, err = m.inspectImage(reconcileCtx)
		cancel()
		if err != nil || !found {
			if pullErr != nil {
				return "", pullErr
			}
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
		createErr := m.runDiscard(ctx, m.options.CommandTimeout, "volume", "create", "--driver", "local", "--label", "io.rig.managed=generated-ingress", "--label", "io.rig.identity-version=v1", caddyVolumeName)
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout)
		inspection, found, err = m.inspectVolume(reconcileCtx)
		cancel()
		if (err != nil || !found) && createErr != nil {
			return createErr
		}
	}
	if err != nil || !found || inspection.Name != caddyVolumeName || inspection.Driver != "local" || inspection.Scope != "local" || len(inspection.Options) != 0 || inspection.Labels["io.rig.managed"] != "generated-ingress" || inspection.Labels["io.rig.identity-version"] != "v1" {
		return &Error{Code: DiagnosticIngressDrift}
	}
	return nil
}

func (m *Manager) ensureIngressNetwork(ctx context.Context) (string, error) {
	inspection, found, err := m.inspectNetwork(ctx, caddyNetworkName)
	if err != nil {
		return "", err
	}
	if !found {
		attemptCtx, cancelAttempts := context.WithTimeout(ctx, m.options.CommandTimeout)
		defer cancelAttempts()
		for index := 0; index < 64 && !found; index++ {
			subnet, gateway, _ := ingressNetworkCandidate(index)
			createErr := m.runDiscard(attemptCtx, m.options.CommandTimeout, "network", "create", "--driver", "bridge", "--subnet", subnet, "--gateway", gateway,
				"--label", "io.rig.managed=generated-ingress-network", "--label", "io.rig.identity-version=v1", caddyNetworkName)
			inspection, found, err = m.inspectNetwork(attemptCtx, caddyNetworkName)
			if err != nil {
				return "", err
			}
			if !found && attemptCtx.Err() != nil {
				if createErr != nil {
					return "", createErr
				}
				return "", &Error{Code: DiagnosticIngressUnavailable}
			}
		}
	}
	ip, valid := ingressNetworkIdentity(inspection)
	if !found {
		return "", &Error{Code: DiagnosticIngressUnavailable}
	}
	if !valid {
		return "", &Error{Code: DiagnosticIngressDrift}
	}
	return ip, nil
}

func (m *Manager) createCaddy(ctx context.Context, imageID, ingressIP string) error {
	if net.ParseIP(ingressIP) == nil {
		return &Error{Code: DiagnosticIngressDrift}
	}
	args := []string{"container", "create", "--name", caddyContainerName, "--hostname", caddyContainerName, "--network", caddyNetworkName,
		"--ip", ingressIP,
		"--mount", "type=volume,src=" + caddyVolumeName + ",dst=/config", "--user", "1000:1000", "--read-only",
		"--tmpfs", "/data:rw,noexec,nosuid,nodev,size=67108864", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "1.000", "--pids-limit", "128", "--ulimit", "nofile=1024:1024",
		"--publish", "127.0.0.1:" + strconv.FormatUint(uint64(m.options.HostPort), 10) + ":8080/tcp", "--restart", "unless-stopped",
		"--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3",
		"--env", "XDG_CONFIG_HOME=/config", "--env", "XDG_DATA_HOME=/data",
		"--label", "io.rig.managed=generated-ingress", "--label", "io.rig.identity-version=v1", "--label", "io.rig.listener-isolation=v1",
		imageID, "run", "--config", "/config/active.json"}
	createErr := m.runDiscard(ctx, m.options.CommandTimeout, args...)
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.CommandTimeout)
	defer cancel()
	inspection, found, inspectErr := m.inspectCaddy(reconcileCtx)
	if inspectErr != nil || !found || !validCaddyInspection(inspection, imageID, m.options.HostPort) {
		if createErr != nil {
			return createErr
		}
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	config, err := buildCaddyConfig(map[string]routeRecord{}, "0.0.0.0:8080")
	if err != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if err := m.copyConfig(reconcileCtx, config, "active.json"); err != nil {
		return err
	}
	startErr := m.runDiscard(reconcileCtx, m.options.CommandTimeout, "container", "start", caddyContainerName)
	started, startedFound, inspectStartedErr := m.inspectCaddy(reconcileCtx)
	if inspectStartedErr != nil || !startedFound || !started.Running || !validCaddyInspection(started, imageID, m.options.HostPort) {
		if startErr != nil {
			return startErr
		}
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	return nil
}

func (m *Manager) applyRoutes(ctx context.Context, routes map[string]routeRecord, filename string) error {
	listenAddress, err := m.caddyListenAddress(ctx)
	if err != nil {
		return err
	}
	config, err := buildCaddyConfig(routes, listenAddress)
	if err != nil {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if err := m.copyConfig(ctx, config, filename); err != nil {
		return err
	}
	containerPath := "/config/" + filename
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "caddy", "validate", "--config", containerPath); err != nil {
		return &Error{Code: DiagnosticRouteValidateFailed}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", caddyContainerName, "caddy", "reload", "--config", containerPath); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", "--user", "0:0", caddyContainerName, "cp", containerPath, "/config/current.json"); err != nil {
		return &Error{Code: DiagnosticRouteReloadFailed}
	}
	return nil
}

func (m *Manager) installRestartConfig(ctx context.Context) error {
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", "--user", "0:0", caddyContainerName, "cp", "/config/current.json", "/config/active.next.json"); err != nil {
		return err
	}
	return m.runDiscard(ctx, m.options.CommandTimeout, "container", "exec", "--user", "0:0", caddyContainerName, "mv", "/config/active.next.json", "/config/active.json")
}

func (m *Manager) copyConfig(ctx context.Context, contents []byte, filename string) error {
	defer clear(contents)
	if len(contents) == 0 || !validConfigFilename(filename) {
		return &Error{Code: DiagnosticRouteInvalid}
	}
	if !m.validWorkingDirectory() {
		return &Error{Code: DiagnosticIngressDrift}
	}
	file, err := os.CreateTemp(m.options.WorkingDirectory, ".rig-caddy-*.json")
	if err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	if _, err := file.Write(contents); err != nil || file.Sync() != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	createdInfo, err := file.Stat()
	if err != nil || !createdInfo.Mode().IsRegular() || file.Close() != nil {
		file.Close()
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	fileInfo, err := os.Lstat(path)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 || generatedIngressPathIsReparsePoint(path) || !os.SameFile(createdInfo, fileInfo) || !m.validWorkingDirectory() {
		return &Error{Code: DiagnosticIngressDrift}
	}
	if err := m.runDiscard(ctx, m.options.CommandTimeout, "container", "cp", path, caddyContainerName+":/config/"+filename); err != nil {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	return nil
}

func (m *Manager) caddyListenAddress(ctx context.Context) (string, error) {
	inspection, found, err := m.inspectCaddy(ctx)
	if err != nil || !found || !inspection.Running {
		return "", &Error{Code: DiagnosticIngressUnavailable}
	}
	attachment := inspection.Networks[caddyNetworkName]
	network, networkFound, networkErr := m.inspectNetwork(ctx, caddyNetworkName)
	expectedIP, networkValid := ingressNetworkIdentity(network)
	if attachment == nil || !networkFound || networkErr != nil || !networkValid || attachment.IPAddress != expectedIP {
		return "", &Error{Code: DiagnosticIngressDrift}
	}
	return net.JoinHostPort(attachment.IPAddress, "8080"), nil
}

func (m *Manager) reconcileCaddyNetworks(ctx context.Context, routes map[string]routeRecord) error {
	desired, err := m.desiredNetworks(ctx, routes)
	if err != nil {
		return err
	}
	caddy, found, err := m.inspectCaddy(ctx)
	if err != nil || !found || !validContainerID(caddy.ID) {
		return &Error{Code: DiagnosticIngressUnavailable}
	}
	expectedID := normalizeID(caddy.ID)
	for network := range caddy.Networks {
		if _, keep := desired[network]; keep {
			continue
		}
		inspection, exists, inspectErr := m.inspectNetwork(ctx, network)
		if inspectErr != nil || !exists || !validApplicationNetwork(inspection, inspection.Labels["io.rig.application"]) {
			return &Error{Code: DiagnosticIngressDrift}
		}
		if err := m.runDiscard(ctx, m.options.CommandTimeout, "network", "disconnect", network, caddy.ID); err != nil {
			refreshed, ok, _ := m.inspectCaddy(context.WithoutCancel(ctx))
			if !ok || normalizeID(refreshed.ID) != expectedID {
				return &Error{Code: DiagnosticIngressUnavailable}
			}
			if _, attached := refreshed.Networks[network]; attached {
				return &Error{Code: DiagnosticIngressUnavailable}
			}
		}
	}
	for network := range desired {
		if network == caddyNetworkName {
			continue
		}
		refreshed, ok, inspectErr := m.inspectCaddy(ctx)
		if inspectErr != nil || !ok || normalizeID(refreshed.ID) != expectedID {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
		if _, attached := refreshed.Networks[network]; attached {
			continue
		}
		if err := m.runDiscard(ctx, m.options.CommandTimeout, "network", "connect", network, caddy.ID); err != nil {
			refreshed, ok, _ = m.inspectCaddy(context.WithoutCancel(ctx))
			if !ok || normalizeID(refreshed.ID) != expectedID {
				return &Error{Code: DiagnosticIngressUnavailable}
			}
			if _, attached := refreshed.Networks[network]; !attached {
				return &Error{Code: DiagnosticIngressUnavailable}
			}
		}
	}
	final, found, err := m.inspectCaddy(ctx)
	if err != nil || !found || normalizeID(final.ID) != expectedID || len(final.Networks) != len(desired) {
		return &Error{Code: DiagnosticIngressDrift}
	}
	for network := range desired {
		attachment := final.Networks[network]
		if attachment == nil {
			return &Error{Code: DiagnosticIngressDrift}
		}
		if network == caddyNetworkName {
			ingress, ok, inspectErr := m.inspectNetwork(ctx, caddyNetworkName)
			expectedIP, valid := ingressNetworkIdentity(ingress)
			if inspectErr != nil || !ok || !valid || attachment.IPAddress != expectedIP {
				return &Error{Code: DiagnosticIngressDrift}
			}
		}
	}
	return nil
}

func (m *Manager) desiredNetworks(ctx context.Context, routes map[string]routeRecord) (map[string]string, error) {
	desired := map[string]string{caddyNetworkName: ""}
	for appID, route := range routes {
		if validateRoute(route) != nil {
			return nil, &Error{Code: DiagnosticRouteInvalid}
		}
		network := route.Endpoints[0].NetworkName
		if owner, exists := desired[network]; exists && owner != appID {
			return nil, &Error{Code: DiagnosticIngressDrift}
		}
		inspection, found, err := m.inspectNetwork(ctx, network)
		if err != nil || !found || !validApplicationNetwork(inspection, appID) {
			return nil, &Error{Code: DiagnosticIngressDrift}
		}
		desired[network] = appID
	}
	return desired, nil
}

func (m *Manager) disconnectApplicationNetworks(ctx context.Context, caddy caddyInspection) error {
	if !validContainerID(caddy.ID) {
		return &Error{Code: DiagnosticIngressDrift}
	}
	for network := range caddy.Networks {
		if network == caddyNetworkName {
			continue
		}
		inspection, found, err := m.inspectNetwork(ctx, network)
		if err != nil || !found || !validApplicationNetwork(inspection, inspection.Labels["io.rig.application"]) {
			return &Error{Code: DiagnosticIngressDrift}
		}
		if err := m.runDiscard(ctx, m.options.CommandTimeout, "network", "disconnect", network, caddy.ID); err != nil {
			return &Error{Code: DiagnosticIngressUnavailable}
		}
	}
	return nil
}

func validIngressNetwork(value networkInspection) bool {
	_, valid := ingressNetworkIdentity(value)
	return valid
}

func ingressNetworkIdentity(value networkInspection) (string, bool) {
	if value.Name != caddyNetworkName || value.Driver != "bridge" || value.Scope != "local" || value.Internal || len(value.Options) != 0 ||
		value.Labels["io.rig.managed"] != "generated-ingress-network" || value.Labels["io.rig.identity-version"] != "v1" || len(value.IPAM) != 1 {
		return "", false
	}
	for index := 0; index < 64; index++ {
		subnet, gateway, address := ingressNetworkCandidate(index)
		if value.IPAM[0].Subnet == subnet && value.IPAM[0].Gateway == gateway {
			return address, true
		}
	}
	return "", false
}

func ingressNetworkCandidate(index int) (subnet, gateway, address string) {
	if index < 0 || index >= 64 {
		return "", "", ""
	}
	base := netip.AddrFrom4([4]byte{10, 203, byte(index / 16), byte(index%16) * 16})
	prefix := netip.PrefixFrom(base, 28)
	return prefix.String(), base.Next().String(), base.Next().Next().String()
}

func validApplicationNetwork(value networkInspection, appID string) bool {
	return validAppID(appID) && value.Name != caddyNetworkName && value.Driver == "bridge" && value.Scope == "local" && !value.Internal &&
		value.Labels["io.rig.managed"] == generatedruntime.NetworkOwnershipLabelValue && value.Labels["io.rig.application"] == appID
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
			inspection.Labels["io.rig.component"] != endpoint.Component || inspection.Labels["io.rig.slot"] != string(route.Slot) || inspection.Labels["io.rig.role"] != endpoint.Role ||
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

func (m *Manager) runDiscard(ctx context.Context, timeout time.Duration, args ...string) error {
	result, err := m.run(ctx, timeout, args...)
	clearResult(&result)
	return err
}

type imageInspection struct {
	ID          string   `json:"id"`
	OS          string   `json:"os"`
	RepoDigests []string `json:"repoDigests"`
}

type volumeInspection struct {
	Name    string            `json:"name"`
	Driver  string            `json:"driver"`
	Scope   string            `json:"scope"`
	Options map[string]string `json:"options"`
	Labels  map[string]string `json:"labels"`
}

type caddyInspection struct {
	ID           string                         `json:"id"`
	Name         string                         `json:"name"`
	Image        string                         `json:"image"`
	Labels       map[string]string              `json:"labels"`
	Hostname     string                         `json:"hostname"`
	User         string                         `json:"user"`
	Env          []string                       `json:"env"`
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
	NetworkMode  string                         `json:"networkMode"`
	Ulimits      []ulimitInspection             `json:"ulimits"`
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
	Name     string            `json:"name"`
	Driver   string            `json:"driver"`
	Scope    string            `json:"scope"`
	Internal bool              `json:"internal"`
	Options  map[string]string `json:"options"`
	IPAM     []networkIPAM     `json:"ipam"`
	Labels   map[string]string `json:"labels"`
}

type networkIPAM struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway"`
}

type endpointInspection struct {
	ID       string                        `json:"id"`
	Labels   map[string]string             `json:"labels"`
	Running  bool                          `json:"running"`
	Health   string                        `json:"health"`
	Networks map[string]*networkAttachment `json:"networks"`
}

type networkAttachment struct {
	Aliases   []string `json:"Aliases"`
	IPAddress string   `json:"IPAddress"`
}

type ulimitInspection struct {
	Name string `json:"Name"`
	Hard int64  `json:"Hard"`
	Soft int64  `json:"Soft"`
}

const (
	imageInspectFormat    = `{"id":{{json .ID}},"os":{{json .Os}},"repoDigests":{{json .RepoDigests}}}`
	volumeInspectFormat   = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}},"labels":{{json .Labels}}}`
	caddyInspectFormat    = `{"id":{{json .ID}},"name":{{json .Name}},"image":{{json .Image}},"labels":{{json .Config.Labels}},"hostname":{{json .Config.Hostname}},"user":{{json .Config.User}},"env":{{json .Config.Env}},"cmd":{{json .Config.Cmd}},"readOnly":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"capAdd":{{json .HostConfig.CapAdd}},"capDrop":{{json .HostConfig.CapDrop}},"securityOpt":{{json .HostConfig.SecurityOpt}},"binds":{{json .HostConfig.Binds}},"mounts":{{json .Mounts}},"tmpfs":{{json .HostConfig.Tmpfs}},"memory":{{json .HostConfig.Memory}},"memorySwap":{{json .HostConfig.MemorySwap}},"nanoCpus":{{json .HostConfig.NanoCPUs}},"pidsLimit":{{json .HostConfig.PidsLimit}},"logType":{{json .HostConfig.LogConfig.Type}},"logConfig":{{json .HostConfig.LogConfig.Config}},"restart":{{json .HostConfig.RestartPolicy.Name}},"networkMode":{{json .HostConfig.NetworkMode}},"ulimits":{{json .HostConfig.Ulimits}},"running":{{json .State.Running}},"portBindings":{{json .HostConfig.PortBindings}},"networks":{{if .NetworkSettings}}{{json .NetworkSettings.Networks}}{{else}}null{{end}}}`
	networkInspectFormat  = `{"name":{{json .Name}},"driver":{{json .Driver}},"scope":{{json .Scope}},"internal":{{json .Internal}},"options":{{json .Options}},"ipam":{{json .IPAM.Config}},"labels":{{json .Labels}}}`
	endpointInspectFormat = `{"id":{{json .ID}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"networks":{{if .NetworkSettings}}{{json .NetworkSettings.Networks}}{{else}}null{{end}}}`
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
		value.Hostname != caddyContainerName || value.NetworkMode != caddyNetworkName || !containsString(value.Env, "XDG_CONFIG_HOME=/config") || !containsString(value.Env, "XDG_DATA_HOME=/data") ||
		!value.ReadOnly || value.Privileged || len(value.CapAdd) != 0 || !exactFoldSet(value.CapDrop, "ALL") || !exactStringSet(value.SecurityOpt, "no-new-privileges") ||
		len(value.Binds) != 0 || value.Memory != 268435456 || value.MemorySwap != 268435456 || value.NanoCPUs != 1_000_000_000 || value.PIDsLimit != 128 ||
		len(value.Tmpfs) != 1 || value.Tmpfs["/data"] != "rw,noexec,nosuid,nodev,size=67108864" ||
		value.LogType != "local" || value.LogConfig["max-size"] != "10m" || value.LogConfig["max-file"] != "3" || value.Restart != "unless-stopped" ||
		len(value.Cmd) != 3 || value.Cmd[0] != "run" || value.Cmd[1] != "--config" || value.Cmd[2] != "/config/active.json" ||
		value.Labels["io.rig.managed"] != "generated-ingress" || value.Labels["io.rig.identity-version"] != "v1" || value.Labels["io.rig.listener-isolation"] != "v1" ||
		len(value.Ulimits) != 1 || value.Ulimits[0] != (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}) {
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
	return mountOK && len(value.PortBindings) == 1 && len(binding) == 1 && len(binding[0]) == 2 && binding[0]["HostIp"] == "127.0.0.1" && binding[0]["HostPort"] == strconv.FormatUint(uint64(hostPort), 10)
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
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && !generatedIngressPathIsReparsePoint(value)
}

func (m *Manager) validWorkingDirectory() bool {
	if m == nil || m.workingDirectoryIdentity == nil || !validAbsoluteDirectory(m.options.WorkingDirectory) {
		return false
	}
	current, err := os.Lstat(m.options.WorkingDirectory)
	return err == nil && os.SameFile(m.workingDirectoryIdentity, current)
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
	return value == "active.json" || value == "proposed.json" || value == "recovery.json" || value == "reconcile.json" || value == "rollback.json"
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

func exactStringSet(values []string, expected string) bool {
	return len(values) == 1 && values[0] == expected
}

func exactFoldSet(values []string, expected string) bool {
	return len(values) == 1 && strings.EqualFold(values[0], expected)
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
