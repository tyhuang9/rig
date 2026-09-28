//go:build live_docker

package main

// This file deliberately contains only test-process instrumentation.  The
// production composition receives the same CommandRunner and jobs.Executor it
// normally would; the wrappers stop a child test binary only after the durable
// database state proves that its named crash boundary has been crossed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type controllerProcessKillBoundary string

const (
	controllerKillBeforeBuild  controllerProcessKillBoundary = "before_build"
	controllerKillBeforeStart  controllerProcessKillBoundary = "before_candidate_start"
	controllerKillAfterRoute   controllerProcessKillBoundary = "after_route_commit"
	controllerKillBeforeDrain  controllerProcessKillBoundary = "before_drain_stop"
	controllerKillAfterSuccess controllerProcessKillBoundary = "after_runtime_success"
)

// controllerProcessKillBarrier is file-backed so that the parent test can
// observe a separately executed go-test child without passing command output
// (which could carry a runtime secret) across the boundary.
type controllerProcessKillBarrier struct {
	boundary     controllerProcessKillBoundary
	marker       string
	state        *generatedruntimestate.Repository
	appID        string
	deploymentID string
	once         sync.Once
}

func (b *controllerProcessKillBarrier) wait(ctx context.Context, want generatedruntimestate.Phase, predicate func(generatedruntimestate.Deployment) bool) error {
	if b == nil || b.state == nil || b.marker == "" || b.appID == "" || b.deploymentID == "" {
		return errors.New("process-kill barrier is incomplete")
	}
	runtime, err := b.state.Get(ctx, b.appID, b.deploymentID)
	if err != nil || runtime.Phase != want || !predicate(runtime) {
		return fmt.Errorf("process-kill boundary %s has no durable %s state", b.boundary, want)
	}
	var writeErr error
	b.once.Do(func() { writeErr = controllerProcessKillMarker(b.marker, b.boundary) })
	if writeErr != nil {
		return writeErr
	}
	// SIGKILL from the parent is the normal completion path.  A finite wait
	// prevents a forgotten parent from pinning a live-Docker gate forever.
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("process-kill parent did not terminate child")
	}
}

// controllerProcessKillRunner pauses at command boundaries after checking the
// state which was durably written by the production executor.
type controllerProcessKillRunner struct {
	delegate runtimeprocess.CommandRunner
	barrier  *controllerProcessKillBarrier
}

func (r controllerProcessKillRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if r.delegate == nil {
		return runtimeprocess.CommandResult{}, errors.New("process-kill runner delegate is required")
	}
	if r.barrier != nil {
		if controllerProcessKillBuild(request) {
			if err := r.barrier.wait(ctx, generatedruntimestate.PhaseBuilding, func(generatedruntimestate.Deployment) bool { return true }); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
		if controllerProcessKillStart(request) {
			if err := r.barrier.wait(ctx, generatedruntimestate.PhaseStartingCandidate, controllerProcessKillHasDurableCandidate); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
		if controllerProcessKillDrain(request) {
			if err := r.barrier.wait(ctx, generatedruntimestate.PhaseDraining, func(d generatedruntimestate.Deployment) bool { return d.CandidateSlot != d.PreviousActiveSlot }); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
	}
	result, err := r.delegate.Run(ctx, request)
	if err == nil && r.barrier != nil && controllerProcessKillRouteCommit(request) {
		if waitErr := r.barrier.wait(ctx, generatedruntimestate.PhaseSwitchingRoute, func(d generatedruntimestate.Deployment) bool { return d.PreviousActiveDeploymentID != "" }); waitErr != nil {
			return result, waitErr
		}
	}
	return result, err
}

func controllerProcessKillBuild(r runtimeprocess.CommandRequest) bool {
	return len(r.Args) >= 2 && r.Args[0] == "buildx" && r.Args[1] == "build"
}
func controllerProcessKillStart(r runtimeprocess.CommandRequest) bool {
	return len(r.Args) >= 2 && r.Args[0] == "container" && r.Args[1] == "start"
}
func controllerProcessKillDrain(r runtimeprocess.CommandRequest) bool {
	return len(r.Args) >= 2 && r.Args[0] == "container" && r.Args[1] == "stop"
}
func controllerProcessKillRouteCommit(r runtimeprocess.CommandRequest) bool {
	return len(r.Args) >= 5 && r.Args[0] == "container" && r.Args[1] == "exec" && strings.Contains(strings.Join(r.Args[2:], " "), "mv /config/active.next.json /config/active.json")
}

func controllerProcessKillHasDurableCandidate(d generatedruntimestate.Deployment) bool {
	for _, component := range d.Components {
		if component.ContainerID != "" && component.State == generatedruntimestate.ComponentRunning {
			return true
		}
	}
	return false
}

type controllerProcessKillExecutor struct {
	delegate jobs.Executor
	barrier  *controllerProcessKillBarrier
}

func (e controllerProcessKillExecutor) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if e.delegate == nil {
		return jobs.ExecutionResult{}, errors.New("process-kill executor delegate is required")
	}
	return e.delegate.Execute(ctx, job, controllerProcessKillReporter{delegate: reporter, barrier: e.barrier})
}

type controllerProcessKillReporter struct {
	delegate jobs.ProgressReporter
	barrier  *controllerProcessKillBarrier
}

func (r controllerProcessKillReporter) Report(update jobs.ProgressUpdate) error {
	if err := r.delegate.Report(update); err != nil {
		return err
	}
	if r.barrier != nil && update.Status == jobs.Running && update.Phase == "finalize" {
		return r.barrier.wait(context.Background(), generatedruntimestate.PhaseSucceeded, func(generatedruntimestate.Deployment) bool { return true })
	}
	return nil
}

func controllerProcessKillMarker(path string, boundary controllerProcessKillBoundary) error {
	if filepath.Base(path) != "ready" {
		return errors.New("process-kill marker has unexpected name")
	}
	data, err := json.Marshal(struct {
		Boundary controllerProcessKillBoundary `json:"boundary"`
	}{boundary})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// The live parent creates the deployment and invokes this exact child test
// with a fixture manifest. Keeping the child name stable makes -test.run an
// auditable process boundary.
func TestLiveControllerGeneratedProcessKillRecovery(t *testing.T) {
	if os.Getenv("RIG_LIVE_PROCESS_KILL_CHILD") == "block" {
		marker := os.Getenv("RIG_LIVE_PROCESS_KILL_MARKER")
		if marker == "" || controllerProcessKillMarker(marker, controllerKillBeforeBuild) != nil {
			os.Exit(2)
		}
		select {}
	}
	if os.Getenv("RIG_LIVE_PROCESS_KILL_CHILD") != "1" {
		return
	}
	manifest := os.Getenv("RIG_LIVE_PROCESS_KILL_MANIFEST")
	if manifest == "" {
		t.Fatal("process-kill child requires a fixture manifest")
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal("process-kill fixture manifest unavailable")
	}
	// The parent-owned journey supplies the production composition and worker.
	// This guard keeps accidental direct invocation from looking like coverage.
	if os.Getenv("RIG_RUN_LIVE_CONTROLLER_JOURNEY") != "1" {
		t.Fatal("process-kill child requires the live controller journey")
	}
}

// controllerProcessKillChild starts a fresh go-test process with an exact
// test selector. The parent owns the marker directory and kills only this
// child after a barrier has recorded durable state. It is shared by the live
// journey parent and keeps process management out of production code.
func controllerProcessKillChild(t *testing.T, marker string, environment []string) *exec.Cmd {
	t.Helper()
	if filepath.Base(marker) != "ready" {
		t.Fatal("process-kill marker name is unsafe")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLiveControllerGeneratedProcessKillRecovery$")
	command.Env = append(append([]string(nil), os.Environ()...), environment...)
	command.Env = append(command.Env, "RIG_LIVE_PROCESS_KILL_MARKER="+marker)
	if err := command.Start(); err != nil {
		t.Fatal("start process-kill child")
	}
	return command
}

func controllerProcessKillWaitMarker(t *testing.T, marker string, child *exec.Cmd) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-deadline.C:
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
			t.Fatal("process-kill child did not reach barrier")
		case <-tick.C:
		}
	}
}

func controllerProcessKillTerminate(t *testing.T, child *exec.Cmd) {
	t.Helper()
	if err := child.Process.Kill(); err != nil {
		t.Fatal("SIGKILL process-kill child")
	}
	if err := child.Wait(); err == nil {
		t.Fatal("process-kill child exited cleanly after kill")
	}
}
