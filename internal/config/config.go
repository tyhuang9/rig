package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	if !c.FakeRuntime && filepath.Base(c.DataRoot) == ".hostd-dev" {
		return Config{}, fmt.Errorf("fake runtime must be explicitly enabled for development data root")
	}
	return c, nil
}

func (c Config) EnsureDataRoot() error {
	for _, part := range []string{"", "logs", "runtime", "apps"} {
		if err := os.MkdirAll(filepath.Join(c.DataRoot, part), 0o700); err != nil {
			return err
		}
	}
	return nil
}
