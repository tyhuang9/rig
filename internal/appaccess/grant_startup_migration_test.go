package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestHostingGatewayStartupSnapshotMigratedGrantDisablePair(t *testing.T) {
	db, repository, grant, disable := migratedPreparedGrantDisableFixture(t)

	assertState := func(name string, grantState AppAccessGrantState,
		disableState AppAccessDisableState, acknowledged bool,
	) {
		t.Helper()
		snapshot, err := repository.HostingGatewayStartupSnapshot(context.Background())
		if err != nil {
			grantSnapshot, grantErr := repository.AppAccessGrantStartupSnapshot(context.Background())
			disableSnapshot, disableErr := repository.AppAccessDisableStartupSnapshot(context.Background())
			t.Fatalf("%s snapshot: %v; grants=%#v grant_error=%v disables=%#v disable_error=%v",
				name, err, grantSnapshot, grantErr, disableSnapshot, disableErr)
		}
		if len(snapshot.Grants.Claims) != 1 || len(snapshot.Disables.Claims) != 1 {
			t.Fatalf("%s census grants=%d disables=%d", name,
				len(snapshot.Grants.Claims), len(snapshot.Disables.Claims))
		}
		gotGrant := snapshot.Grants.Claims[0]
		gotDisable := snapshot.Disables.Claims[0]
		if gotGrant.Claim.State != grantState || gotGrant.DisableIntent == nil ||
			gotGrant.DisableIntent.OperationID != disable.OperationID ||
			gotDisable.Claim.State != disableState ||
			(gotDisable.ProtectedClearAck != nil) != acknowledged {
			t.Fatalf("%s grant=%#v disable=%#v", name, gotGrant, gotDisable)
		}
	}

	assertState("migration", AppAccessGrantPrepared, AppAccessDisablePrepared, false)
	if _, err := db.Exec(`UPDATE lan_port_allocations SET state='uncertain' WHERE id=?`,
		grant.Spec.AllocationID); err == nil || !strings.Contains(err.Error(), "frozen by disable intent") {
		t.Fatalf("direct state bypass was not frozen: %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET disabled_at=? WHERE id=?`,
		formatTime(testNow), grant.Spec.AllocationID); err == nil {
		t.Fatal("direct release bypassed the uncommitted disable")
	}
	grantOwner := AppAccessGrantClaimOwnerFor(grant)
	grant, changed, err := repository.ResolveAppAccessGrantClaim(context.Background(), grantOwner,
		AppAccessGrantPrepared, AppAccessGrantRolledBack, AppAccessGrantProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('a'),
		})
	if err != nil || !changed {
		t.Fatalf("roll back migrated grant changed=%t claim=%#v error=%v", changed, grant, err)
	}
	allocation, err := readAllocationByID(context.Background(), db, grant.Spec.AllocationID)
	if err != nil || allocation.State != AllocationActive || allocation.ReleasedAt != nil {
		t.Fatalf("paired rollback allocation=%#v error=%v", allocation, err)
	}
	assertState("grant rolled back", AppAccessGrantRolledBack, AppAccessDisablePrepared, false)

	disableOwner := AppAccessDisableClaimOwnerFor(disable)
	disable, changed, err = repository.AdvanceAppAccessDisableClaim(context.Background(), disableOwner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing)
	if err != nil || !changed {
		t.Fatalf("start migrated disable changed=%t claim=%#v error=%v", changed, disable, err)
	}
	assertState("disable withdrawing", AppAccessGrantRolledBack, AppAccessDisableWithdrawing, false)

	disable, changed, err = repository.ResolveAppAccessDisableClaim(context.Background(), disableOwner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, AppAccessDisableProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('b'),
		})
	if err != nil || !changed || disable.Proof == nil {
		t.Fatalf("commit migrated disable changed=%t claim=%#v error=%v", changed, disable, err)
	}
	allocation, err = readAllocationByID(context.Background(), db, grant.Spec.AllocationID)
	if err != nil || allocation.State != AllocationActive || allocation.ReleasedAt == nil ||
		!allocation.ReleasedAt.Equal(disable.Proof.ObservedAt) {
		t.Fatalf("paired disable allocation=%#v error=%v", allocation, err)
	}
	var retiredAt sql.NullString
	if err := db.QueryRow(`SELECT retired_at FROM lan_app_access_grant_claims WHERE attempt_id=?`,
		grant.AttemptID).Scan(&retiredAt); err != nil || retiredAt.Valid {
		t.Fatalf("rolled-back grant retirement=%#v error=%v", retiredAt, err)
	}
	assertState("disable committed before clear ack", AppAccessGrantRolledBack, AppAccessDisableCommitted, false)

	if _, changed, err := repository.AcknowledgeAppAccessDisableProtectedClear(context.Background(),
		disable.OperationID, disable.Proof.GatewayOperationID, testDigest('c'), disable.Proof.ObservedAt); err != nil || !changed {
		t.Fatalf("acknowledge migrated disable changed=%t error=%v", changed, err)
	}
	assertState("disable clear acknowledged", AppAccessGrantRolledBack, AppAccessDisableCommitted, true)
}

func TestLegacyPairRollbackMigrationRejectsForgedLineage(t *testing.T) {
	t.Run("forged disable claim does not receive exception", func(t *testing.T) {
		db, repository, grant, disable := migratedPreparedGrantDisableFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_app_access_disable_claim_identity_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_app_access_disable_claims SET spec_digest=? WHERE operation_id=?`,
			testDigest('e'), disable.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(),
			AppAccessGrantClaimOwnerFor(grant), AppAccessGrantPrepared, AppAccessGrantRolledBack,
			AppAccessGrantProof{GatewayOperationID: uuid.NewString(),
				ProtectedStateDigest: testDigest('f')}); err == nil {
			t.Fatal("forged disable claim received the legacy rollback exception")
		}
		retained, err := repository.AppAccessGrantClaim(context.Background(), grant.AttemptID)
		if err != nil || retained.State != AppAccessGrantPrepared {
			t.Fatalf("failed rollback retained=%#v error=%v", retained, err)
		}
		allocation, err := readAllocationByID(context.Background(), db, grant.Spec.AllocationID)
		if err != nil || allocation.State != AllocationActive || allocation.ReleasedAt != nil {
			t.Fatalf("forged-lineage allocation=%#v error=%v", allocation, err)
		}
	})

	t.Run("unrelated intent uses ordinary rollback", func(t *testing.T) {
		db, repository, grant, disable := migratedPreparedGrantDisableFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_app_access_disable_intent_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_app_access_disable_intents SET allocated_port=8101 WHERE operation_id=?`,
			disable.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(),
			AppAccessGrantClaimOwnerFor(grant), AppAccessGrantPrepared, AppAccessGrantRolledBack,
			AppAccessGrantProof{GatewayOperationID: uuid.NewString(),
				ProtectedStateDigest: testDigest('9')}); err != nil {
			t.Fatalf("ordinary rollback with unrelated intent: %v", err)
		}
		allocation, err := readAllocationByID(context.Background(), db, grant.Spec.AllocationID)
		if err != nil || allocation.State != AllocationUncertain || allocation.ReleasedAt != nil {
			t.Fatalf("unrelated-intent allocation=%#v error=%v", allocation, err)
		}
	})
}

func TestLegacyPairRollbackMigrationPreservesOrdinaryRollback(t *testing.T) {
	_, repository, revision, _, input := approvedGrantFixture(t)
	if _, err := repository.Activate(context.Background(), AllocationOwner{
		AllocationID: revision.Allocation.ID, AppID: revision.AppID,
		OperationID: revision.OperationID, AccessRevisionID: revision.ID,
	}); err != nil {
		t.Fatal(err)
	}
	grant, _, err := repository.ClaimAppAccessGrant(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(),
		AppAccessGrantClaimOwnerFor(grant), AppAccessGrantPrepared, AppAccessGrantRolledBack,
		AppAccessGrantProof{GatewayOperationID: uuid.NewString(),
			ProtectedStateDigest: testDigest('8')}); err != nil {
		t.Fatal(err)
	}
	allocation, err := readAllocationByID(context.Background(), repository.db, grant.Spec.AllocationID)
	if err != nil || allocation.State != AllocationUncertain || allocation.ReleasedAt != nil {
		t.Fatalf("ordinary rollback allocation=%#v error=%v", allocation, err)
	}
}

func TestHostingGatewayStartupSnapshotRejectsUnprovedMigratedGrantRelease(t *testing.T) {
	db, repository, grant, _ := migratedPreparedGrantDisableFixture(t)
	if _, _, err := repository.ResolveAppAccessGrantClaim(context.Background(),
		AppAccessGrantClaimOwnerFor(grant), AppAccessGrantPrepared, AppAccessGrantRolledBack,
		AppAccessGrantProof{GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('d')}); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []string{
		"lan_port_allocation_disable_intent_freeze",
		"lan_port_allocation_disable_release_guard",
	} {
		if _, err := db.Exec(`DROP TRIGGER ` + trigger); err != nil {
			t.Fatalf("drop fixture guard %s: %v", trigger, err)
		}
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET disabled_at=? WHERE id=?`,
		formatTime(testNow), grant.Spec.AllocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.HostingGatewayStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("unproved release error=%v", err)
	}
}

func migratedPreparedGrantDisableFixture(t *testing.T) (*sql.DB, *Repository,
	AppAccessGrantClaim, AppAccessDisableClaim,
) {
	t.Helper()
	db := appAccessDBThrough028(t)
	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		appID         = "44444444-4444-4444-8444-444444444444"
		allocationID  = "55555555-5555-4555-8555-555555555555"
		ownerOp       = "66666666-6666-4666-8666-666666666666"
		revisionID    = "77777777-7777-4777-8777-777777777777"
		disableOp     = "88888888-8888-4888-8888-888888888888"
		grantAttempt  = "99999999-9999-4999-8999-999999999999"
	)
	stamp := formatTime(testNow)
	profileSpec := GatewayProfileSpec{SelectedIPv4: "192.168.44.20",
		InterfaceID: "migrated-grant-disable", PortStart: 8100, PortEnd: 8101}
	profileDigest, err := GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	profileRequestDigest, err := gatewayRequestDigest(ConfigureGatewayInput{
		OperationID: profileOp, ExpectedRevisionNumber: 0, Spec: profileSpec,
		Approval: Approval{Action: ActionConfigureGateway, SpecDigest: profileDigest,
			ActorID: administrator},
	}, profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	allocation := Allocation{ID: allocationID, AppID: appID, Port: 8100,
		OwnerOperationID: ownerOp, OwnerRevisionID: revisionID,
		ReservationDigest: testDigest('1'), GatewayProfileRevisionID: profileID,
		GatewayProfileRevisionNumber: 1, State: AllocationActive, ReservedAt: testNow}
	accessDigest, err := AppAccessSpecDigest(AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	revision := AppAccessRevision{ID: revisionID, AppID: appID, RevisionNumber: 1,
		OperationID: ownerOp, SpecDigest: accessDigest, ApprovedBy: administrator,
		ApprovedAt: testNow, Allocation: allocation}
	profile := GatewayProfileRevision{ID: profileID, RevisionNumber: 1,
		OperationID: profileOp, Spec: profileSpec, SpecDigest: profileDigest,
		ApprovedBy: administrator, ApprovedAt: testNow}
	grantInput := ClaimAppAccessGrantInput{AttemptID: grantAttempt,
		Spec: AppAccessGrantSpecFor(revision, profile), ActorID: administrator}
	grantRequestDigest, err := appAccessGrantRequestDigest(grantInput)
	if err != nil {
		t.Fatal(err)
	}
	disableSpec := AppAccessDisableSpecFor(revision)
	disableSpecDigest, err := AppAccessDisableSpecDigest(disableSpec)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
			VALUES(?,'migrated-pair-admin','hash','administrator',?,?)`, []any{administrator, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'migrated-pair','Migrated Pair','draft',?,?)`, []any{appID, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
			id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
			interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		) VALUES(?,1,?,?,'configure_lan_gateway',?,?,?,?,?,?,?)`, []any{profileID, profileOp,
			profileRequestDigest, profileSpec.SelectedIPv4, profileSpec.InterfaceID, profileSpec.PortStart,
			profileSpec.PortEnd, profileDigest, administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stamp}},
		{`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{allocationID, appID, allocation.Port,
			ownerOp, allocation.ReservationDigest, profileID, stamp}},
		{`INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,1,?,?,'enable_lan_access',?,?,?,1,?,?,?)`, []any{revisionID, appID,
			ownerOp, testDigest('3'), allocationID, allocation.Port, profileID, accessDigest, administrator, stamp}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, []any{revisionID, stamp, appID}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{revisionID, allocationID}},
		{`UPDATE lan_port_allocations SET state='active' WHERE id=?`, []any{allocationID}},
		{`INSERT INTO lan_app_access_grant_claims(
			attempt_id,request_digest,approval_action,app_id,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			access_spec_digest,allocated_port,gateway_profile_revision_id,
			gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
			approved_at,created_at,state,state_sequence,updated_at
		) VALUES(?,?,'enable_lan_access',?,?,?, ?,1,?,?,?,1,?,?,?,?,'prepared',1,?)`,
			[]any{grantAttempt, grantRequestDigest, appID, allocationID, ownerOp, revisionID,
				accessDigest, allocation.Port, profileID, profileDigest, administrator, stamp, stamp, stamp}},
		{`INSERT INTO lan_app_access_disable_intents(
			operation_id,app_id,request_digest,approval_action,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,?,'disable_lan_access',?,?,?,?,?,?,1,?,?,?)`, []any{disableOp, appID,
			testDigest('4'), allocationID, ownerOp, revisionID, disableSpec.AccessRevisionNumber,
			disableSpec.Port, profileID, disableSpecDigest, administrator, stamp}},
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed migrated pair statement %d: %v", index+1, err)
		}
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("migrate prepared pair through latest: %v", err)
	}
	repository := testRepository(db)
	grant, err := repository.AppAccessGrantClaim(context.Background(), grantAttempt)
	if err != nil {
		t.Fatal(err)
	}
	disable, err := repository.AppAccessDisableClaim(context.Background(), disableOp)
	if err != nil {
		t.Fatal(err)
	}
	if disable.SourceGrantAttemptID != "" {
		t.Fatalf("migration linked prepared grant as committed source: %q", disable.SourceGrantAttemptID)
	}
	return db, repository, grant, disable
}

func appAccessDBThrough028(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join("..", "database", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() > "028_lan_app_access_grant_claims.sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("..", "database", "migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, entry.Name()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
