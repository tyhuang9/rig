package deployments

import (
	"context"
	"errors"
	"path/filepath"
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
