//go:build live_docker

// Package liveauthority prepares real persisted deployment authority for
// direct generated-runtime Docker fixtures. It is compiled only for hosted
// live_docker tests and does not provide a production authorization fallback.
package liveauthority

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

// Authority owns the scoped, same-database deployment authority used by a
// direct live runtime fixture.
type Authority struct {
	repository   *deployments.Repository
	db           *sql.DB
	jobs         *jobs.Service
	runtime      *generatedruntimestate.Repository
	actorID      string
	scope        deploymenteffects.Scope
	bound        map[string]*boundCandidate
	currentByApp map[string]*boundCandidate
}

type boundCandidate struct {
	spec     generatedruntime.CandidateSpec
	jobID    string
	finished bool
}

// New constructs a concrete guarded deployments repository on db and keeps a
// real effects lease for the lifetime of the test.
func New(t *testing.T, db *sql.DB, workingDirectory string) *Authority {
	t.Helper()
	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuard(db, guard)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release direct live generated authority: %v", err)
		}
	})
	authority := &Authority{
		repository:   repository,
		db:           db,
		jobs:         jobService,
		runtime:      generatedruntimestate.New(db),
		actorID:      uuid.NewString(),
		scope:        scope,
		bound:        make(map[string]*boundCandidate),
		currentByApp: make(map[string]*boundCandidate),
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, authority.actorID, "direct-live-"+authority.actorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return authority
}

// Repository is the concrete guarded repository required by Engine.
func (a *Authority) Repository() *deployments.Repository { return a.repository }

// BindCandidate persists an immutable job/binding/deployment/runtime graph and
// returns the original scoped context and typed network request for spec.
func (a *Authority) BindCandidate(t *testing.T, parent context.Context, spec generatedruntime.CandidateSpec) (context.Context, generatedruntime.CandidateSpec) {
	t.Helper()
	if existing := a.bound[spec.DeploymentID]; existing != nil {
		expected, supplied := existing.spec, spec
		expected.Network = deployments.GeneratedNetworkRequest{}
		supplied.Network = deployments.GeneratedNetworkRequest{}
		if !reflect.DeepEqual(expected, supplied) {
			t.Fatal("direct live deployment identity was reused with different immutable candidate authority")
		}
		return deploymenteffects.WithScope(parent, a.scope), existing.spec
	}
	previous := a.currentByApp[spec.AppID]
	if previous != nil {
		a.FinishCandidate(t, previous.spec.DeploymentID)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`, spec.AppID, spec.AppID, spec.AppID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if spec.ConfigurationRevisionID == "" {
		if previous != nil {
			spec.ConfigurationRevisionID = previous.spec.ConfigurationRevisionID
			spec.ConfigurationRevisionNumber = previous.spec.ConfigurationRevisionNumber
		} else {
			spec.ConfigurationRevisionID = uuid.NewString()
		}
	}
	if spec.ConfigurationRevisionNumber == 0 {
		spec.ConfigurationRevisionNumber = 1
	}
	if spec.DeploymentPlanRevisionNumber == 0 {
		spec.DeploymentPlanRevisionNumber = 1
	}
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,0)`, spec.ConfigurationRevisionID, spec.AppID, spec.ConfigurationRevisionNumber, "direct-live:"+spec.ConfigurationRevisionID, a.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO deployment_plan_revisions(id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at) VALUES(?,?,?,?,'generated_node','direct-live','1',?,'local',0,?,?,1,0,'',?,?,'accepted',?,?)`, spec.DeploymentPlanRevisionID, spec.AppID, spec.DeploymentPlanRevisionNumber, "direct-live:"+spec.DeploymentPlanRevisionID, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), a.actorID, stamp, a.actorID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO releases(id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,?,?,?)`, spec.ReleaseID, spec.AppID, stamp, strings.Repeat("4", 64), strings.Repeat("5", 64), spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	created, err := a.jobs.CreateDeploymentWithInputTransaction(parent, jobs.DeploymentCreateRequest{
		ApplicationID:  spec.AppID,
		IdempotencyKey: "direct-live-" + spec.DeploymentID,
		RequestedBy:    a.actorID,
		Input:          jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create direct live deployment job: created=%t error=%v", created.Created, err)
	}
	if _, err := a.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO deployments(id,app_id,release_id,job_id,status,configuration_mode,actual_configuration_revision_id,actual_configuration_revision_number,provenance_initialized,runtime_strategy,deployment_plan_revision_id,deployment_plan_revision_number) VALUES(?,?,?,?,'preparing','current',?,?,1,'generated_node',?,?)`, spec.DeploymentID, spec.AppID, spec.ReleaseID, created.Job.ID, spec.ConfigurationRevisionID, spec.ConfigurationRevisionNumber, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, spec.AppID, 1)
	if err != nil {
		t.Fatal(err)
	}
	boundContext := deploymenteffects.WithScope(parent, a.scope)
	if err := a.repository.Gate(boundContext, deployments.GateRequest{Claim: claim, DeploymentID: spec.DeploymentID}); err != nil {
		t.Fatal(err)
	}
	runtimeDeployment, createdRuntime, err := a.runtime.Begin(parent, generatedruntimestate.BeginInput{
		DeploymentID:                 spec.DeploymentID,
		AppID:                        spec.AppID,
		ReleaseID:                    spec.ReleaseID,
		DeploymentPlanRevisionID:     spec.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
		ComponentNames:               []string{spec.ComponentName},
	})
	if err != nil || !createdRuntime {
		t.Fatalf("begin direct live generated runtime: created=%t error=%v", createdRuntime, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate} {
		runtimeDeployment, err = a.runtime.Advance(parent, spec.AppID, spec.DeploymentID, runtimeDeployment.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	if generatedruntime.Slot(runtimeDeployment.PreviousActiveSlot) != spec.ActiveSlot {
		t.Fatalf("direct live predecessor slot=%q want=%q", runtimeDeployment.PreviousActiveSlot, spec.ActiveSlot)
	}
	spec.Network = deployments.GeneratedNetworkRequest{
		Claim:                        claim,
		DeploymentID:                 spec.DeploymentID,
		ReleaseID:                    spec.ReleaseID,
		ConfigurationRevisionID:      spec.ConfigurationRevisionID,
		ConfigurationRevisionNumber:  spec.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID:     spec.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: spec.DeploymentPlanRevisionNumber,
	}
	bound := &boundCandidate{spec: spec, jobID: created.Job.ID}
	a.bound[spec.DeploymentID] = bound
	a.currentByApp[spec.AppID] = bound
	return boundContext, spec
}

// FinishCandidate records enough canonical runtime history for a subsequent
// direct live replacement to retain the accepted predecessor slot.
func (a *Authority) FinishCandidate(t *testing.T, deploymentID string) {
	t.Helper()
	bound := a.bound[deploymentID]
	if bound == nil {
		t.Fatal("direct live generated deployment is not bound")
	}
	if bound.finished {
		return
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	spec := bound.spec
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO generated_image_artifacts(id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,build_definition_digest,attempt_number,image_content_id,state,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,'direct-live',?,1,?,'ready',?,?,?)`, spec.ArtifactID, spec.ReleaseID, spec.DeploymentPlanRevisionID, spec.DeploymentPlanRevisionNumber, spec.ComponentName, spec.BuildDefinitionDigest, spec.ImageContentID, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	component, err := a.runtime.SetImageReady(context.Background(), spec.AppID, spec.DeploymentID, spec.ComponentName, spec.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	description, err := generatedruntime.DescribeInactiveCandidate(spec.AppID, spec.ComponentName, spec.ActiveSlot)
	if err != nil {
		t.Fatal(err)
	}
	component, err = a.runtime.SetContainerStarting(context.Background(), spec.AppID, spec.DeploymentID, spec.ComponentName, description.ContainerName)
	if err != nil {
		t.Fatal(err)
	}
	component, err = a.runtime.SetContainerRunning(context.Background(), spec.AppID, spec.DeploymentID, spec.ComponentName, strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.AdvanceComponent(context.Background(), spec.AppID, spec.DeploymentID, spec.ComponentName, component.State, generatedruntimestate.ComponentHealthy); err != nil {
		t.Fatal(err)
	}
	runtimeDeployment, err := a.runtime.Get(context.Background(), spec.AppID, spec.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute} {
		runtimeDeployment, err = a.runtime.Advance(context.Background(), spec.AppID, spec.DeploymentID, runtimeDeployment.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	active, err := a.runtime.Active(context.Background(), spec.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if _, switched, err := a.runtime.SwitchActive(context.Background(), spec.AppID, spec.DeploymentID, active.Generation); err != nil || !switched {
		t.Fatalf("switch direct live generated authority: switched=%t error=%v", switched, err)
	}
	for _, next := range []generatedruntimestate.Phase{generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded} {
		runtimeDeployment, err = a.runtime.Advance(context.Background(), spec.AppID, spec.DeploymentID, runtimeDeployment.Phase, next, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec(`UPDATE jobs SET status='succeeded',phase='succeeded',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, bound.jobID); err != nil {
		t.Fatal(err)
	}
	bound.finished = true
}

// RejectCandidate records a failed candidate and restores the accepted
// predecessor as the fixture's current deployment authority.
func (a *Authority) RejectCandidate(t *testing.T, rejectedDeploymentID, retainedDeploymentID string) {
	t.Helper()
	rejected := a.bound[rejectedDeploymentID]
	if rejected == nil || rejected.finished {
		t.Fatal("direct live rejected-candidate authority is invalid")
	}
	var retained *boundCandidate
	if retainedDeploymentID != "" {
		retained = a.bound[retainedDeploymentID]
		if retained == nil || retained.spec.AppID != rejected.spec.AppID || !retained.finished {
			t.Fatal("direct live retained-candidate authority is invalid")
		}
	}
	runtimeDeployment, err := a.runtime.Get(context.Background(), rejected.spec.AppID, rejected.spec.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.Advance(context.Background(), rejected.spec.AppID, rejected.spec.DeploymentID, runtimeDeployment.Phase, generatedruntimestate.PhaseFailed, generatedruntimestate.DiagnosticRuntimeUnavailable); err != nil {
		t.Fatal(err)
	}
	if _, err := a.repository.Transition(context.Background(), rejected.spec.AppID, rejected.spec.DeploymentID, deployments.Failed, "runtime_unavailable"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`UPDATE jobs SET status='failed',phase='failed',error_code='runtime_unavailable',finished_at=?,updated_at=? WHERE id=?`, stamp, stamp, rejected.jobID); err != nil {
		t.Fatal(err)
	}
	if retained == nil {
		delete(a.currentByApp, rejected.spec.AppID)
	} else {
		a.currentByApp[retained.spec.AppID] = retained
	}
}
