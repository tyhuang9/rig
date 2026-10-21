package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedimage"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/jobs"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type liveHostdRebindBaselinePhase string

const (
	liveHostdRebindBaselineADeployPhase       liveHostdRebindBaselinePhase = "baseline_a_deploy"
	liveHostdRebindBaselineBDeployPhase       liveHostdRebindBaselinePhase = "baseline_b_deploy"
	liveHostdRebindLegacyIdentityCapturePhase liveHostdRebindBaselinePhase = "legacy_identity_capture"
	liveHostdRebindBaselineWorkerStopBudget                                = 10 * time.Second
)

type liveHostdRebindBaselineDiagnosticSnapshot struct {
	FixturePhase           string
	Correlation            string
	LastAcceptedJobPhase   string
	ReturnObservation      string
	ExecutionCode          string
	RetainedCandidateCode  string
	RetainedCompileCode    string
	RetainedRuntimeCode    string
	ErrorCanceled          bool
	ErrorDeadline          bool
	ErrorTermination       bool
	ImageBoundaryRefusal   bool
	NetworkBoundaryRefusal bool
	ReturnContextCanceled  bool
	ReturnContextDeadline  bool
}

type liveHostdRebindBaselineReturnClassification struct {
	ExecutionCode          string
	RetainedCandidateCode  string
	RetainedCompileCode    string
	RetainedRuntimeCode    string
	ErrorCanceled          bool
	ErrorDeadline          bool
	ErrorTermination       bool
	ImageBoundaryRefusal   bool
	NetworkBoundaryRefusal bool
	ReturnContextCanceled  bool
	ReturnContextDeadline  bool
}

// liveHostdRebindBaselineExecutionObserver observes only the two baseline
// deployments. Its private identity fields prevent a different deployment or
// attempt from being represented as a fixture baseline observation.
type liveHostdRebindBaselineExecutionObserver struct {
	mu       sync.Mutex
	delegate jobs.Executor
	active   liveHostdRebindBaselinePhase
	failure  liveHostdRebindBaselinePhase
	overflow bool
	slots    [2]liveHostdRebindBaselineExecutionSlot
}

type liveHostdRebindBaselineExecutionSlot struct {
	begun, bound, ambiguous, executing, returned bool
	phase                                        liveHostdRebindBaselinePhase
	jobID, appID                                 string
	attempt                                      int
	snapshot                                     liveHostdRebindBaselineDiagnosticSnapshot
}

type liveHostdRebindBaselineExecutionIdentity struct {
	jobID, appID string
	attempt      int
}

type liveHostdRebindBaselineProgressReporter struct {
	delegate jobs.ProgressReporter
	observer *liveHostdRebindBaselineExecutionObserver
	slot     int
	identity liveHostdRebindBaselineExecutionIdentity
}

func newLiveHostdRebindBaselineExecutionObserver(delegate jobs.Executor) *liveHostdRebindBaselineExecutionObserver {
	return &liveHostdRebindBaselineExecutionObserver{delegate: delegate}
}

// Begin records the fixture phase before its deployment is enqueued. A phase
// is never rearmed: a second identity or phase use becomes unavailable rather
// than replacing the first private binding.
func (observer *liveHostdRebindBaselineExecutionObserver) Begin(phase liveHostdRebindBaselinePhase) {
	phase = liveHostdRebindBaselineClosedPhase(phase)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.active = phase
	index, observed := liveHostdRebindBaselinePhaseSlot(phase)
	if !observed {
		return
	}
	slot := &observer.slots[index]
	if slot.begun {
		liveHostdRebindBaselineMarkAmbiguous(slot, phase)
		return
	}
	slot.begun = true
	slot.phase = phase
	slot.snapshot = liveHostdRebindBaselineUnavailableSnapshot(phase, "unavailable")
}

func (observer *liveHostdRebindBaselineExecutionObserver) MarkFixtureFailure(phase liveHostdRebindBaselinePhase) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.failure = liveHostdRebindBaselineClosedPhase(phase)
}

func (observer *liveHostdRebindBaselineExecutionObserver) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	slot, identity, observed := observer.observeExecution(job)
	forward := reporter
	if observed && reporter != nil {
		forward = &liveHostdRebindBaselineProgressReporter{
			delegate: reporter, observer: observer, slot: slot, identity: identity,
		}
	}
	result, err := observer.delegate.Execute(ctx, job, forward)
	if observed {
		classification := liveHostdRebindBaselineBestEffortReturnClassification(ctx, err)
		observer.observeReturn(slot, identity, classification)
	}
	return result, err
}

func (reporter *liveHostdRebindBaselineProgressReporter) Report(update jobs.ProgressUpdate) error {
	err := reporter.delegate.Report(update)
	if err != nil {
		return err
	}
	reporter.observer.observeAcceptedPhase(reporter.slot, reporter.identity, update.Phase)
	return nil
}

func (observer *liveHostdRebindBaselineExecutionObserver) observeExecution(job jobs.Job) (int, liveHostdRebindBaselineExecutionIdentity, bool) {
	identity := liveHostdRebindBaselineExecutionIdentity{jobID: job.ID, appID: job.ResourceID, attempt: job.Attempt}
	if !liveHostdRebindBaselineValidDeploymentJob(job) {
		return 0, liveHostdRebindBaselineExecutionIdentity{}, false
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	index, observed := liveHostdRebindBaselinePhaseSlot(observer.active)
	if !observed {
		observer.overflow = true
		return 0, liveHostdRebindBaselineExecutionIdentity{}, false
	}
	slot := &observer.slots[index]
	if !slot.begun {
		observer.overflow = true
		return 0, liveHostdRebindBaselineExecutionIdentity{}, false
	}
	if slot.ambiguous {
		return 0, liveHostdRebindBaselineExecutionIdentity{}, false
	}
	if !slot.bound {
		slot.bound, slot.executing = true, true
		slot.jobID, slot.appID, slot.attempt = identity.jobID, identity.appID, identity.attempt
		slot.snapshot = liveHostdRebindBaselineUnavailableSnapshot(slot.phase, "bound")
		return index, identity, true
	}
	if liveHostdRebindBaselineSameIdentity(slot, identity) && !slot.executing && !slot.returned {
		slot.executing = true
		return index, identity, true
	}
	liveHostdRebindBaselineMarkAmbiguous(slot, slot.phase)
	return 0, liveHostdRebindBaselineExecutionIdentity{}, false
}

func (observer *liveHostdRebindBaselineExecutionObserver) observeAcceptedPhase(
	index int, identity liveHostdRebindBaselineExecutionIdentity, phase string,
) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if index < 0 || index >= len(observer.slots) {
		return
	}
	slot := &observer.slots[index]
	if !slot.bound || !slot.executing || slot.snapshot.Correlation != "bound" || !liveHostdRebindBaselineSameIdentity(slot, identity) {
		return
	}
	slot.snapshot.LastAcceptedJobPhase = liveHostdRebindBaselineJobPhaseLabel(phase)
}

func (observer *liveHostdRebindBaselineExecutionObserver) observeReturn(
	index int, identity liveHostdRebindBaselineExecutionIdentity, classification liveHostdRebindBaselineReturnClassification,
) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if index < 0 || index >= len(observer.slots) {
		return
	}
	slot := &observer.slots[index]
	if !slot.bound || !slot.executing || slot.snapshot.Correlation != "bound" || !liveHostdRebindBaselineSameIdentity(slot, identity) {
		return
	}
	slot.executing, slot.returned = false, true
	snapshot := &slot.snapshot
	snapshot.ReturnObservation = "returned"
	snapshot.ExecutionCode = classification.ExecutionCode
	snapshot.RetainedCandidateCode = classification.RetainedCandidateCode
	snapshot.RetainedCompileCode = classification.RetainedCompileCode
	snapshot.RetainedRuntimeCode = classification.RetainedRuntimeCode
	snapshot.ErrorCanceled = classification.ErrorCanceled
	snapshot.ErrorDeadline = classification.ErrorDeadline
	snapshot.ErrorTermination = classification.ErrorTermination
	snapshot.ImageBoundaryRefusal = classification.ImageBoundaryRefusal
	snapshot.NetworkBoundaryRefusal = classification.NetworkBoundaryRefusal
	snapshot.ReturnContextCanceled = classification.ReturnContextCanceled
	snapshot.ReturnContextDeadline = classification.ReturnContextDeadline
}

func liveHostdRebindBaselineContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Best-effort diagnostic classification must never replace the executor's
// already-returned result or error. Delegate and reporter calls deliberately
// remain outside this recovery boundary so their panics still propagate.
func liveHostdRebindBaselineBestEffortReturnClassification(
	ctx context.Context, err error,
) (classification liveHostdRebindBaselineReturnClassification) {
	classification = liveHostdRebindBaselineUnavailableReturnClassification()
	defer func() {
		if recover() != nil {
			classification = liveHostdRebindBaselineUnavailableReturnClassification()
		}
	}()
	return liveHostdRebindBaselineClassifyReturn(err, liveHostdRebindBaselineContextError(ctx))
}

func liveHostdRebindBaselineUnavailableReturnClassification() liveHostdRebindBaselineReturnClassification {
	return liveHostdRebindBaselineReturnClassification{
		ExecutionCode:          "unavailable",
		RetainedCandidateCode:  "unavailable",
		RetainedCompileCode:    "unavailable",
		RetainedRuntimeCode:    "unavailable",
		ErrorCanceled:          false,
		ErrorDeadline:          false,
		ErrorTermination:       false,
		ImageBoundaryRefusal:   false,
		NetworkBoundaryRefusal: false,
		ReturnContextCanceled:  false,
		ReturnContextDeadline:  false,
	}
}

// Classify before taking observer.mu: errors.Is and errors.As may invoke
// caller-provided error methods, so no error traversal may run under lock.
func liveHostdRebindBaselineClassifyReturn(err, contextErr error) liveHostdRebindBaselineReturnClassification {
	return liveHostdRebindBaselineReturnClassification{
		ExecutionCode:          liveHostdRebindBaselineExecutionCode(err),
		RetainedCandidateCode:  liveHostdRebindBaselineCandidateCode(err),
		RetainedCompileCode:    liveHostdRebindBaselineCompileCode(err),
		RetainedRuntimeCode:    liveHostdRebindBaselineRuntimeCode(err),
		ErrorCanceled:          errors.Is(err, context.Canceled),
		ErrorDeadline:          errors.Is(err, context.DeadlineExceeded),
		ErrorTermination:       errors.Is(err, runtimeprocess.ErrTerminationFailed),
		ImageBoundaryRefusal:   generatedimage.IsGeneratedImageBoundaryFailure(err),
		NetworkBoundaryRefusal: generatedruntime.IsGeneratedNetworkBoundaryFailure(err),
		ReturnContextCanceled:  errors.Is(contextErr, context.Canceled),
		ReturnContextDeadline:  errors.Is(contextErr, context.DeadlineExceeded),
	}
}

func (observer *liveHostdRebindBaselineExecutionObserver) Snapshot(phase liveHostdRebindBaselinePhase) liveHostdRebindBaselineDiagnosticSnapshot {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.snapshotLocked(phase)
}

func (observer *liveHostdRebindBaselineExecutionObserver) FailureSnapshot() (liveHostdRebindBaselineDiagnosticSnapshot, bool) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.failure == "" {
		return liveHostdRebindBaselineDiagnosticSnapshot{}, false
	}
	return observer.snapshotLocked(observer.failure), true
}

func (observer *liveHostdRebindBaselineExecutionObserver) snapshotLocked(phase liveHostdRebindBaselinePhase) liveHostdRebindBaselineDiagnosticSnapshot {
	if index, observed := liveHostdRebindBaselinePhaseSlot(phase); observed {
		slot := observer.slots[index]
		if slot.begun {
			return slot.snapshot
		}
	}
	correlation := "unavailable"
	if observer.overflow {
		correlation = "overflow"
	}
	return liveHostdRebindBaselineUnavailableSnapshot(phase, correlation)
}

func liveHostdRebindBaselinePhaseSlot(phase liveHostdRebindBaselinePhase) (int, bool) {
	switch phase {
	case liveHostdRebindBaselineADeployPhase:
		return 0, true
	case liveHostdRebindBaselineBDeployPhase:
		return 1, true
	default:
		return 0, false
	}
}

func liveHostdRebindBaselineClosedPhase(phase liveHostdRebindBaselinePhase) liveHostdRebindBaselinePhase {
	switch phase {
	case liveHostdRebindBaselineADeployPhase, liveHostdRebindBaselineBDeployPhase, liveHostdRebindLegacyIdentityCapturePhase:
		return phase
	default:
		return liveHostdRebindBaselinePhase("unknown")
	}
}

func liveHostdRebindBaselineMarkAmbiguous(slot *liveHostdRebindBaselineExecutionSlot, phase liveHostdRebindBaselinePhase) {
	*slot = liveHostdRebindBaselineExecutionSlot{
		begun: true, ambiguous: true, phase: phase,
		snapshot: liveHostdRebindBaselineUnavailableSnapshot(phase, "ambiguous"),
	}
}

func liveHostdRebindBaselineSameIdentity(slot *liveHostdRebindBaselineExecutionSlot, identity liveHostdRebindBaselineExecutionIdentity) bool {
	return slot.jobID == identity.jobID && slot.appID == identity.appID && slot.attempt == identity.attempt
}

func liveHostdRebindBaselineValidDeploymentJob(job jobs.Job) bool {
	return job.ID != "" && job.Type == "deploy" && job.ResourceType == "application" && job.ResourceID != "" && job.Attempt > 0
}

func liveHostdRebindBaselineUnavailableSnapshot(
	phase liveHostdRebindBaselinePhase, correlation string,
) liveHostdRebindBaselineDiagnosticSnapshot {
	return liveHostdRebindBaselineDiagnosticSnapshot{
		FixturePhase:          liveHostdRebindBaselinePhaseLabel(phase),
		Correlation:           correlation,
		LastAcceptedJobPhase:  "unavailable",
		ReturnObservation:     "unavailable",
		ExecutionCode:         "unavailable",
		RetainedCandidateCode: "unavailable",
		RetainedCompileCode:   "unavailable",
		RetainedRuntimeCode:   "unavailable",
	}
}

func liveHostdRebindBaselinePhaseLabel(phase liveHostdRebindBaselinePhase) string {
	switch phase {
	case liveHostdRebindBaselineADeployPhase, liveHostdRebindBaselineBDeployPhase, liveHostdRebindLegacyIdentityCapturePhase:
		return string(phase)
	case "":
		return "unavailable"
	default:
		return "unknown"
	}
}

func liveHostdRebindBaselineJobPhaseLabel(phase string) string {
	switch phase {
	case "validate", "prepare_workspace", "materialize_release", "apply_runtime", "wait_for_health", "finalize":
		return phase
	default:
		return "unknown"
	}
}

func liveHostdRebindBaselineExecutionCode(err error) string {
	if err == nil {
		return "none"
	}
	var execution *jobs.ExecutionError
	if !errors.As(err, &execution) || execution == nil {
		return "unknown"
	}
	return lanTwoAppJobCodeLabel(execution.Code)
}

func liveHostdRebindBaselineCandidateCode(err error) string {
	if err == nil {
		return "none"
	}
	var candidate *deployments.GeneratedCandidateError
	if !errors.As(err, &candidate) || candidate == nil {
		return "none"
	}
	switch candidate.Code {
	case "validation_failed", "runtime_unavailable", "runtime_timeout", "process_termination_failed", "runtime_output_truncated",
		"image_unavailable", "image_drift_detected", "network_drift_detected", "network_provision_failed",
		"candidate_slot_occupied", "candidate_create_failed", "candidate_start_failed", "candidate_hardening_failed",
		"candidate_unhealthy", "candidate_exited", "candidate_cleanup_failed", "insufficient_replacement_capacity",
		"configuration_unavailable", "internal_error", "cancelled", "retirement_unavailable":
		return candidate.Code
	default:
		return "unknown"
	}
}

func liveHostdRebindBaselineCompileCode(err error) string {
	if err == nil {
		return "none"
	}
	var compile *generatedimage.CompileError
	if !errors.As(err, &compile) || compile == nil {
		return "none"
	}
	switch compile.Code {
	case "validation_failed", "source_integrity_failed", "deployment_plan_review_required", "configuration_review_required",
		"configuration_unavailable", "unsupported_runtime", "build_configuration_requires_build_command", "authority_unavailable",
		"build_in_progress", "image_result_unavailable", "image_validation_uncertain", "image_selection_invalid",
		"build_cancelled", "build_failed", "runtime_unavailable", "process_termination_failed", "internal_error":
		return compile.Code
	default:
		return "unknown"
	}
}

func liveHostdRebindBaselineRuntimeCode(err error) string {
	if err == nil {
		return "none"
	}
	var runtimeErr *generatedruntime.Error
	if !errors.As(err, &runtimeErr) || runtimeErr == nil {
		return "none"
	}
	switch runtimeErr.Code {
	case generatedruntime.DiagnosticValidationFailed, generatedruntime.DiagnosticRuntimeUnavailable,
		generatedruntime.DiagnosticRuntimeTimeout, generatedruntime.DiagnosticProcessTerminationFailed,
		generatedruntime.DiagnosticRuntimeOutputTruncated, generatedruntime.DiagnosticImageUnavailable,
		generatedruntime.DiagnosticImageDriftDetected, generatedruntime.DiagnosticNetworkDriftDetected,
		generatedruntime.DiagnosticNetworkProvisionFailed, generatedruntime.DiagnosticCandidateSlotOccupied,
		generatedruntime.DiagnosticCandidateCreateFailed, generatedruntime.DiagnosticCandidateStartFailed,
		generatedruntime.DiagnosticCandidateHardeningFailed, generatedruntime.DiagnosticCandidateUnhealthy,
		generatedruntime.DiagnosticCandidateExited, generatedruntime.DiagnosticCandidateCleanupFailed,
		generatedruntime.DiagnosticInsufficientReplacementSpace, generatedruntime.DiagnosticConfigurationUnavailable,
		generatedruntime.DiagnosticInternalError, generatedruntime.DiagnosticCancelled:
		return string(runtimeErr.Code)
	default:
		return "unknown"
	}
}

func liveHostdRebindBaselineFailureDiagnosticLine(observer *liveHostdRebindBaselineExecutionObserver) (string, bool) {
	if observer == nil {
		return "", false
	}
	snapshot, failed := observer.FailureSnapshot()
	if !failed {
		return "", false
	}
	return fmt.Sprintf("M4 rebind baseline failure diagnostic: fixture_phase=%s correlation=%s last_accepted_job_phase=%s return_observation=%s execution_code=%s retained_candidate_code=%s retained_compile_code=%s retained_runtime_code=%s error_canceled=%t error_deadline=%t error_termination=%t image_boundary_refusal=%t network_boundary_refusal=%t return_context_canceled=%t return_context_deadline=%t",
		snapshot.FixturePhase, snapshot.Correlation, snapshot.LastAcceptedJobPhase, snapshot.ReturnObservation,
		snapshot.ExecutionCode, snapshot.RetainedCandidateCode, snapshot.RetainedCompileCode, snapshot.RetainedRuntimeCode,
		snapshot.ErrorCanceled, snapshot.ErrorDeadline, snapshot.ErrorTermination, snapshot.ImageBoundaryRefusal,
		snapshot.NetworkBoundaryRefusal, snapshot.ReturnContextCanceled, snapshot.ReturnContextDeadline), true
}

func liveHostdRebindRunBaselinePhase(
	observer *liveHostdRebindBaselineExecutionObserver, phase liveHostdRebindBaselinePhase, run func(),
) {
	observer.Begin(phase)
	completed := false
	defer func() {
		if !completed {
			observer.MarkFixtureFailure(phase)
		}
	}()
	run()
	completed = true
}

func liveHostdRebindStopBaselineWorker(
	stop func(), done <-chan struct{}, timeout func(), logLine func(string), observer *liveHostdRebindBaselineExecutionObserver,
) {
	liveHostdRebindStopBaselineWorkerWithDeadline(
		stop, done, time.After(liveHostdRebindBaselineWorkerStopBudget), timeout, logLine, observer,
	)
}

func liveHostdRebindStopBaselineWorkerWithDeadline(
	stop func(), done <-chan struct{}, deadline <-chan time.Time, timeout func(), logLine func(string), observer *liveHostdRebindBaselineExecutionObserver,
) {
	stop()
	select {
	case <-done:
	case <-deadline:
		timeout()
	}
	if line, failed := liveHostdRebindBaselineFailureDiagnosticLine(observer); failed {
		logLine(line)
	}
}

type liveHostdRebindExecutorFunc func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error)

func (execute liveHostdRebindExecutorFunc) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	return execute(ctx, job, reporter)
}

type liveHostdRebindReporterFunc func(jobs.ProgressUpdate) error

func (report liveHostdRebindReporterFunc) Report(update jobs.ProgressUpdate) error {
	return report(update)
}

type liveHostdRebindPanicError struct{}

func (liveHostdRebindPanicError) Error() string { panic("hostile error text") }

type liveHostdRebindReentrantError struct {
	observer *liveHostdRebindBaselineExecutionObserver
}

func (err liveHostdRebindReentrantError) Error() string { return "reentrant classifier error" }

func (err liveHostdRebindReentrantError) Is(error) bool {
	err.observer.Snapshot(liveHostdRebindBaselineADeployPhase)
	return false
}

type liveHostdRebindPanicIsError struct{}

func (liveHostdRebindPanicIsError) Error() string { return "panic-is-error" }
func (liveHostdRebindPanicIsError) Is(error) bool { panic("classification-is-secret") }

type liveHostdRebindPanicAsError struct{}

func (liveHostdRebindPanicAsError) Error() string { return "panic-as-error" }
func (liveHostdRebindPanicAsError) As(any) bool   { panic("classification-as-secret") }

type liveHostdRebindPanicUnwrapError struct{}

func (liveHostdRebindPanicUnwrapError) Error() string { return "panic-unwrap-error" }
func (liveHostdRebindPanicUnwrapError) Unwrap() error { panic("classification-unwrap-secret") }

type liveHostdRebindPanicContext struct{}

func (liveHostdRebindPanicContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (liveHostdRebindPanicContext) Done() <-chan struct{}       { return nil }
func (liveHostdRebindPanicContext) Err() error                  { panic("classification-context-secret") }
func (liveHostdRebindPanicContext) Value(any) any               { return nil }

func liveHostdRebindBaselineTestJob(id, app string, attempt int) jobs.Job {
	return jobs.Job{ID: id, Type: "deploy", ResourceType: "application", ResourceID: app, Attempt: attempt}
}

func TestLiveHostdRebindBaselineExecutorForwardsExactly(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "context-value")
	job := liveHostdRebindBaselineTestJob("job-a", "app-a", 1)
	wantResult := jobs.ExecutionResult{CompletionCode: "completed", Disposition: jobs.ExecutionWaitingUser, PauseDisposition: "paused"}
	wantErr := errors.New("returned exactly")
	var calls int
	observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(gotCtx context.Context, gotJob jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			calls++
			if gotCtx != ctx || !reflect.DeepEqual(gotJob, job) || reporter == nil {
				t.Fatal("observer changed executor inputs")
			}
			return wantResult, wantErr
		},
	))
	observer.Begin(liveHostdRebindBaselineADeployPhase)
	gotResult, gotErr := observer.Execute(ctx, job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
	if calls != 1 || gotResult != wantResult || gotErr != wantErr {
		t.Fatalf("forward result=%#v error=%v calls=%d", gotResult, gotErr, calls)
	}
	if snapshot := observer.Snapshot(liveHostdRebindBaselineADeployPhase); snapshot.ReturnObservation != "returned" || snapshot.ExecutionCode != "unknown" {
		t.Fatalf("return snapshot=%+v", snapshot)
	}

	t.Run("nil error", func(t *testing.T) {
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				return wantResult, nil
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		result, err := observer.Execute(ctx, job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		if result != wantResult || err != nil || observer.Snapshot(liveHostdRebindBaselineADeployPhase).ExecutionCode != "none" {
			t.Fatalf("nil forwarding result=%#v error=%v", result, err)
		}
	})

	t.Run("nil reporter", func(t *testing.T) {
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				if reporter != nil {
					t.Fatal("observer replaced nil reporter")
				}
				return wantResult, nil
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		result, err := observer.Execute(ctx, job, nil)
		if result != wantResult || err != nil {
			t.Fatalf("nil reporter result=%#v error=%v", result, err)
		}
	})

	t.Run("nil context", func(t *testing.T) {
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(gotCtx context.Context, _ jobs.Job, _ jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				if gotCtx != nil {
					t.Fatal("observer replaced nil context")
				}
				return wantResult, nil
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		result, err := observer.Execute(nil, job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		if result != wantResult || err != nil {
			t.Fatalf("nil context result=%#v error=%v", result, err)
		}
	})

	t.Run("panic", func(t *testing.T) {
		panicValue := &struct{ marker string }{marker: "same panic"}
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				panic(panicValue)
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("panic=%v", recovered)
			}
			snapshot := observer.Snapshot(liveHostdRebindBaselineADeployPhase)
			if snapshot.ReturnObservation != "unavailable" {
				t.Fatalf("panic return snapshot=%+v", snapshot)
			}
		}()
		observer.Execute(ctx, job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
	})

	t.Run("reporter panic", func(t *testing.T) {
		panicValue := &struct{ marker string }{marker: "same reporter panic"}
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				reporter.Report(jobs.ProgressUpdate{Phase: "apply_runtime"})
				return wantResult, nil
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		defer func() {
			if recovered := recover(); recovered != panicValue {
				t.Fatalf("reporter panic=%v", recovered)
			}
			snapshot := observer.Snapshot(liveHostdRebindBaselineADeployPhase)
			if snapshot.ReturnObservation != "unavailable" {
				t.Fatalf("reporter panic return snapshot=%+v", snapshot)
			}
		}()
		observer.Execute(ctx, job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { panic(panicValue) }))
	})
}

func TestLiveHostdRebindBaselineReporterForwardsOnlyAcceptedUpdates(t *testing.T) {
	ctx := context.Background()
	job := liveHostdRebindBaselineTestJob("job-a", "app-a", 1)
	update := jobs.ProgressUpdate{Status: jobs.Running, Phase: "apply_runtime", Progress: 73, Checkpoint: "checkpoint-secret", Level: "warn", Code: "phase-secret", Message: "message-secret"}
	var reported []jobs.ProgressUpdate
	observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			return jobs.ExecutionResult{}, reporter.Report(update)
		},
	))
	observer.Begin(liveHostdRebindBaselineADeployPhase)
	if _, err := observer.Execute(ctx, job, liveHostdRebindReporterFunc(func(got jobs.ProgressUpdate) error {
		reported = append(reported, got)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0] != update || observer.Snapshot(liveHostdRebindBaselineADeployPhase).LastAcceptedJobPhase != "apply_runtime" {
		t.Fatalf("reported=%#v snapshot=%+v", reported, observer.Snapshot(liveHostdRebindBaselineADeployPhase))
	}

	reporterErr := errors.New("report failure")
	failed := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			return jobs.ExecutionResult{}, reporter.Report(update)
		},
	))
	failed.Begin(liveHostdRebindBaselineADeployPhase)
	if _, err := failed.Execute(ctx, job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return reporterErr })); err != reporterErr {
		t.Fatalf("reporter error=%v", err)
	}
	if snapshot := failed.Snapshot(liveHostdRebindBaselineADeployPhase); snapshot.LastAcceptedJobPhase != "unavailable" {
		t.Fatalf("failed report advanced snapshot=%+v", snapshot)
	}
}

func TestLiveHostdRebindBaselineDiagnosticsAreClosedAndSanitized(t *testing.T) {
	const secret = "do-not-log-this-secret"
	job := liveHostdRebindBaselineTestJob("job-"+secret, "app-"+secret, 1)
	job.Input = []byte(secret)
	err := errors.Join(
		&jobs.ExecutionError{Code: "apply_failed", Detail: secret},
		&deployments.GeneratedCandidateError{Code: "candidate_create_failed"},
		&generatedimage.CompileError{Code: "build_failed"},
		&generatedruntime.Error{Code: generatedruntime.DiagnosticRuntimeUnavailable},
		runtimeprocess.ErrTerminationFailed,
		context.Canceled,
		liveHostdRebindPanicError{},
	)
	observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			if reportErr := reporter.Report(jobs.ProgressUpdate{Phase: "phase-" + secret, Checkpoint: secret, Code: secret, Message: secret}); reportErr != nil {
				return jobs.ExecutionResult{}, reportErr
			}
			return jobs.ExecutionResult{}, err
		},
	))
	observer.Begin(liveHostdRebindBaselineADeployPhase)
	if _, gotErr := observer.Execute(context.Background(), job, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil })); gotErr != err {
		t.Fatal("observer changed hostile error")
	}
	observer.MarkFixtureFailure(liveHostdRebindBaselineADeployPhase)
	line, failed := liveHostdRebindBaselineFailureDiagnosticLine(observer)
	if !failed || strings.Contains(line, secret) || strings.Contains(line, "hostile error text") {
		t.Fatalf("unsafe diagnostic line=%q", line)
	}
	snapshot := observer.Snapshot(liveHostdRebindBaselineADeployPhase)
	if snapshot.LastAcceptedJobPhase != "unknown" || snapshot.ExecutionCode != "apply_failed" ||
		snapshot.RetainedCandidateCode != "candidate_create_failed" || snapshot.RetainedCompileCode != "build_failed" ||
		snapshot.RetainedRuntimeCode != "runtime_unavailable" || !snapshot.ErrorCanceled || !snapshot.ErrorTermination || !snapshot.ImageBoundaryRefusal {
		t.Fatalf("sanitized snapshot=%+v", snapshot)
	}
}

func TestLiveHostdRebindBaselineClassificationDoesNotHoldObserverLock(t *testing.T) {
	var observer *liveHostdRebindBaselineExecutionObserver
	var returnErr error
	observer = newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			return jobs.ExecutionResult{}, returnErr
		},
	))
	returnErr = liveHostdRebindReentrantError{observer: observer}
	observer.Begin(liveHostdRebindBaselineADeployPhase)
	done := make(chan error, 1)
	go func() {
		_, err := observer.Execute(context.Background(), liveHostdRebindBaselineTestJob("job-a", "app-a", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		done <- err
	}()
	select {
	case err := <-done:
		if err != returnErr {
			t.Fatalf("reentrant error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant error classifier held observer lock")
	}
}

func TestLiveHostdRebindBaselineClassificationPanicsAreUnavailableAndForwarded(t *testing.T) {
	sentinel := errors.New("delegate-sentinel")
	wantResult := jobs.ExecutionResult{CompletionCode: "delegate-result"}
	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{name: "is", ctx: context.Background(), err: errors.Join(sentinel, liveHostdRebindPanicIsError{})},
		{name: "as", ctx: context.Background(), err: errors.Join(sentinel, liveHostdRebindPanicAsError{})},
		{name: "unwrap", ctx: context.Background(), err: errors.Join(sentinel, liveHostdRebindPanicUnwrapError{})},
		{name: "context", ctx: liveHostdRebindPanicContext{}, err: sentinel},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
				func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
					return wantResult, test.err
				},
			))
			observer.Begin(liveHostdRebindBaselineADeployPhase)
			result, err := observer.Execute(test.ctx, liveHostdRebindBaselineTestJob("job-a", "app-a", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
			if result != wantResult || err != test.err {
				t.Fatalf("classification panic changed result=%#v error=%v", result, err)
			}
			observer.MarkFixtureFailure(liveHostdRebindBaselineADeployPhase)
			snapshot, failed := observer.FailureSnapshot()
			if !failed || snapshot.ReturnObservation != "returned" || snapshot.ExecutionCode != "unavailable" ||
				snapshot.RetainedCandidateCode != "unavailable" || snapshot.RetainedCompileCode != "unavailable" ||
				snapshot.RetainedRuntimeCode != "unavailable" || snapshot.ErrorCanceled || snapshot.ErrorDeadline ||
				snapshot.ErrorTermination || snapshot.ImageBoundaryRefusal || snapshot.NetworkBoundaryRefusal ||
				snapshot.ReturnContextCanceled || snapshot.ReturnContextDeadline {
				t.Fatalf("classification panic snapshot=%+v failed=%t", snapshot, failed)
			}
			line, logged := liveHostdRebindBaselineFailureDiagnosticLine(observer)
			if !logged || strings.Contains(line, "classification-") {
				t.Fatalf("classification panic line=%q logged=%t", line, logged)
			}
		})
	}
}

func TestLiveHostdRebindBaselineCorrelationIsBounded(t *testing.T) {
	t.Run("mismatched identity is ambiguous", func(t *testing.T) {
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				return jobs.ExecutionResult{}, errors.New("first")
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		first := liveHostdRebindBaselineTestJob("job-a", "app-a", 1)
		observer.Execute(context.Background(), first, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		for _, mismatched := range []jobs.Job{
			liveHostdRebindBaselineTestJob("job-b", "app-a", 1),
			liveHostdRebindBaselineTestJob("job-a", "app-b", 1),
			liveHostdRebindBaselineTestJob("job-a", "app-a", 2),
		} {
			observer.Execute(context.Background(), mismatched, liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		}
		observer.MarkFixtureFailure(liveHostdRebindBaselineADeployPhase)
		snapshot, failed := observer.FailureSnapshot()
		if !failed || snapshot.Correlation != "ambiguous" || snapshot.ReturnObservation != "unavailable" || snapshot.ExecutionCode != "unavailable" {
			t.Fatalf("mismatch snapshot=%+v failed=%t", snapshot, failed)
		}
	})

	t.Run("third deployment overflows", func(t *testing.T) {
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				return jobs.ExecutionResult{}, nil
			},
		))
		for _, phase := range []liveHostdRebindBaselinePhase{liveHostdRebindBaselineADeployPhase, liveHostdRebindBaselineBDeployPhase} {
			observer.Begin(phase)
			observer.Execute(context.Background(), liveHostdRebindBaselineTestJob(string(phase), "app-"+string(phase), 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		}
		observer.Begin(liveHostdRebindBaselinePhase("unexpected"))
		observer.Execute(context.Background(), liveHostdRebindBaselineTestJob("job-c", "app-c", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		observer.MarkFixtureFailure(liveHostdRebindBaselinePhase("unexpected"))
		snapshot, failed := observer.FailureSnapshot()
		if !failed || snapshot.FixturePhase != "unknown" || snapshot.Correlation != "overflow" || snapshot.ReturnObservation != "unavailable" {
			t.Fatalf("overflow snapshot=%+v failed=%t", snapshot, failed)
		}
	})

	t.Run("panic leaves only accepted phase", func(t *testing.T) {
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				if err := reporter.Report(jobs.ProgressUpdate{Phase: "apply_runtime"}); err != nil {
					return jobs.ExecutionResult{}, err
				}
				panic("missing return")
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		func() {
			defer func() { _ = recover() }()
			observer.Execute(context.Background(), liveHostdRebindBaselineTestJob("job-a", "app-a", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		}()
		observer.MarkFixtureFailure(liveHostdRebindBaselineADeployPhase)
		snapshot, failed := observer.FailureSnapshot()
		if !failed || snapshot.LastAcceptedJobPhase != "apply_runtime" || snapshot.ReturnObservation != "unavailable" || snapshot.ExecutionCode != "unavailable" {
			t.Fatalf("missing-return snapshot=%+v failed=%t", snapshot, failed)
		}
	})
}

func TestLiveHostdRebindBaselineSnapshotAndCleanupOrdering(t *testing.T) {
	observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			var reports sync.WaitGroup
			for range 24 {
				reports.Add(1)
				go func() {
					defer reports.Done()
					if err := reporter.Report(jobs.ProgressUpdate{Phase: "apply_runtime"}); err != nil {
						t.Errorf("concurrent report: %v", err)
					}
				}()
			}
			reports.Wait()
			return jobs.ExecutionResult{}, errors.New("baseline failure")
		},
	))
	observer.Begin(liveHostdRebindBaselineADeployPhase)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		observer.Execute(context.Background(), liveHostdRebindBaselineTestJob("job-a", "app-a", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
	}()
	for range 100 {
		snapshot := observer.Snapshot(liveHostdRebindBaselineADeployPhase)
		if snapshot.LastAcceptedJobPhase != "unavailable" && snapshot.LastAcceptedJobPhase != "apply_runtime" {
			t.Fatalf("incoherent concurrent snapshot=%+v", snapshot)
		}
	}
	<-finished
	observer.MarkFixtureFailure(liveHostdRebindBaselineADeployPhase)

	done := make(chan struct{})
	var events []string
	liveHostdRebindStopBaselineWorker(
		func() { events = append(events, "stop"); close(done) },
		done,
		func() { events = append(events, "timeout") },
		func(line string) {
			if !strings.Contains(line, "fixture_phase=baseline_a_deploy") {
				t.Fatalf("unexpected cleanup line=%q", line)
			}
			events = append(events, "log")
		},
		observer,
	)
	if strings.Join(events, ",") != "stop,log" {
		t.Fatalf("cleanup ordering=%v", events)
	}

	t.Run("timeout logs missing return", func(t *testing.T) {
		release := make(chan struct{})
		reported := make(chan struct{})
		exited := make(chan struct{})
		observer := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
			func(_ context.Context, _ jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
				if err := reporter.Report(jobs.ProgressUpdate{Phase: "apply_runtime"}); err != nil {
					return jobs.ExecutionResult{}, err
				}
				close(reported)
				<-release
				return jobs.ExecutionResult{}, nil
			},
		))
		observer.Begin(liveHostdRebindBaselineADeployPhase)
		go func() {
			defer close(exited)
			observer.Execute(context.Background(), liveHostdRebindBaselineTestJob("job-timeout", "app-timeout", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
		}()
		<-reported
		defer func() {
			close(release)
			<-exited
		}()

		observer.MarkFixtureFailure(liveHostdRebindBaselineADeployPhase)
		deadline := make(chan time.Time)
		close(deadline)
		workerDone := make(chan struct{})
		var timeoutEvents []string
		var timeoutLine string
		liveHostdRebindStopBaselineWorkerWithDeadline(
			func() { timeoutEvents = append(timeoutEvents, "stop") },
			workerDone,
			deadline,
			func() { timeoutEvents = append(timeoutEvents, "timeout") },
			func(line string) {
				timeoutLine = line
				timeoutEvents = append(timeoutEvents, "log")
			},
			observer,
		)
		if strings.Join(timeoutEvents, ",") != "stop,timeout,log" {
			t.Fatalf("timeout cleanup ordering=%v", timeoutEvents)
		}
		if !strings.Contains(timeoutLine, "last_accepted_job_phase=apply_runtime") ||
			!strings.Contains(timeoutLine, "return_observation=unavailable") {
			t.Fatalf("timeout cleanup line=%q", timeoutLine)
		}
	})

	success := newLiveHostdRebindBaselineExecutionObserver(liveHostdRebindExecutorFunc(
		func(context.Context, jobs.Job, jobs.ProgressReporter) (jobs.ExecutionResult, error) {
			return jobs.ExecutionResult{}, nil
		},
	))
	liveHostdRebindRunBaselinePhase(success, liveHostdRebindBaselineADeployPhase, func() {
		success.Execute(context.Background(), liveHostdRebindBaselineTestJob("job-success", "app-success", 1), liveHostdRebindReporterFunc(func(jobs.ProgressUpdate) error { return nil }))
	})
	closed := make(chan struct{})
	close(closed)
	logged := false
	liveHostdRebindStopBaselineWorker(func() {}, closed, func() { t.Fatal("success cleanup timed out") }, func(string) { logged = true }, success)
	if logged {
		t.Fatal("successful baseline emitted a failure diagnostic")
	}
}
