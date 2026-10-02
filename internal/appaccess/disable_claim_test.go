package appaccess

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAppAccessDisableClaimCommitsExactReleaseAfterProof(t *testing.T) {
	db, repository, revision, profile, grantInput := approvedGrantFixture(t)
	grant, _, err := repository.ClaimAppAccessGrant(context.Background(), grantInput)
	if err != nil {
		t.Fatal(err)
	}
	grantOwner := AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), grantOwner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), grantOwner,
		AppAccessGrantApplying, AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(), grantOwner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, AppAccessGrantProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('a'),
		}); err != nil {
		t.Fatal(err)
	}

	input := approvedDisableInput(t, revision)
	claim, created, err := repository.ClaimAppAccessDisable(context.Background(), input)
	if err != nil || !created || claim.State != AppAccessDisablePrepared ||
		claim.SourceGrantAttemptID != grant.AttemptID {
		t.Fatalf("disable claim=%#v created=%t error=%v", claim, created, err)
	}
	replayed, created, err := New(db).ClaimAppAccessDisable(context.Background(), input)
	if err != nil || created || replayed.OperationID != claim.OperationID {
		t.Fatalf("disable replay=%#v created=%t error=%v", replayed, created, err)
	}
	changed := input
	changed.ExpectedRevisionNumber++
	if _, _, err := repository.ClaimAppAccessDisable(context.Background(), changed); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed replay error=%v", err)
	}

	owner := AppAccessDisableClaimOwnerFor(claim)
	wrongOwner := owner
	wrongOwner.AllocationID = uuid.NewString()
	if _, _, err := repository.AdvanceAppAccessDisableClaim(context.Background(), wrongOwner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner error=%v", err)
	}
	viewer := owner
	viewer.ActorID = testViewer
	if _, _, err := repository.AdvanceAppAccessDisableClaim(context.Background(), viewer,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("viewer transition error=%v", err)
	}
	withdrawing, changedState, err := repository.AdvanceAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing)
	if err != nil || !changedState || withdrawing.State != AppAccessDisableWithdrawing {
		t.Fatalf("withdrawing=%#v changed=%t error=%v", withdrawing, changedState, err)
	}
	authorized, err := repository.AuthorizeAppAccessDisable(context.Background(), AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []AppAccessDisableState{AppAccessDisableWithdrawing},
	})
	if err != nil || authorized.SourceGrant == nil || authorized.SourceGrant.AttemptID != grant.AttemptID ||
		authorized.Profile.ID != profile.ID {
		t.Fatalf("withdrawing authorization=%#v error=%v", authorized, err)
	}

	otherApp := addApps(t, db, 1)[0]
	if allocation, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: otherApp, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	}); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("port became available before proof: allocation=%#v error=%v", allocation, err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET disabled_at=? WHERE id=?`,
		formatTime(testNow), revision.Allocation.ID); err == nil {
		t.Fatal("SQL bypass released allocation before disable commit")
	}
	if _, err := db.Exec(`UPDATE lan_app_access_disable_claims
		SET state='committed',state_sequence=3,updated_at=? WHERE operation_id=?`,
		formatTime(testNow), claim.OperationID); err == nil {
		t.Fatal("SQL bypass committed disable without proof")
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET retired_at=?,retired_by_disable_operation_id=? WHERE attempt_id=?`,
		formatTime(testNow), claim.OperationID, grant.AttemptID); err == nil {
		t.Fatal("SQL bypass retired source grant before disable commit")
	}

	proof := AppAccessDisableProof{GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('b')}
	committed, changedState, err := repository.ResolveAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, proof)
	if err != nil || !changedState || committed.State != AppAccessDisableCommitted || committed.Proof == nil {
		t.Fatalf("committed=%#v changed=%t error=%v", committed, changedState, err)
	}
	if _, changedState, err := repository.ResolveAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, proof); err != nil || changedState {
		t.Fatalf("commit replay changed=%t error=%v", changedState, err)
	}
	authorized, err = repository.AuthorizeAppAccessDisable(context.Background(), AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []AppAccessDisableState{AppAccessDisableCommitted},
	})
	if err != nil || authorized.Allocation.ReleasedAt == nil || authorized.SourceGrant == nil ||
		authorized.SourceGrant.RetiredAt == nil ||
		authorized.SourceGrant.RetiredByDisableOperationID != claim.OperationID {
		t.Fatalf("committed authorization=%#v error=%v", authorized, err)
	}
	startup, err := repository.AppAccessDisableStartupSnapshot(context.Background())
	if err != nil || len(startup.Claims) != 1 || startup.Claims[0].SourceGrant == nil ||
		startup.Claims[0].SourceGrant.RetiredAt == nil || startup.Claims[0].Allocation.ReleasedAt == nil {
		t.Fatalf("disable startup=%#v error=%v", startup, err)
	}
	combined, err := repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(combined.Disables.Claims) != 1 ||
		combined.Disables.Claims[0].Claim.OperationID != claim.OperationID {
		t.Fatalf("combined startup=%#v error=%v", combined, err)
	}
	if _, err := repository.CurrentAppAccess(context.Background(), revision.AppID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("released head remained LAN current: %v", err)
	}
	allocation, created, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: revision.AppID, OperationID: uuid.NewString(), ExpectedRevisionNumber: revision.RevisionNumber,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil || !created || allocation.Port != revision.Allocation.Port {
		t.Fatalf("post-proof port allocation=%#v created=%t error=%v", allocation, created, err)
	}
	newRevision, created, err := repository.ApproveAppAccess(context.Background(),
		approvedAccessInput(t, allocation, revision.RevisionNumber))
	if err != nil || !created || newRevision.RevisionNumber != revision.RevisionNumber+1 {
		t.Fatalf("deliberate re-enable revision=%#v created=%t error=%v", newRevision, created, err)
	}
	historical, err := repository.AuthorizeAppAccessDisable(context.Background(), AppAccessDisableAuthorizationInput{
		Owner: owner, PermittedStates: []AppAccessDisableState{AppAccessDisableCommitted},
	})
	if err != nil || historical.SourceGrant == nil ||
		historical.SourceGrant.AttemptID != grant.AttemptID {
		t.Fatalf("historical committed authorization=%#v error=%v", historical, err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_disable_claim_events SET state='uncertain'
		WHERE operation_id=? AND sequence=1`, claim.OperationID); err == nil {
		t.Fatal("disable event history was mutable")
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_disable_claim_events WHERE operation_id=?`, claim.OperationID); err == nil {
		t.Fatal("disable event history was deletable")
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_disable_claims WHERE operation_id=?`, claim.OperationID); err == nil {
		t.Fatal("disable claim was deletable")
	}
}

func TestAppAccessDisableClaimSQLRequiresExistingSourceGrant(t *testing.T) {
	db, repository, revision, _, grantInput := approvedGrantFixture(t)
	grant, _, err := repository.ClaimAppAccessGrant(context.Background(), grantInput)
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, AppAccessGrantProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('a'),
		}); err != nil {
		t.Fatal(err)
	}

	input := approvedDisableInput(t, revision)
	spec := AppAccessDisableSpecFor(revision)
	requestDigest, err := disableApprovalRequestDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	if _, err := db.Exec(`INSERT INTO lan_app_access_disable_intents(
		operation_id,app_id,request_digest,approval_action,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.OperationID, spec.AppID, requestDigest, ActionDisableAppAccess,
		spec.AllocationID, spec.OwnerOperationID, spec.AccessRevisionID,
		spec.AccessRevisionNumber, spec.Port, spec.GatewayProfileRevisionID,
		spec.GatewayProfileRevisionNumber, input.Approval.SpecDigest,
		input.Approval.ActorID, stamp); err != nil {
		t.Fatal(err)
	}
	insertClaim := `INSERT INTO lan_app_access_disable_claims(
		operation_id,request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at,source_grant_attempt_id,
		created_at,state,state_sequence,updated_at
	) SELECT operation_id,request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at,?,approved_at,'prepared',1,approved_at
	FROM lan_app_access_disable_intents WHERE operation_id=?`
	if _, err := db.Exec(insertClaim, nil, input.OperationID); err == nil ||
		!strings.Contains(err.Error(), "source grant") {
		t.Fatalf("omitted committed source grant was accepted or rejected for another reason: %v", err)
	}
	if _, err := db.Exec(insertClaim, grant.AttemptID, input.OperationID); err != nil {
		t.Fatalf("exact committed source grant was rejected: %v", err)
	}
}

func TestAppAccessDisableRecoveryUsesCurrentAdministratorAfterApproverDemotion(t *testing.T) {
	db, repository, revision, _, _ := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
	if err != nil {
		t.Fatal(err)
	}
	recoveryActor := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'disable-recovery-admin','hash','administrator',?,?)`, recoveryActor,
		formatTime(testNow), formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, claim.ApprovedBy); err != nil {
		t.Fatal(err)
	}
	original := AppAccessDisableClaimOwnerFor(claim)
	if _, err := repository.AuthorizeAppAccessDisable(context.Background(), AppAccessDisableAuthorizationInput{
		Owner: original, PermittedStates: []AppAccessDisableState{AppAccessDisablePrepared},
	}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("demoted approver remained authorized: %v", err)
	}
	recoveryOwner := original
	recoveryOwner.ActorID = recoveryActor
	if _, err := repository.AuthorizeAppAccessDisable(context.Background(), AppAccessDisableAuthorizationInput{
		Owner: recoveryOwner, PermittedStates: []AppAccessDisableState{AppAccessDisablePrepared},
	}); err != nil {
		t.Fatalf("current administrator could not reconstruct original approval: %v", err)
	}
	if _, _, err := repository.AdvanceAppAccessDisableClaim(context.Background(), recoveryOwner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); err != nil {
		t.Fatalf("current administrator could not enter withdrawal: %v", err)
	}
	committed, _, err := repository.ResolveAppAccessDisableClaim(context.Background(), recoveryOwner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, AppAccessDisableProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('d'),
		})
	if err != nil || committed.State != AppAccessDisableCommitted || committed.ApprovedBy != claim.ApprovedBy {
		t.Fatalf("replacement administrator recovery=%#v err=%v", committed, err)
	}
}

func TestAppAccessDisableMutationSerializesAcrossClaims(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.77.8", InterfaceID: "disable-serialization", PortStart: 8100, PortEnd: 8101}, 0))
	if err != nil {
		t.Fatal(err)
	}
	apps := addApps(t, db, 2)
	var firstClaim AppAccessDisableClaim
	var secondRevision AppAccessRevision
	for index, appID := range apps {
		allocation, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
			AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
		})
		if err != nil {
			t.Fatal(err)
		}
		revision, _, err := repository.ApproveAppAccess(context.Background(), approvedAccessInput(t, allocation, 0))
		if err != nil {
			t.Fatal(err)
		}
		claim, _, err := repository.ClaimAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
		if index == 0 {
			if err != nil {
				t.Fatal(err)
			}
			firstClaim = claim
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("competing prepared disable error=%v", err)
		} else {
			secondRevision = revision
		}
	}
	grantInput := ClaimAppAccessGrantInput{AttemptID: uuid.NewString(),
		Spec: AppAccessGrantSpecFor(secondRevision, profile), ActorID: secondRevision.ApprovedBy}
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), grantInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("grant was allowed during prepared disable: %v", err)
	}
	firstOwner := AppAccessDisableClaimOwnerFor(firstClaim)
	if _, _, err := repository.AdvanceAppAccessDisableClaim(context.Background(), firstOwner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessDisableClaim(context.Background(), firstOwner,
		AppAccessDisableWithdrawing, AppAccessDisableUncertain); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), grantInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("grant was allowed during uncertain disable: %v", err)
	}
}

func testDigest(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
