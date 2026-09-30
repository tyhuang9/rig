package appaccess

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestGatewayUpgradeStartupSnapshotEmpty(t *testing.T) {
	db := appAccessDB(t)
	snapshot, err := New(db).GatewayUpgradeStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentProfile != nil || len(snapshot.Claims) != 0 || snapshot.Claims == nil {
		t.Fatalf("empty snapshot = %#v", snapshot)
	}
	if _, err := (*Repository)(nil).GatewayUpgradeStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil repository error = %v", err)
	}
}

func TestGatewayUpgradeStartupSnapshotPreparedClaim(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.70.8", InterfaceID: "startup-prepared-adapter", PortStart: 8101, PortEnd: 8118}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := repository.GatewayUpgradeStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentProfile == nil || *snapshot.CurrentProfile != profile || len(snapshot.Claims) != 1 {
		t.Fatalf("prepared snapshot = %#v", snapshot)
	}
	startupClaim := snapshot.Claims[0]
	wantActionDigest := mustGatewayUpgradeDigest(t, GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber, ProfileSpecDigest: profile.SpecDigest,
	})
	if startupClaim.Claim != claim || startupClaim.Profile != profile || startupClaim.ApprovedActionDigest != wantActionDigest {
		t.Fatalf("startup claim = %#v", startupClaim)
	}
}

func TestGatewayUpgradeStartupSnapshotRetainsRolledBackOlderProfileAndDemotedActor(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	oldProfile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.71.8", InterfaceID: "startup-old-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, oldProfile))
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeRolledBack)
	if err != nil || !changed {
		t.Fatalf("rollback = %#v changed=%t error=%v", rolledBack, changed, err)
	}

	const replacementAdministrator = "33333333-3333-4333-8333-333333333333"
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'startup-replacement-admin','hash','administrator',?,?)`, replacementAdministrator, formatTime(testNow), formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, testAdministrator); err != nil {
		t.Fatal(err)
	}
	repository.now = func() time.Time { return testNow.Add(time.Hour) }
	replacementSpec := GatewayProfileSpec{
		SelectedIPv4: "10.71.0.8", InterfaceID: "startup-replacement-adapter", PortStart: 8102, PortEnd: 8110,
	}
	replacementInput := ConfigureGatewayInput{
		OperationID: uuid.NewString(), ExpectedRevisionNumber: 1, Spec: replacementSpec,
		Approval: Approval{
			Action: ActionConfigureGateway, SpecDigest: mustGatewayDigest(t, replacementSpec), ActorID: replacementAdministrator,
		},
	}
	replacement, created, err := repository.ConfigureGatewayProfile(ctx, replacementInput)
	if err != nil || !created {
		t.Fatalf("replacement = %#v created=%t error=%v", replacement, created, err)
	}
	replacementUpgradeSpec := GatewayProfileUpgradeSpec{
		ProfileRevisionID: replacement.ID, ProfileRevisionNumber: replacement.RevisionNumber, ProfileSpecDigest: replacement.SpecDigest,
	}
	replacementClaim, created, err := repository.ClaimGatewayProfileUpgrade(ctx, ClaimGatewayProfileUpgradeInput{
		OperationID: uuid.NewString(),
		Spec:        replacementUpgradeSpec,
		Approval: Approval{
			Action: ActionUpgradeGateway, SpecDigest: mustGatewayUpgradeDigest(t, replacementUpgradeSpec), ActorID: replacementAdministrator,
		},
	})
	if err != nil || !created {
		t.Fatalf("replacement claim = %#v created=%t error=%v", replacementClaim, created, err)
	}

	snapshot, err := repository.GatewayUpgradeStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentProfile == nil || *snapshot.CurrentProfile != replacement || len(snapshot.Claims) != 2 {
		t.Fatalf("historical snapshot = %#v", snapshot)
	}
	if snapshot.Claims[0].Claim != rolledBack || snapshot.Claims[0].Profile != oldProfile {
		t.Fatalf("historical claim = %#v", snapshot.Claims[0])
	}
	if snapshot.Claims[1].Claim != replacementClaim || snapshot.Claims[1].Profile != replacement {
		t.Fatalf("replacement claim = %#v", snapshot.Claims[1])
	}
}

func TestGatewayUpgradeStartupSnapshotRejectsBrokenEventHistory(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.72.8", InterfaceID: "startup-event-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER lan_gateway_upgrade_claim_event_retain`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM lan_gateway_upgrade_claim_events WHERE operation_id=? AND sequence=1`, claim.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GatewayUpgradeStartupSnapshot(ctx); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("broken event history error = %v", err)
	}
}

func TestGatewayUpgradeStartupSnapshotRejectsCompetingActiveClaims(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.73.8", InterfaceID: "startup-competing-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX lan_gateway_upgrade_claims_blocking_singleton`); err != nil {
		t.Fatal(err)
	}
	competingInput := approvedGatewayUpgradeClaimInput(t, profile)
	requestDigest, err := gatewayUpgradeClaimRequestDigest(competingInput)
	if err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow.Add(time.Second))
	if _, err := db.Exec(`INSERT INTO lan_gateway_upgrade_claims(
		operation_id,request_digest,approval_action,profile_revision_id,profile_revision_number,
		profile_spec_digest,approved_by,approved_at,state,state_sequence,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, competingInput.OperationID, requestDigest, ActionUpgradeGateway,
		profile.ID, profile.RevisionNumber, profile.SpecDigest, competingInput.Approval.ActorID,
		stamp, GatewayProfileUpgradePrepared, 1, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GatewayUpgradeStartupSnapshot(ctx); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("competing active claims error = %v", err)
	}
}

func TestGatewayUpgradeStartupSnapshotRejectsMalformedSpecAndApprovalBindings(t *testing.T) {
	t.Run("active actor demoted", func(t *testing.T) {
		db, repository, _, _ := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, testAdministrator); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.GatewayUpgradeStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("demoted active actor error = %v", err)
		}
	})

	t.Run("profile spec", func(t *testing.T) {
		db, repository, profile, _ := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_profile_revision_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_gateway_profile_revisions SET selected_ipv4='192.168.62.9' WHERE id=?`, profile.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.GatewayUpgradeStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("malformed profile spec error = %v", err)
		}
	})

	t.Run("claim approval actor", func(t *testing.T) {
		db, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_upgrade_claim_identity_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_gateway_upgrade_claims SET approved_by=? WHERE operation_id=?`, testViewer, claim.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.GatewayUpgradeStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("malformed approval actor error = %v", err)
		}
	})

	t.Run("claim profile digest", func(t *testing.T) {
		db, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_upgrade_claim_identity_immutable`); err != nil {
			t.Fatal(err)
		}
		wrongDigest := strings.Repeat("a", 64)
		wrongSpec := GatewayProfileUpgradeSpec{
			ProfileRevisionID: claim.ProfileRevisionID, ProfileRevisionNumber: claim.ProfileRevisionNumber,
			ProfileSpecDigest: wrongDigest,
		}
		wrongActionDigest := mustGatewayUpgradeDigest(t, wrongSpec)
		wrongRequestDigest, err := gatewayUpgradeClaimRequestDigest(ClaimGatewayProfileUpgradeInput{
			OperationID: claim.OperationID,
			Spec:        wrongSpec,
			Approval: Approval{
				Action: ActionUpgradeGateway, SpecDigest: wrongActionDigest, ActorID: claim.ApprovedBy,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_gateway_upgrade_claims SET profile_spec_digest=?,request_digest=? WHERE operation_id=?`,
			wrongDigest, wrongRequestDigest, claim.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.GatewayUpgradeStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("malformed claim profile digest error = %v", err)
		}
	})
}

func TestGatewayUpgradeStartupSnapshotUsesOneReadSnapshotAcrossHandles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dataRoot := t.TempDir()
	readerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })
	writerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })
	addUsers(t, readerDB)
	reader := testRepository(readerDB)
	writer := testRepository(writerDB)
	profile, _, err := reader.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.74.8", InterfaceID: "startup-snapshot-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := reader.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}

	claimsRead := make(chan struct{})
	resume := make(chan struct{})
	reader.afterUpgradeStartupClaimsRead = func() {
		close(claimsRead)
		<-resume
	}
	type snapshotResult struct {
		snapshot GatewayUpgradeStartupSnapshot
		err      error
	}
	resultChannel := make(chan snapshotResult, 1)
	go func() {
		snapshot, err := reader.GatewayUpgradeStartupSnapshot(ctx)
		resultChannel <- snapshotResult{snapshot: snapshot, err: err}
	}()
	waitForSignal(t, claimsRead, "startup snapshot claim list")
	rolledBack, changed, rollbackErr := writer.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeRolledBack)
	replacement, created, replacementErr := writer.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "10.74.0.8", InterfaceID: "startup-snapshot-replacement", PortStart: 8100, PortEnd: 8119}, 1))
	close(resume)
	if rollbackErr != nil || !changed {
		t.Fatalf("concurrent rollback = %#v changed=%t error=%v", rolledBack, changed, rollbackErr)
	}
	if replacementErr != nil || !created {
		t.Fatalf("concurrent replacement = %#v created=%t error=%v", replacement, created, replacementErr)
	}

	got := waitForResult(t, resultChannel, "gateway startup snapshot")
	if got.err != nil || got.snapshot.CurrentProfile == nil || *got.snapshot.CurrentProfile != profile ||
		len(got.snapshot.Claims) != 1 || got.snapshot.Claims[0].Claim != claim || got.snapshot.Claims[0].Profile != profile {
		t.Fatalf("original snapshot = %#v error=%v", got.snapshot, got.err)
	}

	reader.afterUpgradeStartupClaimsRead = nil
	fresh, err := reader.GatewayUpgradeStartupSnapshot(ctx)
	if err != nil || fresh.CurrentProfile == nil || *fresh.CurrentProfile != replacement || len(fresh.Claims) != 1 ||
		fresh.Claims[0].Claim != rolledBack || fresh.Claims[0].Profile != profile {
		t.Fatalf("fresh snapshot = %#v error=%v", fresh, err)
	}
}
