package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindTypedCompletedSuccessorRestoresWithoutReceipt(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	ctx := context.Background()
	physical, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
	if err != nil || physical.Disposition != appaccess.GatewayRebindDispositionCommit {
		t.Fatalf("complete handover fixture: %v", err)
	}
	before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 17 || len(history.TerminalsV2) != 0 {
		t.Fatalf("missing completed unreceipted prefix: %v", err)
	}
	fixture.runner.stopFinal()
	fixture.runner.effects = nil
	result, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
	if err != nil || !reflect.DeepEqual(result, physical) || !fixture.runner.final.Running ||
		fixture.runner.predecessor.FinalContainer.Running || fixture.predecessor.manager.gatewayRebindAdmissionBlocked() ||
		!reflect.DeepEqual(fixture.runner.effects, [][]string{{"container", "start", fixture.runner.finalID}}) {
		t.Fatalf("authorized completed restart failed: final=%t effects=%v err=%v", fixture.runner.final.Running, fixture.runner.effects, err)
	}
	if replay, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress); err != nil ||
		!reflect.DeepEqual(replay, physical) || len(fixture.runner.effects) != 1 {
		t.Fatalf("completed restart replay repeated an effect: %v", err)
	}
	after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
		fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil {
		t.Fatalf("restoration changed immutable state or fence: SQL=%v history=%v", sqlErr, historyErr)
	}
}

type gatewayRebindCompletedRestoreRepository struct {
	*appaccess.Repository
	before func()
	alter  func(*appaccess.HostingGatewayStartupSnapshot)
}

func (r *gatewayRebindCompletedRestoreRepository) HostingGatewayStartupSnapshot(ctx context.Context) (appaccess.HostingGatewayStartupSnapshot, error) {
	if r.before != nil {
		r.before()
	}
	snapshot, err := r.Repository.HostingGatewayStartupSnapshot(ctx)
	if err == nil && r.alter != nil {
		r.alter(&snapshot)
	}
	return snapshot, err
}

type gatewayRebindCompletedLostStartAckRunner struct {
	*gatewayRebindTypedHandoverRuntimeRunner
}

func (r gatewayRebindCompletedLostStartAckRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	result, err := r.gatewayRebindTypedHandoverRuntimeRunner.Run(ctx, request)
	if err == nil && reflect.DeepEqual(request.Args, []string{"container", "start", r.finalID}) {
		return result, errors.New("injected lost completed successor start acknowledgement")
	}
	return result, err
}

func TestGatewayRebindTypedCompletedRestartRequiresFreshAuthority(t *testing.T) {
	fixture, driver, request := newGatewayRebindTypedForwardOnlyFixture(t)
	ctx := context.Background()
	actor := fixture.attempt.Claim.ConfigureApproval.ActorID
	originalBody := append([]byte(nil), fixture.runner.stage.activeBody...)
	defer clear(originalBody)
	for _, kind := range []string{"approved receipt", "lost start acknowledgement", "revoked before start",
		"revoked during absence probe", "revoked after start", "incomplete runtime census", "unhealthy runtime",
		"LAN head changed", "config changed", "replacement container", "fail-stop latched", "commit barrier latched",
		"observational confirmation", "successor ready"} {
		t.Run(kind, func(t *testing.T) {
			fixture.predecessor.manager.gatewayRebindFailStopLatch().Store(false)
			fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Store(false)
			fixture.runner.final.ID = fixture.runner.finalID
			fixture.runner.stopFinal()
			fixture.runner.effects, fixture.runner.afterEffect = nil, nil
			fixture.runner.stage.activeBody = append([]byte(nil), originalBody...)
			fixture.predecessor.manager.runner = fixture.runner
			driver.handover = fixture.runtime
			if _, err := fixture.predecessor.db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, actor); err != nil {
				t.Fatal(err)
			}
			demoted := false
			demote := func() {
				if demoted {
					return
				}
				if _, err := fixture.predecessor.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, actor); err != nil {
					t.Fatal(err)
				}
				demoted = true
			}
			repository := &gatewayRebindCompletedRestoreRepository{Repository: fixture.predecessor.repository}
			fixture.predecessor.manager.options.RebindCurrentStateRepository = repository
			switch kind {
			case "lost start acknowledgement":
				fixture.predecessor.manager.runner = gatewayRebindCompletedLostStartAckRunner{fixture.runner}
			case "revoked before start":
				demote()
			case "revoked during absence probe":
				runtime := fixture.runtime
				runtime.listenerAbsent = func(callCtx context.Context, address string, port uint16) bool {
					absent := fixture.runner.listenerAbsent(callCtx, address, port)
					if absent {
						demote()
					}
					return absent
				}
				driver.handover = runtime
			case "revoked after start":
				fixture.runner.afterEffect = func(args []string) {
					if reflect.DeepEqual(args, []string{"container", "start", fixture.runner.finalID}) {
						demote()
					}
				}
			case "incomplete runtime census":
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents = nil }
			case "unhealthy runtime":
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents[0].State = "failed" }
			case "LAN head changed":
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.Grants.Claims[0].AccessHeadCurrent = false }
			case "config changed":
				fixture.runner.stage.activeBody = []byte("{}")
			case "replacement container":
				fixture.runner.final.ID = strings.Repeat("8", 64)
			case "fail-stop latched":
				fixture.predecessor.manager.gatewayRebindFailStopLatch().Store(true)
			case "commit barrier latched":
				fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Store(true)
			case "successor ready":
				advanceGatewayRebindTypedForwardFixture(t, fixture, request, appaccess.GatewayRebindSuccessorReady)
			}
			before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "successor ready" {
				request = gatewayRebindForwardConfirmationRequest(fixture.attempt, *request.Terminal, before)
			}
			history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil {
				t.Fatal(err)
			}
			var result gatewayRebindTypedPhysicalResult
			if kind == "observational confirmation" {
				err = driver.confirmForwardServingLocked(ctx, request)
			} else {
				result, err = driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
			}
			approved := kind == "approved receipt" || kind == "lost start acknowledgement" || kind == "successor ready"
			wantEffects := [][]string(nil)
			if approved || kind == "revoked after start" {
				wantEffects = append(wantEffects, []string{"container", "start", fixture.runner.finalID})
			}
			if kind == "revoked after start" {
				wantEffects = append(wantEffects, []string{"container", "stop", "--time", "10", fixture.runner.finalID})
			}
			if !reflect.DeepEqual(wantEffects, fixture.runner.effects) || fixture.runner.final.Running != approved ||
				fixture.runner.predecessor.FinalContainer.Running ||
				(strings.HasPrefix(kind, "revoked") && !demoted) {
				t.Fatalf("restart effects/authority: effects=%v want=%v final=%t demoted=%t err=%v",
					fixture.runner.effects, wantEffects, fixture.runner.final.Running, demoted, err)
			}
			if approved {
				if err != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit || fixture.predecessor.manager.gatewayRebindAdmissionBlocked() {
					t.Fatalf("approved restoration failed: %v", err)
				}
			} else {
				var diagnostic *Error
				if err == nil || !errors.As(err, &diagnostic) || diagnostic.candidateMayBeLive != (kind == "replacement container") ||
					!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) ||
					(kind != "observational confirmation" && !fixture.predecessor.manager.gatewayRebindFailStopLatch().Load()) ||
					(kind == "commit barrier latched" && !fixture.predecessor.manager.gatewayRebindCommitBarrierLatch().Load()) {
					t.Fatalf("restart refusal did not fail closed: %v", err)
				}
			}
			after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
				fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil || !fixture.runner.finalPresent ||
				!fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
				t.Fatalf("restoration changed durable authority/resources: SQL=%v history=%v", sqlErr, historyErr)
			}
		})
	}
}

func TestGatewayRebindTypedCompletedRestartUsesCommittedAuthority(t *testing.T) {
	f, request, _, runner := gatewayRebindCommittedServingFixture(t)
	ctx := context.Background()
	driver := managerGatewayRebindCrossStoreDriver{manager: f.manager, stage: &gatewayRebindTypedStageDriverFake{}}
	before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.lostStartAck = true
	appendProgress := func(context.Context, gatewayRebindProgressRecord) error {
		t.Fatal("completed recovery attempted a progress write")
		return nil
	}
	for repeat := 0; repeat < 2; repeat++ {
		result, err := driver.reconcileSuccessorLocked(ctx, request, appendProgress)
		if err != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit || !runner.container.Running ||
			!reflect.DeepEqual(runner.effects, [][]string{{"container", "start", runner.target.Resources.FinalContainer.ID}}) || f.manager.gatewayRebindAdmissionBlocked() {
			t.Fatalf("database-committed restart/replay failed: effects=%v err=%v", runner.effects, err)
		}
	}
	after, sqlErr := f.repository.GatewayRebindRecoverySnapshot(ctx)
	retained, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
		f.repository.CheckGatewayRebindFence(ctx) == nil {
		t.Fatalf("committed restore changed SQL/history/fence: SQL=%v history=%v", sqlErr, historyErr)
	}
}

func TestGatewayRebindTypedCommittedRunningReplayRequiresCompleteCensus(t *testing.T) {
	fixture, driver, request := newGatewayRebindTypedForwardOnlyFixture(t)
	ctx := context.Background()
	advanceGatewayRebindTypedForwardFixture(t, fixture, request, appaccess.GatewayRebindSuccessorReady)
	advanceGatewayRebindTypedForwardFixture(t, fixture, request, appaccess.GatewayRebindDatabaseCommitted)
	before, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request = gatewayRebindForwardConfirmationRequest(fixture.attempt, *request.Terminal, before)
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"approved", "incomplete runtime census", "LAN head changed"} {
		t.Run(kind, func(t *testing.T) {
			fixture.predecessor.manager.gatewayRebindFailStopLatch().Store(false)
			fixture.runner.startFinal()
			fixture.runner.effects = nil
			repository := &gatewayRebindCompletedRestoreRepository{Repository: fixture.predecessor.repository}
			if kind == "incomplete runtime census" {
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.RuntimeComponents = nil }
			} else if kind == "LAN head changed" {
				repository.alter = func(s *appaccess.HostingGatewayStartupSnapshot) { s.Grants.Claims[0].AccessHeadCurrent = false }
			}
			fixture.predecessor.manager.options.RebindCurrentStateRepository = repository
			result, err := driver.reconcileSuccessorLocked(ctx, request, fixture.appendProgress)
			if kind == "approved" {
				if err != nil || result.Disposition != appaccess.GatewayRebindDispositionCommit || !fixture.runner.final.Running || len(fixture.runner.effects) != 0 {
					t.Fatalf("approved running replay failed: effects=%v err=%v", fixture.runner.effects, err)
				}
			} else {
				var diagnostic *Error
				if err == nil || !errors.As(err, &diagnostic) || diagnostic.candidateMayBeLive || fixture.runner.final.Running ||
					!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || !fixture.predecessor.manager.gatewayRebindFailStopLatch().Load() ||
					!reflect.DeepEqual(fixture.runner.effects, [][]string{{"container", "stop", "--time", "10", fixture.runner.finalID}}) {
					t.Fatalf("incomplete committed census kept serving: effects=%v err=%v", fixture.runner.effects, err)
				}
			}
			after, sqlErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
			retained, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if sqlErr != nil || historyErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, retained) ||
				fixture.predecessor.repository.CheckGatewayRebindFence(ctx) == nil || fixture.runner.predecessor.FinalContainer.Running ||
				!fixture.runner.finalPresent || !fixture.runner.configPresent || !fixture.runner.dataPresent || !fixture.runner.networkPresent {
				t.Fatalf("running replay changed durable authority/resources: SQL=%v history=%v", sqlErr, historyErr)
			}
		})
	}
}
