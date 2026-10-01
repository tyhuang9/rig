package appaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestAppAccessDisableIntentRequiresExactAdministratorApprovalAndFreezesAllocation(t *testing.T) {
	db, repository, owner := approvedReservationFixture(t)
	if _, err := repository.Activate(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	revision, err := repository.CurrentAppAccess(context.Background(), owner.AppID)
	if err != nil {
		t.Fatal(err)
	}
	disableInput := approvedDisableInput(t, revision)

	viewer := disableInput
	viewer.OperationID = uuid.NewString()
	viewer.Approval.ActorID = testViewer
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), viewer); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("viewer disable approval error = %v", err)
	}
	wrongAction := disableInput
	wrongAction.OperationID = uuid.NewString()
	wrongAction.Approval.Action = ActionEnableAppAccess
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), wrongAction); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("wrong disable action error = %v", err)
	}
	wrongOwner := disableInput
	wrongOwner.OperationID = uuid.NewString()
	wrongOwner.Owner.OperationID = uuid.NewString()
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), wrongOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner disable approval error = %v", err)
	}

	intent, created, err := repository.ApproveAppAccessDisable(context.Background(), disableInput)
	if err != nil || !created || intent.Spec != AppAccessDisableSpecFor(revision) {
		t.Fatalf("disable intent = %#v created=%t error=%v", intent, created, err)
	}
	replayed, created, err := New(db).ApproveAppAccessDisable(context.Background(), disableInput)
	if err != nil || created || replayed.OperationID != intent.OperationID || replayed.RequestDigest != intent.RequestDigest {
		t.Fatalf("disable intent replay = %#v created=%t error=%v", replayed, created, err)
	}
	loaded, err := New(db).AppAccessDisableIntent(context.Background(), intent.OperationID)
	if err != nil || loaded != intent {
		t.Fatalf("loaded disable intent = %#v error=%v", loaded, err)
	}
	changed := disableInput
	changed.ExpectedRevisionNumber++
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), changed); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("changed disable replay error = %v", err)
	}
	competing := disableInput
	competing.OperationID = uuid.NewString()
	if _, _, err := repository.ApproveAppAccessDisable(context.Background(), competing); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing disable intent error = %v", err)
	}

	if _, err := repository.Activate(context.Background(), owner); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending disable idempotent activation error = %v", err)
	}
	if _, err := repository.MarkUncertain(context.Background(), owner); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending disable allocation transition error = %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET released_at=? WHERE id=?`, formatTime(testNow), owner.AllocationID); err == nil {
		t.Fatal("disable intent allowed owned allocation release")
	}
	current, err := repository.CurrentAppAccess(context.Background(), owner.AppID)
	if err != nil || current.Allocation.ReleasedAt != nil || current.Allocation.OwnerRevisionID != owner.AccessRevisionID || current.Allocation.State != AllocationActive {
		t.Fatalf("frozen active allocation = %#v error=%v", current.Allocation, err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_disable_intents SET approved_at=? WHERE operation_id=?`, formatTime(testNow), intent.OperationID); err == nil {
		t.Fatal("disable intent was mutable")
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_disable_intents WHERE operation_id=?`, intent.OperationID); err == nil {
		t.Fatal("disable intent was removable")
	}
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action=? AND resource_type='lan_app_access_disable' AND resource_id=?`, string(ActionDisableAppAccess), intent.OperationID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("disable audit rows=%d error=%v", audits, err)
	}
}

func TestUncertainAllocationDisableIntentRetainsOwnershipAndFencesRelease(t *testing.T) {
	db, repository, owner := approvedReservationFixture(t)
	if _, err := repository.MarkUncertain(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	revision, err := repository.CurrentAppAccess(context.Background(), owner.AppID)
	if err != nil {
		t.Fatal(err)
	}
	intent, created, err := repository.ApproveAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
	if err != nil || !created {
		t.Fatalf("uncertain disable intent = %#v created=%t error=%v", intent, created, err)
	}
	if _, err := repository.Activate(context.Background(), owner); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("uncertain reconciliation after disable intent error = %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET released_at=? WHERE id=?`, formatTime(testNow), owner.AllocationID); err == nil {
		t.Fatal("disable intent allowed uncertain allocation release")
	}
	revision, err = repository.CurrentAppAccess(context.Background(), owner.AppID)
	if err != nil || revision.Allocation.State != AllocationUncertain || revision.Allocation.ReleasedAt != nil || revision.Allocation.OwnerRevisionID != owner.AccessRevisionID {
		t.Fatalf("retained uncertain allocation = %#v error=%v", revision.Allocation, err)
	}
	var live int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_port_allocations WHERE id=? AND released_at IS NULL`, owner.AllocationID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("uncertain live allocation rows=%d error=%v", live, err)
	}
}

func TestIndependentDatabaseHandlesSerializeDisableIntentApproval(t *testing.T) {
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
	appID := addApps(t, firstDB, 1)[0]
	firstRepository := testRepository(firstDB)
	secondRepository := testRepository(secondDB)
	profile, _, err := firstRepository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t, GatewayProfileSpec{SelectedIPv4: "192.168.94.8", InterfaceID: "disable-contention", PortStart: 8110, PortEnd: 8110}, 0))
	if err != nil {
		t.Fatal(err)
	}
	allocation, _, err := firstRepository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0, GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := firstRepository.ApproveAppAccess(context.Background(), approvedAccessInput(t, allocation, 0))
	if err != nil {
		t.Fatal(err)
	}
	input := approvedDisableInput(t, revision)

	locked := make(chan struct{})
	release := make(chan struct{})
	firstRepository.afterDisableIntentLock = func() {
		close(locked)
		<-release
	}
	type result struct {
		intent  AppAccessDisableIntent
		created bool
		err     error
	}
	firstResult := make(chan result, 1)
	secondResult := make(chan result, 1)
	go func() {
		value, created, err := firstRepository.ApproveAppAccessDisable(context.Background(), input)
		firstResult <- result{intent: value, created: created, err: err}
	}()
	waitForSignal(t, locked, "disable intent write lock")
	go func() {
		value, created, err := secondRepository.ApproveAppAccessDisable(context.Background(), input)
		secondResult <- result{intent: value, created: created, err: err}
	}()
	second := waitForResult(t, secondResult, "contended disable intent")
	if !errors.Is(second.err, ErrConflict) {
		t.Fatalf("contended disable intent = %#v created=%t error=%v", second.intent, second.created, second.err)
	}
	close(release)
	first := waitForResult(t, firstResult, "first disable intent")
	if first.err != nil || !first.created || first.intent.OperationID != input.OperationID {
		t.Fatalf("first disable intent = %#v created=%t error=%v", first.intent, first.created, first.err)
	}
	replay, created, err := secondRepository.ApproveAppAccessDisable(context.Background(), input)
	if err != nil || created || replay.OperationID != first.intent.OperationID {
		t.Fatalf("post-contention replay = %#v created=%t error=%v", replay, created, err)
	}
}

func approvedDisableInput(t *testing.T, revision AppAccessRevision) ApproveAppAccessDisableInput {
	t.Helper()
	spec := AppAccessDisableSpecFor(revision)
	digest, err := AppAccessDisableSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return ApproveAppAccessDisableInput{
		OperationID:            uuid.NewString(),
		ExpectedRevisionNumber: revision.RevisionNumber,
		Owner: AllocationOwner{
			AllocationID: revision.Allocation.ID, AppID: revision.AppID,
			OperationID: revision.OperationID, AccessRevisionID: revision.ID,
		},
		Approval: Approval{Action: ActionDisableAppAccess, SpecDigest: digest, ActorID: testAdministrator},
	}
}
