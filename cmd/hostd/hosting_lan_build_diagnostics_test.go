package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const (
	lanTwoAppBuildDiagnosticCounterLimit = 1 << 20
	lanTwoAppBuildArtifactLimit          = 64
	lanTwoAppBuildActiveStartLimit       = 64
	lanTwoAppBuildElapsedLimit           = int64((30 * time.Minute) / time.Millisecond)
)

type lanTwoAppBuildOperation uint8

const (
	lanTwoAppBuildOperationSetup lanTwoAppBuildOperation = iota
	lanTwoAppBuildOperationLoad
	lanTwoAppBuildOperationInspect
	lanTwoAppBuildOperationOther
	lanTwoAppBuildOperationCount
)

func (operation lanTwoAppBuildOperation) label() string {
	switch operation {
	case lanTwoAppBuildOperationSetup:
		return "builder_setup"
	case lanTwoAppBuildOperationLoad:
		return "buildx_build_load"
	case lanTwoAppBuildOperationInspect:
		return "image_inspect"
	default:
		return "other"
	}
}

type lanTwoAppBuildCommandOperationSnapshot struct {
	Entered               int
	Returned              int
	InFlight              bool
	ReturnedElapsedMillis int64
	InFlightElapsedMillis int64
	InFlightElapsedKnown  bool
	ReturnStatus          string
	StdoutTruncated       bool
	StderrTruncated       bool
}

type lanTwoAppBuildCommandOperationState struct {
	lanTwoAppBuildCommandOperationSnapshot
	activeCount          int
	activeStarts         [lanTwoAppBuildActiveStartLimit]time.Time
	activeStartCount     int
	activeStartsOverflow bool
}

type lanTwoAppBuildCommandDiagnosticSnapshot struct {
	Operations [lanTwoAppBuildOperationCount]lanTwoAppBuildCommandOperationSnapshot
}

type lanTwoAppBuildCommandDiagnosticRunner struct {
	delegate runtimeprocess.CommandRunner
	now      func() time.Time

	mu         sync.Mutex
	operations [lanTwoAppBuildOperationCount]lanTwoAppBuildCommandOperationState
}

func newLanTwoAppBuildCommandDiagnosticRunner(delegate runtimeprocess.CommandRunner) *lanTwoAppBuildCommandDiagnosticRunner {
	return &lanTwoAppBuildCommandDiagnosticRunner{delegate: delegate, now: time.Now}
}

func (runner *lanTwoAppBuildCommandDiagnosticRunner) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if runner == nil || runner.delegate == nil {
		return runtimeprocess.CommandResult{}, errors.New("LAN build diagnostic runner is unavailable")
	}
	operation := lanTwoAppBuildOperationFor(request)
	started := runner.clockNow()
	runner.recordEntered(operation, started)
	result, err := runner.delegate.Run(ctx, request)
	runner.recordReturned(operation, started, runner.clockNow().Sub(started), result, err)
	return result, err
}

func (runner *lanTwoAppBuildCommandDiagnosticRunner) clockNow() time.Time {
	if runner == nil || runner.now == nil {
		return time.Now()
	}
	return runner.now()
}

func (runner *lanTwoAppBuildCommandDiagnosticRunner) recordEntered(operation lanTwoAppBuildOperation, started time.Time) {
	if runner == nil || operation >= lanTwoAppBuildOperationCount {
		return
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	entry := &runner.operations[operation]
	entry.Entered = lanTwoAppBuildDiagnosticIncrement(entry.Entered)
	entry.activeCount++
	if !entry.activeStartsOverflow && entry.activeStartCount < len(entry.activeStarts) {
		entry.activeStarts[entry.activeStartCount] = started
		entry.activeStartCount++
	} else {
		entry.activeStartsOverflow = true
	}
}

func (runner *lanTwoAppBuildCommandDiagnosticRunner) recordReturned(operation lanTwoAppBuildOperation, started time.Time, elapsed time.Duration, result runtimeprocess.CommandResult, err error) {
	if runner == nil || operation >= lanTwoAppBuildOperationCount {
		return
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	entry := &runner.operations[operation]
	entry.Returned = lanTwoAppBuildDiagnosticIncrement(entry.Returned)
	if entry.activeCount > 0 {
		entry.activeCount--
	}
	if !entry.activeStartsOverflow {
		for index := 0; index < entry.activeStartCount; index++ {
			if entry.activeStarts[index] != started {
				continue
			}
			entry.activeStartCount--
			entry.activeStarts[index] = entry.activeStarts[entry.activeStartCount]
			entry.activeStarts[entry.activeStartCount] = time.Time{}
			break
		}
	}
	if entry.activeCount == 0 {
		entry.activeStartCount = 0
		entry.activeStartsOverflow = false
		clear(entry.activeStarts[:])
	}
	if duration := lanTwoAppBuildElapsedMillis(elapsed); duration > entry.ReturnedElapsedMillis {
		entry.ReturnedElapsedMillis = duration
	}
	entry.ReturnStatus = lanTwoAppBuildDiagnosticMergeStatus(entry.ReturnStatus, lanTwoAppBuildCommandResultStatus(err))
	entry.StdoutTruncated = entry.StdoutTruncated || result.StdoutTruncated
	entry.StderrTruncated = entry.StderrTruncated || result.StderrTruncated
}

func (runner *lanTwoAppBuildCommandDiagnosticRunner) Snapshot() lanTwoAppBuildCommandDiagnosticSnapshot {
	if runner == nil {
		return lanTwoAppBuildCommandDiagnosticSnapshot{}
	}
	now := runner.clockNow()
	runner.mu.Lock()
	defer runner.mu.Unlock()
	var snapshot lanTwoAppBuildCommandDiagnosticSnapshot
	for operation := lanTwoAppBuildOperation(0); operation < lanTwoAppBuildOperationCount; operation++ {
		state := runner.operations[operation]
		entry := state.lanTwoAppBuildCommandOperationSnapshot
		entry.InFlight = state.activeCount > 0
		entry.InFlightElapsedMillis = 0
		entry.InFlightElapsedKnown = false
		if entry.InFlight && !state.activeStartsOverflow && state.activeStartCount == state.activeCount {
			oldest := state.activeStarts[0]
			for index := 1; index < state.activeStartCount; index++ {
				if state.activeStarts[index].Before(oldest) {
					oldest = state.activeStarts[index]
				}
			}
			entry.InFlightElapsedMillis = lanTwoAppBuildElapsedMillis(now.Sub(oldest))
			entry.InFlightElapsedKnown = true
		}
		snapshot.Operations[operation] = entry
	}
	return snapshot
}

func lanTwoAppBuildDiagnosticIncrement(value int) int {
	if value >= lanTwoAppBuildDiagnosticCounterLimit {
		return lanTwoAppBuildDiagnosticCounterLimit
	}
	return value + 1
}

func lanTwoAppBuildElapsedMillis(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	milliseconds := value.Milliseconds()
	if milliseconds > lanTwoAppBuildElapsedLimit {
		return lanTwoAppBuildElapsedLimit
	}
	return milliseconds
}

func lanTwoAppBuildDiagnosticMergeStatus(current, next string) string {
	if current == "termination_failed" || next == "termination_failed" {
		return "termination_failed"
	}
	if current == "" || current == "unavailable" {
		return next
	}
	if current == next {
		return current
	}
	return "mixed"
}

func lanTwoAppBuildCommandResultStatus(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, runtimeprocess.ErrTerminationFailed):
		return "termination_failed"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "failed"
	}
}

func lanTwoAppBuildOperationFor(request runtimeprocess.CommandRequest) lanTwoAppBuildOperation {
	if len(request.Args) >= 2 && request.Args[0] == "buildx" && request.Args[1] == "build" && lanTwoAppBuildHasArgument(request.Args, "--load") {
		return lanTwoAppBuildOperationLoad
	}
	if len(request.Args) >= 2 && request.Args[0] == "image" && request.Args[1] == "inspect" {
		return lanTwoAppBuildOperationInspect
	}
	if len(request.Args) >= 2 && request.Args[0] == "buildx" && lanTwoAppBuildIsSetupVerb(request.Args[1]) {
		return lanTwoAppBuildOperationSetup
	}
	return lanTwoAppBuildOperationOther
}

func lanTwoAppBuildIsSetupVerb(verb string) bool {
	switch verb {
	case "create", "inspect", "ls", "rm":
		return true
	default:
		return false
	}
}

func lanTwoAppBuildHasArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
}

type lanTwoAppWorkerReturnSnapshot struct {
	Returned          bool
	ErrorPresent      bool
	ErrorCanceled     bool
	ErrorDeadline     bool
	ReturnWasCanceled bool
	ReturnWasDeadline bool
}

type lanTwoAppWorkerReturnObserver struct {
	mu       sync.Mutex
	snapshot lanTwoAppWorkerReturnSnapshot
}

func (observer *lanTwoAppWorkerReturnObserver) Record(err, contextErr error) {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.snapshot.Returned {
		return
	}
	observer.snapshot = lanTwoAppWorkerReturnSnapshot{
		Returned:          true,
		ErrorPresent:      err != nil,
		ErrorCanceled:     errors.Is(err, context.Canceled),
		ErrorDeadline:     errors.Is(err, context.DeadlineExceeded),
		ReturnWasCanceled: errors.Is(contextErr, context.Canceled),
		ReturnWasDeadline: errors.Is(contextErr, context.DeadlineExceeded),
	}
}

func (observer *lanTwoAppWorkerReturnObserver) Snapshot() lanTwoAppWorkerReturnSnapshot {
	if observer == nil {
		return lanTwoAppWorkerReturnSnapshot{}
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.snapshot
}

type lanTwoAppBuildReleaseSnapshot struct {
	ID         string
	AppID      string
	PlanID     string
	PlanNumber int64
	Ready      bool
}

type lanTwoAppBuildPlanSnapshot struct {
	ID             string
	AppID          string
	RevisionNumber int64
	ComponentNames []string
	Accepted       bool
}

type lanTwoAppBuildArtifactSnapshot struct {
	ComponentName string
	State         string
	AttemptNumber int64
}

type lanTwoAppBuildArtifactRows struct {
	Rows      []lanTwoAppBuildArtifactSnapshot
	Truncated bool
}

type lanTwoAppBuildReleaseReader func(context.Context, string, string) (lanTwoAppBuildReleaseSnapshot, error)
type lanTwoAppBuildPlanReader func(context.Context, string, string, int64) (lanTwoAppBuildPlanSnapshot, error)
type lanTwoAppBuildArtifactReader func(context.Context, string, string, int64) (lanTwoAppBuildArtifactRows, error)

func lanTwoAppBuildArtifactReaderFor(db *sql.DB) lanTwoAppBuildArtifactReader {
	return func(ctx context.Context, releaseID, planID string, planNumber int64) (lanTwoAppBuildArtifactRows, error) {
		if db == nil || ctx == nil || releaseID == "" || planID == "" || planNumber < 1 {
			return lanTwoAppBuildArtifactRows{}, errors.New("LAN build artifact diagnostic reader is unavailable")
		}
		rows, err := db.QueryContext(ctx, `SELECT component_id,state,attempt_number
			FROM generated_image_artifacts
			WHERE release_id=? AND deployment_plan_revision_id=? AND deployment_plan_revision_number=?
			ORDER BY component_id,attempt_number
			LIMIT ?`, releaseID, planID, planNumber, lanTwoAppBuildArtifactLimit+1)
		if err != nil {
			return lanTwoAppBuildArtifactRows{}, err
		}
		defer rows.Close()
		result := lanTwoAppBuildArtifactRows{Rows: make([]lanTwoAppBuildArtifactSnapshot, 0, lanTwoAppBuildArtifactLimit)}
		for rows.Next() {
			var value lanTwoAppBuildArtifactSnapshot
			if err := rows.Scan(&value.ComponentName, &value.State, &value.AttemptNumber); err != nil {
				return lanTwoAppBuildArtifactRows{}, err
			}
			if len(result.Rows) == lanTwoAppBuildArtifactLimit {
				result.Truncated = true
				continue
			}
			result.Rows = append(result.Rows, value)
		}
		if err := rows.Err(); err != nil {
			return lanTwoAppBuildArtifactRows{}, err
		}
		return result, nil
	}
}

type lanTwoAppBuildArtifactStateCounts struct {
	Building, Ready, Failed, Cancelled, Unavailable, Unknown int
	AttemptMax                                               int64
}

type lanTwoAppBuildArtifactObservation struct {
	Read   string
	Counts lanTwoAppBuildArtifactStateCounts
}

type lanTwoAppBuildFailureDiagnosticInput struct {
	Parent             context.Context
	Application        string
	Job                jobs.Job
	ExpectedPlanID     string
	ExpectedPlanNumber int64
	Deployments        lanTwoAppDeploymentList
	Runtime            lanTwoAppRuntimeGet
	Release            lanTwoAppBuildReleaseReader
	Plan               lanTwoAppBuildPlanReader
	Artifacts          lanTwoAppBuildArtifactReader
	Commands           func() lanTwoAppBuildCommandDiagnosticSnapshot
	Worker             func() lanTwoAppWorkerReturnSnapshot
}

type lanTwoAppBuildFailureDiagnosticDependencies struct {
	Release   lanTwoAppBuildReleaseReader
	Plan      lanTwoAppBuildPlanReader
	Artifacts lanTwoAppBuildArtifactReader
	Commands  func() lanTwoAppBuildCommandDiagnosticSnapshot
	Worker    func() lanTwoAppWorkerReturnSnapshot
}

type lanTwoAppBuildFailureObservation struct {
	CommandsAvailable, WorkerAvailable                                          bool
	JobRead, JobStatus, JobCode, RuntimeDiagnostic                              string
	DeploymentRead, DeploymentStatus, RuntimeRead, RuntimePhase, MigrationState string
	Components                                                                  lanTwoAppComponentStateCounts
	Artifacts                                                                   lanTwoAppBuildArtifactObservation
	Commands                                                                    lanTwoAppBuildCommandDiagnosticSnapshot
	Worker                                                                      lanTwoAppWorkerReturnSnapshot
}

func lanTwoAppBuildFailureOnlyDiagnostic(logFailure func()) (func(), func()) {
	completed := false
	failure := func() {
		if !completed && logFailure != nil {
			logFailure()
		}
	}
	return failure, func() { completed = true }
}

func lanTwoAppBuildFailureObservationFor(input lanTwoAppBuildFailureDiagnosticInput) lanTwoAppBuildFailureObservation {
	observation := lanTwoAppBuildFailureObservation{
		JobRead: "unavailable", JobStatus: "unavailable", JobCode: "unavailable", RuntimeDiagnostic: "unavailable",
		DeploymentRead: "unavailable", DeploymentStatus: "unavailable", RuntimeRead: "unavailable",
		RuntimePhase: "unavailable", MigrationState: "unavailable", Artifacts: lanTwoAppBuildArtifactObservation{Read: "unavailable"},
	}
	if input.Commands != nil {
		observation.CommandsAvailable = true
		observation.Commands = input.Commands()
	}
	if input.Worker != nil {
		observation.WorkerAvailable = true
		observation.Worker = input.Worker()
	}
	if input.Application == "" || input.Job.ID == "" || input.Job.Type != "deploy" || input.Job.ResourceType != "application" || input.Job.ResourceID != input.Application {
		observation.JobRead = "identity_mismatch"
		return observation
	}
	observation.JobRead = "ok"
	observation.JobStatus = lanTwoAppJobStatusLabel(input.Job.Status)
	observation.JobCode = lanTwoAppJobCodeLabel(input.Job.ErrorCode)
	if input.Deployments == nil || input.Runtime == nil {
		return observation
	}
	ctx, cancel := lanTwoAppFailureDiagnosticContext(input.Parent)
	defer cancel()
	history, err := input.Deployments(ctx, input.Application, lanTwoAppFailureHistoryLimit)
	if err != nil {
		observation.DeploymentRead = "failed"
		return observation
	}
	var deployment *deployments.Deployment
	for index := range history {
		if history[index].JobID != input.Job.ID {
			continue
		}
		if deployment != nil {
			observation.DeploymentRead = "ambiguous"
			return observation
		}
		deployment = &history[index]
	}
	if deployment == nil {
		observation.DeploymentRead = "missing"
		return observation
	}
	if deployment.ID == "" || deployment.AppID != input.Application {
		observation.DeploymentRead = "identity_mismatch"
		return observation
	}
	observation.DeploymentRead = "ok"
	observation.DeploymentStatus = lanTwoAppDeploymentStatusLabel(deployment.Status)
	runtimeState, err := input.Runtime(ctx, input.Application, deployment.ID)
	if err != nil {
		observation.RuntimeRead = "failed"
		return observation
	}
	if runtimeState.AppID != input.Application || runtimeState.DeploymentID != deployment.ID {
		observation.RuntimeRead = "identity_mismatch"
		return observation
	}
	observation.RuntimeRead = "ok"
	observation.RuntimeDiagnostic = lanTwoAppRuntimeDiagnosticLabel(runtimeState.DiagnosticCode)
	observation.RuntimePhase = lanTwoAppRuntimePhaseLabel(runtimeState.Phase)
	observation.MigrationState = lanTwoAppMigrationStateLabel(runtimeState.MigrationState)
	for _, component := range runtimeState.Components {
		switch component.State {
		case generatedruntimestate.ComponentPending:
			observation.Components.Pending++
		case generatedruntimestate.ComponentImageReady:
			observation.Components.ImageReady++
		case generatedruntimestate.ComponentStarting:
			observation.Components.Starting++
		case generatedruntimestate.ComponentRunning:
			observation.Components.Running++
		case generatedruntimestate.ComponentHealthy:
			observation.Components.Healthy++
		case generatedruntimestate.ComponentActive:
			observation.Components.Active++
		case generatedruntimestate.ComponentDraining:
			observation.Components.Draining++
		case generatedruntimestate.ComponentStopped:
			observation.Components.Stopped++
		case generatedruntimestate.ComponentFailed:
			observation.Components.Failed++
		default:
			observation.Components.Unknown++
		}
	}
	observation.Artifacts = lanTwoAppBuildArtifactObservationFor(ctx, input, *deployment, runtimeState)
	return observation
}

func lanTwoAppBuildArtifactObservationFor(ctx context.Context, input lanTwoAppBuildFailureDiagnosticInput, deployment deployments.Deployment, runtimeState generatedruntimestate.Deployment) lanTwoAppBuildArtifactObservation {
	observation := lanTwoAppBuildArtifactObservation{Read: "unavailable"}
	if input.Release == nil || input.Plan == nil || input.Artifacts == nil || input.ExpectedPlanID == "" || input.ExpectedPlanNumber < 1 ||
		deployment.ReleaseID == "" || deployment.DeploymentPlanRevisionID != input.ExpectedPlanID || deployment.DeploymentPlanRevisionNumber != input.ExpectedPlanNumber {
		observation.Read = "identity_mismatch"
		return observation
	}
	release, err := input.Release(ctx, input.Application, deployment.ReleaseID)
	if err != nil {
		observation.Read = "release_failed"
		return observation
	}
	if !release.Ready || release.ID != deployment.ReleaseID || release.AppID != input.Application || release.PlanID != input.ExpectedPlanID || release.PlanNumber != input.ExpectedPlanNumber {
		observation.Read = "identity_mismatch"
		return observation
	}
	plan, err := input.Plan(ctx, input.Application, input.ExpectedPlanID, input.ExpectedPlanNumber)
	if err != nil {
		observation.Read = "plan_failed"
		return observation
	}
	if !plan.Accepted || plan.ID != input.ExpectedPlanID || plan.AppID != input.Application || plan.RevisionNumber != input.ExpectedPlanNumber {
		observation.Read = "identity_mismatch"
		return observation
	}
	expectedComponents, valid := lanTwoAppBuildComponentSet(plan.ComponentNames)
	if !valid || runtimeState.ReleaseID != release.ID || runtimeState.DeploymentPlanRevisionID != plan.ID || runtimeState.DeploymentPlanRevisionNumber != plan.RevisionNumber ||
		!lanTwoAppBuildRuntimeComponentsMatch(runtimeState.Components, expectedComponents) {
		observation.Read = "identity_mismatch"
		return observation
	}
	artifacts, err := input.Artifacts(ctx, release.ID, plan.ID, plan.RevisionNumber)
	if err != nil {
		observation.Read = "failed"
		return observation
	}
	if artifacts.Truncated {
		observation.Read = "truncated"
		return observation
	}
	for _, artifact := range artifacts.Rows {
		if artifact.AttemptNumber < 1 || !expectedComponents[artifact.ComponentName] {
			observation.Read = "identity_mismatch"
			return observation
		}
	}
	for _, artifact := range artifacts.Rows {
		if artifact.AttemptNumber > observation.Counts.AttemptMax {
			observation.Counts.AttemptMax = artifact.AttemptNumber
		}
		switch artifact.State {
		case "building":
			observation.Counts.Building++
		case "ready":
			observation.Counts.Ready++
		case "failed":
			observation.Counts.Failed++
		case "cancelled":
			observation.Counts.Cancelled++
		case "unavailable":
			observation.Counts.Unavailable++
		default:
			observation.Counts.Unknown++
		}
	}
	observation.Read = "ok"
	return observation
}

func lanTwoAppBuildComponentSet(names []string) (map[string]bool, bool) {
	if len(names) == 0 || len(names) > lanTwoAppBuildArtifactLimit {
		return nil, false
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || set[name] {
			return nil, false
		}
		set[name] = true
	}
	return set, true
}

func lanTwoAppBuildRuntimeComponentsMatch(components []generatedruntimestate.Component, expected map[string]bool) bool {
	if len(components) != len(expected) {
		return false
	}
	seen := make(map[string]bool, len(components))
	for _, component := range components {
		if component.Name == "" || !expected[component.Name] || seen[component.Name] {
			return false
		}
		seen[component.Name] = true
	}
	return true
}

func lanTwoAppBuildFailureObservationLine(observation lanTwoAppBuildFailureObservation) string {
	return fmt.Sprintf("LAN build failure diagnostic: commands_available=%t worker_available=%t job_read=%s job_status=%s job_code=%s runtime_diagnostic=%s deployment_read=%s deployment_status=%s runtime_read=%s runtime_phase=%s migration_state=%s components=pending:%d,image_ready:%d,starting:%d,running:%d,healthy:%d,active:%d,draining:%d,stopped:%d,failed:%d,unknown:%d artifacts_read=%s artifacts=building:%d,ready:%d,failed:%d,cancelled:%d,unavailable:%d,unknown:%d,attempt_max:%d worker_returned=%t worker_error_present=%t worker_error_canceled=%t worker_error_deadline=%t worker_return_canceled=%t worker_return_deadline=%t commands=%s",
		observation.CommandsAvailable, observation.WorkerAvailable,
		observation.JobRead, observation.JobStatus, observation.JobCode, observation.RuntimeDiagnostic,
		observation.DeploymentRead, observation.DeploymentStatus, observation.RuntimeRead, observation.RuntimePhase, observation.MigrationState,
		observation.Components.Pending, observation.Components.ImageReady, observation.Components.Starting, observation.Components.Running,
		observation.Components.Healthy, observation.Components.Active, observation.Components.Draining, observation.Components.Stopped,
		observation.Components.Failed, observation.Components.Unknown, observation.Artifacts.Read,
		observation.Artifacts.Counts.Building, observation.Artifacts.Counts.Ready, observation.Artifacts.Counts.Failed,
		observation.Artifacts.Counts.Cancelled, observation.Artifacts.Counts.Unavailable, observation.Artifacts.Counts.Unknown,
		observation.Artifacts.Counts.AttemptMax, observation.Worker.Returned, observation.Worker.ErrorPresent,
		observation.Worker.ErrorCanceled, observation.Worker.ErrorDeadline, observation.Worker.ReturnWasCanceled,
		observation.Worker.ReturnWasDeadline, lanTwoAppBuildCommandSnapshotLine(observation.Commands))
}

func lanTwoAppBuildCommandSnapshotLine(snapshot lanTwoAppBuildCommandDiagnosticSnapshot) string {
	parts := make([]string, 0, lanTwoAppBuildOperationCount)
	for operation := lanTwoAppBuildOperation(0); operation < lanTwoAppBuildOperationCount; operation++ {
		entry := snapshot.Operations[operation]
		status := entry.ReturnStatus
		if status == "" {
			status = "unavailable"
		}
		parts = append(parts, fmt.Sprintf("%s{entered:%d,returned:%d,in_flight:%t,returned_elapsed_ms:%d,in_flight_elapsed_ms:%d,in_flight_elapsed_known:%t,status:%s,stdout_truncated:%t,stderr_truncated:%t}",
			operation.label(), entry.Entered, entry.Returned, entry.InFlight, entry.ReturnedElapsedMillis, entry.InFlightElapsedMillis, entry.InFlightElapsedKnown, status, entry.StdoutTruncated, entry.StderrTruncated))
	}
	return strings.Join(parts, ";")
}

func TestLanTwoAppBuildDiagnosticCommandRunnerIsBoundedAndRedacted(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := runtimeprocess.CommandRequest{
		Executable: "executable-secret", Directory: "directory-secret",
		Args: []string{"buildx", "build", "--load", "argument-secret"}, Env: []string{"TOKEN=environment-secret"},
		Timeout: time.Minute, OutputLimit: 1024,
	}
	wantResult := runtimeprocess.CommandResult{Stdout: []byte("stdout-secret"), Stderr: []byte("stderr-secret"), StdoutTruncated: true}
	wantError := errors.New("command-secret")
	delegate := lanTwoAppBuildDiagnosticRunnerFunc(func(gotContext context.Context, gotRequest runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if gotContext != ctx || !reflect.DeepEqual(gotRequest, request) {
			t.Error("diagnostic wrapper changed the delegated context or request")
		}
		entered <- struct{}{}
		<-release
		return wantResult, wantError
	})
	runner := newLanTwoAppBuildCommandDiagnosticRunner(delegate)
	var clockMu sync.Mutex
	now := time.Unix(0, 0)
	runner.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(time.Millisecond)
		return now
	}
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			delegated := request
			delegated.Args = append([]string(nil), request.Args...)
			delegated.Env = append([]string(nil), request.Env...)
			gotResult, gotError := runner.Run(ctx, delegated)
			if gotError != wantError || !reflect.DeepEqual(gotResult, wantResult) ||
				string(gotResult.Stdout) != "stdout-secret" || string(gotResult.Stderr) != "stderr-secret" ||
				&gotResult.Stdout[0] != &wantResult.Stdout[0] || &gotResult.Stderr[0] != &wantResult.Stderr[0] {
				t.Error("diagnostic wrapper changed the returned result or error identity")
			}
		}()
	}
	for range 2 {
		<-entered
	}
	inFlight := runner.Snapshot().Operations[lanTwoAppBuildOperationLoad]
	if inFlight.Entered != 2 || inFlight.Returned != 0 || !inFlight.InFlight || !inFlight.InFlightElapsedKnown || inFlight.InFlightElapsedMillis <= 0 {
		t.Fatalf("in-flight snapshot=%#v", inFlight)
	}
	close(release)
	wait.Wait()
	snapshot := runner.Snapshot()
	entry := snapshot.Operations[lanTwoAppBuildOperationLoad]
	if entry.Entered != 2 || entry.Returned != 2 || entry.InFlight || entry.ReturnedElapsedMillis <= 0 || entry.InFlightElapsedKnown || entry.ReturnStatus != "failed" || !entry.StdoutTruncated || entry.StderrTruncated {
		t.Fatalf("completed snapshot=%#v", entry)
	}
	line := lanTwoAppBuildCommandSnapshotLine(snapshot)
	for _, forbidden := range []string{"stdout-secret", "stderr-secret", "command-secret", "executable-secret", "directory-secret", "argument-secret", "environment-secret", "--load"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("command diagnostic leaked %q: %s", forbidden, line)
		}
	}
}

func TestLanTwoAppBuildDiagnosticPreservesJoinedTerminationFailure(t *testing.T) {
	for _, canceled := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(canceled.Error(), func(t *testing.T) {
			joined := errors.Join(canceled, &runtimeprocess.TerminationError{VerifyErr: errors.New("termination-detail-secret")})
			if got := lanTwoAppBuildCommandResultStatus(joined); got != "termination_failed" {
				t.Fatalf("joined termination classification = %q", got)
			}
		})
	}
	for _, pair := range [][2]string{{"ok", "termination_failed"}, {"termination_failed", "ok"}, {"mixed", "termination_failed"}, {"termination_failed", "mixed"}} {
		t.Run(pair[0]+"_then_"+pair[1], func(t *testing.T) {
			if got := lanTwoAppBuildDiagnosticMergeStatus(pair[0], pair[1]); got != "termination_failed" {
				t.Fatalf("aggregate lost termination failure: %q", got)
			}
		})
	}
}

func TestLanTwoAppBuildDiagnosticReportsObserverAvailability(t *testing.T) {
	for _, test := range []struct {
		name             string
		commands, worker bool
	}{{"unconfigured", false, false}, {"commands_only", true, false}, {"worker_only", false, true}, {"both", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			input := lanTwoAppBuildFailureDiagnosticInput{}
			if test.commands {
				input.Commands = func() lanTwoAppBuildCommandDiagnosticSnapshot { return lanTwoAppBuildCommandDiagnosticSnapshot{} }
			}
			if test.worker {
				input.Worker = func() lanTwoAppWorkerReturnSnapshot { return lanTwoAppWorkerReturnSnapshot{} }
			}
			observation := lanTwoAppBuildFailureObservationFor(input)
			if observation.CommandsAvailable != test.commands || observation.WorkerAvailable != test.worker {
				t.Fatal("unconfigured observer was reported as an observed zero")
			}
			want := fmt.Sprintf("commands_available=%t worker_available=%t", test.commands, test.worker)
			if !strings.Contains(lanTwoAppBuildFailureObservationLine(observation), want) {
				t.Fatal("diagnostic line omitted observer availability")
			}
		})
	}
}

func TestLanTwoAppBuildDiagnosticOperationClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want lanTwoAppBuildOperation
	}{
		{name: "loaded build", args: []string{"buildx", "build", "--load"}, want: lanTwoAppBuildOperationLoad},
		{name: "builder create", args: []string{"buildx", "create"}, want: lanTwoAppBuildOperationSetup},
		{name: "builder list", args: []string{"buildx", "ls"}, want: lanTwoAppBuildOperationSetup},
		{name: "builder inspect", args: []string{"buildx", "inspect"}, want: lanTwoAppBuildOperationSetup},
		{name: "unloaded build", args: []string{"buildx", "build"}, want: lanTwoAppBuildOperationOther},
		{name: "container start", args: []string{"container", "start"}, want: lanTwoAppBuildOperationOther},
		{name: "container exec", args: []string{"container", "exec"}, want: lanTwoAppBuildOperationOther},
		{name: "network inspect", args: []string{"network", "inspect"}, want: lanTwoAppBuildOperationOther},
		{name: "image inspect", args: []string{"image", "inspect"}, want: lanTwoAppBuildOperationInspect},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := lanTwoAppBuildOperationFor(runtimeprocess.CommandRequest{Args: test.args}); got != test.want {
				t.Fatalf("operation=%s want=%s", got.label(), test.want.label())
			}
		})
	}
}

func TestLanTwoAppBuildArtifactReaderUsesMigratedScopeAndBound(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal("open migrated diagnostic database")
	}
	t.Cleanup(func() { _ = db.Close() })
	const targetAppID = "10000000-0000-4000-8000-000000000001"
	const targetPlanID = "20000000-0000-4000-8000-000000000002"
	const targetReleaseID = "30000000-0000-4000-8000-000000000003"
	const foreignAppID = "40000000-0000-4000-8000-000000000004"
	const foreignPlanID = "50000000-0000-4000-8000-000000000005"
	const foreignReleaseID = "60000000-0000-4000-8000-000000000006"
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES('lan-build-diagnostic-owner','lan-build-diagnostic-owner','hash','administrator',datetime('now'),datetime('now'))`); err != nil {
		t.Fatal("seed diagnostic owner")
	}
	lanTwoAppBuildDiagnosticSeedArtifactRelease(t, db, targetAppID, targetPlanID, targetReleaseID, "target", "1", "2", "3", "4")
	lanTwoAppBuildDiagnosticSeedArtifactRelease(t, db, foreignAppID, foreignPlanID, foreignReleaseID, "foreign", "5", "6", "7", "8")
	for index := range lanTwoAppBuildArtifactLimit + 1 {
		if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
			id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,
			build_definition_digest,attempt_number,state,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,1,'building',datetime('now'),datetime('now'))`,
			fmt.Sprintf("lan-build-target-artifact-%03d", index), targetReleaseID, targetPlanID, 1,
			fmt.Sprintf("target-component-%03d", index), "generated-node/v1", fmt.Sprintf("%064x", index+1)); err != nil {
			t.Fatal("seed bounded target artifact history")
		}
	}
	if _, err := db.Exec(`INSERT INTO generated_image_artifacts(
		id,release_id,deployment_plan_revision_id,deployment_plan_revision_number,component_id,compiler_version,
		build_definition_digest,attempt_number,state,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,1,'building',datetime('now'),datetime('now'))`,
		"lan-build-foreign-artifact", foreignReleaseID, foreignPlanID, 1, "foreign-component", "generated-node/v1", strings.Repeat("f", 64)); err != nil {
		t.Fatal("seed unrelated artifact history")
	}

	reader := lanTwoAppBuildArtifactReaderFor(db)
	rows, err := reader(context.Background(), targetReleaseID, targetPlanID, 1)
	if err != nil || !rows.Truncated || len(rows.Rows) != lanTwoAppBuildArtifactLimit {
		t.Fatalf("bounded scoped rows=%d truncated=%t err=%t", len(rows.Rows), rows.Truncated, err != nil)
	}
	for _, row := range rows.Rows {
		if !strings.HasPrefix(row.ComponentName, "target-component-") || row.State != "building" || row.AttemptNumber != 1 {
			t.Fatal("scoped reader returned an unexpected artifact shape")
		}
	}
	wrongPlan, err := reader(context.Background(), targetReleaseID, foreignPlanID, 1)
	if err != nil || wrongPlan.Truncated || len(wrongPlan.Rows) != 0 {
		t.Fatalf("mixed release/plan rows=%d truncated=%t err=%t", len(wrongPlan.Rows), wrongPlan.Truncated, err != nil)
	}
	foreignRows, err := reader(context.Background(), foreignReleaseID, foreignPlanID, 1)
	if err != nil || foreignRows.Truncated || len(foreignRows.Rows) != 1 || foreignRows.Rows[0].ComponentName != "foreign-component" {
		t.Fatalf("unrelated scoped rows=%d truncated=%t err=%t", len(foreignRows.Rows), foreignRows.Truncated, err != nil)
	}
}

func lanTwoAppBuildDiagnosticSeedArtifactRelease(t *testing.T, db *sql.DB, appID, planID, releaseID, tag, fingerprint, resolved, canonical, tree string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'draft',datetime('now'),datetime('now'))`, appID, "lan-build-diagnostic-"+tag, "LAN build diagnostic "+tag); err != nil {
		t.Fatal("seed diagnostic application")
	}
	if _, err := db.Exec(`INSERT INTO deployment_plan_revisions(
		id,app_id,revision_number,bundle_ref,strategy,detector,detector_version,source_structural_fingerprint,
		analyzed_source_provider,analyzed_repository_id,analyzed_resolved_digest,canonical_digest,component_count,
		field_provenance_count,migration_evidence_digest,revised_by,revised_at,acceptance_status,accepted_by,accepted_at
	) VALUES(?,?,1,?,'generated_node','js-ts','1',?,'local',0,?,?,1,8,'','lan-build-diagnostic-owner',datetime('now'),'accepted','lan-build-diagnostic-owner',datetime('now'))`,
		planID, appID, "apps/"+appID+"/deployment-plans/"+planID+".json", strings.Repeat(fingerprint, 64), strings.Repeat(resolved, 64), strings.Repeat(canonical, 64)); err != nil {
		t.Fatal("seed diagnostic plan")
	}
	if _, err := db.Exec(`UPDATE deployment_plan_heads SET revision_id=?,revision_number=1,updated_at=datetime('now') WHERE app_id=?`, planID, appID); err != nil {
		t.Fatal("seed diagnostic plan head")
	}
	if _, err := db.Exec(`INSERT INTO releases(
		id,app_id,status,metadata_json,created_at,source_provider,repository_id,resolved_sha,compose_path,
		archive_sha256,workspace_state,workspace_tree_sha256,deployment_plan_revision_id,deployment_plan_revision_number
	) VALUES(?,?,'ready','{}',datetime('now'),'local',0,?,'',?,'ready',?,?,1)`,
		releaseID, appID, strings.Repeat(resolved, 64), strings.Repeat(tree, 64), strings.Repeat(tree, 64), planID); err != nil {
		t.Fatal("seed diagnostic release")
	}
}

type lanTwoAppBuildDiagnosticRunnerFunc func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)

func (runner lanTwoAppBuildDiagnosticRunnerFunc) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return runner(ctx, request)
}

func TestLanTwoAppBuildDiagnosticInFlightOverflowStaysConservative(t *testing.T) {
	const calls = lanTwoAppBuildActiveStartLimit + 1
	entered := make(chan struct{}, calls)
	release := make(chan struct{})
	runner := newLanTwoAppBuildCommandDiagnosticRunner(lanTwoAppBuildDiagnosticRunnerFunc(func(context.Context, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		entered <- struct{}{}
		<-release
		return runtimeprocess.CommandResult{}, nil
	}))
	var clockMu sync.Mutex
	now := time.Unix(0, 0)
	runner.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(time.Millisecond)
		return now
	}
	var wait sync.WaitGroup
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"buildx", "build", "--load"}})
		}()
	}
	for range calls {
		<-entered
	}
	inFlight := runner.Snapshot().Operations[lanTwoAppBuildOperationLoad]
	if inFlight.Entered != calls || inFlight.Returned != 0 || !inFlight.InFlight || inFlight.InFlightElapsedKnown {
		t.Fatalf("overflow in-flight snapshot=%#v", inFlight)
	}
	close(release)
	wait.Wait()
	afterDrain := runner.Snapshot().Operations[lanTwoAppBuildOperationLoad]
	if afterDrain.Entered != calls || afterDrain.Returned != calls || afterDrain.InFlight || afterDrain.InFlightElapsedKnown {
		t.Fatalf("overflow drain snapshot=%#v", afterDrain)
	}
}

func TestLanTwoAppBuildFailureDiagnosticValidatesArtifactProvenanceAndStaysQuietAfterSuccess(t *testing.T) {
	const applicationID = "application-secret"
	const jobID = "job-secret"
	const deploymentID = "deployment-secret"
	observation := lanTwoAppBuildFailureObservationFor(lanTwoAppBuildFailureDiagnosticInput{
		Parent: context.Background(), Application: applicationID,
		Job:            jobs.Job{ID: jobID, Type: "deploy", ResourceType: "application", ResourceID: applicationID, Status: string(jobs.Running)},
		ExpectedPlanID: "plan-secret", ExpectedPlanNumber: 3,
		Deployments: func(context.Context, string, int) ([]deployments.Deployment, error) {
			return []deployments.Deployment{{ID: deploymentID, AppID: applicationID, JobID: jobID, ReleaseID: "release-secret", DeploymentPlanRevisionID: "plan-secret", DeploymentPlanRevisionNumber: 3, Status: deployments.Applying}}, nil
		},
		Runtime: func(context.Context, string, string) (generatedruntimestate.Deployment, error) {
			return generatedruntimestate.Deployment{AppID: applicationID, DeploymentID: deploymentID, ReleaseID: "release-secret", DeploymentPlanRevisionID: "plan-secret", DeploymentPlanRevisionNumber: 3, Components: []generatedruntimestate.Component{{Name: "api", State: generatedruntimestate.ComponentPending}, {Name: "frontend", State: generatedruntimestate.ComponentPending}}}, nil
		},
		Release: func(context.Context, string, string) (lanTwoAppBuildReleaseSnapshot, error) {
			return lanTwoAppBuildReleaseSnapshot{ID: "release-secret", AppID: applicationID, PlanID: "plan-secret", PlanNumber: 3, Ready: true}, nil
		},
		Plan: func(context.Context, string, string, int64) (lanTwoAppBuildPlanSnapshot, error) {
			return lanTwoAppBuildPlanSnapshot{ID: "plan-secret", AppID: applicationID, RevisionNumber: 3, ComponentNames: []string{"api", "frontend"}, Accepted: true}, nil
		},
		Artifacts: func(context.Context, string, string, int64) (lanTwoAppBuildArtifactRows, error) {
			return lanTwoAppBuildArtifactRows{Rows: []lanTwoAppBuildArtifactSnapshot{{ComponentName: "forged-component-secret", State: "building", AttemptNumber: 1}}}, nil
		},
	})
	if observation.Artifacts.Read != "identity_mismatch" || observation.Artifacts.Counts != (lanTwoAppBuildArtifactStateCounts{}) {
		t.Fatalf("artifact provenance mismatch was aggregated: %#v", observation.Artifacts)
	}
	line := lanTwoAppBuildFailureObservationLine(observation)
	for _, forbidden := range []string{applicationID, jobID, deploymentID, "release-secret", "plan-secret", "forged-component-secret"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("build diagnostic leaked %q: %s", forbidden, line)
		}
	}
	var order []string
	failure, complete := lanTwoAppBuildFailureOnlyDiagnostic(func() { order = append(order, "diagnostic") })
	func() {
		defer func() { order = append(order, "teardown") }()
		defer failure()
	}()
	if strings.Join(order, ",") != "diagnostic,teardown" {
		t.Fatalf("failure diagnostic order=%q", strings.Join(order, ","))
	}
	complete()
	failure()
	if strings.Join(order, ",") != "diagnostic,teardown" {
		t.Fatal("completed diagnostic emitted a success-path log")
	}
}
