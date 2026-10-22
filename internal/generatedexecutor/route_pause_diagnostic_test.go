package generatedexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
	"github.com/hostd/hostd/internal/jobs"
)

func TestGeneratedExecutorRoutePauseAttribution(t *testing.T) {
	t.Run("runtime begin", func(t *testing.T) {
		fixture := newExecutorFixture(t, false)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.state.beginErr = generatedruntimestate.ErrDeploymentInProgress

		result, err := fixture.execute(context.Background(), fixture.deploymentJob(), fixture.reporter)
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRuntimeBegin, routePauseCauseDeploymentInProgress)
	})

	t.Run("committed runtime head mismatch", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.deployments.transitionErrStatus = deployments.Succeeded
		fixture.deployments.transitionErr = errors.New("final deployment acknowledgement unavailable")
		if _, err := fixture.execute(); err == nil {
			t.Fatal("initial finalization unexpectedly succeeded")
		}
		fixture.deployments.transitionErr = nil
		fixture.state.activeOverride = &generatedruntimestate.ActiveHead{
			AppID: fixture.app.ID, DeploymentID: testActorID, ReleaseID: fixture.release.ID,
			Slot: string(generatedruntime.SlotBlue), Generation: 2,
		}

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRuntimeSuccessHead, routePauseCauseActiveHeadMismatch)
	})

	t.Run("committed runtime head read error", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.deployments.transitionErrStatus = deployments.Succeeded
		fixture.deployments.transitionErr = errors.New("final deployment acknowledgement unavailable")
		if _, err := fixture.execute(); err == nil {
			t.Fatal("initial finalization unexpectedly succeeded")
		}
		fixture.deployments.transitionErr = nil
		fixture.executor.state = routePauseActiveErrorState{
			runtimeState: fixture.state,
			err:          generatedruntimestate.ErrInvalidState,
		}
		builds, switches, legacyStops := fixture.compiler.calls, len(fixture.routes.requests), fixture.runtime.stopCalls
		creates, starts := fixture.runner.createCalls, fixture.runner.startCalls
		physicalStops, removals := fixture.runner.stopCalls, fixture.runner.removeCalls

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRuntimeSuccessHead, routePauseCauseInvalidState)
		if fixture.compiler.calls != builds || len(fixture.routes.requests) != switches || fixture.runtime.stopCalls != legacyStops ||
			fixture.runner.createCalls != creates || fixture.runner.startCalls != starts ||
			fixture.runner.stopCalls != physicalStops || fixture.runner.removeCalls != removals {
			t.Fatalf("head read pause performed effects: builds=%d/%d switches=%d/%d legacy stops=%d/%d creates=%d/%d starts=%d/%d physical stops=%d/%d removals=%d/%d",
				fixture.compiler.calls, builds, len(fixture.routes.requests), switches, fixture.runtime.stopCalls, legacyStops,
				fixture.runner.createCalls, creates, fixture.runner.startCalls, starts, fixture.runner.stopCalls, physicalStops,
				fixture.runner.removeCalls, removals)
		}
		runtimeDeployment, deployment := fixture.runtimeDeployment(t), fixture.deployment(t)
		if runtimeDeployment.Phase != generatedruntimestate.PhaseSucceeded || deployment.Status != deployments.WaitingHealth {
			t.Fatalf("head read pause changed durable state: runtime=%+v deployment=%+v", runtimeDeployment, deployment)
		}
	})

	t.Run("gateway readiness during route switch", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.routes.err = gatewayReadinessRouteError{}

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRouteSwitch, routePauseCauseGatewayReadinessFailed)
	})

	t.Run("post active route state persistence", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.state.failAdvanceToDraining = true

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRouteSwitch, routePauseCauseUnknown)
	})

	t.Run("receipt owned predecessor retirement", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		if _, err := fixture.execute(); err != nil {
			t.Fatal(err)
		}
		fixture.nextDeployment(t)
		fixture.routes.requests = nil
		fixture.executor.waitDrain = func(context.Context, time.Duration) error { return nil }

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStagePreviousRetirement, routePauseCauseRetirementUnavailable)
		if fixture.runtime.stopCalls != 1 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 {
			t.Fatalf("retirement guard attempts=%d physical stops=%d removals=%d", fixture.runtime.stopCalls, fixture.runner.stopCalls, fixture.runner.removeCalls)
		}
	})

	t.Run("final runtime advance", func(t *testing.T) {
		const secret = "token=do-not-disclose /private/path"
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.state.advanceErrTo = generatedruntimestate.PhaseSucceeded
		fixture.state.advanceErr = errors.New(secret)

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRuntimeSuccessAdvance, routePauseCauseUnknown)
		if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "/private/path") {
			t.Fatalf("route pause log exposed raw error: %s", logs.String())
		}
	})

	t.Run("success emits no observation", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		result, err := fixture.execute()
		if err != nil || result.CompletionCode != "deployment_completed" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if logs.Len() != 0 {
			t.Fatalf("successful execution emitted route pause: %s", logs.String())
		}
	})
}

func TestGeneratedExecutorRoutePauseCauseClassification(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want routePauseCause
	}{
		{name: "retirement unavailable", err: errors.Join(errors.New("private detail"), deployments.ErrGeneratedCandidateRetirementUnavailable), want: routePauseCauseRetirementUnavailable},
		{name: "candidate cleanup", err: &generatedruntime.Error{Code: generatedruntime.DiagnosticCandidateCleanupFailed}, want: routePauseCauseCandidateCleanupFailed},
		{name: "gateway readiness", err: routeSwitchError{gatewayReadinessFailed: true}, want: routePauseCauseGatewayReadinessFailed},
		{name: "canceled", err: context.Canceled, want: routePauseCauseCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: routePauseCauseDeadlineExceeded},
		{name: "state not found", err: generatedruntimestate.ErrNotFound, want: routePauseCauseStateNotFound},
		{name: "invalid state", err: generatedruntimestate.ErrInvalidState, want: routePauseCauseInvalidState},
		{name: "invalid transition", err: generatedruntimestate.ErrInvalidTransition, want: routePauseCauseInvalidTransition},
		{name: "state conflict", err: generatedruntimestate.ErrConflict, want: routePauseCauseStateConflict},
		{name: "unmatched", err: errors.New("token=do-not-disclose /private/path"), want: routePauseCauseUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyRoutePauseCause(test.err); got != test.want {
				t.Fatalf("cause=%q want=%q", got, test.want)
			}
		})
	}
}

func TestGeneratedExecutorRoutePauseDiagnosticControls(t *testing.T) {
	t.Run("logger write failure cannot change pause", func(t *testing.T) {
		fixture := newExecutorFixture(t, false)
		fixture.executor.options.RoutePauseLogger = slog.New(slog.NewJSONHandler(routePauseFailingWriter{}, nil))
		fixture.state.beginErr = generatedruntimestate.ErrDeploymentInProgress
		result, err := fixture.execute(context.Background(), fixture.deploymentJob(), fixture.reporter)
		requireRoutePauseResult(t, result, err)
	})

	t.Run("invalid pair is clamped", func(t *testing.T) {
		executor := &Executor{}
		logs := attachRoutePauseTestLogger(executor)
		executor.observeRoutePause(context.Background(), routePauseStageRuntimeBegin, routePauseCauseRetirementUnavailable)
		requireRoutePauseRecord(t, logs, routePauseStageRuntimeBegin, routePauseCauseUnknown)
	})

	t.Run("unknown stage is omitted", func(t *testing.T) {
		executor := &Executor{}
		logs := attachRoutePauseTestLogger(executor)
		executor.observeRoutePause(context.Background(), routePauseStage("forged-stage"), routePauseCauseUnknown)
		if logs.Len() != 0 {
			t.Fatalf("unknown route pause stage was logged: %s", logs.String())
		}
	})

	t.Run("nil logger preserves no-op", func(t *testing.T) {
		(&Executor{}).observeRoutePause(context.Background(), routePauseStageRuntimeBegin, routePauseCauseDeploymentInProgress)
	})
}

type routePauseFailingWriter struct{}

func (routePauseFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("diagnostic sink unavailable")
}

type routePauseActiveErrorState struct {
	runtimeState
	err error
}

func (s routePauseActiveErrorState) Active(context.Context, string) (generatedruntimestate.ActiveHead, error) {
	return generatedruntimestate.ActiveHead{}, s.err
}

func attachRoutePauseTestLogger(executor *Executor) *bytes.Buffer {
	logs := &bytes.Buffer{}
	executor.options.RoutePauseLogger = slog.New(slog.NewJSONHandler(logs, nil))
	return logs
}

func requireRoutePauseResult(t *testing.T, result jobs.ExecutionResult, err error) {
	t.Helper()
	want := waitingFor(jobs.PauseRouteReconciliationRequired)
	if err != nil || result != want {
		t.Fatalf("result=%+v want=%+v err=%v", result, want, err)
	}
}

func requireRoutePauseRecord(t *testing.T, logs *bytes.Buffer, stage routePauseStage, cause routePauseCause) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("route pause records=%q want exactly one", logs.String())
	}
	var record struct {
		Message string `json:"msg"`
		Stage   string `json:"route_pause_stage"`
		Cause   string `json:"route_pause_cause"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode route pause record: %v", err)
	}
	if record.Message != routePauseMessage || record.Stage != string(stage) || record.Cause != string(cause) {
		t.Fatalf("route pause record=%+v want message=%q stage=%q cause=%q", record, routePauseMessage, stage, cause)
	}
}
