package appaccess

import (
	"context"
	"strings"
	"testing"
)

func TestClaimGatewayRebindV2PersistsExactPreparedClaimAndRoster(t *testing.T) {
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	finishGatewayRebindFixtureWork(t, fixture.db)
	proposal := gatewayRebindV2ProposalForFixture(t, fixture)
	claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || !created {
		t.Fatalf("claim=%#v created=%t error=%v", claim, created, err)
	}
	if claim.Spec != proposal.Spec || claim.State != GatewayRebindPrepared || claim.StateSequence != 1 ||
		claim.RequestDigest == "" || claim.SuccessorProfileRequestDigest == "" {
		t.Fatalf("stored claim=%#v", claim)
	}
	replayed, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || created || replayed != claim {
		t.Fatalf("replay=%#v created=%t error=%v", replayed, created, err)
	}

	storedRoster, err := readGatewayRebindRosterV2(context.Background(), fixture.db, proposal.Spec.OperationID)
	if err != nil || len(storedRoster) != 1 || storedRoster[0].EntryDigest != proposal.Roster[0].EntryDigest {
		t.Fatalf("stored roster=%#v error=%v", storedRoster, err)
	}
}

func TestClaimGatewayRebindV2RejectsIncompleteAndStaleRosterAtomically(t *testing.T) {
	t.Run("incomplete", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		finishGatewayRebindFixtureWork(t, fixture.db)
		proposal := gatewayRebindV2ProposalForFixture(t, fixture)
		proposal.Roster = nil
		proposal.Spec.RosterCount = 0
		proposal.Spec.RosterDigest, _ = GatewayRebindRosterV2Digest(nil)
		proposal.RebindApproval.SpecDigest, _ = GatewayRebindSpecV2Digest(proposal.Spec)
		if _, _, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal); err == nil {
			t.Fatal("incomplete roster was claimed")
		}
		assertNoGatewayRebindClaim(t, fixture, proposal.Spec.OperationID)
	})

	t.Run("stale runtime head", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		finishGatewayRebindFixtureWork(t, fixture.db)
		proposal := gatewayRebindV2ProposalForFixture(t, fixture)
		proposal.Roster[0].RouteGeneration++
		proposal.Roster[0].EntryDigest, _ = GatewayRebindRosterEntryV2Digest(proposal.Roster[0])
		proposal.Spec.RosterDigest, _ = GatewayRebindRosterV2Digest(proposal.Roster)
		proposal.RebindApproval.SpecDigest, _ = GatewayRebindSpecV2Digest(proposal.Spec)
		if _, _, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal); err == nil {
			t.Fatal("stale runtime roster was claimed")
		}
		assertNoGatewayRebindClaim(t, fixture, proposal.Spec.OperationID)
	})
}

func gatewayRebindV2ProposalForFixture(t *testing.T,
	fixture gatewayRebindFixture,
) GatewayRebindPreclaimProposalV2 {
	t.Helper()
	entry := GatewayRebindRosterEntryV2{
		Version: GatewayRebindRosterVersionV2, OperationID: fixture.claim.Spec.OperationID,
		Ordinal: fixture.entry.Ordinal, AppID: fixture.entry.AppID,
		AllocationID: fixture.entry.AllocationID, Port: fixture.entry.Port,
		AllocationOwnerOperationID: fixture.entry.AllocationOwnerOperationID,
		AllocationState:            fixture.entry.AllocationState, AccessRevisionID: fixture.entry.AccessRevisionID,
		AccessRevisionNumber: fixture.entry.AccessRevisionNumber, AccessSpecDigest: fixture.entry.AccessSpecDigest,
		GrantAttemptID: fixture.entry.GrantAttemptID, GrantStateSequence: fixture.entry.GrantStateSequence,
		GrantProtectedStateDigest: fixture.entry.GrantProtectedStateDigest,
		SourceProfileRevisionID:   fixture.profile.ID, SourceProfileRevisionNumber: fixture.profile.RevisionNumber,
		SourceProfileSpecDigest: fixture.profile.SpecDigest,
		ServingDeploymentID:     fixture.entry.ServingDeploymentID,
		ServingReleaseID:        fixture.entry.ServingReleaseID, ServingSlot: fixture.entry.ServingSlot,
		RouteGeneration: fixture.entry.RouteGeneration,
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
	spec := GatewayRebindSpecV2{
		Version: GatewayRebindSpecVersionV2, OperationID: fixture.claim.Spec.OperationID,
		Predecessor: GatewayRebindSourceRef{
			Lineage: GatewayCurrentLineageRef{
				Kind:              GatewayRebindSourceGatewayUpgrade,
				OperationID:       fixture.claim.Spec.PredecessorUpgradeOperationID,
				ProfileRevisionID: fixture.profile.ID, ProfileRevisionNumber: fixture.profile.RevisionNumber,
				ProfileSpecDigest: fixture.profile.SpecDigest, ProtectedGeneration: 7,
				ProtectedIdentityDigest: strings.Repeat("b", 64),
				ProtectedJournalDigest:  strings.Repeat("c", 64),
			},
			SourceStateVersion: 2, SourceStateRevision: 0,
			SourceStateDigest:           strings.Repeat("d", 64),
			PredecessorCheckpointDigest: strings.Repeat("e", 64),
		},
		SuccessorProfileRevisionID:     fixture.claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: fixture.claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    fixture.claim.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               fixture.claim.Spec.SuccessorProfile,
		RosterVersion:                  GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: 1,
	}
	specDigest, err := GatewayRebindSpecV2Digest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return GatewayRebindPreclaimProposalV2{
		Spec: spec, Roster: roster,
		RebindApproval:    Approval{Action: ActionRebindGateway, SpecDigest: specDigest, ActorID: testAdministrator},
		ConfigureApproval: fixture.claim.ConfigureApproval,
	}
}

func assertNoGatewayRebindClaim(t *testing.T, fixture gatewayRebindFixture, operationID string) {
	t.Helper()
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims WHERE operation_id=?`,
		operationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("claim count=%d error=%v", count, err)
	}
}
