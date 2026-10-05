package database

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const lanGatewayRebindGuardedCommitMigration = "035_lan_gateway_rebind_guarded_commit.sql"

func TestLANGatewayRebindGuardedCommitMigrationRetainsV1AndIsMirrored(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + lanGatewayRebindGuardedCommitMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", lanGatewayRebindGuardedCommitMigration))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, public) {
		t.Fatal("public and embedded migration 035 differ")
	}

	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "035_")); err != nil {
		t.Fatalf("migrate through 034: %v", err)
	}
	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		upgradeOp     = "44444444-4444-4444-8444-444444444444"
		rebindOp      = "55555555-5555-4555-8555-555555555555"
		successorID   = "66666666-6666-4666-8666-666666666666"
		successorOp   = "77777777-7777-4777-8777-777777777777"
		stamp         = "2026-10-05T12:00:00.000000000Z"
	)
	digest := func(character string) string { return strings.Repeat(character, 64) }
	for index, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		 VALUES(?,'guarded-admin','hash','administrator',?,?)`, []any{administrator, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
		 id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
		 interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		 ) VALUES(?,1,?,?,'configure_lan_gateway','192.168.80.8','guarded-source',8100,8119,?,?,?)`,
			[]any{profileID, profileOp, digest("1"), digest("2"), administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stamp}},
		{`INSERT INTO lan_gateway_upgrade_claims(
		 operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
		 profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
		 ) VALUES(?,?,'upgrade_generated_ingress',?,1,?,?,?,'prepared',1,?)`,
			[]any{upgradeOp, digest("3"), profileID, digest("2"), administrator, stamp, stamp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='committed',state_sequence=2,updated_at=? WHERE operation_id=?`, []any{stamp, upgradeOp}},
		{`INSERT INTO lan_gateway_rebind_claims(
		 operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		 predecessor_profile_revision_id,predecessor_profile_revision_number,
		 predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		 predecessor_protected_identity_digest,successor_profile_revision_id,
		 successor_profile_revision_number,successor_profile_operation_id,
		 successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		 successor_port_start,successor_port_end,successor_profile_spec_digest,
		 configure_approval_action,configure_approved_by,configure_approved_at,
		 roster_digest,roster_count,state,state_sequence,created_at,updated_at
		 ) VALUES(?,?,'rebind_lan_gateway',?,?,?,?,1,?,?,?, ?,2,?,?,'192.168.81.8',
		 'guarded-successor',8100,8119,?,'configure_lan_gateway',?,?,?,0,'prepared',1,?,?)`,
			[]any{rebindOp, digest("4"), digest("5"), administrator, stamp, profileID, digest("2"),
				upgradeOp, digest("6"), successorID, successorOp, digest("7"), digest("8"),
				administrator, stamp, digest("9"), stamp, stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed 034 row %d: %v", index+1, err)
		}
	}

	only035 := fstest.MapFS{"migrations/" + lanGatewayRebindGuardedCommitMigration: {Data: embedded}}
	if err := migrateFS(db, only035); err != nil {
		t.Fatalf("apply 035: %v", err)
	}
	var specVersion, rosterVersion, generation int
	var operationID, state string
	if err := db.QueryRow(`SELECT operation_id,state,spec_format_version,roster_format_version,
		predecessor_protected_generation FROM lan_gateway_rebind_claims WHERE operation_id=?`, rebindOp).
		Scan(&operationID, &state, &specVersion, &rosterVersion, &generation); err != nil {
		t.Fatal(err)
	}
	if operationID != rebindOp || state != "prepared" || specVersion != 1 || rosterVersion != 1 || generation != 0 {
		t.Fatalf("retained v1 row changed: op=%q state=%q spec=%d roster=%d generation=%d",
			operationID, state, specVersion, rosterVersion, generation)
	}
	var initialEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claim_events
		WHERE operation_id=? AND sequence=1 AND state='prepared'`, rebindOp).Scan(&initialEvents); err != nil || initialEvents != 1 {
		t.Fatalf("retained initial event=%d error=%v", initialEvents, err)
	}

	if _, err := db.Exec(`INSERT INTO lan_gateway_rebind_transition_commands(
		operation_id,sequence,previous_state,previous_sequence,next_state,purpose,
		protected_record_digest,terminal_receipt_digest,command_digest,created_at,
		proof_version,terminal_disposition,protected_generation,protected_phase,
		protected_record_sequence,predecessor_checkpoint_digest,source_state_version,
		source_state_revision,source_state_digest,successor_operational_state_version,
		successor_operational_state_revision,authorization_nonce,canonical_payload
	) VALUES(?,2,'prepared',1,'successor_ready','lan_gateway_rebind_transition',
		?,?,?, ?,1,'commit',1,'successor_ready',1,?,2,0,?,0,0,?,?)`,
		rebindOp, digest("a"), digest("b"), digest("c"), stamp, digest("d"), digest("e"),
		strings.Repeat("f", 64), `{"forged":true}`); err == nil {
		t.Fatal("ordinary SQL inserted a transition command without capability")
	}
	var commands int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_transition_commands`).Scan(&commands); err != nil || commands != 0 {
		t.Fatalf("forged command count=%d error=%v", commands, err)
	}
	var triggerSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger'
		AND name='lan_gateway_rebind_transition_guard'`).Scan(&triggerSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(triggerSQL, "rig_gateway_rebind_consume_v1") ||
		!strings.Contains(triggerSQL, "NEW.canonical_payload") {
		t.Fatalf("guard trigger is not payload-bound: %s", triggerSQL)
	}
}
