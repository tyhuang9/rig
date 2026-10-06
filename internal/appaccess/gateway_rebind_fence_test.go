package appaccess

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGatewayRebindFenceUsesValidatedPreparedClaim(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	if err := fixture.repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, ErrGatewayRebindActive) {
		t.Fatalf("valid prepared claim fence error = %v", err)
	}

	incomplete := newGatewayRebindFixture(t, false)
	if err := incomplete.repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("incomplete claim fence error = %v", err)
	}
}

func TestGatewayRebindFenceUsesVersionedRecoveryHistory(t *testing.T) {
	t.Run("committed V2 releases after every active phase is fenced", func(t *testing.T) {
		fixture, proposal, claim := claimGatewayRebindV2ForFence(t)
		assertGatewayRebindFenceActive(t, fixture.repository)

		replayed, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
		if err != nil || created || replayed != claim {
			t.Fatalf("prepared replay=%#v created=%t error=%v", replayed, created, err)
		}
		assertGatewayRebindFenceActive(t, fixture.repository)

		receipt := strings.Repeat("7", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
				GatewayRebindSuccessorReady, receipt, nil)); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceActive(t, fixture.repository)

		transfer := gatewayRebindV2TransferForFence(t, fixture, proposal, claim, receipt)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(),
			gatewayRebindProofForClaimV2(t, claim, GatewayRebindSuccessorReady, 2,
				GatewayRebindDatabaseCommitted, receipt, []GatewayRebindAllocationTransfer{transfer})); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceActive(t, fixture.repository)

		committed := gatewayRebindProofForClaimV2(t, claim, GatewayRebindDatabaseCommitted, 3,
			GatewayRebindCommitted, receipt, nil)
		committed.LocalAttestationDigest = strings.Repeat("8", 64)
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), committed); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceOpen(t, fixture.repository)

		replayed, created, err = fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
		if err != nil || created || replayed.State != GatewayRebindCommitted || replayed.StateSequence != 4 {
			t.Fatalf("committed replay=%#v created=%t error=%v", replayed, created, err)
		}
		assertGatewayRebindFenceOpen(t, fixture.repository)
	})

	t.Run("unresolved V2 remains fenced and rolled back V2 releases", func(t *testing.T) {
		fixture, proposal, claim := claimGatewayRebindV2ForFence(t)
		unresolved := gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindUnresolved, "", nil)
		unresolved.TerminalDisposition = GatewayRebindDispositionNone
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), unresolved); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceActive(t, fixture.repository)

		abortReceipt := strings.Repeat("a", 64)
		rolledBack := gatewayRebindProofForClaimV2(t, claim, GatewayRebindUnresolved, 2,
			GatewayRebindRolledBack, abortReceipt, nil)
		rolledBack.TerminalDisposition = GatewayRebindDispositionAbort
		rolledBack.ExpectedHeadRevisionID = claim.Spec.Predecessor.Lineage.ProfileRevisionID
		rolledBack.ExpectedHeadRevisionNumber = claim.Spec.Predecessor.Lineage.ProfileRevisionNumber
		rolledBack.ExpectedHeadSpecDigest = claim.Spec.Predecessor.Lineage.ProfileSpecDigest
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), rolledBack); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceOpen(t, fixture.repository)

		replayed, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
		if err != nil || created || replayed.State != GatewayRebindRolledBack || replayed.StateSequence != 3 {
			t.Fatalf("rolled-back replay=%#v created=%t error=%v", replayed, created, err)
		}
		assertGatewayRebindFenceOpen(t, fixture.repository)
	})

	t.Run("corrupt terminal V2 history fails closed", func(t *testing.T) {
		fixture, _, claim := claimGatewayRebindV2ForFence(t)
		rolledBack := gatewayRebindProofForClaimV2(t, claim, GatewayRebindPrepared, 1,
			GatewayRebindRolledBack, strings.Repeat("b", 64), nil)
		rolledBack.TerminalDisposition = GatewayRebindDispositionAbort
		if _, err := fixture.repository.ApplyGatewayRebindTransition(context.Background(), rolledBack); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceOpen(t, fixture.repository)

		if _, err := fixture.db.Exec(`DROP TRIGGER lan_gateway_rebind_event_retain`); err != nil {
			t.Fatal(err)
		}
		if result, err := fixture.db.Exec(`DELETE FROM lan_gateway_rebind_claim_events
			WHERE operation_id=? AND sequence=2`, claim.Spec.OperationID); err != nil {
			t.Fatal(err)
		} else if affected, _ := result.RowsAffected(); affected != 1 {
			t.Fatalf("deleted terminal event rows=%d", affected)
		}
		if err := fixture.repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("corrupt terminal history fence error=%v", err)
		}
	})
}

func TestGatewayRebindFenceAllowsValidatedNoHistoryStates(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		assertGatewayRebindFenceOpen(t, testRepository(appAccessDB(t)))
	})

	t.Run("configured before upgrade", func(t *testing.T) {
		repository := testRepository(appAccessDB(t))
		if _, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
			GatewayProfileSpec{SelectedIPv4: "192.168.94.8", InterfaceID: "fence-pre-upgrade",
				PortStart: 8100, PortEnd: 8119}, 0)); err != nil {
			t.Fatal(err)
		}
		assertGatewayRebindFenceOpen(t, repository)
	})

	t.Run("committed native upgrade", func(t *testing.T) {
		fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
		assertGatewayRebindFenceOpen(t, fixture.repository)
	})
}

func claimGatewayRebindV2ForFence(t *testing.T,
) (gatewayRebindFixture, GatewayRebindPreclaimProposalV2, GatewayRebindClaimV2) {
	t.Helper()
	fixture := newGatewayRebindFixtureOnDBWithClaim(t, appAccessDB(t), false, false)
	finishGatewayRebindFixtureWork(t, fixture.db)
	proposal := gatewayRebindV2ProposalForFixture(t, fixture)
	claim, created, err := fixture.repository.ClaimGatewayRebindV2(context.Background(), proposal)
	if err != nil || !created {
		t.Fatalf("claim=%#v created=%t error=%v", claim, created, err)
	}
	return fixture, proposal, claim
}

func gatewayRebindV2TransferForFence(t *testing.T, fixture gatewayRebindFixture,
	proposal GatewayRebindPreclaimProposalV2, claim GatewayRebindClaimV2, receipt string,
) GatewayRebindAllocationTransfer {
	t.Helper()
	transfer := GatewayRebindAllocationTransfer{
		Version: GatewayRebindTransferVersionV1, OperationID: claim.Spec.OperationID,
		Ordinal: 1, AppID: fixture.entry.AppID, AllocationID: fixture.entry.AllocationID,
		GrantAttemptID: fixture.entry.GrantAttemptID, SourceBindingDigest: strings.Repeat("5", 64),
		RosterEntryDigest:              proposal.Roster[0].EntryDigest,
		SourceProfileRevisionID:        fixture.profile.ID,
		SourceProfileRevisionNumber:    fixture.profile.RevisionNumber,
		SourceProfileSpecDigest:        fixture.profile.SpecDigest,
		SuccessorProfileRevisionID:     claim.Spec.SuccessorProfileRevisionID,
		SuccessorProfileRevisionNumber: claim.Spec.SuccessorProfileRevisionNumber,
		SuccessorProfileSpecDigest:     claim.ConfigureApproval.SpecDigest,
		TerminalReceiptDigest:          receipt,
	}
	var err error
	transfer.TransferDigest, err = GatewayRebindAllocationTransferDigest(transfer)
	if err != nil {
		t.Fatal(err)
	}
	return transfer
}

func assertGatewayRebindFenceActive(t *testing.T, repository *Repository) {
	t.Helper()
	if err := repository.CheckGatewayRebindFence(context.Background()); !errors.Is(err, ErrGatewayRebindActive) {
		t.Fatalf("active fence error=%v", err)
	}
}

func assertGatewayRebindFenceOpen(t *testing.T, repository *Repository) {
	t.Helper()
	if err := repository.CheckGatewayRebindFence(context.Background()); err != nil {
		t.Fatalf("open fence error=%v", err)
	}
}
