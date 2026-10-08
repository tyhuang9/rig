package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindCompositionRejectTransition struct {
	*appaccess.Repository
	stopAt        appaccess.GatewayRebindState
	rejected      int
	beforeFailure func()
}

func (r *gatewayRebindCompositionRejectTransition) ApplyGatewayRebindTransition(ctx context.Context,
	proof appaccess.GatewayRebindTransitionProof,
) (appaccess.GatewayRebindTransitionCommand, error) {
	if proof.NextState == r.stopAt {
		r.rejected++
		r.beforeFailure()
		return appaccess.GatewayRebindTransitionCommand{}, errors.New("injected refusal before SQL transition")
	}
	return r.Repository.ApplyGatewayRebindTransition(ctx, proof)
}

// Model a new process lifetime, not a second Manager in the failed process.
// Only the new Manager gets new latches; the old latches are never cleared.
// Docker resources, protected files, SQL and configuration remain the same.
func newGatewayRebindCompositionRecoveryProcess(t *testing.T, source *Manager,
	runner *gatewayRebindCompositionRunner,
) *Manager {
	t.Helper()
	fresh := freshGatewayRebindRecoveryManager(source)
	fresh.gatewayRebindFailStop = &atomic.Bool{}
	fresh.gatewayRebindCommitBarrier = &atomic.Bool{}
	driver := &gatewayRebindCompositionDriver{t: t, runner: runner}
	driver.install(fresh)
	return fresh
}

func TestGatewayRebindConcreteCompositionRecoversForwardAfterDurableFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stopAt   appaccess.GatewayRebindState
		phase    appaccess.GatewayRebindState
		receipts int
	}{
		{"lost completed progress acknowledgement", "", appaccess.GatewayRebindPrepared, 0},
		{"before database commit", appaccess.GatewayRebindDatabaseCommitted, appaccess.GatewayRebindSuccessorReady, 1},
		{"before terminal SQL commit", appaccess.GatewayRebindCommitted, appaccess.GatewayRebindDatabaseCommitted, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, input, driver := newGatewayRebindCompositionFixture(t)
			r, ctx := driver.runner, context.Background()
			failureEffects := -1
			repository := &gatewayRebindCompositionRejectTransition{Repository: f.repository, stopAt: tc.stopAt,
				beforeFailure: func() { failureEffects = len(r.effects) }}
			lostAck := 0
			driver.afterProgress = func(record gatewayRebindProgressRecord) error {
				if tc.stopAt == "" && record.Phase == gatewayRebindProgressHandoverCommitted {
					lostAck++
					failureEffects = len(r.effects)
					return errors.New("injected lost completed progress acknowledgement")
				}
				return nil
			}
			// These failures execute the real cleanup paths. They do not simulate
			// abrupt termination, which requires a separate process-level gate.
			result, commitErr := f.manager.commitGatewayRebindWithDriver(ctx, repository, input, driver)
			var diagnostic *Error
			if commitErr == nil || result != (GatewayRebindCommitResult{}) || !errors.As(commitErr, &diagnostic) ||
				diagnostic.candidateMayBeLive || !f.manager.gatewayRebindFailStopLatch().Load() ||
				(tc.stopAt == "" && lostAck != 1) || (tc.stopAt != "" && repository.rejected != 1) {
				t.Fatalf("failure boundary: result=%#v ack=%d SQL=%d err=%v", result, lostAck, repository.rejected, commitErr)
			}
			before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			committed := tc.phase == appaccess.GatewayRebindDatabaseCommitted
			if err != nil || historyErr != nil || before.Active == nil || before.Phase != tc.phase ||
				before.DatabaseCommitObserved != committed || before.RollbackAllowed != (tc.phase == appaccess.GatewayRebindPrepared) ||
				len(history.Progress) != 17 || len(history.TerminalsV2) != tc.receipts ||
				f.repository.CheckGatewayRebindFence(ctx) == nil || r.stagePresent || !r.finalPresent ||
				r.final.Running || r.predecessor.FinalContainer.Running || !r.configPresent || !r.dataPresent || !r.networkPresent {
				t.Fatalf("wrong retained boundary: phase=%s progress=%d receipts=%d SQL=%v history=%v",
					before.Phase, len(history.Progress), len(history.TerminalsV2), err, historyErr)
			}
			if failureEffects < 0 || !reflect.DeepEqual(r.effects[failureEffects:],
				[][]string{{"container", "stop", "--time", "10", r.finalID}}) {
				t.Fatal("failure did not withdraw the exact successor")
			}
			files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			oldFailStop, oldBarrier := f.manager.gatewayRebindFailStopLatch().Load(), f.manager.gatewayRebindCommitBarrierLatch().Load()
			if oldBarrier != committed {
				t.Fatal("failure retained the wrong process commit barrier")
			}
			effects := len(r.effects)
			fresh := newGatewayRebindCompositionRecoveryProcess(t, f.manager, r)
			prepared, err := fresh.InspectGatewayRebindCurrent(ctx, f.repository)
			if err != nil || prepared.ActiveOperationID != input.Inspection.Spec.OperationID ||
				prepared.ActiveSpecVersion != appaccess.GatewayRebindSpecVersionV2 || prepared.ActivePhase != tc.phase || prepared.FenceReleased {
				t.Fatalf("active startup classification: %+v error=%v", prepared, err)
			}
			recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
			if err != nil || !recovered.Recovered || recovered.InitialActivePhase != tc.phase ||
				recovered.FinalActivePhase != appaccess.GatewayRebindCommitted || recovered.Disposition != appaccess.GatewayRebindDispositionCommit ||
				!recovered.FenceReleased || !validSHA256(recovered.CurrentAttestationDigest) || fresh.gatewayRebindAdmissionBlocked() ||
				!r.finalPresent || !r.final.Running || r.predecessor.FinalContainer.Running || r.stagePresent ||
				!reflect.DeepEqual(r.effects[effects:], [][]string{{"container", "start", r.finalID}}) {
				t.Fatalf("same-backend forward recovery: result=%#v effects=%v err=%v", recovered, r.effects[effects:], err)
			}
			confirmed, err := fresh.InspectGatewayRebindCurrent(ctx, f.repository)
			if err != nil || confirmed.ActiveOperationID != "" || confirmed.ActiveSpecVersion != 0 || !confirmed.FenceReleased ||
				confirmed.CurrentRecoveryMode != GatewayCurrentRecoveryStable || recovered.SelectedCurrentAuthority == nil ||
				confirmed.SelectedCurrentAuthority != *recovered.SelectedCurrentAuthority ||
				confirmed.CurrentStateDigest != recovered.CurrentStateDigest {
				t.Fatalf("active recovery startup checkpoint: %+v error=%v", confirmed, err)
			}
			after, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
			if err != nil || after.Active != nil || f.repository.CheckGatewayRebindFence(ctx) != nil ||
				(committed && !reflect.DeepEqual(before.CurrentTransfers, after.CurrentTransfers)) {
				t.Fatalf("recovery rewrote prior SQL evidence or retained its fence: %v", err)
			}
			requireGatewayRebindCompositionSQLPrefix(t, before, after)
			gatewayRebindSequenceRequireRetainedFiles(t, fresh, files)
			if f.manager.gatewayRebindFailStopLatch().Load() != oldFailStop || f.manager.gatewayRebindCommitBarrierLatch().Load() != oldBarrier {
				t.Fatal("new-process recovery changed the failed process latches")
			}
			requireGatewayRebindCompositionReplay(t, fresh, f.repository, r)
		})
	}
}

func TestGatewayRebindConcreteCompositionRecoversRetainedRollback(t *testing.T) {
	f, input, driver := newGatewayRebindCompositionFixture(t)
	r, ctx := driver.runner, context.Background()
	completionRefused, rollbackRefused, rollbackStart := 0, 0, 0
	driver.beforeProgress = func(record gatewayRebindProgressRecord) error {
		switch record.Phase {
		case gatewayRebindProgressHandoverCommitted:
			completionRefused++
			rollbackStart = len(r.effects)
			return errors.New("injected refusal before completed handover write")
		case gatewayRebindProgressHandoverRolledBack:
			rollbackRefused++
			return errors.New("injected refusal before rollback completion write")
		}
		return nil
	}
	result, commitErr := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver)
	before, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if commitErr == nil || result != (GatewayRebindCommitResult{}) || completionRefused != 1 || rollbackRefused != 1 ||
		err != nil || historyErr != nil || before.Active == nil || before.Phase != appaccess.GatewayRebindPrepared ||
		!before.RollbackAllowed || before.DatabaseCommitObserved || f.repository.CheckGatewayRebindFence(ctx) == nil ||
		len(history.Progress) != 17 || history.Progress[16].Record.Phase != gatewayRebindProgressRollbackIntent || len(history.TerminalsV2) != 0 ||
		r.stagePresent || r.finalPresent || r.configPresent || r.dataPresent || r.networkPresent || !r.predecessor.FinalContainer.Running {
		t.Fatalf("retained rollback: completion=%d rollback=%d progress=%d err=%v SQL=%v history=%v",
			completionRefused, rollbackRefused, len(history.Progress), commitErr, err, historyErr)
	}
	wantEffects := [][]string{
		{"container", "stop", "--time", "10", r.finalID},
		{"container", "rm", r.finalID},
		{"volume", "rm", r.intent.Identity.DataVolume},
		{"volume", "rm", r.intent.Identity.ConfigVolume},
		{"network", "rm", r.stage.networkID},
		{"container", "start", normalizeID(r.predecessor.FinalContainer.ID)},
	}
	if !reflect.DeepEqual(r.effects[rollbackStart:], wantEffects) {
		t.Fatalf("rollback changed resources outside the exact removal plan: %v", r.effects[rollbackStart:])
	}
	files, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	oldFailStop, oldBarrier := f.manager.gatewayRebindFailStopLatch().Load(), f.manager.gatewayRebindCommitBarrierLatch().Load()
	effects := len(r.effects)
	fresh := newGatewayRebindCompositionRecoveryProcess(t, f.manager, r)
	recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
	if err != nil || !recovered.Recovered || recovered.InitialActivePhase != appaccess.GatewayRebindPrepared ||
		recovered.FinalActivePhase != appaccess.GatewayRebindRolledBack || recovered.Disposition != appaccess.GatewayRebindDispositionAbort ||
		!recovered.FenceReleased || fresh.gatewayRebindAdmissionBlocked() || len(r.effects) != effects || !r.predecessor.FinalContainer.Running ||
		r.finalPresent || r.stagePresent || r.networkPresent || r.configPresent || r.dataPresent {
		t.Fatalf("rollback recovery: result=%#v effects=%v err=%v", recovered, r.effects[effects:], err)
	}
	after, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	history, historyErr = fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || historyErr != nil || after.Active != nil || f.repository.CheckGatewayRebindFence(ctx) != nil ||
		!reflect.DeepEqual(before.CurrentSource, after.CurrentSource) || !reflect.DeepEqual(before.CurrentTransfers, after.CurrentTransfers) ||
		len(history.Progress) != 18 || history.Progress[17].Record.Phase != gatewayRebindProgressHandoverRolledBack || len(history.TerminalsV2) != 1 {
		t.Fatalf("rollback changed current authority or omitted terminal evidence: SQL=%v history=%v", err, historyErr)
	}
	requireGatewayRebindCompositionSQLPrefix(t, before, after)
	gatewayRebindSequenceRequireRetainedFiles(t, fresh, files)
	if f.manager.gatewayRebindFailStopLatch().Load() != oldFailStop || f.manager.gatewayRebindCommitBarrierLatch().Load() != oldBarrier {
		t.Fatal("rollback recovery changed the previous process latches")
	}
	requireGatewayRebindCompositionReplay(t, fresh, f.repository, r)
}

func requireGatewayRebindCompositionSQLPrefix(t *testing.T, before, after appaccess.GatewayRebindRecoverySnapshot) {
	t.Helper()
	if len(before.History) != 1 || len(after.History) != 1 {
		t.Fatal("recovery changed the operation history roster")
	}
	b, a := before.History[0], after.History[0]
	if b.Claim.SpecVersion != appaccess.GatewayRebindSpecVersionV2 || a.Claim.SpecVersion != b.Claim.SpecVersion ||
		b.Claim.Legacy != nil || a.Claim.Legacy != nil || b.Claim.V2 == nil || a.Claim.V2 == nil {
		t.Fatal("recovery changed the typed claim format")
	}
	priorClaim, currentClaim := *b.Claim.V2, *a.Claim.V2
	// State/sequence/update time are the mutable SQL projection. Every other
	// claim field and every existing history row must remain identical.
	currentClaim.State, currentClaim.StateSequence, currentClaim.UpdatedAt = priorClaim.State, priorClaim.StateSequence, priorClaim.UpdatedAt
	if !reflect.DeepEqual(priorClaim, currentClaim) || !reflect.DeepEqual(b.RosterV1, a.RosterV1) ||
		!reflect.DeepEqual(b.RosterV2, a.RosterV2) || !reflect.DeepEqual(b.RuntimeHeads, a.RuntimeHeads) ||
		len(a.Events) < len(b.Events) || len(a.Commands) < len(b.Commands) || len(a.Transfers) < len(b.Transfers) ||
		!slices.Equal(b.Events, a.Events[:len(b.Events)]) || !slices.Equal(b.Commands, a.Commands[:len(b.Commands)]) ||
		!slices.EqualFunc(b.Transfers, a.Transfers[:len(b.Transfers)], func(x, y appaccess.GatewayRebindAllocationTransfer) bool {
			return reflect.DeepEqual(x, y)
		}) {
		t.Fatal("recovery rewrote retained SQL claim, roster, event, command or transfer evidence")
	}
}

func requireGatewayRebindCompositionReplay(t *testing.T, source *Manager, repository *appaccess.Repository,
	runner *gatewayRebindCompositionRunner,
) {
	t.Helper()
	ctx := context.Background()
	before, err := repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readGatewayHistorySnapshotMode(source.store, true)
	if err != nil {
		t.Fatal(err)
	}
	effects := len(runner.effects)
	fresh := newGatewayRebindCompositionRecoveryProcess(t, source, runner)
	result, err := fresh.RecoverGatewayRebindStartup(ctx, repository)
	if err != nil || result.Recovered || !result.FenceReleased || len(runner.effects) != effects || fresh.gatewayRebindAdmissionBlocked() {
		t.Fatalf("terminal replay: result=%#v effects=%v err=%v", result, runner.effects[effects:], err)
	}
	after, err := repository.HostingGatewayStartupSnapshot(ctx)
	afterFiles, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil || filesErr != nil || !reflect.DeepEqual(before, after) || !sameGatewayHistorySnapshot(files, afterFiles) {
		t.Fatalf("terminal replay changed durable authority: SQL=%v history=%v", err, filesErr)
	}
}
