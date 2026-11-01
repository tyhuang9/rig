package generatedingress

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

func TestGeneratedRoutePredecessorSecondActivationCommitsExactRosters(t *testing.T) {
	fixture := newGeneratedRoutePredecessorFixture(t)
	result, err := fixture.successor.Activate(fixture.owner.validation.migration.guarded.context)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeploymentID != fixture.successorDeploymentID || result.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("successor activation result=%#v", result)
	}
	assertGeneratedRoutePredecessorCommitted(t, fixture)
	observation, err := fixture.manager.Observe(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRoute(fixture.successorRoute, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		t.Fatalf("successor physical route=%#v", observation)
	}
}

func TestGeneratedRoutePredecessorUpdateFailureRollsBackAndRestores(t *testing.T) {
	fixture := newGeneratedRoutePredecessorFixture(t)
	const marker = "injected predecessor roster drain failure"
	if _, err := fixture.database().Exec(`CREATE TRIGGER route_activation_predecessor_abort
		BEFORE UPDATE OF state ON generated_runtime_components
		WHEN OLD.deployment_id='` + fixture.predecessorDeploymentID + `' AND OLD.state='active' AND NEW.state='draining'
		BEGIN SELECT RAISE(ABORT, '` + marker + `'); END`); err != nil {
		t.Fatal(err)
	}
	commandsBefore := len(fixture.runner.commands)
	if result, err := fixture.successor.Activate(fixture.owner.validation.migration.guarded.context); err == nil ||
		result.DeploymentID != "" || !strings.Contains(err.Error(), marker) {
		t.Fatalf("predecessor failpoint result=%#v error=%v", result, err)
	}
	assertGeneratedRoutePredecessorRolledBack(t, fixture)
	observation, err := fixture.manager.Observe(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRoute(fixture.predecessorRoute, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		t.Fatalf("restored predecessor route=%#v want=%#v", observation, fixture.predecessorRoute)
	}
	if generatedRoutePredecessorCommandCount(fixture.runner.commands[commandsBefore:], "reload") < 2 {
		t.Fatalf("failure did not execute and restore physical route: commands=%v", fixture.runner.commands[commandsBefore:])
	}
}

func TestGeneratedRoutePredecessorUnreadableHeadRefusesBeforePhysicalSwitch(t *testing.T) {
	fixture := newGeneratedRoutePredecessorFixture(t)
	capacityBefore := generatedRoutePredecessorCapacitySnapshot(t, fixture)
	effectsBefore := [2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)}
	mutated := false
	var mutationErr error
	fixture.runner.gatewayProbeMutate = func(*ingressRunner) {
		if mutated {
			return
		}
		mutated = true
		result, err := fixture.database().Exec(`DELETE FROM generated_runtime_active_heads WHERE app_id=?`, fixture.appID)
		mutationErr = err
		if err == nil {
			changed, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				mutationErr = rowsErr
			} else if changed != 1 {
				mutationErr = errors.New("active head delete did not reach one row")
			}
		}
	}
	commandsBefore := len(fixture.runner.commands)
	if result, err := fixture.successor.Activate(fixture.owner.validation.migration.guarded.context); err == nil ||
		result.DeploymentID != "" || !errors.Is(err, deploymentexecution.ErrIdentityLost) ||
		!errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unreadable-head activation result=%#v error=%v", result, err)
	}
	if !mutated || mutationErr != nil {
		t.Fatalf("unreadable-head mutation reached=%t error=%v", mutated, mutationErr)
	}
	observation, err := fixture.manager.Observe(context.Background(), fixture.appID)
	if err != nil || !sameRoute(fixture.predecessorRoute,
		routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		t.Fatalf("unreadable-head public observation=%#v error=%v", observation, err)
	}
	state, err := fixture.manager.store.loadRequired()
	if err != nil || state.Pending != nil || len(state.Active) != 1 ||
		!sameRoute(state.Active[fixture.appID], fixture.predecessorRoute) {
		t.Fatalf("unreadable-head protected state=%#v error=%v", state, err)
	}
	expected := expectedConfig(t, fixture.runner, map[string]routeRecord{fixture.appID: fixture.predecessorRoute})
	if !bytes.Equal(fixture.runner.liveConfig, expected) ||
		!bytes.Equal(fixture.runner.files[caddyContainerName+":/config/active.json"], expected) {
		t.Fatal("unreadable-head changed the physical predecessor before Begin")
	}
	if reloads := generatedRoutePredecessorCommandCount(fixture.runner.commands[commandsBefore:], "reload"); reloads != 0 {
		t.Fatalf("unreadable-head pre-Begin refusal reloads=%d commands=%v",
			reloads, fixture.runner.commands[commandsBefore:])
	}
	var headCount int
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads WHERE app_id=?`,
		fixture.appID).Scan(&headCount); err != nil || headCount != 0 {
		t.Fatalf("unreadable-head durable head count=%d error=%v", headCount, err)
	}
	var receiptCount int
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 {
		t.Fatalf("ambiguous activation receipt count=%d", receiptCount)
	}
	if after := generatedRoutePredecessorCapacitySnapshot(t, fixture); !reflect.DeepEqual(after, capacityBefore) ||
		[2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)} != effectsBefore {
		t.Fatalf("ambiguous activation changed capacity/effects: before=%#v after=%#v", capacityBefore, after)
	}
}

func TestGeneratedRoutePredecessorRollbackAuthorityHelperRefusesDurableDrift(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(*testing.T, *generatedRoutePredecessorFixture) (int64, error)
		verify      func(*testing.T, *generatedRoutePredecessorFixture)
		wantReadErr error
	}{
		{name: "crossed_attempt_after_pending_helper_input", mutate: func(t *testing.T, fixture *generatedRoutePredecessorFixture) (int64, error) {
			result, err := fixture.database().Exec(`UPDATE jobs SET attempt=attempt+1,updated_at=? WHERE id=?`,
				time.Now().UTC().Format(time.RFC3339Nano), fixture.owner.validation.migration.claim.JobID())
			if err != nil {
				return 0, err
			}
			return result.RowsAffected()
		}, verify: func(t *testing.T, fixture *generatedRoutePredecessorFixture) {
			var attempt int
			if err := fixture.database().QueryRow(`SELECT attempt FROM jobs WHERE id=?`,
				fixture.owner.validation.migration.claim.JobID()).Scan(&attempt); err != nil ||
				attempt != fixture.owner.validation.migration.claim.Attempt()+1 {
				t.Fatalf("crossed rollback helper attempt=%d error=%v", attempt, err)
			}
		}},
		{name: "durable_cancelling_after_pending_helper_input", mutate: func(t *testing.T, fixture *generatedRoutePredecessorFixture) (int64, error) {
			result, err := fixture.database().Exec(`UPDATE jobs SET phase='cancelling',updated_at=? WHERE id=?`,
				time.Now().UTC().Format(time.RFC3339Nano), fixture.owner.validation.migration.claim.JobID())
			if err != nil {
				return 0, err
			}
			return result.RowsAffected()
		}, verify: func(t *testing.T, fixture *generatedRoutePredecessorFixture) {
			var phase string
			if err := fixture.database().QueryRow(`SELECT phase FROM jobs WHERE id=?`,
				fixture.owner.validation.migration.claim.JobID()).Scan(&phase); err != nil || phase != "cancelling" {
				t.Fatalf("durable rollback helper phase=%q error=%v", phase, err)
			}
		}},
		{name: "missing_head_after_pending_helper_input", mutate: func(t *testing.T, fixture *generatedRoutePredecessorFixture) (int64, error) {
			result, err := fixture.database().Exec(`DELETE FROM generated_runtime_active_heads WHERE app_id=?`,
				fixture.appID)
			if err != nil {
				return 0, err
			}
			return result.RowsAffected()
		}, verify: func(t *testing.T, fixture *generatedRoutePredecessorFixture) {
			var count int
			if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_runtime_active_heads WHERE app_id=?`,
				fixture.appID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("missing-head rollback helper count=%d error=%v", count, err)
			}
		}, wantReadErr: sql.ErrNoRows},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGeneratedRoutePredecessorFixture(t)
			capacityBefore := generatedRoutePredecessorCapacitySnapshot(t, fixture)
			effectsBefore := [2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)}
			commandsBefore := len(fixture.runner.commands)
			state, done, transition := generatedRoutePredecessorBeginPending(t, fixture)
			defer done()
			mutationRows, mutationErr := test.mutate(t, fixture)
			if mutationErr != nil || mutationRows != 1 {
				t.Fatalf("%s mutation rows=%d error=%v", test.name, mutationRows, mutationErr)
			}
			test.verify(t, fixture)
			fixed := errors.New("fixed route activation SQL failure")
			err := fixture.successor.resolveActivationFailure(
				fixture.owner.validation.migration.guarded.context, state, transition, fixed)
			if !errors.Is(err, fixed) || !errors.Is(err, deploymentexecution.ErrIdentityLost) ||
				test.wantReadErr != nil && !errors.Is(err, test.wantReadErr) {
				t.Fatalf("%s rollback helper error=%v", test.name, err)
			}
			test.verify(t, fixture)
			if test.wantReadErr == nil {
				assertGeneratedRoutePredecessorRollbackRefused(t, fixture, commandsBefore,
					capacityBefore, effectsBefore)
			} else {
				assertGeneratedRoutePredecessorRollbackRefusedWithoutHead(t, fixture, commandsBefore,
					capacityBefore, effectsBefore)
			}
		})
	}
}

func TestGeneratedRoutePredecessorRollbackAuthorityHandlesCallerCancelAndCrossedBinding(t *testing.T) {
	t.Run("caller_only_cancellation_restores", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		capacityBefore := generatedRoutePredecessorCapacitySnapshot(t, fixture)
		effectsBefore := [2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)}
		commandsBefore := len(fixture.runner.commands)
		state, done, transition := generatedRoutePredecessorBeginPending(t, fixture)
		defer done()
		ctx, cancel := context.WithCancel(fixture.owner.validation.migration.guarded.context)
		cancel()
		fixed := errors.New("fixed activation SQL failure")
		if err := fixture.successor.resolveActivationFailure(ctx, state, transition, fixed); !errors.Is(err, fixed) ||
			errors.Is(err, context.Canceled) {
			t.Fatalf("caller-only cancellation rollback error=%v", err)
		}
		assertGeneratedRoutePredecessorRolledBack(t, fixture)
		observation, err := fixture.manager.Observe(context.Background(), fixture.appID)
		if err != nil || !sameRoute(fixture.predecessorRoute,
			routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
			t.Fatalf("caller-only cancellation restored route=%#v error=%v", observation, err)
		}
		if reloads := generatedRoutePredecessorCommandCount(fixture.runner.commands[commandsBefore:], "reload"); reloads != 4 {
			t.Fatalf("caller-only cancellation reloads=%d commands=%v", reloads, fixture.runner.commands[commandsBefore:])
		}
		if after := generatedRoutePredecessorCapacitySnapshot(t, fixture); !reflect.DeepEqual(after, capacityBefore) ||
			[2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)} != effectsBefore {
			t.Fatalf("caller-only cancellation changed capacity/effects: before=%#v after=%#v", capacityBefore, after)
		}
	})

	t.Run("crossed_binding_input_refuses_before_restore", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		capacityBefore := generatedRoutePredecessorCapacitySnapshot(t, fixture)
		effectsBefore := [2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)}
		commandsBefore := len(fixture.runner.commands)
		state, done, transition := generatedRoutePredecessorBeginPending(t, fixture)
		defer done()
		var crossedJobID string
		var crossedAttempt int
		if err := fixture.database().QueryRow(`SELECT deployment.job_id,job.attempt
			FROM deployments deployment JOIN jobs job ON job.id=deployment.job_id
			WHERE deployment.id=?`, fixture.predecessorDeploymentID).Scan(&crossedJobID, &crossedAttempt); err != nil {
			t.Fatal(err)
		}
		binding, err := fixture.owner.validation.migration.bindings.Read(context.Background(), crossedJobID)
		if err != nil {
			t.Fatal(err)
		}
		classification, err := fixture.owner.validation.migration.bindings.Check(context.Background(), binding)
		if err != nil || classification != deploymentbinding.ClassificationStale {
			t.Fatalf("crossed rollback binding classification=%d error=%v", classification, err)
		}
		crossedClaim, err := deploymentexecution.NewClaim(binding, crossedJobID, fixture.appID, crossedAttempt)
		if err != nil {
			t.Fatal(err)
		}
		crossedState := &generatedCandidateExecutionState{pinned: state.pinned, pins: state.pins}
		crossedState.pins.claim = crossedClaim
		fixed := errors.New("fixed activation SQL failure")
		if err := fixture.successor.resolveActivationFailure(context.Background(), crossedState, transition, fixed); !errors.Is(err, fixed) || !errors.Is(err, deploymentexecution.ErrAuthorityStale) {
			t.Fatalf("crossed rollback binding error=%v", err)
		}
		assertGeneratedRoutePredecessorRollbackRefused(t, fixture, commandsBefore,
			capacityBefore, effectsBefore)
	})
}

type generatedRoutePredecessorFixture struct {
	owner                        *candidateOwnerFixture
	manager                      *Manager
	runner                       *ingressRunner
	routeRunner                  *generatedRoutePredecessorRunner
	candidateRunner              *generatedRouteCandidateRunner
	successor                    *GeneratedRouteExecution
	appID                        string
	predecessorDeploymentID      string
	successorDeploymentID        string
	predecessorActivationReceipt string
	predecessorRoute             routeRecord
	successorRoute               routeRecord
}

type generatedRoutePredecessorCapacityState struct {
	group              *candidateCapacityGroup
	charge             *candidateCapacityCharge
	receipt            *candidateReceiptTombstone
	token              candidateCapacityChargeToken
	lease              *generatedcapacity.Lease
	epoch              uint64
	physical           candidatePhysicalState
	chargeState        candidateChargeState
	owner              candidateAccountingOwner
	refs               int
	receiptRetained    bool
	explicitRetained   bool
	logicallyRetired   bool
	tokenHeld          bool
	tokenConfigured    bool
	tokenPublished     bool
	capacityProbeCalls int
}

func generatedRoutePredecessorCapacitySnapshot(t *testing.T,
	fixture *generatedRoutePredecessorFixture,
) generatedRoutePredecessorCapacityState {
	t.Helper()
	ledger := fixture.owner.authority.ledger
	key := candidateCapacityGroupKey{appID: fixture.appID, deploymentID: fixture.successorDeploymentID}
	ledger.mu.Lock()
	group := ledger.groups[key]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatal("successor capacity group is missing")
	}
	group.mu.Lock()
	component := group.byName["api"]
	if component == nil || component.charge == nil || component.receipt == nil {
		group.mu.Unlock()
		t.Fatalf("successor capacity component=%#v", component)
	}
	charge := component.charge
	result := generatedRoutePredecessorCapacityState{group: group, charge: charge,
		receipt: component.receipt, token: charge.token, lease: charge.lease, epoch: charge.epoch,
		physical: candidatePhysicalFromWord(component.receipt.state.Load()), chargeState: charge.state,
		owner: charge.owner, refs: charge.refs, receiptRetained: charge.receiptRetained,
		explicitRetained: charge.explicitRetained, logicallyRetired: charge.logicallyRetired,
		tokenHeld: component.tokenHeld, tokenConfigured: candidateCapacityChargeTokenConfigured(charge.token),
		tokenPublished: candidateCapacityChargeTokenPublished(charge.token)}
	group.mu.Unlock()
	_, result.capacityProbeCalls = fixture.owner.source.observed()
	return result
}

func generatedRoutePredecessorBeginPending(t *testing.T, fixture *generatedRoutePredecessorFixture) (
	*generatedCandidateExecutionState, func(), *generatedRouteActivationGatewayTransition,
) {
	t.Helper()
	state, done, err := fixture.successor.candidate.begin(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.successor.checkRouteActivationAuthority(
		fixture.owner.validation.migration.guarded.context, state); err != nil {
		done()
		t.Fatal(err)
	}
	transition, err := fixture.manager.beginGeneratedDomainRouteActivation(
		fixture.owner.validation.migration.guarded.context, fixture.successor.request, nil)
	if err != nil {
		done()
		t.Fatal(err)
	}
	return state, done, transition
}

func assertGeneratedRoutePredecessorPendingPhysical(t *testing.T,
	fixture *generatedRoutePredecessorFixture, commandsBefore int,
) {
	t.Helper()
	state, err := fixture.manager.store.loadRequired()
	if err != nil || state.Pending == nil || state.Pending.AppID != fixture.appID ||
		state.Pending.Previous == nil || !sameRoute(*state.Pending.Previous, fixture.predecessorRoute) ||
		!sameRoute(state.Pending.Proposed, fixture.successorRoute) ||
		!sameRoute(state.Active[fixture.appID], fixture.predecessorRoute) {
		t.Fatalf("rollback refusal protected state=%#v error=%v", state, err)
	}
	expected := expectedConfig(t, fixture.runner, map[string]routeRecord{fixture.appID: fixture.successorRoute})
	if !bytes.Equal(fixture.runner.liveConfig, expected) ||
		!bytes.Equal(fixture.runner.files[caddyContainerName+":/config/active.json"], expected) {
		t.Fatal("rollback refusal did not retain the exact physical successor")
	}
	if reloads := generatedRoutePredecessorCommandCount(fixture.runner.commands[commandsBefore:], "reload"); reloads != 2 {
		t.Fatalf("rollback refusal reloads=%d commands=%v", reloads, fixture.runner.commands[commandsBefore:])
	}
}

func assertGeneratedRoutePredecessorRollbackRefused(t *testing.T, fixture *generatedRoutePredecessorFixture,
	commandsBefore int, capacityBefore generatedRoutePredecessorCapacityState, effectsBefore [2]int,
) {
	t.Helper()
	assertGeneratedRoutePredecessorRollbackRefusedWithoutHead(t, fixture, commandsBefore,
		capacityBefore, effectsBefore)
	head, err := generatedruntimestate.New(fixture.database()).Active(context.Background(), fixture.appID)
	if err != nil || head.DeploymentID != fixture.predecessorDeploymentID ||
		head.ActivationReceiptID != fixture.predecessorActivationReceipt {
		t.Fatalf("rollback refusal head=%#v error=%v", head, err)
	}
}

func assertGeneratedRoutePredecessorRollbackRefusedWithoutHead(t *testing.T,
	fixture *generatedRoutePredecessorFixture, commandsBefore int,
	capacityBefore generatedRoutePredecessorCapacityState, effectsBefore [2]int,
) {
	t.Helper()
	assertGeneratedRoutePredecessorPendingPhysical(t, fixture, commandsBefore)
	var receipts int
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	after := generatedRoutePredecessorCapacitySnapshot(t, fixture)
	effectsAfter := [2]int{fixture.owner.runner.count(candidateOwnerStop), fixture.owner.runner.count(candidateOwnerRemove)}
	if receipts != 1 || !reflect.DeepEqual(after, capacityBefore) || effectsAfter != effectsBefore {
		t.Fatalf("rollback refusal receipts=%d capacity before=%#v after=%#v effects=%v/%v",
			receipts, capacityBefore, after, effectsBefore, effectsAfter)
	}
}

func (f *generatedRoutePredecessorFixture) database() *sql.DB {
	return f.owner.validation.migration.repository.db
}

func newGeneratedRoutePredecessorFixture(t *testing.T) *generatedRoutePredecessorFixture {
	t.Helper()
	owner, candidateRunner, jobService := newGeneratedRouteSequentialCandidateOwner(t)
	predecessorExecution, predecessorIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)
	manager, runner, routeRunner := newGeneratedRoutePredecessorManagerFixture(t)
	appID := owner.validation.migration.guarded.appID
	runner.appID = appID
	runner.network = predecessorIdentity.NetworkName
	runner.endpoint = generatedRouteEndpoint(predecessorIdentity)
	routeRunner.endpoints[predecessorIdentity.ContainerID] = generatedRouteEndpointSnapshot{
		appID: appID, endpoint: generatedRouteEndpoint(predecessorIdentity), slot: generatedidentity.Slot(predecessorIdentity.Slot)}
	authority, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
	if err != nil {
		t.Fatalf("construct route authority: %v", err)
	}
	predecessor, err := authority.Pin(owner.validation.migration.guarded.context, predecessorExecution,
		generatedRouteRequest(appID, predecessorIdentity))
	if err != nil {
		t.Fatalf("pin predecessor route: %v", err)
	}
	if _, err := predecessor.Activate(owner.validation.migration.guarded.context); err != nil {
		t.Fatalf("activate predecessor route: %v", err)
	}
	predecessorHead, err := generatedruntimestate.New(owner.validation.migration.repository.db).
		Active(context.Background(), appID)
	if err != nil {
		t.Fatalf("read predecessor active head: %v", err)
	}
	predecessorObservation, err := manager.Observe(context.Background(), appID)
	if err != nil {
		t.Fatalf("observe predecessor route: %v", err)
	}
	completeGeneratedRouteSequentialCandidate(t, owner, predecessorHead)

	secondMigration := newGeneratedRouteSuccessorMigration(t, owner.validation.migration, jobService)
	owner.validation = newGeneratedRouteSuccessorValidation(t, secondMigration)
	owner.runner.containerID = strings.Repeat("8", 64)
	successorExecution, successorIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)
	successorRequest := generatedRouteRequest(appID, successorIdentity)
	successorRequest.FromSlot = predecessorObservation.Slot
	successor, err := authority.Pin(owner.validation.migration.guarded.context, successorExecution, successorRequest)
	if err != nil {
		t.Fatalf("pin successor route: %v", err)
	}
	if !sameActivationHeadForRouteAuthority(successor.expectedHead, predecessorHead) || len(successor.predecessorRoster) != 1 ||
		successor.predecessorRoster[0].DeploymentID != predecessorHead.DeploymentID {
		t.Fatalf("successor predecessor proof head=%#v roster=%#v", successor.expectedHead, successor.predecessorRoster)
	}
	runner.network = successorIdentity.NetworkName
	runner.endpoint = generatedRouteEndpoint(successorIdentity)
	routeRunner.slot = generatedidentity.Slot(successorIdentity.Slot)
	routeRunner.endpoints[successorIdentity.ContainerID] = generatedRouteEndpointSnapshot{
		appID: appID, endpoint: generatedRouteEndpoint(successorIdentity), slot: generatedidentity.Slot(successorIdentity.Slot)}
	return &generatedRoutePredecessorFixture{owner: owner, manager: manager, runner: runner,
		routeRunner: routeRunner, candidateRunner: candidateRunner, successor: successor,
		appID: appID, predecessorDeploymentID: predecessorHead.DeploymentID,
		successorDeploymentID:        secondMigration.request.DeploymentID,
		predecessorActivationReceipt: predecessorHead.ActivationReceiptID,
		predecessorRoute: routeRecord{Slot: predecessorObservation.Slot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), predecessorObservation.Endpoints...)},
		successorRoute: routeRecord{Slot: successorRequest.ToSlot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), successorRequest.Endpoints...)}}
}

func completeGeneratedRouteSequentialCandidate(t *testing.T, owner *candidateOwnerFixture,
	head generatedruntimestate.ActiveHead,
) {
	t.Helper()
	completeGeneratedRouteSequentialCandidateState(t, owner, head)
	establishGeneratedRoutePredecessorLifecycle(t, owner, head)
}

func completeGeneratedRouteSequentialCandidateState(t *testing.T, owner *candidateOwnerFixture,
	head generatedruntimestate.ActiveHead,
) {
	t.Helper()
	if owner == nil || owner.validation == nil || owner.validation.migration == nil ||
		head.AppID != owner.validation.migration.guarded.appID ||
		head.DeploymentID != owner.validation.request.DeploymentID {
		t.Fatal("route completion requires the exact activated candidate")
	}
	migration := owner.validation.migration
	if _, err := generatedruntimestate.New(migration.repository.db).Advance(context.Background(), head.AppID,
		head.DeploymentID, generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, ""); err != nil {
		t.Fatalf("complete predecessor runtime: %v", err)
	}
	if _, err := migration.repository.Transition(context.Background(), head.AppID,
		head.DeploymentID, deployments.Succeeded, ""); err != nil {
		t.Fatalf("complete predecessor deployment: %v", err)
	}
	finishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := migration.repository.db.Exec(`UPDATE jobs
		SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=?
		WHERE id=? AND status='running'`, finishedAt, finishedAt, migration.claim.JobID())
	if err != nil {
		t.Fatalf("mark predecessor deployment job complete: %v", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("complete predecessor deployment job changed=%d error=%v", changed, err)
	}
}

type generatedRouteLifecycleEffectSpec struct {
	boundary  applifecycle.Boundary
	component string
	mode      applifecycle.EffectMode
	outcome   applifecycle.Outcome
}

func establishGeneratedRoutePredecessorLifecycle(t *testing.T, owner *candidateOwnerFixture,
	head generatedruntimestate.ActiveHead,
) {
	t.Helper()
	if owner == nil || owner.validation == nil || owner.validation.migration == nil {
		t.Fatal("predecessor lifecycle requires an exact generated candidate owner")
	}
	migration := owner.validation.migration
	database := migration.repository.db
	repository := applifecycle.New(database)
	service := jobs.New(database)
	configuration, err := migration.configuration.Get(context.Background(), migration.guarded.appID)
	if err != nil {
		t.Fatal(err)
	}
	physical := owner.runner.snapshot()
	policy := physical.policy
	if !physical.exists || !physical.running || physical.containerID == "" ||
		policy.identity.DeploymentID != head.DeploymentID || policy.identity.Slot != head.Slot {
		t.Fatalf("predecessor retained physical=%#v head=%#v", physical, head)
	}
	technology := policy.identity.Technology
	if technology == "" && len(migration.plan.Components) == 1 &&
		migration.plan.Components[0].Name == policy.identity.Component && migration.plan.Components[0].NodeVersion != "" {
		// Production setup derives this from the plan component's explicit
		// Technology. The shared generated-migration fixture predates that field;
		// its non-empty Node version is the fixture's existing Node contract.
		// Every physical identity field below remains receipt-derived.
		technology = "node"
	}
	pins := applifecycle.GeneratedLifecyclePinsV1{
		Version: 1, DeploymentID: head.DeploymentID, ReleaseID: policy.identity.ReleaseID,
		Slot: head.Slot, ActiveGeneration: head.Generation,
		PlanRevision: applifecycle.RevisionRef{ID: migration.request.DeploymentPlanRevisionID,
			Number: migration.request.DeploymentPlanRevisionNumber},
		Configuration: applifecycle.ConfigurationRef{RevisionID: migration.request.ConfigurationRevisionID,
			RevisionNumber: migration.request.ConfigurationRevisionNumber, FormatVersion: configuration.FormatVersion,
			PlanRevision: applifecycle.RevisionRef{ID: migration.request.DeploymentPlanRevisionID,
				Number: migration.request.DeploymentPlanRevisionNumber}, RuntimeComponents: []string{policy.identity.Component}},
		BaselineDisposition: applifecycle.BaselineRunning,
		Components: []applifecycle.RetainedComponent{{
			Name: policy.identity.Component, Role: policy.identity.Role, Technology: technology,
			ContainerID: physical.containerID, ContainerName: policy.identity.ContainerName,
			ArtifactID: policy.identity.ArtifactID, ImageContentID: policy.identity.ImageContentID,
			BuildDefinitionDigest: policy.buildDefinitionDigest, NetworkName: policy.identity.NetworkName,
			NetworkAlias: policy.identity.NetworkAlias, InternalPort: policy.internalPort,
			WorkingDirectory: policy.identity.WorkingDirectory, RunCommandDigest: policy.identity.RunCommandDigest,
			Limits: applifecycle.RetainedLimits{MemoryBytes: policy.limits.MemoryBytes,
				MemorySwapBytes: policy.limits.MemoryBytes, CPUQuota: policy.limits.MilliCPUs * 100,
				CPUPeriod: 100000, PIDsLimit: policy.limits.PIDs, TmpfsBytes: policy.limits.TmpfsBytes,
				LogDriver: "local", LogMaxSize: policy.limits.LogSize, LogMaxFiles: policy.limits.LogFiles,
				HardeningPolicyVersion: "v1"},
		}},
		Route: applifecycle.AppRouteAnchorV1{SelectedLineageKind: "gateway_rebind",
			SelectedLineageGeneration: 1, SelectedLineageOperationID: uuid.NewString(),
			SelectedLineageTerminalDigest: strings.Repeat("f", 64), ProtectedFormatVersion: 2,
			TargetRecordDigest: strings.Repeat("a", 64)},
	}
	accepted, err := repository.Accept(context.Background(), service, applifecycle.LifecycleRequest{
		ApplicationID: migration.guarded.appID, ActorID: migration.guarded.actorID,
		IdempotencyKey: "route-predecessor-authority-" + uuid.NewString(), Action: applifecycle.Start,
		ExpectedDesiredRevision: 0,
	}, pins)
	if err != nil {
		t.Fatalf("accept predecessor lifecycle: %v pins=%#v physical=%#v configuration=%#v", err, pins, physical, configuration)
	}
	if !accepted.Created {
		t.Fatal("predecessor lifecycle operation was not newly accepted")
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := database.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=?
		WHERE id=? AND status='queued'`, startedAt, startedAt, accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("start predecessor lifecycle job changed=%d error=%v", changed, err)
	}
	job, err := service.Get(accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := applifecycle.WorkerClaimFor(job)
	if err != nil {
		t.Fatal(err)
	}
	specs := []generatedRouteLifecycleEffectSpec{
		{boundary: applifecycle.BoundaryHealth, mode: applifecycle.EffectObservationOnly,
			outcome: applifecycle.OutcomeHealthy},
		{boundary: applifecycle.BoundaryPublish, mode: applifecycle.EffectCommand,
			outcome: applifecycle.OutcomeServing},
		{boundary: applifecycle.BoundaryComplete, mode: applifecycle.EffectObservationOnly,
			outcome: applifecycle.OutcomeCompleteRunning},
	}
	for _, spec := range specs {
		tx, err := database.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		request := applifecycle.EffectRequestV1{Version: 1, OperationID: accepted.Operation.JobID,
			ApplicationID: accepted.Operation.ApplicationID, DesiredRevision: accepted.Operation.DesiredRevision,
			Boundary: spec.boundary, ComponentName: spec.component, Mode: spec.mode,
			PinsDigest: accepted.Operation.PinsDigest}
		intent, created, err := repository.BeginBoundaryTx(context.Background(), tx, claim, accepted.Operation, request)
		if err == nil && !created {
			err = errors.New("predecessor lifecycle intent was not newly retained")
		}
		if err == nil {
			proof := applifecycle.EffectProofV1{Version: 1, OperationID: accepted.Operation.JobID,
				ApplicationID: accepted.Operation.ApplicationID, DesiredRevision: accepted.Operation.DesiredRevision,
				Boundary: spec.boundary, ComponentName: spec.component, Outcome: spec.outcome,
				IntentDigest: intent.RequestDigest, EvidenceDigest: strings.Repeat("e", 64),
				PinsDigest: accepted.Operation.PinsDigest}
			var observed bool
			var observeErr error
			if spec.boundary == applifecycle.BoundaryComplete {
				_, observed, observeErr = repository.CompleteTx(context.Background(), tx, claim, intent, proof)
			} else {
				_, observed, observeErr = repository.ObserveBoundaryTx(context.Background(), tx, claim, intent, proof)
			}
			if observeErr != nil {
				err = observeErr
			} else if !observed {
				err = errors.New("predecessor lifecycle receipt was not newly retained")
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			t.Fatalf("complete predecessor lifecycle %s/%s: %v", spec.boundary, spec.component, err)
		}
	}
	finishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err = database.Exec(`UPDATE jobs
		SET status='succeeded',phase='succeeded',progress_percent=100,finished_at=?,updated_at=?
		WHERE id=? AND status='running'`, finishedAt, finishedAt, accepted.Operation.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("complete predecessor lifecycle job changed=%d error=%v", changed, err)
	}
	validator, err := applifecycle.NewDeploymentBindingValidator(repository)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	completion, err := validator.CurrentOperationTx(context.Background(), tx, deploymentbinding.OperationRef{
		ApplicationID: accepted.Operation.ApplicationID, OperationID: accepted.Operation.JobID,
		DesiredRevision: accepted.Operation.DesiredRevision,
	})
	if err != nil || completion != deploymentbinding.CompletionComplete {
		t.Fatalf("predecessor lifecycle completion=%v error=%v", completion, err)
	}
	unresolved, err := validator.UnresolvedHistoryTx(context.Background(), tx, accepted.Operation.ApplicationID)
	if err != nil || unresolved {
		t.Fatalf("predecessor lifecycle unresolved=%t error=%v", unresolved, err)
	}
	var triggerAuthority int
	if err := tx.QueryRow(`SELECT EXISTS(
		SELECT 1
		FROM applications a
		JOIN application_desired_lifecycle_heads h ON h.app_id=a.id
		JOIN application_lifecycle_operations o ON o.app_id=h.app_id AND o.desired_revision=h.revision_number
		JOIN application_desired_lifecycle_transitions tr ON tr.app_id=o.app_id
		 AND tr.revision_number=o.desired_revision AND tr.job_id=o.job_id AND tr.action=o.action
		JOIN jobs lifecycle_job ON lifecycle_job.id=o.job_id
		JOIN application_lifecycle_effect_receipts completion ON completion.job_id=o.job_id
		 AND completion.boundary='complete' AND completion.component_name='' AND completion.outcome='complete_running'
		WHERE a.id=? AND h.desired_state='running' AND h.revision_number=? AND h.updated_at IS NOT NULL
		 AND o.job_id=? AND o.action IN ('start','restart') AND tr.expected_revision=o.expected_revision
		 AND tr.desired_state='running' AND tr.actor_id=o.actor_id AND lifecycle_job.type=o.action
		 AND lifecycle_job.resource_type='application' AND lifecycle_job.resource_id=o.app_id
		 AND lifecycle_job.requested_by=o.actor_id
	)`, accepted.Operation.ApplicationID, accepted.Operation.DesiredRevision, accepted.Operation.JobID).Scan(&triggerAuthority); err != nil {
		t.Fatalf("read predecessor lifecycle SQL binding authority: %v", err)
	}
	if triggerAuthority != 1 {
		t.Fatalf("predecessor lifecycle lacks exact SQL binding authority: operation=%#v", accepted.Operation)
	}
}

type generatedRoutePredecessorRunner struct {
	*ingressRunner
	slot      generatedidentity.Slot
	endpoints map[string]generatedRouteEndpointSnapshot
}

type generatedRouteEndpointSnapshot struct {
	appID    string
	endpoint generatedroute.RouteEndpoint
	slot     generatedidentity.Slot
}

func (r *generatedRoutePredecessorRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) >= 3 && args[0] == "network" && args[1] == "inspect" {
		target := args[len(args)-1]
		for _, snapshot := range r.endpoints {
			if target == snapshot.endpoint.NetworkName {
				r.commands = append(r.commands, append([]string(nil), args...))
				return jsonResult(networkInspection{Name: target, Driver: "bridge", Scope: "local",
					Options: map[string]string{}, Labels: map[string]string{
						"io.rig.managed":     generatedidentity.NetworkOwnershipLabelValue,
						"io.rig.application": snapshot.appID,
					}}), nil
			}
		}
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "inspect" {
		snapshot, exists := r.endpoints[args[len(args)-1]]
		if !exists {
			return r.ingressRunner.Run(ctx, request)
		}
		r.commands = append(r.commands, append([]string(nil), args...))
		role := r.endpointRoleLabel
		if role == "" {
			role = snapshot.endpoint.Role
		}
		appID := snapshot.appID
		if appID == "" {
			appID = r.appID
		}
		return jsonResult(endpointInspection{ID: snapshot.endpoint.ContainerID, Labels: map[string]string{
			"io.rig.managed": "generated-runtime", "io.rig.application": appID,
			"io.rig.component": snapshot.endpoint.Component, "io.rig.slot": string(snapshot.slot), "io.rig.role": role,
		}, Running: true, Health: "healthy",
			Networks: map[string]*networkAttachment{snapshot.endpoint.NetworkName: {
				Aliases: []string{snapshot.endpoint.NetworkAlias}}}}), nil
	}
	return r.ingressRunner.Run(ctx, request)
}

func newGeneratedRoutePredecessorManagerFixture(t *testing.T) (*Manager, *ingressRunner,
	*generatedRoutePredecessorRunner,
) {
	t.Helper()
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	appID := "11111111-1111-4111-8111-111111111111"
	initial := endpoint("web", "server", "rig-app-network", "web-blue", 3000, 'b')
	_, _, ingressIP := ingressNetworkCandidate(0)
	runner := &ingressRunner{appID: appID, network: initial.NetworkName, endpoint: initial,
		caddyNetworks: map[string]*networkAttachment{caddyNetworkName: {
			GwPriority: caddyGatewayPriority, IPAddress: ingressIP}},
		ingressContainers: map[string]caddyNetworkContainerInspection{strings.Repeat("d", 64): {
			Name: caddyContainerName, IPv4Address: ingressIP + "/28"}},
		files: map[string][]byte{}}
	routeRunner := &generatedRoutePredecessorRunner{ingressRunner: runner, slot: generatedidentity.SlotBlue,
		endpoints: make(map[string]generatedRouteEndpointSnapshot)}
	reader := &generatedRouteColdAccountingRouteReadRunner{routes: routeRunner, delegate: routeRunner}
	manager, err := New(reader, Options{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, DataRoot: root,
		RebindFenceCheck:             func(context.Context) error { return nil },
		RebindCurrentStateRepository: gatewayCurrentSelectionRepositoryFunc(emptyStartupPresenceRepository),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, runner, routeRunner
}

type generatedRouteCandidateRunner struct {
	delegate      *candidateOwnerRunner
	physical      map[string]candidateOwnerPhysical
	beforeInspect func(string)
}

func (r *generatedRouteCandidateRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	kind := candidateOwnerKind(request)
	if kind == candidateOwnerInspect {
		target := request.Args[len(request.Args)-1]
		if r.beforeInspect != nil {
			r.beforeInspect(target)
		}
		for _, physical := range r.physical {
			if target == physical.containerID || target == physical.policy.identity.ContainerName {
				r.delegate.mu.Lock()
				r.delegate.requestsLog = append(r.delegate.requestsLog, copyCandidateOwnerRequest(request))
				r.delegate.mu.Unlock()
				if !physical.exists {
					return r.delegate.absentResult(target)
				}
				return r.delegate.inspectResult(candidateOwnerPhysicalInspection(physical)), nil
			}
		}
		return r.delegate.absentResult(target)
	}
	if kind == candidateOwnerImageInspect || kind == candidateOwnerNetworkInspect {
		bound := r.delegate.snapshot()
		target := request.Args[len(request.Args)-1]
		for _, physical := range r.physical {
			matches := kind == candidateOwnerImageInspect && target == physical.policy.identity.ImageContentID ||
				kind == candidateOwnerNetworkInspect && target == physical.policy.identity.NetworkName
			if matches {
				r.delegate.setPhysical(physical)
				break
			}
		}
		result, err := r.delegate.Run(ctx, request)
		r.delegate.setPhysical(bound)
		return result, err
	}
	if kind == candidateOwnerStart || kind == candidateOwnerStop || kind == candidateOwnerRemove {
		target := request.Args[len(request.Args)-1]
		if physical, exists := r.physical[target]; exists {
			r.delegate.setPhysical(physical)
		}
	}
	result, err := r.delegate.Run(ctx, request)
	if err == nil && (kind == candidateOwnerCreate || kind == candidateOwnerStart ||
		kind == candidateOwnerStop || kind == candidateOwnerRemove) {
		physical := r.delegate.snapshot()
		r.physical[physical.containerID] = physical
	}
	return result, err
}

func newGeneratedRouteSequentialCandidateOwner(t *testing.T) (*candidateOwnerFixture,
	*generatedRouteCandidateRunner, *jobs.Service,
) {
	t.Helper()
	migration, jobService := newGeneratedRouteCanonicalMigration(t)
	owner, runner := newGeneratedRouteSequentialCandidateOwnerForMigration(t, migration)
	return owner, runner, jobService
}

func newGeneratedRouteSequentialCandidateOwnerForMigration(t *testing.T,
	migration *generatedMigrationFixture,
) (*candidateOwnerFixture, *generatedRouteCandidateRunner) {
	return newGeneratedRouteSequentialCandidateOwnerForMigrationWithContainerID(t, migration,
		strings.Repeat("7", 64))
}

func newGeneratedRouteSequentialCandidateOwnerForMigrationWithContainerID(t *testing.T,
	migration *generatedMigrationFixture, containerID string,
) (*candidateOwnerFixture, *generatedRouteCandidateRunner) {
	t.Helper()
	if migration == nil || !validContainerID(containerID) {
		t.Fatal("sequential candidate owner requires a canonical migration")
	}
	validation := newGeneratedRouteSuccessorValidation(t, migration)
	options := GeneratedCandidateProcessOptions{
		DockerExecutable: filepath.Join(t.TempDir(), "docker"), DockerConfigDirectory: t.TempDir(),
		WorkingDirectory: t.TempDir(), CommandTimeout: time.Minute, HealthTimeout: time.Minute,
		HealthPollInterval: 100 * time.Millisecond, OutputLimit: 16 << 10,
		MemoryBytes: 128 << 20, MilliCPUs: 500, PIDs: 64, TmpfsBytes: 8 << 20,
		LogSize: "1m", LogFiles: 2, ReplacementDiskBytes: 128 << 20,
	}
	source := &candidateOwnerCapacitySource{value: generatedcapacity.Snapshot{
		MemoryAvailableBytes: 2 * uint64(options.MemoryBytes), DiskAvailableBytes: 2 * options.ReplacementDiskBytes}}
	budget, err := generatedcapacity.New(source)
	if err != nil {
		t.Fatalf("construct sequential candidate budget: %v", err)
	}
	base := &candidateOwnerRunner{containerID: containerID,
		absenceErr: generatedMigrationCompletedExitOne(t),
		before:     make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest)),
		results:    make(map[candidateOwnerCommandKind]func(runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error))}
	runner := &generatedRouteCandidateRunner{delegate: base, physical: make(map[string]candidateOwnerPhysical)}
	environment := &candidateOwnerEnvironment{path: filepath.Join(t.TempDir(), "candidate.env")}
	authority, err := validation.migration.repository.GeneratedCandidateAuthority(runner, environment, budget, options)
	if err != nil {
		t.Fatalf("construct sequential candidate authority: %v", err)
	}
	validation.authority = authority
	return &candidateOwnerFixture{validation: validation, authority: authority, runner: base,
		environment: environment, source: source, budget: budget, options: options}, runner
}

func newGeneratedRouteCanonicalMigration(t *testing.T) (*generatedMigrationFixture, *jobs.Service) {
	t.Helper()
	dataRoot := filepath.Join(t.TempDir(), "state")
	db, err := databasefixture.Open(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newGeneratedRouteCanonicalMigrationOnDatabase(t, db, dataRoot, directories.WorkingDirectory)
}

func newGeneratedRouteCanonicalMigrationOnDatabase(t *testing.T, db *sql.DB, dataRoot,
	workingDirectory string,
) (*generatedMigrationFixture, *jobs.Service) {
	t.Helper()
	if db == nil || dataRoot == "" || workingDirectory == "" {
		t.Fatal("canonical route migration dependencies are incomplete")
	}
	lifecycle := applifecycle.New(db)
	validator, err := applifecycle.NewDeploymentBindingValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := deploymentplans.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := appconfig.New(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuardAndGeneratedCandidates(db, guard, plans, configuration)
	if err != nil {
		t.Fatal(err)
	}
	stores, err := repository.GeneratedOwnerStores()
	if err != nil {
		t.Fatal(err)
	}
	migration := newGeneratedMigrationFixtureWithDependencies(t, db, jobService, bindings, repository, stores,
		plans, configuration, workingDirectory, []string{"api"}, []string{"api"})
	return migration, jobService
}

func prepareGeneratedRouteSequentialCandidate(t *testing.T,
	owner *candidateOwnerFixture,
) (*GeneratedCandidateExecution, CandidateIdentity) {
	t.Helper()
	reservation := owner.reserve(t)
	t.Cleanup(reservation.Release)
	execution := owner.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := owner.beginCreate(t, execution)
	owner.bindInvocation(t, invocation)
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatalf("create sequential route candidate: %v", err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatalf("start sequential route candidate: %v", err)
	}
	owner.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatalf("observe sequential route candidate health: %v", err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatalf("finish sequential route candidate health: %v", err)
	}
	return execution, identity
}

func newGeneratedRouteSuccessorValidation(t *testing.T,
	migration *generatedMigrationFixture,
) *candidateValidationFixture {
	t.Helper()
	state := generatedruntimestate.New(migration.repository.db)
	if _, err := state.BeginMigration(context.Background(), migration.claim.ApplicationID(),
		migration.request.DeploymentID); err != nil {
		t.Fatalf("begin successor migration effect: %v", err)
	}
	if _, err := state.FinishMigration(context.Background(), migration.claim.ApplicationID(),
		migration.request.DeploymentID, true); err != nil {
		t.Fatalf("finish successor migration effect: %v", err)
	}
	deployment, err := state.Advance(context.Background(), migration.claim.ApplicationID(),
		migration.request.DeploymentID, generatedruntimestate.PhaseMigrating,
		generatedruntimestate.PhaseStartingCandidate, "")
	if err != nil {
		t.Fatalf("advance successor to candidate start: %v", err)
	}
	request := GeneratedCandidateRequest{Claim: migration.claim, DeploymentID: migration.request.DeploymentID,
		ReleaseID:                    migration.request.ReleaseID,
		ConfigurationRevisionID:      migration.request.ConfigurationRevisionID,
		ConfigurationRevisionNumber:  migration.request.ConfigurationRevisionNumber,
		DeploymentPlanRevisionID:     migration.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: migration.request.DeploymentPlanRevisionNumber,
		CandidateSlot:                deployment.CandidateSlot}
	pins, err := candidatePinsFromRequest(request)
	if err != nil {
		t.Fatalf("derive successor candidate pins: %v", err)
	}
	loader := &GeneratedCandidateAuthority{stores: migration.repository.stores}
	protected, err := loader.loadCandidateProtectedSnapshot(context.Background(), pins)
	if err != nil {
		t.Fatalf("load successor candidate protected snapshot: %v", err)
	}
	return &candidateValidationFixture{migration: migration, authority: loader, state: state,
		request: request, pins: pins, protected: protected}
}

func newGeneratedRouteSuccessorMigration(t *testing.T, first *generatedMigrationFixture,
	jobService *jobs.Service,
) *generatedMigrationFixture {
	t.Helper()
	database := first.repository.db
	if jobService == nil {
		t.Fatal("successor migration requires the canonical job service")
	}
	created, err := jobService.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: first.guarded.appID, IdempotencyKey: "route-successor-" + uuid.NewString(),
		RequestedBy: first.guarded.actorID, Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create successor deployment job: created=%t error=%v", created.Created, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`UPDATE jobs SET status='running',phase='running',attempt=2,started_at=?,updated_at=? WHERE id=?`,
		now, now, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	deployment, _, err := first.repository.GetOrCreateByJob(context.Background(), first.guarded.appID,
		created.Job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = first.repository.InitializeRuntime(context.Background(), first.guarded.appID, deployment.ID,
		first.request.ReleaseID, first.request.ConfigurationRevisionID, first.request.ConfigurationRevisionNumber,
		deployments.RuntimeGeneratedNode, first.request.DeploymentPlanRevisionID, first.request.DeploymentPlanRevisionNumber)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, created.Job.ID, first.guarded.appID, 2)
	if err != nil {
		t.Fatalf("construct successor deployment claim: %v", err)
	}
	if err := first.repository.Gate(first.guarded.context, deployments.GateRequest{Claim: claim, DeploymentID: deployment.ID}); err != nil {
		t.Fatalf("gate successor deployment: %v", err)
	}
	state := generatedruntimestate.New(database)
	runtimeDeployment, wasCreated, err := state.Begin(context.Background(), generatedruntimestate.BeginInput{
		DeploymentID: deployment.ID, AppID: first.guarded.appID, ReleaseID: first.request.ReleaseID,
		DeploymentPlanRevisionID:     first.request.DeploymentPlanRevisionID,
		DeploymentPlanRevisionNumber: first.request.DeploymentPlanRevisionNumber, ComponentNames: []string{"api"},
	})
	if err != nil || !wasCreated {
		t.Fatalf("begin successor runtime: created=%t error=%v", wasCreated, err)
	}
	runtimeDeployment, err = state.Advance(context.Background(), first.guarded.appID, deployment.ID,
		runtimeDeployment.Phase, generatedruntimestate.PhaseBuilding, "")
	if err != nil {
		t.Fatalf("advance successor runtime to building: %v", err)
	}
	if _, err := state.SetImageReady(context.Background(), first.guarded.appID, deployment.ID, "api", first.request.ArtifactID); err != nil {
		t.Fatalf("record successor image: %v", err)
	}
	if _, err := state.Advance(context.Background(), first.guarded.appID, deployment.ID,
		runtimeDeployment.Phase, generatedruntimestate.PhaseMigrating, ""); err != nil {
		t.Fatalf("advance successor runtime to migrating: %v", err)
	}
	return &generatedMigrationFixture{repository: first.repository, bindings: first.bindings, jobService: jobService, configuration: first.configuration,
		guarded: first.guarded, releaseEffects: first.releaseEffects, plan: first.plan, claim: claim, binding: created.Binding,
		request: candidateMigrationRequestFixture{DeploymentID: deployment.ID, ReleaseID: first.request.ReleaseID,
			ConfigurationRevisionID:      first.request.ConfigurationRevisionID,
			ConfigurationRevisionNumber:  first.request.ConfigurationRevisionNumber,
			DeploymentPlanRevisionID:     first.request.DeploymentPlanRevisionID,
			DeploymentPlanRevisionNumber: first.request.DeploymentPlanRevisionNumber,
			ArtifactID:                   first.request.ArtifactID, ArtifactAttemptNumber: first.request.ArtifactAttemptNumber}}
}

func assertGeneratedRoutePredecessorRolledBack(t *testing.T, fixture *generatedRoutePredecessorFixture) {
	t.Helper()
	database := fixture.owner.validation.migration.repository.db
	head, err := generatedruntimestate.New(database).Active(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts, predecessorActive, successorHealthy int
	var successorPhase generatedruntimestate.Phase
	queries := []struct {
		query string
		args  []any
		into  any
	}{
		{`SELECT COUNT(*) FROM generated_route_activations`, nil, &receipts},
		{`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='active'`, []any{fixture.predecessorDeploymentID}, &predecessorActive},
		{`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='healthy'`, []any{fixture.successorDeploymentID}, &successorHealthy},
		{`SELECT phase FROM generated_runtime_deployments WHERE deployment_id=?`, []any{fixture.successorDeploymentID}, &successorPhase},
	}
	for _, query := range queries {
		if err := database.QueryRow(query.query, query.args...).Scan(query.into); err != nil {
			t.Fatal(err)
		}
	}
	if receipts != 1 || head.DeploymentID != fixture.predecessorDeploymentID ||
		head.ActivationReceiptID != fixture.predecessorActivationReceipt || predecessorActive != 1 ||
		successorHealthy != 1 || successorPhase != generatedruntimestate.PhaseSwitchingRoute {
		t.Fatalf("rollback receipts=%d head=%#v predecessor-active=%d successor-healthy=%d phase=%q",
			receipts, head, predecessorActive, successorHealthy, successorPhase)
	}
}

func assertGeneratedRoutePredecessorCommitted(t *testing.T, fixture *generatedRoutePredecessorFixture) {
	t.Helper()
	database := fixture.owner.validation.migration.repository.db
	head, err := generatedruntimestate.New(database).Active(context.Background(), fixture.appID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts, predecessorDraining, successorActive int
	if err := database.QueryRow(`SELECT COUNT(*) FROM generated_route_activations`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='draining'`,
		fixture.predecessorDeploymentID).Scan(&predecessorDraining); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components WHERE deployment_id=? AND state='active'`,
		fixture.successorDeploymentID).Scan(&successorActive); err != nil {
		t.Fatal(err)
	}
	if receipts != 2 || predecessorDraining != 1 || successorActive != 1 ||
		head.DeploymentID != fixture.successorDeploymentID || head.Generation != 2 || head.ActivationReceiptID == "" {
		t.Fatalf("commit receipts=%d predecessor-draining=%d successor-active=%d head=%#v",
			receipts, predecessorDraining, successorActive, head)
	}
}

func generatedRoutePredecessorCommandCount(commands [][]string, argument string) int {
	count := 0
	for _, command := range commands {
		for _, value := range command {
			if value == argument {
				count++
				break
			}
		}
	}
	return count
}
