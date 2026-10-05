package appaccess

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGatewayRebindStartupSnapshotAcceptsFreshDatabase(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshot, err := New(db).GatewayRebindStartupSnapshot(context.Background())
	if err != nil || snapshot.CurrentProfile != nil || len(snapshot.Claims) != 0 {
		t.Fatalf("fresh snapshot = %#v error=%v", snapshot, err)
	}
}

func TestGatewayRebindStartupSnapshotValidatesCompleteDormantLineage(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	wantBinding := GatewayRebindStartupGrantBinding{
		AppID: fixture.grant.Spec.AppID, AttemptID: fixture.grant.AttemptID,
		RequestDigest: fixture.grant.RequestDigest, ApprovedBy: fixture.grant.Spec.ApprovedBy,
		GatewayOperationID: fixture.grant.Proof.GatewayOperationID,
	}
	if err != nil || snapshot.CurrentProfile == nil || snapshot.CurrentProfile.ID != fixture.profile.ID ||
		len(snapshot.Claims) != 1 || snapshot.Claims[0].Claim.RequestDigest != fixture.claim.RequestDigest ||
		len(snapshot.Claims[0].Roster) != 1 || snapshot.Claims[0].Roster[0] != fixture.entry ||
		len(snapshot.Claims[0].GrantBindings) != len(snapshot.Claims[0].Roster) ||
		snapshot.Claims[0].GrantBindings[0] != wantBinding ||
		snapshot.Claims[0].PredecessorUpgrade.State != GatewayProfileUpgradeCommitted {
		t.Fatalf("validated snapshot = %#v error=%v", snapshot, err)
	}
	replayed, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
	if err != nil || !reflect.DeepEqual(replayed, snapshot) {
		t.Fatalf("deterministic replay = %#v error=%v", replayed, err)
	}
	if _, err := (*Repository)(nil).GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO lan_gateway_rebind_roster_entries(
		operation_id,ordinal,app_id,allocation_id,allocated_port,allocation_owner_operation_id,
		allocation_state,access_revision_id,access_revision_number,access_spec_digest,
		grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		serving_deployment_id,serving_release_id,serving_slot,route_generation,entry_digest
	) SELECT operation_id,ordinal,app_id,allocation_id,allocated_port,allocation_owner_operation_id,
		allocation_state,access_revision_id,access_revision_number,access_spec_digest,
		grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		serving_deployment_id,serving_release_id,serving_slot,route_generation,entry_digest
		FROM lan_gateway_rebind_roster_entries WHERE operation_id=?`, fixture.claim.Spec.OperationID); err == nil {
		t.Fatal("duplicate roster entry was accepted")
	}
	if _, err := fixture.db.Exec(`UPDATE lan_gateway_rebind_roster_entries SET entry_digest=entry_digest WHERE operation_id=?`, fixture.claim.Spec.OperationID); err == nil {
		t.Fatal("roster entry was mutable")
	}
	if _, err := fixture.db.Exec(`DELETE FROM lan_gateway_rebind_roster_entries WHERE operation_id=?`, fixture.claim.Spec.OperationID); err == nil {
		t.Fatal("roster entry was deletable")
	}
}

func TestGatewayRebindStartupSnapshotRejectsIncompleteStaleAndCorruptLineage(t *testing.T) {
	t.Run("missing roster remains fenced", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, false)
		if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("incomplete snapshot error = %v", err)
		}
		_, err := fixture.db.Exec(`INSERT INTO lan_port_allocations(
			id,app_id,port,owner_operation_id,reservation_digest,gateway_profile_revision_id,
			gateway_profile_revision_number,state,reserved_at
		) VALUES(?,?,?,?,?,?,?,'reserved',?)`, uuid.NewString(), fixture.entry.AppID, 8119,
			uuid.NewString(), strings.Repeat("f", 64), fixture.profile.ID,
			fixture.profile.RevisionNumber, formatTime(testNow))
		if err == nil || !strings.Contains(err.Error(), "rebind fence is active") {
			t.Fatalf("incomplete claim did not retain SQL fence: %v", err)
		}
	})

	t.Run("stale route generation", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		for _, trigger := range []string{"lan_gateway_rebind_fence_runtime_head_update", "generated_runtime_active_head_valid_update"} {
			if _, err := fixture.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.db.Exec(`UPDATE generated_runtime_active_heads SET generation=generation+1 WHERE app_id=?`, fixture.entry.AppID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("stale route generation error = %v", err)
		}
	})

	t.Run("access revision request digest", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		if _, err := fixture.db.Exec(`DROP TRIGGER lan_app_access_revision_immutable_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE lan_app_access_revisions
			SET request_digest='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
			WHERE app_id=? AND id=?`, fixture.entry.AppID, fixture.entry.AccessRevisionID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("corrupt access revision request digest error = %v", err)
		}
	})

	t.Run("grant profile spec digest does not match predecessor", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		corrupt := fixture.grant
		corrupt.Spec.GatewayProfileSpecDigest = strings.Repeat("9", 64)
		requestDigest, err := appAccessGrantRequestDigest(ClaimAppAccessGrantInput{
			AttemptID: corrupt.AttemptID, Spec: corrupt.Spec,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, trigger := range []string{
			"lan_gateway_rebind_fence_grant_update",
			"lan_app_access_grant_identity_immutable",
		} {
			if _, err := fixture.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.db.Exec(`UPDATE lan_app_access_grant_claims
			SET gateway_profile_spec_digest=?,request_digest=? WHERE attempt_id=?`,
			corrupt.Spec.GatewayProfileSpecDigest, requestDigest, corrupt.AttemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("grant profile mismatch error = %v", err)
		}
	})

	t.Run("grant proof operation does not match predecessor upgrade", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		for _, trigger := range []string{
			"lan_gateway_rebind_fence_grant_update",
			"lan_app_access_grant_state_transition",
			"lan_app_access_grant_transition_event",
			"lan_app_access_grant_event_immutable_update",
		} {
			if _, err := fixture.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
				t.Fatal(err)
			}
		}
		wrongOperationID := uuid.NewString()
		if _, err := fixture.db.Exec(`UPDATE lan_app_access_grant_claims
			SET gateway_operation_id=? WHERE attempt_id=?`, wrongOperationID, fixture.grant.AttemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.Exec(`UPDATE lan_app_access_grant_claim_events
			SET gateway_operation_id=? WHERE attempt_id=? AND sequence=?`,
			wrongOperationID, fixture.grant.AttemptID, fixture.grant.StateSequence); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("grant proof operation mismatch error = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		trigger string
		query   string
	}{
		{
			name: "claim request digest", trigger: "lan_gateway_rebind_claim_immutable_update",
			query: `UPDATE lan_gateway_rebind_claims SET request_digest='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`,
		},
		{
			name: "roster entry digest", trigger: "lan_gateway_rebind_roster_immutable_update",
			query: `UPDATE lan_gateway_rebind_roster_entries SET entry_digest='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`,
		},
		{
			name: "event timestamp", trigger: "lan_gateway_rebind_event_immutable_update",
			query: `UPDATE lan_gateway_rebind_claim_events SET created_at='2026-10-03T12:00:01.000000000Z'`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindFixture(t, true)
			if _, err := fixture.db.Exec(`DROP TRIGGER ` + test.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.Exec(test.query); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
				t.Fatalf("corrupt snapshot error = %v", err)
			}
		})
	}

	t.Run("approver demoted", func(t *testing.T) {
		fixture := newGatewayRebindFixture(t, true)
		if _, err := fixture.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, testAdministrator); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
			t.Fatalf("demoted approver error = %v", err)
		}
	})
}

func TestGatewayRebindStartupSnapshotUsesOneReadSnapshotAcrossHandles(t *testing.T) {
	dataRoot := t.TempDir()
	readerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })
	addUsers(t, readerDB)
	fixture := newGatewayRebindFixtureOnDB(t, readerDB, true)
	writerDB, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })

	claimsRead := make(chan struct{})
	resume := make(chan struct{})
	fixture.repository.afterRebindStartupClaimsRead = func() {
		close(claimsRead)
		<-resume
	}
	type result struct {
		snapshot GatewayRebindStartupSnapshot
		err      error
	}
	results := make(chan result, 1)
	go func() {
		snapshot, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background())
		results <- result{snapshot: snapshot, err: err}
	}()
	waitForSignal(t, claimsRead, "rebind claim list")
	if _, err := writerDB.Exec(`DROP TRIGGER lan_gateway_rebind_roster_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := writerDB.Exec(`UPDATE lan_gateway_rebind_roster_entries
		SET entry_digest='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`); err != nil {
		t.Fatal(err)
	}
	close(resume)
	got := waitForResult(t, results, "rebind startup snapshot")
	if got.err != nil || len(got.snapshot.Claims) != 1 || got.snapshot.Claims[0].Roster[0] != fixture.entry {
		t.Fatalf("original read snapshot = %#v error=%v", got.snapshot, got.err)
	}
	fixture.repository.afterRebindStartupClaimsRead = nil
	if _, err := fixture.repository.GatewayRebindStartupSnapshot(context.Background()); !errors.Is(err, ErrInvalidStoredState) {
		t.Fatalf("fresh corrupt snapshot error = %v", err)
	}
}

func TestGatewayRebindStartupSnapshotPropagatesCanceledContext(t *testing.T) {
	fixture := newGatewayRebindFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.repository.afterRebindStartupClaimsRead = cancel
	if _, err := fixture.repository.GatewayRebindStartupSnapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
}

type gatewayRebindFixture struct {
	db         *sql.DB
	repository *Repository
	profile    GatewayProfileRevision
	grant      AppAccessGrantClaim
	claim      GatewayRebindClaim
	entry      GatewayRebindRosterEntry
}

func newGatewayRebindFixture(t *testing.T, insertRoster bool) gatewayRebindFixture {
	t.Helper()
	db := appAccessDB(t)
	return newGatewayRebindFixtureOnDB(t, db, insertRoster)
}

func newGatewayRebindFixtureOnDB(t *testing.T, db *sql.DB, insertRoster bool) gatewayRebindFixture {
	t.Helper()
	ctx := context.Background()
	repository := testRepository(db)
	profile, _, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.96.8", InterfaceID: "rebind-predecessor", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil {
		t.Fatal(err)
	}
	appID := addApps(t, db, 1)[0]
	allocation, _, err := repository.ReserveAppAccess(ctx, ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := repository.ApproveAppAccess(ctx, approvedAccessInput(t, allocation, 0))
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := repository.ClaimAppAccessGrant(ctx, ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: testAdministrator,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantOwner := AppAccessGrantClaimOwnerFor(grant)
	if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, grantOwner, AppAccessGrantPrepared, AppAccessGrantApplying); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AdvanceAppAccessGrantClaim(ctx, grantOwner, AppAccessGrantApplying, AppAccessGrantDBActive); err != nil {
		t.Fatal(err)
	}
	upgradeInput := approvedGatewayUpgradeClaimInput(t, profile)
	grantProof := AppAccessGrantProof{
		GatewayOperationID:   upgradeInput.OperationID,
		ProtectedStateDigest: strings.Repeat("a", 64),
		ObservedAt:           testNow.Add(time.Second),
	}
	grant, _, err = repository.ResolveAppAccessGrantClaim(ctx, grantOwner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, grantProof)
	if err != nil {
		t.Fatal(err)
	}
	active := seedGatewayRebindActiveRuntime(t, db, appID)

	upgrade, _, err := repository.ClaimGatewayProfileUpgrade(ctx, upgradeInput)
	if err != nil {
		t.Fatal(err)
	}
	upgradeOwner := gatewayUpgradeClaimOwner(upgrade)
	if _, _, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, upgradeOwner,
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing); err != nil {
		t.Fatal(err)
	}
	upgrade, _, err = repository.AdvanceGatewayProfileUpgradeClaim(ctx, upgradeOwner,
		GatewayProfileUpgradeServing, GatewayProfileUpgradeCommitted)
	if err != nil {
		t.Fatal(err)
	}

	rebindOperationID := uuid.NewString()
	entry := GatewayRebindRosterEntry{
		OperationID: rebindOperationID, Ordinal: 1, AppID: appID,
		AllocationID: revision.Allocation.ID, Port: revision.Allocation.Port,
		AllocationOwnerOperationID: revision.Allocation.OwnerOperationID,
		AllocationState:            AllocationActive, AccessRevisionID: revision.ID,
		AccessRevisionNumber: revision.RevisionNumber, AccessSpecDigest: revision.SpecDigest,
		GrantAttemptID: grant.AttemptID, GrantStateSequence: grant.StateSequence,
		GrantProtectedStateDigest: grant.Proof.ProtectedStateDigest,
		ServingDeploymentID:       active.DeploymentID, ServingReleaseID: active.ReleaseID,
		ServingSlot: active.Slot, RouteGeneration: active.Generation,
	}
	entry.EntryDigest, err = GatewayRebindRosterEntryDigest(entry)
	if err != nil {
		t.Fatal(err)
	}
	rosterDigest, err := GatewayRebindRosterDigest([]GatewayRebindRosterEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	successor := GatewayProfileSpec{
		SelectedIPv4: "192.168.97.8", InterfaceID: "rebind-successor", PortStart: 8100, PortEnd: 8119,
	}
	spec := GatewayRebindSpec{
		OperationID:                  rebindOperationID,
		PredecessorProfileRevisionID: profile.ID, PredecessorProfileRevisionNumber: profile.RevisionNumber,
		PredecessorProfileSpecDigest: profile.SpecDigest, PredecessorUpgradeOperationID: upgrade.OperationID,
		PredecessorProtectedIdentityDigest: strings.Repeat("b", 64),
		SuccessorProfileRevisionID:         uuid.NewString(), SuccessorProfileRevisionNumber: profile.RevisionNumber + 1,
		SuccessorProfileOperationID: uuid.NewString(), SuccessorProfile: successor,
		RosterDigest: rosterDigest, RosterCount: 1,
	}
	successorSpecDigest, err := GatewayProfileSpecDigest(successor)
	if err != nil {
		t.Fatal(err)
	}
	configureApproval := Approval{Action: ActionConfigureGateway, SpecDigest: successorSpecDigest, ActorID: testAdministrator}
	specDigest, err := GatewayRebindSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	rebindApproval := Approval{Action: ActionRebindGateway, SpecDigest: specDigest, ActorID: testAdministrator}
	canonical, _ := canonicalGatewaySpec(successor)
	successorRequestDigest, err := gatewayRequestDigest(ConfigureGatewayInput{
		OperationID: spec.SuccessorProfileOperationID, ExpectedRevisionNumber: profile.RevisionNumber,
		Spec: successor, Approval: configureApproval,
	}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest, err := gatewayRebindClaimRequestDigest(spec, rebindApproval, configureApproval)
	if err != nil {
		t.Fatal(err)
	}
	claimTime := testNow.Add(2 * time.Second)
	claim := GatewayRebindClaim{
		Spec: spec, RequestDigest: requestDigest, RebindApproval: rebindApproval,
		RebindApprovedAt: claimTime, ConfigureApproval: configureApproval, ConfigureApprovedAt: claimTime,
		SuccessorProfileRequestDigest: successorRequestDigest, State: GatewayRebindPrepared,
		StateSequence: 1, CreatedAt: claimTime, UpdatedAt: claimTime,
	}
	insertGatewayRebindClaim(t, db, claim)
	if insertRoster {
		insertGatewayRebindRosterEntry(t, db, entry)
	}
	return gatewayRebindFixture{
		db: db, repository: repository, profile: profile, grant: grant, claim: claim, entry: entry,
	}
}

func seedGatewayRebindActiveRuntime(t *testing.T, db *sql.DB, appID string) generatedruntimestate.ActiveHead {
	t.Helper()
	planID, releaseID, deploymentID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := formatTime(testNow)
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
		planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret",
		strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64),
		testAdministrator, stamp, testAdministrator, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, appID, stamp,
		strings.Repeat("d", 64), strings.Repeat("f", 64), planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,?,?)`, jobID, appID, testAdministrator, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`, deploymentID, appID, releaseID, jobID, planID); err != nil {
		t.Fatal(err)
	}
	runtime := generatedruntimestate.New(db)
	value, _, err := runtime.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
		compiler_version,build_definition_digest,attempt_number,image_content_id,state,
		created_at,updated_at,finished_at
	) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, "api",
		strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64), stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetImageReady(context.Background(), appID, deploymentID, "api", artifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerStarting(context.Background(), appID, deploymentID, "api", "rig-rebind-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SetContainerRunning(context.Background(), appID, deploymentID, "api", strings.Repeat("3", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AdvanceComponent(context.Background(), appID, deploymentID, "api",
		generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(context.Background(), appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtime.SwitchActive(context.Background(), appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("switch active head=%#v changed=%t error=%v", head, changed, err)
	}
	return head
}

func insertGatewayRebindClaim(t *testing.T, db *sql.DB, claim GatewayRebindClaim) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO lan_gateway_rebind_claims(
		operation_id,request_digest,approval_action,spec_digest,approved_by,approved_at,
		predecessor_profile_revision_id,predecessor_profile_revision_number,
		predecessor_profile_spec_digest,predecessor_upgrade_operation_id,
		predecessor_protected_identity_digest,successor_profile_revision_id,
		successor_profile_revision_number,successor_profile_operation_id,
		successor_profile_request_digest,successor_selected_ipv4,successor_interface_id,
		successor_port_start,successor_port_end,successor_profile_spec_digest,
		configure_approval_action,configure_approved_by,configure_approved_at,
		roster_digest,roster_count,state,state_sequence,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		claim.Spec.OperationID, claim.RequestDigest, claim.RebindApproval.Action,
		claim.RebindApproval.SpecDigest, claim.RebindApproval.ActorID, formatTime(claim.RebindApprovedAt),
		claim.Spec.PredecessorProfileRevisionID, claim.Spec.PredecessorProfileRevisionNumber,
		claim.Spec.PredecessorProfileSpecDigest, claim.Spec.PredecessorUpgradeOperationID,
		claim.Spec.PredecessorProtectedIdentityDigest, claim.Spec.SuccessorProfileRevisionID,
		claim.Spec.SuccessorProfileRevisionNumber, claim.Spec.SuccessorProfileOperationID,
		claim.SuccessorProfileRequestDigest, claim.Spec.SuccessorProfile.SelectedIPv4,
		claim.Spec.SuccessorProfile.InterfaceID, claim.Spec.SuccessorProfile.PortStart,
		claim.Spec.SuccessorProfile.PortEnd, claim.ConfigureApproval.SpecDigest,
		claim.ConfigureApproval.Action, claim.ConfigureApproval.ActorID, formatTime(claim.ConfigureApprovedAt),
		claim.Spec.RosterDigest, claim.Spec.RosterCount, claim.State, claim.StateSequence,
		formatTime(claim.CreatedAt), formatTime(claim.UpdatedAt))
	if err != nil {
		t.Fatal(err)
	}
}

func insertGatewayRebindRosterEntry(t *testing.T, db *sql.DB, entry GatewayRebindRosterEntry) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO lan_gateway_rebind_roster_entries(
		operation_id,ordinal,app_id,allocation_id,allocated_port,allocation_owner_operation_id,
		allocation_state,access_revision_id,access_revision_number,access_spec_digest,
		grant_attempt_id,grant_state_sequence,grant_protected_state_digest,
		serving_deployment_id,serving_release_id,serving_slot,route_generation,entry_digest
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, entry.OperationID, entry.Ordinal, entry.AppID,
		entry.AllocationID, entry.Port, entry.AllocationOwnerOperationID, entry.AllocationState,
		entry.AccessRevisionID, entry.AccessRevisionNumber, entry.AccessSpecDigest,
		entry.GrantAttemptID, entry.GrantStateSequence, entry.GrantProtectedStateDigest,
		entry.ServingDeploymentID, entry.ServingReleaseID, entry.ServingSlot,
		entry.RouteGeneration, entry.EntryDigest)
	if err != nil {
		t.Fatal(err)
	}
}
