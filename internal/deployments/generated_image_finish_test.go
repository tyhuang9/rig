package deployments

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func prepareGeneratedImageFinish(t *testing.T, migration bool) (*generatedNetworkFixture, generatedimagestate.Artifact) {
	t.Helper()
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, migration)
	execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, generatedImageRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Leave()
	artifact, created, err := execution.SelectArtifact()
	if err != nil || !created {
		t.Fatalf("select image: created=%t error=%v", created, err)
	}
	if err := execution.RecordBuiltImageReady("sha256:" + strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}
	artifact, err = generatedimagestate.NewRepository(fixture.guarded.repository.db).Get(context.Background(), artifact.ID)
	if err != nil || artifact.State != generatedimagestate.ArtifactReady {
		t.Fatalf("ready image=%#v error=%v", artifact, err)
	}
	return fixture, artifact
}

func TestGeneratedImageFinishChoosesApprovedPendingMigration(t *testing.T) {
	fixture, artifact := prepareGeneratedImageFinish(t, true)
	next, err := fixture.guarded.repository.FinishGeneratedImagePreparation(fixture.guarded.context, fixture.request)
	if err != nil || next != generatedruntimestate.PhaseMigrating {
		t.Fatalf("finish next=%s error=%v", next, err)
	}
	persisted, err := fixture.state.Get(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID)
	if err != nil || persisted.Phase != generatedruntimestate.PhaseMigrating || persisted.MigrationState != generatedruntimestate.MigrationPending || !persisted.MigrationStartedAt.IsZero() || !persisted.MigrationFinishedAt.IsZero() {
		t.Fatalf("handoff executed or skipped the pending migration: state=%#v error=%v", persisted, err)
	}
	after, err := generatedimagestate.NewRepository(fixture.guarded.repository.db).Get(context.Background(), artifact.ID)
	if err != nil || !reflect.DeepEqual(after, artifact) {
		t.Fatalf("handoff changed immutable image: image=%#v error=%v", after, err)
	}
}

func TestGeneratedImageFinishRefusesChangedAuthorityAndIncompleteAggregate(t *testing.T) {
	for _, test := range []struct {
		name      string
		migration bool
		cause     error
		mutate    func(*testing.T, *generatedNetworkFixture)
	}{
		{name: "pending sibling", cause: generatedruntimestate.ErrInvalidTransition, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			addGeneratedImageFinishSibling(t, f, false)
		}},
		{name: "extra ready sibling beyond accepted plan", cause: generatedruntimestate.ErrInvalidTransition, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			addGeneratedImageFinishSibling(t, f, true)
		}},
		{name: "policy added after readiness", cause: ErrInvalidDeployment, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			if _, err := f.guarded.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(lower(hex(randomblob(16))),?,'v1','network','app',?,'allowed','2026-10-19T00:00:00Z')`, f.deployment.ID, findingFingerprint("v1", "network", "app")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "migration approval revoked after readiness", migration: true, cause: ErrApprovalRequired, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			if _, err := f.guarded.repository.db.Exec(`DELETE FROM deployment_plan_migration_approvals WHERE revision_id=? AND app_id=?`, f.request.DeploymentPlanRevisionID, f.claim.ApplicationID()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "worker attempt advanced after readiness", cause: deploymentexecution.ErrIdentityLost, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			if _, err := f.guarded.repository.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`, f.claim.JobID()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong runtime phase", cause: deploymentexecution.ErrStageInvalid, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			if _, err := f.state.Advance(context.Background(), f.claim.ApplicationID(), f.deployment.ID, generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseStartingCandidate, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "predecessor changed after readiness", cause: deploymentexecution.ErrAuthorityStale, mutate: func(t *testing.T, f *generatedNetworkFixture) {
			// Corrupt only this disposable fixture DB to exercise the reader's
			// independent refusal; this is not a legal production state writer.
			if _, err := f.guarded.repository.db.Exec(`DROP TRIGGER generated_runtime_active_head_valid_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.guarded.repository.db.Exec(`UPDATE generated_runtime_active_heads SET deployment_id=?,release_id=?,slot='blue',generation=1,updated_at='2026-10-19T00:00:00Z' WHERE app_id=?`, f.deployment.ID, f.request.ReleaseID, f.claim.ApplicationID()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, artifact := prepareGeneratedImageFinish(t, test.migration)
			test.mutate(t, fixture)
			before, err := fixture.state.Get(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID)
			if err != nil {
				t.Fatal(err)
			}
			next, err := fixture.guarded.repository.FinishGeneratedImagePreparation(fixture.guarded.context, fixture.request)
			if !errors.Is(err, test.cause) || next != "" {
				t.Fatalf("refused handoff next=%s error=%v, want %v", next, err, test.cause)
			}
			after, err := fixture.state.Get(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("refused handoff changed runtime: before=%#v after=%#v error=%v", before, after, err)
			}
			persisted, err := generatedimagestate.NewRepository(fixture.guarded.repository.db).Get(context.Background(), artifact.ID)
			if err != nil || !reflect.DeepEqual(persisted, artifact) {
				t.Fatalf("refused handoff changed immutable image: image=%#v error=%v", persisted, err)
			}
		})
	}
}

func addGeneratedImageFinishSibling(t *testing.T, fixture *generatedNetworkFixture, ready bool) {
	t.Helper()
	if _, err := fixture.guarded.repository.db.Exec(`INSERT INTO generated_runtime_components(deployment_id,component_name,slot,state,created_at,updated_at) SELECT deployment_id,'worker',candidate_slot,'pending','2026-10-19T00:00:00Z','2026-10-19T00:00:00Z' FROM generated_runtime_deployments WHERE deployment_id=?`, fixture.deployment.ID); err != nil {
		t.Fatal(err)
	}
	if !ready {
		return
	}
	store := generatedimagestate.NewRepository(fixture.guarded.repository.db)
	artifact, created, err := store.Begin(context.Background(), generatedimagestate.BeginArtifactInput{
		ReleaseID: fixture.request.ReleaseID, DeploymentPlanRevisionID: fixture.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: fixture.request.DeploymentPlanRevisionNumber, ComponentID: "worker",
		CompilerVersion: "generated-node/v1", BuildDefinitionDigest: strings.Repeat("8", 64),
	})
	if err != nil || !created {
		t.Fatalf("extra ready artifact: created=%t error=%v", created, err)
	}
	if _, err := store.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("8", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.SetImageReady(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID, "worker", artifact.ID); err != nil {
		t.Fatal(err)
	}
}
