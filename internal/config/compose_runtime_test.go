package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComposeRuntimeRequiresExplicitEnablement(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		compose bool
		fake    bool
	}{
		{name: "default is non-executing"},
		{name: "compose explicitly enabled", args: []string{"--compose-runtime"}, compose: true},
		{
			name: "fake explicitly enabled",
			args: []string{
				"--fake-runtime",
				"--data-root", filepath.Join(t.TempDir(), ".hostd-dev"),
			},
			fake: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := FromFlags(test.args)
			if err != nil {
				t.Fatalf("FromFlags() error = %v", err)
			}
			if configuration.ComposeRuntime != test.compose || configuration.FakeRuntime != test.fake {
				t.Fatalf(
					"runtime flags = (compose %v, fake %v), want (compose %v, fake %v)",
					configuration.ComposeRuntime,
					configuration.FakeRuntime,
					test.compose,
					test.fake,
				)
			}
		})
	}
}

func TestComposeAndFakeRuntimeAreMutuallyExclusiveBeforeRootCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created", ".hostd-dev")
	_, err := FromFlags([]string{
		"--compose-runtime",
		"--fake-runtime",
		"--data-root", root,
	})
	if err == nil {
		t.Fatal("FromFlags() succeeded with both runtime modes enabled")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("FromFlags() error = %q, want mutual-exclusion diagnostic", err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("data root was created before runtime-mode validation: stat error = %v", statErr)
	}
}

func TestComposeRuntimeRejectsRemoteDockerEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		ok       bool
	}{
		{name: "environment default", ok: true},
		{name: "local Unix socket", endpoint: "unix:///var/run/docker.sock", ok: true},
		{name: "local Windows named pipe", endpoint: "npipe:////./pipe/docker_engine", ok: true},
		{name: "TCP", endpoint: "tcp://127.0.0.1:2375"},
		{name: "HTTP", endpoint: "http://127.0.0.1:2375"},
		{name: "HTTPS", endpoint: "https://docker.example.test"},
		{name: "SSH", endpoint: "ssh://host.example.test"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--compose-runtime"}
			if test.endpoint != "" {
				args = append(args, "--docker-endpoint", test.endpoint)
			}
			configuration, err := FromFlags(args)
			if (err == nil) != test.ok {
				t.Fatalf("FromFlags() error = %v, want success %v", err, test.ok)
			}
			if err == nil && configuration.DockerEndpoint != test.endpoint {
				t.Fatalf("DockerEndpoint = %q, want %q", configuration.DockerEndpoint, test.endpoint)
			}
		})
	}
}

func TestComposeRuntimeTimeoutBounds(t *testing.T) {
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{name: "minimums", args: []string{"--compose-config-timeout", "1s", "--compose-apply-timeout", "2s", "--compose-wait-timeout", "1s"}, ok: true},
		{name: "maximums", args: []string{"--compose-config-timeout", "5m", "--compose-apply-timeout", "2h", "--compose-wait-timeout", "1h"}, ok: true},
		{name: "configuration below minimum", args: []string{"--compose-config-timeout", "999ms"}},
		{name: "configuration above maximum", args: []string{"--compose-config-timeout", "5m1s"}},
		{name: "apply above maximum", args: []string{"--compose-apply-timeout", "2h1s"}},
		{name: "wait below minimum", args: []string{"--compose-wait-timeout", "999ms"}},
		{name: "wait above maximum", args: []string{"--compose-wait-timeout", "1h1s"}},
		{name: "apply must exceed wait", args: []string{"--compose-apply-timeout", "2m", "--compose-wait-timeout", "2m"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := FromFlags(test.args)
			if (err == nil) != test.ok {
				t.Fatalf("FromFlags() error = %v, want success %v", err, test.ok)
			}
			if err == nil && (configuration.ComposeConfigTimeout < time.Second || configuration.ComposeApplyTimeout <= configuration.ComposeWaitTimeout) {
				t.Fatalf("accepted invalid timeouts: %+v", configuration)
			}
		})
	}
}
