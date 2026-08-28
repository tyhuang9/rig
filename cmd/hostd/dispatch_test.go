package main

import (
	"reflect"
	"testing"
)

func TestClassifyHostdInvocation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantMode   hostdMode
		wantArgs   []string
		wantLegacy bool
		wantError  bool
	}{
		{name: "no arguments opens ui", wantMode: hostdModeUI},
		{name: "explicit ui", args: []string{"ui", "--endpoint", "http://127.0.0.1:8000"}, wantMode: hostdModeUI, wantArgs: []string{"--endpoint", "http://127.0.0.1:8000"}},
		{name: "explicit server", args: []string{"serve", "--fake-runtime"}, wantMode: hostdModeServe, wantArgs: []string{"--fake-runtime"}},
		{name: "legacy server flags", args: []string{"--data-root", ".hostd-dev", "--fake-runtime"}, wantMode: hostdModeServe, wantArgs: []string{"--data-root", ".hostd-dev", "--fake-runtime"}, wantLegacy: true},
		{name: "unknown command", args: []string{"deploy"}, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyHostdInvocation(tt.args)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError %v", err, tt.wantError)
			}
			if tt.wantError {
				return
			}
			if got.mode != tt.wantMode || got.legacyServerArgs != tt.wantLegacy || !reflect.DeepEqual(got.args, tt.wantArgs) {
				t.Fatalf("invocation = %#v, want mode=%v args=%v legacy=%v", got, tt.wantMode, tt.wantArgs, tt.wantLegacy)
			}
		})
	}
}

func TestClassifyHostdInvocationCopiesArguments(t *testing.T) {
	args := []string{"serve", "--listen", "127.0.0.1:7345"}
	got, err := classifyHostdInvocation(args)
	if err != nil {
		t.Fatal(err)
	}
	args[1] = "changed"
	if got.args[0] != "--listen" {
		t.Fatalf("classified arguments alias caller memory: %v", got.args)
	}
}
