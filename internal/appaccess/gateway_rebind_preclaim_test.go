package appaccess

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func gatewayRebindPreclaimProposalFor(fixture gatewayRebindFixture) GatewayRebindPreclaimProposal {
	return GatewayRebindPreclaimProposal{
		Spec: fixture.claim.Spec, RebindApproval: fixture.claim.RebindApproval,
		ConfigureApproval: fixture.claim.ConfigureApproval,
		Roster:            []GatewayRebindRosterEntry{fixture.entry},
	}
}

func TestGatewayRebindPreclaimSnapshotValidatesUnpersistedProposal(t *testing.T) {
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	proposal := gatewayRebindPreclaimProposalFor(fixture)
	first, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), proposal)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable preclaim snapshot: %v", err)
	}
	if first.CurrentProfile != fixture.profile || first.PredecessorClaim.OperationID != proposal.Spec.PredecessorUpgradeOperationID ||
		first.PredecessorClaim.State != GatewayProfileUpgradeCommitted ||
		first.ProposedClaim.RequestDigest != fixture.claim.RequestDigest ||
		first.ProposedClaim.SuccessorProfileRequestDigest != fixture.claim.SuccessorProfileRequestDigest ||
		len(first.Proposal.Roster) != 1 || first.Proposal.Roster[0] != fixture.entry ||
		len(first.GrantBindings) != 1 || first.GrantBindings[0].AttemptID != fixture.grant.AttemptID {
		t.Fatalf("incomplete preclaim snapshot: %#v", first)
	}
	var claims, events, roster int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claim_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_roster_entries`).Scan(&roster); err != nil {
		t.Fatal(err)
	}
	if claims != 0 || events != 0 || roster != 0 {
		t.Fatalf("preclaim wrote durable state: claims=%d events=%d roster=%d", claims, events, roster)
	}
}

func TestGatewayRebindPreclaimSnapshotAcceptsEmptyRoster(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	ctx := context.Background()
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.96.8", InterfaceID: "empty-predecessor", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}
	owner := gatewayUpgradeClaimOwner(upgrade)
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner,
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner,
		GatewayProfileUpgradeServing, GatewayProfileUpgradeCommitted); err != nil {
		t.Fatal(err)
	}
	rosterDigest, err := GatewayRebindRosterDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	successor := GatewayProfileSpec{SelectedIPv4: "192.168.97.8", InterfaceID: "empty-successor", PortStart: 8100, PortEnd: 8119}
	spec := GatewayRebindSpec{
		OperationID:                  uuid.NewString(),
		PredecessorProfileRevisionID: profile.ID, PredecessorProfileRevisionNumber: profile.RevisionNumber,
		PredecessorProfileSpecDigest: profile.SpecDigest, PredecessorUpgradeOperationID: upgrade.OperationID,
		PredecessorProtectedIdentityDigest: strings.Repeat("b", 64),
		SuccessorProfileRevisionID:         uuid.NewString(), SuccessorProfileRevisionNumber: profile.RevisionNumber + 1,
		SuccessorProfileOperationID: uuid.NewString(), SuccessorProfile: successor,
		RosterDigest: rosterDigest, RosterCount: 0,
	}
	specDigest, err := GatewayRebindSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	configureDigest, err := GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	proposal := GatewayRebindPreclaimProposal{
		Spec:              spec,
		RebindApproval:    Approval{Action: ActionRebindGateway, SpecDigest: specDigest, ActorID: testAdministrator},
		ConfigureApproval: Approval{Action: ActionConfigureGateway, SpecDigest: configureDigest, ActorID: testAdministrator},
	}
	snapshot, err := repository.GatewayRebindPreclaimSnapshot(ctx, proposal)
	if err != nil || len(snapshot.Proposal.Roster) != 0 || len(snapshot.GrantBindings) != 0 {
		t.Fatalf("empty roster preclaim=%#v error=%v", snapshot, err)
	}
}

func TestGatewayRebindPreclaimSnapshotRejectsBadProposalAndStoredDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *gatewayRebindFixture, *GatewayRebindPreclaimProposal)
	}{
		{name: "wrong rebind approval", mutate: func(_ *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.RebindApproval.SpecDigest = p.ConfigureApproval.SpecDigest
		}},
		{name: "wrong configure approval", mutate: func(_ *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.ConfigureApproval = p.RebindApproval
		}},
		{name: "stale predecessor", mutate: func(t *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.Spec.PredecessorProfileRevisionNumber++
			p.Spec.SuccessorProfileRevisionNumber++
			refreshGatewayRebindPreclaimProposalDigests(t, p)
		}},
		{name: "partial roster", mutate: func(t *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.Roster = nil
			p.Spec.RosterCount = 0
			refreshGatewayRebindPreclaimProposalDigests(t, p)
		}},
		{name: "wrong entry digest", mutate: func(_ *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.Roster[0].EntryDigest = p.Spec.RosterDigest
		}},
		{name: "active claim", mutate: func(t *testing.T, f *gatewayRebindFixture, _ *GatewayRebindPreclaimProposal) {
			insertGatewayRebindClaim(t, f.db, f.claim)
			insertGatewayRebindRosterEntry(t, f.db, f.entry)
		}},
		{name: "approval actor demoted", mutate: func(t *testing.T, f *gatewayRebindFixture, _ *GatewayRebindPreclaimProposal) {
			if _, err := f.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, testAdministrator); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "runtime head drift", mutate: func(t *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.Roster[0].RouteGeneration++
			refreshGatewayRebindPreclaimProposalDigests(t, p)
		}},
		{name: "grant sequence drift", mutate: func(t *testing.T, _ *gatewayRebindFixture, p *GatewayRebindPreclaimProposal) {
			p.Roster[0].GrantStateSequence++
			refreshGatewayRebindPreclaimProposalDigests(t, p)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
			proposal := gatewayRebindPreclaimProposalFor(fixture)
			test.mutate(t, &fixture, &proposal)
			if _, err := fixture.repository.GatewayRebindPreclaimSnapshot(context.Background(), proposal); err == nil {
				t.Fatal("invalid preclaim accepted")
			}
		})
	}
}

func refreshGatewayRebindPreclaimProposalDigests(t *testing.T, proposal *GatewayRebindPreclaimProposal) {
	t.Helper()
	for index := range proposal.Roster {
		digest, err := GatewayRebindRosterEntryDigest(proposal.Roster[index])
		if err != nil {
			t.Fatal(err)
		}
		proposal.Roster[index].EntryDigest = digest
	}
	var err error
	proposal.Spec.RosterDigest, err = GatewayRebindRosterDigest(proposal.Roster)
	if err != nil {
		t.Fatal(err)
	}
	proposal.RebindApproval.SpecDigest, err = GatewayRebindSpecDigest(proposal.Spec)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRebindPreclaimSnapshotCancellationAndNilRepository(t *testing.T) {
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.repository.GatewayRebindPreclaimSnapshot(ctx, gatewayRebindPreclaimProposalFor(fixture)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error: %v", err)
	}
	if _, err := (*Repository)(nil).GatewayRebindPreclaimSnapshot(context.Background(), gatewayRebindPreclaimProposalFor(fixture)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil repository error: %v", err)
	}
}
