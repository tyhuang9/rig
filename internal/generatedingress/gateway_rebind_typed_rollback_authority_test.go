package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindTypedRollbackWithdrawsAfterAuthorityLoss(t *testing.T) {
	for _, test := range []struct {
		name, actor, boundary string
		stopped, refuseProbe  bool
	}{
		{name: "running predecessor rebind approver", actor: "rebind", boundary: "stage removal"},
		{name: "running predecessor configure approver", actor: "configure", boundary: "stage removal"},
		{name: "running predecessor LAN approver", actor: "LAN", boundary: "stage removal"},
		{name: "before predecessor start", actor: "rebind", boundary: "network removal", stopped: true},
		{name: "after predecessor start", actor: "configure", boundary: "predecessor start", stopped: true},
		{name: "completion write refusal", actor: "rebind", boundary: "before completion"},
		{name: "lost completion acknowledgement and cached replay", actor: "LAN", boundary: "after completion"},
		{name: "listener absence remains ambiguous", actor: "rebind", boundary: "stage removal", refuseProbe: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkGatewayRebindTypedRollbackAuthorityLoss(t, test.actor, test.boundary, test.stopped, test.refuseProbe)
		})
	}
}

func checkGatewayRebindTypedRollbackAuthorityLoss(t *testing.T, actor, boundary string, stopped, refuseProbe bool) {
	t.Helper()
	fixture := newGatewayRebindTypedHandoverRuntimeFixtureWithApprovals(t, true,
		func(f gatewayRebindPredecessorFixture, input *gatewayRebindCommitInput) {
			for _, approval := range []*appaccess.Approval{&input.RebindApproval, &input.ConfigureApproval} {
				approval.ActorID = uuid.NewString()
				if _, err := f.db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at)
					VALUES(?,?,'hash','administrator',datetime('now'),datetime('now'))`, approval.ActorID, approval.ActorID); err != nil {
					t.Fatal(err)
				}
			}
		})
	actorID := fixture.attempt.Claim.RebindApproval.ActorID
	if actor == "configure" {
		actorID = fixture.attempt.Claim.ConfigureApproval.ActorID
	} else if actor == "LAN" {
		actorID = fixture.predecessor.state.Apps[fixture.predecessor.stateAppID()].LAN.ApprovedBy
	}
	if refuseProbe {
		fixture.runner.refuseAbsence = &gatewayRebindTypedHandoverAbsenceRequest{
			Address: fixture.predecessor.state.Profile.SelectedIPv4, Port: fixture.predecessor.state.Profile.PortStart}
	}
	var initialRole string
	if err := fixture.predecessor.db.QueryRowContext(context.Background(),
		`SELECT role FROM users WHERE id=?`, actorID).Scan(&initialRole); err != nil {
		t.Fatal(err)
	}
	if initialRole != "administrator" {
		t.Fatalf("initial retained rebind approver role=%q actor=%s", initialRole, actorID)
	}
	before, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(before.Progress) != 12 || len(before.IntentsV2) != 1 {
		t.Fatalf("initial protected history intents=%d progress=%d error=%v",
			len(before.IntentsV2), len(before.Progress), err)
	}
	initialProgress := append([]gatewayRebindProgressSelection(nil), before.Progress...)
	initialClaim := before.IntentsV2[0].Intent.Claim

	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("rollback authorization review request is invalid")
	}
	fixture.runner.failFinalCreate = true
	stageID := fixture.runner.effect.StageContainer.ID
	hookCalls, affected := 0, int64(0)
	predecessorStopped := false
	var hookErr error
	demote := func() {
		if hookCalls != 0 {
			return
		}
		hookCalls++
		result, updateErr := fixture.predecessor.db.ExecContext(context.Background(),
			`UPDATE users SET role='viewer' WHERE id=? AND role='administrator'`, actorID)
		if updateErr != nil {
			hookErr = updateErr
			return
		}
		affected, hookErr = result.RowsAffected()
	}
	fixture.runner.afterEffect = func(args []string) {
		if len(args) != 3 {
			return
		}
		// Reach rollback from the admitted running predecessor. Stopping it
		// before reconciliation invalidates the forward admission fixture and
		// never exercises the guarded restart boundary.
		if stopped && args[0] == "network" && args[1] == "rm" {
			fixture.runner.stopPredecessor()
			predecessorStopped = true
		}
		switch boundary {
		case "stage removal":
			if args[0] == "container" && args[1] == "rm" && normalizeID(args[2]) == stageID {
				demote()
			}
		case "network removal":
			if args[0] == "network" && args[1] == "rm" {
				demote()
			}
		case "predecessor start":
			if args[0] == "container" && args[1] == "start" && normalizeID(args[2]) == normalizeID(fixture.runner.predecessor.FinalContainer.ID) {
				demote()
			}
		}
	}
	appendProgress := func(ctx context.Context, record gatewayRebindProgressRecord) error {
		if record.Phase == gatewayRebindProgressHandoverRolledBack && boundary == "before completion" {
			demote()
			return errors.New("injected completion write refusal")
		}
		if err := fixture.appendProgress(ctx, record); err != nil {
			return err
		}
		if record.Phase == gatewayRebindProgressHandoverRolledBack && boundary == "after completion" {
			demote()
			return errors.New("injected lost completion acknowledgement")
		}
		return nil
	}

	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	result, reconcileErr := driver.reconcileSuccessorLocked(context.Background(), request, appendProgress)

	var finalRole string
	roleErr := fixture.predecessor.db.QueryRowContext(context.Background(),
		`SELECT role FROM users WHERE id=?`, actorID).Scan(&finalRole)
	after, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	prefixUnchanged := historyErr == nil && len(after.Progress) >= len(initialProgress) &&
		reflect.DeepEqual(initialProgress, after.Progress[:len(initialProgress)])
	claimUnchanged := historyErr == nil && len(after.IntentsV2) == 1 &&
		reflect.DeepEqual(initialClaim, after.IntentsV2[0].Intent.Claim)
	phases := make([]gatewayRebindProgressPhase, 0, len(after.Progress))
	rollbackIntent, rollbackComplete := false, false
	for _, retained := range after.Progress {
		phases = append(phases, retained.Record.Phase)
		rollbackIntent = rollbackIntent || retained.Record.Phase == gatewayRebindProgressRollbackIntent
		rollbackComplete = rollbackComplete || retained.Record.Phase == gatewayRebindProgressHandoverRolledBack
	}
	stage, stageErr := fixture.runtime.stage.read(context.Background(), fixture.attempt.Intent)
	cleaned := stageErr == nil && !stage.StageContainerFound && !stage.FinalContainerFound &&
		!stage.ConfigVolumeFound && !stage.DataVolumeFound && !stage.NetworkFound &&
		len(stage.OwnedContainers) == 0 && len(stage.OwnedVolumes) == 0 && len(stage.OwnedNetworks) == 0
	predecessor, predecessorErr := fixture.runtime.predecessor(context.Background(), fixture.attempt)
	lanAbsent, loopbackAbsent := false, false
	if predecessorErr == nil {
		lanAbsent = fixture.runner.listenerAbsent(context.Background(), predecessor.Profile.SelectedIPv4,
			predecessor.Profile.PortStart)
		loopbackAbsent = fixture.runner.listenerAbsent(context.Background(), "127.0.0.1",
			predecessor.LocalHostPort)
	}

	setupValid := hookCalls == 1 && hookErr == nil && affected == 1 && roleErr == nil && finalRole == "viewer" &&
		prefixUnchanged && claimUnchanged && predecessorStopped == stopped
	predecessorStarts := 0
	for _, args := range fixture.runner.effects {
		if len(args) == 3 && args[0] == "container" && args[1] == "start" &&
			normalizeID(args[2]) == normalizeID(fixture.runner.predecessor.FinalContainer.ID) {
			predecessorStarts++
		}
	}
	wantStarts := 0
	if boundary == "predecessor start" {
		wantStarts = 1
	}
	setupValid = setupValid && predecessorStarts == wantStarts
	wantComplete := boundary == "after completion"
	var diagnostic *Error
	mayLive := errors.As(reconcileErr, &diagnostic) && diagnostic.candidateMayBeLive
	safe := setupValid && cleaned && rollbackIntent && rollbackComplete == wantComplete && reconcileErr != nil &&
		reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) && predecessorErr == nil &&
		!predecessor.Running && lanAbsent != refuseProbe && loopbackAbsent && mayLive == refuseProbe &&
		fixture.predecessor.repository.CheckGatewayRebindFence(context.Background()) != nil && len(after.TerminalsV2) == 0
	if !safe {
		t.Fatalf("revoked rollback setup=%t hook=%d affected=%d hookErr=%v actor=%s roles=%s/%s roleErr=%v prefix=%t claim=%t result=%#v error=%v progress=%d phases=%v rollbackIntent=%t rollbackComplete=%t cleaned=%t stageErr=%v predecessorErr=%v running=%t address=%s observation=%s lanAbsent=%t loopbackAbsent=%t effects=%#v",
			setupValid, hookCalls, affected, hookErr, actorID, initialRole, finalRole, roleErr,
			prefixUnchanged, claimUnchanged, result, reconcileErr, len(after.Progress), phases,
			rollbackIntent, rollbackComplete, cleaned, stageErr, predecessorErr, predecessor.Running,
			predecessor.Address, predecessor.Observation, lanAbsent, loopbackAbsent, fixture.runner.effects)
	}
	if wantComplete {
		replayed, err := driver.reconcileRollbackLocked(context.Background(), request, fixture.appendProgress)
		retained, readErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		if err == nil || !reflect.DeepEqual(replayed, gatewayRebindTypedPhysicalResult{}) || readErr != nil ||
			!reflect.DeepEqual(after, retained) || fixture.runner.predecessor.FinalContainer.Running {
			t.Fatalf("cached completed rollback bypassed revoked approval: result=%#v err=%v historyErr=%v", replayed, err, readErr)
		}
	}
}

func TestGatewayRebindTypedRollbackRequiresFinalPhysicalReadbacks(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	fixture.runner.failFinalCreate = true
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	if result, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress); err != nil ||
		result.Disposition != appaccess.GatewayRebindDispositionAbort {
		t.Fatalf("authorized rollback setup: result=%#v err=%v", result, err)
	}
	before, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	actorID := fixture.attempt.Claim.RebindApproval.ActorID
	for _, test := range []string{"stopped during serving probes", "restarted during withdrawal probes"} {
		t.Run(test, func(t *testing.T) {
			fixture.runner.startPredecessor()
			fixture.predecessor.manager.gatewayRebindFailStopLatch().Store(false)
			if _, err := fixture.predecessor.db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, actorID); err != nil {
				t.Fatal(err)
			}
			runtime := fixture.runtime
			mutated := false
			if test == "restarted during withdrawal probes" {
				if _, err := fixture.predecessor.db.Exec(`UPDATE users SET role='viewer' WHERE id=?`, actorID); err != nil {
					t.Fatal(err)
				}
			}
			runtime.listenerAbsent = func(ctx context.Context, address string, port uint16) bool {
				absent := fixture.runner.listenerAbsent(ctx, address, port)
				if !mutated && test == "stopped during serving probes" && address == fixture.attempt.Intent.SuccessorProfile.SelectedIPv4 {
					fixture.runner.stopPredecessor()
					mutated = true
				}
				if !mutated && test == "restarted during withdrawal probes" && address == "127.0.0.1" && absent {
					fixture.runner.startPredecessor()
					mutated = true
				}
				return absent
			}
			driver.handover = runtime
			err := driver.confirmRollbackServingLocked(context.Background(), request)
			var diagnostic *Error
			mayLive := errors.As(err, &diagnostic) && diagnostic.candidateMayBeLive
			wantLive := test == "restarted during withdrawal probes"
			if err == nil || !mutated || mayLive != wantLive || fixture.runner.predecessor.FinalContainer.Running != wantLive {
				t.Fatalf("final physical change accepted: mutated=%t live=%t running=%t err=%v", mutated, mayLive,
					fixture.runner.predecessor.FinalContainer.Running, err)
			}
			after, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || !reflect.DeepEqual(before, after) ||
				fixture.predecessor.repository.CheckGatewayRebindFence(context.Background()) == nil {
				t.Fatalf("final readback refusal changed immutable history or released fence: %v", err)
			}
		})
	}
}

type gatewayRebindRefuseStageRemovalRunner struct {
	*gatewayRebindTypedHandoverRuntimeRunner
	refused bool
}

func (r *gatewayRebindRefuseStageRemovalRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (
	runtimeprocess.CommandResult, error,
) {
	if args := request.Args; len(args) == 3 && args[0] == "container" && args[1] == "rm" &&
		normalizeID(args[2]) == r.effect.StageContainer.ID {
		r.refused = true
		return runtimeprocess.CommandResult{}, errors.New("injected retained successor removal failure")
	}
	return r.gatewayRebindTypedHandoverRuntimeRunner.Run(ctx, request)
}

func TestGatewayRebindTypedRollbackReportsRetainedSuccessor(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	runner := &gatewayRebindRefuseStageRemovalRunner{gatewayRebindTypedHandoverRuntimeRunner: fixture.runner}
	fixture.predecessor.manager.runner = runner
	request := gatewayRebindPhysicalReconcileRequest{Attempt: fixture.attempt,
		Mode: gatewayRebindPhysicalReconcileUndecided, SQLPhase: appaccess.GatewayRebindPrepared,
		RollbackAllowed: true}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.predecessor.manager,
		stage: fixture.stageDriver, handover: fixture.runtime}
	// The concrete forward path durably binds the handover before the exact
	// stage-removal failure triggers rollback of that retained attempt.
	result, err := driver.reconcileSuccessorLocked(context.Background(), request, fixture.appendProgress)
	var diagnostic *Error
	if err == nil || !errors.As(err, &diagnostic) || !diagnostic.candidateMayBeLive || !runner.refused ||
		!reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) || !runner.stagePresent ||
		runner.predecessor.FinalContainer.Running {
		t.Fatalf("retained successor was reported withdrawn: refused=%t stage=%t predecessorRunning=%t result=%#v err=%v",
			runner.refused, runner.stagePresent, runner.predecessor.FinalContainer.Running, result, err)
	}
	history, readErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if readErr != nil || len(history.Progress) != 14 || len(history.TerminalsV2) != 0 ||
		history.Progress[13].Record.Phase != gatewayRebindProgressRollbackIntent ||
		fixture.predecessor.repository.CheckGatewayRebindFence(context.Background()) == nil {
		t.Fatalf("cleanup failure released the fence or changed rollback intent: progress=%d terminals=%d err=%v",
			len(history.Progress), len(history.TerminalsV2), readErr)
	}
}
