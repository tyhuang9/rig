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

func TestGatewayRebindTypedStageRuntimeCopyStageConfigRetainsStableGuardDiagnostic(t *testing.T) {
	fixture := newGatewayRebindTypedStageRuntimeFixture(t)
	defer fixture.close(t)
	refusal := errors.New("sensitive stage-config authority refusal")
	guardCalls := 0
	effect := gatewayRebindTypedStageConfigIntentEffect(t, fixture.records)

	err := fixture.runtime.copyStageConfig(context.Background(), fixture.intent, effect,
		func(context.Context) error {
			guardCalls++
			return refusal
		})
	if errors.Is(err, refusal) || guardCalls != 1 || len(fixture.runner.requests) != 0 ||
		len(fixture.runner.effects) != 0 || fixture.runner.copies != 0 {
		t.Fatalf("copy-stage stable guard changed reads/effects: guards=%d requests=%#v effects=%#v copies=%d",
			guardCalls, fixture.runner.requests, fixture.runner.effects, fixture.runner.copies)
	}
	assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticRouteUnresolved, "stable", "pre_effect_guard")
}

func TestGatewayRebindTypedStageRuntimeCopyStageConfigSanitizesCancelledStableRead(t *testing.T) {
	fixture := newGatewayRebindTypedStageRuntimeFixture(t)
	defer fixture.close(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fixture.runtime.copyStageConfig(ctx, fixture.intent,
		gatewayRebindTypedStageConfigIntentEffect(t, fixture.records), func(context.Context) error { return nil })
	assertGatewayRebindTypedStageDiagnostic(t, err, DiagnosticCancelled, "stable", "first_read")
	if len(fixture.runner.effects) != 0 || fixture.runner.copies != 0 {
		t.Fatalf("cancelled stable read reached copy: effects=%#v copies=%d", fixture.runner.effects, fixture.runner.copies)
	}
}

func gatewayRebindTypedStageConfigIntentEffect(t *testing.T,
	records []gatewayRebindProgressRecord,
) gatewayRebindTypedEffectProgress {
	t.Helper()
	for _, record := range records {
		if record.Phase != gatewayRebindProgressStageConfigIntent {
			continue
		}
		if record.TypedEffect == nil || record.TypedEffect.StageConfigIntent == nil ||
			record.TypedEffect.StageConfigCopy != nil {
			t.Fatalf("invalid P7 stage-config intent effect: %#v", record.TypedEffect)
		}
		return *record.TypedEffect
	}
	t.Fatal("missing P7 stage-config intent effect")
	return gatewayRebindTypedEffectProgress{}
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
	image             string
	imageErr          error
	network           gatewayRebindStageNetworkBinding
	networkErr        error
	configVolume      gatewayRebindStageConfigVolumeBinding
	configVolumeErr   error
	dataVolume        gatewayRebindStageDataVolumeBinding
	dataVolumeErr     error
	stageContainer    gatewayRebindStageContainerBinding
	stageContainerErr error
	copyStageErr      error
	serveEndpoint     string
	serveErr          error
	copyErr           error
	calls             map[liveGatewayRebindRuntimeTypedStageMethod]int
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) call(method liveGatewayRebindRuntimeTypedStageMethod) {
	if d.calls == nil {
		d.calls = make(map[liveGatewayRebindRuntimeTypedStageMethod]int)
	}
	d.calls[method]++
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) observeImage(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectGuard,
) (string, error) {
	d.call(liveGatewayRebindRuntimeTypedStageMethodObserveImage)
	return d.image, d.imageErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) bindNetwork(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) (gatewayRebindStageNetworkBinding, error) {
	d.call(liveGatewayRebindRuntimeTypedStageMethodBindNetwork)
	return d.network, d.networkErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) bindConfigVolume(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) (gatewayRebindStageConfigVolumeBinding, error) {
	d.call(liveGatewayRebindRuntimeTypedStageMethodBindConfigVolume)
	return d.configVolume, d.configVolumeErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) bindDataVolume(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) (gatewayRebindStageDataVolumeBinding, error) {
	d.call(liveGatewayRebindRuntimeTypedStageMethodBindDataVolume)
	return d.dataVolume, d.dataVolumeErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) bindStageContainer(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) (gatewayRebindStageContainerBinding, error) {
	d.call(liveGatewayRebindRuntimeTypedStageMethodBindStageContainer)
	return d.stageContainer, d.stageContainerErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) copyStageConfig(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) error {
	d.call(liveGatewayRebindRuntimeTypedStageMethodCopyStageConfig)
	return d.copyStageErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) serveStage(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) (string, error) {
	d.call(liveGatewayRebindRuntimeTypedStageMethodServeStage)
	return d.serveEndpoint, d.serveErr
}

func (d *gatewayRebindTypedStageDiagnosticDelegate) copyFinalConfig(context.Context, gatewayRebindProtectedIntentV2,
	gatewayRebindPredecessorCheckpoint, gatewayRebindTypedEffectProgress, gatewayRebindTypedEffectGuard,
) error {
	d.call(liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig)
	return d.copyErr
}

var _ gatewayRebindTypedStageDriver = (*gatewayRebindTypedStageDiagnosticDelegate)(nil)

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverRetainsFirstFailure(t *testing.T) {
	unknown := errors.New("sensitive raw first failure")
	recognized := newGatewayRebindTypedStageDiagnosticError(context.Background(),
		gatewayRebindTypedStageDiagnosticOperationCopyFinalConfig, gatewayRebindTypedStageDiagnosticCheckpointFinalArchive)
	delegate := &gatewayRebindTypedStageDiagnosticDelegate{serveErr: unknown, copyErr: recognized}
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		delegate: delegate,
	}
	if _, err := driver.serveStage(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil); !errors.Is(err, unknown) {
		t.Fatalf("serve error=%v", err)
	}
	if err := driver.copyFinalConfig(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindPredecessorCheckpoint{},
		gatewayRebindTypedEffectProgress{}, nil); !errors.Is(err, recognized) {
		t.Fatalf("copy error identity changed: %v", err)
	}
	snapshot := driver.diagnosticSnapshot(errors.New("later compensation failure"))
	if delegate.calls[liveGatewayRebindRuntimeTypedStageMethodServeStage] != 1 ||
		delegate.calls[liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig] != 1 || !snapshot.FirstFailed ||
		snapshot.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodServeStage ||
		snapshot.Operation != "unknown" || snapshot.Checkpoint != "unknown" || snapshot.OuterRefusal != "none" {
		t.Fatalf("first refusal changed: delegate=%#v snapshot=%#v", delegate, snapshot)
	}
}

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverSeparatesSuccessfulMethodFromOuterRefusal(t *testing.T) {
	delegate := &gatewayRebindTypedStageDiagnosticDelegate{serveEndpoint: "endpoint"}
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		delegate: delegate,
	}
	endpoint, err := driver.serveStage(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil)
	if err != nil || endpoint != "endpoint" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	snapshot := driver.diagnosticSnapshot(errors.New("outer append refusal"))
	if delegate.calls[liveGatewayRebindRuntimeTypedStageMethodServeStage] != 1 || !snapshot.ServeStageSucceeded || snapshot.FirstFailed ||
		snapshot.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown ||
		snapshot.Operation != "unknown" || snapshot.Checkpoint != "unknown" || snapshot.OuterRefusal != "after_successful_stage_method" {
		t.Fatalf("successful stage method was misclassified: delegate=%#v snapshot=%#v", delegate, snapshot)
	}
}

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverForwardsAllMethodsExactlyOnce(t *testing.T) {
	observeErr := errors.New("observe identity")
	networkErr := errors.New("network identity")
	configErr := errors.New("config identity")
	dataErr := errors.New("data identity")
	containerErr := errors.New("container identity")
	copyStageErr := errors.New("copy-stage identity")
	serveErr := errors.New("serve identity")
	copyFinalErr := errors.New("copy-final identity")
	delegate := &gatewayRebindTypedStageDiagnosticDelegate{
		image: "image", imageErr: observeErr,
		network: gatewayRebindStageNetworkBinding{ID: "network"}, networkErr: networkErr,
		configVolume: gatewayRebindStageConfigVolumeBinding{Name: "config"}, configVolumeErr: configErr,
		dataVolume: gatewayRebindStageDataVolumeBinding{Name: "data"}, dataVolumeErr: dataErr,
		stageContainer: gatewayRebindStageContainerBinding{ID: "container"}, stageContainerErr: containerErr,
		copyStageErr: copyStageErr, serveEndpoint: "endpoint", serveErr: serveErr, copyErr: copyFinalErr,
	}
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{delegate: delegate}
	ctx, intent, effect, guard := context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, gatewayRebindTypedEffectGuard(nil)
	if image, err := driver.observeImage(ctx, intent, guard); image != delegate.image || err != observeErr {
		t.Fatalf("observe returned image=%q err=%v", image, err)
	}
	if value, err := driver.bindNetwork(ctx, intent, effect, guard); !reflect.DeepEqual(value, delegate.network) || err != networkErr {
		t.Fatalf("network returned value=%#v err=%v", value, err)
	}
	if value, err := driver.bindConfigVolume(ctx, intent, effect, guard); !reflect.DeepEqual(value, delegate.configVolume) || err != configErr {
		t.Fatalf("config returned value=%#v err=%v", value, err)
	}
	if value, err := driver.bindDataVolume(ctx, intent, effect, guard); !reflect.DeepEqual(value, delegate.dataVolume) || err != dataErr {
		t.Fatalf("data returned value=%#v err=%v", value, err)
	}
	if value, err := driver.bindStageContainer(ctx, intent, effect, guard); !reflect.DeepEqual(value, delegate.stageContainer) || err != containerErr {
		t.Fatalf("container returned value=%#v err=%v", value, err)
	}
	if err := driver.copyStageConfig(ctx, intent, effect, guard); err != copyStageErr {
		t.Fatalf("copy stage error identity changed: %v", err)
	}
	if endpoint, err := driver.serveStage(ctx, intent, effect, guard); endpoint != delegate.serveEndpoint || err != serveErr {
		t.Fatalf("serve returned endpoint=%q err=%v", endpoint, err)
	}
	if err := driver.copyFinalConfig(ctx, intent, gatewayRebindPredecessorCheckpoint{}, effect, guard); err != copyFinalErr {
		t.Fatalf("copy final error identity changed: %v", err)
	}
	for _, method := range []liveGatewayRebindRuntimeTypedStageMethod{
		liveGatewayRebindRuntimeTypedStageMethodObserveImage,
		liveGatewayRebindRuntimeTypedStageMethodBindNetwork,
		liveGatewayRebindRuntimeTypedStageMethodBindConfigVolume,
		liveGatewayRebindRuntimeTypedStageMethodBindDataVolume,
		liveGatewayRebindRuntimeTypedStageMethodBindStageContainer,
		liveGatewayRebindRuntimeTypedStageMethodCopyStageConfig,
		liveGatewayRebindRuntimeTypedStageMethodServeStage,
		liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig,
	} {
		if delegate.calls[method] != 1 {
			t.Fatalf("method %q calls=%d", method, delegate.calls[method])
		}
	}
}

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverRetainsUnknownFirstFailure(t *testing.T) {
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{}
	driver.record(liveGatewayRebindRuntimeTypedStageMethod("unexpected"), errors.New("raw first failure"))
	driver.record(liveGatewayRebindRuntimeTypedStageMethodCopyFinalConfig,
		newGatewayRebindTypedStageDiagnosticError(context.Background(),
			gatewayRebindTypedStageDiagnosticOperationCopyFinalConfig, gatewayRebindTypedStageDiagnosticCheckpointFinalArchive))
	snapshot := driver.diagnosticSnapshot(nil)
	if !snapshot.FirstFailed || snapshot.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown ||
		snapshot.Operation != "unknown" || snapshot.Checkpoint != "unknown" {
		t.Fatalf("unknown first failure was replaced: %#v", snapshot)
	}
}

func TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverClassifiesSuccessfulCopyStageMethod(t *testing.T) {
	delegate := &gatewayRebindTypedStageDiagnosticDelegate{}
	driver := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{delegate: delegate}
	if err := driver.copyStageConfig(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := driver.diagnosticSnapshot(errors.New("outer append refusal"))
	if delegate.calls[liveGatewayRebindRuntimeTypedStageMethodCopyStageConfig] != 1 || snapshot.FirstFailed ||
		!snapshot.CopyStageConfigSuccess || snapshot.LastSuccessfulMethod != liveGatewayRebindRuntimeTypedStageMethodCopyStageConfig ||
		snapshot.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown ||
		snapshot.OuterRefusal != "after_successful_stage_method" {
		t.Fatalf("successful copy-stage method was misclassified: delegate=%#v snapshot=%#v", delegate, snapshot)
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
		delegate: stageDelegate,
	}
	if err := stage.copyStageConfig(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil); err != nil {
		t.Fatal(err)
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
		diagnostic.FirstFailed || diagnostic.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodUnknown || diagnostic.Operation != "unknown" ||
		diagnostic.Checkpoint != "unknown" || diagnostic.LastSuccessfulMethod != liveGatewayRebindRuntimeTypedStageMethodServeStage ||
		!diagnostic.CopyStageConfigOK || !diagnostic.ServeStageSucceeded || diagnostic.OuterRefusal != "after_successful_stage_method" ||
		diagnostic.LatestCommand != "container_ls" || diagnostic.CommandOutcome != "failure" || diagnostic.CommandContext != "active" ||
		diagnostic.StdoutTruncated || !diagnostic.StderrTruncated {
		t.Fatalf("bounded failure diagnostic changed: %#v", diagnostic)
	}

	delegateFailure := newGatewayRebindTypedStageDiagnosticError(context.Background(),
		gatewayRebindTypedStageDiagnosticOperationCopyStageConfig, gatewayRebindTypedStageDiagnosticCheckpointCopyEffect)
	failedStage := &liveGatewayRebindRuntimeTypedStageDiagnosticDriver{
		delegate: &gatewayRebindTypedStageDiagnosticDelegate{copyStageErr: delegateFailure},
	}
	if err := failedStage.copyStageConfig(context.Background(), gatewayRebindProtectedIntentV2{}, gatewayRebindTypedEffectProgress{}, nil); err != delegateFailure {
		t.Fatalf("delegate failure identity changed: %v", err)
	}
	failedFixture := liveGatewayRebindRuntimeFixture{diagnosticStage: failedStage}
	failedDiagnostic := failedFixture.failureDiagnostic(nil)
	if !failedDiagnostic.FirstFailed || failedDiagnostic.FirstMethod != liveGatewayRebindRuntimeTypedStageMethodCopyStageConfig ||
		failedDiagnostic.Operation != "copy_stage_config" || failedDiagnostic.Checkpoint != "copy_effect" ||
		failedDiagnostic.OuterRefusal != "none" {
		t.Fatalf("nil outer error fabricated refusal or lost delegate failure: %#v", failedDiagnostic)
	}
}
