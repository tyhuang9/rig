package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindTypedCompletedWithoutReceiptWithdrawsSuccessor(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		name := "revoked after durable completion"
		if lostAck {
			name = "cancelled after lost completion acknowledgement"
		}
		t.Run(name, func(t *testing.T) { checkGatewayRebindUnreceiptedCompletionFailure(t, lostAck) })
	}
}

func checkGatewayRebindUnreceiptedCompletionFailure(t *testing.T, lostAck bool) {
	t.Helper()
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var retained gatewayRebindProtectedIntentHistory
	completed := false
	appendProgress := func(appendCtx context.Context, record gatewayRebindProgressRecord) error {
		if err := fixture.appendProgress(appendCtx, record); err != nil {
			return err
		}
		if record.Sequence == 17 {
			completed = true
			retained, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(retained.Progress) != 17 || len(retained.TerminalsV2) != 0 {
				t.Fatalf("completion fixture did not reach unreceipted history: %v", err)
			}
			update, err := fixture.predecessor.db.Exec(`UPDATE users SET role='viewer' WHERE id=? AND role='administrator'`,
				fixture.attempt.Claim.RebindApproval.ActorID)
			if err != nil {
				t.Fatal(err)
			}
			if affected, err := update.RowsAffected(); err != nil || affected != 1 {
				t.Fatalf("revocation affected=%d err=%v", affected, err)
			}
			fixture.runner.effects = nil
			if lostAck {
				cancel()
				return errors.New("injected lost completed-progress acknowledgement")
			}
		}
		return nil
	}
	result, reconcileErr := driver.reconcileSuccessorLocked(ctx, request, appendProgress)
	var diagnostic *Error
	if !completed || reconcileErr == nil || !errors.As(reconcileErr, &diagnostic) || diagnostic.candidateMayBeLive ||
		!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || fixture.runner.final.Running ||
		fixture.runner.predecessor.FinalContainer.Running || !fixture.predecessor.manager.gatewayRebindFailStopLatch().Load() ||
		!reflect.DeepEqual(fixture.runner.effects, [][]string{{"container", "stop", "--time", "10", fixture.runner.finalID}}) {
		t.Fatalf("completed unreceipted refusal: completed=%t final=%t predecessor=%t effects=%v err=%v",
			completed, fixture.runner.final.Running, fixture.runner.predecessor.FinalContainer.Running, fixture.runner.effects, reconcileErr)
	}
	after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(retained, history) ||
		fixture.predecessor.repository.CheckGatewayRebindFence(context.Background()) == nil || !fixture.runner.finalPresent ||
		!fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
		t.Fatalf("withdrawal changed recovery state or removed resources: SQL=%v history=%v", sqlErr, historyErr)
	}
}

func TestGatewayRebindTypedCompletedWithoutReceiptReplay(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	ctx := context.Background()
	physical, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionCommit {
		t.Fatalf("complete physical fixture: %v", err)
	}
	receipt, err := newGatewayRebindCommitTerminalV2(fixture.attempt.Intent, physical.Last, physical.Resources,
		physical.Proof, gatewayRebindTimeStrictlyAfter(fixture.predecessor.manager.gatewayRebindProgressTime(), physical.Last.OccurredAt))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"approved", "revoked", "revoked stopped replay", "approved stopped replay",
		"receipt not installed", "exact receipt installed", "conflicting receipt"} {
		t.Run(kind, func(t *testing.T) {
			fixture.predecessor.manager.gatewayRebindFailStopLatch().Store(false)
			fixture.runner.startFinal()
			fixture.runner.effects = nil
			role := "administrator"
			if kind == "revoked" || kind == "revoked stopped replay" {
				role = "viewer"
			}
			if _, err := fixture.predecessor.db.Exec(`UPDATE users SET role=? WHERE id=?`, role,
				fixture.attempt.Claim.RebindApproval.ActorID); err != nil {
				t.Fatal(err)
			}
			if kind == "revoked stopped replay" || kind == "approved stopped replay" {
				fixture.runner.stopFinal()
			}
			used := request
			if kind == "receipt not installed" || kind == "exact receipt installed" || kind == "conflicting receipt" {
				used.Mode, used.Terminal = gatewayRebindPhysicalReconcileForwardOnly, &receipt
			}
			if kind == "exact receipt installed" {
				store, err := newGatewayRebindTerminalStoreV2(fixture.predecessor.manager.options.DataRoot, receipt.Generation, receipt.OperationID)
				if err != nil || store.installExact(ctx, receipt) != nil {
					t.Fatal("could not install exact receipt")
				}
			}
			if kind == "conflicting receipt" {
				created, err := parseGatewayRebindProgressTime(receipt.CreatedAt)
				if err != nil {
					t.Fatal(err)
				}
				other, err := newGatewayRebindCommitTerminalV2(fixture.attempt.Intent, physical.Last,
					physical.Resources, physical.Proof, created.Add(time.Nanosecond))
				if err != nil || other.Digest == receipt.Digest {
					t.Fatal("could not construct distinct expected receipt")
				}
				used.Terminal = &other
			}
			before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil {
				t.Fatal(err)
			}
			var result gatewayRebindTypedPhysicalResult
			if used.Terminal != nil {
				err = driver.withdrawForwardSuccessorLocked(ctx, used)
			} else {
				result, err = driver.reconcileSuccessorLocked(ctx, used, fixture.appendProgress)
			}
			if kind == "approved" || kind == "approved stopped replay" {
				wantEffects := [][]string(nil)
				if kind == "approved stopped replay" {
					wantEffects = [][]string{{"container", "start", fixture.runner.finalID}}
				}
				if err != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit ||
					!fixture.runner.final.Running || !reflect.DeepEqual(fixture.runner.effects, wantEffects) {
					t.Fatalf("approved cached completion failed: result=%s err=%v", result.Disposition, err)
				}
			} else {
				var diagnostic *Error
				wantLive := kind == "conflicting receipt"
				wantStops := 1
				if wantLive || kind == "revoked stopped replay" || kind == "approved stopped replay" {
					wantStops = 0
				}
				if err == nil || !errors.As(err, &diagnostic) || diagnostic.candidateMayBeLive != wantLive ||
					fixture.runner.final.Running != wantLive || len(fixture.runner.effects) != wantStops ||
					!fixture.predecessor.manager.gatewayRebindFailStopLatch().Load() ||
					!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) {
					t.Fatalf("cached refusal final=%t effects=%v err=%v", fixture.runner.final.Running, fixture.runner.effects, err)
				}
				for _, effect := range fixture.runner.effects {
					if !reflect.DeepEqual(effect, []string{"container", "stop", "--time", "10", fixture.runner.finalID}) {
						t.Fatalf("unexpected effect: %v", effect)
					}
				}
			}
			after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
				fixture.runner.predecessor.FinalContainer.Running || fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil ||
				!fixture.runner.finalPresent || !fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
				t.Fatalf("replay changed history, fence or resources: SQL=%v history=%v", sqlErr, historyErr)
			}
		})
	}
}

type gatewayRebindAfterCompletedDriver struct {
	gatewayRebindCrossStoreDriver
	after       func(context.Context, gatewayRebindPhysicalReconcileRequest, *gatewayRebindTypedPhysicalResult)
	onProgress  func(gatewayRebindProgressRecord)
	withdrawals int
}

func (d *gatewayRebindAfterCompletedDriver) reconcileSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	physical, err := d.gatewayRebindCrossStoreDriver.reconcileSuccessorLocked(ctx, request,
		func(appendCtx context.Context, record gatewayRebindProgressRecord) error {
			if err := appendProgress(appendCtx, record); err != nil {
				return err
			}
			if d.onProgress != nil {
				d.onProgress(record)
			}
			return nil
		})
	if err == nil && physical.Disposition == appaccess.GatewayRebindDispositionCommit {
		d.after(ctx, request, &physical)
	}
	return physical, err
}

func (d *gatewayRebindAfterCompletedDriver) withdrawForwardSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest,
) error {
	d.withdrawals++
	return d.gatewayRebindCrossStoreDriver.withdrawForwardSuccessorLocked(ctx, request)
}

func TestGatewayRebindTypedRecoveryWithdrawsFailedReceiptInstallation(t *testing.T) {
	for _, installed := range []bool{false, true} {
		name := "absent receipt"
		if installed {
			name = "durable receipt after lost acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			completed := false
			var history gatewayRebindProtectedIntentHistory
			driver := &gatewayRebindAfterCompletedDriver{gatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{
				manager: fixture.predecessor.manager, stage: fixture.stageDriver, handover: fixture.runtime}}
			// Keep the existing simulated Docker inventory aligned with durable
			// progress; the coordinator itself still performs every actual append.
			driver.onProgress = func(record gatewayRebindProgressRecord) {
				if record.TypedEffect != nil {
					fixture.runner.effect = *record.TypedEffect
					fixture.runner.stage.effect = *record.TypedEffect
				}
			}
			driver.after = func(callCtx context.Context, request gatewayRebindPhysicalReconcileRequest, physical *gatewayRebindTypedPhysicalResult) {
				completed = true
				if installed {
					if _, err := fixture.predecessor.manager.installGatewayRebindTerminalForPhysicalLocked(callCtx, request.Attempt, *physical); err != nil {
						t.Fatal(err)
					}
				}
				history, err = fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if err != nil {
					t.Fatal(err)
				}
				fixture.runner.effects = nil
				cancel()
			}
			result, recoverErr := fixture.predecessor.manager.recoverGatewayRebindStartupWithDriver(ctx, fixture.predecessor.repository, driver)
			var diagnostic *Error
			if !completed || recoverErr == nil || !errors.As(recoverErr, &diagnostic) || diagnostic.candidateMayBeLive ||
				!reflect.DeepEqual(result, GatewayRebindStartupRecoveryResult{}) || fixture.runner.final.Running ||
				fixture.runner.predecessor.FinalContainer.Running || driver.withdrawals != 1 ||
				!reflect.DeepEqual(fixture.runner.effects, [][]string{{"container", "stop", "--time", "10", fixture.runner.finalID}}) {
				t.Fatalf("failed receipt recovery: completed=%t withdrawals=%d final=%t effects=%v err=%v",
					completed, driver.withdrawals, fixture.runner.final.Running, fixture.runner.effects, recoverErr)
			}
			after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
			retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			wantReceipts := 0
			if installed {
				wantReceipts = 1
			}
			if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
				len(retained.Progress) != 17 || len(retained.TerminalsV2) != wantReceipts ||
				fixture.predecessor.repository.CheckGatewayRebindFence(context.Background()) == nil ||
				!fixture.runner.finalPresent || !fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
				t.Fatalf("receipt failure changed history/fence/resources: SQL=%v history=%v", sqlErr, historyErr)
			}
		})
	}
}

func TestGatewayRebindCommitWithdrawsBeforeReceiptConstruction(t *testing.T) {
	f, input, bounded := newGatewayRebindCoordinatorFixture(t)
	driver := &gatewayRebindAfterCompletedDriver{gatewayRebindCrossStoreDriver: bounded,
		after: func(_ context.Context, _ gatewayRebindPhysicalReconcileRequest, result *gatewayRebindTypedPhysicalResult) {
			// Reject the returned completion before constructing its receipt.
			// The actual durable prefix remains the exact completed history.
			result.Proof.Digest = "invalid completion proof"
		}}
	result, err := f.manager.commitGatewayRebindWithDriver(context.Background(), f.repository, input, driver)
	if err == nil || result != (GatewayRebindCommitResult{}) || driver.withdrawals != 1 || bounded.withdrawCalls != 1 {
		t.Fatalf("pre-receipt failure bypassed withdrawal: calls=%d bounded=%d err=%v", driver.withdrawals, bounded.withdrawCalls, err)
	}
	snapshot, sqlErr := f.repository.GatewayRebindRecoverySnapshot(context.Background())
	history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if sqlErr != nil || historyErr != nil || snapshot.Phase != appaccess.GatewayRebindPrepared || snapshot.Active == nil ||
		len(history.Progress) != 17 || len(history.TerminalsV2) != 0 || f.repository.CheckGatewayRebindFence(context.Background()) == nil {
		t.Fatalf("pre-receipt failure changed recovery direction: SQL=%v history=%v", sqlErr, historyErr)
	}
}

func TestGatewayRebindTypedUndurableCompletionStillRollsBack(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	ctx := context.Background()
	refused := false
	result, err := driver.reconcileSuccessorLocked(ctx, request,
		func(appendCtx context.Context, record gatewayRebindProgressRecord) error {
			if record.Phase == gatewayRebindProgressHandoverCommitted {
				refused = true
				history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
				if err != nil || len(history.Progress) != 16 || len(history.TerminalsV2) != 0 || !fixture.runner.final.Running {
					t.Fatalf("refused completion did not retain serving prefix16: %v", err)
				}
				fixture.runner.effects = nil
				return errors.New("injected refusal before completion record write")
			}
			return fixture.appendProgress(appendCtx, record)
		})
	history, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if !refused || err != nil || result.Disposition != appaccess.GatewayRebindDispositionAbort ||
		result.Last.Sequence != 18 || result.Last.Phase != gatewayRebindProgressHandoverRolledBack ||
		historyErr != nil || len(history.Progress) != 18 || len(history.TerminalsV2) != 0 ||
		history.Progress[16].Record.Phase != gatewayRebindProgressRollbackIntent ||
		history.Progress[17].Record.Phase != gatewayRebindProgressHandoverRolledBack ||
		!fixture.runner.predecessor.FinalContainer.Running || fixture.runner.finalPresent ||
		fixture.runner.configPresent || fixture.runner.dataPresent || fixture.runner.networkPresent ||
		fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil {
		t.Fatalf("undurable completion bypassed rollback: refused=%t result=%s final=%t predecessor=%t err=%v history=%v",
			refused, result.Disposition, fixture.runner.finalPresent, fixture.runner.predecessor.FinalContainer.Running, err, historyErr)
	}
	starts := 0
	for _, effect := range fixture.runner.effects {
		if len(effect) >= 2 && effect[0] == "container" && effect[1] == "start" {
			starts++
			if len(effect) != 3 || normalizeID(effect[2]) != normalizeID(fixture.runner.predecessor.FinalContainer.ID) {
				t.Fatalf("rollback started another container: %v", effect)
			}
		}
	}
	if starts != 1 {
		t.Fatalf("rollback predecessor starts=%d", starts)
	}
}
