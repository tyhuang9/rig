package deployments

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"modernc.org/sqlite"
)

type candidateCommitBoundary uint8

const (
	candidateCommitBeginCreate candidateCommitBoundary = iota + 1
	candidateCommitFinishHealth
)

type candidateCommitFault struct {
	target                    atomic.Int32
	hits                      atomic.Int32
	readOnlyCommitsWhileArmed atomic.Int32
	cause                     error
}

func (f *candidateCommitFault) arm(boundary candidateCommitBoundary) {
	f.target.Store(int32(boundary))
}

type candidateCommitConnector struct {
	dsn   string
	fault *candidateCommitFault
}

func (c *candidateCommitConnector) Connect(context.Context) (driver.Conn, error) {
	connection, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &candidateCommitConnection{Conn: connection, fault: c.fault}, nil
}

func (c *candidateCommitConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type candidateCommitConnection struct {
	driver.Conn
	fault *candidateCommitFault
	mu    sync.Mutex
	tx    *candidateCommitTransaction
}

func (c *candidateCommitConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	wrapped := &candidateCommitTransaction{Tx: tx, connection: c, readOnly: options.ReadOnly}
	c.mu.Lock()
	if c.tx != nil {
		c.mu.Unlock()
		_ = tx.Rollback()
		return nil, errors.New("candidate commit fixture started overlapping transactions on one connection")
	}
	c.tx = wrapped
	c.mu.Unlock()
	return wrapped, nil
}

func (c *candidateCommitConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *candidateCommitConnection) ExecContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, values)
	if err != nil {
		return result, err
	}
	boundary := candidateCommitBoundaryForQuery(query)
	if boundary == 0 {
		return result, nil
	}
	c.mu.Lock()
	if c.tx != nil {
		c.tx.boundary.Store(int32(boundary))
	}
	c.mu.Unlock()
	return result, nil
}

func (c *candidateCommitConnection) QueryContext(ctx context.Context, query string, values []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, values)
}

func (c *candidateCommitConnection) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *candidateCommitConnection) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *candidateCommitConnection) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

func (c *candidateCommitConnection) clear(tx *candidateCommitTransaction) {
	c.mu.Lock()
	if c.tx == tx {
		c.tx = nil
	}
	c.mu.Unlock()
}

type candidateCommitTransaction struct {
	driver.Tx
	connection *candidateCommitConnection
	boundary   atomic.Int32
	readOnly   bool
}

func (t *candidateCommitTransaction) Commit() error {
	err := t.Tx.Commit()
	t.connection.clear(t)
	if err != nil {
		return err
	}
	fault := t.connection.fault
	if t.readOnly && fault.target.Load() != 0 {
		fault.readOnlyCommitsWhileArmed.Add(1)
	}
	boundary := t.boundary.Load()
	if boundary != 0 && fault.target.CompareAndSwap(boundary, 0) {
		fault.hits.Add(1)
		return fault.cause
	}
	return nil
}

func (t *candidateCommitTransaction) Rollback() error {
	err := t.Tx.Rollback()
	t.connection.clear(t)
	return err
}

func candidateCommitBoundaryForQuery(query string) candidateCommitBoundary {
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	switch {
	case strings.HasPrefix(normalized, "insert into generated_candidate_intents("):
		return candidateCommitBeginCreate
	case strings.HasPrefix(normalized, "update generated_runtime_deployments set phase='switching_route'"):
		return candidateCommitFinishHealth
	default:
		return 0
	}
}

func openCandidateCommitDatabase(t *testing.T, fault *candidateCommitFault) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&candidateCommitConnector{dsn: filepath.Join(t.TempDir(), "control.db"), fault: fault})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func newCandidateCommitOwnerFixture(t *testing.T, fault *candidateCommitFault) *candidateOwnerFixture {
	t.Helper()
	base := newGeneratedMigrationRepositoryFixtureOnDatabase(t, openCandidateCommitDatabase(t, fault))
	migration := newGeneratedMigrationFixtureWithBase(t, base, []string{"api"}, []string{"api"})
	return newCandidateOwnerFixtureWithValidation(t, newCandidateOwnerValidationFixtureWithMigration(t, migration))
}

func TestGeneratedCandidateBeginCreatePreservesCommittedIntentWhenAcknowledgementLost(t *testing.T) {
	lostAcknowledgement := errors.New("test BeginCreate commit acknowledgement lost")
	fault := &candidateCommitFault{cause: lostAcknowledgement}
	fixture := newCandidateCommitOwnerFixture(t, fault)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)

	fault.arm(candidateCommitBeginCreate)
	invocation, err := execution.BeginCreate("api")
	if invocation != nil || !errors.Is(err, lostAcknowledgement) {
		t.Fatalf("ambiguous BeginCreate result invocation=%v err=%v", invocation, err)
	}
	assertCandidateCommitFault(t, fault)

	current := fixture.validation.read(t)
	if current.runtime.phase != generatedruntimestate.PhaseStartingCandidate || len(current.components) != 1 ||
		current.components[0].state != generatedruntimestate.ComponentStarting || current.components[0].receipt == nil ||
		current.components[0].containerName == "" || current.components[0].containerID != "" {
		t.Fatalf("durable ambiguous create intent = %#v", current)
	}
	var receipts int
	if err := fixture.validation.migration.repository.db.QueryRow(
		`SELECT COUNT(*) FROM generated_candidate_intents WHERE deployment_id=? AND component_name='api'`,
		fixture.validation.request.DeploymentID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("durable candidate receipt count = %d", receipts)
	}
	if retry, err := execution.BeginCreate("api"); retry != nil || !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate BeginCreate result invocation=%v err=%v", retry, err)
	}
	if adopted, err := execution.AdoptCandidate("api"); adopted != nil || !errors.Is(err, deploymentexecution.ErrStageInvalid) {
		t.Fatalf("ambiguous receipt adoption result invocation=%v err=%v", adopted, err)
	}
	if fixture.runner.count(candidateOwnerCreate) != 0 || fixture.runner.count(candidateOwnerStart) != 0 ||
		fixture.runner.count(candidateOwnerStop) != 0 || fixture.runner.count(candidateOwnerRemove) != 0 {
		t.Fatalf("ambiguous BeginCreate ran physical commands create=%d start=%d stop=%d remove=%d",
			fixture.runner.count(candidateOwnerCreate), fixture.runner.count(candidateOwnerStart),
			fixture.runner.count(candidateOwnerStop), fixture.runner.count(candidateOwnerRemove))
	}
	execution.Leave()
	reservation.Release()
	// No invocation escaped BeginCreate, so only this original binding has
	// exact proof that create was never entered. Its unused share may release
	// without changing the durable uncertain receipt or Starting state.
	leases, reserveErr := fixture.authority.budget.Acquire(context.Background(), generatedcapacity.Limits{
		MemoryBytes: uint64(fixture.authority.options.MemoryBytes),
		DiskBytes:   fixture.authority.options.ReplacementDiskBytes,
	}, 1)
	for _, lease := range leases {
		lease.Release()
	}
	if reserveErr != nil || len(leases) != 1 {
		t.Fatalf("original never-entered capacity was not released: count=%d err=%v", len(leases), reserveErr)
	}
	if after := fixture.validation.read(t); !reflect.DeepEqual(current, after) {
		t.Fatal("releasing original never-entered capacity changed durable intent")
	}
	// A later attachment cannot reconstruct that private proof. It must
	// reacquire and retain capacity while leaving the same receipt unresolved.
	aliasReservation := fixture.reserve(t)
	aliasReservation.Release()
	assertCandidateSecurityCapacityRetained(t, fixture)
}

func TestGeneratedCandidateFinishHealthPreservesCommittedHandoffWhenAcknowledgementLost(t *testing.T) {
	lostAcknowledgement := errors.New("test FinishHealth commit acknowledgement lost")
	fault := &candidateCommitFault{cause: lostAcknowledgement}
	fixture := newCandidateCommitOwnerFixture(t, fault)
	reservation := fixture.reserve(t)
	t.Cleanup(reservation.Release)
	execution := fixture.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := fixture.beginCreate(t, execution)
	fixture.bindInvocation(t, invocation)
	if _, err := execution.CreateAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	fixture.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}

	aliasReservation := fixture.reserve(t)
	t.Cleanup(aliasReservation.Release)
	alias := fixture.pin(t, aliasReservation)
	t.Cleanup(alias.Leave)
	adopted, err := alias.AdoptCandidate("api")
	if err != nil {
		t.Fatal(err)
	}

	fault.arm(candidateCommitFinishHealth)
	state, err := execution.FinishHealth()
	if state.DeploymentID != "" || state.Phase != "" || len(state.Components) != 0 || !errors.Is(err, lostAcknowledgement) {
		t.Fatalf("ambiguous FinishHealth result state=%#v err=%v", state, err)
	}
	assertCandidateCommitFault(t, fault)

	current := fixture.validation.read(t)
	if current.runtime.phase != generatedruntimestate.PhaseSwitchingRoute || len(current.components) != 1 ||
		current.components[0].state != generatedruntimestate.ComponentHealthy || current.components[0].receipt == nil ||
		current.components[0].containerID != fixture.runner.containerID {
		t.Fatalf("durable ambiguous handoff = %#v", current)
	}
	if _, err := execution.FinishHealth(); !errors.Is(err, deploymentexecution.ErrStageInvalid) {
		t.Fatalf("FinishHealth replay error = %v", err)
	}
	if err := execution.RemoveInactive(invocation); !errors.Is(err, errCandidateCleanupClosed) {
		t.Fatalf("original cleanup after ambiguous handoff = %v", err)
	}
	if err := alias.RemoveInactive(adopted); !errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("alias cleanup after ambiguous handoff = %v", err)
	}
	if fixture.runner.count(candidateOwnerCreate) != 1 || fixture.runner.count(candidateOwnerStart) != 1 ||
		fixture.runner.count(candidateOwnerStop) != 0 || fixture.runner.count(candidateOwnerRemove) != 0 {
		t.Fatalf("ambiguous FinishHealth physical commands create=%d start=%d stop=%d remove=%d",
			fixture.runner.count(candidateOwnerCreate), fixture.runner.count(candidateOwnerStart),
			fixture.runner.count(candidateOwnerStop), fixture.runner.count(candidateOwnerRemove))
	}
	execution.Leave()
	alias.Leave()
	reservation.Release()
	aliasReservation.Release()
	assertCandidateSecurityCapacityRetained(t, fixture)
}

func assertCandidateCommitFault(t *testing.T, fault *candidateCommitFault) {
	t.Helper()
	if got := fault.hits.Load(); got != 1 {
		t.Fatalf("candidate commit fault hit count = %d", got)
	}
	if got := fault.readOnlyCommitsWhileArmed.Load(); got < 1 {
		t.Fatalf("read-only commits while fault armed = %d", got)
	}
	if target := fault.target.Load(); target != 0 {
		t.Fatalf("candidate commit fault target remained armed: %d", target)
	}
}
