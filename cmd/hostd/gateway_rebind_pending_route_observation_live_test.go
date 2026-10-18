//go:build live_docker

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	liveHostdRebindPendingRouteObservationCap = 10 * time.Minute
	liveHostdRebindPendingRouteStartCap       = 2 * time.Minute
	liveHostdRebindPendingRouteEventTail      = 8
)

type liveHostdRebindPendingRouteObservation struct {
	observed                 bool
	jobStatus                string
	jobPhase                 string
	jobProgress              int
	jobErrorCode             string
	deploymentFound          bool
	deploymentStatus         deployments.Status
	deploymentDiagnosticCode string
	runtimeFound             bool
	runtimePhase             generatedruntimestate.Phase
	runtimeDiagnosticCode    generatedruntimestate.DiagnosticCode
	componentStates          []generatedruntimestate.ComponentState
	componentDiagnosticCodes []generatedruntimestate.DiagnosticCode
	events                   []jobs.Event
}

type liveHostdRebindPendingRouteObserver struct {
	appID       string
	jobs        *jobs.Service
	deployments *deployments.Repository
	runtime     *generatedruntimestate.Repository
}

type liveHostdRebindPendingRouteSnapshot func(context.Context, string) (liveHostdRebindPendingRouteObservation, error)

func liveHostdRebindNewPendingRouteObserver(db *sql.DB, appID string) *liveHostdRebindPendingRouteObserver {
	return &liveHostdRebindPendingRouteObserver{
		appID: appID, jobs: jobs.New(db), deployments: deployments.New(db), runtime: generatedruntimestate.New(db),
	}
}

func (o *liveHostdRebindPendingRouteObserver) snapshot(ctx context.Context,
	jobID string,
) (liveHostdRebindPendingRouteObservation, error) {
	var empty liveHostdRebindPendingRouteObservation
	if o == nil || o.jobs == nil || o.deployments == nil || o.runtime == nil || ctx == nil || o.appID == "" || jobID == "" {
		return empty, errors.New("invalid pending route observer")
	}
	job, err := o.jobs.Get(jobID)
	if err != nil {
		return empty, errors.New("read pending route job snapshot failed")
	}
	events, err := o.jobs.Events(jobID, 0)
	if err != nil {
		return empty, errors.New("read pending route event snapshot failed")
	}
	values, err := o.deployments.List(ctx, o.appID, 200)
	if err != nil {
		return empty, errors.New("read pending route deployment snapshot failed")
	}
	observation, deploymentID, err := liveHostdRebindPendingRouteObservationFor(o.appID, jobID, job, events, values)
	if err != nil || deploymentID == "" {
		return observation, err
	}
	runtime, err := o.runtime.Get(ctx, o.appID, deploymentID)
	if errors.Is(err, generatedruntimestate.ErrNotFound) {
		return observation, nil
	}
	if err != nil {
		return empty, errors.New("read pending route runtime snapshot failed")
	}
	if runtime.AppID != o.appID || runtime.DeploymentID != deploymentID {
		return empty, errors.New("pending route runtime identity changed")
	}
	if !liveHostdRebindKnownRuntimePhase(runtime.Phase) {
		return empty, errors.New("pending route runtime phase is invalid")
	}
	observation.runtimeFound = true
	observation.runtimePhase = runtime.Phase
	observation.runtimeDiagnosticCode = runtime.DiagnosticCode
	for _, component := range runtime.Components {
		if !liveHostdRebindKnownComponentState(component.State) {
			return empty, errors.New("pending route component state is invalid")
		}
		observation.componentStates = append(observation.componentStates, component.State)
		observation.componentDiagnosticCodes = append(observation.componentDiagnosticCodes, component.DiagnosticCode)
	}
	sort.Slice(observation.componentStates, func(left, right int) bool {
		return observation.componentStates[left] < observation.componentStates[right]
	})
	return observation, nil
}

func liveHostdRebindPendingRouteObservationFor(appID, jobID string, job jobs.Job,
	events []jobs.Event, values []deployments.Deployment,
) (liveHostdRebindPendingRouteObservation, string, error) {
	var observation liveHostdRebindPendingRouteObservation
	if appID == "" || jobID == "" || job.ID != jobID || job.Type != "deploy" ||
		job.ResourceType != "application" || job.ResourceID != appID {
		return observation, "", errors.New("pending route job identity changed")
	}
	if !liveHostdRebindKnownJobStatus(job.Status) || job.Progress < 0 || job.Progress > 100 {
		return observation, "", errors.New("pending route job state is invalid")
	}
	observation = liveHostdRebindPendingRouteObservation{
		observed: true, jobStatus: job.Status, jobPhase: job.Phase, jobProgress: job.Progress,
		jobErrorCode: job.ErrorCode,
	}
	for _, event := range events {
		if event.JobID != jobID {
			return liveHostdRebindPendingRouteObservation{}, "", errors.New("pending route event identity changed")
		}
	}
	if len(events) > liveHostdRebindPendingRouteEventTail {
		events = events[len(events)-liveHostdRebindPendingRouteEventTail:]
	}
	observation.events = append([]jobs.Event(nil), events...)
	deploymentID := ""
	for _, value := range values {
		if value.JobID != jobID {
			continue
		}
		if value.ID == "" || value.AppID != appID || deploymentID != "" {
			return liveHostdRebindPendingRouteObservation{}, "", errors.New("pending route deployment identity changed")
		}
		if !liveHostdRebindKnownDeploymentStatus(value.Status) {
			return liveHostdRebindPendingRouteObservation{}, "", errors.New("pending route deployment state is invalid")
		}
		deploymentID = value.ID
		observation.deploymentFound = true
		observation.deploymentStatus = value.Status
		observation.deploymentDiagnosticCode = value.DiagnosticCode
	}
	return observation, deploymentID, nil
}

func liveHostdRebindPendingRouteObservationBudget(now time.Time, parent context.Context,
	testDeadline time.Time, hasTestDeadline bool,
) (time.Duration, error) {
	if parent == nil {
		return 0, errors.New("invalid pending route observation context")
	}
	deadlines := make([]time.Time, 0, 2)
	if hasTestDeadline {
		deadlines = append(deadlines, testDeadline)
	}
	if deadline, ok := parent.Deadline(); ok {
		deadlines = append(deadlines, deadline)
	}
	return liveHostdRebindBoundedBudget(now, liveHostdRebindPendingRouteObservationCap, deadlines...)
}

func liveHostdRebindWaitPendingRouteBoundaryResult(parent context.Context, testDeadline time.Time,
	hasTestDeadline bool, child *liveHostdRebindChild, jobID string, snapshot liveHostdRebindPendingRouteSnapshot,
) (liveHostdRebindBoundaryMarker, liveHostdRebindPendingRouteObservation, error) {
	var marker liveHostdRebindBoundaryMarker
	var last liveHostdRebindPendingRouteObservation
	if child == nil || child.command == nil || child.command.Process == nil || child.waitDone == nil ||
		child.manifest.BoundaryMarker == "" || child.manifest.OperationID == "" || jobID == "" || snapshot == nil {
		return marker, last, errors.New("invalid pending route boundary observation")
	}
	budget, err := liveHostdRebindPendingRouteObservationBudget(time.Now(), parent, testDeadline, hasTestDeadline)
	if err != nil {
		return marker, last, fmt.Errorf("reserve pending route observation: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	child.startWait()
	startedDeadline := time.Now().Add(liveHostdRebindPendingRouteStartCap)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return marker, last, fmt.Errorf("pending route boundary observation ended: %s snapshot=%s",
				liveHostdRebindFailureCategory(err), last)
		}
		marker, found, err := liveHostdRebindReadPendingRouteBoundary(child)
		if err != nil {
			return marker, last, err
		}
		if found {
			return marker, last, nil
		}
		select {
		case <-child.waitDone:
			return marker, last, fmt.Errorf("pending route child exited before publication boundary: %s snapshot=%s",
				liveHostdRebindFailureCategory(child.waitErr), last)
		default:
		}
		last, err = snapshot(ctx, jobID)
		if err != nil {
			return marker, last, errors.New("pending route durable observation failed")
		}
		if err := ctx.Err(); err != nil {
			return marker, last, fmt.Errorf("pending route boundary observation ended: %s snapshot=%s",
				liveHostdRebindFailureCategory(err), last)
		}
		// The marker may have become durable while the SQLite snapshot was read.
		// Recheck it before interpreting a terminal snapshot so publication wins
		// the only race that matters to this crash-boundary contract.
		marker, found, err = liveHostdRebindReadPendingRouteBoundary(child)
		if err != nil {
			return marker, last, err
		}
		if found {
			return marker, last, nil
		}
		select {
		case <-child.waitDone:
			return marker, last, fmt.Errorf("pending route child exited before publication boundary: %s snapshot=%s",
				liveHostdRebindFailureCategory(child.waitErr), last)
		default:
		}
		if liveHostdRebindPendingRouteTerminal(last.jobStatus) {
			return marker, last, fmt.Errorf("pending route job reached %s before publication boundary snapshot=%s",
				liveHostdRebindSafeJobStatus(last.jobStatus), last)
		}
		if last.jobStatus == string(jobs.Queued) && time.Now().After(startedDeadline) {
			return marker, last, fmt.Errorf("pending route job did not start before publication boundary snapshot=%s", last)
		}
		select {
		case <-ctx.Done():
		case <-child.waitDone:
		case <-ticker.C:
		}
	}
}

func liveHostdRebindReadPendingRouteBoundary(child *liveHostdRebindChild) (liveHostdRebindBoundaryMarker, bool, error) {
	var marker liveHostdRebindBoundaryMarker
	body, err := os.ReadFile(child.manifest.BoundaryMarker)
	if errors.Is(err, os.ErrNotExist) {
		return marker, false, nil
	}
	if err != nil {
		return marker, false, errors.New("read pending route boundary marker failed")
	}
	if len(body) == 0 || len(body) > 8192 || liveHostdRebindDecodeBoundary(body, &marker) != nil ||
		marker.Boundary != "current_effect_ack_lost" || marker.OperationID != child.manifest.OperationID {
		return liveHostdRebindBoundaryMarker{}, false, errors.New("pending route boundary marker is invalid")
	}
	return marker, true, nil
}

func liveHostdRebindDecodeBoundary(body []byte, marker *liveHostdRebindBoundaryMarker) error {
	return json.Unmarshal(body, marker)
}

func (o liveHostdRebindPendingRouteObservation) String() string {
	if !o.observed {
		return "job=unobserved job_error=none deployment=unobserved deployment_diagnostic=none runtime=unobserved runtime_diagnostic=none components=[] component_diagnostics=[] events=[]"
	}
	deployment, runtime := "absent", "absent"
	if o.deploymentFound {
		deployment = liveHostdRebindSafeDeploymentStatus(o.deploymentStatus)
	}
	if o.runtimeFound {
		runtime = liveHostdRebindSafeRuntimePhase(o.runtimePhase)
	}
	components := make([]string, 0, len(o.componentStates))
	for _, state := range o.componentStates {
		components = append(components, liveHostdRebindSafeComponentState(state))
	}
	componentDiagnostics := make([]string, 0, len(o.componentDiagnosticCodes))
	for _, code := range o.componentDiagnosticCodes {
		componentDiagnostics = append(componentDiagnostics, liveHostdRebindSafeComponentDiagnosticCode(code))
	}
	sort.Strings(componentDiagnostics)
	events := make([]string, 0, len(o.events))
	for _, event := range o.events {
		events = append(events, liveHostdRebindSafeJobPhase(event.Phase)+"/"+liveHostdRebindSafeEventCode(event.Code))
	}
	return fmt.Sprintf("job=%s phase=%s progress=%d job_error=%s deployment=%s deployment_diagnostic=%s runtime=%s runtime_diagnostic=%s components=[%s] component_diagnostics=[%s] events=[%s]",
		liveHostdRebindSafeJobStatus(o.jobStatus), liveHostdRebindSafeJobPhase(o.jobPhase), o.jobProgress,
		liveHostdRebindSafeJobErrorCode(o.jobErrorCode), deployment,
		liveHostdRebindSafeDeploymentDiagnosticCode(o.deploymentDiagnosticCode), runtime,
		liveHostdRebindSafeRuntimeDiagnosticCode(o.runtimeDiagnosticCode), strings.Join(components, ","),
		strings.Join(componentDiagnostics, ","), strings.Join(events, ","))
}

func liveHostdRebindPendingRouteTerminal(status string) bool {
	switch jobs.Status(status) {
	case jobs.WaitingUser, jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return true
	default:
		return false
	}
}

func liveHostdRebindKnownJobStatus(value string) bool {
	switch jobs.Status(value) {
	case jobs.Queued, jobs.Assigned, jobs.Running, jobs.Waiting, jobs.WaitingUser, jobs.Succeeded, jobs.Failed,
		jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeJobStatus(value string) string {
	if liveHostdRebindKnownJobStatus(value) {
		return value
	}
	return "unknown"
}

// Keep this closed to jobs.safeExecutionFailure outputs and the worker's one
// direct invalid-result code. Job error detail remains deliberately omitted.
func liveHostdRebindSafeJobErrorCode(value string) string {
	if value == "" {
		return "none"
	}
	switch value {
	case "validation_failed", "runtime_unavailable", "process_termination_failed", "invalid_source",
		"source_unavailable", "source_access_lost", "provider_unavailable", "source_too_large",
		"source_storage_full", "configuration_unavailable", "configuration_review_required",
		"reviewed_revision_stale", "reviewed_revisions_required", "build_configuration_requires_build_command",
		"compose_invalid", "compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated",
		"policy_rejected", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
		"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "internal_error",
		"executor_failed", "executor_invalid_result":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindSafeJobPhase(value string) string {
	switch value {
	case "queued", "assigned", "validate", "prepare_workspace", "materialize_release", "render_compose",
		"evaluate_policy", "apply_runtime", "apply_fake_runtime", "wait_for_health", "finalize", "running",
		"waiting_external", "cancelling", "approval_required", "migration_approval_required",
		"insufficient_replacement_capacity", "route_reconciliation_required", "succeeded", "failed", "cancelled",
		"interrupted", "needs_attention", "waiting_user":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindSafeEventCode(value string) string {
	switch value {
	case "job_queued", "job_resumed", "job_assigned", "phase_started", "waiting_external",
		"cancellation_requested", "job_cancelled", "job_failed", "job_succeeded", "approval_required",
		"migration_approval_required", "insufficient_replacement_capacity", "route_reconciliation_required":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindKnownDeploymentStatus(value deployments.Status) bool {
	switch value {
	case deployments.Preparing, deployments.Applying, deployments.WaitingHealth, deployments.Succeeded,
		deployments.Failed, deployments.Cancelled, deployments.NeedsAttention:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeDeploymentStatus(value deployments.Status) string {
	if liveHostdRebindKnownDeploymentStatus(value) {
		return string(value)
	}
	return "unknown"
}

// Keep this closed to deployments.knownDiagnostic. Failure summary remains
// deliberately omitted.
func liveHostdRebindSafeDeploymentDiagnosticCode(value string) string {
	if value == "" {
		return "none"
	}
	switch value {
	case "daemon_restarted", "runtime_unavailable", "process_termination_failed", "compose_invalid",
		"compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated", "policy_rejected",
		"approval_required", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
		"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "cancelled", "internal_error",
		"invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full",
		"provider_unavailable", "configuration_unavailable", "configuration_review_required",
		"build_configuration_requires_build_command":
		return value
	default:
		return "unknown"
	}
}

func liveHostdRebindKnownRuntimePhase(value generatedruntimestate.Phase) bool {
	switch value {
	case generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding,
		generatedruntimestate.PhaseMigrating, generatedruntimestate.PhaseStartingCandidate,
		generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded,
		generatedruntimestate.PhaseFailed, generatedruntimestate.PhaseCancelled:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeRuntimePhase(value generatedruntimestate.Phase) string {
	if liveHostdRebindKnownRuntimePhase(value) {
		return string(value)
	}
	return "unknown"
}

// Keep this closed to generatedruntimestate.knownDiagnostic.
func liveHostdRebindSafeRuntimeDiagnosticCode(value generatedruntimestate.DiagnosticCode) string {
	if value == "" {
		return "none"
	}
	switch value {
	case generatedruntimestate.DiagnosticInsufficientReplacementCapacity,
		generatedruntimestate.DiagnosticPlanReviewRequired,
		generatedruntimestate.DiagnosticMigrationFailed,
		generatedruntimestate.DiagnosticStartFailed,
		generatedruntimestate.DiagnosticHealthFailed,
		generatedruntimestate.DiagnosticRouteSwitchFailed,
		generatedruntimestate.DiagnosticRuntimeUnavailable,
		generatedruntimestate.DiagnosticDaemonRestarted,
		generatedruntimestate.DiagnosticCancelled,
		generatedruntimestate.DiagnosticInternalError:
		return string(value)
	default:
		return "unknown"
	}
}

func liveHostdRebindKnownComponentState(value generatedruntimestate.ComponentState) bool {
	switch value {
	case generatedruntimestate.ComponentPending, generatedruntimestate.ComponentImageReady,
		generatedruntimestate.ComponentStarting, generatedruntimestate.ComponentRunning,
		generatedruntimestate.ComponentHealthy, generatedruntimestate.ComponentActive,
		generatedruntimestate.ComponentDraining, generatedruntimestate.ComponentStopped,
		generatedruntimestate.ComponentFailed:
		return true
	default:
		return false
	}
}

func liveHostdRebindSafeComponentState(value generatedruntimestate.ComponentState) string {
	if liveHostdRebindKnownComponentState(value) {
		return string(value)
	}
	return "unknown"
}

// Keep this closed to generatedruntimestate.componentDiagnostic.
func liveHostdRebindSafeComponentDiagnosticCode(value generatedruntimestate.DiagnosticCode) string {
	if value == "" {
		return "none"
	}
	switch value {
	case generatedruntimestate.DiagnosticStartFailed,
		generatedruntimestate.DiagnosticHealthFailed,
		generatedruntimestate.DiagnosticRuntimeUnavailable,
		generatedruntimestate.DiagnosticDaemonRestarted,
		generatedruntimestate.DiagnosticCancelled,
		generatedruntimestate.DiagnosticInternalError:
		return string(value)
	default:
		return "unknown"
	}
}

func liveHostdRebindPendingRouteObservationControls(t *testing.T) {
	t.Helper()
	const (
		operationID = "d5228ec6-7325-458f-851c-345277d57e1c"
		jobID       = "8d13dfa0-457c-4b8b-86f2-5f30dbefcbb1"
		appID       = "a358bfec-63bd-41c4-af16-af7f264f3f00"
		secret      = "private-pending-route-control-secret"
	)
	startChild := func(t *testing.T, mode string) *liveHostdRebindChild {
		t.Helper()
		directory := t.TempDir()
		child := liveHostdRebindStartStartupControlChild(t, directory, "", mode, secret)
		child.manifest.OperationID = operationID
		child.manifest.BoundaryMarker = filepath.Join(directory, "boundary.json")
		return child
	}
	nonterminal := func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
		return liveHostdRebindPendingRouteObservation{observed: true, jobStatus: string(jobs.Running), jobPhase: "apply_runtime", jobProgress: 70}, nil
	}
	writeMarker := func(t *testing.T, child *liveHostdRebindChild, marker liveHostdRebindBoundaryMarker) {
		t.Helper()
		if err := liveHostdRebindWritePrivateJSON(child.manifest.BoundaryMarker, marker); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(child *liveHostdRebindChild, deadline time.Time, snapshot liveHostdRebindPendingRouteSnapshot) (liveHostdRebindBoundaryMarker, liveHostdRebindPendingRouteObservation, error) {
		return liveHostdRebindWaitPendingRouteBoundaryResult(context.Background(), deadline, true, child, jobID, snapshot)
	}

	t.Run("budget preserves both cleanup reserves and finite cap", func(t *testing.T) {
		now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
		for _, test := range []struct {
			name        string
			parent      context.Context
			deadline    time.Time
			hasDeadline bool
			want        time.Duration
			wantErr     bool
		}{
			{name: "finite cap", parent: context.Background(), want: liveHostdRebindPendingRouteObservationCap},
			{name: "short test deadline", parent: context.Background(), deadline: now.Add(liveHostdRebindStartupCleanupReserve + 90*time.Second), hasDeadline: true, want: 90 * time.Second},
			{name: "short fixture deadline", parent: liveHostdRebindControlDeadlineContext(t, now.Add(liveHostdRebindStartupCleanupReserve+30*time.Second)), want: 30 * time.Second},
			{name: "shortest known deadline", parent: liveHostdRebindControlDeadlineContext(t, now.Add(liveHostdRebindStartupCleanupReserve+30*time.Second)), deadline: now.Add(liveHostdRebindStartupCleanupReserve + time.Minute), hasDeadline: true, want: 30 * time.Second},
			{name: "reserve exhausted", parent: context.Background(), deadline: now.Add(liveHostdRebindStartupCleanupReserve), hasDeadline: true, wantErr: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, err := liveHostdRebindPendingRouteObservationBudget(now, test.parent, test.deadline, test.hasDeadline)
				if test.wantErr {
					if err == nil {
						t.Fatal("pending route budget accepted an exhausted cleanup reserve")
					}
					return
				}
				if err != nil || got != test.want {
					t.Fatalf("pending route budget=%s error=%v want=%s", got, err, test.want)
				}
			})
		}
	})

	t.Run("durable marker wins an actual terminal ordering race", func(t *testing.T) {
		child := startChild(t, "stall")
		reads := 0
		marker, _, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
			reads++
			writeMarker(t, child, liveHostdRebindBoundaryMarker{Boundary: "current_effect_ack_lost", OperationID: operationID})
			return liveHostdRebindPendingRouteObservation{observed: true, jobStatus: string(jobs.Failed), jobPhase: "failed", jobProgress: 70}, nil
		})
		if err != nil || marker.Boundary != "current_effect_ack_lost" || reads != 1 {
			t.Fatalf("marker ordering marker=%+v error=%v reads=%d", marker, err, reads)
		}
		liveHostdRebindKillAndReap(t, child)
	})

	t.Run("terminal job before marker fails", func(t *testing.T) {
		for _, status := range []jobs.Status{jobs.WaitingUser, jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention} {
			t.Run(string(status), func(t *testing.T) {
				child := startChild(t, "stall")
				_, _, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
					return liveHostdRebindPendingRouteObservation{
						observed: true, jobStatus: string(status), jobPhase: string(status), jobProgress: 80,
						jobErrorCode: "apply_failed", deploymentFound: true, deploymentStatus: deployments.Failed,
						deploymentDiagnosticCode: "process_termination_failed", runtimeFound: true,
						runtimePhase:          generatedruntimestate.PhaseFailed,
						runtimeDiagnosticCode: generatedruntimestate.DiagnosticStartFailed,
						componentStates:       []generatedruntimestate.ComponentState{generatedruntimestate.ComponentFailed},
						componentDiagnosticCodes: []generatedruntimestate.DiagnosticCode{
							generatedruntimestate.DiagnosticInternalError,
						},
					}, nil
				})
				diagnostic := fmt.Sprint(err)
				if err == nil || !strings.Contains(diagnostic, "before publication boundary") ||
					!strings.Contains(diagnostic, "job_error=apply_failed") ||
					!strings.Contains(diagnostic, "deployment_diagnostic=process_termination_failed") ||
					!strings.Contains(diagnostic, "runtime_diagnostic=start_failed") ||
					!strings.Contains(diagnostic, "component_diagnostics=[internal_error]") ||
					strings.Contains(diagnostic, secret) {
					t.Fatalf("terminal status=%s error=%v", status, err)
				}
				liveHostdRebindKillAndReap(t, child)
			})
		}
	})

	t.Run("child exit is finite and does not expose stderr", func(t *testing.T) {
		child := startChild(t, "exit")
		_, _, err := wait(child, time.Now().Add(time.Hour), nonterminal)
		if err == nil || !strings.Contains(err.Error(), "child exited") || strings.Contains(err.Error(), secret) {
			t.Fatalf("child exit error=%v", err)
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	t.Run("timeout uses scenario cleanup to reap the exact child", func(t *testing.T) {
		child := startChild(t, "stall")
		_, observation, err := wait(child, time.Now().Add(liveHostdRebindStartupCleanupReserve+100*time.Millisecond), nonterminal)
		if err == nil || !strings.Contains(err.Error(), "deadline_exceeded") || observation.jobStatus != string(jobs.Running) {
			t.Fatalf("bounded timeout observation=%s error=%v", observation, err)
		}
		cancelled, joined := false, false
		cleanup := liveHostdRebindPendingCurrentCleanup{
			cancel: func() { cancelled = true },
			reap: func() error {
				if !cancelled {
					return errors.New("cleanup reaped before request cancellation")
				}
				return liveHostdRebindKillAndReapError(child)
			},
			join: func() error { joined = true; return nil },
		}
		if reapErr, joinErr := cleanup.run(); reapErr != nil || joinErr != nil || !joined {
			t.Fatalf("timeout cleanup reaping=%v joining=%v joined=%t", reapErr, joinErr, joined)
		}
		liveHostdRebindAssertChildReaped(t, child)
	})

	t.Run("invalid markers and observer failures are closed", func(t *testing.T) {
		t.Run("invalid marker", func(t *testing.T) {
			child := startChild(t, "stall")
			writeMarker(t, child, liveHostdRebindBoundaryMarker{Boundary: "wrong", OperationID: secret})
			_, _, err := wait(child, time.Now().Add(time.Hour), nonterminal)
			if err == nil || !strings.Contains(err.Error(), "marker is invalid") || strings.Contains(err.Error(), secret) {
				t.Fatalf("invalid marker error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
		t.Run("observer error", func(t *testing.T) {
			child := startChild(t, "stall")
			_, _, err := wait(child, time.Now().Add(time.Hour), func(context.Context, string) (liveHostdRebindPendingRouteObservation, error) {
				return liveHostdRebindPendingRouteObservation{}, errors.New(secret)
			})
			if err == nil || !strings.Contains(err.Error(), "durable observation failed") || strings.Contains(err.Error(), secret) {
				t.Fatalf("observer error=%v", err)
			}
			liveHostdRebindKillAndReap(t, child)
		})
	})

	t.Run("exact job binding rejects foreign durable records", func(t *testing.T) {
		base := jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: appID,
			Status: string(jobs.Running), Phase: "apply_runtime", Progress: 70}
		for _, test := range []struct {
			name        string
			job         jobs.Job
			events      []jobs.Event
			deployments []deployments.Deployment
		}{
			{name: "foreign job", job: func() jobs.Job { value := base; value.ID = secret; return value }()},
			{name: "foreign event", job: base, events: []jobs.Event{{JobID: secret}}},
			{name: "foreign exact deployment", job: base, deployments: []deployments.Deployment{{ID: "deployment", AppID: secret, JobID: jobID, Status: deployments.Applying}}},
			{name: "duplicate exact deployment", job: base, deployments: []deployments.Deployment{{ID: "one", AppID: appID, JobID: jobID, Status: deployments.Applying}, {ID: "two", AppID: appID, JobID: jobID, Status: deployments.Applying}}},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, _, err := liveHostdRebindPendingRouteObservationFor(appID, jobID, test.job, test.events, test.deployments)
				if err == nil || strings.Contains(err.Error(), secret) {
					t.Fatalf("exact binding error=%v", err)
				}
			})
		}
	})

	t.Run("exact job binding selects only the requested records", func(t *testing.T) {
		job := jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: appID,
			Status: string(jobs.Running), Phase: "apply_runtime", Progress: 70, ErrorCode: "apply_failed"}
		events := make([]jobs.Event, 0, liveHostdRebindPendingRouteEventTail+2)
		for index := 0; index < liveHostdRebindPendingRouteEventTail+2; index++ {
			events = append(events, jobs.Event{JobID: jobID, Phase: "apply_runtime", Code: "phase_started"})
		}
		observation, deploymentID, err := liveHostdRebindPendingRouteObservationFor(appID, jobID, job, events,
			[]deployments.Deployment{
				{ID: "foreign", AppID: appID, JobID: "foreign-job", Status: deployments.Succeeded, DiagnosticCode: "runtime_unavailable"},
				{ID: "exact", AppID: appID, JobID: jobID, Status: deployments.Applying, DiagnosticCode: "apply_failed"},
			})
		if err != nil || deploymentID != "exact" || !observation.deploymentFound ||
			observation.jobErrorCode != "apply_failed" || observation.deploymentStatus != deployments.Applying ||
			observation.deploymentDiagnosticCode != "apply_failed" ||
			len(observation.events) != liveHostdRebindPendingRouteEventTail {
			t.Fatalf("exact binding deployment=%q observation=%s error=%v", deploymentID, observation, err)
		}
	})

	t.Run("real SQLite observer binds exact durable state and closes private fields", func(t *testing.T) {
		db, err := database.Open(filepath.Join(t.TempDir(), "observer"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error("close pending route SQLite observer control")
			}
		}()

		actorID, planID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)
		if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?, ?,?)`,
			actorID, actorID, "hash", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',?,?)`,
			appID, appID, "Pending route observer", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
			id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
			analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
			field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
		) VALUES(?,?,1,?,'generated_node','observer-control','1',?,'local',0,?,?,1,8,'',?,?,'accepted',?,?)`,
			planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".secret", strings.Repeat("a", 64),
			strings.Repeat("b", 64), strings.Repeat("c", 64), actorID, now, actorID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO releases(
			id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,workspace_state,
			workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
		) VALUES(?,?,'ready','{}',?,'local',0,?,'ready',?,?,1)`,
			releaseID, appID, now, strings.Repeat("d", 64), strings.Repeat("e", 64), planID); err != nil {
			t.Fatal(err)
		}

		jobStore, deploymentStore := jobs.New(db), deployments.New(db)
		foreignJob, created, err := jobStore.CreateWithInput(jobs.CreateRequest{
			Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID, Input: jobs.NoInput{},
		})
		if err != nil || !created {
			t.Fatalf("create historical observer job: created=%t error=%v", created, err)
		}
		foreignDeployment, created, err := deploymentStore.GetOrCreateByJob(context.Background(), appID, foreignJob.ID,
			string(jobs.ConfigurationCurrent))
		if err != nil || !created {
			t.Fatalf("create historical observer deployment: created=%t error=%v", created, err)
		}
		if _, err := db.Exec(`UPDATE jobs SET status='failed',phase='failed',progress_percent=60,error_code='runtime_unavailable',error_detail=?,finished_at=?,updated_at=? WHERE id=?`,
			secret, now, now, foreignJob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployments SET status='failed',diagnostic_code='runtime_unavailable',failure_summary=? WHERE id=?`,
			secret, foreignDeployment.ID); err != nil {
			t.Fatal(err)
		}

		exactJob, created, err := jobStore.CreateWithInput(jobs.CreateRequest{
			Type: "deploy", ResourceType: "application", ResourceID: appID, RequestedBy: actorID,
			Input: jobs.DeploymentInput{ReleaseID: releaseID, ConfigurationMode: jobs.ConfigurationCurrent},
		})
		if err != nil || !created {
			t.Fatalf("create exact observer job: created=%t error=%v", created, err)
		}
		exactDeployment, created, err := deploymentStore.GetOrCreateByJob(context.Background(), appID, exactJob.ID,
			string(jobs.ConfigurationCurrent))
		if err != nil || !created {
			t.Fatalf("create exact observer deployment: created=%t error=%v", created, err)
		}
		if _, err := deploymentStore.InitializeRuntime(context.Background(), appID, exactDeployment.ID, releaseID, "", 0,
			deployments.RuntimeGeneratedNode, planID, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, exactDeployment.ID, deployments.Applying, ""); err != nil {
			t.Fatal(err)
		}
		runtimeStore := generatedruntimestate.New(db)
		if _, created, err := runtimeStore.Begin(context.Background(), generatedruntimestate.BeginInput{
			DeploymentID: exactDeployment.ID, AppID: appID, ReleaseID: releaseID,
			DeploymentPlanRevisionID: planID, DeploymentPlanRevisionNumber: 1, ComponentNames: []string{secret},
		}); err != nil || !created {
			t.Fatalf("begin exact observer runtime: created=%t error=%v", created, err)
		}
		if _, err := db.Exec(`UPDATE jobs SET status='assigned',phase='assigned',attempt=1,updated_at=? WHERE id=?`,
			now, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		if err := jobStore.Update(exactJob.ID, jobs.Running, "apply_runtime", 70, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.FailComponent(context.Background(), appID, exactDeployment.ID, secret,
			generatedruntimestate.ComponentPending, generatedruntimestate.DiagnosticInternalError); err != nil {
			t.Fatal(err)
		}
		if _, err := runtimeStore.Advance(context.Background(), appID, exactDeployment.ID,
			generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseFailed,
			generatedruntimestate.DiagnosticStartFailed); err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentStore.Transition(context.Background(), appID, exactDeployment.ID,
			deployments.Failed, "process_termination_failed"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE jobs SET status='failed',phase='failed',checkpoint_json=?,error_code='apply_failed',error_detail=?,finished_at=?,updated_at=? WHERE id=?`,
			`{"private":"`+secret+`"}`, secret, now, now, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployments SET failure_summary=? WHERE id=?`, secret, exactDeployment.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE job_events SET message=? WHERE job_id=? AND code='phase_started'`, secret, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		persisted, err := jobStore.Get(exactJob.ID)
		events, eventsErr := jobStore.Events(exactJob.ID, 0)
		privateEvent := len(events) == 2 && events[1].Message == secret
		if err != nil || eventsErr != nil || !strings.Contains(persisted.Checkpoint, secret) ||
			persisted.ErrorCode != "apply_failed" || persisted.ErrorDetail != secret || !privateEvent {
			t.Fatalf("private durable observer fixture was not established: job_error=%v events_error=%v private_event=%t",
				err, eventsErr, privateEvent)
		}

		observation, err := liveHostdRebindNewPendingRouteObserver(db, appID).snapshot(context.Background(), exactJob.ID)
		diagnostic := observation.String()
		privateDiagnostic := strings.Contains(diagnostic, secret)
		if err != nil || observation.jobStatus != string(jobs.Failed) || observation.jobPhase != "failed" ||
			observation.jobProgress != 70 || observation.jobErrorCode != "apply_failed" ||
			!observation.deploymentFound || observation.deploymentStatus != deployments.Failed ||
			observation.deploymentDiagnosticCode != "process_termination_failed" || !observation.runtimeFound ||
			observation.runtimePhase != generatedruntimestate.PhaseFailed ||
			observation.runtimeDiagnosticCode != generatedruntimestate.DiagnosticStartFailed ||
			len(observation.componentStates) != 1 || observation.componentStates[0] != generatedruntimestate.ComponentFailed ||
			len(observation.componentDiagnosticCodes) != 1 ||
			observation.componentDiagnosticCodes[0] != generatedruntimestate.DiagnosticInternalError ||
			len(observation.events) != 2 || privateDiagnostic ||
			!strings.Contains(diagnostic, "job_error=apply_failed") ||
			!strings.Contains(diagnostic, "deployment=failed deployment_diagnostic=process_termination_failed") ||
			!strings.Contains(diagnostic, "runtime=failed runtime_diagnostic=start_failed") ||
			!strings.Contains(diagnostic, "components=[failed] component_diagnostics=[internal_error]") {
			t.Fatalf("real observer state invalid: error=%v historical_deployment_selected=%t private_diagnostic=%t",
				err, observation.deploymentStatus == foreignDeployment.Status, privateDiagnostic)
		}

		if _, err := db.Exec(`UPDATE jobs SET error_code=? WHERE id=?`, secret, exactJob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE deployments SET diagnostic_code=? WHERE id=?`, secret, exactDeployment.ID); err != nil {
			t.Fatal(err)
		}
		unknown, err := liveHostdRebindNewPendingRouteObserver(db, appID).snapshot(context.Background(), exactJob.ID)
		unknownDiagnostic := unknown.String()
		if err != nil || strings.Contains(unknownDiagnostic, secret) ||
			!strings.Contains(unknownDiagnostic, "job_error=unknown") ||
			!strings.Contains(unknownDiagnostic, "deployment_diagnostic=unknown") ||
			!strings.Contains(unknownDiagnostic, "runtime_diagnostic=start_failed") ||
			!strings.Contains(unknownDiagnostic, "component_diagnostics=[internal_error]") {
			t.Fatalf("real observer unknown-code closure failed: error=%v private=%t diagnostic=%s",
				err, strings.Contains(unknownDiagnostic, secret), unknownDiagnostic)
		}
	})

	t.Run("diagnostic allowlist excludes durable private fields", func(t *testing.T) {
		observation := liveHostdRebindPendingRouteObservation{
			observed: true, jobStatus: secret, jobPhase: secret, jobProgress: 42, jobErrorCode: secret,
			deploymentFound: true, deploymentStatus: deployments.Status(secret), deploymentDiagnosticCode: secret,
			runtimeFound: true, runtimePhase: generatedruntimestate.Phase(secret),
			runtimeDiagnosticCode: generatedruntimestate.DiagnosticCode(secret),
			componentStates:       []generatedruntimestate.ComponentState{generatedruntimestate.ComponentState(secret)},
			componentDiagnosticCodes: []generatedruntimestate.DiagnosticCode{
				generatedruntimestate.DiagnosticCode(secret),
			},
			events: []jobs.Event{{Phase: secret, Code: secret, Message: secret}},
		}
		diagnostic := observation.String()
		if privateDiagnostic, closedUnknown := strings.Contains(diagnostic, secret), strings.Contains(diagnostic, "unknown"); privateDiagnostic || !closedUnknown {
			t.Fatalf("diagnostic sanitization failed: private=%t closed_unknown=%t", privateDiagnostic, closedUnknown)
		}
	})

	t.Run("diagnostic code allowlists are exact and closed", func(t *testing.T) {
		domains := []struct {
			name   string
			values []string
			safe   func(string) string
		}{
			{
				name: "job error",
				values: []string{
					"validation_failed", "runtime_unavailable", "process_termination_failed", "invalid_source",
					"source_unavailable", "source_access_lost", "provider_unavailable", "source_too_large",
					"source_storage_full", "configuration_unavailable", "configuration_review_required",
					"reviewed_revision_stale", "reviewed_revisions_required", "build_configuration_requires_build_command",
					"compose_invalid", "compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated",
					"policy_rejected", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
					"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "internal_error",
					"executor_failed", "executor_invalid_result",
				},
				safe: liveHostdRebindSafeJobErrorCode,
			},
			{
				name: "deployment diagnostic",
				values: []string{
					"daemon_restarted", "runtime_unavailable", "process_termination_failed", "compose_invalid",
					"compose_config_invalid", "compose_config_timeout", "compose_config_output_truncated", "policy_rejected",
					"approval_required", "apply_failed", "compose_apply_failed", "compose_apply_timeout",
					"compose_apply_output_truncated", "health_failed", "gateway_readiness_failed", "cancelled", "internal_error",
					"invalid_source", "source_unavailable", "source_access_lost", "source_too_large", "source_storage_full",
					"provider_unavailable", "configuration_unavailable", "configuration_review_required",
					"build_configuration_requires_build_command",
				},
				safe: liveHostdRebindSafeDeploymentDiagnosticCode,
			},
			{
				name: "runtime diagnostic",
				values: []string{
					"insufficient_replacement_capacity", "deployment_plan_review_required", "migration_failed", "start_failed",
					"health_failed", "route_switch_failed", "runtime_unavailable", "daemon_restarted", "cancelled", "internal_error",
				},
				safe: func(value string) string {
					return liveHostdRebindSafeRuntimeDiagnosticCode(generatedruntimestate.DiagnosticCode(value))
				},
			},
			{
				name: "component diagnostic",
				values: []string{
					"start_failed", "health_failed", "runtime_unavailable", "daemon_restarted", "cancelled", "internal_error",
				},
				safe: func(value string) string {
					return liveHostdRebindSafeComponentDiagnosticCode(generatedruntimestate.DiagnosticCode(value))
				},
			},
		}
		for _, domain := range domains {
			t.Run(domain.name, func(t *testing.T) {
				if got := domain.safe(""); got != "none" {
					t.Fatalf("empty diagnostic=%q", got)
				}
				for _, value := range domain.values {
					if got := domain.safe(value); got != value {
						t.Fatalf("known diagnostic %q=%q", value, got)
					}
				}
				if got := domain.safe(secret); got != "unknown" || strings.Contains(got, secret) {
					t.Fatalf("unknown diagnostic=%q", got)
				}
			})
		}
	})
}
