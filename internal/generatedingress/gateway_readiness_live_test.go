package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// TestLiveGeneratedGatewayReadiness is an opt-in acceptance gate for the
// distinction between a container's loopback health check and reachability
// from the pinned Caddy gateway container. It owns only the fixed identities
// checked during preflight and cleanup below.
func TestLiveGeneratedGatewayReadiness(t *testing.T) {
	if os.Getenv("RIG_RUN_LIVE_GENERATED_RUNTIME") != "1" {
		t.Skip("set RIG_RUN_LIVE_GENERATED_RUNTIME=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("live gateway readiness requires Docker's default local Linux context")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker executable unavailable")
	}
	docker, err = filepath.Abs(docker)
	if err != nil {
		t.Fatal("resolve docker executable")
	}

	root := t.TempDir()
	state := filepath.Join(root, "ingress-state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal("create live gateway controller state root:", err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(state)
	if err != nil {
		t.Fatal("prepare live gateway controller directories:", err)
	}
	dockerConfig, working := directories.DockerConfigDirectory, directories.WorkingDirectory

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	runner := &runtimeprocess.ExecRunner{}
	liveGatewayRequireLocalDocker(t, ctx, runner, docker, root, dockerConfig)

	const (
		appA = "12121212-1212-4121-8121-121212121212"
		appB = "34343434-3434-4343-8343-343434343434"
		appC = "78787878-7878-4787-8787-787878787878"
		plan = "56565656-5656-4565-8565-565656565656"
	)
	networkA, err := generatedidentity.DescribeAppNetwork(appA)
	if err != nil {
		t.Fatal("describe first application network")
	}
	networkB, err := generatedidentity.DescribeAppNetwork(appB)
	if err != nil {
		t.Fatal("describe second application network")
	}
	networkC, err := generatedidentity.DescribeAppNetwork(appC)
	if err != nil {
		t.Fatal("describe replacement fixture network")
	}
	images := []liveGatewayImage{
		{tag: "rig-generated-gateway-live:blue", appID: appA},
		{tag: "rig-generated-gateway-live:loopback", appID: appA},
		{tag: "rig-generated-gateway-live:replacement", appID: appC},
		{tag: "rig-generated-gateway-live:first-loopback", appID: appB},
		{tag: "rig-generated-gateway-live:replacement-baseline", appID: appC},
	}
	liveGatewayRequireCleanPreflight(t, ctx, runner, docker, root, dockerConfig, []string{networkA.Name, networkB.Name, networkC.Name}, images)

	limits := liveRuntimeContainerLimits{
		MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 128,
		TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 2,
	}
	hostPort := freeLoopbackPort(t)
	engineOptions := liveRuntimeEngineOptions{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		CommandTimeout: 45 * time.Second, HealthTimeout: 90 * time.Second, HealthPollInterval: 250 * time.Millisecond,
		OutputLimit: liveDockerOutputLimit, Limits: limits, ReplacementDiskBytes: 96 << 20,
	}
	authority, engine := liveGeneratedDeployments(t, ctx, state, runner, hostPort, engineOptions)
	ingress := authority.domain.Gateway()

	var candidates []liveRuntimeCandidate
	t.Cleanup(func() {
		liveGatewayCleanup(t, engine, runner, docker, root, dockerConfig, candidates, map[string]string{networkA.Name: appA, networkB.Name: appB, networkC.Name: appC}, images)
	})

	blueSpec := liveCandidateSpec("blue", appA, plan)
	blueSpec = authority.PrepareCandidate(t, ctx, blueSpec)
	blueSpec.ImageContentID = buildLiveGatewayImage(t, ctx, runner, docker, root, dockerConfig, images[0].tag, blueSpec, "blue", "0.0.0.0")
	blue := startLiveCandidate(t, ctx, engine, authority, blueSpec)
	candidates = append(candidates, blue)
	blueRoute := generatedroute.RouteSwitchRequest{
		AppID: appA, ToSlot: blue.Slot, Endpoints: []generatedroute.RouteEndpoint{liveEndpoint(blue)},
	}
	if err := authority.activate(t, blue, blueRoute); err != nil {
		failLiveIngress(t, "route healthy blue candidate", err)
	}
	assertLiveOneShot(t, ctx, ingress.options.HostPort, appA, "/", "blue", "healthy blue route did not serve through Caddy")
	blueObservation, err := ingress.Observe(ctx, appA)
	if err != nil || blueObservation.URL == "" || blueObservation.Slot != blue.Slot || !reflect.DeepEqual(blueObservation.Endpoints, blueRoute.Endpoints) {
		t.Fatal("healthy blue route was not attested")
	}

	loopbackSpec := liveCandidateSpec("green", appA, plan)
	loopbackSpec.ActiveSlot = blue.Slot
	loopbackSpec = authority.PrepareCandidate(t, ctx, loopbackSpec)
	loopbackSpec.ImageContentID = buildLiveGatewayImage(t, ctx, runner, docker, root, dockerConfig, images[1].tag, loopbackSpec, "loopback", "127.0.0.1")
	loopback := createAndStartLiveCandidate(t, ctx, engine, authority, loopbackSpec)
	candidates = append(candidates, loopback)
	if err := authority.waitHealthy(loopback); err != nil {
		failLiveGatewayRuntime(t, "loopback candidate did not pass Docker health", err)
	}

	loopbackRoute := generatedroute.RouteSwitchRequest{
		AppID: appA, FromSlot: blue.Slot, ToSlot: loopback.Slot, Endpoints: []generatedroute.RouteEndpoint{liveEndpoint(loopback)},
	}
	switchErr := authority.activate(t, loopback, loopbackRoute)
	if !IsCode(switchErr, DiagnosticGatewayReadinessFailed) {
		failLiveIngress(t, "loopback candidate did not report gateway readiness failure", switchErr)
	}
	if testRouteCandidateMayBeLive(switchErr) || testRouteGatewayReadinessFailed(switchErr) == false {
		t.Fatal("gateway readiness failure did not preserve its safe route contract")
	}
	liveGatewayAssertCommittedRoute(t, ingress, appA, blueRoute)
	assertLiveOneShot(t, ctx, ingress.options.HostPort, appA, "/", "blue", "Caddy stopped serving blue after loopback rejection")
	observedAfterFailure, err := ingress.Observe(ctx, appA)
	if err != nil || observedAfterFailure.URL != blueObservation.URL || observedAfterFailure.Slot != blueObservation.Slot || !reflect.DeepEqual(observedAfterFailure.Endpoints, blueObservation.Endpoints) {
		t.Fatal("blue attested URL changed after loopback rejection")
	}

	if err := engine.StopAndRemove(ctx, loopback, 0); !errors.Is(err, deployments.ErrGeneratedCandidateRetirementUnavailable) {
		t.Fatal("handed-off candidate bypassed the retirement boundary")
	}
	if !liveGatewayResourceExists(t, ctx, runner, docker, root, dockerConfig, "container", loopback.ContainerName) {
		t.Fatal("unresolved handed-off candidate was removed")
	}
	authority.sessions[loopback.DeploymentID].Close()
	// Post-handoff recovery belongs to the route/retirement owner. Exercise a
	// successful replacement on a separate application rather than fabricating
	// failure/completion history to reuse the unresolved application's slot.
	baselineSpec := liveCandidateSpec("blue", appC, plan)
	baselineSpec.ReleaseID, baselineSpec.DeploymentID, baselineSpec.ArtifactID = uuid.NewString(), uuid.NewString(), uuid.NewString()
	baselineSpec = authority.PrepareCandidate(t, ctx, baselineSpec)
	baselineSpec.ImageContentID = buildLiveGatewayImage(t, ctx, runner, docker, root, dockerConfig, images[4].tag, baselineSpec, "replacement-baseline", "0.0.0.0")
	baseline := startLiveCandidate(t, ctx, engine, authority, baselineSpec)
	candidates = append(candidates, baseline)
	baselineRoute := generatedroute.RouteSwitchRequest{AppID: appC, ToSlot: baseline.Slot, Endpoints: []generatedroute.RouteEndpoint{liveEndpoint(baseline)}}
	if err := authority.activate(t, baseline, baselineRoute); err != nil {
		failLiveIngress(t, "route replacement baseline", err)
	}

	replacementSpec := liveCandidateSpec("green", appC, plan)
	replacementSpec.ReleaseID = "d1111111-1111-4111-8111-111111111111"
	replacementSpec.DeploymentID = "d2222222-2222-4222-8222-222222222222"
	replacementSpec.ArtifactID = "d3333333-3333-4333-8333-333333333333"
	replacementSpec.BuildDefinitionDigest = strings.Repeat("d", 64)
	replacementSpec.ActiveSlot = baseline.Slot
	replacementSpec = authority.PrepareCandidate(t, ctx, replacementSpec)
	replacementSpec.ImageContentID = buildLiveGatewayImage(t, ctx, runner, docker, root, dockerConfig, images[2].tag, replacementSpec, "replacement", "0.0.0.0")
	replacement := startLiveCandidate(t, ctx, engine, authority, replacementSpec)
	candidates = append(candidates, replacement)
	replacementRoute := generatedroute.RouteSwitchRequest{
		AppID: appC, FromSlot: baseline.Slot, ToSlot: replacement.Slot, Endpoints: []generatedroute.RouteEndpoint{liveEndpoint(replacement)},
	}
	if err := authority.activate(t, replacement, replacementRoute); err != nil {
		failLiveIngress(t, "route healthy replacement", err)
	}
	assertLiveOneShot(t, ctx, ingress.options.HostPort, appC, "/", "replacement", "healthy replacement did not serve through Caddy")

	firstSpec := liveCandidateSpec("other", appB, plan)
	firstSpec = authority.PrepareCandidate(t, ctx, firstSpec)
	firstSpec.ImageContentID = buildLiveGatewayImage(t, ctx, runner, docker, root, dockerConfig, images[3].tag, firstSpec, "first-loopback", "127.0.0.1")
	firstLoopback := createAndStartLiveCandidate(t, ctx, engine, authority, firstSpec)
	candidates = append(candidates, firstLoopback)
	if err := authority.waitHealthy(firstLoopback); err != nil {
		failLiveGatewayRuntime(t, "first loopback candidate did not pass Docker health", err)
	}
	firstRoute := generatedroute.RouteSwitchRequest{
		AppID: appB, ToSlot: firstLoopback.Slot, Endpoints: []generatedroute.RouteEndpoint{liveEndpoint(firstLoopback)},
	}
	firstErr := authority.activate(t, firstLoopback, firstRoute)
	if !IsCode(firstErr, DiagnosticGatewayReadinessFailed) || testRouteCandidateMayBeLive(firstErr) || !testRouteGatewayReadinessFailed(firstErr) {
		failLiveIngress(t, "first loopback deployment did not fail safely", firstErr)
	}
	liveGatewayAssertNoCommittedRoute(t, ingress, appB)
	firstObservation, observeErr := ingress.Observe(ctx, appB)
	if !IsCode(observeErr, DiagnosticRouteInvalid) || firstObservation.URL != "" || firstObservation.Slot != "" || len(firstObservation.Endpoints) != 0 {
		t.Fatal("first loopback deployment exposed a verified URL")
	}
	liveGatewayAssertCaddyDetached(t, ctx, runner, docker, root, dockerConfig, networkB.Name)
	assertLiveOneShot(t, ctx, ingress.options.HostPort, appA, "/", "blue", "first loopback deployment changed retained route")
	assertLiveOneShot(t, ctx, ingress.options.HostPort, appC, "/", "replacement", "first loopback deployment changed replacement route")
	authority.sessions[firstLoopback.DeploymentID].Close()
}

type liveGatewayImage struct {
	tag   string
	appID string
}

func buildLiveGatewayImage(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, root, dockerConfig, tag string, spec liveRuntimeCandidateSpec, version, bindAddress string) string {
	t.Helper()
	server := fmt.Sprintf("import { createServer } from 'node:http';\nconst port=Number(process.env.RIG_RUNTIME_INTERNAL_PORT);\nconst version=%q;\ncreateServer((request,response)=>{ response.writeHead(200,{ 'content-type':'text/plain' }); response.end(version); }).listen(port,%q);\n", version, bindAddress)
	return buildLiveGatewayImageWithServer(t, ctx, runner, docker, root, dockerConfig, tag, spec, version, server)
}

func buildLiveGatewayImageWithServer(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, root, dockerConfig, tag string, spec liveRuntimeCandidateSpec, version, server string) string {
	t.Helper()
	// Buildx persists state below DOCKER_CONFIG. Keep fixture build state out of
	// the intentionally empty runtime Docker configuration used by the engine.
	buildConfig := t.TempDir()
	contextRoot := filepath.Join(root, "gateway-image-"+version)
	if err := os.Mkdir(contextRoot, 0o700); err != nil {
		t.Fatal("create gateway image fixture")
	}
	containerfile := fmt.Sprintf("FROM %s\nWORKDIR /workspace\nCOPY --chmod=0555 rig-entrypoint /usr/local/bin/rig-entrypoint\nCOPY --chown=node:node server.mjs /workspace/server.mjs\nUSER node\nENTRYPOINT [\"/usr/local/bin/rig-entrypoint\"]\n", liveNodeImage)
	entrypoint := "#!/bin/sh\nset -eu\nexec \"$@\"\n"
	for name, contents := range map[string]string{"Dockerfile": containerfile, "rig-entrypoint": entrypoint, "server.mjs": server} {
		if err := os.WriteFile(filepath.Join(contextRoot, name), []byte(contents), 0o600); err != nil {
			t.Fatal("write gateway image fixture")
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
	result, err := runLiveDocker(ctx, runner, docker, root, buildConfig, 5*time.Minute, args...)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("build gateway image fixture")
	}
	clearLiveResult(&result)
	result, err = runLiveDocker(ctx, runner, docker, root, buildConfig, 45*time.Second, "image", "inspect", "--format", "{{.ID}}", tag)
	defer clearLiveResult(&result)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		t.Fatal("inspect gateway image fixture")
	}
	imageID := strings.TrimSpace(string(result.Stdout))
	if !liveImageID(imageID) {
		t.Fatal("gateway image fixture has invalid image ID")
	}
	return imageID
}

func liveGatewayRequireLocalDocker(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string) {
	t.Helper()
	for _, request := range [][]string{{"context", "show"}, {"context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}"}, {"info"}} {
		result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, request...)
		output := strings.TrimSpace(string(result.Stdout))
		valid := err == nil && !result.StdoutTruncated && !result.StderrTruncated
		clearLiveResult(&result)
		if !valid {
			t.Fatal("Docker daemon unavailable")
		}
		switch request[0] {
		case "context":
			if request[1] == "show" && output != "default" {
				t.Fatal("live gateway readiness requires Docker's default local context")
			}
			if request[1] == "inspect" && !strings.HasPrefix(output, "unix:///") {
				t.Fatal("live gateway readiness requires a local Unix Docker endpoint")
			}
		}
	}
}

func liveGatewayAssertCommittedRoute(t *testing.T, ingress *Manager, appID string, request generatedroute.RouteSwitchRequest) {
	t.Helper()
	state, err := ingress.store.load()
	if err != nil || state.Pending != nil {
		t.Fatal("committed ingress state is unavailable")
	}
	route, found := state.Active[appID]
	want := routeRecord{Slot: request.ToSlot, Endpoints: request.Endpoints}
	if !found || !sameRoute(route, want) {
		t.Fatal("committed ingress route changed")
	}
}

func liveGatewayAssertNoCommittedRoute(t *testing.T, ingress *Manager, appID string) {
	t.Helper()
	state, err := ingress.store.load()
	if err != nil || state.Pending != nil {
		t.Fatal("first deployment left uncertain ingress state")
	}
	if _, found := state.Active[appID]; found {
		t.Fatal("first loopback deployment committed a route")
	}
}

func liveGatewayAssertCaddyDetached(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig, network string) {
	t.Helper()
	result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, "network", "inspect", "--format", "{{json .Containers}}", network)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("inspect second application network")
	}
	var containers map[string]struct {
		Name string `json:"Name"`
	}
	err = json.Unmarshal(result.Stdout, &containers)
	clearLiveResult(&result)
	if err != nil {
		t.Fatal("decode second application network")
	}
	for _, container := range containers {
		if container.Name == caddyContainerName {
			t.Fatal("first loopback deployment left Caddy attached to an uncommitted network")
		}
	}
}

func failLiveGatewayRuntime(t *testing.T, phase string, err error) {
	t.Helper()
	t.Fatalf("%s: runtime=%s", phase, liveRuntimeDiagnostic(err))
}

func liveGatewayRequireCleanPreflight(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string, appNetworks []string, images []liveGatewayImage) {
	t.Helper()
	resources := []struct{ kind, name string }{
		{"container", caddyContainerName}, {"volume", caddyVolumeName}, {"network", caddyNetworkName},
	}
	for _, network := range appNetworks {
		resources = append(resources, struct{ kind, name string }{"network", network})
	}
	for _, image := range images {
		resources = append(resources, struct{ kind, name string }{"image", image.tag})
	}
	for _, resource := range resources {
		if liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, resource.kind, resource.name) {
			t.Fatalf("disposable Docker daemon already contains %s %s", resource.kind, resource.name)
		}
	}
}

// A successful list is required to prove absence. Inspect errors cannot safely
// distinguish a missing identity from a daemon or authorization failure.
func liveGatewayResourceExists(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig, kind, name string) bool {
	t.Helper()
	var args []string
	switch kind {
	case "container":
		args = []string{"container", "ls", "--all", "--format", "{{.Names}}"}
	case "volume":
		args = []string{"volume", "ls", "--format", "{{.Name}}"}
	case "network":
		args = []string{"network", "ls", "--format", "{{.Name}}"}
	case "image":
		args = []string{"image", "ls", "--format", "{{.Repository}}:{{.Tag}}"}
	default:
		t.Fatal("unsupported Docker resource kind")
	}
	result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, args...)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("cannot establish disposable Docker resource preflight")
	}
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(result.Stdout), []byte("\n")) {
		if string(bytes.TrimSpace(line)) == name {
			found = true
			break
		}
	}
	clearLiveResult(&result)
	return found
}

func liveGatewayCleanup(t *testing.T, engine liveRuntimeEngine, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string, candidates []liveRuntimeCandidate, appNetworks map[string]string, images []liveGatewayImage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for index := len(candidates) - 1; index >= 0; index-- {
		candidate := candidates[index]
		if candidate.ContainerID != "" {
			liveGatewayRemoveOwnedCandidate(t, ctx, runner, docker, directory, dockerConfig, candidate)
		}
	}
	liveGatewayRemoveOwnedIngress(t, ctx, runner, docker, directory, dockerConfig)
	for network, appID := range appNetworks {
		liveGatewayRemoveOwnedNetwork(t, ctx, runner, docker, directory, dockerConfig, network, appID)
	}
	for _, image := range images {
		liveGatewayRemoveOwnedImage(t, ctx, runner, docker, directory, dockerConfig, image)
	}
	for _, resource := range []struct{ kind, name string }{
		{"container", caddyContainerName}, {"volume", caddyVolumeName}, {"network", caddyNetworkName},
	} {
		if liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, resource.kind, resource.name) {
			t.Errorf("owned ingress %s remains after cleanup", resource.kind)
		}
	}
	for network := range appNetworks {
		if liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, "network", network) {
			t.Errorf("owned application network remains after cleanup")
		}
	}
	for _, image := range images {
		if liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, "image", image.tag) {
			t.Errorf("owned fixture image remains after cleanup")
		}
	}
}

// This is disposable fixture teardown, not controller retirement evidence.
// The candidate ID came from the real owner's create result and every immutable
// ownership label is checked before removing that exact Docker ID.
func liveGatewayRemoveOwnedCandidate(t *testing.T, parent context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string, candidate liveRuntimeCandidate) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	if !liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, "container", candidate.ContainerName) {
		return
	}
	format := `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Config.Labels}}}`
	result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 20*time.Second, "container", "inspect", "--format", format, candidate.ContainerID)
	var observed struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	valid := err == nil && !result.StdoutTruncated && !result.StderrTruncated && json.Unmarshal(result.Stdout, &observed) == nil &&
		observed.ID == candidate.ContainerID && strings.TrimPrefix(observed.Name, "/") == candidate.ContainerName && liveContainsLabels(observed.Labels, liveCandidateLabels(candidate))
	clearLiveResult(&result)
	if !valid {
		t.Error("fixture candidate ownership is uncertain; retaining resource")
		return
	}
	result, err = runLiveDocker(ctx, runner, docker, directory, dockerConfig, 20*time.Second, "container", "rm", "--force", candidate.ContainerID)
	clearLiveResult(&result)
	if err != nil {
		t.Error("remove exact disposable fixture candidate")
	}
}

func liveGatewayRemoveOwnedIngress(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string) {
	t.Helper()
	for _, resource := range []struct {
		kind, name, managed string
		remove              []string
	}{
		{"container", caddyContainerName, "generated-ingress", []string{"container", "rm", "--force", caddyContainerName}},
		{"volume", caddyVolumeName, "generated-ingress", []string{"volume", "rm", "--force", caddyVolumeName}},
		{"network", caddyNetworkName, "generated-ingress-network", []string{"network", "rm", caddyNetworkName}},
	} {
		if !liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, resource.kind, resource.name) {
			continue
		}
		format := "{{json .Labels}}"
		if resource.kind == "container" {
			format = "{{json .Config.Labels}}"
		}
		result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, resource.kind, "inspect", "--format", format, resource.name)
		var labels map[string]string
		valid := err == nil && !result.StdoutTruncated && !result.StderrTruncated && json.Unmarshal(result.Stdout, &labels) == nil && labels["io.rig.managed"] == resource.managed && labels["io.rig.identity-version"] == "v1"
		clearLiveResult(&result)
		if !valid {
			t.Errorf("generated ingress %s ownership is uncertain; retaining resource", resource.kind)
			continue
		}
		result, err = runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, resource.remove...)
		clearLiveResult(&result)
		if err != nil {
			t.Errorf("remove exact generated ingress %s", resource.kind)
		}
	}
}

func liveGatewayRemoveOwnedNetwork(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig, network, appID string) {
	t.Helper()
	if !liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, "network", network) {
		return
	}
	result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, "network", "inspect", "--format", "{{json .Labels}}", network)
	var labels map[string]string
	valid := err == nil && !result.StdoutTruncated && !result.StderrTruncated && json.Unmarshal(result.Stdout, &labels) == nil && labels["io.rig.managed"] == generatedidentity.NetworkOwnershipLabelValue && labels["io.rig.application"] == appID
	clearLiveResult(&result)
	if !valid {
		t.Error("application network ownership is uncertain; retaining resource")
		return
	}
	result, err = runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, "network", "rm", network)
	clearLiveResult(&result)
	if err != nil {
		t.Error("remove exact application network")
	}
}

func liveGatewayRemoveOwnedImage(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string, image liveGatewayImage) {
	t.Helper()
	if !liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, "image", image.tag) {
		return
	}
	result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, "image", "inspect", "--format", "{{json .Config.Labels}}", image.tag)
	var labels map[string]string
	valid := err == nil && !result.StdoutTruncated && !result.StderrTruncated && json.Unmarshal(result.Stdout, &labels) == nil && labels["io.rig.managed"] == "generated-image" && labels["io.rig.application"] == image.appID
	clearLiveResult(&result)
	if !valid {
		t.Error("fixture image ownership is uncertain; retaining resource")
		return
	}
	result, err = runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, "image", "rm", "--force", image.tag)
	clearLiveResult(&result)
	if err != nil {
		t.Error("remove exact fixture image")
	}
}
