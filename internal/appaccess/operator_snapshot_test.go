package appaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestReadAppAccessOperatorSnapshotPendingReservation(t *testing.T) {
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.120.10", InterfaceID: "operator-pending", PortStart: 8100, PortEnd: 8100}, 0))
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

	snapshot, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), appID)
	if err != nil || snapshot.ExpectedRevisionNumber != 0 || snapshot.DesiredAccess != nil ||
		snapshot.PendingReservation == nil || snapshot.PendingReservation.Allocation != allocation ||
		snapshot.PendingReservation.ExpectedRevisionNumber != 0 || snapshot.GrantClaim != nil ||
		snapshot.DisableClaim != nil || snapshot.DisableReview != nil {
		t.Fatalf("snapshot=%#v error=%v", snapshot, err)
	}
	wantDigest, err := AppAccessSpecDigest(AppAccessSpecFor(allocation))
	if err != nil || snapshot.PendingReservation.ApprovalDigest != wantDigest {
		t.Fatalf("approval digest=%q want=%q error=%v", snapshot.PendingReservation.ApprovalDigest, wantDigest, err)
	}
}

func TestReadAppAccessOperatorSnapshotCurrentGrantAndDisableReview(t *testing.T) {
	_, repository, revision, _, grantInput := approvedGrantFixture(t)
	grant, _, err := repository.ClaimAppAccessGrant(context.Background(), grantInput)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), revision.AppID)
	if err != nil || snapshot.ExpectedRevisionNumber != revision.RevisionNumber ||
		snapshot.DesiredAccess == nil || *snapshot.DesiredAccess != revision ||
		snapshot.PendingReservation != nil || snapshot.GrantClaim == nil ||
		snapshot.GrantClaim.AttemptID != grant.AttemptID || snapshot.DisableClaim != nil ||
		snapshot.DisableReview == nil {
		t.Fatalf("snapshot=%#v error=%v", snapshot, err)
	}
	wantSpec := AppAccessDisableSpecFor(revision)
	wantDigest, err := AppAccessDisableSpecDigest(wantSpec)
	if err != nil || snapshot.DisableReview.Spec != wantSpec || snapshot.DisableReview.ApprovalDigest != wantDigest {
		t.Fatalf("disable review=%#v wantSpec=%#v wantDigest=%q error=%v", snapshot.DisableReview, wantSpec, wantDigest, err)
	}
}

func TestReadAppAccessOperatorSnapshotDisableClaimLifecycle(t *testing.T) {
	t.Run("pending disable suppresses review", func(t *testing.T) {
		_, repository, revision, _, _ := approvedGrantFixture(t)
		claim, _, err := repository.ClaimAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
		if err != nil {
			t.Fatal(err)
		}

		snapshot, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), revision.AppID)
		if err != nil || snapshot.DesiredAccess == nil || snapshot.DisableClaim == nil ||
			snapshot.DisableClaim.OperationID != claim.OperationID || snapshot.DisableReview != nil ||
			snapshot.PendingReservation != nil {
			t.Fatalf("snapshot=%#v error=%v", snapshot, err)
		}
	})

	t.Run("committed disable remains exact after release", func(t *testing.T) {
		_, repository, revision, _, _ := approvedGrantFixture(t)
		claim := commitOperatorSnapshotDisable(t, repository, revision)

		stored, err := repository.AppAccessDisableClaim(context.Background(), claim.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		authorized, err := readAppAccessDisableAuthorization(context.Background(), repository.db, stored)
		if err != nil || authorized.Claim.OperationID != claim.OperationID || authorized.Allocation.ReleasedAt == nil {
			t.Fatalf("historical authorization=%#v error=%v", authorized, err)
		}
		mismatched := stored
		mismatched.Spec.OwnerOperationID = uuid.NewString()
		if _, err := readAppAccessDisableAuthorization(context.Background(), repository.db, mismatched); err == nil {
			t.Fatal("mismatched committed disable lineage was accepted")
		}

		snapshot, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), revision.AppID)
		if err != nil || snapshot.ExpectedRevisionNumber != revision.RevisionNumber ||
			snapshot.DesiredAccess != nil || snapshot.PendingReservation != nil ||
			snapshot.GrantClaim != nil || snapshot.DisableClaim == nil ||
			snapshot.DisableClaim.OperationID != claim.OperationID ||
			snapshot.DisableClaim.State != AppAccessDisableCommitted || snapshot.DisableReview != nil {
			t.Fatalf("snapshot=%#v error=%v", snapshot, err)
		}
	})
}

func TestReadAppAccessOperatorSnapshotReleasedHeadWithNewReservation(t *testing.T) {
	_, repository, revision, profile, _ := approvedGrantFixture(t)
	claim := commitOperatorSnapshotDisable(t, repository, revision)
	if _, _, err := repository.AcknowledgeAppAccessDisableProtectedClear(context.Background(), claim.OperationID,
		claim.Proof.GatewayOperationID, testDigest('f'), claim.Proof.ObservedAt); err != nil {
		t.Fatal(err)
	}
	allocation, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: revision.AppID, OperationID: uuid.NewString(), ExpectedRevisionNumber: revision.RevisionNumber,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), revision.AppID)
	if err != nil || snapshot.ExpectedRevisionNumber != revision.RevisionNumber ||
		snapshot.DesiredAccess != nil || snapshot.PendingReservation == nil ||
		snapshot.PendingReservation.Allocation.ID != allocation.ID ||
		snapshot.PendingReservation.ExpectedRevisionNumber != revision.RevisionNumber ||
		snapshot.GrantClaim != nil || snapshot.DisableClaim == nil ||
		snapshot.DisableClaim.OperationID != claim.OperationID || snapshot.DisableReview != nil {
		t.Fatalf("snapshot=%#v error=%v", snapshot, err)
	}
}

func TestReadAppAccessOperatorSnapshotRejectsCorruptOrAmbiguousState(t *testing.T) {
	t.Run("invalid current reservation digest", func(t *testing.T) {
		db := appAccessDB(t)
		repository := testRepository(db)
		profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
			GatewayProfileSpec{SelectedIPv4: "192.168.121.10", InterfaceID: "operator-corrupt-reservation", PortStart: 8100, PortEnd: 8100}, 0))
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
		if _, err := db.Exec(`DROP TRIGGER lan_port_allocation_identity_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_port_allocations SET reservation_digest=? WHERE id=?`, testDigest('0'), allocation.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), appID); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("corrupt reservation error=%v", err)
		}
	})

	t.Run("multiple live reservations", func(t *testing.T) {
		db := appAccessDB(t)
		repository := testRepository(db)
		profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
			GatewayProfileSpec{SelectedIPv4: "192.168.122.10", InterfaceID: "operator-ambiguous-reservation", PortStart: 8100, PortEnd: 8101}, 0))
		if err != nil {
			t.Fatal(err)
		}
		appID := addApps(t, db, 1)[0]
		if _, _, err := repository.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
			AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP INDEX lan_port_allocations_live_app`); err != nil {
			t.Fatal(err)
		}
		secondInput := ReserveAppAccessInput{AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber}
		reservationDigest, err := reservationRequestDigest(secondInput)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,?,'reserved',?)`, uuid.NewString(), appID, 8101, secondInput.OperationID,
			reservationDigest, profile.ID, profile.RevisionNumber, formatTime(testNow)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ReadAppAccessOperatorSnapshot(context.Background(), appID); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("ambiguous reservation error=%v", err)
		}
	})
}

func TestReadAppAccessOperatorSnapshotUsesOneReadSnapshot(t *testing.T) {
	dataRoot := t.TempDir()
	readerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })
	writerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })
	addUsers(t, readerDB)
	reader := testRepository(readerDB)
	writer := testRepository(writerDB)
	profile, _, err := writer.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.123.10", InterfaceID: "operator-read-snapshot", PortStart: 8100, PortEnd: 8100}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appID := addApps(t, writerDB, 1)[0]

	headRead := make(chan struct{})
	release := make(chan struct{})
	reader.afterOperatorSnapshotHeadRead = func() {
		select {
		case <-headRead:
			return
		default:
			close(headRead)
			<-release
		}
	}
	type result struct {
		snapshot AppAccessOperatorSnapshot
		err      error
	}
	readResult := make(chan result, 1)
	go func() {
		snapshot, err := reader.ReadAppAccessOperatorSnapshot(context.Background(), appID)
		readResult <- result{snapshot: snapshot, err: err}
	}()
	waitForSignal(t, headRead, "operator snapshot head")
	allocation, created, err := writer.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil || !created {
		t.Fatalf("concurrent reservation=%#v created=%t error=%v", allocation, created, err)
	}
	close(release)
	got := waitForResult(t, readResult, "operator snapshot")
	if got.err != nil || got.snapshot.ExpectedRevisionNumber != 0 || got.snapshot.PendingReservation != nil ||
		got.snapshot.DesiredAccess != nil {
		t.Fatalf("read snapshot=%#v error=%v", got.snapshot, got.err)
	}
	fresh, err := reader.ReadAppAccessOperatorSnapshot(context.Background(), appID)
	if err != nil || fresh.PendingReservation == nil || fresh.PendingReservation.Allocation.ID != allocation.ID {
		t.Fatalf("fresh snapshot=%#v error=%v", fresh, err)
	}
}

func commitOperatorSnapshotDisable(t *testing.T, repository *Repository, revision AppAccessRevision) AppAccessDisableClaim {
	t.Helper()
	claim, _, err := repository.ClaimAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessDisableClaimOwnerFor(claim)
	if _, _, err := repository.AdvanceAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); err != nil {
		t.Fatal(err)
	}
	claim, _, err = repository.ResolveAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, AppAccessDisableProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('e'),
		})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
