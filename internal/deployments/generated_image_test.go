package deployments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedimagestate"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

func generatedImageRequest(fixture *generatedNetworkFixture) GeneratedImageRequest {
	return GeneratedImageRequest{
		Claim: fixture.claim, DeploymentID: fixture.deployment.ID, ReleaseID: fixture.request.ReleaseID,
		ConfigurationRevisionID: fixture.request.ConfigurationRevisionID, ConfigurationRevisionNumber: 1,
		DeploymentPlanRevisionID: fixture.request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: 1,
		ComponentName: "api", CompilerVersion: "generated-node/v1", BuildDefinitionDigest: strings.Repeat("a", 64),
		PlanCanonicalDigest: strings.Repeat("3", 64), SourceProvider: "local", SourceRepositoryID: 0,
		SourceResolvedDigest: strings.Repeat("4", 64), PlanSourceProvider: "local", PlanSourceRepositoryID: 0,
		PlanSourceResolvedDigest: strings.Repeat("2", 64), WorkspaceTreeSHA256: strings.Repeat("5", 64),
	}
}

func TestGeneratedImageAuthoritySelectsAndCommitsExactReadyAttachment(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	request := generatedImageRequest(fixture)
	execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
	if err != nil {
		t.Fatal(err)
	}
	artifact, created, err := execution.SelectArtifact()
	if err != nil || !created || artifact.State != generatedimagestate.ArtifactBuilding {
		t.Fatalf("select=%#v created=%t err=%v", artifact, created, err)
	}
	selection, err := execution.BuildSelection()
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.guarded.repository.PinGeneratedImageCommand(selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.CheckBeforeBuildLoad(); err != nil {
		t.Fatal(err)
	}
	command.Leave()
	imageID := "sha256:" + strings.Repeat("b", 64)
	if err := execution.RecordBuiltImageReady(imageID); err != nil {
		t.Fatal(err)
	}
	execution.Leave()
	persisted, err := generatedimagestate.NewRepository(fixture.guarded.repository.db).Get(context.Background(), artifact.ID)
	if err != nil || persisted.State != generatedimagestate.ArtifactReady || persisted.ImageContentID != imageID {
		t.Fatalf("artifact=%#v err=%v", persisted, err)
	}
	component, err := fixture.state.Component(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID, "api")
	if err != nil || component.State != generatedruntimestate.ComponentImageReady || component.ImageArtifactID != artifact.ID {
		t.Fatalf("component=%#v err=%v", component, err)
	}
	observed, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Leave()
	reused, err := observed.ObservePersistedImage()
	if err != nil || reused.ID != artifact.ID {
		t.Fatalf("observed=%#v err=%v", reused, err)
	}
	if err := observed.RecordReusedImageReady(); err != nil {
		t.Fatalf("verify persisted attachment: %v", err)
	}
}

func TestGeneratedImageAuthorityExistingBuildingCannotMintBuildSelection(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	request := generatedImageRequest(fixture)
	first, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
	if err != nil {
		t.Fatal(err)
	}
	original, created, err := first.SelectArtifact()
	if err != nil || !created {
		t.Fatalf("first=%#v created=%t err=%v", original, created, err)
	}
	first.Leave()
	resumed, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Leave()
	existing, created, err := resumed.SelectArtifact()
	if err != nil || created || existing.ID != original.ID {
		t.Fatalf("existing=%#v created=%t err=%v", existing, created, err)
	}
	if _, err := resumed.BuildSelection(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("existing building minted selection: %v", err)
	}
	if _, err := fixture.guarded.repository.PinGeneratedImageCommand(GeneratedImageBuildSelection{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("zero selection=%v", err)
	}
	if _, err := resumed.BuildSelection(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resumed execution minted selection: %v", err)
	}
}

func TestGeneratedImageBuildSelectionRetainsOriginalRepositoryContextAndLifetime(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	parent, cancel := context.WithCancel(context.Background())
	ctx := deploymenteffects.WithScope(parent, fixture.guarded.scope)
	execution, err := fixture.guarded.repository.PinGeneratedImage(ctx, generatedImageRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := execution.SelectArtifact(); err != nil {
		t.Fatal(err)
	}
	selection, err := execution.BuildSelection()
	if err != nil {
		t.Fatal(err)
	}
	foreign := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	if _, err := foreign.guarded.repository.PinGeneratedImageCommand(selection); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("foreign repository accepted selection: %v", err)
	}
	cancel()
	if _, err := fixture.guarded.repository.PinGeneratedImageCommand(selection); !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("replacement context escaped original cancellation: %v", err)
	}
	execution.Leave()
	if _, err := execution.BuildSelection(); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("selection survived leave: %v", err)
	}
}

func TestGeneratedImageResultTransactionsReuseReplaceAndRollbackAtomically(t *testing.T) {
	t.Run("reuse", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := generatedImageRequest(fixture)
		store := generatedimagestate.NewRepository(fixture.guarded.repository.db)
		artifact, created, err := store.Begin(context.Background(), generatedimagestate.BeginArtifactInput{ReleaseID: request.ReleaseID, DeploymentPlanRevisionID: request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: 1, ComponentID: request.ComponentName, CompilerVersion: request.CompilerVersion, BuildDefinitionDigest: request.BuildDefinitionDigest})
		if err != nil || !created {
			t.Fatal(err)
		}
		artifact, err = store.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("c", 64))
		if err != nil {
			t.Fatal(err)
		}
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		selected, created, err := execution.SelectArtifact()
		if err != nil || created || selected.ID != artifact.ID {
			t.Fatalf("selected=%#v created=%t err=%v", selected, created, err)
		}
		if err := execution.RecordReusedImageReady(); err != nil {
			t.Fatal(err)
		}
		component, err := fixture.state.Component(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID, "api")
		if err != nil || component.ImageArtifactID != artifact.ID {
			t.Fatalf("component=%#v err=%v", component, err)
		}
	})
	t.Run("bounded replacement", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := generatedImageRequest(fixture)
		store := generatedimagestate.NewRepository(fixture.guarded.repository.db)
		old, _, _ := store.Begin(context.Background(), generatedimagestate.BeginArtifactInput{ReleaseID: request.ReleaseID, DeploymentPlanRevisionID: request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: 1, ComponentID: request.ComponentName, CompilerVersion: request.CompilerVersion, BuildDefinitionDigest: request.BuildDefinitionDigest})
		old, _ = store.Complete(context.Background(), old.ID, "sha256:"+strings.Repeat("d", 64))
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, _, err = execution.SelectArtifact(); err != nil {
			t.Fatal(err)
		}
		replacement, err := execution.ReplaceMissingReusableArtifact()
		if err != nil || replacement.AttemptNumber != old.AttemptNumber+1 {
			t.Fatalf("replacement=%#v err=%v", replacement, err)
		}
		if _, err = execution.ReplaceMissingReusableArtifact(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("second replacement=%v", err)
		}
		old, _ = store.Get(context.Background(), old.ID)
		if old.State != generatedimagestate.ArtifactUnavailable {
			t.Fatalf("old=%#v", old)
		}
	})
	t.Run("component update rolls back completion", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := generatedImageRequest(fixture)
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		artifact, _, err := execution.SelectArtifact()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.guarded.repository.db.Exec(`CREATE TRIGGER generated_image_result_fault BEFORE UPDATE ON generated_runtime_components BEGIN SELECT RAISE(ABORT,'result fault'); END`); err != nil {
			t.Fatal(err)
		}
		if err := execution.RecordBuiltImageReady("sha256:" + strings.Repeat("e", 64)); err == nil {
			t.Fatal("result fault accepted")
		}
		persisted, err := generatedimagestate.NewRepository(fixture.guarded.repository.db).Get(context.Background(), artifact.ID)
		if err != nil || persisted.State != generatedimagestate.ArtifactBuilding {
			t.Fatalf("persisted=%#v err=%v", persisted, err)
		}
		component, err := fixture.state.Component(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID, "api")
		if err != nil || component.State != generatedruntimestate.ComponentPending {
			t.Fatalf("component=%#v err=%v", component, err)
		}
		selected, _ := execution.SelectedArtifact()
		if selected.State != generatedimagestate.ArtifactBuilding || selected.ImageContentID != "" {
			t.Fatalf("failed commit published selection=%#v", selected)
		}
	})
}

func TestGeneratedImageAuthorityRefusesWrongPhasePolicyAndAttempt(t *testing.T) {
	t.Run("wrong phase", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseStartingCandidate, false)
		request := generatedImageRequest(fixture)
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, _, err := execution.SelectArtifact(); !errors.Is(err, deploymentexecution.ErrStageInvalid) {
			t.Fatalf("wrong phase=%v", err)
		}
	})
	t.Run("wrong attempt", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := generatedImageRequest(fixture)
		claim, err := deploymentexecution.NewClaim(fixture.binding, fixture.claim.JobID(), fixture.claim.ApplicationID(), 2)
		if err != nil {
			t.Fatal(err)
		}
		request.Claim = claim
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, _, err := execution.SelectArtifact(); !errors.Is(err, deploymentexecution.ErrIdentityLost) {
			t.Fatalf("wrong attempt=%v", err)
		}
	})
	t.Run("policy addition", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := generatedImageRequest(fixture)
		if _, err := fixture.guarded.repository.db.Exec(`INSERT INTO deployment_policy_findings(id,deployment_id,policy_version,capability,scope,fingerprint,disposition,created_at) VALUES(lower(hex(randomblob(16))),?,'v1','network','app',?,'allowed',datetime('now'))`, fixture.deployment.ID, findingFingerprint("v1", "network", "app")); err != nil {
			t.Fatal(err)
		}
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, _, err := execution.SelectArtifact(); !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("policy drift=%v", err)
		}
	})
	t.Run("candidate risk in sibling", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		request := generatedImageRequest(fixture)
		stamp := "2026-10-18T00:00:00Z"
		if _, err := fixture.guarded.repository.db.Exec(`INSERT INTO generated_runtime_components(deployment_id,component_name,slot,state,created_at,updated_at) SELECT deployment_id,'worker',candidate_slot,'pending',?,? FROM generated_runtime_deployments WHERE deployment_id=?`, stamp, stamp, fixture.deployment.ID); err != nil {
			t.Fatal(err)
		}
		store := generatedimagestate.NewRepository(fixture.guarded.repository.db)
		artifact, _, err := store.Begin(context.Background(), generatedimagestate.BeginArtifactInput{ReleaseID: request.ReleaseID, DeploymentPlanRevisionID: request.DeploymentPlanRevisionID, DeploymentPlanRevisionNumber: 1, ComponentID: "worker", CompilerVersion: request.CompilerVersion, BuildDefinitionDigest: strings.Repeat("b", 64)})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err = store.Complete(context.Background(), artifact.ID, "sha256:"+strings.Repeat("1", 64))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.state.SetImageReady(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID, "worker", artifact.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.state.SetContainerStarting(context.Background(), fixture.claim.ApplicationID(), fixture.deployment.ID, "worker", "rig-worker"); err != nil {
			t.Fatal(err)
		}
		execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, _, err := execution.SelectArtifact(); !errors.Is(err, ErrInvalidDeployment) {
			t.Fatalf("sibling candidate risk accepted: %v", err)
		}
	})
	t.Run("semantic refusal retains cancellation", func(t *testing.T) {
		fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
		parent, cancel := context.WithCancel(context.Background())
		ctx := deploymenteffects.WithScope(parent, fixture.guarded.scope)
		request := generatedImageRequest(fixture)
		request.WorkspaceTreeSHA256 = strings.Repeat("9", 64)
		fixture.guarded.repository.afterGeneratedImageIdentityRead = cancel
		defer func() { fixture.guarded.repository.afterGeneratedImageIdentityRead = nil }()
		execution, err := fixture.guarded.repository.PinGeneratedImage(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		defer execution.Leave()
		if _, _, err := execution.SelectArtifact(); !errors.Is(err, ErrInvalidDeployment) || !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("semantic cancellation causes=%v", err)
		}
	})
}

func TestGeneratedImagePreparationDerivesNextPhaseFromPersistedMigration(t *testing.T) {
	fixture := newGeneratedNetworkFixture(t, generatedruntimestate.PhaseBuilding, false)
	request := generatedImageRequest(fixture)
	execution, err := fixture.guarded.repository.PinGeneratedImage(fixture.guarded.context, request)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = execution.SelectArtifact()
	if err != nil {
		t.Fatal(err)
	}
	if err = execution.RecordBuiltImageReady("sha256:" + strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	execution.Leave()
	next, err := fixture.guarded.repository.FinishGeneratedImagePreparation(fixture.guarded.context, fixture.request)
	if err != nil || next != generatedruntimestate.PhaseStartingCandidate {
		t.Fatalf("next=%s err=%v", next, err)
	}
}
