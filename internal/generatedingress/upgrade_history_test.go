package generatedingress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/generatedruntime"
)

func TestGatewayHistoryScannerSelectsCommittedGenerationZero(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	installCommittedV2Pair(t, manager)

	history, err := manager.scanGatewayUpgradeHistoryLocked()
	if err != nil {
		t.Fatalf("scan committed generation zero: %v", err)
	}
	if !history.committed || history.store == nil || history.store.generation != 0 || history.journal.Phase != gatewayPhaseCommitted {
		t.Fatalf("committed history = %#v", history)
	}
}

func TestGatewayHistoryRetirementPermitsV1AfterSourceStateChanges(t *testing.T) {
	manager, runner := newManagerFixture(t, false)
	store, state, journal := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
	if _, err := store.installRollbackRetirementReceipt(state, journal); err != nil {
		t.Fatal(err)
	}

	changed := routeState{Version: stateVersion, Active: map[string]routeRecord{}}
	if err := manager.store.save(changed); err != nil {
		t.Fatal(err)
	}

	history, err := manager.scanGatewayUpgradeHistoryLocked()
	if err != nil || history.committed {
		t.Fatalf("retired history after v1 change = %#v, err=%v", history, err)
	}
	if err := manager.fenceLegacyV1Locked(); err != nil {
		t.Fatalf("retired history did not release v1 fence: %v", err)
	}
	if err := manager.Switch(context.Background(), switchRequest(runner)); err != nil {
		t.Fatalf("retired history did not permit a v1 switch: %v", err)
	}
	selection, err := manager.selectGatewayUpgradeGenerationLocked(journal.OperationID)
	if err != nil || !selection.Existing || !selection.Retired || selection.Generation != 0 || !reflect.DeepEqual(selection.State, state) {
		t.Fatalf("historical replay selection = %#v, err=%v", selection, err)
	}
}

func TestGatewayHistoryResolutionResumesExactNonterminalTail(t *testing.T) {
	phases := []gatewayMigrationPhase{gatewayPhasePrepared, gatewayPhaseStaged, gatewayPhaseUncertain}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			manager, _ := newManagerFixture(t, false)
			source, input := upgradeTestPreparation(t)
			if err := manager.store.save(source); err != nil {
				t.Fatal(err)
			}
			state, journal, err := prepareGatewayV2State(source, input)
			if err != nil {
				t.Fatal(err)
			}
			store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.createV2State(state); err != nil {
				t.Fatal(err)
			}
			if err := store.createMigrationJournal(journal); err != nil {
				t.Fatal(err)
			}
			if phase != gatewayPhasePrepared {
				journal, err = store.transitionMigrationJournal(input.OperationID, gatewayPhasePrepared, gatewayPhaseStageIntent)
				if err != nil {
					t.Fatal(err)
				}
				bindUpgradeStageResources(t, store, input.OperationID)
				if phase == gatewayPhaseStaged {
					journal, err = store.transitionMigrationJournal(input.OperationID, gatewayPhaseStageIntent, gatewayPhaseStaged)
				} else {
					journal, err = store.transitionMigrationJournal(input.OperationID, gatewayPhaseStageIntent, gatewayPhaseUncertain)
				}
				if err != nil {
					t.Fatal(err)
				}
			}

			selection, err := manager.resolveGatewayUpgradeGenerationLocked(input.OperationID)
			if err != nil || !selection.Existing || selection.Retired || selection.Generation != 0 ||
				selection.Journal.Phase != phase || !reflect.DeepEqual(selection.State, state) {
				t.Fatalf("resume selection = %#v, err=%v", selection, err)
			}
			if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
				t.Fatal("nonterminal tail released strict ownership scanner")
			}
			if err := manager.fenceLegacyV1Locked(); err == nil {
				t.Fatal("nonterminal tail released v1 fence")
			}
			if _, err := manager.resolveGatewayUpgradeGenerationLocked("77777777-7777-4777-8777-777777777777"); err == nil {
				t.Fatal("nonterminal tail allowed a different operation")
			}
		})
	}
}

func TestGatewayHistoryResolutionRepairsOnlyExactLatestStateOnlyTail(t *testing.T) {
	t.Run("generation zero exact retry", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		source, input := upgradeTestPreparation(t)
		if err := manager.store.save(source); err != nil {
			t.Fatal(err)
		}
		state, journal, err := prepareGatewayV2State(source, input)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.createV2State(state); err != nil {
			t.Fatal(err)
		}

		selection, err := manager.resolveGatewayUpgradeGenerationLocked(input.OperationID)
		if err != nil || selection.Store == nil || selection.Generation != 0 || selection.Existing || !selection.PartialState ||
			!reflect.DeepEqual(selection.State, state) || selection.Journal != (gatewayMigrationJournal{}) {
			t.Fatalf("state-only selection = %#v, err=%v", selection, err)
		}
		if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
			t.Fatal("state-only tail released strict ownership scanner")
		}
		if _, err := manager.selectGatewayUpgradeGenerationLocked(input.OperationID); err == nil {
			t.Fatal("state-only tail was selectable for retirement")
		}
		if _, err := manager.resolveGatewayUpgradeGenerationLocked("77777777-7777-4777-8777-777777777777"); err == nil {
			t.Fatal("state-only tail accepted another operation")
		}

		if err := store.createMigrationJournal(journal); err != nil {
			t.Fatal(err)
		}
		repaired, err := manager.resolveGatewayUpgradeGenerationLocked(input.OperationID)
		if err != nil || !repaired.Existing || repaired.PartialState || repaired.Journal.Phase != gatewayPhasePrepared {
			t.Fatalf("repaired generation = %#v, err=%v", repaired, err)
		}
	})

	t.Run("later generation exact retry", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		store0, state0, journal0 := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
		if _, err := store0.installRollbackRetirementReceipt(state0, journal0); err != nil {
			t.Fatal(err)
		}
		source, input := upgradeTestPreparation(t)
		input.OperationID = "77777777-7777-4777-8777-777777777777"
		state, _, err := prepareGatewayV2State(source, input)
		if err != nil {
			t.Fatal(err)
		}
		store1, err := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 1, input.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store1.createV2State(state); err != nil {
			t.Fatal(err)
		}
		selection, err := manager.resolveGatewayUpgradeGenerationLocked(input.OperationID)
		if err != nil || selection.Generation != 1 || !selection.PartialState || selection.Store.v2Path != store1.v2Path {
			t.Fatalf("later partial selection = %#v, err=%v", selection, err)
		}
	})

	t.Run("stale source", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		source, input := upgradeTestPreparation(t)
		if err := manager.store.save(source); err != nil {
			t.Fatal(err)
		}
		state, _, err := prepareGatewayV2State(source, input)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.createV2State(state); err != nil {
			t.Fatal(err)
		}
		if err := manager.store.save(routeState{Version: stateVersion, Active: map[string]routeRecord{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.resolveGatewayUpgradeGenerationLocked(input.OperationID); err == nil {
			t.Fatal("state-only tail with changed v1 source was accepted")
		}
	})

	t.Run("journal only", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		source, input := upgradeTestPreparation(t)
		if err := manager.store.save(source); err != nil {
			t.Fatal(err)
		}
		_, journal, err := prepareGatewayV2State(source, input)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.writeExact(store.journalPath, store.journalPurpose, journal, true, maxGatewayMigrationBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.resolveGatewayUpgradeGenerationLocked(input.OperationID); err == nil {
			t.Fatal("journal-only history was accepted")
		}
	})
}

func TestGatewayV2InitialStateMustExactlyMirrorSourceRoutes(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	source, input := upgradeTestPreparation(t)
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	state, _, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	app := state.Apps[upgradeTestAppA]
	app.Route.Slot = generatedruntime.SlotGreen
	state.Apps[upgradeTestAppA] = app
	if !validGatewayV2RouteState(state) {
		t.Fatal("route-divergent test state should remain structurally valid")
	}
	store, err := newGatewayUpgradeStateStore(manager.options.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err == nil {
		t.Fatal("initial v2 state with routes divergent from source was accepted")
	}
}

func TestGatewayHistoryMissingOrTamperedRetirementReceiptFailsClosed(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
		if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
			t.Fatal("unretired rollback released v1 fence")
		}
	})

	t.Run("tampered source", func(t *testing.T) {
		manager, _ := newManagerFixture(t, false)
		store, state, journal := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
		receipt, err := store.installRollbackRetirementReceipt(state, journal)
		if err != nil {
			t.Fatal(err)
		}
		receipt.Source.IdentityDigest = strings.Repeat("f", 64)
		body, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err := upgradeProtectedWrite(store.receiptPath, store.receiptPurpose, body); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
			t.Fatal("tampered retirement receipt released v1 fence")
		}
	})
}

func TestGatewayHistoryInPlaceMutationDuringScanFailsClosed(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	store, state, journal := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
	if _, err := store.installRollbackRetirementReceipt(state, journal); err != nil {
		t.Fatal(err)
	}

	originalRead := upgradeProtectedRead
	mutated := false
	upgradeProtectedRead = func(path, purpose string) ([]byte, error) {
		plaintext, err := originalRead(path, purpose)
		if err != nil || mutated || purpose != store.receiptPurpose {
			return plaintext, err
		}
		before, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil || len(raw) == 0 {
			t.Fatalf("read protected receipt for mutation: bytes=%d, err=%v", len(raw), readErr)
		}
		offset := len(raw) / 2
		changed := []byte{raw[offset] ^ 0xff}
		file, openErr := os.OpenFile(path, os.O_WRONLY, 0)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteAt(changed, int64(offset)); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			t.Fatal(syncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		// Restore the visible timestamp so this test proves content hashing,
		// rather than only metadata comparison, detects the in-place rewrite.
		if timeErr := os.Chtimes(path, before.ModTime(), before.ModTime()); timeErr != nil {
			t.Fatal(timeErr)
		}
		after, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("test mutation changed file identity or metadata: before=%#v after=%#v", before, after)
		}
		mutated = true
		return plaintext, nil
	}
	t.Cleanup(func() { upgradeProtectedRead = originalRead })

	if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
		t.Fatal("in-place history mutation was accepted")
	}
	if !mutated {
		t.Fatal("test did not mutate a scanned history artifact")
	}
}

func TestGatewayHistoryLaterGenerationAndResolution(t *testing.T) {
	manager, _ := newManagerFixture(t, false)
	store0, state0, journal0 := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
	if _, err := store0.installRollbackRetirementReceipt(state0, journal0); err != nil {
		t.Fatal(err)
	}

	newOperation := "77777777-7777-4777-8777-777777777777"
	next, err := manager.resolveGatewayUpgradeGenerationLocked(newOperation)
	if err != nil || next.Existing || next.Generation != 1 || next.Store == nil || next.Store.operationID != newOperation {
		t.Fatalf("next generation = %#v, err=%v", next, err)
	}
	store1, _, journal1 := installCommittedGeneration(t, manager, next.Store, newOperation)
	history, err := manager.scanGatewayUpgradeHistoryLocked()
	if err != nil || !history.committed || history.store.v2Path != store1.v2Path || history.journal.OperationID != journal1.OperationID {
		t.Fatalf("generation one committed history = %#v, err=%v", history, err)
	}
	replay, err := manager.resolveGatewayUpgradeGenerationLocked(newOperation)
	if err != nil || !replay.Existing || replay.Generation != 1 || replay.Retired {
		t.Fatalf("committed replay = %#v, err=%v", replay, err)
	}
	if _, err := manager.resolveGatewayUpgradeGenerationLocked("88888888-8888-4888-8888-888888888888"); err == nil {
		t.Fatal("committed tail allowed another generation")
	}
	if _, err := manager.selectGatewayUpgradeGenerationLocked(journal0.OperationID); err == nil {
		t.Fatal("historical operation was selectable behind later generation")
	}
}

func TestGatewayHistoryPartialCorruptDuplicateAndGapFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Manager)
	}{
		{name: "partial", mutate: func(t *testing.T, manager *Manager) {
			store, err := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 1, "77777777-7777-4777-8777-777777777777")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.v2Path, []byte("not protected"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed reserved name", mutate: func(t *testing.T, manager *Manager) {
			if err := os.WriteFile(filepath.Join(manager.store.root, "routes-v2.g1.bad.bundle"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown abort artifact", mutate: func(t *testing.T, manager *Manager) {
			if err := os.WriteFile(filepath.Join(manager.store.root, "gateway-v1-to-v2-abort.bundle"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "generation gap", mutate: func(t *testing.T, manager *Manager) {
			store, err := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 2, "77777777-7777-4777-8777-777777777777")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.v2Path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate generation", mutate: func(t *testing.T, manager *Manager) {
			first, err := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 1, "77777777-7777-4777-8777-777777777777")
			if err != nil {
				t.Fatal(err)
			}
			second, err := newGatewayUpgradeGenerationStore(manager.options.DataRoot, 1, "88888888-8888-4888-8888-888888888888")
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{first.v2Path, second.v2Path} {
				if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, _ := newManagerFixture(t, false)
			store, state, journal := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
			if _, err := store.installRollbackRetirementReceipt(state, journal); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, manager)
			if _, err := manager.scanGatewayUpgradeHistoryLocked(); err == nil {
				t.Fatal("invalid history was accepted")
			}
			if err := manager.fenceLegacyV1Locked(); err == nil {
				t.Fatal("invalid history released v1 fence")
			}
		})
	}
}

func installRolledBackGeneration(t *testing.T, manager *Manager, generation uint64, operationID string) (*gatewayUpgradeStateStore, gatewayV2RouteState, gatewayMigrationJournal) {
	t.Helper()
	source, input := upgradeTestPreparation(t)
	input.OperationID = operationID
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	var store *gatewayUpgradeStateStore
	if generation == 0 {
		store, err = newGatewayUpgradeStateStore(manager.options.DataRoot)
	} else {
		store, err = newGatewayUpgradeGenerationStore(manager.options.DataRoot, generation, operationID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	journal, err = store.transitionMigrationJournal(operationID, gatewayPhasePrepared, gatewayPhaseRollbackIntent)
	if err != nil {
		t.Fatal(err)
	}
	journal, err = store.transitionMigrationJournal(operationID, gatewayPhaseRollbackIntent, gatewayPhaseRolledBack)
	if err != nil {
		t.Fatal(err)
	}
	return store, state, journal
}

func installCommittedGeneration(t *testing.T, manager *Manager, store *gatewayUpgradeStateStore, operationID string) (*gatewayUpgradeStateStore, gatewayV2RouteState, gatewayMigrationJournal) {
	t.Helper()
	source, input := upgradeTestPreparation(t)
	input.OperationID = operationID
	if err := manager.store.save(source); err != nil {
		t.Fatal(err)
	}
	state, journal, err := prepareGatewayV2State(source, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.createV2State(state); err != nil {
		t.Fatal(err)
	}
	if err := store.createMigrationJournal(journal); err != nil {
		t.Fatal(err)
	}
	phase := gatewayPhasePrepared
	for _, next := range []gatewayMigrationPhase{gatewayPhaseStageIntent, gatewayPhaseStaged, gatewayPhaseTransferIntent, gatewayPhaseV2Serving, gatewayPhaseCommitted} {
		if next == gatewayPhaseStaged {
			bindUpgradeStageResources(t, store, operationID)
		}
		if next == gatewayPhaseV2Serving {
			bindUpgradeFinalResource(t, store, operationID)
		}
		journal, err = store.transitionMigrationJournal(operationID, phase, next)
		if err != nil {
			t.Fatal(err)
		}
		phase = next
	}
	return store, state, journal
}
