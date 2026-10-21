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
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
)

type cleanupPositiveFence struct {
	db      *sql.DB
	pending bool
}

func (v *cleanupPositiveFence) ConfiguredFor(db *sql.DB) bool { return v != nil && v.db == db }
func (v *cleanupPositiveFence) PendingStartExistsTx(context.Context, *sql.Tx) (bool, error) {
	return v.pending, nil
}

func TestGeneratedMigrationCleanupBindsIdentityAndCancellationState(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	guard, err := NewGuard(fixture.db, fixture.bindings, &cleanupPositiveFence{db: fixture.db}, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	original, cancelOriginal := context.WithCancel(context.Background())
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	}()
	pin, err := guard.PinScope(deploymenteffects.WithScope(original, scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Leave()
	invocationID := uuid.NewString()
	cleanup, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Leave()
	deadline, ok := cleanup.state.ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 69*time.Minute || remaining > generatedMigrationCleanupTimeout {
		t.Fatalf("cleanup deadline ok=%t remaining=%s", ok, remaining)
	}

	// Cancellation moves the worker to the only post-cancel cleanup state.
	// The child retains scope values but deliberately does not inherit the
	// original cancellation.
	cancelOriginal()
	fixture.setJobState(t, "waiting_external", "cancelling", 1)
	for _, check := range []func(*sql.Tx, Claim, string, string) error{
		cleanup.CheckBeforeRemoveTx,
		cleanup.CheckRemovedObservationTx,
	} {
		tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		checkErr := check(tx, fixture.claim, fixture.deploymentID, invocationID)
		_ = tx.Rollback()
		if checkErr != nil {
			t.Fatalf("cancellation cleanup check = %v", checkErr)
		}
	}

	wrongAttempt, err := NewClaim(fixture.claim.binding, fixture.claim.jobID, fixture.claim.applicationID, fixture.claim.attempt+1)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanupCheckError(t, fixture, cleanup, wrongAttempt, fixture.deploymentID, invocationID, ErrIdentityLost)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, uuid.NewString(), ErrIdentityLost)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, uuid.NewString(), invocationID, ErrIdentityLost)

	foreignBindings, err := deploymentbinding.New(fixture.db, &executionLifecycleValidator{db: fixture.db})
	if err != nil {
		t.Fatal(err)
	}
	foreignBinding, err := foreignBindings.Read(context.Background(), fixture.claim.jobID)
	if err != nil {
		t.Fatal(err)
	}
	foreignClaim, err := NewClaim(foreignBinding, fixture.claim.jobID, fixture.claim.applicationID, fixture.claim.attempt)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanupCheckError(t, fixture, cleanup, foreignClaim, fixture.deploymentID, invocationID, ErrIdentityLost)
}

func TestGeneratedMigrationCleanupGateIsClosedAndNonrenewable(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	validator := &cleanupPositiveFence{db: fixture.db}
	guard, err := NewGuard(fixture.db, fixture.bindings, validator, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	}()
	pin, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Leave()
	invocationID := uuid.NewString()
	cleanup, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Leave()

	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, nil)
	fixture.setJobState(t, "waiting_external", "cancelling", 1)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, nil)
	fixture.setJobState(t, "running", "cancelling", 1)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, ErrIdentityLost)
	fixture.setJobState(t, "running", "running", 2)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, ErrIdentityLost)
	fixture.setJobState(t, "running", "running", 1)
	fixture.setDeploymentStatus(t, "waiting_health", true)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, ErrStageInvalid)
	fixture.setDeploymentStatus(t, "applying", true)
	validator.pending = true
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, ErrPositiveFence)
	validator.pending = false
	if _, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("same invocation derived a second live cleanup: %v", err)
	}
	if _, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("live cleanup retargeted: %v", err)
	}

	copyCleanup := *cleanup
	copyCleanup.Leave()
	cleanup.Leave()
	if _, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("same invocation renewed cleanup: %v", err)
	}
	if _, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("cleanup retargeted after leave: %v", err)
	}
}

func TestGeneratedMigrationCleanupRejectsMissingProvenance(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	if _, err := fixture.db.Exec(`DROP TRIGGER deployment_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	fixture.setDeploymentStatus(t, "applying", false)
	guard, err := NewGuard(fixture.db, fixture.bindings, &cleanupPositiveFence{db: fixture.db}, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	}()
	pin, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Leave()
	invocationID := uuid.NewString()
	cleanup, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Leave()

	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, ErrStageInvalid)
}

func TestGeneratedMigrationCleanupExpiryDoesNotRenew(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	guard, err := NewGuard(fixture.db, fixture.bindings, &cleanupPositiveFence{db: fixture.db}, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	}()
	pin, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Leave()
	invocationID := uuid.NewString()
	cleanup, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Leave()

	// Replace only the same-package test capability's bounded context with an
	// already elapsed deadline. The production derivation above has separately
	// established the fixed 70-minute absolute deadline.
	cleanup.state.mu.Lock()
	priorCancel := cleanup.state.cancel
	expired, cancelExpired := context.WithDeadline(context.WithoutCancel(cleanup.state.ctx), time.Unix(0, 0))
	cleanup.state.ctx = expired
	cleanup.state.cancel = func() {
		priorCancel()
		cancelExpired()
	}
	cleanup.state.mu.Unlock()

	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	checkErr := cleanup.CheckBeforeRemoveTx(tx, fixture.claim, fixture.deploymentID, invocationID)
	_ = tx.Rollback()
	if !errors.Is(checkErr, ErrCanceled) || !errors.Is(checkErr, context.DeadlineExceeded) {
		t.Fatalf("expired cleanup check = %v", checkErr)
	}
	cleanup.Leave()
	if _, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("expired invocation renewed cleanup: %v", err)
	}
	if _, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("expired cleanup retargeted: %v", err)
	}
}

func TestGeneratedMigrationCleanupRemainsUsableWhileOnlyParentCloses(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	guard, err := NewGuard(fixture.db, fixture.bindings, &cleanupPositiveFence{db: fixture.db}, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	}()
	pin, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), scope))
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Leave()
	invocationID := uuid.NewString()
	cleanup, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Leave()

	parentDone := make(chan struct{})
	go func() {
		pin.Leave()
		close(parentDone)
	}()
	waitForPinnedClosing(t, pin)
	assertCleanupCheckError(t, fixture, cleanup, fixture.claim, fixture.deploymentID, invocationID, nil)
	assertNotClosed(t, parentDone, "parent left while its cleanup child remained live")
	cleanup.Leave()
	select {
	case <-parentDone:
	case <-time.After(time.Second):
		t.Fatal("parent did not leave after cleanup child left")
	}
}

func TestGeneratedMigrationCleanupAndParentLeaveJoinActiveCheck(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	validator := &blockingPositiveFence{db: fixture.db, entered: make(chan context.Context, 1), continueCheck: make(chan struct{})}
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(validator.continueCheck) }) }
	defer unblock()
	guard, err := NewGuard(fixture.db, fixture.bindings, validator, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := guard.PinScope(deploymenteffects.WithScope(context.Background(), scope))
	if err != nil {
		t.Fatal(err)
	}
	invocationID := uuid.NewString()
	cleanup, err := pin.DeriveGeneratedMigrationCleanup(fixture.claim, fixture.deploymentID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	checkDone := make(chan error, 1)
	go func() {
		checkDone <- cleanup.CheckBeforeRemoveTx(tx, fixture.claim, fixture.deploymentID, invocationID)
	}()
	select {
	case <-validator.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup check did not reach validator")
	}

	childDone := make(chan struct{})
	go func() {
		cleanup.Leave()
		close(childDone)
	}()
	waitForCleanupClosing(t, cleanup)
	parentDone := make(chan struct{})
	go func() {
		pin.Leave()
		close(parentDone)
	}()
	waitForPinnedClosing(t, pin)
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- release() }()
	assertNotClosed(t, childDone, "cleanup left while check was active")
	assertNotClosed(t, parentDone, "parent left while cleanup was active")
	select {
	case err := <-releaseDone:
		t.Fatalf("outer scope released while cleanup was active: %v", err)
	default:
	}

	unblock()
	select {
	case checkErr := <-checkDone:
		if !errors.Is(checkErr, ErrCanceled) && !errors.Is(checkErr, ErrScopeUnavailable) {
			t.Fatalf("closing cleanup check = %v", checkErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup check did not return")
	}
	for name, done := range map[string]<-chan struct{}{"cleanup": childDone, "parent": parentDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s leave did not finish", name)
		}
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("outer scope did not release after cleanup joined")
	}
}

func assertCleanupCheckError(t *testing.T, fixture *concurrencyFixture, cleanup *GeneratedMigrationCleanup, claim Claim, deploymentID, invocationID string, want error) {
	t.Helper()
	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	checkErr := cleanup.CheckBeforeRemoveTx(tx, claim, deploymentID, invocationID)
	_ = tx.Rollback()
	if want == nil && checkErr != nil {
		t.Fatalf("cleanup check = %v", checkErr)
	}
	if want != nil && !errors.Is(checkErr, want) {
		t.Fatalf("cleanup check = %v want %v", checkErr, want)
	}
}

func assertNotClosed(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
		t.Fatal(message)
	default:
	}
}

func waitForCleanupClosing(t *testing.T, cleanup *GeneratedMigrationCleanup) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		cleanup.state.mu.Lock()
		closing := cleanup.state.closing
		cleanup.state.mu.Unlock()
		if closing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup Leave did not close new checks")
		}
		runtime.Gosched()
	}
}

func (f *concurrencyFixture) setJobState(t *testing.T, status, phase string, attempt int) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE jobs SET status=?,phase=?,attempt=? WHERE id=?`, status, phase, attempt, f.claim.jobID); err != nil {
		t.Fatal(err)
	}
}

func (f *concurrencyFixture) setDeploymentStatus(t *testing.T, status string, provenance bool) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE deployments SET status=?,provenance_initialized=? WHERE id=?`, status, provenance, f.deploymentID); err != nil {
		t.Fatal(err)
	}
}
