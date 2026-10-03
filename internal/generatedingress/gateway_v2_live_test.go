package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
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
	trace := liveGatewayV2InstallOperationTrace(fixture)

	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request, liveGatewayV2Authorizer(t, fixture.request))
	if err != nil || result.Outcome != GatewayV2UpgradeCommitted {
		trace.log(t)
		liveGatewayV2LogOperationDiagnostic(t, fixture)
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
	trace := liveGatewayV2InstallOperationTrace(fixture)

	var conflictID string
	t.Cleanup(func() {
		liveGatewayV2RemoveConflictContainer(t, fixture, conflictID)
	})
	conflictID = liveGatewayV2StartConflictContainer(t, fixture)
	liveGatewayV2AssertConflictBinding(t, fixture, conflictID)
	liveGatewayV2AwaitConflict(t, fixture, "bind-conflict fixture did not own the approved listener")

	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request, liveGatewayV2Authorizer(t, fixture.request))
	if err == nil || result.Outcome != GatewayV2UpgradeRolledBack {
		trace.log(t)
		liveGatewayV2LogOperationDiagnostic(t, fixture)
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
	liveGatewayV2AssertSourceAttestation(t, fixture)
	return fixture
}

// liveGatewayV2AssertSourceAttestation diagnoses only the read-only v1/no-v2
// proof that must pass before UpgradeGatewayV2 may create protected state or a
// Docker resource. Public CI output is limited to predicate names; it never
// includes Docker inspect data, resource identities, addresses, paths, labels,
// configuration contents, or command errors.
func liveGatewayV2AssertSourceAttestation(t *testing.T, fixture *liveGatewayV2Fixture) {
	t.Helper()
	preparation, err := gatewayV2UpgradePreparation(fixture.request)
	if err != nil {
		t.Fatal("gateway-v2 source attestation rejected before mutation: fixture_request_valid")
	}
	preparation.LocalHostPort = fixture.ingress.options.HostPort
	identity, err := newGatewayV2Identity(fixture.request.OperationID)
	if err != nil {
		t.Fatal("gateway-v2 source attestation rejected before mutation: fixture_identity_valid")
	}
	observation, err := fixture.ingress.inspectGatewayV2PreparationAbortDocker(fixture.ctx, fixture.source, identity)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		t.Fatal("gateway-v2 source attestation rejected before mutation: source_snapshot_available")
	}
	defer clearGatewayV2DockerObservation(&observation)

	identityDigest, identityErr := gatewayV1ObservedIdentityDigest(observation)
	journal := gatewayMigrationJournal{Source: gatewayMigrationSourceRef{
		IdentityDigest: identityDigest,
		LocalHostPort:  preparation.LocalHostPort,
	}}
	failures := liveGatewayV2SourceAttestationFailures(fixture.source, preparation, journal, observation, identityErr)
	_, valid := classifyGatewayV2PreparationAbort(fixture.source, preparation, observation)
	if valid && len(failures) == 0 {
		return
	}
	if len(failures) == 0 {
		failures = append(failures, "aggregate_source_classifier")
	}
	t.Fatalf("gateway-v2 source attestation rejected before mutation: failed predicates=%s", strings.Join(failures, ","))
}

func liveGatewayV2SourceAttestationFailures(source routeState, preparation gatewayUpgradePreparation,
	journal gatewayMigrationJournal, observation gatewayV2DockerObservation, identityErr error,
) []string {
	failures := make([]string, 0, 16)
	add := func(name string, valid bool) {
		if !valid {
			failures = append(failures, name)
		}
	}

	add("source_and_request_inputs", validRouteState(source) && source.Pending == nil &&
		validCanonicalUUID(preparation.OperationID) && validGatewayProfileBinding(preparation.Profile) && preparation.LocalHostPort != 0)
	add("observed_v1_identity", identityErr == nil && validSHA256(journal.Source.IdentityDigest))
	add("pinned_image", validGatewayPinnedImage(observation.Image, observation.ImageFound))

	v1Presence := observation.V1ContainerFound && observation.V1VolumeFound && observation.V1NetworkFound
	add("v1_resources_present", v1Presence)
	v1RuntimeValid := validGatewayContainerRuntime(observation.V1Runtime, true)
	add("v1_runtime", v1RuntimeValid)
	v1EffectivePortsValid := gatewayV2EffectivePortBindingsMatchConfigured(
		observation.V1Runtime.EffectivePortBindings, observation.V1Container.PortBindings)
	add("v1_effective_port_bindings", v1EffectivePortsValid)
	v1ContainerSpecValid := validCaddyInspection(observation.V1Container, observation.Image.ID, journal.Source.LocalHostPort)
	add("v1_container_spec", v1ContainerSpecValid)
	v1VolumeValid := observation.V1Volume.Name == caddyVolumeName && observation.V1Volume.Driver == "local" &&
		observation.V1Volume.Scope == "local" && len(observation.V1Volume.Options) == 0 &&
		observation.V1Volume.Labels[gatewayV2ManagedLabelKey] == gatewayV2ManagedContainerLabel &&
		observation.V1Volume.Labels[gatewayV2IdentityLabelKey] == gatewayV1IdentityVersion
	add("v1_config_volume", v1VolumeValid)

	listenIP, ingressValid := caddyIngressAddress(observation.V1Network, observation.V1Container.ID)
	add("v1_ingress_attachment", ingressValid)
	expectedConfig, configErr := buildCaddyConfig(source.Active,
		net.JoinHostPort(listenIP, strconv.FormatUint(uint64(gatewayV2ContainerPort), 10)))
	v1ConfigValid := configErr == nil && sameCaddyConfig(expectedConfig, observation.V1RestartConfig) &&
		(observation.V1Container.Running && sameCaddyConfig(expectedConfig, observation.V1Config) ||
			!observation.V1Container.Running && len(observation.V1Config) == 0)
	add("v1_config", v1ConfigValid)

	owners, ownersValid := gatewayRouteNetworkOwners(source.Active)
	networkShapeValid := ownersValid && len(observation.V1ApplicationNetworks) == len(owners) &&
		len(observation.V1ApplicationNetworkIDs) == len(owners) && len(observation.V1Container.Networks) == len(owners)+1
	add("v1_application_network_shape", networkShapeValid)
	networkMembershipValid := networkShapeValid
	if networkMembershipValid {
		for name, appID := range owners {
			inspection, exists := observation.V1ApplicationNetworks[name]
			if !exists || !validContainerID(observation.V1ApplicationNetworkIDs[name]) ||
				!validApplicationNetwork(inspection.identity(), appID) ||
				!validGatewayApplicationNetworkMembership(inspection, observation.V1Container, name) {
				networkMembershipValid = false
				break
			}
		}
	}
	add("v1_application_network_membership", networkMembershipValid)
	observedIdentity, observedIdentityErr := gatewayV1ObservedIdentityDigest(observation)
	v1IdentityBindingValid := observedIdentityErr == nil && observedIdentity == journal.Source.IdentityDigest
	add("v1_identity_binding", v1IdentityBindingValid)
	if !validGatewayV1Base(source, journal, observation, true) && v1Presence &&
		v1RuntimeValid && v1EffectivePortsValid && v1ContainerSpecValid && v1VolumeValid && ingressValid &&
		v1ConfigValid && networkMembershipValid && v1IdentityBindingValid {
		add("v1_base_aggregate", false)
	}

	add("v1_snapshot_stable", observation.V1Stable)
	add("v1_resource_snapshot_stable", observation.V1ResourcesStable)
	add("v1_endpoint_identity", observation.V1EndpointIdentityProven)
	add("v1_serving", observation.V1Container.Running && !observation.V1Container.Restarting)
	add("v2_resource_snapshot_stable", observation.V2ResourcesStable)
	add("v2_stage_absence_stable", observation.StageStable)
	add("v2_final_absence_stable", observation.FinalStable)
	add("v2_owned_inventory_stable", observation.OwnedInventoriesStable)
	add("v2_resources_absent", gatewayV2SourceAttestationHasNoV2Resources(observation))
	return failures
}

func TestGatewayV2SourceAttestationDiagnosticNamesRejectingPredicates(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1Only)
	preparation := gatewayUpgradePreparation{
		OperationID:   state.OperationID,
		Profile:       state.Profile,
		LocalHostPort: journal.Source.LocalHostPort,
	}
	if failures := liveGatewayV2SourceAttestationFailures(source, preparation, journal, observation, nil); len(failures) != 0 {
		t.Fatalf("valid source diagnostic failures=%s", strings.Join(failures, ","))
	}

	observation.V1Runtime.EffectivePortBindings["9999/tcp"] = []map[string]string{{
		"HostIp": "127.0.0.1", "HostPort": "9999",
	}}
	observation.V1EndpointIdentityProven = false
	if got, want := strings.Join(liveGatewayV2SourceAttestationFailures(source, preparation, journal, observation, nil), ","),
		"v1_effective_port_bindings,v1_endpoint_identity"; got != want {
		t.Fatalf("source diagnostic failures=%q, want %q", got, want)
	}
}

type liveGatewayV2OperationProofDiagnostic struct {
	topology                  gatewayObservedTopology
	recovery                  gatewayV2RecoveryTopology
	requestBound              bool
	topologyInputs            bool
	pinnedImage               bool
	resourcesBound            bool
	v1Base                    bool
	v1Stable                  bool
	v1ResourcesStable         bool
	v1EndpointIdentity        bool
	v2ResourcesStable         bool
	stageStable               bool
	finalStable               bool
	ownedInventoryStable      bool
	stageRunningExact         bool
	stageStoppedExact         bool
	stageConfigExact          bool
	stage404                  bool
	stageHostPublication      bool
	transferRunningStageExact bool
	transferNoContainersExact bool
	transferStoppedFinalExact bool
	v1StoppedRestartable      bool
	v1RollbackReady           bool
	finalContainerExact       bool
	finalIngressNetworkExact  bool
	finalApplicationNetworks  bool
	finalConfigExact          bool
	final404                  bool
	finalRoutes               bool
	finalHostPublication      bool
	finalEndpointIdentity     bool
	finalTopologyExact        bool
}

func liveGatewayV2OperationProofs(request GatewayV2UpgradeRequest, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal, observation gatewayV2DockerObservation,
) liveGatewayV2OperationProofDiagnostic {
	v1StoppedRestartable := observation.V1Stable && observation.V1ResourcesStable &&
		!observation.V1Container.Running && !observation.V1Container.Restarting
	v1RollbackReady := v1StoppedRestartable && observation.V1EndpointIdentityProven
	stageRunningExact := classifyGatewayV2Topology(source, state, journal, observation) == gatewayTopologyExactV1WithStage
	return liveGatewayV2OperationProofDiagnostic{
		topology:                  classifyGatewayV2Topology(source, state, journal, observation),
		recovery:                  classifyGatewayV2RecoveryTopology(source, state, journal, observation),
		requestBound:              gatewayV2RequestMatchesState(request, state, journal),
		topologyInputs:            validGatewayTopologyInputs(source, state, journal),
		pinnedImage:               validGatewayPinnedImage(observation.Image, observation.ImageFound),
		resourcesBound:            gatewayV2ObservedResourcesMatchJournal(journal, observation),
		v1Base:                    validGatewayV1Base(source, journal, observation, journal.Phase != gatewayPhaseCommitted),
		v1Stable:                  observation.V1Stable,
		v1ResourcesStable:         observation.V1ResourcesStable,
		v1EndpointIdentity:        observation.V1EndpointIdentityProven,
		v2ResourcesStable:         observation.V2ResourcesStable,
		stageStable:               observation.StageStable,
		finalStable:               observation.FinalStable,
		ownedInventoryStable:      observation.OwnedInventoriesStable,
		stageRunningExact:         stageRunningExact,
		stageStoppedExact:         validGatewayV2StoppedStage(state, journal, observation),
		stageConfigExact:          validGatewayV2StageConfig(state, observation.StageConfig, observation.StageRestartConfig),
		stage404:                  observation.Stage404Proven,
		stageHostPublication:      observation.StageHostPublicationProven,
		transferRunningStageExact: validGatewayV2RunningStageForTransfer(state, journal, observation),
		transferNoContainersExact: validGatewayV2TransferInfrastructureNoContainers(state, journal, observation),
		transferStoppedFinalExact: validGatewayV2StoppedFinalForTransfer(state, journal, observation),
		v1StoppedRestartable:      v1StoppedRestartable,
		v1RollbackReady:           v1RollbackReady,
		finalContainerExact: validGatewayV2Container(state, journal, observation.FinalContainer, observation.FinalRuntime,
			observation.FinalContainerFound, gatewayV2FinalContainerRole, observation.Image.ID),
		finalIngressNetworkExact: validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork,
			observation.IngressFound, observation.FinalContainer.ID, state.Identity.FinalContainer),
		finalApplicationNetworks: validGatewayV2ApplicationNetworks(state, observation.FinalContainer,
			observation.ApplicationNetworks, observation.ApplicationNetworkIDs),
		finalConfigExact:      validGatewayV2FinalConfig(state, observation.FinalConfig, observation.FinalRestartConfig),
		final404:              observation.Final404Proven,
		finalRoutes:           observation.FinalRoutesProven,
		finalHostPublication:  observation.FinalHostPublicationProven,
		finalEndpointIdentity: observation.FinalEndpointIdentityProven,
		finalTopologyExact: validGatewayV2FinalTopology(state, journal, observation, v1StoppedRestartable,
			v1RollbackReady, validGatewayV2FinalConfig(state, observation.FinalConfig, observation.FinalRestartConfig)),
	}
}

func liveGatewayV2LogOperationDiagnostic(t *testing.T, fixture *liveGatewayV2Fixture) {
	t.Helper()
	store, err := newGatewayUpgradeStateStore(fixture.stateRoot)
	if err != nil {
		t.Log("gateway-v2 protected diagnostic: phase_available=false")
		return
	}
	state, journal, err := store.loadBoundUpgrade(fixture.spec.operationID)
	if err != nil {
		t.Log("gateway-v2 protected diagnostic: phase_available=false")
		return
	}
	observation, err := fixture.ingress.inspectGatewayV2Docker(fixture.ctx, fixture.source, state, journal)
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		t.Logf("gateway-v2 protected diagnostic: phase=%s snapshot_available=false", journal.Phase)
		return
	}
	defer clearGatewayV2DockerObservation(&observation)
	proof := liveGatewayV2OperationProofs(fixture.request, fixture.source, state, journal, observation)
	selectedInterface := gatewayV2SelectedInterfacePreflight(state.Profile) == nil
	finalLoopbackRoutes := false
	if observation.FinalContainerFound && observation.FinalContainer.Running && !observation.FinalContainer.Restarting {
		finalLoopbackRoutes = proveGatewayV2FinalLoopbackRoutes(fixture.ctx, state, journal, probeGatewayV2HostStatus)
	}
	t.Logf("gateway-v2 protected diagnostic: phase=%s snapshot_available=true topology=%s recovery=%s "+
		"request_bound=%t topology_inputs=%t pinned_image=%t resources_bound=%t "+
		"v1_base=%t v1_stable=%t v1_resources_stable=%t v1_endpoint_identity=%t "+
		"v2_resources_stable=%t stage_stable=%t final_stable=%t owned_inventory_stable=%t "+
		"stage_running_exact=%t stage_stopped_exact=%t stage_config_exact=%t stage_404=%t stage_host_publication=%t "+
		"transfer_running_stage_exact=%t transfer_no_containers_exact=%t transfer_stopped_final_exact=%t "+
		"v1_stopped_restartable=%t v1_rollback_ready=%t final_container_exact=%t final_ingress_network_exact=%t "+
		"final_application_networks=%t final_config_exact=%t final_404=%t final_routes=%t final_host_publication=%t "+
		"final_endpoint_identity=%t final_topology_exact=%t final_loopback_routes=%t selected_interface=%t",
		journal.Phase, proof.topology, proof.recovery,
		proof.requestBound, proof.topologyInputs, proof.pinnedImage, proof.resourcesBound,
		proof.v1Base, proof.v1Stable, proof.v1ResourcesStable, proof.v1EndpointIdentity,
		proof.v2ResourcesStable, proof.stageStable, proof.finalStable, proof.ownedInventoryStable,
		proof.stageRunningExact, proof.stageStoppedExact, proof.stageConfigExact, proof.stage404, proof.stageHostPublication,
		proof.transferRunningStageExact, proof.transferNoContainersExact, proof.transferStoppedFinalExact,
		proof.v1StoppedRestartable, proof.v1RollbackReady, proof.finalContainerExact, proof.finalIngressNetworkExact,
		proof.finalApplicationNetworks, proof.finalConfigExact, proof.final404, proof.finalRoutes, proof.finalHostPublication,
		proof.finalEndpointIdentity, proof.finalTopologyExact, finalLoopbackRoutes, selectedInterface)
}

func TestGatewayV2OperationProofDiagnosticDistinguishesStageAndFinal(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	request := gatewayV2UpgradeRequestFromStateForTest(state)

	stagedJournal := journal
	stagedJournal.Resources = gatewayV2IdentityTestBoundResources(t)
	stagedJournal.Resources.FinalContainerID = ""
	stagedJournal.Phase = gatewayPhaseStaged
	staged := gatewayV2IdentityTestObservation(t, source, state, stagedJournal, gatewayTopologyExactV1WithStage)
	stagedProof := liveGatewayV2OperationProofs(request, source, state, stagedJournal, staged)
	if stagedProof.topology != gatewayTopologyExactV1WithStage || !stagedProof.stageRunningExact || stagedProof.finalTopologyExact {
		t.Fatal("stage diagnostic did not distinguish exact staged topology")
	}

	finalJournal := journal
	finalJournal.Resources = gatewayV2IdentityTestBoundResources(t)
	finalJournal.Resources.StageContainerID = strings.Repeat("c", 64)
	finalJournal.Phase = gatewayPhaseV2Serving
	final := gatewayV2IdentityTestObservation(t, source, state, finalJournal, gatewayTopologyExactFinalV2)
	finalProof := liveGatewayV2OperationProofs(request, source, state, finalJournal, final)
	if finalProof.topology != gatewayTopologyExactFinalV2 || !finalProof.finalTopologyExact || finalProof.stageRunningExact {
		t.Fatal("transfer diagnostic did not distinguish exact final topology")
	}
}

const liveGatewayV2TraceMaximumEvents = 256

type liveGatewayV2TracePhase string

const (
	liveGatewayV2TracePhasePreparation liveGatewayV2TracePhase = "preparation"
	liveGatewayV2TracePhaseUnknown     liveGatewayV2TracePhase = "unknown"
)

type liveGatewayV2TraceStep string

const (
	liveGatewayV2TraceStageObserveTopology           liveGatewayV2TraceStep = "stage_observe_topology"
	liveGatewayV2TraceStageObserveRecovery           liveGatewayV2TraceStep = "stage_observe_recovery"
	liveGatewayV2TraceStageHostPreflight             liveGatewayV2TraceStep = "stage_host_preflight"
	liveGatewayV2TraceStageSelectedInterface         liveGatewayV2TraceStep = "stage_selected_interface"
	liveGatewayV2TraceStagePinnedImage               liveGatewayV2TraceStep = "stage_pinned_image"
	liveGatewayV2TraceStageCreateIngressNetwork      liveGatewayV2TraceStep = "stage_create_ingress_network"
	liveGatewayV2TraceStageCreateConfigVolume        liveGatewayV2TraceStep = "stage_create_config_volume"
	liveGatewayV2TraceStageCreateDataVolume          liveGatewayV2TraceStep = "stage_create_data_volume"
	liveGatewayV2TraceStageCreateUnknownVolume       liveGatewayV2TraceStep = "stage_create_unknown_volume"
	liveGatewayV2TraceStageCreateContainer           liveGatewayV2TraceStep = "stage_create_container"
	liveGatewayV2TraceStageCreateContainerDetail     liveGatewayV2TraceStep = "stage_create_container_detail"
	liveGatewayV2TraceStageCopyConfig                liveGatewayV2TraceStep = "stage_copy_config"
	liveGatewayV2TraceStageReadRestartConfig         liveGatewayV2TraceStep = "stage_read_restart_config"
	liveGatewayV2TraceStageAttestStopped             liveGatewayV2TraceStep = "stage_attest_stopped"
	liveGatewayV2TraceStageAttestStoppedDetail       liveGatewayV2TraceStep = "stage_attest_stopped_detail"
	liveGatewayV2TraceStageStabilityDetail           liveGatewayV2TraceStep = "stage_stability_detail"
	liveGatewayV2TraceStageAttestStoppedCompensation liveGatewayV2TraceStep = "stage_attest_stopped_compensation"
	liveGatewayV2TraceStageStart                     liveGatewayV2TraceStep = "stage_start"
	liveGatewayV2TraceStageStartDetail               liveGatewayV2TraceStep = "stage_start_detail"
	liveGatewayV2TraceStageStartFailureShape         liveGatewayV2TraceStep = "stage_start_failure_shape"
	liveGatewayV2TraceStageStop                      liveGatewayV2TraceStep = "stage_stop"
	liveGatewayV2TraceStageRemove                    liveGatewayV2TraceStep = "stage_remove"
	liveGatewayV2TraceTransferObserveTopology        liveGatewayV2TraceStep = "transfer_observe_topology"
	liveGatewayV2TraceTransferObserveRecovery        liveGatewayV2TraceStep = "transfer_observe_recovery"
	liveGatewayV2TraceTransferProveFinalRoutes       liveGatewayV2TraceStep = "transfer_prove_final_routes"
	liveGatewayV2TraceTransferSelectedInterface      liveGatewayV2TraceStep = "transfer_selected_interface"
	liveGatewayV2TraceTransferCopyFinalConfig        liveGatewayV2TraceStep = "transfer_copy_final_config"
	liveGatewayV2TraceTransferReadFinalConfig        liveGatewayV2TraceStep = "transfer_read_final_config"
	liveGatewayV2TraceTransferStopStage              liveGatewayV2TraceStep = "transfer_stop_stage"
	liveGatewayV2TraceTransferRemoveStage            liveGatewayV2TraceStep = "transfer_remove_stage"
	liveGatewayV2TraceTransferCreateFinal            liveGatewayV2TraceStep = "transfer_create_final"
	liveGatewayV2TraceTransferStopV1                 liveGatewayV2TraceStep = "transfer_stop_v1"
	liveGatewayV2TraceTransferStartV1                liveGatewayV2TraceStep = "transfer_start_v1"
	liveGatewayV2TraceTransferStartFinal             liveGatewayV2TraceStep = "transfer_start_final"
	liveGatewayV2TraceTransferStopFinal              liveGatewayV2TraceStep = "transfer_stop_final"
	liveGatewayV2TraceTransferRemoveFinal            liveGatewayV2TraceStep = "transfer_remove_final"
)

type liveGatewayV2TraceOutcome string

const (
	liveGatewayV2TraceOK                              liveGatewayV2TraceOutcome = "ok"
	liveGatewayV2TraceError                           liveGatewayV2TraceOutcome = "error"
	liveGatewayV2TraceTrue                            liveGatewayV2TraceOutcome = "true"
	liveGatewayV2TraceFalse                           liveGatewayV2TraceOutcome = "false"
	liveGatewayV2TraceConfigMatch                     liveGatewayV2TraceOutcome = "config_match"
	liveGatewayV2TraceConfigMismatch                  liveGatewayV2TraceOutcome = "config_mismatch"
	liveGatewayV2TraceTopologyUnknown                 liveGatewayV2TraceOutcome = "topology_unknown_or_identity_drift"
	liveGatewayV2TraceTopologyExactV1                 liveGatewayV2TraceOutcome = "topology_exact_v1"
	liveGatewayV2TraceTopologyExactV1WithStage        liveGatewayV2TraceOutcome = "topology_exact_v1_with_stage"
	liveGatewayV2TraceTopologyExactFinalV2            liveGatewayV2TraceOutcome = "topology_exact_final_v2"
	liveGatewayV2TraceRecoveryUnknown                 liveGatewayV2TraceOutcome = "recovery_unknown_or_identity_drift"
	liveGatewayV2TraceRecoveryStagePartial            liveGatewayV2TraceOutcome = "recovery_stage_partial_infrastructure"
	liveGatewayV2TraceRecoveryStageStopped            liveGatewayV2TraceOutcome = "recovery_stage_stopped"
	liveGatewayV2TraceRecoveryTransferV1StageStopped  liveGatewayV2TraceOutcome = "recovery_transfer_v1_serving_stage_stopped"
	liveGatewayV2TraceRecoveryTransferV1NoContainers  liveGatewayV2TraceOutcome = "recovery_transfer_v1_serving_no_containers"
	liveGatewayV2TraceRecoveryTransferV1FinalStopped  liveGatewayV2TraceOutcome = "recovery_transfer_v1_serving_final_stopped"
	liveGatewayV2TraceRecoveryTransferV1Stopped       liveGatewayV2TraceOutcome = "recovery_transfer_v1_stopped_stage_running"
	liveGatewayV2TraceRecoveryTransferBothStopped     liveGatewayV2TraceOutcome = "recovery_transfer_v1_and_stage_stopped"
	liveGatewayV2TraceRecoveryTransferNoContainers    liveGatewayV2TraceOutcome = "recovery_transfer_v1_stopped_no_containers"
	liveGatewayV2TraceRecoveryTransferStoppedFinal    liveGatewayV2TraceOutcome = "recovery_transfer_v1_stopped_final_stopped"
	liveGatewayV2TraceCreateArgsRejected              liveGatewayV2TraceOutcome = "create_args_rejected"
	liveGatewayV2TraceCreateDockerCommandFailed       liveGatewayV2TraceOutcome = "docker_command_failed"
	liveGatewayV2TraceCreateInspectError              liveGatewayV2TraceOutcome = "inspect_error"
	liveGatewayV2TraceCreateNotFound                  liveGatewayV2TraceOutcome = "container_not_found"
	liveGatewayV2TraceCreateValidationFailed          liveGatewayV2TraceOutcome = "stopped_container_validation_failed"
	liveGatewayV2TraceCreateValidationPassed          liveGatewayV2TraceOutcome = "stopped_container_validation_passed"
	liveGatewayV2TraceCreateValidationUnclassified    liveGatewayV2TraceOutcome = "unclassified_validation_failure"
	liveGatewayV2TraceCreatePredicateRuntimeState     liveGatewayV2TraceOutcome = "failed_predicate_runtime_state"
	liveGatewayV2TraceCreatePredicateIdentity         liveGatewayV2TraceOutcome = "failed_predicate_identity"
	liveGatewayV2TraceCreatePredicateExecution        liveGatewayV2TraceOutcome = "failed_predicate_execution"
	liveGatewayV2TraceCreatePredicateIsolation        liveGatewayV2TraceOutcome = "failed_predicate_isolation"
	liveGatewayV2TraceCreatePredicateLimits           liveGatewayV2TraceOutcome = "failed_predicate_limits"
	liveGatewayV2TraceCreatePredicateLogging          liveGatewayV2TraceOutcome = "failed_predicate_logging"
	liveGatewayV2TraceCreatePredicateRestart          liveGatewayV2TraceOutcome = "failed_predicate_restart_policy"
	liveGatewayV2TraceCreatePredicateEntrypoint       liveGatewayV2TraceOutcome = "failed_predicate_entrypoint_and_command"
	liveGatewayV2TraceCreatePredicateUlimit           liveGatewayV2TraceOutcome = "failed_predicate_ulimit"
	liveGatewayV2TraceCreatePredicateLabels           liveGatewayV2TraceOutcome = "failed_predicate_labels"
	liveGatewayV2TraceCreatePredicateMounts           liveGatewayV2TraceOutcome = "failed_predicate_mounts"
	liveGatewayV2TraceCreatePredicateConfiguredPorts  liveGatewayV2TraceOutcome = "failed_predicate_configured_ports"
	liveGatewayV2TraceCreatePredicateEffectivePorts   liveGatewayV2TraceOutcome = "failed_predicate_effective_ports_absent"
	liveGatewayV2TraceCreatePredicateExpectedNetworks liveGatewayV2TraceOutcome = "failed_predicate_expected_networks"
	liveGatewayV2TraceCreatePredicateStoppedNetworks  liveGatewayV2TraceOutcome = "failed_predicate_stopped_networks"
	liveGatewayV2TraceCreateLabelsMissingRequired     liveGatewayV2TraceOutcome = "labels_missing_required_key"
	liveGatewayV2TraceCreateLabelsRequiredMismatch    liveGatewayV2TraceOutcome = "labels_required_value_mismatch"
	liveGatewayV2TraceCreateLabelsExtraRig            liveGatewayV2TraceOutcome = "labels_extra_rig_key"
	liveGatewayV2TraceCreateLabelsExtraForeign        liveGatewayV2TraceOutcome = "labels_extra_non_rig_key"
	liveGatewayV2TraceCreateNetworksMissing           liveGatewayV2TraceOutcome = "stopped_networks_missing_expected_key"
	liveGatewayV2TraceCreateNetworksExtra             liveGatewayV2TraceOutcome = "stopped_networks_extra_key"
	liveGatewayV2TraceCreateNetworksIDAbsent          liveGatewayV2TraceOutcome = "stopped_networks_network_id_absent"
	liveGatewayV2TraceCreateNetworksIDMalformed       liveGatewayV2TraceOutcome = "stopped_networks_network_id_malformed_nonempty"
	liveGatewayV2TraceCreateNetworksIDMismatch        liveGatewayV2TraceOutcome = "stopped_networks_ingress_network_id_mismatch"
	liveGatewayV2TraceCreateNetworksEndpointPresent   liveGatewayV2TraceOutcome = "stopped_networks_endpoint_id_present"
	liveGatewayV2TraceCreateNetworksAddressPresent    liveGatewayV2TraceOutcome = "stopped_networks_ip_address_present"
	liveGatewayV2TraceCreateNetworksIPv6Present       liveGatewayV2TraceOutcome = "stopped_networks_ipv6_gateway_present"
	liveGatewayV2TraceCreateNetworksPriorityMismatch  liveGatewayV2TraceOutcome = "stopped_networks_gateway_priority_mismatch"
	liveGatewayV2TraceCreateNetworksIPAMMissing       liveGatewayV2TraceOutcome = "stopped_networks_ipam_missing"
	liveGatewayV2TraceCreateNetworksIPv4Mismatch      liveGatewayV2TraceOutcome = "stopped_networks_requested_ipv4_mismatch"
	liveGatewayV2TraceCreateNetworksIPAMIPv6Present   liveGatewayV2TraceOutcome = "stopped_networks_ipam_ipv6_present"
	liveGatewayV2TraceCreateNetworksUnexpectedIPAM    liveGatewayV2TraceOutcome = "stopped_networks_unexpected_application_ipam"
	liveGatewayV2TraceStartCommandSucceeded           liveGatewayV2TraceOutcome = "start_command_succeeded"
	liveGatewayV2TraceStartValidationRejected         liveGatewayV2TraceOutcome = "start_validation_rejected"
	liveGatewayV2TraceStartRunnerUnavailable          liveGatewayV2TraceOutcome = "start_runner_unavailable"
	liveGatewayV2TraceStartUnexpectedCommand          liveGatewayV2TraceOutcome = "start_unexpected_command"
	liveGatewayV2TraceStartOutputTruncated            liveGatewayV2TraceOutcome = "start_output_truncated"
	liveGatewayV2TraceStartCancelled                  liveGatewayV2TraceOutcome = "start_cancelled_or_timed_out"
	liveGatewayV2TraceStartTerminationFailed          liveGatewayV2TraceOutcome = "start_termination_failed"
	liveGatewayV2TraceStartHostPortConflict           liveGatewayV2TraceOutcome = "start_host_port_conflict"
	liveGatewayV2TraceStartSelectedAddressUnavailable liveGatewayV2TraceOutcome = "start_selected_address_unavailable"
	liveGatewayV2TraceStartNetworkUnavailable         liveGatewayV2TraceOutcome = "start_network_unavailable"
	liveGatewayV2TraceStartContainerMissing           liveGatewayV2TraceOutcome = "start_container_missing"
	liveGatewayV2TraceStartPermissionDenied           liveGatewayV2TraceOutcome = "start_permission_denied"
	liveGatewayV2TraceStartRuntimeMountFailed         liveGatewayV2TraceOutcome = "start_runtime_mount_failed"
	liveGatewayV2TraceStartRuntimeExecFailed          liveGatewayV2TraceOutcome = "start_runtime_exec_failed"
	liveGatewayV2TraceStartRuntimeSecurityFailed      liveGatewayV2TraceOutcome = "start_runtime_security_failed"
	liveGatewayV2TraceStartRuntimeCgroupFailed        liveGatewayV2TraceOutcome = "start_runtime_cgroup_failed"
	liveGatewayV2TraceStartRuntimeNamespaceFailed     liveGatewayV2TraceOutcome = "start_runtime_namespace_failed"
	liveGatewayV2TraceStartRuntimeCreateFailed        liveGatewayV2TraceOutcome = "start_runtime_create_failed"
	liveGatewayV2TraceStartExternalConnectivityFailed liveGatewayV2TraceOutcome = "start_external_connectivity_failed"
	liveGatewayV2TraceStartCommandFailedOther         liveGatewayV2TraceOutcome = "start_command_failed_other"
)

const (
	liveGatewayV2TraceStartShapeOCI           liveGatewayV2TraceOutcome = "start_shape_oci_runtime"
	liveGatewayV2TraceStartShapeRunc          liveGatewayV2TraceOutcome = "start_shape_runc_create"
	liveGatewayV2TraceStartShapeProcessInit   liveGatewayV2TraceOutcome = "start_shape_process_init"
	liveGatewayV2TraceStartShapeMissingPath   liveGatewayV2TraceOutcome = "start_shape_missing_path"
	liveGatewayV2TraceStartShapeNotDirectory  liveGatewayV2TraceOutcome = "start_shape_not_directory"
	liveGatewayV2TraceStartShapeInvalidArg    liveGatewayV2TraceOutcome = "start_shape_invalid_argument"
	liveGatewayV2TraceStartShapeReadOnly      liveGatewayV2TraceOutcome = "start_shape_read_only_filesystem"
	liveGatewayV2TraceStartShapeResourceBusy  liveGatewayV2TraceOutcome = "start_shape_resource_unavailable"
	liveGatewayV2TraceStartShapeNetwork       liveGatewayV2TraceOutcome = "start_shape_network_or_endpoint"
	liveGatewayV2TraceStartShapePort          liveGatewayV2TraceOutcome = "start_shape_port_or_address"
	liveGatewayV2TraceStartShapeExec          liveGatewayV2TraceOutcome = "start_shape_exec_or_executable"
	liveGatewayV2TraceStartShapeMount         liveGatewayV2TraceOutcome = "start_shape_mount_or_rootfs"
	liveGatewayV2TraceStartShapePermission    liveGatewayV2TraceOutcome = "start_shape_permission"
	liveGatewayV2TraceStartShapeProcSys       liveGatewayV2TraceOutcome = "start_shape_proc_sys"
	liveGatewayV2TraceStartShapePortSysctl    liveGatewayV2TraceOutcome = "start_shape_unprivileged_port_sysctl"
	liveGatewayV2TraceStartShapeSysctl        liveGatewayV2TraceOutcome = "start_shape_sysctl"
	liveGatewayV2TraceStartShapeRlimit        liveGatewayV2TraceOutcome = "start_shape_rlimit"
	liveGatewayV2TraceStartShapePivotRoot     liveGatewayV2TraceOutcome = "start_shape_pivot_root"
	liveGatewayV2TraceStartShapeHostname      liveGatewayV2TraceOutcome = "start_shape_hostname"
	liveGatewayV2TraceStartShapeCapability    liveGatewayV2TraceOutcome = "start_shape_capability"
	liveGatewayV2TraceStartShapeCgroup        liveGatewayV2TraceOutcome = "start_shape_cgroup"
	liveGatewayV2TraceStartShapeNoKnownDetail liveGatewayV2TraceOutcome = "start_shape_no_known_detail"
)

const (
	liveGatewayV2TraceAttestInspectError               liveGatewayV2TraceOutcome = "attest_inspect_error"
	liveGatewayV2TraceAttestUnclassified               liveGatewayV2TraceOutcome = "attest_unclassified_failure"
	liveGatewayV2TraceAttestPhase                      liveGatewayV2TraceOutcome = "failed_predicate_attest_phase"
	liveGatewayV2TraceAttestTopologyInputs             liveGatewayV2TraceOutcome = "failed_predicate_attest_topology_inputs"
	liveGatewayV2TraceAttestPinnedImage                liveGatewayV2TraceOutcome = "failed_predicate_attest_pinned_image"
	liveGatewayV2TraceAttestResourceJournalBinding     liveGatewayV2TraceOutcome = "failed_predicate_attest_resource_journal_binding"
	liveGatewayV2TraceAttestV1Base                     liveGatewayV2TraceOutcome = "failed_predicate_attest_v1_base"
	liveGatewayV2TraceAttestV1Serving                  liveGatewayV2TraceOutcome = "failed_predicate_attest_v1_serving"
	liveGatewayV2TraceAttestV1Stability                liveGatewayV2TraceOutcome = "failed_predicate_attest_v1_stability"
	liveGatewayV2TraceAttestV1ResourceStability        liveGatewayV2TraceOutcome = "failed_predicate_attest_v1_resource_stability"
	liveGatewayV2TraceAttestV1EndpointIdentity         liveGatewayV2TraceOutcome = "failed_predicate_attest_v1_endpoint_identity"
	liveGatewayV2TraceAttestV2ResourceStability        liveGatewayV2TraceOutcome = "failed_predicate_attest_v2_resource_stability"
	liveGatewayV2TraceAttestStageStability             liveGatewayV2TraceOutcome = "failed_predicate_attest_stage_stability"
	liveGatewayV2TraceAttestFinalStability             liveGatewayV2TraceOutcome = "failed_predicate_attest_final_stability"
	liveGatewayV2TraceAttestOwnedInventoryStability    liveGatewayV2TraceOutcome = "failed_predicate_attest_owned_inventory_stability"
	liveGatewayV2TraceAttestStageConfigBuild           liveGatewayV2TraceOutcome = "failed_predicate_attest_stage_config_build"
	liveGatewayV2TraceAttestStageInfrastructure        liveGatewayV2TraceOutcome = "failed_predicate_attest_stage_infrastructure"
	liveGatewayV2TraceAttestStageBinding               liveGatewayV2TraceOutcome = "failed_predicate_attest_stage_binding"
	liveGatewayV2TraceAttestFinalAbsent                liveGatewayV2TraceOutcome = "failed_predicate_attest_final_absent"
	liveGatewayV2TraceAttestOwnedContainers            liveGatewayV2TraceOutcome = "failed_predicate_attest_owned_containers"
	liveGatewayV2TraceAttestOwnedVolumes               liveGatewayV2TraceOutcome = "failed_predicate_attest_owned_volumes"
	liveGatewayV2TraceAttestOwnedNetworks              liveGatewayV2TraceOutcome = "failed_predicate_attest_owned_networks"
	liveGatewayV2TraceAttestVolumes                    liveGatewayV2TraceOutcome = "failed_predicate_attest_volumes"
	liveGatewayV2TraceAttestStoppedContainer           liveGatewayV2TraceOutcome = "failed_predicate_attest_stopped_container"
	liveGatewayV2TraceAttestStoppedIngressNetwork      liveGatewayV2TraceOutcome = "failed_predicate_attest_stopped_ingress_network"
	liveGatewayV2TraceAttestIngressConfigured          liveGatewayV2TraceOutcome = "failed_predicate_attest_ingress_configured"
	liveGatewayV2TraceAttestIngressInspectedID         liveGatewayV2TraceOutcome = "failed_predicate_attest_ingress_inspected_id"
	liveGatewayV2TraceAttestIngressJournalID           liveGatewayV2TraceOutcome = "failed_predicate_attest_ingress_journal_id"
	liveGatewayV2TraceAttestIngressConfiguredReference liveGatewayV2TraceOutcome = "failed_predicate_attest_ingress_configured_reference"
	liveGatewayV2TraceAttestIngressResourceIdentity    liveGatewayV2TraceOutcome = "failed_predicate_attest_ingress_resource_identity"
	liveGatewayV2TraceAttestStageLiveConfigAbsent      liveGatewayV2TraceOutcome = "failed_predicate_attest_stage_live_config_absent"
	liveGatewayV2TraceAttestStageRestartConfig         liveGatewayV2TraceOutcome = "failed_predicate_attest_stage_restart_config"
	liveGatewayV2TraceAttestApplicationNetworksAbsent  liveGatewayV2TraceOutcome = "failed_predicate_attest_application_networks_absent"
)

const (
	liveGatewayV2TraceStabilityInspectCaptureIncomplete liveGatewayV2TraceOutcome = "stage_stability_inspect_capture_incomplete"
	liveGatewayV2TraceStabilityConfigCaptureIncomplete  liveGatewayV2TraceOutcome = "stage_stability_restart_config_capture_incomplete"
	liveGatewayV2TraceStabilityContainerIdentity        liveGatewayV2TraceOutcome = "stage_stability_container_identity_changed"
	liveGatewayV2TraceStabilityContainerState           liveGatewayV2TraceOutcome = "stage_stability_container_state_changed"
	liveGatewayV2TraceStabilityContainerExecution       liveGatewayV2TraceOutcome = "stage_stability_container_execution_changed"
	liveGatewayV2TraceStabilityContainerIsolation       liveGatewayV2TraceOutcome = "stage_stability_container_isolation_changed"
	liveGatewayV2TraceStabilityContainerResources       liveGatewayV2TraceOutcome = "stage_stability_container_resources_changed"
	liveGatewayV2TraceStabilityContainerMounts          liveGatewayV2TraceOutcome = "stage_stability_container_mounts_changed"
	liveGatewayV2TraceStabilityContainerMountOrderOnly  liveGatewayV2TraceOutcome = "stage_stability_container_mount_order_only"
	liveGatewayV2TraceStabilityContainerMountContent    liveGatewayV2TraceOutcome = "stage_stability_container_mount_content_changed"
	liveGatewayV2TraceStabilityContainerMemory          liveGatewayV2TraceOutcome = "stage_stability_container_memory_changed"
	liveGatewayV2TraceStabilityContainerMemorySwap      liveGatewayV2TraceOutcome = "stage_stability_container_memory_swap_changed"
	liveGatewayV2TraceStabilityContainerNanoCPUs        liveGatewayV2TraceOutcome = "stage_stability_container_nano_cpus_changed"
	liveGatewayV2TraceStabilityContainerPIDsLimit       liveGatewayV2TraceOutcome = "stage_stability_container_pids_limit_changed"
	liveGatewayV2TraceStabilityContainerUlimits         liveGatewayV2TraceOutcome = "stage_stability_container_ulimits_changed"
	liveGatewayV2TraceStabilityContainerLogging         liveGatewayV2TraceOutcome = "stage_stability_container_logging_changed"
	liveGatewayV2TraceStabilityContainerLabels          liveGatewayV2TraceOutcome = "stage_stability_container_labels_changed"
	liveGatewayV2TraceStabilityContainerPorts           liveGatewayV2TraceOutcome = "stage_stability_container_ports_changed"
	liveGatewayV2TraceStabilityContainerNetworks        liveGatewayV2TraceOutcome = "stage_stability_container_networks_changed"
	liveGatewayV2TraceStabilityRuntimeState             liveGatewayV2TraceOutcome = "stage_stability_runtime_state_changed"
	liveGatewayV2TraceStabilityRuntimePorts             liveGatewayV2TraceOutcome = "stage_stability_runtime_ports_changed"
	liveGatewayV2TraceStabilityRuntimeNetworks          liveGatewayV2TraceOutcome = "stage_stability_runtime_networks_changed"
	liveGatewayV2TraceStabilityRestartConfig            liveGatewayV2TraceOutcome = "stage_stability_restart_config_changed"
	liveGatewayV2TraceStabilityUnclassified             liveGatewayV2TraceOutcome = "stage_stability_unclassified"
)

type liveGatewayV2TraceEvent struct {
	phase   liveGatewayV2TracePhase
	step    liveGatewayV2TraceStep
	outcome liveGatewayV2TraceOutcome
}

type liveGatewayV2OperationTrace struct {
	mu        sync.Mutex
	phase     liveGatewayV2TracePhase
	events    []liveGatewayV2TraceEvent
	truncated bool
}

func liveGatewayV2InstallOperationTrace(fixture *liveGatewayV2Fixture) *liveGatewayV2OperationTrace {
	trace := &liveGatewayV2OperationTrace{phase: liveGatewayV2TracePhasePreparation}
	if fixture == nil || fixture.ingress == nil {
		return trace
	}
	fixture.ingress.gatewayV2UpgradeDriver = liveGatewayV2TracingUpgradeDriver{
		gatewayV2UpgradeDriver: managerGatewayV2UpgradeDriver{manager: fixture.ingress}, manager: fixture.ingress, trace: trace,
	}
	fixture.ingress.gatewayV2TransferDriver = liveGatewayV2TracingTransferDriver{
		gatewayV2TransferDriver: managerGatewayV2TransferDriver{manager: fixture.ingress}, trace: trace,
	}
	return trace
}

func (r *liveGatewayV2OperationTrace) recordAt(step liveGatewayV2TraceStep, phase gatewayMigrationPhase,
	outcome liveGatewayV2TraceOutcome,
) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = liveGatewayV2ClosedTracePhase(phase)
	r.appendLocked(liveGatewayV2TraceEvent{phase: r.phase, step: step, outcome: outcome})
}

func (r *liveGatewayV2OperationTrace) recordCurrent(step liveGatewayV2TraceStep, outcome liveGatewayV2TraceOutcome) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLocked(liveGatewayV2TraceEvent{phase: r.phase, step: step, outcome: outcome})
}

func (r *liveGatewayV2OperationTrace) appendLocked(event liveGatewayV2TraceEvent) {
	if len(r.events) == liveGatewayV2TraceMaximumEvents {
		r.truncated = true
		return
	}
	r.events = append(r.events, event)
}

func (r *liveGatewayV2OperationTrace) snapshot() ([]liveGatewayV2TraceEvent, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]liveGatewayV2TraceEvent(nil), r.events...), r.truncated
}

func (r *liveGatewayV2OperationTrace) log(t *testing.T) {
	t.Helper()
	events, truncated := r.snapshot()
	if len(events) == 0 {
		t.Log("gateway-v2 protected operation trace: events_available=false")
		return
	}
	for index, event := range events {
		t.Logf("gateway-v2 protected operation trace: sequence=%d phase=%s step=%s outcome=%s",
			index+1, event.phase, event.step, event.outcome)
	}
	if truncated {
		t.Log("gateway-v2 protected operation trace: truncated=true")
	}
}

func liveGatewayV2ClosedTracePhase(phase gatewayMigrationPhase) liveGatewayV2TracePhase {
	switch phase {
	case gatewayPhasePrepared, gatewayPhaseStageIntent, gatewayPhaseStaged, gatewayPhaseTransferIntent,
		gatewayPhaseV2Serving, gatewayPhaseCommitted, gatewayPhaseRollbackIntent, gatewayPhaseRolledBack,
		gatewayPhaseUncertain:
		return liveGatewayV2TracePhase(phase)
	default:
		return liveGatewayV2TracePhaseUnknown
	}
}

func liveGatewayV2ErrorOutcome(err error) liveGatewayV2TraceOutcome {
	if err != nil {
		return liveGatewayV2TraceError
	}
	return liveGatewayV2TraceOK
}

func liveGatewayV2BoolOutcome(value bool) liveGatewayV2TraceOutcome {
	if value {
		return liveGatewayV2TraceTrue
	}
	return liveGatewayV2TraceFalse
}

func liveGatewayV2TopologyOutcome(topology gatewayObservedTopology) liveGatewayV2TraceOutcome {
	switch topology {
	case gatewayTopologyExactV1Only:
		return liveGatewayV2TraceTopologyExactV1
	case gatewayTopologyExactV1WithStage:
		return liveGatewayV2TraceTopologyExactV1WithStage
	case gatewayTopologyExactFinalV2:
		return liveGatewayV2TraceTopologyExactFinalV2
	default:
		return liveGatewayV2TraceTopologyUnknown
	}
}

func liveGatewayV2RecoveryOutcome(recovery gatewayV2RecoveryTopology) liveGatewayV2TraceOutcome {
	switch recovery {
	case gatewayV2RecoveryStageIntentPartialInfrastructure:
		return liveGatewayV2TraceRecoveryStagePartial
	case gatewayV2RecoveryStageIntentStoppedStage:
		return liveGatewayV2TraceRecoveryStageStopped
	case gatewayV2RecoveryTransferIntentV1ServingStoppedStage:
		return liveGatewayV2TraceRecoveryTransferV1StageStopped
	case gatewayV2RecoveryTransferIntentV1ServingNoContainers:
		return liveGatewayV2TraceRecoveryTransferV1NoContainers
	case gatewayV2RecoveryTransferIntentV1ServingStoppedFinal:
		return liveGatewayV2TraceRecoveryTransferV1FinalStopped
	case gatewayV2RecoveryTransferIntentStoppedV1:
		return liveGatewayV2TraceRecoveryTransferV1Stopped
	case gatewayV2RecoveryTransferIntentV1StoppedStage:
		return liveGatewayV2TraceRecoveryTransferBothStopped
	case gatewayV2RecoveryTransferIntentNoContainers:
		return liveGatewayV2TraceRecoveryTransferNoContainers
	case gatewayV2RecoveryTransferIntentStoppedFinal:
		return liveGatewayV2TraceRecoveryTransferStoppedFinal
	default:
		return liveGatewayV2TraceRecoveryUnknown
	}
}

type liveGatewayV2TracingUpgradeDriver struct {
	gatewayV2UpgradeDriver
	manager *Manager
	trace   *liveGatewayV2OperationTrace
}

func (d liveGatewayV2TracingUpgradeDriver) observeTopology(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) gatewayObservedTopology {
	result := d.gatewayV2UpgradeDriver.observeTopology(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceStageObserveTopology, journal.Phase, liveGatewayV2TopologyOutcome(result))
	return result
}

func (d liveGatewayV2TracingUpgradeDriver) observeRecovery(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) gatewayV2RecoveryTopology {
	result := d.gatewayV2UpgradeDriver.observeRecovery(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceStageObserveRecovery, journal.Phase, liveGatewayV2RecoveryOutcome(result))
	return result
}

func (d liveGatewayV2TracingUpgradeDriver) hostPreflight(ctx context.Context, profile gatewayProfileBinding,
	plan gatewayV2NetworkPlan,
) error {
	err := d.gatewayV2UpgradeDriver.hostPreflight(ctx, profile, plan)
	d.trace.recordCurrent(liveGatewayV2TraceStageHostPreflight, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingUpgradeDriver) selectedInterfacePreflight(profile gatewayProfileBinding) error {
	err := d.gatewayV2UpgradeDriver.selectedInterfacePreflight(profile)
	d.trace.recordCurrent(liveGatewayV2TraceStageSelectedInterface, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingUpgradeDriver) pinnedImage(ctx context.Context) (string, error) {
	id, err := d.gatewayV2UpgradeDriver.pinnedImage(ctx)
	d.trace.recordCurrent(liveGatewayV2TraceStagePinnedImage, liveGatewayV2ErrorOutcome(err))
	return id, err
}

func (d liveGatewayV2TracingUpgradeDriver) createIngressNetwork(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (string, error) {
	id, err := d.gatewayV2UpgradeDriver.createIngressNetwork(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceStageCreateIngressNetwork, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return id, err
}

func (d liveGatewayV2TracingUpgradeDriver) createVolume(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, role string,
) (gatewayV1VolumeIdentity, error) {
	step := liveGatewayV2TraceStageCreateUnknownVolume
	switch role {
	case gatewayV2ConfigVolumeRole:
		step = liveGatewayV2TraceStageCreateConfigVolume
	case gatewayV2DataVolumeRole:
		step = liveGatewayV2TraceStageCreateDataVolume
	}
	identity, err := d.gatewayV2UpgradeDriver.createVolume(ctx, state, journal, role)
	d.trace.recordAt(step, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return identity, err
}

func (d liveGatewayV2TracingUpgradeDriver) createStageContainer(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (string, error) {
	args, err := gatewayV2ContainerCreateArgs(state, journal, gatewayV2StageContainerRole, "sha256:"+journal.Resources.ImageID)
	if err != nil {
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateArgsRejected)
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceError)
		return "", err
	}
	if d.manager == nil {
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateInspectError)
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceError)
		return "", gatewayV2StageError(ctx)
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	clearResult(&result)
	if err != nil {
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateDockerCommandFailed)
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceError)
		return "", err
	}
	container, runtime, found, err := d.manager.inspectNamedGatewayContainer(ctx, state.Identity.StageContainer)
	if err != nil {
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateInspectError)
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceError)
		return "", gatewayV2StageError(ctx)
	}
	if !found {
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateNotFound)
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceError)
		return "", gatewayV2StageError(ctx)
	}
	imageID := "sha256:" + journal.Resources.ImageID
	if !validGatewayV2StoppedContainer(state, journal, container, runtime, true, gatewayV2StageContainerRole, imageID) {
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateValidationFailed)
		failures := liveGatewayV2StoppedContainerFailureOutcomes(state, journal, container, runtime,
			gatewayV2StageContainerRole, imageID)
		if len(failures) == 0 {
			failures = append(failures, liveGatewayV2TraceCreateValidationUnclassified)
		}
		for _, failure := range failures {
			d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, failure)
		}
		d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceError)
		return "", gatewayV2StageError(ctx)
	}
	d.trace.recordAt(liveGatewayV2TraceStageCreateContainerDetail, journal.Phase, liveGatewayV2TraceCreateValidationPassed)
	d.trace.recordAt(liveGatewayV2TraceStageCreateContainer, journal.Phase, liveGatewayV2TraceOK)
	return container.ID, nil
}

func liveGatewayV2StoppedContainerFailureOutcomes(state gatewayV2RouteState, journal gatewayMigrationJournal,
	value caddyInspection, runtime gatewayContainerRuntime, role, imageID string,
) []liveGatewayV2TraceOutcome {
	name, configFilename, restart := state.Identity.StageContainer, state.Identity.StageConfigFilename, gatewayV2StageRestartPolicy
	if role == gatewayV2FinalContainerRole {
		name, configFilename, restart = state.Identity.FinalContainer, state.Identity.ActiveConfigFilename, gatewayV2FinalRestartPolicy
	}
	failures := make([]liveGatewayV2TraceOutcome, 0, 16)
	add := func(failure liveGatewayV2TraceOutcome, valid bool) {
		if !valid {
			failures = append(failures, failure)
		}
	}
	add(liveGatewayV2TraceCreatePredicateRuntimeState, !value.Running && !value.Restarting &&
		validGatewayContainerRuntime(runtime, false) && validContainerID(value.ID) && normalizeID(value.Image) == normalizeID(imageID))
	add(liveGatewayV2TraceCreatePredicateIdentity,
		strings.TrimPrefix(value.Name, "/") == name && value.Hostname == name && value.NetworkMode == state.Identity.IngressNetwork)
	add(liveGatewayV2TraceCreatePredicateExecution, value.User == "1000:1000" && exactGatewayV2Environment(value.Env))
	add(liveGatewayV2TraceCreatePredicateIsolation, value.ReadOnly && !value.Privileged && onlyCaddyCapability(value.CapAdd) &&
		exactFoldSet(value.CapDrop, "ALL") && onlyNoNewPrivileges(value.SecurityOpt) && len(value.Binds) == 0 && len(value.Tmpfs) == 0)
	add(liveGatewayV2TraceCreatePredicateLimits, value.Memory == 268435456 && value.MemorySwap == 268435456 &&
		value.NanoCPUs == 1_000_000_000 && value.PIDsLimit == 128)
	add(liveGatewayV2TraceCreatePredicateLogging, value.LogType == "local" && len(value.LogConfig) == 2 &&
		value.LogConfig["max-size"] == "10m" && value.LogConfig["max-file"] == "3")
	add(liveGatewayV2TraceCreatePredicateRestart, value.Restart == restart)
	add(liveGatewayV2TraceCreatePredicateEntrypoint, len(value.Entrypoint) == 1 && value.Entrypoint[0] == caddyExecutable &&
		len(value.Cmd) == 3 && value.Cmd[0] == "run" && value.Cmd[1] == "--config" && value.Cmd[2] == "/config/"+configFilename)
	add(liveGatewayV2TraceCreatePredicateUlimit,
		len(value.Ulimits) == 1 && value.Ulimits[0] == (ulimitInspection{Name: "nofile", Hard: 1024, Soft: 1024}))
	expectedLabels := gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, role, true)
	labelsValid := validGatewayV2ContainerLabels(value.Labels, expectedLabels)
	add(liveGatewayV2TraceCreatePredicateLabels, labelsValid)
	if !labelsValid {
		failures = append(failures, liveGatewayV2LabelFailureOutcomes(value.Labels, expectedLabels)...)
	}
	add(liveGatewayV2TraceCreatePredicateMounts, validGatewayV2Mounts(value.Mounts, state.Identity))
	add(liveGatewayV2TraceCreatePredicateConfiguredPorts, validGatewayV2PortBindings(value.PortBindings, state, journal, role))
	add(liveGatewayV2TraceCreatePredicateEffectivePorts, !gatewayV2HasEffectivePortBinding(runtime.EffectivePortBindings))
	expectedNetworks, validNetworks := gatewayV2ExpectedContainerNetworks(state, role)
	add(liveGatewayV2TraceCreatePredicateExpectedNetworks, validNetworks)
	stoppedNetworksValid := validNetworks &&
		validGatewayV2StoppedContainerNetworks(state, journal, role, expectedNetworks, runtime.ConfiguredNetworks)
	add(liveGatewayV2TraceCreatePredicateStoppedNetworks, stoppedNetworksValid)
	if validNetworks && !stoppedNetworksValid {
		failures = append(failures,
			liveGatewayV2StoppedNetworkFailureOutcomes(state, journal, role, expectedNetworks, runtime.ConfiguredNetworks)...)
	}
	return failures
}

func liveGatewayV2LabelFailureOutcomes(actual, expected map[string]string) []liveGatewayV2TraceOutcome {
	missingRequired, requiredMismatch, extraRig, extraForeign := false, false, false, false
	for key, expectedValue := range expected {
		actualValue, exists := actual[key]
		missingRequired = missingRequired || !exists
		requiredMismatch = requiredMismatch || exists && actualValue != expectedValue
	}
	for key := range actual {
		if _, exists := expected[key]; exists {
			continue
		}
		if strings.HasPrefix(key, "io.rig.") {
			extraRig = true
		} else {
			extraForeign = true
		}
	}
	return liveGatewayV2SelectedTraceOutcomes(
		liveGatewayV2TraceSelection{missingRequired, liveGatewayV2TraceCreateLabelsMissingRequired},
		liveGatewayV2TraceSelection{requiredMismatch, liveGatewayV2TraceCreateLabelsRequiredMismatch},
		liveGatewayV2TraceSelection{extraRig, liveGatewayV2TraceCreateLabelsExtraRig},
		liveGatewayV2TraceSelection{extraForeign, liveGatewayV2TraceCreateLabelsExtraForeign},
	)
}

func liveGatewayV2StoppedNetworkFailureOutcomes(state gatewayV2RouteState, journal gatewayMigrationJournal, role string,
	expected map[string]struct{}, actual map[string]gatewayV2ConfiguredNetwork,
) []liveGatewayV2TraceOutcome {
	missing, extra := false, false
	idAbsent, idMalformed, endpointPresent, addressPresent, ipv6Present := false, false, false, false, false
	idMismatch := false
	priorityMismatch, ipamMissing, ipv4Mismatch, ipamIPv6Present, unexpectedIPAM := false, false, false, false, false
	for name := range expected {
		attachment, exists := actual[name]
		if !exists {
			missing = true
			continue
		}
		idAbsent = idAbsent || attachment.NetworkID == ""
		idMalformed = idMalformed || attachment.NetworkID != "" && !validContainerID(attachment.NetworkID)
		endpointPresent = endpointPresent || attachment.EndpointID != ""
		addressPresent = addressPresent || attachment.IPAddress != ""
		ipv6Present = ipv6Present || attachment.IPv6Gateway != ""
		if name == state.Identity.IngressNetwork {
			idMismatch = idMismatch || attachment.NetworkID != "" && validContainerID(attachment.NetworkID) &&
				normalizeID(attachment.NetworkID) != journal.Resources.IngressNetworkID
			priorityMismatch = priorityMismatch || attachment.GwPriority != caddyGatewayPriority
			ipamMissing = ipamMissing || attachment.IPAMConfig == nil
			if attachment.IPAMConfig != nil {
				ipv4Mismatch = ipv4Mismatch || attachment.IPAMConfig.IPv4Address != state.Network.ContainerIPv4
				ipamIPv6Present = ipamIPv6Present || attachment.IPAMConfig.IPv6Address != ""
			}
		} else {
			priorityMismatch = priorityMismatch || role != gatewayV2FinalContainerRole || attachment.GwPriority != 0
			unexpectedIPAM = unexpectedIPAM || attachment.IPAMConfig != nil && *attachment.IPAMConfig != (gatewayV2ConfiguredIPAM{})
		}
	}
	for name := range actual {
		if _, exists := expected[name]; !exists {
			extra = true
		}
	}
	return liveGatewayV2SelectedTraceOutcomes(
		liveGatewayV2TraceSelection{missing, liveGatewayV2TraceCreateNetworksMissing},
		liveGatewayV2TraceSelection{extra, liveGatewayV2TraceCreateNetworksExtra},
		liveGatewayV2TraceSelection{idAbsent, liveGatewayV2TraceCreateNetworksIDAbsent},
		liveGatewayV2TraceSelection{idMalformed, liveGatewayV2TraceCreateNetworksIDMalformed},
		liveGatewayV2TraceSelection{idMismatch, liveGatewayV2TraceCreateNetworksIDMismatch},
		liveGatewayV2TraceSelection{endpointPresent, liveGatewayV2TraceCreateNetworksEndpointPresent},
		liveGatewayV2TraceSelection{addressPresent, liveGatewayV2TraceCreateNetworksAddressPresent},
		liveGatewayV2TraceSelection{ipv6Present, liveGatewayV2TraceCreateNetworksIPv6Present},
		liveGatewayV2TraceSelection{priorityMismatch, liveGatewayV2TraceCreateNetworksPriorityMismatch},
		liveGatewayV2TraceSelection{ipamMissing, liveGatewayV2TraceCreateNetworksIPAMMissing},
		liveGatewayV2TraceSelection{ipv4Mismatch, liveGatewayV2TraceCreateNetworksIPv4Mismatch},
		liveGatewayV2TraceSelection{ipamIPv6Present, liveGatewayV2TraceCreateNetworksIPAMIPv6Present},
		liveGatewayV2TraceSelection{unexpectedIPAM, liveGatewayV2TraceCreateNetworksUnexpectedIPAM},
	)
}

type liveGatewayV2TraceSelection struct {
	selected bool
	outcome  liveGatewayV2TraceOutcome
}

func liveGatewayV2SelectedTraceOutcomes(values ...liveGatewayV2TraceSelection) []liveGatewayV2TraceOutcome {
	outcomes := make([]liveGatewayV2TraceOutcome, 0, len(values))
	for _, value := range values {
		if value.selected {
			outcomes = append(outcomes, value.outcome)
		}
	}
	return outcomes
}

func (d liveGatewayV2TracingUpgradeDriver) copyStageConfig(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, contents []byte,
) error {
	err := d.gatewayV2UpgradeDriver.copyStageConfig(ctx, state, journal, contents)
	d.trace.recordAt(liveGatewayV2TraceStageCopyConfig, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingUpgradeDriver) readStageRestartConfig(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) ([]byte, error) {
	contents, err := d.gatewayV2UpgradeDriver.readStageRestartConfig(ctx, state, journal)
	outcome := liveGatewayV2ErrorOutcome(err)
	if err == nil {
		expected, expectedErr := buildGatewayV2StageConfig(state)
		if expectedErr == nil && sameCaddyConfig(expected, contents) {
			outcome = liveGatewayV2TraceConfigMatch
		} else {
			outcome = liveGatewayV2TraceConfigMismatch
		}
		clear(expected)
	}
	d.trace.recordAt(liveGatewayV2TraceStageReadRestartConfig, journal.Phase, outcome)
	return contents, err
}

type liveGatewayV2StageStabilityCaptureRunner struct {
	inner                 runtimeprocess.CommandRunner
	stageName             string
	stageID               string
	configFilename        string
	inspectAttempts       int
	restartConfigAttempts int
	inspectOutputs        [][]byte
	restartConfigOutputs  [][]byte
}

func (r *liveGatewayV2StageStabilityCaptureRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	result, err := r.inner.Run(ctx, request)
	if r.isStageInspect(request.Args) {
		r.inspectAttempts++
		if err == nil && !result.StdoutTruncated && !result.StderrTruncated && len(r.inspectOutputs) < 2 {
			r.inspectOutputs = append(r.inspectOutputs, append([]byte(nil), result.Stdout...))
		}
	}
	if r.isStageRestartConfigRead(request.Args) {
		r.restartConfigAttempts++
		if err == nil && !result.StdoutTruncated && !result.StderrTruncated && len(r.restartConfigOutputs) < 2 {
			r.restartConfigOutputs = append(r.restartConfigOutputs, append([]byte(nil), result.Stdout...))
		}
	}
	return result, err
}

func (r *liveGatewayV2StageStabilityCaptureRunner) isStageInspect(args []string) bool {
	return len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" &&
		args[3] == gatewayContainerInspectFormat && args[4] == r.stageName
}

func (r *liveGatewayV2StageStabilityCaptureRunner) isStageRestartConfigRead(args []string) bool {
	if len(args) != 4 || args[0] != "container" || args[1] != "cp" || args[3] != "-" {
		return false
	}
	suffix := ":/config/" + r.configFilename
	container := strings.TrimSuffix(args[2], suffix)
	return container != args[2] && validContainerID(container) && normalizeID(container) == normalizeID(r.stageID)
}

func (r *liveGatewayV2StageStabilityCaptureRunner) failureOutcomes() []liveGatewayV2TraceOutcome {
	failures := make([]liveGatewayV2TraceOutcome, 0, 16)
	if r.inspectAttempts != 2 || len(r.inspectOutputs) != 2 {
		failures = append(failures, liveGatewayV2TraceStabilityInspectCaptureIncomplete)
	} else {
		var first, confirmed gatewayContainerInspection
		if json.Unmarshal(r.inspectOutputs[0], &first) != nil || json.Unmarshal(r.inspectOutputs[1], &confirmed) != nil {
			failures = append(failures, liveGatewayV2TraceStabilityInspectCaptureIncomplete)
		} else {
			failures = append(failures, liveGatewayV2StageInspectionChangeOutcomes(first, confirmed)...)
		}
		first = gatewayContainerInspection{}
		confirmed = gatewayContainerInspection{}
	}
	if r.restartConfigAttempts != 2 || len(r.restartConfigOutputs) != 2 {
		failures = append(failures, liveGatewayV2TraceStabilityConfigCaptureIncomplete)
	} else {
		first, firstValid := liveGatewayV2RestartConfigFromArchive(r.restartConfigOutputs[0], r.configFilename)
		confirmed, confirmedValid := liveGatewayV2RestartConfigFromArchive(r.restartConfigOutputs[1], r.configFilename)
		if !firstValid || !confirmedValid {
			failures = append(failures, liveGatewayV2TraceStabilityConfigCaptureIncomplete)
		} else if !sameCaddyConfig(first, confirmed) {
			failures = append(failures, liveGatewayV2TraceStabilityRestartConfig)
		}
		clear(first)
		clear(confirmed)
	}
	return failures
}

func liveGatewayV2StageInspectionChangeOutcomes(first, confirmed gatewayContainerInspection) []liveGatewayV2TraceOutcome {
	changed := func(values ...bool) bool {
		for _, same := range values {
			if !same {
				return true
			}
		}
		return false
	}
	return liveGatewayV2SelectedTraceOutcomes(
		liveGatewayV2TraceSelection{changed(first.ID == confirmed.ID, first.Name == confirmed.Name,
			first.Image == confirmed.Image, first.Hostname == confirmed.Hostname, first.User == confirmed.User,
			first.NetworkMode == confirmed.NetworkMode), liveGatewayV2TraceStabilityContainerIdentity},
		liveGatewayV2TraceSelection{changed(first.Running == confirmed.Running,
			first.Restarting == confirmed.Restarting), liveGatewayV2TraceStabilityContainerState},
		liveGatewayV2TraceSelection{changed(reflect.DeepEqual(first.Env, confirmed.Env),
			reflect.DeepEqual(first.Entrypoint, confirmed.Entrypoint), reflect.DeepEqual(first.Cmd, confirmed.Cmd)),
			liveGatewayV2TraceStabilityContainerExecution},
		liveGatewayV2TraceSelection{changed(first.ReadOnly == confirmed.ReadOnly, first.Privileged == confirmed.Privileged,
			reflect.DeepEqual(first.CapAdd, confirmed.CapAdd), reflect.DeepEqual(first.CapDrop, confirmed.CapDrop),
			reflect.DeepEqual(first.SecurityOpt, confirmed.SecurityOpt), reflect.DeepEqual(first.Binds, confirmed.Binds),
			reflect.DeepEqual(first.Tmpfs, confirmed.Tmpfs)), liveGatewayV2TraceStabilityContainerIsolation},
		liveGatewayV2TraceSelection{changed(reflect.DeepEqual(first.Mounts, confirmed.Mounts), first.Memory == confirmed.Memory,
			first.MemorySwap == confirmed.MemorySwap, first.NanoCPUs == confirmed.NanoCPUs,
			first.PIDsLimit == confirmed.PIDsLimit, reflect.DeepEqual(first.Ulimits, confirmed.Ulimits)),
			liveGatewayV2TraceStabilityContainerResources},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.Mounts, confirmed.Mounts), liveGatewayV2TraceStabilityContainerMounts},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.Mounts, confirmed.Mounts) && len(first.Mounts) > 0 &&
			liveGatewayV2SameMountMultiset(first.Mounts, confirmed.Mounts), liveGatewayV2TraceStabilityContainerMountOrderOnly},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.Mounts, confirmed.Mounts) &&
			!liveGatewayV2SameMountMultiset(first.Mounts, confirmed.Mounts), liveGatewayV2TraceStabilityContainerMountContent},
		liveGatewayV2TraceSelection{first.Memory != confirmed.Memory, liveGatewayV2TraceStabilityContainerMemory},
		liveGatewayV2TraceSelection{first.MemorySwap != confirmed.MemorySwap, liveGatewayV2TraceStabilityContainerMemorySwap},
		liveGatewayV2TraceSelection{first.NanoCPUs != confirmed.NanoCPUs, liveGatewayV2TraceStabilityContainerNanoCPUs},
		liveGatewayV2TraceSelection{first.PIDsLimit != confirmed.PIDsLimit, liveGatewayV2TraceStabilityContainerPIDsLimit},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.Ulimits, confirmed.Ulimits), liveGatewayV2TraceStabilityContainerUlimits},
		liveGatewayV2TraceSelection{changed(first.LogType == confirmed.LogType,
			reflect.DeepEqual(first.LogConfig, confirmed.LogConfig), first.Restart == confirmed.Restart),
			liveGatewayV2TraceStabilityContainerLogging},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.Labels, confirmed.Labels),
			liveGatewayV2TraceStabilityContainerLabels},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.PortBindings, confirmed.PortBindings),
			liveGatewayV2TraceStabilityContainerPorts},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.Networks, confirmed.Networks),
			liveGatewayV2TraceStabilityContainerNetworks},
		liveGatewayV2TraceSelection{changed(first.Paused == confirmed.Paused, first.Dead == confirmed.Dead,
			first.RestartCount == confirmed.RestartCount), liveGatewayV2TraceStabilityRuntimeState},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.EffectivePortBindings, confirmed.EffectivePortBindings),
			liveGatewayV2TraceStabilityRuntimePorts},
		liveGatewayV2TraceSelection{!reflect.DeepEqual(first.ConfiguredNetworks, confirmed.ConfiguredNetworks),
			liveGatewayV2TraceStabilityRuntimeNetworks},
	)
}

func liveGatewayV2SameMountMultiset(first, confirmed []mountInspection) bool {
	if len(first) != len(confirmed) {
		return false
	}
	counts := make(map[mountInspection]int, len(first))
	for _, mount := range first {
		counts[mount]++
	}
	for _, mount := range confirmed {
		counts[mount]--
		if counts[mount] < 0 {
			return false
		}
	}
	return true
}

func liveGatewayV2RestartConfigFromArchive(value []byte, filename string) ([]byte, bool) {
	reader := tar.NewReader(bytes.NewReader(value))
	header, err := reader.Next()
	if err != nil || header == nil || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > gatewayV2MaxConfigBytes ||
		strings.TrimPrefix(header.Name, "./") != filename {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(reader, gatewayV2MaxConfigBytes+1))
	if err != nil || int64(len(body)) != header.Size || !json.Valid(body) {
		clear(body)
		return nil, false
	}
	if _, err := reader.Next(); err != io.EOF {
		clear(body)
		return nil, false
	}
	return body, true
}

func (r *liveGatewayV2StageStabilityCaptureRunner) clear() {
	for _, output := range r.inspectOutputs {
		clear(output)
	}
	for _, output := range r.restartConfigOutputs {
		clear(output)
	}
	clear(r.inspectOutputs)
	clear(r.restartConfigOutputs)
	r.inspectOutputs = nil
	r.restartConfigOutputs = nil
}

func (d liveGatewayV2TracingUpgradeDriver) attestStoppedStage(ctx context.Context, source routeState,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) bool {
	if d.manager == nil || d.manager.runner == nil || ctx == nil || !validGatewayTopologyInputs(source, state, journal) {
		d.trace.recordAt(liveGatewayV2TraceStageAttestStoppedDetail, journal.Phase, liveGatewayV2TraceAttestInspectError)
		d.trace.recordAt(liveGatewayV2TraceStageAttestStopped, journal.Phase, liveGatewayV2TraceFalse)
		return false
	}
	capture := &liveGatewayV2StageStabilityCaptureRunner{
		inner: d.manager.runner, stageName: state.Identity.StageContainer,
		stageID: journal.Resources.StageContainerID, configFilename: state.Identity.StageConfigFilename,
	}
	d.manager.runner = capture
	var observation gatewayV2DockerObservation
	var err error
	func() {
		defer func() { d.manager.runner = capture.inner }()
		observation, err = d.manager.inspectGatewayV2Docker(ctx, source, state, journal)
	}()
	stabilityFailures := capture.failureOutcomes()
	capture.clear()
	if err != nil {
		clearGatewayV2DockerObservation(&observation)
		d.trace.recordAt(liveGatewayV2TraceStageAttestStoppedDetail, journal.Phase, liveGatewayV2TraceAttestInspectError)
		d.trace.recordAt(liveGatewayV2TraceStageAttestStopped, journal.Phase, liveGatewayV2TraceFalse)
		return false
	}
	defer clearGatewayV2DockerObservation(&observation)
	result := classifyGatewayV2RecoveryTopology(source, state, journal, observation) == gatewayV2RecoveryStageIntentStoppedStage
	if !result {
		failures := liveGatewayV2StoppedStageFailureOutcomes(source, state, journal, observation)
		if len(failures) == 0 {
			failures = append(failures, liveGatewayV2TraceAttestUnclassified)
		}
		for _, failure := range failures {
			d.trace.recordAt(liveGatewayV2TraceStageAttestStoppedDetail, journal.Phase, failure)
		}
		if !observation.StageStable {
			if len(stabilityFailures) == 0 {
				stabilityFailures = append(stabilityFailures, liveGatewayV2TraceStabilityUnclassified)
			}
			for _, failure := range stabilityFailures {
				d.trace.recordAt(liveGatewayV2TraceStageStabilityDetail, journal.Phase, failure)
			}
		}
	}
	d.trace.recordAt(liveGatewayV2TraceStageAttestStopped, journal.Phase, liveGatewayV2BoolOutcome(result))
	return result
}

func liveGatewayV2StoppedStageFailureOutcomes(source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal, observation gatewayV2DockerObservation,
) []liveGatewayV2TraceOutcome {
	failures := make([]liveGatewayV2TraceOutcome, 0, 32)
	add := func(failure liveGatewayV2TraceOutcome, valid bool) {
		if !valid {
			failures = append(failures, failure)
		}
	}

	add(liveGatewayV2TraceAttestPhase, journal.Phase == gatewayPhaseStageIntent)
	add(liveGatewayV2TraceAttestTopologyInputs, validGatewayTopologyInputs(source, state, journal))
	add(liveGatewayV2TraceAttestPinnedImage, validGatewayPinnedImage(observation.Image, observation.ImageFound))
	add(liveGatewayV2TraceAttestResourceJournalBinding, gatewayV2ObservedResourcesMatchJournal(journal, observation))
	add(liveGatewayV2TraceAttestV1Base, validGatewayV1Base(source, journal, observation, true))
	add(liveGatewayV2TraceAttestV1Serving, observation.V1Container.Running && !observation.V1Container.Restarting)
	add(liveGatewayV2TraceAttestV1Stability, observation.V1Stable)
	add(liveGatewayV2TraceAttestV1ResourceStability, observation.V1ResourcesStable)
	add(liveGatewayV2TraceAttestV1EndpointIdentity, observation.V1EndpointIdentityProven)
	add(liveGatewayV2TraceAttestV2ResourceStability, observation.V2ResourcesStable)
	add(liveGatewayV2TraceAttestStageStability, observation.StageStable)
	add(liveGatewayV2TraceAttestFinalStability, observation.FinalStable)
	add(liveGatewayV2TraceAttestOwnedInventoryStability, observation.OwnedInventoriesStable)

	infrastructureValid := validGatewayV2StoppedStageInfrastructure(state, journal, observation)
	add(liveGatewayV2TraceAttestStageInfrastructure, infrastructureValid)
	add(liveGatewayV2TraceAttestStageBinding, journal.Resources.StageContainerID != "")
	add(liveGatewayV2TraceAttestFinalAbsent,
		journal.Resources.FinalContainerID == "" && !observation.FinalContainerFound)
	add(liveGatewayV2TraceAttestOwnedContainers,
		validOwnedNameSet(observation.OwnedContainers, state.Identity.StageContainer))
	add(liveGatewayV2TraceAttestOwnedVolumes,
		validOwnedNameSet(observation.OwnedVolumes, state.Identity.ConfigVolume, state.Identity.DataVolume))
	add(liveGatewayV2TraceAttestOwnedNetworks,
		validOwnedNameSet(observation.OwnedNetworks, state.Identity.IngressNetwork))
	add(liveGatewayV2TraceAttestVolumes, validGatewayV2Volumes(state, journal, observation))

	stoppedContainerValid := validGatewayV2StoppedContainer(state, journal, observation.StageContainer,
		observation.StageRuntime, observation.StageContainerFound, gatewayV2StageContainerRole, observation.Image.ID)
	add(liveGatewayV2TraceAttestStoppedContainer, stoppedContainerValid)
	if !stoppedContainerValid {
		failures = append(failures, liveGatewayV2StoppedContainerFailureOutcomes(state, journal,
			observation.StageContainer, observation.StageRuntime, gatewayV2StageContainerRole, observation.Image.ID)...)
	}

	stoppedIngressValid := validGatewayV2StoppedIngressNetwork(state, journal, observation.IngressNetwork,
		observation.IngressNetworkID, observation.IngressFound, observation.StageRuntime)
	add(liveGatewayV2TraceAttestStoppedIngressNetwork, stoppedIngressValid)
	if !stoppedIngressValid {
		attachment, configured := observation.StageRuntime.ConfiguredNetworks[state.Identity.IngressNetwork]
		inspectedIDValid := validContainerID(observation.IngressNetworkID)
		add(liveGatewayV2TraceAttestIngressConfigured, configured)
		add(liveGatewayV2TraceAttestIngressInspectedID, inspectedIDValid)
		add(liveGatewayV2TraceAttestIngressJournalID,
			inspectedIDValid && normalizeID(observation.IngressNetworkID) == journal.Resources.IngressNetworkID)
		add(liveGatewayV2TraceAttestIngressConfiguredReference,
			configured && validGatewayV2StoppedNetworkReference(attachment.NetworkID, observation.IngressNetworkID))
		add(liveGatewayV2TraceAttestIngressResourceIdentity,
			validGatewayV2IngressNetwork(state, journal, observation.IngressNetwork, observation.IngressFound, "", ""))
	}

	expected, err := buildGatewayV2StageConfig(state)
	add(liveGatewayV2TraceAttestStageConfigBuild, err == nil)
	add(liveGatewayV2TraceAttestStageLiveConfigAbsent, len(observation.StageConfig) == 0)
	if err == nil {
		add(liveGatewayV2TraceAttestStageRestartConfig, sameCaddyConfig(expected, observation.StageRestartConfig))
	}
	clear(expected)
	add(liveGatewayV2TraceAttestApplicationNetworksAbsent,
		len(observation.ApplicationNetworks) == 0 && len(observation.ApplicationNetworkIDs) == 0)
	return failures
}

func (d liveGatewayV2TracingUpgradeDriver) attestStoppedStageForCompensation(ctx context.Context, source routeState,
	state gatewayV2RouteState, journal gatewayMigrationJournal,
) bool {
	result := d.gatewayV2UpgradeDriver.attestStoppedStageForCompensation(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceStageAttestStoppedCompensation, journal.Phase, liveGatewayV2BoolOutcome(result))
	return result
}

func (d liveGatewayV2TracingUpgradeDriver) startStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	if d.manager == nil || d.manager.runner == nil {
		d.trace.recordAt(liveGatewayV2TraceStageStartDetail, journal.Phase, liveGatewayV2TraceStartRunnerUnavailable)
		err := d.gatewayV2UpgradeDriver.startStage(ctx, state, journal)
		d.trace.recordAt(liveGatewayV2TraceStageStart, journal.Phase, liveGatewayV2ErrorOutcome(err))
		return err
	}
	capture := &liveGatewayV2StartCaptureRunner{inner: d.manager.runner}
	d.manager.runner = capture
	var err error
	func() {
		defer func() { d.manager.runner = capture.inner }()
		err = d.gatewayV2UpgradeDriver.startStage(ctx, state, journal)
	}()
	outcome := capture.outcome
	if !capture.called {
		if !validGatewayV2RouteState(state) || journal.Phase != gatewayPhaseStageIntent || !validSHA256(journal.Resources.StageContainerID) {
			outcome = liveGatewayV2TraceStartValidationRejected
		} else {
			outcome = liveGatewayV2TraceStartCommandFailedOther
		}
	}
	d.trace.recordAt(liveGatewayV2TraceStageStartDetail, journal.Phase, outcome)
	for _, detail := range capture.details {
		d.trace.recordAt(liveGatewayV2TraceStageStartFailureShape, journal.Phase, detail)
	}
	d.trace.recordAt(liveGatewayV2TraceStageStart, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

type liveGatewayV2StartCaptureRunner struct {
	inner   runtimeprocess.CommandRunner
	called  bool
	outcome liveGatewayV2TraceOutcome
	details []liveGatewayV2TraceOutcome
}

func (r *liveGatewayV2StartCaptureRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	result, err := r.inner.Run(ctx, request)
	r.called = true
	if len(request.Args) != 3 || request.Args[0] != "container" || request.Args[1] != "start" {
		r.outcome = liveGatewayV2TraceStartUnexpectedCommand
	} else {
		r.outcome = liveGatewayV2StartCommandOutcome(result, err)
		r.details = liveGatewayV2StartFailureShapeOutcomes(result, err)
	}
	return result, err
}

func liveGatewayV2StartFailureShapeOutcomes(result runtimeprocess.CommandResult, err error) []liveGatewayV2TraceOutcome {
	if err == nil || result.StdoutTruncated || result.StderrTruncated {
		return nil
	}
	message := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
	contains := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(message, part) {
				return true
			}
		}
		return false
	}
	details := liveGatewayV2SelectedTraceOutcomes(
		liveGatewayV2TraceSelection{contains("oci runtime"), liveGatewayV2TraceStartShapeOCI},
		liveGatewayV2TraceSelection{contains("runc create"), liveGatewayV2TraceStartShapeRunc},
		liveGatewayV2TraceSelection{contains("unable to start container process", "during container init"), liveGatewayV2TraceStartShapeProcessInit},
		liveGatewayV2TraceSelection{contains("no such file or directory"), liveGatewayV2TraceStartShapeMissingPath},
		liveGatewayV2TraceSelection{contains("not a directory"), liveGatewayV2TraceStartShapeNotDirectory},
		liveGatewayV2TraceSelection{contains("invalid argument"), liveGatewayV2TraceStartShapeInvalidArg},
		liveGatewayV2TraceSelection{contains("read-only file system"), liveGatewayV2TraceStartShapeReadOnly},
		liveGatewayV2TraceSelection{contains("resource temporarily unavailable"), liveGatewayV2TraceStartShapeResourceBusy},
		liveGatewayV2TraceSelection{contains("network", "endpoint"), liveGatewayV2TraceStartShapeNetwork},
		liveGatewayV2TraceSelection{contains("port", "address"), liveGatewayV2TraceStartShapePort},
		liveGatewayV2TraceSelection{contains("exec", "executable"), liveGatewayV2TraceStartShapeExec},
		liveGatewayV2TraceSelection{contains("mount", "rootfs"), liveGatewayV2TraceStartShapeMount},
		liveGatewayV2TraceSelection{contains("permission denied", "operation not permitted"), liveGatewayV2TraceStartShapePermission},
		liveGatewayV2TraceSelection{contains("/proc/sys/"), liveGatewayV2TraceStartShapeProcSys},
		liveGatewayV2TraceSelection{contains("ip_unprivileged_port_start"), liveGatewayV2TraceStartShapePortSysctl},
		liveGatewayV2TraceSelection{contains("sysctl"), liveGatewayV2TraceStartShapeSysctl},
		liveGatewayV2TraceSelection{contains("rlimit"), liveGatewayV2TraceStartShapeRlimit},
		liveGatewayV2TraceSelection{contains("pivot_root"), liveGatewayV2TraceStartShapePivotRoot},
		liveGatewayV2TraceSelection{contains("hostname"), liveGatewayV2TraceStartShapeHostname},
		liveGatewayV2TraceSelection{contains("capability", "capabilities"), liveGatewayV2TraceStartShapeCapability},
		liveGatewayV2TraceSelection{contains("cgroup"), liveGatewayV2TraceStartShapeCgroup},
	)
	if len(details) == 0 {
		return []liveGatewayV2TraceOutcome{liveGatewayV2TraceStartShapeNoKnownDetail}
	}
	return details
}

func liveGatewayV2StartCommandOutcome(result runtimeprocess.CommandResult, err error) liveGatewayV2TraceOutcome {
	if result.StdoutTruncated || result.StderrTruncated {
		return liveGatewayV2TraceStartOutputTruncated
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return liveGatewayV2TraceStartCancelled
	}
	if errors.Is(err, runtimeprocess.ErrTerminationFailed) {
		return liveGatewayV2TraceStartTerminationFailed
	}
	if err == nil {
		return liveGatewayV2TraceStartCommandSucceeded
	}
	message := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
	switch {
	case strings.Contains(message, "port is already allocated"), strings.Contains(message, "port is already in use"),
		strings.Contains(message, "address already in use"), strings.Contains(message, "failed to bind host port"),
		strings.Contains(message, "port binding failed"),
		strings.Contains(message, "bind") && strings.Contains(message, "port"):
		return liveGatewayV2TraceStartHostPortConflict
	case strings.Contains(message, "cannot assign requested address"), strings.Contains(message, "requested address is not valid in its context"):
		return liveGatewayV2TraceStartSelectedAddressUnavailable
	case strings.Contains(message, "no such network"),
		strings.Contains(message, "network") && strings.Contains(message, "not found"):
		return liveGatewayV2TraceStartNetworkUnavailable
	case strings.Contains(message, "no such container"):
		return liveGatewayV2TraceStartContainerMissing
	case strings.Contains(message, "permission denied"), strings.Contains(message, "access is denied"):
		return liveGatewayV2TraceStartPermissionDenied
	case strings.Contains(message, "failed programming external connectivity"), strings.Contains(message, "failed to create endpoint"):
		return liveGatewayV2TraceStartExternalConnectivityFailed
	case strings.Contains(message, "oci runtime") && (strings.Contains(message, "mount") || strings.Contains(message, "rootfs")):
		return liveGatewayV2TraceStartRuntimeMountFailed
	case strings.Contains(message, "oci runtime") && (strings.Contains(message, "exec") || strings.Contains(message, "executable")):
		return liveGatewayV2TraceStartRuntimeExecFailed
	case strings.Contains(message, "oci runtime") && (strings.Contains(message, "seccomp") || strings.Contains(message, "apparmor") ||
		strings.Contains(message, "capability") || strings.Contains(message, "operation not permitted")):
		return liveGatewayV2TraceStartRuntimeSecurityFailed
	case strings.Contains(message, "oci runtime") && strings.Contains(message, "cgroup"):
		return liveGatewayV2TraceStartRuntimeCgroupFailed
	case strings.Contains(message, "oci runtime") && strings.Contains(message, "namespace"):
		return liveGatewayV2TraceStartRuntimeNamespaceFailed
	case strings.Contains(message, "oci runtime"), strings.Contains(message, "failed to create task"),
		strings.Contains(message, "failed to start shim"), strings.Contains(message, "container init"):
		return liveGatewayV2TraceStartRuntimeCreateFailed
	default:
		return liveGatewayV2TraceStartCommandFailedOther
	}
}

func (d liveGatewayV2TracingUpgradeDriver) stopStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2UpgradeDriver.stopStage(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceStageStop, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingUpgradeDriver) removeStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2UpgradeDriver.removeStage(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceStageRemove, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

type liveGatewayV2TracingTransferDriver struct {
	gatewayV2TransferDriver
	trace *liveGatewayV2OperationTrace
}

func (d liveGatewayV2TracingTransferDriver) observeTopology(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) gatewayObservedTopology {
	result := d.gatewayV2TransferDriver.observeTopology(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferObserveTopology, journal.Phase, liveGatewayV2TopologyOutcome(result))
	return result
}

func (d liveGatewayV2TracingTransferDriver) observeRecovery(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) gatewayV2RecoveryTopology {
	result := d.gatewayV2TransferDriver.observeRecovery(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferObserveRecovery, journal.Phase, liveGatewayV2RecoveryOutcome(result))
	return result
}

func (d liveGatewayV2TracingTransferDriver) proveFinalHostRoutes(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) bool {
	result := d.gatewayV2TransferDriver.proveFinalHostRoutes(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferProveFinalRoutes, journal.Phase, liveGatewayV2BoolOutcome(result))
	return result
}

func (d liveGatewayV2TracingTransferDriver) selectedInterfacePreflight(profile gatewayProfileBinding) error {
	err := d.gatewayV2TransferDriver.selectedInterfacePreflight(profile)
	d.trace.recordCurrent(liveGatewayV2TraceTransferSelectedInterface, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) copyFinalConfigToStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal, contents []byte,
) error {
	err := d.gatewayV2TransferDriver.copyFinalConfigToStage(ctx, state, journal, contents)
	d.trace.recordAt(liveGatewayV2TraceTransferCopyFinalConfig, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) readFinalConfigFromStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) ([]byte, error) {
	contents, err := d.gatewayV2TransferDriver.readFinalConfigFromStage(ctx, state, journal)
	outcome := liveGatewayV2ErrorOutcome(err)
	if err == nil {
		expected, expectedErr := expectedGatewayV2FinalConfig(state)
		if expectedErr == nil && sameCaddyConfig(expected, contents) {
			outcome = liveGatewayV2TraceConfigMatch
		} else {
			outcome = liveGatewayV2TraceConfigMismatch
		}
		clear(expected)
	}
	d.trace.recordAt(liveGatewayV2TraceTransferReadFinalConfig, journal.Phase, outcome)
	return contents, err
}

func (d liveGatewayV2TracingTransferDriver) stopStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.stopStage(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferStopStage, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) removeStage(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.removeStage(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferRemoveStage, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) createFinal(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) (string, error) {
	id, err := d.gatewayV2TransferDriver.createFinal(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferCreateFinal, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return id, err
}

func (d liveGatewayV2TracingTransferDriver) stopV1(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.stopV1(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferStopV1, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) startV1(ctx context.Context, source routeState, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.startV1(ctx, source, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferStartV1, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) startFinal(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.startFinal(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferStartFinal, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) stopFinal(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.stopFinal(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferStopFinal, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func (d liveGatewayV2TracingTransferDriver) removeFinal(ctx context.Context, state gatewayV2RouteState,
	journal gatewayMigrationJournal,
) error {
	err := d.gatewayV2TransferDriver.removeFinal(ctx, state, journal)
	d.trace.recordAt(liveGatewayV2TraceTransferRemoveFinal, journal.Phase, liveGatewayV2ErrorOutcome(err))
	return err
}

func TestGatewayV2StoppedContainerDiagnosticMatchesValidator(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseStageIntent
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Resources.FinalContainerID = ""
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
	stopGatewayV2TestContainer(&observation.StageContainer, &observation.StageRuntime, &observation.StageConfig,
		state, observation.IngressNetworkID, nil)
	if !validGatewayV2StoppedContainer(state, journal, observation.StageContainer, observation.StageRuntime, true,
		gatewayV2StageContainerRole, observation.Image.ID) {
		t.Fatal("valid stopped-container diagnostic fixture was rejected")
	}
	if failures := liveGatewayV2StoppedContainerFailureOutcomes(state, journal, observation.StageContainer,
		observation.StageRuntime, gatewayV2StageContainerRole, observation.Image.ID); len(failures) != 0 {
		t.Fatalf("valid stopped-container diagnostic failures=%v", failures)
	}

	drift := observation.StageContainer
	drift.LogConfig = map[string]string{"max-size": "10m", "max-file": "4"}
	if validGatewayV2StoppedContainer(state, journal, drift, observation.StageRuntime, true,
		gatewayV2StageContainerRole, observation.Image.ID) {
		t.Fatal("production stopped-container validator accepted logging drift")
	}
	failures := liveGatewayV2StoppedContainerFailureOutcomes(state, journal, drift, observation.StageRuntime,
		gatewayV2StageContainerRole, observation.Image.ID)
	if !reflect.DeepEqual(failures, []liveGatewayV2TraceOutcome{liveGatewayV2TraceCreatePredicateLogging}) {
		t.Fatalf("logging drift diagnostic failures=%v", failures)
	}

	expectedLabels := gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, gatewayV2StageContainerRole, true)
	labelsWithImageMetadata := make(map[string]string, len(expectedLabels)+1)
	for key, value := range expectedLabels {
		labelsWithImageMetadata[key] = value
	}
	labelsWithImageMetadata["org.opencontainers.image.title"] = "metadata"
	if failures := liveGatewayV2LabelFailureOutcomes(labelsWithImageMetadata, expectedLabels); !reflect.DeepEqual(failures,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceCreateLabelsExtraForeign}) {
		t.Fatalf("foreign label diagnostic failures=%v", failures)
	}

	expectedNetworks, valid := gatewayV2ExpectedContainerNetworks(state, gatewayV2StageContainerRole)
	if !valid {
		t.Fatal("valid stage network plan was rejected")
	}
	networks := make(map[string]gatewayV2ConfiguredNetwork, len(observation.StageRuntime.ConfiguredNetworks))
	for name, network := range observation.StageRuntime.ConfiguredNetworks {
		networks[name] = network
	}
	ingress := networks[state.Identity.IngressNetwork]
	ingress.NetworkID = ""
	ingress.IPAMConfig = nil
	networks[state.Identity.IngressNetwork] = ingress
	if failures := liveGatewayV2StoppedNetworkFailureOutcomes(state, journal, gatewayV2StageContainerRole, expectedNetworks, networks); !reflect.DeepEqual(failures, []liveGatewayV2TraceOutcome{
		liveGatewayV2TraceCreateNetworksIDAbsent, liveGatewayV2TraceCreateNetworksIPAMMissing,
	}) {
		t.Fatalf("stopped network diagnostic failures=%v", failures)
	}
	ingress.IPAMConfig = &gatewayV2ConfiguredIPAM{IPv4Address: state.Network.ContainerIPv4}
	ingress.NetworkID = "malformed"
	networks[state.Identity.IngressNetwork] = ingress
	if failures := liveGatewayV2StoppedNetworkFailureOutcomes(state, journal, gatewayV2StageContainerRole, expectedNetworks, networks); !reflect.DeepEqual(failures,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceCreateNetworksIDMalformed}) {
		t.Fatalf("malformed stopped network ID diagnostic failures=%v", failures)
	}
	ingress.NetworkID = "sha256:" + strings.Repeat("9", 64)
	networks[state.Identity.IngressNetwork] = ingress
	if failures := liveGatewayV2StoppedNetworkFailureOutcomes(state, journal, gatewayV2StageContainerRole, expectedNetworks, networks); !reflect.DeepEqual(failures,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceCreateNetworksIDMismatch}) {
		t.Fatalf("mismatched stopped network ID diagnostic failures=%v", failures)
	}
}

func TestLiveGatewayV2StoppedStageDiagnosticMatchesClassifier(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseStageIntent
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Resources.FinalContainerID = ""
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
	stopGatewayV2TestContainer(&observation.StageContainer, &observation.StageRuntime, &observation.StageConfig,
		state, observation.IngressNetworkID, nil)
	observation.IngressNetwork.Containers = map[string]caddyNetworkContainerInspection{}
	observation.Stage404Proven = false
	observation.StageHostPublicationProven = false
	if got := classifyGatewayV2RecoveryTopology(source, state, journal, observation); got != gatewayV2RecoveryStageIntentStoppedStage {
		t.Fatalf("valid stopped-stage fixture topology = %q", got)
	}
	if failures := liveGatewayV2StoppedStageFailureOutcomes(source, state, journal, observation); len(failures) != 0 {
		t.Fatalf("valid stopped-stage diagnostic failures=%v", failures)
	}

	stageUnstable := observation
	stageUnstable.StageStable = false
	if got := liveGatewayV2StoppedStageFailureOutcomes(source, state, journal, stageUnstable); !reflect.DeepEqual(got,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceAttestStageStability}) {
		t.Fatalf("stage stability diagnostic failures=%v", got)
	}

	ownedNetworkMissing := observation
	ownedNetworkMissing.OwnedNetworks = nil
	if got := liveGatewayV2StoppedStageFailureOutcomes(source, state, journal, ownedNetworkMissing); !reflect.DeepEqual(got,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceAttestStageInfrastructure, liveGatewayV2TraceAttestOwnedNetworks}) {
		t.Fatalf("owned-network diagnostic failures=%v", got)
	}

	ingressMembershipDrift := observation
	ingressMembershipDrift.IngressNetwork = gatewayV2IdentityTestNetwork(state, journal,
		ingressMembershipDrift.StageContainer.ID, state.Identity.StageContainer)
	if got := liveGatewayV2StoppedStageFailureOutcomes(source, state, journal, ingressMembershipDrift); !reflect.DeepEqual(got,
		[]liveGatewayV2TraceOutcome{
			liveGatewayV2TraceAttestStageInfrastructure,
			liveGatewayV2TraceAttestStoppedIngressNetwork,
			liveGatewayV2TraceAttestIngressResourceIdentity,
		}) {
		t.Fatalf("ingress membership diagnostic failures=%v", got)
	}

	restartConfigDrift := observation
	restartConfigDrift.StageRestartConfig = []byte(`{"admin":{"listen":"localhost:2019"}}`)
	if got := liveGatewayV2StoppedStageFailureOutcomes(source, state, journal, restartConfigDrift); !reflect.DeepEqual(got,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceAttestStageRestartConfig}) {
		t.Fatalf("restart-config diagnostic failures=%v", got)
	}
}

func TestLiveGatewayV2StageStabilityCaptureClosesFieldChanges(t *testing.T) {
	source, state, journal := gatewayV2IdentityTestState(t)
	journal.Phase = gatewayPhaseStageIntent
	journal.Resources = gatewayV2IdentityTestBoundResources(t)
	journal.Resources.FinalContainerID = ""
	observation := gatewayV2IdentityTestObservation(t, source, state, journal, gatewayTopologyExactV1WithStage)
	stopGatewayV2TestContainer(&observation.StageContainer, &observation.StageRuntime, &observation.StageConfig,
		state, observation.IngressNetworkID, nil)

	first := gatewayContainerInspection{caddyInspection: observation.StageContainer,
		gatewayContainerRuntime: observation.StageRuntime}
	confirmed := liveGatewayV2CloneContainerInspection(t, first)
	config, err := buildGatewayV2StageConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(config)

	newCapture := func(confirmed gatewayContainerInspection, confirmedConfig []byte) *liveGatewayV2StageStabilityCaptureRunner {
		firstJSON, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		confirmedJSON, err := json.Marshal(confirmed)
		if err != nil {
			clear(firstJSON)
			t.Fatal(err)
		}
		return &liveGatewayV2StageStabilityCaptureRunner{
			inspectAttempts: 2, restartConfigAttempts: 2, configFilename: state.Identity.StageConfigFilename,
			inspectOutputs: [][]byte{firstJSON, confirmedJSON},
			restartConfigOutputs: [][]byte{
				liveGatewayV2RestartConfigArchiveForTest(t, state.Identity.StageConfigFilename, config),
				liveGatewayV2RestartConfigArchiveForTest(t, state.Identity.StageConfigFilename, confirmedConfig),
			},
		}
	}

	stable := newCapture(confirmed, config)
	if failures := stable.failureOutcomes(); len(failures) != 0 {
		stable.clear()
		t.Fatalf("stable capture failures=%v", failures)
	}
	stable.clear()

	networkNormalized := liveGatewayV2CloneContainerInspection(t, confirmed)
	ingress := networkNormalized.ConfiguredNetworks[state.Identity.IngressNetwork]
	ingress.NetworkID = ""
	networkNormalized.ConfiguredNetworks[state.Identity.IngressNetwork] = ingress
	networkCapture := newCapture(networkNormalized, config)
	if failures := networkCapture.failureOutcomes(); !reflect.DeepEqual(failures,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceStabilityRuntimeNetworks}) {
		networkCapture.clear()
		t.Fatalf("runtime-network capture failures=%v", failures)
	}
	networkCapture.clear()

	configCapture := newCapture(confirmed, []byte(`{"admin":{"listen":"localhost:2019"}}`))
	if failures := configCapture.failureOutcomes(); !reflect.DeepEqual(failures,
		[]liveGatewayV2TraceOutcome{liveGatewayV2TraceStabilityRestartConfig}) {
		configCapture.clear()
		t.Fatalf("restart-config capture failures=%v", failures)
	}
	configCapture.clear()
}

func liveGatewayV2CloneContainerInspection(t *testing.T, value gatewayContainerInspection) gatewayContainerInspection {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	var cloned gatewayContainerInspection
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func liveGatewayV2RestartConfigArchiveForTest(t *testing.T, filename string, contents []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: filename, Mode: 0o600, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), buffer.Bytes()...)
}

func TestLiveGatewayV2StageMountDiagnosticDistinguishesOrderAndContent(t *testing.T) {
	first := gatewayContainerInspection{caddyInspection: caddyInspection{Mounts: []mountInspection{
		{Type: "volume", Name: "config", Destination: "/config", RW: true},
		{Type: "volume", Name: "data", Destination: "/data", RW: true},
	}}}
	reordered := gatewayContainerInspection{caddyInspection: caddyInspection{Mounts: []mountInspection{
		first.Mounts[1], first.Mounts[0],
	}}}
	wantOrder := []liveGatewayV2TraceOutcome{
		liveGatewayV2TraceStabilityContainerResources,
		liveGatewayV2TraceStabilityContainerMounts,
		liveGatewayV2TraceStabilityContainerMountOrderOnly,
	}
	if got := liveGatewayV2StageInspectionChangeOutcomes(first, reordered); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("reordered mounts = %v, want %v", got, wantOrder)
	}
	changed := reordered
	changed.Mounts = append([]mountInspection(nil), reordered.Mounts...)
	changed.Mounts[0].RW = false
	wantContent := []liveGatewayV2TraceOutcome{
		liveGatewayV2TraceStabilityContainerResources,
		liveGatewayV2TraceStabilityContainerMounts,
		liveGatewayV2TraceStabilityContainerMountContent,
	}
	if got := liveGatewayV2StageInspectionChangeOutcomes(first, changed); !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("changed mounts = %v, want %v", got, wantContent)
	}
}

func TestGatewayV2OperationTraceClosesAndBoundsValues(t *testing.T) {
	trace := &liveGatewayV2OperationTrace{phase: liveGatewayV2TracePhasePreparation}
	trace.recordAt(liveGatewayV2TraceStageObserveTopology, gatewayMigrationPhase("untrusted-phase"),
		liveGatewayV2TopologyOutcome(gatewayObservedTopology("untrusted-topology")))
	for index := 0; index < liveGatewayV2TraceMaximumEvents; index++ {
		trace.recordCurrent(liveGatewayV2TraceStageHostPreflight, liveGatewayV2TraceOK)
	}
	events, truncated := trace.snapshot()
	if len(events) != liveGatewayV2TraceMaximumEvents || !truncated {
		t.Fatalf("trace bounds = events %d, truncated %t", len(events), truncated)
	}
	if events[0].phase != liveGatewayV2TracePhaseUnknown || events[0].outcome != liveGatewayV2TraceTopologyUnknown {
		t.Fatalf("trace did not map unknown values to closed enums: %+v", events[0])
	}
}

func TestLiveGatewayV2StartCommandOutcomeClosesDaemonErrors(t *testing.T) {
	commandError := errors.New("command failed")
	tests := []struct {
		name   string
		result runtimeprocess.CommandResult
		err    error
		want   liveGatewayV2TraceOutcome
	}{
		{name: "success", want: liveGatewayV2TraceStartCommandSucceeded},
		{name: "truncated", result: runtimeprocess.CommandResult{StderrTruncated: true}, want: liveGatewayV2TraceStartOutputTruncated},
		{name: "cancelled", err: context.Canceled, want: liveGatewayV2TraceStartCancelled},
		{name: "termination failed", err: runtimeprocess.ErrTerminationFailed, want: liveGatewayV2TraceStartTerminationFailed},
		{name: "host port conflict", result: runtimeprocess.CommandResult{Stderr: []byte("port is already allocated")}, err: commandError, want: liveGatewayV2TraceStartHostPortConflict},
		{name: "wrapped host port conflict", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: failed to bind host port")}, err: commandError, want: liveGatewayV2TraceStartHostPortConflict},
		{name: "selected address unavailable", result: runtimeprocess.CommandResult{Stderr: []byte("cannot assign requested address")}, err: commandError, want: liveGatewayV2TraceStartSelectedAddressUnavailable},
		{name: "network unavailable", result: runtimeprocess.CommandResult{Stderr: []byte("no such network")}, err: commandError, want: liveGatewayV2TraceStartNetworkUnavailable},
		{name: "container missing", result: runtimeprocess.CommandResult{Stderr: []byte("no such container")}, err: commandError, want: liveGatewayV2TraceStartContainerMissing},
		{name: "permission denied", result: runtimeprocess.CommandResult{Stderr: []byte("permission denied")}, err: commandError, want: liveGatewayV2TraceStartPermissionDenied},
		{name: "runtime mount failed", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: mount setup failed")}, err: commandError, want: liveGatewayV2TraceStartRuntimeMountFailed},
		{name: "runtime exec failed", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: exec failed")}, err: commandError, want: liveGatewayV2TraceStartRuntimeExecFailed},
		{name: "runtime security failed", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: seccomp setup failed")}, err: commandError, want: liveGatewayV2TraceStartRuntimeSecurityFailed},
		{name: "runtime cgroup failed", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: cgroup setup failed")}, err: commandError, want: liveGatewayV2TraceStartRuntimeCgroupFailed},
		{name: "runtime namespace failed", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: namespace setup failed")}, err: commandError, want: liveGatewayV2TraceStartRuntimeNamespaceFailed},
		{name: "runtime create failed", result: runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed")}, err: commandError, want: liveGatewayV2TraceStartRuntimeCreateFailed},
		{name: "external connectivity", result: runtimeprocess.CommandResult{Stderr: []byte("failed programming external connectivity")}, err: commandError, want: liveGatewayV2TraceStartExternalConnectivityFailed},
		{name: "wrapped external connectivity", result: runtimeprocess.CommandResult{Stderr: []byte("failed to create task: failed programming external connectivity")}, err: commandError, want: liveGatewayV2TraceStartExternalConnectivityFailed},
		{name: "other does not escape", result: runtimeprocess.CommandResult{Stderr: []byte("sensitive unexpected daemon output")}, err: commandError, want: liveGatewayV2TraceStartCommandFailedOther},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := liveGatewayV2StartCommandOutcome(test.result, test.err); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLiveGatewayV2StartFailureShapeDoesNotExposeDaemonOutput(t *testing.T) {
	result := runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: runc create failed: unable to start container process: no such file or directory: sensitive-value")}
	got := liveGatewayV2StartFailureShapeOutcomes(result, errors.New("command failed"))
	want := []liveGatewayV2TraceOutcome{
		liveGatewayV2TraceStartShapeOCI,
		liveGatewayV2TraceStartShapeRunc,
		liveGatewayV2TraceStartShapeProcessInit,
		liveGatewayV2TraceStartShapeMissingPath,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failure shape = %v, want %v", got, want)
	}
	for _, outcome := range got {
		if strings.Contains(string(outcome), "sensitive-value") {
			t.Fatal("daemon output escaped into trace")
		}
	}
	if got := liveGatewayV2StartFailureShapeOutcomes(runtimeprocess.CommandResult{StderrTruncated: true}, errors.New("command failed")); len(got) != 0 {
		t.Fatalf("truncated output produced failure shape %v", got)
	}
	sysctl := runtimeprocess.CommandResult{Stderr: []byte("OCI runtime create failed: error during container init: open /proc/sys/net/ipv4/ip_unprivileged_port_start: invalid argument")}
	wantSysctl := []liveGatewayV2TraceOutcome{
		liveGatewayV2TraceStartShapeOCI,
		liveGatewayV2TraceStartShapeProcessInit,
		liveGatewayV2TraceStartShapeInvalidArg,
		liveGatewayV2TraceStartShapePort,
		liveGatewayV2TraceStartShapeProcSys,
		liveGatewayV2TraceStartShapePortSysctl,
	}
	if got := liveGatewayV2StartFailureShapeOutcomes(sysctl, errors.New("command failed")); !reflect.DeepEqual(got, wantSysctl) {
		t.Fatalf("sysctl failure shape = %v, want %v", got, wantSysctl)
	}
}

func TestLiveGatewayV2CleanupAuthorizesOnlyTerminalCertainPhases(t *testing.T) {
	for _, phase := range []gatewayMigrationPhase{gatewayPhaseCommitted, gatewayPhaseRolledBack} {
		if !liveGatewayV2CleanupAuthorizedPhase(phase) {
			t.Fatalf("terminal certain phase %q was rejected", phase)
		}
	}
	for _, phase := range []gatewayMigrationPhase{
		gatewayPhasePrepared, gatewayPhaseStageIntent, gatewayPhaseStaged, gatewayPhaseTransferIntent,
		gatewayPhaseV2Serving, gatewayPhaseRollbackIntent, gatewayPhaseUncertain,
	} {
		if liveGatewayV2CleanupAuthorizedPhase(phase) {
			t.Fatalf("nonterminal or uncertain phase %q was authorized", phase)
		}
	}
}

func gatewayV2UpgradeRequestFromStateForTest(state gatewayV2RouteState) GatewayV2UpgradeRequest {
	return GatewayV2UpgradeRequest{
		OperationID: state.OperationID,
		Profile: GatewayV2ProfileBinding{
			RevisionID: state.Profile.RevisionID, RevisionNumber: state.Profile.RevisionNumber,
			SpecDigest: state.Profile.SpecDigest, SelectedIPv4: state.Profile.SelectedIPv4,
			InterfaceID: state.Profile.InterfaceID, PortStart: state.Profile.PortStart, PortEnd: state.Profile.PortEnd,
		},
		ApprovedBy: state.UpgradeAction.ApprovedBy, ApprovedActionDigest: state.UpgradeAction.Digest,
	}
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
	cleanupAuthorized := storeErr == nil && liveGatewayV2CleanupAuthorizedPhase(journal.Phase)
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
			!validGatewayV2ContainerLabels(container.Labels, gatewayV2ResourceLabels(state, journal, gatewayV2ManagedContainerLabel, resource.role, true)) {
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

func liveGatewayV2CleanupAuthorizedPhase(phase gatewayMigrationPhase) bool {
	return phase == gatewayPhaseCommitted || phase == gatewayPhaseRolledBack
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
