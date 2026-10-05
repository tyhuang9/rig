package generatedingress

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGatewayRebindFinalHandoverTerminalReceiptInstallsExactlyAndPreservesHistory(t *testing.T) {
	value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
	manager, dataRoot := handoverTestManager(t, value)
	before, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(before.Progress) != 12 || len(before.Terminals) != 0 {
		t.Fatalf("initial history: progress=%d terminals=%d error=%v", len(before.Progress), len(before.Terminals), err)
	}
	records := handoverTestSuccessRecords(t, value, initial)
	for _, record := range records {
		store, err := newGatewayRebindProgressStore(dataRoot, record.Generation, record.OperationID, record.Sequence)
		if err != nil {
			t.Fatalf("sequence %d store: %v", record.Sequence, err)
		}
		if err := store.installExact(context.Background(), record); err != nil {
			t.Fatalf("install sequence %d: %v", record.Sequence, err)
		}
	}
	pending, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(pending.Progress) != 17 || len(pending.Terminals) != 0 {
		t.Fatalf("terminal phase history: progress=%d terminals=%d error=%v", len(pending.Progress), len(pending.Terminals), err)
	}
	receipt, err := gatewayRebindFinalHandoverTerminalReceiptFor(pending, gatewayRebindProgressTimestamp(18))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindFinalHandoverTerminalStore(dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.installExact(context.Background(), receipt); err != nil {
		t.Fatalf("install terminal receipt: %v", err)
	}
	after, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(after.Terminals) != 1 || !reflect.DeepEqual(after.Terminals[0].Receipt, receipt) {
		t.Fatalf("installed terminal receipt was not exact: %v", err)
	}
	if !gatewayRebindGenerationSelectionEqual(before.Predecessor, after.Predecessor) ||
		!reflect.DeepEqual(before.Source, after.Source) || !reflect.DeepEqual(before.Intents, after.Intents) {
		t.Fatal("terminal receipt changed predecessor, source, or protected intent")
	}
	for index := range before.Progress {
		if !reflect.DeepEqual(before.Progress[index].Record, after.Progress[index].Record) {
			t.Fatalf("terminal append changed immutable sequence %d", index+1)
		}
	}
	if err := store.installExact(context.Background(), receipt); err != nil {
		t.Fatalf("exact receipt replay failed: %v", err)
	}
	forged := receipt
	forged.FinalConfigDigest = strings.Repeat("f", 64)
	forged.Digest, _ = gatewayRebindFinalHandoverTerminalDigest(forged)
	if err := store.installExact(context.Background(), forged); err == nil {
		t.Fatal("conflicting self-consistent terminal receipt replaced immutable evidence")
	}
}

func TestGatewayRebindFinalHandoverTerminalReceiptRejectsEarlyAndForgedHistory(t *testing.T) {
	value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
	manager, _ := handoverTestManager(t, value)
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayRebindFinalHandoverTerminalReceiptFor(history, gatewayRebindProgressTimestamp(13)); err == nil {
		t.Fatal("terminal receipt was created before a terminal progress phase")
	}
	records := handoverTestSuccessRecords(t, value, initial)
	forged := records[len(records)-1]
	forged.Handover = cloneGatewayRebindFinalHandoverProgress(forged.Handover)
	forged.Handover.Outcome = nil
	forged.Digest, _ = gatewayRebindProgressDigest(forged)
	selections := handoverTestProgressSelections(t, value, records)
	selections[len(selections)-1].Record = forged
	history.Progress = append(history.Progress, selections...)
	if _, err := gatewayRebindFinalHandoverTerminalReceiptFor(history, gatewayRebindProgressTimestamp(18)); err == nil {
		t.Fatal("receipt builder accepted a forged terminal history")
	}
}

func handoverTestManager(t *testing.T, value gatewayRebindFinalHandoverContext) (*Manager, string) {
	t.Helper()
	if value.Predecessor.Store == nil || value.Predecessor.Store.directory == nil {
		t.Fatal("missing predecessor store")
	}
	root := value.Predecessor.Store.directory.root
	dataRoot := filepath.Dir(filepath.Dir(root))
	return &Manager{store: value.Predecessor.Store.directory, options: Options{DataRoot: dataRoot}}, dataRoot
}

func handoverTestSuccessRecords(t *testing.T, value gatewayRebindFinalHandoverContext,
	initial gatewayRebindFinalHandoverObservation,
) []gatewayRebindProgressRecord {
	t.Helper()
	plan, err := gatewayRebindFinalHandoverPlanFor(value, initial)
	if err != nil {
		t.Fatal(err)
	}
	sequenceThirteen, err := newGatewayRebindFinalHandoverIntentProgress(value.Intent, value.SequenceTwelve,
		plan, gatewayRebindProgressTimestamp(13))
	if err != nil {
		t.Fatal(err)
	}
	value.Plan = &plan
	final, err := gatewayRebindFinalContainerBindingFor(value, strings.Repeat("9", 64))
	if err != nil {
		t.Fatal(err)
	}
	sequenceFourteen, err := newGatewayRebindFinalContainerBoundProgress(value.Intent, sequenceThirteen,
		final, gatewayRebindProgressTimestamp(14))
	if err != nil {
		t.Fatal(err)
	}
	prepared := handoverTestObservation(t, initial, func(observation *gatewayRebindFinalHandoverObservation) {
		observation.Stage = gatewayRebindHandoverContainerAbsent
		observation.Final = gatewayRebindHandoverContainerStopped
		observation.FinalID = final.ID
	})
	sequenceFifteen, err := newGatewayRebindFinalHandoverCutoverProgress(value.Intent, sequenceFourteen,
		prepared, gatewayRebindProgressTimestamp(15))
	if err != nil {
		t.Fatal(err)
	}
	serving := handoverTestObservation(t, prepared, func(observation *gatewayRebindFinalHandoverObservation) {
		observation.Final = gatewayRebindHandoverContainerRunning
		observation.PredecessorRunning = false
		observation.PredecessorAddress = gatewayRebindPredecessorAddressPresent
		observation.PredecessorObservationDigest = strings.Repeat("d", 64)
		observation.PredecessorStopDigest = observation.PredecessorObservationDigest
		observation.RoutesDigest = plan.RoutePlanDigest
	})
	sequenceSixteen, err := newGatewayRebindFinalHandoverServingProgress(value.Intent, sequenceFifteen,
		serving, gatewayRebindProgressTimestamp(16))
	if err != nil {
		t.Fatal(err)
	}
	sequenceSeventeen, err := newGatewayRebindFinalHandoverTerminalProgress(value.Intent, sequenceSixteen,
		gatewayRebindFinalHandoverOutcomeCommit, serving, "", gatewayRebindProgressTimestamp(17))
	if err != nil {
		t.Fatal(err)
	}
	return []gatewayRebindProgressRecord{sequenceThirteen, sequenceFourteen, sequenceFifteen, sequenceSixteen, sequenceSeventeen}
}

func handoverTestProgressSelections(t *testing.T, value gatewayRebindFinalHandoverContext,
	records []gatewayRebindProgressRecord,
) []gatewayRebindProgressSelection {
	t.Helper()
	_, dataRoot := handoverTestManager(t, value)
	result := make([]gatewayRebindProgressSelection, 0, len(records))
	for _, record := range records {
		store, err := newGatewayRebindProgressStore(dataRoot, record.Generation, record.OperationID, record.Sequence)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, gatewayRebindProgressSelection{
			Store: store, Generation: record.Generation, Sequence: record.Sequence, Record: record, Existing: true,
		})
	}
	return result
}
