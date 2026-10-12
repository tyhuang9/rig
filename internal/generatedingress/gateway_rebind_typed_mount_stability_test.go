package generatedingress

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// gatewayRebindTypedMountOrderRunner delegates to the concrete typed runtime
// runners, then independently decodes each container inspection before
// applying the requested Docker mount order. It never mutates the delegate's
// retained container or mount slices.
type gatewayRebindTypedMountOrderRunner struct {
	delegate runtimeprocess.CommandRunner
	reverse  map[string]map[int]bool
	counts   map[string]int
	emitted  map[string][][]mountInspection
}

func (r *gatewayRebindTypedMountOrderRunner) Run(ctx context.Context,
	request runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	result, err := r.delegate.Run(ctx, request)
	if err != nil || len(request.Args) != 5 || request.Args[0] != "container" || request.Args[1] != "inspect" ||
		request.Args[2] != "--format" || request.Args[3] != gatewayContainerInspectFormat || len(result.Stdout) == 0 {
		return result, err
	}
	name := request.Args[len(request.Args)-1]
	reversed, targeted := r.reverse[name]
	if !targeted {
		return result, nil
	}
	r.counts[name]++
	var decoded map[string]json.RawMessage
	if decodeErr := json.Unmarshal(result.Stdout, &decoded); decodeErr != nil {
		return runtimeprocess.CommandResult{}, fmt.Errorf("decode delegated container inspection: %w", decodeErr)
	}
	var mounts []mountInspection
	if decodeErr := json.Unmarshal(decoded["mounts"], &mounts); decodeErr != nil {
		return runtimeprocess.CommandResult{}, fmt.Errorf("decode delegated container mounts: %w", decodeErr)
	}
	mounts = append([]mountInspection(nil), mounts...)
	if !reversed[r.counts[name]] {
		r.emitted[name] = append(r.emitted[name], mounts)
		return result, nil
	}
	if len(mounts) == 2 {
		mounts[0], mounts[1] = mounts[1], mounts[0]
	}
	r.emitted[name] = append(r.emitted[name], append([]mountInspection(nil), mounts...))
	encodedMounts, encodeErr := json.Marshal(mounts)
	if encodeErr != nil {
		return runtimeprocess.CommandResult{}, fmt.Errorf("encode delegated container mounts: %w", encodeErr)
	}
	decoded["mounts"] = encodedMounts
	encoded, encodeErr := json.Marshal(decoded)
	if encodeErr != nil {
		return runtimeprocess.CommandResult{}, fmt.Errorf("encode delegated container inspection: %w", encodeErr)
	}
	result.Stdout = encoded
	return result, nil
}

func newGatewayRebindTypedMountOrderRunner(delegate runtimeprocess.CommandRunner,
	reverse map[string]map[int]bool,
) *gatewayRebindTypedMountOrderRunner {
	return &gatewayRebindTypedMountOrderRunner{delegate: delegate, reverse: reverse,
		counts: map[string]int{}, emitted: map[string][][]mountInspection{}}
}

func assertGatewayRebindTypedMountOrders(t *testing.T, emitted [][]mountInspection,
	original []mountInspection, reversed map[int]bool,
) {
	t.Helper()
	if len(original) != 2 {
		t.Fatalf("fixture retained %d mounts, want 2", len(original))
	}
	for index, mounts := range emitted {
		wantFirst := original[0].Name
		if reversed[index+1] {
			wantFirst = original[1].Name
		}
		if len(mounts) != 2 || mounts[0].Name != wantFirst {
			t.Fatalf("container inspect %d mount order=%#v", index+1, mounts)
		}
	}
}

func cloneGatewayRebindTypedMountStabilityContainer(t *testing.T, value caddyInspection) caddyInspection {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result caddyInspection
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func cloneGatewayRebindTypedMountStabilityPhysical(t *testing.T,
	value gatewayRebindTypedHandoverPhysical,
) gatewayRebindTypedHandoverPhysical {
	t.Helper()
	result := value
	result.Stage.StageContainer = cloneGatewayRebindTypedMountStabilityContainer(t, value.Stage.StageContainer)
	result.Final = cloneGatewayRebindTypedMountStabilityContainer(t, value.Final)
	if value.Predecessor.PhysicalUpgrade != nil {
		physicalUpgrade := *value.Predecessor.PhysicalUpgrade
		physicalUpgrade.FinalContainer = cloneGatewayRebindTypedMountStabilityContainer(t,
			value.Predecessor.PhysicalUpgrade.FinalContainer)
		result.Predecessor.PhysicalUpgrade = &physicalUpgrade
	}
	return result
}

func TestGatewayRebindTypedStageCopyStageConfigAcceptsOnlyOwnedMountOrderChange(t *testing.T) {
	fixture, copyRunner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
	defer fixture.close(t)
	stageName := fixture.intent.Identity.StageContainer
	original := append([]mountInspection(nil), fixture.runner.container.Mounts...)
	interceptor := newGatewayRebindTypedMountOrderRunner(copyRunner, map[string]map[int]bool{
		stageName: {2: true, 4: true},
	})
	fixture.runtime.manager.runner = interceptor

	err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect,
		func(context.Context) error { return nil })
	if err != nil {
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "stable", "stable_equality")
		if interceptor.counts[stageName] != 2 || copyRunner.stageCopies != 0 || copyRunner.archiveCalls != 0 ||
			len(fixture.runner.effects) != 0 {
			t.Fatalf("mount-order refusal happened outside P7 stable equality: reads=%d copies=%d archives=%d effects=%#v",
				interceptor.counts[stageName], copyRunner.stageCopies, copyRunner.archiveCalls, fixture.runner.effects)
		}
		t.Fatalf("P7 stable equality refused exact owned mounts in reverse Docker order: %v", err)
	}
	if copyRunner.stageCopies != 1 || copyRunner.archiveCalls != 2 || len(fixture.runner.effects) != 0 {
		t.Fatalf("mount-order-only copy effects=%#v copies=%d archives=%d",
			fixture.runner.effects, copyRunner.stageCopies, copyRunner.archiveCalls)
	}
	if interceptor.counts[stageName] != 4 || len(interceptor.emitted[stageName]) != 4 {
		t.Fatalf("stage inspect count=%d emitted=%d", interceptor.counts[stageName], len(interceptor.emitted[stageName]))
	}
	assertGatewayRebindTypedMountOrders(t, interceptor.emitted[stageName], original, interceptor.reverse[stageName])
	if !reflect.DeepEqual(fixture.runner.container.Mounts, original) {
		t.Fatalf("interceptor mutated retained stage mounts: got=%#v want=%#v",
			fixture.runner.container.Mounts, original)
	}
}

func TestGatewayRebindTypedStageCopyStageConfigRejectsMountAndCommandDrift(t *testing.T) {
	fixture, copyRunner, effect := newGatewayRebindTypedStageCopyDiagnosticFixture(t)
	defer fixture.close(t)
	original := cloneGatewayRebindTypedMountStabilityContainer(t, fixture.runner.container)
	if len(original.Mounts) != 2 || len(original.Cmd) < 2 {
		t.Fatalf("invalid stage drift fixture: mounts=%#v cmd=%#v", original.Mounts, original.Cmd)
	}

	for _, test := range []struct {
		name string
		kind string
	}{
		{name: "changed mount", kind: "changed"},
		{name: "added mount", kind: "added"},
		{name: "removed mount", kind: "removed"},
		{name: "duplicate mount", kind: "duplicate"},
		{name: "command order", kind: "command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneGatewayRebindTypedMountStabilityContainer(t, original)
			candidate.Mounts[0], candidate.Mounts[1] = candidate.Mounts[1], candidate.Mounts[0]
			switch test.kind {
			case "changed":
				candidate.Mounts[0].Destination = "/unexpected"
			case "added":
				candidate.Mounts = append(candidate.Mounts, mountInspection{
					Type: "volume", Name: "unexpected", Destination: "/unexpected", RW: true,
				})
			case "removed":
				candidate.Mounts = candidate.Mounts[:1]
			case "duplicate":
				candidate.Mounts[1] = candidate.Mounts[0]
			case "command":
				candidate.Cmd[0], candidate.Cmd[1] = candidate.Cmd[1], candidate.Cmd[0]
			default:
				t.Fatalf("unknown drift kind %q", test.kind)
			}
			retained := cloneGatewayRebindTypedMountStabilityContainer(t, candidate)
			fixture.runner.container = candidate
			copyRunner.events = nil
			copyRunner.archiveCalls = 0
			copyRunner.stageCopies = 0
			fixture.runner.effects = nil

			err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect,
				func(context.Context) error { return nil })
			if err == nil {
				t.Fatal("stage drift reached copy without refusal")
			}
			if copyRunner.stageCopies != 0 || copyRunner.archiveCalls != 0 || len(fixture.runner.effects) != 0 {
				t.Fatalf("stage drift crossed effect boundary: copies=%d archives=%d effects=%#v",
					copyRunner.stageCopies, copyRunner.archiveCalls, fixture.runner.effects)
			}
			if !reflect.DeepEqual(fixture.runner.container, retained) {
				t.Fatalf("drift refusal mutated retained container: got=%#v want=%#v",
					fixture.runner.container, retained)
			}
		})
	}
	fixture.runner.container = original
}

func TestGatewayRebindTypedHandoverPhysicalComparatorPreservesExactFields(t *testing.T) {
	fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
	effect := fixture.effectAt(12)
	physical, err := fixture.runtime.readResources(context.Background(), fixture.attempt, effect, true, false)
	if err != nil {
		t.Fatal(err)
	}
	physical.Final = cloneGatewayRebindTypedMountStabilityContainer(t, physical.Stage.StageContainer)
	physical.FinalFound = true
	if physical.Predecessor.PhysicalUpgrade == nil ||
		!physical.Predecessor.PhysicalUpgrade.FinalContainerFound ||
		physical.Predecessor.Selection.Upgrade == nil || len(physical.Final.Cmd) < 2 ||
		physical.Final.Cmd[0] == physical.Final.Cmd[1] ||
		!validGatewayRebindStageContainerMounts(physical.Stage.StageContainer.Mounts,
			fixture.attempt.Intent.Identity) ||
		!validGatewayRebindStageContainerMounts(physical.Final.Mounts, fixture.attempt.Intent.Identity) ||
		!validGatewayV2Mounts(physical.Predecessor.PhysicalUpgrade.FinalContainer.Mounts,
			physical.Predecessor.Selection.Upgrade.State.Identity) {
		t.Fatal("physical comparator fixture lacks exact owned mounts")
	}

	left := cloneGatewayRebindTypedMountStabilityPhysical(t, physical)
	right := cloneGatewayRebindTypedMountStabilityPhysical(t, physical)
	right.Stage.StageContainer.Mounts[0], right.Stage.StageContainer.Mounts[1] =
		right.Stage.StageContainer.Mounts[1], right.Stage.StageContainer.Mounts[0]
	right.Final.Mounts[0], right.Final.Mounts[1] = right.Final.Mounts[1], right.Final.Mounts[0]
	right.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[0],
		right.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[1] =
		right.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[1],
		right.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[0]
	leftBefore := cloneGatewayRebindTypedMountStabilityPhysical(t, left)
	rightBefore := cloneGatewayRebindTypedMountStabilityPhysical(t, right)
	leftUpgrade := left.Predecessor.PhysicalUpgrade
	rightUpgrade := right.Predecessor.PhysicalUpgrade
	if !sameGatewayRebindTypedHandoverPhysical(fixture.attempt.Intent, left, right) {
		t.Fatal("physical comparator refused exact owned mount order changes")
	}
	canonical, ok := canonicalGatewayRebindTypedHandoverPhysical(fixture.attempt.Intent, right)
	if !ok || canonical.Predecessor.PhysicalUpgrade == rightUpgrade ||
		&canonical.Stage.StageContainer.Mounts[0] == &right.Stage.StageContainer.Mounts[0] ||
		&canonical.Final.Mounts[0] == &right.Final.Mounts[0] ||
		&canonical.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[0] ==
			&right.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[0] {
		t.Fatal("physical comparator did not clone every normalized mount input")
	}
	if left.Predecessor.PhysicalUpgrade != leftUpgrade || right.Predecessor.PhysicalUpgrade != rightUpgrade ||
		!reflect.DeepEqual(left, leftBefore) || !reflect.DeepEqual(right, rightBefore) {
		t.Fatal("physical comparator mutated a caller-owned observation")
	}

	for _, test := range []struct {
		name string
		kind string
	}{
		{name: "changed successor final mount", kind: "final_changed"},
		{name: "duplicate successor final mount", kind: "final_duplicate"},
		{name: "changed predecessor final mount", kind: "predecessor_changed"},
		{name: "duplicate predecessor final mount", kind: "predecessor_duplicate"},
	} {
		candidate := cloneGatewayRebindTypedMountStabilityPhysical(t, physical)
		switch test.kind {
		case "final_changed":
			candidate.Final.Mounts[0].Name = "unexpected"
		case "final_duplicate":
			candidate.Final.Mounts[1] = candidate.Final.Mounts[0]
		case "predecessor_changed":
			candidate.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[0].Name = "unexpected"
		case "predecessor_duplicate":
			candidate.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[1] =
				candidate.Predecessor.PhysicalUpgrade.FinalContainer.Mounts[0]
		}
		before := cloneGatewayRebindTypedMountStabilityPhysical(t, candidate)
		if _, accepted := canonicalGatewayRebindTypedHandoverPhysical(fixture.attempt.Intent, candidate); accepted {
			t.Fatalf("%s was accepted", test.name)
		}
		if !reflect.DeepEqual(candidate, before) {
			t.Fatalf("%s refusal mutated its input", test.name)
		}
	}

	for _, test := range []struct {
		name string
		kind string
	}{
		{name: "stage presence", kind: "stage_presence"},
		{name: "final presence", kind: "final_presence"},
		{name: "predecessor presence", kind: "predecessor_presence"},
		{name: "absent stage raw mounts", kind: "absent_stage_mounts"},
		{name: "absent final raw mounts", kind: "absent_final_mounts"},
		{name: "unrelated command order", kind: "command"},
	} {
		first := cloneGatewayRebindTypedMountStabilityPhysical(t, physical)
		second := cloneGatewayRebindTypedMountStabilityPhysical(t, physical)
		switch test.kind {
		case "stage_presence":
			second.Stage.StageContainerFound = false
		case "final_presence":
			second.FinalFound = false
		case "predecessor_presence":
			second.Predecessor.PhysicalUpgrade = nil
		case "absent_stage_mounts":
			first.Stage.StageContainerFound = false
			second.Stage.StageContainerFound = false
			second.Stage.StageContainer.Mounts[0], second.Stage.StageContainer.Mounts[1] =
				second.Stage.StageContainer.Mounts[1], second.Stage.StageContainer.Mounts[0]
		case "absent_final_mounts":
			first.FinalFound = false
			second.FinalFound = false
			second.Final.Mounts[0], second.Final.Mounts[1] = second.Final.Mounts[1], second.Final.Mounts[0]
		case "command":
			second.Final.Cmd[0], second.Final.Cmd[1] = second.Final.Cmd[1], second.Final.Cmd[0]
		}
		if sameGatewayRebindTypedHandoverPhysical(fixture.attempt.Intent, first, second) {
			t.Fatalf("physical comparator ignored %s", test.name)
		}
	}
}

func TestGatewayRebindTypedPrepareHandoverAcceptsOnlyOwnedMountOrderChanges(t *testing.T) {
	for _, test := range []struct {
		name               string
		reverseStage       map[int]bool
		reversePredecessor map[int]bool
		refusalStageReads  int
		refusalNestedReads int
	}{
		{name: "first and second physical stage reads", reverseStage: map[int]bool{2: true},
			refusalStageReads: 2, refusalNestedReads: 8},
		{name: "final third physical stage read", reverseStage: map[int]bool{3: true},
			refusalStageReads: 3, refusalNestedReads: 12},
		{name: "nested native predecessor across outer reads",
			reversePredecessor: map[int]bool{5: true, 6: true, 7: true, 8: true},
			refusalStageReads:  2, refusalNestedReads: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGatewayRebindTypedHandoverRuntimeFixture(t, true)
			stageName := fixture.attempt.Intent.Identity.StageContainer
			predecessorName := fixture.runner.predecessorState.Identity.FinalContainer
			originalStage := append([]mountInspection(nil), fixture.runner.stage.container.Mounts...)
			originalPredecessor := append([]mountInspection(nil), fixture.runner.predecessor.FinalContainer.Mounts...)
			interceptor := newGatewayRebindTypedMountOrderRunner(fixture.runner, map[string]map[int]bool{
				stageName: test.reverseStage, predecessorName: test.reversePredecessor,
			})
			fixture.predecessor.manager.runner = interceptor
			fixture.runtime.manager.runner = interceptor
			fixture.runtime.stage.manager.runner = interceptor

			effect := fixture.effectAt(12)
			preparation, err := fixture.runtime.prepareHandover(context.Background(), fixture.attempt,
				fixture.current, effect, func(context.Context) error { return nil })
			if err != nil {
				assertGatewayRebindTypedMountOrders(t, interceptor.emitted[stageName], originalStage,
					test.reverseStage)
				assertGatewayRebindTypedMountOrders(t, interceptor.emitted[predecessorName], originalPredecessor,
					test.reversePredecessor)
				if interceptor.counts[stageName] != test.refusalStageReads ||
					interceptor.counts[predecessorName] != test.refusalNestedReads || len(fixture.runner.effects) != 0 {
					t.Fatalf("P12 mount-order refusal occurred outside expected comparison: stageReads=%d nestedReads=%d effects=%#v err=%v",
						interceptor.counts[stageName], interceptor.counts[predecessorName], fixture.runner.effects, err)
				}
				t.Fatalf("P12 stable comparison refused exact owned mounts in reverse Docker order: %v", err)
			}
			if preparation.PredecessorObservationDigest == "" || preparation.LocalHostPort == 0 {
				t.Fatalf("incomplete handover preparation: %#v", preparation)
			}
			if len(fixture.runner.effects) != 0 {
				t.Fatalf("prepare handover dispatched effects: %#v", fixture.runner.effects)
			}
			if interceptor.counts[stageName] != 3 || interceptor.counts[predecessorName] != 12 {
				t.Fatalf("physical read counts stage=%d nested=%d want=3/12",
					interceptor.counts[stageName], interceptor.counts[predecessorName])
			}
			assertGatewayRebindTypedMountOrders(t, interceptor.emitted[stageName], originalStage,
				test.reverseStage)
			assertGatewayRebindTypedMountOrders(t, interceptor.emitted[predecessorName], originalPredecessor,
				test.reversePredecessor)
			if !reflect.DeepEqual(fixture.runner.stage.container.Mounts, originalStage) ||
				!reflect.DeepEqual(fixture.runner.predecessor.FinalContainer.Mounts, originalPredecessor) {
				t.Fatalf("interceptor mutated retained mounts: stage=%#v predecessor=%#v",
					fixture.runner.stage.container.Mounts, fixture.runner.predecessor.FinalContainer.Mounts)
			}
		})
	}
}

var _ runtimeprocess.CommandRunner = (*gatewayRebindTypedMountOrderRunner)(nil)
