package generatedingress

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindRecoveryFailAttestationDriver struct {
	*gatewayRebindBoundedPhysicalDriver
}

type gatewayRebindRecoveryRejectTransitionRepository struct {
	*appaccess.Repository
}

type gatewayRebindRecoverySnapshotDriftRepository struct {
	*appaccess.Repository
	drift bool
}

type gatewayRebindRecoveryChangedNoEffectDriver struct {
	*gatewayRebindBoundedPhysicalDriver
	changeObservation bool
	afterProof        func(appaccess.GatewayRebindClaimV2, gatewayRebindPredecessorCheckpoint)
}

func (r *gatewayRebindRecoveryRejectTransitionRepository) ApplyGatewayRebindTransition(context.Context,
	appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	return appaccess.GatewayRebindTransitionCommand{}, errors.New("injected interruption before rollback SQL")
}

func (r *gatewayRebindRecoverySnapshotDriftRepository) GatewayRebindRecoverySnapshot(ctx context.Context,
) (appaccess.GatewayRebindRecoverySnapshot, error) {
	snapshot, err := r.Repository.GatewayRebindRecoverySnapshot(ctx)
	if err == nil && r.drift && snapshot.CurrentSource != nil {
		changed := *snapshot.CurrentSource
		changed.ProfileSpecDigest = strings.Repeat("f", 64)
		snapshot.CurrentSource = &changed
	}
	return snapshot, err
}

func (d *gatewayRebindRecoveryChangedNoEffectDriver) proveNoSuccessorEffectsLocked(ctx context.Context,
	claim appaccess.GatewayRebindClaimV2, roster []appaccess.GatewayRebindRosterEntryV2,
	checkpoint gatewayRebindPredecessorCheckpoint,
) (gatewayRebindNoEffectAbortProof, error) {
	proof, err := d.gatewayRebindBoundedPhysicalDriver.proveNoSuccessorEffectsLocked(ctx, claim, roster, checkpoint)
	if err != nil {
		return gatewayRebindNoEffectAbortProof{}, err
	}
	if d.afterProof != nil {
		d.afterProof(claim, checkpoint)
	}
	if d.changeObservation {
		proof.ObservationDigest = strings.Repeat("8", 64)
		proof.Digest, err = gatewayRebindNoEffectAbortProofDigest(proof)
	}
	return proof, err
}

func TestRecoverGatewayRebindStartupRefusesFirstNoEffectAbortAfterSnapshotDrift(t *testing.T) {
	fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
	fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
		return errors.New("injected crash after SQL prepared claim")
	}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("post-claim interruption unexpectedly completed")
	}
	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	fresh.gatewayRebindFailStop = &atomic.Bool{}
	fresh.gatewayRebindCommitBarrier = &atomic.Bool{}
	fresh.gatewayRebindV2NetworkObserver = func(context.Context,
		appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("successor network unavailable after restart")
	}
	drifting := &gatewayRebindRecoverySnapshotDriftRepository{Repository: fixture.repository}
	driftDriver := &gatewayRebindRecoveryChangedNoEffectDriver{
		gatewayRebindBoundedPhysicalDriver: &gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template},
		afterProof: func(appaccess.GatewayRebindClaimV2, gatewayRebindPredecessorCheckpoint) {
			drifting.drift = true
		},
	}
	if result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), drifting,
		driftDriver); err == nil || result != (GatewayRebindStartupRecoveryResult{}) {
		t.Fatalf("first no-effect snapshot drift result=%#v error=%v", result, err)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
		fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
		t.Fatalf("first no-effect snapshot drift released fence: %#v error=%v", snapshot, err)
	}
	history, err := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.TerminalsV2) != 0 {
		t.Fatalf("first no-effect snapshot drift created terminal: terminals=%d error=%v",
			len(history.TerminalsV2), err)
	}
}

func (d *gatewayRebindRecoveryFailAttestationDriver) attestCommittedCurrentLocked(context.Context,
	gatewayCurrentSelection,
) (string, error) {
	return "", errors.New("injected committed-current attestation interruption")
}

func freshGatewayRebindRecoveryManager(source *Manager) *Manager {
	return &Manager{runner: source.runner, store: source.store, options: source.options,
		dockerEnv:                append([]string(nil), source.dockerEnv...),
		workingDirectoryIdentity: source.workingDirectoryIdentity, mu: newContextMutex(),
		gatewayRebindFailStop:      source.gatewayRebindFailStopLatch(),
		gatewayRebindCommitBarrier: source.gatewayRebindCommitBarrierLatch()}
}

func TestRecoverGatewayRebindStartupNoHistoryOwnsAndReleasesAdmissionLocks(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	result, err := fixture.manager.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
		&gatewayRebindBoundedPhysicalDriver{t: t})
	if err != nil || result.RebindHistoryPresent || result.Recovered || !result.FenceReleased ||
		result.SelectedCurrentAuthority != nil {
		t.Fatalf("no-history recovery=%#v error=%v", result, err)
	}
	if release, lockErr := fixture.manager.lockGatewayRaw(context.Background()); lockErr != nil {
		t.Fatalf("gateway lock remained held after no-history recovery: %v", lockErr)
	} else if releaseErr := release(); releaseErr != nil {
		t.Fatalf("release gateway lock after no-history recovery: %v", releaseErr)
	}
}

func TestRecoverGatewayRebindStartupRepairsPreparedClaimThenSafelyAbortsNoEffects(t *testing.T) {
	fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
	fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
		return errors.New("injected crash after SQL prepared claim")
	}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("post-claim interruption unexpectedly completed")
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared {
		t.Fatalf("interrupted prepared snapshot=%#v error=%v", snapshot, err)
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Checkpoints) != 0 || len(history.IntentsV2) != 0 {
		t.Fatalf("post-claim boundary wrote protected admission evidence: %#v error=%v", history, err)
	}

	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	fresh.gatewayRebindV2NetworkObserver = func(context.Context,
		appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		return gatewayRebindSuccessorNetworkObservation{}, errors.New("successor network unavailable after restart")
	}
	recoveryDriver := &gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template}
	result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository, recoveryDriver)
	if err != nil || !result.RebindHistoryPresent || !result.Recovered ||
		result.InitialActivePhase != appaccess.GatewayRebindPrepared ||
		result.FinalActivePhase != appaccess.GatewayRebindRolledBack ||
		result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased ||
		result.SelectedCurrentAuthority == nil || recoveryDriver.abortCalls != 1 || recoveryDriver.commitCalls != 0 {
		t.Fatalf("prepared startup rollback=%#v calls=%d/%d error=%v",
			result, recoveryDriver.abortCalls, recoveryDriver.commitCalls, err)
	}
	confirmed, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || confirmed.Active != nil || fixture.repository.CheckGatewayRebindFence(context.Background()) != nil {
		t.Fatalf("prepared startup rollback retained fence: %#v error=%v", confirmed, err)
	}
}

func TestRecoverGatewayRebindStartupReprovesRetainedNoEffectReceiptBeforeSQLRollback(t *testing.T) {
	newInterrupted := func(t *testing.T) (gatewayRebindPredecessorFixture, gatewayCurrentStateFixture) {
		t.Helper()
		fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
		fixture.manager.gatewayRebindAfterClaim = func(context.Context, appaccess.GatewayRebindClaimV2) error {
			return errors.New("injected crash after SQL prepared claim")
		}
		if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
			input, driver); err == nil {
			t.Fatal("post-claim interruption unexpectedly completed")
		}
		interrupted := freshGatewayRebindRecoveryManager(fixture.manager)
		interrupted.gatewayRebindFailStop = &atomic.Bool{}
		interrupted.gatewayRebindCommitBarrier = &atomic.Bool{}
		interrupted.gatewayRebindV2NetworkObserver = func(context.Context,
			appaccess.GatewayRebindClaimV2,
		) (gatewayRebindSuccessorNetworkObservation, error) {
			return gatewayRebindSuccessorNetworkObservation{}, errors.New("successor network unavailable after restart")
		}
		rejecting := &gatewayRebindRecoveryRejectTransitionRepository{Repository: fixture.repository}
		if result, err := interrupted.recoverGatewayRebindStartupWithDriver(context.Background(), rejecting,
			&gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template}); err == nil ||
			result != (GatewayRebindStartupRecoveryResult{}) {
			t.Fatalf("receipt crash boundary result=%#v error=%v", result, err)
		}
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
			fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
			t.Fatalf("receipt crash boundary lost prepared fence: %#v error=%v", snapshot, err)
		}
		history, err := interrupted.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(history.TerminalsV2) != 1 || history.TerminalsV2[0].Receipt.NoEffectProof == nil {
			t.Fatalf("receipt crash boundary history=%#v error=%v", history, err)
		}
		return fixture, driver.template
	}

	t.Run("matching fresh proof releases rollback fence", func(t *testing.T) {
		fixture, template := newInterrupted(t)
		fresh := freshGatewayRebindRecoveryManager(fixture.manager)
		fresh.gatewayRebindFailStop = &atomic.Bool{}
		fresh.gatewayRebindCommitBarrier = &atomic.Bool{}
		driver := &gatewayRebindBoundedPhysicalDriver{t: t, template: template}
		result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository, driver)
		if err != nil || result.FinalActivePhase != appaccess.GatewayRebindRolledBack ||
			result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased ||
			driver.abortCalls != 1 {
			t.Fatalf("fresh no-effect proof result=%#v calls=%d error=%v", result, driver.abortCalls, err)
		}
	})

	t.Run("changed fresh observation retains rollback fence", func(t *testing.T) {
		fixture, template := newInterrupted(t)
		fresh := freshGatewayRebindRecoveryManager(fixture.manager)
		fresh.gatewayRebindFailStop = &atomic.Bool{}
		fresh.gatewayRebindCommitBarrier = &atomic.Bool{}
		driver := &gatewayRebindRecoveryChangedNoEffectDriver{gatewayRebindBoundedPhysicalDriver: &gatewayRebindBoundedPhysicalDriver{t: t, template: template}}
		driver.changeObservation = true
		if result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
			driver); err == nil || result != (GatewayRebindStartupRecoveryResult{}) {
			t.Fatalf("changed no-effect observation result=%#v error=%v", result, err)
		}
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
			fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
			t.Fatalf("changed no-effect observation released fence: %#v error=%v", snapshot, err)
		}
	})

	t.Run("protected terminal removal after proof retains rollback fence", func(t *testing.T) {
		fixture, template := newInterrupted(t)
		history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err != nil || len(history.TerminalsV2) != 1 || history.TerminalsV2[0].Store == nil {
			t.Fatalf("retained no-effect terminal is unavailable: %#v error=%v", history, err)
		}
		terminalPath := history.TerminalsV2[0].Store.path
		fresh := freshGatewayRebindRecoveryManager(fixture.manager)
		fresh.gatewayRebindFailStop = &atomic.Bool{}
		fresh.gatewayRebindCommitBarrier = &atomic.Bool{}
		driver := &gatewayRebindRecoveryChangedNoEffectDriver{
			gatewayRebindBoundedPhysicalDriver: &gatewayRebindBoundedPhysicalDriver{t: t, template: template},
			afterProof: func(appaccess.GatewayRebindClaimV2, gatewayRebindPredecessorCheckpoint) {
				if removeErr := os.Remove(terminalPath); removeErr != nil {
					t.Fatalf("remove retained no-effect terminal after proof: %v", removeErr)
				}
			},
		}
		if result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
			driver); err == nil || result != (GatewayRebindStartupRecoveryResult{}) {
			t.Fatalf("protected terminal removal result=%#v error=%v", result, err)
		}
		snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindPrepared ||
			fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
			t.Fatalf("protected terminal removal released fence: %#v error=%v", snapshot, err)
		}
	})
}

func TestRecoverGatewayRebindStartupCompletesDatabaseCommittedAttemptAndReattestsCurrent(t *testing.T) {
	fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
	interrupted := &gatewayRebindRecoveryFailAttestationDriver{gatewayRebindBoundedPhysicalDriver: driver}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, interrupted); err == nil {
		t.Fatal("injected database-committed interruption unexpectedly completed")
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindDatabaseCommitted ||
		!snapshot.DatabaseCommitObserved || snapshot.RollbackAllowed {
		t.Fatalf("database-committed interruption snapshot=%#v error=%v", snapshot, err)
	}

	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	retirement, retirementRunner := installGatewayRebindStrictPredecessorRetirementForSelectedCurrent(t, fresh)
	recoveryDriver := &gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template}
	result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository, recoveryDriver)
	if err != nil || !result.RebindHistoryPresent || !result.Recovered ||
		result.InitialActivePhase != appaccess.GatewayRebindDatabaseCommitted ||
		result.FinalActivePhase != appaccess.GatewayRebindCommitted ||
		result.Disposition != appaccess.GatewayRebindDispositionCommit || !result.FenceReleased ||
		result.SelectedCurrentAuthority == nil || result.SelectedCurrentAuthority.OperationID != input.Inspection.Spec.OperationID ||
		result.CurrentStateVersion != gatewayCurrentRouteStateVersion || result.CurrentStateRevision == 0 ||
		!validSHA256(result.CurrentStateDigest) || !validSHA256(result.CurrentAttestationDigest) ||
		recoveryDriver.commitCalls != 1 || recoveryDriver.attestCalls != 2 || retirement.retireCalls != 1 ||
		len(retirementRunner.effects) != 0 {
		history, historyErr := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		attempt, attemptErr := gatewayRebindPreparedAttemptFromHistory(snapshot, history)
		terminal, terminalErr := gatewayRebindActiveTerminalV2(history, *snapshot.Active.Claim.V2)
		requestValid := false
		if terminal != nil && attemptErr == nil {
			requestValid = validGatewayRebindPhysicalReconcileRequest(gatewayRebindPhysicalReconcileRequest{
				Attempt: attempt, Mode: gatewayRebindPhysicalReconcileForwardOnly,
				SQLPhase: snapshot.Phase, RollbackAllowed: snapshot.RollbackAllowed,
				DatabaseCommitObserved: snapshot.DatabaseCommitObserved, Terminal: terminal})
		}
		t.Fatalf("database-committed startup recovery=%#v calls=%d/%d error=%v historyErr=%v attemptErr=%v terminalErr=%v terminal=%t requestValid=%t",
			result, recoveryDriver.commitCalls, recoveryDriver.attestCalls, err, historyErr, attemptErr,
			terminalErr, terminal != nil, requestValid)
	}
	confirmed, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || confirmed.Active != nil || fixture.repository.CheckGatewayRebindFence(context.Background()) != nil {
		t.Fatalf("committed startup recovery retained fence: %#v error=%v", confirmed, err)
	}
	restart := freshGatewayRebindRecoveryManager(fresh)
	restartRetirement, restartRunner := installGatewayRebindStrictPredecessorRetirementForSelectedCurrent(t, restart)
	restartDriver := &gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template}
	restarted, err := restart.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
		restartDriver)
	if err != nil || !restarted.RebindHistoryPresent || restarted.Recovered || !restarted.FenceReleased ||
		restarted.SelectedCurrentAuthority == nil || restarted.CurrentStateRevision == 0 ||
		!validSHA256(restarted.CurrentAttestationDigest) || restartDriver.commitCalls != 0 ||
		restartRetirement.retireCalls != 1 || len(restartRunner.effects) != 0 ||
		restartDriver.attestCalls != 1 {
		t.Fatalf("committed normal restart=%#v calls=%d/%d error=%v",
			restarted, restartDriver.commitCalls, restartDriver.attestCalls, err)
	}
}

func TestRecoverGatewayRebindStartupDatabaseCommitIsForwardOnlyBeforePhysicalEffects(t *testing.T) {
	fixture, input, driver := newGatewayRebindCoordinatorFixture(t)
	interrupted := &gatewayRebindRecoveryFailAttestationDriver{gatewayRebindBoundedPhysicalDriver: driver}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, interrupted); err == nil {
		t.Fatal("injected database-committed interruption unexpectedly completed")
	}
	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	rollbackDriver := &gatewayRebindBoundedPhysicalDriver{t: t, template: driver.template, rollback: true}
	if result, err := fresh.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
		rollbackDriver); err == nil || result != (GatewayRebindStartupRecoveryResult{}) {
		t.Fatalf("database-committed recovery accepted rollback result=%#v error=%v", result, err)
	}
	if rollbackDriver.lastRequest.Mode != gatewayRebindPhysicalReconcileForwardOnly ||
		rollbackDriver.lastRequest.SQLPhase != appaccess.GatewayRebindDatabaseCommitted ||
		rollbackDriver.lastRequest.Terminal == nil ||
		rollbackDriver.lastRequest.Terminal.Disposition != appaccess.GatewayRebindDispositionCommit {
		t.Fatalf("database-committed physical authority=%#v", rollbackDriver.lastRequest)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != appaccess.GatewayRebindDatabaseCommitted ||
		snapshot.RollbackAllowed || !snapshot.DatabaseCommitObserved ||
		fixture.repository.CheckGatewayRebindFence(context.Background()) == nil {
		t.Fatalf("forward-only refusal changed database-committed fence: %#v error=%v", snapshot, err)
	}
}

func TestRecoverGatewayRebindStartupReleaseFailureLatchesEveryManager(t *testing.T) {
	fixture := newGatewayRebindPredecessorFixtureWithClaim(t, false)
	fixture.manager.gatewayRebindFailStop = &atomic.Bool{}
	fixture.manager.gatewayRebindCommitBarrier = &atomic.Bool{}
	original := gatewayRebindAcquireDeploymentEffects
	gatewayRebindAcquireDeploymentEffects = func(context.Context, string) (func() error, error) {
		return func() error { return errors.New("injected startup effects release failure") }, nil
	}
	t.Cleanup(func() { gatewayRebindAcquireDeploymentEffects = original })
	result, err := fixture.manager.recoverGatewayRebindStartupWithDriver(context.Background(), fixture.repository,
		&gatewayRebindBoundedPhysicalDriver{t: t})
	if err == nil || result != (GatewayRebindStartupRecoveryResult{}) ||
		!fixture.manager.gatewayRebindFailStopLatch().Load() {
		t.Fatalf("startup release failure result=%#v failStop=%t error=%v",
			result, fixture.manager.gatewayRebindFailStopLatch().Load(), err)
	}
	fresh := freshGatewayRebindRecoveryManager(fixture.manager)
	if release, lockErr := fresh.lockGatewayRaw(context.Background()); lockErr == nil || release != nil {
		if release != nil {
			_ = release()
		}
		t.Fatalf("fresh Manager crossed startup release fail-stop: release=%t error=%v", release != nil, lockErr)
	}
}
