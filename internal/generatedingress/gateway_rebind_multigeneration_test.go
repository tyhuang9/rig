package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindConcreteCompositionSecondGeneration(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "commit"
		if rollback {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			f, input, driver := newGatewayRebindMultiFixture(t)
			ctx := context.Background()
			entry := input.Inspection.Roster[0]
			ref := appaccess.GatewayBindingRef{AppID: entry.AppID, AllocationID: entry.AllocationID,
				AccessRevisionID: entry.AccessRevisionID, GrantAttemptID: entry.GrantAttemptID}
			original, err := f.repository.ResolveGatewayBinding(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			first, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver)
			if err != nil || first.FinalPhase != appaccess.GatewayRebindCommitted || !first.FenceReleased {
				t.Fatalf("first concrete commit: result=%#v err=%v", first, err)
			}
			prior := driver.backend.entries[0]
			priorID, priorConfig := prior.finalID, append([]byte(nil), prior.stage.activeBody...)
			defer clear(priorConfig)
			before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || len(before.History) != 1 || len(before.CurrentTransfers) != 1 {
				t.Fatalf("first history: %v", err)
			}
			selected, err := f.manager.selectGatewayCurrentLocked(ctx, before)
			if err != nil || selected.State == nil || selected.Store == nil {
				t.Fatalf("first selection: %v", err)
			}
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			profile := input.Inspection.Spec.SuccessorProfile
			profile.SelectedIPv4, profile.InterfaceID = "192.168.98.8", "rebind-next-successor"
			next := gatewayRebindSequenceNextInput(t, f, input.Inspection.Spec.SuccessorProfileRevisionNumber+1, profile)
			if next.Inspection.Spec.Predecessor.Lineage != selected.State.Lineage ||
				next.Inspection.Spec.Predecessor.SourceStateDigest != selected.State.Digest ||
				next.Inspection.Roster[0].PredecessorTransferDigest == nil ||
				*next.Inspection.Roster[0].PredecessorTransferDigest != before.CurrentTransfers[0].TransferDigest {
				t.Fatal("second proposal did not select the actual committed current and prior transfer")
			}
			refused := 0
			secondEffects := len(driver.backend.effects)
			driver.beforeProgress = func(record gatewayRebindProgressRecord) error {
				if rollback && record.Phase == gatewayRebindProgressHandoverCommitted {
					refused++
					return errors.New("injected second completion write failure")
				}
				return nil
			}
			second, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, next, driver)
			phase, disposition := appaccess.GatewayRebindCommitted, appaccess.GatewayRebindDispositionCommit
			if rollback {
				phase, disposition = appaccess.GatewayRebindRolledBack, appaccess.GatewayRebindDispositionAbort
			}
			if err != nil || second.FinalPhase != phase || second.Disposition != disposition || !second.FenceReleased || (rollback && refused != 1) {
				history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				t.Fatalf("second concrete result=%#v err=%v progress=%d history=%v effects=%d", second, err, len(history.Progress), historyErr, len(driver.backend.effects))
			}
			if len(driver.backend.entries) != 2 || driver.backend.entries[0] != prior || prior.finalID != priorID ||
				!prior.finalPresent || !prior.networkPresent || !prior.configPresent || !prior.dataPresent ||
				!reflect.DeepEqual(priorConfig, prior.stage.activeBody) || prior.final.Running != rollback ||
				driver.runner.finalID == priorID || driver.runner.stage.networkID == prior.stage.networkID ||
				driver.runner.stagePresent || driver.runner.finalPresent == rollback || driver.runner.final.Running == rollback ||
				prior.predecessor.FinalContainer.Running {
				t.Fatal("second handover replaced retained resources or left the wrong gateway serving")
			}
			if driver.runner.configPresent == rollback || driver.runner.dataPresent == rollback || driver.runner.networkPresent == rollback {
				t.Fatal("second generation retained the wrong volume/network presence")
			}
			priorStarts, priorStops := 0, 0
			for _, effect := range driver.backend.effects[secondEffects:] {
				name := effect[len(effect)-1]
				if effect[0] == "container" && (effect[1] == "start" || effect[1] == "stop") {
					if name == normalizeID(prior.predecessor.FinalContainer.ID) {
						t.Fatal("second attempt changed native serving state")
					}
					if name == priorID {
						if effect[1] == "start" {
							priorStarts++
						} else {
							priorStops++
						}
					}
				}
				if effect[1] == "rm" && (name == priorID || name == prior.stage.networkID ||
					name == prior.intent.Identity.ConfigVolume || name == prior.intent.Identity.DataVolume) {
					t.Fatal("removed first-generation resource")
				}
			}
			wantStarts := 0
			if rollback {
				wantStarts = 1
			}
			if priorStops != 1 || priorStarts != wantStarts {
				t.Fatalf("prior effects: starts=%d stops=%d", priorStarts, priorStops)
			}
			after, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || len(after.History) != 2 || after.Active != nil || !reflect.DeepEqual(before.History[0], after.History[0]) {
				t.Fatalf("second operation changed first SQL history: %v", err)
			}
			resolved, err := f.repository.ResolveGatewayBinding(ctx, ref)
			wantTransfers := 2
			if rollback {
				wantTransfers = 1
			}
			if err != nil || len(resolved.TransferChain) != wantTransfers ||
				!reflect.DeepEqual(resolved.TransferChain[0], before.CurrentTransfers[0]) ||
				!reflect.DeepEqual(resolved.RawAllocation, original.RawAllocation) ||
				!reflect.DeepEqual(resolved.RawAccessRevision, original.RawAccessRevision) ||
				!reflect.DeepEqual(resolved.RawGrant, original.RawGrant) || !reflect.DeepEqual(resolved.RawProfile, original.RawProfile) {
				t.Fatalf("second operation changed raw authority or transfer chain: %v", err)
			}
			if !rollback && (resolved.TransferChain[1].PredecessorTransferDigest == nil ||
				*resolved.TransferChain[1].PredecessorTransferDigest != before.CurrentTransfers[0].TransferDigest ||
				resolved.CurrentGatewaySource != second.SelectedCurrentAuthority) {
				t.Fatal("second commit did not extend the exact transfer chain")
			}
			if rollback && !reflect.DeepEqual(before.CurrentSource, after.CurrentSource) {
				t.Fatal("rollback replaced first current authority")
			}
			retained, err := selected.Store.load()
			if err != nil || !reflect.DeepEqual(retained, *selected.State) {
				t.Fatalf("first protected bundle changed: %v", err)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
			files, err = readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			effects := len(driver.backend.effects)
			fresh := freshGatewayRebindRecoveryManager(f.manager)
			fresh.gatewayRebindFailStop, fresh.gatewayRebindCommitBarrier = &atomic.Bool{}, &atomic.Bool{}
			replay := &gatewayRebindMultiDriver{gatewayRebindCompositionDriver: &gatewayRebindCompositionDriver{
				t: t, runner: driver.runner}, backend: driver.backend}
			replay.installMulti(fresh)
			recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
			if err != nil || recovered.Recovered || !recovered.FenceReleased || !validSHA256(recovered.CurrentAttestationDigest) ||
				fresh.gatewayRebindAdmissionBlocked() || len(driver.backend.effects) != effects {
				t.Fatalf("second-generation replay=%#v effects=%v err=%v", recovered, driver.backend.effects[effects:], err)
			}
			confirmed, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			confirmedFiles, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
			if err != nil || filesErr != nil || !reflect.DeepEqual(after, confirmed) || !sameGatewayHistorySnapshot(files, confirmedFiles) {
				t.Fatalf("fresh replay changed SQL/files: SQL=%v files=%v", err, filesErr)
			}
		})
	}
}
