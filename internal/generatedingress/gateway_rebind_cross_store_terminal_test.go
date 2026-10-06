package generatedingress

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

func installGatewayRebindTypedAttemptForTerminal(t *testing.T, fixture gatewayCurrentStateFixture,
	checkpoint gatewayRebindPredecessorCheckpoint, intent gatewayRebindProtectedIntentV2,
	resources gatewayRebindFinalHandoverResourceBindings, proof gatewayRebindFinalHandoverTerminalProof,
) gatewayRebindProgressRecord {
	t.Helper()
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install typed intent: %v", err)
	}
	progress, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	progressStore, err := newGatewayRebindProgressStore(fixture.dataRoot, progress.Generation, progress.OperationID, progress.Sequence)
	if err != nil || progressStore.installExact(context.Background(), progress) != nil {
		t.Fatalf("install typed progress: %v", err)
	}
	records, finalResources, finalProof := gatewayRebindTypedCompleteProgressFixture(t, fixture, intent, progress)
	if !reflect.DeepEqual(finalResources, resources) {
		t.Fatal("typed resource fixture disagrees with requested resources")
	}
	_ = proof
	_ = finalProof
	for _, record := range records[1:] {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed progress %d: %v", record.Sequence, storeErr)
		}
	}
	return records[len(records)-1]
}

func gatewayRebindTypedCompleteProgressFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	intent gatewayRebindProtectedIntentV2, first gatewayRebindProgressRecord,
) ([]gatewayRebindProgressRecord, gatewayRebindFinalHandoverResourceBindings,
	gatewayRebindFinalHandoverTerminalProof,
) {
	t.Helper()
	resources := fixture.receipt.Resources
	resources.ConfigVolume.Name = intent.Identity.ConfigVolume
	resources.DataVolume.Name = intent.Identity.DataVolume
	resources.Digest = ""
	var err error
	resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
	if err != nil {
		t.Fatal(err)
	}
	stagePlanDigest, err := gatewayRebindTypedStagePlanDigest(intent)
	if err != nil {
		t.Fatal(err)
	}
	effect := gatewayRebindTypedEffectProgress{ImageID: resources.ImageID, StagePlanDigest: stagePlanDigest}
	records := []gatewayRebindProgressRecord{first}
	firstAt, err := parseGatewayRebindProgressTime(first.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(2); sequence <= 17; sequence++ {
		switch sequence {
		case 3:
			network := resources.IngressNetwork
			effect.Network = &network
		case 4:
			volume := resources.ConfigVolume
			effect.ConfigVolume = &volume
		case 5:
			volume := resources.DataVolume
			effect.DataVolume = &volume
		case 6:
			container := resources.StageContainer
			effect.StageContainer = &container
		case 7:
			effect.StageConfigIntentDigest = strings.Repeat("2", 64)
		case 8:
			effect.StageConfigCopyDigest = strings.Repeat("3", 64)
		case 9:
			effect.StageStartIntentDigest = strings.Repeat("4", 64)
		case 10:
			effect.StageServingDigest = strings.Repeat("5", 64)
		case 11:
			effect.FinalConfigIntentDigest = strings.Repeat("6", 64)
		case 12:
			effect.FinalConfigCopyDigest = strings.Repeat("7", 64)
		case 13:
			effect.HandoverIntentDigest = strings.Repeat("8", 64)
			effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), resources.ApplicationNetworks...)
		case 14:
			container := *resources.FinalContainer
			effect.FinalContainer = &container
		case 15:
			effect.CutoverIntentDigest = strings.Repeat("9", 64)
		case 16:
			effect.SuccessorServingDigest = strings.Repeat("a", 64)
		case 17:
			proof := fixture.receipt.PhysicalProof
			proof.PriorProgressDigest = records[len(records)-1].Digest
			proof.Observation.FinalID = resources.FinalContainer.ID
			proof.Observation.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), resources.ApplicationNetworks...)
			proof.Digest = ""
			proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
			if err != nil {
				t.Fatal(err)
			}
			resourcesCopy, proofCopy := resources, proof
			effect.Resources, effect.PhysicalProof = &resourcesCopy, &proofCopy
		}
		record, recordErr := newGatewayRebindTypedEffectProgressV2(intent, records, effect,
			firstAt.Add(time.Duration(sequence-1)*time.Nanosecond))
		if recordErr != nil {
			t.Fatalf("build typed progress %d: %v", sequence, recordErr)
		}
		records = append(records, record)
	}
	return records, resources, *records[len(records)-1].TypedEffect.PhysicalProof
}

func gatewayRebindTypedTerminalPhysicalFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	intent gatewayRebindProtectedIntentV2, previous gatewayRebindProgressRecord,
) (gatewayRebindFinalHandoverResourceBindings, gatewayRebindFinalHandoverTerminalProof) {
	t.Helper()
	resources := fixture.receipt.Resources
	resources.ConfigVolume.Name = intent.Identity.ConfigVolume
	resources.DataVolume.Name = intent.Identity.DataVolume
	resources.Digest = ""
	var err error
	resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resources)
	if err != nil {
		t.Fatal(err)
	}
	proof := fixture.receipt.PhysicalProof
	proof.PriorProgressDigest = previous.Digest
	proof.Observation.FinalID = resources.FinalContainer.ID
	proof.Observation.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), resources.ApplicationNetworks...)
	proof.Digest = ""
	proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	if err != nil || !gatewayRebindTypedPhysicalOutcomeMatches(intent.Identity, previous.Digest, resources, proof) {
		t.Fatalf("typed physical fixture: %v", err)
	}
	return resources, proof
}

func gatewayRebindTypedRollbackProofFixture(t *testing.T, fixture gatewayCurrentStateFixture,
	predecessorRoutesDigest, priorProgressDigest string,
) gatewayRebindFinalHandoverTerminalProof {
	t.Helper()
	proof := fixture.receipt.PhysicalProof
	proof.Kind = gatewayRebindFinalHandoverOutcomeAbort
	proof.PriorProgressDigest = priorProgressDigest
	proof.Observation.Stage = gatewayRebindHandoverContainerAbsent
	proof.Observation.Final = gatewayRebindHandoverContainerAbsent
	proof.Observation.FinalID = ""
	proof.Observation.PredecessorRunning = true
	proof.Observation.PredecessorAddress = gatewayRebindPredecessorAddressPresent
	proof.Observation.ConfigVolumePresent = false
	proof.Observation.DataVolumePresent = false
	proof.Observation.IngressNetworkPresent = false
	proof.Observation.ApplicationNetworks = nil
	proof.Observation.ConfigDigest = ""
	proof.Observation.RoutesDigest = ""
	proof.Observation.PredecessorRoutesDigest = predecessorRoutesDigest
	proof.Observation.PredecessorStopDigest = ""
	proof.Observation.Digest = ""
	var err error
	proof.Observation.Digest, err = gatewayRebindFinalHandoverObservationDigest(proof.Observation)
	if err != nil {
		t.Fatal(err)
	}
	proof.Digest = ""
	proof.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proof)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestGatewayRebindTypedCommitTerminalScansAndSelectsActualLineage(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	if err := fixture.store.installBaseline(fixture.baseline); err != nil {
		t.Fatal(err)
	}
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	// The typed physical result binds every retained physical boundary and the
	// final proof to sequence sixteen rather than reusing any v1 journal proof.
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install typed intent: %v", err)
	}
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	firstStore, _ := newGatewayRebindProgressStore(fixture.dataRoot, first.Generation, first.OperationID, first.Sequence)
	if err := firstStore.installExact(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	records, resources, proof := gatewayRebindTypedCompleteProgressFixture(t, fixture, intent, first)
	for _, record := range records[1:] {
		progressStore, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || progressStore.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed progress %d: %v", record.Sequence, storeErr)
		}
	}
	progress := records[len(records)-1]
	receipt, err := newGatewayRebindCommitTerminalV2(intent, progress, resources,
		proof, time.Unix(21, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(context.Background(), receipt) != nil {
		t.Fatalf("install typed terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Terminals) != 1 || len(history.TerminalsV2) != 1 ||
		!reflect.DeepEqual(history.TerminalsV2[0].Receipt, receipt) {
		t.Fatalf("scan typed terminal: v1=%d v2=%d error=%v", len(history.Terminals), len(history.TerminalsV2), err)
	}
	typedProgress := make([]gatewayRebindProgressSelection, 0, 17)
	for _, retained := range history.Progress {
		if retained.Generation == intent.Generation {
			typedProgress = append(typedProgress, retained)
		}
	}
	if len(typedProgress) != 17 || !gatewayRebindTerminalV2MatchesIntentHistory(receipt, intent, checkpoint, typedProgress) {
		t.Fatalf("complete typed progress prefix mismatch: records=%d", len(typedProgress))
	}
	changedResources := receipt
	resourcesCopy := *receipt.Resources
	resourcesCopy.ImageID = strings.Repeat("0", 64)
	if resourcesCopy.ImageID == receipt.Resources.ImageID {
		resourcesCopy.ImageID = strings.Repeat("f", 64)
	}
	resourcesCopy.Digest, err = gatewayRebindFinalHandoverResourcesDigest(resourcesCopy)
	if err != nil {
		t.Fatal(err)
	}
	changedResources.Resources = &resourcesCopy
	changedResources.Digest, err = gatewayRebindTerminalDigestV2(changedResources)
	if err != nil || !validGatewayRebindTerminalReceiptV2(changedResources) ||
		gatewayRebindTerminalV2MatchesIntentHistory(changedResources, intent, checkpoint, typedProgress) {
		t.Fatalf("rehashed alternate terminal resources were not isolated from retained progress: %v", err)
	}
	changedProof := receipt
	proofCopy := *receipt.PhysicalProof
	proofCopy.Observation.ConfigDigest = strings.Repeat("0", 64)
	if proofCopy.Observation.ConfigDigest == receipt.PhysicalProof.Observation.ConfigDigest {
		proofCopy.Observation.ConfigDigest = strings.Repeat("f", 64)
	}
	proofCopy.Observation.Digest, err = gatewayRebindFinalHandoverObservationDigest(proofCopy.Observation)
	if err != nil {
		t.Fatal(err)
	}
	proofCopy.Digest, err = gatewayRebindFinalHandoverOutcomeDigest(proofCopy)
	if err != nil {
		t.Fatal(err)
	}
	changedProof.PhysicalProof = &proofCopy
	changedProof.Digest, err = gatewayRebindTerminalDigestV2(changedProof)
	if err != nil || !validGatewayRebindTerminalReceiptV2(changedProof) ||
		gatewayRebindTerminalV2MatchesIntentHistory(changedProof, intent, checkpoint, typedProgress) {
		t.Fatalf("rehashed alternate terminal proof was not isolated from retained progress: %v", err)
	}
	if gatewayRebindProgressMatchesIntentV2(typedProgress[16].Record, intent, typedProgress[:15]) {
		t.Fatal("typed progress matcher accepted a missing sequence sixteen")
	}
	lineage, err := gatewayRebindCurrentLineageV2(receipt)
	if err != nil || lineage.OperationID != intent.OperationID || lineage.TerminalReceiptDigest != receipt.Digest ||
		lineage.ProtectedIntentDigest != intent.Digest || lineage.ProtectedJournalDigest != "" {
		t.Fatalf("typed lineage mismatch: %#v error=%v", lineage, err)
	}
	view, err := newGatewayRebindAttemptTerminalViewV2(receipt)
	if err != nil || !gatewayRebindAttemptTerminalMatchesLineage(view, lineage) || view.LegacyReceipt != nil || view.TypedReceipt == nil {
		t.Fatalf("typed terminal union mismatch: %#v error=%v", view, err)
	}
	changedView := view
	changedView.Resources = view.Resources
	changedFinal := *view.Resources.FinalContainer
	changedFinal.ID = strings.Repeat("a", 64)
	if changedFinal.ID == view.Resources.FinalContainer.ID {
		changedFinal.ID = strings.Repeat("b", 64)
	}
	changedView.Resources.FinalContainer = &changedFinal
	changedView.Resources.Digest, err = gatewayRebindFinalHandoverResourcesDigest(changedView.Resources)
	if err != nil || gatewayRebindAttemptTerminalMatchesLineage(changedView, lineage) {
		t.Fatalf("rehashed typed terminal summary accepted: %v", err)
	}
	mixedView := view
	mixedView.LegacyReceipt = &fixture.receipt
	if gatewayRebindAttemptTerminalMatchesLineage(mixedView, lineage) {
		t.Fatal("mixed typed and legacy terminal summary accepted")
	}
	selectedView, err := gatewayCurrentSelectionTerminalView(gatewayCurrentSelection{
		Kind: gatewayCurrentSelectionRebind, Lineage: lineage, Terminal: &view,
	})
	if err != nil || !reflect.DeepEqual(selectedView, view) {
		t.Fatalf("typed current terminal adapter mismatch: %#v error=%v", selectedView, err)
	}
	transfers, err := newGatewayRebindTransfersV2(intent, receipt, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := newGatewayCurrentRouteBaselineFromV2Terminal(intent, receipt, checkpoint, transfers)
	if err != nil || len(baseline.Apps) != len(checkpoint.CurrentState.Apps) || baseline.Lineage != lineage || baseline.Revision != 1 {
		t.Fatalf("typed baseline mismatch: apps=%d error=%v", len(baseline.Apps), err)
	}
	currentStore, err := newGatewayCurrentRouteStateStore(fixture.dataRoot, lineage)
	if err != nil || currentStore.installBaseline(baseline) != nil {
		t.Fatalf("install typed baseline: %v", err)
	}
	loaded, err := currentStore.load()
	if err != nil || !reflect.DeepEqual(loaded, baseline) {
		t.Fatalf("load typed baseline: %v", err)
	}
}

func TestGatewayRebindTypedNoEffectAbortRequiresCheckpointWithoutIntent(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	proof := gatewayRebindNoEffectAbortProof{
		Version: gatewayRebindNoEffectProofVersion, Purpose: gatewayRebindNoEffectProofPurpose,
		Generation: checkpoint.Generation, OperationID: checkpoint.OperationID,
		ClaimRequestDigest: intent.Claim.RequestDigest, PredecessorCheckpointDigest: checkpoint.Digest,
		SourceStateDigest: checkpoint.SourceStateDigest, SuccessorIdentityDigest: intent.Identity.Digest,
		ObservationDigest: strings.Repeat("a", 64), CreatedAt: time.Unix(4, 0).UTC().Format(time.RFC3339Nano),
	}
	proof.Digest, err = gatewayRebindNoEffectAbortProofDigest(proof)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newGatewayRebindNoEffectAbortTerminalV2(intent.Claim, intent.Roster, checkpoint, proof, time.Unix(5, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, receipt.Generation, receipt.OperationID)
	if err != nil || store.installExact(context.Background(), receipt) != nil {
		t.Fatalf("install no-effect terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	typedProgress := 0
	for _, record := range history.Progress {
		if record.Generation == checkpoint.Generation {
			typedProgress++
		}
	}
	if err != nil || len(history.TerminalsV2) != 1 || len(history.IntentsV2) != 0 || typedProgress != 0 ||
		history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("scan no-effect abort: intents=%d progress=%d terminals=%d error=%v",
			len(history.IntentsV2), typedProgress, len(history.TerminalsV2), err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := intentStore.installExact(intent); err == nil {
		t.Fatal("typed intent was installed after an immutable no-effect abort")
	}
	if _, err := intentStore.load(); err == nil {
		t.Fatal("typed intent file was created before terminal conflict refusal")
	}
	nonIncreasing := receipt
	nonIncreasing.CreatedAt = proof.CreatedAt
	nonIncreasing.Digest, _ = gatewayRebindTerminalDigestV2(nonIncreasing)
	if !validGatewayRebindTerminalReceiptV2(nonIncreasing) ||
		gatewayRebindTerminalV2MatchesNoIntentHistory(nonIncreasing, checkpoint) {
		t.Fatal("no-effect terminal/proof time ordering was not enforced by retained-history matching")
	}
	changed := receipt
	changed.Disposition = appaccess.GatewayRebindDispositionCommit
	changed.Digest, _ = gatewayRebindTerminalDigestV2(changed)
	if validGatewayRebindTerminalReceiptV2(changed) {
		t.Fatal("no-effect abort proof was accepted as commit")
	}

	conflictFixture := newGatewayCurrentStateFixture(t)
	conflictCheckpoint, conflictIntent := gatewayRebindAttemptTypedFixture(t, conflictFixture)
	conflictCheckpointStore, _ := newGatewayRebindPredecessorCheckpointStore(conflictFixture.dataRoot,
		conflictCheckpoint.Generation, conflictCheckpoint.OperationID)
	if err := conflictCheckpointStore.installExact(conflictCheckpoint); err != nil {
		t.Fatal(err)
	}
	conflictIntentStore, _ := newGatewayRebindProtectedIntentV2Store(conflictFixture.dataRoot,
		conflictIntent.Generation, conflictIntent.OperationID)
	if err := conflictIntentStore.installExact(conflictIntent); err != nil {
		t.Fatal(err)
	}
	conflictProof := proof
	conflictProof.OperationID = conflictIntent.OperationID
	conflictProof.Generation = conflictIntent.Generation
	conflictProof.ClaimRequestDigest = conflictIntent.Claim.RequestDigest
	conflictProof.PredecessorCheckpointDigest = conflictCheckpoint.Digest
	conflictProof.SourceStateDigest = conflictCheckpoint.SourceStateDigest
	conflictProof.SuccessorIdentityDigest = conflictIntent.Identity.Digest
	conflictProof.Digest, _ = gatewayRebindNoEffectAbortProofDigest(conflictProof)
	conflictReceipt, err := newGatewayRebindNoEffectAbortTerminalV2(conflictIntent.Claim, conflictIntent.Roster,
		conflictCheckpoint, conflictProof, time.Unix(5, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	conflictStore, _ := newGatewayRebindTerminalStoreV2(conflictFixture.dataRoot,
		conflictReceipt.Generation, conflictReceipt.OperationID)
	if err := conflictStore.installExact(context.Background(), conflictReceipt); err == nil {
		t.Fatal("no-intent terminal was installed beside a retained typed intent")
	}
	if _, err := conflictStore.load(); err == nil {
		t.Fatal("conflicting no-intent terminal file was created before refusal")
	}
}

func TestGatewayRebindTypedRollbackTerminalBindsAdoptedOwnedResource(t *testing.T) {
	fixture := newGatewayCurrentStateFixture(t)
	checkpoint, intent := gatewayRebindAttemptTypedFixture(t, fixture)
	checkpointStore, err := newGatewayRebindPredecessorCheckpointStore(fixture.dataRoot, checkpoint.Generation, checkpoint.OperationID)
	if err != nil || checkpointStore.installExact(checkpoint) != nil {
		t.Fatalf("install typed checkpoint: %v", err)
	}
	intentStore, err := newGatewayRebindProtectedIntentV2Store(fixture.dataRoot, intent.Generation, intent.OperationID)
	if err != nil || intentStore.installExact(intent) != nil {
		t.Fatalf("install typed intent: %v", err)
	}
	first, err := newGatewayRebindSuccessorIntentProgressV2(intent, time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	records, resources, _ := gatewayRebindTypedCompleteProgressFixture(t, fixture, intent, first)
	// Retain the forward prefix through handover intent. The final create then
	// succeeds without its sequence-fourteen acknowledgment, so rollback must
	// first adopt the exact configured container under that retained intent.
	forward := append([]gatewayRebindProgressRecord(nil), records[:13]...)
	for _, record := range forward {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed forward progress %d: %v", record.Sequence, storeErr)
		}
	}
	routesDigest, err := gatewayRebindTypedCheckpointRoutesDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	owned := gatewayRebindTypedRollbackOwnedFromEffect(*forward[len(forward)-1].TypedEffect)
	finalCopy := *resources.FinalContainer
	owned.FinalContainer = &finalCopy
	lastAt, err := parseGatewayRebindProgressTime(forward[len(forward)-1].OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	rollbackIntent, err := newGatewayRebindTypedRollbackIntentV2(intent, forward, owned, routesDigest,
		strings.Repeat("b", 64), lastAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	withoutAdoption := owned
	if _, err := newGatewayRebindTypedRollbackIntentV2(intent, forward, withoutAdoption, routesDigest, "",
		lastAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("unacknowledged final create was accepted without exact adoption proof")
	}
	beforeHandover := append([]gatewayRebindProgressRecord(nil), records[:12]...)
	if _, err := newGatewayRebindTypedRollbackIntentV2(intent, beforeHandover, owned, routesDigest,
		strings.Repeat("b", 64), lastAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("final container adoption was accepted before retained handover intent")
	}
	early := append([]gatewayRebindProgressRecord(nil), records[:2]...)
	futureOwned := gatewayRebindTypedRollbackOwnedFromEffect(*early[len(early)-1].TypedEffect)
	networkCopy, configCopy := resources.IngressNetwork, resources.ConfigVolume
	futureOwned.IngressNetwork, futureOwned.ConfigVolume = &networkCopy, &configCopy
	earlyAt, _ := parseGatewayRebindProgressTime(early[len(early)-1].OccurredAt)
	if _, err := newGatewayRebindTypedRollbackIntentV2(intent, early, futureOwned, routesDigest,
		strings.Repeat("c", 64), earlyAt.Add(time.Nanosecond)); err == nil {
		t.Fatal("network creation intent adopted an unauthorized future config volume")
	}
	firstAt, _ := parseGatewayRebindProgressTime(first.OccurredAt)
	postIntentRollback, err := newGatewayRebindTypedRollbackIntentV2(intent,
		[]gatewayRebindProgressRecord{first}, gatewayRebindTypedRollbackOwnedResources{}, routesDigest, "",
		firstAt.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("sequence-one post-intent rollback: %v", err)
	}
	postIntentProof := gatewayRebindTypedRollbackProofFixture(t, fixture, routesDigest, postIntentRollback.Digest)
	postIntentComplete, err := newGatewayRebindTypedRollbackCompleteV2(intent,
		[]gatewayRebindProgressRecord{first, postIntentRollback}, postIntentProof, firstAt.Add(2*time.Nanosecond))
	if err != nil {
		t.Fatalf("sequence-one rollback completion: %v", err)
	}
	postIntentTerminal, err := newGatewayRebindRollbackTerminalV2(intent, postIntentComplete, checkpoint,
		firstAt.Add(3*time.Nanosecond))
	postIntentProgress := []gatewayRebindProgressSelection{
		{Generation: first.Generation, Sequence: first.Sequence, Record: first},
		{Generation: postIntentRollback.Generation, Sequence: postIntentRollback.Sequence, Record: postIntentRollback},
		{Generation: postIntentComplete.Generation, Sequence: postIntentComplete.Sequence, Record: postIntentComplete},
	}
	if err != nil || !gatewayRebindTerminalV2MatchesIntentHistory(postIntentTerminal, intent, checkpoint, postIntentProgress) {
		t.Fatalf("sequence-one retained intent could not roll back safely: %v", err)
	}
	withRollbackIntent := append(append([]gatewayRebindProgressRecord(nil), forward...), rollbackIntent)
	proof := gatewayRebindTypedRollbackProofFixture(t, fixture, routesDigest, rollbackIntent.Digest)
	rollbackComplete, err := newGatewayRebindTypedRollbackCompleteV2(intent, withRollbackIntent, proof,
		lastAt.Add(2*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []gatewayRebindProgressRecord{rollbackIntent, rollbackComplete} {
		store, storeErr := newGatewayRebindProgressStore(fixture.dataRoot, record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil || store.installExact(context.Background(), record) != nil {
			t.Fatalf("install typed rollback progress %d: %v", record.Sequence, storeErr)
		}
	}
	all := append(withRollbackIntent, rollbackComplete)
	terminal, err := newGatewayRebindRollbackTerminalV2(intent, rollbackComplete, checkpoint,
		lastAt.Add(3*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	store, err := newGatewayRebindTerminalStoreV2(fixture.dataRoot, terminal.Generation, terminal.OperationID)
	if err != nil || store.installExact(context.Background(), terminal) != nil ||
		store.installExact(context.Background(), terminal) != nil {
		t.Fatalf("install/replay typed rollback terminal: %v", err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	typedProgress := make([]gatewayRebindProgressSelection, 0, len(all))
	for _, selection := range history.Progress {
		if selection.Record.OperationID == intent.OperationID {
			typedProgress = append(typedProgress, selection)
		}
	}
	if err != nil || len(history.TerminalsV2) != 1 || len(typedProgress) != len(all) ||
		!gatewayRebindTerminalV2MatchesIntentHistory(terminal, intent, checkpoint, typedProgress) {
		t.Fatalf("typed rollback history mismatch: records=%d terminals=%d error=%v",
			len(typedProgress), len(history.TerminalsV2), err)
	}
	retained, err := gatewayRebindRetainedOperationInspectionV2(terminal)
	if err != nil || retained.Disposition != appaccess.GatewayRebindDispositionAbort ||
		retained.Resources.FinalContainerID != finalCopy.ID || retained.Resources.FinalContainerOwnershipDigest != finalCopy.OwnershipDigest {
		t.Fatalf("typed rollback retained ownership mismatch: %#v error=%v", retained, err)
	}
	wrongRemoval := proof
	wrongRemoval.Observation.IngressNetworkPresent = true
	wrongRemoval.Observation.Digest, _ = gatewayRebindFinalHandoverObservationDigest(wrongRemoval.Observation)
	wrongRemoval.Digest, _ = gatewayRebindFinalHandoverOutcomeDigest(wrongRemoval)
	if _, err := newGatewayRebindTypedRollbackCompleteV2(intent, withRollbackIntent, wrongRemoval,
		lastAt.Add(2*time.Nanosecond)); err == nil {
		t.Fatal("rollback completion accepted retained successor network")
	}
	if _, err := newGatewayRebindRollbackTerminalV2(intent, rollbackComplete, checkpoint,
		lastAt.Add(2*time.Nanosecond)); err == nil {
		t.Fatal("rollback terminal accepted a non-increasing timestamp")
	}
}
