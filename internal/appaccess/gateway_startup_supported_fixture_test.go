package appaccess

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

// This is the SQL contract of the supported live fixture, not a replacement
// for its controller and Docker coverage. Canonical application, plan, release
// and deployment prerequisites follow the established appaccess test fixture;
// all gateway, grant and generated-runtime transitions use their repositories.
func TestHostingGatewayStartupSnapshotAcceptsSupportedFixtureCombinedStateAfterReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	setupDB, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	setupOpen := true
	t.Cleanup(func() {
		if setupOpen {
			_ = setupDB.Close()
		}
	})
	addUsers(t, setupDB)
	apps := addApps(t, setupDB, 2)
	sort.Strings(apps)

	wantHeads := make([]GatewayRebindRuntimeHead, 0, len(apps))
	wantComponents := make([]GatewayStartupRuntimeComponent, 0, len(apps)*2)
	for _, appID := range apps {
		head, components := seedSupportedFixtureRuntime(t, setupDB, appID)
		wantHeads = append(wantHeads, GatewayRebindRuntimeHead{
			AppID: head.AppID, DeploymentID: head.DeploymentID, ReleaseID: head.ReleaseID,
			Slot: head.Slot, Generation: head.Generation, UpdatedAt: head.UpdatedAt,
		})
		wantComponents = append(wantComponents, components...)
	}
	sort.Slice(wantHeads, func(i, j int) bool { return wantHeads[i].AppID < wantHeads[j].AppID })
	sort.Slice(wantComponents, func(i, j int) bool {
		if wantComponents[i].AppID == wantComponents[j].AppID {
			return wantComponents[i].Name < wantComponents[j].Name
		}
		return wantComponents[i].AppID < wantComponents[j].AppID
	})

	repository := testRepository(setupDB)
	profile, created, err := repository.ConfigureGatewayProfile(ctx, approvedGatewayInput(t,
		GatewayProfileSpec{SelectedIPv4: "192.168.96.8", InterfaceID: "supported-fixture-predecessor", PortStart: 8100, PortEnd: 8119}, 0))
	if err != nil || !created {
		t.Fatalf("configure supported predecessor: created=%t error=%v", created, err)
	}
	upgradeInput := approvedGatewayUpgradeClaimInput(t, profile)
	upgrade, created, err := repository.ClaimGatewayProfileUpgrade(ctx, upgradeInput)
	if err != nil || !created {
		t.Fatalf("claim supported native upgrade: created=%t error=%v", created, err)
	}
	upgradeOwner := gatewayUpgradeClaimOwner(upgrade)
	upgrade, changed, err := repository.AdvanceGatewayProfileUpgradeClaim(ctx, upgradeOwner,
		GatewayProfileUpgradePrepared, GatewayProfileUpgradeServing)
	if err != nil || !changed {
		t.Fatalf("advance supported native upgrade to serving: changed=%t error=%v", changed, err)
	}
	upgrade, changed, err = repository.AdvanceGatewayProfileUpgradeClaim(ctx, upgradeOwner,
		GatewayProfileUpgradeServing, GatewayProfileUpgradeCommitted)
	if err != nil || !changed {
		t.Fatalf("commit supported native upgrade: changed=%t error=%v", changed, err)
	}

	grants := make([]supportedFixtureGrant, 0, len(apps))
	for index, appID := range apps {
		grants = append(grants, commitSupportedFixtureGrant(t, repository, profile, upgrade, appID, index))
	}
	beforeSnapshot, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		stage, cause := HostingGatewayStartupDiagnostic(err)
		t.Fatalf("supported fixture combined startup snapshot diagnostic=(%q,%q)", stage, cause)
	}
	before := readSupportedFixtureAuthority(t, repository, grants)
	if !reflect.DeepEqual(before.profile, profile) || !reflect.DeepEqual(before.upgrade, upgrade) ||
		!reflect.DeepEqual(before.heads, wantHeads) || before.rebind.Active != nil ||
		len(before.rebind.History) != 0 || before.rebind.CurrentProfile == nil ||
		before.rebind.CurrentSource == nil || before.rebind.CurrentSource.Kind != GatewayRebindSourceGatewayUpgrade ||
		before.rebind.CurrentSource.OperationID != upgrade.OperationID {
		t.Fatal("supported fixture authority was incomplete before reopen")
	}
	assertSupportedFixtureHasNoRebindClaims(t, setupDB)
	historyBefore := readSupportedFixtureHistory(t, setupDB)
	assertSupportedFixtureHistoryShape(t, historyBefore, len(grants))

	if err := setupDB.Close(); err != nil {
		t.Fatal("close supported fixture database:", err)
	}
	setupOpen = false
	reopenedDB, err := database.Open(root)
	if err != nil {
		t.Fatal("reopen supported fixture database:", err)
	}
	t.Cleanup(func() { _ = reopenedDB.Close() })
	reopened := testRepository(reopenedDB)

	changesBefore := readSupportedFixtureTotalChanges(t, reopenedDB)
	snapshot, err := reopened.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		stage, cause := HostingGatewayStartupDiagnostic(err)
		t.Fatalf("reopened supported fixture combined startup snapshot diagnostic=(%q,%q)", stage, cause)
	}
	changesAfter := readSupportedFixtureTotalChanges(t, reopenedDB)
	if changesAfter != changesBefore {
		t.Fatal("combined startup snapshot wrote to the reopened database")
	}
	if !reflect.DeepEqual(snapshot, beforeSnapshot) {
		t.Fatal("combined startup snapshot changed across database reopen")
	}
	if snapshot.Upgrades.CurrentProfile == nil || !reflect.DeepEqual(*snapshot.Upgrades.CurrentProfile, profile) {
		t.Fatal("combined snapshot changed the current gateway profile")
	}
	if len(snapshot.Upgrades.Claims) != 1 {
		t.Fatalf("combined snapshot upgrade claims=%d want=1", len(snapshot.Upgrades.Claims))
	}
	if !reflect.DeepEqual(snapshot.Upgrades.Claims[0], GatewayUpgradeStartupClaim{
		Claim: upgrade, Profile: profile, ApprovedActionDigest: upgradeInput.Approval.SpecDigest,
	}) {
		t.Fatal("combined snapshot changed the committed native upgrade identity")
	}
	if len(snapshot.Grants.Claims) != len(grants) {
		t.Fatalf("combined snapshot grants=%d want=%d", len(snapshot.Grants.Claims), len(grants))
	}
	grantsByAttempt := make(map[string]supportedFixtureGrant, len(grants))
	for _, grant := range grants {
		grantsByAttempt[grant.claim.AttemptID] = grant
	}
	if snapshot.Rebind.CurrentSource == nil {
		t.Fatal("combined snapshot omitted the current gateway authority")
	}
	for _, claim := range snapshot.Grants.Claims {
		want, ok := grantsByAttempt[claim.Claim.AttemptID]
		if !ok {
			t.Fatal("combined snapshot contained an unknown or duplicate grant attempt")
		}
		assertSupportedFixtureGrantSnapshot(t, claim, want, profile, *snapshot.Rebind.CurrentSource)
		delete(grantsByAttempt, claim.Claim.AttemptID)
	}
	if len(grantsByAttempt) != 0 {
		t.Fatal("combined snapshot omitted a committed grant")
	}
	if len(snapshot.Disables.Claims) != 0 {
		t.Fatal("combined snapshot invented a disable claim")
	}
	if !reflect.DeepEqual(snapshot.Rebind, before.rebind) {
		t.Fatal("combined snapshot changed no-rebind current authority")
	}
	if snapshot.ActiveRebindApprovalAuthority != nil {
		t.Fatal("combined snapshot invented active rebind approval authority")
	}
	if !reflect.DeepEqual(snapshot.RuntimeHeads, wantHeads) ||
		!reflect.DeepEqual(snapshot.RuntimeComponents, wantComponents) {
		t.Fatalf("combined runtime census heads=%d components=%d want=(%d,%d)",
			len(snapshot.RuntimeHeads), len(snapshot.RuntimeComponents), len(wantHeads), len(wantComponents))
	}
	assertSupportedFixtureHasNoRebindClaims(t, reopenedDB)
	historyAfter := readSupportedFixtureHistory(t, reopenedDB)
	if historyAfter != historyBefore {
		t.Fatal("combined startup snapshot changed persisted claim or audit history")
	}
	after := readSupportedFixtureAuthority(t, reopened, grants)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("combined startup snapshot changed persisted supported fixture authority")
	}
}

type supportedFixtureGrant struct {
	claim    AppAccessGrantClaim
	revision AppAccessRevision
}

func commitSupportedFixtureGrant(t *testing.T, repository *Repository, profile GatewayProfileRevision,
	upgrade GatewayProfileUpgradeClaim, appID string, index int,
) supportedFixtureGrant {
	t.Helper()
	ctx := context.Background()
	allocation, created, err := repository.ReserveAppAccess(ctx, ReserveAppAccessInput{
		AppID: appID, OperationID: uuid.NewString(), ExpectedRevisionNumber: 0,
		GatewayProfileRevisionID: profile.ID, GatewayProfileRevisionNumber: profile.RevisionNumber,
	})
	if err != nil || !created {
		t.Fatalf("reserve supported grant %d: created=%t error=%v", index, created, err)
	}
	revision, created, err := repository.ApproveAppAccess(ctx, approvedAccessInput(t, allocation, 0))
	if err != nil || !created {
		t.Fatalf("approve supported grant %d: created=%t error=%v", index, created, err)
	}
	claim, created, err := repository.ClaimAppAccessGrant(ctx, ClaimAppAccessGrantInput{
		AttemptID: uuid.NewString(), Spec: AppAccessGrantSpecFor(revision, profile), ActorID: testAdministrator,
	})
	if err != nil || !created {
		t.Fatalf("claim supported grant %d: created=%t error=%v", index, created, err)
	}
	owner := AppAccessGrantClaimOwnerFor(claim)
	claim, changed, err := repository.AdvanceAppAccessGrantClaim(ctx, owner,
		AppAccessGrantPrepared, AppAccessGrantApplying)
	if err != nil || !changed {
		t.Fatalf("apply supported grant %d: changed=%t error=%v", index, changed, err)
	}
	claim, changed, err = repository.AdvanceAppAccessGrantClaim(ctx, owner,
		AppAccessGrantApplying, AppAccessGrantDBActive)
	if err != nil || !changed {
		t.Fatalf("activate supported grant database state %d: changed=%t error=%v", index, changed, err)
	}
	claim, changed, err = repository.ResolveAppAccessGrantClaim(ctx, owner,
		AppAccessGrantDBActive, AppAccessGrantCommitted, AppAccessGrantProof{
			GatewayOperationID:   upgrade.OperationID,
			ProtectedStateDigest: supportedFixtureDigest(fmt.Sprintf("grant-%d-%s", index, appID)),
		})
	if err != nil || !changed {
		t.Fatalf("commit supported grant %d: changed=%t error=%v", index, changed, err)
	}
	approvedRevision := revision
	revision, err = repository.CurrentAppAccess(ctx, appID)
	if err != nil {
		t.Fatalf("read committed supported grant revision %d: %v", index, err)
	}
	if revision.ID != approvedRevision.ID || revision.RevisionNumber != approvedRevision.RevisionNumber ||
		revision.Allocation.State != AllocationActive {
		t.Fatalf("committed supported grant revision %d did not retain its identity and activate", index)
	}
	return supportedFixtureGrant{claim: claim, revision: revision}
}

func assertSupportedFixtureGrantSnapshot(t *testing.T, claim AppAccessGrantStartupClaim,
	want supportedFixtureGrant, profile GatewayProfileRevision, currentSource GatewayCurrentAuthorityRef,
) {
	t.Helper()
	if !reflect.DeepEqual(claim.Claim, want.claim) {
		t.Fatal("combined snapshot changed a committed grant claim")
	}
	if !reflect.DeepEqual(claim.Revision, want.revision) {
		t.Fatal("combined snapshot changed a committed grant's current access revision")
	}
	if claim.Allocation != want.revision.Allocation {
		t.Fatal("combined snapshot changed a committed grant's active allocation")
	}
	if !reflect.DeepEqual(claim.Profile, profile) {
		t.Fatal("combined snapshot changed a committed grant's raw profile")
	}
	if !reflect.DeepEqual(claim.EffectiveProfile, profile) {
		t.Fatal("combined snapshot changed a committed native grant's effective profile")
	}
	if claim.CurrentGatewaySource != currentSource {
		t.Fatal("combined snapshot changed a committed native grant's current authority")
	}
	if len(claim.TransferChain) != 0 || claim.TransferChainTipDigest != "" ||
		claim.TerminalReceiptDigest != "" {
		t.Fatal("combined snapshot invented rebind proof for a committed native grant")
	}
	if !reflect.DeepEqual(claim.RetainedEffectiveProfile, GatewayProfileRevision{}) ||
		claim.RetainedGatewaySource != (GatewayCurrentAuthorityRef{}) ||
		len(claim.RetainedTransferChain) != 0 || claim.RetainedTransferChainTipDigest != "" ||
		claim.RetainedTerminalReceiptDigest != "" {
		t.Fatal("combined snapshot projected current native authority as retained authority")
	}
	if claim.DisableIntent != nil {
		t.Fatal("combined snapshot invented a disable intent for a committed grant")
	}
	if !claim.AccessHeadCurrent {
		t.Fatal("combined snapshot did not mark a committed grant access head current")
	}
	if !claim.ProfileHeadCurrent {
		t.Fatal("combined snapshot did not mark a committed grant profile head current")
	}
	if !claim.ApproverIsAdministrator {
		t.Fatal("combined snapshot did not preserve the committed grant administrator approval")
	}
	if claim.AppArchived {
		t.Fatal("combined snapshot marked an active fixture application archived")
	}
}

func seedSupportedFixtureRuntime(t *testing.T, db *sql.DB, appID string) (
	generatedruntimestate.ActiveHead, []GatewayStartupRuntimeComponent,
) {
	t.Helper()
	ctx := context.Background()
	// releasesnapshot.MaterializeLocal and releasesnapshot.Materialize produce
	// 16 random bytes encoded as 32 lowercase hexadecimal release IDs.
	planID, releaseID := uuid.NewString(), supportedFixtureDigest(appID + "-release-id")[:32]
	deploymentID, jobID := uuid.NewString(), uuid.NewString()
	stamp := formatTime(testNow)
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,2,8,'',?,?,'accepted',?,?)`,
		planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".json",
		supportedFixtureDigest(appID+"-source"), supportedFixtureDigest(appID+"-resolved"),
		supportedFixtureDigest(appID+"-plan"), testAdministrator, stamp, testAdministrator, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, appID, stamp,
		supportedFixtureDigest(appID+"-release"), supportedFixtureDigest(appID+"-workspace"), planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at)
		VALUES(?,'deploy','application',?,'running','running',?,?,?)`,
		jobID, appID, testAdministrator, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployments(
		id,app_id,release_id,job_id,status,configuration_mode,provenance_initialized,runtime_strategy,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,?,?,'preparing','current',1,'generated_node',?,1)`,
		deploymentID, appID, releaseID, jobID, planID); err != nil {
		t.Fatal(err)
	}

	runtime := generatedruntimestate.New(db)
	value, created, err := runtime.Begin(ctx, generatedruntimestate.BeginInput{
		DeploymentID: deploymentID, AppID: appID, ReleaseID: releaseID,
		DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1,
		ComponentNames: []string{"api", "frontend"},
	})
	if err != nil || !created {
		t.Fatalf("begin supported runtime for %s: created=%t error=%v", appID, created, err)
	}
	for _, component := range []string{"api", "frontend"} {
		artifactID := uuid.NewString()
		if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
			id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,
			compiler_version,build_definition_digest,attempt_number,image_content_id,state,
			created_at,updated_at,finished_at
		) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, releaseID, planID, 1, component,
			supportedFixtureDigest(appID+"-"+component+"-definition"),
			"sha256:"+supportedFixtureDigest(appID+"-"+component+"-image"), stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		containerName := "rig-supported-" + strings.ReplaceAll(appID, "-", "")[:12] + "-" + component
		containerID := supportedFixtureDigest(appID + "-" + component + "-container")
		if _, err := runtime.SetImageReady(ctx, appID, deploymentID, component, artifactID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.SetContainerStarting(ctx, appID, deploymentID, component, containerName); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.SetContainerRunning(ctx, appID, deploymentID, component, containerID); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.AdvanceComponent(ctx, appID, deploymentID, component,
			generatedruntimestate.ComponentRunning, generatedruntimestate.ComponentHealthy); err != nil {
			t.Fatal(err)
		}
	}
	for _, next := range []generatedruntimestate.Phase{
		generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
	} {
		value, err = runtime.Advance(ctx, appID, deploymentID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	head, changed, err := runtime.SwitchActive(ctx, appID, deploymentID, 0)
	if err != nil || !changed {
		t.Fatalf("switch supported runtime head for %s: changed=%t error=%v", appID, changed, err)
	}
	components := make([]GatewayStartupRuntimeComponent, 0, 2)
	for _, name := range []string{"api", "frontend"} {
		component, err := runtime.Component(ctx, appID, deploymentID, name)
		if err != nil {
			t.Fatal(err)
		}
		components = append(components, GatewayStartupRuntimeComponent{
			AppID: appID, DeploymentID: deploymentID, ReleaseID: releaseID,
			Name: component.Name, Slot: component.Slot, ImageArtifactID: component.ImageArtifactID,
			ContainerName: component.ContainerName, ContainerID: component.ContainerID,
			State: string(component.State), DiagnosticCode: string(component.DiagnosticCode),
			CreatedAt: component.CreatedAt, UpdatedAt: component.UpdatedAt, FinishedAt: component.FinishedAt,
		})
	}
	return head, components
}

type supportedFixtureAuthority struct {
	profile  GatewayProfileRevision
	upgrade  GatewayProfileUpgradeClaim
	rebind   GatewayRebindRecoverySnapshot
	heads    []GatewayRebindRuntimeHead
	bindings []GatewayBindingResolution
}

func readSupportedFixtureAuthority(t *testing.T, repository *Repository,
	grants []supportedFixtureGrant,
) supportedFixtureAuthority {
	t.Helper()
	ctx := context.Background()
	profile, err := repository.CurrentGatewayProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, err := repository.CurrentGatewayProfileUpgradeClaim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rebind, err := repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	heads, err := repository.GatewayRebindRuntimeHeads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bindings := make([]GatewayBindingResolution, 0, len(grants))
	for _, grant := range grants {
		binding, err := repository.ResolveGatewayBinding(ctx, GatewayBindingRef{
			AppID: grant.claim.Spec.AppID, AllocationID: grant.claim.Spec.AllocationID,
			AccessRevisionID: grant.claim.Spec.AccessRevisionID, GrantAttemptID: grant.claim.AttemptID,
		})
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}
	return supportedFixtureAuthority{
		profile: profile, upgrade: upgrade, rebind: rebind, heads: heads, bindings: bindings,
	}
}

type supportedFixtureHistory struct {
	upgradeClaims int
	upgradeEvents int
	grantClaims   int
	grantEvents   int
	rebindClaims  int
	rebindEvents  int
	auditEvents   int
}

func readSupportedFixtureHistory(t *testing.T, db *sql.DB) supportedFixtureHistory {
	t.Helper()
	var history supportedFixtureHistory
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM lan_gateway_upgrade_claims),
		(SELECT COUNT(*) FROM lan_gateway_upgrade_claim_events),
		(SELECT COUNT(*) FROM lan_app_access_grant_claims),
		(SELECT COUNT(*) FROM lan_app_access_grant_claim_events),
		(SELECT COUNT(*) FROM lan_gateway_rebind_claims),
		(SELECT COUNT(*) FROM lan_gateway_rebind_claim_events),
		(SELECT COUNT(*) FROM audit_events)`).Scan(
		&history.upgradeClaims, &history.upgradeEvents,
		&history.grantClaims, &history.grantEvents,
		&history.rebindClaims, &history.rebindEvents, &history.auditEvents,
	); err != nil {
		t.Fatal("read supported fixture claim history:", err)
	}
	return history
}

func assertSupportedFixtureHistoryShape(t *testing.T, history supportedFixtureHistory, grants int) {
	t.Helper()
	if history.upgradeClaims != 1 || history.upgradeEvents != 3 {
		t.Fatal("supported fixture did not retain one three-event committed native upgrade")
	}
	if history.grantClaims != grants || history.grantEvents != grants*4 {
		t.Fatal("supported fixture did not retain four ordered events per committed grant")
	}
	if history.rebindClaims != 0 || history.rebindEvents != 0 {
		t.Fatal("supported fixture unexpectedly retained rebind history")
	}
}

func readSupportedFixtureTotalChanges(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var changes int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		t.Fatal("read supported fixture connection change count:", err)
	}
	return changes
}

func assertSupportedFixtureHasNoRebindClaims(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lan_gateway_rebind_claims`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("supported baseline rebind claims=%d want=0", count)
	}
}

func supportedFixtureDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}
