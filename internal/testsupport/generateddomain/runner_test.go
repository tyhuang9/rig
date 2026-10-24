package generateddomain

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestRunnerServesExactCapacityProbeByContainerName(t *testing.T) {
	runner := NewRunner(Options{MemoryAvailableBytes: 512, DiskAvailableBytes: 1024})
	runner.container = true
	runner.running = true
	runner.network = true

	result, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"container", "exec", "rig-generated-caddy-v1", "sh", "-c", "capacity"},
	})
	if err != nil || string(result.Stdout) != "512 1024\n" || runner.CapacityCalls() != 1 {
		t.Fatalf("capacity=%q calls=%d error=%v", result.Stdout, runner.CapacityCalls(), err)
	}
}

func TestRunnerAppliesExactGatewayRouteConfigAndPromotion(t *testing.T) {
	runner := NewRunner(Options{})
	runner.container = true
	runner.running = true
	runner.network = true
	runner.files["active.json"] = []byte(`{"apps":{}}`)
	runner.config = append([]byte(nil), runner.files["active.json"]...)
	networkProjection, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{
		"network", "inspect", "--format", `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Labels}},"driver":{{json .Driver}},"scope":{{json .Scope}},"options":{{json .Options}}}`, NetworkID,
	}})
	var projected struct {
		ID string `json:"id"`
	}
	if err != nil || json.Unmarshal(networkProjection.Stdout, &projected) != nil || projected.ID != NetworkID {
		t.Fatalf("network projection=%q decoded=%q error=%v", networkProjection.Stdout, projected.ID, err)
	}
	proposed := []byte(`{"apps":{"example":{}}}`)
	path := t.TempDir() + "/proposed.json"
	if err := os.WriteFile(path, proposed, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"container", "cp", path, "rig-generated-caddy-v1:/config/proposed.json"},
		{"container", "exec", "rig-generated-caddy-v1", "caddy", "validate", "--config", "/config/proposed.json"},
		{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/proposed.json"},
		{"container", "exec", "--user", "0:0", "rig-generated-caddy-v1", "cp", "/config/proposed.json", "/config/current.json"},
		{"container", "exec", "--user", "0:0", "rig-generated-caddy-v1", "cp", "/config/current.json", "/config/active.next.json"},
		{"container", "exec", "--user", "0:0", "rig-generated-caddy-v1", "mv", "/config/active.next.json", "/config/active.json"},
	}
	for _, command := range commands {
		if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: command}); err != nil {
			t.Fatalf("command=%#v error=%v", command, err)
		}
	}
	observed, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"container", "exec", "rig-generated-caddy-v1", "wget", "-qO-", "http://127.0.0.1:2019/config/"},
	})
	if err != nil || !bytes.Equal(observed.Stdout, proposed) || !bytes.Equal(runner.files["active.json"], proposed) {
		t.Fatalf("live=%q active=%q error=%v", observed.Stdout, runner.files["active.json"], err)
	}
}

type fallbackRunner func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)

func (f fallbackRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return f(request)
}

func TestRunnerDelegatesCandidateCommandsAndOptionallyMergesFullCensus(t *testing.T) {
	foreignImage := "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	fallback := fallbackRunner(func(request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		switch {
		case reflect.DeepEqual(request.Args, []string{"image", "ls", "--all", "--no-trunc", "--quiet"}):
			return runtimeprocess.CommandResult{Stdout: []byte(foreignImage + "\n")}, nil
		case reflect.DeepEqual(request.Args, []string{"image", "inspect", "--format", "candidate", foreignImage}):
			return runtimeprocess.CommandResult{Stdout: []byte("candidate-observation")}, nil
		case reflect.DeepEqual(request.Args, []string{"network", "create", "rig-app-network"}):
			return runtimeprocess.CommandResult{Stdout: []byte("app-network-id\n")}, nil
		default:
			t.Fatalf("unexpected fallback command: %#v", request.Args)
			return runtimeprocess.CommandResult{}, nil
		}
	})
	runner := NewRunner(Options{Fallback: fallback, MergeFallbackCensus: true})

	census, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"image", "ls", "--all", "--no-trunc", "--quiet"},
	})
	if err != nil || !bytes.Contains(census.Stdout, []byte(ImageID)) || !bytes.Contains(census.Stdout, []byte(foreignImage)) {
		t.Fatalf("merged image census=%q error=%v", census.Stdout, err)
	}
	inspection, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"image", "inspect", "--format", "candidate", foreignImage},
	})
	if err != nil || string(inspection.Stdout) != "candidate-observation" {
		t.Fatalf("candidate inspection=%q error=%v", inspection.Stdout, err)
	}
	created, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"network", "create", "rig-app-network"},
	})
	if err != nil || string(created.Stdout) != "app-network-id\n" {
		t.Fatalf("candidate network create=%q error=%v", created.Stdout, err)
	}
}

func TestRunnerTracksExactGatewayAttachmentsAndCanDelegateCapacityProbe(t *testing.T) {
	capacity := runtimeprocess.CommandResult{Stdout: []byte("cancelled-capacity")}
	fallback := fallbackRunner(func(request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if len(request.Args) == 6 && request.Args[0] == "container" && request.Args[1] == "exec" &&
			request.Args[2] == ContainerID && request.Args[3] == "sh" && request.Args[4] == "-c" {
			return capacity, nil
		}
		t.Fatalf("unexpected fallback command: %#v", request.Args)
		return runtimeprocess.CommandResult{}, nil
	})
	runner := NewRunner(Options{Fallback: fallback, DelegateCapacity: true})
	runner.container = true
	runner.running = true
	runner.network = true

	if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"network", "connect", "rig-app-network", ContainerID},
	}); err != nil {
		t.Fatal(err)
	}
	observed, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"container", "inspect", "--format", "details", ContainerID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var inspection struct {
		Networks map[string]json.RawMessage `json:"networks"`
	}
	if err := json.Unmarshal(observed.Stdout, &inspection); err != nil || inspection.Networks["rig-app-network"] == nil {
		t.Fatalf("gateway attachment=%s error=%v", observed.Stdout, err)
	}
	delegated, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"container", "exec", ContainerID, "sh", "-c", "capacity"},
	})
	if err != nil || !reflect.DeepEqual(delegated, capacity) {
		t.Fatalf("delegated capacity=%#v error=%v", delegated, err)
	}
	if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"network", "disconnect", "rig-app-network", ContainerID},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerServesGatewayReadinessOnlyOverExactAttachedNetwork(t *testing.T) {
	runner := NewRunner(Options{})
	runner.container = true
	runner.running = true
	runner.network = true

	if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{
		Args: []string{"network", "connect", "rig-app-network", ContainerID},
	}); err != nil {
		t.Fatal(err)
	}
	probe := func(network string) (runtimeprocess.CommandResult, error) {
		return runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{
			"container", "exec", ContainerID,
			"curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}",
			"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
			"http://api." + network + ":3000/",
		}})
	}
	result, err := probe("rig-app-network")
	if err != nil || string(result.Stdout) != "204" {
		t.Fatalf("attached readiness=%q error=%v", result.Stdout, err)
	}
	if _, err := probe("rig-unattached-network"); err == nil {
		t.Fatal("unattached gateway readiness probe succeeded")
	}
}
