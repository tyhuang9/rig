package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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
			err := openDashboardWith(context.Background(), "http://127.0.0.1:7345", tt.goos, func(ctx context.Context, got string, values ...string) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > dashboardCommandTimeout {
					t.Fatalf("runner context is not usefully bounded: deadline=%v ok=%v", deadline, ok)
				}
				name, args = got, append([]string(nil), values...)
				return nil
			})
			if err != nil || name != tt.name || !reflect.DeepEqual(args, tt.args) {
				t.Fatalf("command=%s %v err=%v", name, args, err)
			}
		})
	}
}

func TestRunDashboardCommandWaitsForRepeatedRunnerCompletion(t *testing.T) {
	for i := 0; i < 3; i++ {
		completion := filepath.Join(t.TempDir(), "completed")
		t.Setenv("HOSTD_DASHBOARD_HELPER_COMPLETION", completion)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := runDashboardCommand(ctx, os.Args[0], "-test.run=^TestDashboardCommandHelperProcess$")
		cancel()
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if _, err := os.Stat(completion); err != nil {
			t.Fatalf("run %d returned before helper completion: %v", i, err)
		}
	}
}

func TestDashboardCommandHelperProcess(t *testing.T) {
	completion := os.Getenv("HOSTD_DASHBOARD_HELPER_COMPLETION")
	if completion == "" {
		return
	}
	time.Sleep(25 * time.Millisecond)
	if err := os.WriteFile(completion, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
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
