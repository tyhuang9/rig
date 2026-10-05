package generatedingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

type liveGatewayRebindStageConfigCopyCountingDriver struct {
	managerGatewayRebindStageConfigCopyDriver
	copyCalls int
	copyErr   error
}

func (d *liveGatewayRebindStageConfigCopyCountingDriver) copyStageConfig(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, contents []byte,
) error {
	d.copyCalls++
	if err := d.managerGatewayRebindStageConfigCopyDriver.copyStageConfig(ctx, intent, stage, contents); err != nil {
		return err
	}
	return d.copyErr
}

// TestLiveGatewayRebindSuccessorStageConfigCopy proves the direct copy path:
// the same guarded call copies, reads back, and records sequence eight.
func TestLiveGatewayRebindSuccessorStageConfigCopy(t *testing.T) {
	liveGatewayRebindSuccessorStageConfigCopy(t, false)
}

// TestLiveGatewayRebindSuccessorStageConfigCopyLostAcknowledgmentAdopts proves
// that a real copy with a lost acknowledgment is adopted only after a fresh
// Manager reads back the exact bound bytes.
func TestLiveGatewayRebindSuccessorStageConfigCopyLostAcknowledgmentAdopts(t *testing.T) {
	liveGatewayRebindSuccessorStageConfigCopy(t, true)
}

func liveGatewayRebindSuccessorStageConfigCopy(t *testing.T, lostAcknowledgment bool) {
	t.Helper()
	spec := liveGatewayV2FixtureSpec{
		appID:            "e1111111-1111-4111-8111-111111111111",
		planID:           "e2222222-2222-4222-8222-222222222222",
		operationID:      "e3333333-3333-4333-8333-333333333333",
		profileRevision:  "e4444444-4444-4444-8444-444444444444",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:rebind-stage-config-copy",
		applicationReply: "gateway-v2-rebind-stage-config-copy",
		countRequests:    true,
	}
	if lostAcknowledgment {
		spec.appID = "f1111111-1111-4111-8111-111111111111"
		spec.planID = "f2222222-2222-4222-8222-222222222222"
		spec.operationID = "f3333333-3333-4333-8333-333333333333"
		spec.profileRevision = "f4444444-4444-4444-8444-444444444444"
		spec.imageTag = "rig-generated-gateway-v2-live:rebind-stage-config-copy-lost-ack"
		spec.applicationReply = "gateway-v2-rebind-stage-config-copy-lost-ack"
	}
	fixture := newLiveGatewayV2Fixture(t, spec)
	db, err := database.Open(fixture.stateRoot)
	if err != nil {
		t.Fatal("open live rebind stage-config-copy SQLite fixture")
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
		t.Fatal("live stage-config-copy successor did not pass zero-claim admission")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !gatewayRebindPredecessorMatches(prepared, predecessorState, predecessorJournal) {
		t.Fatal("live stage-config-copy prepared claim did not bind predecessor")
	}

	reads := liveGatewayRebindStageProductionReads(fixture.ingress)
	publicPreflight, err := fixture.ingress.InspectGatewayRebindSuccessorPreflight(fixture.ctx, repository)
	if err != nil {
		t.Fatal("production stage-config-copy successor preflight rejected present host interface")
	}
	observation, err := readGatewayRebindSuccessorPreflightObservation(fixture.ctx, repository, reads)
	if err != nil || !reflect.DeepEqual(publicPreflight, observation.result) {
		t.Fatal("stage-config-copy protected-intent observation differed from production preflight")
	}
	predecessor := gatewayUpgradeGenerationSelection{
		Store: predecessorStore, Generation: predecessorStore.generation,
		State: predecessorState, Journal: predecessorJournal, Existing: true,
		operationID: predecessorJournal.OperationID,
	}
	selection, err := fixture.ingress.resolveGatewayRebindProtectedIntentLocked(proposal.Spec.OperationID)
	if err != nil {
		t.Fatal("reserve stage-config-copy successor intent generation")
	}
	intent, err := newGatewayRebindProtectedIntent(prepared, predecessor, observation, selection.Generation)
	if err != nil || selection.Store.installExact(intent) != nil {
		t.Fatal("install exact stage-config-copy successor protected intent")
	}

	networkDriver := managerGatewayRebindStageNetworkDriver{manager: fixture.ingress}
	image, found, err := networkDriver.inspectImage(fixture.ctx)
	if err != nil || !validGatewayPinnedImage(image, found) {
		t.Fatal("inspect exact pinned live gateway image")
	}
	baseTime := time.Now().UTC()
	first, err := newGatewayRebindSuccessorIntentProgress(intent, baseTime)
	if err != nil {
		t.Fatal("construct live stage-config-copy successor intent progress")
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, first.Sequence)
	if err != nil || firstStore.installExact(fixture.ctx, first) != nil {
		t.Fatal("install live stage-config-copy successor intent progress")
	}
	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindStageIntentObservation{
		OccurredAt:            baseTime.Add(time.Nanosecond),
		ObservedDockerImageID: normalizeID(image.ID),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	})
	if err != nil {
		t.Fatal("construct live stage-config-copy stage intent progress")
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.stateRoot, intent.Generation, intent.OperationID, second.Sequence)
	if err != nil || secondStore.installExact(fixture.ctx, second) != nil {
		t.Fatal("install live stage-config-copy stage intent progress")
	}

	var networkID string
	var configBinding *gatewayRebindStageConfigVolumeBinding
	var dataBinding *gatewayRebindStageDataVolumeBinding
	var containerBinding *gatewayRebindStageContainerBinding
	// Once sequence seven exists, a missing sequence-eight receipt leaves an
	// ambiguous file in /config. Cleanup preserves that whole chain for CI.
	t.Cleanup(func() {
		cleanupLiveGatewayRebindStoppedStageContainerChain(t, fixture, intent, networkID,
			configBinding, dataBinding, containerBinding)
	})

	beforeRoute, err := fixture.ingress.store.load()
	if err != nil {
		t.Fatal("load predecessor route before stage-config copy")
	}
	beforeDocker, err := fixture.ingress.inspectGatewayRebindDocker(
		fixture.ctx, beforeRoute, predecessorState, predecessorJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(beforeRoute, predecessorState, predecessorJournal, beforeDocker) {
		clearGatewayV2DockerObservation(&beforeDocker)
		t.Fatal("inspect exact predecessor Docker state before stage-config copy")
	}
	beforeDockerDigest, err := gatewayRebindEffectBoundaryDockerDigest(beforeDocker, predecessorState.Identity)
	clearGatewayV2DockerObservation(&beforeDocker)
	if err != nil {
		t.Fatal("digest exact predecessor Docker state before stage-config copy")
	}
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted predecessor requests did not settle before stage-config copy")
	}

	if err := fixture.ingress.stageGatewayRebindSuccessorNetwork(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(2*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor network before stage-config copy", err)
	}
	networkHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(networkHistory.Progress) != 3 || networkHistory.Progress[2].Record.Stage == nil ||
		networkHistory.Progress[2].Record.Stage.Network == nil {
		t.Fatal("live successor network did not bind exact sequence three")
	}
	networkID = networkHistory.Progress[2].Record.Stage.Network.ID
	if err := fixture.ingress.stageGatewayRebindSuccessorConfigVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(3*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor config volume before stage-config copy", err)
	}
	configHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(configHistory.Progress) != 4 || configHistory.Progress[3].Record.Stage == nil ||
		configHistory.Progress[3].Record.Stage.ConfigVolume == nil {
		t.Fatal("live successor config volume did not bind exact sequence four")
	}
	configBinding = configHistory.Progress[3].Record.Stage.ConfigVolume
	if err := fixture.ingress.stageGatewayRebindSuccessorDataVolume(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(4*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind successor data volume before stage-config copy", err)
	}
	dataHistory, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(dataHistory.Progress) != 5 || dataHistory.Progress[4].Record.Stage == nil ||
		dataHistory.Progress[4].Record.Stage.DataVolume == nil {
		t.Fatal("live successor data volume did not bind exact sequence five")
	}
	dataBinding = dataHistory.Progress[4].Record.Stage.DataVolume
	if err := fixture.ingress.stageGatewayRebindSuccessorContainer(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(5*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "stage live rebind stopped successor container before stage-config copy", err)
	}
	beforeIntent, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeIntent.Progress) != 6 || beforeIntent.Progress[5].Record.Stage == nil ||
		beforeIntent.Progress[5].Record.Stage.StageContainer == nil {
		t.Fatal("live successor container did not bind exact sequence six")
	}
	containerBinding = beforeIntent.Progress[5].Record.Stage.StageContainer
	containerDriver := managerGatewayRebindStageContainerDriver{manager: fixture.ingress}
	beforeTopology, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, reads, containerDriver, intent, *beforeIntent.Progress[5].Record.Stage, containerBinding)
	if err != nil {
		t.Fatal("read sequence-six Linux Docker topology before stage-config intent")
	}
	expected, err := gatewayRebindStageConfigBytes(intent)
	if err != nil {
		t.Fatal("build exact stage config bytes")
	}
	defer clear(expected)
	configIntentDriver := managerGatewayRebindStageConfigIntentDriver{manager: fixture.ingress}
	empty, err := configIntentDriver.configVolumeEmpty(fixture.ctx, intent, *beforeIntent.Progress[5].Record.Stage)
	if err != nil || !empty {
		t.Fatal("live sequence-six config mount was not a strictly empty Docker archive")
	}

	if err := fixture.ingress.prepareGatewayRebindSuccessorStageConfigIntent(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, baseTime.Add(6*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "install live rebind stage config intent", err)
	}
	beforeCopy, err := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(beforeCopy.Progress) != 7 {
		t.Fatal("live successor config intent did not install sequence seven")
	}
	for index := range beforeIntent.Progress {
		if !reflect.DeepEqual(beforeIntent.Progress[index], beforeCopy.Progress[index]) {
			t.Fatal("sequence-seven intent changed an earlier protected record")
		}
	}
	beforeCopyBytes := liveGatewayRebindProgressBytes(t, beforeCopy)
	seventh := beforeCopy.Progress[6].Record
	intentBinding, intentErr := gatewayRebindStageConfigIntentBindingFor(intent, beforeIntent.Progress[5].Record)
	digest := sha256.Sum256(expected)
	if intentErr != nil || seventh.PreviousDigest != beforeIntent.Progress[5].Record.Digest || seventh.Stage == nil ||
		seventh.Stage.StageConfigIntent == nil || *seventh.Stage.StageConfigIntent != intentBinding ||
		intentBinding.ContentDigest != hex.EncodeToString(digest[:]) || intentBinding.ContentLength != int64(len(expected)) {
		t.Fatal("live sequence-seven stage config intent did not bind exact bytes and digest")
	}
	withoutIntent := *seventh.Stage
	withoutIntent.StageConfigIntent = nil
	if !reflect.DeepEqual(withoutIntent, *beforeIntent.Progress[5].Record.Stage) {
		t.Fatal("sequence-seven intent changed the bound stopped successor")
	}
	configCopyDriver := managerGatewayRebindStageConfigCopyDriver{manager: fixture.ingress}
	inventory, err := configCopyDriver.configVolumeInventory(fixture.ctx, intent, *seventh.Stage, expected)
	if err != nil || inventory != gatewayRebindStageConfigInventoryEmpty {
		t.Fatal("sequence-seven intent changed the strict empty Docker config inventory")
	}

	copyDriver := &liveGatewayRebindStageConfigCopyCountingDriver{
		managerGatewayRebindStageConfigCopyDriver: managerGatewayRebindStageConfigCopyDriver{manager: fixture.ingress},
	}
	if lostAcknowledgment {
		copyDriver.copyErr = errors.New("controlled lost acknowledgment after the real stage config copy")
	}
	copyResultErr := fixture.ingress.copyGatewayRebindSuccessorStageConfigWithDriver(fixture.ctx, repository, reads,
		fixture.ingress.inspectGatewayRebindDocker, copyDriver, baseTime.Add(7*time.Nanosecond), nil)
	if !lostAcknowledgment && copyResultErr != nil {
		failLiveIngress(t, "copy exact live rebind stage config", copyResultErr)
	}
	if lostAcknowledgment && copyResultErr == nil {
		t.Fatal("controlled lost stage-config-copy acknowledgment was reported as success")
	}
	if copyDriver.copyCalls != 1 {
		t.Fatalf("real guarded stage config copy calls=%d want=1", copyDriver.copyCalls)
	}
	var boundHistory gatewayRebindProtectedIntentHistory
	if lostAcknowledgment {
		lostHistory, scanErr := fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if scanErr != nil || !reflect.DeepEqual(beforeCopy, lostHistory) ||
			!reflect.DeepEqual(beforeCopyBytes, liveGatewayRebindProgressBytes(t, lostHistory)) {
			t.Fatal("lost stage-config-copy acknowledgment changed the sequence-seven protected history")
		}
		inventory, err = configCopyDriver.configVolumeInventory(fixture.ctx, intent, *seventh.Stage, expected)
		if err != nil || inventory != gatewayRebindStageConfigInventoryExact {
			t.Fatal("lost stage-config-copy acknowledgment did not leave the exact physical stage file for guarded adoption")
		}

		adopting, adoptErr := New(fixture.runner, fixture.ingress.options)
		if adoptErr != nil {
			t.Fatal("restart generated ingress manager to adopt exact copied stage config")
		}
		adoptionReads := liveGatewayRebindStageProductionReads(adopting)
		adoptionDriver := &liveGatewayRebindStageConfigCopyCountingDriver{
			managerGatewayRebindStageConfigCopyDriver: managerGatewayRebindStageConfigCopyDriver{manager: adopting},
		}
		if adoptErr := adopting.copyGatewayRebindSuccessorStageConfigWithDriver(fixture.ctx, repository, adoptionReads,
			adopting.inspectGatewayRebindDocker, adoptionDriver, baseTime.Add(8*time.Nanosecond), nil); adoptErr != nil {
			failLiveIngress(t, "adopt exact live stage config after lost acknowledgment", adoptErr)
		}
		if adoptionDriver.copyCalls != 0 {
			t.Fatalf("fresh Manager adopted an exact copied config by copying again: %d", adoptionDriver.copyCalls)
		}
		boundHistory, err = adopting.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	} else {
		boundHistory, err = fixture.ingress.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	}
	if err != nil || len(boundHistory.Progress) != 8 {
		t.Fatal("live successor config copy did not install sequence eight")
	}
	for index := range beforeCopy.Progress {
		if !reflect.DeepEqual(beforeCopy.Progress[index], boundHistory.Progress[index]) {
			t.Fatal("sequence-eight receipt changed an earlier protected record")
		}
	}
	boundBytes := liveGatewayRebindProgressBytes(t, boundHistory)
	for index := range beforeCopyBytes {
		if !bytes.Equal(beforeCopyBytes[index], boundBytes[index]) {
			t.Fatalf("sequence-eight receipt changed protected progress bytes at sequence %d", index+1)
		}
	}
	copyBinding, copyErr := gatewayRebindStageConfigCopyBindingFor(intent, seventh)
	eighth := boundHistory.Progress[7].Record
	if copyErr != nil || eighth.PreviousDigest != seventh.Digest || eighth.Stage == nil ||
		eighth.Stage.StageConfigCopy == nil || *eighth.Stage.StageConfigCopy != copyBinding ||
		copyBinding.StageConfigIntent != intentBinding || copyBinding.PriorProgressDigest != seventh.Digest {
		t.Fatal("live sequence-eight stage config copy receipt did not bind sequence-seven bytes")
	}
	withoutCopy := *eighth.Stage
	withoutCopy.StageConfigCopy = nil
	if !reflect.DeepEqual(withoutCopy, *seventh.Stage) {
		t.Fatal("sequence-eight receipt changed the stopped successor or config intent")
	}
	inventory, err = configCopyDriver.configVolumeInventory(fixture.ctx, intent, *eighth.Stage, expected)
	if err != nil || inventory != gatewayRebindStageConfigInventoryExact {
		t.Fatal("live sequence-eight config inventory did not contain the exact stage file")
	}
	physical, err := containerDriver.inspect(fixture.ctx, intent)
	if err != nil || !validGatewayRebindStageContainerObservation(intent, *eighth.Stage, physical, containerBinding) ||
		physical.StageContainer.Running || physical.StageContainer.Restarting || physical.StageRuntime.Paused ||
		physical.StageRuntime.Dead || physical.StageRuntime.RestartCount != 0 ||
		gatewayV2HasEffectivePortBinding(physical.StageRuntime.EffectivePortBindings) {
		t.Fatal("stage-config copy started the bound successor or published a listener")
	}
	afterTopology, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, reads, containerDriver, intent, *eighth.Stage, containerBinding)
	if err != nil || !reflect.DeepEqual(beforeTopology, afterTopology) {
		t.Fatal("stage-config copy changed the exact Linux Docker or host topology")
	}

	restarted, err := New(fixture.runner, fixture.ingress.options)
	if err != nil {
		t.Fatal("restart generated ingress manager before stage-config-copy replay")
	}
	restartReads := liveGatewayRebindStageProductionReads(restarted)
	replayDriver := &liveGatewayRebindStageConfigCopyCountingDriver{
		managerGatewayRebindStageConfigCopyDriver: managerGatewayRebindStageConfigCopyDriver{manager: restarted},
	}
	if err := restarted.copyGatewayRebindSuccessorStageConfigWithDriver(fixture.ctx, repository, restartReads,
		restarted.inspectGatewayRebindDocker, replayDriver, baseTime.Add(9*time.Nanosecond), nil); err != nil {
		failLiveIngress(t, "fresh-manager replay live stage config copy", err)
	}
	if replayDriver.copyCalls != 0 {
		t.Fatalf("fresh-manager exact replay copied stage config again: %d", replayDriver.copyCalls)
	}
	replayedHistory, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || !reflect.DeepEqual(boundHistory, replayedHistory) ||
		!reflect.DeepEqual(boundBytes, liveGatewayRebindProgressBytes(t, replayedHistory)) {
		t.Fatal("fresh-manager replay changed protected sequence-eight records or bytes")
	}
	replayInventory, err := replayDriver.configVolumeInventory(fixture.ctx, intent, *eighth.Stage, expected)
	if err != nil || replayInventory != gatewayRebindStageConfigInventoryExact {
		t.Fatal("fresh-manager replay did not read back the exact stage config bytes")
	}
	replayedTopology, err := readGatewayRebindStageContainerPhysicalObservation(
		fixture.ctx, restartReads, replayDriver, intent, *eighth.Stage, containerBinding)
	if err != nil || !reflect.DeepEqual(afterTopology, replayedTopology) {
		t.Fatal("fresh-manager replay changed the successor host topology")
	}

	afterPrepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || !reflect.DeepEqual(prepared, afterPrepared) {
		t.Fatal("stage-config copy changed prepared SQLite claim")
	}
	afterState, afterJournal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if !reflect.DeepEqual(predecessorState, afterState) || !reflect.DeepEqual(predecessorJournal, afterJournal) {
		t.Fatal("stage-config copy changed protected predecessor")
	}
	afterRoute, err := restarted.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatal("stage-config copy changed active application route")
	}
	afterDocker, err := restarted.inspectGatewayRebindDocker(fixture.ctx, afterRoute, afterState, afterJournal)
	if err != nil || !validGatewayRebindPredecessorDocker(afterRoute, afterState, afterJournal, afterDocker) {
		clearGatewayV2DockerObservation(&afterDocker)
		t.Fatal("stage-config copy changed predecessor Docker resources")
	}
	afterDockerDigest, err := gatewayRebindEffectBoundaryDockerDigest(afterDocker, afterState.Identity)
	clearGatewayV2DockerObservation(&afterDocker)
	if err != nil || afterDockerDigest != beforeDockerDigest {
		t.Fatal("stage-config copy changed predecessor Docker resources")
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("stage-config copy or replay forwarded an application request")
	}
}

var _ gatewayRebindStageConfigCopyDriver = (*liveGatewayRebindStageConfigCopyCountingDriver)(nil)
