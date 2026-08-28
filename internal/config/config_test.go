package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFakeRuntimeRequiresIsolatedDevelopmentRoot(t *testing.T) {
	tests := []struct {
		name string
		root string
		ok   bool
	}{
		{name: "development marker", root: filepath.Join(t.TempDir(), ".hostd-dev"), ok: true},
		{name: "isolated test temporary root", root: filepath.Join(os.TempDir(), "hostd-test-config"), ok: true},
		{name: "default production root", root: Defaults().DataRoot, ok: false},
		{name: "arbitrary root", root: filepath.Join(t.TempDir(), "state"), ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FromFlags([]string{"--fake-runtime", "--data-root", test.root})
			if (err == nil) != test.ok {
				t.Fatalf("FromFlags() error = %v, want success %v", err, test.ok)
			}
		})
	}
}

func TestDevelopmentRootDoesNotImplicitlyEnableFakeRuntime(t *testing.T) {
	config, err := FromFlags([]string{"--data-root", filepath.Join(t.TempDir(), ".hostd-dev")})
	if err != nil {
		t.Fatal(err)
	}
	if config.FakeRuntime {
		t.Fatal("development data root implicitly enabled fake runtime")
	}
}

func TestListenAddressMustBeExplicitLoopback(t *testing.T) {
	tests := []struct {
		address string
		ok      bool
	}{
		{address: "127.0.0.1:7345", ok: true},
		{address: "127.42.0.9:7345", ok: true},
		{address: "[::1]:7345", ok: true},
		{address: "0.0.0.0:7345", ok: false},
		{address: "[::]:7345", ok: false},
		{address: "192.168.1.10:7345", ok: false},
		{address: "localhost:7345", ok: false},
		{address: "hostd.local:7345", ok: false},
		{address: ":7345", ok: false},
		{address: "127.0.0.1:http", ok: false},
		{address: "127.0.0.1", ok: false},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			_, err := FromFlags([]string{"--listen", test.address})
			if (err == nil) != test.ok {
				t.Fatalf("FromFlags(--listen %q) error = %v, want success %v", test.address, err, test.ok)
			}
		})
	}
}

func TestGitHubAppPublicConfigurationIsPairwiseAndBounded(t *testing.T) {
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{name: "disabled", ok: true},
		{name: "enabled", args: []string{"--github-client-id", "Iv1.abc_123", "--github-app-slug", "hostd-app"}, ok: true},
		{name: "client only", args: []string{"--github-client-id", "Iv1_abc"}},
		{name: "slug only", args: []string{"--github-app-slug", "hostd-app"}},
		{name: "client whitespace", args: []string{"--github-client-id", "client id", "--github-app-slug", "hostd-app"}},
		{name: "slug URL", args: []string{"--github-client-id", "client", "--github-app-slug", "https://evil.example/app"}},
		{name: "slug traversal", args: []string{"--github-client-id", "client", "--github-app-slug", "../app"}},
		{name: "slug uppercase", args: []string{"--github-client-id", "client", "--github-app-slug", "Hostd-App"}},
		{name: "oversized client", args: []string{"--github-client-id", strings.Repeat("a", 256), "--github-app-slug", "hostd-app"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration, err := FromFlags(test.args)
			if (err == nil) != test.ok {
				t.Fatalf("FromFlags() error = %v, want success %v", err, test.ok)
			}
			if err == nil && configuration.GitHubConnectionsEnabled() != (test.name == "enabled") {
				t.Fatalf("GitHubConnectionsEnabled = %v", configuration.GitHubConnectionsEnabled())
			}
		})
	}
}
