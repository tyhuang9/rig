package generatedingress

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

// TestLiveGatewayRebindSuccessorDataVolumeStage proves the third private
// successor effect on a disposable default-local Linux Docker daemon.
func TestLiveGatewayRebindSuccessorDataVolumeStage(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "c1111111-1111-4111-8111-111111111111",
		planID:           "c2222222-2222-4222-8222-222222222222",
		operationID:      "c3333333-3333-4333-8333-333333333333",
		profileRevision:  "c4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-data-volume-stage",
		applicationReply: "gateway-v2-rebind-data-volume-stage",
		countRequests:    true,
	})
	db, err := database.Open(fixture.stateRoot)
	if err != nil {
		t.Fatal("open live rebind data-volume SQLite fixture")
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := appaccess.New(db)
	profile := prepareLiveGatewayRebindStagePredecessor(t, fixture, db, repository)
	proposal := seedLiveGatewayRebindPublicPassiveLineage(t, fixture, db, repository, profile,
		appaccess.GatewayProfileSpec{
			SelectedIPv4: fixture.request.Profile.SelectedIPv4,
			InterfaceID:  fixture.request.Profile.InterfaceID,
			PortStart:    fixture.port,
			PortEnd:      fixture.port,
		})
	predecessorState, predecessorJournal, predecessorStore := liveGatewayV2LoadDurableOperation(t, fixture)
	preclaim, err := repository.GatewayRebindPreclaimSnapshot(fixture.ctx, proposal)
	if err != nil || !gatewayRebindPreclaimMatches(preclaim, predecessorState, predecessorJournal) {
		t.Fatal("live data-volume successor did not pass zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("live data-volume prepared claim did not bind predecessor")
	}

	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("production data-volume successor preflight rejected present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("data-volume protected-intent observation differed from production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve data-volume successor intent generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact data-volume successor protected intent")
	}

	networkDriver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	image, found, err := networkDriver.inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned live gateway image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct live data-volume successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install live data-volume successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt:            baseTime.Add(time.Nanosecond),
		ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct live data-volume stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install live data-volume stage intent progress")
	}

	var networkID string
	var configBinding *gatewayRebindStageConfigVolumeBinding
	var dataBinding *gatewayRebindStageDataVolumeBinding
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStageDataVolumeChain(
			t, fixture, intent, networkID, configBinding, dataBinding)
	})
	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before data-volume stage")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before data-volume stage")
	}
	defer clearGatewayV2DockerObservation(&beforeDocker)
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted predecessor requests did not settle before data-volume stage")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(2*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor network before data volume", err)
	}
	networkHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(networkHistory.Progress) != 3 || networkHistory.Progress[2].Record.Stage == nil ||
		networkHistory.Progress[2].Record.Stage.Network == nil {
		t.Fatal("live successor network did not bind exact sequence three")
	}
	networkID = networkHistory.Progress[2].Record.Stage.Network.ID
	if err := fixture.ingress.stageGatewayRebindSuccessorConfigVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(3*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor config volume before data volume", err)
	}
	beforeData, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeData.Progress) != 4 || beforeData.Progress[3].Record.Stage == nil ||
		beforeData.Progress[3].Record.Stage.ConfigVolume == nil {
		t.Fatal("live successor config volume did not bind exact sequence four")
	}
	configBinding = beforeData.Progress[3].Record.Stage.ConfigVolume
	beforeDataBytes := liveGatewayRebindProgressBytes(t, beforeData)
	configDriver := managerGatewayRebindStageConfigVolumeDriver{manager: fixture.ingress}
	configObserved, err := configDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, configObserved, networkID, configBinding) {
		t.Fatal("live successor config volume was not exact before data volume")
	}
	configDelta, err := readGatewayRebindStageConfigVolumePhysicalObservation(
		fixture.ctx, reads, configDriver, intent, networkID, configBinding)
	if err != nil || configDelta.ConfigVolume == nil || *configDelta.ConfigVolume != *configBinding {
		t.Fatal("live successor config volume lacked the exact physical binding")
	}
	dataDriver := managerGatewayRebindStageDataVolumeDriver{manager: fixture.ingress}
	absent, err := dataDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, absent, networkID, *configBinding, nil) {
		t.Fatal("data volume or other successor resource existed before sequence five")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorDataVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(4*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor data volume", err)
	}
	boundHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(boundHistory.Progress) != 5 {
		t.Fatal("live successor data volume did not install sequence five")
	}
	for i := range beforeData.Progress {
		if !reflect.DeepEqual(beforeData.Progress[i], boundHistory.Progress[i]) {
			t.Fatal("data-volume stage changed an earlier protected record")
		}
	}
	boundBytes := liveGatewayRebindProgressBytes(t, boundHistory)
	if !reflect.DeepEqual(beforeDataBytes, boundBytes[:len(beforeDataBytes)]) {
		t.Fatal("data-volume stage changed earlier protected record bytes")
	}
	bound := boundHistory.Progress[4].Record
	ownershipDigest, digestErr := gatewayRebindStageDataVolumeOwnershipDigest(intent)
	if digestErr != nil || bound.PreviousDigest != beforeData.Progress[3].Record.Digest ||
		bound.Stage == nil || bound.Stage.ConfigVolume == nil || bound.Stage.DataVolume == nil ||
		bound.Stage.DataVolume.Name != intent.Intent.Identity.DataVolume ||
		bound.Stage.DataVolume.OwnershipDigest != ownershipDigest {
		t.Fatal("live successor data-volume progress has an invalid binding")
	}
	withoutData := *bound.Stage
	withoutData.DataVolume = nil
	if !reflect.DeepEqual(withoutData, *beforeData.Progress[3].Record.Stage) {
		t.Fatal("data-volume binding changed prepared successor stage, network, or config volume")
	}
	dataBinding = bound.Stage.DataVolume
	physical, err := dataDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(
		intent, physical, networkID, *configBinding, dataBinding) ||
		physical.DataVolumeIdentity.Mountpoint != dataBinding.Mountpoint ||
		physical.DataVolumeIdentity.CreatedAt != dataBinding.CreatedAt ||
		physical.DataVolume.Name != dataBinding.Name || physical.DataVolume.Driver != "local" ||
		physical.DataVolume.Scope != "local" || len(physical.DataVolume.Options) != 0 ||
		!reflect.DeepEqual(physical.DataVolume.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2DataVolumeRole)) ||
		physical.ConfigVolumeIdentity.Mountpoint != configBinding.Mountpoint ||
		physical.ConfigVolumeIdentity.CreatedAt != configBinding.CreatedAt ||
		!validOwnedNameSet(physical.OwnedVolumes,
			intent.Intent.Identity.ConfigVolume, intent.Intent.Identity.DataVolume) {
		t.Fatal("production Docker did not expose exactly the bound config and data volumes")
	}
	createdDelta, err := readGatewayRebindStageDataVolumePhysicalObservation(
		fixture.ctx, reads, dataDriver, intent, networkID, *configBinding, dataBinding)
	if err != nil || createdDelta.DataVolume == nil || *createdDelta.DataVolume != *dataBinding ||
		createdDelta.ConfigVolume != *configBinding ||
		createdDelta.NetworkID != configDelta.NetworkID ||
		createdDelta.NetworkOwnershipDigest != configDelta.NetworkOwnershipDigest ||
		!reflect.DeepEqual(createdDelta.Candidates, configDelta.Candidates) ||
		!reflect.DeepEqual(createdDelta.HostRoutes, configDelta.HostRoutes) ||
		!reflect.DeepEqual(createdDelta.HostInterfaces, configDelta.HostInterfaces) ||
		!reflect.DeepEqual(createdDelta.DockerIDs, configDelta.DockerIDs) ||
		!reflect.DeepEqual(createdDelta.DockerPrefixes, configDelta.DockerPrefixes) {
		t.Fatal("data-volume creation changed the successor network, config volume, or Linux host delta")
	}
	restarted, err := New(fixture.runner, fixture.ingress.options)
	if err != nil {
		t.Fatal("restart generated ingress manager before data-volume replay")
	}
	restartReads := liveGatewayRebindStageProductionReads(restarted)
	if err := restarted.stageGatewayRebindSuccessorDataVolume(fixture.ctx, repository, restartReads,
		restarted.inspectGatewayRebindDocker, baseTime.Add(5*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "replay live rebind successor data volume", err)
	}
	replayedHistory, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(boundHistory, replayedHistory) ||
		!reflect.DeepEqual(boundBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) {
		t.Fatal("live data-volume replay changed protected records or bytes")
	}
	replayedDriver := managerGatewayRebindStageDataVolumeDriver{manager: restarted}
	replayed, err := replayedDriver.inspect(fixture.ctx, intent)
	if err != nil || !reflect.DeepEqual(physical, replayed) ||
		!validGatewayRebindStageDataVolumeObservation(intent, replayed, networkID, *configBinding, dataBinding) {
		t.Fatal("live data-volume replay changed exact Docker identity")
	}
	replayedDelta, err := readGatewayRebindStageDataVolumePhysicalObservation(
		fixture.ctx, restartReads, replayedDriver, intent, networkID, *configBinding, dataBinding)
	if err != nil || !reflect.DeepEqual(createdDelta, replayedDelta) {
		t.Fatal("live data-volume replay changed the physical host observation")
	}

	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		t.Fatal("data-volume stage changed prepared SQLite claim")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		t.Fatal("data-volume stage changed protected predecessor")
	}
	afterRoute, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatal("data-volume stage changed active application route")
	}
	afterDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) ||
		!reflect.DeepEqual(beforeDocker, afterDocker) {
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("data-volume stage changed predecessor Docker resources")
	}
	clearGatewayV2DockerObservation(&afterDocker)
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("data-volume stage or replay forwarded an application request")
	}
}

func cleanupLiveGatewayRebindStageDataVolumeChain(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	binding *gatewayRebindStageDataVolumeBinding,
) {
	t.Helper()
	if !cleanupLiveGatewayRebindStageDataVolume(t, fixture, intent, networkID, config, binding) {
		return
	}
	if !cleanupLiveGatewayRebindStageConfigVolumeAfterData(t, fixture, intent, networkID, config, binding) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	driver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, observed, networkID) {
		t.Error("live data-volume cleanup could not prove both volumes absent; retaining network")
		return
	}
	cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, networkID)
}

func cleanupLiveGatewayRebindStageDataVolume(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	binding *gatewayRebindStageDataVolumeBinding,
) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) == 0 || len(history.Progress) != 5 ||
		!reflect.DeepEqual(history.Intents[len(history.Intents)-1].Intent, intent) ||
		history.Progress[4].Record.Stage == nil || history.Progress[4].Record.Stage.Network == nil ||
		history.Progress[4].Record.Stage.Network.ID != networkID ||
		history.Progress[4].Record.Stage.ConfigVolume == nil ||
		history.Progress[4].Record.Stage.DataVolume == nil || config == nil || binding == nil ||
		*history.Progress[4].Record.Stage.ConfigVolume != *config ||
		*history.Progress[4].Record.Stage.DataVolume != *binding {
		t.Error("live data-volume cleanup has no exact protected sequence-five binding; retaining resources")
		return false
	}
	driver := managerGatewayRebindStageDataVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil {
		t.Error("inspect live data volume for exact cleanup")
		return false
	}
	if !observed.DataVolumeFound {
		t.Error("live data-volume cleanup found a missing bound volume; retaining resources")
		return false
	}
	if !validContainerID(networkID) ||
		!validGatewayRebindStageDataVolumeObservation(intent, observed, networkID, *config, binding) {
		t.Error("live data-volume cleanup has no exact protected ownership; retaining volume")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", binding.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor data volume")
		return false
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, residue, networkID, *config, nil) {
		t.Error("exact live data-volume cleanup left owned residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStageConfigVolumeAfterData(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding,
) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) == 0 || len(history.Progress) != 5 ||
		!reflect.DeepEqual(history.Intents[len(history.Intents)-1].Intent, intent) ||
		history.Progress[4].Record.Stage == nil || history.Progress[4].Record.Stage.Network == nil ||
		history.Progress[4].Record.Stage.Network.ID != networkID ||
		history.Progress[4].Record.Stage.ConfigVolume == nil ||
		history.Progress[4].Record.Stage.DataVolume == nil || config == nil || data == nil ||
		*history.Progress[4].Record.Stage.ConfigVolume != *config ||
		*history.Progress[4].Record.Stage.DataVolume != *data {
		t.Error("live config-volume cleanup has no exact protected sequence-five binding; retaining resources")
		return false
	}
	driver := managerGatewayRebindStageConfigVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validContainerID(networkID) ||
		!validGatewayRebindStageConfigVolumeObservation(intent, observed, networkID, config) {
		t.Error("live config-volume cleanup has no exact protected ownership; retaining volume")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", config.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor config volume after data volume")
		return false
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, residue, networkID, nil) {
		t.Error("exact live config-volume cleanup left owned residue after data volume")
		return false
	}
	return true
}
