package generatedingress

import (
	"context"
	"errors"
	"testing"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

type gatewayRebindHandoverDiagnosticStageReporter interface {
	handoverDiagnosticStage() string
}

type gatewayRebindDiagnosticRunnerStub struct {
	result runtimeprocess.CommandResult
	err    error
	calls  int
}

func (r *gatewayRebindDiagnosticRunnerStub) Run(context.Context,
	runtimeprocess.CommandRequest,
) (runtimeprocess.CommandResult, error) {
	r.calls++
	return r.result, r.err
}

func gatewayRebindHandoverDiagnosticStageForTest(err error) string {
	var reporter gatewayRebindHandoverDiagnosticStageReporter
	if errors.As(err, &reporter) {
		return reporter.handoverDiagnosticStage()
	}
	return ""
}

func TestGatewayRebindHandoverDiagnosticFirstReadStage(t *testing.T) {
	driver := managerGatewayRebindFinalHandoverDriver{}
	_, err := driver.observeHandover(context.Background(), gatewayRebindFinalHandoverContext{})
	if err == nil || gatewayRebindHandoverDiagnosticStageForTest(err) != "input" {
		t.Fatalf("stage=%q err=%v", gatewayRebindHandoverDiagnosticStageForTest(err), err)
	}
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || diagnostic.Code != DiagnosticRouteUnresolved ||
		err.Error() != gatewayRebindEffectBoundaryError(context.Background()).Error() || errors.Unwrap(err) == nil {
		t.Fatalf("generic boundary contract changed: err=%#v unwrap=%#v", err, errors.Unwrap(err))
	}
	reads := 0
	firstErr := errors.New("first read refusal")
	_, err = observeGatewayRebindHandoverStable(context.Background(), func() (gatewayRebindFinalHandoverObservation, error) {
		reads++
		return gatewayRebindFinalHandoverObservation{}, firstErr
	})
	if !errors.Is(err, firstErr) || reads != 1 {
		t.Fatalf("first read short circuit: reads=%d err=%v", reads, err)
	}
}

func TestGatewayRebindHandoverDiagnosticSecondReadStageUsesFreshGenericCause(t *testing.T) {
	upstream := newGatewayRebindHandoverDiagnosticError(context.Background(), gatewayRebindHandoverDiagnosticHost)
	reads := 0
	_, err := observeGatewayRebindHandoverStable(context.Background(), func() (gatewayRebindFinalHandoverObservation, error) {
		reads++
		if reads == 2 {
			return gatewayRebindFinalHandoverObservation{}, upstream
		}
		return gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent}, nil
	})
	if got := gatewayRebindHandoverDiagnosticStageForTest(err); got != "host" {
		t.Fatalf("stage=%q err=%v", got, err)
	}
	if errors.Unwrap(err) == errors.Unwrap(upstream) || errors.Is(err, upstream) {
		t.Fatal("second read retained the upstream diagnostic error or its cause")
	}
	if reads != 2 {
		t.Fatalf("second read callback count=%d", reads)
	}
	assertGatewayRebindHandoverGenericBoundary(t, err)
}

func TestGatewayRebindHandoverDiagnosticStableReadContract(t *testing.T) {
	first := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerAbsent}
	second := gatewayRebindFinalHandoverObservation{Stage: gatewayRebindHandoverContainerStopped}
	reads := 0
	_, err := observeGatewayRebindHandoverStable(context.Background(), func() (gatewayRebindFinalHandoverObservation, error) {
		reads++
		if reads == 1 {
			return first, nil
		}
		return second, nil
	})
	if got := gatewayRebindHandoverDiagnosticStageForTest(err); got != "stable_read" {
		t.Fatalf("stage=%q err=%v", got, err)
	}
	if reads != 2 {
		t.Fatalf("unequal stable read callback count=%d", reads)
	}
	assertGatewayRebindHandoverGenericBoundary(t, err)
	if err := gatewayRebindHandoverStableReadError(context.Background(), first, first); err != nil {
		t.Fatalf("equal stable reads failed: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledReads := 0
	_, err = observeGatewayRebindHandoverStable(cancelled, func() (gatewayRebindFinalHandoverObservation, error) {
		cancelledReads++
		return first, nil
	})
	if got := gatewayRebindHandoverDiagnosticStageForTest(err); got != "stable_read" {
		t.Fatalf("cancelled stable-read stage=%q err=%v", got, err)
	}
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || diagnostic.Code != DiagnosticCancelled ||
		err.Error() != gatewayRebindEffectBoundaryError(cancelled).Error() || errors.Unwrap(err) == nil || cancelledReads != 2 {
		t.Fatalf("cancelled stable-read contract: reads=%d err=%#v unwrap=%#v", cancelledReads, err, errors.Unwrap(err))
	}
}

func TestGatewayRebindHandoverDiagnosticUnknownSecondReadIsSanitized(t *testing.T) {
	upstream := errors.New("sensitive upstream detail")
	err := gatewayRebindHandoverSecondReadError(context.Background(), upstream)
	if got := gatewayRebindHandoverDiagnosticStageForTest(err); got != "" {
		t.Fatalf("unexpected stage=%q", got)
	}
	if errors.Is(err, upstream) || err.Error() == upstream.Error() {
		t.Fatal("unknown second-read error escaped the generic boundary")
	}
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || diagnostic.Code != DiagnosticRouteUnresolved ||
		err.Error() != gatewayRebindEffectBoundaryError(context.Background()).Error() || errors.Unwrap(err) != nil {
		t.Fatalf("sanitized generic boundary changed: err=%#v unwrap=%#v", err, errors.Unwrap(err))
	}
}

func TestLiveGatewayRebindRuntimeDiagnosticRunnerDelegatesAndPreservesError(t *testing.T) {
	want := errors.New("runner refusal")
	stub := &gatewayRebindDiagnosticRunnerStub{err: want}
	runner := &liveGatewayRebindRuntimeDiagnosticRunner{runner: stub}
	_, err := runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"network", "rm", "private-id"}})
	if !errors.Is(err, want) || stub.calls != 1 || runner.lastCommandClass != "network_rm" {
		t.Fatalf("err=%v calls=%d class=%q", err, stub.calls, runner.lastCommandClass)
	}
	_, _ = runner.Run(context.Background(), runtimeprocess.CommandRequest{Args: []string{"secret", "operation", "private-id"}})
	if runner.lastCommandClass != "unknown" {
		t.Fatalf("unknown args produced class %q", runner.lastCommandClass)
	}
}

func TestLiveGatewayRebindRuntimeValidatedProgressUpdatesOnlyAfterAppend(t *testing.T) {
	driver := &liveGatewayRebindRuntimeRollbackDriver{lastSequence: 3, lastPhase: gatewayRebindProgressStageIntent}
	record := gatewayRebindProgressRecord{Sequence: 4, Phase: gatewayRebindProgressStageConfigIntent}
	want := errors.New("append refusal")
	if err := driver.appendValidatedProgress(context.Background(), record,
		func(context.Context, gatewayRebindProgressRecord) error { return want }); !errors.Is(err, want) {
		t.Fatalf("append error=%v", err)
	}
	if driver.lastSequence != 3 || driver.lastPhase != gatewayRebindProgressStageIntent {
		t.Fatal("failed append changed last validated progress")
	}
	if err := driver.appendValidatedProgress(context.Background(), record,
		func(context.Context, gatewayRebindProgressRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if driver.lastSequence != 4 || driver.lastPhase != gatewayRebindProgressStageConfigIntent {
		t.Fatal("successful protected append did not update last validated progress")
	}
}

func assertGatewayRebindHandoverGenericBoundary(t *testing.T, err error) {
	t.Helper()
	var diagnostic *Error
	if !errors.As(err, &diagnostic) || diagnostic.Code != DiagnosticRouteUnresolved ||
		err.Error() != gatewayRebindEffectBoundaryError(context.Background()).Error() || errors.Unwrap(err) == nil {
		t.Fatalf("generic boundary contract changed: err=%#v unwrap=%#v", err, errors.Unwrap(err))
	}
}
