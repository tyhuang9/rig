package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

func TestAuthorizeGatewayProfileUpgradeExactReplayAndCurrentHead(t *testing.T) {
	ctx := context.Background()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.61.8", InterfaceID: "authorization-adapter", PortStart: 8102, PortEnd: 8112}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claimInput := approvedGatewayUpgradeClaimInput(t, profile)
	claim, _, err := repository.ClaimGatewayProfileUpgrade(ctx, claimInput)
	if err != nil {
		t.Fatal(err)
	}

	authorizationInput := gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)
	authorization, err := repository.AuthorizeGatewayProfileUpgrade(ctx, authorizationInput)
	if err != nil || authorization.Claim != claim || authorization.Profile != profile {
		t.Fatalf("prepared authorization = %#v error=%v", authorization, err)
	}

	rolledBack, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeRolledBack)
	if err != nil || !changed {
		t.Fatalf("rollback = %#v changed=%t error=%v", rolledBack, changed, err)
	}
	authorizationInput.PermittedStates = []GatewayProfileUpgradeState{GatewayProfileUpgradeRolledBack}
	authorization, err = repository.AuthorizeGatewayProfileUpgrade(ctx, authorizationInput)
	if err != nil || authorization.Claim != rolledBack || authorization.Profile != profile {
		t.Fatalf("historical rollback authorization = %#v error=%v", authorization, err)
	}
	authorizationInput.PermittedStates = []GatewayProfileUpgradeState{GatewayProfileUpgradePrepared}
	if _, err := repository.AuthorizeGatewayProfileUpgrade(ctx, authorizationInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("disallowed rollback state error = %v", err)
	}

	replacement, created, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "10.61.0.8", InterfaceID: "replacement-authorization-adapter", PortStart: 8100, PortEnd: 8119}, 1))
	if err != nil || !created || replacement.RevisionNumber != 2 {
		t.Fatalf("replacement profile = %#v created=%t error=%v", replacement, created, err)
	}
	authorizationInput.PermittedStates = []GatewayProfileUpgradeState{GatewayProfileUpgradeRolledBack}
	if _, err := repository.AuthorizeGatewayProfileUpgrade(ctx, authorizationInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale historical profile head error = %v", err)
	}
	if _, err := repository.GatewayProfileUpgradeClaim(ctx, claim.OperationID); err != nil {
		t.Fatalf("historical claim was not retained: %v", err)
	}
}

func TestAuthorizeGatewayProfileUpgradeRejectsMismatchesAndCorruption(t *testing.T) {
	t.Run("input boundaries", func(t *testing.T) {
		db, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		_ = db
		valid := gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)
		for _, test := range []struct {
			name string
			edit func(*GatewayProfileUpgradeAuthorizationInput)
			want error
		}{
			{name: "missing permitted state", edit: func(value *GatewayProfileUpgradeAuthorizationInput) { value.PermittedStates = nil }, want: ErrInvalidInput},
			{name: "duplicate permitted state", edit: func(value *GatewayProfileUpgradeAuthorizationInput) {
				value.PermittedStates = append(value.PermittedStates, GatewayProfileUpgradePrepared)
			}, want: ErrInvalidInput},
			{name: "invalid permitted state", edit: func(value *GatewayProfileUpgradeAuthorizationInput) {
				value.PermittedStates = []GatewayProfileUpgradeState{"future"}
			}, want: ErrInvalidInput},
			{name: "wrong request digest", edit: func(value *GatewayProfileUpgradeAuthorizationInput) {
				value.RequestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}, want: ErrIdempotencyMismatch},
			{name: "wrong action digest", edit: func(value *GatewayProfileUpgradeAuthorizationInput) {
				value.ActionDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			}, want: ErrApprovalRequired},
		} {
			t.Run(test.name, func(t *testing.T) {
				input := valid
				input.PermittedStates = append([]GatewayProfileUpgradeState(nil), valid.PermittedStates...)
				test.edit(&input)
				if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), input); !errors.Is(err, test.want) {
					t.Fatalf("error = %v, want %v", err, test.want)
				}
			})
		}
		if _, err := (*Repository)(nil).AuthorizeGatewayProfileUpgrade(context.Background(), valid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("nil repository error = %v", err)
		}
	})

	t.Run("claim binding mismatch", func(t *testing.T) {
		_, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		input := gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)
		input.ActorID = testViewer
		recomputeGatewayUpgradeAuthorizationDigests(t, &input)
		if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), input); !errors.Is(err, ErrIdempotencyMismatch) {
			t.Fatalf("actor binding mismatch error = %v", err)
		}
	})

	t.Run("actor no longer administrator", func(t *testing.T) {
		db, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, testAdministrator); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)); !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("demoted actor error = %v", err)
		}
	})

	t.Run("profile binding mismatch", func(t *testing.T) {
		_, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		input := gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)
		input.ProfileRevisionID = uuid.NewString()
		recomputeGatewayUpgradeAuthorizationDigests(t, &input)
		if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), input); !errors.Is(err, ErrIdempotencyMismatch) {
			t.Fatalf("profile binding mismatch error = %v", err)
		}
	})

	t.Run("corrupt claim request digest", func(t *testing.T) {
		db, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_upgrade_claim_identity_immutable`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_gateway_upgrade_claims SET request_digest=? WHERE operation_id=?`,
			"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", claim.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("corrupt claim error = %v", err)
		}
	})

	t.Run("corrupt profile values", func(t *testing.T) {
		db, repository, profile, claim := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_profile_revision_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE lan_gateway_profile_revisions SET selected_ipv4='192.168.62.9' WHERE id=?`, profile.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("corrupt profile error = %v", err)
		}
	})

	t.Run("corrupt event history", func(t *testing.T) {
		db, repository, _, claim := gatewayUpgradeAuthorizationFixture(t)
		if _, err := db.Exec(`DROP TRIGGER lan_gateway_upgrade_claim_event_matches_head`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO lan_gateway_upgrade_claim_events(operation_id,sequence,state,created_at) VALUES(?,2,'committed',?)`,
			claim.OperationID, formatTime(testNow.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AuthorizeGatewayProfileUpgrade(context.Background(), gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("corrupt history error = %v", err)
		}
	})
}

func TestAuthorizeGatewayProfileUpgradeUsesOneReadSnapshotAcrossHandles(t *testing.T) {
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
		GatewayProfileSpec{SelectedIPv4: "172.20.63.8", InterfaceID: "authorization-snapshot-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := reader.ClaimGatewayProfileUpgrade(ctx, approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}

	claimRead := make(chan struct{})
	resume := make(chan struct{})
	reader.afterUpgradeAuthClaimRead = func() {
		close(claimRead)
		<-resume
	}
	type result struct {
		authorization GatewayProfileUpgradeAuthorization
		err           error
	}
	resultChannel := make(chan result, 1)
	preparedInput := gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradePrepared)
	go func() {
		authorization, err := reader.AuthorizeGatewayProfileUpgrade(ctx, preparedInput)
		resultChannel <- result{authorization: authorization, err: err}
	}()
	waitForSignal(t, claimRead, "upgrade authorization claim read")
	serving, changed, err := writer.AdvanceGatewayProfileUpgradeClaim(ctx, gatewayUpgradeClaimOwner(claim),
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing)
	close(resume)
	if err != nil || !changed {
		t.Fatalf("concurrent serving transition = %#v changed=%t error=%v", serving, changed, err)
	}
	got := waitForResult(t, resultChannel, "upgrade authorization snapshot")
	if got.err != nil || got.authorization.Claim.State != GatewayProfileUpgradePrepared || got.authorization.Profile != profile {
		t.Fatalf("snapshot authorization = %#v error=%v", got.authorization, got.err)
	}

	reader.afterUpgradeAuthClaimRead = nil
	if _, err := reader.AuthorizeGatewayProfileUpgrade(ctx, preparedInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("fresh prepared authorization error = %v", err)
	}
	servingInput := gatewayUpgradeAuthorizationInput(t, claim, GatewayProfileUpgradeServing)
	fresh, err := reader.AuthorizeGatewayProfileUpgrade(ctx, servingInput)
	if err != nil || fresh.Claim != serving {
		t.Fatalf("fresh serving authorization = %#v error=%v", fresh, err)
	}
}

func gatewayUpgradeAuthorizationFixture(t *testing.T) (*sql.DB, *Repository, GatewayProfileRevision, GatewayProfileUpgradeClaim) {
	t.Helper()
	db := appAccessDB(t)
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(context.Background(), approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.62.8", InterfaceID: "authorization-fixture-adapter", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := repository.ClaimGatewayProfileUpgrade(context.Background(), approvedGatewayUpgradeClaimInput(t, profile))
	if err != nil {
		t.Fatal(err)
	}
	return db, repository, profile, claim
}

func gatewayUpgradeAuthorizationInput(t *testing.T, claim GatewayProfileUpgradeClaim, states ...GatewayProfileUpgradeState) GatewayProfileUpgradeAuthorizationInput {
	t.Helper()
	spec := GatewayProfileUpgradeSpec{
		ProfileRevisionID: claim.ProfileRevisionID, ProfileRevisionNumber: claim.ProfileRevisionNumber,
		ProfileSpecDigest: claim.ProfileSpecDigest,
	}
	return GatewayProfileUpgradeAuthorizationInput{
		OperationID: claim.OperationID, RequestDigest: claim.RequestDigest,
		ActionDigest: mustGatewayUpgradeDigest(t, spec), ActorID: claim.ApprovedBy,
		ProfileRevisionID: claim.ProfileRevisionID, ProfileRevisionNumber: claim.ProfileRevisionNumber,
		ProfileSpecDigest: claim.ProfileSpecDigest, PermittedStates: states,
	}
}

func recomputeGatewayUpgradeAuthorizationDigests(t *testing.T, input *GatewayProfileUpgradeAuthorizationInput) {
	t.Helper()
	spec := GatewayProfileUpgradeSpec{
		ProfileRevisionID: input.ProfileRevisionID, ProfileRevisionNumber: input.ProfileRevisionNumber,
		ProfileSpecDigest: input.ProfileSpecDigest,
	}
	input.ActionDigest = mustGatewayUpgradeDigest(t, spec)
	requestDigest, err := gatewayUpgradeClaimRequestDigest(ClaimGatewayProfileUpgradeInput{
		OperationID: input.OperationID,
		Spec:        spec,
		Approval: Approval{
			Action: ActionUpgradeGateway, SpecDigest: input.ActionDigest, ActorID: input.ActorID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input.RequestDigest = requestDigest
}
