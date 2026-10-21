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

type candidateCleanupCheck func(*sql.Tx, Claim, string, string, string, string) error

func TestGeneratedCandidatePositiveChecksUseFixedMainStagesAndFence(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	validator := &cleanupPositiveFence{db: fixture.db}
	pin := pinCandidateScope(t, fixture, context.Background(), validator)
	checks := []struct {
		name        string
		check       func(*sql.Tx, Claim, string) error
		waitingWant error
	}{
		{name: "create", check: pin.CheckBeforeCandidateCreateTx, waitingWant: ErrStageInvalid},
		{name: "start", check: pin.CheckBeforeCandidateStartTx, waitingWant: ErrStageInvalid},
		{name: "observation", check: pin.CheckCandidateObservationTx},
		{name: "result", check: pin.CheckCandidateResultTx},
	}

	fixture.setDeploymentStatus(t, "applying", true)
	for _, test := range checks {
		assertCandidatePinCheck(t, fixture, test.name+" applying", test.check, nil)
	}
	fixture.setDeploymentStatus(t, "waiting_health", true)
	for _, test := range checks {
		assertCandidatePinCheck(t, fixture, test.name+" waiting health", test.check, test.waitingWant)
	}
	fixture.setDeploymentStatus(t, "applying", true)
	validator.pending = true
	for _, test := range checks {
		assertCandidatePinCheck(t, fixture, test.name+" pending start", test.check, ErrPositiveFence)
	}
}

func TestGeneratedCandidateCleanupBindsExactIdentityAndCancellationState(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	original, cancelOriginal := context.WithCancel(context.Background())
	pin := pinCandidateScope(t, fixture, original, &cleanupPositiveFence{db: fixture.db})
	componentName, receiptID, invocationID := "api", uuid.NewString(), uuid.NewString()
	cleanup, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup.Leave)
	deadline, ok := cleanup.state.ctx.Deadline()
	remaining := time.Until(deadline)
	if !ok || remaining <= 69*time.Minute || remaining > generatedCandidateCleanupTimeout {
		t.Fatalf("cleanup deadline ok=%t remaining=%s", ok, remaining)
	}

	for _, check := range []candidateCleanupCheck{cleanup.CheckBeforeStopTx, cleanup.CheckBeforeRemoveTx, cleanup.CheckRemovedObservationTx} {
		assertCandidateCleanupCheck(t, fixture, check, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, nil)
	}
	fixture.setDeploymentStatus(t, "waiting_health", true)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, nil)

	// The bounded child keeps the original scope identity without inheriting
	// cancellation, and cleanup is allowed only in the exact cancellation state.
	cancelOriginal()
	fixture.setJobState(t, "waiting_external", "cancelling", 1)
	for _, check := range []candidateCleanupCheck{cleanup.CheckBeforeStopTx, cleanup.CheckBeforeRemoveTx, cleanup.CheckRemovedObservationTx} {
		assertCandidateCleanupCheck(t, fixture, check, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, nil)
	}

	wrongAttempt, err := NewClaim(fixture.claim.binding, fixture.claim.jobID, fixture.claim.applicationID, fixture.claim.attempt+1)
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, wrongAttempt, fixture.deploymentID, componentName, receiptID, invocationID, ErrIdentityLost)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, fixture.claim, uuid.NewString(), componentName, receiptID, invocationID, ErrIdentityLost)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, fixture.claim, fixture.deploymentID, "worker", receiptID, invocationID, ErrIdentityLost)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, fixture.claim, fixture.deploymentID, componentName, uuid.NewString(), invocationID, ErrIdentityLost)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, fixture.claim, fixture.deploymentID, componentName, receiptID, uuid.NewString(), ErrIdentityLost)

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
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeStopTx, foreignClaim, fixture.deploymentID, componentName, receiptID, invocationID, ErrIdentityLost)
}

func TestGeneratedCandidateCleanupRejectsJobStageAndFenceDrift(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	validator := &cleanupPositiveFence{db: fixture.db}
	pin := pinCandidateScope(t, fixture, context.Background(), validator)
	componentName, receiptID, invocationID := "api", uuid.NewString(), uuid.NewString()
	cleanup, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup.Leave)

	fixture.setJobState(t, "running", "cancelling", 1)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeRemoveTx, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, ErrIdentityLost)
	fixture.setJobState(t, "running", "running", 2)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeRemoveTx, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, ErrIdentityLost)
	fixture.setJobState(t, "running", "running", 1)
	fixture.setDeploymentStatus(t, "succeeded", true)
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeRemoveTx, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, ErrStageInvalid)
	fixture.setDeploymentStatus(t, "applying", true)
	validator.pending = true
	assertCandidateCleanupCheck(t, fixture, cleanup.CheckBeforeRemoveTx, fixture.claim, fixture.deploymentID, componentName, receiptID, invocationID, ErrPositiveFence)
}

func TestGeneratedCandidateCleanupSpendsTwoSiblingSlotsWithoutRenewal(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	pin := pinCandidateScope(t, fixture, context.Background(), &cleanupPositiveFence{db: fixture.db})
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, " api", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("invalid component derivation error=%v", err)
	}
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", "not-a-receipt", uuid.NewString()); !errors.Is(err, ErrInvalidClaim) {
		t.Fatalf("invalid receipt derivation error=%v", err)
	}

	firstReceipt, firstInvocation := uuid.NewString(), uuid.NewString()
	first, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", firstReceipt, firstInvocation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Leave)
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "worker", firstReceipt, uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("receipt reused across siblings: %v", err)
	}
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "worker", uuid.NewString(), firstInvocation); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("invocation reused across siblings: %v", err)
	}
	changedClaim, err := NewClaim(fixture.claim.binding, fixture.claim.jobID, fixture.claim.applicationID, fixture.claim.attempt+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pin.DeriveGeneratedCandidateCleanup(changedClaim, fixture.deploymentID, "worker", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("sibling retargeted to another claim: %v", err)
	}

	second, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "worker", uuid.NewString(), uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Leave)
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "admin", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("third sibling derived: %v", err)
	}
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, uuid.NewString(), "api", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("cleanup retargeted to another deployment: %v", err)
	}
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("live component slot renewed: %v", err)
	}

	copyFirst := *first
	copyFirst.Leave()
	first.Leave()
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("left component slot renewed: %v", err)
	}
	assertCandidateCleanupCheck(t, fixture, second.CheckBeforeRemoveTx, fixture.claim, fixture.deploymentID, "worker", second.state.receiptID, second.state.invocationID, nil)
}

func TestGeneratedCandidateCleanupExpiryDoesNotRenew(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	pin := pinCandidateScope(t, fixture, context.Background(), &cleanupPositiveFence{db: fixture.db})
	receiptID, invocationID := uuid.NewString(), uuid.NewString()
	cleanup, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", receiptID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup.Leave)

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
	checkErr := cleanup.CheckBeforeStopTx(tx, fixture.claim, fixture.deploymentID, "api", receiptID, invocationID)
	_ = tx.Rollback()
	if !errors.Is(checkErr, ErrCanceled) || !errors.Is(checkErr, context.DeadlineExceeded) {
		t.Fatalf("expired cleanup check=%v", checkErr)
	}
	cleanup.Leave()
	if _, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", uuid.NewString(), uuid.NewString()); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("expired component slot renewed: %v", err)
	}
}

func TestGeneratedCandidateCleanupAndParentLeaveJoinTwoSiblingsAndActiveCheck(t *testing.T) {
	fixture := newConcurrencyFixture(t)
	fixture.setDeploymentStatus(t, "applying", true)
	validator := &blockingPositiveFence{db: fixture.db, entered: make(chan context.Context, 1), continueCheck: make(chan struct{})}
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(validator.continueCheck) }) }
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
		_ = release()
		t.Fatal(err)
	}
	first, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "api", uuid.NewString(), uuid.NewString())
	if err != nil {
		pin.Leave()
		_ = release()
		t.Fatal(err)
	}
	second, err := pin.DeriveGeneratedCandidateCleanup(fixture.claim, fixture.deploymentID, "worker", uuid.NewString(), uuid.NewString())
	if err != nil {
		first.Leave()
		pin.Leave()
		_ = release()
		t.Fatal(err)
	}
	releaseFinished := false
	var releaseDone chan error
	defer func() {
		unblock()
		first.Leave()
		second.Leave()
		pin.Leave()
		if releaseDone == nil {
			_ = release()
		} else if !releaseFinished {
			<-releaseDone
		}
	}()

	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	checkDone := make(chan error, 1)
	go func() {
		checkDone <- first.CheckBeforeStopTx(tx, fixture.claim, fixture.deploymentID, "api", first.state.receiptID, first.state.invocationID)
	}()
	select {
	case <-validator.entered:
	case <-time.After(time.Second):
		t.Fatal("candidate cleanup check did not reach validator")
	}

	firstDone := make(chan struct{})
	go func() {
		first.Leave()
		close(firstDone)
	}()
	waitForCandidateCleanupClosing(t, first)
	parentDone := make(chan struct{})
	go func() {
		pin.Leave()
		close(parentDone)
	}()
	waitForPinnedClosing(t, pin)
	releaseDone = make(chan error, 1)
	go func() { releaseDone <- release() }()
	assertNotClosed(t, firstDone, "first cleanup left while its check was active")
	assertNotClosed(t, parentDone, "parent left while candidate children were active")
	select {
	case err := <-releaseDone:
		t.Fatalf("outer scope released while candidate children were active: %v", err)
	default:
	}

	second.Leave()
	assertNotClosed(t, parentDone, "parent left while first candidate cleanup was active")
	unblock()
	select {
	case checkErr := <-checkDone:
		if !errors.Is(checkErr, ErrCanceled) && !errors.Is(checkErr, ErrScopeUnavailable) {
			t.Fatalf("closing candidate cleanup check=%v", checkErr)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate cleanup check did not return")
	}
	for name, done := range map[string]<-chan struct{}{"first cleanup": firstDone, "parent": parentDone} {
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
		releaseFinished = true
	case <-time.After(time.Second):
		t.Fatal("outer scope did not release after candidate cleanup joined")
	}
}

func pinCandidateScope(t *testing.T, fixture *concurrencyFixture, ctx context.Context, validator PositiveFenceTxValidator) *Pinned {
	t.Helper()
	guard, err := NewGuard(fixture.db, fixture.bindings, validator, fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	})
	pin, err := guard.PinScope(deploymenteffects.WithScope(ctx, scope))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pin.Leave)
	return pin
}

func assertCandidatePinCheck(t *testing.T, fixture *concurrencyFixture, name string, check func(*sql.Tx, Claim, string) error, want error) {
	t.Helper()
	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	checkErr := check(tx, fixture.claim, fixture.deploymentID)
	_ = tx.Rollback()
	if want == nil && checkErr != nil {
		t.Fatalf("%s check=%v", name, checkErr)
	}
	if want != nil && !errors.Is(checkErr, want) {
		t.Fatalf("%s check=%v want %v", name, checkErr, want)
	}
}

func assertCandidateCleanupCheck(t *testing.T, fixture *concurrencyFixture, check candidateCleanupCheck, claim Claim, deploymentID, componentName, receiptID, invocationID string, want error) {
	t.Helper()
	tx, err := fixture.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	checkErr := check(tx, claim, deploymentID, componentName, receiptID, invocationID)
	_ = tx.Rollback()
	if want == nil && checkErr != nil {
		t.Fatalf("candidate cleanup check=%v", checkErr)
	}
	if want != nil && !errors.Is(checkErr, want) {
		t.Fatalf("candidate cleanup check=%v want %v", checkErr, want)
	}
}

func waitForCandidateCleanupClosing(t *testing.T, cleanup *GeneratedCandidateCleanup) {
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
			t.Fatal("candidate cleanup Leave did not close new checks")
		}
		runtime.Gosched()
	}
}
