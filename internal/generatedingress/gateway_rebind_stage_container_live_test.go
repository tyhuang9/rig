package generatedingress

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

// TestLiveGatewayRebindSuccessorStoppedStageContainer proves that the fourth
// private successor effect creates one exact, stopped container on a
// disposable default-local Linux Docker daemon. It must remain inert until a
// later, separately guarded cutover stage.
func TestLiveGatewayRebindSuccessorStoppedStageContainer(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "d1111111-1111-4111-8111-111111111111",
		planID:           "d2222222-2222-4222-8222-222222222222",
		operationID:      "d3333333-3333-4333-8333-333333333333",
		profileRevision:  "d4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-stopped-stage-container",
		applicationReply: "gateway-v2-rebind-stopped-stage-container",
		countRequests:    true,
	})
	db, err := database.Open(fixture.stateRoot)
	if err != nil {
		t.Fatal("open live rebind stopped-container SQLite fixture")
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
		t.Fatal("live stopped-container successor did not pass zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("live stopped-container prepared claim did not bind predecessor")
	}

	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("production stopped-container successor preflight rejected present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("stopped-container protected-intent observation differed from production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve stopped-container successor intent generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact stopped-container successor protected intent")
	}

	networkDriver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	image, found, err := networkDriver.inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned live gateway image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct live stopped-container successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install live stopped-container successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt:            baseTime.Add(time.Nanosecond),
		ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct live stopped-container stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install live stopped-container stage intent progress")
	}

	var networkID string
	var configBinding *gatewayRebindStageConfigVolumeBinding
	var dataBinding *gatewayRebindStageDataVolumeBinding
	var containerBinding *gatewayRebindStageContainerBinding
	// This must be registered before the first Docker mutation. If an effect
	// becomes ambiguous, cleanup retains it and the CI residue gate fails.
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStoppedStageContainerChain(t, fixture, intent, networkID,
			configBinding, dataBinding, containerBinding)
	})

	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before stopped-container stage")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before stopped-container stage")
	}
	beforeDockerDigest, err := gatewayRebindEffectBoundaryDockerDigest(beforeDocker, predecessorState.Identity)
	clearGatewayV2DockerObservation(&beforeDocker)
	if err != nil {
		t.Fatal("digest exact predecessor Docker state before stopped-container stage")
	}
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted predecessor requests did not settle before stopped-container stage")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(2*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor network before stopped container", err)
	}
	networkHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(networkHistory.Progress) != 3 || networkHistory.Progress[2].Record.Stage == nil ||
		networkHistory.Progress[2].Record.Stage.Network == nil {
		t.Fatal("live successor network did not bind exact sequence three")
	}
	networkID = networkHistory.Progress[2].Record.Stage.Network.ID
	if err := fixture.ingress.stageGatewayRebindSuccessorConfigVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(3*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor config volume before stopped container", err)
	}
	configHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(configHistory.Progress) != 4 || configHistory.Progress[3].Record.Stage == nil ||
		configHistory.Progress[3].Record.Stage.ConfigVolume == nil {
		t.Fatal("live successor config volume did not bind exact sequence four")
	}
	configBinding = configHistory.Progress[3].Record.Stage.ConfigVolume
	if err := fixture.ingress.stageGatewayRebindSuccessorDataVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(4*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor data volume before stopped container", err)
	}
	beforeContainer, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeContainer.Progress) != 5 || beforeContainer.Progress[4].Record.Stage == nil ||
		beforeContainer.Progress[4].Record.Stage.DataVolume == nil {
		t.Fatal("live successor data volume did not bind exact sequence five")
	}
	dataBinding = beforeContainer.Progress[4].Record.Stage.DataVolume
	beforeContainerBytes := liveGatewayRebindProgressBytes(t, beforeContainer)
	dataDriver := managerGatewayRebindStageDataVolumeDriver{manager: fixture.ingress}
	dataObservation, err := dataDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, dataObservation, networkID,
		*configBinding, dataBinding) {
		t.Fatal("live successor data volume was not exact before stopped-container stage")
	}
	dataDelta, err := readGatewayRebindStageDataVolumePhysicalObservation(
		fixture.ctx, reads, dataDriver, intent, networkID, *configBinding, dataBinding)
	if err != nil || dataDelta.DataVolume == nil || *dataDelta.DataVolume != *dataBinding {
		t.Fatal("live successor data volume lacked its exact physical receipt")
	}
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	absent, err := containerDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent,
		*beforeContainer.Progress[4].Record.Stage, absent, nil) {
		t.Fatal("stopped container or another successor container existed before sequence six")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorContainer(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(5*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind stopped successor container", err)
	}
	boundHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(boundHistory.Progress) != 6 {
		t.Fatal("live stopped successor container did not install sequence six")
	}
	for i := range beforeContainer.Progress {
		if !reflect.DeepEqual(beforeContainer.Progress[i], boundHistory.Progress[i]) {
			t.Fatal("stopped-container stage changed an earlier protected record")
		}
	}
	boundBytes := liveGatewayRebindProgressBytes(t, boundHistory)
	if !reflect.DeepEqual(beforeContainerBytes, boundBytes[:len(beforeContainerBytes)]) {
		t.Fatal("stopped-container stage changed earlier protected record bytes")
	}
	bound := boundHistory.Progress[5].Record
	ownershipDigest, ownershipErr := gatewayRebindStageContainerOwnershipDigest(intent)
	configurationDigest, configurationErr := gatewayRebindStageContainerConfigurationDigest(intent,
		*beforeContainer.Progress[4].Record.Stage)
	if ownershipErr != nil || configurationErr != nil || bound.PreviousDigest != beforeContainer.Progress[4].Record.Digest ||
		bound.Stage == nil || bound.Stage.StageContainer == nil ||
		bound.Stage.StageContainer.OwnershipDigest != ownershipDigest ||
		bound.Stage.StageContainer.ConfigurationDigest != configurationDigest ||
		!validContainerID(bound.Stage.StageContainer.ID) {
		t.Fatal("live stopped-container progress has an invalid exact binding")
	}
	withoutContainer := *bound.Stage
	withoutContainer.StageContainer = nil
	if !reflect.DeepEqual(withoutContainer, *beforeContainer.Progress[4].Record.Stage) {
		t.Fatal("stopped-container binding changed the prepared successor network or volumes")
	}
	containerBinding = bound.Stage.StageContainer
	physical, err := containerDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, *bound.Stage, physical, containerBinding) ||
		physical.StageContainer.Running || physical.StageContainer.Restarting || physical.StageRuntime.Paused ||
		physical.StageRuntime.Dead || physical.StageRuntime.RestartCount != 0 ||
		gatewayV2HasEffectivePortBinding(physical.StageRuntime.EffectivePortBindings) {
		t.Fatal("production Docker did not expose an exact inert stopped successor container")
	}
	configured, configuredOK := physical.StageRuntime.ConfiguredNetworks[intent.Intent.Identity.IngressNetwork]
	if !configuredOK || configured.EndpointID != "" || configured.IPAddress != "" ||
		configured.IPAMConfig == nil || configured.IPAMConfig.IPv4Address != intent.Intent.Network.ContainerIPv4 ||
		!validGatewayRebindStageContainerPortBindings(physical.StageContainer.PortBindings, intent) {
		t.Fatal("stopped successor container lost its exact configured network or port settings")
	}
	createdDelta, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, reads, containerDriver, intent, *bound.Stage, containerBinding)
	if err != nil || createdDelta.StageContainer == nil || *createdDelta.StageContainer != *containerBinding ||
		createdDelta.ConfigVolume != *configBinding || createdDelta.DataVolume != *dataBinding ||
		createdDelta.NetworkID != dataDelta.NetworkID ||
		createdDelta.NetworkOwnershipDigest != dataDelta.NetworkOwnershipDigest ||
		!reflect.DeepEqual(createdDelta.Candidates, dataDelta.Candidates) ||
		!reflect.DeepEqual(createdDelta.HostRoutes, dataDelta.HostRoutes) ||
		!reflect.DeepEqual(createdDelta.HostInterfaces, dataDelta.HostInterfaces) ||
		!reflect.DeepEqual(createdDelta.DockerIDs, dataDelta.DockerIDs) ||
		!reflect.DeepEqual(createdDelta.DockerPrefixes, dataDelta.DockerPrefixes) {
		t.Fatal("stopped-container creation changed the successor host topology or volume receipts")
	}

	restarted, err := New(fixture.runner, fixture.ingress.options)
	if err != nil {
		t.Fatal("restart generated ingress manager before stopped-container replay")
	}
	restartReads := liveGatewayRebindStageProductionReads(restarted)
	if err := restarted.stageGatewayRebindSuccessorContainer(fixture.ctx, repository, restartReads,
		restarted.inspectGatewayRebindDocker, baseTime.Add(6*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "replay live rebind stopped successor container", err)
	}
	replayedHistory, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(boundHistory, replayedHistory) ||
		!reflect.DeepEqual(boundBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) {
		t.Fatal("live stopped-container replay changed protected records or bytes")
	}
	replayedDriver := managerGatewayRebindStageContainerDriver{manager: restarted}
	replayed, err := replayedDriver.inspect(fixture.ctx, intent)
	if err != nil {
		t.Fatal("fresh-manager stopped-container replay inspection failed")
	}
	if !reflect.DeepEqual(physical, replayed) {
		t.Fatal("fresh-manager stopped-container replay physical snapshot changed")
	}
	if !validGatewayRebindStageContainerObservation(intent, *bound.Stage, replayed, containerBinding) {
		t.Fatal("fresh-manager stopped-container replay exact binding proof failed")
	}
	if !validOwnedNameSet(replayed.OwnedContainers, intent.Intent.Identity.StageContainer) {
		t.Fatal("fresh-manager stopped-container replay owned-container census changed")
	}
	replayedDelta, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, restartReads, replayedDriver, intent, *bound.Stage, containerBinding)
	if err != nil || !reflect.DeepEqual(createdDelta, replayedDelta) {
		t.Fatal("fresh-manager replay changed the stopped-container physical observation")
	}

	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		t.Fatal("stopped-container stage changed prepared SQLite claim")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		t.Fatal("stopped-container stage changed protected predecessor")
	}
	afterRoute, err := fixture.ingress.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatal("stopped-container stage changed active application route")
	}
	afterDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) {
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("stopped-container stage changed predecessor Docker resources")
	}
	afterDockerDigest, err := gatewayRebindEffectBoundaryDockerDigest(afterDocker, afterState.Identity)
	clearGatewayV2DockerObservation(&afterDocker)
	if err != nil || afterDockerDigest != beforeDockerDigest {
		t.Fatal("stopped-container stage changed predecessor Docker resources")
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("stopped-container stage or replay forwarded an application request")
	}
}

func cleanupLiveGatewayRebindStoppedStageContainerChain(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) {
	t.Helper()
	if container == nil {
		cleanupLiveGatewayRebindStoppedStagePreContainer(t, fixture, intent, networkID, config, data)
		return
	}
	if !cleanupLiveGatewayRebindStoppedStageContainer(t, fixture, intent, networkID, config, data, container) {
		return
	}
	if !cleanupLiveGatewayRebindStoppedStageDataVolume(t, fixture, intent, networkID, config, data, container) {
		return
	}
	if !cleanupLiveGatewayRebindStoppedStageConfigVolume(t, fixture, intent, networkID, config, data, container) {
		return
	}
	cleanupLiveGatewayRebindStoppedStageNetwork(t, fixture, intent, networkID, config, data, container)
}

// Cleanup may run after a failure in one of the three earlier, fully bound
// stages. Those histories are already covered by their own exact cleanup
// helpers. A container not bound into sequence six is never adopted or
// cleaned: it is an ambiguous create-before-bind outcome and must remain for
// the residue gate to expose.
func cleanupLiveGatewayRebindStoppedStagePreContainer(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding,
) {
	t.Helper()
	if networkID == "" {
		return
	}
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	observed, err := containerDriver.inspect(ctx, intent)
	if err != nil || observed.StageContainerFound || len(observed.OwnedContainers) != 0 {
		t.Error("live stopped-container cleanup found an unbound or uncertain stage container; retaining resources")
		return
	}
	if data != nil {
		cleanupLiveGatewayRebindStageDataVolumeChain(t, fixture, intent, networkID, config, data)
		return
	}
	if config != nil {
		if observed.DataVolumeFound || !validGatewayRebindStageConfigVolumeObservation(intent,
			gatewayRebindStageConfigVolumeObservation{
				Network: observed.Network, NetworkID: observed.NetworkID, NetworkFound: observed.NetworkFound,
				ConfigVolume: observed.ConfigVolume, ConfigVolumeIdentity: observed.ConfigVolumeIdentity,
				ConfigVolumeFound: observed.ConfigVolumeFound, OwnedVolumes: observed.OwnedVolumes,
				OwnedNetworks: observed.OwnedNetworks,
			}, networkID, config) {
			t.Error("live stopped-container cleanup found uncertain sequence-four resources; retaining resources")
			return
		}
		cleanupLiveGatewayRebindStageConfigVolume(t, fixture, intent, networkID, config)
		postConfig, postConfigErr := containerDriver.inspect(ctx, intent)
		if postConfigErr != nil || postConfig.ConfigVolumeFound || postConfig.DataVolumeFound ||
			postConfig.StageContainerFound || len(postConfig.OwnedContainers) != 0 || len(postConfig.OwnedVolumes) != 0 {
			t.Error("live stopped-container cleanup retained config-volume residue; retaining network")
			return
		}
		cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, networkID)
		return
	}
	if observed.ConfigVolumeFound || observed.DataVolumeFound || len(observed.OwnedVolumes) != 0 ||
		!validGatewayRebindStageNetworkObservation(intent, gatewayRebindStageNetworkObservation{
			Network: observed.Network, ID: observed.NetworkID, Found: observed.NetworkFound,
			OwnedNetworks: observed.OwnedNetworks,
		}, networkID) {
		t.Error("live stopped-container cleanup found uncertain sequence-three resources; retaining network")
		return
	}
	cleanupLiveGatewayRebindStageNetwork(t, fixture, intent, networkID)
}

func liveGatewayRebindStoppedStageCleanupLineage(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) (gatewayRebindStageIntent, bool) {
	t.Helper()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || (len(history.Progress) != 6 && len(history.Progress) != 8 &&
		len(history.Progress) != 9 && len(history.Progress) != 10 && len(history.Progress) != 11 && len(history.Progress) != 12) ||
		!reflect.DeepEqual(history.Intents[0].Intent, intent) || !validContainerID(networkID) ||
		config == nil || data == nil || container == nil {
		t.Error("live stopped-container cleanup has no exact protected stage lineage; retaining resources")
		return gatewayRebindStageIntent{}, false
	}
	fifth := history.Progress[4].Record.Stage
	sixth := history.Progress[5].Record.Stage
	if fifth == nil || sixth == nil || fifth.Network == nil || fifth.ConfigVolume == nil || fifth.DataVolume == nil ||
		fifth.Network.ID != networkID || *fifth.ConfigVolume != *config || *fifth.DataVolume != *data ||
		sixth.StageContainer == nil || *sixth.StageContainer != *container ||
		history.Progress[5].Record.PreviousDigest != history.Progress[4].Record.Digest {
		t.Error("live stopped-container cleanup protected receipt does not match resource bindings; retaining resources")
		return gatewayRebindStageIntent{}, false
	}
	withoutContainer := *sixth
	withoutContainer.StageContainer = nil
	if !reflect.DeepEqual(withoutContainer, *fifth) {
		t.Error("live stopped-container cleanup found a sequence-six stage that changed prior receipts; retaining resources")
		return gatewayRebindStageIntent{}, false
	}
	if len(history.Progress) >= 8 {
		seventh := history.Progress[6].Record
		eighth := history.Progress[7].Record
		sixthRecord := history.Progress[5].Record
		intentBinding, intentErr := gatewayRebindStageConfigIntentBindingFor(intent, sixthRecord)
		copyBinding, copyErr := gatewayRebindStageConfigCopyBindingFor(intent, seventh)
		if intentErr != nil || copyErr != nil || seventh.PreviousDigest != sixthRecord.Digest ||
			seventh.Stage == nil || seventh.Stage.StageConfigIntent == nil ||
			*seventh.Stage.StageConfigIntent != intentBinding ||
			eighth.PreviousDigest != seventh.Digest || eighth.Stage == nil ||
			eighth.Stage.StageConfigCopy == nil || *eighth.Stage.StageConfigCopy != copyBinding {
			t.Error("live stopped-container cleanup has no exact protected sequence-seven and sequence-eight receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutIntent := *seventh.Stage
		withoutIntent.StageConfigIntent = nil
		if !reflect.DeepEqual(withoutIntent, *sixth) {
			t.Error("live stopped-container cleanup found a sequence-seven stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutCopy := *eighth.Stage
		withoutCopy.StageConfigCopy = nil
		if !reflect.DeepEqual(withoutCopy, *seventh.Stage) {
			t.Error("live stopped-container cleanup found a sequence-eight stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
	}
	if len(history.Progress) >= 9 {
		eighth := history.Progress[7].Record
		ninth := history.Progress[8].Record
		binding, bindingErr := gatewayRebindStageStartIntentBindingFor(intent, eighth)
		if bindingErr != nil || ninth.PreviousDigest != eighth.Digest || ninth.Stage == nil ||
			ninth.Stage.StageStartIntent == nil || *ninth.Stage.StageStartIntent != binding {
			t.Error("live stopped-container cleanup has no exact protected sequence-nine start intent; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutStart := *ninth.Stage
		withoutStart.StageStartIntent = nil
		if !reflect.DeepEqual(withoutStart, *eighth.Stage) {
			t.Error("live stopped-container cleanup found a sequence-nine stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
	}
	if len(history.Progress) >= 10 {
		ninth := history.Progress[8].Record
		tenth := history.Progress[9].Record
		if tenth.PreviousDigest != ninth.Digest || tenth.Stage == nil || tenth.Stage.StageServing == nil ||
			!validGatewayRebindStageServingBinding(intent, ninth, *tenth.Stage.StageServing) {
			t.Error("live stopped-container cleanup has no exact protected sequence-ten serving receipt; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutServing := *tenth.Stage
		withoutServing.StageServing = nil
		if !reflect.DeepEqual(withoutServing, *ninth.Stage) {
			t.Error("live stopped-container cleanup found a sequence-ten stage that changed prior receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
	}
	if len(history.Progress) >= 11 {
		tenth := history.Progress[9].Record
		eleventh := history.Progress[10].Record
		if eleventh.Stage == nil || eleventh.Stage.FinalConfigIntent == nil ||
			!validGatewayRebindFinalConfigIntentBinding(intent, tenth, *eleventh.Stage.FinalConfigIntent) {
			t.Error("live cleanup lacks exact sequence-eleven final config intent; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		withoutFinalIntent := *eleventh.Stage
		withoutFinalIntent.FinalConfigIntent = nil
		if !reflect.DeepEqual(withoutFinalIntent, *tenth.Stage) {
			t.Error("live cleanup final config intent changed prior stage receipts; retaining resources")
			return gatewayRebindStageIntent{}, false
		}
		if len(history.Progress) == 12 {
			twelfth := history.Progress[11].Record
			if twelfth.Stage == nil || twelfth.Stage.FinalConfigCopy == nil ||
				!validGatewayRebindFinalConfigCopyBinding(intent, eleventh, *twelfth.Stage.FinalConfigCopy) {
				t.Error("live cleanup lacks exact sequence-twelve copy receipt; retaining resources")
				return gatewayRebindStageIntent{}, false
			}
			withoutFinalCopy := *twelfth.Stage
			withoutFinalCopy.FinalConfigCopy = nil
			if !reflect.DeepEqual(withoutFinalCopy, *eleventh.Stage) {
				t.Error("live cleanup copy receipt changed prior stage receipts; retaining resources")
				return gatewayRebindStageIntent{}, false
			}
		}
	}
	return *sixth, true
}

func cleanupLiveGatewayRebindStoppedStageContainer(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, binding *gatewayRebindStageContainerBinding,
) bool {
	t.Helper()
	stage, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, binding)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	history, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Error("live stopped-container cleanup cannot reread protected lineage; retaining resources")
		return false
	}
	if len(history.Progress) >= 11 {
		if !liveGatewayRebindFinalConfigCleanupInventory(t, fixture, ctx, intent, history) {
			return false
		}
	} else if len(history.Progress) >= 8 {
		expected, expectedErr := gatewayRebindStageConfigBytes(intent)
		if expectedErr != nil || history.Progress[7].Record.Stage == nil {
			clear(expected)
			t.Error("live stopped-container cleanup cannot regenerate exact protected stage config; retaining resources")
			return false
		}
		defer clear(expected)
		inventoryStage := *history.Progress[7].Record.Stage
		if len(history.Progress) >= 9 {
			if history.Progress[8].Record.Stage == nil {
				t.Error("live stopped-container cleanup lacks its protected start intent; retaining resources")
				return false
			}
			// A proved stop can retain Caddy's exact autosave after a start.
			// The production reader requires the unchanged durable start intent
			// before allowing that snapshot alongside the approved stage file.
			inventoryStage = *history.Progress[8].Record.Stage
		}
		copyDriver := managerGatewayRebindStageStartIntentDriver{manager: fixture.ingress}
		inventory, inventoryErr := copyDriver.configVolumeInventory(ctx, intent, inventoryStage, expected)
		if inventoryErr != nil || inventory != gatewayRebindStageConfigInventoryExact {
			t.Error("live stopped-container cleanup cannot prove the exact authorized stage configuration; retaining resources")
			return false
		}
	}
	driver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, stage, observed, binding) ||
		observed.StageContainer.Running || observed.StageContainer.Restarting || observed.StageRuntime.Paused ||
		observed.StageRuntime.Dead || observed.StageRuntime.RestartCount != 0 ||
		gatewayV2HasEffectivePortBinding(observed.StageRuntime.EffectivePortBindings) {
		t.Error("live stopped-container cleanup cannot prove exact inert bound container; retaining resources")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "container", "rm", binding.ID)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live stopped successor container without force")
		return false
	}
	withoutContainer := stage
	withoutContainer.StageContainer = nil
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, withoutContainer, residue, nil) {
		t.Error("exact live stopped-container cleanup left owned container or endpoint residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStoppedStageDataVolume(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) bool {
	t.Helper()
	stage, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, container)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	withoutContainer := stage
	withoutContainer.StageContainer = nil
	containerObservation, err := containerDriver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, withoutContainer, containerObservation, nil) {
		t.Error("live data-volume cleanup cannot prove stopped container is absent; retaining resources")
		return false
	}
	driver := managerGatewayRebindStageDataVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, observed, networkID, *config, data) {
		t.Error("live data-volume cleanup cannot prove exact protected ownership; retaining resources")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", data.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor data volume after stopped container")
		return false
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageDataVolumeObservation(intent, residue, networkID, *config, nil) {
		t.Error("exact live data-volume cleanup left owned residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStoppedStageConfigVolume(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) bool {
	t.Helper()
	if _, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, container); !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	containerObservation, err := containerDriver.inspect(ctx, intent)
	if err != nil || containerObservation.StageContainerFound || len(containerObservation.OwnedContainers) != 0 ||
		containerObservation.DataVolumeFound {
		t.Error("live config-volume cleanup cannot prove container and data volume are absent; retaining resources")
		return false
	}
	driver := managerGatewayRebindStageConfigVolumeDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, observed, networkID, config) {
		t.Error("live config-volume cleanup cannot prove exact protected ownership; retaining resources")
		return false
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "volume", "rm", config.Name)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor config volume after stopped container")
		return false
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageConfigVolumeObservation(intent, residue, networkID, nil) {
		t.Error("exact live config-volume cleanup left owned residue")
		return false
	}
	return true
}

func cleanupLiveGatewayRebindStoppedStageNetwork(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, networkID string, config *gatewayRebindStageConfigVolumeBinding,
	data *gatewayRebindStageDataVolumeBinding, container *gatewayRebindStageContainerBinding,
) {
	t.Helper()
	if _, ok := liveGatewayRebindStoppedStageCleanupLineage(t, fixture, intent, networkID, config, data, container); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	containerObservation, err := containerDriver.inspect(ctx, intent)
	if err != nil || containerObservation.StageContainerFound || len(containerObservation.OwnedContainers) != 0 ||
		containerObservation.ConfigVolumeFound || containerObservation.DataVolumeFound {
		t.Error("live network cleanup cannot prove successor container and volumes are absent; retaining network")
		return
	}
	driver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	observed, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkObservation(intent, observed, networkID) {
		t.Error("live network cleanup cannot prove exact protected ownership; retaining network")
		return
	}
	result, removeErr := runLiveDocker(ctx, fixture.runner, fixture.docker, fixture.root, fixture.dockerConfig,
		45*time.Second, "network", "rm", networkID)
	clearLiveResult(&result)
	if removeErr != nil {
		t.Error("remove exact live successor network after stopped container")
		return
	}
	residue, err := driver.inspect(ctx, intent)
	if err != nil || !validGatewayRebindStageNetworkAbsentObservation(intent, residue) {
		t.Error("exact live stopped-container network cleanup left owned residue")
	}
}
