package generatedingress

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS != "linux" {
		t.Fatal("live application-origin isolation requires a local Linux Docker host")
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
	liveGatewayRequireLocalDocker(t, ctx, runner, docker, root, dockerConfig)

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
	blueSpec.ImageContentID = buildLiveImage(t, ctx, runner, docker, root, imageTags[0], blueSpec, "blue")
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
	otherSpec.ImageContentID = buildLiveImage(t, ctx, runner, docker, root, imageTags[2], otherSpec, "other")
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
	assertLiveHTTPResponseProbeControl(t, ctx, runner, docker, working, dockerConfig, blue)
	externalTLS := newLiveExternalTLSFixture(t)
	defer externalTLS.Close(t)
	assertLiveApplicationNetworkIsolation(t, ctx, runner, docker, working, dockerConfig, ingress, blue, other, hostPort, appID, "blue", otherAppID, "other", externalTLS)

	greenSpec := liveCandidateSpec("green", appID, planID)
	greenSpec.ActiveSlot = blue.Slot
	greenSpec.ImageContentID = buildLiveImage(t, ctx, runner, docker, root, imageTags[1], greenSpec, "green")

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
	assertLiveApplicationNetworkIsolation(t, ctx, runner, docker, working, dockerConfig, ingress, green, other, hostPort, appID, "green", otherAppID, "other", externalTLS)
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

func buildLiveImage(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, root, tag string, spec generatedruntime.CandidateSpec, version string) string {
	t.Helper()
	// Buildx writes state beneath DOCKER_CONFIG/buildx by default. Fixture
	// builds must never share the runtime engine's deliberately empty config.
	// Keep this outside the build context and discard it with the test.
	dockerConfig := t.TempDir()
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

type liveFixtureRunner func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)

func (run liveFixtureRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return run(ctx, request)
}

func TestLiveFixtureBuildKeepsRuntimeDockerConfigurationEmpty(t *testing.T) {
	runtimeConfig := t.TempDir()
	t.Setenv("DOCKER_CONFIG", runtimeConfig)
	var buildConfig string
	calls := 0
	imageID := "sha256:" + strings.Repeat("a", 64)
	runner := liveFixtureRunner(func(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		calls++
		if len(request.Env) != 1 || !strings.HasPrefix(request.Env[0], "DOCKER_CONFIG=") {
			t.Fatal("fixture Docker environment must be explicit")
		}
		config := strings.TrimPrefix(request.Env[0], "DOCKER_CONFIG=")
		if config == runtimeConfig || !filepath.IsAbs(config) {
			t.Fatal("fixture build used runtime Docker configuration")
		}
		if request.Args[0] == "build" {
			buildConfig = config
			// Model the Buildx fallback without requiring a Docker daemon.
			if err := os.Mkdir(filepath.Join(config, "buildx"), 0o700); err != nil {
				t.Fatal("create fixture Buildx state")
			}
			return runtimeprocess.CommandResult{}, nil
		}
		if config != buildConfig || request.Args[0] != "image" {
			t.Fatal("unexpected fixture Docker request")
		}
		return runtimeprocess.CommandResult{Stdout: []byte(imageID)}, nil
	})
	spec := liveCandidateSpec("blue", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	if got := buildLiveImage(t, context.Background(), runner, "docker", t.TempDir(), "rig-generated-live-test:blue", spec, "blue"); got != imageID || calls != 2 {
		t.Fatal("fixture image was not built and inspected")
	}
	entries, err := os.ReadDir(runtimeConfig)
	if err != nil || len(entries) != 0 {
		t.Fatal("fixture build contaminated runtime Docker configuration")
	}
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

const (
	liveApplicationInspectFormat = `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Config.Labels}},"running":{{json .State.Running}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"networkMode":{{json .HostConfig.NetworkMode}},"pidMode":{{json .HostConfig.PidMode}},"ipcMode":{{json .HostConfig.IpcMode}},"readOnly":{{json .HostConfig.ReadonlyRootfs}},"privileged":{{json .HostConfig.Privileged}},"binds":{{json .HostConfig.Binds}},"mounts":{{json .Mounts}},"portBindings":{{json .HostConfig.PortBindings}},"networks":{{if .NetworkSettings}}{{json .NetworkSettings.Networks}}{{else}}null{{end}}}`
	liveExternalTLSName          = "live-external.rig.test"
	liveApplicationProbeLimit    = 256
)

// The probe scripts emit only a small structured outcome. They do not inspect
// ambient environment, follow redirects, use proxies, or copy response bodies
// into Docker command output.
const liveApplicationHTTPProbeCommand = `const http=require('node:http');
const [host,port,expected]=process.argv.slice(-3);
let done=false,receivedStatus=0,timer;
const finish=value=>{if(done)return;done=true;clearTimeout(timer);process.stdout.write(JSON.stringify(value)+'\n',()=>process.exit(0));};
const received=bodyMatch=>finish({outcome:'response',status:receivedStatus,bodyMatch});
const request=http.request({host,port:Number(port),path:'/',method:'GET',agent:false,headers:{connection:'close'}},response=>{
  receivedStatus=response.statusCode;
  if(expected===''){received(true);response.destroy();return;}
  let body='';response.setEncoding('utf8');
  response.on('data',chunk=>{if(body.length+chunk.length>64){received(false);response.destroy();return;}body+=chunk;});
  response.once('end',()=>received(body===expected));
  response.once('error',()=>received(false));
  response.once('aborted',()=>received(false));
});
request.once('error',error=>{if(receivedStatus){received(false);}else{finish({outcome:'error',code:typeof error.code==='string'?error.code:'unknown'});}});
timer=setTimeout(()=>{if(receivedStatus){received(false);}request.destroy(Object.assign(new Error('timeout'),{code:'ETIMEDOUT'}));},1500);
request.end();`

const liveApplicationHTTPSProbeCommand = `const https=require('node:https');const [host,port,_expected,ca]=process.argv.slice(-4);let done=false,timer;const finish=value=>{if(done)return;done=true;clearTimeout(timer);process.stdout.write(JSON.stringify(value)+'\n',()=>process.exit(0));};const request=https.request({host,port:Number(port),servername:'live-external.rig.test',ca:Buffer.from(ca,'base64'),rejectUnauthorized:true,path:'/',method:'GET',agent:false,headers:{connection:'close'}},response=>{response.resume();response.once('end',()=>finish({outcome:'response',status:response.statusCode}));});timer=setTimeout(()=>request.destroy(Object.assign(new Error('timeout'),{code:'ETIMEDOUT'})),1500);request.once('error',error=>finish({outcome:'error',code:typeof error.code==='string'?error.code:'unknown'}));request.end();`

type liveApplicationInspection struct {
	ID           string                         `json:"id"`
	Name         string                         `json:"name"`
	Labels       map[string]string              `json:"labels"`
	Running      bool                           `json:"running"`
	Health       string                         `json:"health"`
	NetworkMode  string                         `json:"networkMode"`
	PIDMode      string                         `json:"pidMode"`
	IPCMode      string                         `json:"ipcMode"`
	ReadOnly     bool                           `json:"readOnly"`
	Privileged   bool                           `json:"privileged"`
	Binds        []string                       `json:"binds"`
	Mounts       []liveApplicationMount         `json:"mounts"`
	PortBindings map[string][]map[string]string `json:"portBindings"`
	Networks     map[string]*networkAttachment  `json:"networks"`
}

type liveApplicationMount struct {
	Type   string `json:"Type"`
	Source string `json:"Source"`
}

type liveAttestedApplication struct {
	candidate generatedruntime.Candidate
	address   netip.Addr
	gateway   netip.Addr
	network   netip.Prefix
}

type liveAttestedCaddy struct {
	id      string
	address netip.Addr
}

type liveExternalTLSFixture struct {
	server   *http.Server
	listener net.Listener
	port     uint16
	ca       string
}

type liveApplicationProbeResult struct {
	Outcome   string `json:"outcome"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	BodyMatch bool   `json:"bodyMatch"`
}

type liveApplicationProbeDisposition string

const (
	liveApplicationProbeReachable liveApplicationProbeDisposition = "reachable"
	liveApplicationProbeBlocked   liveApplicationProbeDisposition = "blocked"
	liveApplicationProbeUnknown   liveApplicationProbeDisposition = "unknown"
)

func assertLiveApplicationNetworkIsolation(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, ingress *Manager, first, second generatedruntime.Candidate, hostPort uint16, firstAppID, firstVersion, secondAppID, secondVersion string, external liveExternalTLSFixture) {
	t.Helper()
	firstAttested := attestLiveApplication(t, ctx, runner, docker, working, dockerConfig, first)
	secondAttested := attestLiveApplication(t, ctx, runner, docker, working, dockerConfig, second)
	if firstAttested.candidate.NetworkName == secondAttested.candidate.NetworkName || firstAttested.network == secondAttested.network {
		t.Fatal("application isolation fixture attached both candidates to one bridge")
	}
	caddy := attestLiveCaddyForApplication(t, ctx, ingress, firstAttested)
	secondCaddy := attestLiveCaddyForApplication(t, ctx, ingress, secondAttested)
	if caddy.id != secondCaddy.id {
		t.Fatal("application probes did not attest the same owned Caddy instance")
	}

	assertLiveApplicationProbeSuccess(t, ctx, runner, docker, working, dockerConfig, firstAttested.candidate, firstAttested.address, firstAttested.candidate.InternalPort, "first application own-network private target")
	assertLiveApplicationProbeSuccess(t, ctx, runner, docker, working, dockerConfig, secondAttested.candidate, secondAttested.address, secondAttested.candidate.InternalPort, "second application own-network private target")
	assertLiveApplicationAliasSuccess(t, ctx, runner, docker, working, dockerConfig, firstAttested.candidate, firstAttested.candidate.NetworkAlias, firstVersion, "first application alias did not serve its owned fixture")
	assertLiveApplicationAliasSuccess(t, ctx, runner, docker, working, dockerConfig, secondAttested.candidate, secondAttested.candidate.NetworkAlias, secondVersion, "second application alias did not serve its owned fixture")
	assertLiveApplicationProbeDenied(t, ctx, runner, docker, working, dockerConfig, firstAttested.candidate, secondAttested.address, secondAttested.candidate.InternalPort, "first application reached second private address")
	assertLiveApplicationProbeDenied(t, ctx, runner, docker, working, dockerConfig, secondAttested.candidate, firstAttested.address, firstAttested.candidate.InternalPort, "second application reached first private address")
	assertLiveApplicationAliasDenied(t, ctx, runner, docker, working, dockerConfig, firstAttested.candidate, secondAttested.candidate.NetworkAlias+"."+secondAttested.candidate.NetworkName, secondAttested.candidate.InternalPort, "first application exposed second qualified private alias")
	assertLiveApplicationAliasDenied(t, ctx, runner, docker, working, dockerConfig, secondAttested.candidate, firstAttested.candidate.NetworkAlias+"."+firstAttested.candidate.NetworkName, firstAttested.candidate.InternalPort, "second application exposed first qualified private alias")
	assertLiveApplicationHTTPSProbeSuccess(t, ctx, runner, docker, working, dockerConfig, firstAttested.candidate, firstAttested.gateway, external, "first application could not reach external TLS fixture")
	assertLiveApplicationHTTPSProbeSuccess(t, ctx, runner, docker, working, dockerConfig, secondAttested.candidate, secondAttested.gateway, external, "second application could not reach external TLS fixture")
	assertLiveCaddyAdminControl(t, ctx, runner, docker, working, dockerConfig, caddy.id)
	assertLiveApplicationProbeDenied(t, ctx, runner, docker, working, dockerConfig, firstAttested.candidate, caddy.address, 2019, "application reached Caddy private admin address")
	assertLiveApplicationProbeDenied(t, ctx, runner, docker, working, dockerConfig, secondAttested.candidate, secondCaddy.address, 2019, "second application reached Caddy private admin address")
	assertLiveOneShot(t, ctx, hostPort, firstAppID, "/", firstVersion, "first public route did not remain available after application-origin probes")
	assertLiveOneShot(t, ctx, hostPort, secondAppID, "/", secondVersion, "second public route did not remain available after application-origin probes")
}

func attestLiveApplication(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, candidate generatedruntime.Candidate) liveAttestedApplication {
	t.Helper()
	if ctx.Err() != nil || !validContainerID(candidate.ContainerID) {
		t.Fatal("application candidate attestation was invalid")
	}
	result, err := runLiveDocker(ctx, runner, docker, working, dockerConfig, 10*time.Second, "container", "inspect", "--format", liveApplicationInspectFormat, candidate.ContainerID)
	defer clearLiveResult(&result)
	if ctx.Err() != nil || err != nil || result.StdoutTruncated || result.StderrTruncated {
		t.Fatal("application candidate attestation did not complete")
	}
	var inspection liveApplicationInspection
	if err := json.Unmarshal(result.Stdout, &inspection); err != nil || !validLiveApplicationInspection(inspection, candidate) {
		t.Fatal("application candidate attestation did not match its owned identity")
	}
	attachment := inspection.Networks[candidate.NetworkName]
	address, err := netip.ParseAddr(attachment.IPAddress)
	if err != nil || !address.Is4() || !address.IsPrivate() {
		t.Fatal("application candidate did not have a private bridge address")
	}

	network := attestLiveApplicationBridge(t, ctx, runner, docker, working, dockerConfig, candidate)
	if !network.prefix.Contains(address) || address == network.gateway {
		t.Fatal("application candidate bridge address did not match its owned network")
	}
	return liveAttestedApplication{candidate: candidate, address: address, gateway: network.gateway, network: network.prefix}
}

func validLiveApplicationInspection(value liveApplicationInspection, candidate generatedruntime.Candidate) bool {
	if normalizeID(value.ID) != normalizeID(candidate.ContainerID) || strings.TrimPrefix(value.Name, "/") != candidate.ContainerName || !value.Running || value.Health != "healthy" || value.NetworkMode != candidate.NetworkName || value.PIDMode != "" || (value.IPCMode != "" && value.IPCMode != "private") || !value.ReadOnly || value.Privileged || len(value.Binds) != 0 || len(value.PortBindings) != 0 || len(value.Networks) != 1 || !liveContainsLabels(value.Labels, liveCandidateLabels(candidate)) {
		return false
	}
	for _, mount := range value.Mounts {
		if mount.Type != "tmpfs" || mount.Source != "" {
			return false
		}
	}
	attachment := value.Networks[candidate.NetworkName]
	return attachment != nil && liveContainsString(attachment.Aliases, candidate.NetworkAlias)
}

func liveCandidateLabels(candidate generatedruntime.Candidate) map[string]string {
	return map[string]string{
		"io.rig.managed": "generated-runtime", "io.rig.application": candidate.AppID,
		"io.rig.release": candidate.ReleaseID, "io.rig.deployment": candidate.DeploymentID,
		"io.rig.artifact": candidate.ArtifactID, "io.rig.plan": candidate.DeploymentPlanRevisionID,
		"io.rig.component": candidate.Component, "io.rig.slot": string(candidate.Slot), "io.rig.role": candidate.Role,
	}
}

func liveContainsLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func liveContainsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type liveApplicationBridge struct {
	prefix  netip.Prefix
	gateway netip.Addr
}

func attestLiveApplicationBridge(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, candidate generatedruntime.Candidate) liveApplicationBridge {
	t.Helper()
	result, err := runLiveDocker(ctx, runner, docker, working, dockerConfig, 10*time.Second, "network", "inspect", "--format", networkInspectFormat, candidate.NetworkName)
	defer clearLiveResult(&result)
	if ctx.Err() != nil || err != nil || result.StdoutTruncated || result.StderrTruncated {
		t.Fatal("application bridge attestation did not complete")
	}
	var network networkInspection
	if err := json.Unmarshal(result.Stdout, &network); err != nil || !validApplicationNetwork(network, candidate.AppID) || len(network.IPAM) != 1 {
		t.Fatal("application bridge attestation did not match its owned identity")
	}
	prefix, err := netip.ParsePrefix(network.IPAM[0].Subnet)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
		t.Fatal("application bridge did not have a private IPv4 subnet")
	}
	gateway, err := netip.ParseAddr(network.IPAM[0].Gateway)
	if err != nil || !gateway.Is4() || !gateway.IsPrivate() || !prefix.Contains(gateway) {
		t.Fatal("application bridge did not have an attested private gateway")
	}
	return liveApplicationBridge{prefix: prefix, gateway: gateway}
}

func attestLiveCaddyForApplication(t *testing.T, ctx context.Context, ingress *Manager, application liveAttestedApplication) liveAttestedCaddy {
	t.Helper()
	caddyID, err := ingress.attestedCaddyForGateway(ctx, routeRecord{Slot: application.candidate.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(application.candidate)}})
	if err != nil {
		failLiveIngress(t, "attest Caddy for application-origin probe", err)
	}
	caddy, found, err := ingress.inspectCaddy(ctx)
	if err != nil || !found || normalizeID(caddy.ID) != normalizeID(caddyID) || !caddy.Running || caddy.Restarting || caddy.Labels["io.rig.managed"] != "generated-ingress" {
		t.Fatal("Caddy application-origin attestation did not match the pinned ingress identity")
	}
	attachment := caddy.Networks[application.candidate.NetworkName]
	if attachment == nil {
		t.Fatal("attested Caddy was not attached to the application bridge")
	}
	address, err := netip.ParseAddr(attachment.IPAddress)
	if err != nil || !address.Is4() || !address.IsPrivate() || !application.network.Contains(address) || address == application.address || address == application.gateway {
		t.Fatal("attested Caddy did not have a private application bridge address")
	}
	return liveAttestedCaddy{id: caddyID, address: address}
}

func assertLiveCaddyAdminControl(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig, caddyID string) {
	t.Helper()
	result, err := runLiveDocker(ctx, runner, docker, working, dockerConfig, 8*time.Second,
		"container", "exec", caddyID,
		"curl", "--disable", "--silent", "--show-error", "--fail", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2", "http://127.0.0.1:2019/config/",
	)
	defer clearLiveResult(&result)
	if ctx.Err() != nil || err != nil || result.StdoutTruncated || result.StderrTruncated || string(result.Stdout) != "200" {
		t.Fatal("attested Caddy localhost admin control did not return a read-only success status")
	}
}

// Exercise the actual probe script against a listener that sends HTTP error
// headers and deliberately never ends the response. Observed HTTP headers
// must remain reachable, including when an alias body comparison times out.
const liveHTTPResponseProbeControlCommand = `const http=require('node:http');const {spawn}=require('node:child_process');
const script=process.argv.at(-1);
async function probe(command,args){
  return new Promise((resolve,reject)=>{
    const child=spawn(process.execPath,['-e',command,...args],{stdio:['ignore','pipe','ignore']});
    let output='';const timer=setTimeout(()=>{child.kill();reject(new Error('control deadline'));},4000);
    child.stdout.on('data',chunk=>{output+=chunk;if(output.length>256){child.kill();reject(new Error('control output'));}});
    child.once('error',error=>{clearTimeout(timer);reject(error);});
    child.once('close',code=>{clearTimeout(timer);if(code!==0){reject(new Error('control exit'));return;}try{resolve(JSON.parse(output));}catch{reject(new Error('control result'));}});
  });
}
async function control(expected,status){
  const server=http.createServer((_request,response)=>{response.writeHead(status);response.flushHeaders();});
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  try{
    const value=await probe(script,['127.0.0.1',String(server.address().port),expected]);
    if(value.outcome!=='response'||value.status!==status||value.code!==undefined||value.bodyMatch!==(expected===''))throw new Error('HTTP headers lost');
  }finally{server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
}
async function unconnected(){
  const fake="{const http=require('node:http');const {EventEmitter}=require('node:events');http.request=()=>{const request=new EventEmitter();request.end=()=>{};request.destroy=error=>queueMicrotask(()=>request.emit('error',error));return request;};}\n"+script;
  const value=await probe(fake,['127.0.0.1','1','']);
  if(value.outcome!=='error'||value.code!=='ETIMEDOUT'||value.status!==undefined)throw new Error('unconnected request did not reach probe deadline');
}
(async()=>{await control('',403);await control('expected',401);await unconnected();process.stdout.write('3',()=>process.exit(0));})().catch(()=>process.exit(1));`

func assertLiveHTTPResponseProbeControl(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate) {
	t.Helper()
	result, err := runLiveDocker(ctx, runner, docker, working, dockerConfig, 10*time.Second,
		"container", "exec", source.ContainerID, "node", "-e", liveHTTPResponseProbeControlCommand, liveApplicationHTTPProbeCommand)
	defer clearLiveResult(&result)
	if ctx.Err() != nil || err != nil || result.StdoutTruncated || result.StderrTruncated || string(result.Stdout) != "3" {
		t.Fatal("HTTP response and unconnected socket controls did not retain their distinct classifications")
	}
}

func assertLiveApplicationProbeSuccess(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, target netip.Addr, port uint16, failure string) {
	t.Helper()
	result := runLiveApplicationProbe(t, ctx, runner, docker, working, dockerConfig, source, liveApplicationHTTPProbeCommand, target, port, "", "", failure)
	if liveApplicationProbeClassification(result) != liveApplicationProbeReachable || result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices {
		t.Fatalf("%s: %s", failure, liveApplicationProbeDiagnostic(result))
	}
}

func assertLiveApplicationProbeDenied(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, target netip.Addr, port uint16, failure string) {
	t.Helper()
	result := runLiveApplicationProbe(t, ctx, runner, docker, working, dockerConfig, source, liveApplicationHTTPProbeCommand, target, port, "", "", failure)
	if liveApplicationProbeClassification(result) != liveApplicationProbeBlocked {
		t.Fatalf("%s: %s", failure, liveApplicationProbeDiagnostic(result))
	}
}

func assertLiveApplicationAliasSuccess(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, alias, expected, failure string) {
	t.Helper()
	if !validName(alias, 96) {
		t.Fatal("application-origin alias probe had an invalid owned alias")
	}
	result := runLiveApplicationProbeTarget(t, ctx, runner, docker, working, dockerConfig, source, liveApplicationHTTPProbeCommand, alias, source.InternalPort, expected, "", failure)
	if liveApplicationProbeClassification(result) != liveApplicationProbeReachable || result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices || !result.BodyMatch {
		t.Fatalf("%s: %s", failure, liveApplicationProbeDiagnostic(result))
	}
}

func assertLiveApplicationAliasDenied(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, alias string, port uint16, failure string) {
	t.Helper()
	if !strings.Contains(alias, ".") {
		t.Fatal("application-origin alias probe did not use a qualified private alias")
	}
	result := runLiveApplicationProbeTarget(t, ctx, runner, docker, working, dockerConfig, source, liveApplicationHTTPProbeCommand, alias, port, "", "", failure)
	if liveApplicationProbeClassification(result) != liveApplicationProbeBlocked {
		t.Fatalf("%s: %s", failure, liveApplicationProbeDiagnostic(result))
	}
}

func assertLiveApplicationHTTPSProbeSuccess(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, gateway netip.Addr, external liveExternalTLSFixture, failure string) {
	t.Helper()
	result := runLiveApplicationProbe(t, ctx, runner, docker, working, dockerConfig, source, liveApplicationHTTPSProbeCommand, gateway, external.port, "", external.ca, failure)
	if liveApplicationProbeClassification(result) != liveApplicationProbeReachable || result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices {
		t.Fatalf("%s: %s", failure, liveApplicationProbeDiagnostic(result))
	}
}

func runLiveApplicationProbe(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, command string, target netip.Addr, port uint16, expected, ca, failure string) liveApplicationProbeResult {
	t.Helper()
	if !target.Is4() {
		t.Fatal("application-origin probe had an invalid attested target")
	}
	return runLiveApplicationProbeTarget(t, ctx, runner, docker, working, dockerConfig, source, command, target.String(), port, expected, ca, failure)
}

func runLiveApplicationProbeTarget(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, working, dockerConfig string, source generatedruntime.Candidate, command, target string, port uint16, expected, ca, failure string) liveApplicationProbeResult {
	t.Helper()
	if ctx.Err() != nil || !validContainerID(source.ContainerID) || target == "" || port == 0 {
		t.Fatal("application-origin probe had an invalid attested target")
	}
	args := []string{"container", "exec", source.ContainerID, "node", "-e", command, target, fmt.Sprintf("%d", port), expected}
	if ca != "" {
		args = append(args, ca)
	}
	result, err := runLiveDocker(ctx, runner, docker, working, dockerConfig, 8*time.Second, args...)
	defer clearLiveResult(&result)
	if ctx.Err() != nil || err != nil || result.StdoutTruncated || result.StderrTruncated {
		reason := "command failed"
		var exitError *exec.ExitError
		switch {
		case ctx.Err() != nil:
			reason = "parent canceled"
		case errors.Is(err, context.DeadlineExceeded):
			reason = "command deadline"
		case errors.As(err, &exitError):
			reason = fmt.Sprintf("command exit %d", exitError.ExitCode())
		case result.StdoutTruncated || result.StderrTruncated:
			reason = "output truncated"
		}
		t.Fatalf("%s: application-origin probe did not complete (%s)", failure, reason)
	}
	value, valid := decodeLiveApplicationProbeResult(result.Stdout)
	if !valid {
		t.Fatalf("%s: application-origin probe returned an invalid or unknown result (%s)", failure, liveApplicationProbeFailureSummary(result.Stdout))
	}
	return value
}

func liveApplicationProbeFailureSummary(output []byte) string {
	if len(output) == 0 {
		return "empty output"
	}
	if len(output) > liveApplicationProbeLimit {
		return "oversized output"
	}
	var value liveApplicationProbeResult
	if json.Unmarshal(output, &value) != nil {
		return "malformed output"
	}
	if value.Outcome == "error" {
		if len(value.Code) == 0 || len(value.Code) > 32 {
			return "invalid transport code"
		}
		for _, character := range value.Code {
			if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
				return "invalid transport code"
			}
		}
		return "transport code " + value.Code
	}
	if value.Outcome == "response" {
		return fmt.Sprintf("response status %d", value.Status)
	}
	return "unknown outcome"
}

func decodeLiveApplicationProbeResult(value []byte) (liveApplicationProbeResult, bool) {
	var result liveApplicationProbeResult
	if len(value) == 0 || len(value) > liveApplicationProbeLimit {
		return liveApplicationProbeResult{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return liveApplicationProbeResult{}, false
	}
	switch result.Outcome {
	case "response":
		return result, result.Status >= 100 && result.Status <= 599 && result.Code == ""
	case "error":
		return result, result.Status == 0 && !result.BodyMatch && liveApplicationTransportDenied(result.Code)
	default:
		return liveApplicationProbeResult{}, false
	}
}

func liveApplicationProbeClassification(result liveApplicationProbeResult) liveApplicationProbeDisposition {
	if result.Outcome == "response" && result.Status >= 100 && result.Status <= 599 && result.Code == "" {
		return liveApplicationProbeReachable
	}
	if result.Outcome == "error" && result.Status == 0 && !result.BodyMatch && liveApplicationTransportDenied(result.Code) {
		return liveApplicationProbeBlocked
	}
	return liveApplicationProbeUnknown
}

func liveApplicationTransportDenied(code string) bool {
	switch code {
	case "EACCES", "ECONNREFUSED", "ECONNRESET", "EHOSTUNREACH", "ENETUNREACH", "ENOTFOUND", "ETIMEDOUT":
		return true
	default:
		return false
	}
}

func liveApplicationProbeDiagnostic(result liveApplicationProbeResult) string {
	return fmt.Sprintf("outcome=%s,status=%d,code=%s", result.Outcome, result.Status, result.Code)
}

func newLiveExternalTLSFixture(t *testing.T) liveExternalTLSFixture {
	t.Helper()
	certificate, ca := newLiveExternalTLSCertificate(t)
	listener, err := tls.Listen("tcp4", "0.0.0.0:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal("start external TLS fixture")
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("external TLS fixture stopped unexpectedly")
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	if port <= 0 || port > int(^uint16(0)) {
		_ = listener.Close()
		t.Fatal("external TLS fixture had an invalid listener port")
	}
	return liveExternalTLSFixture{server: server, listener: listener, port: uint16(port), ca: base64.StdEncoding.EncodeToString(ca)}
}

func (fixture liveExternalTLSFixture) Close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fixture.server.Shutdown(ctx); err != nil {
		t.Error("stop external TLS fixture")
	}
	if err := fixture.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Error("close external TLS fixture")
	}
}

func newLiveExternalTLSCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate external TLS fixture CA")
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal("sign external TLS fixture CA")
	}
	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate external TLS fixture leaf")
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), DNSNames: []string{liveExternalTLSName}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, leafPublic, caPrivate)
	if err != nil {
		t.Fatal("sign external TLS fixture leaf")
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafPrivate)
	if err != nil {
		t.Fatal("encode external TLS fixture leaf key")
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}))
	if err != nil {
		t.Fatal("load external TLS fixture certificate")
	}
	return certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
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

func TestLiveApplicationProbeClassificationFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		value liveApplicationProbeResult
		want  liveApplicationProbeDisposition
	}{
		{name: "successful response is reachable", value: liveApplicationProbeResult{Outcome: "response", Status: http.StatusNoContent}, want: liveApplicationProbeReachable},
		{name: "forbidden status is still reachable", value: liveApplicationProbeResult{Outcome: "response", Status: http.StatusForbidden}, want: liveApplicationProbeReachable},
		{name: "unauthorized status is still reachable", value: liveApplicationProbeResult{Outcome: "response", Status: http.StatusUnauthorized}, want: liveApplicationProbeReachable},
		{name: "missing path is still reachable", value: liveApplicationProbeResult{Outcome: "response", Status: http.StatusNotFound}, want: liveApplicationProbeReachable},
		{name: "network denial is blocked", value: liveApplicationProbeResult{Outcome: "error", Code: "ENETUNREACH"}, want: liveApplicationProbeBlocked},
		{name: "DNS denial is supplementary only", value: liveApplicationProbeResult{Outcome: "error", Code: "ENOTFOUND"}, want: liveApplicationProbeBlocked},
		{name: "unknown error fails closed", value: liveApplicationProbeResult{Outcome: "error", Code: "EUNKNOWN"}, want: liveApplicationProbeUnknown},
		{name: "contradictory response and denial fails closed", value: liveApplicationProbeResult{Outcome: "error", Status: http.StatusForbidden, Code: "ETIMEDOUT"}, want: liveApplicationProbeUnknown},
		{name: "contradictory body and denial fails closed", value: liveApplicationProbeResult{Outcome: "error", Code: "ECONNREFUSED", BodyMatch: true}, want: liveApplicationProbeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := liveApplicationProbeClassification(test.value); got != test.want {
				t.Fatalf("classification=%s want=%s", got, test.want)
			}
		})
	}
	for _, value := range [][]byte{
		[]byte(`{"outcome":"response","status":600}`),
		[]byte(`{"outcome":"error","code":"EUNKNOWN"}`),
		[]byte(`not-json`),
		[]byte(``),
		[]byte(`null`),
		[]byte(`{"outcome":"error","code":"ECONNREFUSED","unexpected":true}`),
		[]byte(`{"outcome":"error","code":"ECONNREFUSED"} {}`),
		[]byte(`{"outcome":"error","code":"ECONNREFUSED","status":403}`),
		[]byte(`{"outcome":"error","code":"ECONNREFUSED","bodyMatch":true}`),
		bytes.Repeat([]byte(" "), liveApplicationProbeLimit+1),
	} {
		if _, valid := decodeLiveApplicationProbeResult(value); valid {
			t.Fatal("unknown probe result was accepted")
		}
	}
	for _, test := range []struct {
		output []byte
		want   string
	}{
		{[]byte(`{"outcome":"error","code":"EAI_AGAIN"}`), "transport code EAI_AGAIN"},
		{[]byte(`{"outcome":"error","code":"secret://host"}`), "invalid transport code"},
		{[]byte(`{"outcome":"response","status":403}`), "response status 403"},
		{[]byte(`not-json`), "malformed output"},
	} {
		if got := liveApplicationProbeFailureSummary(test.output); got != test.want {
			t.Fatalf("safe failure summary=%q want=%q", got, test.want)
		}
	}
}

func TestLiveApplicationInspectionRetainsOwnedIsolation(t *testing.T) {
	candidate := generatedruntime.Candidate{
		ContainerID: strings.Repeat("a", 64), ContainerName: "owned-api-blue",
		AppID: "11111111-1111-4111-8111-111111111111", NetworkName: "owned-network", NetworkAlias: "api-blue",
		ReleaseID: "release", DeploymentID: "deployment", ArtifactID: "artifact", DeploymentPlanRevisionID: "plan",
		Component: "api", Slot: generatedruntime.SlotBlue, Role: generatedruntime.RoleServer,
	}
	for _, test := range []struct {
		name   string
		mutate func(*liveApplicationInspection)
	}{
		{"wrong container", func(value *liveApplicationInspection) { value.ID = strings.Repeat("b", 64) }},
		{"wrong name", func(value *liveApplicationInspection) { value.Name = "/other" }},
		{"stopped source", func(value *liveApplicationInspection) { value.Running = false }},
		{"unhealthy source", func(value *liveApplicationInspection) { value.Health = "unhealthy" }},
		{"wrong owner", func(value *liveApplicationInspection) { value.Labels["io.rig.application"] = "other" }},
		{"host network", func(value *liveApplicationInspection) { value.NetworkMode = "host" }},
		{"host PID", func(value *liveApplicationInspection) { value.PIDMode = "host" }},
		{"shared PID", func(value *liveApplicationInspection) { value.PIDMode = "container:other" }},
		{"host IPC", func(value *liveApplicationInspection) { value.IPCMode = "host" }},
		{"shared IPC", func(value *liveApplicationInspection) { value.IPCMode = "container:other" }},
		{"writable root", func(value *liveApplicationInspection) { value.ReadOnly = false }},
		{"privileged", func(value *liveApplicationInspection) { value.Privileged = true }},
		{"binds", func(value *liveApplicationInspection) {
			value.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"}
		}},
		{"host socket mount", func(value *liveApplicationInspection) {
			value.Mounts = []liveApplicationMount{{Type: "bind", Source: "/var/run/docker.sock"}}
		}},
		{"named volume", func(value *liveApplicationInspection) {
			value.Mounts = []liveApplicationMount{{Type: "volume", Source: "other"}}
		}},
		{"tmpfs with host source", func(value *liveApplicationInspection) { value.Mounts[0].Source = "/host" }},
		{"published port", func(value *liveApplicationInspection) {
			value.PortBindings = map[string][]map[string]string{"3000/tcp": {{"HostPort": "3000"}}}
		}},
		{"second network", func(value *liveApplicationInspection) { value.Networks["other"] = &networkAttachment{} }},
		{"missing attachment", func(value *liveApplicationInspection) { value.Networks = nil }},
		{"nil attachment", func(value *liveApplicationInspection) { value.Networks[candidate.NetworkName] = nil }},
		{"wrong alias", func(value *liveApplicationInspection) {
			value.Networks[candidate.NetworkName].Aliases = []string{"other"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := liveApplicationInspection{ID: candidate.ContainerID, Name: "/" + candidate.ContainerName,
				Labels: liveCandidateLabels(candidate), Running: true, Health: "healthy", NetworkMode: candidate.NetworkName,
				ReadOnly: true, IPCMode: "private", Mounts: []liveApplicationMount{{Type: "tmpfs"}},
				Networks: map[string]*networkAttachment{candidate.NetworkName: {Aliases: []string{candidate.NetworkAlias}}}}
			if !validLiveApplicationInspection(value, candidate) {
				t.Fatal("owned private fixture with tmpfs was rejected")
			}
			test.mutate(&value)
			if validLiveApplicationInspection(value, candidate) {
				t.Fatal("unsafe or foreign running-container inspection was accepted")
			}
		})
	}
}

func TestLiveExternalTLSCertificateVerifiesFixtureName(t *testing.T) {
	certificate, ca := newLiveExternalTLSCertificate(t)
	block, rest := pem.Decode(ca)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 || len(certificate.Certificate) != 1 {
		t.Fatal("external TLS fixture did not produce one CA-signed leaf")
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal("parse external TLS fixture CA")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal("parse external TLS fixture leaf")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: liveExternalTLSName, CurrentTime: time.Now()}); err != nil {
		t.Fatal("external TLS fixture leaf did not verify against its CA")
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
