package generatedingress

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestGatewayRebindFinalHandoverProgressBuildsStrictSuccessAndRollbackGraphs(t *testing.T) {
	value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
	plan, err := gatewayRebindFinalHandoverPlanFor(value, initial)
	if err != nil {
		t.Fatal(err)
	}
	intent := value.Intent
	sequenceTwelve := value.SequenceTwelve
	sequenceThirteen, err := newGatewayRebindFinalHandoverIntentProgress(intent, sequenceTwelve, plan,
		gatewayRebindProgressTimestamp(13))
	if err != nil {
		t.Fatal(err)
	}
	value.Plan = &plan
	final, err := gatewayRebindFinalContainerBindingFor(value, strings.Repeat("9", 64))
	if err != nil {
		t.Fatal(err)
	}
	sequenceFourteen, err := newGatewayRebindFinalContainerBoundProgress(intent, sequenceThirteen, final,
		gatewayRebindProgressTimestamp(14))
	if err != nil {
		t.Fatal(err)
	}
	prepared := handoverTestObservation(t, initial, func(observation *gatewayRebindFinalHandoverObservation) {
		observation.Stage = gatewayRebindHandoverContainerAbsent
		observation.Final = gatewayRebindHandoverContainerStopped
		observation.FinalID = final.ID
	})
	sequenceFifteen, err := newGatewayRebindFinalHandoverCutoverProgress(intent, sequenceFourteen, prepared,
		gatewayRebindProgressTimestamp(15))
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
	sequenceSixteen, err := newGatewayRebindFinalHandoverServingProgress(intent, sequenceFifteen, serving,
		gatewayRebindProgressTimestamp(16))
	if err != nil {
		t.Fatal(err)
	}
	sequenceSeventeen, err := newGatewayRebindFinalHandoverTerminalProgress(intent, sequenceSixteen,
		gatewayRebindFinalHandoverOutcomeCommit, serving, "", gatewayRebindProgressTimestamp(17))
	if err != nil || sequenceSeventeen.Phase != gatewayRebindProgressHandoverCommitted {
		t.Fatalf("commit progress: %v", err)
	}

	for index, record := range []gatewayRebindProgressRecord{
		sequenceThirteen, sequenceFourteen, sequenceFifteen, sequenceSixteen, sequenceSeventeen,
	} {
		contextValue := value
		contextValue.Phase = record.Phase
		contextValue.Plan = record.Handover.Plan
		contextValue.Final = record.Handover.Final
		if !validGatewayRebindFinalHandoverProgressContext(contextValue, record) {
			t.Fatalf("success record %d failed contextual validation", index+13)
		}
	}

	predecessorRoutesDigest, err := gatewayRebindFinalHandoverPredecessorRoutesDigest(value.Predecessor)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []gatewayRebindProgressRecord{sequenceThirteen, sequenceFourteen, sequenceFifteen, sequenceSixteen} {
		rollback, err := newGatewayRebindFinalHandoverRollbackProgress(intent, source,
			gatewayRebindProgressTimestamp(source.Sequence+1))
		if err != nil {
			t.Fatalf("rollback from %s: %v", source.Phase, err)
		}
		observation := handoverTestObservation(t, initial, func(observation *gatewayRebindFinalHandoverObservation) {
			observation.Stage = gatewayRebindHandoverContainerAbsent
			observation.Final = gatewayRebindHandoverContainerAbsent
			observation.FinalID = ""
			observation.ConfigVolumePresent = false
			observation.DataVolumePresent = false
			observation.IngressNetworkPresent = false
			observation.ConfigDigest = ""
		})
		if rollback.Handover.Rollback.CutoverBegun {
			observation = handoverTestObservation(t, observation, func(observation *gatewayRebindFinalHandoverObservation) {
				observation.PredecessorRunning = true
				observation.PredecessorAddress = gatewayRebindPredecessorAddressPresent
				observation.PredecessorObservationDigest = strings.Repeat("f", 64)
				observation.PredecessorStopDigest = ""
				observation.PredecessorRoutesDigest = predecessorRoutesDigest
			})
		}
		terminal, err := newGatewayRebindFinalHandoverTerminalProgress(intent, rollback,
			gatewayRebindFinalHandoverOutcomeAbort, observation, predecessorRoutesDigest,
			gatewayRebindProgressTimestamp(rollback.Sequence+1))
		if err != nil || terminal.Phase != gatewayRebindProgressHandoverRolledBack {
			t.Fatalf("abort from %s: %v", source.Phase, err)
		}
		contextValue := value
		contextValue.Phase = terminal.Phase
		contextValue.Plan = terminal.Handover.Plan
		contextValue.Final = terminal.Handover.Final
		if !validGatewayRebindFinalHandoverProgressContext(contextValue, terminal) {
			t.Fatalf("abort from %s failed contextual validation", source.Phase)
		}
	}
}

func TestGatewayRebindFinalHandoverProgressStoreRejectsForgedContextBeforeWrite(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindFinalHandoverPlan)
	}{
		{"configuration digest", func(plan *gatewayRebindFinalHandoverPlan) {
			plan.FinalConfigurationDigest = strings.Repeat("f", 64)
		}},
		{"local host port", func(plan *gatewayRebindFinalHandoverPlan) {
			plan.LocalHostPort++
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
			manager, dataRoot := handoverTestManager(t, value)
			before, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(before.Progress) != 12 {
				t.Fatalf("initial history: progress=%d error=%v", len(before.Progress), err)
			}
			plan, err := gatewayRebindFinalHandoverPlanFor(value, initial)
			if err != nil {
				t.Fatal(err)
			}
			record, err := newGatewayRebindFinalHandoverIntentProgress(value.Intent, value.SequenceTwelve,
				plan, gatewayRebindProgressTimestamp(13))
			if err != nil {
				t.Fatal(err)
			}
			record.Handover = cloneGatewayRebindFinalHandoverProgress(record.Handover)
			test.mutate(record.Handover.Plan)
			record.Handover.Plan.Digest, err = gatewayRebindFinalHandoverPlanDigest(*record.Handover.Plan)
			if err != nil {
				t.Fatal(err)
			}
			record.Digest, err = gatewayRebindProgressDigest(record)
			if err != nil || !validGatewayRebindProgressRecord(record) {
				t.Fatalf("forged record is not self-consistent: %v", err)
			}
			store, err := newGatewayRebindProgressStore(dataRoot, record.Generation, record.OperationID, record.Sequence)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.installExact(context.Background(), record); err == nil {
				t.Fatal("context-forged sequence thirteen was installed")
			}
			if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected sequence thirteen created a protected file: %v", err)
			}
			after, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(after.Progress) != len(before.Progress) || len(after.Terminals) != 0 {
				t.Fatalf("prior history unreadable after rejection: progress=%d terminals=%d error=%v",
					len(after.Progress), len(after.Terminals), err)
			}
			for index := range before.Progress {
				if !reflect.DeepEqual(before.Progress[index].Record, after.Progress[index].Record) {
					t.Fatalf("rejected write changed sequence %d", index+1)
				}
			}
		})
	}
}

func TestGatewayRebindFinalHandoverProgressRejectsForgedBindingsAndIllegalEdges(t *testing.T) {
	value, initial := newGatewayRebindFinalHandoverPlanTestContext(t)
	plan, err := gatewayRebindFinalHandoverPlanFor(value, initial)
	if err != nil {
		t.Fatal(err)
	}
	sequenceThirteen, err := newGatewayRebindFinalHandoverIntentProgress(value.Intent, value.SequenceTwelve,
		plan, gatewayRebindProgressTimestamp(13))
	if err != nil {
		t.Fatal(err)
	}
	forged := sequenceThirteen
	forged.Handover = cloneGatewayRebindFinalHandoverProgress(sequenceThirteen.Handover)
	forged.Handover.Plan.FinalConfigDigest = strings.Repeat("a", 64)
	forged.Handover.Plan.Digest, _ = gatewayRebindFinalHandoverPlanDigest(*forged.Handover.Plan)
	forged.Digest, _ = gatewayRebindProgressDigest(forged)
	contextValue := value
	contextValue.Phase = forged.Phase
	contextValue.Plan = forged.Handover.Plan
	if validGatewayRebindFinalHandoverProgressContext(contextValue, forged) {
		t.Fatal("self-consistent forged handover plan passed predecessor-aware validation")
	}
	if _, err := newGatewayRebindFinalHandoverRollbackProgress(value.Intent, value.SequenceTwelve,
		gatewayRebindProgressTimestamp(13)); err == nil {
		t.Fatal("rollback edge before handover intent was accepted")
	}
	if _, err := newGatewayRebindFinalHandoverTerminalProgress(value.Intent, sequenceThirteen,
		gatewayRebindFinalHandoverOutcomeCommit, initial, "", gatewayRebindProgressTimestamp(14)); err == nil {
		t.Fatal("terminal commit skipped final binding, cutover and serving proof")
	}
}

func handoverTestObservation(t *testing.T, source gatewayRebindFinalHandoverObservation,
	mutate func(*gatewayRebindFinalHandoverObservation),
) gatewayRebindFinalHandoverObservation {
	t.Helper()
	value := source
	value.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), source.ApplicationNetworks...)
	mutate(&value)
	var err error
	value.Digest, err = gatewayRebindFinalHandoverObservationDigest(value)
	if err != nil || !validGatewayRebindFinalHandoverObservationValue(value) {
		t.Fatalf("invalid handover test observation: %v", err)
	}
	return value
}

func cloneGatewayRebindFinalHandoverProgress(source *gatewayRebindFinalHandoverProgress) *gatewayRebindFinalHandoverProgress {
	if source == nil {
		return nil
	}
	value := *source
	if source.Plan != nil {
		plan := *source.Plan
		plan.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), source.Plan.ApplicationNetworks...)
		value.Plan = &plan
	}
	return &value
}
