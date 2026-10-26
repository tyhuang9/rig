//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedbuilder"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainRetainedV1ColdFixture struct {
	generatedDomainA0NamespaceFixture
	observation gatewayV2DockerObservation
}

func TestCloneGeneratedDomainColdRetainedGatewayNetworkPreservesMembership(t *testing.T) {
	t.Run("nonempty_independent", func(t *testing.T) {
		original := caddyNetworkInspection{Containers: map[string]caddyNetworkContainerInspection{
			strings.Repeat("a", 64): {Name: "retained-a", IPv4Address: "10.241.0.2/28"},
			strings.Repeat("b", 64): {Name: "retained-b", IPv4Address: "10.241.0.3/28"},
		}}
		cloned := cloneGeneratedDomainColdRetainedGatewayNetwork(original)
		if !reflect.DeepEqual(cloned.Containers, original.Containers) || len(cloned.Containers) != 2 {
			t.Fatalf("cloned retained network members = %#v, want %#v", cloned.Containers, original.Containers)
		}
		original.Containers[strings.Repeat("a", 64)] = caddyNetworkContainerInspection{Name: "source-mutated"}
		cloned.Containers[strings.Repeat("b", 64)] = caddyNetworkContainerInspection{Name: "clone-mutated"}
		if cloned.Containers[strings.Repeat("a", 64)].Name != "retained-a" ||
			original.Containers[strings.Repeat("b", 64)].Name != "retained-b" {
			t.Fatalf("retained network clone aliases source: source=%#v clone=%#v", original.Containers, cloned.Containers)
		}
	})
	t.Run("nil_preserved", func(t *testing.T) {
		cloned := cloneGeneratedDomainColdRetainedGatewayNetwork(caddyNetworkInspection{})
		if cloned.Containers != nil {
			t.Fatalf("nil retained network members cloned as %#v", cloned.Containers)
		}
	})
}

func TestGeneratedDomainColdRetainedGatewayDefersCanonicalNativeV1ToActiveCensus(t *testing.T) {
	state, _, observation := generatedDomainNativeV1AttestationFixture(t)
	expected, err := resolveGeneratedDomainColdGatewayExpectation(
		deployments.GeneratedDomainStartupSnapshot{},
		generatedDomainProtectedStartupSnapshot{nativeV1: &state},
	)
	if err != nil || !generatedDomainColdActiveNativeV1Expectation(expected) {
		t.Fatalf("canonical active native V1 expectation = %#v, %v", expected, err)
	}
	snapshot := generatedDomainDockerStartupSnapshot{
		objects: generatedDomainRetainedV1CensusObjects(t, observation),
	}

	t.Run("canonical_selection_leaves_complete_triple_to_active_census", func(t *testing.T) {
		proof, err := attestGeneratedDomainColdRetainedGateway(
			context.Background(), nil, nil, snapshot, expected,
		)
		if err != nil || proof.presence != generatedDomainColdRetainedGatewayAbsent ||
			proof.authority != nil || len(proof.objects) != 0 || proof.digest == "" {
			t.Fatalf("active native V1 retained proof = %#v, %v", proof, err)
		}
		for _, object := range snapshot.objects {
			if proof.accounts(object) {
				t.Fatalf("active native V1 object was consumed by retained proof: %#v", object)
			}
		}
	})

	t.Run("names_without_canonical_authority_remain_closed", func(t *testing.T) {
		malformed := expected
		malformed.nativeV1 = nil
		if generatedDomainColdActiveNativeV1Expectation(malformed) {
			t.Fatal("names-only native V1 projection gained active authority")
		}
		proof, err := attestGeneratedDomainColdRetainedGateway(
			context.Background(), nil, nil, snapshot, malformed,
		)
		if !errors.Is(err, errGeneratedDomainConstructionRefused) ||
			!reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
			t.Fatalf("names-only native V1 retained proof = %#v, %v", proof, err)
		}
	})
}

func TestGeneratedDomainColdAccountsForExactRetainedV1Triple(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedDomainRetainedV1ColdFixture(t)
		generatedDomainRetainedV1AssertConstruction(t, fixture, fixture.construction, true, true)
	})
}

func TestGeneratedDomainColdAccountsForExactRetainedV1TripleWithCurrentV2UpgradeOrigin(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedDomainRetainedV1ColdFixture(t)
		generatedDomainRetainedV1InstallCurrentV2(t, &fixture)
		generatedDomainRetainedV1AssertConstruction(t, fixture, fixture.construction, true, true)
	})
}

func TestGeneratedDomainColdRejectsCompleteRetainedV1UnderRebindOrigin(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedDomainRetainedV1RebindFixture(t)
		database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := resolveGeneratedDomainColdGatewayExpectation(database, protected)
		if err != nil || expected.kind != generatedDomainGatewayCurrentV1 || expected.currentV1 == nil ||
			expected.terminal == nil || expected.currentV1.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind ||
			expected.currentV1.Digest != fixture.currentDigest {
			t.Fatalf("canonical rebind-origin gateway expectation = %#v, %v", expected, err)
		}
		if _, supported := generatedDomainColdRetainedGatewaySelectedAuthority(expected); supported {
			t.Fatal("rebind-origin current V1 unexpectedly selected retained-gateway authority")
		}
		objects, presence, err := generatedDomainColdRetainedGatewayObjects(
			generatedDomainDockerStartupSnapshot{objects: fixture.runner.delegate.objects})
		if err != nil || presence != generatedDomainColdRetainedGatewayPresent || len(objects) != 3 ||
			objects[0].id != fixture.observation.V1Container.ID ||
			objects[1].id != fixture.observation.V1NetworkID || objects[2].name != caddyVolumeName {
			t.Fatalf("complete retained V1 rebind census = %#v presence=%d error=%v", objects, presence, err)
		}

		t.Run("private_gate_exact_present_unsupported", func(t *testing.T) {
			beforeCommands := len(fixture.runner.snapshot())
			proof, err := attestGeneratedDomainColdRetainedGateway(context.Background(), nil, nil,
				generatedDomainDockerStartupSnapshot{objects: fixture.runner.delegate.objects}, expected)
			if !errors.Is(err, errGeneratedDomainConstructionRefused) ||
				!reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
				t.Fatalf("private retained V1 unsupported-lineage gate proof=%#v error=%v", proof, err)
			}
			if commands := fixture.runner.snapshot(); len(commands) != beforeCommands {
				t.Fatalf("private retained V1 unsupported-lineage gate dispatched reads: %#v", commands[beforeCommands:])
			}
		})

		t.Run("public_constructor_early_physical_identity_refusal_no_effects_not_gate_reachability", func(t *testing.T) {
			before := generatedDomainRetainedV1RebindProtection(t, fixture)
			domain, err := NewGeneratedDomain(context.Background(), fixture.identity.stores,
				fixture.runner, fixture.environment, generatedColdBoundaryPublicOptions(fixture.identity.options))
			if domain != nil || err == nil {
				t.Fatalf("rebind-origin retained V1 domain = %#v, %v", domain, err)
			}
			assertGeneratedDomainConstructionDiagnostic(t, err, generatedDomainConstructionStageStartupObservation,
				generatedDomainConstructionReasonPhysicalIdentity,
				"generated domain construction was refused: stage=startup_observation reason=physical_identity")
			after := generatedDomainRetainedV1RebindProtection(t, fixture)
			if !reflect.DeepEqual(before.database, after.database) ||
				!sameGatewayHistorySnapshot(before.history, after.history) ||
				!sameGeneratedDomainProtectedStartup(before.protected, after.protected) {
				t.Fatal("rebind-origin retained V1 refusal changed SQL or protected files")
			}
			for _, command := range fixture.runner.snapshot() {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
					t.Fatalf("rebind-origin retained V1 refusal dispatched an effect or capacity probe: %#v", command)
				}
				if generatedDomainRetainedV1DetailedReadCommand(command, fixture.observation) {
					t.Fatalf("rebind-origin retained V1 refusal dispatched a retained detail read: %#v", command)
				}
			}
			generatedDomainA0AssertNoInteractiveGatewayCommands(t, fixture.runner.snapshot())
		})
	})
}

type generatedDomainRetainedV1RebindFixture struct {
	identity      generatedDomainIdentityFixture
	runner        *generatedDomainConstructionRunner
	environment   *candidateFactoryEnvironment
	store         *stateStore
	observation   gatewayV2DockerObservation
	currentDigest string
}

type generatedDomainRetainedV1RebindProtectionSnapshot struct {
	database  gatewayRebindFixtureDatabaseSnapshot
	history   gatewayHistorySnapshot
	protected generatedDomainProtectedStartupSnapshot
}

func newGeneratedDomainRetainedV1RebindFixture(t *testing.T) generatedDomainRetainedV1RebindFixture {
	t.Helper()
	predecessor := newGatewayRebindPredecessorFixtureWithEndpointLANApproverAndCompletion(
		t, false, '3', "", false, false)
	if predecessor.seeded == nil || predecessor.seeded.owner == nil || predecessor.seeded.owner.runner == nil {
		t.Fatal("app-bearing rebind predecessor physical fixture is incomplete")
	}
	physical := predecessor.seeded.owner.runner.snapshot()
	source, err := predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	observation := gatewayV2IdentityTestObservation(t, source, predecessor.state, predecessor.journal,
		gatewayTopologyExactFinalV2)
	generatedDomainAdaptNativeV2Observation(predecessor.state, predecessor.journal, &observation)
	generatedDomainRetainedV1CanonicalizeDockerIDs(&observation)
	if !validStoppedGatewayV1Base(source, predecessor.journal, observation, false) {
		t.Fatal("rebind predecessor retained V1 fixture does not satisfy the stopped-gateway proof")
	}
	predecessor.seeded.complete(t)
	predecessor.seeded = nil
	predecessor, input, driver := newGatewayRebindCoordinatorFixtureWithPredecessor(t, predecessor)
	result, err := predecessor.manager.commitGatewayRebindWithDriver(
		context.Background(), predecessor.repository, input, driver)
	if err != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit ||
		result.FinalPhase != appaccess.GatewayRebindCommitted || !result.FenceReleased {
		t.Fatalf("commit app-bearing rebind fixture = %#v, %v", result, err)
	}
	database, err := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selection, err := predecessor.manager.selectGatewayCurrentLocked(context.Background(), database)
	if err != nil || selection.Kind != gatewayCurrentSelectionRebind || selection.State == nil ||
		selection.Terminal == nil || len(selection.State.Apps) != 1 ||
		selection.State.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind {
		t.Fatalf("committed app-bearing rebind selection = %#v, %v", selection, err)
	}

	identity := newGeneratedDomainIdentityFixtureOnDatabase(t, predecessor.db, predecessor.manager.options.DataRoot)
	identity.options.HostPort = predecessor.manager.options.HostPort
	identity.options.CommandTimeout = predecessor.manager.options.CommandTimeout
	identity.options.OutputLimit = predecessor.manager.options.OutputLimit
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), identity.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	startup, err := identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected, err := resolveGeneratedDomainColdGatewayExpectation(startup, protected)
	if err != nil || expected.kind != generatedDomainGatewayCurrentV1 || expected.currentV1 == nil ||
		expected.terminal == nil || expected.currentV1.Digest != selection.State.Digest ||
		expected.currentV1.Lineage.Kind != appaccess.GatewayRebindSourceGatewayRebind {
		t.Fatalf("resolved app-bearing rebind expectation = %#v, %v", expected, err)
	}
	current := generatedDomainCurrentAttestationFixtureFromSelection(t, selection,
		predecessor.manager.options.HostPort)
	gateway := generatedDomainCurrentOuterGatewayRunner(t, current, false)
	objects := generatedDomainRetainedV1CurrentGatewayObjects(t, current)
	if !generatedDomainExpectedDockerObjects(objects, expected) {
		t.Fatalf("app-bearing rebind Docker objects do not match canonical expectation: %#v", objects)
	}
	objects = append(objects, generatedDomainA0ApplicationObjects(t, []candidateOwnerPhysical{physical})...)
	objects = append(objects, generatedDomainRetainedV1CensusObjects(t, observation)...)
	// App-bearing startup deliberately defers current gateway I/O. Bind the
	// single image response to the retained proof so the complete stopped V1
	// triple remains readable if the unsupported-lineage gate regresses.
	gateway.image = cloneGeneratedDomainColdRetainedGatewayImage(observation.Image)
	generatedDomainRetainedV1ConfigureRunner(t, gateway, observation)
	construction := &generatedDomainConstructionRunner{
		roster: &generatedDomainCandidateRosterRunner{physical: []candidateOwnerPhysical{physical}},
		delegate: &generatedDomainRecoveryCensusRunner{
			daemonID: "generated-domain-retained-v1-rebind", objects: objects,
			imageID: gateway.image.ID, gateway: gateway,
		},
	}
	store, err := newStateStore(identity.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	return generatedDomainRetainedV1RebindFixture{identity: identity, runner: construction,
		environment: &candidateFactoryEnvironment{path: t.TempDir()}, store: store,
		observation: observation, currentDigest: selection.State.Digest}
}

func generatedDomainRetainedV1CurrentGatewayObjects(t *testing.T,
	current generatedDomainCurrentAttestationFixture,
) []generatedDomainDockerObject {
	t.Helper()
	if current.stateV1.Identity.Rebind == nil {
		t.Fatal("app-bearing rebind physical gateway identity is incomplete")
	}
	configName, dataName := current.stateV1.Identity.Rebind.ConfigVolume,
		current.stateV1.Identity.Rebind.DataVolume
	config, configOK := current.volumes[configName]
	data, dataOK := current.volumes[dataName]
	if !configOK || !dataOK {
		t.Fatal("app-bearing rebind physical gateway fixture is incomplete")
	}
	return []generatedDomainDockerObject{
		{kind: "container", id: current.container.ID, name: current.container.Name,
			labels: cloneStringMap(current.container.Labels)},
		{kind: "network", id: current.network.ID, name: current.network.Name,
			labels: cloneStringMap(current.network.Labels), data: generatedDomainDockerObjectData{
				driver: current.network.Driver, scope: current.network.Scope,
				options: cloneStringMap(current.network.Options),
			}},
		{kind: "volume", name: config.Name, labels: cloneStringMap(config.Labels),
			data: generatedDomainDockerObjectData{driver: config.Driver, scope: config.Scope,
				options: cloneStringMap(config.Options), mountpoint: config.Mountpoint, createdAt: config.CreatedAt}},
		{kind: "volume", name: data.Name, labels: cloneStringMap(data.Labels),
			data: generatedDomainDockerObjectData{driver: data.Driver, scope: data.Scope,
				options: cloneStringMap(data.Options), mountpoint: data.Mountpoint, createdAt: data.CreatedAt}},
	}
}

func generatedDomainRetainedV1RebindProtection(t *testing.T,
	fixture generatedDomainRetainedV1RebindFixture,
) generatedDomainRetainedV1RebindProtectionSnapshot {
	t.Helper()
	database, err := snapshotGatewayRebindFixtureDatabase(context.Background(), fixture.identity.database)
	if err != nil {
		t.Fatal("read rebind-origin retained V1 SQL protection snapshot")
	}
	history, err := readGatewayHistorySnapshotPolicy(fixture.store, true, true)
	if err != nil {
		t.Fatal("read rebind-origin retained V1 gateway protection snapshot")
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal("read rebind-origin retained V1 protected namespace snapshot")
	}
	return generatedDomainRetainedV1RebindProtectionSnapshot{
		database: database, history: history, protected: protected,
	}
}

func generatedDomainRetainedV1DetailedReadCommand(command []string,
	observation gatewayV2DockerObservation,
) bool {
	if len(command) == 5 && command[0] == "container" && command[1] == "inspect" &&
		command[3] == generatedDomainStartupContainerInspectFormat && command[4] == observation.V1Container.ID {
		return true
	}
	if len(command) == 5 && command[0] == "network" && command[1] == "inspect" &&
		command[3] == generatedDomainStartupNetworkInspectFormat && command[4] == observation.V1NetworkID {
		return true
	}
	if len(command) == 5 && command[0] == "volume" && command[1] == "inspect" &&
		command[3] == generatedDomainVolumeInspectFormat && command[4] == caddyVolumeName {
		return true
	}
	return len(command) == 4 && command[0] == "container" && command[1] == "cp" &&
		strings.HasPrefix(command[2], observation.V1Container.ID+":/config/")
}

func TestGeneratedDomainColdPreservesNoRetainedV1Compatibility(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	generatedDomainConstructionCase(t, func(t *testing.T) {
		fixture := newGeneratedDomainA0NamespaceFixture(t)
		before := generatedDomainA0ReadProtection(t, fixture)
		domain, err := NewGeneratedDomain(context.Background(), fixture.identity.stores,
			fixture.construction, fixture.environment, generatedColdBoundaryPublicOptions(fixture.identity.options))
		if err != nil || domain == nil || domain.construction == nil || domain.construction.cold == nil ||
			domain.construction.cold.retainedGateway.presence != generatedDomainColdRetainedGatewayAbsent ||
			len(domain.construction.cold.retainedGateway.objects) != 0 {
			t.Fatalf("no-retained V1 cold domain = %#v, %v", domain, err)
		}
		after := generatedDomainA0ReadProtection(t, fixture)
		if !generatedDomainA0SameProtection(before, after) {
			t.Fatal("no-retained V1 construction changed SQL or protected files")
		}
		clear(before.body)
		clear(after.body)
		for _, command := range fixture.runner.snapshot() {
			if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
				t.Fatalf("no-retained V1 reconstruction dispatched an effect or capacity probe: %#v", command)
			}
		}
	})
}

func TestGeneratedDomainColdRejectsIncompleteCrossedOrOrphanRetainedV1(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name             string
		mutate           func(*generatedDomainRetainedV1ColdFixture)
		diagnosticStage  generatedDomainConstructionStage
		diagnosticReason generatedDomainConstructionReason
	}{
		{name: "missing_volume", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			fixture.runner.delegate.objects = generatedDomainRetainedV1RemoveCensusObject(t,
				fixture.runner.delegate.objects, "volume", caddyVolumeName)
		}},
		{name: "crossed_container_identity",
			diagnosticStage:  generatedDomainConstructionStageStartupObservation,
			diagnosticReason: generatedDomainConstructionReasonDockerCensus,
			mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
				object := generatedDomainRetainedV1CensusObject(t, fixture.runner.delegate.objects,
					"container", caddyContainerName)
				object.id = strings.Repeat("e", 64)
			}},
		{name: "extra_legacy_prefixed_container",
			diagnosticStage:  generatedDomainConstructionStageStartupObservation,
			diagnosticReason: generatedDomainConstructionReasonDockerCensus,
			mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
				fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
					kind: "container", id: strings.Repeat("e", 64), name: caddyContainerName + "-orphan"})
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainRetainedV1ColdFixture(t)
				test.mutate(&fixture)
				if test.diagnosticStage != "" {
					generatedDomainRetainedV1AssertConstructionDiagnostic(t, fixture, fixture.construction,
						false, test.diagnosticStage, test.diagnosticReason)
					return
				}
				generatedDomainRetainedV1AssertConstruction(t, fixture, fixture.construction, false, false)
			})
		})
	}
}

func TestGeneratedDomainColdRejectsUnsafeRetainedV1PhysicalProof(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func(*generatedDomainRetainedV1ColdFixture)
	}{
		{name: "running", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			container := fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID]
			container.Running = true
			fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID] = container
		}},
		{name: "restarting", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			container := fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID]
			container.Restarting = true
			fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID] = container
		}},
		{name: "live_network_member", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			network := fixture.runner.delegate.gateway.networks[fixture.observation.V1NetworkID]
			network.Containers = map[string]caddyNetworkContainerInspection{fixture.observation.V1Container.ID: {
				Name: caddyContainerName, IPv4Address: "10.0.0.2/28"}}
			fixture.runner.delegate.gateway.networks[fixture.observation.V1NetworkID] = network
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainRetainedV1ColdFixture(t)
				test.mutate(&fixture)
				generatedDomainRetainedV1AssertConstruction(t, fixture, fixture.construction, false, false)
			})
		})
	}
}

func TestGeneratedDomainColdRejectsCrossedRetainedV1Proof(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func(*generatedDomainRetainedV1ColdFixture)
	}{
		{name: "restart_config", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			restart := fixture.runner.delegate.gateway.restarts[fixture.observation.V1Container.ID]
			restart.body = []byte(`{"apps":{}}`)
			fixture.runner.delegate.gateway.restarts[fixture.observation.V1Container.ID] = restart
		}},
		{name: "volume_filesystem_identity", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			object := generatedDomainRetainedV1CensusObject(t, fixture.runner.delegate.objects,
				"volume", caddyVolumeName)
			object.data.mountpoint += "-crossed"
		}},
		{name: "detailed_container_identity", mutate: func(fixture *generatedDomainRetainedV1ColdFixture) {
			container := fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID]
			container.ID = strings.Repeat("e", 64)
			fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID] = container
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainRetainedV1ColdFixture(t)
				test.mutate(&fixture)
				generatedDomainRetainedV1AssertConstruction(t, fixture, fixture.construction, false, false)
			})
		})
	}
}

func TestGeneratedDomainColdRejectsRetainedV1FinalDrift(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name             string
		wrap             func(*generatedDomainRetainedV1ColdFixture) runtimeprocess.CommandRunner
		diagnosticStage  generatedDomainConstructionStage
		diagnosticReason generatedDomainConstructionReason
	}{
		{name: "same_name_container_replaced", wrap: func(fixture *generatedDomainRetainedV1ColdFixture) runtimeprocess.CommandRunner {
			id := fixture.observation.V1Container.ID
			return &generatedDomainRetainedV1MutationRunner{delegate: fixture.construction, occurrence: 2,
				match: func(args []string) bool {
					// The second complete census starts only after the initial retained triple was accepted.
					return len(args) == 6 && args[0] == "container" && args[1] == "ls"
				}, mutate: func() {
					container := fixture.runner.delegate.gateway.containers[id]
					container.ID = strings.Repeat("e", 64)
					fixture.runner.delegate.gateway.containers[id] = container
				}}
		}},
		{name: "triple_disappears",
			diagnosticStage:  generatedDomainConstructionStageStartupObservation,
			diagnosticReason: generatedDomainConstructionReasonDockerCensus,
			wrap: func(fixture *generatedDomainRetainedV1ColdFixture) runtimeprocess.CommandRunner {
				return &generatedDomainRetainedV1MutationRunner{delegate: fixture.construction, occurrence: 2,
					match: func(args []string) bool {
						return len(args) == 6 && args[0] == "container" && args[1] == "ls"
					}, mutate: func() {
						for _, entry := range []struct{ kind, name string }{
							{"container", caddyContainerName}, {"network", caddyNetworkName}, {"volume", caddyVolumeName},
						} {
							fixture.runner.delegate.objects = generatedDomainRetainedV1RemoveCensusObject(t,
								fixture.runner.delegate.objects, entry.kind, entry.name)
						}
					}}
			}},
		{name: "restart_config_changes", wrap: func(fixture *generatedDomainRetainedV1ColdFixture) runtimeprocess.CommandRunner {
			id := fixture.observation.V1Container.ID
			return &generatedDomainRetainedV1MutationRunner{delegate: fixture.construction, occurrence: 2,
				match: func(args []string) bool {
					return len(args) == 4 && args[0] == "container" && args[1] == "cp" &&
						args[2] == id+":/config/"+gatewayV2ActiveConfigFile
				}, mutate: func() {
					restart := fixture.runner.delegate.gateway.restarts[id]
					restart.body = []byte(`{"apps":{}}`)
					fixture.runner.delegate.gateway.restarts[id] = restart
				}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainRetainedV1ColdFixture(t)
				runner := test.wrap(&fixture)
				if test.diagnosticStage != "" {
					generatedDomainRetainedV1AssertConstructionDiagnostic(t, fixture, runner,
						true, test.diagnosticStage, test.diagnosticReason)
					return
				}
				generatedDomainRetainedV1AssertConstruction(t, fixture, runner, false, true)
			})
		})
	}
}

func TestGeneratedDomainColdRejectsRetainedV1AppearanceOrAuthorityChange(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("triple_appears_after_initial_absence", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			base := newGeneratedDomainA0NamespaceFixture(t)
			observation := generatedDomainRetainedV1Observation(t, base)
			fixture := generatedDomainRetainedV1ColdFixture{generatedDomainA0NamespaceFixture: base,
				observation: observation}
			runner := &generatedDomainRetainedV1MutationRunner{delegate: fixture.construction, occurrence: 2,
				match: func(args []string) bool {
					return len(args) == 6 && args[0] == "container" && args[1] == "ls"
				}, mutate: func() {
					generatedDomainRetainedV1InstallPhysical(t, &fixture.generatedDomainA0NamespaceFixture,
						observation)
				}}
			generatedDomainRetainedV1AssertConstructionDiagnostic(t, fixture, runner, true,
				generatedDomainConstructionStageStartupObservation,
				generatedDomainConstructionReasonDockerCensus)
		})
	})
	t.Run("protected_identity_digest_changes", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture := newGeneratedDomainRetainedV1ColdFixture(t)
			database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := resolveGeneratedDomainColdGatewayExpectation(database, protected)
			if err != nil || expected.upgrade == nil || expected.journal == nil || expected.upgrade.Store == nil {
				t.Fatalf("retained V1 authority-change fixture = %#v, %v", expected, err)
			}
			journal := *expected.journal
			journal.Source.IdentityDigest = strings.Repeat("0", 64)
			runner := &generatedDomainRetainedV1MutationRunner{delegate: fixture.construction, occurrence: 2,
				match: func(args []string) bool {
					return len(args) == 6 && args[0] == "container" && args[1] == "ls"
				}, mutate: func() {
					if writeErr := expected.upgrade.Store.writeExact(expected.upgrade.Store.journalPath,
						expected.upgrade.Store.journalPurpose, journal, false, maxGatewayMigrationBytes); writeErr != nil {
						t.Fatalf("change retained V1 protected identity digest: %v", writeErr)
					}
				}}
			domain, constructErr := NewGeneratedDomain(context.Background(), fixture.identity.stores,
				runner, fixture.environment, generatedColdBoundaryPublicOptions(fixture.identity.options))
			if domain != nil || constructErr == nil || runner.observations() != 2 {
				t.Fatalf("retained V1 authority drift = %#v, %v; observations=%d",
					domain, constructErr, runner.observations())
			}
			assertGeneratedDomainConstructionDiagnostic(t, constructErr,
				generatedDomainConstructionStageStartupObservation,
				generatedDomainConstructionReasonStability,
				"generated domain construction was refused: stage=startup_observation reason=stability")
			for _, command := range fixture.runner.snapshot() {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
					t.Fatalf("retained V1 authority drift dispatched an effect or capacity probe: %#v", command)
				}
			}
		})
	})
}

type generatedDomainRetainedV1MutationRunner struct {
	mu         sync.Mutex
	delegate   runtimeprocess.CommandRunner
	match      func([]string) bool
	mutate     func()
	occurrence int
	seen       int
	mutations  int
}

func (runner *generatedDomainRetainedV1MutationRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	runner.mu.Lock()
	if runner.match != nil && runner.match(request.Args) {
		runner.seen++
		if runner.seen == runner.occurrence && runner.mutate != nil {
			runner.mutations++
			runner.mutate()
		}
	}
	runner.mu.Unlock()
	return runner.delegate.Run(ctx, request)
}

func (runner *generatedDomainRetainedV1MutationRunner) observations() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.seen
}

func (runner *generatedDomainRetainedV1MutationRunner) counts() (int, int) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.seen, runner.mutations
}

func generatedDomainRetainedV1AssertConstruction(t *testing.T, fixture generatedDomainRetainedV1ColdFixture,
	runner runtimeprocess.CommandRunner, pass, gatewayProofAllowed bool,
) {
	t.Helper()
	generatedDomainRetainedV1AssertConstructionWithError(t, fixture, runner, pass, gatewayProofAllowed,
		func(t *testing.T, err error) {
			t.Helper()
			if !errors.Is(err, errGeneratedDomainConstructionRefused) {
				t.Fatalf("invalid retained V1 cold error = %v", err)
			}
		})
}

func generatedDomainRetainedV1AssertConstructionDiagnostic(t *testing.T,
	fixture generatedDomainRetainedV1ColdFixture, runner runtimeprocess.CommandRunner,
	gatewayProofAllowed bool, stage generatedDomainConstructionStage, reason generatedDomainConstructionReason,
) {
	t.Helper()
	generatedDomainRetainedV1AssertConstructionWithError(t, fixture, runner, false, gatewayProofAllowed,
		func(t *testing.T, err error) {
			t.Helper()
			message := "generated domain construction was refused: stage=" + string(stage) +
				" reason=" + string(reason)
			assertGeneratedDomainConstructionDiagnostic(t, err, stage, reason, message)
		})
}

func generatedDomainRetainedV1AssertConstructionWithError(t *testing.T,
	fixture generatedDomainRetainedV1ColdFixture, runner runtimeprocess.CommandRunner,
	pass, gatewayProofAllowed bool, assertRefusal func(*testing.T, error),
) {
	t.Helper()
	before := generatedDomainRetainedV1ReadProtection(t, fixture.generatedDomainA0NamespaceFixture)
	domain, err := NewGeneratedDomain(context.Background(), fixture.identity.stores,
		runner, fixture.environment, generatedColdBoundaryPublicOptions(fixture.identity.options))
	if pass {
		if err != nil || domain == nil || domain.construction == nil || domain.construction.cold == nil ||
			len(domain.construction.cold.groups) != 2 ||
			domain.construction.cold.retainedGateway.presence != generatedDomainColdRetainedGatewayPresent ||
			len(domain.construction.cold.retainedGateway.objects) != 3 {
			t.Fatalf("retained V1 cold domain = %#v, %v", domain, err)
		}
	} else {
		if domain != nil || err == nil {
			t.Fatalf("invalid retained V1 cold domain = %#v, %v", domain, err)
		}
		assertRefusal(t, err)
	}
	if mutation, ok := runner.(*generatedDomainRetainedV1MutationRunner); ok {
		observations, mutations := mutation.counts()
		if observations < mutation.occurrence || mutations != 1 {
			t.Fatalf("retained V1 mutation observations = %d, mutations = %d; want at least %d and exactly 1",
				observations, mutations, mutation.occurrence)
		}
	}

	after := generatedDomainRetainedV1ReadProtection(t, fixture.generatedDomainA0NamespaceFixture)
	if !generatedDomainA0SameProtection(before, after) {
		t.Fatal("retained V1 public construction changed SQL or protected files")
	}
	clear(before.body)
	clear(after.body)
	commands := fixture.runner.snapshot()
	for _, command := range commands {
		if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
			t.Fatalf("retained V1 reconstruction dispatched an effect or capacity probe: %#v", command)
		}
	}
	if !gatewayProofAllowed {
		generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
	}
}

func generatedDomainRetainedV1ReadProtection(t *testing.T,
	fixture generatedDomainA0NamespaceFixture,
) generatedDomainA0ProtectionSnapshot {
	t.Helper()
	databaseSnapshot, err := snapshotGatewayRebindFixtureDatabase(context.Background(), fixture.identity.database)
	if err != nil {
		t.Fatal("read retained V1 SQL protection snapshot")
	}
	history, err := readGatewayHistorySnapshotPolicy(fixture.store, true, true)
	if err != nil {
		t.Fatal("read retained V1 current-aware gateway protection snapshot")
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal("read retained V1 protected namespace snapshot")
	}
	body, file, err := fixture.builder.directory.ReadFileSnapshot(generatedbuilder.IdentityFilename, 8<<10)
	if err != nil {
		t.Fatal("read retained V1 builder protection snapshot")
	}
	return generatedDomainA0ProtectionSnapshot{database: databaseSnapshot, history: history,
		protected: protected, body: body, file: file}
}

func generatedDomainRetainedV1CensusObject(t *testing.T, objects []generatedDomainDockerObject,
	kind, name string,
) *generatedDomainDockerObject {
	t.Helper()
	var result *generatedDomainDockerObject
	for index := range objects {
		object := &objects[index]
		if object.kind != kind || strings.TrimPrefix(object.name, "/") != name {
			continue
		}
		if result != nil {
			t.Fatalf("retained V1 census object %s/%s is duplicated", kind, name)
		}
		result = object
	}
	if result == nil {
		t.Fatalf("retained V1 census object %s/%s is absent", kind, name)
	}
	return result
}

func generatedDomainRetainedV1RemoveCensusObject(t *testing.T, objects []generatedDomainDockerObject,
	kind, name string,
) []generatedDomainDockerObject {
	t.Helper()
	result := make([]generatedDomainDockerObject, 0, len(objects))
	removed := 0
	for _, object := range objects {
		if object.kind == kind && strings.TrimPrefix(object.name, "/") == name {
			removed++
			continue
		}
		result = append(result, object)
	}
	if removed != 1 {
		t.Fatalf("retained V1 census removal %s/%s matched %d objects, want 1", kind, name, removed)
	}
	return result
}

func newGeneratedDomainRetainedV1ColdFixture(t *testing.T) generatedDomainRetainedV1ColdFixture {
	t.Helper()
	fixture := newGeneratedDomainA0NamespaceFixture(t)
	generatedDomainA0AssertExactPreCallCensus(t, fixture)
	observation := generatedDomainRetainedV1Observation(t, fixture)
	generatedDomainRetainedV1InstallPhysical(t, &fixture, observation)
	generatedDomainA0RestoreDeploymentIdentityGuard(t, fixture.identity)
	return generatedDomainRetainedV1ColdFixture{generatedDomainA0NamespaceFixture: fixture, observation: observation}
}

func generatedDomainRetainedV1ColdAttestationFixture(t *testing.T) (generatedDomainRetainedV1ColdFixture,
	*generatedDomainConstruction, generatedDomainStartupObservation, generatedDomainGatewayExpectation,
) {
	t.Helper()
	fixture := newGeneratedDomainRetainedV1ColdFixture(t)
	result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
		fixture.construction, fixture.environment, fixture.identity.options)
	if err != nil || result == nil || result.manager == nil || result.candidates == nil {
		t.Fatalf("valid retained V1 attestation fixture = %#v, %v", result, err)
	}
	observed, err := inspectGeneratedDomainStartupObservation(context.Background(), fixture.identity.stores,
		fixture.construction, fixture.identity.options)
	if err != nil {
		t.Fatalf("inspect retained V1 attestation fixture: %v", err)
	}
	expected, err := resolveGeneratedDomainColdGatewayExpectation(observed.database, observed.protected)
	if err != nil {
		t.Fatalf("resolve retained V1 attestation fixture: %v", err)
	}
	return fixture, result, observed, expected
}

func TestGeneratedDomainColdRetainedGatewaySelectionTripleAndReadAttribution(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		reason generatedDomainConstructionReason
		run    func(*testing.T)
	}{
		{name: "present_unsupported_selection", reason: generatedDomainConstructionReason("cold_retained_gateway_selection"), run: func(t *testing.T) {
			fixture := newGeneratedDomainRetainedV1RebindFixture(t)
			database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := resolveGeneratedDomainColdGatewayExpectation(database, protected)
			if err != nil {
				t.Fatal(err)
			}
			before := len(fixture.runner.snapshot())
			proof, err := attestGeneratedDomainColdRetainedGateway(context.Background(), nil, nil,
				generatedDomainDockerStartupSnapshot{objects: fixture.runner.delegate.objects}, expected)
			assertGeneratedDomainConstructionDiagnostic(t, err, generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReason("cold_retained_gateway_selection"),
				"generated domain construction was refused: stage=cold_construction reason=cold_retained_gateway_selection")
			if !reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
				t.Fatalf("unsupported retained selection populated proof: %#v", proof)
			}
			commands := fixture.runner.snapshot()[before:]
			for _, command := range commands {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
					t.Fatalf("unsupported retained selection dispatched effect/capacity probe: %#v", command)
				}
			}
			generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
		}},
		{name: "incomplete_triple", reason: generatedDomainConstructionReason("cold_retained_gateway_triple"), run: func(t *testing.T) {
			fixture, result, observed, expected := generatedDomainRetainedV1ColdAttestationFixture(t)
			observed.docker.objects = generatedDomainRetainedV1RemoveCensusObject(t, observed.docker.objects,
				"volume", caddyVolumeName)
			before := len(fixture.runner.snapshot())
			proof, err := attestGeneratedDomainColdRetainedGateway(context.Background(), result.manager,
				result.candidates, observed.docker, expected)
			assertGeneratedDomainConstructionDiagnostic(t, err, generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReason("cold_retained_gateway_triple"),
				"generated domain construction was refused: stage=cold_construction reason=cold_retained_gateway_triple")
			if !reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
				t.Fatalf("incomplete retained triple populated proof: %#v", proof)
			}
			commands := fixture.runner.snapshot()[before:]
			for _, command := range commands {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
					t.Fatalf("incomplete retained triple dispatched effect/capacity probe: %#v", command)
				}
			}
			generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
		}},
		{name: "pinned_image_read", reason: generatedDomainConstructionReason("cold_retained_gateway_read"), run: func(t *testing.T) {
			fixture, result, observed, expected := generatedDomainRetainedV1ColdAttestationFixture(t)
			fixture.runner.delegate.gateway.image.ID = "sha256:" + strings.Repeat("f", 64)
			before := len(fixture.runner.snapshot())
			proof, err := attestGeneratedDomainColdRetainedGateway(context.Background(), result.manager,
				result.candidates, observed.docker, expected)
			assertGeneratedDomainConstructionDiagnostic(t, err, generatedDomainConstructionStageColdConstruction,
				generatedDomainConstructionReason("cold_retained_gateway_read"),
				"generated domain construction was refused: stage=cold_construction reason=cold_retained_gateway_read")
			if !reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
				t.Fatalf("retained image read refusal populated proof: %#v", proof)
			}
			commands := fixture.runner.snapshot()[before:]
			for _, command := range commands {
				if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
					t.Fatalf("retained image read refusal dispatched effect/capacity probe: %#v", command)
				}
			}
			generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) { test.run(t) })
		})
	}
}

func TestGeneratedDomainColdRetainedGatewayPolicyAndDigestAttribution(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		reason generatedDomainConstructionReason
		mutate func(*gatewayContainerInspection)
		check  func(*testing.T, generatedDomainColdRetainedGatewayAuthority, gatewayV2DockerObservation)
	}{
		{name: "policy_with_matching_digest", reason: generatedDomainConstructionReason("cold_retained_gateway_policy"),
			mutate: func(container *gatewayContainerInspection) { container.Running = true },
			check: func(t *testing.T, selected generatedDomainColdRetainedGatewayAuthority, observation gatewayV2DockerObservation) {
				digest, err := gatewayV1ObservedIdentityDigest(observation)
				if err != nil || digest != selected.journal.Source.IdentityDigest {
					t.Fatalf("policy-only observed digest = %q, %v; want protected digest", digest, err)
				}
			}},
		{name: "protected_digest_only", reason: generatedDomainConstructionReason("cold_retained_gateway_protected_digest"),
			mutate: func(container *gatewayContainerInspection) { container.RestartCount++ },
			check: func(t *testing.T, selected generatedDomainColdRetainedGatewayAuthority, observation gatewayV2DockerObservation) {
				digest, err := gatewayV1ObservedIdentityDigest(observation)
				copied := selected.journal
				copied.Source.IdentityDigest = digest
				if err != nil || digest == selected.journal.Source.IdentityDigest ||
					!validStoppedGatewayV1Base(selected.source, copied, observation, false) {
					t.Fatalf("digest-only fixture did not preserve every non-digest policy predicate: %v", err)
				}
			}},
		{name: "policy_precedes_digest", reason: generatedDomainConstructionReason("cold_retained_gateway_policy"),
			mutate: func(container *gatewayContainerInspection) { container.Running = true; container.RestartCount++ },
			check: func(t *testing.T, selected generatedDomainColdRetainedGatewayAuthority, observation gatewayV2DockerObservation) {
				digest, err := gatewayV1ObservedIdentityDigest(observation)
				copied := selected.journal
				copied.Source.IdentityDigest = digest
				if err != nil || digest == selected.journal.Source.IdentityDigest ||
					validStoppedGatewayV1Base(selected.source, copied, observation, false) {
					t.Fatalf("simultaneous-failure fixture did not independently fail policy and digest: %v", err)
				}
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, observed, expected := generatedDomainRetainedV1ColdAttestationFixture(t)
				selected, ok := generatedDomainColdRetainedGatewaySelectedAuthority(expected)
				if !ok {
					t.Fatal("retained V1 policy fixture lacks selected authority")
				}
				container := fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID]
				test.mutate(&container)
				fixture.runner.delegate.gateway.containers[fixture.observation.V1Container.ID] = container
				physical := fixture.observation
				physical.V1Container = container.caddyInspection
				physical.V1Runtime = container.gatewayContainerRuntime
				test.check(t, selected, physical)
				before := len(fixture.runner.snapshot())
				proof, err := attestGeneratedDomainColdRetainedGateway(context.Background(), result.manager,
					result.candidates, observed.docker, expected)
				assertGeneratedDomainConstructionDiagnostic(t, err, generatedDomainConstructionStageColdConstruction,
					test.reason, "generated domain construction was refused: stage=cold_construction reason="+string(test.reason))
				if !reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
					t.Fatalf("retained policy/digest refusal populated proof: %#v", proof)
				}
				commands := fixture.runner.snapshot()[before:]
				for _, command := range commands {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("retained policy/digest refusal dispatched effect/capacity probe: %#v", command)
					}
				}
				generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
			})
		})
	}
}

func TestGeneratedDomainColdRetainedGatewayFinalConsistencyAttribution(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		reason generatedDomainConstructionReason
		mutate func(*testing.T, []generatedDomainDockerObject)
	}{
		{name: "identity", reason: generatedDomainConstructionReasonColdRetainedGatewayFinalIdentity,
			mutate: func(t *testing.T, objects []generatedDomainDockerObject) {
				generatedDomainRetainedV1CensusObject(t, objects, "volume", caddyVolumeName).id = strings.Repeat("f", 64)
			}},
		{name: "labels", reason: generatedDomainConstructionReasonColdRetainedGatewayFinalLabels,
			mutate: func(t *testing.T, objects []generatedDomainDockerObject) {
				container := generatedDomainRetainedV1CensusObject(t, objects, "container", caddyContainerName)
				container.labels = cloneStringMap(container.labels)
				container.labels["io.rig.unexpected-retained-final"] = "v1"
			}},
		{name: "projection", reason: generatedDomainConstructionReasonColdRetainedGatewayFinalProjection,
			mutate: func(t *testing.T, objects []generatedDomainDockerObject) {
				generatedDomainRetainedV1CensusObject(t, objects, "volume", caddyVolumeName).data.mountpoint += "-crossed"
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture, result, observed, expected := generatedDomainRetainedV1ColdAttestationFixture(t)
				test.mutate(t, observed.docker.objects)
				generatedDomainRetainedV1AssertFinalConsistencyRefusal(t, fixture, result, observed, expected, test.reason)
			})
		})
	}
}

func TestGeneratedDomainColdRetainedGatewayFinalConsistencyFallback(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	t.Run("multiple_families", func(t *testing.T) {
		generatedDomainConstructionCase(t, func(t *testing.T) {
			fixture, result, observed, expected := generatedDomainRetainedV1ColdAttestationFixture(t)
			volume := generatedDomainRetainedV1CensusObject(t, observed.docker.objects, "volume", caddyVolumeName)
			volume.id = strings.Repeat("f", 64)
			volume.data.mountpoint += "-crossed"
			generatedDomainRetainedV1AssertFinalConsistencyRefusal(t, fixture, result, observed, expected,
				generatedDomainConstructionReasonColdRetainedGatewayFinal)
		})
	})
	t.Run("unconstrained_projection_preserves_label_attribution", func(t *testing.T) {
		descriptors := generatedDomainNativeV1DockerDescriptors()
		containerID := strings.Repeat("a", 64)
		networkID := strings.Repeat("b", 64)
		objects := make([]generatedDomainDockerObject, len(descriptors))
		for index, descriptor := range descriptors {
			objects[index] = generatedDomainDockerObject{kind: descriptor.kind, id: descriptor.id,
				name: descriptor.name, labels: cloneStringMap(descriptor.labels), data: descriptor.data}
			objects[index].data.options = cloneStringMap(descriptor.data.options)
		}
		objects[0].id = containerID
		objects[1].id = networkID
		objects[0].labels["io.rig.unexpected-retained-final"] = "v1"
		objects[2].data.mountpoint = "/var/lib/docker/volumes/retained-v1/_data"
		objects[2].data.createdAt = "2026-10-26T00:00:00Z"
		observation := gatewayV2DockerObservation{V1Container: caddyInspection{ID: containerID}, V1NetworkID: networkID}
		if generatedDomainColdRetainedGatewayCensusMatches(objects, observation) {
			t.Fatal("label-refusal fixture unexpectedly satisfies the production matcher")
		}
		if got := generatedDomainColdRetainedGatewayFinalReason(objects, observation); got != generatedDomainConstructionReasonColdRetainedGatewayFinalLabels {
			t.Fatalf("unconstrained projection classifier = %q, want %q", got,
				generatedDomainConstructionReasonColdRetainedGatewayFinalLabels)
		}
	})
}

func generatedDomainRetainedV1AssertFinalConsistencyRefusal(t *testing.T,
	fixture generatedDomainRetainedV1ColdFixture, result *generatedDomainConstruction,
	observed generatedDomainStartupObservation, expected generatedDomainGatewayExpectation,
	reason generatedDomainConstructionReason,
) {
	t.Helper()
	objects, presence, err := generatedDomainColdRetainedGatewayObjects(observed.docker)
	if err != nil || presence != generatedDomainColdRetainedGatewayPresent || len(objects) != 3 {
		t.Fatalf("retained final census objects = %#v presence=%d error=%v", objects, presence, err)
	}
	if generatedDomainColdRetainedGatewayCensusMatches(objects, fixture.observation) {
		t.Fatal("retained final refusal fixture still matches the production census predicate")
	}
	if got := generatedDomainColdRetainedGatewayFinalReason(objects, fixture.observation); got != reason {
		t.Fatalf("retained final refusal reason = %q, want %q", got, reason)
	}
	before := len(fixture.runner.snapshot())
	proof, err := attestGeneratedDomainColdRetainedGateway(context.Background(), result.manager,
		result.candidates, observed.docker, expected)
	message := "generated domain construction was refused: stage=cold_construction reason=" + string(reason)
	assertGeneratedDomainConstructionDiagnostic(t, err, generatedDomainConstructionStageColdConstruction, reason, message)
	if !reflect.DeepEqual(proof, generatedDomainColdRetainedGatewayProof{}) {
		t.Fatalf("retained final consistency refusal populated proof: %#v", proof)
	}
	commands := fixture.runner.snapshot()[before:]
	for _, command := range commands {
		if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
			t.Fatalf("retained final consistency refusal dispatched effect/capacity probe: %#v", command)
		}
	}
	generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
	generatedDomainRetainedV1AssertFinalReadPrefix(t, commands, fixture.observation, expected)
}

func generatedDomainRetainedV1AssertFinalReadPrefix(t *testing.T, commands [][]string,
	observation gatewayV2DockerObservation, expected generatedDomainGatewayExpectation,
) {
	t.Helper()
	selected, ok := generatedDomainColdRetainedGatewaySelectedAuthority(expected)
	if !ok {
		t.Fatal("retained final read prefix lacks selected authority")
	}
	want := [][]string{
		{"image", "inspect", "--format", imageInspectFormat, selected.journal.Resources.ImageID},
		{"container", "inspect", "--format", generatedDomainStartupContainerInspectFormat, observation.V1Container.ID},
		{"volume", "inspect", "--format", generatedDomainVolumeInspectFormat, caddyVolumeName},
		{"network", "inspect", "--format", generatedDomainStartupNetworkInspectFormat, observation.V1NetworkID},
		{"container", "cp", observation.V1Container.ID + ":/config/" + gatewayV2ActiveConfigFile, "-"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("retained final refusal read prefix = %#v, want %#v", commands, want)
	}
}

func generatedDomainRetainedV1Observation(t *testing.T,
	fixture generatedDomainA0NamespaceFixture,
) gatewayV2DockerObservation {
	t.Helper()
	database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal("read retained V1 database authority")
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal("read retained V1 protected authority")
	}
	expected, err := resolveGeneratedDomainColdGatewayExpectation(database, protected)
	if err != nil || expected.kind != generatedDomainGatewayNativeV2 || expected.nativeV1 == nil ||
		expected.nativeV2 == nil || expected.journal == nil || expected.upgrade == nil {
		t.Fatalf("retained V1 canonical gateway authority = %#v, %v", expected, err)
	}
	source := cloneRouteState(*expected.nativeV1)
	state := cloneGatewayV2RouteState(*expected.nativeV2)
	journal := *expected.journal
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	generatedDomainAdaptNativeV2Observation(state, journal, &observation)
	generatedDomainRetainedV1CanonicalizeDockerIDs(&observation)
	if !validStoppedGatewayV1Base(source, journal, observation, false) {
		t.Fatal("retained V1 fixture does not satisfy the production stopped-gateway proof")
	}
	return observation
}

func generatedDomainRetainedV1InstallPhysical(t *testing.T, fixture *generatedDomainA0NamespaceFixture,
	observation gatewayV2DockerObservation,
) {
	t.Helper()
	if fixture == nil || fixture.runner == nil || fixture.runner.delegate == nil {
		t.Fatal("retained V1 physical fixture is incomplete")
	}
	objects := generatedDomainRetainedV1CensusObjects(t, observation)
	fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, objects...)
	generatedDomainRetainedV1ConfigureRunner(t, fixture.runner.delegate.gateway, observation)
}

func generatedDomainRetainedV1InstallCurrentV2(t *testing.T, fixture *generatedDomainRetainedV1ColdFixture) {
	t.Helper()
	if fixture == nil || fixture.runner == nil || fixture.runner.delegate == nil ||
		fixture.runner.delegate.gateway == nil {
		t.Fatal("retained V1 current V2 fixture is incomplete")
	}
	database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := resolveGeneratedDomainColdGatewayExpectation(database, protected)
	if err != nil || expected.kind != generatedDomainGatewayNativeV2 || expected.nativeV1 == nil ||
		expected.nativeV2 == nil || expected.upgrade == nil {
		t.Fatalf("retained V1 native authority before current V2 = %#v, %v", expected, err)
	}
	lineage, err := gatewayUpgradeCurrentLineage(*expected.upgrade)
	if err != nil {
		t.Fatal(err)
	}
	selection := gatewayCurrentSelection{Kind: gatewayCurrentSelectionUpgrade, Lineage: lineage,
		Upgrade: expected.upgrade, UpgradeSource: expected.nativeV1}
	authority := gatewayCurrentV2PromotionAuthority{Snapshot: database.Gateway}
	if !gatewayCurrentV2PromotionSnapshotMatches(selection, authority.Snapshot) {
		t.Fatal("retained V1 current V2 promotion authority does not match the exact A0 runtime census")
	}
	current, err := gatewayCurrentV2BaselineFor(selection, authority)
	if err != nil {
		t.Fatal(err)
	}
	gatewayCurrentInstallV2TestState(t, fixture.identity.options.DataRoot, current)
	wanted, err := gatewayCurrentPhysicalConfigBytesV2(current)
	token, tokenErr := gatewayCurrentPhysicalProbeTokenV2(current)
	if err != nil || tokenErr != nil || !validSHA256(token) {
		t.Fatalf("retained V1 current V2 config: config=%v token=%v", err, tokenErr)
	}
	containerID := fixture.observation.FinalContainer.ID
	restart, ok := fixture.runner.delegate.gateway.restarts[containerID]
	if !ok {
		t.Fatal("retained V1 current gateway restart fixture is absent")
	}
	restart.name, restart.body = expected.nativeV2.Identity.ActiveConfigFilename, append([]byte(nil), wanted...)
	fixture.runner.delegate.gateway.restarts[containerID] = restart
	fixture.runner.delegate.gateway.live = append([]byte(nil), wanted...)
	fixture.runner.delegate.gateway.challenge = gatewayV2ChallengeBodyPrefix +
		gatewayV2PortChallenge(token, current.Profile.PortStart)
	generatedDomainA0RestoreDeploymentIdentityGuard(t, fixture.identity)
	database, err = fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	protected, err = readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := resolveGeneratedDomainColdGatewayExpectation(database, protected)
	if err != nil || !generatedDomainCurrentV2UpgradeExpectationValid(selected) || selected.currentV2 == nil ||
		selected.currentV2.Digest != current.Digest {
		t.Fatalf("retained V1 current V2 authority = %#v, %v", selected, err)
	}
}

func generatedDomainRetainedV1CanonicalizeDockerIDs(observation *gatewayV2DockerObservation) {
	observation.V1Container.ID = normalizeID(observation.V1Container.ID)
	observation.V1NetworkID = normalizeID(observation.V1NetworkID)
	for name, configured := range observation.V1Runtime.ConfiguredNetworks {
		configured.NetworkID = normalizeID(configured.NetworkID)
		observation.V1Runtime.ConfiguredNetworks[name] = configured
	}
	for name, id := range observation.V1ApplicationNetworkIDs {
		observation.V1ApplicationNetworkIDs[name] = normalizeID(id)
	}
}

func generatedDomainRetainedV1CensusObjects(t *testing.T,
	observation gatewayV2DockerObservation,
) []generatedDomainDockerObject {
	t.Helper()
	descriptors := generatedDomainNativeV1DockerDescriptors()
	if len(descriptors) != 3 {
		t.Fatalf("retained V1 descriptor count = %d, want 3", len(descriptors))
	}
	for index := range descriptors {
		descriptor := &descriptors[index]
		switch descriptor.kind {
		case "container":
			descriptor.id = observation.V1Container.ID
		case "network":
			descriptor.id = observation.V1NetworkID
		case "volume":
			descriptor.data.mountpoint = observation.V1VolumeIdentity.Mountpoint
			descriptor.data.createdAt = observation.V1VolumeIdentity.CreatedAt
		default:
			t.Fatalf("unexpected retained V1 descriptor kind %q", descriptor.kind)
		}
	}
	return generatedDomainDockerObjectsForDescriptors(descriptors)
}

func generatedDomainRetainedV1ConfigureRunner(t *testing.T, runner *generatedDomainGatewayAttestationRunner,
	observation gatewayV2DockerObservation,
) {
	t.Helper()
	if runner == nil || runner.container.ID == "" || runner.network.ID == "" ||
		runner.volumes == nil || runner.container.ID == observation.V1Container.ID ||
		runner.network.ID == observation.V1NetworkID ||
		normalizeID(runner.image.ID) != normalizeID(observation.Image.ID) {
		t.Fatal("retained V1 detailed runner is incomplete or crossed")
	}
	retainedContainer := gatewayContainerInspection{caddyInspection: observation.V1Container,
		gatewayContainerRuntime: observation.V1Runtime}
	retainedNetwork := gatewayNetworkIdentityInspection{ID: observation.V1NetworkID,
		Name: observation.V1Network.Name, Driver: observation.V1Network.Driver,
		Scope: observation.V1Network.Scope, Internal: observation.V1Network.Internal,
		Options: observation.V1Network.Options, IPAM: observation.V1Network.IPAM,
		Labels: observation.V1Network.Labels, Containers: observation.V1Network.Containers}
	runner.containers = map[string]gatewayContainerInspection{
		runner.container.ID:        runner.container,
		observation.V1Container.ID: retainedContainer,
	}
	runner.networks = map[string]gatewayNetworkIdentityInspection{
		runner.network.ID:       runner.network,
		observation.V1NetworkID: retainedNetwork,
	}
	runner.volumes[observation.V1Volume.Name] = generatedDomainGatewayVolumeIdentity(
		observation.V1Volume, observation.V1VolumeIdentity)
	runner.restarts = map[string]generatedDomainRestartFixture{
		runner.container.ID: {name: runner.restartName, body: append([]byte(nil), runner.restart...)},
		observation.V1Container.ID: {name: gatewayV2ActiveConfigFile,
			body: append([]byte(nil), observation.V1RestartConfig...)},
	}
}
