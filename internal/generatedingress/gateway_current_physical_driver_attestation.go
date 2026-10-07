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
	invalid := func() (gatewayCurrentPhysicalAttestation, error) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if d.manager() == nil || d.runtime == nil || ctx == nil || ctx.Err() != nil || guard == nil ||
		!validGatewayCurrentPhysicalTarget(target) || target.Pending != nil || target.LANRecovery != nil || guard(ctx) != nil {
		return invalid()
	}
	first, err := d.runtime.observe(ctx, target)
	if err != nil || !gatewayCurrentServingRestoreProofMatches(first, target, gatewayCurrentPhysicalStableServing) || guard(ctx) != nil {
		return invalid()
	}
	second, err := d.runtime.observe(ctx, target)
	if err != nil || !gatewayCurrentServingRestoreProofMatches(second, target, gatewayCurrentPhysicalStableServing) ||
		!reflect.DeepEqual(first, second) || guard(ctx) != nil {
		return invalid()
	}
	return second, nil
}

var _ gatewayRebindCurrentAttestationDriver = managedGatewayCurrentPhysicalDriver{}
