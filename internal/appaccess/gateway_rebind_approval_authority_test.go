package appaccess

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func gatewayRebindDistinctApprovalFixture(t *testing.T, db *sql.DB) (gatewayRebindFixture, GatewayRebindClaimV2) {
	t.Helper()
	f := newGatewayRebindFixtureOnDBWithClaim(t, db, false, false)
	finishGatewayRebindFixtureWork(t, db)
	proposal := gatewayRebindV2ProposalForFixture(t, f)
	for _, approval := range []*Approval{&proposal.RebindApproval, &proposal.ConfigureApproval} {
		approval.ActorID = uuid.NewString()
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
			VALUES(?,?,'hash','administrator',datetime('now'),datetime('now'))`, approval.ActorID, approval.ActorID); err != nil {
			t.Fatal(err)
		}
	}
	claim, created, err := f.repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || !created {
		t.Fatalf("distinct approval claim: created=%t err=%v", created, err)
	}
	return f, claim
}

func TestHostingGatewayActiveRebindApprovalsPreserveRevokedOwnership(t *testing.T) {
	f, claim := gatewayRebindDistinctApprovalFixture(t, appAccessDB(t))
	ctx := context.Background()
	before, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !before.ActiveRebindApprovalsAuthorizeServing() {
		t.Fatalf("approved snapshot: %v", err)
	}
	want := GatewayRebindActiveApprovalAuthority{OperationID: claim.Spec.OperationID,
		SpecVersion: GatewayRebindSpecVersionV2, RebindActorID: claim.RebindApproval.ActorID,
		ConfigureActorID: claim.ConfigureApproval.ActorID, RebindApproverIsAdministrator: true,
		ConfigureApproverIsAdministrator: true}
	if before.ActiveRebindApprovalAuthority == nil || *before.ActiveRebindApprovalAuthority != want {
		t.Fatalf("wrong approval binding: %+v", before.ActiveRebindApprovalAuthority)
	}
	for _, actor := range []string{claim.RebindApproval.ActorID, claim.ConfigureApproval.ActorID} {
		t.Run(actor, func(t *testing.T) {
			if _, err := f.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, actor); err != nil {
				t.Fatal(err)
			}
			after, err := f.repository.HostingGatewayStartupSnapshot(ctx)
			if err != nil || after.ActiveRebindApprovalAuthority == nil || after.ActiveRebindApprovalsAuthorizeServing() {
				t.Fatalf("revocation hidden or serving authorized: %v", err)
			}
			if after.ActiveRebindApprovalAuthority.RebindApproverIsAdministrator != (actor != claim.RebindApproval.ActorID) ||
				after.ActiveRebindApprovalAuthority.ConfigureApproverIsAdministrator != (actor != claim.ConfigureApproval.ActorID) {
				t.Fatal("role projection crossed independent approvers")
			}
			// The only changed authority is the projected role. LAN/profile
			// approvals, history, runtime and the active SQL fence remain intact.
			after.ActiveRebindApprovalAuthority = before.ActiveRebindApprovalAuthority
			if !reflect.DeepEqual(before, after) {
				t.Fatal("demotion changed retained ownership or unrelated authority")
			}
			recovery, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || !reflect.DeepEqual(recovery, before.Rebind) {
				t.Fatalf("revoked ownership became unreadable: %v", err)
			}
			assertGatewayRebindFenceActive(t, f.repository)
			if _, err := f.db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, actor); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostingGatewayActiveRebindApprovalsShareReadTransaction(t *testing.T) {
	root := t.TempDir()
	reader, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	addUsers(t, reader)
	f, claim := gatewayRebindDistinctApprovalFixture(t, reader)
	writer, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	called := false
	f.repository.afterGrantStartupClaimsRead = func() {
		f.repository.afterGrantStartupClaimsRead = nil
		called = true
		if _, err := writer.Exec(`UPDATE users SET role='viewer' WHERE id=?`, claim.ConfigureApproval.ActorID); err != nil {
			t.Fatal(err)
		}
	}
	before, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || !called || !before.ActiveRebindApprovalsAuthorizeServing() {
		t.Fatalf("snapshot mixed old ownership and new role: hook=%t err=%v", called, err)
	}
	after, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || after.ActiveRebindApprovalsAuthorizeServing() || after.ActiveRebindApprovalAuthority == nil ||
		after.ActiveRebindApprovalAuthority.ConfigureApproverIsAdministrator || !reflect.DeepEqual(before.Rebind, after.Rebind) {
		t.Fatalf("fresh snapshot missed revocation: %v", err)
	}
}

func TestHostingGatewayActiveRebindApprovalsRejectCrossedProjection(t *testing.T) {
	f, _ := gatewayRebindDistinctApprovalFixture(t, appAccessDB(t))
	snapshot, err := f.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HostingGatewayStartupSnapshot){
		"missing projection": func(s *HostingGatewayStartupSnapshot) { s.ActiveRebindApprovalAuthority = nil },
		"wrong operation":    func(s *HostingGatewayStartupSnapshot) { s.ActiveRebindApprovalAuthority.OperationID = uuid.NewString() },
		"wrong version":      func(s *HostingGatewayStartupSnapshot) { s.ActiveRebindApprovalAuthority.SpecVersion = 1 },
		"crossed actors": func(s *HostingGatewayStartupSnapshot) {
			s.ActiveRebindApprovalAuthority.RebindActorID = s.ActiveRebindApprovalAuthority.ConfigureActorID
		},
		"revoked rebind": func(s *HostingGatewayStartupSnapshot) {
			s.ActiveRebindApprovalAuthority.RebindApproverIsAdministrator = false
		},
		"revoked configure": func(s *HostingGatewayStartupSnapshot) {
			s.ActiveRebindApprovalAuthority.ConfigureApproverIsAdministrator = false
		},
		"dual union":                func(s *HostingGatewayStartupSnapshot) { s.Rebind.Active.Claim.Legacy = &GatewayRebindClaim{} },
		"missing union":             func(s *HostingGatewayStartupSnapshot) { s.Rebind.Active.Claim.V2 = nil },
		"wrong union version":       func(s *HostingGatewayStartupSnapshot) { s.Rebind.Active.Claim.SpecVersion = 1 },
		"no active with projection": func(s *HostingGatewayStartupSnapshot) { s.Rebind.Active = nil },
	} {
		t.Run(name, func(t *testing.T) {
			value := snapshot
			active, authority := *snapshot.Rebind.Active, *snapshot.ActiveRebindApprovalAuthority
			value.Rebind.Active, value.ActiveRebindApprovalAuthority = &active, &authority
			mutate(&value)
			if value.ActiveRebindApprovalsAuthorizeServing() {
				t.Fatal("crossed authority accepted")
			}
		})
	}
	if !(HostingGatewayStartupSnapshot{}).ActiveRebindApprovalsAuthorizeServing() {
		t.Fatal("no active claim incorrectly requires approvals")
	}
	legacy := newGatewayRebindFixture(t, true)
	legacySnapshot, err := legacy.repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || !legacySnapshot.ActiveRebindApprovalsAuthorizeServing() || legacySnapshot.ActiveRebindApprovalAuthority.SpecVersion != 1 {
		t.Fatalf("legacy authority rejected: %v", err)
	}
}
