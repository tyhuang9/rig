package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/hostnetwork"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	liveGatewayV2ConflictContainer = "rig-gateway-v2-live-bind-conflict"
	liveGatewayV2ConflictManaged   = "gateway-v2-live-bind-conflict"
	liveGatewayV2ConflictBody      = "rig-gateway-v2-bind-conflict"
)

type liveGatewayV2FixtureSpec struct {
	appID            string
	planID           string
	operationID      string
	profileRevision  string
	approvedBy       string
	imageTag         string
	applicationReply string
}

type liveGatewayV2Fixture struct {
	ctx          context.Context
	runner       runtimeprocess.CommandRunner
	docker       string
	root         string
	dockerConfig string
	stateRoot    string
	ingress      *Manager
	candidates   []generatedruntime.Candidate
	spec         liveGatewayV2FixtureSpec
	interfaceIP  string
	port         uint16
	request      GatewayV2UpgradeRequest
	source       routeState
}

// TestLiveGatewayV2UpgradeCommitAndRestart is the hosted Linux acceptance
// gate for the production v1-to-v2 coordinator. It deliberately uses one
// unassigned LAN port so every published LAN response must remain a 404.
func TestLiveGatewayV2UpgradeCommitAndRestart(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "10101010-1010-4010-8010-101010101010",
		planID:           "20202020-2020-4020-8020-202020202020",
		operationID:      "30303030-3030-4030-8030-303030303030",
		profileRevision:  "40404040-4040-4040-8040-404040404040",
		approvedBy:       "50505050-5050-4050-8050-505050505050",
		imageTag:         "rig-generated-gateway-v2-live:commit",
		applicationReply: "gateway-v2-commit",
	})

	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request, liveGatewayV2Authorizer(t, fixture.request))
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		failLiveIngress(t, "commit gateway-v2 upgrade", err)
	}
	liveGatewayV2AssertSourceUnchanged(t, fixture)
	assertLiveOneShot(t, fixture.ctx, fixture.ingress.options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply,
		"gateway-v2 did not preserve the local application route")
	liveGatewayV2AssertUnassigned404(t, fixture, fixture.interfaceIP)

	state, journal, store := liveGatewayV2LoadDurableOperation(t, fixture)
	if journal.Phase != gatewayPhaseCommitted || !gatewayV2RequestMatchesState(fixture.request, state, journal) {
		t.Fatal("gateway-v2 committed result was not durably bound to the exact request")
	}
	liveGatewayV2AssertCommittedIdentity(t, fixture, state, journal)
	if _, err := store.loadRollbackRetirementReceipt(state, journal); err == nil {
		t.Fatal("committed gateway-v2 operation unexpectedly has a rollback-retirement receipt")
	}

	restarted, err := New(fixture.runner, fixture.ingress.options)
	if err != nil {
		t.Fatal("restart generated ingress manager")
	}
	status, err := restarted.ObserveGatewayV2Operation(fixture.ctx, fixture.spec.operationID)
	if err != nil || status.OperationID != fixture.spec.operationID || status.Availability != GatewayV2OperationServing {
		t.Fatalf("restarted gateway-v2 observation = %+v, err=%v", status, err)
	}
	observation, err := restarted.Observe(fixture.ctx, fixture.spec.appID)
	wantURL := "http://" + fixture.spec.appID + ".rig.localhost:" + strconv.FormatUint(uint64(fixture.ingress.options.HostPort), 10)
	if err != nil || observation.URL != wantURL || observation.Slot != fixture.candidates[0].Slot ||
		!reflect.DeepEqual(observation.Endpoints, []generatedruntime.RouteEndpoint{liveEndpoint(fixture.candidates[0])}) {
		t.Fatalf("restarted application observation = %+v, err=%v", observation, err)
	}
	assertLiveOneShot(t, fixture.ctx, fixture.ingress.options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply,
		"restarted manager did not observe the preserved local route")
	liveGatewayV2AssertUnassigned404(t, fixture, fixture.interfaceIP)
}

// TestLiveGatewayV2BindConflictRollsBack occupies the approved host listener
// with a separate, test-owned Docker container. The production stage start
// must fail closed, restore the exact v1 route, and retire all v2 resources.
func TestLiveGatewayV2BindConflictRollsBack(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "61616161-6161-4161-8161-616161616161",
		planID:           "62626262-6262-4262-8262-626262626262",
		operationID:      "63636363-6363-4363-8363-636363636363",
		profileRevision:  "64646464-6464-4464-8464-646464646464",
		approvedBy:       "65656565-6565-4565-8565-656565656565",
		imageTag:         "rig-generated-gateway-v2-live:conflict",
		applicationReply: "gateway-v2-conflict",
	})

	var conflictID string
	t.Cleanup(func() {
		liveGatewayV2RemoveConflictContainer(t, fixture, conflictID)
	})
	conflictID = liveGatewayV2StartConflictContainer(t, fixture)
	liveGatewayV2AssertConflictBinding(t, fixture, conflictID)
	liveGatewayV2AwaitConflict(t, fixture, "bind-conflict fixture did not own the approved listener")

	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request, liveGatewayV2Authorizer(t, fixture.request))
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		t.Fatalf("bind-conflict upgrade = %+v, err=%v", result, err)
	}
	liveGatewayV2AssertSourceUnchanged(t, fixture)
	assertLiveOneShot(t, fixture.ctx, fixture.ingress.options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply,
		"bind-conflict rollback did not preserve the v1 local route")
	liveGatewayV2AwaitConflict(t, fixture, "gateway-v2 leaked onto the conflict-owned listener")
	liveGatewayV2AssertNoOwnedResources(t, fixture)

	state, journal, store := liveGatewayV2LoadDurableOperation(t, fixture)
	if journal.Phase != gatewayPhaseRolledBack || !gatewayV2RequestMatchesState(fixture.request, state, journal) {
		t.Fatal("bind-conflict rollback was not retained as the exact durable operation")
	}
	if !validContainerID(journal.Resources.StageContainerID) || journal.Resources.FinalContainerID != "" ||
		!validContainerID(journal.Resources.IngressNetworkID) ||
		journal.Resources.ConfigVolume == (gatewayV2VolumeResourceBinding{}) ||
		journal.Resources.DataVolume == (gatewayV2VolumeResourceBinding{}) {
		t.Fatal("bind-conflict rollback did not reach the journal-bound stage start")
	}
	if _, err := store.loadRollbackRetirementReceipt(state, journal); err != nil {
		t.Fatal("bind-conflict rollback did not durably retire its v2 resources")
	}

	restarted, err := New(fixture.runner, fixture.ingress.options)
	if err != nil {
		t.Fatal("restart generated ingress manager after bind conflict")
	}
	status, err := restarted.ObserveGatewayV2Operation(fixture.ctx, fixture.spec.operationID)
	if err != nil || status.OperationID != fixture.spec.operationID || status.Availability != GatewayV2OperationUnavailable {
		t.Fatalf("restarted rollback observation = %+v, err=%v", status, err)
	}
	assertLiveOneShot(t, fixture.ctx, fixture.ingress.options.HostPort, fixture.spec.appID, "/", fixture.spec.applicationReply,
		"restarted manager did not preserve v1 after bind-conflict rollback")
}

func newLiveGatewayV2Fixture(t *testing.T, spec liveGatewayV2FixtureSpec) *liveGatewayV2Fixture {
	t.Helper()
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" {
		t.Skip("set RIG_RUN_LIVE_GATEWAY_V2=1 on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("live gateway-v2 upgrade requires Docker's default local Linux context")
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
	stateRoot := filepath.Join(root, "ingress-state")
	for _, directory := range []string{dockerConfig, working, stateRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal("create live gateway-v2 directory")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	t.Cleanup(cancel)
	runner := runtimeprocess.ExecRunner{}
	liveGatewayRequireLocalDocker(t, ctx, runner, docker, root, dockerConfig)
	selected, port := liveGatewayV2SelectHostBinding(t)

	appNetwork, err := generatedruntime.DescribeAppNetwork(spec.appID)
	if err != nil {
		t.Fatal("describe gateway-v2 application network")
	}
	image := liveGatewayImage{tag: spec.imageTag, appID: spec.appID}
	liveGatewayRequireCleanPreflight(t, ctx, runner, docker, root, dockerConfig, []string{appNetwork.Name}, []liveGatewayImage{image})
	liveGatewayV2RequireCleanPreflight(t, ctx, runner, docker, root, dockerConfig, spec.operationID)

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
		t.Fatal("create live gateway-v2 runtime")
	}
	ingress, err := New(runner, Options{
		DockerExecutable: docker, DockerConfigDirectory: dockerConfig, WorkingDirectory: working,
		DataRoot: stateRoot, HostPort: freeLoopbackPort(t), CommandTimeout: 45 * time.Second,
		PullTimeout: 5 * time.Minute, OutputLimit: liveDockerOutputLimit,
	})
	if err != nil {
		t.Fatal("create live gateway-v2 ingress")
	}
	fixture := &liveGatewayV2Fixture{
		ctx: ctx, runner: runner, docker: docker, root: root, dockerConfig: dockerConfig,
		stateRoot: stateRoot, ingress: ingress, spec: spec, interfaceIP: selected.IPv4, port: port,
	}
	// LIFO cleanup removes v2 first, then candidates/v1/application resources.
	t.Cleanup(func() {
		liveGatewayCleanup(t, engine, runner, docker, root, dockerConfig, fixture.candidates,
			map[string]string{appNetwork.Name: spec.appID}, []liveGatewayImage{image})
	})
	t.Cleanup(func() {
		liveGatewayV2Cleanup(t, fixture)
	})

	appSpec := liveCandidateSpec("blue", spec.appID, spec.planID)
	appSpec.ImageContentID = buildLiveGatewayImage(t, ctx, runner, docker, root, dockerConfig, image.tag, appSpec, spec.applicationReply, "0.0.0.0")
	candidate := startLiveCandidate(t, ctx, engine, appSpec)
	fixture.candidates = append(fixture.candidates, candidate)
	route := generatedruntime.RouteSwitchRequest{
		AppID: spec.appID, ToSlot: candidate.Slot, Endpoints: []generatedruntime.RouteEndpoint{liveEndpoint(candidate)},
	}
	if err := ingress.Switch(ctx, route); err != nil {
		failLiveIngress(t, "install gateway-v2 source route", err)
	}
	assertLiveOneShot(t, ctx, ingress.options.HostPort, spec.appID, "/", spec.applicationReply,
		"gateway-v2 source route did not serve through v1")
	engine.ReleaseAdmission(candidate)
	fixture.source, err = ingress.store.load()
	if err != nil || fixture.source.Pending != nil {
		t.Fatal("load gateway-v2 source state")
	}
	fixture.request = liveGatewayV2Request(t, spec, selected, port)
	return fixture
}

func liveGatewayV2Request(t *testing.T, spec liveGatewayV2FixtureSpec, selected hostnetwork.Candidate, port uint16) GatewayV2UpgradeRequest {
	t.Helper()
	profileSpec := appaccess.GatewayProfileSpec{
		SelectedIPv4: selected.IPv4, InterfaceID: selected.InterfaceID, PortStart: port, PortEnd: port,
	}
	specDigest, err := appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal("digest live gateway-v2 profile")
	}
	profile := gatewayProfileBinding{
		RevisionID: spec.profileRevision, RevisionNumber: 1, SpecDigest: specDigest,
		SelectedIPv4: selected.IPv4, InterfaceID: selected.InterfaceID, PortStart: port, PortEnd: port,
	}
	actionDigest, err := gatewayUpgradeActionDigest(profile, gatewayV2IdentityVersion)
	if err != nil {
		t.Fatal("digest live gateway-v2 approval")
	}
	return GatewayV2UpgradeRequest{
		OperationID: spec.operationID,
		Profile: GatewayV2ProfileBinding{
			RevisionID: profile.RevisionID, RevisionNumber: profile.RevisionNumber, SpecDigest: profile.SpecDigest,
			SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID, PortStart: profile.PortStart, PortEnd: profile.PortEnd,
		},
		ApprovedBy: spec.approvedBy, ApprovedActionDigest: actionDigest,
	}
}

func liveGatewayV2SelectHostBinding(t *testing.T) (hostnetwork.Candidate, uint16) {
	t.Helper()
	candidates, err := hostnetwork.CurrentCandidates()
	if err != nil {
		t.Fatal("enumerate private host interfaces")
	}
	for _, candidate := range candidates {
		if _, err := hostnetwork.Select(candidates, candidate.InterfaceID, candidate.IPv4); err != nil {
			continue
		}
		for port := appaccess.GatewayPortStart; port <= appaccess.GatewayPortEnd; port++ {
			listener, err := net.Listen("tcp4", net.JoinHostPort(candidate.IPv4, strconv.FormatUint(uint64(port), 10)))
			if err != nil {
				continue
			}
			loopback, loopbackErr := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10)))
			if loopbackErr != nil {
				_ = listener.Close()
				continue
			}
			if err := loopback.Close(); err != nil {
				_ = listener.Close()
				t.Fatal("release gateway-v2 loopback-port preflight")
			}
			if err := listener.Close(); err != nil {
				t.Fatal("release gateway-v2 host-port preflight")
			}
			return candidate, port
		}
	}
	t.Fatal("no uniquely selectable private host interface has an available gateway port")
	return hostnetwork.Candidate{}, 0
}

func liveGatewayV2Authorizer(t *testing.T, want GatewayV2UpgradeRequest) GatewayV2UpgradeAuthorizer {
	t.Helper()
	return func(ctx context.Context, got GatewayV2UpgradeRequest) error {
		if ctx == nil || ctx.Err() != nil || !reflect.DeepEqual(got, want) {
			t.Error("gateway-v2 mutation authorization did not retain the exact fixture request")
			return errors.New("gateway-v2 live-test authorization mismatch")
		}
		return nil
	}
}

func liveGatewayV2AssertSourceUnchanged(t *testing.T, fixture *liveGatewayV2Fixture) {
	t.Helper()
	got, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(got, fixture.source) {
		t.Fatal("gateway-v2 upgrade changed the immutable v1 source state")
	}
}

func liveGatewayV2AssertUnassigned404(t *testing.T, fixture *liveGatewayV2Fixture, address string) {
	t.Helper()
	for _, probe := range []struct{ host, path string }{
		{address, "/"},
		{"wrong.invalid", "/"},
		{fixture.spec.appID + ".rig.localhost", "/"},
	} {
		result := probeGatewayV2HostStatus(fixture.ctx, address, fixture.port, probe.host, probe.path)
		if !result.Connected || !result.Responded || result.Status != http.StatusNotFound {
			t.Fatalf("unassigned gateway-v2 listener host=%q path=%q status=%d connected=%t responded=%t",
				probe.host, probe.path, result.Status, result.Connected, result.Responded)
		}
	}
	loopback := probeGatewayV2HostStatus(fixture.ctx, "127.0.0.1", fixture.port, address, "/")
	if loopback.Connected || loopback.Responded {
		t.Fatal("gateway-v2 LAN listener was also published on loopback")
	}
}

func liveGatewayV2AssertCommittedIdentity(t *testing.T, fixture *liveGatewayV2Fixture, state gatewayV2RouteState, journal gatewayMigrationJournal) {
	t.Helper()
	container, containerRuntime, found, err := fixture.ingress.inspectNamedGatewayContainer(fixture.ctx, state.Identity.FinalContainer)
	if err != nil || !validGatewayV2Container(state, journal, container, containerRuntime, found,
		gatewayV2FinalContainerRole, "sha256:"+journal.Resources.ImageID) ||
		normalizeID(container.ID) != journal.Resources.FinalContainerID {
		t.Fatal("committed gateway-v2 container identity or exact host bindings were not proven")
	}
	base, err := gatewayV2HostChallenge(state)
	if err != nil {
		t.Fatal("derive committed gateway-v2 host challenge")
	}
	challenge := gatewayV2PortChallenge(base, fixture.port)
	result := probeGatewayV2HostStatus(fixture.ctx, fixture.interfaceIP, fixture.port, fixture.interfaceIP,
		gatewayV2ChallengePathPrefix+challenge)
	if !result.Connected || !result.Responded || result.Status != http.StatusNotFound ||
		result.Body != gatewayV2ChallengeBodyPrefix+challenge {
		t.Fatal("committed gateway-v2 listener did not return its state-bound Caddy challenge")
	}
	if !fixture.ingress.probeGatewayV2ContainerChallenge(fixture.ctx, container.ID, state.Network.ContainerIPv4,
		fixture.port, fixture.interfaceIP, challenge) {
		t.Fatal("committed gateway-v2 container did not prove the same state-bound challenge")
	}
}

func liveGatewayV2LoadDurableOperation(t *testing.T, fixture *liveGatewayV2Fixture) (gatewayV2RouteState, gatewayMigrationJournal, *gatewayUpgradeStateStore) {
	t.Helper()
	store, err := newGatewayUpgradeStateStore(fixture.stateRoot)
	if err != nil {
		t.Fatal("open durable gateway-v2 operation store")
	}
	state, journal, err := store.loadBoundUpgrade(fixture.spec.operationID)
	if err != nil {
		t.Fatal("load durable gateway-v2 operation")
	}
	return state, journal, store
}

func liveGatewayV2StartConflictContainer(t *testing.T, fixture *liveGatewayV2Fixture) string {
	t.Helper()
	binding := net.JoinHostPort(fixture.interfaceIP, strconv.FormatUint(uint64(fixture.port), 10)) + ":8080/tcp"
	result, err := runLiveDocker(fixture.ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, 45*time.Second,
		"container", "run", "--detach", "--name", liveGatewayV2ConflictContainer,
		"--label", gatewayV2ManagedLabelKey+"="+liveGatewayV2ConflictManaged,
		"--label", gatewayV2OperationLabelKey+"="+fixture.spec.operationID,
		"--publish", binding, "--entrypoint", caddyExecutable, caddyImage,
		"respond", "--listen", ":8080", "--status", strconv.Itoa(http.StatusConflict), "--body", liveGatewayV2ConflictBody)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("start gateway-v2 bind-conflict container")
	}
	id := strings.TrimSpace(string(result.Stdout))
	clearLiveResult(&result)
	if !validContainerID(id) {
		t.Fatal("bind-conflict container returned an invalid identity")
	}
	id = normalizeID(id)
	return id
}

func liveGatewayV2AssertConflictBinding(t *testing.T, fixture *liveGatewayV2Fixture, expectedID string) {
	t.Helper()
	container, containerRuntime, found, err := fixture.ingress.inspectNamedGatewayContainer(fixture.ctx, liveGatewayV2ConflictContainer)
	wantBinding := map[string][]map[string]string{
		"8080/tcp": {{"HostIp": fixture.interfaceIP, "HostPort": strconv.FormatUint(uint64(fixture.port), 10)}},
	}
	if err != nil || !found || !validContainerID(container.ID) || normalizeID(container.ID) != expectedID ||
		strings.TrimPrefix(container.Name, "/") != liveGatewayV2ConflictContainer || !container.Running || container.Restarting ||
		container.Labels[gatewayV2ManagedLabelKey] != liveGatewayV2ConflictManaged ||
		container.Labels[gatewayV2OperationLabelKey] != fixture.spec.operationID ||
		!reflect.DeepEqual(container.PortBindings, wantBinding) ||
		!gatewayV2EffectivePortBindingsMatchConfigured(containerRuntime.EffectivePortBindings, wantBinding) {
		t.Fatal("bind-conflict container identity or exact selected-address binding was not proven")
	}
}

func liveGatewayV2AwaitConflict(t *testing.T, fixture *liveGatewayV2Fixture, failure string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	path := gatewayV2ChallengePathPrefix + strings.Repeat("f", 64)
	for {
		result := probeGatewayV2HostStatus(fixture.ctx, fixture.interfaceIP, fixture.port, "wrong.invalid", path)
		if result.Connected && result.Responded && result.Status == http.StatusConflict && result.Body == liveGatewayV2ConflictBody {
			return
		}
		if fixture.ctx.Err() != nil || time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func liveGatewayV2RequireCleanPreflight(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig, operationID string) {
	t.Helper()
	for _, resource := range []struct{ kind, name string }{
		{"container", gatewayV2ContainerName},
		{"container", gatewayV2StageContainerBase + operationID},
		{"container", liveGatewayV2ConflictContainer},
		{"network", gatewayV2NetworkName},
		{"volume", gatewayV2ConfigVolumeName},
		{"volume", gatewayV2DataVolumeName},
	} {
		if liveGatewayResourceExists(t, ctx, runner, docker, directory, dockerConfig, resource.kind, resource.name) {
			t.Fatalf("disposable Docker daemon already contains %s %s", resource.kind, resource.name)
		}
	}
	for _, kind := range []string{"container", "network", "volume"} {
		if names := liveGatewayV2OwnedResourceNames(t, ctx, runner, docker, directory, dockerConfig, kind); len(names) != 0 {
			t.Fatalf("disposable Docker daemon already contains gateway-v2 %s resources", kind)
		}
	}
}

func liveGatewayV2OwnedResourceNames(t *testing.T, ctx context.Context, runner runtimeprocess.CommandRunner, docker, directory, dockerConfig, kind string) []string {
	t.Helper()
	var args []string
	switch kind {
	case "container":
		args = []string{"container", "ls", "--all", "--format", "{{.Names}}"}
	case "network":
		args = []string{"network", "ls", "--format", "{{.Name}}"}
	case "volume":
		args = []string{"volume", "ls", "--format", "{{.Name}}"}
	default:
		t.Fatal("unsupported gateway-v2 resource kind")
	}
	args = append(args, "--filter", "label="+gatewayV2ManagedLabelKey, "--filter", "label="+gatewayV2IdentityLabelKey+"="+gatewayV2IdentityVersion)
	result, err := runLiveDocker(ctx, runner, docker, directory, dockerConfig, 45*time.Second, args...)
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		clearLiveResult(&result)
		t.Fatal("list gateway-v2 owned resources")
	}
	body := strings.TrimSpace(string(result.Stdout))
	clearLiveResult(&result)
	if body == "" {
		return nil
	}
	return strings.Fields(body)
}

func liveGatewayV2AssertNoOwnedResources(t *testing.T, fixture *liveGatewayV2Fixture) {
	t.Helper()
	for _, kind := range []string{"container", "network", "volume"} {
		if names := liveGatewayV2OwnedResourceNames(t, fixture.ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, kind); len(names) != 0 {
			t.Fatalf("bind-conflict rollback retained gateway-v2 %s resources: %v", kind, names)
		}
	}
}

func liveGatewayV2Cleanup(t *testing.T, fixture *liveGatewayV2Fixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, storeErr := newGatewayUpgradeStateStore(fixture.stateRoot)
	var state gatewayV2RouteState
	var journal gatewayMigrationJournal
	if storeErr == nil {
		state, journal, storeErr = store.loadBoundUpgrade(fixture.spec.operationID)
	}
	cleanupAuthorized := storeErr == nil && journal.Phase == gatewayPhaseCommitted
	for _, resource := range []struct{ name, role string }{
		{gatewayV2StageContainerBase + fixture.spec.operationID, gatewayV2StageContainerRole},
		{gatewayV2ContainerName, gatewayV2FinalContainerRole},
	} {
		container, _, found, err := fixture.ingress.inspectNamedGatewayContainer(ctx, resource.name)
		if err != nil {
			t.Errorf("inspect gateway-v2 cleanup container %s", resource.name)
			continue
		}
		if !found {
			continue
		}
		expectedID := journal.Resources.StageContainerID
		if resource.role == gatewayV2FinalContainerRole {
			expectedID = journal.Resources.FinalContainerID
		}
		if !cleanupAuthorized || expectedID == "" || strings.TrimPrefix(container.Name, "/") != resource.name ||
			!validContainerID(container.ID) || normalizeID(container.ID) != expectedID ||
			!reflect.DeepEqual(container.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, resource.role, true)) {
			t.Errorf("gateway-v2 cleanup container %s has uncertain ownership; retaining it", resource.name)
			continue
		}
		result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, 45*time.Second,
			"container", "rm", "--force", normalizeID(container.ID))
		clearLiveResult(&result)
		if removeErr != nil {
			t.Errorf("remove exact gateway-v2 cleanup container %s", resource.name)
		}
	}

	network, networkID, found, err := fixture.ingress.inspectNamedGatewayNetwork(ctx, gatewayV2NetworkName)
	if err != nil {
		t.Error("inspect gateway-v2 cleanup network")
	} else if found {
		owned := cleanupAuthorized && network.Name == gatewayV2NetworkName && validContainerID(networkID) &&
			normalizeID(networkID) == journal.Resources.IngressNetworkID &&
			reflect.DeepEqual(network.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedNetworkLabel, gatewayV2IngressNetworkRole, false))
		clearCaddyNetworkInspection(&network)
		if !owned {
			t.Error("gateway-v2 cleanup network has uncertain ownership; retaining it")
		} else {
			result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, 45*time.Second,
				"network", "rm", normalizeID(networkID))
			clearLiveResult(&result)
			if removeErr != nil {
				t.Error("remove exact gateway-v2 cleanup network")
			}
		}
	} else {
		clearCaddyNetworkInspection(&network)
	}

	for _, resource := range []struct{ name, role string }{
		{gatewayV2ConfigVolumeName, gatewayV2ConfigVolumeRole},
		{gatewayV2DataVolumeName, gatewayV2DataVolumeRole},
	} {
		volume, identity, found, err := fixture.ingress.inspectNamedVolumeWithIdentity(ctx, resource.name)
		if err != nil {
			t.Errorf("inspect gateway-v2 cleanup volume %s", resource.name)
			continue
		}
		if !found {
			continue
		}
		bound := journal.Resources.ConfigVolume
		if resource.role == gatewayV2DataVolumeRole {
			bound = journal.Resources.DataVolume
		}
		observed, bindingErr := newGatewayV2VolumeResourceBinding(identity)
		if !cleanupAuthorized || bindingErr != nil || observed != bound || volume.Name != resource.name ||
			!reflect.DeepEqual(volume.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, resource.role, false)) {
			t.Errorf("gateway-v2 cleanup volume %s has uncertain ownership; retaining it", resource.name)
			continue
		}
		result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, 45*time.Second,
			"volume", "rm", resource.name)
		clearLiveResult(&result)
		if removeErr != nil {
			t.Errorf("remove exact gateway-v2 cleanup volume %s", resource.name)
		}
	}
}

func liveGatewayV2RemoveConflictContainer(t *testing.T, fixture *liveGatewayV2Fixture, expectedID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if expectedID == "" || !validContainerID(expectedID) {
		if liveGatewayResourceExists(t, ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
			"container", liveGatewayV2ConflictContainer) {
			t.Error("bind-conflict container has no immutable returned identity; retaining it")
		}
		return
	}
	const format = `{"id":{{json .ID}},"name":{{json .Name}},"labels":{{json .Config.Labels}}}`
	result, err := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, 45*time.Second,
		"container", "inspect", "--format", format, liveGatewayV2ConflictContainer)
	if err != nil {
		clearLiveResult(&result)
		return
	}
	var inspection struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	valid := !result.StdoutTruncated && !result.StderrTruncated && json.Unmarshal(result.Stdout, &inspection) == nil &&
		validContainerID(inspection.ID) && strings.TrimPrefix(inspection.Name, "/") == liveGatewayV2ConflictContainer &&
		inspection.Labels[gatewayV2ManagedLabelKey] == liveGatewayV2ConflictManaged &&
		inspection.Labels[gatewayV2OperationLabelKey] == fixture.spec.operationID &&
		normalizeID(inspection.ID) == normalizeID(expectedID)
	clearLiveResult(&result)
	if !valid {
		t.Error("bind-conflict cleanup ownership is uncertain; retaining container")
		return
	}
	result, err = runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig, 45*time.Second,
		"container", "rm", "--force", normalizeID(inspection.ID))
	clearLiveResult(&result)
	if err != nil {
		t.Error("remove exact bind-conflict container")
	}
}
