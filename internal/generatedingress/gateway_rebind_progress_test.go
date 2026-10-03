package generatedingress

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func gatewayRebindProgressFixture(t *testing.T) (gatewayRebindPredecessorFixture, gatewayRebindProtectedIntent) {
	t.Helper()
	fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
	selection, err := fixture.manager.resolveGatewayRebindProtectedIntentLocked(observation.result.RebindOperationID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, selection.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.Store.installExact(intent); err != nil {
		t.Fatal(err)
	}
	return fixture, intent
}

func gatewayRebindProgressTimestamp(sequence uint64) time.Time {
	return time.Date(2026, time.October, 3, 12, 0, int(sequence), 0, time.UTC)
}

func gatewayRebindProgressStageObservation(intent gatewayRebindProtectedIntent,
	sequence uint64,
) gatewayRebindStageIntentObservation {
	return gatewayRebindStageIntentObservation{
		OccurredAt:            gatewayRebindProgressTimestamp(sequence),
		ObservedDockerImageID: strings.Repeat("b", 64),
		NetworkTopologyDigest: intent.NetworkObservationDigest,
	}
}

func TestGatewayRebindProgressInstallsScansAndReplaysExactPreEffectRecords(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	beforeDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeCommands := len(fixture.runner.commands)

	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatalf("exact first replay: %v", err)
	}
	loadedFirst, err := firstStore.load()
	if err != nil || !reflect.DeepEqual(loadedFirst, first) {
		t.Fatalf("first readback=%#v err=%v", loadedFirst, err)
	}

	second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
	if err != nil {
		t.Fatal(err)
	}
	if second.Stage == nil || second.Stage.ApprovedCaddyImageDigest != intent.Intent.Identity.CaddyImageDigest ||
		second.Stage.ObservedDockerImageID == second.Stage.ApprovedCaddyImageDigest {
		t.Fatalf("stage image bindings were conflated: %#v", second.Stage)
	}
	secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondStore.installExact(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.installExact(context.Background(), second); err != nil {
		t.Fatalf("exact stage replay: %v", err)
	}

	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Intents) != 1 || len(history.Progress) != 2 ||
		!reflect.DeepEqual(history.Progress[0].Record, first) || !reflect.DeepEqual(history.Progress[1].Record, second) {
		t.Fatalf("progress history=%#v err=%v", history, err)
	}
	if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
		t.Fatal("legacy history scanner accepted rebind progress")
	}
	if _, err := fixture.manager.scanGatewayUpgradeHistoryLocked(); err == nil {
		t.Fatal("legacy ownership scanner accepted rebind progress")
	}
	afterDatabase, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(afterDatabase, beforeDatabase) || len(fixture.runner.commands) != beforeCommands {
		t.Fatalf("pre-effect progress changed SQLite or Docker commands: database=%t commands=%d err=%v",
			reflect.DeepEqual(afterDatabase, beforeDatabase), len(fixture.runner.commands)-beforeCommands, err)
	}
}

func TestGatewayRebindProgressRejectsMissingIntentInvalidOrderingAndCancellationBeforeWrite(t *testing.T) {
	t.Run("missing installed protected intent", func(t *testing.T) {
		fixture, snapshot, predecessor, observation := gatewayRebindProtectedIntentFixture(t)
		intent, err := newGatewayRebindProtectedIntent(snapshot, predecessor, observation, predecessor.Generation+1)
		if err != nil {
			t.Fatal(err)
		}
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err == nil {
			t.Fatal("progress without an installed protected intent was accepted")
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing-intent install created progress artifact: %v", err)
		}
	})

	t.Run("stage record without installed successor record", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), second); err == nil {
			t.Fatal("stage record without successor record was accepted")
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stage-before-successor install created progress artifact: %v", err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err != nil {
			t.Fatalf("rejected stage-before-successor changed protected history: %v", err)
		}
	})

	t.Run("cancelled before write", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.installExact(ctx, first); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled install error=%v", err)
		}
		if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled install created progress artifact: %v", err)
		}
	})
}

func TestGatewayRebindProgressRejectsInvalidStageObservation(t *testing.T) {
	_, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageIntentObservation)
	}{
		{"equal timestamp", func(value *gatewayRebindStageIntentObservation) {
			value.OccurredAt = gatewayRebindProgressTimestamp(1)
		}},
		{"backward timestamp", func(value *gatewayRebindStageIntentObservation) {
			value.OccurredAt = gatewayRebindProgressTimestamp(1).Add(-time.Second)
		}},
		{"malformed Docker image ID", func(value *gatewayRebindStageIntentObservation) {
			value.ObservedDockerImageID = "sha256:invalid"
		}},
		{"malformed network topology digest", func(value *gatewayRebindStageIntentObservation) {
			value.NetworkTopologyDigest = "invalid"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := gatewayRebindProgressStageObservation(intent, 2)
			test.mutate(&observation)
			value, err := newGatewayRebindStageIntentProgress(intent, first, observation)
			if err == nil || !reflect.DeepEqual(value, gatewayRebindProgressRecord{}) {
				t.Fatalf("invalid stage observation was accepted: record=%#v err=%v", value, err)
			}
		})
	}
}

func TestGatewayRebindProgressCreateOnlyAmbiguityRequiresFreshStrictReplay(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	original := upgradeProtectedWriteNew
	upgradeProtectedWriteNew = func(path, purpose string, body []byte) error {
		if err := original(path, purpose, body); err != nil {
			return err
		}
		return errors.New("injected post-install uncertainty")
	}
	t.Cleanup(func() { upgradeProtectedWriteNew = original })
	if err := store.installExact(context.Background(), first); err == nil {
		t.Fatal("ambiguous first progress install was reported as success")
	}
	upgradeProtectedWriteNew = original
	if err := store.installExact(context.Background(), first); err != nil {
		t.Fatalf("fresh strict exact replay after ambiguity: %v", err)
	}
}

func TestGatewayRebindProgressRejectsForgedBindingsBeforeAndDuringHistoryScan(t *testing.T) {
	fixture, intent := gatewayRebindProgressFixture(t)
	first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindProgressRecord)
		valid  bool
	}{
		{"wrong phase", func(value *gatewayRebindProgressRecord) { value.Phase = gatewayRebindProgressSuccessorIntent }, false},
		{"wrong scoped purpose", func(value *gatewayRebindProgressRecord) { value.Purpose = "wrong" }, false},
		{"forged protected digest", func(value *gatewayRebindProgressRecord) { value.ProtectedIntentDigest = strings.Repeat("d", 64) }, true},
		{"resource plan substitution", func(value *gatewayRebindProgressRecord) {
			value.Stage.NetworkPlan = gatewayRebindSuccessorIntentNetwork{
				Subnet: "10.242.0.16/28", GatewayIPv4: "10.242.0.17", ContainerIPv4: "10.242.0.18",
			}
		}, true},
		{"observed image is approved digest", func(value *gatewayRebindProgressRecord) {
			value.Stage.ObservedDockerImageID = value.Stage.ApprovedCaddyImageDigest
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			second, makeErr := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			test.mutate(&second)
			second.Digest, makeErr = gatewayRebindProgressDigest(second)
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			if validGatewayRebindProgressRecord(second) != test.valid {
				t.Fatalf("structural validity=%t want %t for %#v", validGatewayRebindProgressRecord(second), test.valid, second)
			}
			store, storeErr := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if test.valid {
				if err := store.installExact(context.Background(), second); err == nil {
					t.Fatal("forged structural record was installed")
				}
				if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("forged record created an artifact: %v", err)
				}
				return
			}
			if err := store.installExact(context.Background(), second); err == nil {
				t.Fatal("invalid record was installed")
			}
		})
	}
}

func TestGatewayRebindProgressHistoryRejectsNamespaceTamperingAndChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindStageIntent)
	}{
		{name: "sequence three image substitution", mutate: func(stage *gatewayRebindStageIntent) {
			stage.ObservedDockerImageID = strings.Repeat("d", 64)
		}},
		{name: "sequence three topology substitution", mutate: func(stage *gatewayRebindStageIntent) {
			stage.NetworkTopologyDigest = strings.Repeat("d", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, intent := gatewayRebindProgressFixture(t)
			first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
			if err != nil {
				t.Fatal(err)
			}
			firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 1)
			if err != nil || firstStore.installExact(context.Background(), first) != nil {
				t.Fatalf("install sequence one: %v", err)
			}
			second, err := newGatewayRebindStageIntentProgress(intent, first,
				gatewayRebindProgressStageObservation(intent, 2))
			if err != nil {
				t.Fatal(err)
			}
			secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 2)
			if err != nil || secondStore.installExact(context.Background(), second) != nil {
				t.Fatalf("install sequence two: %v", err)
			}
			ownership, err := gatewayRebindStageNetworkOwnershipDigest(intent)
			if err != nil {
				t.Fatal(err)
			}
			third, err := newGatewayRebindStageNetworkProgress(intent, second,
				strings.Repeat("a", 64), ownership, gatewayRebindProgressTimestamp(3))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(third.Stage)
			third.Digest, err = gatewayRebindProgressDigest(third)
			if err != nil || !validGatewayRebindProgressRecord(third) {
				t.Fatalf("forged sequence three is not structurally valid: %v", err)
			}
			thirdStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot,
				intent.Generation, intent.OperationID, 3)
			if err != nil {
				t.Fatal(err)
			}
			state := gatewayUpgradeStateStore{directory: thirdStore.directory}
			if err := state.writeExact(thirdStore.path, thirdStore.purpose, third, false,
				maxGatewayRebindProgressBytes); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatal("scanner accepted sequence three with a substituted sequence two stage field")
			}
		})
	}

	t.Run("persisted stage without successor sequence", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		name, _ := gatewayRebindProgressName(intent.Generation, intent.OperationID, 2)
		if err := os.WriteFile(filepath.Join(fixture.manager.store.root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil ||
			!strings.Contains(err.Error(), "sequence gap") {
			t.Fatalf("persisted sequence two without one was not rejected as a gap: %v", err)
		}
	})

	t.Run("unsupported future progress sequence", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := firstStore.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := secondStore.installExact(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		name, _ := gatewayRebindProgressName(intent.Generation, intent.OperationID, 4)
		if err := os.WriteFile(filepath.Join(fixture.manager.store.root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil ||
			!strings.Contains(err.Error(), "sequence gap") {
			t.Fatalf("unsupported fourth progress sequence was accepted: %v", err)
		}
	})

	t.Run("same generation file collision", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		name, _ := gatewayRebindProgressName(intent.Generation, uuid.NewString(), 1)
		path := filepath.Join(fixture.manager.store.root, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("different-operation progress collision was accepted")
		}
		if _, err := readGatewayHistorySnapshot(fixture.manager.store); err == nil {
			t.Fatal("legacy scanner accepted progress collision")
		}
	})

	t.Run("self-consistent persisted field substitution", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		firstStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := firstStore.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		second, err := newGatewayRebindStageIntentProgress(intent, first, gatewayRebindProgressStageObservation(intent, 2))
		if err != nil {
			t.Fatal(err)
		}
		secondStore, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := secondStore.installExact(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		forged := second
		forged.Stage = &gatewayRebindStageIntent{
			Identity:                 second.Stage.Identity,
			ApprovedCaddyImageDigest: second.Stage.ApprovedCaddyImageDigest,
			ObservedDockerImageID:    second.Stage.ObservedDockerImageID,
			NetworkPlan: gatewayRebindSuccessorIntentNetwork{
				Subnet: "10.242.0.16/28", GatewayIPv4: "10.242.0.17", ContainerIPv4: "10.242.0.18",
			},
			NetworkPlanDigest:     second.Stage.NetworkPlanDigest,
			NetworkTopologyDigest: second.Stage.NetworkTopologyDigest,
		}
		forged.Digest, err = gatewayRebindProgressDigest(forged)
		if err != nil || !validGatewayRebindProgressRecord(forged) {
			t.Fatalf("forged record was not structurally self-consistent: %v", err)
		}
		state := gatewayUpgradeStateStore{directory: secondStore.directory}
		if err := state.writeExact(secondStore.path, secondStore.purpose, forged, false, maxGatewayRebindProgressBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
			t.Fatal("rebind-aware scan accepted recomputed resource-plan substitution")
		}
	})

	t.Run("unknown malformed and case-folded namespace", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		name, _ := gatewayRebindProgressName(intent.Generation, intent.OperationID, 1)
		for _, candidate := range []string{
			"gateway-rebind-progress.v1.unknown.bundle",
			gatewayRebindProgressFilenamePrefix + "not-a-generation." + intent.OperationID + ".s01.bundle",
			"Gateway-rebind-progress.v1.g" + strings.TrimPrefix(name, gatewayRebindProgressFilenamePrefix),
		} {
			if err := os.WriteFile(filepath.Join(fixture.manager.store.root, candidate), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatalf("rebind-aware scanner accepted %q", candidate)
			}
			if err := os.Remove(filepath.Join(fixture.manager.store.root, candidate)); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("changed artifact during scan", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		checkpoint := func() {
			file, openErr := os.OpenFile(store.path, os.O_RDWR, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			byteValue := []byte{0}
			if _, readErr := file.ReadAt(byteValue, 0); readErr != nil {
				_ = file.Close()
				t.Fatal(readErr)
			}
			byteValue[0] ^= 0xff
			if _, writeErr := file.WriteAt(byteValue, 0); writeErr != nil {
				_ = file.Close()
				t.Fatal(writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(checkpoint); err == nil {
			t.Fatal("changed progress artifact was accepted")
		}
	})

	t.Run("same-content replacement during scan", func(t *testing.T) {
		fixture, intent := gatewayRebindProgressFixture(t)
		first, err := newGatewayRebindSuccessorIntentProgress(intent, gatewayRebindProgressTimestamp(1))
		if err != nil {
			t.Fatal(err)
		}
		store, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, intent.Generation, intent.OperationID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.installExact(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		originalInfo, err := os.Stat(store.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(store.path)
		if err != nil {
			t.Fatal(err)
		}
		replacementPath := filepath.Join(fixture.manager.store.root, "progress-replacement.tmp")
		if err := os.WriteFile(replacementPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		replacementInfo, err := os.Stat(replacementPath)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(originalInfo, replacementInfo) {
			t.Fatal("replacement unexpectedly shares the original file identity")
		}
		checkpointCalls := 0
		checkpoint := func() {
			checkpointCalls++
			if err := os.Remove(store.path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacementPath, store.path); err != nil {
				t.Fatal(err)
			}
			installedInfo, err := os.Stat(store.path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(replacementInfo, installedInfo) {
				t.Fatal("renamed replacement lost its file identity")
			}
			if loaded, err := store.load(); err != nil || !reflect.DeepEqual(loaded, first) {
				t.Fatalf("same-content replacement failed exact protected readback: %#v %v", loaded, err)
			}
		}
		if _, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(checkpoint); err == nil {
			t.Fatal("same-content replacement of a retained progress artifact was accepted")
		}
		if checkpointCalls != 1 {
			t.Fatalf("checkpoint calls=%d, want one", checkpointCalls)
		}
	})

	t.Run("generation overflow", func(t *testing.T) {
		fixture := newGatewayRebindPredecessorFixture(t)
		if _, err := newGatewayRebindProgressStore(fixture.manager.options.DataRoot, math.MaxUint64,
			uuid.NewString(), 1); err == nil {
			t.Fatal("maximum generation was accepted for progress storage")
		}
	})
}
