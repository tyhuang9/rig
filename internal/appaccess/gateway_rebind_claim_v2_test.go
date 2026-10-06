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

func TestClaimGatewayRebindV2BindsCompleteRuntimeHeadsAtAdmission(t *testing.T) {
	for _, test := range []struct {
		name                   string
		proposalTimeDrift      bool
		proposalTimeEquivalent bool
		mutate                 func(t *testing.T, fixture gatewayRebindFixture, loopbackID string,
			loopbackHead generatedruntimestate.ActiveHead)
	}{
		{name: "unchanged"},
		{name: "add loopback head", mutate: func(t *testing.T, fixture gatewayRebindFixture,
			_ string, _ generatedruntimestate.ActiveHead,
		) {
			appID := addApps(t, fixture.db, 1)[0]
			seedGatewayRebindActiveRuntime(t, fixture.db, appID)
		}},
		{name: "delete loopback head", mutate: func(t *testing.T, fixture gatewayRebindFixture,
			loopbackID string, _ generatedruntimestate.ActiveHead,
		) {
			if _, err := fixture.db.Exec(`DELETE FROM generated_runtime_active_heads WHERE app_id=?`, loopbackID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replace loopback head", mutate: func(t *testing.T, fixture gatewayRebindFixture,
			_ string, loopbackHead generatedruntimestate.ActiveHead,
		) {
			redeployRuntimeHeadForRebindTest(t, fixture.db, loopbackHead)
		}},
		{name: "equivalent timestamp zone", proposalTimeEquivalent: true},
		{name: "wrong signed loopback timestamp", proposalTimeDrift: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
			loopbackID := addApps(t, fixture.db, 1)[0]
			loopback := seedGatewayRebindActiveRuntime(t, fixture.db, loopbackID)
			finishGatewayRebindFixtureWork(t, fixture.db)
			proposal := gatewayRebindV2ProposalForFixture(t, fixture)
			before := append([]GatewayRebindRuntimeHead(nil), proposal.RuntimeHeads...)
			if test.proposalTimeEquivalent {
				for index := range proposal.RuntimeHeads {
					proposal.RuntimeHeads[index].UpdatedAt = proposal.RuntimeHeads[index].UpdatedAt.In(
						time.FixedZone("equivalent", -5*60*60))
				}
			}
			if test.proposalTimeDrift {
				for index := range proposal.RuntimeHeads {
					if proposal.RuntimeHeads[index].AppID == loopbackID {
						proposal.RuntimeHeads[index].UpdatedAt = proposal.RuntimeHeads[index].UpdatedAt.Add(time.Nanosecond)
					}
				}
				proposal.Spec.RuntimeHeadsDigest, _ = GatewayRebindRuntimeHeadsV2Digest(
					proposal.Spec.OperationID, proposal.RuntimeHeads)
				proposal.RebindApproval.SpecDigest, _ = GatewayRebindSpecV2Digest(proposal.Spec)
			}
			if test.mutate != nil {
				test.mutate(t, fixture, loopbackID, loopback)
				finishGatewayRebindFixtureWork(t, fixture.db)
			}
			after, err := fixture.repository.GatewayRebindRuntimeHeads(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
			if test.mutate == nil && !test.proposalTimeDrift {
				if err != nil || !created || claim.State != GatewayRebindPrepared ||
					!reflect.DeepEqual(after, before) {
					t.Fatalf("unchanged claim=%#v created=%t heads=%#v error=%v", claim, created, after, err)
				}
				return
			}
			if err == nil || created || sameGatewayRebindRuntimeHeads(after, proposal.RuntimeHeads) {
				t.Fatalf("changed census was admitted: created=%t before=%#v after=%#v error=%v",
					created, before, after, err)
			}
			assertNoGatewayRebindClaim(t, fixture, proposal.Spec.OperationID)
		})
	}
}

func TestClaimGatewayRebindV2CanonicalizesNilAndEmptyZeroHeadCensus(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.72.8", InterfaceID: "zero-head-source", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	upgradeInput := approvedGatewayUpgradeClaimInput(t, profile)
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), upgradeInput)
	if err != nil {
		t.Fatal(err)
	}
	owner := gatewayUpgradeClaimOwner(upgrade)
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner,
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner,
		GatewayProfileUpgradeServing, GatewayProfileUpgradeCommitted); err != nil {
		t.Fatal(err)
	}

	operationID := uuid.NewString()
	successor := GatewayProfileSpec{
		SelectedIPv4: "192.168.73.8", InterfaceID: "zero-head-successor", PortStart: 8100, PortEnd: 8119,
	}
	rosterDigest, err := GatewayRebindRosterV2Digest(nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHeadsDigest, err := GatewayRebindRuntimeHeadsV2Digest(operationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	proposal := GatewayRebindPreclaimProposalV2{Spec: GatewayRebindSpecV2{
		Version: GatewayRebindSpecVersionV2, OperationID: operationID,
		Predecessor: GatewayRebindSourceRef{Lineage: GatewayCurrentLineageRef{
			Kind: GatewayRebindSourceGatewayUpgrade, OperationID: upgrade.OperationID,
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
			ProfileSpecDigest: profile.SpecDigest, ProtectedGeneration: 3,
			ProtectedIdentityDigest: strings.Repeat("a", 64), ProtectedJournalDigest: strings.Repeat("b", 64),
		}, SourceStateVersion: 2, SourceStateRevision: 0,
			SourceStateDigest: strings.Repeat("c", 64), PredecessorCheckpointDigest: strings.Repeat("d", 64)},
		SuccessorProtectedGeneration: 4, SuccessorProfileRevisionID: uuid.NewString(),
		SuccessorProfileRevisionNumber: profile.RevisionNumber + 1,
		SuccessorProfileOperationID:    uuid.NewString(), SuccessorProfile: successor,
		RosterVersion: GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: 0,
		RuntimeHeadsVersion: GatewayRebindRuntimeHeadsVersionV1,
		RuntimeHeadsDigest:  runtimeHeadsDigest, RuntimeHeadsCount: 0,
	}}
	profileDigest, err := GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	proposal.ConfigureApproval = Approval{Action: ActionConfigureGateway, SpecDigest: profileDigest, ActorID: testAdministrator}
	proposal.RebindApproval = Approval{Action: ActionRebindGateway, ActorID: testAdministrator}
	proposal.RebindApproval.SpecDigest, err = GatewayRebindSpecV2Digest(proposal.Spec)
	if err != nil {
		t.Fatal(err)
	}
	claim, created, err := repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || !created || claim.Spec.RuntimeHeadsCount != 0 {
		t.Fatalf("zero-head claim=%#v created=%t error=%v", claim, created, err)
	}
	proposal.RuntimeHeads = []GatewayRebindRuntimeHead{}
	replayed, created, err := repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || created || replayed != claim {
		t.Fatalf("empty replay=%#v created=%t error=%v", replayed, created, err)
	}
	snapshot, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || len(snapshot.Active.RuntimeHeads) != 0 {
		t.Fatalf("zero-head recovery=%#v error=%v", snapshot, err)
	}
}

func TestGatewayRebindRuntimeHeadHistoryIsSealedAndValidated(t *testing.T) {
	t.Run("ordinary SQL cannot mutate or extend retained heads", func(t *testing.T) {
		fixture, proposal, claim := claimGatewayRebindV2ForFence(t)
		head := proposal.RuntimeHeads[0]
		if _, err := fixture.db.Exec(`UPDATE generated_runtime_active_heads
			SET generation=generation WHERE app_id=?`, head.AppID); err == nil ||
			!strings.Contains(err.Error(), "rebind fence is active") {
			t.Fatalf("active live-head mutation=%v", err)
		}
		if _, err := fixture.db.Exec(`UPDATE lan_gateway_rebind_runtime_heads
			SET generation=generation+1 WHERE operation_id=?`, claim.Spec.OperationID); err == nil ||
			!strings.Contains(err.Error(), "runtime head is immutable") {
			t.Fatalf("retained update=%v", err)
		}
		if _, err := fixture.db.Exec(`DELETE FROM lan_gateway_rebind_runtime_heads
			WHERE operation_id=?`, claim.Spec.OperationID); err == nil ||
			!strings.Contains(err.Error(), "runtime head history is retained") {
			t.Fatalf("retained delete=%v", err)
		}
		gapDigest, err := gatewayRebindRuntimeHeadV2Digest(claim.Spec.OperationID, 3, head)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`INSERT INTO lan_gateway_rebind_runtime_heads(
			operation_id,ordinal,app_id,deployment_id,release_id,slot,generation,updated_at,entry_digest
		) VALUES(?,3,?,?,?,?,?,?,?)`, claim.Spec.OperationID, head.AppID, head.DeploymentID,
			head.ReleaseID, head.Slot, head.Generation, formatTime(head.UpdatedAt), gapDigest); err == nil ||
			!strings.Contains(err.Error(), "runtime head is not exact") {
			t.Fatalf("extra/gapped retained insert=%v", err)
		}
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil || snapshot.Active == nil ||
			!sameGatewayRebindRuntimeHeads(snapshot.Active.RuntimeHeads, proposal.RuntimeHeads) {
			t.Fatalf("sealed history=%#v error=%v", snapshot, err)
		}
	})

	t.Run("missing retained head fails recovery", func(t *testing.T) {
		fixture, _, claim := claimGatewayRebindV2ForFence(t)
		if _, err := fixture.db.Exec(`DROP TRIGGER lan_gateway_rebind_runtime_head_retain`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`DELETE FROM lan_gateway_rebind_runtime_heads
			WHERE operation_id=?`, claim.Spec.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("missing retained head recovery=%v", err)
		}
	})

	t.Run("unsorted retained heads fail recovery", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		loopbackID := addApps(t, fixture.db, 1)[0]
		seedGatewayRebindActiveRuntime(t, fixture.db, loopbackID)
		finishGatewayRebindFixtureWork(t, fixture.db)
		proposal := gatewayRebindV2ProposalForFixture(t, fixture)
		claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
		if err != nil || !created || len(proposal.RuntimeHeads) != 2 {
			t.Fatalf("two-head claim=%#v created=%t error=%v", claim, created, err)
		}
		first, second := proposal.RuntimeHeads[0], proposal.RuntimeHeads[1]
		firstDigest, _ := gatewayRebindRuntimeHeadV2Digest(claim.Spec.OperationID, 2, first)
		secondDigest, _ := gatewayRebindRuntimeHeadV2Digest(claim.Spec.OperationID, 1, second)
		if _, err := fixture.db.Exec(`DROP TRIGGER lan_gateway_rebind_runtime_head_immutable_update`); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`UPDATE lan_gateway_rebind_runtime_heads SET ordinal=3 WHERE operation_id=? AND ordinal=1`, []any{claim.Spec.OperationID}},
			{`UPDATE lan_gateway_rebind_runtime_heads SET ordinal=1,entry_digest=? WHERE operation_id=? AND ordinal=2`, []any{secondDigest, claim.Spec.OperationID}},
			{`UPDATE lan_gateway_rebind_runtime_heads SET ordinal=2,entry_digest=? WHERE operation_id=? AND ordinal=3`, []any{firstDigest, claim.Spec.OperationID}},
		} {
			if _, err := fixture.db.Exec(statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("unsorted retained head recovery=%v", err)
		}
	})

	t.Run("terminal claim refuses retained extension", func(t *testing.T) {
		fixture, proposal, claim := claimGatewayRebindV2ForFence(t)
		abort := gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindRolledBack, strings.Repeat("9", 64), nil)
		abort.TerminalDisposition = GatewayRebindDispositionAbort
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), abort); err != nil {
			t.Fatal(err)
		}
		head := proposal.RuntimeHeads[0]
		digest, _ := gatewayRebindRuntimeHeadV2Digest(claim.Spec.OperationID, 2, head)
		if _, err := fixture.db.Exec(`INSERT INTO lan_gateway_rebind_runtime_heads(
			operation_id,ordinal,app_id,deployment_id,release_id,slot,generation,updated_at,entry_digest
		) VALUES(?,2,?,?,?,?,?,?,?)`, claim.Spec.OperationID, head.AppID, head.DeploymentID,
			head.ReleaseID, head.Slot, head.Generation, formatTime(head.UpdatedAt), digest); err == nil ||
			!strings.Contains(err.Error(), "runtime head is not exact") {
			t.Fatalf("terminal retained extension=%v", err)
		}
	})
}

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

func TestClaimGatewayRebindV2ReservesGenerationAcrossRollback(t *testing.T) {
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	finishGatewayRebindFixtureWork(t, fixture.db)
	first := gatewayRebindV2ProposalForFixture(t, fixture)
	first.Spec.Predecessor.Lineage.ProtectedGeneration = 0
	first.Spec.SuccessorProtectedGeneration = 1
	first.RebindApproval.SpecDigest, _ = GatewayRebindSpecV2Digest(first.Spec)
	firstClaim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), first)
	if err != nil || !created {
		t.Fatalf("first claim=%#v created=%t error=%v", firstClaim, created, err)
	}
	firstAbort := gatewayRebindProofForClaimV2(t, firstClaim, GatewayRebindPrepared, 1,
		GatewayRebindRolledBack, strings.Repeat("4", 64), nil)
	firstAbort.TerminalDisposition = GatewayRebindDispositionAbort
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), firstAbort); err != nil {
		t.Fatal(err)
	}

	reused := reissueGatewayRebindV2Proposal(t, first, 1)
	if _, _, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), reused); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused protected generation error=%v", err)
	}
	assertNoGatewayRebindClaim(t, fixture, reused.Spec.OperationID)

	second := reissueGatewayRebindV2Proposal(t, first, 2)
	secondClaim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), second)
	if err != nil || !created || secondClaim.Spec.SuccessorProtectedGeneration != 2 {
		t.Fatalf("second claim=%#v created=%t error=%v", secondClaim, created, err)
	}
	wrongGeneration := gatewayRebindProofForClaimV2(t, secondClaim, GatewayRebindPrepared, 1,
		GatewayRebindRolledBack, strings.Repeat("5", 64), nil)
	wrongGeneration.TerminalDisposition = GatewayRebindDispositionAbort
	wrongGeneration.ProtectedGeneration = 1
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), wrongGeneration); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("cross-spec proof generation error=%v", err)
	}
	directInsert, revoke := armGatewayRebindTransitionDirect(t, fixture, wrongGeneration)
	defer revoke()
	if err := directInsert(); err == nil || !strings.Contains(err.Error(), "proof is not exact") {
		t.Fatalf("direct cross-spec proof generation error=%v", err)
	}
	if err := directInsert(); err == nil || !strings.Contains(err.Error(), "invalid or spent") {
		t.Fatalf("direct cross-spec proof reused consumed capability error=%v", err)
	}
	var commands int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_transition_commands
		WHERE operation_id=?`, second.Spec.OperationID).Scan(&commands); err != nil || commands != 0 {
		t.Fatalf("wrong-generation command count=%d error=%v", commands, err)
	}
	secondAbort := gatewayRebindProofForClaimV2(t, secondClaim, GatewayRebindPrepared, 1,
		GatewayRebindRolledBack, strings.Repeat("5", 64), nil)
	secondAbort.TerminalDisposition = GatewayRebindDispositionAbort
	if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), secondAbort); err != nil {
		t.Fatal(err)
	}
}

func reissueGatewayRebindV2Proposal(t *testing.T, base GatewayRebindPreclaimProposalV2,
	generation uint64,
) GatewayRebindPreclaimProposalV2 {
	t.Helper()
	value := base
	value.Roster = append([]GatewayRebindRosterEntryV2(nil), base.Roster...)
	value.Spec.OperationID = uuid.NewString()
	value.Spec.SuccessorProtectedGeneration = generation
	value.Spec.SuccessorProfileRevisionID = uuid.NewString()
	value.Spec.SuccessorProfileOperationID = uuid.NewString()
	for index := range value.Roster {
		value.Roster[index].OperationID = value.Spec.OperationID
		value.Roster[index].EntryDigest, _ = GatewayRebindRosterEntryV2Digest(value.Roster[index])
	}
	value.Spec.RosterDigest, _ = GatewayRebindRosterV2Digest(value.Roster)
	value.Spec.RuntimeHeadsDigest, _ = GatewayRebindRuntimeHeadsV2Digest(value.Spec.OperationID,
		value.RuntimeHeads)
	value.RebindApproval.SpecDigest, _ = GatewayRebindSpecV2Digest(value.Spec)
	return value
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
	runtimeHeads, err := fixture.repository.GatewayRebindRuntimeHeads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtimeHeadsDigest, err := GatewayRebindRuntimeHeadsV2Digest(fixture.claim.Spec.OperationID,
		runtimeHeads)
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
		SuccessorProtectedGeneration:   8,
		SuccessorProfileRevisionID:     fixture.claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: fixture.claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileOperationID:    fixture.claim.Spec.SuccessorProfileOperationID,
		SuccessorProfile:               fixture.claim.Spec.SuccessorProfile,
		RosterVersion:                  GatewayRebindRosterVersionV2, RosterDigest: rosterDigest, RosterCount: 1,
		RuntimeHeadsVersion: GatewayRebindRuntimeHeadsVersionV1,
		RuntimeHeadsDigest:  runtimeHeadsDigest, RuntimeHeadsCount: int64(len(runtimeHeads)),
	}
	specDigest, err := GatewayRebindSpecV2Digest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return GatewayRebindPreclaimProposalV2{
		Spec: spec, Roster: roster, RuntimeHeads: runtimeHeads,
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
