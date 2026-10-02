package database

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLANAppAccessDisableMigrationPreservesOwnedAndReleasedAllocations(t *testing.T) {
	db := openMemoryDatabase(t)
	legacy := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "027_" {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		legacy["migrations/"+entry.Name()] = &fstest.MapFile{Data: body}
	}
	if err := migrateFS(db, legacy); err != nil {
		t.Fatalf("migrate through 026: %v", err)
	}

	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		viewer        = "12121212-1212-4212-8212-121212121212"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		ownedApp      = "44444444-4444-4444-8444-444444444444"
		ownedAlloc    = "55555555-5555-4555-8555-555555555555"
		ownedOp       = "66666666-6666-4666-8666-666666666666"
		ownedRevision = "77777777-7777-4777-8777-777777777777"
		releasedApp   = "88888888-8888-4888-8888-888888888888"
		releasedAlloc = "99999999-9999-4999-8999-999999999999"
		releasedOp    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		stamp         = "2026-09-29T15:04:05.123456789Z"
		digestOne     = "1111111111111111111111111111111111111111111111111111111111111111"
		digestTwo     = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?, 'disable-admin', 'hash', 'administrator', ?, ?),(?, 'disable-viewer', 'hash', 'viewer', ?, ?)`, []any{administrator, stamp, stamp, viewer, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?, 'owned-app', 'Owned App', 'draft', ?, ?),(?, 'released-app', 'Released App', 'draft', ?, ?)`, []any{ownedApp, stamp, stamp, releasedApp, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,interface_id,port_start,port_end,spec_digest,approved_by,approved_at) VALUES(?,1,?,?,'configure_lan_gateway','192.168.1.20','migration-adapter',8100,8102,?,?,?)`, []any{profileID, profileOp, digestOne, digestTwo, administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stamp}},
		{`INSERT INTO lan_port_allocations(id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,gateway_profile_revision_number,state,reserved_at) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{ownedAlloc, ownedApp, 8100, ownedOp, digestOne, profileID, stamp}},
		{`INSERT INTO lan_app_access_revisions(id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,spec_digest,approved_by,approved_at) VALUES(?,?,1,?,?,'enable_lan_access',?,8100,?,1,?,?,?)`, []any{ownedRevision, ownedApp, ownedOp, digestOne, ownedAlloc, profileID, digestTwo, administrator, stamp}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, []any{ownedRevision, stamp, ownedApp}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{ownedRevision, ownedAlloc}},
		{`UPDATE lan_port_allocations SET state='uncertain' WHERE id=?`, []any{ownedAlloc}},
		{`INSERT INTO lan_port_allocations(id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,gateway_profile_revision_number,state,reserved_at) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{releasedAlloc, releasedApp, 8101, releasedOp, digestTwo, profileID, stamp}},
		{`UPDATE lan_port_allocations SET released_at=? WHERE id=?`, []any{stamp, releasedAlloc}},
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed statement %d: %v", index+1, err)
		}
	}
	var allocationTableBefore string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='lan_port_allocations'`).Scan(&allocationTableBefore); err != nil {
		t.Fatal(err)
	}
	originalAllocationTriggers := []string{
		"lan_port_allocation_identity_immutable",
		"lan_port_allocation_owner_update",
		"lan_port_allocation_state_update",
		"lan_port_allocation_release_update",
		"lan_port_allocation_retain",
	}
	triggerSQLBefore := make(map[string]string, len(originalAllocationTriggers))
	for _, name := range originalAllocationTriggers {
		var statement string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&statement); err != nil {
			t.Fatalf("read original trigger %s: %v", name, err)
		}
		triggerSQLBefore[name] = statement
	}

	disableMigration, err := migrations.ReadFile("migrations/027_lan_app_access_disable_ledger.sql")
	if err != nil {
		t.Fatal(err)
	}
	legacy["migrations/027_lan_app_access_disable_ledger.sql"] = &fstest.MapFile{Data: disableMigration}
	for pass := 1; pass <= 2; pass++ {
		if err := migrateFS(db, legacy); err != nil {
			t.Fatalf("migration pass %d: %v", pass, err)
		}
	}
	var allocationTableAfter string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='lan_port_allocations'`).Scan(&allocationTableAfter); err != nil {
		t.Fatal(err)
	}
	if allocationTableAfter != allocationTableBefore {
		t.Fatal("migration 027 rebuilt or changed the migration 025 allocation table")
	}
	for _, name := range originalAllocationTriggers {
		var after string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&after); err != nil {
			t.Fatalf("read preserved trigger %s: %v", name, err)
		}
		if after != triggerSQLBefore[name] {
			t.Fatalf("migration 027 changed original allocation trigger %s", name)
		}
	}
	var appID, operationID, revisionID, state string
	var port int
	var releasedAt *string
	if err := db.QueryRow(`SELECT app_id,port,owner_operation_id,owner_revision_id,state,released_at FROM lan_port_allocations WHERE id=?`, ownedAlloc).Scan(&appID, &port, &operationID, &revisionID, &state, &releasedAt); err != nil {
		t.Fatal(err)
	}
	if appID != ownedApp || port != 8100 || operationID != ownedOp || revisionID != ownedRevision || state != "uncertain" || releasedAt != nil {
		t.Fatalf("owned allocation changed during migration: app=%q port=%d operation=%q revision=%q state=%q released=%v", appID, port, operationID, revisionID, state, releasedAt)
	}
	var releasedOwner *string
	if err := db.QueryRow(`SELECT app_id,port,owner_operation_id,owner_revision_id,state,released_at FROM lan_port_allocations WHERE id=?`, releasedAlloc).Scan(&appID, &port, &operationID, &releasedOwner, &state, &releasedAt); err != nil {
		t.Fatal(err)
	}
	if appID != releasedApp || port != 8101 || operationID != releasedOp || releasedOwner != nil || state != "reserved" || releasedAt == nil || *releasedAt != stamp {
		t.Fatalf("released allocation changed during migration: app=%q port=%d operation=%q owner=%v state=%q released=%v", appID, port, operationID, releasedOwner, state, releasedAt)
	}
	var intents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_intents`).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("invented disable intents=%d error=%v", intents, err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET released_at=? WHERE id=?`, stamp, ownedAlloc); err == nil {
		t.Fatal("migration 027 weakened the existing owned-allocation release denial")
	}
	var finalizationTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='lan_app_access_disable_finalizations'`).Scan(&finalizationTables); err != nil || finalizationTables != 0 {
		t.Fatalf("unexpected finalization tables=%d error=%v", finalizationTables, err)
	}

	const disableOperation = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	insertDisableIntent := `INSERT INTO lan_app_access_disable_intents(
		operation_id,app_id,request_digest,approval_action,allocation_id,
		allocation_owner_operation_id,access_revision_id,access_revision_number,
		allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
		spec_digest,approved_by,approved_at
	) VALUES(?,?,?,'disable_lan_access',?,?,?,?,?,?,1,?,?,?)`
	if _, err := db.Exec(insertDisableIntent,
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc", ownedApp, digestOne, ownedAlloc,
		"dddddddd-dddd-4ddd-8ddd-dddddddddddd", ownedRevision, 1, 8100,
		profileID, digestTwo, administrator, stamp); err == nil {
		t.Fatal("disable intent accepted a mismatched allocation owner")
	}
	if _, err := db.Exec(insertDisableIntent,
		"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", ownedApp, digestOne, ownedAlloc,
		ownedOp, ownedRevision, 1, 8100, profileID, digestTwo, viewer, stamp); err == nil {
		t.Fatal("disable intent accepted a non-administrator")
	}
	if _, err := db.Exec(insertDisableIntent,
		disableOperation, ownedApp, digestOne, ownedAlloc, ownedOp, ownedRevision, 1,
		8100, profileID, digestTwo, administrator, stamp); err != nil {
		t.Fatalf("insert exact disable intent: %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET state='active' WHERE id=?`, ownedAlloc); err == nil || !strings.Contains(err.Error(), "frozen by disable intent") {
		t.Fatalf("disable intent did not freeze state transition: %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET owner_revision_id=owner_revision_id WHERE id=?`, ownedAlloc); err == nil {
		t.Fatal("disable intent did not freeze owner transition")
	}
	if _, err := db.Exec(`UPDATE lan_port_allocations SET released_at=? WHERE id=?`, stamp, ownedAlloc); err == nil {
		t.Fatal("disable intent allowed owned allocation release")
	}
	var frozenState, frozenOwner string
	if err := db.QueryRow(`SELECT state,owner_revision_id FROM lan_port_allocations WHERE id=?`, ownedAlloc).Scan(&frozenState, &frozenOwner); err != nil {
		t.Fatal(err)
	}
	if frozenState != "uncertain" || frozenOwner != ownedRevision {
		t.Fatalf("disable intent changed allocation state=%q owner=%q", frozenState, frozenOwner)
	}
}
