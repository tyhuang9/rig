package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindTypedStageCopyDiagnosticRunner struct {
	delegate      *gatewayRebindTypedStageRuntimeRunner
	expected      []byte
	events        []string
	archiveCalls  int
	failArchiveAt int
	stageCopies   int
	copyErr       error
}

func (r *gatewayRebindTypedStageCopyDiagnosticRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	args := request.Args
	archiveArgs := []string{"container", "cp", r.delegate.effect.StageContainer.ID + ":/config/.", "-"}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" && args[3] == "-" {
		if !reflect.DeepEqual(args, archiveArgs) {
			return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage-config archive read")
		}
		r.events = append(r.events, "archive")
		r.archiveCalls++
		if r.failArchiveAt == r.archiveCalls {
			return runtimeprocess.CommandResult{}, errors.New("stage archive unavailable")
		}
		return r.delegate.Run(ctx, request)
	}
	if len(args) == 4 && args[0] == "container" && args[1] == "cp" {
		r.events = append(r.events, "copy")
		r.stageCopies++
		body, err := os.ReadFile(args[2])
		if err != nil || args[3] != r.delegate.effect.StageContainer.ID+":/config/"+r.delegate.intent.Identity.StageConfigFilename ||
			!bytes.Equal(body, r.expected) {
			return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage-config copy")
		}
		if r.copyErr != nil {
			return runtimeprocess.CommandResult{}, r.copyErr
		}
		r.delegate.archiveOverride = gatewayRebindStageConfigCopyTestTar(r.delegate.t, r.expected, true)
		return runtimeprocess.CommandResult{}, nil
	}
	if len(args) >= 2 && (args[1] == "inspect" || args[1] == "ls") {
		return r.delegate.Run(ctx, request)
	}
	return runtimeprocess.CommandResult{}, errors.New("unexpected typed stage-config diagnostic command")
}

func newGatewayRebindTypedStageCopyDiagnosticFixture(t *testing.T) (
	gatewayRebindTypedStageRuntimeFixture,
	*gatewayRebindTypedStageCopyDiagnosticRunner,
	gatewayRebindTypedEffectProgress,
) {
	t.Helper()
	fixture := newGatewayRebindTypedStageRuntimeFixture(t)
	effect := gatewayRebindTypedStageConfigIntentEffect(t, fixture.records)
	expected, err := gatewayRebindTypedStageConfigBytes(fixture.intent)
	if err != nil {
		fixture.close(t)
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(expected) })
	empty := gatewayRebindStageConfigCopyTestTar(t, expected, false)
	exact := gatewayRebindStageConfigCopyTestTar(t, expected, true)
	if inventory, archiveErr := gatewayRebindExactStageConfigVolumeArchive(empty, expected); archiveErr != nil ||
		inventory != gatewayRebindStageConfigInventoryEmpty {
		fixture.close(t)
		t.Fatalf("canonical empty stage archive inventory=%d error=%v", inventory, archiveErr)
	}
	if inventory, archiveErr := gatewayRebindExactStageConfigVolumeArchive(exact, expected); archiveErr != nil ||
		inventory != gatewayRebindStageConfigInventoryExact {
		fixture.close(t)
		t.Fatalf("canonical exact stage archive inventory=%d error=%v", inventory, archiveErr)
	}
	fixture.runner.archiveOverride = empty
	runner := &gatewayRebindTypedStageCopyDiagnosticRunner{delegate: fixture.runner, expected: expected}
	fixture.runtime.manager.runner = runner
	return fixture, runner, effect
}

func TestGatewayRebindTypedStageRuntimeCopyStageConfigEffectOrder(t *testing.T) {
	t.Run("empty inventory copies once", func(t *testing.T) {
		fixture, runner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
		defer fixture.close(t)
		guard := func(context.Context) error {
			runner.events = append(runner.events, "guard")
			return nil
		}
		if err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect, guard); err != nil {
			t.Fatal(err)
		}
		want := []string{"guard", "guard", "archive", "guard", "copy", "guard", "guard", "guard", "archive"}
		if runner.stageCopies != 1 || runner.archiveCalls != 2 || !reflect.DeepEqual(runner.events, want) {
			t.Fatalf("copy-stage order=%#v copies=%d archives=%d want=%#v", runner.events, runner.stageCopies, runner.archiveCalls, want)
		}
	})

	t.Run("copy failure has no post-copy guard", func(t *testing.T) {
		fixture, runner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
		defer fixture.close(t)
		copyRefusal := errors.New("copy acknowledgement lost")
		runner.copyErr = copyRefusal
		err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect, func(context.Context) error {
			runner.events = append(runner.events, "guard")
			return nil
		})
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "copy_stage_config", "copy_effect")
		want := []string{"guard", "guard", "archive", "guard", "copy"}
		if errors.Is(err, copyRefusal) || runner.stageCopies != 1 || runner.archiveCalls != 1 || !reflect.DeepEqual(runner.events, want) {
			t.Fatalf("copy failure order=%#v copies=%d archives=%d want=%#v", runner.events, runner.stageCopies, runner.archiveCalls, want)
		}
	})

	t.Run("pre-copy guard refusal", func(t *testing.T) {
		fixture, runner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
		defer fixture.close(t)
		refusal := errors.New("sensitive pre-copy refusal")
		err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect, func(context.Context) error {
			runner.events = append(runner.events, "guard")
			if runner.archiveCalls == 1 {
				return refusal
			}
			return nil
		})
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "copy_stage_config", "pre_effect_guard")
		if errors.Is(err, refusal) || runner.stageCopies != 0 || !reflect.DeepEqual(runner.events, []string{"guard", "guard", "archive", "guard"}) {
			t.Fatalf("pre-copy guard reached an effect: events=%#v copies=%d err=%v", runner.events, runner.stageCopies, err)
		}
	})

	t.Run("post-copy guard refusal", func(t *testing.T) {
		fixture, runner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
		defer fixture.close(t)
		refusal := errors.New("sensitive post-copy refusal")
		err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect, func(context.Context) error {
			runner.events = append(runner.events, "guard")
			if runner.stageCopies == 1 {
				return refusal
			}
			return nil
		})
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "copy_stage_config", "post_effect_guard")
		if errors.Is(err, refusal) || runner.stageCopies != 1 || runner.archiveCalls != 1 ||
			!reflect.DeepEqual(runner.events, []string{"guard", "guard", "archive", "guard", "copy", "guard"}) {
			t.Fatalf("post-copy guard order changed: events=%#v copies=%d archives=%d err=%v", runner.events, runner.stageCopies, runner.archiveCalls, err)
		}
	})

	t.Run("exact inventory replay adds no effect guards", func(t *testing.T) {
		fixture, runner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
		defer fixture.close(t)
		runner.delegate.archiveOverride = gatewayRebindStageConfigCopyTestTar(t, runner.expected, true)
		if err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect, func(context.Context) error {
			runner.events = append(runner.events, "guard")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := []string{"guard", "guard", "archive", "guard", "guard", "archive"}
		if runner.stageCopies != 0 || runner.archiveCalls != 2 || !reflect.DeepEqual(runner.events, want) {
			t.Fatalf("exact replay changed effects/guards: events=%#v copies=%d archives=%d want=%#v", runner.events, runner.stageCopies, runner.archiveCalls, want)
		}
	})
}

func TestGatewayRebindTypedStageRuntimeCopyStageConfigArchiveDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name       string
		failAt     int
		checkpoint string
		copies     int
		events     []string
	}{
		{name: "initial archive", failAt: 1, checkpoint: "stage_archive", events: []string{"guard", "guard", "archive"}},
		{name: "post-copy archive", failAt: 2, checkpoint: "post_copy_stage_archive", copies: 1,
			events: []string{"guard", "guard", "archive", "guard", "copy", "guard", "guard", "guard", "archive"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, runner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
			defer fixture.close(t)
			runner.failArchiveAt = test.failAt
			err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect, func(context.Context) error {
				runner.events = append(runner.events, "guard")
				return nil
			})
			assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "copy_stage_config", test.checkpoint)
			if runner.stageCopies != test.copies || !reflect.DeepEqual(runner.events, test.events) {
				t.Fatalf("archive failure order=%#v copies=%d want=%#v/%d", runner.events, runner.stageCopies, test.events, test.copies)
			}
		})
	}
}
