package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGatewayCurrentNativeAbortEmergencyRequiresCompleteBoundHistory(t *testing.T) {
	f := gatewayCurrentNativeEmergencyAbortedFixture(t)
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Checkpoints) != 1 || len(history.TerminalsV2) != 1 {
		t.Fatalf("missing completed fixture: %v", err)
	}
	checkpointStore := history.Checkpoints[0].Store
	terminalStore := history.TerminalsV2[0].Store
	nativeStore := history.Predecessor.Store
	if err := upgradeProtectedWrite(f.manager.store.path, statePurpose, []byte("{")); err != nil {
		t.Fatal(err)
	}
	f.manager.options.RebindCurrentStateRepository = nil
	f.manager.options.RebindFenceCheck = func(context.Context) error { return errors.New("SQL unavailable") }
	// Each mutation starts from a positively proved complete immutable census.
	for _, mutation := range []struct {
		name  string
		apply func(t *testing.T) func()
	}{
		{"missing checkpoint", func(t *testing.T) func() {
			return removeGatewayCurrentAbortTestArtifact(t, checkpointStore.path, checkpointStore.purpose)
		}},
		{"missing terminal", func(t *testing.T) func() {
			return removeGatewayCurrentAbortTestArtifact(t, terminalStore.path, terminalStore.purpose)
		}},
		{"valid journal changed after checkpoint", func(t *testing.T) func() {
			journal := history.Predecessor.Journal
			journal.Resources.FinalContainerID = strings.Repeat("8", 64)
			if !validGatewayMigrationJournal(journal) {
				t.Fatal("journal mutation must stay structurally valid")
			}
			return replaceGatewayCurrentAbortTestArtifact(t, nativeStore.journalPath, nativeStore.journalPurpose, journal)
		}},
		{"valid checkpoint changed after terminal", func(t *testing.T) func() {
			body, err := json.Marshal(history.Checkpoints[0].Checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint gatewayRebindPredecessorCheckpoint
			if err := json.Unmarshal(body, &checkpoint); err != nil {
				t.Fatal(err)
			}
			for id, app := range checkpoint.UpgradeState.Apps {
				app.Route.Endpoints[0].ContainerID = strings.Repeat("9", 64)
				checkpoint.UpgradeState.Apps[id] = app
				break
			}
			checkpoint.SourceStateDigest, err = canonicalDigest(*checkpoint.UpgradeState)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint.Digest, err = gatewayRebindPredecessorCheckpointDigest(checkpoint)
			if err != nil || !validGatewayRebindPredecessorCheckpoint(checkpoint) {
				t.Fatal("invalid checkpoint mutation")
			}
			return replaceGatewayCurrentAbortTestArtifact(t, checkpointStore.path, checkpointStore.purpose, checkpoint)
		}},
		{"additional native generation", func(t *testing.T) func() {
			store, err := newGatewayUpgradeGenerationStore(f.manager.options.DataRoot, 1, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			if err := upgradeProtectedWrite(store.v2Path, store.v2Purpose, []byte("{")); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Remove(store.v2Path); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"orphan current bundle", func(t *testing.T) func() {
			fixture := newGatewayCurrentStateFixture(t)
			store, err := newGatewayCurrentRouteStateStore(f.manager.options.DataRoot, fixture.baseline.Lineage)
			if err != nil || store.installBaseline(fixture.baseline) != nil {
				t.Fatalf("install orphan current bundle: %v", err)
			}
			return func() {
				if err := os.Remove(store.path); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if _, census, err := f.manager.gatewayCurrentOwnedStopTargetsProtectedPartialLocked(); err != nil ||
				!census.ProtectedRebindHistory || census.CommittedOwnership || census.UnresolvedAttempt {
				t.Fatalf("positive immutable census failed: %+v %v", census, err)
			}
			undo := mutation.apply(t)
			defer undo()
			native := &fakeGatewayV2LANGrantDriver{t: t, manager: f.manager}
			f.manager.gatewayV2LANGrantDriver = native
			current, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
			if err == nil || !current.Incomplete {
				t.Fatal("incomplete or crossed history was reported complete")
			}
			if err := f.manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err == nil || len(native.events) != 0 {
				t.Fatal("unproved immutable history allowed physical withdrawal")
			}
		})
	}
	// Even a valid opening census cannot authorize successful completion after drift.
	native := &fakeGatewayV2LANGrantDriver{t: t, manager: f.manager}
	f.manager.gatewayV2LANGrantDriver = gatewayCurrentNativeEmergencyAfterStopDriver{
		fakeGatewayV2LANGrantDriver: native, after: func() {
			if err := os.WriteFile(filepath.Join(f.manager.store.root, "unexplained.bundle"), []byte("unknown"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	}
	if err := f.manager.stopOwnedGatewayV2OnStartupFailure(context.Background()); err == nil || !native.gatewayStopped ||
		!f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatal("late immutable history drift escaped final check")
	}
}

func removeGatewayCurrentAbortTestArtifact(t *testing.T, path, purpose string) func() {
	t.Helper()
	body, err := upgradeProtectedRead(path, purpose)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return func() {
		defer clear(body)
		if err := upgradeProtectedWrite(path, purpose, body); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceGatewayCurrentAbortTestArtifact(t *testing.T, path, purpose string, value any) func() {
	t.Helper()
	original, err := upgradeProtectedRead(path, purpose)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	if err := upgradeProtectedWrite(path, purpose, body); err != nil {
		t.Fatal(err)
	}
	return func() {
		defer clear(original)
		if err := upgradeProtectedWrite(path, purpose, original); err != nil {
			t.Fatal(err)
		}
	}
}
