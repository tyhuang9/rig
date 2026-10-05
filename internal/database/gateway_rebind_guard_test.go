package database

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestGatewayRebindGuardConsumesExactCapabilityBeforeRowVisibility(t *testing.T) {
	db := openGatewayRebindGuardProbe(t)
	exact := gatewayRebindGuardFixture()

	t.Run("exact then spent", func(t *testing.T) {
		nonce, revoke, err := ArmGatewayRebindTransitionGuard(exact)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, exact); err != nil {
			t.Fatalf("exact capability rejected: %v", err)
		}
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, exact); err == nil ||
			!strings.Contains(err.Error(), "invalid or spent") {
			t.Fatalf("spent capability replay error=%v", err)
		}
	})

	t.Run("payload mismatch consumes", func(t *testing.T) {
		nonce, revoke, err := ArmGatewayRebindTransitionGuard(exact)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		wrong := exact
		wrong.CanonicalPayload += " "
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, wrong); err == nil {
			t.Fatal("payload mismatch was accepted")
		}
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, exact); err == nil {
			t.Fatal("payload mismatch did not consume capability")
		}
	})

	t.Run("random", func(t *testing.T) {
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, strings.Repeat("f", 64), exact); err == nil {
			t.Fatal("random capability was accepted")
		}
	})

	t.Run("later trigger rollback leaves no row and no capability", func(t *testing.T) {
		rollback := exact
		rollback.CanonicalPayload = `{"rollback":true}`
		nonce, revoke, err := ArmGatewayRebindTransitionGuard(rollback)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, rollback); err == nil ||
			!strings.Contains(err.Error(), "later failure") {
			t.Fatalf("later trigger failure=%v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM gateway_rebind_guard_probe WHERE nonce=?`, nonce).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed statement row count=%d error=%v", count, err)
		}
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, rollback); err == nil {
			t.Fatal("rolled-back statement revived consumed capability")
		}
	})
}

func TestGatewayRebindGuardCancellationAndTransactionRollbackNeverReviveCapability(t *testing.T) {
	db := openGatewayRebindGuardProbe(t)
	exact := gatewayRebindGuardFixture()

	t.Run("cancelled before statement", func(t *testing.T) {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
		nonce, revoke, err := ArmGatewayRebindTransitionGuard(exact)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := insertGatewayRebindGuardProbe(ctx, conn, nonce, exact); err == nil {
			t.Fatal("cancelled statement unexpectedly succeeded")
		}
		revoke()
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, exact); err == nil {
			t.Fatal("revoked cancelled capability was accepted")
		}
	})

	t.Run("transaction rollback after consume", func(t *testing.T) {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
		nonce, revoke, err := ArmGatewayRebindTransitionGuard(exact)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		if _, err := insertGatewayRebindGuardProbe(context.Background(), conn, nonce, exact); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if _, err := insertGatewayRebindGuardProbe(context.Background(), db, nonce, exact); err == nil {
			t.Fatal("transaction rollback revived consumed capability")
		}
	})
}

type gatewayRebindGuardExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func openGatewayRebindGuardProbe(t *testing.T) *sql.DB {
	t.Helper()
	db := openMemoryDatabase(t)
	if _, err := db.Exec(`CREATE TABLE gateway_rebind_guard_probe(
		nonce TEXT PRIMARY KEY, operation_id TEXT NOT NULL, sequence INTEGER NOT NULL,
		previous_state TEXT NOT NULL, previous_sequence INTEGER NOT NULL,
		next_state TEXT NOT NULL, purpose TEXT NOT NULL, protected_record_digest TEXT NOT NULL,
		terminal_receipt_digest TEXT NOT NULL, local_attestation_digest TEXT NOT NULL,
		terminal_disposition TEXT NOT NULL, command_digest TEXT NOT NULL,
		canonical_payload TEXT NOT NULL
	);
	CREATE TRIGGER gateway_rebind_guard_probe_consume
	BEFORE INSERT ON gateway_rebind_guard_probe
	WHEN rig_gateway_rebind_consume_v1(
		NEW.nonce,NEW.operation_id,NEW.sequence,NEW.previous_state,NEW.previous_sequence,
		NEW.next_state,NEW.purpose,NEW.protected_record_digest,
		NEW.terminal_receipt_digest,NEW.local_attestation_digest,
		NEW.terminal_disposition,NEW.command_digest,NEW.canonical_payload
	)<>1
	BEGIN SELECT RAISE(ABORT,'gateway rebind capability invalid or spent'); END;
	CREATE TRIGGER gateway_rebind_guard_probe_later_failure
	AFTER INSERT ON gateway_rebind_guard_probe
	WHEN NEW.canonical_payload='{"rollback":true}'
	BEGIN SELECT RAISE(ABORT,'later failure'); END;`); err != nil {
		t.Fatal(err)
	}
	return db
}

func gatewayRebindGuardFixture() GatewayRebindTransitionGuard {
	return GatewayRebindTransitionGuard{
		OperationID: "11111111-1111-4111-8111-111111111111",
		Sequence:    2, PreviousState: "prepared", PreviousSequence: 1,
		NextState: "successor_ready", Purpose: "lan_gateway_rebind_transition",
		ProtectedRecordDigest: strings.Repeat("1", 64),
		TerminalReceiptDigest: strings.Repeat("2", 64),
		TerminalDisposition:   "commit", CommandDigest: strings.Repeat("3", 64),
		CanonicalPayload: `{"exact":true}`,
	}
}

func insertGatewayRebindGuardProbe(ctx context.Context, exec gatewayRebindGuardExecer,
	nonce string, value GatewayRebindTransitionGuard,
) (sql.Result, error) {
	return exec.ExecContext(ctx, `INSERT INTO gateway_rebind_guard_probe(
		nonce,operation_id,sequence,previous_state,previous_sequence,next_state,purpose,
		protected_record_digest,terminal_receipt_digest,local_attestation_digest,
		terminal_disposition,command_digest,canonical_payload
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, nonce, value.OperationID, value.Sequence,
		value.PreviousState, value.PreviousSequence, value.NextState, value.Purpose,
		value.ProtectedRecordDigest, value.TerminalReceiptDigest,
		value.LocalAttestationDigest, value.TerminalDisposition,
		value.CommandDigest, value.CanonicalPayload)
}
