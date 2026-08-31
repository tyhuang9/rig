package generatedimage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type builderDaemonFake struct {
	mu sync.Mutex

	network  *dockerNetwork
	builders map[string]buildxBuilder
	nodes    map[string]builderIdentity
	boxes    map[string]buildkitContainer
	calls    []runtimeprocess.CommandRequest
	err      error
	stderr   []byte
	outputs  [][]byte
}

func (d *builderDaemonFake) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	d.calls = append(d.calls, request)
	if d.err != nil {
		output := append([]byte(nil), d.stderr...)
		d.outputs = append(d.outputs, output)
		return runtimeprocess.CommandResult{Stderr: output}, d.err
	}
	if len(request.Args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("invalid fake Docker command")
	}
	switch request.Args[0] {
	case "network":
		switch request.Args[1] {
		case "inspect":
			if d.network == nil || request.Args[len(request.Args)-1] != d.network.Name {
				output := []byte("Error response from daemon: network not found")
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stderr: output}, errors.New("exit status 1")
			}
			body, _ := json.Marshal(d.network)
			d.outputs = append(d.outputs, body)
			return runtimeprocess.CommandResult{Stdout: body}, nil
		case "create":
			name := request.Args[len(request.Args)-1]
			labels := labelsFromCreate(request.Args)
			d.network = &dockerNetwork{Name: name, Driver: "bridge", Scope: "local", Labels: labels, Options: optionsFromCreate(request.Args)}
			output := []byte("fake-network-id")
			d.outputs = append(d.outputs, output)
			return runtimeprocess.CommandResult{Stdout: output}, nil
		}
	case "buildx":
		switch request.Args[1] {
		case "ls":
			var lines []string
			for _, builder := range d.builders {
				body, _ := json.Marshal(builder)
				lines = append(lines, string(body))
			}
			output := []byte(strings.Join(lines, "\n"))
			d.outputs = append(d.outputs, output)
			return runtimeprocess.CommandResult{Stdout: output}, nil
		case "create":
			name := flagArgument(request.Args, "--name")
			if name == "" {
				return runtimeprocess.CommandResult{}, errors.New("missing builder name")
			}
			if d.builders == nil {
				d.builders = make(map[string]buildxBuilder)
			}
			if d.nodes == nil {
				d.nodes = make(map[string]builderIdentity)
			}
			d.builders[name] = buildxBuilder{Name: name, Driver: flagArgument(request.Args, "--driver")}
			d.nodes[name] = builderIdentity{BuilderName: name, NodeName: flagArgument(request.Args, "--node"), NetworkName: strings.TrimPrefix(driverOption(request.Args, "network"), "network=")}
			output := []byte(name)
			d.outputs = append(d.outputs, output)
			return runtimeprocess.CommandResult{Stdout: output}, nil
		case "inspect":
			name := flagArgument(request.Args, "--builder")
			if identity, exists := d.nodes[name]; exists {
				if d.boxes == nil {
					d.boxes = make(map[string]buildkitContainer)
				}
				if _, exists := d.boxes[buildkitContainerName(identity)]; !exists {
					d.boxes[buildkitContainerName(identity)] = validBuildkitContainer(identity)
				}
			}
			output := []byte("Name: fake\nDriver: docker-container\n")
			d.outputs = append(d.outputs, output)
			return runtimeprocess.CommandResult{Stdout: output}, nil
		}
	case "container":
		if request.Args[1] == "inspect" {
			name := request.Args[len(request.Args)-1]
			container, exists := d.boxes[name]
			if !exists {
				output := []byte("Error response from daemon: No such container")
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stderr: output}, errors.New("exit status 1")
			}
			body, _ := json.Marshal(container)
			d.outputs = append(d.outputs, body)
			return runtimeprocess.CommandResult{Stdout: body}, nil
		}
	}
	return runtimeprocess.CommandResult{}, errors.New("unsupported fake Docker command")
}

func (d *builderDaemonFake) requests() []runtimeprocess.CommandRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]runtimeprocess.CommandRequest(nil), d.calls...)
}

func (d *builderDaemonFake) outputWasCleared() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, output := range d.outputs {
		for _, value := range output {
			if value != 0 {
				return false
			}
		}
	}
	return true
}

func TestBuilderManagerCreatesBootstrapsAndScopesTheBuilder(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", filepath.Join(t.TempDir(), "inherited-docker"))
	t.Setenv("BUILDX_CONFIG", filepath.Join(t.TempDir(), "inherited-buildx"))
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)

	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.DockerExecutable == "" || !strings.HasPrefix(session.BuilderName, "rig-buildkit-") {
		t.Fatalf("session = %#v", session)
	}
	environment := environmentValues(session.Environment())
	for key, inherited := range map[string]string{"DOCKER_CONFIG": os.Getenv("DOCKER_CONFIG"), "BUILDX_CONFIG": os.Getenv("BUILDX_CONFIG")} {
		if environment[key] == "" || environment[key] == inherited {
			t.Fatalf("%s inherited or missing: %#v", key, environment)
		}
	}
	if _, present := environment["DOCKER_TLS_VERIFY"]; present {
		t.Fatal("unrelated inherited Docker environment was passed through")
	}
	if session.Environment()[0] == "" {
		t.Fatal("unexpected blank environment")
	}
	copy := session.Environment()
	copy[0] = "MUTATED=value"
	if session.Environment()[0] == "MUTATED=value" {
		t.Fatal("BuilderSession exposed a mutable environment")
	}

	requests := daemon.requests()
	if got, want := commandKinds(requests), []string{"network inspect", "network create", "network inspect", "buildx ls", "buildx create", "buildx inspect", "buildx ls", "container inspect"}; !sameStrings(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	if !containsExactOrPrefix(requests[1].Args, "com.docker.network.bridge.enable_icc=false") {
		t.Fatalf("network create did not disable ICC: %#v", requests[1].Args)
	}
	create := requests[4]
	for _, required := range []string{
		"--driver", "docker-container", "--node", "--driver-opt", "network=rig-buildnet-",
		"memory=2147483648", "memory-swap=2147483648", "cpu-period=100000", "cpu-quota=100000",
		"--buildkitd-config", "--use",
	} {
		if !containsExactOrPrefix(create.Args, required) {
			t.Fatalf("buildx create lacks %q: %#v", required, create.Args)
		}
	}
	configPath := flagArgument(create.Args, "--buildkitd-config")
	body, readErr := os.ReadFile(configPath)
	if readErr != nil || string(body) != buildkitdConfiguration {
		t.Fatalf("buildkit config = %q, %v", body, readErr)
	}
	for _, path := range []string{environment["DOCKER_CONFIG"], environment["BUILDX_CONFIG"]} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("scoped config directory %q unavailable: %v", path, statErr)
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("scoped config directory %q was not empty: %v %#v", path, readErr, entries)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("config permissions = %o, want 0700", info.Mode().Perm())
		}
	}
	if !daemon.outputWasCleared() {
		t.Fatal("raw builder output was retained after preparation")
	}
}

func TestBuilderManagerReusesAndRevalidatesExistingBuilder(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	first, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstCount := len(daemon.requests())
	second, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.BuilderName != second.BuilderName || first.DockerExecutable != second.DockerExecutable {
		t.Fatalf("sessions differ: %#v %#v", first, second)
	}
	requests := daemon.requests()[firstCount:]
	if got, want := commandKinds(requests), []string{"network inspect", "buildx ls", "buildx inspect", "buildx ls", "container inspect"}; !sameStrings(got, want) {
		t.Fatalf("reuse commands = %#v, want %#v", got, want)
	}
}

func TestBuilderManagerRefusesPostBootstrapContainerResourceDrift(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	identity := daemon.nodes[session.BuilderName]
	container := daemon.boxes[buildkitContainerName(identity)]
	container.HostConfig.Memory = 1 << 30
	daemon.boxes[buildkitContainerName(identity)] = container
	daemon.mu.Unlock()
	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("resource drift error = %v", err)
	}
}

func TestBuilderManagerRefusesBuildNetworkIsolationDrift(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	if _, err := manager.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	daemon.network.Options["com.docker.network.bridge.enable_icc"] = "true"
	daemon.mu.Unlock()
	_, err := manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("network drift error = %v", err)
	}
}

func TestBuilderManagerRefusesBuilderDriverDrift(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	daemon.builders[session.BuilderName] = buildxBuilder{Name: session.BuilderName, Driver: "docker"}
	daemon.mu.Unlock()
	before := len(daemon.requests())
	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("drift error = %v", err)
	}
	for _, request := range daemon.requests()[before:] {
		if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] == "create" {
			t.Fatal("manager adopted or replaced a drifted builder")
		}
	}
}

func TestBuilderManagerClassifiesDaemonFailureAndClearsOutput(t *testing.T) {
	daemon := &builderDaemonFake{err: errors.New("exit status 1"), stderr: []byte("Cannot connect to the Docker daemon at unix:///private/socket")}
	manager := newBuilderManagerForTest(t, daemon)
	_, err := manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderRuntimeUnavailable) {
		t.Fatalf("daemon error = %v", err)
	}
	if !daemon.outputWasCleared() {
		t.Fatal("daemon output was not cleared")
	}
}

func TestBuilderManagerPrepareIsConcurrentAndIdempotent(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	const workers = 8
	results := make(chan BuilderSession, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			session, err := manager.Prepare(context.Background())
			if err != nil {
				errors <- err
				return
			}
			results <- session
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var name string
	for session := range results {
		if name == "" {
			name = session.BuilderName
		} else if session.BuilderName != name {
			t.Fatalf("concurrent sessions selected different builders: %q, %q", name, session.BuilderName)
		}
	}
	creates := 0
	for _, request := range daemon.requests() {
		if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("buildx create calls = %d, want 1", creates)
	}
}

func newBuilderManagerForTest(t *testing.T, runner runtimeprocess.CommandRunner) *BuilderManager {
	t.Helper()
	manager, err := NewBuilderManager(runner, BuilderManagerOptions{
		DataRoot: t.TempDir(), DockerExecutable: filepath.Join(t.TempDir(), "docker"), PrepareTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func labelsFromCreate(args []string) map[string]string {
	labels := make(map[string]string)
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--label" {
			continue
		}
		key, value, found := strings.Cut(args[index+1], "=")
		if found {
			labels[key] = value
		}
	}
	return labels
}

func optionsFromCreate(args []string) map[string]string {
	options := make(map[string]string)
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--opt" {
			continue
		}
		key, value, found := strings.Cut(args[index+1], "=")
		if found {
			options[key] = value
		}
	}
	return options
}

func driverOption(args []string, key string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--driver-opt" {
			continue
		}
		if strings.HasPrefix(args[index+1], key+"=") {
			return args[index+1]
		}
	}
	return ""
}

func validBuildkitContainer(identity builderIdentity) buildkitContainer {
	var container buildkitContainer
	container.Name = "/" + buildkitContainerName(identity)
	container.HostConfig.Memory = 2 << 30
	container.HostConfig.MemorySwap = 2 << 30
	container.HostConfig.CPUPeriod = 100000
	container.HostConfig.CPUQuota = 100000
	container.HostConfig.NetworkMode = identity.NetworkName
	container.NetworkSettings.Networks = map[string]json.RawMessage{identity.NetworkName: json.RawMessage(`{}`)}
	return container
}

func flagArgument(args []string, flag string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	return ""
}

func commandKinds(requests []runtimeprocess.CommandRequest) []string {
	kinds := make([]string, 0, len(requests))
	for _, request := range requests {
		if len(request.Args) >= 2 {
			kinds = append(kinds, request.Args[0]+" "+request.Args[1])
		}
	}
	return kinds
}

func environmentValues(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, value, found := strings.Cut(value, "=")
		if found {
			result[key] = value
		}
	}
	return result
}

func containsExactOrPrefix(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted || strings.HasPrefix(value, wanted) {
			return true
		}
	}
	return false
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
