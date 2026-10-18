package deployments

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedrecovery"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type repositoryFixture struct {
	repository *Repository
	dataRoot   string
	appA       string
	appB       string
	actor      string
	jobA       string
	jobB       string
}

type guardedRepositoryFixture struct {
	repository *Repository
	guard      *deploymentexecution.Guard
	jobs       *jobs.Service
	bindings   *deploymentbinding.Repository
	context    context.Context
	appID      string
	actorID    string
	sequence   int
}

func TestGetOrCreateByJobIsAtomicAndRejectsReplayMismatch(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()

	const callers = 8
	results := make(chan Deployment, callers)
	errorsSeen := make(chan error, callers)
	created := make(chan bool, callers)
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for i := 0; i < callers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			deployment, wasCreated, err := fixture.repository.GetOrCreateByJob(ctx, fixture.appA, fixture.jobA, "current")
			results <- deployment
			created <- wasCreated
			errorsSeen <- err
		}()
	}
	start.Done()
	workers.Wait()
	close(results)
	close(created)
	close(errorsSeen)

	var deploymentID string
	createdCount := 0
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	for value := range created {
		if value {
			createdCount++
		}
	}
	for deployment := range results {
		if deploymentID == "" {
			deploymentID = deployment.ID
		}
		if deployment.ID != deploymentID || deployment.AppID != fixture.appA || deployment.JobID != fixture.jobA {
			t.Fatalf("inconsistent linkage: %#v", deployment)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	if _, _, err := fixture.repository.GetOrCreateByJob(ctx, fixture.appA, fixture.jobA, "original"); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("mode mismatch error = %v", err)
	}
	if _, _, err := fixture.repository.GetOrCreateByJob(ctx, fixture.appB, fixture.jobA, "current"); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("cross-app replay error = %v", err)
	}
}

func newRepositoryFixture(t *testing.T) repositoryFixture {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := database.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := repositoryFixture{repository: New(db), dataRoot: dataRoot, appA: uuid.NewString(), appB: uuid.NewString(), actor: uuid.NewString(), jobA: uuid.NewString(), jobB: uuid.NewString()}
	machine := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`, fixture.actor, "owner", "hash", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,?,'local','ready','test','test','test','test',?,?)`, machine, "local", now, now); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{fixture.appA, fixture.appB} {
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?, 'draft',?,?)`, app, app, app, machine, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{fixture.jobA, fixture.appA}, {fixture.jobB, fixture.appB}} {
		if _, err := db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,?,?)`, pair[0], pair[1], fixture.actor, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func newGuardedRepositoryFixture(t *testing.T, fixture repositoryFixture) *guardedRepositoryFixture {
	t.Helper()
	if _, err := fixture.repository.db.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled' WHERE id IN (?,?)`, fixture.jobA, fixture.jobB); err != nil {
		t.Fatal(err)
	}
	lifecycle := applifecycle.New(fixture.repository.db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(fixture.repository.db, validator)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewWithDeploymentBindings(fixture.repository.db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(fixture.repository.db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewWithExecutionGuard(fixture.repository.db, guard)
	if err != nil {
		t.Fatal(err)
	}
	repository.now = fixture.repository.now
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release deployment effects scope: %v", err)
		}
	})
	return &guardedRepositoryFixture{
		repository: repository, guard: guard, jobs: jobService, bindings: bindings,
		context: deploymenteffects.WithScope(context.Background(), scope),
		appID:   fixture.appA, actorID: fixture.actor,
	}
}

func (f *guardedRepositoryFixture) deployment(t *testing.T) (Deployment, deploymentexecution.Claim) {
	t.Helper()
	f.sequence++
	created, err := f.jobs.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: f.appID, IdempotencyKey: fmt.Sprintf("guarded-policy-%d", f.sequence), RequestedBy: f.actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create guarded deployment job: created=%t error=%v", created.Created, err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := f.repository.db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	deployment, err := f.repository.Create(context.Background(), f.appID, created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = f.repository.Initialize(context.Background(), f.appID, deployment.ID, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, f.appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return deployment, claim
}

func (f *guardedRepositoryFixture) gate(deployment Deployment, claim deploymentexecution.Claim, findings []Finding) error {
	return f.repository.Gate(f.context, GateRequest{Claim: claim, DeploymentID: deployment.ID, Findings: findings})
}

type gateDurableSnapshot struct {
	deployment Deployment
	jobStatus  string
	jobPhase   string
	jobAttempt int
	approvals  []string
}

func (f *guardedRepositoryFixture) snapshot(t *testing.T, deployment Deployment, claim deploymentexecution.Claim) gateDurableSnapshot {
	t.Helper()
	value, err := f.repository.Get(context.Background(), f.appID, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := gateDurableSnapshot{deployment: value}
	if err := f.repository.db.QueryRow(`SELECT status,phase,attempt FROM jobs WHERE id=?`, claim.JobID()).Scan(&result.jobStatus, &result.jobPhase, &result.jobAttempt); err != nil {
		t.Fatal(err)
	}
	rows, err := f.repository.db.Query(`SELECT id,app_id,policy_version,capability,scope,fingerprint,COALESCE(revoked_at,'') FROM runtime_approvals ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var values [7]string
		if err := rows.Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6]); err != nil {
			t.Fatal(err)
		}
		result.approvals = append(result.approvals, strings.Join(values[:], "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f repositoryFixture) localDeployment(t *testing.T) Deployment {
	t.Helper()
	value, err := f.repository.Create(context.Background(), f.appA, f.jobA, "current")
	if err != nil {
		t.Fatal(err)
	}
	value, err = f.repository.Initialize(context.Background(), f.appA, value.ID, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func finding(capability, scope, disposition string) Finding {
	return Finding{PolicyVersion: "compose-runtime-v1", Capability: capability, Scope: scope, Fingerprint: findingFingerprint("compose-runtime-v1", capability, scope), Disposition: disposition}
}

func TestInitializeLocksLocalAndReleaseProvenanceAndEnforcesIDOR(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	local := fixture.localDeployment(t)
	if !local.ProvenanceInitialized || local.ReleaseID != "" || local.ActualConfigurationRevisionNumber != 0 {
		t.Fatalf("local provenance = %#v", local)
	}
	if _, err := fixture.repository.Initialize(ctx, fixture.appA, local.ID, uuid.NewString(), "", 0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second local initialization = %v", err)
	}
	if _, err := fixture.repository.Get(ctx, fixture.appB, local.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-app get = %v", err)
	}
	if _, err := fixture.repository.db.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled' WHERE id=?`, fixture.jobA); err != nil {
		t.Fatal(err)
	}

	releaseID := uuid.NewString()
	if _, err := fixture.repository.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at) VALUES(?,?,'ready','{}',datetime('now'))`, releaseID, fixture.appA); err != nil {
		t.Fatal(err)
	}
	jobID := uuid.NewString()
	if _, err := fixture.repository.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,datetime('now'),datetime('now'))`, jobID, fixture.appA, fixture.actor); err != nil {
		t.Fatal(err)
	}
	releaseDeployment, err := fixture.repository.Create(ctx, fixture.appA, jobID, "original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Initialize(ctx, fixture.appA, releaseDeployment.ID, releaseID, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Initialize(ctx, fixture.appA, releaseDeployment.ID, "", "", 0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second release initialization = %v", err)
	}
}

func TestInitializeRuntimePinsGeneratedPlanAndComposeRemainsCompatible(t *testing.T) {
	t.Run("generated plan is exact and immutable", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		planID, releaseID := seedRuntimePlanRelease(t, fixture, RuntimeGeneratedNode)
		deployment, err := fixture.repository.Create(context.Background(), fixture.appA, fixture.jobA, "current")
		if err != nil {
			t.Fatal(err)
		}
		deployment, err = fixture.repository.InitializeRuntime(context.Background(), fixture.appA, deployment.ID, releaseID, "", 0, RuntimeGeneratedNode, planID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if deployment.RuntimeStrategy != RuntimeGeneratedNode || deployment.DeploymentPlanRevisionID != planID || deployment.DeploymentPlanRevisionNumber != 1 {
			t.Fatalf("runtime provenance = %#v", deployment)
		}
		if _, err := fixture.repository.db.Exec(`UPDATE deployments SET deployment_plan_revision_number=2 WHERE id=?`, deployment.ID); err == nil {
			t.Fatal("generated runtime plan provenance was mutable")
		}
	})

	t.Run("compose inherits a release-pinned compose plan", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		planID, releaseID := seedRuntimePlanRelease(t, fixture, RuntimeCompose)
		deployment, err := fixture.repository.Create(context.Background(), fixture.appA, fixture.jobA, "current")
		if err != nil {
			t.Fatal(err)
		}
		deployment, err = fixture.repository.Initialize(context.Background(), fixture.appA, deployment.ID, releaseID, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if deployment.RuntimeStrategy != RuntimeCompose || deployment.DeploymentPlanRevisionID != planID || deployment.DeploymentPlanRevisionNumber != 1 {
			t.Fatalf("compose runtime provenance = %#v", deployment)
		}
	})

	t.Run("compose cannot execute a generated release", func(t *testing.T) {
		fixture := newRepositoryFixture(t)
		_, releaseID := seedRuntimePlanRelease(t, fixture, RuntimeGeneratedNode)
		deployment, err := fixture.repository.Create(context.Background(), fixture.appA, fixture.jobA, "current")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repository.Initialize(context.Background(), fixture.appA, deployment.ID, releaseID, "", 0); !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("generated release initialized as Compose: %v", err)
		}
	})
}

func seedRuntimePlanRelease(t *testing.T, fixture repositoryFixture, strategy RuntimeStrategy) (string, string) {
	t.Helper()
	planID := uuid.NewString()
	releaseID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := fixture.repository.db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,?,'test','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
		planID, fixture.appA, "apps/"+fixture.appA+"/deployment-plans/"+planID+".secret", strategy,
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), fixture.actor, now, fixture.actor, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,workspace_tree_sha256,
		deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`, releaseID, fixture.appA, now, strings.Repeat("b", 64), strings.Repeat("d", 64), planID); err != nil {
		t.Fatal(err)
	}
	return planID, releaseID
}

func TestReleaseRuntimeStrategyComesFromPinnedPlan(t *testing.T) {
	tests := []struct {
		name     string
		strategy RuntimeStrategy
		legacy   bool
	}{
		{name: "legacy release defaults to compose", strategy: RuntimeCompose, legacy: true},
		{name: "compose plan remains compose", strategy: RuntimeCompose},
		{name: "generated plan is exposed", strategy: RuntimeGeneratedNode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t)
			var releaseID string
			if test.legacy {
				releaseID = uuid.NewString()
				if _, err := fixture.repository.db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,workspace_state,workspace_tree_sha256) VALUES(?,?,'ready','{}',datetime('now'),'ready',?)`, releaseID, fixture.appA, strings.Repeat("d", 64)); err != nil {
					t.Fatal(err)
				}
			} else {
				_, releaseID = seedRuntimePlanRelease(t, fixture, test.strategy)
			}

			release, err := fixture.repository.Release(context.Background(), fixture.appA, releaseID)
			if err != nil {
				t.Fatal(err)
			}
			if release.RuntimeStrategy != test.strategy {
				t.Fatalf("release runtime strategy = %q, want %q", release.RuntimeStrategy, test.strategy)
			}
			releases, err := fixture.repository.Releases(context.Background(), fixture.appA, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(releases) != 1 || releases[0].RuntimeStrategy != test.strategy {
				t.Fatalf("release history = %#v", releases)
			}
		})
	}
}

func TestGatePersistsFullFindingSetAndAtomicallyRechecksApprovals(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	guarded := newGuardedRepositoryFixture(t, fixture)
	deployment, claim := guarded.deployment(t)
	one := finding("privileged", "service:web", "approval_required")
	two := finding("external_bind", "service:web:/srv/data:/data", "approval_required")
	if err := guarded.gate(deployment, claim, []Finding{two, one}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("first gate = %v", err)
	}
	paused, err := fixture.repository.Get(ctx, fixture.appA, deployment.ID)
	if err != nil || paused.Status != NeedsAttention || len(paused.Findings) != 2 {
		t.Fatalf("paused = %#v, %v", paused, err)
	}
	if _, _, err := fixture.repository.Grant(ctx, fixture.appB, fixture.actor, one.Fingerprint); !errors.Is(err, ErrInvalidDeployment) {
		t.Fatalf("cross-app grant = %v", err)
	}
	approvalOne, created, err := fixture.repository.Grant(ctx, fixture.appA, fixture.actor, one.Fingerprint)
	if err != nil || !created {
		t.Fatalf("grant one = %#v %t %v", approvalOne, created, err)
	}
	if _, duplicate, err := fixture.repository.Grant(ctx, fixture.appA, fixture.actor, one.Fingerprint); err != nil || duplicate {
		t.Fatalf("idempotent grant = created %t err %v", duplicate, err)
	}
	if err := guarded.gate(deployment, claim, []Finding{one, two}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("partially approved gate = %v", err)
	}
	if _, _, err := fixture.repository.Grant(ctx, fixture.appA, fixture.actor, two.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := guarded.gate(deployment, claim, []Finding{one, two}); err != nil {
		t.Fatal(err)
	}
	applying, err := fixture.repository.Get(ctx, fixture.appA, deployment.ID)
	if err != nil || applying.Status != Applying || applying.StartedAt.IsZero() {
		t.Fatalf("applying = %#v, %v", applying, err)
	}
	if _, err := fixture.repository.Revoke(ctx, fixture.appA, approvalOne.ID, fixture.actor); !errors.Is(err, ErrApprovalInUse) {
		t.Fatalf("active approval revocation = %v", err)
	}
	if _, err := fixture.repository.Transition(ctx, fixture.appA, deployment.ID, Failed, "apply_failed"); err != nil {
		t.Fatal(err)
	}
	if revoked, err := fixture.repository.Revoke(ctx, fixture.appA, approvalOne.ID, fixture.actor); err != nil || revoked.RevokedAt.IsZero() {
		t.Fatalf("revoked = %#v, %v", revoked, err)
	}
}

func TestGatePersistsRejectedFindingsAndTerminalizes(t *testing.T) {
	fixture := newRepositoryFixture(t)
	guarded := newGuardedRepositoryFixture(t, fixture)
	deployment, claim := guarded.deployment(t)
	approval := finding("privileged", "service:web", "approval_required")
	rejected := finding("remote_build_context", "service:web:https://example.invalid/repo.git", "rejected")
	if err := guarded.gate(deployment, claim, []Finding{approval, rejected}); !errors.Is(err, ErrRejectedCapability) {
		t.Fatalf("rejected gate = %v", err)
	}
	persisted, err := fixture.repository.Get(context.Background(), fixture.appA, deployment.ID)
	if err != nil || persisted.Status != Failed || persisted.DiagnosticCode != "policy_rejected" || len(persisted.Findings) != 2 {
		t.Fatalf("rejected deployment = %#v, %v", persisted, err)
	}
}

func TestGateRequiresGuardDatabaseAndExactLiveScope(t *testing.T) {
	base := newRepositoryFixture(t)
	guarded := newGuardedRepositoryFixture(t, base)
	deployment, claim := guarded.deployment(t)
	before, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := base.repository.Gate(context.Background(), GateRequest{Claim: claim, DeploymentID: deployment.ID}); !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("unguarded Gate=%v", err)
	}
	if repository, err := NewWithExecutionGuard(base.repository.db, nil); repository != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("nil guard repository=%v error=%v", repository, err)
	}
	foreignBase := newRepositoryFixture(t)
	if repository, err := NewWithExecutionGuard(foreignBase.repository.db, guarded.guard); repository != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("foreign guard repository=%v error=%v", repository, err)
	}
	if guarded.repository.UsesDatabase(foreignBase.repository.db) || !guarded.repository.UsesDatabase(base.repository.db) || (*Repository)(nil).UsesDatabase(base.repository.db) {
		t.Fatal("repository database identity was not exact and nil safe")
	}
	if err := guarded.repository.Gate(context.Background(), GateRequest{Claim: claim, DeploymentID: deployment.ID}); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("missing scope Gate=%v", err)
	}
	foreignDirectories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreignScope, release, err := deploymenteffects.AcquireScoped(context.Background(), foreignDirectories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	foreignContext := deploymenteffects.WithScope(context.Background(), foreignScope)
	if err := guarded.repository.Gate(foreignContext, GateRequest{Claim: claim, DeploymentID: deployment.ID}); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		_ = release()
		t.Fatalf("foreign scope Gate=%v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refusals changed deployment: before=%#v after=%#v error=%v", before, after, err)
	}
}

func TestGateRefusesLostAttemptAndCancellingJobWithoutPolicyWrites(t *testing.T) {
	for _, test := range []struct {
		name, status, phase string
		attempt             int
	}{
		{name: "lost attempt", status: "running", phase: "running", attempt: 2},
		{name: "cancelling", status: "waiting_external", phase: "cancelling", attempt: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newRepositoryFixture(t)
			guarded := newGuardedRepositoryFixture(t, base)
			deployment, claim := guarded.deployment(t)
			before, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := base.repository.db.Exec(`UPDATE jobs SET status=?,phase=?,attempt=? WHERE id=?`, test.status, test.phase, test.attempt, claim.JobID()); err != nil {
				t.Fatal(err)
			}
			if err := guarded.gate(deployment, claim, []Finding{finding("privileged", "service:web", "approval_required")}); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("Gate=%v", err)
			}
			after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("authority refusal changed deployment: before=%#v after=%#v error=%v", before, after, err)
			}
		})
	}
}

func TestGateRequiresExactCompleteFindingSetAcrossRetryAndRecoveryStages(t *testing.T) {
	for _, stage := range []Status{Preparing, NeedsAttention, Applying, WaitingHealth} {
		t.Run(string(stage), func(t *testing.T) {
			base := newRepositoryFixture(t)
			guarded := newGuardedRepositoryFixture(t, base)
			deployment, claim := guarded.deployment(t)
			one := finding("privileged", "service:web", "approval_required")
			two := finding("external_bind", "service:web:/srv/data:/data", "allowed")
			if err := guarded.gate(deployment, claim, []Finding{one, two}); !errors.Is(err, ErrApprovalRequired) {
				t.Fatalf("seed Gate=%v", err)
			}
			if _, _, err := guarded.repository.Grant(context.Background(), guarded.appID, guarded.actorID, one.Fingerprint); err != nil {
				t.Fatal(err)
			}
			switch stage {
			case Preparing:
				if _, err := base.repository.db.Exec(`UPDATE deployments SET status='preparing',diagnostic_code=NULL,failure_code=NULL,failure_summary=NULL WHERE id=?`, deployment.ID); err != nil {
					t.Fatal(err)
				}
			case NeedsAttention:
			case Applying, WaitingHealth:
				if err := guarded.gate(deployment, claim, []Finding{two, one}); err != nil {
					t.Fatal(err)
				}
				if stage == WaitingHealth {
					if _, err := guarded.repository.Transition(context.Background(), guarded.appID, deployment.ID, WaitingHealth, ""); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
			if err != nil {
				t.Fatal(err)
			}
			changed := one
			changed.Disposition = "allowed"
			for name, supplied := range map[string][]Finding{
				"empty":               nil,
				"subset":              {one},
				"superset":            {one, two, finding("host_network", "service:worker", "allowed")},
				"changed disposition": {changed, two},
			} {
				t.Run(name, func(t *testing.T) {
					if err := guarded.gate(deployment, claim, supplied); !errors.Is(err, ErrInvalidDeployment) {
						t.Fatalf("Gate=%v", err)
					}
					after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("mismatch changed deployment: before=%#v after=%#v error=%v", before, after, err)
					}
				})
			}
			if err := guarded.gate(deployment, claim, []Finding{two, one}); err != nil {
				t.Fatalf("matching reordered Gate=%v", err)
			}
			after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stage == Applying || stage == WaitingHealth {
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("recovered Gate wrote state: before=%#v after=%#v", before, after)
				}
			} else if after.Status != Applying || len(after.Findings) != 2 {
				t.Fatalf("retry result=%#v", after)
			}
		})
	}
}

func TestGateRecoveredPolicyRefusalsAreReadOnly(t *testing.T) {
	for _, disposition := range []string{"approval_required", "rejected"} {
		for _, stage := range []Status{Applying, WaitingHealth} {
			t.Run(disposition+"/"+string(stage), func(t *testing.T) {
				base := newRepositoryFixture(t)
				guarded := newGuardedRepositoryFixture(t, base)
				deployment, claim := guarded.deployment(t)
				value := finding("privileged", "service:web", disposition)
				stamp := time.Now().UTC().Format(time.RFC3339Nano)
				if _, err := base.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), deployment.ID, value.PolicyVersion, value.Capability, value.Scope, value.Fingerprint, value.Disposition, stamp); err != nil {
					t.Fatal(err)
				}
				if _, err := base.repository.db.Exec(`UPDATE deployments SET status=?,started_at=? WHERE id=?`, stage, stamp, deployment.ID); err != nil {
					t.Fatal(err)
				}
				before := guarded.snapshot(t, deployment, claim)
				want := ErrApprovalRequired
				if disposition == "rejected" {
					want = ErrRejectedCapability
					changed := value
					changed.Disposition = "allowed"
					if err := guarded.gate(deployment, claim, []Finding{changed}); !errors.Is(err, ErrInvalidDeployment) {
						t.Fatalf("mismatched rejected evidence Gate=%v", err)
					}
					if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
						t.Fatalf("rejected mismatch wrote state: before=%#v after=%#v", before, after)
					}
				}
				if err := guarded.gate(deployment, claim, []Finding{value}); !errors.Is(err, want) {
					t.Fatalf("Gate=%v", err)
				}
				if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
					t.Fatalf("recovered refusal wrote state: before=%#v after=%#v", before, after)
				}
			})
		}
	}
}

func TestGateRecoveredEmptyAndMalformedEvidenceRefuseWithoutWrites(t *testing.T) {
	t.Run("empty stored set versus nonempty request", func(t *testing.T) {
		base := newRepositoryFixture(t)
		guarded := newGuardedRepositoryFixture(t, base)
		deployment, claim := guarded.deployment(t)
		if err := guarded.gate(deployment, claim, nil); err != nil {
			t.Fatal(err)
		}
		before := guarded.snapshot(t, deployment, claim)
		if err := guarded.gate(deployment, claim, []Finding{finding("external_bind", "service:web:/data", "allowed")}); !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("Gate=%v", err)
		}
		if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
			t.Fatalf("empty mismatch wrote state: before=%#v after=%#v", before, after)
		}
	})

	for _, test := range []struct {
		name     string
		findings []Finding
	}{
		{name: "malformed", findings: []Finding{{PolicyVersion: "compose-runtime-v1", Capability: "privileged", Scope: "service:web", Fingerprint: strings.Repeat("0", 64), Disposition: "allowed"}}},
		{name: "duplicate", findings: func() []Finding {
			value := finding("privileged", "service:web", "allowed")
			return []Finding{value, value}
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newRepositoryFixture(t)
			guarded := newGuardedRepositoryFixture(t, base)
			deployment, claim := guarded.deployment(t)
			before := guarded.snapshot(t, deployment, claim)
			if err := guarded.gate(deployment, claim, test.findings); !errors.Is(err, ErrInvalidDeployment) {
				t.Fatalf("Gate=%v", err)
			}
			if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid request wrote state: before=%#v after=%#v", before, after)
			}
		})
	}

	for _, cancelAfterRead := range []bool{false, true} {
		name := "malformed retained evidence"
		if cancelAfterRead {
			name += " with concurrent cancellation"
		}
		t.Run(name, func(t *testing.T) {
			base := newRepositoryFixture(t)
			guarded := newGuardedRepositoryFixture(t, base)
			deployment, claim := guarded.deployment(t)
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := base.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), deployment.ID, "compose-runtime-v1", "privileged", "service:web", strings.Repeat("0", 64), "allowed", stamp); err != nil {
				t.Fatal(err)
			}
			if cancelAfterRead {
				ctx, cancel := context.WithCancel(guarded.context)
				guarded.context = ctx
				guarded.repository.afterGatePolicyRead = cancel
			}
			before := guarded.snapshot(t, deployment, claim)
			err := guarded.gate(deployment, claim, nil)
			if !errors.Is(err, ErrInvalidDeployment) || errors.Is(err, deploymentexecution.ErrUnavailable) {
				t.Fatalf("Gate=%v", err)
			}
			if cancelAfterRead && (!errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancellation cause missing: %v", err)
			}
			if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
				t.Fatalf("malformed retained evidence wrote state: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestGateRecoveredApprovalsRequireEveryExactActiveTuple(t *testing.T) {
	for _, mutation := range []string{"revoked", "foreign app", "wrong version", "wrong capability", "wrong scope", "one of two missing"} {
		t.Run(mutation, func(t *testing.T) {
			base := newRepositoryFixture(t)
			guarded := newGuardedRepositoryFixture(t, base)
			deployment, claim := guarded.deployment(t)
			one := finding("privileged", "service:web", "approval_required")
			two := finding("host_network", "service:worker", "approval_required")
			if err := guarded.gate(deployment, claim, []Finding{one, two}); !errors.Is(err, ErrApprovalRequired) {
				t.Fatal(err)
			}
			if _, _, err := guarded.repository.Grant(context.Background(), guarded.appID, guarded.actorID, two.Fingerprint); err != nil {
				t.Fatal(err)
			}
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			var err error
			switch mutation {
			case "revoked":
				first, _, grantErr := guarded.repository.Grant(context.Background(), guarded.appID, guarded.actorID, one.Fingerprint)
				if grantErr != nil {
					t.Fatal(grantErr)
				}
				_, err = guarded.repository.Revoke(context.Background(), guarded.appID, first.ID, guarded.actorID)
			case "foreign app":
				_, err = base.repository.db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), base.appB, one.PolicyVersion, one.Capability, one.Scope, one.Fingerprint, guarded.actorID, stamp)
			case "wrong version":
				_, err = base.repository.db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), guarded.appID, "compose-runtime-v0", one.Capability, one.Scope, one.Fingerprint, guarded.actorID, stamp)
			case "wrong capability":
				_, err = base.repository.db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), guarded.appID, one.PolicyVersion, "different", one.Scope, one.Fingerprint, guarded.actorID, stamp)
			case "wrong scope":
				_, err = base.repository.db.Exec(`INSERT INTO runtime_approvals(id,app_id,policy_version,capability,scope,fingerprint,granted_by,granted_at) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), guarded.appID, one.PolicyVersion, one.Capability, "service:different", one.Fingerprint, guarded.actorID, stamp)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := base.repository.db.Exec(`UPDATE deployments SET status='applying',started_at=? WHERE id=?`, stamp, deployment.ID); err != nil {
				t.Fatal(err)
			}
			before := guarded.snapshot(t, deployment, claim)
			if err := guarded.gate(deployment, claim, []Finding{one, two}); !errors.Is(err, ErrApprovalRequired) {
				t.Fatalf("Gate=%v", err)
			}
			if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
				t.Fatalf("approval refusal wrote state: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestGateCancellationAfterPolicyReadAndCommitFailureRollback(t *testing.T) {
	t.Run("transaction begin failure", func(t *testing.T) {
		base := newRepositoryFixture(t)
		guarded := newGuardedRepositoryFixture(t, base)
		deployment, claim := guarded.deployment(t)
		before := guarded.snapshot(t, deployment, claim)
		if err := base.repository.db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := guarded.gate(deployment, claim, nil); !errors.Is(err, deploymentexecution.ErrUnavailable) {
			t.Fatalf("Gate=%v", err)
		}
		reopened, err := database.Open(base.dataRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		reader := *guarded
		reader.repository = New(reopened)
		if after := reader.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
			t.Fatalf("begin failure changed state: before=%#v after=%#v", before, after)
		}
	})

	t.Run("cancellation before refusal classification", func(t *testing.T) {
		base := newRepositoryFixture(t)
		guarded := newGuardedRepositoryFixture(t, base)
		deployment, claim := guarded.deployment(t)
		ctx, cancel := context.WithCancel(guarded.context)
		guarded.repository.afterGatePolicyRead = cancel
		before, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = guarded.repository.Gate(ctx, GateRequest{Claim: claim, DeploymentID: deployment.ID, Findings: []Finding{finding("privileged", "service:web", "rejected")}})
		if !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Gate=%v", err)
		}
		after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("cancellation changed deployment: before=%#v after=%#v error=%v", before, after, err)
		}
	})

	t.Run("finding insert failure rolls back", func(t *testing.T) {
		base := newRepositoryFixture(t)
		guarded := newGuardedRepositoryFixture(t, base)
		deployment, claim := guarded.deployment(t)
		if _, err := base.repository.db.Exec(`CREATE TRIGGER gate_insert_fault BEFORE INSERT ON deployment_policy_findings BEGIN SELECT RAISE(ABORT,'gate insert fault'); END`); err != nil {
			t.Fatal(err)
		}
		before, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = guarded.gate(deployment, claim, []Finding{finding("external_bind", "service:web:/data", "allowed")})
		if !errors.Is(err, deploymentexecution.ErrUnavailable) {
			t.Fatalf("Gate=%v", err)
		}
		after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("insert failure changed deployment: before=%#v after=%#v error=%v", before, after, err)
		}
	})

	t.Run("deployment update failure rolls back findings", func(t *testing.T) {
		base := newRepositoryFixture(t)
		guarded := newGuardedRepositoryFixture(t, base)
		deployment, claim := guarded.deployment(t)
		if _, err := base.repository.db.Exec(`CREATE TRIGGER gate_update_fault BEFORE UPDATE OF status ON deployments WHEN OLD.id=NEW.id BEGIN SELECT RAISE(ABORT,'gate update fault'); END`); err != nil {
			t.Fatal(err)
		}
		before := guarded.snapshot(t, deployment, claim)
		err := guarded.gate(deployment, claim, []Finding{finding("external_bind", "service:web:/data", "allowed")})
		if !errors.Is(err, deploymentexecution.ErrUnavailable) {
			t.Fatalf("Gate=%v", err)
		}
		if after := guarded.snapshot(t, deployment, claim); !reflect.DeepEqual(before, after) {
			t.Fatalf("update failure changed deployment: before=%#v after=%#v", before, after)
		}
	})

	t.Run("deferred foreign key fails commit", func(t *testing.T) {
		base := newRepositoryFixture(t)
		guarded := newGuardedRepositoryFixture(t, base)
		deployment, claim := guarded.deployment(t)
		if _, err := base.repository.db.Exec(`CREATE TABLE gate_commit_fault(id TEXT PRIMARY KEY, missing_app TEXT NOT NULL, FOREIGN KEY(missing_app) REFERENCES applications(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
			t.Fatal(err)
		}
		if _, err := base.repository.db.Exec(`CREATE TRIGGER gate_commit_fault_insert AFTER INSERT ON deployment_policy_findings BEGIN INSERT INTO gate_commit_fault(id,missing_app) VALUES(NEW.id,'missing-app'); END`); err != nil {
			t.Fatal(err)
		}
		before, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = guarded.gate(deployment, claim, []Finding{finding("external_bind", "service:web:/data", "allowed")})
		if !errors.Is(err, deploymentexecution.ErrUnavailable) {
			t.Fatalf("Gate=%v", err)
		}
		after, err := base.repository.Get(context.Background(), base.appA, deployment.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("commit failure changed deployment: before=%#v after=%#v error=%v", before, after, err)
		}
		var faultRows int
		if err := base.repository.db.QueryRow(`SELECT COUNT(*) FROM gate_commit_fault`).Scan(&faultRows); err != nil || faultRows != 0 {
			t.Fatalf("commit fault rows=%d error=%v", faultRows, err)
		}
	})
}

func TestSourceFailureDiagnosticTaxonomyIsSanitized(t *testing.T) {
	for _, code := range []string{"invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full", "provider_unavailable", "configuration_unavailable", "configuration_review_required", "build_configuration_requires_build_command", "gateway_readiness_failed"} {
		fixture := newRepositoryFixture(t)
		deployment := fixture.localDeployment(t)
		failed, err := fixture.repository.Transition(context.Background(), fixture.appA, deployment.ID, Failed, code)
		if err != nil {
			t.Fatalf("code %q: %v", code, err)
		}
		if failed.DiagnosticCode != code || failed.FailureSummary == "" {
			t.Fatalf("code %q persisted %#v", code, failed)
		}
	}
}

func TestTransitionsAndRecoveryUseExplicitAllowlistAndPreserveNeedsAttention(t *testing.T) {
	fixture := newRepositoryFixture(t)
	guarded := newGuardedRepositoryFixture(t, fixture)
	ctx := context.Background()
	preparing, preparingClaim := guarded.deployment(t)
	if _, err := fixture.repository.Transition(ctx, fixture.appA, preparing.ID, Succeeded, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("preparing to succeeded = %v", err)
	}
	if err := guarded.gate(preparing, preparingClaim, []Finding{finding("privileged", "service:web", "approval_required")}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal(err)
	}
	if _, err := fixture.repository.db.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled' WHERE id=?`, preparingClaim.JobID()); err != nil {
		t.Fatal(err)
	}
	applying, applyingClaim := guarded.deployment(t)
	if err := guarded.gate(applying, applyingClaim, nil); err != nil {
		t.Fatal(err)
	}
	beforePreparing, err := fixture.repository.Get(ctx, fixture.appA, preparing.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeApplying, err := fixture.repository.Get(ctx, fixture.appA, applying.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.Recover(ctx); !errors.Is(err, generatedrecovery.ErrGuardedRecoveryRequired) {
		t.Fatalf("standalone recovery error=%v", err)
	}
	afterPreparing, err := fixture.repository.Get(ctx, fixture.appA, preparing.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterApplying, err := fixture.repository.Get(ctx, fixture.appA, applying.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforePreparing, afterPreparing) || !reflect.DeepEqual(beforeApplying, afterApplying) {
		t.Fatal("standalone recovery refusal changed deployment evidence")
	}
	if _, err := generatedrecovery.RecoverRuntime(ctx, fixture.repository.db, fixture.repository.now().UTC(), guarded.bindings); err != nil {
		t.Fatal(err)
	}
	recoveredPreparing, err := fixture.repository.Get(ctx, fixture.appA, preparing.ID)
	if err != nil || recoveredPreparing.Status != NeedsAttention {
		t.Fatalf("needs attention recovery = %#v, %v", recoveredPreparing, err)
	}
	recoveredApplying, err := fixture.repository.Get(ctx, fixture.appA, applying.ID)
	if err != nil || recoveredApplying.Status != Failed || recoveredApplying.DiagnosticCode != "daemon_restarted" {
		t.Fatalf("applying recovery = %#v, %v", recoveredApplying, err)
	}
	if _, err := fixture.repository.Transition(ctx, fixture.appA, applying.ID, Succeeded, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal transition = %v", err)
	}
}

func TestReleaseHistoryPreservesCredentialFreeProvenanceAndLegacyDeployments(t *testing.T) {
	fixture := newRepositoryFixture(t)
	ctx := context.Background()
	releaseID := strings.Repeat("a", 32)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := fixture.repository.db.Exec(`INSERT INTO releases(id,app_id,source_commit_sha,source_branch,status,metadata_json,created_at,source_provider,repository_id,repository_owner,repository_name,tracked_ref,resolved_sha,compose_path,archive_sha256,workspace_tree_sha256,workspace_state) VALUES(?,?,?,?, 'ready','{}',?,'github',?,?,?,?,?,?,?,?,'ready')`, releaseID, fixture.appA, strings.Repeat("b", 40), "main", created, 42, "owner", "repository", "refs/heads/main", strings.Repeat("b", 40), "compose.yaml", strings.Repeat("c", 64), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	release, err := fixture.repository.Release(ctx, fixture.appA, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	if release.RepositoryID != 42 || release.RepositoryOwner != "owner" || release.RepositoryName != "repository" || release.TrackedRef != "refs/heads/main" || release.ResolvedSHA != strings.Repeat("b", 40) || release.ArchiveSHA256 != strings.Repeat("c", 64) || release.ComposePath != "compose.yaml" {
		t.Fatalf("release provenance = %#v", release)
	}
	if _, err := fixture.repository.Release(ctx, fixture.appB, releaseID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-app release lookup = %v", err)
	}

	legacyID := uuid.NewString()
	if _, err := fixture.repository.db.Exec(`INSERT INTO deployments(id,app_id,status,started_at,finished_at) VALUES(?,?,'succeeded',?,?)`, legacyID, fixture.appA, created, created); err != nil {
		t.Fatal(err)
	}
	history, err := fixture.repository.List(ctx, fixture.appA, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, deployment := range history {
		if deployment.ID == legacyID {
			found = true
			if deployment.JobID != "" {
				t.Fatalf("legacy deployment job ID = %q", deployment.JobID)
			}
		}
	}
	if !found {
		t.Fatal("legacy deployment with a null job linkage was omitted from history")
	}
}
