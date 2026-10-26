package deployments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

func TestGeneratedOwnerStoresStartupSnapshotRequiresExactCanonicalBundle(t *testing.T) {
	ctx := context.Background()
	fixture := newGeneratedOwnerDomainFixture(t)
	snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GeneratedDeployments == nil || snapshot.Artifacts == nil || snapshot.LifecycleOperations == nil ||
		snapshot.LifecycleBindings == nil || snapshot.UnresolvedLifecycleApps == nil {
		t.Fatal("canonical empty snapshot contains nil slices")
	}
	copyValue := *fixture.stores
	if _, err := copyValue.GeneratedDomainStartupSnapshot(ctx); err != nil {
		t.Fatalf("unchanged value copy rejected: %v", err)
	}
	foreign := newGeneratedOwnerDomainFixture(t)
	copyValue.database = foreign.stores.database
	if got, err := copyValue.GeneratedDomainStartupSnapshot(ctx); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
		!reflect.DeepEqual(got, GeneratedDomainStartupSnapshot{}) {
		t.Fatalf("retargeted bundle snapshot=%#v error=%v", got, err)
	}
	var nilStores *GeneratedOwnerStores
	if got, err := nilStores.GeneratedDomainStartupSnapshot(ctx); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) ||
		!reflect.DeepEqual(got, GeneratedDomainStartupSnapshot{}) {
		t.Fatalf("nil bundle snapshot=%#v error=%v", got, err)
	}
}

func TestGeneratedOwnerStoresStartupSnapshotDistinguishesOrdinaryHistoryAndEveryGeneratedCensus(t *testing.T) {
	t.Run("ordinary history", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.GeneratedDeployments) != 0 || len(snapshot.Artifacts) != 0 ||
			len(snapshot.Runtime.ActiveHeads) != 0 || len(snapshot.Runtime.Deployments) != 0 ||
			len(snapshot.Runtime.Components) != 0 || len(snapshot.Runtime.CandidateIntents) != 0 ||
			snapshot.LifecycleOperationCount != 0 || len(snapshot.UnresolvedLifecycleApps) != 0 {
			t.Fatalf("ordinary history reported generated effects: %#v", snapshot)
		}
	})

	t.Run("generic deployment before runtime", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
		deployment := seedStartupGenericDeployment(t, fixture, planID, releaseID)
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.GeneratedDeployments) != 1 || snapshot.GeneratedDeployments[0].ID != deployment.ID ||
			len(snapshot.Runtime.Deployments) != 0 {
			t.Fatalf("pre-runtime census=%#v", snapshot)
		}
	})

	t.Run("artifact without runtime", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
		artifact, created, err := generatedimagestate.NewRepository(fixture.stores.database).Begin(context.Background(),
			generatedimagestate.BeginArtifactInput{ReleaseID: releaseID, DeploymentPlanRevisionID: planID,
				DeploymentPlanRevisionNumber: 1, ComponentID: "api", CompilerVersion: "test",
				BuildDefinitionDigest: strings.Repeat("a", 64)})
		if err != nil || !created {
			t.Fatalf("begin artifact: created=%t error=%v", created, err)
		}
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.GeneratedDeployments) != 0 || len(snapshot.Runtime.Deployments) != 0 ||
			len(snapshot.Artifacts) != 1 || snapshot.Artifacts[0].ID != artifact.ID ||
			snapshot.Artifacts[0].State != generatedimagestate.ArtifactBuilding {
			t.Fatalf("artifact-only census=%#v", snapshot)
		}
	})

	t.Run("runtime and component", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
		deployment := seedStartupGenericDeployment(t, fixture, planID, releaseID)
		if _, created, err := generatedruntimestate.New(fixture.stores.database).Begin(context.Background(),
			generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: fixture.base.appA,
				ReleaseID: releaseID, DeploymentPlanRevisionID: planID,
				DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"}}); err != nil || !created {
			t.Fatalf("begin runtime: created=%t error=%v", created, err)
		}
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.GeneratedDeployments) != 1 || len(snapshot.Runtime.Deployments) != 1 ||
			len(snapshot.Runtime.Components) != 1 || snapshot.Runtime.Components[0].ComponentName != "api" {
			t.Fatalf("runtime census=%#v", snapshot)
		}
	})
}

func TestGeneratedOwnerStoresStartupSnapshotUsesOneRealSQLiteSnapshotAcrossReaders(t *testing.T) {
	ctx := context.Background()
	fixture := newGeneratedOwnerDomainFixture(t)
	planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
	jobID := seedStartupGeneratedDeploymentJob(t, fixture)
	deployment, _, err := fixture.stores.repository.GetOrCreateByJob(ctx, fixture.base.appA, jobID, "current")
	if err != nil {
		t.Fatal(err)
	}
	writerDB, err := database.Open(fixture.dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })

	tx, err := fixture.stores.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := appaccess.New(fixture.stores.database).HostingGatewayStartupSnapshotTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := New(writerDB).InitializeRuntime(ctx, fixture.base.appA, deployment.ID, releaseID, "", 0,
		RuntimeGeneratedNode, planID, 1); err != nil {
		t.Fatal(err)
	}
	held, err := fixture.stores.generatedDomainStartupSnapshotTx(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(held.GeneratedDeployments) != 0 {
		t.Fatalf("held transaction observed later writer: %#v", held.GeneratedDeployments)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fresh, err := fixture.stores.GeneratedDomainStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.GeneratedDeployments) != 1 || fresh.GeneratedDeployments[0].ID != deployment.ID {
		t.Fatalf("fresh snapshot missed committed writer: %#v", fresh.GeneratedDeployments)
	}
}

func TestGeneratedOwnerStoresStartupSnapshotLifecycleSummaryUsesCanonicalValidation(t *testing.T) {
	t.Run("positive head missing current operation", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		if _, err := fixture.stores.database.Exec(`DROP TRIGGER application_desired_lifecycle_head_valid_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.stores.database.Exec(`UPDATE application_desired_lifecycle_heads
			SET desired_state='running',revision_number=1,updated_at=? WHERE app_id=?`,
			startupSnapshotStamp(), fixture.base.appA); err != nil {
			t.Fatal(err)
		}
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.LifecycleOperationCount != 0 ||
			!reflect.DeepEqual(snapshot.UnresolvedLifecycleApps, []string{fixture.base.appA}) {
			t.Fatalf("missing-current summary=%#v", snapshot)
		}
	})

	t.Run("complete retained history", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
		deployment := seedStartupGenericDeployment(t, fixture, planID, releaseID)
		if _, created, err := generatedruntimestate.New(fixture.stores.database).Begin(context.Background(),
			generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: fixture.base.appA,
				ReleaseID: releaseID, DeploymentPlanRevisionID: planID,
				DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"}}); err != nil || !created {
			t.Fatalf("begin runtime: created=%t error=%v", created, err)
		}
		dropStartupLifecycleInsertGuards(t, fixture.stores.database)
		pinsJSON, pinsDigest := startupLifecyclePins(t, deployment.ID, releaseID, planID)
		jobID := seedStartupLifecycleOperation(t, fixture, 1, "start", "", "running", deployment.ID,
			releaseID, planID, pinsJSON, pinsDigest)
		dropStartupLifecycleEffectInsertGuards(t, fixture.stores.database)
		seedStartupLifecycleEffect(t, fixture.stores.database, jobID, fixture.base.appA, 1, pinsDigest,
			applifecycle.BoundaryHealth, applifecycle.EffectObservationOnly, applifecycle.OutcomeHealthy)
		seedStartupLifecycleEffect(t, fixture.stores.database, jobID, fixture.base.appA, 1, pinsDigest,
			applifecycle.BoundaryPublish, applifecycle.EffectCommand, applifecycle.OutcomeServing)
		seedStartupLifecycleEffect(t, fixture.stores.database, jobID, fixture.base.appA, 1, pinsDigest,
			applifecycle.BoundaryComplete, applifecycle.EffectObservationOnly, applifecycle.OutcomeCompleteRunning)
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.LifecycleOperationCount != 1 || len(snapshot.UnresolvedLifecycleApps) != 0 {
			t.Fatalf("complete lifecycle summary=%#v", snapshot)
		}
		if len(snapshot.LifecycleOperations) != 1 || snapshot.LifecycleOperations[0] !=
			(GeneratedDomainStartupLifecycleOperation{AppID: fixture.base.appA, JobID: jobID,
				DeploymentID: deployment.ID, DesiredRevision: 1, Complete: true}) {
			t.Fatalf("complete lifecycle operations=%#v", snapshot.LifecycleOperations)
		}
		if len(snapshot.LifecycleBindings) != 1 || snapshot.LifecycleBindings[0] !=
			(GeneratedDomainStartupLifecycleBinding{DeploymentJobID: deployment.JobID,
				AppID: fixture.base.appA, Mode: deploymentbinding.ModeLegacyUninitialized}) {
			t.Fatalf("complete lifecycle bindings=%#v", snapshot.LifecycleBindings)
		}
	})

	fixture := newGeneratedOwnerDomainFixture(t)
	planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
	deployment := seedStartupGenericDeployment(t, fixture, planID, releaseID)
	if _, created, err := generatedruntimestate.New(fixture.stores.database).Begin(context.Background(),
		generatedruntimestate.BeginInput{DeploymentID: deployment.ID, AppID: fixture.base.appA,
			ReleaseID: releaseID, DeploymentPlanRevisionID: planID,
			DeploymentPlanRevisionNumber: 1, ComponentNames: []string{"api"}}); err != nil || !created {
		t.Fatalf("begin runtime: created=%t error=%v", created, err)
	}
	dropStartupLifecycleInsertGuards(t, fixture.stores.database)
	pinsJSON, pinsDigest := startupLifecyclePins(t, deployment.ID, releaseID, planID)
	firstJob := seedStartupLifecycleOperation(t, fixture, 1, "stop", "", "stopped", deployment.ID,
		releaseID, planID, pinsJSON, pinsDigest)
	if _, err := fixture.stores.database.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=? WHERE id=?`,
		startupSnapshotStamp(), firstJob); err != nil {
		t.Fatal(err)
	}
	secondJob := seedStartupLifecycleOperation(t, fixture, 2, "start", "stopped", "running", deployment.ID,
		releaseID, planID, pinsJSON, pinsDigest)
	snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LifecycleOperationCount != 2 || !reflect.DeepEqual(snapshot.UnresolvedLifecycleApps, []string{fixture.base.appA}) {
		t.Fatalf("valid two-operation incomplete lifecycle summary=%#v", snapshot)
	}

	if _, err := fixture.stores.database.Exec(`DROP TRIGGER application_lifecycle_effect_intent_valid_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.stores.database.Exec(`INSERT INTO application_lifecycle_effect_intents(
		job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
	) VALUES(?,'health','',1,1,'{}',?,?)`, secondJob, strings.Repeat("e", 64), startupSnapshotStamp()); err != nil {
		t.Fatal(err)
	}
	var historyBefore [6]int64
	if err := fixture.stores.database.QueryRow(`SELECT
		(SELECT COUNT(*) FROM application_desired_lifecycle_transitions),
		(SELECT COUNT(*) FROM application_lifecycle_operations),
		(SELECT COUNT(*) FROM application_lifecycle_operation_components),
		(SELECT COUNT(*) FROM application_lifecycle_effect_intents),
		(SELECT COUNT(*) FROM application_lifecycle_effect_receipts),
		(SELECT COUNT(*) FROM application_deployment_lifecycle_bindings)`).Scan(
		&historyBefore[0], &historyBefore[1], &historyBefore[2], &historyBefore[3], &historyBefore[4], &historyBefore[5]); err != nil {
		t.Fatal(err)
	}
	var changesBefore int64
	if err := fixture.stores.database.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	failed, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
	if !errors.Is(err, deploymentbinding.ErrInvalidBinding) || !reflect.DeepEqual(failed, GeneratedDomainStartupSnapshot{}) {
		t.Fatalf("earlier incomplete operation %s hid later malformed graph %s: snapshot=%#v error=%v",
			firstJob, secondJob, failed, err)
	}
	var historyAfter [6]int64
	if err := fixture.stores.database.QueryRow(`SELECT
		(SELECT COUNT(*) FROM application_desired_lifecycle_transitions),
		(SELECT COUNT(*) FROM application_lifecycle_operations),
		(SELECT COUNT(*) FROM application_lifecycle_operation_components),
		(SELECT COUNT(*) FROM application_lifecycle_effect_intents),
		(SELECT COUNT(*) FROM application_lifecycle_effect_receipts),
		(SELECT COUNT(*) FROM application_deployment_lifecycle_bindings)`).Scan(
		&historyAfter[0], &historyAfter[1], &historyAfter[2], &historyAfter[3], &historyAfter[4], &historyAfter[5]); err != nil {
		t.Fatal(err)
	}
	var changesAfter int64
	if err := fixture.stores.database.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if historyAfter != historyBefore || changesAfter != changesBefore {
		t.Fatalf("failed lifecycle validation changed history: counts %v -> %v, changes %d -> %d",
			historyBefore, historyAfter, changesBefore, changesAfter)
	}
}

func TestGeneratedOwnerStoresStartupSnapshotIsReadOnlyStableAndSecretFree(t *testing.T) {
	fixture := newGeneratedOwnerDomainFixture(t)
	var before, after int64
	if err := fixture.stores.database.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.stores.database.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || !reflect.DeepEqual(first, second) {
		t.Fatalf("snapshot was not read-only and stable: changes %d->%d equal=%t", before, after, reflect.DeepEqual(first, second))
	}
	for _, value := range []any{
		GeneratedDomainStartupDeployment{}, GeneratedDomainStartupArtifact{},
		GeneratedDomainStartupLifecycleOperation{}, GeneratedDomainStartupLifecycleBinding{},
		generatedruntimestate.GeneratedDomainStartupActiveHead{},
		generatedruntimestate.GeneratedDomainStartupDeployment{},
		generatedruntimestate.GeneratedDomainStartupComponent{},
		generatedruntimestate.GeneratedDomainStartupCandidateIntent{},
		generatedruntimestate.RouteAccountingHandoffAuthorization{},
		generatedruntimestate.RouteAccountingHandoffReceipt{},
		generatedruntimestate.RouteCompletionReceipt{},
	} {
		assertStartupSnapshotSafeValueType(t, reflect.TypeOf(value))
	}
}

func TestGeneratedOwnerStoresStartupSnapshotFailsClosedForCancellationClosedDatabaseAndMalformedRows(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(ctx)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
			t.Fatalf("canceled snapshot=%#v error=%v", snapshot, err)
		}
	})
	t.Run("closed database", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		if err := fixture.stores.database.Close(); err != nil {
			t.Fatal(err)
		}
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if err == nil || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
			t.Fatalf("closed snapshot=%#v error=%v", snapshot, err)
		}
	})
	t.Run("crossed generic parent", func(t *testing.T) {
		fixture := newGeneratedOwnerDomainFixture(t)
		planID, releaseID := seedStartupGeneratedPlanRelease(t, fixture)
		deployment := seedStartupGenericDeployment(t, fixture, planID, releaseID)
		if _, err := fixture.stores.database.Exec(`UPDATE jobs SET resource_id=? WHERE id=?`, fixture.base.appB, deployment.JobID); err != nil {
			t.Fatal(err)
		}
		snapshot, err := fixture.stores.GeneratedDomainStartupSnapshot(context.Background())
		if !errors.Is(err, ErrInvalidDeployment) || !reflect.DeepEqual(snapshot, GeneratedDomainStartupSnapshot{}) {
			t.Fatalf("crossed snapshot=%#v error=%v", snapshot, err)
		}
	})
}

func seedStartupGeneratedPlanRelease(t *testing.T, fixture generatedOwnerDomainFixture) (string, string) {
	t.Helper()
	planID, releaseID := uuid.NewString(), uuid.NewString()
	stamp := startupSnapshotStamp()
	if _, err := fixture.stores.database.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','test','1',?,'local',0,?,?,1,0,'',?,?,'accepted',?,?)`,
		planID, fixture.base.appA, "startup:"+planID, strings.Repeat("1", 64), strings.Repeat("2", 64),
		strings.Repeat("3", 64), fixture.base.actor, stamp, fixture.base.actor, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.stores.database.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,
		workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, fixture.base.appA,
		stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), planID); err != nil {
		t.Fatal(err)
	}
	return planID, releaseID
}

func seedStartupGenericDeployment(t *testing.T, fixture generatedOwnerDomainFixture, planID, releaseID string) Deployment {
	t.Helper()
	jobID := seedStartupGeneratedDeploymentJob(t, fixture)
	deployment, _, err := fixture.stores.repository.GetOrCreateByJob(context.Background(), fixture.base.appA, jobID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = fixture.stores.repository.InitializeRuntime(context.Background(), fixture.base.appA,
		deployment.ID, releaseID, "", 0, RuntimeGeneratedNode, planID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func seedStartupGeneratedDeploymentJob(t *testing.T, fixture generatedOwnerDomainFixture) string {
	t.Helper()
	validator, err := applifecycle.NewDeploymentBindingValidator(applifecycle.New(fixture.stores.database))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(fixture.stores.database, validator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(fixture.stores.database, bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: fixture.base.appA, IdempotencyKey: "startup-generated-deployment",
		RequestedBy: fixture.base.actor, Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created || created.Job.ID == "" ||
		created.Binding.JobID() != created.Job.ID || created.Binding.ApplicationID() != fixture.base.appA ||
		created.Binding.Mode() != deploymentbinding.ModeLegacyUninitialized {
		t.Fatalf("create generated deployment job: created=%t job=%q binding=%q/%q/%q error=%v",
			created.Created, created.Job.ID, created.Binding.JobID(), created.Binding.ApplicationID(), created.Binding.Mode(), err)
	}
	terminal, err := service.Cancel(created.Job.ID)
	if err != nil || terminal.Status != string(jobs.Cancelled) || terminal.Phase != "cancelled" {
		t.Fatalf("cancel generated deployment job: job=%q status=%q phase=%q error=%v",
			created.Job.ID, terminal.Status, terminal.Phase, err)
	}
	return created.Job.ID
}

func startupLifecyclePins(t *testing.T, deploymentID, releaseID, planID string) (string, string) {
	t.Helper()
	pins := applifecycle.GeneratedLifecyclePinsV1{
		Version: 1, DeploymentID: deploymentID, ReleaseID: releaseID, Slot: "blue", ActiveGeneration: 1,
		PlanRevision: applifecycle.RevisionRef{ID: planID, Number: 1},
		Configuration: applifecycle.ConfigurationRef{PlanRevision: applifecycle.RevisionRef{ID: planID, Number: 1},
			RuntimeComponents: []string{"api"}},
		BaselineDisposition: applifecycle.BaselineRunning,
		Components: []applifecycle.RetainedComponent{{
			Name: "api", Role: "web", Technology: "test", ContainerID: strings.Repeat("1", 64),
			ContainerName: "rig-test-api", ArtifactID: uuid.NewString(), ImageContentID: "sha256:" + strings.Repeat("2", 64),
			BuildDefinitionDigest: strings.Repeat("3", 64), NetworkName: "rig-test", NetworkAlias: "api",
			InternalPort: 8080, RunCommandDigest: strings.Repeat("4", 64),
			Limits: applifecycle.RetainedLimits{MemoryBytes: 1, MemorySwapBytes: 1, CPUQuota: 1, CPUPeriod: 1,
				PIDsLimit: 1, LogDriver: "json-file", LogMaxSize: "1m", LogMaxFiles: 1, HardeningPolicyVersion: "v1"},
		}},
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_upgrade", SelectedLineageGeneration: 1,
			SelectedLineageOperationID: uuid.NewString(), SelectedLineageTerminalDigest: strings.Repeat("5", 64),
			ProtectedFormatVersion: 1, TargetRecordDigest: strings.Repeat("6", 64)},
	}
	encoded, err := json.Marshal(pins)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:])
}

func dropStartupLifecycleInsertGuards(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{
		"application_lifecycle_operation_valid_insert",
		"application_lifecycle_operation_component_valid_insert",
	} {
		if _, err := db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

func dropStartupLifecycleEffectInsertGuards(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{
		"application_lifecycle_effect_intent_valid_insert",
		"application_lifecycle_effect_receipt_valid_insert",
	} {
		if _, err := db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

func seedStartupLifecycleEffect(t *testing.T, db *sql.DB, jobID, appID string, revision int64, pinsDigest string,
	boundary applifecycle.Boundary, mode applifecycle.EffectMode, outcome applifecycle.Outcome,
) {
	t.Helper()
	request := applifecycle.EffectRequestV1{Version: 1, OperationID: jobID, ApplicationID: appID,
		DesiredRevision: revision, Boundary: boundary, Mode: mode, PinsDigest: pinsDigest}
	requestJSON, requestDigest := startupCanonicalJSON(t, request)
	if _, err := db.Exec(`INSERT INTO application_lifecycle_effect_intents(
		job_id,boundary,component_name,writer_attempt,request_version,request_json,request_digest,created_at
	) VALUES(?,?, '',1,1,?,?,?)`, jobID, string(boundary), requestJSON, requestDigest, startupSnapshotStamp()); err != nil {
		t.Fatal(err)
	}
	proof := applifecycle.EffectProofV1{Version: 1, OperationID: jobID, ApplicationID: appID,
		DesiredRevision: revision, Boundary: boundary, Outcome: outcome, IntentDigest: requestDigest,
		EvidenceDigest: strings.Repeat("e", 64), PinsDigest: pinsDigest}
	proofJSON, proofDigest := startupCanonicalJSON(t, proof)
	if _, err := db.Exec(`INSERT INTO application_lifecycle_effect_receipts(
		job_id,boundary,component_name,writer_attempt,outcome,proof_version,proof_json,proof_digest,observed_at
	) VALUES(?,?, '',1,?,1,?,?,?)`, jobID, string(boundary), string(outcome), proofJSON, proofDigest,
		startupSnapshotStamp()); err != nil {
		t.Fatal(err)
	}
}

func startupCanonicalJSON(t *testing.T, value any) (string, string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:])
}

func seedStartupLifecycleOperation(t *testing.T, fixture generatedOwnerDomainFixture, revision int64,
	action, previousState, desiredState, deploymentID, releaseID, planID, pinsJSON, pinsDigest string,
) string {
	t.Helper()
	jobID := uuid.NewString()
	idempotencyKey := "startup-lifecycle-" + action
	stamp := startupSnapshotStamp()
	if _, err := fixture.stores.database.Exec(`INSERT INTO jobs(
		id,type,resource_type,resource_id,status,phase,requested_by,idempotency_key,input_json,created_at,updated_at
	) VALUES(? ,?,'application',?,'queued','queued',?,?,'{}',?,?)`, jobID, action, fixture.base.appA,
		fixture.base.actor, idempotencyKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	var previous any
	if previousState != "" {
		previous = previousState
	}
	if _, err := fixture.stores.database.Exec(`INSERT INTO application_desired_lifecycle_transitions(
		app_id,revision_number,expected_revision,previous_state,desired_state,job_id,action,actor_id,created_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, fixture.base.appA, revision, revision-1, previous, desiredState,
		jobID, action, fixture.base.actor, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.stores.database.Exec(`INSERT INTO application_lifecycle_operations(
		job_id,app_id,desired_revision,expected_revision,action,actor_id,idempotency_key,version,runtime_strategy,
		baseline_disposition,deployment_id,active_generation,release_id,slot,plan_revision_id,plan_revision_number,
		configuration_revision_id,configuration_revision_number,configuration_format,pins_json,pins_digest,created_at
	) VALUES(?,?,?,?,?,?,?,1,'generated_node','running',?,1,?,'blue',?,1,'',0,0,?,?,?)`,
		jobID, fixture.base.appA, revision, revision-1, action, fixture.base.actor, idempotencyKey,
		deploymentID, releaseID, planID, pinsJSON, pinsDigest, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.stores.database.Exec(`INSERT INTO application_lifecycle_operation_components(job_id,ordinal,component_name)
		VALUES(?,0,'api')`, jobID); err != nil {
		t.Fatal(err)
	}
	return jobID
}

func assertStartupSnapshotSafeValueType(t *testing.T, value reflect.Type) {
	t.Helper()
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		name := strings.ToLower(field.Name)
		if strings.Contains(name, "secret") || strings.Contains(name, "json") {
			t.Fatalf("unsafe startup field %s.%s", value, field.Name)
		}
		switch field.Type.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map:
			t.Fatalf("capability-bearing startup field %s.%s has type %s", value, field.Name, field.Type)
		case reflect.Slice:
			if field.Type.Elem().Kind() == reflect.Uint8 {
				t.Fatalf("raw byte startup field %s.%s", value, field.Name)
			}
		}
	}
}

func startupSnapshotStamp() string {
	return time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
}
