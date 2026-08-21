package composeruntime

import (
	"context"
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
	"github.com/hostd/hostd/internal/apps"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/releasesnapshot"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
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
}

func (f *fakeReleases) Materialize(_ context.Context, owner, appID string) (releasesnapshot.Release, error) {
	f.materializeCalls++
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
	return value
}

type fakeDeployments struct {
	deployment deployments.Deployment
	findings   []deployments.Finding
	gateErr    error
	actions    []string
}

func (f *fakeDeployments) GetOrCreateByJob(_ context.Context, appID, jobID, mode string) (deployments.Deployment, bool, error) {
	if f.deployment.ID == "" {
		f.deployment = deployments.Deployment{
			ID:                uuid.NewString(),
			AppID:             appID,
			JobID:             jobID,
			ConfigurationMode: mode,
			Status:            deployments.Preparing,
		}
		f.actions = append(f.actions, "create")
		return f.deployment, true, nil
	}
	return f.deployment, false, nil
}

func (f *fakeDeployments) Get(context.Context, string, string) (deployments.Deployment, error) {
	return f.deployment, nil
}

func (f *fakeDeployments) Initialize(_ context.Context, _, _, releaseID, revisionID string, revisionNumber int64) (deployments.Deployment, error) {
	if f.deployment.ProvenanceInitialized {
		return deployments.Deployment{}, deployments.ErrInvalidTransition
	}
	f.deployment.ReleaseID = releaseID
	f.deployment.ActualConfigurationRevisionID = revisionID
	f.deployment.ActualConfigurationRevisionNumber = revisionNumber
	f.deployment.ProvenanceInitialized = true
	f.actions = append(f.actions, "initialize")
	return f.deployment, nil
}

func (f *fakeDeployments) Gate(_ context.Context, _, _ string, findings []deployments.Finding) error {
	f.findings = append([]deployments.Finding(nil), findings...)
	f.actions = append(f.actions, "gate")
	if errors.Is(f.gateErr, deployments.ErrApprovalRequired) {
		f.deployment.Status = deployments.NeedsAttention
	} else if errors.Is(f.gateErr, deployments.ErrRejectedCapability) {
		f.deployment.Status = deployments.Failed
	} else if f.gateErr == nil {
		f.deployment.Status = deployments.Applying
	}
	return f.gateErr
}

func (f *fakeDeployments) Transition(_ context.Context, _, _ string, status deployments.Status, diagnostic string) (deployments.Deployment, error) {
	if f.deployment.Status == deployments.Failed || f.deployment.Status == deployments.Cancelled || f.deployment.Status == deployments.Succeeded {
		return deployments.Deployment{}, deployments.ErrInvalidTransition
	}
	f.deployment.Status = status
	f.deployment.DiagnosticCode = diagnostic
	f.actions = append(f.actions, "transition:"+string(status)+":"+diagnostic)
	return f.deployment, nil
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
}

func (r *recordingReporter) Report(update jobs.ProgressUpdate) error {
	r.updates = append(r.updates, update)
	if update.Phase == r.errAt {
		return errors.New("report failed")
	}
	return nil
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
		if last(fixture.deployments.actions) != "gate" {
			t.Fatalf("gate was not final DB action before up: %v", fixture.deployments.actions)
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

	result, err := fixture.executor.Execute(context.Background(), fixture.job, &fixture.reporter)
	if err != nil || result.CompletionCode != "deployment_completed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if fixture.deployments.deployment.Status != deployments.Succeeded || fixture.deployments.deployment.ReleaseID != fixture.releases.release.ID || fixture.deployments.deployment.ActualConfigurationRevisionID != fixture.configuration.current.RevisionID {
		t.Fatalf("deployment=%#v", fixture.deployments.deployment)
	}
	if fixture.configuration.currentCalls != 1 || len(fixture.configuration.revisionRequest) != 0 {
		t.Fatalf("configuration calls current=%d exact=%v", fixture.configuration.currentCalls, fixture.configuration.revisionRequest)
	}
	assertExactExecutorRequests(t, fixture, fixture.runner.requests)
	assertProgressPhases(t, fixture.reporter.updates, []string{"validate", "prepare_workspace", "materialize_release", "render_compose", "evaluate_policy", "apply_runtime", "wait_for_health", "finalize"})
	assertCleared(t, configStdout, configStderr, upStdout, upStderr)
	assertRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestExecutorFailureAndPauseTaxonomyCleansProtectedFiles(t *testing.T) {
	tests := []struct {
		name            string
		configResult    runtimeprocess.CommandResult
		configError     error
		upResult        runtimeprocess.CommandResult
		upError         error
		gateError       error
		cleanupError    bool
		cancelRequested bool
		wantCode        string
		wantStatus      deployments.Status
		wantPause       bool
		wantRunnerRuns  int
		wantDisposition string
	}{
		{name: "config truncated", configResult: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{}}}`), StdoutTruncated: true}, wantCode: "compose_invalid", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "config stderr truncated", configResult: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{}}}`), StderrTruncated: true}, wantCode: "compose_invalid", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "config empty", configResult: runtimeprocess.CommandResult{}, wantCode: "compose_invalid", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "config nonzero", configError: errors.New("exit status 1"), wantCode: "compose_invalid", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "config timeout", configError: context.DeadlineExceeded, wantCode: "compose_invalid", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "config cancellation", configError: context.Canceled, cancelRequested: true, wantCode: "cancelled", wantStatus: deployments.Cancelled, wantRunnerRuns: 1},
		{name: "malformed effective config", configResult: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{}}`)}, wantCode: "compose_invalid", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "approval pause", configResult: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{"privileged":true}}}`)}, gateError: deployments.ErrApprovalRequired, wantStatus: deployments.NeedsAttention, wantPause: true, wantRunnerRuns: 1, wantDisposition: DispositionApprovalRequired},
		{name: "policy rejection", configResult: runtimeprocess.CommandResult{Stdout: []byte(`{"services":{"web":{"build":"https://example.invalid/repository.git"}}}`)}, gateError: deployments.ErrRejectedCapability, wantCode: "policy_rejected", wantStatus: deployments.Failed, wantRunnerRuns: 1, wantDisposition: DispositionRejected},
		{name: "up nonzero is apply failure", configResult: validModel(), upError: errors.New("exit status 1"), wantCode: "apply_failed", wantStatus: deployments.Failed, wantRunnerRuns: 2},
		{name: "up stdout truncated", configResult: validModel(), upResult: runtimeprocess.CommandResult{StdoutTruncated: true}, wantCode: "apply_failed", wantStatus: deployments.Failed, wantRunnerRuns: 2},
		{name: "up stderr truncated", configResult: validModel(), upResult: runtimeprocess.CommandResult{StderrTruncated: true}, wantCode: "apply_failed", wantStatus: deployments.Failed, wantRunnerRuns: 2},
		{name: "outer timeout is health failure", configResult: validModel(), upError: context.DeadlineExceeded, wantCode: "health_failed", wantStatus: deployments.Failed, wantRunnerRuns: 2},
		{name: "up cancellation", configResult: validModel(), upError: context.Canceled, cancelRequested: true, wantCode: "cancelled", wantStatus: deployments.Cancelled, wantRunnerRuns: 2},
		{name: "cleanup failure overrides compose failure", configError: errors.New("exit status 1"), cleanupError: true, wantCode: "internal_error", wantStatus: deployments.Failed, wantRunnerRuns: 1},
		{name: "cleanup failure overrides success", configResult: validModel(), cleanupError: true, wantCode: "internal_error", wantStatus: deployments.Failed, wantRunnerRuns: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			fixture.deployments.gateErr = test.gateError
			fixture.runner.run = func(index int, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				if index == 0 {
					return test.configResult, test.configError
				}
				return test.upResult, test.upError
			}
			if test.cleanupError {
				originalCleanup := fixture.executor.cleanup
				fixture.executor.cleanup = func(files *securetemp.Files) error {
					_ = originalCleanup(files)
					return errors.New("cleanup failed")
				}
			}
			executionContext := context.Background()
			if test.cancelRequested {
				cancelledContext, cancel := context.WithCancelCause(executionContext)
				cancel(jobs.ErrCancellationRequested)
				executionContext = cancelledContext
			}
			result, err := fixture.executor.Execute(executionContext, fixture.job, &fixture.reporter)
			if test.wantPause {
				if err != nil || result.Disposition != jobs.ExecutionWaitingUser || result.PauseDisposition != "approval_required" {
					t.Fatalf("result=%#v err=%v", result, err)
				}
			} else {
				var executionErr *jobs.ExecutionError
				if !errors.As(err, &executionErr) || executionErr.Code != test.wantCode {
					t.Fatalf("error=%v want code=%q", err, test.wantCode)
				}
			}
			if fixture.deployments.deployment.Status != test.wantStatus {
				t.Fatalf("deployment=%#v", fixture.deployments.deployment)
			}
			if len(fixture.runner.requests) != test.wantRunnerRuns {
				t.Fatalf("runner calls=%d want=%d", len(fixture.runner.requests), test.wantRunnerRuns)
			}
			for _, request := range fixture.runner.requests {
				if request.Executable == "git" || containsArgument(request.Args, "down") || containsArgument(request.Args, "rollback") {
					t.Fatalf("failure triggered forbidden rollback/source command: %#v", request)
				}
			}
			if test.gateError != nil && len(fixture.deployments.findings) == 0 {
				t.Fatal("policy gate received no findings")
			}
			if test.wantDisposition != "" && !hasFindingDisposition(fixture.deployments.findings, test.wantDisposition) {
				t.Fatalf("findings=%#v want disposition=%q", fixture.deployments.findings, test.wantDisposition)
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestExecutorParentShutdownLeavesDeploymentForRecovery(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	fixture.runner.run = func(_ int, _ runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		return runtimeprocess.CommandResult{}, context.Canceled
	}
	executionContext, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.executor.Execute(executionContext, fixture.job, &fixture.reporter)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error=%v", err)
	}
	if fixture.deployments.deployment.Status != deployments.Preparing || len(fixture.runner.requests) != 1 {
		t.Fatalf("shutdown deployment=%#v requests=%v", fixture.deployments.deployment, fixture.runner.requests)
	}
	assertRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestExecutorPriorReleaseConfigurationModesAndResumePin(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         jobs.ConfigurationMode
		initialized  bool
		wantCurrent  int
		wantRevision revisionRequest
	}{
		{name: "prior original", mode: jobs.ConfigurationOriginal, wantRevision: revisionRequest{revisionID: "release-revision", revisionNumber: 4}},
		{name: "prior current", mode: jobs.ConfigurationCurrent, wantCurrent: 1},
		{name: "resume uses pinned actual", mode: jobs.ConfigurationCurrent, initialized: true, wantRevision: revisionRequest{revisionID: "pinned-revision", revisionNumber: 7}},
	} {
		t.Run(test.name, func(t *testing.T) {
			releaseID := strings.Repeat("a", 32)
			fixture := newExecutorFixture(t, test.mode, releaseID)
			fixture.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
			fixture.releases.release = releasesnapshot.Release{
				ID:                          releaseID,
				AppID:                       fixture.job.ResourceID,
				WorkspacePath:               fixture.workspace,
				ComposePath:                 "compose.yaml",
				WorkspaceState:              releasesnapshot.WorkspaceStateReady,
				ConfigurationRevisionID:     "release-revision",
				ConfigurationRevisionNumber: 4,
			}
			fixture.configuration.exact = appconfig.ExecutionConfiguration{RevisionID: test.wantRevision.revisionID, RevisionNumber: test.wantRevision.revisionNumber, Environment: []byte("TOKEN='exact'\n")}
			if test.initialized {
				fixture.deployments.deployment = deployments.Deployment{
					ID:                                uuid.NewString(),
					AppID:                             fixture.job.ResourceID,
					JobID:                             fixture.job.ID,
					Status:                            deployments.NeedsAttention,
					ConfigurationMode:                 string(test.mode),
					ReleaseID:                         releaseID,
					ActualConfigurationRevisionID:     "pinned-revision",
					ActualConfigurationRevisionNumber: 7,
					ProvenanceInitialized:             true,
				}
			}
			fixture.runner.run = successfulRunner(validModel())
			if _, err := fixture.executor.Execute(context.Background(), fixture.job, &fixture.reporter); err != nil {
				t.Fatal(err)
			}
			if fixture.configuration.currentCalls != test.wantCurrent || !reflect.DeepEqual(fixture.configuration.revisionRequest, nonzeroRevisionRequests(test.wantRevision)) {
				t.Fatalf("current=%d exact=%v", fixture.configuration.currentCalls, fixture.configuration.revisionRequest)
			}
			if fixture.releases.readyCalls != 1 || fixture.releases.materializeCalls != 0 {
				t.Fatalf("release calls ready=%d materialize=%d", fixture.releases.readyCalls, fixture.releases.materializeCalls)
			}
			if test.initialized && (containsArgument(fixture.deployments.actions, "initialize") || fixture.deployments.deployment.ActualConfigurationRevisionID != "pinned-revision" || fixture.deployments.deployment.ActualConfigurationRevisionNumber != 7) {
				t.Fatalf("resume changed initialized provenance: %#v actions=%v", fixture.deployments.deployment, fixture.deployments.actions)
			}
			if !test.initialized && test.mode == jobs.ConfigurationOriginal && (fixture.deployments.deployment.ActualConfigurationRevisionID != "release-revision" || fixture.deployments.deployment.ActualConfigurationRevisionNumber != 4) {
				t.Fatalf("original mode actual configuration=%#v", fixture.deployments.deployment)
			}
			if !test.initialized && test.mode == jobs.ConfigurationCurrent && fixture.deployments.deployment.ActualConfigurationRevisionID != fixture.configuration.current.RevisionID {
				t.Fatalf("current mode actual configuration=%#v", fixture.deployments.deployment)
			}
		})
	}
}

func TestExecutorLatestGitHubUsesRequestedActorAndCurrentConfiguration(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	fixture.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
	fixture.releases.release = releasesnapshot.Release{
		ID:                          strings.Repeat("a", 32),
		AppID:                       fixture.job.ResourceID,
		WorkspacePath:               fixture.workspace,
		ComposePath:                 "compose.yaml",
		WorkspaceState:              releasesnapshot.WorkspaceStateReady,
		ConfigurationRevisionID:     "original-release-revision",
		ConfigurationRevisionNumber: 3,
	}
	fixture.runner.run = successfulRunner(validModel())
	if _, err := fixture.executor.Execute(context.Background(), fixture.job, &fixture.reporter); err != nil {
		t.Fatal(err)
	}
	if fixture.releases.materializeCalls != 1 || fixture.releases.readyCalls != 0 || fixture.releases.materializeOwner != fixture.job.RequestedBy || fixture.releases.materializeApp != fixture.job.ResourceID {
		t.Fatalf("release calls materialize=%d ready=%d owner=%q app=%q", fixture.releases.materializeCalls, fixture.releases.readyCalls, fixture.releases.materializeOwner, fixture.releases.materializeApp)
	}
	if fixture.configuration.currentCalls != 1 || len(fixture.configuration.revisionRequest) != 0 {
		t.Fatalf("configuration calls current=%d exact=%v", fixture.configuration.currentCalls, fixture.configuration.revisionRequest)
	}
	if fixture.deployments.deployment.ReleaseID != fixture.releases.release.ID || fixture.deployments.deployment.ActualConfigurationRevisionID != fixture.configuration.current.RevisionID {
		t.Fatalf("initialized provenance=%#v", fixture.deployments.deployment)
	}
}

func TestExecutorRejectsInvalidRequestedActorBeforeSideEffects(t *testing.T) {
	for _, actor := range []string{"", "owner", "not-a-uuid"} {
		t.Run(actor, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			fixture.job.RequestedBy = actor
			result, err := fixture.executor.Execute(context.Background(), fixture.job, &fixture.reporter)
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != "validation_failed" || result != (jobs.ExecutionResult{}) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if len(fixture.deployments.actions) != 0 || len(fixture.runner.requests) != 0 || fixture.configuration.currentCalls != 0 || fixture.releases.materializeCalls != 0 {
				t.Fatalf("invalid actor caused side effects: actions=%v requests=%v", fixture.deployments.actions, fixture.runner.requests)
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestExecutorMapsSourceAndConfigurationDiagnostics(t *testing.T) {
	tests := []struct {
		name            string
		prepare         func(*executorFixture)
		wantCode        string
		wantStatus      deployments.Status
		cancelRequested bool
	}{
		{name: "application unavailable", prepare: func(f *executorFixture) { f.applications.err = errors.New("database unavailable") }, wantCode: "invalid_source", wantStatus: deployments.Failed},
		{name: "provider unavailable", prepare: githubFailure(&releasesnapshot.Error{Code: "provider_unavailable"}), wantCode: "provider_unavailable", wantStatus: deployments.Failed},
		{name: "source access lost", prepare: githubFailure(&releasesnapshot.Error{Code: "source_access_lost"}), wantCode: "source_access_lost", wantStatus: deployments.Failed},
		{name: "source too large", prepare: githubFailure(&releasesnapshot.Error{Code: "source_too_large"}), wantCode: "source_too_large", wantStatus: deployments.Failed},
		{name: "provider cancellation", prepare: githubFailure(&releasesnapshot.Error{Code: "canceled"}), wantCode: "cancelled", wantStatus: deployments.Cancelled, cancelRequested: true},
		{name: "current configuration unavailable", prepare: func(f *executorFixture) { f.configuration.currentErr = errors.New("corrupt bundle") }, wantCode: "configuration_unavailable", wantStatus: deployments.Failed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			test.prepare(&fixture)
			executionContext := context.Background()
			if test.cancelRequested {
				cancelledContext, cancel := context.WithCancelCause(executionContext)
				cancel(jobs.ErrCancellationRequested)
				executionContext = cancelledContext
			}
			_, err := fixture.executor.Execute(executionContext, fixture.job, &fixture.reporter)
			var executionErr *jobs.ExecutionError
			if !errors.As(err, &executionErr) || executionErr.Code != test.wantCode {
				t.Fatalf("error=%v want code=%q", err, test.wantCode)
			}
			if fixture.deployments.deployment.Status != test.wantStatus || len(fixture.runner.requests) != 0 {
				t.Fatalf("deployment=%#v requests=%v", fixture.deployments.deployment, fixture.runner.requests)
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func githubFailure(err error) func(*executorFixture) {
	return func(fixture *executorFixture) {
		fixture.applications.application.Source = apps.Source{Type: apps.SourceGitHub}
		fixture.releases.materializeErr = err
	}
}

func TestExecutorLocalDirectoryDirectFileAndAmbiguousSource(t *testing.T) {
	for _, test := range []struct {
		name      string
		makePath  func(*testing.T) (string, string, string)
		wantError bool
	}{
		{name: "directory", makePath: func(t *testing.T) (string, string, string) {
			root := t.TempDir()
			nested := filepath.Join(root, "deploy")
			if err := os.Mkdir(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nested, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return root, root, "deploy/compose.yaml"
		}},
		{name: "direct file", makePath: func(t *testing.T) (string, string, string) {
			root := t.TempDir()
			compose := filepath.Join(root, "compose.yaml")
			if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return compose, root, "compose.yaml"
		}},
		{name: "ambiguous directory", wantError: true, makePath: func(t *testing.T) (string, string, string) {
			root := t.TempDir()
			for _, name := range []string{"compose.yaml", "compose.yml"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return root, "", ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			sourcePath, _, wantCompose := test.makePath(t)
			fixture.applications.application.Source = apps.Source{Type: apps.SourceLocal, Path: sourcePath}
			managedWorkspace := t.TempDir()
			if test.wantError {
				fixture.releases.materializeErr = &releasesnapshot.Error{Code: "invalid_source"}
			} else {
				managedCompose := filepath.Join(managedWorkspace, filepath.FromSlash(wantCompose))
				if err := os.MkdirAll(filepath.Dir(managedCompose), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(managedCompose, []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				canonicalManaged, err := canonicalPath(managedWorkspace)
				if err != nil {
					t.Fatal(err)
				}
				fixture.releases.release.WorkspacePath = canonicalManaged
				fixture.releases.release.ComposePath = wantCompose
			}
			fixture.runner.run = successfulRunner(validModel())
			_, err := fixture.executor.Execute(context.Background(), fixture.job, &fixture.reporter)
			if test.wantError {
				var executionErr *jobs.ExecutionError
				if !errors.As(err, &executionErr) || executionErr.Code != "invalid_source" || len(fixture.runner.requests) != 0 || fixture.deployments.deployment.Status != deployments.Failed {
					t.Fatalf("error=%v requests=%v deployment=%#v", err, fixture.runner.requests, fixture.deployments.deployment)
				}
				return
			}
			if err != nil || len(fixture.runner.requests) != 2 {
				t.Fatalf("error=%v requests=%v", err, fixture.runner.requests)
			}
			canonicalWorkspace, canonicalErr := canonicalPath(managedWorkspace)
			if canonicalErr != nil {
				t.Fatal(canonicalErr)
			}
			config := fixture.runner.requests[0]
			if config.Directory != canonicalWorkspace || valueAfter(config.Args, "--project-directory") != canonicalWorkspace || valueAfter(config.Args, "-f") != filepath.Join(canonicalWorkspace, filepath.FromSlash(wantCompose)) {
				t.Fatalf("local config request=%#v", config)
			}
		})
	}
}

func TestExecutorManagedWorkspaceBindIsReadOnlyForGitHubAndLocal(t *testing.T) {
	for _, sourceType := range []string{apps.SourceGitHub, apps.SourceLocal} {
		for _, readOnly := range []bool{false, true} {
			name := sourceType + "/writable"
			if readOnly {
				name = sourceType + "/read-only"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
				fixture.applications.application.Source.Type = sourceType
				fixture.releases.release.SourceProvider = sourceType
				body, err := json.Marshal(serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "bind", "source": fixture.workspace, "target": "/app", "read_only": readOnly}},
				}))
				if err != nil {
					t.Fatal(err)
				}
				fixture.runner.run = successfulRunner(runtimeprocess.CommandResult{Stdout: body})
				if !readOnly {
					fixture.deployments.gateErr = deployments.ErrRejectedCapability
				}
				_, executeErr := fixture.executor.Execute(context.Background(), fixture.job, &fixture.reporter)
				wantCapability, wantDisposition := "workspace_bind_mount", DispositionAllowed
				if !readOnly {
					wantCapability, wantDisposition = "writable_managed_bind", DispositionRejected
					var executionErr *jobs.ExecutionError
					if !errors.As(executeErr, &executionErr) || executionErr.Code != "policy_rejected" || len(fixture.runner.requests) != 1 {
						t.Fatalf("execute error=%v requests=%d", executeErr, len(fixture.runner.requests))
					}
				} else if executeErr != nil || len(fixture.runner.requests) != 2 {
					t.Fatalf("execute error=%v requests=%d", executeErr, len(fixture.runner.requests))
				}
				if len(fixture.deployments.findings) != 1 || fixture.deployments.findings[0].Capability != wantCapability || fixture.deployments.findings[0].Disposition != wantDisposition {
					t.Fatalf("findings=%#v", fixture.deployments.findings)
				}
			})
		}
	}
}

type executorFixture struct {
	executor      *Executor
	applications  *fakeApplications
	releases      *fakeReleases
	configuration *fakeConfiguration
	deployments   *fakeDeployments
	runner        *fakeRunner
	reporter      recordingReporter
	job           jobs.Job
	workspace     string
	dataRoot      string
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
	deploymentRepository := &fakeDeployments{}
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
	return executorFixture{
		executor:      executor,
		applications:  applications,
		releases:      releases,
		configuration: configuration,
		deployments:   deploymentRepository,
		runner:        runner,
		job: jobs.Job{
			ID:           uuid.NewString(),
			Type:         "deploy",
			ResourceType: "application",
			ResourceID:   applicationID,
			RequestedBy:  uuid.NewString(),
			Input:        input,
			Attempt:      1,
		},
		workspace: workspace,
		dataRoot:  dataRoot,
	}
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
