package database

import (
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
)

const lanGatewayRebindReleaseIdentityMigration = "037_lan_gateway_rebind_release_identity.sql"

const (
	releaseIdentityAdministrator = "11111111-1111-4111-8111-111111111111"
	releaseIdentityProfile       = "22222222-2222-4222-8222-222222222222"
	releaseIdentityProfileOp     = "33333333-3333-4333-8333-333333333333"
	releaseIdentityUpgradeOp     = "44444444-4444-4444-8444-444444444444"
	releaseIdentityRebindOp      = "55555555-5555-4555-8555-555555555555"
	releaseIdentitySuccessor     = "66666666-6666-4666-8666-666666666666"
	releaseIdentitySuccessorOp   = "77777777-7777-4777-8777-777777777777"
	releaseIdentityApp           = "88888888-8888-4888-8888-888888888888"
	releaseIdentityOtherApp      = "99999999-9999-4999-8999-999999999999"
	releaseIdentityPlan          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	releaseIdentityDeployment    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	releaseIdentityArtifact      = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	releaseIdentityUUID          = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	releaseIdentityOtherRelease  = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	releaseIdentityHex           = "0123456789abcdef0123456789abcdef"
	releaseIdentityStamp         = "2026-10-15T12:00:00.000000000Z"
)

type lanGatewayRebindReleaseIdentityFixture struct {
	operationID  string
	appID        string
	otherAppID   string
	deploymentID string
	releaseID    any
	slot         string
	generation   int64
	updatedAt    string
	entryDigest  string
}

type lanGatewayRebindRuntimeHeadInsert struct {
	operationID  string
	ordinal      int64
	appID        string
	deploymentID string
	releaseID    any
	slot         string
	generation   int64
	updatedAt    string
	entryDigest  string
}

func TestLANGatewayRebindReleaseIdentityMigrationFreshAcceptsSupportedReleaseIDs(t *testing.T) {
	for _, test := range []struct {
		name      string
		releaseID string
	}{
		{name: "canonical UUID", releaseID: releaseIdentityUUID},
		{name: "snapshot lowercase hex", releaseID: releaseIdentityHex},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := migrateFS(db, migrations); err != nil {
				t.Fatalf("migrate fresh schema: %v", err)
			}
			fixture := seedLANGatewayRebindReleaseIdentityFixture(t, db, test.releaseID)
			if err := insertLANGatewayRebindRuntimeHead(db, fixture.runtimeHead()); err != nil {
				t.Fatalf("insert supported release runtime head: %v", err)
			}
			var releaseID, storageClass string
			if err := db.QueryRow(`SELECT release_id,typeof(release_id)
				FROM lan_gateway_rebind_runtime_heads WHERE operation_id=? AND ordinal=1`,
				fixture.operationID).Scan(&releaseID, &storageClass); err != nil {
				t.Fatal(err)
			}
			if releaseID != test.releaseID || storageClass != "text" {
				t.Fatalf("retained release identity=(%q,%q) want=(%q,text)",
					releaseID, storageClass, test.releaseID)
			}
			var applications int
			if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
				lanGatewayRebindReleaseIdentityMigration).Scan(&applications); err != nil || applications != 1 {
				t.Fatalf("migration 037 applications=%d error=%v", applications, err)
			}
		})
	}
}

func TestLANGatewayRebindReleaseIdentityMigrationRejectsMalformedReleaseIDsWithExactParents(t *testing.T) {
	for name, releaseID := range map[string]string{
		"uppercase":           strings.ToUpper(releaseIdentityHex),
		"short":               releaseIdentityHex[:31],
		"long":                releaseIdentityHex + "0",
		"nonhex":              "g" + releaseIdentityHex[1:],
		"whitespace":          " " + releaseIdentityHex[1:],
		"noncanonical UUID":   "{" + releaseIdentityUUID + "}",
		"embedded NUL suffix": releaseIdentityHex + "\x00suffix",
	} {
		t.Run(name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := migrateFS(db, migrations); err != nil {
				t.Fatalf("migrate fresh schema: %v", err)
			}
			fixture := seedLANGatewayRebindReleaseIdentityFixture(t, db, releaseID)
			head := fixture.runtimeHead()
			assertSQLRejectedWith(t, db, name, "runtime head is not exact",
				lanGatewayRebindRuntimeHeadInsertSQL, head.arguments()...)
			var retained int
			if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_runtime_heads
				WHERE operation_id=?`, fixture.operationID).Scan(&retained); err != nil || retained != 0 {
				t.Fatalf("rejected release identity retained rows=%d error=%v", retained, err)
			}
		})
	}
}

func TestLANGatewayRebindReleaseIdentityMigrationPreservesCrossedHeadGuards(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrations); err != nil {
		t.Fatalf("migrate fresh schema: %v", err)
	}
	fixture := seedLANGatewayRebindReleaseIdentityFixture(t, db, releaseIdentityHex)

	for name, mutate := range map[string]func(*lanGatewayRebindRuntimeHeadInsert){
		"crossed app": func(head *lanGatewayRebindRuntimeHeadInsert) {
			head.appID = fixture.otherAppID
		},
		"crossed deployment": func(head *lanGatewayRebindRuntimeHeadInsert) {
			head.deploymentID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		},
		"crossed release": func(head *lanGatewayRebindRuntimeHeadInsert) {
			head.releaseID = releaseIdentityOtherRelease
		},
		"crossed slot":       func(head *lanGatewayRebindRuntimeHeadInsert) { head.slot = "green" },
		"crossed generation": func(head *lanGatewayRebindRuntimeHeadInsert) { head.generation++ },
		"crossed timestamp": func(head *lanGatewayRebindRuntimeHeadInsert) {
			head.updatedAt = "2026-10-15T12:00:01.000000000Z"
		},
		"gapped ordinal": func(head *lanGatewayRebindRuntimeHeadInsert) { head.ordinal = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			head := fixture.runtimeHead()
			mutate(&head)
			assertSQLRejectedWith(t, db, name, "runtime head is not exact",
				lanGatewayRebindRuntimeHeadInsertSQL, head.arguments()...)
		})
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_runtime_heads
		WHERE operation_id=?`, fixture.operationID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("rejected attempts retained rows=%d error=%v", retained, err)
	}
}

func TestLANGatewayRebindReleaseIdentityMigrationRejectsBlobReleaseIDsWithExactParents(t *testing.T) {
	for _, test := range []struct {
		name      string
		releaseID []byte
	}{
		{name: "malformed 32 byte blob", releaseID: []byte(strings.Repeat("g", 32))},
		{name: "canonical looking blob", releaseID: []byte(releaseIdentityHex)},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMemoryDatabase(t)
			if err := migrateFS(db, migrations); err != nil {
				t.Fatalf("migrate fresh schema: %v", err)
			}
			fixture := seedLANGatewayRebindReleaseIdentityFixture(t, db, test.releaseID)
			var releaseType string
			if err := db.QueryRow(`SELECT typeof(h.release_id)
				FROM generated_runtime_active_heads h
				JOIN generated_runtime_deployments d
				  ON d.app_id=h.app_id AND d.deployment_id=h.deployment_id
				 AND d.release_id=h.release_id AND d.candidate_slot=h.slot
				JOIN releases r ON r.app_id=h.app_id AND r.id=h.release_id
				WHERE h.app_id=?`, fixture.appID).Scan(&releaseType); err != nil || releaseType != "blob" {
				t.Fatalf("matched BLOB runtime relation type=%q error=%v", releaseType, err)
			}
			head := fixture.runtimeHead()
			assertSQLRejectedWith(t, db, test.name, "runtime head is not exact",
				lanGatewayRebindRuntimeHeadInsertSQL, head.arguments()...)
		})
	}
}

func TestLANGatewayRebindReleaseIdentityMigrationUpgradeFrom036AcceptsHexReleaseID(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "037_")); err != nil {
		t.Fatalf("migrate through 036: %v", err)
	}
	fixture := seedLANGatewayRebindReleaseIdentityFixture(t, db, releaseIdentityHex)
	assertSQLRejectedWith(t, db, "pre-037 hex release identity", "runtime head is not exact",
		lanGatewayRebindRuntimeHeadInsertSQL, fixture.runtimeHead().arguments()...)

	body, err := migrations.ReadFile("migrations/" + lanGatewayRebindReleaseIdentityMigration)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateFS(db, fstest.MapFS{
		"migrations/" + lanGatewayRebindReleaseIdentityMigration: {Data: body},
	}); err != nil {
		t.Fatalf("apply 037: %v", err)
	}
	if err := insertLANGatewayRebindRuntimeHead(db, fixture.runtimeHead()); err != nil {
		t.Fatalf("insert upgraded hex release runtime head: %v", err)
	}
	var releaseID, storageClass string
	if err := db.QueryRow(`SELECT release_id,typeof(release_id)
		FROM lan_gateway_rebind_runtime_heads WHERE operation_id=? AND ordinal=1`,
		fixture.operationID).Scan(&releaseID, &storageClass); err != nil {
		t.Fatal(err)
	}
	if releaseID != releaseIdentityHex || storageClass != "text" {
		t.Fatalf("upgraded release identity=(%q,%q) want=(%q,text)",
			releaseID, storageClass, releaseIdentityHex)
	}
}

func TestLANGatewayRebindReleaseIdentityMigrationUpgrades036WithoutChangingHistory(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "037_")); err != nil {
		t.Fatalf("migrate through 036: %v", err)
	}
	fixture := seedLANGatewayRebindReleaseIdentityFixture(t, db, releaseIdentityUUID)
	if err := insertLANGatewayRebindRuntimeHead(db, fixture.runtimeHead()); err != nil {
		t.Fatalf("insert pre-037 UUID runtime head: %v", err)
	}
	before := lanGatewayRebindReleaseIdentityHistory(t, db, fixture.operationID)

	body, err := migrations.ReadFile("migrations/" + lanGatewayRebindReleaseIdentityMigration)
	if err != nil {
		t.Fatal(err)
	}
	only037 := fstest.MapFS{
		"migrations/" + lanGatewayRebindReleaseIdentityMigration: {Data: body},
	}
	for pass := 1; pass <= 2; pass++ {
		if err := migrateFS(db, only037); err != nil {
			t.Fatalf("apply 037 pass %d: %v", pass, err)
		}
	}
	after := lanGatewayRebindReleaseIdentityHistory(t, db, fixture.operationID)
	if after != before {
		t.Fatalf("migration 037 changed retained history:\nbefore=%q\nafter=%q", before, after)
	}
	assertSQLRejectedWith(t, db, "retained head update", "runtime head is immutable",
		`UPDATE lan_gateway_rebind_runtime_heads SET generation=generation+1 WHERE operation_id=?`,
		fixture.operationID)
	assertSQLRejectedWith(t, db, "retained head delete", "runtime head history is retained",
		`DELETE FROM lan_gateway_rebind_runtime_heads WHERE operation_id=?`, fixture.operationID)
	var applications int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
		lanGatewayRebindReleaseIdentityMigration).Scan(&applications); err != nil || applications != 1 {
		t.Fatalf("migration 037 applications=%d error=%v", applications, err)
	}
	assertNoForeignKeyViolations(t, db)
}

func TestLANGatewayRebindReleaseIdentityMigrationFailureRestoresOldTrigger(t *testing.T) {
	db := openMemoryDatabase(t)
	if err := migrateFS(db, migrationSetBefore(t, "037_")); err != nil {
		t.Fatalf("migrate through 036: %v", err)
	}
	var oldTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger'
		AND name='lan_gateway_rebind_runtime_head_exact_insert'`).Scan(&oldTrigger); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/" + lanGatewayRebindReleaseIdentityMigration)
	if err != nil {
		t.Fatal(err)
	}
	failing := append(append([]byte(nil), body...),
		[]byte("\nSELECT value FROM migration_037_forced_failure;\n")...)
	err = migrateFS(db, fstest.MapFS{
		"migrations/" + lanGatewayRebindReleaseIdentityMigration: {Data: failing},
	})
	if err == nil {
		t.Fatal("failing migration 037 unexpectedly succeeded")
	}
	var restoredTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger'
		AND name='lan_gateway_rebind_runtime_head_exact_insert'`).Scan(&restoredTrigger); err != nil {
		t.Fatal(err)
	}
	if restoredTrigger != oldTrigger {
		t.Fatal("failed migration 037 did not restore the previous exact-insert trigger")
	}
	var applications int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`,
		lanGatewayRebindReleaseIdentityMigration).Scan(&applications); err != nil || applications != 0 {
		t.Fatalf("failed migration 037 ledger rows=%d error=%v", applications, err)
	}
}

func seedLANGatewayRebindReleaseIdentityFixture(t *testing.T, db *sql.DB, releaseID any) lanGatewayRebindReleaseIdentityFixture {
	t.Helper()
	digest := strings.Repeat("1", 64)
	otherDigest := strings.Repeat("2", 64)
	for index, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		 VALUES(?,'release-identity-admin','hash','administrator',?,?)`,
			[]any{releaseIdentityAdministrator, releaseIdentityStamp, releaseIdentityStamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		 VALUES(?,'release-identity-app','Release Identity App','draft',?,?)`,
			[]any{releaseIdentityApp, releaseIdentityStamp, releaseIdentityStamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
		 VALUES(?,'release-identity-other','Release Identity Other','draft',?,?)`,
			[]any{releaseIdentityOtherApp, releaseIdentityStamp, releaseIdentityStamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
		 id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
		 interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		 ) VALUES(?,1,?,?,'configure_lan_gateway','192.168.82.8',
		 'release-identity-source',8100,8119,?,?,?)`,
			[]any{releaseIdentityProfile, releaseIdentityProfileOp, digest, digest,
				releaseIdentityAdministrator, releaseIdentityStamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=?
		 WHERE singleton=1`, []any{releaseIdentityProfile, releaseIdentityStamp}},
		{`INSERT INTO lan_gateway_upgrade_claims(
		 operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
		 profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
		 ) VALUES(?,?,'upgrade_generated_ingress',?,1,?,?,?,'prepared',1,?)`,
			[]any{releaseIdentityUpgradeOp, digest, releaseIdentityProfile, digest,
				releaseIdentityAdministrator, releaseIdentityStamp, releaseIdentityStamp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='serving',state_sequence=2,updated_at=?
		 WHERE operation_id=?`, []any{releaseIdentityStamp, releaseIdentityUpgradeOp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='committed',state_sequence=3,updated_at=?
		 WHERE operation_id=?`, []any{releaseIdentityStamp, releaseIdentityUpgradeOp}},
		{`INSERT INTO deployment_plan_revisions(
		 id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,
		 source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,
		 analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,
		 migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
		 ) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',
		 ?,?,'accepted',?,?)`, []any{releaseIdentityPlan, releaseIdentityApp,
			"apps/" + releaseIdentityApp + "/deployment-plans/" + releaseIdentityPlan + ".json",
			digest, digest, digest, releaseIdentityAdministrator, releaseIdentityStamp,
			releaseIdentityAdministrator, releaseIdentityStamp}},
		{`INSERT INTO releases(
		 id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		 workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
		 ) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`,
			[]any{releaseID, releaseIdentityApp, releaseIdentityStamp, digest, digest, releaseIdentityPlan}},
		{`INSERT INTO releases(
		 id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		 workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
		 ) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`,
			[]any{releaseIdentityOtherRelease, releaseIdentityApp, releaseIdentityStamp,
				otherDigest, otherDigest, releaseIdentityPlan}},
		{`INSERT INTO deployments(
		 id,app_id,release_id,status,configuration_mode,provenance_initialized,
		 runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number
		 ) VALUES(?,?,?,'preparing','current',1,'generated_node',?,1)`,
			[]any{releaseIdentityDeployment, releaseIdentityApp, releaseID, releaseIdentityPlan}},
		{`INSERT INTO generated_runtime_deployments(
		 deployment_id,app_id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,
		 candidate_slot,phase,migration_state,created_at,updated_at
		 ) VALUES(?,?,?,?,1,'blue','preflight','not_required',?,?)`,
			[]any{releaseIdentityDeployment, releaseIdentityApp, releaseID, releaseIdentityPlan,
				releaseIdentityStamp, releaseIdentityStamp}},
		{`INSERT INTO generated_runtime_components(
		 deployment_id,component_name,slot,state,created_at,updated_at
		 ) VALUES(?,'api','blue','pending',?,?)`,
			[]any{releaseIdentityDeployment, releaseIdentityStamp, releaseIdentityStamp}},
		{`INSERT INTO generated_image_artifacts(
		 id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		 compiler_version,build_definition_digest,attempt_number,image_content_id,state,
		 created_at,updated_at,finished_at
		 ) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`,
			[]any{releaseIdentityArtifact, releaseID, releaseIdentityPlan, 1, "api", digest,
				"sha256:" + digest, releaseIdentityStamp, releaseIdentityStamp, releaseIdentityStamp}},
		{`UPDATE generated_runtime_components SET image_artifact_id=?,state='image_ready',updated_at=?
		 WHERE deployment_id=? AND component_name='api'`,
			[]any{releaseIdentityArtifact, releaseIdentityStamp, releaseIdentityDeployment}},
		{`UPDATE generated_runtime_components SET container_name='rig-release-identity-api',
		 state='starting',updated_at=? WHERE deployment_id=? AND component_name='api'`,
			[]any{releaseIdentityStamp, releaseIdentityDeployment}},
		{`UPDATE generated_runtime_components SET container_id=?,state='running',updated_at=?
		 WHERE deployment_id=? AND component_name='api'`,
			[]any{digest, releaseIdentityStamp, releaseIdentityDeployment}},
		{`UPDATE generated_runtime_components SET state='healthy',updated_at=?
		 WHERE deployment_id=? AND component_name='api'`,
			[]any{releaseIdentityStamp, releaseIdentityDeployment}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed release identity fixture statement %d: %v", index+1, err)
		}
	}
	for _, phase := range []string{"building", "starting_candidate", "waiting_health", "switching_route"} {
		if _, err := db.Exec(`UPDATE generated_runtime_deployments SET phase=?,updated_at=?
			WHERE deployment_id=?`, phase, releaseIdentityStamp, releaseIdentityDeployment); err != nil {
			t.Fatalf("advance release identity runtime to %s: %v", phase, err)
		}
	}
	if _, err := db.Exec(`UPDATE generated_runtime_active_heads
		SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at=? WHERE app_id=?`,
		releaseIdentityDeployment, releaseID, releaseIdentityStamp, releaseIdentityApp); err != nil {
		t.Fatalf("activate release identity runtime: %v", err)
	}
	if _, err := db.Exec(`UPDATE generated_runtime_components SET state='active',updated_at=?
		WHERE deployment_id=? AND component_name='api'`, releaseIdentityStamp,
		releaseIdentityDeployment); err != nil {
		t.Fatalf("activate release identity component: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,predecessor_source_kind,
		predecessor_rebind_operation_id,predecessor_terminal_receipt_digest,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_operation_id,successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at,
		spec_format_version,roster_format_version,predecessor_protected_generation,
		predecessor_protected_journal_digest,predecessor_protected_intent_digest,
		predecessor_source_state_version,predecessor_source_state_revision,
		predecessor_source_state_digest,predecessor_checkpoint_digest,
		successor_protected_generation,runtime_heads_format_version,
		runtime_heads_digest,runtime_heads_count
	) VALUES(?,?,'rebind_lan_gateway',?,?,?,?,1,?,?,?,'gateway_upgrade',NULL,NULL,
		?,2,?,?,'192.168.83.8','release-identity-successor',8100,8119,?,
		'configure_lan_gateway',?,?,?,0,'prepared',1,?,?,2,2,0,?,NULL,2,0,?,?,1,1,?,1)`,
		releaseIdentityRebindOp, digest, digest, releaseIdentityAdministrator, releaseIdentityStamp,
		releaseIdentityProfile, digest, releaseIdentityUpgradeOp, digest,
		releaseIdentitySuccessor, releaseIdentitySuccessorOp, digest, digest,
		releaseIdentityAdministrator, releaseIdentityStamp, digest, releaseIdentityStamp,
		releaseIdentityStamp, digest, digest, digest, digest); err != nil {
		t.Fatalf("seed release identity v2 claim: %v", err)
	}
	return lanGatewayRebindReleaseIdentityFixture{
		operationID: releaseIdentityRebindOp, appID: releaseIdentityApp,
		otherAppID: releaseIdentityOtherApp, deploymentID: releaseIdentityDeployment,
		releaseID: releaseID, slot: "blue", generation: 1, updatedAt: releaseIdentityStamp,
		entryDigest: digest,
	}
}

func (fixture lanGatewayRebindReleaseIdentityFixture) runtimeHead() lanGatewayRebindRuntimeHeadInsert {
	return lanGatewayRebindRuntimeHeadInsert{
		operationID: fixture.operationID, ordinal: 1, appID: fixture.appID,
		deploymentID: fixture.deploymentID, releaseID: fixture.releaseID,
		slot: fixture.slot, generation: fixture.generation, updatedAt: fixture.updatedAt,
		entryDigest: fixture.entryDigest,
	}
}

const lanGatewayRebindRuntimeHeadInsertSQL = `INSERT INTO lan_gateway_rebind_runtime_heads(
	operation_id,ordinal,app_id,deployment_id,release_id,slot,generation,updated_at,entry_digest
) VALUES(?,?,?,?,?,?,?,?,?)`

func (head lanGatewayRebindRuntimeHeadInsert) arguments() []any {
	return []any{head.operationID, head.ordinal, head.appID, head.deploymentID,
		head.releaseID, head.slot, head.generation, head.updatedAt, head.entryDigest}
}

func insertLANGatewayRebindRuntimeHead(db *sql.DB, head lanGatewayRebindRuntimeHeadInsert) error {
	_, err := db.Exec(lanGatewayRebindRuntimeHeadInsertSQL, head.arguments()...)
	return err
}

func lanGatewayRebindReleaseIdentityHistory(t *testing.T, db *sql.DB, operationID string) string {
	t.Helper()
	var value string
	if err := db.QueryRow(`SELECT json_object(
		'claimEvents',(SELECT COUNT(*) FROM lan_gateway_rebind_claim_events WHERE operation_id=?),
		'operationId',operation_id,'ordinal',ordinal,'appId',app_id,
		'deploymentId',deployment_id,'releaseId',release_id,'releaseType',typeof(release_id),
		'slot',slot,'generation',generation,'updatedAt',updated_at,'entryDigest',entry_digest
	) FROM lan_gateway_rebind_runtime_heads WHERE operation_id=? AND ordinal=1`,
		operationID, operationID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
