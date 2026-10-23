package generatedingress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

func TestGeneratedRouteActivationTransactionFailuresRollBackAllSQL(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		table     string
		when      string
	}{
		{name: "receipt insert", operation: "INSERT", table: "generated_route_activations"},
		{name: "head update", operation: "UPDATE", table: "generated_runtime_active_heads"},
		{name: "candidate roster update", operation: "UPDATE", table: "generated_runtime_components",
			when: "WHEN OLD.state='healthy' AND NEW.state='active'"},
		{name: "deployment phase update", operation: "UPDATE", table: "generated_runtime_deployments",
			when: "WHEN OLD.phase='switching_route' AND NEW.phase='draining'"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, sealed, _, runner := newGeneratedRouteActivationFailureFixture(t)
			database := fixture.validation.migration.repository.db
			marker := fmt.Sprintf("injected route activation %s failure", test.name)
			trigger := fmt.Sprintf(`CREATE TRIGGER route_activation_abort_%d BEFORE %s ON %s %s
				BEGIN SELECT RAISE(ABORT, '%s'); END`,
				index, test.operation, test.table, test.when, marker)
			if _, err := database.Exec(trigger); err != nil {
				t.Fatal(err)
			}

			if deployment, err := sealed.Activate(fixture.validation.migration.guarded.context); err == nil ||
				deployment.DeploymentID != "" || !strings.Contains(err.Error(), marker) {
				t.Fatalf("activation at %s result=%#v error=%v", test.name, deployment, err)
			}
			assertGeneratedRouteActivationInitialSQL(t, fixture)

			stored, err := sealed.authority.manager.store.load()
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := stored.Active[runner.appID]; !exists {
				t.Fatalf("physical route disappeared after %s failure", test.name)
			}
		})
	}
	t.Run("commit", func(t *testing.T) {
		fixture, sealed, _, _ := newGeneratedRouteActivationFailureFixture(t)
		database := fixture.validation.migration.repository.db
		if _, err := database.Exec(`CREATE TABLE route_activation_commit_fault(
			id TEXT PRIMARY KEY, missing_app TEXT NOT NULL,
			FOREIGN KEY(missing_app) REFERENCES applications(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TRIGGER route_activation_commit_abort
			AFTER UPDATE OF phase ON generated_runtime_deployments
			WHEN OLD.phase='switching_route' AND NEW.phase='draining'
			BEGIN INSERT INTO route_activation_commit_fault(id,missing_app)
			VALUES(NEW.deployment_id,'missing-route-activation-app'); END`); err != nil {
			t.Fatal(err)
		}
		if deployment, err := sealed.Activate(fixture.validation.migration.guarded.context); err == nil ||
			deployment.DeploymentID != "" || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Fatalf("commit-failure activation result=%#v error=%v", deployment, err)
		}
		assertGeneratedRouteActivationInitialSQL(t, fixture)
		var faultCount int
		if err := database.QueryRow(`SELECT COUNT(*) FROM route_activation_commit_fault`).Scan(&faultCount); err != nil {
			t.Fatal(err)
		}
		if faultCount != 0 {
			t.Fatalf("deferred commit fault row survived rollback: %d", faultCount)
		}
	})
}

func TestGeneratedRouteActivationExactPersistedReplayAndCrossedFields(t *testing.T) {
	t.Run("exact replay", func(t *testing.T) {
		fixture, sealed, execution, _ := newGeneratedRouteActivationFailureFixture(t)
		pinned, claim, input := generatedRouteActivationWriterInput(t, sealed, execution)
		firstDeployment, err := sealed.Activate(fixture.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		if firstDeployment.DeploymentID != input.successorDeploymentID {
			t.Fatalf("initial activation deployment=%#v", firstDeployment)
		}
		input.committedRouteProof = generatedRouteActivationObservedProof(t, sealed)
		replayed, err := sealed.authority.activateRoute(context.Background(), pinned, claim, input)
		if err != nil {
			t.Fatal(err)
		}
		if replayed.changed || replayed.head.ActivationReceiptID != input.receiptID ||
			replayed.receipt.CommittedRouteProofDigest != generatedRouteActivationDocumentDigest(input.committedRouteProof) {
			t.Fatalf("exact replay result=%#v", replayed)
		}
		assertGeneratedRouteActivationCommittedSQL(t, fixture, input)
	})

	crossed := []struct {
		name   string
		mutate func(*testing.T, *candidateOwnerFixture, *generatedRouteActivationInput)
	}{
		{name: "receipt", mutate: func(_ *testing.T, _ *candidateOwnerFixture, input *generatedRouteActivationInput) {
			input.receiptID = crossedGeneratedRouteActivationHex(input.receiptID)
		}},
		{name: "candidate roster", mutate: func(_ *testing.T, _ *candidateOwnerFixture, input *generatedRouteActivationInput) {
			input.candidateRoster[0].ContainerID = crossedGeneratedRouteActivationHex(input.candidateRoster[0].ContainerID)
		}},
		{name: "proof", mutate: func(_ *testing.T, _ *candidateOwnerFixture, input *generatedRouteActivationInput) {
			input.committedRouteProof = []byte(`{"crossed":true}`)
		}},
		{name: "original Claim", mutate: func(t *testing.T, fixture *candidateOwnerFixture, _ *generatedRouteActivationInput) {
			if _, err := fixture.validation.migration.repository.db.Exec(`UPDATE jobs SET attempt=attempt+1 WHERE id=?`,
				fixture.validation.migration.claim.JobID()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range crossed {
		t.Run("rejects crossed "+test.name, func(t *testing.T) {
			fixture, sealed, execution, _ := newGeneratedRouteActivationFailureFixture(t)
			pinned, claim, input := generatedRouteActivationWriterInput(t, sealed, execution)
			first, err := sealed.Activate(fixture.validation.migration.guarded.context)
			if err != nil {
				t.Fatal(err)
			}
			input.committedRouteProof = generatedRouteActivationObservedProof(t, sealed)
			crossedInput := input
			crossedInput.candidateRoster = append([]generatedruntimestate.RouteActivationComponent(nil), input.candidateRoster...)
			test.mutate(t, fixture, &crossedInput)
			if replayed, err := sealed.authority.activateRoute(context.Background(), pinned, claim, crossedInput); err == nil || replayed.changed {
				t.Fatalf("crossed %s replay result=%#v error=%v", test.name, replayed, err)
			}
			assertGeneratedRouteActivationCommittedSQL(t, fixture, input)
			if first.DeploymentID != input.successorDeploymentID {
				t.Fatalf("initial activation deployment = %q", first.DeploymentID)
			}
		})
	}
}

func TestGeneratedRouteExecutionIsOneShotAcrossConcurrentAndFailedOriginalUse(t *testing.T) {
	fixture, sealed, _, _ := newGeneratedRouteActivationFailureFixture(t)
	type result struct {
		deployment generatedruntimestate.Deployment
		err        error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	ctx, cancel := context.WithTimeout(fixture.validation.migration.guarded.context, 5*time.Second)
	defer cancel()
	var ready, workers sync.WaitGroup
	ready.Add(2)
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			ready.Done()
			<-start
			deployment, err := sealed.Activate(ctx)
			results <- result{deployment: deployment, err: err}
		}()
	}
	ready.Wait()
	close(start)
	observedResults := make([]result, 0, 2)
	var waitErr error
	for len(observedResults) < 2 && waitErr == nil {
		select {
		case observed := <-results:
			observedResults = append(observedResults, observed)
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	}
	cancel()
	joined := make(chan struct{})
	go func() {
		workers.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent activation workers did not join after cancellation")
	}
	close(results)
	if waitErr != nil {
		t.Fatalf("concurrent activation did not finish: %v", waitErr)
	}
	successes, unavailable := 0, 0
	for _, observed := range observedResults {
		switch {
		case observed.err == nil && observed.deployment.DeploymentID == fixture.validation.request.DeploymentID:
			successes++
		case errors.Is(observed.err, deploymentexecution.ErrScopeUnavailable) && observed.deployment.DeploymentID == "":
			unavailable++
		default:
			t.Fatalf("unexpected concurrent activation result=%#v error=%v", observed.deployment, observed.err)
		}
	}
	if successes != 1 || unavailable != 1 {
		t.Fatalf("concurrent original use successes=%d scope-unavailable=%d", successes, unavailable)
	}

	var receiptCount int
	if err := fixture.validation.migration.repository.db.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	head, err := generatedruntimestate.New(fixture.validation.migration.repository.db).
		Active(context.Background(), fixture.validation.migration.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || head.Generation != 1 || head.ActivationReceiptID == "" {
		t.Fatalf("one-shot durable state receipts=%d head=%#v", receiptCount, head)
	}

	t.Run("failure consumes original", func(t *testing.T) {
		failedFixture, failed, _, failedRunner := newGeneratedRouteActivationFailureFixture(t)
		const marker = "injected route activation failure reuse"
		if _, err := failedFixture.validation.migration.repository.db.Exec(`CREATE TRIGGER route_activation_failure_reuse
			BEFORE INSERT ON generated_route_activations
			BEGIN SELECT RAISE(ABORT, 'injected route activation failure reuse'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := failed.Activate(failedFixture.validation.migration.guarded.context); err == nil ||
			!strings.Contains(err.Error(), marker) {
			t.Fatalf("injected activation failure error=%v", err)
		}
		commands := len(failedRunner.commands)
		if deployment, err := failed.Activate(context.Background()); deployment.DeploymentID != "" ||
			!errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
			t.Fatalf("failed original reuse result=%#v error=%v", deployment, err)
		}
		if len(failedRunner.commands) != commands {
			t.Fatalf("failed original reuse issued commands: before=%d after=%d", commands, len(failedRunner.commands))
		}
		assertGeneratedRouteActivationInitialSQL(t, failedFixture)
	})
}

func newGeneratedRouteActivationFailureFixture(t *testing.T) (*candidateOwnerFixture, *GeneratedRouteExecution,
	*GeneratedCandidateExecution, *ingressRunner,
) {
	t.Helper()
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
	return fixture, sealed, execution, runner
}

func generatedRouteActivationWriterInput(t *testing.T, sealed *GeneratedRouteExecution,
	execution *GeneratedCandidateExecution,
) (*deploymentexecution.Pinned, deploymentexecution.Claim, generatedRouteActivationInput) {
	t.Helper()
	state, done, err := execution.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	input := generatedRouteActivationInput{receiptID: sealed.receiptID, appID: sealed.request.AppID,
		successorDeploymentID: state.pins.deploymentID, successorReleaseID: state.pins.releaseID,
		successorSlot: state.pins.candidateSlot, expectedHead: sealed.expectedHead,
		candidateRoster:   append([]generatedruntimestate.RouteActivationComponent(nil), sealed.candidateRoster...),
		predecessorRoster: append([]generatedruntimestate.RouteActivationComponent(nil), sealed.predecessorRoster...),
		routeIntent:       append([]byte(nil), sealed.intent...)}
	return state.pinned, state.pins.claim, input
}

func generatedRouteActivationObservedProof(t *testing.T, sealed *GeneratedRouteExecution) []byte {
	t.Helper()
	observation, err := sealed.authority.manager.Observe(context.Background(), sealed.request.AppID)
	if err != nil {
		t.Fatal(err)
	}
	proof := generatedRouteProofDocument{Version: 1, ReceiptID: sealed.receiptID,
		After: routeRecord{Slot: observation.Slot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), observation.Endpoints...)}}
	if sealed.hadPrevious {
		before := sealed.previous
		proof.Before = &before
	}
	body, err := marshalCanonicalGeneratedRouteDocument(proof)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertGeneratedRouteActivationInitialSQL(t *testing.T, fixture *candidateOwnerFixture) {
	t.Helper()
	database := fixture.validation.migration.repository.db
	var receiptCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	head, err := generatedruntimestate.New(database).Active(context.Background(), fixture.validation.migration.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	var phase generatedruntimestate.Phase
	var state generatedruntimestate.ComponentState
	if err := database.QueryRow(`SELECT phase FROM generated_runtime_deployments WHERE deployment_id=?`,
		fixture.validation.request.DeploymentID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT state FROM generated_runtime_components WHERE deployment_id=? AND component_name='api'`,
		fixture.validation.request.DeploymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 0 || head.Generation != 0 || head.DeploymentID != "" || head.ReleaseID != "" ||
		head.Slot != "" || head.ActivationReceiptID != "" || phase != generatedruntimestate.PhaseSwitchingRoute ||
		state != generatedruntimestate.ComponentHealthy {
		t.Fatalf("partial activation receipts=%d head=%#v phase=%q state=%q", receiptCount, head, phase, state)
	}
}

func assertGeneratedRouteActivationCommittedSQL(t *testing.T, fixture *candidateOwnerFixture,
	input generatedRouteActivationInput,
) {
	t.Helper()
	database := fixture.validation.migration.repository.db
	var receiptCount, activeCount int
	var proofDigest string
	if err := database.QueryRow(`SELECT COUNT(*),COALESCE(MAX(committed_route_proof_digest),'')
		FROM generated_route_activations WHERE id=?`, input.receiptID).Scan(&receiptCount, &proofDigest); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components
		WHERE deployment_id=? AND state='active'`, input.successorDeploymentID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	var phase generatedruntimestate.Phase
	if err := database.QueryRow(`SELECT phase FROM generated_runtime_deployments WHERE deployment_id=?`,
		input.successorDeploymentID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	head, err := generatedruntimestate.New(database).Active(context.Background(), input.appID)
	if err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || proofDigest != generatedRouteActivationDocumentDigest(input.committedRouteProof) ||
		activeCount != len(input.candidateRoster) || phase != generatedruntimestate.PhaseDraining ||
		head.DeploymentID != input.successorDeploymentID || head.ReleaseID != input.successorReleaseID ||
		head.Slot != input.successorSlot || head.Generation != input.expectedHead.Generation+1 ||
		head.ActivationReceiptID != input.receiptID {
		t.Fatalf("committed activation receipts=%d proof=%q active=%d phase=%q head=%#v", receiptCount,
			proofDigest, activeCount, phase, head)
	}
}

func crossedGeneratedRouteActivationHex(value string) string {
	if value[len(value)-1] == '0' {
		return value[:len(value)-1] + "1"
	}
	return value[:len(value)-1] + "0"
}
