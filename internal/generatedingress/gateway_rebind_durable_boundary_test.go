package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindDurableBoundaryRunner struct {
	events     []GatewayRebindDurableBoundaryEvent
	timeline   []string
	fault      error
	faultCalls []GatewayRebindTerminalReleaseKind
}

func (r *gatewayRebindDurableBoundaryRunner) Run(context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	return runtimeprocess.CommandResult{}, nil
}

func (r *gatewayRebindDurableBoundaryRunner) ObserveGatewayRebindDurableBoundary(_ context.Context,
	event GatewayRebindDurableBoundaryEvent,
) {
	r.events = append(r.events, event)
	r.timeline = append(r.timeline, "observe:"+string(event.Boundary))
}

func (r *gatewayRebindDurableBoundaryRunner) GatewayRebindTerminalReleaseFault(
	kind GatewayRebindTerminalReleaseKind,
) error {
	r.faultCalls = append(r.faultCalls, kind)
	r.timeline = append(r.timeline, "fault:"+string(kind))
	return r.fault
}

func TestGatewayRebindDurableBoundaryObserverReceivesImmutableMetadata(t *testing.T) {
	runner := &gatewayRebindDurableBoundaryRunner{}
	manager := &Manager{runner: runner}
	event := GatewayRebindDurableBoundaryEvent{
		Boundary:    GatewayRebindBoundaryProtectedProgressReadback,
		OperationID: "12345678-1234-4234-8234-123456789abc", ProtectedPhase: "rollback_intent",
		ProtectedGeneration: 7, ProtectedSequence: 18, Digest: "digest",
	}
	manager.observeGatewayRebindDurableBoundary(context.Background(), event)
	event.OperationID = "changed"
	if len(runner.events) != 1 || runner.events[0].OperationID != "12345678-1234-4234-8234-123456789abc" ||
		runner.events[0].ProtectedPhase != "rollback_intent" || runner.events[0].ProtectedSequence != 18 {
		t.Fatalf("observer did not receive copied immutable metadata: %+v", runner.events)
	}
}

func TestGatewayRebindTerminalReleaseFaultRunsOnlyAfterSuccessfulRealRelease(t *testing.T) {
	injected := errors.New("injected post-release fault")
	runner := &gatewayRebindDurableBoundaryRunner{fault: injected}
	manager := &Manager{runner: runner}
	event := GatewayRebindDurableBoundaryEvent{
		Boundary:    GatewayRebindBoundaryFenceClearReadback,
		OperationID: "12345678-1234-4234-8234-123456789abc",
		SQLPhase:    appaccess.GatewayRebindCommitted, ProtectedPhase: "handover_committed",
		ProtectedGeneration: 7, ProtectedSequence: 17, Digest: strings.Repeat("a", 64),
		Disposition: appaccess.GatewayRebindDispositionCommit,
	}
	realCalls := 0
	err := manager.releaseGatewayRebindTerminalBoundary(context.Background(),
		GatewayRebindTerminalReleaseEffects, func() error {
			realCalls++
			runner.timeline = append(runner.timeline, "real:effects")
			return nil
		}, event)
	if realCalls != 1 || !errors.Is(err, injected) ||
		!reflect.DeepEqual(runner.timeline, []string{"real:effects", "observe:effects_release", "fault:effects"}) ||
		!reflect.DeepEqual(runner.faultCalls, []GatewayRebindTerminalReleaseKind{GatewayRebindTerminalReleaseEffects}) {
		t.Fatalf("post-release fault did not preserve real release ordering: calls=%d timeline=%v err=%v",
			realCalls, runner.timeline, err)
	}
}

func TestGatewayRebindTerminalReleaseFailureIsNotReportedAsCompleted(t *testing.T) {
	realFailure := errors.New("real release failed")
	runner := &gatewayRebindDurableBoundaryRunner{fault: errors.New("must not run")}
	manager := &Manager{runner: runner}
	realCalls := 0
	err := manager.releaseGatewayRebindTerminalBoundary(context.Background(),
		GatewayRebindTerminalReleaseGateway, func() error {
			realCalls++
			return realFailure
		}, GatewayRebindDurableBoundaryEvent{Boundary: GatewayRebindBoundaryFenceClearReadback,
			OperationID: "12345678-1234-4234-8234-123456789abc",
			SQLPhase:    appaccess.GatewayRebindCommitted, ProtectedPhase: "handover_committed",
			ProtectedGeneration: 7, ProtectedSequence: 17, Digest: strings.Repeat("a", 64),
			Disposition: appaccess.GatewayRebindDispositionCommit})
	if realCalls != 1 || !errors.Is(err, realFailure) || len(runner.events) != 0 || len(runner.faultCalls) != 0 {
		t.Fatalf("failed real release was reported as completed: calls=%d events=%v faults=%v err=%v",
			realCalls, runner.events, runner.faultCalls, err)
	}
}

func TestGatewayRebindTerminalReleaseUnarmedAndNilManagerStayPrivate(t *testing.T) {
	runner := &gatewayRebindDurableBoundaryRunner{fault: errors.New("must not run")}
	manager := &Manager{runner: runner}
	realCalls := 0
	if err := manager.releaseGatewayRebindTerminalBoundary(context.Background(),
		GatewayRebindTerminalReleaseEffects, func() error { realCalls++; return nil },
		GatewayRebindDurableBoundaryEvent{}); err != nil {
		t.Fatal(err)
	}
	var nilManager *Manager
	if err := nilManager.releaseGatewayRebindTerminalBoundary(context.Background(),
		GatewayRebindTerminalReleaseGateway, func() error { realCalls++; return nil },
		GatewayRebindDurableBoundaryEvent{Boundary: GatewayRebindBoundaryFenceClearReadback,
			OperationID: "12345678-1234-4234-8234-123456789abc",
			SQLPhase:    appaccess.GatewayRebindCommitted, ProtectedPhase: "handover_committed",
			ProtectedGeneration: 7, ProtectedSequence: 17, Digest: strings.Repeat("a", 64),
			Disposition: appaccess.GatewayRebindDispositionCommit}); err != nil {
		t.Fatal(err)
	}
	nilRunnerManager := &Manager{}
	if err := nilRunnerManager.releaseGatewayRebindTerminalBoundary(context.Background(),
		GatewayRebindTerminalReleaseGateway, func() error { realCalls++; return nil },
		GatewayRebindDurableBoundaryEvent{Boundary: GatewayRebindBoundaryFenceClearReadback,
			OperationID: "12345678-1234-4234-8234-123456789abc",
			SQLPhase:    appaccess.GatewayRebindCommitted, ProtectedPhase: "handover_committed",
			ProtectedGeneration: 7, ProtectedSequence: 17, Digest: strings.Repeat("a", 64),
			Disposition: appaccess.GatewayRebindDispositionCommit}); err != nil {
		t.Fatal(err)
	}
	if realCalls != 3 || len(runner.events) != 0 || len(runner.faultCalls) != 0 {
		t.Fatalf("unarmed release seam escaped its runner capability: calls=%d events=%v faults=%v",
			realCalls, runner.events, runner.faultCalls)
	}
}

func TestGatewayRebindTerminalReleaseConsumesMetadataBeforeUnlockWithoutErasingSuccessor(t *testing.T) {
	runner := &gatewayRebindDurableBoundaryRunner{}
	oldEvent := GatewayRebindDurableBoundaryEvent{
		Boundary:    GatewayRebindBoundaryFenceClearReadback,
		OperationID: "12345678-1234-4234-8234-123456789abc",
		SQLPhase:    appaccess.GatewayRebindCommitted, ProtectedPhase: "handover_committed",
		ProtectedGeneration: 7, ProtectedSequence: 17, Digest: strings.Repeat("a", 64),
		Disposition: appaccess.GatewayRebindDispositionCommit,
	}
	successorEvent := GatewayRebindDurableBoundaryEvent{
		Boundary:    GatewayRebindBoundaryFenceClearReadback,
		OperationID: "87654321-4321-4321-8321-cba987654321",
		SQLPhase:    appaccess.GatewayRebindRolledBack, ProtectedPhase: "handover_rolled_back",
		ProtectedGeneration: 8, ProtectedSequence: 19, Digest: strings.Repeat("b", 64),
		Disposition: appaccess.GatewayRebindDispositionAbort,
	}
	manager := &Manager{runner: runner, gatewayRebindTerminalReleaseEvent: oldEvent,
		gatewayRebindFailStop: &atomic.Bool{}, gatewayRebindCommitBarrier: &atomic.Bool{}}
	manager.gatewayRebindCommitBarrier.Store(true)
	consumedBeforeEffects, consumedBeforeGateway := false, false
	err := manager.releaseGatewayRebindTerminalLocks(func() error {
		consumedBeforeEffects = manager.gatewayRebindTerminalReleaseEvent == (GatewayRebindDurableBoundaryEvent{})
		runner.timeline = append(runner.timeline, "real:effects")
		return nil
	}, func() error {
		consumedBeforeGateway = manager.gatewayRebindTerminalReleaseEvent == (GatewayRebindDurableBoundaryEvent{})
		runner.timeline = append(runner.timeline, "real:gateway")
		// Model a successor acquiring the Manager lock immediately after the
		// real gateway release unlocks it and arming its own terminal metadata
		// before the old release callback returns to its caller.
		manager.gatewayRebindTerminalReleaseEvent = successorEvent
		return nil
	}, nil)
	wantTimeline := []string{
		"real:effects", "observe:effects_release", "fault:effects",
		"real:gateway", "observe:gateway_release", "fault:gateway",
	}
	if err != nil || !consumedBeforeEffects || !consumedBeforeGateway ||
		manager.gatewayRebindTerminalReleaseEvent != successorEvent ||
		manager.gatewayRebindFailStop.Load() || manager.gatewayRebindCommitBarrier.Load() ||
		!reflect.DeepEqual(runner.timeline, wantTimeline) || len(runner.events) != 2 ||
		runner.events[0].OperationID != oldEvent.OperationID || runner.events[1].OperationID != oldEvent.OperationID {
		t.Fatalf("terminal metadata crossed unlock: effects_consumed=%t gateway_consumed=%t event=%+v timeline=%v fail_stop=%t barrier=%t error=%v",
			consumedBeforeEffects, consumedBeforeGateway, manager.gatewayRebindTerminalReleaseEvent,
			runner.timeline, manager.gatewayRebindFailStop.Load(), manager.gatewayRebindCommitBarrier.Load(), err)
	}
}
