package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

type gatewayRebindFinalConfigCopyFake struct {
	*gatewayRebindStageStartFake
	expectedStage   []byte
	expectedActive  []byte
	inventory       gatewayRebindFinalConfigInventory
	inventoryErr    error
	inventoryErrAt  int
	inventoryCalls  int
	copyErr         error
	copyTakesEffect bool
	copyCalls       int
	onCopy          func()
}

func (f *gatewayRebindFinalConfigCopyFake) configVolumeInventory(ctx context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expected []byte,
) (gatewayRebindStageConfigInventory, error) {
	if f.inventory == gatewayRebindFinalConfigInventoryExactPair {
		return 0, errors.New("legacy stage-only inventory rejects active config")
	}
	return f.gatewayRebindStageStartFake.configVolumeInventory(ctx, intent, stage, expected)
}

func (f *gatewayRebindFinalConfigCopyFake) finalConfigVolumeInventory(_ context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, expectedStage, expectedActive []byte,
) (gatewayRebindFinalConfigInventory, error) {
	f.inventoryCalls++
	if !reflect.DeepEqual(intent, f.intent) || stage.StageContainer == nil ||
		stage.StageContainer.ID != f.containerID || stage.StageServing == nil ||
		stage.FinalConfigIntent == nil || !bytes.Equal(expectedStage, f.expectedStage) ||
		!bytes.Equal(expectedActive, f.expectedActive) {
		return 0, errors.New("unexpected final config inventory input")
	}
	if f.inventoryErr != nil && (f.inventoryErrAt == 0 || f.inventoryCalls == f.inventoryErrAt) {
		return 0, f.inventoryErr
	}
	return f.inventory, nil
}

func (f *gatewayRebindFinalConfigCopyFake) copyFinalConfig(_ context.Context,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent, contents []byte,
) error {
	f.copyCalls++
	if !reflect.DeepEqual(intent, f.intent) || stage.StageContainer == nil ||
		stage.StageContainer.ID != f.containerID || stage.StageServing == nil ||
		stage.FinalConfigIntent == nil || stage.FinalConfigCopy != nil ||
		!bytes.Equal(contents, f.expectedActive) {
		return errors.New("unexpected final config copy input")
	}
	if f.copyErr == nil || f.copyTakesEffect {
		f.inventory = gatewayRebindFinalConfigInventoryExactPair
	}
	if f.onCopy != nil {
		f.onCopy()
	}
	return f.copyErr
}

func TestGatewayRebindFinalConfigCopyAppendsCompactReceiptAndReplaysReadOnly(t *testing.T) {
	fixture, fake, reads, before := installGatewayRebindFinalConfigCopyIntent(t)
	beforeDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeRoute, err := fixture.predecessor.manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	checkpointCalls := 0
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), func() { checkpointCalls++ }); err != nil {
		t.Fatalf("copy exact final config: %v", err)
	}
	if checkpointCalls != 1 || fake.copyCalls != 1 || fake.inventory != gatewayRebindFinalConfigInventoryExactPair ||
		fake.startCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("checkpoint=%d copies=%d inventory=%d starts=%d stops=%d",
			checkpointCalls, fake.copyCalls, fake.inventory, fake.startCalls, fake.stopCalls)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 {
		t.Fatalf("sequence-twelve history=%d error=%v", len(history.Progress), err)
	}
	for index := range before {
		body, readErr := os.ReadFile(history.Progress[index].Store.path)
		if readErr != nil || !bytes.Equal(body, before[index]) {
			t.Fatalf("sequence %d changed: %v", index+1, readErr)
		}
	}
	record := history.Progress[11].Record
	if record.Sequence != 12 || record.Phase != gatewayRebindProgressFinalConfigCopied || record.Stage == nil ||
		record.Stage.FinalConfigIntent == nil || record.Stage.FinalConfigCopy == nil {
		t.Fatalf("invalid final config receipt record: %#v", record)
	}
	wantReceipt, err := gatewayRebindFinalConfigCopyBindingFor(fixture.intent, history.Progress[10].Record)
	if err != nil || *record.Stage.FinalConfigCopy != wantReceipt {
		t.Fatalf("receipt=%#v expected=%#v error=%v", record.Stage.FinalConfigCopy, wantReceipt, err)
	}
	if !bytes.Equal(fake.expected, fake.expectedStage) || bytes.Equal(fake.expectedStage, fake.expectedActive) {
		t.Fatal("live stage configuration was replaced by the inactive final configuration")
	}
	afterDatabase, err := fixture.predecessor.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(beforeDatabase, afterDatabase) {
		t.Fatalf("copy changed SQLite: equal=%t error=%v", reflect.DeepEqual(beforeDatabase, afterDatabase), err)
	}
	afterRoute, err := fixture.predecessor.manager.store.load()
	if err != nil || !reflect.DeepEqual(beforeRoute, afterRoute) {
		t.Fatalf("copy changed route state: equal=%t error=%v", reflect.DeepEqual(beforeRoute, afterRoute), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(13), nil); err != nil {
		t.Fatalf("fresh Manager receipt replay: %v", err)
	}
	if fake.copyCalls != 1 {
		t.Fatalf("receipt replay recopied final config: calls=%d", fake.copyCalls)
	}
}

func TestGatewayRebindFinalConfigCopyAdoptsOnlyExactPairAfterFreshProof(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	fake.inventory = gatewayRebindFinalConfigInventoryExactPair
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatalf("adopt exact final config pair: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 || fake.copyCalls != 0 {
		t.Fatalf("adoption history=%d copies=%d error=%v", len(history.Progress), fake.copyCalls, err)
	}
}

func TestGatewayRebindFinalConfigCopyReceiptReplayRejectsPhysicalDriftWithoutRepair(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindFinalConfigCopyFake)
		reset  func(*gatewayRebindFinalConfigCopyFake)
	}{
		{name: "missing active config", mutate: func(fake *gatewayRebindFinalConfigCopyFake) {
			fake.inventory = gatewayRebindFinalConfigInventoryStageOnly
		}},
		{name: "live stage config drift", mutate: func(fake *gatewayRebindFinalConfigCopyFake) {
			fake.liveConfigDrift = true
		}, reset: func(fake *gatewayRebindFinalConfigCopyFake) { fake.liveConfigDrift = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
			if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(12), nil); err != nil {
				t.Fatal(err)
			}
			test.mutate(fake)
			if test.reset != nil {
				defer test.reset(fake)
			}
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			err := restarted.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(13), nil)
			if err == nil || fake.copyCalls != 1 {
				t.Fatalf("drift replay error=%v copies=%d", err, fake.copyCalls)
			}
			history, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 12 {
				t.Fatalf("drift replay changed history=%d scan=%v", len(history.Progress), scanErr)
			}
		})
	}
}

func TestGatewayRebindFinalConfigCopyFailureAndUncertaintyRequireFreshRetry(t *testing.T) {
	for _, test := range []struct {
		name            string
		copyErr         error
		copyTakesEffect bool
		cancel          bool
		readbackErrAt   int
		wantRetryCopies int
	}{
		{name: "copy failure", copyErr: errors.New("copy failed"), wantRetryCopies: 2},
		{name: "copy acknowledgement lost", copyErr: errors.New("copy uncertain"), copyTakesEffect: true, wantRetryCopies: 1},
		{name: "cancellation after copy", copyTakesEffect: true, cancel: true, wantRetryCopies: 1},
		{name: "post-copy inventory uncertainty", readbackErrAt: 5, wantRetryCopies: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
			fake.copyErr = test.copyErr
			fake.copyTakesEffect = test.copyTakesEffect || test.readbackErrAt != 0
			fake.inventoryErrAt = test.readbackErrAt
			if test.readbackErrAt != 0 {
				fake.inventoryErr = errors.New("inventory uncertain")
			}
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				fake.onCopy = cancel
			}
			err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(ctx,
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(12), nil)
			if err == nil {
				t.Fatal("uncertain copy reported durable success")
			}
			history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if scanErr != nil || len(history.Progress) != 11 {
				t.Fatalf("failure installed receipt: history=%d scan=%v", len(history.Progress), scanErr)
			}
			fake.copyErr = nil
			fake.inventoryErr = nil
			fake.onCopy = nil
			restarted := newGatewayRebindStageContainerManager(t, fixture)
			if err := restarted.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
				fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
				gatewayRebindProgressTimestamp(13), nil); err != nil {
				t.Fatalf("fresh retry: %v", err)
			}
			if fake.copyCalls != test.wantRetryCopies {
				t.Fatalf("copies=%d want=%d", fake.copyCalls, test.wantRetryCopies)
			}
		})
	}
}

func TestGatewayRebindFinalConfigCopyAmbiguousReceiptWriteReplaysWithoutRecopy(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	originalWrite := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if err := originalWrite(path, purpose, body); err != nil {
			return err
		}
		return errors.New("injected sequence-twelve write uncertainty")
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = originalWrite })
	err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil)
	if err == nil {
		t.Fatal("ambiguous receipt write reported success")
	}
	upgradeProtectedWriteNew = originalWrite
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(13), nil); err != nil {
		t.Fatalf("fresh replay after ambiguous receipt: %v", err)
	}
	history, scanErr := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 12 || fake.copyCalls != 1 {
		t.Fatalf("ambiguous replay history=%d copies=%d scan=%v", len(history.Progress), fake.copyCalls, scanErr)
	}
}

func TestGatewayRebindFinalConfigCopyRejectsCheckpointRuntimeHeadDrift(t *testing.T) {
	fixture, stageFake, reads, loopbackID := installGatewayRebindFinalConfigLoopbackServingProgress(t)
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		stageFake, gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatal(err)
	}
	fake := newGatewayRebindFinalConfigCopyFake(t, fixture, stageFake)
	for _, trigger := range []string{
		"lan_gateway_rebind_fence_runtime_head_update", "generated_runtime_active_head_valid_update",
	} {
		if _, err := fixture.predecessor.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatal(err)
		}
	}
	checkpointCalls := 0
	err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), func() {
			checkpointCalls++
			if _, updateErr := fixture.predecessor.db.Exec(`UPDATE generated_runtime_active_heads
				SET generation=generation+1,updated_at=? WHERE app_id=?`,
				gatewayRebindProgressTimestamp(14).Format(time.RFC3339Nano), loopbackID); updateErr != nil {
				t.Fatal(updateErr)
			}
		})
	if err == nil || checkpointCalls != 1 || fake.copyCalls != 0 {
		t.Fatalf("runtime-head checkpoint drift accepted: calls=%d copies=%d error=%v",
			checkpointCalls, fake.copyCalls, err)
	}
	history, scanErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if scanErr != nil || len(history.Progress) != 11 {
		t.Fatalf("drift installed receipt: history=%d scan=%v", len(history.Progress), scanErr)
	}
}

func TestGatewayRebindFinalConfigCopyRejectsCheckpointPredecessorDriftBeforeCopy(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	checkpointCalls := 0
	err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), func() {
			checkpointCalls++
			drifted := cloneGatewayV2RouteState(fixture.predecessor.state)
			for appID, app := range drifted.Apps {
				app.Route.Endpoints[0].InternalPort++
				drifted.Apps[appID] = app
				break
			}
			if writeErr := fixture.predecessor.store.writeExact(fixture.predecessor.store.v2Path,
				fixture.predecessor.store.v2Purpose, drifted, false, maxV2RouteStateBytes); writeErr != nil {
				t.Fatal(writeErr)
			}
		})
	if err == nil || checkpointCalls != 1 || fake.copyCalls != 0 {
		t.Fatalf("predecessor checkpoint drift accepted: calls=%d copies=%d error=%v",
			checkpointCalls, fake.copyCalls, err)
	}
	receiptStore, storeErr := newGatewayRebindProgressStore(fixture.predecessor.manager.options.DataRoot,
		fixture.intent.Generation, fixture.intent.OperationID, 12)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if _, statErr := os.Stat(receiptStore.path); !os.IsNotExist(statErr) {
		t.Fatalf("predecessor drift created sequence twelve: %v", statErr)
	}
}

func TestGatewayRebindFinalConfigCopyReceiptTamperAndOldPhaseReplayFailClosed(t *testing.T) {
	fixture, fake, reads, _ := installGatewayRebindFinalConfigCopyIntent(t)
	if err := fixture.predecessor.manager.copyGatewayRebindSuccessorFinalConfigWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 12 {
		t.Fatalf("history=%d error=%v", len(history.Progress), err)
	}
	restarted := newGatewayRebindStageContainerManager(t, fixture)
	if err := restarted.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(13), nil); err == nil {
		t.Fatal("sequence-eleven coordinator accepted sequence-twelve history")
	}
	if err := restarted.startGatewayRebindSuccessorStageWithDriver(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		gatewayRebindProgressTimestamp(13), nil); err == nil {
		t.Fatal("sequence-ten coordinator accepted sequence-twelve history")
	}
	// Even if the active file disappears, the old stage-only proof must not
	// accept a history that has already advanced to the final-copy phase.
	fake.inventory = gatewayRebindFinalConfigInventoryStageOnly
	latestStage := *history.Progress[11].Record.Stage
	if _, err := restarted.readGatewayRebindStageServingAttestation(context.Background(),
		fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture), fake,
		fixture.intent, latestStage, latestStage.StageServing, 12); err == nil {
		t.Fatal("legacy stage-serving proof accepted sequence-twelve history")
	}
	fake.inventory = gatewayRebindFinalConfigInventoryExactPair
	forged := history.Progress[11].Record
	stage := *forged.Stage
	receipt := *stage.FinalConfigCopy
	receipt.ProtectedPredecessorDigest = receipt.ProtectedIntentDigest
	receipt.CopyEffectDigest, err = gatewayRebindFinalConfigCopyEffectDigest(receipt)
	if err != nil || !validGatewayRebindFinalConfigCopyBindingValue(receipt) {
		t.Fatalf("forged receipt is not structurally self-consistent: %v", err)
	}
	stage.FinalConfigCopy = &receipt
	forged.Stage = &stage
	forged.Digest, err = gatewayRebindProgressDigest(forged)
	if err != nil || !validGatewayRebindProgressRecord(forged) {
		t.Fatalf("forged record is not structurally self-consistent: %v", err)
	}
	state := gatewayUpgradeStateStore{directory: history.Progress[11].Store.directory}
	if err := state.writeExact(history.Progress[11].Store.path, history.Progress[11].Store.purpose,
		forged, false, gatewayRebindProgressMaxBytes(12)); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
		t.Fatal("protected-history scanner accepted contextually forged sequence-twelve receipt")
	}
}

func installGatewayRebindFinalConfigCopyIntent(t *testing.T) (gatewayRebindEffectBoundaryFixture,
	*gatewayRebindFinalConfigCopyFake, gatewayRebindSuccessorPreflightReads, [][]byte,
) {
	t.Helper()
	fixture, stageFake, reads, _ := installGatewayRebindFinalConfigServingProgress(t)
	if err := fixture.predecessor.manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(
		context.Background(), fixture.predecessor.repository, reads, gatewayRebindStageNetworkInspect(t, fixture),
		stageFake, gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatalf("install sequence-eleven final config intent: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 11 {
		t.Fatalf("sequence-eleven history=%d error=%v", len(history.Progress), err)
	}
	stageFake.stage = *history.Progress[10].Record.Stage
	fake := newGatewayRebindFinalConfigCopyFake(t, fixture, stageFake)
	before := make([][]byte, len(history.Progress))
	for index := range history.Progress {
		before[index], err = os.ReadFile(history.Progress[index].Store.path)
		if err != nil {
			t.Fatal(err)
		}
	}
	return fixture, fake, reads, before
}

func newGatewayRebindFinalConfigCopyFake(t *testing.T, fixture gatewayRebindEffectBoundaryFixture,
	stageFake *gatewayRebindStageStartFake,
) *gatewayRebindFinalConfigCopyFake {
	t.Helper()
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 11 || history.Progress[10].Record.Stage == nil ||
		history.Progress[10].Record.Stage.FinalConfigIntent == nil {
		t.Fatalf("sequence-eleven history=%d error=%v", len(history.Progress), err)
	}
	stageFake.stage = *history.Progress[10].Record.Stage
	stage, err := gatewayRebindStageConfigBytes(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	active, err := gatewayRebindFinalConfigBytes(fixture.intent,
		history.Progress[10].Record.Stage.FinalConfigIntent.RoutePlan)
	if err != nil {
		clear(stage)
		t.Fatal(err)
	}
	fake := &gatewayRebindFinalConfigCopyFake{
		gatewayRebindStageStartFake: stageFake,
		expectedStage:               stage, expectedActive: active,
		inventory: gatewayRebindFinalConfigInventoryStageOnly,
	}
	t.Cleanup(func() {
		clear(fake.expectedStage)
		clear(fake.expectedActive)
	})
	return fake
}
