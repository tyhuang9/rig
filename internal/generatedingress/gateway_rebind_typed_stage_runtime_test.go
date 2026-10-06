package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindTypedStageRuntimeFixture struct {
	intent     gatewayRebindProtectedIntentV2
	checkpoint gatewayRebindPredecessorCheckpoint
	records    []gatewayRebindProgressRecord
	runtime    gatewayRebindTypedStageRuntime
	runner     *gatewayRebindTypedStageRuntimeRunner
	server     *http.Server
	listener   net.Listener
}

type gatewayRebindTypedStageRuntimeRunner struct {
	t                       *testing.T
	intent                  gatewayRebindProtectedIntentV2
	effect                  gatewayRebindTypedEffectProgress
	container               caddyInspection
	containerRuntime        gatewayContainerRuntime
	network                 caddyNetworkInspection
	networkID               string
	configVolume            gatewayVolumeIdentityInspection
	dataVolume              gatewayVolumeIdentityInspection
	stageBody               []byte
	activeBody              []byte
	autosave                []byte
	ownedContainers         []string
	ownedVolumes            []string
	ownedNetworks           []string
	requests                [][]string
	effects                 [][]string
	archiveReads            int
	starts                  int
	copies                  int
	lostStartAcknowledgment bool
	lostCopyAcknowledgment  bool
	afterArchive            func(*gatewayRebindTypedStageRuntimeRunner)
	archiveOverride         []byte
}

func TestGatewayRebindTypedStageRuntimeCommandsStartReplayAndCopyFinalConfig(t *testing.T) {
	fixture := newGatewayRebindTypedStageRuntimeFixture(t)
	defer fixture.close(t)
	guard := func(context.Context) error { return nil }
	start := *fixture.records[8].TypedEffect

	fixture.runner.lostStartAcknowledgment = true
	if endpoint, err := fixture.runtime.serveStage(context.Background(), fixture.intent, start, guard); err == nil || endpoint != "" {
		t.Fatalf("lost start acknowledgement returned endpoint=%q error=%v", endpoint, err)
	}
	if fixture.runner.starts != 1 || !fixture.runner.container.Running {
		t.Fatalf("lost start did not retain one exact running container: starts=%d running=%t",
			fixture.runner.starts, fixture.runner.container.Running)
	}
	endpoint, err := fixture.runtime.serveStage(context.Background(), fixture.intent, start, guard)
	if err != nil || endpoint != fixture.runner.endpointID() || fixture.runner.starts != 1 {
		t.Fatalf("idempotent start replay endpoint=%q starts=%d error=%v", endpoint, fixture.runner.starts, err)
	}

	copyEffect := *fixture.records[10].TypedEffect
	active, err := gatewayRebindTypedFinalConfigBytes(fixture.intent, fixture.checkpoint,
		copyEffect.FinalConfigIntent.RoutePlan)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(active)
	fixture.runner.activeBody = nil
	if inventory, inventoryErr := gatewayRebindExactFinalConfigVolumeArchive(fixture.runner.configArchive(),
		fixture.runner.stageBody, active); inventoryErr != nil || inventory != gatewayRebindFinalConfigInventoryStageOnly {
		t.Fatalf("test runner stage-only archive inventory=%d error=%v", inventory, inventoryErr)
	}
	fixture.runner.lostCopyAcknowledgment = true
	copyErr := fixture.runtime.copyFinalConfig(context.Background(), fixture.intent, fixture.checkpoint,
		copyEffect, guard)
	if copyErr == nil {
		t.Fatal("lost final-config copy acknowledgement was accepted")
	}
	if fixture.runner.copies != 1 || !sameCaddyConfig(fixture.runner.activeBody, active) {
		t.Fatalf("lost copy did not retain exact active config: copies=%d bytes=%d requests=%#v error=%v",
			fixture.runner.copies, len(fixture.runner.activeBody), fixture.runner.requests, copyErr)
	}
	if err := fixture.runtime.copyFinalConfig(context.Background(), fixture.intent, fixture.checkpoint,
		copyEffect, guard); err != nil || fixture.runner.copies != 1 {
		t.Fatalf("idempotent final-config replay copies=%d error=%v", fixture.runner.copies, err)
	}
}

func TestGatewayRebindTypedStageRuntimeRejectsAlteredRestartAndLateBoundaryDrift(t *testing.T) {
	t.Run("altered restart config", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		fixture.runner.archiveOverride = stageAutosaveTestArchive(t, []byte(`{"tampered":true}`), fixture.runner.autosave)
		if _, err := fixture.runtime.serveStage(context.Background(), fixture.intent,
			*fixture.records[8].TypedEffect, func(context.Context) error { return nil }); err == nil {
			t.Fatal("altered stage restart config was accepted")
		}
		if fixture.runner.starts != 0 || len(fixture.runner.effects) != 0 {
			t.Fatalf("altered restart config reached Docker effects: %#v", fixture.runner.effects)
		}
	})

	for _, test := range []struct {
		name  string
		drift func(*gatewayRebindTypedStageRuntimeRunner)
	}{
		{name: "bound volume", drift: func(r *gatewayRebindTypedStageRuntimeRunner) {
			r.configVolume.Mountpoint += "-replacement"
		}},
		{name: "bound network", drift: func(r *gatewayRebindTypedStageRuntimeRunner) {
			r.networkID = strings.Repeat("f", 64)
		}},
		{name: "owned resource census", drift: func(r *gatewayRebindTypedStageRuntimeRunner) {
			r.ownedContainers = append(r.ownedContainers, "rig-rebind-unexpected")
			sort.Strings(r.ownedContainers)
		}},
	} {
		t.Run("late "+test.name+" drift", func(t *testing.T) {
			fixture := newGatewayRebindTypedStageRuntimeFixture(t)
			defer fixture.close(t)
			fixture.runner.afterArchive = test.drift
			if _, err := fixture.runtime.serveStage(context.Background(), fixture.intent,
				*fixture.records[8].TypedEffect, func(context.Context) error { return nil }); err == nil {
				t.Fatal("late bound-resource drift was accepted")
			}
			if fixture.runner.starts != 0 || len(fixture.runner.effects) != 0 {
				t.Fatalf("late bound-resource drift reached Docker effects: %#v", fixture.runner.effects)
			}
		})
	}

	t.Run("late authority drift before start", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		stale := false
		fixture.runner.afterArchive = func(*gatewayRebindTypedStageRuntimeRunner) { stale = true }
		guard := func(context.Context) error {
			if stale {
				return errors.New("injected stale authority")
			}
			return nil
		}
		if _, err := fixture.runtime.serveStage(context.Background(), fixture.intent,
			*fixture.records[8].TypedEffect, guard); err == nil {
			t.Fatal("late authority drift was accepted")
		}
		if fixture.runner.starts != 0 || len(fixture.runner.effects) != 0 {
			t.Fatalf("late authority drift reached Docker effects: %#v", fixture.runner.effects)
		}
	})
}

func TestGatewayRebindTypedStageRuntimeRejectsAuthorityDriftAfterFinalArchive(t *testing.T) {
	fixture := newGatewayRebindTypedStageRuntimeFixture(t)
	defer fixture.close(t)
	fixture.runner.start()
	stale := false
	fixture.runner.afterArchive = func(r *gatewayRebindTypedStageRuntimeRunner) {
		if r.archiveReads == 2 {
			stale = true
		}
	}
	guard := func(context.Context) error {
		if stale {
			return errors.New("injected final-archive authority drift")
		}
		return nil
	}
	if endpoint, err := fixture.runtime.serveStage(context.Background(), fixture.intent,
		*fixture.records[8].TypedEffect, guard); err == nil || endpoint != "" {
		t.Fatalf("final-archive drift returned endpoint=%q error=%v", endpoint, err)
	}
	if fixture.runner.starts != 0 || len(fixture.runner.effects) != 0 {
		t.Fatalf("final-archive drift dispatched an effect: %#v", fixture.runner.effects)
	}
}

func newGatewayRebindTypedStageRuntimeFixture(t *testing.T) gatewayRebindTypedStageRuntimeFixture {
	t.Helper()
	listener, candidate := gatewayRebindTypedStageRuntimeListener(t)
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Connection", "close")
		writer.WriteHeader(http.StatusNotFound)
		if strings.HasPrefix(request.URL.Path, gatewayV2ChallengePathPrefix) {
			_, _ = writer.Write([]byte(gatewayV2ChallengeBodyPrefix + strings.TrimPrefix(request.URL.Path,
				gatewayV2ChallengePathPrefix)))
		}
	})}
	go func() { _ = server.Serve(listener) }()

	current := newGatewayCurrentStateFixture(t)
	checkpoint, base := gatewayRebindAttemptTypedFixture(t, current)
	profile := base.Claim.Spec.SuccessorProfile
	profile.SelectedIPv4, profile.InterfaceID, profile.PortStart, profile.PortEnd = candidate.IPv4, candidate.InterfaceID, port, port
	profileDigest, err := appaccess.GatewayProfileSpecDigest(appaccess.GatewayProfileSpec{
		SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID,
		PortStart: profile.PortStart, PortEnd: profile.PortEnd,
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("digest typed successor profile: %v", err)
	}
	profileBinding := gatewayProfileBinding{RevisionID: base.SuccessorProfile.RevisionID,
		RevisionNumber: base.SuccessorProfile.RevisionNumber, SpecDigest: profileDigest,
		SelectedIPv4: profile.SelectedIPv4, InterfaceID: profile.InterfaceID, PortStart: port, PortEnd: port}
	plan, err := selectGatewayV2NetworkPlan(context.Background(), profileBinding, gatewayV2NetworkPlanReads{
		candidates: func() ([]hostNetworkCandidate, error) { return []hostNetworkCandidate{candidate}, nil },
		host:       func() (gatewayV2HostNetworkSnapshot, error) { return gatewayV2HostNetworkSnapshot{}, nil },
		docker:     func(context.Context) ([]netip.Prefix, error) { return []netip.Prefix{}, nil },
	})
	if err != nil {
		_ = server.Close()
		t.Fatalf("select typed stage network plan: %v", err)
	}
	network := base.NetworkObservation
	network.Candidates = gatewayRebindCandidateProjection([]hostNetworkCandidate{candidate})
	network.HostRoutes, network.HostInterfaces = []string{}, []string{}
	network.DockerNetworkIDs, network.DockerPrefixes = []string{}, []string{}
	network.Plan = gatewayRebindSuccessorIntentNetwork(plan)
	intent := gatewayRebindAttemptTypedIntentForCheckpoint(t, checkpoint, append([]appaccess.GatewayRebindRosterEntryV2(nil),
		base.Roster...), uuid.NewString(), base.SuccessorProfile.RevisionNumber,
		uuid.NewString(), profile, network)
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		_ = server.Close()
		t.Fatalf("construct typed successor-intent progress: %v", err)
	}
	records, _, _ := gatewayRebindTypedCompleteProgressFixture(t, current, checkpoint, intent, first)
	effect := *records[8].TypedEffect
	stageBody, err := gatewayRebindTypedStageConfigBytes(intent)
	if err != nil {
		_ = server.Close()
		t.Fatalf("build typed stage config: %v", err)
	}
	autosave, err := gatewayRebindCanonicalAutosaveConfig(stageBody)
	if err != nil {
		_ = server.Close()
		clear(stageBody)
		t.Fatalf("build typed canonical autosave: %v", err)
	}
	runner := newGatewayRebindTypedStageRuntimeRunner(t, intent, effect, stageBody, autosave)
	reads := gatewayRebindTypedStageRuntimeReads(t, intent, effect.Network.ID)
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		_ = server.Close()
		t.Fatalf("prepare typed stage runner directories: %v", err)
	}
	workingIdentity, err := os.Lstat(directories.WorkingDirectory)
	if err != nil {
		_ = server.Close()
		t.Fatalf("stat typed stage runner directory: %v", err)
	}
	manager := &Manager{runner: runner, options: Options{DockerExecutable: "pinned-docker",
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: 2 * time.Second,
		OutputLimit: defaultOutputLimit}, workingDirectoryIdentity: workingIdentity}
	return gatewayRebindTypedStageRuntimeFixture{intent: intent, checkpoint: checkpoint, records: records,
		runtime: gatewayRebindTypedStageRuntime{manager: manager, reads: reads}, runner: runner,
		server: server, listener: listener}
}

func (f gatewayRebindTypedStageRuntimeFixture) close(t *testing.T) {
	t.Helper()
	clear(f.runner.stageBody)
	clear(f.runner.activeBody)
	clear(f.runner.autosave)
	if err := f.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Error(err)
	}
}

func gatewayRebindTypedStageRuntimeListener(t *testing.T) (net.Listener, hostNetworkCandidate) {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range interfaces {
		addresses, addressErr := current.Addrs()
		if addressErr != nil {
			continue
		}
		for _, value := range addresses {
			prefix, prefixErr := netip.ParsePrefix(value.String())
			if prefixErr != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
				continue
			}
			for port := uint16(8100); port <= 8119; port++ {
				listener, listenErr := net.Listen("tcp4", net.JoinHostPort(prefix.Addr().String(), strconv.Itoa(int(port))))
				if listenErr == nil {
					return listener, hostNetworkCandidate{InterfaceID: strconv.Itoa(current.Index) + "/" + current.Name,
						IPv4: prefix.Addr().String(), Prefix: prefix.Masked()}
				}
			}
		}
	}
	t.Fatal("no bindable private IPv4 interface")
	return nil, hostNetworkCandidate{}
}

func newGatewayRebindTypedStageRuntimeRunner(t *testing.T, intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, stageBody, autosave []byte,
) *gatewayRebindTypedStageRuntimeRunner {
	t.Helper()
	bridge, err := gatewayRebindTypedStageBridgeName(intent)
	if err != nil {
		t.Fatal(err)
	}
	bindings := make(map[string][]map[string]string)
	for port := intent.SuccessorProfile.PortStart; ; port++ {
		value := strconv.FormatUint(uint64(port), 10)
		bindings[value+"/tcp"] = []map[string]string{{"HostIp": intent.SuccessorProfile.SelectedIPv4,
			"HostPort": value}}
		if port == intent.SuccessorProfile.PortEnd {
			break
		}
	}
	container := caddyInspection{ID: effect.StageContainer.ID, Name: "/" + intent.Identity.StageContainer,
		Image: "sha256:" + effect.ImageID, Labels: gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole), Hostname: intent.Identity.StageHostname,
		User: "1000:1000", Env: []string{"PATH=/usr/local/bin", "XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"},
		Entrypoint: []string{caddyExecutable}, Cmd: []string{"run", "--config", "/config/" + intent.Identity.StageConfigFilename},
		ReadOnly: true, CapAdd: []string{caddyCapability}, CapDrop: []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"}, Mounts: []mountInspection{
			{Type: "volume", Name: intent.Identity.ConfigVolume, Destination: "/config", RW: true},
			{Type: "volume", Name: intent.Identity.DataVolume, Destination: "/data", RW: true}},
		Memory: 268435456, MemorySwap: 268435456, NanoCPUs: 1_000_000_000, PIDsLimit: 128,
		LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"},
		Restart: gatewayV2StageRestartPolicy, NetworkMode: intent.Identity.IngressNetwork,
		Ulimits: []ulimitInspection{{Name: "nofile", Hard: 1024, Soft: 1024}}, PortBindings: bindings}
	configured := gatewayV2ConfiguredNetwork{NetworkID: effect.Network.ID, GwPriority: caddyGatewayPriority,
		IPAMConfig: &gatewayV2ConfiguredIPAM{IPv4Address: intent.Network.ContainerIPv4}}
	return &gatewayRebindTypedStageRuntimeRunner{t: t, intent: intent, effect: effect, container: container,
		containerRuntime: gatewayContainerRuntime{EffectivePortBindings: map[string][]map[string]string{},
			ConfiguredNetworks: map[string]gatewayV2ConfiguredNetwork{intent.Identity.IngressNetwork: configured}},
		network: caddyNetworkInspection{Name: intent.Identity.IngressNetwork, Driver: "bridge", Scope: "local",
			Options: map[string]string{gatewayRebindBridgeNameOptionKey: bridge},
			IPAM:    caddyNetworkIPAM{Config: []networkIPAM{{Subnet: intent.Network.Subnet, Gateway: intent.Network.GatewayIPv4}}},
			Labels: gatewayRebindTypedStageResourceLabels(intent, gatewayV2ManagedNetworkLabel,
				gatewayV2IngressNetworkRole), Containers: map[string]caddyNetworkContainerInspection{}},
		networkID: effect.Network.ID,
		configVolume: gatewayVolumeIdentityInspection{Name: intent.Identity.ConfigVolume, Driver: "local", Scope: "local",
			Options: map[string]string{}, Labels: gatewayRebindTypedStageResourceLabels(intent,
				gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole), Mountpoint: effect.ConfigVolume.Mountpoint,
			CreatedAt: effect.ConfigVolume.CreatedAt},
		dataVolume: gatewayVolumeIdentityInspection{Name: intent.Identity.DataVolume, Driver: "local", Scope: "local",
			Options: map[string]string{}, Labels: gatewayRebindTypedStageResourceLabels(intent,
				gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole), Mountpoint: effect.DataVolume.Mountpoint,
			CreatedAt: effect.DataVolume.CreatedAt},
		stageBody: append([]byte(nil), stageBody...), autosave: append([]byte(nil), autosave...),
		ownedContainers: []string{intent.Identity.StageContainer},
		ownedVolumes:    []string{intent.Identity.ConfigVolume, intent.Identity.DataVolume},
		ownedNetworks:   []string{intent.Identity.IngressNetwork}}
}

func gatewayRebindTypedStageRuntimeReads(t *testing.T, intent gatewayRebindProtectedIntentV2,
	networkID string,
) gatewayRebindSuccessorPreflightReads {
	t.Helper()
	baseline := gatewayRebindTypedRetainedNetworkReads(t, intent)
	plan := netip.MustParsePrefix(intent.Network.Subnet)
	return gatewayRebindSuccessorPreflightReads{network: gatewayV2NetworkPlanReads{
		candidates: baseline.network.candidates,
		host:       baseline.network.host,
		docker: func(context.Context) ([]netip.Prefix, error) {
			return []netip.Prefix{plan}, nil
		},
	}, dockerIDs: func(context.Context) ([]string, error) { return []string{networkID}, nil }}
}

func (r *gatewayRebindTypedStageRuntimeRunner) endpointID() string { return strings.Repeat("e", 64) }

func (r *gatewayRebindTypedStageRuntimeRunner) start() {
	r.container.Running = true
	r.container.Networks = map[string]*networkAttachment{r.intent.Identity.IngressNetwork: {
		IPAddress: r.intent.Network.ContainerIPv4, GwPriority: caddyGatewayPriority}}
	configured := r.containerRuntime.ConfiguredNetworks[r.intent.Identity.IngressNetwork]
	configured.EndpointID, configured.IPAddress = r.endpointID(), r.intent.Network.ContainerIPv4
	r.containerRuntime.ConfiguredNetworks[r.intent.Identity.IngressNetwork] = configured
	r.containerRuntime.EffectivePortBindings = cloneGatewayCurrentPhysicalTestPortBindings(r.container.PortBindings)
	prefix := netip.MustParsePrefix(r.intent.Network.Subnet)
	r.network.Containers = map[string]caddyNetworkContainerInspection{r.effect.StageContainer.ID: {
		Name:        r.intent.Identity.StageContainer,
		IPv4Address: r.intent.Network.ContainerIPv4 + "/" + strconv.Itoa(prefix.Bits())}}
}

func (r *gatewayRebindTypedStageRuntimeRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := append([]string(nil), request.Args...)
	r.requests = append(r.requests, args)
	if len(args) >= 2 && args[1] == "inspect" {
		return r.inspect(args)
	}
	if len(args) >= 2 && args[1] == "ls" {
		return r.list(args)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] == "-" {
		r.archiveReads++
		archive := r.configArchive()
		if r.afterArchive != nil {
			r.afterArchive(r)
		}
		return runtimeprocess.CommandResult{Stdout: archive}, nil
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" {
		body, err := os.ReadFile(args[2])
		if err != nil || args[3] != r.effect.StageContainer.ID+":/config/"+r.intent.Identity.ActiveConfigFilename {
			return runtimeprocess.CommandResult{}, errors.New("unexpected typed final-config copy")
		}
		r.effects = append(r.effects, args)
		r.copies++
		r.activeBody = append([]byte(nil), body...)
		if r.lostCopyAcknowledgment {
			r.lostCopyAcknowledgment = false
			return runtimeprocess.CommandResult{}, errors.New("lost typed copy acknowledgement")
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" && args[2] == r.effect.StageContainer.ID {
		r.effects = append(r.effects, args)
		r.starts++
		r.start()
		if r.lostStartAcknowledgment {
			r.lostStartAcknowledgment = false
			return runtimeprocess.CommandResult{}, errors.New("lost typed start acknowledgement")
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "exec" && args[2] == r.effect.StageContainer.ID &&
		args[3] == "curl" {
		url := args[len(args)-1]
		if strings.Contains(url, "127.0.0.1:2019/config/") {
			return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.stageBody...)}, nil
		}
		index := strings.Index(url, gatewayV2ChallengePathPrefix)
		if index < 0 {
			return runtimeprocess.CommandResult{}, errors.New("unexpected typed container probe")
		}
		challenge := url[index+len(gatewayV2ChallengePathPrefix):]
		return runtimeprocess.CommandResult{Stdout: []byte(gatewayV2ChallengeBodyPrefix + challenge + "\n404")}, nil
	}
	return runtimeprocess.CommandResult{}, fmt.Errorf("unexpected typed stage command: %s", strings.Join(args, " "))
}

func (r *gatewayRebindTypedStageRuntimeRunner) inspect(args []string) (runtimeprocess.CommandResult, error) {
	name := args[len(args)-1]
	switch args[0] {
	case "image":
		return jsonResult(imageInspection{ID: "sha256:" + r.effect.ImageID, OS: "linux",
			RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest}}), nil
	case "network":
		if name != r.intent.Identity.IngressNetwork {
			return gatewayCurrentPhysicalNotFound("network")
		}
		return gatewayCurrentPhysicalNetworkResult(r.networkID, r.network)
	case "volume":
		if name == r.intent.Identity.ConfigVolume {
			return jsonResult(r.configVolume), nil
		}
		if name == r.intent.Identity.DataVolume {
			return jsonResult(r.dataVolume), nil
		}
		return gatewayCurrentPhysicalNotFound("volume")
	case "container":
		if name == r.intent.Identity.FinalContainer {
			return gatewayCurrentPhysicalNotFound("container")
		}
		if name != r.intent.Identity.StageContainer && normalizeID(name) != r.effect.StageContainer.ID {
			return gatewayCurrentPhysicalNotFound("container")
		}
		return jsonResult(gatewayContainerInspection{caddyInspection: r.container,
			gatewayContainerRuntime: r.containerRuntime}), nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage inspect")
}

func (r *gatewayRebindTypedStageRuntimeRunner) list(args []string) (runtimeprocess.CommandResult, error) {
	var names []string
	switch args[0] {
	case "container":
		names = append([]string(nil), r.ownedContainers...)
	case "volume":
		names = append([]string(nil), r.ownedVolumes...)
	case "network":
		names = append([]string(nil), r.ownedNetworks...)
	default:
		return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage ownership inventory")
	}
	sort.Strings(names)
	if len(names) == 0 {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(names, "\n") + "\n")}, nil
}

func (r *gatewayRebindTypedStageRuntimeRunner) configArchive() []byte {
	if len(r.archiveOverride) != 0 {
		return append([]byte(nil), r.archiveOverride...)
	}
	if len(r.activeBody) == 0 {
		return stageAutosaveTestArchive(r.t, r.stageBody, r.autosave)
	}
	return finalConfigAutosaveArchive(r.t, r.stageBody, r.activeBody, r.autosave,
		"caddy/", r.intent.Identity.StageConfigFilename, r.intent.Identity.ActiveConfigFilename,
		"caddy/autosave.json")
}
