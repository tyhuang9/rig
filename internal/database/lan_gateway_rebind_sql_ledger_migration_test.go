package database

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const lanGatewayRebindSQLLedgerMigration = "034_lan_gateway_rebind_sql_ledger.sql"

func TestLANGatewayRebindSQLLedgerMigrationPreservesPopulated033History(t *testing.T) {
	db := openMemoryDatabase(t)
	through033 := migrationSetBefore(t, "034_")
	if err := migrateFS(db, through033); err != nil {
		t.Fatalf("migrate through 033: %v", err)
	}

	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		upgradeOp     = "44444444-4444-4444-8444-444444444444"
		rebindOp      = "55555555-5555-4555-8555-555555555555"
		successorID   = "66666666-6666-4666-8666-666666666666"
		successorOp   = "77777777-7777-4777-8777-777777777777"
		appID         = "88888888-8888-4888-8888-888888888888"
		allocationID  = "99999999-9999-4999-8999-999999999999"
		accessID      = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		accessOp      = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		grantID       = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		planID        = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		releaseID     = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
		deploymentID  = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		artifactID    = "10101010-1010-4010-8010-101010101010"
		stamp         = "2026-10-03T12:00:00.000000000Z"
		digestOne     = "1111111111111111111111111111111111111111111111111111111111111111"
		digestTwo     = "2222222222222222222222222222222222222222222222222222222222222222"
		digestThree   = "3333333333333333333333333333333333333333333333333333333333333333"
		digestFour    = "4444444444444444444444444444444444444444444444444444444444444444"
		digestFive    = "5555555555555555555555555555555555555555555555555555555555555555"
	)
	for index, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
			VALUES(?,'ledger-admin','hash','administrator',?,?)`, []any{administrator, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'ledger-app','Ledger App','draft',?,?)`, []any{appID, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
			id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
			interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		) VALUES(?,1,?,?,'configure_lan_gateway','192.168.72.8','ledger-predecessor',8100,8119,?,?,?)`,
			[]any{profileID, profileOp, digestOne, digestTwo, administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, []any{profileID, stamp}},
		{`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,8100,?,?,?,1,'reserved',?)`,
			[]any{allocationID, appID, accessOp, digestOne, profileID, stamp}},
		{`INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,
			allocation_id,allocated_port,gateway_profile_revision_id,
			gateway_profile_revision_number,spec_digest,approved_by,approved_at
		) VALUES(?,?,1,?,?,'enable_lan_access',?,8100,?,1,?,?,?)`,
			[]any{accessID, appID, accessOp, digestTwo, allocationID, profileID, digestThree, administrator, stamp}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`,
			[]any{accessID, stamp, appID}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{accessID, allocationID}},
		{`INSERT INTO lan_app_access_grant_claims(
			attempt_id,request_digest,approval_action,app_id,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			access_spec_digest,allocated_port,gateway_profile_revision_id,
			gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
			approved_at,created_at,state,state_sequence,updated_at
		) VALUES(?,?,'enable_lan_access',?,?,?,?,1,?,8100,?,1,?,?,?,?,'prepared',1,?)`,
			[]any{grantID, digestFour, appID, allocationID, accessOp, accessID, digestThree,
				profileID, digestTwo, administrator, stamp, stamp, stamp}},
		{`UPDATE lan_app_access_grant_claims
			SET state='applying',state_sequence=2,updated_at=? WHERE attempt_id=?`, []any{stamp, grantID}},
		{`UPDATE lan_app_access_grant_claims
			SET state='db_active',state_sequence=3,updated_at=? WHERE attempt_id=?`, []any{stamp, grantID}},
		{`UPDATE lan_app_access_grant_claims
			SET state='committed',state_sequence=4,updated_at=?,resolution_outcome='committed',
				gateway_operation_id=?,protected_state_digest=?,resolved_at=? WHERE attempt_id=?`,
			[]any{stamp, upgradeOp, digestFive, stamp, grantID}},
		{`INSERT INTO deployment_plan_revisions(
			id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,
			source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,
			analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,
			migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
		) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
			[]any{planID, appID, "apps/" + appID + "/deployment-plans/" + planID + ".secret",
				digestOne, digestTwo, digestThree, administrator, stamp, administrator, stamp}},
		{`INSERT INTO releases(
			id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
			workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`,
			[]any{releaseID, appID, stamp, digestTwo, digestFour, planID}},
		{`INSERT INTO deployments(
			id,app_id,release_id,status,configuration_mode,provenance_initialized,
			runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,?,'preparing','current',1,'generated_node',?,1)`,
			[]any{deploymentID, appID, releaseID, planID}},
		{`INSERT INTO lan_gateway_upgrade_claims(
			operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
			profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
		) VALUES(?,?,'upgrade_generated_ingress',?,1,?,?,?,'prepared',1,?)`,
			[]any{upgradeOp, digestOne, profileID, digestTwo, administrator, stamp, stamp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='serving',state_sequence=2,updated_at=? WHERE operation_id=?`, []any{stamp, upgradeOp}},
		{`UPDATE lan_gateway_upgrade_claims SET state='committed',state_sequence=3,updated_at=? WHERE operation_id=?`, []any{stamp, upgradeOp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed populated 033 statement %d: %v", index+1, err)
		}
	}

	runtime := generatedruntimestate.New(db)
	value, _, err := runtime.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1,
		ComponentNames: []string{"api"},
	})
	if err != nil {
		t.Fatalf("begin generated runtime: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,
		created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, "api",
		digestOne, "sha256:"+digestTwo, stamp, stamp, stamp); err != nil {
		t.Fatalf("seed generated image: %v", err)
	}
	if _, err := runtime.SetImageReady(context.Background(), appID, deploymentID, "api", artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerStarting(context.Background(), appID, deploymentID, "api", "rig-ledger-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerRunning(context.Background(), appID, deploymentID, "api", digestThree); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AdvanceComponent(context.Background(), appID, deploymentID, "api",
		generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding,
		generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth,
		generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(context.Background(), appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatalf("advance generated runtime to %s: %v", next, err)
		}
	}
	active, changed, err := runtime.SwitchActive(context.Background(), appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("switch generated runtime active=%#v changed=%t error=%v", active, changed, err)
	}

	if _, err := db.Exec(`INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,successor_profile_revision_id,
		successor_profile_revision_number,successor_profile_operation_id,
		successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		successor_port_start,successor_port_end,successor_profile_spec_digest,
		configure_approval_action,configure_approved_by,configure_approved_at,
		roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) VALUES(?,?,'rebind_lan_gateway',?,?,?,?,1,?,?,?, ?,2,?,?,'192.168.73.8',
		'ledger-successor',8100,8119,?,'configure_lan_gateway',?,?,?,1,'prepared',1,?,?)`,
		rebindOp, digestThree, digestFour, administrator, stamp, profileID, digestTwo,
		upgradeOp, digestThree, successorID, successorOp, digestFour, digestThree,
		administrator, stamp, digestFour, stamp, stamp); err != nil {
		t.Fatalf("seed populated 033 claim: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO lan_gateway_rebind_roster_entries(
		operation_id,ordinal,app_id,allocation_id,allocated_port,allocation_owner_operation_id,
		allocation_state,access_revision_id,access_revision_number,access_spec_digest,
		grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		serving_deployment_id,serving_release_id,serving_slot,route_generation,entry_digest
	) VALUES(?,1,?,?,8100,?,'active',?,1,?,?,4,?,?,?,?,1,?)`,
		rebindOp, appID, allocationID, accessOp, accessID, digestThree, grantID, digestFive,
		deploymentID, releaseID, active.Slot, digestTwo); err != nil {
		t.Fatalf("seed populated 033 roster: %v", err)
	}

	only034 := fstest.MapFS{}
	body, err := migrations.ReadFile("migrations/" + lanGatewayRebindSQLLedgerMigration)
	if err != nil {
		t.Fatal(err)
	}
	only034["migrations/"+lanGatewayRebindSQLLedgerMigration] = &fstest.MapFile{Data: body}
	if err := migrateFS(db, only034); err != nil {
		t.Fatalf("upgrade populated 033 database: %v", err)
	}

	var (
		operationID, requestDigest, sourceKind, predecessorUpgrade, state, createdAt string
		sequence, rosterCount                                                        int
	)
	if err := db.QueryRow(`SELECT operation_id,request_digest,predecessor_source_kind,
		predecessor_upgrade_operation_id,state,state_sequence,roster_count,created_at
		FROM lan_gateway_rebind_claims WHERE operation_id=?`, rebindOp).Scan(
		&operationID, &requestDigest, &sourceKind, &predecessorUpgrade,
		&state, &sequence, &rosterCount, &createdAt); err != nil {
		t.Fatal(err)
	}
	if operationID != rebindOp || requestDigest != digestThree || sourceKind != "gateway_upgrade" ||
		predecessorUpgrade != upgradeOp || state != "prepared" || sequence != 1 ||
		rosterCount != 1 || createdAt != stamp {
		t.Fatalf("migrated claim changed: operation=%q request=%q source=%q predecessor=%q state=%q sequence=%d roster=%d created=%q",
			operationID, requestDigest, sourceKind, predecessorUpgrade, state, sequence, rosterCount, createdAt)
	}
	var (
		rosterOperation, rosterApp, rosterAllocation, rosterOwner, rosterState string
		rosterAccessID, rosterAccessDigest, rosterGrant, rosterGrantDigest     string
		rosterDeployment, rosterRelease, rosterSlot, rosterEntryDigest         string
		ordinal, allocatedPort, accessNumber, grantSequence, routeGeneration   int
	)
	if err := db.QueryRow(`SELECT
		operation_id,ordinal,app_id,allocation_id,allocated_port,
		allocation_owner_operation_id,allocation_state,access_revision_id,
		access_revision_number,access_spec_digest,grant_attempt_id,
		grant_state_sequence,grant_protected_state_digest,serving_deployment_id,
		serving_release_id,serving_slot,route_generation,entry_digest
		FROM lan_gateway_rebind_roster_entries WHERE operation_id=? AND ordinal=1`, rebindOp).Scan(
		&rosterOperation, &ordinal, &rosterApp, &rosterAllocation, &allocatedPort,
		&rosterOwner, &rosterState, &rosterAccessID, &accessNumber, &rosterAccessDigest,
		&rosterGrant, &grantSequence, &rosterGrantDigest, &rosterDeployment,
		&rosterRelease, &rosterSlot, &routeGeneration, &rosterEntryDigest); err != nil {
		t.Fatal(err)
	}
	if rosterOperation != rebindOp || ordinal != 1 || rosterApp != appID ||
		rosterAllocation != allocationID || allocatedPort != 8100 || rosterOwner != accessOp ||
		rosterState != "active" || rosterAccessID != accessID || accessNumber != 1 ||
		rosterAccessDigest != digestThree || rosterGrant != grantID || grantSequence != 4 ||
		rosterGrantDigest != digestFive || rosterDeployment != deploymentID ||
		rosterRelease != releaseID || rosterSlot != active.Slot || routeGeneration != 1 ||
		rosterEntryDigest != digestTwo {
		t.Fatalf("migrated roster changed: operation=%q ordinal=%d app=%q allocation=%q port=%d owner=%q state=%q access=%q/%d accessDigest=%q grant=%q/%d grantDigest=%q deployment=%q release=%q slot=%q generation=%d entry=%q",
			rosterOperation, ordinal, rosterApp, rosterAllocation, allocatedPort, rosterOwner,
			rosterState, rosterAccessID, accessNumber, rosterAccessDigest, rosterGrant,
			grantSequence, rosterGrantDigest, rosterDeployment, rosterRelease, rosterSlot,
			routeGeneration, rosterEntryDigest)
	}
	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claim_events
		WHERE operation_id=? AND sequence=1 AND state='prepared' AND created_at=?`, rebindOp, stamp).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("prepared event rows=%d error=%v", eventCount, err)
	}
	var upgradeEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_upgrade_claim_events WHERE operation_id=?`, upgradeOp).Scan(&upgradeEvents); err != nil || upgradeEvents != 3 {
		t.Fatalf("predecessor upgrade events=%d error=%v", upgradeEvents, err)
	}
	assertNoForeignKeyViolations(t, db)
	assertSQLRejectedWith(t, db, "populated 034 runtime-head fence", "rebind fence is active",
		`UPDATE generated_runtime_active_heads SET generation=generation WHERE app_id=?`, appID)

	assertSQLRejected(t, db, "claim update", `UPDATE lan_gateway_rebind_claims SET updated_at=updated_at WHERE operation_id=?`, rebindOp)
	assertSQLRejected(t, db, "direct event append", `INSERT INTO lan_gateway_rebind_claim_events(operation_id,sequence,state,created_at)
		VALUES(?,2,'unresolved',?)`, rebindOp, stamp)
	assertSQLRejectedWith(t, db, "terminal claim insert", "must begin prepared", `INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,predecessor_source_kind,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_operation_id,successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) SELECT
		'88888888-8888-4888-8888-888888888888',request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,'gateway_upgrade',
		'99999999-9999-4999-8999-999999999999',successor_profile_revision_number,
		'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,'committed',2,created_at,updated_at
	FROM lan_gateway_rebind_claims WHERE operation_id=?`, rebindOp)
	assertSQLRejectedWith(t, db, "prepared claim timestamp mismatch", "must begin prepared", `INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,predecessor_source_kind,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_operation_id,successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) SELECT
		'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,'gateway_upgrade',
		'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',successor_profile_revision_number,
		'cccccccc-cccc-4ccc-8ccc-cccccccccccc',successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,'prepared',1,created_at,'2026-10-03T12:00:01.000000000Z'
	FROM lan_gateway_rebind_claims WHERE operation_id=?`, rebindOp)
	assertSQLRejectedWith(t, db, "prior rebind without receipt ledger", "committed current predecessor", `INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,predecessor_source_kind,
		predecessor_rebind_operation_id,predecessor_terminal_receipt_digest,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_operation_id,successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) SELECT
		'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,NULL,
		predecessor_protected_identity_digest,'gateway_rebind',operation_id,?,
		'cccccccc-cccc-4ccc-8ccc-cccccccccccc',successor_profile_revision_number,
		'dddddddd-dddd-4ddd-8ddd-dddddddddddd',successor_profile_request_digest,
		successor_selected_ipv4,successor_interface_id,successor_port_start,successor_port_end,
		successor_profile_spec_digest,configure_approval_action,configure_approved_by,
		configure_approved_at,roster_digest,roster_count,'prepared',1,created_at,updated_at
	FROM lan_gateway_rebind_claims WHERE operation_id=?`, digestOne, rebindOp)
	assertSQLRejectedWith(t, db, "transition command", "guarded transition writer is not installed", `INSERT INTO lan_gateway_rebind_transition_commands(
		operation_id,sequence,previous_state,previous_sequence,next_state,purpose,
		protected_record_digest,terminal_receipt_digest,command_digest,created_at
	) VALUES(?,2,'prepared',1,'successor_ready','lan_gateway_rebind_transition',?,?,?,?)`,
		rebindOp, digestOne, digestTwo, digestThree, stamp)
	assertSQLRejectedWith(t, db, "allocation transfer", "guarded transfer writer is not installed", `INSERT INTO lan_gateway_rebind_allocation_transfers(
		operation_id,ordinal,app_id,allocation_id,grant_attempt_id,source_binding_digest,
		successor_profile_revision_id,successor_profile_revision_number,
		successor_profile_spec_digest,terminal_receipt_digest,transfer_digest,created_at
	) VALUES(?,1,'missing-app','missing-allocation','missing-grant',?,?,2,?,?,?,?)`,
		rebindOp, digestOne, successorID, digestTwo, digestThree, digestFour, stamp)
}

func TestLANGatewayRebindSQLLedgerMigrationIsDormantAndMirrored(t *testing.T) {
	embedded, err := migrations.ReadFile("migrations/" + lanGatewayRebindSQLLedgerMigration)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", lanGatewayRebindSQLLedgerMigration))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, public) {
		t.Fatal("public and embedded migration 034 differ")
	}

	db := openMemoryDatabase(t)
	through034 := migrationSetBefore(t, "035_")
	if err := migrateFS(db, through034); err != nil {
		t.Fatalf("fresh migration: %v", err)
	}
	for _, table := range []string{
		"lan_gateway_rebind_claims", "lan_gateway_rebind_claim_events",
		"lan_gateway_rebind_transition_commands", "lan_gateway_rebind_allocation_transfers",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("fresh %s rows=%d error=%v", table, count, err)
		}
	}
	assertNoForeignKeyViolations(t, db)
	const (
		administrator = "11111111-1111-4111-8111-111111111111"
		profileID     = "22222222-2222-4222-8222-222222222222"
		profileOp     = "33333333-3333-4333-8333-333333333333"
		stamp         = "2026-10-03T12:00:00.000000000Z"
		digestOne     = "1111111111111111111111111111111111111111111111111111111111111111"
		digestTwo     = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'ledger-no-claim-admin','hash','administrator',?,?)`, administrator, stamp, stamp); err != nil {
		t.Fatalf("seed administrator without rebind claim: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
		interface_id,port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,1,?,?,'configure_lan_gateway','192.168.74.8','ledger-no-claim',8100,8119,?,?,?)`,
		profileID, profileOp, digestOne, digestTwo, administrator, stamp); err != nil {
		t.Fatalf("ordinary profile revision without rebind claim: %v", err)
	}
	if _, err := db.Exec(`UPDATE lan_gateway_profile_heads
		SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`, profileID, stamp); err != nil {
		t.Fatalf("ordinary profile head advance without rebind claim: %v", err)
	}
	var currentProfile string
	if err := db.QueryRow(`SELECT revision_id FROM lan_gateway_profile_heads WHERE singleton=1`).Scan(&currentProfile); err != nil || currentProfile != profileID {
		t.Fatalf("ordinary profile head=%q error=%v", currentProfile, err)
	}

	var claimSQL, eventSQL, activeIndexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='lan_gateway_rebind_claims'`).Scan(&claimSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='lan_gateway_rebind_claim_events'`).Scan(&eventSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='index' AND name='lan_gateway_rebind_claims_one_active'`).Scan(&activeIndexSQL); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"successor_ready", "database_committed", "committed", "rolled_back", "unresolved"} {
		if !strings.Contains(claimSQL, "'"+state+"'") || !strings.Contains(eventSQL, "'"+state+"'") {
			t.Fatalf("future state %q missing from rebuilt claim/event schema", state)
		}
	}
	if strings.Contains(eventSQL, "UNIQUE(operation_id, state)") {
		t.Fatal("migration 034 retained state uniqueness that prevents recovery revisits")
	}
	for _, state := range []string{"prepared", "successor_ready", "database_committed", "unresolved"} {
		if !strings.Contains(activeIndexSQL, "'"+state+"'") {
			t.Fatalf("active-claim index is missing %q", state)
		}
	}
	for _, terminal := range []string{"committed", "rolled_back"} {
		if strings.Contains(activeIndexSQL, "'"+terminal+"'") {
			t.Fatalf("active-claim index includes terminal state %q", terminal)
		}
	}

	rows, err := db.Query(`SELECT sql FROM sqlite_schema
		WHERE type='trigger' AND name LIKE 'lan_gateway_rebind_fence_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var fences int
	for rows.Next() {
		var triggerSQL string
		if err := rows.Scan(&triggerSQL); err != nil {
			t.Fatal(err)
		}
		fences++
		normalized := strings.Join(strings.Fields(triggerSQL), " ")
		if !strings.Contains(normalized, "WHEN EXISTS (SELECT 1 FROM lan_gateway_rebind_claims)") {
			t.Fatalf("migration 034 weakened dormant fence: %s", normalized)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fences != 18 {
		t.Fatalf("recreated fence count=%d, want 18", fences)
	}
	for _, pin := range []string{
		"lan_gateway_profile_upgrade_claim_pin_insert",
		"lan_gateway_profile_upgrade_claim_pin_head",
		"lan_gateway_profile_head_grant_fence",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, pin).Scan(&count); err != nil || count != 1 {
			t.Fatalf("preserved pin %s count=%d error=%v", pin, count, err)
		}
	}
}

func migrationSetBefore(t *testing.T, exclusivePrefix string) fstest.MapFS {
	t.Helper()
	result := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= exclusivePrefix {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		result["migrations/"+entry.Name()] = &fstest.MapFile{Data: body}
	}
	return result
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	var table, rowID, parent string
	var foreignKeyID int
	err := db.QueryRow(`SELECT "table",rowid,parent,fkid FROM pragma_foreign_key_check LIMIT 1`).Scan(
		&table, &rowID, &parent, &foreignKeyID)
	if err == nil {
		t.Fatalf("foreign key violation table=%q row=%q parent=%q id=%d", table, rowID, parent, foreignKeyID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign key check: %v", err)
	}
}

func assertSQLRejected(t *testing.T, db *sql.DB, name, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err == nil {
		t.Fatalf("%s was accepted", name)
	}
}

func assertSQLRejectedWith(t *testing.T, db *sql.DB, name, fragment, query string, args ...any) {
	t.Helper()
	_, err := db.Exec(query, args...)
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("%s error=%v, want %q", name, err, fragment)
	}
}
