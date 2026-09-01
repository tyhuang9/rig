package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveNodeImage = "node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5"

type liveEnvironmentStager struct{}

func (liveEnvironmentStager) Stage(string, int, []byte) (generatedruntime.EnvironmentLease, error) {
	return nil, errors.New("live lifecycle test does not stage configuration")
}

type liveCapacitySource struct{}

func (liveCapacitySource) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 8 << 30}, nil
}

func TestLiveGeneratedBlueGreenLifecycle(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_GENERATED_RUNTIME") != "1" {
		t.Skip("set RIG_RUN_LIVE_GENERATED_RUNTIME=1 on a disposable Linux Docker host")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker executable is unavailable")
	}
	docker, err = filepath.Abs(docker)
	if err != nil {
		t.Fatal("resolve docker executable")
	}
	root := t.TempDir()
	dockerConfig := filepath.Join(root, "docker-config")
	working := filepath.Join(root, "working")
	state := filepath.Join(root, "ingress-state")
	for _, directory := range []string{dockerConfig, working, state} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create live lifecycle directory: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if output, err := dockerCommand(ctx, docker, root, "info"); err != nil {
		t.Fatalf("docker daemon unavailable: %v: %s", err, output)
	}

	appID := "11111111-1111-4111-8111-111111111111"
	planID := "22222222-2222-4222-8222-222222222222"
	network, err := generatedruntime.DescribeAppNetwork(appID)
	if err != nil {
		t.Fatal(err)
	}
	hostPort := freeLoopbackPort(t)
	imageTags := []string{"rig-generated-live-test:blue", "rig-generated-live-test:green"}
	defer cleanupLiveDocker(t, docker, root, network.Name, imageTags)

	runner := runtimeprocess.ExecRunner{}
	limits := generatedruntime.ContainerLimits{
		MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 128,
		TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 1,
	}
	engine, err := generatedruntime.NewEngine(runner, liveEnvironmentStager{}, liveCapacitySource{}, generatedruntime.EngineOptions{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		CommandTimeout: 45 * time.Second, HealthTimeout: 90 * time.Second, HealthPollInterval: 250 * time.Millisecond,
		OutputLimit: 64 << 10, Limits: limits, ReplacementDiskBytes: 96 << 20,
	})
	if err != nil {
		t.Fatalf("create generated runtime: %v", err)
	}
	ingress, err := New(runner, Options{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		DataRoot: state, HostPort: hostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: 64 << 10,
	})
	if err != nil {
		t.Fatalf("create generated ingress: %v", err)
	}

	blueSpec := liveCandidateSpec("blue", appID, planID)
	blueSpec.ImageContentID = buildLiveImage(t, ctx, docker, root, imageTags[0], blueSpec, "blue")
	blue := startLiveCandidate(t, ctx, engine, blueSpec)
	defer func() { _ = engine.StopAndRemove(context.Background(), blue, 0) }()
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{
		AppID: appID, ToSlot: blue.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(blue)},
	}); err != nil {
		t.Fatalf("route first slot: %v", err)
	}
	assertLiveResponse(t, hostPort, appID, "blue")
	engine.ReleaseAdmission(blue)

	greenSpec := liveCandidateSpec("green", appID, planID)
	greenSpec.ActiveSlot = blue.Slot
	greenSpec.ImageContentID = buildLiveImage(t, ctx, docker, root, imageTags[1], greenSpec, "green")
	green := startLiveCandidate(t, ctx, engine, greenSpec)
	defer func() { _ = engine.StopAndRemove(context.Background(), green, 0) }()

	// A healthy inactive candidate must not receive traffic before the route commit.
	assertLiveResponse(t, hostPort, appID, "blue")
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{
		AppID: appID, FromSlot: blue.Slot, ToSlot: green.Slot,
		Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(green)}, DrainPeriod: 100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("switch to replacement slot: %v", err)
	}
	assertLiveResponse(t, hostPort, appID, "green")
	if err := engine.StopAndRemove(ctx, blue, time.Second); err != nil {
		t.Fatalf("remove drained slot: %v", err)
	}
	assertLiveResponse(t, hostPort, appID, "green")
	engine.ReleaseAdmission(green)
}

func liveCandidateSpec(version, appID, planID string) generatedruntime.CandidateSpec {
	identities := map[string][]string{
		"blue":  {"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"},
		"green": {"66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888"},
	}
	values := identities[version]
	return generatedruntime.CandidateSpec{
		AppID: appID, ReleaseID: values[0], DeploymentID: values[1], ArtifactID: values[2],
		DeploymentPlanRevisionID: planID, ComponentName: "api", Role: generatedruntime.RoleServer,
		RootDirectory: ".", RunCommand: "node server.mjs", InternalPort: 3000, HealthProbe: "/health",
		BuildDefinitionDigest: strings.Repeat(map[string]string{"blue": "a", "green": "b"}[version], 64),
	}
}

func buildLiveImage(t *testing.T, ctx context.Context, docker, root, tag string, spec generatedruntime.CandidateSpec, version string) string {
	t.Helper()
	contextRoot := filepath.Join(root, "image-"+version)
	if err := os.Mkdir(contextRoot, 0o700); err != nil {
		t.Fatalf("create image context: %v", err)
	}
	containerfile := fmt.Sprintf("FROM %s\nWORKDIR /workspace\nCOPY --chmod=0555 rig-entrypoint /usr/local/bin/rig-entrypoint\nCOPY --chown=node:node server.mjs /workspace/server.mjs\nUSER node\nENTRYPOINT [\"/usr/local/bin/rig-entrypoint\"]\n", liveNodeImage)
	entrypoint := "#!/bin/sh\nset -eu\nexec \"$@\"\n"
	server := fmt.Sprintf("import { createServer } from 'node:http';\nconst version = %q;\nconst port = Number(process.env.RIG_RUNTIME_INTERNAL_PORT);\ncreateServer((request, response) => { response.writeHead(200, { 'content-type': 'text/plain' }); response.end(version); }).listen(port, '0.0.0.0');\n", version)
	for name, contents := range map[string]string{"Dockerfile": containerfile, "rig-entrypoint": entrypoint, "server.mjs": server} {
		if err := os.WriteFile(filepath.Join(contextRoot, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write image fixture: %v", err)
		}
	}
	labels := map[string]string{
		"io.rig.managed": "generated-image", "io.rig.application": spec.AppID,
		"io.rig.release": spec.ReleaseID, "io.rig.artifact": spec.ArtifactID,
		"io.rig.plan": spec.DeploymentPlanRevisionID, "io.rig.component": spec.ComponentName,
		"io.rig.role": spec.Role, "io.rig.definition": spec.BuildDefinitionDigest,
	}
	args := []string{"build", "--pull", "--quiet", "--tag", tag}
	for _, key := range []string{"io.rig.managed", "io.rig.application", "io.rig.release", "io.rig.artifact", "io.rig.plan", "io.rig.component", "io.rig.role", "io.rig.definition"} {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, contextRoot)
	if output, err := dockerCommand(ctx, docker, root, args...); err != nil {
		t.Fatalf("build %s image: %v: %s", version, err, output)
	}
	output, err := dockerCommand(ctx, docker, root, "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		t.Fatalf("inspect %s image: %v: %s", version, err, output)
	}
	return strings.TrimSpace(output)
}

func startLiveCandidate(t *testing.T, ctx context.Context, engine *generatedruntime.Engine, spec generatedruntime.CandidateSpec) generatedruntime.Candidate {
	t.Helper()
	candidate, err := engine.CreateInactiveCandidate(ctx, spec)
	if err != nil {
		t.Fatalf("create %s candidate: %v", spec.ReleaseID, err)
	}
	if err := engine.StartCandidate(ctx, candidate); err != nil {
		t.Fatalf("start %s candidate: %v", spec.ReleaseID, err)
	}
	if err := engine.WaitHealthy(ctx, candidate); err != nil {
		t.Fatalf("wait for %s candidate: %v", spec.ReleaseID, err)
	}
	return candidate
}

func liveEndpoint(candidate generatedruntime.Candidate) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{
		Component: candidate.Component, Role: candidate.Role, ContainerID: candidate.ContainerID,
		NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort,
	}
}

func assertLiveResponse(t *testing.T, port uint16, appID, expected string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = appID + ".rig.localhost"
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 64))
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && string(body) == expected {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("generated ingress did not serve %q", expected)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func freeLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return uint16(port)
}

func cleanupLiveDocker(t *testing.T, docker, root, appNetwork string, imageTags []string) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	commands := [][]string{
		{"container", "rm", "--force", caddyContainerName},
		{"network", "rm", appNetwork, caddyNetworkName},
		{"volume", "rm", "--force", caddyVolumeName},
		{"image", "rm", "--force", imageTags[0], imageTags[1]},
	}
	for _, args := range commands {
		_, _ = dockerCommand(cleanupCtx, docker, root, args...)
	}
}

func dockerCommand(ctx context.Context, docker, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, docker, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
