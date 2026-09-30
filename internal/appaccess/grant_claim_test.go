package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestAppAccessGrantClaimLifecycleAuthorizationAndDisableFence(t *testing.T) {
	db, repository, revision, _, input := approvedGrantFixture(t)
	claim, created, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil || !created || claim.State != AppAccessGrantPrepared || claim.StateSequence != 1 {
		t.Fatalf("claim = %#v created=%t error=%v", claim, created, err)
	}
	replayed, created, err := New(db).ClaimAppAccessGrant(context.Background(), input)
	if err != nil || created || replayed.AttemptID != claim.AttemptID || replayed.RequestDigest != claim.RequestDigest {
		t.Fatalf("replay = %#v created=%t error=%v", replayed, created, err)
	}
	changedInput := input
	changedInput.Spec.Port++
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), changedInput); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed replay error = %v", err)
	}
	competing := input
	competing.AttemptID = uuid.NewString()
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), competing); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing claim error = %v", err)
	}

	owner := AppAccessGrantClaimOwnerFor(claim)
	applying, changed, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying)
	if err != nil || !changed || applying.State != AppAccessGrantApplying {
		t.Fatalf("applying = %#v changed=%t error=%v", applying, changed, err)
	}
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), approvedDisableInput(t, revision)); err == nil {
		t.Fatal("disable intent was inserted while grant applied")
	}
	var disables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_intents WHERE allocation_id=?`,
		revision.Allocation.ID).Scan(&disables); err != nil || disables != 0 {
		t.Fatalf("disable intents=%d error=%v", disables, err)
	}
	if _, err := repository.AuthorizeAppAccessGrant(context.Background(), AppAccessGrantAuthorizationInput{
		Owner: owner, PermittedStates: []AppAccessGrantState{AppAccessGrantApplying},
	}); err != nil {
		t.Fatalf("applying authorization: %v", err)
	}
	dbActive, changed, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive)
	if err != nil || !changed || dbActive.State != AppAccessGrantDBActive {
		t.Fatalf("db active = %#v changed=%t error=%v", dbActive, changed, err)
	}
	allocation, err := readAllocationByID(context.Background(), db, revision.Allocation.ID)
	if err != nil || allocation.State != AllocationActive {
		t.Fatalf("activated allocation = %#v error=%v", allocation, err)
	}
	proof := AppAccessGrantProof{
		GatewayOperationID:   uuid.NewString(),
		ProtectedStateDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ObservedAt:           testNow.Add(time.Second),
	}
	committed, changed, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, proof)
	if err != nil || !changed || committed.State != AppAccessGrantCommitted || committed.Proof == nil ||
		committed.Proof.ProtectedStateDigest != proof.ProtectedStateDigest {
		t.Fatalf("committed = %#v changed=%t error=%v", committed, changed, err)
	}
	if _, changed, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, proof); err != nil || changed {
		t.Fatalf("commit replay changed=%t error=%v", changed, err)
	}
	differentProof := proof
	differentProof.ProtectedStateDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, differentProof); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("different proof replay error = %v", err)
	}

	if _, err := db.Exec(`UPDATE lan_app_access_grant_claim_events SET state='uncertain'
		WHERE attempt_id=? AND sequence=1`, claim.AttemptID); err == nil {
		t.Fatal("grant event was updated")
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_grant_claim_events WHERE attempt_id=?`, claim.AttemptID); err == nil {
		t.Fatal("grant events were deleted")
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_grant_claims WHERE attempt_id=?`, claim.AttemptID); err == nil {
		t.Fatal("grant claim was deleted")
	}
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), approvedDisableInput(t, revision)); err != nil {
		t.Fatalf("committed grant should permit later disable intent: %v", err)
	}
	if _, err := repository.AuthorizeAppAccessGrant(context.Background(), AppAccessGrantAuthorizationInput{
		Owner: owner, PermittedStates: []AppAccessGrantState{AppAccessGrantCommitted},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("authorization with disable intent error = %v", err)
	}
}

func TestAppAccessGrantAuditRecordsExecutingAdministratorSeparatelyFromApprover(t *testing.T) {
	db, repository, revision, _, input := approvedGrantFixture(t)
	operatorID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'grant-operator','hash','administrator',datetime('now'),datetime('now'))`, operatorID); err != nil {
		t.Fatal(err)
	}
	input.ActorID = operatorID
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil || claim.Spec.ApprovedBy != revision.ApprovedBy || claim.Spec.ApprovedBy == operatorID {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	owner.ActorID = operatorID
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	// The original approver may lose access while a different administrator
	// performs fail-closed recovery. That recovery must retain the real actor.
	if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, revision.ApprovedBy); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantRolledBack, AppAccessGrantProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT actor_id FROM audit_events
		WHERE resource_type='lan_app_access_grant' AND resource_id=? ORDER BY id`, claim.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var actor string
		if err := rows.Scan(&actor); err != nil || actor != operatorID {
			t.Fatalf("audit actor=%q err=%v, want %q", actor, err, operatorID)
		}
		count++
	}
	if err := rows.Err(); err != nil || count != 3 {
		t.Fatalf("audit rows=%d err=%v", count, err)
	}
}

func TestAppAccessGrantAmbiguousActivationReplayRetainsClaim(t *testing.T) {
	_, repository, revision, _, input := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	repository.beforeGrantTransitionCommit = cancel
	if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, owner,
		AppAccessGrantApplying, AppAccessGrantDBActive); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled activation error = %v", err)
	}
	repository.beforeGrantTransitionCommit = nil
	retained, err := repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || retained.State != AppAccessGrantApplying {
		t.Fatalf("retained claim = %#v error=%v", retained, err)
	}
	allocation, err := readAllocationByID(context.Background(), repository.db, revision.Allocation.ID)
	if err != nil || allocation.State != AllocationReserved {
		t.Fatalf("rolled-back activation allocation = %#v error=%v", allocation, err)
	}
	activated, changed, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive)
	if err != nil || !changed || activated.State != AppAccessGrantDBActive {
		t.Fatalf("activation retry = %#v changed=%t error=%v", activated, changed, err)
	}
	replayed, changed, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive)
	if err != nil || changed || replayed.State != AppAccessGrantDBActive {
		t.Fatalf("activation readback replay = %#v changed=%t error=%v", replayed, changed, err)
	}
}

func TestAppAccessGrantAuthorizationAndApplyingRejectStaleAdministrator(t *testing.T) {
	db, repository, _, _, input := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, input.Spec.ApprovedBy); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthorizeAppAccessGrant(context.Background(), AppAccessGrantAuthorizationInput{
		Owner: owner, PermittedStates: []AppAccessGrantState{AppAccessGrantApplying},
	}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("stale administrator authorization error = %v", err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive); err == nil {
		t.Fatal("applying claim entered db_active after approver demotion")
	}
	retained, err := repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || retained.State != AppAccessGrantApplying {
		t.Fatalf("retained claim = %#v error=%v", retained, err)
	}
}

func TestAppAccessGrantTerminalCommitRejectsDemotedApprover(t *testing.T) {
	db, repository, _, _, input := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, input.Spec.ApprovedBy); err != nil {
		t.Fatal(err)
	}
	proof := AppAccessGrantProof{GatewayOperationID: uuid.NewString(),
		ProtectedStateDigest: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, proof); err == nil {
		t.Fatal("committed grant after approving administrator lost role")
	}
	retained, err := repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || retained.State != AppAccessGrantDBActive {
		t.Fatalf("terminal failure changed claim: %#v error=%v", retained, err)
	}
}

func TestAppAccessGrantDirectHeadDeletionIsFenced(t *testing.T) {
	db, repository, revision, _, input := approvedGrantFixture(t)
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_heads WHERE app_id=?`, revision.AppID); err == nil {
		t.Fatal("deleted access head of retained grant")
	}
	if _, err := db.Exec(`DELETE FROM lan_gateway_profile_heads WHERE singleton=1`); err == nil {
		t.Fatal("deleted gateway profile head of retained grant")
	}
}

func TestAppAccessGrantSerializesUnresolvedAttemptsAcrossApps(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.92.9", InterfaceID: "grant-serialization", PortStart: 8109, PortEnd: 8110}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appIDs := addApps(t, db, 2)
	inputs := make([]ClaimAppAccessGrantInput, 0, len(appIDs))
	for _, appID := range appIDs {
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
		inputs = append(inputs, ClaimAppAccessGrantInput{AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: revision.ApprovedBy})
	}
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), inputs[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), inputs[1]); !errors.Is(err, ErrConflict) {
		t.Fatalf("second unresolved app grant error = %v", err)
	}
}

func TestAppAccessGrantUncertainRollbackRequiresDistinctRetryAttempt(t *testing.T) {
	_, repository, revision, _, input := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantUncertain); err != nil {
		t.Fatal(err)
	}
	proof := AppAccessGrantProof{
		GatewayOperationID:   uuid.NewString(),
		ProtectedStateDigest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ObservedAt:           testNow.Add(time.Second),
	}
	rolledBack, changed, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantUncertain, AppAccessGrantRolledBack, proof)
	if err != nil || !changed || rolledBack.State != AppAccessGrantRolledBack {
		t.Fatalf("rollback = %#v changed=%t error=%v", rolledBack, changed, err)
	}
	allocation, err := readAllocationByID(context.Background(), repository.db, revision.Allocation.ID)
	if err != nil || allocation.State != AllocationUncertain {
		t.Fatalf("rollback allocation = %#v error=%v", allocation, err)
	}
	retry := input
	retry.AttemptID = uuid.NewString()
	second, created, err := repository.ClaimAppAccessGrant(context.Background(), retry)
	if err != nil || !created || second.AttemptID == claim.AttemptID {
		t.Fatalf("distinct retry = %#v created=%t error=%v", second, created, err)
	}
	if _, _, err := repository.ClaimAppAccessGrant(context.Background(), input); err != nil {
		t.Fatalf("historical original replay: %v", err)
	}
}

func TestAppAccessGrantProvedRollbackDemotesLegacyActiveAllocation(t *testing.T) {
	_, repository, revision, _, input := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	allocationOwner := AllocationOwner{
		AllocationID: revision.Allocation.ID, AppID: revision.AppID,
		OperationID: revision.OperationID, AccessRevisionID: revision.ID,
	}
	if allocation, err := repository.Activate(context.Background(), allocationOwner); err != nil || allocation.State != AllocationActive {
		t.Fatalf("legacy activation = %#v error=%v", allocation, err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	proof := AppAccessGrantProof{
		GatewayOperationID:   uuid.NewString(),
		ProtectedStateDigest: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantRolledBack, proof); err != nil {
		t.Fatal(err)
	}
	allocation, err := readAllocationByID(context.Background(), repository.db, revision.Allocation.ID)
	if err != nil || allocation.State != AllocationUncertain {
		t.Fatalf("rolled-back legacy allocation = %#v error=%v", allocation, err)
	}
}

func TestIndependentDatabaseHandlesSerializeAppAccessGrantClaims(t *testing.T) {
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	addUsers(t, db)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.84.10", InterfaceID: "grant-contention", PortStart: 8110, PortEnd: 8110}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appID := addApps(t, db, 1)[0]
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
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	first := testRepository(db)
	second := testRepository(secondDB)
	firstInput := ClaimAppAccessGrantInput{AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: revision.ApprovedBy}
	secondInput := firstInput
	secondInput.AttemptID = uuid.NewString()
	locked := make(chan struct{})
	release := make(chan struct{})
	first.afterGrantClaimLock = func() {
		close(locked)
		<-release
	}
	type result struct {
		claim   AppAccessGrantClaim
		created bool
		err     error
	}
	firstResult := make(chan result, 1)
	go func() {
		claim, created, err := first.ClaimAppAccessGrant(context.Background(), firstInput)
		firstResult <- result{claim: claim, created: created, err: err}
	}()
	waitForSignal(t, locked, "first grant claim lock")
	secondResult := make(chan result, 1)
	go func() {
		claim, created, err := second.ClaimAppAccessGrant(context.Background(), secondInput)
		secondResult <- result{claim: claim, created: created, err: err}
	}()
	close(release)
	gotFirst := waitForResult(t, firstResult, "first grant claim")
	gotSecond := waitForResult(t, secondResult, "second grant claim")
	if gotFirst.err != nil || !gotFirst.created || gotFirst.claim.AttemptID != firstInput.AttemptID {
		t.Fatalf("first result = %#v", gotFirst)
	}
	if !errors.Is(gotSecond.err, ErrConflict) || gotSecond.created {
		t.Fatalf("second result = %#v", gotSecond)
	}
}

func TestIndependentDatabaseHandlesSerializeGrantAndDisable(t *testing.T) {
	t.Run("applying wins", func(t *testing.T) {
		first, second, revision, _, input := twoHandleGrantFixture(t)
		claim, _, err := first.ClaimAppAccessGrant(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		owner := AppAccessGrantClaimOwnerFor(claim)
		locked := make(chan struct{})
		release := make(chan struct{})
		first.beforeGrantTransitionCommit = func() {
			close(locked)
			<-release
		}
		transitionResult := make(chan error, 1)
		go func() {
			_, _, err := first.AdvanceAppAccessGrantClaim(context.Background(), owner,
				AppAccessGrantPrepared, AppAccessGrantApplying)
			transitionResult <- err
		}()
		waitForSignal(t, locked, "applying transition lock")
		disableResult := make(chan error, 1)
		go func() {
			_, _, err := second.ApproveAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
			disableResult <- err
		}()
		close(release)
		if err := waitForResult(t, transitionResult, "applying transition"); err != nil {
			t.Fatal(err)
		}
		if err := waitForResult(t, disableResult, "blocked disable"); err == nil {
			t.Fatal("disable won after applying held the write lock")
		}
	})

	t.Run("disable wins", func(t *testing.T) {
		first, second, revision, _, input := twoHandleGrantFixture(t)
		claim, _, err := first.ClaimAppAccessGrant(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		owner := AppAccessGrantClaimOwnerFor(claim)
		locked := make(chan struct{})
		release := make(chan struct{})
		first.afterDisableIntentLock = func() {
			close(locked)
			<-release
		}
		disableResult := make(chan error, 1)
		go func() {
			_, _, err := first.ApproveAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
			disableResult <- err
		}()
		waitForSignal(t, locked, "disable intent lock")
		transitionResult := make(chan error, 1)
		go func() {
			_, _, err := second.AdvanceAppAccessGrantClaim(context.Background(), owner,
				AppAccessGrantPrepared, AppAccessGrantApplying)
			transitionResult <- err
		}()
		close(release)
		if err := waitForResult(t, disableResult, "disable intent"); err != nil {
			t.Fatal(err)
		}
		if err := waitForResult(t, transitionResult, "blocked applying transition"); err == nil {
			t.Fatal("applying won after disable held the write lock")
		}
		retained, err := first.AppAccessGrantClaim(context.Background(), claim.AttemptID)
		if err != nil || retained.State != AppAccessGrantPrepared {
			t.Fatalf("retained prepared claim = %#v error=%v", retained, err)
		}
	})
}

func TestAppAccessGrantStartupSnapshotUsesOneReadSnapshot(t *testing.T) {
	db, repository, _, _, input := approvedGrantFixture(t)
	claim, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	reader := testRepository(db)
	writer := testRepository(db)
	idsRead := make(chan struct{})
	release := make(chan struct{})
	reader.afterGrantStartupClaimsRead = func() {
		close(idsRead)
		<-release
	}
	type result struct {
		snapshot AppAccessGrantStartupSnapshot
		err      error
	}
	readResult := make(chan result, 1)
	go func() {
		snapshot, err := reader.AppAccessGrantStartupSnapshot(context.Background())
		readResult <- result{snapshot: snapshot, err: err}
	}()
	waitForSignal(t, idsRead, "grant startup attempt IDs")
	writeResult := make(chan error, 1)
	go func() {
		_, _, err := writer.AdvanceAppAccessGrantClaim(context.Background(), AppAccessGrantClaimOwnerFor(claim),
			AppAccessGrantPrepared, AppAccessGrantApplying)
		writeResult <- err
	}()
	close(release)
	got := waitForResult(t, readResult, "grant startup snapshot")
	if err := waitForResult(t, writeResult, "concurrent grant transition"); err != nil {
		t.Fatal(err)
	}
	if got.err != nil || len(got.snapshot.Claims) != 1 ||
		got.snapshot.Claims[0].Claim.State != AppAccessGrantPrepared ||
		!got.snapshot.Claims[0].AccessHeadCurrent || !got.snapshot.Claims[0].ProfileHeadCurrent ||
		!got.snapshot.Claims[0].ApproverIsAdministrator || got.snapshot.Claims[0].DisableIntent != nil {
		t.Fatalf("startup snapshot = %#v error=%v", got.snapshot, got.err)
	}
	current, err := repository.AppAccessGrantClaim(context.Background(), claim.AttemptID)
	if err != nil || current.State != AppAccessGrantApplying {
		t.Fatalf("current claim = %#v error=%v", current, err)
	}
	combined, err := repository.HostingGatewayStartupSnapshot(context.Background())
	if err != nil || len(combined.Grants.Claims) != 1 || combined.Grants.Claims[0].Claim.State != AppAccessGrantApplying {
		t.Fatalf("combined startup = %#v error=%v", combined, err)
	}
}

func approvedGrantFixture(t *testing.T) (*sql.DB, *Repository, AppAccessRevision, GatewayProfileRevision, ClaimAppAccessGrantInput) {
	t.Helper()
	db, repository, owner := approvedReservationFixture(t)
	revision, err := repository.CurrentAppAccess(context.Background(), owner.AppID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := repository.CurrentGatewayProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return db, repository, revision, profile, ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: revision.ApprovedBy,
	}
}

func twoHandleGrantFixture(t *testing.T) (*Repository, *Repository, AppAccessRevision, GatewayProfileRevision, ClaimAppAccessGrantInput) {
	t.Helper()
	dataRoot := t.TempDir()
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	addUsers(t, db)
	first := testRepository(db)
	profile, _, err := first.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.85.10", InterfaceID: "grant-disable-race", PortStart: 8111, PortEnd: 8111}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appID := addApps(t, db, 1)[0]
	allocation, _, err := first.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := first.ApproveAppAccess(context.Background(), approvedAccessInput(t, allocation, 0))
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.Close() })
	return first, testRepository(secondDB), revision, profile, ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: revision.ApprovedBy,
	}
}
