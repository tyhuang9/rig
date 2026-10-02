package database

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLANAppAccessGrantMigrationPreservesHistoryAndFencesSQLBypass(t *testing.T) {
	db := openMemoryDatabase(t)
	legacy := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "028_" {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		legacy["migrations/"+entry.Name()] = &fstest.MapFile{Data: body}
	}
	if err := migrateFS(db, legacy); err != nil {
		t.Fatalf("migrate through 027: %v", err)
	}

	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		viewer        = "12121212-1212-4212-8212-121212121212"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		appID         = "44444444-4444-4444-8444-444444444444"
		allocationID  = "55555555-5555-4555-8555-555555555555"
		ownerOp       = "66666666-6666-4666-8666-666666666666"
		revisionID    = "77777777-7777-4777-8777-777777777777"
		attemptID     = "88888888-8888-4888-8888-888888888888"
		gatewayOp     = "99999999-9999-4999-8999-999999999999"
		stampOne      = "2026-09-29T15:04:05.123456789Z"
		stampTwo      = "2026-09-29T15:04:06.123456789Z"
		stampThree    = "2026-09-29T15:04:07.123456789Z"
		stampFour     = "2026-09-29T15:04:08.123456789Z"
		digestOne     = "1111111111111111111111111111111111111111111111111111111111111111"
		digestTwo     = "2222222222222222222222222222222222222222222222222222222222222222"
		digestThree   = "3333333333333333333333333333333333333333333333333333333333333333"
	)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES
			(?,'grant-admin','hash','administrator',?,?),(?,'grant-viewer','hash','viewer',?,?)`, []any{administrator, stampOne, stampOne, viewer, stampOne, stampOne}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'grant-app','Grant App','draft',?,?)`, []any{appID, stampOne, stampOne}},
		{`INSERT INTO lan_gateway_profile_revisions(
			id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
			interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		) VALUES(?,1,?,?,'configure_lan_gateway','192.168.88.20','grant-migration',8100,8100,?,?,?)`, []any{profileID, profileOp, digestOne, digestTwo, administrator, stampOne}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stampOne}},
		{`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{allocationID, appID, 8100, ownerOp, digestOne, profileID, stampOne}},
		{`INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,1,?,?,'enable_lan_access',?,8100,?,1,?,?,?)`, []any{revisionID, appID, ownerOp, digestOne, allocationID, profileID, digestTwo, administrator, stampOne}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, []any{revisionID, stampOne, appID}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{revisionID, allocationID}},
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed statement %d: %v", index+1, err)
		}
	}

	objects := []string{
		"lan_port_allocations", "lan_app_access_disable_intents",
		"lan_port_allocation_identity_immutable", "lan_port_allocation_owner_update",
		"lan_port_allocation_state_update", "lan_port_allocation_release_update",
		"lan_port_allocation_retain", "lan_port_allocation_disable_intent_freeze",
		"lan_app_access_disable_exact_owner_insert", "lan_app_access_disable_intent_immutable_update",
	}
	before := make(map[string]string, len(objects))
	for _, name := range objects {
		var statement string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&statement); err != nil {
			t.Fatalf("read legacy object %s: %v", name, err)
		}
		before[name] = statement
	}
	for pass := 1; pass <= 2; pass++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("migration pass %d: %v", pass, err)
		}
	}
	for _, name := range objects {
		var after string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&after); err != nil {
			t.Fatalf("read migrated object %s: %v", name, err)
		}
		if after != before[name] {
			t.Fatalf("migration 028 changed existing object %s", name)
		}
	}
	var owner, state string
	if err := db.QueryRow(`SELECT owner_revision_id,state FROM lan_port_allocations WHERE id=?`, allocationID).Scan(&owner, &state); err != nil {
		t.Fatal(err)
	}
	if owner != revisionID || state != "reserved" {
		t.Fatalf("migration changed allocation owner=%q state=%q", owner, state)
	}

	insertClaim := `INSERT INTO lan_app_access_grant_claims(
		attempt_id,request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		access_spec_digest,allocated_port,gateway_profile_revision_id,
		gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
		approved_at,created_at,state,state_sequence,updated_at
	) VALUES(?,?,'enable_lan_access',?,?,?,?,1,?,8100,?,1,?,?,?,?,'prepared',1,?)`
	if _, err := db.Exec(insertClaim, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", digestThree,
		appID, allocationID, ownerOp, revisionID, digestTwo, profileID, digestTwo,
		viewer, stampOne, stampOne, stampOne); err == nil {
		t.Fatal("grant claim accepted a non-administrator")
	}
	if _, err := db.Exec(insertClaim, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", digestThree,
		appID, allocationID, ownerOp, revisionID, digestOne, profileID, digestTwo,
		administrator, stampOne, stampOne, stampOne); err == nil {
		t.Fatal("grant claim accepted a mismatched access digest")
	}
	if _, err := db.Exec(insertClaim, attemptID, digestThree, appID, allocationID,
		ownerOp, revisionID, digestTwo, profileID, digestTwo, administrator,
		stampOne, stampOne, stampOne); err != nil {
		t.Fatalf("insert exact prepared claim: %v", err)
	}
	if _, err := db.Exec(insertClaim, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", digestThree,
		appID, allocationID, ownerOp, revisionID, digestTwo, profileID, digestTwo,
		administrator, stampOne, stampOne, stampOne); err == nil {
		t.Fatal("competing attempt acquired the same allocation")
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET state='applying',state_sequence=2,updated_at=? WHERE attempt_id=?`, stampTwo, attemptID); err != nil {
		t.Fatalf("enter applying: %v", err)
	}

	insertDisable := `INSERT INTO lan_app_access_disable_intents(
		operation_id,app_id,request_digest,approval_action,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at
	) VALUES(?,? ,?,'disable_lan_access',?,?,?,?,8100,?,1,?,?,?)`
	if _, err := db.Exec(insertDisable, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", appID,
		digestOne, allocationID, ownerOp, revisionID, 1, profileID, digestTwo,
		administrator, stampTwo); err == nil || !strings.Contains(err.Error(), "grant is in progress") {
		t.Fatalf("disable bypass while applying error = %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_heads SET updated_at=updated_at WHERE app_id=?`, appID); err == nil ||
		!strings.Contains(err.Error(), "pinned by grant claim") {
		t.Fatalf("access head bypass error = %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_gateway_profile_heads SET updated_at=updated_at WHERE singleton=1`); err == nil ||
		!strings.Contains(err.Error(), "pinned by grant claim") {
		t.Fatalf("profile head bypass error = %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET state='committed',state_sequence=3,updated_at=? WHERE attempt_id=?`, stampThree, attemptID); err == nil {
		t.Fatal("claim skipped db_active and terminal proof")
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET state='db_active',state_sequence=3,updated_at=? WHERE attempt_id=?`, stampThree, attemptID); err != nil {
		t.Fatalf("enter db_active: %v", err)
	}
	if err := db.QueryRow(`SELECT state FROM lan_port_allocations WHERE id=?`, allocationID).Scan(&state); err != nil || state != "active" {
		t.Fatalf("db_active allocation state=%q error=%v", state, err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET state='uncertain' WHERE id=?`, allocationID); err == nil ||
		!strings.Contains(err.Error(), "fenced by grant claim") {
		t.Fatalf("allocation state bypass error = %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET state='uncertain',state_sequence=4,updated_at=? WHERE attempt_id=?`, stampFour, attemptID); err != nil {
		t.Fatalf("mark uncertain: %v", err)
	}
	if err := db.QueryRow(`SELECT state FROM lan_port_allocations WHERE id=?`, allocationID).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("uncertain allocation state=%q error=%v", state, err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET state='rolled_back',state_sequence=5,updated_at=? WHERE attempt_id=?`, stampFour, attemptID); err == nil {
		t.Fatal("terminal transition accepted no protected-state proof")
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claims
		SET state='rolled_back',state_sequence=5,updated_at=?,resolution_outcome='rolled_back',
			gateway_operation_id=?,protected_state_digest=?,resolved_at=? WHERE attempt_id=?`,
		stampFour, gatewayOp, digestThree, stampFour, attemptID); err != nil {
		t.Fatalf("proved rollback: %v", err)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_grant_claim_events WHERE attempt_id=?`, attemptID).Scan(&events); err != nil || events != 5 {
		t.Fatalf("grant events=%d error=%v", events, err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_grant_claim_events SET state='prepared'
		WHERE attempt_id=? AND sequence=2`, attemptID); err == nil {
		t.Fatal("grant event history was mutable")
	}
	if _, err := db.Exec(`DELETE FROM lan_app_access_grant_claims WHERE attempt_id=?`, attemptID); err == nil {
		t.Fatal("grant claim history was deletable")
	}
	if _, err := db.Exec(insertClaim, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", digestThree,
		appID, allocationID, ownerOp, revisionID, digestTwo, profileID, digestTwo,
		administrator, stampOne, stampFour, stampFour); err != nil {
		t.Fatalf("distinct retry after proved rollback: %v", err)
	}
}
