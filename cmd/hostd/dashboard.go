package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

type dashboardCommandRunner func(context.Context, string, ...string) error

const dashboardCommandTimeout = 10 * time.Second

func openDashboard(ctx context.Context, target string) error {
	return openDashboardWith(ctx, target, runtime.GOOS, runDashboardCommand)
}

func runDashboardCommand(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func openDashboardWith(ctx context.Context, target, goos string, run dashboardCommandRunner) error {
	if ctx == nil || run == nil {
		return errors.New("dashboard opener is not configured")
	}
	u, err := url.ParseRequestURI(target)
	if err != nil || !u.IsAbs() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("dashboard URL must be an absolute loopback HTTP(S) origin")
	}
	ip := net.ParseIP(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 {
		return errors.New("dashboard URL must be a loopback origin with an explicit port")
	}
	origin := u.Scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
	commandCtx, cancel := context.WithTimeout(ctx, dashboardCommandTimeout)
	defer cancel()
	switch goos {
	case "windows":
		return run(commandCtx, "rundll32", "url.dll,FileProtocolHandler", origin)
	case "darwin":
		return run(commandCtx, "open", origin)
	case "linux":
		return run(commandCtx, "xdg-open", origin)
	default:
		return fmt.Errorf("opening the dashboard is unsupported on %s", goos)
	}
}
