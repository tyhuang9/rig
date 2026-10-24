//go:build live_docker

package main

// This file deliberately contains only test-process instrumentation.  The
// production composition receives the same CommandRunner and jobs.Executor it
// normally would; the wrappers stop a child test binary only after the durable
// database state proves that its named crash boundary has been crossed.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/config"
	"github.com/hostd/hostd/internal/controller"
	"github.com/hostd/hostd/internal/controllerowner"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deploymentplans"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/machines"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
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
	boundary               controllerProcessKillBoundary
	marker                 string
	state                  *generatedruntimestate.Repository
	db                     *sql.DB
	appID                  string
	jobID                  string
	mu                     sync.Mutex
	deploymentID           string
	candidateRouteReloaded bool
	once                   sync.Once
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

func (b *controllerProcessKillBarrier) candidateRouteCommitted(request runtimeprocess.CommandRequest) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if controllerProcessKillRouteReload(request) {
		b.candidateRouteReloaded = controllerProcessKillCandidateRouteReload(request)
		return false
	}
	return b.candidateRouteReloaded && controllerProcessKillRouteCommit(request)
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
	if err == nil && r.barrier != nil && r.barrier.boundary == controllerKillAfterRoute {
		if r.barrier.candidateRouteCommitted(request) && r.barrier.ready(ctx, generatedruntimestate.PhaseSwitchingRoute) {
			if waitErr := r.barrier.wait(ctx, generatedruntimestate.PhaseSwitchingRoute, func(d generatedruntimestate.Deployment) bool { return d.PreviousActiveDeploymentID != "" }); waitErr != nil {
				return result, waitErr
			}
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
func controllerProcessKillRouteReload(r runtimeprocess.CommandRequest) bool {
	return len(r.Args) == 7 && r.Args[0] == "container" && r.Args[1] == "exec" && r.Args[2] == "rig-generated-caddy-v1" &&
		r.Args[3] == "caddy" && r.Args[4] == "reload" && r.Args[5] == "--config" && strings.HasPrefix(r.Args[6], "/config/")
}
func controllerProcessKillCandidateRouteReload(r runtimeprocess.CommandRequest) bool {
	return controllerProcessKillRouteReload(r) && r.Args[6] == "/config/proposed.json"
}
func controllerProcessKillRouteCommit(r runtimeprocess.CommandRequest) bool {
	return len(r.Args) == 8 && r.Args[0] == "container" && r.Args[1] == "exec" && r.Args[2] == "--user" && r.Args[3] == "0:0" &&
		r.Args[4] == "rig-generated-caddy-v1" && r.Args[5] == "mv" && r.Args[6] == "/config/active.next.json" && r.Args[7] == "/config/active.json"
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
		{"candidate route reload", []string{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/proposed.json"}, controllerProcessKillCandidateRouteReload, true},
		{"recovery route reload", []string{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/recovery.json"}, controllerProcessKillCandidateRouteReload, false},
		{"route commit", []string{"container", "exec", "--user", "0:0", "rig-generated-caddy-v1", "mv", "/config/active.next.json", "/config/active.json"}, controllerProcessKillRouteCommit, true},
		{"route reload is not commit", []string{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/proposed.json"}, controllerProcessKillRouteCommit, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.match(runtimeprocess.CommandRequest{Args: test.args}); got != test.want {
				t.Fatalf("command boundary match=%t want=%t", got, test.want)
			}
		})
	}
	barrier := &controllerProcessKillBarrier{}
	routeCommit := runtimeprocess.CommandRequest{Args: []string{"container", "exec", "--user", "0:0", "rig-generated-caddy-v1", "mv", "/config/active.next.json", "/config/active.json"}}
	candidateReload := runtimeprocess.CommandRequest{Args: []string{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/proposed.json"}}
	recoveryReload := runtimeprocess.CommandRequest{Args: []string{"container", "exec", "rig-generated-caddy-v1", "caddy", "reload", "--config", "/config/recovery.json"}}
	if barrier.candidateRouteCommitted(routeCommit) || barrier.candidateRouteCommitted(candidateReload) || !barrier.candidateRouteCommitted(routeCommit) {
		t.Fatal("route boundary did not distinguish old-route recovery from the committed candidate route")
	}
	barrier = &controllerProcessKillBarrier{}
	if barrier.candidateRouteCommitted(candidateReload) || barrier.candidateRouteCommitted(recoveryReload) || barrier.candidateRouteCommitted(routeCommit) {
		t.Fatal("route boundary treated recovery after a proposed reload as a committed candidate route")
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
	DataRoot      string                        `json:"dataRoot"`
	Source        string                        `json:"source"`
	Docker        string                        `json:"docker"`
	ListenAddress string                        `json:"listenAddress"`
	APIMarker     string                        `json:"apiMarker"`
	AppID         string                        `json:"appId"`
	JobID         string                        `json:"jobId"`
	Boundary      controllerProcessKillBoundary `json:"boundary"`
}

type controllerChildStartupKind string
type controllerChildStartupStage string

const (
	controllerChildStartupControllerRestart controllerChildStartupKind = "controller_restart"
	controllerChildStartupProcessKill       controllerChildStartupKind = "process_kill"

	controllerChildStartupManifest              controllerChildStartupStage = "manifest"
	controllerChildStartupAddress               controllerChildStartupStage = "address"
	controllerChildStartupLocalDocker           controllerChildStartupStage = "local_docker"
	controllerChildStartupDirectories           controllerChildStartupStage = "controller_directories"
	controllerChildStartupOwnerLease            controllerChildStartupStage = "owner_lease"
	controllerChildStartupSealedDatabase        controllerChildStartupStage = "sealed_database"
	controllerChildStartupSourceMaterializer    controllerChildStartupStage = "source_materializer"
	controllerChildStartupSourceRecovery        controllerChildStartupStage = "source_recovery"
	controllerChildStartupConfiguration         controllerChildStartupStage = "configuration"
	controllerChildStartupConfigurationRecovery controllerChildStartupStage = "configuration_recovery"
	controllerChildStartupDeploymentPlans       controllerChildStartupStage = "deployment_plans"
	controllerChildStartupPlanRecovery          controllerChildStartupStage = "plan_recovery"
	controllerChildStartupDeploymentJobs        controllerChildStartupStage = "deployment_jobs"
	controllerChildStartupLocalMachine          controllerChildStartupStage = "local_machine"
	controllerChildStartupGeneratedDomain       controllerChildStartupStage = "generated_domain"
	controllerChildStartupRuntimeComposition    controllerChildStartupStage = "runtime_composition"
	controllerChildStartupCapacityPressure      controllerChildStartupStage = "capacity_pressure"
	controllerChildStartupWorkerRecovery        controllerChildStartupStage = "worker_recovery"
	controllerChildStartupAPIListener           controllerChildStartupStage = "api_listener"
	controllerChildStartupReadyProtocol         controllerChildStartupStage = "ready_protocol"

	controllerChildStartupDiagnosticMaxBytes   = 8 << 10
	controllerChildStartupDiagnosticMaxRecords = 32
	controllerChildConstructionMaxErrors       = 32
)

// controllerChildStartupFail preserves the existing fixed test failure while
// adding one private, closed diagnostic record to stderr. Child stdout remains
// uncaptured because it can contain scoped runtime data.
func controllerChildStartupFail(t *testing.T, kind controllerChildStartupKind, stage controllerChildStartupStage, message string) {
	controllerChildStartupFailWithConstruction(t, kind, stage, nil, message)
}

func controllerChildStartupFailWithConstruction(t *testing.T, kind controllerChildStartupKind,
	stage controllerChildStartupStage, construction error, message string,
) {
	t.Helper()
	record := struct {
		Message            string `json:"msg"`
		Kind               string `json:"child_kind"`
		Stage              string `json:"child_stage"`
		Result             string `json:"child_result"`
		ConstructionStage  string `json:"construction_stage,omitempty"`
		ConstructionReason string `json:"construction_reason,omitempty"`
	}{
		Message: "controller child startup failed",
		Kind:    string(kind),
		Stage:   string(stage),
		Result:  "failed",
	}
	if controllerChildStartupConstructionEligible(record.Kind, record.Stage) {
		record.ConstructionStage, record.ConstructionReason, _ = controllerChildConstructionDiagnostic(construction)
	}
	if controllerChildStartupKnownPair(record.Kind, record.Stage) {
		if encoded, err := json.Marshal(record); err == nil {
			_, _ = fmt.Fprintln(os.Stderr, string(encoded))
		}
	}
	t.Fatal(message)
}

func controllerChildConstructionDiagnostic(err error) (string, string, bool) {
	if err == nil {
		return "", "", false
	}
	var pending [controllerChildConstructionMaxErrors]error
	pending[0] = err
	read, write := 0, 1
	for read < write {
		current := pending[read]
		read++
		if current == nil {
			continue
		}
		if stage, reason, ok := controllerChildConstructionDiagnosticText(current.Error()); ok {
			return stage, reason, true
		}
		if multiple, ok := current.(interface{ Unwrap() []error }); ok {
			causes := multiple.Unwrap()
			if remaining := len(pending) - write; len(causes) > remaining {
				causes = causes[:remaining]
			}
			for _, cause := range causes {
				if cause != nil && write < len(pending) {
					pending[write] = cause
					write++
				}
			}
			continue
		}
		if single, ok := current.(interface{ Unwrap() error }); ok && write < len(pending) {
			pending[write] = single.Unwrap()
			write++
		}
	}
	return "", "", false
}

func controllerChildConstructionDiagnosticText(value string) (string, string, bool) {
	const prefix = "generated domain construction was refused: stage="
	value, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return "", "", false
	}
	stage, reason, ok := strings.Cut(value, " reason=")
	if !ok || !controllerChildConstructionDiagnosticKnownPair(stage, reason) {
		return "", "", false
	}
	return stage, reason, true
}

func controllerChildConstructionDiagnosticKnownPair(stage, reason string) bool {
	startupReason := func(value string) bool {
		switch value {
		case "input", "physical_identity", "path_identity", "database", "protected_state",
			"gateway_expectation", "docker_census", "docker_daemon", "docker_attestation",
			"docker_image", "docker_container", "docker_volume", "docker_network",
			"docker_restart_config", "docker_live_config", "docker_static_response", "stability":
			return true
		default:
			return false
		}
	}
	finalReason := func(value string) bool {
		return startupReason(value) || value == "classification" || value == "recovery" ||
			value == "disposition" || value == "process_latch" || value == "fence"
	}
	switch stage {
	case "startup_observation":
		return startupReason(reason)
	case "initial_classification":
		return reason == "input" || reason == "disposition" || reason == "recovery"
	case "stable_construction", "empty_construction":
		return reason == "recovery"
	case "stable_final":
		return reason == "state_store" || finalReason(reason)
	case "empty_reclassification":
		return reason == "classification" || startupReason(reason)
	case "empty_final_locked", "empty_final_released":
		return finalReason(reason)
	case "cold_construction":
		switch reason {
		case "input", "path_identity", "state_store", "assembly", "fence", "stability",
			"cold_accounting", "cold_group_reconstruction", "cold_builder_attestation",
			"cold_builder_identity", "cold_builder_census", "cold_builder_container_read",
			"cold_builder_network_read", "cold_builder_image_read", "cold_builder_container_policy",
			"cold_builder_network_policy", "cold_builder_image_policy", "cold_builder_correspondence",
			"cold_builder_repeated", "cold_builder_final",
			"cold_reservation", "cold_ownership", "cold_head", "cold_group_attestation",
			"cold_group_image_attestation", "cold_group_container_attestation",
			"cold_group_network_attestation", "cold_group_census", "cold_group_gateway_attestation",
			"cold_manifest", "lock", "release", "final_validation":
			return true
		default:
			return false
		}
	case "manifest":
		return reason == "manifest" || reason == "classification" || reason == "recovery" || reason == "disposition" ||
			startupReason(reason)
	case "join":
		return reason == "manifest"
	default:
		return false
	}
}

func controllerChildStartupConstructionEligible(kind, stage string) bool {
	return kind == string(controllerChildStartupControllerRestart) &&
		stage == string(controllerChildStartupGeneratedDomain) ||
		kind == string(controllerChildStartupProcessKill) &&
			stage == string(controllerChildStartupRuntimeComposition)
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
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupManifest,
			"process-kill child manifest or marker unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupManifest,
			"process-kill child requires a fixture manifest")
	}
	var manifest controllerProcessKillManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.DataRoot == "" || manifest.Source == "" || manifest.Docker == "" ||
		uuid.Validate(manifest.AppID) != nil || uuid.Validate(manifest.JobID) != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupManifest,
			"process-kill child manifest invalid")
	}
	host, port, addressErr := net.SplitHostPort(manifest.ListenAddress)
	portNumber, portErr := strconv.Atoi(port)
	if addressErr != nil || portErr != nil || host != "127.0.0.1" || portNumber < 1 || portNumber > 65535 || filepath.Base(manifest.APIMarker) != "ready" {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupAddress,
			"process-kill child requires an exact loopback API address and readiness marker")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupLocalDocker,
			"process-kill child requires local Linux Docker")
	}
	childContext, cancel := context.WithTimeout(context.Background(), 46*time.Minute)
	defer cancel()
	directories, err := runtimedocker.PrepareControllerDirectories(manifest.DataRoot)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupDirectories,
			"prepare process-kill controller owner directories")
	}
	ownerLease, err := controllerowner.Acquire(childContext, directories.WorkingDirectory)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupOwnerLease,
			"acquire process-kill controller owner")
	}
	defer func() { _ = ownerLease.Close() }()
	db, err := database.OpenForGeneratedDomain(childContext, manifest.DataRoot, ownerLease)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupSealedDatabase,
			"open retained controller database")
	}
	defer db.Close()
	provider := controllerJourneyNewGitHubProvider(t, manifest.Source)
	sources := sourceconnections.NewService(sourceconnections.NewRepository(db), provider, sourceconnections.NewFileCredentialStore(manifest.DataRoot), "fixture-app", time.Now)
	snapshots, err := releasesnapshot.New(db, sources, manifest.DataRoot)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupSourceMaterializer,
			"reopen immutable release materializer")
	}
	if err := snapshots.Recover(); err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupSourceRecovery,
			"recover immutable release workspace")
	}
	configuration, err := appconfig.New(db, manifest.DataRoot)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupConfiguration,
			"reopen scoped configuration")
	}
	if err := configuration.Recover(childContext); err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupConfigurationRecovery,
			"recover scoped configuration")
	}
	plans, err := deploymentplans.New(db, manifest.DataRoot)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupDeploymentPlans,
			"reopen deployment plans")
	}
	if err := plans.Recover(childContext); err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupPlanRecovery,
			"recover accepted deployment plans")
	}
	deploymentStore := deployments.New(db)
	jobStore, bindingStore, err := newDeploymentJobService(db)
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupDeploymentJobs,
			"reopen deployment job service")
	}
	machineStore := machines.New(db)
	if _, err := machineStore.EnsureLocal(); err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupLocalMachine,
			"reopen controller local machine")
	}
	settings := config.Defaults()
	settings.DataRoot = manifest.DataRoot
	settings.GeneratedRuntime = true
	effectsWorkingDirectory, scopedAdmission := liveDeploymentExecutionFixture(t, db, manifest.DataRoot)
	var barrier *controllerProcessKillBarrier
	if manifest.Boundary != "" {
		barrier = &controllerProcessKillBarrier{
			boundary: manifest.Boundary, marker: marker, state: generatedruntimestate.New(db),
			db: db, appID: manifest.AppID, jobID: manifest.JobID,
		}
	}
	composition, err := prepareRuntimeCompositionForTest(t, childContext, settings, runtimeCompositionDependencies{
		db: db, applications: apps.New(db), snapshots: snapshots, configuration: configuration,
		deployments: deploymentStore, plans: plans, bindings: bindingStore,
		effectsWorkingDirectory: effectsWorkingDirectory,
	}, runtimeCompositionOptions{
		dockerExecutable: manifest.Docker,
		runner:           &controllerProcessKillRunner{delegate: runtimeprocess.ExecRunner{}, barrier: barrier},
		routePauseLogger: newStructuredLogger(os.Stderr, "info"),
	})
	if err != nil {
		controllerChildStartupFailWithConstruction(t, controllerChildStartupProcessKill,
			controllerChildStartupRuntimeComposition, err, "reopen production generated composition")
	}
	executor := jobs.Executor(composition.executor)
	if barrier != nil {
		executor = controllerProcessKillExecutor{delegate: executor, barrier: barrier}
	}
	done, err := prepareRuntimeWorker(childContext, runtimeRecovery{
		recoverState: jobStore.RecoverRuntimeContext,
	}, executor, func(workerContext context.Context, executor jobs.Executor) error {
		return jobStore.RunWorkerWithScopedAdmission(workerContext, executor, scopedAdmission)
	}, func(error) {})
	if err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupWorkerRecovery,
			"recover production deployment worker")
	}
	handler := (&controller.Server{
		Auth: auth.New(db), Apps: apps.New(db), Jobs: jobStore, Machines: machineStore, Sources: sources,
		Configuration: configuration, Deployments: deploymentStore, DeploymentPlans: plans,
		GeneratedIngress: composition.ingress, GeneratedRuntimeState: composition.state,
		GeneratedRuntime: true, Caddy: true, DataRoot: manifest.DataRoot,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler()
	listener, err := net.Listen("tcp", manifest.ListenAddress)
	if err != nil || listener.Addr().String() != manifest.ListenAddress {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupAPIListener,
			"child controller could not bind the same loopback API address")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if err := controllerProcessKillMarker(manifest.APIMarker, "api_ready"); err != nil {
		controllerChildStartupFail(t, controllerChildStartupProcessKill, controllerChildStartupReadyProtocol,
			"child controller could not publish API readiness")
	}
	select {
	case <-done:
		t.Fatal("process-kill child worker stopped before parent termination")
	case <-serveDone:
		t.Fatal("process-kill child API stopped before parent termination")
	case <-childContext.Done():
		t.Fatal("process-kill child exceeded runtime bound")
	}
}

func controllerProcessKillMatrix(
	t *testing.T, ctx context.Context, docker, source, dataRoot, appID, administratorID string,
	plan apicontract.DeploymentPlanRevision, configuration apicontract.ApplicationConfiguration,
	entries []apicontract.ScopedConfigurationValueInput,
	api **httptest.Server, handler http.Handler,
	ingress *generatedingress.Manager, jobStore *jobs.Service, db *sql.DB,
	request func(string, string, any, int, any, ...map[string]string) []byte,
) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Fatal("process-kill matrix requires SIGKILL on Linux")
	}
	state := generatedruntimestate.New(db)
	previousMarker := "controller-journey"
	for index, boundary := range []controllerProcessKillBoundary{
		controllerKillBeforeBuild, controllerKillBeforeStart, controllerKillAfterRoute,
		controllerKillBeforeDrain, controllerKillAfterSuccess, controllerKillAfterMain,
	} {
		if !t.Run(string(boundary), func(t *testing.T) {
			if index > 0 {
				// The previous controller was killed after convergence. Reopen
				// the authenticated API only to accept the next immutable job;
				// the child takes over the same listener before executing it.
				server := httptest.NewServer(handler)
				*api = server
				t.Cleanup(server.Close)
			}
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
				PublicBuildDisclosureAcknowledged: true, Entries: updatedEntries, Remove: []apicontract.ScopedConfigurationKey{},
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
			apiMarker := controllerProcessKillAPIReadyPath(t, folder, "first")
			listenAddress := strings.TrimPrefix((*api).URL, "http://")
			manifest := controllerProcessKillManifest{DataRoot: dataRoot, Source: source, Docker: docker,
				ListenAddress: listenAddress, APIMarker: apiMarker,
				AppID: appID, JobID: mutation.Job.ID, Boundary: boundary}
			encoded, err := json.Marshal(manifest)
			if err != nil || os.WriteFile(manifestPath, encoded, 0o600) != nil {
				t.Fatal("write non-secret process-kill child manifest")
			}
			(*api).Close()
			child := controllerProcessKillChild(t, marker, []string{
				"RIG_LIVE_PROCESS_KILL_CHILD=1", "RIG_LIVE_PROCESS_KILL_MANIFEST=" + manifestPath,
			})
			childStopped := false
			t.Cleanup(func() {
				controllerProcessKillCleanupChild(child)
				if t.Failed() && !childStopped {
					controllerProcessKillReportClosedDiagnostic(t, child)
				}
			})
			controllerProcessKillWaitMarker(t, apiMarker, child)
			controllerProcessKillAssertAuthenticated(t, administratorID, request)
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
			childStopped = true
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
			manifest.APIMarker = controllerProcessKillAPIReadyPath(t, folder, "restarted")
			encoded, err = json.Marshal(manifest)
			if err != nil || os.WriteFile(manifestPath, encoded, 0o600) != nil {
				t.Fatal("write restart manifest")
			}
			restarted := controllerProcessKillChild(t, marker, []string{
				"RIG_LIVE_PROCESS_KILL_CHILD=1", "RIG_LIVE_PROCESS_KILL_MANIFEST=" + manifestPath,
			})
			restartedStopped := false
			t.Cleanup(func() {
				controllerProcessKillCleanupChild(restarted)
				if t.Failed() && !restartedStopped {
					controllerProcessKillReportClosedDiagnostic(t, restarted)
				}
			})
			controllerProcessKillWaitMarker(t, manifest.APIMarker, restarted)
			controllerProcessKillAssertAuthenticated(t, administratorID, request)
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
			restartedStopped = true
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
		}) {
			return
		}
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

func controllerProcessKillAPIReadyPath(t *testing.T, folder, stage string) string {
	t.Helper()
	directory := filepath.Join(folder, "api-"+stage)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal("prepare child controller API readiness directory")
	}
	return filepath.Join(directory, "ready")
}

func controllerProcessKillAssertAuthenticated(
	t *testing.T, administratorID string,
	request func(string, string, any, int, any, ...map[string]string) []byte,
) {
	t.Helper()
	var me apicontract.MeResponse
	request(http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, &me)
	if me.User.ID != administratorID || me.User.Username != "journey-admin" || me.User.Role != "administrator" {
		t.Fatal("restarted child controller did not authenticate the original administrator session")
	}
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
	if err != nil {
		var diagnostic *generatedingress.Error
		if errors.As(err, &diagnostic) {
			t.Fatalf("live Caddy route attestation failed: code=%s", diagnostic.Code)
		}
		t.Fatal("live Caddy route attestation failed: code=unknown")
	}
	if observed.Slot != generatedruntime.Slot(expected.CandidateSlot) || len(observed.Endpoints) != len(expected.Components) {
		t.Fatalf("live Caddy route differs from expected serving slot: observed_slot=%s expected_slot=%s observed_endpoints=%d expected_components=%d",
			observed.Slot, expected.CandidateSlot, len(observed.Endpoints), len(expected.Components))
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
	stderr  bytes.Buffer
}

func controllerProcessKillCapture(command *exec.Cmd) *controllerProcessKillProcess {
	child := &controllerProcessKillProcess{command: command, done: make(chan struct{})}
	command.Stderr = &child.stderr
	return child
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

func controllerProcessKillClosedDiagnostic(child *controllerProcessKillProcess) (string, bool) {
	if child == nil {
		return "", false
	}
	select {
	case <-child.done:
		stderr := child.stderr.String()
		diagnostic := liveHostdRebindClosedDiagnostic(stderr)
		if code := controllerChildStartupDiagnosticCode(stderr); code != "" {
			diagnostic += " child=" + code
		}
		return diagnostic, true
	default:
		return "", false
	}
}

func controllerChildStartupDiagnosticCode(stderr string) string {
	limited := []byte(stderr)
	if len(limited) > controllerChildStartupDiagnosticMaxBytes {
		limited = limited[len(limited)-controllerChildStartupDiagnosticMaxBytes:]
		if newline := bytes.IndexByte(limited, '\n'); newline < 0 {
			return ""
		} else {
			limited = limited[newline+1:]
		}
	}
	limited = bytes.TrimSpace(limited)
	for records := 0; len(limited) > 0 && records < controllerChildStartupDiagnosticMaxRecords; records++ {
		line := limited
		if newline := bytes.LastIndexByte(limited, '\n'); newline >= 0 {
			line, limited = limited[newline+1:], limited[:newline]
		} else {
			limited = nil
		}
		if kind, stage, constructionStage, constructionReason, ok :=
			controllerChildStartupDiagnosticRecord(bytes.TrimSpace(line)); ok {
			code := kind + "/" + stage + "/failed"
			if constructionStage != "" {
				code += " construction=" + constructionStage + "/" + constructionReason
			}
			return code
		}
	}
	return ""
}

func controllerChildStartupDiagnosticRecord(line []byte) (string, string, string, string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	opening, err := decoder.Token()
	openingDelimiter, isDelimiter := opening.(json.Delim)
	if err != nil || !isDelimiter || openingDelimiter != '{' {
		return "", "", "", "", false
	}
	var message, kind, stage, result, constructionStage, constructionReason string
	seen := make(map[string]bool, 6)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		key, isString := keyToken.(string)
		if keyErr != nil || !isString || seen[key] {
			return "", "", "", "", false
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return "", "", "", "", false
		}
		value, valid := controllerChildStartupDiagnosticString(raw)
		if !valid {
			return "", "", "", "", false
		}
		switch key {
		case "msg":
			message = value
		case "child_kind":
			kind = value
		case "child_stage":
			stage = value
		case "child_result":
			result = value
		case "construction_stage":
			constructionStage = value
		case "construction_reason":
			constructionReason = value
		default:
			return "", "", "", "", false
		}
	}
	closing, closeErr := decoder.Token()
	closingDelimiter, isDelimiter := closing.(json.Delim)
	if closeErr != nil || !isDelimiter || closingDelimiter != '}' {
		return "", "", "", "", false
	}
	var trailing json.RawMessage
	if decoder.Decode(&trailing) != io.EOF || message != "controller child startup failed" || result != "failed" ||
		!seen["msg"] || !seen["child_kind"] || !seen["child_stage"] || !seen["child_result"] ||
		!controllerChildStartupKnownPair(kind, stage) {
		return "", "", "", "", false
	}
	hasConstructionStage, hasConstructionReason := seen["construction_stage"], seen["construction_reason"]
	if hasConstructionStage != hasConstructionReason || hasConstructionStage &&
		(!controllerChildStartupConstructionEligible(kind, stage) ||
			!controllerChildConstructionDiagnosticKnownPair(constructionStage, constructionReason)) {
		return "", "", "", "", false
	}
	return kind, stage, constructionStage, constructionReason, true
}

func controllerChildStartupDiagnosticString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func controllerChildStartupKnownPair(kind, stage string) bool {
	switch controllerChildStartupKind(kind) {
	case controllerChildStartupControllerRestart:
		switch controllerChildStartupStage(stage) {
		case controllerChildStartupManifest, controllerChildStartupDirectories, controllerChildStartupOwnerLease,
			controllerChildStartupSealedDatabase, controllerChildStartupSourceMaterializer, controllerChildStartupSourceRecovery,
			controllerChildStartupConfiguration, controllerChildStartupConfigurationRecovery,
			controllerChildStartupDeploymentPlans, controllerChildStartupPlanRecovery, controllerChildStartupDeploymentJobs,
			controllerChildStartupGeneratedDomain, controllerChildStartupRuntimeComposition,
			controllerChildStartupCapacityPressure, controllerChildStartupWorkerRecovery,
			controllerChildStartupAPIListener, controllerChildStartupReadyProtocol:
			return true
		}
	case controllerChildStartupProcessKill:
		switch controllerChildStartupStage(stage) {
		case controllerChildStartupManifest, controllerChildStartupAddress, controllerChildStartupLocalDocker,
			controllerChildStartupDirectories, controllerChildStartupOwnerLease, controllerChildStartupSealedDatabase,
			controllerChildStartupSourceMaterializer, controllerChildStartupSourceRecovery,
			controllerChildStartupConfiguration, controllerChildStartupConfigurationRecovery,
			controllerChildStartupDeploymentPlans, controllerChildStartupPlanRecovery,
			controllerChildStartupDeploymentJobs, controllerChildStartupLocalMachine,
			controllerChildStartupRuntimeComposition, controllerChildStartupWorkerRecovery,
			controllerChildStartupAPIListener, controllerChildStartupReadyProtocol:
			return true
		}
	}
	return false
}

func controllerProcessKillReportClosedDiagnostic(t *testing.T, child *controllerProcessKillProcess) {
	t.Helper()
	if diagnostic, ok := controllerProcessKillClosedDiagnostic(child); ok {
		t.Logf("process-kill child diagnostic=%q", diagnostic)
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
	child := controllerProcessKillCapture(command)
	if err := command.Start(); err != nil {
		t.Fatal("start process-kill child")
	}
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
