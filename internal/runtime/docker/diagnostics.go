package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const defaultCommandTimeout = 3 * time.Second

type HostResources struct {
	MemoryTotalBytes     uint64 `json:"memoryTotalBytes"`
	MemoryAvailableBytes uint64 `json:"memoryAvailableBytes"`
	DiskTotalBytes       uint64 `json:"diskTotalBytes"`
	DiskAvailableBytes   uint64 `json:"diskAvailableBytes"`
}

type Diagnostics struct {
	DaemonRunning     bool          `json:"daemonRunning"`
	ClientAvailable   bool          `json:"clientAvailable"`
	EngineReady       bool          `json:"engineReady"`
	ComposeAvailable  bool          `json:"composeAvailable"`
	CaddyManaged      bool          `json:"caddyManaged"`
	OS                string        `json:"os"`
	Architecture      string        `json:"architecture"`
	DockerVersion     string        `json:"dockerVersion"`
	ComposeVersion    string        `json:"composeVersion"`
	DockerDetail      string        `json:"dockerDetail"`
	ComposeDetail     string        `json:"composeDetail"`
	StartupLimitation string        `json:"startupLimitation"`
	Resources         HostResources `json:"resources"`
}

type Command struct {
	Executable  string
	Arguments   []string
	Environment map[string]string
}

type Runner interface {
	LookPath(string) (string, error)
	Run(context.Context, Command) (string, error)
}

type Checker struct {
	Runner           Runner
	CommandTimeout   time.Duration
	DockerEndpoint   string
	ResourceRoot     string
	CollectResources func(string) (HostResources, error)
}

type execRunner struct{}

func (execRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
func (execRunner) Run(ctx context.Context, command Command) (string, error) {
	cmd := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Env = os.Environ()
	for key, value := range command.Environment {
		prefix := key + "="
		for index := len(cmd.Env) - 1; index >= 0; index-- {
			if strings.HasPrefix(strings.ToUpper(cmd.Env[index]), strings.ToUpper(prefix)) {
				cmd.Env = append(cmd.Env[:index], cmd.Env[index+1:]...)
			}
		}
		cmd.Env = append(cmd.Env, prefix+value)
	}
	output, err := cmd.Output()
	return strings.TrimSpace(string(output)), err
}

func NewChecker(endpoint, resourceRoot string) Checker {
	return Checker{Runner: execRunner{}, CommandTimeout: defaultCommandTimeout, DockerEndpoint: endpoint, ResourceRoot: resourceRoot, CollectResources: collectHostResources}
}

func Check(ctx context.Context, caddy bool, endpoint, resourceRoot string) Diagnostics {
	return NewChecker(endpoint, resourceRoot).Check(ctx, caddy)
}

func (c Checker) Check(ctx context.Context, caddy bool) Diagnostics {
	diagnostic := Diagnostics{DaemonRunning: true, OS: runtime.GOOS, Architecture: runtime.GOARCH, CaddyManaged: caddy}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = defaultCommandTimeout
	}
	if c.CollectResources == nil {
		c.CollectResources = collectHostResources
	}
	if resources, err := c.CollectResources(c.ResourceRoot); err == nil {
		diagnostic.Resources = resources
	}
	path, err := c.Runner.LookPath("docker")
	if err != nil {
		diagnostic.DockerDetail = "Docker CLI not found"
		diagnostic.ComposeDetail = "Docker Compose V2 is unavailable"
		return withStartupLimitation(diagnostic)
	}
	diagnostic.ClientAvailable = true
	environment := map[string]string{}
	if c.DockerEndpoint != "" {
		environment["DOCKER_HOST"] = c.DockerEndpoint
	}
	dockerVersion, err := c.run(ctx, Command{Executable: path, Arguments: []string{"version", "--format", "{{.Server.Version}}"}, Environment: environment})
	dockerVersion = strings.TrimSpace(dockerVersion)
	if err == nil && dockerVersion != "" {
		diagnostic.EngineReady = true
		diagnostic.DockerVersion = dockerVersion
		diagnostic.DockerDetail = dockerVersion
	} else if errors.Is(err, context.DeadlineExceeded) {
		diagnostic.DockerDetail = "Docker engine check timed out"
	} else {
		diagnostic.DockerDetail = "Docker engine is unavailable"
	}
	composeVersion, err := c.run(ctx, Command{Executable: path, Arguments: []string{"compose", "version", "--short"}, Environment: environment})
	composeVersion = strings.TrimSpace(composeVersion)
	if err == nil && composeVersion != "" {
		diagnostic.ComposeAvailable = true
		diagnostic.ComposeVersion = composeVersion
		diagnostic.ComposeDetail = composeVersion
	} else if errors.Is(err, context.DeadlineExceeded) {
		diagnostic.ComposeDetail = "Docker Compose V2 check timed out"
	} else {
		diagnostic.ComposeDetail = "Docker Compose V2 is unavailable"
	}
	return withStartupLimitation(diagnostic)
}

func (c Checker) run(parent context.Context, command Command) (string, error) {
	ctx, cancel := context.WithTimeout(parent, c.CommandTimeout)
	defer cancel()
	output, err := c.Runner.Run(ctx, command)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", context.DeadlineExceeded
	}
	return output, err
}

func withStartupLimitation(diagnostic Diagnostics) Diagnostics {
	if runtime.GOOS == "windows" {
		diagnostic.StartupLimitation = "Docker Desktop may require the hosting Windows user to sign in after restart."
	}
	return diagnostic
}
