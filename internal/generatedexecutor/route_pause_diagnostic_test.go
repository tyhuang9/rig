package generatedexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
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
		fixture.routes.postActivateErr = errors.New("route activation acknowledgement unavailable")

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStageRouteSwitch, routePauseCauseUnknown)
		if runtime := fixture.runtimeDeployment(t); runtime.Phase != generatedruntimestate.PhaseDraining ||
			fixture.routes.delegateCalls != 1 {
			t.Fatalf("durable activation runtime=%+v delegate calls=%d", runtime, fixture.routes.delegateCalls)
		}
	})
}

func TestGeneratedExecutorCompletionRoutePauseAttribution(t *testing.T) {
	t.Run("receiptless succeeded runtime", func(t *testing.T) {
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		result, err := fixture.execute()
		if err != nil || result.CompletionCode != "deployment_completed" || logs.Len() != 0 {
			t.Fatalf("first result=%+v err=%v logs=%s", result, err, logs.String())
		}
		builds, switches, legacyStops := fixture.compiler.calls, len(fixture.routes.requests), fixture.runtime.stopCalls
		creates, starts, physicalStops, removals := fixture.runner.createCalls, fixture.runner.startCalls, fixture.runner.stopCalls, fixture.runner.removeCalls

		result, err = fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStagePreviousRetirement, routePauseCauseRetirementUnavailable)
		if fixture.compiler.calls != builds || len(fixture.routes.requests) != switches || fixture.runtime.stopCalls != legacyStops ||
			fixture.runner.createCalls != creates || fixture.runner.startCalls != starts ||
			fixture.runner.stopCalls != physicalStops || fixture.runner.removeCalls != removals {
			t.Fatal("receiptless replay performed runtime effects")
		}
	})

	t.Run("receipt owned completion refusal", func(t *testing.T) {
		const secret = "token=do-not-disclose /private/path"
		fixture := newSealedExecutorFixture(t)
		logs := attachRoutePauseTestLogger(fixture.executor)
		fixture.routes.retirementBlock = errors.Join(errors.New(secret), deployments.ErrGeneratedCandidateRetirementUnavailable)

		result, err := fixture.execute()
		requireRoutePauseResult(t, result, err)
		requireRoutePauseRecord(t, logs, routePauseStagePreviousRetirement, routePauseCauseRetirementUnavailable)
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
	var nilIngress *generatedingress.Error
	routeUnresolved := func() error {
		return &generatedingress.Error{Code: generatedingress.DiagnosticRouteUnresolved}
	}
	unrelatedIngress := func() error {
		return &generatedingress.Error{Code: generatedingress.DiagnosticValidationFailed}
	}
	for _, test := range []struct {
		name string
		err  error
		want routePauseCause
	}{
		{name: "retirement unavailable", err: errors.Join(errors.New("private detail"), deployments.ErrGeneratedCandidateRetirementUnavailable), want: routePauseCauseRetirementUnavailable},
		{name: "candidate cleanup", err: &generatedruntime.Error{Code: generatedruntime.DiagnosticCandidateCleanupFailed}, want: routePauseCauseCandidateCleanupFailed},
		{name: "gateway readiness", err: routeSwitchError{cause: routePauseCauseGatewayReadinessFailed}, want: routePauseCauseGatewayReadinessFailed},
		{name: "canceled", err: context.Canceled, want: routePauseCauseCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: routePauseCauseDeadlineExceeded},
		{name: "state not found", err: generatedruntimestate.ErrNotFound, want: routePauseCauseStateNotFound},
		{name: "invalid state", err: generatedruntimestate.ErrInvalidState, want: routePauseCauseInvalidState},
		{name: "invalid transition", err: generatedruntimestate.ErrInvalidTransition, want: routePauseCauseInvalidTransition},
		{name: "state conflict", err: generatedruntimestate.ErrConflict, want: routePauseCauseStateConflict},
		{name: "deployment execution canceled", err: deploymentexecution.ErrCanceled, want: routePauseCauseCanceled},
		{name: "invalid claim", err: deploymentexecution.ErrInvalidClaim, want: routePauseCauseInvalidClaim},
		{name: "invalid configuration", err: deploymentexecution.ErrInvalidConfiguration, want: routePauseCauseInvalidConfiguration},
		{name: "scope unavailable", err: deploymentexecution.ErrScopeUnavailable, want: routePauseCauseScopeUnavailable},
		{name: "authority stale", err: deploymentexecution.ErrAuthorityStale, want: routePauseCauseAuthorityStale},
		{name: "authority invalid", err: deploymentexecution.ErrAuthorityInvalid, want: routePauseCauseAuthorityInvalid},
		{name: "authority unresolved", err: deploymentexecution.ErrAuthorityUnresolved, want: routePauseCauseAuthorityUnresolved},
		{name: "identity lost", err: deploymentexecution.ErrIdentityLost, want: routePauseCauseIdentityLost},
		{name: "stage invalid", err: deploymentexecution.ErrStageInvalid, want: routePauseCauseStageInvalid},
		{name: "positive fence", err: deploymentexecution.ErrPositiveFence, want: routePauseCausePositiveFence},
		{name: "storage unavailable", err: deploymentexecution.ErrUnavailable, want: routePauseCauseStorageUnavailable},
		{name: "route unresolved", err: routeUnresolved(), want: routePauseCauseRouteUnresolved},
		{name: "typed nil ingress is unknown", err: nilIngress, want: routePauseCauseUnknown},
		{name: "unrelated ingress before route", err: errors.Join(unrelatedIngress(), routeUnresolved()), want: routePauseCauseRouteUnresolved},
		{name: "route before unrelated ingress", err: errors.Join(routeUnresolved(), unrelatedIngress()), want: routePauseCauseRouteUnresolved},
		{name: "typed nil before route", err: errors.Join(nilIngress, routeUnresolved()), want: routePauseCauseRouteUnresolved},
		{name: "route before typed nil", err: errors.Join(routeUnresolved(), nilIngress), want: routePauseCauseRouteUnresolved},
		{name: "identity wins storage", err: errors.Join(deploymentexecution.ErrIdentityLost, deploymentexecution.ErrUnavailable), want: routePauseCauseIdentityLost},
		{name: "identity wins storage reverse", err: errors.Join(deploymentexecution.ErrUnavailable, deploymentexecution.ErrIdentityLost), want: routePauseCauseIdentityLost},
		{name: "duplicate identity remains identity", err: errors.Join(deploymentexecution.ErrIdentityLost, deploymentexecution.ErrIdentityLost), want: routePauseCauseIdentityLost},
		{name: "distinct identities are unknown", err: errors.Join(deploymentexecution.ErrAuthorityInvalid, deploymentexecution.ErrIdentityLost), want: routePauseCauseUnknown},
		{name: "distinct identities reverse are unknown", err: errors.Join(deploymentexecution.ErrIdentityLost, deploymentexecution.ErrAuthorityInvalid), want: routePauseCauseUnknown},
		{name: "distinct identities with storage are unknown", err: errors.Join(deploymentexecution.ErrAuthorityInvalid, deploymentexecution.ErrUnavailable, deploymentexecution.ErrIdentityLost), want: routePauseCauseUnknown},
		{name: "route and identity are unknown", err: errors.Join(routeUnresolved(), deploymentexecution.ErrIdentityLost), want: routePauseCauseUnknown},
		{name: "route and identity reverse are unknown", err: errors.Join(deploymentexecution.ErrIdentityLost, routeUnresolved()), want: routePauseCauseUnknown},
		{name: "route wins storage", err: errors.Join(routeUnresolved(), deploymentexecution.ErrUnavailable), want: routePauseCauseRouteUnresolved},
		{name: "route wins storage reverse", err: errors.Join(deploymentexecution.ErrUnavailable, routeUnresolved()), want: routePauseCauseRouteUnresolved},
		{name: "old state cause wins identity", err: errors.Join(deploymentexecution.ErrIdentityLost, generatedruntimestate.ErrConflict), want: routePauseCauseStateConflict},
		{name: "canceled wins identity", err: errors.Join(deploymentexecution.ErrIdentityLost, deploymentexecution.ErrCanceled), want: routePauseCauseCanceled},
		{name: "context deadline wins deployment cancellation", err: errors.Join(deploymentexecution.ErrCanceled, context.DeadlineExceeded), want: routePauseCauseDeadlineExceeded},
		{name: "unapproved sealed cause does not fall through", err: errors.Join(routeSwitchError{cause: routePauseCauseRetirementUnavailable}, deploymentexecution.ErrIdentityLost), want: routePauseCauseUnknown},
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
	t.Run("route adapter retains only closed causes", func(t *testing.T) {
		const secret = "token=do-not-disclose /private/path"
		for _, test := range []struct {
			name string
			err  error
			want routePauseCause
		}{
			{name: "canceled", err: errors.Join(errors.New(secret), context.Canceled), want: routePauseCauseCanceled},
			{name: "deadline", err: errors.Join(errors.New(secret), context.DeadlineExceeded), want: routePauseCauseDeadlineExceeded},
			{name: "not found", err: errors.Join(errors.New(secret), generatedruntimestate.ErrNotFound), want: routePauseCauseStateNotFound},
			{name: "invalid state", err: errors.Join(errors.New(secret), generatedruntimestate.ErrInvalidState), want: routePauseCauseInvalidState},
			{name: "invalid transition", err: errors.Join(errors.New(secret), generatedruntimestate.ErrInvalidTransition), want: routePauseCauseInvalidTransition},
			{name: "conflict", err: errors.Join(errors.New(secret), generatedruntimestate.ErrConflict), want: routePauseCauseStateConflict},
			{name: "unknown", err: errors.New(secret), want: routePauseCauseUnknown},
		} {
			t.Run(test.name, func(t *testing.T) {
				adapted := newRouteSwitchError(test.err, "route activation failed")
				if got := classifyRoutePauseCause(adapted); got != test.want {
					t.Fatalf("cause=%q want=%q", got, test.want)
				}
				if strings.Contains(adapted.Error(), secret) || errors.Is(adapted, test.err) {
					t.Fatalf("adapter retained raw route error: %v", adapted)
				}
			})
		}
	})

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

	t.Run("retirement identities are previous retirement only", func(t *testing.T) {
		causes := []routePauseCause{
			routePauseCauseInvalidClaim,
			routePauseCauseInvalidConfiguration,
			routePauseCauseScopeUnavailable,
			routePauseCauseAuthorityStale,
			routePauseCauseAuthorityInvalid,
			routePauseCauseAuthorityUnresolved,
			routePauseCauseIdentityLost,
			routePauseCauseStageInvalid,
			routePauseCausePositiveFence,
			routePauseCauseStorageUnavailable,
			routePauseCauseRouteUnresolved,
		}
		stages := []routePauseStage{
			routePauseStageRuntimeBegin,
			routePauseStageRuntimeSuccessHead,
			routePauseStageRouteSwitch,
			routePauseStagePreviousRetirement,
			routePauseStageRuntimeSuccessAdvance,
		}
		for _, cause := range causes {
			for _, stage := range stages {
				want := stage == routePauseStagePreviousRetirement
				if got := knownRoutePausePair(stage, cause); got != want {
					t.Fatalf("known pair stage=%q cause=%q got=%t want=%t", stage, cause, got, want)
				}
			}
		}
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

func TestGeneratedExecutorPreviousRetirementIntegrityRoutePauseAttribution(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want routePauseCause
	}{
		{name: "identity lost", err: deploymentexecution.ErrIdentityLost, want: routePauseCauseIdentityLost},
		{name: "scope unavailable", err: deploymentexecution.ErrScopeUnavailable, want: routePauseCauseScopeUnavailable},
		{name: "route unresolved", err: &generatedingress.Error{Code: generatedingress.DiagnosticRouteUnresolved}, want: routePauseCauseRouteUnresolved},
	} {
		t.Run(test.name, func(t *testing.T) {
			requirePreviousRetirementPauseAttribution(t, test.err, test.want)
		})
	}
}

func TestGeneratedExecutorPreviousRetirementAvailabilityRoutePauseAttribution(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want routePauseCause
	}{
		{name: "storage unavailable", err: deploymentexecution.ErrUnavailable, want: routePauseCauseStorageUnavailable},
		{name: "unmatched", err: errors.New("unmatched retirement boundary"), want: routePauseCauseUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			requirePreviousRetirementPauseAttribution(t, test.err, test.want)
		})
	}
}

func requirePreviousRetirementPauseAttribution(t *testing.T, refusal error, want routePauseCause) {
	t.Helper()
	const secret = "token=do-not-disclose /private/path"
	fixture := newSealedExecutorFixture(t)
	fixture.capacity.SetCapacity(128<<20, 128<<20)
	checkCompetitor := fixture.capacityCompetitor(t)
	logs := attachRoutePauseTestLogger(fixture.executor)
	blocked := errors.Join(errors.New(secret), refusal)
	fixture.routes.retirementBlock = blocked

	result, err := fixture.execute()
	requireRoutePauseResult(t, result, err)
	requireRoutePauseRecord(t, logs, routePauseStagePreviousRetirement, want)
	runtimeDeployment := fixture.runtimeDeployment(t)
	if fixture.routes.lastActivation.retirement == nil || fixture.routes.retirementCalls != 1 ||
		!errors.Is(fixture.routes.retirementErr, refusal) ||
		fixture.runtime.stopCalls != 0 || fixture.runner.stopCalls != 0 || fixture.runner.removeCalls != 0 ||
		runtimeDeployment.Phase != generatedruntimestate.PhaseDraining ||
		generatedExecutorRetirementReceiptCount(t, fixture) != 0 {
		t.Fatalf("retirement=%d/%v handle=%t stopped=%d docker=%d/%d runtime=%+v receipts=%d",
			fixture.routes.retirementCalls, fixture.routes.retirementErr,
			fixture.routes.lastActivation.retirement != nil, fixture.runtime.stopCalls,
			fixture.runner.stopCalls, fixture.runner.removeCalls, runtimeDeployment,
			generatedExecutorRetirementReceiptCount(t, fixture))
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "/private/path") {
		t.Fatalf("route pause log exposed raw error: %s", logs.String())
	}
	checkCompetitor(true)
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
