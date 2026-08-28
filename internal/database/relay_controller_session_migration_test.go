package database

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRelayControllerSessionMigrationUpgradesFromSevenAndEnforcesPrivacy(t *testing.T) {
	db := openMemoryDatabase(t)
	for _, name := range []string{"001_foundation.sql", "002_github_connections.sql", "003_application_sources.sql", "004_release_snapshots.sql", "005_application_configuration.sql", "006_compose_deployment_runtime.sql", "007_relay_controller_foundation.sql"} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	body, err := migrations.ReadFile("migrations/008_relay_controller_session.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	public, err := migrations.ReadFile("migrations/008_relay_controller_session.sql")
	if err != nil || !bytes.Equal(body, public) {
		t.Fatalf("embedded migration read = %v", err)
	}

	var schema strings.Builder
	rows, err := db.Query(`SELECT name,COALESCE(sql,'') FROM sqlite_master WHERE name LIKE 'relay_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			t.Fatal(err)
		}
		schema.WriteString(strings.ToLower(name + " " + statement + "\n"))
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "poll_token", "access_token", "refresh_token", "oauth", "pkce", "signature", "nonce", "raw_frame", "raw_json", "session_id", "challenge_id", "secret_sentinel"} {
		if strings.Contains(schema.String(), forbidden) {
			t.Errorf("relay SQLite schema contains forbidden material %q", forbidden)
		}
	}
	for _, table := range []string{"relay_controller_session_state", "relay_controller_subscriptions", "relay_subscription_sync_heads", "relay_subscription_sync_sets", "relay_subscription_sync_items", "relay_source_event_inbox", "relay_access_event_inbox", "relay_outbound_commands"} {
		if !strings.Contains(schema.String(), table) {
			t.Errorf("missing %s", table)
		}
	}
	for _, table := range []string{"relay_controller_session_state", "relay_source_event_inbox", "relay_access_event_inbox"} {
		columnRows, err := db.Query(`SELECT lower(name) FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for columnRows.Next() {
			var name string
			if err := columnRows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name == "message_id" || name == "session_id" || strings.Contains(name, "nonce") || strings.Contains(name, "signature") {
				t.Errorf("%s contains forbidden column %s", table, name)
			}
		}
		columnRows.Close()
	}
	primaryKey := map[string]int{}
	columnRows, err := db.Query(`PRAGMA table_info(relay_access_event_inbox)`)
	if err != nil {
		t.Fatal(err)
	}
	for columnRows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := columnRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if pk > 0 {
			primaryKey[name] = pk
		}
	}
	if err := columnRows.Close(); err != nil {
		t.Fatal(err)
	}
	if primaryKey["controller_id"] != 1 || primaryKey["event_id"] != 2 || len(primaryKey) != 2 {
		t.Fatalf("access inbox primary key = %#v", primaryKey)
	}
}

func TestRelayControllerSessionMigrationConstraintsStandWithoutRepository(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	controllerID := "11111111-1111-4111-8111-111111111111"
	keyID := "22222222-2222-4222-8222-222222222222"
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?)`, keyID, controllerID, bytes.Repeat([]byte{1}, 32), "relay/controllers/"+controllerID+"/keys/"+keyID+".key", now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_session_state(controller_id,epoch,fence,state,key_id,last_error_code,attempt,next_attempt_at,last_ready_at,last_seen_at,state_changed_at,updated_at) VALUES(?,1,1,'ready',?,NULL,0,NULL,?,?,?,?)`, controllerID, keyID, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE relay_controller_session_state SET fence=1,updated_at=? WHERE controller_id=?`, now, controllerID); err == nil {
		t.Fatal("stale session fence was accepted")
	}
	if _, err := db.Exec(`INSERT INTO relay_access_event_inbox(controller_id,event_id,installation_id,repository_id,change_code,observed_at,received_at) VALUES(?,'33333333-3333-4333-8333-333333333333',1,0,'provider.raw_body',?,?)`, controllerID, now, now); err == nil {
		t.Fatal("unknown access code was accepted")
	}
}

func TestRelayOutboundCommandSchemaEnforcesControllerAggregateAndLifecycle(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	controllerA := "11111111-1111-4111-8111-111111111111"
	controllerB := "99999999-9999-4999-8999-999999999999"
	oldKey := "22222222-2222-4222-8222-222222222222"
	newKey := "33333333-3333-4333-8333-333333333333"
	bindingPending := "44444444-4444-4444-8444-444444444444"
	bindingAuthorized := "55555555-5555-4555-8555-555555555555"
	rotationID := "66666666-6666-4666-8666-666666666666"
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES('owner','owner','hash',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES('connection','owner','github','connected','42','octocat',1,?,?,?,?,?)`, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, controllerA, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?),(?,?,?,'ed25519','pending',?,?,?,NULL,NULL)`,
		oldKey, controllerA, bytes.Repeat([]byte{1}, 32), "relay/controllers/"+controllerA+"/keys/"+oldKey+".key", now, now, now, now,
		newKey, controllerA, bytes.Repeat([]byte{2}, 32), "relay/controllers/"+controllerA+"/keys/"+newKey+".key", now, now); err != nil {
		t.Fatal(err)
	}
	insertBinding := `INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?, ?,?,?)`
	if _, err := db.Exec(insertBinding, bindingPending, "owner", "connection", controllerA, 10, 20, "removal_pending", now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insertBinding, bindingAuthorized, "owner", "connection", controllerA, 10, 21, "authorized", now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_key_rotations(rotation_id,controller_id,old_key_id,new_key_id,state,expires_at,state_changed_at,created_at,updated_at) VALUES(?,?,?,?, 'prepare',datetime(?,'+1 hour'),?,?,?)`, rotationID, controllerA, oldKey, newKey, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	digest := bytes.Repeat([]byte{0x44}, 32)
	insertCommand := `INSERT INTO relay_outbound_commands(controller_id,message_id,command_type,binding_id,rotation_id,stage,sent_at,canonical_digest,state) VALUES(?,?,?,?,?,?,?,?,'prepared')`
	if _, err := db.Exec(insertCommand, controllerA, "77777777-7777-4777-8777-777777777777", "binding.remove", bindingAuthorized, nil, "remove", now, digest); err == nil {
		t.Fatal("binding command was accepted before removal_pending")
	}
	if _, err := db.Exec(insertCommand, controllerA, "77777777-7777-4777-8777-777777777778", "key.rotation.propose", nil, rotationID, "propose", now, digest); err == nil {
		t.Fatal("rotation propose command was accepted while rotation was prepare")
	}
	if _, err := db.Exec(`UPDATE relay_key_rotations SET state='propose',state_changed_at=?,updated_at=? WHERE rotation_id=?`, now, now, rotationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(2,?,'active',?,?)`, controllerB, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insertCommand, controllerB, "88888888-8888-4888-8888-888888888881", "binding.remove", bindingPending, nil, "remove", now, digest); err == nil {
		t.Fatal("cross-controller binding command was accepted")
	}
	if _, err := db.Exec(insertCommand, controllerB, "88888888-8888-4888-8888-888888888882", "key.rotation.propose", nil, rotationID, "propose", now, digest); err == nil {
		t.Fatal("cross-controller rotation command was accepted")
	}

	pairs := map[string]map[string]bool{"relay_installation_bindings": {}, "relay_key_rotations": {}}
	rows, err := db.Query(`PRAGMA foreign_key_list(relay_outbound_commands)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if _, ok := pairs[table]; ok {
			pairs[table][from+"->"+to] = true
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for table, columns := range pairs {
		if !columns["controller_id->controller_id"] {
			t.Errorf("%s foreign key does not bind controller_id: %#v", table, columns)
		}
	}
}
