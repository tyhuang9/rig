package deployments

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
)

type repositoryFixture struct {
	repository *Repository
	appA       string
	appB       string
	actor      string
	jobA       string
	jobB       string
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
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := repositoryFixture{repository: New(db), appA: uuid.NewString(), appB: uuid.NewString(), actor: uuid.NewString(), jobA: uuid.NewString(), jobB: uuid.NewString()}
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
	deployment := fixture.localDeployment(t)
	one := finding("privileged", "service:web", "approval_required")
	two := finding("external_bind", "service:web:/srv/data:/data", "approval_required")
	if err := fixture.repository.Gate(ctx, fixture.appA, deployment.ID, []Finding{two, one}); !errors.Is(err, ErrApprovalRequired) {
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
	if err := fixture.repository.Gate(ctx, fixture.appA, deployment.ID, []Finding{one, two}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("partially approved gate = %v", err)
	}
	if _, _, err := fixture.repository.Grant(ctx, fixture.appA, fixture.actor, two.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.Gate(ctx, fixture.appA, deployment.ID, []Finding{one, two}); err != nil {
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
	deployment := fixture.localDeployment(t)
	approval := finding("privileged", "service:web", "approval_required")
	rejected := finding("remote_build_context", "service:web:https://example.invalid/repo.git", "rejected")
	if err := fixture.repository.Gate(context.Background(), fixture.appA, deployment.ID, []Finding{approval, rejected}); !errors.Is(err, ErrRejectedCapability) {
		t.Fatalf("rejected gate = %v", err)
	}
	persisted, err := fixture.repository.Get(context.Background(), fixture.appA, deployment.ID)
	if err != nil || persisted.Status != Failed || persisted.DiagnosticCode != "policy_rejected" || len(persisted.Findings) != 2 {
		t.Fatalf("rejected deployment = %#v, %v", persisted, err)
	}
}

func TestSourceFailureDiagnosticTaxonomyIsSanitized(t *testing.T) {
	for _, code := range []string{"invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full", "provider_unavailable", "configuration_unavailable"} {
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
	ctx := context.Background()
	preparing, err := fixture.repository.Create(ctx, fixture.appA, fixture.jobA, "current")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Transition(ctx, fixture.appA, preparing.ID, Succeeded, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("preparing to succeeded = %v", err)
	}
	if _, err := fixture.repository.Initialize(ctx, fixture.appA, preparing.ID, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.Gate(ctx, fixture.appA, preparing.ID, []Finding{finding("privileged", "service:web", "approval_required")}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal(err)
	}
	if _, err := fixture.repository.db.Exec(`UPDATE jobs SET status='cancelled',phase='cancelled' WHERE id=?`, fixture.jobA); err != nil {
		t.Fatal(err)
	}
	jobID := uuid.NewString()
	if _, err := fixture.repository.db.Exec(`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,datetime('now'),datetime('now'))`, jobID, fixture.appA, fixture.actor); err != nil {
		t.Fatal(err)
	}
	applying, err := fixture.repository.Create(ctx, fixture.appA, jobID, "current")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Initialize(ctx, fixture.appA, applying.ID, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.Gate(ctx, fixture.appA, applying.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.Recover(ctx); err != nil {
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
