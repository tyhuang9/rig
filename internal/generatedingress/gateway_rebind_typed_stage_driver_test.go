package generatedingress

import (
	"context"
	"testing"
)

type gatewayRebindTypedStageDriverFake struct {
	t        *testing.T
	template gatewayCurrentStateFixture
	guards   int
	steps    []string
}

func (f *gatewayRebindTypedStageDriverFake) guard(ctx context.Context, value gatewayRebindTypedEffectGuard) {
	f.t.Helper()
	if value == nil || value(ctx) != nil {
		f.t.Fatal("typed stage fake received stale effect authority")
	}
	f.guards++
}

func (f *gatewayRebindTypedStageDriverFake) observeImage(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	guard gatewayRebindTypedEffectGuard,
) (string, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "image")
	return f.template.receipt.Resources.ImageID, nil
}

func (f *gatewayRebindTypedStageDriverFake) bindNetwork(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageNetworkBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "network")
	return gatewayRebindTypedStageNetworkBindingFor(intent, f.template.receipt.Resources.IngressNetwork.ID)
}

func (f *gatewayRebindTypedStageDriverFake) bindConfigVolume(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageConfigVolumeBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "config-volume")
	value := f.template.receipt.Resources.ConfigVolume
	return gatewayRebindTypedStageConfigVolumeBindingFor(intent, value.Mountpoint, value.CreatedAt)
}

func (f *gatewayRebindTypedStageDriverFake) bindDataVolume(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageDataVolumeBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "data-volume")
	value := f.template.receipt.Resources.DataVolume
	return gatewayRebindTypedStageDataVolumeBindingFor(intent, value.Mountpoint, value.CreatedAt)
}

func (f *gatewayRebindTypedStageDriverFake) bindStageContainer(ctx context.Context, intent gatewayRebindProtectedIntentV2,
	effect gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (gatewayRebindStageContainerBinding, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "stage-container")
	return gatewayRebindTypedStageContainerBindingFor(intent, effect,
		f.template.receipt.Resources.StageContainer.ID)
}

func (f *gatewayRebindTypedStageDriverFake) copyStageConfig(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) error {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "stage-config")
	return nil
}

func (f *gatewayRebindTypedStageDriverFake) serveStage(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	_ gatewayRebindTypedEffectProgress, guard gatewayRebindTypedEffectGuard,
) (string, error) {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "stage-serving")
	return f.template.receipt.Resources.StageContainer.ID, nil
}

func (f *gatewayRebindTypedStageDriverFake) copyFinalConfig(ctx context.Context, _ gatewayRebindProtectedIntentV2,
	_ gatewayRebindPredecessorCheckpoint, _ gatewayRebindTypedEffectProgress,
	guard gatewayRebindTypedEffectGuard,
) error {
	f.guard(ctx, guard)
	f.steps = append(f.steps, "final-config")
	return nil
}

func TestGatewayRebindTypedStageDriverPersistsCompleteStagePrefix(t *testing.T) {
	fixture, input, bounded := newGatewayRebindCoordinatorFixture(t)
	fake := &gatewayRebindTypedStageDriverFake{t: t, template: bounded.template}
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager, stage: fake}

	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("incomplete typed final handover unexpectedly committed")
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) != 12 ||
		history.Progress[11].Record.Sequence != 12 ||
		history.Progress[11].Record.Phase != gatewayRebindProgressFinalConfigCopied ||
		history.Progress[11].Record.TypedEffect == nil ||
		history.Progress[11].Record.TypedEffect.FinalConfigCopy == nil ||
		len(history.TerminalsV2) != 0 || fake.guards < len(fake.steps) || len(fake.steps) != 8 {
		t.Fatalf("typed stage prefix progress=%d terminals=%d steps=%v guards=%d error=%v",
			len(history.Progress), len(history.TerminalsV2), fake.steps, fake.guards, err)
	}
	snapshot, err := fixture.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || snapshot.Active == nil || snapshot.Phase != "prepared" || !snapshot.RollbackAllowed {
		t.Fatalf("typed stage fence snapshot=%#v error=%v", snapshot, err)
	}
}
