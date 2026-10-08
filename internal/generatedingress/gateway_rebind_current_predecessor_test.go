package generatedingress

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestGatewayRebindStoppedCurrentPredecessorDoesNotClaimListenerAbsence(t *testing.T) {
	f := newGatewayCurrentStateFixture(t)
	f.manager.options.HostPort = f.history.Predecessor.Journal.Source.LocalHostPort
	target := gatewayCurrentPhysicalRuntimeTestTarget(t, f)
	runner := newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State, f.manager.options.HostPort)
	runner.stopAt(mustGatewayCurrentPhysicalConfig(t, target.State))
	f.manager.runner = runner
	handover := gatewayRebindTypedHandoverRuntime{manager: f.manager,
		hostProbe: func(_ context.Context, address string, _ uint16, _, _ string) gatewayV2HostProbeResult {
			return gatewayV2HostProbeResult{Connected: address == "127.0.0.1", Responded: address == "127.0.0.1"}
		}, containerProbe: func(context.Context, string, string, uint16, string, string) bool { return false }}
	proof, err := handover.observeCurrentPredecessor(context.Background(), target)
	if err != nil || proof.Running || proof.Serving != nil || !validSHA256(proof.Digest) || len(runner.effects) != 0 {
		t.Fatalf("stopped ownership proof=%#v effects=%v err=%v", proof, runner.effects, err)
	}
	ordinary := managerGatewayCurrentPhysicalRuntime{manager: f.manager, hostProbe: handover.hostProbe, containerProbe: handover.containerProbe}
	if _, err := ordinary.observe(context.Background(), target); err == nil {
		t.Fatal("ordinary current recovery accepted an occupied shared listener")
	}
	replay, err := handover.observeCurrentPredecessor(context.Background(), target)
	if err != nil || replay.Digest != proof.Digest || len(runner.effects) != 0 {
		t.Fatalf("ownership replay: %v", err)
	}
}

type gatewayRebindCurrentPredecessorDriftRunner struct {
	*gatewayCurrentPhysicalExecutor
	images int
}

func (r *gatewayRebindCurrentPredecessorDriftRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if len(request.Args) >= 2 && request.Args[0] == "image" && request.Args[1] == "inspect" {
		r.images++
		if r.images == 2 {
			r.container.Labels[gatewayV2OperationLabelKey] = "changed-between-reads"
		}
	}
	return r.gatewayCurrentPhysicalExecutor.Run(ctx, request)
}

func TestGatewayRebindCurrentPredecessorRejectsChangedInventory(t *testing.T) {
	for _, name := range []string{"replacement ID", "restart config", "effective binding", "between reads", "running without serving proof"} {
		t.Run(name, func(t *testing.T) {
			f := newGatewayCurrentStateFixture(t)
			f.manager.options.HostPort = f.history.Predecessor.Journal.Source.LocalHostPort
			target := gatewayCurrentPhysicalRuntimeTestTarget(t, f)
			runner := newGatewayCurrentPhysicalExecutor(t, target, target.State, target.State, f.manager.options.HostPort)
			if name != "running without serving proof" {
				runner.stopAt(mustGatewayCurrentPhysicalConfig(t, target.State))
			}
			f.manager.runner = runner
			switch name {
			case "replacement ID":
				runner.container.ID = strings.Repeat("8", 64)
			case "restart config":
				runner.files[target.Identity.Rebind.ActiveConfigFilename] = []byte(`{"tampered":true}`)
			case "effective binding":
				runner.containerRuntime.EffectivePortBindings = cloneGatewayCurrentPhysicalTestPortBindings(runner.container.PortBindings)
			case "between reads":
				f.manager.runner = &gatewayRebindCurrentPredecessorDriftRunner{gatewayCurrentPhysicalExecutor: runner}
			}
			handover := gatewayRebindTypedHandoverRuntime{manager: f.manager,
				hostProbe: func(context.Context, string, uint16, string, string) gatewayV2HostProbeResult {
					return gatewayV2HostProbeResult{}
				},
				containerProbe: func(context.Context, string, string, uint16, string, string) bool { return false }}
			if _, err := handover.observeCurrentPredecessor(context.Background(), target); err == nil || len(runner.effects) != 0 {
				t.Fatalf("changed predecessor was accepted: effects=%v err=%v", runner.effects, err)
			}
		})
	}
}

func TestGatewayRebindRollbackPredecessorRequiresUnoccupiedListeners(t *testing.T) {
	f := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	f.runner.stopStage()
	f.runner.stopPredecessor()
	runtime := f.runtime
	absent := func(ctx context.Context, address string, port uint16) bool {
		if ctx == nil || ctx.Err() != nil || port == 0 {
			return false
		}
		// Prefix 12 has no handover plan yet. Inspect actual effective bindings
		// instead of deriving the loopback port from a future protected record.
		for _, inventory := range []gatewayContainerRuntime{f.runner.predecessor.FinalRuntime,
			f.runner.stage.containerRuntime, f.runner.finalRuntime} {
			for _, bindings := range inventory.EffectivePortBindings {
				for _, binding := range bindings {
					if binding["HostIp"] == address && binding["HostPort"] == strconv.Itoa(int(port)) {
						return false
					}
				}
			}
		}
		return true
	}
	runtime.listenerAbsent = absent
	guard := func(context.Context) error { return nil }
	if predecessor, err := runtime.authorizedRollbackPredecessor(context.Background(), f.attempt, f.current, guard); err != nil || predecessor.Running {
		t.Fatalf("stopped predecessor with absent listeners: %v", err)
	}
	drifted := false
	runtime.listenerAbsent = func(ctx context.Context, address string, port uint16) bool {
		drifted = true
		return absent(ctx, address, port)
	}
	changingGuard := func(context.Context) error {
		if drifted {
			return errors.New("protected authority changed during listener probes")
		}
		return nil
	}
	if _, err := runtime.authorizedRollbackPredecessor(context.Background(), f.attempt, f.current, changingGuard); err == nil || !drifted {
		t.Fatal("listener probes did not recheck changed authority before restart")
	}
	runtime.listenerAbsent = func(ctx context.Context, address string, port uint16) bool {
		if address == "127.0.0.1" {
			return false
		}
		return absent(ctx, address, port)
	}
	if _, err := runtime.authorizedRollbackPredecessor(context.Background(), f.attempt, f.current, guard); err == nil {
		t.Fatal("occupied loopback listener authorized predecessor restart")
	}
	if f.runner.predecessor.FinalContainer.Running || len(f.runner.effects) != 0 {
		t.Fatal("refused authorization restarted or changed a container")
	}
}
