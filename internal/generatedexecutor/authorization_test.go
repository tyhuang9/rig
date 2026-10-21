package generatedexecutor

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
)

type authorizationFixtureStore struct {
	deployment deployments.Deployment
	getErr     error
	gateErr    error
	gateCalls  int
	requests   []deployments.GateRequest
	events     *[]string
}

func (s *authorizationFixtureStore) Get(context.Context, string, string) (deployments.Deployment, error) {
	return s.deployment, s.getErr
}
func (s *authorizationFixtureStore) Gate(_ context.Context, request deployments.GateRequest) error {
	s.gateCalls++
	s.requests = append(s.requests, request)
	if s.events != nil {
		*s.events = append(*s.events, "gate")
	}
	return s.gateErr
}

type authorizationFixtureCapacity struct {
	calls       int
	err         error
	events      *[]string
	delegate    replacementReserver
	reservation generatedruntime.ReplacementReservation
}

func (c *authorizationFixtureCapacity) ReserveReplacement(ctx context.Context, request deployments.GeneratedReplacementRequest) (generatedruntime.ReplacementReservation, error) {
	c.calls++
	if c.events != nil {
		*c.events = append(*c.events, "reserve")
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.delegate == nil {
		return nil, errors.New("successful authorization requires the sealed candidate authority")
	}
	reservation, err := c.delegate.ReserveReplacement(ctx, request)
	c.reservation = reservation
	return reservation, err
}

type authorizationFixturePlans struct {
	plan deploymentplans.DeploymentPlanRevision
	err  error
}

func (p authorizationFixturePlans) Get(context.Context, string) (deploymentplans.DeploymentPlanRevision, error) {
	return p.plan, nil
}

func (p authorizationFixturePlans) GetRevision(context.Context, string, string, int64) (deploymentplans.DeploymentPlanRevision, error) {
	return p.plan, p.err
}

func TestRepositoryAuthorizationGateMovesExactPinnedDeploymentToApplying(t *testing.T) {
	authority := newSealedExecutorFixture(t)
	store, plans, request := authorizationFixture(t, authority)
	events := []string{}
	store.events = &events
	capacity := &authorizationFixtureCapacity{events: &events, delegate: authority.engine}
	gate, err := NewAuthorizationGate(store, plans, capacity)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := gate.Authorize(authority.context, request)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if store.gateCalls != 1 {
		t.Fatalf("gate calls = %d", store.gateCalls)
	}
	assertAuthorizationGateRequest(t, store.requests[0], request)
	if !reflect.DeepEqual(events, []string{"reserve", "gate"}) {
		t.Fatalf("authorization ordering = %v", events)
	}
	for _, status := range []deployments.Status{deployments.Applying, deployments.WaitingHealth} {
		store.deployment.Status = status
		reservation, err = gate.Authorize(authority.context, request)
		if err != nil {
			t.Fatal(err)
		}
		reservation.Release()
		assertAuthorizationGateRequest(t, store.requests[len(store.requests)-1], request)
	}
	if store.gateCalls != 3 {
		t.Fatalf("recovered authorization gate calls = %d", store.gateCalls)
	}
	if capacity.calls != 3 {
		t.Fatalf("recovery reservations = %d", capacity.calls)
	}
}

func assertAuthorizationGateRequest(t *testing.T, actual deployments.GateRequest, request AuthorizationRequest) {
	t.Helper()
	if actual.DeploymentID != request.DeploymentID || !reflect.DeepEqual(actual.Claim, request.Claim) || actual.Findings != nil {
		t.Fatalf("gate request = %#v", actual)
	}
}

func TestRepositoryAuthorizationGateFailsBeforeMutationOnProvenanceOrMigrationDrift(t *testing.T) {
	store, plans, request := authorizationFixture(t, nil)
	capacity := &authorizationFixtureCapacity{}
	gate, _ := NewAuthorizationGate(store, plans, capacity)
	store.deployment.ReleaseID = "wrong"
	if _, err := gate.Authorize(context.Background(), request); err == nil || store.gateCalls != 0 || capacity.calls != 0 {
		t.Fatal("release provenance drift was accepted")
	}
	store, plans, request = authorizationFixture(t, nil)
	plans.plan.Plan.Migration = &deploymentplans.Migration{ComponentName: "web", RootDirectory: ".", Command: "npm run migrate", EvidenceDigest: string(make([]byte, 64)), Approval: deploymentplans.MigrationApproval{Status: deploymentplans.MigrationApprovalPending}}
	capacity = &authorizationFixtureCapacity{}
	gate, _ = NewAuthorizationGate(store, plans, capacity)
	if _, err := gate.Authorize(context.Background(), request); err != deployments.ErrApprovalRequired || store.gateCalls != 0 || capacity.calls != 0 {
		t.Fatalf("migration error=%v gateCalls=%d", err, store.gateCalls)
	}
}

func TestRepositoryAuthorizationGatePreservesReadFailureCauses(t *testing.T) {
	storageErr := errors.New("storage sentinel")
	for _, test := range []struct {
		name string
		err  error
		set  func(*authorizationFixtureStore, *authorizationFixturePlans, error)
	}{
		{name: "deployment storage", err: storageErr, set: func(store *authorizationFixtureStore, _ *authorizationFixturePlans, err error) { store.getErr = err }},
		{name: "deployment canceled", err: context.Canceled, set: func(store *authorizationFixtureStore, _ *authorizationFixturePlans, err error) { store.getErr = err }},
		{name: "plan storage", err: storageErr, set: func(_ *authorizationFixtureStore, plans *authorizationFixturePlans, err error) { plans.err = err }},
		{name: "plan deadline", err: context.DeadlineExceeded, set: func(_ *authorizationFixtureStore, plans *authorizationFixturePlans, err error) { plans.err = err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, plans, request := authorizationFixture(t, nil)
			test.set(store, &plans, test.err)
			capacity := &authorizationFixtureCapacity{}
			gate, err := NewAuthorizationGate(store, plans, capacity)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.Authorize(context.Background(), request); !errors.Is(err, test.err) {
				t.Fatalf("read error=%v", err)
			}
			if capacity.calls != 0 || store.gateCalls != 0 {
				t.Fatalf("capacity=%d gate=%d", capacity.calls, store.gateCalls)
			}
		})
	}
}

func TestRepositoryAuthorizationGateReleasesReservationWhenDatabaseGateFails(t *testing.T) {
	authority := newSealedExecutorFixture(t)
	store, plans, request := authorizationFixture(t, authority)
	store.deployment.Status = deployments.Succeeded
	capacity := &authorizationFixtureCapacity{delegate: authority.engine}
	gate, _ := NewAuthorizationGate(store, plans, capacity)
	if _, err := gate.Authorize(authority.context, request); err == nil {
		t.Fatal("terminal deployment was authorized")
	}
	if capacity.reservation == nil || authority.engine.RetainReplacementCapacity(capacity.reservation) == nil {
		t.Fatal("failed authorization left its sealed reservation live")
	}
}

func TestRepositoryAuthorizationGateReleasesRecoveredReservationExactlyOnceOnGateRefusal(t *testing.T) {
	for _, status := range []deployments.Status{deployments.Applying, deployments.WaitingHealth} {
		t.Run(string(status), func(t *testing.T) {
			authority := newSealedExecutorFixture(t)
			store, plans, request := authorizationFixture(t, authority)
			store.deployment.Status = status
			cause := errors.New("gate refusal sentinel")
			store.gateErr = cause
			capacity := &authorizationFixtureCapacity{delegate: authority.engine}
			gate, err := NewAuthorizationGate(store, plans, capacity)
			if err != nil {
				t.Fatal(err)
			}
			if reservation, err := gate.Authorize(authority.context, request); reservation != nil || !errors.Is(err, cause) {
				t.Fatalf("reservation=%#v error=%v", reservation, err)
			}
			if capacity.calls != 1 || store.gateCalls != 1 || capacity.reservation == nil || authority.engine.RetainReplacementCapacity(capacity.reservation) == nil {
				t.Fatalf("capacity=%d gate=%d reservation=%#v", capacity.calls, store.gateCalls, capacity.reservation)
			}
			assertAuthorizationGateRequest(t, store.requests[0], request)
		})
	}
}

func TestRepositoryAuthorizationGateMapsInsufficientAggregateCapacity(t *testing.T) {
	store, plans, request := authorizationFixture(t, nil)
	capacity := &authorizationFixtureCapacity{err: &generatedruntime.Error{Code: generatedruntime.DiagnosticInsufficientReplacementSpace}}
	gate, _ := NewAuthorizationGate(store, plans, capacity)
	if _, err := gate.Authorize(context.Background(), request); err != ErrInsufficientReplacementCapacity {
		t.Fatalf("capacity error = %v", err)
	}
	if store.gateCalls != 0 {
		t.Fatal("database gate ran without aggregate capacity")
	}
}

func authorizationFixture(t *testing.T, authority *sealedExecutorFixture) (*authorizationFixtureStore, authorizationFixturePlans, AuthorizationRequest) {
	if authority != nil {
		deployment, request := authority.prepareAuthorization(t)
		return &authorizationFixtureStore{deployment: deployment}, authorizationFixturePlans{plan: authority.plan}, request
	}
	appID := "11111111-1111-4111-8111-111111111111"
	deploymentID := "22222222-2222-4222-8222-222222222222"
	planID := "33333333-3333-4333-8333-333333333333"
	releaseID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &authorizationFixtureStore{deployment: deployments.Deployment{ID: deploymentID, AppID: appID, ReleaseID: releaseID, Status: deployments.Preparing, RuntimeStrategy: deployments.RuntimeGeneratedNode, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ProvenanceInitialized: true}}
	plans := authorizationFixturePlans{plan: deploymentplans.DeploymentPlanRevision{ID: planID, AppID: appID, RevisionNumber: 1, Plan: deploymentplans.Plan{Strategy: deploymentplans.StrategyGeneratedNode, Components: []deploymentplans.Component{{Name: "web", Role: "server"}}}}}
	request := AuthorizationRequest{Claim: newGeneratedExecutorClaim(t), AppID: appID, DeploymentID: deploymentID, ReleaseID: releaseID, DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, CandidateSlot: generatedruntime.SlotBlue, ComponentCount: 1}
	return store, plans, request
}
