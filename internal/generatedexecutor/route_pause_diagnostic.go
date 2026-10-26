package generatedexecutor

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hostd/hostd/internal/deploymentexecution"
	"github.com/hostd/hostd/internal/deployments"
	"github.com/hostd/hostd/internal/generatedingress"
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
	routePauseCauseInvalidClaim           routePauseCause = "invalid_claim"
	routePauseCauseInvalidConfiguration   routePauseCause = "invalid_configuration"
	routePauseCauseScopeUnavailable       routePauseCause = "scope_unavailable"
	routePauseCauseAuthorityStale         routePauseCause = "authority_stale"
	routePauseCauseAuthorityInvalid       routePauseCause = "authority_invalid"
	routePauseCauseAuthorityUnresolved    routePauseCause = "authority_unresolved"
	routePauseCauseIdentityLost           routePauseCause = "identity_lost"
	routePauseCauseStageInvalid           routePauseCause = "stage_invalid"
	routePauseCausePositiveFence          routePauseCause = "positive_fence"
	routePauseCauseStorageUnavailable     routePauseCause = "storage_unavailable"
	routePauseCauseRouteUnresolved        routePauseCause = "route_unresolved"
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
	case errors.Is(err, deploymentexecution.ErrCanceled):
		return routePauseCauseCanceled
	default:
		return classifyPreviousRetirementIdentity(err)
	}
}

func classifyPreviousRetirementIdentity(err error) routePauseCause {
	identities := []struct {
		match bool
		cause routePauseCause
	}{
		{match: generatedIngressCodeInErrorTree(err, generatedingress.DiagnosticRouteUnresolved), cause: routePauseCauseRouteUnresolved},
		{match: errors.Is(err, deploymentexecution.ErrInvalidClaim), cause: routePauseCauseInvalidClaim},
		{match: errors.Is(err, deploymentexecution.ErrInvalidConfiguration), cause: routePauseCauseInvalidConfiguration},
		{match: errors.Is(err, deploymentexecution.ErrScopeUnavailable), cause: routePauseCauseScopeUnavailable},
		{match: errors.Is(err, deploymentexecution.ErrAuthorityStale), cause: routePauseCauseAuthorityStale},
		{match: errors.Is(err, deploymentexecution.ErrAuthorityInvalid), cause: routePauseCauseAuthorityInvalid},
		{match: errors.Is(err, deploymentexecution.ErrAuthorityUnresolved), cause: routePauseCauseAuthorityUnresolved},
		{match: errors.Is(err, deploymentexecution.ErrIdentityLost), cause: routePauseCauseIdentityLost},
		{match: errors.Is(err, deploymentexecution.ErrStageInvalid), cause: routePauseCauseStageInvalid},
		{match: errors.Is(err, deploymentexecution.ErrPositiveFence), cause: routePauseCausePositiveFence},
	}
	var matched routePauseCause
	count := 0
	for _, identity := range identities {
		if !identity.match {
			continue
		}
		matched = identity.cause
		count++
	}
	if count == 1 {
		return matched
	}
	if count > 1 {
		return routePauseCauseUnknown
	}
	if errors.Is(err, deploymentexecution.ErrUnavailable) {
		return routePauseCauseStorageUnavailable
	}
	return routePauseCauseUnknown
}

func generatedIngressCodeInErrorTree(err error, code generatedingress.DiagnosticCode) bool {
	if err == nil {
		return false
	}
	if candidate, ok := err.(*generatedingress.Error); ok && candidate != nil && candidate.Code == code {
		return true
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, item := range wrapped.Unwrap() {
			if generatedIngressCodeInErrorTree(item, code) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return generatedIngressCodeInErrorTree(wrapped.Unwrap(), code)
	}
	return false
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
			cause == routePauseCauseCandidateCleanupFailed || knownRoutePauseStateCause(cause) ||
			knownPreviousRetirementIdentityCause(cause)
	case routePauseStageRuntimeSuccessAdvance:
		return knownRoutePauseStateCause(cause)
	default:
		return false
	}
}

func knownPreviousRetirementIdentityCause(cause routePauseCause) bool {
	switch cause {
	case routePauseCauseInvalidClaim,
		routePauseCauseInvalidConfiguration,
		routePauseCauseScopeUnavailable,
		routePauseCauseAuthorityStale,
		routePauseCauseAuthorityInvalid,
		routePauseCauseAuthorityUnresolved,
		routePauseCauseIdentityLost,
		routePauseCauseStageInvalid,
		routePauseCausePositiveFence,
		routePauseCauseStorageUnavailable,
		routePauseCauseRouteUnresolved:
		return true
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
