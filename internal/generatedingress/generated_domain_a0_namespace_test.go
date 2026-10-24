//go:build linux || windows

package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedbuilder"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/secretfile"
)

type generatedDomainA0AppFixture struct {
	migration   *generatedMigrationFixture
	candidate   *candidateOwnerFixture
	reservation GeneratedReplacementReservation
	execution   *GeneratedCandidateExecution
	identities  []CandidateIdentity
	physical    []candidateOwnerPhysical
}

type generatedDomainA0NamespaceFixture struct {
	identity     generatedDomainIdentityFixture
	apps         []generatedDomainA0AppFixture
	runner       *generatedDomainConstructionRunner
	construction runtimeprocess.CommandRunner
	builder      *generatedDomainTwoComponentBuilderFixture
	store        *stateStore
	environment  *candidateFactoryEnvironment
}

type generatedDomainA0ProtectionSnapshot struct {
	database  gatewayRebindFixtureDatabaseSnapshot
	history   gatewayHistorySnapshot
	protected generatedDomainProtectedStartupSnapshot
	body      []byte
	file      *securetemp.GeneratedBuilderFileSnapshot
}

type generatedDomainA0RouteSnapshot struct {
	database gatewayRebindFixtureDatabaseSnapshot
	body     []byte
	info     os.FileInfo
}

func TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccounting(t *testing.T) {
	generatedDomainA0CommittedV2NamespacePartition(t, 0, 3)
}

func TestGeneratedDomainA0CommittedV2NamespaceRequiresColdAccountingPart2(t *testing.T) {
	generatedDomainA0CommittedV2NamespacePartition(t, 3, 6)
}

func generatedDomainA0CommittedV2NamespacePartition(t *testing.T, start, end int) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	cases := []struct {
		name   string
		mutate func(*generatedDomainA0NamespaceFixture)
		pass   bool
	}{
		{name: "exact_two_app_v2_and_builder_namespace", pass: true},
		{name: "missing_second_app_frontend", mutate: func(fixture *generatedDomainA0NamespaceFixture) {
			fixture.runner.delegate.objects = removeGeneratedDomainA0Object(fixture.runner.delegate.objects,
				"container", fixture.apps[1].migration.guarded.appID, "frontend")
		}},
		{name: "crossed_second_app_frontend", mutate: func(fixture *generatedDomainA0NamespaceFixture) {
			object := generatedDomainA0Object(t, fixture.runner.delegate.objects,
				"container", fixture.apps[1].migration.guarded.appID, "frontend")
			object.labels["io.rig.application"] = fixture.apps[0].migration.guarded.appID
		}},
		{name: "extra_selected_resource", mutate: func(fixture *generatedDomainA0NamespaceFixture) {
			fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
				kind: "volume", name: "ordinary-a0-volume", labels: map[string]string{"rig.a0": "extra"}})
		}},
		{name: "unowned_prefixed_application_resource", mutate: func(fixture *generatedDomainA0NamespaceFixture) {
			fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
				kind: "container", id: strings.Repeat("1", 64), name: "rig-a-unowned-a0-api"})
		}},
		{name: "crossed_persisted_builder_resource", mutate: func(fixture *generatedDomainA0NamespaceFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "container" && object.name == generatedDomainTwoComponentBuilderContainerName(fixture.builder) {
					object.labels["rig.builder"] = "rig-buildkit-bbbbbbbbbbbbbbbbbbbbbbbb"
					return
				}
			}
			t.Fatal("persisted builder container is absent from the A0 namespace")
		}},
	}
	for _, test := range cases[start:end] {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainA0NamespaceFixture(t)
				generatedDomainA0AssertExactPreCallCensus(t, fixture)
				if test.mutate != nil {
					test.mutate(&fixture)
				}
				before := generatedDomainA0ReadProtection(t, fixture)
				domain, err := NewGeneratedDomain(context.Background(), fixture.identity.stores,
					fixture.construction, fixture.environment, generatedColdBoundaryPublicOptions(fixture.identity.options))
				if test.pass {
					if err != nil || domain == nil || domain.construction == nil || domain.construction.cold == nil ||
						len(domain.construction.cold.groups) != 2 {
						t.Fatalf("A0 two-app cold domain = %#v, %v", domain, err)
					}
				} else if domain != nil || err == nil || !errors.Is(err, errGeneratedDomainConstructionRefused) {
					t.Fatalf("invalid A0 namespace = %#v, %v", domain, err)
				}
				after := generatedDomainA0ReadProtection(t, fixture)
				if !generatedDomainA0SameProtection(before, after) {
					t.Fatal("A0 public construction changed SQL or protected files")
				}
				clear(before.body)
				clear(after.body)
				commands := fixture.runner.snapshot()
				for _, command := range commands {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("A0 reconstruction dispatched an effect or capacity probe: %#v", command)
					}
				}
				if !test.pass {
					generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
				}
			})
		})
	}
}

func TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExact(t *testing.T) {
	generatedDomainA0GatewayOnlyNamespacePartition(t, 0, 3)
}

func TestGeneratedDomainA0GatewayOnlyNamespaceRemainsExactPart2(t *testing.T) {
	generatedDomainA0GatewayOnlyNamespacePartition(t, 3, 5)
}

func generatedDomainA0GatewayOnlyNamespacePartition(t *testing.T, start, end int) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	cases := []struct {
		name            string
		mutate          func(*generatedDomainConstructionFixture)
		pass            bool
		privateSentinel bool
		publicReason    generatedDomainConstructionReason
	}{
		{name: "exact_gateway_only_namespace", pass: true},
		{name: "missing_gateway_object", publicReason: generatedDomainConstructionReasonDockerAttestation,
			mutate: func(fixture *generatedDomainConstructionFixture) {
				fixture.runner.delegate.objects = fixture.runner.delegate.objects[1:]
			}},
		{name: "extra_gateway_object", publicReason: generatedDomainConstructionReasonDockerAttestation,
			mutate: func(fixture *generatedDomainConstructionFixture) {
				fixture.runner.delegate.objects = append(fixture.runner.delegate.objects, generatedDomainDockerObject{
					kind: "volume", name: "rig-a0-unexpected-gateway-volume",
					labels: map[string]string{"io.rig.unexpected": "a0-gateway"},
					data: generatedDomainDockerObjectData{driver: "local", scope: "local", options: map[string]string{},
						mountpoint: "/var/lib/docker/volumes/rig-a0-unexpected-gateway-volume/_data",
						createdAt:  "2026-10-24T00:00:00Z"}})
			}},
		{name: "crossed_gateway_expectation", privateSentinel: true, mutate: func(fixture *generatedDomainConstructionFixture) {
			store, err := newStateStore(fixture.identity.dataRoot)
			if err != nil {
				t.Fatal("open crossed A0 gateway state")
			}
			state, err := store.load()
			if err != nil {
				t.Fatal("load crossed A0 gateway state")
			}
			state.Active[uuid.NewString()] = routeRecord{Slot: generatedidentity.SlotBlue,
				Endpoints: []generatedroute.RouteEndpoint{{Component: "api", Role: "server",
					ContainerID: strings.Repeat("1", 64), NetworkName: "rig-a-crossed",
					NetworkAlias: "api", InternalPort: 3000}}}
			if err := store.save(state); err != nil {
				t.Fatal("install crossed A0 gateway expectation")
			}
		}},
		{name: "malformed_gateway_expectation", publicReason: generatedDomainConstructionReasonProtectedState,
			mutate: func(fixture *generatedDomainConstructionFixture) {
				path := filepath.Join(fixture.identity.dataRoot, "runtime", "generated-ingress", stateFilename)
				if err := secretfile.Write(path, statePurpose, []byte(`{"version":1,"active":{},"unknown":true}`)); err != nil {
					t.Fatal("install malformed A0 gateway expectation")
				}
			}},
	}
	for _, test := range cases[start:end] {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainStableConstructionFixture(t)
				if test.mutate != nil {
					test.mutate(&fixture)
				}
				before := generatedDomainA0ReadRouteProtection(t, fixture)
				domain, err := NewGeneratedDomain(context.Background(), fixture.identity.stores, fixture.runner,
					fixture.environment, generatedColdBoundaryPublicOptions(fixture.identity.options))
				if test.pass {
					if err != nil || domain == nil || domain.construction == nil ||
						domain.construction.cold != nil || domain.construction.manifest == nil {
						t.Fatalf("exact gateway-only domain = %#v, %v", domain, err)
					}
				} else if domain != nil || err == nil ||
					(test.privateSentinel && !errors.Is(err, errGeneratedDomainConstructionRefused)) {
					t.Fatalf("invalid gateway-only domain = %#v, %v", domain, err)
				}
				if test.publicReason != "" {
					assertGeneratedDomainConstructionDiagnostic(t, err,
						generatedDomainConstructionStageStartupObservation,
						test.publicReason,
						"generated domain construction was refused: stage=startup_observation reason="+
							string(test.publicReason))
				}
				after := generatedDomainA0ReadRouteProtection(t, fixture)
				if !generatedDomainA0SameRouteProtection(before, after) {
					t.Fatal("gateway-only construction changed SQL or protected route state")
				}
				clear(before.body)
				clear(after.body)
				commands := fixture.runner.snapshot()
				for _, command := range commands {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatal("gateway-only construction dispatched an effect or capacity probe")
					}
				}
				if !test.pass {
					generatedDomainA0AssertNoInteractiveGatewayCommands(t, commands)
				}
			})
		})
	}
}

func generatedDomainA0ReadProtection(t *testing.T,
	fixture generatedDomainA0NamespaceFixture,
) generatedDomainA0ProtectionSnapshot {
	t.Helper()
	databaseSnapshot, err := snapshotGatewayRebindFixtureDatabase(context.Background(), fixture.identity.database)
	if err != nil {
		t.Fatal("read A0 SQL protection snapshot")
	}
	history, err := readGatewayHistorySnapshotMode(fixture.store, true)
	if err != nil {
		t.Fatal("read A0 gateway protection snapshot")
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal("read A0 protected namespace snapshot")
	}
	body, file, err := fixture.builder.directory.ReadFileSnapshot(generatedbuilder.IdentityFilename, 8<<10)
	if err != nil {
		t.Fatal("read A0 builder protection snapshot")
	}
	return generatedDomainA0ProtectionSnapshot{database: databaseSnapshot, history: history,
		protected: protected, body: body, file: file}
}

func generatedDomainA0SameProtection(before, after generatedDomainA0ProtectionSnapshot) bool {
	return reflect.DeepEqual(before.database, after.database) && sameGatewayHistorySnapshot(before.history, after.history) &&
		sameGeneratedDomainProtectedStartup(before.protected, after.protected) &&
		bytes.Equal(before.body, after.body) && before.file.Same(after.file)
}

func generatedDomainA0ReadRouteProtection(t *testing.T,
	fixture generatedDomainConstructionFixture,
) generatedDomainA0RouteSnapshot {
	t.Helper()
	database, err := snapshotGatewayRebindFixtureDatabase(context.Background(), fixture.identity.database)
	if err != nil {
		t.Fatal("read gateway-only SQL protection snapshot")
	}
	path := filepath.Join(fixture.identity.dataRoot, "runtime", "generated-ingress", stateFilename)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read gateway-only route protection snapshot")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, info) {
		clear(body)
		t.Fatal("gateway-only route protection identity is invalid")
	}
	return generatedDomainA0RouteSnapshot{database: database, body: body, info: info}
}

func generatedDomainA0SameRouteProtection(before, after generatedDomainA0RouteSnapshot) bool {
	return reflect.DeepEqual(before.database, after.database) && bytes.Equal(before.body, after.body) &&
		before.info != nil && after.info != nil && os.SameFile(before.info, after.info) &&
		before.info.Mode() == after.info.Mode()
}

func generatedDomainA0AssertNoInteractiveGatewayCommands(t *testing.T, commands [][]string) {
	t.Helper()
	for _, command := range commands {
		if len(command) >= 3 && (generatedDomainGatewayAdminCommand(command, command[2]) ||
			generatedDomainGatewayChallengeCommand(command, command[2])) {
			t.Fatal("invalid A0 namespace dispatched gateway admin or challenge traffic")
		}
	}
}

func generatedDomainA0AssertExactPreCallCensus(t *testing.T, fixture generatedDomainA0NamespaceFixture) {
	t.Helper()
	if fixture.runner == nil || fixture.runner.delegate == nil || fixture.builder == nil || len(fixture.apps) != 2 {
		t.Fatal("A0 pre-call census fixture is incomplete")
	}
	database, err := fixture.identity.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal("read A0 pre-call database snapshot")
	}
	protected, err := readGeneratedDomainProtectedStartup(context.Background(), fixture.identity.options.DataRoot)
	if err != nil {
		t.Fatal("read A0 pre-call protected snapshot")
	}
	if recovery := resolveGeneratedDomainRecoveryDockerExpectation(database, protected); recovery.kind != generatedDomainRecoveryDockerNone || len(recovery.recoveries) != 0 {
		t.Fatal("A0 pre-call state unexpectedly requires gateway recovery")
	}
	gateway, err := resolveGeneratedDomainGatewayExpectation(database, protected)
	if err != nil {
		t.Fatal("resolve A0 pre-call gateway expectation")
	}
	if applicationRoutes, valid := generatedDomainGatewayApplicationRoutes(gateway); !valid || !applicationRoutes {
		t.Fatal("A0 pre-call gateway expectation is not a committed app-bearing form")
	}
	wantKinds := map[string]int{"container": 6, "network": 4, "volume": 2, "image": 5}
	gotKinds := make(map[string]int, len(wantKinds))
	seen := make(map[string]struct{}, 17)
	appCounts := make(map[string]map[string]int, 2)
	for _, app := range fixture.apps {
		appCounts[app.migration.guarded.appID] = map[string]int{}
	}
	builderObjects := 0
	unlabelledImages := 0
	for _, object := range fixture.runner.delegate.objects {
		gotKinds[object.kind]++
		identity := object.id
		if object.kind == "volume" {
			identity = object.name
		}
		if identity == "" {
			t.Fatal("A0 pre-call census contains an empty object identity")
		}
		key := object.kind + "\x00" + identity
		if _, duplicate := seen[key]; duplicate {
			t.Fatal("A0 pre-call census contains a duplicate object identity")
		}
		seen[key] = struct{}{}
		if appID := object.labels["io.rig.application"]; appID != "" {
			counts, expected := appCounts[appID]
			if expected && (object.kind == "container" || object.kind == "network" || object.kind == "image") {
				counts[object.kind]++
			}
		}
		if object.name == generatedDomainTwoComponentBuilderContainerName(fixture.builder) ||
			object.name == fixture.builder.identity.NetworkName {
			builderObjects++
		}
		if object.kind == "image" && len(object.labels) == 0 {
			unlabelledImages++
		}
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) || len(seen) != 17 || builderObjects != 2 || unlabelledImages != 1 {
		t.Fatal("A0 pre-call census does not contain the exact gateway, application, and builder object set")
	}
	for _, counts := range appCounts {
		if counts["container"] != 2 || counts["network"] != 1 || counts["image"] != 2 {
			t.Fatal("A0 pre-call census does not contain an exact two-component application set")
		}
	}
}

func newGeneratedDomainA0NamespaceFixture(t *testing.T) generatedDomainA0NamespaceFixture {
	t.Helper()
	base := newGeneratedDomainTwoComponentColdFixture(t, true)
	identity := base.identity
	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	firstPhysical := append([]candidateOwnerPhysical(nil), base.runner.roster.(*generatedDomainCandidateRosterRunner).physical...)
	first := generatedDomainA0AppFixture{migration: base.migration, physical: firstPhysical}
	second := newGeneratedDomainA0SecondApp(t, identity, base.migration)
	identity.stores = second.migration.repository.stores
	generatedDomainA0RestoreDeploymentIdentityGuard(t, identity)

	_, _, ingressIP := ingressNetworkCandidate(0)
	ingress := &ingressRunner{caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
		GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
		ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
			Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
	endpoints := make(map[string]generatedRouteEndpointSnapshot)
	for appID, record := range source.Active {
		for _, endpoint := range record.Endpoints {
			endpoints[endpoint.ContainerID] = generatedRouteEndpointSnapshot{appID: appID, endpoint: endpoint, slot: record.Slot}
		}
	}
	for _, candidate := range second.identities {
		endpoint := generatedRouteEndpoint(candidate)
		endpoints[candidate.ContainerID] = generatedRouteEndpointSnapshot{appID: second.migration.guarded.appID,
			endpoint: endpoint, slot: generatedidentity.Slot(candidate.Slot)}
	}
	routeRunner := &generatedDomainTwoComponentRouteRunner{daemonID: "generated-domain-a0-namespace",
		delegate: &generatedRoutePredecessorRunner{ingressRunner: ingress, endpoints: endpoints}}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database,
		stores: identity.stores, runner: routeRunner, options: identity.options}, store)
	if err != nil {
		t.Fatal(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores, routeRunner, identity.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		t.Fatal(err)
	}
	routes, err := newGeneratedRouteAuthority(manager, second.candidate.authority, second.candidate.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	request := generatedroute.RouteSwitchRequest{AppID: second.migration.guarded.appID,
		ToSlot: generatedidentity.Slot(second.identities[0].Slot), Endpoints: []generatedroute.RouteEndpoint{
			generatedRouteEndpoint(second.identities[0]), generatedRouteEndpoint(second.identities[1])}}
	sealed, err := routes.Pin(second.migration.guarded.context, second.execution, request)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := sealed.Activate(second.migration.guarded.context)
	if err != nil || activated.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activate second A0 route = %#v, %v", activated, err)
	}
	if _, err := generatedruntimestate.New(identity.database).Advance(context.Background(),
		second.migration.guarded.appID, second.migration.request.DeploymentID,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := second.migration.repository.Transition(context.Background(), second.migration.guarded.appID,
		second.migration.request.DeploymentID, Succeeded, ""); err != nil {
		t.Fatal(err)
	}
	second.execution.Leave()
	second.reservation.Release()
	if err := second.migration.releaseEffects(); err != nil {
		t.Fatal(err)
	}

	source, err = store.load()
	if err != nil || len(source.Active) != 2 {
		t.Fatalf("load two-app A0 route state = %#v, %v", source, err)
	}
	state, journal := installGeneratedDomainA0CommittedV2(t, identity, manager, source,
		[]*generatedMigrationFixture{first.migration, second.migration})
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactFinalV2)
	generatedDomainAdaptNativeV2Observation(state, journal, &observation)
	gateway := generatedDomainNativeV2OuterGatewayRunner(t, state, journal, observation)
	objects := generatedDomainA0GatewayObjects(state, journal)
	physicalRoster := append(first.physical, second.physical...)
	objects = append(objects, generatedDomainA0ApplicationObjects(t, physicalRoster)...)
	if base.builder == nil {
		t.Fatal("A0 namespace requires the persisted generated builder")
	}
	builderObjects := generatedDomainA0BuilderObjects(t, base)
	objects = append(objects, builderObjects...)
	identity.options.HostPort = journal.Source.LocalHostPort
	runner := &generatedDomainConstructionRunner{roster: &generatedDomainCandidateRosterRunner{physical: physicalRoster},
		delegate: &generatedDomainRecoveryCensusRunner{daemonID: "generated-domain-a0-namespace",
			objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	base.builder.runner = &generatedDomainTwoComponentRouteRunner{delegate: runner, builder: &base.builder.physical}
	return generatedDomainA0NamespaceFixture{identity: identity, apps: []generatedDomainA0AppFixture{first, second},
		runner: runner, construction: base.builder.runner, builder: base.builder,
		store: store, environment: &candidateFactoryEnvironment{path: t.TempDir()}}
}

func newGeneratedDomainA0SecondApp(t *testing.T, identity generatedDomainIdentityFixture,
	first *generatedMigrationFixture,
) generatedDomainA0AppFixture {
	t.Helper()
	if first == nil || first.repository == nil || first.repository.Repository == nil ||
		first.repository.stores == nil || first.jobService == nil || first.bindings == nil || first.plans == nil ||
		first.configuration == nil {
		t.Fatal("first A0 migration dependencies are incomplete")
	}
	migration := newGeneratedMigrationFixtureWithDependenciesAndImages(t, identity.database, first.jobService,
		first.bindings, first.repository.Repository, first.repository.stores, first.plans, first.configuration,
		identity.options.WorkingDirectory, []string{"api", "frontend"}, []string{"api", "frontend"}, map[string]string{
			"api": "sha256:" + strings.Repeat("c", 64), "frontend": "sha256:" + strings.Repeat("d", 64),
		}, generatedDomainTwoComponentPlan())
	generatedDomainA0RestoreDeploymentIdentityGuard(t, identity)
	validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
	options := generatedDomainCandidateOptions(identity.options)
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: 2 * uint64(options.MemoryBytes), DiskAvailableBytes: 2 * options.ReplacementDiskBytes}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	base := &candidateOwnerRunner{absenceErr: generatedMigrationCompletedExitOne(t),
		before:  make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results: make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
	candidateRunner := &generatedRouteCandidateRunner{delegate: base, physical: make(map[string]candidateOwnerPhysical)}
	environment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := migration.repository.GeneratedCandidateAuthority(candidateRunner, environment, budget, options)
	if err != nil {
		t.Fatal(err)
	}
	validation.authority = authority
	candidate := &candidateOwnerFixture{validation: validation, authority: authority, runner: base,
		environment: environment, source: source, budget: budget, options: options}
	reservation := candidate.reserve(t)
	execution := candidate.pin(t, reservation)
	names := []string{"api", "frontend"}
	containerIDs := []string{strings.Repeat("9", 64), strings.Repeat("a", 64)}
	invocations := make([]GeneratedCandidateInvocation, 0, len(names))
	identities := make([]CandidateIdentity, 0, len(names))
	for index, name := range names {
		invocation, err := execution.BeginCreate(name)
		if err != nil {
			t.Fatal(err)
		}
		permit, ok := invocation.(*generatedCandidateInvocationPermit)
		if !ok || permit == nil {
			t.Fatalf("second A0 invocation type = %T", invocation)
		}
		base.mu.Lock()
		base.containerID = containerIDs[index]
		base.physical = candidateOwnerPhysical{policy: permit.policy}
		base.mu.Unlock()
		created, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatalf("create second A0 %s candidate: %v", name, err)
		}
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatal(err)
		}
		invocations = append(invocations, invocation)
		identities = append(identities, created)
	}
	state, err := execution.FinishStarting()
	if err != nil || state.Phase != generatedruntimestate.PhaseWaitingHealth {
		t.Fatalf("finish second A0 starting = %#v, %v", state, err)
	}
	for _, invocation := range invocations {
		if err := execution.WaitHealthyAndRecord(invocation); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	physical := make([]candidateOwnerPhysical, 0, len(candidateRunner.physical))
	for _, value := range candidateRunner.physical {
		physical = append(physical, value)
	}
	sort.Slice(physical, func(i, j int) bool {
		return physical[i].policy.identity.Component < physical[j].policy.identity.Component
	})
	return generatedDomainA0AppFixture{migration: migration, candidate: candidate, reservation: reservation,
		execution: execution, identities: identities, physical: physical}
}

const generatedDomainA0DeploymentIdentityGuardSQL = `CREATE TRIGGER deployment_identity_immutable BEFORE UPDATE OF app_id,release_id,job_id,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number,provenance_initialized ON deployments
WHEN NEW.app_id IS NOT OLD.app_id OR NEW.job_id IS NOT OLD.job_id
  OR NEW.configuration_mode IS NOT OLD.configuration_mode
  OR (NEW.release_id IS NOT OLD.release_id AND NOT (OLD.status='preparing' AND OLD.provenance_initialized=0 AND OLD.release_id IS NULL AND NEW.release_id IS NOT NULL))
  OR ((NEW.actual_configuration_revision_id IS NOT OLD.actual_configuration_revision_id OR NEW.actual_configuration_revision_number IS NOT OLD.actual_configuration_revision_number)
      AND NOT (OLD.status='preparing' AND OLD.provenance_initialized=0 AND OLD.actual_configuration_revision_id IS NULL AND OLD.actual_configuration_revision_number=0))
  OR (NEW.provenance_initialized IS NOT OLD.provenance_initialized AND NOT (OLD.status='preparing' AND OLD.provenance_initialized=0 AND NEW.provenance_initialized=1))
BEGIN SELECT RAISE(ABORT, 'deployment identity is immutable'); END`

func generatedDomainA0RestoreDeploymentIdentityGuard(t *testing.T, identity generatedDomainIdentityFixture) {
	t.Helper()
	if identity.database == nil {
		t.Fatal("A0 deployment identity guard requires the sealed database")
	}
	var count int
	if err := identity.database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name='deployment_identity_immutable'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		if _, err := identity.database.Exec(generatedDomainA0DeploymentIdentityGuardSQL); err != nil {
			t.Fatal(err)
		}
		count = 1
	}
	if count != 1 {
		t.Fatalf("deployment identity guard count = %d, want 1", count)
	}
	var statement string
	if err := identity.database.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='deployment_identity_immutable'`).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if strings.Join(strings.Fields(statement), " ") != strings.Join(strings.Fields(generatedDomainA0DeploymentIdentityGuardSQL), " ") {
		t.Fatal("deployment identity guard does not match the checked-in schema")
	}
}

func generatedDomainA0BuilderObjects(t *testing.T, base generatedDomainTwoComponentColdFixture) []generatedDomainDockerObject {
	t.Helper()
	if base.builder == nil || base.runner == nil || base.runner.delegate == nil {
		t.Fatal("A0 namespace builder fixture is incomplete")
	}
	containerName := generatedDomainTwoComponentBuilderContainerName(base.builder)
	networkName := base.builder.identity.NetworkName
	objects := make([]generatedDomainDockerObject, 0, 2)
	for _, object := range base.runner.delegate.objects {
		if (object.kind == "container" && object.name == containerName) ||
			(object.kind == "network" && object.name == networkName) {
			objects = append(objects, object)
		}
	}
	if len(objects) != 2 {
		t.Fatalf("persisted A0 builder object count = %d, want 2", len(objects))
	}
	return objects
}

func installGeneratedDomainA0CommittedV2(t *testing.T, identity generatedDomainIdentityFixture, manager *Manager,
	source routeState, migrations []*generatedMigrationFixture,
) (gatewayV2RouteState, gatewayMigrationJournal) {
	t.Helper()
	db := identity.database
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'a0-admin','hash','administrator',datetime('now'),datetime('now'))`, gatewayRebindTestAdministrator); err != nil {
		t.Fatal(err)
	}
	repository := appaccess.New(db)
	ctx := context.Background()
	profileSpec := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.96.8", InterfaceID: "a0-gateway",
		PortStart: 8100, PortEnd: 8119}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := repository.ConfigureGatewayProfile(ctx, appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), Spec: profileSpec,
		Approval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: profileDigest, ActorID: gatewayRebindTestAdministrator}})
	if err != nil {
		t.Fatal(err)
	}
	upgradeOperationID := uuid.NewString()
	type grantFixture struct {
		revision appaccess.AppAccessRevision
		grant    appaccess.AppAccessGrantClaim
	}
	grants := make(map[string]grantFixture, len(migrations))
	for _, migration := range migrations {
		appID := migration.guarded.appID
		allocation, _, err := repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{AppID: appID,
			OperationID: uuid.NewString(), GatewayProfileRevisionID: profile.ID,
			GatewayProfileRevisionNumber: profile.RevisionNumber})
		if err != nil {
			t.Fatal(err)
		}
		digest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
		if err != nil {
			t.Fatal(err)
		}
		revision, _, err := repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{AppID: appID,
			OperationID: allocation.OwnerOperationID, AllocationID: allocation.ID,
			Approval: appaccess.Approval{Action: appaccess.ActionEnableAppAccess,
				SpecDigest: digest, ActorID: gatewayRebindTestAdministrator}})
		if err != nil {
			t.Fatal(err)
		}
		grant, _, err := repository.ClaimAppAccessGrant(ctx, appaccess.ClaimAppAccessGrantInput{
			AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, profile),
			ActorID: gatewayRebindTestAdministrator})
		if err != nil {
			t.Fatal(err)
		}
		owner := appaccess.AppAccessGrantClaimOwnerFor(grant)
		if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, owner,
			appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, owner,
			appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive); err != nil {
			t.Fatal(err)
		}
		grant, _, err = repository.ResolveAppAccessGrantClaim(ctx, owner,
			appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted, appaccess.AppAccessGrantProof{
				GatewayOperationID: upgradeOperationID, ProtectedStateDigest: strings.Repeat("a", 64)})
		if err != nil {
			t.Fatal(err)
		}
		grants[appID] = grantFixture{revision: revision, grant: grant}
	}
	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{ProfileRevisionID: profile.ID,
		ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(ctx, appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: upgradeOperationID, Spec: upgradeSpec,
		Approval: appaccess.Approval{Action: appaccess.ActionUpgradeGateway,
			SpecDigest: upgradeDigest, ActorID: gatewayRebindTestAdministrator}})
	if err != nil {
		t.Fatal(err)
	}
	owner := appaccess.GatewayProfileUpgradeClaimOwner{OperationID: upgrade.OperationID,
		ProfileRevisionID: upgrade.ProfileRevisionID, ProfileRevisionNumber: upgrade.ProfileRevisionNumber}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner,
		appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeServing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner,
		appaccess.GatewayProfileUpgradeServing, appaccess.GatewayProfileUpgradeCommitted); err != nil {
		t.Fatal(err)
	}
	profileBinding := gatewayProfileBinding{RevisionID: profile.ID, RevisionNumber: profile.RevisionNumber,
		SpecDigest: profile.SpecDigest, SelectedIPv4: profile.Spec.SelectedIPv4,
		InterfaceID: profile.Spec.InterfaceID, PortStart: profile.Spec.PortStart, PortEnd: profile.Spec.PortEnd}
	identityDigest, err := gatewayV1ObservedIdentityDigest(gatewayV2DockerObservation{
		Image: gatewayV2IdentityTestImage(), V1Container: caddyInspection{ID: "sha256:" + strings.Repeat("b", 64)},
		V1NetworkID: "sha256:" + strings.Repeat("e", 64),
		V1VolumeIdentity: gatewayV1VolumeIdentity{
			Mountpoint: "/var/lib/docker/volumes/rig-generated-caddy-config-v1/_data",
			CreatedAt:  "2026-09-29T00:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, gatewayUpgradePreparation{OperationID: upgrade.OperationID,
		Profile: profileBinding, Network: gatewayV2NetworkPlan{Subnet: "10.241.0.0/28", GatewayIPv4: "10.241.0.1",
			ContainerIPv4: "10.241.0.2"}, SourceIdentityDigest: identityDigest,
		LocalHostPort: manager.options.HostPort, ApprovedBy: upgrade.ApprovedBy,
		ApprovedActionDigest: upgradeDigest})
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	phase := gatewayPhasePrepared
	for _, next := range []gatewayMigrationPhase{gatewayPhaseStageIntent, gatewayPhaseStaged,
		gatewayPhaseTransferIntent, gatewayPhaseV2Serving, gatewayPhaseCommitted} {
		if next == gatewayPhaseStaged {
			bindUpgradeStageResources(t, store, upgrade.OperationID)
		}
		if next == gatewayPhaseV2Serving {
			bindUpgradeFinalResource(t, store, upgrade.OperationID)
		}
		journal, err = store.transitionMigrationJournal(upgrade.OperationID, phase, next)
		if err != nil {
			t.Fatal(err)
		}
		phase = next
	}
	state, journal, err = store.loadBoundUpgrade(upgrade.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for appID, item := range grants {
		binding, err := gatewayV2LANBindingForRequest(gatewayV2LANGrantRequest{AttemptID: item.grant.AttemptID,
			ClaimRequestDigest: item.grant.RequestDigest, AppID: appID, AllocationID: item.revision.Allocation.ID,
			OwnerOperationID: item.revision.OperationID, Port: item.revision.Allocation.Port,
			AccessRevisionID: item.revision.ID, AccessRevisionNumber: item.revision.RevisionNumber,
			AccessSpecDigest: item.revision.SpecDigest, ApprovedBy: item.grant.Spec.ApprovedBy,
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
			GatewayProfileSpecDigest: profile.SpecDigest})
		if err != nil {
			t.Fatal(err)
		}
		previous := cloneGatewayV2AppRoute(state.Apps[appID])
		proposed := cloneGatewayV2AppRoute(previous)
		proposed.LAN = &binding
		pending := cloneGatewayV2RouteState(state)
		pending.Pending = &gatewayV2PendingRoute{Kind: gatewayV2PendingLANGrant, AppID: appID,
			Previous: &previous, Proposed: proposed}
		if err := store.saveCommittedV2State(pending, journal); err != nil {
			t.Fatal(err)
		}
		state = cloneGatewayV2RouteState(pending)
		state.Pending = nil
		state.Apps[appID] = proposed
		if err := store.saveCommittedV2State(state, journal); err != nil {
			t.Fatal(err)
		}
	}
	state, journal, err = store.loadBoundUpgrade(upgrade.OperationID)
	if err != nil || len(state.Apps) != 2 {
		t.Fatalf("load committed two-app A0 V2 state = %#v, %v", state, err)
	}
	return state, journal
}

func generatedDomainA0GatewayObjects(state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) []generatedDomainDockerObject {
	descriptors := []generatedDomainRecoveryDockerDescriptor{
		generatedDomainGatewayV2ContainerDescriptor(state, journal, gatewayV2FinalContainerRole,
			journal.Resources.FinalContainerID),
		generatedDomainGatewayV2NetworkDescriptor(state, journal),
		generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2ConfigVolumeRole,
			journal.Resources.ConfigVolume),
		generatedDomainGatewayV2VolumeDescriptor(state, journal, gatewayV2DataVolumeRole,
			journal.Resources.DataVolume),
	}
	objects := generatedDomainDockerObjectsForDescriptors(descriptors)
	imageID := journal.Resources.ImageID
	if !strings.HasPrefix(imageID, "sha256:") {
		imageID = "sha256:" + imageID
	}
	objects = append(objects, generatedDomainDockerObject{kind: "image", id: imageID,
		labels: map[string]string{}, data: generatedDomainDockerObjectData{contentID: imageID}})
	return objects
}

func generatedDomainA0ApplicationObjects(t *testing.T,
	physical []candidateOwnerPhysical,
) []generatedDomainDockerObject {
	t.Helper()
	result := make([]generatedDomainDockerObject, 0, len(physical)*2+2)
	networks := make(map[string]string)
	for _, value := range physical {
		policy := value.policy
		result = append(result,
			generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID,
				labels: map[string]string{"io.rig.managed": "generated-image",
					"io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact":  policy.identity.ArtifactID,
					"io.rig.plan":      policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role,
					"io.rig.definition": policy.buildDefinitionDigest},
				data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
			generatedDomainDockerObject{kind: "container", id: value.containerID,
				name: policy.identity.ContainerName, labels: candidatePolicyContainerLabels(policy)})
		if previous, exists := networks[policy.identity.NetworkName]; exists && previous != policy.identity.AppID {
			t.Fatal("A0 application network is crossed")
		}
		networks[policy.identity.NetworkName] = policy.identity.AppID
	}
	for network, appID := range networks {
		result = append(result, generatedDomainDockerObject{kind: "network", id: generatedDomainA0NetworkID(appID),
			name: network, labels: map[string]string{"io.rig.managed": "generated-runtime-network",
				"io.rig.application": appID},
			data: generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}})
	}
	return result
}

func generatedDomainA0NetworkID(appID string) string {
	value := strings.ReplaceAll(appID, "-", "")
	return strings.Repeat(value, 2)[:64]
}

func generatedDomainA0Object(t *testing.T, objects []generatedDomainDockerObject,
	kind, appID, component string,
) *generatedDomainDockerObject {
	t.Helper()
	for index := range objects {
		object := &objects[index]
		if object.kind == kind && object.labels["io.rig.application"] == appID &&
			object.labels["io.rig.component"] == component {
			return object
		}
	}
	t.Fatalf("A0 object %s/%s/%s is absent", kind, appID, component)
	return nil
}

func removeGeneratedDomainA0Object(objects []generatedDomainDockerObject,
	kind, appID, component string,
) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, 0, len(objects))
	for _, object := range objects {
		if object.kind == kind && object.labels["io.rig.application"] == appID &&
			object.labels["io.rig.component"] == component {
			continue
		}
		result = append(result, object)
	}
	return result
}

func generatedDomainTwoComponentBuilderContainerName(fixture *generatedDomainTwoComponentBuilderFixture) string {
	if fixture == nil {
		return ""
	}
	return strings.TrimPrefix(fixture.physical.container.Name, "/")
}
