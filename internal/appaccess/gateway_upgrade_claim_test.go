package appaccess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestGatewayProfileUpgradeDigestMatchesProtectedJournalContract(t *testing.T) {
	spec := GatewayProfileUpgradeSpec{
		ProfileRevisionID:     "33333333-3333-4333-8333-333333333333",
		ProfileRevisionNumber: 7,
		ProfileSpecDigest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	digest, err := GatewayProfileUpgradeSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "02a5b19f5da19a7ecc264505a76f4e3acc1c5f515d24c6e25c0966190114c6b3"
	if digest != expected {
		t.Fatalf("upgrade digest = %q, want protected-journal contract %q", digest, expected)
	}
	for _, invalid := range []GatewayProfileUpgradeSpec{
		{},
		{ProfileRevisionID: uuid.NewString(), ProfileRevisionNumber: 0, ProfileSpecDigest: spec.ProfileSpecDigest},
		{ProfileRevisionID: uuid.NewString(), ProfileRevisionNumber: 1, ProfileSpecDigest: "not-a-digest"},
	} {
		if _, err := GatewayProfileUpgradeSpecDigest(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid spec %#v error = %v", invalid, err)
		}
	}
}

func TestGatewayProfileUpgradeClaimApprovalReplayStaleAndCompeting(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.44.8", InterfaceID: "upgrade-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	input := approvedGatewayUpgradeClaimInput(t, profile)

	wrongAction := input
	wrongAction.OperationID = uuid.NewString()
	wrongAction.Approval.Action = ActionConfigureGateway
	if _, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), wrongAction); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("wrong claim action error = %v", err)
	}
	viewer := input
	viewer.OperationID = uuid.NewString()
	viewer.Approval.ActorID = testViewer
	if _, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), viewer); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("viewer claim error = %v", err)
	}
	stale := input
	stale.OperationID = uuid.NewString()
	stale.Spec.ProfileRevisionNumber++
	stale.Approval.SpecDigest = mustGatewayUpgradeDigest(t, stale.Spec)
	if _, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile claim error = %v", err)
	}

	claim, created, err := repository.ClaimGatewayProfileUpgrade(context.Background(), input)
	if err != nil || !created || claim.State != GatewayProfileUpgradePrepared || claim.StateSequence != 1 || claim.ProfileRevisionID != profile.ID {
		t.Fatalf("claim = %#v created=%t error=%v", claim, created, err)
	}
	replayed, created, err := New(db).ClaimGatewayProfileUpgrade(context.Background(), input)
	if err != nil || created || replayed != claim {
		t.Fatalf("claim replay = %#v created=%t error=%v", replayed, created, err)
	}
	changed := input
	changed.Approval.ActorID = testViewer
	if _, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), changed); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed claim replay error = %v", err)
	}
	competing := approvedGatewayUpgradeClaimInput(t, profile)
	if _, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), competing); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing claim error = %v", err)
	}
	current, err := repository.CurrentGatewayProfileUpgradeClaim(context.Background())
	if err != nil || current != claim {
		t.Fatalf("current claim = %#v error=%v", current, err)
	}
	byOperation, err := New(db).GatewayProfileUpgradeClaim(context.Background(), input.OperationID)
	if err != nil || byOperation != claim {
		t.Fatalf("reopened claim = %#v error=%v", byOperation, err)
	}

	replacement := approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "10.44.0.8", InterfaceID: "replacement-adapter", PortStart: 8100, PortEnd: 8119}, 1)
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), replacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("profile replacement while prepared error = %v", err)
	}
}

func TestGatewayProfileUpgradeClaimTransitionsRetainHistoryAndCommittedPin(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "172.20.44.8", InterfaceID: "transition-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	input := approvedGatewayUpgradeClaimInput(t, profile)
	claim, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	owner := gatewayUpgradeClaimOwner(claim)
	serving, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing)
	if err != nil || !changed || serving.State != GatewayProfileUpgradeServing || serving.StateSequence != 2 {
		t.Fatalf("serving claim = %#v changed=%t error=%v", serving, changed, err)
	}
	replayed, changed, err := New(db).AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing)
	if err != nil || changed || replayed != serving {
		t.Fatalf("serving replay = %#v changed=%t error=%v", replayed, changed, err)
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradePrepared, GatewayProfileUpgradeUnresolved); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transition error = %v", err)
	}
	wrongOwner := owner
	wrongOwner.ProfileRevisionID = uuid.NewString()
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), wrongOwner, GatewayProfileUpgradeServing, GatewayProfileUpgradeCommitted); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner transition error = %v", err)
	}
	committed, changed, err := New(db).AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradeServing, GatewayProfileUpgradeCommitted)
	if err != nil || !changed || committed.State != GatewayProfileUpgradeCommitted || committed.StateSequence != 3 {
		t.Fatalf("committed claim = %#v changed=%t error=%v", committed, changed, err)
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradePrepared, GatewayProfileUpgradeCommitted); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-predecessor committed replay error = %v", err)
	}
	current, err := repository.CurrentGatewayProfileUpgradeClaim(context.Background())
	if err != nil || current != committed {
		t.Fatalf("committed current claim = %#v error=%v", current, err)
	}
	replacement := approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.45.8", InterfaceID: "committed-replacement", PortStart: 8100, PortEnd: 8119}, 1)
	if _, _, err := repository.ConfigureGatewayProfile(context.Background(), replacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("profile replacement while committed error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,interface_id,
		port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), 2, uuid.NewString(),
		"1111111111111111111111111111111111111111111111111111111111111111", ActionConfigureGateway,
		"192.168.45.8", "direct-sql-adapter", 8100, 8119,
		"2222222222222222222222222222222222222222222222222222222222222222", testAdministrator, formatTime(testNow)); err == nil {
		t.Fatal("direct SQL inserted a profile revision while committed claim pinned the profile")
	}

	var events, audit int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_upgrade_claim_events WHERE operation_id=?`, owner.OperationID).Scan(&events); err != nil || events != 3 {
		t.Fatalf("transition events=%d error=%v", events, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_type='lan_gateway_upgrade' AND resource_id=?`, owner.OperationID).Scan(&audit); err != nil || audit != 3 {
		t.Fatalf("upgrade audit events=%d error=%v", audit, err)
	}
	if _, err := db.Exec(`UPDATE lan_gateway_upgrade_claim_events SET state='unresolved' WHERE operation_id=? AND sequence=1`, owner.OperationID); err == nil {
		t.Fatal("immutable claim event was updated")
	}
	if _, err := db.Exec(`DELETE FROM lan_gateway_upgrade_claims WHERE operation_id=?`, owner.OperationID); err == nil {
		t.Fatal("upgrade claim was deleted")
	}
	if _, err := db.Exec(`UPDATE audit_events SET metadata_json='{}' WHERE resource_type='lan_gateway_upgrade' AND resource_id=?`, owner.OperationID); err == nil {
		t.Fatal("upgrade audit event was updated")
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE resource_type='lan_gateway_upgrade' AND resource_id=?`, owner.OperationID); err == nil {
		t.Fatal("upgrade audit event was deleted")
	}
}

func TestGatewayProfileUpgradeClaimCompletesSynchronouslyOrAfterUnresolvedRecovery(t *testing.T) {
	for _, test := range []struct {
		name       string
		unresolved bool
		terminal   GatewayProfileUpgradeState
	}{
		{name: "synchronous commit", terminal: GatewayProfileUpgradeCommitted},
		{name: "commit after unresolved", unresolved: true, terminal: GatewayProfileUpgradeCommitted},
		{name: "rollback after unresolved", unresolved: true, terminal: GatewayProfileUpgradeRolledBack},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db := appAccessDB(t)
			repository := testRepository(db)
			profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
				GatewayProfileSpec{SelectedIPv4: "192.168.48.8", InterfaceID: "recovery-adapter", PortStart: 8100, PortEnd: 8119}, 0))
			if err != nil {
				t.Fatal(err)
			}
			claim, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
			if err != nil {
				t.Fatal(err)
			}
			owner := gatewayUpgradeClaimOwner(claim)
			predecessor := GatewayProfileUpgradePrepared
			if test.unresolved {
				if _, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner, predecessor, GatewayProfileUpgradeUnresolved); err != nil || !changed {
					t.Fatalf("unresolved transition changed=%t error=%v", changed, err)
				}
				if _, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner, predecessor, GatewayProfileUpgradeUnresolved); err != nil || changed {
					t.Fatalf("unresolved replay changed=%t error=%v", changed, err)
				}
				if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner, GatewayProfileUpgradeUnresolved, GatewayProfileUpgradeServing); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("unresolved-to-serving error=%v", err)
				}
				predecessor = GatewayProfileUpgradeUnresolved
			}
			terminal, changed, err := New(db).AdvanceGatewayProfileUpgradeClaim(ctx, owner, predecessor, test.terminal)
			if err != nil || !changed || terminal.State != test.terminal {
				t.Fatalf("terminal transition=%#v changed=%t error=%v", terminal, changed, err)
			}
			replayed, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner, predecessor, test.terminal)
			if err != nil || changed || replayed != terminal {
				t.Fatalf("terminal replay=%#v changed=%t error=%v", replayed, changed, err)
			}
			if test.unresolved {
				if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner, GatewayProfileUpgradePrepared, test.terminal); !errors.Is(err, ErrConflict) {
					t.Fatalf("stale terminal predecessor error=%v", err)
				}
			} else if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, owner, GatewayProfileUpgradeServing, test.terminal); !errors.Is(err, ErrConflict) {
				t.Fatalf("wrong terminal predecessor error=%v", err)
			}
			var events int
			if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_upgrade_claim_events WHERE operation_id=?`, owner.OperationID).Scan(&events); err != nil || events != int(terminal.StateSequence) {
				t.Fatalf("events=%d sequence=%d error=%v", events, terminal.StateSequence, err)
			}
		})
	}
}

func TestGatewayProfileUpgradeClaimRequiresInitialPreparedEvent(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.49.8", InterfaceID: "initial-event-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	input := approvedGatewayUpgradeClaimInput(t, profile)
	requestDigest, err := gatewayUpgradeClaimRequestDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []GatewayProfileUpgradeState{GatewayProfileUpgradePrepared, GatewayProfileUpgradeCommitted} {
		if _, err := db.Exec(`INSERT INTO lan_gateway_upgrade_claims(
			operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
			profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, input.OperationID, requestDigest, ActionUpgradeGateway,
			profile.ID, profile.RevisionNumber, profile.SpecDigest, input.Approval.ActorID,
			formatTime(testNow), state, 2, formatTime(testNow)); err == nil {
			t.Fatalf("initial %s claim at sequence 2 was accepted", state)
		}
	}
	if _, created, err := repository.ClaimGatewayProfileUpgrade(ctx, input); err != nil || !created {
		t.Fatalf("valid prepared claim created=%t error=%v", created, err)
	}
}

func TestGatewayProfileUpgradeClaimPinsPreviouslyInsertedProfileRevision(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.50.8", InterfaceID: "head-pin-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	future := GatewayProfileSpec{SelectedIPv4: "192.168.51.8", InterfaceID: "future-adapter", PortStart: 8100, PortEnd: 8119}
	futureDigest, err := GatewayProfileSpecDigest(future)
	if err != nil {
		t.Fatal(err)
	}
	futureID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,interface_id,
		port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, futureID, 2, uuid.NewString(),
		"1111111111111111111111111111111111111111111111111111111111111111", ActionConfigureGateway,
		future.SelectedIPv4, future.InterfaceID, future.PortStart, future.PortEnd, futureDigest,
		testAdministrator, formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=2,updated_at=? WHERE singleton=1`,
		futureID, formatTime(testNow)); err == nil {
		t.Fatal("profile head advanced to an earlier inserted revision while upgrade claim pinned it")
	}
	current, err := repository.CurrentGatewayProfile(ctx)
	if err != nil || current.ID != profile.ID {
		t.Fatalf("current profile=%#v error=%v", current, err)
	}
}

func TestCurrentGatewayProfileUpgradeClaimUsesOneReadSnapshot(t *testing.T) {
	dataRoot := t.TempDir()
	firstDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.Close() })
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	addUsers(t, firstDB)
	reader := testRepository(firstDB)
	writer := testRepository(secondDB)
	profile, _, err := reader.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.52.8", InterfaceID: "snapshot-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := reader.ClaimGatewayProfileUpgrade(context.Background(), approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lookedUp := make(chan struct{})
	resume := make(chan struct{})
	reader.afterCurrentClaimLookup = func() {
		close(lookedUp)
		<-resume
	}
	type readResult struct {
		claim GatewayProfileUpgradeClaim
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		value, err := reader.CurrentGatewayProfileUpgradeClaim(ctx)
		result <- readResult{claim: value, err: err}
	}()
	select {
	case <-lookedUp:
	case <-ctx.Done():
		t.Fatalf("claim lookup did not reach the snapshot boundary: %v", ctx.Err())
	}
	_, _, writeErr := writer.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeRolledBack)
	close(resume)
	if writeErr != nil {
		t.Fatalf("concurrent rollback: %v", writeErr)
	}
	select {
	case got := <-result:
		if got.err != nil || got.claim.State != GatewayProfileUpgradePrepared {
			t.Fatalf("snapshot claim=%#v error=%v", got.claim, got.err)
		}
	case <-ctx.Done():
		t.Fatalf("snapshot read did not finish: %v", ctx.Err())
	}
	reader.afterCurrentClaimLookup = nil
	if _, err := reader.CurrentGatewayProfileUpgradeClaim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current claim after rollback error=%v", err)
	}
}

func TestGatewayProfileUpgradeRolledBackUnpinsProfileAndUnresolvedDoesNot(t *testing.T) {
	for _, test := range []struct {
		name       string
		terminal   GatewayProfileUpgradeState
		wantPinned bool
	}{
		{name: "rolled back", terminal: GatewayProfileUpgradeRolledBack, wantPinned: false},
		{name: "unresolved", terminal: GatewayProfileUpgradeUnresolved, wantPinned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := appAccessDB(t)
			repository := testRepository(db)
			profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
				GatewayProfileSpec{SelectedIPv4: "192.168.46.8", InterfaceID: "terminal-adapter", PortStart: 8100, PortEnd: 8119}, 0))
			if err != nil {
				t.Fatal(err)
			}
			claim, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), approvedGatewayUpgradeClaimInput(t, profile))
			if err != nil {
				t.Fatal(err)
			}
			owner := gatewayUpgradeClaimOwner(claim)
			if _, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradePrepared, test.terminal); err != nil || !changed {
				t.Fatalf("terminal transition changed=%t error=%v", changed, err)
			}
			if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), owner, GatewayProfileUpgradeServing, test.terminal); !errors.Is(err, ErrConflict) {
				t.Fatalf("wrong-predecessor terminal replay error = %v", err)
			}
			_, currentErr := New(db).CurrentGatewayProfileUpgradeClaim(context.Background())
			if test.wantPinned && currentErr != nil {
				t.Fatalf("pinned current error = %v", currentErr)
			}
			if !test.wantPinned && !errors.Is(currentErr, ErrNotFound) {
				t.Fatalf("rolled-back current error = %v", currentErr)
			}
			replacement := approvedGatewayInput(t,
				GatewayProfileSpec{SelectedIPv4: "10.46.0.8", InterfaceID: "terminal-replacement", PortStart: 8100, PortEnd: 8119}, 1)
			_, created, configureErr := repository.ConfigureGatewayProfile(context.Background(), replacement)
			if test.wantPinned && !errors.Is(configureErr, ErrConflict) {
				t.Fatalf("pinned replacement created=%t error=%v", created, configureErr)
			}
			if !test.wantPinned && (configureErr != nil || !created) {
				t.Fatalf("rolled-back replacement created=%t error=%v", created, configureErr)
			}
		})
	}
}

func TestIndependentDatabaseHandlesSerializeGatewayUpgradeClaims(t *testing.T) {
	dataRoot := t.TempDir()
	firstDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.Close() })
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	if _, err := secondDB.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	addUsers(t, firstDB)
	firstRepository := testRepository(firstDB)
	secondRepository := testRepository(secondDB)
	profile, _, err := firstRepository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.47.8", InterfaceID: "claim-race-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	firstInput := approvedGatewayUpgradeClaimInput(t, profile)
	secondInput := approvedGatewayUpgradeClaimInput(t, profile)
	locked := make(chan struct{})
	release := make(chan struct{})
	firstRepository.afterGatewayClaimLock = func() {
		close(locked)
		<-release
	}
	type result struct {
		claim   GatewayProfileUpgradeClaim
		created bool
		err     error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		claim, created, err := firstRepository.ClaimGatewayProfileUpgrade(context.Background(), firstInput)
		firstResult <- result{claim: claim, created: created, err: err}
	}()
	waitForSignal(t, locked, "gateway upgrade claim lock")
	go func() {
		claim, created, err := secondRepository.ClaimGatewayProfileUpgrade(context.Background(), secondInput)
		secondResult <- result{claim: claim, created: created, err: err}
	}()
	second := waitForResult(t, secondResult, "contended gateway upgrade claim")
	close(release)
	if !errors.Is(second.err, ErrConflict) {
		t.Fatalf("contended claim = %#v created=%t error=%v", second.claim, second.created, second.err)
	}
	first := waitForResult(t, firstResult, "first gateway upgrade claim")
	if first.err != nil || !first.created {
		t.Fatalf("first claim = %#v created=%t error=%v", first.claim, first.created, first.err)
	}
	if claim, created, err := secondRepository.ClaimGatewayProfileUpgrade(context.Background(), secondInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("serialized competing claim = %#v created=%t error=%v", claim, created, err)
	}
	replayed, created, err := secondRepository.ClaimGatewayProfileUpgrade(context.Background(), firstInput)
	if err != nil || created || replayed.OperationID != first.claim.OperationID {
		t.Fatalf("cross-handle claim replay = %#v created=%t error=%v", replayed, created, err)
	}
}

func approvedGatewayUpgradeClaimInput(t *testing.T, profile GatewayProfileRevision) ClaimGatewayProfileUpgradeInput {
	t.Helper()
	spec := GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
	}
	return ClaimGatewayProfileUpgradeInput{
		OperationID: uuid.NewString(), Spec: spec,
		Approval: Approval{Action: ActionUpgradeGateway, SpecDigest: mustGatewayUpgradeDigest(t, spec), ActorID: testAdministrator},
	}
}

func mustGatewayUpgradeDigest(t *testing.T, spec GatewayProfileUpgradeSpec) string {
	t.Helper()
	digest, err := GatewayProfileUpgradeSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func gatewayUpgradeClaimOwner(claim GatewayProfileUpgradeClaim) GatewayProfileUpgradeClaimOwner {
	return GatewayProfileUpgradeClaimOwner{
		OperationID: claim.OperationID, ProfileRevisionID: claim.ProfileRevisionID, ProfileRevisionNumber: claim.ProfileRevisionNumber,
	}
}
