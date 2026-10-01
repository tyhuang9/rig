package database

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLANAppAccessDisableClaimMigrationBackfillsImmutableIntent(t *testing.T) {
	db := openMemoryDatabase(t)
	legacy := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "029_" {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		legacy["migrations/"+entry.Name()] = &fstest.MapFile{Data: body}
	}
	if err := migrateFS(db, legacy); err != nil {
		t.Fatalf("migrate through 028: %v", err)
	}

	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		appID         = "44444444-4444-4444-8444-444444444444"
		allocationID  = "55555555-5555-4555-8555-555555555555"
		ownerOp       = "66666666-6666-4666-8666-666666666666"
		revisionID    = "77777777-7777-4777-8777-777777777777"
		disableOp     = "88888888-8888-4888-8888-888888888888"
		secondAppID   = "99999999-9999-4999-8999-999999999999"
		secondAllocID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		secondOwnerOp = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		secondRevID   = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		secondDisable = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		grantAttempt  = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		stamp         = "2026-09-30T12:00:00.000000000Z"
		digestOne     = "1111111111111111111111111111111111111111111111111111111111111111"
		digestTwo     = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
			VALUES(?,'disable-claim-admin','hash','administrator',?,?)`, []any{administrator, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'disable-claim-app','Disable Claim App','draft',?,?)`, []any{appID, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
			id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
			interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		) VALUES(?,1,?,?,'configure_lan_gateway','192.168.44.20','disable-claim-migration',8100,8101,?,?,?)`, []any{profileID, profileOp, digestOne, digestTwo, administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stamp}},
		{`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{allocationID, appID, 8100, ownerOp, digestOne, profileID, stamp}},
		{`INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,1,?,?,'enable_lan_access',?,8100,?,1,?,?,?)`, []any{revisionID, appID, ownerOp, digestOne, allocationID, profileID, digestTwo, administrator, stamp}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, []any{revisionID, stamp, appID}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{revisionID, allocationID}},
		{`UPDATE lan_port_allocations SET state='active' WHERE id=?`, []any{allocationID}},
		{`INSERT INTO lan_app_access_grant_claims(
			attempt_id,request_digest,approval_action,app_id,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			access_spec_digest,allocated_port,gateway_profile_revision_id,
			gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
			approved_at,created_at,state,state_sequence,updated_at
		) VALUES(?,?,'enable_lan_access',?,?,?, ?,1,?,8100,?,1,?,?,? ,?,'prepared',1,?)`,
			[]any{grantAttempt, digestOne, appID, allocationID, ownerOp, revisionID,
				digestTwo, profileID, digestTwo, administrator, stamp, stamp, stamp}},
		{`INSERT INTO lan_app_access_disable_intents(
			operation_id,app_id,request_digest,approval_action,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,?,'disable_lan_access',?,?,?,?,8100,?,1,?,?,?)`, []any{disableOp, appID, digestOne, allocationID, ownerOp, revisionID, 1, profileID, digestTwo, administrator, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'disable-claim-app-two','Disable Claim App Two','draft',?,?)`, []any{secondAppID, stamp, stamp}},
		{`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{secondAllocID, secondAppID, 8101, secondOwnerOp, digestOne, profileID, stamp}},
		{`INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,1,?,?,'enable_lan_access',?,8101,?,1,?,?,?)`, []any{secondRevID, secondAppID, secondOwnerOp, digestOne, secondAllocID, profileID, digestTwo, administrator, stamp}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, []any{secondRevID, stamp, secondAppID}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{secondRevID, secondAllocID}},
		{`UPDATE lan_port_allocations SET state='active' WHERE id=?`, []any{secondAllocID}},
		{`INSERT INTO lan_app_access_disable_intents(
			operation_id,app_id,request_digest,approval_action,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,?,'disable_lan_access',?,?,?,?,8101,?,1,?,?,?)`,
			[]any{secondDisable, secondAppID, digestOne, secondAllocID, secondOwnerOp,
				secondRevID, 1, profileID, digestTwo, administrator, stamp}},
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed statement %d: %v", index+1, err)
		}
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate 029: %v", err)
	}
	var state, createdAt string
	var sequence int64
	if err := db.QueryRow(`SELECT state,state_sequence,created_at
		FROM lan_app_access_disable_claims WHERE operation_id=?`, disableOp).Scan(
		&state, &sequence, &createdAt); err != nil {
		t.Fatal(err)
	}
	if state != "prepared" || sequence != 1 || createdAt != stamp {
		t.Fatalf("backfilled claim state=%q sequence=%d created=%q", state, sequence, createdAt)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_claim_events
		WHERE operation_id IN (?,?) AND sequence=1 AND state='prepared'`, disableOp, secondDisable).Scan(&events); err != nil || events != 2 {
		t.Fatalf("backfilled events=%d error=%v", events, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_claims
		WHERE state='prepared'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("backfilled prepared claims=%d error=%v", events, err)
	}
	if _, err := db.Exec(`UPDATE lan_app_access_disable_claim_events SET state='uncertain'
		WHERE operation_id=? AND sequence=1`, disableOp); err == nil {
		t.Fatal("backfilled disable event was mutable")
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET disabled_at=? WHERE id=?`, stamp, allocationID); err == nil {
		t.Fatal("owned allocation was released without a committed disable proof")
	}
}
