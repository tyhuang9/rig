package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

// These sequences use real SQLite transitions and protected records. Only
// Docker effects and observations are simulated; they do not prove traffic
// withdrawal, container ownership, or restart behavior on a Docker host.
func TestGatewayRebindCoordinatorCommitsRepeatedTransferChain(t *testing.T) {
	ctx := context.Background()
	f, firstInput, templateDriver := newGatewayRebindCoordinatorFixture(t)
	firstDriver := gatewayRebindSequencePhysicalDriver(t, templateDriver.template, 1)
	entry := firstInput.Inspection.Roster[0]
	ref := appaccess.GatewayBindingRef{AppID: entry.AppID, AllocationID: entry.AllocationID,
		AccessRevisionID: entry.AccessRevisionID, GrantAttemptID: entry.GrantAttemptID}
	original, err := f.repository.ResolveGatewayBinding(ctx, ref)
	if err != nil || len(original.TransferChain) != 0 {
		t.Fatalf("original binding: chain=%d error=%v", len(original.TransferChain), err)
	}
	firstResult, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, firstInput, firstDriver)
	if err != nil || firstResult.FinalPhase != appaccess.GatewayRebindCommitted || !firstResult.FenceReleased {
		t.Fatalf("first commit: %+v error=%v", firstResult, err)
	}
	firstSnapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || len(firstSnapshot.History) != 1 || len(firstSnapshot.CurrentTransfers) != 1 {
		t.Fatalf("first SQL history: entries=%d transfers=%d error=%v", len(firstSnapshot.History), len(firstSnapshot.CurrentTransfers), err)
	}
	firstSelection, err := f.manager.selectGatewayCurrentLocked(ctx, firstSnapshot)
	if err != nil || firstSelection.State == nil || firstSelection.Store == nil || firstSelection.Terminal == nil {
		t.Fatalf("first protected selection: %v", err)
	}
	firstFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	secondProfile := firstInput.Inspection.Spec.SuccessorProfile
	secondProfile.SelectedIPv4, secondProfile.InterfaceID = "192.168.98.8", "rebind-next-successor"
	observe := f.manager.gatewayRebindV2NetworkObserver
	f.manager.gatewayRebindV2NetworkObserver = func(ctx context.Context, claim appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
		value, err := observe(ctx, claim)
		// The second synthetic host inventory must actually include the newly
		// approved address; reusing the first inventory correctly rolls back.
		value.Candidates = []gatewayRebindSuccessorNetworkCandidate{{InterfaceID: secondProfile.InterfaceID,
			IPv4: secondProfile.SelectedIPv4, Prefix: "192.168.98.0/24"}}
		value.HostInterfaces = append(append([]string(nil), value.HostInterfaces...), "192.168.98.0/24")
		sort.Strings(value.HostInterfaces)
		return value, err
	}
	secondInput := gatewayRebindSequenceNextInput(t, f, firstInput.Inspection.Spec.SuccessorProfileRevisionNumber+1, secondProfile)
	prior := firstSnapshot.CurrentTransfers[0]
	secondEntry := secondInput.Inspection.Roster[0]
	if secondInput.Inspection.ProtectedGeneration <= firstInput.Inspection.ProtectedGeneration ||
		secondInput.Inspection.Spec.Predecessor.Lineage != firstSelection.State.Lineage ||
		secondInput.Inspection.Spec.Predecessor.SourceStateDigest != firstSelection.State.Digest ||
		secondEntry.SourceProfileRevisionID != original.RawProfile.ID ||
		secondEntry.PredecessorTransferDigest == nil || *secondEntry.PredecessorTransferDigest != prior.TransferDigest {
		t.Fatal("second approval lost the raw source, selected state, or exact prior transfer")
	}
	secondDriver := gatewayRebindSequencePhysicalDriver(t, templateDriver.template, 2)
	secondResult, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, secondInput, secondDriver)
	if err != nil || secondResult.FinalPhase != appaccess.GatewayRebindCommitted || !secondResult.FenceReleased {
		t.Fatalf("second commit: %+v error=%v", secondResult, err)
	}
	secondSnapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || secondSnapshot.Active != nil || secondSnapshot.Phase != "" ||
		len(secondSnapshot.History) != 2 || !reflect.DeepEqual(secondSnapshot.History[0], firstSnapshot.History[0]) ||
		secondSnapshot.CurrentSource == nil || *secondSnapshot.CurrentSource != secondResult.SelectedCurrentAuthority ||
		secondSnapshot.CurrentDatabaseCommittedEvent == nil ||
		secondSnapshot.CurrentDatabaseCommittedEvent.OperationID != secondResult.OperationID {
		t.Fatalf("second SQL current/history mismatch: %v", err)
	}
	resolved, err := f.repository.ResolveGatewayBinding(ctx, ref)
	if err != nil || len(resolved.TransferChain) != 2 || !reflect.DeepEqual(resolved.TransferChain[0], prior) ||
		resolved.TransferChain[1].PredecessorTransferDigest == nil ||
		*resolved.TransferChain[1].PredecessorTransferDigest != prior.TransferDigest ||
		resolved.EffectiveProfile.ID != secondInput.Inspection.Spec.SuccessorProfileRevisionID ||
		resolved.CurrentGatewaySource != secondResult.SelectedCurrentAuthority ||
		resolved.TerminalReceiptDigest != secondResult.TerminalReceiptDigest ||
		resolved.TransferChainTipDigest != resolved.TransferChain[1].TransferDigest {
		t.Fatalf("second transfer-chain resolution: chain=%d error=%v", len(resolved.TransferChain), err)
	}
	if !reflect.DeepEqual(resolved.RawAllocation, original.RawAllocation) ||
		!reflect.DeepEqual(resolved.RawAccessRevision, original.RawAccessRevision) ||
		!reflect.DeepEqual(resolved.RawGrant, original.RawGrant) || !reflect.DeepEqual(resolved.RawProfile, original.RawProfile) {
		t.Fatal("repeated commit rewrote original allocation, revision, grant, or profile")
	}
	secondSelection, err := f.manager.selectGatewayCurrentLocked(ctx, secondSnapshot)
	if err != nil || secondSelection.State == nil || secondSelection.Terminal == nil ||
		secondSelection.State.Apps[entry.AppID].LAN == nil ||
		secondSelection.State.Apps[entry.AppID].LAN.Raw != *f.state.Apps[entry.AppID].LAN ||
		!reflect.DeepEqual(secondSelection.State.Apps[entry.AppID].LAN.Transfer, &resolved.TransferChain[1]) {
		t.Fatalf("second protected route lost raw binding or transfer: %v", err)
	}
	retained, err := firstSelection.Store.load()
	if err != nil || !reflect.DeepEqual(retained, *firstSelection.State) {
		t.Fatalf("first generation route bundle changed: %v", err)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, firstFiles)
	current, err := f.manager.InspectGatewayRebindCurrent(ctx, f.repository)
	if err != nil || !current.FenceReleased || current.ActiveOperationID != "" || len(current.Retained) != 2 ||
		current.SelectedCurrentAuthority != secondResult.SelectedCurrentAuthority ||
		current.CurrentStateDigest != secondSelection.State.Digest || f.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("repeated current inspection: %+v error=%v", current, err)
	}
	// Distinct simulated identities matter: reusing a template's container ID
	// would make retained withdrawal ownership ambiguous after the second commit.
	targets, err := f.manager.gatewayCurrentOwnedStopTargetsProtectedLocked()
	if err != nil || len(targets) != 2 || targets[0].FinalContainer.ID == targets[1].FinalContainer.ID {
		t.Fatalf("retained stop ownership: targets=%d error=%v", len(targets), err)
	}
	startup, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || len(startup.Grants.Claims) != 1 || len(startup.Grants.Claims[0].TransferChain) != 2 ||
		startup.Grants.Claims[0].EffectiveProfile.ID != resolved.EffectiveProfile.ID ||
		!reflect.DeepEqual(startup.Grants.Claims[0].Claim, original.RawGrant) {
		t.Fatalf("repeated startup census: %v", err)
	}
	if firstDriver.commitCalls != 1 || secondDriver.commitCalls != 1 ||
		firstDriver.attestCalls != 1 || secondDriver.attestCalls != 1 || firstDriver.abortCalls+secondDriver.abortCalls != 0 {
		t.Fatal("unexpected simulated effect or attestation counts")
	}
}

func TestGatewayRebindCoordinatorCommitsAfterRetainedRollback(t *testing.T) {
	for _, afterIntent := range []bool{false, true} {
		t.Run(fmt.Sprintf("post-intent-%t", afterIntent), func(t *testing.T) {
			ctx := context.Background()
			f, firstInput, templateDriver := newGatewayRebindCoordinatorFixture(t)
			firstDriver := gatewayRebindSequencePhysicalDriver(t, templateDriver.template, 1)
			observe := f.manager.gatewayRebindV2NetworkObserver
			if afterIntent {
				firstDriver.rollback = true
			} else {
				f.manager.gatewayRebindV2NetworkObserver = func(context.Context, appaccess.GatewayRebindClaimV2) (gatewayRebindSuccessorNetworkObservation, error) {
					return gatewayRebindSuccessorNetworkObservation{}, errors.New("test-only network admission refusal")
				}
			}
			aborted, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, firstInput, firstDriver)
			if err != nil || aborted.FinalPhase != appaccess.GatewayRebindRolledBack || !aborted.FenceReleased {
				t.Fatalf("first rollback: %+v error=%v", aborted, err)
			}
			before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || len(before.History) != 1 || before.Active != nil || before.CurrentSource == nil ||
				before.CurrentSource.OperationID != f.journal.OperationID || len(before.CurrentTransfers) != 0 {
				t.Fatalf("rollback did not preserve native current: %v", err)
			}
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			f.manager.gatewayRebindV2NetworkObserver = observe
			next := gatewayRebindSequenceNextInput(t, f, firstInput.Inspection.Spec.SuccessorProfileRevisionNumber,
				firstInput.Inspection.Spec.SuccessorProfile)
			if next.Inspection.ProtectedGeneration <= firstInput.Inspection.ProtectedGeneration ||
				next.Inspection.Spec.Predecessor.Lineage != firstInput.Inspection.Spec.Predecessor.Lineage ||
				next.Inspection.Spec.Predecessor.SourceStateDigest != firstInput.Inspection.Spec.Predecessor.SourceStateDigest ||
				next.Inspection.Roster[0].PredecessorTransferDigest != nil {
				t.Fatal("retry reused consumed generation or inherited an aborted transfer")
			}
			nextDriver := gatewayRebindSequencePhysicalDriver(t, templateDriver.template, 2)
			committed, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, next, nextDriver)
			if err != nil || committed.FinalPhase != appaccess.GatewayRebindCommitted || !committed.FenceReleased {
				t.Fatalf("commit after retained rollback: %+v error=%v", committed, err)
			}
			after, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || after.Active != nil || len(after.History) != 2 ||
				!reflect.DeepEqual(after.History[0], before.History[0]) || len(after.CurrentTransfers) != 1 ||
				after.CurrentTransfers[0].PredecessorTransferDigest != nil || after.CurrentSource == nil ||
				*after.CurrentSource != committed.SelectedCurrentAuthority {
				t.Fatalf("commit rewrote abort or inherited its authority: %v", err)
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
			current, err := f.manager.InspectGatewayRebindCurrent(ctx, f.repository)
			if err != nil || !current.FenceReleased || len(current.Retained) != 2 ||
				current.Retained[0].Disposition != appaccess.GatewayRebindDispositionAbort ||
				current.Retained[1].Disposition != appaccess.GatewayRebindDispositionCommit ||
				f.manager.gatewayRebindAdmissionBlocked() {
				t.Fatalf("post-rollback current inspection: %+v error=%v", current, err)
			}
		})
	}
}

func gatewayRebindSequenceNextInput(t *testing.T, f gatewayRebindPredecessorFixture,
	revision int64, profile appaccess.GatewayProfileSpec,
) gatewayRebindCommitInput {
	t.Helper()
	inspection, err := f.manager.InspectGatewayRebindProposal(context.Background(), f.repository, GatewayRebindProposalInput{
		OperationID: "81818181-8181-4181-8181-818181818181", SuccessorProfileRevisionID: "82828282-8282-4282-8282-828282828282",
		SuccessorProfileOperationID: "83838383-8383-4383-8383-838383838383", SuccessorProfileRevisionNumber: revision,
		SuccessorProfile: profile})
	if err != nil || len(inspection.Roster) != 1 {
		t.Fatalf("next proposal: roster=%d error=%v", len(inspection.Roster), err)
	}
	return gatewayRebindCommitInput{Inspection: inspection,
		RebindApproval: appaccess.Approval{Action: appaccess.ActionRebindGateway,
			SpecDigest: inspection.SpecDigest, ActorID: gatewayRebindTestAdministrator},
		ConfigureApproval: appaccess.Approval{Action: appaccess.ActionConfigureGateway,
			SpecDigest: inspection.SuccessorProfileSpecDigest, ActorID: gatewayRebindTestAdministrator}}
}

func gatewayRebindSequencePhysicalDriver(t *testing.T, template gatewayCurrentStateFixture, ordinal int) *gatewayRebindBoundedPhysicalDriver {
	t.Helper()
	id := func(kind string) string {
		digest, err := canonicalDigest(fmt.Sprintf("test-only/rebind-sequence/%d/%s", ordinal, kind))
		if err != nil {
			t.Fatal(err)
		}
		return "sha256:" + digest
	}
	resources := &template.receipt.Resources
	resources.IngressNetwork.ID = id("ingress-network")
	resources.StageContainer.ID = id("stage-container")
	final := *resources.FinalContainer
	final.ID = id("final-container")
	resources.FinalContainer = &final
	resources.ConfigVolume.Mountpoint = fmt.Sprintf("/var/lib/docker/volumes/rebind-sequence-%d-config/_data", ordinal)
	resources.DataVolume.Mountpoint = fmt.Sprintf("/var/lib/docker/volumes/rebind-sequence-%d-data/_data", ordinal)
	return &gatewayRebindBoundedPhysicalDriver{t: t, template: template}
}

func gatewayRebindSequenceRequireRetainedFiles(t *testing.T, manager *Manager, before gatewayHistorySnapshot) {
	t.Helper()
	after, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	retained := gatewayHistorySnapshot{files: make(map[string]gatewayHistoryFileFingerprint, len(before.files))}
	for name := range before.files {
		if value, ok := after.files[name]; ok {
			retained.files[name] = value
		}
	}
	if !sameGatewayHistorySnapshot(before, retained) {
		t.Fatal("a later operation replaced, deleted, or rewrote retained protected history")
	}
}
