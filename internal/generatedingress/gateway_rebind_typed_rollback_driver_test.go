package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindTypedRollbackDriverFake struct {
	*gatewayRebindTypedStageDriverFake
	manager                 *Manager
	failSequence            uint64
	rollbackEffects         int
	rollbackIntentInstalled bool
}

func (f *gatewayRebindTypedRollbackDriverFake) observeImage(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, guard gatewayRebindTypedEffectGuard,
) (string, error) {
	if f.failSequence == 1 {
		f.guard(ctx, guard)
		return "", errors.New("injected sequence-one physical refusal")
	}
	return f.gatewayRebindTypedStageDriverFake.observeImage(ctx, intent, guard)
}

func (f *gatewayRebindTypedRollbackDriverFake) bindNetwork(ctx context.Context,
	intent gatewayRebindProtectedIntentV2, effect gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageNetworkBinding, error) {
	if f.failSequence == 2 {
		f.guard(ctx, guard)
		return gatewayRebindStageNetworkBinding{}, errors.New("injected sequence-two physical refusal")
	}
	return f.gatewayRebindTypedStageDriverFake.bindNetwork(ctx, intent, effect, guard)
}

func (f *gatewayRebindTypedRollbackDriverFake) prepareRollback(ctx context.Context,
	attempt gatewayRebindPreparedAttempt, _ gatewayCurrentSelection, progress []gatewayRebindProgressRecord,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindTypedRollbackPreparation, error) {
	f.guard(ctx, guard)
	if len(progress) == 0 || progress[len(progress)-1].Sequence != f.failSequence {
		return gatewayRebindTypedRollbackPreparation{}, errors.New("unexpected typed rollback prefix")
	}
	owned := gatewayRebindTypedRollbackOwnedResources{}
	if progress[len(progress)-1].TypedEffect != nil {
		owned = gatewayRebindTypedRollbackOwnedFromEffect(*progress[len(progress)-1].TypedEffect)
	}
	routes, err := gatewayRebindTypedCheckpointRoutesDigest(attempt.Checkpoint)
	if err != nil {
		return gatewayRebindTypedRollbackPreparation{}, err
	}
	return gatewayRebindTypedRollbackPreparation{Owned: owned, PredecessorRoutesDigest: routes}, nil
}

func (f *gatewayRebindTypedRollbackDriverFake) rollbackSuccessor(ctx context.Context,
	_ gatewayRebindPreparedAttempt, _ gatewayCurrentSelection, rollbackRecord gatewayRebindProgressRecord,
	guard gatewayRebindTypedEffectGuard,
) (gatewayRebindFinalHandoverTerminalProof, error) {
	f.guard(ctx, guard)
	f.rollbackEffects++
	history, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) == 0 {
		return gatewayRebindFinalHandoverTerminalProof{}, errors.New("typed rollback intent was not readable before cleanup")
	}
	last := history.Progress[len(history.Progress)-1].Record
	if last.Phase != gatewayRebindProgressRollbackIntent || last.TypedRollback == nil ||
		!reflect.DeepEqual(last, rollbackRecord) {
		return gatewayRebindFinalHandoverTerminalProof{}, errors.New("typed rollback cleanup preceded its durable intent")
	}
	f.rollbackIntentInstalled = true
	return gatewayRebindTypedRollbackProofFixture(f.t, f.template,
		rollbackRecord.TypedRollback.PredecessorRoutesDigest, rollbackRecord.Digest), nil
}

func TestGatewayRebindTypedDriverPersistsRollbackIntentBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name         string
		failSequence uint64
		wantProgress int
	}{
		{name: "post-intent-no-successor-effects", failSequence: 1, wantProgress: 3},
		{name: "post-image-before-network-effect", failSequence: 2, wantProgress: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, bounded := newGatewayRebindCoordinatorFixture(t)
			stage := &gatewayRebindTypedStageDriverFake{t: t, template: bounded.template}
			fake := &gatewayRebindTypedRollbackDriverFake{gatewayRebindTypedStageDriverFake: stage,
				manager: fixture.manager, failSequence: test.failSequence}
			driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager, stage: fake, handover: fake}

			result, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(),
				fixture.repository, input, driver)
			if err != nil {
				t.Fatal(err)
			}
			if result.FinalPhase != appaccess.GatewayRebindRolledBack ||
				result.Disposition != appaccess.GatewayRebindDispositionAbort || !result.FenceReleased ||
				fake.rollbackEffects != 1 || !fake.rollbackIntentInstalled {
				t.Fatalf("typed rollback result=%#v effects=%d durableIntent=%t",
					result, fake.rollbackEffects, fake.rollbackIntentInstalled)
			}
			history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
			if err != nil || len(history.Progress) != test.wantProgress || len(history.TerminalsV2) != 1 ||
				history.Progress[len(history.Progress)-2].Record.Phase != gatewayRebindProgressRollbackIntent ||
				history.Progress[len(history.Progress)-1].Record.Phase != gatewayRebindProgressHandoverRolledBack ||
				history.TerminalsV2[0].Receipt.Disposition != appaccess.GatewayRebindDispositionAbort {
				t.Fatalf("typed rollback history progress=%d terminals=%d error=%v",
					len(history.Progress), len(history.TerminalsV2), err)
			}
			snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
			if err != nil || snapshot.Active != nil || snapshot.CurrentSource == nil ||
				snapshot.CurrentSource.OperationID == input.Inspection.Spec.OperationID {
				t.Fatalf("typed rollback SQL snapshot=%#v error=%v", snapshot, err)
			}
		})
	}
}
