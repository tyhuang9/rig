package database

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

const (
	relayBindingIndexMigration = "015_relay_binding_owner_removable.sql"
	relayBindingIndexName      = "relay_binding_owner_removable"
	relayBindingIndexOwner     = "read-owner"
	relayBindingIndexConn      = "read-connection"
	relayBindingIndexControl   = "81000000-0000-4000-8000-000000000001"
	relayBindingIndexTime      = "2026-08-26T12:00:00.000000000Z"
)

func TestRelayBindingOwnerRemovableIndexFreshAndUpgradePreserveData(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db := openMemoryDatabase(t)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		assertRelayBindingOwnerRemovableIndex(t, db)
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, relayBindingIndexMigration).Scan(&applied); err != nil || applied != 1 {
			t.Fatalf("fresh migration record=%d err=%v", applied, err)
		}
	})

	t.Run("upgrade", func(t *testing.T) {
		db := openMemoryDatabase(t)
		applyMigrationsThrough(t, db, "014_github_auto_deploy_resolution_reservations.sql")
		seedRelayBindingIndexHistory(t, db, 4096)
		if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		for _, name := range migrationNames(t, migrations, "migrations") {
			if name == relayBindingIndexMigration {
				break
			}
			if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`, name); err != nil {
				t.Fatalf("record %s: %v", name, err)
			}
		}
		before := relayBindingIndexCounts(t, db)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(db); err != nil {
			t.Fatalf("idempotent migration: %v", err)
		}
		if after := relayBindingIndexCounts(t, db); after != before {
			t.Fatalf("upgrade changed binding data before=%v after=%v", before, after)
		}
		assertRelayBindingOwnerRemovableIndex(t, db)
		assertRelayBindingReadModelQueryPlan(t, db)

		// The migration is rollback-safe because it adds only an index. Removing
		// the index leaves every historical and removable binding intact.
		if _, err := db.Exec(`DROP INDEX relay_binding_owner_removable`); err != nil {
			t.Fatal(err)
		}
		if afterDrop := relayBindingIndexCounts(t, db); afterDrop != before {
			t.Fatalf("index rollback changed data before=%v after=%v", before, afterDrop)
		}
		body, err := migrations.ReadFile("migrations/" + relayBindingIndexMigration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("reapply additive index: %v", err)
		}
		assertRelayBindingOwnerRemovableIndex(t, db)
	})
}

func TestRelayBindingOwnerRemovableIndexIsPartialAndOrdered(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	assertRelayBindingOwnerRemovableIndex(t, db)

	rows, err := db.Query(`PRAGMA index_xinfo('relay_binding_owner_removable')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type indexedColumn struct {
		name string
		desc int
	}
	columns := make([]indexedColumn, 0, 3)
	for rows.Next() {
		var sequence, columnID, descending, key int
		var name sql.NullString
		var collation sql.NullString
		if err = rows.Scan(&sequence, &columnID, &name, &descending, &collation, &key); err != nil {
			t.Fatal(err)
		}
		if key == 1 {
			columns = append(columns, indexedColumn{name: name.String, desc: descending})
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []indexedColumn{{name: "owner_user_id", desc: 0}, {name: "updated_at", desc: 1}, {name: "binding_id", desc: 1}}
	if fmt.Sprint(columns) != fmt.Sprint(want) {
		t.Fatalf("index columns=%v want=%v", columns, want)
	}

	var partial int
	list, err := db.Query(`PRAGMA index_list('relay_installation_bindings')`)
	if err != nil {
		t.Fatal(err)
	}
	for list.Next() {
		var sequence, unique, isPartial int
		var name, origin string
		if err = list.Scan(&sequence, &name, &unique, &origin, &isPartial); err != nil {
			list.Close()
			t.Fatal(err)
		}
		if name == relayBindingIndexName {
			partial = isPartial
		}
	}
	if err = list.Close(); err != nil {
		t.Fatal(err)
	}
	if partial != 1 {
		t.Fatalf("index partial=%d", partial)
	}
}

func assertRelayBindingOwnerRemovableIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	var statement string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, relayBindingIndexName).Scan(&statement); err != nil {
		t.Fatalf("read removable index: %v", err)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(statement)), " ")
	want := "create index relay_binding_owner_removable on relay_installation_bindings(owner_user_id, updated_at desc, binding_id desc) where state in ('authorized','access_lost','removal_pending')"
	if normalized != want {
		t.Fatalf("index definition=%q want=%q", normalized, want)
	}
}

func assertRelayBindingReadModelQueryPlan(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN
		SELECT binding_id,connection_id,installation_id,repository_id,state,updated_at
		FROM relay_installation_bindings
		WHERE owner_user_id=? AND state IN ('authorized','access_lost','removal_pending')
		ORDER BY updated_at DESC,binding_id DESC
		LIMIT 1001`, relayBindingIndexOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(strings.ToLower(detail))
		plan.WriteByte('\n')
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "using index "+relayBindingIndexName) {
		t.Fatalf("read-model query plan=%q", plan.String())
	}
	if strings.Contains(plan.String(), "temp b-tree") {
		t.Fatalf("read-model query requires temporary ordering=%q", plan.String())
	}
}

func seedRelayBindingIndexHistory(t *testing.T, db *sql.DB, terminalCount int) {
	t.Helper()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,'hash','administrator',?,?)`, relayBindingIndexOwner, relayBindingIndexOwner, relayBindingIndexTime, relayBindingIndexTime)
	mustExec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at)
		VALUES(?,?,'github','connected','42','octocat',1,?,?,?,?,?)`, relayBindingIndexConn, relayBindingIndexOwner, relayBindingIndexTime, relayBindingIndexTime, relayBindingIndexTime, relayBindingIndexTime, relayBindingIndexTime)
	mustExec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, relayBindingIndexControl, relayBindingIndexTime, relayBindingIndexTime)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := tx.Prepare(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at,completed_at)
		VALUES(?,?,?,?,?,?,'removed',?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < terminalCount; index++ {
		bindingID := fmt.Sprintf("82%06x-0000-4000-8000-%012x", index, index)
		if _, err = terminal.Exec(bindingID, relayBindingIndexOwner, relayBindingIndexConn, relayBindingIndexControl, index+1, index+1, relayBindingIndexTime, relayBindingIndexTime, relayBindingIndexTime, relayBindingIndexTime); err != nil {
			terminal.Close()
			t.Fatal(err)
		}
	}
	if err = terminal.Close(); err != nil {
		t.Fatal(err)
	}
	for index, state := range []string{"authorized", "access_lost", "removal_pending"} {
		bindingID := fmt.Sprintf("8300000%d-0000-4000-8000-%012x", index, index)
		errorCode := any(nil)
		if state == "access_lost" {
			errorCode = "source_access_lost"
		}
		if _, err = tx.Exec(`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,last_error_code,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, bindingID, relayBindingIndexOwner, relayBindingIndexConn, relayBindingIndexControl, terminalCount+index+1, terminalCount+index+1, state, relayBindingIndexTime, errorCode, relayBindingIndexTime, relayBindingIndexTime); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func relayBindingIndexCounts(t *testing.T, db *sql.DB) [2]int {
	t.Helper()
	var result [2]int
	if err := db.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN state IN ('authorized','access_lost','removal_pending') THEN 1 ELSE 0 END) FROM relay_installation_bindings WHERE owner_user_id=?`, relayBindingIndexOwner).Scan(&result[0], &result[1]); err != nil {
		t.Fatal(err)
	}
	return result
}
