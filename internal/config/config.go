package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Mode            string
	ListenAddress   string
	DataRoot        string
	LogLevel        string
	DockerEndpoint  string
	FakeRuntime     bool
	CaddyManagement bool
}

func Defaults() Config {
	root, err := os.UserConfigDir()
	if err != nil {
		root = "."
	}
	return Config{Mode: "controller-agent", ListenAddress: "127.0.0.1:7345", DataRoot: filepath.Join(root, "hostd"), LogLevel: "info"}
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
	fs.BoolVar(&c.CaddyManagement, "caddy-management", false, "enable Caddy management")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if c.Mode != "controller-agent" && c.Mode != "agent" {
		return Config{}, errors.New("mode must be controller-agent or agent")
	}
	if c.ListenAddress == "" {
		return Config{}, errors.New("listen address is required")
	}
	if c.FakeRuntime && !safeFakeRuntimeRoot(c.DataRoot) {
		return Config{}, errors.New("fake runtime requires a resolved .hostd-dev root or an isolated hostd-* test root under the system temporary directory")
	}
	return c, nil
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
