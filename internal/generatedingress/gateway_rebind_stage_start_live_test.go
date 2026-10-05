package generatedingress

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

const liveGatewayRebindStageStartNetworkEnvironment = "RIG_RUN_LIVE_GATEWAY_REBIND_STAGE_START_NETWORK"

type liveGatewayRebindStageStartDriver struct {
	managerGatewayRebindStageStartDriver
	startCalls    int
	stopCalls     int
	failProof     bool
	failed        bool
	injectedReady bool
}

func (d *liveGatewayRebindStageStartDriver) start(ctx context.Context, id string) error {
	d.startCalls++
	return d.managerGatewayRebindStageStartDriver.start(ctx, id)
}

func (d *liveGatewayRebindStageStartDriver) stop(ctx context.Context, id string) error {
	d.stopCalls++
	return d.managerGatewayRebindStageStartDriver.stop(ctx, id)
}

func (d *liveGatewayRebindStageStartDriver) hostProbe(ctx context.Context, address string,
	port uint16, host, path string,
) gatewayV2HostProbeResult {
	if d.failProof && !d.failed && strings.HasPrefix(path, gatewayV2ChallengePathPrefix) {
		actual := d.managerGatewayRebindStageStartDriver.hostProbe(ctx, address, port, host, path)
		challenge := strings.TrimPrefix(path, gatewayV2ChallengePathPrefix)
		d.injectedReady = validSHA256(challenge) && actual.Connected && actual.Responded &&
			actual.Status == http.StatusNotFound && actual.Body == gatewayV2ChallengeBodyPrefix+challenge
		d.failed = true
		return gatewayV2HostProbeResult{}
	}
	return d.managerGatewayRebindStageStartDriver.hostProbe(ctx, address, port, host, path)
}

func TestLiveGatewayRebindSuccessorStageStart(t *testing.T) {
	liveGatewayRebindSuccessorStageStart(t, "direct")
}

func TestLiveGatewayRebindSuccessorStageStartLostAcknowledgmentAdopts(t *testing.T) {
	liveGatewayRebindSuccessorStageStart(t, "lost-ack")
}

func TestLiveGatewayRebindSuccessorStageStartProofFailureWithdraws(t *testing.T) {
	liveGatewayRebindSuccessorStageStart(t, "proof-failure")
}

func liveGatewayRebindSuccessorStageStart(t *testing.T, mode string,
	afterServing ...func(*liveGatewayV2Fixture, *appaccess.Repository, gatewayRebindProtectedIntent, time.Time),
) {
	t.Helper()
	spec := liveGatewayV2FixtureSpec{
		appID:            "a1111111-1111-4111-8111-111111111111",
		planID:           "a2222222-2222-4222-8222-222222222222",
		operationID:      "a3333333-3333-4333-8333-333333333333",
		profileRevision:  "a4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-stage-start",
		applicationReply: "gateway-v2-rebind-stage-start",
		countRequests:    true,
	}
	switch mode {
	case "lost-ack":
		spec.appID, spec.planID, spec.operationID, spec.profileRevision =
			"b1111111-1111-4111-8111-111111111111", "b2222222-2222-4222-8222-222222222222",
			"b3333333-3333-4333-8333-333333333333", "b4444444-4444-4444-8444-444444444444"
		spec.imageTag += "-lost-ack"
	case "proof-failure":
		spec.appID, spec.planID, spec.operationID, spec.profileRevision =
			"c1111111-1111-4111-8111-111111111111", "c2222222-2222-4222-8222-222222222222",
			"c3333333-3333-4333-8333-333333333333", "c4444444-4444-4444-8444-444444444444"
		spec.imageTag += "-proof-failure"
	case "final-copy", "final-copy-lost-ack":
		spec.appID, spec.planID, spec.operationID, spec.profileRevision =
			"d1111111-1111-4111-8111-111111111111", "d2222222-2222-4222-8222-222222222222",
			"d3333333-3333-4333-8333-333333333333", "d4444444-4444-4444-8444-444444444444"
		spec.imageTag += "-" + mode
	}
	predecessorAddress, successorAddress := liveGatewayRebindHostAddresses(t, liveGatewayRebindStageStartNetworkEnvironment)
	fixture := newLiveGatewayV2Fixture(t, spec)
	fixture.interfaceIP = predecessorAddress.IPv4
	fixture.request = liveGatewayV2Request(t, spec, predecessorAddress, fixture.port)
	db, err := database.Open(fixture.stateRoot)
	if err != nil {
		t.Fatal("open live stage-start SQLite fixture")
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := appaccess.New(db)
	profile := prepareLiveGatewayRebindStagePredecessor(t, fixture, db, repository)
	proposal := seedLiveGatewayRebindPublicPassiveLineage(t, fixture, db, repository, profile,
		appaccess.GatewayProfileSpec{
			SelectedIPv4: successorAddress.IPv4,
			InterfaceID:  successorAddress.InterfaceID,
			PortStart:    fixture.port, PortEnd: fixture.port,
		})
	if proposal.Spec.SuccessorProfile.SelectedIPv4 == fixture.request.Profile.SelectedIPv4 {
		t.Fatal("stage start requires a distinct successor address while the predecessor remains live")
	}
	predecessorState, predecessorJournal, predecessorStore := liveGatewayV2LoadDurableOperation(t, fixture)
	preclaim, err := repository.GatewayRebindPreclaimSnapshot(fixture.ctx, proposal)
	if err != nil || !gatewayRebindPreclaimMatches(preclaim, predecessorState, predecessorJournal) {
		t.Fatal("stage-start predecessor did not pass zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("stage-start prepared claim did not bind predecessor")
	}
	beforeAccess, err := repository.CurrentAppAccess(fixture.ctx, spec.appID)
	if err != nil {
		t.Fatal("read committed predecessor app access before stage start")
	}
	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("stage-start preflight rejected present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("stage-start protected-intent observation differed from production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve stage-start protected successor generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact stage-start protected successor intent")
	}
	image, found, err := (managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}).inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned stage-start image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct stage-start successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install stage-start successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt: baseTime.Add(time.Nanosecond), ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct stage-start stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install stage-start stage intent progress")
	}
	var networkID string
	var configBinding *gatewayRebindStageConfigVolumeBinding
	var dataBinding *gatewayRebindStageDataVolumeBinding
	var containerBinding *gatewayRebindStageContainerBinding
	// Cleanup is registered before the first Docker effect. Unbound or uncertain
	// effects remain visible to the CI residue scan.
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStageStartChain(t, fixture, intent, networkID,
			configBinding, dataBinding, containerBinding)
	})

	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before stage start")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before stage start")
	}
	beforeDockerDigest, err := gatewayRebindPredecessorDockerDigest(beforeDocker, predecessorState.Identity)
	clearGatewayV2DockerObservation(&beforeDocker)
	if err != nil {
		t.Fatal("digest exact predecessor Docker state")
	}
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted predecessor requests did not settle before stage start")
	}
	stages := []struct {
		name string
		run  func(time.Time) error
	}{
		{"network", func(at time.Time) error {
			return fixture.ingress.stageGatewayRebindSuccessorNetwork(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
		{"config volume", func(at time.Time) error {
			return fixture.ingress.stageGatewayRebindSuccessorConfigVolume(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
		{"data volume", func(at time.Time) error {
			return fixture.ingress.stageGatewayRebindSuccessorDataVolume(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
		{"stopped container", func(at time.Time) error {
			return fixture.ingress.stageGatewayRebindSuccessorContainer(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
		{"config intent", func(at time.Time) error {
			return fixture.ingress.prepareGatewayRebindSuccessorStageConfigIntent(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
		{"config copy", func(at time.Time) error {
			return fixture.ingress.copyGatewayRebindSuccessorStageConfig(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
		{"start intent", func(at time.Time) error {
			return fixture.ingress.prepareGatewayRebindSuccessorStageStartIntent(
				fixture.ctx, repository, reads, fixture.ingress.inspectGatewayRebindDocker, at, nil)
		}},
	}
	for index, stage := range stages {
		if err := stage.run(baseTime.Add(time.Duration(index+2) * time.Nanosecond)); err != nil {
			failLiveIngress(t, "prepare live stage-start "+stage.name, err)
		}
		history, scanErr := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || len(history.Progress) != index+3 || history.Progress[index+2].Record.Stage == nil {
			t.Fatalf("live stage-start %s did not bind exact protected sequence %d", stage.name, index+3)
		}
		bound := history.Progress[index+2].Record.Stage
		if bound.Network != nil {
			networkID = bound.Network.ID
		}
		if bound.ConfigVolume != nil {
			configBinding = bound.ConfigVolume
		}
		if bound.DataVolume != nil {
			dataBinding = bound.DataVolume
		}
		if bound.StageContainer != nil {
			containerBinding = bound.StageContainer
		}
	}
	beforeStart, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeStart.Progress) != 9 || beforeStart.Progress[8].Record.Stage == nil ||
		beforeStart.Progress[8].Record.Stage.StageStartIntent == nil || containerBinding == nil {
		t.Fatal("live stage start lacks exact sequence-nine intent")
	}
	beforeBytes := liveGatewayRebindProgressBytes(t, beforeStart)
	startBinding := *beforeStart.Progress[8].Record.Stage.StageStartIntent
	driver := &liveGatewayRebindStageStartDriver{
		managerGatewayRebindStageStartDriver: managerGatewayRebindStageStartDriver{manager: fixture.ingress},
		failProof:                            mode == "proof-failure",
	}
	if mode == "lost-ack" {
		// Model an acknowledgment lost after the real Docker start but before the
		// process can write sequence ten. The new Manager must inspect and adopt.
		if err := driver.start(fixture.ctx, startBinding.StageContainer.ID); err != nil {
			t.Fatal("real Docker stage start did not take effect before simulated lost acknowledgment")
		}
		stillNine, scanErr := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || !reflect.DeepEqual(beforeBytes, liveGatewayRebindProgressBytes(t, stillNine)) {
			t.Fatal("real start before lost acknowledgment changed protected sequence nine")
		}
	}
	acting := fixture.ingress
	if mode == "lost-ack" {
		acting, err = New(fixture.runner, fixture.ingress.options)
		if err != nil {
			t.Fatal("restart Manager after lost Docker acknowledgment")
		}
	}
	actingDriver := driver
	if acting != fixture.ingress {
		actingDriver = &liveGatewayRebindStageStartDriver{
			managerGatewayRebindStageStartDriver: managerGatewayRebindStageStartDriver{manager: acting},
		}
	}
	actingReads := liveGatewayRebindStageProductionReads(acting)
	startErr := acting.startGatewayRebindSuccessorStageWithDriver(fixture.ctx, repository, actingReads,
		acting.inspectGatewayRebindDocker, actingDriver, baseTime.Add(9*time.Nanosecond), nil)
	if mode == "proof-failure" {
		if startErr == nil || !driver.failed || !driver.injectedReady || driver.startCalls != 1 || driver.stopCalls != 1 ||
			candidateMayBeLive(startErr) {
			t.Fatalf("ready real Docker proof was not safely compensated: error=%v injected=%t ready=%t starts=%d stops=%d",
				startErr, driver.failed, driver.injectedReady, driver.startCalls, driver.stopCalls)
		}
		afterFailure, scanErr := acting.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || !reflect.DeepEqual(beforeBytes, liveGatewayRebindProgressBytes(t, afterFailure)) {
			t.Fatal("compensated proof failure changed protected sequence-nine history")
		}
		stopped, inspectErr := driver.inspect(fixture.ctx, intent)
		if inspectErr != nil || !validGatewayRebindStageContainerObservation(intent,
			*beforeStart.Progress[8].Record.Stage, stopped, containerBinding) ||
			!proveGatewayRebindStagePublicationWithdrawn(fixture.ctx, startBinding, driver.hostProbe) {
			t.Fatal("compensated stage is not exactly stopped with selected and loopback listeners withdrawn")
		}
	} else {
		if startErr != nil {
			failLiveIngress(t, "start or adopt exact live successor stage", startErr)
		}
		if (mode != "lost-ack" && driver.startCalls != 1) ||
			(mode == "lost-ack" && (driver.startCalls != 1 || actingDriver.startCalls != 0)) ||
			actingDriver.stopCalls != 0 {
			t.Fatalf("unexpected Docker start/stop calls: original=%d adoption=%d stops=%d",
				driver.startCalls, actingDriver.startCalls, actingDriver.stopCalls)
		}
		bound, scanErr := acting.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || len(bound.Progress) != 10 || bound.Progress[9].Record.Stage == nil ||
			bound.Progress[9].Record.Stage.StageServing == nil {
			t.Fatal("real Docker stage start did not bind sequence ten")
		}
		boundBytes := liveGatewayRebindProgressBytes(t, bound)
		for index := range beforeBytes {
			if !bytes.Equal(beforeBytes[index], boundBytes[index]) ||
				!reflect.DeepEqual(beforeStart.Progress[index].Record, bound.Progress[index].Record) {
				t.Fatalf("stage start rewrote protected history at sequence %d", index+1)
			}
		}
		serving := *bound.Progress[9].Record.Stage.StageServing
		if bound.Progress[9].Record.PreviousDigest != beforeStart.Progress[8].Record.Digest ||
			!validGatewayRebindStageServingBinding(intent, beforeStart.Progress[8].Record, serving) {
			t.Fatal("real Docker stage start installed an invalid sequence-ten binding")
		}
		physical, inspectErr := actingDriver.inspect(fixture.ctx, intent)
		if inspectErr != nil || !validGatewayRebindRunningStageContainerObservation(intent,
			*bound.Progress[9].Record.Stage, physical) ||
			physical.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork].EndpointID != serving.EndpointID ||
			!proveGatewayRebindStagePublication(fixture.ctx, startBinding, containerBinding.ID,
				actingDriver.hostProbe, actingDriver.containerProbe) {
			t.Fatal("real Docker stage did not retain exact runtime and selected-address publication")
		}
		replayed, restartErr := New(fixture.runner, fixture.ingress.options)
		if restartErr != nil {
			t.Fatal("restart Manager before sequence-ten replay")
		}
		replayDriver := &liveGatewayRebindStageStartDriver{
			managerGatewayRebindStageStartDriver: managerGatewayRebindStageStartDriver{manager: replayed},
		}
		if err := replayed.startGatewayRebindSuccessorStageWithDriver(fixture.ctx, repository,
			liveGatewayRebindStageProductionReads(replayed), replayed.inspectGatewayRebindDocker,
			replayDriver, baseTime.Add(10*time.Nanosecond), nil); err != nil {
			failLiveIngress(t, "fresh-Manager replay exact serving stage", err)
		}
		replayedHistory, scanErr := replayed.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || !reflect.DeepEqual(boundBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) ||
			replayDriver.startCalls != 0 || replayDriver.stopCalls != 0 {
			t.Fatal("fresh-Manager replay changed sequence ten or dispatched another Docker effect")
		}
		for _, checkpoint := range afterServing {
			checkpoint(fixture, repository, intent, baseTime)
		}
	}
	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		t.Fatal("stage start changed the prepared SQLite claim")
	}
	afterAccess, err := repository.CurrentAppAccess(fixture.ctx, spec.appID)
	if err != nil || !reflect.DeepEqual(beforeAccess, afterAccess) {
		t.Fatal("stage start changed predecessor app access or its LAN URL input")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		t.Fatal("stage start changed protected predecessor")
	}
	afterRoute, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatal("stage start changed the active application route or URL")
	}
	afterDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) {
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("stage start changed predecessor Docker resources")
	}
	afterDockerDigest, err := gatewayRebindPredecessorDockerDigest(afterDocker, afterState.Identity)
	clearGatewayV2DockerObservation(&afterDocker)
	if err != nil || beforeDockerDigest != afterDockerDigest {
		t.Fatal("stage start changed exact predecessor Docker state")
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("stage start or replay forwarded an application request")
	}
}

func cleanupLiveGatewayRebindStageStartChain(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) {
	t.Helper()
	if container != nil {
		history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err == nil && (len(history.Progress) == 6 || len(history.Progress) == 8) {
			cleanupLiveGatewayRebindStoppedStageContainerChain(t, fixture, intent, networkID,
				config, data, container)
			return
		}
		if err != nil || len(history.Progress) < 9 || len(history.Progress) > 12 ||
			history.Progress[8].Record.Stage == nil ||
			history.Progress[8].Record.Stage.StageStartIntent == nil {
			t.Error("live stage-start cleanup lacks exact protected start intent; retaining resources")
			return
		}
		if _, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID,
			config, data, container); !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if len(history.Progress) >= 11 && !liveGatewayRebindFinalConfigCleanupInventory(t, fixture, ctx, intent, history) {
			return
		}
		driver := managerGatewayRebindStageStartDriver{manager: fixture.ingress}
		observed, inspectErr := driver.inspect(ctx, intent)
		if inspectErr != nil {
			t.Error("live stage-start cleanup cannot inspect exact bound stage; retaining resources")
			return
		}
		if gatewayRebindStageObservationMayBeLive(observed) {
			if len(history.Progress) < 10 || history.Progress[9].Record.Stage == nil ||
				!validGatewayRebindRunningStageContainerObservation(intent,
					*history.Progress[9].Record.Stage, observed) ||
				observed.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork].EndpointID !=
					history.Progress[9].Record.Stage.StageServing.EndpointID {
				t.Error("live stage-start cleanup found an uncertain running stage; retaining resources")
				return
			}
			if err := driver.stop(ctx, container.ID); err != nil {
				t.Error("live stage-start cleanup could not stop the exact bound container; retaining resources")
				return
			}
		}
		stopped, inspectErr := driver.inspect(ctx, intent)
		if inspectErr != nil || !validGatewayRebindStageContainerObservation(intent,
			*history.Progress[8].Record.Stage, stopped, container) ||
			!proveGatewayRebindStagePublicationWithdrawn(ctx,
				*history.Progress[8].Record.Stage.StageStartIntent, driver.hostProbe) {
			t.Error("live stage-start cleanup cannot prove exact stop and listener withdrawal; retaining resources")
			return
		}
	}
	cleanupLiveGatewayRebindStoppedStageContainerChain(t, fixture, intent, networkID, config, data, container)
}

var _ gatewayRebindStageStartDriver = (*liveGatewayRebindStageStartDriver)(nil)
