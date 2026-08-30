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
)

type dashboardCommandRunner func(context.Context, string, ...string) error

func openDashboard(ctx context.Context, target string) error {
	return openDashboardWith(ctx, target, runtime.GOOS, func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Start()
	})
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
	switch goos {
	case "windows":
		return run(ctx, "rundll32", "url.dll,FileProtocolHandler", origin)
	case "darwin":
		return run(ctx, "open", origin)
	case "linux":
		return run(ctx, "xdg-open", origin)
	default:
		return fmt.Errorf("opening the dashboard is unsupported on %s", goos)
	}
}
