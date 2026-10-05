package appaccess

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestResolveNativeGatewayBindingFromCommittedUpgradeAuthority(t *testing.T) {
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	resolution, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.RawProfile != resolution.EffectiveProfile || len(resolution.TransferChain) != 0 ||
		resolution.TransferChainTipDigest != "" || resolution.TerminalReceiptDigest != "" ||
		resolution.CurrentGatewaySource.Kind != GatewayRebindSourceGatewayUpgrade ||
		resolution.CurrentGatewaySource.OperationID != fixture.claim.Spec.PredecessorUpgradeOperationID ||
		resolution.CurrentGatewaySource.ProfileRevisionID != fixture.profile.ID ||
		resolution.CurrentGatewaySource.ProfileRevisionNumber != fixture.profile.RevisionNumber ||
		resolution.CurrentGatewaySource.ProfileSpecDigest != fixture.profile.SpecDigest {
		t.Fatalf("native upgrade resolution=%#v", resolution)
	}
}

func TestResolveGatewayBindingSeparatesRawAndEffectiveProfiles(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	receipt := strings.Repeat("7", 64)
	commitGatewayRebindFixture(t, fixture, receipt)

	resolution, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.RawProfile.ID != fixture.profile.ID ||
		resolution.EffectiveProfile.ID != fixture.claim.Spec.SuccessorProfileRevisionID ||
		resolution.CurrentGatewaySource.Kind != GatewayRebindSourceGatewayRebind ||
		resolution.CurrentGatewaySource.OperationID != fixture.claim.Spec.OperationID ||
		resolution.CurrentGatewaySource.TerminalReceiptDigest != receipt ||
		resolution.TerminalReceiptDigest != receipt || len(resolution.TransferChain) != 1 ||
		resolution.TransferChainTipDigest != resolution.TransferChain[0].TransferDigest {
		t.Fatalf("transferred resolution=%#v", resolution)
	}
	authorization, err := fixture.repository.AuthorizeAppAccessGrant(context.Background(),
		AppAccessGrantAuthorizationInput{
			Owner:           AppAccessGrantClaimOwnerFor(fixture.grant),
			PermittedStates: []AppAccessGrantState{AppAccessGrantCommitted},
		})
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Profile.ID != resolution.RawProfile.ID ||
		authorization.EffectiveProfile.ID != resolution.EffectiveProfile.ID ||
		authorization.CurrentGatewaySource != resolution.CurrentGatewaySource ||
		len(authorization.TransferChain) != 1 ||
		authorization.TransferChain[0].TransferDigest != resolution.TransferChain[0].TransferDigest ||
		authorization.TransferChainTipDigest != resolution.TransferChainTipDigest ||
		authorization.TerminalReceiptDigest != resolution.TerminalReceiptDigest {
		t.Fatalf("transferred authorization=%#v", authorization)
	}

	nativeApp := addApps(t, fixture.db, 1)[0]
	allocation, _, err := fixture.repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: nativeApp, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID:     resolution.EffectiveProfile.ID,
		GatewayProfileRevisionNumber: resolution.EffectiveProfile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := fixture.repository.ApproveAppAccess(context.Background(), approvedAccessInput(t, allocation, 0))
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := fixture.repository.ClaimAppAccessGrant(context.Background(), ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, resolution.EffectiveProfile),
		ActorID: testAdministrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := fixture.repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	grant, _, err = fixture.repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, AppAccessGrantProof{
			GatewayOperationID:   fixture.claim.Spec.OperationID,
			ProtectedStateDigest: strings.Repeat("a", 64), ObservedAt: testNow.Add(10 * time.Second),
		})
	if err != nil {
		t.Fatal(err)
	}
	native, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: nativeApp, AllocationID: allocation.ID, AccessRevisionID: revision.ID,
		GrantAttemptID: grant.AttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if native.RawProfile != native.EffectiveProfile || len(native.TransferChain) != 0 ||
		native.TransferChainTipDigest != "" || native.TerminalReceiptDigest != receipt ||
		native.CurrentGatewaySource.OperationID != fixture.claim.Spec.OperationID {
		t.Fatalf("native resolution=%#v", native)
	}
	nativeAuthorization, err := fixture.repository.AuthorizeAppAccessGrant(context.Background(),
		AppAccessGrantAuthorizationInput{
			Owner:           AppAccessGrantClaimOwnerFor(grant),
			PermittedStates: []AppAccessGrantState{AppAccessGrantCommitted},
		})
	if err != nil {
		t.Fatal(err)
	}
	if nativeAuthorization.Profile != nativeAuthorization.EffectiveProfile ||
		nativeAuthorization.CurrentGatewaySource != native.CurrentGatewaySource ||
		len(nativeAuthorization.TransferChain) != 0 || nativeAuthorization.TransferChainTipDigest != "" ||
		nativeAuthorization.TerminalReceiptDigest != receipt {
		t.Fatalf("native authorization=%#v", nativeAuthorization)
	}
}

func TestResolveGatewayBindingWalksRepeatedRebindAcrossRuntimeAdvance(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	firstReceipt := strings.Repeat("7", 64)
	commitGatewayRebindFixture(t, fixture, firstReceipt)
	first, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	previousHead, err := generatedruntimestate.New(fixture.db).Active(context.Background(), fixture.entry.AppID)
	if err != nil {
		t.Fatal(err)
	}
	advancedHead := redeployRuntimeHeadForRebindTest(t, fixture.db, previousHead)
	finishGatewayRebindFixtureWork(t, fixture.db)

	proposal := gatewayRebindV2ProposalForCommittedFixture(t, fixture, first, advancedHead)
	claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || !created {
		t.Fatalf("claim=%#v created=%t error=%v", claim, created, err)
	}
	prepared, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Active == nil || prepared.Phase != GatewayRebindPrepared ||
		prepared.DatabaseCommittedEvent != nil || prepared.DatabaseCommitObserved || !prepared.RollbackAllowed ||
		prepared.CurrentSource == nil || prepared.CurrentSource.OperationID != fixture.claim.Spec.OperationID ||
		prepared.CurrentDatabaseCommittedEvent == nil ||
		prepared.CurrentDatabaseCommittedEvent.OperationID != fixture.claim.Spec.OperationID {
		t.Fatalf("prepared second rebind snapshot=%#v", prepared)
	}

	secondReceipt := strings.Repeat("9", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindSuccessorReady, secondReceipt, nil)); err != nil {
		t.Fatal(err)
	}
	predecessor := first.TransferChainTipDigest
	transfer := GatewayRebindAllocationTransfer{
		Version: GatewayRebindTransferVersionV1, OperationID: claim.Spec.OperationID,
		Ordinal: 1, AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		GrantAttemptID: fixture.entry.GrantAttemptID, SourceBindingDigest: strings.Repeat("5", 64),
		RosterEntryDigest:              proposal.Roster[0].EntryDigest,
		SourceProfileRevisionID:        first.EffectiveProfile.ID,
		SourceProfileRevisionNumber:    first.EffectiveProfile.RevisionNumber,
		SourceProfileSpecDigest:        first.EffectiveProfile.SpecDigest,
		PredecessorTransferDigest:      &predecessor,
		SuccessorProfileRevisionID:     claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileSpecDigest:     claim.ConfigureApproval.SpecDigest,
		TerminalReceiptDigest:          secondReceipt,
	}
	transfer.TransferDigest, err = GatewayRebindAllocationTransferDigest(transfer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForClaimV2(t, claim, GatewayRebindSuccessorReady, 2,
			GatewayRebindDatabaseCommitted, secondReceipt, []GatewayRebindAllocationTransfer{transfer})); err != nil {
		t.Fatal(err)
	}

	databaseCommitted, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(databaseCommitted.TransferChain) != 2 ||
		databaseCommitted.TransferChain[0].SourceBindingDigest == databaseCommitted.TransferChain[1].SourceBindingDigest ||
		databaseCommitted.TransferChain[1].PredecessorTransferDigest == nil ||
		*databaseCommitted.TransferChain[1].PredecessorTransferDigest != databaseCommitted.TransferChain[0].TransferDigest ||
		databaseCommitted.TransferChainTipDigest != transfer.TransferDigest ||
		databaseCommitted.CurrentGatewaySource.OperationID != claim.Spec.OperationID ||
		databaseCommitted.TerminalReceiptDigest != secondReceipt {
		t.Fatalf("database committed resolution=%#v", databaseCommitted)
	}

	committed := gatewayRebindProofForClaimV2(t, claim, GatewayRebindDatabaseCommitted, 3,
		GatewayRebindCommitted, secondReceipt, nil)
	committed.LocalAttestationDigest = strings.Repeat("8", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), committed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active != nil || len(snapshot.History) != 2 || snapshot.CurrentSource == nil ||
		snapshot.CurrentSource.OperationID != claim.Spec.OperationID ||
		snapshot.CurrentDatabaseCommittedEvent == nil ||
		snapshot.CurrentDatabaseCommittedEvent.OperationID != claim.Spec.OperationID ||
		len(snapshot.CurrentTransfers) != 1 || snapshot.CurrentTransfers[0].TransferDigest != transfer.TransferDigest {
		t.Fatalf("committed second rebind snapshot=%#v", snapshot)
	}
}

func gatewayRebindV2ProposalForCommittedFixture(t *testing.T, fixture gatewayRebindFixture,
	current GatewayBindingResolution, head generatedruntimestate.ActiveHead,
) GatewayRebindPreclaimProposalV2 {
	t.Helper()
	operationID := uuid.NewString()
	predecessor := current.TransferChainTipDigest
	entry := GatewayRebindRosterEntryV2{
		Version: GatewayRebindRosterVersionV2, OperationID: operationID, Ordinal: 1,
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID, Port: fixture.entry.Port,
		AllocationOwnerOperationID: fixture.entry.AllocationOwnerOperationID,
		AllocationState:            fixture.entry.AllocationState, AccessRevisionID: fixture.entry.AccessRevisionID,
		AccessRevisionNumber: fixture.entry.AccessRevisionNumber, AccessSpecDigest: fixture.entry.AccessSpecDigest,
		GrantAttemptID: fixture.entry.GrantAttemptID, GrantStateSequence: fixture.grant.StateSequence,
		GrantProtectedStateDigest:   fixture.grant.Proof.ProtectedStateDigest,
		SourceProfileRevisionID:     current.EffectiveProfile.ID,
		SourceProfileRevisionNumber: current.EffectiveProfile.RevisionNumber,
		SourceProfileSpecDigest:     current.EffectiveProfile.SpecDigest,
		PredecessorTransferDigest:   &predecessor,
		ServingDeploymentID:         head.DeploymentID, ServingReleaseID: head.ReleaseID,
		ServingSlot: head.Slot, RouteGeneration: head.Generation,
	}
	var err error
	entry.EntryDigest, err = GatewayRebindRosterEntryV2Digest(entry)
	if err != nil {
		t.Fatal(err)
	}
	roster := []GatewayRebindRosterEntryV2{entry}
	rosterDigest, err := GatewayRebindRosterV2Digest(roster)
	if err != nil {
		t.Fatal(err)
	}
	successor := GatewayProfileSpec{
		SelectedIPv4: "192.168.98.8", InterfaceID: "rebind-second-successor",
		PortStart: current.EffectiveProfile.Spec.PortStart, PortEnd: current.EffectiveProfile.Spec.PortEnd,
	}
	spec := GatewayRebindSpecV2{
		Version: GatewayRebindSpecVersionV2, OperationID: operationID,
		Predecessor: GatewayRebindSourceRef{
			Lineage: GatewayCurrentLineageRef{
				Kind: GatewayRebindSourceGatewayRebind, OperationID: current.CurrentGatewaySource.OperationID,
				ProfileRevisionID:     current.EffectiveProfile.ID,
				ProfileRevisionNumber: current.EffectiveProfile.RevisionNumber,
				ProfileSpecDigest:     current.EffectiveProfile.SpecDigest,
				ProtectedGeneration:   1, ProtectedIdentityDigest: strings.Repeat("a", 64),
				ProtectedIntentDigest: strings.Repeat("b", 64),
				TerminalReceiptDigest: current.TerminalReceiptDigest,
			},
			SourceStateVersion: 1, SourceStateRevision: 2,
			SourceStateDigest: strings.Repeat("c", 64), PredecessorCheckpointDigest: strings.Repeat("d", 64),
		},
		SuccessorProfileRevisionID:     uuid.NewString(),
		SuccessorProfileRevisionNumber: current.EffectiveProfile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: successor,
		RosterVersion: GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: 1,
	}
	profileDigest, err := GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	specDigest, err := GatewayRebindSpecV2Digest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return GatewayRebindPreclaimProposalV2{
		Spec: spec, Roster: roster,
		RebindApproval:    Approval{Action: ActionRebindGateway, SpecDigest: specDigest, ActorID: testAdministrator},
		ConfigureApproval: Approval{Action: ActionConfigureGateway, SpecDigest: profileDigest, ActorID: testAdministrator},
	}
}

func gatewayRebindProofForClaimV2(t *testing.T, claim GatewayRebindClaimV2,
	previous GatewayRebindState, sequence int64, next GatewayRebindState, receipt string,
	transfers []GatewayRebindAllocationTransfer,
) GatewayRebindTransitionProof {
	t.Helper()
	expected := claim.Spec.Predecessor.Lineage
	expectedID, expectedNumber, expectedDigest := expected.ProfileRevisionID,
		expected.ProfileRevisionNumber, expected.ProfileSpecDigest
	if previous == GatewayRebindDatabaseCommitted || previous == GatewayRebindUnresolved {
		expectedID, expectedNumber, expectedDigest = claim.Spec.SuccessorProfileRevisionID,
			claim.Spec.SuccessorProfileRevisionNumber, claim.ConfigureApproval.SpecDigest
	}
	proof := GatewayRebindTransitionProof{
		Version: GatewayRebindTransitionVersionV1, Purpose: GatewayRebindTransitionPurpose,
		OperationID: claim.Spec.OperationID, ClaimRequestDigest: claim.RequestDigest,
		ClaimSpecDigest: claim.RebindApproval.SpecDigest,
		ExpectedState:   previous, ExpectedSequence: sequence, NextState: next,
		ExpectedHeadRevisionID: expectedID, ExpectedHeadRevisionNumber: expectedNumber,
		ExpectedHeadSpecDigest: expectedDigest, ProtectedGeneration: expected.ProtectedGeneration + 1,
		ProtectedPhase: string(next), ProtectedRecordSequence: uint64(sequence),
		ProtectedRecordDigest: strings.Repeat("1", 64), TerminalReceiptDigest: receipt,
		TerminalDisposition:         GatewayRebindDispositionCommit,
		PredecessorCheckpointDigest: claim.Spec.Predecessor.PredecessorCheckpointDigest,
		SourceStateVersion:          claim.Spec.Predecessor.SourceStateVersion,
		SourceStateRevision:         claim.Spec.Predecessor.SourceStateRevision,
		SourceStateDigest:           claim.Spec.Predecessor.SourceStateDigest, Transfers: transfers,
	}
	if next == GatewayRebindDatabaseCommitted {
		proof.SuccessorOperationalStateVersion = 1
		proof.SuccessorOperationalStateRevision = 1
		proof.SuccessorOperationalStateDigest = strings.Repeat("4", 64)
		proof.TransferManifestDigest, _ = GatewayRebindTransferManifestDigest(transfers)
	}
	return proof
}

func commitGatewayRebindFixture(t *testing.T, fixture gatewayRebindFixture, receipt string) {
	t.Helper()
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForFixture(t, fixture, GatewayRebindPrepared, 1,
			GatewayRebindSuccessorReady, receipt, nil)); err != nil {
		t.Fatal(err)
	}
	transfer := gatewayRebindTransferForFixture(t, fixture, receipt)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForFixture(t, fixture, GatewayRebindSuccessorReady, 2,
			GatewayRebindDatabaseCommitted, receipt, []GatewayRebindAllocationTransfer{transfer})); err != nil {
		t.Fatal(err)
	}
	committed := gatewayRebindProofForFixture(t, fixture, GatewayRebindDatabaseCommitted, 3,
		GatewayRebindCommitted, receipt, nil)
	committed.LocalAttestationDigest = strings.Repeat("8", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), committed); err != nil {
		t.Fatal(err)
	}
}
