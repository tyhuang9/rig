package generatedingress

import (
	"context"
	"errors"
)

type gatewayRebindTypedStageDiagnosticOperation uint8

const (
	gatewayRebindTypedStageDiagnosticOperationUnknown gatewayRebindTypedStageDiagnosticOperation = iota
	gatewayRebindTypedStageDiagnosticOperationStable
	gatewayRebindTypedStageDiagnosticOperationServeStage
	gatewayRebindTypedStageDiagnosticOperationCopyFinalConfig
)

func (operation gatewayRebindTypedStageDiagnosticOperation) String() string {
	switch operation {
	case gatewayRebindTypedStageDiagnosticOperationStable:
		return "stable"
	case gatewayRebindTypedStageDiagnosticOperationServeStage:
		return "serve_stage"
	case gatewayRebindTypedStageDiagnosticOperationCopyFinalConfig:
		return "copy_final_config"
	default:
		return "unknown"
	}
}

type gatewayRebindTypedStageDiagnosticCheckpoint uint8

const (
	gatewayRebindTypedStageDiagnosticCheckpointUnknown gatewayRebindTypedStageDiagnosticCheckpoint = iota
	gatewayRebindTypedStageDiagnosticCheckpointPrecondition
	gatewayRebindTypedStageDiagnosticCheckpointPreEffectGuard
	gatewayRebindTypedStageDiagnosticCheckpointFirstRead
	gatewayRebindTypedStageDiagnosticCheckpointFirstTopology
	gatewayRebindTypedStageDiagnosticCheckpointSecondRead
	gatewayRebindTypedStageDiagnosticCheckpointSecondTopology
	gatewayRebindTypedStageDiagnosticCheckpointStableEquality
	gatewayRebindTypedStageDiagnosticCheckpointPostEffectGuard
	gatewayRebindTypedStageDiagnosticCheckpointResourceShape
	gatewayRebindTypedStageDiagnosticCheckpointConfigRender
	gatewayRebindTypedStageDiagnosticCheckpointStageArchive
	gatewayRebindTypedStageDiagnosticCheckpointStartEffect
	gatewayRebindTypedStageDiagnosticCheckpointCopyEffect
	gatewayRebindTypedStageDiagnosticCheckpointRunningShape
	gatewayRebindTypedStageDiagnosticCheckpointLiveConfig
	gatewayRebindTypedStageDiagnosticCheckpointPublication
	gatewayRebindTypedStageDiagnosticCheckpointFinalArchive
	gatewayRebindTypedStageDiagnosticCheckpointEndpointShape
)

func (checkpoint gatewayRebindTypedStageDiagnosticCheckpoint) String() string {
	switch checkpoint {
	case gatewayRebindTypedStageDiagnosticCheckpointPrecondition:
		return "precondition"
	case gatewayRebindTypedStageDiagnosticCheckpointPreEffectGuard:
		return "pre_effect_guard"
	case gatewayRebindTypedStageDiagnosticCheckpointFirstRead:
		return "first_read"
	case gatewayRebindTypedStageDiagnosticCheckpointFirstTopology:
		return "first_topology"
	case gatewayRebindTypedStageDiagnosticCheckpointSecondRead:
		return "second_read"
	case gatewayRebindTypedStageDiagnosticCheckpointSecondTopology:
		return "second_topology"
	case gatewayRebindTypedStageDiagnosticCheckpointStableEquality:
		return "stable_equality"
	case gatewayRebindTypedStageDiagnosticCheckpointPostEffectGuard:
		return "post_effect_guard"
	case gatewayRebindTypedStageDiagnosticCheckpointResourceShape:
		return "resource_shape"
	case gatewayRebindTypedStageDiagnosticCheckpointConfigRender:
		return "config_render"
	case gatewayRebindTypedStageDiagnosticCheckpointStageArchive:
		return "stage_archive"
	case gatewayRebindTypedStageDiagnosticCheckpointStartEffect:
		return "start_effect"
	case gatewayRebindTypedStageDiagnosticCheckpointCopyEffect:
		return "copy_effect"
	case gatewayRebindTypedStageDiagnosticCheckpointRunningShape:
		return "running_shape"
	case gatewayRebindTypedStageDiagnosticCheckpointLiveConfig:
		return "live_config"
	case gatewayRebindTypedStageDiagnosticCheckpointPublication:
		return "publication"
	case gatewayRebindTypedStageDiagnosticCheckpointFinalArchive:
		return "final_archive"
	case gatewayRebindTypedStageDiagnosticCheckpointEndpointShape:
		return "endpoint_shape"
	default:
		return "unknown"
	}
}

type gatewayRebindTypedStageDiagnosticError struct {
	operation  gatewayRebindTypedStageDiagnosticOperation
	checkpoint gatewayRebindTypedStageDiagnosticCheckpoint
	cause      error
}

func newGatewayRebindTypedStageDiagnosticError(ctx context.Context,
	operation gatewayRebindTypedStageDiagnosticOperation,
	checkpoint gatewayRebindTypedStageDiagnosticCheckpoint,
) error {
	if operation.String() == "unknown" {
		operation = gatewayRebindTypedStageDiagnosticOperationUnknown
	}
	if checkpoint.String() == "unknown" {
		checkpoint = gatewayRebindTypedStageDiagnosticCheckpointUnknown
	}
	return &gatewayRebindTypedStageDiagnosticError{
		operation: operation, checkpoint: checkpoint, cause: gatewayRebindEffectBoundaryError(ctx),
	}
}

func (e *gatewayRebindTypedStageDiagnosticError) Error() string { return e.cause.Error() }
func (e *gatewayRebindTypedStageDiagnosticError) Unwrap() error { return e.cause }

func (e *gatewayRebindTypedStageDiagnosticError) typedStageDiagnostic() (string, string) {
	if e == nil {
		return gatewayRebindTypedStageDiagnosticOperationUnknown.String(),
			gatewayRebindTypedStageDiagnosticCheckpointUnknown.String()
	}
	return e.operation.String(), e.checkpoint.String()
}

func gatewayRebindTypedStageDiagnosticErrorFrom(ctx context.Context, err error) error {
	var diagnostic *gatewayRebindTypedStageDiagnosticError
	if errors.As(err, &diagnostic) && diagnostic != nil {
		return newGatewayRebindTypedStageDiagnosticError(ctx, diagnostic.operation, diagnostic.checkpoint)
	}
	return newGatewayRebindTypedStageDiagnosticError(ctx,
		gatewayRebindTypedStageDiagnosticOperationUnknown, gatewayRebindTypedStageDiagnosticCheckpointUnknown)
}
