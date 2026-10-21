package composeruntime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/applifecycle"
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deploymentbinding"
	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimedocker "github.com/hostd/hostd/internal/runtime/docker"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
	"github.com/hostd/hostd/internal/testsupport/databasefixture"
)

type fakeApplications struct {
	application apps.Application
	err         error
}

func (f *fakeApplications) Get(string) (apps.Application, error) {
	return f.application, f.err
}

type fakeReleases struct {
	release               releasesnapshot.Release
	err                   error
	materializeErr        error
	readyErr              error
	materializeCalls      int
	localMaterializeCalls int
	readyCalls            int
	materializeOwner      string
	materializeApp        string
	readyApp              string
	readyRelease          string
	beforeMaterialize     func()
}

func (f *fakeReleases) Materialize(_ context.Context, owner, appID string) (releasesnapshot.Release, error) {
	f.materializeCalls++
	if f.beforeMaterialize != nil {
		f.beforeMaterialize()
	}
	f.materializeOwner, f.materializeApp = owner, appID
	if f.materializeErr != nil {
		return releasesnapshot.Release{}, f.materializeErr
	}
	return f.release, f.err
}

func (f *fakeReleases) MaterializeLocal(_ context.Context, appID, sourcePath string) (releasesnapshot.Release, error) {
	f.localMaterializeCalls++
	f.materializeApp = appID
	if f.materializeErr != nil {
		return releasesnapshot.Release{}, f.materializeErr
	}
	return f.release, f.err
}

func (f *fakeReleases) ReadyRelease(_ context.Context, appID, releaseID string) (releasesnapshot.Release, error) {
	f.readyCalls++
	f.readyApp, f.readyRelease = appID, releaseID
	if f.readyErr != nil {
		return releasesnapshot.Release{}, f.readyErr
	}
	return f.release, f.err
}

type revisionRequest struct {
	revisionID     string
	revisionNumber int64
}

type fakeConfiguration struct {
	current         appconfig.ExecutionConfiguration
	exact           appconfig.ExecutionConfiguration
	err             error
	currentErr      error
	exactErr        error
	currentCalls    int
	revisionRequest []revisionRequest
}

func (f *fakeConfiguration) ExportCurrentForExecution(context.Context, string) (appconfig.ExecutionConfiguration, error) {
	f.currentCalls++
	if f.currentErr != nil {
		return appconfig.ExecutionConfiguration{}, f.currentErr
	}
	return cloneExecutionConfiguration(f.current), f.err
}

func (f *fakeConfiguration) ExportRevisionForExecution(_ context.Context, _ string, revisionID string, revisionNumber int64) (appconfig.ExecutionConfiguration, error) {
	f.revisionRequest = append(f.revisionRequest, revisionRequest{revisionID: revisionID, revisionNumber: revisionNumber})
	if f.exactErr != nil {
		return appconfig.ExecutionConfiguration{}, f.exactErr
	}
	return cloneExecutionConfiguration(f.exact), f.err
}

func cloneExecutionConfiguration(value appconfig.ExecutionConfiguration) appconfig.ExecutionConfiguration {
	value.Environment = append([]byte(nil), value.Environment...)
	value.SecretOrigins = append([]appconfig.SecretOrigin(nil), value.SecretOrigins...)
	for index := range value.SecretOrigins {
		value.SecretOrigins[index].Key = append([]byte(nil), value.SecretOrigins[index].Key...)
		value.SecretOrigins[index].Value = append([]byte(nil), value.SecretOrigins[index].Value...)
	}
	return value
}

type fakeRunner struct {
	requests []runtimeprocess.CommandRequest
	run      func(int, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error)
}

func (f *fakeRunner) Run(_ context.Context, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	f.requests = append(f.requests, request)
	return f.run(len(f.requests)-1, request)
}

type recordingReporter struct {
	updates []jobs.ProgressUpdate
	errAt   string
	err     error
	before  func(jobs.ProgressUpdate)
}

func (r *recordingReporter) Report(update jobs.ProgressUpdate) error {
	r.updates = append(r.updates, update)
	if r.before != nil {
		r.before(update)
	}
	if update.Phase == r.errAt {
		if r.err != nil {
			return r.err
		}
		return errors.New("report failed")
	}
	return nil
}

func TestExecutorRequiresConfiguredDeploymentExecutionRepository(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	for _, test := range []struct {
		name       string
		repository *deployments.Repository
	}{
		{name: "nil", repository: nil},
		{name: "unguarded", repository: deployments.New(fixture.db)},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor, err := NewExecutor(fixture.applications, fixture.releases, fixture.configuration, test.repository, fixture.executor.temporary, fixture.runner, fixture.executor.options)
			if executor != nil || err == nil {
				t.Fatalf("executor=%#v error=%v", executor, err)
			}
		})
	}
}

func TestExecutorRejectsZeroOrMismatchedClaimBeforePreparation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*jobs.Job, *deploymentexecution.Claim)
	}{
		{name: "zero claim", mutate: func(_ *jobs.Job, claim *deploymentexecution.Claim) { *claim = deploymentexecution.Claim{} }},
		{name: "job mismatch", mutate: func(job *jobs.Job, _ *deploymentexecution.Claim) { job.ID = uuid.NewString() }},
		{name: "application mismatch", mutate: func(job *jobs.Job, _ *deploymentexecution.Claim) { job.ResourceID = uuid.NewString() }},
		{name: "attempt mismatch", mutate: func(job *jobs.Job, _ *deploymentexecution.Claim) { job.Attempt++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			job, claim := fixture.job, fixture.claim
			test.mutate(&job, &claim)
			result, err := fixture.executor.ExecuteBound(fixture.context, job, claim, &fixture.reporter)
			var executionErr *jobs.ExecutionError
			if result != (jobs.ExecutionResult{}) || !errors.As(err, &executionErr) || executionErr.Code != "validation_failed" || !errors.Is(err, deploymentexecution.ErrInvalidClaim) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if len(fixture.reporter.updates) != 0 || fixture.deploymentCount(t) != 0 || fixture.releases.materializeCalls != 0 ||
				fixture.releases.localMaterializeCalls != 0 || fixture.releases.readyCalls != 0 || fixture.configuration.currentCalls != 0 ||
				len(fixture.configuration.revisionRequest) != 0 || len(fixture.runner.requests) != 0 {
				t.Fatalf("claim refusal performed work: progress=%#v deployments=%d releases=%#v configuration=%#v runner=%#v", fixture.reporter.updates, fixture.deploymentCount(t), fixture.releases, fixture.configuration, fixture.runner.requests)
			}
		})
	}
}

func TestExecutorUsesInspectedEffectiveConfigAsSoleMutationInput(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	t.Setenv("HOSTD_SENTINEL_SECRET", "must-not-be-forwarded")
	const configJSON = `{"services":{"web":{"image":"nginx","environment":{"TOKEN":"render-secret"}}}}`
	configStdout := []byte(configJSON)
	configStderr := []byte("config-secret")
	upStdout := []byte("up-secret")
	upStderr := []byte("up-error-secret")
	fixture.runner.run = func(index int, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if containsEnvironmentKey(request.Env, "HOSTD_SENTINEL_SECRET") {
			t.Fatalf("ambient secret forwarded: %v", request.Env)
		}
		if index == 0 {
			environment, err := os.ReadFile(valueAfter(request.Args, "--env-file"))
			if err != nil || string(environment) != "TOKEN='runtime-secret'\n" {
				t.Fatalf("runtime env=%q err=%v", environment, err)
			}
			return runtimeprocess.CommandResult{Stdout: configStdout, Stderr: configStderr}, nil
		}
		persisted := fixture.deployment(t)
		if persisted.Status != deployments.Applying {
			t.Fatalf("final policy gate status before up=%s", persisted.Status)
		}
		composePath := valueAfter(request.Args, "-f")
		if composePath == filepath.Join(fixture.workspace, "compose.yaml") {
			t.Fatal("source Compose file reused for mutation")
		}
		effective, err := os.ReadFile(composePath)
		if err != nil || string(effective) != configJSON {
			t.Fatalf("effective config=%q err=%v", effective, err)
		}
		return runtimeprocess.CommandResult{Stdout: upStdout, Stderr: upStderr}, nil
	}
	result, err := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter)
	if err != nil || result.CompletionCode != "deployment_completed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted := fixture.deployment(t)
	if persisted.Status != deployments.Succeeded || persisted.ReleaseID != fixture.releases.release.ID || persisted.ActualConfigurationRevisionID != fixture.configuration.current.RevisionID {
		t.Fatalf("deployment=%#v", persisted)
	}
	assertExactExecutorRequests(t, fixture, fixture.runner.requests)
	assertProgressPhases(t, fixture.reporter.updates, []string{"validate", "prepare_workspace", "materialize_release", "render_compose", "evaluate_policy", "apply_runtime", "wait_for_health", "finalize"})
	assertCleared(t, configStdout, configStderr, upStdout, upStderr)
	assertRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestExecutorRejectsSanitizedSecretDerivedPolicyBeforeMutation(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	const secret = "net_admin"
	fixture.configuration.current.Environment = []byte("TOKEN='" + secret + "'\n")
	fixture.configuration.current.SecretOrigins = []appconfig.SecretOrigin{{RevisionID: fixture.configuration.current.RevisionID, RevisionNumber: 1, Key: []byte("TOKEN"), Value: []byte(secret)}}
	configStdout := []byte(`{"services":{"web":{"cap_add":["NET_ADMIN"]}}}`)
	fixture.runner.run = successfulRunner(runtimeprocess.CommandResult{Stdout: configStdout})
	_, err := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter)
	var executionErr *jobs.ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != "policy_rejected" || len(fixture.runner.requests) != 1 {
		t.Fatalf("error=%v requests=%d", err, len(fixture.runner.requests))
	}
	findings := fixture.findings(t)
	if len(findings) != 1 || findings[0].Disposition != DispositionRejected || !strings.Contains(findings[0].Scope, "secret-origin:") {
		t.Fatalf("findings=%#v", findings)
	}
	encoded, _ := json.Marshal(findings)
	if bytes.Contains(bytes.ToLower(encoded), []byte(secret)) {
		t.Fatalf("secret reached findings: %s", encoded)
	}
	if fixture.deployment(t).Status != deployments.Failed {
		t.Fatalf("deployment=%#v", fixture.deployment(t))
	}
	assertCleared(t, configStdout)
	assertRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestExecutorFailureAndPauseTaxonomyCleansProtectedFiles(t *testing.T) {
	tests := []struct {
		name       string
		model      runtimeprocess.CommandResult
		configErr  error
		upResult   runtimeprocess.CommandResult
		upErr      error
		cleanupErr bool
		reportErr  bool
		cancelAt   int
		wantCode   string
		wantStatus deployments.Status
		wantRuns   int
		wantPause  bool
	}{
		{name: "config invalid", configErr: errors.New("exit status 1"), wantCode: "compose_config_invalid", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config stdout truncated", model: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{}}}`), StdoutTruncated: true}, wantCode: "compose_config_output_truncated", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config stderr truncated", model: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{}}}`), StderrTruncated: true}, wantCode: "compose_config_output_truncated", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config empty", wantCode: "compose_config_invalid", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config malformed", model: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{}}`)}, wantCode: "compose_config_invalid", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config timeout", configErr: context.DeadlineExceeded, wantCode: "compose_config_timeout", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config termination", configErr: errors.Join(context.DeadlineExceeded, runtimeprocess.ErrTerminationFailed), wantCode: "process_termination_failed", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "config cancellation", configErr: context.Canceled, cancelAt: 1, wantCode: "cancelled", wantStatus: deployments.Cancelled, wantRuns: 1},
		{name: "approval pause", model: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{"privileged":true}}}`)}, wantStatus: deployments.NeedsAttention, wantRuns: 1, wantPause: true},
		{name: "policy rejection", model: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{"build":"https://example.invalid/repository.git"}}}`)}, wantCode: "policy_rejected", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "up nonzero retains applying", model: validModel(), upErr: errors.New("exit status 1"), wantCode: "compose_apply_failed", wantStatus: deployments.Applying, wantRuns: 2},
		{name: "up timeout retains applying", model: validModel(), upErr: context.DeadlineExceeded, wantCode: "compose_apply_timeout", wantStatus: deployments.Applying, wantRuns: 2},
		{name: "up truncation retains applying", model: validModel(), upResult: runtimeprocess.CommandResult{StdoutTruncated: true}, wantCode: "compose_apply_output_truncated", wantStatus: deployments.Applying, wantRuns: 2},
		{name: "up stderr truncation retains applying", model: validModel(), upResult: runtimeprocess.CommandResult{StderrTruncated: true}, wantCode: "compose_apply_output_truncated", wantStatus: deployments.Applying, wantRuns: 2},
		{name: "up termination retains applying", model: validModel(), upErr: errors.Join(context.DeadlineExceeded, runtimeprocess.ErrTerminationFailed), wantCode: "process_termination_failed", wantStatus: deployments.Applying, wantRuns: 2},
		{name: "up cancellation retains applying", model: validModel(), upErr: context.Canceled, cancelAt: 2, wantCode: "cancelled", wantStatus: deployments.Applying, wantRuns: 2},
		{name: "pre-gate cleanup failure", configErr: errors.New("exit status 1"), cleanupErr: true, wantCode: "internal_error", wantStatus: deployments.Failed, wantRuns: 1},
		{name: "cleanup after acknowledgement retains waiting", model: validModel(), cleanupErr: true, wantCode: "internal_error", wantStatus: deployments.WaitingHealth, wantRuns: 2},
		{name: "report after acknowledgement retains waiting", model: validModel(), reportErr: true, wantCode: "internal_error", wantStatus: deployments.WaitingHealth, wantRuns: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			executionContext := fixture.context
			var cancel context.CancelCauseFunc
			if test.cancelAt != 0 {
				executionContext, cancel = context.WithCancelCause(fixture.context)
			}
			fixture.runner.run = func(index int, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				if test.cancelAt == index+1 {
					cancel(jobs.ErrCancellationRequested)
				}
				if index == 0 {
					return test.model, test.configErr
				}
				return test.upResult, test.upErr
			}
			if test.cleanupErr {
				original := fixture.executor.cleanup
				fixture.executor.cleanup = func(files *securetemp.Files) error { _ = original(files); return errors.New("cleanup failed") }
			}
			if test.reportErr {
				fixture.reporter.errAt = "finalize"
			}
			result, err := fixture.executor.ExecuteBound(executionContext, fixture.job, fixture.claim, &fixture.reporter)
			if test.wantPause {
				if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != "approval_required" {
					t.Fatalf("result=%#v err=%v", result, err)
				}
			} else {
				var ee *jobs.ExecutionError
				if !errors.As(err, &ee) || ee.Code != test.wantCode {
					t.Fatalf("error=%v want=%s", err, test.wantCode)
				}
			}
			if got := fixture.deployment(t).Status; got != test.wantStatus {
				t.Fatalf("status=%s want=%s", got, test.wantStatus)
			}
			if len(fixture.runner.requests) != test.wantRuns {
				t.Fatalf("runs=%d want=%d", len(fixture.runner.requests), test.wantRuns)
			}
			for _, request := range fixture.runner.requests {
				if request.Executable == "git" || containsArgument(request.Args, "down") || containsArgument(request.Args, "rm") || containsArgument(request.Args, "rollback") {
					t.Fatalf("failure path issued forbidden source/rollback command: %#v", request)
				}
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestExecutorParentShutdownLeavesDeploymentForRecovery(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	ctx, cancel := context.WithCancel(fixture.context)
	fixture.runner.run = func(index int, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if index == 0 {
			return validModel(), nil
		}
		cancel()
		return runtimeprocess.CommandResult{}, context.Canceled
	}
	_, err := fixture.executor.ExecuteBound(ctx, fixture.job, fixture.claim, &fixture.reporter)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, deploymentexecution.ErrCanceled) {
		t.Fatalf("shutdown error=%v", err)
	}
	if fixture.deployment(t).Status != deployments.Applying || len(fixture.runner.requests) != 2 {
		t.Fatalf("deployment=%#v requests=%v", fixture.deployment(t), fixture.runner.requests)
	}
	assertRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestExecutorRejectsInvalidRequestedActorBeforeSideEffects(t *testing.T) {
	for _, actor := range []string{"", "owner", "not-a-uuid"} {
		t.Run(actor, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			fixture.job.RequestedBy = actor
			_, err := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter)
			var ee *jobs.ExecutionError
			if !errors.As(err, &ee) || ee.Code != "validation_failed" || fixture.deploymentCount(t) != 0 || len(fixture.runner.requests) != 0 {
				t.Fatalf("error=%v deployments=%d requests=%d", err, fixture.deploymentCount(t), len(fixture.runner.requests))
			}
		})
	}
}

func TestExecutorPreservesGateRefusalAndCancellationWithoutFailureMutation(t *testing.T) {
	for _, test := range []struct {
		name    string
		cancel  bool
		cleanup bool
	}{
		{name: "live authority refusal with cleanup", cleanup: true},
		{name: "cancelled gate", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			ctx := fixture.context
			var cancel context.CancelFunc
			if test.cancel {
				ctx, cancel = context.WithCancel(fixture.context)
			}
			fixture.runner.run = func(index int, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				if index == 0 {
					if test.cancel {
						cancel()
					} else if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=2 WHERE id=?`, fixture.job.ID); err != nil {
						t.Fatal(err)
					}
					return validModel(), nil
				}
				return runtimeprocess.CommandResult{}, nil
			}
			cleanupFailure := errors.New("cleanup failed")
			if test.cleanup {
				original := fixture.executor.cleanup
				fixture.executor.cleanup = func(files *securetemp.Files) error { _ = original(files); return cleanupFailure }
			}
			_, err := fixture.executor.ExecuteBound(ctx, fixture.job, fixture.claim, &fixture.reporter)
			var ee *jobs.ExecutionError
			if !errors.As(err, &ee) || ee.Code != "runtime_unavailable" || len(fixture.runner.requests) != 1 || fixture.deployment(t).Status != deployments.Preparing {
				t.Fatalf("error=%v requests=%d deployment=%#v", err, len(fixture.runner.requests), fixture.deployment(t))
			}
			if !test.cancel && !errors.Is(err, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("identity cause missing: %v", err)
			}
			if test.cancel && (!errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancellation missing: %v", err)
			}
			if test.cleanup && !errors.Is(err, cleanupFailure) {
				t.Fatalf("cleanup missing: %v", err)
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestExecutorPriorReleaseConfigurationModesAndResumePin(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        jobs.ConfigurationMode
		initialized bool
		wantCurrent int
		wantNumber  int64
	}{
		{name: "prior original", mode: jobs.ConfigurationOriginal, wantNumber: 4},
		{name: "prior current", mode: jobs.ConfigurationCurrent, wantCurrent: 1, wantNumber: 1},
		{name: "resume uses pinned actual", mode: jobs.ConfigurationCurrent, initialized: true, wantNumber: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, test.mode, strings.Repeat("a", 32))
			fixture.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
			wantID := fixture.configuration.current.RevisionID
			if test.wantNumber == 4 {
				wantID = fixture.releaseConfigID
			}
			if test.wantNumber == 7 {
				wantID = fixture.pinnedConfigID
			}
			fixture.configuration.exact = appconfig.ExecutionConfiguration{RevisionID: wantID, RevisionNumber: test.wantNumber, Environment: []byte("TOKEN='exact'\n")}
			if test.initialized {
				deployment, _, err := fixture.deployments.GetOrCreateByJob(context.Background(), fixture.job.ResourceID, fixture.job.ID, string(test.mode))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = fixture.deployments.Initialize(context.Background(), fixture.job.ResourceID, deployment.ID, fixture.releases.release.ID, fixture.pinnedConfigID, 7); err != nil {
					t.Fatal(err)
				}
				if _, err = fixture.deployments.Transition(context.Background(), fixture.job.ResourceID, deployment.ID, deployments.NeedsAttention, "approval_required"); err != nil {
					t.Fatal(err)
				}
			}
			fixture.runner.run = successfulRunner(validModel())
			if _, err := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter); err != nil {
				t.Fatal(err)
			}
			if fixture.configuration.currentCalls != test.wantCurrent {
				t.Fatalf("current calls=%d", fixture.configuration.currentCalls)
			}
			if fixture.releases.readyCalls != 1 || fixture.releases.materializeCalls != 0 || fixture.releases.localMaterializeCalls != 0 {
				t.Fatalf("prior release resolution=%#v", fixture.releases)
			}
			if test.wantCurrent == 0 && (len(fixture.configuration.revisionRequest) != 1 || fixture.configuration.revisionRequest[0].revisionID != wantID || fixture.configuration.revisionRequest[0].revisionNumber != test.wantNumber) {
				t.Fatalf("exact requests=%v", fixture.configuration.revisionRequest)
			}
			persisted := fixture.deployment(t)
			if persisted.ActualConfigurationRevisionID != wantID || persisted.ActualConfigurationRevisionNumber != test.wantNumber {
				t.Fatalf("deployment=%#v", persisted)
			}
		})
	}
}

func TestExecutorLatestGitHubUsesRequestedActorAndCurrentConfiguration(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	fixture.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
	fixture.releases.release.SourceProvider = apps.SourceGitHub
	fixture.runner.run = successfulRunner(validModel())
	if _, err := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter); err != nil {
		t.Fatal(err)
	}
	if fixture.releases.materializeCalls != 1 || fixture.releases.materializeOwner != fixture.job.RequestedBy || fixture.configuration.currentCalls != 1 {
		t.Fatalf("releases=%#v configuration=%#v", fixture.releases, fixture.configuration)
	}
	if fixture.deployment(t).ReleaseID != fixture.releases.release.ID {
		t.Fatalf("deployment=%#v", fixture.deployment(t))
	}
}

func TestExecutorMapsSourceAndConfigurationDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, code string
		prepare    func(*executorFixture)
		cancel     bool
		wantStatus deployments.Status
	}{
		{name: "application unavailable", code: "invalid_source", wantStatus: deployments.Failed, prepare: func(f *executorFixture) { f.applications.err = errors.New("database unavailable") }},
		{name: "provider unavailable", code: "provider_unavailable", prepare: func(f *executorFixture) {
			f.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
			f.releases.materializeErr = &releasesnapshot.Error{Code: "provider_unavailable"}
		}, wantStatus: deployments.Failed},
		{name: "source access lost", code: "source_access_lost", wantStatus: deployments.Failed, prepare: githubFailure(&releasesnapshot.Error{Code: "source_access_lost"})},
		{name: "source too large", code: "source_too_large", wantStatus: deployments.Failed, prepare: githubFailure(&releasesnapshot.Error{Code: "source_too_large"})},
		{name: "source storage full", code: "source_storage_full", wantStatus: deployments.Failed, prepare: githubFailure(&releasesnapshot.Error{Code: releasesnapshot.ErrorCodeSourceStorageFull})},
		{name: "provider cancellation", code: "cancelled", cancel: true, wantStatus: deployments.Cancelled, prepare: githubFailure(&releasesnapshot.Error{Code: "canceled"})},
		{name: "current configuration unavailable", code: "configuration_unavailable", wantStatus: deployments.Failed, prepare: func(f *executorFixture) { f.configuration.currentErr = errors.New("corrupt bundle") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			test.prepare(&fixture)
			executionContext := fixture.context
			if test.cancel {
				var cancel context.CancelCauseFunc
				executionContext, cancel = context.WithCancelCause(fixture.context)
				fixture.releases.beforeMaterialize = func() { cancel(jobs.ErrCancellationRequested) }
			}
			_, err := fixture.executor.ExecuteBound(executionContext, fixture.job, fixture.claim, &fixture.reporter)
			var ee *jobs.ExecutionError
			if !errors.As(err, &ee) || ee.Code != test.code || fixture.deployment(t).Status != test.wantStatus || len(fixture.runner.requests) != 0 {
				t.Fatalf("error=%v deployment=%#v requests=%d", err, fixture.deployment(t), len(fixture.runner.requests))
			}
		})
	}
}

func githubFailure(err error) func(*executorFixture) {
	return func(f *executorFixture) {
		f.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
		f.releases.materializeErr = err
	}
}

func TestExecutorLocalDirectoryDirectFileAndAmbiguousSource(t *testing.T) {
	for _, test := range []struct {
		name              string
		direct, ambiguous bool
	}{
		{name: "directory"}, {name: "direct file", direct: true}, {name: "ambiguous directory", ambiguous: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			root := t.TempDir()
			wantCompose := filepath.Join("deploy", "compose.yaml")
			if test.direct || test.ambiguous {
				wantCompose = "compose.yaml"
			}
			compose := filepath.Join(root, wantCompose)
			if err := os.MkdirAll(filepath.Dir(compose), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.applications.application.Source = apps.Source{Type: apps.SourceLocal, Path: root}
			if test.direct {
				fixture.applications.application.Source.Path = compose
			}
			if test.ambiguous {
				if err := os.WriteFile(filepath.Join(root, "compose.yml"), []byte("services:{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixture.releases.materializeErr = &releasesnapshot.Error{Code: "invalid_source"}
			}
			managed := t.TempDir()
			managedCompose := filepath.Join(managed, wantCompose)
			if err := os.MkdirAll(filepath.Dir(managedCompose), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(managedCompose, []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			managed, _ = canonicalPath(managed)
			fixture.releases.release.WorkspacePath = managed
			fixture.releases.release.ComposePath = filepath.ToSlash(wantCompose)
			fixture.runner.run = successfulRunner(validModel())
			_, err := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter)
			if test.ambiguous {
				var ee *jobs.ExecutionError
				if !errors.As(err, &ee) || ee.Code != "invalid_source" || len(fixture.runner.requests) != 0 {
					t.Fatalf("error=%v requests=%d", err, len(fixture.runner.requests))
				}
				return
			}
			if err != nil || len(fixture.runner.requests) != 2 || fixture.runner.requests[0].Directory != managed {
				t.Fatalf("error=%v requests=%#v", err, fixture.runner.requests)
			}
			config := fixture.runner.requests[0]
			if valueAfter(config.Args, "--project-directory") != managed || valueAfter(config.Args, "-f") != filepath.Join(managed, wantCompose) {
				t.Fatalf("config request=%#v", config)
			}
		})
	}
}

func TestExecutorManagedWorkspaceBindIsReadOnlyForGitHubAndLocal(t *testing.T) {
	for _, sourceType := range []string{apps.SourceGitHub, apps.SourceLocal} {
		for _, readOnly := range []bool{false, true} {
			t.Run(sourceType+map[bool]string{false: "/writable", true: "/read-only"}[readOnly], func(t *testing.T) {
				fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
				fixture.applications.application.Source.Type = sourceType
				fixture.releases.release.SourceProvider = sourceType
				body, err := json.Marshal(serviceModel(map[string]any{"volumes": []any{map[string]any{"type": "bind", "source": fixture.workspace, "target": "/app", "read_only": readOnly}}}))
				if err != nil {
					t.Fatal(err)
				}
				fixture.runner.run = successfulRunner(runtimeprocess.CommandResult{Stdout: body})
				_, executeErr := fixture.executor.ExecuteBound(fixture.context, fixture.job, fixture.claim, &fixture.reporter)
				findings := fixture.findings(t)
				wantCapability := "writable_managed_bind"
				if readOnly {
					wantCapability = "workspace_bind_mount"
				}
				if readOnly {
					if executeErr != nil || len(fixture.runner.requests) != 2 || len(findings) != 1 || findings[0].Capability != wantCapability || findings[0].Disposition != DispositionAllowed {
						t.Fatalf("error=%v findings=%#v requests=%d", executeErr, findings, len(fixture.runner.requests))
					}
				} else {
					var ee *jobs.ExecutionError
					if !errors.As(executeErr, &ee) || ee.Code != "policy_rejected" || len(fixture.runner.requests) != 1 || len(findings) != 1 || findings[0].Capability != wantCapability || findings[0].Disposition != DispositionRejected {
						t.Fatalf("error=%v findings=%#v requests=%d", executeErr, findings, len(fixture.runner.requests))
					}
				}
			})
		}
	}
}

type executorFixture struct {
	executor         *Executor
	applications     *fakeApplications
	releases         *fakeReleases
	configuration    *fakeConfiguration
	deployments      *deployments.Repository
	db               *sql.DB
	context          context.Context
	workingDirectory string
	releaseScope     func() error
	runner           *fakeRunner
	reporter         recordingReporter
	job              jobs.Job
	claim            deploymentexecution.Claim
	workspace        string
	dataRoot         string
	releaseConfigID  string
	pinnedConfigID   string
}

func (f executorFixture) deployment(t *testing.T) deployments.Deployment {
	t.Helper()
	var id string
	if err := f.db.QueryRow(`SELECT id FROM deployments WHERE job_id=?`, f.job.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	value, err := f.deployments.Get(context.Background(), f.job.ResourceID, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f executorFixture) deploymentCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE job_id=?`, f.job.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f executorFixture) findings(t *testing.T) []deployments.Finding {
	t.Helper()
	if f.deploymentCount(t) == 0 {
		return nil
	}
	return f.deployment(t).Findings
}

func newExecutorFixture(t *testing.T, mode jobs.ConfigurationMode, releaseID string) executorFixture {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := canonicalPath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	temporary, err := securetemp.New(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	applicationID := uuid.NewString()
	applications := &fakeApplications{application: apps.Application{ID: applicationID, Source: apps.Source{Type: apps.SourceLocal, Path: workspace}}}
	releases := &fakeReleases{release: releasesnapshot.Release{ID: strings.Repeat("a", 32), AppID: applicationID, SourceProvider: "local", ResolvedSHA: strings.Repeat("b", 64), ComposePath: "compose.yaml", WorkspacePath: workspace, WorkspaceState: releasesnapshot.WorkspaceStateReady}}
	configuration := &fakeConfiguration{
		current: appconfig.ExecutionConfiguration{RevisionID: uuid.NewString(), RevisionNumber: 1, Environment: []byte("TOKEN='runtime-secret'\n")},
	}
	db, job, claim, deploymentRepository, executionContext, workingDirectory, releaseScope := newBoundComposeJob(t, applicationID, dataRoot)
	configurationID := configuration.current.RevisionID
	releaseConfigurationID := uuid.NewString()
	pinnedConfigurationID := uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, revision := range []struct {
		id     string
		number int64
	}{{configurationID, 1}, {releaseConfigurationID, 4}, {pinnedConfigurationID, 7}} {
		if _, err := db.Exec(`INSERT INTO application_configuration_revisions(id,app_id,revision_number,bundle_ref,created_by,created_at,variable_count,secret_count) VALUES(?,?,?,?,?,?,0,1)`, revision.id, applicationID, revision.number, "fixture:"+revision.id, job.RequestedBy, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE application_configuration_heads SET revision_id=?,revision_number=1,updated_at=? WHERE app_id=?`, configurationID, stamp, applicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO releases(id,app_id,status,metadata_json,created_at,workspace_state,workspace_tree_sha256,configuration_revision_id,configuration_revision_number) VALUES(?,?,'ready','{}',?,'ready',?,?,4)`, releases.release.ID, applicationID, stamp, strings.Repeat("d", 64), releaseConfigurationID); err != nil {
		t.Fatal(err)
	}
	releases.release.ConfigurationRevisionID = releaseConfigurationID
	releases.release.ConfigurationRevisionNumber = 4
	runner := &fakeRunner{}
	executor, err := NewExecutor(applications, releases, configuration, deploymentRepository, temporary, runner, ExecutorOptions{
		DockerExecutable: "docker-test",
		DockerEndpoint:   "npipe:////./pipe/docker_engine",
		ConfigTimeout:    5 * time.Second,
		ApplyTimeout:     30 * time.Second,
		WaitTimeout:      10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(jobs.DeploymentInput{ReleaseID: releaseID, ConfigurationMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	job.Input = input
	return executorFixture{
		executor:      executor,
		applications:  applications,
		releases:      releases,
		configuration: configuration,
		deployments:   deploymentRepository,
		runner:        runner,
		job:           job, claim: claim, db: db, context: executionContext, workingDirectory: workingDirectory, releaseScope: releaseScope,
		workspace:       workspace,
		dataRoot:        dataRoot,
		releaseConfigID: releaseConfigurationID,
		pinnedConfigID:  pinnedConfigurationID,
	}
}

func newBoundComposeJob(t *testing.T, appID, dataRoot string) (*sql.DB, jobs.Job, deploymentexecution.Claim, *deployments.Repository, context.Context, string, func() error) {
	t.Helper()
	db, err := databasefixture.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	actorID := uuid.NewString()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, actorID, actorID, "hash", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO applications(id,slug,name,status,created_at,updated_at) VALUES(?,?,?,'deployed',?,?)`, appID, appID, appID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	validator, err := applifecycle.NewDeploymentBindingValidator(applifecycle.New(db))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := deploymentbinding.New(db, validator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.NewWithDeploymentBindings(db, bindings)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateDeploymentWithInputTransaction(context.Background(), jobs.DeploymentCreateRequest{
		ApplicationID: appID, IdempotencyKey: "compose-executor", RequestedBy: actorID,
		Input: jobs.DeploymentInput{ConfigurationMode: jobs.ConfigurationCurrent},
	}, jobs.DeploymentCreateHooks{})
	if err != nil || !created.Created {
		t.Fatalf("create bound Compose job: created=%t error=%v", created.Created, err)
	}
	job := created.Job
	job.Attempt = 1
	if _, err := db.Exec(`UPDATE jobs SET status='running',phase='running',attempt=1,started_at=?,updated_at=? WHERE id=?`, stamp, stamp, job.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := deploymentexecution.NewClaim(created.Binding, job.ID, appID, job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := applifecycle.New(db)
	positive, err := applifecycle.NewPositiveFenceValidator(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := runtimedocker.PrepareControllerDirectories(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := deploymentexecution.NewGuard(db, bindings, positive, directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := deployments.NewWithExecutionGuard(db, guard)
	if err != nil {
		t.Fatal(err)
	}
	scope, releaseScope, err := deploymenteffects.AcquireScoped(context.Background(), directories.WorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := releaseScope(); err != nil {
			t.Errorf("release scope: %v", err)
		}
	})
	return db, job, claim, repository, deploymenteffects.WithScope(context.Background(), scope), directories.WorkingDirectory, releaseScope
}

func successfulRunner(model runtimeprocess.CommandResult) func(int, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
	return func(index int, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if index == 0 {
			return model, nil
		}
		return runtimeprocess.CommandResult{}, nil
	}
}

func validModel() runtimeprocess.CommandResult {
	return runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{"image":"nginx"}}}`)}
}

func assertExactExecutorRequests(t *testing.T, fixture executorFixture, requests []runtimeprocess.CommandRequest) {
	t.Helper()
	if len(requests) != 2 {
		t.Fatalf("requests=%#v", requests)
	}
	project := "rig-" + strings.ReplaceAll(fixture.job.ResourceID, "-", "")
	config := requests[0]
	wantConfig := []string{"compose", "--project-name", project, "--project-directory", fixture.workspace, "--env-file", valueAfter(config.Args, "--env-file"), "-f", filepath.Join(fixture.workspace, "compose.yaml"), "config", "--format", "json", "--no-env-resolution"}
	if !reflect.DeepEqual(config.Args, wantConfig) || config.Executable != "docker-test" || config.Directory != fixture.workspace || config.Timeout != 5*time.Second || config.OutputLimit != runtimeprocess.DefaultOutputLimit {
		t.Fatalf("config request=%#v want args=%#v", config, wantConfig)
	}
	up := requests[1]
	wantUp := []string{"compose", "--project-name", project, "--project-directory", fixture.workspace, "--env-file", valueAfter(up.Args, "--env-file"), "-f", valueAfter(up.Args, "-f"), "up", "-d", "--wait", "--wait-timeout", "10"}
	if !reflect.DeepEqual(up.Args, wantUp) || up.Executable != "docker-test" || up.Directory != fixture.workspace || up.Timeout != 30*time.Second || up.OutputLimit != runtimeprocess.DefaultOutputLimit {
		t.Fatalf("up request=%#v want args=%#v", up, wantUp)
	}
	if valueAfter(config.Args, "--env-file") != valueAfter(up.Args, "--env-file") || valueAfter(config.Args, "-f") == valueAfter(up.Args, "-f") {
		t.Fatalf("config/up file scoping mismatch: %#v %#v", config.Args, up.Args)
	}
	wantEnvironment := scopedCommandEnvironment("npipe:////./pipe/docker_engine")
	if !reflect.DeepEqual(config.Env, wantEnvironment) || !reflect.DeepEqual(up.Env, wantEnvironment) || !containsEnvironmentValue(config.Env, "DOCKER_HOST", "npipe:////./pipe/docker_engine") {
		t.Fatalf("scoped environment config=%v up=%v want=%v", config.Env, up.Env, wantEnvironment)
	}
	for _, request := range requests {
		if request.Executable == "git" || containsArgument(request.Args, "down") || containsArgument(request.Args, "rollback") {
			t.Fatalf("forbidden rollback/source command: %#v", request)
		}
	}
}

func assertProgressPhases(t *testing.T, updates []jobs.ProgressUpdate, want []string) {
	t.Helper()
	got := make([]string, len(updates))
	for index := range updates {
		got[index] = updates[index].Phase
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phases=%v want=%v", got, want)
	}
}

func assertCleared(t *testing.T, values ...[]byte) {
	t.Helper()
	for _, value := range values {
		for _, character := range value {
			if character != 0 {
				t.Fatalf("secret-bearing output was not cleared: %q", value)
			}
		}
	}
}

func assertRuntimeTempEmpty(t *testing.T, dataRoot string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "runtime", "compose"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime temp entries=%v err=%v", entries, err)
	}
}

func valueAfter(arguments []string, flag string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == flag {
			return arguments[index+1]
		}
	}
	return ""
}

func containsEnvironmentKey(environment []string, key string) bool {
	for _, value := range environment {
		if strings.HasPrefix(value, key+"=") {
			return true
		}
	}
	return false
}

func containsEnvironmentValue(environment []string, key, want string) bool {
	for _, value := range environment {
		if value == key+"="+want {
			return true
		}
	}
	return false
}

func containsArgument(arguments []string, want string) bool {
	for _, value := range arguments {
		if value == want {
			return true
		}
	}
	return false
}

func hasFindingDisposition(findings []deployments.Finding, disposition string) bool {
	for _, finding := range findings {
		if finding.Disposition == disposition {
			return true
		}
	}
	return false
}

func last(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func nonzeroRevisionRequests(request revisionRequest) []revisionRequest {
	if request.revisionNumber == 0 {
		return nil
	}
	return []revisionRequest{request}
}
