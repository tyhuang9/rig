package generatedingress

import (
	"context"
	"errors"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGeneratedRouteAuthorityCommitsReceiptHeadAndRosterWithOriginalClaim(t *testing.T) {
	fixture, execution, identity := generatedRouteCandidateFixture(t)
	manager, runner := newManagerFixture(t, false)
	runner.appID = fixture.validation.migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
	authority, err := newGeneratedRouteAuthority(manager, fixture.authority, fixture.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	authorityCopy := *authority
	if copied, err := authorityCopy.Pin(fixture.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity)); copied != nil || !errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("copied route authority result=%#v error=%v", copied, err)
	}
	sealed, err := authority.Pin(fixture.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	state, done, err := execution.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	probe := generatedRouteActivationInput{receiptID: sealed.receiptID, appID: runner.appID,
		successorDeploymentID: state.pins.deploymentID, successorReleaseID: state.pins.releaseID,
		successorSlot: state.pins.candidateSlot, expectedHead: sealed.expectedHead,
		candidateRoster: sealed.candidateRoster, predecessorRoster: sealed.predecessorRoster,
		routeIntent: sealed.intent, committedRouteProof: []byte(`{}`)}
	if !validGeneratedRouteActivationInput(probe, state.pins.claim) {
		done()
		t.Fatalf("sealed activation input is invalid: %#v", probe)
	}
	if _, valid := canonicalGeneratedRouteActivationRoster(probe.candidateRoster); !valid {
		done()
		t.Fatalf("sealed candidate roster is invalid: %#v", probe.candidateRoster)
	}
	done()
	sealedCopy := *sealed
	if copied, err := sealedCopy.Activate(fixture.validation.migration.guarded.context); copied.DeploymentID != "" ||
		!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("copied sealed activation result=%#v error=%v", copied, err)
	}
	result, err := sealed.Activate(fixture.validation.migration.guarded.context)
	if err != nil {
		current, _ := generatedruntimestate.New(fixture.validation.migration.repository.db).
			Get(context.Background(), runner.appID, fixture.validation.request.DeploymentID)
		t.Fatalf("activate: %v expected=%#v candidate=%#v runtime=%#v", err,
			sealed.expectedHead, sealed.candidateRoster, current)
	}
	if result.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("activated deployment phase = %q", result.Phase)
	}
	repository := generatedruntimestate.New(fixture.validation.migration.repository.db)
	head, err := repository.Active(context.Background(), runner.appID)
	if err != nil {
		t.Fatal(err)
	}
	if head.DeploymentID != fixture.validation.request.DeploymentID || head.Generation != 1 ||
		head.ActivationReceiptID == "" {
		t.Fatalf("activated head = %#v", head)
	}
	var receiptCount, activeCount int
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations
		WHERE id=? AND app_id=? AND successor_deployment_id=?`, head.ActivationReceiptID, runner.appID,
		fixture.validation.request.DeploymentID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components
		WHERE deployment_id=? AND state='active'`, fixture.validation.request.DeploymentID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || activeCount != 1 {
		t.Fatalf("durable activation receipt=%d active-components=%d", receiptCount, activeCount)
	}
	if _, err := sealed.Activate(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("sealed activation replay error = %v", err)
	}
	if err := manager.Switch(context.Background(), generatedRouteRequest(runner.appID, identity)); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ordinary Manager.Switch error = %v", err)
	}
	if err := manager.Provision(context.Background()); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ordinary Manager.Provision error = %v", err)
	}
	if err := manager.Recover(context.Background()); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ordinary Manager.Recover error = %v", err)
	}
}

func TestGeneratedRouteAuthorityRejectsChangedOriginalClaimWithoutPartialSQLCommit(t *testing.T) {
	fixture, execution, identity := generatedRouteCandidateFixture(t)
	manager, runner := newManagerFixture(t, false)
	runner.appID = fixture.validation.migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
	authority, err := newGeneratedRouteAuthority(manager, fixture.authority, fixture.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := authority.Pin(fixture.validation.migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	sealedCopy := *sealed
	changed := false
	runner.gatewayProbeMutate = func(*ingressRunner) {
		if changed {
			return
		}
		changed = true
		if _, mutateErr := fixture.validation.migration.repository.db.Exec(`UPDATE jobs SET attempt=attempt+1
			WHERE id=?`, fixture.validation.migration.claim.JobID()); mutateErr != nil {
			t.Fatalf("change original claim: %v", mutateErr)
		}
	}
	if _, err := sealed.Activate(fixture.validation.migration.guarded.context); err == nil {
		t.Fatal("activation with a changed original Claim succeeded")
	}
	commandsAfterFailure := len(runner.commands)
	if copied, err := sealedCopy.Activate(fixture.validation.migration.guarded.context); copied.DeploymentID != "" ||
		!errors.Is(err, deploymentexecution.ErrInvalidConfiguration) {
		t.Fatalf("copied failed activation result=%#v error=%v", copied, err)
	}
	if len(runner.commands) != commandsAfterFailure {
		t.Fatalf("copied failed activation issued commands: before=%d after=%d", commandsAfterFailure, len(runner.commands))
	}
	if !changed {
		t.Fatal("claim did not change after sealed physical proof")
	}
	var receiptCount int
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).
		Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	head, err := generatedruntimestate.New(fixture.validation.migration.repository.db).
		Active(context.Background(), runner.appID)
	if err != nil {
		t.Fatal(err)
	}
	if receiptCount != 0 || head.Generation != 0 || head.DeploymentID != "" || head.ActivationReceiptID != "" {
		t.Fatalf("failed activation committed SQL receipt=%d head=%#v", receiptCount, head)
	}
	var phase generatedruntimestate.Phase
	var state generatedruntimestate.ComponentState
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT phase FROM generated_runtime_deployments
		WHERE deployment_id=?`, fixture.validation.request.DeploymentID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT state FROM generated_runtime_components
		WHERE deployment_id=? AND component_name='api'`, fixture.validation.request.DeploymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if phase != generatedruntimestate.PhaseSwitchingRoute || state != generatedruntimestate.ComponentHealthy {
		t.Fatalf("failed activation phase/state = %q/%q", phase, state)
	}
	stored, err := manager.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stored.Active[runner.appID]; !exists {
		t.Fatal("first physical route was discarded without a durable predecessor")
	}
}

func generatedRouteCandidateFixture(t *testing.T) (*candidateOwnerFixture, *GeneratedCandidateExecution, CandidateIdentity) {
	t.Helper()
	fixture := newCandidateOwnerFixture(t)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}
	return fixture, execution, identity
}

func generatedRouteEndpoint(identity CandidateIdentity) generatedroute.RouteEndpoint {
	return generatedroute.RouteEndpoint{Component: identity.ComponentName, Role: identity.Role,
		ContainerID: identity.ContainerID, NetworkName: identity.NetworkName,
		NetworkAlias: identity.NetworkAlias, InternalPort: identity.InternalPort}
}

func generatedRouteRequest(appID string, identity CandidateIdentity) generatedroute.RouteSwitchRequest {
	return generatedroute.RouteSwitchRequest{AppID: appID, ToSlot: generatedidentity.Slot(identity.Slot),
		Endpoints: []generatedroute.RouteEndpoint{generatedRouteEndpoint(identity)}}
}
