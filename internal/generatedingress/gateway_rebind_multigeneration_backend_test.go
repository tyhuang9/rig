package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
	observe := f.manager.gatewayRebindV2NetworkObserver
	f.manager.gatewayRebindV2NetworkObserver = func(ctx context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		value, err := observe(ctx, claim)
		if err != nil {
			return value, err
		}
		// Both approved successor addresses exist before either attempt. The
		// same host census is retained for every generation and replay.
		value.Candidates = append(value.Candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: "rebind-next-successor", IPv4: "192.168.98.8", Prefix: "192.168.98.0/24"})
		value.HostInterfaces = append(value.HostInterfaces, "192.168.98.0/24")
		sort.Slice(value.Candidates, func(i, j int) bool { return gatewayRebindCandidateLess(value.Candidates[i], value.Candidates[j]) })
		sort.Strings(value.HostInterfaces)
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
					_, prefixes := r.networkInventory()
					return prefixes, nil
				},
			},
			dockerIDs: func(context.Context) ([]string, error) { ids, _ := r.networkInventory(); return ids, nil },
		})
		if err != nil {
			return gatewayRebindSuccessorNetworkObservation{}, err
		}
		return newGatewayRebindSuccessorNetworkObservation(observed)
	}
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

func (r *gatewayRebindMultiRunner) servers() []gatewayRebindCompositionServer {
	native := r.entries[0]
	servers := []gatewayRebindCompositionServer{{native.predecessor.FinalContainer, native.predecessor.FinalRuntime,
		native.predecessorState.Network.ContainerIPv4, native.predecessor.FinalConfig}}
	for _, entry := range r.entries {
		if entry.stagePresent {
			servers = append(servers, gatewayRebindCompositionServer{entry.stage.container, entry.stage.containerRuntime, entry.intent.Network.ContainerIPv4, entry.stage.stageBody})
		}
		if entry.finalPresent {
			servers = append(servers, gatewayRebindCompositionServer{entry.final, entry.finalRuntime, entry.intent.Network.ContainerIPv4, entry.stage.activeBody})
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

type gatewayRebindMultiResource struct {
	kind, name, id string
	labels         map[string]string
	entry          *gatewayRebindCompositionRunner
	present        bool
}

func (r *gatewayRebindMultiRunner) resources() []gatewayRebindMultiResource {
	n := r.entries[0]
	resources := []gatewayRebindMultiResource{
		{"container", caddyContainerName, normalizeID(n.predecessor.V1Container.ID), n.predecessor.V1Container.Labels, n, n.predecessor.V1ContainerFound},
		{"container", n.predecessorState.Identity.FinalContainer, normalizeID(n.predecessor.FinalContainer.ID), n.predecessor.FinalContainer.Labels, n, n.predecessor.FinalContainerFound},
		{"volume", caddyVolumeName, caddyVolumeName, n.predecessor.V1Volume.Labels, n, n.predecessor.V1VolumeFound},
		{"volume", n.predecessorState.Identity.ConfigVolume, n.predecessorState.Identity.ConfigVolume, n.predecessor.ConfigVolume.Labels, n, n.predecessor.ConfigVolumeFound},
		{"volume", n.predecessorState.Identity.DataVolume, n.predecessorState.Identity.DataVolume, n.predecessor.DataVolume.Labels, n, n.predecessor.DataVolumeFound},
		{"network", caddyNetworkName, n.predecessor.V1NetworkID, n.predecessor.V1Network.Labels, n, n.predecessor.V1NetworkFound},
		{"network", n.predecessorState.Identity.IngressNetwork, n.predecessor.IngressNetworkID, n.predecessor.IngressNetwork.Labels, n, n.predecessor.IngressFound},
	}
	for _, entry := range r.entries {
		if entry.stage == nil {
			continue
		}
		resources = append(resources,
			gatewayRebindMultiResource{"container", entry.intent.Identity.StageContainer, entry.stage.container.ID, entry.stage.container.Labels, entry, entry.stagePresent},
			gatewayRebindMultiResource{"container", entry.intent.Identity.FinalContainer, entry.finalID, entry.final.Labels, entry, entry.finalPresent},
			gatewayRebindMultiResource{"volume", entry.intent.Identity.ConfigVolume, entry.intent.Identity.ConfigVolume, entry.stage.configVolume.Labels, entry, entry.configPresent},
			gatewayRebindMultiResource{"volume", entry.intent.Identity.DataVolume, entry.intent.Identity.DataVolume, entry.stage.dataVolume.Labels, entry, entry.dataPresent},
			gatewayRebindMultiResource{"network", entry.intent.Identity.IngressNetwork, entry.stage.networkID, entry.stage.network.Labels, entry, entry.networkPresent})
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
		name := a[len(a)-1]
		if network, found := native.applicationNetwork(name); found {
			for _, entry := range r.entries {
				delete(network.Containers, entry.finalID)
			}
			for _, entry := range r.entries {
				if entry.finalPresent && entry.final.Running {
					if attachment := entry.final.Networks[name]; attachment != nil {
						network.Containers[entry.finalID] = caddyNetworkContainerInspection{Name: entry.intent.Identity.FinalContainer, IPv4Address: attachment.IPAddress + "/24"}
					}
				}
			}
			return gatewayCurrentPhysicalNetworkResult(native.applicationNetworkID(name), network)
		}
	}
	name := a[len(a)-1]
	if a[0] == "container" {
		switch a[1] {
		case "create":
			name = gatewayRebindCompositionArg(a, "--name")
		case "exec":
			name = a[2]
		case "cp":
			name = a[3]
			if name == "-" {
				name = a[2]
			}
			name = strings.SplitN(name, ":/", 2)[0]
		}
	}
	for _, resource := range r.resources() {
		if resource.kind != a[0] || (resource.name != name && resource.id != normalizeID(name)) {
			continue
		}
		entry := resource.entry
		before := len(entry.effects)
		// Raw network inspection accepts the Docker name; identity is checked
		// independently by the returned immutable ID.
		if a[0] == "network" && a[1] == "inspect" {
			request.Args = append([]string(nil), a...)
			request.Args[len(a)-1] = resource.name
		}
		result, err := entry.Run(ctx, request)
		r.effects = append(r.effects, entry.effects[before:]...)
		return result, err
	}
	if a[1] == "inspect" && (a[0] == "image" || a[0] == "container") {
		return native.Run(ctx, request) // Image or application endpoint, never a retained gateway ID.
	}
	return runtimeprocess.CommandResult{}, fmt.Errorf("unowned multi-generation command: %v", a)
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
