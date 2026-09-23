package config

import (
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/controllerrelay"
)

type Config struct {
	Mode                        string
	ListenAddress               string
	DataRoot                    string
	LogLevel                    string
	DockerEndpoint              string
	FakeRuntime                 bool
	ComposeRuntime              bool
	ComposeConfigTimeout        time.Duration
	ComposeApplyTimeout         time.Duration
	ComposeWaitTimeout          time.Duration
	ReleaseWorkspacePerAppBytes int64
	ReleaseWorkspaceGlobalBytes int64
	CaddyManagement             bool
	GitHubClientID              string
	GitHubAppSlug               string
	ControllerRelay             bool
	RelayOrigin                 string
}

const (
	defaultReleaseWorkspacePerAppBytes = int64(1 << 30)
	defaultReleaseWorkspaceGlobalBytes = int64(8 << 30)
	minReleaseWorkspaceQuotaBytes      = int64(1 << 20)
	maxReleaseWorkspacePerAppBytes     = int64(1 << 40)
	maxReleaseWorkspaceGlobalBytes     = int64(16 << 40)
	officialGitHubClientID             = "Iv23liDUN8TZv2ZW9Hjn"
	officialGitHubAppSlug              = "rig-deployment-connector"
)

var (
	githubClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	githubAppSlugPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,98}[a-z0-9])?$`)
)

func Defaults() Config {
	root, err := os.UserConfigDir()
	if err != nil {
		root = "."
	}
	return Config{
		Mode:                        "controller-agent",
		ListenAddress:               "127.0.0.1:7345",
		DataRoot:                    filepath.Join(root, "hostd"),
		LogLevel:                    "info",
		ComposeConfigTimeout:        30 * time.Second,
		ComposeApplyTimeout:         15 * time.Minute,
		ComposeWaitTimeout:          2 * time.Minute,
		ReleaseWorkspacePerAppBytes: defaultReleaseWorkspacePerAppBytes,
		ReleaseWorkspaceGlobalBytes: defaultReleaseWorkspaceGlobalBytes,
		GitHubClientID:              officialGitHubClientID,
		GitHubAppSlug:               officialGitHubAppSlug,
	}
}

func FromFlags(args []string) (Config, error) {
	c := Defaults()
	fs := flag.NewFlagSet("hostd", flag.ContinueOnError)
	fs.StringVar(&c.Mode, "mode", c.Mode, "controller-agent or agent")
	fs.StringVar(&c.ListenAddress, "listen", c.ListenAddress, "listen address")
	fs.StringVar(&c.DataRoot, "data-root", c.DataRoot, "hostd data root")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug, info, warn, or error")
	fs.StringVar(&c.DockerEndpoint, "docker-endpoint", "", "Docker endpoint override")
	fs.BoolVar(&c.FakeRuntime, "fake-runtime", false, "enable fake runtime (development/test only)")
	fs.BoolVar(&c.ComposeRuntime, "compose-runtime", false, "enable Docker Compose deployments")
	fs.DurationVar(&c.ComposeConfigTimeout, "compose-config-timeout", c.ComposeConfigTimeout, "Docker Compose configuration timeout")
	fs.DurationVar(&c.ComposeApplyTimeout, "compose-apply-timeout", c.ComposeApplyTimeout, "Docker Compose apply timeout")
	fs.DurationVar(&c.ComposeWaitTimeout, "compose-wait-timeout", c.ComposeWaitTimeout, "Docker Compose health wait timeout")
	fs.Int64Var(&c.ReleaseWorkspacePerAppBytes, "release-workspace-per-app-bytes", c.ReleaseWorkspacePerAppBytes, "maximum retained release workspace bytes per application")
	fs.Int64Var(&c.ReleaseWorkspaceGlobalBytes, "release-workspace-global-bytes", c.ReleaseWorkspaceGlobalBytes, "maximum retained release workspace bytes across applications")
	fs.BoolVar(&c.CaddyManagement, "caddy-management", false, "enable Caddy management")
	githubConnections := true
	fs.BoolVar(&githubConnections, "github-connections", githubConnections, "enable GitHub connections")
	fs.StringVar(&c.GitHubClientID, "github-client-id", c.GitHubClientID, "public GitHub App client ID override")
	fs.StringVar(&c.GitHubAppSlug, "github-app-slug", c.GitHubAppSlug, "public GitHub App slug override")
	fs.BoolVar(&c.ControllerRelay, "controller-relay", false, "enable controller relay lifecycle")
	fs.StringVar(&c.RelayOrigin, "relay-origin", "", "canonical HTTPS controller relay origin")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	provided := make(map[string]bool)
	fs.Visit(func(flag *flag.Flag) {
		provided[flag.Name] = true
	})
	if c.Mode != "controller-agent" && c.Mode != "agent" {
		return Config{}, errors.New("mode must be controller-agent or agent")
	}
	if c.ControllerRelay != (c.RelayOrigin != "") {
		return Config{}, errors.New("controller-relay and relay-origin must be provided together")
	}
	if c.ControllerRelay {
		if c.Mode != "controller-agent" {
			return Config{}, errors.New("controller relay requires controller-agent mode")
		}
		if _, err := controllerrelay.ParseCanonicalHTTPSOrigin(c.RelayOrigin); err != nil {
			return Config{}, errors.New("relay origin is invalid")
		}
	}
	if c.ListenAddress == "" {
		return Config{}, errors.New("listen address is required")
	}
	if err := validateLoopbackListenAddress(c.ListenAddress); err != nil {
		return Config{}, err
	}
	if c.FakeRuntime && c.ComposeRuntime {
		return Config{}, errors.New("fake-runtime and compose-runtime are mutually exclusive")
	}
	if c.FakeRuntime && !safeFakeRuntimeRoot(c.DataRoot) {
		return Config{}, errors.New("fake runtime requires a resolved .hostd-dev root or an isolated hostd-* test root under the system temporary directory")
	}
	if c.ComposeRuntime && !localDockerEndpoint(c.DockerEndpoint) {
		return Config{}, errors.New("compose runtime requires a local Docker endpoint")
	}
	if c.ComposeConfigTimeout < time.Second || c.ComposeConfigTimeout > 5*time.Minute ||
		c.ComposeApplyTimeout < time.Second || c.ComposeApplyTimeout > 2*time.Hour ||
		c.ComposeWaitTimeout < time.Second || c.ComposeWaitTimeout > time.Hour ||
		c.ComposeApplyTimeout <= c.ComposeWaitTimeout {
		return Config{}, errors.New("compose runtime timeouts are outside supported bounds")
	}
	if c.ReleaseWorkspacePerAppBytes < minReleaseWorkspaceQuotaBytes || c.ReleaseWorkspacePerAppBytes > maxReleaseWorkspacePerAppBytes ||
		c.ReleaseWorkspaceGlobalBytes < minReleaseWorkspaceQuotaBytes || c.ReleaseWorkspaceGlobalBytes > maxReleaseWorkspaceGlobalBytes ||
		c.ReleaseWorkspacePerAppBytes > c.ReleaseWorkspaceGlobalBytes {
		return Config{}, errors.New("release workspace quotas are outside supported bounds")
	}
	clientOverride := provided["github-client-id"]
	slugOverride := provided["github-app-slug"]
	if !githubConnections {
		if clientOverride || slugOverride {
			return Config{}, errors.New("github-connections=false cannot be combined with GitHub App overrides")
		}
		if c.ControllerRelay {
			return Config{}, errors.New("controller relay requires GitHub connections")
		}
		c.GitHubClientID = ""
		c.GitHubAppSlug = ""
		return c, nil
	}
	if clientOverride != slugOverride {
		return Config{}, errors.New("github-client-id and github-app-slug overrides must be provided together")
	}
	if !githubClientIDPattern.MatchString(c.GitHubClientID) || !githubAppSlugPattern.MatchString(c.GitHubAppSlug) {
		return Config{}, errors.New("GitHub App client ID or slug is invalid")
	}
	return c, nil
}

func localDockerEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}
	if strings.TrimSpace(endpoint) != endpoint || strings.ContainsAny(endpoint, "?#\x00") {
		return false
	}
	if strings.HasPrefix(endpoint, "unix:///") {
		return len(strings.TrimPrefix(endpoint, "unix://")) > 1
	}
	if strings.HasPrefix(endpoint, "npipe:////./pipe/") {
		return len(strings.TrimPrefix(endpoint, "npipe:////./pipe/")) > 0
	}
	return false
}

func (c Config) GitHubConnectionsEnabled() bool {
	return c.GitHubClientID != "" && c.GitHubAppSlug != ""
}

func validateLoopbackListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return errors.New("listen address must use an explicit loopback host and numeric port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listen address must use an explicit loopback host and numeric port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must use an explicit loopback host and numeric port")
	}
	return nil
}

func safeFakeRuntimeRoot(root string) bool {
	resolved, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if evaluated, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = evaluated
	} else if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(resolved)); parentErr == nil {
		resolved = filepath.Join(parent, filepath.Base(resolved))
	}
	if filepath.Base(resolved) == ".hostd-dev" {
		return true
	}
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		temp, err = filepath.Abs(os.TempDir())
		if err != nil {
			return false
		}
	}
	relative, err := filepath.Rel(temp, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return strings.HasPrefix(filepath.Base(resolved), "hostd-")
}

func (c Config) EnsureDataRoot() error {
	for _, part := range []string{"", "logs", "runtime", "apps"} {
		if err := os.MkdirAll(filepath.Join(c.DataRoot, part), 0o700); err != nil {
			return err
		}
	}
	return nil
}
