package database

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLANGatewayRebindLineageMigrationAddsDormantUnreleasableFence(t *testing.T) {
	db := openMemoryDatabase(t)
	through032 := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "033_" {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		through032["migrations/"+entry.Name()] = &fstest.MapFile{Data: body}
	}
	if err := migrateFS(db, through032); err != nil {
		t.Fatalf("migrate through populated 032 base: %v", err)
	}

	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		viewer        = "22222222-2222-4222-8222-222222222222"
		profileID     = "33333333-3333-4333-8333-333333333333"
		profileOp     = "44444444-4444-4444-8444-444444444444"
		upgradeOp     = "55555555-5555-4555-8555-555555555555"
		rebindOp      = "66666666-6666-4666-8666-666666666666"
		successorID   = "77777777-7777-4777-8777-777777777777"
		successorOp   = "88888888-8888-4888-8888-888888888888"
		appID         = "99999999-9999-4999-8999-999999999999"
		stamp         = "2026-10-03T12:00:00.000000000Z"
		digestOne     = "1111111111111111111111111111111111111111111111111111111111111111"
		digestTwo     = "2222222222222222222222222222222222222222222222222222222222222222"
		digestThree   = "3333333333333333333333333333333333333333333333333333333333333333"
		digestFour    = "4444444444444444444444444444444444444444444444444444444444444444"
	)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES
			(?,'rebind-admin','hash','administrator',?,?),
			(?,'rebind-viewer','hash','viewer',?,?)`, []any{administrator, stamp, stamp, viewer, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'rebind-app','Rebind App','draft',?,?)`, []any{appID, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
			id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
			interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		) VALUES(?,1,?,?,'configure_lan_gateway','192.168.70.8','rebind-adapter',8100,8119,?,?,?)`,
			[]any{profileID, profileOp, digestOne, digestTwo, administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stamp}},
		{`INSERT INTO lan_gateway_upgrade_claims(
			operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
			profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
		) VALUES(?,?,'upgrade_generated_ingress',?,1,?,?,?,'prepared',1,?)`,
			[]any{upgradeOp, digestOne, profileID, digestTwo, administrator, stamp, stamp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='serving',state_sequence=2,updated_at=? WHERE operation_id=?`, []any{stamp, upgradeOp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='committed',state_sequence=3,updated_at=? WHERE operation_id=?`, []any{stamp, upgradeOp}},
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed 032 statement %d: %v", index+1, err)
		}
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("apply 033: %v", err)
	}

	insertClaim := `INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,successor_profile_revision_id,
		successor_profile_revision_number,successor_profile_operation_id,
		successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		successor_port_start,successor_port_end,successor_profile_spec_digest,
		configure_approval_action,configure_approved_by,configure_approved_at,
		roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) VALUES(?,?,'rebind_lan_gateway',?,?,?,?,1,?,?,?, ?,2,?,?,'192.168.71.8',
		'rebind-successor',8100,8119,?,'configure_lan_gateway',?,?,?,0,'prepared',1,?,?)`
	claimArgs := func(configureActor string) []any {
		return []any{
			rebindOp, digestThree, digestFour, administrator, stamp, profileID, digestTwo,
			upgradeOp, digestThree, successorID, successorOp, digestFour, digestThree,
			configureActor, stamp, digestFour, stamp, stamp,
		}
	}
	wrongPredecessorArgs := claimArgs(administrator)
	wrongPredecessorArgs[7] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := db.Exec(insertClaim, wrongPredecessorArgs...); err == nil ||
		!strings.Contains(err.Error(), "committed current predecessor") {
		t.Fatalf("wrong predecessor error = %v", err)
	}
	if _, err := db.Exec(insertClaim, claimArgs(viewer)...); err == nil ||
		!strings.Contains(err.Error(), "both administrator approvals") {
		t.Fatalf("non-administrator nested configure approval error = %v", err)
	}
	if _, err := db.Exec(insertClaim, claimArgs(administrator)...); err != nil {
		t.Fatalf("insert exact dormant claim: %v", err)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claim_events
		WHERE operation_id=? AND sequence=1 AND state='prepared'`, rebindOp).Scan(&events); err != nil || events != 1 {
		t.Fatalf("initial event rows=%d error=%v", events, err)
	}

	assertFence := func(name, query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err == nil || !strings.Contains(err.Error(), "rebind fence is active") {
			t.Fatalf("%s bypass error = %v", name, err)
		}
	}
	assertFence("reservation", `INSERT INTO lan_port_allocations(
		id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
		gateway_profile_revision_number,state,reserved_at
	) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',?,8100,
		'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',?,?,1,'reserved',?)`, appID, digestOne, profileID, stamp)
	assertFence("access approval", `INSERT INTO lan_app_access_revisions(
		id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at
	) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',?,1,
		'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',?,'enable_lan_access',
		'cccccccc-cccc-4ccc-8ccc-cccccccccccc',8100,?,1,?,?,?)`,
		appID, digestOne, profileID, digestTwo, administrator, stamp)
	assertFence("grant", `INSERT INTO lan_app_access_grant_claims(
		attempt_id,request_digest,approval_action,app_id,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		access_spec_digest,allocated_port,gateway_profile_revision_id,
		gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
		approved_at,created_at,state,state_sequence,updated_at
	) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',?,'enable_lan_access',?,
		'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb','cccccccc-cccc-4ccc-8ccc-cccccccccccc',
		'dddddddd-dddd-4ddd-8ddd-dddddddddddd',1,?,8100,?,1,?,?,?,?,'prepared',1,?)`,
		digestOne, appID, digestTwo, profileID, digestTwo, administrator, stamp, stamp, stamp)
	assertFence("disable intent", `INSERT INTO lan_app_access_disable_intents(
		operation_id,app_id,request_digest,approval_action,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at
	) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',?,?,'disable_lan_access',
		'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb','cccccccc-cccc-4ccc-8ccc-cccccccccccc',
		'dddddddd-dddd-4ddd-8ddd-dddddddddddd',1,8100,?,1,?,?,?)`,
		appID, digestOne, profileID, digestTwo, administrator, stamp)
	assertFence("profile insert", `INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
		interface_id,port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,2,?,?,'configure_lan_gateway','192.168.71.8','blocked',8100,8119,?,?,?)`,
		successorID, successorOp, digestOne, digestTwo, administrator, stamp)
	assertFence("profile head", `UPDATE lan_gateway_profile_heads SET updated_at=updated_at WHERE singleton=1`)
	assertFence("active deployment head", `UPDATE generated_runtime_active_heads SET generation=generation WHERE app_id=?`, appID)
	assertFence("new application head inserts", `INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','rebind-new-app','Rebind New App','draft',?,?)`, stamp, stamp)
	assertFence("access head delete", `DELETE FROM lan_app_access_heads WHERE app_id=?`, appID)
	assertFence("profile head delete", `DELETE FROM lan_gateway_profile_heads WHERE singleton=1`)
	assertFence("active deployment head delete", `DELETE FROM generated_runtime_active_heads WHERE app_id=?`, appID)

	if _, err := db.Exec(`UPDATE lan_gateway_rebind_claims SET updated_at=updated_at WHERE operation_id=?`, rebindOp); err == nil {
		t.Fatal("dormant claim was mutable")
	}
	if _, err := db.Exec(`DELETE FROM lan_gateway_rebind_claims WHERE operation_id=?`, rebindOp); err == nil {
		t.Fatal("dormant claim was deletable")
	}
	if _, err := db.Exec(`UPDATE lan_gateway_rebind_claim_events SET created_at=created_at WHERE operation_id=?`, rebindOp); err == nil {
		t.Fatal("initial event was mutable")
	}
	for _, forbidden := range []string{"lan_gateway_rebind_allocation_transfers", "lan_gateway_rebind_fence_releases"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, forbidden).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unsafe 033 surface %s count=%d error=%v", forbidden, count, err)
		}
	}
	if _, err := db.Exec(insertClaim, claimArgs(administrator)...); err == nil {
		t.Fatal("a second dormant singleton claim was accepted")
	}
}
