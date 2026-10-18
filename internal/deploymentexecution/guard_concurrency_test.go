package deploymentexecution

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
)

type executionLifecycleValidator struct{ db *sql.DB }

func (v *executionLifecycleValidator) ConfiguredFor(db *sql.DB) bool { return v != nil && v.db == db }
func (*executionLifecycleValidator) HistoricalBindingTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) error {
	return nil
}
func (*executionLifecycleValidator) CurrentOperationTx(context.Context, *sql.Tx, deploymentbinding.OperationRef) (deploymentbinding.Completion, error) {
	return deploymentbinding.CompletionComplete, nil
}
func (*executionLifecycleValidator) UnresolvedHistoryTx(context.Context, *sql.Tx, string) (bool, error) {
	return false, nil
}

type blockingPositiveFence struct {
	db            *sql.DB
	entered       chan context.Context
	continueCheck chan struct{}
	cancel        context.CancelFunc
}

func TestPolicyGateStatusIsClosed(t *testing.T) {
	for _, status := range []string{"preparing", "needs_attention", "applying", "waiting_health"} {
		if !policyGateStatus(status) {
			t.Fatalf("authorized status %q refused", status)
		}
	}
	for _, status := range []string{"", "queued", "failed", "cancelled", "succeeded", "unknown"} {
		if policyGateStatus(status) {
			t.Fatalf("unauthorized status %q accepted", status)
		}
	}
}

func (v *blockingPositiveFence) ConfiguredFor(db *sql.DB) bool { return v != nil && v.db == db }

func (v *blockingPositiveFence) PendingStartExistsTx(ctx context.Context, _ *sql.Tx) (bool, error) {
	if v.entered != nil {
		v.entered <- ctx
	}
	if v.continueCheck != nil {
		<-v.continueCheck
	}
	if v.cancel != nil {
		v.cancel()
	}
	return false, nil
}

func TestPinnedLeaveClosesEntryWaitsForCheckAndInvalidatesCopies(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	validator := &blockingPositiveFence{db: fixture.db, entered: make(chan context.Context, 1), continueCheck: make(chan struct{})}
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(validator.continueCheck) }) }
	defer unblock()
	ctx := context.WithValue(context.Background(), executionContextKey{}, "original")
	guard, err := NewGuard(fixture.db, fixture.bindings, validator, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := guard.PinScope(deploymenteffects.WithScope(ctx, scope))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		unblock()
		pin.Leave()
		_ = release()
	}()
	copyPin := *pin
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	checkDone := make(chan error, 1)
	go func() { checkDone <- pin.CheckPolicyGateTx(tx, fixture.claim, fixture.deploymentID) }()
	var observed context.Context
	select {
	case observed = <-validator.entered:
	case <-time.After(time.Second):
		t.Fatal("policy check did not reach the validator callback")
	}
	if got := observed.Value(executionContextKey{}); got != "original" {
		t.Fatalf("validator context value=%v", got)
	}
	leaveDone := make(chan struct{})
	go func() {
		copyPin.Leave()
		close(leaveDone)
	}()
	waitForPinnedClosing(t, pin)
	if err := pin.CheckPolicyGateTx(tx, fixture.claim, fixture.deploymentID); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("new check while closing error=%v", err)
	}
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- release() }()
	select {
	case err := <-leaveDone:
		t.Fatalf("pin left while callback was active: %v", err)
	default:
	}
	select {
	case err := <-releaseDone:
		t.Fatalf("outer scope released while callback was active: %v", err)
	default:
	}
	unblock()
	var checkErr error
	select {
	case checkErr = <-checkDone:
	case <-time.After(time.Second):
		t.Fatal("active check did not return after callback release")
	}
	if !errors.Is(checkErr, ErrScopeUnavailable) {
		t.Fatalf("active check error=%v", checkErr)
	}
	select {
	case <-leaveDone:
	case <-time.After(time.Second):
		t.Fatal("Leave did not finish after the active check")
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("outer scope did not release after the active check")
	}
	pin.Leave()
	copyPin.Leave()
	if err := pin.CheckPolicyGateTx(tx, fixture.claim, fixture.deploymentID); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("left original error=%v", err)
	}
	if err := copyPin.CheckPolicyGateTx(tx, fixture.claim, fixture.deploymentID); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("left copy error=%v", err)
	}
}

func TestPinnedCheckObservesCancellationAfterSuccessfulValidatorCallback(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	validator := &blockingPositiveFence{db: fixture.db, cancel: cancel}
	guard, err := NewGuard(fixture.db, fixture.bindings, validator, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	pin, err := guard.PinScope(deploymenteffects.WithScope(ctx, scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Leave()
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := pin.CheckPolicyGateTx(tx, fixture.claim, fixture.deploymentID); !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

type executionContextKey struct{}

type concurrencyFixture struct {
	db           *sql.DB
	bindings     *deploymentbinding.Repository
	claim        Claim
	deploymentID string
	directory    string
}

func newConcurrencyFixture(t *testing.T) *concurrencyFixture {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	actorID, appID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, []any{actorID, actorID, "hash", stamp, stamp}},
		{`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, []any{appID, appID, appID, stamp, stamp}},
		{`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,attempt,requested_by,input_json,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',0,?,'{}',?,?)`, []any{jobID, appID, actorID, stamp, stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	bindings, err := deploymentbinding.New(db, &executionLifecycleValidator{db: db})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindings.InsertNewTx(context.Background(), tx, jobID, time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=? WHERE id=?`, stamp, jobID); err != nil {
		t.Fatal(err)
	}
	deploymentID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO deployments(id,app_id,job_id,status,configuration_mode,provenance_initialized,started_at) VALUES(?,?,?,'preparing','current',1,?)`, deploymentID, appID, jobID, stamp); err != nil {
		t.Fatal(err)
	}
	claim, err := NewClaim(binding, jobID, appID, 1)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &concurrencyFixture{db: db, bindings: bindings, claim: claim, deploymentID: deploymentID, directory: directories.WorkingDirectory}
}

func waitForPinnedClosing(t *testing.T, pin *Pinned) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		pin.state.mu.Lock()
		closing := pin.state.closing
		pin.state.mu.Unlock()
		if closing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Leave did not close new checks")
		}
		runtime.Gosched()
	}
}
