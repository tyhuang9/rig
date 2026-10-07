package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayCurrentTypedEmergencyWithdrawsWithCorruptNativeRoute(t *testing.T) {
	for _, corrupt := range []string{"source", "native v2"} {
		t.Run(corrupt, func(t *testing.T) {
			f, input, physical := newGatewayRebindCoordinatorFixture(t)
			ctx := context.Background()
			if _, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, physical); err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := f.manager.selectGatewayCurrentLocked(ctx, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			path, purpose := f.manager.store.path, statePurpose
			if corrupt == "native v2" {
				store, err := newGatewayUpgradeStateStore(f.manager.options.DataRoot)
				if err != nil {
					t.Fatal(err)
				}
				path, purpose = store.v2Path, store.v2Purpose
			}
			if err := upgradeProtectedWrite(path, purpose, []byte("{")); err != nil {
				t.Fatal(err)
			}
			before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil); err == nil {
				t.Fatal("normal serving history accepted corrupt native state")
			}
			f.manager.options.RebindCurrentStateRepository = nil
			f.manager.options.RebindFenceCheck = func(context.Context) error { return errors.New("SQL unavailable") }
			target, err := gatewayCurrentPhysicalTargetFor(selected, *selected.State, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			localPort := f.journal.Source.LocalHostPort
			runner := newGatewayCurrentPhysicalExecutor(t, target, *selected.State, *selected.State, localPort)
			runner.lostStopAck = true
			driver := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, localPort)
			f.manager.gatewayCurrentPhysicalDriver = driver
			result, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
			if err != nil || result.Incomplete || result.OwnershipIndeterminate || !result.RebindOwnershipPresent ||
				result.VerifiedTargets != 1 || result.StoppedOrAbsentTargets != 1 || runner.container.Running ||
				len(runner.effects) != 1 || runner.effects[0][0] != "container" || runner.effects[0][1] != "stop" ||
				runner.effects[0][len(runner.effects[0])-1] != target.Resources.FinalContainer.ID {
				t.Fatalf("immutable typed withdrawal failed: result=%+v effects=%v err=%v", result, runner.effects, err)
			}
			retained, err := selected.Store.load()
			if err != nil || !reflect.DeepEqual(retained, *selected.State) || !f.manager.gatewayRebindAdmissionBlocked() {
				t.Fatal("emergency stop changed protected routes or released startup")
			}
			after, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil || !sameGatewayHistorySnapshot(before, after) {
				t.Fatal("emergency withdrawal rewrote immutable history")
			}
		})
	}
}

func TestGatewayCurrentTypedEmergencyValidatesPriorNativeGenerations(t *testing.T) {
	for _, kind := range []string{"retired", "aborted"} {
		t.Run(kind, func(t *testing.T) {
			manager, _ := newManagerFixture(t, false)
			var receiptPath, receiptPurpose string
			if kind == "retired" {
				store, state, journal := installRolledBackGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555")
				if _, err := store.installRollbackRetirementReceipt(state, journal); err != nil {
					t.Fatal(err)
				}
				receiptPath, receiptPurpose = store.receiptPath, store.receiptPurpose
			} else {
				store, _, _, receipt := preparePreJournalAbortGeneration(t, manager, 0, "55555555-5555-4555-8555-555555555555", true)
				if _, err := store.installPreJournalAbortReceipt(receipt); err != nil {
					t.Fatal(err)
				}
				receiptPath, receiptPurpose = store.abortPath, store.abortPurpose
			}
			op := "77777777-7777-4777-8777-777777777777"
			next, err := manager.resolveGatewayUpgradeGenerationLocked(op)
			if err != nil {
				t.Fatal(err)
			}
			store, state, journal := installCommittedGeneration(t, manager, next.Store, op)
			lineage, err := gatewayUpgradeCurrentLineage(gatewayUpgradeGenerationSelection{
				Store: store, Generation: 1, State: state, Journal: journal, Existing: true, operationID: op})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := newGatewayRebindPredecessorCheckpoint(2, "88888888-8888-4888-8888-888888888888", lineage, &state, nil)
			if err != nil {
				t.Fatal(err)
			}
			cpStore, err := newGatewayRebindPredecessorCheckpointStore(manager.options.DataRoot, 2, checkpoint.OperationID)
			if err != nil || cpStore.installExact(checkpoint) != nil {
				t.Fatalf("checkpoint install: %v", err)
			}
			if err := upgradeProtectedWrite(manager.store.path, statePurpose, []byte("{")); err != nil {
				t.Fatal(err)
			}
			if err := upgradeProtectedWrite(store.v2Path, store.v2Purpose, []byte("{")); err != nil {
				t.Fatal(err)
			}
			history, err := manager.scanGatewayCurrentTypedOwnedStopHistoryLocked()
			if err != nil || history.Predecessor.Generation != 1 || len(history.Checkpoints) != 1 || len(history.TerminalsV2) != 0 ||
				!reflect.DeepEqual(history.Source, routeState{}) {
				t.Fatalf("valid immutable origin was not retained as unresolved: %v", err)
			}
			undo := removeGatewayCurrentAbortTestArtifact(t, receiptPath, receiptPurpose)
			defer undo()
			if _, err := manager.scanGatewayCurrentTypedOwnedStopHistoryLocked(); err == nil {
				t.Fatal("missing prior native terminal was ignored")
			}
		})
	}
}

func TestGatewayCurrentTypedEmergencyRetainsRepeatedCommitOwnership(t *testing.T) {
	f, firstInput, template := newGatewayRebindCoordinatorFixture(t)
	ctx := context.Background()
	first := gatewayRebindSequencePhysicalDriver(t, template.template, 1)
	if _, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, firstInput, first); err != nil {
		t.Fatal(err)
	}
	profile := firstInput.Inspection.Spec.SuccessorProfile
	profile.SelectedIPv4, profile.InterfaceID = "192.168.98.8", "rebind-next-successor"
	observe := f.manager.gatewayRebindV2NetworkObserver
	f.manager.gatewayRebindV2NetworkObserver = func(ctx context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		value, err := observe(ctx, claim)
		value.Candidates = []gatewayRebindSuccessorNetworkCandidate{{InterfaceID: profile.InterfaceID,
			IPv4: profile.SelectedIPv4, Prefix: "192.168.98.0/24"}}
		value.HostInterfaces = append(append([]string(nil), value.HostInterfaces...), "192.168.98.0/24")
		sort.Strings(value.HostInterfaces)
		return value, err
	}
	second := gatewayRebindSequenceNextInput(t, f, firstInput.Inspection.Spec.SuccessorProfileRevisionNumber+1, profile)
	if _, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, second,
		gatewayRebindSequencePhysicalDriver(t, template.template, 2)); err != nil {
		t.Fatal(err)
	}
	if err := upgradeProtectedWrite(f.manager.store.path, statePurpose, []byte("{")); err != nil {
		t.Fatal(err)
	}
	f.manager.options.RebindCurrentStateRepository = nil
	f.manager.options.RebindFenceCheck = func(context.Context) error { return errors.New("SQL unavailable") }
	driver := &gatewayCurrentStartupOwnedStopDriver{}
	f.manager.gatewayCurrentPhysicalDriver = driver
	result, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
	if err != nil || result.Incomplete || result.VerifiedTargets != 2 || result.StoppedOrAbsentTargets != 2 ||
		len(driver.targets) != 2 || driver.targets[0].FinalContainer.ID == driver.targets[1].FinalContainer.ID {
		t.Fatalf("retained immutable owners lost: result=%+v calls=%d err=%v", result, len(driver.targets), err)
	}
	history, err := f.manager.scanGatewayCurrentTypedOwnedStopHistoryLocked()
	if err != nil || len(history.TerminalsV2) != 2 {
		t.Fatalf("immutable terminal history: %v", err)
	}
	terminal := history.TerminalsV2[1].Receipt
	terminal.ClaimSpecDigest = strings.Repeat("9", 64)
	terminal.Digest, err = gatewayRebindTerminalDigestV2(terminal)
	if err != nil || !validGatewayRebindTerminalReceiptV2(terminal) {
		t.Fatal("terminal mutation must stay valid")
	}
	store := history.TerminalsV2[1].Store
	undo := replaceGatewayCurrentAbortTestArtifact(t, store.path, store.purpose, terminal)
	defer undo()
	driver.targets = nil
	result, err = f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
	if err == nil || !result.Incomplete || len(driver.targets) != 0 {
		t.Fatal("structurally valid terminal crossed its exact intent/progress history")
	}
}
