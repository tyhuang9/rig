package generatedimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type builderDaemonFake struct {
	mu sync.Mutex

	network             *dockerNetwork
	builders            map[string]buildxBuilder
	nodes               map[string]builderIdentity
	boxes               map[string]buildkitContainer
	calls               []runtimeprocess.CommandRequest
	err                 error
	stderr              []byte
	outputs             [][]byte
	osType              string
	containerRunErr     error
	containerRunStderr  []byte
	containerRunLostAck bool
	buildxCreateLostAck bool
	infoOverride        *dockerInfo
	replaceOnLifecycle  bool
	duplicateBuilders   bool
}

type blockingBuilderRunner struct {
	delegate runtimeprocess.CommandRunner
	target   string
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

type builderRunnerFunc func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)

func (f builderRunnerFunc) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return f(ctx, request)
}

var (
	builderCompletedExitOnce sync.Once
	builderCompletedExitErr  error
)

func completedBuilderExitError() error {
	builderCompletedExitOnce.Do(func() {
		command := exec.Command(os.Args[0], "-test.run=^TestBuilderManagerCreatesBootstrapsAndScopesTheBuilder$")
		command.Env = append(os.Environ(), "RIG_TEST_BUILDER_COMPLETED_EXIT=1")
		builderCompletedExitErr = command.Run()
	})
	return builderCompletedExitErr
}

func (r *blockingBuilderRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if commandKindForBuilderTest(request) == r.target {
		r.once.Do(func() { close(r.entered) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return runtimeprocess.CommandResult{}, ctx.Err()
		}
	}
	return r.delegate.Run(ctx, request)
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
	case "info":
		info := validDockerInfo()
		if d.infoOverride != nil {
			info = *d.infoOverride
		} else if d.osType != "" {
			info.OSType = d.osType
		}
		body, _ := json.Marshal(info)
		d.outputs = append(d.outputs, body)
		return runtimeprocess.CommandResult{Stdout: body}, nil
	case "network":
		switch request.Args[1] {
		case "inspect":
			if d.network == nil || request.Args[len(request.Args)-1] != d.network.Name {
				name := request.Args[len(request.Args)-1]
				output := []byte("Error response from daemon: network " + name + " not found")
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stderr: output}, completedBuilderExitError()
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
			if got := flagArgument(request.Args, "--format"); got != "json" {
				return runtimeprocess.CommandResult{}, errors.New("unexpected Buildx builder-list format")
			}
			var lines []string
			for _, builder := range d.builders {
				body, _ := json.Marshal(builder)
				lines = append(lines, string(body))
				if d.duplicateBuilders {
					lines = append(lines, string(body))
				}
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
			endpoint := request.Args[len(request.Args)-1]
			d.builders[name] = buildxBuilder{Name: name, Driver: flagArgument(request.Args, "--driver"), Nodes: []buildxNode{{Name: flagArgument(request.Args, "--node"), Endpoint: endpoint}}}
			d.nodes[name] = builderIdentity{BuilderName: name, NodeName: flagArgument(request.Args, "--node")}
			output := []byte(name)
			d.outputs = append(d.outputs, output)
			if d.buildxCreateLostAck {
				return runtimeprocess.CommandResult{Stdout: output}, errors.New("buildx create acknowledgement lost")
			}
			return runtimeprocess.CommandResult{Stdout: output}, nil
		case "inspect":
			output := []byte("Name: fake\nDriver: remote\n")
			d.outputs = append(d.outputs, output)
			return runtimeprocess.CommandResult{Stdout: output}, nil
		}
	case "container":
		switch request.Args[1] {
		case "inspect":
			name := request.Args[len(request.Args)-1]
			container, exists := d.boxes[name]
			if !exists {
				output := []byte("Error: No such container: " + name)
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stderr: output}, completedBuilderExitError()
			}
			body, _ := json.Marshal(container)
			d.outputs = append(d.outputs, body)
			return runtimeprocess.CommandResult{Stdout: body}, nil
		case "run":
			if d.containerRunErr != nil && !d.containerRunLostAck {
				output := append([]byte(nil), d.containerRunStderr...)
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stderr: output}, d.containerRunErr
			}
			if d.boxes == nil {
				d.boxes = make(map[string]buildkitContainer)
			}
			name := flagArgument(request.Args, "--name")
			labels := labelsFromCreate(request.Args)
			identity := builderIdentity{
				Schema: builderStateSchema, BuilderName: labels["rig.builder"], NetworkName: labels["rig.network"],
				NodeName: "rig-node-" + strings.TrimPrefix(name, "rig-buildkitd-"),
			}
			quota := defaultStateQuotaBytes
			if value := labels["rig.quota.bytes"]; value != "" {
				_, _ = fmt.Sscan(value, &quota)
			}
			d.boxes[name] = validBuildkitContainer(identity, quota)
			output := []byte("sha256:" + strings.Repeat("1", 64))
			d.outputs = append(d.outputs, output)
			if d.containerRunLostAck {
				return runtimeprocess.CommandResult{Stdout: output}, d.containerRunErr
			}
			return runtimeprocess.CommandResult{Stdout: output}, nil
		case "start", "restart", "unpause":
			id := request.Args[len(request.Args)-1]
			for name, container := range d.boxes {
				if container.ID != id {
					continue
				}
				container.State.Running = true
				container.State.Paused = false
				container.State.Restarting = false
				if d.replaceOnLifecycle {
					container.ID = strings.Repeat("c", 64)
				}
				d.boxes[name] = container
				output := []byte(id)
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stdout: output}, nil
			}
			output := []byte("Error response from daemon: No such container")
			d.outputs = append(d.outputs, output)
			return runtimeprocess.CommandResult{Stderr: output}, errors.New("exit status 1")
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
	if os.Getenv("RIG_TEST_BUILDER_COMPLETED_EXIT") == "1" {
		os.Exit(1)
	}
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
	if got, want := commandKinds(requests), []string{"info --format", "network inspect", "network create", "network inspect", "container inspect", "container run", "container inspect", "buildx ls", "buildx create", "buildx ls", "buildx inspect", "buildx ls", "container inspect"}; !sameStrings(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	if !containsExactOrPrefix(requests[2].Args, "com.docker.network.bridge.enable_icc=false") {
		t.Fatalf("network create did not disable ICC: %#v", requests[2].Args)
	}
	containerRun := requests[5]
	for _, required := range []string{
		"--privileged", "--network", "rig-buildnet-", "--restart", "unless-stopped", "--memory", "3221225472", "--memory-swap",
		"--pids-limit", "512", "type=tmpfs,destination=/var/lib/buildkit,tmpfs-size=2147483648,tmpfs-mode=0700",
		"rig.quota.bytes=2147483648", buildkitImage,
	} {
		if !containsExactOrPrefix(containerRun.Args, required) {
			t.Fatalf("container run lacks %q: %#v", required, containerRun.Args)
		}
	}
	if !containsExactOrPrefix(containerRun.Args, "--oci-worker-gc-keepstorage=1073") {
		t.Fatalf("BuildKit GC keepstorage was not expressed in MB: %#v", containerRun.Args)
	}
	create := requests[8]
	for _, required := range []string{"--driver", "remote", "--node", "--driver-opt", "default-load=false", "--use", "docker-container://rig-buildkitd-"} {
		if !containsExactOrPrefix(create.Args, required) {
			t.Fatalf("buildx create lacks %q: %#v", required, create.Args)
		}
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

func TestBuilderManagerRefusesDuplicateBuilderRecords(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	identity, _, err := manager.preparePersistentState()
	if err != nil {
		t.Fatal(err)
	}
	daemon.network = &dockerNetwork{
		Name: identity.NetworkName, Driver: "bridge", Scope: "local",
		Labels: map[string]string{
			"rig.controller": "generated-builder", "rig.builder": identity.BuilderName,
			"rig.network": identity.NetworkName,
		},
		Options: map[string]string{"com.docker.network.bridge.enable_icc": "false"},
	}
	daemon.boxes = map[string]buildkitContainer{
		buildkitContainerName(identity): validBuildkitContainer(identity, defaultStateQuotaBytes),
	}
	daemon.builders[identity.BuilderName] = buildxBuilder{
		Name: identity.BuilderName, Driver: "remote",
		Nodes: []buildxNode{{Name: identity.NodeName, Endpoint: buildkitRemoteEndpoint(identity)}},
	}
	daemon.duplicateBuilders = true

	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("duplicate builder records error = %v", err)
	}
	if got, want := commandKinds(daemon.requests()), []string{"info --format", "network inspect", "container inspect", "buildx ls"}; !sameStrings(got, want) {
		t.Fatalf("commands after duplicate builder records = %#v, want read-only %#v", got, want)
	}
}

func TestBuildkitCommandExpressesGCKeepStorageInMegabytes(t *testing.T) {
	got := buildkitCommand(defaultStateQuotaBytes)
	if !sameStrings(got, []string{"--oci-worker-net", "bridge", "--oci-worker-gc", "--oci-worker-gc-keepstorage=1073", "--oci-max-parallelism=1"}) {
		t.Fatalf("buildkit command = %#v", got)
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
	if got, want := commandKinds(requests), []string{"info --format", "network inspect", "container inspect", "buildx ls", "buildx inspect", "buildx ls", "container inspect"}; !sameStrings(got, want) {
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

func TestBuilderManagerRefusesRestartPolicyDrift(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	identity := daemon.nodes[session.BuilderName]
	container := daemon.boxes[buildkitContainerName(identity)]
	container.HostConfig.RestartPolicy.Name = "no"
	daemon.boxes[buildkitContainerName(identity)] = container
	before := len(daemon.calls)
	daemon.mu.Unlock()

	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("restart-policy drift error = %v", err)
	}
	if got, want := commandKinds(daemon.requests()[before:]), []string{"info --format", "network inspect", "container inspect"}; !sameStrings(got, want) {
		t.Fatalf("commands after restart-policy drift = %#v, want read-only %#v", got, want)
	}
}

func TestBuilderManagerRefusesHardQuotaMountDriftBeforeBuilderMutation(t *testing.T) {
	for name, mutate := range map[string]func(*buildkitContainer){
		"configured size": func(container *buildkitContainer) {
			container.HostConfig.Mounts[0].TmpfsOptions.SizeBytes--
		},
		"active type": func(container *buildkitContainer) {
			container.Mounts[0].Type = "volume"
		},
		"additional mount": func(container *buildkitContainer) {
			container.Mounts = append(container.Mounts, dockerMount{Type: "bind", Source: "C:\\private", Destination: "/private", RW: true})
		},
	} {
		t.Run(name, func(t *testing.T) {
			daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
			manager := newBuilderManagerForTest(t, daemon)
			session, err := manager.Prepare(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			daemon.mu.Lock()
			identity := daemon.nodes[session.BuilderName]
			container := daemon.boxes[buildkitContainerName(identity)]
			mutate(&container)
			daemon.boxes[buildkitContainerName(identity)] = container
			before := len(daemon.calls)
			daemon.mu.Unlock()

			_, err = manager.Prepare(context.Background())
			if !IsBuilderError(err, BuilderDriftDetected) {
				t.Fatalf("mount drift error = %v", err)
			}
			if got, want := commandKinds(daemon.requests()[before:]), []string{"info --format", "network inspect", "container inspect"}; !sameStrings(got, want) {
				t.Fatalf("commands after mount drift = %#v, want read-only %#v", got, want)
			}
		})
	}
}

func TestBuilderManagerFailsClosedWhenHardQuotaIsUnsupported(t *testing.T) {
	t.Run("non Linux Docker engine", func(t *testing.T) {
		daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder), osType: "windows"}
		manager := newBuilderManagerForTest(t, daemon)
		_, err := manager.Prepare(context.Background())
		if !IsBuilderError(err, BuilderHardQuotaUnavailable) {
			t.Fatalf("unsupported platform error = %v", err)
		}
		if got, want := commandKinds(daemon.requests()), []string{"info --format"}; !sameStrings(got, want) {
			t.Fatalf("unsupported platform commands = %#v, want %#v", got, want)
		}
	})

	for name, mutate := range map[string]func(*dockerInfo){
		"memory controls": func(info *dockerInfo) { info.MemoryLimit = false },
		"swap controls":   func(info *dockerInfo) { info.SwapLimit = false },
		"CPU period":      func(info *dockerInfo) { info.CPUCfsPeriod = false },
		"CPU quota":       func(info *dockerInfo) { info.CPUCfsQuota = false },
		"PID controls":    func(info *dockerInfo) { info.PidsLimit = false },
		"warning":         func(info *dockerInfo) { info.Warnings = []string{"WARNING: No swap limit support"} },
	} {
		t.Run(name, func(t *testing.T) {
			info := validDockerInfo()
			mutate(&info)
			daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder), infoOverride: &info}
			manager := newBuilderManagerForTest(t, daemon)
			_, err := manager.Prepare(context.Background())
			if !IsBuilderError(err, BuilderHardQuotaUnavailable) {
				t.Fatalf("unsupported controller error = %v", err)
			}
			if got, want := commandKinds(daemon.requests()), []string{"info --format"}; !sameStrings(got, want) {
				t.Fatalf("unsupported controller commands = %#v, want %#v", got, want)
			}
		})
	}

	t.Run("tmpfs creation rejected", func(t *testing.T) {
		daemon := &builderDaemonFake{
			builders: make(map[string]buildxBuilder), containerRunErr: errors.New("exit status 1"),
			containerRunStderr: []byte("tmpfs mounts are not supported by this runtime"),
		}
		manager := newBuilderManagerForTest(t, daemon)
		_, err := manager.Prepare(context.Background())
		if !IsBuilderError(err, BuilderHardQuotaUnavailable) {
			t.Fatalf("unsupported tmpfs error = %v", err)
		}
		for _, request := range daemon.requests() {
			if len(request.Args) >= 2 && request.Args[0] == "buildx" {
				t.Fatalf("Buildx mutated before quota establishment: %#v", request.Args)
			}
		}
	})
}

func TestBuilderManagerClassifiesHardQuotaExhaustion(t *testing.T) {
	daemon := &builderDaemonFake{
		builders: make(map[string]buildxBuilder), containerRunErr: errors.New("exit status 1"),
		containerRunStderr: []byte("failed to mount tmpfs: no space left on device"),
	}
	manager := newBuilderManagerForTest(t, daemon)
	_, err := manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderHardQuotaExhausted) {
		t.Fatalf("quota exhaustion error = %v", err)
	}
}

func TestBuilderManagerMapsOnlyRecognizedQuotaFailures(t *testing.T) {
	for name, test := range map[string]struct {
		stderr string
		want   BuilderErrorCode
	}{
		"unsupported tmpfs":      {stderr: "tmpfs mounts are not supported by this runtime", want: BuilderHardQuotaUnavailable},
		"invalid tmpfs mount":    {stderr: `invalid mount config for type "tmpfs"`, want: BuilderHardQuotaUnavailable},
		"generic Docker failure": {stderr: "container creation failed", want: BuilderProvisionFailed},
	} {
		t.Run(name, func(t *testing.T) {
			daemon := &builderDaemonFake{
				builders: make(map[string]buildxBuilder), containerRunErr: errors.New("exit status 1"),
				containerRunStderr: []byte(test.stderr),
			}
			manager := newBuilderManagerForTest(t, daemon)
			_, err := manager.Prepare(context.Background())
			if !IsBuilderError(err, test.want) {
				t.Fatalf("quota provisioning error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestBuilderManagerRecoversExactOwnedContainerLifecycle(t *testing.T) {
	for name, mutate := range map[string]func(*buildkitContainer){
		"stopped":    func(container *buildkitContainer) { container.State.Running = false },
		"paused":     func(container *buildkitContainer) { container.State.Paused = true },
		"restarting": func(container *buildkitContainer) { container.State.Restarting = true },
	} {
		t.Run(name, func(t *testing.T) {
			daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
			manager := newBuilderManagerForTest(t, daemon)
			session, err := manager.Prepare(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			daemon.mu.Lock()
			identity := daemon.nodes[session.BuilderName]
			containerName := buildkitContainerName(identity)
			container := daemon.boxes[containerName]
			mutate(&container)
			daemon.boxes[containerName] = container
			before := len(daemon.calls)
			daemon.mu.Unlock()

			if _, err := manager.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			requests := daemon.requests()[before:]
			wantAction := map[string]string{"stopped": "container start", "paused": "container unpause", "restarting": "container restart"}[name]
			if got := commandKinds(requests); len(got) < 6 || got[3] != wantAction || got[4] != "container inspect" {
				t.Fatalf("recovery commands = %#v, want verified %q followed by inspect", got, wantAction)
			}
			if target := requests[3].Args[len(requests[3].Args)-1]; target != container.ID {
				t.Fatalf("recovery target = %q, want exact container ID %q", target, container.ID)
			}
		})
	}
}

func TestBuilderManagerRefusesLifecycleRecoveryWhenOwnershipDrifts(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	identity := daemon.nodes[session.BuilderName]
	containerName := buildkitContainerName(identity)
	container := daemon.boxes[containerName]
	container.State.Running = false
	container.Config.Labels["rig.builder"] = "rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa"
	daemon.boxes[containerName] = container
	before := len(daemon.calls)
	daemon.mu.Unlock()

	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("ownership drift error = %v", err)
	}
	if got, want := commandKinds(daemon.requests()[before:]), []string{"info --format", "network inspect", "container inspect"}; !sameStrings(got, want) {
		t.Fatalf("commands after lifecycle ownership drift = %#v, want read-only %#v", got, want)
	}
}

func TestBuilderManagerRefusesContainerReplacementDuringLifecycleRecovery(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	identity := daemon.nodes[session.BuilderName]
	containerName := buildkitContainerName(identity)
	container := daemon.boxes[containerName]
	container.State.Running = false
	daemon.boxes[containerName] = container
	daemon.replaceOnLifecycle = true
	daemon.mu.Unlock()

	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("replacement race error = %v", err)
	}
}

func TestBuilderManagerBlocksLegacyV1StateBeforeDockerMutation(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	legacy := []byte(`{"schema":1,"builderName":"rig-buildkit-aaaaaaaaaaaaaaaaaaaaaaaa","nodeName":"rig-node-aaaaaaaaaaaaaaaaaaaaaaaa","networkName":"rig-buildnet-aaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if err := manager.directory.WriteNewFile(legacyBuilderIdentityFilename, legacy); err != nil {
		t.Fatal(err)
	}
	clear(legacy)

	_, err := manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("legacy identity error = %v", err)
	}
	if requests := daemon.requests(); len(requests) != 0 {
		t.Fatalf("Docker was contacted for legacy state: %#v", requests)
	}
	if _, err := manager.directory.ReadFile(builderIdentityFilename, 8<<10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v2 identity created beside legacy state: %v", err)
	}
}

func TestBuilderManagerRefusesRemoteEndpointDrift(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	session, err := manager.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	daemon.mu.Lock()
	builder := daemon.builders[session.BuilderName]
	builder.Nodes[0].Endpoint = "tcp://127.0.0.1:1234"
	daemon.builders[session.BuilderName] = builder
	before := len(daemon.calls)
	daemon.mu.Unlock()

	_, err = manager.Prepare(context.Background())
	if !IsBuilderError(err, BuilderDriftDetected) {
		t.Fatalf("endpoint drift error = %v", err)
	}
	for _, request := range daemon.requests()[before:] {
		if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] != "ls" {
			t.Fatalf("drifted endpoint was used or replaced: %#v", request.Args)
		}
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

func TestBuilderManagerFailsClosedOnAmbiguousMissingInspections(t *testing.T) {
	exitErr := completedBuilderExitError()
	var completedExit *exec.ExitError
	if !errors.As(exitErr, &completedExit) || completedExit.ProcessState == nil || completedExit.ExitCode() != 1 {
		t.Fatal("builder test helper did not provide a completed exit-one process")
	}
	for _, resource := range []string{"network", "container"} {
		for _, ambiguity := range []string{"transport", "cancellation", "truncation", "termination", "joined exit", "wrapped exit"} {
			t.Run(resource+"_"+ambiguity, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				runner := builderRunnerFunc(func(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
					name := request.Args[len(request.Args)-1]
					message := "Error response from daemon: network " + name + " not found"
					if resource == "container" {
						message = "Error: No such container: " + name
					}
					result := runtimeprocess.CommandResult{Stderr: []byte(message)}
					err := error(errors.New("ambiguous Docker transport failure"))
					switch ambiguity {
					case "cancellation":
						cancel()
						err = exitErr
					case "truncation":
						result.StderrTruncated = true
						err = exitErr
					case "termination":
						err = errors.Join(exitErr, runtimeprocess.ErrTerminationFailed)
					case "joined exit":
						err = errors.Join(exitErr, errors.New("Docker transport failed after process exit"))
					case "wrapped exit":
						err = fmt.Errorf("Docker client wrapper: %w", exitErr)
					}
					return result, err
				})
				manager := newBuilderManagerForTest(t, runner)
				identity, env, err := manager.preparePersistentState()
				if err != nil {
					t.Fatal(err)
				}
				found := true
				if resource == "network" {
					_, found, err = manager.inspectNetwork(ctx, identity.NetworkName, env)
				} else {
					_, found, err = manager.inspectBuildkitContainer(ctx, identity, env)
				}
				if err == nil || found {
					t.Fatalf("ambiguous missing inspection was accepted: found=%t error=%v", found, err)
				}
				if ambiguity == "cancellation" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause lost: %v", err)
				}
				if ambiguity == "truncation" && !IsBuilderError(err, BuilderOutputTruncated) {
					t.Fatalf("truncation classification=%v", err)
				}
				if ambiguity == "termination" && !errors.Is(err, runtimeprocess.ErrTerminationFailed) {
					t.Fatalf("termination cause lost: %v", err)
				}
			})
		}
	}
}

func TestBuilderManagerQuotaObservationRejectsUncertainResults(t *testing.T) {
	for _, test := range []struct {
		name string
		code BuilderErrorCode
		run  func(context.CancelFunc) (runtimeprocess.CommandResult, error)
	}{
		{name: "truncated", code: BuilderOutputTruncated, run: func(context.CancelFunc) (runtimeprocess.CommandResult, error) {
			body, _ := json.Marshal(validDockerInfo())
			return runtimeprocess.CommandResult{Stdout: body, StdoutTruncated: true}, nil
		}},
		{name: "termination", code: BuilderTerminationFailed, run: func(context.CancelFunc) (runtimeprocess.CommandResult, error) {
			return runtimeprocess.CommandResult{}, runtimeprocess.ErrTerminationFailed
		}},
		{name: "cancellation after read", code: BuilderCancelled, run: func(cancel context.CancelFunc) (runtimeprocess.CommandResult, error) {
			body, _ := json.Marshal(validDockerInfo())
			cancel()
			return runtimeprocess.CommandResult{Stdout: body}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := builderRunnerFunc(func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				return test.run(cancel)
			})
			manager := newBuilderManagerForTest(t, runner)
			err := manager.verifyQuotaPlatform(ctx, nil)
			if !IsBuilderError(err, test.code) {
				t.Fatalf("quota observation error=%v want=%s", err, test.code)
			}
		})
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

func TestBuilderManagerRevalidatesAuthorityBeforeEveryMutation(t *testing.T) {
	for _, test := range []struct {
		name      string
		command   string
		configure func(*builderDaemonFake, builderIdentity)
	}{
		{name: "network create", command: "network create"},
		{name: "buildkit run", command: "container run", configure: func(daemon *builderDaemonFake, identity builderIdentity) {
			daemon.network = validBuilderNetwork(identity)
		}},
		{name: "buildkit start", command: "container start", configure: func(daemon *builderDaemonFake, identity builderIdentity) {
			daemon.network = validBuilderNetwork(identity)
			container := validBuildkitContainer(identity, defaultStateQuotaBytes)
			container.State.Running = false
			daemon.boxes[buildkitContainerName(identity)] = container
		}},
		{name: "buildkit restart", command: "container restart", configure: func(daemon *builderDaemonFake, identity builderIdentity) {
			daemon.network = validBuilderNetwork(identity)
			container := validBuildkitContainer(identity, defaultStateQuotaBytes)
			container.State.Restarting = true
			daemon.boxes[buildkitContainerName(identity)] = container
		}},
		{name: "buildkit unpause", command: "container unpause", configure: func(daemon *builderDaemonFake, identity builderIdentity) {
			daemon.network = validBuilderNetwork(identity)
			container := validBuildkitContainer(identity, defaultStateQuotaBytes)
			container.State.Paused = true
			daemon.boxes[buildkitContainerName(identity)] = container
		}},
		{name: "buildx create", command: "buildx create", configure: func(daemon *builderDaemonFake, identity builderIdentity) {
			daemon.network = validBuilderNetwork(identity)
			daemon.boxes[buildkitContainerName(identity)] = validBuildkitContainer(identity, defaultStateQuotaBytes)
		}},
		{name: "builder bootstrap", command: "buildx inspect", configure: func(daemon *builderDaemonFake, identity builderIdentity) {
			daemon.network = validBuilderNetwork(identity)
			daemon.boxes[buildkitContainerName(identity)] = validBuildkitContainer(identity, defaultStateQuotaBytes)
			daemon.builders[identity.BuilderName] = buildxBuilder{Name: identity.BuilderName, Driver: "remote", Nodes: []buildxNode{{Name: identity.NodeName, Endpoint: buildkitRemoteEndpoint(identity)}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder), boxes: make(map[string]buildkitContainer)}
			manager := newBuilderManagerForTest(t, daemon)
			identity, _, err := manager.preparePersistentState()
			if err != nil {
				t.Fatal(err)
			}
			if test.configure != nil {
				test.configure(daemon, identity)
			}
			beforeArtifact, ok := manager.execution.SelectedArtifact()
			if !ok {
				t.Fatal("selected artifact unavailable before builder check")
			}
			state := generatedruntimestate.New(manager.authority.db)
			beforeComponent, err := state.Component(context.Background(), manager.authority.claim.ApplicationID(), manager.authority.request.DeploymentID, manager.authority.request.ComponentName)
			if err != nil {
				t.Fatal(err)
			}

			barrierCalls := 0
			manager.afterCommandPrepared = func(request runtimeprocess.CommandRequest) {
				if commandKindForBuilderTest(request) != test.command {
					return
				}
				barrierCalls++
				invalidateGeneratedImageJobAttempt(t, manager.authority)
			}
			_, err = manager.Prepare(context.Background())
			if !errors.Is(err, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("authority refusal=%v", err)
			}
			if barrierCalls != 1 {
				t.Fatalf("prepared-command barrier calls=%d", barrierCalls)
			}
			for _, request := range daemon.requests() {
				if commandKindForBuilderTest(request) == test.command {
					t.Fatalf("refused mutation entered runner: %#v", request.Args)
				}
			}
			afterArtifact, err := NewArtifactRepository(manager.authority.db).Get(context.Background(), beforeArtifact.ID)
			if err != nil || afterArtifact != beforeArtifact {
				t.Fatalf("artifact changed after refusal: before=%#v after=%#v err=%v", beforeArtifact, afterArtifact, err)
			}
			afterComponent, err := state.Component(context.Background(), manager.authority.claim.ApplicationID(), manager.authority.request.DeploymentID, manager.authority.request.ComponentName)
			if err != nil || afterComponent != beforeComponent {
				t.Fatalf("component changed after refusal: before=%#v after=%#v err=%v", beforeComponent, afterComponent, err)
			}
		})
	}
}

func TestBuilderManagerReconcilesExactLostAcknowledgementsUnderFreshAuthority(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*builderDaemonFake)
		command   string
	}{
		{name: "buildkit run", command: "container run", configure: func(daemon *builderDaemonFake) {
			daemon.containerRunErr = errors.New("container run acknowledgement lost")
			daemon.containerRunLostAck = true
		}},
		{name: "buildx create", command: "buildx create", configure: func(daemon *builderDaemonFake) {
			daemon.buildxCreateLostAck = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
			test.configure(daemon)
			manager := newBuilderManagerForTest(t, daemon)
			session, err := manager.Prepare(context.Background())
			if err != nil || !validBuilderSession(session) {
				t.Fatalf("lost acknowledgement session=%#v error=%v", session, err)
			}
			entries := 0
			for _, request := range daemon.requests() {
				if commandKindForBuilderTest(request) == test.command {
					entries++
				}
			}
			if entries != 1 {
				t.Fatalf("mutation entries=%d want=1; commands=%#v", entries, commandKinds(daemon.requests()))
			}
		})
	}
}

func TestBuilderManagerStopsBeforeNextMutationWhenAuthorityChangesAfterPriorCommand(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	manager := newBuilderManagerForTest(t, daemon)
	manager.afterCommandPrepared = func(request runtimeprocess.CommandRequest) {
		if commandKindForBuilderTest(request) == "container run" {
			invalidateGeneratedImageJobAttempt(t, manager.authority)
		}
	}
	_, err := manager.Prepare(context.Background())
	if !errors.Is(err, deploymentexecution.ErrIdentityLost) {
		t.Fatalf("authority refusal=%v", err)
	}
	if daemon.network == nil {
		t.Fatal("prior network create did not complete")
	}
	if len(daemon.boxes) != 0 {
		t.Fatalf("next mutation entered runner: %#v", daemon.boxes)
	}
}

func TestBuilderManagerCommandPinKeepsOriginalScopeWithoutHoldingSQL(t *testing.T) {
	daemon := &builderDaemonFake{builders: make(map[string]buildxBuilder)}
	blocking := &blockingBuilderRunner{delegate: daemon, target: "network create", entered: make(chan struct{}), release: make(chan struct{})}
	manager := newBoundBuilderManager(t, blocking, BuilderManagerOptions{DataRoot: t.TempDir(), DockerExecutable: filepath.Join(t.TempDir(), "docker.exe"), PrepareTimeout: time.Minute})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prepareDone := make(chan error, 1)
	go func() {
		_, err := manager.Prepare(ctx)
		prepareDone <- err
	}()
	select {
	case <-blocking.entered:
	case <-ctx.Done():
		t.Fatalf("builder command did not enter: %v", ctx.Err())
	}
	released := false
	defer func() {
		if !released {
			close(blocking.release)
		}
	}()

	manager.execution.Leave()
	releaseStarted := make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() {
		close(releaseStarted)
		releaseDone <- manager.authority.releaseScope()
	}()
	<-releaseStarted
	for {
		probe, err := manager.authority.repository.PinGeneratedImage(manager.authority.context, manager.authority.request)
		if errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			break
		}
		if err != nil {
			t.Fatalf("scope-closing probe error=%v", err)
		}
		probe.Leave()
		select {
		case <-ctx.Done():
			t.Fatalf("scope release did not enter closing state: %v", ctx.Err())
		default:
			runtime.Gosched()
		}
	}
	select {
	case err := <-releaseDone:
		t.Fatalf("scope released while command pin was active: %v", err)
	default:
	}

	writerDone := make(chan error, 1)
	go func() {
		_, err := manager.authority.db.ExecContext(ctx, `UPDATE applications SET updated_at=updated_at WHERE id=?`, manager.authority.claim.ApplicationID())
		writerDone <- err
	}()
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("independent writer failed while Docker command blocked: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("independent writer blocked behind Docker command: %v", ctx.Err())
	}

	close(blocking.release)
	released = true
	select {
	case err := <-prepareDone:
		if err != nil {
			t.Fatalf("builder preparation after unblock: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("builder did not finish after unblock: %v", ctx.Err())
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatalf("release scope after command: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("scope remained pinned after builder return: %v", ctx.Err())
	}
}

func newBuilderManagerForTest(t *testing.T, runner runtimeprocess.CommandRunner) *boundBuilderManager {
	t.Helper()
	return newBoundBuilderManager(t, runner, BuilderManagerOptions{
		DataRoot: t.TempDir(), DockerExecutable: filepath.Join(t.TempDir(), "docker"), PrepareTimeout: time.Minute,
	})
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

func validBuildkitContainer(identity builderIdentity, quotaBytes int64) buildkitContainer {
	var container buildkitContainer
	container.ID = strings.Repeat("b", 64)
	container.Name = "/" + buildkitContainerName(identity)
	container.Image = "sha256:" + strings.Repeat("a", 64)
	container.State.Running = true
	container.Config.Image = buildkitImage
	container.Config.Cmd = buildkitCommand(quotaBytes)
	container.Config.Labels = map[string]string{
		"rig.controller": "generated-builder", "rig.builder": identity.BuilderName,
		"rig.network": identity.NetworkName, "rig.quota.bytes": fmt.Sprintf("%d", quotaBytes),
	}
	container.HostConfig.Memory = buildkitMemoryLimit(quotaBytes)
	container.HostConfig.MemorySwap = buildkitMemoryLimit(quotaBytes)
	container.HostConfig.CPUPeriod = 100000
	container.HostConfig.CPUQuota = 100000
	container.HostConfig.PidsLimit = buildkitPIDsLimit
	container.HostConfig.Privileged = true
	container.HostConfig.NetworkMode = identity.NetworkName
	container.HostConfig.RestartPolicy.Name = "unless-stopped"
	container.HostConfig.LogConfig.Type = "json-file"
	container.HostConfig.LogConfig.Config = map[string]string{"max-size": "10m", "max-file": "1"}
	tmpfs := &struct {
		SizeBytes int64 `json:"SizeBytes"`
		Mode      int64 `json:"Mode"`
	}{SizeBytes: quotaBytes, Mode: 0o700}
	container.HostConfig.Mounts = []dockerConfiguredMount{{Type: "tmpfs", Target: buildkitStatePath, TmpfsOptions: tmpfs}}
	container.NetworkSettings.Networks = map[string]json.RawMessage{identity.NetworkName: json.RawMessage(`{}`)}
	container.Mounts = []dockerMount{{Type: "tmpfs", Destination: buildkitStatePath, RW: true}}
	return container
}

func validBuilderNetwork(identity builderIdentity) *dockerNetwork {
	return &dockerNetwork{
		Name: identity.NetworkName, Driver: "bridge", Scope: "local",
		Labels:  map[string]string{"rig.controller": "generated-builder", "rig.builder": identity.BuilderName, "rig.network": identity.NetworkName},
		Options: map[string]string{"com.docker.network.bridge.enable_icc": "false"},
	}
}

func commandKindForBuilderTest(request runtimeprocess.CommandRequest) string {
	if len(request.Args) < 2 {
		return ""
	}
	return request.Args[0] + " " + request.Args[1]
}

func validDockerInfo() dockerInfo {
	return dockerInfo{
		OSType: "linux", MemoryLimit: true, SwapLimit: true,
		CPUCfsPeriod: true, CPUCfsQuota: true, PidsLimit: true,
	}
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
