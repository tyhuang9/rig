package autodeploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/controllerrelay"
	controldb "github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
	"github.com/hostd/hostd/internal/relay/wss"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgreSQLRelayOutageConvergesDurablyAcrossRelayAndControllerRestart exercises the
// bounded outage path using real PostgreSQL relay state, WSS protocol sessions,
// and a reopened SQLite controller. It is opt-in because it requires a local
// PostgreSQL DSN; it deliberately does not attempt to prove OS-process failure.
func TestPostgreSQLRelayOutageConvergesDurablyAcrossRelayAndControllerRestart(t *testing.T) {
	dsn := os.Getenv("RIG_RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RIG_RELAY_TEST_DATABASE_URL is unset; relay outage convergence integration not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, reopenPG := relayOutagePostgres(t, ctx, dsn)
	relay, err := store.New(pool, store.Options{})
	if err != nil {
		t.Fatal("create relay store")
	}

	root := t.TempDir()
	db := relayOutageControllerDB(t, root)
	controllerID, keyID, bindingID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x53}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	now := time.Now().UTC()
	relayOutageEnrollRelay(t, ctx, relay, now, controllerID, keyID, publicKey)
	relayOutageSeedController(t, ctx, db, now, controllerID, keyID, bindingID, publicKey)
	controllerRepository := controllerrelay.NewRepository(db)
	autoRepository := NewRepository(db)
	status, err := autoRepository.Configure(ctx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}

	handler, server := relayOutageServer(t, relay)
	firstRun, stopFirst := relayOutageSupervisor(t, server.URL, server, controllerRepository, controllerID, keyID, privateKey, publicKey, nil)
	relayOutageWait(t, ctx, "initial authenticated subscription sync", func(queryCtx context.Context) (bool, error) {
		var count int
		err := pool.QueryRow(queryCtx, `SELECT COUNT(*) FROM relay_subscriptions WHERE controller_id=$1 AND subscription_id=$2`, controllerID, status.SubscriptionID).Scan(&count)
		return count == 1, err
	})
	stopFirst()
	server.Close()
	handler.StopAdmissions()
	relayOutageWaitRun(t, firstRun)
	relayOutageWaitHandler(t, handler)

	for generation, sha := range []string{testSHA, secondSHA, coordinatorThirdSHA} {
		result, pushErr := relay.PushSourceEvent(ctx, store.SourceEvent{
			DeliveryID: uuid.NewString(), InstallationID: testInstallation, RepositoryID: testRepository,
			Ref: testRef, SHA: sha, ReceivedAt: now.Add(time.Duration(generation) * time.Second), ObservedAt: now.Add(time.Duration(generation) * time.Second),
		}, []store.SourceRoute{{ControllerID: controllerID, SubscriptionID: status.SubscriptionID}})
		if pushErr != nil || len(result.Desired) != 1 || result.Desired[0].Generation != uint64(generation+1) {
			t.Fatalf("outage push generation %d result=%#v err=%v", generation+1, result, pushErr)
		}
	}
	var generation int64
	var desiredSHA string
	if err = pool.QueryRow(ctx, `SELECT generation,observed_sha FROM relay_desired_states WHERE subscription_id=$1`, status.SubscriptionID).Scan(&generation, &desiredSHA); err != nil || generation != 3 || desiredSHA != coordinatorThirdSHA {
		t.Fatalf("PostgreSQL desired generation=%d sha=%q want generation=3 sha=%q err=%v", generation, desiredSHA, coordinatorThirdSHA, err)
	}

	jobService := jobs.New(db)
	manual, created, err := jobService.CreateWithInput(jobs.CreateRequest{Type: "deploy", ResourceType: "application", ResourceID: testApp, IdempotencyKey: "relay-outage-manual", RequestedBy: testOwner, Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}})
	if err != nil || !created {
		t.Fatalf("create manual deployment created=%t err=%v", created, err)
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- jobService.RunWorker(workerCtx, jobs.NewFakeExecutor()) }()
	relayOutageWait(t, ctx, "manual deployment during relay outage", func(queryCtx context.Context) (bool, error) {
		var jobStatus string
		err := db.QueryRowContext(queryCtx, `SELECT status FROM jobs WHERE id=?`, manual.ID).Scan(&jobStatus)
		return jobStatus == string(jobs.Succeeded), err
	})
	cancelWorker()
	relayOutageWaitRun(t, workerDone)

	if err = db.Close(); err != nil {
		t.Fatal("close controller database")
	}
	db = relayOutageControllerDB(t, root)
	controllerRepository = controllerrelay.NewRepository(db)
	autoRepository = NewRepository(db)
	relay.Close()
	pool.Close()
	pool = reopenPG()
	relay, err = store.New(pool, store.Options{})
	if err != nil {
		t.Fatal("reconstruct relay store")
	}
	handler, server = relayOutageServer(t, relay)
	dial := &relayOutageDropACKDial{dropped: make(chan struct{}), release: make(chan struct{})}
	defer dial.Release()
	secondRun, stopSecond := relayOutageSupervisor(t, server.URL, server, controllerRepository, controllerID, keyID, privateKey, publicKey, dial.Dial)
	select {
	case <-dial.dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("first restarted source ACK was not deliberately dropped")
	}
	stopSecond()
	if err = db.Close(); err != nil {
		t.Fatal("close controller database after durable lost ACK")
	}
	db = relayOutageControllerDB(t, root)
	controllerRepository = controllerrelay.NewRepository(db)
	autoRepository = NewRepository(db)
	relayOutageAssertDurableSource(t, ctx, db, controllerID, status.SubscriptionID)
	dial.Release()
	relayOutageWaitRun(t, secondRun)
	thirdRun, stopThird := relayOutageSupervisor(t, server.URL, server, controllerRepository, controllerID, keyID, privateKey, publicKey, nil)
	defer func() {
		stopThird()
		server.Close()
		handler.StopAdmissions()
		relayOutageWaitRun(t, thirdRun)
		relayOutageWaitHandler(t, handler)
		relay.Close()
		pool.Close()
	}()
	relayOutageWait(t, ctx, "generation three durable inbox and relay ACK", func(queryCtx context.Context) (bool, error) {
		var inboxGeneration, inboxRows, ackGeneration, terminalGeneration, sourceACKs int64
		var inboxSHA string
		err := db.QueryRowContext(queryCtx, `SELECT COALESCE(MAX(generation),0),COUNT(*),COALESCE(MAX(observed_sha),'') FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=?`, controllerID, status.SubscriptionID).Scan(&inboxGeneration, &inboxRows, &inboxSHA)
		if err == nil {
			err = db.QueryRowContext(queryCtx, `SELECT COALESCE(MAX(generation),0) FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, controllerID, status.SubscriptionID).Scan(&ackGeneration)
		}
		if err == nil {
			err = pool.QueryRow(queryCtx, `SELECT COALESCE(MAX(generation),0) FROM relay_desired_states WHERE subscription_id=$1 AND decision='acked'`, status.SubscriptionID).Scan(&terminalGeneration)
		}
		if err == nil {
			err = pool.QueryRow(queryCtx, `SELECT COUNT(*) FROM relay_session_commands WHERE command_type='ack.source'`).Scan(&sourceACKs)
		}
		return inboxGeneration == 3 && inboxRows == 1 && inboxSHA == coordinatorThirdSHA && ackGeneration == 3 && terminalGeneration == 3 && sourceACKs == 1, err
	})

	resolver := &relayOutageResolver{sha: coordinatorThirdSHA}
	config := DefaultCoordinatorConfig()
	config.MinResolveInterval = time.Nanosecond
	coordinator, err := NewCoordinator(autoRepository, resolver, jobs.New(db), config)
	if err != nil {
		t.Fatal(err)
	}
	claimed, event := coordinator.processOne(ctx)
	if !claimed || event.Dispatched != 1 || resolver.calls != 1 {
		t.Fatalf("latest-head coordinator event=%#v claimed=%t resolver calls=%d", event, claimed, resolver.calls)
	}
	converged, err := autoRepository.Get(ctx, testApp)
	if err != nil || converged.LastConsumedGeneration != 3 || converged.ActiveJobID == "" {
		t.Fatalf("coordinator did not consume generation three status=%#v err=%v", converged, err)
	}
	autoJob, err := jobs.New(db).Get(converged.ActiveJobID)
	if err != nil || autoJob.ID == manual.ID || autoJob.Type != "deploy" || autoJob.ResourceType != "application" || autoJob.ResourceID != testApp || autoJob.RequestedBy != testOwner || string(autoJob.Input) != `{"releaseId":"","configurationMode":"current"}` {
		t.Fatalf("latest-head job=%#v manual=%q err=%v", autoJob, manual.ID, err)
	}
	var idempotencyKey string
	if err = db.QueryRowContext(ctx, `SELECT idempotency_key FROM jobs WHERE id=?`, autoJob.ID).Scan(&idempotencyKey); err != nil || idempotencyKey != DispatchIdempotencyKey(status.Revision, converged.ActiveDispatchSequence) {
		t.Fatalf("latest-head idempotency=%q want=%q err=%v", idempotencyKey, DispatchIdempotencyKey(status.Revision, converged.ActiveDispatchSequence), err)
	}
	var autoJobCount int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE type='deploy' AND resource_type='application' AND resource_id=? AND id<>?`, testApp, manual.ID).Scan(&autoJobCount); err != nil || autoJobCount != 1 {
		t.Fatalf("latest-head deploy jobs=%d want=1 err=%v", autoJobCount, err)
	}
}

func relayOutageAssertDurableSource(t *testing.T, ctx context.Context, db *sql.DB, controllerID, subscriptionID string) {
	t.Helper()
	var inboxRows, inboxGeneration, ackGeneration int64
	var inboxSHA, ackSHA string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(generation),0),COALESCE(MAX(observed_sha),'') FROM relay_source_event_inbox WHERE controller_id=? AND subscription_id=?`, controllerID, subscriptionID).Scan(&inboxRows, &inboxGeneration, &inboxSHA); err != nil {
		t.Fatalf("load durable source inbox after lost ACK: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT generation,observed_sha FROM relay_source_ack_heads WHERE controller_id=? AND subscription_id=?`, controllerID, subscriptionID).Scan(&ackGeneration, &ackSHA); err != nil {
		t.Fatalf("load durable source ACK head after lost ACK: %v", err)
	}
	if inboxRows != 1 || inboxGeneration != 3 || inboxSHA != coordinatorThirdSHA || ackGeneration != 3 || ackSHA != coordinatorThirdSHA {
		t.Fatalf("durable source after lost ACK rows=%d inbox=(%d,%q) ack=(%d,%q)", inboxRows, inboxGeneration, inboxSHA, ackGeneration, ackSHA)
	}
}

func relayOutagePostgres(t *testing.T, ctx context.Context, dsn string) (*pgxpool.Pool, func() *pgxpool.Pool) {
	t.Helper()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("open PostgreSQL test administrator")
	}
	t.Cleanup(admin.Close)
	schema := "relay_outage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create isolated PostgreSQL test schema")
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse PostgreSQL test configuration")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open isolated PostgreSQL test pool")
	}
	t.Cleanup(pool.Close)
	if err = store.Migrate(ctx, pool); err != nil {
		t.Fatal("migrate isolated PostgreSQL test schema")
	}
	reopen := func() *pgxpool.Pool {
		reopened, openErr := pgxpool.NewWithConfig(ctx, cfg)
		if openErr != nil {
			t.Fatal("reopen isolated PostgreSQL test pool")
		}
		if migrateErr := store.Migrate(ctx, reopened); migrateErr != nil {
			reopened.Close()
			t.Fatal("re-migrate isolated PostgreSQL test schema")
		}
		t.Cleanup(reopened.Close)
		return reopened
	}
	return pool, reopen
}

func relayOutageControllerDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := controldb.Open(root)
	if err != nil {
		t.Fatal("open controller database")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func relayOutageSeedController(t *testing.T, ctx context.Context, db *sql.DB, at time.Time, controllerID, keyID, bindingID string, publicKey ed25519.PublicKey) {
	t.Helper()
	stamp := timestamp(at)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?,'administrator',?,?)`, []any{testOwner, testOwner, "hash", stamp, stamp}},
		{`INSERT INTO source_connections(id,owner_user_id,provider,status,provider_user_id,provider_login,credential_generation,access_expires_at,refresh_expires_at,connected_at,created_at,updated_at) VALUES(?,?,'github','connected','42','octocat',1,?,?,?,?,?)`, []any{testConnection, testOwner, stamp, stamp, stamp, stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,'Application','draft',?,?)`, []any{testApp, testApp, stamp, stamp}},
		{`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?,'github',?,?,?,'octo','app','main',?,'compose.yaml',?,?,?)`, []any{testApp, testConnection, testInstallation, testRepository, testRef, testSHA, stamp, stamp}},
		{`INSERT INTO relay_controllers(singleton,controller_id,state,created_at,updated_at) VALUES(1,?,'active',?,?)`, []any{controllerID, stamp, stamp}},
		{`INSERT INTO relay_controller_keys(key_id,controller_id,public_key,algorithm,state,protected_key_ref,created_at,updated_at,activated_at,possession_confirmed_at) VALUES(?,?,?,'ed25519','active',?,?,?,?,?)`, []any{keyID, controllerID, publicKey, controllerrelay.ProtectedKeyRef(controllerID, keyID), stamp, stamp, stamp, stamp}},
		{`INSERT INTO relay_installation_bindings(binding_id,owner_user_id,connection_id,controller_id,installation_id,repository_id,state,state_changed_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'authorized',?,?,?)`, []any{bindingID, testOwner, testConnection, controllerID, testInstallation, testRepository, stamp, stamp, stamp}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func relayOutageServer(t *testing.T, relay *store.Store) (*wss.Handler, *httptest.Server) {
	t.Helper()
	config := wss.DefaultConfig()
	config.PollInterval = 10 * time.Millisecond
	config.StoreTimeout = time.Second
	config.WriteTimeout = time.Second
	handler, err := wss.NewHandler(relay, config, wss.Options{Entropy: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(func() {
		server.Close()
		handler.StopAdmissions()
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = handler.Wait(waitCtx)
	})
	return handler, server
}

func relayOutageSupervisor(t *testing.T, url string, server *httptest.Server, repository *controllerrelay.Repository, controllerID, keyID string, privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey, dial controllerrelay.SessionDialFunc) (<-chan error, func()) {
	t.Helper()
	transportConfig := controllerrelay.DefaultSessionTransportConfig()
	transportConfig.HandshakeTimeout = time.Second
	transportConfig.WriteTimeout = time.Second
	transportConfig.PersistenceTimeout = time.Second
	transport, err := controllerrelay.NewSessionTransport(url, repository, &relayOutageCredentials{controllerID: controllerID, keyID: keyID, privateKey: privateKey, publicKey: publicKey}, server.Client().Transport, dial, transportConfig)
	if err != nil {
		t.Fatal(err)
	}
	config := controllerrelay.DefaultSupervisorConfig()
	config.InitialBackoff = 5 * time.Millisecond
	config.MaximumBackoff = 25 * time.Millisecond
	config.Jitter = func(delay time.Duration, _ uint32) time.Duration { return delay }
	supervisor, err := controllerrelay.NewSupervisor(transport, repository, relayOutageRecovery{}, relayOutageCompleter{}, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	return done, cancel
}

func relayOutageEnrollRelay(t *testing.T, ctx context.Context, relay *store.Store, at time.Time, controllerID, keyID string, publicKey ed25519.PublicKey) {
	t.Helper()
	stateHash := bytes.Repeat([]byte{0x11}, 32)
	enrollmentID, err := relay.CreateEnrollment(ctx, store.EnrollmentInput{
		ControllerID: controllerID, KeyID: keyID, PublicKey: publicKey, InstallationID: testInstallation, RepositoryID: testRepository,
		StateHash: stateHash, PollHash: bytes.Repeat([]byte{0x12}, 32), PKCECiphertext: bytes.Repeat([]byte{0x13}, 29), PKCESealNonce: bytes.Repeat([]byte{0x14}, 12), RequestNonce: bytes.Repeat([]byte{0x15}, 32), ExpiresAt: at.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := relay.ClaimEnrollmentState(ctx, stateHash)
	if err != nil {
		t.Fatal(err)
	}
	claim.Destroy()
	if err = relay.CompleteEnrollment(ctx, enrollmentID); err != nil {
		t.Fatal(err)
	}
}

type relayOutageCredentials struct {
	controllerID, keyID string
	privateKey          ed25519.PrivateKey
	publicKey           ed25519.PublicKey
}

func (c *relayOutageCredentials) ReadControllerKey(controllerID, keyID string, expected []byte) (controllerrelay.ControllerKeyBundle, error) {
	if c == nil || controllerID != c.controllerID || keyID != c.keyID || !bytes.Equal(expected, c.publicKey) {
		return controllerrelay.ControllerKeyBundle{}, errors.New("test credential unavailable")
	}
	return controllerrelay.ControllerKeyBundle{Version: 1, ControllerID: controllerID, KeyID: keyID, PrivateKey: append(ed25519.PrivateKey(nil), c.privateKey...), PublicKey: append(ed25519.PublicKey(nil), c.publicKey...)}, nil
}

type relayOutageRecovery struct{}

func (relayOutageRecovery) RecoverControllerKeysPage(context.Context, controllerrelay.ControllerKeyRecoveryCursor, int) (controllerrelay.ControllerKeyRecoveryPage, error) {
	return controllerrelay.ControllerKeyRecoveryPage{Complete: true}, nil
}

type relayOutageCompleter struct{}

func (relayOutageCompleter) CompleteRotationAfterFencedReady(context.Context, string, string, uint64, uint64) error {
	return nil
}

type relayOutageResolver struct {
	sha   string
	calls int
}

func (r *relayOutageResolver) ResolveHead(context.Context, SourceScope) (string, error) {
	r.calls++
	return r.sha, nil
}

type relayOutageDropACKDial struct {
	droppedOnce atomic.Bool
	dropped     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (dial *relayOutageDropACKDial) Release() {
	if dial != nil {
		dial.releaseOnce.Do(func() { close(dial.release) })
	}
}

func (dial *relayOutageDropACKDial) Dial(ctx context.Context, target string, options *websocket.DialOptions) (controllerrelay.SessionSocket, *http.Response, error) {
	connection, response, err := websocket.Dial(ctx, target, options)
	if err != nil {
		return nil, response, err
	}
	return &relayOutageDropACKSocket{SessionSocket: connection, dial: dial}, response, nil
}

type relayOutageDropACKSocket struct {
	controllerrelay.SessionSocket
	dial *relayOutageDropACKDial
}

func (socket *relayOutageDropACKSocket) Write(ctx context.Context, messageType websocket.MessageType, data []byte) error {
	frame, err := protocol.Decode(data, protocol.DefaultMaxEnvelopeBytes)
	if err == nil {
		if ack, ok := frame.(*protocol.Ack); ok && ack.Source != nil && socket.dial.droppedOnce.CompareAndSwap(false, true) {
			_ = socket.SessionSocket.CloseNow()
			close(socket.dial.dropped)
			<-socket.dial.release
			return errors.New("test source ACK connection drop")
		}
	}
	return socket.SessionSocket.Write(ctx, messageType, data)
}

func relayOutageWait(t *testing.T, parent context.Context, label string, ready func(context.Context) (bool, error)) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		queryCtx, cancel := context.WithTimeout(parent, time.Second)
		ok, err := ready(queryCtx)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		} else if ok {
			return
		}
		select {
		case <-parent.Done():
			t.Fatalf("timed out waiting for %s: %v", label, parent.Err())
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", label)
		case <-ticker.C:
		}
	}
}
func relayOutageWaitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("background run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background run did not stop")
	}
}

func relayOutageWaitHandler(t *testing.T, handler *wss.Handler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handler.Wait(ctx); err != nil {
		t.Fatalf("wait for relay handler: %v", err)
	}
}
