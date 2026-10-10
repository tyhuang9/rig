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
	"net/url"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntime"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// This backend starts with only the predecessor. Successor templates describe
// future inspect responses; presence, config bytes and running state change only
// when the concrete adapters dispatch Docker commands. The same backend survives
// SQL commit and fresh Manager construction.
type gatewayRebindCompositionRunner struct {
	*gatewayRebindTypedHandoverRuntimeRunner
	before           runtimeprocess.CommandRunner
	currentCandidate []byte
	currentLive      []byte
	currentNext      []byte
	currentRestart   []byte
	currentAutosave  []byte
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
		clear(runner.currentCandidate)
		clear(runner.currentLive)
		clear(runner.currentNext)
		clear(runner.currentRestart)
		clear(runner.currentAutosave)
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
			if r.currentConfigInitialized() {
				if archive, found := r.currentConfigArchive(args[2]); found {
					r.requests = append(r.requests, append([]string(nil), args...))
					return runtimeprocess.CommandResult{Stdout: archive}, nil
				}
				if strings.HasPrefix(args[2], r.finalID+":/config/") {
					return runtimeprocess.CommandResult{}, errors.New("unexpected composed current config archive")
				}
			}
			if strings.HasPrefix(args[2], r.stage.container.ID+":/config/") || strings.HasPrefix(args[2], r.finalID+":/config/") {
				r.requests = append(r.requests, append([]string(nil), args...))
				return runtimeprocess.CommandResult{Stdout: r.configArchive(args[2])}, nil
			}
		} else if r.finalPresent && strings.HasPrefix(args[3], r.finalID+":/config/") {
			if args[3] != r.finalID+":/config/"+gatewayCurrentPhysicalConfigFilename {
				return runtimeprocess.CommandResult{}, errors.New("unexpected composed current config destination")
			}
			info, err := os.Stat(args[2])
			if err != nil {
				return runtimeprocess.CommandResult{}, err
			}
			if !info.Mode().IsRegular() {
				return runtimeprocess.CommandResult{}, errors.New("composed current config source is not a regular file")
			}
			// copyGatewayV2Config creates a root-owned 0644 source. Windows does not
			// preserve POSIX permission bits for ordinary test files, so retain the
			// portable regular-file check there and enforce the requested mode where
			// the filesystem represents it.
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
				return runtimeprocess.CommandResult{}, errors.New("unexpected composed current config source mode")
			}
			body, err := os.ReadFile(args[2])
			if err != nil {
				return runtimeprocess.CommandResult{}, err
			}
			clear(r.currentCandidate)
			r.currentCandidate = append([]byte(nil), body...)
			clear(body)
			record()
			return runtimeprocess.CommandResult{}, nil
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
	if r.currentConfigInitialized() {
		if result, handled, err := r.runCurrentConfigCommand(args); handled {
			return result, err
		}
	}
	if len(args) == 3 && args[0] == "container" && args[1] == "start" && args[2] == r.finalID && r.finalPresent &&
		len(r.currentRestart) != 0 {
		restart := r.currentRestartBody()
		if len(restart) == 0 {
			return runtimeprocess.CommandResult{}, errors.New("missing composed current restart config")
		}
		autosave, err := gatewayRebindCanonicalAutosaveConfig(restart)
		if err != nil {
			return runtimeprocess.CommandResult{}, err
		}
		clear(r.currentLive)
		r.currentLive = append([]byte(nil), restart...)
		clear(r.currentAutosave)
		r.currentAutosave = autosave
		r.startFinal()
		clear(r.stage.autosave)
		r.stage.autosave = append([]byte(nil), r.currentAutosave...)
		record()
		r.afterPhysicalEffect(args)
		return runtimeprocess.CommandResult{}, nil
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
		return gatewayRebindCompositionSingleFileArchive(r.t, r.intent.Identity.ActiveConfigFilename, r.stage.activeBody)
	}
	return gatewayRebindCompositionArchive(r.t, []gatewayRebindCompositionArchiveFile{
		{name: r.intent.Identity.StageConfigFilename, body: r.stage.stageBody, mode: 0o644},
		{name: r.intent.Identity.ActiveConfigFilename, body: r.stage.activeBody, mode: 0o644},
		{name: "caddy/autosave.json", body: r.stage.autosave, mode: 0o600, uid: 1000, gid: 1000},
	})
}

func (r *gatewayRebindCompositionRunner) runCurrentConfigCommand(args []string) (runtimeprocess.CommandResult, bool, error) {
	if len(args) < 3 || args[0] != "container" || args[1] != "exec" {
		return runtimeprocess.CommandResult{}, false, nil
	}
	target := ""
	if args[2] == "--user" {
		if len(args) < 5 || args[3] != "0:0" || args[4] != r.finalID {
			return runtimeprocess.CommandResult{}, true, errors.New("unexpected composed current root command")
		}
		target = args[4]
	} else {
		target = args[2]
	}
	if target != r.finalID {
		if args[2] != "--user" && len(args) >= 5 && args[3] == "caddy" &&
			(args[4] == "validate" || args[4] == "reload") {
			return runtimeprocess.CommandResult{}, true, errors.New("foreign composed current caddy command")
		}
		if gatewayRebindCompositionCurrentEndpointHeadURL(args) != "" {
			return runtimeprocess.CommandResult{}, true, errors.New("foreign composed current endpoint probe")
		}
		return runtimeprocess.CommandResult{}, false, nil
	}
	if !r.finalPresent || !r.final.Running {
		return runtimeprocess.CommandResult{}, true, errors.New("composed current container is not running")
	}
	if args[2] != "--user" {
		if gatewayRebindCompositionCurrentAdminRead(args, r.finalID) {
			return runtimeprocess.CommandResult{Stdout: append([]byte(nil), r.currentLiveBody()...)}, true, nil
		}
		if challenge, ok := r.currentChallengeRead(args); ok {
			return runtimeprocess.CommandResult{Stdout: []byte(gatewayV2ChallengeBodyPrefix + challenge + "\n404")}, true, nil
		}
		if endpointURL := gatewayRebindCompositionCurrentEndpointHeadURL(args); endpointURL != "" {
			if !r.currentEndpointHead(endpointURL) {
				return runtimeprocess.CommandResult{}, true, errors.New("unexpected composed current endpoint probe")
			}
			return runtimeprocess.CommandResult{Stdout: []byte("200")}, true, nil
		}
	}
	configPath := "/config/" + gatewayCurrentPhysicalConfigFilename
	activePath := "/config/" + r.intent.Identity.ActiveConfigFilename
	record := func() {
		r.requests = append(r.requests, append([]string(nil), args...))
		r.effects = append(r.effects, append([]string(nil), args...))
	}
	switch {
	case reflect.DeepEqual(args, []string{"container", "exec", r.finalID, "caddy", "validate", "--config", configPath}):
		if len(r.currentCandidate) == 0 {
			return runtimeprocess.CommandResult{}, true, errors.New("missing composed current candidate config")
		}
		canonical, err := gatewayRebindCanonicalAutosaveConfig(r.currentCandidate)
		if err != nil {
			return runtimeprocess.CommandResult{}, true, err
		}
		clear(canonical)
		record()
		return runtimeprocess.CommandResult{}, true, nil
	case reflect.DeepEqual(args, []string{"container", "exec", r.finalID, "caddy", "reload", "--config", configPath}):
		if len(r.currentCandidate) == 0 {
			return runtimeprocess.CommandResult{}, true, errors.New("missing composed current candidate config")
		}
		autosave, err := gatewayRebindCanonicalAutosaveConfig(r.currentCandidate)
		if err != nil {
			return runtimeprocess.CommandResult{}, true, err
		}
		clear(r.currentLive)
		r.currentLive = append([]byte(nil), r.currentCandidate...)
		clear(r.currentAutosave)
		r.currentAutosave = autosave
		record()
		return runtimeprocess.CommandResult{}, true, nil
	case reflect.DeepEqual(args, []string{"container", "exec", "--user", "0:0", r.finalID, "cp", configPath, "/config/active.next.json"}):
		if len(r.currentCandidate) == 0 {
			return runtimeprocess.CommandResult{}, true, errors.New("missing composed current candidate config")
		}
		clear(r.currentNext)
		r.currentNext = append([]byte(nil), r.currentCandidate...)
		record()
		return runtimeprocess.CommandResult{}, true, nil
	case reflect.DeepEqual(args, []string{"container", "exec", "--user", "0:0", r.finalID, "mv", "/config/active.next.json", activePath}):
		if len(r.currentNext) == 0 {
			return runtimeprocess.CommandResult{}, true, errors.New("missing composed current next config")
		}
		clear(r.currentRestart)
		r.currentRestart = append([]byte(nil), r.currentNext...)
		clear(r.currentNext)
		r.currentNext = nil
		record()
		return runtimeprocess.CommandResult{}, true, nil
	default:
		return runtimeprocess.CommandResult{}, true, errors.New("unexpected composed current config command")
	}
}

// gatewayRebindCompositionCurrentEndpointHeadURL accepts only the exact
// endpoint transport command issued by Manager.probeGatewayEndpoint. The
// target is intentionally left variable here so foreign targets can be denied
// before the underlying handover runner's permissive simulated exec path.
func gatewayRebindCompositionCurrentEndpointHeadURL(args []string) string {
	prefix := []string{"container", "exec"}
	suffix := []string{"curl", "--disable", "--silent", "--head", "--output", "/dev/null", "--write-out", "%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2"}
	if len(args) != len(prefix)+1+len(suffix)+1 || !reflect.DeepEqual(args[:len(prefix)], prefix) ||
		args[2] == "" || !reflect.DeepEqual(args[len(prefix)+1:len(prefix)+1+len(suffix)], suffix) {
		return ""
	}
	return args[len(args)-1]
}

// currentEndpointHead models one observable transport check without becoming a
// general curl interpreter. Its target must remain an exact, uniquely retained
// running endpoint reachable through an attached application network and an
// upstream that the current live Caddy configuration actually addresses.
func (r *gatewayRebindCompositionRunner) currentEndpointHead(endpointURL string) bool {
	var matched generatedruntime.RouteEndpoint
	urlMatches, aliasMatches := 0, 0
	for _, route := range gatewayV2RouteRecords(r.predecessorState) {
		for _, endpoint := range route.Endpoints {
			address := net.JoinHostPort(endpoint.NetworkAlias+"."+endpoint.NetworkName,
				strconv.FormatUint(uint64(endpoint.InternalPort), 10))
			if endpointURL != "http://"+address+"/" {
				continue
			}
			matched, urlMatches = endpoint, urlMatches+1
		}
	}
	if urlMatches != 1 || normalizeID(matched.ContainerID) == normalizeID(r.finalID) {
		return false
	}
	for _, route := range gatewayV2RouteRecords(r.predecessorState) {
		for _, endpoint := range route.Endpoints {
			if endpoint.NetworkAlias == matched.NetworkAlias && endpoint.NetworkName == matched.NetworkName {
				aliasMatches++
			}
		}
	}
	if aliasMatches != 1 {
		return false
	}
	if attachment := r.final.Networks[matched.NetworkName]; attachment == nil || attachment.IPAddress == "" {
		return false
	}
	network, found := r.applicationNetwork(matched.NetworkName)
	if !found {
		return false
	}
	member, found := network.Containers[matched.ContainerID]
	if !found {
		for id, value := range network.Containers {
			if normalizeID(id) == normalizeID(matched.ContainerID) {
				member, found = value, true
				break
			}
		}
	}
	if !found || member.Name != matched.Component {
		return false
	}
	endpoint, found := r.endpoint(matched.ContainerID)
	if !found || !endpoint.Running || endpoint.Health != "healthy" {
		return false
	}
	endpointAttachment := endpoint.Networks[matched.NetworkName]
	if endpointAttachment == nil || endpointAttachment.IPAddress == "" || !containsString(endpointAttachment.Aliases, matched.NetworkAlias) {
		return false
	}
	var config caddyConfig
	if json.Unmarshal(r.currentLiveBody(), &config) != nil {
		return false
	}
	dial := net.JoinHostPort(matched.NetworkAlias+"."+matched.NetworkName, strconv.FormatUint(uint64(matched.InternalPort), 10))
	for _, server := range config.Apps.HTTP.Servers {
		for _, route := range server.Routes {
			for _, handler := range route.Handle {
				if handler.Handler != "reverse_proxy" {
					continue
				}
				for _, upstream := range handler.Upstreams {
					if upstream.Dial == dial {
						return true
					}
				}
			}
		}
	}
	return false
}

func gatewayRebindCompositionCurrentAdminRead(args []string, finalID string) bool {
	return reflect.DeepEqual(args, []string{"container", "exec", finalID, "curl", "--disable", "--silent", "--show-error",
		"--fail", "--proto", "=http", "--noproxy", "*", "--max-time", "10", "http://127.0.0.1:2019/config/"})
}

func (r *gatewayRebindCompositionRunner) currentChallengeRead(args []string) (string, bool) {
	prefix := []string{"container", "exec", r.finalID, "curl", "--disable", "--silent", "--show-error", "--output", "-",
		"--write-out", "\n%{http_code}", "--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1",
		"--max-time", "2", "--header"}
	if len(args) != len(prefix)+2 || !reflect.DeepEqual(args[:len(prefix)], prefix) {
		return "", false
	}
	host, found := strings.CutPrefix(args[len(prefix)], "Host: ")
	hostAddress, hostErr := netip.ParseAddr(host)
	if !found || hostErr != nil || !hostAddress.Is4() || !hostAddress.IsPrivate() || hostAddress.String() != host {
		return "", false
	}
	parsed, err := url.Parse(args[len(args)-1])
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	port, portErr := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || !address.Is4() || portErr != nil || port == 0 || parsed.Path == gatewayV2ChallengePathPrefix {
		return "", false
	}
	challenge, found := strings.CutPrefix(parsed.Path, gatewayV2ChallengePathPrefix)
	if !found || !validSHA256(challenge) {
		return "", false
	}
	proof := gatewayRebindCompositionConfigProbe(r.currentLiveBody(), net.JoinHostPort(address.String(), strconv.FormatUint(port, 10)), host, parsed.Path)
	if !proof.Connected || !proof.Responded || proof.Status != http.StatusNotFound ||
		proof.Body != gatewayV2ChallengeBodyPrefix+challenge {
		return "", false
	}
	return challenge, true
}

func (r *gatewayRebindCompositionRunner) currentLiveBody() []byte {
	if len(r.currentLive) != 0 {
		return r.currentLive
	}
	return r.stage.activeBody
}

func (r *gatewayRebindCompositionRunner) currentConfigInitialized() bool {
	return len(r.currentCandidate) != 0 || len(r.currentLive) != 0 || len(r.currentNext) != 0 ||
		len(r.currentRestart) != 0 || len(r.currentAutosave) != 0
}

func (r *gatewayRebindCompositionRunner) currentRestartBody() []byte {
	if len(r.currentRestart) != 0 {
		return r.currentRestart
	}
	return r.stage.activeBody
}

func (r *gatewayRebindCompositionRunner) currentAutosaveBody() []byte {
	if len(r.currentAutosave) != 0 {
		return r.currentAutosave
	}
	return r.stage.autosave
}

func (r *gatewayRebindCompositionRunner) currentConfigArchive(source string) ([]byte, bool) {
	prefix := r.finalID + ":/config/"
	if !strings.HasPrefix(source, prefix) {
		return nil, false
	}
	name := strings.TrimPrefix(source, prefix)
	switch name {
	case r.intent.Identity.ActiveConfigFilename:
		return gatewayRebindCompositionSingleFileArchive(r.t, name, r.currentRestartBody()), true
	case gatewayCurrentPhysicalConfigFilename:
		if len(r.currentCandidate) == 0 {
			return nil, false
		}
		return gatewayRebindCompositionSingleFileArchive(r.t, name, r.currentCandidate), true
	case ".":
		files := []gatewayRebindCompositionArchiveFile{
			{name: r.intent.Identity.StageConfigFilename, body: r.stage.stageBody, mode: 0o644},
			{name: r.intent.Identity.ActiveConfigFilename, body: r.currentRestartBody(), mode: 0o644},
		}
		if len(r.currentCandidate) != 0 {
			files = append([]gatewayRebindCompositionArchiveFile{{
				name: gatewayCurrentPhysicalConfigFilename, body: r.currentCandidate, mode: 0o644,
			}}, files...)
		}
		if len(r.currentNext) != 0 {
			files = append(files, gatewayRebindCompositionArchiveFile{name: "active.next.json", body: r.currentNext, mode: 0o644})
		}
		if autosave := r.currentAutosaveBody(); len(autosave) != 0 {
			files = append(files, gatewayRebindCompositionArchiveFile{
				name: "caddy/autosave.json", body: autosave, mode: 0o600, uid: 1000, gid: 1000,
			})
		}
		return gatewayRebindCompositionArchive(r.t, files), true
	default:
		return nil, false
	}
}

type gatewayRebindCompositionArchiveFile struct {
	name     string
	body     []byte
	mode     int64
	uid, gid int
}

// gatewayRebindCompositionSingleFileArchive models Docker's direct
// container-cp-out form: the first and only entry is the requested regular
// file. Directory copies intentionally use gatewayRebindCompositionArchive.
func gatewayRebindCompositionSingleFileArchive(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	return gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{{
		Name: name, Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o644,
	}}, body)
}

func gatewayRebindCompositionArchive(t *testing.T, files []gatewayRebindCompositionArchiveFile) []byte {
	t.Helper()
	headers := []tar.Header{{Name: ".", Typeflag: tar.TypeDir, Mode: 0o755}}
	body := []byte(nil)
	for _, file := range files {
		if len(file.body) == 0 {
			continue
		}
		if file.name == "caddy/autosave.json" {
			headers = append(headers, tar.Header{Name: "caddy/", Typeflag: tar.TypeDir, Mode: 0o1777})
		}
		headers = append(headers, tar.Header{Name: file.name, Typeflag: tar.TypeReg, Size: int64(len(file.body)), Mode: file.mode, Uid: file.uid, Gid: file.gid})
		body = append(body, file.body...)
	}
	return gatewayRebindStageConfigCopyHeadersTar(t, headers, body)
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
		servers = append(servers, gatewayRebindCompositionServer{r.final, r.finalRuntime, r.intent.Network.ContainerIPv4, r.currentLiveBody()})
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
		prepared, err := fresh.InspectGatewayRebindCurrent(ctx, f.repository)
		if err != nil || prepared.CurrentRecoveryMode != GatewayCurrentRecoveryStable || !prepared.FenceReleased {
			t.Fatalf("stopped terminal startup classification: %+v error=%v", prepared, err)
		}
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
		confirmed, err := fresh.InspectGatewayRebindCurrent(ctx, f.repository)
		if err != nil || !reflect.DeepEqual(prepared, confirmed) {
			t.Fatalf("stopped terminal startup checkpoint changed: %v", err)
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
