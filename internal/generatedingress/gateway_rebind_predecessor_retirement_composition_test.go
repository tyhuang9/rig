package generatedingress

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// gatewayRebindRetirementRunner models only Docker's post-stop runtime state
// acknowledgement. The retained composition runner still owns every exact
// command, inventory, and identity response.
type gatewayRebindRetirementRunner struct {
	runtimeprocess.CommandRunner
	before func([]string)
	after  func([]string)
}

type gatewayRebindFinalSweepRevivalRuntime struct {
	managerGatewayCurrentPhysicalRuntime
	revive               func()
	localProofSucceeded  bool
	revived              bool
	finalInventorySweeps int
}

func (r *gatewayRebindFinalSweepRevivalRuntime) retiredLocalListenerSafe(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction, port uint16, guard func(context.Context) error,
) bool {
	safe := r.managerGatewayCurrentPhysicalRuntime.retiredLocalListenerSafe(ctx, action, port, guard)
	if safe && port == action.Native.Journal.Source.LocalHostPort {
		r.localProofSucceeded = true
		if !r.revived {
			r.revive()
			r.revived = true
		}
	}
	return safe
}

func (r *gatewayRebindFinalSweepRevivalRuntime) predecessorInventoriesStopped(ctx context.Context,
	action gatewayCurrentPredecessorRetirementAction,
) bool {
	r.finalInventorySweeps++
	return r.managerGatewayCurrentPhysicalRuntime.predecessorInventoriesStopped(ctx, action)
}

func (r gatewayRebindRetirementRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if r.before != nil {
		r.before(request.Args)
	}
	result, err := r.CommandRunner.Run(ctx, request)
	if err == nil && r.after != nil {
		r.after(request.Args)
	}
	return result, err
}

func TestGatewayRebindStartupRetiresReturnedNativePredecessorBeforeRestoringCurrent(t *testing.T) {
	f, input, driver := newGatewayRebindCompositionFixture(t)
	ctx := context.Background()
	result, err := f.manager.commitGatewayRebindWithDriver(ctx, f.repository, input, driver)
	if err != nil || result.FinalPhase != appaccess.GatewayRebindCommitted || !result.FenceReleased {
		t.Fatalf("commit result=%#v err=%v", result, err)
	}
	r := driver.runner
	if r.predecessor.FinalContainer.Running || !r.final.Running {
		t.Fatal("commit did not withdraw the exact native predecessor")
	}

	// Model the terminal snapshot before the old NIC returns, then return the
	// exact pinned predecessor and stop the selected current as on host reboot.
	oldIPv4 := f.state.Profile.SelectedIPv4
	oldPort := f.state.Profile.PortStart
	originalObserver := f.manager.gatewayRebindV2NetworkObserver
	returnedMode := "absent"
	f.manager.gatewayRebindV2NetworkObserver = func(observeCtx context.Context,
		claim appaccess.GatewayRebindClaimV2,
	) (gatewayRebindSuccessorNetworkObservation, error) {
		observation, observeErr := originalObserver(observeCtx, claim)
		if observeErr != nil {
			return observation, observeErr
		}
		filtered := make([]gatewayRebindSuccessorNetworkCandidate, 0, len(observation.Candidates))
		returned := make([]gatewayRebindSuccessorNetworkCandidate, 0, 1)
		for _, candidate := range observation.Candidates {
			if candidate.IPv4 == oldIPv4 {
				returned = append(returned, candidate)
			} else {
				filtered = append(filtered, candidate)
			}
		}
		if len(returned) != 1 {
			return gatewayRebindSuccessorNetworkObservation{}, context.Canceled
		}
		switch returnedMode {
		case "absent":
		case "exact":
			filtered = append(filtered, returned[0])
		case "wrong interface":
			wrong := returned[0]
			wrong.InterfaceID = "returned-on-wrong-interface"
			filtered = append(filtered, wrong)
		case "duplicate":
			filtered = append(filtered, returned[0], returned[0])
		default:
			return gatewayRebindSuccessorNetworkObservation{}, context.Canceled
		}
		observation.Candidates = filtered
		sort.Slice(observation.Candidates, func(i, j int) bool {
			return gatewayRebindCandidateLess(observation.Candidates[i], observation.Candidates[j])
		})
		return observation, nil
	}
	snapshot, err := f.repository.GatewayRebindRecoverySnapshot(ctx)
	selection, selectionErr := f.manager.selectGatewayCurrentLocked(ctx, snapshot)
	history, historyErr := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	action, actionErr := gatewayCurrentPredecessorRetirementActionFor(selection, history)
	if err != nil || selectionErr != nil || historyErr != nil || actionErr != nil {
		t.Fatalf("returned NIC action: snapshot=%v selection=%v history=%v action=%v", err, selectionErr, historyErr, actionErr)
	}
	managed, ok := f.manager.gatewayCurrentPhysicalDriver.(managedGatewayCurrentPhysicalDriver)
	if !ok {
		t.Fatalf("current driver=%T", f.manager.gatewayCurrentPhysicalDriver)
	}
	physical, ok := managed.runtime.(managerGatewayCurrentPhysicalRuntime)
	if !ok {
		t.Fatalf("current runtime=%T", managed.runtime)
	}
	probeCalls := 0
	originalProbe := physical.hostProbe
	physical.hostProbe = func(probeCtx context.Context, address string, port uint16, host, path string) gatewayV2HostProbeResult {
		probeCalls++
		return originalProbe(probeCtx, address, port, host, path)
	}
	countingProbe := physical.hostProbe
	managed.runtime = physical
	f.manager.gatewayCurrentPhysicalDriver = managed
	guard := func(context.Context) error { return nil }
	resetNative := func() {
		r.predecessor = gatewayRebindFixtureDockerObservation(t, f)
		r.stopPredecessor()
		r.effects, r.requests = nil, nil
		f.manager.runner = r
	}
	for _, test := range []struct {
		name        string
		mutate      func()
		wantEffects int
		wantFailure bool
	}{
		{name: "native running", mutate: r.startPredecessor, wantEffects: 1},
		{name: "native restarting", mutate: func() { r.predecessor.FinalContainer.Restarting = true }, wantEffects: 1},
		{name: "native paused", mutate: func() { r.predecessor.FinalRuntime.Paused = true }, wantEffects: 1, wantFailure: true},
		{name: "native dead", mutate: func() { r.predecessor.FinalRuntime.Dead = true }, wantEffects: 1, wantFailure: true},
		{name: "native stopped"},
		{name: "native crossed owner", mutate: func() {
			r.predecessor.FinalContainer.Labels[gatewayV2OperationLabelKey] = "crossed-owner"
		}, wantFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetNative()
			if test.mutate != nil {
				test.mutate()
			}
			f.manager.runner = gatewayRebindRetirementRunner{CommandRunner: r, after: func(args []string) {
				if reflect.DeepEqual(args, []string{"container", "stop", "--time", "10", action.Native.Journal.Resources.FinalContainerID}) {
					r.predecessor.FinalContainer.Restarting = false
				}
			}}
			before := len(r.effects)
			err := physical.stopNativePredecessorOwned(ctx, action.Native, guard)
			stops := 0
			for _, effect := range r.effects[before:] {
				if reflect.DeepEqual(effect, []string{"container", "stop", "--time", "10", action.Native.Journal.Resources.FinalContainerID}) {
					stops++
				}
			}
			if (err != nil) != test.wantFailure || stops != test.wantEffects || len(r.effects[before:]) != test.wantEffects {
				t.Fatalf("native retirement err=%v stops=%d effects=%v", err, stops, r.effects[before:])
			}
		})
	}
	resetNative()
	if err := physical.stopNativePredecessorOwned(ctx, action.Native, func(context.Context) error { return context.Canceled }); err == nil || len(r.effects) != 0 {
		t.Fatalf("native guard refusal reached stop: effects=%v err=%v", r.effects, err)
	}
	resetNative()
	r.startPredecessor()
	returnedMode = "exact"
	beforeRevivalEffects := len(r.effects)
	revival := &gatewayRebindFinalSweepRevivalRuntime{managerGatewayCurrentPhysicalRuntime: physical,
		revive: r.startPredecessor}
	managed.runtime = revival
	if err := managed.retireGatewayCurrentPredecessors(ctx, action, guard); err == nil ||
		!revival.localProofSucceeded || !revival.revived || revival.finalInventorySweeps != 1 {
		t.Fatalf("revived native predecessor passed final inventory: local-proof=%t revived=%t sweeps=%d err=%v",
			revival.localProofSucceeded, revival.revived, revival.finalInventorySweeps, err)
	}
	exactStops := 0
	for _, effect := range r.effects[beforeRevivalEffects:] {
		if reflect.DeepEqual(effect, []string{"container", "stop", "--time", "10", action.Native.Journal.Resources.FinalContainerID}) {
			exactStops++
		}
	}
	if exactStops != 1 || len(r.effects[beforeRevivalEffects:]) != 1 {
		t.Fatalf("final-sweep revival exact immutable-ID stops=%d effects=%v", exactStops, r.effects[beforeRevivalEffects:])
	}
	resetNative()
	physical.hostProbe = countingProbe
	managed.runtime = physical
	f.manager.gatewayCurrentPhysicalDriver = managed
	for _, test := range []struct {
		name         string
		wantSafe     bool
		wantNoProbes bool
	}{
		{name: "absent old address", wantSafe: true, wantNoProbes: true},
		{name: "wrong interface", wantSafe: false, wantNoProbes: true},
		{name: "duplicate old address", wantSafe: false, wantNoProbes: true},
		{name: "exact returned stopped predecessor", wantSafe: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "absent old address":
				returnedMode = "absent"
			case "wrong interface":
				returnedMode = "wrong interface"
			case "duplicate old address":
				returnedMode = "duplicate"
			default:
				returnedMode = "exact"
			}
			probeCalls = 0
			if got := physical.retiredProfileListenersSafe(ctx, action, action.Native.State.Profile, guard); got != test.wantSafe ||
				(test.wantNoProbes && probeCalls != 0) || (!test.wantNoProbes && probeCalls == 0) {
				t.Fatalf("returned NIC safety=%t probes=%d want safety=%t no-probes=%t", got, probeCalls, test.wantSafe, test.wantNoProbes)
			}
		})
	}
	returnedMode = "absent"
	preReturn, err := f.manager.gatewayRebindV2NetworkObserver(ctx, appaccess.GatewayRebindClaimV2{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range preReturn.Candidates {
		if candidate.IPv4 == oldIPv4 {
			t.Fatal("pre-return observation unexpectedly contains the retired address")
		}
	}
	returnedMode = "exact"
	returned, err := f.manager.gatewayRebindV2NetworkObserver(ctx, appaccess.GatewayRebindClaimV2{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	exactReturned := false
	for _, candidate := range returned.Candidates {
		if candidate.IPv4 == oldIPv4 {
			count++
			exactReturned = exactReturned || candidate.InterfaceID == f.state.Profile.InterfaceID
		}
	}
	if count != 1 || !exactReturned {
		t.Fatalf("returned old address candidates=%d exact=%t", count, exactReturned)
	}
	if probe := r.hostProbe(ctx, oldIPv4, oldPort, oldIPv4, "/"); probe.Connected {
		t.Fatal("retired predecessor listener present before exact old address return")
	}
	beforeSQL, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeHistory, err := f.manager.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeFiles, err := readGatewayHistorySnapshotMode(f.manager.store, true)
	if err != nil {
		t.Fatal(err)
	}
	r.stopFinal()
	r.startPredecessor()
	if !r.predecessor.FinalContainer.Running || r.final.Running {
		t.Fatal("failed to reproduce returned predecessor with stopped current")
	}
	if probe := r.hostProbe(ctx, oldIPv4, oldPort, oldIPv4, "/"); !probe.Connected {
		t.Fatal("returned exact predecessor does not occupy its old listener")
	}
	effects := len(r.effects)
	fresh := freshGatewayRebindRecoveryManager(f.manager)
	fresh.gatewayRebindV2NetworkObserver = f.manager.gatewayRebindV2NetworkObserver
	driver.install(fresh)
	handled, err := fresh.RestoreGatewayCurrentServingStartup(ctx, f.repository)
	if err != nil || !handled || !r.final.Running || r.predecessor.FinalContainer.Running {
		t.Fatalf("startup did not retire predecessor before current restore: handled=%t err=%v effects=%v",
			handled, err, r.effects[effects:])
	}
	want := [][]string{
		{"container", "stop", "--time", "10", action.Native.Journal.Resources.FinalContainerID},
		{"container", "start", r.finalID},
	}
	if !reflect.DeepEqual(r.effects[effects:], want) {
		t.Fatalf("predecessor retirement/current restore order=%v want=%v", r.effects[effects:], want)
	}
	for port := f.state.Profile.PortStart; port <= f.state.Profile.PortEnd; port++ {
		probe := r.hostProbe(ctx, oldIPv4, port, oldIPv4, "/")
		if probe.Connected || probe.Responded {
			t.Fatalf("retired old listener remains at %s:%d", oldIPv4, port)
		}
		if port == ^uint16(0) {
			break
		}
	}
	recovered, err := fresh.RecoverGatewayRebindStartup(ctx, f.repository)
	if err != nil || !recovered.FenceReleased || len(r.effects) != effects+len(want) {
		t.Fatalf("terminal recovery after retirement: result=%#v err=%v effects=%v", recovered, err, r.effects[effects:])
	}
	afterSQL, err := f.repository.HostingGatewayStartupSnapshot(ctx)
	afterHistory, historyErr := fresh.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	afterFiles, filesErr := readGatewayHistorySnapshotMode(fresh.store, true)
	if err != nil || historyErr != nil || filesErr != nil || !reflect.DeepEqual(beforeSQL, afterSQL) ||
		!sameGatewayRebindCurrentHistory(beforeHistory, afterHistory) || !sameGatewayHistorySnapshot(beforeFiles, afterFiles) {
		t.Fatal("startup retirement changed SQL or protected history")
	}
	replayEffects := len(r.effects)
	replay := freshGatewayRebindRecoveryManager(fresh)
	replay.gatewayRebindV2NetworkObserver = fresh.gatewayRebindV2NetworkObserver
	driver.install(replay)
	if handled, err = replay.RestoreGatewayCurrentServingStartup(ctx, f.repository); err != nil || !handled {
		t.Fatalf("stable retirement replay restore handled=%t err=%v", handled, err)
	}
	if recovered, err = replay.RecoverGatewayRebindStartup(ctx, f.repository); err != nil || !recovered.FenceReleased || len(r.effects) != replayEffects {
		t.Fatalf("stable retirement replay effects=%v result=%#v err=%v", r.effects[replayEffects:], recovered, err)
	}
	replaySQL, sqlErr := f.repository.HostingGatewayStartupSnapshot(ctx)
	replayHistory, replayHistoryErr := replay.scanGatewayRebindProtectedIntentHistoryLocked(nil)
	replayFiles, replayFilesErr := readGatewayHistorySnapshotMode(replay.store, true)
	if sqlErr != nil || replayHistoryErr != nil || replayFilesErr != nil || !reflect.DeepEqual(beforeSQL, replaySQL) ||
		!sameGatewayRebindCurrentHistory(beforeHistory, replayHistory) || !sameGatewayHistorySnapshot(beforeFiles, replayFiles) {
		t.Fatal("retirement replay changed SQL or protected history")
	}
}
