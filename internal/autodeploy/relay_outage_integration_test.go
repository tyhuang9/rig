package autodeploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
	// Preparing a fresh migrated controller is fixture setup, not part of the
	// bounded outage/restart journey. Both phases retain a finite budget.
	setupStarted := time.Now()
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), relayOutageSetupDeadline)
	defer cancelSetup()
	pool, reopenPG := relayOutagePostgres(t, setupCtx, dsn)
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
	relayOutageEnrollRelay(t, setupCtx, relay, now, controllerID, keyID, publicKey)
	if err = relayOutageSeedController(setupCtx, db.ExecContext, now, controllerID, keyID, bindingID, publicKey); err != nil {
		relayOutageFatalSetupFailure(t, setupCtx, relayOutageSetupFailureControllerSeed, err)
	}
	controllerRepository := controllerrelay.NewRepository(db)
	autoRepository := NewRepository(db)
	status, err := autoRepository.Configure(setupCtx, ConfigureRequest{ApplicationID: testApp, ActorUserID: testOwner, ExpectedRevision: 0, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}

	handler, server := relayOutageServer(t, relay)
	firstRun, stopFirst, firstSupervisor := relayOutageSupervisor(t, server.URL, server, controllerRepository, controllerID, keyID, privateKey, publicKey, nil)
	firstFixture := relayOutageFirstRunFixture{stopSupervisor: stopFirst, server: server, handler: handler, done: firstRun}
	t.Cleanup(func() { firstFixture.Stop(t) })
	relayOutageWaitInitialSubscriptionSync(t, setupCtx, db, pool, controllerID, status.SubscriptionID, firstSupervisor)
	deadlineDiagnostic, deadlineExceeded := relayOutageStopFirstFixtureAndObserveSetupDeadline(setupCtx, setupStarted, func() {
		firstFixture.Stop(t)
	}, time.Now)
	if deadlineExceeded {
		t.Log(relayOutageFormatSetupDeadlineDiagnostic(deadlineDiagnostic))
		t.Fatal("initial relay fixture exceeded setup deadline")
	}
	cancelSetup()
	// This single deadline covers every outage update, both controller
	// restarts, lost-ACK recovery and final convergence; no restart resets it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

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

	jobService := newCoordinatorJobService(t, db)
	manualResult, err := jobService.CreateDeploymentWithInputTransaction(ctx, jobs.DeploymentCreateRequest{ApplicationID: testApp, IdempotencyKey: "relay-outage-manual", RequestedBy: testOwner, Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent}}, jobs.DeploymentCreateHooks{})
	manual := manualResult.Job
	if err != nil || !manualResult.Created {
		t.Fatalf("create manual deployment created=%t err=%v", manualResult.Created, err)
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
	pool = reopenPG(ctx)
	relay, err = store.New(pool, store.Options{})
	if err != nil {
		t.Fatal("reconstruct relay store")
	}
	handler, server = relayOutageServer(t, relay)
	dial := &relayOutageDropACKDial{dropped: make(chan struct{}), release: make(chan struct{})}
	defer dial.Release()
	secondRun, stopSecond, _ := relayOutageSupervisor(t, server.URL, server, controllerRepository, controllerID, keyID, privateKey, publicKey, dial.Dial)
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
	thirdRun, stopThird, _ := relayOutageSupervisor(t, server.URL, server, controllerRepository, controllerID, keyID, privateKey, publicKey, nil)
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
	coordinator, err := NewCoordinator(autoRepository, resolver, newCoordinatorJobService(t, db), config)
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

func relayOutagePostgres(t *testing.T, ctx context.Context, dsn string) (*pgxpool.Pool, func(context.Context) *pgxpool.Pool) {
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
	reopen := func(reopenCtx context.Context) *pgxpool.Pool {
		reopened, openErr := pgxpool.NewWithConfig(reopenCtx, cfg)
		if openErr != nil {
			t.Fatal("reopen isolated PostgreSQL test pool")
		}
		if migrateErr := store.Migrate(reopenCtx, reopened); migrateErr != nil {
			reopened.Close()
			t.Fatal("re-migrate isolated PostgreSQL test schema")
		}
		t.Cleanup(reopened.Close)
		return reopened
	}
	return pool, reopen
}

const (
	relayOutageControllerOpenAttempts   = 21
	relayOutageControllerOpenRetryDelay = 25 * time.Millisecond
)

type relayOutageControllerOpenFailure struct {
	category string
	attempts int
}

func relayOutageControllerDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	// This fixture simulates a process restart by closing and reopening SQLite
	// in one process. Retry only SQLite's explicit transient lock results; all
	// other open failures remain immediate and their untrusted details stay out
	// of hosted logs.
	db, failure := relayOutageOpenControllerDB(root, relayOutageControllerOpenAttempts, controldb.Open, time.Sleep)
	if db == nil {
		t.Fatalf("open controller database: category=%s attempts=%d", failure.category, failure.attempts)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func relayOutageOpenControllerDB(root string, maxAttempts int, open func(string) (*sql.DB, error), sleep func(time.Duration)) (*sql.DB, relayOutageControllerOpenFailure) {
	if maxAttempts < 1 || open == nil || sleep == nil {
		return nil, relayOutageControllerOpenFailure{category: "unavailable"}
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		db, err := open(root)
		if err == nil && db != nil {
			return db, relayOutageControllerOpenFailure{}
		}
		if db != nil {
			_ = db.Close()
		}
		category, retryable := relayOutageSQLiteOpenFailure(err)
		failure := relayOutageControllerOpenFailure{category: category, attempts: attempt}
		if !retryable || attempt == maxAttempts {
			return nil, failure
		}
		sleep(relayOutageControllerOpenRetryDelay)
	}
	return nil, relayOutageControllerOpenFailure{category: "unavailable", attempts: maxAttempts}
}

func relayOutageSQLiteOpenFailure(err error) (string, bool) {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return "unavailable", false
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_BUSY:
		return "sqlite_busy", true
	case sqlite3.SQLITE_LOCKED:
		return "sqlite_locked", true
	default:
		return "sqlite_error", false
	}
}

func TestRelayOutageControllerOpenRetriesOnlyTransientSQLiteLocks(t *testing.T) {
	busyErr := relayOutageSQLiteBusyError(t)
	ready, err := controldb.Open(t.TempDir())
	if err != nil {
		t.Fatal("open ready controller database")
	}
	t.Cleanup(func() { _ = ready.Close() })

	t.Run("busy eventually opens", func(t *testing.T) {
		calls, sleeps := 0, 0
		db, failure := relayOutageOpenControllerDB("ignored", 3, func(string) (*sql.DB, error) {
			calls++
			if calls < 3 {
				return nil, busyErr
			}
			return ready, nil
		}, func(time.Duration) { sleeps++ })
		if db != ready || failure != (relayOutageControllerOpenFailure{}) || calls != 3 || sleeps != 2 {
			t.Fatalf("db=%p failure=%#v calls=%d sleeps=%d", db, failure, calls, sleeps)
		}
	})

	t.Run("busy remains bounded", func(t *testing.T) {
		calls, sleeps := 0, 0
		db, failure := relayOutageOpenControllerDB("ignored", 3, func(string) (*sql.DB, error) {
			calls++
			return nil, busyErr
		}, func(time.Duration) { sleeps++ })
		if db != nil || failure.category != "sqlite_busy" || failure.attempts != 3 || calls != 3 || sleeps != 2 {
			t.Fatalf("db=%p failure=%#v calls=%d sleeps=%d", db, failure, calls, sleeps)
		}
	})

	t.Run("unknown fails immediately", func(t *testing.T) {
		calls, sleeps := 0, 0
		db, failure := relayOutageOpenControllerDB("ignored", 3, func(string) (*sql.DB, error) {
			calls++
			return nil, errors.New("private database detail")
		}, func(time.Duration) { sleeps++ })
		if db != nil || failure.category != "unavailable" || failure.attempts != 1 || calls != 1 || sleeps != 0 {
			t.Fatalf("db=%p failure=%#v calls=%d sleeps=%d", db, failure, calls, sleeps)
		}
	})
}

func TestRelayOutageControllerSeedReturnsFirstError(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	publicKey := ed25519.PublicKey(bytes.Repeat([]byte{0x41}, ed25519.PublicKeySize))

	t.Run("complete seed", func(t *testing.T) {
		calls := 0
		err := relayOutageSeedController(context.Background(), func(context.Context, string, ...any) (sql.Result, error) {
			calls++
			return nil, nil
		}, at, "controller", "key", "binding", publicKey)
		if err != nil || calls != 7 {
			t.Fatalf("seed error=%v calls=%d", err, calls)
		}
	})

	t.Run("first failure stops seed", func(t *testing.T) {
		privateErr := errors.New("private controller seed detail")
		calls := 0
		err := relayOutageSeedController(context.Background(), func(context.Context, string, ...any) (sql.Result, error) {
			calls++
			if calls == 3 {
				return nil, privateErr
			}
			return nil, nil
		}, at, "controller", "key", "binding", publicKey)
		if !errors.Is(err, privateErr) || calls != 3 {
			t.Fatalf("seed error=%v calls=%d", err, calls)
		}
	})

	if err := relayOutageSeedController(context.Background(), nil, at, "controller", "key", "binding", publicKey); relayOutageClassifySetupFailure(context.Background(), err) != relayOutageSetupFailureOther {
		t.Fatalf("nil executor error=%v class=%q", err, relayOutageClassifySetupFailure(context.Background(), err))
	}
}

func TestRelayOutageSetupFailureDiagnosticIsClosedAndSanitized(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal("open constraint fixture")
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE constraint_fixture (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal("create constraint fixture")
	}
	if _, err = db.Exec(`INSERT INTO constraint_fixture(id) VALUES(1)`); err != nil {
		t.Fatal("seed constraint fixture")
	}
	_, constraintErr := db.Exec(`INSERT INTO constraint_fixture(id) VALUES(1)`)
	if constraintErr == nil {
		t.Fatal("duplicate constraint fixture was accepted")
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelDeadline()

	for _, test := range []struct {
		name  string
		ctx   context.Context
		err   error
		class relayOutageSetupFailureClass
	}{
		{name: "direct deadline", ctx: canceledCtx, err: fmt.Errorf("private deadline detail: %w", context.DeadlineExceeded), class: relayOutageSetupFailureDeadline},
		{name: "direct canceled", ctx: context.Background(), err: fmt.Errorf("private canceled detail: %w", context.Canceled), class: relayOutageSetupFailureCanceled},
		{name: "constraint", ctx: canceledCtx, err: fmt.Errorf("private constraint detail: %w", constraintErr), class: relayOutageSetupFailureConstraint},
		{name: "deadline context fallback", ctx: deadlineCtx, err: errors.New("private driver interrupt detail"), class: relayOutageSetupFailureDeadline},
		{name: "canceled context fallback", ctx: canceledCtx, err: errors.New("private driver interrupt detail"), class: relayOutageSetupFailureCanceled},
		{name: "other", ctx: context.Background(), err: errors.New("private other detail"), class: relayOutageSetupFailureOther},
	} {
		t.Run(test.name, func(t *testing.T) {
			class := relayOutageClassifySetupFailure(test.ctx, test.err)
			if class != test.class {
				t.Fatalf("class=%q want=%q", class, test.class)
			}
			diagnostic := relayOutageFormatSetupFailureDiagnostic(relayOutageSetupFailureControllerSeed, class)
			want := fmt.Sprintf("relay_outage_setup_failure phase=controller_seed error_class=%s", test.class)
			if diagnostic != want || strings.Contains(diagnostic, "private") {
				t.Fatalf("diagnostic=%q want=%q", diagnostic, want)
			}
		})
	}

	for _, invalid := range []struct {
		phase relayOutageSetupFailurePhase
		class relayOutageSetupFailureClass
	}{
		{phase: "", class: relayOutageSetupFailureOther},
		{phase: "relay_enrollment", class: relayOutageSetupFailureOther},
		{phase: relayOutageSetupFailureControllerSeed, class: ""},
		{phase: relayOutageSetupFailureControllerSeed, class: "timeout"},
	} {
		if diagnostic := relayOutageFormatSetupFailureDiagnostic(invalid.phase, invalid.class); diagnostic != "" {
			t.Fatalf("invalid phase=%q class=%q diagnostic=%q", invalid.phase, invalid.class, diagnostic)
		}
	}
}

func TestRelayOutageSetupFailureFatalEmitsOnlyClosedProjection(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("locate relay outage diagnostic helper")
	}
	command := exec.Command(executable, "-test.run=^TestRelayOutageSetupFailureFatalHelper$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "RIG_RELAY_OUTAGE_FATAL_HELPER=1")
	output, runErr := command.CombinedOutput()
	if runErr == nil {
		t.Fatal("fatal relay outage diagnostic helper succeeded")
	}
	text := string(output)
	want := "relay_outage_setup_failure phase=controller_seed error_class=canceled"
	if !strings.Contains(text, want) || strings.Contains(text, "private driver interrupt detail") {
		t.Fatalf("fatal diagnostic did not retain the closed projection: %q", text)
	}
}

func TestRelayOutageSetupFailureFatalHelper(t *testing.T) {
	if os.Getenv("RIG_RELAY_OUTAGE_FATAL_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	relayOutageFatalSetupFailure(t, ctx, relayOutageSetupFailureControllerSeed, errors.New("private driver interrupt detail"))
}

func TestRelayOutageSetupDeadlineObservationStopsFirstFixture(t *testing.T) {
	started := time.Unix(100, 0)

	t.Run("deadline before first fixture stop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		times := []time.Time{started.Add(9 * time.Second), started.Add(9 * time.Second), started.Add(12 * time.Second)}
		next := 0
		stopped := 0
		diagnostic, found := relayOutageStopFirstFixtureAndObserveSetupDeadline(ctx, started, func() { stopped++ }, func() time.Time {
			value := times[next]
			next++
			return value
		})
		if stopped != 1 || !found || diagnostic.phase != relayOutageSetupDeadlineBeforeFirstFixtureStop || diagnostic.preStopElapsedMS != 9000 || diagnostic.stopElapsedMS != 3000 {
			t.Fatalf("stopped=%d found=%t diagnostic=%+v", stopped, found, diagnostic)
		}
		if got := relayOutageFormatSetupDeadlineDiagnostic(diagnostic); got != "relay_outage_setup_deadline phase=before_first_fixture_stop deadline_exceeded=true pre_stop_elapsed_ms=9000 stop_elapsed_ms=3000" {
			t.Fatalf("diagnostic=%q", got)
		}
	})

	t.Run("deadline during first fixture stop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		times := []time.Time{started.Add(8 * time.Second), started.Add(8 * time.Second), started.Add(9500 * time.Millisecond)}
		next := 0
		stopped := 0
		diagnostic, found := relayOutageStopFirstFixtureAndObserveSetupDeadline(ctx, started, func() {
			stopped++
			cancel()
		}, func() time.Time {
			value := times[next]
			next++
			return value
		})
		if stopped != 1 || !found || diagnostic.phase != relayOutageSetupDeadlineAfterFirstFixtureStop || diagnostic.preStopElapsedMS != 8000 || diagnostic.stopElapsedMS != 1500 {
			t.Fatalf("stopped=%d found=%t diagnostic=%+v", stopped, found, diagnostic)
		}
	})

	t.Run("no deadline expiry emits no record", func(t *testing.T) {
		times := []time.Time{started.Add(time.Second), started.Add(time.Second), started.Add(2 * time.Second)}
		next := 0
		stopped := 0
		_, found := relayOutageStopFirstFixtureAndObserveSetupDeadline(context.Background(), started, func() { stopped++ }, func() time.Time {
			value := times[next]
			next++
			return value
		})
		if stopped != 1 || found {
			t.Fatalf("stopped=%d found=%t", stopped, found)
		}
	})
}

func TestRelayOutageSetupDeadlineDiagnosticRejectsInvalidValues(t *testing.T) {
	limit := int64(relayOutageSetupDeadline / time.Millisecond)
	for _, diagnostic := range []relayOutageSetupDeadlineDiagnostic{
		{phase: relayOutageSetupDeadlinePhase("unknown"), deadlineExceeded: true},
		{phase: relayOutageSetupDeadlineBeforeFirstFixtureStop, deadlineExceeded: false},
		{phase: relayOutageSetupDeadlineBeforeFirstFixtureStop, deadlineExceeded: true, preStopElapsedMS: -1},
		{phase: relayOutageSetupDeadlineBeforeFirstFixtureStop, deadlineExceeded: true, stopElapsedMS: -1},
		{phase: relayOutageSetupDeadlineBeforeFirstFixtureStop, deadlineExceeded: true, preStopElapsedMS: limit + 1},
		{phase: relayOutageSetupDeadlineBeforeFirstFixtureStop, deadlineExceeded: true, stopElapsedMS: limit + 1},
	} {
		if got := relayOutageFormatSetupDeadlineDiagnostic(diagnostic); got != "" {
			t.Fatalf("invalid diagnostic=%+v formatted=%q", diagnostic, got)
		}
	}
}

func TestRelayOutageBoundedElapsedMilliseconds(t *testing.T) {
	started := time.Unix(100, 0)
	limit := int64(relayOutageSetupDeadline / time.Millisecond)
	for _, test := range []struct {
		name     string
		finished time.Time
		want     int64
	}{
		{name: "negative is zero", finished: started.Add(-time.Millisecond), want: 0},
		{name: "sub millisecond is zero", finished: started.Add(999 * time.Microsecond), want: 0},
		{name: "ordinary duration", finished: started.Add(1234 * time.Millisecond), want: 1234},
		{name: "over limit is clipped", finished: started.Add(relayOutageSetupDeadline + time.Second), want: limit},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := relayOutageBoundedElapsedMilliseconds(started, test.finished); got != test.want {
				t.Fatalf("elapsed=%d want=%d", got, test.want)
			}
		})
	}
}

func relayOutageSQLiteBusyError(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.db")
	first, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("open first busy database")
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("open second busy database")
	}
	t.Cleanup(func() { _ = second.Close() })
	if _, err = first.Exec(`CREATE TABLE busy_fixture (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal("create busy database fixture")
	}
	if _, err = second.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal("disable second busy timeout")
	}
	tx, err := first.Begin()
	if err != nil {
		t.Fatal("begin busy database transaction")
	}
	if _, err = tx.Exec(`INSERT INTO busy_fixture(id) VALUES(1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal("lock busy database fixture")
	}
	_, busyErr := second.Exec(`INSERT INTO busy_fixture(id) VALUES(2)`)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal("release busy database fixture")
	}
	category, retryable := relayOutageSQLiteOpenFailure(busyErr)
	if category != "sqlite_busy" || !retryable {
		t.Fatalf("busy database category=%s retryable=%t", category, retryable)
	}
	return busyErr
}

type relayOutageSeedExec func(context.Context, string, ...any) (sql.Result, error)

func relayOutageSeedController(ctx context.Context, exec relayOutageSeedExec, at time.Time, controllerID, keyID, bindingID string, publicKey ed25519.PublicKey) error {
	if exec == nil {
		return errors.New("controller seed executor is unavailable")
	}
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
		if _, err := exec(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
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

func relayOutageSupervisor(t *testing.T, url string, server *httptest.Server, repository *controllerrelay.Repository, controllerID, keyID string, privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey, dial controllerrelay.SessionDialFunc) (<-chan error, func(), *controllerrelay.Supervisor) {
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
	return done, cancel, supervisor
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

const (
	relayOutageSetupDeadline          = 20 * time.Second
	relayOutageInitialSyncTimeout     = 5 * time.Second
	relayOutageDiagnosticQueryTimeout = 100 * time.Millisecond
	relayOutageDiagnosticCountLimit   = 1000
	relayOutageDiagnosticUnavailable  = -1
)

const relayOutageSetupDeadlineDiagnosticPrefix = "relay_outage_setup_deadline"

const relayOutageSetupFailureDiagnosticPrefix = "relay_outage_setup_failure"

type relayOutageSetupFailurePhase string

const relayOutageSetupFailureControllerSeed relayOutageSetupFailurePhase = "controller_seed"

type relayOutageSetupFailureClass string

const (
	relayOutageSetupFailureDeadline   relayOutageSetupFailureClass = "deadline"
	relayOutageSetupFailureCanceled   relayOutageSetupFailureClass = "canceled"
	relayOutageSetupFailureConstraint relayOutageSetupFailureClass = "constraint"
	relayOutageSetupFailureOther      relayOutageSetupFailureClass = "other"
)

func relayOutageFatalSetupFailure(t testing.TB, ctx context.Context, phase relayOutageSetupFailurePhase, err error) {
	t.Helper()
	diagnostic := relayOutageFormatSetupFailureDiagnostic(phase, relayOutageClassifySetupFailure(ctx, err))
	if diagnostic == "" {
		diagnostic = "relay outage setup failed"
	}
	t.Fatal(diagnostic)
}

func relayOutageClassifySetupFailure(ctx context.Context, err error) relayOutageSetupFailureClass {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return relayOutageSetupFailureDeadline
	case errors.Is(err, context.Canceled):
		return relayOutageSetupFailureCanceled
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT {
		return relayOutageSetupFailureConstraint
	}
	if ctx != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return relayOutageSetupFailureDeadline
		case context.Canceled:
			return relayOutageSetupFailureCanceled
		}
	}
	return relayOutageSetupFailureOther
}

func relayOutageFormatSetupFailureDiagnostic(phase relayOutageSetupFailurePhase, class relayOutageSetupFailureClass) string {
	if phase != relayOutageSetupFailureControllerSeed {
		return ""
	}
	switch class {
	case relayOutageSetupFailureDeadline, relayOutageSetupFailureCanceled, relayOutageSetupFailureConstraint, relayOutageSetupFailureOther:
		return fmt.Sprintf("%s phase=%s error_class=%s", relayOutageSetupFailureDiagnosticPrefix, phase, class)
	default:
		return ""
	}
}

type relayOutageSetupDeadlinePhase string

const (
	relayOutageSetupDeadlineBeforeFirstFixtureStop relayOutageSetupDeadlinePhase = "before_first_fixture_stop"
	relayOutageSetupDeadlineAfterFirstFixtureStop  relayOutageSetupDeadlinePhase = "after_first_fixture_stop"
)

type relayOutageSetupDeadlineDiagnostic struct {
	phase            relayOutageSetupDeadlinePhase
	deadlineExceeded bool
	preStopElapsedMS int64
	stopElapsedMS    int64
}

func relayOutageStopFirstFixtureAndObserveSetupDeadline(ctx context.Context, setupStarted time.Time, stop func(), now func() time.Time) (relayOutageSetupDeadlineDiagnostic, bool) {
	beforeStop := now()
	deadlineExceededBeforeStop := ctx != nil && ctx.Err() != nil
	stopStarted := now()
	stop()
	stopFinished := now()
	phase := relayOutageSetupDeadlineAfterFirstFixtureStop
	if deadlineExceededBeforeStop {
		phase = relayOutageSetupDeadlineBeforeFirstFixtureStop
	} else if ctx == nil || ctx.Err() == nil {
		return relayOutageSetupDeadlineDiagnostic{}, false
	}
	return relayOutageSetupDeadlineDiagnostic{
		phase:            phase,
		deadlineExceeded: true,
		preStopElapsedMS: relayOutageBoundedElapsedMilliseconds(setupStarted, beforeStop),
		stopElapsedMS:    relayOutageBoundedElapsedMilliseconds(stopStarted, stopFinished),
	}, true
}

func relayOutageBoundedElapsedMilliseconds(started, finished time.Time) int64 {
	elapsed := finished.Sub(started)
	if elapsed <= 0 {
		return 0
	}
	limit := int64(relayOutageSetupDeadline / time.Millisecond)
	milliseconds := int64(elapsed / time.Millisecond)
	if milliseconds > limit {
		return limit
	}
	return milliseconds
}

func relayOutageFormatSetupDeadlineDiagnostic(diagnostic relayOutageSetupDeadlineDiagnostic) string {
	if !relayOutageKnownSetupDeadlinePhase(diagnostic.phase) || !diagnostic.deadlineExceeded {
		return ""
	}
	limit := int64(relayOutageSetupDeadline / time.Millisecond)
	if diagnostic.preStopElapsedMS < 0 || diagnostic.preStopElapsedMS > limit || diagnostic.stopElapsedMS < 0 || diagnostic.stopElapsedMS > limit {
		return ""
	}
	return fmt.Sprintf("%s phase=%s deadline_exceeded=%t pre_stop_elapsed_ms=%d stop_elapsed_ms=%d", relayOutageSetupDeadlineDiagnosticPrefix, diagnostic.phase, diagnostic.deadlineExceeded, diagnostic.preStopElapsedMS, diagnostic.stopElapsedMS)
}

func relayOutageKnownSetupDeadlinePhase(phase relayOutageSetupDeadlinePhase) bool {
	switch phase {
	case relayOutageSetupDeadlineBeforeFirstFixtureStop, relayOutageSetupDeadlineAfterFirstFixtureStop:
		return true
	default:
		return false
	}
}

type relayOutageFirstRunFixture struct {
	stopOnce       sync.Once
	stopSupervisor func()
	server         *httptest.Server
	handler        *wss.Handler
	done           <-chan error
}

func (fixture *relayOutageFirstRunFixture) Stop(t *testing.T) {
	if fixture == nil {
		return
	}
	fixture.stopOnce.Do(func() {
		fixture.stopSupervisor()
		fixture.server.Close()
		fixture.handler.StopAdmissions()
		select {
		case err := <-fixture.done:
			if err != nil {
				t.Fatal("initial relay fixture supervisor failed")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("initial relay fixture supervisor did not stop")
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if fixture.handler.Wait(waitCtx) != nil {
			t.Fatal("initial relay fixture handler did not stop")
		}
	})
}

func relayOutageWaitInitialSubscriptionSync(t *testing.T, parent context.Context, db *sql.DB, pool *pgxpool.Pool, controllerID, subscriptionID string, supervisor *controllerrelay.Supervisor) {
	t.Helper()
	deadline := time.NewTimer(relayOutageInitialSyncTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if relayOutagePostgresDiagnosticCount(parent, pool, `SELECT COUNT(*) FROM relay_subscriptions WHERE controller_id=$1 AND subscription_id=$2`, controllerID, subscriptionID) == 1 {
			return
		}
		select {
		case <-parent.Done():
			relayOutageFatalInitialSubscriptionSync(t, db, pool, controllerID, supervisor)
		case <-deadline.C:
			relayOutageFatalInitialSubscriptionSync(t, db, pool, controllerID, supervisor)
		case <-ticker.C:
		}
	}
}

func relayOutageFatalInitialSubscriptionSync(t *testing.T, db *sql.DB, pool *pgxpool.Pool, controllerID string, supervisor *controllerrelay.Supervisor) {
	t.Helper()
	snapshot := relayOutageSafeSupervisorSnapshot(supervisor)
	t.Fatalf("timed out waiting for initial authenticated subscription sync: sqlite_active_controller_subscriptions=%d sqlite_subscription_sync_heads=%d sqlite_inflight_sync_sets=%d postgres_relay_sessions=%d postgres_controller_leases=%d postgres_subscriptions_sync_commands=%d postgres_current_relay_subscriptions=%d supervisor_state=%s supervisor_outcome=%s supervisor_paused=%t",
		relayOutageSQLiteDiagnosticCount(context.Background(), db, `SELECT COUNT(*) FROM relay_controller_subscriptions WHERE controller_id=? AND state='active'`, controllerID),
		relayOutageSQLiteDiagnosticCount(context.Background(), db, `SELECT COUNT(*) FROM relay_subscription_sync_heads WHERE controller_id=?`, controllerID),
		relayOutageSQLiteDiagnosticCount(context.Background(), db, `SELECT COUNT(*) FROM relay_subscription_sync_sets WHERE controller_id=? AND state='inflight'`, controllerID),
		relayOutagePostgresDiagnosticCount(context.Background(), pool, `SELECT COUNT(*) FROM relay_sessions WHERE controller_id=$1 AND revoked_at IS NULL`, controllerID),
		relayOutagePostgresDiagnosticCount(context.Background(), pool, `SELECT COUNT(*) FROM relay_controller_leases WHERE controller_id=$1`, controllerID),
		relayOutagePostgresDiagnosticCount(context.Background(), pool, `SELECT COUNT(*) FROM relay_session_commands WHERE controller_id=$1 AND command_type='subscriptions.sync'`, controllerID),
		relayOutagePostgresDiagnosticCount(context.Background(), pool, `SELECT COUNT(*) FROM relay_subscriptions WHERE controller_id=$1 AND retired_generation IS NULL`, controllerID),
		snapshot.state, snapshot.outcome, snapshot.paused)
}

func relayOutageSQLiteDiagnosticCount(parent context.Context, db *sql.DB, query string, args ...any) int {
	if db == nil {
		return relayOutageDiagnosticUnavailable
	}
	return relayOutageDiagnosticCount(parent, func(queryCtx context.Context) (int, error) {
		var count int
		err := db.QueryRowContext(queryCtx, query, args...).Scan(&count)
		return count, err
	})
}

func relayOutagePostgresDiagnosticCount(parent context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	if pool == nil {
		return relayOutageDiagnosticUnavailable
	}
	return relayOutageDiagnosticCount(parent, func(queryCtx context.Context) (int, error) {
		var count int
		err := pool.QueryRow(queryCtx, query, args...).Scan(&count)
		return count, err
	})
}

func relayOutageDiagnosticCount(parent context.Context, load func(context.Context) (int, error)) int {
	queryCtx, cancel := context.WithTimeout(parent, relayOutageDiagnosticQueryTimeout)
	defer cancel()
	count, err := load(queryCtx)
	if err != nil || count < 0 {
		return relayOutageDiagnosticUnavailable
	}
	if count > relayOutageDiagnosticCountLimit {
		return relayOutageDiagnosticCountLimit
	}
	return count
}

type relayOutageSafeSnapshot struct {
	state   string
	outcome string
	paused  bool
}

func relayOutageSafeSupervisorSnapshot(supervisor *controllerrelay.Supervisor) relayOutageSafeSnapshot {
	if supervisor == nil {
		return relayOutageSafeSnapshot{state: "unavailable", outcome: "unavailable"}
	}
	snapshot := supervisor.Snapshot()
	return relayOutageSafeSnapshot{state: relayOutageSafeSupervisorState(snapshot.State), outcome: relayOutageSafeSupervisorOutcome(snapshot.Outcome), paused: snapshot.Paused}
}

func relayOutageSafeSupervisorState(state string) string {
	switch state {
	case "", controllerrelay.SessionDisconnected, controllerrelay.SessionConnecting, controllerrelay.SessionAuthenticating, controllerrelay.SessionReady, controllerrelay.SessionBackoff, controllerrelay.SessionNeedsAttention, controllerrelay.SessionStopped:
		return state
	default:
		return "unavailable"
	}
}

func relayOutageSafeSupervisorOutcome(outcome string) string {
	switch outcome {
	case "", "relay_unavailable", "connection_closed", "protocol_error", "persistence_unavailable", "credential_unavailable", "identity_unavailable", "queue_saturated", "session_expired":
		return outcome
	default:
		return "unavailable"
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
