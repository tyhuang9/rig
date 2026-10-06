package generatedingress

import (
	"context"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

// Real SQLite and protected history exercise the composition between a typed
// rebind commit, disable authorization, terminal release and clear ack. The
// batch installation and physical withdrawal are fixture setup; no Docker or
// controller HTTP acceptance is inferred from this test.
func TestGatewayCurrentLANRecoveryFinalizationCommitsRealSQLAfterRebind(t *testing.T) {
	gatewayCurrentLANRecoveryCompletedSQLFixture(t)
}

func gatewayCurrentLANRecoveryCompletedSQLFixture(t *testing.T) gatewayRebindPredecessorFixture {
	t.Helper()
	ctx := context.Background()
	f, input, physical := newGatewayRebindCoordinatorFixture(t)
	committed, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, physical)
	if err != nil || committed.FinalPhase != appaccess.GatewayRebindCommitted {
		t.Fatalf("rebind fixture: %v", err)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.manager.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil || selection.State == nil {
		t.Fatalf("select typed current: %v", err)
	}
	entry := input.Inspection.Roster[0]
	selection = gatewayCurrentServingRestoreRedeployFixture(t, f, selection, entry)
	resolution, err := f.repository.ResolveGatewayBinding(ctx, appaccess.GatewayBindingRef{
		AppID: entry.AppID, AllocationID: entry.AllocationID,
		AccessRevisionID: entry.AccessRevisionID, GrantAttemptID: entry.GrantAttemptID})
	if err != nil || len(resolution.TransferChain) != 1 {
		t.Fatalf("resolve transferred grant: %v", err)
	}
	revision := resolution.RawAccessRevision
	specDigest, err := appaccess.AppAccessDisableSpecDigest(appaccess.AppAccessDisableSpecFor(revision))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := f.repository.ClaimAppAccessDisable(ctx, appaccess.ApproveAppAccessDisableInput{
		OperationID: "67676767-6767-4767-8767-676767676767", ExpectedRevisionNumber: revision.RevisionNumber,
		Owner: appaccess.AllocationOwner{AllocationID: revision.Allocation.ID, AppID: revision.AppID,
			OperationID: revision.OperationID, AccessRevisionID: revision.ID},
		Approval: appaccess.Approval{Action: appaccess.ActionDisableAppAccess, SpecDigest: specDigest,
			ActorID: gatewayRebindTestAdministrator}})
	if err != nil {
		t.Fatalf("claim disable after rebind: %v", err)
	}
	owner := appaccess.AppAccessDisableClaimOwnerFor(claim)
	authorized, err := f.repository.AuthorizeAppAccessDisable(ctx, appaccess.AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{appaccess.AppAccessDisablePrepared}})
	if err != nil || authorized.SourceGrant == nil || authorized.SourceGrant.AttemptID != entry.GrantAttemptID {
		t.Fatalf("authorize transferred disable: %v", err)
	}
	rawRequest := gatewayV2LANGrantRequestForBinding(entry.AppID, selection.State.Apps[entry.AppID].LAN.Raw)
	request := GatewayV2LANDisableRequest{OperationID: claim.OperationID, RequestDigest: claim.RequestDigest,
		SpecDigest: claim.SpecDigest, AppID: claim.Spec.AppID, AllocationID: claim.Spec.AllocationID,
		OwnerOperationID: claim.Spec.OwnerOperationID, Port: claim.Spec.Port, AccessRevisionID: claim.Spec.AccessRevisionID,
		AccessRevisionNumber: claim.Spec.AccessRevisionNumber, AccessSpecDigest: authorized.Revision.SpecDigest,
		ApprovedBy: claim.ApprovedBy, GatewayProfileRevisionID: claim.Spec.GatewayProfileRevisionID,
		GatewayProfileRevisionNumber: claim.Spec.GatewayProfileRevisionNumber,
		GatewayProfileSpecDigest:     authorized.Profile.SpecDigest, SourceGrant: &rawRequest}
	grants := gatewayCurrentStartupGrants(t, *selection.State)
	if len(grants) != 1 || grants[0].Request != rawRequest {
		t.Fatal("fixture requires the exact transferred source grant")
	}
	grants[0].DisableIntentOperationID = claim.OperationID
	claims, err := validateGatewayV2LANAccessStartupClaims(grants, []GatewayV2LANDisableStartupClaim{{
		Request: request, State: claim.State, StateSequence: claim.StateSequence, CurrentBinding: grants[0].CurrentBinding}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := gatewayCurrentLANRecoveryInstallState(*selection.State, claims)
	if err != nil || !gatewayCurrentLANRecoveryCensusMatchesHead(batch, claims, snapshot.CurrentTransfers) ||
		selection.Store.saveNext(*selection.State, batch) != nil {
		t.Fatalf("install exact approved recovery head fixture: %v", err)
	}
	files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	driver := newGatewayCurrentLANBatchRetirementDriver(t)
	f.manager.gatewayCurrentPhysicalDriver = driver
	var ack appaccess.AppAccessDisableProtectedClearAck
	if err := f.manager.WithGatewayV2LANRecoveryDisableFinalization(ctx, request,
		func(ctx context.Context, observation GatewayV2LANDisableObservation) error {
			if _, _, err := f.repository.AdvanceAppAccessDisableClaim(ctx, owner,
				appaccess.AppAccessDisablePrepared, appaccess.AppAccessDisableWithdrawing); err != nil {
				return err
			}
			if _, err := f.repository.AuthorizeAppAccessDisable(ctx, appaccess.AppAccessDisableAuthorizationInput{
				Owner: owner, PermittedStates: []appaccess.AppAccessDisableState{appaccess.AppAccessDisableWithdrawing}}); err != nil {
				return err
			}
			if _, _, err := f.repository.ResolveAppAccessDisableClaim(ctx, owner,
				appaccess.AppAccessDisableWithdrawing, appaccess.AppAccessDisableCommitted, appaccess.AppAccessDisableProof{
					GatewayOperationID: observation.GatewayOperationID, ProtectedStateDigest: observation.ProtectedStateDigest,
					ObservedAt: observation.ObservedAt}); err != nil {
				return err
			}
			confirmed, err := f.repository.AppAccessDisableClaim(ctx, claim.OperationID)
			if err != nil || confirmed.State != appaccess.AppAccessDisableCommitted || confirmed.Proof == nil ||
				confirmed.Proof.GatewayOperationID != observation.GatewayOperationID ||
				confirmed.Proof.ProtectedStateDigest != observation.ProtectedStateDigest {
				return appaccess.ErrInvalidStoredState
			}
			return nil
		}, func(ctx context.Context, observation GatewayV2LANDisableObservation) error {
			var err error
			ack, _, err = f.repository.AcknowledgeAppAccessDisableProtectedClear(ctx,
				claim.OperationID, observation.GatewayOperationID, observation.ProtectedStateDigest, observation.ObservedAt)
			return err
		}); err != nil {
		t.Fatalf("real SQL disable finalization: %v", err)
	}
	installed, err := selection.Store.load()
	if err != nil || installed.LANRecovery.Head != 1 || installed.Revision != batch.Revision+2 ||
		installed.Apps[entry.AppID].LAN != nil || ack.OperationID != claim.OperationID ||
		ack.GatewayOperationID != committed.OperationID {
		t.Fatal("real SQL completion did not preserve exact protected progress")
	}
	disables, err := f.repository.AppAccessDisableStartupSnapshot(ctx)
	if err != nil || len(disables.Claims) != 1 || disables.Claims[0].ProtectedClearAck == nil ||
		!reflect.DeepEqual(*disables.Claims[0].ProtectedClearAck, ack) ||
		!reflect.DeepEqual(disables.Claims[0].RetainedTransferChain, resolution.TransferChain) ||
		disables.Claims[0].RetainedGatewaySource != resolution.CurrentGatewaySource ||
		disables.Claims[0].Allocation.ReleasedAt == nil {
		t.Fatalf("terminal SQL reader lost clear acknowledgment or historical transfer: %v", err)
	}
	grant, err := f.repository.AppAccessGrantClaim(ctx, entry.GrantAttemptID)
	if err != nil || grant.RetiredByDisableOperationID != claim.OperationID ||
		!reflect.DeepEqual(grant.Spec, resolution.RawGrant.Spec) || grant.RequestDigest != resolution.RawGrant.RequestDigest ||
		!reflect.DeepEqual(grant.Proof, resolution.RawGrant.Proof) {
		t.Fatal("disable did not retire the exact source or changed its historical approval/proof")
	}
	after, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, after) || len(driver.ownedStops) != 0 {
		t.Fatal("disable rewrote rebind history/current authority or required compensation")
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, files)
	return f
}
