package docker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeRunner struct {
	path     string
	lookErr  error
	commands []Command
	run      func(context.Context, Command) (string, error)
}

func (runner *fakeRunner) LookPath(string) (string, error) { return runner.path, runner.lookErr }
func (runner *fakeRunner) Run(ctx context.Context, command Command) (string, error) {
	runner.commands = append(runner.commands, command)
	return runner.run(ctx, command)
}

func TestCheckerUsesFixedArgumentsEndpointAndResources(t *testing.T) {
	runner := &fakeRunner{path: "/fixed/docker"}
	runner.run = func(_ context.Context, command Command) (string, error) {
		if command.Arguments[0] == "version" {
			return "27.1.2\n", nil
		}
		return "2.29.1\n", nil
	}
	resources := HostResources{MemoryTotalBytes: 10, MemoryAvailableBytes: 5, DiskTotalBytes: 20, DiskAvailableBytes: 8}
	checker := Checker{Runner: runner, CommandTimeout: time.Second, DockerEndpoint: "tcp://127.0.0.1:2375;ignored", ResourceRoot: "/state", CollectResources: func(root string) (HostResources, error) {
		if root != "/state" {
			t.Fatalf("resource root = %q", root)
		}
		return resources, nil
	}}
	diagnostic := checker.Check(context.Background(), false)
	if !diagnostic.EngineReady || !diagnostic.ComposeAvailable || diagnostic.DockerVersion != "27.1.2" || diagnostic.ComposeVersion != "2.29.1" || diagnostic.Resources != resources {
		t.Fatalf("unexpected diagnostics: %#v", diagnostic)
	}
	wantArguments := [][]string{{"version", "--format", "{{.Server.Version}}"}, {"compose", "version", "--short"}}
	for index, command := range runner.commands {
		if command.Executable != "/fixed/docker" || !reflect.DeepEqual(command.Arguments, wantArguments[index]) {
			t.Fatalf("command %d = %#v", index, command)
		}
		if command.Environment["DOCKER_HOST"] != checker.DockerEndpoint {
			t.Fatalf("DOCKER_HOST = %q", command.Environment["DOCKER_HOST"])
		}
	}
}

func TestCheckerBoundsCommandsAndReportsErrors(t *testing.T) {
	runner := &fakeRunner{path: "docker"}
	runner.run = func(ctx context.Context, command Command) (string, error) {
		if command.Arguments[0] == "version" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "", errors.New("compose failed")
	}
	checker := Checker{Runner: runner, CommandTimeout: 10 * time.Millisecond, CollectResources: func(string) (HostResources, error) { return HostResources{}, errors.New("unavailable") }}
	diagnostic := checker.Check(context.Background(), false)
	if diagnostic.EngineReady || diagnostic.ComposeAvailable || diagnostic.DockerDetail != "Docker engine check timed out" || diagnostic.ComposeDetail != "Docker Compose V2 is unavailable" {
		t.Fatalf("unexpected error diagnostics: %#v", diagnostic)
	}
}

func TestCheckerHandlesMissingCLI(t *testing.T) {
	checker := Checker{Runner: &fakeRunner{lookErr: errors.New("missing")}, CollectResources: func(string) (HostResources, error) { return HostResources{}, nil }}
	diagnostic := checker.Check(context.Background(), false)
	if diagnostic.ClientAvailable || diagnostic.DockerDetail != "Docker CLI not found" || diagnostic.ComposeDetail == "" {
		t.Fatalf("unexpected missing-client diagnostics: %#v", diagnostic)
	}
}
