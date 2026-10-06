package generatedingress

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindTypedImageRunner struct{ image imageInspection }

func (r gatewayRebindTypedImageRunner) Run(_ context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	if len(args) >= 2 && args[0] == "image" && args[1] == "inspect" {
		body, err := json.Marshal(r.image)
		return runtimeprocess.CommandResult{Stdout: body}, err
	}
	if len(args) >= 2 && args[1] == "inspect" {
		return runtimeprocess.CommandResult{Stderr: []byte("Error: No such " + args[0])}, errors.New("not found")
	}
	if len(args) >= 2 && args[1] == "ls" {
		return runtimeprocess.CommandResult{}, nil
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage Docker command: " + strings.Join(args, " "))
}

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
	intent := history.IntentsV2[0].Intent
	effect := history.Progress[4].Record.TypedEffect
	if effect == nil || effect.ConfigVolume == nil {
		t.Fatal("typed config-volume binding is unavailable")
	}
	observed := volumeInspection{Name: intent.Identity.ConfigVolume, Driver: "local", Scope: "local",
		Options: map[string]string{}, Labels: gatewayRebindTypedStageResourceLabels(intent,
			gatewayV2ManagedContainerLabel, gatewayV2ConfigVolumeRole)}
	identity := gatewayV1VolumeIdentity{Mountpoint: effect.ConfigVolume.Mountpoint,
		CreatedAt: effect.ConfigVolume.CreatedAt}
	if !gatewayRebindTypedStageVolumeMatches(intent, observed, identity, true, intent.Identity.ConfigVolume,
		gatewayV2ConfigVolumeRole, effect.ConfigVolume) {
		t.Fatal("exact retained config-volume identity was refused")
	}
	replaced := identity
	replaced.Mountpoint += "-replacement"
	if reflect.DeepEqual(replaced, identity) || gatewayRebindTypedStageVolumeMatches(intent, observed, replaced, true,
		intent.Identity.ConfigVolume, gatewayV2ConfigVolumeRole, effect.ConfigVolume) {
		t.Fatal("same-name replacement config volume was accepted")
	}
	actualManager := &Manager{runner: gatewayRebindTypedImageRunner{image: imageInspection{
		ID: "sha256:" + bounded.template.receipt.Resources.ImageID, OS: "linux",
		RepoDigests: []string{"caddy@" + gatewayV2CaddyImageDigest},
	}}, options: Options{DockerExecutable: "docker", WorkingDirectory: t.TempDir(), CommandTimeout: time.Second}}
	imageID, imageErr := (gatewayRebindTypedStageRuntime{manager: actualManager}).observeImage(
		context.Background(), intent, func(context.Context) error { return nil })
	if imageErr != nil || imageID != bounded.template.receipt.Resources.ImageID {
		t.Fatalf("normalized typed image id=%q error=%v", imageID, imageErr)
	}
}

func TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists(t *testing.T) {
	fixture, input, _ := newGatewayRebindCoordinatorFixture(t)
	driver := managerGatewayRebindCrossStoreDriver{manager: fixture.manager}
	if _, err := fixture.manager.commitGatewayRebindWithDriver(context.Background(), fixture.repository,
		input, driver); err == nil {
		t.Fatal("partial typed physical adapter became production reachable")
	}
	history, err := fixture.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 1 || history.Progress[0].Record.Sequence != 1 {
		t.Fatalf("closed adapter changed physical prefix: progress=%d error=%v", len(history.Progress), err)
	}
}
