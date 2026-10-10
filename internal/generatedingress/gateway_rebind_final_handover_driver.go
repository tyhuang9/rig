package generatedingress

import (
	"context"
	"errors"
	"strings"
)

// Production effects are private and require the same complete observation as
// the coordinator. A name may locate an object for inspection, but only an
// immutable recorded ID may authorize a container or network mutation.
type managerGatewayRebindFinalHandoverDriver struct {
	managerGatewayRebindFinalConfigCopyDriver
	reads         gatewayRebindSuccessorPreflightReads
	inspectDocker gatewayRebindDockerInspector
}

func newManagerGatewayRebindFinalHandoverDriver(m *Manager, reads gatewayRebindSuccessorPreflightReads,
	inspectDocker gatewayRebindDockerInspector,
) managerGatewayRebindFinalHandoverDriver {
	return managerGatewayRebindFinalHandoverDriver{managerGatewayRebindFinalConfigCopyDriver{manager: m}, reads, inspectDocker}
}

func (d managerGatewayRebindFinalHandoverDriver) observeHandover(ctx context.Context,
	value gatewayRebindFinalHandoverContext,
) (gatewayRebindFinalHandoverObservation, error) {
	return observeGatewayRebindHandoverStable(ctx, func() (gatewayRebindFinalHandoverObservation, error) {
		return d.readHandover(ctx, value)
	})
}

func (d managerGatewayRebindFinalHandoverDriver) createFinal(ctx context.Context,
	value gatewayRebindFinalHandoverContext,
) (string, error) {
	if value.Phase != gatewayRebindProgressFinalHandoverIntent || value.Final != nil || value.CreatedFinalID != "" {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	proof, err := d.observeHandover(ctx, value)
	if err != nil || value.Plan == nil || proof.Stage != gatewayRebindHandoverContainerAbsent ||
		proof.Final != gatewayRebindHandoverContainerAbsent || !proof.ConfigVolumePresent || !proof.DataVolumePresent ||
		!proof.IngressNetworkPresent || proof.PredecessorObservationDigest != value.Plan.PredecessorObservationDigest {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	args, err := gatewayRebindFinalContainerCreateArgs(value)
	if err != nil {
		return "", err
	}
	result, err := d.manager.run(ctx, d.manager.options.CommandTimeout, args...)
	defer clearResult(&result)
	if err != nil || ctx.Err() != nil {
		return "", gatewayRebindEffectBoundaryError(ctx)
	}
	id := strings.TrimSpace(string(result.Stdout))
	if _, err := gatewayRebindFinalContainerBindingFor(value, id); err != nil {
		return "", err
	}
	return id, nil
}

func (d managerGatewayRebindFinalHandoverDriver) guardedEffect(ctx context.Context,
	value gatewayRebindFinalHandoverContext, allowed func(gatewayRebindFinalHandoverObservation) bool, args ...string,
) error {
	if value.CreatedFinalID != "" || value.Plan == nil || !validGatewayRebindFinalHandoverPlan(value, *value.Plan) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	proof, err := d.observeHandover(ctx, value)
	if err != nil || !allowed(proof) || ctx.Err() != nil {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return d.manager.runDiscard(ctx, d.manager.options.CommandTimeout, args...)
}

func gatewayRebindHandoverRollbackPhase(value gatewayRebindFinalHandoverContext) bool {
	return value.Phase == gatewayRebindProgressRollbackIntent
}

func (d managerGatewayRebindFinalHandoverDriver) stopStage(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	if !validGatewayRebindFinalHandoverBase(value) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return (value.Phase == gatewayRebindProgressFinalHandoverIntent || gatewayRebindHandoverRollbackPhase(value)) &&
			proof.Stage == gatewayRebindHandoverContainerRunning && proof.Final == gatewayRebindHandoverContainerAbsent &&
			proof.PredecessorObservationDigest == value.Plan.PredecessorObservationDigest
	}, "container", "stop", "--time", "10", value.SequenceTwelve.Stage.StageContainer.ID)
}

func (d managerGatewayRebindFinalHandoverDriver) removeStage(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	if !validGatewayRebindFinalHandoverBase(value) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return (value.Phase == gatewayRebindProgressFinalHandoverIntent || gatewayRebindHandoverRollbackPhase(value)) &&
			proof.Stage == gatewayRebindHandoverContainerStopped && proof.Final == gatewayRebindHandoverContainerAbsent &&
			proof.PredecessorObservationDigest == value.Plan.PredecessorObservationDigest
	}, "container", "rm", value.SequenceTwelve.Stage.StageContainer.ID)
}

func (d managerGatewayRebindFinalHandoverDriver) stopPredecessor(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return value.Phase == gatewayRebindProgressCutoverIntent && proof.PredecessorRunning &&
			proof.Stage == gatewayRebindHandoverContainerAbsent && proof.Final == gatewayRebindHandoverContainerStopped &&
			proof.PredecessorObservationDigest == value.Plan.PredecessorObservationDigest
	}, "container", "stop", "--time", "10", value.Predecessor.Journal.Resources.FinalContainerID)
}

func (d managerGatewayRebindFinalHandoverDriver) startPredecessor(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return gatewayRebindHandoverRollbackPhase(value) && !proof.PredecessorRunning &&
			proof.PredecessorAddress == gatewayRebindPredecessorAddressPresent &&
			proof.Stage == gatewayRebindHandoverContainerAbsent && proof.Final != gatewayRebindHandoverContainerRunning
	}, "container", "start", value.Predecessor.Journal.Resources.FinalContainerID)
}

func (d managerGatewayRebindFinalHandoverDriver) finalEffect(ctx context.Context, value gatewayRebindFinalHandoverContext,
	allowed func(gatewayRebindFinalHandoverObservation) bool, args ...string,
) error {
	if value.Final == nil || !validGatewayRebindFinalContainerBinding(value, *value.Final) {
		return gatewayRebindEffectBoundaryError(ctx)
	}
	return d.guardedEffect(ctx, value, allowed, append(args, value.Final.ID)...)
}

func (d managerGatewayRebindFinalHandoverDriver) startFinal(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.finalEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return value.Phase == gatewayRebindProgressCutoverIntent && !proof.PredecessorRunning &&
			proof.Stage == gatewayRebindHandoverContainerAbsent && proof.Final == gatewayRebindHandoverContainerStopped
	}, "container", "start")
}

func (d managerGatewayRebindFinalHandoverDriver) stopFinal(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.finalEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return gatewayRebindHandoverRollbackPhase(value) && proof.Final == gatewayRebindHandoverContainerRunning
	}, "container", "stop", "--time", "10")
}

func (d managerGatewayRebindFinalHandoverDriver) removeFinal(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.finalEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return gatewayRebindHandoverRollbackPhase(value) && proof.Final == gatewayRebindHandoverContainerStopped
	}, "container", "rm")
}

func (d managerGatewayRebindFinalHandoverDriver) removeConfigVolume(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return gatewayRebindHandoverRollbackPhase(value) && proof.Stage == gatewayRebindHandoverContainerAbsent &&
			proof.Final == gatewayRebindHandoverContainerAbsent && proof.ConfigVolumePresent
	}, "volume", "rm", value.Intent.Intent.Identity.ConfigVolume)
}

func (d managerGatewayRebindFinalHandoverDriver) removeDataVolume(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return gatewayRebindHandoverRollbackPhase(value) && proof.Stage == gatewayRebindHandoverContainerAbsent &&
			proof.Final == gatewayRebindHandoverContainerAbsent && proof.DataVolumePresent
	}, "volume", "rm", value.Intent.Intent.Identity.DataVolume)
}

func (d managerGatewayRebindFinalHandoverDriver) removeIngressNetwork(ctx context.Context, value gatewayRebindFinalHandoverContext) error {
	if !validGatewayRebindFinalHandoverBase(value) {
		return errors.New("invalid generated ingress rebind handover network")
	}
	return d.guardedEffect(ctx, value, func(proof gatewayRebindFinalHandoverObservation) bool {
		return gatewayRebindHandoverRollbackPhase(value) && proof.Stage == gatewayRebindHandoverContainerAbsent &&
			proof.Final == gatewayRebindHandoverContainerAbsent && !proof.ConfigVolumePresent && !proof.DataVolumePresent && proof.IngressNetworkPresent
	}, "network", "rm", value.SequenceTwelve.Stage.Network.ID)
}

var _ gatewayRebindFinalHandoverDriver = managerGatewayRebindFinalHandoverDriver{}
