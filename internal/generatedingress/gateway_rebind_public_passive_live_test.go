package generatedingress

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// TestLiveGatewayRebindPublicPassivePredecessor proves that both public
// predecessor attestors inspect a real committed Docker gateway without
// forwarding a routed application request. The shared fixture enforces a disposable
// default local Linux Docker host and removes its exact owned resources.
func TestLiveGatewayRebindPublicPassivePredecessor(t *testing.T) {
	fixture := newLiveGatewayV2Fixture(t, liveGatewayV2FixtureSpec{
		appID:            "81818181-8181-4181-8181-818181818181",
		planID:           "82828282-8282-4282-8282-828282828282",
		operationID:      "83838383-8383-4383-8383-838383838383",
		profileRevision:  "84848484-8484-4484-8484-848484848484",
		approvedBy:       gatewayRebindTestAdministrator,
		imageTag:         "rig-generated-gateway-v2-live:public-passive-rebind",
		applicationReply: "gateway-v2-public-passive-rebind",
		countRequests:    true,
	})
	db, repository := fixture.db, fixture.repository
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
		VALUES(?,'rebind-live-admin','hash','administrator',datetime('now'),datetime('now'))`,
		gatewayRebindTestAdministrator); err != nil {
		t.Fatal("seed live predecessor administrator")
	}
	profileSpec := appaccess.GatewayProfileSpec{
		SelectedIPv4: fixture.request.Profile.SelectedIPv4,
		InterfaceID:  fixture.request.Profile.InterfaceID,
		PortStart:    fixture.port,
		PortEnd:      fixture.port,
	}
	profile, _, err := repository.ConfigureGatewayProfile(fixture.ctx, appaccess.ConfigureGatewayInput{
		OperationID: uuid.NewString(), Spec: profileSpec,
		Approval: appaccess.Approval{
			Action: appaccess.ActionConfigureGateway, SpecDigest: fixture.request.Profile.SpecDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatalf("seed exact live predecessor profile: %v", err)
	}
	if profile.SpecDigest != fixture.request.Profile.SpecDigest {
		t.Fatalf("live predecessor profile digest mismatch: got=%q want=%q",
			profile.SpecDigest, fixture.request.Profile.SpecDigest)
	}
	// ConfigureGatewayProfile owns the revision ID; bind the live upgrade to
	// that persisted ID before Docker or protected v2 state is created.
	fixture.request.Profile.RevisionID = profile.ID
	upgradeSpec := appaccess.GatewayProfileUpgradeSpec{
		ProfileRevisionID: profile.ID, ProfileRevisionNumber: profile.RevisionNumber,
		ProfileSpecDigest: profile.SpecDigest,
	}
	upgradeDigest, err := appaccess.GatewayProfileUpgradeSpecDigest(upgradeSpec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.ApprovedActionDigest = upgradeDigest
	liveGatewayV2AssertSourceAttestation(t, fixture)
	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(fixture.ctx, appaccess.ClaimGatewayProfileUpgradeInput{
		OperationID: fixture.request.OperationID, Spec: upgradeSpec,
		Approval: appaccess.Approval{
			Action: appaccess.ActionUpgradeGateway, SpecDigest: upgradeDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatalf("seed live predecessor upgrade claim: %v", err)
	}
	result, err := fixture.ingress.UpgradeGatewayV2(fixture.ctx, fixture.request,
		liveGatewayV2Authorizer(t, fixture.request))
	if err != nil {
		liveGatewayV2LogOperationDiagnostic(t, fixture)
		failLiveIngress(t, "commit live rebind predecessor", err)
	}
	if result.Outcome != GatewayV2UpgradeCommitted {
		liveGatewayV2LogOperationDiagnostic(t, fixture)
		t.Fatalf("live rebind predecessor upgrade outcome: got=%q want=%q",
			result.Outcome, GatewayV2UpgradeCommitted)
	}
	upgradeOwner := appaccess.GatewayProfileUpgradeClaimOwner{
		OperationID: upgrade.OperationID, ProfileRevisionID: profile.ID,
		ProfileRevisionNumber: profile.RevisionNumber,
	}
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(fixture.ctx, upgradeOwner,
		appaccess.GatewayProfileUpgradePrepared, appaccess.GatewayProfileUpgradeServing); err != nil {
		t.Fatalf("advance live predecessor upgrade to serving: %v", err)
	}
	if upgrade, _, err = repository.AdvanceGatewayProfileUpgradeClaim(fixture.ctx, upgradeOwner,
		appaccess.GatewayProfileUpgradeServing, appaccess.GatewayProfileUpgradeCommitted); err != nil {
		t.Fatalf("commit live predecessor upgrade claim: %v", err)
	}
	proposal := seedLiveGatewayRebindPublicPassiveLineage(t, fixture, db, repository, profile)
	state, journal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if journal.Phase != gatewayPhaseCommitted || !gatewayV2RequestMatchesState(fixture.request, state, journal) {
		t.Fatal("live predecessor protected upgrade did not remain committed")
	}
	census, err := repository.GatewayRebindQuiescenceCensus(fixture.ctx)
	if err != nil || census.Jobs.Total != 1 || census.Jobs.Succeeded != 1 ||
		census.Deployments.Total != 1 || census.Deployments.Succeeded != 1 {
		t.Fatalf("live predecessor jobs and deployments are not terminal: census=%+v err=%v", census, err)
	}
	preclaim, err := repository.GatewayRebindPreclaimSnapshot(fixture.ctx, proposal)
	if err != nil || !gatewayRebindPreclaimMatches(preclaim, state, journal) {
		t.Fatalf("live zero-claim SQLite and protected predecessor do not match: %v", err)
	}
	baseline := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	settled := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID)
	if baseline.Routed == 0 || baseline.Routed != settled.Routed {
		t.Fatal("counted app routes did not settle after live predecessor setup")
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := fixture.ingress.InspectGatewayRebindPreclaimDockerPredecessor(
			fixture.ctx, repository, proposal); err != nil {
			t.Fatalf("public preclaim Docker attestor %d failed: %v", attempt, err)
		}
		if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
			t.Fatalf("public preclaim Docker attestor %d forwarded a routed application request", attempt)
		}
	}
	if err := fixture.ingress.InspectGatewayRebindPreparedDockerPredecessor(
		fixture.ctx, repository); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("prepared Docker attestor accepted zero-claim predecessor: %v", err)
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("zero-claim prepared attestor rejection forwarded a routed application request")
	}
	insertLiveGatewayRebindPublicPassiveClaim(t, db, preclaim)
	prepared, err := repository.GatewayRebindStartupSnapshot(fixture.ctx)
	if err != nil || len(prepared.Claims) != 1 || !gatewayRebindPredecessorMatches(prepared, state, journal) {
		t.Fatalf("inserted prepared claim does not match live predecessor: %v", err)
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("test-owned claim insertion forwarded a routed application request")
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := fixture.ingress.InspectGatewayRebindPreparedDockerPredecessor(fixture.ctx, repository); err != nil {
			t.Fatalf("public prepared Docker attestor %d failed: %v", attempt, err)
		}
		if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
			t.Fatalf("public prepared Docker attestor %d forwarded a routed application request", attempt)
		}
	}
	if err := fixture.ingress.InspectGatewayRebindPreclaimDockerPredecessor(
		fixture.ctx, repository, proposal); !IsCode(err, DiagnosticRouteUnresolved) {
		t.Fatalf("preclaim Docker attestor accepted prepared predecessor: %v", err)
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed != baseline.Routed {
		t.Fatal("prepared-claim preclaim attestor rejection forwarded a routed application request")
	}
	if !fixture.ingress.proveGatewayV2FinalRoutes(fixture.ctx, state, journal.Resources.FinalContainerID) {
		t.Fatal("serving route proof failed as the independent counter positive control")
	}
	if got := liveGatewayRebindReadRequestCount(t, fixture, fixture.candidates[0].ContainerID); got.Routed <= baseline.Routed {
		t.Fatal("serving route proof did not increase the application request counter")
	}
}

type liveGatewayRebindGrantLease struct {
	repository *appaccess.Repository
	input      appaccess.ClaimAppAccessGrantInput
	owner      appaccess.AppAccessGrantClaimOwner
	request    GatewayV2LANGrantRequest
}

func (lease liveGatewayRebindGrantLease) Revalidate(ctx context.Context, request GatewayV2LANGrantRequest) error {
	claim, _, err := lease.repository.ClaimAppAccessGrant(ctx, lease.input)
	if err != nil || request != lease.request || claim.State != appaccess.AppAccessGrantApplying ||
		claim.AttemptID != request.AttemptID || claim.RequestDigest != request.ClaimRequestDigest {
		return errors.New("live grant lease drifted before Docker application")
	}
	return nil
}

func (lease liveGatewayRebindGrantLease) Activate(ctx context.Context, request GatewayV2LANGrantRequest) error {
	if request != lease.request {
		return errors.New("live grant activation request drifted")
	}
	_, _, err := lease.repository.AdvanceAppAccessGrantClaim(ctx, lease.owner,
		appaccess.AppAccessGrantApplying, appaccess.AppAccessGrantDBActive)
	return err
}

func (liveGatewayRebindGrantLease) Release() error { return nil }

func seedLiveGatewayRebindPublicPassiveLineage(t *testing.T, fixture *liveGatewayV2Fixture,
	db *sql.DB, repository *appaccess.Repository, profile appaccess.GatewayProfileRevision,
	successorOverride ...appaccess.GatewayProfileSpec,
) appaccess.GatewayRebindPreclaimProposal {
	t.Helper()
	if len(successorOverride) > 1 {
		t.Fatal("live rebind lineage received multiple successor profiles")
	}
	ctx := fixture.ctx
	allocation, _, err := repository.ReserveAppAccess(ctx, appaccess.ReserveAppAccessInput{
		AppID: fixture.spec.appID, OperationID: uuid.NewString(),
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatalf("reserve exact live gateway port: %v", err)
	}
	if allocation.Port != fixture.port {
		t.Fatalf("live gateway allocation port mismatch: got=%d want=%d", allocation.Port, fixture.port)
	}
	accessDigest, err := appaccess.AppAccessSpecDigest(appaccess.AppAccessSpecFor(allocation))
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := repository.ApproveAppAccess(ctx, appaccess.ApproveAppAccessInput{
		AppID: fixture.spec.appID, OperationID: allocation.OwnerOperationID, AllocationID: allocation.ID,
		Approval: appaccess.Approval{
			Action: appaccess.ActionEnableAppAccess, SpecDigest: accessDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
	})
	if err != nil {
		t.Fatalf("approve live app access: %v", err)
	}
	grantInput := appaccess.ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: appaccess.AppAccessGrantSpecFor(revision, profile),
		ActorID: gatewayRebindTestAdministrator,
	}
	grant, _, err := repository.ClaimAppAccessGrant(ctx, grantInput)
	if err != nil {
		t.Fatalf("claim live app grant: %v", err)
	}
	active := validateLiveGatewayRebindPublishedRuntime(t, db, fixture)
	owner := appaccess.AppAccessGrantClaimOwnerFor(grant)
	request := GatewayV2LANGrantRequest{
		AttemptID: grant.AttemptID, ClaimRequestDigest: grant.RequestDigest,
		AppID: fixture.spec.appID, AllocationID: revision.Allocation.ID,
		OwnerOperationID: revision.OperationID, Port: revision.Allocation.Port,
		AccessRevisionID: revision.ID, AccessRevisionNumber: revision.RevisionNumber,
		AccessSpecDigest: revision.SpecDigest, ApprovedBy: grant.Spec.ApprovedBy,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
		GatewayProfileSpecDigest: profile.SpecDigest,
	}
	authorize := func(ctx context.Context, got GatewayV2LANGrantRequest) (GatewayV2LANGrantAuthorizationLease, error) {
		if !reflect.DeepEqual(got, request) {
			return nil, errors.New("live grant authorization request drifted")
		}
		if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, owner,
			appaccess.AppAccessGrantPrepared, appaccess.AppAccessGrantApplying); err != nil {
			return nil, err
		}
		return liveGatewayRebindGrantLease{
			repository: repository, input: grantInput, owner: owner, request: request,
		}, nil
	}
	granted, err := fixture.ingress.GrantGatewayV2LAN(ctx, request, authorize)
	if err != nil {
		failLiveIngress(t, "grant live predecessor LAN access", err)
	}
	grant, _, err = repository.ResolveAppAccessGrantClaim(ctx, owner,
		appaccess.AppAccessGrantDBActive, appaccess.AppAccessGrantCommitted, appaccess.AppAccessGrantProof{
			GatewayOperationID:   granted.Receipt.GatewayOperationID,
			ProtectedStateDigest: granted.Receipt.ProtectedStateDigest,
		})
	if err != nil || grant.Proof == nil {
		t.Fatalf("commit live app grant proof: %v", err)
	}
	state, journal, _ := liveGatewayV2LoadDurableOperation(t, fixture)
	if state.Apps[fixture.spec.appID].LAN == nil || journal.Phase != gatewayPhaseCommitted {
		t.Fatal("live predecessor LAN binding was not protected and committed")
	}
	rebindOperationID := uuid.NewString()
	entry := appaccess.GatewayRebindRosterEntry{
		OperationID: rebindOperationID, Ordinal: 1, AppID: fixture.spec.appID,
		AllocationID: revision.Allocation.ID, Port: revision.Allocation.Port,
		AllocationOwnerOperationID: revision.OperationID, AllocationState: appaccess.AllocationActive,
		AccessRevisionID: revision.ID, AccessRevisionNumber: revision.RevisionNumber,
		AccessSpecDigest: revision.SpecDigest, GrantAttemptID: grant.AttemptID,
		GrantStateSequence: grant.StateSequence, GrantProtectedStateDigest: grant.Proof.ProtectedStateDigest,
		ServingDeploymentID: active.DeploymentID, ServingReleaseID: active.ReleaseID,
		ServingSlot: active.Slot, RouteGeneration: active.Generation,
	}
	entry.EntryDigest, err = appaccess.GatewayRebindRosterEntryDigest(entry)
	if err != nil {
		t.Fatal(err)
	}
	rosterDigest, err := appaccess.GatewayRebindRosterDigest([]appaccess.GatewayRebindRosterEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	successor := appaccess.GatewayProfileSpec{
		SelectedIPv4: "192.168.97.8", InterfaceID: "rebind-live-successor",
		PortStart: fixture.port, PortEnd: fixture.port,
	}
	if len(successorOverride) == 1 {
		successor = successorOverride[0]
	}
	spec := appaccess.GatewayRebindSpec{
		OperationID:                        rebindOperationID,
		PredecessorProfileRevisionID:       profile.ID,
		PredecessorProfileRevisionNumber:   profile.RevisionNumber,
		PredecessorProfileSpecDigest:       profile.SpecDigest,
		PredecessorUpgradeOperationID:      fixture.request.OperationID,
		PredecessorProtectedIdentityDigest: state.Identity.Digest,
		SuccessorProfileRevisionID:         uuid.NewString(),
		SuccessorProfileRevisionNumber:     profile.RevisionNumber + 1,
		SuccessorProfileOperationID:        uuid.NewString(), SuccessorProfile: successor,
		RosterDigest: rosterDigest, RosterCount: 1,
	}
	successorDigest, err := appaccess.GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	rebindDigest, err := appaccess.GatewayRebindSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	return appaccess.GatewayRebindPreclaimProposal{
		Spec: spec, Roster: []appaccess.GatewayRebindRosterEntry{entry},
		RebindApproval: appaccess.Approval{
			Action: appaccess.ActionRebindGateway, SpecDigest: rebindDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
		ConfigureApproval: appaccess.Approval{
			Action: appaccess.ActionConfigureGateway, SpecDigest: successorDigest,
			ActorID: gatewayRebindTestAdministrator,
		},
	}
}

func insertLiveGatewayRebindPublicPassiveClaim(t *testing.T, db *sql.DB,
	preclaim appaccess.GatewayRebindPreclaimSnapshot,
) {
	t.Helper()
	claim := preclaim.ProposedClaim
	stamp := time.Now().UTC()
	claim.RebindApprovedAt = stamp
	claim.ConfigureApprovedAt = stamp
	claim.CreatedAt = stamp
	claim.UpdatedAt = stamp
	insertGatewayRebindPredecessorClaim(t, db, claim)
	for _, entry := range preclaim.Proposal.Roster {
		insertGatewayRebindPredecessorRoster(t, db, entry)
	}
	var claimCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims WHERE operation_id=?`,
		claim.Spec.OperationID).Scan(&claimCount); err != nil || claimCount != 1 {
		t.Fatalf("test-owned prepared claim insert failed: %v", err)
	}
}

func validateLiveGatewayRebindPublishedRuntime(t *testing.T, db *sql.DB,
	fixture *liveGatewayV2Fixture,
) generatedruntimestate.ActiveHead {
	t.Helper()
	if fixture == nil || fixture.authority == nil || len(fixture.candidates) != 1 {
		t.Fatal("live published runtime requires one original fixture candidate")
	}
	candidate := fixture.candidates[0]
	return fixture.authority.requirePublishedHistory(t, fixture.ctx, db, candidate)
}
