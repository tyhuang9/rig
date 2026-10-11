package generatedingress

import "context"

// gatewayCurrentAttestationCheckpoint is private, finite diagnostic evidence.
// It never changes the public error code or text and never retains an upstream
// error, Docker output, configuration, or authority value.
type gatewayCurrentAttestationCheckpoint uint8

const (
	gatewayCurrentAttestationCheckpointUnknown gatewayCurrentAttestationCheckpoint = iota
	gatewayCurrentAttestationCheckpointManagerPrecondition
	gatewayCurrentAttestationCheckpointManagerAuthority
	gatewayCurrentAttestationCheckpointManagerForwardBoundary
	gatewayCurrentAttestationCheckpointManagerServingCensus
	gatewayCurrentAttestationCheckpointManagerHistoryStability
	gatewayCurrentAttestationCheckpointManagerPreDriverGuard
	gatewayCurrentAttestationCheckpointManagerDriver
	gatewayCurrentAttestationCheckpointManagerProof
	gatewayCurrentAttestationCheckpointManagerPostDriverGuard
	gatewayCurrentAttestationCheckpointDriverPrecondition
	gatewayCurrentAttestationCheckpointDriverInitialGuard
	gatewayCurrentAttestationCheckpointDriverFirstObservation
	gatewayCurrentAttestationCheckpointDriverFirstProof
	gatewayCurrentAttestationCheckpointDriverFirstGuard
	gatewayCurrentAttestationCheckpointDriverSecondObservation
	gatewayCurrentAttestationCheckpointDriverSecondProof
	gatewayCurrentAttestationCheckpointDriverStability
	gatewayCurrentAttestationCheckpointDriverSecondGuard
	gatewayCurrentAttestationCheckpointRuntimePrecondition
	gatewayCurrentAttestationCheckpointRuntimeStability
	gatewayCurrentAttestationCheckpointRuntimeInventoryPrecondition
	gatewayCurrentAttestationCheckpointRuntimeImage
	gatewayCurrentAttestationCheckpointRuntimeConfigVolume
	gatewayCurrentAttestationCheckpointRuntimeDataVolume
	gatewayCurrentAttestationCheckpointRuntimeIngressNetwork
	gatewayCurrentAttestationCheckpointRuntimeStageAbsence
	gatewayCurrentAttestationCheckpointRuntimeOwnedCensus
	gatewayCurrentAttestationCheckpointRuntimeFinalContainer
	gatewayCurrentAttestationCheckpointRuntimeIngressMembership
	gatewayCurrentAttestationCheckpointRuntimeApplicationNetworks
	gatewayCurrentAttestationCheckpointRuntimeEndpointProof
	gatewayCurrentAttestationCheckpointRuntimeRestartConfig
	gatewayCurrentAttestationCheckpointRuntimeLiveConfig
	gatewayCurrentAttestationCheckpointRuntimeConfigRender
	gatewayCurrentAttestationCheckpointRuntimeStoppedListener
	gatewayCurrentAttestationCheckpointRuntimeActiveConfig
	gatewayCurrentAttestationCheckpointRuntimeRestartMatch
	gatewayCurrentAttestationCheckpointRuntimeServingProbes
	gatewayCurrentAttestationCheckpointRuntimeProofInput
	gatewayCurrentAttestationCheckpointRuntimeProofDigest
)

func (checkpoint gatewayCurrentAttestationCheckpoint) String() string {
	switch checkpoint {
	case gatewayCurrentAttestationCheckpointManagerPrecondition:
		return "manager_precondition"
	case gatewayCurrentAttestationCheckpointManagerAuthority:
		return "manager_authority"
	case gatewayCurrentAttestationCheckpointManagerForwardBoundary:
		return "manager_forward_boundary"
	case gatewayCurrentAttestationCheckpointManagerServingCensus:
		return "manager_serving_census"
	case gatewayCurrentAttestationCheckpointManagerHistoryStability:
		return "manager_history_stability"
	case gatewayCurrentAttestationCheckpointManagerPreDriverGuard:
		return "manager_pre_driver_guard"
	case gatewayCurrentAttestationCheckpointManagerDriver:
		return "manager_driver"
	case gatewayCurrentAttestationCheckpointManagerProof:
		return "manager_proof"
	case gatewayCurrentAttestationCheckpointManagerPostDriverGuard:
		return "manager_post_driver_guard"
	case gatewayCurrentAttestationCheckpointDriverPrecondition:
		return "driver_precondition"
	case gatewayCurrentAttestationCheckpointDriverInitialGuard:
		return "driver_initial_guard"
	case gatewayCurrentAttestationCheckpointDriverFirstObservation:
		return "driver_first_observation"
	case gatewayCurrentAttestationCheckpointDriverFirstProof:
		return "driver_first_proof"
	case gatewayCurrentAttestationCheckpointDriverFirstGuard:
		return "driver_first_guard"
	case gatewayCurrentAttestationCheckpointDriverSecondObservation:
		return "driver_second_observation"
	case gatewayCurrentAttestationCheckpointDriverSecondProof:
		return "driver_second_proof"
	case gatewayCurrentAttestationCheckpointDriverStability:
		return "driver_stability"
	case gatewayCurrentAttestationCheckpointDriverSecondGuard:
		return "driver_second_guard"
	case gatewayCurrentAttestationCheckpointRuntimePrecondition:
		return "runtime_precondition"
	case gatewayCurrentAttestationCheckpointRuntimeStability:
		return "runtime_stability"
	case gatewayCurrentAttestationCheckpointRuntimeInventoryPrecondition:
		return "runtime_inventory_precondition"
	case gatewayCurrentAttestationCheckpointRuntimeImage:
		return "runtime_image"
	case gatewayCurrentAttestationCheckpointRuntimeConfigVolume:
		return "runtime_config_volume"
	case gatewayCurrentAttestationCheckpointRuntimeDataVolume:
		return "runtime_data_volume"
	case gatewayCurrentAttestationCheckpointRuntimeIngressNetwork:
		return "runtime_ingress_network"
	case gatewayCurrentAttestationCheckpointRuntimeStageAbsence:
		return "runtime_stage_absence"
	case gatewayCurrentAttestationCheckpointRuntimeOwnedCensus:
		return "runtime_owned_census"
	case gatewayCurrentAttestationCheckpointRuntimeFinalContainer:
		return "runtime_final_container"
	case gatewayCurrentAttestationCheckpointRuntimeIngressMembership:
		return "runtime_ingress_membership"
	case gatewayCurrentAttestationCheckpointRuntimeApplicationNetworks:
		return "runtime_application_networks"
	case gatewayCurrentAttestationCheckpointRuntimeEndpointProof:
		return "runtime_endpoint_proof"
	case gatewayCurrentAttestationCheckpointRuntimeRestartConfig:
		return "runtime_restart_config"
	case gatewayCurrentAttestationCheckpointRuntimeLiveConfig:
		return "runtime_live_config"
	case gatewayCurrentAttestationCheckpointRuntimeConfigRender:
		return "runtime_config_render"
	case gatewayCurrentAttestationCheckpointRuntimeStoppedListener:
		return "runtime_stopped_listener"
	case gatewayCurrentAttestationCheckpointRuntimeActiveConfig:
		return "runtime_active_config"
	case gatewayCurrentAttestationCheckpointRuntimeRestartMatch:
		return "runtime_restart_match"
	case gatewayCurrentAttestationCheckpointRuntimeServingProbes:
		return "runtime_serving_probes"
	case gatewayCurrentAttestationCheckpointRuntimeProofInput:
		return "runtime_proof_input"
	case gatewayCurrentAttestationCheckpointRuntimeProofDigest:
		return "runtime_proof_digest"
	default:
		return "unknown"
	}
}

func (checkpoint gatewayCurrentAttestationCheckpoint) valid() bool {
	return checkpoint != gatewayCurrentAttestationCheckpointUnknown && checkpoint.String() != "unknown"
}

func newGatewayCurrentAttestationError(ctx context.Context,
	checkpoint gatewayCurrentAttestationCheckpoint,
) *Error {
	code := DiagnosticRouteUnresolved
	if ctx != nil && ctx.Err() != nil {
		code = DiagnosticCancelled
	}
	if !checkpoint.valid() {
		checkpoint = gatewayCurrentAttestationCheckpointUnknown
	}
	return &Error{Code: code, currentAttestationCheckpoint: checkpoint}
}

func gatewayCurrentAttestationErrorFrom(ctx context.Context, err error,
	fallback gatewayCurrentAttestationCheckpoint,
) *Error {
	checkpoint := gatewayCurrentAttestationCheckpointFromError(err)
	if !checkpoint.valid() {
		checkpoint = fallback
	}
	return newGatewayCurrentAttestationError(ctx, checkpoint)
}

func gatewayCurrentAttestationCheckpointFromError(err error) gatewayCurrentAttestationCheckpoint {
	value, ok := err.(*Error)
	if !ok || value == nil || !value.currentAttestationCheckpoint.valid() {
		return gatewayCurrentAttestationCheckpointUnknown
	}
	return value.currentAttestationCheckpoint
}
