package generatedingress

import (
	"context"
	"reflect"
)

// Serving recovery needs an invocation-local authority guard inside the physical
// adapter. Ownership-only adapters must not silently authorize publication.
type gatewayCurrentLANCommitRecoveryPhysicalDriver interface {
	applyGatewayCurrentPhysicalAuthorized(context.Context, gatewayCurrentPhysicalTransition, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
	restoreGatewayCurrentPhysicalAuthorized(context.Context, gatewayCurrentPhysicalTransition, func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

func (m *Manager) publishGatewayCurrentLANRecoveryLocked(ctx context.Context,
	transition gatewayCurrentPhysicalTransition, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if authorize == nil || !validGatewayCurrentPhysicalTransition(transition) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	driver, ok := m.currentPhysicalDriver().(gatewayCurrentLANCommitRecoveryPhysicalDriver)
	if !ok {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	if err := authorize(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	var proof gatewayCurrentPhysicalAttestation
	var err error
	target, outcome := transition.Effective, gatewayCurrentPhysicalRecoveryEffective
	switch transition.Kind {
	case gatewayCurrentPhysicalLANGrant:
		proof, err = driver.applyGatewayCurrentPhysicalAuthorized(ctx, transition, authorize)
	case gatewayCurrentPhysicalLANWithdrawal:
		target, outcome = transition.Before, gatewayCurrentPhysicalRecoveryBefore
		proof, err = driver.restoreGatewayCurrentPhysicalAuthorized(ctx, transition, authorize)
	default:
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	if !validGatewayCurrentPhysicalAttestation(proof) || proof.Outcome != outcome ||
		proof.Lineage != transition.Before.Lineage || !reflect.DeepEqual(proof.State, target) ||
		!reflect.DeepEqual(proof.Pending, transition.Pending.Pending) || proof.LANRecovery != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentRouteOperationError(ctx)
	}
	if err := authorize(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	return proof, nil
}

// Serving revocation must not prevent exact-owned withdrawal. Reconcile the
// retained state before choosing compensation; never reinstall an old revision.
func (m *Manager) withdrawGatewayCurrentLANRecoveryLocked(ctx context.Context,
	transition gatewayCurrentPhysicalTransition, effective gatewayCurrentRouteState,
	request GatewayV2LANGrantRequest,
) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if _, err := m.confirmGatewayCurrentStateLocked(recoveryCtx, transition.Pending); err == nil {
		var withdrawalErr error
		if transition.Kind == gatewayCurrentPhysicalLANGrant {
			_, withdrawalErr = m.restoreGatewayCurrentPhysicalLocked(recoveryCtx, transition)
		} else {
			_, withdrawalErr = m.applyGatewayCurrentPhysicalLocked(recoveryCtx, transition)
		}
		if withdrawalErr == nil {
			return gatewayCurrentRouteOperationError(ctx)
		}
	} else if _, err := m.confirmGatewayCurrentStateLocked(recoveryCtx, effective); err == nil {
		if err := m.quarantineGatewayCurrentGrantLocked(recoveryCtx, effective, request); err != nil {
			return err
		}
		return gatewayCurrentRouteOperationError(ctx)
	}
	if _, err := m.stopGatewayCurrentPhysicalLocked(recoveryCtx, transition); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	return gatewayCurrentRouteOperationError(ctx)
}

// An uncertain initial withdrawal cannot rely on a serving callback. Stop only
// the exact retained transition, and preserve uncertainty if even that is unproved.
func (m *Manager) stopGatewayCurrentLANRecoveryLocked(ctx context.Context,
	transition gatewayCurrentPhysicalTransition,
) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v2ObservationTimeout)
	defer cancel()
	if _, err := m.stopGatewayCurrentPhysicalLocked(stopCtx, transition); err != nil {
		return &Error{Code: DiagnosticRouteUnresolved, candidateMayBeLive: true}
	}
	return gatewayCurrentRouteOperationError(ctx)
}
