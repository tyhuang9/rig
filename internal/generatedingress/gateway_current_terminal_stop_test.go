package generatedingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func gatewayCurrentTerminalStopFixture(t *testing.T) (gatewayRebindPredecessorFixture,
	gatewayCurrentSelection, *gatewayCurrentPhysicalExecutor,
) {
	t.Helper()
	f, input, physical := newGatewayRebindCoordinatorFixture(t)
	ctx := context.Background()
	if _, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, physical); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := f.manager.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	port := f.journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
	f.manager.gatewayCurrentPhysicalDriver = managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, port)
	f.manager.options.RebindCurrentStateRepository = nil
	f.manager.options.RebindFenceCheck = func(context.Context) error { return errors.New("SQL unavailable") }
	return f, selection, runner
}

func TestGatewayCurrentTerminalEmergencyStopsDespiteCorruptCurrentBundle(t *testing.T) {
	for _, kind := range []string{"corrupt", "missing", "native and current corrupt"} {
		t.Run(kind, func(t *testing.T) {
			f, selection, runner := gatewayCurrentTerminalStopFixture(t)
			ctx := context.Background()
			if kind != "missing" {
				if err := upgradeProtectedWrite(selection.Store.path, selection.Store.purpose, []byte("{")); err != nil {
					t.Fatal(err)
				}
			} else {
				undo := removeGatewayCurrentAbortTestArtifact(t, selection.Store.path, selection.Store.purpose)
				defer undo()
			}
			if kind == "native and current corrupt" {
				if err := upgradeProtectedWrite(f.manager.store.path, statePurpose, []byte("{")); err != nil {
					t.Fatal(err)
				}
			}
			beforeCurrent, beforeReadErr := os.ReadFile(selection.Store.path)
			if kind != "missing" {
				if _, err := readGatewayRebindProtectedPresenceMode(f.manager.options.DataRoot, false); err == nil {
					t.Fatal("ordinary presence reader accepted corrupt current state")
				}
			}
			before, err := readGatewayHistorySnapshotMode(f.manager.store, true)
			if err != nil {
				t.Fatal(err)
			}
			runner.lostStopAck = true
			f.manager.gatewayRebindCommitBarrierLatch().Store(true)
			result, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
			if err == nil || !result.Incomplete || !result.OwnershipIndeterminate || !result.RebindOwnershipPresent ||
				result.VerifiedTargets != 1 || result.StoppedOrAbsentTargets != 1 || runner.container.Running || len(runner.effects) != 1 ||
				!reflect.DeepEqual(runner.effects[0], []string{"container", "stop", "--time", "10", runner.target.Resources.FinalContainer.ID}) {
				t.Fatalf("terminal withdrawal failed: result=%+v effects=%v err=%v", result, runner.effects, err)
			}
			if !f.manager.gatewayRebindFailStopLatch().Load() || !f.manager.gatewayRebindCommitBarrierLatch().Load() {
				t.Fatal("emergency cleared admission latch")
			}
			afterCurrent, afterReadErr := os.ReadFile(selection.Store.path)
			if !bytes.Equal(beforeCurrent, afterCurrent) || (beforeReadErr == nil) != (afterReadErr == nil) {
				t.Fatal("emergency replaced corrupt or missing routes")
			}
			gatewayRebindSequenceRequireRetainedFiles(t, f.manager, before)
			for _, args := range runner.requests {
				if len(args) < 2 || (args[1] != "inspect" && args[1] != "ls" && args[1] != "stop") {
					t.Fatalf("terminal withdrawal used non-inspect/stop capability: %v", args)
				}
			}
			// Idempotent withdrawal still reports startup corruption, with no effect.
			repeated, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
			if err == nil || !repeated.Incomplete || repeated.StoppedOrAbsentTargets != 1 || len(runner.effects) != 1 {
				t.Fatalf("stopped repeat: %+v %v", repeated, err)
			}
			runner.containerPresent, runner.ownedContainers = false, nil
			absent, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(ctx)
			if err == nil || !absent.Incomplete || absent.StoppedOrAbsentTargets != 1 || len(runner.effects) != 1 {
				t.Fatalf("exact absent repeat: %+v %v", absent, err)
			}
		})
	}
}

func TestGatewayCurrentTerminalEmergencyRefusesForeignOrAmbiguousOwnership(t *testing.T) {
	f, selection, runner := gatewayCurrentTerminalStopFixture(t)
	if err := upgradeProtectedWrite(selection.Store.path, selection.Store.purpose, []byte("{")); err != nil {
		t.Fatal(err)
	}
	base := cloneGatewayCurrentPhysicalTestContainer(runner.container)
	baseRuntime := cloneGatewayCurrentPhysicalTestRuntime(runner.containerRuntime)
	for _, kind := range []string{"replacement ID", "replacement image", "crossed labels", "extra owner", "wrong published port"} {
		t.Run(kind, func(t *testing.T) {
			runner.container = cloneGatewayCurrentPhysicalTestContainer(base)
			runner.containerRuntime = cloneGatewayCurrentPhysicalTestRuntime(baseRuntime)
			runner.effects = nil
			runner.ownedContainers = []string{runner.target.Identity.Rebind.FinalContainer}
			switch kind {
			case "replacement ID":
				runner.container.ID = strings.Repeat("f", 64)
			case "replacement image":
				runner.container.Image = strings.Repeat("9", 64)
			case "crossed labels":
				runner.container.Labels[gatewayRebindIntentDigestLabelKey] = strings.Repeat("9", 64)
			case "extra owner":
				runner.ownedContainers = append(runner.ownedContainers, "unexpected-rebind-owner")
			case "wrong published port":
				runner.container.PortBindings["3000/tcp"] = []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "3000"}}
			}
			result, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
			if err == nil || !result.Incomplete || result.StoppedOrAbsentTargets != 0 || len(runner.effects) != 0 || !runner.container.Running {
				t.Fatalf("unproved owner stopped: %+v %v effects=%v", result, err, runner.effects)
			}
		})
	}
}

type gatewayCurrentTerminalStopHookExecutor struct {
	*gatewayCurrentPhysicalExecutor
	hook       func([]string)
	resultHook func([]string, *runtimeprocess.CommandResult)
}

func (r *gatewayCurrentTerminalStopHookExecutor) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	result, err := r.gatewayCurrentPhysicalExecutor.Run(ctx, request)
	if r.hook != nil {
		r.hook(request.Args)
	}
	if r.resultHook != nil {
		r.resultHook(request.Args, &result)
	}
	return result, err
}

func TestGatewayCurrentTerminalEmergencyRequiresCompleteInventoryAndListenerAbsence(t *testing.T) {
	f, selection, runner := gatewayCurrentTerminalStopFixture(t)
	if err := upgradeProtectedWrite(selection.Store.path, selection.Store.purpose, []byte("{")); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"truncated inventory", "changed volume identity", "listener remains"} {
		t.Run(kind, func(t *testing.T) {
			driver := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, f.journal.Source.LocalHostPort)
			f.manager.gatewayCurrentPhysicalDriver = driver
			if kind == "listener remains" {
				physical := driver.runtime.(managerGatewayCurrentPhysicalRuntime)
				physical.hostProbe = func(context.Context, string, uint16, string, string) gatewayV2HostProbeResult {
					return gatewayV2HostProbeResult{Connected: true}
				}
				driver.runtime = physical
				f.manager.gatewayCurrentPhysicalDriver = driver
			} else {
				f.manager.runner = &gatewayCurrentTerminalStopHookExecutor{gatewayCurrentPhysicalExecutor: runner, resultHook: func(args []string, result *runtimeprocess.CommandResult) {
					if len(args) < 2 {
						return
					}
					if kind == "truncated inventory" && args[0] == "container" && args[1] == "ls" {
						result.StdoutTruncated = true
					}
					if kind == "changed volume identity" && args[0] == "volume" && args[1] == "inspect" {
						result.Stdout = bytes.ReplaceAll(result.Stdout, []byte(runner.target.Resources.ConfigVolume.Mountpoint), []byte("/different-owned-volume"))
					}
				}}
			}
			result, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
			wantEffects := 0
			if kind == "listener remains" {
				wantEffects = 1
			}
			if err == nil || !result.Incomplete || result.StoppedOrAbsentTargets != 0 || len(runner.effects) != wantEffects || runner.container.Running != (wantEffects == 0) {
				t.Fatalf("incomplete physical proof accepted: result=%+v effects=%v err=%v", result, runner.effects, err)
			}
		})
	}
}

func TestGatewayCurrentTerminalEmergencyRechecksHistoryBeforeStop(t *testing.T) {
	for _, kind := range []string{"terminal", "current bytes"} {
		t.Run(kind, func(t *testing.T) {
			f, selection, runner := gatewayCurrentTerminalStopFixture(t)
			if err := upgradeProtectedWrite(selection.Store.path, selection.Store.purpose, []byte("{")); err != nil {
				t.Fatal(err)
			}
			history, err := f.manager.gatewayCurrentOwnedStopHistoryLocked()
			if err != nil {
				t.Fatal(err)
			}
			mutated := false
			f.manager.runner = &gatewayCurrentTerminalStopHookExecutor{gatewayCurrentPhysicalExecutor: runner, hook: func(args []string) {
				if mutated || len(args) < 2 || args[0] != "container" || args[1] != "inspect" {
					return
				}
				mutated = true
				path, purpose := history.TerminalsV2[0].Store.path, history.TerminalsV2[0].Store.purpose
				if kind == "current bytes" {
					path, purpose = selection.Store.path, selection.Store.purpose
				}
				if err := upgradeProtectedWrite(path, purpose, []byte("{{")); err != nil {
					t.Fatal(err)
				}
			}}
			result, err := f.manager.stopOwnedGatewayCurrentOnStartupFailure(context.Background())
			if !mutated || err == nil || !result.Incomplete || result.StoppedOrAbsentTargets != 0 || len(runner.effects) != 0 || !runner.container.Running {
				t.Fatalf("late history drift crossed stop: %+v %v effects=%v", result, err, runner.effects)
			}
		})
	}
}
