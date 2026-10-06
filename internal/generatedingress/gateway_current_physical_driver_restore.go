package generatedingress

import (
	"context"
	"reflect"
)

// gatewayCurrentServingPhysicalRuntime is the narrow publication primitive
// used by startup serving restoration. A running container is accepted only
// when it already has the exact authorized topology. All publication effects
// are limited to an exact-owned stopped container.
type gatewayCurrentServingPhysicalRuntime interface {
	restoreServing(context.Context, gatewayCurrentPhysicalTarget, gatewayCurrentPhysicalOutcome,
		func(context.Context) error) (gatewayCurrentPhysicalAttestation, error)
}

func (d managedGatewayCurrentPhysicalDriver) restoreGatewayCurrentServing(ctx context.Context,
	action gatewayCurrentServingRestoreAction, authorize func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if authorize == nil || !validGatewayCurrentServingRestoreAction(action) || ctx == nil || ctx.Err() != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	_, target, err := d.selectGatewayCurrentServingRestoreExact(ctx, action)
	if err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	expected, ok := gatewayCurrentServingRestoreExpectedOutcome(action)
	if !ok {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	guard := func(effectCtx context.Context) error {
		if effectCtx == nil || effectCtx.Err() != nil {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		if err := authorize(effectCtx); err != nil {
			return err
		}
		_, fresh, err := d.selectGatewayCurrentServingRestoreExact(effectCtx, action)
		if err != nil || !reflect.DeepEqual(fresh, target) {
			return gatewayCurrentPhysicalDriverError(effectCtx)
		}
		return nil
	}
	return d.restoreGatewayCurrentServingAtTarget(ctx, target, expected, guard)
}

// Both serving consumers construct a purpose-specific exact-selection guard
// before reaching this physical primitive. Neither may bypass admission.
func (d managedGatewayCurrentPhysicalDriver) restoreGatewayCurrentServingAtTarget(ctx context.Context,
	target gatewayCurrentPhysicalTarget, expected gatewayCurrentPhysicalOutcome, guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	physical, ok := d.runtime.(gatewayCurrentServingPhysicalRuntime)
	if !ok || guard == nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proof, restoreErr := physical.restoreServing(ctx, target, expected, guard)
	if restoreErr == nil && !gatewayCurrentServingRestoreProofMatches(proof, target, expected) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}

	// A start or config-copy error can be a lost acknowledgement. Resolve it
	// only with a fresh bounded observation under the complete SQL/protected
	// authorization callback and an exact selected-action recheck.
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager())
	defer cancel()
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	confirmed, err := d.runtime.observe(proofCtx, target)
	if err != nil || !gatewayCurrentServingRestoreProofMatches(confirmed, target, expected) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	if restoreErr == nil && !reflect.DeepEqual(proof, confirmed) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return confirmed, nil
}

func (d managedGatewayCurrentPhysicalDriver) selectGatewayCurrentServingRestoreExact(ctx context.Context,
	action gatewayCurrentServingRestoreAction,
) (gatewayCurrentSelection, gatewayCurrentPhysicalTarget, error) {
	if !validGatewayCurrentServingRestoreAction(action) {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	selection, err := d.selectExact(ctx, action.Selected)
	if err != nil {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, err
	}
	actual, err := gatewayCurrentServingRestoreActionForSelection(selection, action.AuthorizationDigest)
	if err != nil || !reflect.DeepEqual(actual, action) {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	target, err := gatewayCurrentPhysicalTargetFor(selection, action.Target, nil, action.Selected.LANRecovery)
	if err != nil {
		return gatewayCurrentSelection{}, gatewayCurrentPhysicalTarget{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	return selection, target, nil
}

func gatewayCurrentServingRestoreExpectedOutcome(action gatewayCurrentServingRestoreAction) (
	gatewayCurrentPhysicalOutcome, bool,
) {
	switch action.Mode {
	case gatewayCurrentServingRestoreStable:
		return gatewayCurrentPhysicalStableServing, true
	case gatewayCurrentServingRestoreCompletedBatch:
		return gatewayCurrentPhysicalRecoveryMixed, true
	default:
		return "", false
	}
}

func gatewayCurrentServingRestoreProofMatches(proof gatewayCurrentPhysicalAttestation,
	target gatewayCurrentPhysicalTarget, expected gatewayCurrentPhysicalOutcome,
) bool {
	return gatewayCurrentPhysicalAttestationAtTarget(proof, target) && proof.Outcome == expected &&
		!proof.Runtime.ListenerAbsent
}

func (d managerGatewayCurrentPhysicalRuntime) restoreServing(ctx context.Context,
	target gatewayCurrentPhysicalTarget, expected gatewayCurrentPhysicalOutcome,
	guard func(context.Context) error,
) (gatewayCurrentPhysicalAttestation, error) {
	if d.manager == nil || guard == nil || !validGatewayCurrentPhysicalTarget(target) ||
		(expected != gatewayCurrentPhysicalStableServing && expected != gatewayCurrentPhysicalRecoveryMixed) ||
		ctx == nil || ctx.Err() != nil {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(ctx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proof, err := d.observe(ctx, target)
	if err == nil && gatewayCurrentServingRestoreProofMatches(proof, target, expected) {
		if err := guard(ctx); err != nil {
			return gatewayCurrentPhysicalAttestation{}, err
		}
		return proof, nil
	}

	// Serving restore is intentionally narrower than ordinary reconciliation:
	// an inexact running container is never rewritten in place. Only an exact
	// terminal-owned stopped container with a known authorized restart file and
	// exact desired networks may be copied/started.
	stopped, exactRestart, inventoryErr := d.stoppedRecoveryInventory(ctx, target)
	if inventoryErr != nil || !stopped {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := d.restartStopped(ctx, target, guard, exactRestart); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proofCtx, cancel := gatewayCurrentPhysicalProofContext(ctx, d.manager)
	defer cancel()
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	proof, err = d.observe(proofCtx, target)
	if err != nil || !gatewayCurrentServingRestoreProofMatches(proof, target, expected) {
		return gatewayCurrentPhysicalAttestation{}, gatewayCurrentPhysicalDriverError(ctx)
	}
	if err := guard(proofCtx); err != nil {
		return gatewayCurrentPhysicalAttestation{}, err
	}
	return proof, nil
}

var _ gatewayCurrentServingRestoreDriver = managedGatewayCurrentPhysicalDriver{}
