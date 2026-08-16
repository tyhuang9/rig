package config

import (
	"os"
	"path/filepath"
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
