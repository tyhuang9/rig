package main

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/database"
)

type legacyNativeBatchExpectedApp struct {
	appID, disableOperation string
}

type legacyNativeBatchExtractionExpectation struct {
	actorID, upgradeOperationID string
	apps                        []legacyNativeBatchExpectedApp
}

func legacyNativeBatchExtractionExpectationFor(authority legacyNativeBatchAuthority) legacyNativeBatchExtractionExpectation {
	value := legacyNativeBatchExtractionExpectation{actorID: authority.actor.id,
		upgradeOperationID: authority.upgrade.OperationID}
	for _, access := range authority.access {
		value.apps = append(value.apps, legacyNativeBatchExpectedApp{appID: access.allocation.AppID,
			disableOperation: access.disableOperation})
	}
	return value
}

func validateLegacyNativeBatchExtractionSnapshot(snapshot appaccess.HostingGatewayStartupSnapshot,
	expected legacyNativeBatchExtractionExpectation,
) error {
	if expected.actorID == "" || expected.upgradeOperationID == "" || len(expected.apps) != 2 ||
		expected.apps[0].appID == "" || expected.apps[1].appID == "" ||
		expected.apps[0].appID == expected.apps[1].appID ||
		expected.apps[0].disableOperation == "" || expected.apps[1].disableOperation == "" ||
		expected.apps[0].disableOperation == expected.apps[1].disableOperation {
		return fmt.Errorf("invalid native authority extraction expectation")
	}
	if snapshot.Upgrades.CurrentProfile == nil || len(snapshot.Upgrades.Claims) != 1 ||
		snapshot.Upgrades.Claims[0].Claim.State != appaccess.GatewayProfileUpgradeCommitted ||
		snapshot.Upgrades.Claims[0].Claim.OperationID != expected.upgradeOperationID ||
		len(snapshot.Grants.Claims) != len(expected.apps) || len(snapshot.Disables.Claims) != 0 ||
		len(snapshot.Rebind.History) != 0 || snapshot.Rebind.Active != nil ||
		snapshot.Rebind.CurrentSource == nil ||
		snapshot.Rebind.CurrentSource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
		snapshot.Rebind.CurrentSource.OperationID != expected.upgradeOperationID {
		return fmt.Errorf("native authority startup census does not identify one settled upgrade with two grants")
	}
	wanted := make(map[string]bool, len(expected.apps))
	for _, app := range expected.apps {
		wanted[app.appID] = true
	}
	seen := make(map[string]bool, len(expected.apps))
	for _, entry := range snapshot.Grants.Claims {
		appID := entry.Claim.Spec.AppID
		if !wanted[appID] || seen[appID] ||
			entry.Claim.State != appaccess.AppAccessGrantCommitted || entry.Claim.StateSequence != 4 ||
			entry.Claim.Proof == nil || entry.Claim.RetiredAt != nil || entry.DisableIntent != nil ||
			!entry.AccessHeadCurrent || !entry.ProfileHeadCurrent || !entry.ApproverIsAdministrator ||
			entry.CurrentGatewaySource.Kind != appaccess.GatewayRebindSourceGatewayUpgrade ||
			entry.CurrentGatewaySource.OperationID != expected.upgradeOperationID {
			return fmt.Errorf("native authority grant census contains a foreign or incomplete claim")
		}
		seen[appID] = true
	}
	if len(seen) != len(expected.apps) {
		return fmt.Errorf("native authority grant census omitted an expected application")
	}
	return nil
}

func readLegacyNativeBatchAuthority(t *testing.T, ctx context.Context, db *sql.DB,
	expected legacyNativeBatchExtractionExpectation,
) legacyNativeBatchAuthority {
	t.Helper()
	snapshot, err := appaccess.New(db).HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal("read native authority startup census:", err)
	}
	if err := validateLegacyNativeBatchExtractionSnapshot(snapshot, expected); err != nil {
		t.Fatal(err)
	}

	result := legacyNativeBatchAuthority{profile: *snapshot.Upgrades.CurrentProfile,
		upgrade: snapshot.Upgrades.Claims[0].Claim}
	if err := db.QueryRowContext(ctx, `SELECT id,username,passphrase_hash,role,created_at,updated_at
		FROM users WHERE id=?`, expected.actorID).Scan(&result.actor.id, &result.actor.username,
		&result.actor.passphraseHash, &result.actor.role, &result.actor.createdAt, &result.actor.updatedAt); err != nil {
		t.Fatal("read exact native authority administrator:", err)
	}
	if result.actor.role != "administrator" {
		t.Fatal("native authority actor is not an administrator")
	}
	if err := db.QueryRowContext(ctx, `SELECT request_digest FROM lan_gateway_profile_revisions WHERE id=? AND revision_number=?`,
		result.profile.ID, result.profile.RevisionNumber).Scan(&result.profileRequest); err != nil {
		t.Fatal("read exact native authority gateway profile request:", err)
	}
	result.upgradeEvents = readLegacyNativeBatchEvents(t, ctx, db,
		`SELECT sequence,state,NULL,NULL,created_at FROM lan_gateway_upgrade_claim_events
		 WHERE operation_id=? ORDER BY sequence`, result.upgrade.OperationID)

	for _, expectedApp := range expected.apps {
		var app legacyNativeBatchApplicationRow
		if err := db.QueryRowContext(ctx, `SELECT id,slug,name,description,source_path,active_machine_id,
			status,created_at,updated_at,archived_at FROM applications WHERE id=?`, expectedApp.appID).Scan(
			&app.id, &app.slug, &app.name, &app.description, &app.sourcePath, &app.machineID,
			&app.status, &app.createdAt, &app.updatedAt, &app.archivedAt); err != nil {
			t.Fatal("read exact native authority application:", err)
		}
		if app.archivedAt.Valid || app.machineID == "" {
			t.Fatal("native authority application is archived or unbound")
		}
		result.applications = append(result.applications, app)
	}
	if result.applications[0].machineID != result.applications[1].machineID {
		t.Fatal("native authority applications do not share the retained local machine")
	}
	if err := db.QueryRowContext(ctx, `SELECT id,name,mode,status,last_heartbeat_at,os,architecture,
		hostname,docker_version,compose_version,agent_version,capabilities_json,resources_json,created_at,updated_at
		FROM machines WHERE id=?`, result.applications[0].machineID).Scan(&result.machine.id, &result.machine.name,
		&result.machine.mode, &result.machine.status, &result.machine.lastHeartbeat, &result.machine.osName,
		&result.machine.architecture, &result.machine.hostname, &result.machine.dockerVersion,
		&result.machine.composeVersion, &result.machine.agentVersion, &result.machine.capabilitiesJSON,
		&result.machine.resourcesJSON, &result.machine.createdAt, &result.machine.updatedAt); err != nil {
		t.Fatal("read exact native authority machine:", err)
	}

	byApp := make(map[string]appaccess.AppAccessGrantStartupClaim, len(snapshot.Grants.Claims))
	for _, entry := range snapshot.Grants.Claims {
		byApp[entry.Claim.Spec.AppID] = entry
	}
	for _, expectedApp := range expected.apps {
		entry := byApp[expectedApp.appID]
		access := legacyNativeBatchAccessRow{allocation: entry.Allocation, revision: entry.Revision,
			grant: entry.Claim, disableOperation: expectedApp.disableOperation}
		if err := db.QueryRowContext(ctx, `SELECT request_digest FROM lan_app_access_revisions
			WHERE app_id=? AND id=? AND revision_number=?`, expectedApp.appID, entry.Revision.ID,
			entry.Revision.RevisionNumber).Scan(&access.revisionRequest); err != nil {
			t.Fatal("read exact native authority access request:", err)
		}
		access.grantEvents = readLegacyNativeBatchEvents(t, ctx, db,
			`SELECT sequence,state,gateway_operation_id,protected_state_digest,created_at
			 FROM lan_app_access_grant_claim_events WHERE attempt_id=? ORDER BY sequence`, entry.Claim.AttemptID)
		result.access = append(result.access, access)
	}
	if err := validateLegacyNativeBatchAuthority(result); err != nil {
		t.Fatal(err)
	}
	return result
}

func readLegacyNativeBatchEvents(t *testing.T, ctx context.Context, db *sql.DB,
	query, identity string,
) []legacyNativeBatchEvent {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []legacyNativeBatchEvent
	for rows.Next() {
		var value legacyNativeBatchEvent
		if err := rows.Scan(&value.sequence, &value.state, &value.gatewayOperationID,
			&value.protectedStateDigest, &value.createdAt); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLegacyNativeBatchAuthorityExtractorRoundTrip(t *testing.T) {
	ctx := context.Background()
	original := deterministicLegacyNativeBatchAuthority(t)
	source := legacyPairCompositionDBThrough028(t)
	seedLegacyNativeAuthorityThrough028(t, source, original, false)
	if err := database.Migrate(source); err != nil {
		t.Fatal("migrate native authority source through latest:", err)
	}
	assertLegacyNativeBatchSchemaGuards(t, source, true)
	assertLegacyNativeBatchExcludedRows(t, source, true)

	expected := legacyNativeBatchExtractionExpectationFor(original)
	snapshot, err := appaccess.New(source).HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyNativeBatchExtractionSnapshot(snapshot, expected); err != nil {
		t.Fatal("valid native authority census rejected:", err)
	}
	for name, mutate := range map[string]func(*appaccess.HostingGatewayStartupSnapshot){
		"wrong current source": func(value *appaccess.HostingGatewayStartupSnapshot) {
			current := *value.Rebind.CurrentSource
			current.Kind = appaccess.GatewayRebindSourceGatewayRebind
			value.Rebind.CurrentSource = &current
		},
		"foreign grant census": func(value *appaccess.HostingGatewayStartupSnapshot) {
			value.Grants.Claims = append(value.Grants.Claims, appaccess.AppAccessGrantStartupClaim{})
		},
		"incomplete grant census": func(value *appaccess.HostingGatewayStartupSnapshot) {
			value.Grants.Claims = value.Grants.Claims[:1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := snapshot
			changed.Grants.Claims = append([]appaccess.AppAccessGrantStartupClaim(nil), snapshot.Grants.Claims...)
			mutate(&changed)
			if err := validateLegacyNativeBatchExtractionSnapshot(changed, expected); err == nil {
				t.Fatal("invalid native authority census was accepted")
			}
		})
	}

	extracted := readLegacyNativeBatchAuthority(t, ctx, source, expected)
	for name, values := range map[string][2]any{
		"actor":           {extracted.actor, original.actor},
		"machine":         {extracted.machine, original.machine},
		"applications":    {extracted.applications, original.applications},
		"profile":         {extracted.profile, original.profile},
		"profile request": {extracted.profileRequest, original.profileRequest},
		"upgrade":         {extracted.upgrade, original.upgrade},
		"upgrade events":  {extracted.upgradeEvents, original.upgradeEvents},
	} {
		if !reflect.DeepEqual(values[0], values[1]) {
			t.Fatalf("native authority extractor changed immutable %s values", name)
		}
	}
	if len(extracted.access) != len(original.access) {
		t.Fatal("native authority extractor changed immutable access cardinality")
	}
	for index := range original.access {
		for name, values := range map[string][2]any{
			"allocation":        {extracted.access[index].allocation, original.access[index].allocation},
			"revision":          {extracted.access[index].revision, original.access[index].revision},
			"revision request":  {extracted.access[index].revisionRequest, original.access[index].revisionRequest},
			"grant":             {extracted.access[index].grant, original.access[index].grant},
			"grant events":      {extracted.access[index].grantEvents, original.access[index].grantEvents},
			"disable operation": {extracted.access[index].disableOperation, original.access[index].disableOperation},
		} {
			if !reflect.DeepEqual(values[0], values[1]) {
				t.Fatalf("native authority extractor changed immutable access %s values at index %d", name, index)
			}
		}
	}
	target := legacyPairCompositionDBThrough028(t)
	batch := seedLegacyNativePendingLANBatch(t, target, extracted)
	if len(batch.operationIDs) != 2 || len(batch.claims) != 2 {
		t.Fatal("native authority roundtrip did not produce the exact migrated batch")
	}
}
