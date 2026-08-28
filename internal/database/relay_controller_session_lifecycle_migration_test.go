package database

import (
	"bytes"
	"database/sql"
	"errors"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestRelayControllerSessionLifecycleMigrationFreshAndUpgrade(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T) *sql.DB
	}{
		{name: "fresh", prepare: func(t *testing.T) *sql.DB { return openMemoryDatabase(t) }},
		{name: "upgrade from 011", prepare: openDatabaseAtRelayMigration011WithState},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := test.prepare(t)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			if test.name == "upgrade from 011" {
				var epoch, fence int
				var state, changed, updated string
				if err := db.QueryRow(`SELECT epoch,fence,state,state_changed_at,updated_at FROM relay_controller_session_state WHERE controller_id='11111111-1111-4111-8111-111111111111'`).Scan(&epoch, &fence, &state, &changed, &updated); err != nil || epoch != 1 || fence != 2 || state != "disconnected" || changed != "2026-08-25T12:01:00Z" || updated != "2026-08-25T12:01:00Z" {
					t.Fatalf("011 state was not preserved epoch=%d fence=%d state=%q changed=%q updated=%q err=%v", epoch, fence, state, changed, updated, err)
				}
			}
			assertRelaySessionLifecycleTransition(t, db)
		})
	}
}

func openDatabaseAtRelayMigration011WithState(t *testing.T) *sql.DB {
	t.Helper()
	db := openDatabaseAtRelayMigration010WithState(t)
	body, err := migrations.ReadFile("migrations/011_relay_controller_key_io_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES('011_relay_controller_key_io_leases.sql',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE relay_controller_session_state SET state='disconnected',key_id=NULL,fence=2,updated_at='2026-08-25T12:01:00Z',state_changed_at='2026-08-25T12:01:00Z' WHERE controller_id='11111111-1111-4111-8111-111111111111'`); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertRelaySessionLifecycleTransition(t *testing.T, db *sql.DB) {
	t.Helper()
	const controllerID = "11111111-1111-4111-8111-111111111111"
	const now = "2026-08-25T12:01:00Z"
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM relay_controllers WHERE controller_id=?`, controllerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,attempt,state_changed_at,updated_at) VALUES(?,1,1,'disconnected',0,?,?)`, controllerID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES('22222222-2222-4222-8222-222222222222',?,?,'ed25519','active',?,?,?,?,?)`, controllerID, bytes.Repeat([]byte{1}, 32), "relay/controllers/"+controllerID+"/keys/22222222-2222-4222-8222-222222222222.key", now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE relay_controller_session_state SET state='needs_attention',last_error_code='rotation_failed',fence=fence+1,state_changed_at=?,updated_at=? WHERE controller_id=?`, now, now, controllerID); err != nil {
		t.Fatalf("disconnected recovery failure was rejected: %v", err)
	}
	var beforeEpoch, beforeFence int
	var beforeState, beforeKey, beforeError string
	if err := db.QueryRow(`SELECT epoch,fence,state,COALESCE(key_id,''),COALESCE(last_error_code,'') FROM relay_controller_session_state WHERE controller_id=?`, controllerID).Scan(&beforeEpoch, &beforeFence, &beforeState, &beforeKey, &beforeError); err != nil {
		t.Fatal(err)
	}
	assertRelaySessionLifecycleConstraint(t, db, controllerID, beforeEpoch, beforeFence, beforeState, beforeKey, beforeError,
		`UPDATE relay_controller_session_state SET state='ready',key_id='22222222-2222-4222-8222-222222222222',fence=fence+1,last_error_code=NULL,last_ready_at=?,last_seen_at=?,state_changed_at=?,updated_at=? WHERE controller_id=?`, now, now, now, now, controllerID)
	assertRelaySessionLifecycleConstraint(t, db, controllerID, beforeEpoch, beforeFence, beforeState, beforeKey, beforeError,
		`UPDATE relay_controller_session_state SET epoch=epoch+1,state='ready',key_id='22222222-2222-4222-8222-222222222222',fence=1,last_error_code=NULL,last_ready_at=?,last_seen_at=?,state_changed_at=?,updated_at=? WHERE controller_id=?`, now, now, now, now, controllerID)
	assertRelaySessionLifecycleConstraint(t, db, controllerID, beforeEpoch, beforeFence, beforeState, beforeKey, beforeError,
		`UPDATE relay_controller_session_state SET epoch=epoch+2,state='disconnected',fence=1,key_id=NULL,last_error_code=NULL,state_changed_at=?,updated_at=? WHERE controller_id=?`, now, now, controllerID)
	if _, err := db.Exec(`UPDATE relay_controller_session_state SET updated_at=? WHERE controller_id=?`, now, controllerID); err == nil {
		t.Fatal("same-fence lifecycle update was accepted")
	}
}

func assertRelaySessionLifecycleConstraint(t *testing.T, db *sql.DB, controllerID string, beforeEpoch, beforeFence int, beforeState, beforeKey, beforeError, statement string, arguments ...any) {
	t.Helper()
	_, err := db.Exec(statement, arguments...)
	var sqliteErr *sqlite.Error
	if err == nil || !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_TRIGGER {
		t.Fatalf("illegal lifecycle constraint error = %T %v", err, err)
	}
	var afterEpoch, afterFence int
	var afterState, afterKey, afterError string
	if err := db.QueryRow(`SELECT epoch,fence,state,COALESCE(key_id,''),COALESCE(last_error_code,'') FROM relay_controller_session_state WHERE controller_id=?`, controllerID).Scan(&afterEpoch, &afterFence, &afterState, &afterKey, &afterError); err != nil {
		t.Fatal(err)
	}
	if afterEpoch != beforeEpoch || afterFence != beforeFence || afterState != beforeState || afterKey != beforeKey || afterError != beforeError {
		t.Fatalf("illegal transition mutated row before=(%d,%d,%q,%q,%q) after=(%d,%d,%q,%q,%q)", beforeEpoch, beforeFence, beforeState, beforeKey, beforeError, afterEpoch, afterFence, afterState, afterKey, afterError)
	}
}
