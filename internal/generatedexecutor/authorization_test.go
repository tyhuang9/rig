package generatedexecutor

import (
	"context"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type authorizationFixtureStore struct {
	deployment deployments.Deployment
	gateCalls  int
}

func (s *authorizationFixtureStore) Get(context.Context, string, string) (deployments.Deployment, error) {
	return s.deployment, nil
}
func (s *authorizationFixtureStore) Gate(context.Context, string, string, []deployments.Finding) error {
	s.gateCalls++
	return nil
}

type authorizationFixturePlans struct {
	plan deploymentplans.DeploymentPlanRevision
}

func (p authorizationFixturePlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return p.plan, nil
}

func TestRepositoryAuthorizationGateMovesExactPinnedDeploymentToApplying(t *testing.T) {
	store, plans, request := authorizationFixture()
	gate, err := NewAuthorizationGate(store, plans)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Authorize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if store.gateCalls != 1 {
		t.Fatalf("gate calls = %d", store.gateCalls)
	}
	store.deployment.Status = deployments.Applying
	if err := gate.Authorize(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if store.gateCalls != 1 {
		t.Fatal("idempotent recovery unexpectedly repeated the transition")
	}
}

func TestRepositoryAuthorizationGateFailsBeforeMutationOnProvenanceOrMigrationDrift(t *testing.T) {
	store, plans, request := authorizationFixture()
	gate, _ := NewAuthorizationGate(store, plans)
	store.deployment.ReleaseID = "wrong"
	if err := gate.Authorize(context.Background(), request); err == nil || store.gateCalls != 0 {
		t.Fatal("release provenance drift was accepted")
	}
	store, plans, request = authorizationFixture()
	plans.plan.Plan.Migration = &deploymentplans.Migration{ComponentName: "web", RootDirectory: ".", Command: "npm run migrate", EvidenceDigest: string(make([]byte, 64)), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}}
	gate, _ = NewAuthorizationGate(store, plans)
	if err := gate.Authorize(context.Background(), request); err != deployments.ErrApprovalRequired || store.gateCalls != 0 {
		t.Fatalf("migration error=%v gateCalls=%d", err, store.gateCalls)
	}
}

func authorizationFixture() (*authorizationFixtureStore, authorizationFixturePlans, AuthorizationRequest) {
	appID := "11111111-1111-4111-8111-111111111111"
	deploymentID := "22222222-2222-4222-8222-222222222222"
	planID := "33333333-3333-4333-8333-333333333333"
	releaseID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &authorizationFixtureStore{deployment: deployments.Deployment{ID: deploymentID, AppID: appID, ReleaseID: releaseID, Status: deployments.Preparing, RuntimeStrategy: deployments.RuntimeGeneratedNode, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ProvenanceInitialized: true}}
	plans := authorizationFixturePlans{plan: deploymentplans.DeploymentPlanRevision{ID: planID, AppID: appID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{{Name: "web", Role: "server"}}}}}
	request := AuthorizationRequest{AppID: appID, DeploymentID: deploymentID, ReleaseID: releaseID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, CandidateSlot: generatedruntime.SlotBlue, ComponentCount: 1}
	return store, plans, request
}
