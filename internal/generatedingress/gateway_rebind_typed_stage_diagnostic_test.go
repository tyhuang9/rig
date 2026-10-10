package generatedingress

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindTypedStageDiagnosticReporter interface {
	typedStageDiagnostic() (string, string)
}

func TestGatewayRebindTypedStageRuntimeDiagnosticBeforeStartGuard(t *testing.T) {
	fixture := newGatewayRebindTypedStageRuntimeFixture(t)
	defer fixture.close(t)
	upstream := errors.New("sensitive authority refusal")
	guardCalls := 0

	endpoint, err := fixture.runtime.serveStage(context.Background(), fixture.intent,
		*fixture.records[8].TypedEffect, func(context.Context) error {
			guardCalls++
			return upstream
		})
	if endpoint != "" || err == nil {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	var public *Error
	if !errors.As(err, &public) || public.Code != DiagnosticRouteUnresolved ||
		err.Error() != gatewayRebindEffectBoundaryError(context.Background()).Error() || errors.Is(err, upstream) {
		t.Fatalf("public boundary changed: err=%#v unwrap=%#v", err, errors.Unwrap(err))
	}
	var reporter gatewayRebindTypedStageDiagnosticReporter
	if !errors.As(err, &reporter) {
		t.Fatalf("typed-stage checkpoint was lost: err=%#v", err)
	}
	operation, checkpoint := reporter.typedStageDiagnostic()
	if operation != "stable" || checkpoint != "pre_effect_guard" || errors.Unwrap(err) == nil {
		t.Fatalf("operation=%q checkpoint=%q unwrap=%#v", operation, checkpoint, errors.Unwrap(err))
	}
	if guardCalls != 1 || len(fixture.runner.requests) != 0 || len(fixture.runner.effects) != 0 || fixture.runner.starts != 0 {
		t.Fatalf("before-start guard changed reads/effects: guards=%d requests=%#v effects=%#v starts=%d",
			guardCalls, fixture.runner.requests, fixture.runner.effects, fixture.runner.starts)
	}
}

func TestGatewayRebindTypedStageRuntimeDiagnosticRefusalCheckpoints(t *testing.T) {
	t.Run("altered running observation", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		fixture.runner.start()
		configured := fixture.runner.containerRuntime.ConfiguredNetworks[fixture.intent.Identity.IngressNetwork]
		configured.EndpointID = "invalid-endpoint"
		fixture.runner.containerRuntime.ConfiguredNetworks[fixture.intent.Identity.IngressNetwork] = configured
		_, err := fixture.runtime.serveStage(context.Background(), fixture.intent, *fixture.records[8].TypedEffect,
			func(context.Context) error { return nil })
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "serve_stage", "running_shape")
		if fixture.runner.starts != 0 || len(fixture.runner.effects) != 0 {
			t.Fatalf("altered running observation reached effects: starts=%d effects=%#v", fixture.runner.starts, fixture.runner.effects)
		}
	})

	t.Run("live config mismatch", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		fixture.runner.afterArchive = func(r *gatewayRebindTypedStageRuntimeRunner) {
			if r.archiveReads == 1 {
				r.stageBody = []byte(`{"tampered":true}`)
			}
		}
		_, err := fixture.runtime.serveStage(context.Background(), fixture.intent, *fixture.records[8].TypedEffect,
			func(context.Context) error { return nil })
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "serve_stage", "live_config")
		if fixture.runner.starts != 1 || len(fixture.runner.effects) != 1 {
			t.Fatalf("live config mismatch order changed: starts=%d effects=%#v", fixture.runner.starts, fixture.runner.effects)
		}
	})

	t.Run("post-start guard refusal", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		refusal := errors.New("sensitive post-start refusal")
		guardCalls, requestCount, archiveReads := 0, -1, -1
		_, err := fixture.runtime.serveStage(context.Background(), fixture.intent, *fixture.records[8].TypedEffect,
			func(context.Context) error {
				guardCalls++
				if fixture.runner.starts == 0 {
					return nil
				}
				requestCount, archiveReads = len(fixture.runner.requests), fixture.runner.archiveReads
				return refusal
			})
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "serve_stage", "post_effect_guard")
		if errors.Is(err, refusal) || guardCalls != 6 || fixture.runner.starts != 1 || len(fixture.runner.effects) != 1 ||
			requestCount < 0 || archiveReads < 0 || len(fixture.runner.requests) != requestCount || fixture.runner.archiveReads != archiveReads {
			t.Fatalf("post-start guard order changed: guards=%d starts=%d effects=%#v requests=%d/%d archives=%d/%d",
				guardCalls, fixture.runner.starts, fixture.runner.effects, requestCount, len(fixture.runner.requests), archiveReads, fixture.runner.archiveReads)
		}
	})

	t.Run("archive refusal after final copy", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		fixture.runner.start()
		fixture.runner.afterArchive = func(r *gatewayRebindTypedStageRuntimeRunner) {
			if r.archiveReads == 1 {
				r.archiveOverride = stageAutosaveTestArchive(r.t, r.stageBody, r.autosave)
			}
		}
		err := fixture.runtime.copyFinalConfig(context.Background(), fixture.intent, fixture.checkpoint,
			*fixture.records[10].TypedEffect, func(context.Context) error { return nil })
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "copy_final_config", "final_archive")
		if fixture.runner.copies != 1 || len(fixture.runner.effects) != 1 {
			t.Fatalf("final archive refusal order changed: copies=%d effects=%#v", fixture.runner.copies, fixture.runner.effects)
		}
	})

	t.Run("post-copy guard refusal", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		fixture.runner.start()
		refusal := errors.New("sensitive post-copy refusal")
		guardCalls, requestCount, archiveReads := 0, -1, -1
		err := fixture.runtime.copyFinalConfig(context.Background(), fixture.intent, fixture.checkpoint,
			*fixture.records[10].TypedEffect, func(context.Context) error {
				guardCalls++
				if fixture.runner.copies == 0 {
					return nil
				}
				requestCount, archiveReads = len(fixture.runner.requests), fixture.runner.archiveReads
				return refusal
			})
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "copy_final_config", "post_effect_guard")
		if errors.Is(err, refusal) || guardCalls != 4 || fixture.runner.copies != 1 || len(fixture.runner.effects) != 1 ||
			requestCount < 0 || archiveReads < 0 || len(fixture.runner.requests) != requestCount || fixture.runner.archiveReads != archiveReads {
			t.Fatalf("post-copy guard order changed: guards=%d copies=%d effects=%#v requests=%d/%d archives=%d/%d",
				guardCalls, fixture.runner.copies, fixture.runner.effects, requestCount, len(fixture.runner.requests), archiveReads, fixture.runner.archiveReads)
		}
	})

	t.Run("cancelled stable read remains cancelled", func(t *testing.T) {
		fixture := newGatewayRebindTypedStageRuntimeFixture(t)
		defer fixture.close(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := fixture.runtime.stable(ctx, fixture.intent, func(context.Context) error { return nil })
		assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticCancelled, "stable", "first_read")
	})
}

func assertGatewayRebindTypedStageDiagnostic(t *testing.T, err error, code DiagnosticCode, operation, checkpoint string) {
	t.Helper()
	var public *Error
	if err == nil || !errors.As(err, &public) || public.Code != code ||
		err.Error() != (&Error{Code: code}).Error() || errors.Unwrap(err) == nil {
		t.Fatalf("public diagnostic changed: err=%#v unwrap=%#v", err, errors.Unwrap(err))
	}
	var reporter gatewayRebindTypedStageDiagnosticReporter
	if !errors.As(err, &reporter) {
		t.Fatalf("typed-stage diagnostic missing: err=%#v", err)
	}
	if gotOperation, gotCheckpoint := reporter.typedStageDiagnostic(); gotOperation != operation || gotCheckpoint != checkpoint {
		t.Fatalf("operation=%q checkpoint=%q want=%q/%q", gotOperation, gotCheckpoint, operation, checkpoint)
	}
}

func TestGatewayRebindTypedStageDiagnosticSanitizesUnknownError(t *testing.T) {
	upstream := errors.New("sensitive upstream detail")
	err := gatewayRebindTypedStageDiagnosticErrorFrom(context.Background(), upstream)
	assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "unknown", "unknown")
	if errors.Is(err, upstream) || strings.Contains(err.Error(), upstream.Error()) {
		t.Fatalf("upstream detail escaped: err=%#v", err)
	}
	invalid := newGatewayRebindTypedStageDiagnosticError(context.Background(), 99, 99)
	assertGatewayRebindTypedStageDiagnostic(t, invalid, DiagnosticRouteUnresolved, "unknown", "unknown")
}

type gatewayRebindTypedStageDiagnosticDelegate struct {
	gatewayRebindTypedStageDriver
	serveEndpoint string
	serveErr      error
	copyErr       error
	serveCalls    int
	copyCalls     int
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) serveStage(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) (string, error) {
	d.serveCalls++
	return d.serveEndpoint, d.serveErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) copyFinalConfig(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindPredecessorCheckpoint, gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) error {
	d.copyCalls++
	return d.copyErr
}

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverRetainsFirstFailure(t *testing.T) {
	unknown := errors.New("sensitive raw first failure")
	recognized := newGatewayRebindTypedStageDiagnosticError(context.Background(),
		gatewayRebindTypedStageDiagnosticOperationCopyFinalConfig, gatewayRebindTypedStageDiagnosticCheckpointFinalArchive)
	delegate := &gatewayRebindTypedStageDiagnosticDelegate{serveErr: unknown, copyErr: recognized}
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		gatewayRebindTypedStageDriver: delegate,
		first:                         liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot{FirstMethod: liveGatewayRebindRuntimeTypedStageMethodUnknown},
	}
	if _, err := driver.serveStage(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil); !errors.Is(err, unknown) {
		t.Fatalf("serve error=%v", err)
	}
	if err := driver.copyFinalConfig(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindPredecessorCheckpoint{},
		gatewayRebindTypedEffectProgress{}, nil); !errors.Is(err, recognized) {
		t.Fatalf("copy error identity changed: %v", err)
	}
	snapshot := driver.diagnosticSnapshot(errors.New("later compensation failure"))
	if delegate.serveCalls != 1 || delegate.copyCalls != 1 || snapshot.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodServeStage ||
		snapshot.Operation != "unknown" || snapshot.Checkpoint != "unknown" || snapshot.OuterRefusal != "none" {
		t.Fatalf("first refusal changed: delegate=%#v snapshot=%#v", delegate, snapshot)
	}
}

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverSeparatesSuccessfulMethodFromOuterRefusal(t *testing.T) {
	delegate := &gatewayRebindTypedStageDiagnosticDelegate{serveEndpoint: "endpoint"}
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		gatewayRebindTypedStageDriver: delegate,
		first:                         liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot{FirstMethod: liveGatewayRebindRuntimeTypedStageMethodUnknown},
	}
	endpoint, err := driver.serveStage(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil)
	if err != nil || endpoint != "endpoint" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	snapshot := driver.diagnosticSnapshot(errors.New("outer append refusal"))
	if delegate.serveCalls != 1 || !snapshot.ServeStageSucceeded || snapshot.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown ||
		snapshot.Operation != "unknown" || snapshot.Checkpoint != "unknown" || snapshot.OuterRefusal != "after_successful_stage_method" {
		t.Fatalf("successful stage method was misclassified: delegate=%#v snapshot=%#v", delegate, snapshot)
	}
}

type gatewayRebindTypedStageDiagnosticRunnerStub struct {
	result runtimeprocess.CommandResult
	err    error
	calls  int
}

func (s *gatewayRebindTypedStageDiagnosticRunnerStub) Run(context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	s.calls++
	return s.result, s.err
}

func TestLiveGatewayRebindRuntimeDiagnosticRunnerCapturesBoundedResult(t *testing.T) {
	for _, test := range []struct {
		name           string
		context        func() context.Context
		result         runtimeprocess.CommandResult
		err            error
		outcome        string
		commandContext string
	}{
		{name: "success", context: context.Background, result: runtimeprocess.CommandResult{Stdout: []byte("private output"), StdoutTruncated: true}, outcome: "success", commandContext: "active"},
		{name: "failure", context: context.Background, result: runtimeprocess.CommandResult{Stderr: []byte("private error"), StderrTruncated: true}, err: errors.New("private runner error"), outcome: "failure", commandContext: "active"},
		{name: "cancelled", context: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, err: errors.New("private cancellation error"), outcome: "cancelled", commandContext: "cancelled"},
		{name: "deadline", context: func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
			defer cancel()
			return ctx
		}, err: errors.New("private deadline error"), outcome: "deadline", commandContext: "deadline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &gatewayRebindTypedStageDiagnosticRunnerStub{result: test.result, err: test.err}
			runner := &liveGatewayRebindRuntimeDiagnosticRunner{runner: stub}
			result, err := runner.Run(test.context(), runtimeprocess.CommandRequest{Args: []string{"network", "ls", "private-id"}})
			class, diagnostic := runner.diagnosticSnapshot()
			if !errors.Is(err, test.err) || !reflect.DeepEqual(result, test.result) || stub.calls != 1 || class != "network_ls" ||
				diagnostic.Outcome != test.outcome || diagnostic.Context != test.commandContext ||
				diagnostic.StdoutTruncated != test.result.StdoutTruncated || diagnostic.StderrTruncated != test.result.StderrTruncated {
				t.Fatalf("bounded command result changed: calls=%d class=%q diagnostic=%#v err=%v", stub.calls, class, diagnostic, err)
			}
		})
	}
}

func TestLiveGatewayRebindRuntimeFailureDiagnosticUsesBoundedSnapshots(t *testing.T) {
	emptyClass, emptyResult := (&liveGatewayRebindRuntimeDiagnosticRunner{}).diagnosticSnapshot()
	if emptyClass != "unknown" || emptyResult.Outcome != "unknown" || emptyResult.Context != "unknown" ||
		emptyResult.StdoutTruncated || emptyResult.StderrTruncated {
		t.Fatalf("empty runner snapshot changed: class=%q result=%#v", emptyClass, emptyResult)
	}
	stageDelegate := &gatewayRebindTypedStageDiagnosticDelegate{serveEndpoint: "endpoint"}
	stage := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		gatewayRebindTypedStageDriver: stageDelegate,
		first:                         liveGatewayRebindRuntimeTypedStageDiagnosticSnapshot{FirstMethod: liveGatewayRebindRuntimeTypedStageMethodUnknown},
	}
	if _, err := stage.serveStage(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil); err != nil {
		t.Fatal(err)
	}
	runner := &liveGatewayRebindRuntimeDiagnosticRunner{runner: &gatewayRebindTypedStageDiagnosticRunnerStub{
		result: runtimeprocess.CommandResult{StderrTruncated: true}, err: errors.New("private runner failure"),
	}}
	if _, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"container", "ls", "private-id"}}); err == nil {
		t.Fatal("runner failure was lost")
	}
	driver := &liveGatewayRebindRuntimeRollbackDriver{}
	if err := driver.appendValidatedProgress(context.Background(), gatewayRebindProgressRecord{Sequence: 9, Phase: gatewayRebindProgressStageIntent},
		func(context.Context, gatewayRebindProgressRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fixture := liveGatewayRebindRuntimeFixture{diagnosticStage: stage, diagnosticRunner: runner, diagnosticDriver: driver}
	diagnostic := fixture.failureDiagnostic(errors.New("private outer failure"))
	if diagnostic.LastSequence != 9 || diagnostic.LastPhase != gatewayRebindProgressStageIntent ||
		diagnostic.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown || diagnostic.Operation != "unknown" ||
		diagnostic.Checkpoint != "unknown" || !diagnostic.ServeStageSucceeded || diagnostic.OuterRefusal != "after_successful_stage_method" ||
		diagnostic.LatestCommand != "container_ls" || diagnostic.CommandOutcome != "failure" || diagnostic.CommandContext != "active" ||
		diagnostic.StdoutTruncated || !diagnostic.StderrTruncated {
		t.Fatalf("bounded failure diagnostic changed: %#v", diagnostic)
	}
}
