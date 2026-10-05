package generatedingress

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindFinalConfigAutosaveRetainsStartedStageProofAcrossProgress(t *testing.T) {
	fixture, stageFake, reads, _ := installGatewayRebindFinalConfigServingProgress(t)
	manager := fixture.predecessor.manager
	ctx := context.Background()
	stageBytes, err := gatewayRebindStageConfigBytes(fixture.intent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(stageBytes, &decoded); err != nil {
		t.Fatal(err)
	}
	autosave, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	check := func(sequence int, withActive bool) {
		t.Helper()
		history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(history.Progress) != sequence {
			t.Fatalf("sequence %d history: %v", sequence, err)
		}
		stage := *history.Progress[sequence-1].Record.Stage
		if !gatewayRebindStageAutosaveHistoryMatches(history, fixture.intent, stage, stageBytes) {
			t.Fatalf("sequence %d lost the exact retained start-intent proof", sequence)
		}
		var active []byte
		files := []string{"caddy/", "stage.json", "caddy/autosave.json"}
		if withActive {
			active, err = gatewayRebindFinalConfigBytes(fixture.intent, stage.FinalConfigIntent.RoutePlan)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, "active.json")
		}
		runner := &finalConfigDriverRunner{result: runtimeprocess.CommandResult{
			Stdout: finalConfigAutosaveArchive(t, stageBytes, active, autosave, files...),
		}}
		original := manager.runner
		manager.runner = runner
		defer func() { manager.runner = original }()
		inventory, err := (managerGatewayRebindStageStartIntentDriver{manager: manager}).configVolumeInventory(ctx, fixture.intent, stage, stageBytes)
		if !withActive && (err != nil || inventory != gatewayRebindStageConfigInventoryExact) {
			t.Fatalf("sequence %d production stage inventory: %v", sequence, err)
		}
		if withActive {
			if err == nil || inventory != 0 {
				t.Fatal("stage-only reader accepted inactive final file")
			}
			runner.result.Stdout = finalConfigAutosaveArchive(t, stageBytes, active, autosave, files...)
			pair, err := (managerGatewayRebindFinalConfigCopyDriver{manager: manager}).finalConfigVolumeInventory(ctx, fixture.intent, stage, stageBytes, active)
			if err != nil || pair != gatewayRebindFinalConfigInventoryExactPair {
				t.Fatalf("sequence twelve exact pair with stage autosave: %v", err)
			}
		}
		confirmed, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || !reflect.DeepEqual(history, confirmed) {
			t.Fatalf("sequence %d inventory changed protected history: %v", sequence, err)
		}
	}
	check(10, false)
	if err := manager.prepareGatewayRebindSuccessorFinalConfigIntentWithDriver(ctx, fixture.predecessor.repository,
		reads, gatewayRebindStageNetworkInspect(t, fixture), stageFake, gatewayRebindProgressTimestamp(11), nil); err != nil {
		t.Fatal(err)
	}
	check(11, false)
	fake := newGatewayRebindFinalConfigCopyFake(t, fixture, stageFake)
	if err := manager.copyGatewayRebindSuccessorFinalConfigWithDriver(ctx, fixture.predecessor.repository,
		reads, gatewayRebindStageNetworkInspect(t, fixture), fake, gatewayRebindProgressTimestamp(12), nil); err != nil {
		t.Fatal(err)
	}
	check(12, true)
}
