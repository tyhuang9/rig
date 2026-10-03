package generatedingress

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

// TestLiveGatewayRebindSuccessorConfigVolumeStage proves the second private
// successor effect on a disposable default-local Linux Docker daemon.
func TestLiveGatewayRebindSuccessorConfigVolumeStage(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "b1111111-1111-4111-8111-111111111111",
		planID:           "b2222222-2222-4222-8222-222222222222",
		operationID:      "b3333333-3333-4333-8333-333333333333",
		profileRevision:  "b4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-config-volume-stage",
		applicationReply: "gateway-v2-rebind-config-volume-stage",
		countRequests:    true,
	})
	db, err := database.Open(fixture.stateRoot)
	if err != nil {
		t.Fatal("open live rebind config-volume SQLite fixture")
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
		t.Fatal("live config-volume successor did not pass zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("live config-volume prepared claim did not bind predecessor")
	}

	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("production config-volume successor preflight rejected present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("config-volume protected-intent observation differed from production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve config-volume successor intent generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact config-volume successor protected intent")
	}

	networkDriver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	image, found, err := networkDriver.inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned live gateway image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct live config-volume successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install live config-volume successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt:            baseTime.Add(time.Nanosecond),
		ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct live config-volume stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install live config-volume stage intent progress")
	}

	var networkID string
	var volumeBinding *gatewayRebindStageConfigVolumeBinding
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStageConfigVolume(t, fixture, intent, networkID, volumeBinding)
		cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, networkID)
	})
	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before config-volume stage")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before config-volume stage")
	}
	defer clearGatewayV2DockerObservation(&beforeDocker)
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted predecessor requests did not settle before config-volume stage")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(2*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor network before config volume", err)
	}
	beforeVolume, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeVolume.Progress) != 3 || beforeVolume.Progress[2].Record.Stage == nil ||
		beforeVolume.Progress[2].Record.Stage.Network == nil {
		t.Fatal("live successor network did not bind exact sequence three")
	}
	networkID = beforeVolume.Progress[2].Record.Stage.Network.ID
	beforeVolumeBytes := liveGatewayRebindProgressBytes(t, beforeVolume)
	network, err := networkDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, network, networkID) {
		t.Fatal("live successor network was not exact before config volume")
	}
	networkDelta, err := readGatewayRebindStageNetworkPhysicalObservation(
		fixture.ctx, reads, networkDriver, intent, networkID)
	if err != nil || !validLiveGatewayRebindStagePhysicalDelta(intent, networkDelta, networkID) {
		t.Fatal("live successor network lacked the exact Linux bridge delta")
	}
	volumeDriver := managerGatewayRebindStageConfigVolumeDriver{manager: fixture.ingress}
	absent, err := volumeDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, absent, networkID, nil) {
		t.Fatal("config volume or other successor resource existed before sequence four")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorConfigVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(3*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor config volume", err)
	}
	boundHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(boundHistory.Progress) != 4 {
		t.Fatal("live successor config volume did not install sequence four")
	}
	for i := range beforeVolume.Progress {
		if !reflect.DeepEqual(beforeVolume.Progress[i], boundHistory.Progress[i]) {
			t.Fatal("config-volume stage changed an earlier protected record")
		}
	}
	boundBytes := liveGatewayRebindProgressBytes(t, boundHistory)
	if !reflect.DeepEqual(beforeVolumeBytes, boundBytes[:len(beforeVolumeBytes)]) {
		t.Fatal("config-volume stage changed earlier protected record bytes")
	}
	bound := boundHistory.Progress[3].Record
	ownershipDigest, digestErr := gatewayRebindStageConfigVolumeOwnershipDigest(intent)
	if digestErr != nil || bound.PreviousDigest != beforeVolume.Progress[2].Record.Digest ||
		bound.Stage == nil || bound.Stage.ConfigVolume == nil ||
		bound.Stage.ConfigVolume.Name != intent.Intent.Identity.ConfigVolume ||
		bound.Stage.ConfigVolume.OwnershipDigest != ownershipDigest {
		t.Fatal("live successor config-volume progress has an invalid binding")
	}
	withoutVolume := *bound.Stage
	withoutVolume.ConfigVolume = nil
	if !reflect.DeepEqual(withoutVolume, *beforeVolume.Progress[2].Record.Stage) {
		t.Fatal("config-volume binding changed prepared successor stage or network")
	}
	volumeBinding = bound.Stage.ConfigVolume
	physical, err := volumeDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, physical, networkID, volumeBinding) ||
		physical.ConfigVolumeIdentity.Mountpoint != volumeBinding.Mountpoint ||
		physical.ConfigVolumeIdentity.CreatedAt != volumeBinding.CreatedAt ||
		!reflect.DeepEqual(physical.ConfigVolume.Labels, gatewayRebindStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)) ||
		!validOwnedNameSet(physical.OwnedVolumes, intent.Intent.Identity.ConfigVolume) {
		t.Fatal("production Docker did not expose exactly the bound config volume")
	}
	createdDelta, err := readGatewayRebindStageConfigVolumePhysicalObservation(
		fixture.ctx, reads, volumeDriver, intent, networkID, volumeBinding)
	if err != nil || createdDelta.ConfigVolume == nil || *createdDelta.ConfigVolume != *volumeBinding ||
		createdDelta.NetworkID != networkDelta.NetworkID ||
		createdDelta.NetworkOwnershipDigest != networkDelta.OwnershipDigest ||
		!reflect.DeepEqual(createdDelta.Candidates, networkDelta.Candidates) ||
		!reflect.DeepEqual(createdDelta.HostRoutes, networkDelta.HostRoutes) ||
		!reflect.DeepEqual(createdDelta.HostInterfaces, networkDelta.HostInterfaces) ||
		!reflect.DeepEqual(createdDelta.DockerIDs, networkDelta.DockerIDs) ||
		!reflect.DeepEqual(createdDelta.DockerPrefixes, networkDelta.DockerPrefixes) {
		t.Fatal("config-volume creation changed the successor network or Linux host delta")
	}
	if err := fixture.ingress.stageGatewayRebindSuccessorConfigVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(4*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "replay live rebind successor config volume", err)
	}
	replayedHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(boundHistory, replayedHistory) ||
		!reflect.DeepEqual(boundBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) {
		t.Fatal("live config-volume replay changed protected records or bytes")
	}
	replayed, err := volumeDriver.inspect(fixture.ctx, intent)
	if err != nil || !reflect.DeepEqual(physical, replayed) ||
		!validGatewayRebindStageConfigVolumeObservation(intent, replayed, networkID, volumeBinding) {
		t.Fatal("live config-volume replay changed exact Docker identity")
	}
	replayedDelta, err := readGatewayRebindStageConfigVolumePhysicalObservation(
		fixture.ctx, reads, volumeDriver, intent, networkID, volumeBinding)
	if err != nil || !reflect.DeepEqual(createdDelta, replayedDelta) {
		t.Fatal("live config-volume replay changed the physical host observation")
	}

	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		t.Fatal("config-volume stage changed prepared SQLite claim")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		t.Fatal("config-volume stage changed protected predecessor")
	}
	afterRoute, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatal("config-volume stage changed active application route")
	}
	afterDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) ||
		!reflect.DeepEqual(beforeDocker, afterDocker) {
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("config-volume stage changed predecessor Docker resources")
	}
	clearGatewayV2DockerObservation(&afterDocker)
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("config-volume stage or replay forwarded an application request")
	}
}

func liveGatewayRebindProgressBytes(t *testing.T, history gatewayRebindProtectedIntentHistory) [][]byte {
	t.Helper()
	result := make([][]byte, len(history.Progress))
	for i, progress := range history.Progress {
		bytes, err := os.ReadFile(progress.Store.path)
		if err != nil {
			t.Fatal("read exact protected rebind progress bytes")
		}
		result[i] = bytes
	}
	return result
}

func cleanupLiveGatewayRebindStageConfigVolume(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, binding *gatewayRebindStageConfigVolumeBinding,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	driver := managerGatewayRebindStageConfigVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil {
		t.Error("inspect live config volume for exact cleanup")
		return
	}
	if !observed.ConfigVolumeFound {
		if binding != nil || (validContainerID(networkID) &&
			!validGatewayRebindStageConfigVolumeObservation(intent, observed, networkID, nil)) {
			t.Error("live config-volume cleanup found missing binding or uncertain residue")
		}
		return
	}
	if binding == nil || !validGatewayRebindStageConfigVolumeObservation(intent, observed, networkID, binding) {
		t.Error("live config-volume cleanup has no exact protected ownership; retaining volume")
		return
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", binding.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor config volume")
		return
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, residue, networkID, nil) {
		t.Error("exact live config-volume cleanup left owned residue")
	}
}
