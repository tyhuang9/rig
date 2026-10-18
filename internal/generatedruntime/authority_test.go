package generatedruntime

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

func newConfiguredRuntimeDeployments(t *testing.T, workingDirectory string) *deployments.Repository {
	t.Helper()
	_, repository, _ := configuredRuntimeDeployments(t, workingDirectory)
	return repository
}

func configuredRuntimeDeployments(t *testing.T, workingDirectory string) (*sql.DB, *deployments.Repository, *deploymentbinding.Repository) {
	t.Helper()
	db, err := databasefixture.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lifecycle := applifecycle.New(db)
	bindingValidator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, bindingValidator)
	if err != nil {
		t.Fatal(err)
	}
	positiveFence, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positiveFence, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuard(db, guard)
	if err != nil {
		t.Fatal(err)
	}
	return db, repository, bindings
}

type runtimeNetworkAuthority struct {
	db         *sql.DB
	repository *deployments.Repository
	context    context.Context
	release    func() error
	scope      deploymenteffects.Scope
	spec       CandidateSpec
}

func newRuntimeNetworkAuthority(t *testing.T, workingDirectory string, spec CandidateSpec) runtimeNetworkAuthority {
	t.Helper()
	db, repository, bindings := configuredRuntimeDeployments(t, workingDirectory)
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	actorID, machineID := uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{actorID, "runtime-owner", "hash", stamp, stamp}},
		{`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, []any{machineID, "local", stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,'draft',?,?)`, []any{spec.AppID, spec.AppID, spec.AppID, machineID, stamp, stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	created, err := jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: spec.AppID, IdempotencyKey: "generated-network-authority", RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create generated network job: created=%t error=%v", created.Created, err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.Read(context.Background(), created.Job.ID); err != nil {
		t.Fatalf("read generated network binding: %v", err)
	}
	if spec.ConfigurationRevisionID == "" {
		spec.ConfigurationRevisionID = uuid.NewString()
	}
	if spec.ConfigurationRevisionNumber == 0 {
		spec.ConfigurationRevisionNumber = 1
	}
	if spec.DeploymentPlanRevisionNumber == 0 {
		spec.DeploymentPlanRevisionNumber = 1
	}
	if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,0)`, spec.ConfigurationRevisionID, spec.AppID, spec.ConfigurationRevisionNumber, "generated-network:"+spec.ConfigurationRevisionID, actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,?,?,'generated_node','test','1',?,'local',0,?,?,1,0,'',?,?,'accepted',?,?)`, spec.DeploymentPlanRevisionID, spec.AppID, spec.DeploymentPlanRevisionNumber, "generated-network:"+spec.DeploymentPlanRevisionID, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), actorID, stamp, actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`, spec.ReleaseID, spec.AppID, stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	state := generatedruntimestate.New(db)
	if spec.ActiveSlot != "" {
		seedRuntimeActivePredecessor(t, db, state, spec, stamp)
	}
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,?,'preparing','current',?,?,1,'generated_node',?,?)`, spec.DeploymentID, spec.AppID, spec.ReleaseID, created.Job.ID, spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, spec.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release generated runtime scope: %v", err)
		}
	})
	ctx := deploymenteffects.WithScope(context.Background(), scope)
	if err := repository.Gate(ctx, deployments.GateRequest{Claim: claim, DeploymentID: spec.DeploymentID}); err != nil {
		t.Fatal(err)
	}
	runtimeDeployment, createdRuntime, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: spec.DeploymentID, AppID: spec.AppID, ReleaseID: spec.ReleaseID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
		ComponentNames: []string{spec.ComponentName},
	})
	if err != nil || !createdRuntime {
		t.Fatalf("begin generated runtime: created=%t error=%v", createdRuntime, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
		runtimeDeployment, err = state.Advance(context.Background(), spec.AppID, spec.DeploymentID, runtimeDeployment.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	spec.Network = deployments.GeneratedNetworkRequest{
		Claim: claim, DeploymentID: spec.DeploymentID, ReleaseID: spec.ReleaseID,
		ConfigurationRevisionID: spec.ConfigurationRevisionID, ConfigurationRevisionNumber: spec.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
	}
	return runtimeNetworkAuthority{db: db, repository: repository, context: ctx, release: release, scope: scope, spec: spec}
}

func seedRuntimeActivePredecessor(t *testing.T, db *sql.DB, state *generatedruntimestate.Repository, spec CandidateSpec, stamp string) {
	t.Helper()
	if spec.ActiveSlot != SlotBlue {
		t.Fatalf("unsupported active predecessor slot %q", spec.ActiveSlot)
	}
	priorID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,release_id,status,started_at,finished_at,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,'succeeded',?,?,'current',?,?,1,'generated_node',?,?)`, priorID, spec.AppID, spec.ReleaseID, stamp, stamp, spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	value, created, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: priorID, AppID: spec.AppID, ReleaseID: spec.ReleaseID,
		DeploymentPlanRevisionID: spec.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
		ComponentNames: []string{spec.ComponentName},
	})
	if err != nil || !created {
		t.Fatalf("begin generated predecessor: created=%t error=%v", created, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
		value, err = state.Advance(context.Background(), spec.AppID, priorID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	artifactID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,'test',?,1,?,'ready',?,?,?)`, artifactID, spec.ReleaseID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber, spec.ComponentName, strings.Repeat("6", 64), "sha256:"+strings.Repeat("7", 64), stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	component, err := state.SetImageReady(context.Background(), spec.AppID, priorID, spec.ComponentName, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	component, err = state.SetContainerStarting(context.Background(), spec.AppID, priorID, spec.ComponentName, containerName(spec.AppID, spec.ComponentName, SlotBlue))
	if err != nil {
		t.Fatal(err)
	}
	component, err = state.SetContainerRunning(context.Background(), spec.AppID, priorID, spec.ComponentName, strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.AdvanceComponent(context.Background(), spec.AppID, priorID, spec.ComponentName, component.State, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute} {
		value, err = state.Advance(context.Background(), spec.AppID, priorID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, switched, err := state.SwitchActive(context.Background(), spec.AppID, priorID, 0); err != nil || !switched {
		t.Fatalf("switch generated predecessor: switched=%t error=%v", switched, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded} {
		value, err = state.Advance(context.Background(), spec.AppID, priorID, value.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
}
