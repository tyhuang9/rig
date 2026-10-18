package composeruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/jobs"
	"github.com/hostd/hostd/internal/runtime/deploymenteffects"
	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
	"github.com/hostd/hostd/internal/runtime/securetemp"
)

func prepareComposeApply(t *testing.T, fixture *executorFixture, ctx context.Context, findings []deployments.Finding) inspectedComposeInvocation {
	t.Helper()
	deployment, _, err := fixture.deployments.GetOrCreateByJob(context.Background(), fixture.job.ResourceID, fixture.job.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = fixture.deployments.Initialize(context.Background(), fixture.job.ResourceID, deployment.ID, fixture.releases.release.ID, fixture.configuration.current.RevisionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	gateRequest := deployments.GateRequest{Claim: fixture.claim, DeploymentID: deployment.ID, Findings: findings}
	if err := fixture.deployments.Gate(ctx, gateRequest); errors.Is(err, deployments.ErrApprovalRequired) {
		for _, finding := range findings {
			if finding.Disposition == DispositionApprovalRequired {
				if _, _, grantErr := fixture.deployments.Grant(context.Background(), fixture.job.ResourceID, fixture.job.RequestedBy, finding.Fingerprint); grantErr != nil {
					t.Fatal(grantErr)
				}
			}
		}
		if err := fixture.deployments.Gate(ctx, gateRequest); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	files, err := fixture.executor.temporary.Create(fixture.job.ID, fixture.job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteEnv([]byte("TOKEN='runtime-secret'\n")); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCompose([]byte(`{"services":{"web":{"image":"nginx"}}}`)); err != nil {
		t.Fatal(err)
	}
	project, err := projectName(fixture.job.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return inspectedComposeInvocation{
		claim: fixture.claim, deploymentID: deployment.ID, releaseID: fixture.releases.release.ID,
		configurationRevisionID: fixture.configuration.current.RevisionID, configurationRevisionNumber: 1,
		findings: append([]deployments.Finding(nil), findings...), workspace: fixture.workspace, project: project,
		files: files, environment: scopedCommandEnvironment(fixture.executor.options.DockerEndpoint),
	}
}

func TestComposeApplyAdapterRefusesWaitingHealthAndIdentityDriftBeforeUp(t *testing.T) {
	for _, test := range []struct {
		name     string
		findings []deployments.Finding
		mutate   func(*testing.T, *executorFixture, inspectedComposeInvocation)
		want     error
	}{
		{name: "waiting health cannot rerun", want: deploymentexecution.ErrStageInvalid, mutate: func(t *testing.T, f *executorFixture, invocation inspectedComposeInvocation) {
			if _, err := f.deployments.Transition(context.Background(), f.job.ResourceID, invocation.deploymentID, deployments.WaitingHealth, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "attempt advance", want: deploymentexecution.ErrIdentityLost, mutate: func(t *testing.T, f *executorFixture, _ inspectedComposeInvocation) {
			if _, err := f.db.Exec(`UPDATE jobs SET attempt=2 WHERE id=?`, f.job.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "approval revoked after Gate", want: deployments.ErrApprovalRequired, findings: []deployments.Finding{{
			PolicyVersion: PolicyVersion, Capability: "privileged", Scope: `{"service":"web"}`,
			Fingerprint: PolicyFingerprint(PolicyVersion, "privileged", `{"service":"web"}`), Disposition: DispositionApprovalRequired,
		}}, mutate: func(t *testing.T, f *executorFixture, _ inspectedComposeInvocation) {
			if _, err := f.db.Exec(`DROP TRIGGER runtime_approval_revoke_active`); err != nil {
				t.Fatal(err)
			}
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := f.db.Exec(`UPDATE runtime_approvals SET revoked_by=?,revoked_at=? WHERE app_id=?`, f.job.RequestedBy, stamp, f.job.ResourceID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			invocation := prepareComposeApply(t, &fixture, fixture.context, test.findings)
			test.mutate(t, &fixture, invocation)
			before := fixture.deployment(t)
			fixture.runner.run = func(int, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				return runtimeprocess.CommandResult{}, nil
			}
			_, err := fixture.executor.apply.RunUp(fixture.context, invocation, &fixture.reporter)
			if !errors.Is(err, test.want) || len(fixture.runner.requests) != 0 {
				t.Fatalf("error=%v requests=%#v", err, fixture.runner.requests)
			}
			if after := fixture.deployment(t); !reflect.DeepEqual(before, after) {
				t.Fatalf("pre-up refusal changed deployment: before=%#v after=%#v", before, after)
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestComposeApplyAdapterCancellationAfterGateRefusesBeforeUp(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	ctx, cancel := context.WithCancel(fixture.context)
	invocation := prepareComposeApply(t, &fixture, ctx, nil)
	cancel()
	fixture.runner.run = func(int, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		t.Fatal("runner entered after final-check cancellation")
		return runtimeprocess.CommandResult{}, nil
	}
	_, err := fixture.executor.apply.RunUp(ctx, invocation, &fixture.reporter)
	if !errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled) || len(fixture.runner.requests) != 0 {
		t.Fatalf("error=%v requests=%#v", err, fixture.runner.requests)
	}
	if got := fixture.deployment(t).Status; got != deployments.Applying {
		t.Fatalf("status=%s", got)
	}
	assertRuntimeTempEmpty(t, fixture.dataRoot)
}

func TestComposeApplyAdapterFreshObservationsRefuseDriftWithoutErasingAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name       string
		driftAt    string
		wantStatus deployments.Status
	}{
		{name: "runner drift blocks waiting-health observation", driftAt: "runner", wantStatus: deployments.Applying},
		{name: "report drift blocks succeeded observation", driftAt: "report", wantStatus: deployments.WaitingHealth},
		{name: "cleanup drift blocks succeeded observation", driftAt: "cleanup", wantStatus: deployments.WaitingHealth},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			invocation := prepareComposeApply(t, &fixture, fixture.context, nil)
			drift := func() {
				if _, err := fixture.db.Exec(`UPDATE jobs SET attempt=2 WHERE id=?`, fixture.job.ID); err != nil {
					t.Fatal(err)
				}
			}
			fixture.runner.run = func(int, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				if test.driftAt == "runner" {
					drift()
				}
				return runtimeprocess.CommandResult{}, nil
			}
			if test.driftAt == "report" {
				fixture.reporter.before = func(update jobs.ProgressUpdate) {
					if update.Phase == "finalize" {
						drift()
					}
				}
			}
			if test.driftAt == "cleanup" {
				original := fixture.executor.cleanup
				fixture.executor.cleanup = func(files *securetemp.Files) error { drift(); return original(files) }
			}
			_, err := fixture.executor.apply.RunUp(fixture.context, invocation, &fixture.reporter)
			if !errors.Is(err, deploymentexecution.ErrIdentityLost) {
				t.Fatalf("identity cause missing: %v", err)
			}
			if got := fixture.deployment(t).Status; got != test.wantStatus {
				t.Fatalf("status=%s want=%s", got, test.wantStatus)
			}
			if len(fixture.runner.requests) != 1 {
				t.Fatalf("up calls=%d", len(fixture.runner.requests))
			}
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestComposeApplyAdapterRetainsDurableStateForCommandAndFinalizeOutcomes(t *testing.T) {
	runnerFailure := errors.New("runner failure")
	reportFailure := errors.New("report failure")
	cleanupFailure := errors.New("cleanup failure")
	for _, test := range []struct {
		name        string
		result      runtimeprocess.CommandResult
		runErr      error
		cancelAt    string
		reportFail  bool
		cleanupFail bool
		wantCode    string
		wantStatus  deployments.Status
		wantSuccess bool
		wantCause   error
		wantCancel  bool
	}{
		{name: "known nonzero", runErr: errors.New("exit status 1"), wantCode: "compose_apply_failed", wantStatus: deployments.Applying},
		{name: "truncated acknowledgement", result: runtimeprocess.CommandResult{StdoutTruncated: true}, wantCode: "compose_apply_output_truncated", wantStatus: deployments.Applying},
		{name: "cancel after runner entry", cancelAt: "runner", wantCode: "cancelled", wantStatus: deployments.Applying, wantCancel: true},
		{name: "runner error and cancellation preserve both causes", cancelAt: "runner", runErr: runnerFailure, wantCode: "compose_apply_failed", wantStatus: deployments.Applying, wantCause: runnerFailure, wantCancel: true},
		{name: "final report failure", reportFail: true, wantCode: "internal_error", wantStatus: deployments.WaitingHealth},
		{name: "final report failure and cancellation preserve both causes", cancelAt: "report", reportFail: true, wantCode: "internal_error", wantStatus: deployments.WaitingHealth, wantCause: reportFailure, wantCancel: true},
		{name: "cleanup failure", cleanupFail: true, wantCode: "internal_error", wantStatus: deployments.WaitingHealth},
		{name: "cleanup failure and cancellation preserve both causes", cancelAt: "cleanup", cleanupFail: true, wantCode: "internal_error", wantStatus: deployments.WaitingHealth, wantCause: cleanupFailure, wantCancel: true},
		{name: "success", wantStatus: deployments.Succeeded, wantSuccess: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
			ctx := fixture.context
			var cancel context.CancelCauseFunc
			if test.cancelAt != "" {
				ctx, cancel = context.WithCancelCause(fixture.context)
			}
			invocation := prepareComposeApply(t, &fixture, ctx, nil)
			stdout, stderr := []byte("stdout-secret"), []byte("stderr-secret")
			fixture.runner.run = func(_ int, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
				var independent string
				if err := fixture.db.QueryRow(`SELECT status FROM deployments WHERE id=?`, invocation.deploymentID).Scan(&independent); err != nil || independent != string(deployments.Applying) {
					t.Fatalf("independent DB read status=%q err=%v", independent, err)
				}
				if test.cancelAt == "runner" {
					cancel(jobs.ErrCancellationRequested)
				}
				result := test.result
				result.Stdout, result.Stderr = stdout, stderr
				return result, test.runErr
			}
			if test.reportFail {
				fixture.reporter.errAt = "finalize"
				if test.cancelAt == "report" {
					fixture.reporter.err = reportFailure
					fixture.reporter.before = func(update jobs.ProgressUpdate) {
						if update.Phase == "finalize" {
							cancel(jobs.ErrCancellationRequested)
						}
					}
				}
			}
			if test.cleanupFail {
				original := fixture.executor.cleanup
				fixture.executor.cleanup = func(files *securetemp.Files) error {
					_ = original(files)
					if test.cancelAt == "cleanup" {
						cancel(jobs.ErrCancellationRequested)
						return cleanupFailure
					}
					return errors.New("cleanup failed")
				}
			}
			result, err := fixture.executor.apply.RunUp(ctx, invocation, &fixture.reporter)
			if test.wantSuccess {
				if err != nil || result.CompletionCode != "deployment_completed" {
					t.Fatalf("result=%#v err=%v", result, err)
				}
			} else {
				var executionErr *jobs.ExecutionError
				if !errors.As(err, &executionErr) || executionErr.Code != test.wantCode {
					t.Fatalf("error=%v want=%s", err, test.wantCode)
				}
				if test.wantCause != nil && !errors.Is(err, test.wantCause) {
					t.Fatalf("original cause missing from %v", err)
				}
				if test.wantCancel && (!errors.Is(err, deploymentexecution.ErrCanceled) || !errors.Is(err, context.Canceled)) {
					t.Fatalf("cancellation causes missing from %v", err)
				}
			}
			if got := fixture.deployment(t).Status; got != test.wantStatus {
				t.Fatalf("status=%s want=%s", got, test.wantStatus)
			}
			if len(fixture.runner.requests) != 1 {
				t.Fatalf("up calls=%d", len(fixture.runner.requests))
			}
			request := fixture.runner.requests[0]
			wantArgs := []string{"compose", "--project-name", invocation.project, "--project-directory", invocation.workspace, "--env-file", invocation.files.EnvPath, "-f", invocation.files.ComposePath, "up", "-d", "--wait", "--wait-timeout", "10"}
			if !reflect.DeepEqual(request.Args, wantArgs) {
				t.Fatalf("args=%v want=%v", request.Args, wantArgs)
			}
			assertCleared(t, stdout, stderr)
			assertRuntimeTempEmpty(t, fixture.dataRoot)
		})
	}
}

func TestComposeApplyAdapterUsesOnlyProtectedEffectiveFile(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	invocation := prepareComposeApply(t, &fixture, fixture.context, nil)
	fixture.runner.run = func(_ int, request runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		if valueAfter(request.Args, "-f") != invocation.files.ComposePath || valueAfter(request.Args, "-f") == filepath.Join(fixture.workspace, "compose.yaml") {
			t.Fatalf("compose input=%#v", request.Args)
		}
		if _, err := os.ReadFile(invocation.files.ComposePath); err != nil {
			t.Fatal(err)
		}
		return runtimeprocess.CommandResult{}, nil
	}
	if _, err := fixture.executor.apply.RunUp(fixture.context, invocation, &fixture.reporter); err != nil {
		t.Fatal(err)
	}
}

func TestComposeApplyAdapterKeepsOriginalScopePinnedWhileRunnerBlocks(t *testing.T) {
	fixture := newExecutorFixture(t, jobs.ConfigurationCurrent, "")
	invocation := prepareComposeApply(t, &fixture, fixture.context, nil)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	unblockRunner := func() { unblockOnce.Do(func() { close(unblock) }) }
	defer unblockRunner()
	fixture.runner.run = func(int, runtimeprocess.CommandRequest) (runtimeprocess.CommandResult, error) {
		close(entered)
		<-unblock
		return runtimeprocess.CommandResult{}, nil
	}
	runDone := make(chan error, 1)
	go func() {
		_, err := fixture.executor.apply.RunUp(fixture.context, invocation, &fixture.reporter)
		runDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Compose runner did not enter")
	}
	writerDone := make(chan error, 1)
	go func() {
		_, err := fixture.db.Exec(`UPDATE applications SET name=name WHERE id=?`, fixture.job.ResourceID)
		writerDone <- err
	}()
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("independent writer while runner blocked: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("independent writer was blocked by the Compose command")
	}
	releaseStarted := make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() { close(releaseStarted); releaseDone <- fixture.releaseScope() }()
	select {
	case <-releaseStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("scope release did not start")
	}
	closingDeadline := time.Now().Add(5 * time.Second)
	for {
		leave, err := deploymenteffects.EnterScope(fixture.context, fixture.workingDirectory)
		if errors.Is(err, deploymenteffects.ErrScopeUnavailable) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		leave()
		if time.Now().After(closingDeadline) {
			t.Fatal("scope release did not close new entries")
		}
		runtime.Gosched()
	}
	select {
	case err := <-releaseDone:
		t.Fatalf("scope released while runner blocked: %v", err)
	default:
	}
	unblockRunner()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Compose execution did not finish after runner release")
	}
	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scope did not release after Compose observations and cleanup")
	}
}
