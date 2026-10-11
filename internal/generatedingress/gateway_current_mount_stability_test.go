package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func newGatewayCurrentMountStabilityRuntime(t *testing.T) (*gatewayCurrentPhysicalExecutor,
	gatewayCurrentPhysicalTarget, managerGatewayCurrentPhysicalRuntime,
) {
	t.Helper()
	fixture, transition, target := gatewayCurrentPhysicalExecutorTransition(t)
	localPort := fixture.history.Predecessor.Journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, transition.Effective, transition.Effective, localPort)
	fixture.manager.runner = runner
	fixture.manager.options.HostPort = localPort
	return runner, target, managerGatewayCurrentPhysicalRuntime{manager: fixture.manager,
		hostProbe:      gatewayCurrentPhysicalExecutorHostProbe(localPort),
		containerProbe: func(context.Context, string, string, uint16, string, string) bool { return true }}
}

func gatewayCurrentMountStabilityContainerBytes(t *testing.T, value caddyInspection) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func gatewayCurrentMountStabilityInspectionBytes(t *testing.T, container caddyInspection,
	runtime gatewayContainerRuntime,
) []byte {
	t.Helper()
	encoded, err := json.Marshal(gatewayContainerInspection{caddyInspection: container,
		gatewayContainerRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func gatewayCurrentMountStabilityInspectTarget(args []string, target string) bool {
	return len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" &&
		args[3] == gatewayContainerInspectFormat && args[4] == target
}

func gatewayCurrentMountStabilityMutateResult(t *testing.T, result *runtimeprocess.CommandResult,
	mutate func(*gatewayContainerInspection),
) {
	t.Helper()
	var decoded gatewayContainerInspection
	if err := json.Unmarshal(result.Stdout, &decoded); err != nil {
		t.Fatal(err)
	}
	mutate(&decoded)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	result.Stdout = encoded
}

type gatewayCurrentMountStabilityMutation struct {
	name   string
	mutate func(*gatewayContainerInspection)
}

func gatewayCurrentMountStabilityInvalidMutations() []gatewayCurrentMountStabilityMutation {
	return []gatewayCurrentMountStabilityMutation{
		{name: "added mount", mutate: func(value *gatewayContainerInspection) {
			value.Mounts = append(value.Mounts, mountInspection{Type: "volume", Name: "foreign-volume",
				Destination: "/foreign", RW: true})
		}},
		{name: "removed mount", mutate: func(value *gatewayContainerInspection) {
			value.Mounts = append([]mountInspection(nil), value.Mounts[:1]...)
		}},
		{name: "duplicate mount", mutate: func(value *gatewayContainerInspection) {
			value.Mounts = []mountInspection{value.Mounts[0], value.Mounts[0]}
		}},
		{name: "cross owner mount", mutate: func(value *gatewayContainerInspection) {
			value.Mounts[0].Name = "foreign-owned-volume"
		}},
		{name: "ordered command drift", mutate: func(value *gatewayContainerInspection) {
			value.Cmd[0], value.Cmd[1] = value.Cmd[1], value.Cmd[0]
		}},
	}
}

func TestGatewayCurrentPhysicalObserveAcceptsOnlyOwnedMountOrderChange(t *testing.T) {
	runner, target, runtimeDriver := newGatewayCurrentMountStabilityRuntime(t)
	finalName := target.Identity.Rebind.FinalContainer
	original := gatewayCurrentMountStabilityContainerBytes(t, runner.container)
	originalMounts := append([]mountInspection(nil), runner.container.Mounts...)
	interceptor := newGatewayRebindTypedMountOrderRunner(runner, map[string]map[int]bool{
		finalName: {2: true},
	})
	runtimeDriver.manager.runner = interceptor

	proof, err := runtimeDriver.observe(context.Background(), target)
	if interceptor.counts[finalName] != 2 || len(interceptor.emitted[finalName]) != 2 {
		t.Fatalf("current observation inspected final %d times and emitted %d values, want 2 and 2",
			interceptor.counts[finalName], len(interceptor.emitted[finalName]))
	}
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[finalName], originalMounts,
		map[int]bool{2: true})
	if !bytes.Equal(gatewayCurrentMountStabilityContainerBytes(t, runner.container), original) {
		t.Fatal("current observation mount permutation mutated the retained executor container")
	}
	if len(runner.effects) != 0 {
		t.Fatalf("current observation mount permutation reached effects: %v", runner.effects)
	}
	if err != nil {
		t.Fatalf("current observation refused an exact owned-mount order change after two complete reads: %v", err)
	}
	if !gatewayCurrentServingRestoreProofMatches(proof, target, gatewayCurrentPhysicalRecoveryEffective) {
		t.Fatalf("current observation returned invalid effective-recovery proof: %#v", proof)
	}
}

func TestGatewayCurrentTerminalOwnedInventoryAcceptsOnlyNamedIDMountOrderChange(t *testing.T) {
	runner, target, runtimeDriver := newGatewayCurrentMountStabilityRuntime(t)
	finalName := target.Identity.Rebind.FinalContainer
	finalID := target.Resources.FinalContainer.ID
	original := gatewayCurrentMountStabilityContainerBytes(t, runner.container)
	originalMounts := append([]mountInspection(nil), runner.container.Mounts...)
	interceptor := newGatewayRebindTypedMountOrderRunner(runner, map[string]map[int]bool{
		finalName: {},
		finalID:   {1: true},
	})
	runtimeDriver.manager.runner = interceptor
	facts := gatewayCurrentFinalOwnershipFacts(target, runtimeDriver.manager.options.HostPort)

	container, containerRuntime, present, err := runtimeDriver.terminalOwnedOwnershipInventory(context.Background(), facts)
	if interceptor.counts[finalName] != 1 || interceptor.counts[finalID] != 1 {
		t.Fatalf("terminal ownership inspected named/id %d/%d times, want 1/1",
			interceptor.counts[finalName], interceptor.counts[finalID])
	}
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[finalName], originalMounts, map[int]bool{})
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[finalID], originalMounts, map[int]bool{1: true})
	if !bytes.Equal(gatewayCurrentMountStabilityContainerBytes(t, runner.container), original) {
		t.Fatal("terminal named/id mount permutation mutated the retained executor container")
	}
	if len(runner.effects) != 0 {
		t.Fatalf("terminal named/id mount permutation reached effects: %v", runner.effects)
	}
	if err != nil {
		t.Fatalf("terminal ownership refused exact named/id observations differing only by owned-mount order: %v", err)
	}
	if !present || !gatewayFinalOwnershipContainerMatches(facts, container, containerRuntime) ||
		!reflect.DeepEqual(containerRuntime, runner.containerRuntime) {
		t.Fatalf("terminal ownership returned invalid exact owner: present=%t container=%#v runtime=%#v",
			present, container, containerRuntime)
	}
}

func TestGatewayCurrentPhysicalObserveRejectsInvalidMountsAndOrderedCommandDrift(t *testing.T) {
	for _, test := range gatewayCurrentMountStabilityInvalidMutations() {
		t.Run(test.name, func(t *testing.T) {
			runner, target, runtimeDriver := newGatewayCurrentMountStabilityRuntime(t)
			finalName := target.Identity.Rebind.FinalContainer
			original := gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)
			reads := 0
			hook := &gatewayCurrentTerminalStopHookExecutor{gatewayCurrentPhysicalExecutor: runner,
				resultHook: func(args []string, result *runtimeprocess.CommandResult) {
					if !gatewayCurrentMountStabilityInspectTarget(args, finalName) {
						return
					}
					reads++
					if reads == 2 {
						gatewayCurrentMountStabilityMutateResult(t, result, test.mutate)
					}
				}}
			runtimeDriver.manager.runner = hook

			if proof, err := runtimeDriver.observe(context.Background(), target); err == nil ||
				!reflect.DeepEqual(proof, gatewayCurrentPhysicalAttestation{}) || reads != 2 || len(runner.effects) != 0 {
				t.Fatalf("invalid second current observation accepted: reads=%d effects=%v proof=%#v error=%v",
					reads, runner.effects, proof, err)
			}
			if !bytes.Equal(gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime), original) {
				t.Fatal("invalid current observation mutation changed retained executor input")
			}
		})
	}
}

func TestGatewayCurrentTerminalOwnedInventoryRejectsInvalidNamedOrIDObservation(t *testing.T) {
	for _, side := range []string{"named", "id"} {
		for _, test := range gatewayCurrentMountStabilityInvalidMutations() {
			t.Run(side+"_"+strings.ReplaceAll(test.name, " ", "_"), func(t *testing.T) {
				runner, target, runtimeDriver := newGatewayCurrentMountStabilityRuntime(t)
				finalName, finalID := target.Identity.Rebind.FinalContainer, target.Resources.FinalContainer.ID
				targetName := finalName
				if side == "id" {
					targetName = finalID
				}
				original := gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)
				counts := map[string]int{}
				hook := &gatewayCurrentTerminalStopHookExecutor{gatewayCurrentPhysicalExecutor: runner,
					resultHook: func(args []string, result *runtimeprocess.CommandResult) {
						for _, name := range []string{finalName, finalID} {
							if gatewayCurrentMountStabilityInspectTarget(args, name) {
								counts[name]++
								if name == targetName {
									gatewayCurrentMountStabilityMutateResult(t, result, test.mutate)
								}
							}
						}
					}}
				runtimeDriver.manager.runner = hook
				facts := gatewayCurrentFinalOwnershipFacts(target, runtimeDriver.manager.options.HostPort)

				container, containerRuntime, present, err := runtimeDriver.terminalOwnedOwnershipInventory(
					context.Background(), facts)
				if err == nil || present || !reflect.DeepEqual(container, caddyInspection{}) ||
					!reflect.DeepEqual(containerRuntime, gatewayContainerRuntime{}) || counts[finalName] != 1 ||
					counts[finalID] != 1 || len(runner.effects) != 0 {
					t.Fatalf("invalid %s observation accepted: named/id=%d/%d present=%t effects=%v error=%v",
						side, counts[finalName], counts[finalID], present, runner.effects, err)
				}
				if !bytes.Equal(gatewayCurrentMountStabilityInspectionBytes(t, runner.container,
					runner.containerRuntime), original) {
					t.Fatal("invalid terminal observation mutation changed retained executor input")
				}
			})
		}
	}
}

func TestGatewayCurrentTerminalOwnedInventoryRejectsPresenceMismatch(t *testing.T) {
	for _, test := range []struct {
		name           string
		initialPresent bool
		presentAfter   bool
	}{
		{name: "named present id absent", initialPresent: true, presentAfter: false},
		{name: "named absent id present", initialPresent: false, presentAfter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, target, runtimeDriver := newGatewayCurrentMountStabilityRuntime(t)
			finalName, finalID := target.Identity.Rebind.FinalContainer, target.Resources.FinalContainer.ID
			runner.containerPresent = test.initialPresent
			counts := map[string]int{}
			hook := &gatewayCurrentTerminalStopHookExecutor{gatewayCurrentPhysicalExecutor: runner,
				hook: func(args []string) {
					for _, name := range []string{finalName, finalID} {
						if gatewayCurrentMountStabilityInspectTarget(args, name) {
							counts[name]++
						}
					}
					if gatewayCurrentMountStabilityInspectTarget(args, finalName) {
						runner.containerPresent = test.presentAfter
					}
				}}
			runtimeDriver.manager.runner = hook
			facts := gatewayCurrentFinalOwnershipFacts(target, runtimeDriver.manager.options.HostPort)

			if _, _, _, err := runtimeDriver.terminalOwnedOwnershipInventory(context.Background(), facts); err == nil ||
				counts[finalName] != 1 || counts[finalID] != 1 || len(runner.effects) != 0 {
				t.Fatalf("terminal named/id presence mismatch accepted: named/id=%d/%d effects=%v error=%v",
					counts[finalName], counts[finalID], runner.effects, err)
			}
		})
	}
}

func TestGatewayCurrentTerminalOwnedInventoryRetainsStrongerLivenessValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*gatewayCurrentPhysicalExecutor)
	}{
		{name: "restarting", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.container.Restarting = true
		}},
		{name: "paused", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.containerRuntime.Paused = true
		}},
		{name: "dead", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.containerRuntime.Dead = true
		}},
		{name: "running without effective bindings", mutate: func(runner *gatewayCurrentPhysicalExecutor) {
			runner.containerRuntime.EffectivePortBindings = map[string][]map[string]string{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, target, runtimeDriver := newGatewayCurrentMountStabilityRuntime(t)
			test.mutate(runner)
			facts := gatewayCurrentFinalOwnershipFacts(target, runtimeDriver.manager.options.HostPort)
			if _, present, err := runtimeDriver.terminalOwnedInventory(context.Background(), facts); err == nil ||
				present || len(runner.effects) != 0 {
				t.Fatalf("terminal liveness mismatch accepted: present=%t effects=%v error=%v",
					present, runner.effects, err)
			}
		})
	}
}

func TestGatewayRebindCurrentAttestationAcceptsOwnedMountOrderChangesAfterDatabaseCommit(t *testing.T) {
	f, _, selection, _ := gatewayRebindCommittedServingFixture(t)
	ctx := context.Background()
	beforeCommitBarrier := f.manager.gatewayRebindCommitBarrierLatch().Load()
	beforeFailStop := f.manager.gatewayRebindFailStopLatch().Load()
	beforeSQL, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || beforeSQL.Rebind.Phase != appaccess.GatewayRebindDatabaseCommitted ||
		beforeSQL.Rebind.Active == nil || f.repository.CheckGatewayRebindFence(ctx) == nil || beforeCommitBarrier || beforeFailStop {
		t.Fatalf("fixture is not an active database-committed rebind: snapshot=%#v error=%v", beforeSQL, err)
	}
	beforeHistory, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, *selection.State, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	port := f.journal.Source.LocalHostPort
	runner := newGatewayCurrentPhysicalExecutor(t, target, *selection.State, *selection.State, port)
	original := gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime)
	physical := managedGatewayCurrentServingExecutorDriver(gatewayCurrentStateFixture{manager: f.manager}, runner, port)
	finalName := target.Identity.Rebind.FinalContainer
	interceptor := newGatewayRebindTypedMountOrderRunner(runner, map[string]map[int]bool{
		finalName: {2: true, 4: true},
	})
	f.manager.runner = interceptor
	f.manager.gatewayCurrentPhysicalDriver = physical

	digest, err := f.manager.attestGatewayRebindCurrentLocked(ctx, selection)
	if err != nil || !validSHA256(digest) || interceptor.counts[finalName] != 4 ||
		len(interceptor.emitted[finalName]) != 4 || len(runner.effects) != 0 {
		t.Fatalf("database-committed mount-order attestation: reads=%d effects=%v digest=%q error=%v",
			interceptor.counts[finalName], runner.effects, digest, err)
	}
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[finalName], runner.container.Mounts,
		map[int]bool{2: true, 4: true})
	if !bytes.Equal(gatewayCurrentMountStabilityInspectionBytes(t, runner.container, runner.containerRuntime), original) {
		t.Fatal("database-committed attestation mutated retained executor input")
	}
	afterSQL, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(beforeSQL, afterSQL) || f.repository.CheckGatewayRebindFence(ctx) == nil ||
		f.manager.gatewayRebindCommitBarrierLatch().Load() != beforeCommitBarrier ||
		f.manager.gatewayRebindFailStopLatch().Load() != beforeFailStop {
		t.Fatalf("database-committed attestation changed SQL, fence, or latches: error=%v", err)
	}
	gatewayRebindSequenceRequireRetainedFiles(t, f.manager, beforeHistory)
}
