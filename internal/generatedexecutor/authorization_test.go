package generatedexecutor

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type authorizationFixtureStore struct {
	deployment deployments.Deployment
	gateCalls  int
	events     *[]string
}

func (s *authorizationFixtureStore) Get(context.Context, string, string) (deployments.Deployment, error) {
	return s.deployment, nil
}
func (s *authorizationFixtureStore) Gate(context.Context, string, string, []deployments.Finding) error {
	s.gateCalls++
	if s.events != nil {
		*s.events = append(*s.events, "gate")
	}
	return nil
}

type authorizationFixtureReservation struct{ releases atomic.Int32 }

func (r *authorizationFixtureReservation) Release() { r.releases.Add(1) }

type authorizationFixtureCapacity struct {
	calls       int
	err         error
	events      *[]string
	reservation *authorizationFixtureReservation
}

func (c *authorizationFixtureCapacity) ReserveReplacement(context.Context, int) (generatedruntime.ReplacementReservation, error) {
	c.calls++
	if c.events != nil {
		*c.events = append(*c.events, "reserve")
	}
	if c.err != nil {
		return nil, c.err
	}
	c.reservation = &authorizationFixtureReservation{}
	return c.reservation, nil
}

type authorizationFixturePlans struct {
	plan deploymentplans.DeploymentPlanRevision
}

func (p authorizationFixturePlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return p.plan, nil
}

func TestRepositoryAuthorizationGateMovesExactPinnedDeploymentToApplying(t *testing.T) {
	store, plans, request := authorizationFixture()
	events := []string{}
	store.events = &events
	capacity := &authorizationFixtureCapacity{events: &events}
	gate, err := NewAuthorizationGate(store, plans, capacity)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := gate.Authorize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if store.gateCalls != 1 {
		t.Fatalf("gate calls = %d", store.gateCalls)
	}
	if !reflect.DeepEqual(events, []string{"reserve", "gate"}) {
		t.Fatalf("authorization ordering = %v", events)
	}
	store.deployment.Status = deployments.Applying
	reservation, err = gate.Authorize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if store.gateCalls != 1 {
		t.Fatal("idempotent recovery unexpectedly repeated the transition")
	}
	if capacity.calls != 2 {
		t.Fatalf("recovery reservations = %d", capacity.calls)
	}
}

func TestRepositoryAuthorizationGateFailsBeforeMutationOnProvenanceOrMigrationDrift(t *testing.T) {
	store, plans, request := authorizationFixture()
	capacity := &authorizationFixtureCapacity{}
	gate, _ := NewAuthorizationGate(store, plans, capacity)
	store.deployment.ReleaseID = "wrong"
	if _, err := gate.Authorize(context.Background(), request); err == nil || store.gateCalls != 0 || capacity.calls != 0 {
		t.Fatal("release provenance drift was accepted")
	}
	store, plans, request = authorizationFixture()
	plans.plan.Plan.Migration = &deploymentplans.Migration{ComponentName: "web", RootDirectory: ".", Command: "npm run migrate", EvidenceDigest: string(make([]byte, 64)), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}}
	capacity = &authorizationFixtureCapacity{}
	gate, _ = NewAuthorizationGate(store, plans, capacity)
	if _, err := gate.Authorize(context.Background(), request); err != deployments.ErrApprovalRequired || store.gateCalls != 0 || capacity.calls != 0 {
		t.Fatalf("migration error=%v gateCalls=%d", err, store.gateCalls)
	}
}

func TestRepositoryAuthorizationGateReleasesReservationWhenDatabaseGateFails(t *testing.T) {
	store, plans, request := authorizationFixture()
	store.deployment.Status = deployments.Succeeded
	capacity := &authorizationFixtureCapacity{}
	gate, _ := NewAuthorizationGate(store, plans, capacity)
	if _, err := gate.Authorize(context.Background(), request); err == nil {
		t.Fatal("terminal deployment was authorized")
	}
	if capacity.reservation == nil || capacity.reservation.releases.Load() != 1 {
		t.Fatal("failed authorization did not release its aggregate reservation exactly once")
	}
}

func TestRepositoryAuthorizationGateMapsInsufficientAggregateCapacity(t *testing.T) {
	store, plans, request := authorizationFixture()
	capacity := &authorizationFixtureCapacity{err: &generatedruntime.Error{Code: generatedruntime.DiagnosticInsufficientReplacementSpace}}
	gate, _ := NewAuthorizationGate(store, plans, capacity)
	if _, err := gate.Authorize(context.Background(), request); err != ErrInsufficientReplacementCapacity {
		t.Fatalf("capacity error = %v", err)
	}
	if store.gateCalls != 0 {
		t.Fatal("database gate ran without aggregate capacity")
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
