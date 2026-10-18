package generatedruntime

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

func TestRetainedRuntimeStopsWithoutRemovalAndReconcilesLostAcknowledgement(t *testing.T) {
	target, inspection := retainedRuntimeFixture()
	inspection.Status, inspection.Running, inspection.Health = "running", true, "healthy"
	requests := 0
	engine, runner, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		requests++
		switch commandKind(request.Args) {
		case "container inspect":
			value := inspection
			if requests >= 3 {
				value.Status, value.Running, value.Health = "exited", false, ""
				value.Networks = nil
			}
			return runtimeJSON(value)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		case "container stop":
			return runtimeRequestResult{err: errors.New("lost stop acknowledgement")}
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	defer release()
	guardCalls := 0
	observation, err := engine.StopRetained(ctx, target, 1500*time.Millisecond, func(context.Context) error {
		guardCalls++
		return nil
	})
	if err != nil || observation.State != RetainedStopped || guardCalls != 1 {
		t.Fatalf("lost stop acknowledgement result = %#v guard=%d err=%v", observation, guardCalls, err)
	}
	assertRetainedCommandInventory(t, runner.requests, []string{"container inspect", "network inspect", "container stop", "container inspect", "network inspect"})
}

func TestRetainedRuntimeAggregateStartCapacityAndReservationSafety(t *testing.T) {
	target, stopped := retainedRuntimeFixture()
	stopped.Status, stopped.Running, stopped.Networks = "exited", false, nil
	running := stopped
	running.Status, running.Running, running.Health = "running", true, "healthy"
	running.Networks = hardenedInspection(retainedCandidateSpec(), defaultLimits()).Networks
	state := stopped
	startError := error(nil)
	engine, runner, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			return runtimeJSON(state)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		case "container start":
			state = running
			return runtimeRequestResult{err: startError}
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	defer release()
	observed, reservation, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target})
	if err != nil || len(observed) != 1 || observed[0].State != RetainedStopped {
		t.Fatalf("aggregate observation = %#v, %v", observed, err)
	}
	startError = errors.New("lost start acknowledgement")
	result, err := engine.StartRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", target, reservation, func(context.Context) error { return nil })
	if err != nil || result.State != RetainedRunning {
		t.Fatalf("lost start acknowledgement result = %#v, %v", result, err)
	}
	if err := engine.ReleaseRetainedReservation(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.StartRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", target, reservation, func(context.Context) error { return nil }); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("released reservation reuse = %v", err)
	}
	assertRetainedCommandInventory(t, runner.requests, []string{"container inspect", "network inspect", "container inspect", "network inspect", "container start", "container inspect", "network inspect", "container inspect", "network inspect"})
}

func TestRetainedRuntimeRequiresOwnedNetworkForStoppedAndRunningTargets(t *testing.T) {
	for _, state := range []RetainedState{RetainedStopped, RetainedRunning} {
		t.Run(string(state), func(t *testing.T) {
			target, inspection := retainedRuntimeFixture()
			if state == RetainedStopped {
				inspection.Status, inspection.Running, inspection.Health, inspection.Networks = "exited", false, "", nil
			} else {
				inspection.Status, inspection.Running, inspection.Health = "running", true, "healthy"
			}
			engine, runner, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
				switch commandKind(request.Args) {
				case "container inspect":
					return runtimeJSON(inspection)
				case "network inspect":
					foreign := retainedNetworkInspection(target)
					foreign.Labels["io.rig.application"] = "99999999-9999-4999-8999-999999999999"
					return runtimeJSON(foreign)
				default:
					t.Fatalf("unexpected retained request: %#v", request.Args)
					return runtimeRequestResult{}
				}
			})
			defer release()
			if _, _, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target}); !IsCode(err, DiagnosticNetworkDriftDetected) {
				t.Fatalf("foreign %s network error = %v", state, err)
			}
			assertRetainedCommandInventory(t, runner.requests, []string{"container inspect", "network inspect"})
		})
	}
}

func TestRetainedRuntimeReservesTheWholeStoppedRosterAtomically(t *testing.T) {
	targetA, stoppedA := retainedRuntimeFixture()
	stoppedA.Status, stoppedA.Running, stoppedA.Networks = "exited", false, nil
	targetB, stoppedB := retainedRuntimeFixtureFor("web", strings.Repeat("c", 64))
	stoppedB.Status, stoppedB.Running, stoppedB.Networks = "exited", false, nil
	engine, runner, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 768 << 20, DiskAvailableBytes: 4 << 30}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			if request.Args[len(request.Args)-1] == targetB.ContainerID {
				return runtimeJSON(stoppedB)
			}
			return runtimeJSON(stoppedA)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(targetA))
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	defer release()
	if _, reservation, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{targetB, targetA}); !IsCode(err, DiagnosticInsufficientReplacementSpace) || reservation != nil {
		t.Fatalf("whole-roster capacity result = %#v, %v", reservation, err)
	}
	assertRetainedCommandInventory(t, runner.requests, []string{"container inspect", "network inspect", "container inspect", "network inspect"})
}

func TestRetainedRuntimeCancellationBeforeStartLeavesReservationProvablyUnattempted(t *testing.T) {
	target, stopped := retainedRuntimeFixture()
	stopped.Status, stopped.Running, stopped.Networks = "exited", false, nil
	engine, runner, ctx, scope, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			return runtimeJSON(stopped)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	defer release()
	_, reservation, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	cancelledParent, cancel := context.WithCancel(context.Background())
	cancelledContext := deploymenteffects.WithScope(cancelledParent, scope)
	if _, err := engine.StartRetained(cancelledContext, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", target, reservation, func(context.Context) error {
		cancel()
		return nil
	}); !IsCode(err, DiagnosticCancelled) {
		t.Fatalf("cancelled pre-command start error = %v", err)
	}
	freshContext := deploymenteffects.WithScope(context.Background(), scope)
	if err := engine.ReleaseUnattemptedRetainedReservation(freshContext, reservation); err != nil {
		t.Fatalf("release never-attempted reservation: %v", err)
	}
	assertRetainedCommandInventory(t, runner.requests, []string{"container inspect", "network inspect", "container inspect", "network inspect"})
}

func TestRetainedRuntimeUnattemptedCleanupSurvivesScopeShutdown(t *testing.T) {
	target, stopped := retainedRuntimeFixture()
	stopped.Status, stopped.Running, stopped.Networks = "exited", false, nil
	engine, _, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 512 << 20, DiskAvailableBytes: 128 << 20}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			return runtimeJSON(stopped)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	_, reservation, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReleaseUnattemptedRetainedReservation(ctx, reservation); err != nil {
		t.Fatalf("unattempted cleanup after scope shutdown: %v", err)
	}
	newScope, releaseAgain, err := deploymenteffects.AcquireScoped(context.Background(), engine.options.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAgain()
	freshContext := deploymenteffects.WithScope(context.Background(), newScope)
	_, next, err := engine.ObserveAndReserveRetained(freshContext, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", []RetainedTarget{target})
	if err != nil {
		t.Fatalf("capacity remained reserved after scope-shutdown cleanup: %v", err)
	}
	if err := engine.ReleaseUnattemptedRetainedReservation(freshContext, next); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedRuntimeReleaseSerializesAgainstConcurrentStart(t *testing.T) {
	target, stopped := retainedRuntimeFixture()
	stopped.Status, stopped.Running, stopped.Networks = "exited", false, nil
	blockReleaseInspect := make(chan struct{})
	releaseInspectEntered := make(chan struct{})
	var inspectMu sync.Mutex
	containerInspects := 0
	runner := &synchronizedRetainedRunner{}
	runner.run = func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			inspectMu.Lock()
			containerInspects++
			call := containerInspects
			inspectMu.Unlock()
			if call == 2 {
				close(releaseInspectEntered)
				<-blockReleaseInspect
			}
			return runtimeJSON(stopped)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	}
	engine, ctx, releaseScope := newRetainedRuntimeEngineWithRunner(t, runner, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}})
	defer releaseScope()
	_, reservation, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	value := reservation.(*retainedReservation)
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- engine.ReleaseRetainedReservation(ctx, reservation) }()
	select {
	case <-releaseInspectEntered:
	case <-time.After(time.Second):
		t.Fatal("reservation release did not reach physical observation")
	}
	if value.mu.TryLock() {
		value.mu.Unlock()
		t.Fatal("release did not retain reservation exclusion across physical observation")
	}
	close(blockReleaseInspect)
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
	requestsBeforeStart := runner.requestCount()
	if _, err := engine.StartRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", target, reservation, func(context.Context) error { return nil }); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("start after reservation release = %v", err)
	}
	if got := runner.requestCount(); got != requestsBeforeStart {
		t.Fatalf("released reservation reached Docker: requests=%d want=%d", got, requestsBeforeStart)
	}
}

func TestRetainedRuntimeRefusesUnstableDriftAndRetainsAmbiguousStartCapacity(t *testing.T) {
	target, stopped := retainedRuntimeFixture()
	stopped.Status, stopped.Running, stopped.Networks = "exited", false, nil
	state := stopped
	engine, runner, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			return runtimeJSON(state)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		case "container start":
			return runtimeRequestResult{err: errors.New("definite command failure")}
		default:
			t.Fatalf("unexpected retained request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	defer release()
	_, reservation, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.StartRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", target, reservation, func(context.Context) error { return nil }); !IsCode(err, DiagnosticCandidateStartFailed) {
		t.Fatalf("failed start error = %v", err)
	}
	if err := engine.ReleaseRetainedReservation(ctx, reservation); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("ambiguous attempted share was released: %v", err)
	}
	state.Status, state.Running, state.Paused = "paused", true, true
	if _, _, err := engine.ObserveAndReserveRetained(ctx, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", []RetainedTarget{target}); !IsCode(err, DiagnosticCandidateExited) {
		t.Fatalf("paused state error = %v", err)
	}
	for _, request := range runner.requests {
		if kind := commandKind(request.Args); kind == "container rm" || kind == "container create" || kind == "image build" || kind == "network create" {
			t.Fatalf("destructive retained command = %#v", request.Args)
		}
	}
}

func TestRetainedRuntimeRejectsMissingForeignAndExpiredScopes(t *testing.T) {
	target, _ := retainedRuntimeFixture()
	engine, _, ctx, _, release := newRetainedRuntimeEngine(t, fixedCapacitySource{snapshot: CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}}, func(runtimeprocess.CommandRequest) runtimeRequestResult {
		t.Fatal("scope refusal reached Docker")
		return runtimeRequestResult{}
	})
	if _, _, err := engine.ObserveAndReserveRetained(context.Background(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target}); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("missing scope error = %v", err)
	}
	foreignDirectories, err := runtimedocker.PrepareControllerDirectories(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreignScope, foreignRelease, err := deploymenteffects.AcquireScoped(context.Background(), foreignDirectories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	foreignContext := deploymenteffects.WithScope(context.Background(), foreignScope)
	if _, _, err := engine.ObserveAndReserveRetained(foreignContext, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target}); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("foreign scope error = %v", err)
	}
	if err := foreignRelease(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.ObserveAndReserveRetained(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target}); !IsCode(err, DiagnosticValidationFailed) {
		t.Fatalf("expired scope error = %v", err)
	}
}

type cancellingCapacitySource struct {
	cancel context.CancelFunc
	calls  int
}

func (s *cancellingCapacitySource) Snapshot(context.Context) (CapacitySnapshot, error) {
	s.calls++
	if s.calls == 1 {
		s.cancel()
	}
	return CapacitySnapshot{MemoryAvailableBytes: 4 << 30, DiskAvailableBytes: 4 << 30}, nil
}

func TestRetainedRuntimeCanceledSuccessfulCapacitySnapshotReleasesAdmission(t *testing.T) {
	target, stopped := retainedRuntimeFixture()
	stopped.Status, stopped.Running, stopped.Networks = "exited", false, nil
	parent, cancel := context.WithCancel(context.Background())
	source := &cancellingCapacitySource{cancel: cancel}
	engine, _, _, scope, release := newRetainedRuntimeEngine(t, source, func(request runtimeprocess.CommandRequest) runtimeRequestResult {
		switch commandKind(request.Args) {
		case "container inspect":
			return runtimeJSON(stopped)
		case "network inspect":
			return runtimeJSON(retainedNetworkInspection(target))
		default:
			t.Fatalf("unexpected request: %#v", request.Args)
			return runtimeRequestResult{}
		}
	})
	defer release()
	cancelledContext := deploymenteffects.WithScope(parent, scope)
	if _, _, err := engine.ObserveAndReserveRetained(cancelledContext, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []RetainedTarget{target}); !IsCode(err, DiagnosticCancelled) {
		t.Fatalf("cancelled successful snapshot error = %v", err)
	}
	freshContext := deploymenteffects.WithScope(context.Background(), scope)
	_, reservation, err := engine.ObserveAndReserveRetained(freshContext, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", []RetainedTarget{target})
	if err != nil {
		t.Fatalf("capacity remained reserved after cancelled snapshot: %v", err)
	}
	if err := engine.ReleaseRetainedReservation(freshContext, reservation); err != nil {
		t.Fatal(err)
	}
}

func newRetainedRuntimeEngine(t *testing.T, capacity CapacitySource, run func(runtimeprocess.CommandRequest) runtimeRequestResult) (*Engine, *runtimeFakeRunner, context.Context, deploymenteffects.Scope, func() error) {
	t.Helper()
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtimeFakeRunner{run: run}
	engine, err := NewEngine(runner, &runtimeFakeEnvironment{path: filepath.Join(root, "unused.env")}, capacity, EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, Limits: defaultLimits(), ReplacementDiskBytes: 128 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return engine, runner, deploymenteffects.WithScope(context.Background(), scope), scope, release
}

func retainedRuntimeFixture() (RetainedTarget, containerInspection) {
	return retainedRuntimeFixtureFor("api", testContainerID)
}

func retainedRuntimeFixtureFor(component, containerID string) (RetainedTarget, containerInspection) {
	spec := retainedCandidateSpec()
	spec.ComponentName = component
	candidate := candidateForSpec(spec)
	candidate.ContainerID = containerID
	target := RetainedTarget{
		AppID: candidate.AppID, ReleaseID: candidate.ReleaseID, DeploymentID: candidate.DeploymentID,
		ArtifactID: candidate.ArtifactID, DeploymentPlanRevisionID: candidate.DeploymentPlanRevisionID,
		Component: candidate.Component, Role: candidate.Role, Technology: candidate.Technology,
		Slot: string(candidate.Slot), ContainerID: candidate.ContainerID, ContainerName: candidate.ContainerName,
		NetworkName: candidate.NetworkName, NetworkAlias: candidate.NetworkAlias,
		ImageContentID: candidate.ImageContentID, BuildDefinitionDigest: spec.BuildDefinitionDigest,
		WorkingDirectory: candidate.WorkingDirectory, RunCommandDigest: candidate.RunCommandDigest,
		InternalPort: candidate.InternalPort,
		Policy: RetainedPolicy{
			MemoryBytes: defaultLimits().MemoryBytes, MemorySwapBytes: defaultLimits().MemoryBytes,
			CPUQuota: 100000, CPUPeriod: 100000, PIDsLimit: defaultLimits().PIDs,
			TmpfsBytes: defaultLimits().TmpfsBytes, LogDriver: "local", LogMaxSize: defaultLimits().LogSize,
			LogMaxFiles: defaultLimits().LogFiles, HardeningPolicyVersion: "v1",
		},
	}
	inspection := hardenedInspection(spec, defaultLimits())
	inspection.ID = containerID
	return target, inspection
}

type synchronizedRetainedRunner struct {
	mu       sync.Mutex
	requests []runtimeprocess.CommandRequest
	run      func(runtimeprocess.CommandRequest) runtimeRequestResult
}

func (r *synchronizedRetainedRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	request.Args = append([]string(nil), request.Args...)
	request.Env = append([]string(nil), request.Env...)
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	response := r.run(request)
	return response.result, response.err
}

func (r *synchronizedRetainedRunner) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func newRetainedRuntimeEngineWithRunner(t *testing.T, runner runtimeprocess.CommandRunner, capacity CapacitySource) (*Engine, context.Context, func() error) {
	t.Helper()
	root := t.TempDir()
	directories, err := runtimedocker.PrepareControllerDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(runner, &runtimeFakeEnvironment{path: filepath.Join(root, "unused.env")}, capacity, EngineOptions{
		DockerExecutable: filepath.Join(root, "docker.exe"), DockerConfigDirectory: directories.DockerConfigDirectory,
		WorkingDirectory: directories.WorkingDirectory, CommandTimeout: time.Second, HealthTimeout: time.Second,
		HealthPollInterval: 100 * time.Millisecond, Limits: defaultLimits(), ReplacementDiskBytes: 128 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, release, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return engine, deploymenteffects.WithScope(context.Background(), scope), release
}

func retainedCandidateSpec() CandidateSpec {
	spec := candidateSpec()
	spec.Technology = "node"
	return spec
}

func retainedNetworkInspection(target RetainedTarget) networkInspection {
	return networkInspection{
		Name: target.NetworkName, Driver: "bridge", Scope: "local",
		Labels: map[string]string{"io.rig.managed": NetworkOwnershipLabelValue, "io.rig.application": target.AppID},
	}
}

func assertRetainedCommandInventory(t *testing.T, requests []runtimeprocess.CommandRequest, expected []string) {
	t.Helper()
	actual := make([]string, len(requests))
	for index := range requests {
		actual[index] = commandKind(requests[index].Args)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("retained command inventory = %#v want %#v", actual, expected)
	}
}
