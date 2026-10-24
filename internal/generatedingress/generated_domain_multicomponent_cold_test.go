//go:build linux || windows

package generatedingress

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/projectanalysis"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type generatedDomainTwoComponentColdFixture struct {
	identity    generatedDomainIdentityFixture
	migration   *generatedMigrationFixture
	runner      *generatedDomainConstructionRunner
	environment *candidateFactoryEnvironment
}

type generatedDomainCandidateRosterRunner struct {
	physical []candidateOwnerPhysical
}

type generatedDomainTwoComponentRouteRunner struct {
	daemonID string
	delegate *generatedRoutePredecessorRunner
}

func (r *generatedDomainTwoComponentRouteRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	if len(request.Args) == 3 && request.Args[0] == "info" && request.Args[1] == "--format" &&
		request.Args[2] == "{{json .ID}}" {
		return jsonResult(r.daemonID), nil
	}
	return r.delegate.Run(ctx, request)
}

func (r *generatedDomainCandidateRosterRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	kind := candidateOwnerKind(request)
	if len(request.Args) != 5 ||
		(kind != candidateOwnerImageInspect && kind != candidateOwnerNetworkInspect && kind != candidateOwnerInspect) {
		return runtimeprocess.CommandResult{}, errors.New("unexpected two-component candidate command")
	}
	target := request.Args[4]
	for _, physical := range r.physical {
		matches := kind == candidateOwnerImageInspect && target == physical.policy.identity.ImageContentID ||
			kind == candidateOwnerNetworkInspect && target == physical.policy.identity.NetworkName ||
			kind == candidateOwnerInspect &&
				(target == physical.containerID || target == physical.policy.identity.ContainerName)
		if !matches {
			continue
		}
		candidate := &candidateOwnerRunner{physical: physical}
		return candidate.defaultResult(kind, request)
	}
	return runtimeprocess.CommandResult{}, errors.New("two-component candidate object is absent")
}

func TestGeneratedDomainColdReconstructsExactTwoComponentCurrentRoster(t *testing.T) {
	if !generatedDomainRegistryIsolated(t) {
		return
	}
	for _, test := range []struct {
		name   string
		mutate func(*generatedDomainTwoComponentColdFixture)
		pass   bool
	}{
		{name: "exact_api_and_frontend", pass: true},
		{name: "missing_frontend", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			fixture.runner.delegate.objects = removeGeneratedDomainTwoComponentContainer(
				fixture.runner.delegate.objects, "frontend")
		}},
		{name: "crossed_frontend", mutate: func(fixture *generatedDomainTwoComponentColdFixture) {
			for index := range fixture.runner.delegate.objects {
				object := &fixture.runner.delegate.objects[index]
				if object.kind == "container" && object.labels["io.rig.component"] == "frontend" {
					object.id = strings.Repeat("9", 64)
					return
				}
			}
			t.Fatal("frontend container is absent from the cold census")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generatedDomainConstructionCase(t, func(t *testing.T) {
				fixture := newGeneratedDomainTwoComponentColdFixture(t)
				if test.mutate != nil {
					test.mutate(&fixture)
				}
				result, err := newGeneratedDomainConstruction(context.Background(), fixture.identity.stores,
					fixture.runner, fixture.environment, fixture.identity.options)
				if test.pass {
					if err != nil || result == nil || result.cold == nil || len(result.cold.groups) != 1 {
						t.Fatalf("two-component cold construction = %#v, %v", result, err)
					}
					group := result.cold.groups[0]
					if group.appID != fixture.migration.guarded.appID ||
						group.deploymentID != fixture.migration.request.DeploymentID ||
						group.head == nil || group.head.deploymentID != fixture.migration.request.DeploymentID ||
						len(group.components) != 2 {
						t.Fatalf("two-component cold manifest group = %#v", group)
					}
					names := make([]string, len(group.components))
					receipts := make(map[string]struct{}, len(group.components))
					for index, component := range group.components {
						if component.charged || component.retained || component.receiptID == "" {
							t.Fatalf("two-component cold manifest component = %#v", component)
						}
						names[index] = component.name
						receipts[component.receiptID] = struct{}{}
					}
					sort.Strings(names)
					if !equalStrings(names, []string{"api", "frontend"}) || len(receipts) != 2 {
						t.Fatalf("two-component cold names=%v receipts=%d", names, len(receipts))
					}
				} else if result != nil || err == nil || !errors.Is(err, errGeneratedDomainConstructionRefused) {
					t.Fatalf("invalid two-component cold roster = %#v, %v", result, err)
				}
				for _, command := range fixture.runner.snapshot() {
					if generatedDomainConstructionJoinCommandMutatesOrProbesCapacity(command) {
						t.Fatalf("two-component cold reconstruction dispatched an effect/capacity probe: %#v", command)
					}
				}
			})
		})
	}
}

func newGeneratedDomainTwoComponentColdFixture(t *testing.T) generatedDomainTwoComponentColdFixture {
	t.Helper()
	identity := newGeneratedDomainIdentityFixture(t)
	plan := generatedDomainTwoComponentPlan()
	migration := newGeneratedMigrationFixtureOnDomainWithComponents(t, identity,
		[]string{"api", "frontend"}, []string{"api", "frontend"}, plan)
	identity.stores = migration.repository.stores
	validation := newCandidateOwnerValidationFixtureWithMigration(t, migration)
	options := generatedDomainCandidateOptions(identity.options)
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: 2 * uint64(options.MemoryBytes),
		DiskAvailableBytes:   2 * options.ReplacementDiskBytes,
	}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatal(err)
	}
	base := &candidateOwnerRunner{absenceErr: generatedMigrationCompletedExitOne(t),
		before:  make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results: make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
	candidateRunner := &generatedRouteCandidateRunner{delegate: base, physical: make(map[string]candidateOwnerPhysical)}
	candidateEnvironment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := migration.repository.GeneratedCandidateAuthority(candidateRunner, candidateEnvironment, budget, options)
	if err != nil {
		t.Fatal(err)
	}
	validation.authority = authority
	candidate := &candidateOwnerFixture{validation: validation, authority: authority, runner: base,
		environment: candidateEnvironment, source: source, budget: budget, options: options}
	reservation := candidate.reserve(t)
	t.Cleanup(reservation.Release)
	execution := candidate.pin(t, reservation)
	t.Cleanup(execution.Leave)

	names := []string{"api", "frontend"}
	containerIDs := []string{strings.Repeat("7", 64), strings.Repeat("8", 64)}
	invocations := make([]GeneratedCandidateInvocation, 0, len(names))
	identities := make([]CandidateIdentity, 0, len(names))
	for index, name := range names {
		invocation, err := execution.BeginCreate(name)
		if err != nil {
			t.Fatalf("begin %s create: %v", name, err)
		}
		permit, ok := invocation.(*generatedCandidateInvocationPermit)
		if !ok || permit == nil {
			t.Fatalf("%s invocation type = %T", name, invocation)
		}
		base.mu.Lock()
		base.containerID = containerIDs[index]
		base.physical = candidateOwnerPhysical{policy: permit.policy}
		base.mu.Unlock()
		created, err := execution.CreateAndRecord(invocation)
		if err != nil {
			t.Fatalf("create %s candidate: %v", name, err)
		}
		if err := execution.StartCandidate(invocation); err != nil {
			t.Fatalf("start %s candidate: %v", name, err)
		}
		invocations = append(invocations, invocation)
		identities = append(identities, created)
	}
	state, err := execution.FinishStarting()
	if err != nil || state.Phase != generatedruntimestate.PhaseWaitingHealth {
		t.Fatalf("finish two-component starting = %#v, %v", state, err)
	}
	for index, invocation := range invocations {
		if err := execution.WaitHealthyAndRecord(invocation); err != nil {
			t.Fatalf("observe %s health: %v", names[index], err)
		}
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	if identities[0].NetworkName == "" || identities[0].NetworkName != identities[1].NetworkName ||
		identities[0].ContainerID == identities[1].ContainerID ||
		identities[0].ReceiptID == "" || identities[0].ReceiptID == identities[1].ReceiptID {
		t.Fatalf("two-component candidate identities = %#v", identities)
	}

	store, err := newStateStore(identity.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, _, ingressIP := ingressNetworkCandidate(0)
	ingress := &ingressRunner{appID: migration.guarded.appID, network: identities[0].NetworkName,
		endpoint: generatedRouteEndpoint(identities[0]),
		caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
			GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
		ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
			Name: caddyContainerName, IPv4Address: ingressIP + "/28"}}, files: map[string][]byte{}}
	physicalRoutes := &generatedRoutePredecessorRunner{ingressRunner: ingress,
		slot: generatedidentity.Slot(identities[0].Slot), endpoints: make(map[string]generatedRouteEndpointSnapshot)}
	for _, candidateIdentity := range identities {
		physicalRoutes.endpoints[candidateIdentity.ContainerID] = generatedRouteEndpointSnapshot{
			appID: migration.guarded.appID, endpoint: generatedRouteEndpoint(candidateIdentity),
			slot: generatedidentity.Slot(candidateIdentity.Slot)}
	}
	routeRunner := &generatedDomainTwoComponentRouteRunner{daemonID: "generated-domain-two-component-cold",
		delegate: physicalRoutes}
	manager, err := assembleGeneratedDomainManager(generatedDomainReservationInput{database: identity.database,
		stores: identity.stores, runner: routeRunner, options: identity.options}, store)
	if err != nil {
		t.Fatal(err)
	}
	physical, _, err := inspectGeneratedDomainPhysicalIdentity(context.Background(), identity.stores,
		routeRunner, identity.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindGeneratedDomainManagerView(manager, physical.manager); err != nil {
		t.Fatal(err)
	}
	routes, err := newGeneratedRouteAuthority(manager, candidate.authority, candidate.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	endpoints := []generatedroute.RouteEndpoint{generatedRouteEndpoint(identities[0]), generatedRouteEndpoint(identities[1])}
	request := generatedroute.RouteSwitchRequest{AppID: migration.guarded.appID,
		ToSlot: generatedidentity.Slot(identities[0].Slot), Endpoints: endpoints}
	sealed, err := routes.Pin(migration.guarded.context, execution, request)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := sealed.Activate(migration.guarded.context)
	if err != nil || activated.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activate two-component route = %#v, %v", activated, err)
	}
	observed, err := manager.Observe(context.Background(), migration.guarded.appID)
	if err != nil || !sameRoute(routeRecord{Slot: request.ToSlot, Endpoints: endpoints},
		routeRecord{Slot: observed.Slot, Endpoints: observed.Endpoints}) {
		t.Fatalf("two-component physical route = %#v, %v", observed, err)
	}
	runtimeState := generatedruntimestate.New(identity.database)
	if _, err := runtimeState.Advance(context.Background(), migration.guarded.appID,
		migration.request.DeploymentID, generatedruntimestate.PhaseDraining,
		generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.repository.Transition(context.Background(), migration.guarded.appID,
		migration.request.DeploymentID, Succeeded, ""); err != nil {
		t.Fatal(err)
	}
	execution.Leave()
	reservation.Release()
	if err := migration.releaseEffects(); err != nil {
		t.Fatal(err)
	}

	stateValue, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	_, v2, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, stateValue, v2, journal, gatewayTopologyExactV1Only)
	observation.V1Container.ID = strings.Repeat("d", 64)
	observation.V1NetworkID = strings.Repeat("e", 64)
	for id, member := range observation.V1Network.Containers {
		delete(observation.V1Network.Containers, id)
		observation.V1Network.Containers[observation.V1Container.ID] = member
	}
	gateway := generatedDomainNativeV1OuterGatewayRunner(observation)
	objects := generatedDomainDockerObjectsForDescriptors(generatedDomainNativeV1DockerDescriptors())
	for index := range objects {
		switch objects[index].name {
		case caddyContainerName:
			objects[index].id = observation.V1Container.ID
		case caddyNetworkName:
			objects[index].id = observation.V1NetworkID
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Network.Driver,
				scope: observation.V1Network.Scope, options: cloneStringMap(observation.V1Network.Options)}
		case caddyVolumeName:
			objects[index].data = generatedDomainDockerObjectData{driver: observation.V1Volume.Driver,
				scope: observation.V1Volume.Scope, options: cloneStringMap(observation.V1Volume.Options),
				mountpoint: observation.V1VolumeIdentity.Mountpoint, createdAt: observation.V1VolumeIdentity.CreatedAt}
		}
	}
	physicalRoster := make([]candidateOwnerPhysical, 0, len(candidateRunner.physical))
	for _, value := range candidateRunner.physical {
		physicalRoster = append(physicalRoster, value)
	}
	sort.Slice(physicalRoster, func(i, j int) bool {
		return physicalRoster[i].policy.identity.Component < physicalRoster[j].policy.identity.Component
	})
	if len(physicalRoster) != 2 ||
		physicalRoster[0].policy.identity.ImageContentID == physicalRoster[1].policy.identity.ImageContentID {
		t.Fatalf("two-component physical roster = %#v", physicalRoster)
	}
	for _, value := range physicalRoster {
		policy := value.policy
		objects = append(objects,
			generatedDomainDockerObject{kind: "image", id: policy.identity.ImageContentID,
				labels: map[string]string{"io.rig.managed": "generated-image",
					"io.rig.application": policy.identity.AppID, "io.rig.release": policy.identity.ReleaseID,
					"io.rig.artifact": policy.identity.ArtifactID, "io.rig.plan": policy.identity.DeploymentPlanRevisionID,
					"io.rig.component": policy.identity.Component, "io.rig.role": policy.identity.Role,
					"io.rig.definition": policy.buildDefinitionDigest},
				data: generatedDomainDockerObjectData{contentID: policy.identity.ImageContentID}},
			generatedDomainDockerObject{kind: "container", id: value.containerID,
				name: policy.identity.ContainerName, labels: generatedDomainColdContainerLabels(policy.identity)},
		)
	}
	objects = append(objects, generatedDomainDockerObject{kind: "network", id: strings.Repeat("6", 64),
		name: physicalRoster[0].policy.identity.NetworkName,
		labels: map[string]string{"io.rig.managed": "generated-runtime-network",
			"io.rig.application": physicalRoster[0].policy.identity.AppID},
		data: generatedDomainDockerObjectData{driver: "bridge", scope: "local", options: map[string]string{}}})
	identity.options.HostPort = journal.Source.LocalHostPort
	runner := &generatedDomainConstructionRunner{roster: &generatedDomainCandidateRosterRunner{physical: physicalRoster},
		delegate: &generatedDomainRecoveryCensusRunner{daemonID: "generated-domain-two-component-cold",
			objects: objects, imageID: observation.Image.ID, gateway: gateway}}
	return generatedDomainTwoComponentColdFixture{identity: identity, migration: migration, runner: runner,
		environment: &candidateFactoryEnvironment{path: t.TempDir()}}
}

func generatedDomainTwoComponentPlan() deploymentplans.Plan {
	components := []deploymentplans.Component{
		{Name: "api", Role: "server", Technology: "node", RootDirectory: "apps/api",
			PackageManager: "npm", InstallBehavior: "npm ci", InstallDirectory: "apps/api",
			NodeVersion: "24", BuildCommand: "npm run build", RunCommand: "node server.js",
			InternalPort: 3000, HealthProbe: "/health"},
		{Name: "frontend", Role: "static", Technology: "static", RootDirectory: "apps/frontend",
			StaticOutputDirectory: "dist", PackageManager: "npm", InstallBehavior: "npm ci",
			InstallDirectory: "apps/frontend", NodeVersion: "24", BuildCommand: "npm run build",
			RunCommand: projectanalysis.ManagedStaticCommand("dist", 8080), InternalPort: 8080, HealthProbe: "/"},
	}
	fields := []string{"role", "rootDirectory", "packageManager", "installBehavior", "installDirectory",
		"nodeVersion", "buildCommand", "runCommand", "internalPort", "healthProbe", "technology",
		"staticOutputDirectory"}
	provenance := make([]deploymentplans.FieldProvenance, 0, len(components)*len(fields))
	for _, component := range components {
		for _, field := range fields {
			provenance = append(provenance, deploymentplans.FieldProvenance{
				Field:  "components." + component.Name + "." + field,
				Origin: deploymentplans.ProvenanceUser, Confidence: 100,
				Evidence: []string{"user:deployment-setup"},
			})
		}
	}
	return deploymentplans.Plan{SetupVersion: projectanalysis.SetupVersion,
		Strategy: deploymentplans.StrategyGeneratedNode,
		Detector: deploymentplans.Detector{Name: "manual-setup", Version: "1",
			SourceStructuralFingerprint: strings.Repeat("a", 64)},
		Source: deploymentplans.SourceIdentity{Provider: "github", RepositoryID: 7,
			ResolvedDigest: strings.Repeat("c", 64)},
		Components: components, FieldProvenance: provenance,
		Migration: &deploymentplans.Migration{ComponentName: "api", RootDirectory: "apps/api",
			Command: "npm run migrate -- --locked", EnvironmentKeys: []string{"DATABASE_URL"},
			EvidenceDigest: strings.Repeat("b", 64),
			Approval:       deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}},
	}
}

func removeGeneratedDomainTwoComponentContainer(objects []generatedDomainDockerObject,
	component string,
) []generatedDomainDockerObject {
	result := make([]generatedDomainDockerObject, 0, len(objects))
	for _, object := range objects {
		if object.kind == "container" && object.labels["io.rig.component"] == component {
			continue
		}
		result = append(result, object)
	}
	return result
}
