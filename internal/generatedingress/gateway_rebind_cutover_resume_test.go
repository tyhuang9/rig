package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestGatewayRebindTypedHandoverRuntimeRecoversAfterPredecessorStopAcknowledgementLoss(t *testing.T) {
	t.Run("fresh startup starts only the retained final container", func(t *testing.T) {
		fixture, driver := newGatewayRebindStoppedCutoverCompositionFixture(t)
		beforeSQL, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		beforeFiles, err := readGatewayHistorySnapshotMode(fixture.manager.store, true)
		if err != nil {
			t.Fatal(err)
		}
		effectStart := len(driver.runner.effects)
		fresh := newGatewayRebindCompositionRecoveryProcess(t, fixture.manager, driver.runner)
		recovered, recoverErr := fresh.RecoverGatewayRebindStartup(context.Background(), fixture.repository)
		history, historyErr := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		afterSQL, sqlErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
		confirmed, currentErr := fresh.InspectGatewayRebindCurrent(context.Background(), fixture.repository)
		wantEffects := [][]string{{"container", "start", driver.runner.finalID}}
		if recoverErr != nil || historyErr != nil || sqlErr != nil || currentErr != nil ||
			!recovered.Recovered || recovered.InitialActivePhase != appaccess.GatewayRebindPrepared ||
			recovered.FinalActivePhase != appaccess.GatewayRebindCommitted ||
			recovered.Disposition != appaccess.GatewayRebindDispositionCommit || !recovered.FenceReleased ||
			recovered.SelectedCurrentAuthority == nil || !validSHA256(recovered.CurrentStateDigest) ||
			!validSHA256(recovered.CurrentAttestationDigest) || fresh.gatewayRebindAdmissionBlocked() ||
			len(history.Progress) != 17 || history.Progress[16].Record.Phase != gatewayRebindProgressHandoverCommitted ||
			len(history.TerminalsV2) != 1 || afterSQL.Active != nil ||
			fixture.repository.CheckGatewayRebindFence(context.Background()) != nil ||
			confirmed.ActiveOperationID != "" || !confirmed.FenceReleased ||
			confirmed.SelectedCurrentAuthority != *recovered.SelectedCurrentAuthority ||
			confirmed.CurrentStateDigest != recovered.CurrentStateDigest ||
			!driver.runner.finalPresent || !driver.runner.final.Running ||
			driver.runner.predecessor.FinalContainer.Running || driver.runner.stagePresent ||
			!reflect.DeepEqual(driver.runner.effects[effectStart:], wantEffects) {
			t.Fatalf("stopped-cutover recovery=%#v errors=%v/%v/%v/%v progress=%d terminals=%d active=%t fence=%v current=%#v predecessorRunning=%t stage=%t final=%t/%t effects=%v",
				recovered, recoverErr, historyErr, sqlErr, currentErr, len(history.Progress), len(history.TerminalsV2),
				afterSQL.Active != nil, fixture.repository.CheckGatewayRebindFence(context.Background()), confirmed,
				driver.runner.predecessor.FinalContainer.Running, driver.runner.stagePresent,
				driver.runner.finalPresent, driver.runner.final.Running, driver.runner.effects[effectStart:])
		}
		requireGatewayRebindCompositionSQLPrefix(t, beforeSQL, afterSQL)
		gatewayRebindSequenceRequireRetainedFiles(t, fresh, beforeFiles)
		if got := gatewayRebindCutoverExactEffectCount(driver.runner.effects,
			[]string{"container", "stop", "--time", "10", normalizeID(driver.runner.predecessor.FinalContainer.ID)}); got != 1 {
			t.Fatalf("predecessor stop count=%d effects=%v", got, driver.runner.effects)
		}
	})

	t.Run("forward guard and rollback-only mode cannot adopt the stopped state", func(t *testing.T) {
		fixture, driver := newGatewayRebindStoppedCutoverCompositionFixture(t)
		fresh, recoveryDriver := newGatewayRebindCompositionRecoveryProcessWithDriver(t, fixture.manager, driver.runner)
		request := gatewayRebindStoppedCutoverRequest(t, fresh, fixture.repository)
		beforeSQL, beforeHistory, beforeFiles := gatewayRebindStoppedCutoverState(t, fresh, fixture.repository)
		effectStart := len(driver.runner.effects)
		guardCalls, writes := 0, 0
		recoveryDriver.forwardSessionGuard = func(context.Context) error {
			guardCalls++
			return errors.New("injected stale forward authority")
		}
		appendProgress := func(context.Context, gatewayRebindProgressRecord) error {
			writes++
			return errors.New("unexpected progress write")
		}
		result, reconcileErr := recoveryDriver.reconcileSuccessorForwardLocked(context.Background(), request, appendProgress)
		gatewayRebindRequireStoppedCutoverReadOnly(t, fresh, fixture.repository, driver.runner,
			beforeSQL, beforeHistory, beforeFiles, effectStart)
		if reconcileErr == nil || !reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || guardCalls == 0 || writes != 0 {
			t.Fatalf("stale forward guard result=%#v error=%v guardCalls=%d writes=%d", result, reconcileErr, guardCalls, writes)
		}

		recoveryDriver.forwardSessionGuard = nil
		request.Mode = gatewayRebindPhysicalReconcileRollbackOnly
		result, reconcileErr = recoveryDriver.reconcileSuccessorForwardLocked(context.Background(), request, appendProgress)
		gatewayRebindRequireStoppedCutoverReadOnly(t, fresh, fixture.repository, driver.runner,
			beforeSQL, beforeHistory, beforeFiles, effectStart)
		if reconcileErr == nil || !reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || writes != 0 {
			t.Fatalf("rollback-only forward result=%#v error=%v writes=%d", result, reconcileErr, writes)
		}
	})

	t.Run("foreign final identity is refused without repair", func(t *testing.T) {
		fixture, driver := newGatewayRebindStoppedCutoverCompositionFixture(t)
		fresh, recoveryDriver := newGatewayRebindCompositionRecoveryProcessWithDriver(t, fixture.manager, driver.runner)
		request := gatewayRebindStoppedCutoverRequest(t, fresh, fixture.repository)
		beforeSQL, beforeHistory, beforeFiles := gatewayRebindStoppedCutoverState(t, fresh, fixture.repository)
		effectStart := len(driver.runner.effects)
		runtime, ok := recoveryDriver.handover.(gatewayRebindTypedHandoverRuntime)
		if !ok || beforeHistory.Progress[14].Record.TypedEffect == nil {
			t.Fatal("stopped cutover runtime or retained effect is unavailable")
		}
		effect := *beforeHistory.Progress[14].Record.TypedEffect
		proof, _, stableErr := runtime.stable(context.Background(), request.Attempt, effect,
			func(context.Context) error { return nil })
		if stableErr != nil || !gatewayRebindTypedHandoverReadyToResumeCutover(
			proof, effect.HandoverIntent.Plan, *effect.FinalContainer) {
			t.Fatalf("exact stopped cutover observation was not resumable: error=%v proof=%#v", stableErr, proof)
		}
		for _, test := range []struct {
			name       string
			validShape bool
			mutate     func(*gatewayRebindFinalHandoverObservation)
		}{
			{name: "missing predecessor stop proof", mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.PredecessorStopDigest = ""
			}},
			{name: "ambiguous predecessor address", validShape: true, mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.PredecessorAddress = gatewayRebindPredecessorAddressAmbiguous
			}},
			{name: "unexpected predecessor routes", mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.PredecessorRoutesDigest = value.PredecessorStopDigest
			}},
			{name: "stage present", mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.Stage = gatewayRebindHandoverContainerStopped
			}},
			{name: "final absent", validShape: true, mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.Final, value.FinalID = gatewayRebindHandoverContainerAbsent, ""
			}},
			{name: "configuration drift", validShape: true, mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.ConfigDigest = value.PredecessorStopDigest
			}},
			{name: "application topology drift", validShape: true, mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.ApplicationNetworks = nil
			}},
			{name: "successor routes unexpectedly active", mutate: func(value *gatewayRebindFinalHandoverObservation) {
				value.RoutesDigest = effect.HandoverIntent.Plan.RoutePlanDigest
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidate := proof
				test.mutate(&candidate)
				var digestErr error
				candidate.Digest, digestErr = gatewayRebindFinalHandoverObservationDigest(candidate)
				if digestErr != nil {
					t.Fatalf("mutated stopped cutover observation digest: %v", digestErr)
				}
				if validShape := validGatewayRebindFinalHandoverObservationValue(candidate); validShape != test.validShape {
					t.Fatalf("mutated stopped cutover observation valid shape=%t, want %t", validShape, test.validShape)
				}
				if gatewayRebindTypedHandoverReadyToResumeCutover(
					candidate, effect.HandoverIntent.Plan, *effect.FinalContainer) {
					t.Fatal("unsafe stopped cutover observation was resumable")
				}
			})
		}
		foreignID := strings.Repeat("8", 64)
		driver.runner.final.ID = foreignID
		writes := 0
		result, reconcileErr := recoveryDriver.reconcileSuccessorForwardLocked(context.Background(), request,
			func(context.Context, gatewayRebindProgressRecord) error {
				writes++
				return errors.New("unexpected progress write")
			})
		gatewayRebindRequireStoppedCutoverReadOnly(t, fresh, fixture.repository, driver.runner,
			beforeSQL, beforeHistory, beforeFiles, effectStart)
		if reconcileErr == nil || !reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || writes != 0 ||
			driver.runner.final.ID != foreignID {
			t.Fatalf("foreign final result=%#v error=%v writes=%d final=%q", result, reconcileErr, writes,
				driver.runner.final.ID)
		}
	})
}

func newGatewayRebindStoppedCutoverCompositionFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	*gatewayRebindCompositionDriver,
) {
	t.Helper()
	fixture, input, driver := newGatewayRebindCompositionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := false
	driver.runner.afterEffect = func(args []string) {
		want := []string{"container", "stop", "--time", "10",
			normalizeID(driver.runner.predecessor.FinalContainer.ID)}
		if !stopped && reflect.DeepEqual(args, want) {
			stopped = true
			cancel()
		}
	}
	result, commitErr := fixture.manager.commitGatewayRebindWithDriver(ctx, fixture.repository, input, driver)
	driver.runner.afterEffect = nil
	cancel()
	history, historyErr := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	snapshot, snapshotErr := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if commitErr == nil || !reflect.DeepEqual(result, GatewayRebindCommitResult{}) || !stopped ||
		historyErr != nil || snapshotErr != nil || snapshot.Active == nil ||
		snapshot.Phase != appaccess.GatewayRebindPrepared || snapshot.DatabaseCommitObserved || !snapshot.RollbackAllowed ||
		len(history.Progress) != 15 || history.Progress[14].Record.Phase != gatewayRebindProgressCutoverIntent ||
		len(history.TerminalsV2) != 0 || driver.runner.predecessor.FinalContainer.Running ||
		driver.runner.stagePresent || !driver.runner.finalPresent || driver.runner.final.Running ||
		gatewayRebindCutoverExactEffectCount(driver.runner.effects,
			[]string{"container", "stop", "--time", "10", normalizeID(driver.runner.predecessor.FinalContainer.ID)}) != 1 ||
		gatewayRebindCutoverExactEffectCount(driver.runner.effects,
			[]string{"container", "start", driver.runner.finalID}) != 0 {
		t.Fatalf("interrupted cutover result=%#v error=%v stopped=%t errors=%v/%v phase=%s databaseCommit=%t rollback=%t progress=%d terminals=%d predecessorRunning=%t stage=%t final=%t/%t effects=%v",
			result, commitErr, stopped, historyErr, snapshotErr, snapshot.Phase, snapshot.DatabaseCommitObserved,
			snapshot.RollbackAllowed, len(history.Progress), len(history.TerminalsV2),
			driver.runner.predecessor.FinalContainer.Running, driver.runner.stagePresent,
			driver.runner.finalPresent, driver.runner.final.Running, driver.runner.effects)
	}
	return fixture, driver
}

func gatewayRebindStoppedCutoverRequest(t *testing.T, manager *Manager,
	repository *appaccess.Repository,
) gatewayRebindPhysicalReconcileRequest {
	t.Helper()
	snapshot, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 {
		t.Fatalf("stopped cutover history intents=%d error=%v", len(history.IntentsV2), err)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(history.IntentsV2[0].Intent, history)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: snapshot.Phase,
		RollbackAllowed: snapshot.RollbackAllowed, DatabaseCommitObserved: snapshot.DatabaseCommitObserved}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("stopped cutover request is invalid")
	}
	return request
}

func gatewayRebindStoppedCutoverState(t *testing.T, manager *Manager,
	repository *appaccess.Repository,
) (appaccess.GatewayRebindRecoverySnapshot, gatewayRebindProtectedIntentHistory, gatewayHistorySnapshot) {
	t.Helper()
	snapshot, err := repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history, err := manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, history, files
}

func gatewayRebindRequireStoppedCutoverReadOnly(t *testing.T, manager *Manager,
	repository *appaccess.Repository, runner *gatewayRebindCompositionRunner,
	beforeSQL appaccess.GatewayRebindRecoverySnapshot, beforeHistory gatewayRebindProtectedIntentHistory,
	beforeFiles gatewayHistorySnapshot, effectStart int,
) {
	t.Helper()
	afterSQL, afterHistory, afterFiles := gatewayRebindStoppedCutoverState(t, manager, repository)
	if !reflect.DeepEqual(beforeSQL, afterSQL) || !sameGatewayRebindCurrentHistory(beforeHistory, afterHistory) ||
		!sameGatewayHistorySnapshot(beforeFiles, afterFiles) || len(runner.effects) != effectStart ||
		runner.predecessor.FinalContainer.Running || runner.stagePresent || !runner.finalPresent || runner.final.Running {
		t.Fatalf("refusal changed state sql=%t history=%t files=%t effects=%d/%d predecessorRunning=%t stage=%t final=%t/%t",
			reflect.DeepEqual(beforeSQL, afterSQL), sameGatewayRebindCurrentHistory(beforeHistory, afterHistory),
			sameGatewayHistorySnapshot(beforeFiles, afterFiles), len(runner.effects), effectStart,
			runner.predecessor.FinalContainer.Running, runner.stagePresent, runner.finalPresent, runner.final.Running)
	}
}

func gatewayRebindCutoverExactEffectCount(effects [][]string, want []string) int {
	count := 0
	for _, effect := range effects {
		if reflect.DeepEqual(effect, want) {
			count++
		}
	}
	return count
}
