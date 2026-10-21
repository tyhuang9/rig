package liveauthority

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

func TestAuthorityConstructorDoesNotAcquireEffectsScope(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	_ = New(t, db, working)
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatalf("constructor retained effects lease: %v", err)
	}
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	if leave, err := deploymenteffects.EnterScope(ctx, working); err != nil {
		t.Fatal(err)
	} else {
		leave()
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareCandidateUsesProtectedStoresAndClearsEnvironment(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	authority := New(t, db, working)
	raw := []byte("APP_FIXTURE_SLOT_MARKER=blue\nAPP_FIXTURE_RUNTIME_SECRET=top-secret\n")
	spec := authority.PrepareCandidate(t, context.Background(), liveAuthoritySpec(raw))
	if spec.DeploymentPlanRevisionID == "" || spec.DeploymentPlanRevisionNumber != 1 || spec.ConfigurationRevisionID == "" || spec.ConfigurationRevisionNumber != 1 || spec.Environment != nil {
		t.Fatalf("candidate was not bound to protected revisions: %#v", spec)
	}
	for index, value := range raw {
		if value != 0 {
			t.Fatalf("source environment byte %d was not cleared", index)
		}
	}
	plan, err := authority.plans.GetRevision(context.Background(), spec.AppID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber)
	if err != nil || len(plan.Plan.Components) != 1 || plan.Plan.Components[0].Name != spec.ComponentName {
		t.Fatalf("read protected plan: %#v error=%v", plan, err)
	}
	exported, err := authority.configuration.ExportComponentRuntimeForExecution(context.Background(), spec.AppID, spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber, spec.ComponentName)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Clear()
	if string(exported.Environment) != "# hostd application configuration\nAPP_FIXTURE_RUNTIME_SECRET=top-secret\nAPP_FIXTURE_SLOT_MARKER=blue\n" {
		t.Fatal("protected runtime export changed fixture values")
	}
}

func TestBindNetworkCreatesOnlyGenuineBuildingHistory(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	authority := New(t, db, working)
	spec := authority.PrepareCandidate(t, context.Background(), liveAuthoritySpec(nil))
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	bound := authority.BindNetwork(t, ctx, spec)
	value, err := authority.runtime.Get(ctx, bound.AppID, bound.DeploymentID)
	if err != nil || value.Phase != generatedruntimestate.PhaseBuilding || len(value.Components) != 1 || value.Components[0].State != generatedruntimestate.ComponentPending {
		t.Fatalf("network binding fabricated candidate history: %#v error=%v", value, err)
	}
	var intents, artifacts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=?`, bound.DeploymentID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_image_artifacts WHERE release_id=?`, bound.ReleaseID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || artifacts != 0 {
		t.Fatal("network binding fabricated candidate receipt or image")
	}
	var status deployments.Status
	if err := db.QueryRow(`SELECT status FROM deployments WHERE id=?`, bound.DeploymentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != deployments.Applying {
		t.Fatalf("network binding did not cross the policy gate: status=%q", status)
	}
	execution, err := authority.repository.PinGeneratedNetwork(ctx, bound.Network)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.CheckBeforeCreate(); err != nil {
		execution.Leave()
		t.Fatalf("network binding did not authorize the create boundary: %v", err)
	}
	execution.Leave()
	authority.CancelNetwork(t, ctx, bound.DeploymentID)
	value, err = authority.runtime.Get(ctx, bound.AppID, bound.DeploymentID)
	if err != nil || value.Phase != generatedruntimestate.PhaseCancelled {
		t.Fatalf("cancel network fixture: phase=%q error=%v", value.Phase, err)
	}
}

func TestPrepareNextCandidateRecordsFixtureNodeVersion(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	authority := New(t, db, working)
	spec := liveAuthoritySpec(nil)
	spec.Technology = "nextjs"
	spec = authority.PrepareCandidate(t, context.Background(), spec)
	plan, err := authority.plans.GetRevision(context.Background(), spec.AppID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Plan.Components) != 1 || plan.Plan.Components[0].NodeVersion != "24" {
		t.Fatal("protected plan does not describe the Node 24 Next fixture")
	}
}

func TestBindCandidateReplayPreservesOriginalClaimAcrossSequentialScopes(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	authority := New(t, db, working)
	spec := authority.PrepareCandidate(t, context.Background(), liveAuthoritySpec(nil))

	firstScope, releaseFirst, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	first := authority.BindCandidate(t, deploymenteffects.WithScope(context.Background(), firstScope), spec)
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}

	secondScope, releaseSecond, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseSecond() }()
	replayed := authority.BindCandidate(t, deploymenteffects.WithScope(context.Background(), secondScope), first)
	if replayed.DeploymentID != first.DeploymentID || replayed.Network.Claim != first.Network.Claim {
		t.Fatal("sequential scope replay changed the original deployment or claim")
	}
	var jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE resource_id=? AND type='deploy'`, first.AppID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("replay created %d deployment jobs", jobs)
	}
	authority.CancelNetwork(t, deploymenteffects.WithScope(context.Background(), secondScope), first.DeploymentID)
}

func TestPrepareCandidateClearsStaleDeploymentCapabilities(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	authority := New(t, db, working)
	spec := authority.PrepareCandidate(t, context.Background(), liveAuthoritySpec(nil))
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	bound := authority.BindCandidate(t, ctx, spec)
	prepared := authority.PrepareCandidate(t, context.Background(), bound)
	if prepared.DeploymentID != "" || prepared.Reservation != nil || !reflect.DeepEqual(prepared.Network, deployments.GeneratedNetworkRequest{}) {
		t.Fatal("new preparation retained a stale deployment identity or authority handle")
	}
	authority.CancelNetwork(t, ctx, bound.DeploymentID)
}

func TestBindCandidateAllowsSelectedConfigurationAfterReleaseOriginal(t *testing.T) {
	db, working := liveAuthorityDatabase(t)
	authority := New(t, db, working)
	first := authority.PrepareCandidate(t, context.Background(), liveAuthoritySpec([]byte("VALUE=original\nREMOVED=stale\n")))
	repeated := first
	repeated.Environment = []byte("REMOVED=stale\nVALUE=original\n")
	repeated = authority.PrepareCandidate(t, context.Background(), repeated)
	if repeated.ConfigurationRevisionID != first.ConfigurationRevisionID || repeated.ConfigurationRevisionNumber != first.ConfigurationRevisionNumber {
		t.Fatal("equivalent reordered fixture values created another configuration revision")
	}

	firstScope, releaseFirst, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	first = authority.BindCandidate(t, deploymenteffects.WithScope(context.Background(), firstScope), first)
	authority.CancelNetwork(t, deploymenteffects.WithScope(context.Background(), firstScope), first.DeploymentID)
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}

	selected := first
	selected.DeploymentID = ""
	selected.Network = deployments.GeneratedNetworkRequest{}
	selected.ConfigurationRevisionID, selected.ConfigurationRevisionNumber = "", 0
	selected.Environment = []byte("VALUE=selected\n")
	selected = authority.PrepareCandidate(t, context.Background(), selected)
	if selected.ConfigurationRevisionID == first.ConfigurationRevisionID {
		t.Fatal("test setup did not create a new selected configuration")
	}
	exported, err := authority.configuration.ExportComponentRuntimeForExecution(context.Background(), selected.AppID, selected.ConfigurationRevisionID, selected.ConfigurationRevisionNumber, selected.DeploymentPlanRevisionID, selected.DeploymentPlanRevisionNumber, selected.ComponentName)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Clear()
	if string(exported.Environment) != "# hostd application configuration\nVALUE=selected\n" {
		t.Fatal("fixture update did not replace the selected value and remove the omitted key")
	}

	secondScope, releaseSecond, err := deploymenteffects.AcquireScoped(context.Background(), working)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseSecond() }()
	selected = authority.BindCandidate(t, deploymenteffects.WithScope(context.Background(), secondScope), selected)
	var releaseConfiguration, deploymentConfiguration string
	if err := db.QueryRow(`SELECT r.configuration_revision_id,d.actual_configuration_revision_id
		FROM releases r JOIN deployments d ON d.release_id=r.id
		WHERE r.id=? AND d.id=?`, selected.ReleaseID, selected.DeploymentID).Scan(&releaseConfiguration, &deploymentConfiguration); err != nil {
		t.Fatal(err)
	}
	if releaseConfiguration != first.ConfigurationRevisionID || deploymentConfiguration != selected.ConfigurationRevisionID || releaseConfiguration == deploymentConfiguration {
		t.Fatalf("release-original and selected configuration were not preserved: release=%q deployment=%q", releaseConfiguration, deploymentConfiguration)
	}
	authority.CancelNetwork(t, deploymenteffects.WithScope(context.Background(), secondScope), selected.DeploymentID)
}

func liveAuthorityDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, directories.WorkingDirectory
}

func liveAuthoritySpec(environment []byte) generatedruntime.CandidateSpec {
	return generatedruntime.CandidateSpec{
		AppID: uuid.NewString(), ReleaseID: uuid.NewString(), ArtifactID: uuid.NewString(),
		ComponentName: "api", Role: generatedruntime.RoleServer, Technology: "node",
		RootDirectory: ".", RunCommand: "node server.js", InternalPort: 3000, HealthProbe: "/health",
		ImageContentID: "sha256:" + strings.Repeat("6", 64), BuildDefinitionDigest: strings.Repeat("7", 64),
		Environment: environment,
	}
}
