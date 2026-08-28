package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRunHostdDispatchesModesAndGuidesNonInteractiveUse(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		interactive bool
		want        int
		wantUI      bool
		wantServer  []string
		wantText    string
	}{
		{name: "no args starts UI interactively", interactive: true, wantUI: true},
		{name: "no args guides pipe users", want: 2, wantText: "interactive terminal"},
		{name: "explicit UI guides pipe users", args: []string{"ui"}, want: 2, wantText: "interactive terminal"},
		{name: "explicit UI parses options", args: []string{"ui", "--endpoint", "http://controller", "--session-file", "custom/session"}, interactive: true, wantUI: true},
		{name: "serve starts daemon", args: []string{"serve", "--fake-runtime"}, wantServer: []string{"--fake-runtime"}},
		{name: "legacy flags warn", args: []string{"--fake-runtime"}, wantServer: []string{"--fake-runtime"}, wantText: "deprecated"},
		{name: "unknown command errors", args: []string{"deploy"}, want: 2, wantText: "unknown hostd command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			var ui tuiLaunchOptions
			calledUI := false
			var serverArgs []string
			r := hostdRunners{interactive: func() bool { return tt.interactive }, stdout: &output, stderr: &output, runUI: func(options tuiLaunchOptions) error { calledUI = true; ui = options; return nil }, runServer: func(args []string) int { serverArgs = append([]string(nil), args...); return 0 }}
			got := runHostd(tt.args, r)
			if got != tt.want {
				t.Fatalf("code=%d want=%d output=%s", got, tt.want, output.String())
			}
			if calledUI != tt.wantUI {
				t.Fatalf("ui called=%v", calledUI)
			}
			if !reflect.DeepEqual(serverArgs, tt.wantServer) {
				t.Fatalf("server args=%v want=%v", serverArgs, tt.wantServer)
			}
			if tt.wantText != "" && !bytes.Contains(output.Bytes(), []byte(tt.wantText)) {
				t.Fatalf("output=%q missing %q", output.String(), tt.wantText)
			}
			if calledUI && len(tt.args) > 0 && ui.historyFile == "" {
				t.Fatal("TUI history file was not derived")
			}
		})
	}
}
func TestRunHostdReportsUIFailure(t *testing.T) {
	var output bytes.Buffer
	code := runHostd([]string{"ui"}, hostdRunners{interactive: func() bool { return true }, stdout: &output, stderr: &output, runUI: func(tuiLaunchOptions) error { return errors.New("boom") }, runServer: func([]string) int { return 0 }})
	if code != 1 || !bytes.Contains(output.Bytes(), []byte("boom")) {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestRunHostdUIHelpReturnsSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	code := runHostd([]string{"ui", "--help"}, hostdRunners{interactive: func() bool { return false }, stdout: &stdout, stderr: &stderr, runUI: func(tuiLaunchOptions) error { called = true; return nil }, runServer: func([]string) int { return 0 }})
	if code != 0 || called || !bytes.Contains(stdout.Bytes(), []byte("-endpoint")) || stderr.Len() != 0 {
		t.Fatalf("code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestHistoryFileForSessionPreservesSafeDefaultAndExplicitDirectory(t *testing.T) {
	if got := historyFileForSession(filepath.Join("custom", "session.json")); got != filepath.Join("custom", "hostd-tui-history.json") {
		t.Fatalf("explicit directory history=%q", got)
	}
	if got := historyFileForSession("session.json"); filepath.Dir(got) == "." {
		t.Fatalf("relative session history escaped protected config path: %q", got)
	}
}
