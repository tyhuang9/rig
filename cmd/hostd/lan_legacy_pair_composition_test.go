package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedingress"
)

type legacyPairCapturingIngress struct {
	*fakeGatewayStartupBatchIngress
	t                *testing.T
	expectedGrant    generatedingress.GatewayV2LANStartupClaim
	expectedDisable  generatedingress.GatewayV2LANDisableStartupClaim
	forwardedGrants  []generatedingress.GatewayV2LANStartupClaim
	forwardedDisable []generatedingress.GatewayV2LANDisableStartupClaim
	validated        bool
}

func (f *legacyPairCapturingIngress) QuarantineGatewayV2LANAccessRecoveryBatch(ctx context.Context,
	grants []generatedingress.GatewayV2LANStartupClaim,
	disables []generatedingress.GatewayV2LANDisableStartupClaim,
) error {
	f.t.Helper()
	f.forwardedGrants = append([]generatedingress.GatewayV2LANStartupClaim(nil), grants...)
	f.forwardedDisable = append([]generatedingress.GatewayV2LANDisableStartupClaim(nil), disables...)
	if len(grants) != 1 || grants[0] != f.expectedGrant {
		f.t.Fatalf("forwarded migrated grant=%#v want=%#v", grants, f.expectedGrant)
	}
	if len(disables) != 1 || disables[0] != f.expectedDisable {
		f.t.Fatalf("forwarded migrated disable=%#v want=%#v", disables, f.expectedDisable)
	}
	f.validated = true
	return f.fakeGatewayStartupBatchIngress.QuarantineGatewayV2LANAccessRecoveryBatch(
		ctx, grants, disables)
}

func TestMigratedLegacyPairRecoveryComposition(t *testing.T) {
	ctx := context.Background()
	db, repository, session, grant, disable, gatewayOperationID := migratedLegacyPairCompositionFixture(t)
	snapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		gateway, gatewayErr := repository.GatewayUpgradeStartupSnapshot(ctx)
		grants, grantErr := repository.AppAccessGrantStartupSnapshot(ctx)
		disables, disableErr := repository.AppAccessDisableStartupSnapshot(ctx)
		t.Fatalf("combined snapshot: %v; gateway=%#v gateway_error=%v grants=%#v grant_error=%v disables=%#v disable_error=%v",
			err, gateway, gatewayErr, grants, grantErr, disables, disableErr)
	}
	if len(snapshot.Grants.Claims) != 1 || len(snapshot.Disables.Claims) != 1 ||
		len(snapshot.Upgrades.Claims) != 1 || snapshot.Upgrades.Claims[0].Claim.State != appaccess.GatewayProfileUpgradeCommitted {
		t.Fatalf("migrated startup census: %#v", snapshot)
	}

	normalUpgrade := generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupNormalV2, OperationID: gatewayOperationID,
	}
	quarantinedUpgrade := generatedingress.GatewayV2StartupInspection{
		Disposition: generatedingress.GatewayV2StartupRecoveryOnly, OperationID: gatewayOperationID,
	}
	initialGate := gatewayStartup{snapshot: snapshot, inspection: normalUpgrade, recoveryBatch: true}

	t.Run("quarantine failures stop before head dispatch", func(t *testing.T) {
		t.Run("changed census", func(t *testing.T) {
			ingress := &fakeGatewayStartupBatchIngress{upgrade: quarantinedUpgrade,
				headPresent: true, head: legacyGrantRecoveryHead(grant, disable, 0)}
			changed := snapshot
			changed.Grants.Claims = append([]appaccess.AppAccessGrantStartupClaim(nil), snapshot.Grants.Claims...)
			changed.Grants.Claims[0].AccessHeadCurrent = false
			if _, err := quarantineLANRecoveryBatchStartup(ctx, initialGate, ingress,
				func(context.Context) (appaccess.HostingGatewayStartupSnapshot, error) { return changed, nil }); err == nil ||
				!strings.Contains(err.Error(), "snapshot changed") {
				t.Fatalf("changed census error=%v", err)
			}
			if ingress.observeCalls != 0 || ingress.upgradeCalls != 0 {
				t.Fatalf("changed census reached protected inspection: observe=%d upgrade=%d",
					ingress.observeCalls, ingress.upgradeCalls)
			}
		})

		t.Run("missing protected head", func(t *testing.T) {
			ingress := &fakeGatewayStartupBatchIngress{upgrade: quarantinedUpgrade}
			if _, err := quarantineLANRecoveryBatchStartup(ctx, initialGate, ingress,
				repository.HostingGatewayStartupSnapshot); err == nil ||
				!strings.Contains(err.Error(), "protected LAN recovery batch disappeared") {
				t.Fatalf("missing protected head error=%v", err)
			}
			if ingress.observeCalls != 1 {
				t.Fatalf("protected head observations=%d", ingress.observeCalls)
			}
		})

		t.Run("quarantine error", func(t *testing.T) {
			quarantineErr := errors.New("test quarantine failure")
			ingress := &fakeGatewayStartupBatchIngress{quarantineErr: quarantineErr}
			if _, err := quarantineLANRecoveryBatchStartup(ctx, initialGate, ingress,
				repository.HostingGatewayStartupSnapshot); !errors.Is(err, quarantineErr) {
				t.Fatalf("quarantine error=%v", err)
			}
			if ingress.observeCalls != 0 || ingress.upgradeCalls != 0 {
				t.Fatalf("quarantine failure reached protected inspection: observe=%d upgrade=%d",
					ingress.observeCalls, ingress.upgradeCalls)
			}
		})
	})

	grantIngress := newLegacyPairCapturingIngress(t, snapshot, &fakeGatewayStartupBatchIngress{
		upgrade: quarantinedUpgrade, headPresent: true,
		head: legacyGrantRecoveryHead(grant, disable, 0),
	})
	grantGate, err := quarantineLANRecoveryBatchStartup(ctx, initialGate, grantIngress,
		repository.HostingGatewayStartupSnapshot)
	if err != nil {
		t.Fatalf("quarantine migrated grant head: %v", err)
	}
	if !grantIngress.validated || len(grantIngress.forwardedGrants) != 1 ||
		len(grantIngress.forwardedDisable) != 1 {
		t.Fatal("migrated recovery census was not validated before protected head selection")
	}
	assertLegacyRecoveryPin(t, grantGate, controller.RecoveryLANGrant, grant.AttemptID, grant.Spec.AppID, 0, 2)
	grantReader, err := newLANRecoveryHeadReader(repository, grantIngress, grantGate)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyRecoveryHTTP(t, db, session, grantReader, grantGate, apicontract.LANRecoveryHead{
		Kind: controller.RecoveryLANGrant, OperationID: grant.AttemptID, AppID: grant.Spec.AppID,
		Batch: true, BatchPosition: 1, BatchCount: 2, ClaimState: string(grant.State),
		AccessRevisionID: grant.Spec.AccessRevisionID, AccessRevisionNumber: grant.Spec.AccessRevisionNumber,
		AllocationID: grant.Spec.AllocationID, OwnerOperationID: grant.Spec.OwnerOperationID,
		Port: int(grant.Spec.Port), ApprovalDigest: grant.Spec.AccessSpecDigest,
	})

	grantIngress.head = legacyGrantRecoveryHead(grant, disable, 1)
	if _, err := grantReader.ReadLANRecoveryHead(ctx); err == nil ||
		!strings.Contains(err.Error(), "protected LAN recovery head changed") {
		t.Fatalf("same-startup reader accepted disable head: %v", err)
	}
	grantIngress.head = legacyGrantRecoveryHead(grant, disable, 0)

	grant, changed, err := repository.ResolveAppAccessGrantClaim(ctx,
		appaccess.AppAccessGrantClaimOwnerFor(grant), appaccess.AppAccessGrantPrepared,
		appaccess.AppAccessGrantRolledBack, appaccess.AppAccessGrantProof{
			GatewayOperationID:   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			ProtectedStateDigest: strings.Repeat("a", 64),
		})
	if err != nil || !changed || grant.State != appaccess.AppAccessGrantRolledBack {
		t.Fatalf("exact migrated grant rollback changed=%t claim=%#v error=%v", changed, grant, err)
	}
	if _, err := grantReader.ReadLANRecoveryHead(ctx); err == nil ||
		!strings.Contains(err.Error(), "census changed") {
		t.Fatalf("old reader survived SQLite recovery transition: %v", err)
	}

	restartedSnapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restartedSnapshot.Grants.Claims) != 1 || len(restartedSnapshot.Disables.Claims) != 1 {
		t.Fatalf("restarted legacy census grants=%d disables=%d",
			len(restartedSnapshot.Grants.Claims), len(restartedSnapshot.Disables.Claims))
	}
	restartedGrant := restartedSnapshot.Grants.Claims[0]
	restartedDisable := restartedSnapshot.Disables.Claims[0]
	if restartedGrant.Claim.State != appaccess.AppAccessGrantRolledBack ||
		restartedDisable.Claim.State != appaccess.AppAccessDisablePrepared ||
		restartedGrant.Allocation.ID != grant.Spec.AllocationID ||
		restartedGrant.Allocation.State != appaccess.AllocationActive ||
		restartedGrant.Allocation.ReleasedAt != nil ||
		restartedGrant.Allocation.OwnerOperationID != grant.Spec.OwnerOperationID ||
		restartedGrant.Allocation.OwnerRevisionID != grant.Spec.AccessRevisionID {
		t.Fatalf("migration-032 post-rollback invariant grant=%#v disable=%#v allocation=%#v",
			restartedGrant.Claim, restartedDisable.Claim, restartedGrant.Allocation)
	}
	disableIngress := newLegacyPairCapturingIngress(t, restartedSnapshot, &fakeGatewayStartupBatchIngress{
		upgrade: quarantinedUpgrade, headPresent: true,
		head: legacyGrantRecoveryHead(grant, disable, 1),
	})
	disableGate, err := quarantineLANRecoveryBatchStartup(ctx, gatewayStartup{
		snapshot: restartedSnapshot, inspection: normalUpgrade, recoveryBatch: true,
	}, disableIngress, repository.HostingGatewayStartupSnapshot)
	if err != nil {
		t.Fatalf("fresh restart quarantine for migrated disable: %v", err)
	}
	if !disableIngress.validated || len(disableIngress.forwardedGrants) != 1 ||
		len(disableIngress.forwardedDisable) != 1 {
		t.Fatal("fresh recovery census was not validated before protected disable head selection")
	}
	assertLegacyRecoveryPin(t, disableGate, controller.RecoveryLANDisable, disable.OperationID,
		disable.Spec.AppID, 1, 2)
	disableReader, err := newLANRecoveryHeadReader(repository, disableIngress, disableGate)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyRecoveryHTTP(t, db, session, disableReader, disableGate, apicontract.LANRecoveryHead{
		Kind: controller.RecoveryLANDisable, OperationID: disable.OperationID, AppID: disable.Spec.AppID,
		Batch: true, BatchPosition: 2, BatchCount: 2, ClaimState: string(disable.State),
		AccessRevisionID: disable.Spec.AccessRevisionID, AccessRevisionNumber: disable.Spec.AccessRevisionNumber,
		AllocationID: disable.Spec.AllocationID, OwnerOperationID: disable.Spec.OwnerOperationID,
		Port: int(disable.Spec.Port), ApprovalDigest: disable.SpecDigest,
	})
}

func newLegacyPairCapturingIngress(t *testing.T, snapshot appaccess.HostingGatewayStartupSnapshot,
	inner *fakeGatewayStartupBatchIngress,
) *legacyPairCapturingIngress {
	t.Helper()
	if len(snapshot.Grants.Claims) != 1 || len(snapshot.Disables.Claims) != 1 {
		t.Fatalf("legacy pair census grants=%d disables=%d", len(snapshot.Grants.Claims), len(snapshot.Disables.Claims))
	}
	grant := snapshot.Grants.Claims[0]
	disable := snapshot.Disables.Claims[0]
	if grant.DisableIntent == nil || grant.DisableIntent.OperationID != disable.Claim.OperationID {
		t.Fatalf("legacy grant/disable lineage grant=%#v disable=%#v", grant.DisableIntent, disable.Claim)
	}
	if disable.SourceGrant != nil || disable.Claim.SourceGrantAttemptID != "" {
		t.Fatalf("migrated prepared disable acquired a source grant: entry=%#v claim=%#v",
			disable.SourceGrant, disable.Claim)
	}
	expectedGrant := generatedingress.GatewayV2LANStartupClaim{
		Request: generatedingress.GatewayV2LANGrantRequest{
			AttemptID: grant.Claim.AttemptID, ClaimRequestDigest: grant.Claim.RequestDigest,
			AppID: grant.Claim.Spec.AppID, AllocationID: grant.Claim.Spec.AllocationID,
			OwnerOperationID: grant.Claim.Spec.OwnerOperationID, Port: grant.Claim.Spec.Port,
			AccessRevisionID:     grant.Claim.Spec.AccessRevisionID,
			AccessRevisionNumber: grant.Claim.Spec.AccessRevisionNumber,
			AccessSpecDigest:     grant.Claim.Spec.AccessSpecDigest, ApprovedBy: grant.Claim.Spec.ApprovedBy,
			GatewayProfileRevisionID:     grant.Claim.Spec.GatewayProfileRevisionID,
			GatewayProfileRevisionNumber: grant.Claim.Spec.GatewayProfileRevisionNumber,
			GatewayProfileSpecDigest:     grant.Claim.Spec.GatewayProfileSpecDigest,
		},
		State: grant.Claim.State, StateSequence: grant.Claim.StateSequence,
		DisableIntentOperationID: disable.Claim.OperationID,
	}
	expectedDisable := generatedingress.GatewayV2LANDisableStartupClaim{
		Request: generatedingress.GatewayV2LANDisableRequest{
			OperationID: disable.Claim.OperationID, RequestDigest: disable.Claim.RequestDigest,
			SpecDigest: disable.Claim.SpecDigest, AppID: disable.Claim.Spec.AppID,
			AllocationID:     disable.Claim.Spec.AllocationID,
			OwnerOperationID: disable.Claim.Spec.OwnerOperationID, Port: disable.Claim.Spec.Port,
			AccessRevisionID:     disable.Claim.Spec.AccessRevisionID,
			AccessRevisionNumber: disable.Claim.Spec.AccessRevisionNumber,
			AccessSpecDigest:     disable.Revision.SpecDigest, ApprovedBy: disable.Claim.ApprovedBy,
			GatewayProfileRevisionID:     disable.Claim.Spec.GatewayProfileRevisionID,
			GatewayProfileRevisionNumber: disable.Claim.Spec.GatewayProfileRevisionNumber,
			GatewayProfileSpecDigest:     disable.Profile.SpecDigest,
		},
		State: disable.Claim.State, StateSequence: disable.Claim.StateSequence,
	}
	return &legacyPairCapturingIngress{fakeGatewayStartupBatchIngress: inner, t: t,
		expectedGrant: expectedGrant, expectedDisable: expectedDisable}
}

func migratedLegacyPairCompositionFixture(t *testing.T) (*sql.DB, *appaccess.Repository, auth.Session,
	appaccess.AppAccessGrantClaim, appaccess.AppAccessDisableClaim, string,
) {
	t.Helper()
	db := legacyPairCompositionDBThrough028(t)
	const (
		administrator    = "11111111-1111-4111-8111-111111111111"
		profileID        = "22222222-2222-4222-8222-222222222222"
		profileOperation = "33333333-3333-4333-8333-333333333333"
		appID            = "44444444-4444-4444-8444-444444444444"
		allocationID     = "55555555-5555-4555-8555-555555555555"
		ownerOperation   = "66666666-6666-4666-8666-666666666666"
		revisionID       = "77777777-7777-4777-8777-777777777777"
		disableOperation = "88888888-8888-4888-8888-888888888888"
		grantAttempt     = "99999999-9999-4999-8999-999999999999"
		gatewayOperation = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		stamp            = "2026-09-30T12:00:00.000000000Z"
	)
	profileSpec := appaccess.GatewayProfileSpec{SelectedIPv4: "192.168.44.20",
		InterfaceID: "migrated-legacy-composition", PortStart: 8100, PortEnd: 8101}
	profileDigest, err := appaccess.GatewayProfileSpecDigest(profileSpec)
	if err != nil {
		t.Fatal(err)
	}
	profileRequestDigest := legacyGatewayProfileRequestDigest(t, profileOperation, profileSpec,
		profileDigest, administrator)
	allocation := appaccess.Allocation{ID: allocationID, AppID: appID, Port: 8100,
		OwnerOperationID: ownerOperation, OwnerRevisionID: revisionID,
		GatewayProfileRevisionID: profileID, GatewayProfileRevisionNumber: 1}
	accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	revision := appaccess.AppAccessRevision{ID: revisionID, AppID: appID, RevisionNumber: 1,
		OperationID: ownerOperation, SpecDigest: accessDigest, ApprovedBy: administrator,
		Allocation: allocation}
	grantSpec := appaccess.AppAccessGrantSpecFor(revision, appaccess.GatewayProfileRevision{
		ID: profileID, RevisionNumber: 1, OperationID: profileOperation, Spec: profileSpec,
		SpecDigest: profileDigest, ApprovedBy: administrator,
	})
	grantRequestDigest := legacyGrantRequestDigest(t, grantAttempt, grantSpec)
	reservationRequestDigest := legacyReservationRequestDigest(t, appID, profileID)
	accessRequestDigest := legacyAccessApprovalRequestDigest(t, appID, allocationID,
		accessDigest, administrator)
	disableSpec := appaccess.AppAccessDisableSpecFor(revision)
	disableSpecDigest, err := appaccess.AppAccessDisableSpecDigest(disableSpec)
	if err != nil {
		t.Fatal(err)
	}
	disableRequestDigest := legacyDisableApprovalRequestDigest(t, appaccess.AllocationOwner{
		AllocationID: allocationID, AppID: appID, OperationID: ownerOperation,
		AccessRevisionID: revisionID,
	}, disableSpecDigest, administrator)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
			VALUES(?,'legacy-composition-admin','hash','administrator',?,?)`, []any{administrator, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at)
			VALUES(?,'legacy-composition','Legacy Composition','draft',?,?)`, []any{appID, stamp, stamp}},
		{`INSERT INTO lan_gateway_profile_revisions(
			id,revision_number,operation_id,request_digest,approval_action,selected_ipv4,
			interface_id,port_start,port_end,spec_digest,approved_by,approved_at
		) VALUES(?,1,?,?,'configure_lan_gateway',?,?,?,?,?,?,?)`, []any{profileID, profileOperation,
			profileRequestDigest, profileSpec.SelectedIPv4, profileSpec.InterfaceID, profileSpec.PortStart,
			profileSpec.PortEnd, profileDigest, administrator, stamp}},
		{`UPDATE lan_gateway_profile_heads SET revision_id=?,revision_number=1,updated_at=? WHERE singleton=1`,
			[]any{profileID, stamp}},
		{`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,1,'reserved',?)`, []any{allocationID, appID, 8100, ownerOperation,
			reservationRequestDigest, profileID, stamp}},
		{`INSERT INTO lan_app_access_revisions(
			id,app_id,revision_number,operation_id,request_digest,approval_action,allocation_id,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,1,?,?,'enable_lan_access',?,8100,?,1,?,?,?)`, []any{revisionID, appID,
			ownerOperation, accessRequestDigest, allocationID, profileID, accessDigest, administrator, stamp}},
		{`UPDATE lan_app_access_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`,
			[]any{revisionID, stamp, appID}},
		{`UPDATE lan_port_allocations SET owner_revision_id=? WHERE id=?`, []any{revisionID, allocationID}},
		{`UPDATE lan_port_allocations SET state='active' WHERE id=?`, []any{allocationID}},
		{`INSERT INTO lan_app_access_grant_claims(
			attempt_id,request_digest,approval_action,app_id,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			access_spec_digest,allocated_port,gateway_profile_revision_id,
			gateway_profile_revision_number,gateway_profile_spec_digest,approved_by,
			approved_at,created_at,state,state_sequence,updated_at
		) VALUES(?,?,'enable_lan_access',?,?,?, ?,1,?,8100,?,1,?,?,? ,?,'prepared',1,?)`,
			[]any{grantAttempt, grantRequestDigest, appID, allocationID, ownerOperation, revisionID,
				accessDigest, profileID, profileDigest, administrator, stamp, stamp, stamp}},
		{`INSERT INTO lan_app_access_disable_intents(
			operation_id,app_id,request_digest,approval_action,allocation_id,
			allocation_owner_operation_id,access_revision_id,access_revision_number,
			allocated_port,gateway_profile_revision_id,gateway_profile_revision_number,
			spec_digest,approved_by,approved_at
		) VALUES(?,?,?,'disable_lan_access',?,?,?,?,8100,?,1,?,?,?)`, []any{disableOperation, appID,
			disableRequestDigest, allocationID, ownerOperation, revisionID, 1, profileID,
			disableSpecDigest, administrator, stamp}},
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed legacy pair statement %d: %v", index+1, err)
		}
	}
	repository := appaccess.New(db)
	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{ProfileRevisionID: profileID,
		ProfileRevisionNumber: 1, ProfileSpecDigest: profileDigest}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, created, err := repository.ClaimGatewayProfileUpgrade(context.Background(),
		appaccess.ClaimGatewayProfileUpgradeInput{OperationID: gatewayOperation, Spec: upgradeSpec,
			Approval: appaccess.Approval{Action: appaccess.ActionUpgradeGateway,
				SpecDigest: upgradeDigest, ActorID: administrator}})
	if err != nil || !created {
		t.Fatalf("claim committed gateway history created=%t claim=%#v error=%v", created, upgrade, err)
	}
	upgradeOwner := appaccess.GatewayProfileUpgradeClaimOwner{OperationID: upgrade.OperationID,
		ProfileRevisionID: upgrade.ProfileRevisionID, ProfileRevisionNumber: upgrade.ProfileRevisionNumber}
	if _, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), upgradeOwner,
		appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeServing); err != nil || !changed {
		t.Fatalf("advance gateway history to serving changed=%t error=%v", changed, err)
	}
	if _, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(context.Background(), upgradeOwner,
		appaccess.GatewayProfileUpgradeServing, appaccess.GatewayProfileUpgradeCommitted); err != nil || !changed {
		t.Fatalf("commit gateway history changed=%t error=%v", changed, err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("migrate legacy pair and committed gateway history through latest: %v", err)
	}
	var migration032 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations
		WHERE version='032_lan_app_access_legacy_pair_rollback.sql'`).Scan(&migration032); err != nil || migration032 != 1 {
		t.Fatalf("migration 032 record=%d error=%v", migration032, err)
	}
	grant, err := repository.AppAccessGrantClaim(context.Background(), grantAttempt)
	if err != nil {
		t.Fatal(err)
	}
	disable, err := repository.AppAccessDisableClaim(context.Background(), disableOperation)
	if err != nil {
		t.Fatal(err)
	}
	if disable.SourceGrantAttemptID != "" {
		t.Fatalf("migration linked prepared grant as committed source: %q", disable.SourceGrantAttemptID)
	}
	session, err := auth.New(db).NewSession(administrator)
	if err != nil {
		t.Fatal(err)
	}
	return db, repository, session, grant, disable, gatewayOperation
}

func legacyPairCompositionDBThrough028(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrationRoot := filepath.Join("..", "..", "internal", "database", "migrations")
	entries, err := os.ReadDir(migrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") ||
			entry.Name() > "028_lan_app_access_grant_claims.sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrationRoot, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,datetime('now'))`,
			entry.Name()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func legacyGatewayProfileRequestDigest(t *testing.T, operationID string, spec appaccess.GatewayProfileSpec,
	specDigest, actorID string,
) string {
	t.Helper()
	value := struct {
		Expected int64                        `json:"expectedRevisionNumber"`
		Spec     appaccess.GatewayProfileSpec `json:"spec"`
		Approval appaccess.Approval           `json:"approval"`
	}{0, spec, appaccess.Approval{Action: appaccess.ActionConfigureGateway,
		SpecDigest: specDigest, ActorID: actorID}}
	return legacyJSONDigest(t, value)
}

func legacyGrantRequestDigest(t *testing.T, attemptID string, spec appaccess.AppAccessGrantSpec) string {
	t.Helper()
	value := struct {
		Version   int                          `json:"version"`
		AttemptID string                       `json:"attemptId"`
		Spec      appaccess.AppAccessGrantSpec `json:"spec"`
	}{Version: 1, AttemptID: attemptID, Spec: spec}
	return legacyJSONDigest(t, value)
}

func legacyReservationRequestDigest(t *testing.T, appID, profileID string) string {
	t.Helper()
	value := struct {
		AppID           string `json:"appId"`
		Expected        int64  `json:"expectedRevisionNumber"`
		ProfileID       string `json:"gatewayProfileRevisionId"`
		ProfileRevision int64  `json:"gatewayProfileRevisionNumber"`
	}{AppID: appID, Expected: 0, ProfileID: profileID, ProfileRevision: 1}
	return legacyJSONDigest(t, value)
}

func legacyAccessApprovalRequestDigest(t *testing.T, appID, allocationID, specDigest, actorID string) string {
	t.Helper()
	value := struct {
		AppID        string             `json:"appId"`
		AllocationID string             `json:"allocationId"`
		Expected     int64              `json:"expectedRevisionNumber"`
		Approval     appaccess.Approval `json:"approval"`
	}{AppID: appID, AllocationID: allocationID, Expected: 0,
		Approval: appaccess.Approval{Action: appaccess.ActionEnableAppAccess,
			SpecDigest: specDigest, ActorID: actorID}}
	return legacyJSONDigest(t, value)
}

func legacyDisableApprovalRequestDigest(t *testing.T, owner appaccess.AllocationOwner,
	specDigest, actorID string,
) string {
	t.Helper()
	value := struct {
		Expected int64                     `json:"expectedRevisionNumber"`
		Owner    appaccess.AllocationOwner `json:"owner"`
		Approval appaccess.Approval        `json:"approval"`
	}{Expected: 1, Owner: owner, Approval: appaccess.Approval{
		Action: appaccess.ActionDisableAppAccess, SpecDigest: specDigest, ActorID: actorID,
	}}
	return legacyJSONDigest(t, value)
}

func legacyJSONDigest(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func legacyGrantRecoveryHead(grant appaccess.AppAccessGrantClaim, disable appaccess.AppAccessDisableClaim,
	head int,
) generatedingress.GatewayV2LANRecoveryHead {
	if head == 0 {
		return generatedingress.GatewayV2LANRecoveryHead{Kind: controller.RecoveryLANGrant,
			OperationID: grant.AttemptID, AppID: grant.Spec.AppID, Head: 0, Count: 2}
	}
	return generatedingress.GatewayV2LANRecoveryHead{Kind: controller.RecoveryLANDisable,
		OperationID: disable.OperationID, AppID: disable.Spec.AppID, Head: 1, Count: 2}
}

func assertLegacyRecoveryPin(t *testing.T, gate gatewayStartup, kind, operationID, appID string,
	head, count int,
) {
	t.Helper()
	if !gate.recoveryBatch || gate.recoveryKind != kind || gate.recoveryID != operationID ||
		gate.recoveryAppID != appID || gate.recoveryBatchHead != head || gate.recoveryBatchCount != count {
		t.Fatalf("recovery pin=%+v", gate)
	}
}

func assertLegacyRecoveryHTTP(t *testing.T, db *sql.DB, session auth.Session,
	reader controller.LANRecoveryHeadService, gate gatewayStartup, expected apicontract.LANRecoveryHead,
) {
	t.Helper()
	server := (&controller.Server{
		Auth: auth.New(db), LANRecoveryHeads: reader,
		RecoveryOnly: true, RecoveryLANBatch: true,
		RecoveryBatchHead: gate.recoveryBatchHead, RecoveryBatchCount: gate.recoveryBatchCount,
		RecoveryKind: gate.recoveryKind, RecoveryOperationID: gate.recoveryID, RecoveryAppID: gate.recoveryAppID,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()

	recovery := httptest.NewRequest(http.MethodGet, "/api/v1/lan/recovery", nil)
	recovery.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
	recoveryResponse := httptest.NewRecorder()
	server.ServeHTTP(recoveryResponse, recovery)
	if recoveryResponse.Code != http.StatusOK || recoveryResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("recovery HTTP status=%d cache=%q body=%s", recoveryResponse.Code,
			recoveryResponse.Header().Get("Cache-Control"), recoveryResponse.Body.String())
	}
	var got apicontract.LANRecoveryHead
	if err := json.Unmarshal(recoveryResponse.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("recovery HTTP head=%#v want=%#v", got, expected)
	}
	var raw map[string]any
	if err := json.Unmarshal(recoveryResponse.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw["url"]; leaked || strings.Contains(recoveryResponse.Body.String(), "192.168.44.20") {
		t.Fatalf("recovery HTTP leaked LAN URL: %s", recoveryResponse.Body.String())
	}

	for _, path := range []string{
		"/api/v1/system/status",
		"/api/v1/apps/" + expected.AppID + "/lan-access",
		"/api/v1/apps/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lan-access",
	} {
		restricted := httptest.NewRequest(http.MethodGet, path, nil)
		restricted.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.Token})
		restrictedResponse := httptest.NewRecorder()
		server.ServeHTTP(restrictedResponse, restricted)
		if restrictedResponse.Code != http.StatusServiceUnavailable ||
			!strings.Contains(restrictedResponse.Body.String(), "gateway_reconciliation_required") ||
			restrictedResponse.Header().Get("Cache-Control") != "no-store" ||
			strings.Contains(restrictedResponse.Body.String(), "192.168.44.20") ||
			strings.Contains(restrictedResponse.Body.String(), ":8100") {
			t.Fatalf("restricted HTTP path=%s status=%d cache=%q body=%s", path, restrictedResponse.Code,
				restrictedResponse.Header().Get("Cache-Control"), restrictedResponse.Body.String())
		}
	}
}
