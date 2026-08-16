package docker

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

type Diagnostics struct {
	DaemonRunning     bool   `json:"daemonRunning"`
	ClientAvailable   bool   `json:"clientAvailable"`
	EngineReady       bool   `json:"engineReady"`
	ComposeAvailable  bool   `json:"composeAvailable"`
	CaddyManaged      bool   `json:"caddyManaged"`
	OS                string `json:"os"`
	Architecture      string `json:"architecture"`
	DockerDetail      string `json:"dockerDetail"`
	ComposeDetail     string `json:"composeDetail"`
	StartupLimitation string `json:"startupLimitation"`
}

func Check(ctx context.Context, caddy bool) Diagnostics {
	d := Diagnostics{DaemonRunning: true, OS: runtime.GOOS, Architecture: runtime.GOARCH, CaddyManaged: caddy}
	path, err := exec.LookPath("docker")
	if err != nil {
		d.DockerDetail = "Docker CLI not found"
	} else {
		d.ClientAvailable = true
		cmd := exec.CommandContext(ctx, path, "version", "--format", "{{.Server.Version}}")
		cmd.WaitDelay = 2 * time.Second
		if out, e := cmd.Output(); e == nil {
			d.EngineReady = true
			d.DockerDetail = string(out)
		} else {
			d.DockerDetail = "Docker engine is unavailable"
		}
		compose := exec.CommandContext(ctx, path, "compose", "version", "--short")
		if out, e := compose.Output(); e == nil {
			d.ComposeAvailable = true
			d.ComposeDetail = string(out)
		} else {
			d.ComposeDetail = "Docker Compose V2 is unavailable"
		}
	}
	if runtime.GOOS == "windows" {
		d.StartupLimitation = "Docker Desktop may require the hosting Windows user to sign in after restart."
	}
	return d
}
