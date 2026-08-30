package main

import (
	"context"
	"reflect"
	"testing"
)

func TestOpenDashboardUsesDirectPlatformCommands(t *testing.T) {
	tests := []struct {
		goos, name string
		args       []string
	}{
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", "http://127.0.0.1:7345"}},
		{"darwin", "open", []string{"http://127.0.0.1:7345"}},
		{"linux", "xdg-open", []string{"http://127.0.0.1:7345"}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			var name string
			var args []string
			err := openDashboardWith(context.Background(), "http://127.0.0.1:7345", tt.goos, func(_ context.Context, got string, values ...string) error {
				name, args = got, append([]string(nil), values...)
				return nil
			})
			if err != nil || name != tt.name || !reflect.DeepEqual(args, tt.args) {
				t.Fatalf("command=%s %v err=%v", name, args, err)
			}
		})
	}
}

func TestOpenDashboardRejectsNonOriginAndUnsupportedPlatform(t *testing.T) {
	called := false
	runner := func(context.Context, string, ...string) error { called = true; return nil }
	for _, target := range []string{"https://example.com:443", "http://127.0.0.1:7345/path", "http://127.0.0.1"} {
		if err := openDashboardWith(context.Background(), target, "windows", runner); err == nil {
			t.Fatalf("accepted %q", target)
		}
	}
	if called {
		t.Fatal("invalid URL invoked command runner")
	}
	if err := openDashboardWith(context.Background(), "http://127.0.0.1:7345", "plan9", runner); err == nil {
		t.Fatal("unsupported OS accepted")
	}
}
