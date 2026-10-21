package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedmigration"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

const (
	migrationJourneyFailureDiagnosticBudget = 2 * time.Second
	migrationJourneyFailureHistoryLimit     = 10
)

// These snapshots intentionally retain errors only until the one-line test
// diagnostic is classified. They must never be formatted directly.
type migrationJourneyRunnerDiagnosticSnapshot struct {
	Calls   int
	Results []error
}

type migrationJourneyExecutionDiagnosticSnapshot struct {
	Armed                 bool
	ObservationAvailable  bool
	Err                   error
	ReturnContextCanceled bool
	ReturnContextDeadline bool
}

type migrationJourneyProcessDiagnosticSnapshot struct {
	Armed                bool
	ObservationAvailable bool
	CreateCalls          int
	StartCalls           int
	WaitCalls            int
	RemoveCalls          int
	AttemptMutations     int
	Captured             bool
}

type migrationJourneyJobGet func(string) (jobs.Job, error)
type migrationJourneyDeploymentList func(context.Context, string, int) ([]deployments.Deployment, error)
type migrationJourneyRuntimeGet func(context.Context, string, string) (generatedruntimestate.Deployment, error)

type migrationJourneyResumeFailureDiagnosticInput struct {
	Parent      context.Context
	Application string
	Job         string
	GetJob      migrationJourneyJobGet
	Deployments migrationJourneyDeploymentList
	Runtime     migrationJourneyRuntimeGet
	Runner      func() migrationJourneyRunnerDiagnosticSnapshot
	Execution   func() migrationJourneyExecutionDiagnosticSnapshot
	Process     func() migrationJourneyProcessDiagnosticSnapshot
}

type migrationJourneyResumeFailureObservation struct {
	RunnerCalls       int
	RunnerResultCount int
	RunnerResultCodes string

	ExecutorArmed                 bool
	ExecutorObservationAvailable  bool
	ExecutorErrorPresent          bool
	ExecutorCode                  string
	ExecutorIdentityLost          bool
	ExecutorAuthorityStale        bool
	ExecutorAuthorityInvalid      bool
	ExecutorAuthorityUnresolved   bool
	ExecutorScopeUnavailable      bool
	ExecutorInvalidClaim          bool
	ExecutorInvalidConfiguration  bool
	ExecutorStageInvalid          bool
	ExecutorPositiveFence         bool
	ExecutorAuthorityUnavailable  bool
	ExecutorAuthorityCanceled     bool
	ExecutorErrorContextCanceled  bool
	ExecutorErrorContextDeadline  bool
	ExecutorReturnContextCanceled bool
	ExecutorReturnContextDeadline bool

	JobRead    string
	JobAttempt string
	JobStatus  string
	JobCode    string

	DeploymentRead     string
	DeploymentStatus   string
	DeploymentStarted  string
	DeploymentFinished string

	RuntimeRead       string
	RuntimePhase      string
	MigrationState    string
	RuntimeDiagnostic string
	MigrationStarted  string
	MigrationFinished string
	RuntimeFinished   string

	ProcessArmed                bool
	ProcessObservationAvailable bool
	ProcessCreateCalls          string
	ProcessStartCalls           string
	ProcessWaitCalls            string
	ProcessRemoveCalls          string
	ProcessAttemptMutations     string
	ProcessCaptured             string
}

func migrationJourneyResumeFailureDiagnostic(t *testing.T, input migrationJourneyResumeFailureDiagnosticInput) (func(), func()) {
	t.Helper()
	return migrationJourneyFailureOnlyDiagnostic(func() {
		t.Log(migrationJourneyResumeFailureObservationLine(migrationJourneyResumeFailureObservationFor(input)))
	})
}

func migrationJourneyFailureOnlyDiagnostic(logFailure func()) (func(), func()) {
	completed := false
	failure := func() {
		if !completed && logFailure != nil {
			logFailure()
		}
	}
	return failure, func() { completed = true }
}

func migrationJourneyResumeFailureObservationFor(input migrationJourneyResumeFailureDiagnosticInput) migrationJourneyResumeFailureObservation {
	observation := migrationJourneyResumeFailureObservation{
		RunnerResultCodes:       "unavailable",
		ExecutorCode:            "unavailable",
		JobRead:                 "unavailable",
		JobAttempt:              "unavailable",
		JobStatus:               "unavailable",
		JobCode:                 "unavailable",
		DeploymentRead:          "unavailable",
		DeploymentStatus:        "unavailable",
		DeploymentStarted:       "unavailable",
		DeploymentFinished:      "unavailable",
		RuntimeRead:             "unavailable",
		RuntimePhase:            "unavailable",
		MigrationState:          "unavailable",
		RuntimeDiagnostic:       "unavailable",
		MigrationStarted:        "unavailable",
		MigrationFinished:       "unavailable",
		RuntimeFinished:         "unavailable",
		ProcessCreateCalls:      "unavailable",
		ProcessStartCalls:       "unavailable",
		ProcessWaitCalls:        "unavailable",
		ProcessRemoveCalls:      "unavailable",
		ProcessAttemptMutations: "unavailable",
		ProcessCaptured:         "unavailable",
	}
	if input.Runner != nil {
		runner := input.Runner()
		observation.RunnerCalls = runner.Calls
		observation.RunnerResultCount = len(runner.Results)
		observation.RunnerResultCodes = strings.Join(migrationJourneyRunnerResultCodes(runner.Results), ",")
	}
	if input.Execution != nil {
		execution := input.Execution()
		observation.ExecutorArmed = execution.Armed
		observation.ExecutorObservationAvailable = execution.ObservationAvailable
		observation.ExecutorReturnContextCanceled = execution.ReturnContextCanceled
		observation.ExecutorReturnContextDeadline = execution.ReturnContextDeadline
		if execution.ObservationAvailable {
			observation.ExecutorErrorPresent = execution.Err != nil
			observation.ExecutorCode = migrationJourneyExecutionCode(execution.Err)
			observation.ExecutorIdentityLost = errors.Is(execution.Err, deploymentexecution.ErrIdentityLost)
			observation.ExecutorAuthorityStale = errors.Is(execution.Err, deploymentexecution.ErrAuthorityStale)
			observation.ExecutorAuthorityInvalid = errors.Is(execution.Err, deploymentexecution.ErrAuthorityInvalid)
			observation.ExecutorAuthorityUnresolved = errors.Is(execution.Err, deploymentexecution.ErrAuthorityUnresolved)
			observation.ExecutorScopeUnavailable = errors.Is(execution.Err, deploymentexecution.ErrScopeUnavailable)
			observation.ExecutorInvalidClaim = errors.Is(execution.Err, deploymentexecution.ErrInvalidClaim)
			observation.ExecutorInvalidConfiguration = errors.Is(execution.Err, deploymentexecution.ErrInvalidConfiguration)
			observation.ExecutorStageInvalid = errors.Is(execution.Err, deploymentexecution.ErrStageInvalid)
			observation.ExecutorPositiveFence = errors.Is(execution.Err, deploymentexecution.ErrPositiveFence)
			observation.ExecutorAuthorityUnavailable = errors.Is(execution.Err, deploymentexecution.ErrUnavailable)
			observation.ExecutorAuthorityCanceled = errors.Is(execution.Err, deploymentexecution.ErrCanceled)
			observation.ExecutorErrorContextCanceled = errors.Is(execution.Err, context.Canceled)
			observation.ExecutorErrorContextDeadline = errors.Is(execution.Err, context.DeadlineExceeded)
		} else {
			observation.ExecutorCode = "unavailable"
		}
	}
	if input.Process != nil {
		process := input.Process()
		observation.ProcessArmed = process.Armed
		observation.ProcessObservationAvailable = process.ObservationAvailable
		if process.ObservationAvailable {
			observation.ProcessCreateCalls = strconv.Itoa(process.CreateCalls)
			observation.ProcessStartCalls = strconv.Itoa(process.StartCalls)
			observation.ProcessWaitCalls = strconv.Itoa(process.WaitCalls)
			observation.ProcessRemoveCalls = strconv.Itoa(process.RemoveCalls)
			observation.ProcessAttemptMutations = strconv.Itoa(process.AttemptMutations)
			observation.ProcessCaptured = strconv.FormatBool(process.Captured)
		}
	}

	if input.Application == "" || input.Job == "" || input.GetJob == nil {
		return observation
	}
	job, err := input.GetJob(input.Job)
	if err != nil {
		observation.JobRead = "failed"
		return observation
	}
	if job.ID != input.Job || job.Type != "deploy" || job.ResourceType != "application" || job.ResourceID != input.Application {
		observation.JobRead = "identity_mismatch"
		return observation
	}
	observation.JobRead = "ok"
	observation.JobAttempt = strconv.Itoa(job.Attempt)
	observation.JobStatus = migrationJourneyJobStatusLabel(job.Status)
	observation.JobCode = migrationJourneyJobCodeLabel(job.ErrorCode)
	if input.Deployments == nil || input.Runtime == nil {
		return observation
	}
	ctx, cancel := migrationJourneyFailureDiagnosticContext(input.Parent)
	defer cancel()
	history, err := input.Deployments(ctx, input.Application, migrationJourneyFailureHistoryLimit)
	if err != nil {
		observation.DeploymentRead = "failed"
		return observation
	}
	var matched *deployments.Deployment
	for index := range history {
		if history[index].JobID != input.Job {
			continue
		}
		if matched != nil {
			observation.DeploymentRead = "ambiguous"
			return observation
		}
		matched = &history[index]
	}
	if matched == nil {
		observation.DeploymentRead = "missing"
		return observation
	}
	if matched.ID == "" || matched.AppID != input.Application {
		observation.DeploymentRead = "identity_mismatch"
		return observation
	}
	observation.DeploymentRead = "ok"
	observation.DeploymentStatus = migrationJourneyDeploymentStatusLabel(matched.Status)
	observation.DeploymentStarted = strconv.FormatBool(!matched.StartedAt.IsZero())
	observation.DeploymentFinished = strconv.FormatBool(!matched.FinishedAt.IsZero())
	runtimeState, err := input.Runtime(ctx, input.Application, matched.ID)
	if err != nil {
		observation.RuntimeRead = "failed"
		return observation
	}
	if runtimeState.AppID != input.Application || runtimeState.DeploymentID != matched.ID {
		observation.RuntimeRead = "identity_mismatch"
		return observation
	}
	observation.RuntimeRead = "ok"
	observation.RuntimePhase = migrationJourneyRuntimePhaseLabel(runtimeState.Phase)
	observation.MigrationState = migrationJourneyMigrationStateLabel(runtimeState.MigrationState)
	observation.RuntimeDiagnostic = migrationJourneyRuntimeDiagnosticLabel(runtimeState.DiagnosticCode)
	observation.MigrationStarted = strconv.FormatBool(!runtimeState.MigrationStartedAt.IsZero())
	observation.MigrationFinished = strconv.FormatBool(!runtimeState.MigrationFinishedAt.IsZero())
	observation.RuntimeFinished = strconv.FormatBool(!runtimeState.FinishedAt.IsZero())
	return observation
}

func migrationJourneyFailureDiagnosticContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	// This context bounds only the history and runtime reads. jobs.Service.Get
	// has no context-aware API, so it is intentionally outside this claim.
	return context.WithTimeout(context.WithoutCancel(parent), migrationJourneyFailureDiagnosticBudget)
}

func migrationJourneyResumeFailureObservationLine(observation migrationJourneyResumeFailureObservation) string {
	return fmt.Sprintf("M4 migration resume failure diagnostic: runner_calls=%d runner_result_count=%d runner_result_codes=%s executor_armed=%t executor_observation_available=%t executor_error_present=%t executor_code=%s executor_identity_lost=%t executor_authority_stale=%t executor_authority_invalid=%t executor_authority_unresolved=%t executor_scope_unavailable=%t executor_invalid_claim=%t executor_invalid_configuration=%t executor_stage_invalid=%t executor_positive_fence=%t executor_authority_unavailable=%t executor_authority_canceled=%t executor_error_context_canceled=%t executor_error_context_deadline=%t executor_return_context_canceled=%t executor_return_context_deadline=%t job_read=%s job_attempt=%s job_status=%s job_code=%s deployment_read=%s deployment_status=%s deployment_started=%s deployment_finished=%s runtime_read=%s runtime_phase=%s migration_state=%s runtime_diagnostic=%s migration_started=%s migration_finished=%s runtime_finished=%s process_armed=%t process_observation_available=%t process_create_calls=%s process_start_calls=%s process_wait_calls=%s process_remove_calls=%s process_attempt_mutations=%s process_captured=%s",
		observation.RunnerCalls, observation.RunnerResultCount, observation.RunnerResultCodes,
		observation.ExecutorArmed, observation.ExecutorObservationAvailable, observation.ExecutorErrorPresent, observation.ExecutorCode,
		observation.ExecutorIdentityLost, observation.ExecutorAuthorityStale, observation.ExecutorAuthorityInvalid,
		observation.ExecutorAuthorityUnresolved, observation.ExecutorScopeUnavailable, observation.ExecutorInvalidClaim,
		observation.ExecutorInvalidConfiguration, observation.ExecutorStageInvalid, observation.ExecutorPositiveFence,
		observation.ExecutorAuthorityUnavailable, observation.ExecutorAuthorityCanceled, observation.ExecutorErrorContextCanceled,
		observation.ExecutorErrorContextDeadline, observation.ExecutorReturnContextCanceled, observation.ExecutorReturnContextDeadline,
		observation.JobRead, observation.JobAttempt, observation.JobStatus, observation.JobCode,
		observation.DeploymentRead, observation.DeploymentStatus, observation.DeploymentStarted, observation.DeploymentFinished,
		observation.RuntimeRead, observation.RuntimePhase, observation.MigrationState, observation.RuntimeDiagnostic,
		observation.MigrationStarted, observation.MigrationFinished, observation.RuntimeFinished,
		observation.ProcessArmed, observation.ProcessObservationAvailable, observation.ProcessCreateCalls,
		observation.ProcessStartCalls, observation.ProcessWaitCalls, observation.ProcessRemoveCalls,
		observation.ProcessAttemptMutations, observation.ProcessCaptured)
}

func migrationJourneyRunnerResultCodes(results []error) []string {
	if len(results) == 0 {
		return []string{"none"}
	}
	labels := make([]string, 0, len(results))
	for _, result := range results {
		labels = append(labels, migrationJourneyRunnerResultCode(result))
	}
	return labels
}

func migrationJourneyRunnerResultCode(err error) string {
	if err == nil {
		return "none"
	}
	for _, code := range []string{
		"validation_failed", "migration_create_failed", "migration_start_failed", "migration_failed",
		"migration_cleanup_failed", "cancelled", "migration_timeout", "process_termination_failed", "runtime_unavailable",
	} {
		if generatedmigration.IsCode(err, code) {
			return code
		}
	}
	return "unknown"
}

func migrationJourneyExecutionCode(err error) string {
	if err == nil {
		return "none"
	}
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) {
		return "unknown"
	}
	return migrationJourneyJobCodeLabel(executionErr.Code)
}

func migrationJourneyJobStatusLabel(value string) string {
	switch jobs.Status(value) {
	case jobs.Queued, jobs.Assigned, jobs.Running, jobs.Waiting, jobs.WaitingUser,
		jobs.Succeeded, jobs.Failed, jobs.Cancelled, jobs.Interrupted, jobs.NeedsAttention:
		return value
	default:
		return "unknown"
	}
}

func migrationJourneyJobCodeLabel(value string) string {
	switch value {
	case "":
		return "none"
	case "validation_failed", "runtime_unavailable", "process_termination_failed",
		"invalid_source", "source_unavailable", "source_access_lost", "provider_unavailable",
		"source_too_large", "source_storage_full", "configuration_unavailable",
		"configuration_review_required", "reviewed_revision_stale", "reviewed_revisions_required",
		"build_configuration_requires_build_command", "compose_invalid", "compose_config_invalid",
		"compose_config_timeout", "compose_config_output_truncated", "policy_rejected",
		"apply_failed", "compose_apply_failed", "compose_apply_timeout", "compose_apply_output_truncated",
		"health_failed", "gateway_readiness_failed", "internal_error", "executor_failed", "executor_invalid_result":
		return value
	default:
		return "unknown"
	}
}

func migrationJourneyDeploymentStatusLabel(value deployments.Status) string {
	switch value {
	case deployments.Preparing, deployments.Applying, deployments.WaitingHealth, deployments.Succeeded, deployments.Failed, deployments.Cancelled, deployments.NeedsAttention:
		return string(value)
	default:
		return "unknown"
	}
}

func migrationJourneyRuntimePhaseLabel(value generatedruntimestate.Phase) string {
	switch value {
	case generatedruntimestate.PhasePreflight, generatedruntimestate.PhaseBuilding, generatedruntimestate.PhaseMigrating,
		generatedruntimestate.PhaseStartingCandidate, generatedruntimestate.PhaseWaitingHealth, generatedruntimestate.PhaseSwitchingRoute,
		generatedruntimestate.PhaseDraining, generatedruntimestate.PhaseSucceeded, generatedruntimestate.PhaseFailed, generatedruntimestate.PhaseCancelled:
		return string(value)
	default:
		return "unknown"
	}
}

func migrationJourneyMigrationStateLabel(value generatedruntimestate.MigrationState) string {
	switch value {
	case generatedruntimestate.MigrationNotRequired, generatedruntimestate.MigrationPending, generatedruntimestate.MigrationRunning,
		generatedruntimestate.MigrationSucceeded, generatedruntimestate.MigrationFailed:
		return string(value)
	default:
		return "unknown"
	}
}

func migrationJourneyRuntimeDiagnosticLabel(value generatedruntimestate.DiagnosticCode) string {
	switch value {
	case "":
		return "none"
	case generatedruntimestate.DiagnosticInsufficientReplacementCapacity, generatedruntimestate.DiagnosticPlanReviewRequired,
		generatedruntimestate.DiagnosticMigrationFailed, generatedruntimestate.DiagnosticStartFailed,
		generatedruntimestate.DiagnosticHealthFailed, generatedruntimestate.DiagnosticRouteSwitchFailed,
		generatedruntimestate.DiagnosticRuntimeUnavailable, generatedruntimestate.DiagnosticDaemonRestarted,
		generatedruntimestate.DiagnosticCancelled, generatedruntimestate.DiagnosticInternalError:
		return string(value)
	default:
		return "unknown"
	}
}

func TestMigrationJourneyResumeFailureDiagnosticIsAllowlisted(t *testing.T) {
	const (
		applicationID = "application-secret"
		jobID         = "job-secret"
		deploymentID  = "deployment-secret"
	)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	knownRunner := &generatedmigration.Error{Code: "migration_create_failed"}
	unknownRunner := &generatedmigration.Error{Code: "runner-secret"}
	knownExecutor := fmt.Errorf("executor-wrapper-secret: %w", errors.Join(
		&jobs.ExecutionError{Code: "runtime_unavailable", Detail: "executor-detail-secret"},
		deploymentexecution.ErrIdentityLost,
	))
	observation := migrationJourneyResumeFailureObservationFor(migrationJourneyResumeFailureDiagnosticInput{
		Parent: parent, Application: applicationID, Job: jobID,
		GetJob: func(id string) (jobs.Job, error) {
			if id != jobID {
				t.Fatal("diagnostic job reader did not receive the selected job")
			}
			return jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: applicationID, Attempt: 2, Status: string(jobs.Failed), ErrorCode: "runtime_unavailable"}, nil
		},
		Deployments: func(ctx context.Context, app string, limit int) ([]deployments.Deployment, error) {
			if ctx.Err() != nil || app != applicationID || limit != migrationJourneyFailureHistoryLimit {
				t.Fatal("diagnostic deployment reader did not retain its bounded selected scope")
			}
			return []deployments.Deployment{{ID: deploymentID, AppID: applicationID, JobID: jobID, Status: deployments.Applying}}, nil
		},
		Runtime: func(ctx context.Context, app, deployment string) (generatedruntimestate.Deployment, error) {
			if ctx.Err() != nil || app != applicationID || deployment != deploymentID {
				t.Fatal("diagnostic runtime reader did not retain its bounded selected scope")
			}
			return generatedruntimestate.Deployment{DeploymentID: deploymentID, AppID: applicationID, Phase: generatedruntimestate.PhaseMigrating, MigrationState: generatedruntimestate.MigrationRunning}, nil
		},
		Runner: func() migrationJourneyRunnerDiagnosticSnapshot {
			return migrationJourneyRunnerDiagnosticSnapshot{Calls: 2, Results: []error{knownRunner, unknownRunner}}
		},
		Execution: func() migrationJourneyExecutionDiagnosticSnapshot {
			return migrationJourneyExecutionDiagnosticSnapshot{Armed: true, ObservationAvailable: true, Err: knownExecutor}
		},
		Process: func() migrationJourneyProcessDiagnosticSnapshot { return migrationJourneyProcessDiagnosticSnapshot{} },
	})
	line := migrationJourneyResumeFailureObservationLine(observation)
	for _, want := range []string{"runner_result_codes=migration_create_failed,unknown", "executor_code=runtime_unavailable", "executor_identity_lost=true", "process_armed=false", "process_observation_available=false", "process_create_calls=unavailable", "job_attempt=2", "job_status=failed", "runtime_phase=migrating", "migration_state=running"} {
		if !strings.Contains(line, want) {
			t.Fatal("diagnostic omitted an allowlisted field")
		}
	}
	for _, forbidden := range []string{applicationID, jobID, deploymentID, "runner-secret", "executor-wrapper-secret", "executor-detail-secret"} {
		if strings.Contains(line, forbidden) {
			t.Fatal("diagnostic leaked a withheld value")
		}
	}
}

func TestMigrationJourneyFailureDiagnosticDeferredOrdering(t *testing.T) {
	var order []string
	failure, _ := migrationJourneyFailureOnlyDiagnostic(func() { order = append(order, "diagnostic") })
	func() {
		defer func() { order = append(order, "teardown") }()
		defer failure()
	}()
	if strings.Join(order, ",") != "diagnostic,teardown" {
		t.Fatal("failure diagnostic did not run before the pre-existing teardown defer")
	}
	var successLogs int
	successFailure, successComplete := migrationJourneyFailureOnlyDiagnostic(func() { successLogs++ })
	successComplete()
	successFailure()
	if successLogs != 0 {
		t.Fatal("completed diagnostic emitted a success-path log")
	}
}
