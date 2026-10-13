package generatedingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// gatewayCurrentNativeInventoryMutationRunner changes only one decoded
// container-inspect response. The composed runner retains the authoritative
// Docker fixture so a refusal must not alter its resources or effects.
type gatewayCurrentNativeInventoryMutationRunner struct {
	delegate runtimeprocess.CommandRunner
	target   string
	mutate   func(*gatewayContainerInspection)
}

func (r gatewayCurrentNativeInventoryMutationRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	result, err := r.delegate.Run(ctx, request)
	if err != nil || !gatewayCurrentMountStabilityInspectTarget(request.Args, r.target) || len(result.Stdout) == 0 {
		return result, err
	}
	var observation gatewayContainerInspection
	if err := json.Unmarshal(result.Stdout, &observation); err != nil {
		return runtimeprocess.CommandResult{}, fmt.Errorf("decode native predecessor inspection: %w", err)
	}
	r.mutate(&observation)
	encoded, err := json.Marshal(observation)
	if err != nil {
		return runtimeprocess.CommandResult{}, fmt.Errorf("encode native predecessor inspection: %w", err)
	}
	result.Stdout = encoded
	return result, nil
}

type gatewayCurrentNativeInventoryMountFixture struct {
	predecessor gatewayRebindPredecessorFixture
	target      gatewayCurrentNativePredecessorRetirement
	physical    managerGatewayCurrentPhysicalRuntime
	runner      *gatewayRebindCompositionRunner

	beforeSQL     any
	beforeHistory gatewayRebindProtectedIntentHistory
	beforeFiles   gatewayHistorySnapshot
	beforeEffects int
	beforeDocker  []byte
}

func newGatewayCurrentNativeInventoryMountFixture(t *testing.T) gatewayCurrentNativeInventoryMountFixture {
	t.Helper()
	predecessor, input, driver := newGatewayRebindCompositionFixture(t)
	ctx := context.Background()
	result, err := predecessor.manager.commitGatewayRebindWithDriver(ctx, predecessor.repository, input, driver)
	if err != nil || result.FinalPhase != appaccess.GatewayRebindCommitted || !result.FenceReleased {
		t.Fatalf("commit native predecessor fixture: result=%#v error=%v", result, err)
	}
	snapshot, err := predecessor.repository.GatewayRebindRecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := predecessor.manager.selectGatewayCurrentLocked(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	history, err := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	action, err := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil {
		t.Fatal(err)
	}
	managed, ok := predecessor.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	if !ok {
		t.Fatalf("native predecessor driver=%T", predecessor.manager.gatewayCurrentPhysicalDriver)
	}
	physical, ok := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	if !ok {
		t.Fatalf("native predecessor runtime=%T", managed.runtime)
	}
	beforeSQL, err := predecessor.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeHistory, err := predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(predecessor.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayCurrentNativeInventoryMountFixture{
		predecessor:   predecessor,
		target:        action.Native,
		physical:      physical,
		runner:        driver.runner,
		beforeSQL:     beforeSQL,
		beforeHistory: beforeHistory,
		beforeFiles:   beforeFiles,
		beforeEffects: len(driver.runner.effects),
		beforeDocker: gatewayCurrentMountStabilityInspectionBytes(t,
			driver.runner.predecessor.FinalContainer, driver.runner.predecessor.FinalRuntime),
	}
}

func (f gatewayCurrentNativeInventoryMountFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	afterSQL, err := f.predecessor.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterHistory, err := f.predecessor.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	afterFiles, err := readGatewayHistorySnapshotMode(f.predecessor.manager.store, true)
	if err != nil || !reflect.DeepEqual(f.beforeSQL, afterSQL) ||
		!sameGatewayRebindCurrentHistory(f.beforeHistory, afterHistory) ||
		!sameGatewayHistorySnapshot(f.beforeFiles, afterFiles) {
		t.Fatalf("native predecessor inventory changed durable authority: sql=%v history=%v files=%v",
			err, sameGatewayRebindCurrentHistory(f.beforeHistory, afterHistory),
			sameGatewayHistorySnapshot(f.beforeFiles, afterFiles))
	}
	if len(f.runner.effects) != f.beforeEffects {
		t.Fatalf("native predecessor inventory reached effects: %v", f.runner.effects[f.beforeEffects:])
	}
	if !bytes.Equal(gatewayCurrentMountStabilityInspectionBytes(t,
		f.runner.predecessor.FinalContainer, f.runner.predecessor.FinalRuntime), f.beforeDocker) {
		t.Fatal("native predecessor inventory mutated the retained composed Docker fixture")
	}
}

func TestGatewayCurrentPredecessorNativeOwnershipInventoryAcceptsOnlyNamedIDMountOrderChange(t *testing.T) {
	fixture := newGatewayCurrentNativeInventoryMountFixture(t)
	name, id := fixture.target.State.Identity.FinalContainer, fixture.target.Journal.Resources.FinalContainerID
	originalMounts := append([]mountInspection(nil), fixture.runner.predecessor.FinalContainer.Mounts...)
	if len(originalMounts) != 2 {
		t.Fatalf("native predecessor fixture mounts=%#v, want exact owned pair", originalMounts)
	}
	interceptor := newGatewayRebindTypedMountOrderRunner(fixture.runner, map[string]map[int]bool{
		name: {},
		id:   {1: true},
	})
	fixture.predecessor.manager.runner = interceptor

	container, runtime, present, err := fixture.physical.nativePredecessorOwnershipInventory(context.Background(), fixture.target)
	fixture.assertUnchanged(t)
	if interceptor.counts[name] != 1 || interceptor.counts[id] != 1 || len(interceptor.emitted[name]) != 1 ||
		len(interceptor.emitted[id]) != 1 {
		t.Fatalf("native ownership named/ID reads=%d/%d emitted=%d/%d, want 1/1",
			interceptor.counts[name], interceptor.counts[id], len(interceptor.emitted[name]), len(interceptor.emitted[id]))
	}
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[name], originalMounts, map[int]bool{})
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[id], originalMounts, map[int]bool{1: true})
	if err != nil {
		t.Fatalf("native ownership refused exact named/ID observations differing only by owned-mount order: %v", err)
	}
	if !present || normalizeID(container.ID) != fixture.target.Journal.Resources.FinalContainerID ||
		!reflect.DeepEqual(runtime, fixture.runner.predecessor.FinalRuntime) {
		t.Fatalf("native ownership returned invalid exact owner: present=%t container=%#v runtime=%#v",
			present, container, runtime)
	}
}

func TestGatewayCurrentPredecessorNativeOwnershipInventoryRefusesInvalidNamedOrIDObservation(t *testing.T) {
	fixture := newGatewayCurrentNativeInventoryMountFixture(t)
	for _, side := range []string{"named", "id"} {
		for _, test := range []gatewayCurrentNativeInventoryMutation{
			{name: "changed mount", mutate: func(value *gatewayContainerInspection) {
				value.Mounts[0].Destination = "/unexpected"
			}},
			{name: "missing mount", mutate: func(value *gatewayContainerInspection) {
				value.Mounts = append([]mountInspection(nil), value.Mounts[:1]...)
			}},
			{name: "duplicate mount", mutate: func(value *gatewayContainerInspection) {
				value.Mounts = []mountInspection{value.Mounts[0], value.Mounts[0]}
			}},
			{name: "foreign mount", mutate: func(value *gatewayContainerInspection) {
				value.Mounts[0].Name = "foreign-owned-volume"
			}},
			{name: "ordered command drift", mutate: func(value *gatewayContainerInspection) {
				value.Cmd[0], value.Cmd[1] = value.Cmd[1], value.Cmd[0]
			}},
		} {
			t.Run(side+"_"+strings.ReplaceAll(test.name, " ", "_"), func(t *testing.T) {
				target := fixture.target.State.Identity.FinalContainer
				if side == "id" {
					target = fixture.target.Journal.Resources.FinalContainerID
				}
				fixture.predecessor.manager.runner = gatewayCurrentNativeInventoryMutationRunner{
					delegate: fixture.runner, target: target, mutate: test.mutate,
				}

				container, runtime, present, err := fixture.physical.nativePredecessorOwnershipInventory(context.Background(), fixture.target)
				fixture.assertUnchanged(t)
				if err == nil || present || !reflect.DeepEqual(container, caddyInspection{}) ||
					!reflect.DeepEqual(runtime, gatewayContainerRuntime{}) {
					t.Fatalf("invalid %s %s observation accepted: present=%t container=%#v runtime=%#v error=%v",
						side, test.name, present, container, runtime, err)
				}
			})
		}
	}
}

type gatewayCurrentNativeInventoryMutation struct {
	name   string
	mutate func(*gatewayContainerInspection)
}

func TestGatewayCurrentPredecessorNativeOwnershipInventoryRetainsNamedIDIdentityAndRuntimeEquality(t *testing.T) {
	fixture := newGatewayCurrentNativeInventoryMountFixture(t)
	for _, test := range []struct {
		name   string
		side   string
		mutate func(*gatewayContainerInspection)
	}{
		{name: "named identity", side: "named", mutate: func(value *gatewayContainerInspection) {
			value.ID = strings.Repeat("a", 64)
		}},
		{name: "immutable ID identity", side: "id", mutate: func(value *gatewayContainerInspection) {
			value.ID = strings.Repeat("b", 64)
		}},
		{name: "named runtime", side: "named", mutate: func(value *gatewayContainerInspection) {
			value.Paused = !value.Paused
		}},
		{name: "immutable ID runtime", side: "id", mutate: func(value *gatewayContainerInspection) {
			value.Dead = !value.Dead
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := fixture.target.State.Identity.FinalContainer
			if test.side == "id" {
				target = fixture.target.Journal.Resources.FinalContainerID
			}
			fixture.predecessor.manager.runner = gatewayCurrentNativeInventoryMutationRunner{
				delegate: fixture.runner, target: target, mutate: test.mutate,
			}

			container, runtime, present, err := fixture.physical.nativePredecessorOwnershipInventory(context.Background(), fixture.target)
			fixture.assertUnchanged(t)
			if err == nil || present || !reflect.DeepEqual(container, caddyInspection{}) ||
				!reflect.DeepEqual(runtime, gatewayContainerRuntime{}) {
				t.Fatalf("native ownership accepted %s drift: present=%t container=%#v runtime=%#v error=%v",
					test.name, present, container, runtime, err)
			}
		})
	}
}

var _ runtimeprocess.CommandRunner = gatewayCurrentNativeInventoryMutationRunner{}
