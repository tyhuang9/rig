package appaccess

import (
	"context"
	"errors"
	"reflect"
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
	startup, err := fixture.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var nativeStartup *AppAccessGrantStartupClaim
	for index := range startup.Grants.Claims {
		if startup.Grants.Claims[index].Claim.AttemptID == grant.AttemptID {
			nativeStartup = &startup.Grants.Claims[index]
			break
		}
	}
	if nativeStartup == nil || nativeStartup.Profile != nativeStartup.EffectiveProfile ||
		nativeStartup.CurrentGatewaySource != native.CurrentGatewaySource ||
		len(nativeStartup.TransferChain) != 0 || nativeStartup.TransferChainTipDigest != "" ||
		nativeStartup.TerminalReceiptDigest != receipt || !nativeStartup.ProfileHeadCurrent {
		t.Fatalf("native startup=%#v", nativeStartup)
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
	claim, _ := commitNextGatewayRebindForTest(t, fixture, first)
	startup, err := fixture.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(startup.Upgrades.Claims) != 1 || len(startup.Grants.Claims) != 1 ||
		startup.Grants.Claims[0].EffectiveProfile.ID != claim.Spec.SuccessorProfileRevisionID ||
		!startup.Grants.Claims[0].ProfileHeadCurrent {
		t.Fatalf("startup after repeated rebind=%#v error=%v", startup, err)
	}

	if _, err := fixture.db.Exec(`DROP TRIGGER lan_gateway_rebind_allocation_transfer_retain`); err != nil {
		t.Fatal(err)
	}
	if result, err := fixture.db.Exec(`DELETE FROM lan_gateway_rebind_allocation_transfers
		WHERE operation_id=? AND allocation_id=?`, fixture.claim.Spec.OperationID, fixture.entry.AllocationID); err != nil {
		t.Fatal(err)
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("deleted intermediate transfer rows=%d", affected)
	}
	if _, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	}); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("resolver accepted stored transfer gap: %v", err)
	}
	if _, err := fixture.repository.AuthorizeAppAccessGrant(context.Background(),
		AppAccessGrantAuthorizationInput{Owner: AppAccessGrantClaimOwnerFor(fixture.grant),
			PermittedStates: []AppAccessGrantState{AppAccessGrantCommitted}}); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("authorization accepted stored transfer gap: %v", err)
	}
	rawAllocation, err := readAllocationByID(context.Background(), fixture.db, fixture.entry.AllocationID)
	if err != nil {
		t.Fatal(err)
	}
	rawGrant, err := readAppAccessGrantClaim(context.Background(), fixture.db, fixture.grant.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	rawProfile, _, err := readGatewayRevision(context.Background(), fixture.db,
		first.RawProfile.ID, first.RawProfile.RevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rawAllocation, first.RawAllocation) ||
		!reflect.DeepEqual(rawGrant, first.RawGrant) || !reflect.DeepEqual(rawProfile, first.RawProfile) {
		t.Fatalf("transfer-gap refusal mutated raw facts: allocation=%#v grant=%#v profile=%#v",
			rawAllocation, rawGrant, rawProfile)
	}
}

func TestDisableAuthorizationAndStartupUseCompleteRepeatedRebindChain(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	commitGatewayRebindFixture(t, fixture, strings.Repeat("7", 64))
	first, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, current := commitNextGatewayRebindForTest(t, fixture, first)
	disable, created, err := fixture.repository.ClaimAppAccessDisable(context.Background(),
		approvedDisableInput(t, current.RawAccessRevision))
	if err != nil || !created {
		t.Fatalf("disable=%#v created=%t error=%v", disable, created, err)
	}
	authorization, err := fixture.repository.AuthorizeAppAccessDisable(context.Background(),
		AppAccessDisableAuthorizationInput{Owner: AppAccessDisableClaimOwnerFor(disable),
			PermittedStates: []AppAccessDisableState{AppAccessDisablePrepared}})
	if err != nil || authorization.EffectiveProfile.ID != current.EffectiveProfile.ID ||
		authorization.CurrentGatewaySource != current.CurrentGatewaySource ||
		len(authorization.TransferChain) != 2 ||
		authorization.TransferChainTipDigest != current.TransferChainTipDigest ||
		authorization.TerminalReceiptDigest != current.TerminalReceiptDigest {
		t.Fatalf("repeated-chain disable authorization=%#v error=%v", authorization, err)
	}
	startup, err := fixture.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(startup.Disables.Claims) != 1 ||
		startup.Disables.Claims[0].EffectiveProfile.ID != current.EffectiveProfile.ID ||
		startup.Disables.Claims[0].CurrentGatewaySource != current.CurrentGatewaySource ||
		len(startup.Disables.Claims[0].TransferChain) != 2 ||
		startup.Disables.Claims[0].TransferChainTipDigest != current.TransferChainTipDigest ||
		startup.Disables.Claims[0].TerminalReceiptDigest != current.TerminalReceiptDigest ||
		!startup.Disables.Claims[0].ProfileHeadCurrent {
		t.Fatalf("repeated-chain disable startup=%#v error=%v", startup.Disables, err)
	}
}

func commitNextGatewayRebindForTest(t *testing.T, fixture gatewayRebindFixture,
	first GatewayBindingResolution,
) (GatewayRebindClaimV2, GatewayBindingResolution) {
	t.Helper()
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
		SourceProfileRevisionID:        first.RawProfile.ID,
		SourceProfileRevisionNumber:    first.RawProfile.RevisionNumber,
		SourceProfileSpecDigest:        first.RawProfile.SpecDigest,
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
	current, err := fixture.repository.ResolveGatewayBinding(context.Background(), GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(current.TransferChain) != 2 ||
		current.TransferChain[0].SourceBindingDigest == current.TransferChain[1].SourceBindingDigest ||
		current.TransferChain[0].SourceProfileRevisionID != first.RawProfile.ID ||
		current.TransferChain[1].SourceProfileRevisionID != first.RawProfile.ID ||
		current.TransferChain[0].SourceProfileRevisionNumber != first.RawProfile.RevisionNumber ||
		current.TransferChain[1].SourceProfileRevisionNumber != first.RawProfile.RevisionNumber ||
		current.TransferChain[0].SourceProfileSpecDigest != first.RawProfile.SpecDigest ||
		current.TransferChain[1].SourceProfileSpecDigest != first.RawProfile.SpecDigest ||
		current.TransferChain[1].PredecessorTransferDigest == nil ||
		*current.TransferChain[1].PredecessorTransferDigest != current.TransferChain[0].TransferDigest ||
		current.TransferChainTipDigest != transfer.TransferDigest ||
		current.CurrentGatewaySource.OperationID != claim.Spec.OperationID ||
		current.TerminalReceiptDigest != secondReceipt {
		t.Fatalf("database committed resolution=%#v", current)
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
	return claim, current
}

func TestRetiredGatewayBindingRemainsHistoricalAfterLaterRebind(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	firstReceipt := strings.Repeat("7", 64)
	commitGatewayRebindFixture(t, fixture, firstReceipt)
	ref := GatewayBindingRef{
		AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		AccessRevisionID: fixture.entry.AccessRevisionID, GrantAttemptID: fixture.entry.GrantAttemptID,
	}
	first, err := fixture.repository.ResolveGatewayBinding(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}

	disableInput := approvedDisableInput(t, first.RawAccessRevision)
	disable, created, err := fixture.repository.ClaimAppAccessDisable(context.Background(), disableInput)
	if err != nil || !created {
		t.Fatalf("claim disable=%#v created=%t error=%v", disable, created, err)
	}
	disableOwner := AppAccessDisableClaimOwnerFor(disable)
	if _, _, err := fixture.repository.AdvanceAppAccessDisableClaim(context.Background(), disableOwner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); err != nil {
		t.Fatal(err)
	}
	disable, changed, err := fixture.repository.ResolveAppAccessDisableClaim(context.Background(), disableOwner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, AppAccessDisableProof{
			GatewayOperationID: fixture.claim.Spec.OperationID, ProtectedStateDigest: strings.Repeat("6", 64),
		})
	if err != nil || !changed || disable.Proof == nil {
		t.Fatalf("commit disable=%#v changed=%t error=%v", disable, changed, err)
	}

	previousHead, err := generatedruntimestate.New(fixture.db).Active(context.Background(), fixture.entry.AppID)
	if err != nil {
		t.Fatal(err)
	}
	advancedHead := redeployRuntimeHeadForRebindTest(t, fixture.db, previousHead)
	finishGatewayRebindFixtureWork(t, fixture.db)
	proposal := gatewayRebindV2ProposalForCommittedFixture(t, fixture, first, advancedHead)
	proposal.Roster = nil
	proposal.Spec.RosterCount = 0
	proposal.Spec.RosterDigest, err = GatewayRebindRosterV2Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RebindApproval.SpecDigest, err = GatewayRebindSpecV2Digest(proposal.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || !created {
		t.Fatalf("second claim=%#v created=%t error=%v", claim, created, err)
	}
	secondReceipt := strings.Repeat("9", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindSuccessorReady, secondReceipt, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
		gatewayRebindProofForClaimV2(t, claim, GatewayRebindSuccessorReady, 2,
			GatewayRebindDatabaseCommitted, secondReceipt, nil)); err != nil {
		t.Fatal(err)
	}
	committed := gatewayRebindProofForClaimV2(t, claim, GatewayRebindDatabaseCommitted, 3,
		GatewayRebindCommitted, secondReceipt, nil)
	committed.LocalAttestationDigest = strings.Repeat("8", 64)
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), committed); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.repository.ResolveGatewayBinding(context.Background(), ref); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("released binding unexpectedly resolved to selected successor: %v", err)
	}
	startup, err := fixture.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(startup.Upgrades.Claims) != 1 || len(startup.Grants.Claims) != 1 ||
		len(startup.Disables.Claims) != 1 {
		t.Fatalf("historical startup=%#v error=%v", startup, err)
	}
	grantHistory := startup.Grants.Claims[0]
	disableHistory := startup.Disables.Claims[0]
	if grantHistory.Claim.AttemptID != fixture.grant.AttemptID || grantHistory.EffectiveProfile.ID != "" ||
		grantHistory.CurrentGatewaySource.OperationID != "" || grantHistory.ProfileHeadCurrent ||
		grantHistory.RetainedEffectiveProfile.ID != first.EffectiveProfile.ID ||
		grantHistory.RetainedGatewaySource.OperationID != fixture.claim.Spec.OperationID ||
		len(grantHistory.RetainedTransferChain) != 1 ||
		grantHistory.RetainedTransferChainTipDigest != first.TransferChainTipDigest ||
		grantHistory.RetainedTerminalReceiptDigest != firstReceipt {
		t.Fatalf("retired grant history=%#v", grantHistory)
	}
	if disableHistory.Claim.OperationID != disable.OperationID || disableHistory.EffectiveProfile.ID != "" ||
		disableHistory.CurrentGatewaySource.OperationID != "" || disableHistory.ProfileHeadCurrent ||
		disableHistory.RetainedEffectiveProfile.ID != first.EffectiveProfile.ID ||
		disableHistory.RetainedGatewaySource.OperationID != fixture.claim.Spec.OperationID ||
		len(disableHistory.RetainedTransferChain) != 1 ||
		disableHistory.RetainedTransferChainTipDigest != first.TransferChainTipDigest ||
		disableHistory.RetainedTerminalReceiptDigest != firstReceipt {
		t.Fatalf("retired disable history=%#v", disableHistory)
	}
	authorized, err := fixture.repository.AuthorizeAppAccessDisable(context.Background(),
		AppAccessDisableAuthorizationInput{Owner: disableOwner,
			PermittedStates: []AppAccessDisableState{AppAccessDisableCommitted}})
	if err != nil || authorized.EffectiveProfile.ID != "" ||
		authorized.CurrentGatewaySource.OperationID != "" ||
		authorized.RetainedGatewaySource.OperationID != fixture.claim.Spec.OperationID ||
		authorized.RetainedEffectiveProfile.ID != first.EffectiveProfile.ID ||
		len(authorized.RetainedTransferChain) != 1 ||
		authorized.RetainedTransferChainTipDigest != first.TransferChainTipDigest ||
		authorized.RetainedTerminalReceiptDigest != firstReceipt {
		t.Fatalf("historical disable authorization=%#v error=%v", authorized, err)
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
		SourceProfileRevisionID:     current.RawProfile.ID,
		SourceProfileRevisionNumber: current.RawProfile.RevisionNumber,
		SourceProfileSpecDigest:     current.RawProfile.SpecDigest,
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
		SuccessorProtectedGeneration:   2,
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
		ExpectedHeadSpecDigest: expectedDigest, ProtectedGeneration: claim.Spec.SuccessorProtectedGeneration,
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
