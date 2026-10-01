package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestDisableSuccessorAckHistoricalGrant(t *testing.T) {
	for _, test := range []struct {
		name            string
		otherApp        bool
		noSource        bool
		sameAppSamePort bool
		relation        string
	}{
		{"same app new port", false, false, false, AppAccessDisableSuccessorSameAppNewPort404},
		{"same app same port", false, false, true, AppAccessDisableSuccessorSamePort},
		{"other app same port", true, false, false, AppAccessDisableSuccessorSamePort},
		{"legacy no source grant", true, true, false, AppAccessDisableSuccessorSamePort},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, repo, disable, successor, profile := historicalDisableSuccessorFixture(t, test.otherApp, test.noSource, test.sameAppSamePort)
			dropHistoricalAckViews(t, db)
			applyHistoricalMigration(t, db, "030_lan_app_access_disable_clear_acks.sql")
			var legacyAckCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_clear_acks`).Scan(&legacyAckCount); err != nil || legacyAckCount != 0 {
				t.Fatalf("030 invented exact-404 acknowledgments count=%d error=%v", legacyAckCount, err)
			}
			if err := database.Migrate(db); err != nil {
				t.Fatalf("migrate real 030 to 031: %v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_successor_acks`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("migration invented successor acknowledgments count=%d error=%v", count, err)
			}
			if _, _, err := repo.ClaimAppAccessGrant(context.Background(), ClaimAppAccessGrantInput{
				AttemptID: uuid.NewString(), Spec: successor.Spec, ActorID: testAdministrator,
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("new grant crossed historical disable fence: %v", err)
			}
			if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, uuid.NewString(), successor.Spec.AllocationID,
				test.relation, testDigest('c'), successor.Proof.ObservedAt); !errors.Is(err, ErrConflict) {
				t.Fatalf("forged successor attempt accepted: %v", err)
			}
			if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, successor.AttemptID, uuid.NewString(),
				test.relation, testDigest('c'), successor.Proof.ObservedAt); !errors.Is(err, ErrConflict) {
				t.Fatalf("forged successor allocation accepted: %v", err)
			}
			wrongRelation := AppAccessDisableSuccessorSameAppNewPort404
			if test.relation == AppAccessDisableSuccessorSameAppNewPort404 {
				wrongRelation = AppAccessDisableSuccessorSamePort
			}
			if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
				wrongRelation, testDigest('c'), successor.Proof.ObservedAt); !errors.Is(err, ErrConflict) {
				t.Fatalf("wrong relation accepted: %v", err)
			}
			if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
				test.relation, testDigest('c'), disable.Proof.ObservedAt.Add(-time.Nanosecond)); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("stale observation accepted: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO lan_app_access_disable_successor_acks(
				operation_id,gateway_operation_id,successor_attempt_id,successor_allocation_id,
				relation,final_protected_state_digest,observed_at,acknowledged_at
			) VALUES(?,?,?,?,?,?,?,?)`, disable.OperationID, disable.Proof.GatewayOperationID,
				uuid.NewString(), successor.Spec.AllocationID, test.relation, testDigest('c'),
				formatTime(successor.Proof.ObservedAt), formatTime(successor.Proof.ObservedAt)); err == nil {
				t.Fatal("SQL accepted forged successor attempt")
			}
			ack, created, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
				test.relation, testDigest('c'), successor.Proof.ObservedAt)
			if err != nil || !created || ack.SuccessorAttemptID != successor.AttemptID {
				t.Fatalf("successor ack=%#v created=%t error=%v", ack, created, err)
			}
			replayed, changed, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
				test.relation, testDigest('c'), successor.Proof.ObservedAt.Add(time.Nanosecond))
			if err != nil || changed || replayed != ack {
				t.Fatalf("ack replay=%#v changed=%t error=%v", replayed, changed, err)
			}
			if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
				test.relation, testDigest('d'), successor.Proof.ObservedAt); !errors.Is(err, ErrIdempotencyMismatch) {
				t.Fatalf("changed ack replay accepted: %v", err)
			}
			if _, _, err := repo.AcknowledgeAppAccessDisableProtectedClear(context.Background(), disable.OperationID,
				disable.Proof.GatewayOperationID, testDigest('d'), successor.Proof.ObservedAt); !errors.Is(err, ErrConflict) {
				t.Fatalf("exact-404 ack coexisted with successor ack: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO lan_app_access_disable_clear_acks(
				operation_id,proof_kind,gateway_operation_id,final_protected_state_digest,observed_at,acknowledged_at
			) VALUES(?,?,?,?,?,?)`, disable.OperationID, AppAccessDisableProofExact404,
				disable.Proof.GatewayOperationID, testDigest('d'), formatTime(successor.Proof.ObservedAt),
				formatTime(successor.Proof.ObservedAt)); err == nil {
				t.Fatal("SQL allowed both acknowledgment kinds")
			}
			if _, err := db.Exec(`UPDATE lan_app_access_disable_successor_acks SET relation=? WHERE operation_id=?`,
				wrongRelation, disable.OperationID); err == nil {
				t.Fatal("successor acknowledgment was mutable")
			}
			if _, err := db.Exec(`DELETE FROM lan_app_access_disable_successor_acks WHERE operation_id=?`,
				disable.OperationID); err == nil {
				t.Fatal("successor acknowledgment was deletable")
			}
			snapshot, err := repo.AppAccessDisableStartupSnapshot(context.Background())
			if err != nil || len(snapshot.Claims) != 1 || snapshot.Claims[0].SuccessorAck == nil ||
				snapshot.Claims[0].SuccessorAck.SuccessorAttemptID != successor.AttemptID ||
				snapshot.Claims[0].ProtectedClearAck != nil {
				t.Fatalf("startup successor ack=%#v error=%v", snapshot, err)
			}
			unusedApp := addApps(t, db, 1)[0]
			if _, created, err := repo.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
				AppID: unusedApp, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
				GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
			}); err != nil || !created {
				t.Fatalf("admission not released after successor ack created=%t error=%v", created, err)
			}
		})
	}
}

func TestDisableSuccessorAckRejectsRetiredGrantAndExistingExact404(t *testing.T) {
	t.Run("retired successor", func(t *testing.T) {
		db, repo, oldDisable, successor, _ := historicalDisableSuccessorFixture(t, true, false)
		revision, err := repo.CurrentAppAccess(context.Background(), successor.Spec.AppID)
		if err != nil {
			t.Fatal(err)
		}
		secondDisable, _, err := repo.ClaimAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
		if err != nil {
			t.Fatal(err)
		}
		owner := AppAccessDisableClaimOwnerFor(secondDisable)
		if _, _, err := repo.AdvanceAppAccessDisableClaim(context.Background(), owner,
			AppAccessDisablePrepared, AppAccessDisableWithdrawing); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.ResolveAppAccessDisableClaim(context.Background(), owner,
			AppAccessDisableWithdrawing, AppAccessDisableCommitted, AppAccessDisableProof{
				GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('b'),
			}); err != nil {
			t.Fatal(err)
		}
		dropHistoricalAckViews(t, db)
		if err := database.Migrate(db); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), oldDisable.OperationID,
			oldDisable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
			AppAccessDisableSuccessorSamePort, testDigest('c'), successor.Proof.ObservedAt); !errors.Is(err, ErrConflict) {
			t.Fatalf("retired successor accepted: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO lan_app_access_disable_successor_acks(
			operation_id,gateway_operation_id,successor_attempt_id,successor_allocation_id,
			relation,final_protected_state_digest,observed_at,acknowledged_at
		) VALUES(?,?,?,?,?,?,?,?)`, oldDisable.OperationID, oldDisable.Proof.GatewayOperationID,
			successor.AttemptID, successor.Spec.AllocationID, AppAccessDisableSuccessorSamePort,
			testDigest('c'), formatTime(successor.Proof.ObservedAt), formatTime(successor.Proof.ObservedAt)); err == nil {
			t.Fatal("SQL accepted retired successor")
		}
	})
	t.Run("existing exact 404", func(t *testing.T) {
		db, repo, oldDisable, successor, _ := historicalDisableSuccessorFixture(t, false, false)
		dropHistoricalAckViews(t, db)
		if err := database.Migrate(db); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AcknowledgeAppAccessDisableProtectedClear(context.Background(),
			oldDisable.OperationID, oldDisable.Proof.GatewayOperationID,
			testDigest('c'), successor.Proof.ObservedAt); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AcknowledgeAppAccessDisableSuccessor(context.Background(), oldDisable.OperationID,
			oldDisable.Proof.GatewayOperationID, successor.AttemptID, successor.Spec.AllocationID,
			AppAccessDisableSuccessorSameAppNewPort404, testDigest('d'), successor.Proof.ObservedAt); !errors.Is(err, ErrConflict) {
			t.Fatalf("successor ack coexisted with exact-404 ack: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO lan_app_access_disable_successor_acks(
			operation_id,gateway_operation_id,successor_attempt_id,successor_allocation_id,
			relation,final_protected_state_digest,observed_at,acknowledged_at
		) VALUES(?,?,?,?,?,?,?,?)`, oldDisable.OperationID, oldDisable.Proof.GatewayOperationID,
			successor.AttemptID, successor.Spec.AllocationID,
			AppAccessDisableSuccessorSameAppNewPort404, testDigest('d'),
			formatTime(successor.Proof.ObservedAt), formatTime(successor.Proof.ObservedAt)); err == nil {
			t.Fatal("SQL allowed successor ack after exact-404 ack")
		}
	})
}

func dropHistoricalAckViews(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, view := range []string{"lan_app_access_disable_clear_acks", "lan_app_access_disable_successor_acks"} {
		if _, err := db.Exec(`DROP VIEW temp.` + view); err != nil {
			t.Fatal(err)
		}
	}
}

func applyHistoricalMigration(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "database", "migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(string(body)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply %s: %v", name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func historicalDisableSuccessorFixture(t *testing.T, otherApp, noSource bool, sameAppSamePort ...bool) (*sql.DB, *Repository,
	AppAccessDisableClaim, AppAccessGrantClaim, GatewayProfileRevision,
) {
	t.Helper()
	db := appAccessDBThrough029(t)
	repo := testRepository(db)
	profile, _, err := repo.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.67.9", InterfaceID: "historical-successor",
			PortStart: 8100, PortEnd: 8102}, 0))
	if err != nil {
		t.Fatal(err)
	}
	apps := addApps(t, db, 2)
	allocation, _, err := repo.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: apps[0], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := repo.ApproveAppAccess(context.Background(), approvedAccessInput(t, allocation, 0))
	if err != nil {
		t.Fatal(err)
	}
	if noSource {
		if _, err := repo.Activate(context.Background(), AllocationOwner{
			AllocationID: allocation.ID, AppID: revision.AppID,
			OperationID: revision.OperationID, AccessRevisionID: revision.ID,
		}); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = commitHistoricalGrant(t, repo, revision, profile)
	}
	disable, _, err := repo.ClaimAppAccessDisable(context.Background(), approvedDisableInput(t, revision))
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessDisableClaimOwnerFor(disable)
	if _, _, err := repo.AdvanceAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisablePrepared, AppAccessDisableWithdrawing); err != nil {
		t.Fatal(err)
	}
	disable, _, err = repo.ResolveAppAccessDisableClaim(context.Background(), owner,
		AppAccessDisableWithdrawing, AppAccessDisableCommitted, AppAccessDisableProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('b'),
		})
	if err != nil || disable.Proof == nil {
		t.Fatalf("commit old disable=%#v error=%v", disable, err)
	}
	if noSource && disable.SourceGrantAttemptID != "" {
		t.Fatal("legacy disable unexpectedly linked a source grant")
	}
	repo.now = func() time.Time { return testNow.Add(time.Hour) }
	newApp, expectedRevision := apps[0], revision.RevisionNumber
	if otherApp {
		newApp, expectedRevision = apps[1], 0
	} else if len(sameAppSamePort) == 0 || !sameAppSamePort[0] {
		// Hold the retired port as a reservation while the same app is
		// deliberately reenabled on a different port. The gateway profile
		// remains pinned by a committed v2 upgrade in a real controller.
		_, _, err = repo.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
			AppID: apps[1], OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	newAllocation, _, err := repo.ReserveAppAccess(context.Background(), ReserveAppAccessInput{
		AppID: newApp, OperationID: uuid.NewString(), ExpectedRevisionNumber: expectedRevision,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !otherApp && (len(sameAppSamePort) == 0 || !sameAppSamePort[0]) && newAllocation.Port == disable.Spec.Port {
		t.Fatal("same-app successor reused the disabled port")
	}
	if !otherApp && len(sameAppSamePort) != 0 && sameAppSamePort[0] && newAllocation.Port != disable.Spec.Port {
		t.Fatal("same-app reused-port successor received a different port")
	}
	newRevision, _, err := repo.ApproveAppAccess(context.Background(), approvedAccessInput(t, newAllocation, expectedRevision))
	if err != nil {
		t.Fatal(err)
	}
	successor := commitHistoricalGrant(t, repo, newRevision, profile)
	return db, repo, disable, successor, profile
}

func commitHistoricalGrant(t *testing.T, repo *Repository, revision AppAccessRevision,
	profile GatewayProfileRevision,
) AppAccessGrantClaim {
	t.Helper()
	grant, _, err := repo.ClaimAppAccessGrant(context.Background(), ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: testAdministrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := repo.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AdvanceAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantApplying, AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	grant, _, err = repo.ResolveAppAccessGrantClaim(context.Background(), owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, AppAccessGrantProof{
			GatewayOperationID: uuid.NewString(), ProtectedStateDigest: testDigest('a'),
		})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

// Open an actual 029 database, populate its historical state, then let the
// production migrator apply 030 and 031. This preserves every old constraint.
func appAccessDBThrough029(t *testing.T) *sql.DB {
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() > "029_lan_app_access_disable_claims.sql" {
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
	// Current repository code knows about 030/031 admission acknowledgments.
	// Temporary views let it drive real 029 behavior until the fixture is built;
	// the test drops both views before production migration 030 creates tables.
	if _, err := db.Exec(`CREATE TEMP VIEW lan_app_access_disable_clear_acks AS
		SELECT operation_id FROM lan_app_access_disable_claims WHERE state='committed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TEMP VIEW lan_app_access_disable_successor_acks AS
		SELECT operation_id FROM lan_app_access_disable_claims WHERE 0`); err != nil {
		t.Fatal(err)
	}
	addUsers(t, db)
	return db
}
