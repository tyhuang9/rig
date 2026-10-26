package generatedingress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedcapacity"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainRetainedV2A0Fixture struct {
	identity     generatedDomainIdentityFixture
	runner       *generatedDomainConstructionRunner
	environment  *candidateFactoryEnvironment
	manager      *Manager
	composition  *gatewayRebindCompositionRunner
	physical     candidateOwnerPhysical
	current      caddyInspection
	currentState gatewayCurrentRouteState
	state        gatewayV2RouteState
	journal      gatewayMigrationJournal
	observation  gatewayV2DockerObservation
	census       []generatedDomainDockerObject
	before       generatedDomainRetainedV1RebindProtectionSnapshot
	effects      int
}

// generatedDomainRetainedV2InspectionRunner supplies the exact read-only
// Docker projections used by the passive retained-V2 reader. Every response
// comes from the already authenticated observation, candidate physical state,
// or captured census; every other command retains the existing construction
// runner's fail-closed dispatch.
type generatedDomainRetainedV2InspectionRunner struct {
	delegate    *generatedDomainConstructionRunner
	objects     []generatedDomainDockerObject
	state       gatewayV2RouteState
	observation gatewayV2DockerObservation
	endpoints   map[string]endpointInspection
	lastFailure string
}

func (r *generatedDomainRetainedV2InspectionRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	r.lastFailure = ""
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{
		"container", "inspect", "--format", gatewayContainerInspectFormat}) {
		if result, found, handled := r.container(args[4]); handled {
			r.record(args)
			if !found {
				return runtimeprocess.CommandResult{Stderr: []byte("no such container")},
					errors.New("retained V2 container is absent")
			}
			return jsonResult(result), nil
		}
	}
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{
		"volume", "inspect", "--format", generatedDomainBootstrapVolumeIdentityFormat}) {
		var volume volumeInspection
		var identity gatewayV1VolumeIdentity
		switch args[4] {
		case caddyVolumeName:
			volume, identity = r.observation.V1Volume, r.observation.V1VolumeIdentity
		case r.state.Identity.ConfigVolume:
			volume, identity = r.observation.ConfigVolume, r.observation.ConfigVolumeIdentity
		case r.state.Identity.DataVolume:
			volume, identity = r.observation.DataVolume, r.observation.DataVolumeIdentity
		default:
			r.lastFailure = "volume_identity_unknown"
			return runtimeprocess.CommandResult{}, errors.New("unexpected retained V2 volume identity")
		}
		r.record(args)
		return jsonResult(generatedDomainGatewayVolumeIdentity(volume, identity)), nil
	}
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{"network", "inspect", "--format", "json"}) {
		if result, found, handled := r.network(args[4]); handled {
			r.record(args)
			if !found {
				return runtimeprocess.CommandResult{Stderr: []byte("no such network")},
					errors.New("retained V2 network is absent")
			}
			return jsonResult([]gatewayNetworkIdentityInspection{result}), nil
		}
	}
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{
		"container", "inspect", "--format", endpointInspectFormat}) {
		r.record(args)
		result, found := r.endpoints[normalizeID(args[4])]
		if !found || normalizeID(result.ID) != normalizeID(args[4]) {
			r.lastFailure = "endpoint_identity_unknown"
			return runtimeprocess.CommandResult{}, errors.New("unexpected retained V2 endpoint identity")
		}
		return jsonResult(result), nil
	}
	kind := ""
	switch {
	case reflect.DeepEqual(args, []string{"container", "ls", "--all",
		"--filter", "label=" + gatewayV2ManagedLabelKey,
		"--filter", "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion,
		"--format", "{{.Names}}"}):
		kind = "container"
	case reflect.DeepEqual(args, []string{"volume", "ls",
		"--filter", "label=" + gatewayV2ManagedLabelKey,
		"--filter", "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion,
		"--format", "{{.Name}}"}):
		kind = "volume"
	case reflect.DeepEqual(args, []string{"network", "ls",
		"--filter", "label=" + gatewayV2ManagedLabelKey,
		"--filter", "label=" + gatewayV2IdentityLabelKey + "=" + gatewayV2IdentityVersion,
		"--format", "{{.Name}}"}):
		kind = "network"
	default:
		result, err := r.delegate.Run(ctx, request)
		if err != nil {
			r.lastFailure = "delegate_" + generatedDomainColdRetainedV2CommandFamily(args)
		}
		return result, err
	}
	r.record(args)
	names := make([]string, 0, len(r.objects))
	for _, object := range r.objects {
		if object.kind != kind || object.labels[gatewayV2ManagedLabelKey] == "" ||
			object.labels[gatewayV2IdentityLabelKey] != gatewayV2IdentityVersion {
			continue
		}
		name := strings.TrimPrefix(object.name, "/")
		if name == "" {
			r.lastFailure = "owned_inventory_invalid"
			return runtimeprocess.CommandResult{}, errors.New("invalid retained V2 ownership inventory")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	body := strings.Join(names, "\n")
	if body != "" {
		body += "\n"
	}
	return runtimeprocess.CommandResult{Stdout: []byte(body)}, nil
}

func (r *generatedDomainRetainedV2InspectionRunner) record(args []string) {
	r.delegate.mu.Lock()
	r.delegate.commands = append(r.delegate.commands, append([]string(nil), args...))
	r.delegate.mu.Unlock()
}

func (r *generatedDomainRetainedV2InspectionRunner) container(name string) (
	gatewayContainerInspection, bool, bool,
) {
	switch name {
	case caddyContainerName:
		return gatewayContainerInspection{caddyInspection: r.observation.V1Container,
			gatewayContainerRuntime: r.observation.V1Runtime}, r.observation.V1ContainerFound, true
	case r.state.Identity.StageContainer:
		return gatewayContainerInspection{caddyInspection: r.observation.StageContainer,
			gatewayContainerRuntime: r.observation.StageRuntime}, r.observation.StageContainerFound, true
	case r.state.Identity.FinalContainer:
		return gatewayContainerInspection{caddyInspection: r.observation.FinalContainer,
			gatewayContainerRuntime: r.observation.FinalRuntime}, r.observation.FinalContainerFound, true
	default:
		return gatewayContainerInspection{}, false, false
	}
}

func (r *generatedDomainRetainedV2InspectionRunner) network(name string) (
	gatewayNetworkIdentityInspection, bool, bool,
) {
	if name == caddyNetworkName {
		return generatedDomainRetainedV2NetworkIdentity(r.observation.V1NetworkID,
			r.observation.V1Network), r.observation.V1NetworkFound, true
	}
	if name == r.state.Identity.IngressNetwork {
		return generatedDomainRetainedV2NetworkIdentity(r.observation.IngressNetworkID,
			r.observation.IngressNetwork), r.observation.IngressFound, true
	}
	if value, ok := r.observation.ApplicationNetworks[name]; ok {
		id, idOK := r.observation.ApplicationNetworkIDs[name]
		return generatedDomainRetainedV2NetworkIdentity(id, value), idOK, true
	}
	return gatewayNetworkIdentityInspection{}, false, false
}

func generatedDomainRetainedV2NetworkIdentity(id string,
	value caddyNetworkInspection,
) gatewayNetworkIdentityInspection {
	copy := cloneGeneratedDomainColdRetainedGatewayNetwork(value)
	return gatewayNetworkIdentityInspection{ID: id, Name: copy.Name, Driver: copy.Driver,
		Scope: copy.Scope, Internal: copy.Internal, Options: copy.Options, IPAM: copy.IPAM,
		Labels: copy.Labels, Containers: copy.Containers}
}

func generatedDomainRetainedV2EndpointInspections(physical []candidateOwnerPhysical,
	state gatewayV2RouteState, observation gatewayV2DockerObservation, current caddyInspection,
) (map[string]endpointInspection, bool) {
	physicalByID := make(map[string]candidateOwnerPhysical, len(physical))
	for _, value := range physical {
		id := normalizeID(value.containerID)
		if id == "" || !value.exists || !value.running || value.health != "healthy" {
			return nil, false
		}
		if _, duplicate := physicalByID[id]; duplicate {
			return nil, false
		}
		physicalByID[id] = value
	}
	routes := gatewayV2RouteRecords(state)
	result := make(map[string]endpointInspection, len(physicalByID))
	for appID, route := range routes {
		for _, endpoint := range route.Endpoints {
			id := normalizeID(endpoint.ContainerID)
			value, found := physicalByID[id]
			if !found || value.policy.identity.AppID != appID ||
				value.policy.identity.Component != endpoint.Component ||
				value.policy.identity.Role != endpoint.Role ||
				value.policy.identity.Slot != string(route.Slot) ||
				value.policy.identity.NetworkName != endpoint.NetworkName ||
				value.policy.identity.NetworkAlias != endpoint.NetworkAlias {
				return nil, false
			}
			network, found := observation.ApplicationNetworks[endpoint.NetworkName]
			if !found {
				return nil, false
			}
			var member caddyNetworkContainerInspection
			memberFound := false
			for memberID, candidate := range network.Containers {
				if normalizeID(memberID) == id {
					if memberFound {
						return nil, false
					}
					member, memberFound = candidate, true
				}
			}
			prefix, err := netip.ParsePrefix(member.IPv4Address)
			if !memberFound || err != nil || !prefix.Addr().Is4() {
				return nil, false
			}
			inspection, exists := result[id]
			if !exists {
				inspection = endpointInspection{ID: value.containerID,
					Labels: cloneGeneratedDomainColdRetainedGatewayStringMap(
						candidatePolicyContainerLabels(value.policy)),
					Running: value.running, Health: value.health,
					Networks: make(map[string]*networkAttachment)}
			}
			if _, duplicate := inspection.Networks[endpoint.NetworkName]; duplicate {
				return nil, false
			}
			inspection.Networks[endpoint.NetworkName] = &networkAttachment{
				Aliases: []string{endpoint.NetworkAlias}, IPAddress: prefix.Addr().String()}
			result[id] = inspection
		}
	}
	if len(result) != len(physicalByID) {
		return nil, false
	}
	currentID := normalizeID(current.ID)
	if currentID == "" || !current.Running {
		return nil, false
	}
	if _, duplicate := result[currentID]; duplicate {
		return nil, false
	}
	currentNetworks := make(map[string]*networkAttachment, len(current.Networks))
	for name, attachment := range current.Networks {
		if attachment == nil {
			return nil, false
		}
		copy := *attachment
		copy.Aliases = append([]string(nil), attachment.Aliases...)
		currentNetworks[name] = &copy
	}
	currentInspection := endpointInspection{ID: current.ID,
		Labels:  cloneGeneratedDomainColdRetainedGatewayStringMap(current.Labels),
		Running: current.Running, Networks: currentNetworks}
	currentSeen := false
	for name, network := range observation.ApplicationNetworks {
		for memberID, member := range network.Containers {
			inspection, found := result[normalizeID(memberID)]
			if !found && normalizeID(memberID) == currentID {
				inspection, found, currentSeen = currentInspection, true, true
			}
			attachment := inspection.Networks[name]
			prefix, err := netip.ParsePrefix(member.IPv4Address)
			if !found || attachment == nil || err != nil || !prefix.Addr().Is4() ||
				attachment.IPAddress != prefix.Addr().String() {
				return nil, false
			}
		}
	}
	if !currentSeen {
		return nil, false
	}
	result[currentID] = currentInspection
	return result, true
}

func TestGeneratedDomainColdFirstRecoveryAccountsAuthenticatedRetainedNativeV2Predecessor(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedDomainRetainedV2A0Fixture(t, nil)
		beforeCommands := len(fixture.runner.snapshot())
		first, err := generatedDomainRetainedV2AttestColdBoundary(t, fixture)
		commands := fixture.runner.snapshot()
		generatedDomainRetainedV2AssertProtectedUnchanged(t, fixture)
		if err != nil {
			generatedDomainRetainedV2AssertNoEffects(t, fixture, commands[beforeCommands:], false)
			t.Fatalf("authentic retained NativeV2 first recovery failed: %v", err)
		}
		generatedDomainRetainedV2RequireProofs(t, first)
		generatedDomainRetainedV2AssertNoEffects(t, fixture, commands[beforeCommands:], true)
		secondBefore := len(commands)
		second, err := generatedDomainRetainedV2AttestColdBoundary(t, fixture)
		secondCommands := fixture.runner.snapshot()
		if err != nil {
			generatedDomainRetainedV2AssertNoEffects(t, fixture, secondCommands[secondBefore:], false)
			t.Fatalf("fresh accepted retained NativeV2 cold-group attestation failed: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("accepted retained NativeV2 first recovery did not survive a fresh cold-group attestation: %v", err)
		}
		generatedDomainRetainedV2RequireProofs(t, second)
		generatedDomainRetainedV2AssertNoEffects(t, fixture, secondCommands[secondBefore:], true)
		if len(fixture.runner.snapshot()) <= beforeCommands {
			t.Fatal("retained NativeV2 cold-group attestation performed no authenticated reads")
		}
		generatedDomainRetainedV2AssertProtectedUnchanged(t, fixture)
	})
}

func TestGeneratedDomainColdFirstRecoveryRefusesInvalidRetainedNativeV2Predecessor(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]generatedDomainDockerObject, gatewayV2RouteState, gatewayMigrationJournal) []generatedDomainDockerObject
	}{
		{name: "crossed_predecessor_identity", mutate: func(objects []generatedDomainDockerObject,
			state gatewayV2RouteState, journal gatewayMigrationJournal,
		) []generatedDomainDockerObject {
			result := cloneGeneratedDomainRetainedV2Objects(objects)
			for index := range result {
				if result[index].kind == "container" && result[index].name == state.Identity.FinalContainer &&
					normalizeID(result[index].id) == journal.Resources.FinalContainerID {
					result[index].id = strings.Repeat("8", 64)
					return result
				}
			}
			return result
		}},
		{name: "unrelated_managed_container", mutate: func(objects []generatedDomainDockerObject,
			state gatewayV2RouteState, journal gatewayMigrationJournal,
		) []generatedDomainDockerObject {
			result := cloneGeneratedDomainRetainedV2Objects(objects)
			for _, object := range result {
				if object.kind == "container" && object.name == state.Identity.FinalContainer &&
					normalizeID(object.id) == journal.Resources.FinalContainerID {
					foreign := object
					foreign.id = strings.Repeat("7", 64)
					foreign.name += "-foreign"
					foreign.labels = cloneStringMap(object.labels)
					result = append(result, foreign)
					return result
				}
			}
			return result
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !generatedDomainRegistryIsolated(t) {
				return
			}
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainRetainedV2A0Fixture(t, test.mutate)
				beforeCommands := len(fixture.runner.snapshot())
				proofs, err := generatedDomainRetainedV2AttestColdBoundary(t, fixture)
				commands := fixture.runner.snapshot()
				generatedDomainRetainedV2AssertProtectedUnchanged(t, fixture)
				generatedDomainRetainedV2AssertNoEffects(t, fixture, commands[beforeCommands:], false)
				if !errors.Is(err, errGeneratedDomainConstructionRefused) ||
					proofs.retainedV1.presence != generatedDomainColdRetainedGatewayPresent ||
					proofs.retainedV1.authority == nil || !validSHA256(proofs.retainedV1.digest) ||
					!reflect.DeepEqual(proofs.retainedV2, generatedDomainColdRetainedV2Proof{}) {
					t.Fatalf("invalid retained NativeV2 predecessor error=%v", err)
				}
			})
		})
	}
}

type generatedDomainRetainedV2ColdProofs struct {
	retainedV1 generatedDomainColdRetainedGatewayProof
	retainedV2 generatedDomainColdRetainedV2Proof
}

// A complete retained proof reads the V1 restart once through the dedicated V1
// proof, then twice in each of two passive V2 observations. Each passive V2
// observation also reads the V2 final restart initially and for confirmation.
const (
	generatedDomainRetainedV2ExpectedV1RestartReads = 5
	generatedDomainRetainedV2ExpectedV2RestartReads = 4
)

func generatedDomainRetainedV2AttestColdBoundary(t *testing.T,
	fixture generatedDomainRetainedV2A0Fixture,
) (generatedDomainRetainedV2ColdProofs, error) {
	t.Helper()
	ctx := context.Background()
	observed, err := inspectGeneratedDomainStartupObservation(ctx, fixture.identity.stores,
		fixture.runner, fixture.identity.options)
	if err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	managerRunner := &generatedDomainRetainedV2InspectionRunner{
		delegate: fixture.runner, objects: cloneGeneratedDomainRetainedV2Objects(fixture.census),
		state: fixture.state, observation: fixture.observation}
	var endpointsValid bool
	managerRunner.endpoints, endpointsValid = generatedDomainRetainedV2EndpointInspections(
		[]candidateOwnerPhysical{fixture.physical}, fixture.state, fixture.observation, fixture.current)
	if !endpointsValid {
		return generatedDomainRetainedV2ColdProofs{}, errors.New("retained V2 endpoint identity fixture is invalid")
	}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{
		database: fixture.identity.database, stores: fixture.identity.stores,
		runner: managerRunner, options: fixture.identity.options,
	}, fixture.manager.store)
	if err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(ctx, fixture.identity.stores,
		fixture.runner, fixture.identity.options)
	if err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	budget, err := generatedcapacity.New(manager)
	if err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	authority, err := newGeneratedCandidateAuthorityConcrete(fixture.identity.stores, managerRunner,
		fixture.environment, budget, generatedDomainCandidateOptions(fixture.identity.options))
	if err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	groups, err := readGeneratedDomainColdGroups(ctx, authority, observed)
	if err != nil {
		return generatedDomainRetainedV2ColdProofs{}, err
	}
	var retained generatedDomainRetainedV2ColdProofs
	err = attestGeneratedDomainColdGroupsWithRetainedProofs(ctx, manager, authority, observed, groups,
		&retained.retainedV1, &retained.retainedV2)
	return retained, err
}

func generatedDomainRetainedV2RequireProofs(t *testing.T, proofs generatedDomainRetainedV2ColdProofs) {
	t.Helper()
	if proofs.retainedV1.presence != generatedDomainColdRetainedGatewayPresent ||
		proofs.retainedV1.authority == nil || !validSHA256(proofs.retainedV1.digest) ||
		proofs.retainedV2.presence != generatedDomainColdRetainedV2Present ||
		proofs.retainedV2.authority == nil || !validSHA256(proofs.retainedV2.digest) ||
		len(proofs.retainedV2.objects) != 4 {
		t.Fatalf("cold retained proofs are incomplete: v1=%d/%t/%t v2=%d/%t/%t/%d",
			proofs.retainedV1.presence, proofs.retainedV1.authority != nil, validSHA256(proofs.retainedV1.digest),
			proofs.retainedV2.presence, proofs.retainedV2.authority != nil, validSHA256(proofs.retainedV2.digest),
			len(proofs.retainedV2.objects))
	}
}

func newGeneratedDomainRetainedV2A0Fixture(t *testing.T,
	mutate func([]generatedDomainDockerObject, gatewayV2RouteState, gatewayMigrationJournal) []generatedDomainDockerObject,
) generatedDomainRetainedV2A0Fixture {
	t.Helper()
	predecessor, input, driver, physical, candidateOptions := newGeneratedDomainRetainedV2CompositionFixture(t)
	runner := driver.runner
	predecessor.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
		return errors.New("injected interruption after raw A0 SQL claim")
	}
	result, commitErr := predecessor.manager.commitGatewayRebindWithDriver(context.Background(),
		predecessor.repository, input, driver)
	if commitErr == nil || result != (GatewayRebindCommitResult{}) {
		t.Fatalf("raw A0 interruption result=%#v error=%v", result, commitErr)
	}
	before, err := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || before.Active == nil || before.Active.Claim.V2 == nil ||
		before.Phase != appaccess.GatewayRebindPrepared || before.DatabaseCommitObserved || !before.RollbackAllowed {
		t.Fatalf("raw A0 SQL boundary=%#v error=%v", before, err)
	}
	reconstructGatewayRebindCompositionA0(t, predecessor, runner, before)
	fresh, _ := newGatewayRebindCompositionRecoveryProcessWithDriver(t, predecessor.manager, runner)
	recovered, err := fresh.RecoverGatewayRebindStartup(context.Background(), predecessor.repository)
	if err != nil || !recovered.Recovered || recovered.FinalActivePhase != appaccess.GatewayRebindCommitted ||
		recovered.Disposition != appaccess.GatewayRebindDispositionCommit || !recovered.FenceReleased ||
		recovered.SelectedCurrentAuthority == nil {
		t.Fatalf("raw A0 forward recovery=%#v error=%v", recovered, err)
	}
	database, err := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || database.Active != nil || database.CurrentSource == nil || len(database.History) != 1 {
		t.Fatalf("raw A0 committed database=%#v error=%v", database, err)
	}
	history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatalf("raw A0 retained NativeV2 protected history is unavailable: %v", err)
	}
	first, firstErr := fresh.inspectGatewayV2Docker(context.Background(), history.Source,
		history.Predecessor.State, history.Predecessor.Journal)
	second, secondErr := fresh.inspectGatewayV2Docker(context.Background(), history.Source,
		history.Predecessor.State, history.Predecessor.Journal)
	terminal := len(history.TerminalsV2) == 1
	committed := history.Predecessor.Journal.Phase == gatewayPhaseCommitted
	topology := validGatewayTopologyInputs(history.Source, history.Predecessor.State,
		history.Predecessor.Journal)
	firstStable := first.V2ResourcesStable && first.StageStable && first.FinalStable &&
		first.OwnedInventoriesStable
	secondStable := second.V2ResourcesStable && second.StageStable && second.FinalStable &&
		second.OwnedInventoriesStable
	firstImage := validGatewayPinnedImage(first.Image, first.ImageFound)
	secondImage := validGatewayPinnedImage(second.Image, second.ImageFound)
	firstBound := gatewayV2ObservedResourcesMatchJournal(history.Predecessor.Journal, first)
	secondBound := gatewayV2ObservedResourcesMatchJournal(history.Predecessor.Journal, second)
	firstStopped := validGatewayV2StoppedFinal(history.Predecessor.State,
		history.Predecessor.Journal, first, true)
	secondStopped := validGatewayV2StoppedFinal(history.Predecessor.State,
		history.Predecessor.Journal, second, true)
	stablePair := firstErr == nil && secondErr == nil &&
		sameGatewayRebindPredecessorDockerObservation(first, second,
			history.Predecessor.State.Identity)
	physicalReady := runner.finalPresent && runner.final.Running && !runner.stagePresent &&
		!runner.predecessor.FinalContainer.Running
	if firstErr != nil || secondErr != nil || !terminal || !committed || !topology ||
		!firstStable || !secondStable || !firstImage || !secondImage || !firstBound ||
		!secondBound || !firstStopped || !secondStopped || !stablePair || !physicalReady {
		t.Fatalf("raw A0 retained NativeV2 physical envelope is incomplete: reads=%t/%t terminal=%t committed=%t topology=%t stable=%t/%t image=%t/%t bound=%t/%t stopped=%t/%t pair=%t physical=%t",
			firstErr == nil, secondErr == nil, terminal, committed, topology, firstStable,
			secondStable, firstImage, secondImage, firstBound, secondBound, firstStopped,
			secondStopped, stablePair, physicalReady)
	}
	rawFirstDigest, rawFirstDigestErr := canonicalDigest(first)
	rawSecondDigest, rawSecondDigestErr := canonicalDigest(second)
	transportFirst, transportFirstValid := generatedDomainRetainedV2CanonicalTransport(first)
	transportSecond, transportSecondValid := generatedDomainRetainedV2CanonicalTransport(second)
	confirmedFirstDigest, confirmedFirstDigestErr := canonicalDigest(first)
	confirmedSecondDigest, confirmedSecondDigestErr := canonicalDigest(second)
	transportValid := gatewayV2ObservedResourcesMatchJournal(history.Predecessor.Journal, transportFirst) &&
		gatewayV2ObservedResourcesMatchJournal(history.Predecessor.Journal, transportSecond) &&
		validGatewayV2StoppedFinal(history.Predecessor.State, history.Predecessor.Journal, transportFirst, true) &&
		validGatewayV2StoppedFinal(history.Predecessor.State, history.Predecessor.Journal, transportSecond, true) &&
		validStoppedGatewayV1Base(history.Source, history.Predecessor.Journal, transportFirst, false) &&
		validStoppedGatewayV1Base(history.Source, history.Predecessor.Journal, transportSecond, false) &&
		sameGatewayRebindPredecessorDockerObservation(transportFirst, transportSecond,
			history.Predecessor.State.Identity)
	rawUnchanged := sameGatewayRebindPredecessorDockerObservation(first, second,
		history.Predecessor.State.Identity) &&
		rawFirstDigestErr == nil && rawSecondDigestErr == nil && confirmedFirstDigestErr == nil &&
		confirmedSecondDigestErr == nil && rawFirstDigest == confirmedFirstDigest &&
		rawSecondDigest == confirmedSecondDigest &&
		gatewayV2ObservedResourcesMatchJournal(history.Predecessor.Journal, first) &&
		gatewayV2ObservedResourcesMatchJournal(history.Predecessor.Journal, second) &&
		validGatewayV2StoppedFinal(history.Predecessor.State, history.Predecessor.Journal, first, true) &&
		validGatewayV2StoppedFinal(history.Predecessor.State, history.Predecessor.Journal, second, true)
	if !transportFirstValid || !transportSecondValid || !transportValid || !rawUnchanged {
		t.Fatal("raw A0 retained NativeV2 transport adaptation did not preserve the authenticated envelope")
	}
	transport := transportSecond

	identity := newGeneratedDomainIdentityFixtureAtRoot(t, fresh.options.DataRoot)
	generatedDomainA0RestoreDeploymentIdentityGuard(t, identity)
	identity.options.HostPort = fresh.options.HostPort
	identity.options.CommandTimeout = candidateOptions.CommandTimeout
	identity.options.HealthTimeout = candidateOptions.HealthTimeout
	identity.options.HealthPollInterval = candidateOptions.HealthPollInterval
	identity.options.OutputLimit = candidateOptions.OutputLimit
	identity.options.MemoryBytes = candidateOptions.MemoryBytes
	identity.options.MilliCPUs = candidateOptions.MilliCPUs
	identity.options.PIDs = candidateOptions.PIDs
	identity.options.TmpfsBytes = candidateOptions.TmpfsBytes
	identity.options.LogSize = candidateOptions.LogSize
	identity.options.LogFiles = candidateOptions.LogFiles
	identity.options.ReplacementDiskBytes = candidateOptions.ReplacementDiskBytes
	if !generatedDomainRetainedV2SamePolicyOptions(
		generatedDomainCandidateOptions(identity.options), candidateOptions,
	) {
		t.Fatal("retained NativeV2 cold authority does not preserve the sealed candidate policy options")
	}
	startup, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), identity.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := resolveGeneratedDomainColdGatewayExpectation(startup, protected)
	if err != nil || expected.kind != generatedDomainGatewayCurrentV1 || expected.currentV1 == nil ||
		expected.terminal == nil || expected.retainedV1 == nil ||
		expected.currentV1.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind {
		t.Fatalf("raw A0 retained CurrentV1 expectation=%#v error=%v", expected, err)
	}
	selection, err := fresh.selectGatewayCurrentLocked(context.Background(), database)
	if err != nil || selection.State == nil || selection.Terminal == nil ||
		selection.State.Digest != expected.currentV1.Digest {
		t.Fatalf("raw A0 current selection=%#v error=%v", selection, err)
	}
	current := generatedDomainCurrentAttestationFixtureFromSelection(t, selection, fresh.options.HostPort)
	if normalizeID(runner.final.ID) != normalizeID(current.container.ID) ||
		strings.TrimPrefix(runner.final.Name, "/") != strings.TrimPrefix(current.container.Name, "/") ||
		!runner.final.Running {
		t.Fatal("raw A0 running CurrentV1 physical container is not bound to the protected selection")
	}
	if !generatedDomainRetainedV2BindCurrentPhysicalNetworks(&current, runner.final,
		runner.finalRuntime, transport) {
		t.Fatal("raw A0 running CurrentV1 physical networks are not bound to the protected selection")
	}
	gateway := generatedDomainCurrentOuterGatewayRunner(t, current, false)
	generatedDomainRetainedV1ConfigureRunner(t, gateway, transport)
	generatedDomainRetainedV2ConfigureRunner(t, gateway, history.Predecessor.State, transport)
	currentObjects := generatedDomainRetainedV1CurrentGatewayObjects(t, current)
	applicationObjects := generatedDomainRetainedV2BindApplicationObjects(t,
		generatedDomainA0ApplicationObjects(t, []candidateOwnerPhysical{physical}), transport)
	retainedV1Objects := generatedDomainRetainedV1CensusObjects(t, transport)
	v2Objects := generatedDomainA0GatewayObjects(history.Predecessor.State, history.Predecessor.Journal)
	if mutate != nil {
		v2Objects = mutate(v2Objects, history.Predecessor.State, history.Predecessor.Journal)
	}
	generatedDomainRetainedV2AssertDistinctPhysicalCensus(t, current, history.Predecessor.State,
		history.Predecessor.Journal, currentObjects, applicationObjects, retainedV1Objects, v2Objects)
	objects := append([]generatedDomainDockerObject(nil), currentObjects...)
	objects = append(objects, applicationObjects...)
	objects = append(objects, retainedV1Objects...)
	objects = append(objects, v2Objects...)
	const daemonID = "generated-domain-retained-v2-a0"
	construction := &generatedDomainConstructionRunner{
		roster: &generatedDomainCandidateRosterRunner{physical: []candidateOwnerPhysical{physical}},
		delegate: &generatedDomainRecoveryCensusRunner{
			daemonID: daemonID, objects: objects,
			imageID: gateway.image.ID, gateway: gateway,
		},
	}
	startupReader, readerErr := newGeneratedDomainStartupReader(construction, identity.options, daemonID)
	var attestation generatedDomainGatewayAttestation
	if readerErr == nil {
		attestation, readerErr = startupReader.attestCurrent(context.Background(), time.Now().Add(time.Minute),
			generatedDomainDockerStartupSnapshot{objects: cloneGeneratedDomainRetainedV2Objects(objects)}, expected, true)
	}
	if readerErr != nil || !attestation.exact || attestation.kind != expected.kind ||
		attestation.operationID != expected.currentV1.Lineage.OperationID ||
		attestation.protectedGeneration != expected.currentV1.Lineage.ProtectedGeneration ||
		attestation.stateDigest != expected.stateDigest {
		t.Fatalf("raw A0 CurrentV1 full attestation preflight=%s exact=%t",
			generatedDomainRetainedV2CurrentAttestationCategory(readerErr), attestation.exact)
	}
	fixture := generatedDomainRetainedV2A0Fixture{
		identity: identity, runner: construction,
		environment: &candidateFactoryEnvironment{path: t.TempDir()}, manager: fresh,
		composition: runner, physical: physical, current: runner.final,
		currentState: current.stateV1,
		state:        history.Predecessor.State,
		journal:      history.Predecessor.Journal, observation: transport,
		census:  cloneGeneratedDomainRetainedV2Objects(objects),
		effects: len(runner.effects),
	}
	if !reflect.DeepEqual(fixture.census, construction.delegate.objects) {
		t.Fatal("retained NativeV2 fixture did not capture a lossless Docker census")
	}
	if mutate == nil {
		generatedDomainRetainedV2RequireProductionCensus(t, fixture)
	}
	fixture.before = generatedDomainRetainedV2Protection(t, fixture)
	return fixture
}

func generatedDomainRetainedV2CurrentAttestationCategory(err error) string {
	if err == nil {
		return "ok"
	}
	switch err.Error() {
	case "generated domain current gateway expectation is invalid":
		return "expectation"
	case "generated domain current gateway is incomplete":
		return "census"
	case "generated domain current application network is incomplete":
		return "application_census"
	case "generated domain current application network is invalid":
		return "application_identity"
	case "generated domain current application network identity is duplicated":
		return "application_duplicate"
	case "generated domain current gateway static identity is invalid":
		return "static_identity"
	case "generated domain current gateway restart config is invalid":
		return "restart_config"
	case "generated domain current gateway live config is invalid":
		return "live_config"
	case "generated domain current gateway challenge is invalid":
		return "challenge"
	default:
		return "read_error"
	}
}

func generatedDomainRetainedV2BindApplicationObjects(t *testing.T, objects []generatedDomainDockerObject,
	observation gatewayV2DockerObservation,
) []generatedDomainDockerObject {
	t.Helper()
	result := cloneGeneratedDomainRetainedV2Objects(objects)
	seen := make(map[string]struct{}, len(observation.ApplicationNetworks))
	identities := make(map[string]struct{}, len(observation.ApplicationNetworkIDs))
	for index := range result {
		object := &result[index]
		if object.kind != "network" {
			continue
		}
		network, networkFound := observation.ApplicationNetworks[object.name]
		id, idFound := observation.ApplicationNetworkIDs[object.name]
		owner := object.labels["io.rig.application"]
		if !networkFound || !idFound || !generatedDomainCanonicalDockerID(id, false) ||
			!validApplicationNetwork(network.identity(), owner) ||
			!reflect.DeepEqual(object.labels, network.Labels) || object.data.driver != network.Driver ||
			object.data.scope != network.Scope || !reflect.DeepEqual(object.data.options, network.Options) {
			t.Fatalf("application network %q is not bound to its authenticated Docker observation", object.name)
		}
		if _, duplicate := seen[object.name]; duplicate {
			t.Fatalf("application network %q appears more than once in the Docker census", object.name)
		}
		if _, duplicate := identities[id]; duplicate {
			t.Fatalf("application network %q reuses an authenticated Docker identity", object.name)
		}
		seen[object.name] = struct{}{}
		identities[id] = struct{}{}
		object.id = id
	}
	if len(seen) != len(observation.ApplicationNetworks) {
		t.Fatalf("application network census=%d observation=%d", len(seen), len(observation.ApplicationNetworks))
	}
	return result
}

func generatedDomainRetainedV2BindCurrentPhysicalNetworks(current *generatedDomainCurrentAttestationFixture,
	container caddyInspection,
	runtime gatewayContainerRuntime, observation gatewayV2DockerObservation,
) bool {
	if current == nil || current.stateV1.Identity.Rebind == nil ||
		normalizeID(container.ID) != normalizeID(current.container.ID) {
		return false
	}
	originalContainer := cloneGeneratedDomainColdRetainedGatewayContainer(container)
	originalRuntime := cloneGeneratedDomainColdRetainedGatewayRuntime(runtime)
	owners, valid := gatewayRouteNetworkOwners(gatewayCurrentPhysicalRoutes(current.stateV1))
	if !valid || len(owners) != len(observation.ApplicationNetworks) ||
		len(owners) != len(observation.ApplicationNetworkIDs) {
		return false
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	applications := make([]gatewayRebindHandoverApplicationNetwork, 0, len(names))
	for _, name := range names {
		network, networkFound := observation.ApplicationNetworks[name]
		id, idFound := observation.ApplicationNetworkIDs[name]
		configured, configuredFound := runtime.ConfiguredNetworks[name]
		if !networkFound || !idFound || !configuredFound ||
			!generatedDomainCanonicalDockerID(id, false) || configured.NetworkID != id ||
			!validApplicationNetwork(network.identity(), owners[name]) ||
			!validGatewayApplicationNetworkMembership(network, container, name) {
			return false
		}
		applications = append(applications, gatewayRebindHandoverApplicationNetwork{Name: name, ID: id})
	}
	ingress := runtime.ConfiguredNetworks[current.stateV1.Identity.Rebind.IngressNetwork]
	if !generatedDomainCurrentStartupNetworksMatch(*current.stateV1.Identity.Rebind,
		current.stateV1.Network, container, runtime, ingress.NetworkID, applications) {
		return false
	}
	boundContainer := cloneGeneratedDomainColdRetainedGatewayContainer(current.container)
	boundRuntime := cloneGeneratedDomainColdRetainedGatewayRuntime(current.runtime)
	for _, application := range applications {
		attachment := container.Networks[application.Name]
		if attachment == nil {
			return false
		}
		attachmentCopy := *attachment
		attachmentCopy.Aliases = append([]string(nil), attachment.Aliases...)
		boundContainer.Networks[application.Name] = &attachmentCopy
		boundRuntime.ConfiguredNetworks[application.Name] = runtime.ConfiguredNetworks[application.Name]
	}
	if !generatedDomainCurrentStartupNetworksMatch(*current.stateV1.Identity.Rebind,
		current.stateV1.Network, boundContainer, boundRuntime, ingress.NetworkID, applications) ||
		!reflect.DeepEqual(container, originalContainer) || !reflect.DeepEqual(runtime, originalRuntime) {
		return false
	}
	current.container.Networks = boundContainer.Networks
	current.runtime.ConfiguredNetworks = boundRuntime.ConfiguredNetworks
	return true
}

func newGeneratedDomainRetainedV2CompositionFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayRebindCommitInput, *gatewayRebindCompositionDriver, candidateOwnerPhysical,
	GeneratedCandidateProcessOptions,
) {
	t.Helper()
	predecessor := newGatewayRebindPredecessorFixtureWithEndpointLANApproverAndCompletion(
		t, false, '3', uuid.NewString(), false, false)
	if predecessor.seeded == nil || predecessor.seeded.owner == nil || predecessor.seeded.owner.runner == nil {
		t.Fatal("raw A0 app-bearing predecessor physical fixture is incomplete")
	}
	physical := predecessor.seeded.owner.runner.snapshot()
	candidateOptions := predecessor.seeded.owner.options
	if candidateOptions != predecessor.seeded.owner.authority.options {
		t.Fatal("app-bearing rebind predecessor candidate policy options are not canonical")
	}
	predecessor.seeded.complete(t)
	predecessor.seeded = nil
	predecessor, input, template := newGatewayRebindCoordinatorFixtureWithPredecessor(t, predecessor)
	runner := &gatewayRebindCompositionRunner{gatewayRebindTypedHandoverRuntimeRunner: &gatewayRebindTypedHandoverRuntimeRunner{
		t: t, predecessorState: predecessor.state,
		predecessor: gatewayRebindFixtureDockerObservation(t, predecessor), finalID: strings.Repeat("9", 64)},
		before: predecessor.manager.runner}
	predecessor.manager.gatewayRebindV2NetworkObserver = func(_ context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		value := template.template.intent.NetworkObservation
		value.OperationID, value.ClaimRequestDigest, value.ProfileSpecDigest = claim.Spec.OperationID,
			claim.RequestDigest, claim.ConfigureApproval.SpecDigest
		value.Candidates = append(value.Candidates, gatewayRebindSuccessorNetworkCandidate{
			InterfaceID: predecessor.state.Profile.InterfaceID,
			IPv4:        predecessor.state.Profile.SelectedIPv4, Prefix: "192.168.96.0/24"})
		sort.Slice(value.Candidates, func(i, j int) bool {
			return gatewayRebindCandidateLess(value.Candidates[i], value.Candidates[j])
		})
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
	predecessor.manager.runner = runner
	driver := &gatewayRebindCompositionDriver{
		managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{manager: predecessor.manager},
		t:                                    t, runner: runner,
	}
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
	return predecessor, input, driver, physical, candidateOptions
}

func generatedDomainRetainedV2SamePolicyOptions(left, right GeneratedCandidateProcessOptions) bool {
	return left.MemoryBytes == right.MemoryBytes && left.MilliCPUs == right.MilliCPUs &&
		left.PIDs == right.PIDs && left.TmpfsBytes == right.TmpfsBytes &&
		left.LogSize == right.LogSize && left.LogFiles == right.LogFiles &&
		left.ReplacementDiskBytes == right.ReplacementDiskBytes &&
		left.CommandTimeout == right.CommandTimeout && left.HealthTimeout == right.HealthTimeout &&
		left.HealthPollInterval == right.HealthPollInterval && left.OutputLimit == right.OutputLimit
}

func generatedDomainRetainedV2CanonicalTransport(value gatewayV2DockerObservation) (
	gatewayV2DockerObservation, bool,
) {
	result := value
	result.V1Container.ID = normalizeID(value.V1Container.ID)
	result.V1NetworkID = normalizeID(value.V1NetworkID)
	result.V1Runtime = generatedDomainRetainedV2CanonicalRuntime(value.V1Runtime)
	result.V1ApplicationNetworkIDs = cloneGeneratedDomainColdRetainedGatewayStringMap(
		value.V1ApplicationNetworkIDs)
	for name, id := range result.V1ApplicationNetworkIDs {
		result.V1ApplicationNetworkIDs[name] = normalizeID(id)
	}
	result.V1Network = cloneGeneratedDomainColdRetainedGatewayNetwork(value.V1Network)
	var valid bool
	result.V1Network.Containers, valid = generatedDomainRetainedV2CanonicalMembers(value.V1Network.Containers)
	if !valid {
		return gatewayV2DockerObservation{}, false
	}
	result.V1ApplicationNetworks, valid = generatedDomainRetainedV2CanonicalNetworks(value.V1ApplicationNetworks)
	if !valid {
		return gatewayV2DockerObservation{}, false
	}

	result.StageContainer.ID = normalizeID(value.StageContainer.ID)
	result.StageRuntime = generatedDomainRetainedV2CanonicalRuntime(value.StageRuntime)
	result.FinalContainer.ID = normalizeID(value.FinalContainer.ID)
	result.IngressNetworkID = normalizeID(value.IngressNetworkID)
	result.FinalRuntime = generatedDomainRetainedV2CanonicalRuntime(value.FinalRuntime)
	result.IngressNetwork = cloneGeneratedDomainColdRetainedGatewayNetwork(value.IngressNetwork)
	result.IngressNetwork.Containers, valid = generatedDomainRetainedV2CanonicalMembers(
		value.IngressNetwork.Containers)
	if !valid {
		return gatewayV2DockerObservation{}, false
	}
	result.ApplicationNetworkIDs = cloneGeneratedDomainColdRetainedGatewayStringMap(value.ApplicationNetworkIDs)
	for name, id := range result.ApplicationNetworkIDs {
		result.ApplicationNetworkIDs[name] = normalizeID(id)
	}
	result.ApplicationNetworks, valid = generatedDomainRetainedV2CanonicalNetworks(value.ApplicationNetworks)
	if !valid {
		return gatewayV2DockerObservation{}, false
	}
	return result, true
}

func generatedDomainRetainedV2CanonicalRuntime(value gatewayContainerRuntime) gatewayContainerRuntime {
	result := cloneGeneratedDomainColdRetainedGatewayRuntime(value)
	for name, configured := range result.ConfiguredNetworks {
		configured.NetworkID = normalizeID(configured.NetworkID)
		configured.EndpointID = normalizeID(configured.EndpointID)
		result.ConfiguredNetworks[name] = configured
	}
	return result
}

func generatedDomainRetainedV2CanonicalNetworks(values map[string]caddyNetworkInspection) (
	map[string]caddyNetworkInspection, bool,
) {
	if values == nil {
		return nil, true
	}
	result := make(map[string]caddyNetworkInspection, len(values))
	for name, value := range values {
		copy := cloneGeneratedDomainColdRetainedGatewayNetwork(value)
		var valid bool
		copy.Containers, valid = generatedDomainRetainedV2CanonicalMembers(value.Containers)
		if !valid {
			return nil, false
		}
		result[name] = copy
	}
	return result, true
}

func generatedDomainRetainedV2CanonicalMembers(values map[string]caddyNetworkContainerInspection) (
	map[string]caddyNetworkContainerInspection, bool,
) {
	if values == nil {
		return nil, true
	}
	result := make(map[string]caddyNetworkContainerInspection, len(values))
	for id, member := range values {
		id = normalizeID(id)
		if id == "" {
			return nil, false
		}
		if _, duplicate := result[id]; duplicate {
			return nil, false
		}
		result[id] = member
	}
	return result, true
}

func generatedDomainRetainedV2AssertDistinctPhysicalCensus(t *testing.T,
	current generatedDomainCurrentAttestationFixture, predecessor gatewayV2RouteState,
	journal gatewayMigrationJournal, groups ...[]generatedDomainDockerObject,
) {
	t.Helper()
	if current.stateV1.Identity.Rebind == nil ||
		current.stateV1.Identity.Rebind.FinalContainer == predecessor.Identity.FinalContainer ||
		current.stateV1.Identity.Rebind.IngressNetwork == predecessor.Identity.IngressNetwork ||
		current.stateV1.Identity.Rebind.ConfigVolume == predecessor.Identity.ConfigVolume ||
		current.stateV1.Identity.Rebind.DataVolume == predecessor.Identity.DataVolume ||
		normalizeID(current.container.ID) == normalizeID(journal.Resources.FinalContainerID) ||
		normalizeID(current.network.ID) == normalizeID(journal.Resources.IngressNetworkID) {
		t.Fatal("retained NativeV2 predecessor was aliased to the distinct CurrentV1 successor")
	}
	seen := make(map[string]struct{})
	sharedImages := make(map[string]generatedDomainDockerObject)
	for _, group := range groups {
		for _, object := range group {
			identity := object.id
			if object.kind == "volume" {
				identity = object.name
			}
			if object.kind == "" || identity == "" {
				t.Fatal("retained NativeV2 physical census contains an empty identity")
			}
			key := object.kind + "\x00" + identity
			if previous, duplicate := sharedImages[key]; duplicate && object.kind == "image" {
				if !reflect.DeepEqual(previous, object) {
					t.Fatal("retained NativeV2 shared image projection is crossed")
				}
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				t.Fatal("retained NativeV2 physical census contains a crossed duplicate")
			}
			seen[key] = struct{}{}
			if object.kind == "image" {
				sharedImages[key] = object
			}
		}
	}
}

func generatedDomainRetainedV2RequireProductionCensus(t *testing.T,
	fixture generatedDomainRetainedV2A0Fixture,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), generatedDomainStartupObservationTimeout)
	defer cancel()
	options, ok := normalizeGeneratedDomainOptions(fixture.identity.options)
	if !ok {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "resolver", "none", "options")
	}
	database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(ctx)
	if err != nil {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "resolver", "none", "database")
	}
	protected, err := readGeneratedDomainProtectedStartup(ctx, options.DataRoot)
	if err != nil {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "resolver", "none", "protected")
	}
	expectation, err := resolveGeneratedDomainGatewayExpectation(database, protected)
	if err != nil {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "resolver", "none", "gateway_expectation")
	}
	recovery := resolveGeneratedDomainRecoveryDockerExpectation(database, protected)
	census, err := resolveGeneratedDomainDockerCensus(protected)
	if err != nil {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "resolver", "none", "protected_census")
	}
	reader, err := newGeneratedDomainStartupReader(fixture.runner, options, fixture.runner.delegate.daemonID)
	if err != nil {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "resolver", "none", "reader")
	}
	reader.expectation = generatedDomainStartupObservationGateway(expectation, recovery)
	reader.census = census
	reader.recovery = recovery
	deadline, _ := ctx.Deadline()
	before := len(fixture.runner.snapshot())
	first, firstInventory, err := reader.readDockerCensus(ctx, deadline)
	if err != nil {
		commands := fixture.runner.snapshot()
		generatedDomainRetainedV2CensusPreconditionFailure(t, "reader_first",
			generatedDomainRetainedV2LastCommandFamily(commands[before:]),
			generatedDomainRetainedV2CensusErrorRule(err))
	}
	between := len(fixture.runner.snapshot())
	second, secondInventory, err := reader.readDockerCensus(ctx, deadline)
	if err != nil {
		commands := fixture.runner.snapshot()
		generatedDomainRetainedV2CensusPreconditionFailure(t, "reader_second",
			generatedDomainRetainedV2LastCommandFamily(commands[between:]),
			generatedDomainRetainedV2CensusErrorRule(err))
	}
	if !reflect.DeepEqual(firstInventory, secondInventory) || !sameGeneratedDomainDockerStartup(first, second) {
		generatedDomainRetainedV2CensusPreconditionFailure(t, "equality", "none", "two_read_stability")
	}
}

func generatedDomainRetainedV2CensusPreconditionFailure(t *testing.T, stage, commandFamily, rule string) {
	t.Helper()
	t.Fatalf("retained NativeV2 production census precondition failed: stage=%s command_family=%s rule=%s",
		stage, commandFamily, rule)
}

func generatedDomainRetainedV2LastCommandFamily(commands [][]string) string {
	if len(commands) == 0 {
		return "none"
	}
	command := commands[len(commands)-1]
	if len(command) == 0 {
		return "unknown"
	}
	if len(command) >= 2 && command[1] == "ls" {
		switch command[0] {
		case "container", "network", "volume", "image":
			return command[0] + "_list"
		}
	}
	if len(command) >= 2 && command[1] == "inspect" {
		switch command[0] {
		case "container", "network", "volume", "image":
			return command[0] + "_projection"
		}
	}
	return "unknown"
}

func generatedDomainRetainedV2CensusErrorRule(err error) string {
	if err == nil {
		return "none"
	}
	switch err.Error() {
	case "generated domain Docker read deadline expired":
		return "deadline"
	case "generated domain Docker read failed":
		return "read"
	case "generated domain Docker census exceeds its bound",
		"generated domain Docker inventory exceeds its bound",
		"generated domain Docker image inventory exceeds its bound":
		return "budget"
	case "generated domain Docker inventory is invalid",
		"generated domain Docker inventory is malformed",
		"generated domain Docker image inventory is invalid":
		return "schema"
	case "generated domain Docker identity is invalid":
		return "identity"
	case "generated domain Docker inventory is duplicated":
		return "duplicate"
	case "generated domain Docker inspection is invalid":
		return "projection_schema"
	case "generated domain Docker listed object changed",
		"generated domain Docker listed image changed":
		return "projection_correspondence"
	default:
		return "read_error"
	}
}

func generatedDomainRetainedV2ConfigureRunner(t *testing.T, runner *generatedDomainGatewayAttestationRunner,
	state gatewayV2RouteState, observation gatewayV2DockerObservation,
) {
	t.Helper()
	if runner.containers == nil {
		runner.containers = make(map[string]gatewayContainerInspection)
	}
	runner.containers[observation.FinalContainer.ID] = gatewayContainerInspection{
		caddyInspection: observation.FinalContainer, gatewayContainerRuntime: observation.FinalRuntime}
	if runner.networks == nil {
		runner.networks = make(map[string]gatewayNetworkIdentityInspection)
	}
	runner.networks[observation.IngressNetworkID] = gatewayNetworkIdentityInspection{
		ID: observation.IngressNetworkID, Name: observation.IngressNetwork.Name,
		Driver: observation.IngressNetwork.Driver, Scope: observation.IngressNetwork.Scope,
		Internal: observation.IngressNetwork.Internal, Options: cloneStringMap(observation.IngressNetwork.Options),
		IPAM: observation.IngressNetwork.IPAM, Labels: cloneStringMap(observation.IngressNetwork.Labels),
		Containers: observation.IngressNetwork.Containers,
	}
	for name, network := range observation.ApplicationNetworks {
		id, found := observation.ApplicationNetworkIDs[name]
		if !found || id == "" || network.Name != name {
			t.Fatalf("retained V2 application network %q is not bound to an observed identity", name)
		}
		if _, duplicate := runner.networks[id]; duplicate {
			t.Fatalf("retained V2 application network %q reuses a Docker identity", name)
		}
		runner.networks[id] = generatedDomainRetainedV2NetworkIdentity(id, network)
	}
	runner.volumes[observation.ConfigVolume.Name] = generatedDomainGatewayVolumeIdentity(
		observation.ConfigVolume, observation.ConfigVolumeIdentity)
	runner.volumes[observation.DataVolume.Name] = generatedDomainGatewayVolumeIdentity(
		observation.DataVolume, observation.DataVolumeIdentity)
	if runner.restarts == nil {
		runner.restarts = make(map[string]generatedDomainRestartFixture)
	}
	runner.restarts[observation.FinalContainer.ID] = generatedDomainRestartFixture{
		name: state.Identity.ActiveConfigFilename, body: append([]byte(nil), observation.FinalRestartConfig...)}
}

func generatedDomainRetainedV2Protection(t *testing.T,
	fixture generatedDomainRetainedV2A0Fixture,
) generatedDomainRetainedV1RebindProtectionSnapshot {
	t.Helper()
	database, err := snapshotGatewayRebindFixtureDatabase(context.Background(), fixture.identity.database)
	if err != nil {
		t.Fatal(err)
	}
	history, err := readGatewayHistorySnapshotPolicy(fixture.manager.store, true, true)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	return generatedDomainRetainedV1RebindProtectionSnapshot{database: database, history: history, protected: protected}
}

func generatedDomainRetainedV2AssertProtectedUnchanged(t *testing.T, fixture generatedDomainRetainedV2A0Fixture) {
	t.Helper()
	after := generatedDomainRetainedV2Protection(t, fixture)
	databaseSame := reflect.DeepEqual(fixture.before.database, after.database)
	historySame := sameGatewayHistorySnapshot(fixture.before.history, after.history)
	protectedSame := sameGeneratedDomainProtectedStartup(fixture.before.protected, after.protected)
	if !databaseSame || !historySame || !protectedSame {
		t.Fatalf("retained NativeV2 cold construction changed protected state: database=%t history=%t protected=%t",
			databaseSame, historySame, protectedSame)
	}
}

func generatedDomainRetainedV2AssertNoEffects(t *testing.T, fixture generatedDomainRetainedV2A0Fixture,
	commands [][]string, allowCurrentAttestation bool,
) {
	t.Helper()
	for _, command := range commands {
		if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
			t.Fatalf("retained NativeV2 cold construction dispatched an effect or capacity probe: %#v", command)
		}
	}
	if allowCurrentAttestation {
		if !generatedDomainRetainedV2ExactCurrentAttestationDelta(
			fixture.currentState, fixture.current.ID, fixture.observation.V1Container.ID,
			fixture.observation.FinalContainer.ID, fixture.state.Identity.ActiveConfigFilename, commands,
		) {
			t.Fatal("retained NativeV2 accepted an invalid current attestation command delta")
		}
	} else {
		for _, command := range commands {
			if len(command) >= 2 && command[0] == "container" && command[1] == "exec" {
				t.Fatal("refused retained NativeV2 cold construction dispatched gateway traffic")
			}
		}
	}
	if len(fixture.composition.effects) != fixture.effects {
		t.Fatalf("retained NativeV2 cold construction dispatched physical effects: before=%d after=%d",
			fixture.effects, len(fixture.composition.effects))
	}
	if fixture.runner.delegate == nil || !reflect.DeepEqual(fixture.census, fixture.runner.delegate.objects) {
		t.Fatal("retained NativeV2 cold construction changed the observed Docker census")
	}
}

func generatedDomainRetainedV2ExactCurrentAttestationDelta(state gatewayCurrentRouteState,
	containerID, retainedV1ContainerID, retainedV2ContainerID, retainedV2ActiveConfigFilename string,
	commands [][]string,
) bool {
	restart, admin, challenge, valid := generatedDomainRetainedV2CurrentAttestationCommands(state, containerID)
	retainedV1Restart, retainedV1Valid := generatedDomainRetainedV2RestartConfigCommand(
		retainedV1ContainerID, gatewayV2ActiveConfigFile)
	retainedV2Restart, retainedV2Valid := generatedDomainRetainedV2RestartConfigCommand(
		retainedV2ContainerID, retainedV2ActiveConfigFilename)
	if !valid || !retainedV1Valid || !retainedV2Valid ||
		reflect.DeepEqual(restart, retainedV1Restart) || reflect.DeepEqual(restart, retainedV2Restart) ||
		reflect.DeepEqual(retainedV1Restart, retainedV2Restart) {
		return false
	}
	restartIndex, adminIndex, challengeIndex := -1, -1, -1
	restartCount, retainedV1Count, retainedV2Count, adminCount, challengeCount := 0, 0, 0, 0, 0
	for index, command := range commands {
		switch {
		case reflect.DeepEqual(command, restart):
			restartCount++
			restartIndex = index
		case reflect.DeepEqual(command, retainedV1Restart):
			retainedV1Count++
		case reflect.DeepEqual(command, retainedV2Restart):
			retainedV2Count++
		case reflect.DeepEqual(command, admin):
			adminCount++
			adminIndex = index
		case reflect.DeepEqual(command, challenge):
			challengeCount++
			challengeIndex = index
		case len(command) >= 2 && command[0] == "container" &&
			(command[1] == "exec" || command[1] == "cp"):
			return false
		}
	}
	return restartCount == 1 &&
		retainedV1Count == generatedDomainRetainedV2ExpectedV1RestartReads &&
		retainedV2Count == generatedDomainRetainedV2ExpectedV2RestartReads &&
		adminCount == 1 && challengeCount == 1 &&
		restartIndex < adminIndex && adminIndex < challengeIndex
}

func generatedDomainRetainedV2RestartConfigCommand(containerID, activeConfigFilename string) (
	[]string, bool,
) {
	containerID = normalizeID(containerID)
	if !generatedDomainCanonicalDockerID(containerID, false) || !validName(activeConfigFilename, 255) {
		return nil, false
	}
	return []string{"container", "cp", containerID + ":/config/" + activeConfigFilename, "-"}, true
}

func generatedDomainRetainedV2CurrentAttestationCommands(state gatewayCurrentRouteState,
	containerID string,
) (restart, admin, challenge []string, valid bool) {
	containerID = normalizeID(containerID)
	token, err := gatewayCurrentPhysicalProbeToken(state)
	if err != nil || !generatedDomainCanonicalDockerID(containerID, false) ||
		state.Identity.Rebind == nil || !validName(state.Identity.Rebind.ActiveConfigFilename, 255) {
		return nil, nil, nil, false
	}
	portToken := gatewayV2PortChallenge(token, state.Profile.PortStart)
	restart, valid = generatedDomainRetainedV2RestartConfigCommand(
		containerID, state.Identity.Rebind.ActiveConfigFilename)
	if !valid {
		return nil, nil, nil, false
	}
	admin = []string{"container", "exec", containerID,
		"curl", "--disable", "--silent", "--show-error", "--fail", "--proto", "=http", "--noproxy", "*",
		"--max-time", "10", "http://127.0.0.1:2019/config/"}
	challenge = []string{"container", "exec", containerID,
		"curl", "--disable", "--silent", "--show-error", "--output", "-", "--write-out", "\n%{http_code}",
		"--http1.1", "--proto", "=http", "--noproxy", "*", "--connect-timeout", "1", "--max-time", "2",
		"--header", "Host: " + state.Profile.SelectedIPv4,
		"http://" + net.JoinHostPort(state.Network.ContainerIPv4,
			strconv.FormatUint(uint64(state.Profile.PortStart), 10)) + gatewayV2ChallengePathPrefix + portToken}
	return restart, admin, challenge, true
}

func cloneGeneratedDomainRetainedV2Objects(values []generatedDomainDockerObject) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, len(values))
	for index, value := range values {
		result[index] = value
		result[index].labels = cloneGeneratedDomainColdRetainedGatewayStringMap(value.labels)
		result[index].data.options = cloneGeneratedDomainColdRetainedGatewayStringMap(value.data.options)
	}
	return result
}
