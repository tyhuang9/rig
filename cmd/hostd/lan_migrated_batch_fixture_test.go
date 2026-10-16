package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

type legacyNativeBatchActorRow struct {
	id, username, passphraseHash, role, createdAt, updatedAt string
}

type legacyNativeBatchMachineRow struct {
	id, name, mode, status, osName, architecture, hostname, agentVersion string
	lastHeartbeat, dockerVersion, composeVersion                         sql.NullString
	capabilitiesJSON, resourcesJSON, createdAt, updatedAt                string
}

type legacyNativeBatchApplicationRow struct {
	id, slug, name, description, sourcePath, machineID, status, createdAt, updatedAt string
	archivedAt                                                                       sql.NullString
}

type legacyNativeBatchEvent struct {
	sequence             int64
	state                string
	gatewayOperationID   sql.NullString
	protectedStateDigest sql.NullString
	createdAt            string
}

type legacyNativeBatchAccessRow struct {
	allocation       appaccess.Allocation
	revision         appaccess.AppAccessRevision
	revisionRequest  string
	grant            appaccess.AppAccessGrantClaim
	grantEvents      []legacyNativeBatchEvent
	disableOperation string
}

type legacyNativeBatchAuthority struct {
	actor          legacyNativeBatchActorRow
	machine        legacyNativeBatchMachineRow
	applications   []legacyNativeBatchApplicationRow
	profile        appaccess.GatewayProfileRevision
	profileRequest string
	upgrade        appaccess.GatewayProfileUpgradeClaim
	upgradeEvents  []legacyNativeBatchEvent
	access         []legacyNativeBatchAccessRow
}

type legacyNativePendingLANBatch struct {
	operationIDs []string
	claims       map[string]legacyNativeBatchAccessRow
}

func seedLegacyNativePendingLANBatch(t *testing.T, db *sql.DB,
	authority legacyNativeBatchAuthority,
) legacyNativePendingLANBatch {
	t.Helper()
	batch := seedLegacyNativeAuthorityThrough028(t, db, authority, true)
	if err := database.Migrate(db); err != nil {
		t.Fatal("migrate native pending LAN batch through latest:", err)
	}
	assertLegacyNativeBatchSchemaGuards(t, db, true)
	assertLegacyNativeBatchExcludedRows(t, db, true)
	assertLegacyNativePendingLANBatch(t, db, authority, batch)
	return batch
}

func seedLegacyNativeAuthorityThrough028(t *testing.T, db *sql.DB,
	authority legacyNativeBatchAuthority, includeDisableIntents bool,
) legacyNativePendingLANBatch {
	t.Helper()
	if err := validateLegacyNativeBatchAuthority(authority); err != nil {
		t.Fatal(err)
	}
	assertLegacyNativeBatchSchemaGuards(t, db, false)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	mustExec := func(label, query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}

	mustExec("seed historical administrator", `INSERT INTO users(
		id,username,passphrase_hash,role,created_at,updated_at
	) VALUES(?,?,?,?,?,?)`, authority.actor.id, authority.actor.username, authority.actor.passphraseHash,
		authority.actor.role, authority.actor.createdAt, authority.actor.updatedAt)
	m := authority.machine
	mustExec("seed historical machine", `INSERT INTO machines(
		id,name,mode,status,last_heartbeat_at,os,architecture,hostname,docker_version,
		compose_version,agent_version,capabilities_json,resources_json,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.id, m.name, m.mode, m.status, m.lastHeartbeat,
		m.osName, m.architecture, m.hostname, m.dockerVersion, m.composeVersion, m.agentVersion,
		m.capabilitiesJSON, m.resourcesJSON, m.createdAt, m.updatedAt)
	for _, app := range authority.applications {
		mustExec("seed historical application", `INSERT INTO applications(
			id,slug,name,description,source_path,active_machine_id,status,created_at,updated_at,archived_at
		) VALUES(?,?,?,?,?,?,?,?,?,?)`, app.id, app.slug, app.name, app.description, app.sourcePath,
			app.machineID, app.status, app.createdAt, app.updatedAt, app.archivedAt)
	}
	p := authority.profile
	mustExec("seed historical gateway profile", `INSERT INTO lan_gateway_profile_revisions(
		id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
		interface_id,port_start,port_end,spec_digest,approved_by,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.RevisionNumber, p.OperationID, authority.profileRequest,
		appaccess.ActionConfigureGateway, p.Spec.SelectedIPv4, p.Spec.InterfaceID, p.Spec.PortStart,
		p.Spec.PortEnd, p.SpecDigest, p.ApprovedBy, p.ApprovedAt.Format(timeFormat))
	mustExec("select historical gateway profile", `UPDATE lan_gateway_profile_heads
		SET revision_id=?,revision_number=?,updated_at=? WHERE singleton=1`,
		p.ID, p.RevisionNumber, p.ApprovedAt.Format(timeFormat))

	u := authority.upgrade
	firstUpgrade := authority.upgradeEvents[0]
	mustExec("seed prepared historical gateway upgrade", `INSERT INTO lan_gateway_upgrade_claims(
		operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
		profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,1,?)`, u.OperationID, u.RequestDigest, appaccess.ActionUpgradeGateway,
		u.ProfileRevisionID, u.ProfileRevisionNumber, u.ProfileSpecDigest, u.ApprovedBy,
		u.ApprovedAt.Format(timeFormat), firstUpgrade.state, firstUpgrade.createdAt)
	for _, event := range authority.upgradeEvents[1:] {
		mustExec("replay historical gateway upgrade event", `UPDATE lan_gateway_upgrade_claims
			SET state=?,state_sequence=?,updated_at=?
			WHERE operation_id=? AND state_sequence=?`, event.state, event.sequence, event.createdAt,
			u.OperationID, event.sequence-1)
	}

	batch := legacyNativePendingLANBatch{operationIDs: make([]string, 0, len(authority.access)),
		claims: make(map[string]legacyNativeBatchAccessRow, len(authority.access))}
	for _, access := range authority.access {
		a, r, g := access.allocation, access.revision, access.grant
		mustExec("seed reserved historical allocation", `INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,?,'reserved',?)`, a.ID, a.AppID, a.Port, a.OwnerOperationID,
			a.ReservationDigest, a.GatewayProfileRevisionID, a.GatewayProfileRevisionNumber,
			a.ReservedAt.Format(timeFormat))
		mustExec("seed historical access revision", `INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,?,?,?,'enable_lan_access',?,?,?,?,?,?,?)`, r.ID, r.AppID, r.RevisionNumber,
			r.OperationID, access.revisionRequest, a.ID, a.Port, a.GatewayProfileRevisionID,
			a.GatewayProfileRevisionNumber, r.SpecDigest, r.ApprovedBy, r.ApprovedAt.Format(timeFormat))
		mustExec("select historical access revision", `UPDATE lan_app_access_heads
			SET revision_id=?,revision_number=?,updated_at=? WHERE app_id=?`,
			r.ID, r.RevisionNumber, r.ApprovedAt.Format(timeFormat), r.AppID)
		mustExec("bind historical allocation owner", `UPDATE lan_port_allocations
			SET owner_revision_id=? WHERE id=?`, r.ID, a.ID)

		firstGrant := access.grantEvents[0]
		mustExec("seed prepared historical grant", `INSERT INTO lan_app_access_grant_claims(
			attempt_id,request_digest,approval_action,app_id,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			access_spec_digest,allocated_port,gateway_profile_revision_id,
			gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
			approved_at,created_at,state,state_sequence,updated_at
		) VALUES(?1,?2,'enable_lan_access',?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,'prepared',1,?15)`,
			g.AttemptID, g.RequestDigest, g.Spec.AppID, g.Spec.AllocationID, g.Spec.OwnerOperationID,
			g.Spec.AccessRevisionID, g.Spec.AccessRevisionNumber, g.Spec.AccessSpecDigest, g.Spec.Port,
			g.Spec.GatewayProfileRevisionID, g.Spec.GatewayProfileRevisionNumber,
			g.Spec.GatewayProfileSpecDigest, g.Spec.ApprovedBy, g.ApprovedAt.Format(timeFormat),
			firstGrant.createdAt)
		for _, event := range access.grantEvents[1:] {
			if event.state == string(appaccess.AppAccessGrantCommitted) {
				mustExec("replay committed historical grant event", `UPDATE lan_app_access_grant_claims
					SET state=?,state_sequence=?,updated_at=?,resolution_outcome=?,gateway_operation_id=?,
					protected_state_digest=?,resolved_at=? WHERE attempt_id=? AND state_sequence=?`,
					event.state, event.sequence, event.createdAt, event.state, event.gatewayOperationID,
					event.protectedStateDigest, event.createdAt, g.AttemptID, event.sequence-1)
			} else {
				mustExec("replay nonterminal historical grant event", `UPDATE lan_app_access_grant_claims
					SET state=?,state_sequence=?,updated_at=? WHERE attempt_id=? AND state_sequence=?`,
					event.state, event.sequence, event.createdAt, g.AttemptID, event.sequence-1)
			}
		}

		if includeDisableIntents {
			disableSpec := appaccess.AppAccessDisableSpecFor(r)
			disableDigest, err := appaccess.AppAccessDisableSpecDigest(disableSpec)
			if err != nil {
				t.Fatal(err)
			}
			owner := appaccess.AllocationOwner{AllocationID: a.ID, AppID: a.AppID,
				OperationID: a.OwnerOperationID, AccessRevisionID: r.ID}
			disableRequest := legacyDisableApprovalRequestDigestAtRevision(t, owner, r.RevisionNumber,
				disableDigest, authority.actor.id)
			mustExec("seed migration-027 disable intent", `INSERT INTO lan_app_access_disable_intents(
				operation_id,app_id,request_digest,approval_action,allocation_id,
				allocation_owner_operation_id,access_revision_id,access_revision_number,
				allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
				spec_digest,approved_by,approved_at
			) VALUES(?,?,?,'disable_lan_access',?,?,?,?,?,?,?,?,?,?)`, access.disableOperation,
				a.AppID, disableRequest, a.ID, a.OwnerOperationID, r.ID, r.RevisionNumber, a.Port,
				a.GatewayProfileRevisionID, a.GatewayProfileRevisionNumber, disableDigest,
				authority.actor.id, g.UpdatedAt.Format(timeFormat))
			batch.operationIDs = append(batch.operationIDs, access.disableOperation)
			batch.claims[access.disableOperation] = access
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(batch.operationIDs, func(i, j int) bool {
		return batch.claims[batch.operationIDs[i]].allocation.AppID <
			batch.claims[batch.operationIDs[j]].allocation.AppID
	})
	assertLegacyNativeBatchExcludedRows(t, db, false)
	return batch
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func validateLegacyNativeBatchAuthority(value legacyNativeBatchAuthority) error {
	if value.actor.id == "" || value.actor.role != "administrator" || value.machine.id == "" ||
		len(value.applications) != 2 || len(value.access) != 2 {
		return fmt.Errorf("invalid historical native batch authority cardinality")
	}
	if err := validateLegacyNativeBatchUpgradeHistory(value.upgrade, value.upgradeEvents); err != nil {
		return err
	}
	apps, allocations, attempts, disables, ports := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[uint16]bool{}
	for index, access := range value.access {
		if len(access.grantEvents) != 4 || access.grant.State != appaccess.AppAccessGrantCommitted ||
			access.grant.StateSequence != 4 || access.grant.Proof == nil ||
			access.grantEvents[0].sequence != 1 || access.grantEvents[0].state != string(appaccess.AppAccessGrantPrepared) ||
			!legacyNativeTimestampEqual(access.grantEvents[0].createdAt, access.grant.CreatedAt) ||
			access.grantEvents[3].state != string(appaccess.AppAccessGrantCommitted) ||
			access.grantEvents[3].gatewayOperationID.String != access.grant.Proof.GatewayOperationID ||
			access.grantEvents[3].protectedStateDigest.String != access.grant.Proof.ProtectedStateDigest ||
			!legacyNativeTimestampEqual(access.grantEvents[3].createdAt, access.grant.Proof.ObservedAt) ||
			access.allocation.State != appaccess.AllocationActive || access.allocation.ReleasedAt != nil ||
			access.revision.AppID != access.allocation.AppID || access.grant.Spec.AppID != access.allocation.AppID ||
			access.disableOperation == "" || index >= len(value.applications) {
			return fmt.Errorf("invalid historical native access authority at index %d", index)
		}
		appID, allocationID, attemptID, disableID, port := access.allocation.AppID, access.allocation.ID,
			access.grant.AttemptID, access.disableOperation, access.allocation.Port
		if apps[appID] || allocations[allocationID] || attempts[attemptID] || disables[disableID] || ports[port] {
			return fmt.Errorf("historical native batch identities are not distinct")
		}
		apps[appID], allocations[allocationID], attempts[attemptID], disables[disableID], ports[port] = true, true, true, true, true
	}
	for _, app := range value.applications {
		if !apps[app.id] || app.machineID != value.machine.id || app.archivedAt.Valid {
			return fmt.Errorf("historical native application identity is not exact")
		}
	}
	return nil
}

func validateLegacyNativeBatchUpgradeHistory(claim appaccess.GatewayProfileUpgradeClaim,
	events []legacyNativeBatchEvent,
) error {
	if claim.State != appaccess.GatewayProfileUpgradeCommitted ||
		(claim.StateSequence != 2 && claim.StateSequence != 3) ||
		int64(len(events)) != claim.StateSequence {
		return fmt.Errorf("invalid historical native upgrade history cardinality")
	}

	var previousAt time.Time
	for index, event := range events {
		if event.sequence != int64(index+1) {
			return fmt.Errorf("invalid historical native upgrade event sequence")
		}
		if event.gatewayOperationID.Valid || event.protectedStateDigest.Valid {
			return fmt.Errorf("invalid historical native upgrade event protected fields")
		}
		createdAt, err := time.Parse(time.RFC3339Nano, event.createdAt)
		if err != nil || (index > 0 && createdAt.Before(previousAt)) {
			return fmt.Errorf("invalid historical native upgrade event timestamp")
		}
		switch {
		case index == 0:
			if event.state != string(appaccess.GatewayProfileUpgradePrepared) || !createdAt.Equal(claim.ApprovedAt) {
				return fmt.Errorf("invalid historical native upgrade prepared event")
			}
		case claim.StateSequence == 3 && index == 1:
			if event.state != string(appaccess.GatewayProfileUpgradeServing) {
				return fmt.Errorf("invalid historical native upgrade intermediate event")
			}
		case index == len(events)-1:
			if event.state != string(appaccess.GatewayProfileUpgradeCommitted) || !createdAt.Equal(claim.UpdatedAt) {
				return fmt.Errorf("invalid historical native upgrade final event")
			}
		default:
			return fmt.Errorf("invalid historical native upgrade history shape")
		}
		previousAt = createdAt
	}
	return nil
}

func legacyNativeTimestampEqual(value string, expected time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Equal(expected)
}

func assertLegacyNativeBatchSchemaGuards(t *testing.T, db *sql.DB, migrated bool) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("historical native fixture foreign keys enabled=%d error=%v", enabled, err)
	}
	names := []string{"lan_app_access_disable_exact_owner_insert", "lan_port_allocation_disable_intent_freeze",
		"lan_app_access_grant_exact_owner_insert", "lan_app_access_grant_state_transition",
		"lan_port_allocation_state_update"}
	if migrated {
		names = append(names, "lan_app_access_disable_claim_matches_intent_insert",
			"lan_app_access_disable_claim_source_grant_insert", "lan_app_access_disable_claim_prepared_insert")
	}
	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("historical native fixture trigger %q count=%d error=%v", name, count, err)
		}
	}
}

func assertLegacyNativeBatchExcludedRows(t *testing.T, db *sql.DB, migrated bool) {
	t.Helper()
	tables := []string{"sessions", "jobs", "deployments", "generated_runtime_deployments",
		"generated_runtime_components", "audit_events"}
	if migrated {
		tables = append(tables, "lan_gateway_rebind_claims", "lan_gateway_rebind_claim_events",
			"lan_gateway_rebind_roster_entries")
	}
	for _, table := range tables {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("historical native fixture retained unexpected %s rows=%d error=%v", table, count, err)
		}
	}
	var emptyHeads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads
		WHERE generation=0 AND deployment_id IS NULL AND release_id IS NULL AND slot IS NULL AND updated_at IS NULL`).Scan(&emptyHeads); err != nil || emptyHeads != 2 {
		t.Fatalf("historical native fixture empty runtime heads=%d error=%v", emptyHeads, err)
	}
	var allHeads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads`).Scan(&allHeads); err != nil || allHeads != emptyHeads {
		t.Fatalf("historical native fixture retained active runtime heads=%d empty=%d error=%v", allHeads, emptyHeads, err)
	}
}

func assertLegacyNativePendingLANBatch(t *testing.T, db *sql.DB, authority legacyNativeBatchAuthority,
	batch legacyNativePendingLANBatch,
) {
	t.Helper()
	snapshot, err := appaccess.New(db).HostingGatewayStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal("read migrated native batch snapshot:", err)
	}
	if len(snapshot.Upgrades.Claims) != 1 || snapshot.Upgrades.Claims[0].Claim.OperationID != authority.upgrade.OperationID ||
		snapshot.Upgrades.Claims[0].Claim.State != appaccess.GatewayProfileUpgradeCommitted ||
		len(snapshot.Grants.Claims) != 2 || len(snapshot.Disables.Claims) != 2 ||
		len(snapshot.Rebind.History) != 0 || snapshot.Rebind.Active != nil || snapshot.Rebind.CurrentSource == nil ||
		snapshot.Rebind.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		snapshot.Rebind.CurrentSource.OperationID != authority.upgrade.OperationID {
		t.Fatalf("migrated native batch authority census upgrades=%d grants=%d disables=%d rebind_history=%d active=%t source=%q",
			len(snapshot.Upgrades.Claims), len(snapshot.Grants.Claims), len(snapshot.Disables.Claims),
			len(snapshot.Rebind.History), snapshot.Rebind.Active != nil, func() string {
				if snapshot.Rebind.CurrentSource == nil {
					return ""
				}
				return string(snapshot.Rebind.CurrentSource.Kind)
			}())
	}
	if len(batch.operationIDs) != 2 || len(batch.claims) != 2 {
		t.Fatal("migrated native batch identity count changed")
	}
	seen := make(map[string]bool, 2)
	for _, entry := range snapshot.Disables.Claims {
		expected, ok := batch.claims[entry.Claim.OperationID]
		if !ok || entry.Claim.State != appaccess.AppAccessDisablePrepared || entry.Claim.StateSequence != 1 ||
			entry.Claim.Spec.AppID != expected.allocation.AppID || entry.Claim.Spec.AllocationID != expected.allocation.ID ||
			entry.Claim.Spec.OwnerOperationID != expected.allocation.OwnerOperationID ||
			entry.Claim.Spec.AccessRevisionID != expected.revision.ID || entry.Claim.Spec.Port != expected.allocation.Port ||
			entry.Claim.SourceGrantAttemptID != expected.grant.AttemptID || entry.SourceGrant == nil ||
			entry.SourceGrant.AttemptID != expected.grant.AttemptID || !entry.AccessHeadCurrent ||
			!entry.ProfileHeadCurrent || !entry.ApproverIsAdministrator ||
			entry.CurrentGatewaySource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
			entry.CurrentGatewaySource.OperationID != authority.upgrade.OperationID {
			t.Fatalf("migrated native claim identity mismatch operation=%q", entry.Claim.OperationID)
		}
		seen[entry.Claim.OperationID] = true
	}
	if len(seen) != 2 {
		t.Fatal("migrated native batch omitted an expected operation")
	}
	var migration029, all, foreign int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version='029_lan_app_access_disable_claims.sql'`).Scan(&migration029); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_claims`).Scan(&all); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_app_access_disable_claims WHERE operation_id NOT IN (?,?)`,
		batch.operationIDs[0], batch.operationIDs[1]).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if migration029 != 1 || all != 2 || foreign != 0 {
		t.Fatalf("migrated native batch census migration029=%d all=%d foreign=%d", migration029, all, foreign)
	}
}

func TestLegacyNativePendingLANBatchMigratesExactGuardedClaims(t *testing.T) {
	db := legacyPairCompositionDBThrough028(t)
	authority := deterministicLegacyNativeBatchAuthority(t)
	batch := seedLegacyNativePendingLANBatch(t, db, authority)
	if len(batch.operationIDs) != 2 || batch.operationIDs[0] == batch.operationIDs[1] {
		t.Fatal("deterministic native batch did not retain two distinct operations")
	}
}

func deterministicLegacyNativeBatchAuthority(t *testing.T) legacyNativeBatchAuthority {
	t.Helper()
	ids := []string{
		"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666",
		"77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888",
		"99999999-9999-4999-8999-999999999999", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"dddddddd-dddd-4ddd-8ddd-dddddddddddd", "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		"ffffffff-ffff-4fff-8fff-ffffffffffff", "12345678-1234-4234-8234-123456789abc",
		"13572468-2468-4135-8246-135724681357",
	}
	stamp := func(second int) string { return fmt.Sprintf("2026-10-01T12:00:%02d.000000000Z", second) }
	parse := func(value string) time.Time {
		parsed, err := time.Parse(timeFormat, value)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	profileSpec := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.44.20", InterfaceID: "native-batch", PortStart: 8100, PortEnd: 8101}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	profile := appaccess.GatewayProfileRevision{ID: ids[2], RevisionNumber: 1, OperationID: ids[3], Spec: profileSpec,
		SpecDigest: profileDigest, ApprovedBy: ids[0], ApprovedAt: parse(stamp(1))}
	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{ProfileRevisionID: profile.ID, ProfileRevisionNumber: 1, ProfileSpecDigest: profileDigest}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal(err)
	}
	upgradeRequest := legacyJSONDigest(t, struct {
		Spec     appaccess.GatewayProfileUpgradeSpec `json:"spec"`
		Approval appaccess.Approval                  `json:"approval"`
	}{upgradeSpec, appaccess.Approval{Action: appaccess.ActionUpgradeGateway, SpecDigest: upgradeDigest, ActorID: ids[0]}})
	value := legacyNativeBatchAuthority{
		actor: legacyNativeBatchActorRow{ids[0], "native-batch-admin", "hash", "administrator", stamp(0), stamp(0)},
		machine: legacyNativeBatchMachineRow{id: ids[1], name: "native-batch", mode: "local", status: "online",
			osName: "linux", architecture: "amd64", hostname: "native-batch", agentVersion: "test",
			capabilitiesJSON: "{}", resourcesJSON: "{}", createdAt: stamp(0), updatedAt: stamp(0)},
		profile: profile, profileRequest: legacyGatewayProfileRequestDigest(t, profile.OperationID, profileSpec, profileDigest, ids[0]),
		upgrade: appaccess.GatewayProfileUpgradeClaim{OperationID: ids[4], RequestDigest: upgradeRequest,
			ProfileRevisionID: profile.ID, ProfileRevisionNumber: 1, ProfileSpecDigest: profileDigest,
			ApprovedBy: ids[0], ApprovedAt: parse(stamp(2)), State: appaccess.GatewayProfileUpgradeCommitted,
			StateSequence: 3, UpdatedAt: parse(stamp(4))},
		upgradeEvents: []legacyNativeBatchEvent{{1, "prepared", sql.NullString{}, sql.NullString{}, stamp(2)},
			{2, "serving", sql.NullString{}, sql.NullString{}, stamp(3)}, {3, "committed", sql.NullString{}, sql.NullString{}, stamp(4)}},
	}
	for index := 0; index < 2; index++ {
		appID, allocationID, ownerID, revisionID, attemptID, disableID := ids[5+index], ids[7+index], ids[9+index], ids[11+index], ids[13+index], ids[15+index]
		value.applications = append(value.applications, legacyNativeBatchApplicationRow{id: appID,
			slug: fmt.Sprintf("native-batch-%d", index+1), name: fmt.Sprintf("Native Batch %d", index+1),
			machineID: ids[1], status: "draft", createdAt: stamp(5 + index), updatedAt: stamp(5 + index)})
		allocation := appaccess.Allocation{ID: allocationID, AppID: appID, Port: uint16(8100 + index),
			OwnerOperationID: ownerID, OwnerRevisionID: revisionID,
			ReservationDigest:        legacyReservationRequestDigest(t, appID, profile.ID),
			GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: 1,
			State: appaccess.AllocationActive, ReservedAt: parse(stamp(7 + index))}
		accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
		if err != nil {
			t.Fatal(err)
		}
		revision := appaccess.AppAccessRevision{ID: revisionID, AppID: appID, RevisionNumber: 1,
			OperationID: ownerID, SpecDigest: accessDigest, ApprovedBy: ids[0], ApprovedAt: parse(stamp(9 + index)), Allocation: allocation}
		grantSpec := appaccess.AppAccessGrantSpecFor(revision, profile)
		grant := appaccess.AppAccessGrantClaim{AttemptID: attemptID,
			RequestDigest: legacyGrantRequestDigest(t, attemptID, grantSpec), Spec: grantSpec,
			ApprovedAt: revision.ApprovedAt, CreatedAt: parse(stamp(11 + index)),
			State: appaccess.AppAccessGrantCommitted, StateSequence: 4, UpdatedAt: parse(stamp(15 + index)),
			Proof: &appaccess.AppAccessGrantProof{GatewayOperationID: ids[4], ProtectedStateDigest: strings.Repeat(string(rune('a'+index)), 64),
				ObservedAt: parse(stamp(15 + index))}}
		value.access = append(value.access, legacyNativeBatchAccessRow{allocation: allocation, revision: revision,
			revisionRequest: legacyAccessApprovalRequestDigest(t, appID, allocationID, accessDigest, ids[0]), grant: grant,
			grantEvents: []legacyNativeBatchEvent{{1, "prepared", sql.NullString{}, sql.NullString{}, stamp(11 + index)},
				{2, "applying", sql.NullString{}, sql.NullString{}, stamp(12 + index)},
				{3, "db_active", sql.NullString{}, sql.NullString{}, stamp(13 + index)},
				{4, "committed", sql.NullString{String: ids[4], Valid: true}, sql.NullString{String: grant.Proof.ProtectedStateDigest, Valid: true}, stamp(15 + index)}},
			disableOperation: disableID})
	}
	return value
}
