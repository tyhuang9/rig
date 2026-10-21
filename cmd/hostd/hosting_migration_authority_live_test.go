//go:build live_docker

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedmigration"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

const migrationAuthorityInspectFormat = `{"id":{{json .Id}},"name":{{json .Name}},"labels":{{json .Config.Labels}},"status":{{json .State.Status}},"running":{{json .State.Running}},"startedAt":{{json .State.StartedAt}}}`

type migrationAuthorityRevocationInput struct {
	ctx                context.Context
	db                 *sql.DB
	dataRoot           string
	docker             string
	fixtureEnvironment []string
	fixtureRoot        string
	application        apicontract.Application
	v1Deployment       apicontract.Deployment
	v2Mutation         apicontract.JobMutationResponse
	v2Plan             apicontract.DeploymentPlanRevision
	v2Configuration    apicontract.ApplicationConfiguration
	request            func(string, string, any, int, any, ...map[string]string) []byte
	jobs               *jobs.Service
	composition        runtimeComposition
	runner             *migrationJourneyRunner
	process            *migrationAuthorityProcessObserver
	execution          *migrationAuthorityExecutionObserver
}

type migrationAuthorityProcessExpected struct {
	jobID               string
	attempt             int
	applicationID       string
	deploymentID        string
	releaseID           string
	configurationID     string
	configurationNumber int64
	planID              string
	planNumber          int64
	componentName       string
	environmentPath     string
	containerName       string
}

type migrationAuthorityContainerInspection struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Status    string            `json:"status"`
	Running   bool              `json:"running"`
	StartedAt string            `json:"startedAt"`
}

type migrationAuthorityCapturedContainer struct {
	expected   migrationAuthorityProcessExpected
	request    runtimeprocess.CommandRequest
	id         string
	nonce      string
	labels     map[string]string
	deployment deployments.Deployment
	runtime    generatedruntimestate.Deployment
}

type migrationAuthorityProcessSnapshot struct {
	armed            bool
	createCalls      int
	startCalls       int
	waitCalls        int
	removeCalls      int
	attemptMutations int
	captured         bool
}

type migrationAuthorityProcessObserver struct {
	mu               sync.Mutex
	db               *sql.DB
	delegate         runtimeprocess.CommandRunner
	expected         *migrationAuthorityProcessExpected
	created          *migrationAuthorityCapturedContainer
	captured         bool
	createCalls      int
	startCalls       int
	waitCalls        int
	removeCalls      int
	attemptMutations int
}

func newMigrationAuthorityProcessObserver(db *sql.DB) *migrationAuthorityProcessObserver {
	return &migrationAuthorityProcessObserver{db: db, delegate: runtimeprocess.ExecRunner{}}
}

func (observer *migrationAuthorityProcessObserver) Arm(expected migrationAuthorityProcessExpected) error {
	if observer == nil || observer.db == nil || observer.delegate == nil || expected.attempt != 2 ||
		uuid.Validate(expected.jobID) != nil || uuid.Validate(expected.applicationID) != nil ||
		uuid.Validate(expected.deploymentID) != nil || !migrationAuthorityReleaseID(expected.releaseID) ||
		uuid.Validate(expected.configurationID) != nil || expected.configurationNumber < 1 ||
		uuid.Validate(expected.planID) != nil || expected.planNumber < 1 ||
		expected.componentName == "" || !filepath.IsAbs(expected.environmentPath) ||
		filepath.Clean(expected.environmentPath) != expected.environmentPath || expected.containerName == "" {
		return errors.New("invalid migration authority observation")
	}
	copy := expected
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.expected != nil || observer.created != nil {
		return errors.New("migration authority observation already armed")
	}
	observer.expected = &copy
	return nil
}

func (observer *migrationAuthorityProcessObserver) Run(ctx context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	if observer == nil || observer.delegate == nil {
		return runtimeprocess.CommandResult{}, errors.New("migration authority process observer is unavailable")
	}
	targetCreate := observer.trackTargetCommand(request.Args)
	result, runErr := observer.delegate.Run(ctx, request)
	if !targetCreate || runErr != nil {
		return result, runErr
	}
	if err := observer.captureCreated(ctx, request, result); err != nil {
		return result, err
	}
	return result, nil
}

func (observer *migrationAuthorityProcessObserver) trackTargetCommand(args []string) bool {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.expected == nil || len(args) < 2 || args[0] != "container" {
		return false
	}
	if args[1] == "create" && migrationAuthorityCreateMatches(args, *observer.expected) {
		observer.createCalls++
		return true
	}
	if observer.created == nil {
		return false
	}
	id := observer.created.id
	switch {
	case args[1] == "start" && len(args) == 3 && args[2] == id:
		observer.startCalls++
	case args[1] == "wait" && len(args) == 3 && args[2] == id:
		observer.waitCalls++
	case args[1] == "rm" && len(args) == 4 && args[2] == "--force" && args[3] == id:
		observer.removeCalls++
	}
	return false
}

func (observer *migrationAuthorityProcessObserver) captureCreated(ctx context.Context, request runtimeprocess.CommandRequest, result runtimeprocess.CommandResult) error {
	containerID := strings.TrimSpace(string(result.Stdout))
	if !migrationAuthorityLowerHex(containerID, 64) || ctx == nil || ctx.Err() != nil {
		return errors.New("migration authority create observation is invalid")
	}
	observer.mu.Lock()
	if observer.expected == nil || observer.created != nil {
		observer.mu.Unlock()
		return errors.New("migration authority create observation is out of order")
	}
	expected := *observer.expected
	labels := migrationAuthorityLabels(request.Args)
	nonce := labels["io.rig.invocation"]
	artifactID := labels["io.rig.artifact"]
	wantLabels := map[string]string{
		"io.rig.managed": "generated-migration", "io.rig.application": expected.applicationID,
		"io.rig.release": expected.releaseID, "io.rig.deployment": expected.deploymentID,
		"io.rig.artifact": artifactID, "io.rig.plan": expected.planID,
		"io.rig.component": expected.componentName, "io.rig.invocation": nonce,
	}
	created := &migrationAuthorityCapturedContainer{
		expected: expected, request: migrationAuthorityInspectionRequest(request), id: containerID, nonce: nonce,
		labels: migrationAuthorityCloneLabels(wantLabels),
	}
	observer.created = created
	observer.mu.Unlock()

	inspection, found, err := observer.inspect(ctx, request, containerID)
	if err != nil || !found {
		return errors.New("migration authority created container inspection failed")
	}
	environmentPath, exactEnvironmentPath := migrationAuthorityArgument(request.Args, "--env-file")
	if uuid.Validate(artifactID) != nil || uuid.Validate(nonce) != nil || !exactEnvironmentPath || environmentPath != expected.environmentPath ||
		inspection.ID != containerID || strings.TrimPrefix(inspection.Name, "/") != expected.containerName ||
		inspection.Status != "created" || inspection.Running || inspection.StartedAt != "0001-01-01T00:00:00Z" ||
		!reflect.DeepEqual(labels, wantLabels) || !migrationAuthorityContainsLabels(inspection.Labels, wantLabels) {
		return errors.New("migration authority created container ownership is invalid")
	}
	deployment, err := deployments.New(observer.db).Get(ctx, expected.applicationID, expected.deploymentID)
	if err != nil {
		return errors.New("migration authority deployment snapshot failed")
	}
	runtimeState, err := generatedruntimestate.New(observer.db).Get(ctx, expected.applicationID, expected.deploymentID)
	if err != nil || !migrationAuthorityStateMatches(deployment, runtimeState, expected, artifactID) {
		return errors.New("migration authority immutable state is invalid")
	}
	if ctx.Err() != nil {
		return errors.New("migration authority context ended before revocation")
	}
	mutation, err := observer.db.ExecContext(ctx, `UPDATE jobs SET attempt=attempt+1 WHERE id=? AND attempt=2 AND status='running'`, expected.jobID)
	if err != nil {
		return errors.New("migration authority revocation failed")
	}
	rows, err := mutation.RowsAffected()
	if err != nil || rows != 1 || ctx.Err() != nil {
		return errors.New("migration authority revocation was not exact")
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.created != created || observer.captured {
		return errors.New("migration authority create observation changed")
	}
	observer.attemptMutations++
	created.deployment = deployment
	created.runtime = runtimeState
	observer.captured = true
	return nil
}

func (observer *migrationAuthorityProcessObserver) Snapshot() migrationAuthorityProcessSnapshot {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return migrationAuthorityProcessSnapshot{
		armed:       observer.expected != nil,
		createCalls: observer.createCalls, startCalls: observer.startCalls, waitCalls: observer.waitCalls,
		removeCalls: observer.removeCalls, attemptMutations: observer.attemptMutations, captured: observer.captured,
	}
}

func (observer *migrationAuthorityProcessObserver) DiagnosticSnapshot() migrationJourneyProcessDiagnosticSnapshot {
	snapshot := observer.Snapshot()
	return migrationJourneyProcessDiagnosticSnapshot{
		Armed:                snapshot.armed,
		ObservationAvailable: snapshot.armed,
		CreateCalls:          snapshot.createCalls,
		StartCalls:           snapshot.startCalls,
		WaitCalls:            snapshot.waitCalls,
		RemoveCalls:          snapshot.removeCalls,
		AttemptMutations:     snapshot.attemptMutations,
		Captured:             snapshot.captured,
	}
}

func (observer *migrationAuthorityProcessObserver) CapturedState() (deployments.Deployment, generatedruntimestate.Deployment, bool) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.captured || observer.created == nil {
		return deployments.Deployment{}, generatedruntimestate.Deployment{}, false
	}
	deployment := observer.created.deployment
	deployment.Findings = slices.Clone(deployment.Findings)
	runtimeState := observer.created.runtime
	runtimeState.Components = slices.Clone(runtimeState.Components)
	return deployment, runtimeState, true
}

func (observer *migrationAuthorityProcessObserver) AssertRetained(t *testing.T, ctx context.Context) {
	t.Helper()
	observer.mu.Lock()
	if !observer.captured || observer.created == nil {
		observer.mu.Unlock()
		t.Fatal("migration authority observer captured no retained container")
	}
	captured := *observer.created
	observer.mu.Unlock()
	inspection, found, err := observer.inspect(ctx, captured.request, captured.id)
	if err != nil || !found || !migrationAuthorityInspectionMatches(inspection, captured) {
		t.Fatal("retained migration container lost exact created ownership")
	}
}

// CleanupRetained removes only the exact captured migration container after a
// fresh ownership check. Its returned ID must be excluded from broad cleanup
// whenever exact ownership cannot be established or exact removal fails.
func (observer *migrationAuthorityProcessObserver) CleanupRetained(t *testing.T, ctx context.Context) string {
	t.Helper()
	observer.mu.Lock()
	if observer.created == nil {
		observer.mu.Unlock()
		return ""
	}
	captured := *observer.created
	validated := observer.captured
	observer.mu.Unlock()
	if !validated {
		t.Error("retained migration container observation did not complete")
	}
	inspection, found, err := observer.inspect(ctx, captured.request, captured.id)
	if err != nil || !found || !migrationAuthorityInspectionMatches(inspection, captured) {
		t.Error("retained migration container ownership uncertain; retaining")
		return captured.id
	}
	remove := captured.request
	remove.Args = []string{"container", "rm", "--force", captured.id}
	remove.Timeout = 30 * time.Second
	result, removeErr := observer.delegate.Run(ctx, remove)
	migrationAuthorityClearResult(&result)
	if removeErr != nil {
		t.Error("remove exact retained migration container")
		return captured.id
	}
	_, found, err = observer.inspect(ctx, captured.request, captured.id)
	if err != nil || found {
		t.Error("confirm retained migration container absence")
		return captured.id
	}
	return ""
}

func (observer *migrationAuthorityProcessObserver) inspect(ctx context.Context, base runtimeprocess.CommandRequest, id string) (migrationAuthorityContainerInspection, bool, error) {
	inspectContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request := base
	request.Args = []string{"container", "inspect", "--format", migrationAuthorityInspectFormat, id}
	request.Timeout = 10 * time.Second
	request.OutputLimit = 8 << 10
	result, runErr := observer.delegate.Run(inspectContext, request)
	if runErr != nil {
		notFound := migrationAuthorityDefiniteAbsence(inspectContext, result, runErr, id)
		migrationAuthorityClearResult(&result)
		if notFound {
			return migrationAuthorityContainerInspection{}, false, nil
		}
		return migrationAuthorityContainerInspection{}, false, errors.New("migration authority readonly inspection failed")
	}
	if inspectContext.Err() != nil || result.StdoutTruncated || result.StderrTruncated {
		migrationAuthorityClearResult(&result)
		return migrationAuthorityContainerInspection{}, false, errors.New("migration authority readonly inspection was truncated")
	}
	var inspection migrationAuthorityContainerInspection
	err := json.Unmarshal(bytes.TrimSpace(result.Stdout), &inspection)
	migrationAuthorityClearResult(&result)
	if err != nil {
		return migrationAuthorityContainerInspection{}, false, errors.New("migration authority readonly inspection was invalid")
	}
	return inspection, true, nil
}

type migrationAuthorityExecutionObservation struct {
	job        jobs.Job
	err        error
	contextErr error
}

type migrationAuthorityExecutionObserver struct {
	mu          sync.Mutex
	delegate    jobs.Executor
	jobID       string
	returned    chan migrationAuthorityExecutionObservation
	observation *migrationAuthorityExecutionObservation
}

func newMigrationAuthorityExecutionObserver(delegate jobs.Executor) *migrationAuthorityExecutionObserver {
	return &migrationAuthorityExecutionObserver{delegate: delegate}
}

func (observer *migrationAuthorityExecutionObserver) Arm(jobID string) error {
	if observer == nil || observer.delegate == nil || uuid.Validate(jobID) != nil {
		return errors.New("invalid migration execution observation")
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.jobID != "" || observer.returned != nil {
		return errors.New("migration execution observation already armed")
	}
	observer.jobID = jobID
	observer.returned = make(chan migrationAuthorityExecutionObservation, 1)
	return nil
}

func (observer *migrationAuthorityExecutionObserver) Execute(ctx context.Context, job jobs.Job, reporter jobs.ProgressReporter) (jobs.ExecutionResult, error) {
	if observer == nil || observer.delegate == nil {
		return jobs.ExecutionResult{}, errors.New("migration execution observer is unavailable")
	}
	result, err := observer.delegate.Execute(ctx, job, reporter)
	executionContextErr := ctx.Err()
	observer.mu.Lock()
	jobID, returned := observer.jobID, observer.returned
	if job.ID == jobID && returned != nil {
		captured := migrationAuthorityExecutionObservation{job: job, err: err, contextErr: executionContextErr}
		if observer.observation == nil {
			observer.observation = &captured
		}
		observer.mu.Unlock()
		select {
		case returned <- captured:
		default:
		}
	} else {
		observer.mu.Unlock()
	}
	return result, err
}

func (observer *migrationAuthorityExecutionObserver) Wait(ctx context.Context, maximum time.Duration) (migrationAuthorityExecutionObservation, error) {
	observer.mu.Lock()
	returned := observer.returned
	observer.mu.Unlock()
	if returned == nil || maximum <= 0 {
		return migrationAuthorityExecutionObservation{}, errors.New("migration execution observation is not armed")
	}
	waitContext, cancel := context.WithTimeout(ctx, maximum)
	defer cancel()
	select {
	case observation := <-returned:
		return observation, nil
	case <-waitContext.Done():
		return migrationAuthorityExecutionObservation{}, errors.New("migration execution return was not observed")
	}
}

func (observer *migrationAuthorityExecutionObserver) DiagnosticSnapshot() migrationJourneyExecutionDiagnosticSnapshot {
	if observer == nil {
		return migrationJourneyExecutionDiagnosticSnapshot{}
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	snapshot := migrationJourneyExecutionDiagnosticSnapshot{Armed: observer.returned != nil}
	if observer.observation == nil {
		return snapshot
	}
	snapshot.ObservationAvailable = true
	snapshot.Err = observer.observation.err
	snapshot.ReturnContextCanceled = errors.Is(observer.observation.contextErr, context.Canceled)
	snapshot.ReturnContextDeadline = errors.Is(observer.observation.contextErr, context.DeadlineExceeded)
	return snapshot
}

func runMigrationAuthorityRevokedBeforeStart(t *testing.T, input migrationAuthorityRevocationInput) {
	t.Helper()
	if input.ctx == nil || input.db == nil || input.request == nil || input.jobs == nil || input.runner == nil || input.process == nil || input.execution == nil {
		t.Fatal("migration authority scenario dependencies are incomplete")
	}
	var beforeHistory apicontract.DeploymentList
	input.request(http.MethodGet, "/api/v1/apps/"+input.application.ID+"/deployments", nil, http.StatusOK, &beforeHistory)
	beforeDeployment := migrationJourneyDeploymentForJob(t, beforeHistory, input.v2Mutation.Job.ID)
	if beforeDeployment.Status != string(deployments.Preparing) || beforeDeployment.AppID != input.application.ID ||
		beforeDeployment.JobID != input.v2Mutation.Job.ID || beforeDeployment.ReleaseID == "" ||
		beforeDeployment.ActualConfigurationRevisionID != input.v2Configuration.RevisionID ||
		beforeDeployment.ActualConfigurationRevisionNumber != input.v2Configuration.RevisionNumber ||
		beforeDeployment.DeploymentPlanRevisionID != input.v2Plan.RevisionID ||
		beforeDeployment.DeploymentPlanRevisionNumber != input.v2Plan.RevisionNumber {
		t.Fatal("approved v2 migration did not retain its immutable pre-resume deployment")
	}
	beforeActive, err := input.composition.state.Active(input.ctx, input.application.ID)
	if err != nil || beforeActive.DeploymentID != input.v1Deployment.ID || beforeActive.ReleaseID != input.v1Deployment.ReleaseID {
		t.Fatal("v1 route was not active before migration authority revocation")
	}
	migrationAuthorityAssertRelationsAbsent(t, input.ctx, input.docker, input.fixtureEnvironment, input.fixtureRoot)
	const resumedAttempt = 2
	expected := migrationAuthorityProcessExpected{
		jobID: input.v2Mutation.Job.ID, attempt: resumedAttempt, applicationID: input.application.ID,
		deploymentID: beforeDeployment.ID, releaseID: beforeDeployment.ReleaseID,
		configurationID: input.v2Configuration.RevisionID, configurationNumber: input.v2Configuration.RevisionNumber,
		planID: input.v2Plan.RevisionID, planNumber: input.v2Plan.RevisionNumber, componentName: input.v2Plan.Migration.ComponentName,
		environmentPath: filepath.Join(input.dataRoot, "runtime", "generated-runtime", beforeDeployment.ID+"-"+strconv.Itoa(resumedAttempt), "runtime.env"),
		containerName:   "rig-migration-" + strings.ReplaceAll(beforeDeployment.ID, "-", ""),
	}
	resumeFailureDiagnostic, resumeDiagnosticComplete := migrationJourneyResumeFailureDiagnostic(t, migrationJourneyResumeFailureDiagnosticInput{
		Parent:      input.ctx,
		Application: input.application.ID,
		Job:         input.v2Mutation.Job.ID,
		GetJob:      input.jobs.Get,
		Deployments: deployments.New(input.db).List,
		Runtime:     input.composition.state.Get,
		Runner:      input.runner.DiagnosticSnapshot,
		Execution:   input.execution.DiagnosticSnapshot,
		Process:     input.process.DiagnosticSnapshot,
	})
	defer resumeFailureDiagnostic()
	if err := input.process.Arm(expected); err != nil {
		t.Fatal("arm exact migration process observer")
	}
	if err := input.execution.Arm(input.v2Mutation.Job.ID); err != nil {
		t.Fatal("arm migration execution return observer")
	}
	var resumed apicontract.JobResponse
	input.request(http.MethodPost, "/api/v1/jobs/"+input.v2Mutation.Job.ID+"/resume", nil, http.StatusOK, &resumed)
	if resumed.Job.ID != input.v2Mutation.Job.ID ||
		(resumed.Job.Status != string(jobs.Queued) && resumed.Job.Status != string(jobs.Assigned) && resumed.Job.Status != string(jobs.Running)) {
		t.Fatal("v2 approval resume did not resume the authority revocation job")
	}
	returned, err := input.execution.Wait(input.ctx, 9*time.Minute)
	if err != nil || returned.job.ID != input.v2Mutation.Job.ID || returned.job.Type != "deploy" ||
		returned.job.ResourceID != input.application.ID || returned.job.Attempt != resumedAttempt || returned.contextErr != nil || returned.err == nil ||
		!errors.Is(returned.err, deploymentexecution.ErrIdentityLost) {
		t.Fatal("migration executor did not return the retained authority loss")
	}
	if input.runner.Calls() != 1 {
		t.Fatal("revoked v2 migration did not call the real migration runner exactly once")
	}
	identities := input.runner.Identities()
	results := input.runner.Results()
	if len(results) != 1 || results[0] == nil || !errors.Is(results[0], deploymentexecution.ErrIdentityLost) ||
		!generatedmigration.IsCode(results[0], "migration_create_failed") {
		t.Fatal("migration runner did not retain the post-create authority loss")
	}
	migrationJourneyAssertInvocation(t, identities, 0, input.application.ID, returned.job, beforeDeployment)

	mutatedJob, err := input.jobs.Get(input.v2Mutation.Job.ID)
	if err != nil || mutatedJob.Attempt != 3 {
		t.Fatal("migration authority revocation did not persist exact attempt two to three mutation")
	}
	capturedDeployment, beforeRuntime, ok := input.process.CapturedState()
	if !ok {
		t.Fatal("migration authority observer did not retain immutable create-boundary state")
	}
	if beforeRuntime.PreviousActiveDeploymentID != beforeActive.DeploymentID ||
		beforeRuntime.PreviousActiveSlot != beforeActive.Slot || beforeRuntime.CandidateSlot == "" ||
		beforeRuntime.CandidateSlot == beforeActive.Slot {
		t.Fatal("migration authority snapshot did not retain the inactive candidate and original active head")
	}
	afterPersisted, err := deployments.New(input.db).Get(input.ctx, input.application.ID, beforeDeployment.ID)
	if err != nil || !reflect.DeepEqual(afterPersisted, capturedDeployment) {
		t.Fatal("revoked migration changed the durable applying deployment")
	}
	var afterHistory apicontract.DeploymentList
	input.request(http.MethodGet, "/api/v1/apps/"+input.application.ID+"/deployments", nil, http.StatusOK, &afterHistory)
	afterDeployment := migrationJourneyDeploymentForJob(t, afterHistory, input.v2Mutation.Job.ID)
	if afterDeployment.Status != string(deployments.Applying) || afterDeployment.ID != beforeDeployment.ID ||
		afterDeployment.AppID != beforeDeployment.AppID || afterDeployment.JobID != beforeDeployment.JobID ||
		afterDeployment.ReleaseID != beforeDeployment.ReleaseID ||
		afterDeployment.ActualConfigurationRevisionID != input.v2Configuration.RevisionID ||
		afterDeployment.ActualConfigurationRevisionNumber != input.v2Configuration.RevisionNumber ||
		afterDeployment.DeploymentPlanRevisionID != input.v2Plan.RevisionID ||
		afterDeployment.DeploymentPlanRevisionNumber != input.v2Plan.RevisionNumber {
		t.Fatal("revoked migration changed the applying deployment or immutable pins")
	}
	afterRuntime, err := input.composition.state.Get(input.ctx, input.application.ID, beforeDeployment.ID)
	if err != nil || !reflect.DeepEqual(afterRuntime, beforeRuntime) || afterRuntime.Phase != generatedruntimestate.PhaseMigrating ||
		afterRuntime.MigrationState != generatedruntimestate.MigrationRunning || afterRuntime.MigrationStartedAt.IsZero() ||
		!afterRuntime.MigrationFinishedAt.IsZero() {
		t.Fatal("revoked migration changed runtime component, image, candidate, or immutable state")
	}
	afterActive, err := input.composition.state.Active(input.ctx, input.application.ID)
	if err != nil || !reflect.DeepEqual(afterActive, beforeActive) {
		t.Fatal("revoked migration changed the active route head")
	}
	snapshot := input.process.Snapshot()
	if snapshot.createCalls != 1 || snapshot.startCalls != 0 || snapshot.waitCalls != 0 || snapshot.removeCalls != 0 ||
		snapshot.attemptMutations != 1 || !snapshot.captured {
		t.Fatal("revoked migration crossed the create-only physical boundary")
	}
	input.process.AssertRetained(t, input.ctx)
	migrationAuthorityAssertRelationsAbsent(t, input.ctx, input.docker, input.fixtureEnvironment, input.fixtureRoot)
	controllerJourneyRoutedRequest(t, input.ctx, input.application.ID, http.MethodGet, "/counter", "", http.StatusOK, `"value":0`)
	resumeDiagnosticComplete()
}

func migrationAuthorityCreateMatches(args []string, expected migrationAuthorityProcessExpected) bool {
	name, exactName := migrationAuthorityArgument(args, "--name")
	if len(args) < 2 || args[0] != "container" || args[1] != "create" || !exactName || name != expected.containerName {
		return false
	}
	labels := migrationAuthorityLabels(args)
	return len(labels) == 8 && labels["io.rig.managed"] == "generated-migration" &&
		labels["io.rig.application"] == expected.applicationID && labels["io.rig.release"] == expected.releaseID &&
		labels["io.rig.deployment"] == expected.deploymentID && uuid.Validate(labels["io.rig.artifact"]) == nil &&
		labels["io.rig.plan"] == expected.planID && labels["io.rig.component"] == expected.componentName &&
		uuid.Validate(labels["io.rig.invocation"]) == nil
}

func migrationAuthorityStateMatches(deployment deployments.Deployment, runtimeState generatedruntimestate.Deployment, expected migrationAuthorityProcessExpected, artifactID string) bool {
	if deployment.ID != expected.deploymentID || deployment.AppID != expected.applicationID || deployment.JobID != expected.jobID ||
		deployment.Status != deployments.Applying || deployment.ReleaseID != expected.releaseID ||
		deployment.ActualConfigurationRevisionID != expected.configurationID ||
		deployment.ActualConfigurationRevisionNumber != expected.configurationNumber ||
		deployment.RuntimeStrategy != deployments.RuntimeGeneratedNode || !deployment.ProvenanceInitialized ||
		deployment.DeploymentPlanRevisionID != expected.planID || deployment.DeploymentPlanRevisionNumber != expected.planNumber {
		return false
	}
	if runtimeState.DeploymentID != expected.deploymentID || runtimeState.AppID != expected.applicationID ||
		runtimeState.ReleaseID != expected.releaseID || runtimeState.DeploymentPlanRevisionID != expected.planID ||
		runtimeState.DeploymentPlanRevisionNumber != expected.planNumber || runtimeState.Phase != generatedruntimestate.PhaseMigrating ||
		runtimeState.MigrationState != generatedruntimestate.MigrationRunning || runtimeState.MigrationStartedAt.IsZero() ||
		!runtimeState.MigrationFinishedAt.IsZero() || len(runtimeState.Components) != 1 {
		return false
	}
	component := runtimeState.Components[0]
	return component.DeploymentID == expected.deploymentID && component.Name == expected.componentName &&
		component.ImageArtifactID == artifactID && component.State == generatedruntimestate.ComponentImageReady &&
		component.ContainerName == "" && component.ContainerID == ""
}

func migrationAuthorityLabels(args []string) map[string]string {
	labels := map[string]string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--label" {
			continue
		}
		key, value, ok := strings.Cut(args[index+1], "=")
		if !ok || key == "" {
			return map[string]string{}
		}
		if _, duplicate := labels[key]; duplicate {
			return map[string]string{}
		}
		labels[key] = value
		index++
	}
	return labels
}

func migrationAuthorityArgument(args []string, name string) (string, bool) {
	value := ""
	found := false
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			if found {
				return "", false
			}
			value = args[index+1]
			found = true
			index++
		}
	}
	return value, found
}

func migrationAuthorityInspectionRequest(request runtimeprocess.CommandRequest) runtimeprocess.CommandRequest {
	return runtimeprocess.CommandRequest{
		Executable: request.Executable, Directory: request.Directory, Env: append([]string(nil), request.Env...),
		Timeout: 10 * time.Second, OutputLimit: 8 << 10,
	}
}

func migrationAuthorityInspectionMatches(inspection migrationAuthorityContainerInspection, captured migrationAuthorityCapturedContainer) bool {
	return inspection.ID == captured.id && strings.TrimPrefix(inspection.Name, "/") == captured.expected.containerName &&
		inspection.Status == "created" && !inspection.Running && inspection.StartedAt == "0001-01-01T00:00:00Z" &&
		migrationAuthorityContainsLabels(inspection.Labels, captured.labels) && inspection.Labels["io.rig.invocation"] == captured.nonce
}

func migrationAuthorityContainsLabels(actual, expected map[string]string) bool {
	if len(actual) < len(expected) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func migrationAuthorityCloneLabels(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func migrationAuthorityDefiniteAbsence(ctx context.Context, result runtimeprocess.CommandResult, runErr error, identity string) bool {
	exited, ok := runErr.(*exec.ExitError)
	if ctx == nil || ctx.Err() != nil || result.StdoutTruncated || result.StderrTruncated ||
		!ok || exited.ProcessState == nil || !exited.ProcessState.Exited() || exited.ProcessState.ExitCode() != 1 ||
		len(bytes.TrimSpace(result.Stdout)) != 0 {
		return false
	}
	message := strings.TrimSpace(string(result.Stderr))
	return message == "Error response from daemon: No such container: "+identity ||
		message == "Error: No such container: "+identity ||
		message == "Error: No such object: "+identity
}

func migrationAuthorityClearResult(result *runtimeprocess.CommandResult) {
	clear(result.Stdout)
	clear(result.Stderr)
	result.Stdout = nil
	result.Stderr = nil
}

func migrationAuthorityLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func migrationAuthorityReleaseID(value string) bool {
	return uuid.Validate(value) == nil || migrationAuthorityLowerHex(value, 32)
}

func migrationAuthorityAssertRelationsAbsent(t *testing.T, ctx context.Context, docker string, environment []string, fixtureRoot string) {
	t.Helper()
	for _, relation := range []string{"rig_migration_ledger", "rig_migration_counter", "rig_migration_history", "rig_migration_history_lock"} {
		migrationJourneyAssertTableAbsent(t, ctx, docker, environment, fixtureRoot, relation)
	}
}
