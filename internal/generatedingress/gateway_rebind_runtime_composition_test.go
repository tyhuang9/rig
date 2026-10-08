package generatedingress

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// This backend starts with only the predecessor. Successor templates describe
// future inspect responses; presence, config bytes and running state change only
// when the concrete adapters dispatch Docker commands. The same backend survives
// SQL commit and fresh Manager construction.
type gatewayRebindCompositionRunner struct {
	*gatewayRebindTypedHandoverRuntimeRunner
	before runtimeprocess.CommandRunner
}

type gatewayRebindCompositionDriver struct {
	managerGatewayRebindCrossStoreDriver
	t              *testing.T
	runner         *gatewayRebindCompositionRunner
	beforeProgress func(gatewayRebindProgressRecord) error
	afterProgress  func(gatewayRebindProgressRecord) error
}

func (d *gatewayRebindCompositionDriver) reconcileSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	if d.runner.stage == nil {
		d.initialize(request.Attempt.Intent)
	}
	return d.managerGatewayRebindCrossStoreDriver.reconcileSuccessorLocked(ctx, request,
		func(ctx context.Context, record gatewayRebindProgressRecord) error {
			if d.beforeProgress != nil {
				if err := d.beforeProgress(record); err != nil {
					return err
				}
			}
			if err := appendProgress(ctx, record); err != nil {
				return err
			}
			if record.TypedEffect != nil {
				d.runner.effect = *record.TypedEffect
				d.runner.stage.effect = *record.TypedEffect
			}
			if d.afterProgress != nil {
				return d.afterProgress(record)
			}
			return nil
		})
}

func (d *gatewayRebindCompositionDriver) initialize(intent gatewayRebindProtectedIntentV2) {
	d.t.Helper()
	r := d.runner
	r.intent = intent
	r.finalID = gatewayRebindCompositionResourceID(d.t, intent.Identity.FinalContainer)
	network, err := gatewayRebindTypedStageNetworkBindingFor(intent, gatewayRebindCompositionResourceID(d.t, intent.Identity.IngressNetwork))
	if err != nil {
		d.t.Fatal(err)
	}
	config, err := gatewayRebindTypedStageConfigVolumeBindingFor(intent,
		"/var/lib/docker/volumes/"+intent.Identity.ConfigVolume+"/_data", "2026-09-23T00:00:00Z")
	if err != nil {
		d.t.Fatal(err)
	}
	data, err := gatewayRebindTypedStageDataVolumeBindingFor(intent,
		"/var/lib/docker/volumes/"+intent.Identity.DataVolume+"/_data", "2026-09-23T00:00:00Z")
	if err != nil {
		d.t.Fatal(err)
	}
	shape := gatewayRebindTypedEffectProgress{ImageID: normalizeID(r.predecessor.Image.ID),
		Network: &network, ConfigVolume: &config, DataVolume: &data}
	stageID, err := canonicalDigest(struct{ Name string }{intent.Identity.StageContainer})
	if err != nil {
		d.t.Fatal(err)
	}
	container, err := gatewayRebindTypedStageContainerBindingFor(intent, shape, stageID)
	if err != nil {
		d.t.Fatal(err)
	}
	shape.StageContainer = &container
	r.stage = newGatewayRebindTypedStageRuntimeRunner(d.t, intent, shape, nil, nil)
	r.stage.ownedContainers, r.stage.ownedVolumes, r.stage.ownedNetworks = nil, nil, nil
	r.effect = shape
	d.install(d.manager)
}

func gatewayRebindCompositionResourceID(t *testing.T, name string) string {
	t.Helper()
	id, err := canonicalDigest(struct{ Name string }{name})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (d *gatewayRebindCompositionDriver) install(m *Manager) {
	r := d.runner
	m.runner = r
	rebind, current := newGatewayRebindRuntimeDrivers(m)
	handover := rebind.handover.(gatewayRebindTypedHandoverRuntime)
	handover.stage.reads = gatewayRebindTypedHandoverRuntimeReads(d.t, r.intent, r.stage.networkID,
		func() bool { return r.networkPresent })
	handover.stage.reads.network.docker = func(context.Context) ([]netip.Prefix, error) {
		_, prefixes := r.networkInventory()
		return prefixes, nil
	}
	handover.stage.reads.dockerIDs = func(context.Context) ([]string, error) { ids, _ := r.networkInventory(); return ids, nil }
	handover.hostProbe, handover.containerProbe, handover.listenerAbsent = r.hostProbe, r.containerProbe, r.listenerAbsent
	handover.stage.hostProbe, handover.stage.containerProbe = r.hostProbe, r.containerProbe
	rebind.stage, rebind.handover = handover.stage, handover
	d.managerGatewayRebindCrossStoreDriver = rebind
	managed := current.(managedGatewayCurrentPhysicalDriver)
	physical := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	physical.hostProbe, physical.containerProbe = r.hostProbe, r.containerProbe
	managed.runtime = physical
	m.gatewayCurrentPhysicalDriver = managed
	m.gatewayRebindCrossStoreDriver = d
}

func newGatewayRebindCompositionFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindCommitInput, *gatewayRebindCompositionDriver,
) {
	t.Helper()
	f, input, template := newGatewayRebindCoordinatorFixtureWithPredecessor(t,
		newGatewayRebindPredecessorFixtureWithEndpointAndLANApprover(t, false, '3', uuid.NewString()))
	runner := &gatewayRebindCompositionRunner{gatewayRebindTypedHandoverRuntimeRunner: &gatewayRebindTypedHandoverRuntimeRunner{
		t: t, predecessorState: f.state, predecessor: gatewayRebindFixtureDockerObservation(t, f), finalID: strings.Repeat("9", 64)},
		before: f.manager.runner}
	f.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := template.template.intent.NetworkObservation
		value.OperationID, value.ClaimRequestDigest, value.ProfileSpecDigest = claim.Spec.OperationID,
			claim.RequestDigest, claim.ConfigureApproval.SpecDigest
		value.Candidates = append(value.Candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: f.state.Profile.InterfaceID, IPv4: f.state.Profile.SelectedIPv4, Prefix: "192.168.96.0/24"})
		sort.Slice(value.Candidates, func(i, j int) bool { return gatewayRebindCandidateLess(value.Candidates[i], value.Candidates[j]) })
		value.HostInterfaces = append(value.HostInterfaces, "192.168.96.0/24")
		sort.Strings(value.HostInterfaces)
		ids, prefixes := runner.networkInventory()
		value.DockerNetworkIDs = ids
		value.DockerPrefixes = nil
		for _, prefix := range prefixes {
			value.DockerPrefixes = append(value.DockerPrefixes, prefix.String())
		}
		sort.Strings(value.DockerPrefixes)
		return value, nil
	}
	f.manager.runner = runner
	driver := &gatewayRebindCompositionDriver{managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{manager: f.manager},
		t: t, runner: runner}
	t.Cleanup(func() {
		if runner.stage != nil {
			clear(runner.stage.stageBody)
			clear(runner.stage.activeBody)
			clear(runner.stage.autosave)
		}
		clearGatewayV2DockerObservation(&runner.predecessor)
	})
	return f, input, driver
}

func (r *gatewayRebindCompositionRunner) networkInventory() ([]string, []netip.Prefix) {
	ids, prefixes := map[string]bool{}, map[string]bool{}
	add := func(id string, network caddyNetworkInspection) {
		ids[normalizeID(id)] = true
		for _, ipam := range network.IPAM.Config {
			prefixes[ipam.Subnet] = true
		}
	}
	if r.predecessor.V1NetworkFound {
		add(r.predecessor.V1NetworkID, r.predecessor.V1Network)
	}
	if r.predecessor.IngressFound {
		add(r.predecessor.IngressNetworkID, r.predecessor.IngressNetwork)
	}
	for name, network := range r.predecessor.ApplicationNetworks {
		add(r.applicationNetworkID(name), network)
	}
	if r.networkPresent {
		add(r.stage.networkID, r.stage.network)
	}
	var names []string
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	var subnets []netip.Prefix
	for prefix := range prefixes {
		subnets = append(subnets, netip.MustParsePrefix(prefix))
	}
	sort.Slice(subnets, func(i, j int) bool { return subnets[i].String() < subnets[j].String() })
	return names, subnets
}

func (r *gatewayRebindCompositionRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if r.stage == nil {
		return r.before.Run(ctx, request)
	}
	args := request.Args
	if len(args) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("incomplete composed Docker command")
	}
	if args[0] == "container" && args[1] == "ls" {
		for _, filter := range args {
			if volume, found := strings.CutPrefix(filter, "volume="); found {
				var ids []string
				for _, serving := range r.servers() {
					for _, mount := range serving.container.Mounts {
						if mount.Type == "volume" && mount.Name == volume {
							ids = append(ids, normalizeID(serving.container.ID))
							break
						}
					}
				}
				sort.Strings(ids)
				r.requests = append(r.requests, append([]string(nil), args...))
				return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(ids, "\n"))}, nil
			}
		}
	}
	record := func() {
		r.requests = append(r.requests, append([]string(nil), args...))
		r.effects = append(r.effects, append([]string(nil), args...))
	}
	if args[1] == "create" {
		switch args[0] {
		case "network":
			if r.networkPresent || args[len(args)-1] != r.intent.Identity.IngressNetwork {
				return runtimeprocess.CommandResult{}, errors.New("unexpected composed network create")
			}
			record()
			r.stage.network.Driver = gatewayRebindCompositionArg(args, "--driver")
			r.stage.network.Options = gatewayRebindCompositionArgsMap(args, "--opt")
			r.stage.network.Labels = gatewayRebindCompositionArgsMap(args, "--label")
			r.stage.network.IPAM.Config = []networkIPAM{{Subnet: gatewayRebindCompositionArg(args, "--subnet"), Gateway: gatewayRebindCompositionArg(args, "--gateway")}}
			r.networkPresent = true
			return runtimeprocess.CommandResult{Stdout: []byte(r.stage.networkID + "\n")}, nil
		case "volume":
			name := args[len(args)-1]
			if name == r.intent.Identity.ConfigVolume && !r.configPresent {
				r.stage.configVolume.Driver = gatewayRebindCompositionArg(args, "--driver")
				r.stage.configVolume.Labels = gatewayRebindCompositionArgsMap(args, "--label")
				r.configPresent = true
			} else if name == r.intent.Identity.DataVolume && !r.dataPresent {
				r.stage.dataVolume.Driver = gatewayRebindCompositionArg(args, "--driver")
				r.stage.dataVolume.Labels = gatewayRebindCompositionArgsMap(args, "--label")
				r.dataPresent = true
			} else {
				return runtimeprocess.CommandResult{}, errors.New("unexpected composed volume create")
			}
			record()
			return runtimeprocess.CommandResult{Stdout: []byte(name + "\n")}, nil
		case "container":
			want, err := gatewayRebindTypedStageContainerCreateArgs(r.intent, r.effect)
			if err == nil && reflect.DeepEqual(want, args) {
				if r.stagePresent {
					return runtimeprocess.CommandResult{}, errors.New("duplicate composed stage create")
				}
				record()
				r.stagePresent = true
				return runtimeprocess.CommandResult{Stdout: []byte(r.stage.container.ID + "\n")}, nil
			}
			if r.effect.HandoverIntent == nil {
				return runtimeprocess.CommandResult{}, errors.New("final create without handover")
			}
			want, err = gatewayRebindTypedFinalContainerCreateArgs(r.intent, r.effect, r.effect.HandoverIntent.Plan.LocalHostPort, r.effect.HandoverIntent.Plan.ApplicationNetworks)
			if err != nil || !reflect.DeepEqual(want, args) {
				return runtimeprocess.CommandResult{}, errors.New("unexpected composed final create")
			}
		}
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" {
		if args[3] == "-" {
			if strings.HasPrefix(args[2], r.stage.container.ID+":/config/") || strings.HasPrefix(args[2], r.finalID+":/config/") {
				r.requests = append(r.requests, append([]string(nil), args...))
				return runtimeprocess.CommandResult{Stdout: r.configArchive(args[2])}, nil
			}
		} else if r.stagePresent {
			body, err := os.ReadFile(args[2])
			if err != nil {
				return runtimeprocess.CommandResult{}, err
			}
			switch args[3] {
			case r.stage.container.ID + ":/config/" + r.intent.Identity.StageConfigFilename:
				r.stage.stageBody = body
			case r.stage.container.ID + ":/config/" + r.intent.Identity.ActiveConfigFilename:
				r.stage.activeBody = body
			default:
				clear(body)
				return runtimeprocess.CommandResult{}, errors.New("unexpected composed config destination")
			}
			record()
			return runtimeprocess.CommandResult{}, nil
		}
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" && args[2] == r.stage.container.ID && r.stagePresent {
		autosave, err := gatewayRebindCanonicalAutosaveConfig(r.stage.stageBody)
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		record()
		r.stage.autosave = autosave
		r.stage.start()
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "exec" && args[2] == r.stage.container.ID && r.stagePresent &&
		strings.Contains(args[len(args)-1], "127.0.0.1:2019/config/") {
		return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.stage.stageBody...)}, nil
	}
	// The retained handover simulator needs the reserved stage ID even before
	// that binding has been persisted. Its presence flags still govern all reads.
	if r.effect.StageContainer == nil {
		return r.readBeforeStageBinding(args)
	}
	return r.gatewayRebindTypedHandoverRuntimeRunner.Run(ctx, request)
}

func gatewayRebindCompositionArg(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func gatewayRebindCompositionArgsMap(args []string, flag string) map[string]string {
	values := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			key, value, found := strings.Cut(args[i+1], "=")
			if found {
				values[key] = value
			}
		}
	}
	return values
}

func (r *gatewayRebindCompositionRunner) readBeforeStageBinding(args []string) (runtimeprocess.CommandResult, error) {
	if args[1] == "ls" {
		return r.list(args)
	}
	if args[1] != "inspect" {
		return runtimeprocess.CommandResult{}, fmt.Errorf("unexpected command before stage binding: %v", args)
	}
	if args[0] == "container" {
		name := args[len(args)-1]
		if name == r.intent.Identity.StageContainer && r.stagePresent {
			return jsonResult(gatewayContainerInspection{caddyInspection: r.stage.container, gatewayContainerRuntime: r.stage.containerRuntime}), nil
		}
		return gatewayCurrentPhysicalNotFound("container")
	}
	return r.inspect(args)
}

func (r *gatewayRebindCompositionRunner) configArchive(source string) []byte {
	if !strings.HasSuffix(source, "/config/.") {
		return gatewayRebindSingleConfigArchive(r.t, r.intent.Identity.ActiveConfigFilename, r.stage.activeBody)
	}
	headers := []tar.Header{{Name: ".", Typeflag: tar.TypeDir}}
	var body []byte
	for _, file := range []struct {
		name string
		body []byte
	}{
		{r.intent.Identity.StageConfigFilename, r.stage.stageBody},
		{r.intent.Identity.ActiveConfigFilename, r.stage.activeBody},
		{"caddy/autosave.json", r.stage.autosave},
	} {
		if len(file.body) != 0 {
			if file.name == "caddy/autosave.json" {
				headers = append(headers, tar.Header{Name: "caddy/", Typeflag: tar.TypeDir, Mode: 01777})
			}
			headers = append(headers, tar.Header{Name: file.name, Typeflag: tar.TypeReg, Size: int64(len(file.body)), Mode: 0600, Uid: 1000, Gid: 1000})
			body = append(body, file.body...)
		}
	}
	return gatewayRebindStageConfigCopyHeadersTar(r.t, headers, body)
}

func (r *gatewayRebindCompositionRunner) hostProbe(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
	return gatewayRebindCompositionHostProbe(ctx, r.servers(), address, port, host, path)
}

func gatewayRebindCompositionHostProbe(ctx context.Context, servers []gatewayRebindCompositionServer, address string, port uint16, host, path string) gatewayV2HostProbeResult {
	if ctx.Err() != nil {
		return gatewayV2HostProbeResult{}
	}
	for _, serving := range servers {
		if !serving.container.Running {
			continue
		}
		for internal, bindings := range serving.runtime.EffectivePortBindings {
			for _, binding := range bindings {
				if binding["HostIp"] == address && binding["HostPort"] == strconv.Itoa(int(port)) {
					return gatewayRebindCompositionConfigProbe(serving.body,
						net.JoinHostPort(serving.address, strings.TrimSuffix(internal, "/tcp")), host, path)
				}
			}
		}
	}
	return gatewayV2HostProbeResult{}
}

func (r *gatewayRebindCompositionRunner) containerProbe(ctx context.Context, id, address string, port uint16, host, challenge string) bool {
	return gatewayRebindCompositionContainerProbe(ctx, r.servers(), id, address, port, host, challenge)
}

func gatewayRebindCompositionContainerProbe(ctx context.Context, servers []gatewayRebindCompositionServer, id, address string, port uint16, host, challenge string) bool {
	if ctx.Err() != nil {
		return false
	}
	for _, serving := range servers {
		if normalizeID(id) == normalizeID(serving.container.ID) && serving.container.Running && address == serving.address {
			proof := gatewayRebindCompositionConfigProbe(serving.body, net.JoinHostPort(address, strconv.Itoa(int(port))), host, gatewayV2ChallengePathPrefix+challenge)
			return proof.Connected && proof.Responded && proof.Status == http.StatusNotFound && proof.Body == gatewayV2ChallengeBodyPrefix+challenge
		}
	}
	return false
}

type gatewayRebindCompositionServer struct {
	container caddyInspection
	runtime   gatewayContainerRuntime
	address   string
	body      []byte
}

func (r *gatewayRebindCompositionRunner) servers() []gatewayRebindCompositionServer {
	servers := []gatewayRebindCompositionServer{{r.predecessor.FinalContainer, r.predecessor.FinalRuntime, r.predecessorState.Network.ContainerIPv4, r.predecessor.FinalConfig}}
	if r.stagePresent {
		servers = append(servers, gatewayRebindCompositionServer{r.stage.container, r.stage.containerRuntime, r.intent.Network.ContainerIPv4, r.stage.stageBody})
	}
	if r.finalPresent {
		servers = append(servers, gatewayRebindCompositionServer{r.final, r.finalRuntime, r.intent.Network.ContainerIPv4, r.stage.activeBody})
	}
	return servers
}

// Evaluate only the static challenge and fallback routes used by these proofs.
// Reverse-proxy transport remains a simulated endpoint, as in the raw runner.
func gatewayRebindCompositionConfigProbe(body []byte, listen, host, path string) gatewayV2HostProbeResult {
	var config caddyConfig
	if json.Unmarshal(body, &config) != nil {
		return gatewayV2HostProbeResult{}
	}
	contains := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	for _, server := range config.Apps.HTTP.Servers {
		if !contains(server.Listen, listen) {
			continue
		}
		result := gatewayV2HostProbeResult{Connected: true, Responded: true, Status: http.StatusNotFound}
		for _, route := range server.Routes {
			matches := len(route.Match) == 0
			for _, match := range route.Match {
				matches = matches || ((len(match.Host) == 0 || contains(match.Host, host)) && (len(match.Path) == 0 || contains(match.Path, path)))
			}
			if !matches {
				continue
			}
			for _, handler := range route.Handle {
				if handler.Handler == "static_response" {
					result.Status, result.Body = handler.StatusCode, handler.Body
					return result
				}
				if handler.Handler == "reverse_proxy" {
					result.Status = http.StatusOK
					return result
				}
			}
		}
		return result
	}
	return gatewayV2HostProbeResult{}
}

func TestGatewayRebindConcreteCompositionCommitsAndReplaysSameDockerState(t *testing.T) {
	f, input, driver := newGatewayRebindCompositionFixture(t)
	ctx := context.Background()
	result, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver)
	if err != nil || result.FinalPhase != appaccess.GatewayRebindCommitted || !result.FenceReleased {
		history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		t.Fatalf("concrete composition result=%#v err=%v progress=%d historyErr=%v effects=%d", result, err, len(history.Progress), historyErr, len(driver.runner.effects))
	}
	r := driver.runner
	if r.stagePresent || !r.finalPresent || !r.final.Running || r.predecessor.FinalContainer.Running {
		t.Fatal("commit did not leave only the successor serving")
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 17 || len(history.TerminalsV2) != 1 {
		t.Fatalf("typed history: %v", err)
	}
	files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	effects := len(r.effects)
	for replay := 0; replay < 2; replay++ {
		fresh := freshGatewayRebindRecoveryManager(f.manager)
		driver.install(fresh)
		result, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
		if err != nil || !result.FenceReleased || !validSHA256(result.CurrentAttestationDigest) || len(r.effects) != effects || fresh.gatewayRebindAdmissionBlocked() {
			t.Fatalf("fresh replay %d effects=%d/%d err=%v", replay, len(r.effects), effects, err)
		}
		f.manager = fresh
	}
	t.Run("stopped terminal restores exact successor", func(t *testing.T) {
		r.stopFinal() // Simulate a host restart without replacing any resources.
		fresh := freshGatewayRebindRecoveryManager(f.manager)
		driver.install(fresh)
		handled, err := fresh.RestoreGatewayCurrentServingStartup(ctx, f.repository)
		if err != nil || !handled || !r.final.Running || r.predecessor.FinalContainer.Running || len(r.effects) != effects+1 ||
			!reflect.DeepEqual(r.effects[effects], []string{"container", "start", r.finalID}) {
			t.Fatalf("terminal restore handled=%t err=%v effects=%v", handled, err, r.effects[effects:])
		}
		effects = len(r.effects)
		recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
		if err != nil || !recovered.FenceReleased || !validSHA256(recovered.CurrentAttestationDigest) || len(r.effects) != effects {
			t.Fatalf("restored terminal attestation: %v", err)
		}
		f.manager = fresh
	})
	if t.Failed() {
		return
	}
	t.Run("corrupt terminal config withdraws exact owner", func(t *testing.T) {
		clear(r.stage.activeBody)
		r.stage.activeBody = []byte(`{"tampered":true}`)
		fresh := freshGatewayRebindRecoveryManager(f.manager)
		driver.install(fresh)
		_, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
		var diagnostic *Error
		if err == nil || !errors.As(err, &diagnostic) || diagnostic.candidateMayBeLive || !fresh.gatewayRebindAdmissionBlocked() ||
			r.final.Running || r.predecessor.FinalContainer.Running || !r.finalPresent || len(r.effects) != effects+1 ||
			!reflect.DeepEqual(r.effects[effects], []string{"container", "stop", "--time", "10", r.finalID}) {
			t.Fatalf("terminal corruption failed exact withdrawal: err=%v effects=%v", err, r.effects[effects:])
		}
		f.manager = fresh
	})
	after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, after) {
		t.Fatalf("startup changed SQL authority: %v", err)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
}
