package database

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
)

func TestRelayControllerKeyIOLeaseMigrationFreshAndStatefulUpgrade(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T) *sql.DB
	}{
		{name: "fresh", prepare: func(t *testing.T) *sql.DB { return openMemoryDatabase(t) }},
		{name: "stateful upgrade from 010", prepare: openDatabaseAtRelayMigration010WithState},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := test.prepare(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			assertRelayControllerKeyIOLeaseSchema(t, db)
			if test.name == "stateful upgrade from 010" {
				for query, want := range map[string]int{
					`SELECT COUNT(*) FROM relay_controller_keys`:          3,
					`SELECT COUNT(*) FROM relay_key_rotations`:            1,
					`SELECT COUNT(*) FROM relay_controller_session_state`: 1,
					`SELECT COUNT(*) FROM relay_outbound_commands`:        1,
				} {
					var got int
					if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
						t.Fatalf("upgrade state query=%q got=%d want=%d err=%v", query, got, want, err)
					}
				}
			}
		})
	}
}

func openDatabaseAtRelayMigration010WithState(t *testing.T) *sql.DB {
	t.Helper()
	db := openDatabaseAtRelayMigration009(t)
	body, err := migrations.ReadFile("migrations/010_relay_controller_key_cleanup.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES('010_relay_controller_key_cleanup.sql',datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	now := "2026-08-25T12:00:00Z"
	controllerID := "11111111-1111-4111-8111-111111111111"
	oldKeyID := "22222222-2222-4222-8222-222222222222"
	newKeyID := "33333333-3333-4333-8333-333333333333"
	revokedKeyID := "44444444-4444-4444-8444-444444444444"
	rotationID := "55555555-5555-4555-8555-555555555555"
	messageID := "66666666-6666-4666-8666-666666666666"
	if _, err = db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?)`, oldKeyID, controllerID, bytes.Repeat([]byte{1}, 32), "relay/controllers/"+controllerID+"/keys/"+oldKeyID+".key", now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at) VALUES(?,?,?,'ed25519','pending',?,?,?)`, newKeyID, controllerID, bytes.Repeat([]byte{2}, 32), "relay/controllers/"+controllerID+"/keys/"+newKeyID+".key", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,revoked_at) VALUES(?,?,?,'ed25519','revoked',?,?,?,?)`, revokedKeyID, controllerID, bytes.Repeat([]byte{3}, 32), "relay/controllers/"+controllerID+"/keys/"+revokedKeyID+".key", now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,created_at,updated_at) VALUES(?,?,?,?, 'prepare','2026-08-25T13:00:00Z',?,?,?)`, rotationID, controllerID, oldKeyID, newKeyID, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE relay_key_rotations SET state='propose',state_changed_at=?,updated_at=? WHERE rotation_id=?`, now, now, rotationID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,rotation_id,stage,sent_at,canonical_digest,state) VALUES(?,?,'key.rotation.propose',?,'propose',?,?,'prepared')`, controllerID, messageID, rotationID, now, bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,key_id,attempt,last_ready_at,state_changed_at,updated_at) VALUES(?,1,1,'ready',?,0,?,?,?)`, controllerID, oldKeyID, now, now, now); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertRelayControllerKeyIOLeaseSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	controllerID := "11111111-1111-4111-8111-111111111111"
	oldKeyID := "22222222-2222-4222-8222-222222222222"
	now := "2026-08-25T12:00:00Z"
	var controllers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relay_controllers`).Scan(&controllers); err != nil {
		t.Fatal(err)
	}
	if controllers == 0 {
		if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?)`, oldKeyID, controllerID, bytes.Repeat([]byte{1}, 32), "relay/controllers/"+controllerID+"/keys/"+oldKeyID+".key", now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	leaseID := "77777777-7777-4777-8777-777777777777"
	keyID := "88888888-8888-4888-8888-888888888888"
	rotationID := "99999999-9999-4999-8999-999999999999"
	ref := "relay/controllers/" + controllerID + "/keys/" + keyID + ".key"
	if _, err := db.Exec(`INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,rotation_id,old_key_id,public_key,protected_key_ref,created_at,updated_at) VALUES('1/controller/' || ?,?,?,'write','active',1,'2026-08-25T12:05:00Z',?,?,?,?,?,?,?)`, controllerID, controllerID, leaseID, keyID, rotationID, oldKeyID, bytes.Repeat([]byte{8}, 32), ref, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE relay_controller_key_io_leases SET lease_expires_at='2026-08-25T12:06:00Z',updated_at='2026-08-25T12:01:00Z' WHERE controller_id=?`, controllerID); err == nil {
		t.Fatal("same-fence lease extension was accepted")
	}
	if _, err := db.Exec(`UPDATE relay_controller_key_io_leases SET lease_id='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',phase='recovery',fence=2,lease_expires_at='2026-08-25T12:06:00Z',updated_at='2026-08-25T12:01:00Z' WHERE controller_id=?`, controllerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM relay_controller_key_io_leases WHERE controller_id=?`, controllerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,protected_key_ref,created_at,updated_at) VALUES('1/controller/' || ?,?,?,'revoked_cleanup','recovery',1,'2026-08-25T12:05:00Z',?,?,?,?)`, controllerID, controllerID, leaseID, keyID, ref, now, now); err != nil {
		t.Fatalf("distinct revoked cleanup lease was rejected: %v", err)
	}
	var operation string
	if err := db.QueryRow(`SELECT operation FROM relay_controller_key_io_leases WHERE controller_id=?`, controllerID).Scan(&operation); err != nil || operation != "revoked_cleanup" {
		t.Fatalf("durable revoked cleanup operation=%q err=%v", operation, err)
	}
	if _, err := db.Exec(`DELETE FROM relay_controller_key_io_leases WHERE controller_id=?`, controllerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,artifact_name,created_at,updated_at) VALUES('1/controller/' || ?,?,?,'temp_cleanup','recovery',1,'2026-08-25T12:05:00Z','../outside',?,?)`, controllerID, controllerID, leaseID, now, now); err == nil {
		t.Fatal("unsafe temporary artifact name was accepted")
	}
	prospectiveControllerID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	prospectiveKeyID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	prospectiveRef := "relay/controllers/" + prospectiveControllerID + "/keys/" + prospectiveKeyID + ".key"
	if _, err := db.Exec(`INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,public_key,protected_key_ref,created_at,updated_at) VALUES('0/identity',?,?,'identity_write','active',1,'2026-08-25T12:05:00Z',?,?,?, ?,?)`, prospectiveControllerID, leaseID, prospectiveKeyID, bytes.Repeat([]byte{9}, 32), prospectiveRef, now, now); err != nil {
		t.Fatalf("identity lease for prospective controller requires preexisting metadata: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,public_key,protected_key_ref,created_at,updated_at) VALUES('0/identity','dddddddd-dddd-4ddd-8ddd-dddddddddddd','aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','identity_write','active',1,'2026-08-25T12:05:00Z','eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee',?, 'relay/controllers/dddddddd-dddd-4ddd-8ddd-dddddddddddd/keys/eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee.key',?,?)`, bytes.Repeat([]byte{10}, 32), now, now); err == nil {
		t.Fatal("singleton identity-write scope accepted a second creator")
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_key_io_leases(scope_key,controller_id,lease_id,operation,phase,fence,lease_expires_at,key_id,protected_key_ref,created_at,updated_at) VALUES('1/controller/' || ?,?,'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','key_cleanup','recovery',1,'2026-08-25T12:05:00Z',?,?,?,?)`, prospectiveControllerID, prospectiveControllerID, prospectiveKeyID, prospectiveRef, now, now); err == nil {
		t.Fatal("prospective controller cleanup was not blocked by identity writer")
	}
	if _, err := db.Exec(`DELETE FROM relay_controller_key_io_leases WHERE scope_key='0/identity'`); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ column, index string }{{"old_key_id", "relay_key_rotation_old_reference"}, {"new_key_id", "relay_key_rotation_new_reference"}} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT 1 FROM relay_key_rotations WHERE controller_id=? AND `+test.column+`=? AND state='completed'`, controllerID, oldKeyID)
		if err != nil {
			t.Fatal(err)
		}
		var details strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details.WriteString(strings.ToLower(detail))
		}
		rows.Close()
		if !strings.Contains(details.String(), test.index) {
			t.Fatalf("%s lookup plan = %q", test.column, details.String())
		}
	}

	var preservedKeys, preservedRotations, preservedSessions, preservedCommands int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM relay_controller_keys`:          &preservedKeys,
		`SELECT COUNT(*) FROM relay_key_rotations`:            &preservedRotations,
		`SELECT COUNT(*) FROM relay_controller_session_state`: &preservedSessions,
		`SELECT COUNT(*) FROM relay_outbound_commands`:        &preservedCommands,
	} {
		if err := db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if preservedKeys < 1 || preservedRotations < 0 || preservedSessions < 0 || preservedCommands < 0 {
		t.Fatalf("state was not preserved keys=%d rotations=%d sessions=%d commands=%d", preservedKeys, preservedRotations, preservedSessions, preservedCommands)
	}
}
