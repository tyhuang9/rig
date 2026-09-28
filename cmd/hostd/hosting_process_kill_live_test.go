//go:build live_docker

package main

// This file deliberately contains only test-process instrumentation.  The
// production composition receives the same CommandRunner and jobs.Executor it
// normally would; the wrappers stop a child test binary only after the durable
// database state proves that its named crash boundary has been crossed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/sourceconnections"
)

type controllerProcessKillBoundary string

const (
	controllerKillBeforeBuild  controllerProcessKillBoundary = "before_build"
	controllerKillBeforeStart  controllerProcessKillBoundary = "before_candidate_start"
	controllerKillAfterRoute   controllerProcessKillBoundary = "after_route_commit"
	controllerKillBeforeDrain  controllerProcessKillBoundary = "before_drain_stop"
	controllerKillAfterSuccess controllerProcessKillBoundary = "after_runtime_success"
	controllerKillAfterMain    controllerProcessKillBoundary = "after_main_success"
)

// controllerProcessKillBarrier is file-backed so that the parent test can
// observe a separately executed go-test child without passing command output
// (which could carry a runtime secret) across the boundary.
type controllerProcessKillBarrier struct {
	boundary     controllerProcessKillBoundary
	marker       string
	state        *generatedruntimestate.Repository
	db           *sql.DB
	appID        string
	jobID        string
	mu           sync.Mutex
	deploymentID string
	once         sync.Once
}

func (b *controllerProcessKillBarrier) wait(ctx context.Context, want generatedruntimestate.Phase, predicate func(generatedruntimestate.Deployment) bool) error {
	if b == nil || b.state == nil || b.db == nil || b.marker == "" || b.appID == "" || b.jobID == "" {
		return errors.New("process-kill barrier is incomplete")
	}
	deploymentID := b.resolveDeploymentID(ctx)
	runtime, err := b.state.Get(ctx, b.appID, deploymentID)
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

func (b *controllerProcessKillBarrier) ready(ctx context.Context, want generatedruntimestate.Phase) bool {
	if b == nil || b.db == nil || b.state == nil {
		return false
	}
	deploymentID := b.resolveDeploymentID(ctx)
	if deploymentID == "" {
		return false
	}
	value, err := b.state.Get(ctx, b.appID, deploymentID)
	return err == nil && value.Phase == want
}

func (b *controllerProcessKillBarrier) resolveDeploymentID(ctx context.Context) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deploymentID == "" {
		_ = b.db.QueryRowContext(ctx, `SELECT id FROM deployments WHERE app_id=? AND job_id=?`, b.appID, b.jobID).Scan(&b.deploymentID)
	}
	return b.deploymentID
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
		if r.barrier.boundary == controllerKillBeforeBuild && controllerProcessKillBuild(request) && r.barrier.ready(ctx, generatedruntimestate.PhaseBuilding) {
			if err := r.barrier.wait(ctx, generatedruntimestate.PhaseBuilding, func(generatedruntimestate.Deployment) bool { return true }); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
		if r.barrier.boundary == controllerKillBeforeStart && controllerProcessKillStart(request) && r.barrier.ready(ctx, generatedruntimestate.PhaseStartingCandidate) {
			if err := r.barrier.wait(ctx, generatedruntimestate.PhaseStartingCandidate, controllerProcessKillHasDurableCandidate); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
		if r.barrier.boundary == controllerKillBeforeDrain && controllerProcessKillDrain(request) && r.barrier.ready(ctx, generatedruntimestate.PhaseDraining) {
			if err := r.barrier.wait(ctx, generatedruntimestate.PhaseDraining, func(d generatedruntimestate.Deployment) bool { return d.CandidateSlot != d.PreviousActiveSlot }); err != nil {
				return runtimeprocess.CommandResult{}, err
			}
		}
	}
	result, err := r.delegate.Run(ctx, request)
	if err == nil && r.barrier != nil && r.barrier.boundary == controllerKillAfterRoute && controllerProcessKillRouteCommit(request) && r.barrier.ready(ctx, generatedruntimestate.PhaseSwitchingRoute) {
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

func TestControllerProcessKillBoundaryControls(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		match func(runtimeprocess.CommandRequest) bool
		want  bool
	}{
		{"build", []string{"buildx", "build", "--builder", "owned"}, controllerProcessKillBuild, true},
		{"unrelated build", []string{"image", "build"}, controllerProcessKillBuild, false},
		{"candidate start", []string{"container", "start", "candidate"}, controllerProcessKillStart, true},
		{"unrelated create", []string{"container", "create", "candidate"}, controllerProcessKillStart, false},
		{"previous drain", []string{"container", "stop", "--time", "0", "previous"}, controllerProcessKillDrain, true},
		{"unrelated remove", []string{"container", "rm", "previous"}, controllerProcessKillDrain, false},
		{"route commit", []string{"container", "exec", "--user", "0:0", "rig-generated-caddy-v1", "mv", "/config/active.next.json", "/config/active.json"}, controllerProcessKillRouteCommit, true},
		{"route reload", []string{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload"}, controllerProcessKillRouteCommit, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.match(runtimeprocess.CommandRequest{Args: test.args}); got != test.want {
				t.Fatalf("command boundary match=%t want=%t", got, test.want)
			}
		})
	}
	marker := filepath.Join(t.TempDir(), "ready")
	if err := controllerProcessKillMarker(marker, controllerKillAfterRoute); err != nil {
		t.Fatal(err)
	}
	if err := controllerProcessKillMarker(marker, controllerKillAfterRoute); err == nil {
		t.Fatal("process-kill marker was overwritten")
	}
	body, err := os.ReadFile(marker)
	var signal struct {
		Boundary controllerProcessKillBoundary `json:"boundary"`
	}
	if err != nil || json.Unmarshal(body, &signal) != nil || signal.Boundary != controllerKillAfterRoute {
		t.Fatal("process-kill marker did not preserve the exact boundary")
	}
}

type controllerProcessKillExecutor struct {
	delegate jobs.Executor
	barrier  *controllerProcessKillBarrier
}

func (e controllerProcessKillExecutor) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if e.delegate == nil {
		return jobs.ExecutionResult{}, errors.New("process-kill executor delegate is required")
	}
	result, err := e.delegate.Execute(ctx, job, controllerProcessKillReporter{delegate: reporter, barrier: e.barrier})
	if err == nil && e.barrier != nil && e.barrier.boundary == controllerKillAfterMain && result.Disposition == jobs.ExecutionCompleted {
		var status string
		queryErr := e.barrier.db.QueryRowContext(ctx, `SELECT status FROM deployments WHERE app_id=? AND job_id=?`, e.barrier.appID, e.barrier.jobID).Scan(&status)
		if queryErr != nil || status != "succeeded" {
			return jobs.ExecutionResult{}, errors.New("process-kill main completion is not durable")
		}
		if waitErr := e.barrier.wait(ctx, generatedruntimestate.PhaseSucceeded, func(generatedruntimestate.Deployment) bool { return true }); waitErr != nil {
			return jobs.ExecutionResult{}, waitErr
		}
	}
	return result, err
}

type controllerProcessKillReporter struct {
	delegate jobs.ProgressReporter
	barrier  *controllerProcessKillBarrier
}

func (r controllerProcessKillReporter) Report(update jobs.ProgressUpdate) error {
	if err := r.delegate.Report(update); err != nil {
		return err
	}
	if r.barrier != nil && r.barrier.boundary == controllerKillAfterSuccess && update.Status == jobs.Running && update.Phase == "finalize" {
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
	// Publish only after the full marker is durable to readers. Linking the
	// completed file preserves O_EXCL behavior if a marker already exists.
	staged := path + ".next"
	f, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(staged, path)
}

type controllerProcessKillManifest struct {
	DataRoot string                        `json:"dataRoot"`
	Source   string                        `json:"source"`
	Docker   string                        `json:"docker"`
	AppID    string                        `json:"appId"`
	JobID    string                        `json:"jobId"`
	Boundary controllerProcessKillBoundary `json:"boundary"`
}

// The child executes production composition and the durable worker after
// reopening the retained controller data root.
func TestLiveControllerGeneratedProcessKillRecovery(t *testing.T) {
	if os.Getenv("RIG_LIVE_PROCESS_KILL_CHILD") == "1" {
		controllerProcessKillRunChild(t)
		return
	}
	controllerJourneyRun(t, true)
}

func controllerProcessKillRunChild(t *testing.T) {
	path := os.Getenv("RIG_LIVE_PROCESS_KILL_MANIFEST")
	marker := os.Getenv("RIG_LIVE_PROCESS_KILL_MARKER")
	if path == "" || marker == "" || filepath.Base(marker) != "ready" {
		t.Fatal("process-kill child manifest or marker unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		t.Fatal("process-kill child requires a fixture manifest")
	}
	var manifest controllerProcessKillManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.DataRoot == "" || manifest.Source == "" || manifest.Docker == "" ||
		uuid.Validate(manifest.AppID) != nil || uuid.Validate(manifest.JobID) != nil {
		t.Fatal("process-kill child manifest invalid")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("process-kill child requires local Linux Docker")
	}
	db, err := database.Open(manifest.DataRoot)
	if err != nil {
		t.Fatal("open retained controller database")
	}
	defer db.Close()
	provider := controllerJourneyNewGitHubProvider(t, manifest.Source)
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(manifest.DataRoot), "fixture-app", time.Now)
	snapshots, err := releasesnapshot.New(db, sources, manifest.DataRoot)
	if err != nil {
		t.Fatal("reopen immutable release materializer")
	}
	configuration, err := appconfig.New(db, manifest.DataRoot)
	if err != nil {
		t.Fatal("reopen scoped configuration")
	}
	plans, err := deploymentplans.New(db, manifest.DataRoot)
	if err != nil {
		t.Fatal("reopen deployment plans")
	}
	deploymentStore := deployments.New(db)
	jobStore := jobs.New(db)
	settings := config.Defaults()
	settings.DataRoot = manifest.DataRoot
	settings.GeneratedRuntime = true
	childContext, cancel := context.WithTimeout(context.Background(), 46*time.Minute)
	defer cancel()
	var barrier *controllerProcessKillBarrier
	if manifest.Boundary != "" {
		barrier = &controllerProcessKillBarrier{
			boundary: manifest.Boundary, marker: marker, state: generatedruntimestate.New(db),
			db: db, appID: manifest.AppID, jobID: manifest.JobID,
		}
	}
	composition, err := prepareRuntimeComposition(childContext, settings, runtimeCompositionDependencies{
		db: db, applications: apps.New(db), snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans,
	}, runtimeCompositionOptions{dockerExecutable: manifest.Docker, runner: controllerProcessKillRunner{delegate: runtimeprocess.ExecRunner{}, barrier: barrier}})
	if err != nil {
		t.Fatal("reopen production generated composition")
	}
	executor := jobs.Executor(composition.executor)
	if barrier != nil {
		executor = controllerProcessKillExecutor{delegate: executor, barrier: barrier}
	}
	done, err := prepareRuntimeWorker(childContext, runtimeRecovery{
		deployments: deploymentStore.Recover, jobs: jobStore.RecoverInterrupted,
	}, executor, jobStore.RunWorker, func(error) {})
	if err != nil {
		t.Fatal("recover production deployment worker")
	}
	select {
	case <-done:
		t.Fatal("process-kill child worker stopped before parent termination")
	case <-childContext.Done():
		t.Fatal("process-kill child exceeded runtime bound")
	}
}

func controllerProcessKillMatrix(
	t *testing.T, ctx context.Context, docker, source, dataRoot, appID string,
	plan apicontract.DeploymentPlanRevision, configuration apicontract.ApplicationConfiguration,
	entries []apicontract.ScopedConfigurationValueInput,
	ingress *generatedingress.Manager, jobStore *jobs.Service, db *sql.DB,
	request func(string, string, any, int, any, ...map[string]string) []byte,
) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Fatal("process-kill matrix requires SIGKILL on Linux")
	}
	state := generatedruntimestate.New(db)
	previousMarker := "controller-journey"
	for _, boundary := range []controllerProcessKillBoundary{
		controllerKillBeforeBuild, controllerKillBeforeStart, controllerKillAfterRoute,
		controllerKillBeforeDrain, controllerKillAfterSuccess, controllerKillAfterMain,
	} {
		t.Run(string(boundary), func(t *testing.T) {
			previous, err := state.Active(ctx, appID)
			if err != nil || previous.DeploymentID == "" {
				t.Fatal("previous immutable serving head unavailable")
			}
			candidateMarker := "controller-" + string(boundary)
			updatedEntries := append([]apicontract.ScopedConfigurationValueInput(nil), entries...)
			for index := range updatedEntries {
				switch updatedEntries[index].Key {
				case "API_RUNTIME_MARKER", "VITE_BUILD_LABEL":
					updatedEntries[index].Value = candidateMarker
				}
			}
			var nextConfiguration apicontract.ApplicationConfiguration
			request(http.MethodPut, "/api/v1/apps/"+appID+"/scoped-configuration", apicontract.ReplaceScopedApplicationConfigurationRequest{
				ExpectedRevisionNumber: configuration.RevisionNumber,
				PlanRevisionID:         plan.RevisionID, PlanRevisionNumber: plan.RevisionNumber,
				PublicBuildDisclosureAcknowledged: true, Entries: updatedEntries,
			}, http.StatusOK, &nextConfiguration)
			if nextConfiguration.RevisionNumber != configuration.RevisionNumber+1 || nextConfiguration.RevisionID == configuration.RevisionID {
				t.Fatal("replacement did not create a reviewed immutable configuration revision")
			}
			configuration = nextConfiguration
			var mutation apicontract.JobMutationResponse
			request(http.MethodPost, "/api/v1/apps/"+appID+"/deployments", apicontract.DeployApplicationRequest{
				ExpectedPlanRevisionID: plan.RevisionID, ExpectedPlanRevisionNumber: plan.RevisionNumber,
				ExpectedConfigurationRevisionID: configuration.RevisionID, ExpectedConfigurationRevisionNumber: configuration.RevisionNumber,
			}, http.StatusAccepted, &mutation, map[string]string{"Idempotency-Key": uuid.NewString()})
			if !mutation.Created || mutation.Job.ID == "" || mutation.Job.ResourceID != appID {
				t.Fatal("process-kill replacement was not an exact accepted deployment job")
			}
			folder := t.TempDir()
			marker := filepath.Join(folder, "ready")
			manifestPath := filepath.Join(folder, "manifest.json")
			manifest := controllerProcessKillManifest{DataRoot: dataRoot, Source: source, Docker: docker, AppID: appID, JobID: mutation.Job.ID, Boundary: boundary}
			encoded, err := json.Marshal(manifest)
			if err != nil || os.WriteFile(manifestPath, encoded, 0o600) != nil {
				t.Fatal("write non-secret process-kill child manifest")
			}
			child := controllerProcessKillChild(t, marker, []string{
				"RIG_LIVE_PROCESS_KILL_CHILD=1", "RIG_LIVE_PROCESS_KILL_MANIFEST=" + manifestPath,
			})
			t.Cleanup(func() { controllerProcessKillCleanupChild(child) })
			controllerProcessKillWaitMarker(t, marker, child)
			body, err := os.ReadFile(marker)
			var signal struct {
				Boundary controllerProcessKillBoundary `json:"boundary"`
			}
			if err != nil || json.Unmarshal(body, &signal) != nil || signal.Boundary != boundary {
				t.Fatal("process-kill child signaled the wrong durable boundary")
			}
			before, err := jobStore.Get(mutation.Job.ID)
			if err != nil || before.Status != string(jobs.Running) || before.Attempt != 1 {
				t.Fatal("original deployment job was not running at kill boundary")
			}
			pinned := controllerProcessKillPinsForJob(t, db, appID, mutation.Job.ID)
			if pinned.PlanID != plan.RevisionID || pinned.PlanNumber != plan.RevisionNumber ||
				pinned.ConfigurationID != configuration.RevisionID || pinned.ConfigurationNumber != configuration.RevisionNumber ||
				pinned.ReleaseID == previous.ReleaseID {
				t.Fatal("process-kill replacement did not pin the reviewed plan, scoped configuration and a new immutable release")
			}
			var releaseCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM releases WHERE app_id=?`, appID).Scan(&releaseCount); err != nil {
				t.Fatal("count immutable releases before controller kill")
			}
			phase, err := state.Get(ctx, appID, pinned.DeploymentID)
			if err != nil || phase.ReleaseID != pinned.ReleaseID || phase.DeploymentPlanRevisionID != plan.RevisionID ||
				phase.DeploymentPlanRevisionNumber != plan.RevisionNumber {
				t.Fatal("kill boundary lost immutable deployment provenance")
			}
			oldRuntime, err := state.Get(ctx, appID, previous.DeploymentID)
			if err != nil {
				t.Fatal("read previous serving runtime before controller kill")
			}
			componentIDs := make(map[string]string, len(phase.Components))
			for _, component := range phase.Components {
				if component.ContainerID != "" {
					componentIDs[component.Name] = component.ContainerID
				}
			}
			controllerProcessKillTerminate(t, child)
			expectedRoute := oldRuntime
			if boundary != controllerKillBeforeBuild && boundary != controllerKillBeforeStart {
				expectedRoute = phase
			}
			controllerProcessKillAssertRoute(t, ctx, ingress, appID, expectedRoute)
			expectedMarker := candidateMarker
			if boundary == controllerKillBeforeBuild || boundary == controllerKillBeforeStart {
				expectedMarker = previousMarker
			}
			controllerJourneyRoutedRequest(t, ctx, appID, http.MethodGet, "/api/version", "", http.StatusOK, expectedMarker)
			if boundary == controllerKillAfterRoute || boundary == controllerKillBeforeDrain {
				for _, component := range oldRuntime.Components {
					controllerProcessKillAssertRunningContainer(t, ctx, docker, component)
				}
				for _, component := range phase.Components {
					controllerProcessKillAssertRunningContainer(t, ctx, docker, component)
				}
			}
			controllerJourneyRoutedRequest(t, ctx, appID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
			if boundary == controllerKillBeforeBuild || boundary == controllerKillBeforeStart {
				stillPrevious, err := state.Active(ctx, appID)
				if err != nil || stillPrevious.DeploymentID != previous.DeploymentID || stillPrevious.Generation != previous.Generation {
					t.Fatal("pre-route process kill changed the active serving head")
				}
			}
			manifest.Boundary = ""
			encoded, err = json.Marshal(manifest)
			if err != nil || os.WriteFile(manifestPath, encoded, 0o600) != nil {
				t.Fatal("write restart manifest")
			}
			restarted := controllerProcessKillChild(t, marker, []string{
				"RIG_LIVE_PROCESS_KILL_CHILD=1", "RIG_LIVE_PROCESS_KILL_MANIFEST=" + manifestPath,
			})
			t.Cleanup(func() { controllerProcessKillCleanupChild(restarted) })
			deadline := time.Now().Add(8 * time.Minute)
			resumed := false
			for {
				select {
				case <-restarted.done:
					t.Fatalf("restarted controller child exited before job convergence, exit code %d", restarted.command.ProcessState.ExitCode())
				default:
				}
				current, getErr := jobStore.Get(mutation.Job.ID)
				if getErr != nil {
					t.Fatal("read original job during process restart")
				}
				if current.Status == string(jobs.Succeeded) {
					if current.Attempt != before.Attempt+1 || string(current.Input) != string(before.Input) || current.ID != before.ID {
						t.Fatal("restart changed the original immutable job or attempt")
					}
					break
				}
				if current.Status == string(jobs.WaitingUser) && current.PauseDisposition == jobs.PauseRouteReconciliationRequired && !resumed {
					var response apicontract.JobResponse
					request(http.MethodPost, "/api/v1/jobs/"+current.ID+"/resume", nil, http.StatusOK, &response)
					if response.Job.ID != current.ID {
						t.Fatal("authenticated route reconciliation resumed the wrong job")
					}
					resumed = true
				} else if current.Status == string(jobs.Failed) || current.Status == string(jobs.Interrupted) || current.Status == string(jobs.NeedsAttention) ||
					(current.Status == string(jobs.WaitingUser) && current.PauseDisposition != jobs.PauseRouteReconciliationRequired) || time.Now().After(deadline) {
					t.Fatalf("process restart did not converge original job: status=%s phase=%s code=%s", current.Status, current.Phase, current.ErrorCode)
				}
				time.Sleep(250 * time.Millisecond)
			}
			controllerProcessKillTerminate(t, restarted)
			finalPins := controllerProcessKillPinsForJob(t, db, appID, mutation.Job.ID)
			if finalPins != pinned {
				t.Fatal("process restart changed deployment, release, plan or scoped configuration pins")
			}
			var finalReleaseCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM releases WHERE app_id=?`, appID).Scan(&finalReleaseCount); err != nil || finalReleaseCount != releaseCount {
				t.Fatal("process restart duplicated an immutable release")
			}
			finalHead, err := state.Active(ctx, appID)
			if err != nil || finalHead.DeploymentID != pinned.DeploymentID || finalHead.ReleaseID != pinned.ReleaseID || finalHead.Generation != previous.Generation+1 {
				t.Fatal("process restart did not advance exactly one active serving head")
			}
			finalRuntime, err := state.Get(ctx, appID, pinned.DeploymentID)
			if err != nil || finalRuntime.Phase != generatedruntimestate.PhaseSucceeded || len(finalRuntime.Components) == 0 {
				t.Fatal("process restart did not commit the exact runtime success")
			}
			for _, component := range finalRuntime.Components {
				if component.State != generatedruntimestate.ComponentActive || component.ContainerID == "" ||
					(componentIDs[component.Name] != "" && componentIDs[component.Name] != component.ContainerID) ||
					!controllerJourneyExists(t, ctx, docker, "container", component.ContainerName) {
					t.Fatal("process restart replaced or lost an active candidate identity")
				}
				controllerProcessKillAssertRunningContainer(t, ctx, docker, component)
			}
			controllerProcessKillAssertRoute(t, ctx, ingress, appID, finalRuntime)
			previousRuntime, err := state.Get(ctx, appID, previous.DeploymentID)
			if err != nil {
				t.Fatal("read previous immutable runtime after drain")
			}
			for _, component := range previousRuntime.Components {
				if component.State != generatedruntimestate.ComponentStopped || controllerJourneyExists(t, ctx, docker, "container", component.ContainerName) {
					t.Fatal("process restart retained or removed the wrong serving slot")
				}
			}
			controllerJourneyRoutedRequest(t, ctx, appID, http.MethodGet, "/api/notes", "", http.StatusOK, "controller TLS note")
			controllerJourneyRoutedRequest(t, ctx, appID, http.MethodGet, "/api/version", "", http.StatusOK, candidateMarker)
			previousMarker = candidateMarker
			t.Logf("controller SIGKILL recovery boundary=%s job=%s deployment=%s release=%s prior=%s active=%s", boundary, mutation.Job.ID, pinned.DeploymentID, pinned.ReleaseID, previous.DeploymentID, finalHead.DeploymentID)
		})
	}
}

type controllerProcessKillPins struct {
	DeploymentID        string
	ReleaseID           string
	PlanID              string
	PlanNumber          int64
	ConfigurationID     string
	ConfigurationNumber int64
}

func controllerProcessKillAssertRunningContainer(t *testing.T, ctx context.Context, docker string, component generatedruntimestate.Component) {
	t.Helper()
	if component.ContainerName == "" || component.ContainerID == "" {
		t.Fatal("process-kill component has no immutable Docker identity")
	}
	output, err := controllerJourneyDocker(ctx, docker, nil, "container", "inspect", "--format", "{{.Id}} {{.State.Running}}", component.ContainerName)
	if err != nil || strings.TrimSpace(string(output)) != component.ContainerID+" true" {
		t.Fatal("process-kill serving container is missing, stopped or has a different identity")
	}
}

func controllerProcessKillAssertRoute(t *testing.T, ctx context.Context, ingress *generatedingress.Manager, appID string, expected generatedruntimestate.Deployment) {
	t.Helper()
	observed, err := ingress.Observe(ctx, appID)
	if err != nil || observed.Slot != generatedruntime.Slot(expected.CandidateSlot) || len(observed.Endpoints) != len(expected.Components) {
		t.Fatal("live Caddy route does not attest the expected immutable serving slot")
	}
	identities := make(map[string]string, len(expected.Components))
	for _, component := range expected.Components {
		if component.ContainerID == "" {
			t.Fatal("expected serving component has no immutable container ID")
		}
		identities[component.Name] = component.ContainerID
	}
	for _, endpoint := range observed.Endpoints {
		if identities[endpoint.Component] == "" || identities[endpoint.Component] != endpoint.ContainerID {
			t.Fatal("live Caddy endpoint does not match the expected container identity")
		}
		delete(identities, endpoint.Component)
	}
	if len(identities) != 0 {
		t.Fatal("live Caddy route omitted an immutable serving component")
	}
}

func controllerProcessKillPinsForJob(t *testing.T, db *sql.DB, appID, jobID string) controllerProcessKillPins {
	t.Helper()
	var value controllerProcessKillPins
	err := db.QueryRow(`SELECT id,COALESCE(release_id,''),COALESCE(deployment_plan_revision_id,''),deployment_plan_revision_number,
		COALESCE(actual_configuration_revision_id,''),actual_configuration_revision_number FROM deployments WHERE app_id=? AND job_id=?`, appID, jobID).
		Scan(&value.DeploymentID, &value.ReleaseID, &value.PlanID, &value.PlanNumber, &value.ConfigurationID, &value.ConfigurationNumber)
	if err != nil || value.DeploymentID == "" || value.ReleaseID == "" || value.PlanID == "" {
		t.Fatal("read immutable process-kill deployment pins")
	}
	return value
}

type controllerProcessKillProcess struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
}

func controllerProcessKillCleanupChild(child *controllerProcessKillProcess) {
	if child == nil {
		return
	}
	select {
	case <-child.done:
	default:
		_ = child.command.Process.Kill()
		<-child.done
	}
}

// controllerProcessKillChild starts a fresh go-test process with an exact
// test selector. The parent owns the marker directory and kills only this
// child after a barrier has recorded durable state. It is shared by the live
// journey parent and keeps process management out of production code.
func controllerProcessKillChild(t *testing.T, marker string, environment []string) *controllerProcessKillProcess {
	t.Helper()
	if filepath.Base(marker) != "ready" {
		t.Fatal("process-kill marker name is unsafe")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLiveControllerGeneratedProcessKillRecovery$", "-test.timeout=48m")
	command.Env = append(append([]string(nil), os.Environ()...), environment...)
	command.Env = append(command.Env, "RIG_LIVE_PROCESS_KILL_MARKER="+marker)
	if err := command.Start(); err != nil {
		t.Fatal("start process-kill child")
	}
	child := &controllerProcessKillProcess{command: command, done: make(chan struct{})}
	go func() {
		child.err = command.Wait()
		close(child.done)
	}()
	return child
}

func controllerProcessKillWaitMarker(t *testing.T, marker string, child *controllerProcessKillProcess) {
	t.Helper()
	deadline := time.NewTimer(7 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-child.done:
			code := child.command.ProcessState.ExitCode()
			t.Fatalf("process-kill child exited before durable barrier, exit code %d", code)
		case <-deadline.C:
			controllerProcessKillCleanupChild(child)
			t.Fatal("process-kill child did not reach barrier")
		case <-tick.C:
		}
	}
}

func controllerProcessKillTerminate(t *testing.T, child *controllerProcessKillProcess) {
	t.Helper()
	select {
	case <-child.done:
		t.Fatal("process-kill child exited before SIGKILL")
	default:
	}
	if err := child.command.Process.Kill(); err != nil {
		t.Fatal("SIGKILL process-kill child")
	}
	<-child.done
	var exit *exec.ExitError
	if !errors.As(child.err, &exit) || exit.ExitCode() != -1 {
		t.Fatal("process-kill child did not exit because of the operating-system kill")
	}
}
