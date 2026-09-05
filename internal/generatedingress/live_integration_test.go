package generatedingress

import (
	"bytes"
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
	"syscall"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const liveNodeImage = "node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5"

const (
	liveHoldReadyCommand  = `const h=require('node:http');const q=h.get({host:'127.0.0.1',port:Number(process.env.RIG_RUNTIME_INTERNAL_PORT),path:'/hold-ready',timeout:10000},r=>{r.resume();process.exit(r.statusCode===200?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})`
	liveReleaseCommand    = `const h=require('node:http');const q=h.get({host:'127.0.0.1',port:Number(process.env.RIG_RUNTIME_INTERNAL_PORT),path:'/release',timeout:10000},r=>{r.resume();process.exit(r.statusCode===200?0:1)});q.on('error',()=>process.exit(1));q.on('timeout',()=>{q.destroy();process.exit(1)})`
	liveDockerOutputLimit = 64 << 10
)

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
		t.Fatal("docker executable unavailable")
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
			t.Fatal("create live lifecycle directory")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	runner := runtimeprocess.ExecRunner{}
	if result, err := runLiveDocker(ctx, runner, docker, root, dockerConfig, 45*time.Second, "info"); err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("docker daemon unavailable")
	} else {
		clearLiveResult(&result)
	}

	appID := "11111111-1111-4111-8111-111111111111"
	otherAppID := "99999999-9999-4999-8999-999999999999"
	planID := "22222222-2222-4222-8222-222222222222"
	network, err := generatedruntime.DescribeAppNetwork(appID)
	if err != nil {
		t.Fatal("describe application network")
	}
	otherNetwork, err := generatedruntime.DescribeAppNetwork(otherAppID)
	if err != nil {
		t.Fatal("describe other application network")
	}
	hostPort := freeLoopbackPort(t)
	imageTags := []string{"rig-generated-live-test:blue", "rig-generated-live-test:green", "rig-generated-live-test:other"}
	defer cleanupLiveDocker(runner, docker, root, dockerConfig, []string{network.Name, otherNetwork.Name}, imageTags)

	limits := generatedruntime.ContainerLimits{
		MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 128,
		TmpfsBytes: 16 << 20, LogSize: "1m", LogFiles: 2,
	}
	engine, err := generatedruntime.NewEngine(runner, liveEnvironmentStager{}, liveCapacitySource{}, generatedruntime.EngineOptions{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		CommandTimeout: 45 * time.Second, HealthTimeout: 90 * time.Second, HealthPollInterval: 250 * time.Millisecond,
		OutputLimit: liveDockerOutputLimit, Limits: limits, ReplacementDiskBytes: 96 << 20,
	})
	if err != nil {
		t.Fatal("create generated runtime")
	}
	ingress, err := New(runner, Options{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		DataRoot: state, HostPort: hostPort, CommandTimeout: 45 * time.Second, PullTimeout: 5 * time.Minute,
		OutputLimit: liveDockerOutputLimit,
	})
	if err != nil {
		t.Fatal("create generated ingress")
	}

	blueSpec := liveCandidateSpec("blue", appID, planID)
	blueSpec.ImageContentID = buildLiveImage(t, ctx, runner, docker, root, dockerConfig, imageTags[0], blueSpec, "blue")
	blue := startLiveCandidate(t, ctx, engine, blueSpec)
	bluePresent := true
	defer func() {
		if bluePresent {
			_ = engine.StopAndRemove(context.Background(), blue, 0)
		}
	}()
	blueRoute := generatedruntime.RouteSwitchRequest{
		AppID: appID, ToSlot: blue.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(blue)},
	}
	if err := ingress.Switch(ctx, blueRoute); err != nil {
		failLiveIngress(t, "route first slot", err)
	}
	assertLiveResponse(t, hostPort, appID, "/", "blue")
	engine.ReleaseAdmission(blue)

	// Caddy is attached to every private application network. A qualified
	// network alias must keep identical component/slot aliases isolated.
	otherSpec := liveCandidateSpec("other", otherAppID, planID)
	otherSpec.ImageContentID = buildLiveImage(t, ctx, runner, docker, root, dockerConfig, imageTags[2], otherSpec, "other")
	other := startLiveCandidate(t, ctx, engine, otherSpec)
	defer func() { _ = engine.StopAndRemove(context.Background(), other, 0) }()
	if other.Slot != generatedruntime.SlotBlue || other.NetworkAlias != blue.NetworkAlias || other.NetworkName == blue.NetworkName {
		t.Fatal("two-application alias-isolation fixture is invalid")
	}
	otherRoute := generatedruntime.RouteSwitchRequest{
		AppID: otherAppID, ToSlot: other.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(other)},
	}
	if err := ingress.Switch(ctx, otherRoute); err != nil {
		failLiveIngress(t, "route other application", err)
	}
	assertLiveOneShot(t, ctx, hostPort, appID, "/", "blue", "first application changed while routing other application")
	assertLiveOneShot(t, ctx, hostPort, otherAppID, "/", "other", "other application did not serve through its private network")
	engine.ReleaseAdmission(other)

	greenSpec := liveCandidateSpec("green", appID, planID)
	greenSpec.ActiveSlot = blue.Slot
	greenSpec.ImageContentID = buildLiveImage(t, ctx, runner, docker, root, dockerConfig, imageTags[1], greenSpec, "green")

	// A failed inactive candidate is never committed. The engine owns its exact
	// cleanup, while Caddy must continue serving the active blue route.
	unhealthySpec := greenSpec
	unhealthySpec.HealthProbe = "/unhealthy"
	unhealthy := createAndStartLiveCandidate(t, ctx, engine, unhealthySpec)
	assertLiveOneShot(t, ctx, hostPort, appID, "/", "blue", "active slot did not serve during unhealthy replacement")
	assertLiveOneShot(t, ctx, hostPort, otherAppID, "/", "other", "other application changed during unhealthy replacement")
	if err := engine.WaitHealthy(ctx, unhealthy); !generatedruntime.IsCode(err, generatedruntime.DiagnosticCandidateUnhealthy) {
		t.Fatal("unhealthy candidate did not report candidate_unhealthy")
	}
	assertLiveOneShot(t, ctx, hostPort, appID, "/", "blue", "active slot did not serve after unhealthy replacement")
	assertLiveOneShot(t, ctx, hostPort, otherAppID, "/", "other", "other application changed after unhealthy replacement")

	green := startLiveCandidate(t, ctx, engine, greenSpec)
	greenPresent := true
	defer func() {
		if greenPresent {
			_ = engine.StopAndRemove(context.Background(), green, 0)
		}
	}()
	assertLiveOneShot(t, ctx, hostPort, appID, "/", "blue", "inactive healthy slot received traffic before route switch")

	holdCtx, cancelHold := context.WithTimeout(ctx, 45*time.Second)
	defer cancelHold()
	held := make(chan liveHTTPResponse, 1)
	go func() {
		held <- liveRequest(holdCtx, hostPort, appID, "/hold", "blue")
	}()
	runLiveContainerControl(t, holdCtx, runner, docker, working, dockerConfig, blue.ContainerID, liveHoldReadyCommand, "held request did not reach blue")
	select {
	case <-held:
		t.Fatal("held request completed before route switch")
	default:
	}

	greenRoute := generatedruntime.RouteSwitchRequest{
		AppID: appID, FromSlot: blue.Slot, ToSlot: green.Slot,
		Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(green)}, DrainPeriod: time.Second,
	}
	if err := ingress.Switch(ctx, greenRoute); err != nil {
		failLiveIngress(t, "switch to replacement slot", err)
	}
	assertLiveOneShot(t, ctx, hostPort, appID, "/", "green", "replacement slot did not serve after route switch")
	assertLiveOneShot(t, ctx, hostPort, otherAppID, "/", "other", "other application changed after first application route switch")
	runLiveContainerControl(t, holdCtx, runner, docker, working, dockerConfig, blue.ContainerID, liveReleaseCommand, "held request did not release")
	select {
	case response := <-held:
		if !liveResponseMatches(response) {
			t.Fatalf("held request did not complete from blue: %s", liveHTTPDiagnostic(response))
		}
	case <-holdCtx.Done():
		t.Fatal("held request did not complete before cleanup")
	}
	if err := engine.StopAndRemove(ctx, blue, time.Second); err != nil {
		t.Fatal("remove drained slot")
	}
	bluePresent = false
	assertLiveOneShot(t, ctx, hostPort, appID, "/", "green", "replacement slot did not remain active")
	assertLiveOneShot(t, ctx, hostPort, otherAppID, "/", "other", "other application did not remain isolated")
	engine.ReleaseAdmission(green)
}

func liveCandidateSpec(version, appID, planID string) generatedruntime.CandidateSpec {
	identities := map[string][]string{
		"blue":  {"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"},
		"green": {"66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888"},
		"other": {"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"},
	}
	values := identities[version]
	return generatedruntime.CandidateSpec{
		AppID: appID, ReleaseID: values[0], DeploymentID: values[1], ArtifactID: values[2],
		DeploymentPlanRevisionID: planID, ComponentName: "api", Role: generatedruntime.RoleServer,
		RootDirectory: ".", RunCommand: "node server.mjs", InternalPort: 3000, HealthProbe: "/health",
		BuildDefinitionDigest: strings.Repeat(map[string]string{"blue": "a", "green": "b", "other": "c"}[version], 64),
	}
}

func buildLiveImage(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, root, dockerConfig, tag string, spec generatedruntime.CandidateSpec, version string) string {
	t.Helper()
	contextRoot := filepath.Join(root, "image-"+version)
	if err := os.Mkdir(contextRoot, 0o700); err != nil {
		t.Fatal("create image fixture")
	}
	containerfile := fmt.Sprintf("FROM %s\nWORKDIR /workspace\nCOPY --chmod=0555 rig-entrypoint /usr/local/bin/rig-entrypoint\nCOPY --chown=node:node server.mjs /workspace/server.mjs\nUSER node\nENTRYPOINT [\"/usr/local/bin/rig-entrypoint\"]\n", liveNodeImage)
	entrypoint := "#!/bin/sh\nset -eu\nexec \"$@\"\n"
	server := fmt.Sprintf("import { createServer } from 'node:http';\nconst version = %q;\nconst port = Number(process.env.RIG_RUNTIME_INTERNAL_PORT);\nlet resolveEntered;\nlet resolveReleased;\nconst entered = new Promise(resolve => { resolveEntered = resolve; });\nconst released = new Promise(resolve => { resolveReleased = resolve; });\ncreateServer(async (request, response) => {\n  if (request.url === '/unhealthy') { response.writeHead(503); response.end('unhealthy'); return; }\n  if (request.url === '/hold') { resolveEntered(); await released; response.writeHead(200, { 'content-type': 'text/plain' }); response.end(version); return; }\n  if (request.url === '/hold-ready') { await entered; response.writeHead(200); response.end('ready'); return; }\n  if (request.url === '/release') { resolveReleased(); response.writeHead(200); response.end('released'); return; }\n  response.writeHead(200, { 'content-type': 'text/plain' }); response.end(version);\n}).listen(port, '0.0.0.0');\n", version)
	for name, contents := range map[string]string{"Dockerfile": containerfile, "rig-entrypoint": entrypoint, "server.mjs": server} {
		if err := os.WriteFile(filepath.Join(contextRoot, name), []byte(contents), 0o600); err != nil {
			t.Fatal("write image fixture")
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
	result, err := runLiveDocker(ctx, runner, docker, root, dockerConfig, 5*time.Minute, args...)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("build live image")
	}
	clearLiveResult(&result)

	result, err = runLiveDocker(ctx, runner, docker, root, dockerConfig, 45*time.Second, "image", "inspect", "--format", "{{.ID}}", tag)
	defer clearLiveResult(&result)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		t.Fatal("inspect live image")
	}
	imageID := strings.TrimSpace(string(result.Stdout))
	if !liveImageID(imageID) {
		t.Fatal("inspect live image")
	}
	return imageID
}

func createAndStartLiveCandidate(t *testing.T, ctx context.Context, engine *generatedruntime.Engine, spec generatedruntime.CandidateSpec) generatedruntime.Candidate {
	t.Helper()
	candidate, err := engine.CreateInactiveCandidate(ctx, spec)
	if err != nil {
		t.Fatalf("create candidate: runtime=%s", liveRuntimeDiagnostic(err))
	}
	if err := engine.StartCandidate(ctx, candidate); err != nil {
		t.Fatalf("start candidate: runtime=%s", liveRuntimeDiagnostic(err))
	}
	return candidate
}

func startLiveCandidate(t *testing.T, ctx context.Context, engine *generatedruntime.Engine, spec generatedruntime.CandidateSpec) generatedruntime.Candidate {
	t.Helper()
	candidate := createAndStartLiveCandidate(t, ctx, engine, spec)
	if err := engine.WaitHealthy(ctx, candidate); err != nil {
		t.Fatalf("wait for candidate: runtime=%s", liveRuntimeDiagnostic(err))
	}
	return candidate
}

func liveEndpoint(candidate generatedruntime.Candidate) generatedruntime.RouteEndpoint {
	return generatedruntime.RouteEndpoint{
		Component: candidate.Component, Role: candidate.Role, ContainerID: candidate.ContainerID,
		NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias, InternalPort: candidate.InternalPort,
	}
}

type liveHTTPTransport string

const (
	liveHTTPTransportOK      liveHTTPTransport = "ok"
	liveHTTPTransportConnect liveHTTPTransport = "connect"
	liveHTTPTransportRefused liveHTTPTransport = "refused"
	liveHTTPTransportTimeout liveHTTPTransport = "timeout"
	liveHTTPTransportReset   liveHTTPTransport = "reset"
	liveHTTPTransportEOF     liveHTTPTransport = "eof"
	liveHTTPTransportStatus  liveHTTPTransport = "status"
	liveHTTPTransportOther   liveHTTPTransport = "other"
)

type liveHTTPResponse struct {
	transport liveHTTPTransport
	status    string
	bodyMatch bool
}

func liveRequest(ctx context.Context, port uint16, appID, path, expected string) liveHTTPResponse {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return liveHTTPResponse{transport: liveHTTPTransportOther, status: "none"}
	}
	request.Host = appID + ".rig.localhost"
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return liveHTTPResponse{transport: liveHTTPTransportForError(err), status: "none"}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		clearLiveBytes(body)
		return liveHTTPResponse{transport: liveHTTPTransportForError(err), status: liveHTTPStatusBucket(response.StatusCode)}
	}
	transport := liveHTTPTransportOK
	if response.StatusCode != http.StatusOK {
		transport = liveHTTPTransportStatus
	}
	matched := bytes.Equal(body, []byte(expected))
	clearLiveBytes(body)
	return liveHTTPResponse{transport: transport, status: liveHTTPStatusBucket(response.StatusCode), bodyMatch: matched}
}

func liveOneShotRequest(parent context.Context, port uint16, appID, path, expected string) liveHTTPResponse {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	return liveRequest(ctx, port, appID, path, expected)
}

func liveResponseMatches(response liveHTTPResponse) bool {
	return response.transport == liveHTTPTransportOK && response.status == "2xx" && response.bodyMatch
}

func assertLiveResponse(t *testing.T, port uint16, appID, path, expected string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	last := liveHTTPResponse{transport: liveHTTPTransportOther, status: "none"}
	for {
		requestCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		response := liveRequest(requestCtx, port, appID, path, expected)
		cancel()
		last = response
		if liveResponseMatches(response) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("generated ingress initial route unavailable: %s", liveHTTPDiagnostic(last))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func assertLiveOneShot(t *testing.T, ctx context.Context, port uint16, appID, path, expected, failure string) {
	t.Helper()
	response := liveOneShotRequest(ctx, port, appID, path, expected)
	if !liveResponseMatches(response) {
		t.Fatalf("%s: %s", failure, liveHTTPDiagnostic(response))
	}
}

func liveHTTPTransportForError(err error) liveHTTPTransport {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return liveHTTPTransportTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		return liveHTTPTransportRefused
	case errors.Is(err, syscall.ECONNRESET):
		return liveHTTPTransportReset
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return liveHTTPTransportEOF
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return liveHTTPTransportTimeout
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && operationError.Op == "dial" {
		return liveHTTPTransportConnect
	}
	return liveHTTPTransportOther
}

func liveHTTPStatusBucket(status int) string {
	switch {
	case status >= 100 && status < 200:
		return "1xx"
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "none"
	}
}

func liveHTTPDiagnostic(response liveHTTPResponse) string {
	return fmt.Sprintf("transport=%s,status=%s,body_match=%t", response.transport, response.status, response.bodyMatch)
}

func failLiveIngress(t *testing.T, phase string, err error) {
	t.Helper()
	var diagnostic *Error
	if errors.As(err, &diagnostic) {
		t.Fatalf("%s: ingress=%s", phase, diagnostic.Code)
	}
	t.Fatalf("%s: ingress=unclassified", phase)
}

func liveRuntimeDiagnostic(err error) string {
	var diagnostic *generatedruntime.Error
	if !errors.As(err, &diagnostic) || diagnostic == nil {
		return "unclassified"
	}
	switch diagnostic.Code {
	case generatedruntime.DiagnosticValidationFailed, generatedruntime.DiagnosticRuntimeUnavailable,
		generatedruntime.DiagnosticRuntimeTimeout, generatedruntime.DiagnosticProcessTerminationFailed,
		generatedruntime.DiagnosticRuntimeOutputTruncated, generatedruntime.DiagnosticImageUnavailable,
		generatedruntime.DiagnosticImageDriftDetected, generatedruntime.DiagnosticNetworkDriftDetected,
		generatedruntime.DiagnosticNetworkProvisionFailed, generatedruntime.DiagnosticCandidateSlotOccupied,
		generatedruntime.DiagnosticCandidateCreateFailed, generatedruntime.DiagnosticCandidateStartFailed,
		generatedruntime.DiagnosticCandidateHardeningFailed, generatedruntime.DiagnosticCandidateUnhealthy,
		generatedruntime.DiagnosticCandidateExited, generatedruntime.DiagnosticCandidateCleanupFailed,
		generatedruntime.DiagnosticInsufficientReplacementSpace, generatedruntime.DiagnosticConfigurationUnavailable,
		generatedruntime.DiagnosticInternalError, generatedruntime.DiagnosticCancelled:
		return string(diagnostic.Code)
	default:
		return "unclassified"
	}
}

func TestLiveRuntimeDiagnosticOmitsRawAndUnknownErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("sensitive output: %w", &generatedruntime.Error{Code: generatedruntime.DiagnosticCandidateHardeningFailed}), "candidate_hardening_failed"},
		{&generatedruntime.Error{Code: "sensitive_command"}, "unclassified"},
		{errors.New("sensitive output"), "unclassified"},
		{nil, "unclassified"},
	} {
		if got := liveRuntimeDiagnostic(test.err); got != test.want {
			t.Fatal("runtime diagnostic disclosed raw or unknown text")
		}
	}
}

func TestLiveHTTPDiagnosticIsRedacted(t *testing.T) {
	response := liveHTTPResponse{transport: liveHTTPTransportOther, status: "none"}
	const expected = "transport=other,status=none,body_match=false"
	if diagnostic := liveHTTPDiagnostic(response); diagnostic != expected || strings.Contains(diagnostic, "sensitive-body") {
		t.Fatal("live HTTP diagnostic disclosure")
	}
}

func TestLiveHTTPTransportClassifierUsesFixedCategories(t *testing.T) {
	for _, test := range []struct {
		err  error
		want liveHTTPTransport
	}{
		{fmt.Errorf("sensitive: %w", context.DeadlineExceeded), liveHTTPTransportTimeout},
		{fmt.Errorf("sensitive: %w", syscall.ECONNREFUSED), liveHTTPTransportRefused},
		{fmt.Errorf("sensitive: %w", syscall.ECONNRESET), liveHTTPTransportReset},
		{fmt.Errorf("sensitive: %w", io.ErrUnexpectedEOF), liveHTTPTransportEOF},
		{&net.OpError{Op: "dial", Err: errors.New("sensitive")}, liveHTTPTransportConnect},
		{errors.New("sensitive"), liveHTTPTransportOther},
	} {
		if got := liveHTTPTransportForError(test.err); got != test.want {
			t.Fatal("live HTTP transport classifier")
		}
	}
	for _, test := range []struct {
		status int
		want   string
	}{
		{0, "none"}, {100, "1xx"}, {200, "2xx"}, {300, "3xx"}, {400, "4xx"}, {500, "5xx"},
	} {
		if got := liveHTTPStatusBucket(test.status); got != test.want {
			t.Fatal("live HTTP status classifier")
		}
	}
}

func runLiveContainerControl(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig, containerID, command, failure string) {
	t.Helper()
	result, err := runLiveDocker(ctx, runner, docker, working, dockerConfig, 20*time.Second, "container", "exec", containerID, "node", "-e", command)
	defer clearLiveResult(&result)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		t.Fatal(failure)
	}
}

func runLiveDocker(ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig string, timeout time.Duration, args ...string) (runtimeprocess.CommandResult, error) {
	return runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: docker, Args: args, Directory: directory, Env: []string{"DOCKER_CONFIG=" + dockerConfig},
		Timeout: timeout, OutputLimit: liveDockerOutputLimit,
	})
}

func clearLiveResult(result *runtimeprocess.CommandResult) {
	clearLiveBytes(result.Stdout)
	clearLiveBytes(result.Stderr)
	result.Stdout = nil
	result.Stderr = nil
}

func clearLiveBytes(values []byte) {
	for index := range values {
		values[index] = 0
	}
}

func liveImageID(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, value := range value[len("sha256:"):] {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

func freeLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve loopback port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal("release loopback port")
	}
	return uint16(port)
}

func cleanupLiveDocker(runner runtimeprocess.CommandRunner, docker, root, dockerConfig string, appNetworks, imageTags []string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	commands := [][]string{
		{"container", "rm", "--force", caddyContainerName},
		{"volume", "rm", "--force", caddyVolumeName},
	}
	networkArgs := []string{"network", "rm"}
	networkArgs = append(networkArgs, appNetworks...)
	networkArgs = append(networkArgs, caddyNetworkName)
	commands = append(commands, networkArgs)
	imageArgs := []string{"image", "rm", "--force"}
	imageArgs = append(imageArgs, imageTags...)
	commands = append(commands, imageArgs)
	for _, args := range commands {
		result, _ := runLiveDocker(cleanupCtx, runner, docker, root, dockerConfig, 45*time.Second, args...)
		clearLiveResult(&result)
	}
}
