package generatedimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	network            *dockerNetwork
	builders           map[string]buildxBuilder
	nodes              map[string]builderIdentity
	boxes              map[string]buildkitContainer
	calls              []runtimeprocess.CommandRequest
	err                error
	stderr             []byte
	outputs            [][]byte
	osType             string
	containerRunErr    error
	containerRunStderr []byte
	infoOverride       *dockerInfo
	replaceOnLifecycle bool
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
			endpoint := request.Args[len(request.Args)-1]
			d.builders[name] = buildxBuilder{Name: name, Driver: flagArgument(request.Args, "--driver"), Nodes: []buildxNode{{Name: flagArgument(request.Args, "--node"), Endpoint: endpoint}}}
			d.nodes[name] = builderIdentity{BuilderName: name, NodeName: flagArgument(request.Args, "--node")}
			output := []byte(name)
			d.outputs = append(d.outputs, output)
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
				output := []byte("Error response from daemon: No such container")
				d.outputs = append(d.outputs, output)
				return runtimeprocess.CommandResult{Stderr: output}, errors.New("exit status 1")
			}
			body, _ := json.Marshal(container)
			d.outputs = append(d.outputs, body)
			return runtimeprocess.CommandResult{Stdout: body}, nil
		case "run":
			if d.containerRunErr != nil {
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
