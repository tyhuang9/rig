//go:build live_docker

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

const nextLiveBaseImage = "node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e"

type nextLiveCapacity struct{}

func (nextLiveCapacity) Snapshot(context.Context) (generatedruntime.CapacitySnapshot, error) {
	return generatedruntime.CapacitySnapshot{MemoryAvailableBytes: 8 << 30, DiskAvailableBytes: 16 << 30}, nil
}

// This gate deliberately exercises the production runtime and ingress with a
// separately built fixture image. Compiler and durable-controller journeys have
// their own hosted gates; this test must not be reported as covering either.
func TestLiveNextCacheRuntimeRoute(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_NEXT_CACHE") != "1" {
		t.Fatal("set RIG_RUN_LIVE_NEXT_CACHE=1 to run the hosted Docker gate")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("Next cache gate requires the local Linux Docker daemon")
	}
	docker := controllerJourneyExecutable(t, "docker")
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Minute)
	defer cancel()
	if _, err := controllerJourneyDocker(ctx, docker, nil, "info"); err != nil {
		t.Fatal("Docker daemon unavailable")
	}
	root := t.TempDir()
	dockerConfig, working, ingressState, dataRoot := filepath.Join(root, "docker-config"), filepath.Join(root, "working"), filepath.Join(root, "ingress"), filepath.Join(root, "data")
	for _, directory := range []string{dockerConfig, working, ingressState, dataRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	appID, releaseID, artifactID, planID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	network, err := generatedruntime.DescribeAppNetwork(appID)
	if err != nil {
		t.Fatal(err)
	}
	if controllerJourneyExists(t, ctx, docker, "container", "rig-generated-caddy-v1") ||
		controllerJourneyExists(t, ctx, docker, "volume", "rig-generated-caddy-config-v1") ||
		controllerJourneyExists(t, ctx, docker, "network", "rig-generated-caddy-ingress-v1") {
		t.Fatal("disposable Docker daemon already contains generated ingress")
	}
	tag := "rig-live-nextjs:" + uuid.NewString()
	t.Cleanup(func() { nextLiveCleanup(t, docker, appID, network.Name, tag) })
	imageID := nextLiveBuildImage(t, ctx, docker, root, tag, appID, releaseID, artifactID, planID)
	staging, err := securetemp.NewGeneratedRuntime(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := generatedruntime.NewSecureEnvironmentStager(staging)
	if err != nil {
		t.Fatal(err)
	}
	runner := runtimeprocess.ExecRunner{}
	engine, err := generatedruntime.NewEngine(runner, environment, nextLiveCapacity{}, generatedruntime.EngineOptions{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		CommandTimeout: 45 * time.Second, HealthTimeout: 90 * time.Second, HealthPollInterval: 250 * time.Millisecond,
		Limits:               generatedruntime.ContainerLimits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "1m", LogFiles: 2},
		ReplacementDiskBytes: 512 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(controllerJourneyPort(t, "127.0.0.1"))
	ingress, err := generatedingress.New(runner, generatedingress.Options{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		DataRoot: ingressState, HostPort: port, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := sha256.Sum256([]byte("reviewed-nextjs-cache-fixture-v1"))
	spec := generatedruntime.CandidateSpec{
		AppID: appID, ReleaseID: releaseID, DeploymentID: uuid.NewString(), ArtifactID: artifactID,
		DeploymentPlanRevisionID: planID, ComponentName: "web", Role: generatedruntime.RoleServer, Technology: "nextjs",
		RootDirectory: ".", RunCommand: "npm run start", InternalPort: 3000, HealthProbe: "/api/health",
		ImageContentID: imageID, BuildDefinitionDigest: fmt.Sprintf("%x", definition),
	}
	// The engine clears its own input buffer after staging. Keep only synthetic
	// identifiers in assertions and never log Docker configuration or response bodies.
	firstSecret, secondSecret := "next-runtime-"+uuid.NewString(), "next-runtime-"+uuid.NewString()
	spec.EnvironmentOperationID, spec.EnvironmentOperationAttempt = uuid.NewString(), 1
	spec.Environment = []byte("RIG_FIXTURE_RUNTIME_SECRET=" + firstSecret + "\nRIG_FIXTURE_SLOT_MARKER=blue\n")
	blue := nextLiveStart(t, ctx, engine, spec)
	defer func() { _ = engine.StopAndRemove(context.Background(), blue, 0) }()
	assertNextLiveContainer(t, ctx, docker, blue, firstSecret)
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{AppID: appID, ToSlot: blue.Slot, Endpoints: []generatedruntime.RouteEndpoint{nextLiveEndpoint(blue)}}); err != nil {
		t.Fatal("route initial Next server:", err)
	}
	nextLiveAttestedEndpoint(t, ctx, ingress, appID, blue)
	base := "http://127.0.0.1:" + strconv.Itoa(int(port))
	nextLiveRequest(t, ctx, base, appID, "/", http.StatusOK, "next-public-A", firstSecret)
	nextLiveRequest(t, ctx, base, appID, "/api/health", http.StatusOK, `"status":"ok"`, firstSecret)
	nextLiveRequest(t, ctx, base, appID, "/api/runtime", http.StatusOK, `"runtimeMarker":"blue"`, firstSecret)
	nextLiveImageCache(t, ctx, base, appID, firstSecret, 32)
	nextLiveCacheWritable(t, ctx, docker, blue.ContainerID)
	if _, err := controllerJourneyDocker(ctx, docker, nil, "container", "restart", blue.ContainerID); err != nil {
		t.Fatal("restart exact Next candidate")
	}
	if err := engine.WaitHealthy(ctx, blue); err != nil {
		t.Fatal("recover Next candidate after restart:", err)
	}
	assertNextLiveContainer(t, ctx, docker, blue, firstSecret)
	nextLiveImageCache(t, ctx, base, appID, firstSecret, 48)
	nextLiveCacheWritable(t, ctx, docker, blue.ContainerID)
	spec.ActiveSlot, spec.DeploymentID = blue.Slot, uuid.NewString()
	spec.EnvironmentOperationID = uuid.NewString()
	spec.Environment = []byte("RIG_FIXTURE_RUNTIME_SECRET=" + secondSecret + "\nRIG_FIXTURE_SLOT_MARKER=green\n")
	green := nextLiveStart(t, ctx, engine, spec)
	defer func() { _ = engine.StopAndRemove(context.Background(), green, 0) }()
	assertNextLiveContainer(t, ctx, docker, green, secondSecret)
	nextLiveRequest(t, ctx, base, appID, "/api/runtime", http.StatusOK, `"runtimeMarker":"blue"`, firstSecret)
	if err := ingress.Switch(ctx, generatedruntime.RouteSwitchRequest{
		AppID: appID, FromSlot: blue.Slot, ToSlot: green.Slot,
		Endpoints: []generatedruntime.RouteEndpoint{nextLiveEndpoint(green)}, DrainPeriod: time.Second,
	}); err != nil {
		t.Fatal("replace Next route:", err)
	}
	nextLiveAttestedEndpoint(t, ctx, ingress, appID, green)
	nextLiveRequest(t, ctx, base, appID, "/", http.StatusOK, "next-public-A", secondSecret)
	nextLiveRequest(t, ctx, base, appID, "/api/runtime", http.StatusOK, `"runtimeMarker":"green"`, secondSecret)
	nextLiveImageCache(t, ctx, base, appID, secondSecret, 32)
	nextLiveCacheWritable(t, ctx, docker, green.ContainerID)
	if err := engine.StopAndRemove(ctx, blue, time.Second); err != nil {
		t.Fatal("remove drained Next candidate:", err)
	}
	nextLiveRequest(t, ctx, base, appID, "/api/health", http.StatusOK, `"status":"ok"`, secondSecret)
	t.Logf("Next cache runtime route passed: app=%s release=%s blue=%s green=%s port=%d", appID, releaseID, blue.Slot, green.Slot, port)
}

func nextLiveStart(t *testing.T, ctx context.Context, engine *generatedruntime.Engine, spec generatedruntime.CandidateSpec) generatedruntime.Candidate {
	t.Helper()
	candidate, err := engine.CreateInactiveCandidate(ctx, spec)
	if err != nil {
		t.Fatal("create Next candidate:", err)
	}
	if err := engine.StartCandidate(ctx, candidate); err != nil {
		t.Fatal("start Next candidate:", err)
	}
	if err := engine.WaitHealthy(ctx, candidate); err != nil {
		t.Fatal("Next candidate unhealthy:", err)
	}
	return candidate
}

func nextLiveEndpoint(candidate generatedruntime.Candidate) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{Component: candidate.Component, Role: candidate.Role, ContainerID: candidate.ContainerID,
		NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort}
}

func nextLiveAttestedEndpoint(t *testing.T, ctx context.Context, ingress *generatedingress.Manager, appID string, candidate generatedruntime.Candidate) {
	t.Helper()
	observation, err := ingress.Observe(ctx, appID)
	if err != nil || observation.Slot != candidate.Slot || len(observation.Endpoints) != 1 ||
		observation.Endpoints[0].ContainerID != candidate.ContainerID || observation.URL == "" {
		t.Fatal("live Caddy route did not attest the exact Next candidate")
	}
}

func nextLiveBuildImage(t *testing.T, ctx context.Context, docker, root, tag, appID, releaseID, artifactID, planID string) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", "..", "examples", "hosting-nextjs"))
	if err != nil {
		t.Fatal(err)
	}
	buildRoot := filepath.Join(root, "build-context")
	if err := os.Mkdir(buildRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(buildRoot, "source")
	if err := os.Mkdir(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && (entry.Name() == "node_modules" || entry.Name() == ".next") {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !entry.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("fixture contains a non-regular source entry")
		}
		destination := filepath.Join(staged, relative)
		if entry.IsDir() {
			return os.Mkdir(destination, 0o700)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, body, 0o600)
	})
	if err != nil {
		t.Fatal("stage Next fixture source:", err)
	}
	// This image is intentionally independent of the generated compiler. The
	// runtime still verifies exact image labels, digest, user, and entrypoint.
	containerfile := fmt.Sprintf(`FROM %s AS builder
WORKDIR /workspace
RUN ["chown", "node:node", "/workspace"]
COPY --chown=node:node source/ /workspace/
USER node
RUN npm ci
ARG NEXT_PUBLIC_BUILD_MARKER
RUN NEXT_PUBLIC_BUILD_MARKER="$NEXT_PUBLIC_BUILD_MARKER" npm run build
RUN mkdir -p /workspace/.next/cache/images
FROM %s AS runtime
ENV NODE_ENV=production
WORKDIR /workspace
COPY --from=builder --chown=node:node /workspace/ /workspace/
COPY --chmod=0555 rig-entrypoint /usr/local/bin/rig-entrypoint
USER node
ENTRYPOINT ["/usr/local/bin/rig-entrypoint"]
`, nextLiveBaseImage, nextLiveBaseImage)
	for name, body := range map[string]string{
		"Dockerfile":     containerfile,
		"rig-entrypoint": "#!/bin/sh\nset -eu\nexec \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(buildRoot, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	definition := sha256.Sum256([]byte("reviewed-nextjs-cache-fixture-v1"))
	args := []string{"build", "--pull", "--quiet", "--tag", tag, "--build-arg", "NEXT_PUBLIC_BUILD_MARKER=next-public-A"}
	for _, entry := range []string{
		"io.rig.managed=generated-image", "io.rig.application=" + appID, "io.rig.release=" + releaseID,
		"io.rig.artifact=" + artifactID, "io.rig.plan=" + planID, "io.rig.component=web",
		"io.rig.role=server", "io.rig.definition=" + fmt.Sprintf("%x", definition),
	} {
		args = append(args, "--label", entry)
	}
	args = append(args, buildRoot)
	if _, err := controllerJourneyDocker(ctx, docker, nil, args...); err != nil {
		t.Fatal("build isolated Next fixture image")
	}
	output, err := controllerJourneyDocker(ctx, docker, nil, "image", "inspect", "--format", "{{.ID}}", tag)
	imageID := strings.TrimSpace(string(output))
	if err != nil || len(imageID) != 71 || !strings.HasPrefix(imageID, "sha256:") {
		t.Fatal("inspect isolated Next fixture image")
	}
	configuration, err := controllerJourneyDocker(ctx, docker, nil, "image", "inspect", "--format", "{{json .Config.Env}}", imageID)
	if err != nil || bytes.Contains(configuration, []byte("RIG_FIXTURE_RUNTIME_SECRET")) || bytes.Contains(configuration, []byte("next-runtime-")) {
		t.Fatal("runtime-only secret was baked into the Next fixture image")
	}
	return imageID
}

func assertNextLiveContainer(t *testing.T, ctx context.Context, docker string, candidate generatedruntime.Candidate, secret string) {
	t.Helper()
	configuration, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{json .}}", candidate.ContainerID)
	var inspected struct {
		Config struct {
			Env    []string
			Labels map[string]string
			User   string
		}
		HostConfig struct {
			ReadonlyRootfs bool
			Privileged     bool
			CapDrop        []string
			Tmpfs          map[string]string
		}
	}
	if err != nil || json.Unmarshal(configuration, &inspected) != nil {
		t.Fatal("inspect exact Next candidate")
	}
	if inspected.Config.Labels["io.rig.managed"] != "generated-runtime" ||
		inspected.Config.Labels["io.rig.application"] != candidate.AppID ||
		inspected.Config.Labels["io.rig.deployment"] != candidate.DeploymentID ||
		inspected.Config.User != "node" || !inspected.HostConfig.ReadonlyRootfs || inspected.HostConfig.Privileged ||
		len(inspected.HostConfig.CapDrop) != 1 || !strings.EqualFold(inspected.HostConfig.CapDrop[0], "ALL") {
		t.Fatal("Next candidate ownership or read-only hardening differs")
	}
	if len(inspected.HostConfig.Tmpfs) != 2 ||
		!nextLiveOptions(inspected.HostConfig.Tmpfs["/workspace/.next/cache"], "rw", "noexec", "nosuid", "nodev", "uid=1000", "gid=1000", "mode=0700", "size=33554432") ||
		!nextLiveOptions(inspected.HostConfig.Tmpfs["/tmp"], "rw", "noexec", "nosuid", "nodev", "size=67108864") {
		t.Fatal("Next cache tmpfs ownership or quota differs")
	}
	if !strings.Contains(strings.Join(inspected.Config.Env, "\n"), "RIG_FIXTURE_RUNTIME_SECRET="+secret) {
		t.Fatal("Next candidate did not receive its scoped runtime-only secret")
	}
}

func nextLiveOptions(raw string, expected ...string) bool {
	if len(strings.Split(raw, ",")) != len(expected) {
		return false
	}
	seen := make(map[string]bool)
	for _, value := range strings.Split(raw, ",") {
		seen[value] = true
	}
	for _, value := range expected {
		if !seen[value] {
			return false
		}
	}
	return true
}

func nextLiveRequest(t *testing.T, ctx context.Context, base, appID, path string, want int, fragment, secret string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = appID + ".rig.localhost"
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal("Next route unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || response.StatusCode != want || !bytes.Contains(body, []byte(fragment)) ||
		bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte("RIG_FIXTURE_RUNTIME_SECRET")) {
		t.Fatalf("Next routed %s: status=%d want=%d expected-content=%t secret-absent=%t", path, response.StatusCode, want, bytes.Contains(body, []byte(fragment)), !bytes.Contains(body, []byte(secret)))
	}
	return response
}

func nextLiveImageCache(t *testing.T, ctx context.Context, base, appID, secret string, width int) {
	t.Helper()
	path := "/_next/image?url=%2Fpixel.png&w=" + strconv.Itoa(width) + "&q=75"
	first := nextLiveRequest(t, ctx, base, appID, path, http.StatusOK, "PNG", secret)
	second := nextLiveRequest(t, ctx, base, appID, path, http.StatusOK, "PNG", secret)
	if first.Header.Get("X-Nextjs-Cache") != "MISS" || second.Header.Get("X-Nextjs-Cache") != "HIT" ||
		!strings.HasPrefix(second.Header.Get("Content-Type"), "image/") {
		t.Fatal("Next optimized image did not write and reuse its bounded cache")
	}
}

func nextLiveCacheWritable(t *testing.T, ctx context.Context, docker, containerID string) {
	t.Helper()
	output, err := controllerJourneyDocker(ctx, docker, nil, "container", "exec", containerID, "/bin/sh", "-ec",
		"test \"$(stat -c '%u:%g:%a' /workspace/.next/cache)\" = '1000:1000:700' && test -n \"$(find /workspace/.next/cache/images -type f -print -quit)\" && test ! -w /workspace/package.json")
	if err != nil || len(bytes.TrimSpace(output)) != 0 {
		t.Fatal("Next cache ownership, write, or read-only rootfs check failed")
	}
}

func nextLiveCleanup(t *testing.T, docker, appID, network, imageTag string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, resource := range [][]string{
		{"container", "rig-generated-caddy-v1", "generated-ingress"},
		{"volume", "rig-generated-caddy-config-v1", "generated-ingress"},
		{"network", "rig-generated-caddy-ingress-v1", "generated-ingress-network"},
	} {
		if !controllerJourneyExists(t, ctx, docker, resource[0], resource[1]) {
			continue
		}
		format := "{{json .Labels}}"
		if resource[0] == "container" {
			format = "{{json .Config.Labels}}"
		}
		body, err := controllerJourneyDocker(ctx, docker, nil, resource[0], "inspect", "--format", format, resource[1])
		var labels map[string]string
		if err != nil || json.Unmarshal(body, &labels) != nil || labels["io.rig.managed"] != resource[2] || labels["io.rig.identity-version"] != "v1" {
			t.Error("generated ingress ownership uncertain; retaining")
			continue
		}
		args := []string{resource[0], "rm", resource[1]}
		if resource[0] != "network" {
			args = []string{resource[0], "rm", "--force", resource[1]}
		}
		if _, err := controllerJourneyDocker(ctx, docker, nil, args...); err != nil {
			t.Error("remove exact generated ingress resource")
		}
	}
	ids, err := controllerJourneyDocker(ctx, docker, nil, "ps", "-aq", "--filter", "label=io.rig.application="+appID)
	if err != nil {
		t.Error("enumerate exact Next candidate resources")
	}
	for _, id := range strings.Fields(string(ids)) {
		if _, err := controllerJourneyDocker(ctx, docker, nil, "container", "rm", "--force", id); err != nil {
			t.Error("remove exact Next candidate")
		}
	}
	if controllerJourneyExists(t, ctx, docker, "network", network) {
		body, err := controllerJourneyDocker(ctx, docker, nil, "network", "inspect", "--format", "{{json .Labels}}", network)
		var labels map[string]string
		if err != nil || json.Unmarshal(body, &labels) != nil || labels["io.rig.managed"] != generatedruntime.NetworkOwnershipLabelValue || labels["io.rig.application"] != appID {
			t.Error("Next application network ownership uncertain; retaining")
		} else if _, err := controllerJourneyDocker(ctx, docker, nil, "network", "rm", network); err != nil {
			t.Error("remove exact Next application network")
		}
	}
	body, err := controllerJourneyDocker(ctx, docker, nil, "image", "inspect", "--format", "{{json .Config.Labels}}", imageTag)
	if err == nil {
		var labels map[string]string
		if json.Unmarshal(body, &labels) != nil || labels["io.rig.managed"] != "generated-image" || labels["io.rig.application"] != appID {
			t.Error("Next image ownership uncertain; retaining")
		} else if _, err := controllerJourneyDocker(ctx, docker, nil, "image", "rm", "--force", imageTag); err != nil {
			t.Error("remove exact Next fixture image")
		}
	}
	if controllerJourneyExists(t, ctx, docker, "network", network) || controllerJourneyExists(t, ctx, docker, "container", "rig-generated-caddy-v1") ||
		controllerJourneyExists(t, ctx, docker, "volume", "rig-generated-caddy-config-v1") || controllerJourneyExists(t, ctx, docker, "network", "rig-generated-caddy-ingress-v1") {
		t.Error("Next test Docker resources remain after cleanup")
	}
}
