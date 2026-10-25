package generatedingress

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	controldatabase "github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedidentity"
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"modernc.org/sqlite"
)

func TestGeneratedRouteAccountingHandoffSuccess(t *testing.T) {
	fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
	predecessorCharge, predecessorLease, _, _ := generatedRouteAccountingCharge(
		t, fixture, fixture.predecessorDeploymentID, "api")
	successorCharge, successorLease, successorEpoch, _ := generatedRouteAccountingCharge(
		t, fixture, fixture.successorDeploymentID, "api")

	receipt, err := retirement.completeAccountingHandoff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ActivationReceiptID == "" || receipt.AppID != fixture.appID ||
		receipt.SuccessorDeploymentID != fixture.successorDeploymentID ||
		receipt.PredecessorDeploymentID != fixture.predecessorDeploymentID ||
		receipt.SuccessorComponentCount != 1 || receipt.PredecessorComponentCount != 1 ||
		receipt.HandedOffAt.IsZero() {
		t.Fatalf("handoff receipt = %#v", receipt)
	}
	if replay, err := retirement.completeAccountingHandoff(context.Background()); err != nil || replay != receipt {
		t.Fatalf("handoff replay = %#v error=%v", replay, err)
	}
	if successorCharge.lease != successorLease || successorCharge.epoch != successorEpoch ||
		successorCharge.owner != candidateAccountingRouteActive || successorCharge.logicallyRetired {
		t.Fatalf("route-active successor = %+v", successorCharge)
	}
	if predecessorCharge.lease != nil || predecessorCharge.state != candidateChargeReleased ||
		predecessorCharge.owner != candidateAccountingPredecessorRetired || !predecessorCharge.logicallyRetired {
		t.Fatalf("released predecessor = %+v originalLease=%p", predecessorCharge, predecessorLease)
	}
	assertGeneratedRouteAccountingRows(t, fixture, 1, 1)
}

func TestGeneratedRouteAccountingHandoffRefusesBeforeUnlock(t *testing.T) {
	t.Run("missing_retirement_aggregate", func(t *testing.T) {
		fixture := newGeneratedRoutePredecessorFixture(t)
		_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
			fixture.owner.validation.migration.guarded.context)
		if err != nil {
			t.Fatal(err)
		}
		if receipt, err := retirement.completeAccountingHandoff(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) || receipt.ActivationReceiptID != "" {
			t.Fatalf("missing aggregate receipt=%#v error=%v", receipt, err)
		}
		assertGeneratedRouteAccountingRows(t, fixture, 0, 0)
	})

	t.Run("pending_route_graph", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		state, err := fixture.manager.store.load()
		if err != nil {
			t.Fatal(err)
		}
		previous := state.Active[fixture.appID]
		state.Pending = &pendingRoute{AppID: fixture.appID, Previous: &previous, Proposed: fixture.predecessorRoute}
		if err := fixture.manager.store.save(state); err != nil {
			t.Fatal(err)
		}
		predecessorCharge, predecessorLease, _, _ := generatedRouteAccountingCharge(
			t, fixture, fixture.predecessorDeploymentID, "api")
		if receipt, err := retirement.completeAccountingHandoff(context.Background()); err == nil || receipt.ActivationReceiptID != "" {
			t.Fatalf("pending graph receipt=%#v error=%v", receipt, err)
		}
		if predecessorCharge.lease != predecessorLease || predecessorCharge.state != candidateChargeActive ||
			predecessorCharge.owner != candidateAccountingCandidate || predecessorCharge.logicallyRetired {
			t.Fatalf("pending graph changed predecessor = %+v", predecessorCharge)
		}
		assertGeneratedRouteAccountingRows(t, fixture, 0, 0)
	})

	t.Run("first_gateway_unlock_failure", func(t *testing.T) {
		fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
		fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
		original := managerAcquireGatewayOSLock
		managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
			release, err := original(ctx, store)
			if err != nil {
				return nil, err
			}
			return func() error {
				_ = release()
				return errors.New("injected first gateway unlock failure")
			}, nil
		}
		t.Cleanup(func() { managerAcquireGatewayOSLock = original })
		predecessorCharge, predecessorLease, _, _ := generatedRouteAccountingCharge(
			t, fixture, fixture.predecessorDeploymentID, "api")
		if receipt, err := retirement.completeAccountingHandoff(context.Background()); err == nil || receipt.ActivationReceiptID != "" || !fixture.manager.gatewayRebindAdmissionBlocked() {
			t.Fatalf("first unlock receipt=%#v failStop=%t error=%v",
				receipt, fixture.manager.gatewayRebindAdmissionBlocked(), err)
		}
		if predecessorCharge.lease != predecessorLease || predecessorCharge.state != candidateChargeActive ||
			predecessorCharge.owner != candidateAccountingPredecessorRetiring || predecessorCharge.logicallyRetired {
			t.Fatalf("first unlock failure changed predecessor = %+v", predecessorCharge)
		}
		assertGeneratedRouteAccountingRows(t, fixture, 1, 0)
	})
}

func TestGeneratedRouteAccountingHandoffFinalUnlockFailureIsTerminal(t *testing.T) {
	fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	original := managerAcquireGatewayOSLock
	acquisitions := 0
	managerAcquireGatewayOSLock = func(ctx context.Context, store *stateStore) (func() error, error) {
		release, err := original(ctx, store)
		if err != nil {
			return nil, err
		}
		acquisitions++
		current := acquisitions
		return func() error {
			_ = release()
			if current == 2 {
				return errors.New("injected final gateway unlock failure")
			}
			return nil
		}, nil
	}
	t.Cleanup(func() { managerAcquireGatewayOSLock = original })
	predecessorCharge, _, _, _ := generatedRouteAccountingCharge(
		t, fixture, fixture.predecessorDeploymentID, "api")
	if receipt, err := retirement.completeAccountingHandoff(context.Background()); err == nil || receipt.ActivationReceiptID != "" || !fixture.manager.gatewayRebindAdmissionBlocked() {
		t.Fatalf("final unlock receipt=%#v failStop=%t error=%v",
			receipt, fixture.manager.gatewayRebindAdmissionBlocked(), err)
	}
	if predecessorCharge.lease != nil || predecessorCharge.owner != candidateAccountingPredecessorRetired ||
		predecessorCharge.state != candidateChargeReleased || !predecessorCharge.logicallyRetired {
		t.Fatalf("final unlock predecessor = %+v", predecessorCharge)
	}
	assertGeneratedRouteAccountingRows(t, fixture, 1, 1)
	if _, err := retirement.completeAccountingHandoff(context.Background()); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("final unlock failure remained retryable: %v", err)
	}
}

func TestGeneratedRouteAccountingHandoffFinalProofDriftKeepsTerminalCharges(t *testing.T) {
	fixture, retirement := newGeneratedRouteAccountingHandoffFixture(t)
	predecessorID := fixture.successor.predecessorRoster[0].ContainerID
	predecessorReads := 0
	fixture.candidateRunner.beforeInspect = func(target string) {
		if target != predecessorID {
			return
		}
		predecessorReads++
		if predecessorReads == 2 {
			physical := fixture.candidateRunner.physical[predecessorID]
			physical.exists = true
			physical.running = false
			fixture.candidateRunner.physical[predecessorID] = physical
		}
	}
	predecessorCharge, _, _, _ := generatedRouteAccountingCharge(
		t, fixture, fixture.predecessorDeploymentID, "api")
	successorCharge, successorLease, successorEpoch, _ := generatedRouteAccountingCharge(
		t, fixture, fixture.successorDeploymentID, "api")
	if receipt, err := retirement.completeAccountingHandoff(context.Background()); err == nil || receipt.ActivationReceiptID != "" {
		t.Fatalf("final proof drift receipt=%#v error=%v", receipt, err)
	}
	if predecessorReads != 2 || predecessorCharge.lease != nil ||
		predecessorCharge.owner != candidateAccountingPredecessorRetired ||
		predecessorCharge.state != candidateChargeReleased || !predecessorCharge.logicallyRetired {
		t.Fatalf("final proof drift reads=%d predecessor=%+v", predecessorReads, predecessorCharge)
	}
	if successorCharge.lease != successorLease || successorCharge.epoch != successorEpoch ||
		successorCharge.owner != candidateAccountingRouteActive || successorCharge.logicallyRetired {
		t.Fatalf("final proof drift successor=%+v", successorCharge)
	}
	assertGeneratedRouteAccountingRows(t, fixture, 1, 0)
}

func TestGeneratedRouteAccountingHandoffResolvesDurableLostAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
	}{{"authorization_commit", "generated_route_accounting_handoff_authorizations"},
		{"receipt_commit", "generated_route_accounting_handoff_receipts"}} {
		t.Run(test.name, func(t *testing.T) {
			lost := errors.New("test accounting handoff commit acknowledgement lost")
			fault := &generatedRouteAccountingLostAckFault{target: test.target, cause: lost}
			db := openGeneratedRouteAccountingLostAckDatabase(t, fault)
			fixture := newGeneratedRouteAccountingFixtureOnDatabase(t, db)
			retirement := activateGeneratedRouteAccountingFixture(t, fixture)
			predecessorCharge, predecessorLease, _, _ := generatedRouteAccountingCharge(
				t, fixture, fixture.predecessorDeploymentID, "api")

			fault.armed.Store(true)
			first, err := retirement.completeAccountingHandoff(context.Background())
			if !errors.Is(err, lost) || first.ActivationReceiptID != "" || fault.hits.Load() != 1 {
				t.Fatalf("lost acknowledgement receipt=%#v hits=%d error=%v", first, fault.hits.Load(), err)
			}
			if test.target == "generated_route_accounting_handoff_authorizations" {
				if predecessorCharge.lease != predecessorLease ||
					predecessorCharge.owner != candidateAccountingCandidate {
					t.Fatalf("authorization lost acknowledgement changed predecessor=%+v", predecessorCharge)
				}
				assertGeneratedRouteAccountingRows(t, fixture, 1, 0)
				competitor := candidateCapacityTestSpec("other-app", "other-deployment", "worker")
				competitor.limits = generatedcapacity.Limits{MemoryBytes: uint64(fixture.owner.options.MemoryBytes),
					DiskBytes: fixture.owner.options.ReplacementDiskBytes}
				if _, err := fixture.owner.authority.ledger.reserve(context.Background(), competitor); !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
					t.Fatalf("authorization ambiguity released predecessor capacity: %v", err)
				}
			} else {
				if predecessorCharge.lease != nil || predecessorCharge.owner != candidateAccountingPredecessorRetired {
					t.Fatalf("receipt lost acknowledgement predecessor=%+v", predecessorCharge)
				}
				assertGeneratedRouteAccountingRows(t, fixture, 1, 1)
			}
			var admitted *candidateCapacityReservation
			if test.target == "generated_route_accounting_handoff_receipts" {
				competitor := candidateCapacityTestSpec("other-app", "other-deployment", "worker")
				competitor.limits = generatedcapacity.Limits{MemoryBytes: uint64(fixture.owner.options.MemoryBytes),
					DiskBytes: fixture.owner.options.ReplacementDiskBytes}
				admitted = candidateCapacityTestReserve(t, fixture.owner.authority.ledger, competitor)
				defer func() {
					if err := admitted.release(); err != nil {
						t.Error(err)
					}
				}()
			}
			replay, err := retirement.completeAccountingHandoff(context.Background())
			if err != nil || replay.ActivationReceiptID == "" || fault.hits.Load() != 1 || fault.writes.Load() != 1 {
				t.Fatalf("lost acknowledgement replay=%#v hits=%d writes=%d error=%v",
					replay, fault.hits.Load(), fault.writes.Load(), err)
			}
			assertGeneratedRouteAccountingRows(t, fixture, 1, 1)
			if admitted != nil {
				second := candidateCapacityTestSpec("other-app-2", "other-deployment-2", "worker")
				second.limits = generatedcapacity.Limits{MemoryBytes: uint64(fixture.owner.options.MemoryBytes),
					DiskBytes: fixture.owner.options.ReplacementDiskBytes}
				if _, err := fixture.owner.authority.ledger.reserve(context.Background(), second); !errors.Is(err, generatedcapacity.ErrInsufficientCapacity) {
					t.Fatalf("receipt replay released predecessor capacity twice: %v", err)
				}
			}
		})
	}
}

func TestGeneratedRouteAccountingHandoffRetriesCancellationAfterTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fault := &generatedRouteAccountingLostAckFault{
		target: "generated_route_accounting_handoff_authorizations", afterCommit: cancel,
	}
	db := openGeneratedRouteAccountingLostAckDatabase(t, fault)
	fixture := newGeneratedRouteAccountingFixtureOnDatabase(t, db)
	retirement := activateGeneratedRouteAccountingFixture(t, fixture)
	fault.armed.Store(true)
	if receipt, err := retirement.completeAccountingHandoff(ctx); !errors.Is(err, context.Canceled) || receipt.ActivationReceiptID != "" {
		t.Fatalf("cancel after transfer receipt=%#v error=%v", receipt, err)
	}
	operation := retirement.state.accounting
	if operation == nil || operation.stage != generatedRouteAccountingHandoffTransferred ||
		operation.accounting == nil || fault.hits.Load() != 1 {
		t.Fatalf("cancelled cached operation=%#v hits=%d", operation, fault.hits.Load())
	}
	cachedAccounting := operation.accounting
	receipt, err := retirement.completeAccountingHandoff(context.Background())
	if err != nil || receipt.ActivationReceiptID == "" || retirement.state.accounting != operation ||
		operation.accounting != cachedAccounting || fault.writes.Load() != 1 {
		t.Fatalf("cancel retry receipt=%#v sameOperation=%t sameAccounting=%t writes=%d error=%v",
			receipt, retirement.state.accounting == operation, operation.accounting == cachedAccounting,
			fault.writes.Load(), err)
	}
}

func newGeneratedRouteAccountingHandoffFixture(t *testing.T) (
	*generatedRoutePredecessorFixture, *GeneratedPredecessorRetirement,
) {
	t.Helper()
	fixture := newGeneratedRoutePredecessorFixture(t)
	_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
		fixture.owner.validation.migration.guarded.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(retirement.Leave)
	if _, err := retirement.Retire(context.Background()); err != nil {
		t.Fatal(err)
	}
	return fixture, retirement
}

func activateGeneratedRouteAccountingFixture(t *testing.T,
	fixture *generatedRoutePredecessorFixture,
) *GeneratedPredecessorRetirement {
	t.Helper()
	_, retirement, err := fixture.successor.ActivateWithPredecessorRetirement(
		fixture.owner.validation.migration.guarded.context)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(retirement.Leave)
	if _, err := retirement.Retire(context.Background()); err != nil {
		t.Fatal(err)
	}
	return retirement
}

func newGeneratedRouteAccountingFixtureOnDatabase(t *testing.T, db *sql.DB) *generatedRoutePredecessorFixture {
	t.Helper()
	builder, err := securetemp.NewGeneratedBuilderDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := builder.EnsureDirectory("route-accounting-fixture")
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	migration, jobService := newGeneratedRouteCanonicalMigrationOnDatabase(
		t, db, dataRoot, directories.WorkingDirectory)
	owner, candidateRunner := newGeneratedRouteSequentialCandidateOwnerForMigration(t, migration)
	predecessorExecution, predecessorIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)
	manager, runner, routeRunner := newGeneratedRoutePredecessorManagerFixture(t)
	appID := migration.guarded.appID
	runner.appID, runner.network, runner.endpoint = appID, predecessorIdentity.NetworkName,
		generatedRouteEndpoint(predecessorIdentity)
	routeRunner.endpoints[predecessorIdentity.ContainerID] = generatedRouteEndpointSnapshot{
		appID: appID, endpoint: generatedRouteEndpoint(predecessorIdentity),
		slot: generatedidentity.Slot(predecessorIdentity.Slot),
	}
	authority, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	predecessor, err := authority.Pin(migration.guarded.context, predecessorExecution,
		generatedRouteRequest(appID, predecessorIdentity))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := predecessor.Activate(migration.guarded.context); err != nil {
		t.Fatal(err)
	}
	predecessorHead, err := generatedruntimestate.New(db).Active(context.Background(), appID)
	if err != nil {
		t.Fatal(err)
	}
	predecessorObservation, err := manager.Observe(context.Background(), appID)
	if err != nil {
		t.Fatal(err)
	}
	completeGeneratedRouteSequentialCandidate(t, owner, predecessorHead)
	secondMigration := newGeneratedRouteSuccessorMigration(t, migration, jobService)
	owner.validation = newGeneratedRouteSuccessorValidation(t, secondMigration)
	owner.runner.containerID = strings.Repeat("8", 64)
	successorExecution, successorIdentity := prepareGeneratedRouteSequentialCandidate(t, owner)
	successorRequest := generatedRouteRequest(appID, successorIdentity)
	successorRequest.FromSlot = predecessorObservation.Slot
	successor, err := authority.Pin(secondMigration.guarded.context, successorExecution, successorRequest)
	if err != nil {
		t.Fatal(err)
	}
	runner.network, runner.endpoint = successorIdentity.NetworkName, generatedRouteEndpoint(successorIdentity)
	routeRunner.slot = generatedidentity.Slot(successorIdentity.Slot)
	routeRunner.endpoints[successorIdentity.ContainerID] = generatedRouteEndpointSnapshot{
		appID: appID, endpoint: generatedRouteEndpoint(successorIdentity),
		slot: generatedidentity.Slot(successorIdentity.Slot),
	}
	return &generatedRoutePredecessorFixture{owner: owner, manager: manager, runner: runner,
		routeRunner: routeRunner, candidateRunner: candidateRunner, successor: successor, appID: appID,
		predecessorDeploymentID: predecessorHead.DeploymentID,
		successorDeploymentID:   secondMigration.request.DeploymentID,
		predecessorRoute: routeRecord{Slot: predecessorObservation.Slot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), predecessorObservation.Endpoints...)},
		successorRoute: routeRecord{Slot: successorRequest.ToSlot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), successorRequest.Endpoints...)},
	}
}

type generatedRouteAccountingLostAckFault struct {
	armed       atomic.Bool
	hits        atomic.Int32
	writes      atomic.Int32
	target      string
	cause       error
	afterCommit func()
}

type generatedRouteAccountingLostAckConnector struct {
	dsn   string
	fault *generatedRouteAccountingLostAckFault
}

func (c *generatedRouteAccountingLostAckConnector) Connect(context.Context) (driver.Conn, error) {
	connection, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &generatedRouteAccountingLostAckConnection{Conn: connection, fault: c.fault}, nil
}

func (c *generatedRouteAccountingLostAckConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type generatedRouteAccountingLostAckConnection struct {
	driver.Conn
	fault *generatedRouteAccountingLostAckFault
	mu    sync.Mutex
	tx    *generatedRouteAccountingLostAckTransaction
}

func (c *generatedRouteAccountingLostAckConnection) BeginTx(ctx context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	wrapped := &generatedRouteAccountingLostAckTransaction{Tx: tx, connection: c}
	c.mu.Lock()
	if c.tx != nil {
		c.mu.Unlock()
		_ = tx.Rollback()
		return nil, errors.New("accounting lost-ack fixture started overlapping transactions")
	}
	c.tx = wrapped
	c.mu.Unlock()
	return wrapped, nil
}

func (c *generatedRouteAccountingLostAckConnection) PrepareContext(ctx context.Context,
	query string,
) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *generatedRouteAccountingLostAckConnection) ExecContext(ctx context.Context, query string,
	values []driver.NamedValue,
) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, values)
	if err != nil {
		return result, err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if strings.HasPrefix(normalized, "insert into "+c.fault.target+"(") {
		c.fault.writes.Add(1)
		c.mu.Lock()
		if c.tx != nil {
			c.tx.target.Store(true)
		}
		c.mu.Unlock()
	}
	return result, nil
}

func (c *generatedRouteAccountingLostAckConnection) QueryContext(ctx context.Context, query string,
	values []driver.NamedValue,
) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, values)
}

func (c *generatedRouteAccountingLostAckConnection) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *generatedRouteAccountingLostAckConnection) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *generatedRouteAccountingLostAckConnection) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

func (c *generatedRouteAccountingLostAckConnection) clear(tx *generatedRouteAccountingLostAckTransaction) {
	c.mu.Lock()
	if c.tx == tx {
		c.tx = nil
	}
	c.mu.Unlock()
}

type generatedRouteAccountingLostAckTransaction struct {
	driver.Tx
	connection *generatedRouteAccountingLostAckConnection
	target     atomic.Bool
}

func (t *generatedRouteAccountingLostAckTransaction) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		t.connection.clear(t)
		return err
	}
	t.connection.clear(t)
	fault := t.connection.fault
	if t.target.Load() && fault.armed.CompareAndSwap(true, false) {
		fault.hits.Add(1)
		if fault.afterCommit != nil {
			fault.afterCommit()
		}
		return fault.cause
	}
	return nil
}

func (t *generatedRouteAccountingLostAckTransaction) Rollback() error {
	err := t.Tx.Rollback()
	t.connection.clear(t)
	return err
}

func openGeneratedRouteAccountingLostAckDatabase(t *testing.T,
	fault *generatedRouteAccountingLostAckFault,
) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&generatedRouteAccountingLostAckConnector{
		dsn: filepath.Join(t.TempDir(), "control.db"), fault: fault,
	})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	if err := controldatabase.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func generatedRouteAccountingCharge(t *testing.T, fixture *generatedRoutePredecessorFixture,
	deploymentID, componentName string,
) (*candidateCapacityCharge, *generatedcapacity.Lease, uint64, candidateAccountingOwner) {
	t.Helper()
	ledger := fixture.owner.authority.ledger
	ledger.mu.Lock()
	group := ledger.groups[candidateCapacityGroupKey{appID: fixture.appID, deploymentID: deploymentID}]
	ledger.mu.Unlock()
	if group == nil {
		t.Fatalf("accounting group %s is absent", deploymentID)
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	component := group.byName[componentName]
	if component == nil || component.charge == nil {
		t.Fatalf("accounting component %s/%s is absent", deploymentID, componentName)
	}
	charge := component.charge
	return charge, charge.lease, charge.epoch, charge.owner
}

func assertGeneratedRouteAccountingRows(t *testing.T, fixture *generatedRoutePredecessorFixture,
	authorizations, receipts int,
) {
	t.Helper()
	var authorizationCount, receiptCount int
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_accounting_handoff_authorizations
		WHERE activation_receipt_id=?`, fixture.successor.receiptID).Scan(&authorizationCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database().QueryRow(`SELECT COUNT(*) FROM generated_route_accounting_handoff_receipts
		WHERE activation_receipt_id=?`, fixture.successor.receiptID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if authorizationCount != authorizations || receiptCount != receipts {
		t.Fatalf("handoff rows authorization=%d receipt=%d want=%d/%d",
			authorizationCount, receiptCount, authorizations, receipts)
	}
}
