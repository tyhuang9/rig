package generatedingress

import (
	"archive/tar"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appaccess"
)

type gatewayRebindPrefixDriver struct {
	managerGatewayRebindCrossStoreDriver
	stopSequence uint64
}

func (d gatewayRebindPrefixDriver) reconcileSuccessorLocked(ctx context.Context,
	request gatewayRebindPhysicalReconcileRequest, appendProgress gatewayRebindTypedProgressAppender,
) (gatewayRebindTypedPhysicalResult, error) {
	return d.managerGatewayRebindCrossStoreDriver.reconcileSuccessorLocked(ctx, request,
		func(ctx context.Context, record gatewayRebindProgressRecord) error {
			if err := appendProgress(ctx, record); err != nil {
				return err
			}
			if record.Sequence == d.stopSequence {
				return errors.New("injected stop after durable typed prefix")
			}
			return nil
		})
}

func TestGatewayRebindTypedHandoverRuntimeRejectsStageConfigDriftDuringRollbackProof(t *testing.T) {
	newFixture := func(t *testing.T) (gatewayRebindTypedStageRuntimeFixture, gatewayRebindTypedHandoverRuntime,
		gatewayRebindPreparedAttempt, gatewayRebindTypedEffectProgress,
	) {
		t.Helper()
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		effect := *fixture.records[7].TypedEffect
		attempt := gatewayRebindPreparedAttempt{Intent: fixture.intent, Checkpoint: fixture.checkpoint}
		runtime := gatewayRebindTypedHandoverRuntime{manager: fixture.runtime.manager, stage: fixture.runtime}
		return fixture, runtime, attempt, effect
	}
	exactArchive := func(t *testing.T, fixture gatewayRebindTypedStageRuntimeFixture, body []byte) []byte {
		t.Helper()
		headers := []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			gatewayRebindPinnedImageConfigDirectoryTestHeader(),
			{Name: fixture.intent.Identity.StageConfigFilename, Typeflag: tar.TypeReg,
				Size: int64(len(body))},
		}
		return gatewayRebindStageConfigCopyHeadersTar(t, headers, body)
	}

	t.Run("exact copied stage config", func(t *testing.T) {
		fixture, runtime, attempt, effect := newFixture(t)
		defer fixture.close(t)
		fixture.runner.archiveOverride = exactArchive(t, fixture, fixture.runner.stageBody)

		if _, err := runtime.stableRollbackPrefix(context.Background(), attempt, effect,
			func(context.Context) error { return nil }); err != nil {
			t.Fatalf("exact copied stage config was rejected: %v", err)
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("rollback proof dispatched Docker effects: %#v", fixture.runner.effects)
		}
	})

	t.Run("exact started stage config", func(t *testing.T) {
		fixture, runtime, attempt, _ := newFixture(t)
		defer fixture.close(t)
		effect := *fixture.records[8].TypedEffect

		if _, err := runtime.stableRollbackPrefix(context.Background(), attempt, effect,
			func(context.Context) error { return nil }); err != nil {
			t.Fatalf("exact started stage config was rejected: %v", err)
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("started-stage rollback proof dispatched Docker effects: %#v", fixture.runner.effects)
		}
	})

	t.Run("unexpected archive member", func(t *testing.T) {
		fixture, runtime, attempt, effect := newFixture(t)
		defer fixture.close(t)
		fixture.runner.archiveOverride = finalConfigAutosaveArchive(t, fixture.runner.stageBody, nil, nil,
			"caddy/", fixture.intent.Identity.StageConfigFilename, "unexpected-drift.json")

		if _, err := runtime.stableRollbackPrefix(context.Background(), attempt, effect,
			func(context.Context) error { return nil }); err == nil {
			t.Fatal("rollback proof accepted an unexpected stage-config archive member")
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("drifted rollback proof dispatched Docker effects: %#v", fixture.runner.effects)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindTypedStageRuntimeFixture)
	}{
		{name: "changed expected bytes", mutate: func(f *gatewayRebindTypedStageRuntimeFixture) {
			f.runner.archiveOverride = exactArchive(t, *f, []byte(`{"changed":true}`))
		}},
		{name: "archive read error", mutate: func(f *gatewayRebindTypedStageRuntimeFixture) {
			f.runner.archiveOverride = exactArchive(t, *f, f.runner.stageBody)
			f.runner.archiveErr = errors.New("injected archive read failure")
		}},
		{name: "truncated archive result", mutate: func(f *gatewayRebindTypedStageRuntimeFixture) {
			f.runner.archiveOverride = exactArchive(t, *f, f.runner.stageBody)
			f.runner.archiveStdoutTruncated = true
		}},
		{name: "identity drift after archive", mutate: func(f *gatewayRebindTypedStageRuntimeFixture) {
			f.runner.archiveOverride = exactArchive(t, *f, f.runner.stageBody)
			f.runner.afterArchive = func(r *gatewayRebindTypedStageRuntimeRunner) {
				r.configVolume.Mountpoint += "-replacement"
			}
		}},
		{name: "topology drift after archive", mutate: func(f *gatewayRebindTypedStageRuntimeFixture) {
			f.runner.archiveOverride = exactArchive(t, *f, f.runner.stageBody)
			f.runner.afterArchive = func(r *gatewayRebindTypedStageRuntimeRunner) {
				r.networkID = strings.Repeat("f", 64)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, runtime, attempt, effect := newFixture(t)
			defer fixture.close(t)
			test.mutate(&fixture)

			if _, err := runtime.stableRollbackPrefix(context.Background(), attempt, effect,
				func(context.Context) error { return nil }); err == nil {
				t.Fatal("rollback proof accepted unavailable or changed stage-config evidence")
			}
			if len(fixture.runner.effects) != 0 {
				t.Fatalf("refused rollback proof dispatched Docker effects: %#v", fixture.runner.effects)
			}
		})
	}

	t.Run("authority drift after archive", func(t *testing.T) {
		fixture, runtime, attempt, effect := newFixture(t)
		defer fixture.close(t)
		fixture.runner.archiveOverride = exactArchive(t, fixture, fixture.runner.stageBody)
		stale := false
		fixture.runner.afterArchive = func(*gatewayRebindTypedStageRuntimeRunner) { stale = true }
		guard := func(context.Context) error {
			if stale {
				return errors.New("injected stale rollback authority")
			}
			return nil
		}

		if _, err := runtime.stableRollbackPrefix(context.Background(), attempt, effect, guard); err == nil {
			t.Fatal("rollback proof accepted authority drift after the archive read")
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("authority drift dispatched Docker effects: %#v", fixture.runner.effects)
		}
	})

	t.Run("cancellation after archive", func(t *testing.T) {
		fixture, runtime, attempt, effect := newFixture(t)
		defer fixture.close(t)
		fixture.runner.archiveOverride = exactArchive(t, fixture, fixture.runner.stageBody)
		ctx, cancel := context.WithCancel(context.Background())
		fixture.runner.afterArchive = func(*gatewayRebindTypedStageRuntimeRunner) { cancel() }

		if _, err := runtime.stableRollbackPrefix(ctx, attempt, effect,
			func(context.Context) error { return nil }); err == nil {
			t.Fatal("rollback proof accepted cancellation after the archive read")
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("cancelled rollback proof dispatched Docker effects: %#v", fixture.runner.effects)
		}
	})
}

func TestGatewayRebindTypedHandoverRuntimeEnforcesRollbackConfigPhaseContract(t *testing.T) {
	emptyArchive := func(t *testing.T) []byte {
		t.Helper()
		return gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			gatewayRebindPinnedImageConfigDirectoryTestHeader(),
		}, nil)
	}
	exactStageArchive := func(t *testing.T, fixture gatewayRebindTypedStageRuntimeFixture, body []byte) []byte {
		t.Helper()
		return gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{
			{Name: ".", Typeflag: tar.TypeDir},
			gatewayRebindPinnedImageConfigDirectoryTestHeader(),
			{Name: fixture.intent.Identity.StageConfigFilename, Typeflag: tar.TypeReg, Size: int64(len(body))},
		}, body)
	}
	prove := func(t *testing.T, sequence int, configure func(gatewayRebindTypedStageRuntimeFixture,
		gatewayRebindTypedEffectProgress) []byte, want bool,
	) {
		t.Helper()
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		effect := *fixture.records[sequence-1].TypedEffect
		fixture.runner.effect = effect
		fixture.runner.archiveOverride = configure(fixture, effect)
		attempt := gatewayRebindPreparedAttempt{Intent: fixture.intent, Checkpoint: fixture.checkpoint}
		runtime := gatewayRebindTypedHandoverRuntime{manager: fixture.runtime.manager, stage: fixture.runtime}
		_, err := runtime.stableRollbackPrefix(context.Background(), attempt, effect,
			func(context.Context) error { return nil })
		if (err == nil) != want {
			t.Fatalf("sequence %d rollback archive accepted=%t want=%t error=%v", sequence, err == nil, want, err)
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("sequence %d rollback proof dispatched Docker effects: %#v", sequence, fixture.runner.effects)
		}
	}

	for _, test := range []struct {
		name      string
		sequence  int
		configure func(gatewayRebindTypedStageRuntimeFixture, gatewayRebindTypedEffectProgress) []byte
		want      bool
	}{
		{name: "pre-intent empty", sequence: 6,
			configure: func(gatewayRebindTypedStageRuntimeFixture, gatewayRebindTypedEffectProgress) []byte {
				return emptyArchive(t)
			}, want: true},
		{name: "pre-intent copied bytes", sequence: 6,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return exactStageArchive(t, f, f.runner.stageBody)
			}},
		{name: "p07 empty before copy", sequence: 7,
			configure: func(gatewayRebindTypedStageRuntimeFixture, gatewayRebindTypedEffectProgress) []byte {
				return emptyArchive(t)
			}, want: true},
		{name: "p07 exact lost acknowledgement", sequence: 7,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return exactStageArchive(t, f, f.runner.stageBody)
			}, want: true},
		{name: "p07 unexpected member", sequence: 7,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return finalConfigAutosaveArchive(t, f.runner.stageBody, nil, nil,
					"caddy/", f.intent.Identity.StageConfigFilename, "unexpected-drift.json")
			}},
		{name: "p07 changed stage bytes", sequence: 7,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return exactStageArchive(t, f, []byte(`{"changed":true}`))
			}},
		{name: "p11 stage-only before final copy", sequence: 11,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return stageAutosaveTestArchive(t, f.runner.stageBody, f.runner.autosave)
			}, want: true},
		{name: "p11 exact-pair lost acknowledgement", sequence: 11,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, effect gatewayRebindTypedEffectProgress) []byte {
				active, err := gatewayRebindTypedFinalConfigBytes(f.intent, f.checkpoint,
					effect.FinalConfigIntent.RoutePlan)
				if err != nil {
					t.Fatal(err)
				}
				defer clear(active)
				return finalConfigAutosaveArchive(t, f.runner.stageBody, active, f.runner.autosave,
					"caddy/", f.intent.Identity.StageConfigFilename, f.intent.Identity.ActiveConfigFilename,
					"caddy/autosave.json")
			}, want: true},
		{name: "p11 changed active bytes", sequence: 11,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return finalConfigAutosaveArchive(t, f.runner.stageBody, []byte(`{"changed":true}`), f.runner.autosave,
					"caddy/", f.intent.Identity.StageConfigFilename, f.intent.Identity.ActiveConfigFilename,
					"caddy/autosave.json")
			}},
		{name: "p11 unexpected member", sequence: 11,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, effect gatewayRebindTypedEffectProgress) []byte {
				active, err := gatewayRebindTypedFinalConfigBytes(f.intent, f.checkpoint,
					effect.FinalConfigIntent.RoutePlan)
				if err != nil {
					t.Fatal(err)
				}
				defer clear(active)
				return finalConfigAutosaveArchive(t, f.runner.stageBody, active, f.runner.autosave,
					"caddy/", f.intent.Identity.StageConfigFilename, f.intent.Identity.ActiveConfigFilename,
					"caddy/autosave.json", "unexpected-drift.json")
			}},
		{name: "p12 exact pair", sequence: 12,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, effect gatewayRebindTypedEffectProgress) []byte {
				active, err := gatewayRebindTypedFinalConfigBytes(f.intent, f.checkpoint,
					effect.FinalConfigIntent.RoutePlan)
				if err != nil {
					t.Fatal(err)
				}
				defer clear(active)
				return finalConfigAutosaveArchive(t, f.runner.stageBody, active, f.runner.autosave,
					"caddy/", f.intent.Identity.StageConfigFilename, f.intent.Identity.ActiveConfigFilename,
					"caddy/autosave.json")
			}, want: true},
		{name: "p12 missing active", sequence: 12,
			configure: func(f gatewayRebindTypedStageRuntimeFixture, _ gatewayRebindTypedEffectProgress) []byte {
				return stageAutosaveTestArchive(t, f.runner.stageBody, f.runner.autosave)
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prove(t, test.sequence, test.configure, test.want)
		})
	}
}

func TestGatewayRebindTypedHandoverRuntimeRetainsDriftedP07RollbackPrefix(t *testing.T) {
	predecessor, input, bounded := newGatewayRebindCoordinatorFixtureWithPredecessor(t,
		newGatewayRebindPredecessorFixtureWithEndpointAndLANApprover(t, false, '3', uuid.NewString()))
	observation := gatewayRebindFixtureDockerObservation(t, predecessor)
	bounded.template.receipt.Resources.ImageID = normalizeID(observation.Image.ID)
	stageFake := &gatewayRebindTypedStageDriverFake{t: t, template: bounded.template}
	prefixDriver := gatewayRebindPrefixDriver{managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{
		manager: predecessor.manager, stage: stageFake,
	}, stopSequence: 7}
	if result, err := predecessor.manager.commitGatewayRebindWithDriver(context.Background(), predecessor.repository,
		input, prefixDriver); err == nil || result != (GatewayRebindCommitResult{}) {
		t.Fatalf("p07 prefix injection result=%#v error=%v", result, err)
	}
	history, err := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) != 7 ||
		history.Progress[6].Record.Sequence != 7 ||
		history.Progress[6].Record.Phase != gatewayRebindProgressStageConfigIntent ||
		history.Progress[6].Record.TypedEffect == nil ||
		history.Progress[6].Record.TypedEffect.StageConfigIntent == nil ||
		history.Progress[6].Record.TypedEffect.StageConfigCopy != nil || len(history.TerminalsV2) != 0 {
		t.Fatalf("durable p07 prefix progress=%d terminals=%d error=%v", len(history.Progress), len(history.TerminalsV2), err)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(history.IntentsV2[0].Intent, history)
	if err != nil {
		t.Fatal(err)
	}
	records := historyProgressRecords(history, attempt.Intent.Generation, attempt.Intent.OperationID)
	retainedPrefix := append([]gatewayRebindProgressRecord(nil), records...)
	effect := *records[6].TypedEffect
	stageBody, err := gatewayRebindTypedStageConfigBytes(attempt.Intent)
	if err != nil {
		t.Fatal(err)
	}
	autosave, err := gatewayRebindCanonicalAutosaveConfig(stageBody)
	if err != nil {
		clear(stageBody)
		t.Fatal(err)
	}
	stage := newGatewayRebindTypedStageRuntimeRunner(t, attempt.Intent, effect, stageBody, autosave)
	stage.archiveOverride = finalConfigAutosaveArchive(t, stageBody, nil, nil,
		"caddy/", attempt.Intent.Identity.StageConfigFilename, "unexpected-drift.json")
	runner := &gatewayRebindTypedHandoverRuntimeRunner{t: t, intent: attempt.Intent, effect: effect,
		predecessorState: predecessor.state, predecessor: observation, stage: stage,
		stagePresent: true, networkPresent: true, configPresent: true, dataPresent: true,
		finalID: strings.Repeat("9", 64)}
	predecessor.manager.runner = runner
	reads := gatewayRebindTypedHandoverRuntimeReads(t, attempt.Intent, effect.Network.ID,
		func() bool { return runner.networkPresent })
	stageRuntime := gatewayRebindTypedStageRuntime{manager: predecessor.manager, reads: reads}
	runtime := gatewayRebindTypedHandoverRuntime{manager: predecessor.manager, stage: stageRuntime}
	driver := managerGatewayRebindCrossStoreDriver{manager: predecessor.manager, stage: stageRuntime, handover: runtime}
	appendProgress := func(ctx context.Context, record gatewayRebindProgressRecord) error {
		store, storeErr := newGatewayRebindProgressStore(predecessor.manager.options.DataRoot,
			record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil {
			return storeErr
		}
		if storeErr = store.installExact(ctx, record); storeErr != nil {
			return storeErr
		}
		if record.TypedEffect != nil {
			runner.effect = *record.TypedEffect
			runner.stage.effect = *record.TypedEffect
		}
		return nil
	}
	beforeSnapshot, err := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || beforeSnapshot.Active == nil || beforeSnapshot.Phase != "prepared" || !beforeSnapshot.RollbackAllowed {
		t.Fatalf("p07 rollback snapshot=%#v error=%v", beforeSnapshot, err)
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: attempt, Mode: gatewayRebindPhysicalReconcileUndecided,
		SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	result, reconcileErr := driver.reconcileSuccessorLocked(context.Background(), request, appendProgress)
	if reconcileErr == nil || !reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) {
		t.Fatalf("drifted p07 fallback result=%#v error=%v", result, reconcileErr)
	}
	afterHistory, historyErr := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterRecords := historyProgressRecords(afterHistory, attempt.Intent.Generation, attempt.Intent.OperationID)
	afterSnapshot, snapshotErr := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	hasRollback := false
	for _, record := range afterRecords {
		hasRollback = hasRollback || record.TypedRollback != nil
	}
	if historyErr != nil || !reflect.DeepEqual(afterRecords, retainedPrefix) || hasRollback ||
		len(afterHistory.TerminalsV2) != 0 || snapshotErr != nil ||
		!reflect.DeepEqual(beforeSnapshot, afterSnapshot) || !runner.stagePresent || runner.finalPresent ||
		!runner.configPresent || !runner.dataPresent || !runner.networkPresent ||
		!runner.predecessor.FinalContainer.Running || len(runner.effects) != 0 || stage.archiveReads != 2 {
		t.Fatalf("drifted p07 state progress=%d terminals=%d rollback=%t historyErr=%v snapshotErr=%v snapshotEqual=%t stage=%t final=%t config=%t data=%t network=%t predecessorRunning=%t archiveReads=%d effects=%#v",
			len(afterRecords), len(afterHistory.TerminalsV2), hasRollback, historyErr, snapshotErr,
			reflect.DeepEqual(beforeSnapshot, afterSnapshot), runner.stagePresent, runner.finalPresent,
			runner.configPresent, runner.dataPresent, runner.networkPresent,
			runner.predecessor.FinalContainer.Running, stage.archiveReads, runner.effects)
	}
	t.Cleanup(func() {
		clear(stage.stageBody)
		clear(stage.activeBody)
		clear(stage.autosave)
		clearGatewayV2DockerObservation(&runner.predecessor)
	})
}

func TestGatewayRebindTypedHandoverRuntimeRetainsDriftedP08RollbackPrefix(t *testing.T) {
	predecessor, input, bounded := newGatewayRebindCoordinatorFixtureWithPredecessor(t,
		newGatewayRebindPredecessorFixtureWithEndpointAndLANApprover(t, false, '3', uuid.NewString()))
	observation := gatewayRebindFixtureDockerObservation(t, predecessor)
	bounded.template.receipt.Resources.ImageID = normalizeID(observation.Image.ID)
	stageFake := &gatewayRebindTypedStageDriverFake{t: t, template: bounded.template}
	prefixDriver := gatewayRebindPrefixDriver{managerGatewayRebindCrossStoreDriver: managerGatewayRebindCrossStoreDriver{
		manager: predecessor.manager, stage: stageFake,
	}, stopSequence: 8}
	if result, err := predecessor.manager.commitGatewayRebindWithDriver(context.Background(), predecessor.repository,
		input, prefixDriver); err == nil || result != (GatewayRebindCommitResult{}) {
		t.Fatalf("p08 prefix injection result=%#v error=%v", result, err)
	}
	history, err := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.IntentsV2) != 1 || len(history.Progress) != 8 ||
		history.Progress[7].Record.Sequence != 8 ||
		history.Progress[7].Record.Phase != gatewayRebindProgressStageConfigCopied ||
		history.Progress[7].Record.TypedEffect == nil ||
		history.Progress[7].Record.TypedEffect.StageConfigCopy == nil || len(history.TerminalsV2) != 0 {
		t.Fatalf("durable p08 prefix progress=%d terminals=%d error=%v", len(history.Progress), len(history.TerminalsV2), err)
	}
	attempt, err := gatewayRebindTypedPreparedAttemptFromHistory(history.IntentsV2[0].Intent, history)
	if err != nil {
		t.Fatal(err)
	}
	records := historyProgressRecords(history, attempt.Intent.Generation, attempt.Intent.OperationID)
	retainedPrefix := append([]gatewayRebindProgressRecord(nil), records...)
	effect := *records[7].TypedEffect
	stageBody, err := gatewayRebindTypedStageConfigBytes(attempt.Intent)
	if err != nil {
		t.Fatal(err)
	}
	autosave, err := gatewayRebindCanonicalAutosaveConfig(stageBody)
	if err != nil {
		clear(stageBody)
		t.Fatal(err)
	}
	stage := newGatewayRebindTypedStageRuntimeRunner(t, attempt.Intent, effect, stageBody, autosave)
	stage.archiveOverride = finalConfigAutosaveArchive(t, stageBody, nil, nil,
		"caddy/", attempt.Intent.Identity.StageConfigFilename, "unexpected-drift.json")
	runner := &gatewayRebindTypedHandoverRuntimeRunner{t: t, intent: attempt.Intent, effect: effect,
		predecessorState: predecessor.state, predecessor: observation, stage: stage,
		stagePresent: true, networkPresent: true, configPresent: true, dataPresent: true,
		finalID: strings.Repeat("9", 64)}
	predecessor.manager.runner = runner
	reads := gatewayRebindTypedHandoverRuntimeReads(t, attempt.Intent, effect.Network.ID,
		func() bool { return runner.networkPresent })
	stageRuntime := gatewayRebindTypedStageRuntime{manager: predecessor.manager, reads: reads}
	runtime := gatewayRebindTypedHandoverRuntime{manager: predecessor.manager, stage: stageRuntime}
	driver := managerGatewayRebindCrossStoreDriver{manager: predecessor.manager, stage: stageRuntime, handover: runtime}
	appendProgress := func(ctx context.Context, record gatewayRebindProgressRecord) error {
		store, storeErr := newGatewayRebindProgressStore(predecessor.manager.options.DataRoot,
			record.Generation, record.OperationID, record.Sequence)
		if storeErr != nil {
			return storeErr
		}
		if storeErr = store.installExact(ctx, record); storeErr != nil {
			return storeErr
		}
		if record.TypedEffect != nil {
			runner.effect = *record.TypedEffect
			runner.stage.effect = *record.TypedEffect
		}
		return nil
	}
	beforeSnapshot, err := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil || beforeSnapshot.Active == nil || beforeSnapshot.Phase != "prepared" || !beforeSnapshot.RollbackAllowed {
		t.Fatalf("p08 rollback snapshot=%#v error=%v", beforeSnapshot, err)
	}
	request := gatewayRebindPhysicalReconcileRequest{Attempt: attempt, Mode: gatewayRebindPhysicalReconcileUndecided,
		SQLPhase: appaccess.GatewayRebindPrepared, RollbackAllowed: true}
	if !validGatewayRebindPhysicalReconcileRequest(request) {
		t.Fatal("p08 configuration-drift request is invalid")
	}
	result, reconcileErr := driver.reconcileSuccessorLocked(context.Background(), request, appendProgress)
	if reconcileErr == nil || !reflect.DeepEqual(result, gatewayRebindTypedPhysicalResult{}) {
		t.Fatalf("drifted p08 fallback result=%#v error=%v", result, reconcileErr)
	}
	afterHistory, historyErr := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterRecords := historyProgressRecords(afterHistory, attempt.Intent.Generation, attempt.Intent.OperationID)
	afterSnapshot, snapshotErr := predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	hasRollback := false
	for _, record := range afterRecords {
		hasRollback = hasRollback || record.TypedRollback != nil
	}
	if historyErr != nil || len(afterRecords) != 9 || !reflect.DeepEqual(afterRecords[:8], retainedPrefix) ||
		afterRecords[8].Sequence != 9 || afterRecords[8].Phase != gatewayRebindProgressStageStartIntent ||
		hasRollback || len(afterHistory.TerminalsV2) != 0 || snapshotErr != nil ||
		!reflect.DeepEqual(beforeSnapshot, afterSnapshot) || !runner.stagePresent || runner.finalPresent ||
		!runner.configPresent || !runner.dataPresent || !runner.networkPresent ||
		!runner.predecessor.FinalContainer.Running || len(runner.effects) != 0 || stage.archiveReads != 2 {
		t.Fatalf("drifted p08 state progress=%d terminals=%d rollback=%t historyErr=%v snapshotErr=%v snapshotEqual=%t stage=%t final=%t config=%t data=%t network=%t predecessorRunning=%t archiveReads=%d effects=%#v",
			len(afterRecords), len(afterHistory.TerminalsV2), hasRollback, historyErr, snapshotErr,
			reflect.DeepEqual(beforeSnapshot, afterSnapshot), runner.stagePresent, runner.finalPresent,
			runner.configPresent, runner.dataPresent, runner.networkPresent,
			runner.predecessor.FinalContainer.Running, stage.archiveReads, runner.effects)
	}
	t.Cleanup(func() {
		clear(stage.stageBody)
		clear(stage.activeBody)
		clear(stage.autosave)
		clearGatewayV2DockerObservation(&runner.predecessor)
	})
}

func TestGatewayRebindTypedHandoverRuntimeAttestsAdoptedP13FinalWithoutRewritingHistory(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	initialGuard := func(context.Context) error { return nil }
	effect := fixture.effectAt(12)
	preparation, err := fixture.runtime.prepareHandover(context.Background(), fixture.attempt,
		fixture.current, effect, initialGuard)
	if err != nil {
		t.Fatal(err)
	}
	handover, err := gatewayRebindTypedHandoverIntentFor(fixture.attempt.Intent, fixture.records[11], effect,
		preparation.LocalHostPort, preparation.PredecessorObservationDigest,
		preparation.PredecessorInitiallyRunning, preparation.ApplicationNetworks)
	if err != nil {
		t.Fatal(err)
	}
	effect.HandoverIntent = &handover
	effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), preparation.ApplicationNetworks...)
	lastAt, err := parseGatewayRebindProgressTime(fixture.records[len(fixture.records)-1].OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	record, err := newGatewayRebindTypedEffectProgressV2(fixture.attempt.Intent, fixture.attempt.Checkpoint,
		fixture.records, effect, lastAt.Add(time.Nanosecond))
	if err != nil || record.Sequence != 13 || record.TypedEffect == nil {
		t.Fatalf("build durable p13 record=%#v error=%v", record, err)
	}
	if err = fixture.appendProgress(context.Background(), record); err != nil {
		t.Fatalf("persist durable p13 record: %v", err)
	}
	history, err := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil || len(history.Progress) != 13 || history.Progress[12].Record.TypedEffect == nil {
		t.Fatalf("scan durable p13 history progress=%d error=%v", len(history.Progress), err)
	}
	progress := historyProgressRecords(history, fixture.attempt.Intent.Generation, fixture.attempt.Intent.OperationID)
	retainedProgress := append([]gatewayRebindProgressRecord(nil), progress...)
	sourceEffect := *progress[12].TypedEffect
	sourceDigest := sourceEffect.Digest
	fixture.runner.createFinal(nil)
	fixture.runner.effects = nil
	expectedSnapshot, err := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	guardCalls := 0
	guard := func(guardCtx context.Context) error {
		guardCalls++
		freshHistory, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
		freshSnapshot, snapshotErr := fixture.predecessor.repository.GatewayRebindRecoverySnapshot(guardCtx)
		freshProgress := historyProgressRecords(freshHistory, fixture.attempt.Intent.Generation,
			fixture.attempt.Intent.OperationID)
		if historyErr != nil || snapshotErr != nil || !reflect.DeepEqual(freshProgress, retainedProgress) ||
			!reflect.DeepEqual(freshSnapshot, expectedSnapshot) {
			return errors.New("durable p13 rollback authority changed")
		}
		return nil
	}

	rollback, err := fixture.runtime.prepareRollback(context.Background(), fixture.attempt,
		fixture.current, progress, guard)
	if err != nil || rollback.Owned.FinalContainer == nil ||
		rollback.Owned.FinalContainer.ID != fixture.runner.finalID || !validSHA256(rollback.AdoptionProofDigest) {
		t.Fatalf("adopt p13 final rollback=%#v error=%v", rollback, err)
	}
	if sourceEffect.Digest != sourceDigest || progress[12].TypedEffect.Digest != sourceDigest ||
		!validGatewayRebindTypedEffectProgress(*progress[12].TypedEffect, 13) {
		t.Fatal("p13 rollback attestation rewrote or invalidated durable effect history")
	}
	if _, err = fixture.runtime.rollbackState(context.Background(), fixture.attempt, sourceEffect,
		rollback.Owned, guard); err != nil {
		t.Fatalf("attest adopted p13 rollback projection: %v", err)
	}
	if len(fixture.runner.effects) != 0 {
		t.Fatalf("p13 adoption proof dispatched Docker effects: %#v", fixture.runner.effects)
	}
	afterHistory, historyErr := fixture.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterProgress := historyProgressRecords(afterHistory, fixture.attempt.Intent.Generation,
		fixture.attempt.Intent.OperationID)
	if historyErr != nil || !reflect.DeepEqual(afterProgress, retainedProgress) || guardCalls < 4 {
		t.Fatalf("p13 proof history changed=%t guardCalls=%d error=%v",
			!reflect.DeepEqual(afterProgress, retainedProgress), guardCalls, historyErr)
	}
}

func TestGatewayRebindTypedHandoverRuntimeRejectsFinalConfigDriftBeforeRollbackRemoval(t *testing.T) {
	newFixture := func(t *testing.T) (gatewayRebindTypedHandoverRuntimeFixture,
		gatewayRebindTypedEffectProgress,
	) {
		t.Helper()
		fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
		effect := fixture.effectAt(12)
		guard := func(context.Context) error { return nil }
		preparation, err := fixture.runtime.prepareHandover(context.Background(), fixture.attempt,
			fixture.current, effect, guard)
		if err != nil {
			t.Fatal(err)
		}
		handover, err := gatewayRebindTypedHandoverIntentFor(fixture.attempt.Intent, fixture.records[11], effect,
			preparation.LocalHostPort, preparation.PredecessorObservationDigest,
			preparation.PredecessorInitiallyRunning, preparation.ApplicationNetworks)
		if err != nil {
			t.Fatal(err)
		}
		effect.HandoverIntent = &handover
		effect.ApplicationNetworks = append([]gatewayRebindHandoverApplicationNetwork(nil), preparation.ApplicationNetworks...)
		final, err := gatewayRebindTypedFinalContainerBindingFor(fixture.attempt.Intent, effect,
			effect.HandoverIntent.Plan, fixture.runner.finalID)
		if err != nil {
			t.Fatal(err)
		}
		effect.FinalContainer = &final
		effect.Digest = ""
		effect.Digest, err = gatewayRebindTypedEffectDigest(effect)
		if err != nil || !validGatewayRebindTypedEffectProgress(effect, 14) {
			t.Fatalf("build p14 final effect: %v", err)
		}
		fixture.runner.effect = effect
		fixture.runner.stage.effect = effect
		fixture.runner.createFinal(nil)
		fixture.runner.effects = nil
		return fixture, effect
	}

	t.Run("exact final archive", func(t *testing.T) {
		fixture, effect := newFixture(t)
		if _, err := fixture.runtime.stableRollbackResources(context.Background(), fixture.attempt,
			effect, effect, false, true, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("exact final-container archive was rejected: %v", err)
		}
		if len(fixture.runner.effects) != 0 {
			t.Fatalf("clean final proof dispatched Docker effects: %#v", fixture.runner.effects)
		}
	})

	t.Run("unexpected member before removal", func(t *testing.T) {
		fixture, effect := newFixture(t)
		fixture.runner.stage.archiveOverride = finalConfigAutosaveArchive(t, fixture.runner.stage.stageBody,
			fixture.runner.stage.activeBody, fixture.runner.stage.autosave,
			"caddy/", fixture.attempt.Intent.Identity.StageConfigFilename,
			fixture.attempt.Intent.Identity.ActiveConfigFilename, "caddy/autosave.json", "unexpected-drift.json")
		remaining := gatewayRebindTypedRollbackOwnedFromEffect(effect)
		got, removeErr := fixture.runtime.rollbackRemoveContainer(context.Background(), fixture.attempt,
			effect, remaining, true, func(context.Context) error { return nil })
		if removeErr == nil || !reflect.DeepEqual(got, remaining) || !fixture.runner.finalPresent ||
			fixture.runner.stagePresent || fixture.runner.effectCount("container", "stop") != 0 ||
			fixture.runner.effectCount("container", "rm") != 0 {
			t.Fatalf("drifted final rollback remaining=%#v want=%#v error=%v stage=%t final=%t effects=%#v",
				got, remaining, removeErr, fixture.runner.stagePresent, fixture.runner.finalPresent, fixture.runner.effects)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*gatewayRebindTypedHandoverRuntimeFixture, *bool, context.CancelFunc)
	}{
		{name: "archive read error", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, _ *bool, _ context.CancelFunc) {
			f.runner.stage.archiveErr = errors.New("injected final archive read failure")
		}},
		{name: "truncated archive", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, _ *bool, _ context.CancelFunc) {
			f.runner.stage.archiveStdoutTruncated = true
		}},
		{name: "identity drift after archive", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, _ *bool, _ context.CancelFunc) {
			f.runner.stage.afterArchive = func(*gatewayRebindTypedStageRuntimeRunner) {
				f.runner.final.ID = strings.Repeat("8", 64)
			}
		}},
		{name: "topology drift after archive", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, _ *bool, _ context.CancelFunc) {
			f.runner.stage.afterArchive = func(r *gatewayRebindTypedStageRuntimeRunner) {
				r.networkID = strings.Repeat("f", 64)
			}
		}},
		{name: "authority drift after archive", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, stale *bool, _ context.CancelFunc) {
			f.runner.stage.afterArchive = func(*gatewayRebindTypedStageRuntimeRunner) { *stale = true }
		}},
		{name: "cancellation after archive", mutate: func(f *gatewayRebindTypedHandoverRuntimeFixture, _ *bool, cancel context.CancelFunc) {
			f.runner.stage.afterArchive = func(*gatewayRebindTypedStageRuntimeRunner) { cancel() }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, effect := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stale := false
			test.mutate(&fixture, &stale, cancel)
			guard := func(context.Context) error {
				if stale {
					return errors.New("injected stale final rollback authority")
				}
				return nil
			}
			if _, err := fixture.runtime.stableRollbackResources(ctx, fixture.attempt,
				effect, effect, false, true, guard); err == nil {
				t.Fatal("final rollback proof accepted unavailable or changed evidence")
			}
			if len(fixture.runner.effects) != 0 {
				t.Fatalf("refused final proof dispatched Docker effects: %#v", fixture.runner.effects)
			}
		})
	}
}
