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
	"github.com/hostd/hostd/internal/generatedroute"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"modernc.org/sqlite"
)

func TestGeneratedRouteActivationResolvesLostDurableCommitAcknowledgement(t *testing.T) {
	lostAcknowledgement := errors.New("test route activation durable commit acknowledgement lost")
	fault := &generatedRouteLostAckFault{cause: lostAcknowledgement}
	db := openGeneratedRouteLostAckDatabase(t, fault)
	migration := newGeneratedMigrationFixtureOnDatabaseWithComponents(t, db, []string{"api"}, []string{"api"})
	owner := newCandidateOwnerFixtureWithValidation(t, newCandidateOwnerValidationFixtureWithMigration(t, migration))
	reservation := owner.reserve(t)
	t.Cleanup(reservation.Release)
	execution := owner.pin(t, reservation)
	t.Cleanup(execution.Leave)
	invocation := owner.beginCreate(t, execution)
	owner.bindInvocation(t, invocation)
	identity, err := execution.CreateAndRecord(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.StartCandidate(invocation); err != nil {
		t.Fatal(err)
	}
	owner.moveToWaitingHealth(t, execution)
	if err := execution.WaitHealthyAndRecord(invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.FinishHealth(); err != nil {
		t.Fatal(err)
	}

	manager, runner := newManagerFixture(t, false)
	runner.appID = migration.guarded.appID
	runner.network = identity.NetworkName
	runner.endpoint = generatedRouteEndpoint(identity)
	authority, err := newGeneratedRouteAuthority(manager, owner.authority, owner.authority.stores)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := authority.Pin(migration.guarded.context, execution,
		generatedRouteRequest(runner.appID, identity))
	if err != nil {
		t.Fatal(err)
	}
	commandsBefore := len(runner.commands)
	fault.armed.Store(true)
	result, err := sealed.Activate(migration.guarded.context)
	if err != nil {
		t.Fatalf("outer activation did not resolve lost acknowledgement: %v", err)
	}
	if result.DeploymentID != migration.request.DeploymentID || result.Phase != generatedruntimestate.PhaseDraining {
		t.Fatalf("resolved activation result=%#v", result)
	}
	if fault.hits.Load() != 1 || fault.committedBeforeError.Load() != 1 || fault.armed.Load() ||
		fault.activationWrites.Load() != 1 {
		t.Fatalf("lost-ack fault hits=%d committed-before-error=%d armed=%t activation-writes=%d",
			fault.hits.Load(), fault.committedBeforeError.Load(), fault.armed.Load(), fault.activationWrites.Load())
	}

	head, err := generatedruntimestate.New(db).Active(context.Background(), runner.appID)
	if err != nil {
		t.Fatal(err)
	}
	if head.DeploymentID != migration.request.DeploymentID || head.ReleaseID != migration.request.ReleaseID ||
		head.Slot != identity.Slot || head.Generation != 1 || head.ActivationReceiptID != sealed.receiptID {
		t.Fatalf("durable activation head=%#v", head)
	}
	proof := generatedRouteLostAckObservedProof(t, sealed)
	var receiptCount, successorGeneration, creatorAttempt int
	var creatorJob, candidateDigest, predecessorDigest, intentDigest, proofDigest string
	if err := db.QueryRow(`SELECT COUNT(*),MAX(successor_generation),MAX(creator_job_id),
		MAX(creator_worker_attempt),MAX(candidate_roster_digest),MAX(predecessor_roster_digest),
		MAX(route_intent_digest),MAX(committed_route_proof_digest)
		FROM generated_route_activations WHERE id=?`, sealed.receiptID).Scan(
		&receiptCount, &successorGeneration, &creatorJob, &creatorAttempt, &candidateDigest,
		&predecessorDigest, &intentDigest, &proofDigest); err != nil {
		t.Fatal(err)
	}
	wantCandidateDigest := generatedRouteActivationRosterDigest(sealed.candidateRoster)
	canonicalPredecessor, ok := canonicalGeneratedRouteActivationRoster(sealed.predecessorRoster)
	if !ok {
		t.Fatal("sealed predecessor roster is not canonical")
	}
	wantPredecessorDigest := generatedRouteActivationRosterDigest(canonicalPredecessor)
	wantIntentDigest := generatedRouteActivationDocumentDigest(sealed.intent)
	wantProofDigest := generatedRouteActivationDocumentDigest(proof)
	if receiptCount != 1 || successorGeneration != 1 || creatorJob != migration.claim.JobID() ||
		creatorAttempt != migration.claim.Attempt() ||
		candidateDigest != wantCandidateDigest || predecessorDigest != wantPredecessorDigest ||
		intentDigest != wantIntentDigest || proofDigest != wantProofDigest {
		t.Fatalf("durable receipt count=%d generation=%d creator=%s/%d digests=%s/%s/%s/%s want=%s/%s/%s/%s",
			receiptCount, successorGeneration, creatorJob, creatorAttempt, candidateDigest,
			predecessorDigest, intentDigest, proofDigest, wantCandidateDigest,
			wantPredecessorDigest, wantIntentDigest, wantProofDigest)
	}
	var activeRoster, drainingPhase int
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_components
		WHERE deployment_id=? AND state='active'`, migration.request.DeploymentID).Scan(&activeRoster); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM generated_runtime_deployments
		WHERE deployment_id=? AND phase='draining'`, migration.request.DeploymentID).Scan(&drainingPhase); err != nil {
		t.Fatal(err)
	}
	if activeRoster != len(sealed.candidateRoster) || drainingPhase != 1 || len(sealed.predecessorRoster) != 0 {
		t.Fatalf("durable roster/phase active=%d predecessor=%d draining=%d",
			activeRoster, len(sealed.predecessorRoster), drainingPhase)
	}
	observation, err := manager.Observe(context.Background(), runner.appID)
	if err != nil {
		t.Fatal(err)
	}
	wantRoute := routeRecord{Slot: sealed.request.ToSlot,
		Endpoints: append([]generatedroute.RouteEndpoint(nil), sealed.request.Endpoints...)}
	if !sameRoute(wantRoute, routeRecord{Slot: observation.Slot, Endpoints: observation.Endpoints}) {
		t.Fatalf("physical route after lost acknowledgement=%#v", observation)
	}
	// The first fixed reload establishes the canonical owned gateway before the
	// proposed candidate reload. Replay or restoration would add another.
	if generatedRouteLostAckCommandCount(runner.commands[commandsBefore:], "reload") != 2 {
		t.Fatalf("lost acknowledgement caused duplicate switch or restore: commands=%v", runner.commands[commandsBefore:])
	}
	if replay, err := sealed.Activate(context.Background()); replay.DeploymentID != "" ||
		!errors.Is(err, deploymentexecution.ErrScopeUnavailable) {
		t.Fatalf("used execution replay result=%#v error=%v", replay, err)
	}
}

type generatedRouteLostAckFault struct {
	armed                atomic.Bool
	hits                 atomic.Int32
	committedBeforeError atomic.Int32
	activationWrites     atomic.Int32
	cause                error
}

type generatedRouteLostAckConnector struct {
	dsn   string
	fault *generatedRouteLostAckFault
}

func (c *generatedRouteLostAckConnector) Connect(context.Context) (driver.Conn, error) {
	connection, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &generatedRouteLostAckConnection{Conn: connection, fault: c.fault}, nil
}

func (c *generatedRouteLostAckConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type generatedRouteLostAckConnection struct {
	driver.Conn
	fault *generatedRouteLostAckFault
	mu    sync.Mutex
	tx    *generatedRouteLostAckTransaction
}

func (c *generatedRouteLostAckConnection) BeginTx(ctx context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	wrapped := &generatedRouteLostAckTransaction{Tx: tx, connection: c}
	c.mu.Lock()
	if c.tx != nil {
		c.mu.Unlock()
		_ = tx.Rollback()
		return nil, errors.New("route lost-ack fixture started overlapping transactions")
	}
	c.tx = wrapped
	c.mu.Unlock()
	return wrapped, nil
}

func (c *generatedRouteLostAckConnection) PrepareContext(ctx context.Context,
	query string,
) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *generatedRouteLostAckConnection) ExecContext(ctx context.Context, query string,
	values []driver.NamedValue,
) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, values)
	if err != nil {
		return result, err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if strings.HasPrefix(normalized, "insert into generated_route_activations(") {
		c.fault.activationWrites.Add(1)
		c.mu.Lock()
		if c.tx != nil {
			c.tx.activation.Store(true)
		}
		c.mu.Unlock()
	}
	return result, nil
}

func (c *generatedRouteLostAckConnection) QueryContext(ctx context.Context, query string,
	values []driver.NamedValue,
) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, values)
}

func (c *generatedRouteLostAckConnection) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *generatedRouteLostAckConnection) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *generatedRouteLostAckConnection) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

func (c *generatedRouteLostAckConnection) clear(tx *generatedRouteLostAckTransaction) {
	c.mu.Lock()
	if c.tx == tx {
		c.tx = nil
	}
	c.mu.Unlock()
}

type generatedRouteLostAckTransaction struct {
	driver.Tx
	connection *generatedRouteLostAckConnection
	activation atomic.Bool
}

func (t *generatedRouteLostAckTransaction) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		t.connection.clear(t)
		return err
	}
	t.connection.clear(t)
	if t.activation.Load() && t.connection.fault.armed.CompareAndSwap(true, false) {
		t.connection.fault.committedBeforeError.Add(1)
		t.connection.fault.hits.Add(1)
		return t.connection.fault.cause
	}
	return nil
}

func (t *generatedRouteLostAckTransaction) Rollback() error {
	err := t.Tx.Rollback()
	t.connection.clear(t)
	return err
}

func openGeneratedRouteLostAckDatabase(t *testing.T, fault *generatedRouteLostAckFault) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&generatedRouteLostAckConnector{
		dsn: filepath.Join(t.TempDir(), "control.db"), fault: fault})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("%s: %v", pragma, err)
		}
	}
	if err := controldatabase.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func generatedRouteLostAckObservedProof(t *testing.T, sealed *GeneratedRouteExecution) []byte {
	t.Helper()
	observation, err := sealed.authority.manager.Observe(context.Background(), sealed.request.AppID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := marshalCanonicalGeneratedRouteDocument(generatedRouteProofDocument{
		Version: 1, ReceiptID: sealed.receiptID,
		After: routeRecord{Slot: observation.Slot,
			Endpoints: append([]generatedroute.RouteEndpoint(nil), observation.Endpoints...)}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func generatedRouteLostAckCommandCount(commands [][]string, argument string) int {
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
