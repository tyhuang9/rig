package generatedingress

import (
	"context"
	"reflect"
)

// The caller owns the recovery locks and supplies phase-specific authority.
// This seam observes only; it cannot repair configuration or restart serving.
type gatewayRebindCurrentAttestationDriver interface {
	attestGatewayRebindCurrent(context.Context, gatewayCurrentPhysicalTarget,
		func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

func (m *Manager) gatewayRebindCurrentObserver() gatewayRebindCurrentAttestationDriver {
	if m == nil {
		return nil
	}
	if m.gatewayCurrentPhysicalDriver != nil {
		driver, _ := m.gatewayCurrentPhysicalDriver.(gatewayRebindCurrentAttestationDriver)
		return driver
	}
	// Preserve the rebind coordinator's production observation capability
	// without installing a current driver or opening any effect factory.
	driver, _ := newManagedGatewayCurrentPhysicalDriver(m).(gatewayRebindCurrentAttestationDriver)
	return driver
}

func (d managedGatewayCurrentPhysicalDriver) attestGatewayRebindCurrent(ctx context.Context,
	target gatewayCurrentPhysicalTarget, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	invalid := func(checkpoint gatewayCurrentAttestationCheckpoint) (gatewayCurrentPhysicalAttestation, error) {
		return gatewayCurrentPhysicalAttestation{}, newGatewayCurrentAttestationError(ctx, checkpoint)
	}
	invalidFrom := func(err error,
		checkpoint gatewayCurrentAttestationCheckpoint,
	) (gatewayCurrentPhysicalAttestation, error) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentAttestationErrorFrom(ctx, err, checkpoint)
	}
	if d.manager() == nil || d.runtime == nil || ctx == nil || ctx.Err() != nil || guard == nil ||
		!validGatewayCurrentPhysicalTarget(target) || target.Pending != nil || target.LANRecovery != nil {
		return invalid(gatewayCurrentAttestationCheckpointDriverPrecondition)
	}
	if err := guard(ctx); err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointDriverInitialGuard)
	}
	first, err := d.runtime.observe(ctx, target)
	if err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointDriverFirstObservation)
	}
	if !gatewayCurrentServingRestoreProofMatches(first, target, gatewayCurrentPhysicalStableServing) {
		return invalid(gatewayCurrentAttestationCheckpointDriverFirstProof)
	}
	if err := guard(ctx); err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointDriverFirstGuard)
	}
	second, err := d.runtime.observe(ctx, target)
	if err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointDriverSecondObservation)
	}
	if !gatewayCurrentServingRestoreProofMatches(second, target, gatewayCurrentPhysicalStableServing) {
		return invalid(gatewayCurrentAttestationCheckpointDriverSecondProof)
	}
	if !reflect.DeepEqual(first, second) {
		return invalid(gatewayCurrentAttestationCheckpointDriverStability)
	}
	if err := guard(ctx); err != nil {
		return invalidFrom(err, gatewayCurrentAttestationCheckpointDriverSecondGuard)
	}
	return second, nil
}

var _ gatewayRebindCurrentAttestationDriver = managedGatewayCurrentPhysicalDriver{}
