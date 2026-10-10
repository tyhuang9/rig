package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// Each entry retains the actual objects changed by its concrete Docker
// commands. Selecting a new active entry never converts the prior current
// into a native-upgrade fixture or replaces its resource/config objects.
type gatewayRebindMultiRunner struct {
	entries []*gatewayRebindCompositionRunner
	legacy  *gatewayCurrentPhysicalExecutor
	effects [][]string
}

type gatewayRebindMultiDriver struct {
	*gatewayRebindCompositionDriver
	backend *gatewayRebindMultiRunner
}

func (d *gatewayRebindMultiDriver) reconcileSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	intent := request.Attempt.Intent
	if d.runner.stage != nil && d.runner.intent.Digest != intent.Digest {
		var selected *gatewayRebindCompositionRunner
		for _, entry := range d.backend.entries {
			if entry.intent.Digest == intent.Digest {
				selected = entry
			}
		}
		if selected == nil {
			native := d.backend.entries[0]
			selected = &gatewayRebindCompositionRunner{gatewayRebindTypedHandoverRuntimeRunner: &gatewayRebindTypedHandoverRuntimeRunner{
				t: d.t, predecessorState: native.predecessorState, predecessor: native.predecessor}, before: native.before}
			d.backend.entries = append(d.backend.entries, selected)
		}
		d.gatewayRebindCompositionDriver = &gatewayRebindCompositionDriver{t: d.t, runner: selected,
			managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{manager: d.manager},
			beforeProgress:                       d.beforeProgress, afterProgress: d.afterProgress}
	}
	if d.runner.stage == nil {
		d.initialize(intent)
	}
	// initialize installs its single-generation adapter. Rebind every observation
	// and command path to the shared backend before entering concrete reconcile.
	d.installMulti(d.manager)
	return d.gatewayRebindCompositionDriver.reconcileSuccessorLocked(ctx, request, appendProgress)
}

func (d *gatewayRebindMultiDriver) installMulti(m *Manager) {
	d.gatewayRebindCompositionDriver.install(m)
	r := d.backend
	m.runner = r
	handover := d.handover.(gatewayRebindTypedHandoverRuntime)
	handover.stage.reads.network.docker = func(context.Context) ([]netip.Prefix, error) {
		_, prefixes := r.networkInventory()
		return prefixes, nil
	}
	handover.stage.reads.dockerIDs = func(context.Context) ([]string, error) { ids, _ := r.networkInventory(); return ids, nil }
	handover.hostProbe, handover.containerProbe, handover.listenerAbsent = r.hostProbe, r.containerProbe, r.listenerAbsent
	handover.stage.hostProbe, handover.stage.containerProbe = r.hostProbe, r.containerProbe
	d.stage, d.handover = handover.stage, handover
	managed := m.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	physical := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	physical.hostProbe, physical.containerProbe = r.hostProbe, r.containerProbe
	managed.runtime = physical
	m.gatewayCurrentPhysicalDriver, m.gatewayRebindCrossStoreDriver = managed, d
}

func newGatewayRebindMultiFixture(t *testing.T) (gatewayRebindPredecessorFixture, gatewayRebindCommitInput, *gatewayRebindMultiDriver) {
	t.Helper()
	f, input, core := newGatewayRebindCompositionFixture(t)
	r := &gatewayRebindMultiRunner{entries: []*gatewayRebindCompositionRunner{core.runner}}
	d := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: core, backend: r}
	installGatewayRebindMultiNetworkObserver(t, f.manager, r)
	f.manager.runner, f.manager.gatewayRebindCrossStoreDriver = r, d
	t.Cleanup(func() {
		for _, entry := range r.entries[1:] {
			if entry.stage != nil {
				clear(entry.stage.stageBody)
				clear(entry.stage.activeBody)
				clear(entry.stage.autosave)
			}
		}
	})
	return f, input, d
}

func installGatewayRebindMultiNetworkObserver(t *testing.T, m *Manager, backend *gatewayRebindMultiRunner) {
	t.Helper()
	observe := m.gatewayRebindV2NetworkObserver
	m.gatewayRebindV2NetworkObserver = func(ctx context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		if ctx == nil {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid multi-generation host census")
		}
		if err := ctx.Err(); err != nil {
			return gatewayRebindSuccessorNetworkObservation{}, err
		}
		value, err := observe(ctx, claim)
		if err != nil {
			return value, err
		}
		// Both approved successor addresses exist before either attempt. The
		// same host census is retained for every generation and replay.
		nextCandidate := gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: "rebind-next-successor", IPv4: "192.168.98.8", Prefix: "192.168.98.0/24"}
		foundCandidate := false
		for _, candidate := range value.Candidates {
			if candidate == nextCandidate {
				foundCandidate = true
				break
			}
		}
		if !foundCandidate {
			value.Candidates = append(value.Candidates, nextCandidate)
		}
		foundPrefix := false
		for _, prefix := range value.HostInterfaces {
			if prefix == nextCandidate.Prefix {
				foundPrefix = true
				break
			}
		}
		if !foundPrefix {
			value.HostInterfaces = append(value.HostInterfaces, nextCandidate.Prefix)
		}
		sort.Slice(value.Candidates, func(i, j int) bool { return gatewayRebindCandidateLess(value.Candidates[i], value.Candidates[j]) })
		sort.Strings(value.HostInterfaces)
		// Retirement has no admitted claim to bind to a new network plan. Its
		// caller only needs the fixture's already-arranged host candidate census.
		// Keep every nonempty claim on the normal claim-bound planner below.
		if reflect.DeepEqual(claim, appaccess.GatewayRebindClaimV2{}) {
			return value, nil
		}
		var candidates []hostNetworkCandidate
		for _, candidate := range value.Candidates {
			candidates = append(candidates, hostNetworkCandidate{InterfaceID: candidate.InterfaceID,
				IPv4: candidate.IPv4, Prefix: netip.MustParsePrefix(candidate.Prefix)})
		}
		routes, routesOK := parseGatewayRebindPrefixes(value.HostRoutes)
		interfaces, interfacesOK := parseGatewayRebindPrefixes(value.HostInterfaces)
		if !routesOK || !interfacesOK {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("invalid multi-generation host census")
		}
		// Select against every retained ingress. Reusing the first plan after its
		// network exists would overlap that network and invalidate the next intent.
		observed, err := readGatewayRebindV2SuccessorNetwork(ctx, claim, gatewayRebindSuccessorPreflightReads{
			network: gatewayV2NetworkPlanReads{
				candidates: func() ([]hostNetworkCandidate, error) { return candidates, nil },
				host: func() (gatewayV2HostNetworkSnapshot, error) {
					return gatewayV2HostNetworkSnapshot{Routes: routes, Interfaces: interfaces}, nil
				},
				docker: func(context.Context) ([]netip.Prefix, error) {
					_, prefixes := backend.networkInventory()
					return prefixes, nil
				},
			},
			dockerIDs: func(context.Context) ([]string, error) { ids, _ := backend.networkInventory(); return ids, nil },
		})
		if err != nil {
			return gatewayRebindSuccessorNetworkObservation{}, err
		}
		return newGatewayRebindSuccessorNetworkObservation(observed)
	}
}

func (r *gatewayRebindMultiRunner) servers() []gatewayRebindCompositionServer {
	native := r.entries[0]
	servers := make([]gatewayRebindCompositionServer, 0, len(r.entries)*2+2)
	if native.predecessor.FinalContainerFound {
		servers = append(servers, gatewayRebindCompositionServer{native.predecessor.FinalContainer, native.predecessor.FinalRuntime,
			native.predecessorState.Network.ContainerIPv4, native.predecessor.FinalConfig})
	}
	if r.legacy != nil && r.legacy.containerPresent {
		servers = append(servers, gatewayRebindCompositionServer{r.legacy.container, r.legacy.containerRuntime,
			r.legacy.target.State.Network.ContainerIPv4, r.legacy.live})
	}
	for _, entry := range r.entries {
		if entry.stagePresent {
			servers = append(servers, gatewayRebindCompositionServer{entry.stage.container, entry.stage.containerRuntime, entry.intent.Network.ContainerIPv4, entry.stage.stageBody})
		}
		if entry.finalPresent {
			servers = append(servers, gatewayRebindCompositionServer{entry.final, entry.finalRuntime, entry.intent.Network.ContainerIPv4, entry.currentLiveBody()})
		}
	}
	return servers
}

func (r *gatewayRebindMultiRunner) hostProbe(ctx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
	return gatewayRebindCompositionHostProbe(ctx, r.servers(), address, port, host, path)
}

func (r *gatewayRebindMultiRunner) containerProbe(ctx context.Context, id, address string, port uint16, host, challenge string) bool {
	return gatewayRebindCompositionContainerProbe(ctx, r.servers(), id, address, port, host, challenge)
}

func (r *gatewayRebindMultiRunner) listenerAbsent(ctx context.Context, address string, port uint16) bool {
	if ctx == nil || ctx.Err() != nil || port == 0 {
		return false
	}
	for _, server := range r.servers() {
		for _, bindings := range server.runtime.EffectivePortBindings {
			for _, binding := range bindings {
				if binding["HostIp"] == address && binding["HostPort"] == strconv.Itoa(int(port)) {
					return false
				}
			}
		}
	}
	return true
}

func (r *gatewayRebindMultiRunner) networkInventory() ([]string, []netip.Prefix) {
	if r.legacy == nil {
		ids, prefixes := r.entries[0].networkInventory()
		idSet, prefixSet := map[string]bool{}, map[string]bool{}
		for _, id := range ids {
			idSet[id] = true
		}
		for _, prefix := range prefixes {
			prefixSet[prefix.String()] = true
		}
		for _, entry := range r.entries[1:] {
			if entry.networkPresent {
				idSet[entry.stage.networkID] = true
				for _, ipam := range entry.stage.network.IPAM.Config {
					prefixSet[ipam.Subnet] = true
				}
			}
		}
		ids, prefixes = nil, nil
		for id := range idSet {
			ids = append(ids, id)
		}
		for prefix := range prefixSet {
			prefixes = append(prefixes, netip.MustParsePrefix(prefix))
		}
		sort.Strings(ids)
		sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].String() < prefixes[j].String() })
		return ids, prefixes
	}

	// The retained legacy executor is the mutable current authority. Its
	// application networks supersede the native fixture's old endpoint census;
	// only the retained native gateway networks stay globally reserved.
	idSet, prefixSet := map[string]bool{}, map[string]bool{}
	add := func(id string, network caddyNetworkInspection) {
		if id != "" {
			idSet[normalizeID(id)] = true
		}
		for _, ipam := range network.IPAM.Config {
			prefixSet[ipam.Subnet] = true
		}
	}
	for name, id := range r.legacy.networkIDs {
		if network, found := r.legacy.networks[name]; found {
			add(id, network)
		}
	}
	add(r.legacy.target.Resources.IngressNetwork.ID, r.legacy.ingress)
	native := r.entries[0]
	if native.predecessor.V1NetworkFound {
		add(native.predecessor.V1NetworkID, native.predecessor.V1Network)
	}
	if native.predecessor.IngressFound {
		add(native.predecessor.IngressNetworkID, native.predecessor.IngressNetwork)
	}
	for _, entry := range r.entries {
		if entry.networkPresent {
			add(entry.stage.networkID, entry.stage.network)
		}
	}
	ids, prefixes := make([]string, 0, len(idSet)), make([]netip.Prefix, 0, len(prefixSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	for prefix := range prefixSet {
		prefixes = append(prefixes, netip.MustParsePrefix(prefix))
	}
	sort.Strings(ids)
	sort.Slice(prefixes, func(i, j int) bool { return prefixes[i].String() < prefixes[j].String() })
	return ids, prefixes
}

type gatewayRebindMultiResource struct {
	kind, name, id string
	labels         map[string]string
	entry          *gatewayRebindCompositionRunner
	legacy         *gatewayCurrentPhysicalExecutor
	native         bool
	present        bool
}

func (r *gatewayRebindMultiRunner) resources() []gatewayRebindMultiResource {
	n := r.entries[0]
	resources := []gatewayRebindMultiResource{
		{kind: "container", name: caddyContainerName, id: normalizeID(n.predecessor.V1Container.ID), labels: n.predecessor.V1Container.Labels, entry: n, native: true, present: n.predecessor.V1ContainerFound},
		{kind: "container", name: n.predecessorState.Identity.FinalContainer, id: normalizeID(n.predecessor.FinalContainer.ID), labels: n.predecessor.FinalContainer.Labels, entry: n, native: true, present: n.predecessor.FinalContainerFound},
		{kind: "container", name: n.predecessorState.Identity.StageContainer, id: normalizeID(n.predecessor.StageContainer.ID), labels: n.predecessor.StageContainer.Labels, entry: n, native: true, present: n.predecessor.StageContainerFound},
		{kind: "volume", name: caddyVolumeName, id: caddyVolumeName, labels: n.predecessor.V1Volume.Labels, entry: n, native: true, present: n.predecessor.V1VolumeFound},
		{kind: "volume", name: n.predecessorState.Identity.ConfigVolume, id: n.predecessorState.Identity.ConfigVolume, labels: n.predecessor.ConfigVolume.Labels, entry: n, native: true, present: n.predecessor.ConfigVolumeFound},
		{kind: "volume", name: n.predecessorState.Identity.DataVolume, id: n.predecessorState.Identity.DataVolume, labels: n.predecessor.DataVolume.Labels, entry: n, native: true, present: n.predecessor.DataVolumeFound},
		{kind: "network", name: caddyNetworkName, id: n.predecessor.V1NetworkID, labels: n.predecessor.V1Network.Labels, entry: n, native: true, present: n.predecessor.V1NetworkFound},
		{kind: "network", name: n.predecessorState.Identity.IngressNetwork, id: n.predecessor.IngressNetworkID, labels: n.predecessor.IngressNetwork.Labels, entry: n, native: true, present: n.predecessor.IngressFound},
	}
	if r.legacy != nil {
		legacy := r.legacy
		resources = append(resources,
			gatewayRebindMultiResource{kind: "container", name: legacy.target.Identity.Rebind.StageContainer,
				id: normalizeID(legacy.target.Resources.StageContainer.ID), labels: gatewayCurrentPhysicalLabels(legacy.target, gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole), legacy: legacy, present: false},
			gatewayRebindMultiResource{kind: "container", name: legacy.target.Identity.Rebind.FinalContainer,
				id: normalizeID(legacy.target.Resources.FinalContainer.ID), labels: legacy.container.Labels, legacy: legacy, present: legacy.containerPresent},
			gatewayRebindMultiResource{kind: "volume", name: legacy.target.Identity.Rebind.ConfigVolume,
				id: legacy.target.Resources.ConfigVolume.Name, labels: gatewayCurrentPhysicalLabels(legacy.target, gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole), legacy: legacy, present: true},
			gatewayRebindMultiResource{kind: "volume", name: legacy.target.Identity.Rebind.DataVolume,
				id: legacy.target.Resources.DataVolume.Name, labels: gatewayCurrentPhysicalLabels(legacy.target, gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole), legacy: legacy, present: true},
			gatewayRebindMultiResource{kind: "network", name: legacy.target.Identity.Rebind.IngressNetwork,
				id: normalizeID(legacy.target.Resources.IngressNetwork.ID), labels: legacy.ingress.Labels, legacy: legacy, present: true})
	}
	for _, entry := range r.entries {
		if entry.stage == nil {
			continue
		}
		resources = append(resources,
			gatewayRebindMultiResource{kind: "container", name: entry.intent.Identity.StageContainer, id: entry.stage.container.ID, labels: entry.stage.container.Labels, entry: entry, present: entry.stagePresent},
			gatewayRebindMultiResource{kind: "container", name: entry.intent.Identity.FinalContainer, id: entry.finalID, labels: entry.final.Labels, entry: entry, present: entry.finalPresent},
			gatewayRebindMultiResource{kind: "volume", name: entry.intent.Identity.ConfigVolume, id: entry.intent.Identity.ConfigVolume, labels: entry.stage.configVolume.Labels, entry: entry, present: entry.configPresent},
			gatewayRebindMultiResource{kind: "volume", name: entry.intent.Identity.DataVolume, id: entry.intent.Identity.DataVolume, labels: entry.stage.dataVolume.Labels, entry: entry, present: entry.dataPresent},
			gatewayRebindMultiResource{kind: "network", name: entry.intent.Identity.IngressNetwork, id: entry.stage.networkID, labels: entry.stage.network.Labels, entry: entry, present: entry.networkPresent})
	}
	return resources
}

func (r *gatewayRebindMultiRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	a := request.Args
	if len(a) < 2 {
		return runtimeprocess.CommandResult{}, errors.New("incomplete multi-generation command")
	}
	if a[1] == "ls" {
		return r.list(a)
	}
	native := r.entries[0]
	if a[0] == "network" && a[1] == "inspect" {
		if network, id, found := r.applicationNetwork(a[len(a)-1]); found {
			return gatewayCurrentPhysicalNetworkResult(id, network)
		}
	}
	name, err := gatewayRebindMultiCommandTarget(a)
	if err != nil {
		return runtimeprocess.CommandResult{}, err
	}
	if r.legacy != nil && a[0] == "container" && a[1] == "inspect" && r.legacyEndpoint(name) {
		return r.runLegacy(ctx, request)
	}
	if resource, found, err := r.resource(a[0], name); err != nil {
		return runtimeprocess.CommandResult{}, err
	} else if found {
		// Raw network inspection accepts the Docker name; identity is checked
		// independently by the returned immutable ID.
		if a[0] == "network" && a[1] == "inspect" {
			request.Args = append([]string(nil), a...)
			request.Args[len(a)-1] = resource.name
		}
		if resource.legacy != nil {
			return r.runLegacy(ctx, request)
		}
		if resource.native && r.legacy != nil {
			if a[1] != "inspect" {
				return runtimeprocess.CommandResult{}, fmt.Errorf("native multi-generation resource is inspect-only: %v", a)
			}
			if resource.name == resource.entry.predecessorState.Identity.StageContainer {
				if !resource.entry.predecessor.StageContainerFound {
					return gatewayCurrentPhysicalNotFound("container")
				}
				return jsonResult(gatewayContainerInspection{caddyInspection: resource.entry.predecessor.StageContainer,
					gatewayContainerRuntime: resource.entry.predecessor.StageRuntime}), nil
			}
			return resource.entry.gatewayRebindTypedHandoverRuntimeRunner.Run(ctx, request)
		}
		before := len(resource.entry.effects)
		result, err := resource.entry.Run(ctx, request)
		r.effects = append(r.effects, resource.entry.effects[before:]...)
		return result, err
	}
	if a[0] == "image" && a[1] == "inspect" {
		if r.legacy != nil {
			// The retained legacy content identity remains independent from the
			// current pinned choice used to initialize the typed successor.
			if name == caddyImage || gatewayRebindMultiImageID(name, native.predecessor.Image.ID) {
				return native.Run(ctx, request)
			}
			if gatewayRebindMultiImageID(name, r.legacy.target.Resources.ImageID) {
				return r.runLegacy(ctx, request)
			}
			return runtimeprocess.CommandResult{}, fmt.Errorf("unknown multi-generation image: %v", a)
		}
		return native.Run(ctx, request)
	}
	if r.legacy == nil && a[1] == "inspect" && a[0] == "container" {
		return native.Run(ctx, request) // Application endpoint, never a retained gateway ID.
	}
	return runtimeprocess.CommandResult{}, fmt.Errorf("unowned multi-generation command: %v", a)
}

// gatewayRebindMultiCommandTarget accepts only the target position Docker uses
// for each command family. In particular, root exec commands place the
// immutable container ID after the exact --user value; never infer it by
// scanning arbitrary arguments.
func gatewayRebindMultiCommandTarget(args []string) (string, error) {
	if len(args) < 2 {
		return "", errors.New("incomplete multi-generation command")
	}
	if args[0] != "container" {
		return args[len(args)-1], nil
	}
	switch args[1] {
	case "create":
		name := gatewayRebindCompositionArg(args, "--name")
		if name == "" {
			return "", errors.New("missing multi-generation container name")
		}
		return name, nil
	case "exec":
		if len(args) < 3 {
			return "", errors.New("incomplete multi-generation exec")
		}
		if args[2] != "--user" {
			if args[2] == "" {
				return "", errors.New("missing multi-generation exec target")
			}
			return args[2], nil
		}
		if len(args) < 5 || args[3] != "0:0" || args[4] == "" {
			return "", errors.New("malformed multi-generation root exec")
		}
		return args[4], nil
	case "cp":
		if len(args) != 4 {
			return "", errors.New("malformed multi-generation container copy")
		}
		location := args[3]
		if location == "-" {
			location = args[2]
		}
		name, _, found := strings.Cut(location, ":/")
		if !found || name == "" {
			return "", errors.New("missing multi-generation copy target")
		}
		return name, nil
	default:
		return args[len(args)-1], nil
	}
}

func (r *gatewayRebindMultiRunner) applicationNetwork(query string) (caddyNetworkInspection, string, bool) {
	if r.legacy == nil {
		native := r.entries[0]
		network, found := native.applicationNetwork(query)
		if !found {
			return caddyNetworkInspection{}, "", false
		}
		r.overlayGatewayMembers(query, &network)
		return network, native.applicationNetworkID(query), true
	}
	name := query
	if _, found := r.legacy.networks[name]; !found {
		name = r.legacy.networkName(query)
	}
	network, found := r.legacy.networks[name]
	if !found || r.legacy.networkIDs[name] == "" {
		return caddyNetworkInspection{}, "", false
	}
	network.Containers = cloneGatewayRebindMultiNetworkMembers(network.Containers)
	r.overlayGatewayMembers(name, &network)
	return network, normalizeID(r.legacy.networkIDs[name]), true
}

func cloneGatewayRebindMultiNetworkMembers(value map[string]caddyNetworkContainerInspection) map[string]caddyNetworkContainerInspection {
	result := make(map[string]caddyNetworkContainerInspection, len(value))
	for id, member := range value {
		result[id] = member
	}
	return result
}

func (r *gatewayRebindMultiRunner) overlayGatewayMembers(name string, network *caddyNetworkInspection) {
	if network == nil {
		return
	}
	ids := make(map[string]bool)
	for _, resource := range r.resources() {
		if resource.kind == "container" && resource.id != "" {
			ids[normalizeID(resource.id)] = true
		}
	}
	for id := range network.Containers {
		if ids[normalizeID(id)] {
			delete(network.Containers, id)
		}
	}
	for _, server := range r.servers() {
		attachment := server.container.Networks[name]
		if !server.container.Running || attachment == nil || attachment.IPAddress == "" {
			continue
		}
		network.Containers[normalizeID(server.container.ID)] = caddyNetworkContainerInspection{
			Name: strings.TrimPrefix(server.container.Name, "/"), IPv4Address: attachment.IPAddress + "/24"}
	}
}

func (r *gatewayRebindMultiRunner) legacyEndpoint(name string) bool {
	if r.legacy == nil {
		return false
	}
	_, found := r.legacy.endpoints[normalizeID(name)]
	return found
}

func (r *gatewayRebindMultiRunner) resource(kind, name string) (gatewayRebindMultiResource, bool, error) {
	var named, identified []gatewayRebindMultiResource
	for _, resource := range r.resources() {
		if resource.kind != kind {
			continue
		}
		if resource.id != "" && normalizeID(resource.id) == normalizeID(name) {
			identified = append(identified, resource)
		}
		if resource.name == name {
			named = append(named, resource)
		}
	}
	if len(identified) == 1 {
		return identified[0], true, nil
	}
	if len(identified) > 1 {
		return gatewayRebindMultiResource{}, false, fmt.Errorf("ambiguous immutable multi-generation resource %s", name)
	}
	if len(named) == 1 {
		return named[0], true, nil
	}
	if len(named) > 1 {
		return gatewayRebindMultiResource{}, false, fmt.Errorf("ambiguous multi-generation resource %s", name)
	}
	return gatewayRebindMultiResource{}, false, nil
}

func (r *gatewayRebindMultiRunner) runLegacy(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	before := len(r.legacy.effects)
	result, err := r.legacy.Run(ctx, request)
	r.effects = append(r.effects, r.legacy.effects[before:]...)
	return result, err
}

func gatewayRebindMultiImageID(value, want string) bool {
	return normalizeID(value) == normalizeID(want)
}

func (r *gatewayRebindMultiRunner) list(args []string) (runtimeprocess.CommandResult, error) {
	var values []string
	volume := ""
	var filters []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--filter" {
			if value, ok := strings.CutPrefix(args[i+1], "volume="); ok {
				volume = value
			} else if value, ok := strings.CutPrefix(args[i+1], "label="); ok {
				filters = append(filters, value)
			} else {
				return runtimeprocess.CommandResult{}, errors.New("unsupported inventory filter")
			}
		}
	}
	if volume != "" {
		for _, server := range r.servers() {
			for _, mount := range server.container.Mounts {
				if mount.Type == "volume" && mount.Name == volume {
					values = append(values, normalizeID(server.container.ID))
					break
				}
			}
		}
	} else {
		for _, resource := range r.resources() {
			if resource.kind != args[0] || !resource.present {
				continue
			}
			matches := true
			for _, filter := range filters {
				key, value, hasValue := strings.Cut(filter, "=")
				actual, exists := resource.labels[key]
				if !exists || (hasValue && actual != value) {
					matches = false
				}
			}
			if matches {
				values = append(values, resource.name)
			}
		}
	}
	sort.Strings(values)
	return runtimeprocess.CommandResult{Stdout: []byte(strings.Join(values, "\n"))}, nil
}
