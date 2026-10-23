package generatedexecutor

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedruntime"
	"github.com/hostd/hostd/internal/generatedruntimestate"
)

const routePauseMessage = "generated route reconciliation paused"

type routePauseStage string

const (
	routePauseStageRuntimeBegin          routePauseStage = "runtime_begin"
	routePauseStageRuntimeSuccessHead    routePauseStage = "runtime_success_head"
	routePauseStageRouteSwitch           routePauseStage = "route_switch"
	routePauseStagePreviousRetirement    routePauseStage = "previous_retirement"
	routePauseStageRuntimeSuccessAdvance routePauseStage = "runtime_success_advance"
)

type routePauseCause string

const (
	routePauseCauseUnknown                routePauseCause = "unknown"
	routePauseCauseDeploymentInProgress   routePauseCause = "deployment_in_progress"
	routePauseCauseActiveHeadMismatch     routePauseCause = "active_head_mismatch"
	routePauseCauseGatewayReadinessFailed routePauseCause = "gateway_readiness_failed"
	routePauseCauseRetirementUnavailable  routePauseCause = "retirement_unavailable"
	routePauseCauseCandidateCleanupFailed routePauseCause = "candidate_cleanup_failed"
	routePauseCauseCanceled               routePauseCause = "canceled"
	routePauseCauseDeadlineExceeded       routePauseCause = "deadline_exceeded"
	routePauseCauseStateNotFound          routePauseCause = "state_not_found"
	routePauseCauseInvalidState           routePauseCause = "invalid_state"
	routePauseCauseInvalidTransition      routePauseCause = "invalid_transition"
	routePauseCauseStateConflict          routePauseCause = "state_conflict"
)

func (e *Executor) observeRoutePause(ctx context.Context, stage routePauseStage, cause routePauseCause) {
	if e == nil || e.options.RoutePauseLogger == nil || !knownRoutePauseStage(stage) {
		return
	}
	if !knownRoutePausePair(stage, cause) {
		cause = routePauseCauseUnknown
	}
	e.options.RoutePauseLogger.LogAttrs(ctx, slog.LevelWarn, routePauseMessage,
		slog.String("route_pause_stage", string(stage)),
		slog.String("route_pause_cause", string(cause)),
	)
}

func (e *Executor) observeRoutePauseError(ctx context.Context, stage routePauseStage, err error) {
	e.observeRoutePause(ctx, stage, classifyRoutePauseCause(err))
}

func classifyRoutePauseCause(err error) routePauseCause {
	var sealed interface{ routePauseCause() routePauseCause }
	if errors.As(err, &sealed) {
		cause := sealed.routePauseCause()
		if cause == routePauseCauseGatewayReadinessFailed || knownRoutePauseStateCause(cause) {
			return cause
		}
		return routePauseCauseUnknown
	}
	switch {
	case errors.Is(err, deployments.ErrGeneratedCandidateRetirementUnavailable):
		return routePauseCauseRetirementUnavailable
	case generatedruntime.IsCode(err, generatedruntime.DiagnosticCandidateCleanupFailed):
		return routePauseCauseCandidateCleanupFailed
	case generatedruntime.RouteGatewayReadinessFailed(err):
		return routePauseCauseGatewayReadinessFailed
	case errors.Is(err, context.Canceled):
		return routePauseCauseCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return routePauseCauseDeadlineExceeded
	case errors.Is(err, generatedruntimestate.ErrNotFound):
		return routePauseCauseStateNotFound
	case errors.Is(err, generatedruntimestate.ErrInvalidState):
		return routePauseCauseInvalidState
	case errors.Is(err, generatedruntimestate.ErrInvalidTransition):
		return routePauseCauseInvalidTransition
	case errors.Is(err, generatedruntimestate.ErrConflict):
		return routePauseCauseStateConflict
	default:
		return routePauseCauseUnknown
	}
}

func knownRoutePauseStage(stage routePauseStage) bool {
	switch stage {
	case routePauseStageRuntimeBegin,
		routePauseStageRuntimeSuccessHead,
		routePauseStageRouteSwitch,
		routePauseStagePreviousRetirement,
		routePauseStageRuntimeSuccessAdvance:
		return true
	default:
		return false
	}
}

func knownRoutePausePair(stage routePauseStage, cause routePauseCause) bool {
	if cause == routePauseCauseUnknown {
		return true
	}
	switch stage {
	case routePauseStageRuntimeBegin:
		return cause == routePauseCauseDeploymentInProgress
	case routePauseStageRuntimeSuccessHead:
		return cause == routePauseCauseActiveHeadMismatch || knownRoutePauseStateCause(cause)
	case routePauseStageRouteSwitch:
		return cause == routePauseCauseGatewayReadinessFailed || knownRoutePauseStateCause(cause)
	case routePauseStagePreviousRetirement:
		return cause == routePauseCauseRetirementUnavailable ||
			cause == routePauseCauseCandidateCleanupFailed || knownRoutePauseStateCause(cause)
	case routePauseStageRuntimeSuccessAdvance:
		return knownRoutePauseStateCause(cause)
	default:
		return false
	}
}

func knownRoutePauseStateCause(cause routePauseCause) bool {
	switch cause {
	case routePauseCauseCanceled,
		routePauseCauseDeadlineExceeded,
		routePauseCauseStateNotFound,
		routePauseCauseInvalidState,
		routePauseCauseInvalidTransition,
		routePauseCauseStateConflict:
		return true
	default:
		return false
	}
}
